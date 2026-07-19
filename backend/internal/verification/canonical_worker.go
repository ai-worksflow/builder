package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type CanonicalExecutionSpec struct {
	RunID             string
	AttemptID         string
	AttemptFenceEpoch uint64
	PlanID            string
	PlanHash          string
	Content           CanonicalPlanContent
}

type CanonicalExecutionStore interface {
	ReconcileInactiveVerificationExecution(context.Context, Scope, string) (bool, error)
	ClaimVerificationCleanup(context.Context, ClaimVerificationCleanupInput) (VerificationCleanupLease, bool, error)
	CompleteVerificationCleanup(context.Context, CompleteVerificationCleanupInput) error
	FailVerificationCleanup(context.Context, FailVerificationCleanupInput) error
	ConfirmVerificationOperationQuiesced(context.Context, Scope, VerificationExecutionFence, string, string) error
	ClaimCanonicalExecution(context.Context, ClaimCanonicalExecutionInput) (CanonicalExecutionLease, bool, error)
	HeartbeatCanonicalExecution(context.Context, CanonicalExecutionLease, string, time.Duration) (CanonicalExecutionLease, error)
	TransitionCanonicalExecution(context.Context, CanonicalExecutionLease, string, RunState) (CanonicalExecutionLease, error)
	GetCanonicalPlan(context.Context, string, string) (CanonicalPlan, error)
	CompleteCanonicalExecutionCleanup(context.Context, CanonicalExecutionLease, string) error
	CommitCanonicalReceipt(context.Context, CanonicalExecutionLease, CanonicalReceipt, string, string) (CanonicalReceipt, error)
}

type CanonicalMaterializer interface {
	MaterializeCanonical(context.Context, CanonicalExecutionSpec) error
	PrepareCanonical(context.Context, CanonicalExecutionSpec) error
	CollectCanonical(context.Context, CanonicalExecutionSpec) error
	CleanupCanonical(context.Context, VerificationExecutionFence) error
}

type CanonicalCheckExecutionRequest struct {
	ProjectID         string
	RunID             string
	AttemptID         string
	AttemptFenceEpoch uint64
	AttemptCount      uint64
	Subject           CanonicalPlanSubject
	RuntimePolicy     PlanRuntimePolicy
	Dependencies      []PlanDependency
	Check             PlanCheck
}

type CanonicalCheckExecutor interface {
	ExecuteCanonical(context.Context, CanonicalCheckExecutionRequest) (CheckExecutionOutcome, error)
}

type CanonicalArtifactCollector interface {
	CollectReleaseArtifacts(context.Context, CanonicalExecutionSpec, []CheckResult) ([]CanonicalReleaseArtifact, error)
}

type CanonicalWorker struct {
	store        CanonicalExecutionStore
	materializer CanonicalMaterializer
	executor     CanonicalCheckExecutor
	artifacts    CanonicalArtifactCollector
	config       CandidateWorkerConfig
	clock        CandidateWorkerClock
	ids          CandidateWorkerIDGenerator
}

func NewCanonicalWorker(
	store CanonicalExecutionStore,
	materializer CanonicalMaterializer,
	executor CanonicalCheckExecutor,
	artifacts CanonicalArtifactCollector,
	config CandidateWorkerConfig,
	clock CandidateWorkerClock,
	ids CandidateWorkerIDGenerator,
) (*CanonicalWorker, error) {
	if store == nil || materializer == nil || executor == nil || artifacts == nil ||
		!validUUIDs(config.ActorID) || !workerIDPattern.MatchString(config.WorkerID) ||
		config.LeaseDuration < time.Second || config.LeaseDuration > 24*time.Hour ||
		config.HeartbeatInterval <= 0 || config.HeartbeatInterval >= config.LeaseDuration {
		return nil, ErrInvalidWorkerConfig
	}
	if clock == nil {
		clock = systemCandidateWorkerClock{}
	}
	if ids == nil {
		ids = uuidCandidateWorkerIDs{}
	}
	return &CanonicalWorker{
		store: store, materializer: materializer, executor: executor, artifacts: artifacts,
		config: config, clock: clock, ids: ids,
	}, nil
}

func (worker *CanonicalWorker) RunOnce(ctx context.Context) (processed bool, runErr error) {
	if worker == nil || ctx == nil {
		return false, ErrInvalidWorkerConfig
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if reconciled, err := worker.store.ReconcileInactiveVerificationExecution(
		ctx, ScopeCanonical, worker.config.ActorID,
	); err != nil || reconciled {
		return reconciled, err
	}
	cleanup, found, err := worker.store.ClaimVerificationCleanup(ctx, ClaimVerificationCleanupInput{
		Scope: ScopeCanonical, ActorID: worker.config.ActorID, WorkerID: worker.config.WorkerID,
		LeaseDuration: verificationCleanupLeaseDuration(worker.config.LeaseDuration),
	})
	if err != nil || found {
		if err != nil {
			return false, err
		}
		return true, worker.reconcileCleanup(ctx, cleanup)
	}
	attemptID := worker.ids.NewID()
	if !validUUIDs(attemptID) {
		return false, ErrInvalidWorkerConfig
	}
	lease, found, err := worker.store.ClaimCanonicalExecution(ctx, ClaimCanonicalExecutionInput{
		AttemptID: attemptID, ActorID: worker.config.ActorID,
		WorkerID: worker.config.WorkerID, LeaseDuration: worker.config.LeaseDuration,
	})
	if err != nil || !found {
		return found, err
	}
	if err := worker.validateLease(lease, RunClaimed); err != nil {
		return true, err
	}
	fence := canonicalExecutionFence(lease)
	cleanupConfirmed := false
	defer func() {
		if cleanupConfirmed {
			return
		}
		if !errors.Is(runErr, ErrVerificationOperationNotQuiesced) {
			quiescedCtx, quiescedCancel := verificationCleanupContext(ctx)
			_ = worker.store.ConfirmVerificationOperationQuiesced(
				quiescedCtx, ScopeCanonical, fence, worker.config.WorkerID, worker.config.ActorID,
			)
			quiescedCancel()
		}
		cleanupCtx, cancel := verificationCleanupContext(ctx)
		defer cancel()
		if err := worker.materializer.CleanupCanonical(cleanupCtx, fence); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("Canonical execution cleanup: %w", err))
		}
	}()
	plan, err := worker.store.GetCanonicalPlan(ctx, lease.ProjectID, lease.Plan.ID)
	if err != nil {
		return true, err
	}
	parsed, err := ParseCanonicalPlan(plan.Content, plan.PlanHash)
	if err != nil || plan.ID != lease.Plan.ID || plan.PlanHash != lease.Plan.ContentHash ||
		parsed.Content.ProjectID != lease.ProjectID {
		return true, fmt.Errorf("%w: claimed immutable Canonical Plan drifted", ErrInvalidPlan)
	}
	spec, err := newCanonicalExecutionSpec(lease, parsed)
	if err != nil {
		return true, err
	}

	lease, err = worker.transition(ctx, lease, RunMaterializing)
	if err != nil {
		return true, err
	}
	executionError := ""
	if err := worker.runWithHeartbeat(ctx, &lease, func(operationCtx context.Context) error {
		return worker.materializer.MaterializeCanonical(operationCtx, spec)
	}); err != nil {
		if isWorkerStop(ctx, err) {
			return true, err
		}
		executionError = appendExecutionError(executionError, "materialize", err)
	}
	lease, err = worker.transition(ctx, lease, RunPreparing)
	if err != nil {
		return true, err
	}
	if executionError == "" {
		if err := worker.runWithHeartbeat(ctx, &lease, func(operationCtx context.Context) error {
			return worker.materializer.PrepareCanonical(operationCtx, spec)
		}); err != nil {
			if isWorkerStop(ctx, err) {
				return true, err
			}
			executionError = appendExecutionError(executionError, "prepare", err)
		}
	}
	lease, err = worker.transition(ctx, lease, RunRunning)
	if err != nil {
		return true, err
	}
	checks, checkError, err := worker.executeChecks(ctx, &lease, spec, executionError)
	if err != nil {
		return true, err
	}
	if checkError != "" {
		executionError = appendExecutionError(executionError, "execute", errors.New(checkError))
	}
	lease, err = worker.transition(ctx, lease, RunCollecting)
	if err != nil {
		return true, err
	}
	releaseArtifacts := []CanonicalReleaseArtifact{}
	if executionError == "" {
		var collected []CanonicalReleaseArtifact
		collectErr := worker.runWithHeartbeat(ctx, &lease, func(operationCtx context.Context) error {
			var err error
			collected, err = worker.artifacts.CollectReleaseArtifacts(operationCtx, spec, checks)
			return err
		})
		if isWorkerStop(ctx, collectErr) {
			return true, collectErr
		}
		if collectErr != nil {
			executionError = appendExecutionError(executionError, "release artifacts", collectErr)
		} else {
			releaseArtifacts = collected
		}
	}
	if err := worker.runWithHeartbeat(ctx, &lease, func(operationCtx context.Context) error {
		return worker.materializer.CollectCanonical(operationCtx, spec)
	}); err != nil {
		if isWorkerStop(ctx, err) {
			return true, err
		}
		// Cleanup is infrastructure reconciliation, not Canonical quality
		// evidence. Leave the lease-owned Run/Attempt collecting so a fenced
		// takeover can retry; do not mint a Receipt while resources may be live.
		return true, fmt.Errorf("collect Canonical execution resources: %w", err)
	}
	confirmationCtx, cancelConfirmation := context.WithTimeout(ctx, verificationCleanupTimeout)
	confirmationErr := worker.runWithHeartbeat(confirmationCtx, &lease, func(operationCtx context.Context) error {
		return worker.materializer.CleanupCanonical(operationCtx, fence)
	})
	cancelConfirmation()
	if confirmationErr != nil {
		if isWorkerStop(ctx, confirmationErr) {
			return true, confirmationErr
		}
		return true, fmt.Errorf("confirm Canonical execution cleanup: %w", confirmationErr)
	}
	if err := worker.store.CompleteCanonicalExecutionCleanup(ctx, lease, worker.config.ActorID); err != nil {
		return true, fmt.Errorf("complete Canonical cleanup obligation: %w", err)
	}
	cleanupConfirmed = true
	receipt, err := worker.buildReceipt(lease, parsed.Content, checks, releaseArtifacts, executionError)
	if err != nil {
		return true, err
	}
	_, err = worker.store.CommitCanonicalReceipt(
		ctx, lease, receipt, worker.config.ActorID, terminalReasonForDecision(receipt.Decision),
	)
	return true, err
}

func (worker *CanonicalWorker) reconcileCleanup(
	ctx context.Context,
	lease VerificationCleanupLease,
) error {
	if lease.Scope != ScopeCanonical || lease.WorkerID != worker.config.WorkerID {
		return fmt.Errorf("%w: invalid Canonical cleanup lease", ErrWorkerLeaseLost)
	}
	cleanupCtx, cancel := verificationCleanupContext(ctx)
	err := worker.materializer.CleanupCanonical(cleanupCtx, lease.Fence)
	cancel()
	persistCtx, persistCancel := verificationCleanupContext(ctx)
	defer persistCancel()
	if err != nil {
		persistErr := worker.store.FailVerificationCleanup(persistCtx, FailVerificationCleanupInput{
			Lease: lease, ActorID: worker.config.ActorID, Reason: boundedExecutionError(err),
		})
		return mapCleanupFailure(fmt.Errorf("reconcile Canonical cleanup: %w", err), persistErr)
	}
	if err := worker.store.CompleteVerificationCleanup(persistCtx, CompleteVerificationCleanupInput{
		Lease: lease, ActorID: worker.config.ActorID,
	}); err != nil {
		return fmt.Errorf("complete Canonical cleanup reconciliation: %w", err)
	}
	return nil
}

func (worker *CanonicalWorker) transition(
	ctx context.Context,
	lease CanonicalExecutionLease,
	target RunState,
) (CanonicalExecutionLease, error) {
	next, err := worker.store.TransitionCanonicalExecution(ctx, lease, worker.config.ActorID, target)
	if err != nil {
		return CanonicalExecutionLease{}, err
	}
	if err := validateCanonicalLeaseContinuation(lease, next, target); err != nil {
		return CanonicalExecutionLease{}, err
	}
	return next, nil
}

func (worker *CanonicalWorker) runWithHeartbeat(
	ctx context.Context,
	lease *CanonicalExecutionLease,
	operation func(context.Context) error,
) error {
	operationCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	completed := make(chan error, 1)
	go func() { completed <- operation(operationCtx) }()
	ticker := time.NewTicker(worker.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case err := <-completed:
			return err
		case <-ctx.Done():
			cancel()
			return awaitVerificationOperationQuiescence(completed, ctx.Err())
		case <-ticker.C:
			next, err := worker.store.HeartbeatCanonicalExecution(
				ctx, *lease, worker.config.ActorID, worker.config.LeaseDuration,
			)
			if err != nil {
				cancel()
				return awaitVerificationOperationQuiescence(
					completed, fmt.Errorf("%w: %v", ErrWorkerLeaseLost, err),
				)
			}
			if err := validateCanonicalLeaseContinuation(*lease, next, lease.State); err != nil {
				cancel()
				return awaitVerificationOperationQuiescence(completed, err)
			}
			*lease = next
		}
	}
}

func (worker *CanonicalWorker) executeChecks(
	ctx context.Context,
	lease *CanonicalExecutionLease,
	spec CanonicalExecutionSpec,
	phaseError string,
) ([]CheckResult, string, error) {
	ordered, err := topologicalPlanChecks(spec.Content.Checks)
	if err != nil {
		return nil, "", err
	}
	results := make([]CheckResult, 0, len(ordered))
	statuses := map[string]CheckStatus{}
	executionError := ""
	for index, check := range ordered {
		var outcome CheckExecutionOutcome
		var outcomeErr error
		startedAt := worker.clock.Now().UTC().Truncate(time.Microsecond)
		switch {
		case phaseError != "":
			outcomeErr = errors.New(phaseError)
		case dependencyDidNotPass(check.DependsOn, statuses):
			outcomeErr = errors.New("one or more dependent checks did not pass")
		default:
			checkCtx, cancel := context.WithTimeout(ctx, time.Duration(check.TimeoutSeconds)*time.Second)
			executionLease := *lease
			outcomeErr = worker.runWithHeartbeat(checkCtx, lease, func(operationCtx context.Context) error {
				var executeErr error
				outcome, executeErr = worker.executor.ExecuteCanonical(operationCtx, CanonicalCheckExecutionRequest{
					ProjectID: executionLease.ProjectID, RunID: executionLease.RunID,
					AttemptID: executionLease.AttemptID, AttemptFenceEpoch: executionLease.AttemptFenceEpoch,
					AttemptCount: uint64(executionLease.AttemptOrdinal),
					Subject:      spec.Content.Subject, RuntimePolicy: cloneRuntimePolicy(spec.Content.RuntimePolicy),
					Dependencies: clonePlanDependencies(spec.Content.Dependencies), Check: clonePlanCheck(check),
				})
				return executeErr
			})
			cancel()
			if isWorkerStop(ctx, outcomeErr) {
				return nil, "", outcomeErr
			}
		}
		completedAt := worker.clock.Now().UTC().Truncate(time.Microsecond)
		if completedAt.Before(startedAt) {
			completedAt = startedAt
		}
		if outcomeErr == nil {
			outcome = failClosedTruncatedOutcome(outcome)
			if err := validateCheckExecutionOutcome(outcome, lease.AttemptID); err != nil {
				outcomeErr = err
			}
		}
		if outcomeErr != nil {
			executionError = appendExecutionError(executionError, check.ID, outcomeErr)
			outcome = CheckExecutionOutcome{Status: CheckError, Diagnostics: []Diagnostic{{
				ID: fmt.Sprintf("canonical-worker-execution-%03d", index), Code: "verification_execution_error",
				Severity: SeverityInfo, Message: boundedExecutionError(outcomeErr),
			}}}
		} else if outcome.Status == CheckError {
			executionError = appendExecutionError(executionError, check.ID, errors.New("executor reported an execution error"))
		}
		result := CheckResult{
			ID: check.ID, Kind: check.Kind, ServiceID: check.ServiceID, CommandID: check.CommandID,
			Required: check.Required, Status: outcome.Status, AttemptID: lease.AttemptID,
			VerifierImageDigest: check.VerifierImageDigest, Argv: append([]string(nil), check.Argv...),
			WorkingDirectory: check.WorkingDirectory, ExitCode: cloneInt(outcome.ExitCode),
			StartedAt: startedAt, CompletedAt: completedAt, DurationMS: completedAt.Sub(startedAt).Milliseconds(),
			AttemptCount: uint64(lease.AttemptOrdinal), Stdout: outcome.Stdout, Stderr: outcome.Stderr,
			Truncated: outcome.Truncated, RedactionCount: outcome.RedactionCount,
			OracleIDs:              append([]string(nil), check.OracleIDs...),
			AcceptanceCriterionIDs: append([]string(nil), check.AcceptanceCriterionIDs...),
			ObligationIDs:          append([]string(nil), check.ObligationIDs...),
			Diagnostics:            append([]Diagnostic(nil), outcome.Diagnostics...),
		}
		results = append(results, result)
		statuses[check.ID] = result.Status
	}
	return results, executionError, nil
}

func (worker *CanonicalWorker) buildReceipt(
	lease CanonicalExecutionLease,
	content CanonicalPlanContent,
	checks []CheckResult,
	artifacts []CanonicalReleaseArtifact,
	executionError string,
) (CanonicalReceipt, error) {
	receiptID := worker.ids.NewID()
	if !validUUIDs(receiptID) {
		return CanonicalReceipt{}, ErrInvalidWorkerConfig
	}
	obligations := make([]ObligationRequirement, len(content.Obligations))
	for index, obligation := range content.Obligations {
		obligations[index] = ObligationRequirement{
			ID: obligation.ID, Level: obligation.Level, OracleIDs: append([]string(nil), obligation.OracleIDs...),
		}
	}
	return NewCanonicalReceipt(NewCanonicalReceiptInput{
		ID: receiptID, RunID: lease.RunID, ProjectID: lease.ProjectID, Subject: content.Subject,
		BuildManifest: content.BuildManifest, BuildContract: content.BuildContract,
		FullStackTemplate: content.FullStackTemplate, Profile: content.Profile, Plan: lease.Plan,
		AttemptIDs: []string{lease.AttemptID}, Checks: checks, Obligations: obligations,
		ReleaseArtifacts: artifacts, ExecutionError: boundedExecutionText(executionError),
		CreatedBy: worker.config.ActorID, CreatedAt: worker.clock.Now().UTC().Truncate(time.Microsecond),
	})
}

func newCanonicalExecutionSpec(
	lease CanonicalExecutionLease,
	plan CompiledCanonicalPlan,
) (CanonicalExecutionSpec, error) {
	payload, err := json.Marshal(plan.Content)
	if err != nil {
		return CanonicalExecutionSpec{}, err
	}
	var cloned CanonicalPlanContent
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return CanonicalExecutionSpec{}, err
	}
	return CanonicalExecutionSpec{
		RunID: lease.RunID, AttemptID: lease.AttemptID, AttemptFenceEpoch: lease.AttemptFenceEpoch,
		PlanID: lease.Plan.ID, PlanHash: lease.Plan.ContentHash, Content: cloned,
	}, nil
}

func (worker *CanonicalWorker) validateLease(lease CanonicalExecutionLease, expected RunState) error {
	if !validUUIDs(lease.ProjectID, lease.RunID, lease.AttemptID, lease.Plan.ID) ||
		!exactSHA256(lease.Plan.ContentHash) || lease.State != expected || lease.RunVersion == 0 ||
		lease.RunFenceEpoch == 0 || lease.AttemptVersion == 0 || lease.AttemptFenceEpoch != lease.RunFenceEpoch ||
		lease.AttemptOrdinal < 1 || lease.WorkerID != worker.config.WorkerID || lease.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("%w: invalid claimed Canonical lease", ErrWorkerLeaseLost)
	}
	return nil
}

func validateCanonicalLeaseContinuation(
	previous, next CanonicalExecutionLease,
	expectedState RunState,
) error {
	if next.ProjectID != previous.ProjectID || next.RunID != previous.RunID || next.AttemptID != previous.AttemptID ||
		next.Plan != previous.Plan || next.AttemptOrdinal != previous.AttemptOrdinal || next.WorkerID != previous.WorkerID ||
		next.RunFenceEpoch != previous.RunFenceEpoch || next.AttemptFenceEpoch != previous.AttemptFenceEpoch ||
		next.RunVersion <= previous.RunVersion || next.AttemptVersion <= previous.AttemptVersion ||
		next.State != expectedState || next.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("%w: Canonical lease continuation drifted", ErrWorkerLeaseLost)
	}
	return nil
}

var _ CanonicalExecutionStore = (*PostgresStore)(nil)
