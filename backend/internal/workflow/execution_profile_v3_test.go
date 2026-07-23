package workflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

func TestWorkflowExecutionProfileV3QualifiedDispatchDescriptorBytesAreFrozen(t *testing.T) {
	descriptor := WorkflowExecutionProfileV3Descriptor()
	if descriptor.Components.RunnerDispatchID != "qualified-release-controller-dispatch/v1" {
		t.Fatalf("profile v3 runner dispatch = %q", descriptor.Components.RunnerDispatchID)
	}
	canonical, err := domain.CanonicalJSON(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	if got := hex.EncodeToString(digest[:]); got != "854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104" || len(canonical) != 4160 {
		t.Fatalf("profile v3 descriptor bytes drifted: len=%d hash=%s", len(canonical), got)
	}
}

func TestWorkflowExecutionProfileV3PreservesHistoricalDescriptorBytesAndRefs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		descriptor WorkflowExecutionProfileDescriptor
		version    string
		hash       string
		byteLength int
	}{
		{name: "v0", descriptor: LegacyWorkflowExecutionProfileDescriptor(), version: "legacy-pre-pin/v0", hash: "bee729c4921a93fd2e229cd610314359ca420610c195ada00a201507bfd7a14c", byteLength: 3882},
		{name: "v1", descriptor: WorkflowExecutionProfileV1Descriptor(), version: "workflow-engine/v1", hash: "648034d2edc8f82ac2b2959b89e181b8b67db80dadbfcd354672f386d81cbdc1", byteLength: 3838},
		{name: "v2", descriptor: WorkflowExecutionProfileV2Descriptor(), version: "workflow-engine/v2", hash: "dd247a77ce3cfa1095a575a238b93c4bd41dd991eac07e8b62ec170864470da1", byteLength: 3838},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			canonical, err := domain.CanonicalJSON(test.descriptor)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(canonical)
			if got := hex.EncodeToString(digest[:]); got != test.hash || len(canonical) != test.byteLength {
				t.Fatalf("historical descriptor bytes drifted: len=%d hash=%s", len(canonical), got)
			}
			if bytes.Contains(canonical, []byte(`"externalQualificationGate"`)) {
				t.Fatal("historical descriptor bytes contain the profile-v3 capability field")
			}
			ref, err := test.descriptor.Ref()
			if err != nil {
				t.Fatal(err)
			}
			if ref != (domain.WorkflowExecutionProfileRef{Version: test.version, Hash: test.hash}) {
				t.Fatalf("historical descriptor ref drifted: %+v", ref)
			}
		})
	}
	currentBytes, err := domain.CanonicalJSON(CurrentWorkflowExecutionProfileDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	v2Bytes, err := domain.CanonicalJSON(WorkflowExecutionProfileV2Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(currentBytes, v2Bytes) || CurrentWorkflowExecutionProfileRef() != WorkflowExecutionProfileV2Ref() {
		t.Fatal("profile v3 changed the Current alias away from immutable workflow-engine/v2")
	}
}

func TestWorkflowExecutionProfileV3HasExactClosedCapabilityAndNoBuiltinRuntime(t *testing.T) {
	t.Parallel()
	descriptor := WorkflowExecutionProfileV3Descriptor()
	if descriptor.Capabilities.Version != 5 || descriptor.Capabilities.ExternalQualificationGate == nil ||
		!descriptor.Capabilities.ExternalQualificationGate.IsExact() {
		t.Fatalf("profile v3 capability declaration = %+v", descriptor.Capabilities.ExternalQualificationGate)
	}
	declaration, err := domain.CanonicalJSON(descriptor.Capabilities.ExternalQualificationGate)
	if err != nil {
		t.Fatal(err)
	}
	wantDeclaration := `{"blocking":true,"gateName":"external-qualification","inputAuthoritySchema":"worksflow-workflow-input-authority/v1","promotionProtocol":"worksflow-qualification-promotion-consume/v2","receiptSchema":"worksflow-qualification-receipt/v3","waiverPolicy":"never"}`
	if string(declaration) != wantDeclaration {
		t.Fatalf("profile v3 external qualification declaration = %s", declaration)
	}
	ref, err := descriptor.Ref()
	if err != nil {
		t.Fatal(err)
	}
	if ref != (domain.WorkflowExecutionProfileRef{Version: WorkflowExecutionProfileV3Version, Hash: "854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104"}) ||
		ref != WorkflowExecutionProfileV3Ref() {
		t.Fatalf("profile v3 ref = %+v", ref)
	}
	registry, err := NewBuiltinWorkflowExecutionProfileRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(ref); err == nil {
		t.Fatal("profile v3 was registered in the builtin runtime before its authority and handoff exist")
	}
}

func TestExternalQualificationGateConfigRejectsRunnerRetryWaiverAndOpenFields(t *testing.T) {
	t.Parallel()
	exact := `{"blocking":true,"gateName":"external-qualification","inputAuthoritySchema":"worksflow-workflow-input-authority/v1","promotionProtocol":"worksflow-qualification-promotion-consume/v2","receiptSchema":"worksflow-qualification-receipt/v3","waiverPolicy":"never"}`
	for _, extra := range []string{
		`,"runner":"qualification-runner"`,
		`,"retry":{"maxAttempts":2}`,
		`,"allowWaiver":true`,
		`,"requiredRole":"owner"`,
		`,"waiverPolicy":"never"`,
	} {
		payload := strings.TrimSuffix(exact, "}") + extra + "}"
		var config domain.ExternalQualificationGateNodeConfig
		if err := json.Unmarshal([]byte(payload), &config); err == nil {
			t.Fatalf("closed gate config accepted unsupported material: %s", payload)
		}
	}
	var exactConfig domain.ExternalQualificationGateNodeConfig
	if err := json.Unmarshal([]byte(exact), &exactConfig); err != nil || !exactConfig.IsExact() {
		t.Fatalf("exact gate config was rejected: config=%+v err=%v", exactConfig, err)
	}
}

func TestWorkflowExecutionProfileV3RequiresExactQualificationTail(t *testing.T) {
	t.Parallel()
	valid := governedProfileV3Definition(t)
	if err := ValidateDefinitionForExecutionProfile(valid, WorkflowExecutionProfileV3Ref()); err != nil {
		t.Fatalf("valid profile-v3 definition was rejected: %v", err)
	}
	if err := validateExternalQualificationTopologyV3(valid); err != nil {
		t.Fatalf("valid profile-v3 topology was rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*domain.WorkflowDefinition)
		want   string
	}{
		{name: "missing dedicated gate", want: "requires exactly", mutate: func(definition *domain.WorkflowDefinition) {
			removeProfileV3Node(definition, "external-qualification")
		}},
		{name: "duplicate dedicated gate", want: "exactly one dedicated", mutate: func(definition *domain.WorkflowDefinition) {
			node := profileV3Node(t, *definition, "external-qualification")
			node.ID = "external-qualification-duplicate"
			definition.Nodes = append(definition.Nodes, node)
		}},
		{name: "renamed dedicated gate", want: "dedicated gate id", mutate: func(definition *domain.WorkflowDefinition) {
			profileV3NodePointer(t, definition, "external-qualification").ID = "qualification-alias"
			for index := range definition.Edges {
				if definition.Edges[index].From == "external-qualification" {
					definition.Edges[index].From = "qualification-alias"
				}
				if definition.Edges[index].To == "external-qualification" {
					definition.Edges[index].To = "qualification-alias"
				}
			}
		}},
		{name: "non-blocking release", want: "blocking release", mutate: func(definition *domain.WorkflowDefinition) {
			node := profileV3NodePointer(t, definition, "quality")
			config := *node.QualityGate
			config.Blocking = false
			node.QualityGate = &config
		}},
		{name: "waiver policy drift", want: "closed non-waivable", mutate: func(definition *domain.WorkflowDefinition) {
			node := profileV3NodePointer(t, definition, "external-qualification")
			config := *node.ExternalQualificationGate
			config.WaiverPolicy = "manual"
			node.ExternalQualificationGate = &config
		}},
		{name: "preview publish", want: "production Publish", mutate: func(definition *domain.WorkflowDefinition) {
			node := profileV3NodePointer(t, definition, "publish")
			config := *node.Publish
			config.Environment = "preview"
			node.Publish = &config
		}},
		{name: "quality bypass", want: "only and directly", mutate: func(definition *domain.WorkflowDefinition) {
			definition.Edges = append(definition.Edges, domain.WorkflowEdge{ID: "quality-publish-bypass", From: "quality", To: "publish"})
		}},
		{name: "workbench branch", want: "only and directly", mutate: func(definition *domain.WorkflowDefinition) {
			definition.Edges = append(definition.Edges, domain.WorkflowEdge{ID: "workbench-external-branch", From: "workbench", To: "external-qualification"})
		}},
		{name: "quality transform", want: "only and directly", mutate: func(definition *domain.WorkflowDefinition) {
			for index := range definition.Edges {
				if definition.Edges[index].From == "quality" && definition.Edges[index].To == "external-qualification" {
					definition.Edges[index].Mapping = map[string]string{"passed": "status"}
				}
			}
		}},
		{name: "quality non-default port", want: "only and directly", mutate: func(definition *domain.WorkflowDefinition) {
			for index := range definition.Edges {
				if definition.Edges[index].From == "quality" && definition.Edges[index].To == "external-qualification" {
					definition.Edges[index].FromPort = "summary"
				}
			}
		}},
		{name: "alternative publish", want: "exactly one Publish", mutate: func(definition *domain.WorkflowDefinition) {
			node := profileV3Node(t, *definition, "publish")
			node.ID = "publish-alternative"
			definition.Nodes = append(definition.Nodes, node)
		}},
		{name: "multiple workbenches", want: "exactly one Workbench", mutate: func(definition *domain.WorkflowDefinition) {
			node := profileV3Node(t, *definition, "workbench")
			node.ID = "workbench-alternative"
			definition.Nodes = append(definition.Nodes, node)
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			definition := cloneProfileV3Definition(t, valid)
			test.mutate(&definition)
			err := validateExternalQualificationTopologyV3(definition)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("profile-v3 topology error = %v, want %q", err, test.want)
			}
		})
	}
}

func governedProfileV3Definition(t *testing.T) domain.WorkflowDefinition {
	t.Helper()
	actorID, now := uuid.NewString(), time.Now().UTC()
	seeded, err := MinimumLoopDefinition(uuid.NewString(), actorID, now)
	if err != nil {
		t.Fatal(err)
	}
	nodes := append([]domain.NodeDefinition(nil), seeded.Nodes...)
	quality := profileV3Node(t, seeded, "quality")
	externalConfig := domain.ExactExternalQualificationGateConfig()
	nodes = append(nodes, domain.NodeDefinition{
		ID: "external-qualification", Name: "External qualification", Type: domain.NodeExternalQualificationGate,
		InputSchema: quality.OutputSchema, OutputSchema: quality.OutputSchema,
		ExternalQualificationGate: &externalConfig,
	})
	edges := make([]domain.WorkflowEdge, 0, len(seeded.Edges)+1)
	for _, edge := range seeded.Edges {
		if edge.From == "quality" && edge.To == "publish" {
			edges = append(edges,
				domain.WorkflowEdge{ID: edge.ID + "-qualification", From: "quality", To: "external-qualification"},
				domain.WorkflowEdge{ID: edge.ID + "-publish", From: "external-qualification", To: "publish"},
			)
			continue
		}
		edges = append(edges, edge)
	}
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		seeded.ID, seeded.Version+1, "Governed application with external qualification", "6", nodes, edges,
		ProjectBriefInputContract(), ApplicationOutputContract(), WorkflowExecutionProfileV3Ref(), actorID, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func cloneProfileV3Definition(t *testing.T, definition domain.WorkflowDefinition) domain.WorkflowDefinition {
	t.Helper()
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	var clone domain.WorkflowDefinition
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func profileV3Node(t *testing.T, definition domain.WorkflowDefinition, id string) domain.NodeDefinition {
	t.Helper()
	node, ok := definition.FindNode(id)
	if !ok {
		t.Fatalf("profile-v3 fixture node %q is missing", id)
	}
	return node
}

func profileV3NodePointer(t *testing.T, definition *domain.WorkflowDefinition, id string) *domain.NodeDefinition {
	t.Helper()
	for index := range definition.Nodes {
		if definition.Nodes[index].ID == id {
			return &definition.Nodes[index]
		}
	}
	t.Fatalf("profile-v3 fixture node %q is missing", id)
	return nil
}

func removeProfileV3Node(definition *domain.WorkflowDefinition, id string) {
	nodes := definition.Nodes[:0]
	for _, node := range definition.Nodes {
		if node.ID != id {
			nodes = append(nodes, node)
		}
	}
	definition.Nodes = nodes
}
