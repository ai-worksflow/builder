package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/worksflow/builder/backend/internal/repository"
)

var (
	ErrExecutionBlocked = errors.New("Agent execution is blocked")
	ErrExecutionDrift   = errors.New("Agent execution source changed")
	ErrPatchPolicy      = errors.New("Agent patch violates the TaskCapsule path policy")
)

type WorktreeFileResolver interface {
	Resolve(context.Context, string, string, int64) (repository.FileBlobPointer, []byte, error)
}

type WorktreeLease struct {
	AttemptID string
	Fence     uint64
	Root      string
	Workspace string
	Input     string
	Output    string
}

type WorktreeManager struct {
	root  string
	files WorktreeFileResolver
}

func NewWorktreeManager(root string, files WorktreeFileResolver) (*WorktreeManager, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if files == nil || !filepath.IsAbs(root) || root == string(filepath.Separator) ||
		strings.ContainsAny(root, ",\r\n\x00") {
		return nil, fmt.Errorf("%w: an absolute isolated worktree root and file resolver are required", ErrExecutionBlocked)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create worktree root: %v", ErrExecutionBlocked, err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("%w: secure worktree root: %v", ErrExecutionBlocked, err)
	}
	return &WorktreeManager{root: root, files: files}, nil
}

func (manager *WorktreeManager) Prepare(
	ctx context.Context,
	projectID, attemptID string,
	fence uint64,
	tree repository.TreeManifest,
) (WorktreeLease, error) {
	if manager == nil || ctx == nil || !validUUIDs(projectID, attemptID) || fence == 0 {
		return WorktreeLease{}, fmt.Errorf("%w: worktree identity", ErrExecutionBlocked)
	}
	tree, err := repository.ParseTree(tree)
	if err != nil {
		return WorktreeLease{}, fmt.Errorf("%w: exact base tree: %v", ErrExecutionDrift, err)
	}
	root := filepath.Join(manager.root, attemptID, fmt.Sprintf("fence-%d", fence))
	if err := ensureDescendant(manager.root, root); err != nil {
		return WorktreeLease{}, err
	}
	if err := os.RemoveAll(root); err != nil {
		return WorktreeLease{}, fmt.Errorf("%w: clean fenced worktree: %v", ErrExecutionBlocked, err)
	}
	lease := WorktreeLease{
		AttemptID: attemptID, Fence: fence, Root: root,
		Workspace: filepath.Join(root, "workspace"),
		Input:     filepath.Join(root, "input"),
		Output:    filepath.Join(root, "output"),
	}
	for _, directory := range []string{lease.Workspace, lease.Input, lease.Output} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			_ = os.RemoveAll(root)
			return WorktreeLease{}, fmt.Errorf("%w: create fenced worktree: %v", ErrExecutionBlocked, err)
		}
	}
	for _, file := range tree.Files {
		if err := ctx.Err(); err != nil {
			_ = os.RemoveAll(root)
			return WorktreeLease{}, err
		}
		pointer, value, resolveErr := manager.files.Resolve(ctx, projectID, file.ContentHash, file.ByteSize)
		if resolveErr != nil {
			_ = os.RemoveAll(root)
			return WorktreeLease{}, fmt.Errorf("%w: resolve base file %s: %v", ErrExecutionBlocked, file.Path, resolveErr)
		}
		if pointer.Store != repository.FileContentStore || pointer.ContentHash != file.ContentHash ||
			pointer.ByteSize != file.ByteSize || int64(len(value)) != file.ByteSize ||
			rawWorktreeHash(value) != file.ContentHash {
			_ = os.RemoveAll(root)
			return WorktreeLease{}, fmt.Errorf("%w: base file %s content", ErrExecutionDrift, file.Path)
		}
		target, pathErr := secureWorkspacePath(lease.Workspace, file.Path)
		if pathErr != nil {
			_ = os.RemoveAll(root)
			return WorktreeLease{}, pathErr
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			_ = os.RemoveAll(root)
			return WorktreeLease{}, fmt.Errorf("%w: create base file directory: %v", ErrExecutionBlocked, err)
		}
		mode := fs.FileMode(0o600)
		if file.Mode == "100755" {
			mode = 0o700
		}
		if err := os.WriteFile(target, value, mode); err != nil {
			_ = os.RemoveAll(root)
			return WorktreeLease{}, fmt.Errorf("%w: write base file: %v", ErrExecutionBlocked, err)
		}
	}
	return lease, nil
}

func (manager *WorktreeManager) Cleanup(lease WorktreeLease) error {
	if manager == nil || !validUUIDs(lease.AttemptID) || lease.Fence == 0 ||
		lease.Root != filepath.Join(manager.root, lease.AttemptID, fmt.Sprintf("fence-%d", lease.Fence)) {
		return fmt.Errorf("%w: cleanup path is not a fenced worktree", ErrExecutionBlocked)
	}
	if err := ensureDescendant(manager.root, lease.Root); err != nil {
		return err
	}
	if err := os.RemoveAll(lease.Root); err != nil {
		return fmt.Errorf("%w: clean worktree: %v", ErrExecutionBlocked, err)
	}
	return nil
}

type CapturedFileChange struct {
	Operation repository.FileOperation
	Content   []byte
}

type CapturedPatch struct {
	BaseTree     repository.TreeManifest
	ProposedTree repository.TreeManifest
	Changes      []CapturedFileChange
	ChangedBytes int64
}

func CaptureWorktreePatch(
	workspace string,
	base repository.TreeManifest,
	writeSet, protectedPaths []string,
	maxPatchBytes int64,
) (CapturedPatch, error) {
	base, err := repository.ParseTree(base)
	if err != nil {
		return CapturedPatch{}, fmt.Errorf("%w: base tree: %v", ErrExecutionDrift, err)
	}
	if !filepath.IsAbs(workspace) || maxPatchBytes < 1 || maxPatchBytes > repository.MaxTreeBytes ||
		len(writeSet) == 0 || len(protectedPaths) == 0 {
		return CapturedPatch{}, fmt.Errorf("%w: patch capture bounds", ErrExecutionBlocked)
	}
	afterFiles, contentByPath, err := scanWorktree(workspace)
	if err != nil {
		return CapturedPatch{}, err
	}
	proposed, err := repository.NewTree(afterFiles)
	if err != nil {
		return CapturedPatch{}, fmt.Errorf("%w: proposed tree: %v", ErrPatchPolicy, err)
	}
	baseByPath := make(map[string]repository.TreeFile, len(base.Files))
	afterByPath := make(map[string]repository.TreeFile, len(proposed.Files))
	paths := make(map[string]struct{}, len(base.Files)+len(proposed.Files))
	for _, file := range base.Files {
		baseByPath[file.Path], paths[file.Path] = file, struct{}{}
	}
	for _, file := range proposed.Files {
		afterByPath[file.Path], paths[file.Path] = file, struct{}{}
	}
	ordered := make([]string, 0, len(paths))
	for filePath := range paths {
		ordered = append(ordered, filePath)
	}
	sort.Strings(ordered)
	changes := make([]CapturedFileChange, 0)
	changedBytes := int64(0)
	for _, filePath := range ordered {
		before, existedBefore := baseByPath[filePath]
		after, existsAfter := afterByPath[filePath]
		if existedBefore && existsAfter && before == after {
			continue
		}
		if pathInPolicySet(filePath, protectedPaths) || !pathInPolicySet(filePath, writeSet) {
			return CapturedPatch{}, fmt.Errorf("%w: %s", ErrPatchPolicy, filePath)
		}
		operation := repository.FileOperation{
			ID: "capture:" + rawWorktreeHash([]byte(filePath)), Path: filePath,
		}
		var content []byte
		if !existsAfter {
			operation.Kind = repository.OperationDelete
			operation.ExpectedHash = before.ContentHash
		} else {
			operation.Kind = repository.OperationUpsert
			if existedBefore {
				operation.ExpectedHash = before.ContentHash
			}
			operation.ContentHash = after.ContentHash
			operation.ByteSize = after.ByteSize
			operation.Mode = after.Mode
			content = append([]byte(nil), contentByPath[filePath]...)
			changedBytes += int64(len(content))
			if changedBytes > maxPatchBytes {
				return CapturedPatch{}, fmt.Errorf("%w: changed content exceeds the TaskCapsule patch budget", ErrPatchPolicy)
			}
		}
		normalized, normalizeErr := repository.NormalizeOperation(operation)
		if normalizeErr != nil {
			return CapturedPatch{}, fmt.Errorf("%w: normalize %s: %v", ErrPatchPolicy, filePath, normalizeErr)
		}
		changes = append(changes, CapturedFileChange{Operation: normalized, Content: content})
		if len(changes) > MaxPlatformPatchOperations {
			return CapturedPatch{}, fmt.Errorf("%w: changed path count exceeds the qualified output bound", ErrPatchPolicy)
		}
	}
	if len(changes) == 0 {
		return CapturedPatch{}, fmt.Errorf("%w: Runner produced no repository change", ErrPatchPolicy)
	}
	projected := base
	for _, change := range changes {
		projected, err = repository.ApplyOperation(projected, change.Operation)
		if err != nil {
			return CapturedPatch{}, fmt.Errorf("%w: project captured operation: %v", ErrExecutionDrift, err)
		}
	}
	if projected.TreeHash != proposed.TreeHash {
		return CapturedPatch{}, fmt.Errorf("%w: captured operations do not reproduce the scanned tree", ErrExecutionDrift)
	}
	return CapturedPatch{
		BaseTree: base, ProposedTree: proposed, Changes: changes, ChangedBytes: changedBytes,
	}, nil
}

func scanWorktree(workspace string) ([]repository.TreeFile, map[string][]byte, error) {
	root := filepath.Clean(workspace)
	files := make([]repository.TreeFile, 0)
	content := make(map[string][]byte)
	total := int64(0)
	err := filepath.WalkDir(root, func(target string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if target == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!entry.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("%w: worktree contains a symlink or special file", ErrPatchPolicy)
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, target)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		normalized, err := repository.NormalizePath(relative)
		if err != nil || normalized != relative {
			return fmt.Errorf("%w: unsafe output path %s", ErrPatchPolicy, relative)
		}
		if info.Size() < 0 || info.Size() > repository.MaxFileBytes {
			return fmt.Errorf("%w: output file %s exceeds the repository file limit", ErrPatchPolicy, relative)
		}
		value, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		if int64(len(value)) != info.Size() {
			return fmt.Errorf("%w: output file %s changed while being captured", ErrExecutionDrift, relative)
		}
		total += int64(len(value))
		if total > repository.MaxTreeBytes || len(files) >= repository.MaxTreeFiles {
			return fmt.Errorf("%w: output tree exceeds repository limits", ErrPatchPolicy)
		}
		mode := "100644"
		if info.Mode().Perm()&0o111 != 0 {
			mode = "100755"
		}
		files = append(files, repository.TreeFile{
			Path: relative, Mode: mode, ContentHash: rawWorktreeHash(value), ByteSize: int64(len(value)),
		})
		content[relative] = value
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: scan worktree: %v", ErrPatchPolicy, err)
	}
	return files, content, nil
}

func rawWorktreeHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func pathInPolicySet(target string, roots []string) bool {
	target = strings.ToLower(target)
	for _, root := range roots {
		root = strings.ToLower(root)
		if target == root || strings.HasPrefix(target, root+"/") {
			return true
		}
	}
	return false
}

func secureWorkspacePath(root, relative string) (string, error) {
	normalized, err := repository.NormalizePath(relative)
	if err != nil || normalized != relative {
		return "", fmt.Errorf("%w: unsafe workspace path", ErrExecutionDrift)
	}
	target := filepath.Join(root, filepath.FromSlash(normalized))
	if err := ensureDescendant(root, target); err != nil {
		return "", err
	}
	return target, nil
}

func ensureDescendant(root, target string) error {
	root, target = filepath.Clean(root), filepath.Clean(target)
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: path escaped its isolated root", ErrExecutionBlocked)
	}
	return nil
}
