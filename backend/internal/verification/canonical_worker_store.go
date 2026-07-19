package verification

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"gorm.io/gorm"
)

type CanonicalExecutionLease struct {
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

type ClaimCanonicalExecutionInput struct {
	AttemptID     string
	ActorID       string
	WorkerID      string
	LeaseDuration time.Duration
}

type canonicalExecutionRow struct {
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

type canonicalClaimRunRow struct {
	ProjectID     string       `gorm:"column:project_id"`
	RunID         string       `gorm:"column:run_id"`
	PlanID        string       `gorm:"column:plan_id"`
	PlanHash      string       `gorm:"column:plan_hash"`
	State         string       `gorm:"column:state"`
	Version       int64        `gorm:"column:version"`
	FenceEpoch    int64        `gorm:"column:fence_epoch"`
	LeaseExpires  sql.NullTime `gorm:"column:lease_expires_at"`
	LeaseWorkerID string       `gorm:"column:lease_worker_id"`
}

func (store *PostgresStore) ClaimCanonicalExecution(
	ctx context.Context,
	input ClaimCanonicalExecutionInput,
) (CanonicalExecutionLease, bool, error) {
	input.AttemptID, input.ActorID, input.WorkerID = strings.TrimSpace(input.AttemptID), strings.TrimSpace(input.ActorID), strings.TrimSpace(input.WorkerID)
	if store == nil || store.database == nil || !validUUIDs(input.AttemptID, input.ActorID) ||
		!workerIDPattern.MatchString(input.WorkerID) || !validCandidateLeaseDuration(input.LeaseDuration) {
		return CanonicalExecutionLease{}, false, ErrInvalidWorkerConfig
	}
	found := false
	var claimed canonicalExecutionRow
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var run canonicalClaimRunRow
		result := transaction.Raw(`
SELECT run.project_id::text AS project_id, run.id::text AS run_id,
       run.plan_id::text AS plan_id, run.plan_hash, run.state, run.version,
       run.fence_epoch, run.lease_expires_at,
       COALESCE(run.lease_worker_id, '') AS lease_worker_id
FROM canonical_verification_runs AS run
JOIN canonical_verification_plans AS plan ON plan.id = run.plan_id AND plan.plan_hash = run.plan_hash
JOIN verification_profile_policies AS policy
  ON policy.profile_id = plan.verification_profile_id
 AND policy.profile_version = plan.verification_profile_version
 AND policy.profile_hash = plan.verification_profile_hash AND policy.state = 'active'
WHERE run.state = 'queued'
   OR (run.state IN ('claimed','materializing','preparing','running','collecting')
       AND run.lease_expires_at <= statement_timestamp()
       AND EXISTS (
         SELECT 1
         FROM canonical_verification_attempts AS active_attempt
         JOIN verification_execution_cleanups AS cleanup
           ON cleanup.scope = 'canonical'
          AND cleanup.attempt_id = active_attempt.id
          AND cleanup.attempt_fence_epoch = active_attempt.fence_epoch
          AND cleanup.state = 'completed'
         WHERE active_attempt.run_id = run.id
           AND active_attempt.state = run.state
       ))
ORDER BY CASE WHEN run.state = 'queued' THEN 0 ELSE 1 END, run.created_at, run.id
FOR UPDATE OF run SKIP LOCKED LIMIT 1
`).Scan(&run)
		if result.Error != nil {
			return result.Error
		}
		if run.RunID == "" {
			return nil
		}
		found = true
		milliseconds := input.LeaseDuration.Milliseconds()
		if run.State == string(RunQueued) {
			var runUpdate canonicalExecutionRow
			updated := transaction.Raw(`
UPDATE canonical_verification_runs
SET state = 'claimed', version = version + 1, fence_epoch = fence_epoch + 1,
    lease_worker_id = ?, lease_epoch = fence_epoch + 1,
    lease_expires_at = statement_timestamp() + (? * interval '1 millisecond'),
    started_at = statement_timestamp(), updated_by = ?
WHERE id = ? AND state = 'queued' AND version = ? AND fence_epoch = ?
RETURNING version AS run_version, fence_epoch AS run_fence_epoch, lease_expires_at
`, input.WorkerID, milliseconds, input.ActorID, run.RunID, run.Version, run.FenceEpoch).Scan(&runUpdate)
			if updated.Error != nil || updated.RowsAffected != 1 {
				if updated.Error != nil {
					return updated.Error
				}
				return ErrWorkerLeaseLost
			}
			if err := transaction.Exec(`
INSERT INTO canonical_verification_attempts (
  id, schema_version, run_id, project_id, plan_id, plan_hash,
  ordinal, state, version, fence_epoch, created_by, updated_by
) VALUES (?, 'canonical-verification-attempt/v1', ?, ?, ?, ?, 1, 'queued', 1, 0, ?, ?)
`, input.AttemptID, run.RunID, run.ProjectID, run.PlanID, run.PlanHash, input.ActorID, input.ActorID).Error; err != nil {
				return err
			}
			var attemptUpdate canonicalExecutionRow
			attempt := transaction.Raw(`
UPDATE canonical_verification_attempts AS attempt
SET state = 'claimed', version = attempt.version + 1, fence_epoch = run.fence_epoch,
    lease_worker_id = run.lease_worker_id, lease_epoch = run.lease_epoch,
    lease_expires_at = run.lease_expires_at, started_at = statement_timestamp(), updated_by = ?
FROM canonical_verification_runs AS run
WHERE attempt.id = ? AND run.id = attempt.run_id AND attempt.state = 'queued'
RETURNING attempt.id::text AS attempt_id, attempt.ordinal AS attempt_ordinal,
          attempt.version AS attempt_version, attempt.fence_epoch AS attempt_fence_epoch,
          attempt.lease_expires_at
`, input.ActorID, input.AttemptID).Scan(&attemptUpdate)
			if attempt.Error != nil || attempt.RowsAffected != 1 {
				if attempt.Error != nil {
					return attempt.Error
				}
				return ErrWorkerLeaseLost
			}
			claimed.RunVersion, claimed.RunFenceEpoch = runUpdate.RunVersion, runUpdate.RunFenceEpoch
			claimed.AttemptID, claimed.AttemptOrdinal = attemptUpdate.AttemptID, attemptUpdate.AttemptOrdinal
			claimed.AttemptVersion, claimed.AttemptFenceEpoch = attemptUpdate.AttemptVersion, attemptUpdate.AttemptFenceEpoch
			claimed.LeaseExpiresAt = earlierTime(runUpdate.LeaseExpiresAt, attemptUpdate.LeaseExpiresAt)
			if err := insertVerificationCleanupObligation(
				transaction, ScopeCanonical, run.ProjectID, run.RunID, attemptUpdate.AttemptID,
				attemptUpdate.AttemptFenceEpoch, input.ActorID,
			); err != nil {
				return err
			}
		} else {
			var attempt struct {
				ID         string `gorm:"column:id"`
				Ordinal    int    `gorm:"column:ordinal"`
				State      string `gorm:"column:state"`
				Version    int64  `gorm:"column:version"`
				FenceEpoch int64  `gorm:"column:fence_epoch"`
			}
			if err := transaction.Raw(`
SELECT id::text AS id, ordinal, state, version, fence_epoch
FROM canonical_verification_attempts
WHERE run_id = ? AND state IN ('claimed','materializing','preparing','running','collecting')
FOR UPDATE
`, run.RunID).Scan(&attempt).Error; err != nil {
				return err
			}
			if attempt.ID == "" || attempt.State != run.State {
				return runIntegrity("expired Canonical Run active Attempt", nil)
			}
			var runUpdate canonicalExecutionRow
			updated := transaction.Raw(`
UPDATE canonical_verification_runs
SET state = 'claimed', version = version + 1, fence_epoch = fence_epoch + 1,
    lease_worker_id = ?, lease_epoch = fence_epoch + 1,
    lease_expires_at = statement_timestamp() + (? * interval '1 millisecond'), updated_by = ?
WHERE id = ? AND state = ? AND version = ? AND fence_epoch = ?
  AND lease_expires_at <= statement_timestamp()
RETURNING version AS run_version, fence_epoch AS run_fence_epoch, lease_expires_at
`, input.WorkerID, milliseconds, input.ActorID, run.RunID, run.State, run.Version, run.FenceEpoch).Scan(&runUpdate)
			if updated.Error != nil || updated.RowsAffected != 1 {
				if updated.Error != nil {
					return updated.Error
				}
				return ErrWorkerLeaseLost
			}
			var attemptUpdate canonicalExecutionRow
			reclaimed := transaction.Raw(`
UPDATE canonical_verification_attempts AS attempt
SET state = 'claimed', version = attempt.version + 1, fence_epoch = run.fence_epoch,
    lease_worker_id = run.lease_worker_id, lease_epoch = run.lease_epoch,
    lease_expires_at = run.lease_expires_at, updated_by = ?
FROM canonical_verification_runs AS run
WHERE attempt.id = ? AND run.id = attempt.run_id AND attempt.state = ?
  AND attempt.version = ? AND attempt.fence_epoch = ?
  AND attempt.lease_expires_at <= statement_timestamp()
RETURNING attempt.id::text AS attempt_id, attempt.ordinal AS attempt_ordinal,
          attempt.version AS attempt_version, attempt.fence_epoch AS attempt_fence_epoch,
          attempt.lease_expires_at
`, input.ActorID, attempt.ID, attempt.State, attempt.Version, attempt.FenceEpoch).Scan(&attemptUpdate)
			if reclaimed.Error != nil || reclaimed.RowsAffected != 1 {
				if reclaimed.Error != nil {
					return reclaimed.Error
				}
				return ErrWorkerLeaseLost
			}
			claimed.RunVersion, claimed.RunFenceEpoch = runUpdate.RunVersion, runUpdate.RunFenceEpoch
			claimed.AttemptID, claimed.AttemptOrdinal = attemptUpdate.AttemptID, attemptUpdate.AttemptOrdinal
			claimed.AttemptVersion, claimed.AttemptFenceEpoch = attemptUpdate.AttemptVersion, attemptUpdate.AttemptFenceEpoch
			claimed.LeaseExpiresAt = earlierTime(runUpdate.LeaseExpiresAt, attemptUpdate.LeaseExpiresAt)
			if err := insertVerificationCleanupObligation(
				transaction, ScopeCanonical, run.ProjectID, run.RunID, attemptUpdate.AttemptID,
				attemptUpdate.AttemptFenceEpoch, input.ActorID,
			); err != nil {
				return err
			}
		}
		claimed.ProjectID, claimed.RunID, claimed.PlanID, claimed.PlanHash = run.ProjectID, run.RunID, run.PlanID, run.PlanHash
		claimed.RunState, claimed.WorkerID = string(RunClaimed), input.WorkerID
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CanonicalExecutionLease{}, false, mapCandidateWorkerStoreError("claim Canonical execution", err)
	}
	if !found {
		return CanonicalExecutionLease{}, false, nil
	}
	lease, err := canonicalLeaseFromRow(claimed)
	return lease, err == nil, err
}

func (store *PostgresStore) HeartbeatCanonicalExecution(
	ctx context.Context,
	lease CanonicalExecutionLease,
	actorID string,
	duration time.Duration,
) (CanonicalExecutionLease, error) {
	if err := validateCanonicalLease(lease, actorID, duration); err != nil {
		return CanonicalExecutionLease{}, err
	}
	var runUpdate, attemptUpdate canonicalExecutionRow
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		milliseconds := duration.Milliseconds()
		run := transaction.Raw(`
UPDATE canonical_verification_runs
SET version = version + 1,
    lease_expires_at = statement_timestamp() + (? * interval '1 millisecond'), updated_by = ?
WHERE id = ? AND state = ? AND version = ? AND fence_epoch = ?
  AND lease_worker_id = ? AND lease_epoch = ? AND lease_expires_at > statement_timestamp()
RETURNING version AS run_version, fence_epoch AS run_fence_epoch, lease_expires_at
`, milliseconds, actorID, lease.RunID, string(lease.State), lease.RunVersion,
			lease.RunFenceEpoch, lease.WorkerID, lease.RunFenceEpoch).Scan(&runUpdate)
		if run.Error != nil || run.RowsAffected != 1 {
			if run.Error != nil {
				return run.Error
			}
			return ErrWorkerLeaseLost
		}
		attempt := transaction.Raw(`
UPDATE canonical_verification_attempts
SET version = version + 1, lease_expires_at = ?, updated_by = ?
WHERE id = ? AND run_id = ? AND state = ? AND version = ? AND fence_epoch = ?
  AND lease_worker_id = ? AND lease_epoch = ? AND lease_expires_at > statement_timestamp()
RETURNING id::text AS attempt_id, ordinal AS attempt_ordinal,
          version AS attempt_version, fence_epoch AS attempt_fence_epoch, lease_expires_at
`, runUpdate.LeaseExpiresAt, actorID, lease.AttemptID, lease.RunID, string(lease.State),
			lease.AttemptVersion, lease.AttemptFenceEpoch, lease.WorkerID,
			lease.AttemptFenceEpoch).Scan(&attemptUpdate)
		if attempt.Error != nil || attempt.RowsAffected != 1 {
			if attempt.Error != nil {
				return attempt.Error
			}
			return ErrWorkerLeaseLost
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CanonicalExecutionLease{}, mapCandidateWorkerStoreError("heartbeat Canonical execution", err)
	}
	lease.RunVersion, lease.AttemptVersion = uint64(runUpdate.RunVersion), uint64(attemptUpdate.AttemptVersion)
	lease.LeaseExpiresAt = earlierTime(runUpdate.LeaseExpiresAt, attemptUpdate.LeaseExpiresAt)
	return lease, nil
}

func (store *PostgresStore) TransitionCanonicalExecution(
	ctx context.Context,
	lease CanonicalExecutionLease,
	actorID string,
	target RunState,
) (CanonicalExecutionLease, error) {
	if err := validateCanonicalLease(lease, actorID, time.Second); err != nil || !validRunState(target) || runStateIsTerminal(target) {
		return CanonicalExecutionLease{}, ErrInvalidWorkerConfig
	}
	var runUpdate, attemptUpdate canonicalExecutionRow
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		run := transaction.Raw(`
UPDATE canonical_verification_runs
SET state = ?, version = version + 1, updated_by = ?
WHERE id = ? AND state = ? AND version = ? AND fence_epoch = ?
  AND lease_worker_id = ? AND lease_epoch = ? AND lease_expires_at > statement_timestamp()
RETURNING version AS run_version, fence_epoch AS run_fence_epoch, lease_expires_at
`, string(target), actorID, lease.RunID, string(lease.State), lease.RunVersion,
			lease.RunFenceEpoch, lease.WorkerID, lease.RunFenceEpoch).Scan(&runUpdate)
		if run.Error != nil || run.RowsAffected != 1 {
			if run.Error != nil {
				return run.Error
			}
			return ErrWorkerLeaseLost
		}
		attempt := transaction.Raw(`
UPDATE canonical_verification_attempts
SET state = ?, version = version + 1, updated_by = ?
WHERE id = ? AND run_id = ? AND state = ? AND version = ? AND fence_epoch = ?
  AND lease_worker_id = ? AND lease_epoch = ? AND lease_expires_at > statement_timestamp()
RETURNING id::text AS attempt_id, ordinal AS attempt_ordinal,
          version AS attempt_version, fence_epoch AS attempt_fence_epoch, lease_expires_at
`, string(target), actorID, lease.AttemptID, lease.RunID, string(lease.State),
			lease.AttemptVersion, lease.AttemptFenceEpoch, lease.WorkerID,
			lease.AttemptFenceEpoch).Scan(&attemptUpdate)
		if attempt.Error != nil || attempt.RowsAffected != 1 {
			if attempt.Error != nil {
				return attempt.Error
			}
			return ErrWorkerLeaseLost
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CanonicalExecutionLease{}, mapCandidateWorkerStoreError("transition Canonical execution", err)
	}
	lease.State = target
	lease.RunVersion, lease.AttemptVersion = uint64(runUpdate.RunVersion), uint64(attemptUpdate.AttemptVersion)
	lease.LeaseExpiresAt = earlierTime(runUpdate.LeaseExpiresAt, attemptUpdate.LeaseExpiresAt)
	return lease, nil
}

func (store *PostgresStore) CommitCanonicalReceipt(
	ctx context.Context,
	lease CanonicalExecutionLease,
	receipt CanonicalReceipt,
	actorID, terminalReason string,
) (CanonicalReceipt, error) {
	if err := validateCanonicalLease(lease, actorID, time.Second); err != nil || lease.State != RunCollecting {
		return CanonicalReceipt{}, ErrInvalidWorkerConfig
	}
	parsed, err := ParseCanonicalReceipt(receipt)
	if err != nil || parsed.RunID != lease.RunID || parsed.ProjectID != lease.ProjectID ||
		parsed.Plan.ID != lease.Plan.ID || parsed.Plan.ContentHash != lease.Plan.ContentHash ||
		len(parsed.AttemptIDs) != 1 || parsed.AttemptIDs[0] != lease.AttemptID {
		return CanonicalReceipt{}, fmt.Errorf("%w: Canonical Receipt lease projection", ErrInvalidReceipt)
	}
	payload, err := domain.CanonicalJSON(parsed)
	if err != nil {
		return CanonicalReceipt{}, receiptIntegrity("encode Canonical Receipt", err)
	}
	contentRef, err := store.contents.PutPending(ctx, parsed.ProjectID, canonicalReceiptAggregateType, parsed.ID, 1, payload)
	if err != nil {
		return CanonicalReceipt{}, err
	}
	abort := true
	defer func() {
		if abort {
			_ = store.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	attemptIDs, _ := json.Marshal(parsed.AttemptIDs)
	artifacts, _ := json.Marshal(parsed.ReleaseArtifacts)
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Exec(`
INSERT INTO canonical_verification_receipts (
  id, schema_version, scope, run_id, project_id, plan_id, plan_hash,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  verification_profile_id, verification_profile_version, verification_profile_hash,
  attempt_ids, release_artifacts, check_count, coverage_count,
  must_count, must_passed_count, blocker_count, warning_count, decision, execution_error,
  content_store, content_ref, content_hash, payload_hash, created_by, created_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
  ?::jsonb, ?::jsonb, ?, ?, ?, ?, ?, ?, ?, ?, 'mongo', ?, ?, ?, ?, ?
)
`, parsed.ID, parsed.SchemaVersion, string(parsed.Scope), parsed.RunID, parsed.ProjectID,
			parsed.Plan.ID, parsed.Plan.ContentHash, parsed.Subject.WorkspaceArtifactID,
			parsed.Subject.WorkspaceRevisionID, parsed.Subject.WorkspaceContentHash,
			parsed.BuildManifest.ID, parsed.BuildManifest.ContentHash,
			parsed.BuildContract.ID, parsed.BuildContract.ContentHash,
			parsed.FullStackTemplate.ID, parsed.FullStackTemplate.ContentHash,
			parsed.Profile.ID, int64(parsed.Profile.Version), parsed.Profile.ContentHash,
			string(attemptIDs), string(artifacts), len(parsed.Checks), len(parsed.ObligationCoverage),
			parsed.MustCount, parsed.MustPassedCount, parsed.BlockerCount, parsed.WarningCount,
			string(parsed.Decision), nullableString(parsed.ExecutionError), contentRef.ID,
			contentRef.ContentHash, parsed.PayloadHash, parsed.CreatedBy, parsed.CreatedAt).Error; err != nil {
			return err
		}
		for ordinal, check := range parsed.Checks {
			if err := transaction.Exec(`
INSERT INTO canonical_verification_checks (
  receipt_id, ordinal, check_id, kind, required, status, attempt_id, truncated
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, parsed.ID, ordinal, check.ID, check.Kind, check.Required, string(check.Status), check.AttemptID,
				check.Truncated).Error; err != nil {
				return err
			}
		}
		for ordinal, coverage := range parsed.ObligationCoverage {
			checkIDs, marshalErr := json.Marshal(coverage.CheckIDs)
			if marshalErr != nil {
				return marshalErr
			}
			if err := transaction.Exec(`
INSERT INTO canonical_verification_obligation_coverage (
  receipt_id, ordinal, obligation_id, level, check_ids, status
) VALUES (?, ?, ?, ?, ?::jsonb, ?)
`, parsed.ID, ordinal, coverage.ObligationID, coverage.Level, string(checkIDs), coverage.Status).Error; err != nil {
				return err
			}
		}
		terminalReasonValue := nullableString(strings.TrimSpace(terminalReason))
		attempt := transaction.Exec(`
UPDATE canonical_verification_attempts
SET state = ?, version = version + 1, lease_worker_id = NULL, lease_epoch = NULL,
    lease_expires_at = NULL, terminal_reason = ?, execution_error = ?,
    finished_at = statement_timestamp(), updated_by = ?
WHERE id = ? AND run_id = ? AND state = 'collecting' AND version = ? AND fence_epoch = ?
  AND lease_worker_id = ? AND lease_epoch = ? AND lease_expires_at > statement_timestamp()
`, string(parsed.Decision), terminalReasonValue, nullableString(parsed.ExecutionError), actorID,
			lease.AttemptID, lease.RunID, lease.AttemptVersion, lease.AttemptFenceEpoch,
			lease.WorkerID, lease.AttemptFenceEpoch)
		if attempt.Error != nil || attempt.RowsAffected != 1 {
			if attempt.Error != nil {
				return attempt.Error
			}
			return ErrWorkerLeaseLost
		}
		run := transaction.Exec(`
UPDATE canonical_verification_runs
SET state = ?, version = version + 1, lease_worker_id = NULL, lease_epoch = NULL,
    lease_expires_at = NULL, terminal_reason = ?, execution_error = ?,
    finished_at = statement_timestamp(), updated_by = ?
WHERE id = ? AND project_id = ? AND state = 'collecting' AND version = ? AND fence_epoch = ?
  AND lease_worker_id = ? AND lease_epoch = ? AND lease_expires_at > statement_timestamp()
`, string(parsed.Decision), terminalReasonValue, nullableString(parsed.ExecutionError), actorID,
			lease.RunID, lease.ProjectID, lease.RunVersion, lease.RunFenceEpoch,
			lease.WorkerID, lease.RunFenceEpoch)
		if run.Error != nil || run.RowsAffected != 1 {
			if run.Error != nil {
				return run.Error
			}
			return ErrWorkerLeaseLost
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CanonicalReceipt{}, mapCandidateWorkerStoreError("commit Canonical Receipt", err)
	}
	abort = false
	if err := store.contents.Finalize(ctx, contentRef.ID); err != nil {
		return CanonicalReceipt{}, fmt.Errorf("%w: finalize Canonical Receipt: %v", core.ErrContentNotReady, err)
	}
	return store.GetCanonicalReceipt(ctx, parsed.ProjectID, parsed.ID, parsed.PayloadHash)
}

func canonicalLeaseFromRow(row canonicalExecutionRow) (CanonicalExecutionLease, error) {
	if !validUUIDs(row.ProjectID, row.RunID, row.AttemptID, row.PlanID) || !exactSHA256(row.PlanHash) ||
		row.RunVersion < 1 || row.RunFenceEpoch < 1 || row.AttemptVersion < 1 || row.AttemptFenceEpoch < 1 ||
		row.RunFenceEpoch != row.AttemptFenceEpoch || !workerIDPattern.MatchString(row.WorkerID) || row.LeaseExpiresAt.IsZero() {
		return CanonicalExecutionLease{}, runIntegrity("Canonical execution lease projection", nil)
	}
	return CanonicalExecutionLease{
		ProjectID: row.ProjectID, RunID: row.RunID, AttemptID: row.AttemptID,
		Plan: PlanReference{ID: row.PlanID, ContentHash: row.PlanHash}, AttemptOrdinal: row.AttemptOrdinal,
		State: RunState(row.RunState), RunVersion: uint64(row.RunVersion), RunFenceEpoch: uint64(row.RunFenceEpoch),
		AttemptVersion: uint64(row.AttemptVersion), AttemptFenceEpoch: uint64(row.AttemptFenceEpoch),
		WorkerID: row.WorkerID, LeaseExpiresAt: row.LeaseExpiresAt.UTC(),
	}, nil
}

func validateCanonicalLease(lease CanonicalExecutionLease, actorID string, duration time.Duration) error {
	if !validUUIDs(lease.ProjectID, lease.RunID, lease.AttemptID, lease.Plan.ID, strings.TrimSpace(actorID)) ||
		!exactSHA256(lease.Plan.ContentHash) || !workerIDPattern.MatchString(lease.WorkerID) ||
		lease.RunVersion < 1 || lease.RunFenceEpoch < 1 || lease.AttemptVersion < 1 ||
		lease.AttemptFenceEpoch != lease.RunFenceEpoch || !validCandidateLeaseDuration(duration) {
		return ErrInvalidWorkerConfig
	}
	return nil
}
