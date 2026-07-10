package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
)

var (
	ErrNoRunnableNode = errors.New("no runnable workflow node")
	ErrLeaseLost      = errors.New("workflow node lease lost")
	ErrCASConflict    = errors.New("workflow compare-and-swap conflict")
	ErrRunnerNotFound = errors.New("workflow node runner not found")
	ErrRunTerminal    = errors.New("workflow run is terminal")
)

type RunStatus string

const (
	RunPending       RunStatus = "pending"
	RunRunning       RunStatus = "running"
	RunWaitingInput  RunStatus = "waiting_input"
	RunWaitingReview RunStatus = "waiting_review"
	RunCompleted     RunStatus = "completed"
	RunFailed        RunStatus = "failed"
	RunCancelled     RunStatus = "cancelled"
	RunStale         RunStatus = "stale"
)

func (s RunStatus) Terminal() bool {
	return s == RunCompleted || s == RunFailed || s == RunCancelled || s == RunStale
}

type NodeStatus string

const (
	NodePending       NodeStatus = "pending"
	NodeReady         NodeStatus = "ready"
	NodeRunning       NodeStatus = "running"
	NodeWaitingInput  NodeStatus = "waiting_input"
	NodeWaitingReview NodeStatus = "waiting_review"
	NodeCompleted     NodeStatus = "completed"
	NodeFailed        NodeStatus = "failed"
	NodeCancelled     NodeStatus = "cancelled"
	NodeStale         NodeStatus = "stale"
)

func (s NodeStatus) Terminal() bool {
	return s == NodeCompleted || s == NodeFailed || s == NodeCancelled || s == NodeStale
}

type DefinitionRecord struct {
	VersionID   string
	ProjectID   string
	Key         string
	Title       string
	Description string
	Published   bool
	Definition  domain.WorkflowDefinition
}

type NodeMetadata struct {
	DefinitionNodeID string                     `json:"definitionNodeId"`
	SliceID          string                     `json:"sliceId,omitempty"`
	MaxAttempts      int                        `json:"maxAttempts"`
	TimeoutNanos     int64                      `json:"timeoutNanos"`
	Waived           bool                       `json:"waived,omitempty"`
	WaiverReason     string                     `json:"waiverReason,omitempty"`
	SelectedBranch   string                     `json:"selectedBranch,omitempty"`
	Input            json.RawMessage            `json:"input,omitempty"`
	Output           json.RawMessage            `json:"output,omitempty"`
	FanOutOutputs    map[string]json.RawMessage `json:"fanOutOutputs,omitempty"`
}

type SliceContext struct {
	ID           string              `json:"id"`
	Key          string              `json:"key"`
	Title        string              `json:"title"`
	FanOutNodeID string              `json:"fanOutNodeId"`
	Blueprint    domain.ArtifactRef  `json:"blueprint"`
	PageSpec     *domain.ArtifactRef `json:"pageSpec,omitempty"`
	Prototype    *domain.ArtifactRef `json:"prototype,omitempty"`
	OwnerID      string              `json:"ownerId,omitempty"`
}

type RunContext struct {
	Values           map[string]json.RawMessage `json:"values,omitempty"`
	Nodes            map[string]NodeMetadata    `json:"nodes"`
	DisabledEdges    map[string]bool            `json:"disabledEdges,omitempty"`
	SelectedBranches map[string]string          `json:"selectedBranches,omitempty"`
	Slices           map[string]SliceContext    `json:"slices,omitempty"`
}

// ReviewGateVerifier ties a workflow transition to canonical artifact review
// state. Workflow approval is orchestration metadata, never a second source of
// truth for whether a document, blueprint, or prototype revision is approved.
type ReviewGateVerifier interface {
	VerifyApproval(context.Context, string, []domain.ArtifactRef, domain.ReviewGateNodeConfig) error
}

type ReviewGateVerifierFunc func(context.Context, string, []domain.ArtifactRef, domain.ReviewGateNodeConfig) error

func (f ReviewGateVerifierFunc) VerifyApproval(ctx context.Context, projectID string, refs []domain.ArtifactRef, config domain.ReviewGateNodeConfig) error {
	return f(ctx, projectID, refs, config)
}

func NewRunContext() RunContext {
	return RunContext{
		Values: map[string]json.RawMessage{}, Nodes: map[string]NodeMetadata{},
		DisabledEdges: map[string]bool{}, SelectedBranches: map[string]string{}, Slices: map[string]SliceContext{},
	}
}

func (c *RunContext) ensureMaps() {
	if c.Values == nil {
		c.Values = map[string]json.RawMessage{}
	}
	if c.Nodes == nil {
		c.Nodes = map[string]NodeMetadata{}
	}
	if c.DisabledEdges == nil {
		c.DisabledEdges = map[string]bool{}
	}
	if c.SelectedBranches == nil {
		c.SelectedBranches = map[string]string{}
	}
	if c.Slices == nil {
		c.Slices = map[string]SliceContext{}
	}
}

func artifactRefsFromNodeOutput(output json.RawMessage) ([]domain.ArtifactRef, error) {
	var envelope struct {
		ArtifactRevision  *domain.ArtifactRef  `json:"artifactRevision"`
		ArtifactRevisions []domain.ArtifactRef `json:"artifactRevisions"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		return nil, err
	}
	refs := append([]domain.ArtifactRef(nil), envelope.ArtifactRevisions...)
	if envelope.ArtifactRevision != nil {
		refs = append(refs, *envelope.ArtifactRevision)
	}
	result := make([]domain.ArtifactRef, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return nil, err
		}
		key := ref.ArtifactID + "\x00" + ref.RevisionID + "\x00" + ref.ContentHash + "\x00" + ref.AnchorID
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, ref)
	}
	return result, nil
}

type RunRecord struct {
	ID                  string                       `json:"id"`
	ProjectID           string                       `json:"projectId"`
	DefinitionVersionID string                       `json:"definitionVersionId"`
	Definition          domain.WorkflowDefinitionRef `json:"definition"`
	InputManifest       *domain.ManifestRef          `json:"inputManifest,omitempty"`
	Status              RunStatus                    `json:"status"`
	Scope               json.RawMessage              `json:"scope"`
	Context             RunContext                   `json:"context"`
	EventCursor         uint64                       `json:"eventCursor"`
	StartedBy           string                       `json:"startedBy"`
	StartedAt           *time.Time                   `json:"startedAt,omitempty"`
	CompletedAt         *time.Time                   `json:"completedAt,omitempty"`
	CancelledAt         *time.Time                   `json:"cancelledAt,omitempty"`
	Failure             json.RawMessage              `json:"failure,omitempty"`
	CreatedAt           time.Time                    `json:"createdAt"`
	UpdatedAt           time.Time                    `json:"updatedAt"`
	Nodes               map[string]*NodeRecord       `json:"nodes"`
}

// RunSummary is the bounded project-history representation. Detailed context,
// node outputs, and immutable manifest references remain available from GetRun.
type RunSummary struct {
	ID                  string          `json:"id"`
	ProjectID           string          `json:"projectId"`
	DefinitionVersionID string          `json:"definitionVersionId"`
	Status              RunStatus       `json:"status"`
	EventCursor         uint64          `json:"eventCursor"`
	StartedBy           string          `json:"startedBy"`
	StartedAt           *time.Time      `json:"startedAt,omitempty"`
	CompletedAt         *time.Time      `json:"completedAt,omitempty"`
	CancelledAt         *time.Time      `json:"cancelledAt,omitempty"`
	Failure             json.RawMessage `json:"failure,omitempty"`
	CreatedAt           time.Time       `json:"createdAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
}

type RunListOptions struct {
	Status RunStatus
	Limit  int
	Cursor string
}

type RunPage struct {
	Items      []RunSummary `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

type StoreRunFilter struct {
	Status          RunStatus
	Limit           int
	BeforeCreatedAt *time.Time
	BeforeID        string
}

func (r *RunRecord) Validate() error {
	if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.ProjectID) == "" || strings.TrimSpace(r.DefinitionVersionID) == "" || strings.TrimSpace(r.StartedBy) == "" {
		return fmt.Errorf("run id, project, definition version and starter are required")
	}
	if err := r.Definition.Validate(); err != nil {
		return err
	}
	if r.InputManifest != nil {
		if err := r.InputManifest.Validate(); err != nil {
			return err
		}
	}
	if len(r.Nodes) == 0 {
		return fmt.Errorf("run must contain node executions")
	}
	r.Context.ensureMaps()
	return nil
}

type NodeRecord struct {
	ID               string                  `json:"id"`
	RunID            string                  `json:"runId"`
	Key              string                  `json:"key"`
	DefinitionNodeID string                  `json:"definitionNodeId"`
	SliceID          string                  `json:"sliceId,omitempty"`
	Type             domain.WorkflowNodeType `json:"type"`
	Status           NodeStatus              `json:"status"`
	Attempt          int                     `json:"attempt"`
	InputManifest    *domain.ManifestRef     `json:"inputManifest,omitempty"`
	OutputProposal   *domain.ProposalRef     `json:"outputProposal,omitempty"`
	OutputRevisionID string                  `json:"outputRevisionId,omitempty"`
	LeaseOwner       string                  `json:"-"`
	LeaseExpiresAt   *time.Time              `json:"leaseExpiresAt,omitempty"`
	AvailableAt      time.Time               `json:"availableAt"`
	StartedAt        *time.Time              `json:"startedAt,omitempty"`
	CompletedAt      *time.Time              `json:"completedAt,omitempty"`
	Failure          json.RawMessage         `json:"failure,omitempty"`
	CreatedAt        time.Time               `json:"createdAt"`
	UpdatedAt        time.Time               `json:"updatedAt"`
}

type Lease struct {
	RunID          string
	NodeID         string
	NodeKey        string
	WorkerID       string
	Attempt        int
	LeaseExpiresAt time.Time
}

type Event struct {
	ID        string          `json:"id"`
	RunID     string          `json:"runId"`
	Sequence  uint64          `json:"sequence"`
	Type      string          `json:"type"`
	NodeKey   string          `json:"nodeKey,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	ActorID   string          `json:"actorId,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

type SliceRecord struct {
	ID                  string
	ProjectID           string
	Key                 string
	Title               string
	BlueprintRevisionID string
	PageSpecRevisionID  string
	PrototypeRevisionID string
	SyncStatus          string
	WorkflowStatus      string
	OwnerID             string
	BlockerReason       string
	UpdatedAt           time.Time
}

type NodeMutation struct {
	Node           NodeRecord
	ExpectedStatus NodeStatus
	ExpectedOwner  string
}

type RunMutation struct {
	RunID          string
	ExpectedCursor uint64
	Status         RunStatus
	Context        RunContext
	Failure        json.RawMessage
	CompletedAt    *time.Time
	CancelledAt    *time.Time
	Nodes          []NodeMutation
	NewNodes       []NodeRecord
	Slices         []SliceRecord
	Events         []Event
	UpdatedAt      time.Time
}

type Store interface {
	SaveDefinition(context.Context, DefinitionRecord) error
	GetDefinition(context.Context, string, int) (DefinitionRecord, error)
	GetDefinitionVersion(context.Context, string) (DefinitionRecord, error)
	ListDefinitions(context.Context, string) ([]DefinitionRecord, error)
	ListDefinitionVersions(context.Context, string) ([]DefinitionRecord, error)
	PublishDefinitionVersion(context.Context, string, string, string, string) (DefinitionRecord, error)

	SaveManifest(context.Context, domain.InputManifest) error
	GetManifest(context.Context, string) (domain.InputManifest, error)
	SaveProposal(context.Context, *domain.OutputProposal) error
	GetProposal(context.Context, string) (*domain.OutputProposal, error)

	CreateRun(context.Context, *RunRecord, []Event) error
	GetRun(context.Context, string) (*RunRecord, error)
	ListRuns(context.Context, string, StoreRunFilter) ([]RunSummary, error)
	ClaimRunnable(context.Context, string, time.Time, time.Duration) (Lease, error)
	RenewLease(context.Context, Lease, time.Time, time.Duration) (Lease, error)
	Commit(context.Context, RunMutation) error
	ListEvents(context.Context, string, uint64, int) ([]Event, error)
}

type ContentStore interface {
	Put(context.Context, string, string, []byte) (store, ref, hash string, err error)
	Get(context.Context, string, string, string) ([]byte, error)
}

type IDGenerator interface {
	NewID() string
}

type WorkerRunner interface {
	Run(context.Context, Execution) (WorkerResult, error)
}

type RunnerRegistry interface {
	RunnerFor(domain.WorkflowNodeType) (WorkerRunner, bool)
}

type Execution struct {
	Run        RunRecord
	Node       NodeRecord
	Definition domain.NodeDefinition
	Lease      Lease
	Inputs     domain.NodeInputEnvelope
}

// IncomingValues returns immutable copies of the mapped inputs for a port.
// Quality, publish, and other adapters should use this instead of scanning
// Run.Context for whichever completed node happens to be latest.
func (e Execution) IncomingValues(port string) []json.RawMessage {
	return e.Inputs.Values(port)
}

// DecodeIncoming decodes the single incoming value for a port. It fails when
// the port is absent or ambiguous so adapters cannot silently select stale
// global state in a multi-edge DAG.
func (e Execution) DecodeIncoming(port string, target any) error {
	values := e.Inputs.Values(port)
	if len(values) != 1 {
		return fmt.Errorf("incoming port %q requires exactly one value, got %d", normalizedPort(port), len(values))
	}
	if target == nil {
		return fmt.Errorf("incoming decode target is required")
	}
	if err := json.Unmarshal(values[0], target); err != nil {
		return fmt.Errorf("decode incoming port %q: %w", normalizedPort(port), err)
	}
	return nil
}

func normalizedPort(port string) string {
	if strings.TrimSpace(port) == "" {
		return "default"
	}
	return strings.TrimSpace(port)
}

type ResultDisposition string

const (
	ResultComplete   ResultDisposition = "complete"
	ResultWaitInput  ResultDisposition = "wait_input"
	ResultWaitReview ResultDisposition = "wait_review"
)

type FanOutItem struct {
	Key       string              `json:"key"`
	Title     string              `json:"title"`
	Blueprint domain.ArtifactRef  `json:"blueprint"`
	PageSpec  *domain.ArtifactRef `json:"pageSpec,omitempty"`
	Prototype *domain.ArtifactRef `json:"prototype,omitempty"`
	OwnerID   string              `json:"ownerId,omitempty"`
}

type WorkerResult struct {
	Disposition   ResultDisposition
	Output        json.RawMessage
	Manifest      *domain.InputManifest
	Proposal      *domain.ProposalRef
	Branch        string
	FanOutItems   []FanOutItem
	BuildManifest *BuildManifest
}

type BuildManifest struct {
	SchemaVersion int                  `json:"schemaVersion"`
	ProjectID     string               `json:"projectId"`
	RunID         string               `json:"runId"`
	SliceIDs      []string             `json:"sliceIds"`
	BundleIDs     []string             `json:"bundleIds,omitempty"`
	Sources       []domain.ArtifactRef `json:"sources"`
	Constraints   json.RawMessage      `json:"constraints"`
	CreatedAt     time.Time            `json:"createdAt"`
	Hash          string               `json:"hash"`
}

func buildManifestFromExecution(execution Execution) (BuildManifest, error) {
	manifests := make(map[string]BuildManifest)
	for _, binding := range execution.Inputs.Bindings() {
		for _, raw := range []json.RawMessage{binding.Value, binding.Output} {
			var manifest BuildManifest
			if err := json.Unmarshal(raw, &manifest); err != nil || manifest.Validate() != nil {
				continue
			}
			manifests[manifest.Hash] = manifest
		}
	}
	if len(manifests) != 1 {
		return BuildManifest{}, fmt.Errorf("workbench build requires exactly one incoming frozen application build manifest, got %d", len(manifests))
	}
	for _, manifest := range manifests {
		return manifest, nil
	}
	return BuildManifest{}, fmt.Errorf("incoming build manifest is unavailable")
}

func (m *BuildManifest) Freeze() error {
	if err := m.validateFields(); err != nil {
		return err
	}
	if len(m.Constraints) == 0 {
		m.Constraints = json.RawMessage(`{}`)
	}
	if _, err := domain.CanonicalJSON(m.Constraints); err != nil {
		return err
	}
	payload := *m
	payload.Hash = ""
	hash, err := domain.CanonicalHash(payload)
	if err != nil {
		return err
	}
	m.Hash = hash
	return nil
}

func (m BuildManifest) Validate() error {
	if err := m.validateFields(); err != nil {
		return err
	}
	copyManifest := m
	expected := copyManifest.Hash
	copyManifest.Hash = ""
	hash, err := domain.CanonicalHash(copyManifest)
	if err != nil {
		return err
	}
	if !domain.IsCanonicalHash(expected) || hash != expected {
		return fmt.Errorf("build manifest hash mismatch")
	}
	return nil
}

func (m BuildManifest) validateFields() error {
	if m.SchemaVersion < 1 || strings.TrimSpace(m.ProjectID) == "" || strings.TrimSpace(m.RunID) == "" {
		return fmt.Errorf("build manifest schemaVersion, projectId and runId are required")
	}
	if len(m.SliceIDs) == 0 || len(m.BundleIDs) == 0 || len(m.SliceIDs) != len(m.BundleIDs) {
		return fmt.Errorf("build manifest requires one bundle per delivery slice")
	}
	seenSlices, seenBundles := map[string]bool{}, map[string]bool{}
	for index := range m.SliceIDs {
		if strings.TrimSpace(m.SliceIDs[index]) == "" || strings.TrimSpace(m.BundleIDs[index]) == "" || seenSlices[m.SliceIDs[index]] || seenBundles[m.BundleIDs[index]] {
			return fmt.Errorf("build manifest slice and bundle pins must be non-empty and unique")
		}
		seenSlices[m.SliceIDs[index]], seenBundles[m.BundleIDs[index]] = true, true
	}
	if len(m.Sources) == 0 {
		return fmt.Errorf("build manifest requires pinned artifact sources")
	}
	for _, source := range m.Sources {
		if err := source.Validate(); err != nil {
			return err
		}
	}
	if len(m.Constraints) > 0 {
		if _, err := domain.CanonicalJSON(m.Constraints); err != nil {
			return err
		}
	}
	return nil
}

type BuildManifestHook interface {
	Compile(context.Context, Execution) (BuildManifest, error)
}

type ManifestFreezer interface {
	Freeze(context.Context, Execution) (domain.InputManifest, error)
}

type ArtifactInputValidator interface {
	Validate(context.Context, Execution, domain.InputManifest) (json.RawMessage, error)
}

type WorkbenchCompletionValidator interface {
	ValidateCompletion(context.Context, Execution, json.RawMessage) (string, error)
}

type ProposalDispatcher interface {
	Dispatch(context.Context, Execution, domain.InputManifest) (*domain.ProposalRef, error)
}

type ConditionEvaluator interface {
	Evaluate(context.Context, Execution, []domain.ConditionBranch) (string, error)
}
