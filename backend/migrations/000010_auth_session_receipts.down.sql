DROP INDEX IF EXISTS idempotency_auth_refresh_replay_idx;

ALTER TABLE idempotency_records
  DROP CONSTRAINT IF EXISTS idempotency_auth_receipt_safe_check;
