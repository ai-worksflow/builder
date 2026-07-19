package migrations

import (
	"strings"
	"testing"
)

func TestReleaseBundlePublishGateMigrationFailsClosed(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000045_release_bundle_publish_gate.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000045_release_bundle_publish_gate.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TRIGGER deployment_version_release_authority_insert_guard",
		"new deployment versions require an exact Canonical Receipt and ReleaseBundle",
		"bundle.workspace_revision_id = NEW.workspace_revision_id",
		"receipt.build_manifest_id = NEW.build_manifest_id",
		"quality.build_artifact_id = NEW.build_artifact_id",
		"artifact->>'kind' = 'web-static'",
		"artifact->>'contentHash' = NEW.build_hash",
		"rollback must create a new version from the exact prior ReleaseBundle and artifact",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("release publish gate migration is missing %q", expected)
		}
	}
	if strings.Contains(text, "candidate_verification_receipts") {
		t.Fatal("release publish gate trusts Candidate authority")
	}
	if !strings.Contains(string(down), "DROP TRIGGER IF EXISTS deployment_version_release_authority_insert_guard") {
		t.Fatal("release publish gate rollback does not remove its insert guard")
	}
}
