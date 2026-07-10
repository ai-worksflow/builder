package migrations

import (
	"strings"
	"testing"
)

func TestPublicDataRuntimeMigrationIsDefaultDenyAndDeploymentScoped(t *testing.T) {
	t.Parallel()

	contents, err := files.ReadFile("000008_public_data_runtime.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	for _, required := range []string{
		"allow_read boolean not null default false",
		"allow_create boolean not null default false",
		"allow_update boolean not null default false",
		"allow_delete boolean not null default false",
		"foreign key (project_id, table_id) references data_tables(project_id, id) on delete cascade",
		"foreign key (project_id, deployment_id) references deployments(project_id, id) on delete cascade",
		"foreign key (deployment_id, deployment_version_id)",
		"octet_length(token_digest) = 32",
		"status in ('pending', 'active', 'revoked')",
		"where status = 'active'",
		"jsonb_array_length(allowed_origins) between 1 and 16",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("public data runtime migration is missing %q", required)
		}
	}
	if strings.Contains(sql, "capability_token") || strings.Contains(sql, "plaintext_token") {
		t.Fatal("public capability plaintext must never be persisted")
	}
}
