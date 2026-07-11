package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

type fixedStartMetadataResolver struct{ metadata StartArtifactMetadata }

func (r *fixedStartMetadataResolver) ResolveStartArtifactKinds(context.Context, domain.InputManifest) ([]string, error) {
	return append([]string(nil), r.metadata.Kinds...), nil
}

func (r *fixedStartMetadataResolver) ResolveStartArtifactMetadata(context.Context, domain.InputManifest) (StartArtifactMetadata, error) {
	metadata := r.metadata
	metadata.Kinds = append([]string(nil), metadata.Kinds...)
	return metadata, nil
}

func governedApplicationDefinition(t *testing.T, id string, version int, actorID string, now time.Time) domain.WorkflowDefinition {
	t.Helper()
	seeded, err := MinimumLoopDefinition(id, actorID, now)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		id, version, "Governed application", "3", seeded.Nodes, seeded.Edges,
		ProjectBriefInputContract(), ApplicationOutputContract(), CurrentWorkflowExecutionProfileRef(), actorID, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func governedStartManifest(t *testing.T, projectID, actorID, jobType, schema string, purposes ...string) domain.InputManifest {
	t.Helper()
	hash, err := domain.CanonicalHash(map[string]any{"source": "immutable"})
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.ArtifactRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: hash}
	sources := make([]domain.ManifestSource, 0, len(purposes))
	for index, purpose := range purposes {
		current := ref
		if index > 0 {
			current.ArtifactID, current.RevisionID = uuid.NewString(), uuid.NewString()
		}
		sources = append(sources, domain.ManifestSource{Ref: current, Purpose: purpose})
	}
	manifest, err := domain.NewInputManifest(
		uuid.NewString(), projectID, jobType, "", &ref, sources, json.RawMessage(`{}`), schema, actorID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestCompatibleStartMatchesFullInputAndDesiredOutputContract(t *testing.T) {
	t.Parallel()
	definition := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())
	cases := []struct {
		name       string
		descriptor StartManifestDescriptor
		desired    string
		compatible bool
	}{
		{name: "direct", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-input/v1", SourcePurposes: []string{"project_brief"}, ArtifactKinds: []string{"project_brief"}, ArtifactCount: 1}, desired: "application", compatible: true},
		{name: "conversation", descriptor: StartManifestDescriptor{JobType: "conversation.workflow_intent", OutputSchemaVersion: "workflow-intent-input/v1", SourcePurposes: []string{"project_brief"}, ArtifactKinds: []string{"project_brief"}, ArtifactCount: 1}, desired: "application", compatible: true},
		{name: "cross schema", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-intent-input/v1", SourcePurposes: []string{"project_brief"}, ArtifactKinds: []string{"project_brief"}}, desired: "application"},
		{name: "missing purpose", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-input/v1", SourcePurposes: []string{"requirements"}, ArtifactKinds: []string{"project_brief"}}, desired: "application"},
		{name: "cross kind", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-input/v1", SourcePurposes: []string{"project_brief"}, ArtifactKinds: []string{"blueprint"}}, desired: "application"},
		{name: "extra kind", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-input/v1", SourcePurposes: []string{"project_brief"}, ArtifactKinds: []string{"project_brief", "blueprint"}}, desired: "application"},
		{name: "duplicate brief", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-input/v1", SourcePurposes: []string{"project_brief"}, ArtifactKinds: []string{"project_brief"}, ArtifactCount: 2}, desired: "application"},
		{name: "missing kind", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-input/v1", SourcePurposes: []string{"project_brief"}}, desired: "application"},
		{name: "wrong output", descriptor: StartManifestDescriptor{JobType: "workflow_start", OutputSchemaVersion: "workflow-input/v1", SourcePurposes: []string{"project_brief"}, ArtifactKinds: []string{"project_brief"}}, desired: "document"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := CompatibleStart(definition, test.descriptor, test.desired)
			if test.compatible && err != nil {
				t.Fatalf("compatible descriptor rejected: %v", err)
			}
			if !test.compatible && !errors.Is(err, ErrStartManifestIncompatible) {
				t.Fatalf("incompatible descriptor error = %v", err)
			}
		})
	}
}

func TestEngineStartResolvesExactKindsBeforeWritingRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	projectID, actorID := uuid.NewString(), uuid.NewString()
	store := NewMemoryStore(nil)
	definition := governedApplicationDefinition(t, uuid.NewString(), 1, actorID, time.Now().UTC())
	versionID := uuid.NewString()
	if err := store.SaveDefinition(ctx, DefinitionRecord{VersionID: versionID, ProjectID: projectID, Key: "governed-app", Title: "Governed", Published: true, Definition: definition}); err != nil {
		t.Fatal(err)
	}
	manifest := governedStartManifest(t, projectID, actorID, "workflow_start", "workflow-input/v1", "project_brief")
	if err := store.SaveManifest(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	for _, kinds := range [][]string{{}, {"blueprint"}, {"project_brief", "blueprint"}} {
		engine, err := NewEngine(store)
		if err != nil {
			t.Fatal(err)
		}
		engine.StartArtifactKinds = fixedStartArtifactKinds(kinds)
		installCompleteTestExecutionProfileRuntime(t, engine, nil)
		before := len(store.runs)
		if _, err := engine.Start(ctx, StartRequest{ProjectID: projectID, DefinitionVersionID: versionID, InputManifest: manifest.Ref(), StartedBy: actorID}); !errors.Is(err, ErrStartManifestIncompatible) && !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("kinds %v reached run creation: %v", kinds, err)
		}
		if len(store.runs) != before {
			t.Fatalf("kinds %v persisted a run before exact contract validation", kinds)
		}
	}

	unsupportedBase := governedApplicationDefinition(t, uuid.NewString(), 1, actorID, time.Now().UTC())
	unsupportedNodes := append([]domain.NodeDefinition(nil), unsupportedBase.Nodes...)
	for index := range unsupportedNodes {
		if unsupportedNodes[index].ID == "requirements-ai" {
			config := *unsupportedNodes[index].AITransform
			config.JobType, config.ModelPolicy, config.OutputSchemaVersion = "custom_transform", "default", "artifact/v1"
			unsupportedNodes[index].AITransform = &config
		}
	}
	unsupported, err := domain.NewWorkflowDefinitionWithContracts(
		unsupportedBase.ID, 1, "Unsupported", "3", unsupportedNodes, unsupportedBase.Edges,
		ProjectBriefInputContract(), ApplicationOutputContract(), actorID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	unsupported, err = unsupported.WithExecutionProfile(CurrentWorkflowExecutionProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	unsupportedVersionID := uuid.NewString()
	unsafeInstallProfileDefinition(store, DefinitionRecord{
		VersionID: unsupportedVersionID, ProjectID: projectID, Key: "unsupported-app", Title: "Unsupported",
		Published: true, ExecutionProfile: CurrentWorkflowExecutionProfileRef(), Definition: unsupported,
	})
	engine, _ := NewEngine(store)
	engine.StartArtifactKinds = fixedStartArtifactKinds{"project_brief"}
	installCompleteTestExecutionProfileRuntime(t, engine, nil)
	before := len(store.runs)
	if _, err := engine.Start(ctx, StartRequest{ProjectID: projectID, DefinitionVersionID: unsupportedVersionID, InputManifest: manifest.Ref(), StartedBy: actorID}); err == nil {
		t.Fatal("published definition with an unregistered AI capability started")
	}
	if len(store.runs) != before {
		t.Fatal("unsupported published definition persisted a run")
	}
}

func TestCapabilityRegistryRejectsUnregisteredExecutionCapabilities(t *testing.T) {
	t.Parallel()
	capabilities := PlatformWorkflowCapabilities(true, true)
	base := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())
	mutate := func(nodeID string, mutateNode func(*domain.NodeDefinition)) domain.WorkflowDefinition {
		nodes := append([]domain.NodeDefinition(nil), base.Nodes...)
		for index := range nodes {
			if nodes[index].ID == nodeID {
				mutateNode(&nodes[index])
			}
		}
		definition, err := domain.NewWorkflowDefinitionWithContracts(base.ID, 2, base.Name, base.SchemaVersion, nodes, base.Edges, *base.InputContract, *base.OutputContract, base.CreatedBy, base.CreatedAt.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return definition
	}
	tests := []struct {
		name       string
		definition domain.WorkflowDefinition
	}{
		{name: "bad-ai-job", definition: mutate("requirements-ai", func(node *domain.NodeDefinition) {
			config := *node.AITransform
			config.JobType, config.OutputSchemaVersion = "custom_transform", "artifact/v1"
			node.AITransform = &config
		})},
		{name: "bad-ai-policy", definition: mutate("requirements-ai", func(node *domain.NodeDefinition) {
			config := *node.AITransform
			config.ModelPolicy = "unregistered"
			node.AITransform = &config
		})},
		{name: "bad-compiler", definition: mutate("compile-manifest", func(node *domain.NodeDefinition) {
			config := *node.ManifestCompiler
			config.Hook = "v1"
			node.ManifestCompiler = &config
		})},
	}
	for _, test := range tests {
		if err := capabilities.ValidateDefinition(test.definition); err == nil {
			t.Fatalf("unregistered capability %s was accepted", test.name)
		}
	}
}

func TestProposalTopologyDistinguishesANDJoinsFromConditionAlternatives(t *testing.T) {
	t.Parallel()
	capabilities := PlatformWorkflowCapabilities(true, true)
	source := domain.NodeDefinition{ID: "source", Type: domain.NodeArtifactInput}
	aiNode := func(id string) domain.NodeDefinition {
		return domain.NodeDefinition{ID: id, Type: domain.NodeAITransform, AITransform: &domain.AITransformNodeConfig{
			JobType: "derive_requirements", ModelPolicy: "project-default", OutputSchemaVersion: "requirements-proposal/v1",
		}}
	}
	edit := domain.NodeDefinition{ID: "edit", Type: domain.NodeHumanEdit, HumanEdit: &domain.HumanEditNodeConfig{ArtifactKind: "product_requirements"}}
	andJoin := domain.WorkflowDefinition{Nodes: []domain.NodeDefinition{source, aiNode("ai-a"), aiNode("ai-b"), edit}, Edges: []domain.WorkflowEdge{
		{ID: "source-a", From: "source", To: "ai-a"}, {ID: "source-b", From: "source", To: "ai-b"},
		{ID: "a-edit", From: "ai-a", To: "edit"}, {ID: "b-edit", From: "ai-b", To: "edit"},
	}}
	if err := capabilities.validateProposalConsumerTopology(andJoin); err == nil {
		t.Fatal("ordinary AND join collapsed two proposal producers")
	}
	serialAI := domain.WorkflowDefinition{Nodes: []domain.NodeDefinition{source, aiNode("ai-first"), aiNode("ai-second"), edit}, Edges: []domain.WorkflowEdge{
		{ID: "serial-source-first", From: "source", To: "ai-first"},
		{ID: "serial-first-second", From: "ai-first", To: "ai-second"},
		{ID: "serial-second-edit", From: "ai-second", To: "edit"},
	}}
	if err := capabilities.validateProposalConsumerTopology(serialAI); err == nil {
		t.Fatal("second AI transform silently overwrote an unconsumed first proposal")
	}
	condition := domain.NodeDefinition{ID: "choice", Type: domain.NodeCondition, Condition: &domain.ConditionNodeConfig{Branches: []domain.ConditionBranch{{Name: "yes"}, {Name: "no"}}}}
	alternatives := domain.WorkflowDefinition{Nodes: []domain.NodeDefinition{source, condition, aiNode("ai-yes"), aiNode("ai-no"), edit}, Edges: []domain.WorkflowEdge{
		{ID: "source-choice", From: "source", To: "choice"},
		{ID: "yes-ai", From: "choice", FromPort: "yes", To: "ai-yes"}, {ID: "no-ai", From: "choice", FromPort: "no", To: "ai-no"},
		{ID: "yes-edit", From: "ai-yes", To: "edit"}, {ID: "no-edit", From: "ai-no", To: "edit"},
	}}
	if err := capabilities.validateProposalConsumerTopology(alternatives); err != nil {
		t.Fatalf("mutually exclusive single-proposal alternatives rejected: %v", err)
	}
	// An unconditional producer is enabled for every Condition assignment. In
	// the "no" branch it must therefore combine with the replacement producer
	// instead of being lost by pairwise, edge-order-dependent folding.
	permutations := [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	for _, order := range permutations {
		orderedJoin := domain.WorkflowDefinition{Nodes: []domain.NodeDefinition{
			source, aiNode("always-ai"), condition, aiNode("no-ai"), edit,
		}, Edges: []domain.WorkflowEdge{
			{ID: "ordered-source-ai", From: "source", To: "always-ai"},
			{ID: fmt.Sprintf("ordered-incoming-%d", order[0]), From: "always-ai", To: "edit"},
			{ID: "ordered-ai-choice", From: "always-ai", To: "choice"},
			{ID: fmt.Sprintf("ordered-incoming-%d", order[1]), From: "choice", FromPort: "yes", To: "edit"},
			{ID: "ordered-choice-no-ai", From: "choice", FromPort: "no", To: "no-ai"},
			{ID: fmt.Sprintf("ordered-incoming-%d", order[2]), From: "no-ai", To: "edit"},
		}}
		if err := capabilities.validateProposalConsumerTopology(orderedJoin); err == nil {
			t.Fatalf("incoming edge order %v dropped the unconditional proposal producer", order)
		}
	}
	editNode := func(id string) domain.NodeDefinition {
		return domain.NodeDefinition{ID: id, Type: domain.NodeHumanEdit, HumanEdit: &domain.HumanEditNodeConfig{ArtifactKind: "product_requirements"}}
	}
	review := domain.NodeDefinition{ID: "review", Type: domain.NodeReviewGate}
	doubleMaterialization := domain.WorkflowDefinition{Nodes: []domain.NodeDefinition{
		source, aiNode("mat-ai-a"), editNode("mat-edit-a"), aiNode("mat-ai-b"), editNode("mat-edit-b"), review,
	}, Edges: []domain.WorkflowEdge{
		{ID: "mat-source-a", From: "source", To: "mat-ai-a"}, {ID: "mat-ai-edit-a", From: "mat-ai-a", To: "mat-edit-a"},
		{ID: "mat-source-b", From: "source", To: "mat-ai-b"}, {ID: "mat-ai-edit-b", From: "mat-ai-b", To: "mat-edit-b"},
		{ID: "mat-a-review", From: "mat-edit-a", To: "review"}, {ID: "mat-b-review", From: "mat-edit-b", To: "review"},
	}}
	if err := capabilities.validateProposalConsumerTopology(doubleMaterialization); err == nil {
		t.Fatal("ReviewGate collapsed two ordinary AND-joined HumanEdit materializations")
	}
	alternativeMaterialization := domain.WorkflowDefinition{Nodes: []domain.NodeDefinition{
		source, condition, aiNode("mat-ai-yes"), editNode("mat-edit-yes"), aiNode("mat-ai-no"), editNode("mat-edit-no"), review,
	}, Edges: []domain.WorkflowEdge{
		{ID: "mat-source-choice", From: "source", To: "choice"},
		{ID: "mat-choice-yes", From: "choice", FromPort: "yes", To: "mat-ai-yes"}, {ID: "mat-yes-edit", From: "mat-ai-yes", To: "mat-edit-yes"},
		{ID: "mat-choice-no", From: "choice", FromPort: "no", To: "mat-ai-no"}, {ID: "mat-no-edit", From: "mat-ai-no", To: "mat-edit-no"},
		{ID: "mat-yes-review", From: "mat-edit-yes", To: "review"}, {ID: "mat-no-review", From: "mat-edit-no", To: "review"},
	}}
	if err := capabilities.validateProposalConsumerTopology(alternativeMaterialization); err != nil {
		t.Fatalf("mutually exclusive HumanEdit materializations were rejected: %v", err)
	}
	wrongKind := domain.WorkflowDefinition{Nodes: []domain.NodeDefinition{source, aiNode("requirements-ai"), {
		ID: "brief-edit", Type: domain.NodeHumanEdit, HumanEdit: &domain.HumanEditNodeConfig{ArtifactKind: "project_brief"},
	}}, Edges: []domain.WorkflowEdge{{ID: "source-ai", From: "source", To: "requirements-ai"}, {ID: "ai-edit", From: "requirements-ai", To: "brief-edit"}}}
	if err := capabilities.validateProposalConsumerTopology(wrongKind); err == nil {
		t.Fatal("HumanEdit accepted a proposal for a different exact artifact kind")
	}
	conditionBomb := domain.WorkflowDefinition{Nodes: []domain.NodeDefinition{source}}
	previous := "source"
	for index := 0; index < 10; index++ {
		id := fmt.Sprintf("condition-%02d", index)
		conditionBomb.Nodes = append(conditionBomb.Nodes, domain.NodeDefinition{
			ID: id, Type: domain.NodeCondition,
			Condition: &domain.ConditionNodeConfig{Branches: []domain.ConditionBranch{{Name: "yes"}, {Name: "no"}}},
		})
		if index == 0 {
			conditionBomb.Edges = append(conditionBomb.Edges, domain.WorkflowEdge{ID: "condition-entry", From: previous, To: id})
		} else {
			conditionBomb.Edges = append(conditionBomb.Edges,
				domain.WorkflowEdge{ID: id + "-yes", From: previous, FromPort: "yes", To: id},
				domain.WorkflowEdge{ID: id + "-no", From: previous, FromPort: "no", To: id},
			)
		}
		previous = id
	}
	if err := capabilities.validateProposalConsumerTopology(conditionBomb); err == nil {
		t.Fatal("exponential Condition proposal-state graph exceeded no deterministic budget")
	}
}

func TestWorkflowDefinitionRejectsEmptyFanOutRegion(t *testing.T) {
	t.Parallel()
	open := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	_, err := domain.NewWorkflowDefinition(
		uuid.NewString(), 1, "Empty fan-out", "1",
		[]domain.NodeDefinition{
			{ID: "source", Name: "Source", Type: domain.NodeArtifactInput, InputSchema: open, OutputSchema: open, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactBlueprint}, MinimumArtifacts: 1}},
			{ID: "pages", Name: "Pages", Type: domain.NodeFanOut, InputSchema: open, OutputSchema: open, FanOut: &domain.FanOutNodeConfig{ItemsPath: "/pages", SliceKeyPath: "/id", MergeNodeID: "merged", MaxParallel: 2, ItemKind: "blueprint_page"}},
			{ID: "merged", Name: "Merged", Type: domain.NodeMerge, InputSchema: open, OutputSchema: open, Merge: &domain.MergeNodeConfig{FanOutNodeID: "pages", Policy: domain.MergeAll}},
		},
		[]domain.WorkflowEdge{{ID: "source-pages", From: "source", To: "pages"}, {ID: "pages-merged", From: "pages", To: "merged"}},
		uuid.NewString(), time.Now().UTC(),
	)
	if err == nil {
		t.Fatal("direct fan-out to merge created an empty executable region")
	}
}

func TestCapabilityRegistryProvesSemanticAndDeliveryTopology(t *testing.T) {
	t.Parallel()
	capabilities := PlatformWorkflowCapabilities(true, true)
	minimum := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())
	selection, err := BlueprintSelectionFlowDefinition(uuid.NewString(), uuid.NewString(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for name, definition := range map[string]domain.WorkflowDefinition{"minimum": minimum, "selection": selection} {
		if err := capabilities.ValidateDefinition(definition); err != nil {
			t.Fatalf("registered %s flow rejected: %v", name, err)
		}
	}
	rebuild := func(nodes []domain.NodeDefinition, edges []domain.WorkflowEdge) domain.WorkflowDefinition {
		definition, err := domain.NewWorkflowDefinitionWithContracts(
			minimum.ID, minimum.Version+1, minimum.Name, minimum.SchemaVersion,
			nodes, edges, *minimum.InputContract, *minimum.OutputContract,
			minimum.CreatedBy, minimum.CreatedAt.Add(time.Minute),
		)
		if err != nil {
			t.Fatal(err)
		}
		return definition
	}
	withoutNode := func(nodeID, from, to string) domain.WorkflowDefinition {
		nodes := make([]domain.NodeDefinition, 0, len(minimum.Nodes)-1)
		for _, node := range minimum.Nodes {
			if node.ID != nodeID {
				nodes = append(nodes, node)
			}
		}
		edges := make([]domain.WorkflowEdge, 0, len(minimum.Edges)-1)
		for _, edge := range minimum.Edges {
			if edge.From != nodeID && edge.To != nodeID {
				edges = append(edges, edge)
			}
		}
		edges = append(edges, domain.WorkflowEdge{ID: "bypass-" + nodeID, From: from, To: to})
		return rebuild(nodes, edges)
	}
	strongRequirementsReview := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	for index := range strongRequirementsReview {
		if strongRequirementsReview[index].ID == "requirements-review" {
			config := *strongRequirementsReview[index].ReviewGate
			config.MinimumApprovals = 2
			strongRequirementsReview[index].ReviewGate = &config
		}
	}
	if err := capabilities.ValidateDefinition(rebuild(strongRequirementsReview, minimum.Edges)); err != nil {
		t.Fatalf("a stronger requirements-only quorum was rejected: %v", err)
	}
	wrongSemantic := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	for index := range wrongSemantic {
		if wrongSemantic[index].ID == "page-spec-ai" {
			config := *wrongSemantic[index].AITransform
			config.JobType, config.OutputSchemaVersion = "generate_prototype", "prototype-proposal/v1"
			wrongSemantic[index].AITransform = &config
		}
	}
	nonBlockingQuality := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	for index := range nonBlockingQuality {
		if nonBlockingQuality[index].ID == "quality" {
			config := *nonBlockingQuality[index].QualityGate
			config.Blocking = false
			nonBlockingQuality[index].QualityGate = &config
		}
	}
	policyMismatch := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	for index := range policyMismatch {
		if policyMismatch[index].ID == "source" {
			config := *policyMismatch[index].ArtifactInput
			config.MinimumArtifacts, config.MaximumArtifacts = 999, 999
			policyMismatch[index].ArtifactInput = &config
		}
	}
	untrustedDeliverySlice := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	missingFanOutLimit := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	oversizedFanOutLimit := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	for index := range untrustedDeliverySlice {
		if untrustedDeliverySlice[index].ID == "pages" {
			config := *untrustedDeliverySlice[index].FanOut
			config.ItemKind = "delivery_slice"
			untrustedDeliverySlice[index].FanOut = &config
		}
		if missingFanOutLimit[index].ID == "pages" {
			missingConfig, oversizedConfig := *missingFanOutLimit[index].FanOut, *oversizedFanOutLimit[index].FanOut
			missingConfig.MaxItems = 0
			oversizedConfig.MaxItems = domain.MaximumWorkflowFanOutItems + 1
			missingFanOutLimit[index].FanOut, oversizedFanOutLimit[index].FanOut = &missingConfig, &oversizedConfig
		}
	}
	mergeAny := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	mergeQuorum := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	mergeWaiver := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	reviewWaiver := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	for index := range minimum.Nodes {
		if minimum.Nodes[index].ID == "pages-merged" {
			anyConfig, quorumConfig, waiverConfig := *minimum.Nodes[index].Merge, *minimum.Nodes[index].Merge, *minimum.Nodes[index].Merge
			anyConfig.Policy = domain.MergeAny
			quorumConfig.Policy, quorumConfig.Quorum = domain.MergeQuorum, 1
			waiverConfig.AllowWaiver = true
			mergeAny[index].Merge, mergeQuorum[index].Merge, mergeWaiver[index].Merge = &anyConfig, &quorumConfig, &waiverConfig
		}
		if minimum.Nodes[index].ID == "requirements-review" {
			config := *minimum.Nodes[index].ReviewGate
			config.AllowWaiver = true
			reviewWaiver[index].ReviewGate = &config
		}
	}
	oversizedFanOutDefinition := minimum
	oversizedFanOutDefinition.Nodes = oversizedFanOutLimit
	invalid := map[string]domain.WorkflowDefinition{
		"compiler bypass":            withoutNode("compile-manifest", "pages-merged", "workbench"),
		"quality bypass":             withoutNode("quality", "workbench", "publish"),
		"semantic prerequisite":      rebuild(wrongSemantic, minimum.Edges),
		"non-blocking quality":       rebuild(nonBlockingQuality, minimum.Edges),
		"entry policy drift":         rebuild(policyMismatch, minimum.Edges),
		"untrusted delivery slice":   rebuild(untrustedDeliverySlice, minimum.Edges),
		"missing fan-out maxItems":   rebuild(missingFanOutLimit, minimum.Edges),
		"oversized fan-out maxItems": oversizedFanOutDefinition,
		"merge any":                  rebuild(mergeAny, minimum.Edges),
		"merge quorum":               rebuild(mergeQuorum, minimum.Edges),
		"merge waiver":               rebuild(mergeWaiver, minimum.Edges),
		"review waiver":              rebuild(reviewWaiver, minimum.Edges),
	}
	for name, definition := range invalid {
		if err := capabilities.ValidateDefinition(definition); err == nil {
			t.Fatalf("%s topology was accepted", name)
		}
	}
}

func TestApplicationTopologyUsesANDJoinsAndConditionAlternatives(t *testing.T) {
	t.Parallel()
	capabilities := PlatformWorkflowCapabilities(true, true)
	minimum := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())
	rebuild := func(nodes []domain.NodeDefinition, edges []domain.WorkflowEdge) domain.WorkflowDefinition {
		definition, err := domain.NewWorkflowDefinitionWithContracts(
			minimum.ID, minimum.Version+1, minimum.Name, minimum.SchemaVersion,
			nodes, edges, *minimum.InputContract, *minimum.OutputContract,
			minimum.CreatedBy, minimum.CreatedAt.Add(time.Minute),
		)
		if err != nil {
			t.Fatal(err)
		}
		return definition
	}
	cloneNode := func(sourceID, targetID string) domain.NodeDefinition {
		source, ok := minimum.FindNode(sourceID)
		if !ok {
			t.Fatalf("missing fixture node %s", sourceID)
		}
		source.ID, source.Name = targetID, source.Name+" alternative"
		return source
	}
	conditionNode := func(id string) domain.NodeDefinition {
		source, _ := minimum.FindNode("source")
		return domain.NodeDefinition{
			ID: id, Name: "Exclusive choice", Type: domain.NodeCondition,
			InputSchema: source.InputSchema,
			OutputPorts: map[string]domain.PortDefinition{
				"yes": {Schema: source.InputSchema}, "no": {Schema: source.InputSchema},
			},
			Condition: &domain.ConditionNodeConfig{Branches: []domain.ConditionBranch{
				{Name: "yes", Expression: "true"}, {Name: "no", Default: true},
			}},
		}
	}

	// The direct source edge is a complementary AND prerequisite. The main
	// branch supplies the approved slices; it is not an alternative that must
	// independently satisfy compilation.
	contextEdges := append(append([]domain.WorkflowEdge(nil), minimum.Edges...), domain.WorkflowEdge{
		ID: "parallel-context-to-compiler", From: "project-brief-review", To: "compile-manifest",
	})
	if err := capabilities.ValidateDefinition(rebuild(append([]domain.NodeDefinition(nil), minimum.Nodes...), contextEdges)); err != nil {
		t.Fatalf("ordinary complementary AND prerequisite was rejected: %v", err)
	}

	// Two independently reviewed revisions of the same semantic kind are
	// ambiguous at an ordinary join even though both branches look complete if
	// reduced to booleans.
	duplicateNodes := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	duplicateNodes = append(duplicateNodes,
		cloneNode("requirements-ai", "requirements-ai-b"),
		cloneNode("requirements-edit", "requirements-edit-b"),
		cloneNode("requirements-review", "requirements-review-b"),
	)
	duplicateEdges := append([]domain.WorkflowEdge(nil), minimum.Edges...)
	duplicateEdges = append(duplicateEdges,
		domain.WorkflowEdge{ID: "parallel-requirements-start", From: "project-brief-review", To: "requirements-ai-b"},
		domain.WorkflowEdge{ID: "parallel-requirements-ai-edit", From: "requirements-ai-b", To: "requirements-edit-b"},
		domain.WorkflowEdge{ID: "parallel-requirements-edit-review", From: "requirements-edit-b", To: "requirements-review-b"},
		domain.WorkflowEdge{ID: "parallel-requirements-join", From: "requirements-review-b", To: "blueprint-ai"},
	)
	if err := capabilities.ValidateDefinition(rebuild(duplicateNodes, duplicateEdges)); err == nil {
		t.Fatal("ordinary join collapsed two independently materialized requirements revisions")
	}

	// The same producer difference is valid when the branches are mutually
	// exclusive and retain their Condition choice through the shared tail.
	alternativeNodes := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	alternativeNodes = append(alternativeNodes,
		conditionNode("brief-choice"),
		cloneNode("project-brief-ai", "project-brief-ai-b"),
		cloneNode("project-brief-edit", "project-brief-edit-b"),
		cloneNode("project-brief-review", "project-brief-review-b"),
	)
	alternativeEdges := make([]domain.WorkflowEdge, 0, len(minimum.Edges)+6)
	for _, edge := range minimum.Edges {
		if edge.From == "source" && edge.To == "project-brief-ai" || edge.From == "project-brief-review" && edge.To == "requirements-ai" {
			continue
		}
		alternativeEdges = append(alternativeEdges, edge)
	}
	alternativeEdges = append(alternativeEdges,
		domain.WorkflowEdge{ID: "brief-choice-entry", From: "source", To: "brief-choice"},
		domain.WorkflowEdge{ID: "brief-choice-yes", From: "brief-choice", FromPort: "yes", To: "project-brief-ai"},
		domain.WorkflowEdge{ID: "brief-choice-no", From: "brief-choice", FromPort: "no", To: "project-brief-ai-b"},
		domain.WorkflowEdge{ID: "brief-b-ai-edit", From: "project-brief-ai-b", To: "project-brief-edit-b"},
		domain.WorkflowEdge{ID: "brief-b-edit-review", From: "project-brief-edit-b", To: "project-brief-review-b"},
		domain.WorkflowEdge{ID: "brief-a-tail", From: "project-brief-review", To: "requirements-ai"},
		domain.WorkflowEdge{ID: "brief-b-tail", From: "project-brief-review-b", To: "requirements-ai"},
	)
	if err := capabilities.ValidateDefinition(rebuild(alternativeNodes, alternativeEdges)); err != nil {
		t.Fatalf("mutually exclusive complete semantic alternatives were rejected: %v", err)
	}

	// A Condition alternative that jumps around the page fan-out remains an
	// independent executable state and must fail compiler prerequisites.
	bypassNodes := append(append([]domain.NodeDefinition(nil), minimum.Nodes...), conditionNode("slice-choice"))
	bypassEdges := make([]domain.WorkflowEdge, 0, len(minimum.Edges)+3)
	for _, edge := range minimum.Edges {
		if edge.From == "blueprint-review" && edge.To == "pages" {
			continue
		}
		bypassEdges = append(bypassEdges, edge)
	}
	bypassEdges = append(bypassEdges,
		domain.WorkflowEdge{ID: "slice-choice-entry", From: "blueprint-review", To: "slice-choice"},
		domain.WorkflowEdge{ID: "slice-choice-full", From: "slice-choice", FromPort: "yes", To: "pages"},
		domain.WorkflowEdge{ID: "slice-choice-bypass", From: "slice-choice", FromPort: "no", To: "compile-manifest"},
	)
	if err := capabilities.ValidateDefinition(rebuild(bypassNodes, bypassEdges)); err == nil {
		t.Fatal("Condition alternative bypassed required page materialization and merge")
	}

	// Governed ReviewGate stays directly attached to its HumanEdit so a
	// request-changes command can deterministically reopen the materializer.
	reviewNodes := append(append([]domain.NodeDefinition(nil), minimum.Nodes...), conditionNode("review-hop"))
	reviewEdges := make([]domain.WorkflowEdge, 0, len(minimum.Edges)+3)
	for _, edge := range minimum.Edges {
		if edge.From == "requirements-edit" && edge.To == "requirements-review" {
			continue
		}
		reviewEdges = append(reviewEdges, edge)
	}
	reviewEdges = append(reviewEdges,
		domain.WorkflowEdge{ID: "review-hop-entry", From: "requirements-edit", To: "review-hop"},
		domain.WorkflowEdge{ID: "review-hop-yes", From: "review-hop", FromPort: "yes", To: "requirements-review"},
		domain.WorkflowEdge{ID: "review-hop-no", From: "review-hop", FromPort: "no", To: "requirements-review"},
	)
	if err := capabilities.ValidateDefinition(rebuild(reviewNodes, reviewEdges)); err == nil {
		t.Fatal("governed review accepted an indirect HumanEdit materialization")
	}
	siblingReview := cloneNode("requirements-review", "requirements-review-sibling")
	siblingNodes := append(append([]domain.NodeDefinition(nil), minimum.Nodes...), siblingReview)
	siblingEdges := append(append([]domain.WorkflowEdge(nil), minimum.Edges...),
		domain.WorkflowEdge{ID: "requirements-edit-sibling-review", From: "requirements-edit", To: siblingReview.ID},
		domain.WorkflowEdge{ID: "requirements-sibling-review-tail", From: siblingReview.ID, To: "blueprint-ai"},
	)
	if err := capabilities.ValidateDefinition(rebuild(siblingNodes, siblingEdges)); err == nil {
		t.Fatal("one HumanEdit revision was allowed to fork into sibling ReviewGates")
	}
	sideBranchEdges := append(append([]domain.WorkflowEdge(nil), minimum.Edges...), domain.WorkflowEdge{
		ID: "requirements-edit-unreviewed-side", From: "requirements-edit", To: "blueprint-ai",
	})
	if err := capabilities.ValidateDefinition(rebuild(append([]domain.NodeDefinition(nil), minimum.Nodes...), sideBranchEdges)); err == nil {
		t.Fatal("HumanEdit was allowed to branch around its owning ReviewGate")
	}

	selection, err := BlueprintSelectionFlowDefinition(uuid.NewString(), uuid.NewString(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	selectionSource, _ := selection.FindNode("selection")
	selectionChoice := domain.NodeDefinition{
		ID: "selection-choice", Name: "Selection branch choice", Type: domain.NodeCondition,
		InputSchema: selectionSource.InputSchema,
		OutputPorts: map[string]domain.PortDefinition{
			"trusted": {Schema: selectionSource.InputSchema}, "bypass": {Schema: selectionSource.InputSchema},
		},
		Condition: &domain.ConditionNodeConfig{Branches: []domain.ConditionBranch{
			{Name: "trusted", Expression: "true"}, {Name: "bypass", Default: true},
		}},
	}
	selectionNodes := append(append([]domain.NodeDefinition(nil), selection.Nodes...), selectionChoice)
	selectionEdges := make([]domain.WorkflowEdge, 0, len(selection.Edges)+3)
	for _, edge := range selection.Edges {
		if edge.From == "pages" && edge.To == "page-ready" {
			continue
		}
		selectionEdges = append(selectionEdges, edge)
	}
	selectionEdges = append(selectionEdges,
		domain.WorkflowEdge{ID: "selection-choice-entry", From: "pages", To: "selection-choice"},
		domain.WorkflowEdge{ID: "selection-choice-trusted", From: "selection-choice", FromPort: "trusted", To: "page-ready"},
		domain.WorkflowEdge{ID: "selection-choice-bypass", From: "selection-choice", FromPort: "bypass", To: "pages-merged"},
	)
	selectionBypass, err := domain.NewWorkflowDefinitionWithContracts(
		selection.ID, selection.Version+1, selection.Name, selection.SchemaVersion,
		selectionNodes, selectionEdges,
		*selection.InputContract, *selection.OutputContract,
		selection.CreatedBy, selection.CreatedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := capabilities.ValidateDefinition(selectionBypass); err == nil {
		t.Fatal("ordinary selection branch bypassed trusted selection_passthrough")
	}
}

func TestApplicationSemanticLineageInvalidatesDownstreamAfterUpstreamRerun(t *testing.T) {
	t.Parallel()
	capabilities := PlatformWorkflowCapabilities(true, true)
	minimum := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())
	build := func(completeDownstream bool) domain.WorkflowDefinition {
		nodes := append([]domain.NodeDefinition(nil), minimum.Nodes...)
		edges := make([]domain.WorkflowEdge, 0, len(minimum.Edges)+16)
		for _, edge := range minimum.Edges {
			if edge.From == "pages-merged" && edge.To == "compile-manifest" {
				continue
			}
			edges = append(edges, edge)
		}
		cloneNode := func(sourceID, targetID string) domain.NodeDefinition {
			source, ok := minimum.FindNode(sourceID)
			if !ok {
				t.Fatalf("missing fixture node %s", sourceID)
			}
			source.ID, source.Name = targetID, source.Name+" rerun"
			if source.AITransform != nil {
				config := *source.AITransform
				source.AITransform = &config
			}
			if source.HumanEdit != nil {
				config := *source.HumanEdit
				source.HumanEdit = &config
			}
			if source.ReviewGate != nil {
				config := *source.ReviewGate
				source.ReviewGate = &config
			}
			if source.FanOut != nil {
				config := *source.FanOut
				source.FanOut = &config
			}
			if source.Merge != nil {
				config := *source.Merge
				source.Merge = &config
			}
			return source
		}
		rerunNodes := []domain.NodeDefinition{
			cloneNode("requirements-ai", "requirements-ai-rerun"),
			cloneNode("requirements-edit", "requirements-edit-rerun"),
			cloneNode("requirements-review", "requirements-review-rerun"),
		}
		pairs := [][2]string{
			{"pages-merged", "requirements-ai-rerun"},
			{"requirements-ai-rerun", "requirements-edit-rerun"},
			{"requirements-edit-rerun", "requirements-review-rerun"},
		}
		last := "requirements-review-rerun"
		if completeDownstream {
			fanOut := cloneNode("pages", "pages-rerun")
			fanOut.FanOut.MergeNodeID = "pages-merged-rerun"
			merge := cloneNode("pages-merged", "pages-merged-rerun")
			merge.Merge.FanOutNodeID = "pages-rerun"
			rerunNodes = append(rerunNodes,
				cloneNode("blueprint-ai", "blueprint-ai-rerun"),
				cloneNode("blueprint-edit", "blueprint-edit-rerun"),
				cloneNode("blueprint-review", "blueprint-review-rerun"),
				fanOut,
				cloneNode("page-spec-ai", "page-spec-ai-rerun"),
				cloneNode("page-spec-edit", "page-spec-edit-rerun"),
				cloneNode("page-spec-review", "page-spec-review-rerun"),
				cloneNode("prototype-ai", "prototype-ai-rerun"),
				cloneNode("prototype-edit", "prototype-edit-rerun"),
				cloneNode("prototype-review", "prototype-review-rerun"),
				merge,
			)
			pairs = append(pairs,
				[2]string{"requirements-review-rerun", "blueprint-ai-rerun"},
				[2]string{"blueprint-ai-rerun", "blueprint-edit-rerun"},
				[2]string{"blueprint-edit-rerun", "blueprint-review-rerun"},
				[2]string{"blueprint-review-rerun", "pages-rerun"},
				[2]string{"pages-rerun", "page-spec-ai-rerun"},
				[2]string{"page-spec-ai-rerun", "page-spec-edit-rerun"},
				[2]string{"page-spec-edit-rerun", "page-spec-review-rerun"},
				[2]string{"page-spec-review-rerun", "prototype-ai-rerun"},
				[2]string{"prototype-ai-rerun", "prototype-edit-rerun"},
				[2]string{"prototype-edit-rerun", "prototype-review-rerun"},
				[2]string{"prototype-review-rerun", "pages-merged-rerun"},
			)
			last = "pages-merged-rerun"
		}
		pairs = append(pairs, [2]string{last, "compile-manifest"})
		for index, pair := range pairs {
			edges = append(edges, domain.WorkflowEdge{ID: fmt.Sprintf("rerun-edge-%02d", index+1), From: pair[0], To: pair[1]})
		}
		nodes = append(nodes, rerunNodes...)
		definition, err := domain.NewWorkflowDefinitionWithContracts(
			minimum.ID, minimum.Version+1, minimum.Name, minimum.SchemaVersion,
			nodes, edges, *minimum.InputContract, *minimum.OutputContract,
			minimum.CreatedBy, minimum.CreatedAt.Add(time.Minute),
		)
		if err != nil {
			t.Fatal(err)
		}
		return definition
	}
	if err := capabilities.ValidateDefinition(build(false)); err == nil {
		t.Fatal("rerun requirements reused stale approved Blueprint/PageSpec/Prototype lineage")
	}
	if err := capabilities.ValidateDefinition(build(true)); err != nil {
		t.Fatalf("complete deterministic downstream rerun was rejected: %v", err)
	}
}

func TestPrototypeChangeAfterMergeCannotReuseStaleSliceMerge(t *testing.T) {
	t.Parallel()
	capabilities := PlatformWorkflowCapabilities(true, true)
	minimum := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())
	cloneNode := func(sourceID, targetID string) domain.NodeDefinition {
		source, ok := minimum.FindNode(sourceID)
		if !ok {
			t.Fatalf("missing fixture node %s", sourceID)
		}
		source.ID, source.Name = targetID, source.Name+" after merge"
		if source.AITransform != nil {
			config := *source.AITransform
			source.AITransform = &config
		}
		if source.HumanEdit != nil {
			config := *source.HumanEdit
			source.HumanEdit = &config
		}
		if source.ReviewGate != nil {
			config := *source.ReviewGate
			source.ReviewGate = &config
		}
		if source.FanOut != nil {
			config := *source.FanOut
			source.FanOut = &config
		}
		if source.Merge != nil {
			config := *source.Merge
			source.Merge = &config
		}
		return source
	}
	build := func() domain.WorkflowDefinition {
		nodes := append([]domain.NodeDefinition(nil), minimum.Nodes...)
		edges := make([]domain.WorkflowEdge, 0, len(minimum.Edges)+7)
		for _, edge := range minimum.Edges {
			if edge.From == "pages-merged" && edge.To == "compile-manifest" {
				continue
			}
			edges = append(edges, edge)
		}
		nodes = append(nodes,
			cloneNode("prototype-ai", "prototype-ai-after-merge"),
			cloneNode("prototype-edit", "prototype-edit-after-merge"),
			cloneNode("prototype-review", "prototype-review-after-merge"),
		)
		pairs := [][2]string{
			{"pages-merged", "prototype-ai-after-merge"},
			{"prototype-ai-after-merge", "prototype-edit-after-merge"},
			{"prototype-edit-after-merge", "prototype-review-after-merge"},
		}
		pairs = append(pairs, [2]string{"prototype-review-after-merge", "compile-manifest"})
		for index, pair := range pairs {
			edges = append(edges, domain.WorkflowEdge{ID: fmt.Sprintf("prototype-epoch-edge-%02d", index+1), From: pair[0], To: pair[1]})
		}
		definition, err := domain.NewWorkflowDefinitionWithContracts(
			minimum.ID, minimum.Version+1, minimum.Name, minimum.SchemaVersion,
			nodes, edges, *minimum.InputContract, *minimum.OutputContract,
			minimum.CreatedBy, minimum.CreatedAt.Add(time.Minute),
		)
		if err != nil {
			t.Fatal(err)
		}
		return definition
	}
	if err := capabilities.ValidateDefinition(build()); err == nil {
		t.Fatal("Prototype changed after merge reused the stale merged slice snapshot")
	}
}

func TestNewFanOutEpochCannotReuseGlobalPageArtifactsThroughCondition(t *testing.T) {
	t.Parallel()
	capabilities := PlatformWorkflowCapabilities(true, true)
	minimum := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())
	open := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	pages, _ := minimum.FindNode("pages")
	pages.ID, pages.Name = "pages-stale-rebind", "Attempt stale page rebind"
	pagesConfig := *pages.FanOut
	pagesConfig.MergeNodeID = "pages-stale-merged"
	pages.FanOut = &pagesConfig
	merge, _ := minimum.FindNode("pages-merged")
	merge.ID, merge.Name = "pages-stale-merged", "Merge stale pages"
	mergeConfig := *merge.Merge
	mergeConfig.FanOutNodeID = pages.ID
	merge.Merge = &mergeConfig
	condition := domain.NodeDefinition{
		ID: "stale-condition", Name: "Control-only branch", Type: domain.NodeCondition,
		InputSchema: open, OutputPorts: map[string]domain.PortDefinition{"yes": {Schema: open}, "otherwise": {Schema: open}},
		Condition: &domain.ConditionNodeConfig{Branches: []domain.ConditionBranch{
			{Name: "yes", Expression: "true"}, {Name: "otherwise", Default: true},
		}},
	}
	nodes := append(append([]domain.NodeDefinition(nil), minimum.Nodes...), pages, condition, merge)
	edges := make([]domain.WorkflowEdge, 0, len(minimum.Edges)+4)
	for _, edge := range minimum.Edges {
		if edge.From != "pages-merged" || edge.To != "compile-manifest" {
			edges = append(edges, edge)
		}
	}
	edges = append(edges,
		domain.WorkflowEdge{ID: "stale-entry", From: "pages-merged", To: pages.ID},
		domain.WorkflowEdge{ID: "stale-fan-condition", From: pages.ID, To: condition.ID},
		domain.WorkflowEdge{ID: "stale-yes", From: condition.ID, FromPort: "yes", To: merge.ID},
		domain.WorkflowEdge{ID: "stale-otherwise", From: condition.ID, FromPort: "otherwise", To: merge.ID},
		domain.WorkflowEdge{ID: "stale-compile", From: merge.ID, To: "compile-manifest"},
	)
	definition, err := domain.NewWorkflowDefinitionWithContracts(
		minimum.ID, minimum.Version+1, minimum.Name, minimum.SchemaVersion,
		nodes, edges, *minimum.InputContract, *minimum.OutputContract,
		minimum.CreatedBy, minimum.CreatedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := capabilities.ValidateDefinition(definition); err == nil {
		t.Fatal("new fan-out epoch rebound globally approved PageSpec/Prototype through a control-only branch")
	}
}

func TestBlueprintSelectionStartContractRequiresCountAndApproval(t *testing.T) {
	t.Parallel()
	definition, err := BlueprintSelectionFlowDefinition(uuid.NewString(), uuid.NewString(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	base := StartManifestDescriptor{
		JobType: "blueprint.selection", OutputSchemaVersion: "blueprint-selection/v1",
		SourcePurposes: []string{"blueprint_selection_root", "blueprint_selection_node"},
		ArtifactKinds:  []string{"blueprint"}, ArtifactCount: 2, AllArtifactsApproved: true,
	}
	if err := CompatibleStart(definition, base, domain.WorkflowOutputApplication); err != nil {
		t.Fatalf("approved selection contract rejected: %v", err)
	}
	missing := base
	missing.ArtifactCount = 0
	if err := CompatibleStart(definition, missing, domain.WorkflowOutputApplication); !errors.Is(err, ErrStartManifestIncompatible) {
		t.Fatalf("missing selection inputs = %v", err)
	}
	unapproved := base
	unapproved.AllArtifactsApproved = false
	if err := CompatibleStart(definition, unapproved, domain.WorkflowOutputApplication); !errors.Is(err, ErrStartManifestIncompatible) {
		t.Fatalf("unapproved selection inputs = %v", err)
	}
	tooMany := base
	tooMany.ArtifactCount = 102
	if err := CompatibleStart(definition, tooMany, domain.WorkflowOutputApplication); !errors.Is(err, ErrStartManifestIncompatible) {
		t.Fatalf("oversized selection inputs = %v", err)
	}
}

func TestGovernedReleaseQualityGateCannotBeWaivedAtRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	projectID, actorID := uuid.NewString(), uuid.NewString()
	definition := governedApplicationDefinition(t, uuid.NewString(), 1, actorID, time.Now().UTC())
	store := NewMemoryStore(nil)
	record := DefinitionRecord{
		VersionID: uuid.NewString(), ProjectID: projectID, Key: "governed-release", Title: "Governed release",
		Description: "Governed release", Published: true, ExecutionProfile: definition.ExecutionProfile, Definition: definition,
	}
	if err := store.SaveDefinition(ctx, record); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := &RunRecord{
		ID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: record.VersionID, Definition: definition.Ref(), ExecutionProfile: definition.ExecutionProfile,
		Status: RunFailed, Scope: json.RawMessage(`{}`), Context: NewRunContext(), StartedBy: actorID,
		StartedAt: &now, CreatedAt: now, UpdatedAt: now, Nodes: map[string]*NodeRecord{},
	}
	run.Nodes["quality"] = &NodeRecord{
		ID: uuid.NewString(), RunID: run.ID, Key: "quality", DefinitionNodeID: "quality", Type: domain.NodeQualityGate,
		Status: NodeFailed, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
	}
	run.Context.Nodes["quality"] = NodeMetadata{DefinitionNodeID: "quality"}
	if err := store.CreateRun(ctx, run, nil); err != nil {
		t.Fatal(err)
	}
	engine, _ := NewEngine(store)
	err := engine.WaiveNode(ctx, run.ID, "quality", ActorProvenance{
		ActorID: uuid.NewString(), Role: "admin", Action: "approve", Source: ActorSourceAuthenticatedCommand, AuthorizedAt: now,
	}, "skip release checks")
	if err == nil {
		t.Fatal("governed blocking release quality gate was waived")
	}
	stored, _ := store.GetRun(ctx, run.ID)
	if stored.Nodes["quality"].Status != NodeFailed || stored.Context.Nodes["quality"].Waived {
		t.Fatalf("rejected waiver mutated release state: node=%s metadata=%+v", stored.Nodes["quality"].Status, stored.Context.Nodes["quality"])
	}
}

func TestGovernedSemanticReviewCannotBypassProfileValidationAtPersistence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	projectID, actorID := uuid.NewString(), uuid.NewString()
	minimum := governedApplicationDefinition(t, uuid.NewString(), 1, actorID, time.Now().UTC())
	nodes := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	for index := range nodes {
		if nodes[index].ID == "requirements-review" {
			config := *nodes[index].ReviewGate
			config.AllowWaiver, config.MinimumApprovals = true, 2
			nodes[index].ReviewGate = &config
		}
	}
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		minimum.ID, 1, minimum.Name, minimum.SchemaVersion, nodes, minimum.Edges,
		*minimum.InputContract, *minimum.OutputContract, CurrentWorkflowExecutionProfileRef(), actorID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore(nil)
	record := DefinitionRecord{VersionID: uuid.NewString(), ProjectID: projectID, Key: "waivable-review", Title: "Waivable review", Published: true, ExecutionProfile: definition.ExecutionProfile, Definition: definition}
	if err := store.SaveDefinition(ctx, record); err == nil {
		t.Fatal("waivable governed semantic review bypassed execution-profile validation at persistence")
	}
}

func TestApplicationCompilerRejectsDownstreamContextKindsGenerationCannotConsume(t *testing.T) {
	t.Parallel()
	capabilities := PlatformWorkflowCapabilities(true, true)
	minimum := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())
	input := *minimum.InputContract
	input.ArtifactKinds = []string{"project_brief", "test_report"}
	input.MinimumArtifacts, input.MaximumArtifacts = 2, 2
	capabilities.InputContracts = append(capabilities.InputContracts, input)
	nodes := append([]domain.NodeDefinition(nil), minimum.Nodes...)
	for index := range nodes {
		if nodes[index].ID == "source" {
			config := *nodes[index].ArtifactInput
			config.AllowedKinds = append([]string(nil), input.ArtifactKinds...)
			config.AllowedTypes = []domain.ArtifactType{domain.ArtifactDocument, domain.ArtifactTest}
			config.MinimumArtifacts, config.MaximumArtifacts = input.MinimumArtifacts, input.MaximumArtifacts
			nodes[index].ArtifactInput = &config
		}
	}
	definition, err := domain.NewWorkflowDefinitionWithContracts(
		minimum.ID, minimum.Version+1, minimum.Name, minimum.SchemaVersion,
		nodes, minimum.Edges, input, *minimum.OutputContract,
		minimum.CreatedBy, minimum.CreatedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := capabilities.ValidateDefinition(definition); err == nil {
		t.Fatal("test_report context reached the application compiler even though it is a downstream output")
	}
}

func TestCompatibleDefinitionVersionsUsesTrustedManifestAndHighestPublishedVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	projectID, actorID, definitionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := NewMemoryStore(nil)
	for version := 1; version <= 3; version++ {
		definition := governedApplicationDefinition(t, definitionID, version, actorID, time.Now().UTC().Add(time.Duration(version)*time.Minute))
		if err := store.SaveDefinition(ctx, DefinitionRecord{
			VersionID: uuid.NewString(), ProjectID: projectID, Key: "governed-app", Title: "Governed",
			Published: version < 3, Definition: definition,
		}); err != nil {
			t.Fatal(err)
		}
	}
	legacySchema := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	legacy, err := domain.NewWorkflowDefinition(
		uuid.NewString(), 1, "Legacy no output", "2",
		[]domain.NodeDefinition{{ID: "source", Name: "Source", Type: domain.NodeArtifactInput, InputSchema: legacySchema, OutputSchema: legacySchema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1}}},
		nil, actorID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDefinition(ctx, DefinitionRecord{VersionID: uuid.NewString(), ProjectID: projectID, Key: "legacy-no-output", Title: "Legacy", Published: true, Definition: legacy}); err != nil {
		t.Fatal(err)
	}
	manifest := governedStartManifest(t, projectID, actorID, "conversation.workflow_intent", "workflow-intent-input/v1", "project_brief")
	if err := store.SaveManifest(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	engine, _ := NewEngine(store)
	engine.StartArtifactKinds = fixedStartArtifactKinds{"project_brief"}
	installCompleteTestExecutionProfileRuntime(t, engine, nil)
	facade := Facade{Engine: engine, Store: store, Access: fixedWorkflowAccess{}}
	versions, err := facade.CompatibleDefinitionVersions(ctx, projectID, actorID, manifest.Ref(), domain.WorkflowOutputApplication)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Definition.ID != definitionID || versions[0].Definition.Version != 2 {
		t.Fatalf("compatible versions = %+v", versions)
	}
	if versions, err := facade.CompatibleDefinitionVersions(ctx, projectID, actorID, manifest.Ref(), "document"); err != nil || len(versions) != 0 {
		t.Fatalf("wrong desired output discovery = %+v, %v", versions, err)
	}
}

func TestValidateCompatibleDefinitionVersionRevalidatesExactPublishedCandidate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	projectID, actorID := uuid.NewString(), uuid.NewString()
	store := NewMemoryStore(nil)
	definition := governedApplicationDefinition(t, uuid.NewString(), 1, actorID, time.Now().UTC())
	versionID := uuid.NewString()
	if err := store.SaveDefinition(ctx, DefinitionRecord{VersionID: versionID, ProjectID: projectID, Key: "exact-app", Title: "Exact", Published: true, Definition: definition}); err != nil {
		t.Fatal(err)
	}
	valid := governedStartManifest(t, projectID, actorID, "workflow_start", "workflow-input/v1", "project_brief")
	wrongSchema := governedStartManifest(t, projectID, actorID, "workflow_start", "workflow-intent-input/v1", "project_brief")
	for _, manifest := range []domain.InputManifest{valid, wrongSchema} {
		if err := store.SaveManifest(ctx, manifest); err != nil {
			t.Fatal(err)
		}
	}
	resolver := &fixedStartMetadataResolver{metadata: StartArtifactMetadata{Kinds: []string{"project_brief"}, Count: 1, AllApproved: true}}
	engine, _ := NewEngine(store)
	engine.StartArtifactKinds = resolver
	installCompleteTestExecutionProfileRuntime(t, engine, nil)
	facade := Facade{Engine: engine, Store: store, Access: fixedWorkflowAccess{}}
	if err := facade.ValidateCompatibleDefinitionVersion(ctx, projectID, actorID, versionID, valid.Ref(), domain.WorkflowOutputApplication); err != nil {
		t.Fatalf("valid exact candidate rejected: %v", err)
	}
	if err := facade.ValidateCompatibleDefinitionVersion(ctx, projectID, actorID, versionID, wrongSchema.Ref(), domain.WorkflowOutputApplication); err == nil {
		t.Fatal("same-job wrong-schema candidate was accepted")
	}
	resolver.metadata.Kinds = []string{"blueprint"}
	if err := facade.ValidateCompatibleDefinitionVersion(ctx, projectID, actorID, versionID, valid.Ref(), domain.WorkflowOutputApplication); err == nil {
		t.Fatal("wrong exact artifact kind was accepted")
	}
	resolver.metadata = StartArtifactMetadata{Kinds: []string{"project_brief"}, Count: 1, AllApproved: true}
	if err := facade.ValidateCompatibleDefinitionVersion(ctx, projectID, actorID, versionID, valid.Ref(), "document"); err == nil {
		t.Fatal("wrong desired output was accepted")
	}

	selection, err := BlueprintSelectionFlowDefinition(uuid.NewString(), actorID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	selection, err = domain.NewWorkflowDefinitionWithContracts(
		selection.ID, 1, selection.Name, selection.SchemaVersion, selection.Nodes, selection.Edges,
		*selection.InputContract, *selection.OutputContract, selection.CreatedBy, selection.CreatedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	selectionVersionID := uuid.NewString()
	if err := store.SaveDefinition(ctx, DefinitionRecord{VersionID: selectionVersionID, ProjectID: projectID, Key: BlueprintSelectionFlowKey, Title: "Selection", Published: true, Definition: selection}); err != nil {
		t.Fatal(err)
	}
	hash, _ := domain.CanonicalHash(map[string]any{"selection": true})
	root := domain.ArtifactRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: hash}
	anchor := root
	anchor.AnchorID = "page-home"
	selectionManifest, err := domain.NewInputManifest(
		uuid.NewString(), projectID, "blueprint.selection", uuid.NewString(), nil,
		[]domain.ManifestSource{{Ref: root, Purpose: "blueprint_selection_root"}, {Ref: anchor, Purpose: "blueprint_selection_node"}},
		json.RawMessage(`{}`), "blueprint-selection/v1", actorID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveManifest(ctx, selectionManifest); err != nil {
		t.Fatal(err)
	}
	resolver.metadata = StartArtifactMetadata{Kinds: []string{"blueprint"}, Count: 2, AllApproved: false}
	if err := facade.ValidateCompatibleDefinitionVersion(ctx, projectID, actorID, selectionVersionID, selectionManifest.Ref(), domain.WorkflowOutputApplication); err == nil {
		t.Fatal("unapproved Blueprint-selection candidate was accepted")
	}
}

func TestProductionEngineRejectsContractlessStartBeforeWritingRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	projectID, actorID := uuid.NewString(), uuid.NewString()
	open := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	legacy, err := domain.NewWorkflowDefinition(
		uuid.NewString(), 1, "Legacy", "1",
		[]domain.NodeDefinition{{ID: "source", Name: "Source", Type: domain.NodeArtifactInput, InputSchema: open, OutputSchema: open, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1}}},
		nil, actorID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore(nil)
	versionID := uuid.NewString()
	if err := store.SaveDefinition(ctx, DefinitionRecord{VersionID: versionID, ProjectID: projectID, Key: "legacy", Title: "Legacy", Published: true, Definition: legacy}); err != nil {
		t.Fatal(err)
	}
	manifest := governedStartManifest(t, projectID, actorID, "workflow_start", "workflow-input/v1", "project_brief")
	if err := store.SaveManifest(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	engine, _ := NewEngine(store)
	engine.RequireGovernedStarts = true
	if _, err := engine.Start(ctx, StartRequest{
		RunID: uuid.NewString(), ProjectID: projectID, DefinitionVersionID: versionID,
		InputManifest: manifest.Ref(), StartedBy: actorID,
	}); err == nil {
		t.Fatal("production engine started a contractless workflow")
	}
	runs, err := store.ListRuns(ctx, projectID, StoreRunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("rejected contractless start wrote %d runs", len(runs))
	}
}
