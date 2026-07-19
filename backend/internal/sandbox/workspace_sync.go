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

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
	"golang.org/x/sys/unix"
)

var ErrWorkspaceReconciliation = errors.New("sandbox workspace mutation reconciliation is pending")

type WorkspaceMutationSynchronizer interface {
	SynchronizeMutation(
		context.Context,
		SessionView,
		repository.CandidateWorkspace,
		repository.MutationResult,
		[]byte,
	) error
}

type WorkspaceBatchMutationSynchronizer interface {
	SynchronizeBatch(
		context.Context,
		SessionView,
		repository.CandidateWorkspace,
		repository.BatchMutationResult,
	) error
}

func (materializer *WorkspaceMaterializer) SynchronizeMutation(
	ctx context.Context,
	session SessionView,
	candidate repository.CandidateWorkspace,
	mutation repository.MutationResult,
	value []byte,
) error {
	if materializer == nil || ctx == nil || session.SchemaVersion != SessionSchemaVersion ||
		candidate.Validate() != nil || session.ProjectID != candidate.ProjectID ||
		session.Candidate.ID != candidate.ID || session.SessionEpoch != candidate.SessionEpoch {
		return ErrWorkspaceConflict
	}
	entry := mutation.Entry
	operation, err := repository.NormalizeOperation(entry.Operation)
	if err != nil || operation != entry.Operation || entry.CandidateID != candidate.ID ||
		entry.SessionEpoch != session.SessionEpoch || entry.Sequence == 0 || entry.CandidateFrom == 0 ||
		entry.CandidateTo != entry.CandidateFrom+1 || candidate.Version != entry.CandidateTo ||
		candidate.JournalSequence != entry.Sequence || candidate.CurrentTree.TreeHash != entry.AfterTree ||
		mutation.BeforeTree.TreeHash != entry.BeforeTree || mutation.AfterTree.TreeHash != entry.AfterTree ||
		!validDigest(entry.BeforeTree) || !validDigest(entry.AfterTree) {
		return ErrWorkspaceConflict
	}
	afterFile, afterExists := treeFile(candidate.CurrentTree, operation.Path)
	switch operation.Kind {
	case repository.OperationUpsert:
		digest := sha256.Sum256(value)
		if value == nil || !afterExists || afterFile.ContentHash != operation.ContentHash ||
			afterFile.ByteSize != operation.ByteSize || afterFile.Mode != operation.Mode ||
			int64(len(value)) != operation.ByteSize || fmt.Sprintf("sha256:%x", digest) != operation.ContentHash {
			return ErrWorkspaceConflict
		}
	case repository.OperationDelete:
		if value != nil || afterExists {
			return ErrWorkspaceConflict
		}
	case repository.OperationRename:
		_, sourceExists := treeFile(candidate.CurrentTree, operation.FromPath)
		if value != nil || sourceExists || !afterExists || afterFile.ContentHash != operation.ExpectedHash {
			return ErrWorkspaceConflict
		}
	default:
		return ErrWorkspaceConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	lock := materializer.sessionLock(session.ID)
	lock.Lock()
	defer lock.Unlock()
	mount := WorkspaceMount{
		SessionRoot: filepath.Join(materializer.root, session.ID),
		Workspace:   filepath.Join(materializer.root, session.ID, "workspace"),
		CodexHome:   filepath.Join(materializer.root, session.ID, "runtime", "codex"),
	}
	projection, err := loadWorkspaceProjection(mount)
	if err != nil || projection.ProjectID != session.ProjectID || projection.SessionID != session.ID ||
		projection.SessionEpoch != session.SessionEpoch || projection.CandidateID != candidate.ID {
		return ErrWorkspaceConflict
	}
	afterProjection := projection
	afterProjection.CandidateVersion = candidate.Version
	afterProjection.CandidateJournalSequence = candidate.JournalSequence
	afterProjection.TreeHash = candidate.CurrentTree.TreeHash

	workspace, err := openSafeWorkspace(mount.Workspace)
	if err != nil {
		return err
	}
	defer workspace.Close()
	if sameWorkspaceProjection(projection, afterProjection) && projection.CandidateVersion == afterProjection.CandidateVersion {
		if err := workspace.verifyApplied(operation, afterFile); err != nil {
			return err
		}
		return nil
	}
	if projection.CandidateJournalSequence+1 != entry.Sequence || projection.TreeHash != entry.BeforeTree ||
		projection.CandidateVersion > entry.CandidateFrom {
		return ErrWorkspaceConflict
	}
	if err := workspace.apply(operation, afterFile, value); err != nil {
		return err
	}
	if err := workspace.verifyApplied(operation, afterFile); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceWorkspaceProjection(mount, afterProjection); err != nil {
		return err
	}
	return nil
}

// SynchronizeBatch projects an already committed atomic repository batch into
// the live workspace. projection.json advances only after every operation and
// the complete final tree verify. If the process stops between file renames,
// the unchanged before marker makes an exact retry finish the same batch.
func (materializer *WorkspaceMaterializer) SynchronizeBatch(
	ctx context.Context,
	session SessionView,
	candidate repository.CandidateWorkspace,
	mutation repository.BatchMutationResult,
) error {
	if materializer == nil || ctx == nil || session.SchemaVersion != SessionSchemaVersion ||
		candidate.Validate() != nil || session.ProjectID != candidate.ProjectID ||
		session.Candidate.ID != candidate.ID || session.SessionEpoch != candidate.SessionEpoch ||
		len(mutation.Entries) == 0 || mutation.FinalizationPending {
		return ErrWorkspaceConflict
	}
	entries := mutation.Entries
	if candidate.Version != mutation.FinalCandidateVersion ||
		candidate.JournalSequence != entries[len(entries)-1].Sequence ||
		candidate.CurrentTree.TreeHash != mutation.AfterTree.TreeHash ||
		entries[0].BeforeTree != mutation.BeforeTree.TreeHash ||
		entries[len(entries)-1].AfterTree != mutation.AfterTree.TreeHash {
		return ErrWorkspaceConflict
	}
	operations := make([]repository.FileOperation, len(entries))
	values := make([][]byte, len(entries))
	for index, entry := range entries {
		operation, err := repository.NormalizeOperation(entry.Operation)
		if err != nil || operation != entry.Operation || entry.CandidateID != candidate.ID ||
			entry.SessionEpoch != session.SessionEpoch || entry.CandidateFrom == 0 ||
			entry.CandidateTo != entry.CandidateFrom+1 || entry.Sequence == 0 {
			return ErrWorkspaceConflict
		}
		if index > 0 && (entry.CandidateFrom != entries[index-1].CandidateTo ||
			entry.Sequence != entries[index-1].Sequence+1 || entry.BeforeTree != entries[index-1].AfterTree) {
			return ErrWorkspaceConflict
		}
		after, exists := treeFile(candidate.CurrentTree, operation.Path)
		switch operation.Kind {
		case repository.OperationUpsert:
			if !exists || after.ContentHash != operation.ContentHash || after.ByteSize != operation.ByteSize ||
				after.Mode != operation.Mode {
				return ErrWorkspaceConflict
			}
			pointer, value, resolveErr := materializer.files.Resolve(
				ctx, session.ProjectID, operation.ContentHash, operation.ByteSize,
			)
			digest := sha256.Sum256(value)
			if resolveErr != nil || pointer.ContentHash != operation.ContentHash ||
				pointer.ByteSize != operation.ByteSize || int64(len(value)) != operation.ByteSize ||
				fmt.Sprintf("sha256:%x", digest) != operation.ContentHash {
				return ErrWorkspaceConflict
			}
			values[index] = value
		case repository.OperationDelete:
			if exists {
				return ErrWorkspaceConflict
			}
		default:
			return ErrWorkspaceConflict
		}
		operations[index] = operation
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	lock := materializer.sessionLock(session.ID)
	lock.Lock()
	defer lock.Unlock()
	mount := materializer.mount(session.ID)
	projection, err := loadWorkspaceProjection(mount)
	if err != nil || projection.ProjectID != session.ProjectID || projection.SessionID != session.ID ||
		projection.SessionEpoch != session.SessionEpoch || projection.CandidateID != candidate.ID {
		return ErrWorkspaceConflict
	}
	afterProjection := projection
	afterProjection.CandidateVersion = candidate.Version
	afterProjection.CandidateJournalSequence = candidate.JournalSequence
	afterProjection.TreeHash = candidate.CurrentTree.TreeHash
	if sameWorkspaceProjection(projection, afterProjection) && projection.CandidateVersion == afterProjection.CandidateVersion {
		return verifyWorkspaceTree(ctx, mount.Workspace, candidate.CurrentTree)
	}
	// Lease acquisition and other control-only Candidate events can advance the
	// aggregate version without changing the journal or tree projection.
	if projection.CandidateVersion > entries[0].CandidateFrom ||
		projection.CandidateJournalSequence+uint64(len(entries)) != entries[len(entries)-1].Sequence ||
		projection.TreeHash != mutation.BeforeTree.TreeHash {
		return ErrWorkspaceConflict
	}

	workspace, err := openSafeWorkspace(mount.Workspace)
	if err != nil {
		return err
	}
	defer workspace.Close()
	for index, operation := range operations {
		after, _ := treeFile(candidate.CurrentTree, operation.Path)
		if err := workspace.apply(operation, after, values[index]); err != nil {
			return err
		}
		if err := workspace.verifyApplied(operation, after); err != nil {
			return err
		}
	}
	if err := verifyWorkspaceTree(ctx, mount.Workspace, candidate.CurrentTree); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return replaceWorkspaceProjection(mount, afterProjection)
}

func loadWorkspaceProjection(mount WorkspaceMount) (workspaceProjection, error) {
	for _, directory := range []string{mount.SessionRoot, mount.Workspace, mount.CodexHome} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return workspaceProjection{}, ErrWorkspaceConflict
		}
	}
	path := filepath.Join(mount.SessionRoot, "projection.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 2 || info.Size() > 8<<10 {
		return workspaceProjection{}, ErrWorkspaceConflict
	}
	file, err := os.Open(path)
	if err != nil {
		return workspaceProjection{}, ErrWorkspaceConflict
	}
	defer file.Close()
	projection, err := decodeWorkspaceProjection(file)
	if err != nil || projection.SchemaVersion != workspaceProjectionSchemaVersion ||
		projection.CandidateVersion == 0 || projection.TreeHash == "" {
		return workspaceProjection{}, ErrWorkspaceConflict
	}
	return projection, nil
}

func replaceWorkspaceProjection(mount WorkspaceMount, projection workspaceProjection) error {
	encoded, err := json.Marshal(projection)
	if err != nil {
		return ErrWorkspaceInvalid
	}
	temporary := filepath.Join(mount.SessionRoot, ".projection-"+uuid.NewString()+".json")
	if err := writeWorkspaceFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, filepath.Join(mount.SessionRoot, "projection.json")); err != nil {
		return fmt.Errorf("%w: replace projection marker: %v", ErrWorkspaceInvalid, err)
	}
	return syncDirectory(mount.SessionRoot)
}

func treeFile(tree repository.TreeManifest, path string) (repository.TreeFile, bool) {
	for _, file := range tree.Files {
		if file.Path == path {
			return file, true
		}
	}
	return repository.TreeFile{}, false
}

type safeWorkspace struct {
	root int
}

type workspaceFileState struct {
	exists bool
	hash   string
	mode   string
}

func openSafeWorkspace(path string) (*safeWorkspace, error) {
	root, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: open workspace root: %v", ErrWorkspaceConflict, err)
	}
	return &safeWorkspace{root: root}, nil
}

func (workspace *safeWorkspace) Close() error {
	if workspace == nil || workspace.root < 0 {
		return nil
	}
	err := unix.Close(workspace.root)
	workspace.root = -1
	return err
}

func (workspace *safeWorkspace) apply(
	operation repository.FileOperation,
	after repository.TreeFile,
	value []byte,
) error {
	switch operation.Kind {
	case repository.OperationUpsert:
		parent, name, err := workspace.openParent(operation.Path, true)
		if err != nil {
			return err
		}
		defer unix.Close(parent)
		state, err := readWorkspaceFileAt(parent, name)
		if err != nil {
			return err
		}
		if state.exists && state.hash == after.ContentHash && state.mode == after.Mode {
			return nil
		}
		if operation.ExpectedHash == "" {
			if state.exists {
				return ErrWorkspaceConflict
			}
		} else if !state.exists || state.hash != operation.ExpectedHash {
			return ErrWorkspaceConflict
		}
		return writeWorkspaceFileAt(parent, name, value, after.Mode)
	case repository.OperationDelete:
		parent, name, err := workspace.openParent(operation.Path, false)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		defer unix.Close(parent)
		state, err := readWorkspaceFileAt(parent, name)
		if err != nil {
			return err
		}
		if !state.exists {
			return nil
		}
		if state.hash != operation.ExpectedHash {
			return ErrWorkspaceConflict
		}
		if err := unix.Unlinkat(parent, name, 0); err != nil {
			return fmt.Errorf("%w: delete workspace file: %v", ErrWorkspaceInvalid, err)
		}
		return syncWorkspaceDirectory(parent)
	case repository.OperationRename:
		sourceParent, sourceName, err := workspace.openParent(operation.FromPath, false)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if sourceParent >= 0 {
			defer unix.Close(sourceParent)
		}
		targetParent, targetName, err := workspace.openParent(operation.Path, true)
		if err != nil {
			return err
		}
		defer unix.Close(targetParent)
		var source workspaceFileState
		if sourceParent >= 0 {
			source, err = readWorkspaceFileAt(sourceParent, sourceName)
			if err != nil {
				return err
			}
		}
		target, err := readWorkspaceFileAt(targetParent, targetName)
		if err != nil {
			return err
		}
		if !source.exists && target.exists && target.hash == after.ContentHash && target.mode == after.Mode {
			return nil
		}
		if !source.exists || source.hash != operation.ExpectedHash || source.mode != after.Mode || target.exists {
			return ErrWorkspaceConflict
		}
		if err := unix.Renameat(sourceParent, sourceName, targetParent, targetName); err != nil {
			return fmt.Errorf("%w: rename workspace file: %v", ErrWorkspaceInvalid, err)
		}
		if err := syncWorkspaceDirectory(sourceParent); err != nil {
			return err
		}
		if targetParent != sourceParent {
			return syncWorkspaceDirectory(targetParent)
		}
		return nil
	default:
		return ErrWorkspaceConflict
	}
}

func (workspace *safeWorkspace) verifyApplied(
	operation repository.FileOperation,
	after repository.TreeFile,
) error {
	switch operation.Kind {
	case repository.OperationUpsert:
		state, err := workspace.state(operation.Path)
		if err != nil || !state.exists || state.hash != after.ContentHash || state.mode != after.Mode {
			return ErrWorkspaceConflict
		}
	case repository.OperationDelete:
		state, err := workspace.state(operation.Path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if state.exists {
			return ErrWorkspaceConflict
		}
	case repository.OperationRename:
		source, sourceErr := workspace.state(operation.FromPath)
		if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
			return sourceErr
		}
		target, targetErr := workspace.state(operation.Path)
		if targetErr != nil || source.exists || !target.exists || target.hash != after.ContentHash || target.mode != after.Mode {
			return ErrWorkspaceConflict
		}
	default:
		return ErrWorkspaceConflict
	}
	return nil
}

func (workspace *safeWorkspace) state(path string) (workspaceFileState, error) {
	parent, name, err := workspace.openParent(path, false)
	if err != nil {
		return workspaceFileState{}, err
	}
	defer unix.Close(parent)
	return readWorkspaceFileAt(parent, name)
}

func (workspace *safeWorkspace) openParent(path string, create bool) (int, string, error) {
	normalized, err := repository.NormalizePath(path)
	if err != nil || normalized != path {
		return -1, "", ErrWorkspaceConflict
	}
	segments := strings.Split(normalized, "/")
	parent, err := unix.Dup(workspace.root)
	if err != nil {
		return -1, "", fmt.Errorf("%w: duplicate workspace descriptor: %v", ErrWorkspaceInvalid, err)
	}
	for _, segment := range segments[:len(segments)-1] {
		next, openErr := unix.Openat(parent, segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil && create && errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(parent, segment, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				unix.Close(parent)
				return -1, "", fmt.Errorf("%w: create workspace directory: %v", ErrWorkspaceInvalid, mkdirErr)
			}
			next, openErr = unix.Openat(parent, segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			unix.Close(parent)
			if errors.Is(openErr, unix.ENOENT) {
				return -1, "", os.ErrNotExist
			}
			return -1, "", fmt.Errorf("%w: unsafe workspace parent: %v", ErrWorkspaceConflict, openErr)
		}
		unix.Close(parent)
		parent = next
	}
	return parent, segments[len(segments)-1], nil
}

func readWorkspaceFileAt(parent int, name string) (workspaceFileState, error) {
	descriptor, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return workspaceFileState{}, nil
	}
	if err != nil {
		return workspaceFileState{}, fmt.Errorf("%w: unsafe workspace file: %v", ErrWorkspaceConflict, err)
	}
	file := os.NewFile(uintptr(descriptor), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > repository.MaxFileBytes {
		return workspaceFileState{}, ErrWorkspaceConflict
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, repository.MaxFileBytes+1))
	if err != nil || written != info.Size() {
		return workspaceFileState{}, ErrWorkspaceConflict
	}
	mode := "100644"
	if info.Mode().Perm()&0o111 != 0 {
		mode = "100755"
	}
	return workspaceFileState{exists: true, hash: fmt.Sprintf("sha256:%x", hash.Sum(nil)), mode: mode}, nil
}

func writeWorkspaceFileAt(parent int, name string, value []byte, mode string) error {
	permissions := uint32(0o600)
	if mode == "100755" {
		permissions = 0o700
	} else if mode != "100644" {
		return ErrWorkspaceConflict
	}
	temporary := ".worksflow-" + uuid.NewString()
	descriptor, err := unix.Openat(
		parent, temporary,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		permissions,
	)
	if err != nil {
		return fmt.Errorf("%w: create atomic workspace file: %v", ErrWorkspaceInvalid, err)
	}
	file := os.NewFile(uintptr(descriptor), temporary)
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = unix.Unlinkat(parent, temporary, 0)
		}
	}()
	if written, err := file.Write(value); err != nil {
		return fmt.Errorf("%w: write atomic workspace file: %v", ErrWorkspaceInvalid, err)
	} else if written != len(value) {
		return fmt.Errorf("%w: write atomic workspace file: %v", ErrWorkspaceInvalid, io.ErrShortWrite)
	}
	if err := file.Chmod(os.FileMode(permissions)); err != nil {
		return fmt.Errorf("%w: chmod atomic workspace file: %v", ErrWorkspaceInvalid, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("%w: sync atomic workspace file: %v", ErrWorkspaceInvalid, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: close atomic workspace file: %v", ErrWorkspaceInvalid, err)
	}
	if err := unix.Renameat(parent, temporary, parent, name); err != nil {
		return fmt.Errorf("%w: publish atomic workspace file: %v", ErrWorkspaceInvalid, err)
	}
	complete = true
	return syncWorkspaceDirectory(parent)
}

func syncWorkspaceDirectory(descriptor int) error {
	if err := unix.Fsync(descriptor); err != nil {
		return fmt.Errorf("%w: sync workspace directory: %v", ErrWorkspaceInvalid, err)
	}
	return nil
}

var _ WorkspaceMutationSynchronizer = (*WorkspaceMaterializer)(nil)
var _ WorkspaceBatchMutationSynchronizer = (*WorkspaceMaterializer)(nil)
