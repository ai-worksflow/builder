ALTER TABLE workflow_definition_versions
  ADD COLUMN execution_profile_version text,
  ADD COLUMN execution_profile_hash text;

-- Expand phase for rolling deployment. A pre-016 writer omits both columns and
-- can only create profile-less definition JSON, so it is deterministically
-- fenced into the frozen legacy profile. New writers always provide exact refs;
-- the content check below prevents a current payload from falling back here.
ALTER TABLE workflow_definition_versions
  ALTER COLUMN execution_profile_version SET DEFAULT 'legacy-pre-pin/v0',
  ALTER COLUMN execution_profile_hash SET DEFAULT 'bee729c4921a93fd2e229cd610314359ca420610c195ada00a201507bfd7a14c';

UPDATE workflow_definition_versions
SET execution_profile_version = 'legacy-pre-pin/v0',
    execution_profile_hash = 'bee729c4921a93fd2e229cd610314359ca420610c195ada00a201507bfd7a14c';

ALTER TABLE workflow_definition_versions
  ALTER COLUMN execution_profile_version SET NOT NULL,
  ALTER COLUMN execution_profile_hash SET NOT NULL,
  ADD CONSTRAINT workflow_definition_versions_execution_profile_version_check CHECK (
    char_length(btrim(execution_profile_version)) BETWEEN 1 AND 200
  ),
  ADD CONSTRAINT workflow_definition_versions_execution_profile_hash_check CHECK (
    execution_profile_hash ~ '^[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT workflow_definition_versions_execution_profile_content_check CHECK (
    (
      NOT (content ? 'executionProfile')
      AND execution_profile_version = 'legacy-pre-pin/v0'
      AND execution_profile_hash = 'bee729c4921a93fd2e229cd610314359ca420610c195ada00a201507bfd7a14c'
    ) OR (
      content ? 'executionProfile'
      AND jsonb_typeof(content->'executionProfile') = 'object'
      AND content#>>'{executionProfile,version}' = execution_profile_version
      AND content#>>'{executionProfile,hash}' = execution_profile_hash
    )
  ),
  ADD CONSTRAINT workflow_definition_versions_execution_profile_identity_unique UNIQUE (
    id, execution_profile_version, execution_profile_hash
  );

ALTER TABLE workflow_runs
  ADD COLUMN execution_profile_version text,
  ADD COLUMN execution_profile_hash text;

-- Old workers may finish or start only legacy definitions during the expand
-- window. Defaults keep those writes compatible, while the composite FK rejects
-- an old writer that attempts to execute a current-profile definition.
ALTER TABLE workflow_runs
  ALTER COLUMN execution_profile_version SET DEFAULT 'legacy-pre-pin/v0',
  ALTER COLUMN execution_profile_hash SET DEFAULT 'bee729c4921a93fd2e229cd610314359ca420610c195ada00a201507bfd7a14c';

UPDATE workflow_runs AS run
SET execution_profile_version = version.execution_profile_version,
    execution_profile_hash = version.execution_profile_hash
FROM workflow_definition_versions AS version
WHERE version.id = run.definition_version_id;

ALTER TABLE workflow_runs
  ALTER COLUMN execution_profile_version SET NOT NULL,
  ALTER COLUMN execution_profile_hash SET NOT NULL,
  ADD CONSTRAINT workflow_runs_execution_profile_version_check CHECK (
    char_length(btrim(execution_profile_version)) BETWEEN 1 AND 200
  ),
  ADD CONSTRAINT workflow_runs_execution_profile_hash_check CHECK (
    execution_profile_hash ~ '^[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT workflow_runs_definition_execution_profile_fk FOREIGN KEY (
    definition_version_id, execution_profile_version, execution_profile_hash
  ) REFERENCES workflow_definition_versions (
    id, execution_profile_version, execution_profile_hash
  ) ON DELETE RESTRICT ON UPDATE RESTRICT;

CREATE OR REPLACE FUNCTION guard_workflow_definition_execution_profile_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF OLD.definition_id IS DISTINCT FROM NEW.definition_id
     OR OLD.version IS DISTINCT FROM NEW.version
     OR OLD.schema_version IS DISTINCT FROM NEW.schema_version
     OR OLD.content IS DISTINCT FROM NEW.content
     OR OLD.content_hash IS DISTINCT FROM NEW.content_hash
     OR OLD.created_by IS DISTINCT FROM NEW.created_by
     OR OLD.created_at IS DISTINCT FROM NEW.created_at
     OR OLD.execution_profile_version IS DISTINCT FROM NEW.execution_profile_version
     OR OLD.execution_profile_hash IS DISTINCT FROM NEW.execution_profile_hash THEN
    RAISE EXCEPTION 'workflow definition version identity and execution profile are immutable'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER workflow_definition_execution_profile_immutable
BEFORE UPDATE ON workflow_definition_versions
FOR EACH ROW EXECUTE FUNCTION guard_workflow_definition_execution_profile_identity();

CREATE OR REPLACE FUNCTION guard_workflow_run_execution_profile_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF OLD.definition_version_id IS DISTINCT FROM NEW.definition_version_id
     OR OLD.execution_profile_version IS DISTINCT FROM NEW.execution_profile_version
     OR OLD.execution_profile_hash IS DISTINCT FROM NEW.execution_profile_hash THEN
    RAISE EXCEPTION 'workflow run definition and execution profile are immutable'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER workflow_run_execution_profile_immutable
BEFORE UPDATE ON workflow_runs
FOR EACH ROW EXECUTE FUNCTION guard_workflow_run_execution_profile_identity();
