package modelgovernance

import (
	"bytes"
	"strings"
	"testing"
)

func TestModelProfileCanonicalRoundTripAndHashFence(t *testing.T) {
	profile := validModelProfile()
	encoded, err := CanonicalModelProfileJSON(profile)
	if err != nil {
		t.Fatalf("canonicalize ModelProfile: %v", err)
	}
	digest, err := ModelProfileHash(profile)
	if err != nil {
		t.Fatalf("hash ModelProfile: %v", err)
	}
	if digest != sha256Digest(encoded) {
		t.Fatalf("profile digest does not cover canonical bytes: %s", digest)
	}
	if digest != "sha256:c574e5552f4bc892355ee810cfdc64716bd2ea48c913273f606318700e63f955" {
		t.Fatalf("canonical ModelProfile vector drifted: %s", digest)
	}
	parsed, err := ParseModelProfile(encoded, digest)
	if err != nil {
		t.Fatalf("parse canonical ModelProfile: %v", err)
	}
	second, err := CanonicalModelProfileJSON(parsed)
	if err != nil || !bytes.Equal(second, encoded) {
		t.Fatalf("ModelProfile canonical representation drifted: %v", err)
	}

	wrong := "sha256:" + strings.Repeat("f", 64)
	if wrong == digest {
		wrong = "sha256:" + strings.Repeat("e", 64)
	}
	if _, err := ParseModelProfile(encoded, wrong); err == nil || !strings.Contains(err.Error(), "hash drift") {
		t.Fatalf("hash drift was accepted: %v", err)
	}
	if _, err := ParseModelProfile(encoded, "latest"); err == nil {
		t.Fatal("non-canonical expected hash was accepted")
	}
}

func TestParseModelProfileRejectsNonStrictOrNonCanonicalJSON(t *testing.T) {
	encoded := mustCanonicalProfile(t, validModelProfile())
	digest := sha256Digest(encoded)
	schema := `"schemaVersion":"` + ModelProfileSchemaVersion + `"`
	canonicalPrefix := "{" + schema + `,"id":"11111111-1111-4111-8111-111111111111"`
	reorderedPrefix := `{"id":"11111111-1111-4111-8111-111111111111",` + schema
	invalidUTF8 := append([]byte(nil), encoded...)
	invalidIndex := bytes.Index(invalidUTF8, []byte("constructor-implementation"))
	if invalidIndex < 0 {
		t.Fatal("profile fixture workload is missing")
	}
	invalidUTF8[invalidIndex] = 0xff

	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "unknown", payload: appendBeforeClosing(encoded, `,"unexpected":true`)},
		{name: "nested unknown", payload: []byte(strings.Replace(string(encoded), `"kind":"codex-cli"`, `"kind":"codex-cli","tag":"latest"`, 1))},
		{name: "nested duplicate", payload: []byte(strings.Replace(string(encoded), `"kind":"codex-cli"`, `"kind":"codex-cli","kind":"codex-cli"`, 1))},
		{name: "null scalar", payload: []byte(strings.Replace(string(encoded), `"workload":"constructor-implementation"`, `"workload":null`, 1))},
		{name: "null boolean", payload: []byte(strings.Replace(string(encoded), `"streaming":true`, `"streaming":null`, 1))},
		{name: "null array", payload: []byte(strings.Replace(string(encoded), `"profiles":[]`, `"profiles":null`, 1))},
		{name: "duplicate", payload: []byte(strings.Replace(string(encoded), "{", "{"+schema+",", 1))},
		{name: "trailing", payload: append(append([]byte(nil), encoded...), []byte(`{}`)...)},
		{name: "bom", payload: append([]byte{0xef, 0xbb, 0xbf}, encoded...)},
		{name: "invalid utf8", payload: invalidUTF8},
		{name: "pretty whitespace", payload: append([]byte(" "), encoded...)},
		{name: "different field order", payload: []byte(strings.Replace(string(encoded), canonicalPrefix, reorderedPrefix, 1))},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseModelProfile(test.payload, digest); err == nil {
				t.Fatal("non-strict/non-canonical ModelProfile was accepted")
			}
		})
	}
}

func TestValidateModelProfileRejectsMutableOrOpenEndedBindings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ModelProfile)
	}{
		{name: "old schema", mutate: func(value *ModelProfile) { value.SchemaVersion = "worksflow-model-profile/v0" }},
		{name: "invalid id", mutate: func(value *ModelProfile) { value.ID = "profile-latest" }},
		{name: "noncanonical workload", mutate: func(value *ModelProfile) { value.Workload = " Constructor " }},
		{name: "open protocol", mutate: func(value *ModelProfile) { value.Provider.Protocol = "anthropic-messages/v1" }},
		{name: "network executable route", mutate: func(value *ModelProfile) { value.Provider.RouteID = "https://api.openai.com/v1/responses" }},
		{name: "mutable route id", mutate: func(value *ModelProfile) { value.Provider.RouteID = "OpenAI Latest" }},
		{name: "missing route authority", mutate: func(value *ModelProfile) { value.Provider.RouteAuthorityHash = "" }},
		{name: "mutable route authority", mutate: func(value *ModelProfile) { value.Provider.RouteAuthorityHash = "route:latest" }},
		{name: "latest model", mutate: func(value *ModelProfile) {
			value.Provider.RequestedModel = "gpt-5-codex-latest"
			value.Provider.AllowedResolvedModels = []string{"gpt-5-codex-latest"}
		}},
		{name: "bare alias", mutate: func(value *ModelProfile) {
			value.Provider.RequestedModel = "gpt-5-codex"
			value.Provider.AllowedResolvedModels = []string{"gpt-5-codex"}
		}},
		{name: "regex model", mutate: func(value *ModelProfile) {
			value.Provider.RequestedModel = "gpt-5-.*"
			value.Provider.AllowedResolvedModels = []string{"gpt-5-.*"}
		}},
		{name: "invalid snapshot date", mutate: func(value *ModelProfile) {
			value.Provider.RequestedModel = "gpt-5-codex-2026-99-99"
			value.Provider.AllowedResolvedModels = []string{"gpt-5-codex-2026-99-99"}
		}},
		{name: "unsorted resolved models", mutate: func(value *ModelProfile) {
			value.Provider.AllowedResolvedModels = []string{"gpt-6-codex-2026-07-18", value.Provider.RequestedModel}
		}},
		{name: "requested model not allowed", mutate: func(value *ModelProfile) {
			value.Provider.AllowedResolvedModels = []string{"gpt-6-codex-2026-07-18"}
		}},
		{name: "null resolved models", mutate: func(value *ModelProfile) { value.Provider.AllowedResolvedModels = nil }},
		{name: "open runner", mutate: func(value *ModelProfile) { value.Runner.Kind = "custom-runner" }},
		{name: "mutable runner", mutate: func(value *ModelProfile) { value.Runner.ImmutableDigest = "codex:latest" }},
		{name: "missing policy hash", mutate: func(value *ModelProfile) { value.Execution.PolicyHash = "" }},
		{name: "parallel tools without tools", mutate: func(value *ModelProfile) { value.Capabilities.ToolCalls = false }},
		{name: "unbounded context", mutate: func(value *ModelProfile) { value.Limits.ContextWindowTokens = 0 }},
		{name: "token overflow", mutate: func(value *ModelProfile) { value.Limits.MaxInputTokens = value.Limits.ContextWindowTokens }},
		{name: "tool calls without limit", mutate: func(value *ModelProfile) { value.Limits.MaxToolCalls = 0 }},
		{name: "null fallback", mutate: func(value *ModelProfile) { value.Fallback.Profiles = nil }},
		{name: "disabled fallback names target", mutate: func(value *ModelProfile) {
			value.Fallback.Profiles = []FallbackProfileBinding{{ID: "22222222-2222-4222-8222-222222222222", ContentHash: testDigest("fallback"), Workload: value.Workload}}
		}},
		{name: "fallback self reference", mutate: func(value *ModelProfile) {
			value.Fallback.Enabled = true
			value.Fallback.Profiles = []FallbackProfileBinding{{ID: value.ID, ContentHash: testDigest("fallback"), Workload: value.Workload}}
			value.Fallback.OnConditions = []string{"provider-unavailable"}
		}},
		{name: "fallback open condition", mutate: func(value *ModelProfile) {
			value.Fallback.Enabled = true
			value.Fallback.Profiles = []FallbackProfileBinding{{ID: "22222222-2222-4222-8222-222222222222", ContentHash: testDigest("fallback"), Workload: value.Workload}}
			value.Fallback.OnConditions = []string{"any-error"}
		}},
		{name: "fallback mutable profile", mutate: func(value *ModelProfile) {
			value.Fallback.Enabled = true
			value.Fallback.Profiles = []FallbackProfileBinding{{ID: "22222222-2222-4222-8222-222222222222", ContentHash: "latest", Workload: value.Workload}}
			value.Fallback.OnConditions = []string{"provider-unavailable"}
		}},
		{name: "fallback workload drift", mutate: func(value *ModelProfile) {
			value.Fallback.Enabled = true
			value.Fallback.Profiles = []FallbackProfileBinding{{ID: "22222222-2222-4222-8222-222222222222", ContentHash: testDigest("fallback"), Workload: "constructor-planning"}}
			value.Fallback.OnConditions = []string{"provider-unavailable"}
		}},
		{name: "fallback profiles unsorted", mutate: func(value *ModelProfile) {
			value.Fallback.Enabled = true
			value.Fallback.Profiles = []FallbackProfileBinding{
				{ID: "33333333-3333-4333-8333-333333333333", ContentHash: testDigest("fallback-3"), Workload: value.Workload},
				{ID: "22222222-2222-4222-8222-222222222222", ContentHash: testDigest("fallback-2"), Workload: value.Workload},
			}
			value.Fallback.OnConditions = []string{"provider-unavailable"}
		}},
		{name: "null disable conditions", mutate: func(value *ModelProfile) { value.DisableConditions = nil }},
		{name: "missing fail closed condition", mutate: func(value *ModelProfile) {
			value.DisableConditions = []string{"conformance-regression", "provider-model-drift", "provider-route-drift", "runner-drift"}
		}},
		{name: "unsorted disable conditions", mutate: func(value *ModelProfile) {
			value.DisableConditions[0], value.DisableConditions[1] = value.DisableConditions[1], value.DisableConditions[0]
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := validModelProfile()
			test.mutate(&profile)
			if err := ValidateModelProfile(profile); err == nil {
				t.Fatal("invalid ModelProfile was accepted")
			}
		})
	}
}

func TestValidateModelProfileAcceptsOnlyDigestPinnedExplicitFallback(t *testing.T) {
	profile := validModelProfile()
	profile.Fallback = FallbackPolicy{
		Enabled: true,
		Profiles: []FallbackProfileBinding{
			{ID: "22222222-2222-4222-8222-222222222222", ContentHash: testDigest("fallback-profile"), Workload: profile.Workload},
		},
		OnConditions: []string{"provider-timeout", "provider-unavailable"},
	}
	if err := ValidateModelProfile(profile); err != nil {
		t.Fatalf("exact explicit fallback was rejected: %v", err)
	}
}

func TestValidateModelProfileGraphClosesFallbackTargetsAndApprovalBindings(t *testing.T) {
	primary := validModelProfile()
	fallback := validModelProfile()
	fallback.ID = "22222222-2222-4222-8222-222222222222"
	fallbackHash, err := ModelProfileHash(fallback)
	if err != nil {
		t.Fatalf("hash fallback profile: %v", err)
	}
	primary.Fallback = FallbackPolicy{
		Enabled: true,
		Profiles: []FallbackProfileBinding{
			{ID: fallback.ID, ContentHash: fallbackHash, Workload: fallback.Workload},
		},
		OnConditions: []string{"provider-unavailable"},
	}
	bindings := []ProfileAuthorityBinding{
		profileAuthorityBinding(t, primary, "primary-approval"),
		profileAuthorityBinding(t, fallback, "fallback-approval"),
	}
	if err := ValidateModelProfileGraph(bindings); err != nil {
		t.Fatalf("exact acyclic approved profile graph was rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func([]ProfileAuthorityBinding) []ProfileAuthorityBinding
	}{
		{name: "null graph", mutate: func([]ProfileAuthorityBinding) []ProfileAuthorityBinding { return nil }},
		{name: "unresolved target", mutate: func(value []ProfileAuthorityBinding) []ProfileAuthorityBinding { return value[:1] }},
		{name: "target hash drift", mutate: func(value []ProfileAuthorityBinding) []ProfileAuthorityBinding {
			value[0].Profile.Fallback.Profiles = append([]FallbackProfileBinding(nil), value[0].Profile.Fallback.Profiles...)
			value[0].Profile.Fallback.Profiles[0].ContentHash = testDigest("wrong-target")
			value[0] = profileAuthorityBinding(t, value[0].Profile, "primary-approval")
			return value
		}},
		{name: "member hash drift", mutate: func(value []ProfileAuthorityBinding) []ProfileAuthorityBinding {
			value[1].ContentHash = testDigest("wrong-member")
			return value
		}},
		{name: "missing approval binding", mutate: func(value []ProfileAuthorityBinding) []ProfileAuthorityBinding {
			value[1].ApprovalReceiptDigest = ""
			return value
		}},
		{name: "unsorted graph", mutate: func(value []ProfileAuthorityBinding) []ProfileAuthorityBinding {
			return []ProfileAuthorityBinding{value[1], value[0]}
		}},
		{name: "cycle", mutate: func(value []ProfileAuthorityBinding) []ProfileAuthorityBinding {
			primaryProfile := value[0].Profile
			fallbackProfile := value[1].Profile
			preCyclePrimary := validModelProfile()
			preCyclePrimaryHash, hashErr := ModelProfileHash(preCyclePrimary)
			if hashErr != nil {
				t.Fatalf("hash pre-cycle primary: %v", hashErr)
			}
			fallbackProfile.Fallback = FallbackPolicy{
				Enabled: true,
				Profiles: []FallbackProfileBinding{
					{ID: primaryProfile.ID, ContentHash: preCyclePrimaryHash, Workload: primaryProfile.Workload},
				},
				OnConditions: []string{"provider-unavailable"},
			}
			return []ProfileAuthorityBinding{
				profileAuthorityBinding(t, primaryProfile, "primary-approval"),
				profileAuthorityBinding(t, fallbackProfile, "fallback-approval"),
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fresh := []ProfileAuthorityBinding{
				profileAuthorityBinding(t, primary, "primary-approval"),
				profileAuthorityBinding(t, fallback, "fallback-approval"),
			}
			if err := ValidateModelProfileGraph(test.mutate(fresh)); err == nil {
				t.Fatal("invalid profile fallback graph was accepted")
			}
		})
	}
}

func validModelProfile() ModelProfile {
	return ModelProfile{
		SchemaVersion: ModelProfileSchemaVersion,
		ID:            "11111111-1111-4111-8111-111111111111",
		Workload:      "constructor-implementation",
		Provider: ProviderBinding{
			ID:                    "openai-primary",
			Protocol:              ProviderProtocolOpenAIResponsesV1,
			RouteID:               "openai-primary-responses",
			RouteAuthorityHash:    testDigest("provider-route-authority"),
			RequestedModel:        "gpt-5-codex-2026-07-18",
			AllowedResolvedModels: []string{"gpt-5-codex-2026-07-18"},
		},
		Capabilities: ModelCapabilities{
			ToolCalls: true, StructuredOutputs: true, Streaming: true, Reasoning: true, ParallelToolCalls: true,
		},
		Limits: ModelLimits{
			ContextWindowTokens: 262_144, MaxInputTokens: 200_000, MaxOutputTokens: 32_000,
			MaxToolCalls: 128, TimeoutMilliseconds: 900_000, MaxAttempts: 3, MaxCostMicrounits: 500_000_000,
		},
		Runner: RunnerBinding{Kind: RunnerKindCodexCLI, ImmutableDigest: testDigest("runner")},
		Execution: ExecutionBindings{
			PolicyHash: testDigest("policy"), ParametersHash: testDigest("parameters"), PromptHash: testDigest("prompt"),
			SchemaHash: testDigest("schema"), ToolchainHash: testDigest("toolchain"),
		},
		Fallback: FallbackPolicy{Enabled: false, Profiles: []FallbackProfileBinding{}, OnConditions: []string{}},
		DisableConditions: []string{
			"conformance-regression", "provider-model-drift", "provider-route-drift", "runner-drift", "security-policy-violation",
		},
	}
}

func profileAuthorityBinding(t *testing.T, profile ModelProfile, approvalSeed string) ProfileAuthorityBinding {
	t.Helper()
	contentHash, err := ModelProfileHash(profile)
	if err != nil {
		t.Fatalf("hash graph profile: %v", err)
	}
	return ProfileAuthorityBinding{
		Profile: profile, ContentHash: contentHash, ApprovalReceiptDigest: testDigest(approvalSeed),
	}
}

func mustCanonicalProfile(t *testing.T, profile ModelProfile) []byte {
	t.Helper()
	encoded, err := CanonicalModelProfileJSON(profile)
	if err != nil {
		t.Fatalf("canonicalize profile: %v", err)
	}
	return encoded
}

func testDigest(seed string) string {
	return sha256Digest([]byte(seed))
}

func appendBeforeClosing(encoded []byte, insertion string) []byte {
	result := append([]byte(nil), encoded[:len(encoded)-1]...)
	result = append(result, insertion...)
	return append(result, '}')
}
