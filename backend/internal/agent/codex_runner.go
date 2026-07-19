package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	RunnerRequestSchema   = "worksflow-agent-runner-request/v3"
	RunnerExecutionSchema = "worksflow-agent-runner-execution/v3"
)

type ModelCapability struct {
	ID        string
	Token     string
	BaseURL   string
	ExpiresAt time.Time
}

type ModelCapabilityIssuer interface {
	Issue(context.Context, AgentAttempt, TaskCapsule) (ModelCapability, error)
	Revoke(context.Context, string) error
}

type ContainerCommandExecutor interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type DockerCodexRunnerConfig struct {
	RuntimeBinary string
	DaemonHost    string
	RunnerImage   string
	Network       string
	Memory        string
	CPUs          string
	PIDs          int
	OutputLimit   int64
	User          string
}

type DockerCodexRunner struct {
	config       DockerCodexRunnerConfig
	capabilities ModelCapabilityIssuer
	executor     ContainerCommandExecutor
}

var runnerNetworkPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func NewDockerCodexRunner(
	config DockerCodexRunnerConfig,
	capabilities ModelCapabilityIssuer,
	executor ContainerCommandExecutor,
) (*DockerCodexRunner, error) {
	config.RuntimeBinary = strings.TrimSpace(config.RuntimeBinary)
	config.DaemonHost = strings.TrimSpace(config.DaemonHost)
	config.RunnerImage = strings.TrimSpace(config.RunnerImage)
	config.Network = strings.TrimSpace(config.Network)
	config.Memory = strings.TrimSpace(config.Memory)
	config.CPUs = strings.TrimSpace(config.CPUs)
	config.User = strings.TrimSpace(config.User)
	runtimeBase := strings.ToLower(filepath.Base(config.RuntimeBinary))
	validDaemonHost := config.DaemonHost == "" || strings.HasPrefix(config.DaemonHost, "unix:///") ||
		strings.HasPrefix(config.DaemonHost, "tcp://")
	if capabilities == nil || executor == nil || (runtimeBase != "docker" && runtimeBase != "podman") || !validDaemonHost ||
		!digestPinnedImage(config.RunnerImage) || !runnerNetworkPattern.MatchString(config.Network) ||
		config.Network == "bridge" || config.Network == "host" || config.Network == "none" ||
		config.Memory == "" || config.CPUs == "" || config.PIDs < 16 || config.PIDs > 4096 ||
		config.OutputLimit < 1024 || config.OutputLimit > 64<<20 || config.User != "10001:10001" {
		return nil, fmt.Errorf("%w: Docker Codex Runner configuration is invalid", ErrExecutionBlocked)
	}
	return &DockerCodexRunner{config: config, capabilities: capabilities, executor: executor}, nil
}

type RunnerRequest struct {
	SchemaVersion           string `json:"schemaVersion"`
	AttemptID               string `json:"attemptId"`
	Model                   string `json:"model"`
	TaskCapsulePath         string `json:"taskCapsulePath"`
	TaskCapsuleDocumentHash string `json:"taskCapsuleDocumentHash"`
	ContextPackPath         string `json:"contextPackPath"`
	ContextPackDocumentHash string `json:"contextPackDocumentHash"`
	ContextIndexPath        string `json:"contextIndexPath"`
	ContextIndexHash        string `json:"contextIndexHash"`
	PromptPath              string `json:"promptPath"`
	PromptHash              string `json:"promptHash"`
	PromptTemplateHash      string `json:"promptTemplateHash"`
	OutputSchemaPath        string `json:"outputSchemaPath"`
	OutputSchemaHash        string `json:"outputSchemaHash"`
	WallTimeSeconds         int64  `json:"wallTimeSeconds"`
	MaxInputTokens          int64  `json:"maxInputTokens"`
	MaxOutputTokens         int64  `json:"maxOutputTokens"`
	MaxCommands             int64  `json:"maxCommands"`
	MaxLogBytes             int64  `json:"maxLogBytes"`
}

type RunnerExecutionRecord struct {
	SchemaVersion                 string    `json:"schemaVersion"`
	AttemptID                     string    `json:"attemptId"`
	TaskCapsuleDocumentHash       string    `json:"taskCapsuleDocumentHash"`
	ContextPackDocumentHash       string    `json:"contextPackDocumentHash"`
	ContextIndexHash              string    `json:"contextIndexHash"`
	PromptHash                    string    `json:"promptHash"`
	PromptTemplateHash            string    `json:"promptTemplateHash"`
	OutputSchemaHash              string    `json:"outputSchemaHash"`
	ExitCode                      int       `json:"exitCode"`
	StartedAt                     time.Time `json:"startedAt"`
	FinishedAt                    time.Time `json:"finishedAt"`
	TimedOut                      bool      `json:"timedOut"`
	ResultValidJSON               bool      `json:"resultValidJson"`
	MaxInputTokens                int64     `json:"maxInputTokens"`
	MaxOutputTokens               int64     `json:"maxOutputTokens"`
	MaxCommands                   int64     `json:"maxCommands"`
	ObservedInputTokens           int64     `json:"observedInputTokens"`
	ObservedCachedInputTokens     int64     `json:"observedCachedInputTokens"`
	ObservedOutputTokens          int64     `json:"observedOutputTokens"`
	ObservedReasoningOutputTokens int64     `json:"observedReasoningOutputTokens"`
	ObservedCommands              int64     `json:"observedCommands"`
	UsageAvailable                bool      `json:"usageAvailable"`
	BudgetExceeded                bool      `json:"budgetExceeded"`
	BudgetExceededKind            string    `json:"budgetExceededKind,omitempty"`
	Error                         string    `json:"error,omitempty"`
}

type CodexRunnerResult struct {
	StructuredResult []byte
	Events           []byte
	Stderr           []byte
	Execution        []byte
	Record           RunnerExecutionRecord
}

type CodexExecutionError struct {
	Result CodexRunnerResult
	Cause  error
}

func (err *CodexExecutionError) Error() string {
	if err == nil || err.Cause == nil {
		return "Codex Runner failed"
	}
	return "Codex Runner failed: " + err.Cause.Error()
}

func (err *CodexExecutionError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

func (runner *DockerCodexRunner) Run(
	ctx context.Context,
	attempt AgentAttempt,
	capsule TaskCapsule,
	pack ContextPack,
	lease WorktreeLease,
	prompt, outputSchema []byte,
) (CodexRunnerResult, error) {
	_, qualifiedPromptHash := QualifiedPromptTemplate()
	if runner == nil || ctx == nil || lease.AttemptID != attempt.ID || lease.Fence != attempt.FenceEpoch ||
		attempt.Lease == nil || attempt.State != AttemptRunning || attempt.TaskCapsule != capsule.ExactReference() ||
		attempt.ContextPack != pack.ExactReference() || capsule.ContextPack != pack.ExactReference() ||
		attempt.Executor.OutputSchemaHash != capsule.OutputSchemaHash ||
		attempt.Executor.PromptHash != qualifiedPromptHash ||
		runnerImageDigest(runner.config.RunnerImage) != attempt.Executor.RunnerImageDigest {
		return CodexRunnerResult{}, fmt.Errorf("%w: Runner input does not match the claimed Attempt", ErrExecutionDrift)
	}
	if len(prompt) == 0 || len(prompt) > 4<<20 || len(outputSchema) == 0 || len(outputSchema) > 4<<20 ||
		rawWorktreeHash(outputSchema) != capsule.OutputSchemaHash || !json.Valid(outputSchema) {
		return CodexRunnerResult{}, fmt.Errorf("%w: prompt or output schema", ErrExecutionDrift)
	}
	if err := runner.requireInternalNetwork(ctx); err != nil {
		return CodexRunnerResult{}, err
	}
	capability, err := runner.capabilities.Issue(ctx, attempt, capsule)
	if err != nil {
		return CodexRunnerResult{}, fmt.Errorf("%w: issue model capability: %v", ErrExecutionBlocked, err)
	}
	if err := validateModelCapability(capability, time.Now().UTC()); err != nil {
		_ = runner.capabilities.Revoke(context.WithoutCancel(ctx), capability.ID)
		return CodexRunnerResult{}, err
	}
	if !capability.ExpiresAt.After(time.Now().UTC().Add(time.Duration(capsule.Budgets.WallTimeSeconds)*time.Second + 30*time.Second)) {
		_ = runner.capabilities.Revoke(context.WithoutCancel(ctx), capability.ID)
		return CodexRunnerResult{}, fmt.Errorf("%w: model capability expires before the TaskCapsule deadline", ErrExecutionBlocked)
	}
	defer runner.capabilities.Revoke(context.WithoutCancel(ctx), capability.ID)

	request := RunnerRequest{
		SchemaVersion: RunnerRequestSchema, AttemptID: attempt.ID, Model: attempt.Executor.Model,
		TaskCapsulePath: "/input/task-capsule.json", ContextPackPath: "/input/context-pack.json",
		ContextIndexPath: "/input/context/index.json", PromptPath: "/input/prompt.txt",
		PromptHash: rawWorktreeHash(prompt), PromptTemplateHash: attempt.Executor.PromptHash,
		OutputSchemaPath: "/input/output.schema.json", OutputSchemaHash: capsule.OutputSchemaHash,
		WallTimeSeconds: capsule.Budgets.WallTimeSeconds,
		MaxInputTokens:  capsule.Budgets.MaxInputTokens,
		MaxOutputTokens: capsule.Budgets.MaxOutputTokens,
		MaxCommands:     capsule.Budgets.MaxCommands,
		MaxLogBytes:     min(capsule.Budgets.MaxLogBytes, runner.config.OutputLimit),
	}
	request, err = writeRunnerInputs(lease, request, capsule, pack, prompt, outputSchema, capability)
	if err != nil {
		return CodexRunnerResult{}, err
	}
	defer os.Remove(filepath.Join(lease.Root, "model.env"))

	containerName := runnerContainerName(attempt.ID, attempt.FenceEpoch)
	args := runner.runtimeArguments(runner.dockerArguments(containerName, lease)...)
	commandOutput, commandErr := runner.executor.Run(ctx, runner.config.RuntimeBinary, args...)
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	cleanupArgs := runner.runtimeArguments("rm", "--force", containerName)
	_, cleanupErr := runner.executor.Run(cleanupCtx, runner.config.RuntimeBinary, cleanupArgs...)
	cancelCleanup()
	if cleanupErr != nil && !containerMissing(cleanupErr) {
		commandErr = errors.Join(commandErr, fmt.Errorf("remove Agent Runner container: %w", cleanupErr))
	}
	if len(commandOutput) > 0 && int64(len(commandOutput)) > runner.config.OutputLimit {
		commandErr = errors.Join(commandErr, errors.New("container runtime output exceeded its bound"))
	}

	result, readErr := readCodexRunnerResult(lease.Output, runner.config.OutputLimit)
	if readErr != nil {
		return result, &CodexExecutionError{Result: result, Cause: errors.Join(commandErr, readErr)}
	}
	if result.Record.AttemptID != attempt.ID || result.Record.OutputSchemaHash != capsule.OutputSchemaHash ||
		result.Record.TaskCapsuleDocumentHash != request.TaskCapsuleDocumentHash ||
		result.Record.ContextPackDocumentHash != request.ContextPackDocumentHash ||
		result.Record.ContextIndexHash != request.ContextIndexHash ||
		result.Record.PromptHash != request.PromptHash ||
		result.Record.PromptTemplateHash != request.PromptTemplateHash ||
		result.Record.MaxInputTokens != capsule.Budgets.MaxInputTokens ||
		result.Record.MaxOutputTokens != capsule.Budgets.MaxOutputTokens ||
		result.Record.MaxCommands != capsule.Budgets.MaxCommands {
		return result, &CodexExecutionError{Result: result, Cause: fmt.Errorf("%w: Runner execution identity", ErrExecutionDrift)}
	}
	if err := validateRunnerBudgetEvidence(result.Events, result.Record); err != nil {
		return result, &CodexExecutionError{
			Result: result,
			Cause:  fmt.Errorf("%w: Runner budget evidence: %v", ErrExecutionDrift, err),
		}
	}
	if result.Record.Error == "" {
		if err := validateRunnerStructuredResult(outputSchema, result.StructuredResult); err != nil {
			return result, &CodexExecutionError{Result: result, Cause: fmt.Errorf("%w: structured result: %v", ErrExecutionDrift, err)}
		}
	}
	if commandErr != nil || result.Record.ExitCode != 0 || result.Record.Error != "" || !result.Record.ResultValidJSON {
		resultErr := error(nil)
		if result.Record.Error != "" {
			resultErr = errors.New(result.Record.Error)
		}
		if resultErr == nil && commandErr == nil {
			resultErr = errors.New("Runner exited without a valid successful execution record")
		}
		return result, &CodexExecutionError{Result: result, Cause: errors.Join(commandErr, resultErr)}
	}
	return result, nil
}

func validateRunnerStructuredResult(schemaBytes, resultBytes []byte) error {
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external JSON Schema references are disabled")
	}
	if err := compiler.AddResource("memory://agent-output.json", bytes.NewReader(schemaBytes)); err != nil {
		return err
	}
	schema, err := compiler.Compile("memory://agent-output.json")
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(resultBytes))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("structured result contains trailing JSON")
	}
	return schema.Validate(value)
}

type runnerBudgetObservation struct {
	InputTokens           int64
	CachedInputTokens     int64
	OutputTokens          int64
	ReasoningOutputTokens int64
	Commands              int64
	UsageAvailable        bool
}

type runnerJSONEvent struct {
	Type string `json:"type"`
	Item *struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"item,omitempty"`
	Usage *struct {
		InputTokens           *int64 `json:"input_tokens"`
		CachedInputTokens     *int64 `json:"cached_input_tokens"`
		OutputTokens          *int64 `json:"output_tokens"`
		ReasoningOutputTokens *int64 `json:"reasoning_output_tokens"`
	} `json:"usage,omitempty"`
}

func validateRunnerBudgetEvidence(events []byte, record RunnerExecutionRecord) error {
	observed := runnerBudgetObservation{}
	commandIDs := make(map[string]struct{})
	itemTypes := make(map[string]string)
	for _, rawLine := range bytes.Split(events, []byte{'\n'}) {
		line := bytes.TrimSpace(rawLine)
		if len(line) == 0 {
			continue
		}
		var event runnerJSONEvent
		decoder := json.NewDecoder(bytes.NewReader(line))
		if err := decoder.Decode(&event); err != nil || event.Type == "" {
			return errors.New("invalid Codex JSONL event")
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return errors.New("Codex JSONL event contains trailing data")
		}
		if strings.HasPrefix(event.Type, "item.") && event.Item != nil {
			if event.Item.ID == "" || event.Item.Type == "" {
				return errors.New("Codex item event has no stable identity")
			}
			if previous, exists := itemTypes[event.Item.ID]; exists && previous != event.Item.Type {
				return errors.New("Codex item identity changed type")
			}
			itemTypes[event.Item.ID] = event.Item.Type
			if event.Item.Type == "command_execution" {
				if _, exists := commandIDs[event.Item.ID]; !exists {
					commandIDs[event.Item.ID] = struct{}{}
					observed.Commands++
				}
			}
		}
		if event.Type == "turn.completed" {
			if event.Usage == nil || event.Usage.InputTokens == nil || event.Usage.CachedInputTokens == nil ||
				event.Usage.OutputTokens == nil || event.Usage.ReasoningOutputTokens == nil ||
				*event.Usage.InputTokens < 0 || *event.Usage.CachedInputTokens < 0 ||
				*event.Usage.OutputTokens < 0 || *event.Usage.ReasoningOutputTokens < 0 ||
				!checkedAddInt64(&observed.InputTokens, *event.Usage.InputTokens) ||
				!checkedAddInt64(&observed.CachedInputTokens, *event.Usage.CachedInputTokens) ||
				!checkedAddInt64(&observed.OutputTokens, *event.Usage.OutputTokens) ||
				!checkedAddInt64(&observed.ReasoningOutputTokens, *event.Usage.ReasoningOutputTokens) {
				return errors.New("Codex turn usage is invalid")
			}
			observed.UsageAvailable = true
		}
	}
	if record.ObservedInputTokens != observed.InputTokens ||
		record.ObservedCachedInputTokens != observed.CachedInputTokens ||
		record.ObservedOutputTokens != observed.OutputTokens ||
		record.ObservedReasoningOutputTokens != observed.ReasoningOutputTokens ||
		record.ObservedCommands != observed.Commands || record.UsageAvailable != observed.UsageAvailable {
		return errors.New("execution record does not match independently parsed Codex events")
	}
	exceeded, kind := false, ""
	if observed.Commands > record.MaxCommands {
		exceeded, kind = true, "maxCommands"
	} else if observed.UsageAvailable && observed.InputTokens > record.MaxInputTokens {
		exceeded, kind = true, "maxInputTokens"
	} else if observed.UsageAvailable && observed.OutputTokens > record.MaxOutputTokens {
		exceeded, kind = true, "maxOutputTokens"
	}
	if record.BudgetExceeded != exceeded || record.BudgetExceededKind != kind {
		return errors.New("execution record budget decision does not match Codex events")
	}
	if record.Error == "" && (!observed.UsageAvailable || exceeded) {
		return errors.New("successful execution lacks usage or exceeded its budget")
	}
	return nil
}

func checkedAddInt64(total *int64, value int64) bool {
	if value < 0 || value > int64(^uint64(0)>>1)-*total {
		return false
	}
	*total += value
	return true
}

func (runner *DockerCodexRunner) requireInternalNetwork(ctx context.Context) error {
	args := runner.runtimeArguments(
		"network", "inspect", "--format", "{{.Internal}}", runner.config.Network,
	)
	output, err := runner.executor.Run(
		ctx, runner.config.RuntimeBinary, args...,
	)
	if err != nil || strings.TrimSpace(string(output)) != "true" {
		return fmt.Errorf("%w: Runner network is not an existing Docker internal network", ErrExecutionBlocked)
	}
	return nil
}

func (runner *DockerCodexRunner) runtimeArguments(arguments ...string) []string {
	result := make([]string, 0, len(arguments)+2)
	if runner.config.DaemonHost != "" {
		flag := "--host"
		if strings.EqualFold(filepath.Base(runner.config.RuntimeBinary), "podman") {
			flag = "--url"
		}
		result = append(result, flag, runner.config.DaemonHost)
	}
	return append(result, arguments...)
}

func (runner *DockerCodexRunner) dockerArguments(containerName string, lease WorktreeLease) []string {
	labels := []string{
		"worksflow.kind=agent-runner",
		"worksflow.attempt=" + lease.AttemptID,
		"worksflow.fence=" + strconv.FormatUint(lease.Fence, 10),
	}
	args := []string{"run", "--rm", "--name", containerName, "--pull", "never", "--network", runner.config.Network,
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", strconv.Itoa(runner.config.PIDs), "--memory", runner.config.Memory,
		"--cpus", runner.config.CPUs, "--user", runner.config.User,
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=268435456,uid=10001,gid=10001,mode=0700",
		"--mount", "type=bind,src=" + lease.Input + ",dst=/input,readonly",
		"--mount", "type=bind,src=" + lease.Workspace + ",dst=/workspace",
		"--mount", "type=bind,src=" + lease.Output + ",dst=/output",
		"--env-file", filepath.Join(lease.Root, "model.env"),
	}
	for _, label := range labels {
		args = append(args, "--label", label)
	}
	return append(args, runner.config.RunnerImage)
}

func writeRunnerInputs(
	lease WorktreeLease,
	request RunnerRequest,
	capsule TaskCapsule,
	pack ContextPack,
	prompt, schema []byte,
	capability ModelCapability,
) (RunnerRequest, error) {
	capsuleJSON, err := domain.CanonicalJSON(capsule)
	if err != nil {
		return RunnerRequest{}, err
	}
	packJSON, err := domain.CanonicalJSON(pack)
	if err != nil {
		return RunnerRequest{}, err
	}
	contextJSON, err := readRegularBounded(filepath.Join(lease.Input, "context", "index.json"), 4<<20)
	if err != nil {
		return RunnerRequest{}, fmt.Errorf("%w: read materialized context index: %v", ErrExecutionDrift, err)
	}
	var materialized MaterializedContext
	if err := decodeStrictJSON(contextJSON, &materialized); err != nil {
		return RunnerRequest{}, err
	}
	materialized, err = ParseMaterializedContext(materialized)
	if err != nil || materialized.TaskCapsule != capsule.ExactReference() ||
		materialized.ContextPack != pack.ExactReference() {
		return RunnerRequest{}, fmt.Errorf("%w: materialized context index identity", ErrExecutionDrift)
	}
	canonicalContext, err := domain.CanonicalJSON(materialized)
	if err != nil || !bytes.Equal(canonicalContext, contextJSON) {
		return RunnerRequest{}, fmt.Errorf("%w: materialized context index is not canonical", ErrExecutionDrift)
	}
	request.TaskCapsuleDocumentHash = rawWorktreeHash(capsuleJSON)
	request.ContextPackDocumentHash = rawWorktreeHash(packJSON)
	request.ContextIndexHash = rawWorktreeHash(contextJSON)
	requestJSON, err := domain.CanonicalJSON(request)
	if err != nil {
		return RunnerRequest{}, err
	}
	for path, value := range map[string][]byte{
		filepath.Join(lease.Input, "runner-request.json"): requestJSON,
		filepath.Join(lease.Input, "task-capsule.json"):   capsuleJSON,
		filepath.Join(lease.Input, "context-pack.json"):   packJSON,
		filepath.Join(lease.Input, "prompt.txt"):          prompt,
		filepath.Join(lease.Input, "output.schema.json"):  schema,
	} {
		if err := writeExclusiveFile(path, value, 0o400); err != nil {
			return RunnerRequest{}, fmt.Errorf("%w: write Runner input: %v", ErrExecutionBlocked, err)
		}
	}
	environment := "OPENAI_API_KEY=" + capability.Token + "\nOPENAI_BASE_URL=" + capability.BaseURL + "\n"
	if err := writeExclusiveFile(filepath.Join(lease.Root, "model.env"), []byte(environment), 0o600); err != nil {
		return RunnerRequest{}, fmt.Errorf("%w: write model capability environment: %v", ErrExecutionBlocked, err)
	}
	return request, nil
}

func readCodexRunnerResult(output string, maximum int64) (CodexRunnerResult, error) {
	result := CodexRunnerResult{}
	var err error
	result.Execution, err = readRegularBounded(filepath.Join(output, "execution.json"), 1<<20)
	if err != nil {
		return result, fmt.Errorf("read Runner execution record: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(result.Execution))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result.Record); err != nil {
		return result, fmt.Errorf("decode Runner execution record: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return result, errors.New("Runner execution record contains trailing JSON")
	}
	if result.Record.SchemaVersion != RunnerExecutionSchema || !validUUIDs(result.Record.AttemptID) ||
		!sha256Pattern.MatchString(result.Record.TaskCapsuleDocumentHash) ||
		!sha256Pattern.MatchString(result.Record.ContextPackDocumentHash) ||
		!sha256Pattern.MatchString(result.Record.ContextIndexHash) ||
		!sha256Pattern.MatchString(result.Record.PromptHash) ||
		!sha256Pattern.MatchString(result.Record.PromptTemplateHash) ||
		!sha256Pattern.MatchString(result.Record.OutputSchemaHash) || result.Record.StartedAt.IsZero() ||
		result.Record.FinishedAt.Before(result.Record.StartedAt) || len(result.Record.Error) > 2000 ||
		result.Record.MaxInputTokens < 1 || result.Record.MaxInputTokens > 4_000_000 ||
		result.Record.MaxOutputTokens < 1 || result.Record.MaxOutputTokens > 1_000_000 ||
		result.Record.MaxCommands < 1 || result.Record.MaxCommands > 10_000 ||
		result.Record.ObservedInputTokens < 0 || result.Record.ObservedCachedInputTokens < 0 ||
		result.Record.ObservedOutputTokens < 0 || result.Record.ObservedReasoningOutputTokens < 0 ||
		result.Record.ObservedCommands < 0 ||
		(result.Record.BudgetExceededKind != "" && result.Record.BudgetExceededKind != "maxInputTokens" &&
			result.Record.BudgetExceededKind != "maxOutputTokens" && result.Record.BudgetExceededKind != "maxCommands") ||
		result.Record.BudgetExceeded != (result.Record.BudgetExceededKind != "") ||
		(result.Record.Error == "" && !result.Record.ResultValidJSON) ||
		(result.Record.BudgetExceeded && result.Record.Error == "") {
		return result, errors.New("Runner execution record is invalid")
	}
	result.Events, err = readRegularBounded(filepath.Join(output, "events.jsonl"), maximum)
	if err != nil {
		return result, fmt.Errorf("read Runner events: %w", err)
	}
	result.Stderr, err = readRegularBounded(filepath.Join(output, "stderr.log"), maximum)
	if err != nil {
		return result, fmt.Errorf("read Runner stderr: %w", err)
	}
	result.StructuredResult, err = readRegularBounded(filepath.Join(output, "result.json"), maximum)
	if err != nil {
		if result.Record.Error == "" || result.Record.ResultValidJSON {
			return result, fmt.Errorf("read structured Runner result: %w", err)
		}
		result.StructuredResult = nil
	} else if result.Record.ResultValidJSON != json.Valid(result.StructuredResult) {
		return result, errors.New("structured Runner result validity does not match the execution record")
	}
	result.Events = bindRunnerExecutionEvidence(result.Execution, result.Events)
	return result, nil
}

func bindRunnerExecutionEvidence(execution, events []byte) []byte {
	result := make([]byte, 0, len(execution)+len(events)+64)
	result = append(result, `{"type":"worksflow.platform.runner_execution","record":`...)
	result = append(result, execution...)
	result = append(result, '}', '\n')
	return append(result, events...)
}

func validateModelCapability(capability ModelCapability, now time.Time) error {
	endpoint, err := url.Parse(strings.TrimSpace(capability.BaseURL))
	host := ""
	if err == nil {
		host = endpoint.Hostname()
	}
	ip := net.ParseIP(host)
	if !validUUIDs(capability.ID) || strings.TrimSpace(capability.Token) == "" || len(capability.Token) > 2048 ||
		strings.ContainsAny(capability.Token, "\r\n\x00") || endpoint == nil || endpoint.User != nil ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" || host == "" ||
		strings.EqualFold(host, "localhost") || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) ||
		capability.ExpiresAt.IsZero() || !capability.ExpiresAt.After(now.Add(30*time.Second)) ||
		capability.ExpiresAt.After(now.Add(8*time.Hour+3*time.Minute)) {
		return fmt.Errorf("%w: model capability is invalid or unbounded", ErrExecutionBlocked)
	}
	return nil
}

func writeExclusiveFile(path string, value []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(value); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func readRegularBounded(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return nil, errors.New("output is missing, non-regular, or outside its bound")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(value)) > maximum || int64(len(value)) != info.Size() {
		return nil, errors.New("output changed while being read or exceeds its bound")
	}
	return value, nil
}

func digestPinnedImage(image string) bool {
	digest := runnerImageDigest(image)
	return sha256Pattern.MatchString(digest) && strings.HasSuffix(image, "@"+digest) &&
		!strings.ContainsAny(image, " \t\r\n\x00")
}

func runnerImageDigest(image string) string {
	index := strings.LastIndex(image, "@sha256:")
	if index < 0 {
		return ""
	}
	return image[index+1:]
}

func runnerContainerName(attemptID string, fence uint64) string {
	return "worksflow-agent-" + strings.ReplaceAll(attemptID, "-", "") + "-" + strconv.FormatUint(fence, 10)
}

func containerMissing(err error) bool {
	if err == nil {
		return false
	}
	value := strings.ToLower(err.Error())
	return strings.Contains(value, "no such container") || strings.Contains(value, "no such object") ||
		strings.Contains(value, "not found")
}

type OSContainerCommandExecutor struct {
	OutputLimit int64
}

func (executor OSContainerCommandExecutor) Run(
	ctx context.Context,
	binary string,
	args ...string,
) ([]byte, error) {
	limit := executor.OutputLimit
	if limit <= 0 || limit > 64<<20 {
		limit = 1 << 20
	}
	command := exec.CommandContext(ctx, binary, args...)
	var output bytes.Buffer
	command.Stdout = &limitedBuffer{buffer: &output, remaining: limit}
	command.Stderr = &limitedBuffer{buffer: &output, remaining: limit}
	err := command.Run()
	if int64(output.Len()) > limit {
		return nil, errors.New("container runtime output exceeded its limit")
	}
	if err != nil {
		return output.Bytes(), fmt.Errorf("container runtime command failed: %w: %s", err, boundedRuntimeOutput(output.String()))
	}
	return output.Bytes(), nil
}

type limitedBuffer struct {
	buffer    *bytes.Buffer
	remaining int64
}

func (writer *limitedBuffer) Write(value []byte) (int, error) {
	if int64(len(value)) > writer.remaining {
		return 0, errors.New("output limit exceeded")
	}
	written, err := writer.buffer.Write(value)
	writer.remaining -= int64(written)
	return written, err
}

func boundedRuntimeOutput(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 4000 {
		value = value[:4000]
	}
	return value
}
