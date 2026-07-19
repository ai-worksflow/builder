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

type candidateExecutionRow struct {
	ProjectID         string    `gorm:"column:project_id"`
	RunID             string    `gorm:"column:run_id"`
	PlanID            string    `gorm:"column:plan_id"`
	PlanHash          string    `gorm:"column:plan_hash"`
	RunState          string    `gorm:"column:run_state"`
	RunVersion        int64     `gorm:"column:run_version"`
	RunFenceEpoch     int64     `gorm:"column:run_fence_epoch"`
	AttemptID         string    `gorm:"column:attempt_id"`
	AttemptOrdinal    int       `gorm:"column:attempt_ordinal"`
	AttemptVersion    int64     `gorm:"column:attempt_version"`
	AttemptFenceEpoch int64     `gorm:"column:attempt_fence_epoch"`
	WorkerID          string    `gorm:"column:worker_id"`
	LeaseExpiresAt    time.Time `gorm:"column:lease_expires_at"`
}

type candidateExecutionRunRow struct {
	ProjectID     string `gorm:"column:project_id"`
	RunID         string `gorm:"column:run_id"`
	PlanID        string `gorm:"column:plan_id"`
	PlanHash      string `gorm:"column:plan_hash"`
	State         string `gorm:"column:state"`
	Version       int64  `gorm:"column:version"`
	FenceEpoch    int64  `gorm:"column:fence_epoch"`
	LeaseWorkerID string `gorm:"column:lease_worker_id"`
}

type candidateExecutionAttemptRow struct {
	ID         string `gorm:"column:id"`
	Ordinal    int    `gorm:"column:ordinal"`
	State      string `gorm:"column:state"`
	Version    int64  `gorm:"column:version"`
	FenceEpoch int64  `gorm:"column:fence_epoch"`
}

func (store *PostgresStore) ClaimCandidateExecution(
	ctx context.Context,
	input ClaimCandidateExecutionInput,
) (CandidateExecutionLease, bool, error) {
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if store == nil || store.database == nil || !validUUIDs(input.AttemptID, input.ActorID) ||
		!workerIDPattern.MatchString(input.WorkerID) || !validCandidateLeaseDuration(input.LeaseDuration) {
		return CandidateExecutionLease{}, false, ErrInvalidWorkerConfig
	}
	var claimed candidateExecutionRow
	found := false
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var run candidateExecutionRunRow
		result := transaction.Raw(`
SELECT run.project_id::text AS project_id, run.id::text AS run_id,
       run.plan_id::text AS plan_id, run.plan_hash,
       run.state, run.version, run.fence_epoch,
       COALESCE(run.lease_worker_id, '') AS lease_worker_id
FROM candidate_verification_runs AS run
JOIN candidate_verification_plans AS plan
  ON plan.id = run.plan_id AND plan.plan_hash = run.plan_hash
JOIN verification_profile_policies AS policy
  ON policy.profile_id = plan.verification_profile_id
 AND policy.profile_version = plan.verification_profile_version
 AND policy.profile_hash = plan.verification_profile_hash
 AND policy.state = 'active'
WHERE run.state = 'queued'
   OR (run.state IN ('claimed', 'materializing', 'preparing', 'running', 'collecting')
       AND run.lease_expires_at <= statement_timestamp()
       AND EXISTS (
         SELECT 1
         FROM candidate_verification_attempts AS active_attempt
         JOIN verification_execution_cleanups AS cleanup
           ON cleanup.scope = 'candidate'
          AND cleanup.attempt_id = active_attempt.id
          AND cleanup.attempt_fence_epoch = active_attempt.fence_epoch
          AND cleanup.state = 'completed'
         WHERE active_attempt.run_id = run.id
           AND active_attempt.state = run.state
       ))
ORDER BY CASE WHEN run.state = 'queued' THEN 0 ELSE 1 END,
         run.created_at ASC, run.id ASC
FOR UPDATE OF run SKIP LOCKED
LIMIT 1
`).Scan(&run)
		if result.Error != nil {
			return result.Error
		}
		if run.RunID == "" {
			return nil
		}
		found = true
		leaseMilliseconds := input.LeaseDuration.Milliseconds()
		if run.State == string(RunQueued) {
			var runUpdate candidateExecutionRow
			updated := transaction.Raw(`
UPDATE candidate_verification_runs
SET state = 'claimed', version = version + 1, fence_epoch = fence_epoch + 1,
    lease_worker_id = ?, lease_epoch = fence_epoch + 1,
    lease_expires_at = statement_timestamp() + (? * interval '1 millisecond'),
    started_at = statement_timestamp(), updated_by = ?
WHERE id = ? AND project_id = ? AND state = 'queued'
  AND version = ? AND fence_epoch = ?
RETURNING version AS run_version, fence_epoch AS run_fence_epoch,
          lease_expires_at
`, input.WorkerID, leaseMilliseconds, input.ActorID, run.RunID, run.ProjectID,
				run.Version, run.FenceEpoch).Scan(&runUpdate)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrWorkerLeaseLost
			}
			if err := transaction.Exec(`
INSERT INTO candidate_verification_attempts (
  id, schema_version, run_id, project_id, plan_id, plan_hash,
  ordinal, state, version, fence_epoch, created_by, updated_by
) VALUES (
  ?, 'candidate-verification-attempt/v1', ?, ?, ?, ?,
  1, 'queued', 1, 0, ?, ?
)
`, input.AttemptID, run.RunID, run.ProjectID, run.PlanID, run.PlanHash,
				input.ActorID, input.ActorID).Error; err != nil {
				return err
			}
			var attemptUpdate candidateExecutionRow
			attempt := transaction.Raw(`
UPDATE candidate_verification_attempts
SET state = 'claimed', version = version + 1, fence_epoch = fence_epoch + 1,
    lease_worker_id = ?, lease_epoch = fence_epoch + 1,
    lease_expires_at = statement_timestamp() + (? * interval '1 millisecond'),
    started_at = statement_timestamp(), updated_by = ?
WHERE id = ? AND run_id = ? AND state = 'queued' AND version = 1 AND fence_epoch = 0
RETURNING id::text AS attempt_id, ordinal AS attempt_ordinal,
          version AS attempt_version, fence_epoch AS attempt_fence_epoch,
          lease_expires_at
`, input.WorkerID, leaseMilliseconds, input.ActorID, input.AttemptID, run.RunID).Scan(&attemptUpdate)
			if attempt.Error != nil {
				return attempt.Error
			}
			if attempt.RowsAffected != 1 {
				return ErrWorkerLeaseLost
			}
			claimed.RunVersion = runUpdate.RunVersion
			claimed.RunFenceEpoch = runUpdate.RunFenceEpoch
			claimed.AttemptID = attemptUpdate.AttemptID
			claimed.AttemptOrdinal = attemptUpdate.AttemptOrdinal
			claimed.AttemptVersion = attemptUpdate.AttemptVersion
			claimed.AttemptFenceEpoch = attemptUpdate.AttemptFenceEpoch
			claimed.LeaseExpiresAt = earlierTime(runUpdate.LeaseExpiresAt, attemptUpdate.LeaseExpiresAt)
			if err := insertVerificationCleanupObligation(
				transaction, ScopeCandidate, run.ProjectID, run.RunID, attemptUpdate.AttemptID,
				attemptUpdate.AttemptFenceEpoch, input.ActorID,
			); err != nil {
				return err
			}
		} else {
			var attempt candidateExecutionAttemptRow
			result := transaction.Raw(`
SELECT id::text AS id, ordinal, state, version, fence_epoch
FROM candidate_verification_attempts
WHERE run_id = ?
  AND state IN ('claimed', 'materializing', 'preparing', 'running', 'collecting')
FOR UPDATE
`, run.RunID).Scan(&attempt)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 || attempt.ID == "" || attempt.State != run.State {
				return runIntegrity("expired Run active Attempt", nil)
			}
			var runUpdate candidateExecutionRow
			updated := transaction.Raw(`
UPDATE candidate_verification_runs
SET state = 'claimed', version = version + 1, fence_epoch = fence_epoch + 1,
    lease_worker_id = ?, lease_epoch = fence_epoch + 1,
    lease_expires_at = statement_timestamp() + (? * interval '1 millisecond'),
    updated_by = ?
WHERE id = ? AND project_id = ? AND state = ?
  AND version = ? AND fence_epoch = ?
  AND lease_expires_at <= statement_timestamp()
RETURNING version AS run_version, fence_epoch AS run_fence_epoch,
          lease_expires_at
`, input.WorkerID, leaseMilliseconds, input.ActorID, run.RunID, run.ProjectID,
				run.State, run.Version, run.FenceEpoch).Scan(&runUpdate)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrWorkerLeaseLost
			}
			var attemptUpdate candidateExecutionRow
			reclaimedAttempt := transaction.Raw(`
UPDATE candidate_verification_attempts
SET state = 'claimed', version = version + 1, fence_epoch = fence_epoch + 1,
    lease_worker_id = ?, lease_epoch = fence_epoch + 1,
    lease_expires_at = statement_timestamp() + (? * interval '1 millisecond'),
    updated_by = ?
WHERE id = ? AND run_id = ? AND state = ?
  AND version = ? AND fence_epoch = ?
  AND lease_expires_at <= statement_timestamp()
RETURNING id::text AS attempt_id, ordinal AS attempt_ordinal,
          version AS attempt_version, fence_epoch AS attempt_fence_epoch,
          lease_expires_at
`, input.WorkerID, leaseMilliseconds, input.ActorID, attempt.ID, run.RunID,
				attempt.State, attempt.Version, attempt.FenceEpoch).Scan(&attemptUpdate)
			if reclaimedAttempt.Error != nil {
				return reclaimedAttempt.Error
			}
			if reclaimedAttempt.RowsAffected != 1 {
				return ErrWorkerLeaseLost
			}
			claimed.RunVersion = runUpdate.RunVersion
			claimed.RunFenceEpoch = runUpdate.RunFenceEpoch
			claimed.AttemptID = attemptUpdate.AttemptID
			claimed.AttemptOrdinal = attemptUpdate.AttemptOrdinal
			claimed.AttemptVersion = attemptUpdate.AttemptVersion
			claimed.AttemptFenceEpoch = attemptUpdate.AttemptFenceEpoch
			claimed.LeaseExpiresAt = earlierTime(runUpdate.LeaseExpiresAt, attemptUpdate.LeaseExpiresAt)
			if err := insertVerificationCleanupObligation(
				transaction, ScopeCandidate, run.ProjectID, run.RunID, attemptUpdate.AttemptID,
				attemptUpdate.AttemptFenceEpoch, input.ActorID,
			); err != nil {
				return err
			}
		}
		claimed.ProjectID = run.ProjectID
		claimed.RunID = run.RunID
		claimed.PlanID = run.PlanID
		claimed.PlanHash = run.PlanHash
		claimed.RunState = string(RunClaimed)
		claimed.WorkerID = input.WorkerID
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CandidateExecutionLease{}, false, mapCandidateWorkerStoreError("claim", err)
	}
	if !found {
		return CandidateExecutionLease{}, false, nil
	}
	lease, err := candidateLeaseFromRow(claimed)
	return lease, err == nil, err
}

func (store *PostgresStore) HeartbeatCandidateExecution(
	ctx context.Context,
	input HeartbeatCandidateExecutionInput,
) (CandidateExecutionLease, error) {
	input.ActorID = strings.TrimSpace(input.ActorID)
	if err := validateCandidateLeaseInput(input.Lease, input.ActorID, input.LeaseDuration); err != nil {
		return CandidateExecutionLease{}, err
	}
	lease := input.Lease
	var runUpdate, attemptUpdate candidateExecutionRow
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		milliseconds := input.LeaseDuration.Milliseconds()
		run := transaction.Raw(`
UPDATE candidate_verification_runs
SET version = version + 1,
    lease_expires_at = statement_timestamp() + (? * interval '1 millisecond'),
    updated_by = ?
WHERE id = ? AND project_id = ? AND state = ? AND version = ? AND fence_epoch = ?
  AND lease_worker_id = ? AND lease_epoch = ?
  AND lease_expires_at > statement_timestamp()
RETURNING version AS run_version, fence_epoch AS run_fence_epoch, lease_expires_at
`, milliseconds, input.ActorID, lease.RunID, lease.ProjectID, string(lease.State),
			int64(lease.RunVersion), int64(lease.RunFenceEpoch), lease.WorkerID,
			int64(lease.RunFenceEpoch)).Scan(&runUpdate)
		if run.Error != nil {
			return run.Error
		}
		if run.RowsAffected != 1 {
			return ErrWorkerLeaseLost
		}
		attempt := transaction.Raw(`
UPDATE candidate_verification_attempts
SET version = version + 1,
    lease_expires_at = statement_timestamp() + (? * interval '1 millisecond'),
    updated_by = ?
WHERE id = ? AND run_id = ? AND project_id = ? AND state = ?
  AND version = ? AND fence_epoch = ? AND lease_worker_id = ? AND lease_epoch = ?
  AND lease_expires_at > statement_timestamp()
RETURNING version AS attempt_version, fence_epoch AS attempt_fence_epoch, lease_expires_at
`, milliseconds, input.ActorID, lease.AttemptID, lease.RunID, lease.ProjectID,
			string(lease.State), int64(lease.AttemptVersion), int64(lease.AttemptFenceEpoch),
			lease.WorkerID, int64(lease.AttemptFenceEpoch)).Scan(&attemptUpdate)
		if attempt.Error != nil {
			return attempt.Error
		}
		if attempt.RowsAffected != 1 {
			return ErrWorkerLeaseLost
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CandidateExecutionLease{}, mapCandidateWorkerStoreError("heartbeat", err)
	}
	lease.RunVersion = uint64(runUpdate.RunVersion)
	lease.RunFenceEpoch = uint64(runUpdate.RunFenceEpoch)
	lease.AttemptVersion = uint64(attemptUpdate.AttemptVersion)
	lease.AttemptFenceEpoch = uint64(attemptUpdate.AttemptFenceEpoch)
	lease.LeaseExpiresAt = earlierTime(runUpdate.LeaseExpiresAt, attemptUpdate.LeaseExpiresAt)
	return lease, nil
}

func (store *PostgresStore) TransitionCandidateExecution(
	ctx context.Context,
	input TransitionCandidateExecutionInput,
) (CandidateExecutionLease, error) {
	input.ActorID = strings.TrimSpace(input.ActorID)
	if err := validateCandidateLeaseInput(input.Lease, input.ActorID, time.Second); err != nil ||
		!candidateWorkerTransition(input.Lease.State, input.Target) {
		if err != nil {
			return CandidateExecutionLease{}, err
		}
		return CandidateExecutionLease{}, ErrInvalidWorkerConfig
	}
	lease := input.Lease
	var runVersion, attemptVersion struct {
		Version int64 `gorm:"column:version"`
	}
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		run := transaction.Raw(`
UPDATE candidate_verification_runs
SET state = ?, version = version + 1, updated_by = ?
WHERE id = ? AND project_id = ? AND state = ? AND version = ? AND fence_epoch = ?
  AND lease_worker_id = ? AND lease_epoch = ?
  AND lease_expires_at > statement_timestamp()
RETURNING version
`, string(input.Target), input.ActorID, lease.RunID, lease.ProjectID, string(lease.State),
			int64(lease.RunVersion), int64(lease.RunFenceEpoch), lease.WorkerID,
			int64(lease.RunFenceEpoch)).Scan(&runVersion)
		if run.Error != nil {
			return run.Error
		}
		if run.RowsAffected != 1 {
			return ErrWorkerLeaseLost
		}
		attempt := transaction.Raw(`
UPDATE candidate_verification_attempts
SET state = ?, version = version + 1, updated_by = ?
WHERE id = ? AND run_id = ? AND project_id = ? AND state = ?
  AND version = ? AND fence_epoch = ? AND lease_worker_id = ? AND lease_epoch = ?
  AND lease_expires_at > statement_timestamp()
RETURNING version
`, string(input.Target), input.ActorID, lease.AttemptID, lease.RunID, lease.ProjectID,
			string(lease.State), int64(lease.AttemptVersion), int64(lease.AttemptFenceEpoch),
			lease.WorkerID, int64(lease.AttemptFenceEpoch)).Scan(&attemptVersion)
		if attempt.Error != nil {
			return attempt.Error
		}
		if attempt.RowsAffected != 1 {
			return ErrWorkerLeaseLost
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CandidateExecutionLease{}, mapCandidateWorkerStoreError("transition", err)
	}
	lease.State = input.Target
	lease.RunVersion = uint64(runVersion.Version)
	lease.AttemptVersion = uint64(attemptVersion.Version)
	return lease, nil
}

func (store *PostgresStore) GetExecutionPlan(
	ctx context.Context,
	projectID, planID string,
) (Plan, error) {
	return store.GetPlan(ctx, projectID, planID)
}

func (store *PostgresStore) CommitCandidateReceipt(
	ctx context.Context,
	input CommitCandidateReceiptInput,
) (Receipt, error) {
	if input.Lease.State != RunCollecting || input.Receipt.RunID != input.Lease.RunID ||
		input.Receipt.ProjectID != input.Lease.ProjectID || input.Receipt.Plan != input.Lease.Plan {
		return Receipt{}, ErrInvalidReceipt
	}
	receipt, err := store.PersistReceipt(ctx, PersistReceiptInput{
		Receipt: input.Receipt, ExpectedRunVersion: input.Lease.RunVersion,
		ExpectedRunFenceEpoch:  input.Lease.RunFenceEpoch,
		ExpectedRunLeaseWorker: input.Lease.WorkerID,
		ExpectedAttemptID:      input.Lease.AttemptID, ExpectedAttemptVersion: input.Lease.AttemptVersion,
		ExpectedAttemptFence: input.Lease.AttemptFenceEpoch, TerminalReason: input.TerminalReason,
	})
	if errors.Is(err, ErrReceiptRunConflict) {
		return Receipt{}, fmt.Errorf("%w: %v", ErrWorkerLeaseLost, err)
	}
	return receipt, err
}

func candidateLeaseFromRow(row candidateExecutionRow) (CandidateExecutionLease, error) {
	state := RunState(row.RunState)
	if !validUUIDs(row.ProjectID, row.RunID, row.AttemptID, row.PlanID) ||
		!exactSHA256(row.PlanHash) || state != RunClaimed || row.RunVersion <= 0 ||
		row.RunFenceEpoch <= 0 || row.AttemptVersion <= 0 || row.AttemptFenceEpoch <= 0 ||
		row.AttemptOrdinal < 1 || !workerIDPattern.MatchString(row.WorkerID) || row.LeaseExpiresAt.IsZero() {
		return CandidateExecutionLease{}, runIntegrity("Candidate worker lease projection", nil)
	}
	return CandidateExecutionLease{
		ProjectID: row.ProjectID, RunID: row.RunID, AttemptID: row.AttemptID,
		Plan:           PlanReference{ID: row.PlanID, ContentHash: row.PlanHash},
		AttemptOrdinal: row.AttemptOrdinal, State: state,
		RunVersion: uint64(row.RunVersion), RunFenceEpoch: uint64(row.RunFenceEpoch),
		AttemptVersion: uint64(row.AttemptVersion), AttemptFenceEpoch: uint64(row.AttemptFenceEpoch),
		WorkerID: row.WorkerID, LeaseExpiresAt: row.LeaseExpiresAt.UTC(),
	}, nil
}

func validateCandidateLeaseInput(
	lease CandidateExecutionLease,
	actorID string,
	duration time.Duration,
) error {
	if !validUUIDs(actorID, lease.ProjectID, lease.RunID, lease.AttemptID, lease.Plan.ID) ||
		!exactSHA256(lease.Plan.ContentHash) || !candidateWorkerActiveState(lease.State) ||
		lease.RunVersion == 0 || lease.RunFenceEpoch == 0 || lease.AttemptVersion == 0 ||
		lease.AttemptFenceEpoch == 0 || lease.AttemptOrdinal < 1 ||
		!workerIDPattern.MatchString(lease.WorkerID) || lease.LeaseExpiresAt.IsZero() ||
		!validCandidateLeaseDuration(duration) {
		return ErrInvalidWorkerConfig
	}
	return nil
}

func validCandidateLeaseDuration(duration time.Duration) bool {
	return duration >= time.Second && duration <= 24*time.Hour
}

func candidateWorkerActiveState(state RunState) bool {
	switch state {
	case RunClaimed, RunMaterializing, RunPreparing, RunRunning, RunCollecting:
		return true
	default:
		return false
	}
}

func candidateWorkerTransition(current, target RunState) bool {
	return (current == RunClaimed && target == RunMaterializing) ||
		(current == RunMaterializing && target == RunPreparing) ||
		(current == RunPreparing && target == RunRunning) ||
		(current == RunRunning && target == RunCollecting)
}

func earlierTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left.UTC()
	}
	return right.UTC()
}

func mapCandidateWorkerStoreError(operation string, err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrWorkerLeaseLost) {
		return err
	}
	mapped := mapRunStoreError(operation, err)
	if errors.Is(mapped, ErrRunVersionConflict) || errors.Is(mapped, ErrRunTransition) {
		return fmt.Errorf("%w: %v", ErrWorkerLeaseLost, mapped)
	}
	return mapped
}
