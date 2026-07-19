package templates

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

var (
	slugPattern            = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	versionPattern         = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)
	gitCommitPattern       = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	digestPattern          = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	environmentNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
	spdxIdentifierPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+-]*$`)
)

var requiredCommandNames = []string{"build", "install", "lint", "start", "test", "typecheck"}

func validateUUID(value, field string) error {
	if _, err := uuid.Parse(strings.TrimSpace(value)); err != nil {
		return invalid("invalid_uuid", field, "must be a UUID")
	}
	return nil
}

func validateDigest(value, field string) error {
	if !digestPattern.MatchString(value) {
		return invalid("invalid_digest", field, "must be a lowercase sha256 digest")
	}
	return nil
}

func canonicalHash(value any) (string, error) {
	digest, err := domain.CanonicalHash(value)
	if err != nil {
		return "", invalid("canonicalization_failed", "document", err.Error())
	}
	return "sha256:" + digest, nil
}

func normalizeCandidate(candidate AdmissionCandidate) (AdmissionCandidate, error) {
	source, err := normalizeSource(candidate.Source)
	if err != nil {
		return AdmissionCandidate{}, err
	}
	manifest, err := normalizeManifest(candidate.Manifest)
	if err != nil {
		return AdmissionCandidate{}, err
	}
	candidate.Source = source
	candidate.Manifest = manifest
	candidate.SBOMDigest = strings.TrimSpace(candidate.SBOMDigest)
	candidate.LicenseExpression = strings.TrimSpace(candidate.LicenseExpression)
	candidate.LicenseDigest = strings.TrimSpace(candidate.LicenseDigest)
	if err := validateDigest(candidate.SBOMDigest, "candidate.sbomDigest"); err != nil {
		return AdmissionCandidate{}, err
	}
	if err := validateSPDXExpression(candidate.LicenseExpression); err != nil {
		return AdmissionCandidate{}, err
	}
	if err := validateDigest(candidate.LicenseDigest, "candidate.licenseDigest"); err != nil {
		return AdmissionCandidate{}, err
	}
	return candidate, nil
}

func normalizeSource(source TemplateSource) (TemplateSource, error) {
	repository, err := normalizeRepositoryURL(source.Repository)
	if err != nil {
		return TemplateSource{}, err
	}
	branch := strings.TrimSpace(source.Branch)
	if err := validateGitBranch(branch); err != nil {
		return TemplateSource{}, err
	}
	commit := strings.TrimSpace(source.Commit)
	if !gitCommitPattern.MatchString(commit) {
		return TemplateSource{}, invalid("source_not_pinned", "source.commit", "must be an exact lowercase Git object ID, not a branch or tag")
	}
	treeHash := strings.TrimSpace(source.TreeHash)
	if err := validateDigest(treeHash, "source.treeHash"); err != nil {
		return TemplateSource{}, err
	}
	return TemplateSource{Repository: repository, Branch: branch, Commit: commit, TreeHash: treeHash}, nil
}

func normalizeRepositoryURL(raw string) (string, error) {
	if len(raw) > 2048 {
		return "", invalid("invalid_repository", "source.repository", "must be at most 2048 characters")
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" {
		return "", invalid("invalid_repository", "source.repository", "must be an absolute HTTPS Git URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Port() != "" {
		return "", invalid("unsafe_repository", "source.repository", "credentials, query strings, fragments, and explicit ports are forbidden")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") {
		return "", invalid("unsafe_repository", "source.repository", "host must be a public DNS name")
	}
	if address, parseErr := netip.ParseAddr(host); parseErr == nil {
		if address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || !address.IsGlobalUnicast() {
			return "", invalid("unsafe_repository", "source.repository", "private, loopback, and link-local addresses are forbidden")
		}
		return "", invalid("unsafe_repository", "source.repository", "literal IP addresses are forbidden")
	}
	if net.ParseIP(host) != nil {
		return "", invalid("unsafe_repository", "source.repository", "literal IP addresses are forbidden")
	}
	if !strings.HasSuffix(parsed.EscapedPath(), ".git") || parsed.Path == "/.git" || path.Clean(parsed.Path) != parsed.Path {
		return "", invalid("invalid_repository", "source.repository", "path must identify an exact .git repository")
	}
	parsed.Host = host
	return parsed.String(), nil
}

func validateGitBranch(value string) error {
	segments := strings.Split(value, "/")
	invalidBranch := value == "" || len(value) > 240 || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") ||
		strings.Contains(value, "@{") || strings.Contains(value, `\`) || strings.ContainsAny(value, " ~^:?*[")
	for _, segment := range segments {
		invalidBranch = invalidBranch || segment == "" || segment == "." || segment == ".." || strings.HasSuffix(segment, ".lock")
	}
	if invalidBranch {
		return invalid("invalid_branch", "source.branch", "must be a safe human-readable Git branch name")
	}
	return nil
}

func normalizeManifest(manifest TemplateManifest) (TemplateManifest, error) {
	manifest.SchemaVersion = strings.TrimSpace(manifest.SchemaVersion)
	if manifest.SchemaVersion != TemplateManifestSchemaVersion {
		return TemplateManifest{}, &Error{Kind: ErrUnsupportedSchema, Code: "unsupported_manifest_schema", Field: "manifest.schemaVersion", Detail: "must be template-manifest/v1"}
	}
	manifest.TemplateID = strings.TrimSpace(manifest.TemplateID)
	if !slugPattern.MatchString(manifest.TemplateID) || len(manifest.TemplateID) > 120 {
		return TemplateManifest{}, invalid("invalid_template_id", "manifest.templateId", "must be a lowercase kebab-case identifier")
	}
	manifest.DisplayName = strings.TrimSpace(manifest.DisplayName)
	if manifest.DisplayName == "" || len(manifest.DisplayName) > 160 || !utf8.ValidString(manifest.DisplayName) {
		return TemplateManifest{}, invalid("invalid_display_name", "manifest.displayName", "must be valid UTF-8 between 1 and 160 bytes")
	}
	manifest.Version = strings.TrimSpace(manifest.Version)
	if !versionPattern.MatchString(manifest.Version) || len(manifest.Version) > 80 {
		return TemplateManifest{}, invalid("invalid_version", "manifest.version", "must be an explicit semantic version")
	}
	if len(manifest.Services) == 0 || len(manifest.Services) > 16 {
		return TemplateManifest{}, invalid("invalid_services", "manifest.services", "must contain between 1 and 16 services")
	}
	if len(manifest.Toolchains) == 0 || len(manifest.Toolchains) > 16 {
		return TemplateManifest{}, invalid("invalid_toolchains", "manifest.toolchains", "must contain between 1 and 16 pinned toolchains")
	}
	if len(manifest.Commands) == 0 || len(manifest.Commands) > 32 {
		return TemplateManifest{}, invalid("invalid_commands", "manifest.commands", "must contain declared argv commands")
	}
	if len(manifest.Ports) == 0 || len(manifest.Ports) > 32 {
		return TemplateManifest{}, invalid("invalid_ports", "manifest.ports", "must declare bounded service ports")
	}
	if len(manifest.HealthChecks) == 0 || len(manifest.HealthChecks) > 32 {
		return TemplateManifest{}, invalid("invalid_health_checks", "manifest.healthChecks", "must declare service health checks")
	}
	if len(manifest.BuildOutputs) == 0 || len(manifest.BuildOutputs) > 32 {
		return TemplateManifest{}, invalid("invalid_build_outputs", "manifest.buildOutputs", "must declare bounded build outputs")
	}
	if len(manifest.ExtensionPaths) == 0 || len(manifest.ExtensionPaths) > 64 || len(manifest.ProtectedPaths) == 0 || len(manifest.ProtectedPaths) > 64 {
		return TemplateManifest{}, invalid("invalid_path_policy", "manifest.extensionPaths", "extensionPaths and protectedPaths must both be non-empty and bounded")
	}
	if len(manifest.EnvironmentSchema) > 128 {
		return TemplateManifest{}, invalid("invalid_environment_schema", "manifest.environmentSchema", "must contain at most 128 entries")
	}
	if len(manifest.Lockfiles) == 0 || len(manifest.Lockfiles) > 32 {
		return TemplateManifest{}, invalid("missing_lockfiles", "manifest.lockfiles", "at least one content-pinned dependency lockfile is required")
	}

	services := make(map[string]TemplateService, len(manifest.Services))
	serviceRoots := map[string]string{}
	for index, service := range manifest.Services {
		field := fmt.Sprintf("manifest.services[%d]", index)
		service.ID = strings.TrimSpace(service.ID)
		service.Kind = strings.TrimSpace(service.Kind)
		if !slugPattern.MatchString(service.ID) || len(service.ID) > 80 {
			return TemplateManifest{}, invalid("invalid_service", field+".id", "must be a lowercase kebab-case identifier")
		}
		if service.Kind != "web" && service.Kind != "api" && service.Kind != "worker" {
			return TemplateManifest{}, invalid("invalid_service", field+".kind", "must be web, api, or worker")
		}
		root, err := normalizeRelativePath(service.RootPath, true)
		if err != nil {
			return TemplateManifest{}, invalid("invalid_service", field+".rootPath", err.Error())
		}
		service.RootPath = root
		if _, exists := services[service.ID]; exists {
			return TemplateManifest{}, invalid("duplicate_service", field+".id", "service IDs must be unique")
		}
		if prior, exists := serviceRoots[strings.ToLower(root)]; exists {
			return TemplateManifest{}, invalid("duplicate_service_root", field+".rootPath", "duplicates service "+prior)
		}
		services[service.ID] = service
		serviceRoots[strings.ToLower(root)] = service.ID
		manifest.Services[index] = service
	}
	sort.Slice(manifest.Services, func(i, j int) bool { return manifest.Services[i].ID < manifest.Services[j].ID })

	languageServers, err := normalizeLanguageServerProfiles(manifest.LanguageServers, services)
	if err != nil {
		return TemplateManifest{}, err
	}
	manifest.LanguageServers = languageServers

	toolchainNames := map[string]bool{}
	for index, toolchain := range manifest.Toolchains {
		field := fmt.Sprintf("manifest.toolchains[%d]", index)
		toolchain.Name = strings.TrimSpace(toolchain.Name)
		toolchain.Version = strings.TrimSpace(toolchain.Version)
		toolchain.Image = strings.TrimSpace(toolchain.Image)
		if !slugPattern.MatchString(toolchain.Name) || toolchain.Version == "" || len(toolchain.Version) > 120 {
			return TemplateManifest{}, invalid("invalid_toolchain", field, "requires a unique name and explicit version")
		}
		imageParts := strings.Split(toolchain.Image, "@sha256:")
		if len(imageParts) != 2 || strings.TrimSpace(imageParts[0]) == "" || !digestPattern.MatchString("sha256:"+imageParts[1]) {
			return TemplateManifest{}, invalid("unpinned_toolchain", field+".image", "must be an OCI image reference pinned by sha256 digest")
		}
		if strings.ContainsAny(toolchain.Image, " \t\r\n") {
			return TemplateManifest{}, invalid("invalid_toolchain", field+".image", "must not contain whitespace")
		}
		if toolchainNames[toolchain.Name] {
			return TemplateManifest{}, invalid("duplicate_toolchain", field+".name", "toolchain names must be unique")
		}
		toolchainNames[toolchain.Name] = true
		manifest.Toolchains[index] = toolchain
	}
	sort.Slice(manifest.Toolchains, func(i, j int) bool { return manifest.Toolchains[i].Name < manifest.Toolchains[j].Name })

	commands := make(map[string]Command, len(manifest.Commands))
	for name, command := range manifest.Commands {
		name = strings.TrimSpace(name)
		if !slugPattern.MatchString(name) || len(name) > 80 {
			return TemplateManifest{}, invalid("invalid_command", "manifest.commands", "command names must be lowercase kebab-case")
		}
		normalized, err := normalizeCommand(command, "manifest.commands."+name)
		if err != nil {
			return TemplateManifest{}, err
		}
		if _, exists := commands[name]; exists {
			return TemplateManifest{}, invalid("duplicate_command", "manifest.commands."+name, "command names must be unique after trimming")
		}
		commands[name] = normalized
	}
	for _, required := range requiredCommandNames {
		if _, ok := commands[required]; !ok {
			return TemplateManifest{}, invalid("missing_command", "manifest.commands."+required, "required admission command is not declared")
		}
	}
	manifest.Commands = commands

	portNames, portNumbers := map[string]Port{}, map[int]string{}
	for index, port := range manifest.Ports {
		field := fmt.Sprintf("manifest.ports[%d]", index)
		port.Name = strings.TrimSpace(port.Name)
		port.ServiceID = strings.TrimSpace(port.ServiceID)
		port.Protocol = strings.TrimSpace(port.Protocol)
		port.Exposure = strings.TrimSpace(port.Exposure)
		if !slugPattern.MatchString(port.Name) || services[port.ServiceID].ID == "" {
			return TemplateManifest{}, invalid("invalid_port", field, "requires a unique name and existing serviceId")
		}
		if port.Number < 1024 || port.Number > 65535 {
			return TemplateManifest{}, invalid("invalid_port", field+".number", "must be between 1024 and 65535")
		}
		if port.Protocol != "http" && port.Protocol != "https" && port.Protocol != "tcp" {
			return TemplateManifest{}, invalid("invalid_port", field+".protocol", "must be http, https, or tcp")
		}
		if port.Exposure != "internal" && port.Exposure != "preview" {
			return TemplateManifest{}, invalid("invalid_port", field+".exposure", "must be internal or preview")
		}
		if _, exists := portNames[port.Name]; exists {
			return TemplateManifest{}, invalid("duplicate_port", field+".name", "port names must be unique")
		}
		if prior, exists := portNumbers[port.Number]; exists {
			return TemplateManifest{}, invalid("duplicate_port", field+".number", "duplicates port "+prior)
		}
		portNames[port.Name], portNumbers[port.Number] = port, port.Name
		manifest.Ports[index] = port
	}
	sort.Slice(manifest.Ports, func(i, j int) bool { return manifest.Ports[i].Name < manifest.Ports[j].Name })

	healthIDs, healthyServices := map[string]bool{}, map[string]bool{}
	for index, health := range manifest.HealthChecks {
		field := fmt.Sprintf("manifest.healthChecks[%d]", index)
		health.ID = strings.TrimSpace(health.ID)
		health.ServiceID = strings.TrimSpace(health.ServiceID)
		health.PortName = strings.TrimSpace(health.PortName)
		if !slugPattern.MatchString(health.ID) || services[health.ServiceID].ID == "" || portNames[health.PortName].ServiceID != health.ServiceID {
			return TemplateManifest{}, invalid("invalid_health_check", field, "must bind a unique ID to a port owned by the same service")
		}
		if portNames[health.PortName].Protocol != "http" && portNames[health.PortName].Protocol != "https" {
			return TemplateManifest{}, invalid("invalid_health_check", field+".portName", "health checks require an HTTP or HTTPS port")
		}
		health.Path = strings.TrimSpace(health.Path)
		if !strings.HasPrefix(health.Path, "/") || len(health.Path) > 300 || strings.Contains(health.Path, "..") || strings.ContainsAny(health.Path, "?#\r\n") {
			return TemplateManifest{}, invalid("invalid_health_check", field+".path", "must be a local absolute HTTP path without query or fragment")
		}
		if healthIDs[health.ID] {
			return TemplateManifest{}, invalid("duplicate_health_check", field+".id", "health check IDs must be unique")
		}
		healthIDs[health.ID], healthyServices[health.ServiceID] = true, true
		manifest.HealthChecks[index] = health
	}
	for _, service := range manifest.Services {
		if service.Kind != "worker" && !healthyServices[service.ID] {
			return TemplateManifest{}, invalid("missing_health_check", "manifest.healthChecks", "service "+service.ID+" has no health check")
		}
	}
	sort.Slice(manifest.HealthChecks, func(i, j int) bool { return manifest.HealthChecks[i].ID < manifest.HealthChecks[j].ID })

	if manifest.Migration != nil {
		migration := *manifest.Migration
		migration.ServiceID = strings.TrimSpace(migration.ServiceID)
		migration.CommandName = strings.TrimSpace(migration.CommandName)
		if services[migration.ServiceID].ID == "" || commands[migration.CommandName].Argv == nil {
			return TemplateManifest{}, invalid("invalid_migration", "manifest.migration", "must reference an existing service and declared command")
		}
		manifest.Migration = &migration
	}

	outputKeys, outputServices := map[string]bool{}, map[string]bool{}
	for index, output := range manifest.BuildOutputs {
		field := fmt.Sprintf("manifest.buildOutputs[%d]", index)
		output.ServiceID = strings.TrimSpace(output.ServiceID)
		if services[output.ServiceID].ID == "" {
			return TemplateManifest{}, invalid("invalid_build_output", field+".serviceId", "must reference an existing service")
		}
		normalized, err := normalizeRelativePath(output.Path, false)
		if err != nil {
			return TemplateManifest{}, invalid("invalid_build_output", field+".path", err.Error())
		}
		output.Path = normalized
		key := strings.ToLower(output.ServiceID + ":" + output.Path)
		if outputKeys[key] {
			return TemplateManifest{}, invalid("duplicate_build_output", field, "build outputs must be unique")
		}
		outputKeys[key], outputServices[output.ServiceID] = true, true
		manifest.BuildOutputs[index] = output
	}
	for _, service := range manifest.Services {
		if service.Kind != "worker" && !outputServices[service.ID] {
			return TemplateManifest{}, invalid("missing_build_output", "manifest.buildOutputs", "service "+service.ID+" has no declared output")
		}
	}
	sort.Slice(manifest.BuildOutputs, func(i, j int) bool {
		left, right := manifest.BuildOutputs[i], manifest.BuildOutputs[j]
		return left.ServiceID+":"+left.Path < right.ServiceID+":"+right.Path
	})

	extensionPaths, err := normalizePathSet(manifest.ExtensionPaths, "manifest.extensionPaths")
	if err != nil {
		return TemplateManifest{}, err
	}
	protectedPaths, err := normalizePathSet(manifest.ProtectedPaths, "manifest.protectedPaths")
	if err != nil {
		return TemplateManifest{}, err
	}
	for _, extension := range extensionPaths {
		for _, protected := range protectedPaths {
			if pathsOverlap(extension, protected) {
				return TemplateManifest{}, invalid("overlapping_path_policy", "manifest.extensionPaths", extension+" overlaps protected path "+protected)
			}
		}
	}
	manifest.ExtensionPaths, manifest.ProtectedPaths = extensionPaths, protectedPaths

	environmentNames := map[string]bool{}
	for index, variable := range manifest.EnvironmentSchema {
		field := fmt.Sprintf("manifest.environmentSchema[%d]", index)
		variable.Name = strings.TrimSpace(variable.Name)
		variable.Description = strings.TrimSpace(variable.Description)
		if !environmentNamePattern.MatchString(variable.Name) || variable.Description == "" || len(variable.Description) > 500 {
			return TemplateManifest{}, invalid("invalid_environment_variable", field, "requires a unique uppercase name and bounded description")
		}
		if variable.Secret && variable.Default != nil {
			return TemplateManifest{}, invalid("secret_default_forbidden", field+".default", "secret variables must never contain a default")
		}
		if variable.Default != nil {
			value := strings.TrimSpace(*variable.Default)
			if len(value) > 500 || containsControl(value) {
				return TemplateManifest{}, invalid("invalid_environment_variable", field+".default", "must be bounded and contain no control characters")
			}
			variable.Default = &value
		}
		if environmentNames[variable.Name] {
			return TemplateManifest{}, invalid("duplicate_environment_variable", field+".name", "environment variable names must be unique")
		}
		environmentNames[variable.Name] = true
		manifest.EnvironmentSchema[index] = variable
	}
	sort.Slice(manifest.EnvironmentSchema, func(i, j int) bool { return manifest.EnvironmentSchema[i].Name < manifest.EnvironmentSchema[j].Name })

	lockPaths := map[string]bool{}
	for index, lockfile := range manifest.Lockfiles {
		field := fmt.Sprintf("manifest.lockfiles[%d]", index)
		normalized, err := normalizeRelativePath(lockfile.Path, false)
		if err != nil {
			return TemplateManifest{}, invalid("invalid_lockfile", field+".path", err.Error())
		}
		lockfile.Path = normalized
		if err := validateDigest(strings.TrimSpace(lockfile.Digest), field+".digest"); err != nil {
			return TemplateManifest{}, err
		}
		lockfile.Digest = strings.TrimSpace(lockfile.Digest)
		registry, err := normalizeRegistryURL(lockfile.Registry, field+".registry")
		if err != nil {
			return TemplateManifest{}, err
		}
		lockfile.Registry = registry
		key := strings.ToLower(lockfile.Path)
		if lockPaths[key] {
			return TemplateManifest{}, invalid("duplicate_lockfile", field+".path", "lockfile paths must be unique")
		}
		lockPaths[key] = true
		manifest.Lockfiles[index] = lockfile
	}
	sort.Slice(manifest.Lockfiles, func(i, j int) bool { return manifest.Lockfiles[i].Path < manifest.Lockfiles[j].Path })

	manifest.ProfileDigest = strings.TrimSpace(manifest.ProfileDigest)
	if err := validateDigest(manifest.ProfileDigest, "manifest.profileDigest"); err != nil {
		return TemplateManifest{}, err
	}
	return manifest, nil
}

func normalizeCommand(command Command, field string) (Command, error) {
	workingDirectory, err := normalizeRelativePath(command.WorkingDirectory, true)
	if err != nil {
		return Command{}, invalid("invalid_command", field+".workingDirectory", err.Error())
	}
	if len(command.Argv) == 0 || len(command.Argv) > 64 {
		return Command{}, invalid("invalid_command", field+".argv", "must contain between 1 and 64 argv entries")
	}
	argv := make([]string, len(command.Argv))
	for index, argument := range command.Argv {
		argument = strings.TrimSpace(argument)
		if argument == "" || len(argument) > 1024 || containsControl(argument) {
			return Command{}, invalid("invalid_command", fmt.Sprintf("%s.argv[%d]", field, index), "must be non-empty, bounded, and contain no control characters")
		}
		argv[index] = argument
	}
	executable := strings.ToLower(path.Base(argv[0]))
	switch executable {
	case "sh", "bash", "dash", "zsh", "fish", "cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh":
		return Command{}, invalid("shell_command_forbidden", field+".argv[0]", "commands must execute a tool directly, not a shell interpreter")
	}
	return Command{WorkingDirectory: workingDirectory, Argv: argv}, nil
}

func normalizePathSet(values []string, field string) ([]string, error) {
	result := make([]string, len(values))
	seen := map[string]bool{}
	for index, value := range values {
		normalized, err := normalizeRelativePath(value, false)
		if err != nil {
			return nil, invalid("invalid_path_policy", fmt.Sprintf("%s[%d]", field, index), err.Error())
		}
		key := strings.ToLower(normalized)
		if seen[key] {
			return nil, invalid("duplicate_path_policy", fmt.Sprintf("%s[%d]", field, index), "paths must be unique")
		}
		seen[key], result[index] = true, normalized
	}
	sort.Strings(result)
	return result, nil
}

func normalizeRelativePath(value string, allowRoot bool) (string, error) {
	value = strings.TrimSpace(value)
	if allowRoot && value == "." {
		return value, nil
	}
	if value == "" || value == "." || len(value) > 400 || strings.Contains(value, `\`) || strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return "", fmt.Errorf("must be a clean relative POSIX path")
	}
	for _, segment := range strings.Split(value, "/") {
		lower := strings.ToLower(segment)
		if segment == "" || segment == "." || segment == ".." || lower == ".git" || strings.HasPrefix(lower, ".env") || strings.ContainsAny(segment, "\x00\r\n") {
			return "", fmt.Errorf("contains an unsafe path segment")
		}
	}
	return value, nil
}

func pathsOverlap(left, right string) bool {
	left, right = strings.ToLower(left), strings.ToLower(right)
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func normalizeRegistryURL(raw, field string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Port() != "" {
		return "", invalid("invalid_registry", field, "must be an absolute credential-free HTTPS registry URL")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return "", invalid("unsafe_registry", field, "must use a public DNS host")
	}
	parsed.Host = host
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func validateSPDXExpression(value string) error {
	if value == "" || len(value) > 240 || strings.EqualFold(value, "NONE") || strings.EqualFold(value, "NOASSERTION") {
		return invalid("invalid_spdx", "candidate.licenseExpression", "must declare a concrete SPDX license expression")
	}
	replacer := strings.NewReplacer("(", " ( ", ")", " ) ")
	tokens := strings.Fields(replacer.Replace(value))
	if len(tokens) == 0 {
		return invalid("invalid_spdx", "candidate.licenseExpression", "must declare a concrete SPDX license expression")
	}
	depth, expectIdentifier := 0, true
	for index, token := range tokens {
		upper := strings.ToUpper(token)
		if expectIdentifier {
			if token == "(" {
				depth++
				continue
			}
			if !spdxIdentifierPattern.MatchString(token) || upper == "AND" || upper == "OR" || upper == "WITH" {
				return invalid("invalid_spdx", "candidate.licenseExpression", fmt.Sprintf("unexpected token %q at position %d", token, index))
			}
			expectIdentifier = false
			continue
		}
		if token == ")" {
			if depth == 0 {
				return invalid("invalid_spdx", "candidate.licenseExpression", "contains an unmatched closing parenthesis")
			}
			depth--
			continue
		}
		if upper != "AND" && upper != "OR" && upper != "WITH" {
			return invalid("invalid_spdx", "candidate.licenseExpression", fmt.Sprintf("unexpected token %q at position %d", token, index))
		}
		expectIdentifier = true
	}
	if depth != 0 || expectIdentifier {
		return invalid("invalid_spdx", "candidate.licenseExpression", "is incomplete or has unmatched parentheses")
	}
	return nil
}

func containsControl(value string) bool {
	if !utf8.ValidString(value) {
		return true
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func subjectHash(candidate AdmissionCandidate) (string, error) {
	return canonicalHash(struct {
		SchemaVersion string             `json:"schemaVersion"`
		Candidate     AdmissionCandidate `json:"candidate"`
	}{SchemaVersion: TemplateReleaseSchemaVersion, Candidate: candidate})
}

func normalizeEvidence(evidence []GateEvidence, expectedSubject string, evaluatedAt time.Time) ([]GateEvidence, []AdmissionFinding) {
	result := make([]GateEvidence, 0, len(evidence))
	findings := make([]AdmissionFinding, 0)
	required := make(map[AdmissionGate]bool, len(requiredAdmissionGates))
	for _, gate := range requiredAdmissionGates {
		required[gate] = true
	}
	seen := map[AdmissionGate]bool{}
	for index, item := range evidence {
		field := fmt.Sprintf("evidence[%d]", index)
		item.SubjectHash = strings.TrimSpace(item.SubjectHash)
		item.Digest = strings.TrimSpace(item.Digest)
		item.Reference = strings.TrimSpace(item.Reference)
		item.Producer = strings.TrimSpace(item.Producer)
		item.InvocationID = strings.TrimSpace(item.InvocationID)
		item.ObservedAt = item.ObservedAt.UTC()
		if !required[item.Gate] {
			findings = append(findings, blocker("unknown_gate", item.Gate, field+".gate", "evidence identifies a gate outside template-release/v1"))
			continue
		}
		if seen[item.Gate] {
			findings = append(findings, blocker("duplicate_gate_evidence", item.Gate, field+".gate", "required gates accept exactly one evidence record"))
			continue
		}
		seen[item.Gate] = true
		valid := true
		if item.Outcome != EvidencePassed && item.Outcome != EvidenceFailed {
			findings = append(findings, blocker("invalid_evidence_outcome", item.Gate, field+".outcome", "must be passed or failed"))
			valid = false
		}
		if item.SubjectHash != expectedSubject {
			findings = append(findings, blocker("evidence_subject_mismatch", item.Gate, field+".subjectHash", "does not bind the exact candidate subject hash"))
			valid = false
		}
		if !digestPattern.MatchString(item.Digest) {
			findings = append(findings, blocker("invalid_evidence_digest", item.Gate, field+".digest", "must be a lowercase sha256 digest"))
			valid = false
		}
		if !validEvidenceReference(item.Reference) {
			findings = append(findings, blocker("invalid_evidence_reference", item.Gate, field+".reference", "must be a credential-free content, https, oci, or urn reference"))
			valid = false
		}
		if item.Producer == "" || len(item.Producer) > 240 || containsControl(item.Producer) || item.InvocationID == "" || len(item.InvocationID) > 240 || containsControl(item.InvocationID) {
			findings = append(findings, blocker("invalid_evidence_identity", item.Gate, field, "producer and invocationId must be bounded non-empty values"))
			valid = false
		}
		if item.ObservedAt.IsZero() || item.ObservedAt.After(evaluatedAt) {
			findings = append(findings, blocker("invalid_evidence_time", item.Gate, field+".observedAt", "must not be zero or later than evaluation time"))
			valid = false
		}
		if item.Outcome == EvidenceFailed {
			findings = append(findings, blocker("required_gate_failed", item.Gate, field+".outcome", "required admission gate did not pass"))
		}
		if valid {
			result = append(result, item)
		}
	}
	for _, gate := range requiredAdmissionGates {
		if !seen[gate] {
			findings = append(findings, blocker("required_gate_missing", gate, "evidence", "required admission evidence is absent"))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Gate < result[j].Gate })
	sort.SliceStable(findings, func(i, j int) bool {
		left, right := findings[i], findings[j]
		return string(left.Gate)+":"+left.Code+":"+left.Field < string(right.Gate)+":"+right.Code+":"+right.Field
	})
	return result, findings
}

func normalizeSignature(signature SignatureEnvelope, subject string, evaluatedAt time.Time) (SignatureEnvelope, []AdmissionFinding) {
	signature.Format = strings.TrimSpace(signature.Format)
	signature.SubjectHash = strings.TrimSpace(signature.SubjectHash)
	signature.BundleDigest = strings.TrimSpace(signature.BundleDigest)
	signature.Signer = strings.TrimSpace(signature.Signer)
	signature.TransparencyLogRef = strings.TrimSpace(signature.TransparencyLogRef)
	signature.SignedAt = signature.SignedAt.UTC()
	findings := []AdmissionFinding{}
	if signature.Format != "dsse" {
		findings = append(findings, blocker("invalid_signature_format", GateSignatureAttestation, "signature.format", "template-release/v1 requires a DSSE envelope"))
	}
	if signature.SubjectHash != subject {
		findings = append(findings, blocker("signature_subject_mismatch", GateSignatureAttestation, "signature.subjectHash", "signature does not bind the exact candidate subject hash"))
	}
	if !digestPattern.MatchString(signature.BundleDigest) {
		findings = append(findings, blocker("invalid_signature_digest", GateSignatureAttestation, "signature.bundleDigest", "must be a lowercase sha256 digest"))
	}
	if signature.Signer == "" || len(signature.Signer) > 500 || containsControl(signature.Signer) {
		findings = append(findings, blocker("invalid_signer", GateSignatureAttestation, "signature.signer", "must identify the attested signer"))
	}
	if !validEvidenceReference(signature.TransparencyLogRef) {
		findings = append(findings, blocker("invalid_transparency_reference", GateSignatureAttestation, "signature.transparencyLogRef", "must be a durable content, https, oci, or urn reference"))
	}
	if signature.SignedAt.IsZero() || signature.SignedAt.After(evaluatedAt) {
		findings = append(findings, blocker("invalid_signature_time", GateSignatureAttestation, "signature.signedAt", "must not be zero or later than evaluation time"))
	}
	return signature, findings
}

func validEvidenceReference(value string) bool {
	if value == "" || len(value) > 2048 || containsControl(value) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.User != nil {
		return false
	}
	switch parsed.Scheme {
	case "content", "oci":
		return parsed.Host != "" || parsed.Opaque != "" || parsed.Path != ""
	case "urn":
		return parsed.Opaque != ""
	case "https":
		return parsed.Host != "" && parsed.RawQuery == "" && parsed.Fragment == ""
	default:
		return false
	}
}

func blocker(code string, gate AdmissionGate, field, detail string) AdmissionFinding {
	return AdmissionFinding{Code: code, Gate: gate, Field: field, Severity: "blocker", Detail: detail}
}

func cloneAttemptDocument(input admissionAttemptDocument) admissionAttemptDocument {
	var output admissionAttemptDocument
	cloneViaJSON(input, &output)
	return output
}

func cloneReleaseDocument(input templateReleaseDocument) templateReleaseDocument {
	var output templateReleaseDocument
	cloneViaJSON(input, &output)
	return output
}

func cloneFullStackDocument(input fullStackTemplateDocument) fullStackTemplateDocument {
	var output fullStackTemplateDocument
	cloneViaJSON(input, &output)
	return output
}

func cloneViaJSON(input, output any) {
	encoded, err := json.Marshal(input)
	if err != nil {
		panic("templates: clone marshal failed: " + err.Error())
	}
	if err := json.Unmarshal(encoded, output); err != nil {
		panic("templates: clone unmarshal failed: " + err.Error())
	}
}

func isNormalizedCandidate(candidate AdmissionCandidate) bool {
	normalized, err := normalizeCandidate(candidate)
	return err == nil && reflect.DeepEqual(candidate, normalized)
}
