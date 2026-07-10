package migrations

import (
	"strings"
	"testing"
)

func TestArtifactHealthDeliveryMigrationAllowsQualityOutcomes(t *testing.T) {
	t.Parallel()
	contents, err := files.ReadFile("000005_artifact_health_delivery_status.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	for _, status := range []string{"'complete'", "'blocked'"} {
		if !strings.Contains(sql, status) {
			t.Errorf("artifact health delivery constraint is missing %s", status)
		}
	}
}
