package migrations

import (
	"strings"
	"testing"
)

func TestDeliveryMigrationPinsQualityAndDeploymentHistory(t *testing.T) {
	t.Parallel()

	contents, err := files.ReadFile("000003_delivery.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	for _, required := range []string{
		"workspace_artifact_id uuid not null references artifacts(id) on delete restrict",
		"workspace_revision_id uuid not null references artifact_revisions(id) on delete restrict",
		"workspace_content_hash text not null",
		"report_revision_id uuid references artifact_revisions(id) on delete restrict",
		"workflow_run_id uuid references workflow_runs(id) on delete set null",
		"unique (project_id, environment)",
		"unique (deployment_id, number)",
		"source_version_id uuid references deployment_versions(id) on delete restrict",
		"quality_run_id uuid references quality_runs(id) on delete set null",
		"environment_variable_names jsonb not null default '[]'::jsonb",
		"unique (deployment_id, sequence)",
		"deferrable initially deferred",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("delivery migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{"environment_variable_values", "secret_value", "plaintext_secret", "encrypted_secret"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("delivery metadata must not persist environment secret values: %q", forbidden)
		}
	}
}

func TestDeliveryDownMigrationDropsCircularConstraintFirst(t *testing.T) {
	t.Parallel()

	contents, err := files.ReadFile("000003_delivery.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	constraint := strings.Index(sql, "drop constraint if exists deployments_active_version_fk")
	versions := strings.Index(sql, "drop table if exists deployment_versions")
	deployments := strings.Index(sql, "drop table if exists deployments")
	if constraint < 0 || versions < 0 || deployments < 0 || constraint > versions || versions > deployments {
		t.Fatalf("delivery down migration does not break/drop the circular dependency safely: %s", sql)
	}
}
