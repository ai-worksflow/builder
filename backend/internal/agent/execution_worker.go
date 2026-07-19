package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

type ExecutionQueue interface {
	ListClaimable(context.Context, int) ([]AgentAttempt, error)
	GetAttempt(context.Context, string, string) (AgentAttempt, error)
	GetContextPack(context.Context, string, string) (ContextPack, error)
	GetTaskCapsule(context.Context, string, string) (TaskCapsule, error)
}

type ExecutionLifecycle interface {
	Claim(context.Context, WorkerPrincipal, string, string, uint64, time.Duration) (AgentAttempt, error)
	Renew(context.Context, WorkerPrincipal, string, string, uint64, uint64, time.Duration) (AgentAttempt, error)
	Advance(context.Context, WorkerPrincipal, WorkerAdvanceInput) (AgentAttempt, error)
}

type ExecutionTreeResolver interface {
	ResolveExactTree(context.Context, TaskCapsule) (repository.TreeManifest, error)
}

type ExecutionWorktrees interface {
	Prepare(context.Context, string, string, uint64, repository.TreeManifest) (WorktreeLease, error)
	Cleanup(WorktreeLease) error
}

type ExecutionContextBuilder interface {
	Materialize(context.Context, AgentAttempt, TaskCapsule, ContextPack, WorktreeLease) (MaterializedContext, []byte, error)
}

type ExecutionRunner interface {
	Run(context.Context, AgentAttempt, TaskCapsule, ContextPack, WorktreeLease, []byte, []byte) (CodexRunnerResult, error)
}

type ExecutionEvidence interface {
	PutPending(context.Context, AgentAttempt, EvidenceKind, string, []byte) (BlobReference, error)
	Get(context.Context, AgentAttempt, EvidenceKind, BlobReference) ([]byte, error)
	Finalize(context.Context, AgentAttempt, EvidenceKind, BlobReference) error
	Abort(context.Context, AgentAttempt, EvidenceKind, BlobReference) error
}

type ExecutionFiles interface {
	Put(context.Context, string, string, []byte) (repository.FileBlobWriteResult, error)
	Resolve(context.Context, string, string, int64) (repository.FileBlobPointer, []byte, error)
}

type ExecutionWorkerConfig struct {
	WorkerID      string
	ClaimBatch    int
	PollInterval  time.Duration
	LeaseDuration time.Duration
	Heartbeat     time.Duration
}

type ExecutionWorker struct {
	queue       ExecutionQueue
	lifecycle   ExecutionLifecycle
	trees       ExecutionTreeResolver
	worktrees   ExecutionWorktrees
	contexts    ExecutionContextBuilder
	runner      ExecutionRunner
	evidence    ExecutionEvidence
	files       ExecutionFiles
	config      ExecutionWorkerConfig
	logger      *slog.Logger
	readinessMu sync.RWMutex
	lastError   error
}

func NewExecutionWorker(
	queue ExecutionQueue,
	lifecycle ExecutionLifecycle,
	trees ExecutionTreeResolver,
	worktrees ExecutionWorktrees,
	contexts ExecutionContextBuilder,
	runner ExecutionRunner,
	evidence ExecutionEvidence,
	files ExecutionFiles,
	config ExecutionWorkerConfig,
	logger *slog.Logger,
) (*ExecutionWorker, error) {
	workerID, err := normalizeStableValue(config.WorkerID, 160)
	if err != nil || workerID != config.WorkerID || queue == nil || lifecycle == nil || trees == nil ||
		worktrees == nil || contexts == nil || runner == nil || evidence == nil || files == nil ||
		config.ClaimBatch < 1 || config.ClaimBatch > 100 || config.PollInterval <= 0 ||
		config.PollInterval > time.Minute || config.LeaseDuration < time.Second ||
		config.LeaseDuration > 10*time.Minute || config.LeaseDuration%time.Second != 0 ||
		config.Heartbeat <= 0 || config.Heartbeat >= config.LeaseDuration {
		return nil, fmt.Errorf("%w: Agent execution worker configuration", ErrExecutionBlocked)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ExecutionWorker{
		queue: queue, lifecycle: lifecycle, trees: trees, worktrees: worktrees,
		contexts: contexts, runner: runner, evidence: evidence, files: files,
		config: config, logger: logger,
	}, nil
}

func (worker *ExecutionWorker) Run(ctx context.Context) error {
	if worker == nil || ctx == nil {
		return fmt.Errorf("%w: Agent execution worker context", ErrExecutionBlocked)
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			processed, err := worker.RunOnce(ctx)
			worker.recordReadiness(err)
			if err != nil && !errors.Is(err, context.Canceled) {
				worker.logger.Error("Agent execution attempt failed", "error", err)
			}
			delay := worker.config.PollInterval
			if processed {
				delay = 10 * time.Millisecond
			}
			timer.Reset(delay)
		}
	}
}

func (worker *ExecutionWorker) RunOnce(ctx context.Context) (bool, error) {
	if worker == nil || ctx == nil {
		return false, fmt.Errorf("%w: Agent execution worker context", ErrExecutionBlocked)
	}
	claimable, err := worker.queue.ListClaimable(ctx, worker.config.ClaimBatch)
	if err != nil {
		return false, err
	}
	for _, candidate := range claimable {
		principal := WorkerPrincipal{ActorID: candidate.CreatedBy, WorkerID: worker.config.WorkerID}
		claimed, err := worker.lifecycle.Claim(
			ctx,
			principal,
			candidate.ProjectID,
			candidate.ID,
			candidate.Version,
			worker.config.LeaseDuration,
		)
		if err != nil {
			if errors.Is(err, ErrAttemptVersionConflict) || errors.Is(err, ErrAttemptLease) ||
				errors.Is(err, ErrAttemptState) || errors.Is(err, ErrAttemptFenced) ||
				errors.Is(err, ErrAttemptNotFound) {
				continue
			}
			return false, err
		}
		return true, worker.processClaim(ctx, principal, claimed)
	}
	return false, nil
}

func (worker *ExecutionWorker) processClaim(
	ctx context.Context,
	principal WorkerPrincipal,
	attempt AgentAttempt,
) error {
	pack, err := worker.queue.GetContextPack(ctx, attempt.ProjectID, attempt.ContextPack.ID)
	if err != nil {
		return worker.failClaim(ctx, principal, attempt, nil, err)
	}
	capsule, err := worker.queue.GetTaskCapsule(ctx, attempt.ProjectID, attempt.TaskCapsule.ID)
	if err != nil || pack.ExactReference() != attempt.ContextPack || capsule.ExactReference() != attempt.TaskCapsule ||
		capsule.ContextPack != pack.ExactReference() {
		if err == nil {
			err = fmt.Errorf("%w: persisted execution plan drifted", ErrExecutionDrift)
		}
		return worker.failClaim(ctx, principal, attempt, nil, err)
	}
	base, err := worker.trees.ResolveExactTree(ctx, capsule)
	if err != nil {
		return worker.failClaim(ctx, principal, attempt, nil, err)
	}
	if base.TreeHash != attempt.BaseCandidateTreeHash {
		return worker.failClaim(ctx, principal, attempt, nil, fmt.Errorf("%w: exact base tree", ErrExecutionDrift))
	}

	switch attempt.State {
	case AttemptClaimed:
		attempt, err = worker.lifecycle.Advance(ctx, principal, WorkerAdvanceInput{
			ProjectID: attempt.ProjectID, AttemptID: attempt.ID,
			ExpectedVersion: attempt.Version, ExpectedFenceEpoch: attempt.FenceEpoch,
			Target: AttemptRunning, Reason: "digest-pinned Runner execution started",
		})
		if err != nil {
			return err
		}
		fallthrough
	case AttemptRunning:
		attempt, err = worker.executeRunner(ctx, principal, attempt, capsule, pack, base)
		if err != nil {
			return worker.failClaim(ctx, principal, attempt, nil, err)
		}
		if finalState(attempt.State) {
			return nil
		}
	case AttemptPatchReady, AttemptValidating:
		// An expired lease can resume from immutable Patch evidence without
		// rerunning the model or trusting a discarded worktree.
	default:
		return fmt.Errorf("%w: claimed unsupported state %s", ErrExecutionDrift, attempt.State)
	}
	return worker.validatePatch(ctx, principal, attempt, capsule, base)
}

func (worker *ExecutionWorker) executeRunner(
	ctx context.Context,
	principal WorkerPrincipal,
	attempt AgentAttempt,
	capsule TaskCapsule,
	pack ContextPack,
	base repository.TreeManifest,
) (AgentAttempt, error) {
	lease, err := worker.worktrees.Prepare(ctx, attempt.ProjectID, attempt.ID, attempt.FenceEpoch, base)
	if err != nil {
		return attempt, err
	}
	defer func() {
		if cleanupErr := worker.worktrees.Cleanup(lease); cleanupErr != nil {
			worker.logger.Error("Agent worktree cleanup failed", "attempt_id", attempt.ID, "error", cleanupErr)
		}
	}()
	_, prompt, err := worker.contexts.Materialize(ctx, attempt, capsule, pack, lease)
	if err != nil {
		return attempt, err
	}
	schema, schemaHash, err := QualifiedOutputSchema()
	if err != nil || schemaHash != capsule.OutputSchemaHash {
		if err == nil {
			err = fmt.Errorf("%w: qualified output schema changed", ErrExecutionDrift)
		}
		return attempt, err
	}
	result, runErr, heartbeatErr := worker.runWithHeartbeat(
		ctx,
		principal,
		attempt,
		capsule,
		pack,
		lease,
		prompt,
		schema,
	)
	current, loadErr := worker.queue.GetAttempt(ctx, attempt.ProjectID, attempt.ID)
	if loadErr == nil {
		attempt = current
	}
	if heartbeatErr != nil {
		return attempt, heartbeatErr
	}
	if loadErr != nil {
		return attempt, loadErr
	}
	if attempt.State != AttemptRunning || attempt.FenceEpoch != lease.Fence || attempt.Lease == nil ||
		attempt.Lease.WorkerID != principal.WorkerID {
		return attempt, ErrAttemptFenced
	}
	if runErr != nil {
		if err := worker.failClaim(ctx, principal, attempt, &result, runErr); err != nil {
			return attempt, err
		}
		terminal, err := worker.queue.GetAttempt(context.WithoutCancel(ctx), attempt.ProjectID, attempt.ID)
		if err != nil {
			return attempt, err
		}
		return terminal, nil
	}
	captured, err := CaptureWorktreePatch(
		lease.Workspace,
		base,
		capsule.WriteSet,
		capsule.ProtectedPaths,
		capsule.Budgets.MaxPatchBytes,
	)
	if err != nil {
		return attempt, err
	}
	if err := worker.registerPatchFiles(ctx, attempt, captured); err != nil {
		return attempt, err
	}
	patch, err := NewPlatformPatch(attempt, capsule, captured)
	if err != nil {
		return attempt, err
	}
	patchBytes, err := domain.CanonicalJSON(patch)
	if err != nil {
		return attempt, err
	}
	return worker.persistPatchEvidence(ctx, principal, attempt, patchBytes, result)
}

func (worker *ExecutionWorker) runWithHeartbeat(
	ctx context.Context,
	principal WorkerPrincipal,
	attempt AgentAttempt,
	capsule TaskCapsule,
	pack ContextPack,
	lease WorktreeLease,
	prompt, schema []byte,
) (CodexRunnerResult, error, error) {
	runCtx, cancel := context.WithTimeout(
		ctx,
		time.Duration(capsule.Budgets.WallTimeSeconds)*time.Second+time.Minute,
	)
	defer cancel()
	done := make(chan struct{})
	heartbeatResult := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(worker.config.Heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				heartbeatResult <- nil
				return
			case <-runCtx.Done():
				heartbeatResult <- runCtx.Err()
				return
			case <-ticker.C:
				current, err := worker.queue.GetAttempt(runCtx, attempt.ProjectID, attempt.ID)
				if err == nil && (current.FenceEpoch != attempt.FenceEpoch || current.Lease == nil ||
					current.Lease.WorkerID != principal.WorkerID || current.State != AttemptRunning) {
					err = ErrAttemptFenced
				}
				if err == nil {
					_, err = worker.lifecycle.Renew(
						runCtx, principal, current.ProjectID, current.ID,
						current.Version, current.FenceEpoch, worker.config.LeaseDuration,
					)
				}
				if err != nil {
					cancel()
					heartbeatResult <- err
					return
				}
			}
		}
	}()
	result, runErr := worker.runner.Run(runCtx, attempt, capsule, pack, lease, prompt, schema)
	close(done)
	heartbeatErr := <-heartbeatResult
	return result, runErr, heartbeatErr
}

func (worker *ExecutionWorker) registerPatchFiles(
	ctx context.Context,
	attempt AgentAttempt,
	captured CapturedPatch,
) error {
	for _, change := range captured.Changes {
		if change.Operation.Kind != repository.OperationUpsert {
			continue
		}
		written, err := worker.files.Put(ctx, attempt.ProjectID, attempt.CreatedBy, change.Content)
		if err != nil {
			return fmt.Errorf("register platform-captured file %s: %w", change.Operation.Path, err)
		}
		if written.Pointer.ContentHash != change.Operation.ContentHash ||
			written.Pointer.ByteSize != change.Operation.ByteSize {
			return fmt.Errorf("%w: registered file %s drifted", ErrExecutionDrift, change.Operation.Path)
		}
	}
	return nil
}

type pendingAttemptEvidence struct {
	kind      EvidenceKind
	reference BlobReference
}

func (worker *ExecutionWorker) persistPatchEvidence(
	ctx context.Context,
	principal WorkerPrincipal,
	attempt AgentAttempt,
	patchBytes []byte,
	result CodexRunnerResult,
) (AgentAttempt, error) {
	values := []struct {
		kind      EvidenceKind
		mediaType string
		value     []byte
	}{
		{EvidencePatch, "application/json", patchBytes},
		{EvidenceStructuredResult, "application/json", result.StructuredResult},
		{EvidenceStdout, "application/x-ndjson", truncateLogEvidence(result.Events)},
		{EvidenceStderr, "text/plain; charset=utf-8", truncateLogEvidence(result.Stderr)},
	}
	pending := make([]pendingAttemptEvidence, 0, len(values))
	for _, value := range values {
		reference, err := worker.evidence.PutPending(ctx, attempt, value.kind, value.mediaType, value.value)
		if err != nil {
			worker.abortEvidence(ctx, attempt, pending)
			return attempt, err
		}
		pending = append(pending, pendingAttemptEvidence{kind: value.kind, reference: reference})
	}
	evidence := AttemptEvidence{
		Patch: &pending[0].reference, StructuredResult: &pending[1].reference,
		Stdout: &pending[2].reference, Stderr: &pending[3].reference,
	}
	next, err := worker.lifecycle.Advance(ctx, principal, WorkerAdvanceInput{
		ProjectID: attempt.ProjectID, AttemptID: attempt.ID,
		ExpectedVersion: attempt.Version, ExpectedFenceEpoch: attempt.FenceEpoch,
		Target: AttemptPatchReady, Reason: "platform captured and content-addressed the fenced worktree diff",
		Evidence: evidence,
	})
	if err != nil {
		// The SQL transaction outcome may be ambiguous. Leave pending objects
		// for the SQL-authoritative reconciler instead of risking deletion of
		// evidence that became reachable just before a connection failure.
		return attempt, err
	}
	worker.finalizeEvidence(ctx, next, pending)
	return next, nil
}

func (worker *ExecutionWorker) validatePatch(
	ctx context.Context,
	principal WorkerPrincipal,
	attempt AgentAttempt,
	capsule TaskCapsule,
	base repository.TreeManifest,
) error {
	if attempt.Evidence.Patch == nil || attempt.Evidence.StructuredResult == nil {
		return worker.failClaim(ctx, principal, attempt, nil, fmt.Errorf("%w: immutable Patch evidence is absent", ErrExecutionDrift))
	}
	patchBytes, err := worker.evidence.Get(ctx, attempt, EvidencePatch, *attempt.Evidence.Patch)
	if err != nil {
		return worker.failClaim(ctx, principal, attempt, nil, err)
	}
	var patch PlatformPatch
	if err := decodeStrictJSON(patchBytes, &patch); err != nil {
		return worker.failClaim(ctx, principal, attempt, nil, err)
	}
	patch, err = ParsePlatformPatch(patch)
	if err != nil || patch.TaskCapsule != capsule.ExactReference() || patch.AttemptID != attempt.ID ||
		patch.ProjectID != attempt.ProjectID || patch.CandidateID != attempt.CandidateID ||
		patch.ConfigurationHash != attempt.ConfigurationHash {
		if err == nil {
			err = fmt.Errorf("%w: Patch lineage", ErrExecutionDrift)
		}
		return worker.failClaim(ctx, principal, attempt, nil, err)
	}
	for _, operation := range patch.Operations {
		if pathInPolicySet(operation.Path, capsule.ProtectedPaths) ||
			!pathInPolicySet(operation.Path, capsule.WriteSet) {
			return worker.failClaim(ctx, principal, attempt, nil, fmt.Errorf("%w: %s", ErrPatchPolicy, operation.Path))
		}
		if operation.Kind == repository.OperationUpsert {
			pointer, value, resolveErr := worker.files.Resolve(
				ctx, attempt.ProjectID, operation.ContentHash, operation.ByteSize,
			)
			if resolveErr != nil || pointer.ContentHash != operation.ContentHash ||
				pointer.ByteSize != operation.ByteSize || int64(len(value)) != operation.ByteSize ||
				rawWorktreeHash(value) != operation.ContentHash {
				return worker.failClaim(ctx, principal, attempt, nil, fmt.Errorf("%w: Patch file %s is unreachable", ErrExecutionDrift, operation.Path))
			}
		}
	}
	if _, err := ApplyPlatformPatch(base, patch); err != nil {
		return worker.failClaim(ctx, principal, attempt, nil, err)
	}
	structured, err := worker.evidence.Get(
		ctx, attempt, EvidenceStructuredResult, *attempt.Evidence.StructuredResult,
	)
	if err != nil {
		return worker.failClaim(ctx, principal, attempt, nil, err)
	}
	schema, schemaHash, err := QualifiedOutputSchema()
	if err != nil || schemaHash != capsule.OutputSchemaHash || validateRunnerStructuredResult(schema, structured) != nil {
		return worker.failClaim(ctx, principal, attempt, nil, fmt.Errorf("%w: structured output evidence", ErrExecutionDrift))
	}
	if attempt.State == AttemptPatchReady {
		attempt, err = worker.lifecycle.Advance(ctx, principal, WorkerAdvanceInput{
			ProjectID: attempt.ProjectID, AttemptID: attempt.ID,
			ExpectedVersion: attempt.Version, ExpectedFenceEpoch: attempt.FenceEpoch,
			Target: AttemptValidating, Reason: "platform Patch integrity validation started",
		})
		if err != nil {
			return err
		}
	}
	if attempt.State != AttemptValidating {
		return ErrAttemptFenced
	}
	if attempt.Evidence.Validation != nil {
		if err := worker.completeValidated(ctx, principal, attempt, *attempt.Evidence.Validation); err != nil {
			return worker.failClaim(ctx, principal, attempt, nil, err)
		}
		return nil
	}
	receipt, err := NewPatchValidationReceipt(attempt, capsule, patch, *attempt.Evidence.Patch)
	if err != nil {
		return worker.failClaim(ctx, principal, attempt, nil, err)
	}
	receiptBytes, err := domain.CanonicalJSON(receipt)
	if err != nil {
		return worker.failClaim(ctx, principal, attempt, nil, err)
	}
	reference, err := worker.evidence.PutPending(
		ctx, attempt, EvidenceValidation, "application/json", receiptBytes,
	)
	if err != nil {
		return worker.failClaim(ctx, principal, attempt, nil, err)
	}
	next, err := worker.lifecycle.Advance(ctx, principal, WorkerAdvanceInput{
		ProjectID: attempt.ProjectID, AttemptID: attempt.ID,
		ExpectedVersion: attempt.Version, ExpectedFenceEpoch: attempt.FenceEpoch,
		Target:   AttemptReviewReady,
		Reason:   "platform Patch integrity passed; independent full-stack quality remains required",
		Evidence: AttemptEvidence{Validation: &reference},
	})
	if err != nil {
		return err
	}
	worker.finalizeEvidence(ctx, next, []pendingAttemptEvidence{{kind: EvidenceValidation, reference: reference}})
	return nil
}

func (worker *ExecutionWorker) completeValidated(
	ctx context.Context,
	principal WorkerPrincipal,
	attempt AgentAttempt,
	reference BlobReference,
) error {
	value, err := worker.evidence.Get(ctx, attempt, EvidenceValidation, reference)
	if err != nil {
		return err
	}
	var receipt PatchValidationReceipt
	if err := decodeStrictJSON(value, &receipt); err != nil {
		return err
	}
	if _, err := ParsePatchValidationReceipt(receipt); err != nil || receipt.AttemptID != attempt.ID ||
		attempt.Evidence.Patch == nil || receipt.Patch != *attempt.Evidence.Patch ||
		receipt.TaskCapsule != attempt.TaskCapsule {
		return fmt.Errorf("%w: existing validation evidence", ErrExecutionDrift)
	}
	_, err = worker.lifecycle.Advance(ctx, principal, WorkerAdvanceInput{
		ProjectID: attempt.ProjectID, AttemptID: attempt.ID,
		ExpectedVersion: attempt.Version, ExpectedFenceEpoch: attempt.FenceEpoch,
		Target:   AttemptReviewReady,
		Reason:   "recovered exact platform Patch integrity validation",
		Evidence: AttemptEvidence{Validation: &reference},
	})
	return err
}

func (worker *ExecutionWorker) failClaim(
	ctx context.Context,
	principal WorkerPrincipal,
	attempt AgentAttempt,
	result *CodexRunnerResult,
	cause error,
) error {
	if cause == nil {
		cause = errors.New("Agent execution failed")
	}
	if errors.Is(cause, context.Canceled) {
		return cause
	}
	expectedFence := attempt.FenceEpoch
	current, err := worker.queue.GetAttempt(context.WithoutCancel(ctx), attempt.ProjectID, attempt.ID)
	if err == nil {
		attempt = current
	}
	if err != nil {
		return cause
	}
	if finalState(attempt.State) {
		return nil
	}
	if attempt.Lease == nil || attempt.Lease.WorkerID != principal.WorkerID ||
		attempt.FenceEpoch != expectedFence {
		return ErrAttemptFenced
	}
	evidence := AttemptEvidence{}
	pending := []pendingAttemptEvidence{}
	if result != nil {
		for _, value := range []struct {
			kind      EvidenceKind
			mediaType string
			value     []byte
			target    **BlobReference
		}{
			{EvidenceStdout, "application/x-ndjson", truncateLogEvidence(result.Events), &evidence.Stdout},
			{EvidenceStderr, "text/plain; charset=utf-8", truncateLogEvidence(result.Stderr), &evidence.Stderr},
		} {
			reference, putErr := worker.evidence.PutPending(
				context.WithoutCancel(ctx), attempt, value.kind, value.mediaType, value.value,
			)
			if putErr != nil {
				break
			}
			copyReference := reference
			*value.target = &copyReference
			pending = append(pending, pendingAttemptEvidence{kind: value.kind, reference: reference})
		}
	}
	target := AttemptFailed
	if errors.Is(cause, context.DeadlineExceeded) || result != nil && result.Record.TimedOut {
		target = AttemptTimedOut
	}
	reason := boundedExitReason(cause)
	next, transitionErr := worker.lifecycle.Advance(context.WithoutCancel(ctx), principal, WorkerAdvanceInput{
		ProjectID: attempt.ProjectID, AttemptID: attempt.ID,
		ExpectedVersion: attempt.Version, ExpectedFenceEpoch: attempt.FenceEpoch,
		Target: target, Reason: "platform recorded the fenced Agent execution failure",
		Evidence: evidence, ExitReason: reason,
	})
	if transitionErr != nil {
		return errors.Join(cause, transitionErr)
	}
	worker.finalizeEvidence(context.WithoutCancel(ctx), next, pending)
	return nil
}

func (worker *ExecutionWorker) abortEvidence(
	ctx context.Context,
	attempt AgentAttempt,
	values []pendingAttemptEvidence,
) {
	for _, value := range values {
		if err := worker.evidence.Abort(context.WithoutCancel(ctx), attempt, value.kind, value.reference); err != nil {
			worker.logger.Error("abort unreachable Agent evidence failed", "attempt_id", attempt.ID, "error", err)
		}
	}
}

func (worker *ExecutionWorker) finalizeEvidence(
	ctx context.Context,
	attempt AgentAttempt,
	values []pendingAttemptEvidence,
) {
	for _, value := range values {
		if err := worker.evidence.Finalize(context.WithoutCancel(ctx), attempt, value.kind, value.reference); err != nil {
			worker.logger.Warn(
				"Agent evidence finalization deferred to reconciler",
				"attempt_id", attempt.ID,
				"content_ref", value.reference.Ref,
				"error", err,
			)
		}
	}
}

func truncateLogEvidence(value []byte) []byte {
	const maximum = 3 << 20
	if len(value) <= maximum {
		return append([]byte(nil), value...)
	}
	marker := []byte("\n[worksflow: evidence truncated to platform bound]\n")
	result := append([]byte(nil), value[:maximum-len(marker)]...)
	return append(result, marker...)
}

func boundedExitReason(err error) string {
	value := err.Error()
	if len(value) > 2000 {
		value = value[:2000]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	if value == "" {
		return "Agent execution failed"
	}
	return value
}

func decodeStrictJSON(value []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: decode exact evidence: %v", ErrExecutionDrift, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing exact evidence JSON", ErrExecutionDrift)
	}
	return nil
}

func (worker *ExecutionWorker) recordReadiness(err error) {
	worker.readinessMu.Lock()
	defer worker.readinessMu.Unlock()
	if err == nil || errors.Is(err, context.Canceled) {
		worker.lastError = nil
		return
	}
	worker.lastError = err
}

func (worker *ExecutionWorker) Readiness(context.Context) error {
	if worker == nil {
		return fmt.Errorf("%w: Agent execution worker is nil", ErrExecutionBlocked)
	}
	worker.readinessMu.RLock()
	defer worker.readinessMu.RUnlock()
	return worker.lastError
}
