package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrInvalidWorkerConfig = errors.New("invalid candidate verification worker configuration")
	ErrWorkerLeaseLost     = errors.New("candidate verification worker lease was lost")

	workerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,159}$`)
)

const VerificationWorkerActorID = "00000000-0000-4000-8000-000000000042"

type CandidateExecutionLease struct {
	ProjectID         string
	RunID             string
	AttemptID         string
	Plan              PlanReference
	AttemptOrdinal    int
	State             RunState
	RunVersion        uint64
	RunFenceEpoch     uint64
	AttemptVersion    uint64
	AttemptFenceEpoch uint64
	WorkerID          string
	LeaseExpiresAt    time.Time
}

type ClaimCandidateExecutionInput struct {
	AttemptID     string
	ActorID       string
	WorkerID      string
	LeaseDuration time.Duration
}

type HeartbeatCandidateExecutionInput struct {
	Lease         CandidateExecutionLease
	ActorID       string
	LeaseDuration time.Duration
}

type TransitionCandidateExecutionInput struct {
	Lease   CandidateExecutionLease
	ActorID string
	Target  RunState
}

// CommitCandidateReceiptInput must be committed atomically by the store:
// terminalize the exact Attempt, insert the immutable Receipt, then terminalize
// the Run under the same version/fence predicates.
type CommitCandidateReceiptInput struct {
	Lease          CandidateExecutionLease
	Receipt        Receipt
	TerminalReason string
}

type CandidateExecutionStore interface {
	ReconcileInactiveVerificationExecution(context.Context, Scope, string) (bool, error)
	ClaimVerificationCleanup(context.Context, ClaimVerificationCleanupInput) (VerificationCleanupLease, bool, error)
	CompleteVerificationCleanup(context.Context, CompleteVerificationCleanupInput) error
	FailVerificationCleanup(context.Context, FailVerificationCleanupInput) error
	ConfirmVerificationOperationQuiesced(context.Context, Scope, VerificationExecutionFence, string, string) error
	ClaimCandidateExecution(context.Context, ClaimCandidateExecutionInput) (CandidateExecutionLease, bool, error)
	HeartbeatCandidateExecution(context.Context, HeartbeatCandidateExecutionInput) (CandidateExecutionLease, error)
	TransitionCandidateExecution(context.Context, TransitionCandidateExecutionInput) (CandidateExecutionLease, error)
	GetExecutionPlan(context.Context, string, string) (Plan, error)
	CompleteCandidateExecutionCleanup(context.Context, CandidateExecutionLease, string) error
	CommitCandidateReceipt(context.Context, CommitCandidateReceiptInput) (Receipt, error)
}

type CandidateExecutionSpec struct {
	RunID             string
	AttemptID         string
	AttemptFenceEpoch uint64
	PlanID            string
	PlanHash          string
	Content           PlanContent
}

// CandidateMaterializer resolves only immutable, server-owned Plan inputs.
// Implementations must never take browser-supplied images, argv, or paths.
type CandidateMaterializer interface {
	Materialize(context.Context, CandidateExecutionSpec) error
	Prepare(context.Context, CandidateExecutionSpec) error
	Collect(context.Context, CandidateExecutionSpec) error
	CleanupCandidate(context.Context, VerificationExecutionFence) error
}

type CheckExecutionRequest struct {
	ProjectID         string
	RunID             string
	AttemptID         string
	AttemptFenceEpoch uint64
	AttemptCount      uint64
	Subject           CandidatePlanSubject
	RuntimePolicy     PlanRuntimePolicy
	Dependencies      []PlanDependency
	Check             PlanCheck
}

// CheckExecutionOutcome contains runtime facts only. Check identity, image,
// argv, working directory and trace links are copied from the immutable Plan.
type CheckExecutionOutcome struct {
	Status         CheckStatus
	ExitCode       *int
	Stdout         *BlobReference
	Stderr         *BlobReference
	Truncated      bool
	RedactionCount int
	Diagnostics    []Diagnostic
}

type CandidateCheckExecutor interface {
	Execute(context.Context, CheckExecutionRequest) (CheckExecutionOutcome, error)
}

type CandidateWorkerClock interface {
	Now() time.Time
}

type CandidateWorkerIDGenerator interface {
	NewID() string
}

type CandidateWorkerConfig struct {
	ActorID           string
	WorkerID          string
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
}

type CandidateWorker struct {
	store        CandidateExecutionStore
	materializer CandidateMaterializer
	executor     CandidateCheckExecutor
	config       CandidateWorkerConfig
	clock        CandidateWorkerClock
	ids          CandidateWorkerIDGenerator
}

type systemCandidateWorkerClock struct{}

func (systemCandidateWorkerClock) Now() time.Time { return time.Now().UTC() }

type uuidCandidateWorkerIDs struct{}

func (uuidCandidateWorkerIDs) NewID() string { return uuid.NewString() }

func NewCandidateWorker(
	store CandidateExecutionStore,
	materializer CandidateMaterializer,
	executor CandidateCheckExecutor,
	config CandidateWorkerConfig,
	clock CandidateWorkerClock,
	ids CandidateWorkerIDGenerator,
) (*CandidateWorker, error) {
	if store == nil || materializer == nil || executor == nil ||
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
	return &CandidateWorker{
		store: store, materializer: materializer, executor: executor,
		config: config, clock: clock, ids: ids,
	}, nil
}

func (worker *CandidateWorker) RunOnce(ctx context.Context) (processed bool, runErr error) {
	if worker == nil || ctx == nil {
		return false, ErrInvalidWorkerConfig
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if reconciled, err := worker.store.ReconcileInactiveVerificationExecution(
		ctx, ScopeCandidate, worker.config.ActorID,
	); err != nil || reconciled {
		return reconciled, err
	}
	cleanup, found, err := worker.store.ClaimVerificationCleanup(ctx, ClaimVerificationCleanupInput{
		Scope: ScopeCandidate, ActorID: worker.config.ActorID, WorkerID: worker.config.WorkerID,
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
		return false, fmt.Errorf("%w: generated Attempt ID", ErrInvalidWorkerConfig)
	}
	lease, found, err := worker.store.ClaimCandidateExecution(ctx, ClaimCandidateExecutionInput{
		AttemptID: attemptID, ActorID: worker.config.ActorID,
		WorkerID: worker.config.WorkerID, LeaseDuration: worker.config.LeaseDuration,
	})
	if err != nil || !found {
		return found, err
	}
	if err := worker.validateLease(lease, RunClaimed); err != nil {
		return true, err
	}
	fence := candidateExecutionFence(lease)
	cleanupConfirmed := false
	defer func() {
		if cleanupConfirmed {
			return
		}
		if !errors.Is(runErr, ErrVerificationOperationNotQuiesced) {
			quiescedCtx, quiescedCancel := verificationCleanupContext(ctx)
			_ = worker.store.ConfirmVerificationOperationQuiesced(
				quiescedCtx, ScopeCandidate, fence, worker.config.WorkerID, worker.config.ActorID,
			)
			quiescedCancel()
		}
		cleanupCtx, cancel := verificationCleanupContext(ctx)
		defer cancel()
		if err := worker.materializer.CleanupCandidate(cleanupCtx, fence); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("candidate execution cleanup: %w", err))
		}
	}()
	plan, err := worker.store.GetExecutionPlan(ctx, lease.ProjectID, lease.Plan.ID)
	if err != nil {
		return true, err
	}
	parsed, err := ParsePlan(plan.Content, plan.PlanHash)
	if err != nil || plan.ID != lease.Plan.ID || plan.PlanHash != lease.Plan.ContentHash ||
		parsed.Content.ProjectID != lease.ProjectID {
		return true, fmt.Errorf("%w: claimed immutable Plan drifted", ErrInvalidPlan)
	}
	spec, err := newCandidateExecutionSpec(lease, parsed)
	if err != nil {
		return true, err
	}

	lease, err = worker.transition(ctx, lease, RunMaterializing)
	if err != nil {
		return true, err
	}
	var executionError string
	if err := worker.runWithHeartbeat(ctx, &lease, func(operationCtx context.Context) error {
		return worker.materializer.Materialize(operationCtx, spec)
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
			return worker.materializer.Prepare(operationCtx, spec)
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
	if err := worker.runWithHeartbeat(ctx, &lease, func(operationCtx context.Context) error {
		return worker.materializer.Collect(operationCtx, spec)
	}); err != nil {
		if isWorkerStop(ctx, err) {
			return true, err
		}
		// Cleanup is infrastructure reconciliation, not a quality result. Keep
		// the Run/Attempt nonterminal so lease takeover can retry the exact fence;
		// never seal a Receipt while runtime resources may still be live.
		return true, fmt.Errorf("collect Candidate execution resources: %w", err)
	}
	confirmationCtx, cancelConfirmation := context.WithTimeout(ctx, verificationCleanupTimeout)
	confirmationErr := worker.runWithHeartbeat(confirmationCtx, &lease, func(operationCtx context.Context) error {
		return worker.materializer.CleanupCandidate(operationCtx, fence)
	})
	cancelConfirmation()
	if confirmationErr != nil {
		if isWorkerStop(ctx, confirmationErr) {
			return true, confirmationErr
		}
		return true, fmt.Errorf("confirm Candidate execution cleanup: %w", confirmationErr)
	}
	if err := worker.store.CompleteCandidateExecutionCleanup(ctx, lease, worker.config.ActorID); err != nil {
		return true, fmt.Errorf("complete Candidate cleanup obligation: %w", err)
	}
	cleanupConfirmed = true

	receipt, err := worker.buildReceipt(lease, parsed.Content, checks, executionError)
	if err != nil {
		return true, err
	}
	_, err = worker.store.CommitCandidateReceipt(ctx, CommitCandidateReceiptInput{
		Lease: lease, Receipt: receipt, TerminalReason: terminalReasonForDecision(receipt.Decision),
	})
	return true, err
}

func (worker *CandidateWorker) reconcileCleanup(
	ctx context.Context,
	lease VerificationCleanupLease,
) error {
	if lease.Scope != ScopeCandidate || lease.WorkerID != worker.config.WorkerID {
		return fmt.Errorf("%w: invalid Candidate cleanup lease", ErrWorkerLeaseLost)
	}
	cleanupCtx, cancel := verificationCleanupContext(ctx)
	err := worker.materializer.CleanupCandidate(cleanupCtx, lease.Fence)
	cancel()
	persistCtx, persistCancel := verificationCleanupContext(ctx)
	defer persistCancel()
	if err != nil {
		persistErr := worker.store.FailVerificationCleanup(persistCtx, FailVerificationCleanupInput{
			Lease: lease, ActorID: worker.config.ActorID, Reason: boundedExecutionError(err),
		})
		return mapCleanupFailure(fmt.Errorf("reconcile Candidate cleanup: %w", err), persistErr)
	}
	if err := worker.store.CompleteVerificationCleanup(persistCtx, CompleteVerificationCleanupInput{
		Lease: lease, ActorID: worker.config.ActorID,
	}); err != nil {
		return fmt.Errorf("complete Candidate cleanup reconciliation: %w", err)
	}
	return nil
}

func (worker *CandidateWorker) transition(
	ctx context.Context,
	lease CandidateExecutionLease,
	target RunState,
) (CandidateExecutionLease, error) {
	next, err := worker.store.TransitionCandidateExecution(ctx, TransitionCandidateExecutionInput{
		Lease: lease, ActorID: worker.config.ActorID, Target: target,
	})
	if err != nil {
		return CandidateExecutionLease{}, err
	}
	if err := validateLeaseContinuation(lease, next, target); err != nil {
		return CandidateExecutionLease{}, err
	}
	return next, nil
}

func (worker *CandidateWorker) runWithHeartbeat(
	ctx context.Context,
	lease *CandidateExecutionLease,
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
			next, err := worker.store.HeartbeatCandidateExecution(ctx, HeartbeatCandidateExecutionInput{
				Lease: *lease, ActorID: worker.config.ActorID,
				LeaseDuration: worker.config.LeaseDuration,
			})
			if err != nil {
				cancel()
				return awaitVerificationOperationQuiescence(
					completed, fmt.Errorf("%w: %v", ErrWorkerLeaseLost, err),
				)
			}
			if err := validateLeaseContinuation(*lease, next, lease.State); err != nil {
				cancel()
				return awaitVerificationOperationQuiescence(completed, err)
			}
			*lease = next
		}
	}
}

func (worker *CandidateWorker) executeChecks(
	ctx context.Context,
	lease *CandidateExecutionLease,
	spec CandidateExecutionSpec,
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
				outcome, executeErr = worker.executor.Execute(operationCtx, CheckExecutionRequest{
					ProjectID: executionLease.ProjectID, RunID: executionLease.RunID,
					AttemptID: executionLease.AttemptID, AttemptFenceEpoch: executionLease.AttemptFenceEpoch,
					AttemptCount: uint64(executionLease.AttemptOrdinal), Subject: spec.Content.Subject,
					RuntimePolicy: cloneRuntimePolicy(spec.Content.RuntimePolicy),
					Dependencies:  clonePlanDependencies(spec.Content.Dependencies), Check: clonePlanCheck(check),
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
			outcome = CheckExecutionOutcome{
				Status: CheckError,
				Diagnostics: []Diagnostic{{
					ID:   fmt.Sprintf("worker-execution-%03d", index),
					Code: "verification_execution_error", Severity: SeverityInfo,
					Message: boundedExecutionError(outcomeErr),
				}},
			}
		} else if outcome.Status == CheckError {
			executionError = appendExecutionError(
				executionError, check.ID, errors.New("executor reported an execution error"),
			)
		}
		result := CheckResult{
			ID: check.ID, Kind: check.Kind, ServiceID: check.ServiceID, CommandID: check.CommandID,
			Required: check.Required, Status: outcome.Status, AttemptID: lease.AttemptID,
			VerifierImageDigest: check.VerifierImageDigest, Argv: append([]string(nil), check.Argv...),
			WorkingDirectory: check.WorkingDirectory, ExitCode: cloneInt(outcome.ExitCode),
			StartedAt: startedAt, CompletedAt: completedAt,
			DurationMS: completedAt.Sub(startedAt).Milliseconds(), AttemptCount: uint64(lease.AttemptOrdinal),
			Stdout: outcome.Stdout, Stderr: outcome.Stderr, Truncated: outcome.Truncated,
			RedactionCount: outcome.RedactionCount, OracleIDs: append([]string(nil), check.OracleIDs...),
			AcceptanceCriterionIDs: append([]string(nil), check.AcceptanceCriterionIDs...),
			ObligationIDs:          append([]string(nil), check.ObligationIDs...),
			Diagnostics:            append([]Diagnostic(nil), outcome.Diagnostics...),
		}
		results = append(results, result)
		statuses[check.ID] = result.Status
	}
	return results, executionError, nil
}

func (worker *CandidateWorker) buildReceipt(
	lease CandidateExecutionLease,
	content PlanContent,
	checks []CheckResult,
	executionError string,
) (Receipt, error) {
	receiptID := worker.ids.NewID()
	if !validUUIDs(receiptID) {
		return Receipt{}, fmt.Errorf("%w: generated Receipt ID", ErrInvalidWorkerConfig)
	}
	obligations := make([]ObligationRequirement, len(content.Obligations))
	for index, obligation := range content.Obligations {
		obligations[index] = ObligationRequirement{
			ID: obligation.ID, Level: obligation.Level,
			OracleIDs: append([]string(nil), obligation.OracleIDs...),
		}
	}
	return NewCandidateReceipt(NewCandidateReceiptInput{
		ID: receiptID, RunID: lease.RunID, ProjectID: lease.ProjectID,
		Subject: CandidateSubject{
			SessionID: content.Subject.SessionID, CandidateID: content.Subject.CandidateID,
			CandidateSnapshotID: content.Subject.CandidateSnapshotID,
			CandidateVersion:    content.Subject.CandidateVersion,
			JournalSequence:     content.Subject.JournalSequence, SessionEpoch: content.Subject.SessionEpoch,
			WriterLeaseEpoch: content.Subject.WriterLeaseEpoch, TreeHash: content.Subject.TreeHash,
		},
		BuildManifest: content.BuildManifest, BuildContract: content.BuildContract,
		FullStackTemplate: content.FullStackTemplate, Profile: content.Profile, Plan: lease.Plan,
		AttemptIDs: []string{lease.AttemptID}, Checks: checks, Obligations: obligations,
		ExecutionError: boundedExecutionText(executionError), CreatedBy: worker.config.ActorID,
		CreatedAt: worker.clock.Now().UTC().Truncate(time.Microsecond),
	})
}

func (worker *CandidateWorker) validateLease(lease CandidateExecutionLease, expected RunState) error {
	if !validUUIDs(lease.ProjectID, lease.RunID, lease.AttemptID, lease.Plan.ID) ||
		!exactSHA256(lease.Plan.ContentHash) || lease.State != expected || lease.RunVersion == 0 ||
		lease.RunFenceEpoch == 0 || lease.AttemptVersion == 0 || lease.AttemptFenceEpoch == 0 ||
		lease.AttemptOrdinal < 1 || lease.WorkerID != worker.config.WorkerID || lease.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("%w: invalid claimed lease", ErrWorkerLeaseLost)
	}
	return nil
}

func validateLeaseContinuation(
	previous CandidateExecutionLease,
	next CandidateExecutionLease,
	expectedState RunState,
) error {
	if next.ProjectID != previous.ProjectID || next.RunID != previous.RunID ||
		next.AttemptID != previous.AttemptID || next.Plan != previous.Plan ||
		next.AttemptOrdinal != previous.AttemptOrdinal || next.WorkerID != previous.WorkerID ||
		next.RunFenceEpoch != previous.RunFenceEpoch || next.AttemptFenceEpoch != previous.AttemptFenceEpoch ||
		next.RunVersion <= previous.RunVersion || next.AttemptVersion <= previous.AttemptVersion ||
		next.State != expectedState || next.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("%w: lease continuation drifted", ErrWorkerLeaseLost)
	}
	return nil
}

func newCandidateExecutionSpec(lease CandidateExecutionLease, plan CompiledPlan) (CandidateExecutionSpec, error) {
	payload, err := json.Marshal(plan.Content)
	if err != nil {
		return CandidateExecutionSpec{}, err
	}
	var cloned PlanContent
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return CandidateExecutionSpec{}, err
	}
	return CandidateExecutionSpec{
		RunID: lease.RunID, AttemptID: lease.AttemptID, AttemptFenceEpoch: lease.AttemptFenceEpoch,
		PlanID: lease.Plan.ID, PlanHash: lease.Plan.ContentHash, Content: cloned,
	}, nil
}

func clonePlanCheck(check PlanCheck) PlanCheck {
	check.Argv = append([]string(nil), check.Argv...)
	check.OracleIDs = append([]string(nil), check.OracleIDs...)
	check.AcceptanceCriterionIDs = append([]string(nil), check.AcceptanceCriterionIDs...)
	check.ObligationIDs = append([]string(nil), check.ObligationIDs...)
	check.DependsOn = append([]string(nil), check.DependsOn...)
	return check
}

func cloneRuntimePolicy(policy PlanRuntimePolicy) PlanRuntimePolicy {
	payload, _ := json.Marshal(policy)
	var result PlanRuntimePolicy
	_ = json.Unmarshal(payload, &result)
	return result
}

func clonePlanDependencies(values []PlanDependency) []PlanDependency {
	result := make([]PlanDependency, len(values))
	for index, value := range values {
		value.ManifestPaths = append([]string(nil), value.ManifestPaths...)
		value.Lockfiles = append([]PlanDependencyLock(nil), value.Lockfiles...)
		value.ResolverArgv = append([]string(nil), value.ResolverArgv...)
		result[index] = value
	}
	return result
}

func validateCheckExecutionOutcome(outcome CheckExecutionOutcome, attemptID string) error {
	if outcome.RedactionCount < 0 ||
		(outcome.Status != CheckPassed && outcome.Status != CheckFailed && outcome.Status != CheckError) {
		return errors.New("executor returned an invalid status or redaction count")
	}
	if outcome.Status == CheckPassed && (outcome.ExitCode == nil || *outcome.ExitCode != 0) {
		return errors.New("passing executor result requires exit code zero")
	}
	if outcome.Status == CheckPassed && outcome.Truncated {
		return errors.New("truncated executor evidence cannot pass")
	}
	if outcome.Status == CheckFailed && outcome.ExitCode == nil {
		return errors.New("failed executor result requires an exit code")
	}
	if outcome.Status == CheckError && outcome.ExitCode != nil {
		return errors.New("errored executor result cannot report an exit code")
	}
	if err := validateBlob(outcome.Stdout, attemptID); err != nil {
		return err
	}
	if err := validateBlob(outcome.Stderr, attemptID); err != nil {
		return err
	}
	if _, err := normalizeDiagnostics(outcome.Diagnostics); err != nil {
		return err
	}
	return nil
}

func failClosedTruncatedOutcome(outcome CheckExecutionOutcome) CheckExecutionOutcome {
	if !outcome.Truncated {
		return outcome
	}
	diagnostics := make([]Diagnostic, 0, len(outcome.Diagnostics)+1)
	for _, diagnostic := range outcome.Diagnostics {
		if diagnostic.ID == "verification-output-truncated" || diagnostic.Code == "output_truncated" {
			continue
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	diagnostics = append(diagnostics, Diagnostic{
		ID:       "verification-output-truncated",
		Code:     "output_truncated",
		Severity: SeverityBlocker,
		Message:  "Command output exceeded the platform capture limit; discarded output is untrusted, so this check cannot pass.",
	})
	outcome.Status = CheckError
	outcome.ExitCode = nil
	outcome.Diagnostics = diagnostics
	return outcome
}

func topologicalPlanChecks(checks []PlanCheck) ([]PlanCheck, error) {
	byID := make(map[string]PlanCheck, len(checks))
	remaining := make(map[string]int, len(checks))
	dependents := map[string][]string{}
	for _, check := range checks {
		byID[check.ID] = check
		remaining[check.ID] = len(check.DependsOn)
		for _, dependency := range check.DependsOn {
			dependents[dependency] = append(dependents[dependency], check.ID)
		}
	}
	ready := []string{}
	for id, count := range remaining {
		if count == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	result := make([]PlanCheck, 0, len(checks))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		result = append(result, byID[id])
		for _, dependent := range dependents[id] {
			remaining[dependent]--
			if remaining[dependent] == 0 {
				ready = append(ready, dependent)
				sort.Strings(ready)
			}
		}
	}
	if len(result) != len(checks) {
		return nil, planInvalid("cyclic check DAG")
	}
	return result, nil
}

func dependencyDidNotPass(dependencies []string, statuses map[string]CheckStatus) bool {
	for _, dependency := range dependencies {
		if statuses[dependency] != CheckPassed {
			return true
		}
	}
	return false
}

func terminalReasonForDecision(decision Decision) string {
	switch decision {
	case DecisionPassed:
		return ""
	case DecisionFailed:
		return "required verification checks or obligations failed"
	default:
		return "candidate verification execution failed"
	}
}

func isWorkerStop(ctx context.Context, err error) bool {
	return err != nil && (errors.Is(err, ErrWorkerLeaseLost) || ctx.Err() != nil || errors.Is(err, context.Canceled))
}

func appendExecutionError(current, phase string, err error) string {
	if err == nil {
		return current
	}
	next := phase + ": " + boundedExecutionError(err)
	if current != "" {
		next = current + "; " + next
	}
	return boundedExecutionText(next)
}

func boundedExecutionError(err error) string {
	if err == nil {
		return ""
	}
	return boundedExecutionText(err.Error())
}

func boundedExecutionText(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\x00", "invalid-byte"))
	if len(value) <= 2000 {
		return value
	}
	value = value[:2000]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
