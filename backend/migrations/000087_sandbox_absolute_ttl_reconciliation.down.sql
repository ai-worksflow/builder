CREATE OR REPLACE FUNCTION sync_sandbox_session_activity_from_projection()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  INSERT INTO sandbox_session_activity (
    session_id, session_epoch, last_activity_at, idle_deadline, updated_at
  ) VALUES (
    NEW.id, NEW.session_epoch, NEW.updated_at, NEW.idle_deadline, statement_timestamp()
  )
  ON CONFLICT (session_id) DO UPDATE
  SET session_epoch = EXCLUDED.session_epoch,
      last_activity_at = GREATEST(
        sandbox_session_activity.last_activity_at,
        EXCLUDED.last_activity_at
      ),
      idle_deadline = EXCLUDED.idle_deadline,
      updated_at = statement_timestamp();
  RETURN NEW;
END;
$$;
