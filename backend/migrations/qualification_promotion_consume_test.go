package migrations

import (
	"strings"
	"testing"
)

func TestQualificationPromotionConsumeMigrationIsAtomicAppendOnlyAndOperatorScoped(t *testing.T) {
	up, err := files.ReadFile("000071_qualification_promotion_consume.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000071_qualification_promotion_consume.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upText := string(up)
	for _, expected := range []string{
		"CREATE TABLE qualification_promotion_consumptions",
		"CREATE TABLE qualification_promotion_handoffs",
		"authority_nonce uuid NOT NULL UNIQUE",
		"verified_promotion_bytes bytea NOT NULL",
		"golden_fixture_document_digest",
		"credential_revocation_payload_digest",
		"CREATE FUNCTION consume_verified_qualification_promotion",
		"LOCK TABLE qualification_promotion_consumptions IN SHARE ROW EXCLUSIVE MODE",
		"LOCK TABLE qualification_promotion_handoffs IN SHARE ROW EXCLUSIVE MODE",
		"v_now := date_trunc('milliseconds', clock_timestamp())",
		"INSERT INTO qualification_promotion_consumptions",
		"INSERT INTO qualification_promotion_handoffs",
		"promotion-only revision intent does not bind exact verified authority and target",
		"BEFORE UPDATE OR DELETE OR TRUNCATE",
		"SECURITY DEFINER",
		"SET search_path TO pg_catalog, %I, pg_temp",
		"worksflow_qualification_promotion_operator",
		"REVOKE ALL ON FUNCTION %I.consume_verified_qualification_promotion",
	} {
		if !strings.Contains(upText, expected) {
			t.Fatalf("qualification promotion migration is missing %q", expected)
		}
	}
	firstInsert := strings.Index(upText, "INSERT INTO qualification_promotion_consumptions")
	secondInsert := strings.Index(upText, "INSERT INTO qualification_promotion_handoffs")
	if firstInsert < 0 || secondInsert <= firstInsert {
		t.Fatal("consumption and handoff are not inserted in the required order inside one routine")
	}
	for _, forbidden := range []string{
		"GRANT INSERT ON TABLE", "GRANT UPDATE", "GRANT DELETE", "TO PUBLIC",
		"TO worksflow_application", "ON DELETE CASCADE",
	} {
		if strings.Contains(upText, forbidden) {
			t.Fatalf("qualification promotion migration contains forbidden SQL %q", forbidden)
		}
	}
	downText := string(down)
	for _, expected := range []string{
		"LOCK TABLE qualification_promotion_consumptions IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_promotion_handoffs IN ACCESS EXCLUSIVE MODE",
		"IF EXISTS (SELECT 1 FROM qualification_promotion_consumptions)",
		"OR EXISTS (SELECT 1 FROM qualification_promotion_handoffs)",
		"cannot roll back qualification promotion consumption while immutable audit or handoff state is nonempty",
		"DROP FUNCTION IF EXISTS consume_verified_qualification_promotion",
		"DROP TABLE IF EXISTS qualification_promotion_handoffs",
		"DROP TABLE IF EXISTS qualification_promotion_consumptions",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("qualification promotion rollback is missing %q", expected)
		}
	}
	consumeLock := strings.Index(downText, "LOCK TABLE qualification_promotion_consumptions IN ACCESS EXCLUSIVE MODE")
	handoffLock := strings.Index(downText, "LOCK TABLE qualification_promotion_handoffs IN ACCESS EXCLUSIVE MODE")
	emptyCheck := strings.Index(downText, "IF EXISTS (SELECT 1 FROM qualification_promotion_consumptions)")
	if consumeLock < 0 || handoffLock <= consumeLock || emptyCheck <= handoffLock {
		t.Fatal("qualification promotion rollback does not fence both ledgers before its empty-state check")
	}
}
