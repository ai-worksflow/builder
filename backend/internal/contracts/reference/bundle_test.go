package reference

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/worksflow/builder/backend/internal/constructor"
	"github.com/worksflow/builder/backend/internal/contracts"
	"github.com/worksflow/builder/backend/internal/domain"
)

func TestAIConversationBundleLoadsAndCompilesDeterministically(t *testing.T) {
	t.Parallel()

	bundle, err := LoadAIConversation()
	if err != nil {
		t.Fatal(err)
	}
	input := referenceCompileInput(t, bundle)
	first, err := (constructor.Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Content.Status != constructor.StatusReady || len(first.Content.Gaps) != 0 || len(first.Content.Conflicts) != 0 {
		t.Fatalf("status = %s, gaps = %#v, conflicts = %#v", first.Content.Status, first.Content.Gaps, first.Content.Conflicts)
	}
	if len(first.Content.SourceRevisions) != 11 || len(first.Content.States) != 6 || len(first.Content.ContractBindings) != 16 ||
		len(first.Content.AcceptanceCriteria) != 15 || len(first.Content.Oracles) != 15 || len(first.Content.Obligations) != 15 {
		t.Fatalf("compiled Reference shape = sources:%d states:%d bindings:%d criteria:%d oracles:%d obligations:%d",
			len(first.Content.SourceRevisions), len(first.Content.States), len(first.Content.ContractBindings),
			len(first.Content.AcceptanceCriteria), len(first.Content.Oracles), len(first.Content.Obligations))
	}
	for _, obligation := range first.Content.Obligations {
		if obligation.Status != constructor.StatusReady || len(obligation.OracleIDs) != 1 {
			t.Fatalf("obligation = %#v", obligation)
		}
	}
	second, err := (constructor.Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContentHash != second.ContentHash || first.ContractHash != second.ContractHash {
		t.Fatalf("Reference contract is not deterministic: %#v != %#v", first, second)
	}
	const expectedHash = "675aac0656d005a2b929cb1422a81a373f253fe8d99d1dcc96d483fd0a71099a"
	if first.ContractHash != expectedHash {
		t.Fatalf("Reference contract hash = %q, want %q", first.ContractHash, expectedHash)
	}
}

func TestAIConversationProfileFailsClosed(t *testing.T) {
	t.Parallel()

	bundle, err := LoadAIConversation()
	if err != nil {
		t.Fatal(err)
	}
	base := cloneDocuments(bundle.components)
	tests := []struct {
		name string
		code string
		edit func(map[string]json.RawMessage)
	}{
		{name: "event schema missing", code: "reference_profile_document_missing", edit: func(documents map[string]json.RawMessage) {
			delete(documents, contracts.KindRunEventSchema)
		}},
		{name: "run event entity missing", code: "reference_data_entity_set", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindData, func(value map[string]any) {
				value["entities"] = removeObjectByID(value["entities"], "RunEvent")
			})
		}},
		{name: "operation security missing", code: "reference_api_security", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindAPI, func(value map[string]any) {
				paths := value["paths"].(map[string]any)
				path := paths["/api/v1/conversations"].(map[string]any)
				operation := path["get"].(map[string]any)
				delete(operation, "security")
			})
		}},
		{name: "response references a schema object", code: "api_contract_response_reference_type", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindAPI, func(value map[string]any) {
				paths := value["paths"].(map[string]any)
				operation := paths["/api/v1/conversations"].(map[string]any)["get"].(map[string]any)
				operation["responses"].(map[string]any)["401"].(map[string]any)["$ref"] = "#/components/schemas/Error"
			})
		}},
		{name: "standalone event data weakened", code: "reference_event_schema_data", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindRunEventSchema, func(value map[string]any) {
				definition := value["$defs"].(map[string]any)["outputDelta"].(map[string]any)
				body := definition["allOf"].([]any)[1].(map[string]any)
				body["properties"].(map[string]any)["data"].(map[string]any)["required"] = []any{}
			})
		}},
		{name: "API event data weakened", code: "reference_event_schema_data", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindAPI, func(value map[string]any) {
				schemas := value["components"].(map[string]any)["schemas"].(map[string]any)
				body := schemas["OutputDeltaEvent"].(map[string]any)["allOf"].([]any)[1].(map[string]any)
				body["properties"].(map[string]any)["data"].(map[string]any)["required"] = []any{}
			})
		}},
		{name: "provider request schema drift", code: "reference_provider_schema_binding", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindAIRuntime, func(value map[string]any) {
				value["providerPort"].(map[string]any)["requestSchemaRef"] = "#/components/schemas/Message"
			})
		}},
		{name: "tenant composite foreign key missing", code: "reference_data_reference", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindData, func(value map[string]any) {
				for _, raw := range value["entities"].([]any) {
					entity := raw.(map[string]any)
					if entity["id"] == "Message" {
						entity["foreignKeys"] = []any{}
					}
				}
			})
		}},
		{name: "streaming state missing", code: "reference_page_state_set", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindPageSpec, func(value map[string]any) {
				value["states"] = removeObjectByID(value["states"], "state-streaming")
			})
		}},
		{name: "prototype frame missing", code: "reference_prototype_frame_coverage", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindPrototype, func(value map[string]any) {
				value["frames"] = removeObjectByID(value["frames"], "frame-streaming-mobile")
			})
		}},
		{name: "page binding made optional", code: "reference_page_binding_required", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindPageSpec, func(value map[string]any) {
				value["dataBindings"].([]any)[0].(map[string]any)["required"] = false
			})
		}},
		{name: "state presentation evidence missing", code: "reference_prototype_state_evidence", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindPrototype, func(value map[string]any) {
				value["overrides"] = removeObjectByID(value["overrides"], "override-state-loading")
			})
		}},
		{name: "breakpoint layout evidence missing", code: "reference_prototype_breakpoint_evidence", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindPrototype, func(value map[string]any) {
				value["overrides"] = removeObjectByID(value["overrides"], "override-mobile-width")
			})
		}},
		{name: "migration disabled", code: "reference_deployment_migration", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindDeployment, func(value map[string]any) {
				value["migration"].(map[string]any)["required"] = false
			})
		}},
		{name: "undeclared provider secret", code: "reference_deployment_environment_set", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindDeployment, func(value map[string]any) {
				value["environmentVariables"] = append(value["environmentVariables"].([]any), map[string]any{"name": "GOOGLE_API_KEY", "required": true, "secret": true, "scope": "api-runtime"})
			})
		}},
		{name: "oracle missing", code: "reference_verification_oracle_set", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindVerification, func(value map[string]any) {
				value["oracles"] = removeObjectByID(value["oracles"], "verify-disconnect-recovery")
			})
		}},
		{name: "one oracle covers every acceptance criterion", code: "reference_verification_oracle_set", edit: func(documents map[string]json.RawMessage) {
			mutateObject(t, documents, contracts.KindVerification, func(value map[string]any) {
				oracles := value["oracles"].([]any)
				oracles[0].(map[string]any)["acceptanceCriterionIds"] = []any{
					"AC-AI-001", "AC-AI-002", "AC-AI-003", "AC-AI-004", "AC-AI-005",
					"AC-AI-006", "AC-AI-007", "AC-AI-008", "AC-AI-009", "AC-AI-010",
					"AC-AI-011", "AC-AI-012", "AC-AI-013", "AC-AI-014", "AC-AI-015",
				}
				value["oracles"] = oracles[:1]
			})
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			documents := cloneDocuments(base)
			test.edit(documents)
			_, findings := contracts.InspectApplicationProfile(AIConversationProfileID, documents)
			if !hasFinding(findings, test.code) {
				t.Fatalf("findings = %#v, want %s", findings, test.code)
			}
		})
	}
}

func TestReferenceCompilerRequiresStandaloneRunEventSchema(t *testing.T) {
	t.Parallel()

	bundle, err := LoadAIConversation()
	if err != nil {
		t.Fatal(err)
	}
	input := referenceCompileInput(t, bundle)
	filtered := input.Sources[:0]
	for _, source := range input.Sources {
		if source.Ref.Kind != contracts.KindRunEventSchema {
			filtered = append(filtered, source)
		}
	}
	input.Sources = filtered
	compiled, err := (constructor.Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != constructor.StatusBlocked || !hasGap(compiled.Content.Gaps, "reference_profile_document_missing") {
		t.Fatalf("status = %s, gaps = %#v", compiled.Content.Status, compiled.Content.Gaps)
	}
}

func TestReferenceRunEventSchemaAcceptsEveryTypedBranch(t *testing.T) {
	t.Parallel()

	bundle, err := LoadAIConversation()
	if err != nil {
		t.Fatal(err)
	}
	payload, exists := bundle.Component(contracts.KindRunEventSchema)
	if !exists {
		t.Fatal("RunEvent schema missing")
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	compiler.AssertFormat = true
	compiler.LoadURL = func(string) (io.ReadCloser, error) { return nil, errors.New("external refs disabled") }
	const resource = "memory://reference-run-event.json"
	if err := compiler.AddResource(resource, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		t.Fatal(err)
	}
	samples := map[string]any{
		"run.queued":    map[string]any{"queuePosition": 0},
		"run.started":   map[string]any{"modelProfileId": "reference-project-default"},
		"output.delta":  map[string]any{"text": "hello"},
		"tool.call":     map[string]any{"callId": "call-1", "name": "lookup", "arguments": map[string]any{"q": "x"}},
		"tool.result":   map[string]any{"callId": "call-1", "output": "ok"},
		"run.completed": map[string]any{"finishReason": "stop", "usage": map[string]any{"inputTokens": 4, "outputTokens": 2}},
		"run.failed":    map[string]any{"errorCode": "provider_timeout", "errorMessage": "timed out", "retryable": true},
		"run.cancelled": map[string]any{"reason": "user_requested"},
		"heartbeat":     map[string]any{"cursor": 9},
	}
	for eventType, data := range samples {
		event := map[string]any{
			"schemaVersion": "run-event/v1", "eventId": "11111111-1111-4111-8111-111111111111",
			"projectId": "22222222-2222-4222-8222-222222222222", "conversationId": "33333333-3333-4333-8333-333333333333",
			"runId": "44444444-4444-4444-8444-444444444444", "sequence": 9, "type": eventType,
			"createdAt": "2026-07-18T12:00:00Z", "data": data,
		}
		if err := schema.Validate(event); err != nil {
			t.Fatalf("%s event rejected: %v", eventType, err)
		}
	}
	invalid := map[string]any{
		"schemaVersion": "run-event/v1", "eventId": "11111111-1111-4111-8111-111111111111",
		"projectId": "22222222-2222-4222-8222-222222222222", "conversationId": "33333333-3333-4333-8333-333333333333",
		"runId": "44444444-4444-4444-8444-444444444444", "sequence": 9, "type": "output.delta",
		"createdAt": "2026-07-18T12:00:00Z", "data": map[string]any{},
	}
	if err := schema.Validate(invalid); err == nil {
		t.Fatal("invalid output.delta event was accepted")
	}
	for name, mutate := range map[string]func(map[string]any){
		"uuid":      func(value map[string]any) { value["eventId"] = "not-a-uuid" },
		"date-time": func(value map[string]any) { value["createdAt"] = "not-a-date-time" },
	} {
		candidate := map[string]any{}
		for key, value := range invalid {
			candidate[key] = value
		}
		candidate["data"] = map[string]any{"text": "valid"}
		mutate(candidate)
		if err := schema.Validate(candidate); err == nil {
			t.Fatalf("invalid %s format was accepted", name)
		}
	}
}

func TestReferenceBundleRejectsDuplicateKeysAndUnmanifestedDirectoryMembers(t *testing.T) {
	t.Parallel()

	var target map[string]any
	if err := strictDecode([]byte(`{"id":"first","id":"second"}`), &target); err == nil {
		t.Fatal("duplicate JSON object key was accepted")
	}
	bundle, err := LoadAIConversation()
	if err != nil {
		t.Fatal(err)
	}
	components := bundle.Manifest().Components
	if err := verifyDirectoryMembership(components[:len(components)-1]); err == nil {
		t.Fatal("hash-closed directory accepted a JSON member omitted from the manifest")
	}
}

func TestReferenceAcceptanceIndexCannotDriftFromBaseline(t *testing.T) {
	t.Parallel()

	bundle, err := LoadAIConversation()
	if err != nil {
		t.Fatal(err)
	}
	mutated := bundle
	mutated.components = cloneDocuments(bundle.components)
	mutateObject(t, mutated.components, "acceptance_index", func(value map[string]any) {
		value["criteria"].([]any)[0].(map[string]any)["statement"] = "drifted statement"
	})
	if err := mutated.validateAcceptanceIndex(); err == nil {
		t.Fatal("acceptance index drift from the Requirement Baseline was accepted")
	}
}

func referenceCompileInput(t *testing.T, bundle Bundle) constructor.CompileInput {
	t.Helper()
	sourceKinds := []string{
		contracts.KindRequirementBaseline, contracts.KindBlueprint, contracts.KindPageSpec, contracts.KindPrototype,
		contracts.KindAPI, contracts.KindData, contracts.KindPermission, contracts.KindAIRuntime,
		contracts.KindDeployment, contracts.KindVerification, contracts.KindRunEventSchema,
	}
	sources := make([]constructor.PinnedBuildSource, 0, len(sourceKinds))
	for _, kind := range sourceKinds {
		payload, exists := bundle.Component(kind)
		descriptor, descriptorExists := bundle.Descriptor(kind)
		if !exists || !descriptorExists {
			t.Fatalf("Reference component %s missing", kind)
		}
		contentHash, hashErr := domain.CanonicalHash(payload)
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		sources = append(sources, constructor.PinnedBuildSource{
			Ref: constructor.ExactRevisionRef{
				Kind: kind, Purpose: kind, Required: true, ArtifactID: descriptor.ArtifactID,
				RevisionID: descriptor.RevisionID, ContentHash: contentHash, ApprovalStatus: "approved",
			},
			Content: payload,
		})
	}
	manifestHash, err := domain.CanonicalHash(bundle.ManifestPayload())
	if err != nil {
		t.Fatal(err)
	}
	// These exact template refs are deliberately test-only compiler authorities;
	// the bundle never claims they are externally admitted Template Releases.
	template := constructor.FullStackTemplateRef{
		ID: "test-only-reference-full-stack", ContentHash: canonicalTestHash(t, map[string]any{"template": "reference-full-stack"}),
		Certification: "approved", PolicyStatus: "active",
	}
	releases := []constructor.TemplateReleaseRef{
		{ID: "test-only-reference-web", ReleaseHash: canonicalTestHash(t, map[string]any{"release": "web"}), Role: "web", Certification: "approved", PolicyStatus: "active"},
		{ID: "test-only-reference-api", ReleaseHash: canonicalTestHash(t, map[string]any{"release": "api"}), Role: "api", Certification: "approved", PolicyStatus: "active"},
	}
	return constructor.CompileInput{
		ProjectID:               "reference-ai-conversation-project",
		DeliverySliceID:         bundle.manifest.DeliverySliceID,
		DeliverySlicePageNodeID: bundle.manifest.DeliverySliceID,
		BuildManifest:           constructor.BuildManifestRef{ID: "reference-ai-conversation-bundle", ContentHash: manifestHash},
		Sources:                 sources, FullStackTemplate: template, TemplateReleaseRefs: releases,
		TemplateRuntime: referenceTemplateRuntime(template, releases),
	}
}

func referenceTemplateRuntime(template constructor.FullStackTemplateRef, releases []constructor.TemplateReleaseRef) *constructor.TemplateRuntimeFacts {
	releaseByRole := map[string]constructor.TemplateReleaseRef{}
	for _, release := range releases {
		releaseByRole[release.Role] = release
	}
	web, api := releaseByRole["web"], releaseByRole["api"]
	return &constructor.TemplateRuntimeFacts{
		FullStackTemplateID: template.ID, FullStackTemplateHash: template.ContentHash,
		Layout: constructor.TemplateRuntimeLayout{
			ContractTruthSource: "openapi", OpenAPIPath: "contracts/openapi.yaml",
			GeneratedClientPath: "generated/client", DeploymentPath: "deploy/stack.yaml",
			TestPath: "tests", DatabaseEngine: "postgresql",
		},
		Components: []constructor.TemplateRuntimeComponent{
			{
				Role: "web", MountPath: "apps/web", ReleaseID: web.ID, ReleaseHash: web.ReleaseHash,
				ManifestSchemaVersion: "template-manifest/v1",
				Services:              []constructor.TemplateRuntimeService{{ID: "web", Role: "web", RootPath: "."}},
				Commands:              []string{"web-build"},
				Ports:                 []constructor.TemplateRuntimePort{{Name: "web", ServiceID: "web", Number: 3000, Protocol: "http"}},
				HealthChecks:          []constructor.TemplateRuntimeHealthCheck{{ID: "web-health", ServiceID: "web", PortName: "web", Path: "/healthz"}},
				BuildOutputs:          []constructor.TemplateRuntimeBuildOutput{{ServiceID: "web", Path: ".next"}},
				EnvironmentVariables: []constructor.TemplateRuntimeEnvironmentVariable{
					{Name: "PUBLIC_API_ORIGIN", Required: true, Scope: "web-build"},
				},
			},
			{
				Role: "api", MountPath: "services/api", ReleaseID: api.ID, ReleaseHash: api.ReleaseHash,
				ManifestSchemaVersion: "template-manifest/v1",
				Services:              []constructor.TemplateRuntimeService{{ID: "api", Role: "api", RootPath: "."}},
				Commands:              []string{"api-build", "api-migrate"},
				Ports:                 []constructor.TemplateRuntimePort{{Name: "api", ServiceID: "api", Number: 8080, Protocol: "http"}},
				HealthChecks:          []constructor.TemplateRuntimeHealthCheck{{ID: "api-readiness", ServiceID: "api", PortName: "api", Path: "/readyz"}},
				Migration:             &constructor.TemplateRuntimeMigration{ServiceID: "api", CommandName: "api-migrate"},
				BuildOutputs:          []constructor.TemplateRuntimeBuildOutput{{ServiceID: "api", Path: "bin/server"}},
				EnvironmentVariables: []constructor.TemplateRuntimeEnvironmentVariable{
					{Name: "DATABASE_URL", Required: true, Secret: true, Scope: "api-runtime"},
					{Name: "SESSION_SIGNING_KEY", Required: true, Secret: true, Scope: "api-runtime"},
					{Name: "AI_GATEWAY_URL", Required: true, Scope: "api-runtime"},
					{Name: "AI_GATEWAY_CAPABILITY_FILE", Required: true, Secret: true, Scope: "api-runtime"},
					{Name: "MODEL_PROFILE_ID", Required: true, Scope: "api-runtime"},
				},
			},
		},
	}
}

func canonicalTestHash(t *testing.T, value any) string {
	t.Helper()
	hash, err := domain.CanonicalHash(value)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func cloneDocuments(values map[string]json.RawMessage) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(values))
	for kind, payload := range values {
		result[kind] = append(json.RawMessage(nil), payload...)
	}
	return result
}

func mutateObject(t *testing.T, documents map[string]json.RawMessage, kind string, mutate func(map[string]any)) {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(documents[kind], &value); err != nil {
		t.Fatal(err)
	}
	mutate(value)
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	documents[kind] = payload
}

func removeObjectByID(value any, id string) []any {
	items, _ := value.([]any)
	result := make([]any, 0, len(items))
	for _, item := range items {
		object, _ := item.(map[string]any)
		if object["id"] != id {
			result = append(result, item)
		}
	}
	return result
}

func hasFinding(findings []contracts.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func hasGap(gaps []constructor.BuildGap, code string) bool {
	for _, gap := range gaps {
		if gap.Code == code {
			return true
		}
	}
	return false
}
