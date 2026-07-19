package templates

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

type legacyTemplateManifestHashPayload struct {
	SchemaVersion     string                `json:"schemaVersion"`
	TemplateID        string                `json:"templateId"`
	DisplayName       string                `json:"displayName"`
	Version           string                `json:"version"`
	Services          []TemplateService     `json:"services"`
	Toolchains        []Toolchain           `json:"toolchains"`
	Commands          map[string]Command    `json:"commands"`
	Ports             []Port                `json:"ports"`
	HealthChecks      []HealthCheck         `json:"healthChecks"`
	Migration         *MigrationCommand     `json:"migration,omitempty"`
	BuildOutputs      []BuildOutput         `json:"buildOutputs"`
	ExtensionPaths    []string              `json:"extensionPaths"`
	ProtectedPaths    []string              `json:"protectedPaths"`
	EnvironmentSchema []EnvironmentVariable `json:"environmentSchema"`
	Lockfiles         []Lockfile            `json:"lockfiles"`
	ProfileDigest     string                `json:"profileDigest"`
}

func TestLanguageServerProfilesAreOptionalWithoutChangingLegacyManifestHash(t *testing.T) {
	manifest, err := normalizeManifest(validCandidate("legacy-api-template", "api").Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.LanguageServers != nil {
		t.Fatalf("absent languageServers normalized to a present value: %#v", manifest.LanguageServers)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"languageServers"`) {
		t.Fatalf("legacy manifest wire payload grew an optional field: %s", encoded)
	}

	legacy := legacyTemplateManifestHashPayload{
		SchemaVersion: manifest.SchemaVersion, TemplateID: manifest.TemplateID,
		DisplayName: manifest.DisplayName, Version: manifest.Version,
		Services: manifest.Services, Toolchains: manifest.Toolchains, Commands: manifest.Commands,
		Ports: manifest.Ports, HealthChecks: manifest.HealthChecks, Migration: manifest.Migration,
		BuildOutputs: manifest.BuildOutputs, ExtensionPaths: manifest.ExtensionPaths,
		ProtectedPaths: manifest.ProtectedPaths, EnvironmentSchema: manifest.EnvironmentSchema,
		Lockfiles: manifest.Lockfiles, ProfileDigest: manifest.ProfileDigest,
	}
	currentHash, err := canonicalHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	legacyHash, err := canonicalHash(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if currentHash != legacyHash {
		t.Fatalf("omitempty languageServers changed the legacy canonical hash: current=%s legacy=%s", currentHash, legacyHash)
	}
}

func TestLanguageServerProfileAdmissionCanonicalizesAndDeepClonesIdentity(t *testing.T) {
	candidate := validCandidate("lsp-api-template", "api")
	profile := validLanguageServerProfile(t, "python-lsp", "api")
	candidate.Manifest.LanguageServers = []LanguageServerProfile{profile}

	attempt, err := NewAdmissionAttempt(attemptID, actorID, candidate, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	admitted := attempt.Snapshot().Candidate.Manifest.LanguageServers[0]
	if !slices.IsSorted(admitted.LanguageIDs) || !slices.IsSorted(admitted.FileGlobs) || !slices.IsSorted(admitted.Methods) {
		t.Fatalf("profile sets are not canonical: %#v", admitted)
	}
	capabilityHash, err := ComputeLanguageServerCapabilityHash(admitted.Methods)
	if err != nil {
		t.Fatal(err)
	}
	contentHash, err := ComputeLanguageServerProfileContentHash(admitted)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.CapabilityHash != capabilityHash || admitted.ContentHash != contentHash {
		t.Fatalf("independent commitments differ: capability=%s/%s profile=%s/%s", admitted.CapabilityHash, capabilityHash, admitted.ContentHash, contentHash)
	}

	release := approvedRelease(t, secondAttempt, secondRelease, candidate, true)
	copy := release.Snapshot()
	copy.Manifest.LanguageServers[0].Methods[0] = "mutated/method"
	copy.Manifest.LanguageServers[0].Runtime.Argv[0] = "/tmp/mutated"
	copy.Manifest.LanguageServers[0].LanguageIDs[0] = "mutated"
	fresh := release.Snapshot().Manifest.LanguageServers[0]
	if fresh.Methods[0] == "mutated/method" || fresh.Runtime.Argv[0] == "/tmp/mutated" || fresh.LanguageIDs[0] == "mutated" {
		t.Fatal("immutable TemplateRelease snapshot exposed nested language-server profile storage")
	}
}

func TestLanguageServerProfileOrderNormalizesDeterministically(t *testing.T) {
	first := validLanguageServerProfile(t, "python-lsp", "api")
	second := validLanguageServerProfile(t, "python-analysis", "api")
	first.LanguageIDs = slices.Clone(first.LanguageIDs)
	first.FileGlobs = slices.Clone(first.FileGlobs)
	first.Methods = slices.Clone(first.Methods)
	slices.Reverse(first.LanguageIDs)
	slices.Reverse(first.FileGlobs)
	slices.Reverse(first.Methods)
	sealLanguageServerProfile(t, &first)

	left := validCandidate("ordered-lsp-template", "api")
	left.Manifest.LanguageServers = []LanguageServerProfile{first, second}
	right := validCandidate("ordered-lsp-template", "api")
	rightFirst := validLanguageServerProfile(t, "python-lsp", "api")
	rightSecond := validLanguageServerProfile(t, "python-analysis", "api")
	right.Manifest.LanguageServers = []LanguageServerProfile{rightSecond, rightFirst}

	leftAttempt, err := NewAdmissionAttempt(attemptID, actorID, left, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	rightAttempt, err := NewAdmissionAttempt(secondAttempt, actorID, right, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	leftCandidate := leftAttempt.Snapshot().Candidate
	rightCandidate := rightAttempt.Snapshot().Candidate
	leftSubject := leftAttempt.Snapshot().SubjectHash
	rightSubject := rightAttempt.Snapshot().SubjectHash
	if !reflect.DeepEqual(leftCandidate, rightCandidate) || leftSubject != rightSubject {
		t.Fatalf("equivalent profile order produced different authority: left=%s right=%s", leftSubject, rightSubject)
	}
	if leftCandidate.Manifest.LanguageServers[0].ID != "python-analysis" || leftCandidate.Manifest.LanguageServers[1].ID != "python-lsp" {
		t.Fatalf("profiles were not sorted by canonical ID: %#v", leftCandidate.Manifest.LanguageServers)
	}
	if first.ContentHash != rightFirst.ContentHash || first.CapabilityHash != rightFirst.CapabilityHash {
		t.Fatal("set order changed an independently computable profile commitment")
	}
}

func TestLanguageServerProfileAdmissionFailsClosedOnDriftAndForbiddenCapability(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		mutate func(*LanguageServerProfile)
		reseal bool
	}{
		{"schema", "unsupported_language_server_profile_schema", func(p *LanguageServerProfile) { p.SchemaVersion = "language-server-profile/v2" }, true},
		{"profile ID", "invalid_language_server_profile_id", func(p *LanguageServerProfile) { p.ID = "Python LSP" }, true},
		{"service binding", "language_server_service_not_found", func(p *LanguageServerProfile) { p.ServiceID = "missing" }, true},
		{"duplicate language", "duplicate_language_server_language", func(p *LanguageServerProfile) { p.LanguageIDs = append(p.LanguageIDs, p.LanguageIDs[0]) }, true},
		{"duplicate glob", "duplicate_language_server_glob", func(p *LanguageServerProfile) { p.FileGlobs = append(p.FileGlobs, p.FileGlobs[0]) }, true},
		{"unsafe glob", "invalid_language_server_glob", func(p *LanguageServerProfile) { p.FileGlobs = []string{"../**/*.py"} }, true},
		{"protocol", "unsupported_language_server_protocol", func(p *LanguageServerProfile) { p.ProtocolVersion = "3.18" }, true},
		{"runtime image unpinned", "language_server_runtime_not_pinned", func(p *LanguageServerProfile) { p.Runtime.Image = "ghcr.io/worksflow/python-lsp:latest" }, true},
		{"runtime image exact drift", "language_server_profile_hash_mismatch", func(p *LanguageServerProfile) { p.Runtime.Image = "ghcr.io/worksflow/python-lsp@" + digest("9") }, false},
		{"relative executable", "invalid_language_server_executable", func(p *LanguageServerProfile) {
			p.Runtime.ExecutablePath = "bin/pyright-langserver"
			p.Runtime.Argv[0] = p.Runtime.ExecutablePath
		}, true},
		{"executable digest exact drift", "language_server_profile_hash_mismatch", func(p *LanguageServerProfile) { p.Runtime.ExecutableDigest = digest("9") }, false},
		{"executable argv mismatch", "language_server_executable_mismatch", func(p *LanguageServerProfile) { p.Runtime.Argv[0] = "/opt/lsp/bin/other" }, true},
		{"shell executable", "language_server_shell_forbidden", func(p *LanguageServerProfile) {
			p.Runtime.ExecutablePath = "/bin/sh"
			p.Runtime.Argv = []string{"/bin/sh", "-c", "server"}
		}, true},
		{"working directory", "invalid_language_server_working_directory", func(p *LanguageServerProfile) { p.Runtime.WorkingDirectoryPolicy = "workspace-root" }, true},
		{"server info missing", "invalid_language_server_info", func(p *LanguageServerProfile) { p.ServerInfo.Name = "" }, true},
		{"server info exact drift", "language_server_profile_hash_mismatch", func(p *LanguageServerProfile) { p.ServerInfo.Version = "1.2.4" }, false},
		{"initialization hash malformed", "invalid_digest", func(p *LanguageServerProfile) { p.InitializationParametersHash = "latest" }, true},
		{"configuration hash malformed", "invalid_digest", func(p *LanguageServerProfile) { p.WorkspaceConfigurationHash = "latest" }, true},
		{"custom initialization policy", "unsupported_language_server_initialization_policy", func(p *LanguageServerProfile) { p.InitializationParametersHash = digest("8") }, true},
		{"custom workspace configuration", "unsupported_language_server_workspace_configuration", func(p *LanguageServerProfile) { p.WorkspaceConfigurationHash = digest("9") }, true},
		{"versioned diagnostics", "versioned_diagnostics_required", func(p *LanguageServerProfile) { p.RequireVersionedDiagnostics = false }, true},
		{"duplicate method", "duplicate_language_server_method", func(p *LanguageServerProfile) { p.Methods = append(p.Methods, p.Methods[0]) }, true},
		{"unknown method", "unsupported_language_server_method", func(p *LanguageServerProfile) { p.Methods = []string{"textDocument/notReal"} }, true},
		{"apply edit", "forbidden_language_server_method", func(p *LanguageServerProfile) { p.Methods = []string{"workspace/applyEdit"} }, true},
		{"execute command", "forbidden_language_server_method", func(p *LanguageServerProfile) { p.Methods = []string{"workspace/executeCommand"} }, true},
		{"rename", "forbidden_language_server_method", func(p *LanguageServerProfile) { p.Methods = []string{"textDocument/rename"} }, true},
		{"formatting", "forbidden_language_server_method", func(p *LanguageServerProfile) { p.Methods = []string{"textDocument/formatting"} }, true},
		{"code action", "forbidden_language_server_method", func(p *LanguageServerProfile) { p.Methods = []string{"textDocument/codeAction"} }, true},
		{"capability drift", "language_server_capability_hash_mismatch", func(p *LanguageServerProfile) { p.Methods = append(p.Methods, "textDocument/signatureHelp") }, false},
		{"capability hash", "language_server_capability_hash_mismatch", func(p *LanguageServerProfile) { p.CapabilityHash = digest("9") }, false},
		{"profile self hash", "language_server_profile_hash_mismatch", func(p *LanguageServerProfile) { p.ContentHash = digest("9") }, false},
		{"network", "invalid_language_server_isolation", func(p *LanguageServerProfile) { p.Isolation.NetworkPolicy = "egress" }, true},
		{"workspace mount", "invalid_language_server_isolation", func(p *LanguageServerProfile) { p.Isolation.WorkspaceMountPolicy = "read-write" }, true},
		{"workspace plugin", "invalid_language_server_isolation", func(p *LanguageServerProfile) { p.Isolation.WorkspacePluginPolicy = "allowed" }, true},
		{"dynamic SDK", "invalid_language_server_isolation", func(p *LanguageServerProfile) { p.Isolation.DynamicSDKPolicy = "allowed" }, true},
		{"dynamic registration", "invalid_language_server_isolation", func(p *LanguageServerProfile) { p.Isolation.DynamicRegistrationPolicy = "allowed" }, true},
		{"configuration command", "invalid_language_server_isolation", func(p *LanguageServerProfile) { p.Isolation.ConfigurationCommandPolicy = "allowed" }, true},
		{"package hook", "invalid_language_server_isolation", func(p *LanguageServerProfile) { p.Isolation.PackageManagerHookPolicy = "allowed" }, true},
		{"tmp policy", "invalid_language_server_isolation", func(p *LanguageServerProfile) { p.Isolation.TempPolicy = "shared" }, true},
		{"cache policy", "invalid_language_server_isolation", func(p *LanguageServerProfile) { p.Isolation.CachePolicy = "workspace" }, true},
		{"zero request timeout", "invalid_language_server_limit", func(p *LanguageServerProfile) { p.Limits.RequestTimeoutMillis = 0 }, true},
		{"startup hard cap", "language_server_limit_exceeds_hard_cap", func(p *LanguageServerProfile) {
			p.Limits.StartupTimeoutMillis = LanguageServerMaxStartupTimeoutMillis + 1
		}, true},
		{"CPU hard cap", "language_server_limit_exceeds_hard_cap", func(p *LanguageServerProfile) { p.Limits.CPUMillis = LanguageServerMaxCPUMillis + 1 }, true},
		{"memory hard cap", "language_server_limit_exceeds_hard_cap", func(p *LanguageServerProfile) { p.Limits.MemoryBytes = LanguageServerMaxMemoryBytes + 1 }, true},
		{"PID hard cap", "language_server_limit_exceeds_hard_cap", func(p *LanguageServerProfile) { p.Limits.PIDLimit = LanguageServerMaxPIDs + 1 }, true},
		{"temporary storage hard cap", "language_server_limit_exceeds_hard_cap", func(p *LanguageServerProfile) { p.Limits.TempBytes = LanguageServerMaxTempBytes + 1 }, true},
		{"document hard cap", "language_server_limit_exceeds_hard_cap", func(p *LanguageServerProfile) { p.Limits.MaxDocumentBytes = LanguageServerMaxDocumentBytes + 1 }, true},
		{"open document hard cap", "language_server_limit_exceeds_hard_cap", func(p *LanguageServerProfile) { p.Limits.MaxOpenDocuments = LanguageServerMaxOpenDocuments + 1 }, true},
		{"frame hard cap", "language_server_limit_exceeds_hard_cap", func(p *LanguageServerProfile) { p.Limits.MaxFrameBytes = LanguageServerMaxFrameBytes + 1 }, true},
		{"result hard cap", "language_server_limit_exceeds_hard_cap", func(p *LanguageServerProfile) { p.Limits.MaxResultBytes = LanguageServerMaxResultBytes + 1 }, true},
		{"concurrency hard cap", "language_server_limit_exceeds_hard_cap", func(p *LanguageServerProfile) {
			p.Limits.MaxConcurrentRequests = LanguageServerMaxConcurrentRequests + 1
		}, true},
		{"rate hard cap", "language_server_limit_exceeds_hard_cap", func(p *LanguageServerProfile) { p.Limits.RequestsPerSecond = LanguageServerMaxRequestsPerSecond + 1 }, true},
		{"diagnostic result hard cap", "language_server_limit_exceeds_hard_cap", func(p *LanguageServerProfile) {
			p.Limits.MaxDiagnosticsPerDocument = LanguageServerMaxDiagnosticsPerDocument + 1
		}, true},
		{"completion result hard cap", "language_server_limit_exceeds_hard_cap", func(p *LanguageServerProfile) { p.Limits.MaxCompletionItems = LanguageServerMaxCompletionItems + 1 }, true},
		{"navigation result hard cap", "language_server_limit_exceeds_hard_cap", func(p *LanguageServerProfile) {
			p.Limits.MaxNavigationLocations = LanguageServerMaxNavigationLocations + 1
		}, true},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validCandidate("invalid-lsp-template", "api")
			profile := validLanguageServerProfile(t, "python-lsp", "api")
			test.mutate(&profile)
			if test.reseal {
				sealLanguageServerProfileUnchecked(t, &profile)
			}
			candidate.Manifest.LanguageServers = []LanguageServerProfile{profile}
			_, err := NewAdmissionAttempt(attemptID, actorID, candidate, baseTime)
			if err == nil {
				t.Fatalf("invalid profile escaped admission: %#v", profile)
			}
			var templateErr *Error
			if !errors.As(err, &templateErr) || templateErr.Code != test.code {
				t.Fatalf("case %d expected %q, got %v", index, test.code, err)
			}
		})
	}

	t.Run("duplicate profile identity", func(t *testing.T) {
		candidate := validCandidate("duplicate-lsp-template", "api")
		profile := validLanguageServerProfile(t, "python-lsp", "api")
		candidate.Manifest.LanguageServers = []LanguageServerProfile{profile, profile}
		_, err := NewAdmissionAttempt(attemptID, actorID, candidate, baseTime)
		var templateErr *Error
		if !errors.As(err, &templateErr) || templateErr.Code != "duplicate_language_server_profile" {
			t.Fatalf("duplicate profile expected deterministic rejection, got %v", err)
		}
	})
}

func TestLanguageServerBaselineCannotBeMutatedByCaller(t *testing.T) {
	methods := LanguageServerBaselineMethods()
	methods[0] = "workspace/applyEdit"
	if LanguageServerBaselineMethods()[0] == methods[0] {
		t.Fatal("baseline method authority exposed mutable storage")
	}
}

func TestValidateLanguageServerProfileRequiresExactCanonicalObject(t *testing.T) {
	profile := validLanguageServerProfile(t, "python-lsp", "api")
	normalized, err := normalizeLanguageServerProfile(profile, "languageServerProfile")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateLanguageServerProfile(normalized); err != nil {
		t.Fatalf("canonical exact profile was rejected: %v", err)
	}

	unsorted := normalized
	unsorted.Methods = slices.Clone(normalized.Methods)
	slices.Reverse(unsorted.Methods)
	if err := ValidateLanguageServerProfile(unsorted); errorCode(err) != "noncanonical_language_server_profile" {
		t.Fatalf("unsorted exact object expected noncanonical rejection, got %v", err)
	}

	drifted := normalized
	drifted.ContentHash = digest("0")
	if err := ValidateLanguageServerProfile(drifted); errorCode(err) != "language_server_profile_hash_mismatch" {
		t.Fatalf("self-hash drift expected exact rejection, got %v", err)
	}
}

func validLanguageServerProfile(t *testing.T, id, serviceID string) LanguageServerProfile {
	t.Helper()
	profile := LanguageServerProfile{
		SchemaVersion:   LanguageServerProfileSchemaVersion,
		ID:              id,
		ServiceID:       serviceID,
		LanguageIDs:     []string{"python-django", "python"},
		FileGlobs:       []string{"src/**/*.py", "**/*.py"},
		ProtocolVersion: "3.17",
		Runtime: LanguageServerRuntime{
			Image:                  "ghcr.io/worksflow/python-lsp@" + digest("6"),
			ExecutablePath:         "/opt/lsp/bin/pyright-langserver",
			ExecutableDigest:       digest("7"),
			Argv:                   []string{"/opt/lsp/bin/pyright-langserver", "--stdio"},
			WorkingDirectoryPolicy: "service-root",
		},
		ServerInfo:                   LanguageServerInfo{Name: "pyright", Version: "1.2.3"},
		InitializationParametersHash: ProductionV1InitializationParametersHash(),
		WorkspaceConfigurationHash:   ProductionV1WorkspaceConfigurationHash(),
		RequireVersionedDiagnostics:  true,
		Methods: []string{
			"textDocument/references",
			"textDocument/completion",
			"textDocument/publishDiagnostics",
			"textDocument/hover",
		},
		Limits: LanguageServerLimits{
			StartupTimeoutMillis: 10_000, RequestTimeoutMillis: 5_000, ShutdownTimeoutMillis: 2_000,
			CPUMillis: 1_000, MemoryBytes: 512 << 20, PIDLimit: 64, TempBytes: 256 << 20, CacheBytes: 256 << 20,
			MaxOpenDocuments: 16, MaxDocumentBytes: 512 << 10, MaxTotalSyncBytes: 4 << 20,
			MaxFrameBytes: 256 << 10, MaxResultBytes: 512 << 10, MaxConcurrentRequests: 16,
			RequestsPerSecond: 15, RequestBurst: 30, MaxDiagnosticsPerDocument: 1_000,
			MaxCompletionItems: 250, MaxNavigationLocations: 2_500,
		},
		Isolation: LanguageServerIsolation{
			NetworkPolicy: "none", WorkspaceMountPolicy: "read-only",
			TempPolicy: "isolated-bounded", CachePolicy: "isolated-bounded",
			WorkspacePluginPolicy: "forbidden", DynamicSDKPolicy: "forbidden",
			DynamicRegistrationPolicy: "forbidden", ConfigurationCommandPolicy: "forbidden",
			PackageManagerHookPolicy: "forbidden",
		},
	}
	sealLanguageServerProfile(t, &profile)
	return profile
}

func sealLanguageServerProfile(t *testing.T, profile *LanguageServerProfile) {
	t.Helper()
	capabilityHash, err := ComputeLanguageServerCapabilityHash(profile.Methods)
	if err != nil {
		t.Fatal(err)
	}
	profile.CapabilityHash = capabilityHash
	contentHash, err := ComputeLanguageServerProfileContentHash(*profile)
	if err != nil {
		t.Fatal(err)
	}
	profile.ContentHash = contentHash
}

// This helper deliberately hashes an invalid declaration without validating
// it. Negative tests therefore prove the targeted admission rule rejects a
// self-consistent malicious profile rather than failing only because its
// contentHash was stale.
func sealLanguageServerProfileUnchecked(t *testing.T, profile *LanguageServerProfile) {
	t.Helper()
	methods := slices.Clone(profile.Methods)
	sort.Strings(methods)
	capabilityHash, err := canonicalHash(languageServerCapabilityHashPayload{
		SchemaVersion: LanguageServerCapabilitySchemaVersion,
		Methods:       methods,
	})
	if err != nil {
		t.Fatal(err)
	}
	profile.CapabilityHash = capabilityHash
	canonical := *profile
	canonical.LanguageIDs = slices.Clone(profile.LanguageIDs)
	canonical.FileGlobs = slices.Clone(profile.FileGlobs)
	canonical.Methods = methods
	sort.Strings(canonical.LanguageIDs)
	sort.Strings(canonical.FileGlobs)
	canonical.ContentHash = ""
	contentHash, err := hashLanguageServerProfile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	profile.ContentHash = contentHash
}

func errorCode(err error) string {
	var templateErr *Error
	if errors.As(err, &templateErr) {
		return templateErr.Code
	}
	return ""
}
