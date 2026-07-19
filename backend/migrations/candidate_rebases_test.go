package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCandidateRebaseMigrationDeclaresImmutableSuccessorLineage(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000030_candidate_rebases.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000030_candidate_rebases.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TABLE candidate_rebases",
		"CREATE TABLE candidate_rebase_operations",
		"CREATE TABLE candidate_rebase_conflicts",
		"predecessor_candidate_id uuid NOT NULL UNIQUE",
		"successor_candidate_id uuid NOT NULL UNIQUE",
		"CandidateRebase plan can only be populated while applying",
		"CandidateRebase cannot advance before every planned operation is journaled",
		"CandidateRebase operations are immutable",
		"one exact open-to-resolved transition",
		"resolution_file jsonb",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Candidate rebase migration is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"DROP TABLE IF EXISTS candidate_rebase_conflicts",
		"DROP TABLE IF EXISTS candidate_rebase_operations",
		"DROP TABLE IF EXISTS candidate_rebases",
		"DROP FUNCTION IF EXISTS validate_candidate_rebase_insert",
	} {
		if !strings.Contains(string(down), expected) {
			t.Fatalf("Candidate rebase rollback is missing %q", expected)
		}
	}
}

func TestCandidateRebaseMigrationPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "candidate_rebase_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`) })
	database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := Up(ctx, database); err != nil {
		t.Fatalf("migrations.Up failed: %v", err)
	}

	seed := seedRepositoryCandidateCanary(t, ctx, database)
	predecessorID := insertRebaseCandidate(t, ctx, database, seed, uuid.New(), seed.manifestID, seed.manifestHash, "sha256:"+strings.Repeat("1", 64))
	targetManifestID := seed.manifestID
	targetManifestHash := seed.manifestHash
	successorID := insertRebaseCandidate(t, ctx, database, seed, uuid.New(), targetManifestID, targetManifestHash, "sha256:"+strings.Repeat("2", 64))
	rebaseID := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_rebases (
  id, schema_version, project_id, operation_id,
  predecessor_candidate_id, successor_candidate_id, target_build_manifest_id,
  ancestor_tree_hash, predecessor_tree_hash, target_tree_hash, planned_tree_hash, plan_hash,
  state, version, created_by, created_at, updated_at
) VALUES ($1, 'candidate-rebase/v1', $2, 'canary', $3, $4, $5,
          $6, $6, $7, $7, $8, 'applying', 1, $9, $10, $10)
`, rebaseID, seed.projectID, predecessorID, successorID, targetManifestID,
		"sha256:"+strings.Repeat("1", 64), "sha256:"+strings.Repeat("2", 64),
		"sha256:"+strings.Repeat("3", 64), seed.actorID, now); err != nil {
		t.Fatalf("insert exact rebase: %v", err)
	}
	operation, _ := json.Marshal(map[string]any{
		"id": "rebase-op", "kind": "file.upsert", "path": "web/page.tsx",
		"contentHash": "sha256:" + strings.Repeat("4", 64), "byteSize": 4, "mode": "100644",
	})
	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_rebase_operations (rebase_id, ordinal, operation_id, operation)
VALUES ($1, 0, 'rebase-op', $2::jsonb)
`, rebaseID, operation); err != nil {
		t.Fatalf("insert immutable rebase operation: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE candidate_rebase_operations SET ordinal = 1 WHERE rebase_id = $1`, rebaseID); err == nil {
		t.Fatal("rebase operation mutation unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `
UPDATE candidate_rebases
SET state = 'ready', version = 2, updated_at = updated_at + interval '1 second'
WHERE id = $1
`, rebaseID); err == nil {
		t.Fatal("rebase advanced without its exact journal operation")
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM candidate_rebases WHERE id = $1`, rebaseID); err == nil {
		t.Fatal("rebase lineage deletion unexpectedly succeeded")
	}

	down, err := files.ReadFile("000030_candidate_rebases.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("Candidate rebase rollback failed: %v", err)
	}
}

func insertRebaseCandidate(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	seed repositoryCandidateCanarySeed,
	candidateID, manifestID uuid.UUID,
	manifestHash, treeHash string,
) uuid.UUID {
	t.Helper()
	snapshotID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_snapshots (
  id, schema_version, project_id, build_manifest_id, build_manifest_hash,
  build_contract_id, build_contract_hash, full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
  tree_file_count, tree_byte_size, created_by, created_at
) VALUES ($1, 'repository-snapshot/v1', $2, $3, $4,
          $5, $6, $7, $8, $9, $10, $11,
          'content', $1, $16, $12, $13, 0, 0, $14, $15)
`, snapshotID, seed.projectID, manifestID, manifestHash, seed.contractID, seed.contractHash,
		seed.fullStackID, seed.fullStackHash, seed.workspaceArtifactID, seed.workspaceRevisionID,
		seed.workspaceHash, "sha256:"+strings.Repeat("a", 64), treeHash, seed.actorID, seed.createdAt,
		"tree-"+snapshotID.String()); err != nil {
		t.Fatalf("insert rebase snapshot: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_workspaces (
  id, schema_version, project_id, repository_snapshot_id,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  base_tree_store, base_tree_owner_id, base_tree_ref, base_tree_content_hash, base_tree_hash,
  current_tree_store, current_tree_owner_id, current_tree_ref, current_tree_content_hash, current_tree_hash,
  current_tree_file_count, current_tree_byte_size, status, dirty, conflicted, stale, rebase_required,
  session_epoch, version, journal_sequence, writer_lease_epoch, created_by, created_at, updated_at
) SELECT $1, 'candidate-workspace/v1', project_id, id,
         build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
         full_stack_template_id, full_stack_template_hash,
         base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
         tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
         tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
         tree_file_count, tree_byte_size, 'active', false, false, false, false,
         1, 1, 0, 0, $2, $3, $3
  FROM repository_snapshots WHERE id = $4
`, candidateID, seed.actorID, seed.createdAt.Add(time.Second), snapshotID); err != nil {
		t.Fatalf("insert rebase Candidate: %v", err)
	}
	return candidateID
}
