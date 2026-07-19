DROP FUNCTION IF EXISTS acquire_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, bigint, integer, integer, bigint, integer
);

-- New-format claims cannot be represented by the 000063 function. Fence them
-- before restoring that exact predecessor contract.
DELETE FROM repository_exact_tree_literal_index_build_claims;

DROP INDEX IF EXISTS repository_exact_tree_literal_index_build_claim_project_quota_idx;

ALTER TABLE repository_exact_tree_literal_index_build_claims
  DROP COLUMN IF EXISTS reserved_source_bytes;

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

REVOKE ALL ON FUNCTION acquire_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, integer
) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION acquire_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, integer
) TO PUBLIC;
