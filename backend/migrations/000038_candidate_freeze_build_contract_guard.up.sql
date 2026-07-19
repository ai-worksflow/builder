-- Candidate freeze proposals are produced from an exact, server-validated
-- Candidate snapshot rather than an AI generation claim. Keep the exact ready
-- Application Build Contract guard, but do not require a generation claim for
-- this execution source.
CREATE OR REPLACE FUNCTION guard_implementation_build_contract_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  target_contract_id uuid;
  target_contract_hash text;
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF ROW(OLD.application_build_contract_id, OLD.application_build_contract_hash)
       IS DISTINCT FROM
       ROW(NEW.application_build_contract_id, NEW.application_build_contract_hash) THEN
      RAISE EXCEPTION 'implementation Application Build Contract binding is immutable'
        USING ERRCODE = '55000';
    END IF;
  END IF;

  target_contract_id := NEW.application_build_contract_id;
  target_contract_hash := NEW.application_build_contract_hash;

  IF target_contract_id IS NULL OR target_contract_hash IS NULL THEN
    IF TG_OP = 'INSERT' THEN
      RAISE EXCEPTION 'new implementation work requires an exact Application Build Contract'
        USING ERRCODE = '23514';
    END IF;
    IF TG_TABLE_NAME = 'implementation_generation_claims' AND NEW.status = 'failed' THEN
      RETURN NEW;
    END IF;
    IF TG_TABLE_NAME = 'implementation_proposals' AND NEW.status IN ('stale', 'rejected') THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'legacy unbound implementation work cannot advance'
      USING ERRCODE = '23514';
  END IF;

  IF TG_TABLE_NAME = 'implementation_generation_claims' AND NEW.status = 'failed' THEN
    RETURN NEW;
  END IF;
  IF TG_TABLE_NAME = 'implementation_proposals' AND NEW.status IN ('stale', 'rejected') THEN
    RETURN NEW;
  END IF;

  IF NOT exact_application_build_contract_is_ready(
    target_contract_id,
    target_contract_hash,
    NEW.project_id,
    NEW.build_manifest_id
  ) THEN
    RAISE EXCEPTION 'implementation work is not bound to an exact ready same-project Application Build Contract'
      USING ERRCODE = '23514';
  END IF;

  IF TG_TABLE_NAME = 'implementation_proposals'
     AND NEW.execution_source NOT IN ('manual_submission', 'candidate_freeze')
     AND NOT EXISTS (
       SELECT 1
       FROM implementation_generation_claims AS claim
       WHERE claim.reserved_proposal_id = NEW.id
         AND claim.project_id = NEW.project_id
         AND claim.build_manifest_id = NEW.build_manifest_id
         AND claim.application_build_contract_id = target_contract_id
         AND claim.application_build_contract_hash = target_contract_hash
     ) THEN
    RAISE EXCEPTION 'generated implementation proposal Build Contract differs from its generation claim'
      USING ERRCODE = '23514';
  END IF;

  RETURN NEW;
END;
$$;
