package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

const workspaceProjectionSchemaVersion = "sandbox-workspace-projection/v1"

type WorkspaceFileResolver interface {
	Resolve(context.Context, string, string, int64) (repository.FileBlobPointer, []byte, error)
}

type workspaceProjection struct {
	SchemaVersion            string `json:"schemaVersion"`
	ProjectID                string `json:"projectId"`
	SessionID                string `json:"sessionId"`
	SessionEpoch             uint64 `json:"sessionEpoch"`
	CandidateID              string `json:"candidateId"`
	CandidateVersion         uint64 `json:"candidateVersion"`
	CandidateJournalSequence uint64 `json:"candidateJournalSequence"`
	TreeHash                 string `json:"treeHash"`
}

type WorkspaceMaterializer struct {
	root  string
	files WorkspaceFileResolver
	locks [64]sync.Mutex
}

func NewWorkspaceMaterializer(root string, files WorkspaceFileResolver) (*WorkspaceMaterializer, error) {
	root = strings.TrimSpace(root)
	if files == nil || root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root ||
		strings.ContainsAny(root, ",\r\n\x00") {
		return nil, ErrWorkspaceInvalid
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: workspace root must be an existing real directory", ErrWorkspaceInvalid)
	}
	return &WorkspaceMaterializer{root: root, files: files}, nil
}

func (materializer *WorkspaceMaterializer) Materialize(
	ctx context.Context,
	session SessionView,
	candidate repository.CandidateWorkspace,
) (WorkspaceMount, error) {
	if materializer == nil || ctx == nil || session.SchemaVersion != SessionSchemaVersion ||
		candidate.Validate() != nil || session.ProjectID != candidate.ProjectID ||
		session.Candidate.ID != candidate.ID || session.Candidate.Version != candidate.Version ||
		session.Candidate.TreeHash != candidate.CurrentTree.TreeHash ||
		session.Candidate.SessionEpoch != candidate.SessionEpoch || session.SessionEpoch != candidate.SessionEpoch {
		return WorkspaceMount{}, ErrWorkspaceConflict
	}
	if err := session.Quota.validate(); err != nil {
		return WorkspaceMount{}, ErrWorkspaceInvalid
	}
	projection := workspaceProjection{
		SchemaVersion: workspaceProjectionSchemaVersion,
		ProjectID:     session.ProjectID, SessionID: session.ID, SessionEpoch: session.SessionEpoch,
		CandidateID: candidate.ID, CandidateVersion: candidate.Version,
		CandidateJournalSequence: candidate.JournalSequence, TreeHash: candidate.CurrentTree.TreeHash,
	}
	lock := materializer.sessionLock(session.ID)
	lock.Lock()
	defer lock.Unlock()
	finalRoot := filepath.Join(materializer.root, session.ID)
	mount := WorkspaceMount{
		SessionRoot: finalRoot,
		Workspace:   filepath.Join(finalRoot, "workspace"),
		CodexHome:   filepath.Join(finalRoot, "runtime", "codex"),
	}
	if _, err := os.Lstat(finalRoot); err == nil {
		if err := validateExistingWorkspace(mount, projection, candidate.CurrentTree); err != nil {
			return WorkspaceMount{}, err
		}
		return mount, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorkspaceMount{}, fmt.Errorf("%w: inspect existing projection: %v", ErrWorkspaceInvalid, err)
	}

	stagingRoot := filepath.Join(materializer.root, ".staging-"+session.ID+"-"+uuid.NewString())
	staging := WorkspaceMount{
		SessionRoot: stagingRoot,
		Workspace:   filepath.Join(stagingRoot, "workspace"),
		CodexHome:   filepath.Join(stagingRoot, "runtime", "codex"),
	}
	if err := os.MkdirAll(staging.Workspace, 0o700); err != nil {
		return WorkspaceMount{}, fmt.Errorf("%w: create staging workspace: %v", ErrWorkspaceInvalid, err)
	}
	defer os.RemoveAll(stagingRoot)
	if err := os.MkdirAll(staging.CodexHome, 0o700); err != nil {
		return WorkspaceMount{}, fmt.Errorf("%w: create Codex home: %v", ErrWorkspaceInvalid, err)
	}

	var total int64
	for _, file := range candidate.CurrentTree.Files {
		if err := ctx.Err(); err != nil {
			return WorkspaceMount{}, err
		}
		normalized, err := repository.NormalizePath(file.Path)
		if err != nil || normalized != file.Path {
			return WorkspaceMount{}, ErrWorkspaceConflict
		}
		total += file.ByteSize
		if total > session.Quota.WorkspaceBytes {
			return WorkspaceMount{}, fmt.Errorf("%w: Candidate exceeds workspace quota", ErrWorkspaceInvalid)
		}
		pointer, value, err := materializer.files.Resolve(ctx, session.ProjectID, file.ContentHash, file.ByteSize)
		if err != nil {
			return WorkspaceMount{}, fmt.Errorf("%w: resolve %s: %v", ErrWorkspaceInvalid, file.Path, err)
		}
		digest := sha256.Sum256(value)
		if pointer.ContentHash != file.ContentHash || pointer.ByteSize != file.ByteSize ||
			int64(len(value)) != file.ByteSize || fmt.Sprintf("sha256:%x", digest) != file.ContentHash {
			return WorkspaceMount{}, ErrWorkspaceConflict
		}
		target := filepath.Join(staging.Workspace, filepath.FromSlash(normalized))
		if !pathWithin(staging.Workspace, target) {
			return WorkspaceMount{}, ErrWorkspaceInvalid
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return WorkspaceMount{}, fmt.Errorf("%w: create parent: %v", ErrWorkspaceInvalid, err)
		}
		mode := os.FileMode(0o600)
		if file.Mode == "100755" {
			mode = 0o700
		}
		if err := writeWorkspaceFile(target, value, mode); err != nil {
			return WorkspaceMount{}, err
		}
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return WorkspaceMount{}, ErrWorkspaceInvalid
	}
	if err := writeWorkspaceFile(filepath.Join(stagingRoot, "projection.json"), encoded, 0o600); err != nil {
		return WorkspaceMount{}, err
	}
	if err := syncDirectory(staging.Workspace); err != nil {
		return WorkspaceMount{}, err
	}
	if err := syncDirectory(filepath.Join(stagingRoot, "runtime")); err != nil {
		return WorkspaceMount{}, err
	}
	if err := syncDirectory(stagingRoot); err != nil {
		return WorkspaceMount{}, err
	}
	if err := os.Rename(stagingRoot, finalRoot); err != nil {
		if existingErr := validateExistingWorkspace(mount, projection, candidate.CurrentTree); existingErr == nil {
			return mount, nil
		}
		return WorkspaceMount{}, fmt.Errorf("%w: publish workspace projection: %v", ErrWorkspaceInvalid, err)
	}
	if err := syncDirectory(materializer.root); err != nil {
		return WorkspaceMount{}, err
	}
	return mount, nil
}

// RebindSession atomically moves an exact, stopped workspace projection to a
// newly rotated session epoch. The Candidate session rotation is allowed to
// change only fences: the journal sequence and tree must remain byte exact.
// A retry after the marker was replaced is idempotent.
func (materializer *WorkspaceMaterializer) RebindSession(
	ctx context.Context,
	session SessionView,
	candidate repository.CandidateWorkspace,
) (WorkspaceMount, error) {
	if materializer == nil || ctx == nil || session.SchemaVersion != SessionSchemaVersion ||
		candidate.Validate() != nil || !candidateProjectionMatches(session.Candidate, candidate) ||
		session.ProjectID != candidate.ProjectID || session.SessionEpoch != candidate.SessionEpoch {
		return WorkspaceMount{}, ErrWorkspaceConflict
	}
	lock := materializer.sessionLock(session.ID)
	lock.Lock()
	defer lock.Unlock()
	mount := materializer.mount(session.ID)
	actual, err := loadWorkspaceProjection(mount)
	if err != nil {
		return WorkspaceMount{}, err
	}
	expected := workspaceProjection{
		SchemaVersion: workspaceProjectionSchemaVersion,
		ProjectID:     session.ProjectID, SessionID: session.ID, SessionEpoch: session.SessionEpoch,
		CandidateID: candidate.ID, CandidateVersion: candidate.Version,
		CandidateJournalSequence: candidate.JournalSequence, TreeHash: candidate.CurrentTree.TreeHash,
	}
	if sameWorkspaceProjection(actual, expected) && actual.CandidateVersion == expected.CandidateVersion {
		if err := verifyWorkspaceTree(ctx, mount.Workspace, candidate.CurrentTree); err != nil {
			return WorkspaceMount{}, err
		}
		return mount, nil
	}
	if actual.SchemaVersion != workspaceProjectionSchemaVersion || actual.ProjectID != expected.ProjectID ||
		actual.SessionID != expected.SessionID || actual.CandidateID != expected.CandidateID ||
		actual.SessionEpoch+1 != expected.SessionEpoch || actual.CandidateVersion >= expected.CandidateVersion ||
		actual.CandidateJournalSequence != expected.CandidateJournalSequence || actual.TreeHash != expected.TreeHash {
		return WorkspaceMount{}, ErrWorkspaceConflict
	}
	if err := verifyWorkspaceTree(ctx, mount.Workspace, candidate.CurrentTree); err != nil {
		return WorkspaceMount{}, err
	}
	if err := replaceWorkspaceProjection(mount, expected); err != nil {
		return WorkspaceMount{}, err
	}
	return mount, nil
}

// ExistingMount returns the stable mount identity without trusting a
// projection marker. It is used only for idempotent runtime cleanup.
func (materializer *WorkspaceMaterializer) ExistingMount(sessionID string) (WorkspaceMount, bool, error) {
	if materializer == nil || !validUUID(sessionID) {
		return WorkspaceMount{}, false, ErrWorkspaceInvalid
	}
	lock := materializer.sessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()
	mount := materializer.mount(sessionID)
	info, err := os.Lstat(mount.SessionRoot)
	if errors.Is(err, os.ErrNotExist) {
		return WorkspaceMount{}, false, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return WorkspaceMount{}, false, ErrWorkspaceConflict
	}
	for _, directory := range []string{mount.Workspace, mount.CodexHome} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return WorkspaceMount{}, false, ErrWorkspaceConflict
		}
	}
	return mount, true, nil
}

// Remove deletes only the UUID-named session projection after its runtime has
// been terminated. Immutable Candidate checkpoints remain in repository CAS.
func (materializer *WorkspaceMaterializer) Remove(sessionID string) error {
	if materializer == nil || !validUUID(sessionID) {
		return ErrWorkspaceInvalid
	}
	lock := materializer.sessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()
	root := filepath.Join(materializer.root, sessionID)
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !pathWithin(materializer.root, root) {
		return ErrWorkspaceConflict
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("%w: remove terminated workspace: %v", ErrWorkspaceInvalid, err)
	}
	return syncDirectory(materializer.root)
}

func (materializer *WorkspaceMaterializer) mount(sessionID string) WorkspaceMount {
	root := filepath.Join(materializer.root, sessionID)
	return WorkspaceMount{
		SessionRoot: root,
		Workspace:   filepath.Join(root, "workspace"),
		CodexHome:   filepath.Join(root, "runtime", "codex"),
	}
}

func validateExistingWorkspace(
	mount WorkspaceMount,
	expected workspaceProjection,
	tree repository.TreeManifest,
) error {
	for _, directory := range []string{mount.SessionRoot, mount.Workspace, mount.CodexHome} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrWorkspaceConflict
		}
	}
	projectionPath := filepath.Join(mount.SessionRoot, "projection.json")
	info, err := os.Lstat(projectionPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 2 || info.Size() > 8<<10 {
		return ErrWorkspaceConflict
	}
	file, err := os.Open(projectionPath)
	if err != nil {
		return ErrWorkspaceConflict
	}
	defer file.Close()
	actual, err := decodeWorkspaceProjection(file)
	if err != nil || !sameWorkspaceProjection(actual, expected) ||
		actual.CandidateVersion > expected.CandidateVersion {
		return ErrWorkspaceConflict
	}
	return verifyWorkspaceTree(context.Background(), mount.Workspace, tree)
}

func verifyWorkspaceTree(ctx context.Context, root string, tree repository.TreeManifest) error {
	if ctx == nil {
		return ErrWorkspaceConflict
	}
	if _, err := repository.ParseTree(tree); err != nil {
		return ErrWorkspaceConflict
	}
	expectedFiles := make(map[string]repository.TreeFile, len(tree.Files))
	expectedDirectories := map[string]bool{".": true}
	for _, file := range tree.Files {
		expectedFiles[file.Path] = file
		for directory := filepath.Dir(filepath.FromSlash(file.Path)); directory != "."; directory = filepath.Dir(directory) {
			expectedDirectories[filepath.ToSlash(directory)] = true
		}
	}
	seen := make(map[string]bool, len(expectedFiles))
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrWorkspaceConflict
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || filepath.IsAbs(relative) || relative == ".." ||
			strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return ErrWorkspaceConflict
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrWorkspaceConflict
		}
		if entry.IsDir() {
			if !expectedDirectories[relative] {
				return ErrWorkspaceConflict
			}
			return nil
		}
		expected, ok := expectedFiles[relative]
		if !ok || entry.Type()&os.ModeType != 0 {
			return ErrWorkspaceConflict
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() != expected.ByteSize {
			return ErrWorkspaceConflict
		}
		mode := "100644"
		if info.Mode().Perm()&0o111 != 0 {
			mode = "100755"
		}
		if mode != expected.Mode {
			return ErrWorkspaceConflict
		}
		file, err := os.Open(path)
		if err != nil {
			return ErrWorkspaceConflict
		}
		hash := sha256.New()
		written, copyErr := io.Copy(hash, io.LimitReader(file, repository.MaxFileBytes+1))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || written != expected.ByteSize ||
			fmt.Sprintf("sha256:%x", hash.Sum(nil)) != expected.ContentHash {
			return ErrWorkspaceConflict
		}
		seen[relative] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(expectedFiles) {
		return ErrWorkspaceConflict
	}
	return nil
}

func decodeWorkspaceProjection(reader io.Reader) (workspaceProjection, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 8<<10))
	decoder.DisallowUnknownFields()
	var projection workspaceProjection
	if err := decoder.Decode(&projection); err != nil {
		return workspaceProjection{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return workspaceProjection{}, ErrWorkspaceConflict
	}
	return projection, nil
}

func sameWorkspaceProjection(left, right workspaceProjection) bool {
	return left.SchemaVersion == right.SchemaVersion && left.ProjectID == right.ProjectID &&
		left.SessionID == right.SessionID && left.SessionEpoch == right.SessionEpoch &&
		left.CandidateID == right.CandidateID &&
		left.CandidateJournalSequence == right.CandidateJournalSequence && left.TreeHash == right.TreeHash
}

func (materializer *WorkspaceMaterializer) sessionLock(sessionID string) *sync.Mutex {
	var hash uint32 = 2166136261
	for index := 0; index < len(sessionID); index++ {
		hash ^= uint32(sessionID[index])
		hash *= 16777619
	}
	return &materializer.locks[hash%uint32(len(materializer.locks))]
}

func writeWorkspaceFile(path string, value []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("%w: create file: %v", ErrWorkspaceInvalid, err)
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(value); err != nil {
		return fmt.Errorf("%w: write file: %v", ErrWorkspaceInvalid, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("%w: sync file: %v", ErrWorkspaceInvalid, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: close file: %v", ErrWorkspaceInvalid, err)
	}
	complete = true
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: open directory: %v", ErrWorkspaceInvalid, err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("%w: sync directory: %v", ErrWorkspaceInvalid, err)
	}
	return nil
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) &&
		!strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
