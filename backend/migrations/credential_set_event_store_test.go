package migrations

import (
	"strings"
	"testing"
)

func TestCredentialSetEventStoreMigrationIsClosedAppendOnlyAndOwnerScoped(t *testing.T) {
	up, err := files.ReadFile("000072_credential_set_event_store.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000072_credential_set_event_store.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upText := string(up)
	for _, expected := range []string{
		"CREATE TABLE credential_set_events",
		"CREATE TABLE credential_set_operations",
		"CREATE TABLE credential_set_heads",
		"CREATE TABLE credential_set_projection_authorizations",
		"CREATE FUNCTION append_credential_set_event",
		"credential_set_sha256(p_request_bytes) <> p_request_hash",
		"convert_from(p_request_bytes, 'UTF8')::jsonb <> p_request_document",
		"LOCK TABLE credential_set_events IN SHARE ROW EXCLUSIVE MODE",
		"v_now := date_trunc('milliseconds', clock_timestamp())",
		"CredentialSet caller time is outside the post-lock trusted-time fence",
		"CredentialSet event ID is bound to different exact bytes",
		"CredentialSet expected version does not match current head",
		"BEFORE UPDATE OR DELETE OR TRUNCATE ON credential_set_events",
		"BEFORE UPDATE OR DELETE OR TRUNCATE ON credential_set_operations",
		"BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON credential_set_heads",
		"SECURITY DEFINER",
		"SET search_path TO pg_catalog, %I, pg_temp",
		"SET search_path TO pg_catalog, %I', schema_name, schema_name",
		"REVOKE ALL ON FUNCTION append_credential_set_event",
		"REVOKE ALL ON TABLE %I.credential_set_events FROM worksflow_application",
	} {
		if !strings.Contains(upText, expected) {
			t.Fatalf("CredentialSet migration is missing %q", expected)
		}
	}
	for _, forbidden := range []string{
		"raw_token bytea", "raw-token bytea", "cookie bytea", "storage_state", "storage-state bytea",
		"header bytea", "file_path", "capability bytea", "metadata json", "metadata jsonb",
		"GRANT INSERT", "GRANT UPDATE", "GRANT DELETE", "GRANT EXECUTE", "TO PUBLIC",
		"TO worksflow_credential_set_operator", "ON DELETE CASCADE",
	} {
		if strings.Contains(strings.ToLower(upText), strings.ToLower(forbidden)) {
			t.Fatalf("CredentialSet migration contains forbidden SQL %q", forbidden)
		}
	}
	appendPath := "ALTER FUNCTION %I.append_credential_set_event(text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb) '"
	if !strings.Contains(upText, appendPath) ||
		!strings.Contains(upText, "SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name") {
		t.Fatal("CredentialSet append routine does not have exact pg_catalog/trusted-schema/pg_temp search_path")
	}
	guardStart := strings.Index(upText, "ALTER FUNCTION %I.guard_credential_set_head_projection()")
	if guardStart < 0 {
		t.Fatal("CredentialSet head guard has no fixed search_path posture")
	}
	guardEnd := strings.Index(upText[guardStart:], ");")
	if guardEnd < 0 {
		t.Fatal("CredentialSet head guard ALTER FUNCTION block is incomplete")
	}
	guardBlock := upText[guardStart : guardStart+guardEnd]
	if !strings.Contains(guardBlock, "SET search_path TO pg_catalog, %I") || strings.Contains(guardBlock, "pg_temp") {
		t.Fatalf("CredentialSet head guard search_path is not exact trusted-schema-only: %q", guardBlock)
	}
	rejectStart := strings.Index(upText, "ALTER FUNCTION %I.reject_credential_set_immutable_mutation()")
	if rejectStart < 0 {
		t.Fatal("CredentialSet immutable reject trigger has no fixed search_path posture")
	}
	rejectEnd := strings.Index(upText[rejectStart:], ");")
	if rejectEnd < 0 || !strings.Contains(upText[rejectStart:rejectStart+rejectEnd], "SET search_path TO pg_catalog") {
		t.Fatal("CredentialSet immutable reject trigger search_path is not pg_catalog-only")
	}
	downText := string(down)
	for _, expected := range []string{
		"LOCK TABLE credential_set_events IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE credential_set_operations IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE credential_set_heads IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE credential_set_projection_authorizations IN ACCESS EXCLUSIVE MODE",
		"IF EXISTS (SELECT 1 FROM credential_set_events)",
		"OR EXISTS (SELECT 1 FROM credential_set_operations)",
		"OR EXISTS (SELECT 1 FROM credential_set_heads)",
		"OR EXISTS (SELECT 1 FROM credential_set_projection_authorizations)",
		"cannot roll back CredentialSet store while immutable audit state is nonempty",
		"DROP FUNCTION IF EXISTS append_credential_set_event",
		"DROP TABLE IF EXISTS credential_set_heads",
		"DROP TABLE IF EXISTS credential_set_events",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("CredentialSet rollback is missing %q", expected)
		}
	}
}
