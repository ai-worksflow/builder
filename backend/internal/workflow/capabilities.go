package workflow

import (
	"fmt"
	"sort"
	"strings"

	"github.com/worksflow/builder/backend/internal/domain"
)

type AITransformCapability struct {
	JobType               string   `json:"jobType"`
	OutputSchemaVersion   string   `json:"outputSchemaVersion"`
	ModelPolicies         []string `json:"modelPolicies"`
	RequiredArtifactKinds []string `json:"requiredArtifactKinds"`
	RequiredApprovedKinds []string `json:"requiredApprovedKinds"`
	ProducedArtifactKinds []string `json:"producedArtifactKinds"`
}

type ManifestCompilerCapability struct {
	ManifestKind                string   `json:"manifestKind"`
	SchemaVersion               int      `json:"schemaVersion"`
	Hook                        string   `json:"hook"`
	RequiredArtifactKinds       []string `json:"requiredArtifactKinds"`
	RequiredApprovedKinds       []string `json:"requiredApprovedKinds"`
	RequiresMergedSlices        bool     `json:"requiresMergedSlices"`
	ProducedSemanticKinds       []string `json:"producedSemanticKinds"`
	AllowedContextArtifactKinds []string `json:"allowedContextArtifactKinds"`
}

type WorkflowAnalysisLimits struct {
	MaximumDefinitionNodes     int `json:"maximumDefinitionNodes"`
	MaximumDefinitionEdges     int `json:"maximumDefinitionEdges"`
	MaxSemanticPathStates      int `json:"maxSemanticPathStates"`
	MaximumConditionExpression int `json:"maximumConditionExpressionBytes"`
}

const (
	maximumWorkflowDefinitionNodes = 200
	maximumWorkflowDefinitionEdges = 1000
)

// WorkflowCapabilities is the server-authoritative vocabulary from which a
// client may compose executable workflow versions.
type WorkflowCapabilities struct {
	Version                 int                             `json:"version"`
	NodeTypes               []domain.WorkflowNodeType       `json:"nodeTypes"`
	InputContracts          []domain.WorkflowInputContract  `json:"inputContracts"`
	OutputContracts         []domain.WorkflowOutputContract `json:"outputContracts"`
	AITransforms            []AITransformCapability         `json:"aiTransforms"`
	ManifestCompilers       []ManifestCompilerCapability    `json:"manifestCompilers"`
	Transforms              []string                        `json:"transforms"`
	FanOutItemKinds         []string                        `json:"fanOutItemKinds"`
	FanOutMaximumItems      map[string]int                  `json:"fanOutMaximumItems"`
	QualityGates            []string                        `json:"qualityGates"`
	PublishEnvironments     []string                        `json:"publishEnvironments"`
	WorkbenchSchemaVersions []int                           `json:"workbenchSchemaVersions"`
	AnalysisLimits          WorkflowAnalysisLimits          `json:"analysisLimits"`
	// ExternalQualificationGate is omitted from the frozen v0-v2 canonical
	// descriptors. Profile v3 pins the one closed, non-waivable declaration.
	ExternalQualificationGate *domain.ExternalQualificationGateNodeConfig `json:"externalQualificationGate,omitempty"`
}

func PlatformWorkflowCapabilities(quality, publish bool) WorkflowCapabilities {
	nodeTypes := []domain.WorkflowNodeType{
		domain.NodeArtifactInput, domain.NodeAITransform, domain.NodeHumanEdit,
		domain.NodeReviewGate, domain.NodeCondition, domain.NodeFanOut,
		domain.NodeMerge, domain.NodeManifestCompiler, domain.NodeWorkbenchBuild,
		domain.NodeTransform,
	}
	capabilities := WorkflowCapabilities{
		Version:   4,
		NodeTypes: nodeTypes,
		InputContracts: []domain.WorkflowInputContract{
			ProjectBriefInputContract(), BlueprintSelectionInputContract(),
		},
		AITransforms: []AITransformCapability{
			{JobType: "refine_project_brief", OutputSchemaVersion: "project-brief-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"project_brief"}, ProducedArtifactKinds: []string{"project_brief"}},
			{JobType: "derive_requirements", OutputSchemaVersion: "requirements-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"project_brief"}, RequiredApprovedKinds: []string{"project_brief"}, ProducedArtifactKinds: []string{"product_requirements"}},
			{JobType: "decompose_pages", OutputSchemaVersion: "blueprint-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"product_requirements"}, RequiredApprovedKinds: []string{"product_requirements"}, ProducedArtifactKinds: []string{"blueprint"}},
			{JobType: "generate_page_spec", OutputSchemaVersion: "page-spec-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"blueprint"}, RequiredApprovedKinds: []string{"blueprint"}, ProducedArtifactKinds: []string{"page_spec"}},
			{JobType: "generate_prototype", OutputSchemaVersion: "prototype-proposal/v1", ModelPolicies: []string{"project-default"}, RequiredArtifactKinds: []string{"page_spec"}, RequiredApprovedKinds: []string{"page_spec"}, ProducedArtifactKinds: []string{"prototype"}},
		},
		ManifestCompilers: []ManifestCompilerCapability{{
			ManifestKind: "application_build", SchemaVersion: 1, Hook: "application-build-manifest/v1",
			RequiredArtifactKinds: []string{"blueprint", "page_spec", "prototype"}, RequiredApprovedKinds: []string{"blueprint", "page_spec", "prototype"},
			RequiresMergedSlices: true, ProducedSemanticKinds: []string{"application_build_manifest"},
			AllowedContextArtifactKinds: []string{
				"project_brief", "product_requirements", "decision_record", "glossary_policy", "reference_source", "change_request", "requirement_baseline",
				"blueprint", "page_spec", "prototype", "prototype_flow", "fixture_bundle",
				"api_contract", "data_contract", "permission_contract", "design_system", "token_set", "component_registry",
			},
		}},
		Transforms:      []string{"selection_passthrough"},
		FanOutItemKinds: []string{"blueprint_page", "blueprint_selection_page"},
		FanOutMaximumItems: map[string]int{
			"blueprint_page": domain.MaximumWorkflowFanOutItems, "blueprint_selection_page": domain.MaximumWorkflowFanOutItems,
		},
		WorkbenchSchemaVersions: []int{1},
		AnalysisLimits: WorkflowAnalysisLimits{
			MaximumDefinitionNodes: maximumWorkflowDefinitionNodes, MaximumDefinitionEdges: maximumWorkflowDefinitionEdges,
			MaxSemanticPathStates: proposalPathStateBudget, MaximumConditionExpression: maxConditionExpressionBytes,
		},
	}
	if quality {
		capabilities.NodeTypes = append(capabilities.NodeTypes, domain.NodeQualityGate)
		capabilities.QualityGates = []string{"release"}
	}
	if publish {
		capabilities.NodeTypes = append(capabilities.NodeTypes, domain.NodePublish)
		capabilities.PublishEnvironments = []string{"preview", "production"}
	}
	if quality && publish {
		capabilities.OutputContracts = []domain.WorkflowOutputContract{ApplicationOutputContract()}
	}
	sort.Slice(capabilities.NodeTypes, func(i, j int) bool { return capabilities.NodeTypes[i] < capabilities.NodeTypes[j] })
	return capabilities
}

func ProjectBriefInputContract() domain.WorkflowInputContract {
	return domain.WorkflowInputContract{
		Capability:       domain.WorkflowInputProjectBrief,
		ManifestJobTypes: []string{"conversation.workflow_intent", "workflow_start"},
		ArtifactKinds:    []string{"project_brief"}, MinimumArtifacts: 1, MaximumArtifacts: 1, RequireApproved: false,
		RequiredSourcePurposes: []string{"project_brief"},
		ManifestSchemaContracts: map[string]string{
			"conversation.workflow_intent": "workflow-intent-input/v1",
			"workflow_start":               "workflow-input/v1",
		},
	}
}

func BlueprintSelectionInputContract() domain.WorkflowInputContract {
	return domain.WorkflowInputContract{
		Capability: domain.WorkflowInputBlueprintSelection, ManifestJobTypes: []string{"blueprint.selection"},
		ArtifactKinds: []string{"blueprint"}, MinimumArtifacts: 2, MaximumArtifacts: 101, RequireApproved: true,
		RequiredSourcePurposes:  []string{"blueprint_selection_node", "blueprint_selection_root"},
		ManifestSchemaContracts: map[string]string{"blueprint.selection": "blueprint-selection/v1"},
	}
}

func ApplicationOutputContract() domain.WorkflowOutputContract {
	return domain.WorkflowOutputContract{
		Capability: domain.WorkflowOutputApplication, ProducedArtifactKinds: []string{"workspace"},
		TerminalOutcome: domain.WorkflowOutcomeDeployment, TerminalNodeType: domain.NodePublish,
	}
}

func (c WorkflowCapabilities) ValidateDefinition(definition domain.WorkflowDefinition) error {
	if c.Version >= 5 {
		if c.ExternalQualificationGate == nil || !c.ExternalQualificationGate.IsExact() {
			return capabilityError("workflow.capabilities.externalQualificationGate", "capability schema v5 requires the exact closed non-waivable declaration")
		}
	}
	if definition.InputContract == nil || definition.OutputContract == nil {
		return capabilityError("workflow.contracts", "new workflow versions require inputContract and outputContract")
	}
	if !containsInputContract(c.InputContracts, *definition.InputContract) {
		return capabilityError("workflow.inputContract", "is not registered by this server")
	}
	if !containsOutputContract(c.OutputContracts, *definition.OutputContract) {
		return capabilityError("workflow.outputContract", "is not registered by this server")
	}
	entryID, err := definition.EntryNodeID()
	if err != nil {
		return err
	}
	entry, _ := definition.FindNode(entryID)
	if entry.ArtifactInput == nil {
		return capabilityError("workflow.entry", "input contract requires an artifact_input entry")
	}
	if !sameStrings(entry.ArtifactInput.AllowedKinds, definition.InputContract.ArtifactKinds) {
		return capabilityError("workflow.entry.artifactInput.allowedKinds", "must exactly match inputContract.artifactKinds")
	}
	if entry.ArtifactInput.MinimumArtifacts != definition.InputContract.MinimumArtifacts || entry.ArtifactInput.MaximumArtifacts != definition.InputContract.MaximumArtifacts || entry.ArtifactInput.RequireApproved != definition.InputContract.RequireApproved {
		return capabilityError("workflow.entry.artifactInput", "minimumArtifacts, maximumArtifacts and requireApproved must exactly match inputContract policy")
	}
	expectedTypes := make([]string, 0, len(definition.InputContract.ArtifactKinds))
	for _, kind := range definition.InputContract.ArtifactKinds {
		artifactType, ok := domain.WorkflowArtifactTypeForKind(kind)
		if !ok {
			return capabilityError("workflow.inputContract.artifactKinds", "contains an unsupported kind")
		}
		if !containsString(expectedTypes, string(artifactType)) {
			expectedTypes = append(expectedTypes, string(artifactType))
		}
	}
	actualTypes := make([]string, len(entry.ArtifactInput.AllowedTypes))
	for index, artifactType := range entry.ArtifactInput.AllowedTypes {
		actualTypes[index] = string(artifactType)
	}
	if !sameStrings(actualTypes, expectedTypes) {
		return capabilityError("workflow.entry.artifactInput.allowedTypes", "must exactly match the broad categories implied by allowedKinds")
	}

	hasWorkbench, hasPublish := false, false
	for _, node := range definition.Nodes {
		if !containsNodeType(c.NodeTypes, node.Type) {
			return capabilityError("workflow.nodes."+node.ID, "node type is not registered by this server")
		}
		switch node.Type {
		case domain.NodeArtifactInput:
			if node.ID != entryID {
				return capabilityError("workflow.nodes."+node.ID, "artifact_input is only supported as the single workflow entry")
			}
		case domain.NodeAITransform:
			if node.AITransform == nil || !containsAITransform(c.AITransforms, node.AITransform.JobType, node.AITransform.OutputSchemaVersion, node.AITransform.ModelPolicy) {
				return capabilityError("workflow.nodes."+node.ID+".aiTransform", "jobType/outputSchemaVersion/modelPolicy is not registered by this server")
			}
		case domain.NodeHumanEdit:
			if node.HumanEdit == nil || strings.TrimSpace(node.HumanEdit.ArtifactKind) == "" {
				return capabilityError("workflow.nodes."+node.ID+".humanEdit.artifactKind", "an exact artifact kind is required")
			}
		case domain.NodeReviewGate:
			if node.ReviewGate == nil || node.ReviewGate.MinimumApprovals < 1 || !node.ReviewGate.ProhibitSelfReview || node.ReviewGate.AllowWaiver {
				return capabilityError("workflow.nodes."+node.ID+".reviewGate", "application semantic approval requires minimumApprovals>=1, prohibitSelfReview=true, and allowWaiver=false")
			}
		case domain.NodeFanOut:
			if node.FanOut == nil {
				return capabilityError("workflow.nodes."+node.ID+".fanOut", "config is required")
			}
			kind := "generic"
			if strings.TrimSpace(node.FanOut.ItemKind) != "" {
				kind = strings.TrimSpace(node.FanOut.ItemKind)
			}
			if !containsString(c.FanOutItemKinds, kind) {
				return capabilityError("workflow.nodes."+node.ID+".fanOut.itemKind", "is not registered by this server")
			}
			limit, registered := c.FanOutMaximumItems[kind]
			if !registered || limit < 1 || node.FanOut.MaxItems < 1 || node.FanOut.MaxItems > limit {
				return capabilityError("workflow.nodes."+node.ID+".fanOut.maxItems", "must be positive and no greater than the registered resolver limit")
			}
		case domain.NodeMerge:
			if node.Merge == nil || node.Merge.Policy != domain.MergeAll || node.Merge.AllowWaiver {
				return capabilityError("workflow.nodes."+node.ID+".merge", "application fan-out merge requires policy=all and allowWaiver=false")
			}
		case domain.NodeManifestCompiler:
			if node.ManifestCompiler == nil || !containsManifestCompiler(c.ManifestCompilers, *node.ManifestCompiler) {
				return capabilityError("workflow.nodes."+node.ID+".manifestCompiler", "kind/schemaVersion/hook is not registered by this server")
			}
		case domain.NodeWorkbenchBuild:
			hasWorkbench = true
			if node.WorkbenchBuild == nil || !containsInt(c.WorkbenchSchemaVersions, node.WorkbenchBuild.BuildManifestSchemaVersion) {
				return capabilityError("workflow.nodes."+node.ID+".workbenchBuild", "build manifest schema is not registered by this server")
			}
		case domain.NodeQualityGate:
			if node.QualityGate == nil || !containsString(c.QualityGates, node.QualityGate.GateName) {
				return capabilityError("workflow.nodes."+node.ID+".qualityGate", "gate is not registered by this server")
			}
		case domain.NodePublish:
			hasPublish = true
			if node.Publish == nil || !containsString(c.PublishEnvironments, node.Publish.Environment) {
				return capabilityError("workflow.nodes."+node.ID+".publish", "environment is not registered by this server")
			}
		case domain.NodeExternalQualificationGate:
			if node.ExternalQualificationGate == nil || c.ExternalQualificationGate == nil ||
				*node.ExternalQualificationGate != *c.ExternalQualificationGate || !node.ExternalQualificationGate.IsExact() {
				return capabilityError("workflow.nodes."+node.ID+".externalQualificationGate", "config must exactly match the closed non-waivable capability declaration")
			}
		case domain.NodeTransform:
			if node.Transform == nil || !containsString(c.Transforms, node.Transform.Transform) {
				return capabilityError("workflow.nodes."+node.ID+".transform", "transform is not registered by this server")
			}
		}
	}
	for _, kind := range definition.OutputContract.ProducedArtifactKinds {
		if kind == "workspace" && !hasWorkbench {
			return capabilityError("workflow.outputContract", "workspace output requires a real workbench_build node")
		}
	}
	if definition.OutputContract.TerminalOutcome == domain.WorkflowOutcomeApplication && !hasWorkbench {
		return capabilityError("workflow.outputContract", "application outcome requires a real workbench_build node")
	}
	if definition.OutputContract.TerminalOutcome == domain.WorkflowOutcomeDeployment && !hasPublish {
		return capabilityError("workflow.outputContract", "deployment outcome requires a real publish node")
	}
	if c.Version >= 4 {
		if err := validateCurrentDefinitionPortSchemas(definition); err != nil {
			return err
		}
		if err := validateCurrentConditionExpressions(definition); err != nil {
			return err
		}
	}
	if err := validateGovernedEditReviewOwnership(definition); err != nil {
		return err
	}
	if err := c.validateProposalConsumerTopology(definition); err != nil {
		return err
	}
	return c.validateApplicationDeliveryTopology(definition)
}

// Each materialized revision has one owning review transition. Branching is
// allowed after approval; branching before approval would let one review
// request changes while a sibling gate keeps an obsolete revision completed.
func validateGovernedEditReviewOwnership(definition domain.WorkflowDefinition) error {
	children := make(map[string][]string, len(definition.Nodes))
	for _, edge := range definition.Edges {
		children[edge.From] = append(children[edge.From], edge.To)
	}
	for _, node := range definition.Nodes {
		if node.Type != domain.NodeHumanEdit {
			continue
		}
		outgoing := children[node.ID]
		if len(outgoing) != 1 {
			return capabilityError("workflow.nodes."+node.ID, "governed HumanEdit requires exactly one owning ReviewGate before any branch")
		}
		child, exists := definition.FindNode(outgoing[0])
		if !exists || child.Type != domain.NodeReviewGate {
			return capabilityError("workflow.nodes."+node.ID, "governed HumanEdit must directly enter its owning ReviewGate")
		}
	}
	return nil
}

type proposalPathState struct {
	producers     map[string]string
	materializers map[string]string
	choices       map[string]string
}

const proposalPathStateBudget = 256

func (c WorkflowCapabilities) semanticPathStateBudget() int {
	if c.AnalysisLimits.MaxSemanticPathStates > 0 {
		return c.AnalysisLimits.MaxSemanticPathStates
	}
	return proposalPathStateBudget
}

// validateProposalConsumerTopology models normal joins as AND while retaining
// mutually exclusive Condition branches as alternatives. This prevents two
// identical-capability AI nodes from being collapsed into one semantic state.
func (c WorkflowCapabilities) validateProposalConsumerTopology(definition domain.WorkflowDefinition) error {
	budget := c.semanticPathStateBudget()
	nodes := make(map[string]domain.NodeDefinition, len(definition.Nodes))
	indegree := make(map[string]int, len(definition.Nodes))
	incoming := make(map[string][]domain.WorkflowEdge, len(definition.Nodes))
	adjacency := make(map[string][]string, len(definition.Nodes))
	for _, node := range definition.Nodes {
		nodes[node.ID], indegree[node.ID] = node, 0
	}
	for _, edge := range definition.Edges {
		indegree[edge.To]++
		incoming[edge.To] = append(incoming[edge.To], edge)
		adjacency[edge.From] = append(adjacency[edge.From], edge.To)
	}
	queue := make([]string, 0, len(nodes))
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)
	after := make(map[string][]proposalPathState, len(nodes))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		edges := append([]domain.WorkflowEdge(nil), incoming[id]...)
		sort.Slice(edges, func(left, right int) bool { return edges[left].ID < edges[right].ID })
		states := []proposalPathState{{producers: map[string]string{}, materializers: map[string]string{}, choices: map[string]string{}}}
		if len(edges) > 0 {
			edgeGroups := make([][]proposalPathState, 0, len(edges))
			for _, edge := range edges {
				edgeStates := make([]proposalPathState, 0, len(after[edge.From]))
				for _, state := range after[edge.From] {
					next := cloneProposalPathState(state)
					if source := nodes[edge.From]; source.Type == domain.NodeCondition {
						branch := strings.TrimSpace(edge.FromPort)
						if branch == "" {
							branch = "default"
						}
						next.choices[source.ID] = branch
					}
					edgeStates = appendUniqueProposalState(edgeStates, next)
				}
				edgeGroups = append(edgeGroups, edgeStates)
			}
			var combineErr error
			states, combineErr = combineProposalIncomingStates(edgeGroups, budget)
			if combineErr != nil {
				return capabilityError("workflow.nodes."+id, combineErr.Error())
			}
		}
		if len(states) == 0 {
			return capabilityError("workflow.nodes."+id, "has no executable proposal-lineage state")
		}
		node := nodes[id]
		for index := range states {
			switch node.Type {
			case domain.NodeAITransform:
				if len(states[index].producers) != 0 || len(states[index].materializers) != 0 {
					return capabilityError("workflow.nodes."+id, "AI transform cannot overwrite an unconsumed proposal or HumanEdit materialization")
				}
				capability, ok := findAITransform(c.AITransforms, *node.AITransform)
				if !ok || len(capability.ProducedArtifactKinds) != 1 {
					return capabilityError("workflow.nodes."+id, "AI proposal producer must resolve to one exact artifact kind")
				}
				states[index].producers = map[string]string{id: capability.ProducedArtifactKinds[0]}
			case domain.NodeHumanEdit:
				if len(states[index].producers) != 1 {
					return capabilityError("workflow.nodes."+id, "human edit requires exactly one AI proposal producer on every executable path")
				}
				for _, producedKind := range states[index].producers {
					if producedKind != node.HumanEdit.ArtifactKind {
						return capabilityError("workflow.nodes."+id, "human edit artifact kind must exactly match its AI proposal producer")
					}
				}
				states[index].producers = map[string]string{}
				states[index].materializers[id] = node.HumanEdit.ArtifactKind
			case domain.NodeReviewGate:
				if len(states[index].materializers) != 1 {
					return capabilityError("workflow.nodes."+id, "review gate requires exactly one HumanEdit materialization on every executable path")
				}
				states[index].materializers = map[string]string{}
			}
		}
		after[id] = nil
		for _, state := range states {
			after[id] = appendUniqueProposalState(after[id], state)
		}
		for _, successor := range adjacency[id] {
			indegree[successor]--
			if indegree[successor] == 0 {
				queue = append(queue, successor)
				sort.Strings(queue)
			}
		}
	}
	return nil
}

func combineProposalIncomingStates(groups [][]proposalPathState, budget int) ([]proposalPathState, error) {
	choiceMaps := make([]map[string]string, 0)
	for _, group := range groups {
		if len(group) > budget {
			return nil, fmt.Errorf("proposal-lineage analysis exceeded its deterministic state budget")
		}
		for _, state := range group {
			choiceMaps = append(choiceMaps, state.choices)
		}
	}
	assignments, err := maximalCompatibleChoiceAssignments(choiceMaps, budget)
	if err != nil {
		return nil, fmt.Errorf("proposal-lineage analysis exceeded its deterministic state budget")
	}
	combined := make([]proposalPathState, 0, len(assignments))
	for _, assignment := range assignments {
		var merged *proposalPathState
		for _, group := range groups {
			candidates := make([]proposalPathState, 0, 1)
			for _, state := range group {
				if choiceMapMatchesAssignment(state.choices, assignment) {
					candidates = appendUniqueProposalState(candidates, state)
				}
			}
			if len(candidates) > 1 {
				return nil, fmt.Errorf("proposal-lineage analysis found ambiguous states for one Condition assignment")
			}
			if len(candidates) == 0 {
				continue
			}
			if merged == nil {
				value := cloneProposalPathState(candidates[0])
				merged = &value
				continue
			}
			for producer, kind := range candidates[0].producers {
				merged.producers[producer] = kind
			}
			for materializer, kind := range candidates[0].materializers {
				merged.materializers[materializer] = kind
			}
		}
		if merged == nil {
			continue
		}
		merged.choices = cloneChoiceMap(assignment)
		combined = appendUniqueProposalState(combined, *merged)
		if len(combined) > budget {
			return nil, fmt.Errorf("proposal-lineage analysis exceeded its deterministic state budget")
		}
	}
	return combined, nil
}

func proposalChoicesCompatible(left, right map[string]string) bool {
	for condition, branch := range left {
		if other, present := right[condition]; present && other != branch {
			return false
		}
	}
	return true
}

func maximalCompatibleChoiceAssignments(choiceMaps []map[string]string, budget int) ([]map[string]string, error) {
	uniqueMaps := make([]map[string]string, 0, len(choiceMaps))
	for _, choices := range choiceMaps {
		uniqueMaps = appendUniqueChoiceMap(uniqueMaps, choices)
	}
	sort.Slice(uniqueMaps, func(left, right int) bool { return choiceMapKey(uniqueMaps[left]) < choiceMapKey(uniqueMaps[right]) })
	assignments := []map[string]string{{}}
	for _, choices := range uniqueMaps {
		base := append([]map[string]string(nil), assignments...)
		for _, assignment := range base {
			if !proposalChoicesCompatible(assignment, choices) {
				continue
			}
			merged := cloneChoiceMap(assignment)
			for condition, branch := range choices {
				merged[condition] = branch
			}
			assignments = appendUniqueChoiceMap(assignments, merged)
			if len(assignments) > budget {
				return nil, fmt.Errorf("condition assignment budget exceeded")
			}
		}
	}
	maximal := make([]map[string]string, 0, len(assignments))
	for index, assignment := range assignments {
		subsumed := false
		for otherIndex, other := range assignments {
			if index != otherIndex && len(other) > len(assignment) && choiceMapSubset(assignment, other) {
				subsumed = true
				break
			}
		}
		if !subsumed {
			maximal = appendUniqueChoiceMap(maximal, assignment)
		}
	}
	sort.Slice(maximal, func(left, right int) bool { return choiceMapKey(maximal[left]) < choiceMapKey(maximal[right]) })
	return maximal, nil
}

func choiceMapMatchesAssignment(choices, assignment map[string]string) bool {
	return choiceMapSubset(choices, assignment)
}

func choiceMapSubset(subset, superset map[string]string) bool {
	for condition, branch := range subset {
		if superset[condition] != branch {
			return false
		}
	}
	return true
}

func appendUniqueChoiceMap(maps []map[string]string, candidate map[string]string) []map[string]string {
	key := choiceMapKey(candidate)
	for _, existing := range maps {
		if choiceMapKey(existing) == key {
			return maps
		}
	}
	return append(maps, cloneChoiceMap(candidate))
}

func cloneChoiceMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for condition, branch := range source {
		clone[condition] = branch
	}
	return clone
}

func choiceMapKey(choices map[string]string) string {
	values := make([]string, 0, len(choices))
	for condition, branch := range choices {
		values = append(values, condition+"="+branch)
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

func cloneProposalPathState(state proposalPathState) proposalPathState {
	clone := proposalPathState{producers: map[string]string{}, materializers: map[string]string{}, choices: map[string]string{}}
	for producer, kind := range state.producers {
		clone.producers[producer] = kind
	}
	for materializer, kind := range state.materializers {
		clone.materializers[materializer] = kind
	}
	for condition, branch := range state.choices {
		clone.choices[condition] = branch
	}
	return clone
}

func appendUniqueProposalState(states []proposalPathState, candidate proposalPathState) []proposalPathState {
	key := proposalPathStateKey(candidate)
	for _, state := range states {
		if proposalPathStateKey(state) == key {
			return states
		}
	}
	return append(states, candidate)
}

func proposalPathStateKey(state proposalPathState) string {
	producers, materializers, choices := make([]string, 0, len(state.producers)), make([]string, 0, len(state.materializers)), make([]string, 0, len(state.choices))
	for producer, kind := range state.producers {
		producers = append(producers, producer+"="+kind)
	}
	for materializer, kind := range state.materializers {
		materializers = append(materializers, materializer+"="+kind)
	}
	for condition, branch := range state.choices {
		choices = append(choices, condition+"="+branch)
	}
	sort.Strings(producers)
	sort.Strings(materializers)
	sort.Strings(choices)
	return strings.Join(producers, ",") + "|" + strings.Join(materializers, ",") + "|" + strings.Join(choices, ",")
}

type capabilityPathState struct {
	available               map[string]bool
	approved                map[string]bool
	artifactLineage         map[string]string
	pendingReview           string
	pendingMaterializer     string
	pendingReviewFanOut     string
	activeFanOut            string
	selectionBindingsFrozen bool
	selectionPassed         bool
	slicePageSpecProduced   bool
	slicePageSpecApproved   bool
	slicePrototypeProduced  bool
	slicePrototypeApproved  bool
	mergedSlices            bool
	mergedFanOut            string
	deliveryStage           int
	choices                 map[string]string
}

func (c WorkflowCapabilities) validateApplicationDeliveryTopology(definition domain.WorkflowDefinition) error {
	budget := c.semanticPathStateBudget()
	terminalDeliveryStage := 4
	if c.ExternalQualificationGate != nil {
		terminalDeliveryStage = 5
	}
	entryID, err := definition.EntryNodeID()
	if err != nil {
		return err
	}
	terminalID, err := definition.TerminalNodeID()
	if err != nil {
		return err
	}
	nodes := make(map[string]domain.NodeDefinition, len(definition.Nodes))
	indegree := make(map[string]int, len(definition.Nodes))
	adjacency := make(map[string][]string, len(definition.Nodes))
	incomingEdges := make(map[string][]domain.WorkflowEdge, len(definition.Nodes))
	predecessors := make(map[string][]string, len(definition.Nodes))
	for _, node := range definition.Nodes {
		nodes[node.ID] = node
		indegree[node.ID] = 0
	}
	for _, edge := range definition.Edges {
		indegree[edge.To]++
		adjacency[edge.From] = append(adjacency[edge.From], edge.To)
		incomingEdges[edge.To] = append(incomingEdges[edge.To], edge)
		predecessors[edge.To] = append(predecessors[edge.To], edge.From)
	}
	queue := make([]string, 0, len(nodes))
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)
	after := make(map[string][]capabilityPathState, len(nodes))
	initial := capabilityPathState{
		available: map[string]bool{}, approved: map[string]bool{}, artifactLineage: map[string]string{}, choices: map[string]string{},
	}
	for _, kind := range definition.InputContract.ArtifactKinds {
		initial.available[kind] = true
		initial.artifactLineage[kind] = "input:" + kind
		if definition.InputContract.RequireApproved {
			initial.approved[kind] = true
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		node := nodes[id]
		edges := append([]domain.WorkflowEdge(nil), incomingEdges[id]...)
		sort.Slice(edges, func(left, right int) bool { return edges[left].ID < edges[right].ID })
		var incoming []capabilityPathState
		if id == entryID && len(edges) == 0 {
			incoming = []capabilityPathState{initial}
		} else {
			edgeGroups := make([][]capabilityPathState, 0, len(edges))
			for _, edge := range edges {
				edgeStates := make([]capabilityPathState, 0, len(after[edge.From]))
				for _, state := range after[edge.From] {
					next := cloneCapabilityPathState(state)
					if source := nodes[edge.From]; source.Type == domain.NodeCondition {
						branch := strings.TrimSpace(edge.FromPort)
						if branch == "" {
							branch = "default"
						}
						next.choices[source.ID] = branch
					}
					edgeStates = appendUniqueCapabilityState(edgeStates, next)
				}
				edgeGroups = append(edgeGroups, edgeStates)
			}
			var combineErr error
			incoming, combineErr = combineCapabilityIncomingStates(edgeGroups, budget)
			if combineErr != nil {
				return capabilityError("workflow.nodes."+id, combineErr.Error())
			}
		}
		if len(incoming) == 0 {
			return capabilityError("workflow.nodes."+id, "has no executable semantic input state")
		}
		outgoing := make([]capabilityPathState, 0, len(incoming))
		for _, state := range incoming {
			next, transitionErr := c.transitionCapabilityState(definition, node, state, predecessors, terminalID)
			if transitionErr != nil {
				return transitionErr
			}
			outgoing = appendUniqueCapabilityState(outgoing, next)
			if len(outgoing) > budget {
				return capabilityError("workflow.nodes."+id, "application semantic analysis exceeded its deterministic state budget")
			}
		}
		after[id] = outgoing
		for _, successor := range adjacency[id] {
			indegree[successor]--
			if indegree[successor] == 0 {
				queue = append(queue, successor)
				sort.Strings(queue)
			}
		}
	}
	terminalStates := after[terminalID]
	for _, state := range terminalStates {
		if state.deliveryStage != terminalDeliveryStage {
			return capabilityError("workflow.outputContract", "every successful path must end in a verified deployment")
		}
	}
	return nil
}

func (c WorkflowCapabilities) transitionCapabilityState(
	definition domain.WorkflowDefinition,
	node domain.NodeDefinition,
	state capabilityPathState,
	predecessors map[string][]string,
	terminalID string,
) (capabilityPathState, error) {
	next := cloneCapabilityPathState(state)
	terminalDeliveryStage := 4
	if c.ExternalQualificationGate != nil {
		terminalDeliveryStage = 5
	}
	if next.deliveryStage == terminalDeliveryStage && node.ID != terminalID {
		return state, capabilityError("workflow.nodes."+node.ID, "cannot execute after publish")
	}
	switch node.Type {
	case domain.NodeArtifactInput:
		return next, nil
	case domain.NodeAITransform:
		capability, ok := findAITransform(c.AITransforms, *node.AITransform)
		if !ok {
			return state, capabilityError("workflow.nodes."+node.ID, "AI capability is not registered")
		}
		if !hasCapabilityKinds(next.available, capability.RequiredArtifactKinds) || !hasCapabilityKinds(next.approved, capability.RequiredApprovedKinds) {
			return state, capabilityError("workflow.nodes."+node.ID, "AI capability semantic artifact prerequisites are not satisfied on every path")
		}
		if node.AITransform.JobType == "generate_page_spec" && !isActiveFanOutKind(definition, next.activeFanOut, "blueprint_page") {
			return state, capabilityError("workflow.nodes."+node.ID, "PageSpec generation must run inside the active blueprint_page fan-out epoch")
		}
		if node.AITransform.JobType == "generate_prototype" && (!isActiveFanOutKind(definition, next.activeFanOut, "blueprint_page") || !next.slicePageSpecProduced || !next.slicePageSpecApproved) {
			return state, capabilityError("workflow.nodes."+node.ID, "Prototype generation requires an approved PageSpec produced in the same active fan-out epoch")
		}
		for _, kind := range capability.ProducedArtifactKinds {
			invalidateDownstreamArtifactState(&next, kind)
			next.available[kind] = true
			next.artifactLineage[kind] = "ai:" + node.ID
			delete(next.approved, kind)
			if next.pendingReview == kind {
				next.pendingReview = ""
				next.pendingMaterializer = ""
				next.pendingReviewFanOut = ""
			}
			switch kind {
			case "page_spec":
				next.slicePageSpecProduced, next.slicePageSpecApproved = true, false
			case "prototype":
				next.slicePrototypeProduced, next.slicePrototypeApproved = true, false
			}
		}
	case domain.NodeHumanEdit:
		kind := node.HumanEdit.ArtifactKind
		if !next.available[kind] {
			return state, capabilityError("workflow.nodes."+node.ID, "human edit has no matching semantic artifact or proposal input")
		}
		if kind == "page_spec" && (!isActiveFanOutKind(definition, next.activeFanOut, "blueprint_page") || !next.slicePageSpecProduced) {
			return state, capabilityError("workflow.nodes."+node.ID, "PageSpec edit must consume a proposal produced in the same active fan-out epoch")
		}
		if kind == "prototype" && (!isActiveFanOutKind(definition, next.activeFanOut, "blueprint_page") || !next.slicePrototypeProduced || !next.slicePageSpecApproved) {
			return state, capabilityError("workflow.nodes."+node.ID, "Prototype edit must consume a proposal produced in the same active fan-out epoch")
		}
		invalidateDownstreamArtifactState(&next, kind)
		next.available[kind] = true
		next.artifactLineage[kind] = "human:" + node.ID
		delete(next.approved, kind)
		next.pendingReview = kind
		next.pendingMaterializer = node.ID
		if kind == "page_spec" || kind == "prototype" {
			next.pendingReviewFanOut = next.activeFanOut
		} else {
			next.pendingReviewFanOut = ""
		}
	case domain.NodeReviewGate:
		parents := predecessors[node.ID]
		if len(parents) == 0 {
			return state, capabilityError("workflow.nodes."+node.ID, "review gate requires a direct HumanEdit input")
		}
		for _, parentID := range parents {
			parent, _ := definition.FindNode(parentID)
			if parent.Type != domain.NodeHumanEdit {
				return state, capabilityError("workflow.nodes."+node.ID, "governed semantic review must directly consume HumanEdit outputs so request-changes can reopen the exact materializer")
			}
		}
		if next.pendingReview == "" {
			return state, capabilityError("workflow.nodes."+node.ID, "review gate has no exact edited artifact to approve")
		}
		if next.pendingReviewFanOut != "" && next.pendingReviewFanOut != next.activeFanOut {
			return state, capabilityError("workflow.nodes."+node.ID, "slice review no longer belongs to the active fan-out epoch")
		}
		next.approved[next.pendingReview] = true
		switch next.pendingReview {
		case "page_spec":
			next.slicePageSpecApproved = true
		case "prototype":
			next.slicePrototypeApproved = true
		}
		next.pendingReview = ""
		next.pendingMaterializer = ""
		next.pendingReviewFanOut = ""
	case domain.NodeFanOut:
		kind := node.FanOut.ItemKind
		if kind == "" {
			kind = "generic"
		}
		next.selectionBindingsFrozen = false
		switch kind {
		case "blueprint_page":
			if !next.approved["blueprint"] {
				return state, capabilityError("workflow.nodes."+node.ID, "blueprint_page fan-out requires an approved Blueprint")
			}
		case "blueprint_selection_page":
			if definition.InputContract.Capability != domain.WorkflowInputBlueprintSelection {
				return state, capabilityError("workflow.nodes."+node.ID, "selection fan-out requires the Blueprint-selection input capability")
			}
			for _, artifactKind := range []string{"blueprint", "page_spec", "prototype"} {
				next.available[artifactKind], next.approved[artifactKind] = true, true
				next.artifactLineage[artifactKind] = "selection:" + node.ID
			}
			next.selectionBindingsFrozen = true
		}
		next.activeFanOut, next.selectionPassed, next.mergedSlices, next.mergedFanOut = node.ID, false, false, ""
		next.slicePageSpecProduced, next.slicePageSpecApproved = false, false
		next.slicePrototypeProduced, next.slicePrototypeApproved = false, false
	case domain.NodeTransform:
		if node.Transform.Transform == "selection_passthrough" {
			fanOut, exists := definition.FindNode(next.activeFanOut)
			if !exists || fanOut.FanOut == nil || fanOut.FanOut.ItemKind != "blueprint_selection_page" {
				return state, capabilityError("workflow.nodes."+node.ID, "selection_passthrough requires an active Blueprint-selection branch")
			}
			if !next.selectionBindingsFrozen {
				return state, capabilityError("workflow.nodes."+node.ID, "selection_passthrough requires server-validated frozen page bindings")
			}
			next.selectionPassed = true
			next.slicePageSpecProduced, next.slicePageSpecApproved = true, true
			next.slicePrototypeProduced, next.slicePrototypeApproved = true, true
		}
	case domain.NodeMerge:
		if next.activeFanOut != node.Merge.FanOutNodeID {
			return state, capabilityError("workflow.nodes."+node.ID, "merge has no matching active fan-out semantic lineage")
		}
		fanOut, _ := definition.FindNode(next.activeFanOut)
		if fanOut.FanOut != nil && fanOut.FanOut.ItemKind == "blueprint_selection_page" && (!next.selectionPassed || !next.selectionBindingsFrozen) {
			return state, capabilityError("workflow.nodes."+node.ID, "Blueprint-selection branches must pass through trusted selection_passthrough bindings")
		}
		if !next.approved["blueprint"] || !next.slicePageSpecProduced || !next.slicePageSpecApproved || !next.slicePrototypeProduced || !next.slicePrototypeApproved {
			return state, capabilityError("workflow.nodes."+node.ID, "merged application slices require PageSpec and Prototype produced and approved in the current fan-out epoch")
		}
		next.mergedFanOut, next.activeFanOut, next.mergedSlices = next.activeFanOut, "", true
	case domain.NodeManifestCompiler:
		capability, _ := findManifestCompiler(c.ManifestCompilers, *node.ManifestCompiler)
		if next.pendingReview != "" {
			return state, capabilityError("workflow.nodes."+node.ID, "manifest compiler cannot consume an artifact with a pending review")
		}
		if definition.InputContract.Capability == domain.WorkflowInputProjectBrief && (!next.approved["project_brief"] || !next.approved["product_requirements"]) {
			return state, capabilityError("workflow.nodes."+node.ID, "Project Brief application compilation requires current approved Project Brief and requirements lineage")
		}
		if !hasCapabilityKinds(next.available, capability.RequiredArtifactKinds) || !hasCapabilityKinds(next.approved, capability.RequiredApprovedKinds) || (capability.RequiresMergedSlices && !next.mergedSlices) {
			return state, capabilityError("workflow.nodes."+node.ID, "manifest compiler requires complete approved merged prototype-slice lineage")
		}
		for artifactKind, present := range next.available {
			if present && !containsString(capability.AllowedContextArtifactKinds, artifactKind) {
				return state, capabilityError("workflow.nodes."+node.ID, "application build context contains an artifact kind that generation cannot consume")
			}
		}
		if next.deliveryStage != 0 && next.deliveryStage != 2 {
			return state, capabilityError("workflow.nodes."+node.ID, "manifest compiler is duplicated or out of delivery order")
		}
		next.deliveryStage, next.mergedSlices, next.mergedFanOut = 1, false, ""
	case domain.NodeWorkbenchBuild:
		parents := predecessors[node.ID]
		if len(parents) != 1 {
			return state, capabilityError("workflow.nodes."+node.ID, "Workbench must consume one unambiguous manifest compiler output")
		}
		parent, _ := definition.FindNode(parents[0])
		if parent.Type != domain.NodeManifestCompiler || next.deliveryStage != 1 {
			return state, capabilityError("workflow.nodes."+node.ID, "Workbench must directly follow a registered manifest compiler")
		}
		next.deliveryStage = 2
	case domain.NodeQualityGate:
		if node.QualityGate.GateName != "release" || !node.QualityGate.Blocking || next.deliveryStage != 2 {
			return state, capabilityError("workflow.nodes."+node.ID, "application publish requires the blocking release quality gate after Workbench")
		}
		parents := predecessors[node.ID]
		if len(parents) == 0 {
			return state, capabilityError("workflow.nodes."+node.ID, "quality gate requires Workbench input")
		}
		for _, parentID := range parents {
			parent, _ := definition.FindNode(parentID)
			if parent.Type != domain.NodeWorkbenchBuild {
				return state, capabilityError("workflow.nodes."+node.ID, "quality gate may consume only exact Workbench outputs")
			}
		}
		next.deliveryStage = 3
	case domain.NodeExternalQualificationGate:
		parents := predecessors[node.ID]
		if c.ExternalQualificationGate == nil || node.ExternalQualificationGate == nil ||
			*node.ExternalQualificationGate != *c.ExternalQualificationGate || len(parents) != 1 || next.deliveryStage != 3 {
			return state, capabilityError("workflow.nodes."+node.ID, "external qualification must consume one exact blocking release quality result")
		}
		parent, _ := definition.FindNode(parents[0])
		if parent.Type != domain.NodeQualityGate || parent.QualityGate == nil ||
			parent.QualityGate.GateName != "release" || !parent.QualityGate.Blocking {
			return state, capabilityError("workflow.nodes."+node.ID, "external qualification must directly follow the blocking release quality gate")
		}
		next.deliveryStage = 4
	case domain.NodePublish:
		parents := predecessors[node.ID]
		expectedStage := 3
		expectedParentType := domain.NodeQualityGate
		if c.ExternalQualificationGate != nil {
			expectedStage = 4
			expectedParentType = domain.NodeExternalQualificationGate
		}
		if node.ID != terminalID || len(parents) != 1 || next.deliveryStage != expectedStage {
			return state, capabilityError("workflow.nodes."+node.ID, "publish must be the terminal consumer of one exact quality result")
		}
		parent, _ := definition.FindNode(parents[0])
		if parent.Type != expectedParentType {
			message := "publish must directly follow the blocking release quality gate"
			if c.ExternalQualificationGate != nil {
				message = "publish must directly follow the dedicated external qualification gate"
			}
			return state, capabilityError("workflow.nodes."+node.ID, message)
		}
		next.deliveryStage = terminalDeliveryStage
	}
	return next, nil
}

// invalidateDownstreamArtifactState is the semantic lineage epoch boundary.
// Regenerating or editing an upstream artifact makes every dependent kind and
// every frozen slice assembled from it stale until those stages run again.
func invalidateDownstreamArtifactState(state *capabilityPathState, artifactKind string) {
	downstream := map[string][]string{
		"project_brief":        {"product_requirements", "blueprint", "page_spec", "prototype"},
		"product_requirements": {"blueprint", "page_spec", "prototype"},
		"requirement_baseline": {"blueprint", "page_spec", "prototype"},
		"blueprint":            {"page_spec", "prototype"},
		"page_spec":            {"prototype"},
		"prototype":            {},
	}
	invalidated, semanticArtifact := downstream[artifactKind]
	if !semanticArtifact {
		return
	}
	for _, kind := range invalidated {
		delete(state.available, kind)
		delete(state.approved, kind)
		delete(state.artifactLineage, kind)
		if state.pendingReview == kind {
			state.pendingReview = ""
			state.pendingMaterializer = ""
			state.pendingReviewFanOut = ""
		}
	}
	// Even the leaf Prototype participates in the exact merged slice snapshot;
	// changing it advances the lineage epoch although it has no artifact-kind
	// descendants to remove.
	state.mergedSlices, state.mergedFanOut = false, ""
	switch artifactKind {
	case "page_spec":
		state.slicePageSpecApproved = false
		state.slicePrototypeProduced, state.slicePrototypeApproved = false, false
	case "prototype":
		state.slicePrototypeApproved = false
	}
	if artifactKind == "project_brief" || artifactKind == "product_requirements" || artifactKind == "requirement_baseline" || artifactKind == "blueprint" {
		state.activeFanOut = ""
		state.selectionBindingsFrozen, state.selectionPassed = false, false
		state.slicePageSpecProduced, state.slicePageSpecApproved = false, false
		state.slicePrototypeProduced, state.slicePrototypeApproved = false, false
	}
}

func isActiveFanOutKind(definition domain.WorkflowDefinition, fanOutID, itemKind string) bool {
	if strings.TrimSpace(fanOutID) == "" {
		return false
	}
	fanOut, exists := definition.FindNode(fanOutID)
	return exists && fanOut.FanOut != nil && fanOut.FanOut.ItemKind == itemKind
}

func findAITransform(capabilities []AITransformCapability, config domain.AITransformNodeConfig) (AITransformCapability, bool) {
	for _, capability := range capabilities {
		if capability.JobType == config.JobType && capability.OutputSchemaVersion == config.OutputSchemaVersion && containsString(capability.ModelPolicies, config.ModelPolicy) {
			return capability, true
		}
	}
	return AITransformCapability{}, false
}

func findManifestCompiler(capabilities []ManifestCompilerCapability, config domain.ManifestCompilerNodeConfig) (ManifestCompilerCapability, bool) {
	for _, capability := range capabilities {
		if capability.ManifestKind == config.ManifestKind && capability.SchemaVersion == config.SchemaVersion && capability.Hook == config.Hook {
			return capability, true
		}
	}
	return ManifestCompilerCapability{}, false
}

func hasCapabilityKinds(available map[string]bool, required []string) bool {
	for _, kind := range required {
		if !available[kind] {
			return false
		}
	}
	return true
}

// combineCapabilityIncomingStates applies the same execution semantics as
// runtime reconciliation. It first resolves a complete Condition assignment,
// then AND-merges every edge enabled for that assignment in one n-ary step.
// This is intentionally not a pairwise fold: pairwise folding can drop an
// unconditional predecessor when mutually exclusive edges are encountered
// later, making validation depend on edge ID ordering.
func combineCapabilityIncomingStates(groups [][]capabilityPathState, budget int) ([]capabilityPathState, error) {
	choiceMaps := make([]map[string]string, 0)
	for _, group := range groups {
		if len(group) > budget {
			return nil, fmt.Errorf("application semantic analysis exceeded its deterministic state budget")
		}
		for _, state := range group {
			choiceMaps = append(choiceMaps, state.choices)
		}
	}
	assignments, err := maximalCompatibleChoiceAssignments(choiceMaps, budget)
	if err != nil {
		return nil, fmt.Errorf("application semantic analysis exceeded its deterministic state budget")
	}
	combined := make([]capabilityPathState, 0, len(assignments))
	for _, assignment := range assignments {
		var merged *capabilityPathState
		for _, group := range groups {
			candidates := make([]capabilityPathState, 0, 1)
			for _, state := range group {
				if choiceMapMatchesAssignment(state.choices, assignment) {
					candidates = appendUniqueCapabilityState(candidates, state)
				}
			}
			if len(candidates) > 1 {
				return nil, fmt.Errorf("application semantic analysis found ambiguous states for one Condition assignment")
			}
			if len(candidates) == 0 {
				continue
			}
			if merged == nil {
				value := cloneCapabilityPathState(candidates[0])
				merged = &value
				continue
			}
			value, mergeErr := mergeCapabilityPathStates(*merged, candidates[0])
			if mergeErr != nil {
				return nil, mergeErr
			}
			merged = &value
		}
		if merged == nil {
			continue
		}
		merged.choices = cloneChoiceMap(assignment)
		combined = appendUniqueCapabilityState(combined, *merged)
		if len(combined) > budget {
			return nil, fmt.Errorf("application semantic analysis exceeded its deterministic state budget")
		}
	}
	return combined, nil
}

func mergeCapabilityPathStates(left, right capabilityPathState) (capabilityPathState, error) {
	merged := cloneCapabilityPathState(left)
	for kind, present := range right.available {
		if !present {
			continue
		}
		rightLineage := right.artifactLineage[kind]
		if merged.available[kind] {
			leftLineage := merged.artifactLineage[kind]
			if leftLineage == "" || rightLineage == "" || leftLineage != rightLineage {
				return capabilityPathState{}, fmt.Errorf("ordinary AND join has ambiguous current %s artifact lineage", kind)
			}
		} else {
			merged.available[kind] = true
			merged.artifactLineage[kind] = rightLineage
		}
		if right.approved[kind] {
			merged.approved[kind] = true
		}
	}
	if merged.activeFanOut != right.activeFanOut {
		return capabilityPathState{}, fmt.Errorf("ordinary AND join crosses incompatible active fan-out epochs")
	}
	if merged.deliveryStage != right.deliveryStage {
		return capabilityPathState{}, fmt.Errorf("ordinary AND join crosses incompatible delivery stages")
	}
	if merged.mergedFanOut != "" && right.mergedFanOut != "" && merged.mergedFanOut != right.mergedFanOut {
		return capabilityPathState{}, fmt.Errorf("ordinary AND join combines different merged fan-out epochs")
	}
	if merged.mergedFanOut == "" {
		merged.mergedFanOut = right.mergedFanOut
	}
	merged.mergedSlices = merged.mergedSlices || right.mergedSlices

	if merged.pendingReview != "" && right.pendingReview != "" {
		if merged.pendingReview != right.pendingReview || merged.pendingMaterializer != right.pendingMaterializer || merged.pendingReviewFanOut != right.pendingReviewFanOut {
			return capabilityPathState{}, fmt.Errorf("ordinary AND join has multiple current HumanEdit materializations")
		}
	} else if merged.pendingReview == "" && right.pendingReview != "" {
		merged.pendingReview = right.pendingReview
		merged.pendingMaterializer = right.pendingMaterializer
		merged.pendingReviewFanOut = right.pendingReviewFanOut
	}
	if merged.pendingReview != "" && merged.approved[merged.pendingReview] && merged.artifactLineage[merged.pendingReview] == "human:"+merged.pendingMaterializer {
		// One AND prerequisite may contain the review while a sibling carries
		// the exact same materialization. Approval of that immutable revision
		// satisfies both; it is not a review bypass.
		merged.pendingReview = ""
		merged.pendingMaterializer = ""
		merged.pendingReviewFanOut = ""
	}

	// These booleans are monotonic evidence contributed by enabled AND
	// predecessors. Condition alternatives are analyzed as separate states, so
	// an untrusted alternative cannot borrow evidence from the selected branch.
	merged.selectionBindingsFrozen = merged.selectionBindingsFrozen || right.selectionBindingsFrozen
	merged.selectionPassed = merged.selectionPassed || right.selectionPassed
	merged.slicePageSpecProduced = merged.slicePageSpecProduced || right.slicePageSpecProduced
	merged.slicePageSpecApproved = merged.slicePageSpecApproved || right.slicePageSpecApproved
	merged.slicePrototypeProduced = merged.slicePrototypeProduced || right.slicePrototypeProduced
	merged.slicePrototypeApproved = merged.slicePrototypeApproved || right.slicePrototypeApproved
	for condition, branch := range right.choices {
		merged.choices[condition] = branch
	}
	return merged, nil
}

func cloneCapabilityPathState(state capabilityPathState) capabilityPathState {
	clone := state
	clone.available, clone.approved, clone.artifactLineage, clone.choices = map[string]bool{}, map[string]bool{}, map[string]string{}, map[string]string{}
	for kind, value := range state.available {
		clone.available[kind] = value
	}
	for kind, value := range state.approved {
		clone.approved[kind] = value
	}
	for kind, lineage := range state.artifactLineage {
		clone.artifactLineage[kind] = lineage
	}
	for condition, branch := range state.choices {
		clone.choices[condition] = branch
	}
	return clone
}

func appendUniqueCapabilityState(states []capabilityPathState, candidate capabilityPathState) []capabilityPathState {
	key := capabilityStateKey(candidate)
	for _, state := range states {
		if capabilityStateKey(state) == key {
			return states
		}
	}
	return append(states, candidate)
}

func capabilityStateKey(state capabilityPathState) string {
	available, approved, lineage, choices := make([]string, 0, len(state.available)), make([]string, 0, len(state.approved)), make([]string, 0, len(state.artifactLineage)), make([]string, 0, len(state.choices))
	for kind, value := range state.available {
		if value {
			available = append(available, kind)
		}
	}
	for kind, value := range state.approved {
		if value {
			approved = append(approved, kind)
		}
	}
	for kind, producer := range state.artifactLineage {
		lineage = append(lineage, kind+"="+producer)
	}
	for condition, branch := range state.choices {
		choices = append(choices, condition+"="+branch)
	}
	sort.Strings(available)
	sort.Strings(approved)
	sort.Strings(lineage)
	sort.Strings(choices)
	return strings.Join(available, ",") + "|" + strings.Join(approved, ",") + "|" + strings.Join(lineage, ",") + "|" + state.pendingReview + "|" + state.pendingMaterializer + "|" + state.pendingReviewFanOut + "|" + state.activeFanOut + "|" + state.mergedFanOut + "|" + strings.Join(choices, ",") + "|" + fmt.Sprint(
		state.selectionBindingsFrozen, state.selectionPassed,
		state.slicePageSpecProduced, state.slicePageSpecApproved,
		state.slicePrototypeProduced, state.slicePrototypeApproved,
		state.mergedSlices, state.deliveryStage,
	)
}

func capabilityError(field, message string) error {
	return &domain.DomainError{Kind: domain.ErrValidation, Field: field, Message: message}
}

func containsInputContract(contracts []domain.WorkflowInputContract, candidate domain.WorkflowInputContract) bool {
	for _, contract := range contracts {
		if contract.Capability == candidate.Capability &&
			contract.MinimumArtifacts == candidate.MinimumArtifacts && contract.MaximumArtifacts == candidate.MaximumArtifacts && contract.RequireApproved == candidate.RequireApproved &&
			sameStrings(contract.ManifestJobTypes, candidate.ManifestJobTypes) &&
			sameStrings(contract.ArtifactKinds, candidate.ArtifactKinds) &&
			sameStrings(contract.RequiredSourcePurposes, candidate.RequiredSourcePurposes) &&
			sameStringMap(contract.ManifestSchemaContracts, candidate.ManifestSchemaContracts) {
			return true
		}
	}
	return false
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func containsOutputContract(contracts []domain.WorkflowOutputContract, candidate domain.WorkflowOutputContract) bool {
	for _, contract := range contracts {
		if contract.Capability == candidate.Capability && contract.TerminalOutcome == candidate.TerminalOutcome &&
			contract.TerminalNodeType == candidate.TerminalNodeType &&
			sameStrings(contract.ProducedArtifactKinds, candidate.ProducedArtifactKinds) {
			return true
		}
	}
	return false
}

func containsAITransform(capabilities []AITransformCapability, jobType, schema, modelPolicy string) bool {
	for _, capability := range capabilities {
		if capability.JobType == jobType && capability.OutputSchemaVersion == schema && containsString(capability.ModelPolicies, modelPolicy) {
			return true
		}
	}
	return false
}

func containsManifestCompiler(capabilities []ManifestCompilerCapability, config domain.ManifestCompilerNodeConfig) bool {
	for _, capability := range capabilities {
		if capability.ManifestKind == config.ManifestKind && capability.SchemaVersion == config.SchemaVersion && capability.Hook == config.Hook {
			return true
		}
	}
	return false
}

func containsNodeType(values []domain.WorkflowNodeType, candidate domain.WorkflowNodeType) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func containsInt(values []int, candidate int) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func sameStrings(left, right []string) bool {
	leftCopy, rightCopy := append([]string(nil), left...), append([]string(nil), right...)
	for index := range leftCopy {
		leftCopy[index] = strings.TrimSpace(leftCopy[index])
	}
	for index := range rightCopy {
		rightCopy[index] = strings.TrimSpace(rightCopy[index])
	}
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	if len(leftCopy) != len(rightCopy) {
		return false
	}
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}

func (c WorkflowCapabilities) String() string {
	return fmt.Sprintf("workflow capabilities v%d", c.Version)
}
