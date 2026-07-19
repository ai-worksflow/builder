package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrDeadlineStoreUnavailable = errors.New("sandbox lifecycle deadline store is unavailable")
	ErrDeadlineLeaseLost        = errors.New("sandbox lifecycle deadline lease was lost")
)

type DeadlineAction string

const (
	DeadlineSuspend        DeadlineAction = "suspend"
	DeadlineTerminate      DeadlineAction = "terminate"
	DeadlineAbandonCleanup DeadlineAction = "abandon_cleanup"
)

type DeadlineLease struct {
	SessionID  string
	ProjectID  string
	Action     DeadlineAction
	Owner      string
	LeaseEpoch uint64
	ObservedAt time.Time
}

type SandboxActivityRecorder interface {
	TouchSandboxActivity(context.Context, string, uint64) error
}

type DeadlineLeaseStore interface {
	ClaimDueDeadline(context.Context, string, time.Duration) (*DeadlineLease, error)
	CompleteDeadline(context.Context, DeadlineLease) error
	RetryDeadline(context.Context, DeadlineLease, time.Duration, string) error
}

// ClaimDueDeadline atomically leases one stable Session whose database-owned
// idle or absolute deadline has elapsed. SKIP LOCKED plus the persistent lease
// permits multiple API replicas without double-controlling the same runtime.
func (store *Store) ClaimDueDeadline(
	ctx context.Context,
	workerID string,
	leaseDuration time.Duration,
) (*DeadlineLease, error) {
	workerID = strings.TrimSpace(workerID)
	if store == nil || store.database == nil || ctx == nil || workerID == "" || len(workerID) > 200 ||
		strings.ContainsAny(workerID, "\r\n\x00") || leaseDuration < time.Second || leaseDuration > time.Hour {
		return nil, ErrDeadlineStoreUnavailable
	}
	leaseMilliseconds := leaseDuration.Milliseconds()
	var row struct {
		SessionID  string    `gorm:"column:session_id"`
		ProjectID  string    `gorm:"column:project_id"`
		Action     string    `gorm:"column:action"`
		Owner      string    `gorm:"column:lease_owner"`
		LeaseEpoch int64     `gorm:"column:lease_epoch"`
		ObservedAt time.Time `gorm:"column:observed_at"`
	}
	query := store.database.WithContext(ctx).Raw(`
WITH due AS (
  SELECT
    session.id AS session_id,
    session.project_id,
    CASE
	  WHEN session.state = 'terminating' AND candidate.status = 'abandoned' THEN 'abandon_cleanup'
      WHEN session.expires_at <= statement_timestamp() THEN 'terminate'
      WHEN candidate.status <> 'active' THEN 'terminate'
      ELSE 'suspend'
    END AS action
  FROM sandbox_sessions AS session
  JOIN sandbox_session_activity AS activity
    ON activity.session_id = session.id
   AND activity.session_epoch = session.session_epoch
  JOIN candidate_workspaces AS candidate
    ON candidate.id = session.candidate_id
   AND candidate.project_id = session.project_id
  LEFT JOIN sandbox_lifecycle_deadline_leases AS existing
    ON existing.session_id = session.id
  WHERE session.state IN ('ready', 'suspended', 'failed', 'terminating')
    AND (
      (session.state = 'terminating' AND candidate.status = 'abandoned')
      OR session.expires_at <= statement_timestamp()
      OR (
        session.state = 'ready'
        AND activity.idle_deadline <= statement_timestamp()
      )
    )
    AND (
      existing.session_id IS NULL
      OR (
        existing.lease_expires_at <= statement_timestamp()
        AND existing.next_attempt_at <= statement_timestamp()
      )
    )
  ORDER BY
	(session.state = 'terminating' AND candidate.status = 'abandoned') DESC,
    (session.expires_at <= statement_timestamp()) DESC,
    LEAST(session.expires_at, activity.idle_deadline),
    session.id
  FOR UPDATE OF session, activity SKIP LOCKED
  LIMIT 1
), claimed AS (
  INSERT INTO sandbox_lifecycle_deadline_leases AS lease (
    session_id, action, lease_owner, lease_epoch, lease_expires_at,
    next_attempt_at, attempt_count, last_error, claimed_at, updated_at
  )
  SELECT
    due.session_id, due.action, ?, 1,
    statement_timestamp() + (CAST(? AS bigint) * interval '1 millisecond'),
    statement_timestamp(), 1, NULL, statement_timestamp(), statement_timestamp()
  FROM due
  ON CONFLICT (session_id) DO UPDATE
  SET action = EXCLUDED.action,
      lease_owner = EXCLUDED.lease_owner,
      lease_epoch = lease.lease_epoch + 1,
      lease_expires_at = EXCLUDED.lease_expires_at,
      next_attempt_at = EXCLUDED.next_attempt_at,
      attempt_count = lease.attempt_count + 1,
      last_error = NULL,
      claimed_at = EXCLUDED.claimed_at,
      updated_at = EXCLUDED.updated_at
  WHERE lease.lease_expires_at <= statement_timestamp()
    AND lease.next_attempt_at <= statement_timestamp()
  RETURNING session_id, action, lease_owner, lease_epoch, claimed_at AS observed_at
)
SELECT
  claimed.session_id, due.project_id, claimed.action, claimed.lease_owner,
  claimed.lease_epoch, claimed.observed_at
FROM claimed
JOIN due ON due.session_id = claimed.session_id
`, workerID, leaseMilliseconds).Scan(&row)
	if query.Error != nil {
		return nil, fmt.Errorf("%w: claim due deadline: %v", ErrDeadlineStoreUnavailable, query.Error)
	}
	if query.RowsAffected == 0 {
		return nil, nil
	}
	if query.RowsAffected != 1 || !validUUID(row.SessionID) || !validUUID(row.ProjectID) ||
		(row.Action != string(DeadlineSuspend) && row.Action != string(DeadlineTerminate) &&
			row.Action != string(DeadlineAbandonCleanup)) ||
		row.Owner != workerID || row.LeaseEpoch <= 0 || row.ObservedAt.IsZero() {
		return nil, fmt.Errorf("%w: claimed deadline returned an invalid lease", ErrStoreIntegrity)
	}
	return &DeadlineLease{
		SessionID: row.SessionID, ProjectID: row.ProjectID, Action: DeadlineAction(row.Action),
		Owner: row.Owner, LeaseEpoch: uint64(row.LeaseEpoch), ObservedAt: row.ObservedAt.UTC(),
	}, nil
}

func (store *Store) CompleteDeadline(ctx context.Context, lease DeadlineLease) error {
	if err := validateDeadlineLease(ctx, store, lease); err != nil {
		return err
	}
	result := store.database.WithContext(ctx).Exec(`
DELETE FROM sandbox_lifecycle_deadline_leases
WHERE session_id = ? AND lease_owner = ? AND lease_epoch = ?
`, lease.SessionID, lease.Owner, int64(lease.LeaseEpoch))
	if result.Error != nil {
		return fmt.Errorf("%w: complete deadline: %v", ErrDeadlineStoreUnavailable, result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrDeadlineLeaseLost
	}
	return nil
}

func (store *Store) RetryDeadline(
	ctx context.Context,
	lease DeadlineLease,
	retryDelay time.Duration,
	reason string,
) error {
	if err := validateDeadlineLease(ctx, store, lease); err != nil {
		return err
	}
	reason = boundedDeadlineError(reason)
	if retryDelay < time.Second || retryDelay > time.Hour || reason == "" {
		return ErrDeadlineStoreUnavailable
	}
	result := store.database.WithContext(ctx).Exec(`
UPDATE sandbox_lifecycle_deadline_leases
SET lease_expires_at = GREATEST(statement_timestamp(), claimed_at + interval '1 microsecond'),
    next_attempt_at = statement_timestamp() + (CAST(? AS bigint) * interval '1 millisecond'),
    last_error = ?,
    updated_at = statement_timestamp()
WHERE session_id = ? AND lease_owner = ? AND lease_epoch = ?
`, retryDelay.Milliseconds(), reason, lease.SessionID, lease.Owner, int64(lease.LeaseEpoch))
	if result.Error != nil {
		return fmt.Errorf("%w: retry deadline: %v", ErrDeadlineStoreUnavailable, result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrDeadlineLeaseLost
	}
	return nil
}

func validateDeadlineLease(ctx context.Context, store *Store, lease DeadlineLease) error {
	if store == nil || store.database == nil || ctx == nil || !validUUID(lease.SessionID) ||
		!validUUID(lease.ProjectID) || strings.TrimSpace(lease.Owner) == "" || len(lease.Owner) > 200 ||
		lease.LeaseEpoch == 0 || lease.ObservedAt.IsZero() ||
		(lease.Action != DeadlineSuspend && lease.Action != DeadlineTerminate &&
			lease.Action != DeadlineAbandonCleanup) {
		return ErrDeadlineStoreUnavailable
	}
	return nil
}

// TouchSandboxActivity advances only the operational idle projection. It does
// not churn the immutable Session version/ETag. ClaimDueDeadline locks this
// same row, so a heartbeat that wins prevents a claim and a live claim fences
// subsequent stream activity until the lifecycle decision completes.
func (store *Store) TouchSandboxActivity(ctx context.Context, sessionID string, sessionEpoch uint64) error {
	if store == nil || store.database == nil || ctx == nil || !validUUID(sessionID) || sessionEpoch == 0 {
		return ErrDeadlineStoreUnavailable
	}
	result := store.database.WithContext(ctx).Exec(`
UPDATE sandbox_session_activity AS activity
SET last_activity_at = statement_timestamp(),
    idle_deadline = LEAST(
      session.expires_at,
      statement_timestamp() + make_interval(secs => session.idle_hibernate_seconds)
    ),
    updated_at = statement_timestamp()
FROM sandbox_sessions AS session
WHERE activity.session_id = ?
  AND activity.session_id = session.id
  AND activity.session_epoch = ?
  AND session.session_epoch = ?
  AND session.state = 'ready'
  AND statement_timestamp() < session.expires_at
  AND NOT EXISTS (
    SELECT 1
    FROM sandbox_lifecycle_deadline_leases AS lease
    WHERE lease.session_id = session.id
      AND lease.lease_expires_at > statement_timestamp()
  )
`, sessionID, int64(sessionEpoch), int64(sessionEpoch))
	if result.Error != nil {
		return fmt.Errorf("%w: touch sandbox activity: %v", ErrDeadlineStoreUnavailable, result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrEpochFenced
	}
	return nil
}

func boundedDeadlineError(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 2000 {
		value = strings.TrimSpace(string(runes[:2000]))
	}
	return value
}

var _ DeadlineLeaseStore = (*Store)(nil)
var _ SandboxActivityRecorder = (*Store)(nil)
