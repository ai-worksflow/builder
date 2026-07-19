CREATE TABLE application_build_contracts (
    id uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    build_manifest_id uuid NOT NULL REFERENCES application_build_manifests(id) ON DELETE RESTRICT,
    build_manifest_hash text NOT NULL CHECK (build_manifest_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
    full_stack_template_id uuid NOT NULL,
    full_stack_template_hash text NOT NULL CHECK (full_stack_template_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
    schema_version text NOT NULL CHECK (schema_version = 'application-build-contract/v2'),
    compiler_version text NOT NULL CHECK (length(trim(compiler_version)) > 0),
    compiler_hash text NOT NULL CHECK (compiler_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
    content_store text NOT NULL DEFAULT 'mongo',
    content_ref text NOT NULL UNIQUE,
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    contract_hash text NOT NULL CHECK (contract_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
    status text NOT NULL CHECK (status IN ('ready', 'blocked', 'superseded')),
    must_count integer NOT NULL CHECK (must_count >= 0),
    must_ready_count integer NOT NULL CHECK (must_ready_count >= 0 AND must_ready_count <= must_count),
    obligation_count integer NOT NULL CHECK (obligation_count >= must_count),
    source_count integer NOT NULL DEFAULT 0 CHECK (source_count >= 0),
    template_release_count integer NOT NULL DEFAULT 0 CHECK (template_release_count >= 0),
    blocking_count integer NOT NULL CHECK (blocking_count >= 0),
    conflict_count integer NOT NULL CHECK (conflict_count >= 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    superseded_at timestamptz,
    creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
    CONSTRAINT application_build_contract_template_fk
        FOREIGN KEY (full_stack_template_id, full_stack_template_hash)
        REFERENCES full_stack_template_releases(id, content_hash) ON DELETE RESTRICT,
    CONSTRAINT application_build_contract_exact_identity_unique
        UNIQUE (id, project_id, build_manifest_id, contract_hash),
    CONSTRAINT application_build_contract_compile_unique
        UNIQUE (project_id, build_manifest_id, full_stack_template_id, full_stack_template_hash, compiler_hash),
    CONSTRAINT application_build_contract_state_shape CHECK (
        (status = 'ready'
            AND must_count > 0
            AND must_ready_count = must_count
            AND blocking_count = 0
            AND conflict_count = 0
            AND superseded_at IS NULL)
        OR
        (status = 'blocked'
            AND (blocking_count > 0 OR conflict_count > 0 OR must_ready_count <> must_count)
            AND superseded_at IS NULL)
        OR
        (status = 'superseded' AND superseded_at IS NOT NULL)
    )
);

CREATE INDEX application_build_contracts_manifest_idx
    ON application_build_contracts (build_manifest_id, created_at DESC);

CREATE INDEX application_build_contracts_project_status_idx
    ON application_build_contracts (project_id, status, created_at DESC);

CREATE TABLE application_build_contract_sources (
    contract_id uuid NOT NULL REFERENCES application_build_contracts(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    source_kind text NOT NULL CHECK (length(trim(source_kind)) > 0),
    purpose text NOT NULL CHECK (length(trim(purpose)) > 0),
    required boolean NOT NULL,
    artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
    revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
    content_hash text NOT NULL CHECK (content_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
    PRIMARY KEY (contract_id, ordinal),
    CONSTRAINT application_build_contract_source_exact_unique
        UNIQUE (contract_id, artifact_id, revision_id, content_hash)
);

CREATE INDEX application_build_contract_sources_revision_idx
    ON application_build_contract_sources (revision_id, contract_id);

CREATE TABLE application_build_contract_template_releases (
    contract_id uuid NOT NULL REFERENCES application_build_contracts(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    role text NOT NULL CHECK (role IN ('web', 'api', 'worker')),
    template_release_id uuid NOT NULL,
    template_release_content_hash text NOT NULL CHECK (template_release_content_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
    PRIMARY KEY (contract_id, ordinal),
    CONSTRAINT application_build_contract_template_role_unique UNIQUE (contract_id, role),
    CONSTRAINT application_build_contract_release_fk
        FOREIGN KEY (template_release_id, template_release_content_hash)
        REFERENCES template_releases(id, content_hash) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION validate_application_build_contract_template_release()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    contract_creation_transaction_id bigint;
BEGIN
    SELECT creation_transaction_id INTO contract_creation_transaction_id
    FROM application_build_contracts
    WHERE id = NEW.contract_id;

    IF contract_creation_transaction_id IS NULL
       OR contract_creation_transaction_id <> txid_current() THEN
        RAISE EXCEPTION 'Application Build Contract is sealed; Template Release projections cannot be appended'
            USING ERRCODE = '55000';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM application_build_contracts AS contract
        JOIN full_stack_template_components AS component
          ON component.full_stack_template_id = contract.full_stack_template_id
         AND component.full_stack_content_hash = contract.full_stack_template_hash
         AND component.role = NEW.role
         AND component.template_release_id = NEW.template_release_id
         AND component.template_release_content_hash = NEW.template_release_content_hash
        JOIN template_release_policies AS policy
          ON policy.template_release_id = component.template_release_id
         AND policy.release_content_hash = component.template_release_content_hash
         AND policy.state = 'approved'
        WHERE contract.id = NEW.contract_id
    ) THEN
        RAISE EXCEPTION 'Application Build Contract Template Release is not an approved exact component of its FullStackTemplate'
            USING ERRCODE = '23503';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER application_build_contract_template_release_guard
BEFORE INSERT ON application_build_contract_template_releases
FOR EACH ROW EXECUTE FUNCTION validate_application_build_contract_template_release();

CREATE TABLE application_build_contract_obligations (
    contract_id uuid NOT NULL REFERENCES application_build_contracts(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    obligation_id text NOT NULL CHECK (length(trim(obligation_id)) > 0),
    level text NOT NULL CHECK (level IN ('must', 'should')),
    kind text NOT NULL CHECK (length(trim(kind)) > 0),
    source_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
    source_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
    source_content_hash text NOT NULL CHECK (source_content_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
    source_anchor_id text NOT NULL CHECK (length(trim(source_anchor_id)) > 0),
    oracle_ids jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(oracle_ids) = 'array'),
    depends_on jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(depends_on) = 'array'),
    waivable boolean NOT NULL DEFAULT false,
    status text NOT NULL CHECK (status IN ('ready', 'blocked', 'waived')),
    blocking_reason_id text,
    PRIMARY KEY (contract_id, obligation_id),
    CONSTRAINT application_build_contract_obligation_shape CHECK (
        (status = 'ready' AND jsonb_array_length(oracle_ids) > 0 AND blocking_reason_id IS NULL)
        OR (status = 'blocked' AND blocking_reason_id IS NOT NULL)
        OR (status = 'waived' AND waivable = true AND blocking_reason_id IS NULL)
    )
);

CREATE OR REPLACE FUNCTION validate_application_build_contract_parent()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    -- The transaction that creates the parent is the only write window for its
    -- child projections. The values supplied by a caller are intentionally
    -- ignored: PostgreSQL derives and seals the projection counts itself.
    NEW.source_count := 0;
    NEW.template_release_count := 0;
    NEW.creation_transaction_id := txid_current();

    IF NOT EXISTS (
        SELECT 1
        FROM application_build_manifests AS manifest
        WHERE manifest.id = NEW.build_manifest_id
          AND manifest.project_id = NEW.project_id
          AND manifest.manifest_hash = NEW.build_manifest_hash
          AND manifest.status = 'frozen'
    ) THEN
        RAISE EXCEPTION 'Application Build Contract does not reference an exact frozen same-project Build Manifest'
            USING ERRCODE = '23503';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM full_stack_template_releases AS template
        WHERE template.id = NEW.full_stack_template_id
          AND template.content_hash = NEW.full_stack_template_hash
          AND EXISTS (
              SELECT 1
              FROM full_stack_template_components AS component
              WHERE component.full_stack_template_id = template.id
                AND component.full_stack_content_hash = template.content_hash
                AND component.role = 'web'
          )
          AND EXISTS (
              SELECT 1
              FROM full_stack_template_components AS component
              WHERE component.full_stack_template_id = template.id
                AND component.full_stack_content_hash = template.content_hash
                AND component.role = 'api'
          )
          AND NOT EXISTS (
              SELECT 1
              FROM full_stack_template_components AS component
              LEFT JOIN template_release_policies AS policy
                ON policy.template_release_id = component.template_release_id
               AND policy.release_content_hash = component.template_release_content_hash
              WHERE component.full_stack_template_id = template.id
                AND component.full_stack_content_hash = template.content_hash
                AND (policy.template_release_id IS NULL OR policy.state <> 'approved')
          )
    ) THEN
        RAISE EXCEPTION 'Application Build Contract does not reference an active approved FullStackTemplate'
            USING ERRCODE = '23503';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER application_build_contract_parent_guard
BEFORE INSERT ON application_build_contracts
FOR EACH ROW EXECUTE FUNCTION validate_application_build_contract_parent();

CREATE OR REPLACE FUNCTION validate_application_build_contract_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    contract_project_id uuid;
    contract_creation_transaction_id bigint;
BEGIN
    SELECT project_id, creation_transaction_id
      INTO contract_project_id, contract_creation_transaction_id
    FROM application_build_contracts
    WHERE id = NEW.contract_id;

    IF contract_creation_transaction_id IS NULL
       OR contract_creation_transaction_id <> txid_current() THEN
        RAISE EXCEPTION 'Application Build Contract is sealed; source projections cannot be appended'
            USING ERRCODE = '55000';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM artifacts AS artifact
        JOIN artifact_revisions AS revision ON revision.artifact_id = artifact.id
        WHERE artifact.id = NEW.artifact_id
          AND artifact.project_id = contract_project_id
          AND artifact.kind = NEW.source_kind
          AND revision.id = NEW.revision_id
          AND revision.content_hash = NEW.content_hash
          AND revision.workflow_status = 'approved'
    ) THEN
        RAISE EXCEPTION 'Application Build Contract source is not an exact approved same-project revision'
            USING ERRCODE = '23503';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER application_build_contract_source_guard
BEFORE INSERT ON application_build_contract_sources
FOR EACH ROW EXECUTE FUNCTION validate_application_build_contract_source();

CREATE OR REPLACE FUNCTION validate_application_build_contract_obligation_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    contract_project_id uuid;
    contract_creation_transaction_id bigint;
BEGIN
    SELECT project_id, creation_transaction_id
      INTO contract_project_id, contract_creation_transaction_id
    FROM application_build_contracts
    WHERE id = NEW.contract_id;

    IF contract_creation_transaction_id IS NULL
       OR contract_creation_transaction_id <> txid_current() THEN
        RAISE EXCEPTION 'Application Build Contract is sealed; obligation projections cannot be appended'
            USING ERRCODE = '55000';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM artifacts AS artifact
        JOIN artifact_revisions AS revision ON revision.artifact_id = artifact.id
        WHERE artifact.id = NEW.source_artifact_id
          AND artifact.project_id = contract_project_id
          AND revision.id = NEW.source_revision_id
          AND revision.content_hash = NEW.source_content_hash
          AND revision.workflow_status = 'approved'
    ) THEN
        RAISE EXCEPTION 'Application Build Contract obligation is not anchored to an exact approved same-project revision'
            USING ERRCODE = '23503';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER application_build_contract_obligation_source_guard
BEFORE INSERT ON application_build_contract_obligations
FOR EACH ROW EXECUTE FUNCTION validate_application_build_contract_obligation_source();

CREATE OR REPLACE FUNCTION increment_application_build_contract_projection_count()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_TABLE_NAME = 'application_build_contract_sources' THEN
        UPDATE application_build_contracts
        SET source_count = source_count + 1
        WHERE id = NEW.contract_id
          AND creation_transaction_id = txid_current();
    ELSIF TG_TABLE_NAME = 'application_build_contract_template_releases' THEN
        UPDATE application_build_contracts
        SET template_release_count = template_release_count + 1
        WHERE id = NEW.contract_id
          AND creation_transaction_id = txid_current();
    ELSE
        RAISE EXCEPTION 'unsupported Application Build Contract projection table'
            USING ERRCODE = '55000';
    END IF;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'Application Build Contract is sealed; projection counts cannot change'
            USING ERRCODE = '55000';
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER application_build_contract_source_count
AFTER INSERT ON application_build_contract_sources
FOR EACH ROW EXECUTE FUNCTION increment_application_build_contract_projection_count();

CREATE TRIGGER application_build_contract_template_release_count
AFTER INSERT ON application_build_contract_template_releases
FOR EACH ROW EXECUTE FUNCTION increment_application_build_contract_projection_count();

CREATE OR REPLACE FUNCTION validate_application_build_contract_projection()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_id uuid;
    projected_obligations integer;
    projected_must integer;
    projected_ready_must integer;
    projected_sources integer;
    projected_templates integer;
    expected_templates integer;
    minimum_source_ordinal integer;
    maximum_source_ordinal integer;
    minimum_template_ordinal integer;
    maximum_template_ordinal integer;
BEGIN
    IF TG_TABLE_NAME = 'application_build_contracts' THEN
        target_id := NEW.id;
    ELSE
        target_id := NEW.contract_id;
    END IF;

    SELECT count(*),
           count(*) FILTER (WHERE level = 'must'),
           count(*) FILTER (WHERE level = 'must' AND status IN ('ready', 'waived'))
      INTO projected_obligations, projected_must, projected_ready_must
    FROM application_build_contract_obligations
    WHERE contract_id = target_id;

    SELECT count(*), min(ordinal), max(ordinal)
      INTO projected_sources, minimum_source_ordinal, maximum_source_ordinal
    FROM application_build_contract_sources
    WHERE contract_id = target_id;

    SELECT count(*), min(ordinal), max(ordinal)
      INTO projected_templates, minimum_template_ordinal, maximum_template_ordinal
    FROM application_build_contract_template_releases
    WHERE contract_id = target_id;

    SELECT count(*) INTO expected_templates
    FROM application_build_contracts AS contract
    JOIN full_stack_template_components AS component
      ON component.full_stack_template_id = contract.full_stack_template_id
     AND component.full_stack_content_hash = contract.full_stack_template_hash
    WHERE contract.id = target_id;

    IF EXISTS (
        SELECT 1
        FROM application_build_contracts AS contract
        WHERE contract.id = target_id
          AND (
              contract.obligation_count <> projected_obligations
              OR contract.must_count <> projected_must
              OR contract.must_ready_count <> projected_ready_must
              OR contract.source_count <> projected_sources
              OR contract.template_release_count <> projected_templates
              OR projected_sources = 0
              OR minimum_source_ordinal <> 0
              OR maximum_source_ordinal + 1 <> projected_sources
              OR projected_templates <> expected_templates
              OR minimum_template_ordinal <> 0
              OR maximum_template_ordinal + 1 <> projected_templates
          )
    ) THEN
        RAISE EXCEPTION 'Application Build Contract child projections do not match immutable content'
            USING ERRCODE = '23514';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER application_build_contract_projection_parent_guard
AFTER INSERT ON application_build_contracts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_application_build_contract_projection();

CREATE CONSTRAINT TRIGGER application_build_contract_projection_source_guard
AFTER INSERT ON application_build_contract_sources
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_application_build_contract_projection();

CREATE CONSTRAINT TRIGGER application_build_contract_projection_template_guard
AFTER INSERT ON application_build_contract_template_releases
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_application_build_contract_projection();

CREATE CONSTRAINT TRIGGER application_build_contract_projection_obligation_guard
AFTER INSERT ON application_build_contract_obligations
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_application_build_contract_projection();

CREATE OR REPLACE FUNCTION prevent_application_build_contract_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'Application Build Contract history is immutable'
            USING ERRCODE = '55000';
    END IF;

    -- Count maintenance is an internal part of the creation transaction. It is
    -- permitted only one row at a time and is checked again by deferred
    -- projection constraints before commit.
    IF OLD.creation_transaction_id = txid_current()
       AND NEW.creation_transaction_id = OLD.creation_transaction_id
       AND (
           (NEW.source_count = OLD.source_count + 1
               AND NEW.template_release_count = OLD.template_release_count)
           OR
           (NEW.source_count = OLD.source_count
               AND NEW.template_release_count = OLD.template_release_count + 1)
       )
       AND ROW(
           NEW.id, NEW.project_id, NEW.build_manifest_id, NEW.build_manifest_hash,
           NEW.full_stack_template_id, NEW.full_stack_template_hash,
           NEW.schema_version, NEW.compiler_version, NEW.compiler_hash,
           NEW.content_store, NEW.content_ref, NEW.content_hash, NEW.contract_hash,
           NEW.status, NEW.must_count, NEW.must_ready_count, NEW.obligation_count,
           NEW.blocking_count, NEW.conflict_count, NEW.version,
           NEW.created_by, NEW.created_at, NEW.superseded_at
       ) IS NOT DISTINCT FROM ROW(
           OLD.id, OLD.project_id, OLD.build_manifest_id, OLD.build_manifest_hash,
           OLD.full_stack_template_id, OLD.full_stack_template_hash,
           OLD.schema_version, OLD.compiler_version, OLD.compiler_hash,
           OLD.content_store, OLD.content_ref, OLD.content_hash, OLD.contract_hash,
           OLD.status, OLD.must_count, OLD.must_ready_count, OLD.obligation_count,
           OLD.blocking_count, OLD.conflict_count, OLD.version,
           OLD.created_by, OLD.created_at, OLD.superseded_at
       )
    THEN
        RETURN NEW;
    END IF;

    IF OLD.status IN ('ready', 'blocked')
       AND NEW.status = 'superseded'
       AND NEW.superseded_at IS NOT NULL
       AND NEW.version = OLD.version + 1
       AND ROW(
           NEW.id, NEW.project_id, NEW.build_manifest_id, NEW.build_manifest_hash,
           NEW.full_stack_template_id, NEW.full_stack_template_hash,
           NEW.schema_version, NEW.compiler_version, NEW.compiler_hash,
           NEW.content_store, NEW.content_ref, NEW.content_hash, NEW.contract_hash,
           NEW.must_count, NEW.must_ready_count, NEW.obligation_count,
           NEW.source_count, NEW.template_release_count,
           NEW.blocking_count, NEW.conflict_count, NEW.created_by, NEW.created_at,
           NEW.creation_transaction_id
       ) IS NOT DISTINCT FROM ROW(
           OLD.id, OLD.project_id, OLD.build_manifest_id, OLD.build_manifest_hash,
           OLD.full_stack_template_id, OLD.full_stack_template_hash,
           OLD.schema_version, OLD.compiler_version, OLD.compiler_hash,
           OLD.content_store, OLD.content_ref, OLD.content_hash, OLD.contract_hash,
           OLD.must_count, OLD.must_ready_count, OLD.obligation_count,
           OLD.source_count, OLD.template_release_count,
           OLD.blocking_count, OLD.conflict_count, OLD.created_by, OLD.created_at,
           OLD.creation_transaction_id
       )
    THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'Application Build Contract content and projections are immutable'
        USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER application_build_contract_immutable
BEFORE UPDATE OR DELETE ON application_build_contracts
FOR EACH ROW EXECUTE FUNCTION prevent_application_build_contract_mutation();

CREATE OR REPLACE FUNCTION prevent_application_build_contract_child_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'Application Build Contract child projections are immutable'
        USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER application_build_contract_source_immutable
BEFORE UPDATE OR DELETE ON application_build_contract_sources
FOR EACH ROW EXECUTE FUNCTION prevent_application_build_contract_child_mutation();

CREATE TRIGGER application_build_contract_template_immutable
BEFORE UPDATE OR DELETE ON application_build_contract_template_releases
FOR EACH ROW EXECUTE FUNCTION prevent_application_build_contract_child_mutation();

CREATE TRIGGER application_build_contract_obligation_immutable
BEFORE UPDATE OR DELETE ON application_build_contract_obligations
FOR EACH ROW EXECUTE FUNCTION prevent_application_build_contract_child_mutation();
