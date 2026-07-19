package templates

import (
	"crypto/sha256"
	"fmt"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

const (
	LanguageServerMaxProfiles                     = 16
	LanguageServerMaxLanguageIDs                  = 32
	LanguageServerMaxFileGlobs                    = 64
	LanguageServerMaxMethods                      = 32
	LanguageServerMaxStartupTimeoutMillis         = 20_000
	LanguageServerMaxRequestTimeoutMillis         = 10_000
	LanguageServerMaxShutdownTimeoutMillis        = 5_000
	LanguageServerMaxCPUMillis                    = 4_000
	LanguageServerMaxMemoryBytes            int64 = 4 << 30
	LanguageServerMaxPIDs                         = 256
	LanguageServerMaxTempBytes              int64 = 2 << 30
	LanguageServerMaxCacheBytes             int64 = 2 << 30
	LanguageServerMaxOpenDocuments                = 32
	LanguageServerMaxDocumentBytes          int64 = 1 << 20
	LanguageServerMaxTotalSyncBytes         int64 = 8 << 20
	LanguageServerMaxFrameBytes             int64 = 512 << 10
	LanguageServerMaxResultBytes            int64 = 1 << 20
	LanguageServerMaxConcurrentRequests           = 32
	LanguageServerMaxRequestsPerSecond            = 30
	LanguageServerMaxRequestBurst                 = 60
	LanguageServerMaxDiagnosticsPerDocument       = 2_000
	LanguageServerMaxCompletionItems              = 500
	LanguageServerMaxNavigationLocations          = 5_000
)

var (
	languageIDPattern = regexp.MustCompile(`^[a-z][a-z0-9.+-]{0,63}$`)
	ociImagePattern   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?/[a-z0-9]+(?:[._/-][a-z0-9]+)*@sha256:[0-9a-f]{64}$`)

	languageServerBaselineMethods = []string{
		"textDocument/completion",
		"textDocument/declaration",
		"textDocument/definition",
		"textDocument/diagnostic",
		"textDocument/documentHighlight",
		"textDocument/documentSymbol",
		"textDocument/hover",
		"textDocument/implementation",
		"textDocument/inlayHint",
		"textDocument/publishDiagnostics",
		"textDocument/references",
		"textDocument/semanticTokens/full",
		"textDocument/semanticTokens/range",
		"textDocument/signatureHelp",
		"textDocument/typeDefinition",
	}
	languageServerBaselineMethodSet = func() map[string]bool {
		result := make(map[string]bool, len(languageServerBaselineMethods))
		for _, method := range languageServerBaselineMethods {
			result[method] = true
		}
		return result
	}()
)

var (
	productionV1InitializationParametersHash = languageServerPolicyHash(
		`{"customParameters":{},"schemaVersion":"worksflow-lsp-initialization-policy/v1"}`,
	)
	productionV1WorkspaceConfigurationHash = languageServerPolicyHash(
		`{"schemaVersion":"worksflow-lsp-workspace-configuration/v1","settings":{}}`,
	)
)

// ProductionV1InitializationParametersHash commits v1 to no server-specific
// custom initialization parameters. A future widening requires a new protocol
// and a separately reviewed server-specific schema.
func ProductionV1InitializationParametersHash() string {
	return productionV1InitializationParametersHash
}

// ProductionV1WorkspaceConfigurationHash commits v1 to an empty, platform-
// owned workspace configuration. The server cannot request arbitrary settings
// or smuggle commands/plugins through workspace/configuration.
func ProductionV1WorkspaceConfigurationHash() string {
	return productionV1WorkspaceConfigurationHash
}

func languageServerPolicyHash(canonical string) string {
	digest := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf("sha256:%x", digest[:])
}

type languageServerCapabilityHashPayload struct {
	SchemaVersion string   `json:"schemaVersion"`
	Methods       []string `json:"methods"`
}

type languageServerProfileHashPayload struct {
	SchemaVersion                string                  `json:"schemaVersion"`
	ID                           string                  `json:"id"`
	ServiceID                    string                  `json:"serviceId"`
	LanguageIDs                  []string                `json:"languageIds"`
	FileGlobs                    []string                `json:"fileGlobs"`
	ProtocolVersion              string                  `json:"protocolVersion"`
	Runtime                      LanguageServerRuntime   `json:"runtime"`
	ServerInfo                   LanguageServerInfo      `json:"serverInfo"`
	InitializationParametersHash string                  `json:"initializationParametersHash"`
	WorkspaceConfigurationHash   string                  `json:"workspaceConfigurationHash"`
	RequireVersionedDiagnostics  bool                    `json:"requireVersionedDiagnostics"`
	Methods                      []string                `json:"methods"`
	CapabilityHash               string                  `json:"capabilityHash"`
	Limits                       LanguageServerLimits    `json:"limits"`
	Isolation                    LanguageServerIsolation `json:"isolation"`
}

// LanguageServerBaselineMethods returns the immutable production-v1 method
// ceiling. A TemplateRelease profile may only narrow this set.
func LanguageServerBaselineMethods() []string {
	return append([]string(nil), languageServerBaselineMethods...)
}

// ComputeLanguageServerCapabilityHash makes the capability commitment
// independently reproducible from a method set. The method set is validated,
// copied, de-duplicated, and sorted before hashing.
func ComputeLanguageServerCapabilityHash(methods []string) (string, error) {
	normalized, err := normalizeLanguageServerMethods(methods, "methods")
	if err != nil {
		return "", err
	}
	return canonicalHash(languageServerCapabilityHashPayload{
		SchemaVersion: LanguageServerCapabilitySchemaVersion,
		Methods:       normalized,
	})
}

// ComputeLanguageServerProfileContentHash computes the self hash over the
// canonical profile payload while intentionally excluding ContentHash itself.
// The supplied CapabilityHash and ContentHash are not trusted: capabilityHash
// is recomputed from Methods and contentHash is the return value.
func ComputeLanguageServerProfileContentHash(profile LanguageServerProfile) (string, error) {
	normalized, err := normalizeLanguageServerProfileFields(profile)
	if err != nil {
		return "", err
	}
	return hashLanguageServerProfile(normalized)
}

// ValidateLanguageServerProfile is the exact-object authority check used by
// downstream ticket/runtime code. Unlike admission normalization, it rejects
// a semantically equivalent but unsorted, whitespace-padded, or otherwise
// noncanonical object, and it verifies both supplied commitments.
func ValidateLanguageServerProfile(profile LanguageServerProfile) error {
	normalized, err := normalizeLanguageServerProfile(profile, "languageServerProfile")
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(profile, normalized) {
		return invalid("noncanonical_language_server_profile", "languageServerProfile", "must exactly equal its canonical normalized form")
	}
	return nil
}

func normalizeLanguageServerProfiles(profiles []LanguageServerProfile, services map[string]TemplateService) ([]LanguageServerProfile, error) {
	if len(profiles) == 0 {
		return nil, nil
	}
	if len(profiles) > LanguageServerMaxProfiles {
		return nil, invalid("invalid_language_server_profiles", "manifest.languageServers", "must contain at most 16 profiles")
	}
	result := make([]LanguageServerProfile, len(profiles))
	seen := make(map[string]bool, len(profiles))
	for index, profile := range profiles {
		field := fmt.Sprintf("manifest.languageServers[%d]", index)
		normalized, err := normalizeLanguageServerProfile(profile, field)
		if err != nil {
			return nil, err
		}
		if services[normalized.ServiceID].ID == "" {
			return nil, invalid("language_server_service_not_found", field+".serviceId", "must reference an existing manifest service")
		}
		if seen[normalized.ID] {
			return nil, invalid("duplicate_language_server_profile", field+".id", "profile IDs must be unique")
		}
		seen[normalized.ID] = true
		result[index] = normalized
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func normalizeLanguageServerProfile(profile LanguageServerProfile, field string) (LanguageServerProfile, error) {
	suppliedCapabilityHash := strings.TrimSpace(profile.CapabilityHash)
	suppliedContentHash := strings.TrimSpace(profile.ContentHash)
	normalized, err := normalizeLanguageServerProfileFields(profile)
	if err != nil {
		return LanguageServerProfile{}, prefixTemplateErrorField(err, field)
	}
	if err := validateDigest(suppliedCapabilityHash, field+".capabilityHash"); err != nil {
		return LanguageServerProfile{}, err
	}
	if suppliedCapabilityHash != normalized.CapabilityHash {
		return LanguageServerProfile{}, invalid("language_server_capability_hash_mismatch", field+".capabilityHash", "does not commit to the canonical allowed method set")
	}
	expectedContentHash, err := hashLanguageServerProfile(normalized)
	if err != nil {
		return LanguageServerProfile{}, err
	}
	if err := validateDigest(suppliedContentHash, field+".contentHash"); err != nil {
		return LanguageServerProfile{}, err
	}
	if suppliedContentHash != expectedContentHash {
		return LanguageServerProfile{}, invalid("language_server_profile_hash_mismatch", field+".contentHash", "does not commit to the exact canonical language-server profile")
	}
	normalized.ContentHash = expectedContentHash
	return normalized, nil
}

// normalizeLanguageServerProfileFields validates and canonicalizes every field
// other than the two caller-supplied commitments. It always installs the
// independently computed canonical capability hash.
func normalizeLanguageServerProfileFields(profile LanguageServerProfile) (LanguageServerProfile, error) {
	profile.SchemaVersion = strings.TrimSpace(profile.SchemaVersion)
	if profile.SchemaVersion != LanguageServerProfileSchemaVersion {
		return LanguageServerProfile{}, &Error{Kind: ErrUnsupportedSchema, Code: "unsupported_language_server_profile_schema", Field: "schemaVersion", Detail: "must be language-server-profile/v1"}
	}
	profile.ID = strings.TrimSpace(profile.ID)
	if !slugPattern.MatchString(profile.ID) || len(profile.ID) > 80 {
		return LanguageServerProfile{}, invalid("invalid_language_server_profile_id", "id", "must be a lowercase kebab-case identifier")
	}
	profile.ServiceID = strings.TrimSpace(profile.ServiceID)
	if !slugPattern.MatchString(profile.ServiceID) || len(profile.ServiceID) > 80 {
		return LanguageServerProfile{}, invalid("invalid_language_server_service", "serviceId", "must be a lowercase kebab-case service identifier")
	}

	languageIDs, err := normalizeLanguageIDs(profile.LanguageIDs)
	if err != nil {
		return LanguageServerProfile{}, err
	}
	profile.LanguageIDs = languageIDs
	fileGlobs, err := normalizeLanguageServerFileGlobs(profile.FileGlobs)
	if err != nil {
		return LanguageServerProfile{}, err
	}
	profile.FileGlobs = fileGlobs

	profile.ProtocolVersion = strings.TrimSpace(profile.ProtocolVersion)
	if profile.ProtocolVersion != "3.17" {
		return LanguageServerProfile{}, invalid("unsupported_language_server_protocol", "protocolVersion", "production v1 requires LSP 3.17")
	}
	profile.Runtime, err = normalizeLanguageServerRuntime(profile.Runtime)
	if err != nil {
		return LanguageServerProfile{}, err
	}
	profile.ServerInfo, err = normalizeLanguageServerInfo(profile.ServerInfo)
	if err != nil {
		return LanguageServerProfile{}, err
	}
	profile.InitializationParametersHash = strings.TrimSpace(profile.InitializationParametersHash)
	if err := validateDigest(profile.InitializationParametersHash, "initializationParametersHash"); err != nil {
		return LanguageServerProfile{}, err
	}
	if profile.InitializationParametersHash != productionV1InitializationParametersHash {
		return LanguageServerProfile{}, invalid("unsupported_language_server_initialization_policy", "initializationParametersHash", "production v1 permits no server-specific custom initialization parameters")
	}
	profile.WorkspaceConfigurationHash = strings.TrimSpace(profile.WorkspaceConfigurationHash)
	if err := validateDigest(profile.WorkspaceConfigurationHash, "workspaceConfigurationHash"); err != nil {
		return LanguageServerProfile{}, err
	}
	if profile.WorkspaceConfigurationHash != productionV1WorkspaceConfigurationHash {
		return LanguageServerProfile{}, invalid("unsupported_language_server_workspace_configuration", "workspaceConfigurationHash", "production v1 uses the empty platform-owned workspace configuration")
	}
	if !profile.RequireVersionedDiagnostics {
		return LanguageServerProfile{}, invalid("versioned_diagnostics_required", "requireVersionedDiagnostics", "production v1 requires diagnostic versions")
	}
	profile.Methods, err = normalizeLanguageServerMethods(profile.Methods, "methods")
	if err != nil {
		return LanguageServerProfile{}, err
	}
	profile.CapabilityHash, err = canonicalHash(languageServerCapabilityHashPayload{
		SchemaVersion: LanguageServerCapabilitySchemaVersion,
		Methods:       profile.Methods,
	})
	if err != nil {
		return LanguageServerProfile{}, err
	}
	if err := validateLanguageServerLimits(profile.Limits); err != nil {
		return LanguageServerProfile{}, err
	}
	profile.Isolation, err = normalizeLanguageServerIsolation(profile.Isolation)
	if err != nil {
		return LanguageServerProfile{}, err
	}
	profile.ContentHash = ""
	return profile, nil
}

func normalizeLanguageIDs(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > LanguageServerMaxLanguageIDs {
		return nil, invalid("invalid_language_server_languages", "languageIds", "must contain between 1 and 32 language IDs")
	}
	result := make([]string, len(values))
	seen := make(map[string]bool, len(values))
	for index, value := range values {
		value = strings.TrimSpace(value)
		if !languageIDPattern.MatchString(value) {
			return nil, invalid("invalid_language_server_language", fmt.Sprintf("languageIds[%d]", index), "must be a canonical lowercase LSP language ID")
		}
		if seen[value] {
			return nil, invalid("duplicate_language_server_language", fmt.Sprintf("languageIds[%d]", index), "language IDs must be unique")
		}
		seen[value] = true
		result[index] = value
	}
	sort.Strings(result)
	return result, nil
}

func normalizeLanguageServerFileGlobs(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > LanguageServerMaxFileGlobs {
		return nil, invalid("invalid_language_server_globs", "fileGlobs", "must contain between 1 and 64 canonical globs")
	}
	result := make([]string, len(values))
	seen := make(map[string]bool, len(values))
	for index, value := range values {
		value = strings.TrimSpace(value)
		field := fmt.Sprintf("fileGlobs[%d]", index)
		if err := validateLanguageServerFileGlob(value); err != nil {
			return nil, invalid("invalid_language_server_glob", field, err.Error())
		}
		key := strings.ToLower(value)
		if seen[key] {
			return nil, invalid("duplicate_language_server_glob", field, "file globs must be unique")
		}
		seen[key] = true
		result[index] = value
	}
	sort.Strings(result)
	return result, nil
}

func validateLanguageServerFileGlob(value string) error {
	if value == "" || len(value) > 400 || strings.HasPrefix(value, "/") ||
		strings.Contains(value, `\`) || path.Clean(value) != value || containsControl(value) || strings.ContainsAny(value, "?[]{}!") {
		return fmt.Errorf("must be a bounded relative canonical POSIX glob")
	}
	for _, segment := range strings.Split(value, "/") {
		lower := strings.ToLower(segment)
		if segment == "" || segment == "." || segment == ".." || lower == ".git" || strings.HasPrefix(lower, ".env") {
			return fmt.Errorf("contains an unsafe path segment")
		}
		if strings.Contains(segment, "**") && segment != "**" {
			return fmt.Errorf("double-star must occupy an entire path segment")
		}
	}
	return nil
}

func normalizeLanguageServerRuntime(runtime LanguageServerRuntime) (LanguageServerRuntime, error) {
	runtime.Image = strings.TrimSpace(runtime.Image)
	if !ociImagePattern.MatchString(runtime.Image) || strings.ContainsAny(runtime.Image, " \t\r\n") {
		return LanguageServerRuntime{}, invalid("language_server_runtime_not_pinned", "runtime.image", "must be an OCI image reference pinned by an exact sha256 digest")
	}
	runtime.ExecutablePath = strings.TrimSpace(runtime.ExecutablePath)
	if runtime.ExecutablePath == "/" || !strings.HasPrefix(runtime.ExecutablePath, "/") || path.Clean(runtime.ExecutablePath) != runtime.ExecutablePath ||
		len(runtime.ExecutablePath) > 500 || strings.Contains(runtime.ExecutablePath, `\`) || containsControl(runtime.ExecutablePath) {
		return LanguageServerRuntime{}, invalid("invalid_language_server_executable", "runtime.executablePath", "must be a clean absolute POSIX path")
	}
	runtime.ExecutableDigest = strings.TrimSpace(runtime.ExecutableDigest)
	if err := validateDigest(runtime.ExecutableDigest, "runtime.executableDigest"); err != nil {
		return LanguageServerRuntime{}, err
	}
	if len(runtime.Argv) == 0 || len(runtime.Argv) > 64 {
		return LanguageServerRuntime{}, invalid("invalid_language_server_argv", "runtime.argv", "must contain between 1 and 64 direct-exec argv entries")
	}
	argv := make([]string, len(runtime.Argv))
	for index, argument := range runtime.Argv {
		argument = strings.TrimSpace(argument)
		if argument == "" || len(argument) > 1024 || containsControl(argument) {
			return LanguageServerRuntime{}, invalid("invalid_language_server_argv", fmt.Sprintf("runtime.argv[%d]", index), "must be non-empty, bounded, and contain no control characters")
		}
		argv[index] = argument
	}
	if argv[0] != runtime.ExecutablePath {
		return LanguageServerRuntime{}, invalid("language_server_executable_mismatch", "runtime.argv[0]", "must exactly equal runtime.executablePath")
	}
	switch strings.ToLower(path.Base(runtime.ExecutablePath)) {
	case "sh", "bash", "dash", "zsh", "fish", "cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh", "env", "busybox":
		return LanguageServerRuntime{}, invalid("language_server_shell_forbidden", "runtime.executablePath", "must directly execute the admitted language server, not a shell or command dispatcher")
	}
	runtime.Argv = argv
	runtime.WorkingDirectoryPolicy = strings.TrimSpace(runtime.WorkingDirectoryPolicy)
	if runtime.WorkingDirectoryPolicy != "service-root" {
		return LanguageServerRuntime{}, invalid("invalid_language_server_working_directory", "runtime.workingDirectoryPolicy", "must be service-root")
	}
	return runtime, nil
}

func normalizeLanguageServerInfo(info LanguageServerInfo) (LanguageServerInfo, error) {
	info.Name = strings.TrimSpace(info.Name)
	info.Version = strings.TrimSpace(info.Version)
	if info.Name == "" || len(info.Name) > 160 || containsControl(info.Name) {
		return LanguageServerInfo{}, invalid("invalid_language_server_info", "serverInfo.name", "must be a bounded exact server name")
	}
	if info.Version == "" || len(info.Version) > 120 || containsControl(info.Version) {
		return LanguageServerInfo{}, invalid("invalid_language_server_info", "serverInfo.version", "must be a bounded exact server version")
	}
	return info, nil
}

func normalizeLanguageServerMethods(methods []string, field string) ([]string, error) {
	if len(methods) == 0 || len(methods) > LanguageServerMaxMethods {
		return nil, invalid("invalid_language_server_methods", field, "must contain a bounded non-empty subset of the production-v1 baseline")
	}
	result := make([]string, len(methods))
	seen := make(map[string]bool, len(methods))
	for index, method := range methods {
		method = strings.TrimSpace(method)
		methodField := fmt.Sprintf("%s[%d]", field, index)
		if isForbiddenLanguageServerMethod(method) {
			return nil, invalid("forbidden_language_server_method", methodField, "write, command, formatting, rename, and dynamic-registration methods are forbidden")
		}
		if !languageServerBaselineMethodSet[method] {
			return nil, invalid("unsupported_language_server_method", methodField, "must be a member of the production-v1 read-only capability baseline")
		}
		if seen[method] {
			return nil, invalid("duplicate_language_server_method", methodField, "methods must be unique")
		}
		seen[method] = true
		result[index] = method
	}
	sort.Strings(result)
	return result, nil
}

func isForbiddenLanguageServerMethod(method string) bool {
	if strings.HasPrefix(method, "workspace/") || strings.Contains(method, "executeCommand") || strings.Contains(method, "applyEdit") {
		return true
	}
	for _, forbidden := range []string{
		"textDocument/rename", "textDocument/prepareRename", "textDocument/codeAction",
		"textDocument/formatting", "textDocument/rangeFormatting", "textDocument/onTypeFormatting",
		"textDocument/willSaveWaitUntil", "client/registerCapability", "client/unregisterCapability",
	} {
		if method == forbidden {
			return true
		}
	}
	return false
}

func validateLanguageServerLimits(limits LanguageServerLimits) error {
	intLimits := []struct {
		name  string
		value int
		cap   int
	}{
		{"startupTimeoutMillis", limits.StartupTimeoutMillis, LanguageServerMaxStartupTimeoutMillis},
		{"requestTimeoutMillis", limits.RequestTimeoutMillis, LanguageServerMaxRequestTimeoutMillis},
		{"shutdownTimeoutMillis", limits.ShutdownTimeoutMillis, LanguageServerMaxShutdownTimeoutMillis},
		{"cpuMillis", limits.CPUMillis, LanguageServerMaxCPUMillis},
		{"pidLimit", limits.PIDLimit, LanguageServerMaxPIDs},
		{"maxOpenDocuments", limits.MaxOpenDocuments, LanguageServerMaxOpenDocuments},
		{"maxConcurrentRequests", limits.MaxConcurrentRequests, LanguageServerMaxConcurrentRequests},
		{"requestsPerSecond", limits.RequestsPerSecond, LanguageServerMaxRequestsPerSecond},
		{"requestBurst", limits.RequestBurst, LanguageServerMaxRequestBurst},
		{"maxDiagnosticsPerDocument", limits.MaxDiagnosticsPerDocument, LanguageServerMaxDiagnosticsPerDocument},
		{"maxCompletionItems", limits.MaxCompletionItems, LanguageServerMaxCompletionItems},
		{"maxNavigationLocations", limits.MaxNavigationLocations, LanguageServerMaxNavigationLocations},
	}
	for _, limit := range intLimits {
		if limit.value <= 0 {
			return invalid("invalid_language_server_limit", "limits."+limit.name, "must be positive")
		}
		if limit.value > limit.cap {
			return invalid("language_server_limit_exceeds_hard_cap", "limits."+limit.name, fmt.Sprintf("must not exceed %d", limit.cap))
		}
	}
	byteLimits := []struct {
		name  string
		value int64
		cap   int64
	}{
		{"memoryBytes", limits.MemoryBytes, LanguageServerMaxMemoryBytes},
		{"tempBytes", limits.TempBytes, LanguageServerMaxTempBytes},
		{"cacheBytes", limits.CacheBytes, LanguageServerMaxCacheBytes},
		{"maxDocumentBytes", limits.MaxDocumentBytes, LanguageServerMaxDocumentBytes},
		{"maxTotalSyncBytes", limits.MaxTotalSyncBytes, LanguageServerMaxTotalSyncBytes},
		{"maxFrameBytes", limits.MaxFrameBytes, LanguageServerMaxFrameBytes},
		{"maxResultBytes", limits.MaxResultBytes, LanguageServerMaxResultBytes},
	}
	for _, limit := range byteLimits {
		if limit.value <= 0 {
			return invalid("invalid_language_server_limit", "limits."+limit.name, "must be positive")
		}
		if limit.value > limit.cap {
			return invalid("language_server_limit_exceeds_hard_cap", "limits."+limit.name, fmt.Sprintf("must not exceed %d", limit.cap))
		}
	}
	if limits.RequestBurst < limits.RequestsPerSecond {
		return invalid("invalid_language_server_limit", "limits.requestBurst", "must be at least requestsPerSecond")
	}
	if limits.MaxTotalSyncBytes < limits.MaxDocumentBytes {
		return invalid("invalid_language_server_limit", "limits.maxTotalSyncBytes", "must be at least maxDocumentBytes")
	}
	if limits.MaxResultBytes < limits.MaxFrameBytes {
		return invalid("invalid_language_server_limit", "limits.maxResultBytes", "must be at least maxFrameBytes")
	}
	return nil
}

func normalizeLanguageServerIsolation(isolation LanguageServerIsolation) (LanguageServerIsolation, error) {
	policies := []struct {
		field    string
		value    *string
		expected string
	}{
		{"networkPolicy", &isolation.NetworkPolicy, "none"},
		{"workspaceMountPolicy", &isolation.WorkspaceMountPolicy, "read-only"},
		{"tempPolicy", &isolation.TempPolicy, "isolated-bounded"},
		{"cachePolicy", &isolation.CachePolicy, "isolated-bounded"},
		{"workspacePluginPolicy", &isolation.WorkspacePluginPolicy, "forbidden"},
		{"dynamicSdkPolicy", &isolation.DynamicSDKPolicy, "forbidden"},
		{"dynamicRegistrationPolicy", &isolation.DynamicRegistrationPolicy, "forbidden"},
		{"configurationCommandPolicy", &isolation.ConfigurationCommandPolicy, "forbidden"},
		{"packageManagerHookPolicy", &isolation.PackageManagerHookPolicy, "forbidden"},
	}
	for _, policy := range policies {
		*policy.value = strings.TrimSpace(*policy.value)
		if *policy.value != policy.expected {
			return LanguageServerIsolation{}, invalid("invalid_language_server_isolation", "isolation."+policy.field, "must be "+policy.expected)
		}
	}
	return isolation, nil
}

func hashLanguageServerProfile(profile LanguageServerProfile) (string, error) {
	return canonicalHash(languageServerProfileHashPayload{
		SchemaVersion:                profile.SchemaVersion,
		ID:                           profile.ID,
		ServiceID:                    profile.ServiceID,
		LanguageIDs:                  profile.LanguageIDs,
		FileGlobs:                    profile.FileGlobs,
		ProtocolVersion:              profile.ProtocolVersion,
		Runtime:                      profile.Runtime,
		ServerInfo:                   profile.ServerInfo,
		InitializationParametersHash: profile.InitializationParametersHash,
		WorkspaceConfigurationHash:   profile.WorkspaceConfigurationHash,
		RequireVersionedDiagnostics:  profile.RequireVersionedDiagnostics,
		Methods:                      profile.Methods,
		CapabilityHash:               profile.CapabilityHash,
		Limits:                       profile.Limits,
		Isolation:                    profile.Isolation,
	})
}

func prefixTemplateErrorField(err error, prefix string) error {
	templateErr, ok := err.(*Error)
	if !ok {
		return err
	}
	copy := *templateErr
	if copy.Field == "" {
		copy.Field = prefix
	} else {
		copy.Field = prefix + "." + copy.Field
	}
	return &copy
}
