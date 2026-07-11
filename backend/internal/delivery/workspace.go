package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

var (
	privateKeyPattern    = regexp.MustCompile(`(?i)-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----[\s\S]*?-----END (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`)
	providerTokenPattern = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{16,}|(?:gh[pousr]|github_pat)_[a-z0-9_-]{16,}|AKIA[0-9A-Z]{16})\b`)
	credentialPattern    = regexp.MustCompile(`(?i)\b(?:api[_-]?key|client[_-]?secret|auth[_-]?token|password|authorization)\b\s*[:=]\s*(["'])[^"']{12,}(["'])`)
)

type AccessControl interface {
	Authorize(context.Context, string, string, core.Action) (core.Role, error)
}

type RevisionContent struct {
	Artifact storage.ArtifactModel
	Revision storage.ArtifactRevisionModel
	Payload  json.RawMessage
}

type RevisionLoader struct {
	database *gorm.DB
	contents content.Store
	access   AccessControl
}

func NewRevisionLoader(database *gorm.DB, contents content.Store, access AccessControl) (*RevisionLoader, error) {
	if database == nil || contents == nil || access == nil {
		return nil, errors.New("revision loader database, content store and access control are required")
	}
	return &RevisionLoader{database: database, contents: contents, access: access}, nil
}

func (l *RevisionLoader) Load(ctx context.Context, projectID, actorID string, reference core.VersionRef, action core.Action, kinds ...string) (RevisionContent, error) {
	if err := ValidateVersionRef(reference); err != nil {
		return RevisionContent{}, err
	}
	if _, err := l.access.Authorize(ctx, projectID, actorID, action); err != nil {
		return RevisionContent{}, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return RevisionContent{}, Invalid("projectId", "projectId must be a UUID")
	}
	artifactUUID, err := uuid.Parse(reference.ArtifactID)
	if err != nil {
		return RevisionContent{}, Invalid("revision.artifactId", "artifactId must be a UUID")
	}
	revisionUUID, err := uuid.Parse(reference.RevisionID)
	if err != nil {
		return RevisionContent{}, Invalid("revision.revisionId", "revisionId must be a UUID")
	}
	var artifact storage.ArtifactModel
	if err := l.database.WithContext(ctx).Where("id = ? AND project_id = ?", artifactUUID, projectUUID).Take(&artifact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RevisionContent{}, notFound("artifact revision was not found in the project")
		}
		return RevisionContent{}, wrapInternal("load artifact metadata", err)
	}
	if len(kinds) > 0 {
		allowed := false
		for _, kind := range kinds {
			allowed = allowed || artifact.Kind == kind
		}
		if !allowed {
			return RevisionContent{}, conflict("artifact kind is not valid for this delivery operation")
		}
	}
	var revision storage.ArtifactRevisionModel
	if err := l.database.WithContext(ctx).Where("id = ? AND artifact_id = ?", revisionUUID, artifactUUID).Take(&revision).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RevisionContent{}, notFound("artifact revision was not found")
		}
		return RevisionContent{}, wrapInternal("load artifact revision", err)
	}
	if revision.ContentHash != reference.ContentHash {
		return RevisionContent{}, conflict("artifact revision content hash does not match the pinned reference")
	}
	stored, err := l.contents.Get(ctx, revision.ContentRef, revision.ContentHash)
	if err != nil {
		return RevisionContent{}, wrapInternal("load immutable revision content", err)
	}
	return RevisionContent{Artifact: artifact, Revision: revision, Payload: cloneJSON(stored.Payload)}, nil
}

func (l *RevisionLoader) LoadFrozenWorkspace(ctx context.Context, projectID, actorID string, reference core.VersionRef, action core.Action) (WorkspaceSnapshot, error) {
	if reference.AnchorID != nil {
		return WorkspaceSnapshot{}, Invalid("workspaceRevision.anchorId", "a frozen WorkspaceRevision must reference the whole revision, not an anchor")
	}
	loaded, err := l.Load(ctx, projectID, actorID, reference, action, "workspace")
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	if loaded.Revision.WorkflowStatus != "approved" {
		return WorkspaceSnapshot{}, conflict("quality, export and publish require an approved frozen WorkspaceRevision")
	}
	files, name, err := decodeWorkspace(loaded.Payload)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	return WorkspaceSnapshot{ProjectID: projectID, Revision: reference, Name: name, Files: files}, nil
}

func (l *RevisionLoader) LoadBuildManifest(ctx context.Context, projectID, actorID, manifestID string, action core.Action) (core.WorkbenchBundle, error) {
	if _, err := l.access.Authorize(ctx, projectID, actorID, action); err != nil {
		return core.WorkbenchBundle{}, err
	}
	manifestUUID, err := uuid.Parse(manifestID)
	if err != nil {
		return core.WorkbenchBundle{}, Invalid("buildManifestId", "buildManifestId must be a UUID")
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return core.WorkbenchBundle{}, Invalid("projectId", "projectId must be a UUID")
	}
	var model storage.ApplicationBuildManifestModel
	if err := l.database.WithContext(ctx).Where("id = ? AND project_id = ?", manifestUUID, projectUUID).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return core.WorkbenchBundle{}, notFound("build manifest was not found")
		}
		return core.WorkbenchBundle{}, wrapInternal("load build manifest", err)
	}
	if err := core.EnsureWorkflowManifestGroupActivated(ctx, l.database, model); err != nil {
		return core.WorkbenchBundle{}, err
	}
	stored, err := l.contents.Get(ctx, model.ContentRef, model.ContentHash)
	if err != nil {
		return core.WorkbenchBundle{}, wrapInternal("load frozen build manifest", err)
	}
	var bundle core.WorkbenchBundle
	if err := json.Unmarshal(stored.Payload, &bundle); err != nil {
		return core.WorkbenchBundle{}, wrapInternal("decode build manifest", err)
	}
	if bundle.ID != manifestID || bundle.ProjectID != projectID || bundle.ManifestHash != model.ManifestHash {
		return core.WorkbenchBundle{}, conflict("build manifest metadata does not match its immutable payload")
	}
	return bundle, nil
}

// ResolveWorkspaceManifestLineage proves the immutable server-side producer
// relation. The caller supplies any selector in the expected root lineage; the
// returned ID is the exact consumed leaf manifest whose applied proposal
// created the pinned workspace revision.
func (l *RevisionLoader) ResolveWorkspaceManifestLineage(
	ctx context.Context,
	projectID, actorID string,
	reference core.VersionRef,
	selectorManifestID string,
	action core.Action,
) (string, error) {
	if err := ValidateVersionRef(reference); err != nil {
		return "", err
	}
	if reference.AnchorID != nil {
		return "", Invalid("workspaceRevision.anchorId", "publish provenance requires the whole workspace revision")
	}
	if _, err := l.access.Authorize(ctx, projectID, actorID, action); err != nil {
		return "", err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return "", Invalid("projectId", "projectId must be a UUID")
	}
	artifactUUID, err := uuid.Parse(reference.ArtifactID)
	if err != nil {
		return "", Invalid("workspaceRevision.artifactId", "artifactId must be a UUID")
	}
	revisionUUID, err := uuid.Parse(reference.RevisionID)
	if err != nil {
		return "", Invalid("workspaceRevision.revisionId", "revisionId must be a UUID")
	}
	selectorUUID, err := uuid.Parse(strings.TrimSpace(selectorManifestID))
	if err != nil {
		return "", Invalid("buildManifestId", "buildManifestId must be a UUID")
	}
	var lineage struct {
		RevisionID uuid.UUID `gorm:"column:revision_id"`
		ManifestID uuid.UUID `gorm:"column:manifest_id"`
	}
	query := l.database.WithContext(ctx).
		Table("artifact_revisions AS workspace_revision").
		Select("workspace_revision.id AS revision_id, manifest.id AS manifest_id").
		Joins("JOIN artifacts AS workspace_artifact ON workspace_artifact.id = workspace_revision.artifact_id").
		Joins("JOIN implementation_proposals AS proposal ON proposal.id = workspace_revision.implementation_proposal_id").
		Joins("JOIN application_build_manifests AS manifest ON manifest.id = proposal.build_manifest_id").
		Joins("JOIN application_build_manifests AS selector ON selector.id = ?", selectorUUID).
		Where("workspace_revision.id = ? AND workspace_revision.artifact_id = ? AND workspace_revision.content_hash = ?", revisionUUID, artifactUUID, reference.ContentHash).
		Where("workspace_revision.workflow_status = 'approved'").
		Where("workspace_artifact.project_id = ? AND workspace_artifact.kind = 'workspace'", projectUUID).
		Where("proposal.project_id = ? AND proposal.build_manifest_id = manifest.id", projectUUID).
		Where("proposal.status IN ? AND proposal.applied_at IS NOT NULL", []string{"applied", "partially_applied"}).
		Where("manifest.project_id = ? AND manifest.status = 'consumed' AND manifest.invalidated_at IS NULL", projectUUID).
		Where("selector.project_id = ? AND selector.root_manifest_id = manifest.root_manifest_id", projectUUID).
		Where("selector.workflow_run_id IS NOT DISTINCT FROM manifest.workflow_run_id").
		Where("NOT EXISTS (SELECT 1 FROM application_build_manifests AS child WHERE child.derived_from_id = manifest.id)")
	if err := query.Take(&lineage).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", conflict("workspaceRevision was not produced by the selected buildManifest lineage")
		}
		return "", wrapInternal("resolve workspace build manifest lineage", err)
	}
	if lineage.RevisionID != revisionUUID || lineage.ManifestID == uuid.Nil {
		return "", conflict("workspaceRevision build manifest lineage is inconsistent")
	}
	return lineage.ManifestID.String(), nil
}

func decodeWorkspace(payload json.RawMessage) ([]WorkspaceFile, string, error) {
	var value struct {
		Name  string `json:"name"`
		Files []struct {
			Path     string `json:"path"`
			Content  string `json:"content"`
			Language string `json:"language"`
		} `json:"files"`
	}
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, "", Invalid("workspaceRevision", "workspace content is not valid JSON")
	}
	if len(value.Files) > MaxWorkspaceFiles {
		return nil, "", Invalid("workspaceRevision", "workspace contains too many files")
	}
	files := make([]WorkspaceFile, 0, len(value.Files))
	total := 0
	seen := map[string]bool{}
	for index, file := range value.Files {
		path, err := SanitizePath(file.Path)
		if err != nil {
			return nil, "", Invalid(fmt.Sprintf("workspace.files[%d].path", index), err.Error())
		}
		if seen[strings.ToLower(path)] {
			return nil, "", Invalid("workspace.files", "workspace contains duplicate case-insensitive paths")
		}
		seen[strings.ToLower(path)] = true
		if len(file.Content) > MaxWorkspaceFileSize {
			return nil, "", Invalid(fmt.Sprintf("workspace.files[%d].content", index), "workspace file exceeds the size limit")
		}
		total += len(file.Content)
		if total > MaxWorkspaceBytes {
			return nil, "", Invalid("workspace.files", "workspace exceeds the total size limit")
		}
		files = append(files, WorkspaceFile{Path: path, Content: file.Content, Language: strings.TrimSpace(file.Language)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, strings.TrimSpace(value.Name), nil
}

func SanitizePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~") {
		return "", NewError(CodeUnsafePath, 422, "path must be a safe relative project path")
	}
	cleaned := filepath.ToSlash(filepath.Clean(value))
	if cleaned != value || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || filepath.IsAbs(value) {
		return "", NewError(CodeUnsafePath, 422, "path contains traversal or normalization segments")
	}
	for _, segment := range strings.Split(cleaned, "/") {
		lower := strings.ToLower(segment)
		if segment == "" || segment == "." || segment == ".." || lower == ".git" || lower == ".next" || lower == "node_modules" {
			return "", NewError(CodeUnsafePath, 422, "path references a generated, dependency, or source-control location")
		}
		if strings.ContainsAny(segment, `<>:"|?*`) {
			return "", NewError(CodeUnsafePath, 422, "path contains unsupported filename characters")
		}
	}
	return cleaned, nil
}

func SensitivePath(value string) bool {
	for _, segment := range strings.Split(strings.ToLower(filepath.ToSlash(value)), "/") {
		if strings.HasPrefix(segment, ".env") || segment == ".npmrc" || segment == ".pypirc" || segment == "id_rsa" || segment == "id_ed25519" {
			return true
		}
	}
	return false
}

func SensitiveFinding(value string) (string, bool) {
	switch {
	case privateKeyPattern.MatchString(value):
		return "private-key", true
	case providerTokenPattern.MatchString(value):
		return "provider-token", true
	case credentialPattern.MatchString(value):
		return "embedded-credential", true
	default:
		return "", false
	}
}

func RedactSensitive(value string) (string, bool) {
	redacted := privateKeyPattern.ReplaceAllString(value, "[REDACTED PRIVATE KEY]")
	redacted = providerTokenPattern.ReplaceAllString(redacted, "[REDACTED TOKEN]")
	redacted = credentialPattern.ReplaceAllStringFunc(redacted, func(found string) string {
		separator := strings.IndexAny(found, ":=")
		if separator < 0 {
			return "[REDACTED CREDENTIAL]"
		}
		return found[:separator+1] + ` "[REDACTED]"`
	})
	return redacted, redacted != value
}

func materializeWorkspace(baseDirectory string, files []WorkspaceFile) (string, func(), error) {
	directory, err := os.MkdirTemp(baseDirectory, "worksflow-quality-")
	if err != nil {
		return "", nil, wrapInternal("create quality workspace", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	if err := os.Chmod(directory, 0o700); err != nil {
		cleanup()
		return "", nil, wrapInternal("protect quality workspace", err)
	}
	if err := writeWorkspaceFiles(directory, files); err != nil {
		cleanup()
		return "", nil, err
	}
	return directory, cleanup, nil
}

func resetMaterializedWorkspace(directory string, files []WorkspaceFile) error {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Invalid("workspace", "quality workspace was replaced by a sandbox command")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return wrapInternal("inspect quality workspace before isolated check", err)
	}
	for _, entry := range entries {
		target := filepath.Join(directory, entry.Name())
		if err := ensureBuildPathContained(directory, target); err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return wrapInternal("reset quality workspace between isolated checks", err)
		}
	}
	return writeWorkspaceFiles(directory, files)
}

func writeWorkspaceFiles(directory string, files []WorkspaceFile) error {
	for _, file := range files {
		safePath, err := SanitizePath(file.Path)
		if err != nil {
			return err
		}
		target := filepath.Join(directory, filepath.FromSlash(safePath))
		relative, err := filepath.Rel(directory, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return NewError(CodeUnsafePath, 422, "workspace path escapes the temporary sandbox")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return wrapInternal("create quality workspace directory", err)
		}
		if err := os.WriteFile(target, []byte(file.Content), 0o600); err != nil {
			return wrapInternal("write quality workspace file", err)
		}
	}
	return nil
}
