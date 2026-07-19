package migrations

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFreshCandidateSandboxBaselinePostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()

	schema := "fresh_candidate_baseline_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	applyFreshCandidateBaselineMigrationsThrough(
		t, ctx, database, "000053_legacy_ai_implementation_proposal_gate.up.sql",
	)
	seed := seedRepositoryCandidateCanary(t, ctx, database)
	candidate := createSandboxCandidateCanary(t, ctx, database, seed, "fresh-baseline")

	// Convert the pre-054 fixture into the already-supported fresh Repository /
	// Candidate baseline. Only the downstream Session and freeze tables still
	// require migration 54 at this point.
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE application_build_manifests
SET workspace_revision_id = NULL
WHERE id = $1
`, seed.manifestID); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE repository_snapshots
SET base_workspace_artifact_id = NULL,
    base_workspace_revision_id = NULL,
    base_workspace_content_hash = NULL
WHERE id = $1
`, candidate.snapshotID); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE candidate_workspaces
SET base_workspace_artifact_id = NULL,
    base_workspace_revision_id = NULL,
    base_workspace_content_hash = NULL
WHERE id = $1
`, candidate.id); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("prepare fresh Repository/Candidate baseline: %v", err)
	}

	up, err := files.ReadFile("000054_fresh_candidate_sandbox_baseline.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		t.Fatalf("apply fresh Candidate/Sandbox baseline migration: %v", err)
	}

	for _, table := range []string{"sandbox_sessions", "candidate_implementation_freezes"} {
		var nullableCount int
		if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = $1
  AND column_name IN (
    'base_workspace_artifact_id',
    'base_workspace_revision_id',
    'base_workspace_content_hash'
  )
  AND is_nullable = 'YES'
`, table).Scan(&nullableCount); err != nil {
			t.Fatal(err)
		}
		if nullableCount != 3 {
			t.Fatalf("%s has %d nullable base Workspace columns, want 3", table, nullableCount)
		}
	}

	functionChecks := map[string][]string{
		"validate_sandbox_session_insert()": {
			"candidate.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id",
			"snapshot.base_workspace_content_hash IS NOT DISTINCT FROM NEW.base_workspace_content_hash",
			"candidate.current_tree_hash = NEW.candidate_tree_hash",
		},
		"validate_candidate_implementation_freeze()": {
			"candidate.base_workspace_artifact_id IS NOT DISTINCT FROM NEW.base_workspace_artifact_id",
			"proposal.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id",
			"base.tree_hash = NEW.base_tree_hash",
		},
		"validate_candidate_implementation_proposal_receipt()": {
			"receipt.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id",
			"verification_receipt.decision = 'passed'",
			"profile_policy.state = 'active'",
		},
	}
	for functionName, fragments := range functionChecks {
		var definition string
		if err := database.QueryRowContext(
			ctx, `SELECT pg_get_functiondef($1::regprocedure)`, functionName,
		).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(definition, fragment) {
				t.Fatalf("%s lost exact fence %q", functionName, fragment)
			}
		}
	}

	// A dirty Candidate makes its current tree Candidate-owned, as required by
	// the freeze receipt, while its fresh base Workspace tuple remains NULL.
	candidate = dirtySandboxCandidateCanary(
		t, ctx, database, seed.actorID, candidate, "fresh-baseline",
	)
	sessionID := uuid.New()
	insertSandboxSessionCanary(t, ctx, database, seed, candidate.id, sessionID, uuid.Nil, true)
	assertSandboxTransition(
		t, ctx, database, sessionID, 1, candidate.sessionEpoch, "starting", seed.actorID,
		"fresh baseline runner allocated", uuid.Nil, 2, candidate.sessionEpoch, candidate.version,
	)
	assertSandboxTransition(
		t, ctx, database, sessionID, 2, candidate.sessionEpoch, "ready", seed.actorID,
		"fresh baseline runner ready", uuid.Nil, 3, candidate.sessionEpoch, candidate.version,
	)
	checkpointID := createSandboxCheckpointCanary(
		t, ctx, database, candidate.id, seed.actorID, "fresh baseline freeze checkpoint",
	)
	var sessionVersion int64
	if err := database.QueryRowContext(ctx, `
SELECT attach_sandbox_session_checkpoint($1, 3, $2, $3, $4)
`, sessionID, candidate.sessionEpoch, seed.actorID, checkpointID).Scan(&sessionVersion); err != nil {
		t.Fatalf("attach fresh baseline checkpoint: %v", err)
	}
	if sessionVersion != 4 {
		t.Fatalf("fresh baseline Session version = %d, want 4", sessionVersion)
	}
	assertFreshBaseWorkspaceTupleIsNull(t, ctx, database, "sandbox_sessions", sessionID)

	// A non-NULL tuple cannot masquerade as the exact NULL Candidate/Snapshot
	// lineage even though every supplied UUID/hash is otherwise valid history.
	mismatchedSessionID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO sandbox_sessions (
  id, schema_version, project_id, actor_id, candidate_id, repository_snapshot_id,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  runner_image_digest,
  candidate_version, candidate_journal_sequence,
  candidate_session_epoch, candidate_writer_lease_epoch,
  candidate_tree_store, candidate_tree_owner_id, candidate_tree_ref,
  candidate_tree_content_hash, candidate_tree_hash,
  candidate_dirty, candidate_conflicted, candidate_stale, candidate_rebase_required,
  state, version, session_epoch,
  cpu_millis, memory_bytes, workspace_bytes, pid_limit, preview_port_limit,
  idle_hibernate_seconds, max_runtime_seconds
)
SELECT $1, 'sandbox-session/v1', candidate.project_id, $2, candidate.id, candidate.repository_snapshot_id,
       candidate.build_manifest_id, candidate.build_manifest_hash,
       candidate.build_contract_id, candidate.build_contract_hash,
       candidate.full_stack_template_id, candidate.full_stack_template_hash,
       $3, $4, $5, $6,
       candidate.version, candidate.journal_sequence,
       candidate.session_epoch, candidate.writer_lease_epoch,
       candidate.current_tree_store, candidate.current_tree_owner_id, candidate.current_tree_ref,
       candidate.current_tree_content_hash, candidate.current_tree_hash,
       candidate.dirty, candidate.conflicted, candidate.stale, candidate.rebase_required,
       'provisioning', 1, candidate.session_epoch,
       2000, 2147483648, 8589934592, 1024, 4, 900, 7200
FROM candidate_workspaces AS candidate
WHERE candidate.id = $7
`, mismatchedSessionID, seed.actorID,
		seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceHash,
		applicationBuildContractCanaryDigest("fresh-baseline-mismatch-runner"), candidate.id,
	); err == nil || !strings.Contains(err.Error(), "exact live Candidate and RepositorySnapshot lineage") {
		t.Fatalf("non-NULL Sandbox baseline escaped exact NULL lineage: %v", err)
	}

	var baseTreeHash string
	if err := database.QueryRowContext(ctx, `
SELECT base_tree_hash FROM candidate_workspaces WHERE id = $1
`, candidate.id).Scan(&baseTreeHash); err != nil {
		t.Fatal(err)
	}
	proposalID := uuid.New()
	verificationReceiptID := uuid.New()
	verificationReceiptHash := applicationBuildContractCanaryDigest("fresh-baseline-verification-receipt")
	transaction, err = database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO implementation_proposals (
  id, project_id, build_manifest_id, base_workspace_revision_id,
  status, version, content_store, content_ref, content_hash, payload_hash,
  operation_count, accepted_count, rejected_count, created_by, created_at,
  execution_source, candidate_snapshot_id, candidate_base_tree_hash, candidate_tree_hash,
  application_build_contract_id, application_build_contract_hash,
  candidate_verification_binding_version,
  candidate_verification_receipt_id, candidate_verification_receipt_hash,
  unimplemented_count, blocking_diagnostic_count
) VALUES (
  $1, $2, $3, NULL,
  'open', 1, 'blob', $4, $5, $5,
  1, 0, 0, $6, statement_timestamp(),
  'candidate_freeze', $7, $8, $9, $10, $11,
  'candidate-verification-binding/v1', $12, $13, 0, 0
)
`, proposalID, seed.projectID, seed.manifestID,
		"blob://fresh-candidate-proposals/"+proposalID.String(),
		applicationBuildContractCanaryDigest("fresh-candidate-proposal"), seed.actorID,
		checkpointID, baseTreeHash, candidate.treeHash, seed.contractID, seed.contractHash,
		verificationReceiptID, verificationReceiptHash); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("seed fresh Candidate Proposal identity: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit fresh Candidate Proposal identity: %v", err)
	}

	if _, err := database.ExecContext(ctx, `
ALTER TABLE candidate_implementation_freezes
DISABLE TRIGGER candidate_implementation_freeze_verification_guard
`); err != nil {
		t.Fatal(err)
	}
	freezeID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_implementation_freezes (
  id, project_id, session_id, candidate_id, candidate_snapshot_id,
  implementation_proposal_id, request_key, request_hash,
  session_version, candidate_version, journal_sequence,
  session_epoch, writer_lease_epoch, base_tree_hash,
  candidate_tree_store, candidate_tree_owner_id, candidate_tree_ref,
  candidate_tree_content_hash, candidate_tree_hash,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  proposal_payload_hash, operation_count, reason, created_by
) VALUES (
  $1, $2, $3, $4, $5,
  $6, $7, $8,
  $9, $10, $11, $12, $13, $14,
  $15, $16, $17, $18, $19,
  $20, $21, $22, $23, $24, $25,
  NULL, NULL, NULL, $26, 1, 'freeze exact fresh Candidate baseline', $27
)
`, freezeID, seed.projectID, sessionID, candidate.id, checkpointID,
		proposalID, "fresh-baseline-freeze-"+freezeID.String(),
		applicationBuildContractCanaryDigest("fresh-baseline-freeze-request"),
		sessionVersion, candidate.version, candidate.journalSequence,
		candidate.sessionEpoch, candidate.writerLeaseEpoch, baseTreeHash,
		candidate.treeStore, candidate.treeOwnerID, candidate.treeRef,
		candidate.treeContentHash, candidate.treeHash,
		seed.manifestID, seed.manifestHash, seed.contractID, seed.contractHash,
		seed.fullStackID, seed.fullStackHash,
		applicationBuildContractCanaryDigest("fresh-candidate-proposal"), seed.actorID,
	); err != nil {
		t.Fatalf("exact fresh Candidate freeze was rejected: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
ALTER TABLE candidate_implementation_freezes
ENABLE TRIGGER candidate_implementation_freeze_verification_guard
`); err != nil {
		t.Fatal(err)
	}
	assertFreshBaseWorkspaceTupleIsNull(
		t, ctx, database, "candidate_implementation_freezes", freezeID,
	)

	down, err := files.ReadFile("000054_fresh_candidate_sandbox_baseline.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err == nil || !strings.Contains(err.Error(), "explicit handling of fresh Candidate/Sandbox rows") {
		t.Fatalf("rollback did not fail closed with fresh rows: %v", err)
	}

	// Remove only the test's fresh downstream projections under replication
	// role; child rows may remain because the temporary schema is dropped after
	// the canary. This proves a clean legacy-only database can restore NOT NULL.
	transaction, err = database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM candidate_implementation_freezes WHERE id = $1`, freezeID); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM sandbox_sessions WHERE id = $1`, sessionID); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("rollback fresh baseline migration after explicit handling: %v", err)
	}

	for _, table := range []string{"sandbox_sessions", "candidate_implementation_freezes"} {
		var notNullCount int
		if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = $1
  AND column_name IN (
    'base_workspace_artifact_id',
    'base_workspace_revision_id',
    'base_workspace_content_hash'
  )
  AND is_nullable = 'NO'
`, table).Scan(&notNullCount); err != nil {
			t.Fatal(err)
		}
		if notNullCount != 3 {
			t.Fatalf("rollback restored %d NOT NULL base Workspace columns on %s, want 3", notNullCount, table)
		}
	}
	var restoredDefinition string
	if err := database.QueryRowContext(ctx, `
SELECT pg_get_functiondef('validate_sandbox_session_insert()'::regprocedure)
`).Scan(&restoredDefinition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(restoredDefinition, "candidate.base_workspace_revision_id = NEW.base_workspace_revision_id") ||
		strings.Contains(restoredDefinition, "candidate.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id") {
		t.Fatalf("rollback did not restore the prior SandboxSession function definition")
	}
}

func applyFreshCandidateBaselineMigrationsThrough(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	last string,
) {
	t.Helper()
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name > last {
			break
		}
		migration, readErr := files.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, execErr := database.ExecContext(ctx, string(migration)); execErr != nil {
			t.Fatalf("apply prerequisite migration %s: %v", name, execErr)
		}
	}
}

func assertFreshBaseWorkspaceTupleIsNull(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	table string,
	id uuid.UUID,
) {
	t.Helper()
	var allNull bool
	query := `
SELECT base_workspace_artifact_id IS NULL
   AND base_workspace_revision_id IS NULL
   AND base_workspace_content_hash IS NULL
FROM ` + table + ` WHERE id = $1`
	if err := database.QueryRowContext(ctx, query, id).Scan(&allNull); err != nil {
		t.Fatal(err)
	}
	if !allNull {
		t.Fatalf("%s %s does not preserve an all-NULL fresh base Workspace tuple", table, id)
	}
}
