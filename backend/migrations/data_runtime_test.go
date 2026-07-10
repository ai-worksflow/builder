package migrations

import (
	"strings"
	"testing"
)

func TestDataRuntimeMigrationPersistsOnlyCiphertextAndTokenDigests(t *testing.T) {
	t.Parallel()

	contents, err := files.ReadFile("000002_data_runtime.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	for _, required := range []string{
		"encrypted_value bytea",
		"kind = 'secret' and encrypted_value is not null",
		"plain_value is null",
		"token_hash bytea not null",
		"octet_length(token_hash) = 32",
		"references projects(id) on delete cascade",
		"references users(id)",
		"unique (project_id, scope, name)",
		"unique (token_hash)",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("data runtime migration is missing %q", required)
		}
	}
	connectionStart := strings.Index(sql, "create table data_connections")
	connectionEnd := strings.Index(sql[connectionStart:], ");")
	if connectionStart < 0 || connectionEnd < 0 {
		t.Fatal("data_connections definition not found")
	}
	definition := sql[connectionStart : connectionStart+connectionEnd]
	for _, forbidden := range []string{"api_key", "service_key", "encrypted_key", "credential"} {
		if strings.Contains(definition, forbidden) {
			t.Errorf("data_connections must not persist Supabase credentials: %s", forbidden)
		}
	}
}

func TestDataRuntimeMigrationPreviewIsProjectScopedAndOneTime(t *testing.T) {
	t.Parallel()

	contents, err := files.ReadFile("000002_data_runtime.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	for _, required := range []string{
		"base_revision bigint not null",
		"expires_at timestamptz not null",
		"consumed_at timestamptz",
		"unique (preview_id)",
		"where consumed_at is null",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration confirmation invariant missing %q", required)
		}
	}
}
