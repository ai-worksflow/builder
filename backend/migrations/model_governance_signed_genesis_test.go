package migrations

import (
	"strings"
	"testing"
)

func TestModelGovernanceSignedGenesisMigrationIsDistinctAtomicAndOwnerOnly(t *testing.T) {
	up, err := files.ReadFile("000070_model_governance_signed_genesis.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000070_model_governance_signed_genesis.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upText := string(up)
	for _, expected := range []string{
		"ADD COLUMN authority_kind text NOT NULL DEFAULT 'activation'",
		"authority_kind = 'genesis'",
		"initial_revocation_authority_id = 'model-governance-revocations'",
		"CREATE FUNCTION append_model_governance_genesis",
		"CREATE FUNCTION observe_model_governance_trust_policy",
		"CREATE FUNCTION enforce_model_governance_activation_authority_anchor",
		"CREATE TRIGGER model_governance_activation_authority_anchor",
		"SECURITY DEFINER",
		"LOCK TABLE model_governance_activation_heads IN SHARE ROW EXCLUSIVE MODE",
		"LOCK TABLE model_governance_revocation_anchor IN SHARE ROW EXCLUSIVE MODE",
		"v_now := date_trunc('milliseconds', clock_timestamp())",
		"v_existing.genesis_envelope_digest <> p_genesis_envelope_digest",
		"Model Governance Genesis revocation authority drifted",
		"v_revocation.current_trust_policy_hash <> p_current_trust_policy_hash",
		"v_anchor.current_trust_policy_hash <> NEW.trust_policy_hash",
		"SET search_path TO pg_catalog, %I, pg_temp",
		"OWNER TO worksflow_migration_owner",
		"REVOKE ALL ON FUNCTION %I.append_model_governance_genesis",
		"REVOKE ALL ON FUNCTION %I.observe_model_governance_trust_policy",
		"REVOKE ALL ON FUNCTION %I.enforce_model_governance_activation_authority_anchor",
	} {
		if !strings.Contains(upText, expected) {
			t.Fatalf("signed Genesis migration is missing %q", expected)
		}
	}
	for _, forbidden := range []string{"TO PUBLIC", "TO worksflow_application", "GRANT INSERT", "GRANT UPDATE", "GRANT DELETE"} {
		if strings.Contains(upText, forbidden) {
			t.Fatalf("signed Genesis migration contains forbidden SQL %q", forbidden)
		}
	}
	headLock := strings.Index(upText, "LOCK TABLE model_governance_activation_heads IN SHARE ROW EXCLUSIVE MODE")
	revocationRelative := -1
	clockRelative := -1
	if headLock >= 0 {
		revocationRelative = strings.Index(upText[headLock:], "LOCK TABLE model_governance_revocation_anchor IN SHARE ROW EXCLUSIVE MODE")
		clockRelative = strings.Index(upText[headLock:], "v_now := date_trunc('milliseconds', clock_timestamp())")
	}
	if headLock < 0 || revocationRelative < 0 || clockRelative <= revocationRelative {
		t.Fatal("Genesis empty-head/revocation locks and post-lock trusted clock are not ordered")
	}
	downText := string(down)
	for _, expected := range []string{
		"LOCK TABLE model_governance_activation_heads IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE model_governance_revocation_anchor IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE model_governance_activation_records IN ACCESS EXCLUSIVE MODE",
		"IF EXISTS (SELECT 1 FROM model_governance_activation_records)",
		"OR EXISTS (SELECT 1 FROM model_governance_activation_heads)",
		"WHERE current_trust_policy_hash IS NOT NULL",
		"cannot roll back signed Model Governance Genesis while activation audit state is nonempty",
		"DROP FUNCTION IF EXISTS append_model_governance_genesis",
		"DROP FUNCTION IF EXISTS observe_model_governance_trust_policy",
		"DROP TRIGGER IF EXISTS model_governance_activation_authority_anchor",
		"DROP FUNCTION IF EXISTS enforce_model_governance_activation_authority_anchor",
		"DROP COLUMN IF EXISTS authority_kind",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("signed Genesis down migration is missing %q", expected)
		}
	}
	headLockDown := strings.Index(downText, "LOCK TABLE model_governance_activation_heads IN ACCESS EXCLUSIVE MODE")
	anchorLock := strings.Index(downText, "LOCK TABLE model_governance_revocation_anchor IN ACCESS EXCLUSIVE MODE")
	recordLock := strings.Index(downText, "LOCK TABLE model_governance_activation_records IN ACCESS EXCLUSIVE MODE")
	emptyCheck := strings.Index(downText, "IF EXISTS (SELECT 1 FROM model_governance_activation_records)")
	if headLockDown < 0 || anchorLock <= headLockDown || recordLock <= anchorLock || emptyCheck <= recordLock {
		t.Fatal("signed Genesis rollback does not fence all mutable authority tables before its empty-state check")
	}
}
