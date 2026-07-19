DO $credential_set_down_guard$
BEGIN
  -- Fence concurrent append before observing emptiness. Without these locks a
  -- clean read could race a commit while DROP waits for its own stronger lock.
  LOCK TABLE credential_set_events IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE credential_set_operations IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE credential_set_heads IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE credential_set_projection_authorizations IN ACCESS EXCLUSIVE MODE;
  IF EXISTS (SELECT 1 FROM credential_set_events)
     OR EXISTS (SELECT 1 FROM credential_set_operations)
     OR EXISTS (SELECT 1 FROM credential_set_heads)
     OR EXISTS (SELECT 1 FROM credential_set_projection_authorizations) THEN
    RAISE EXCEPTION 'cannot roll back CredentialSet store while immutable audit state is nonempty';
  END IF;
END;
$credential_set_down_guard$;

DROP TRIGGER IF EXISTS credential_set_heads_guard ON credential_set_heads;
DROP TRIGGER IF EXISTS credential_set_operations_immutable ON credential_set_operations;
DROP TRIGGER IF EXISTS credential_set_events_immutable ON credential_set_events;
DROP FUNCTION IF EXISTS append_credential_set_event(text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb);
DROP FUNCTION IF EXISTS guard_credential_set_head_projection();
DROP FUNCTION IF EXISTS reject_credential_set_immutable_mutation();
DROP TABLE IF EXISTS credential_set_projection_authorizations;
DROP TABLE IF EXISTS credential_set_heads;
DROP TABLE IF EXISTS credential_set_operations;
DROP TABLE IF EXISTS credential_set_events;
DROP FUNCTION IF EXISTS credential_set_sha256(bytea);
