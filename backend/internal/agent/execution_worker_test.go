package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/storage/content"
)

type executionMemoryControl struct {
	mu      sync.Mutex
	attempt AgentAttempt
	pack    ContextPack
	capsule TaskCapsule
	now     time.Time
}

func (control *executionMemoryControl) ListClaimable(context.Context, int) ([]AgentAttempt, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.attempt.State == AttemptQueued ||
		workerState(control.attempt.State) && control.attempt.Lease != nil && !control.now.Before(control.attempt.Lease.ExpiresAt) {
		return []AgentAttempt{cloneAttempt(control.attempt)}, nil
	}
	return []AgentAttempt{}, nil
}

func (control *executionMemoryControl) GetAttempt(
	context.Context,
	string,
	string,
) (AgentAttempt, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	return cloneAttempt(control.attempt), nil
}

func (control *executionMemoryControl) GetContextPack(
	context.Context,
	string,
	string,
) (ContextPack, error) {
	return control.pack, nil
}

func (control *executionMemoryControl) GetTaskCapsule(
	context.Context,
	string,
	string,
) (TaskCapsule, error) {
	return control.capsule, nil
}

func (control *executionMemoryControl) Claim(
	_ context.Context,
	principal WorkerPrincipal,
	_, _ string,
	expectedVersion uint64,
	ttl time.Duration,
) (AgentAttempt, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	control.tick()
	next, _, err := control.attempt.Claim(
		expectedVersion, principal.ActorID, principal.WorkerID, ttl, control.now,
	)
	if err != nil {
		return AgentAttempt{}, err
	}
	control.attempt = next
	return cloneAttempt(next), nil
}

func (control *executionMemoryControl) Renew(
	_ context.Context,
	principal WorkerPrincipal,
	_, _ string,
	expectedVersion, expectedFence uint64,
	ttl time.Duration,
) (AgentAttempt, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	control.tick()
	next, _, err := control.attempt.Renew(
		expectedVersion, expectedFence, principal.ActorID, principal.WorkerID, ttl, control.now,
	)
	if err != nil {
		return AgentAttempt{}, err
	}
	control.attempt = next
	return cloneAttempt(next), nil
}

func (control *executionMemoryControl) Advance(
	_ context.Context,
	principal WorkerPrincipal,
	input WorkerAdvanceInput,
) (AgentAttempt, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	control.tick()
	next, _, err := control.attempt.Advance(AdvanceAttemptInput{
		ExpectedVersion: input.ExpectedVersion, ExpectedFenceEpoch: input.ExpectedFenceEpoch,
		ActorID: principal.ActorID, WorkerID: principal.WorkerID,
		Target: input.Target, Reason: input.Reason, Evidence: input.Evidence,
		ExitReason: input.ExitReason,
	}, control.now)
	if err != nil {
		return AgentAttempt{}, err
	}
	control.attempt = next
	return cloneAttempt(next), nil
}

func (control *executionMemoryControl) tick() {
	control.now = control.now.Add(time.Microsecond)
}

type executionTreeResolverFake struct {
	tree repository.TreeManifest
}

func (resolver executionTreeResolverFake) ResolveExactTree(
	context.Context,
	TaskCapsule,
) (repository.TreeManifest, error) {
	return resolver.tree, nil
}

type executionFilesFake struct {
	mu       sync.Mutex
	project  string
	actor    string
	pointers map[string]repository.FileBlobPointer
	values   map[string][]byte
}

func newExecutionFilesFake(projectID, actorID string) *executionFilesFake {
	return &executionFilesFake{
		project: projectID, actor: actorID,
		pointers: map[string]repository.FileBlobPointer{}, values: map[string][]byte{},
	}
}

func (files *executionFilesFake) add(value []byte) repository.FileBlobPointer {
	files.mu.Lock()
	defer files.mu.Unlock()
	hash := rawWorktreeHash(value)
	pointer := repository.FileBlobPointer{
		Store: repository.FileContentStore, Ref: uuid.NewString(), OwnerID: uuid.NewString(),
		ContentHash: hash, ByteSize: int64(len(value)), ContentObjectHash: testHash("e"),
	}
	files.pointers[hash] = pointer
	files.values[hash] = append([]byte(nil), value...)
	return pointer
}

func (files *executionFilesFake) Resolve(
	_ context.Context,
	projectID, hash string,
	size int64,
) (repository.FileBlobPointer, []byte, error) {
	files.mu.Lock()
	defer files.mu.Unlock()
	pointer, exists := files.pointers[hash]
	if !exists || projectID != files.project || pointer.ByteSize != size {
		return repository.FileBlobPointer{}, nil, repository.ErrFileBlobNotFound
	}
	return pointer, append([]byte(nil), files.values[hash]...), nil
}

func (files *executionFilesFake) Put(
	_ context.Context,
	projectID, actorID string,
	value []byte,
) (repository.FileBlobWriteResult, error) {
	if projectID != files.project || actorID != files.actor {
		return repository.FileBlobWriteResult{}, repository.ErrInvalidCandidate
	}
	pointer := files.add(value)
	return repository.FileBlobWriteResult{Pointer: pointer}, nil
}

type executionRunnerFake struct {
	protected bool
	noChange  bool
	result    []byte
}

const testedPath = "apps/web/features/conversation/page.tsx"

func (runner executionRunnerFake) Run(
	_ context.Context,
	attempt AgentAttempt,
	capsule TaskCapsule,
	_ ContextPack,
	lease WorktreeLease,
	prompt, schema []byte,
) (CodexRunnerResult, error) {
	if len(prompt) == 0 || rawWorktreeHash(schema) != capsule.OutputSchemaHash {
		return CodexRunnerResult{}, ErrExecutionDrift
	}
	structured := completeRunnerResult(testedPath, capsule)
	if runner.result != nil {
		structured = runner.result
	}
	if runner.noChange {
		return CodexRunnerResult{
			StructuredResult: structured,
			Events:           []byte("{\"type\":\"turn.completed\"}\n"),
			Stderr:           []byte{},
			Record: RunnerExecutionRecord{
				SchemaVersion: RunnerExecutionSchema, AttemptID: attempt.ID,
				OutputSchemaHash: capsule.OutputSchemaHash, ExitCode: 0, ResultValidJSON: true,
			},
		}, nil
	}
	target := filepath.Join(lease.Workspace, filepath.FromSlash(testedPath))
	if runner.protected {
		target = filepath.Join(lease.Workspace, ".github", "workflows", "escape.yml")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return CodexRunnerResult{}, err
	}
	if err := os.WriteFile(target, []byte("export const result = 'implemented'\n"), 0o600); err != nil {
		return CodexRunnerResult{}, err
	}
	return CodexRunnerResult{
		StructuredResult: structured,
		Events:           []byte("{\"type\":\"turn.completed\"}\n"),
		Stderr:           []byte{},
		Record: RunnerExecutionRecord{
			SchemaVersion: RunnerExecutionSchema, AttemptID: attempt.ID,
			OutputSchemaHash: capsule.OutputSchemaHash, ExitCode: 0, ResultValidJSON: true,
		},
	}, nil
}

func completeRunnerResult(path string, capsule TaskCapsule) []byte {
	result := runnerStructuredResult{
		Summary:      "Implemented the complete exact task.",
		ChangedPaths: []string{path},
		ResourceGraph: runnerResourceGraph{
			Applicable: frontendResourceSourcePath(path),
			Nodes:      []runnerResourceNode{},
			Edges:      []runnerResourceEdge{},
		},
		Blockers: []string{},
	}
	for _, id := range capsule.ObligationIDs {
		result.Obligations = append(result.Obligations, runnerCoverageResult{
			ID: id, Status: "satisfied", Note: "Implemented and wired.",
		})
	}
	for _, id := range capsule.AcceptanceCriterionIDs {
		result.AcceptanceCriteria = append(result.AcceptanceCriteria, runnerCoverageResult{
			ID: id, Status: "satisfied", Note: "Implementation satisfies the criterion.",
		})
	}
	for _, id := range capsule.VerificationCommandIDs {
		result.Verification = append(result.Verification, runnerVerificationResult{
			CommandID: id, Status: "passed", Note: "Passed.",
		})
	}
	payload, err := json.Marshal(result)
	if err != nil {
		panic(err)
	}
	return payload
}

type executionWorkerFixture struct {
	control  *executionMemoryControl
	worker   *ExecutionWorker
	evidence *EvidenceStore
	contents *evidenceContentStoreFake
}

func TestExecutionWorkerProducesReviewableImmutablePlatformPatch(t *testing.T) {
	fixture := newExecutionWorkerFixture(t, false)
	processed, err := fixture.worker.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%t err=%v", processed, err)
	}
	attempt, _ := fixture.control.GetAttempt(context.Background(), "", "")
	if attempt.State != AttemptReviewReady || attempt.Evidence.Patch == nil ||
		attempt.Evidence.StructuredResult == nil || attempt.Evidence.Validation == nil ||
		attempt.Lease != nil || attempt.FinishedAt == nil {
		t.Fatalf("completed Attempt = %#v", attempt)
	}
	patchBytes, err := fixture.evidence.Get(
		context.Background(), attempt, EvidencePatch, *attempt.Evidence.Patch,
	)
	if err != nil {
		t.Fatal(err)
	}
	var patch PlatformPatch
	if err := json.Unmarshal(patchBytes, &patch); err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePlatformPatch(patch); err != nil || len(patch.Operations) != 1 ||
		patch.Operations[0].Path != "apps/web/features/conversation/page.tsx" {
		t.Fatalf("platform Patch=%#v err=%v", patch, err)
	}
	validationBytes, err := fixture.evidence.Get(
		context.Background(), attempt, EvidenceValidation, *attempt.Evidence.Validation,
	)
	if err != nil {
		t.Fatal(err)
	}
	var receipt PatchValidationReceipt
	if err := json.Unmarshal(validationBytes, &receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePatchValidationReceipt(receipt); err != nil ||
		!receipt.IndependentQualityRequired || receipt.Decision != "reviewable" {
		t.Fatalf("validation receipt=%#v err=%v", receipt, err)
	}
	for _, reference := range []*BlobReference{
		attempt.Evidence.Patch, attempt.Evidence.StructuredResult,
		attempt.Evidence.Stdout, attempt.Evidence.Stderr, attempt.Evidence.Validation,
	} {
		if reference == nil || fixture.contents.items[reference.Ref].State != content.StateFinalized {
			t.Fatalf("evidence was not finalized: %#v", reference)
		}
	}
}

func TestExecutionWorkerRejectsProtectedWorktreeChangeWithoutPatch(t *testing.T) {
	fixture := newExecutionWorkerFixture(t, true)
	processed, err := fixture.worker.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%t err=%v", processed, err)
	}
	attempt, _ := fixture.control.GetAttempt(context.Background(), "", "")
	if attempt.State != AttemptFailed || attempt.Evidence.Patch != nil || attempt.ExitReason == "" {
		t.Fatalf("protected-path Attempt = %#v", attempt)
	}
}

func TestExecutionWorkerPersistsSuccessfulRunnerEvidenceWhenPatchIsEmpty(t *testing.T) {
	fixture := newExecutionWorkerFixture(t, false)
	fixture.worker.runner = executionRunnerFake{noChange: true}
	processed, err := fixture.worker.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%t err=%v", processed, err)
	}
	attempt, _ := fixture.control.GetAttempt(context.Background(), "", "")
	if attempt.State != AttemptFailed || attempt.Evidence.Patch != nil ||
		attempt.Evidence.StructuredResult == nil || attempt.Evidence.Stdout == nil ||
		attempt.Evidence.Stderr == nil || attempt.ExitReason == "" {
		t.Fatalf("empty-patch Attempt = %#v", attempt)
	}
	for _, reference := range []*BlobReference{
		attempt.Evidence.StructuredResult, attempt.Evidence.Stdout, attempt.Evidence.Stderr,
	} {
		if fixture.contents.items[reference.Ref].State != content.StateFinalized {
			t.Fatalf("failure evidence was not finalized: %#v", reference)
		}
	}
}

func TestExecutionWorkerRejectsPartialRunnerCoverageWithoutReviewablePatch(t *testing.T) {
	fixture := newExecutionWorkerFixture(t, false)
	result := completeRunnerResult(testedPath, fixture.control.capsule)
	var partial runnerStructuredResult
	if err := json.Unmarshal(result, &partial); err != nil {
		t.Fatal(err)
	}
	partial.AcceptanceCriteria = partial.AcceptanceCriteria[:1]
	result, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	fixture.worker.runner = executionRunnerFake{result: result}
	processed, err := fixture.worker.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%t err=%v", processed, err)
	}
	attempt, _ := fixture.control.GetAttempt(context.Background(), "", "")
	if attempt.State != AttemptFailed || attempt.Evidence.Patch != nil ||
		attempt.Evidence.StructuredResult == nil || attempt.ExitReason == "" {
		t.Fatalf("partial-coverage Attempt = %#v", attempt)
	}
}

func newExecutionWorkerFixture(t *testing.T, protected bool) executionWorkerFixture {
	t.Helper()
	baseFixture := newAgentFixture(t)
	schema, schemaHash, err := QualifiedOutputSchema()
	if err != nil || len(schema) == 0 {
		t.Fatal(err)
	}
	contractPayload := []byte(`{"status":"ready"}`)
	contractOwner, contractRef := uuid.NewString(), uuid.NewString()
	files := newExecutionFilesFake(baseFixture.contextInput.ProjectID, baseFixture.actorID)
	baseValue := []byte("export const result = 'base'\n")
	basePointer := files.add(baseValue)
	baseTree, err := repository.NewTree([]repository.TreeFile{{
		Path: "apps/web/features/conversation/page.tsx", Mode: "100644",
		ContentHash: basePointer.ContentHash, ByteSize: basePointer.ByteSize,
	}})
	if err != nil {
		t.Fatal(err)
	}
	baseFixture.contextInput.BaseCandidateTreeHash = baseTree.TreeHash
	baseFixture.contextInput.Items = []ContextItem{
		{
			Key: "contract:root", Kind: ContextBuildContract,
			Source: &baseFixture.contextInput.BuildContract, Required: true,
			Content: BlobReference{
				Store: AgentEvidenceStore, OwnerID: contractOwner, Ref: contractRef,
				ContentHash: rawWorktreeHash(contractPayload), ByteSize: int64(len(contractPayload)),
			},
		},
		{
			Key: "repo:conversation", Kind: ContextRepositoryFile,
			Path: "apps/web/features/conversation/page.tsx", Required: true,
			Content: BlobReference{
				Store: "repository_file", OwnerID: basePointer.OwnerID, Ref: basePointer.Ref,
				ContentHash: basePointer.ContentHash, ByteSize: basePointer.ByteSize,
			},
		},
	}
	pack, err := NewContextPack(baseFixture.contextInput, baseFixture.now)
	if err != nil {
		t.Fatal(err)
	}
	baseFixture.taskInput.BaseCandidateTreeHash = baseTree.TreeHash
	baseFixture.taskInput.WriteSet = []string{"apps/web/features/conversation"}
	baseFixture.taskInput.ReadSet = []string{"apps/web/features/conversation"}
	baseFixture.taskInput.OutputSchemaHash = schemaHash
	capsule, err := NewTaskCapsule(baseFixture.taskInput, pack, baseFixture.now.Add(time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	executor := testExecutor()
	executor.OutputSchemaHash = schemaHash
	_, executor.PromptHash = QualifiedPromptTemplate()
	attempt, err := NewAttempt(NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: baseFixture.actorID, Executor: executor,
	}, capsule, pack, baseFixture.now.Add(2*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err = attempt.Advance(AdvanceAttemptInput{
		ExpectedVersion: attempt.Version, ActorID: baseFixture.actorID,
		Target: AttemptReady, Reason: "ready",
	}, baseFixture.now.Add(3*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err = attempt.Advance(AdvanceAttemptInput{
		ExpectedVersion: attempt.Version, ActorID: baseFixture.actorID,
		Target: AttemptQueued, Reason: "queued",
	}, baseFixture.now.Add(4*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	control := &executionMemoryControl{
		attempt: attempt, pack: pack, capsule: capsule, now: attempt.UpdatedAt,
	}
	contents := newEvidenceContentStoreFake()
	contents.items[contractRef] = content.StoredContent{
		Reference: content.Reference{
			ID: contractRef, ContentHash: rawWorktreeHash(contractPayload),
			ByteSize: int64(len(contractPayload)), SchemaVersion: 1,
		},
		ProjectID: pack.ProjectID, AggregateType: "application_build_contract",
		AggregateID: contractOwner, State: content.StateFinalized,
		Payload: json.RawMessage(contractPayload), CreatedAt: baseFixture.now,
	}
	evidence, err := NewEvidenceStore(contents)
	if err != nil {
		t.Fatal(err)
	}
	worktrees, err := NewWorktreeManager(filepath.Join(t.TempDir(), "agent-worktrees"), files)
	if err != nil {
		t.Fatal(err)
	}
	contexts, err := NewContextMaterializer(contents, files, templateContextReaderFake{})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewExecutionWorker(
		control,
		control,
		executionTreeResolverFake{tree: baseTree},
		worktrees,
		contexts,
		executionRunnerFake{protected: protected},
		evidence,
		files,
		ExecutionWorkerConfig{
			WorkerID: "runner-test", ClaimBatch: 10, PollInterval: time.Second,
			LeaseDuration: 2 * time.Minute, Heartbeat: time.Minute,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return executionWorkerFixture{control: control, worker: worker, evidence: evidence, contents: contents}
}
