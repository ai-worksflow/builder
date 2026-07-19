package migrations

import (
	"strings"
	"testing"
)

func TestLegacyDeploymentReleaseControllerGateSerializesBothWriters(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000059_legacy_deployment_release_controller_gate.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000059_legacy_deployment_release_controller_gate.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	text := string(up)
	for _, expected := range []string{
		"LOCK TABLE deployments, deployment_versions, release_preview_runs",
		"IN SHARE ROW EXCLUSIVE MODE",
		"CREATE OR REPLACE FUNCTION validate_legacy_deployment_version_controller_gate()",
		"cannot establish legacy/controller gate while deploying legacy and active v2 authority coexist; reconcile it explicitly",
		"CREATE TRIGGER deployment_version_controller_singleflight_guard",
		"BEFORE INSERT ON deployment_versions",
		"FROM projects",
		"FOR UPDATE",
		"legacy production deployment is disabled; use Release Controller v3",
		"legacy preview version requires a deploying parent and must start as deploying authority",
		"version.status = 'deploying'",
		"deployment.status <> 'deploying'",
		"schema_version = 'release-preview-run/v2'",
		"schema_version = 'release-deployment-run/v2'",
		"'queued','claimed','submitting','reconcile_wait','reconciling'",
		"'verifying','reconcile_blocked'",
		"CREATE OR REPLACE FUNCTION validate_release_delivery_run_insert_v2()",
		"status = 'deploying'",
		"Release Controller v3 Run conflicts with a deploying legacy deployment",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("legacy/controller gate migration is missing %q", expected)
		}
	}
	if strings.Count(text, "FOR UPDATE") < 2 {
		t.Fatal("both legacy and v3 admission paths must lock the shared project row")
	}
	for _, forbidden := range []string{
		"DELETE FROM deployment",
		"UPDATE release_preview_runs SET state",
		"UPDATE release_deployment_runs SET state",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("legacy/controller gate uses unsafe mutation %q", forbidden)
		}
	}

	downText := string(down)
	for _, expected := range []string{
		"cannot downgrade legacy deployment controller gate while v2, reconcile-blocked, or deploying authority exists",
		"schema_version = 'release-preview-run/v2'",
		"schema_version = 'release-deployment-run/v2'",
		"state = 'reconcile_blocked'",
		"status = 'deploying'",
		"DROP TRIGGER IF EXISTS deployment_version_controller_singleflight_guard",
		"CREATE OR REPLACE FUNCTION validate_release_delivery_run_insert_v2()",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("legacy/controller gate downgrade is missing %q", expected)
		}
	}
	if strings.Contains(downText, "DELETE FROM") {
		t.Fatal("legacy/controller gate downgrade silently deletes authority")
	}
}
