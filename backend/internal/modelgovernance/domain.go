package modelgovernance

const (
	ModelProfileSchemaVersion = "worksflow-model-profile/v1"
	FrozenCorpusSchemaVersion = "worksflow-frozen-model-conformance-corpus/v1"

	ProviderProtocolOpenAIResponsesV1 = "openai-responses/v1"
	RunnerKindCodexCLI                = "codex-cli"
	SourceTreeDigestSchemaV1          = "worksflow-source-content-tree/v1"
)

// ModelProfile is the exact immutable description of one provider/model,
// runner and policy combination. The content digest is kept outside the
// document so it can be pinned without introducing a recursive self-hash.
type ModelProfile struct {
	SchemaVersion     string            `json:"schemaVersion"`
	ID                string            `json:"id"`
	Workload          string            `json:"workload"`
	Provider          ProviderBinding   `json:"provider"`
	Capabilities      ModelCapabilities `json:"capabilities"`
	Limits            ModelLimits       `json:"limits"`
	Runner            RunnerBinding     `json:"runner"`
	Execution         ExecutionBindings `json:"execution"`
	Fallback          FallbackPolicy    `json:"fallback"`
	DisableConditions []string          `json:"disableConditions"`
}

type ProviderBinding struct {
	ID                    string   `json:"id"`
	Protocol              string   `json:"protocol"`
	RouteID               string   `json:"routeId"`
	RouteAuthorityHash    string   `json:"routeAuthorityHash"`
	RequestedModel        string   `json:"requestedModel"`
	AllowedResolvedModels []string `json:"allowedResolvedModels"`
}

type ModelCapabilities struct {
	ToolCalls         bool `json:"toolCalls"`
	StructuredOutputs bool `json:"structuredOutputs"`
	Streaming         bool `json:"streaming"`
	Reasoning         bool `json:"reasoning"`
	ParallelToolCalls bool `json:"parallelToolCalls"`
}

type ModelLimits struct {
	ContextWindowTokens int64 `json:"contextWindowTokens"`
	MaxInputTokens      int64 `json:"maxInputTokens"`
	MaxOutputTokens     int64 `json:"maxOutputTokens"`
	MaxToolCalls        int   `json:"maxToolCalls"`
	TimeoutMilliseconds int64 `json:"timeoutMilliseconds"`
	MaxAttempts         int   `json:"maxAttempts"`
	MaxCostMicrounits   int64 `json:"maxCostMicrounits"`
}

type RunnerBinding struct {
	Kind            string `json:"kind"`
	ImmutableDigest string `json:"immutableDigest"`
}

// ExecutionBindings commits to every non-model input which can change model
// behavior. They are content digests, never mutable names or paths.
type ExecutionBindings struct {
	PolicyHash     string `json:"policyHash"`
	ParametersHash string `json:"parametersHash"`
	PromptHash     string `json:"promptHash"`
	SchemaHash     string `json:"schemaHash"`
	ToolchainHash  string `json:"toolchainHash"`
}

// FallbackPolicy is explicit and closed. A disabled policy must have empty
// arrays. An enabled policy names digest-pinned profiles and closed trigger
// codes; it never means "pick another available/latest model".
type FallbackPolicy struct {
	Enabled      bool                     `json:"enabled"`
	Profiles     []FallbackProfileBinding `json:"profiles"`
	OnConditions []string                 `json:"onConditions"`
}

type FallbackProfileBinding struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
	Workload    string `json:"workload"`
}

// ProfileAuthorityBinding supplies the separately verified approval receipt
// commitment required to resolve fallback references. It is deliberately not
// a wire DTO: verifying the receipt signature and trust root is the caller's
// responsibility before invoking ValidateModelProfileGraph.
type ProfileAuthorityBinding struct {
	Profile               ModelProfile
	ContentHash           string
	ApprovalReceiptDigest string
}

// FrozenCorpus is an immutable list of exact conformance cases. A CorpusProfile
// binding can be checked against actual ModelProfile bytes with
// ValidateFrozenCorpusForProfile.
type FrozenCorpus struct {
	SchemaVersion       string               `json:"schemaVersion"`
	ID                  string               `json:"id"`
	Profile             CorpusProfileBinding `json:"profile"`
	ThresholdPolicyHash string               `json:"thresholdPolicyHash"`
	HarnessHash         string               `json:"harnessHash"`
	VerifierHash        string               `json:"verifierHash"`
	Cases               []CorpusCase         `json:"cases"`
}

type CorpusProfileBinding struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
	Workload    string `json:"workload"`
}

type CorpusCase struct {
	ID              string                 `json:"id"`
	Input           InputArtifactBinding   `json:"input"`
	HiddenOracle    HiddenOracleBinding    `json:"hiddenOracle"`
	BuildContract   BuildContractBinding   `json:"buildContract"`
	TemplateRelease TemplateReleaseBinding `json:"templateRelease"`
	BaseTree        BaseTreeBinding        `json:"baseTree"`
	Repetitions     int                    `json:"repetitions"`
}

type InputArtifactBinding struct {
	ArtifactID  string `json:"artifactId"`
	MediaType   string `json:"mediaType"`
	ContentHash string `json:"contentHash"`
}

// HiddenOracleBinding exposes commitments only. Ciphertext and key-policy
// digests keep the oracle unavailable to the model-facing runner while letting
// the verifier bind the exact hidden material.
type HiddenOracleBinding struct {
	ArtifactID              string `json:"artifactId"`
	MediaType               string `json:"mediaType"`
	CiphertextHash          string `json:"ciphertextHash"`
	PlaintextCommitmentHash string `json:"plaintextCommitmentHash"`
	KeyPolicyHash           string `json:"keyPolicyHash"`
}

type BuildContractBinding struct {
	ID           string `json:"id"`
	ContentHash  string `json:"contentHash"`
	ContractHash string `json:"contractHash"`
}

type TemplateReleaseBinding struct {
	ID                    string `json:"id"`
	ContentHash           string `json:"contentHash"`
	ApprovalReceiptDigest string `json:"approvalReceiptDigest"`
}

type BaseTreeBinding struct {
	Commit           string `json:"commit"`
	TreeDigestSchema string `json:"treeDigestSchema"`
	TreeDigest       string `json:"treeDigest"`
	Dirty            bool   `json:"dirty"`
}
