-- Fence every writer before observing emptiness. A migration-owner rollback
-- is not authority to destroy immutable governance history.
DO $model_governance_activation_store_rollback_fence$
BEGIN
  -- Match Genesis/activation writer order to avoid an inverse-lock deadlock.
  LOCK TABLE model_governance_activation_heads IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE model_governance_revocation_anchor IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE model_governance_activation_records IN ACCESS EXCLUSIVE MODE;
  IF EXISTS (SELECT 1 FROM model_governance_activation_records)
     OR EXISTS (SELECT 1 FROM model_governance_activation_heads)
     OR EXISTS (SELECT 1 FROM model_governance_revocation_anchor) THEN
    RAISE EXCEPTION 'cannot roll back Model Governance activation store while immutable audit state is nonempty'
      USING ERRCODE = '55000';
  END IF;
END;
$model_governance_activation_store_rollback_fence$;

-- Drop entrypoints before their row-type dependencies, then triggers and
-- relations in foreign-key-safe order.
DROP FUNCTION IF EXISTS append_model_governance_activation(
  bigint, text, uuid, text, text, uuid, text, text, text, text, text,
  bigint, bigint, text, text, text, text, text, text, text, timestamptz
);
DROP FUNCTION IF EXISTS observe_model_governance_revocation_authority(
  bigint, text, bytea, jsonb
);
DROP TRIGGER IF EXISTS model_governance_revocation_anchor_no_delete
  ON model_governance_revocation_anchor;
DROP TRIGGER IF EXISTS model_governance_activation_heads_no_delete
  ON model_governance_activation_heads;
DROP TRIGGER IF EXISTS model_governance_activation_records_immutable
  ON model_governance_activation_records;
DROP TABLE IF EXISTS model_governance_activation_heads;
DROP TABLE IF EXISTS model_governance_activation_records;
DROP TABLE IF EXISTS model_governance_revocation_anchor;
DROP FUNCTION IF EXISTS reject_model_governance_immutable_mutation();
