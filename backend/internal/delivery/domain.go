package delivery

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
)

const (
	RunnerVersion         = "1.0.0"
	MaxWorkspaceFiles     = 2_000
	MaxWorkspaceFileSize  = 2 << 20
	MaxWorkspaceBytes     = 32 << 20
	MaxBuildArtifactBytes = 10 << 20
	MaxArchiveBytes       = 64 << 20
	MaxCommandOutput      = 1 << 20
)

type ErrorCode string

const (
	CodeInvalidInput       ErrorCode = "invalid_input"
	CodeNotFound           ErrorCode = "not_found"
	CodeForbidden          ErrorCode = "forbidden"
	CodeConflict           ErrorCode = "conflict"
	CodePrecondition       ErrorCode = "etag_mismatch"
	CodeUnsafePath         ErrorCode = "unsafe_path"
	CodeSensitiveContent   ErrorCode = "sensitive_content"
	CodeSandboxUnavailable ErrorCode = "sandbox_unavailable"
	CodeSandboxTimeout     ErrorCode = "sandbox_timeout"
	CodeOutputLimit        ErrorCode = "output_limit_exceeded"
	CodeProviderFailure    ErrorCode = "publish_provider_failed"
	CodeInternal           ErrorCode = "internal_error"
)

type DeliveryError struct {
	Code   ErrorCode
	Status int
	Detail string
	Fields map[string][]string
	Cause  error
}

func (e *DeliveryError) Error() string {
	if e == nil {
		return ""
	}
	return e.Detail
}

func (e *DeliveryError) Unwrap() error { return e.Cause }

func NewError(code ErrorCode, status int, detail string) *DeliveryError {
	return &DeliveryError{Code: code, Status: status, Detail: detail}
}

func Invalid(field, detail string) *DeliveryError {
	return &DeliveryError{Code: CodeInvalidInput, Status: http.StatusUnprocessableEntity, Detail: detail, Fields: map[string][]string{field: {detail}}}
}

func AsError(err error) (*DeliveryError, bool) {
	var target *DeliveryError
	ok := errors.As(err, &target)
	return target, ok
}

type WorkspaceFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Language string `json:"language,omitempty"`
}

type WorkspaceSnapshot struct {
	ProjectID string          `json:"projectId"`
	Revision  core.VersionRef `json:"revision"`
	Name      string          `json:"name"`
	Files     []WorkspaceFile `json:"files"`
}

type CheckID string

const (
	CheckBuild         CheckID = "build"
	CheckType          CheckID = "type"
	CheckLint          CheckID = "lint"
	CheckTest          CheckID = "test"
	CheckAccessibility CheckID = "accessibility"
	CheckDependency    CheckID = "dependency"
	CheckSecret        CheckID = "secret"
)

var RequiredChecks = []CheckID{
	CheckBuild, CheckType, CheckLint, CheckTest,
	CheckAccessibility, CheckDependency, CheckSecret,
}

type CheckStatus string

const (
	CheckPassed  CheckStatus = "passed"
	CheckWarning CheckStatus = "warning"
	CheckFailed  CheckStatus = "failed"
	CheckSkipped CheckStatus = "skipped"
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

type Diagnostic struct {
	ID         string   `json:"id,omitempty"`
	CheckID    CheckID  `json:"checkId"`
	Code       string   `json:"code"`
	Severity   Severity `json:"severity"`
	Message    string   `json:"message"`
	Path       string   `json:"path,omitempty"`
	Line       int      `json:"line,omitempty"`
	Column     int      `json:"column,omitempty"`
	Suggestion string   `json:"suggestion,omitempty"`
}

type CheckResult struct {
	ID          CheckID      `json:"id"`
	Status      CheckStatus  `json:"status"`
	ExitCode    *int         `json:"exitCode,omitempty"`
	DurationMS  int64        `json:"durationMs"`
	Output      string       `json:"output,omitempty"`
	Truncated   bool         `json:"truncated,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type QualityReport struct {
	ID                string                  `json:"id"`
	ProjectID         string                  `json:"projectId"`
	WorkflowRunID     *string                 `json:"workflowRunId,omitempty"`
	WorkspaceRevision core.VersionRef         `json:"workspaceRevision"`
	Status            string                  `json:"status"`
	Passed            bool                    `json:"passed"`
	Score             int                     `json:"score"`
	RunnerVersion     string                  `json:"runnerVersion"`
	SandboxKind       string                  `json:"sandboxKind"`
	Checks            []CheckResult           `json:"checks"`
	Diagnostics       []Diagnostic            `json:"diagnostics"`
	ReportArtifactID  string                  `json:"reportArtifactId,omitempty"`
	ReportRevisionID  string                  `json:"reportRevisionId,omitempty"`
	BuildArtifact     *BuildArtifactReference `json:"buildArtifact,omitempty"`
	CreatedBy         string                  `json:"createdBy"`
	StartedAt         time.Time               `json:"startedAt"`
	CompletedAt       *time.Time              `json:"completedAt,omitempty"`
	Version           uint64                  `json:"version"`
	ETag              string                  `json:"etag"`
}

// BuildArtifactReference pins the immutable, content-addressed output captured
// by a passing quality run. ContentHash identifies the canonical Mongo payload;
// BuildHash identifies the decoded file tree consumed by publish providers.
type BuildArtifactReference struct {
	ID          string `json:"id"`
	ContentRef  string `json:"contentRef"`
	ContentHash string `json:"contentHash"`
	BuildHash   string `json:"buildHash"`
	EntryPath   string `json:"entryPath"`
	FileCount   int    `json:"fileCount"`
	TotalBytes  int64  `json:"totalBytes"`
}

type BuildArtifactFile struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"contentBase64"`
}

// BuildArtifact is an immutable static output tree. It deliberately has a
// different type from WorkspaceSnapshot so a provider cannot accidentally
// deploy source files.
type BuildArtifact struct {
	ID                string              `json:"id"`
	WorkspaceRevision core.VersionRef     `json:"workspaceRevision"`
	BuildHash         string              `json:"buildHash"`
	EntryPath         string              `json:"entryPath"`
	Files             []BuildArtifactFile `json:"files"`
	FileCount         int                 `json:"fileCount"`
	TotalBytes        int64               `json:"totalBytes"`
}

type QualityRunInput struct {
	WorkspaceRevision core.VersionRef `json:"workspaceRevision"`
	WorkflowRunID     *string         `json:"workflowRunId,omitempty"`
}

type ExportKind string

const (
	ExportSource    ExportKind = "source"
	ExportDocument  ExportKind = "document"
	ExportBlueprint ExportKind = "blueprint"
	ExportPrototype ExportKind = "prototype"
)

type ExportInput struct {
	Kind            ExportKind       `json:"kind"`
	Revision        *core.VersionRef `json:"revision,omitempty"`
	BuildManifestID string           `json:"buildManifestId,omitempty"`
	RedactSensitive bool             `json:"redactSensitive"`
}

type Archive struct {
	Filename    string
	ContentType string
	Data        []byte
	FileCount   int
	Checksum    string
	Redactions  []string
}

type Environment string

const (
	EnvironmentPreview    Environment = "preview"
	EnvironmentProduction Environment = "production"
)

type DeploymentVersion struct {
	ID                       string                  `json:"id"`
	Number                   uint64                  `json:"number"`
	Action                   string                  `json:"action"`
	SourceVersionID          *string                 `json:"sourceVersionId,omitempty"`
	WorkspaceRevision        core.VersionRef         `json:"workspaceRevision"`
	BuildManifestID          *string                 `json:"buildManifestId,omitempty"`
	QualityRunID             *string                 `json:"qualityRunId,omitempty"`
	BuildArtifact            *BuildArtifactReference `json:"buildArtifact,omitempty"`
	Status                   string                  `json:"status"`
	PublicURL                string                  `json:"publicUrl,omitempty"`
	EntryPath                string                  `json:"entryPath"`
	Checksum                 string                  `json:"checksum"`
	FileCount                int                     `json:"fileCount"`
	TotalBytes               int64                   `json:"totalBytes"`
	EnvironmentRef           string                  `json:"environmentRef"`
	EnvironmentVariableNames []string                `json:"environmentVariableNames"`
	Message                  string                  `json:"message,omitempty"`
	CreatedBy                string                  `json:"createdBy"`
	CreatedAt                time.Time               `json:"createdAt"`
}

type Deployment struct {
	ID              string              `json:"id"`
	ProjectID       string              `json:"projectId"`
	Environment     Environment         `json:"environment"`
	EnvironmentRef  string              `json:"environmentRef"`
	Provider        string              `json:"provider"`
	Status          string              `json:"status"`
	ActiveVersionID *string             `json:"activeVersionId,omitempty"`
	PublicURL       string              `json:"publicUrl,omitempty"`
	Versions        []DeploymentVersion `json:"versions,omitempty"`
	Version         uint64              `json:"version"`
	ETag            string              `json:"etag"`
	LastError       string              `json:"lastError,omitempty"`
	CreatedBy       string              `json:"createdBy"`
	CreatedAt       time.Time           `json:"createdAt"`
	UpdatedAt       time.Time           `json:"updatedAt"`
}

type DeploymentLog struct {
	ID                  string    `json:"id"`
	DeploymentID        string    `json:"deploymentId"`
	DeploymentVersionID *string   `json:"deploymentVersionId,omitempty"`
	Sequence            uint64    `json:"sequence"`
	Level               string    `json:"level"`
	Message             string    `json:"message"`
	CreatedAt           time.Time `json:"createdAt"`
}

type PublishInput struct {
	DeploymentID      string           `json:"deploymentId,omitempty"`
	Environment       Environment      `json:"environment"`
	EnvironmentRef    string           `json:"environmentRef"`
	WorkspaceRevision *core.VersionRef `json:"workspaceRevision,omitempty"`
	BuildManifestID   string           `json:"buildManifestId,omitempty"`
	Message           string           `json:"message,omitempty"`
}

type RollbackInput struct {
	TargetVersionID string `json:"targetVersionId"`
	Message         string `json:"message,omitempty"`
}

type ResolvedEnvironment struct {
	Reference string
	Public    map[string]string
}

func (e Environment) Valid() bool {
	return e == EnvironmentPreview || e == EnvironmentProduction
}

func ValidateVersionRef(reference core.VersionRef) error {
	if _, err := uuid.Parse(reference.ArtifactID); err != nil {
		return Invalid("workspaceRevision.artifactId", "artifactId must be a UUID")
	}
	if _, err := uuid.Parse(reference.RevisionID); err != nil {
		return Invalid("workspaceRevision.revisionId", "revisionId must be a UUID")
	}
	digest := strings.TrimPrefix(reference.ContentHash, "sha256:")
	decoded, err := hex.DecodeString(digest)
	if !strings.HasPrefix(reference.ContentHash, "sha256:") || err != nil || len(decoded) != 32 || reference.ContentHash != "sha256:"+strings.ToLower(digest) {
		return Invalid("workspaceRevision", "an exact artifactId, revisionId and sha256 contentHash are required")
	}
	if reference.AnchorID != nil && (strings.TrimSpace(*reference.AnchorID) == "" || len(*reference.AnchorID) > 256 || strings.ContainsRune(*reference.AnchorID, '\x00')) {
		return Invalid("workspaceRevision.anchorId", "anchorId must contain 1 to 256 safe characters when provided")
	}
	return nil
}

func exactVersionRefEqual(left, right core.VersionRef) bool {
	if left.ArtifactID != right.ArtifactID || left.RevisionID != right.RevisionID || left.ContentHash != right.ContentHash {
		return false
	}
	if left.AnchorID == nil || right.AnchorID == nil {
		return left.AnchorID == nil && right.AnchorID == nil
	}
	return *left.AnchorID == *right.AnchorID
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func wrapInternal(detail string, err error) error {
	return &DeliveryError{Code: CodeInternal, Status: http.StatusInternalServerError, Detail: detail, Cause: err}
}

func conflict(detail string) error {
	return &DeliveryError{Code: CodeConflict, Status: http.StatusConflict, Detail: detail}
}

func notFound(detail string) error {
	return &DeliveryError{Code: CodeNotFound, Status: http.StatusNotFound, Detail: detail}
}

func ensureLength(field, value string, maximum int) error {
	if strings.TrimSpace(value) == "" || len(value) > maximum || strings.ContainsRune(value, '\x00') {
		return Invalid(field, fmt.Sprintf("%s must contain 1 to %d safe characters", field, maximum))
	}
	return nil
}
