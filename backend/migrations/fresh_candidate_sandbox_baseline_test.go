package migrations

import (
	"strings"
	"testing"
)

func TestFreshCandidateSandboxBaselineMigrationDeclaresNilSafeExactLineage(t *testing.T) {
	t.Parallel()

	up, err := files.ReadFile("000054_fresh_candidate_sandbox_baseline.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000054_fresh_candidate_sandbox_baseline.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	upText := string(up)
	for _, required := range []string{
		"ALTER TABLE sandbox_sessions",
		"ALTER TABLE candidate_implementation_freezes",
		"sandbox_sessions_base_workspace_shape",
		"candidate_implementation_freezes_base_workspace_shape",
		"ALTER COLUMN base_workspace_artifact_id DROP NOT NULL",
		"ALTER COLUMN base_workspace_revision_id DROP NOT NULL",
		"ALTER COLUMN base_workspace_content_hash DROP NOT NULL",
		"base_workspace_artifact_id IS NULL",
		"base_workspace_revision_id IS NULL",
		"base_workspace_content_hash IS NULL",
		"base_workspace_artifact_id IS NOT NULL",
		"base_workspace_revision_id IS NOT NULL",
		"base_workspace_content_hash IS NOT NULL",
		"pg_get_functiondef('validate_sandbox_session_insert()'::regprocedure)",
		"pg_get_functiondef('validate_candidate_implementation_freeze()'::regprocedure)",
		"pg_get_functiondef('validate_candidate_implementation_proposal_receipt()'::regprocedure)",
		"candidate.base_workspace_artifact_id IS NOT DISTINCT FROM NEW.base_workspace_artifact_id",
		"candidate.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id",
		"candidate.base_workspace_content_hash IS NOT DISTINCT FROM NEW.base_workspace_content_hash",
		"snapshot.base_workspace_artifact_id IS NOT DISTINCT FROM NEW.base_workspace_artifact_id",
		"snapshot.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id",
		"snapshot.base_workspace_content_hash IS NOT DISTINCT FROM NEW.base_workspace_content_hash",
		"proposal.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id",
		"receipt.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id",
	} {
		if !strings.Contains(upText, required) {
			t.Fatalf("fresh Candidate baseline migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"SET base_workspace_artifact_id",
		"COALESCE(base_workspace",
		"DROP CONSTRAINT implementation_proposal_candidate_freeze_receipt",
		"DROP TRIGGER candidate_implementation_freeze_verification_guard",
	} {
		if strings.Contains(upText, forbidden) {
			t.Fatalf("fresh Candidate baseline migration weakens or invents lineage with %q", forbidden)
		}
	}

	downText := string(down)
	for _, required := range []string{
		"rollback requires explicit handling of fresh Candidate/Sandbox rows",
		"FROM sandbox_sessions",
		"FROM candidate_implementation_freezes",
		"candidate.base_workspace_artifact_id = NEW.base_workspace_artifact_id",
		"snapshot.base_workspace_revision_id = NEW.base_workspace_revision_id",
		"proposal.base_workspace_revision_id = NEW.base_workspace_revision_id",
		"receipt.base_workspace_revision_id = NEW.base_workspace_revision_id",
		"DROP CONSTRAINT IF EXISTS candidate_implementation_freezes_base_workspace_shape",
		"DROP CONSTRAINT IF EXISTS sandbox_sessions_base_workspace_shape",
		"ALTER COLUMN base_workspace_artifact_id SET NOT NULL",
		"ALTER COLUMN base_workspace_revision_id SET NOT NULL",
		"ALTER COLUMN base_workspace_content_hash SET NOT NULL",
	} {
		if !strings.Contains(downText, required) {
			t.Fatalf("fresh Candidate baseline rollback is missing %q", required)
		}
	}
}
