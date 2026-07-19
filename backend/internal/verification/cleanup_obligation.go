package verification

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

type VerificationCleanupLease struct {
	Scope          Scope
	Fence          VerificationExecutionFence
	Version        uint64
	LeaseEpoch     uint64
	WorkerID       string
	LeaseExpiresAt time.Time
}

type ClaimVerificationCleanupInput struct {
	Scope         Scope
	ActorID       string
	WorkerID      string
	LeaseDuration time.Duration
}

type CompleteVerificationCleanupInput struct {
	Lease   VerificationCleanupLease
	ActorID string
}

type FailVerificationCleanupInput struct {
	Lease   VerificationCleanupLease
	ActorID string
	Reason  string
}

type verificationCleanupRow struct {
	Scope          string    `gorm:"column:scope"`
	ProjectID      string    `gorm:"column:project_id"`
	RunID          string    `gorm:"column:run_id"`
	AttemptID      string    `gorm:"column:attempt_id"`
	AttemptFence   int64     `gorm:"column:attempt_fence_epoch"`
	Version        int64     `gorm:"column:version"`
	LeaseEpoch     int64     `gorm:"column:lease_epoch"`
	WorkerID       string    `gorm:"column:lease_worker_id"`
	LeaseExpiresAt time.Time `gorm:"column:lease_expires_at"`
}

type inactiveVerificationExecutionRow struct {
	ProjectID      string `gorm:"column:project_id"`
	RunID          string `gorm:"column:run_id"`
	RunState       string `gorm:"column:run_state"`
	RunVersion     int64  `gorm:"column:run_version"`
	RunFence       int64  `gorm:"column:run_fence_epoch"`
	AttemptID      string `gorm:"column:attempt_id"`
	AttemptState   string `gorm:"column:attempt_state"`
	AttemptVersion int64  `gorm:"column:attempt_version"`
	AttemptFence   int64  `gorm:"column:attempt_fence_epoch"`
	WorkerID       string `gorm:"column:worker_id"`
}

type inactiveQueuedCanonicalRunRow struct {
	ProjectID  string `gorm:"column:project_id"`
	RunID      string `gorm:"column:run_id"`
	RunVersion int64  `gorm:"column:run_version"`
}

const inactiveQueuedCanonicalReason = "verification profile policy is inactive before Canonical execution claim"

func insertVerificationCleanupObligation(
	transaction *gorm.DB,
	scope Scope,
	projectID, runID, attemptID string,
	attemptFence int64,
	actorID string,
) error {
	if transaction == nil || !validCleanupScope(scope) ||
		!validUUIDs(projectID, runID, attemptID, actorID) || attemptFence < 1 {
		return ErrInvalidWorkerConfig
	}
	result := transaction.Exec(`
INSERT INTO verification_execution_cleanups (
  scope, project_id, run_id, attempt_id, attempt_fence_epoch,
  state, version, lease_epoch, created_by, updated_by
) VALUES (?, ?, ?, ?, ?, 'registered', 1, 0, ?, ?)
`, string(scope), projectID, runID, attemptID, attemptFence, actorID, actorID)
	return result.Error
}

func (store *PostgresStore) ReconcileInactiveVerificationExecution(
	ctx context.Context,
	scope Scope,
	actorID string,
) (bool, error) {
	actorID = strings.TrimSpace(actorID)
	if store == nil || store.database == nil || !validCleanupScope(scope) || !validUUIDs(actorID) {
		return false, ErrInvalidWorkerConfig
	}
	reconciled := false
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if scope == ScopeCanonical {
			var queued inactiveQueuedCanonicalRunRow
			result := transaction.Raw(`
SELECT run.project_id::text AS project_id, run.id::text AS run_id,
       run.version AS run_version
FROM canonical_verification_runs AS run
JOIN canonical_verification_plans AS plan
  ON plan.id = run.plan_id AND plan.plan_hash = run.plan_hash
WHERE run.state = 'queued' AND run.fence_epoch = 0
  AND run.lease_worker_id IS NULL AND run.lease_epoch IS NULL
  AND run.lease_expires_at IS NULL AND run.started_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM canonical_verification_attempts AS attempt
    WHERE attempt.run_id = run.id
  )
  AND NOT EXISTS (
    SELECT 1 FROM verification_execution_cleanups AS cleanup
    WHERE cleanup.scope = 'canonical' AND cleanup.run_id = run.id
  )
  AND NOT EXISTS (
    SELECT 1 FROM verification_profile_policies AS policy
    WHERE policy.profile_id = plan.verification_profile_id
      AND policy.profile_version = plan.verification_profile_version
      AND policy.profile_hash = plan.verification_profile_hash
      AND policy.state = 'active'
  )
ORDER BY run.updated_at, run.id
FOR UPDATE OF run SKIP LOCKED
LIMIT 1
`).Scan(&queued)
			if result.Error != nil {
				return result.Error
			}
			if queued.RunID != "" {
				updated := transaction.Exec(`
UPDATE canonical_verification_runs
SET state = 'cancelled', version = version + 1,
    lease_worker_id = NULL, lease_epoch = NULL, lease_expires_at = NULL,
    terminal_reason = ?, execution_error = NULL,
    finished_at = statement_timestamp(), updated_by = ?
WHERE id = ? AND project_id = ? AND state = 'queued' AND version = ?
  AND fence_epoch = 0 AND lease_worker_id IS NULL AND lease_epoch IS NULL
  AND lease_expires_at IS NULL AND started_at IS NULL
`, inactiveQueuedCanonicalReason, actorID, queued.RunID, queued.ProjectID, queued.RunVersion)
				if updated.Error != nil || updated.RowsAffected != 1 {
					if updated.Error != nil {
						return updated.Error
					}
					return ErrWorkerLeaseLost
				}
				reconciled = true
				return nil
			}
		}

		var current inactiveVerificationExecutionRow
		var query string
		switch scope {
		case ScopeCandidate:
			query = `
SELECT run.project_id::text AS project_id, run.id::text AS run_id,
       run.state AS run_state, run.version AS run_version,
       run.fence_epoch AS run_fence_epoch,
       attempt.id::text AS attempt_id, attempt.state AS attempt_state,
       attempt.version AS attempt_version, attempt.fence_epoch AS attempt_fence_epoch,
       COALESCE(run.lease_worker_id, '') AS worker_id
FROM verification_execution_cleanups AS cleanup
JOIN candidate_verification_attempts AS attempt
  ON attempt.id = cleanup.attempt_id AND attempt.run_id = cleanup.run_id
 AND attempt.project_id = cleanup.project_id
 AND attempt.fence_epoch = cleanup.attempt_fence_epoch
JOIN candidate_verification_runs AS run ON run.id = attempt.run_id
JOIN candidate_verification_plans AS plan ON plan.id = run.plan_id AND plan.plan_hash = run.plan_hash
WHERE cleanup.scope = 'candidate' AND cleanup.state = 'completed'
  AND run.state IN ('claimed','materializing','preparing','running','collecting')
  AND attempt.state = run.state AND run.fence_epoch = attempt.fence_epoch
  AND run.lease_expires_at <= statement_timestamp()
  AND attempt.lease_expires_at <= statement_timestamp()
  AND NOT EXISTS (
    SELECT 1 FROM verification_profile_policies AS policy
    WHERE policy.profile_id = plan.verification_profile_id
      AND policy.profile_version = plan.verification_profile_version
      AND policy.profile_hash = plan.verification_profile_hash
      AND policy.state = 'active'
  )
ORDER BY run.updated_at, run.id
FOR UPDATE OF cleanup, attempt, run SKIP LOCKED
LIMIT 1`
		case ScopeCanonical:
			query = `
SELECT run.project_id::text AS project_id, run.id::text AS run_id,
       run.state AS run_state, run.version AS run_version,
       run.fence_epoch AS run_fence_epoch,
       attempt.id::text AS attempt_id, attempt.state AS attempt_state,
       attempt.version AS attempt_version, attempt.fence_epoch AS attempt_fence_epoch,
       COALESCE(run.lease_worker_id, '') AS worker_id
FROM verification_execution_cleanups AS cleanup
JOIN canonical_verification_attempts AS attempt
  ON attempt.id = cleanup.attempt_id AND attempt.run_id = cleanup.run_id
 AND attempt.project_id = cleanup.project_id
 AND attempt.fence_epoch = cleanup.attempt_fence_epoch
JOIN canonical_verification_runs AS run ON run.id = attempt.run_id
JOIN canonical_verification_plans AS plan ON plan.id = run.plan_id AND plan.plan_hash = run.plan_hash
WHERE cleanup.scope = 'canonical' AND cleanup.state = 'completed'
  AND run.state IN ('claimed','materializing','preparing','running','collecting')
  AND attempt.state = run.state AND run.fence_epoch = attempt.fence_epoch
  AND run.lease_expires_at <= statement_timestamp()
  AND attempt.lease_expires_at <= statement_timestamp()
  AND NOT EXISTS (
    SELECT 1 FROM verification_profile_policies AS policy
    WHERE policy.profile_id = plan.verification_profile_id
      AND policy.profile_version = plan.verification_profile_version
      AND policy.profile_hash = plan.verification_profile_hash
      AND policy.state = 'active'
  )
ORDER BY run.updated_at, run.id
FOR UPDATE OF cleanup, attempt, run SKIP LOCKED
LIMIT 1`
		}
		result := transaction.Raw(query).Scan(&current)
		if result.Error != nil {
			return result.Error
		}
		if current.RunID == "" {
			return nil
		}
		if current.RunFence != current.AttemptFence || current.WorkerID == "" {
			return runIntegrity("inactive verification cleanup reconciliation", nil)
		}
		reason := "verification profile policy is inactive after exact execution cleanup"
		if scope == ScopeCandidate {
			attempt := transaction.Exec(`
UPDATE candidate_verification_attempts
SET state = 'cancelled', version = version + 1, lease_expires_at = NULL,
    terminal_reason = ?, execution_error = NULL,
    finished_at = statement_timestamp(), updated_by = ?
WHERE id = ? AND run_id = ? AND project_id = ? AND state = ?
  AND version = ? AND fence_epoch = ? AND lease_worker_id = ?
  AND lease_epoch = ? AND lease_expires_at <= statement_timestamp()
`, reason, actorID, current.AttemptID, current.RunID, current.ProjectID,
				current.AttemptState, current.AttemptVersion, current.AttemptFence,
				current.WorkerID, current.AttemptFence)
			if attempt.Error != nil || attempt.RowsAffected != 1 {
				if attempt.Error != nil {
					return attempt.Error
				}
				return ErrWorkerLeaseLost
			}
			run := transaction.Exec(`
UPDATE candidate_verification_runs
SET state = 'cancelled', version = version + 1, lease_expires_at = NULL,
    terminal_reason = ?, execution_error = NULL,
    finished_at = statement_timestamp(), updated_by = ?
WHERE id = ? AND project_id = ? AND state = ? AND version = ?
  AND fence_epoch = ? AND lease_worker_id = ? AND lease_epoch = ?
  AND lease_expires_at <= statement_timestamp()
`, reason, actorID, current.RunID, current.ProjectID, current.RunState,
				current.RunVersion, current.RunFence, current.WorkerID, current.RunFence)
			if run.Error != nil || run.RowsAffected != 1 {
				if run.Error != nil {
					return run.Error
				}
				return ErrWorkerLeaseLost
			}
		} else {
			attempt := transaction.Exec(`
UPDATE canonical_verification_attempts
SET state = 'cancelled', version = version + 1,
    lease_worker_id = NULL, lease_epoch = NULL, lease_expires_at = NULL,
    terminal_reason = ?, execution_error = NULL,
    finished_at = statement_timestamp(), updated_by = ?
WHERE id = ? AND run_id = ? AND project_id = ? AND state = ?
  AND version = ? AND fence_epoch = ? AND lease_worker_id = ?
  AND lease_epoch = ? AND lease_expires_at <= statement_timestamp()
`, reason, actorID, current.AttemptID, current.RunID, current.ProjectID,
				current.AttemptState, current.AttemptVersion, current.AttemptFence,
				current.WorkerID, current.AttemptFence)
			if attempt.Error != nil || attempt.RowsAffected != 1 {
				if attempt.Error != nil {
					return attempt.Error
				}
				return ErrWorkerLeaseLost
			}
			run := transaction.Exec(`
UPDATE canonical_verification_runs
SET state = 'cancelled', version = version + 1,
    lease_worker_id = NULL, lease_epoch = NULL, lease_expires_at = NULL,
    terminal_reason = ?, execution_error = NULL,
    finished_at = statement_timestamp(), updated_by = ?
WHERE id = ? AND project_id = ? AND state = ? AND version = ?
  AND fence_epoch = ? AND lease_worker_id = ? AND lease_epoch = ?
  AND lease_expires_at <= statement_timestamp()
`, reason, actorID, current.RunID, current.ProjectID, current.RunState,
				current.RunVersion, current.RunFence, current.WorkerID, current.RunFence)
			if run.Error != nil || run.RowsAffected != 1 {
				if run.Error != nil {
					return run.Error
				}
				return ErrWorkerLeaseLost
			}
		}
		reconciled = true
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return false, mapCandidateWorkerStoreError("reconcile inactive verification execution", err)
	}
	return reconciled, nil
}

func (store *PostgresStore) ClaimVerificationCleanup(
	ctx context.Context,
	input ClaimVerificationCleanupInput,
) (VerificationCleanupLease, bool, error) {
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if store == nil || store.database == nil || !validCleanupScope(input.Scope) ||
		!validUUIDs(input.ActorID) || !workerIDPattern.MatchString(input.WorkerID) ||
		!validCandidateLeaseDuration(input.LeaseDuration) {
		return VerificationCleanupLease{}, false, ErrInvalidWorkerConfig
	}

	var claimed verificationCleanupRow
	found := false
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var current verificationCleanupRow
		result := transaction.Raw(`
SELECT cleanup.scope, cleanup.project_id::text AS project_id,
       cleanup.run_id::text AS run_id, cleanup.attempt_id::text AS attempt_id,
       cleanup.attempt_fence_epoch, cleanup.version, cleanup.lease_epoch
FROM verification_execution_cleanups AS cleanup
WHERE cleanup.scope = ?
  AND (
    (cleanup.state = 'pending' AND cleanup.next_attempt_at <= statement_timestamp())
    OR (cleanup.state = 'cleaning' AND cleanup.lease_expires_at <= statement_timestamp())
    OR (cleanup.state = 'registered' AND (
      (cleanup.scope = 'candidate' AND EXISTS (
        SELECT 1
        FROM candidate_verification_attempts AS attempt
        JOIN candidate_verification_runs AS run ON run.id = attempt.run_id
        WHERE attempt.id = cleanup.attempt_id
          AND attempt.run_id = cleanup.run_id
          AND attempt.project_id = cleanup.project_id
          AND attempt.fence_epoch = cleanup.attempt_fence_epoch
          AND attempt.state IN ('claimed','materializing','preparing','running','collecting')
          AND run.state = attempt.state
          AND attempt.lease_expires_at <= statement_timestamp()
          AND run.lease_expires_at <= statement_timestamp()
      ))
      OR (cleanup.scope = 'canonical' AND EXISTS (
        SELECT 1
        FROM canonical_verification_attempts AS attempt
        JOIN canonical_verification_runs AS run ON run.id = attempt.run_id
        WHERE attempt.id = cleanup.attempt_id
          AND attempt.run_id = cleanup.run_id
          AND attempt.project_id = cleanup.project_id
          AND attempt.fence_epoch = cleanup.attempt_fence_epoch
          AND attempt.state IN ('claimed','materializing','preparing','running','collecting')
          AND run.state = attempt.state
          AND attempt.lease_expires_at <= statement_timestamp()
          AND run.lease_expires_at <= statement_timestamp()
      ))
    ))
  )
ORDER BY CASE cleanup.state WHEN 'pending' THEN 0 WHEN 'cleaning' THEN 1 ELSE 2 END,
         cleanup.created_at, cleanup.attempt_id, cleanup.attempt_fence_epoch
FOR UPDATE OF cleanup SKIP LOCKED
LIMIT 1
`, string(input.Scope)).Scan(&current)
		if result.Error != nil {
			return result.Error
		}
		if current.AttemptID == "" {
			return nil
		}
		found = true
		updated := transaction.Raw(`
UPDATE verification_execution_cleanups
SET state = 'cleaning', version = version + 1, lease_epoch = lease_epoch + 1,
    lease_worker_id = ?, claimed_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() + (? * interval '1 millisecond'),
    next_attempt_at = NULL, last_error = NULL, updated_by = ?
WHERE scope = ? AND attempt_id = ? AND attempt_fence_epoch = ?
  AND version = ? AND lease_epoch = ?
RETURNING scope, project_id::text AS project_id, run_id::text AS run_id,
          attempt_id::text AS attempt_id, attempt_fence_epoch, version, lease_epoch,
          lease_worker_id, lease_expires_at
`, input.WorkerID, input.LeaseDuration.Milliseconds(), input.ActorID,
			current.Scope, current.AttemptID, current.AttemptFence,
			current.Version, current.LeaseEpoch).Scan(&claimed)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrWorkerLeaseLost
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return VerificationCleanupLease{}, false, mapCandidateWorkerStoreError("claim verification cleanup", err)
	}
	if !found {
		return VerificationCleanupLease{}, false, nil
	}
	lease, err := verificationCleanupLeaseFromRow(claimed)
	return lease, err == nil, err
}

func (store *PostgresStore) CompleteVerificationCleanup(
	ctx context.Context,
	input CompleteVerificationCleanupInput,
) error {
	input.ActorID = strings.TrimSpace(input.ActorID)
	if err := validateVerificationCleanupLease(input.Lease, input.ActorID); err != nil {
		return err
	}
	lease := input.Lease
	result := store.database.WithContext(ctx).Exec(`
UPDATE verification_execution_cleanups
SET state = 'completed', version = version + 1,
    lease_worker_id = NULL, claimed_at = NULL, lease_expires_at = NULL,
    next_attempt_at = NULL, last_error = NULL,
    completed_at = statement_timestamp(), updated_by = ?
WHERE scope = ? AND attempt_id = ? AND attempt_fence_epoch = ?
  AND state = 'cleaning' AND version = ? AND lease_epoch = ?
  AND lease_worker_id = ? AND lease_expires_at > statement_timestamp()
`, input.ActorID, string(lease.Scope), lease.Fence.AttemptID,
		int64(lease.Fence.AttemptFenceEpoch), int64(lease.Version), int64(lease.LeaseEpoch),
		lease.WorkerID)
	if result.Error != nil {
		return mapCandidateWorkerStoreError("complete verification cleanup", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrWorkerLeaseLost
	}
	return nil
}

func (store *PostgresStore) FailVerificationCleanup(
	ctx context.Context,
	input FailVerificationCleanupInput,
) error {
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.Reason = boundedExecutionText(input.Reason)
	if err := validateVerificationCleanupLease(input.Lease, input.ActorID); err != nil || input.Reason == "" {
		if err != nil {
			return err
		}
		return ErrInvalidWorkerConfig
	}
	lease := input.Lease
	result := store.database.WithContext(ctx).Exec(`
UPDATE verification_execution_cleanups
SET state = 'pending', version = version + 1,
    lease_worker_id = NULL, claimed_at = NULL, lease_expires_at = NULL,
    next_attempt_at = statement_timestamp() + (
      LEAST(30000, 250 * (1::bigint << LEAST(7, lease_epoch - 1)::integer)) * interval '1 millisecond'
    ),
    last_error = ?, updated_by = ?
WHERE scope = ? AND attempt_id = ? AND attempt_fence_epoch = ?
  AND state = 'cleaning' AND version = ? AND lease_epoch = ?
  AND lease_worker_id = ? AND lease_expires_at > statement_timestamp()
`, input.Reason, input.ActorID, string(lease.Scope), lease.Fence.AttemptID,
		int64(lease.Fence.AttemptFenceEpoch), int64(lease.Version), int64(lease.LeaseEpoch),
		lease.WorkerID)
	if result.Error != nil {
		return mapCandidateWorkerStoreError("fail verification cleanup", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrWorkerLeaseLost
	}
	return nil
}

func (store *PostgresStore) ConfirmVerificationOperationQuiesced(
	ctx context.Context,
	scope Scope,
	fence VerificationExecutionFence,
	workerID, actorID string,
) error {
	workerID = strings.TrimSpace(workerID)
	actorID = strings.TrimSpace(actorID)
	if store == nil || store.database == nil || !validCleanupScope(scope) ||
		validateVerificationExecutionFence(fence) != nil ||
		!workerIDPattern.MatchString(workerID) || !validUUIDs(actorID) {
		return ErrInvalidWorkerConfig
	}
	if scope != ScopeCandidate {
		// Canonical has no external active-execution cancellation endpoint. Its
		// policy reconciliation starts only after the cleanup is completed.
		return nil
	}
	result := store.database.WithContext(ctx).Exec(`
UPDATE verification_execution_cleanups AS cleanup
SET version = version + 1, next_attempt_at = statement_timestamp(), updated_by = ?
WHERE cleanup.scope = 'candidate' AND cleanup.project_id = ? AND cleanup.run_id = ?
  AND cleanup.attempt_id = ? AND cleanup.attempt_fence_epoch = ?
  AND cleanup.state = 'pending' AND cleanup.next_attempt_at > statement_timestamp()
  AND EXISTS (
    SELECT 1
    FROM candidate_verification_attempts AS attempt
    JOIN candidate_verification_runs AS run ON run.id = attempt.run_id
    WHERE attempt.id = cleanup.attempt_id AND attempt.run_id = cleanup.run_id
      AND attempt.project_id = cleanup.project_id
      AND attempt.fence_epoch = cleanup.attempt_fence_epoch
      AND attempt.state = 'cancelled' AND run.state = 'cancelled'
      AND attempt.lease_worker_id = ? AND run.lease_worker_id = ?
  )
`, actorID, fence.ProjectID, fence.RunID, fence.AttemptID,
		int64(fence.AttemptFenceEpoch), workerID, workerID)
	if result.Error != nil {
		return mapCandidateWorkerStoreError("confirm verification operation quiescence", result.Error)
	}
	return nil
}

func (store *PostgresStore) CompleteCandidateExecutionCleanup(
	ctx context.Context,
	lease CandidateExecutionLease,
	actorID string,
) error {
	if err := validateCandidateLeaseInput(lease, strings.TrimSpace(actorID), time.Second); err != nil ||
		lease.State != RunCollecting {
		return ErrInvalidWorkerConfig
	}
	return store.completeLeasedExecutionCleanup(ctx, ScopeCandidate, candidateExecutionFence(lease), actorID, `
SELECT 1
FROM candidate_verification_attempts AS attempt
JOIN candidate_verification_runs AS run ON run.id = attempt.run_id
WHERE attempt.id = ? AND attempt.run_id = ? AND attempt.project_id = ?
  AND attempt.state = 'collecting' AND attempt.version = ? AND attempt.fence_epoch = ?
  AND attempt.lease_worker_id = ? AND attempt.lease_epoch = ?
  AND attempt.lease_expires_at > statement_timestamp()
  AND run.state = 'collecting' AND run.version = ? AND run.fence_epoch = ?
  AND run.lease_worker_id = ? AND run.lease_epoch = ?
  AND run.lease_expires_at > statement_timestamp()
`, lease.AttemptID, lease.RunID, lease.ProjectID, int64(lease.AttemptVersion),
		int64(lease.AttemptFenceEpoch), lease.WorkerID, int64(lease.AttemptFenceEpoch),
		int64(lease.RunVersion), int64(lease.RunFenceEpoch), lease.WorkerID, int64(lease.RunFenceEpoch))
}

func (store *PostgresStore) CompleteCanonicalExecutionCleanup(
	ctx context.Context,
	lease CanonicalExecutionLease,
	actorID string,
) error {
	if err := validateCanonicalLease(lease, strings.TrimSpace(actorID), time.Second); err != nil ||
		lease.State != RunCollecting {
		return ErrInvalidWorkerConfig
	}
	return store.completeLeasedExecutionCleanup(ctx, ScopeCanonical, canonicalExecutionFence(lease), actorID, `
SELECT 1
FROM canonical_verification_attempts AS attempt
JOIN canonical_verification_runs AS run ON run.id = attempt.run_id
WHERE attempt.id = ? AND attempt.run_id = ? AND attempt.project_id = ?
  AND attempt.state = 'collecting' AND attempt.version = ? AND attempt.fence_epoch = ?
  AND attempt.lease_worker_id = ? AND attempt.lease_epoch = ?
  AND attempt.lease_expires_at > statement_timestamp()
  AND run.state = 'collecting' AND run.version = ? AND run.fence_epoch = ?
  AND run.lease_worker_id = ? AND run.lease_epoch = ?
  AND run.lease_expires_at > statement_timestamp()
`, lease.AttemptID, lease.RunID, lease.ProjectID, int64(lease.AttemptVersion),
		int64(lease.AttemptFenceEpoch), lease.WorkerID, int64(lease.AttemptFenceEpoch),
		int64(lease.RunVersion), int64(lease.RunFenceEpoch), lease.WorkerID, int64(lease.RunFenceEpoch))
}

func (store *PostgresStore) completeLeasedExecutionCleanup(
	ctx context.Context,
	scope Scope,
	fence VerificationExecutionFence,
	actorID string,
	leaseQuery string,
	leaseArgs ...any,
) error {
	result := store.database.WithContext(ctx).Exec(`
UPDATE verification_execution_cleanups AS cleanup
SET state = 'completed', version = version + 1,
    completed_at = statement_timestamp(), updated_by = ?
WHERE cleanup.scope = ? AND cleanup.attempt_id = ?
  AND cleanup.attempt_fence_epoch = ? AND cleanup.state = 'registered'
  AND EXISTS (`+leaseQuery+`)
`, append([]any{actorID, string(scope), fence.AttemptID, int64(fence.AttemptFenceEpoch)}, leaseArgs...)...)
	if result.Error != nil {
		return mapCandidateWorkerStoreError("complete leased execution cleanup", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrWorkerLeaseLost
	}
	return nil
}

func verificationCleanupLeaseFromRow(row verificationCleanupRow) (VerificationCleanupLease, error) {
	scope := Scope(row.Scope)
	fence := VerificationExecutionFence{
		ProjectID: row.ProjectID, RunID: row.RunID, AttemptID: row.AttemptID,
		AttemptFenceEpoch: uint64(row.AttemptFence),
	}
	if !validCleanupScope(scope) || validateVerificationExecutionFence(fence) != nil ||
		row.Version < 1 || row.LeaseEpoch < 1 || !workerIDPattern.MatchString(row.WorkerID) ||
		row.LeaseExpiresAt.IsZero() {
		return VerificationCleanupLease{}, runIntegrity("verification cleanup lease projection", nil)
	}
	return VerificationCleanupLease{
		Scope: scope, Fence: fence, Version: uint64(row.Version), LeaseEpoch: uint64(row.LeaseEpoch),
		WorkerID: row.WorkerID, LeaseExpiresAt: row.LeaseExpiresAt.UTC(),
	}, nil
}

func validateVerificationCleanupLease(lease VerificationCleanupLease, actorID string) error {
	if !validCleanupScope(lease.Scope) || validateVerificationExecutionFence(lease.Fence) != nil ||
		!validUUIDs(strings.TrimSpace(actorID)) || lease.Version == 0 || lease.LeaseEpoch == 0 ||
		!workerIDPattern.MatchString(lease.WorkerID) || lease.LeaseExpiresAt.IsZero() {
		return ErrInvalidWorkerConfig
	}
	return nil
}

func validCleanupScope(scope Scope) bool {
	return scope == ScopeCandidate || scope == ScopeCanonical
}

func mapCleanupFailure(primary, persistence error) error {
	if persistence == nil {
		return primary
	}
	return errors.Join(primary, fmt.Errorf("persist cleanup retry: %w", persistence))
}
