DROP TRIGGER IF EXISTS implementation_proposal_build_contract_guard ON implementation_proposals;
DROP TRIGGER IF EXISTS implementation_generation_build_contract_guard ON implementation_generation_claims;
DROP FUNCTION IF EXISTS guard_implementation_build_contract_binding();
DROP FUNCTION IF EXISTS exact_application_build_contract_is_ready(uuid, text, uuid, uuid);

DROP INDEX IF EXISTS implementation_proposal_build_contract_idx;
DROP INDEX IF EXISTS implementation_generation_build_contract_idx;

ALTER TABLE implementation_proposals
  DROP CONSTRAINT IF EXISTS implementation_proposal_build_contract_exact_fk,
  DROP CONSTRAINT IF EXISTS implementation_proposal_build_contract_shape,
  DROP COLUMN IF EXISTS application_build_contract_hash,
  DROP COLUMN IF EXISTS application_build_contract_id;

ALTER TABLE implementation_generation_claims
  DROP CONSTRAINT IF EXISTS implementation_generation_build_contract_exact_fk,
  DROP CONSTRAINT IF EXISTS implementation_generation_build_contract_shape,
  DROP COLUMN IF EXISTS application_build_contract_hash,
  DROP COLUMN IF EXISTS application_build_contract_id;

ALTER TABLE application_build_contracts
  DROP CONSTRAINT IF EXISTS application_build_contract_id_hash_unique;

