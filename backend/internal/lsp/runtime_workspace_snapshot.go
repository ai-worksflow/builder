package lsp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/sandbox"
)

const (
	runtimeWorkspaceSnapshotSchema = "lsp-runtime-workspace-snapshot/v1"
	runtimeWorkspaceSnapshotLimit  = int64(8 << 10)
	runtimeWorkspaceMinimumQuota   = int64(1 << 20)
	runtimeWorkspaceMaximumQuota   = int64(1 << 40)
)

var (
	ErrRuntimeWorkspaceSnapshotInvalid     = errors.New("invalid LSP runtime workspace snapshot authority")
	ErrRuntimeWorkspaceSnapshotConflict    = errors.New("LSP runtime workspace snapshot conflicts with exact Candidate authority")
	ErrRuntimeWorkspaceSnapshotUnavailable = errors.New("LSP runtime workspace snapshot is unavailable")
)

// RuntimeWorkspaceSnapshotMaterializer projects the exact Candidate tree into
// an immutable LSP-only directory. It never reads or returns the mutable
// <session>/workspace used by terminals and Sandbox processes.
type RuntimeWorkspaceSnapshotMaterializer struct {
	root     string
	rootInfo os.FileInfo
	files    RuntimeBindingFiles
	locks    [64]sync.Mutex
}

type runtimeWorkspaceSnapshotProjection struct {
	// Candidate version and journal sequence are intentionally not part of the
	// persisted tree identity: a later exact CAS head may return A -> B -> A.
	// Materialize validates those counters against SessionView on every call,
	// then reuses A only after hashing every published byte against tree A.
	SchemaVersion string `json:"schemaVersion"`
	ProjectID     string `json:"projectId"`
	SessionID     string `json:"sessionId"`
	SessionEpoch  uint64 `json:"sessionEpoch"`
	CandidateID   string `json:"candidateId"`
	TreeHash      string `json:"treeHash"`
}

// NewRuntimeWorkspaceSnapshotMaterializer binds snapshot publication to one
// canonical Sandbox workspace base root and the tenant-scoped repository blob
// resolver. The base root must already exist and must not be a symlink.
func NewRuntimeWorkspaceSnapshotMaterializer(
	root string,
	files RuntimeBindingFiles,
) (*RuntimeWorkspaceSnapshotMaterializer, error) {
	if files == nil || root == "" || strings.TrimSpace(root) != root || !filepath.IsAbs(root) ||
		filepath.Clean(root) != root || len(root) > 4096 || strings.ContainsAny(root, ",\x00\r\n") {
		return nil, ErrRuntimeWorkspaceSnapshotInvalid
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%w: workspace base root", ErrRuntimeWorkspaceSnapshotInvalid)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return nil, fmt.Errorf("%w: non-canonical workspace base root", ErrRuntimeWorkspaceSnapshotInvalid)
	}
	return &RuntimeWorkspaceSnapshotMaterializer{root: root, rootInfo: info, files: files}, nil
}

// Materialize builds or fully re-verifies one immutable exact-tree snapshot.
// New heads that return to the same tree safely reuse its verified bytes; a
// different tree receives a different path and never replaces an old view.
func (materializer *RuntimeWorkspaceSnapshotMaterializer) Materialize(
	ctx context.Context,
	session sandbox.SessionView,
	candidate repository.CandidateWorkspace,
) (result sandbox.WorkspaceMount, resultErr error) {
	if materializer == nil || ctx == nil {
		return sandbox.WorkspaceMount{}, ErrRuntimeWorkspaceSnapshotInvalid
	}
	tree, projection, err := validateRuntimeWorkspaceSnapshotInput(session, candidate)
	if err != nil {
		return sandbox.WorkspaceMount{}, err
	}
	if err := ctx.Err(); err != nil {
		return sandbox.WorkspaceMount{}, err
	}
	lock := materializer.sessionLock(session.ID)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return sandbox.WorkspaceMount{}, err
	}

	sessionRoot, snapshotsRoot, err := materializer.prepareSessionRoot(session.ID)
	if err != nil {
		return sandbox.WorkspaceMount{}, err
	}
	mount := runtimeWorkspaceSnapshotMount(sessionRoot, tree.TreeHash)
	if info, inspectErr := os.Lstat(mount.SessionRoot); inspectErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return sandbox.WorkspaceMount{}, ErrRuntimeWorkspaceSnapshotConflict
		}
		if err := verifyRuntimeWorkspaceSnapshot(ctx, mount, projection, tree); err != nil {
			return sandbox.WorkspaceMount{}, err
		}
		return mount, nil
	} else if !errors.Is(inspectErr, os.ErrNotExist) {
		return sandbox.WorkspaceMount{}, fmt.Errorf(
			"%w: inspect exact snapshot: %v", ErrRuntimeWorkspaceSnapshotUnavailable, inspectErr,
		)
	}

	stagingRoot, err := os.MkdirTemp(snapshotsRoot, ".staging-")
	if err != nil {
		return sandbox.WorkspaceMount{}, fmt.Errorf(
			"%w: create snapshot staging root: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err,
		)
	}
	staging := sandbox.WorkspaceMount{
		SessionRoot: stagingRoot,
		Workspace:   filepath.Join(stagingRoot, "workspace"),
		CodexHome:   filepath.Join(stagingRoot, "runtime", "codex"),
	}
	published := false
	defer func() {
		if !published {
			if cleanupErr := removeRuntimeWorkspaceSnapshot(stagingRoot); cleanupErr != nil {
				result = sandbox.WorkspaceMount{}
				resultErr = errors.Join(resultErr, cleanupErr)
			}
		}
	}()
	if err := materializer.buildSnapshot(ctx, staging, session, tree, projection); err != nil {
		return sandbox.WorkspaceMount{}, err
	}
	if err := ctx.Err(); err != nil {
		return sandbox.WorkspaceMount{}, err
	}

	err = unix.Renameat2(
		unix.AT_FDCWD, stagingRoot,
		unix.AT_FDCWD, mount.SessionRoot,
		unix.RENAME_NOREPLACE,
	)
	if err != nil {
		if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOTEMPTY) {
			if verifyErr := verifyRuntimeWorkspaceSnapshot(ctx, mount, projection, tree); verifyErr != nil {
				return sandbox.WorkspaceMount{}, verifyErr
			}
			return mount, nil
		}
		return sandbox.WorkspaceMount{}, fmt.Errorf(
			"%w: atomic no-replace snapshot publish: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err,
		)
	}
	published = true
	if err := syncRuntimeWorkspaceDirectory(snapshotsRoot); err != nil {
		return sandbox.WorkspaceMount{}, err
	}
	if err := verifyRuntimeWorkspaceSnapshot(ctx, mount, projection, tree); err != nil {
		return sandbox.WorkspaceMount{}, err
	}
	return mount, nil
}

func validateRuntimeWorkspaceSnapshotInput(
	session sandbox.SessionView,
	candidate repository.CandidateWorkspace,
) (repository.TreeManifest, runtimeWorkspaceSnapshotProjection, error) {
	if session.SchemaVersion != sandbox.SessionSchemaVersion || session.State != sandbox.StateReady ||
		!canonicalUUID(session.ID) || !canonicalUUID(session.ProjectID) || candidate.Validate() != nil ||
		session.ProjectID != candidate.ProjectID || session.SessionEpoch != candidate.SessionEpoch ||
		session.Quota.WorkspaceBytes < runtimeWorkspaceMinimumQuota ||
		session.Quota.WorkspaceBytes > runtimeWorkspaceMaximumQuota {
		return repository.TreeManifest{}, runtimeWorkspaceSnapshotProjection{}, ErrRuntimeWorkspaceSnapshotConflict
	}
	projected := session.Candidate
	if projected.ID != candidate.ID || projected.RepositorySnapshotID != candidate.RepositorySnapshotID ||
		projected.Status != candidate.Status || projected.BaseTreeHash != candidate.BaseTreeHash ||
		projected.TreeHash != candidate.CurrentTree.TreeHash || projected.Version != candidate.Version ||
		projected.JournalSequence != candidate.JournalSequence || projected.SessionEpoch != candidate.SessionEpoch ||
		projected.WriterLeaseEpoch != candidate.WriterLeaseEpoch || projected.Dirty != candidate.Dirty ||
		projected.Conflicted != candidate.Conflicted || projected.Stale != candidate.Stale ||
		projected.RebaseRequired != candidate.RebaseRequired ||
		!projected.UpdatedAt.Equal(candidate.UpdatedAt.UTC()) {
		return repository.TreeManifest{}, runtimeWorkspaceSnapshotProjection{}, ErrRuntimeWorkspaceSnapshotConflict
	}
	tree, err := repository.ParseTree(candidate.CurrentTree)
	if err != nil || !digestPattern.MatchString(tree.TreeHash) {
		return repository.TreeManifest{}, runtimeWorkspaceSnapshotProjection{}, ErrRuntimeWorkspaceSnapshotConflict
	}
	remaining := session.Quota.WorkspaceBytes
	for _, file := range tree.Files {
		if file.ByteSize < 0 || file.ByteSize > remaining {
			return repository.TreeManifest{}, runtimeWorkspaceSnapshotProjection{}, fmt.Errorf(
				"%w: exact Candidate exceeds session workspace quota", ErrRuntimeWorkspaceSnapshotConflict,
			)
		}
		remaining -= file.ByteSize
	}
	return tree, runtimeWorkspaceSnapshotProjection{
		SchemaVersion: runtimeWorkspaceSnapshotSchema,
		ProjectID:     session.ProjectID, SessionID: session.ID, SessionEpoch: session.SessionEpoch,
		CandidateID: candidate.ID, TreeHash: tree.TreeHash,
	}, nil
}

func (materializer *RuntimeWorkspaceSnapshotMaterializer) prepareSessionRoot(
	sessionID string,
) (string, string, error) {
	if err := materializer.revalidateBaseRoot(); err != nil {
		return "", "", err
	}
	sessionRoot := filepath.Join(materializer.root, sessionID)
	if !runtimeSnapshotPathWithin(materializer.root, sessionRoot) {
		return "", "", ErrRuntimeWorkspaceSnapshotConflict
	}
	if err := validateRuntimeSnapshotDirectory(sessionRoot, 0, false); err != nil {
		return "", "", fmt.Errorf("%w: session root", ErrRuntimeWorkspaceSnapshotConflict)
	}
	runtimeRoot := filepath.Join(sessionRoot, "runtime")
	if err := validateRuntimeSnapshotDirectory(runtimeRoot, 0, false); err != nil {
		return "", "", fmt.Errorf("%w: session runtime root", ErrRuntimeWorkspaceSnapshotConflict)
	}
	snapshotsRoot := filepath.Join(runtimeRoot, "lsp-snapshots")
	if err := os.Mkdir(snapshotsRoot, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", "", fmt.Errorf(
			"%w: create snapshot namespace: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err,
		)
	}
	if err := validateRuntimeSnapshotDirectory(snapshotsRoot, 0o700, true); err != nil {
		return "", "", err
	}
	if err := syncRuntimeWorkspaceDirectory(runtimeRoot); err != nil {
		return "", "", err
	}
	return sessionRoot, snapshotsRoot, nil
}

func (materializer *RuntimeWorkspaceSnapshotMaterializer) revalidateBaseRoot() error {
	info, err := os.Lstat(materializer.root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(materializer.rootInfo, info) || info.Mode() != materializer.rootInfo.Mode() {
		return ErrRuntimeWorkspaceSnapshotConflict
	}
	resolved, err := filepath.EvalSymlinks(materializer.root)
	if err != nil || resolved != materializer.root {
		return ErrRuntimeWorkspaceSnapshotConflict
	}
	return nil
}

func (materializer *RuntimeWorkspaceSnapshotMaterializer) buildSnapshot(
	ctx context.Context,
	mount sandbox.WorkspaceMount,
	session sandbox.SessionView,
	tree repository.TreeManifest,
	projection runtimeWorkspaceSnapshotProjection,
) error {
	if err := os.Mkdir(mount.Workspace, 0o700); err != nil {
		return fmt.Errorf("%w: create staged workspace: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err)
	}
	runtimeRoot := filepath.Dir(mount.CodexHome)
	if err := os.Mkdir(runtimeRoot, 0o700); err != nil {
		return fmt.Errorf("%w: create staged runtime root: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err)
	}
	if err := os.Mkdir(mount.CodexHome, 0o700); err != nil {
		return fmt.Errorf("%w: create staged Codex placeholder: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err)
	}

	directories := runtimeWorkspaceTreeDirectories(tree)
	for _, relative := range directories {
		if err := ctx.Err(); err != nil {
			return err
		}
		if relative == "." {
			continue
		}
		target := filepath.Join(mount.Workspace, filepath.FromSlash(relative))
		if !runtimeSnapshotPathWithin(mount.Workspace, target) {
			return ErrRuntimeWorkspaceSnapshotConflict
		}
		if err := os.Mkdir(target, 0o700); err != nil {
			return fmt.Errorf("%w: create exact tree directory: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err)
		}
	}

	for _, file := range tree.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		pointer, value, err := materializer.files.Resolve(
			ctx, session.ProjectID, file.ContentHash, file.ByteSize,
		)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf(
				"%w: resolve exact Candidate blob for %s: %v",
				ErrRuntimeWorkspaceSnapshotUnavailable, file.Path, err,
			)
		}
		if err := verifyRuntimeSnapshotBlob(pointer, value, file); err != nil {
			return err
		}
		target := filepath.Join(mount.Workspace, filepath.FromSlash(file.Path))
		if !runtimeSnapshotPathWithin(mount.Workspace, target) {
			return ErrRuntimeWorkspaceSnapshotConflict
		}
		mode := os.FileMode(0o444)
		if file.Mode == "100755" {
			mode = 0o555
		}
		if err := writeRuntimeSnapshotFile(target, value, mode); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	encoded, err := json.Marshal(projection)
	if err != nil || len(encoded) == 0 || int64(len(encoded)) > runtimeWorkspaceSnapshotLimit {
		return ErrRuntimeWorkspaceSnapshotConflict
	}
	if err := writeRuntimeSnapshotFile(
		filepath.Join(mount.SessionRoot, "projection.json"), encoded, 0o444,
	); err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		directory := mount.Workspace
		if directories[index] != "." {
			directory = filepath.Join(mount.Workspace, filepath.FromSlash(directories[index]))
		}
		if err := sealRuntimeSnapshotDirectory(directory); err != nil {
			return err
		}
	}
	for _, directory := range []string{mount.CodexHome, runtimeRoot, mount.SessionRoot} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := sealRuntimeSnapshotDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func verifyRuntimeSnapshotBlob(
	pointer repository.FileBlobPointer,
	value []byte,
	expected repository.TreeFile,
) error {
	if pointer.Store != repository.FileContentStore || pointer.Ref == "" ||
		strings.TrimSpace(pointer.Ref) != pointer.Ref || len(pointer.Ref) > 512 ||
		!canonicalUUID(pointer.OwnerID) || pointer.ContentHash != expected.ContentHash ||
		pointer.ByteSize != expected.ByteSize || !digestPattern.MatchString(pointer.ContentObjectHash) ||
		int64(len(value)) != expected.ByteSize {
		return ErrRuntimeWorkspaceSnapshotConflict
	}
	digest := sha256.Sum256(value)
	if fmt.Sprintf("sha256:%x", digest[:]) != expected.ContentHash {
		return ErrRuntimeWorkspaceSnapshotConflict
	}
	return nil
}

func verifyRuntimeWorkspaceSnapshot(
	ctx context.Context,
	mount sandbox.WorkspaceMount,
	expected runtimeWorkspaceSnapshotProjection,
	tree repository.TreeManifest,
) error {
	if ctx == nil {
		return ErrRuntimeWorkspaceSnapshotInvalid
	}
	for _, directory := range []string{
		mount.SessionRoot, mount.Workspace, filepath.Dir(mount.CodexHome), mount.CodexHome,
	} {
		if err := validateRuntimeSnapshotDirectory(directory, 0o555, true); err != nil {
			return ErrRuntimeWorkspaceSnapshotConflict
		}
	}
	if !runtimeSnapshotDirectoryEntries(mount.SessionRoot, "projection.json", "runtime", "workspace") ||
		!runtimeSnapshotDirectoryEntries(filepath.Dir(mount.CodexHome), "codex") ||
		!runtimeSnapshotDirectoryEntries(mount.CodexHome) {
		return ErrRuntimeWorkspaceSnapshotConflict
	}
	projectionBytes, err := readRuntimeSnapshotFile(
		ctx, filepath.Join(mount.SessionRoot, "projection.json"), 0o444,
		1, runtimeWorkspaceSnapshotLimit,
	)
	if err != nil {
		return err
	}
	actual, err := decodeRuntimeWorkspaceSnapshotProjection(projectionBytes)
	if err != nil || actual != expected {
		return ErrRuntimeWorkspaceSnapshotConflict
	}
	return verifyRuntimeWorkspaceTree(ctx, mount.Workspace, tree)
}

func verifyRuntimeWorkspaceTree(
	ctx context.Context,
	root string,
	tree repository.TreeManifest,
) error {
	parsed, err := repository.ParseTree(tree)
	if err != nil {
		return ErrRuntimeWorkspaceSnapshotConflict
	}
	expectedFiles := make(map[string]repository.TreeFile, len(parsed.Files))
	for _, file := range parsed.Files {
		expectedFiles[file.Path] = file
	}
	expectedDirectories := make(map[string]bool)
	for _, directory := range runtimeWorkspaceTreeDirectories(parsed) {
		expectedDirectories[directory] = true
	}
	seenFiles := make(map[string]bool, len(expectedFiles))
	seenDirectories := make(map[string]bool, len(expectedDirectories))
	err = filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrRuntimeWorkspaceSnapshotConflict
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, current)
		if err != nil || filepath.IsAbs(relative) || relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return ErrRuntimeWorkspaceSnapshotConflict
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrRuntimeWorkspaceSnapshotConflict
		}
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrRuntimeWorkspaceSnapshotConflict
		}
		if entry.IsDir() {
			if !expectedDirectories[relative] || info.Mode().Perm() != 0o555 {
				return ErrRuntimeWorkspaceSnapshotConflict
			}
			seenDirectories[relative] = true
			return nil
		}
		expected, found := expectedFiles[relative]
		if !found || !info.Mode().IsRegular() || !runtimeSnapshotSingleLink(info) ||
			info.Size() != expected.ByteSize {
			return ErrRuntimeWorkspaceSnapshotConflict
		}
		mode := os.FileMode(0o444)
		if expected.Mode == "100755" {
			mode = 0o555
		}
		if info.Mode().Perm() != mode {
			return ErrRuntimeWorkspaceSnapshotConflict
		}
		if err := hashRuntimeSnapshotFile(ctx, current, info, expected); err != nil {
			return err
		}
		seenFiles[relative] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seenFiles) != len(expectedFiles) || len(seenDirectories) != len(expectedDirectories) {
		return ErrRuntimeWorkspaceSnapshotConflict
	}
	return nil
}

func hashRuntimeSnapshotFile(
	ctx context.Context,
	path string,
	pathInfo os.FileInfo,
	expected repository.TreeFile,
) error {
	file, err := openRuntimeSnapshotFileNoFollow(path)
	if err != nil {
		return ErrRuntimeWorkspaceSnapshotConflict
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !sameRuntimeSnapshotFile(pathInfo, openedInfo) || !runtimeSnapshotSingleLink(openedInfo) {
		return ErrRuntimeWorkspaceSnapshotConflict
	}
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			written += int64(count)
			if written > expected.ByteSize {
				return ErrRuntimeWorkspaceSnapshotConflict
			}
			_, _ = hash.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return ErrRuntimeWorkspaceSnapshotConflict
		}
	}
	afterOpen, err := file.Stat()
	afterPath, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || written != expected.ByteSize ||
		!sameRuntimeSnapshotFile(openedInfo, afterOpen) || !sameRuntimeSnapshotFile(openedInfo, afterPath) ||
		fmt.Sprintf("sha256:%x", hash.Sum(nil)) != expected.ContentHash {
		return ErrRuntimeWorkspaceSnapshotConflict
	}
	return nil
}

func readRuntimeSnapshotFile(
	ctx context.Context,
	path string,
	mode os.FileMode,
	minimum, maximum int64,
) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode ||
		!runtimeSnapshotSingleLink(info) || info.Size() < minimum || info.Size() > maximum {
		return nil, ErrRuntimeWorkspaceSnapshotConflict
	}
	file, err := openRuntimeSnapshotFileNoFollow(path)
	if err != nil {
		return nil, ErrRuntimeWorkspaceSnapshotConflict
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameRuntimeSnapshotFile(info, opened) || !runtimeSnapshotSingleLink(opened) {
		return nil, ErrRuntimeWorkspaceSnapshotConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(value)) > maximum {
		return nil, ErrRuntimeWorkspaceSnapshotConflict
	}
	afterOpen, statErr := file.Stat()
	afterPath, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || !sameRuntimeSnapshotFile(opened, afterOpen) ||
		!sameRuntimeSnapshotFile(opened, afterPath) {
		return nil, ErrRuntimeWorkspaceSnapshotConflict
	}
	return value, nil
}

func decodeRuntimeWorkspaceSnapshotProjection(
	value []byte,
) (runtimeWorkspaceSnapshotProjection, error) {
	fields, err := decodeExactObject(value, []string{
		"schemaVersion", "projectId", "sessionId", "sessionEpoch", "candidateId", "treeHash",
	})
	if err != nil {
		return runtimeWorkspaceSnapshotProjection{}, err
	}
	var result runtimeWorkspaceSnapshotProjection
	if decodeString(fields["schemaVersion"], &result.SchemaVersion) != nil ||
		decodeString(fields["projectId"], &result.ProjectID) != nil ||
		decodeString(fields["sessionId"], &result.SessionID) != nil ||
		decodeUint(fields["sessionEpoch"], &result.SessionEpoch) != nil ||
		decodeString(fields["candidateId"], &result.CandidateID) != nil ||
		decodeString(fields["treeHash"], &result.TreeHash) != nil ||
		result.SchemaVersion != runtimeWorkspaceSnapshotSchema || !canonicalUUID(result.ProjectID) ||
		!canonicalUUID(result.SessionID) || !canonicalUUID(result.CandidateID) || result.SessionEpoch == 0 ||
		!digestPattern.MatchString(result.TreeHash) {
		return runtimeWorkspaceSnapshotProjection{}, ErrRuntimeWorkspaceSnapshotConflict
	}
	return result, nil
}

func runtimeWorkspaceTreeDirectories(tree repository.TreeManifest) []string {
	directories := map[string]bool{".": true}
	for _, file := range tree.Files {
		for directory := filepath.Dir(filepath.FromSlash(file.Path)); directory != "."; directory = filepath.Dir(directory) {
			directories[filepath.ToSlash(directory)] = true
		}
	}
	result := make([]string, 0, len(directories))
	for directory := range directories {
		result = append(result, directory)
	}
	sort.Slice(result, func(left, right int) bool {
		leftDepth := strings.Count(result[left], "/")
		rightDepth := strings.Count(result[right], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return result[left] < result[right]
	})
	return result
}

func runtimeWorkspaceSnapshotMount(sessionRoot, treeHash string) sandbox.WorkspaceMount {
	root := filepath.Join(sessionRoot, "runtime", "lsp-snapshots", treeHash)
	return sandbox.WorkspaceMount{
		SessionRoot: root,
		Workspace:   filepath.Join(root, "workspace"),
		CodexHome:   filepath.Join(root, "runtime", "codex"),
	}
}

func validateRuntimeSnapshotDirectory(path string, mode os.FileMode, exactMode bool) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		(exactMode && info.Mode().Perm() != mode) || (!exactMode && info.Mode().Perm()&0o022 != 0) {
		return ErrRuntimeWorkspaceSnapshotConflict
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return ErrRuntimeWorkspaceSnapshotConflict
	}
	return nil
}

func runtimeSnapshotDirectoryEntries(path string, expected ...string) bool {
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != len(expected) {
		return false
	}
	sort.Strings(expected)
	for index, entry := range entries {
		if entry.Name() != expected[index] {
			return false
		}
	}
	return true
}

func writeRuntimeSnapshotFile(path string, value []byte, finalMode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("%w: create immutable snapshot file: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err)
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(value); err != nil {
		return fmt.Errorf("%w: write immutable snapshot file: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err)
	}
	if err := file.Chmod(finalMode); err != nil {
		return fmt.Errorf("%w: seal immutable snapshot file: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("%w: sync immutable snapshot file: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: close immutable snapshot file: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err)
	}
	complete = true
	return nil
}

func sealRuntimeSnapshotDirectory(path string) error {
	if err := os.Chmod(path, 0o555); err != nil {
		return fmt.Errorf("%w: seal snapshot directory: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err)
	}
	return syncRuntimeWorkspaceDirectory(path)
}

func syncRuntimeWorkspaceDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: open snapshot directory: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("%w: sync snapshot directory: %v", ErrRuntimeWorkspaceSnapshotUnavailable, err)
	}
	return nil
}

func openRuntimeSnapshotFileNoFollow(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

func sameRuntimeSnapshotFile(expected, actual os.FileInfo) bool {
	return expected != nil && actual != nil && os.SameFile(expected, actual) &&
		expected.Mode() == actual.Mode() && expected.Size() == actual.Size()
}

func runtimeSnapshotSingleLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}

func runtimeSnapshotPathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func removeRuntimeWorkspaceSnapshot(root string) error {
	var result error
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			result = errors.Join(result, os.Chmod(path, 0o700))
		}
		return nil
	})
	if !errors.Is(walkErr, os.ErrNotExist) {
		result = errors.Join(result, walkErr)
	}
	if err := os.RemoveAll(root); !errors.Is(err, os.ErrNotExist) {
		result = errors.Join(result, err)
	}
	if result != nil {
		return fmt.Errorf("%w: remove snapshot staging root: %v", ErrRuntimeWorkspaceSnapshotUnavailable, result)
	}
	return nil
}

func (materializer *RuntimeWorkspaceSnapshotMaterializer) sessionLock(sessionID string) *sync.Mutex {
	var hash uint32 = 2166136261
	for index := 0; index < len(sessionID); index++ {
		hash ^= uint32(sessionID[index])
		hash *= 16777619
	}
	return &materializer.locks[hash%uint32(len(materializer.locks))]
}

var _ RuntimeBindingWorkspace = (*RuntimeWorkspaceSnapshotMaterializer)(nil)
