package migrations

import (
	"strings"
	"testing"
)

func TestQualificationEvidenceEventStoreMigrationIsClosedReplayableAndOwnerScoped(t *testing.T) {
	up, err := files.ReadFile("000073_qualification_evidence_event_store.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000073_qualification_evidence_event_store.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upText := string(up)
	for _, expected := range []string{
		"CREATE TABLE qualification_evidence_events",
		"CREATE TABLE qualification_evidence_operations",
		"CREATE TABLE qualification_evidence_heads",
		"CREATE TABLE qualification_evidence_projection_authorizations",
		"CREATE FUNCTION append_qualification_evidence_event",
		"qualification_evidence_sha256(p_request_bytes) <> p_request_hash",
		"qualification_evidence_sha256(p_event_bytes) <> p_event_hash",
		"convert_from(p_request_bytes, 'UTF8')::jsonb <> p_request_document",
		"convert_from(p_event_bytes, 'UTF8')::jsonb <> p_event_document",
		"LOCK TABLE qualification_evidence_events IN SHARE ROW EXCLUSIVE MODE",
		"Qualification Evidence EventID is bound to different exact bytes",
		"Qualification Evidence OperationID is globally bound to another orchestration",
		"Qualification Evidence expected version does not match current head",
		"v_now := date_trunc('milliseconds', clock_timestamp())",
		"Qualification Evidence caller time is outside the post-lock trusted-time fence",
		"pg_catalog.convert_to(previous_id, 'UTF8') >= pg_catalog.convert_to(artifact_id, 'UTF8')",
		"BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_evidence_events",
		"BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_evidence_operations",
		"BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON qualification_evidence_heads",
		"SECURITY DEFINER",
		"ALTER FUNCTION %I.qualification_evidence_sha256(bytea) SET search_path TO pg_catalog",
		"ALTER TABLE %I.qualification_evidence_events OWNER TO worksflow_migration_owner",
		"ALTER TABLE %I.qualification_evidence_operations OWNER TO worksflow_migration_owner",
		"ALTER TABLE %I.qualification_evidence_heads OWNER TO worksflow_migration_owner",
		"ALTER TABLE %I.qualification_evidence_projection_authorizations OWNER TO worksflow_migration_owner",
		"ALTER FUNCTION %I.qualification_evidence_sha256(bytea) OWNER TO worksflow_migration_owner",
		"ALTER FUNCTION %I.reject_qualification_evidence_immutable_mutation() OWNER TO worksflow_migration_owner",
		"ALTER FUNCTION %I.guard_qualification_evidence_head_projection() OWNER TO worksflow_migration_owner",
		"append_qualification_evidence_event(text,bytea,jsonb,text,bytea,jsonb) OWNER TO worksflow_migration_owner",
		"REVOKE ALL ON FUNCTION %I.append_qualification_evidence_event",
		"REVOKE ALL ON TABLE %I.qualification_evidence_events",
	} {
		if !strings.Contains(upText, expected) {
			t.Fatalf("Qualification Evidence migration is missing %q", expected)
		}
	}
	for _, forbidden := range []string{
		"raw_token", "raw-token", "cookie bytea", "storage_state", "storage-state bytea",
		"header bytea", "environment json", "file_path", "capability bytea", "metadata json",
		"GRANT INSERT", "GRANT UPDATE", "GRANT DELETE", "GRANT EXECUTE", "TO PUBLIC",
		"worksflow_qualification_evidence_operator", "ON DELETE CASCADE",
	} {
		if strings.Contains(strings.ToLower(upText), strings.ToLower(forbidden)) {
			t.Fatalf("Qualification Evidence migration contains forbidden SQL %q", forbidden)
		}
	}
	appendStart := strings.Index(upText, "ALTER FUNCTION %I.append_qualification_evidence_event(text,bytea,jsonb,text,bytea,jsonb)")
	if appendStart < 0 || !strings.Contains(upText[appendStart:], "SET search_path TO pg_catalog, %I, pg_temp") {
		t.Fatal("Qualification Evidence append routine lacks exact pg_catalog/trusted-schema/pg_temp search_path")
	}
	guardStart := strings.Index(upText, "ALTER FUNCTION %I.guard_qualification_evidence_head_projection()")
	if guardStart < 0 {
		t.Fatal("Qualification Evidence head guard has no fixed search_path posture")
	}
	guardEnd := strings.Index(upText[guardStart:], ");")
	if guardEnd < 0 {
		t.Fatal("Qualification Evidence head guard ALTER block is incomplete")
	}
	guardBlock := upText[guardStart : guardStart+guardEnd]
	if !strings.Contains(guardBlock, "SET search_path TO pg_catalog, %I") || strings.Contains(guardBlock, "pg_temp") {
		t.Fatalf("Qualification Evidence head guard search_path is not trusted-schema-only: %q", guardBlock)
	}
	rejectStart := strings.Index(upText, "ALTER FUNCTION %I.reject_qualification_evidence_immutable_mutation()")
	if rejectStart < 0 {
		t.Fatal("Qualification Evidence immutable trigger lacks a fixed search_path")
	}
	rejectEnd := strings.Index(upText[rejectStart:], ");")
	if rejectEnd < 0 || !strings.Contains(upText[rejectStart:rejectStart+rejectEnd], "SET search_path TO pg_catalog") {
		t.Fatal("Qualification Evidence immutable trigger search_path is not pg_catalog-only")
	}
	downText := string(down)
	for _, expected := range []string{
		"LOCK TABLE qualification_evidence_events IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_evidence_operations IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_evidence_heads IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_evidence_projection_authorizations IN ACCESS EXCLUSIVE MODE",
		"IF EXISTS (SELECT 1 FROM qualification_evidence_events)",
		"OR EXISTS (SELECT 1 FROM qualification_evidence_operations)",
		"OR EXISTS (SELECT 1 FROM qualification_evidence_heads)",
		"OR EXISTS (SELECT 1 FROM qualification_evidence_projection_authorizations)",
		"cannot roll back Qualification Evidence store while immutable audit state is nonempty",
		"DROP FUNCTION IF EXISTS append_qualification_evidence_event",
		"DROP TABLE IF EXISTS qualification_evidence_heads",
		"DROP TABLE IF EXISTS qualification_evidence_events",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("Qualification Evidence rollback is missing %q", expected)
		}
	}
}
