package constructor

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	BuildContractSchemaVersion = "application-build-contract/v2"
	CompilerVersion            = "worksflow-constraint-compiler/v7"

	StatusReady      = "ready"
	StatusBlocked    = "blocked"
	StatusSuperseded = "superseded"
)

type CompilerIdentity struct {
	Version string `json:"version"`
	Hash    string `json:"hash"`
}

type ExactRevisionRef struct {
	Kind           string `json:"kind"`
	Purpose        string `json:"purpose"`
	Required       bool   `json:"required"`
	ArtifactID     string `json:"artifactId"`
	RevisionID     string `json:"revisionId"`
	ContentHash    string `json:"contentHash"`
	ApprovalStatus string `json:"approvalStatus"`
}

type PinnedBuildSource struct {
	Ref     ExactRevisionRef `json:"ref"`
	Content json.RawMessage  `json:"content"`
}

type BuildManifestRef struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

type WorkspaceRevisionRef struct {
	ArtifactID  string `json:"artifactId"`
	RevisionID  string `json:"revisionId"`
	ContentHash string `json:"contentHash"`
}

type TemplateReleaseRef struct {
	ID            string `json:"id"`
	ReleaseHash   string `json:"releaseHash"`
	Role          string `json:"role"`
	Certification string `json:"certification"`
	PolicyStatus  string `json:"policyStatus"`
}

type FullStackTemplateRef struct {
	ID            string `json:"id"`
	ContentHash   string `json:"contentHash"`
	Certification string `json:"certification"`
	PolicyStatus  string `json:"policyStatus"`
}

// TemplateRuntimeFacts is a trusted projection of the exact immutable
// FullStackTemplate and TemplateRelease manifests selected by Template
// Registry. It is compiler input only: the exact template/release hashes in
// the BuildContract remain the durable commitment, while these facts let the
// compiler reject Deployment Contract drift without asking a model to infer
// runtime layout from prose.
type TemplateRuntimeFacts struct {
	FullStackTemplateID   string                     `json:"fullStackTemplateId"`
	FullStackTemplateHash string                     `json:"fullStackTemplateHash"`
	Layout                TemplateRuntimeLayout      `json:"layout"`
	Components            []TemplateRuntimeComponent `json:"components"`
}

type TemplateRuntimeLayout struct {
	ContractTruthSource string `json:"contractTruthSource"`
	OpenAPIPath         string `json:"openapiPath"`
	GeneratedClientPath string `json:"generatedClientPath"`
	DeploymentPath      string `json:"deploymentPath"`
	TestPath            string `json:"testPath"`
	DatabaseEngine      string `json:"databaseEngine"`
}

type TemplateRuntimeComponent struct {
	Role                  string                               `json:"role"`
	MountPath             string                               `json:"mountPath"`
	ReleaseID             string                               `json:"releaseId"`
	ReleaseHash           string                               `json:"releaseHash"`
	ManifestSchemaVersion string                               `json:"manifestSchemaVersion"`
	Services              []TemplateRuntimeService             `json:"services"`
	Commands              []string                             `json:"commands"`
	Ports                 []TemplateRuntimePort                `json:"ports"`
	HealthChecks          []TemplateRuntimeHealthCheck         `json:"healthChecks"`
	Migration             *TemplateRuntimeMigration            `json:"migration,omitempty"`
	BuildOutputs          []TemplateRuntimeBuildOutput         `json:"buildOutputs"`
	EnvironmentVariables  []TemplateRuntimeEnvironmentVariable `json:"environmentVariables"`
}

type TemplateRuntimeService struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	RootPath string `json:"rootPath"`
}

type TemplateRuntimePort struct {
	Name      string `json:"name"`
	ServiceID string `json:"serviceId"`
	Number    int    `json:"number"`
	Protocol  string `json:"protocol"`
}

type TemplateRuntimeHealthCheck struct {
	ID        string `json:"id"`
	ServiceID string `json:"serviceId"`
	PortName  string `json:"portName"`
	Path      string `json:"path"`
}

type TemplateRuntimeMigration struct {
	ServiceID   string `json:"serviceId"`
	CommandName string `json:"commandName"`
}

type TemplateRuntimeBuildOutput struct {
	ServiceID string `json:"serviceId"`
	Path      string `json:"path"`
}

type TemplateRuntimeEnvironmentVariable struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Secret   bool   `json:"secret"`
	// Scope is the deterministic Deployment v1 bridge projection derived from
	// the component role; TemplateManifest v1 does not declare it directly.
	Scope string `json:"scope"`
}

// CompileInput is an internal trusted value. HTTP clients may select only an
// exact FullStackTemplate ref; a service adapter must load every source and
// release fact from canonical storage before invoking Compiler.Compile.
type CompileInput struct {
	ProjectID       string
	DeliverySliceID string
	// DeliverySlicePageNodeID is the exact Blueprint anchor carried by the
	// Workbench bundle. DeliverySliceID is an orchestration UUID and must not be
	// confused with the semantic Blueprint page identity.
	DeliverySlicePageNodeID string
	BuildManifest           BuildManifestRef
	BaseWorkspace           *WorkspaceRevisionRef
	Sources                 []PinnedBuildSource
	FullStackTemplate       FullStackTemplateRef
	TemplateReleaseRefs     []TemplateReleaseRef
	TemplateRuntime         *TemplateRuntimeFacts
	ForbiddenClaims         []string
}

type RouteConstraint struct {
	PageNodeID             string   `json:"pageNodeId"`
	Route                  string   `json:"route"`
	RequiredRoles          []string `json:"requiredRoles"`
	AcceptanceCriterionIDs []string `json:"acceptanceCriterionIds"`
}

type StateConstraint struct {
	PageNodeID string `json:"pageNodeId"`
	ID         string `json:"id"`
	Key        string `json:"key"`
	Required   bool   `json:"required"`
}

type ContractBinding struct {
	ID             string           `json:"id"`
	Kind           string           `json:"kind"`
	TargetID       string           `json:"targetId"`
	SourceRevision ExactRevisionRef `json:"sourceRevision"`
}

type AcceptanceCriterion struct {
	ID             string           `json:"id"`
	Statement      string           `json:"statement"`
	RequirementIDs []string         `json:"requirementIds"`
	SourceRevision ExactRevisionRef `json:"sourceRevision"`
}

type Oracle struct {
	ID                     string           `json:"id"`
	AcceptanceCriterionIDs []string         `json:"acceptanceCriterionIds"`
	Kind                   string           `json:"kind"`
	Target                 string           `json:"target"`
	CommandID              string           `json:"commandId,omitempty"`
	SourceRevision         ExactRevisionRef `json:"sourceRevision"`
}

type Obligation struct {
	ID               string           `json:"id"`
	Level            string           `json:"level"`
	Kind             string           `json:"kind"`
	SourceRevision   ExactRevisionRef `json:"sourceRevision"`
	SourceAnchorID   string           `json:"sourceAnchorId"`
	OracleIDs        []string         `json:"oracleIds"`
	DependsOn        []string         `json:"dependsOn"`
	Waivable         bool             `json:"waivable"`
	Status           string           `json:"status"`
	BlockingReasonID string           `json:"blockingReasonId,omitempty"`
}

type BuildGap struct {
	ID            string   `json:"id"`
	Code          string   `json:"code"`
	Path          string   `json:"path"`
	Message       string   `json:"message"`
	SourceID      string   `json:"sourceId,omitempty"`
	ObligationIDs []string `json:"obligationIds"`
	Blocking      bool     `json:"blocking"`
}

type BuildConflict struct {
	ID        string   `json:"id"`
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	SourceIDs []string `json:"sourceIds"`
	Blocking  bool     `json:"blocking"`
}

type Waiver struct {
	ID                  string    `json:"id"`
	ObligationIDs       []string  `json:"obligationIds"`
	Reason              string    `json:"reason"`
	ApprovedBy          string    `json:"approvedBy"`
	ExpiresAt           time.Time `json:"expiresAt"`
	AlternativeOracleID string    `json:"alternativeOracleId"`
}

type ContractContent struct {
	SchemaVersion       string                `json:"schemaVersion"`
	Compiler            CompilerIdentity      `json:"compiler"`
	ProjectID           string                `json:"projectId"`
	DeliverySliceID     string                `json:"deliverySliceId"`
	BuildManifest       BuildManifestRef      `json:"buildManifest"`
	BaseWorkspace       *WorkspaceRevisionRef `json:"baseWorkspaceRevision,omitempty"`
	SourceRevisions     []ExactRevisionRef    `json:"sourceRevisions"`
	FullStackTemplate   FullStackTemplateRef  `json:"fullStackTemplate"`
	TemplateReleaseRefs []TemplateReleaseRef  `json:"templateReleaseRefs"`
	Routes              []RouteConstraint     `json:"routes"`
	States              []StateConstraint     `json:"states"`
	ContractBindings    []ContractBinding     `json:"contractBindings"`
	AcceptanceCriteria  []AcceptanceCriterion `json:"acceptanceCriteria"`
	Oracles             []Oracle              `json:"oracles"`
	Obligations         []Obligation          `json:"obligations"`
	Waivers             []Waiver              `json:"waivers"`
	Gaps                []BuildGap            `json:"gaps"`
	Conflicts           []BuildConflict       `json:"conflicts"`
	ForbiddenClaims     []string              `json:"forbiddenClaims"`
	Status              string                `json:"status"`
}

type CompiledContract struct {
	Content      ContractContent `json:"content"`
	ContentHash  string          `json:"contentHash"`
	ContractHash string          `json:"contractHash"`
}

type ApplicationBuildContract struct {
	ID              string          `json:"id"`
	ProjectID       string          `json:"projectId"`
	BuildManifestID string          `json:"buildManifestId"`
	Status          string          `json:"status"`
	Version         uint64          `json:"version"`
	ETag            string          `json:"etag"`
	ContentHash     string          `json:"contentHash"`
	ContractHash    string          `json:"contractHash"`
	Contract        ContractContent `json:"contract"`
	MustCount       int             `json:"mustCount"`
	MustReadyCount  int             `json:"mustReadyCount"`
	BlockingCount   int             `json:"blockingCount"`
	ConflictCount   int             `json:"conflictCount"`
	CreatedBy       string          `json:"createdBy"`
	CreatedAt       time.Time       `json:"createdAt"`
	SupersededAt    *time.Time      `json:"supersededAt,omitempty"`
}

// MarshalJSON guarantees that the public representation always exposes the
// canonical hash of Contract, including for values assembled by legacy
// persistence adapters that predate the explicit ContractHash field.
func (value ApplicationBuildContract) MarshalJSON() ([]byte, error) {
	type wireApplicationBuildContract ApplicationBuildContract
	wire := wireApplicationBuildContract(value)
	canonicalHash, err := domain.CanonicalHash(wire.Contract)
	if err != nil {
		return nil, fmt.Errorf("hash Application Build Contract content: %w", err)
	}
	if wire.ContractHash != "" && wire.ContractHash != canonicalHash {
		return nil, fmt.Errorf("Application Build Contract contractHash does not match canonical contract content")
	}
	wire.ContractHash = canonicalHash
	return json.Marshal(wire)
}

// UnmarshalJSON rejects a claimed contractHash that does not authenticate the
// embedded canonical Contract content. The field is mandatory on the public
// wire representation.
func (value *ApplicationBuildContract) UnmarshalJSON(payload []byte) error {
	type wireApplicationBuildContract ApplicationBuildContract
	var wire wireApplicationBuildContract
	if err := json.Unmarshal(payload, &wire); err != nil {
		return err
	}
	canonicalHash, err := domain.CanonicalHash(wire.Contract)
	if err != nil {
		return fmt.Errorf("hash Application Build Contract content: %w", err)
	}
	if wire.ContractHash == "" {
		return fmt.Errorf("Application Build Contract contractHash is required")
	}
	if wire.ContractHash != canonicalHash {
		return fmt.Errorf("Application Build Contract contractHash does not match canonical contract content")
	}
	*value = ApplicationBuildContract(wire)
	return nil
}
