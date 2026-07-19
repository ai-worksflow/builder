package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"gorm.io/gorm"
)

const (
	canonicalWorkspaceMaxFiles    = 2_000
	canonicalWorkspaceMaxFileSize = 2 << 20
	canonicalWorkspaceMaxBytes    = 32 << 20
)

var ErrCanonicalMaterialization = errors.New("canonical verification materialization failed")

type CanonicalWorkspaceFile struct {
	Path    string
	Content []byte
	Mode    string
}

type CanonicalWorkspaceSnapshot struct {
	ProjectID   string
	Subject     CanonicalPlanSubject
	Name        string
	Files       []CanonicalWorkspaceFile
	ContentRef  string
	ContentHash string
}

type CanonicalWorkspaceSource interface {
	LoadCanonicalWorkspace(context.Context, string, CanonicalPlanSubject) (CanonicalWorkspaceSnapshot, error)
}

type PostgresCanonicalWorkspaceSource struct {
	database *gorm.DB
	contents PlanningContentReader
}

func NewPostgresCanonicalWorkspaceSource(
	database *gorm.DB,
	contents PlanningContentReader,
) (*PostgresCanonicalWorkspaceSource, error) {
	if database == nil || contents == nil {
		return nil, fmt.Errorf("%w: database and content reader are required", ErrCanonicalMaterialization)
	}
	return &PostgresCanonicalWorkspaceSource{database: database, contents: contents}, nil
}

func (source *PostgresCanonicalWorkspaceSource) LoadCanonicalWorkspace(
	ctx context.Context,
	projectID string,
	subject CanonicalPlanSubject,
) (CanonicalWorkspaceSnapshot, error) {
	if !validUUIDs(projectID) {
		return CanonicalWorkspaceSnapshot{}, fmt.Errorf("%w: invalid project identity", ErrCanonicalMaterialization)
	}
	if normalized, err := normalizeCanonicalPlanSubject(subject); err != nil || normalized != subject {
		return CanonicalWorkspaceSnapshot{}, fmt.Errorf("%w: invalid exact WorkspaceRevision", ErrCanonicalMaterialization)
	}
	var row struct {
		ProjectID   string `gorm:"column:project_id"`
		ArtifactID  string `gorm:"column:artifact_id"`
		RevisionID  string `gorm:"column:revision_id"`
		ContentRef  string `gorm:"column:content_ref"`
		ContentHash string `gorm:"column:content_hash"`
	}
	result := source.database.WithContext(ctx).Raw(`
SELECT artifact.project_id::text AS project_id,
       artifact.id::text AS artifact_id,
       revision.id::text AS revision_id,
       revision.content_ref,
       revision.content_hash
FROM artifact_revisions AS revision
JOIN artifacts AS artifact
  ON artifact.id = revision.artifact_id
 AND artifact.project_id = ?
 AND artifact.kind = 'workspace'
WHERE revision.id = ?
  AND revision.artifact_id = ?
  AND revision.content_hash = ?
  AND revision.workflow_status = 'approved'
`, projectID, subject.WorkspaceRevisionID, subject.WorkspaceArtifactID, subject.WorkspaceContentHash).Scan(&row)
	if result.Error != nil {
		return CanonicalWorkspaceSnapshot{}, fmt.Errorf("%w: resolve exact approved WorkspaceRevision: %v", ErrCanonicalMaterialization, result.Error)
	}
	if result.RowsAffected != 1 || row.ProjectID != projectID || row.ArtifactID != subject.WorkspaceArtifactID ||
		row.RevisionID != subject.WorkspaceRevisionID || row.ContentHash != subject.WorkspaceContentHash ||
		strings.TrimSpace(row.ContentRef) == "" {
		return CanonicalWorkspaceSnapshot{}, fmt.Errorf("%w: exact approved WorkspaceRevision is unavailable", ErrCanonicalMaterialization)
	}
	stored, err := source.contents.Get(ctx, row.ContentRef, row.ContentHash)
	if err != nil {
		return CanonicalWorkspaceSnapshot{}, fmt.Errorf("%w: read exact WorkspaceRevision content: %v", ErrCanonicalMaterialization, err)
	}
	if stored.ProjectID != projectID || stored.AggregateType != "artifact_revision" ||
		stored.AggregateID != subject.WorkspaceRevisionID || stored.SchemaVersion != 1 ||
		stored.State != content.StateFinalized || stored.ID != row.ContentRef || stored.ContentHash != row.ContentHash {
		return CanonicalWorkspaceSnapshot{}, fmt.Errorf("%w: WorkspaceRevision content metadata drifted", ErrCanonicalMaterialization)
	}
	name, files, err := decodeCanonicalWorkspace(stored.Payload)
	if err != nil {
		return CanonicalWorkspaceSnapshot{}, err
	}
	return CanonicalWorkspaceSnapshot{
		ProjectID: projectID, Subject: subject, Name: name, Files: files,
		ContentRef: row.ContentRef, ContentHash: row.ContentHash,
	}, nil
}

type CanonicalExecutionEnvironment interface {
	PrepareCanonical(context.Context, CanonicalExecutionSpec) error
	CleanupVerificationEnvironment(context.Context, VerificationEnvironmentCleanup) error
}

type CanonicalWorkspaceMaterializer struct {
	source      CanonicalWorkspaceSource
	root        string
	environment CanonicalExecutionEnvironment
}

func NewCanonicalWorkspaceMaterializer(
	source CanonicalWorkspaceSource,
	root string,
	environment CanonicalExecutionEnvironment,
) (*CanonicalWorkspaceMaterializer, error) {
	root, err := prepareVerificationWorkspaceRoot(root)
	if source == nil || err != nil {
		return nil, fmt.Errorf("%w: source and private workspace root are required", ErrCanonicalMaterialization)
	}
	return &CanonicalWorkspaceMaterializer{source: source, root: root, environment: environment}, nil
}

func (materializer *CanonicalWorkspaceMaterializer) MaterializeCanonical(
	ctx context.Context,
	spec CanonicalExecutionSpec,
) error {
	if err := validateCanonicalExecutionSpec(spec); err != nil {
		return err
	}
	snapshot, err := materializer.source.LoadCanonicalWorkspace(ctx, spec.Content.ProjectID, spec.Content.Subject)
	if err != nil {
		return err
	}
	if snapshot.ProjectID != spec.Content.ProjectID || snapshot.Subject != spec.Content.Subject ||
		snapshot.ContentHash != spec.Content.Subject.WorkspaceContentHash {
		return fmt.Errorf("%w: exact WorkspaceRevision source drifted", ErrCanonicalMaterialization)
	}
	finalRoot := materializer.executionRoot(spec.AttemptID, spec.AttemptFenceEpoch)
	stagingRoot, err := os.MkdirTemp(materializer.root, ".canonical-staging-"+spec.AttemptID+"-")
	if err != nil {
		return fmt.Errorf("%w: create staging directory: %v", ErrCanonicalMaterialization, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stagingRoot)
		}
	}()
	workspace := filepath.Join(stagingRoot, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		return fmt.Errorf("%w: create workspace: %v", ErrCanonicalMaterialization, err)
	}
	for _, file := range snapshot.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(workspace, filepath.FromSlash(file.Path))
		if !pathWithinVerificationRoot(workspace, target) {
			return fmt.Errorf("%w: workspace path escaped root", ErrCanonicalMaterialization)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("%w: create workspace directory: %v", ErrCanonicalMaterialization, err)
		}
		mode, err := canonicalWorkspaceFileMode(file.Mode)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, file.Content, mode); err != nil {
			return fmt.Errorf("%w: write workspace file: %v", ErrCanonicalMaterialization, err)
		}
		if err := os.Chmod(target, mode); err != nil {
			return fmt.Errorf("%w: seal workspace file: %v", ErrCanonicalMaterialization, err)
		}
	}
	if err := sealVerificationDirectories(workspace); err != nil {
		return fmt.Errorf("%w: seal workspace: %v", ErrCanonicalMaterialization, err)
	}
	identity := []byte(spec.PlanHash + "\n" + snapshot.ContentHash + "\n")
	if err := os.WriteFile(filepath.Join(stagingRoot, "identity"), identity, 0o400); err != nil {
		return fmt.Errorf("%w: write workspace identity: %v", ErrCanonicalMaterialization, err)
	}
	if err := withVerificationAttemptLock(ctx, materializer.root, spec.AttemptID, func(attemptRoot string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := materializer.cleanupSupersededCanonicalFences(ctx, attemptRoot, canonicalSpecFence(spec)); err != nil {
			return err
		}
		if err := removeVerificationExecutionRoot(finalRoot); err != nil {
			return fmt.Errorf("%w: replace exact fence workspace: %v", ErrCanonicalMaterialization, err)
		}
		if err := os.Rename(stagingRoot, finalRoot); err != nil {
			return fmt.Errorf("%w: commit workspace: %v", ErrCanonicalMaterialization, err)
		}
		return nil
	}); err != nil {
		return err
	}
	committed = true
	return nil
}

func (materializer *CanonicalWorkspaceMaterializer) PrepareCanonical(
	ctx context.Context,
	spec CanonicalExecutionSpec,
) error {
	if err := validateCanonicalExecutionSpec(spec); err != nil {
		return err
	}
	return withVerificationAttemptLock(ctx, materializer.root, spec.AttemptID, func(attemptRoot string) error {
		fences, err := verificationAttemptFences(attemptRoot)
		if err != nil {
			return fmt.Errorf("%w: inspect Attempt fences: %v", ErrCanonicalMaterialization, err)
		}
		marker, err := readVerificationRuntimeFence(attemptRoot)
		if err != nil {
			return fmt.Errorf("%w: inspect runtime fence: %v", ErrCanonicalMaterialization, err)
		}
		if containsNewerVerificationFence(fences, marker, spec.AttemptFenceEpoch) {
			return fmt.Errorf("%w: a newer fence owns the Canonical execution resources", ErrWorkerLeaseLost)
		}
		root := materializer.executionRoot(spec.AttemptID, spec.AttemptFenceEpoch)
		identity, err := os.ReadFile(filepath.Join(root, "identity"))
		if err != nil || string(identity) != spec.PlanHash+"\n"+spec.Content.Subject.WorkspaceContentHash+"\n" {
			return fmt.Errorf("%w: prepared workspace identity drifted", ErrCanonicalMaterialization)
		}
		info, err := os.Lstat(filepath.Join(root, "workspace"))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: prepared workspace is unavailable", ErrCanonicalMaterialization)
		}
		if materializer.environment != nil {
			if err := writeVerificationRuntimeFence(attemptRoot, spec.AttemptFenceEpoch); err != nil {
				return fmt.Errorf("%w: claim Canonical runtime fence: %v", ErrCanonicalMaterialization, err)
			}
			if err := materializer.environment.PrepareCanonical(ctx, spec); err != nil {
				return fmt.Errorf("%w: prepare execution environment: %v", ErrCanonicalMaterialization, err)
			}
		}
		return nil
	})
}

func (materializer *CanonicalWorkspaceMaterializer) CollectCanonical(
	ctx context.Context,
	spec CanonicalExecutionSpec,
) error {
	if err := validateCanonicalExecutionSpec(spec); err != nil {
		return err
	}
	return materializer.CleanupCanonical(ctx, canonicalSpecFence(spec))
}

func (materializer *CanonicalWorkspaceMaterializer) CleanupCanonical(
	ctx context.Context,
	fence VerificationExecutionFence,
) error {
	if materializer == nil || ctx == nil || validateVerificationExecutionFence(fence) != nil {
		return fmt.Errorf("%w: invalid exact Canonical cleanup", ErrCanonicalMaterialization)
	}
	return withVerificationAttemptLock(ctx, materializer.root, fence.AttemptID, func(attemptRoot string) error {
		fences, err := verificationAttemptFences(attemptRoot)
		if err != nil {
			return fmt.Errorf("%w: inspect Attempt fences: %v", ErrCanonicalMaterialization, err)
		}
		marker, err := readVerificationRuntimeFence(attemptRoot)
		if err != nil {
			return fmt.Errorf("%w: inspect runtime fence: %v", ErrCanonicalMaterialization, err)
		}
		ownsShared := marker == fence.AttemptFenceEpoch &&
			!containsNewerVerificationFence(fences, marker, fence.AttemptFenceEpoch)
		if materializer.environment != nil {
			if err := materializer.environment.CleanupVerificationEnvironment(
				ctx, VerificationEnvironmentCleanup{Fence: fence, OwnsSharedRuntime: ownsShared},
			); err != nil {
				return fmt.Errorf("%w: clean exact Canonical environment: %v", ErrCanonicalMaterialization, err)
			}
		}
		if err := removeVerificationExecutionRoot(
			materializer.executionRoot(fence.AttemptID, fence.AttemptFenceEpoch),
		); err != nil {
			return fmt.Errorf("%w: clean exact Canonical workspace: %v", ErrCanonicalMaterialization, err)
		}
		if ownsShared {
			if err := removeVerificationRuntimeFence(attemptRoot, fence.AttemptFenceEpoch); err != nil {
				return fmt.Errorf("%w: release Canonical runtime marker: %v", ErrCanonicalMaterialization, err)
			}
		}
		if err := removeEmptyVerificationAttemptRoot(attemptRoot); err != nil {
			return fmt.Errorf("%w: remove empty Canonical Attempt root: %v", ErrCanonicalMaterialization, err)
		}
		return nil
	})
}

func (materializer *CanonicalWorkspaceMaterializer) cleanupSupersededCanonicalFences(
	ctx context.Context,
	attemptRoot string,
	current VerificationExecutionFence,
) error {
	fences, err := verificationAttemptFences(attemptRoot)
	if err != nil {
		return fmt.Errorf("%w: inspect prior Canonical Attempt fences: %v", ErrCanonicalMaterialization, err)
	}
	marker, err := readVerificationRuntimeFence(attemptRoot)
	if err != nil {
		return fmt.Errorf("%w: inspect prior Canonical runtime fence: %v", ErrCanonicalMaterialization, err)
	}
	if containsNewerVerificationFence(fences, marker, current.AttemptFenceEpoch) {
		return fmt.Errorf("%w: a newer fence owns the Canonical execution resources", ErrWorkerLeaseLost)
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
				return fmt.Errorf("%w: clean superseded Canonical environment: %v", ErrCanonicalMaterialization, err)
			}
		}
		if err := removeVerificationExecutionRoot(
			materializer.executionRoot(current.AttemptID, value),
		); err != nil {
			return fmt.Errorf("%w: clean superseded Canonical workspace: %v", ErrCanonicalMaterialization, err)
		}
		if marker == value {
			if err := removeVerificationRuntimeFence(attemptRoot, value); err != nil {
				return fmt.Errorf("%w: release superseded Canonical runtime marker: %v", ErrCanonicalMaterialization, err)
			}
		}
	}
	return nil
}

func canonicalSpecFence(spec CanonicalExecutionSpec) VerificationExecutionFence {
	return VerificationExecutionFence{
		ProjectID: spec.Content.ProjectID, RunID: spec.RunID, AttemptID: spec.AttemptID,
		AttemptFenceEpoch: spec.AttemptFenceEpoch,
	}
}

func (materializer *CanonicalWorkspaceMaterializer) executionRoot(attemptID string, fence uint64) string {
	return filepath.Join(materializer.root, attemptID, strconv.FormatUint(fence, 10))
}

func validateCanonicalExecutionSpec(spec CanonicalExecutionSpec) error {
	if !validUUIDs(spec.RunID, spec.AttemptID, spec.PlanID, spec.Content.ProjectID) ||
		spec.AttemptFenceEpoch == 0 || !exactSHA256(spec.PlanHash) ||
		spec.Content.SchemaVersion != CanonicalPlanContentSchemaVersion || spec.Content.Scope != ScopeCanonical {
		return fmt.Errorf("%w: invalid immutable execution specification", ErrCanonicalMaterialization)
	}
	if normalized, err := normalizeCanonicalPlanSubject(spec.Content.Subject); err != nil || normalized != spec.Content.Subject {
		return fmt.Errorf("%w: invalid exact WorkspaceRevision", ErrCanonicalMaterialization)
	}
	return nil
}

func decodeCanonicalWorkspace(payload json.RawMessage) (string, []CanonicalWorkspaceFile, error) {
	var value struct {
		Name  string `json:"name"`
		Files []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
			Mode    string `json:"mode"`
		} `json:"files"`
	}
	if err := json.Unmarshal(payload, &value); err != nil {
		return "", nil, fmt.Errorf("%w: WorkspaceRevision content is not valid JSON", ErrCanonicalMaterialization)
	}
	if len(value.Files) == 0 || len(value.Files) > canonicalWorkspaceMaxFiles {
		return "", nil, fmt.Errorf("%w: WorkspaceRevision file count is outside limits", ErrCanonicalMaterialization)
	}
	files := make([]CanonicalWorkspaceFile, 0, len(value.Files))
	seen, total := map[string]bool{}, 0
	for _, file := range value.Files {
		path, err := normalizeCanonicalWorkspacePath(file.Path)
		if err != nil {
			return "", nil, err
		}
		caseFolded := strings.ToLower(path)
		if seen[caseFolded] {
			return "", nil, fmt.Errorf("%w: duplicate case-insensitive workspace path", ErrCanonicalMaterialization)
		}
		seen[caseFolded] = true
		if len(file.Content) > canonicalWorkspaceMaxFileSize {
			return "", nil, fmt.Errorf("%w: workspace file exceeds size limit", ErrCanonicalMaterialization)
		}
		total += len(file.Content)
		if total > canonicalWorkspaceMaxBytes {
			return "", nil, fmt.Errorf("%w: workspace exceeds total size limit", ErrCanonicalMaterialization)
		}
		if _, err := canonicalWorkspaceFileMode(file.Mode); err != nil {
			return "", nil, err
		}
		files = append(files, CanonicalWorkspaceFile{Path: path, Content: []byte(file.Content), Mode: file.Mode})
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return strings.TrimSpace(value.Name), files, nil
}

func canonicalWorkspaceFileMode(value string) (os.FileMode, error) {
	switch value {
	case "100644":
		return 0o400, nil
	case "100755":
		return 0o500, nil
	default:
		return 0, fmt.Errorf("%w: workspace file mode must be 100644 or 100755", ErrCanonicalMaterialization)
	}
}

func normalizeCanonicalWorkspacePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	normalized, err := repository.NormalizePath(value)
	if err != nil || normalized != value || len(value) > 512 || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") {
		return "", fmt.Errorf("%w: unsafe workspace path", ErrCanonicalMaterialization)
	}
	for _, segment := range strings.Split(strings.ToLower(normalized), "/") {
		if segment == ".git" || segment == ".next" || segment == "node_modules" {
			return "", fmt.Errorf("%w: generated, dependency, or source-control path", ErrCanonicalMaterialization)
		}
	}
	return normalized, nil
}
