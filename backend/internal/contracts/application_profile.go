package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

const (
	ProfileGenericAIRuntime        = "generic-ai-runtime/v1"
	ProfileMessages                = "messages/v1"
	ProfileReferenceAIConversation = "reference-ai-conversation/v1"

	KindRequirementBaseline = "requirement_baseline"
	KindBlueprint           = "blueprint"
	KindPageSpec            = "page_spec"
	KindPrototype           = "prototype"
	KindRunEventSchema      = "run_event_schema"
)

type ApplicationProfileFacts struct {
	ProfileID              string   `json:"profileId"`
	Entities               []string `json:"entities"`
	Operations             []string `json:"operations"`
	StateKeys              []string `json:"stateKeys"`
	EventTypes             []string `json:"eventTypes"`
	AcceptanceCriterionIDs []string `json:"acceptanceCriterionIds"`
}

var referenceAIConversationOperations = map[string]APIOperation{
	"createConversation": {ID: "createConversation", Method: "POST", Path: "/api/v1/conversations"},
	"listConversations":  {ID: "listConversations", Method: "GET", Path: "/api/v1/conversations"},
	"getConversation":    {ID: "getConversation", Method: "GET", Path: "/api/v1/conversations/{conversationId}"},
	"listMessages":       {ID: "listMessages", Method: "GET", Path: "/api/v1/conversations/{conversationId}/messages"},
	"sendMessage":        {ID: "sendMessage", Method: "POST", Path: "/api/v1/conversations/{conversationId}/messages"},
	"listRuns":           {ID: "listRuns", Method: "GET", Path: "/api/v1/conversations/{conversationId}/runs"},
	"createRun":          {ID: "createRun", Method: "POST", Path: "/api/v1/conversations/{conversationId}/runs"},
	"getRun":             {ID: "getRun", Method: "GET", Path: "/api/v1/conversations/{conversationId}/runs/{runId}"},
	"streamRunEvents":    {ID: "streamRunEvents", Method: "GET", Path: "/api/v1/conversations/{conversationId}/runs/{runId}/events"},
	"cancelRun":          {ID: "cancelRun", Method: "POST", Path: "/api/v1/conversations/{conversationId}/runs/{runId}/cancel"},
	"retryRun":           {ID: "retryRun", Method: "POST", Path: "/api/v1/conversations/{conversationId}/runs/{runId}/retry"},
}

var referenceAIConversationEntities = []string{"Conversation", "Message", "Run", "RunEvent"}
var referenceAIConversationStates = []string{"cancelled", "empty", "error", "loading", "ready", "streaming"}
var referenceAIConversationEvents = []string{
	"heartbeat", "output.delta", "run.cancelled", "run.completed", "run.failed",
	"run.queued", "run.started", "tool.call", "tool.result",
}
var referenceAIConversationAcceptance = []string{
	"AC-AI-001", "AC-AI-002", "AC-AI-003", "AC-AI-004", "AC-AI-005",
	"AC-AI-006", "AC-AI-007", "AC-AI-008", "AC-AI-009", "AC-AI-010",
	"AC-AI-011", "AC-AI-012", "AC-AI-013", "AC-AI-014", "AC-AI-015",
}

func InspectApplicationProfile(profileID string, documents map[string]json.RawMessage) (ApplicationProfileFacts, []Finding) {
	profileID = strings.TrimSpace(profileID)
	switch profileID {
	case ProfileGenericAIRuntime, ProfileMessages:
		return inspectGenericAIRuntimeProfile(profileID, documents)
	case ProfileReferenceAIConversation:
		return inspectReferenceAIConversation(documents)
	default:
		return ApplicationProfileFacts{ProfileID: profileID}, []Finding{
			profileBlocker(KindAIRuntime, "application_profile_unsupported", "$.applicationProfile", "Application profile is not supported by this compiler."),
		}
	}
}

func inspectGenericAIRuntimeProfile(profileID string, documents map[string]json.RawMessage) (ApplicationProfileFacts, []Finding) {
	facts := ApplicationProfileFacts{ProfileID: profileID}
	apiValue, apiErr := decodeJSON(documents[KindAPI])
	runtimeValue, runtimeErr := decodeJSON(documents[KindAIRuntime])
	api, apiOK := apiValue.(map[string]any)
	runtime, runtimeOK := runtimeValue.(map[string]any)
	if apiErr != nil || runtimeErr != nil || !apiOK || !runtimeOK {
		return facts, []Finding{profileBlocker(KindAIRuntime, "provider_port_document_missing", "$", "AI runtime application profile requires exact API and AI Runtime documents.")}
	}
	providerPort, _ := runtime["providerPort"].(map[string]any)
	for _, binding := range []struct{ field, path string }{
		{"requestSchemaRef", "$.providerPort.requestSchemaRef"},
		{"eventSchemaRef", "$.providerPort.eventSchemaRef"},
	} {
		reference := stringValue(providerPort[binding.field])
		if !strings.HasPrefix(reference, "#/components/schemas/") || !localJSONPointerExists(api, reference) {
			return facts, []Finding{profileBlocker(KindAIRuntime, "provider_port_schema_unresolved", binding.path, "Provider Port schema reference must resolve to a local API Contract component schema.")}
		}
	}
	return facts, nil
}

func inspectReferenceAIConversation(documents map[string]json.RawMessage) (ApplicationProfileFacts, []Finding) {
	facts := ApplicationProfileFacts{
		ProfileID: ProfileReferenceAIConversation,
		Entities:  append([]string{}, referenceAIConversationEntities...), Operations: sortedOperationIDs(referenceAIConversationOperations),
		StateKeys: append([]string{}, referenceAIConversationStates...), EventTypes: append([]string{}, referenceAIConversationEvents...),
		AcceptanceCriterionIDs: append([]string{}, referenceAIConversationAcceptance...),
	}
	findings := []Finding{}
	required := []string{
		KindRequirementBaseline, KindBlueprint, KindPageSpec, KindPrototype, KindAPI, KindData,
		KindPermission, KindAIRuntime, KindDeployment, KindVerification, KindRunEventSchema,
	}
	for _, kind := range required {
		if len(bytes.TrimSpace(documents[kind])) == 0 {
			findings = append(findings, profileBlocker(kind, "reference_profile_document_missing", "$", "Reference AI conversation profile requires an exact "+kind+" document."))
		}
	}
	if len(findings) != 0 {
		return facts, sortedProfileFindings(findings)
	}

	leafFacts := map[string]Facts{}
	for _, kind := range []string{KindAPI, KindData, KindPermission, KindAIRuntime, KindDeployment, KindVerification} {
		inspected, leafFindings := Inspect(kind, documents[kind])
		for _, finding := range leafFindings {
			finding.SourceKind = kind
			findings = append(findings, finding)
		}
		leafFacts[kind] = inspected
	}
	if len(findings) != 0 {
		return facts, sortedProfileFindings(findings)
	}

	objects := map[string]map[string]any{}
	for _, kind := range required {
		if kind == KindRunEventSchema {
			continue
		}
		value, err := decodeJSON(documents[kind])
		object, ok := value.(map[string]any)
		if err != nil || !ok {
			findings = append(findings, profileBlocker(kind, "reference_profile_document_invalid", "$", "Reference profile document must be one JSON object."))
			continue
		}
		objects[kind] = object
	}
	if len(findings) != 0 {
		return facts, sortedProfileFindings(findings)
	}

	findings = append(findings, inspectReferenceBaseline(objects[KindRequirementBaseline])...)
	findings = append(findings, inspectReferenceData(objects[KindData], leafFacts[KindData])...)
	findings = append(findings, inspectReferenceAPI(objects[KindAPI], leafFacts[KindAPI])...)
	findings = append(findings, inspectReferencePermission(objects[KindPermission], leafFacts[KindPermission])...)
	findings = append(findings, inspectReferencePageAndPrototype(objects[KindPageSpec], objects[KindPrototype])...)
	findings = append(findings, inspectReferenceRuntime(objects[KindAIRuntime], leafFacts[KindAIRuntime])...)
	eventTypes, eventFindings := inspectReferenceEventSchema(documents[KindRunEventSchema])
	findings = append(findings, eventFindings...)
	findings = append(findings, inspectReferenceDeployment(objects[KindDeployment], objects[KindData], objects[KindAIRuntime])...)
	findings = append(findings, inspectReferenceVerification(leafFacts[KindVerification])...)

	runtimeEvents := []string{}
	if leafFacts[KindAIRuntime].AIRuntimeProfile != nil {
		runtimeEvents = leafFacts[KindAIRuntime].AIRuntimeProfile.EventTypes
	}
	apiEvents := apiRunEventTypes(objects[KindAPI])
	if !sameProfileSet(runtimeEvents, referenceAIConversationEvents) ||
		!sameProfileSet(eventTypes, referenceAIConversationEvents) ||
		!sameProfileSet(apiEvents, referenceAIConversationEvents) {
		findings = append(findings, profileBlocker(
			KindRunEventSchema, "reference_event_type_closure", "$",
			"AI Runtime, OpenAPI RunEvent, and standalone RunEvent schema must declare the exact same Reference event-type closure.",
		))
	}
	return facts, sortedProfileFindings(findings)
}

func inspectReferenceBaseline(baseline map[string]any) []Finding {
	requirements := map[string]bool{}
	criteria := []string{}
	for _, item := range objectSlice(baseline["requirements"]) {
		switch stringValue(item["type"]) {
		case "requirement":
			if strings.EqualFold(stringValue(item["priority"]), "must") {
				requirements[stringValue(item["requirementId"])] = true
			}
		case "acceptanceCriterion":
			criteria = append(criteria, stringValue(item["acceptanceCriterionId"]))
		}
	}
	if len(requirements) != 10 || !sameProfileSet(criteria, referenceAIConversationAcceptance) {
		return []Finding{profileBlocker(
			KindRequirementBaseline, "reference_acceptance_closure", "$.requirements",
			"Reference profile requires ten Must requirements and the exact fifteen stable acceptance criteria.",
		)}
	}
	return nil
}

func inspectReferenceData(data map[string]any, facts Facts) []Finding {
	findings := []Finding{}
	if facts.SchemaVersion != "data-contract/v2" {
		findings = append(findings, profileBlocker(KindData, "reference_data_version_required", "$.schemaVersion", "Reference profile requires data-contract/v2 composite relationship semantics."))
	}
	entityObjects := objectByStringField(objectSlice(data["entities"]), "id")
	entityIDs := make([]string, 0, len(facts.Entities))
	for _, entity := range facts.Entities {
		entityIDs = append(entityIDs, entity.ID)
	}
	if !sameProfileSet(entityIDs, referenceAIConversationEntities) {
		findings = append(findings, profileBlocker(KindData, "reference_data_entity_set", "$.entities", "Reference profile requires exactly Conversation, Message, Run, and RunEvent entities."))
	}
	for _, entityID := range referenceAIConversationEntities {
		entity := entityObjects[entityID]
		tenant, _ := entity["tenantScope"].(map[string]any)
		if stringValue(tenant["mode"]) != "project" || stringValue(tenant["fieldId"]) != "projectId" {
			findings = append(findings, profileBlocker(KindData, "reference_data_tenant_scope", "$.entities[id="+entityID+"].tenantScope", "Every Reference entity must be scoped by projectId."))
		}
		fields := objectByStringField(objectSlice(entity["fields"]), "id")
		for fieldID, fieldType := range map[string]string{"id": "uuid", "projectId": "uuid"} {
			if stringValue(fields[fieldID]["type"]) != fieldType || booleanValue(fields[fieldID]["nullable"]) {
				findings = append(findings, profileBlocker(KindData, "reference_data_field", "$.entities[id="+entityID+"].fields[id="+fieldID+"]", "Reference entity field is missing or has the wrong type/nullability."))
			}
		}
	}
	for _, reference := range []struct {
		entity, id, targetEntity, onDelete string
		fields, targetFields               []string
	}{
		{"Message", "messageConversation", "Conversation", "cascade", []string{"projectId", "conversationId"}, []string{"projectId", "id"}},
		{"Run", "runConversation", "Conversation", "cascade", []string{"projectId", "conversationId"}, []string{"projectId", "id"}},
		{"Run", "runTriggerMessage", "Message", "restrict", []string{"projectId", "triggerMessageId"}, []string{"projectId", "id"}},
		{"Run", "runRetryLineage", "Run", "restrict", []string{"projectId", "retryOfRunId"}, []string{"projectId", "id"}},
		{"RunEvent", "runEventConversation", "Conversation", "cascade", []string{"projectId", "conversationId"}, []string{"projectId", "id"}},
		{"RunEvent", "runEventRun", "Run", "cascade", []string{"projectId", "runId"}, []string{"projectId", "id"}},
	} {
		if !containsProfileForeignKey(entityObjects[reference.entity], reference.id, reference.fields, reference.targetEntity, reference.targetFields, reference.onDelete) {
			findings = append(findings, profileBlocker(KindData, "reference_data_reference", "$.entities[id="+reference.entity+"].foreignKeys[id="+reference.id+"]", "Reference entity relationship must use the exact project-scoped composite foreign key."))
		}
	}
	for _, index := range []struct {
		entity string
		fields []string
		unique bool
	}{
		{"Conversation", []string{"projectId", "id"}, true},
		{"Message", []string{"projectId", "id"}, true},
		{"Message", []string{"projectId", "clientMessageId"}, true},
		{"Message", []string{"projectId", "conversationId", "sequence"}, true},
		{"Run", []string{"projectId", "id"}, true},
		{"Run", []string{"projectId", "idempotencyKey"}, true},
		{"RunEvent", []string{"projectId", "runId", "sequence"}, true},
	} {
		if !containsProfileIndex(entityObjects[index.entity], index.fields, index.unique) {
			findings = append(findings, profileBlocker(KindData, "reference_data_index", "$.entities[id="+index.entity+"].indexes", "Reference persistence requires the declared idempotency or sequence index."))
		}
	}
	return findings
}

func inspectReferenceAPI(api map[string]any, facts Facts) []Finding {
	findings := []Finding{}
	if facts.SchemaVersion != "api-contract/v2" {
		findings = append(findings, profileBlocker(KindAPI, "reference_api_version_required", "$.schemaVersion", "Reference profile requires api-contract/v2 response and local-reference semantics."))
	}
	actual := make(map[string]APIOperation, len(facts.Operations))
	for _, operation := range facts.Operations {
		actual[operation.ID] = operation
	}
	if len(actual) != len(referenceAIConversationOperations) {
		findings = append(findings, profileBlocker(KindAPI, "reference_api_operation_set", "$.paths", "Reference profile API operation set is incomplete or contains undeclared operations."))
	}
	operations := apiOperationObjects(api)
	for id, expected := range referenceAIConversationOperations {
		candidate, exists := actual[id]
		if !exists || candidate.Method != expected.Method || candidate.Path != expected.Path {
			findings = append(findings, profileBlocker(KindAPI, "reference_api_operation", "$.paths[operationId="+id+"]", "Reference API operation method/path is missing or incorrect."))
			continue
		}
		if !operationUsesSessionAuth(operations[id]) {
			findings = append(findings, profileBlocker(KindAPI, "reference_api_security", "$.paths[operationId="+id+"].security", "Every Reference API operation must require SessionAuth."))
		}
	}
	for _, id := range []string{"createConversation", "sendMessage", "createRun", "cancelRun", "retryRun"} {
		if !operationRequiresHeader(operations[id], "Idempotency-Key") {
			findings = append(findings, profileBlocker(KindAPI, "reference_api_idempotency", "$.paths[operationId="+id+"].parameters", "Reference mutation must require Idempotency-Key."))
		}
	}
	components, _ := api["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	for _, name := range []string{"RunEvent", "ProviderGenerateRequest", "ProviderEvent", "RetryRunRequest"} {
		if _, exists := schemas[name].(map[string]any); !exists {
			findings = append(findings, profileBlocker(KindAPI, "reference_api_schema_missing", "$.components.schemas."+name, "Reference API component schema is required."))
		}
	}
	retry, _ := schemas["RetryRunRequest"].(map[string]any)
	retryProperties, _ := retry["properties"].(map[string]any)
	reason, _ := retryProperties["reason"].(map[string]any)
	if !containsProfileString(stringSlice(retry["required"]), "reason") || int64Value(reason["minLength"]) < 1 {
		findings = append(findings, profileBlocker(KindAPI, "reference_retry_reason_schema", "$.components.schemas.RetryRunRequest", "RetryRunRequest must require a non-empty reason."))
	}
	stream := operations["streamRunEvents"]
	responses, _ := stream["responses"].(map[string]any)
	okResponse, _ := responses["200"].(map[string]any)
	content, _ := okResponse["content"].(map[string]any)
	media, _ := content["text/event-stream"].(map[string]any)
	streamSchema, _ := media["schema"].(map[string]any)
	if stringValue(streamSchema["$ref"]) != "#/components/schemas/RunEvent" {
		findings = append(findings, profileBlocker(KindAPI, "reference_sse_contract", "$.paths[operationId=streamRunEvents].responses.200", "Run event operation must return text/event-stream using the local RunEvent schema."))
	}
	_, eventFindings := inspectReferenceAPIRunEventSchema(api)
	findings = append(findings, eventFindings...)
	return findings
}

func inspectReferencePermission(permission map[string]any, facts Facts) []Finding {
	findings := []Finding{}
	tenant, _ := permission["tenant"].(map[string]any)
	if stringValue(tenant["mode"]) != "project" || stringValue(tenant["claim"]) != "project_id" {
		findings = append(findings, profileBlocker(KindPermission, "reference_permission_tenant", "$.tenant", "Reference permission contract must use the project_id tenant claim."))
	}
	if !containsProfileString(facts.Roles, "conversation-member") {
		findings = append(findings, profileBlocker(KindPermission, "reference_permission_role", "$.roles", "Reference profile requires the conversation-member role."))
	}
	for _, policy := range facts.Policies {
		if !policy.TenantScoped {
			findings = append(findings, profileBlocker(KindPermission, "reference_permission_scope", "$.policies[id="+policy.ID+"]", "All Reference permission policies must be tenant scoped."))
		}
	}
	for resource, actions := range map[string][]string{
		"conversations": {"create", "read", "list"},
		"messages":      {"create", "read", "list"},
		"runs":          {"create", "read", "list", "cancel", "retry"},
		"run_events":    {"read", "stream", "resume"},
	} {
		if !profilePolicyCovers(facts.Policies, "conversation-member", resource, actions) {
			findings = append(findings, profileBlocker(KindPermission, "reference_permission_action", "$.policies", "Reference member policy does not cover all required "+resource+" actions."))
		}
	}
	return findings
}

func inspectReferencePageAndPrototype(page, prototype map[string]any) []Finding {
	findings := []Finding{}
	stateKeys := []string{}
	for _, state := range objectSlice(page["states"]) {
		if booleanValue(state["required"]) {
			stateKeys = append(stateKeys, stringValue(state["key"]))
		}
	}
	if !sameProfileSet(stateKeys, referenceAIConversationStates) {
		findings = append(findings, profileBlocker(KindPageSpec, "reference_page_state_set", "$.states", "Reference PageSpec requires ready, loading, empty, error, streaming, and cancelled states."))
	}
	if !sameProfileSet(stringSlice(page["acceptanceCriterionIds"]), referenceAIConversationAcceptance) {
		findings = append(findings, profileBlocker(KindPageSpec, "reference_page_acceptance", "$.acceptanceCriterionIds", "Reference PageSpec must cover every profile acceptance criterion."))
	}
	apiBindings, dataBindings, requiredBindingIDs := []string{}, []string{}, []string{}
	for _, binding := range objectSlice(page["dataBindings"]) {
		if !booleanValue(binding["required"]) {
			findings = append(findings, profileBlocker(KindPageSpec, "reference_page_binding_required", "$.dataBindings[id="+stringValue(binding["id"])+"].required", "Every Reference API and data binding must be required."))
		}
		requiredBindingIDs = append(requiredBindingIDs, stringValue(binding["id"]))
		switch stringValue(binding["source"]) {
		case "api":
			apiBindings = append(apiBindings, stringValue(binding["operationId"]))
		case "database":
			dataBindings = append(dataBindings, stringValue(binding["entityId"]))
		}
	}
	if !sameProfileSet(apiBindings, sortedOperationIDs(referenceAIConversationOperations)) || !sameProfileSet(dataBindings, referenceAIConversationEntities) {
		findings = append(findings, profileBlocker(KindPageSpec, "reference_page_binding_set", "$.dataBindings", "Reference PageSpec must bind every profile API operation and persistent entity."))
	}
	prototypeStates := []string{}
	for _, state := range objectSlice(prototype["states"]) {
		if booleanValue(state["required"]) {
			prototypeStates = append(prototypeStates, stringValue(state["key"]))
		}
	}
	breakpoints := []string{}
	for _, breakpoint := range objectSlice(prototype["breakpoints"]) {
		breakpoints = append(breakpoints, stringValue(breakpoint["id"]))
	}
	if !sameProfileSet(prototypeStates, referenceAIConversationStates) || len(breakpoints) < 3 {
		findings = append(findings, profileBlocker(KindPrototype, "reference_prototype_state_coverage", "$.states", "Reference Prototype must preserve all six states and declare at least three breakpoints."))
	}
	stateIDs := map[string]bool{}
	stateKeyByID := map[string]string{}
	for _, state := range objectSlice(page["states"]) {
		stateID := stringValue(state["id"])
		stateIDs[stateID] = true
		stateKeyByID[stateID] = stringValue(state["key"])
	}
	breakpointByID := objectByStringField(objectSlice(prototype["breakpoints"]), "id")
	coverage := map[string]bool{}
	for _, frame := range objectSlice(prototype["frames"]) {
		coverage[stringValue(frame["stateId"])+"\x00"+stringValue(frame["breakpointId"])] = true
	}
	for stateID := range stateIDs {
		for _, breakpoint := range breakpoints {
			if !coverage[stateID+"\x00"+breakpoint] {
				findings = append(findings, profileBlocker(KindPrototype, "reference_prototype_frame_coverage", "$.frames", "Every Reference state must have a frame at every declared breakpoint."))
			}
		}
	}
	statePresentation := map[string]bool{}
	breakpointWidth, breakpointHeight := map[string]bool{}, map[string]bool{}
	for _, override := range objectSlice(prototype["overrides"]) {
		stateID, breakpointID := stringValue(override["stateId"]), stringValue(override["breakpointId"])
		propertyPath := stringValue(override["propertyPath"])
		if stateID != "" && breakpointID == "" && propertyPath == "properties.stateKey" && stringValue(override["value"]) == stateKeyByID[stateID] {
			statePresentation[stateID] = true
		}
		breakpoint := breakpointByID[breakpointID]
		if breakpointID != "" && stateID == "" && propertyPath == "layout.width" && int64Value(override["value"]) == int64Value(breakpoint["viewportWidth"]) {
			breakpointWidth[breakpointID] = true
		}
		if breakpointID != "" && stateID == "" && propertyPath == "layout.height" && int64Value(override["value"]) == int64Value(breakpoint["viewportHeight"]) {
			breakpointHeight[breakpointID] = true
		}
	}
	for stateID := range stateIDs {
		if !statePresentation[stateID] {
			findings = append(findings, profileBlocker(KindPrototype, "reference_prototype_state_evidence", "$.overrides", "Every Reference state must provide an exact state-specific presentation override."))
		}
	}
	for _, breakpointID := range breakpoints {
		if !breakpointWidth[breakpointID] || !breakpointHeight[breakpointID] {
			findings = append(findings, profileBlocker(KindPrototype, "reference_prototype_breakpoint_evidence", "$.overrides", "Every Reference breakpoint must provide exact viewport width and height layout overrides."))
		}
	}
	realizedBindings := map[string]bool{}
	layers, _ := prototype["layers"].(map[string]any)
	for _, raw := range layers {
		layer, _ := raw.(map[string]any)
		if bindingID := stringValue(layer["dataBindingId"]); bindingID != "" {
			realizedBindings[bindingID] = true
		}
	}
	for _, bindingID := range requiredBindingIDs {
		if !realizedBindings[bindingID] {
			findings = append(findings, profileBlocker(KindPrototype, "reference_prototype_binding_coverage", "$.layers", "Prototype layers must realize every required PageSpec data binding."))
			break
		}
	}
	return findings
}

func inspectReferenceRuntime(runtime map[string]any, facts Facts) []Finding {
	profile := facts.AIRuntimeProfile
	if facts.SchemaVersion != "ai-runtime-contract/v2" || profile == nil || profile.ApplicationProfile != ProfileReferenceAIConversation {
		return []Finding{profileBlocker(KindAIRuntime, "ai_runtime_profile_version_required", "$.schemaVersion", "Reference profile requires ai-runtime-contract/v2 and its exact applicationProfile.")}
	}
	providerPort, _ := runtime["providerPort"].(map[string]any)
	gateway, _ := runtime["gateway"].(map[string]any)
	streaming, _ := runtime["streaming"].(map[string]any)
	if stringValue(providerPort["contractKind"]) != KindAPI || stringValue(gateway["plane"]) != "generated-application" {
		return []Finding{profileBlocker(KindAIRuntime, "reference_gateway_plane", "$.gateway", "Reference model port must use the generated-application gateway plane and API contract schemas.")}
	}
	if profile.ProviderRequestSchemaRef != "#/components/schemas/ProviderGenerateRequest" ||
		profile.ProviderEventSchemaRef != "#/components/schemas/ProviderEvent" ||
		stringValue(streaming["eventSchemaRef"]) != "run-event-schema.json" {
		return []Finding{profileBlocker(KindAIRuntime, "reference_provider_schema_binding", "$.providerPort", "Reference Provider Port and stream must pin the exact bundled API and RunEvent schemas.")}
	}
	return nil
}

func inspectReferenceEventSchema(payload json.RawMessage) ([]string, []Finding) {
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	compiler.AssertFormat = true
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external JSON Schema references are disabled")
	}
	const resource = "memory://reference-run-event-schema.json"
	if err := compiler.AddResource(resource, bytes.NewReader(payload)); err != nil {
		return nil, []Finding{profileBlocker(KindRunEventSchema, "reference_event_schema_invalid", "$", err.Error())}
	}
	if _, err := compiler.Compile(resource); err != nil {
		return nil, []Finding{profileBlocker(KindRunEventSchema, "reference_event_schema_invalid", "$", err.Error())}
	}
	value, err := decodeJSON(payload)
	root, ok := value.(map[string]any)
	if err != nil || !ok {
		return nil, []Finding{profileBlocker(KindRunEventSchema, "reference_event_schema_invalid", "$", "RunEvent schema must be one JSON object.")}
	}
	definitions, _ := root["$defs"].(map[string]any)
	return inspectReferenceRunEventShape(root, definitions, "#/$defs/", "#/$defs/usage", KindRunEventSchema)
}

func inspectReferenceAPIRunEventSchema(api map[string]any) ([]string, []Finding) {
	components, _ := api["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	runEvent, _ := schemas["RunEvent"].(map[string]any)
	if runEvent == nil {
		return nil, []Finding{profileBlocker(KindAPI, "reference_event_schema_shape", "$.components.schemas.RunEvent", "Reference API must expose the exact typed RunEvent union.")}
	}
	return inspectReferenceRunEventShape(runEvent, schemas, "#/components/schemas/", "#/components/schemas/RunEventUsage", KindAPI)
}

func inspectReferenceRunEventShape(root, definitions map[string]any, referencePrefix, usageReference, sourceKind string) ([]string, []Finding) {
	findings := []Finding{}
	types := []string{}
	branches := objectSlice(root["oneOf"])
	if len(branches) != len(referenceAIConversationEvents) {
		findings = append(findings, profileBlocker(sourceKind, "reference_event_schema_shape", "$.oneOf", "Reference RunEvent must contain exactly nine typed branches."))
	}
	commonValidated := map[string]bool{}
	for index, branch := range branches {
		branchReference := stringValue(branch["$ref"])
		branchName := strings.TrimPrefix(branchReference, referencePrefix)
		definition, exists := definitions[branchName].(map[string]any)
		if !strings.HasPrefix(branchReference, referencePrefix) || !exists {
			findings = append(findings, profileBlocker(sourceKind, "reference_event_schema_branch", fmt.Sprintf("$.oneOf[%d]", index), "RunEvent branch must resolve to a local typed definition."))
			continue
		}
		allOf := objectSlice(definition["allOf"])
		if len(allOf) != 2 {
			findings = append(findings, profileBlocker(sourceKind, "reference_event_schema_branch", fmt.Sprintf("$.oneOf[%d]", index), "RunEvent branch must combine the exact common envelope and one typed data schema."))
			continue
		}
		commonReference := stringValue(allOf[0]["$ref"])
		commonName := strings.TrimPrefix(commonReference, referencePrefix)
		common, commonExists := definitions[commonName].(map[string]any)
		if !strings.HasPrefix(commonReference, referencePrefix) || !commonExists {
			findings = append(findings, profileBlocker(sourceKind, "reference_event_schema_common", fmt.Sprintf("$.oneOf[%d]", index), "RunEvent branch must reference a local common envelope."))
			continue
		}
		if !commonValidated[commonReference] {
			commonValidated[commonReference] = true
			if schemaSignature(common, nil) != schemaSignature(referenceRunEventCommonSchema(), nil) {
				findings = append(findings, profileBlocker(sourceKind, "reference_event_schema_common", "$", "RunEvent common envelope must preserve exact identity, tenant, cursor, time, discriminator, and data fields."))
			}
		}
		if len(allOf[1]) != 1 {
			findings = append(findings, profileBlocker(sourceKind, "reference_event_schema_branch", fmt.Sprintf("$.oneOf[%d]", index), "RunEvent typed branch may contain only its exact property refinements."))
			continue
		}
		properties, _ := allOf[1]["properties"].(map[string]any)
		if len(properties) != 2 {
			findings = append(findings, profileBlocker(sourceKind, "reference_event_schema_branch", fmt.Sprintf("$.oneOf[%d]", index), "RunEvent typed branch must refine exactly type and data."))
			continue
		}
		typeSchema, _ := properties["type"].(map[string]any)
		eventType := stringValue(typeSchema["const"])
		expectedData, expected := referenceRunEventDataSchemas()[eventType]
		data, dataOK := properties["data"].(map[string]any)
		if !expected || !dataOK || schemaSignature(data, map[string]string{usageReference: "urn:worksflow:reference-run-event-usage"}) != schemaSignature(expectedData, nil) {
			findings = append(findings, profileBlocker(sourceKind, "reference_event_schema_data", fmt.Sprintf("$.oneOf[%d]", index), "RunEvent branch data must preserve the exact required fields and value constraints for its event type."))
			continue
		}
		types = append(types, eventType)
	}
	usageName := strings.TrimPrefix(usageReference, referencePrefix)
	usage, usageExists := definitions[usageName].(map[string]any)
	if !usageExists || schemaSignature(usage, nil) != schemaSignature(referenceRunEventUsageSchema(), nil) {
		findings = append(findings, profileBlocker(sourceKind, "reference_event_schema_usage", "$", "RunEvent usage payload must preserve exact token accounting fields."))
	}
	sort.Strings(types)
	if !sameProfileSet(types, referenceAIConversationEvents) {
		findings = append(findings, profileBlocker(sourceKind, "reference_event_schema_branch", "$.oneOf", "RunEvent branches must declare each Reference event type exactly once."))
	}
	return types, findings
}

func referenceRunEventCommonSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"schemaVersion", "eventId", "projectId", "conversationId", "runId", "sequence", "type", "createdAt", "data"},
		"properties": map[string]any{
			"schemaVersion":  map[string]any{"const": "run-event/v1"},
			"eventId":        map[string]any{"type": "string", "format": "uuid"},
			"projectId":      map[string]any{"type": "string", "format": "uuid"},
			"conversationId": map[string]any{"type": "string", "format": "uuid"},
			"runId":          map[string]any{"type": "string", "format": "uuid"},
			"sequence":       map[string]any{"type": "integer", "minimum": 1},
			"type":           map[string]any{"enum": append([]string{}, referenceAIConversationEvents...)},
			"createdAt":      map[string]any{"type": "string", "format": "date-time"},
			"data":           map[string]any{"type": "object"},
		},
	}
}

func referenceRunEventUsageSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"inputTokens", "outputTokens"},
		"properties": map[string]any{
			"inputTokens":  map[string]any{"type": "integer", "minimum": 0},
			"outputTokens": map[string]any{"type": "integer", "minimum": 0},
		},
	}
}

func referenceRunEventDataSchemas() map[string]map[string]any {
	object := func(required []string, properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
	}
	nonEmptyString := func() map[string]any { return map[string]any{"type": "string", "minLength": 1} }
	return map[string]map[string]any{
		"run.queued":    object([]string{"queuePosition"}, map[string]any{"queuePosition": map[string]any{"type": "integer", "minimum": 0}}),
		"run.started":   object([]string{"modelProfileId"}, map[string]any{"modelProfileId": nonEmptyString()}),
		"output.delta":  object([]string{"text"}, map[string]any{"text": nonEmptyString()}),
		"tool.call":     object([]string{"callId", "name", "arguments"}, map[string]any{"callId": nonEmptyString(), "name": nonEmptyString(), "arguments": map[string]any{"type": "object"}}),
		"tool.result":   object([]string{"callId", "output"}, map[string]any{"callId": nonEmptyString(), "output": map[string]any{}}),
		"run.completed": object([]string{"finishReason", "usage"}, map[string]any{"finishReason": nonEmptyString(), "usage": map[string]any{"$ref": "urn:worksflow:reference-run-event-usage"}}),
		"run.failed":    object([]string{"errorCode", "errorMessage", "retryable"}, map[string]any{"errorCode": nonEmptyString(), "errorMessage": nonEmptyString(), "retryable": map[string]any{"type": "boolean"}}),
		"run.cancelled": object([]string{"reason"}, map[string]any{"reason": nonEmptyString()}),
		"heartbeat":     object([]string{"cursor"}, map[string]any{"cursor": map[string]any{"type": "integer", "minimum": 1}}),
	}
}

func schemaSignature(value any, substitutions map[string]string) string {
	normalized := normalizeSchemaSignatureValue(value, substitutions, "")
	encoded, _ := json.Marshal(normalized)
	return string(encoded)
}

func normalizeSchemaSignatureValue(value any, substitutions map[string]string, parentKey string) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			if key == "$ref" {
				if replacement := substitutions[stringValue(child)]; replacement != "" {
					result[key] = replacement
					continue
				}
			}
			result[key] = normalizeSchemaSignatureValue(child, substitutions, key)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = normalizeSchemaSignatureValue(child, substitutions, parentKey)
		}
		if parentKey == "required" || parentKey == "enum" {
			sort.Slice(result, func(i, j int) bool { return fmt.Sprint(result[i]) < fmt.Sprint(result[j]) })
		}
		return result
	case []string:
		result := append([]string{}, typed...)
		if parentKey == "required" || parentKey == "enum" {
			sort.Strings(result)
		}
		return result
	default:
		return typed
	}
}

func inspectReferenceDeployment(deployment, data, runtime map[string]any) []Finding {
	findings := []Finding{}
	migration, _ := deployment["migration"].(map[string]any)
	dataMigration, _ := data["migrationPolicy"].(map[string]any)
	if !booleanValue(migration["required"]) || !booleanValue(migration["beforeTraffic"]) || stringValue(migration["commandId"]) != stringValue(dataMigration["applyCommandId"]) {
		findings = append(findings, profileBlocker(KindDeployment, "reference_deployment_migration", "$.migration", "Reference deployment must run the exact Data Contract migration command before traffic."))
	}
	environments := objectByStringField(objectSlice(deployment["environmentVariables"]), "name")
	gateway, _ := runtime["gateway"].(map[string]any)
	expected := map[string]struct {
		secret bool
		scope  string
	}{
		"PUBLIC_API_ORIGIN":   {secret: false, scope: "web-build"},
		"DATABASE_URL":        {secret: true, scope: "api-runtime"},
		"SESSION_SIGNING_KEY": {secret: true, scope: "api-runtime"},
		"MODEL_PROFILE_ID":    {secret: false, scope: "api-runtime"},
		stringValue(gateway["endpointEnvironmentVariable"]):   {secret: false, scope: "api-runtime"},
		stringValue(gateway["capabilityEnvironmentVariable"]): {secret: true, scope: "api-runtime"},
	}
	if len(expected) != 6 || len(environments) != len(expected) {
		findings = append(findings, profileBlocker(KindDeployment, "reference_deployment_environment_set", "$.environmentVariables", "Reference deployment must expose only the exact database, session, and Gateway environment allowlist."))
	}
	for name, declaration := range expected {
		environment := environments[name]
		if name == "" || environment == nil || !booleanValue(environment["required"]) || booleanValue(environment["secret"]) != declaration.secret || stringValue(environment["scope"]) != declaration.scope {
			findings = append(findings, profileBlocker(KindDeployment, "reference_deployment_environment", "$.environmentVariables", "Reference deployment is missing an exact required API runtime environment declaration."))
		}
	}
	for name := range environments {
		if _, allowed := expected[name]; !allowed {
			findings = append(findings, profileBlocker(KindDeployment, "reference_provider_secret_exposure", "$.environmentVariables[name="+name+"]", "Generated application deployment must not receive a provider credential."))
		}
	}
	return findings
}

func inspectReferenceVerification(facts Facts) []Finding {
	if len(facts.Oracles) != len(referenceAIConversationAcceptance) {
		return []Finding{profileBlocker(KindVerification, "reference_verification_oracle_set", "$.oracles", "Reference profile requires exactly fifteen independent blocking Oracles.")}
	}
	coverage := map[string]int{}
	for _, oracle := range facts.Oracles {
		if len(oracle.AcceptanceCriterionIDs) != 1 || strings.TrimSpace(oracle.CommandID) == "" {
			return []Finding{profileBlocker(KindVerification, "reference_verification_oracle_shape", "$.oracles[id="+oracle.ID+"]", "Every Reference Oracle must map exactly one acceptance criterion to one executable command identity.")}
		}
		for _, acceptanceID := range oracle.AcceptanceCriterionIDs {
			coverage[acceptanceID]++
		}
	}
	for _, acceptanceID := range referenceAIConversationAcceptance {
		if coverage[acceptanceID] != 1 {
			return []Finding{profileBlocker(KindVerification, "reference_verification_coverage", "$.oracles", "Every Reference acceptance criterion must map to exactly one blocking oracle.")}
		}
	}
	if len(coverage) != len(referenceAIConversationAcceptance) {
		return []Finding{profileBlocker(KindVerification, "reference_verification_coverage", "$.oracles", "Verification Contract contains acceptance criteria outside the Reference profile.")}
	}
	return nil
}

func apiOperationObjects(api map[string]any) map[string]map[string]any {
	result := map[string]map[string]any{}
	paths, _ := api["paths"].(map[string]any)
	for _, rawPath := range paths {
		path, _ := rawPath.(map[string]any)
		for _, method := range []string{"delete", "get", "head", "options", "patch", "post", "put"} {
			operation, _ := path[method].(map[string]any)
			if id := stringValue(operation["operationId"]); id != "" {
				result[id] = operation
			}
		}
	}
	return result
}

func apiRunEventTypes(api map[string]any) []string {
	values, _ := inspectReferenceAPIRunEventSchema(api)
	return values
}

func operationUsesSessionAuth(operation map[string]any) bool {
	values, _ := operation["security"].([]any)
	for _, value := range values {
		object, _ := value.(map[string]any)
		if _, exists := object["SessionAuth"]; exists {
			return true
		}
	}
	return false
}

func operationRequiresHeader(operation map[string]any, name string) bool {
	for _, parameter := range objectSlice(operation["parameters"]) {
		if stringValue(parameter["in"]) == "header" && strings.EqualFold(stringValue(parameter["name"]), name) && booleanValue(parameter["required"]) {
			return true
		}
	}
	return false
}

func profilePolicyCovers(policies []PermissionPolicy, role, resource string, actions []string) bool {
	covered := map[string]bool{}
	for _, policy := range policies {
		if policy.Resource != resource || !policy.TenantScoped || !containsProfileString(policy.Roles, role) {
			continue
		}
		for _, action := range policy.Actions {
			covered[action] = true
		}
	}
	for _, action := range actions {
		if !covered[action] {
			return false
		}
	}
	return true
}

func containsProfileIndex(entity map[string]any, fields []string, unique bool) bool {
	for _, index := range objectSlice(entity["indexes"]) {
		if booleanValue(index["unique"]) == unique && sameOrderedStrings(stringSlice(index["fieldIds"]), fields) {
			return true
		}
	}
	return false
}

func containsProfileForeignKey(entity map[string]any, id string, fields []string, targetEntity string, targetFields []string, onDelete string) bool {
	for _, foreignKey := range objectSlice(entity["foreignKeys"]) {
		if stringValue(foreignKey["id"]) == id &&
			sameOrderedStrings(stringSlice(foreignKey["fieldIds"]), fields) &&
			stringValue(foreignKey["targetEntityId"]) == targetEntity &&
			sameOrderedStrings(stringSlice(foreignKey["targetFieldIds"]), targetFields) &&
			stringValue(foreignKey["onDelete"]) == onDelete {
			return true
		}
	}
	return false
}

func objectByStringField(values []map[string]any, field string) map[string]map[string]any {
	result := make(map[string]map[string]any, len(values))
	for _, value := range values {
		result[stringValue(value[field])] = value
	}
	return result
}

func sortedOperationIDs(values map[string]APIOperation) []string {
	result := make([]string, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func sameProfileSet(left, right []string) bool {
	leftCopy, rightCopy := append([]string{}, left...), append([]string{}, right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	return sameOrderedStrings(leftCopy, rightCopy)
}

func sameOrderedStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func containsProfileString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func booleanValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func profileBlocker(sourceKind, code, path, message string) Finding {
	return Finding{Code: code, Path: path, Message: message, Severity: "blocker", SourceKind: sourceKind}
}

func sortedProfileFindings(findings []Finding) []Finding {
	sort.Slice(findings, func(i, j int) bool {
		left := findings[i].SourceKind + "\x00" + findings[i].Code + "\x00" + findings[i].Path + "\x00" + findings[i].Message
		right := findings[j].SourceKind + "\x00" + findings[j].Code + "\x00" + findings[j].Path + "\x00" + findings[j].Message
		return left < right
	})
	return findings
}
