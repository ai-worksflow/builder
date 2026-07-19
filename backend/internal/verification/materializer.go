package verification

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/worksflow/builder/backend/internal/repository"
	"gorm.io/gorm"
)

var (
	ErrCandidateMaterialization = errors.New("candidate verification materialization failed")
)

type verificationTreeReader interface {
	Get(context.Context, string, string, repository.TreeBlobPointer) (repository.TreeManifest, error)
}

type verificationFileResolver interface {
	Resolve(context.Context, string, string, int64) (repository.FileBlobPointer, []byte, error)
}

type CandidateWorkspaceMaterializer struct {
	database        *gorm.DB
	trees           verificationTreeReader
	files           verificationFileResolver
	root            string
	resolveSnapshot func(context.Context, CandidateExecutionSpec) (repository.TreeManifest, error)
	environment     CandidateExecutionEnvironment
}

type CandidateExecutionEnvironment interface {
	Prepare(context.Context, CandidateExecutionSpec) error
	CleanupVerificationEnvironment(context.Context, VerificationEnvironmentCleanup) error
}

type candidateSnapshotMaterializationRow struct {
	TreeFileCount int   `gorm:"column:tree_file_count"`
	TreeByteSize  int64 `gorm:"column:tree_byte_size"`
}

func NewCandidateWorkspaceMaterializer(
	database *gorm.DB,
	trees verificationTreeReader,
	files verificationFileResolver,
	root string,
	environment CandidateExecutionEnvironment,
) (*CandidateWorkspaceMaterializer, error) {
	root, err := prepareVerificationWorkspaceRoot(root)
	if database == nil || trees == nil || files == nil || err != nil {
		return nil, fmt.Errorf("%w: database, tree/file sources, and private workspace root are required", ErrCandidateMaterialization)
	}
	materializer := &CandidateWorkspaceMaterializer{
		database: database, trees: trees, files: files, root: root, environment: environment,
	}
	materializer.resolveSnapshot = materializer.resolveExactSnapshot
	return materializer, nil
}

func (materializer *CandidateWorkspaceMaterializer) Materialize(
	ctx context.Context,
	spec CandidateExecutionSpec,
) error {
	if err := validateCandidateExecutionSpec(spec); err != nil {
		return err
	}
	tree, err := materializer.resolveSnapshot(ctx, spec)
	if err != nil {
		return err
	}
	subject := spec.Content.Subject

	finalRoot := materializer.executionRoot(spec.AttemptID, spec.AttemptFenceEpoch)
	stagingRoot, err := os.MkdirTemp(materializer.root, ".staging-"+spec.AttemptID+"-")
	if err != nil {
		return fmt.Errorf("%w: create staging directory: %v", ErrCandidateMaterialization, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stagingRoot)
		}
	}()
	workspace := filepath.Join(stagingRoot, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		return fmt.Errorf("%w: create workspace: %v", ErrCandidateMaterialization, err)
	}
	for _, file := range tree.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		normalized, normalizeErr := repository.NormalizePath(file.Path)
		if normalizeErr != nil || normalized != file.Path {
			return fmt.Errorf("%w: unsafe tree path", ErrCandidateMaterialization)
		}
		_, value, resolveErr := materializer.files.Resolve(
			ctx, spec.Content.ProjectID, file.ContentHash, file.ByteSize,
		)
		if resolveErr != nil || int64(len(value)) != file.ByteSize {
			return fmt.Errorf("%w: resolve %s: %v", ErrCandidateMaterialization, file.Path, resolveErr)
		}
		target := filepath.Join(workspace, filepath.FromSlash(file.Path))
		if !pathWithinVerificationRoot(workspace, target) {
			return fmt.Errorf("%w: path escaped workspace", ErrCandidateMaterialization)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("%w: create parent directory: %v", ErrCandidateMaterialization, err)
		}
		mode := os.FileMode(0o400)
		if file.Mode == "100755" {
			mode = 0o500
		}
		if err := os.WriteFile(target, value, mode); err != nil {
			return fmt.Errorf("%w: write %s: %v", ErrCandidateMaterialization, file.Path, err)
		}
		if err := os.Chmod(target, mode); err != nil {
			return fmt.Errorf("%w: seal %s: %v", ErrCandidateMaterialization, file.Path, err)
		}
	}
	if err := sealVerificationDirectories(workspace); err != nil {
		return fmt.Errorf("%w: seal workspace: %v", ErrCandidateMaterialization, err)
	}
	identity := []byte(spec.PlanHash + "\n" + subject.TreeHash + "\n")
	if err := os.WriteFile(filepath.Join(stagingRoot, "identity"), identity, 0o400); err != nil {
		return fmt.Errorf("%w: write workspace identity: %v", ErrCandidateMaterialization, err)
	}
	if err := withVerificationAttemptLock(ctx, materializer.root, spec.AttemptID, func(attemptRoot string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := materializer.cleanupSupersededCandidateFences(ctx, attemptRoot, candidateSpecFence(spec)); err != nil {
			return err
		}
		if err := removeVerificationExecutionRoot(finalRoot); err != nil {
			return fmt.Errorf("%w: replace prior exact fence workspace: %v", ErrCandidateMaterialization, err)
		}
		if err := os.Rename(stagingRoot, finalRoot); err != nil {
			return fmt.Errorf("%w: commit workspace: %v", ErrCandidateMaterialization, err)
		}
		return nil
	}); err != nil {
		return err
	}
	committed = true
	return nil
}

func (materializer *CandidateWorkspaceMaterializer) resolveExactSnapshot(
	ctx context.Context,
	spec CandidateExecutionSpec,
) (repository.TreeManifest, error) {
	subject := spec.Content.Subject
	var snapshot candidateSnapshotMaterializationRow
	result := materializer.database.WithContext(ctx).Raw(`
SELECT tree_file_count, tree_byte_size
FROM candidate_snapshots
WHERE id = ? AND project_id = ? AND candidate_id = ?
  AND candidate_version = ? AND journal_sequence = ?
  AND session_epoch = ? AND writer_lease_epoch = ?
  AND tree_store = ? AND tree_owner_id = ? AND tree_ref = ?
  AND tree_content_hash = ? AND tree_hash = ?
`, subject.CandidateSnapshotID, spec.Content.ProjectID, subject.CandidateID,
		subject.CandidateVersion, subject.JournalSequence, subject.SessionEpoch,
		subject.WriterLeaseEpoch, subject.TreeStore, subject.TreeOwnerID,
		subject.TreeRef, subject.TreeContentHash, subject.TreeHash).Scan(&snapshot)
	if result.Error != nil {
		return repository.TreeManifest{}, fmt.Errorf("%w: resolve exact CandidateSnapshot: %v", ErrCandidateMaterialization, result.Error)
	}
	if result.RowsAffected != 1 || snapshot.TreeFileCount < 0 || snapshot.TreeByteSize < 0 {
		return repository.TreeManifest{}, fmt.Errorf("%w: exact CandidateSnapshot is unavailable", ErrCandidateMaterialization)
	}
	pointer := repository.TreeBlobPointer{
		Store: subject.TreeStore, Ref: subject.TreeRef, OwnerID: subject.TreeOwnerID,
		TreeHash: subject.TreeHash, FileCount: snapshot.TreeFileCount,
		ByteSize: snapshot.TreeByteSize, ContentObjectHash: subject.TreeContentHash,
	}
	tree, err := materializer.trees.Get(ctx, spec.Content.ProjectID, subject.TreeOwnerID, pointer)
	if err != nil {
		return repository.TreeManifest{}, fmt.Errorf("%w: read exact CandidateSnapshot tree: %v", ErrCandidateMaterialization, err)
	}
	tree, err = repository.ParseTree(tree)
	if err != nil || tree.TreeHash != subject.TreeHash || len(tree.Files) != snapshot.TreeFileCount ||
		verificationTreeByteSize(tree) != snapshot.TreeByteSize {
		return repository.TreeManifest{}, fmt.Errorf("%w: CandidateSnapshot tree identity drifted", ErrCandidateMaterialization)
	}
	return tree, nil
}

func (materializer *CandidateWorkspaceMaterializer) Prepare(
	ctx context.Context,
	spec CandidateExecutionSpec,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateCandidateExecutionSpec(spec); err != nil {
		return err
	}
	return withVerificationAttemptLock(ctx, materializer.root, spec.AttemptID, func(attemptRoot string) error {
		fences, err := verificationAttemptFences(attemptRoot)
		if err != nil {
			return fmt.Errorf("%w: inspect Attempt fences: %v", ErrCandidateMaterialization, err)
		}
		marker, err := readVerificationRuntimeFence(attemptRoot)
		if err != nil {
			return fmt.Errorf("%w: inspect runtime fence: %v", ErrCandidateMaterialization, err)
		}
		if containsNewerVerificationFence(fences, marker, spec.AttemptFenceEpoch) {
			return fmt.Errorf("%w: a newer fence owns the execution resources", ErrWorkerLeaseLost)
		}
		root := materializer.executionRoot(spec.AttemptID, spec.AttemptFenceEpoch)
		identity, err := os.ReadFile(filepath.Join(root, "identity"))
		if err != nil || string(identity) != spec.PlanHash+"\n"+spec.Content.Subject.TreeHash+"\n" {
			return fmt.Errorf("%w: prepared workspace identity drifted", ErrCandidateMaterialization)
		}
		info, err := os.Lstat(filepath.Join(root, "workspace"))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: prepared workspace is unavailable", ErrCandidateMaterialization)
		}
		if materializer.environment != nil {
			if err := writeVerificationRuntimeFence(attemptRoot, spec.AttemptFenceEpoch); err != nil {
				return fmt.Errorf("%w: claim runtime fence: %v", ErrCandidateMaterialization, err)
			}
			if err := materializer.environment.Prepare(ctx, spec); err != nil {
				return fmt.Errorf("%w: prepare execution environment: %v", ErrCandidateMaterialization, err)
			}
		}
		return nil
	})
}

func (materializer *CandidateWorkspaceMaterializer) Collect(
	ctx context.Context,
	spec CandidateExecutionSpec,
) error {
	if err := validateCandidateExecutionSpec(spec); err != nil {
		return err
	}
	return materializer.CleanupCandidate(ctx, candidateSpecFence(spec))
}

func (materializer *CandidateWorkspaceMaterializer) CleanupCandidate(
	ctx context.Context,
	fence VerificationExecutionFence,
) error {
	if materializer == nil || ctx == nil || validateVerificationExecutionFence(fence) != nil {
		return fmt.Errorf("%w: invalid exact Candidate cleanup", ErrCandidateMaterialization)
	}
	return withVerificationAttemptLock(ctx, materializer.root, fence.AttemptID, func(attemptRoot string) error {
		fences, err := verificationAttemptFences(attemptRoot)
		if err != nil {
			return fmt.Errorf("%w: inspect Attempt fences: %v", ErrCandidateMaterialization, err)
		}
		marker, err := readVerificationRuntimeFence(attemptRoot)
		if err != nil {
			return fmt.Errorf("%w: inspect runtime fence: %v", ErrCandidateMaterialization, err)
		}
		ownsShared := marker == fence.AttemptFenceEpoch &&
			!containsNewerVerificationFence(fences, marker, fence.AttemptFenceEpoch)
		if materializer.environment != nil {
			if err := materializer.environment.CleanupVerificationEnvironment(
				ctx, VerificationEnvironmentCleanup{Fence: fence, OwnsSharedRuntime: ownsShared},
			); err != nil {
				return fmt.Errorf("%w: clean exact Candidate environment: %v", ErrCandidateMaterialization, err)
			}
		}
		if err := removeVerificationExecutionRoot(
			materializer.executionRoot(fence.AttemptID, fence.AttemptFenceEpoch),
		); err != nil {
			return fmt.Errorf("%w: clean exact Candidate workspace: %v", ErrCandidateMaterialization, err)
		}
		if ownsShared {
			if err := removeVerificationRuntimeFence(attemptRoot, fence.AttemptFenceEpoch); err != nil {
				return fmt.Errorf("%w: release Candidate runtime marker: %v", ErrCandidateMaterialization, err)
			}
		}
		if err := removeEmptyVerificationAttemptRoot(attemptRoot); err != nil {
			return fmt.Errorf("%w: remove empty Candidate Attempt root: %v", ErrCandidateMaterialization, err)
		}
		return nil
	})
}

func (materializer *CandidateWorkspaceMaterializer) cleanupSupersededCandidateFences(
	ctx context.Context,
	attemptRoot string,
	current VerificationExecutionFence,
) error {
	fences, err := verificationAttemptFences(attemptRoot)
	if err != nil {
		return fmt.Errorf("%w: inspect prior Attempt fences: %v", ErrCandidateMaterialization, err)
	}
	marker, err := readVerificationRuntimeFence(attemptRoot)
	if err != nil {
		return fmt.Errorf("%w: inspect prior runtime fence: %v", ErrCandidateMaterialization, err)
	}
	if containsNewerVerificationFence(fences, marker, current.AttemptFenceEpoch) {
		return fmt.Errorf("%w: a newer fence owns the execution resources", ErrWorkerLeaseLost)
	}
	stale := append([]uint64(nil), fences...)
	if marker != 0 {
		stale = append(stale, marker)
	}
	stale = uniqueVerificationFences(stale)
	for _, value := range stale {
		if value > current.AttemptFenceEpoch {
			continue
		}
		fence := current
		fence.AttemptFenceEpoch = value
		if materializer.environment != nil {
			if err := materializer.environment.CleanupVerificationEnvironment(
				ctx, VerificationEnvironmentCleanup{Fence: fence, OwnsSharedRuntime: marker == value},
			); err != nil {
				return fmt.Errorf("%w: clean superseded Candidate environment: %v", ErrCandidateMaterialization, err)
			}
		}
		if err := removeVerificationExecutionRoot(
			materializer.executionRoot(current.AttemptID, value),
		); err != nil {
			return fmt.Errorf("%w: clean superseded Candidate workspace: %v", ErrCandidateMaterialization, err)
		}
		if marker == value {
			if err := removeVerificationRuntimeFence(attemptRoot, value); err != nil {
				return fmt.Errorf("%w: release superseded Candidate runtime marker: %v", ErrCandidateMaterialization, err)
			}
		}
	}
	return nil
}

func candidateSpecFence(spec CandidateExecutionSpec) VerificationExecutionFence {
	return VerificationExecutionFence{
		ProjectID: spec.Content.ProjectID, RunID: spec.RunID, AttemptID: spec.AttemptID,
		AttemptFenceEpoch: spec.AttemptFenceEpoch,
	}
}

func (materializer *CandidateWorkspaceMaterializer) executionRoot(attemptID string, fence uint64) string {
	return filepath.Join(materializer.root, attemptID, strconv.FormatUint(fence, 10))
}

func validateCandidateExecutionSpec(spec CandidateExecutionSpec) error {
	if !validUUIDs(spec.RunID, spec.AttemptID, spec.PlanID, spec.Content.ProjectID) ||
		spec.AttemptFenceEpoch == 0 || !exactSHA256(spec.PlanHash) ||
		!supportedPlanContentSchema(spec.Content.SchemaVersion) || spec.Content.Scope != ScopeCandidate {
		return fmt.Errorf("%w: invalid immutable execution specification", ErrCandidateMaterialization)
	}
	if spec.PlanID == "" || spec.PlanHash == "" || spec.Content.Subject.CandidateSnapshotID == "" {
		return fmt.Errorf("%w: incomplete immutable execution specification", ErrCandidateMaterialization)
	}
	return nil
}

func prepareVerificationWorkspaceRoot(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, '\x00') {
		return "", ErrCandidateMaterialization
	}
	absolute, err := filepath.Abs(value)
	if err != nil || filepath.Clean(absolute) != absolute {
		return "", ErrCandidateMaterialization
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrCandidateMaterialization
	}
	return absolute, nil
}

func pathWithinVerificationRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}

func sealVerificationDirectories(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrCandidateMaterialization
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := os.Chmod(directories[index], 0o500); err != nil {
			return err
		}
	}
	return nil
}

func removeVerificationExecutionRoot(root string) error {
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		} else {
			_ = os.Chmod(path, 0o600)
		}
		return nil
	})
	return os.RemoveAll(root)
}

func verificationTreeByteSize(tree repository.TreeManifest) int64 {
	var result int64
	for _, file := range tree.Files {
		result += file.ByteSize
	}
	return result
}
