package migrations

import (
	"strings"
	"testing"
)

func TestArtifactRevisionSourcesMigrationIsImmutableAndExact(t *testing.T) {
	t.Parallel()

	up, err := files.ReadFile("000011_artifact_revision_sources.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000011_artifact_revision_sources.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"CREATE TABLE artifact_revision_sources",
		"revision_id uuid NOT NULL",
		"ordinal integer NOT NULL",
		"source_artifact_id uuid NOT NULL",
		"source_revision_id uuid NOT NULL",
		"source_content_hash text NOT NULL",
		"source_anchor_id text",
		"purpose text NOT NULL",
		"required boolean NOT NULL",
		"PRIMARY KEY (revision_id, source_revision_id, purpose)",
		"UNIQUE (revision_id, ordinal)",
	} {
		if !strings.Contains(string(up), required) {
			t.Fatalf("revision source migration is missing %q", required)
		}
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS artifact_revision_sources") {
		t.Fatal("revision source migration rollback must drop the immutable source table")
	}
}
