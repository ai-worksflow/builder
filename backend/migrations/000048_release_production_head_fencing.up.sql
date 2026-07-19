-- Production delivery is serialized per project/environment and every Run
-- records the exact production head it was created against. The mutable head
-- is only a projection: immutable ProductionReceipts and DeploymentRevisions
-- remain the authority and every head advance is an in-transaction CAS.

DO $$
BEGIN
  IF EXISTS (
    SELECT project_id
    FROM release_deployment_runs
    WHERE state IN ('queued','claimed','deploying','verifying')
    GROUP BY project_id
    HAVING count(*) > 1
  ) THEN
    RAISE EXCEPTION 'release production head migration requires at most one nonterminal Run per project'
      USING ERRCODE = '40001';
  END IF;
  IF EXISTS (
    SELECT 1
    FROM release_deployment_runs
    WHERE state IN ('queued','claimed','deploying','verifying')
  ) THEN
    RAISE EXCEPTION 'release production head migration requires production delivery Runs to be drained'
      USING ERRCODE = '40001';
  END IF;
END;
$$;

ALTER TABLE release_deployment_runs
  ADD COLUMN environment text NOT NULL DEFAULT 'production',
  ADD COLUMN expected_revision_id uuid,
  ADD COLUMN expected_revision_hash text,
  ADD COLUMN expected_production_receipt_id uuid,
  ADD COLUMN expected_production_receipt_hash text,
  ADD CONSTRAINT release_deployment_run_environment_shape CHECK (
    environment = btrim(environment)
    AND environment ~ '^[a-z][a-z0-9-]{0,62}$'
  ),
  ADD CONSTRAINT release_deployment_run_expected_head_shape CHECK (
    (
      expected_revision_id IS NULL
      AND expected_revision_hash IS NULL
      AND expected_production_receipt_id IS NULL
      AND expected_production_receipt_hash IS NULL
    ) OR (
      expected_revision_id IS NOT NULL
      AND expected_revision_hash ~ '^sha256:[0-9a-f]{64}$'
      AND expected_production_receipt_id IS NOT NULL
      AND expected_production_receipt_hash ~ '^sha256:[0-9a-f]{64}$'
    )
  ),
  ADD CONSTRAINT release_deployment_run_expected_revision_exact_fk
    FOREIGN KEY (expected_revision_id, expected_revision_hash)
    REFERENCES release_deployment_revisions(id, payload_hash) ON DELETE RESTRICT,
  ADD CONSTRAINT release_deployment_run_expected_receipt_exact_fk
    FOREIGN KEY (expected_production_receipt_id, expected_production_receipt_hash)
    REFERENCES release_production_receipts(id, payload_hash) ON DELETE RESTRICT;

CREATE TABLE release_production_heads (
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  environment text NOT NULL CHECK (
    environment = btrim(environment)
    AND environment ~ '^[a-z][a-z0-9-]{0,62}$'
  ),
  deployment_revision_id uuid,
  deployment_revision_hash text,
  production_receipt_id uuid,
  production_receipt_hash text,
  generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
  updated_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  PRIMARY KEY (project_id, environment),
  CONSTRAINT release_production_head_shape CHECK (
    (
      generation = 0
      AND deployment_revision_id IS NULL
      AND deployment_revision_hash IS NULL
      AND production_receipt_id IS NULL
      AND production_receipt_hash IS NULL
    ) OR (
      generation > 0
      AND deployment_revision_id IS NOT NULL
      AND deployment_revision_hash ~ '^sha256:[0-9a-f]{64}$'
      AND production_receipt_id IS NOT NULL
      AND production_receipt_hash ~ '^sha256:[0-9a-f]{64}$'
    )
  ),
  CONSTRAINT release_production_head_revision_exact_fk
    FOREIGN KEY (deployment_revision_id, deployment_revision_hash)
    REFERENCES release_deployment_revisions(id, payload_hash) ON DELETE RESTRICT,
  CONSTRAINT release_production_head_receipt_exact_fk
    FOREIGN KEY (production_receipt_id, production_receipt_hash)
    REFERENCES release_production_receipts(id, payload_hash) ON DELETE RESTRICT
);

-- Existing terminal history is preserved. Its latest healthy immutable facts
-- seed the projection; no historical Run is rewritten.
WITH latest AS (
  SELECT DISTINCT ON (run.project_id)
    run.project_id,
    revision.id AS revision_id,
    revision.payload_hash AS revision_hash,
    receipt.id AS receipt_id,
    receipt.payload_hash AS receipt_hash,
    run.updated_by,
    run.finished_at
  FROM release_deployment_runs AS run
  JOIN release_deployment_revisions AS revision
    ON revision.run_id = run.id
   AND revision.project_id = run.project_id
  JOIN release_production_receipts AS receipt
    ON receipt.id = revision.production_receipt_id
   AND receipt.payload_hash = revision.production_receipt_hash
  WHERE run.state = 'healthy'
  ORDER BY run.project_id, run.finished_at DESC, run.id DESC
)
INSERT INTO release_production_heads (
  project_id, environment,
  deployment_revision_id, deployment_revision_hash,
  production_receipt_id, production_receipt_hash,
  generation, updated_by, updated_at
)
SELECT project_id, 'production', revision_id, revision_hash,
       receipt_id, receipt_hash, 1, updated_by, finished_at
FROM latest;

CREATE UNIQUE INDEX release_deployment_runs_one_nonterminal_environment_idx
  ON release_deployment_runs (project_id, environment)
  WHERE state IN ('queued','claimed','deploying','verifying');

CREATE OR REPLACE FUNCTION validate_release_production_head_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.generation <> 0
       OR NEW.deployment_revision_id IS NOT NULL
       OR NEW.deployment_revision_hash IS NOT NULL
       OR NEW.production_receipt_id IS NOT NULL
       OR NEW.production_receipt_hash IS NOT NULL THEN
      RAISE EXCEPTION 'new release production head must start empty'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'release production head cannot be deleted'
      USING ERRCODE = '55000';
  END IF;

  IF NEW.project_id IS DISTINCT FROM OLD.project_id
     OR NEW.environment IS DISTINCT FROM OLD.environment
     OR NEW.generation <> OLD.generation + 1
     OR NEW.updated_at <= OLD.updated_at
     OR NEW.deployment_revision_id IS NULL
     OR NEW.production_receipt_id IS NULL
     OR (
       NEW.deployment_revision_id IS NOT DISTINCT FROM OLD.deployment_revision_id
       AND NEW.deployment_revision_hash IS NOT DISTINCT FROM OLD.deployment_revision_hash
       AND NEW.production_receipt_id IS NOT DISTINCT FROM OLD.production_receipt_id
       AND NEW.production_receipt_hash IS NOT DISTINCT FROM OLD.production_receipt_hash
     ) THEN
    RAISE EXCEPTION 'release production head update must be one exact forward CAS'
      USING ERRCODE = '40001';
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM release_deployment_revisions AS revision
    JOIN release_production_receipts AS receipt
      ON receipt.id = revision.production_receipt_id
     AND receipt.payload_hash = revision.production_receipt_hash
    JOIN release_deployment_runs AS run
      ON run.id = revision.run_id
     AND run.project_id = revision.project_id
    WHERE revision.id = NEW.deployment_revision_id
      AND revision.payload_hash = NEW.deployment_revision_hash
      AND receipt.id = NEW.production_receipt_id
      AND receipt.payload_hash = NEW.production_receipt_hash
      AND receipt.decision = 'passed'
      AND run.project_id = NEW.project_id
      AND run.environment = NEW.environment
      AND run.state = 'verifying'
      AND run.expected_revision_id IS NOT DISTINCT FROM OLD.deployment_revision_id
      AND run.expected_revision_hash IS NOT DISTINCT FROM OLD.deployment_revision_hash
      AND run.expected_production_receipt_id IS NOT DISTINCT FROM OLD.production_receipt_id
      AND run.expected_production_receipt_hash IS NOT DISTINCT FROM OLD.production_receipt_hash
      AND run.updated_by = NEW.updated_by
  ) THEN
    RAISE EXCEPTION 'release production head CAS requires the exact verifying Run and immutable passed facts'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_production_head_mutation_guard
BEFORE INSERT OR UPDATE OR DELETE ON release_production_heads
FOR EACH ROW EXECUTE FUNCTION validate_release_production_head_mutation();

CREATE OR REPLACE FUNCTION validate_release_deployment_run_head_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  head release_production_heads%ROWTYPE;
BEGIN
  INSERT INTO release_production_heads (
    project_id, environment, generation, updated_by
  ) VALUES (
    NEW.project_id, NEW.environment, 0, NEW.created_by
  ) ON CONFLICT (project_id, environment) DO NOTHING;

  SELECT * INTO STRICT head
  FROM release_production_heads
  WHERE project_id = NEW.project_id AND environment = NEW.environment
  FOR UPDATE;

  IF NEW.expected_revision_id IS DISTINCT FROM head.deployment_revision_id
     OR NEW.expected_revision_hash IS DISTINCT FROM head.deployment_revision_hash
     OR NEW.expected_production_receipt_id IS DISTINCT FROM head.production_receipt_id
     OR NEW.expected_production_receipt_hash IS DISTINCT FROM head.production_receipt_hash THEN
    RAISE EXCEPTION 'release deployment Run expected production head is stale'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_deployment_run_head_insert_guard
BEFORE INSERT ON release_deployment_runs
FOR EACH ROW EXECUTE FUNCTION validate_release_deployment_run_head_insert();

CREATE OR REPLACE FUNCTION validate_release_deployment_run_head_terminal()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  head release_production_heads%ROWTYPE;
BEGIN
  IF NEW.state = OLD.state OR NEW.state NOT IN ('healthy','failed','error','cancelled') THEN
    RETURN NEW;
  END IF;

  SELECT * INTO STRICT head
  FROM release_production_heads
  WHERE project_id = NEW.project_id AND environment = NEW.environment;

  IF NEW.state = 'healthy' THEN
    IF NOT EXISTS (
      SELECT 1
      FROM release_deployment_revisions AS revision
      JOIN release_production_receipts AS receipt
        ON receipt.id = revision.production_receipt_id
       AND receipt.payload_hash = revision.production_receipt_hash
      WHERE revision.run_id = NEW.id
        AND revision.project_id = NEW.project_id
        AND receipt.decision = 'passed'
        AND head.deployment_revision_id = revision.id
        AND head.deployment_revision_hash = revision.payload_hash
        AND head.production_receipt_id = receipt.id
        AND head.production_receipt_hash = receipt.payload_hash
    ) THEN
      RAISE EXCEPTION 'healthy release deployment Run requires its committed production head CAS'
        USING ERRCODE = '40001';
    END IF;
  ELSIF head.deployment_revision_id IS DISTINCT FROM OLD.expected_revision_id
        OR head.deployment_revision_hash IS DISTINCT FROM OLD.expected_revision_hash
        OR head.production_receipt_id IS DISTINCT FROM OLD.expected_production_receipt_id
        OR head.production_receipt_hash IS DISTINCT FROM OLD.expected_production_receipt_hash THEN
    RAISE EXCEPTION 'failed release deployment Run observed a changed production head'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_deployment_run_head_terminal_guard
BEFORE UPDATE ON release_deployment_runs
FOR EACH ROW EXECUTE FUNCTION validate_release_deployment_run_head_terminal();
