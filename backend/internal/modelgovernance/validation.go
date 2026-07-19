package modelgovernance

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	digestPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	stableIDPattern    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	mediaTypePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)
	commitPattern      = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	modelDatePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,116}-[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	modelDigestPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,61}@[0-9a-f]{64}$`)
)

var allowedFallbackConditions = map[string]struct{}{
	"provider-rate-limited": {},
	"provider-timeout":      {},
	"provider-unavailable":  {},
}

var allowedDisableConditions = map[string]struct{}{
	"conformance-regression":    {},
	"cost-budget-exceeded":      {},
	"latency-budget-exceeded":   {},
	"provider-model-drift":      {},
	"provider-route-drift":      {},
	"runner-drift":              {},
	"security-policy-violation": {},
}

var mandatoryDisableConditions = []string{
	"conformance-regression",
	"provider-model-drift",
	"provider-route-drift",
	"runner-drift",
	"security-policy-violation",
}

func ValidateModelProfile(profile ModelProfile) error {
	if profile.SchemaVersion != ModelProfileSchemaVersion {
		return fmt.Errorf("schemaVersion must equal %q", ModelProfileSchemaVersion)
	}
	if !validUUIDv4(profile.ID) {
		return errors.New("id must be a canonical UUIDv4")
	}
	if !validStableID(profile.Workload) {
		return errors.New("workload must be a canonical stable identifier")
	}
	if err := validateProvider(profile.Provider); err != nil {
		return err
	}
	if err := validateCapabilitiesAndLimits(profile.Capabilities, profile.Limits); err != nil {
		return err
	}
	if profile.Runner.Kind != RunnerKindCodexCLI {
		return fmt.Errorf("runner.kind must equal %q", RunnerKindCodexCLI)
	}
	if !validDigest(profile.Runner.ImmutableDigest) {
		return errors.New("runner.immutableDigest must be a canonical sha256 digest")
	}
	for _, binding := range []struct{ name, value string }{
		{name: "execution.policyHash", value: profile.Execution.PolicyHash},
		{name: "execution.parametersHash", value: profile.Execution.ParametersHash},
		{name: "execution.promptHash", value: profile.Execution.PromptHash},
		{name: "execution.schemaHash", value: profile.Execution.SchemaHash},
		{name: "execution.toolchainHash", value: profile.Execution.ToolchainHash},
	} {
		if !validDigest(binding.value) {
			return fmt.Errorf("%s must be a canonical sha256 digest", binding.name)
		}
	}
	if err := validateFallback(profile.ID, profile.Workload, profile.Fallback); err != nil {
		return err
	}
	if profile.DisableConditions == nil {
		return errors.New("disableConditions must be a non-null array")
	}
	if err := requireSortedUniqueEnum(profile.DisableConditions, allowedDisableConditions, "disableConditions", 16); err != nil {
		return err
	}
	disabledBy := make(map[string]struct{}, len(profile.DisableConditions))
	for _, condition := range profile.DisableConditions {
		disabledBy[condition] = struct{}{}
	}
	for _, condition := range mandatoryDisableConditions {
		if _, exists := disabledBy[condition]; !exists {
			return fmt.Errorf("disableConditions must include %q", condition)
		}
	}
	return nil
}

func validateProvider(provider ProviderBinding) error {
	if !validStableID(provider.ID) {
		return errors.New("provider.id must be a canonical stable identifier")
	}
	if provider.Protocol != ProviderProtocolOpenAIResponsesV1 {
		return fmt.Errorf("provider.protocol must equal %q", ProviderProtocolOpenAIResponsesV1)
	}
	if !validStableID(provider.RouteID) {
		return errors.New("provider.routeId must be a non-network-executable canonical registry identifier")
	}
	if !validDigest(provider.RouteAuthorityHash) {
		return errors.New("provider.routeAuthorityHash must bind an exact separately verified route authority")
	}
	if !validPinnedModel(provider.RequestedModel) {
		return errors.New("provider.requestedModel must be an exact date- or digest-pinned lowercase model id")
	}
	if provider.AllowedResolvedModels == nil || len(provider.AllowedResolvedModels) == 0 || len(provider.AllowedResolvedModels) > 32 {
		return errors.New("provider.allowedResolvedModels must contain 1 through 32 exact model ids")
	}
	if err := requireStrictlySorted(provider.AllowedResolvedModels, "provider.allowedResolvedModels"); err != nil {
		return err
	}
	requestedAllowed := false
	for index, model := range provider.AllowedResolvedModels {
		if !validPinnedModel(model) {
			return fmt.Errorf("provider.allowedResolvedModels[%d] must be an exact date- or digest-pinned lowercase model id", index)
		}
		requestedAllowed = requestedAllowed || model == provider.RequestedModel
	}
	if !requestedAllowed {
		return errors.New("provider.requestedModel must be included in allowedResolvedModels")
	}
	return nil
}

func validateCapabilitiesAndLimits(capabilities ModelCapabilities, limits ModelLimits) error {
	if capabilities.ParallelToolCalls && !capabilities.ToolCalls {
		return errors.New("capabilities.parallelToolCalls requires capabilities.toolCalls")
	}
	if limits.ContextWindowTokens < 1 || limits.ContextWindowTokens > 10_000_000 ||
		limits.MaxInputTokens < 1 || limits.MaxOutputTokens < 1 ||
		limits.MaxInputTokens > limits.ContextWindowTokens || limits.MaxOutputTokens > limits.ContextWindowTokens ||
		limits.MaxInputTokens+limits.MaxOutputTokens > limits.ContextWindowTokens {
		return errors.New("limits token values must be positive, bounded, and fit within contextWindowTokens")
	}
	if capabilities.ToolCalls {
		if limits.MaxToolCalls < 1 || limits.MaxToolCalls > 1024 {
			return errors.New("limits.maxToolCalls must be between 1 and 1024 when tool calls are enabled")
		}
	} else if limits.MaxToolCalls != 0 {
		return errors.New("limits.maxToolCalls must be zero when tool calls are disabled")
	}
	if limits.TimeoutMilliseconds < 1_000 || limits.TimeoutMilliseconds > 3_600_000 {
		return errors.New("limits.timeoutMilliseconds must be between 1000 and 3600000")
	}
	if limits.MaxAttempts < 1 || limits.MaxAttempts > 10 {
		return errors.New("limits.maxAttempts must be between 1 and 10")
	}
	if limits.MaxCostMicrounits < 1 || limits.MaxCostMicrounits > 1_000_000_000_000 {
		return errors.New("limits.maxCostMicrounits must be positive and bounded")
	}
	return nil
}

func validateFallback(profileID, workload string, fallback FallbackPolicy) error {
	if fallback.Profiles == nil || fallback.OnConditions == nil {
		return errors.New("fallback.profiles and fallback.onConditions must be non-null arrays")
	}
	if !fallback.Enabled {
		if len(fallback.Profiles) != 0 || len(fallback.OnConditions) != 0 {
			return errors.New("disabled fallback must have empty profiles and onConditions")
		}
		return nil
	}
	if len(fallback.Profiles) == 0 || len(fallback.Profiles) > 16 {
		return errors.New("enabled fallback must bind 1 through 16 exact profiles")
	}
	profileIDs := make([]string, len(fallback.Profiles))
	for index, boundProfile := range fallback.Profiles {
		profileIDs[index] = boundProfile.ID
		if !validUUIDv4(boundProfile.ID) || boundProfile.ID == profileID || !validDigest(boundProfile.ContentHash) || boundProfile.Workload != workload {
			return fmt.Errorf("fallback.profiles[%d] must bind a different canonical UUIDv4, exact content hash, and the same workload", index)
		}
	}
	if err := requireStrictlySorted(profileIDs, "fallback.profiles by id"); err != nil {
		return err
	}
	if err := requireSortedUniqueEnum(fallback.OnConditions, allowedFallbackConditions, "fallback.onConditions", 8); err != nil {
		return err
	}
	if len(fallback.OnConditions) == 0 {
		return errors.New("enabled fallback must name at least one closed trigger condition")
	}
	return nil
}

// ValidateModelProfileGraph resolves every fallback against a complete set of
// exact profiles, requires a caller-supplied commitment to a separately
// verified approval receipt for every member, and rejects cycles. It cannot
// infer an approval decision from that digest, verify receipt signatures, or
// grant activation; those authority checks belong to GovernanceVerifier.
func ValidateModelProfileGraph(bindings []ProfileAuthorityBinding) error {
	if bindings == nil || len(bindings) == 0 || len(bindings) > 1024 {
		return errors.New("profile graph must be a non-null set of 1 through 1024 authority bindings")
	}
	profiles := make(map[string]ProfileAuthorityBinding, len(bindings))
	profileIDs := make([]string, len(bindings))
	for index, binding := range bindings {
		if err := ValidateModelProfile(binding.Profile); err != nil {
			return fmt.Errorf("profile graph member %d is invalid: %w", index, err)
		}
		contentHash, err := ModelProfileHash(binding.Profile)
		if err != nil {
			return err
		}
		if binding.ContentHash != contentHash {
			return fmt.Errorf("profile graph member %q content hash does not match its canonical ModelProfile", binding.Profile.ID)
		}
		if !validDigest(binding.ApprovalReceiptDigest) {
			return fmt.Errorf("profile graph member %q lacks an exact externally verified approval receipt digest", binding.Profile.ID)
		}
		profileIDs[index] = binding.Profile.ID
		if _, duplicate := profiles[binding.Profile.ID]; duplicate {
			return fmt.Errorf("profile graph contains duplicate profile %q", binding.Profile.ID)
		}
		profiles[binding.Profile.ID] = binding
	}
	if err := requireStrictlySorted(profileIDs, "profile graph by profile id"); err != nil {
		return err
	}
	for _, binding := range bindings {
		for _, fallback := range binding.Profile.Fallback.Profiles {
			if _, exists := profiles[fallback.ID]; !exists {
				return fmt.Errorf("profile %q fallback %q is unresolved", binding.Profile.ID, fallback.ID)
			}
		}
	}

	states := make(map[string]uint8, len(profiles))
	var visit func(string) error
	visit = func(profileID string) error {
		switch states[profileID] {
		case 1:
			return fmt.Errorf("profile fallback graph contains a cycle through %q", profileID)
		case 2:
			return nil
		}
		states[profileID] = 1
		for _, fallback := range profiles[profileID].Profile.Fallback.Profiles {
			if err := visit(fallback.ID); err != nil {
				return err
			}
		}
		states[profileID] = 2
		return nil
	}
	for _, profileID := range profileIDs {
		if err := visit(profileID); err != nil {
			return err
		}
	}
	for _, binding := range bindings {
		for _, fallback := range binding.Profile.Fallback.Profiles {
			target := profiles[fallback.ID]
			if target.ContentHash != fallback.ContentHash || target.Profile.Workload != fallback.Workload {
				return fmt.Errorf("profile %q fallback %q does not match the exact externally resolved target", binding.Profile.ID, fallback.ID)
			}
		}
	}
	return nil
}

func ValidateFrozenCorpus(corpus FrozenCorpus) error {
	if corpus.SchemaVersion != FrozenCorpusSchemaVersion {
		return fmt.Errorf("schemaVersion must equal %q", FrozenCorpusSchemaVersion)
	}
	if !validUUIDv4(corpus.ID) {
		return errors.New("id must be a canonical UUIDv4")
	}
	if !validUUIDv4(corpus.Profile.ID) || !validDigest(corpus.Profile.ContentHash) || !validStableID(corpus.Profile.Workload) {
		return errors.New("profile must bind a canonical profile UUID, content hash, and workload")
	}
	for _, binding := range []struct{ name, value string }{
		{name: "thresholdPolicyHash", value: corpus.ThresholdPolicyHash},
		{name: "harnessHash", value: corpus.HarnessHash},
		{name: "verifierHash", value: corpus.VerifierHash},
	} {
		if !validDigest(binding.value) {
			return fmt.Errorf("%s must be a canonical sha256 digest", binding.name)
		}
	}
	if corpus.Cases == nil || len(corpus.Cases) == 0 || len(corpus.Cases) > 512 {
		return errors.New("cases must be a non-null array containing 1 through 512 cases")
	}
	caseIDs := make([]string, len(corpus.Cases))
	artifactIDs := map[string]struct{}{}
	oracleCiphertextHashes := map[string]string{}
	oraclePlaintextHashes := map[string]string{}
	buildContracts := map[string]BuildContractBinding{}
	templateReleases := map[string]TemplateReleaseBinding{}
	baseTrees := map[string]BaseTreeBinding{}
	for index, corpusCase := range corpus.Cases {
		caseIDs[index] = corpusCase.ID
		if err := validateCorpusCase(corpusCase, index); err != nil {
			return err
		}
		for _, artifactID := range []string{corpusCase.Input.ArtifactID, corpusCase.HiddenOracle.ArtifactID} {
			if _, duplicate := artifactIDs[artifactID]; duplicate {
				return fmt.Errorf("cases contain duplicate artifact id %q", artifactID)
			}
			artifactIDs[artifactID] = struct{}{}
		}
		oracleCiphertextHashes[corpusCase.HiddenOracle.CiphertextHash] = corpusCase.ID
		oraclePlaintextHashes[corpusCase.HiddenOracle.PlaintextCommitmentHash] = corpusCase.ID
		if existing, exists := buildContracts[corpusCase.BuildContract.ID]; exists && existing != corpusCase.BuildContract {
			return fmt.Errorf("BuildContract %q has conflicting bindings", corpusCase.BuildContract.ID)
		}
		buildContracts[corpusCase.BuildContract.ID] = corpusCase.BuildContract
		if existing, exists := templateReleases[corpusCase.TemplateRelease.ID]; exists && existing != corpusCase.TemplateRelease {
			return fmt.Errorf("TemplateRelease %q has conflicting bindings", corpusCase.TemplateRelease.ID)
		}
		templateReleases[corpusCase.TemplateRelease.ID] = corpusCase.TemplateRelease
		if existing, exists := baseTrees[corpusCase.BaseTree.Commit]; exists && existing != corpusCase.BaseTree {
			return fmt.Errorf("base commit %q has conflicting tree bindings", corpusCase.BaseTree.Commit)
		}
		baseTrees[corpusCase.BaseTree.Commit] = corpusCase.BaseTree
	}
	for _, corpusCase := range corpus.Cases {
		contentHash := corpusCase.Input.ContentHash
		if oracleCaseID, exists := oracleCiphertextHashes[contentHash]; exists {
			return fmt.Errorf("input for case %q equals hidden-oracle ciphertext for case %q", corpusCase.ID, oracleCaseID)
		}
		if oracleCaseID, exists := oraclePlaintextHashes[contentHash]; exists {
			return fmt.Errorf("input for case %q equals hidden-oracle plaintext commitment for case %q", corpusCase.ID, oracleCaseID)
		}
	}
	return requireStrictlySorted(caseIDs, "cases by id")
}

func validateCorpusCase(corpusCase CorpusCase, index int) error {
	location := fmt.Sprintf("cases[%d]", index)
	if !validStableID(corpusCase.ID) {
		return fmt.Errorf("%s.id must be a canonical stable identifier", location)
	}
	if !validStableID(corpusCase.Input.ArtifactID) || !validMediaType(corpusCase.Input.MediaType) || !validDigest(corpusCase.Input.ContentHash) {
		return fmt.Errorf("%s.input must bind a canonical artifact id, media type, and content hash", location)
	}
	if !validStableID(corpusCase.HiddenOracle.ArtifactID) || corpusCase.HiddenOracle.ArtifactID == corpusCase.Input.ArtifactID ||
		corpusCase.HiddenOracle.MediaType != "application/octet-stream" || !validDigest(corpusCase.HiddenOracle.CiphertextHash) ||
		!validDigest(corpusCase.HiddenOracle.PlaintextCommitmentHash) || !validDigest(corpusCase.HiddenOracle.KeyPolicyHash) ||
		corpusCase.HiddenOracle.CiphertextHash == corpusCase.HiddenOracle.PlaintextCommitmentHash ||
		corpusCase.HiddenOracle.CiphertextHash == corpusCase.HiddenOracle.KeyPolicyHash ||
		corpusCase.HiddenOracle.PlaintextCommitmentHash == corpusCase.HiddenOracle.KeyPolicyHash {
		return fmt.Errorf("%s.hiddenOracle must bind distinct sealed ciphertext, plaintext commitment, and key policy digests", location)
	}
	if !validUUIDv4(corpusCase.BuildContract.ID) || !validDigest(corpusCase.BuildContract.ContentHash) || !validDigest(corpusCase.BuildContract.ContractHash) {
		return fmt.Errorf("%s.buildContract must bind a canonical UUID, content hash, and contract hash", location)
	}
	if !validUUIDv4(corpusCase.TemplateRelease.ID) || !validDigest(corpusCase.TemplateRelease.ContentHash) || !validDigest(corpusCase.TemplateRelease.ApprovalReceiptDigest) {
		return fmt.Errorf("%s.templateRelease must bind a canonical UUID, content hash, and approval receipt digest", location)
	}
	if !commitPattern.MatchString(corpusCase.BaseTree.Commit) || corpusCase.BaseTree.TreeDigestSchema != SourceTreeDigestSchemaV1 ||
		!validDigest(corpusCase.BaseTree.TreeDigest) || corpusCase.BaseTree.Dirty {
		return fmt.Errorf("%s.baseTree must bind one clean canonical commit and source-content tree digest", location)
	}
	if corpusCase.Repetitions < 1 || corpusCase.Repetitions > 100 {
		return fmt.Errorf("%s.repetitions must be between 1 and 100", location)
	}
	return nil
}

// ValidateFrozenCorpusForProfile closes the corpus's profile reference against
// the actual canonical ModelProfile value; matching labels without an exact
// content-hash match are insufficient.
func ValidateFrozenCorpusForProfile(corpus FrozenCorpus, profile ModelProfile) error {
	if err := ValidateFrozenCorpus(corpus); err != nil {
		return err
	}
	if err := ValidateModelProfile(profile); err != nil {
		return fmt.Errorf("validate bound ModelProfile: %w", err)
	}
	profileHash, err := ModelProfileHash(profile)
	if err != nil {
		return err
	}
	if corpus.Profile.ID != profile.ID || corpus.Profile.Workload != profile.Workload || corpus.Profile.ContentHash != profileHash {
		return errors.New("FrozenCorpus profile id, workload, or content hash does not match the exact ModelProfile")
	}
	return nil
}

func ParseFrozenCorpusForProfile(encoded []byte, expectedHash string, profile ModelProfile) (FrozenCorpus, error) {
	corpus, err := ParseFrozenCorpus(encoded, expectedHash)
	if err != nil {
		return FrozenCorpus{}, err
	}
	if err := ValidateFrozenCorpusForProfile(corpus, profile); err != nil {
		return FrozenCorpus{}, err
	}
	return corpus, nil
}

func validUUIDv4(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.Version() == 4 && parsed.String() == value
}

func validDigest(value string) bool { return digestPattern.MatchString(value) }

func validStableID(value string) bool {
	return len(value) <= 128 && stableIDPattern.MatchString(value)
}

func validMediaType(value string) bool {
	return len(value) <= 128 && mediaTypePattern.MatchString(value)
}

func validPinnedModel(value string) bool {
	if len(value) > 128 || value != strings.ToLower(value) || strings.Contains(value, "latest") || strings.ContainsAny(value, `*?[](){}|\\^$+`) {
		return false
	}
	if modelDigestPattern.MatchString(value) {
		return true
	}
	if !modelDatePattern.MatchString(value) || len(value) < 11 {
		return false
	}
	date := value[len(value)-10:]
	parsed, err := time.Parse("2006-01-02", date)
	return err == nil && parsed.Format("2006-01-02") == date
}

func requireStrictlySorted(values []string, field string) error {
	for index := range values {
		if index > 0 && values[index-1] >= values[index] {
			return fmt.Errorf("%s must be strictly sorted and unique", field)
		}
	}
	return nil
}

func requireSortedUniqueEnum(values []string, allowed map[string]struct{}, field string, maximum int) error {
	if len(values) > maximum {
		return fmt.Errorf("%s exceeds %d values", field, maximum)
	}
	if err := requireStrictlySorted(values, field); err != nil {
		return err
	}
	for index, value := range values {
		if _, exists := allowed[value]; !exists {
			return fmt.Errorf("%s[%d] is not in the v1 closed set", field, index)
		}
	}
	return nil
}
