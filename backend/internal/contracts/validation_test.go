package contracts

import (
	"encoding/json"
	"testing"
)

func TestMachineContractFixturesAreAccepted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind    string
		payload string
		assert  func(*testing.T, Facts)
	}{
		{KindAPI, validAPIContract, func(t *testing.T, facts Facts) {
			if len(facts.Operations) != 1 || facts.Operations[0].ID != "listMessages" {
				t.Fatalf("operations = %#v", facts.Operations)
			}
		}},
		{KindData, validDataContract, func(t *testing.T, facts Facts) {
			if len(facts.Entities) != 1 || facts.Entities[0].ID != "Message" {
				t.Fatalf("entities = %#v", facts.Entities)
			}
		}},
		{KindPermission, validPermissionContract, func(t *testing.T, facts Facts) {
			if len(facts.Roles) != 1 || facts.Roles[0] != "message-reader" {
				t.Fatalf("roles = %#v", facts.Roles)
			}
		}},
		{KindAIRuntime, validAIRuntimeContract, func(t *testing.T, facts Facts) {
			if !facts.AIRuntime {
				t.Fatal("AI runtime fact was not set")
			}
		}},
		{KindDeployment, validDeploymentContract, func(t *testing.T, facts Facts) {
			if !facts.Deployment || len(facts.DeploymentRole) != 2 {
				t.Fatalf("deployment facts = %#v", facts)
			}
		}},
		{KindVerification, validVerificationContract, func(t *testing.T, facts Facts) {
			if len(facts.Oracles) != 1 || facts.Oracles[0].ID != "verify-messages" {
				t.Fatalf("oracles = %#v", facts.Oracles)
			}
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.kind, func(t *testing.T) {
			t.Parallel()
			facts, findings := Inspect(test.kind, json.RawMessage(test.payload))
			if len(findings) != 0 {
				t.Fatalf("unexpected findings: %#v", findings)
			}
			if facts.SchemaVersion != ExpectedSchemaVersion(test.kind) {
				t.Fatalf("schema version = %q", facts.SchemaVersion)
			}
			test.assert(t, facts)
		})
	}
}

func TestGenericDocumentCannotMasqueradeAsMachineContract(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{KindAPI, KindData, KindPermission, KindAIRuntime, KindDeployment, KindVerification} {
		_, findings := Inspect(kind, json.RawMessage(`{"blocks":[],"summary":"looks useful"}`))
		if !hasCode(findings, "contract_schema_invalid") {
			t.Fatalf("%s findings = %#v", kind, findings)
		}
	}
}

func TestGenericApplicationProfileRequiresResolvableProviderPortSchemas(t *testing.T) {
	t.Parallel()

	documents := map[string]json.RawMessage{
		KindAPI:       json.RawMessage(validAPIContract),
		KindAIRuntime: json.RawMessage(validAIRuntimeContract),
	}
	if _, findings := InspectApplicationProfile(ProfileMessages, documents); len(findings) != 0 {
		t.Fatalf("valid generic profile findings = %#v", findings)
	}
	var runtime map[string]any
	if err := json.Unmarshal(documents[KindAIRuntime], &runtime); err != nil {
		t.Fatal(err)
	}
	runtime["providerPort"].(map[string]any)["eventSchemaRef"] = "#/components/schemas/Missing"
	encoded, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	documents[KindAIRuntime] = encoded
	if _, findings := InspectApplicationProfile(ProfileMessages, documents); !hasCode(findings, "provider_port_schema_unresolved") {
		t.Fatalf("drift findings = %#v", findings)
	}
}

func TestAIRuntimeV1RemainsReadableWithoutV2Fields(t *testing.T) {
	t.Parallel()

	legacy := json.RawMessage(`{"schemaVersion":"ai-runtime-contract/v1","providerPolicy":{"policyId":"legacy","modelClass":"reasoning","fallbackAllowed":false},"conversation":{"persistence":"required","messageRoles":["user","assistant"]},"run":{"idempotencyRequired":true,"statusValues":["queued","running","completed","failed","cancelled"]},"streaming":{"transport":"sse","eventTypes":["run.started","output.delta","run.completed","run.failed"],"resumeCursor":true},"cancellation":{"supported":true,"terminalStatus":"cancelled"},"retry":{"reasonRequired":true,"maxAttempts":3,"supersedeOnModelChange":true},"limits":{"maxInputBytes":1000,"maxOutputBytes":1000,"timeoutSeconds":30},"retention":{"messageDays":30,"runDays":30,"redactionRequired":true}}`)
	facts, findings := Inspect(KindAIRuntime, legacy)
	if len(findings) != 0 {
		t.Fatalf("legacy findings = %#v", findings)
	}
	if facts.SchemaVersion != "ai-runtime-contract/v1" || facts.AIRuntimeProfile != nil {
		t.Fatalf("legacy facts = %#v", facts)
	}
}

func TestAPIAndDataV1RemainReadableWithoutV2Fields(t *testing.T) {
	t.Parallel()

	apiFacts, apiFindings := Inspect(KindAPI, json.RawMessage(`{"schemaVersion":"api-contract/v1","openapi":"3.1.0","info":{"title":"Legacy","version":"1"},"paths":{"/legacy":{"get":{"operationId":"getLegacy","responses":{"200":{"description":"ok"}}}}}}`))
	if len(apiFindings) != 0 || apiFacts.SchemaVersion != "api-contract/v1" {
		t.Fatalf("legacy API facts/findings = %#v / %#v", apiFacts, apiFindings)
	}
	dataFacts, dataFindings := Inspect(KindData, json.RawMessage(`{"schemaVersion":"data-contract/v1","entities":[{"id":"Legacy","tableName":"legacy","fields":[{"id":"id","name":"id","type":"uuid","nullable":false}],"primaryKey":["id"],"indexes":[],"tenantScope":{"mode":"global"}}],"migrationPolicy":{"tool":"goose","directory":"migrations","applyCommandId":"migrate","rollbackPolicy":"forward-only"}}`))
	if len(dataFindings) != 0 || dataFacts.SchemaVersion != "data-contract/v1" {
		t.Fatalf("legacy Data facts/findings = %#v / %#v", dataFacts, dataFindings)
	}
}

func TestMachineContractSemanticFailuresAreFailClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		kind    string
		payload string
		code    string
	}{
		{
			name: "external OpenAPI ref", kind: KindAPI, code: "api_contract_external_reference",
			payload: `{"schemaVersion":"api-contract/v1","openapi":"3.1.0","info":{"title":"API","version":"1"},"paths":{"/items":{"get":{"operationId":"getItems","responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"https://example.test/schema.json"}}}}}}}}}`,
		},
		{
			name: "response ref targets schema", kind: KindAPI, code: "api_contract_response_reference_type",
			payload: `{"schemaVersion":"api-contract/v2","openapi":"3.1.0","info":{"title":"API","version":"1"},"paths":{"/items":{"get":{"operationId":"getItems","responses":{"400":{"$ref":"#/components/schemas/Error"}}}}},"components":{"schemas":{"Error":{"type":"object"}}}}`,
		},
		{
			name: "unknown data primary key", kind: KindData, code: "data_contract_invalid_primary_key",
			payload: `{"schemaVersion":"data-contract/v1","entities":[{"id":"Item","tableName":"items","fields":[{"id":"id","name":"id","type":"uuid","nullable":false}],"primaryKey":["missing"],"indexes":[],"tenantScope":{"mode":"global"}}],"migrationPolicy":{"tool":"goose","directory":"migrations","applyCommandId":"migrate","rollbackPolicy":"forward-only"}}`,
		},
		{
			name: "tenant foreign key omits tenant", kind: KindData, code: "data_contract_foreign_key_tenant_scope",
			payload: `{"schemaVersion":"data-contract/v2","entities":[{"id":"Parent","tableName":"parents","fields":[{"id":"id","name":"id","type":"uuid","nullable":false},{"id":"projectId","name":"project_id","type":"uuid","nullable":false}],"primaryKey":["id"],"indexes":[{"id":"parentProjectId","fieldIds":["projectId","id"],"unique":true}],"foreignKeys":[],"tenantScope":{"mode":"project","fieldId":"projectId"}},{"id":"Child","tableName":"children","fields":[{"id":"id","name":"id","type":"uuid","nullable":false},{"id":"projectId","name":"project_id","type":"uuid","nullable":false},{"id":"parentId","name":"parent_id","type":"uuid","nullable":false}],"primaryKey":["id"],"indexes":[],"foreignKeys":[{"id":"childParent","fieldIds":["parentId"],"targetEntityId":"Parent","targetFieldIds":["id"],"onDelete":"restrict"}],"tenantScope":{"mode":"project","fieldId":"projectId"}}],"migrationPolicy":{"tool":"goose","directory":"migrations","applyCommandId":"migrate","rollbackPolicy":"forward-only"}}`,
		},
		{
			name: "tenant bypass", kind: KindPermission, code: "permission_contract_tenant_bypass",
			payload: `{"schemaVersion":"permission-contract/v1","identity":{"subjectClaim":"sub","authentication":"session"},"tenant":{"mode":"project","claim":"project_id"},"roles":[{"id":"reader"}],"policies":[{"id":"read","roles":["reader"],"resource":"item","actions":["read"],"tenantScoped":false,"effect":"allow"}]}`,
		},
		{
			name: "missing stream failure event", kind: KindAIRuntime, code: "ai_runtime_event_missing",
			payload: `{"schemaVersion":"ai-runtime-contract/v2","applicationProfile":"messages/v1","providerPolicy":{"policyId":"default","modelClass":"reasoning","fallbackAllowed":false,"profilePinned":true},"providerPort":{"portId":"generated-ai","protocol":"worksflow-generated-ai/v1","contractKind":"api_contract","requestSchemaRef":"#/components/schemas/ProviderGenerateRequest","eventSchemaRef":"#/components/schemas/ProviderEvent","streamingRequired":true,"cancellationPropagation":true,"usageRequired":true},"gateway":{"plane":"generated-application","endpointEnvironmentVariable":"AI_GATEWAY_URL","capabilityEnvironmentVariable":"AI_GATEWAY_CAPABILITY_FILE","capabilityMode":"file","providerCredentials":"gateway-only","providerKeyExposure":"forbidden","tenantScoped":true,"auditRequired":true},"conversation":{"persistence":"required","tenantScoped":true,"messageRoles":["user","assistant"]},"run":{"persistent":true,"idempotencyRequired":true,"statusValues":["queued","running","completed","failed","cancelled"]},"streaming":{"transport":"sse","eventTypes":["run.started","output.delta","run.completed","run.cancelled","heartbeat"],"eventSchemaRef":"run-event-schema.json","durable":true,"resumeCursor":true,"cursorField":"sequence","resumeHeader":"Last-Event-ID"},"cancellation":{"supported":true,"idempotent":true,"terminalStatus":"cancelled"},"retry":{"reasonRequired":true,"maxAttempts":3,"createsNewAttempt":true,"supersedeOnModelChange":true},"rateLimit":{"scope":"tenant-actor","requests":60,"windowSeconds":60,"burst":10,"retryAfterRequired":true,"failClosed":true},"limits":{"maxInputBytes":1000,"maxOutputBytes":1000,"timeoutSeconds":30},"retention":{"messageDays":30,"runDays":30,"eventDays":14,"redactionRequired":true},"audit":{"requiredFields":["tenantId","projectId","actorId","conversationId","runId","providerPolicyId","requestId","outcome","usage"],"promptContent":"redacted","responseContent":"redacted","retentionDays":30}}`,
		},
		{
			name: "unknown deployment port", kind: KindDeployment, code: "deployment_contract_unknown_service_port",
			payload: `{"schemaVersion":"deployment-contract/v1","services":[{"id":"web","role":"web","sourceRoot":"apps/web","portName":"missing","healthCheckId":"web-health","buildOutput":"apps/web/dist"},{"id":"api","role":"api","sourceRoot":"services/api","portName":"api","healthCheckId":"api-health","buildOutput":"services/api/bin"}],"ports":[{"name":"web","containerPort":3000,"protocol":"http"},{"name":"api","containerPort":8080,"protocol":"http"}],"environmentVariables":[],"healthChecks":[{"id":"web-health","portName":"web","method":"GET","path":"/","expectedStatuses":[200]},{"id":"api-health","portName":"api","method":"GET","path":"/health","expectedStatuses":[200]}],"migration":{"required":true,"commandId":"migrate","beforeTraffic":true},"releasePolicy":{"strategy":"rolling","rollbackRequired":true,"immutableImages":true}}`,
		},
		{
			name: "deployment health check uses another service port", kind: KindDeployment, code: "deployment_contract_service_health_port_mismatch",
			payload: `{"schemaVersion":"deployment-contract/v1","services":[{"id":"web","role":"web","sourceRoot":"apps/web","portName":"web","healthCheckId":"api-health","buildOutput":"apps/web/dist"},{"id":"api","role":"api","sourceRoot":"services/api","portName":"api","healthCheckId":"api-health","buildOutput":"services/api/bin"}],"ports":[{"name":"web","containerPort":3000,"protocol":"http"},{"name":"api","containerPort":8080,"protocol":"http"}],"environmentVariables":[],"healthChecks":[{"id":"web-health","portName":"web","method":"GET","path":"/","expectedStatuses":[200]},{"id":"api-health","portName":"api","method":"GET","path":"/health","expectedStatuses":[200]}],"migration":{"required":true,"commandId":"migrate","beforeTraffic":true},"releasePolicy":{"strategy":"rolling","rollbackRequired":true,"immutableImages":true}}`,
		},
		{
			name: "duplicate oracle", kind: KindVerification, code: "verification_contract_duplicate_oracle",
			payload: `{"schemaVersion":"verification-contract/v1","oracles":[{"id":"same","acceptanceCriterionIds":["AC-1"],"kind":"unit","target":"unit","commandId":"test","blocking":true},{"id":"same","acceptanceCriterionIds":["AC-2"],"kind":"health","target":"health","blocking":true}]}`,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, findings := Inspect(test.kind, json.RawMessage(test.payload))
			if !hasCode(findings, test.code) {
				t.Fatalf("findings = %#v, want %s", findings, test.code)
			}
		})
	}
}

func hasCode(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

const validAPIContract = `{
  "schemaVersion":"api-contract/v2","openapi":"3.1.0","info":{"title":"Messages API","version":"1.0.0"},
  "paths":{"/messages":{"get":{"operationId":"listMessages","responses":{"200":{"description":"ok"}}}}},
  "components":{"schemas":{"Message":{"type":"object","properties":{"id":{"type":"string"}}},"ProviderGenerateRequest":{"type":"object"},"ProviderEvent":{"type":"object"}},"securitySchemes":{}}
}`

const validDataContract = `{
  "schemaVersion":"data-contract/v2",
  "entities":[{"id":"Message","tableName":"messages","fields":[
    {"id":"id","name":"id","type":"uuid","nullable":false},
    {"id":"projectId","name":"project_id","type":"uuid","nullable":false}
  ],"primaryKey":["id"],"indexes":[{"id":"messageProject","fieldIds":["projectId"],"unique":false}],"foreignKeys":[],"tenantScope":{"mode":"project","fieldId":"projectId"}}],
  "migrationPolicy":{"tool":"goose","directory":"services/api/migrations","applyCommandId":"api-migrate","rollbackPolicy":"forward-only"}
}`

const validPermissionContract = `{
  "schemaVersion":"permission-contract/v1",
  "identity":{"subjectClaim":"sub","authentication":"session"},
  "tenant":{"mode":"project","claim":"project_id"},
  "roles":[{"id":"message-reader","description":"Read messages"}],
  "policies":[{"id":"read-messages","roles":["message-reader"],"resource":"messages","actions":["read"],"tenantScoped":true,"effect":"allow"}]
}`

const validAIRuntimeContract = `{
  "schemaVersion":"ai-runtime-contract/v2","applicationProfile":"messages/v1",
  "providerPolicy":{"policyId":"project-default","modelClass":"reasoning","fallbackAllowed":false,"profilePinned":true},
  "providerPort":{"portId":"generated-ai","protocol":"worksflow-generated-ai/v1","contractKind":"api_contract","requestSchemaRef":"#/components/schemas/ProviderGenerateRequest","eventSchemaRef":"#/components/schemas/ProviderEvent","streamingRequired":true,"cancellationPropagation":true,"usageRequired":true},
  "gateway":{"plane":"generated-application","endpointEnvironmentVariable":"AI_GATEWAY_URL","capabilityEnvironmentVariable":"AI_GATEWAY_CAPABILITY_FILE","capabilityMode":"file","providerCredentials":"gateway-only","providerKeyExposure":"forbidden","tenantScoped":true,"auditRequired":true},
  "conversation":{"persistence":"required","tenantScoped":true,"messageRoles":["system","user","assistant","tool"]},
  "run":{"persistent":true,"idempotencyRequired":true,"statusValues":["queued","running","completed","failed","cancelled"]},
  "streaming":{"transport":"sse","eventTypes":["run.started","output.delta","run.completed","run.failed","run.cancelled"],"eventSchemaRef":"run-event-schema.json","durable":true,"resumeCursor":true,"cursorField":"sequence","resumeHeader":"Last-Event-ID"},
  "cancellation":{"supported":true,"idempotent":true,"terminalStatus":"cancelled"},
  "retry":{"reasonRequired":true,"maxAttempts":3,"createsNewAttempt":true,"supersedeOnModelChange":true},
  "rateLimit":{"scope":"tenant-actor","requests":60,"windowSeconds":60,"burst":10,"retryAfterRequired":true,"failClosed":true},
  "limits":{"maxInputBytes":1048576,"maxOutputBytes":1048576,"timeoutSeconds":120},
  "retention":{"messageDays":30,"runDays":90,"eventDays":30,"redactionRequired":true},
  "audit":{"requiredFields":["tenantId","projectId","actorId","conversationId","runId","providerPolicyId","requestId","outcome","usage"],"promptContent":"redacted","responseContent":"redacted","retentionDays":90}
}`

const validDeploymentContract = `{
  "schemaVersion":"deployment-contract/v1",
  "services":[
    {"id":"web","role":"web","sourceRoot":"apps/web","portName":"web","healthCheckId":"web-health","buildOutput":"apps/web/dist"},
    {"id":"api","role":"api","sourceRoot":"services/api","portName":"api","healthCheckId":"api-health","buildOutput":"services/api/bin"}
  ],
  "ports":[{"name":"web","containerPort":3000,"protocol":"http"},{"name":"api","containerPort":8080,"protocol":"http"}],
  "environmentVariables":[{"name":"DATABASE_URL","required":true,"secret":true,"scope":"api-runtime"}],
  "healthChecks":[
    {"id":"web-health","portName":"web","method":"GET","path":"/","expectedStatuses":[200]},
    {"id":"api-health","portName":"api","method":"GET","path":"/health","expectedStatuses":[200]}
  ],
  "migration":{"required":true,"commandId":"api-migrate","beforeTraffic":true},
  "releasePolicy":{"strategy":"rolling","rollbackRequired":true,"immutableImages":true}
}`

const validVerificationContract = `{
  "schemaVersion":"verification-contract/v1",
  "oracles":[{"id":"verify-messages","acceptanceCriterionIds":["AC-MESSAGES"],"kind":"contract","target":"GET /messages","commandId":"test-contract","blocking":true}]
}`
