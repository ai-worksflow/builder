package migrations

import (
	"strings"
	"testing"
)

func TestModelGovernanceActivationStoreMigrationIsAtomicClosedAndOwnerOnly(t *testing.T) {
	up, err := files.ReadFile("000069_model_governance_activation_store.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000069_model_governance_activation_store.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upText := string(up)
	for _, expected := range []string{
		"CREATE TABLE model_governance_activation_records",
		"model_governance_activation_history_unique UNIQUE (workload, generation)",
		"model_governance_activation_exact_profile_unique UNIQUE",
		"CREATE TABLE model_governance_activation_heads",
		"model_governance_activation_head_record_fk",
		"ON UPDATE RESTRICT ON DELETE RESTRICT",
		"CREATE TABLE model_governance_revocation_anchor",
		"authority_bytes bytea NOT NULL",
		"authority_document jsonb NOT NULL",
		"CREATE FUNCTION append_model_governance_activation",
		"SECURITY DEFINER",
		"FOR UPDATE OF head",
		"empty workload head requires a separate signed bootstrap authority",
		"outside the PostgreSQL trusted-time fence",
		"CREATE FUNCTION observe_model_governance_revocation_authority",
		"(p_authority_document->'digestRevocations') @> (v_current.authority_document->'digestRevocations')",
		"(p_authority_document->'signerRevocations') @> (v_current.authority_document->'signerRevocations')",
		"pg_catalog.convert_from(p_authority_bytes, 'UTF8')::jsonb <> p_authority_document",
		"SET search_path TO pg_catalog, %I, pg_temp",
		"OWNER TO worksflow_migration_owner",
		"REVOKE ALL ON TABLE %I.model_governance_activation_records FROM worksflow_application",
		"REVOKE ALL ON FUNCTION %I.append_model_governance_activation",
		"REVOKE ALL ON FUNCTION %I.observe_model_governance_revocation_authority",
	} {
		if !strings.Contains(upText, expected) {
			t.Fatalf("Model Governance PostgreSQL migration is missing %q", expected)
		}
	}
	for _, forbidden := range []string{
		"TO PUBLIC", "TO worksflow_application", "GRANT INSERT", "GRANT UPDATE", "GRANT DELETE",
	} {
		if strings.Contains(upText, forbidden) {
			t.Fatalf("owner-only Model Governance migration contains forbidden SQL %q", forbidden)
		}
	}
	appendLock := strings.Index(upText, "FOR UPDATE OF head")
	if appendLock < 0 {
		t.Fatal("activation append does not lock its workload head")
	}
	appendFreshClock := strings.Index(upText[appendLock:], "v_now := date_trunc('milliseconds', clock_timestamp())")
	if appendFreshClock < 0 {
		t.Fatal("activation append does not refresh PostgreSQL time after locking its workload head")
	}
	revocationLock := strings.Index(upText, "LOCK TABLE model_governance_revocation_anchor IN SHARE ROW EXCLUSIVE MODE")
	if revocationLock < 0 {
		t.Fatal("revocation observation does not serialize its initially empty singleton")
	}
	revocationRead := strings.Index(upText[revocationLock:], "FROM model_governance_revocation_anchor")
	revocationFreshClock := strings.Index(upText[revocationLock:], "v_now := date_trunc('milliseconds', clock_timestamp())")
	revocationCurrentCheck := strings.Index(upText[revocationLock:], "v_now >= v_expires_at")
	if revocationRead < 0 || revocationFreshClock < 0 || revocationCurrentCheck < 0 ||
		revocationRead >= revocationFreshClock || revocationFreshClock >= revocationCurrentCheck {
		t.Fatal("revocation observation does not refresh and validate PostgreSQL time after locking its singleton anchor")
	}
	downText := string(down)
	for _, expected := range []string{
		"LOCK TABLE model_governance_activation_heads IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE model_governance_revocation_anchor IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE model_governance_activation_records IN ACCESS EXCLUSIVE MODE",
		"IF EXISTS (SELECT 1 FROM model_governance_activation_records)",
		"OR EXISTS (SELECT 1 FROM model_governance_activation_heads)",
		"OR EXISTS (SELECT 1 FROM model_governance_revocation_anchor)",
		"cannot roll back Model Governance activation store while immutable audit state is nonempty",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("Model Governance activation-store rollback is missing %q", expected)
		}
	}
	ordered := []string{
		"LOCK TABLE model_governance_activation_heads IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE model_governance_revocation_anchor IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE model_governance_activation_records IN ACCESS EXCLUSIVE MODE",
		"IF EXISTS (SELECT 1 FROM model_governance_activation_records)",
		"DROP FUNCTION IF EXISTS append_model_governance_activation",
		"DROP FUNCTION IF EXISTS observe_model_governance_revocation_authority",
		"DROP TABLE IF EXISTS model_governance_activation_heads",
		"DROP TABLE IF EXISTS model_governance_activation_records",
		"DROP TABLE IF EXISTS model_governance_revocation_anchor",
		"DROP FUNCTION IF EXISTS reject_model_governance_immutable_mutation",
	}
	prior := -1
	for _, statement := range ordered {
		index := strings.Index(downText, statement)
		if index < 0 || index <= prior {
			t.Fatalf("rollback statement %q is absent or not dependency ordered", statement)
		}
		prior = index
	}
}
