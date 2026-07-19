DO $qualification_evidence_down_guard$
BEGIN
  -- Fence concurrent appends before observing emptiness. A clean observation
  -- cannot race a later immutable commit while DROP waits for its own lock.
  LOCK TABLE qualification_evidence_events IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_operations IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_heads IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_projection_authorizations IN ACCESS EXCLUSIVE MODE;
  IF EXISTS (SELECT 1 FROM qualification_evidence_events)
     OR EXISTS (SELECT 1 FROM qualification_evidence_operations)
     OR EXISTS (SELECT 1 FROM qualification_evidence_heads)
     OR EXISTS (SELECT 1 FROM qualification_evidence_projection_authorizations) THEN
    RAISE EXCEPTION 'cannot roll back Qualification Evidence store while immutable audit state is nonempty';
  END IF;
END;
$qualification_evidence_down_guard$;

DROP TRIGGER IF EXISTS qualification_evidence_heads_guard ON qualification_evidence_heads;
DROP TRIGGER IF EXISTS qualification_evidence_operations_immutable ON qualification_evidence_operations;
DROP TRIGGER IF EXISTS qualification_evidence_events_immutable ON qualification_evidence_events;
DROP FUNCTION IF EXISTS append_qualification_evidence_event(text,bytea,jsonb,text,bytea,jsonb);
DROP FUNCTION IF EXISTS guard_qualification_evidence_head_projection();
DROP FUNCTION IF EXISTS reject_qualification_evidence_immutable_mutation();
DROP TABLE IF EXISTS qualification_evidence_projection_authorizations;
DROP TABLE IF EXISTS qualification_evidence_heads;
DROP TABLE IF EXISTS qualification_evidence_operations;
DROP TABLE IF EXISTS qualification_evidence_events;
DROP FUNCTION IF EXISTS qualification_evidence_sha256(bytea);
