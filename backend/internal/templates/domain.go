package templates

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	AdmissionAttemptSchemaVersion         = "template-admission-attempt/v1"
	AdmissionAttemptSchemaVersionV2       = "template-admission-attempt/v2"
	TemplateManifestSchemaVersion         = "template-manifest/v1"
	TemplateReleaseSchemaVersion          = "template-release/v1"
	TemplateReleaseSchemaVersionV2        = "template-release/v2"
	ReleasePolicySchemaVersion            = "template-release-policy/v1"
	ReleasePolicySchemaVersionV2          = "template-release-policy/v2"
	FullStackTemplateSchemaVersion        = "full-stack-template/v1"
	LanguageServerProfileSchemaVersion    = "language-server-profile/v1"
	LanguageServerCapabilitySchemaVersion = "language-server-capabilities/v1"
)

var (
	ErrInvalidTemplate      = errors.New("invalid template admission input")
	ErrInvalidTransition    = errors.New("invalid template admission state transition")
	ErrAdmissionRejected    = errors.New("template admission rejected")
	ErrReleaseNotSelectable = errors.New("template release is not selectable")
	ErrImmutableRelease     = errors.New("template release is immutable")
	ErrUnsupportedSchema    = errors.New("unsupported template schema")
)

// Error gives callers a stable field and code without coupling the domain to
// an HTTP transport.
type Error struct {
	Kind   error
	Code   string
	Field  string
	Detail string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Detail)
	}
	return fmt.Sprintf("%s (%s): %s", e.Code, e.Field, e.Detail)
}

func (e *Error) Unwrap() error { return e.Kind }

func invalid(code, field, detail string) error {
	return &Error{Kind: ErrInvalidTemplate, Code: code, Field: field, Detail: detail}
}

func transition(entity string, from, to fmt.Stringer) error {
	return &Error{
		Kind:   ErrInvalidTransition,
		Code:   "invalid_state_transition",
		Field:  entity,
		Detail: fmt.Sprintf("cannot transition from %q to %q", from.String(), to.String()),
	}
}

type AttemptStatus string

const (
	AttemptCandidate  AttemptStatus = "candidate"
	AttemptValidating AttemptStatus = "validating"
	AttemptApproved   AttemptStatus = "approved"
	AttemptRejected   AttemptStatus = "rejected"
)

func (s AttemptStatus) String() string { return string(s) }

type ReleasePolicyState string

const (
	ReleaseApproved   ReleasePolicyState = "approved"
	ReleaseDeprecated ReleasePolicyState = "deprecated"
	ReleaseRevoked    ReleasePolicyState = "revoked"
)

func (s ReleasePolicyState) String() string { return string(s) }

type EvidenceOutcome string

const (
	EvidencePassed EvidenceOutcome = "passed"
	EvidenceFailed EvidenceOutcome = "failed"
)

type AdmissionGate string

const (
	GateSourceIdentity       AdmissionGate = "source_identity"
	GateManifestSchema       AdmissionGate = "manifest_schema"
	GateLicenseSPDX          AdmissionGate = "license_spdx"
	GateDependencyLock       AdmissionGate = "dependency_lock"
	GateRegistryPolicy       AdmissionGate = "registry_policy"
	GateInstall              AdmissionGate = "install"
	GateLint                 AdmissionGate = "lint"
	GateTypecheck            AdmissionGate = "typecheck"
	GateUnitTest             AdmissionGate = "unit_test"
	GateBuild                AdmissionGate = "build"
	GateStartHealth          AdmissionGate = "start_health"
	GateContractSmoke        AdmissionGate = "contract_smoke"
	GateContainerBuild       AdmissionGate = "container_build"
	GateSecretScan           AdmissionGate = "secret_scan"
	GateSBOM                 AdmissionGate = "sbom"
	GateVulnerability        AdmissionGate = "vulnerability"
	GateSignatureAttestation AdmissionGate = "signature_attestation"
)

var requiredAdmissionGates = []AdmissionGate{
	GateSourceIdentity,
	GateManifestSchema,
	GateLicenseSPDX,
	GateDependencyLock,
	GateRegistryPolicy,
	GateInstall,
	GateLint,
	GateTypecheck,
	GateUnitTest,
	GateBuild,
	GateStartHealth,
	GateContractSmoke,
	GateContainerBuild,
	GateSecretScan,
	GateSBOM,
	GateVulnerability,
	GateSignatureAttestation,
}

// RequiredAdmissionGates returns a copy so callers cannot weaken the domain's
// fail-closed gate set.
func RequiredAdmissionGates() []AdmissionGate {
	return append([]AdmissionGate(nil), requiredAdmissionGates...)
}

type TemplateSource struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	TreeHash   string `json:"treeHash"`
}

type TemplateService struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	RootPath string `json:"rootPath"`
}

type Toolchain struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Image   string `json:"image"`
}

// Command is argv-based by design. A free-form shell string would make the
// manifest itself an execution-injection surface.
type Command struct {
	WorkingDirectory string   `json:"workingDirectory"`
	Argv             []string `json:"argv"`
}

type Port struct {
	Name      string `json:"name"`
	ServiceID string `json:"serviceId"`
	Number    int    `json:"number"`
	Protocol  string `json:"protocol"`
	Exposure  string `json:"exposure"`
}

type HealthCheck struct {
	ID        string `json:"id"`
	ServiceID string `json:"serviceId"`
	PortName  string `json:"portName"`
	Path      string `json:"path"`
}

type MigrationCommand struct {
	ServiceID   string `json:"serviceId"`
	CommandName string `json:"commandName"`
}

type BuildOutput struct {
	ServiceID string `json:"serviceId"`
	Path      string `json:"path"`
}

type EnvironmentVariable struct {
	Name        string  `json:"name"`
	Required    bool    `json:"required"`
	Secret      bool    `json:"secret"`
	Description string  `json:"description"`
	Default     *string `json:"default,omitempty"`
}

type Lockfile struct {
	Path     string `json:"path"`
	Digest   string `json:"digest"`
	Registry string `json:"registry"`
}

// LanguageServerProfile is an immutable, self-hashed declaration of one
// production language-server identity. It deliberately names every executable,
// capability, isolation, and resource input instead of discovering them from a
// mutable workspace or PATH at runtime.
type LanguageServerProfile struct {
	SchemaVersion                string                  `json:"schemaVersion"`
	ID                           string                  `json:"id"`
	ContentHash                  string                  `json:"contentHash"`
	ServiceID                    string                  `json:"serviceId"`
	LanguageIDs                  []string                `json:"languageIds"`
	FileGlobs                    []string                `json:"fileGlobs"`
	ProtocolVersion              string                  `json:"protocolVersion"`
	Runtime                      LanguageServerRuntime   `json:"runtime"`
	ServerInfo                   LanguageServerInfo      `json:"serverInfo"`
	InitializationParametersHash string                  `json:"initializationParametersHash"`
	WorkspaceConfigurationHash   string                  `json:"workspaceConfigurationHash"`
	RequireVersionedDiagnostics  bool                    `json:"requireVersionedDiagnostics"`
	Methods                      []string                `json:"methods"`
	CapabilityHash               string                  `json:"capabilityHash"`
	Limits                       LanguageServerLimits    `json:"limits"`
	Isolation                    LanguageServerIsolation `json:"isolation"`
}

type LanguageServerRuntime struct {
	Image                  string   `json:"image"`
	ExecutablePath         string   `json:"executablePath"`
	ExecutableDigest       string   `json:"executableDigest"`
	Argv                   []string `json:"argv"`
	WorkingDirectoryPolicy string   `json:"workingDirectoryPolicy"`
}

type LanguageServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// LanguageServerLimits are profile ceilings. The runtime takes the smaller of
// these values and the platform ceiling; admission rejects a profile which
// attempts to declare a value above the platform's v1 hard cap.
type LanguageServerLimits struct {
	StartupTimeoutMillis      int   `json:"startupTimeoutMillis"`
	RequestTimeoutMillis      int   `json:"requestTimeoutMillis"`
	ShutdownTimeoutMillis     int   `json:"shutdownTimeoutMillis"`
	CPUMillis                 int   `json:"cpuMillis"`
	MemoryBytes               int64 `json:"memoryBytes"`
	PIDLimit                  int   `json:"pidLimit"`
	TempBytes                 int64 `json:"tempBytes"`
	CacheBytes                int64 `json:"cacheBytes"`
	MaxOpenDocuments          int   `json:"maxOpenDocuments"`
	MaxDocumentBytes          int64 `json:"maxDocumentBytes"`
	MaxTotalSyncBytes         int64 `json:"maxTotalSyncBytes"`
	MaxFrameBytes             int64 `json:"maxFrameBytes"`
	MaxResultBytes            int64 `json:"maxResultBytes"`
	MaxConcurrentRequests     int   `json:"maxConcurrentRequests"`
	RequestsPerSecond         int   `json:"requestsPerSecond"`
	RequestBurst              int   `json:"requestBurst"`
	MaxDiagnosticsPerDocument int   `json:"maxDiagnosticsPerDocument"`
	MaxCompletionItems        int   `json:"maxCompletionItems"`
	MaxNavigationLocations    int   `json:"maxNavigationLocations"`
}

// String-valued policies are intentional: unlike a false boolean, an omitted
// policy cannot be confused with an explicit fail-closed prohibition.
type LanguageServerIsolation struct {
	NetworkPolicy              string `json:"networkPolicy"`
	WorkspaceMountPolicy       string `json:"workspaceMountPolicy"`
	TempPolicy                 string `json:"tempPolicy"`
	CachePolicy                string `json:"cachePolicy"`
	WorkspacePluginPolicy      string `json:"workspacePluginPolicy"`
	DynamicSDKPolicy           string `json:"dynamicSdkPolicy"`
	DynamicRegistrationPolicy  string `json:"dynamicRegistrationPolicy"`
	ConfigurationCommandPolicy string `json:"configurationCommandPolicy"`
	PackageManagerHookPolicy   string `json:"packageManagerHookPolicy"`
}

type TemplateManifest struct {
	SchemaVersion     string                  `json:"schemaVersion"`
	TemplateID        string                  `json:"templateId"`
	DisplayName       string                  `json:"displayName"`
	Version           string                  `json:"version"`
	Services          []TemplateService       `json:"services"`
	Toolchains        []Toolchain             `json:"toolchains"`
	Commands          map[string]Command      `json:"commands"`
	Ports             []Port                  `json:"ports"`
	HealthChecks      []HealthCheck           `json:"healthChecks"`
	Migration         *MigrationCommand       `json:"migration,omitempty"`
	BuildOutputs      []BuildOutput           `json:"buildOutputs"`
	ExtensionPaths    []string                `json:"extensionPaths"`
	ProtectedPaths    []string                `json:"protectedPaths"`
	EnvironmentSchema []EnvironmentVariable   `json:"environmentSchema"`
	Lockfiles         []Lockfile              `json:"lockfiles"`
	ProfileDigest     string                  `json:"profileDigest"`
	LanguageServers   []LanguageServerProfile `json:"languageServers,omitempty"`
}

type AdmissionCandidate struct {
	Source            TemplateSource   `json:"source"`
	Manifest          TemplateManifest `json:"manifest"`
	SBOMDigest        string           `json:"sbomDigest"`
	LicenseExpression string           `json:"licenseExpression"`
	LicenseDigest     string           `json:"licenseDigest"`
}

type GateEvidence struct {
	Gate         AdmissionGate   `json:"gate"`
	Outcome      EvidenceOutcome `json:"outcome"`
	SubjectHash  string          `json:"subjectHash"`
	Digest       string          `json:"digest"`
	Reference    string          `json:"reference"`
	Producer     string          `json:"producer"`
	InvocationID string          `json:"invocationId"`
	ObservedAt   time.Time       `json:"observedAt"`
}

type SignatureEnvelope struct {
	Format             string    `json:"format"`
	SubjectHash        string    `json:"subjectHash"`
	BundleDigest       string    `json:"bundleDigest"`
	Signer             string    `json:"signer"`
	TransparencyLogRef string    `json:"transparencyLogRef"`
	SignedAt           time.Time `json:"signedAt"`
}

type AdmissionFinding struct {
	Code     string        `json:"code"`
	Gate     AdmissionGate `json:"gate,omitempty"`
	Field    string        `json:"field,omitempty"`
	Severity string        `json:"severity"`
	Detail   string        `json:"detail"`
}

type AdmissionAttemptView struct {
	ID                string                       `json:"id"`
	SchemaVersion     string                       `json:"schemaVersion"`
	Status            AttemptStatus                `json:"status"`
	Version           uint64                       `json:"version"`
	Candidate         AdmissionCandidate           `json:"candidate"`
	SubjectHash       string                       `json:"subjectHash"`
	Evidence          []GateEvidence               `json:"evidence"`
	Signature         *SignatureEnvelope           `json:"signature,omitempty"`
	Findings          []AdmissionFinding           `json:"findings"`
	ApprovedReleaseID string                       `json:"approvedReleaseId,omitempty"`
	RequestedBy       string                       `json:"requestedBy"`
	EvaluatedBy       string                       `json:"evaluatedBy,omitempty"`
	CreatedAt         time.Time                    `json:"createdAt"`
	UpdatedAt         time.Time                    `json:"updatedAt"`
	EvaluatedAt       *time.Time                   `json:"evaluatedAt,omitempty"`
	AuthorityReceipt  *ArtifactAuthorityReceiptRef `json:"authorityReceipt,omitempty"`
}

type admissionAttemptDocument = AdmissionAttemptView

// AdmissionAttempt is a value-state machine. Its document is private so all
// status changes pass through the transition methods below.
type AdmissionAttempt struct {
	document admissionAttemptDocument
}

func (a AdmissionAttempt) Snapshot() AdmissionAttemptView {
	return cloneAttemptDocument(a.document)
}

func (a AdmissionAttempt) MarshalJSON() ([]byte, error) {
	return json.Marshal(a.document)
}

type TemplateReleaseView struct {
	ID                 string                       `json:"id"`
	SchemaVersion      string                       `json:"schemaVersion"`
	AdmissionAttemptID string                       `json:"admissionAttemptId"`
	Source             TemplateSource               `json:"source"`
	Manifest           TemplateManifest             `json:"manifest"`
	SBOMDigest         string                       `json:"sbomDigest"`
	LicenseExpression  string                       `json:"licenseExpression"`
	LicenseDigest      string                       `json:"licenseDigest"`
	EvidenceRefs       []GateEvidence               `json:"evidenceRefs"`
	Signature          SignatureEnvelope            `json:"signature"`
	SubjectHash        string                       `json:"subjectHash"`
	ContentHash        string                       `json:"contentHash"`
	ApprovedBy         string                       `json:"approvedBy"`
	ApprovedAt         time.Time                    `json:"approvedAt"`
	AuthorityReceipt   *ArtifactAuthorityReceiptRef `json:"authorityReceipt,omitempty"`
}

type templateReleaseDocument = TemplateReleaseView

// TemplateRelease has no mutation methods and keeps its document private.
// Deprecation and revocation belong to ReleasePolicy instead.
type TemplateRelease struct {
	document templateReleaseDocument
}

func (r TemplateRelease) Snapshot() TemplateReleaseView {
	return cloneReleaseDocument(r.document)
}

func (r TemplateRelease) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.document)
}

func (r TemplateRelease) ID() string          { return r.document.ID }
func (r TemplateRelease) ContentHash() string { return r.document.ContentHash }
func (r TemplateRelease) SubjectHash() string { return r.document.SubjectHash }

type ReleasePolicy struct {
	SchemaVersion      string                       `json:"schemaVersion"`
	TemplateReleaseID  string                       `json:"templateReleaseId"`
	ReleaseContentHash string                       `json:"releaseContentHash"`
	AuthorityReceipt   *ArtifactAuthorityReceiptRef `json:"authorityReceipt,omitempty"`
	State              ReleasePolicyState           `json:"state"`
	Version            uint64                       `json:"version"`
	Reason             string                       `json:"reason"`
	UpdatedBy          string                       `json:"updatedBy"`
	CreatedAt          time.Time                    `json:"createdAt"`
	UpdatedAt          time.Time                    `json:"updatedAt"`
}

type TemplateReleaseRef struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
	SubjectHash string `json:"subjectHash"`
}

type FullStackComponent struct {
	Role      string             `json:"role"`
	MountPath string             `json:"mountPath"`
	Release   TemplateReleaseRef `json:"release"`
}

type FullStackLayout struct {
	ContractTruthSource string `json:"contractTruthSource"`
	OpenAPIPath         string `json:"openapiPath"`
	GeneratedClientPath string `json:"generatedClientPath"`
	DeploymentPath      string `json:"deploymentPath"`
	TestPath            string `json:"testPath"`
	DatabaseEngine      string `json:"databaseEngine"`
}

type FullStackTemplateView struct {
	ID            string               `json:"id"`
	SchemaVersion string               `json:"schemaVersion"`
	TemplateID    string               `json:"templateId"`
	Version       string               `json:"version"`
	Components    []FullStackComponent `json:"components"`
	Layout        FullStackLayout      `json:"layout"`
	ContentHash   string               `json:"contentHash"`
	CreatedBy     string               `json:"createdBy"`
	CreatedAt     time.Time            `json:"createdAt"`
}

type fullStackTemplateDocument = FullStackTemplateView

type FullStackTemplate struct {
	document fullStackTemplateDocument
}

func (t FullStackTemplate) Snapshot() FullStackTemplateView {
	return cloneFullStackDocument(t.document)
}

func (t FullStackTemplate) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.document)
}

func (t FullStackTemplate) ID() string          { return t.document.ID }
func (t FullStackTemplate) ContentHash() string { return t.document.ContentHash }

type FullStackComponentInput struct {
	Role      string
	MountPath string
	Release   TemplateRelease
}
