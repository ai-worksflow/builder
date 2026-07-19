package contracts

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

const (
	KindAPI          = "api_contract"
	KindData         = "data_contract"
	KindPermission   = "permission_contract"
	KindAIRuntime    = "ai_runtime_contract"
	KindDeployment   = "deployment_contract"
	KindVerification = "verification_contract"
)

var schemaFiles = map[string]map[string]string{
	KindAPI: {
		"api-contract/v1": "schemas/api-contract-v1.json",
		"api-contract/v2": "schemas/api-contract-v2.json",
	},
	KindData: {
		"data-contract/v1": "schemas/data-contract-v1.json",
		"data-contract/v2": "schemas/data-contract-v2.json",
	},
	KindPermission: {"permission-contract/v1": "schemas/permission-contract-v1.json"},
	KindAIRuntime: {
		"ai-runtime-contract/v1": "schemas/ai-runtime-contract-v1.json",
		"ai-runtime-contract/v2": "schemas/ai-runtime-contract-v2.json",
	},
	KindDeployment:   {"deployment-contract/v1": "schemas/deployment-contract-v1.json"},
	KindVerification: {"verification-contract/v1": "schemas/verification-contract-v1.json"},
}

var schemaVersions = map[string]string{
	KindAPI:          "api-contract/v2",
	KindData:         "data-contract/v2",
	KindPermission:   "permission-contract/v1",
	KindAIRuntime:    "ai-runtime-contract/v2",
	KindDeployment:   "deployment-contract/v1",
	KindVerification: "verification-contract/v1",
}

//go:embed schemas/*.json
var embeddedSchemas embed.FS

type Finding struct {
	Code       string `json:"code"`
	Path       string `json:"path"`
	Message    string `json:"message"`
	Severity   string `json:"severity"`
	SourceKind string `json:"sourceKind,omitempty"`
}

type APIOperation struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Path   string `json:"path"`
}

type DataEntity struct {
	ID       string   `json:"id"`
	FieldIDs []string `json:"fieldIds"`
}

type PermissionPolicy struct {
	ID           string   `json:"id"`
	Roles        []string `json:"roles"`
	Resource     string   `json:"resource"`
	Actions      []string `json:"actions"`
	TenantScoped bool     `json:"tenantScoped"`
}

type Oracle struct {
	ID                     string   `json:"id"`
	AcceptanceCriterionIDs []string `json:"acceptanceCriterionIds"`
	Kind                   string   `json:"kind"`
	Target                 string   `json:"target"`
	CommandID              string   `json:"commandId,omitempty"`
}

type AIRuntimeProfile struct {
	ApplicationProfile           string   `json:"applicationProfile"`
	ProviderPortID               string   `json:"providerPortId"`
	ProviderProtocol             string   `json:"providerProtocol"`
	ProviderRequestSchemaRef     string   `json:"providerRequestSchemaRef"`
	ProviderEventSchemaRef       string   `json:"providerEventSchemaRef"`
	GatewayEndpointEnvironment   string   `json:"gatewayEndpointEnvironment"`
	GatewayCapabilityEnvironment string   `json:"gatewayCapabilityEnvironment"`
	EventSchemaRef               string   `json:"eventSchemaRef"`
	EventTypes                   []string `json:"eventTypes"`
	RateLimitScope               string   `json:"rateLimitScope"`
	RateLimitRequests            int64    `json:"rateLimitRequests"`
	RateLimitWindowSeconds       int64    `json:"rateLimitWindowSeconds"`
	RateLimitBurst               int64    `json:"rateLimitBurst"`
	TimeoutSeconds               int64    `json:"timeoutSeconds"`
}

// Facts is the machine-readable projection consumed by the deterministic
// BuildContract compiler. It is produced only after schema and semantic
// validation; callers must never infer these facts from prose.
type Facts struct {
	Kind             string             `json:"kind"`
	SchemaVersion    string             `json:"schemaVersion"`
	Operations       []APIOperation     `json:"operations"`
	Entities         []DataEntity       `json:"entities"`
	Roles            []string           `json:"roles"`
	Policies         []PermissionPolicy `json:"policies"`
	Oracles          []Oracle           `json:"oracles"`
	AIRuntime        bool               `json:"aiRuntime"`
	AIRuntimeProfile *AIRuntimeProfile  `json:"aiRuntimeProfile,omitempty"`
	Deployment       bool               `json:"deployment"`
	DeploymentRole   []string           `json:"deploymentRoles"`
}

var (
	compiledSchemas map[string]*jsonschema.Schema
	schemaLoadErr   error
	schemaLoadOnce  sync.Once
)

func SupportedKind(kind string) bool {
	_, supported := schemaFiles[strings.TrimSpace(kind)]
	return supported
}

func ExpectedSchemaVersion(kind string) string {
	return schemaVersions[strings.TrimSpace(kind)]
}

func Validate(kind string, payload json.RawMessage) []Finding {
	_, findings := Inspect(kind, payload)
	return findings
}

func Inspect(kind string, payload json.RawMessage) (Facts, []Finding) {
	kind = strings.TrimSpace(kind)
	if !SupportedKind(kind) {
		return emptyFacts(kind), []Finding{blocker("contract_kind_unsupported", "$", "Artifact kind is not a supported machine contract.")}
	}
	value, err := decodeJSON(payload)
	if err != nil {
		return emptyFacts(kind), []Finding{blocker("contract_invalid_json", "$", "Contract content must be one JSON object: "+err.Error())}
	}
	object, ok := value.(map[string]any)
	if !ok {
		return emptyFacts(kind), []Finding{blocker("contract_schema_invalid", "$", "Contract content must be one JSON object.")}
	}
	version := strings.TrimSpace(stringValue(object["schemaVersion"]))
	_, versionSupported := schemaFiles[kind][version]
	if !versionSupported {
		return emptyFacts(kind), []Finding{blocker("contract_schema_invalid", "$.schemaVersion", "Contract schemaVersion is missing or unsupported.")}
	}
	schemas, err := loadSchemas()
	if err != nil {
		return emptyFacts(kind), []Finding{blocker("contract_schema_unavailable", "$", "Platform contract schema could not be loaded: "+err.Error())}
	}
	if err := schemas[schemaKey(kind, version)].Validate(value); err != nil {
		return emptyFacts(kind), []Finding{blocker("contract_schema_invalid", "$", err.Error())}
	}
	facts, findings := inspectSemantics(kind, object)
	if len(findings) != 0 {
		return emptyFacts(kind), findings
	}
	facts.SchemaVersion = version
	return facts, nil
}

func emptyFacts(kind string) Facts {
	return Facts{
		Kind: kind, SchemaVersion: ExpectedSchemaVersion(kind), Operations: []APIOperation{}, Entities: []DataEntity{},
		Roles: []string{}, Policies: []PermissionPolicy{}, Oracles: []Oracle{}, DeploymentRole: []string{},
	}
}

func decodeJSON(payload json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values are not allowed")
		}
		return nil, err
	}
	return value, nil
}

func loadSchemas() (map[string]*jsonschema.Schema, error) {
	schemaLoadOnce.Do(func() {
		compiledSchemas = make(map[string]*jsonschema.Schema)
		for kind, versions := range schemaFiles {
			for version, fileName := range versions {
				payload, err := embeddedSchemas.ReadFile(fileName)
				if err != nil {
					schemaLoadErr = fmt.Errorf("read %s: %w", fileName, err)
					return
				}
				compiler := jsonschema.NewCompiler()
				compiler.Draft = jsonschema.Draft2020
				compiler.LoadURL = func(string) (io.ReadCloser, error) {
					return nil, errors.New("external JSON Schema references are disabled")
				}
				resource := "memory://" + fileName
				if err := compiler.AddResource(resource, bytes.NewReader(payload)); err != nil {
					schemaLoadErr = fmt.Errorf("add %s: %w", fileName, err)
					return
				}
				schema, err := compiler.Compile(resource)
				if err != nil {
					schemaLoadErr = fmt.Errorf("compile %s: %w", fileName, err)
					return
				}
				compiledSchemas[schemaKey(kind, version)] = schema
			}
		}
	})
	return compiledSchemas, schemaLoadErr
}

func schemaKey(kind, version string) string {
	return kind + "\x00" + version
}

func inspectSemantics(kind string, value map[string]any) (Facts, []Finding) {
	switch kind {
	case KindAPI:
		return inspectAPI(value)
	case KindData:
		return inspectData(value)
	case KindPermission:
		return inspectPermission(value)
	case KindAIRuntime:
		return inspectAIRuntime(value)
	case KindDeployment:
		return inspectDeployment(value)
	case KindVerification:
		return inspectVerification(value)
	default:
		return emptyFacts(kind), []Finding{blocker("contract_kind_unsupported", "$", "Artifact kind is not a supported machine contract.")}
	}
}

func inspectAPI(value map[string]any) (Facts, []Finding) {
	facts := emptyFacts(KindAPI)
	seen := map[string]bool{}
	findings := rejectExternalRefs(value, "$")
	if stringValue(value["schemaVersion"]) == "api-contract/v2" {
		findings = append(findings, validateLocalAPIReferences(value)...)
		findings = append(findings, validateAPIResponseReferences(value)...)
	}
	paths, _ := value["paths"].(map[string]any)
	methods := []string{"delete", "get", "head", "options", "patch", "post", "put"}
	pathNames := sortedMapKeys(paths)
	for _, path := range pathNames {
		item, _ := paths[path].(map[string]any)
		for _, method := range methods {
			operation, exists := item[method].(map[string]any)
			if !exists {
				continue
			}
			operationID := stringValue(operation["operationId"])
			if seen[operationID] {
				findings = append(findings, blocker("api_contract_duplicate_operation", "$.paths", "operationId "+operationID+" is duplicated."))
				continue
			}
			seen[operationID] = true
			facts.Operations = append(facts.Operations, APIOperation{ID: operationID, Method: strings.ToUpper(method), Path: path})
		}
	}
	sort.Slice(facts.Operations, func(i, j int) bool { return facts.Operations[i].ID < facts.Operations[j].ID })
	return facts, findings
}

func inspectData(value map[string]any) (Facts, []Finding) {
	facts := emptyFacts(KindData)
	entities := objectSlice(value["entities"])
	entityByID := map[string]map[string]any{}
	tables := map[string]bool{}
	findings := []Finding{}
	if migration, ok := value["migrationPolicy"].(map[string]any); ok && !safeRelativePath(stringValue(migration["directory"])) {
		findings = append(findings, blocker("data_contract_migration_path", "$.migrationPolicy.directory", "Migration directory must be a normalized relative path."))
	}
	for index, entity := range entities {
		id := stringValue(entity["id"])
		table := stringValue(entity["tableName"])
		if _, duplicate := entityByID[id]; duplicate {
			findings = append(findings, blocker("data_contract_duplicate_entity", fmt.Sprintf("$.entities[%d].id", index), "Entity ID is duplicated."))
		}
		if tables[table] {
			findings = append(findings, blocker("data_contract_duplicate_table", fmt.Sprintf("$.entities[%d].tableName", index), "Table name is duplicated."))
		}
		entityByID[id] = entity
		tables[table] = true
	}
	for index, entity := range entities {
		id := stringValue(entity["id"])
		fieldIDs := map[string]bool{}
		fieldNames := map[string]bool{}
		fieldByID := map[string]map[string]any{}
		fact := DataEntity{ID: id, FieldIDs: []string{}}
		for fieldIndex, field := range objectSlice(entity["fields"]) {
			fieldID := stringValue(field["id"])
			fieldName := stringValue(field["name"])
			if fieldIDs[fieldID] || fieldNames[fieldName] {
				findings = append(findings, blocker("data_contract_duplicate_field", fmt.Sprintf("$.entities[%d].fields[%d]", index, fieldIndex), "Field ID and name must be unique within an entity."))
			}
			fieldIDs[fieldID] = true
			fieldNames[fieldName] = true
			fieldByID[fieldID] = field
			fact.FieldIDs = append(fact.FieldIDs, fieldID)
			if reference, ok := field["references"].(map[string]any); ok {
				targetEntity := stringValue(reference["entityId"])
				targetField := stringValue(reference["fieldId"])
				target, exists := entityByID[targetEntity]
				if !exists || !containsField(target, targetField) {
					findings = append(findings, blocker("data_contract_invalid_reference", fmt.Sprintf("$.entities[%d].fields[%d].references", index, fieldIndex), "Foreign-key target does not exist."))
				} else if stringValue(value["schemaVersion"]) == "data-contract/v2" && dataEntitiesShareTenantScope(entity, target) {
					findings = append(findings, blocker(
						"data_contract_tenant_reference_requires_composite",
						fmt.Sprintf("$.entities[%d].fields[%d].references", index, fieldIndex),
						"Tenant-scoped entity relationships must use an entity-level composite foreign key that includes both tenant fields.",
					))
				}
			}
		}
		for _, primaryKey := range stringSlice(entity["primaryKey"]) {
			if !fieldIDs[primaryKey] {
				findings = append(findings, blocker("data_contract_invalid_primary_key", fmt.Sprintf("$.entities[%d].primaryKey", index), "Primary key references an unknown field."))
			}
		}
		indexIDs := map[string]bool{}
		for indexIndex, indexValue := range objectSlice(entity["indexes"]) {
			indexID := stringValue(indexValue["id"])
			if indexIDs[indexID] {
				findings = append(findings, blocker("data_contract_duplicate_index", fmt.Sprintf("$.entities[%d].indexes[%d].id", index, indexIndex), "Index ID is duplicated."))
			}
			indexIDs[indexID] = true
			for _, fieldID := range stringSlice(indexValue["fieldIds"]) {
				if !fieldIDs[fieldID] {
					findings = append(findings, blocker("data_contract_invalid_index", fmt.Sprintf("$.entities[%d].indexes[%d].fieldIds", index, indexIndex), "Index references an unknown field."))
				}
			}
		}
		if tenant, ok := entity["tenantScope"].(map[string]any); ok && stringValue(tenant["mode"]) != "global" && !fieldIDs[stringValue(tenant["fieldId"])] {
			findings = append(findings, blocker("data_contract_invalid_tenant_field", fmt.Sprintf("$.entities[%d].tenantScope.fieldId", index), "Tenant scope references an unknown field."))
		}
		if stringValue(value["schemaVersion"]) == "data-contract/v2" {
			foreignKeyIDs := map[string]bool{}
			for foreignKeyIndex, foreignKey := range objectSlice(entity["foreignKeys"]) {
				field := fmt.Sprintf("$.entities[%d].foreignKeys[%d]", index, foreignKeyIndex)
				foreignKeyID := stringValue(foreignKey["id"])
				if foreignKeyIDs[foreignKeyID] {
					findings = append(findings, blocker("data_contract_duplicate_foreign_key", field+".id", "Foreign-key ID is duplicated within the entity."))
				}
				foreignKeyIDs[foreignKeyID] = true
				sourceFieldIDs := stringSlice(foreignKey["fieldIds"])
				targetFieldIDs := stringSlice(foreignKey["targetFieldIds"])
				targetEntityID := stringValue(foreignKey["targetEntityId"])
				targetEntity, targetExists := entityByID[targetEntityID]
				if len(sourceFieldIDs) != len(targetFieldIDs) {
					findings = append(findings, blocker("data_contract_foreign_key_arity", field, "Foreign-key source and target field lists must have the same length."))
					continue
				}
				if !targetExists {
					findings = append(findings, blocker("data_contract_invalid_foreign_key_target", field+".targetEntityId", "Foreign-key target entity does not exist."))
					continue
				}
				targetFields := dataFieldsByID(targetEntity)
				validFields := true
				for fieldOffset := range sourceFieldIDs {
					sourceField, sourceExists := fieldByID[sourceFieldIDs[fieldOffset]]
					targetField, targetFieldExists := targetFields[targetFieldIDs[fieldOffset]]
					if !sourceExists || !targetFieldExists {
						validFields = false
						continue
					}
					if stringValue(sourceField["type"]) != stringValue(targetField["type"]) {
						findings = append(findings, blocker("data_contract_foreign_key_type_mismatch", field, "Foreign-key source and target field types must match by ordinal."))
					}
					if stringValue(foreignKey["onDelete"]) == "set-null" && !booleanValue(sourceField["nullable"]) {
						findings = append(findings, blocker("data_contract_foreign_key_set_null", field, "SET NULL foreign keys may contain only nullable source fields."))
					}
				}
				if !validFields {
					findings = append(findings, blocker("data_contract_invalid_foreign_key_field", field, "Foreign key references an unknown source or target field."))
					continue
				}
				if !dataEntityHasCandidateKey(targetEntity, targetFieldIDs) {
					findings = append(findings, blocker("data_contract_foreign_key_target_not_unique", field+".targetFieldIds", "Foreign-key target fields must be an exact primary key or unique index."))
				}
				if !dataForeignKeyPreservesTenant(entity, targetEntity, sourceFieldIDs, targetFieldIDs) {
					findings = append(findings, blocker("data_contract_foreign_key_tenant_scope", field, "Foreign keys between tenant-scoped entities must bind matching tenant fields at the same ordinal."))
				}
			}
		}
		sort.Strings(fact.FieldIDs)
		facts.Entities = append(facts.Entities, fact)
	}
	sort.Slice(facts.Entities, func(i, j int) bool { return facts.Entities[i].ID < facts.Entities[j].ID })
	return facts, findings
}

func inspectPermission(value map[string]any) (Facts, []Finding) {
	facts := emptyFacts(KindPermission)
	findings := []Finding{}
	roleSet := map[string]bool{}
	for index, role := range objectSlice(value["roles"]) {
		id := stringValue(role["id"])
		if roleSet[id] {
			findings = append(findings, blocker("permission_contract_duplicate_role", fmt.Sprintf("$.roles[%d].id", index), "Role ID is duplicated."))
		}
		roleSet[id] = true
		facts.Roles = append(facts.Roles, id)
	}
	policySet := map[string]bool{}
	tenant, _ := value["tenant"].(map[string]any)
	requiresTenantScope := stringValue(tenant["mode"]) != "single"
	for index, policy := range objectSlice(value["policies"]) {
		id := stringValue(policy["id"])
		if policySet[id] {
			findings = append(findings, blocker("permission_contract_duplicate_policy", fmt.Sprintf("$.policies[%d].id", index), "Policy ID is duplicated."))
		}
		policySet[id] = true
		roles := stringSlice(policy["roles"])
		for _, role := range roles {
			if !roleSet[role] {
				findings = append(findings, blocker("permission_contract_unknown_role", fmt.Sprintf("$.policies[%d].roles", index), "Policy references an unknown role."))
			}
		}
		tenantScoped, _ := policy["tenantScoped"].(bool)
		if requiresTenantScope && !tenantScoped {
			findings = append(findings, blocker("permission_contract_tenant_bypass", fmt.Sprintf("$.policies[%d].tenantScoped", index), "Multi-tenant policies must be tenant scoped."))
		}
		sort.Strings(roles)
		actions := stringSlice(policy["actions"])
		sort.Strings(actions)
		facts.Policies = append(facts.Policies, PermissionPolicy{ID: id, Roles: roles, Resource: stringValue(policy["resource"]), Actions: actions, TenantScoped: tenantScoped})
	}
	sort.Strings(facts.Roles)
	sort.Slice(facts.Policies, func(i, j int) bool { return facts.Policies[i].ID < facts.Policies[j].ID })
	return facts, findings
}

func inspectAIRuntime(value map[string]any) (Facts, []Finding) {
	facts := emptyFacts(KindAIRuntime)
	facts.SchemaVersion = stringValue(value["schemaVersion"])
	facts.AIRuntime = true
	findings := []Finding{}
	run, _ := value["run"].(map[string]any)
	statuses := stringSet(stringSlice(run["statusValues"]))
	for _, required := range []string{"queued", "running", "completed", "failed", "cancelled"} {
		if !statuses[required] {
			findings = append(findings, blocker("ai_runtime_status_missing", "$.run.statusValues", "AI runtime status "+required+" is required."))
		}
	}
	streaming, _ := value["streaming"].(map[string]any)
	events := stringSet(stringSlice(streaming["eventTypes"]))
	for _, required := range []string{"run.started", "output.delta", "run.completed", "run.failed"} {
		if !events[required] {
			findings = append(findings, blocker("ai_runtime_event_missing", "$.streaming.eventTypes", "AI runtime event "+required+" is required."))
		}
	}
	if facts.SchemaVersion == "ai-runtime-contract/v2" {
		providerPort, _ := value["providerPort"].(map[string]any)
		gateway, _ := value["gateway"].(map[string]any)
		rateLimit, _ := value["rateLimit"].(map[string]any)
		limits, _ := value["limits"].(map[string]any)
		retention, _ := value["retention"].(map[string]any)
		eventTypes := stringSlice(streaming["eventTypes"])
		sort.Strings(eventTypes)
		facts.AIRuntimeProfile = &AIRuntimeProfile{
			ApplicationProfile: stringValue(value["applicationProfile"]),
			ProviderPortID:     stringValue(providerPort["portId"]), ProviderProtocol: stringValue(providerPort["protocol"]),
			ProviderRequestSchemaRef: stringValue(providerPort["requestSchemaRef"]), ProviderEventSchemaRef: stringValue(providerPort["eventSchemaRef"]),
			GatewayEndpointEnvironment:   stringValue(gateway["endpointEnvironmentVariable"]),
			GatewayCapabilityEnvironment: stringValue(gateway["capabilityEnvironmentVariable"]),
			EventSchemaRef:               stringValue(streaming["eventSchemaRef"]), EventTypes: eventTypes,
			RateLimitScope: stringValue(rateLimit["scope"]), RateLimitRequests: int64Value(rateLimit["requests"]),
			RateLimitWindowSeconds: int64Value(rateLimit["windowSeconds"]), RateLimitBurst: int64Value(rateLimit["burst"]),
			TimeoutSeconds: int64Value(limits["timeoutSeconds"]),
		}
		if !safeRelativePath(facts.AIRuntimeProfile.EventSchemaRef) {
			findings = append(findings, blocker("ai_runtime_event_schema_path", "$.streaming.eventSchemaRef", "AI runtime event schema must use a normalized relative path."))
		}
		if facts.AIRuntimeProfile.RateLimitBurst > facts.AIRuntimeProfile.RateLimitRequests {
			findings = append(findings, blocker("ai_runtime_rate_limit_burst", "$.rateLimit.burst", "AI runtime rate-limit burst cannot exceed requests per window."))
		}
		if int64Value(retention["eventDays"]) > int64Value(retention["runDays"]) {
			findings = append(findings, blocker("ai_runtime_event_retention", "$.retention.eventDays", "AI runtime event retention cannot exceed run retention."))
		}
	}
	return facts, findings
}

func inspectDeployment(value map[string]any) (Facts, []Finding) {
	facts := emptyFacts(KindDeployment)
	facts.Deployment = true
	findings := []Finding{}
	ports := map[string]bool{}
	portNumbers := map[string]bool{}
	for index, port := range objectSlice(value["ports"]) {
		name := stringValue(port["name"])
		number := fmt.Sprint(port["containerPort"])
		if ports[name] || portNumbers[number] {
			findings = append(findings, blocker("deployment_contract_duplicate_port", fmt.Sprintf("$.ports[%d]", index), "Port name and container port must be unique."))
		}
		ports[name] = true
		portNumbers[number] = true
	}
	healthChecks := map[string]string{}
	for index, health := range objectSlice(value["healthChecks"]) {
		id := stringValue(health["id"])
		if _, duplicate := healthChecks[id]; duplicate {
			findings = append(findings, blocker("deployment_contract_duplicate_health", fmt.Sprintf("$.healthChecks[%d].id", index), "Health check ID is duplicated."))
		}
		portName := stringValue(health["portName"])
		if !ports[portName] {
			findings = append(findings, blocker("deployment_contract_unknown_health_port", fmt.Sprintf("$.healthChecks[%d].portName", index), "Health check references an unknown port."))
		}
		healthChecks[id] = portName
	}
	serviceIDs := map[string]bool{}
	roles := map[string]bool{}
	for index, service := range objectSlice(value["services"]) {
		id := stringValue(service["id"])
		role := stringValue(service["role"])
		if serviceIDs[id] {
			findings = append(findings, blocker("deployment_contract_duplicate_service", fmt.Sprintf("$.services[%d].id", index), "Service ID is duplicated."))
		}
		serviceIDs[id] = true
		roles[role] = true
		if !safeRelativePath(stringValue(service["sourceRoot"])) || !safeRelativePath(stringValue(service["buildOutput"])) {
			findings = append(findings, blocker("deployment_contract_service_path", fmt.Sprintf("$.services[%d]", index), "Service roots and build outputs must be normalized relative paths."))
		}
		if !ports[stringValue(service["portName"])] {
			findings = append(findings, blocker("deployment_contract_unknown_service_port", fmt.Sprintf("$.services[%d].portName", index), "Service references an unknown port."))
		}
		healthPort, exists := healthChecks[stringValue(service["healthCheckId"])]
		if !exists {
			findings = append(findings, blocker("deployment_contract_unknown_service_health", fmt.Sprintf("$.services[%d].healthCheckId", index), "Service references an unknown health check."))
		} else if healthPort != stringValue(service["portName"]) {
			findings = append(findings, blocker("deployment_contract_service_health_port_mismatch", fmt.Sprintf("$.services[%d].healthCheckId", index), "Service health check must use the service port."))
		}
	}
	for _, required := range []string{"web", "api"} {
		if !roles[required] {
			findings = append(findings, blocker("deployment_contract_service_role_missing", "$.services", "Full-stack deployment requires a "+required+" service."))
		}
	}
	for role := range roles {
		facts.DeploymentRole = append(facts.DeploymentRole, role)
	}
	sort.Strings(facts.DeploymentRole)
	environmentNames := map[string]bool{}
	for index, environment := range objectSlice(value["environmentVariables"]) {
		name := stringValue(environment["name"])
		if environmentNames[name] {
			findings = append(findings, blocker("deployment_contract_duplicate_environment", fmt.Sprintf("$.environmentVariables[%d].name", index), "Environment variable name is duplicated."))
		}
		environmentNames[name] = true
	}
	return facts, findings
}

func inspectVerification(value map[string]any) (Facts, []Finding) {
	facts := emptyFacts(KindVerification)
	findings := []Finding{}
	seen := map[string]bool{}
	for index, oracle := range objectSlice(value["oracles"]) {
		id := stringValue(oracle["id"])
		if seen[id] {
			findings = append(findings, blocker("verification_contract_duplicate_oracle", fmt.Sprintf("$.oracles[%d].id", index), "Oracle ID is duplicated."))
		}
		seen[id] = true
		criteria := stringSlice(oracle["acceptanceCriterionIds"])
		sort.Strings(criteria)
		facts.Oracles = append(facts.Oracles, Oracle{
			ID: id, AcceptanceCriterionIDs: criteria, Kind: stringValue(oracle["kind"]),
			Target: stringValue(oracle["target"]), CommandID: stringValue(oracle["commandId"]),
		})
	}
	sort.Slice(facts.Oracles, func(i, j int) bool { return facts.Oracles[i].ID < facts.Oracles[j].ID })
	return facts, findings
}

func rejectExternalRefs(value any, path string) []Finding {
	findings := []Finding{}
	switch typed := value.(type) {
	case map[string]any:
		keys := sortedMapKeys(typed)
		for _, key := range keys {
			childPath := path + "." + key
			if key == "$ref" {
				reference, _ := typed[key].(string)
				if !strings.HasPrefix(reference, "#/") {
					findings = append(findings, blocker("api_contract_external_reference", childPath, "Only document-local OpenAPI references are allowed."))
				}
				continue
			}
			findings = append(findings, rejectExternalRefs(typed[key], childPath)...)
		}
	case []any:
		for index, child := range typed {
			findings = append(findings, rejectExternalRefs(child, fmt.Sprintf("%s[%d]", path, index))...)
		}
	}
	return findings
}

func validateLocalAPIReferences(root map[string]any) []Finding {
	findings := []Finding{}
	var walk func(any, string)
	walk = func(value any, path string) {
		switch typed := value.(type) {
		case map[string]any:
			for _, key := range sortedMapKeys(typed) {
				childPath := path + "." + key
				if key == "$ref" {
					reference := stringValue(typed[key])
					if strings.HasPrefix(reference, "#/") && !localJSONPointerExists(root, reference) {
						findings = append(findings, blocker("api_contract_dangling_reference", childPath, "OpenAPI local reference does not resolve to an object in the same document."))
					}
					continue
				}
				walk(typed[key], childPath)
			}
		case []any:
			for index, child := range typed {
				walk(child, fmt.Sprintf("%s[%d]", path, index))
			}
		}
	}
	walk(root, "$")
	return findings
}

func validateAPIResponseReferences(root map[string]any) []Finding {
	findings := []Finding{}
	paths, _ := root["paths"].(map[string]any)
	for _, pathName := range sortedMapKeys(paths) {
		rawPath := paths[pathName]
		pathItem, _ := rawPath.(map[string]any)
		for _, method := range []string{"delete", "get", "head", "options", "patch", "post", "put"} {
			operation, _ := pathItem[method].(map[string]any)
			responses, _ := operation["responses"].(map[string]any)
			for _, status := range sortedMapKeys(responses) {
				rawResponse := responses[status]
				response, _ := rawResponse.(map[string]any)
				reference := stringValue(response["$ref"])
				if reference != "" && !strings.HasPrefix(reference, "#/components/responses/") {
					findings = append(findings, blocker(
						"api_contract_response_reference_type",
						"$.paths["+pathName+"]["+method+"].responses["+status+"].$ref",
						"OpenAPI response references must target a component Response Object, not a Schema Object.",
					))
				}
			}
		}
	}
	return findings
}

func localJSONPointerExists(root any, reference string) bool {
	current := root
	for _, encoded := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		segment := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch typed := current.(type) {
		case map[string]any:
			var exists bool
			current, exists = typed[segment]
			if !exists {
				return false
			}
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(typed) {
				return false
			}
			current = typed[index]
		default:
			return false
		}
	}
	return true
}

func containsField(entity map[string]any, target string) bool {
	for _, field := range objectSlice(entity["fields"]) {
		if stringValue(field["id"]) == target {
			return true
		}
	}
	return false
}

func dataFieldsByID(entity map[string]any) map[string]map[string]any {
	result := map[string]map[string]any{}
	for _, field := range objectSlice(entity["fields"]) {
		result[stringValue(field["id"])] = field
	}
	return result
}

func dataEntityHasCandidateKey(entity map[string]any, fieldIDs []string) bool {
	if sameOrderedContractStrings(stringSlice(entity["primaryKey"]), fieldIDs) {
		return true
	}
	for _, index := range objectSlice(entity["indexes"]) {
		if booleanValue(index["unique"]) && sameOrderedContractStrings(stringSlice(index["fieldIds"]), fieldIDs) {
			return true
		}
	}
	return false
}

func dataForeignKeyPreservesTenant(source, target map[string]any, sourceFields, targetFields []string) bool {
	sourceTenant, _ := source["tenantScope"].(map[string]any)
	targetTenant, _ := target["tenantScope"].(map[string]any)
	sourceMode, targetMode := stringValue(sourceTenant["mode"]), stringValue(targetTenant["mode"])
	if sourceMode == "global" || targetMode == "global" {
		return true
	}
	if sourceMode != targetMode {
		return false
	}
	sourceTenantField, targetTenantField := stringValue(sourceTenant["fieldId"]), stringValue(targetTenant["fieldId"])
	for index := range sourceFields {
		if sourceFields[index] == sourceTenantField && targetFields[index] == targetTenantField {
			return true
		}
	}
	return false
}

func dataEntitiesShareTenantScope(source, target map[string]any) bool {
	sourceTenant, _ := source["tenantScope"].(map[string]any)
	targetTenant, _ := target["tenantScope"].(map[string]any)
	sourceMode, targetMode := stringValue(sourceTenant["mode"]), stringValue(targetTenant["mode"])
	return sourceMode != "global" && sourceMode == targetMode
}

func sameOrderedContractStrings(left, right []string) bool {
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

func objectSlice(value any) []map[string]any {
	values, _ := value.([]any)
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if object, ok := value.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

func stringSlice(value any) []string {
	values, _ := value.([]any)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func int64Value(value any) int64 {
	number, _ := value.(json.Number)
	parsed, _ := number.Int64()
	return parsed
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func safeRelativePath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func blocker(code, path, message string) Finding {
	return Finding{Code: code, Path: path, Message: message, Severity: "blocker"}
}
