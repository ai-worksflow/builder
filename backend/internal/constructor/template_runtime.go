package constructor

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

type expectedTemplateService struct {
	ID          string
	Role        string
	SourceRoot  string
	Port        TemplateRuntimePort
	Health      TemplateRuntimeHealthCheck
	BuildOutput string
}

type templateEnvironmentDeclaration struct {
	Name     string
	Required bool
	Secret   bool
	Scope    string
	Source   string
}

// validateTemplateDeploymentClosure compares only facts represented by both
// TemplateManifest v1 and Deployment Contract v1. Representation gaps are
// blockers, never inferred defaults: v1 supports exactly one service, port,
// health check and build output per selected component, plus at most one stack
// migration. Layout remains an exact hash-committed Template fact but has no
// Deployment v1 equality target.
func validateTemplateDeploymentClosure(
	template FullStackTemplateRef,
	releases []TemplateReleaseRef,
	runtime *TemplateRuntimeFacts,
	deploymentSource PinnedBuildSource,
	diagnostics *diagnosticBuilder,
) {
	if runtime == nil {
		diagnostics.gap(
			"template-runtime", "template_runtime_facts_missing", "$.templateRuntime",
			"Compiler requires trusted runtime facts from the exact selected FullStackTemplate and Template Releases.",
			template.ID, nil,
		)
		return
	}
	if strings.TrimSpace(runtime.FullStackTemplateID) != template.ID ||
		strings.TrimSpace(runtime.FullStackTemplateHash) != template.ContentHash {
		diagnostics.gap(
			"template-runtime", "template_runtime_identity_mismatch", "$.templateRuntime",
			"Template runtime facts do not bind the exact selected FullStackTemplate identity and hash.",
			template.ID, nil,
		)
	}
	validateTemplateRuntimeLayout(runtime.Layout, template.ID, diagnostics)

	releaseByRole := make(map[string]TemplateReleaseRef, len(releases))
	for _, release := range releases {
		releaseByRole[release.Role] = release
	}
	components := append([]TemplateRuntimeComponent(nil), runtime.Components...)
	sort.Slice(components, func(i, j int) bool {
		left := templateRuntimeComponentOrderKey(components[i])
		right := templateRuntimeComponentOrderKey(components[j])
		return left < right
	})
	if len(components) != len(releases) {
		diagnostics.gap(
			"template-runtime", "template_runtime_component_set_mismatch", "$.templateRuntime.components",
			"Template runtime component set must exactly match selected TemplateRelease refs.",
			template.ID, nil,
		)
	}

	expectedServices := map[string]expectedTemplateService{}
	expectedPorts := map[string]TemplateRuntimePort{}
	expectedHealth := map[string]TemplateRuntimeHealthCheck{}
	portNumbers := map[int]string{}
	environments := map[string]templateEnvironmentDeclaration{}
	migrations := []TemplateRuntimeMigration{}
	seenComponents := map[string]bool{}
	for componentIndex, component := range components {
		componentPath := fmt.Sprintf("$.templateRuntime.components[%d]", componentIndex)
		role := strings.TrimSpace(component.Role)
		release := releaseByRole[role]
		componentKey := role + "\x00" + strings.TrimSpace(component.ReleaseID)
		if seenComponents[componentKey] || release.ID == "" || release.ID != strings.TrimSpace(component.ReleaseID) ||
			release.ReleaseHash != strings.TrimSpace(component.ReleaseHash) {
			diagnostics.gap(
				"template-runtime", "template_runtime_component_set_mismatch", componentPath,
				"Template runtime component must bind one exact selected release ID, hash, and role.",
				strings.TrimSpace(component.ReleaseID), nil,
			)
		}
		seenComponents[componentKey] = true
		if strings.TrimSpace(component.ManifestSchemaVersion) != "template-manifest/v1" {
			diagnostics.gap(
				"template-runtime", "template_runtime_manifest_version", componentPath+".manifestSchemaVersion",
				"Deployment v1 bridge requires template-manifest/v1 runtime facts.",
				strings.TrimSpace(component.ReleaseID), nil,
			)
		}
		mount, mountOK := normalizedRuntimePath(component.MountPath, false)
		if !mountOK {
			diagnostics.gap(
				"template-runtime", "template_runtime_mount_invalid", componentPath+".mountPath",
				"Template component mount path must be one normalized relative path.",
				strings.TrimSpace(component.ReleaseID), nil,
			)
			continue
		}
		if len(component.Services) != 1 || strings.TrimSpace(component.Services[0].Role) != role {
			diagnostics.gap(
				"template-runtime", "template_runtime_component_role_ambiguous", componentPath+".services",
				"Deployment v1 bridge requires exactly one service whose kind equals the FullStack component role.",
				strings.TrimSpace(component.ReleaseID), nil,
			)
			continue
		}
		service := component.Services[0]
		serviceID := strings.TrimSpace(service.ID)
		root, rootOK := joinedRuntimePath(mount, service.RootPath, true)
		if serviceID == "" || !rootOK {
			diagnostics.gap(
				"template-runtime", "template_runtime_service_invalid", componentPath+".services",
				"Template runtime service requires a stable ID and canonical mounted source root.",
				strings.TrimSpace(component.ReleaseID), nil,
			)
			continue
		}
		if _, duplicate := expectedServices[serviceID]; duplicate {
			diagnostics.gap(
				"template-runtime", "template_runtime_service_collision", componentPath+".services",
				"Selected Template Releases contain a cross-release service ID collision.",
				serviceID, nil,
			)
			continue
		}
		if len(component.Ports) != 1 || len(component.HealthChecks) != 1 || len(component.BuildOutputs) != 1 {
			diagnostics.gap(
				"template-runtime", "template_runtime_v1_cardinality_unrepresentable", componentPath,
				"Deployment v1 can bridge exactly one port, health check, and build output per Template service.",
				serviceID, nil,
			)
			continue
		}
		portFact := component.Ports[0]
		portFact.Name = strings.TrimSpace(portFact.Name)
		portFact.ServiceID = strings.TrimSpace(portFact.ServiceID)
		portFact.Protocol = strings.TrimSpace(portFact.Protocol)
		if portFact.Name == "" || portFact.ServiceID != serviceID || portFact.Number < 1024 || portFact.Number > 65535 {
			diagnostics.gap(
				"template-runtime", "template_runtime_port_invalid", componentPath+".ports",
				"Template port must bind this exact service with a stable name and valid number.",
				serviceID, nil,
			)
			continue
		}
		if portFact.Protocol != "http" && portFact.Protocol != "tcp" {
			diagnostics.gap(
				"template-runtime", "template_runtime_protocol_unrepresentable", componentPath+".ports",
				"Deployment Contract v1 cannot represent this exact Template port protocol.",
				portFact.Name, nil,
			)
			continue
		}
		if prior, duplicate := expectedPorts[portFact.Name]; duplicate {
			diagnostics.gap("template-runtime", "template_runtime_port_collision", componentPath+".ports", "Selected Template Releases contain a cross-release port-name collision.", prior.Name, nil)
			continue
		}
		if prior, duplicate := portNumbers[portFact.Number]; duplicate {
			diagnostics.gap("template-runtime", "template_runtime_port_collision", componentPath+".ports", "Selected Template Releases contain a cross-release port-number collision.", prior, nil)
			continue
		}
		healthFact := component.HealthChecks[0]
		healthFact.ID = strings.TrimSpace(healthFact.ID)
		healthFact.ServiceID = strings.TrimSpace(healthFact.ServiceID)
		healthFact.PortName, healthFact.Path = strings.TrimSpace(healthFact.PortName), strings.TrimSpace(healthFact.Path)
		if healthFact.ID == "" || healthFact.ServiceID != serviceID || healthFact.PortName != portFact.Name || !validRuntimeHealthPath(healthFact.Path) {
			diagnostics.gap(
				"template-runtime", "template_runtime_health_invalid", componentPath+".healthChecks",
				"Template health check must bind the exact service port and normalized local path.",
				healthFact.ID, nil,
			)
			continue
		}
		if _, duplicate := expectedHealth[healthFact.ID]; duplicate {
			diagnostics.gap("template-runtime", "template_runtime_health_collision", componentPath+".healthChecks", "Selected Template Releases contain a cross-release health-check ID collision.", healthFact.ID, nil)
			continue
		}
		outputFact := component.BuildOutputs[0]
		output, outputOK := joinedRuntimePath(mount, outputFact.Path, false)
		if strings.TrimSpace(outputFact.ServiceID) != serviceID || !outputOK {
			diagnostics.gap("template-runtime", "template_runtime_build_output_invalid", componentPath+".buildOutputs", "Template build output must resolve to one canonical mounted path.", serviceID, nil)
			continue
		}
		expectedPorts[portFact.Name] = portFact
		portNumbers[portFact.Number] = portFact.Name
		expectedHealth[healthFact.ID] = healthFact
		expectedServices[serviceID] = expectedTemplateService{
			ID: serviceID, Role: role, SourceRoot: root, Port: portFact,
			Health: healthFact, BuildOutput: output,
		}

		commandSet := stringSet(component.Commands)
		if component.Migration != nil {
			migration := *component.Migration
			migration.ServiceID, migration.CommandName = strings.TrimSpace(migration.ServiceID), strings.TrimSpace(migration.CommandName)
			if migration.ServiceID != serviceID || migration.CommandName == "" || !commandSet[migration.CommandName] {
				diagnostics.gap("template-runtime", "template_runtime_migration_invalid", componentPath+".migration", "Template migration must reference this component service and a declared argv command.", component.ReleaseID, nil)
			} else {
				migrations = append(migrations, migration)
			}
		}
		for _, environment := range component.EnvironmentVariables {
			declaration := templateEnvironmentDeclaration{
				Name: strings.TrimSpace(environment.Name), Required: environment.Required, Secret: environment.Secret,
				Scope: strings.TrimSpace(environment.Scope), Source: component.ReleaseID,
			}
			if declaration.Name == "" || declaration.Scope != templateEnvironmentScope(role) {
				diagnostics.gap("template-runtime", "template_runtime_environment_scope_ambiguous", componentPath+".environmentVariables", "Template environment scope must be derived from one unambiguous component role.", component.ReleaseID, nil)
				continue
			}
			if prior, duplicate := environments[declaration.Name]; duplicate {
				diagnostics.gap("template-runtime", "template_runtime_environment_collision", componentPath+".environmentVariables", "Selected Template Releases contain a cross-role environment variable collision.", prior.Source, nil)
				continue
			}
			environments[declaration.Name] = declaration
		}
	}

	var deployment map[string]any
	if err := json.Unmarshal(deploymentSource.Content, &deployment); err != nil {
		return
	}
	validateDeploymentServicesAgainstTemplate(expectedServices, deployment, deploymentSource.Ref.RevisionID, diagnostics)
	validateDeploymentPortsAgainstTemplate(expectedPorts, deployment, deploymentSource.Ref.RevisionID, diagnostics)
	validateDeploymentHealthAgainstTemplate(expectedHealth, deployment, deploymentSource.Ref.RevisionID, diagnostics)
	validateDeploymentMigrationAgainstTemplate(migrations, deployment, deploymentSource.Ref.RevisionID, diagnostics)
	validateDeploymentEnvironmentAgainstTemplate(environments, deployment, deploymentSource.Ref.RevisionID, diagnostics)
}

// templateRuntimeComponentOrderKey keeps fail-closed diagnostics stable even
// for invalid Registry input containing duplicate release identities. The
// identity prefix preserves the normal role/release ordering; the complete
// immutable projection is a deterministic tie-breaker for duplicate entries.
func templateRuntimeComponentOrderKey(component TemplateRuntimeComponent) string {
	content, _ := json.Marshal(component)
	return strings.TrimSpace(component.Role) + "\x00" +
		strings.TrimSpace(component.ReleaseID) + "\x00" +
		strings.TrimSpace(component.ReleaseHash) + "\x00" + string(content)
}

func validateTemplateRuntimeLayout(layout TemplateRuntimeLayout, sourceID string, diagnostics *diagnosticBuilder) {
	valid := strings.TrimSpace(layout.ContractTruthSource) == "openapi" && strings.TrimSpace(layout.DatabaseEngine) == "postgresql"
	for _, value := range []string{layout.OpenAPIPath, layout.GeneratedClientPath, layout.DeploymentPath, layout.TestPath} {
		_, pathValid := normalizedRuntimePath(value, false)
		valid = valid && pathValid
	}
	if !valid {
		diagnostics.gap(
			"template-runtime", "template_runtime_layout_invalid", "$.templateRuntime.layout",
			"Template runtime layout must preserve canonical OpenAPI/PostgreSQL truth and normalized generated paths.",
			sourceID, nil,
		)
	}
}

func validateDeploymentServicesAgainstTemplate(expected map[string]expectedTemplateService, deployment map[string]any, sourceID string, diagnostics *diagnosticBuilder) {
	actual := objectSlice(deployment["services"])
	seen := map[string]bool{}
	if len(actual) != len(expected) {
		diagnostics.gap("template-runtime", "template_deployment_service_set_drift", "$.services", "Deployment services must exactly match the representable selected Template services.", sourceID, nil)
	}
	for index, service := range actual {
		id := stringField(service, "id")
		templateService, exists := expected[id]
		seen[id] = true
		if !exists {
			diagnostics.gap("template-runtime", "template_deployment_service_set_drift", fmt.Sprintf("$.services[%d]", index), "Deployment contains a service outside the selected Template runtime facts.", sourceID, nil)
			continue
		}
		if stringField(service, "role") != templateService.Role || stringField(service, "sourceRoot") != templateService.SourceRoot {
			diagnostics.gap("template-runtime", "template_deployment_service_drift", fmt.Sprintf("$.services[%d]", index), "Deployment service role or mounted source root drifts from the selected Template.", sourceID, nil)
		}
		if stringField(service, "portName") != templateService.Port.Name {
			diagnostics.gap("template-runtime", "template_deployment_service_port_drift", fmt.Sprintf("$.services[%d].portName", index), "Deployment service port drifts from the selected Template.", sourceID, nil)
		}
		if stringField(service, "healthCheckId") != templateService.Health.ID {
			diagnostics.gap("template-runtime", "template_deployment_service_health_drift", fmt.Sprintf("$.services[%d].healthCheckId", index), "Deployment service health check drifts from the selected Template.", sourceID, nil)
		}
		if stringField(service, "buildOutput") != templateService.BuildOutput {
			diagnostics.gap("template-runtime", "template_deployment_build_output_drift", fmt.Sprintf("$.services[%d].buildOutput", index), "Deployment service build output drifts from the mounted Template output.", sourceID, nil)
		}
	}
	for id := range expected {
		if !seen[id] {
			diagnostics.gap("template-runtime", "template_deployment_service_set_drift", "$.services", "Deployment omits a selected Template service.", id, nil)
		}
	}
}

func validateDeploymentPortsAgainstTemplate(expected map[string]TemplateRuntimePort, deployment map[string]any, sourceID string, diagnostics *diagnosticBuilder) {
	actual := objectSlice(deployment["ports"])
	seen := map[string]bool{}
	if len(actual) != len(expected) {
		diagnostics.gap("template-runtime", "template_deployment_port_set_drift", "$.ports", "Deployment ports must exactly match selected Template ports.", sourceID, nil)
	}
	for index, port := range actual {
		name := stringField(port, "name")
		expectedPort, exists := expected[name]
		seen[name] = true
		number, numberOK := exactRuntimeInteger(port["containerPort"])
		if !exists || !numberOK || number != expectedPort.Number || stringField(port, "protocol") != expectedPort.Protocol {
			diagnostics.gap("template-runtime", "template_deployment_port_drift", fmt.Sprintf("$.ports[%d]", index), "Deployment port name, number, or protocol drifts from the selected Template.", sourceID, nil)
		}
	}
	for name := range expected {
		if !seen[name] {
			diagnostics.gap("template-runtime", "template_deployment_port_set_drift", "$.ports", "Deployment omits a selected Template port.", name, nil)
		}
	}
}

func validateDeploymentHealthAgainstTemplate(expected map[string]TemplateRuntimeHealthCheck, deployment map[string]any, sourceID string, diagnostics *diagnosticBuilder) {
	actual := objectSlice(deployment["healthChecks"])
	seen := map[string]bool{}
	if len(actual) != len(expected) {
		diagnostics.gap("template-runtime", "template_deployment_health_set_drift", "$.healthChecks", "Deployment health checks must exactly match selected Template health identities and paths.", sourceID, nil)
	}
	for index, health := range actual {
		id := stringField(health, "id")
		expectedHealth, exists := expected[id]
		seen[id] = true
		if !exists || stringField(health, "portName") != expectedHealth.PortName || stringField(health, "path") != expectedHealth.Path {
			diagnostics.gap("template-runtime", "template_deployment_health_drift", fmt.Sprintf("$.healthChecks[%d]", index), "Deployment health ID, port, or path drifts from the selected Template; method/status remain Deployment-local v1 facts.", sourceID, nil)
		}
	}
	for id := range expected {
		if !seen[id] {
			diagnostics.gap("template-runtime", "template_deployment_health_set_drift", "$.healthChecks", "Deployment omits a selected Template health check.", id, nil)
		}
	}
}

func validateDeploymentMigrationAgainstTemplate(migrations []TemplateRuntimeMigration, deployment map[string]any, sourceID string, diagnostics *diagnosticBuilder) {
	migration, _ := deployment["migration"].(map[string]any)
	required, _ := migration["required"].(bool)
	if len(migrations) > 1 {
		diagnostics.gap("template-runtime", "template_runtime_migration_unrepresentable", "$.migration", "Deployment v1 cannot represent migrations from more than one selected Template component.", sourceID, nil)
		return
	}
	if required != (len(migrations) == 1) {
		diagnostics.gap("template-runtime", "template_deployment_migration_drift", "$.migration", "Deployment migration requirement must exactly match the selected Template migration declaration.", sourceID, nil)
		return
	}
	if required && stringField(migration, "commandId") != migrations[0].CommandName {
		diagnostics.gap("template-runtime", "template_deployment_migration_drift", "$.migration", "Required Deployment migration must match the single exact selected Template migration command.", sourceID, nil)
	}
}

func validateDeploymentEnvironmentAgainstTemplate(expected map[string]templateEnvironmentDeclaration, deployment map[string]any, sourceID string, diagnostics *diagnosticBuilder) {
	actual := map[string]map[string]any{}
	for _, environment := range objectSlice(deployment["environmentVariables"]) {
		actual[stringField(environment, "name")] = environment
	}
	for name, declaration := range expected {
		environment, declared := actual[name]
		if !declared {
			if declaration.Required {
				diagnostics.gap("template-runtime", "template_deployment_environment_missing", "$.environmentVariables", "Deployment omits a required selected Template environment variable.", name, nil)
			}
			continue
		}
		required, _ := environment["required"].(bool)
		secret, _ := environment["secret"].(bool)
		if (declaration.Required && !required) || declaration.Secret != secret || stringField(environment, "scope") != declaration.Scope {
			diagnostics.gap("template-runtime", "template_deployment_environment_drift", "$.environmentVariables[name="+name+"]", "Deployment weakens or changes a selected Template environment declaration.", sourceID, nil)
		}
	}
}

func normalizedRuntimePath(value string, allowDot bool) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "." && allowDot {
		return value, true
	}
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || path.Clean(value) != value {
		return "", false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", false
		}
	}
	return value, true
}

func joinedRuntimePath(mount, relative string, relativeMayBeDot bool) (string, bool) {
	mount, mountOK := normalizedRuntimePath(mount, false)
	relative, relativeOK := normalizedRuntimePath(relative, relativeMayBeDot)
	if !mountOK || !relativeOK {
		return "", false
	}
	if relative == "." {
		return mount, true
	}
	return normalizedRuntimePath(mount+"/"+relative, false)
}

func validRuntimeHealthPath(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.Contains(value, "..") && !strings.ContainsAny(value, "?#\r\n")
}

func exactRuntimeInteger(value any) (int, bool) {
	switch number := value.(type) {
	case float64:
		converted := int(number)
		return converted, float64(converted) == number
	case json.Number:
		parsed, err := number.Int64()
		return int(parsed), err == nil && int64(int(parsed)) == parsed
	case int:
		return number, true
	default:
		return 0, false
	}
}
