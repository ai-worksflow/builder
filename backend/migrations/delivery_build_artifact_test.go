package migrations

import (
	"strings"
	"testing"
)

func TestDeliveryBuildArtifactMigrationPinsQualityAndDeployment(t *testing.T) {
	up, err := files.ReadFile("000007_delivery_build_artifacts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000007_delivery_build_artifacts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"build_artifact_id", "build_content_ref", "build_content_hash", "build_hash", "build_entry_path", "quality_run_id"} {
		if !strings.Contains(string(up), required) {
			t.Fatalf("delivery build artifact migration is missing %s", required)
		}
	}
	for _, required := range []string{"DROP COLUMN IF EXISTS build_artifact_id", "DROP COLUMN IF EXISTS build_content_ref", "DROP COLUMN IF EXISTS build_hash"} {
		if !strings.Contains(string(down), required) {
			t.Fatalf("delivery build artifact rollback is missing %s", required)
		}
	}
}
