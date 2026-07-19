package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

type modelCapabilityIssuerFake struct {
	capability ModelCapability
	issued     int
	revoked    []string
}

func (issuer *modelCapabilityIssuerFake) Issue(
	context.Context,
	AgentAttempt,
	TaskCapsule,
) (ModelCapability, error) {
	issuer.issued++
	return issuer.capability, nil
}

func (issuer *modelCapabilityIssuerFake) Revoke(_ context.Context, id string) error {
	issuer.revoked = append(issuer.revoked, id)
	return nil
}

type codexContainerExecutorFake struct {
	lease            WorktreeLease
	attemptID        string
	schemaHash       string
	internal         bool
	structuredResult []byte
	commands         [][]string
}

func (executor *codexContainerExecutorFake) Run(
	_ context.Context,
	_ string,
	args ...string,
) ([]byte, error) {
	executor.commands = append(executor.commands, append([]string(nil), args...))
	if len(args) >= 2 && args[0] == "network" && args[1] == "inspect" {
		if executor.internal {
			return []byte("true\n"), nil
		}
		return []byte("false\n"), nil
	}
	if len(args) > 0 && args[0] == "rm" {
		return nil, errors.New("No such container")
	}
	if len(args) == 0 || args[0] != "run" {
		return nil, errors.New("unexpected container command")
	}
	result := executor.structuredResult
	if result == nil {
		result = []byte(`{"summary":"Implemented the exact task.","changedPaths":[],"verification":[],"blockers":[]}`)
	}
	for name, value := range map[string][]byte{
		"result.json":  result,
		"events.jsonl": []byte("{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":120,\"cached_input_tokens\":80,\"output_tokens\":30,\"reasoning_output_tokens\":10}}\n"),
		"stderr.log":   {},
	} {
		if err := os.WriteFile(filepath.Join(executor.lease.Output, name), value, 0o600); err != nil {
			return nil, err
		}
	}
	requestPayload, err := os.ReadFile(filepath.Join(executor.lease.Input, "runner-request.json"))
	if err != nil {
		return nil, err
	}
	var request RunnerRequest
	if err := json.Unmarshal(requestPayload, &request); err != nil {
		return nil, err
	}
	record, _ := json.Marshal(RunnerExecutionRecord{
		SchemaVersion: RunnerExecutionSchema, AttemptID: executor.attemptID,
		TaskCapsuleDocumentHash: request.TaskCapsuleDocumentHash,
		ContextPackDocumentHash: request.ContextPackDocumentHash,
		ContextIndexHash:        request.ContextIndexHash,
		PromptHash:              request.PromptHash,
		PromptTemplateHash:      request.PromptTemplateHash,
		OutputSchemaHash:        executor.schemaHash, ExitCode: 0,
		StartedAt: time.Now().UTC().Add(-time.Second), FinishedAt: time.Now().UTC(),
		ResultValidJSON: true, MaxInputTokens: request.MaxInputTokens,
		MaxOutputTokens: request.MaxOutputTokens, MaxCommands: request.MaxCommands,
		ObservedInputTokens: 120, ObservedCachedInputTokens: 80,
		ObservedOutputTokens: 30, ObservedReasoningOutputTokens: 10,
		UsageAvailable: true,
	})
	if err := os.WriteFile(filepath.Join(executor.lease.Output, "execution.json"), record, 0o600); err != nil {
		return nil, err
	}
	return nil, nil
}

func TestDockerCodexRunnerUsesDigestPinnedLeastPrivilegeContainer(t *testing.T) {
	fixture := codexRunnerFixture(t)
	_, fixture.attempt.Executor.PromptHash = QualifiedPromptTemplate()
	issuer := &modelCapabilityIssuerFake{capability: ModelCapability{
		ID: uuid.NewString(), Token: "short-lived-attempt-token",
		BaseURL: "http://agent-model-gateway:8090/v1", ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	}}
	executor := &codexContainerExecutorFake{
		lease: fixture.lease, attemptID: fixture.attempt.ID,
		schemaHash: fixture.capsule.OutputSchemaHash, internal: true,
	}
	runner, err := NewDockerCodexRunner(DockerCodexRunnerConfig{
		RuntimeBinary: "docker", RunnerImage: "registry.example/worksflow/codex-agent@" + fixture.attempt.Executor.RunnerImageDigest,
		Network: "worksflow-agent-model", Memory: "4g", CPUs: "2.0", PIDs: 256,
		OutputLimit: 8 << 20, User: "10001:10001",
	}, issuer, executor)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(
		context.Background(), fixture.attempt, fixture.capsule, fixture.pack,
		fixture.lease, []byte("Implement only the exact sealed task."), fixture.schema,
	)
	if err != nil || result.Record.AttemptID != fixture.attempt.ID || issuer.issued != 1 ||
		len(issuer.revoked) != 1 || issuer.revoked[0] != issuer.capability.ID {
		t.Fatalf("Runner result=%#v issued=%d revoked=%#v err=%v", result, issuer.issued, issuer.revoked, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.lease.Root, "model.env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("model capability file remains after execution: %v", err)
	}
	if len(executor.commands) != 3 {
		t.Fatalf("container commands = %#v", executor.commands)
	}
	run := strings.Join(executor.commands[1], "\n")
	for _, required := range []string{
		"--pull", "never", "--read-only", "--cap-drop", "ALL",
		"no-new-privileges", "--network", "worksflow-agent-model",
		"--user", "10001:10001", "--env-file",
		"/tmp:rw,noexec,nosuid,nodev,size=268435456,uid=10001,gid=10001,mode=0700",
	} {
		if !strings.Contains(run, required) {
			t.Fatalf("Docker run omits %q: %#v", required, executor.commands[1])
		}
	}
	if strings.Contains(run, issuer.capability.Token) {
		t.Fatal("attempt token leaked into Docker CLI arguments")
	}
}

func TestDockerCodexRunnerFailsClosedOnNetworkAndStructuredOutput(t *testing.T) {
	fixture := codexRunnerFixture(t)
	_, fixture.attempt.Executor.PromptHash = QualifiedPromptTemplate()
	issuer := &modelCapabilityIssuerFake{capability: ModelCapability{
		ID: uuid.NewString(), Token: "short-lived-attempt-token",
		BaseURL: "http://agent-model-gateway:8090/v1", ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	}}
	executor := &codexContainerExecutorFake{
		lease: fixture.lease, attemptID: fixture.attempt.ID,
		schemaHash: fixture.capsule.OutputSchemaHash,
	}
	runner, err := NewDockerCodexRunner(DockerCodexRunnerConfig{
		RuntimeBinary: "docker", RunnerImage: "registry.example/worksflow/codex-agent@" + fixture.attempt.Executor.RunnerImageDigest,
		Network: "worksflow-agent-model", Memory: "4g", CPUs: "2.0", PIDs: 256,
		OutputLimit: 8 << 20, User: "10001:10001",
	}, issuer, executor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(
		context.Background(), fixture.attempt, fixture.capsule, fixture.pack,
		fixture.lease, []byte("Implement."), fixture.schema,
	); !errors.Is(err, ErrExecutionBlocked) || issuer.issued != 0 {
		t.Fatalf("non-internal network error=%v issued=%d", err, issuer.issued)
	}

	executor.internal = true
	executor.structuredResult = []byte(`{"changedPaths":[],"verification":[],"blockers":[]}`)
	if _, err := runner.Run(
		context.Background(), fixture.attempt, fixture.capsule, fixture.pack,
		fixture.lease, []byte("Implement."), fixture.schema,
	); !errors.Is(err, ErrExecutionDrift) {
		t.Fatalf("schema-invalid structured result error=%v", err)
	}
	if _, err := NewDockerCodexRunner(DockerCodexRunnerConfig{
		RuntimeBinary: "docker", RunnerImage: "registry.example/worksflow/codex-agent:latest",
		Network: "worksflow-agent-model", Memory: "4g", CPUs: "2.0", PIDs: 256,
		OutputLimit: 8 << 20, User: "10001:10001",
	}, issuer, executor); !errors.Is(err, ErrExecutionBlocked) {
		t.Fatalf("mutable image error=%v", err)
	}
}

func TestValidateRunnerBudgetEvidenceIndependentlyRecomputesEvents(t *testing.T) {
	events := []byte(strings.Join([]string{
		`{"type":"item.started","item":{"id":"command-1","type":"command_execution","command":"go test ./..."}}`,
		`{"type":"item.completed","item":{"id":"command-1","type":"command_execution","status":"completed"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":120,"cached_input_tokens":80,"output_tokens":30,"reasoning_output_tokens":10}}`,
	}, "\n"))
	record := RunnerExecutionRecord{
		MaxInputTokens: 200, MaxOutputTokens: 50, MaxCommands: 1,
		ObservedInputTokens: 120, ObservedCachedInputTokens: 80,
		ObservedOutputTokens: 30, ObservedReasoningOutputTokens: 10,
		ObservedCommands: 1, UsageAvailable: true,
	}
	if err := validateRunnerBudgetEvidence(events, record); err != nil {
		t.Fatalf("exact budget evidence rejected: %v", err)
	}
	record.ObservedCommands = 0
	if err := validateRunnerBudgetEvidence(events, record); err == nil {
		t.Fatal("forged command count was accepted")
	}
	record.ObservedCommands = 1
	record.MaxCommands = 0
	record.BudgetExceeded = true
	record.BudgetExceededKind = "maxCommands"
	record.Error = "TaskCapsule maxCommands budget exceeded"
	if err := validateRunnerBudgetEvidence(events, record); err != nil {
		t.Fatalf("independently verified command overrun was rejected: %v", err)
	}
}

func TestReadCodexRunnerResultPreservesBudgetEvidenceWithoutFinalResult(t *testing.T) {
	output := t.TempDir()
	digest := "sha256:" + strings.Repeat("a", 64)
	record := RunnerExecutionRecord{
		SchemaVersion: RunnerExecutionSchema, AttemptID: uuid.NewString(),
		TaskCapsuleDocumentHash: digest, ContextPackDocumentHash: digest,
		ContextIndexHash: digest, PromptHash: digest, PromptTemplateHash: digest,
		OutputSchemaHash: digest, ExitCode: 1,
		StartedAt: time.Now().UTC().Add(-time.Second), FinishedAt: time.Now().UTC(),
		MaxInputTokens: 100, MaxOutputTokens: 50, MaxCommands: 1,
		ObservedCommands: 2, BudgetExceeded: true, BudgetExceededKind: "maxCommands",
		Error: "TaskCapsule maxCommands budget exceeded",
	}
	recordBytes, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string][]byte{
		"execution.json": recordBytes,
		"events.jsonl": []byte(strings.Join([]string{
			`{"type":"item.started","item":{"id":"command-1","type":"command_execution"}}`,
			`{"type":"item.started","item":{"id":"command-2","type":"command_execution"}}`,
		}, "\n")),
		"stderr.log": nil,
	} {
		if err := os.WriteFile(filepath.Join(output, name), value, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := readCodexRunnerResult(output, 1<<20)
	if err != nil || result.Record.BudgetExceededKind != "maxCommands" || result.StructuredResult != nil {
		t.Fatalf("budget failure result=%#v err=%v", result, err)
	}
	if !bytes.HasPrefix(result.Events, []byte(`{"type":"worksflow.platform.runner_execution","record":`)) {
		t.Fatalf("platform-bound execution record is absent from immutable stdout evidence: %s", result.Events)
	}
	if err := validateRunnerBudgetEvidence(result.Events, result.Record); err != nil {
		t.Fatalf("preserved budget evidence did not validate: %v", err)
	}
}

type codexRunnerTestFixture struct {
	pack    ContextPack
	capsule TaskCapsule
	attempt AgentAttempt
	lease   WorktreeLease
	schema  []byte
}

func codexRunnerFixture(t *testing.T) codexRunnerTestFixture {
	t.Helper()
	schema, schemaHash, err := QualifiedOutputSchema()
	if err != nil {
		t.Fatal(err)
	}
	base := newAgentFixture(t)
	base.taskInput.OutputSchemaHash = schemaHash
	base.taskInput.Budgets.WallTimeSeconds = 300
	pack, err := NewContextPack(base.contextInput, base.now)
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := NewTaskCapsule(base.taskInput, pack, base.now.Add(time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	executorIdentity := testExecutor()
	executorIdentity.OutputSchemaHash = schemaHash
	attempt, err := NewAttempt(NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: base.actorID, Executor: executorIdentity,
	}, capsule, pack, base.now.Add(2*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err = attempt.Advance(AdvanceAttemptInput{
		ExpectedVersion: attempt.Version, ActorID: base.actorID,
		Target: AttemptReady, Reason: "ready",
	}, base.now.Add(3*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err = attempt.Advance(AdvanceAttemptInput{
		ExpectedVersion: attempt.Version, ActorID: base.actorID,
		Target: AttemptQueued, Reason: "queued",
	}, base.now.Add(4*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err = attempt.Claim(
		attempt.Version, base.actorID, "runner-a", 10*time.Minute, base.now.Add(5*time.Microsecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err = attempt.Advance(AdvanceAttemptInput{
		ExpectedVersion: attempt.Version, ExpectedFenceEpoch: attempt.FenceEpoch,
		ActorID: base.actorID, WorkerID: "runner-a", Target: AttemptRunning, Reason: "running",
	}, base.now.Add(6*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repository.NewTree(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Runner tests need only the isolated mount layout; no repository file is
	// materialized in this empty scratch tree.
	root := t.TempDir()
	lease := WorktreeLease{
		AttemptID: attempt.ID, Fence: attempt.FenceEpoch,
		Root: filepath.Join(root, "fence"),
	}
	lease.Workspace = filepath.Join(lease.Root, "workspace")
	lease.Input = filepath.Join(lease.Root, "input")
	lease.Output = filepath.Join(lease.Root, "output")
	for _, path := range []string{lease.Workspace, lease.Input, lease.Output} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	contextEntry := MaterializedContextEntry{
		Key: "runner-test-context", Kind: ContextBuildContract,
		InputPath: "/input/context/items/000.json", Reference: pack.Items[0].Content,
		MaterializedHash: testHash("4"), ByteSize: 2, Required: true,
	}
	materialized := MaterializedContext{
		SchemaVersion: MaterializedContextSchemaVersion,
		TaskCapsule:   capsule.ExactReference(), ContextPack: pack.ExactReference(),
		Entries: []MaterializedContextEntry{contextEntry},
	}
	materialized.ContentHash, err = semanticHash(materializedContextPayload(materialized))
	if err != nil {
		t.Fatal(err)
	}
	contextJSON, err := domain.CanonicalJSON(materialized)
	if err != nil {
		t.Fatal(err)
	}
	contextRoot := filepath.Join(lease.Input, "context")
	if err := os.MkdirAll(contextRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextRoot, "index.json"), contextJSON, 0o400); err != nil {
		t.Fatal(err)
	}
	_ = tree
	return codexRunnerTestFixture{
		pack: pack, capsule: capsule, attempt: attempt, lease: lease, schema: schema,
	}
}
