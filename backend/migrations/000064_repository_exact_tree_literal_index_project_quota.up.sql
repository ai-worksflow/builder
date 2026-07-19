-- Logical project quota is reserved by the same durable claim that fences a
-- single exact-tree builder. Existing claims were created without a source
-- reservation and cannot be trusted for quota accounting, so fence them at
-- this deployment boundary. Their owners will fail their next renewal or
-- publication and a current process can safely retry the immutable build.
DROP FUNCTION acquire_repository_exact_tree_literal_index_build_claim(uuid, text, uuid, integer);

DELETE FROM repository_exact_tree_literal_index_build_claims;

ALTER TABLE repository_exact_tree_literal_index_build_claims
  ADD COLUMN reserved_source_bytes bigint NOT NULL
  CONSTRAINT repository_exact_tree_literal_index_build_claim_reserved_bytes_check
  CHECK (reserved_source_bytes BETWEEN 0 AND 67108864);

CREATE INDEX repository_exact_tree_literal_index_build_claim_project_quota_idx
  ON repository_exact_tree_literal_index_build_claims (
    project_id, lease_expires_at, tree_hash
  ) INCLUDE (reserved_source_bytes);

CREATE OR REPLACE FUNCTION acquire_repository_exact_tree_literal_index_build_claim(
  target_project_id uuid,
  target_tree_hash text,
  target_owner_token uuid,
  target_source_bytes bigint,
  ttl_milliseconds integer,
  max_project_trees integer,
  max_project_source_bytes bigint,
  max_project_active_builds integer
)
RETURNS TABLE (
  decision text,
  current_owner_token uuid,
  current_attempt bigint,
  current_reserved_source_bytes bigint,
  current_lease_expires_at timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
  observed_at timestamptz := clock_timestamp();
  existing_claim repository_exact_tree_literal_index_build_claims%ROWTYPE;
  project_tree_count bigint;
  project_source_bytes bigint;
  project_active_build_count bigint;
BEGIN
  IF target_project_id IS NULL
     OR target_owner_token IS NULL
     OR target_tree_hash !~ '^sha256:[0-9a-f]{64}$'
     OR target_source_bytes IS NULL
     OR target_source_bytes < 0
     OR target_source_bytes > 67108864
     OR ttl_milliseconds IS NULL
     OR ttl_milliseconds < 50
     OR ttl_milliseconds > 300000
     OR max_project_trees IS NULL
     OR max_project_trees < 1
     OR max_project_trees > 10000
     OR max_project_source_bytes IS NULL
     OR max_project_source_bytes < 1
     OR max_project_source_bytes > 1099511627776
     OR max_project_active_builds IS NULL
     OR max_project_active_builds < 1
     OR max_project_active_builds > max_project_trees THEN
    RAISE EXCEPTION 'Exact-tree literal index quota claim input is invalid'
      USING ERRCODE = '22023';
  END IF;

  -- Different trees of one project must not each observe the same final quota
  -- slot. This transaction-scoped project lock is held only for accounting and
  -- claim mutation, never while source bytes are resolved.
  PERFORM pg_advisory_xact_lock(hashtextextended(
    target_project_id::text || '|repository-exact-tree-literal-index-project-quota',
    0
  ));

  SELECT claim.*
  INTO existing_claim
  FROM repository_exact_tree_literal_index_build_claims AS claim
  WHERE claim.project_id = target_project_id
    AND claim.tree_hash = target_tree_hash
  FOR UPDATE;

  IF FOUND AND existing_claim.lease_expires_at > observed_at THEN
    IF existing_claim.reserved_source_bytes <> target_source_bytes THEN
      RAISE EXCEPTION 'Exact-tree literal index claim source reservation conflicts with the same tree'
        USING ERRCODE = '55000';
    END IF;

    IF existing_claim.owner_token = target_owner_token THEN
      UPDATE repository_exact_tree_literal_index_build_claims AS claim
      SET renewed_at = observed_at,
          lease_expires_at = observed_at
            + make_interval(secs => ttl_milliseconds::double precision / 1000.0)
      WHERE claim.project_id = target_project_id
        AND claim.tree_hash = target_tree_hash
        AND claim.owner_token = target_owner_token
        AND claim.attempt = existing_claim.attempt
        AND claim.lease_expires_at > observed_at
      RETURNING
        'acquired'::text,
        claim.owner_token,
        claim.attempt,
        claim.reserved_source_bytes,
        claim.lease_expires_at
      INTO
        decision,
        current_owner_token,
        current_attempt,
        current_reserved_source_bytes,
        current_lease_expires_at;
      IF NOT FOUND THEN
        RAISE EXCEPTION 'Exact-tree literal index live claim changed while quota locked'
          USING ERRCODE = '55000';
      END IF;
      RETURN NEXT;
      RETURN;
    END IF;

    decision := 'waiting';
    current_owner_token := existing_claim.owner_token;
    current_attempt := existing_claim.attempt;
    current_reserved_source_bytes := existing_claim.reserved_source_bytes;
    current_lease_expires_at := existing_claim.lease_expires_at;
    RETURN NEXT;
    RETURN;
  END IF;

  SELECT
    count(*)::bigint,
    COALESCE(sum(usage.source_bytes), 0)::bigint,
    count(*) FILTER (WHERE usage.active_build)::bigint
  INTO project_tree_count, project_source_bytes, project_active_build_count
  FROM (
    SELECT
      manifest.tree_hash,
      manifest.total_bytes AS source_bytes,
      false AS active_build
    FROM repository_exact_tree_literal_index_manifests AS manifest
    WHERE manifest.project_id = target_project_id
      AND manifest.status = 'ready'

    UNION ALL

    SELECT
      claim.tree_hash,
      claim.reserved_source_bytes AS source_bytes,
      true AS active_build
    FROM repository_exact_tree_literal_index_build_claims AS claim
    WHERE claim.project_id = target_project_id
      AND claim.lease_expires_at > observed_at
      AND NOT EXISTS (
        SELECT 1
        FROM repository_exact_tree_literal_index_manifests AS manifest
        WHERE manifest.project_id = claim.project_id
          AND manifest.tree_hash = claim.tree_hash
          AND manifest.status = 'ready'
      )
  ) AS usage;

  IF project_tree_count >= max_project_trees THEN
    decision := 'quota_trees';
    RETURN NEXT;
    RETURN;
  END IF;
  IF project_source_bytes > max_project_source_bytes - target_source_bytes THEN
    decision := 'quota_source_bytes';
    RETURN NEXT;
    RETURN;
  END IF;
  IF project_active_build_count >= max_project_active_builds THEN
    decision := 'quota_active_builds';
    RETURN NEXT;
    RETURN;
  END IF;

  INSERT INTO repository_exact_tree_literal_index_build_claims AS claim (
    project_id, tree_hash, owner_token, attempt, reserved_source_bytes,
    acquired_at, renewed_at, lease_expires_at
  ) VALUES (
    target_project_id, target_tree_hash, target_owner_token, 1, target_source_bytes,
    observed_at, observed_at,
    observed_at + make_interval(secs => ttl_milliseconds::double precision / 1000.0)
  )
  ON CONFLICT (project_id, tree_hash) DO UPDATE
  SET owner_token = EXCLUDED.owner_token,
      attempt = claim.attempt + 1,
      reserved_source_bytes = EXCLUDED.reserved_source_bytes,
      acquired_at = observed_at,
      renewed_at = observed_at,
      lease_expires_at = observed_at
        + make_interval(secs => ttl_milliseconds::double precision / 1000.0)
  WHERE claim.lease_expires_at <= observed_at
  RETURNING
    'acquired'::text,
    claim.owner_token,
    claim.attempt,
    claim.reserved_source_bytes,
    claim.lease_expires_at
  INTO
    decision,
    current_owner_token,
    current_attempt,
    current_reserved_source_bytes,
    current_lease_expires_at;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'Exact-tree literal index quota claim was not acquired after project serialization'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEXT;
END;
$$;

COMMENT ON COLUMN repository_exact_tree_literal_index_build_claims.reserved_source_bytes IS
  'Logical exact-tree source bytes atomically reserved before any FileBlob resolution; expired claims cease consuming project quota.';
COMMENT ON FUNCTION acquire_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, bigint, integer, integer, bigint, integer
) IS
  'Atomically accounts ready manifests plus live claims per project and returns distinct tree, source-byte, or active-build quota decisions.';

REVOKE ALL ON FUNCTION acquire_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, bigint, integer, integer, bigint, integer
) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION acquire_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, bigint, integer, integer, bigint, integer
) TO PUBLIC;
