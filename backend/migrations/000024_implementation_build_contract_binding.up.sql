-- Every implementation mutation is bound to the exact ready Application
-- Build Contract that authorized it. Nullable columns preserve historical
-- readability; triggers reject unbound new work and prevent legacy work from
-- advancing beyond a terminal stale/rejected state.

ALTER TABLE application_build_contracts
  ADD CONSTRAINT application_build_contract_id_hash_unique
  UNIQUE (id, contract_hash);

ALTER TABLE implementation_generation_claims
  ADD COLUMN application_build_contract_id uuid,
  ADD COLUMN application_build_contract_hash text,
  ADD CONSTRAINT implementation_generation_build_contract_shape CHECK (
    (application_build_contract_id IS NULL AND application_build_contract_hash IS NULL)
    OR
    (application_build_contract_id IS NOT NULL
      AND application_build_contract_hash ~ '^(sha256:)?[0-9a-f]{64}$')
  ),
  ADD CONSTRAINT implementation_generation_build_contract_exact_fk
    FOREIGN KEY (application_build_contract_id, application_build_contract_hash)
    REFERENCES application_build_contracts(id, contract_hash)
    ON DELETE RESTRICT;

ALTER TABLE implementation_proposals
  ADD COLUMN application_build_contract_id uuid,
  ADD COLUMN application_build_contract_hash text,
  ADD CONSTRAINT implementation_proposal_build_contract_shape CHECK (
    (application_build_contract_id IS NULL AND application_build_contract_hash IS NULL)
    OR
    (application_build_contract_id IS NOT NULL
      AND application_build_contract_hash ~ '^(sha256:)?[0-9a-f]{64}$')
  ),
  ADD CONSTRAINT implementation_proposal_build_contract_exact_fk
    FOREIGN KEY (application_build_contract_id, application_build_contract_hash)
    REFERENCES application_build_contracts(id, contract_hash)
    ON DELETE RESTRICT;

CREATE INDEX implementation_generation_build_contract_idx
  ON implementation_generation_claims (application_build_contract_id, application_build_contract_hash)
  WHERE application_build_contract_id IS NOT NULL;

CREATE INDEX implementation_proposal_build_contract_idx
  ON implementation_proposals (application_build_contract_id, application_build_contract_hash)
  WHERE application_build_contract_id IS NOT NULL;

CREATE OR REPLACE FUNCTION exact_application_build_contract_is_ready(
  target_contract_id uuid,
  target_contract_hash text,
  target_project_id uuid,
  target_build_manifest_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
  SELECT EXISTS (
    SELECT 1
    FROM application_build_contracts AS contract
    JOIN application_build_manifests AS manifest
      ON manifest.id = contract.build_manifest_id
     AND manifest.project_id = contract.project_id
     AND manifest.manifest_hash = contract.build_manifest_hash
    WHERE contract.id = target_contract_id
      AND contract.contract_hash = target_contract_hash
      AND contract.project_id = target_project_id
      AND contract.build_manifest_id = target_build_manifest_id
      AND contract.status = 'ready'
      AND contract.must_count > 0
      AND contract.must_ready_count = contract.must_count
      AND contract.blocking_count = 0
      AND contract.conflict_count = 0
      AND manifest.status = 'frozen'
      AND NOT EXISTS (
        SELECT 1
        FROM application_build_contract_template_releases AS selected
        LEFT JOIN template_release_policies AS policy
          ON policy.template_release_id = selected.template_release_id
         AND policy.release_content_hash = selected.template_release_content_hash
        WHERE selected.contract_id = contract.id
          AND (policy.template_release_id IS NULL OR policy.state <> 'approved')
      )
  );
$$;

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

  -- Failure/stale/rejection must remain writable even when a formerly ready
  -- contract is superseded or a component policy is revoked.
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
     AND NEW.execution_source <> 'manual_submission'
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

CREATE TRIGGER implementation_generation_build_contract_guard
BEFORE INSERT OR UPDATE ON implementation_generation_claims
FOR EACH ROW EXECUTE FUNCTION guard_implementation_build_contract_binding();

CREATE TRIGGER implementation_proposal_build_contract_guard
BEFORE INSERT OR UPDATE ON implementation_proposals
FOR EACH ROW EXECUTE FUNCTION guard_implementation_build_contract_binding();

