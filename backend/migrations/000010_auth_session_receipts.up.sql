ALTER TABLE idempotency_records
  ADD CONSTRAINT idempotency_auth_receipt_safe_check
  CHECK (
    resource_type NOT IN (
      'auth_session_sign_up_v1',
      'auth_session_sign_in_v1',
      'auth_session_refresh_v1'
    )
    OR (
      scope ~ '^auth:(sign-up|sign-in|refresh):[0-9a-f]{64}$'
      AND request_hash ~ '^[0-9a-f]{64}$'
      AND resource_id IS NOT NULL
      AND resource_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
      AND response_status IS NOT NULL
      AND response_body IS NULL
      AND COALESCE(response_headers, '{}'::jsonb) = '{}'::jsonb
      AND completed_at IS NOT NULL
    )
  );

CREATE INDEX idempotency_auth_refresh_replay_idx
  ON idempotency_records (idempotency_key, resource_id)
  WHERE resource_type = 'auth_session_refresh_v1'
    AND completed_at IS NOT NULL;
