package migrations

import (
	"strings"
	"testing"
)

func TestArtifactHealthBackfillCoversEveryExistingArtifactIdempotently(t *testing.T) {
	t.Parallel()

	up, err := files.ReadFile("000020_backfill_artifact_health.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000020_backfill_artifact_health.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	lower := strings.ToLower(string(up))
	for _, required := range []string{
		"insert into artifact_health",
		"from artifacts",
		"artifact_health.artifact_id is null",
		"on conflict (artifact_id) do nothing",
	} {
		if !strings.Contains(lower, required) {
			t.Fatalf("artifact health backfill is missing %q", required)
		}
	}
	if !strings.Contains(strings.ToLower(string(down)), "intentionally retained") {
		t.Fatal("artifact health rollback must document why backfilled canonical state is retained")
	}
}
