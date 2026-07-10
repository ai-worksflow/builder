package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type WorkflowNodeType string

const (
	NodeArtifactInput    WorkflowNodeType = "artifact_input"
	NodeAITransform      WorkflowNodeType = "ai_transform"
	NodeHumanEdit        WorkflowNodeType = "human_edit"
	NodeReviewGate       WorkflowNodeType = "review_gate"
	NodeCondition        WorkflowNodeType = "condition"
	NodeFanOut           WorkflowNodeType = "fan_out"
	NodeMerge            WorkflowNodeType = "merge"
	NodeQualityGate      WorkflowNodeType = "quality_gate"
	NodeManifestCompiler WorkflowNodeType = "manifest_compiler"
	NodeWorkbenchBuild   WorkflowNodeType = "workbench_build"
	NodePublish          WorkflowNodeType = "publish"

	// Legacy types remain valid so persisted v1 definitions can still be replayed.
	NodeAI        WorkflowNodeType = "ai"
	NodeHumanTask WorkflowNodeType = "human_task"
	NodeApproval  WorkflowNodeType = "approval"
	NodeTransform WorkflowNodeType = "transform"
	NodeDelivery  WorkflowNodeType = "delivery"
)

type PortDefinition struct {
	Schema      json.RawMessage `json:"schema"`
	Description string          `json:"description,omitempty"`
}

type ArtifactInputNodeConfig struct {
	AllowedTypes     []ArtifactType `json:"allowedTypes"`
	RequireApproved  bool           `json:"requireApproved"`
	MinimumArtifacts int            `json:"minimumArtifacts"`
}

type AITransformNodeConfig struct {
	JobType             string        `json:"jobType"`
	ModelPolicy         string        `json:"modelPolicy"`
	OutputSchemaVersion string        `json:"outputSchemaVersion"`
	MaxAttempts         int           `json:"maxAttempts"`
	Timeout             time.Duration `json:"timeout"`
}

type HumanEditNodeConfig struct {
	ArtifactType ArtifactType `json:"artifactType"`
	RequiredRole string       `json:"requiredRole"`
	Instructions string       `json:"instructions,omitempty"`
}

type ReviewGateNodeConfig struct {
	RequiredRole       string `json:"requiredRole"`
	MinimumApprovals   int    `json:"minimumApprovals"`
	ProhibitSelfReview bool   `json:"prohibitSelfReview"`
	AllowWaiver        bool   `json:"allowWaiver"`
}

type ConditionBranch struct {
	Name       string `json:"name"`
	Expression string `json:"expression,omitempty"`
	Default    bool   `json:"default"`
}

type ConditionNodeConfig struct {
	Branches []ConditionBranch `json:"branches"`
}

type FanOutNodeConfig struct {
	ItemsPath    string `json:"itemsPath"`
	SliceKeyPath string `json:"sliceKeyPath"`
	MergeNodeID  string `json:"mergeNodeId"`
	MaxParallel  int    `json:"maxParallel"`
}

type MergePolicy string

const (
	MergeAll    MergePolicy = "all"
	MergeAny    MergePolicy = "any"
	MergeQuorum MergePolicy = "quorum"
)

type MergeNodeConfig struct {
	FanOutNodeID string      `json:"fanOutNodeId"`
	Policy       MergePolicy `json:"policy"`
	Quorum       int         `json:"quorum,omitempty"`
	AllowWaiver  bool        `json:"allowWaiver"`
}

type ManifestCompilerNodeConfig struct {
	ManifestKind  string `json:"manifestKind"`
	SchemaVersion int    `json:"schemaVersion"`
	Hook          string `json:"hook"`
}

type WorkbenchBuildNodeConfig struct {
	BuildManifestSchemaVersion int           `json:"buildManifestSchemaVersion"`
	MaxAttempts                int           `json:"maxAttempts"`
	Timeout                    time.Duration `json:"timeout"`
}

type PublishNodeConfig struct {
	Environment   string `json:"environment"`
	RequiredRole  string `json:"requiredRole"`
	AllowRollback bool   `json:"allowRollback"`
}

type AINodeConfig struct {
	JobType             string `json:"jobType"`
	ModelPolicy         string `json:"modelPolicy"`
	OutputSchemaVersion string `json:"outputSchemaVersion"`
}

type HumanTaskNodeConfig struct {
	TaskType     string `json:"taskType"`
	RequiredRole string `json:"requiredRole"`
}

type ApprovalNodeConfig struct {
	RequiredRole       string `json:"requiredRole"`
	MinimumApprovals   int    `json:"minimumApprovals"`
	ProhibitSelfReview bool   `json:"prohibitSelfReview"`
}

type TransformNodeConfig struct {
	Transform string `json:"transform"`
}

type QualityGateNodeConfig struct {
	GateName string `json:"gateName"`
	Blocking bool   `json:"blocking"`
}

type DeliveryNodeConfig struct {
	Target            string `json:"target"`
	RequiresPrototype bool   `json:"requiresPrototype"`
}

// NodeDefinition uses a discriminated union: exactly one matching config must be set.
type NodeDefinition struct {
	ID           string                    `json:"id"`
	Name         string                    `json:"name"`
	Type         WorkflowNodeType          `json:"type"`
	InputSchema  json.RawMessage           `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage           `json:"outputSchema,omitempty"`
	InputPorts   map[string]PortDefinition `json:"inputPorts,omitempty"`
	OutputPorts  map[string]PortDefinition `json:"outputPorts,omitempty"`

	ArtifactInput    *ArtifactInputNodeConfig    `json:"artifactInput,omitempty"`
	AITransform      *AITransformNodeConfig      `json:"aiTransform,omitempty"`
	HumanEdit        *HumanEditNodeConfig        `json:"humanEdit,omitempty"`
	ReviewGate       *ReviewGateNodeConfig       `json:"reviewGate,omitempty"`
	Condition        *ConditionNodeConfig        `json:"condition,omitempty"`
	FanOut           *FanOutNodeConfig           `json:"fanOut,omitempty"`
	Merge            *MergeNodeConfig            `json:"merge,omitempty"`
	ManifestCompiler *ManifestCompilerNodeConfig `json:"manifestCompiler,omitempty"`
	WorkbenchBuild   *WorkbenchBuildNodeConfig   `json:"workbenchBuild,omitempty"`
	Publish          *PublishNodeConfig          `json:"publish,omitempty"`

	AI          *AINodeConfig          `json:"ai,omitempty"`
	HumanTask   *HumanTaskNodeConfig   `json:"humanTask,omitempty"`
	Approval    *ApprovalNodeConfig    `json:"approval,omitempty"`
	Transform   *TransformNodeConfig   `json:"transform,omitempty"`
	QualityGate *QualityGateNodeConfig `json:"qualityGate,omitempty"`
	Delivery    *DeliveryNodeConfig    `json:"delivery,omitempty"`
}

func (n NodeDefinition) Validate() error {
	issues := make([]ValidationIssue, 0)
	if strings.TrimSpace(n.ID) == "" {
		issues = append(issues, ValidationIssue{Path: "node.id", Message: "is required"})
	}
	if strings.TrimSpace(n.Name) == "" {
		issues = append(issues, ValidationIssue{Path: "node.name", Message: "is required"})
	}
	configs := 0
	for _, present := range []bool{
		n.ArtifactInput != nil, n.AITransform != nil, n.HumanEdit != nil, n.ReviewGate != nil,
		n.Condition != nil, n.FanOut != nil, n.Merge != nil, n.ManifestCompiler != nil,
		n.WorkbenchBuild != nil, n.Publish != nil,
		n.AI != nil, n.HumanTask != nil, n.Approval != nil, n.Transform != nil,
		n.QualityGate != nil, n.Delivery != nil,
	} {
		if present {
			configs++
		}
	}
	if configs != 1 {
		issues = append(issues, ValidationIssue{Path: "node.config", Message: "exactly one typed config is required"})
	}
	switch n.Type {
	case NodeArtifactInput:
		if n.ArtifactInput == nil || len(n.ArtifactInput.AllowedTypes) == 0 || n.ArtifactInput.MinimumArtifacts < 1 {
			issues = append(issues, ValidationIssue{Path: "node.artifactInput", Message: "matching config with allowedTypes and positive minimumArtifacts is required"})
		} else {
			for _, artifactType := range n.ArtifactInput.AllowedTypes {
				if !artifactType.Valid() {
					issues = append(issues, ValidationIssue{Path: "node.artifactInput.allowedTypes", Message: "contains an invalid artifact type"})
				}
			}
		}
	case NodeAITransform:
		if n.AITransform == nil || strings.TrimSpace(n.AITransform.JobType) == "" || strings.TrimSpace(n.AITransform.OutputSchemaVersion) == "" || n.AITransform.MaxAttempts < 1 || n.AITransform.Timeout <= 0 {
			issues = append(issues, ValidationIssue{Path: "node.aiTransform", Message: "matching config with jobType, schema version, attempts and timeout is required"})
		}
	case NodeHumanEdit:
		if n.HumanEdit == nil || !n.HumanEdit.ArtifactType.Valid() || strings.TrimSpace(n.HumanEdit.RequiredRole) == "" {
			issues = append(issues, ValidationIssue{Path: "node.humanEdit", Message: "matching config with artifactType and requiredRole is required"})
		}
	case NodeReviewGate:
		if n.ReviewGate == nil || strings.TrimSpace(n.ReviewGate.RequiredRole) == "" || n.ReviewGate.MinimumApprovals < 1 {
			issues = append(issues, ValidationIssue{Path: "node.reviewGate", Message: "matching config with requiredRole and positive minimumApprovals is required"})
		}
	case NodeCondition:
		if n.Condition == nil {
			issues = append(issues, ValidationIssue{Path: "node.condition", Message: "matching condition config is required"})
		} else if err := validateConditionBranches(n.Condition.Branches); err != nil {
			issues = append(issues, ValidationIssue{Path: "node.condition.branches", Message: err.Error()})
		}
	case NodeFanOut:
		if n.FanOut == nil || !strings.HasPrefix(n.FanOut.ItemsPath, "/") || !strings.HasPrefix(n.FanOut.SliceKeyPath, "/") || strings.TrimSpace(n.FanOut.MergeNodeID) == "" || n.FanOut.MaxParallel < 1 {
			issues = append(issues, ValidationIssue{Path: "node.fanOut", Message: "matching config with JSON pointers, mergeNodeId and positive maxParallel is required"})
		}
	case NodeMerge:
		if n.Merge == nil || strings.TrimSpace(n.Merge.FanOutNodeID) == "" || !n.Merge.Policy.Valid() || (n.Merge.Policy == MergeQuorum && n.Merge.Quorum < 1) {
			issues = append(issues, ValidationIssue{Path: "node.merge", Message: "matching config with fanOutNodeId and a valid policy is required"})
		}
	case NodeManifestCompiler:
		if n.ManifestCompiler == nil || strings.TrimSpace(n.ManifestCompiler.ManifestKind) == "" || n.ManifestCompiler.SchemaVersion < 1 || strings.TrimSpace(n.ManifestCompiler.Hook) == "" {
			issues = append(issues, ValidationIssue{Path: "node.manifestCompiler", Message: "matching config with kind, schemaVersion and hook is required"})
		}
	case NodeWorkbenchBuild:
		if n.WorkbenchBuild == nil || n.WorkbenchBuild.BuildManifestSchemaVersion < 1 || n.WorkbenchBuild.MaxAttempts < 1 || n.WorkbenchBuild.Timeout <= 0 {
			issues = append(issues, ValidationIssue{Path: "node.workbenchBuild", Message: "matching config with schema version, attempts and timeout is required"})
		}
	case NodePublish:
		if n.Publish == nil || strings.TrimSpace(n.Publish.Environment) == "" || strings.TrimSpace(n.Publish.RequiredRole) == "" {
			issues = append(issues, ValidationIssue{Path: "node.publish", Message: "matching config with environment and requiredRole is required"})
		}
	case NodeAI:
		if n.AI == nil || strings.TrimSpace(n.AI.JobType) == "" || strings.TrimSpace(n.AI.OutputSchemaVersion) == "" {
			issues = append(issues, ValidationIssue{Path: "node.ai", Message: "matching config with jobType and outputSchemaVersion is required"})
		}
	case NodeHumanTask:
		if n.HumanTask == nil || strings.TrimSpace(n.HumanTask.TaskType) == "" || strings.TrimSpace(n.HumanTask.RequiredRole) == "" {
			issues = append(issues, ValidationIssue{Path: "node.humanTask", Message: "matching config with taskType and requiredRole is required"})
		}
	case NodeApproval:
		if n.Approval == nil || strings.TrimSpace(n.Approval.RequiredRole) == "" || n.Approval.MinimumApprovals < 1 {
			issues = append(issues, ValidationIssue{Path: "node.approval", Message: "matching config with requiredRole and positive minimumApprovals is required"})
		}
	case NodeTransform:
		if n.Transform == nil || strings.TrimSpace(n.Transform.Transform) == "" {
			issues = append(issues, ValidationIssue{Path: "node.transform", Message: "matching transform config is required"})
		}
	case NodeQualityGate:
		if n.QualityGate == nil || strings.TrimSpace(n.QualityGate.GateName) == "" {
			issues = append(issues, ValidationIssue{Path: "node.qualityGate", Message: "matching gate config is required"})
		}
	case NodeDelivery:
		if n.Delivery == nil || strings.TrimSpace(n.Delivery.Target) == "" {
			issues = append(issues, ValidationIssue{Path: "node.delivery", Message: "matching delivery config is required"})
		}
	default:
		issues = append(issues, ValidationIssue{Path: "node.type", Message: "unknown node type"})
	}
	if _, err := n.ResolvedInputPorts(); err != nil {
		issues = append(issues, ValidationIssue{Path: "node.inputPorts", Message: err.Error()})
	}
	if _, err := n.ResolvedOutputPorts(); err != nil {
		issues = append(issues, ValidationIssue{Path: "node.outputPorts", Message: err.Error()})
	}
	return validationError(issues)
}

func (p MergePolicy) Valid() bool {
	return p == MergeAll || p == MergeAny || p == MergeQuorum
}

func validateConditionBranches(branches []ConditionBranch) error {
	if len(branches) < 2 {
		return fmt.Errorf("at least two branches are required")
	}
	seen := map[string]struct{}{}
	defaults := 0
	for _, branch := range branches {
		name := strings.TrimSpace(branch.Name)
		if name == "" {
			return fmt.Errorf("branch name is required")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("branch names must be unique")
		}
		seen[name] = struct{}{}
		if branch.Default {
			defaults++
		} else if strings.TrimSpace(branch.Expression) == "" {
			return fmt.Errorf("non-default branch %q requires an expression", name)
		}
	}
	if defaults != 1 {
		return fmt.Errorf("exactly one default branch is required")
	}
	return nil
}

func (n NodeDefinition) ResolvedInputPorts() (map[string]PortDefinition, error) {
	return resolvePorts(n.InputPorts, n.InputSchema)
}

func (n NodeDefinition) ResolvedOutputPorts() (map[string]PortDefinition, error) {
	return resolvePorts(n.OutputPorts, n.OutputSchema)
}

func resolvePorts(explicit map[string]PortDefinition, legacy json.RawMessage) (map[string]PortDefinition, error) {
	if len(explicit) == 0 {
		if _, err := parseObjectSchema(legacy); err != nil {
			return nil, err
		}
		return map[string]PortDefinition{"default": {Schema: cloneJSON(legacy)}}, nil
	}
	ports := make(map[string]PortDefinition, len(explicit))
	for name, port := range explicit {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("port name is required")
		}
		if _, err := parseObjectSchema(port.Schema); err != nil {
			return nil, fmt.Errorf("port %q: %w", name, err)
		}
		port.Schema = cloneJSON(port.Schema)
		ports[name] = port
	}
	return ports, nil
}

type WorkflowEdge struct {
	ID       string            `json:"id"`
	From     string            `json:"from"`
	FromPort string            `json:"fromPort,omitempty"`
	To       string            `json:"to"`
	ToPort   string            `json:"toPort,omitempty"`
	Mapping  map[string]string `json:"mapping,omitempty"` // target property -> source property
}

type WorkflowDefinition struct {
	ID            string           `json:"id"`
	Version       int              `json:"version"`
	Name          string           `json:"name"`
	SchemaVersion string           `json:"schemaVersion"`
	Nodes         []NodeDefinition `json:"nodes"`
	Edges         []WorkflowEdge   `json:"edges"`
	Hash          string           `json:"hash"`
	CreatedBy     string           `json:"createdBy"`
	CreatedAt     time.Time        `json:"createdAt"`
}

type WorkflowDefinitionRef struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Hash    string `json:"hash"`
}

func (r WorkflowDefinitionRef) Validate() error {
	if strings.TrimSpace(r.ID) == "" || r.Version < 1 || !IsCanonicalHash(r.Hash) {
		return invalid("workflowDefinitionRef", "id, positive version and SHA-256 hash are required")
	}
	return nil
}

// WorkflowSliceRef is the immutable lineage pointer carried across a fan-out
// region. It intentionally contains no mutable delivery state; consumers must
// resolve the exact slice snapshot from the pinned workflow run.
type WorkflowSliceRef struct {
	ID           string `json:"id"`
	Key          string `json:"key"`
	FanOutNodeID string `json:"fanOutNodeId"`
}

// NodeOutputReference identifies the exact predecessor state from which an
// input binding was materialized. Artifact and slice refs are propagated
// through control nodes so downstream runners never need to scan global run
// context to rediscover their lineage.
type NodeOutputReference struct {
	RunID             string             `json:"runId"`
	NodeKey           string             `json:"nodeKey"`
	DefinitionNodeID  string             `json:"definitionNodeId"`
	SliceID           string             `json:"sliceId,omitempty"`
	InputManifest     *ManifestRef       `json:"inputManifest,omitempty"`
	OutputProposal    *ProposalRef       `json:"outputProposal,omitempty"`
	OutputRevisionID  string             `json:"outputRevisionId,omitempty"`
	ArtifactRevisions []ArtifactRef      `json:"artifactRevisions,omitempty"`
	DeliverySliceRefs []WorkflowSliceRef `json:"deliverySliceRefs,omitempty"`
}

// NodeInputBinding is a frozen edge transfer. Output is the predecessor's
// canonical selected port output; Value is the canonical value after applying
// the edge mapping for the target port. Their hashes make accidental mutation
// or stale adapter reads fail closed.
type NodeInputBinding struct {
	EdgeID     string              `json:"edgeId"`
	FromPort   string              `json:"fromPort"`
	ToPort     string              `json:"toPort"`
	Mapping    map[string]string   `json:"mapping,omitempty"`
	Source     NodeOutputReference `json:"source"`
	Output     json.RawMessage     `json:"output"`
	OutputHash string              `json:"outputHash"`
	Value      json.RawMessage     `json:"value"`
	ValueHash  string              `json:"valueHash"`
}

// NodeInputEnvelope stores only canonical bytes internally. Callers receive
// decoded copies through Bindings/Values, so mutating a returned value cannot
// alter the execution snapshot or its hash.
type NodeInputEnvelope struct {
	canonical json.RawMessage
	hash      string
}

type nodeInputEnvelopePayload struct {
	Bindings []NodeInputBinding `json:"bindings"`
	Hash     string             `json:"hash"`
}

func NewNodeInputEnvelope(bindings []NodeInputBinding) (NodeInputEnvelope, error) {
	normalized := make([]NodeInputBinding, len(bindings))
	for index, binding := range bindings {
		copyBinding, err := normalizeNodeInputBinding(binding)
		if err != nil {
			return NodeInputEnvelope{}, fmt.Errorf("input binding %d: %w", index, err)
		}
		normalized[index] = copyBinding
	}
	sort.Slice(normalized, func(left, right int) bool {
		leftKey := normalized[left].EdgeID + "\x00" + normalized[left].Source.NodeKey + "\x00" + normalized[left].FromPort + "\x00" + normalized[left].ToPort
		rightKey := normalized[right].EdgeID + "\x00" + normalized[right].Source.NodeKey + "\x00" + normalized[right].FromPort + "\x00" + normalized[right].ToPort
		return leftKey < rightKey
	})
	payload := nodeInputEnvelopePayload{Bindings: normalized}
	hash, err := CanonicalHash(payload)
	if err != nil {
		return NodeInputEnvelope{}, err
	}
	payload.Hash = hash
	canonical, err := CanonicalJSON(payload)
	if err != nil {
		return NodeInputEnvelope{}, err
	}
	return NodeInputEnvelope{canonical: canonical, hash: hash}, nil
}

func (e NodeInputEnvelope) Validate() error {
	if len(e.canonical) == 0 || !IsCanonicalHash(e.hash) {
		return invalid("nodeInputEnvelope", "canonical payload and hash are required")
	}
	var payload nodeInputEnvelopePayload
	if err := json.Unmarshal(e.canonical, &payload); err != nil {
		return invalid("nodeInputEnvelope", err.Error())
	}
	expected := payload.Hash
	payload.Hash = ""
	rebuilt, err := NewNodeInputEnvelope(payload.Bindings)
	if err != nil {
		return err
	}
	if expected != rebuilt.hash || e.hash != rebuilt.hash {
		return &DomainError{Kind: ErrConflict, Field: "nodeInputEnvelope.hash", Message: "input envelope payload was mutated"}
	}
	return nil
}

func (e NodeInputEnvelope) Hash() string { return e.hash }

func (e NodeInputEnvelope) Canonical() json.RawMessage {
	return cloneJSON(e.canonical)
}

func (e NodeInputEnvelope) Bindings() []NodeInputBinding {
	var payload nodeInputEnvelopePayload
	if json.Unmarshal(e.canonical, &payload) != nil {
		return nil
	}
	bindings := make([]NodeInputBinding, len(payload.Bindings))
	for index, binding := range payload.Bindings {
		bindings[index], _ = normalizeNodeInputBinding(binding)
	}
	return bindings
}

func (e NodeInputEnvelope) BindingsForPort(port string) []NodeInputBinding {
	if port == "" {
		port = "default"
	}
	bindings := make([]NodeInputBinding, 0)
	for _, binding := range e.Bindings() {
		if binding.ToPort == port {
			bindings = append(bindings, binding)
		}
	}
	return bindings
}

func (e NodeInputEnvelope) Values(port string) []json.RawMessage {
	bindings := e.BindingsForPort(port)
	values := make([]json.RawMessage, len(bindings))
	for index, binding := range bindings {
		values[index] = cloneJSON(binding.Value)
	}
	return values
}

func (e NodeInputEnvelope) FirstValue(port string) (json.RawMessage, NodeOutputReference, bool) {
	bindings := e.BindingsForPort(port)
	if len(bindings) == 0 {
		return nil, NodeOutputReference{}, false
	}
	return cloneJSON(bindings[0].Value), cloneNodeOutputReference(bindings[0].Source), true
}

func (e NodeInputEnvelope) ArtifactRefs() []ArtifactRef {
	refs := make([]ArtifactRef, 0)
	for _, binding := range e.Bindings() {
		for _, ref := range binding.Source.ArtifactRevisions {
			duplicate := false
			for _, existing := range refs {
				if existing.Equal(ref) {
					duplicate = true
					break
				}
			}
			if !duplicate {
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

func (e NodeInputEnvelope) SliceRefs() []WorkflowSliceRef {
	refs := make([]WorkflowSliceRef, 0)
	seen := map[string]bool{}
	for _, binding := range e.Bindings() {
		for _, ref := range binding.Source.DeliverySliceRefs {
			key := ref.FanOutNodeID + "\x00" + ref.ID
			if !seen[key] {
				seen[key] = true
				refs = append(refs, ref)
			}
		}
	}
	sort.Slice(refs, func(left, right int) bool {
		if refs[left].FanOutNodeID == refs[right].FanOutNodeID {
			return refs[left].Key < refs[right].Key
		}
		return refs[left].FanOutNodeID < refs[right].FanOutNodeID
	})
	return refs
}

func (e NodeInputEnvelope) MarshalJSON() ([]byte, error) {
	if len(e.canonical) == 0 {
		return []byte("null"), nil
	}
	return append([]byte(nil), e.canonical...), nil
}

func (e *NodeInputEnvelope) UnmarshalJSON(raw []byte) error {
	if e == nil {
		return invalid("nodeInputEnvelope", "target is nil")
	}
	if string(raw) == "null" || len(raw) == 0 {
		*e = NodeInputEnvelope{}
		return nil
	}
	var payload nodeInputEnvelopePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return invalid("nodeInputEnvelope", err.Error())
	}
	expected := payload.Hash
	payload.Hash = ""
	rebuilt, err := NewNodeInputEnvelope(payload.Bindings)
	if err != nil {
		return err
	}
	if expected != rebuilt.hash {
		return &DomainError{Kind: ErrConflict, Field: "nodeInputEnvelope.hash", Message: "input envelope hash mismatch"}
	}
	*e = rebuilt
	return nil
}

func normalizeNodeInputBinding(binding NodeInputBinding) (NodeInputBinding, error) {
	binding.EdgeID = strings.TrimSpace(binding.EdgeID)
	binding.FromPort = strings.TrimSpace(binding.FromPort)
	binding.ToPort = strings.TrimSpace(binding.ToPort)
	if binding.EdgeID == "" || binding.FromPort == "" || binding.ToPort == "" {
		return NodeInputBinding{}, invalid("nodeInputBinding", "edgeId, fromPort and toPort are required")
	}
	if strings.TrimSpace(binding.Source.RunID) == "" || strings.TrimSpace(binding.Source.NodeKey) == "" || strings.TrimSpace(binding.Source.DefinitionNodeID) == "" {
		return NodeInputBinding{}, invalid("nodeInputBinding.source", "runId, nodeKey and definitionNodeId are required")
	}
	binding.Mapping = cloneStringMap(binding.Mapping)
	binding.Source = cloneNodeOutputReference(binding.Source)
	if binding.Source.InputManifest != nil {
		if err := binding.Source.InputManifest.Validate(); err != nil {
			return NodeInputBinding{}, err
		}
	}
	if binding.Source.OutputProposal != nil {
		if err := binding.Source.OutputProposal.Validate(); err != nil {
			return NodeInputBinding{}, err
		}
	}
	for _, ref := range binding.Source.ArtifactRevisions {
		if err := ref.Validate(); err != nil {
			return NodeInputBinding{}, err
		}
	}
	for _, ref := range binding.Source.DeliverySliceRefs {
		if strings.TrimSpace(ref.ID) == "" || strings.TrimSpace(ref.Key) == "" || strings.TrimSpace(ref.FanOutNodeID) == "" {
			return NodeInputBinding{}, invalid("nodeInputBinding.source.deliverySliceRefs", "id, key and fanOutNodeId are required")
		}
	}
	var err error
	binding.Output, err = CanonicalJSON(binding.Output)
	if err != nil {
		return NodeInputBinding{}, err
	}
	binding.Value, err = CanonicalJSON(binding.Value)
	if err != nil {
		return NodeInputBinding{}, err
	}
	outputHash, err := CanonicalHash(binding.Output)
	if err != nil {
		return NodeInputBinding{}, err
	}
	valueHash, err := CanonicalHash(binding.Value)
	if err != nil {
		return NodeInputBinding{}, err
	}
	if binding.OutputHash != "" && binding.OutputHash != outputHash {
		return NodeInputBinding{}, &DomainError{Kind: ErrConflict, Field: "nodeInputBinding.outputHash", Message: "output hash mismatch"}
	}
	if binding.ValueHash != "" && binding.ValueHash != valueHash {
		return NodeInputBinding{}, &DomainError{Kind: ErrConflict, Field: "nodeInputBinding.valueHash", Message: "value hash mismatch"}
	}
	binding.OutputHash = outputHash
	binding.ValueHash = valueHash
	sort.Slice(binding.Source.ArtifactRevisions, func(left, right int) bool {
		leftRef, rightRef := binding.Source.ArtifactRevisions[left], binding.Source.ArtifactRevisions[right]
		return leftRef.ArtifactID+"\x00"+leftRef.RevisionID+"\x00"+leftRef.AnchorID < rightRef.ArtifactID+"\x00"+rightRef.RevisionID+"\x00"+rightRef.AnchorID
	})
	sort.Slice(binding.Source.DeliverySliceRefs, func(left, right int) bool {
		leftRef, rightRef := binding.Source.DeliverySliceRefs[left], binding.Source.DeliverySliceRefs[right]
		return leftRef.FanOutNodeID+"\x00"+leftRef.Key+"\x00"+leftRef.ID < rightRef.FanOutNodeID+"\x00"+rightRef.Key+"\x00"+rightRef.ID
	})
	return binding, nil
}

func cloneNodeOutputReference(source NodeOutputReference) NodeOutputReference {
	clone := source
	if source.InputManifest != nil {
		value := *source.InputManifest
		clone.InputManifest = &value
	}
	if source.OutputProposal != nil {
		value := *source.OutputProposal
		clone.OutputProposal = &value
	}
	clone.ArtifactRevisions = append([]ArtifactRef(nil), source.ArtifactRevisions...)
	clone.DeliverySliceRefs = append([]WorkflowSliceRef(nil), source.DeliverySliceRefs...)
	return clone
}

func NewWorkflowDefinition(id string, version int, name, schemaVersion string, nodes []NodeDefinition, edges []WorkflowEdge, createdBy string, now time.Time) (WorkflowDefinition, error) {
	definition := WorkflowDefinition{
		ID: strings.TrimSpace(id), Version: version, Name: strings.TrimSpace(name), SchemaVersion: strings.TrimSpace(schemaVersion),
		Nodes: cloneNodeDefinitions(nodes), Edges: cloneWorkflowEdges(edges), CreatedBy: strings.TrimSpace(createdBy), CreatedAt: now.UTC(),
	}
	if err := definition.validate(false); err != nil {
		return WorkflowDefinition{}, err
	}
	hash, err := definition.calculateHash()
	if err != nil {
		return WorkflowDefinition{}, err
	}
	definition.Hash = hash
	return definition, nil
}

func (d WorkflowDefinition) Ref() WorkflowDefinitionRef {
	return WorkflowDefinitionRef{ID: d.ID, Version: d.Version, Hash: d.Hash}
}

func (d WorkflowDefinition) FindNode(id string) (NodeDefinition, bool) {
	for _, node := range d.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return NodeDefinition{}, false
}

func (d WorkflowDefinition) Incoming(nodeID string) []WorkflowEdge {
	edges := make([]WorkflowEdge, 0)
	for _, edge := range d.Edges {
		if edge.To == nodeID {
			edges = append(edges, edge)
		}
	}
	return cloneWorkflowEdges(edges)
}

func (d WorkflowDefinition) Outgoing(nodeID string) []WorkflowEdge {
	edges := make([]WorkflowEdge, 0)
	for _, edge := range d.Edges {
		if edge.From == nodeID {
			edges = append(edges, edge)
		}
	}
	return cloneWorkflowEdges(edges)
}

func (d WorkflowDefinition) EntryNodeID() (string, error) {
	degree := map[string]int{}
	for _, node := range d.Nodes {
		degree[node.ID] = 0
	}
	for _, edge := range d.Edges {
		degree[edge.To]++
	}
	entries := nodesWithDegree(degree, 0)
	if len(entries) != 1 {
		return "", &DomainError{Kind: ErrValidation, Field: "workflow.entry", Message: "definition does not have exactly one entry"}
	}
	return entries[0], nil
}

func (d WorkflowDefinition) TerminalNodeID() (string, error) {
	degree := map[string]int{}
	for _, node := range d.Nodes {
		degree[node.ID] = 0
	}
	for _, edge := range d.Edges {
		degree[edge.From]++
	}
	terminals := nodesWithDegree(degree, 0)
	if len(terminals) != 1 {
		return "", &DomainError{Kind: ErrValidation, Field: "workflow.terminal", Message: "definition does not have exactly one terminal"}
	}
	return terminals[0], nil
}

// FanOutRegion returns definition nodes dynamically instantiated for each slice.
// The fan-out and paired merge themselves are excluded.
func (d WorkflowDefinition) FanOutRegion(fanOutNodeID string) ([]string, error) {
	fanOut, exists := d.FindNode(fanOutNodeID)
	if !exists || fanOut.Type != NodeFanOut || fanOut.FanOut == nil {
		return nil, invalid("workflow.fanOut", "node is not a fan-out")
	}
	mergeID := fanOut.FanOut.MergeNodeID
	region := map[string]struct{}{}
	queue := make([]string, 0)
	for _, edge := range d.Outgoing(fanOutNodeID) {
		if edge.To != mergeID {
			queue = append(queue, edge.To)
		}
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == mergeID {
			continue
		}
		if _, visited := region[current]; visited {
			continue
		}
		region[current] = struct{}{}
		for _, edge := range d.Outgoing(current) {
			queue = append(queue, edge.To)
		}
	}
	result := make([]string, 0, len(region))
	for id := range region {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func (d WorkflowDefinition) Validate() error {
	if err := d.validate(true); err != nil {
		return err
	}
	hash, err := d.calculateHash()
	if err != nil {
		return err
	}
	if hash != d.Hash {
		return &DomainError{Kind: ErrConflict, Field: "workflow.hash", Message: "workflow definition payload was mutated"}
	}
	return nil
}

func (d WorkflowDefinition) validate(requireHash bool) error {
	issues := make([]ValidationIssue, 0)
	if d.ID == "" || d.Name == "" || d.SchemaVersion == "" || d.CreatedBy == "" {
		issues = append(issues, ValidationIssue{Path: "workflow", Message: "id, name, schemaVersion and createdBy are required"})
	}
	if d.Version < 1 {
		issues = append(issues, ValidationIssue{Path: "workflow.version", Message: "must be positive"})
	}
	if requireHash && !IsCanonicalHash(d.Hash) {
		issues = append(issues, ValidationIssue{Path: "workflow.hash", Message: "must be a SHA-256 hash"})
	}
	if len(d.Nodes) == 0 {
		issues = append(issues, ValidationIssue{Path: "workflow.nodes", Message: "at least one node is required"})
	}
	nodes := make(map[string]NodeDefinition, len(d.Nodes))
	for index, node := range d.Nodes {
		if err := node.Validate(); err != nil {
			issues = append(issues, ValidationIssue{Path: fmt.Sprintf("workflow.nodes[%d]", index), Message: err.Error()})
		}
		if _, duplicate := nodes[node.ID]; duplicate {
			issues = append(issues, ValidationIssue{Path: "workflow.nodes", Message: "node IDs must be unique"})
		}
		nodes[node.ID] = node
	}
	edgeIDs := map[string]struct{}{}
	indegree := map[string]int{}
	outdegree := map[string]int{}
	adjacency := map[string][]string{}
	reverse := map[string][]string{}
	for id := range nodes {
		indegree[id] = 0
		outdegree[id] = 0
	}
	for index, edge := range d.Edges {
		path := fmt.Sprintf("workflow.edges[%d]", index)
		if strings.TrimSpace(edge.ID) == "" || strings.TrimSpace(edge.From) == "" || strings.TrimSpace(edge.To) == "" {
			issues = append(issues, ValidationIssue{Path: path, Message: "id, from and to are required"})
			continue
		}
		if _, duplicate := edgeIDs[edge.ID]; duplicate {
			issues = append(issues, ValidationIssue{Path: path + ".id", Message: "edge IDs must be unique"})
		}
		edgeIDs[edge.ID] = struct{}{}
		from, fromExists := nodes[edge.From]
		to, toExists := nodes[edge.To]
		if !fromExists || !toExists {
			issues = append(issues, ValidationIssue{Path: path, Message: "edge endpoint does not exist"})
			continue
		}
		if edge.From == edge.To {
			issues = append(issues, ValidationIssue{Path: path, Message: "self-edge is not allowed"})
			continue
		}
		fromPorts, fromPortErr := from.ResolvedOutputPorts()
		toPorts, toPortErr := to.ResolvedInputPorts()
		fromPort := edge.FromPort
		if fromPort == "" {
			fromPort = "default"
		}
		toPort := edge.ToPort
		if toPort == "" {
			toPort = "default"
		}
		outputPort, outputExists := fromPorts[fromPort]
		inputPort, inputExists := toPorts[toPort]
		switch {
		case fromPortErr != nil || toPortErr != nil:
			// Node validation already reports the malformed schema.
		case !outputExists:
			issues = append(issues, ValidationIssue{Path: path + ".fromPort", Message: fmt.Sprintf("output port %q does not exist", fromPort)})
		case !inputExists:
			issues = append(issues, ValidationIssue{Path: path + ".toPort", Message: fmt.Sprintf("input port %q does not exist", toPort)})
		default:
			if err := validateSchemaCompatibility(outputPort.Schema, inputPort.Schema, edge.Mapping); err != nil {
				issues = append(issues, ValidationIssue{Path: path + ".mapping", Message: err.Error()})
			}
		}
		indegree[edge.To]++
		outdegree[edge.From]++
		adjacency[edge.From] = append(adjacency[edge.From], edge.To)
		reverse[edge.To] = append(reverse[edge.To], edge.From)
	}
	if len(nodes) > 0 {
		if !containsDAG(nodes, indegree, adjacency) {
			issues = append(issues, ValidationIssue{Path: "workflow.edges", Message: "workflow graph must be acyclic"})
		}
		entries := nodesWithDegree(indegree, 0)
		terminals := nodesWithDegree(outdegree, 0)
		if len(entries) != 1 {
			issues = append(issues, ValidationIssue{Path: "workflow.entry", Message: fmt.Sprintf("exactly one entry node is required, found %d", len(entries))})
		}
		if len(terminals) != 1 {
			issues = append(issues, ValidationIssue{Path: "workflow.terminal", Message: fmt.Sprintf("exactly one terminal node is required, found %d", len(terminals))})
		}
		if len(entries) == 1 && len(reachableFrom(entries[0], adjacency)) != len(nodes) {
			issues = append(issues, ValidationIssue{Path: "workflow.nodes", Message: "every node must be reachable from the entry"})
		}
		if len(terminals) == 1 && len(reachableFrom(terminals[0], reverse)) != len(nodes) {
			issues = append(issues, ValidationIssue{Path: "workflow.nodes", Message: "every node must have a path to the terminal"})
		}
		issues = append(issues, validateConditionEdges(nodes, d.Edges)...)
		issues = append(issues, validateFanOutMergePairs(nodes, adjacency, d.Edges)...)
	}
	return validationError(issues)
}

func nodesWithDegree(degrees map[string]int, target int) []string {
	result := make([]string, 0)
	for id, degree := range degrees {
		if degree == target {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
}

func reachableFrom(start string, adjacency map[string][]string) map[string]struct{} {
	visited := map[string]struct{}{}
	queue := []string{start}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, exists := visited[current]; exists {
			continue
		}
		visited[current] = struct{}{}
		queue = append(queue, adjacency[current]...)
	}
	return visited
}

func validateConditionEdges(nodes map[string]NodeDefinition, edges []WorkflowEdge) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	for id, node := range nodes {
		if node.Type != NodeCondition || node.Condition == nil {
			continue
		}
		counts := map[string]int{}
		for _, edge := range edges {
			if edge.From != id {
				continue
			}
			port := edge.FromPort
			if port == "" {
				port = "default"
			}
			counts[port]++
		}
		allowed := map[string]struct{}{}
		for _, branch := range node.Condition.Branches {
			allowed[branch.Name] = struct{}{}
			if counts[branch.Name] != 1 {
				issues = append(issues, ValidationIssue{Path: "workflow.nodes." + id, Message: fmt.Sprintf("condition branch %q must have exactly one outgoing edge", branch.Name)})
			}
		}
		for port := range counts {
			if _, exists := allowed[port]; !exists {
				issues = append(issues, ValidationIssue{Path: "workflow.nodes." + id, Message: fmt.Sprintf("outgoing port %q is not a declared condition branch", port)})
			}
		}
	}
	return issues
}

func validateFanOutMergePairs(nodes map[string]NodeDefinition, adjacency map[string][]string, edges []WorkflowEdge) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	pairedMerges := map[string]string{}
	regionOwners := map[string]string{}
	for id, node := range nodes {
		if node.Type != NodeFanOut || node.FanOut == nil {
			continue
		}
		merge, exists := nodes[node.FanOut.MergeNodeID]
		if !exists || merge.Type != NodeMerge || merge.Merge == nil {
			issues = append(issues, ValidationIssue{Path: "workflow.nodes." + id + ".fanOut.mergeNodeId", Message: "must reference a merge node"})
			continue
		}
		if merge.Merge.FanOutNodeID != id {
			issues = append(issues, ValidationIssue{Path: "workflow.nodes." + merge.ID + ".merge.fanOutNodeId", Message: "must reciprocally reference the fan-out node"})
		}
		if owner, duplicate := pairedMerges[merge.ID]; duplicate && owner != id {
			issues = append(issues, ValidationIssue{Path: "workflow.nodes." + merge.ID, Message: "merge node cannot pair with multiple fan-out nodes"})
		}
		pairedMerges[merge.ID] = id
		if _, reachable := reachableFrom(id, adjacency)[merge.ID]; !reachable {
			issues = append(issues, ValidationIssue{Path: "workflow.nodes." + id, Message: "paired merge must be downstream of fan-out"})
		}
		for _, successor := range adjacency[id] {
			if successor == merge.ID {
				continue
			}
			if _, reachesMerge := reachableFrom(successor, adjacency)[merge.ID]; !reachesMerge {
				issues = append(issues, ValidationIssue{Path: "workflow.nodes." + id, Message: fmt.Sprintf("fan-out branch through %q must terminate at paired merge", successor)})
			}
		}
		region := fanOutRegionIDs(id, merge.ID, adjacency)
		for member := range region {
			if owner, overlaps := regionOwners[member]; overlaps && owner != id {
				issues = append(issues, ValidationIssue{Path: "workflow.nodes." + id, Message: fmt.Sprintf("fan-out region overlaps %q at node %q", owner, member)})
			}
			regionOwners[member] = id
		}
		for _, edge := range edges {
			if region[edge.To] && edge.From != id && !region[edge.From] {
				issues = append(issues, ValidationIssue{Path: "workflow.edges." + edge.ID, Message: "fan-out region cannot have an external entry"})
			}
			if region[edge.From] && edge.To != merge.ID && !region[edge.To] {
				issues = append(issues, ValidationIssue{Path: "workflow.edges." + edge.ID, Message: "fan-out region can only exit through its paired merge"})
			}
		}
	}
	for id, node := range nodes {
		if node.Type == NodeMerge && node.Merge != nil {
			if _, paired := pairedMerges[id]; !paired {
				issues = append(issues, ValidationIssue{Path: "workflow.nodes." + id, Message: "merge node must be paired with a fan-out node"})
			}
		}
	}
	return issues
}

func fanOutRegionIDs(fanOutID, mergeID string, adjacency map[string][]string) map[string]bool {
	region := map[string]bool{}
	queue := append([]string(nil), adjacency[fanOutID]...)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == mergeID || region[current] {
			continue
		}
		region[current] = true
		queue = append(queue, adjacency[current]...)
	}
	return region
}

func (d WorkflowDefinition) calculateHash() (string, error) {
	payload := struct {
		ID            string           `json:"id"`
		Version       int              `json:"version"`
		Name          string           `json:"name"`
		SchemaVersion string           `json:"schemaVersion"`
		Nodes         []NodeDefinition `json:"nodes"`
		Edges         []WorkflowEdge   `json:"edges"`
		CreatedBy     string           `json:"createdBy"`
		CreatedAt     time.Time        `json:"createdAt"`
	}{d.ID, d.Version, d.Name, d.SchemaVersion, d.Nodes, d.Edges, d.CreatedBy, d.CreatedAt}
	return CanonicalHash(payload)
}

func cloneNodeDefinitions(nodes []NodeDefinition) []NodeDefinition {
	clones := make([]NodeDefinition, len(nodes))
	for index, node := range nodes {
		node.InputSchema = cloneJSON(node.InputSchema)
		node.OutputSchema = cloneJSON(node.OutputSchema)
		node.InputPorts = clonePorts(node.InputPorts)
		node.OutputPorts = clonePorts(node.OutputPorts)
		if node.ArtifactInput != nil {
			copy := *node.ArtifactInput
			copy.AllowedTypes = append([]ArtifactType(nil), node.ArtifactInput.AllowedTypes...)
			node.ArtifactInput = &copy
		}
		if node.AITransform != nil {
			copy := *node.AITransform
			node.AITransform = &copy
		}
		if node.HumanEdit != nil {
			copy := *node.HumanEdit
			node.HumanEdit = &copy
		}
		if node.ReviewGate != nil {
			copy := *node.ReviewGate
			node.ReviewGate = &copy
		}
		if node.Condition != nil {
			copy := *node.Condition
			copy.Branches = append([]ConditionBranch(nil), node.Condition.Branches...)
			node.Condition = &copy
		}
		if node.FanOut != nil {
			copy := *node.FanOut
			node.FanOut = &copy
		}
		if node.Merge != nil {
			copy := *node.Merge
			node.Merge = &copy
		}
		if node.ManifestCompiler != nil {
			copy := *node.ManifestCompiler
			node.ManifestCompiler = &copy
		}
		if node.WorkbenchBuild != nil {
			copy := *node.WorkbenchBuild
			node.WorkbenchBuild = &copy
		}
		if node.Publish != nil {
			copy := *node.Publish
			node.Publish = &copy
		}
		if node.AI != nil {
			copy := *node.AI
			node.AI = &copy
		}
		if node.HumanTask != nil {
			copy := *node.HumanTask
			node.HumanTask = &copy
		}
		if node.Approval != nil {
			copy := *node.Approval
			node.Approval = &copy
		}
		if node.Transform != nil {
			copy := *node.Transform
			node.Transform = &copy
		}
		if node.QualityGate != nil {
			copy := *node.QualityGate
			node.QualityGate = &copy
		}
		if node.Delivery != nil {
			copy := *node.Delivery
			node.Delivery = &copy
		}
		clones[index] = node
	}
	return clones
}

func clonePorts(source map[string]PortDefinition) map[string]PortDefinition {
	if source == nil {
		return nil
	}
	clone := make(map[string]PortDefinition, len(source))
	for name, port := range source {
		port.Schema = cloneJSON(port.Schema)
		clone[name] = port
	}
	return clone
}

func cloneWorkflowEdges(edges []WorkflowEdge) []WorkflowEdge {
	clones := make([]WorkflowEdge, len(edges))
	for index, edge := range edges {
		edge.Mapping = cloneStringMap(edge.Mapping)
		clones[index] = edge
	}
	return clones
}

type objectSchema struct {
	Properties map[string]string
	Required   []string
}

func parseObjectSchema(raw json.RawMessage) (objectSchema, error) {
	if len(raw) == 0 {
		return objectSchema{}, fmt.Errorf("schema is required")
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return objectSchema{}, fmt.Errorf("invalid JSON schema: %w", err)
	}
	if schema.Type != "object" {
		return objectSchema{}, fmt.Errorf("top-level schema type must be object")
	}
	shape := objectSchema{Properties: map[string]string{}, Required: append([]string(nil), schema.Required...)}
	for name, property := range schema.Properties {
		var descriptor struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(property, &descriptor); err != nil || descriptor.Type == "" {
			return objectSchema{}, fmt.Errorf("property %q must declare a type", name)
		}
		shape.Properties[name] = descriptor.Type
	}
	seenRequired := map[string]struct{}{}
	for _, name := range shape.Required {
		if _, exists := shape.Properties[name]; !exists {
			return objectSchema{}, fmt.Errorf("required property %q is not declared", name)
		}
		if _, duplicate := seenRequired[name]; duplicate {
			return objectSchema{}, fmt.Errorf("required property %q is duplicated", name)
		}
		seenRequired[name] = struct{}{}
	}
	return shape, nil
}

func validateSchemaCompatibility(outputRaw, inputRaw json.RawMessage, mapping map[string]string) error {
	output, err := parseObjectSchema(outputRaw)
	if err != nil {
		return err
	}
	input, err := parseObjectSchema(inputRaw)
	if err != nil {
		return err
	}
	for target, source := range mapping {
		if _, exists := input.Properties[target]; !exists {
			return fmt.Errorf("mapping target %q is not in the input schema", target)
		}
		if _, exists := output.Properties[source]; !exists {
			return fmt.Errorf("mapping source %q is not in the output schema", source)
		}
	}
	for _, target := range input.Required {
		source := target
		if mapped, exists := mapping[target]; exists {
			source = mapped
		}
		sourceType, exists := output.Properties[source]
		if !exists {
			return fmt.Errorf("required input %q is not produced upstream", target)
		}
		if sourceType != input.Properties[target] {
			return fmt.Errorf("input %q expects %s but upstream produces %s", target, input.Properties[target], sourceType)
		}
	}
	return nil
}

func containsDAG(nodes map[string]NodeDefinition, indegree map[string]int, adjacency map[string][]string) bool {
	remaining := make(map[string]int, len(indegree))
	queue := make([]string, 0)
	for id, count := range indegree {
		remaining[id] = count
		if count == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, target := range adjacency[id] {
			remaining[target]--
			if remaining[target] == 0 {
				queue = append(queue, target)
			}
		}
	}
	return visited == len(nodes)
}

type WorkflowRunStatus string

const (
	WorkflowRunQueued    WorkflowRunStatus = "queued"
	WorkflowRunRunning   WorkflowRunStatus = "running"
	WorkflowRunWaiting   WorkflowRunStatus = "waiting"
	WorkflowRunSucceeded WorkflowRunStatus = "succeeded"
	WorkflowRunFailed    WorkflowRunStatus = "failed"
	WorkflowRunCancelled WorkflowRunStatus = "cancelled"
)

type NodeRunStatus string

const (
	NodeRunPending       NodeRunStatus = "pending"
	NodeRunReady         NodeRunStatus = "ready"
	NodeRunRunning       NodeRunStatus = "running"
	NodeRunWaitingReview NodeRunStatus = "waiting_review"
	NodeRunSucceeded     NodeRunStatus = "succeeded"
	NodeRunFailed        NodeRunStatus = "failed"
	NodeRunSkipped       NodeRunStatus = "skipped"
	NodeRunCancelled     NodeRunStatus = "cancelled"
)

type ProposalRef struct {
	ID          string `json:"id"`
	PayloadHash string `json:"payloadHash"`
}

func (r ProposalRef) Validate() error {
	if strings.TrimSpace(r.ID) == "" || !IsCanonicalHash(r.PayloadHash) {
		return invalid("proposalRef", "id and payload hash are required")
	}
	return nil
}

type NodeRun struct {
	NodeID         string        `json:"nodeId"`
	Status         NodeRunStatus `json:"status"`
	InputManifest  *ManifestRef  `json:"inputManifest,omitempty"`
	OutputProposal *ProposalRef  `json:"outputProposal,omitempty"`
	Error          string        `json:"error,omitempty"`
	StartedAt      *time.Time    `json:"startedAt,omitempty"`
	CompletedAt    *time.Time    `json:"completedAt,omitempty"`
}

type WorkflowRun struct {
	ID              string                `json:"id"`
	ProjectID       string                `json:"projectId"`
	Definition      WorkflowDefinitionRef `json:"definition"`
	InitialManifest ManifestRef           `json:"initialManifest"`
	Status          WorkflowRunStatus     `json:"status"`
	Nodes           map[string]*NodeRun   `json:"nodes"`
	Version         uint64                `json:"version"`
	CreatedBy       string                `json:"createdBy"`
	CreatedAt       time.Time             `json:"createdAt"`
	StartedAt       *time.Time            `json:"startedAt,omitempty"`
	CompletedAt     *time.Time            `json:"completedAt,omitempty"`
}

func NewWorkflowRun(id, projectID, createdBy string, definition WorkflowDefinition, manifest ManifestRef, now time.Time) (*WorkflowRun, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(projectID) == "" || strings.TrimSpace(createdBy) == "" {
		return nil, invalid("workflowRun", "id, projectId and createdBy are required")
	}
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, &DomainError{Kind: ErrManifestUnpinned, Field: "workflowRun.initialManifest", Message: err.Error()}
	}
	run := &WorkflowRun{
		ID: id, ProjectID: projectID, Definition: definition.Ref(), InitialManifest: manifest,
		Status: WorkflowRunQueued, Nodes: map[string]*NodeRun{}, Version: 1,
		CreatedBy: createdBy, CreatedAt: now.UTC(),
	}
	indegree := map[string]int{}
	for _, node := range definition.Nodes {
		indegree[node.ID] = 0
	}
	for _, edge := range definition.Edges {
		indegree[edge.To]++
	}
	for _, node := range definition.Nodes {
		status := NodeRunPending
		if indegree[node.ID] == 0 {
			status = NodeRunReady
		}
		run.Nodes[node.ID] = &NodeRun{NodeID: node.ID, Status: status}
	}
	return run, nil
}

func (r *WorkflowRun) Start(expectedVersion uint64, now time.Time) error {
	if r.Version != expectedVersion {
		return conflict("workflowRun", expectedVersion, r.Version)
	}
	if r.Status != WorkflowRunQueued {
		return transition("workflowRun", string(r.Status), string(WorkflowRunRunning))
	}
	startedAt := now.UTC()
	r.Status = WorkflowRunRunning
	r.StartedAt = &startedAt
	r.Version++
	return nil
}

func (r *WorkflowRun) StartNode(definition WorkflowDefinition, nodeID string, manifest ManifestRef, expectedVersion uint64, now time.Time) error {
	if err := r.checkDefinition(definition); err != nil {
		return err
	}
	if r.Version != expectedVersion {
		return conflict("workflowRun", expectedVersion, r.Version)
	}
	if r.Status != WorkflowRunRunning && r.Status != WorkflowRunWaiting {
		return transition("workflowRun", string(r.Status), "start_node")
	}
	if err := manifest.Validate(); err != nil {
		return &DomainError{Kind: ErrManifestUnpinned, Field: "nodeRun.inputManifest", Message: err.Error()}
	}
	node, ok := r.Nodes[nodeID]
	if !ok {
		return &DomainError{Kind: ErrNotFound, Field: "workflowRun.nodeId", Message: nodeID}
	}
	if node.Status != NodeRunReady {
		return transition("nodeRun", string(node.Status), string(NodeRunRunning))
	}
	startedAt := now.UTC()
	copyManifest := manifest
	node.InputManifest = &copyManifest
	node.Status = NodeRunRunning
	node.StartedAt = &startedAt
	r.Status = WorkflowRunRunning
	r.Version++
	return nil
}

func (r *WorkflowRun) WaitForReview(definition WorkflowDefinition, nodeID string, expectedVersion uint64) error {
	if err := r.checkDefinition(definition); err != nil {
		return err
	}
	if r.Version != expectedVersion {
		return conflict("workflowRun", expectedVersion, r.Version)
	}
	if err := r.ensureActive(); err != nil {
		return err
	}
	node, ok := r.Nodes[nodeID]
	if !ok {
		return &DomainError{Kind: ErrNotFound, Field: "workflowRun.nodeId", Message: nodeID}
	}
	if node.Status != NodeRunRunning {
		return transition("nodeRun", string(node.Status), string(NodeRunWaitingReview))
	}
	node.Status = NodeRunWaitingReview
	r.refreshStatus()
	r.Version++
	return nil
}

func (r *WorkflowRun) ResumeNode(definition WorkflowDefinition, nodeID string, expectedVersion uint64) error {
	if err := r.checkDefinition(definition); err != nil {
		return err
	}
	if r.Version != expectedVersion {
		return conflict("workflowRun", expectedVersion, r.Version)
	}
	if err := r.ensureActive(); err != nil {
		return err
	}
	node, ok := r.Nodes[nodeID]
	if !ok {
		return &DomainError{Kind: ErrNotFound, Field: "workflowRun.nodeId", Message: nodeID}
	}
	if node.Status != NodeRunWaitingReview {
		return transition("nodeRun", string(node.Status), string(NodeRunRunning))
	}
	node.Status = NodeRunRunning
	r.Status = WorkflowRunRunning
	r.Version++
	return nil
}

func (r *WorkflowRun) CompleteNode(definition WorkflowDefinition, nodeID string, output *ProposalRef, expectedVersion uint64, now time.Time) error {
	if err := r.checkDefinition(definition); err != nil {
		return err
	}
	if r.Version != expectedVersion {
		return conflict("workflowRun", expectedVersion, r.Version)
	}
	if err := r.ensureActive(); err != nil {
		return err
	}
	node, ok := r.Nodes[nodeID]
	if !ok {
		return &DomainError{Kind: ErrNotFound, Field: "workflowRun.nodeId", Message: nodeID}
	}
	if node.Status != NodeRunRunning && node.Status != NodeRunWaitingReview {
		return transition("nodeRun", string(node.Status), string(NodeRunSucceeded))
	}
	if output != nil {
		if err := output.Validate(); err != nil {
			return err
		}
		copyOutput := *output
		node.OutputProposal = &copyOutput
	}
	completedAt := now.UTC()
	node.Status = NodeRunSucceeded
	node.CompletedAt = &completedAt
	r.unlockSuccessors(definition)
	r.refreshStatus()
	if r.Status == WorkflowRunSucceeded {
		r.CompletedAt = &completedAt
	}
	r.Version++
	return nil
}

func (r *WorkflowRun) FailNode(definition WorkflowDefinition, nodeID, message string, expectedVersion uint64, now time.Time) error {
	if err := r.checkDefinition(definition); err != nil {
		return err
	}
	if r.Version != expectedVersion {
		return conflict("workflowRun", expectedVersion, r.Version)
	}
	if err := r.ensureActive(); err != nil {
		return err
	}
	node, ok := r.Nodes[nodeID]
	if !ok {
		return &DomainError{Kind: ErrNotFound, Field: "workflowRun.nodeId", Message: nodeID}
	}
	if node.Status != NodeRunRunning && node.Status != NodeRunWaitingReview {
		return transition("nodeRun", string(node.Status), string(NodeRunFailed))
	}
	if strings.TrimSpace(message) == "" {
		return invalid("nodeRun.error", "is required")
	}
	completedAt := now.UTC()
	node.Status = NodeRunFailed
	node.Error = strings.TrimSpace(message)
	node.CompletedAt = &completedAt
	r.Status = WorkflowRunFailed
	r.CompletedAt = &completedAt
	r.Version++
	return nil
}

func (r *WorkflowRun) SkipNode(definition WorkflowDefinition, nodeID, reason string, expectedVersion uint64, now time.Time) error {
	if err := r.checkDefinition(definition); err != nil {
		return err
	}
	if r.Version != expectedVersion {
		return conflict("workflowRun", expectedVersion, r.Version)
	}
	if err := r.ensureActive(); err != nil {
		return err
	}
	node, ok := r.Nodes[nodeID]
	if !ok {
		return &DomainError{Kind: ErrNotFound, Field: "workflowRun.nodeId", Message: nodeID}
	}
	if node.Status != NodeRunReady {
		return transition("nodeRun", string(node.Status), string(NodeRunSkipped))
	}
	if strings.TrimSpace(reason) == "" {
		return invalid("nodeRun.reason", "is required")
	}
	completedAt := now.UTC()
	node.Status = NodeRunSkipped
	node.Error = strings.TrimSpace(reason)
	node.CompletedAt = &completedAt
	r.unlockSuccessors(definition)
	r.refreshStatus()
	if r.Status == WorkflowRunSucceeded {
		r.CompletedAt = &completedAt
	}
	r.Version++
	return nil
}

func (r *WorkflowRun) Cancel(expectedVersion uint64, now time.Time) error {
	if r.Version != expectedVersion {
		return conflict("workflowRun", expectedVersion, r.Version)
	}
	if r.Status == WorkflowRunSucceeded || r.Status == WorkflowRunFailed || r.Status == WorkflowRunCancelled {
		return transition("workflowRun", string(r.Status), string(WorkflowRunCancelled))
	}
	completedAt := now.UTC()
	r.Status = WorkflowRunCancelled
	r.CompletedAt = &completedAt
	for _, node := range r.Nodes {
		if node.Status == NodeRunPending || node.Status == NodeRunReady || node.Status == NodeRunRunning || node.Status == NodeRunWaitingReview {
			node.Status = NodeRunCancelled
			node.CompletedAt = &completedAt
		}
	}
	r.Version++
	return nil
}

func (r WorkflowRun) checkDefinition(definition WorkflowDefinition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	if r.Definition != definition.Ref() {
		return &DomainError{Kind: ErrConflict, Field: "workflowRun.definition", Message: "run is pinned to another workflow definition"}
	}
	return nil
}

func (r WorkflowRun) ensureActive() error {
	if r.Status != WorkflowRunRunning && r.Status != WorkflowRunWaiting {
		return transition("workflowRun", string(r.Status), "mutate_node")
	}
	return nil
}

func (r *WorkflowRun) unlockSuccessors(definition WorkflowDefinition) {
	predecessors := map[string][]string{}
	for _, edge := range definition.Edges {
		predecessors[edge.To] = append(predecessors[edge.To], edge.From)
	}
	for nodeID, node := range r.Nodes {
		if node.Status != NodeRunPending {
			continue
		}
		ready := true
		for _, predecessor := range predecessors[nodeID] {
			status := r.Nodes[predecessor].Status
			if status != NodeRunSucceeded && status != NodeRunSkipped {
				ready = false
				break
			}
		}
		if ready {
			node.Status = NodeRunReady
		}
	}
}

func (r *WorkflowRun) refreshStatus() {
	readyOrRunning, waiting, unfinished := false, false, false
	for _, node := range r.Nodes {
		switch node.Status {
		case NodeRunReady, NodeRunRunning:
			readyOrRunning = true
			unfinished = true
		case NodeRunWaitingReview:
			waiting = true
			unfinished = true
		case NodeRunPending:
			unfinished = true
		case NodeRunFailed:
			r.Status = WorkflowRunFailed
			return
		}
	}
	switch {
	case !unfinished:
		r.Status = WorkflowRunSucceeded
	case readyOrRunning:
		r.Status = WorkflowRunRunning
	case waiting:
		r.Status = WorkflowRunWaiting
	}
}

type DeliverySliceStatus string

const (
	SlicePlanned    DeliverySliceStatus = "planned"
	SliceReady      DeliverySliceStatus = "ready"
	SliceInProgress DeliverySliceStatus = "in_progress"
	SliceInReview   DeliverySliceStatus = "in_review"
	SliceBlocked    DeliverySliceStatus = "blocked"
	SliceCompleted  DeliverySliceStatus = "completed"
	SliceCancelled  DeliverySliceStatus = "cancelled"
)

type DeliverySlice struct {
	ID                string              `json:"id"`
	ProjectID         string              `json:"projectId"`
	Key               string              `json:"key"`
	Title             string              `json:"title"`
	Blueprint         ArtifactRef         `json:"blueprint"`
	Prototype         *ArtifactRef        `json:"prototype,omitempty"`
	Sources           []ArtifactRef       `json:"sources"`
	NodeKeys          []string            `json:"nodeKeys"`
	RequiresPrototype bool                `json:"requiresPrototype"`
	OwnerID           string              `json:"ownerId,omitempty"`
	Status            DeliverySliceStatus `json:"status"`
	BlockedReason     string              `json:"blockedReason,omitempty"`
	Version           uint64              `json:"version"`
	CreatedAt         time.Time           `json:"createdAt"`
	UpdatedAt         time.Time           `json:"updatedAt"`
}

func NewDeliverySlice(id, projectID, key, title string, blueprint ArtifactRef, prototype *ArtifactRef, sources []ArtifactRef, nodeKeys []string, requiresPrototype bool, now time.Time) (*DeliverySlice, error) {
	slice := &DeliverySlice{
		ID: strings.TrimSpace(id), ProjectID: strings.TrimSpace(projectID), Key: strings.TrimSpace(key), Title: strings.TrimSpace(title),
		Blueprint: blueprint, Sources: append([]ArtifactRef(nil), sources...), NodeKeys: append([]string(nil), nodeKeys...),
		RequiresPrototype: requiresPrototype, Status: SlicePlanned, Version: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if prototype != nil {
		copyRef := *prototype
		slice.Prototype = &copyRef
	}
	if slice.ID == "" || slice.ProjectID == "" || slice.Key == "" || slice.Title == "" {
		return nil, invalid("deliverySlice", "id, projectId, key and title are required")
	}
	if err := slice.validatePins(); err != nil {
		return nil, err
	}
	if len(slice.NodeKeys) == 0 {
		return nil, invalid("deliverySlice.nodeKeys", "at least one blueprint node is required")
	}
	return slice, nil
}

func (s DeliverySlice) validatePins() error {
	if err := s.Blueprint.Validate(); err != nil {
		return &DomainError{Kind: ErrManifestUnpinned, Field: "deliverySlice.blueprint", Message: err.Error()}
	}
	if s.Prototype != nil {
		if err := s.Prototype.Validate(); err != nil {
			return &DomainError{Kind: ErrManifestUnpinned, Field: "deliverySlice.prototype", Message: err.Error()}
		}
	}
	for index, source := range s.Sources {
		if err := source.Validate(); err != nil {
			return &DomainError{Kind: ErrManifestUnpinned, Field: fmt.Sprintf("deliverySlice.sources[%d]", index), Message: err.Error()}
		}
	}
	return nil
}

func (s *DeliverySlice) Assign(ownerID string, expectedVersion uint64, now time.Time) error {
	if s.Version != expectedVersion {
		return conflict("deliverySlice", expectedVersion, s.Version)
	}
	if strings.TrimSpace(ownerID) == "" {
		return invalid("deliverySlice.ownerId", "is required")
	}
	if s.Status == SliceCompleted || s.Status == SliceCancelled {
		return transition("deliverySlice", string(s.Status), "assign")
	}
	s.OwnerID = ownerID
	s.Version++
	s.UpdatedAt = now.UTC()
	return nil
}

func (s *DeliverySlice) BindPrototype(ref ArtifactRef, expectedVersion uint64, now time.Time) error {
	if s.Version != expectedVersion {
		return conflict("deliverySlice", expectedVersion, s.Version)
	}
	if s.Status != SlicePlanned && s.Status != SliceBlocked {
		return transition("deliverySlice", string(s.Status), "bind_prototype")
	}
	if err := ref.Validate(); err != nil {
		return &DomainError{Kind: ErrManifestUnpinned, Field: "deliverySlice.prototype", Message: err.Error()}
	}
	s.Prototype = &ref
	s.Version++
	s.UpdatedAt = now.UTC()
	return nil
}

func (s *DeliverySlice) MarkReady(expectedVersion uint64, now time.Time) error {
	if err := s.validatePins(); err != nil {
		return err
	}
	if s.RequiresPrototype && s.Prototype == nil {
		return invalid("deliverySlice.prototype", "is required before the slice is ready")
	}
	return s.transition(expectedVersion, SliceReady, now, SlicePlanned)
}

func (s *DeliverySlice) Start(expectedVersion uint64, now time.Time) error {
	return s.transition(expectedVersion, SliceInProgress, now, SliceReady)
}

func (s *DeliverySlice) SubmitReview(expectedVersion uint64, now time.Time) error {
	return s.transition(expectedVersion, SliceInReview, now, SliceInProgress)
}

func (s *DeliverySlice) RequestChanges(expectedVersion uint64, now time.Time) error {
	return s.transition(expectedVersion, SliceInProgress, now, SliceInReview)
}

func (s *DeliverySlice) Complete(expectedVersion uint64, now time.Time) error {
	return s.transition(expectedVersion, SliceCompleted, now, SliceInReview)
}

func (s *DeliverySlice) Block(reason string, expectedVersion uint64, now time.Time) error {
	if strings.TrimSpace(reason) == "" {
		return invalid("deliverySlice.blockedReason", "is required")
	}
	if err := s.transition(expectedVersion, SliceBlocked, now, SlicePlanned, SliceReady, SliceInProgress, SliceInReview); err != nil {
		return err
	}
	s.BlockedReason = strings.TrimSpace(reason)
	return nil
}

func (s *DeliverySlice) Unblock(expectedVersion uint64, now time.Time) error {
	if err := s.transition(expectedVersion, SlicePlanned, now, SliceBlocked); err != nil {
		return err
	}
	s.BlockedReason = ""
	return nil
}

func (s *DeliverySlice) Cancel(expectedVersion uint64, now time.Time) error {
	return s.transition(expectedVersion, SliceCancelled, now, SlicePlanned, SliceReady, SliceInProgress, SliceInReview, SliceBlocked)
}

func (s *DeliverySlice) transition(expectedVersion uint64, target DeliverySliceStatus, now time.Time, allowed ...DeliverySliceStatus) error {
	if s.Version != expectedVersion {
		return conflict("deliverySlice", expectedVersion, s.Version)
	}
	for _, status := range allowed {
		if s.Status == status {
			s.Status = target
			s.Version++
			s.UpdatedAt = now.UTC()
			return nil
		}
	}
	return transition("deliverySlice", string(s.Status), string(target))
}

// DeterministicNodeOrder is useful to orchestrators and test diagnostics.
func DeterministicNodeOrder(definition WorkflowDefinition) []string {
	indegree := map[string]int{}
	adjacency := map[string][]string{}
	for _, node := range definition.Nodes {
		indegree[node.ID] = 0
	}
	for _, edge := range definition.Edges {
		indegree[edge.To]++
		adjacency[edge.From] = append(adjacency[edge.From], edge.To)
	}
	queue := make([]string, 0)
	for id, count := range indegree {
		if count == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)
	order := make([]string, 0, len(indegree))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, id)
		for _, target := range adjacency[id] {
			indegree[target]--
			if indegree[target] == 0 {
				queue = append(queue, target)
				sort.Strings(queue)
			}
		}
	}
	return order
}
