package migrations

import (
	"strings"
	"testing"
)

func TestReleasePreviewSingleFlightLocksEveryNonterminalExactBundleState(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000057_release_preview_singleflight.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000057_release_preview_singleflight.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	text := string(up)
	for _, expected := range []string{
		"CREATE OR REPLACE FUNCTION validate_release_bundle_insert()",
		"NEW.created_at = TIMESTAMPTZ '0001-01-01 00:00:00+00'",
		"NEW.created_at > statement_timestamp()",
		"NEW.creation_transaction_id := txid_current()",
		"ReleaseBundle requires one exact passed Canonical Receipt and identical release artifacts",
		"CREATE UNIQUE INDEX release_preview_runs_one_nonterminal_bundle_idx",
		"ON release_preview_runs (project_id, release_bundle_id, release_bundle_hash)",
		"'queued','claimed','submitting','reconcile_wait','reconciling','verifying','reconcile_blocked'",
		"HAVING count(*) > 1",
		"duplicate nonterminal exact Bundle authority exists",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("release preview single-flight migration is missing %q", expected)
		}
	}
	for _, forbidden := range []string{
		"DELETE FROM release_preview_runs",
		"SET state = 'cancelled'",
		"WHERE state IN ('queued','claimed','submitting','reconcile_wait','reconciling','verifying')",
		"NEW.created_at := statement_timestamp()",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("release preview single-flight migration uses unsafe shortcut %q", forbidden)
		}
	}

	downText := string(down)
	for _, expected := range []string{
		"cannot downgrade preview single-flight while ReleaseBundle, v2, or reconcile-blocked Preview authority exists",
		"schema_version = 'release-preview-run/v2'",
		"state = 'reconcile_blocked'",
		"FROM release_delivery_operations",
		"FROM release_bundles",
		"DROP INDEX IF EXISTS release_preview_runs_one_nonterminal_bundle_idx",
		"NEW.created_at := statement_timestamp()",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("release preview single-flight rollback is missing %q", expected)
		}
	}
	if strings.Contains(downText, "DELETE FROM release_") {
		t.Fatal("release preview single-flight rollback silently deletes authority")
	}
}
