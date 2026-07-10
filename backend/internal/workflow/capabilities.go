package workflow

import (
	"fmt"
	"sort"
	"strings"

	"github.com/worksflow/builder/backend/internal/domain"
)

type AITransformCapability struct {
	JobType             string   `json:"jobType"`
	OutputSchemaVersion string   `json:"outputSchemaVersion"`
	ModelPolicies       []string `json:"modelPolicies"`
}

type ManifestCompilerCapability struct {
	ManifestKind  string `json:"manifestKind"`
	SchemaVersion int    `json:"schemaVersion"`
	Hook          string `json:"hook"`
}

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
	QualityGates            []string                        `json:"qualityGates"`
	PublishEnvironments     []string                        `json:"publishEnvironments"`
	WorkbenchSchemaVersions []int                           `json:"workbenchSchemaVersions"`
}

func PlatformWorkflowCapabilities(quality, publish bool) WorkflowCapabilities {
	nodeTypes := []domain.WorkflowNodeType{
		domain.NodeArtifactInput, domain.NodeAITransform, domain.NodeHumanEdit,
		domain.NodeReviewGate, domain.NodeCondition, domain.NodeFanOut,
		domain.NodeMerge, domain.NodeManifestCompiler, domain.NodeWorkbenchBuild,
		domain.NodeTransform,
	}
	capabilities := WorkflowCapabilities{
		Version:   1,
		NodeTypes: nodeTypes,
		InputContracts: []domain.WorkflowInputContract{
			ProjectBriefInputContract(), BlueprintSelectionInputContract(),
		},
		AITransforms: []AITransformCapability{
			{JobType: "refine_project_brief", OutputSchemaVersion: "project-brief-proposal/v1", ModelPolicies: []string{"project-default"}},
			{JobType: "derive_requirements", OutputSchemaVersion: "requirements-proposal/v1", ModelPolicies: []string{"project-default"}},
			{JobType: "decompose_pages", OutputSchemaVersion: "blueprint-proposal/v1", ModelPolicies: []string{"project-default"}},
			{JobType: "generate_page_spec", OutputSchemaVersion: "page-spec-proposal/v1", ModelPolicies: []string{"project-default"}},
			{JobType: "generate_prototype", OutputSchemaVersion: "prototype-proposal/v1", ModelPolicies: []string{"project-default"}},
		},
		ManifestCompilers: []ManifestCompilerCapability{{
			ManifestKind: "application_build", SchemaVersion: 1, Hook: "application-build-manifest/v1",
		}},
		Transforms:              []string{"selection_passthrough"},
		FanOutItemKinds:         []string{"generic", "delivery_slice", "blueprint_page", "blueprint_selection_page"},
		WorkbenchSchemaVersions: []int{1},
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
		ArtifactKinds:    []string{"project_brief"}, RequiredSourcePurposes: []string{"project_brief"},
		ManifestSchemaContracts: map[string]string{
			"conversation.workflow_intent": "workflow-intent-input/v1",
			"workflow_start":               "workflow-input/v1",
		},
	}
}

func BlueprintSelectionInputContract() domain.WorkflowInputContract {
	return domain.WorkflowInputContract{
		Capability: domain.WorkflowInputBlueprintSelection, ManifestJobTypes: []string{"blueprint.selection"},
		ArtifactKinds:           []string{"blueprint"},
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
	expectedTypes := make([]string, 0, len(definition.InputContract.ArtifactKinds))
	for _, kind := range definition.InputContract.ArtifactKinds {
		artifactType, ok := domain.WorkflowArtifactTypeForKind(kind)
		if !ok {
			return capabilityError("workflow.inputContract.artifactKinds", "contains an unsupported kind")
		}
		expectedTypes = append(expectedTypes, string(artifactType))
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
		case domain.NodeFanOut:
			kind := "generic"
			if node.FanOut != nil && strings.TrimSpace(node.FanOut.ItemKind) != "" {
				kind = strings.TrimSpace(node.FanOut.ItemKind)
			}
			if !containsString(c.FanOutItemKinds, kind) {
				return capabilityError("workflow.nodes."+node.ID+".fanOut.itemKind", "is not registered by this server")
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
	return nil
}

func capabilityError(field, message string) error {
	return &domain.DomainError{Kind: domain.ErrValidation, Field: field, Message: message}
}

func containsInputContract(contracts []domain.WorkflowInputContract, candidate domain.WorkflowInputContract) bool {
	for _, contract := range contracts {
		if contract.Capability == candidate.Capability &&
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
