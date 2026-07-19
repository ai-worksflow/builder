-- Removing the authority-kind projection from any audited registry would make
-- Genesis indistinguishable from ordinary activation. Refuse that rollback.
DO $model_governance_genesis_rollback_fence$
BEGIN
  -- Match Genesis/activation writer order to avoid an inverse-lock deadlock.
  LOCK TABLE model_governance_activation_heads IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE model_governance_revocation_anchor IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE model_governance_activation_records IN ACCESS EXCLUSIVE MODE;
  IF EXISTS (SELECT 1 FROM model_governance_activation_records)
     OR EXISTS (SELECT 1 FROM model_governance_activation_heads)
     OR EXISTS (
       SELECT 1 FROM model_governance_revocation_anchor
       WHERE current_trust_policy_hash IS NOT NULL
     ) THEN
    RAISE EXCEPTION 'cannot roll back signed Model Governance Genesis while activation audit state is nonempty'
      USING ERRCODE = '55000';
  END IF;
END;
$model_governance_genesis_rollback_fence$;

DROP FUNCTION IF EXISTS append_model_governance_genesis(
  uuid, text, text, uuid, text, text, text, text, text, bigint, bigint,
  text, text, text, text, text, text, text, text, text, text, bigint,
  text, bigint, timestamptz, text
);
DROP FUNCTION IF EXISTS observe_model_governance_trust_policy(text, text, bigint);
DROP TRIGGER IF EXISTS model_governance_activation_authority_anchor
  ON model_governance_activation_records;
DROP FUNCTION IF EXISTS enforce_model_governance_activation_authority_anchor();

ALTER TABLE model_governance_activation_records
  DROP CONSTRAINT IF EXISTS model_governance_activation_authority_kind_closed,
  DROP COLUMN IF EXISTS initial_revocation_authority_epoch,
  DROP COLUMN IF EXISTS initial_revocation_authority_hash,
  DROP COLUMN IF EXISTS initial_revocation_authority_id,
  DROP COLUMN IF EXISTS genesis_payload_digest,
  DROP COLUMN IF EXISTS genesis_envelope_digest,
  DROP COLUMN IF EXISTS authority_kind;

ALTER TABLE model_governance_revocation_anchor
  DROP CONSTRAINT IF EXISTS model_governance_trust_policy_anchor_closed,
  DROP COLUMN IF EXISTS trust_policy_observed_at,
  DROP COLUMN IF EXISTS trust_policy_revocation_epoch,
  DROP COLUMN IF EXISTS trust_policy_revocation_hash,
  DROP COLUMN IF EXISTS current_trust_policy_hash;
