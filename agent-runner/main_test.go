package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDecodeRequestRequiresEveryExactInputDigest(t *testing.T) {
	request := validRunnerRequest()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRequest(payload)
	if err != nil || decoded.ContextIndexHash != request.ContextIndexHash {
		t.Fatalf("decode request = %#v, err = %v", decoded, err)
	}

	request.PromptHash = ""
	payload, _ = json.Marshal(request)
	if _, err := decodeRequest(payload); err == nil {
		t.Fatal("request without an exact prompt digest was accepted")
	}
	if _, err := decodeRequest(append(payload, []byte(` {}`)...)); err == nil {
		t.Fatal("request with trailing JSON was accepted")
	}
	request = validRunnerRequest()
	request.MaxCommands = 0
	payload, _ = json.Marshal(request)
	if _, err := decodeRequest(payload); err == nil {
		t.Fatal("request without an exact command budget was accepted")
	}
}

func TestValidateTaskCapsuleBudgetsRequiresExactTokenAndCommandBudgets(t *testing.T) {
	request := validRunnerRequest()
	payload := []byte(`{"schemaVersion":"agent-task-capsule/v1","budgets":{"wallTimeSeconds":300,"maxInputTokens":100000,"maxOutputTokens":4096,"maxCommands":7,"maxLogBytes":2097152}}`)
	if err := validateTaskCapsuleBudgets(payload, request); err != nil {
		t.Fatalf("matching budgets were rejected: %v", err)
	}
	request.MaxOutputTokens++
	if err := validateTaskCapsuleBudgets(payload, request); err == nil {
		t.Fatal("drifted output-token budget was accepted")
	}
}

func TestCodexEventWriterCountsStableCommandsAndRecordsUsage(t *testing.T) {
	var output bytes.Buffer
	writer := newCodexEventWriter(&output, 2, nil)
	stream := strings.Join([]string{
		`{"type":"item.started","item":{"id":"command-1","type":"command_execution"}}`,
		`{"type":"item.completed","item":{"id":"command-1","type":"command_execution"}}`,
		`{"type":"item.completed","item":{"id":"message-1","type":"agent_message"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":120,"cached_input_tokens":80,"output_tokens":30,"reasoning_output_tokens":10}}`,
	}, "\n")
	for _, part := range []string{stream[:31], stream[31:97], stream[97:]} {
		if _, err := writer.Write([]byte(part)); err != nil {
			t.Fatalf("write JSONL part: %v", err)
		}
	}
	if err := writer.Finalize(); err != nil {
		t.Fatal(err)
	}
	observed := writer.Observation(200, 50)
	if observed.Commands != 1 || observed.InputTokens != 120 || observed.CachedInputTokens != 80 ||
		observed.OutputTokens != 30 || observed.ReasoningOutputTokens != 10 || !observed.UsageAvailable ||
		observed.BudgetExceeded {
		t.Fatalf("unexpected observation: %#v", observed)
	}
}

func TestCodexEventWriterCancelsOnFirstCommandBeyondBudget(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	writer := newCodexEventWriter(io.Discard, 1, cancel)
	first := []byte(`{"type":"item.started","item":{"id":"command-1","type":"command_execution"}}` + "\n")
	second := []byte(`{"type":"item.started","item":{"id":"command-2","type":"command_execution"}}` + "\n")
	if _, err := writer.Write(first); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(second); err == nil {
		t.Fatal("command beyond maxCommands did not fail the event stream")
	}
	if !errors.Is(context.Cause(ctx), writer.Finalize()) {
		t.Fatalf("command context cause=%v final=%v", context.Cause(ctx), writer.Finalize())
	}
	observed := writer.Observation(1_000, 1_000)
	if !observed.BudgetExceeded || observed.BudgetExceededKind != "maxCommands" || observed.Commands != 2 {
		t.Fatalf("unexpected command budget observation: %#v", observed)
	}
}

func TestCodexEventWriterFailsPostHocOnObservedTokenBudget(t *testing.T) {
	writer := newCodexEventWriter(io.Discard, 1, nil)
	_, err := writer.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":101,"cached_input_tokens":0,"output_tokens":21,"reasoning_output_tokens":0}}` + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Finalize(); err != nil {
		t.Fatal(err)
	}
	observed := writer.Observation(100, 20)
	if !observed.BudgetExceeded || observed.BudgetExceededKind != "maxInputTokens" {
		t.Fatalf("unexpected token budget observation: %#v", observed)
	}
}

func TestCodexEventWriterRejectsIncompleteUsageEvidence(t *testing.T) {
	writer := newCodexEventWriter(io.Discard, 1, nil)
	if _, err := writer.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}` + "\n")); err == nil {
		t.Fatal("turn usage without cached/reasoning counters was accepted")
	}
}

func TestCodexArgumentsIsolateToolEnvironmentAndAvoidDeprecatedFullAuto(t *testing.T) {
	arguments := codexArguments(validRunnerRequest())
	joined := strings.Join(arguments, "\n")
	if strings.Contains(joined, "--full-auto") {
		t.Fatal("deprecated full-auto compatibility flag remains enabled")
	}
	for _, required := range []string{
		"--strict-config",
		"--ignore-user-config",
		"--ignore-rules",
		`shell_environment_policy.include_only=["PATH","HOME"]`,
		"allow_login_shell=false",
		"--sandbox",
		"workspace-write",
	} {
		if !slices.Contains(arguments, required) {
			t.Fatalf("Codex arguments omit %q: %#v", required, arguments)
		}
	}
}

func TestReadRegularBoundedRejectsSymlinksAndOversizeFiles(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "regular.json")
	if err := os.WriteFile(regular, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readRegularBounded(regular, 64)
	if err != nil || string(value) != `{"ok":true}` {
		t.Fatalf("regular file = %q, err = %v", value, err)
	}
	symlink := filepath.Join(root, "link.json")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegularBounded(symlink, 64); err == nil {
		t.Fatal("symlink input was accepted")
	}
	if _, err := readRegularBounded(regular, 2); err == nil {
		t.Fatal("oversize input was accepted")
	}
}

func TestModelGatewayEnvironmentRequiresExactInternalEndpoint(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "attempt-scoped-token")
	t.Setenv("OPENAI_BASE_URL", "http://agent-model-gateway:8080/internal/agent-model/v1")
	if err := validateModelGatewayEnvironment(); err != nil {
		t.Fatalf("qualified gateway environment was rejected: %v", err)
	}
	t.Setenv("OPENAI_BASE_URL", "http://localhost:8080/internal/agent-model/v1")
	if err := validateModelGatewayEnvironment(); err == nil {
		t.Fatal("loopback gateway environment was accepted")
	}
}

func validRunnerRequest() runnerRequest {
	digest := "sha256:" + strings.Repeat("a", 64)
	return runnerRequest{
		SchemaVersion:   requestSchema,
		AttemptID:       "123e4567-e89b-42d3-a456-426614174000",
		Model:           "qualified-model",
		TaskCapsulePath: taskCapsulePath, TaskCapsuleDocumentHash: digest,
		ContextPackPath: contextPackPath, ContextPackDocumentHash: digest,
		ContextIndexPath: contextIndexPath, ContextIndexHash: digest,
		PromptPath: "/input/prompt.txt", PromptHash: digest, PromptTemplateHash: digest,
		OutputSchemaPath: "/input/output.schema.json", OutputSchemaHash: digest,
		WallTimeSeconds: 300, MaxInputTokens: 100_000, MaxOutputTokens: 4096,
		MaxCommands: 7, MaxLogBytes: 1 << 20,
	}
}
