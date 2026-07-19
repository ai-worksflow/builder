-- Durable pre-resolve single-builder claims prevent concurrent callers on
-- different API instances from each hydrating and copying the same exact tree.
-- The claim is coordination state only; the immutable ready index remains the
-- publication authority introduced by 000062.
CREATE TABLE repository_exact_tree_literal_index_build_claims (
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  tree_hash text NOT NULL CHECK (tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  owner_token uuid NOT NULL,
  attempt bigint NOT NULL CHECK (attempt > 0),
  acquired_at timestamptz NOT NULL,
  renewed_at timestamptz NOT NULL,
  lease_expires_at timestamptz NOT NULL,
  PRIMARY KEY (project_id, tree_hash),
  CONSTRAINT repository_exact_tree_literal_index_build_claim_time_shape CHECK (
    acquired_at <= renewed_at
    AND lease_expires_at > renewed_at
    AND lease_expires_at <= renewed_at + interval '5 minutes'
  )
);

CREATE INDEX repository_exact_tree_literal_index_build_claim_expiry_idx
  ON repository_exact_tree_literal_index_build_claims (
    lease_expires_at, project_id, tree_hash
  );

CREATE OR REPLACE FUNCTION acquire_repository_exact_tree_literal_index_build_claim(
  target_project_id uuid,
  target_tree_hash text,
  target_owner_token uuid,
  ttl_milliseconds integer
)
RETURNS TABLE (
  acquired boolean,
  current_owner_token uuid,
  current_attempt bigint,
  current_lease_expires_at timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
  observed_at timestamptz := clock_timestamp();
BEGIN
  IF target_project_id IS NULL
     OR target_owner_token IS NULL
     OR target_tree_hash !~ '^sha256:[0-9a-f]{64}$'
     OR ttl_milliseconds IS NULL
     OR ttl_milliseconds < 50
     OR ttl_milliseconds > 300000 THEN
    RAISE EXCEPTION 'Exact-tree literal index build claim input is invalid'
      USING ERRCODE = '22023';
  END IF;

  INSERT INTO repository_exact_tree_literal_index_build_claims AS claim (
    project_id, tree_hash, owner_token, attempt,
    acquired_at, renewed_at, lease_expires_at
  ) VALUES (
    target_project_id, target_tree_hash, target_owner_token, 1,
    observed_at, observed_at,
    observed_at + make_interval(secs => ttl_milliseconds::double precision / 1000.0)
  )
  ON CONFLICT (project_id, tree_hash) DO UPDATE
  SET owner_token = EXCLUDED.owner_token,
      attempt = CASE
        WHEN claim.owner_token = target_owner_token
         AND claim.lease_expires_at > observed_at
          THEN claim.attempt
        ELSE claim.attempt + 1
      END,
      acquired_at = CASE
        WHEN claim.owner_token = target_owner_token
         AND claim.lease_expires_at > observed_at
          THEN claim.acquired_at
        ELSE observed_at
      END,
      renewed_at = observed_at,
      lease_expires_at = observed_at
        + make_interval(secs => ttl_milliseconds::double precision / 1000.0)
  WHERE claim.owner_token = target_owner_token
     OR claim.lease_expires_at <= observed_at
  RETURNING true, owner_token, attempt, lease_expires_at
  INTO acquired, current_owner_token, current_attempt, current_lease_expires_at;

  IF FOUND THEN
    RETURN NEXT;
    RETURN;
  END IF;

  RETURN QUERY
  SELECT false, claim.owner_token, claim.attempt, claim.lease_expires_at
  FROM repository_exact_tree_literal_index_build_claims AS claim
  WHERE claim.project_id = target_project_id
    AND claim.tree_hash = target_tree_hash;
END;
$$;

CREATE OR REPLACE FUNCTION renew_repository_exact_tree_literal_index_build_claim(
  target_project_id uuid,
  target_tree_hash text,
  target_owner_token uuid,
  target_attempt bigint,
  ttl_milliseconds integer
)
RETURNS TABLE (
  renewed boolean,
  current_lease_expires_at timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
  observed_at timestamptz := clock_timestamp();
BEGIN
  IF target_project_id IS NULL
     OR target_owner_token IS NULL
     OR target_tree_hash !~ '^sha256:[0-9a-f]{64}$'
     OR target_attempt IS NULL
     OR target_attempt <= 0
     OR ttl_milliseconds IS NULL
     OR ttl_milliseconds < 50
     OR ttl_milliseconds > 300000 THEN
    RAISE EXCEPTION 'Exact-tree literal index build claim renewal input is invalid'
      USING ERRCODE = '22023';
  END IF;

  UPDATE repository_exact_tree_literal_index_build_claims AS claim
  SET renewed_at = observed_at,
      lease_expires_at = observed_at
        + make_interval(secs => ttl_milliseconds::double precision / 1000.0)
  WHERE claim.project_id = target_project_id
    AND claim.tree_hash = target_tree_hash
    AND claim.owner_token = target_owner_token
    AND claim.attempt = target_attempt
    AND claim.lease_expires_at > observed_at
  RETURNING true, lease_expires_at
  INTO renewed, current_lease_expires_at;

  IF FOUND THEN
    RETURN NEXT;
    RETURN;
  END IF;

  renewed := false;
  current_lease_expires_at := NULL;
  RETURN NEXT;
END;
$$;

CREATE OR REPLACE FUNCTION release_repository_exact_tree_literal_index_build_claim(
  target_project_id uuid,
  target_tree_hash text,
  target_owner_token uuid,
  target_attempt bigint
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
  released boolean;
BEGIN
  IF target_project_id IS NULL
     OR target_owner_token IS NULL
     OR target_tree_hash !~ '^sha256:[0-9a-f]{64}$'
     OR target_attempt IS NULL
     OR target_attempt <= 0 THEN
    RAISE EXCEPTION 'Exact-tree literal index build claim release input is invalid'
      USING ERRCODE = '22023';
  END IF;

  DELETE FROM repository_exact_tree_literal_index_build_claims AS claim
  WHERE claim.project_id = target_project_id
    AND claim.tree_hash = target_tree_hash
    AND claim.owner_token = target_owner_token
    AND claim.attempt = target_attempt;
  released := FOUND;
  RETURN released;
END;
$$;

COMMENT ON TABLE repository_exact_tree_literal_index_build_claims IS
  'Tenant-scoped expiring single-builder coordination acquired before any exact-tree FileBlob resolution. Expired owners are fenced by owner token plus attempt.';

REVOKE INSERT, UPDATE, DELETE, TRUNCATE
  ON repository_exact_tree_literal_index_build_claims FROM PUBLIC;
REVOKE ALL ON FUNCTION acquire_repository_exact_tree_literal_index_build_claim(uuid, text, uuid, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION renew_repository_exact_tree_literal_index_build_claim(uuid, text, uuid, bigint, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION release_repository_exact_tree_literal_index_build_claim(uuid, text, uuid, bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION acquire_repository_exact_tree_literal_index_build_claim(uuid, text, uuid, integer) TO PUBLIC;
GRANT EXECUTE ON FUNCTION renew_repository_exact_tree_literal_index_build_claim(uuid, text, uuid, bigint, integer) TO PUBLIC;
GRANT EXECUTE ON FUNCTION release_repository_exact_tree_literal_index_build_claim(uuid, text, uuid, bigint) TO PUBLIC;
