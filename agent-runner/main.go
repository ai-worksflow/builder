package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"sync"
	"time"
)

const (
	requestPath       = "/input/runner-request.json"
	taskCapsulePath   = "/input/task-capsule.json"
	contextPackPath   = "/input/context-pack.json"
	contextIndexPath  = "/input/context/index.json"
	embeddedSchema    = "/opt/worksflow-agent/output.schema.json"
	workspacePath     = "/workspace"
	eventsPath        = "/output/events.jsonl"
	stderrPath        = "/output/stderr.log"
	resultPath        = "/output/result.json"
	executionPath     = "/output/execution.json"
	requestSchema     = "worksflow-agent-runner-request/v3"
	executionSchema   = "worksflow-agent-runner-execution/v3"
	taskCapsuleSchema = "agent-task-capsule/v1"
	maximumInputSize  = 4 << 20
	maximumLogSize    = 64 << 20
)

var (
	uuidPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type runnerRequest struct {
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

type executionRecord struct {
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

type capsuleBudgetEnvelope struct {
	SchemaVersion string `json:"schemaVersion"`
	Budgets       struct {
		WallTimeSeconds int64 `json:"wallTimeSeconds"`
		MaxInputTokens  int64 `json:"maxInputTokens"`
		MaxOutputTokens int64 `json:"maxOutputTokens"`
		MaxCommands     int64 `json:"maxCommands"`
		MaxLogBytes     int64 `json:"maxLogBytes"`
	} `json:"budgets"`
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	request, err := loadRequest(requestPath)
	if err != nil {
		return err
	}
	var taskCapsule []byte
	for _, input := range []struct {
		name string
		path string
		hash string
	}{
		{name: "TaskCapsule", path: request.TaskCapsulePath, hash: request.TaskCapsuleDocumentHash},
		{name: "ContextPack", path: request.ContextPackPath, hash: request.ContextPackDocumentHash},
		{name: "materialized context index", path: request.ContextIndexPath, hash: request.ContextIndexHash},
	} {
		value, readErr := readBounded(input.path, maximumInputSize)
		if readErr != nil || hashBytes(value) != input.hash || !json.Valid(value) {
			return fmt.Errorf("read exact %s: immutable JSON bytes do not match the request", input.name)
		}
		if input.path == taskCapsulePath {
			taskCapsule = value
		}
	}
	if err := validateTaskCapsuleBudgets(taskCapsule, request); err != nil {
		return err
	}
	prompt, err := readBounded(request.PromptPath, maximumInputSize)
	if err != nil {
		return fmt.Errorf("read exact prompt: %w", err)
	}
	if len(bytes.TrimSpace(prompt)) == 0 || hashBytes(prompt) != request.PromptHash {
		return errors.New("exact prompt bytes do not match the qualified request")
	}
	schema, err := readBounded(request.OutputSchemaPath, maximumInputSize)
	if err != nil {
		return fmt.Errorf("read output schema: %w", err)
	}
	embedded, err := readBounded(embeddedSchema, maximumInputSize)
	if err != nil {
		return fmt.Errorf("read image-qualified output schema: %w", err)
	}
	if hashBytes(schema) != request.OutputSchemaHash || !json.Valid(schema) || !bytes.Equal(schema, embedded) {
		return errors.New("output schema bytes do not match the qualified digest")
	}
	if err := validateModelGatewayEnvironment(); err != nil {
		return err
	}
	if err := os.MkdirAll(os.Getenv("HOME"), 0o700); err != nil {
		return fmt.Errorf("create ephemeral Codex home: %w", err)
	}

	started := time.Now().UTC()
	timeoutContext, timeoutCancel := context.WithTimeout(
		context.Background(), time.Duration(request.WallTimeSeconds)*time.Second,
	)
	defer timeoutCancel()
	commandContext, commandCancel := context.WithCancelCause(timeoutContext)
	defer commandCancel(nil)
	command := exec.CommandContext(commandContext, "codex", codexArguments(request)...)
	command.Stdin = bytes.NewReader(prompt)
	events, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create event output: %w", err)
	}
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = events.Close()
		return fmt.Errorf("create stderr output: %w", err)
	}
	eventOutput := &boundedWriter{writer: events, remaining: request.MaxLogBytes}
	eventWriter := newCodexEventWriter(eventOutput, request.MaxCommands, commandCancel)
	stderrWriter := &boundedWriter{writer: stderr, remaining: request.MaxLogBytes}
	command.Stdout, command.Stderr = eventWriter, stderrWriter
	runErr := command.Run()
	eventErr := eventWriter.Finalize()
	closeErr := errors.Join(events.Close(), stderr.Close())
	finished := time.Now().UTC()
	exitCode := 0
	if runErr != nil {
		exitCode = 1
		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			exitCode = exitError.ExitCode()
		}
	}
	result, resultErr := readBounded(resultPath, maximumInputSize)
	resultValid := resultErr == nil && json.Valid(result)
	observation := eventWriter.Observation(request.MaxInputTokens, request.MaxOutputTokens)
	record := executionRecord{
		SchemaVersion: executionSchema, AttemptID: request.AttemptID,
		TaskCapsuleDocumentHash: request.TaskCapsuleDocumentHash,
		ContextPackDocumentHash: request.ContextPackDocumentHash,
		ContextIndexHash:        request.ContextIndexHash,
		PromptHash:              request.PromptHash, PromptTemplateHash: request.PromptTemplateHash,
		OutputSchemaHash: request.OutputSchemaHash, ExitCode: exitCode,
		StartedAt: started, FinishedAt: finished,
		TimedOut: errors.Is(timeoutContext.Err(), context.DeadlineExceeded), ResultValidJSON: resultValid,
		MaxInputTokens: request.MaxInputTokens, MaxOutputTokens: request.MaxOutputTokens,
		MaxCommands: request.MaxCommands, ObservedInputTokens: observation.InputTokens,
		ObservedCachedInputTokens:     observation.CachedInputTokens,
		ObservedOutputTokens:          observation.OutputTokens,
		ObservedReasoningOutputTokens: observation.ReasoningOutputTokens,
		ObservedCommands:              observation.Commands, UsageAvailable: observation.UsageAvailable,
		BudgetExceeded: observation.BudgetExceeded, BudgetExceededKind: observation.BudgetExceededKind,
	}
	if observation.BudgetExceeded {
		record.Error = "TaskCapsule " + observation.BudgetExceededKind + " budget exceeded"
	} else if eventErr != nil {
		record.Error = boundedError(eventErr)
	} else if runErr != nil {
		record.Error = boundedError(runErr)
	} else if closeErr != nil {
		record.Error = boundedError(closeErr)
	} else if !observation.UsageAvailable {
		record.Error = "Codex did not emit auditable turn token usage"
	} else if !resultValid {
		record.Error = "Codex did not produce one valid structured result"
	}
	if err := writeExclusiveJSON(executionPath, record); err != nil {
		return err
	}
	if record.Error != "" {
		return errors.New(record.Error)
	}
	return nil
}

func codexArguments(request runnerRequest) []string {
	gatewayURL := strings.TrimRight(strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")), "/")
	return []string{
		"--ask-for-approval", "never",
		"exec",
		"--ephemeral",
		"--sandbox", "workspace-write",
		"--strict-config",
		"--ignore-user-config",
		"--ignore-rules",
		"--config", `model_provider="worksflow_gateway"`,
		"--config", `model_providers.worksflow_gateway.name="Worksflow Agent Model Gateway"`,
		"--config", "model_providers.worksflow_gateway.base_url=" + strconv.Quote(gatewayURL),
		"--config", `model_providers.worksflow_gateway.env_key="OPENAI_API_KEY"`,
		"--config", `model_providers.worksflow_gateway.wire_api="responses"`,
		"--config", "model_providers.worksflow_gateway.supports_websockets=false",
		"--config", `shell_environment_policy.include_only=["PATH","HOME"]`,
		"--config", "allow_login_shell=false",
		"--json",
		"--skip-git-repo-check",
		"--output-schema", request.OutputSchemaPath,
		"--output-last-message", resultPath,
		"--model", request.Model,
		"-C", workspacePath,
		"-",
	}
}

func loadRequest(path string) (runnerRequest, error) {
	payload, err := readBounded(path, maximumInputSize)
	if err != nil {
		return runnerRequest{}, err
	}
	return decodeRequest(payload)
}

func decodeRequest(payload []byte) (runnerRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var request runnerRequest
	if err := decoder.Decode(&request); err != nil {
		return runnerRequest{}, fmt.Errorf("decode runner request: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return runnerRequest{}, err
	}
	request.Model = strings.TrimSpace(request.Model)
	if request.SchemaVersion != requestSchema || !uuidPattern.MatchString(request.AttemptID) ||
		request.Model == "" || len(request.Model) > 160 || strings.ContainsAny(request.Model, "\r\n\x00") ||
		request.TaskCapsulePath != taskCapsulePath || request.ContextPackPath != contextPackPath ||
		request.ContextIndexPath != contextIndexPath ||
		request.PromptPath != "/input/prompt.txt" || request.OutputSchemaPath != "/input/output.schema.json" ||
		!digestPattern.MatchString(request.TaskCapsuleDocumentHash) ||
		!digestPattern.MatchString(request.ContextPackDocumentHash) ||
		!digestPattern.MatchString(request.ContextIndexHash) ||
		!digestPattern.MatchString(request.PromptHash) ||
		!digestPattern.MatchString(request.PromptTemplateHash) ||
		!digestPattern.MatchString(request.OutputSchemaHash) ||
		request.WallTimeSeconds < 1 || request.WallTimeSeconds > 8*60*60 ||
		request.MaxInputTokens < 1 || request.MaxInputTokens > 4_000_000 ||
		request.MaxOutputTokens < 1 || request.MaxOutputTokens > 1_000_000 ||
		request.MaxCommands < 1 || request.MaxCommands > 10_000 ||
		request.MaxLogBytes < 1024 || request.MaxLogBytes > maximumLogSize {
		return runnerRequest{}, errors.New("runner request is not exact, canonical, or bounded")
	}
	return request, nil
}

func validateTaskCapsuleBudgets(payload []byte, request runnerRequest) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var capsule capsuleBudgetEnvelope
	if err := decoder.Decode(&capsule); err != nil {
		return errors.New("TaskCapsule budget envelope is invalid")
	}
	if err := requireEOF(decoder); err != nil {
		return errors.New("TaskCapsule budget envelope has trailing data")
	}
	if capsule.SchemaVersion != taskCapsuleSchema ||
		capsule.Budgets.WallTimeSeconds != request.WallTimeSeconds ||
		capsule.Budgets.MaxInputTokens != request.MaxInputTokens ||
		capsule.Budgets.MaxOutputTokens != request.MaxOutputTokens ||
		capsule.Budgets.MaxCommands != request.MaxCommands ||
		capsule.Budgets.MaxLogBytes < request.MaxLogBytes {
		return errors.New("runner budgets do not match the exact TaskCapsule")
	}
	return nil
}

type budgetObservation struct {
	InputTokens           int64
	CachedInputTokens     int64
	OutputTokens          int64
	ReasoningOutputTokens int64
	Commands              int64
	UsageAvailable        bool
	BudgetExceeded        bool
	BudgetExceededKind    string
}

type codexEventWriter struct {
	mutex        sync.Mutex
	output       io.Writer
	maximum      int64
	cancel       context.CancelCauseFunc
	partial      []byte
	commandIDs   map[string]struct{}
	commandTypes map[string]string
	observation  budgetObservation
	failure      error
}

type codexJSONEvent struct {
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

func newCodexEventWriter(output io.Writer, maximumCommands int64, cancel context.CancelCauseFunc) *codexEventWriter {
	return &codexEventWriter{
		output: output, maximum: maximumCommands, cancel: cancel,
		commandIDs: make(map[string]struct{}), commandTypes: make(map[string]string),
	}
}

func (writer *codexEventWriter) Write(value []byte) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	if writer.failure != nil {
		return 0, writer.failure
	}
	written, err := writer.output.Write(value)
	if written > 0 {
		writer.partial = append(writer.partial, value[:written]...)
		writer.consumeLines(false)
	}
	if err != nil {
		writer.fail(err)
	}
	if writer.failure != nil {
		return written, writer.failure
	}
	return written, nil
}

func (writer *codexEventWriter) Finalize() error {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	writer.consumeLines(true)
	return writer.failure
}

func (writer *codexEventWriter) Observation(maximumInput, maximumOutput int64) budgetObservation {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	result := writer.observation
	if !result.BudgetExceeded && result.UsageAvailable && result.InputTokens > maximumInput {
		result.BudgetExceeded = true
		result.BudgetExceededKind = "maxInputTokens"
	}
	if !result.BudgetExceeded && result.UsageAvailable && result.OutputTokens > maximumOutput {
		result.BudgetExceeded = true
		result.BudgetExceededKind = "maxOutputTokens"
	}
	return result
}

func (writer *codexEventWriter) consumeLines(final bool) {
	for writer.failure == nil {
		index := bytes.IndexByte(writer.partial, '\n')
		if index < 0 {
			break
		}
		line := bytes.TrimSpace(writer.partial[:index])
		writer.partial = writer.partial[index+1:]
		if len(line) > 0 {
			writer.consumeEvent(line)
		}
	}
	if writer.failure == nil && len(writer.partial) > maximumInputSize {
		writer.fail(errors.New("Codex JSONL event exceeded the Runner event bound"))
	}
	if final && writer.failure == nil && len(bytes.TrimSpace(writer.partial)) > 0 {
		writer.consumeEvent(bytes.TrimSpace(writer.partial))
		writer.partial = nil
	}
}

func (writer *codexEventWriter) consumeEvent(line []byte) {
	var event codexJSONEvent
	decoder := json.NewDecoder(bytes.NewReader(line))
	if err := decoder.Decode(&event); err != nil || event.Type == "" {
		writer.fail(errors.New("Codex emitted an invalid JSONL event"))
		return
	}
	if err := requireEOF(decoder); err != nil {
		writer.fail(errors.New("Codex emitted a JSONL event with trailing data"))
		return
	}
	if strings.HasPrefix(event.Type, "item.") && event.Item != nil {
		if event.Item.ID == "" || event.Item.Type == "" {
			writer.fail(errors.New("Codex emitted an item event without a stable identity"))
			return
		}
		if previous, exists := writer.commandTypes[event.Item.ID]; exists && previous != event.Item.Type {
			writer.fail(errors.New("Codex reused an item identity for a different item type"))
			return
		}
		writer.commandTypes[event.Item.ID] = event.Item.Type
		if event.Item.Type == "command_execution" {
			if _, exists := writer.commandIDs[event.Item.ID]; !exists {
				writer.commandIDs[event.Item.ID] = struct{}{}
				writer.observation.Commands++
				if writer.observation.Commands > writer.maximum {
					writer.observation.BudgetExceeded = true
					writer.observation.BudgetExceededKind = "maxCommands"
					writer.fail(errors.New("TaskCapsule maxCommands budget exceeded"))
				}
			}
		}
	}
	if event.Type == "turn.completed" {
		if event.Usage == nil || event.Usage.InputTokens == nil || event.Usage.CachedInputTokens == nil ||
			event.Usage.OutputTokens == nil || event.Usage.ReasoningOutputTokens == nil ||
			*event.Usage.InputTokens < 0 || *event.Usage.CachedInputTokens < 0 ||
			*event.Usage.OutputTokens < 0 || *event.Usage.ReasoningOutputTokens < 0 {
			writer.fail(errors.New("Codex turn completion omitted valid token usage"))
			return
		}
		if !addObservedTokens(&writer.observation.InputTokens, *event.Usage.InputTokens) ||
			!addObservedTokens(&writer.observation.CachedInputTokens, *event.Usage.CachedInputTokens) ||
			!addObservedTokens(&writer.observation.OutputTokens, *event.Usage.OutputTokens) ||
			!addObservedTokens(&writer.observation.ReasoningOutputTokens, *event.Usage.ReasoningOutputTokens) {
			writer.fail(errors.New("Codex token usage overflowed the Runner record"))
			return
		}
		writer.observation.UsageAvailable = true
	}
}

func (writer *codexEventWriter) fail(err error) {
	if writer.failure != nil || err == nil {
		return
	}
	writer.failure = err
	if writer.cancel != nil {
		writer.cancel(err)
	}
}

func addObservedTokens(total *int64, value int64) bool {
	if value < 0 || *total > int64(^uint64(0)>>1)-value {
		return false
	}
	*total += value
	return true
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func readBounded(path string, maximum int64) ([]byte, error) {
	clean := filepath.Clean(path)
	if maximum < 0 || (!strings.HasPrefix(clean, "/input/") && !strings.HasPrefix(clean, "/output/") &&
		!strings.HasPrefix(clean, "/opt/worksflow-agent/")) {
		return nil, errors.New("path is outside the fixed Runner mounts")
	}
	return readRegularBounded(clean, maximum)
}

func readRegularBounded(clean string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(clean)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return nil, errors.New("file is missing, non-regular, or outside the Runner bound")
	}
	file, err := os.Open(clean)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("file identity changed while opening")
	}
	value, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(value)) > maximum || int64(len(value)) != info.Size() {
		return nil, errors.New("file changed while being read or exceeds Runner bound")
	}
	return value, nil
}

func validateModelGatewayEnvironment() error {
	token := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")), "/")
	parsed, err := url.Parse(baseURL)
	host := ""
	if err == nil {
		host = parsed.Hostname()
	}
	ip := net.ParseIP(host)
	if token == "" || token != os.Getenv("OPENAI_API_KEY") || len(token) > 2048 ||
		strings.ContainsAny(token, "\r\n\x00") || err != nil || parsed.User != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/internal/agent-model/v1" ||
		host == "" || strings.EqualFold(host, "localhost") ||
		(ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) {
		return errors.New("attempt-scoped Model Gateway credentials are invalid")
	}
	return nil
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

type boundedWriter struct {
	writer    io.Writer
	remaining int64
}

func (writer *boundedWriter) Write(value []byte) (int, error) {
	if int64(len(value)) > writer.remaining {
		return 0, errors.New("Codex output exceeded the TaskCapsule log bound")
	}
	written, err := writer.writer.Write(value)
	writer.remaining -= int64(written)
	return written, err
}

func writeExclusiveJSON(path string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func boundedError(err error) string {
	value := strings.TrimSpace(err.Error())
	if len(value) > 2000 {
		value = value[:2000]
	}
	return value
}
