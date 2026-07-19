package migrations

import (
	"strings"
	"testing"
)

func TestReleaseDeliveryRunOperationAuthorityIsDeferredAndExact(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000061_release_delivery_run_operation_authority.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000061_release_delivery_run_operation_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)

	lockAt := strings.Index(text, "LOCK TABLE release_preview_runs, release_deployment_runs")
	scanAt := strings.Index(text, "cannot establish v2 Run authority")
	if lockAt < 0 || scanAt < 0 || lockAt > scanAt {
		t.Fatal("v2 Run authority migration must lock every writer table before its install scan")
	}
	for _, expected := range []string{
		"release_delivery_operations IN SHARE ROW EXCLUSIVE MODE",
		"run.schema_version = 'release-preview-run/v2'",
		"run.schema_version = 'release-deployment-run/v2'",
		"operation.project_id = run.project_id",
		"operation.kind = 'preview'",
		"operation.kind = 'production'",
		"operation.preview_run_id = run.id",
		"operation.deployment_run_id = run.id",
		") <> 1",
		"CREATE OR REPLACE FUNCTION validate_release_delivery_run_operation_authority()",
		"CREATE CONSTRAINT TRIGGER release_preview_run_operation_authority_guard",
		"CREATE CONSTRAINT TRIGGER release_deployment_run_operation_authority_guard",
		"AFTER INSERT ON release_preview_runs",
		"AFTER INSERT ON release_deployment_runs",
		"DEFERRABLE INITIALLY DEFERRED",
		"NEW.schema_version <> 'release-preview-run/v2'",
		"NEW.schema_version <> 'release-deployment-run/v2'",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("v2 Run operation authority migration is missing %q", expected)
		}
	}
	for _, forbidden := range []string{
		"UPDATE release_preview_runs",
		"UPDATE release_deployment_runs",
		"DELETE FROM release_delivery_operations",
		"INSERT INTO release_delivery_operations",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("v2 Run operation authority migration repairs authority via %q", forbidden)
		}
	}

	downText := string(down)
	for _, expected := range []string{
		"cannot downgrade v2 Run operation authority while v2 Runs or delivery Operations exist",
		"schema_version = 'release-preview-run/v2'",
		"schema_version = 'release-deployment-run/v2'",
		"EXISTS (SELECT 1 FROM release_delivery_operations)",
		"DROP TRIGGER IF EXISTS release_preview_run_operation_authority_guard",
		"DROP TRIGGER IF EXISTS release_deployment_run_operation_authority_guard",
		"DROP FUNCTION IF EXISTS validate_release_delivery_run_operation_authority()",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("v2 Run operation authority downgrade is missing %q", expected)
		}
	}
}
