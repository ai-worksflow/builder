package verification

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type runRow struct {
	ID             string         `gorm:"column:id"`
	SchemaVersion  string         `gorm:"column:schema_version"`
	ProjectID      string         `gorm:"column:project_id"`
	PlanID         string         `gorm:"column:plan_id"`
	PlanHash       string         `gorm:"column:plan_hash"`
	RequestKey     string         `gorm:"column:request_key"`
	RequestHash    string         `gorm:"column:request_hash"`
	Reason         string         `gorm:"column:reason"`
	ParentRunID    sql.NullString `gorm:"column:parent_run_id"`
	RetryReason    sql.NullString `gorm:"column:retry_reason"`
	State          string         `gorm:"column:state"`
	Version        int64          `gorm:"column:version"`
	FenceEpoch     int64          `gorm:"column:fence_epoch"`
	LeaseWorkerID  sql.NullString `gorm:"column:lease_worker_id"`
	LeaseEpoch     sql.NullInt64  `gorm:"column:lease_epoch"`
	LeaseExpiresAt sql.NullTime   `gorm:"column:lease_expires_at"`
	TerminalReason sql.NullString `gorm:"column:terminal_reason"`
	ExecutionError sql.NullString `gorm:"column:execution_error"`
	StartedAt      sql.NullTime   `gorm:"column:started_at"`
	FinishedAt     sql.NullTime   `gorm:"column:finished_at"`
	CreatedBy      string         `gorm:"column:created_by"`
	UpdatedBy      string         `gorm:"column:updated_by"`
	CreatedAt      time.Time      `gorm:"column:created_at"`
	UpdatedAt      time.Time      `gorm:"column:updated_at"`
}

func (runRow) TableName() string { return "candidate_verification_runs" }

type runAttemptRow struct {
	ID              string         `gorm:"column:id"`
	RunID           string         `gorm:"column:run_id"`
	Ordinal         int            `gorm:"column:ordinal"`
	ParentAttemptID sql.NullString `gorm:"column:parent_attempt_id"`
	RetryReason     sql.NullString `gorm:"column:retry_reason"`
	State           string         `gorm:"column:state"`
	Version         int64          `gorm:"column:version"`
	FenceEpoch      int64          `gorm:"column:fence_epoch"`
	LeaseExpiresAt  sql.NullTime   `gorm:"column:lease_expires_at"`
	TerminalReason  sql.NullString `gorm:"column:terminal_reason"`
	ExecutionError  sql.NullString `gorm:"column:execution_error"`
	StartedAt       sql.NullTime   `gorm:"column:started_at"`
	FinishedAt      sql.NullTime   `gorm:"column:finished_at"`
	CreatedAt       time.Time      `gorm:"column:created_at"`
	UpdatedAt       time.Time      `gorm:"column:updated_at"`
}

func (runAttemptRow) TableName() string { return "candidate_verification_attempts" }

func (store *PostgresStore) CreateRun(ctx context.Context, input CreateRunInput) (Run, error) {
	if err := ctx.Err(); err != nil {
		return Run{}, err
	}
	normalized, err := normalizeCreateRunInput(input, true)
	if err != nil {
		return Run{}, err
	}
	var persisted runRow
	replayed := false
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Exec(`
INSERT INTO candidate_verification_runs (
  id, schema_version, project_id, plan_id, plan_hash,
  request_key, request_hash, reason, parent_run_id, retry_reason,
  state, version, fence_epoch, created_by, updated_by
) VALUES (
  ?, 'candidate-verification-run/v1', ?, ?, ?, ?, ?, ?, NULLIF(?, '')::uuid, NULLIF(?, ''),
  'queued', 1, 0, ?, ?
)
ON CONFLICT DO NOTHING
`, normalized.ID, normalized.ProjectID, normalized.Plan.ID, normalized.Plan.ContentHash,
			normalized.RequestKey, normalized.RequestHash, normalized.Reason,
			normalized.ParentRunID, normalized.RetryReason, normalized.CreatedBy, normalized.CreatedBy)
		if result.Error != nil {
			return result.Error
		}
		replayed = result.RowsAffected == 0
		queryErr := transaction.Where(
			"project_id = ? AND request_key = ?", normalized.ProjectID, normalized.RequestKey,
		).Take(&persisted).Error
		if errors.Is(queryErr, gorm.ErrRecordNotFound) && replayed {
			return ErrRunIdempotencyConflict
		}
		if queryErr != nil {
			return queryErr
		}
		if !runRowMatchesCreate(persisted, normalized) {
			return ErrRunIdempotencyConflict
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Run{}, mapRunStoreError("create", err)
	}
	run, err := runFromRow(persisted)
	if err != nil {
		return Run{}, err
	}
	run.Replayed = replayed
	return run, nil
}

func (store *PostgresStore) GetRun(ctx context.Context, projectID, runID string) (Run, error) {
	if !validUUIDs(projectID, runID) {
		return Run{}, runInvalid("project or Run identity")
	}
	var row runRow
	err := store.database.WithContext(ctx).
		Where("id = ? AND project_id = ?", runID, projectID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Run{}, ErrRunNotFound
	}
	if err != nil {
		return Run{}, mapRunStoreError("load", err)
	}
	return runFromRow(row)
}

func (store *PostgresStore) FindRunByRequest(
	ctx context.Context,
	projectID, requestKey string,
) (Run, bool, error) {
	requestKey = strings.TrimSpace(requestKey)
	if !validUUIDs(projectID) || requestKey == "" || len(requestKey) > 128 ||
		strings.ContainsRune(requestKey, '\x00') {
		return Run{}, false, runInvalid("project identity or request key")
	}
	var row runRow
	err := store.database.WithContext(ctx).
		Where("project_id = ? AND request_key = ?", projectID, requestKey).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, mapRunStoreError("find by request", err)
	}
	run, err := runFromRow(row)
	return run, err == nil, err
}

func (store *PostgresStore) ResolveRunProject(ctx context.Context, runID string) (string, error) {
	if !validUUIDs(runID) {
		return "", runInvalid("Run identity")
	}
	var row struct {
		ProjectID string `gorm:"column:project_id"`
	}
	err := store.database.WithContext(ctx).Raw(`
SELECT project_id::text AS project_id
FROM candidate_verification_runs
WHERE id = ?
`, runID).Scan(&row).Error
	if err != nil {
		return "", mapRunStoreError("resolve project", err)
	}
	if row.ProjectID == "" {
		return "", ErrRunNotFound
	}
	return row.ProjectID, nil
}

func (store *PostgresStore) GetRunView(ctx context.Context, projectID, runID string) (RunView, error) {
	run, err := store.GetRun(ctx, projectID, runID)
	if err != nil {
		return RunView{}, err
	}
	plan, err := store.GetPlan(ctx, projectID, run.Plan.ID)
	if err != nil || plan.PlanHash != run.Plan.ContentHash {
		return RunView{}, runIntegrity("Run Plan", err)
	}

	attemptCount, latestAttempt, err := store.loadRunAttemptSummary(ctx, run.ID)
	if err != nil {
		return RunView{}, err
	}
	var receipt *Receipt
	if run.State == RunPassed || run.State == RunFailed || run.State == RunError {
		value, receiptErr := store.GetReceiptByRun(ctx, run.ID)
		if receiptErr == nil {
			receipt = &value
		} else if !errors.Is(receiptErr, ErrReceiptNotFound) {
			return RunView{}, receiptErr
		}
	}
	fresh, err := store.candidatePlanIsCurrent(ctx, plan)
	if err != nil {
		return RunView{}, err
	}
	return buildRunView(run, plan, latestAttempt, attemptCount, receipt, fresh), nil
}

func (store *PostgresStore) CancelRun(ctx context.Context, input CancelRunInput) (RunView, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	if !validUUIDs(input.ProjectID, input.RunID, input.ActorID) || input.ExpectedVersion == 0 ||
		input.Reason == "" || len(input.Reason) > 2000 || strings.ContainsRune(input.Reason, '\x00') {
		return RunView{}, runInvalid("cancel precondition or reason")
	}
	var cancelled runRow
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var current runRow
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND project_id = ?", input.RunID, input.ProjectID).
			Take(&current).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrRunNotFound
		} else if err != nil {
			return err
		}
		if current.Version != int64(input.ExpectedVersion) || current.FenceEpoch != int64(input.ExpectedFenceEpoch) {
			return ErrRunVersionConflict
		}
		if runStateIsTerminal(RunState(current.State)) {
			return ErrRunTransition
		}
		var activeAttempts []runAttemptRow
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("run_id = ? AND state NOT IN ?", input.RunID, terminalRunStates()).
			Order("ordinal ASC").Find(&activeAttempts).Error; err != nil {
			return err
		}
		if len(activeAttempts) > 1 {
			return runIntegrity("multiple active Attempts", nil)
		}
		for _, attempt := range activeAttempts {
			result := transaction.Exec(`
UPDATE candidate_verification_attempts
SET state = 'cancelled', version = version + 1,
    terminal_reason = ?, execution_error = NULL,
    lease_expires_at = NULL,
    started_at = COALESCE(started_at, statement_timestamp()),
    finished_at = statement_timestamp(), updated_by = ?
WHERE id = ? AND run_id = ? AND version = ? AND fence_epoch = ?
  AND state NOT IN ('passed', 'failed', 'error', 'cancelled', 'timed_out')
`, input.Reason, input.ActorID, attempt.ID, input.RunID, attempt.Version, attempt.FenceEpoch)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrRunVersionConflict
			}
		}
		result := transaction.Exec(`
UPDATE candidate_verification_runs
SET state = 'cancelled', version = version + 1,
    terminal_reason = ?, execution_error = NULL,
    lease_expires_at = NULL,
    started_at = COALESCE(started_at, statement_timestamp()),
    finished_at = statement_timestamp(), updated_by = ?
WHERE id = ? AND project_id = ? AND version = ? AND fence_epoch = ?
  AND state NOT IN ('passed', 'failed', 'error', 'cancelled', 'timed_out')
`, input.Reason, input.ActorID, input.RunID, input.ProjectID,
			int64(input.ExpectedVersion), int64(input.ExpectedFenceEpoch))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRunVersionConflict
		}
		for _, attempt := range activeAttempts {
			if !attempt.LeaseExpiresAt.Valid {
				return runIntegrity("active Attempt cleanup activation lease", nil)
			}
			activation := transaction.Exec(`
UPDATE verification_execution_cleanups
SET state = 'pending', version = version + 1,
    next_attempt_at = GREATEST(
      statement_timestamp(), ?::timestamptz + interval '15 seconds'
    ), updated_by = ?
WHERE scope = 'candidate' AND attempt_id = ? AND attempt_fence_epoch = ?
  AND run_id = ? AND project_id = ? AND state = 'registered'
`, attempt.LeaseExpiresAt.Time, input.ActorID, attempt.ID, attempt.FenceEpoch, input.RunID, input.ProjectID)
			if activation.Error != nil {
				return activation.Error
			}
			var cleanup struct {
				State string `gorm:"column:state"`
			}
			if err := transaction.Raw(`
SELECT state
FROM verification_execution_cleanups
WHERE scope = 'candidate' AND attempt_id = ? AND attempt_fence_epoch = ?
  AND run_id = ? AND project_id = ?
`, attempt.ID, attempt.FenceEpoch, input.RunID, input.ProjectID).Scan(&cleanup).Error; err != nil {
				return err
			}
			if cleanup.State != "pending" && cleanup.State != "cleaning" && cleanup.State != "completed" {
				return runIntegrity("cancelled active Attempt cleanup obligation", nil)
			}
		}
		return transaction.Where("id = ?", input.RunID).Take(&cancelled).Error
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return RunView{}, mapRunStoreError("cancel", err)
	}
	return store.GetRunView(ctx, input.ProjectID, cancelled.ID)
}

func (store *PostgresStore) loadRunAttemptSummary(
	ctx context.Context,
	runID string,
) (int, *AttemptSummary, error) {
	var count int64
	if err := store.database.WithContext(ctx).Model(&runAttemptRow{}).
		Where("run_id = ?", runID).Count(&count).Error; err != nil {
		return 0, nil, mapRunStoreError("count Attempts", err)
	}
	if count == 0 {
		return 0, nil, nil
	}
	var row runAttemptRow
	if err := store.database.WithContext(ctx).Where("run_id = ?", runID).
		Order("ordinal DESC").Take(&row).Error; err != nil {
		return 0, nil, mapRunStoreError("load latest Attempt", err)
	}
	value, err := attemptSummaryFromRow(row)
	if err != nil {
		return 0, nil, err
	}
	return int(count), &value, nil
}

func (store *PostgresStore) candidatePlanIsCurrent(ctx context.Context, plan Plan) (bool, error) {
	var result struct {
		Fresh bool `gorm:"column:fresh"`
	}
	err := store.database.WithContext(ctx).Raw(`
SELECT EXISTS (
  SELECT 1
  FROM candidate_verification_plans AS exact_plan
  JOIN candidate_workspaces AS candidate
    ON candidate.id = exact_plan.candidate_id
   AND candidate.project_id = exact_plan.project_id
  JOIN candidate_snapshots AS snapshot
    ON snapshot.id = exact_plan.candidate_snapshot_id
   AND snapshot.project_id = exact_plan.project_id
   AND snapshot.candidate_id = exact_plan.candidate_id
  JOIN sandbox_sessions AS session
    ON session.id = exact_plan.sandbox_session_id
   AND session.project_id = exact_plan.project_id
   AND session.candidate_id = exact_plan.candidate_id
  WHERE exact_plan.id = ? AND exact_plan.plan_hash = ?
    AND candidate.status = 'active'
    AND NOT candidate.conflicted AND NOT candidate.stale AND NOT candidate.rebase_required
    AND candidate.version = exact_plan.candidate_version
    AND candidate.journal_sequence = exact_plan.journal_sequence
    AND candidate.session_epoch = exact_plan.session_epoch
    AND candidate.writer_lease_epoch = exact_plan.writer_lease_epoch
    AND candidate.current_tree_store = exact_plan.tree_store
    AND candidate.current_tree_owner_id = exact_plan.tree_owner_id
    AND candidate.current_tree_ref = exact_plan.tree_ref
    AND candidate.current_tree_content_hash = exact_plan.tree_content_hash
    AND candidate.current_tree_hash = exact_plan.tree_hash
    AND candidate.build_manifest_id = exact_plan.build_manifest_id
    AND verification_normalize_sha256(candidate.build_manifest_hash) = exact_plan.build_manifest_hash
    AND candidate.build_contract_id = exact_plan.build_contract_id
    AND verification_normalize_sha256(candidate.build_contract_hash) = exact_plan.build_contract_hash
    AND candidate.full_stack_template_id = exact_plan.full_stack_template_id
    AND candidate.full_stack_template_hash = exact_plan.full_stack_template_hash
    AND snapshot.candidate_version = exact_plan.candidate_version
    AND snapshot.journal_sequence = exact_plan.journal_sequence
    AND snapshot.session_epoch = exact_plan.session_epoch
    AND snapshot.writer_lease_epoch = exact_plan.writer_lease_epoch
    AND snapshot.tree_store = exact_plan.tree_store
    AND snapshot.tree_owner_id = exact_plan.tree_owner_id
    AND snapshot.tree_ref = exact_plan.tree_ref
    AND snapshot.tree_content_hash = exact_plan.tree_content_hash
    AND snapshot.tree_hash = exact_plan.tree_hash
    AND session.state = 'ready' AND session.version = exact_plan.session_version
    AND session.session_epoch = exact_plan.session_epoch
    AND session.latest_checkpoint_id = exact_plan.candidate_snapshot_id
    AND session.candidate_version = exact_plan.candidate_version
    AND session.candidate_journal_sequence = exact_plan.journal_sequence
    AND session.candidate_session_epoch = exact_plan.session_epoch
    AND session.candidate_writer_lease_epoch = exact_plan.writer_lease_epoch
    AND session.candidate_tree_store = exact_plan.tree_store
    AND session.candidate_tree_owner_id = exact_plan.tree_owner_id
    AND session.candidate_tree_ref = exact_plan.tree_ref
    AND session.candidate_tree_content_hash = exact_plan.tree_content_hash
    AND session.candidate_tree_hash = exact_plan.tree_hash
    AND session.build_manifest_id = exact_plan.build_manifest_id
    AND verification_normalize_sha256(session.build_manifest_hash) = exact_plan.build_manifest_hash
    AND session.build_contract_id = exact_plan.build_contract_id
    AND verification_normalize_sha256(session.build_contract_hash) = exact_plan.build_contract_hash
    AND session.full_stack_template_id = exact_plan.full_stack_template_id
    AND session.full_stack_template_hash = exact_plan.full_stack_template_hash
) AS fresh
`, plan.ID, plan.PlanHash).Scan(&result).Error
	if err != nil {
		return false, mapRunStoreError("check current Candidate subject", err)
	}
	return result.Fresh, nil
}

func buildRunView(
	run Run,
	plan Plan,
	latestAttempt *AttemptSummary,
	attemptCount int,
	receipt *Receipt,
	fresh bool,
) RunView {
	view := RunView{
		Run: run, Subject: plan.Content.Subject,
		BuildManifest: plan.Content.BuildManifest, BuildContract: plan.Content.BuildContract,
		FullStackTemplate: plan.Content.FullStackTemplate, Profile: plan.Content.Profile,
		CheckCount: len(plan.Content.Checks), AttemptCount: attemptCount,
		LatestAttempt: latestAttempt, Stale: !fresh,
		AllowedActions: []RunAction{}, BlockingReasons: []RunBlockingReason{},
	}
	for _, check := range plan.Content.Checks {
		if check.Required {
			view.RequiredCheckCount++
		}
	}
	if receipt != nil {
		reference := repository.ExactReference{ID: receipt.ID, ContentHash: receipt.PayloadHash}
		decision := receipt.Decision
		view.Receipt, view.ReceiptDecision = &reference, &decision
		view.CompletedCheckCount = len(receipt.Checks)
		view.MustCount, view.MustPassedCount = receipt.MustCount, receipt.MustPassedCount
		view.BlockerCount, view.WarningCount = receipt.BlockerCount, receipt.WarningCount
	}
	view.AllowedActions, view.BlockingReasons = deriveRunActions(run, receipt, fresh)
	return view
}

func deriveRunActions(run Run, receipt *Receipt, fresh bool) ([]RunAction, []RunBlockingReason) {
	actions := []RunAction{}
	reasons := []RunBlockingReason{}
	freeze := []RunAction{RunActionFreeze}
	if !runStateIsTerminal(run.State) {
		actions = append(actions, RunActionCancel)
		reasons = append(reasons, RunBlockingReason{
			Code: RunBlockingInProgress, Actions: freeze,
			Detail: "Verification is still running for the exact Candidate checkpoint.",
		})
		return actions, reasons
	}
	if receipt != nil {
		actions = append(actions, RunActionViewReceipt)
	}
	switch run.State {
	case RunPassed:
		if receipt == nil || receipt.Decision != DecisionPassed {
			reasons = append(reasons, RunBlockingReason{
				Code: RunBlockingReceiptMissing, Actions: freeze,
				Detail: "The passing Run does not have its exact finalized VerificationReceipt.",
			})
		} else if !fresh {
			source := repository.ExactReference{ID: run.Plan.ID, ContentHash: run.Plan.ContentHash}
			reasons = append(reasons, RunBlockingReason{
				Code: RunBlockingCandidateChanged, Actions: freeze,
				Detail: "The Candidate or checkpoint advanced after this VerificationPlan was created.", SourceRef: &source,
			})
		} else {
			actions = append(actions, RunActionFreeze)
		}
	case RunFailed:
		actions = append(actions, RunActionRetry)
		reasons = append(reasons, RunBlockingReason{
			Code: RunBlockingFailed, Actions: freeze,
			Detail: "One or more required verification checks failed.",
		})
	case RunError:
		actions = append(actions, RunActionRetry)
		reasons = append(reasons, RunBlockingReason{
			Code: RunBlockingExecutionError, Actions: freeze,
			Detail: "The quality runner could not form a trusted verification decision.",
		})
	case RunCancelled:
		actions = append(actions, RunActionRetry)
		reasons = append(reasons, RunBlockingReason{
			Code: RunBlockingCancelled, Actions: freeze,
			Detail: "Verification was cancelled before a passing Receipt was produced.",
		})
	case RunTimedOut:
		actions = append(actions, RunActionRetry)
		reasons = append(reasons, RunBlockingReason{
			Code: RunBlockingTimedOut, Actions: freeze,
			Detail: "Verification timed out before a passing Receipt was produced.",
		})
	}
	return actions, reasons
}

func runRowMatchesCreate(row runRow, input CreateRunInput) bool {
	return row.SchemaVersion == RunSchemaVersion && row.ProjectID == input.ProjectID &&
		row.PlanID == input.Plan.ID && row.PlanHash == input.Plan.ContentHash &&
		row.RequestKey == input.RequestKey && row.RequestHash == input.RequestHash &&
		row.Reason == input.Reason && nullString(row.ParentRunID) == input.ParentRunID &&
		nullString(row.RetryReason) == input.RetryReason && row.CreatedBy == input.CreatedBy
}

func runFromRow(row runRow) (Run, error) {
	state := RunState(row.State)
	if row.SchemaVersion != RunSchemaVersion || !validUUIDs(row.ID, row.ProjectID, row.PlanID, row.CreatedBy, row.UpdatedBy) ||
		!exactSHA256(row.PlanHash) || row.RequestKey == "" || row.RequestHash == "" ||
		!exactSHA256(row.RequestHash) || row.Reason == "" || !validRunState(state) ||
		row.Version <= 0 || row.FenceEpoch < 0 || row.CreatedAt.IsZero() || row.UpdatedAt.Before(row.CreatedAt) {
		return Run{}, runIntegrity("Run row projection", nil)
	}
	if row.ParentRunID.Valid && !validUUIDs(row.ParentRunID.String) {
		return Run{}, runIntegrity("Run parent projection", nil)
	}
	return Run{
		SchemaVersion: row.SchemaVersion, ID: row.ID, ProjectID: row.ProjectID,
		Plan:       PlanReference{ID: row.PlanID, ContentHash: row.PlanHash},
		RequestKey: row.RequestKey, RequestHash: row.RequestHash, Reason: row.Reason,
		ParentRunID: nullString(row.ParentRunID), RetryReason: nullString(row.RetryReason),
		State: state, Version: uint64(row.Version), FenceEpoch: uint64(row.FenceEpoch),
		LeaseWorkerID: nullString(row.LeaseWorkerID), LeaseEpoch: nullableUint64(row.LeaseEpoch),
		LeaseExpiresAt: nullableTime(row.LeaseExpiresAt), TerminalReason: nullString(row.TerminalReason),
		ExecutionError: nullString(row.ExecutionError), StartedAt: nullableTime(row.StartedAt),
		FinishedAt: nullableTime(row.FinishedAt), CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy,
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}, nil
}

func attemptSummaryFromRow(row runAttemptRow) (AttemptSummary, error) {
	state := RunState(row.State)
	if !validUUIDs(row.ID, row.RunID) || row.Ordinal < 1 || row.Ordinal > 64 ||
		!validRunState(state) || row.Version <= 0 || row.FenceEpoch < 0 ||
		row.CreatedAt.IsZero() || row.UpdatedAt.Before(row.CreatedAt) {
		return AttemptSummary{}, runIntegrity("Attempt summary projection", nil)
	}
	return AttemptSummary{
		ID: row.ID, Ordinal: row.Ordinal, ParentAttemptID: nullString(row.ParentAttemptID),
		RetryReason: nullString(row.RetryReason), State: state, Version: uint64(row.Version),
		FenceEpoch: uint64(row.FenceEpoch), TerminalReason: nullString(row.TerminalReason),
		ExecutionError: nullString(row.ExecutionError), StartedAt: nullableTime(row.StartedAt),
		FinishedAt: nullableTime(row.FinishedAt), CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}, nil
}

func terminalRunStates() []string {
	return []string{string(RunPassed), string(RunFailed), string(RunError), string(RunCancelled), string(RunTimedOut)}
}

func nullableUint64(value sql.NullInt64) uint64 {
	if value.Valid && value.Int64 > 0 {
		return uint64(value.Int64)
	}
	return 0
}

func nullableTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}

type runStoreError struct {
	operation string
	kind      error
	cause     error
}

func (err *runStoreError) Error() string {
	return fmt.Sprintf("candidate verification run persistence %s: %v: %v", err.operation, err.kind, err.cause)
}

func (err *runStoreError) Unwrap() []error { return []error{err.kind, err.cause} }

func runIntegrity(detail string, cause error) error {
	if cause == nil {
		cause = errors.New(detail)
	}
	return &runStoreError{operation: detail, kind: ErrRunStoreIntegrity, cause: cause}
}

func mapRunStoreError(operation string, err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var persisted *runStoreError
	if errors.As(err, &persisted) {
		return err
	}
	for _, known := range []error{
		ErrInvalidRun, ErrRunNotFound, ErrRunIdempotencyConflict,
		ErrRunVersionConflict, ErrRunTransition, ErrRunStoreIntegrity,
	} {
		if errors.Is(err, known) {
			return err
		}
	}
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		switch {
		case postgres.Code == "40001" || postgres.Code == "40P01":
			return &runStoreError{operation: operation, kind: ErrRunVersionConflict, cause: err}
		case postgres.Code == "23514" || postgres.Code == "23503" || postgres.Code == "55000":
			return &runStoreError{operation: operation, kind: ErrRunTransition, cause: err}
		case postgres.Code == "23505":
			return &runStoreError{operation: operation, kind: ErrRunIdempotencyConflict, cause: err}
		case strings.HasPrefix(postgres.Code, "08") || postgres.Code == "57P01" ||
			postgres.Code == "57P02" || postgres.Code == "57P03":
			return &runStoreError{operation: operation, kind: ErrRunStoreDown, cause: err}
		default:
			return &runStoreError{operation: operation, kind: ErrRunStoreIntegrity, cause: err}
		}
	}
	return &runStoreError{operation: operation, kind: ErrRunStoreDown, cause: err}
}
