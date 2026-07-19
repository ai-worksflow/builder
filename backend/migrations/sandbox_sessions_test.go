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

func TestSandboxSessionsMigrationDeclaresImmutableFencedPersistence(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000025_sandbox_sessions.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000025_sandbox_sessions.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TABLE sandbox_sessions",
		"CREATE TABLE sandbox_session_template_releases",
		"CREATE TABLE sandbox_session_services",
		"CREATE TABLE sandbox_session_ports",
		"CREATE TABLE sandbox_session_transition_events",
		"candidate_session_epoch bigint NOT NULL",
		"candidate_writer_lease_epoch bigint NOT NULL",
		"candidate_tree_owner_id uuid NOT NULL",
		"candidate_tree_content_hash text NOT NULL",
		"build_manifest_hash ~ '^(sha256:)?[0-9a-f]{64}$'",
		"build_contract_hash ~ '^(sha256:)?[0-9a-f]{64}$'",
		"sandbox_checkpoint_is_exact",
		"candidate.dirty",
		"dirty Candidate requires an exact current CandidateSnapshot before suspend, terminate, or cancel",
		"dirty resumed Candidate requires an exact current CandidateSnapshot before ready",
		"parent.state IN ('ready', 'resuming')",
		"session.state IN ('ready', 'resuming')",
		"candidate_session_epoch_to = session_epoch_to",
		"rotate_candidate_workspace_session",
		"candidate.version = parent.candidate_version + 1",
		"candidate.writer_lease_epoch",
		"candidate.current_tree_owner_id",
		"candidate.current_tree_content_hash",
		"SandboxSession transition events are append-only",
		"SandboxSession exact lineage, quota, TTL, and runner identity are immutable",
		"event.creation_transaction_id = txid_current()",
		"event_count <> parent.version - 1",
		"DEFERRABLE INITIALLY DEFERRED",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("SandboxSession migration is missing %q", expected)
		}
	}
	lower := strings.ToLower(text)
	for _, forbidden := range []string{
		"allowed_actions",
		"blocking_reasons",
		"browser.disconnected",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("SandboxSession persistence contains derived or transport-only state %q", forbidden)
		}
	}
	downText := string(down)
	for _, expected := range []string{
		"DROP TABLE IF EXISTS sandbox_session_transition_events",
		"DROP TABLE IF EXISTS sandbox_session_ports",
		"DROP TABLE IF EXISTS sandbox_session_services",
		"DROP TABLE IF EXISTS sandbox_session_template_releases",
		"DROP TABLE IF EXISTS sandbox_sessions",
		"DROP FUNCTION IF EXISTS transition_sandbox_session",
		"DROP FUNCTION IF EXISTS sandbox_checkpoint_is_exact",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("SandboxSession rollback is missing %q", expected)
		}
	}
}

func TestSandboxSessionsMigrationPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "sandbox_session_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	if err := Up(ctx, database); err != nil {
		t.Fatalf("migrations.Up failed in temporary schema: %v", err)
	}

	for _, table := range []string{
		"sandbox_sessions", "sandbox_session_template_releases",
		"sandbox_session_services", "sandbox_session_ports",
		"sandbox_session_transition_events",
	} {
		var actual string
		if err := database.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, table).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != table {
			t.Fatalf("expected %s table, got %q", table, actual)
		}
	}
	var derivedColumnCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name LIKE 'sandbox_session%'
  AND column_name IN ('allowed_actions', 'blocking_reasons')
`).Scan(&derivedColumnCount); err != nil {
		t.Fatal(err)
	}
	if derivedColumnCount != 0 {
		t.Fatalf("SandboxSession persisted %d derived action/blocking columns", derivedColumnCount)
	}

	seed := seedRepositoryCandidateCanary(t, ctx, database)
	// Keep this canary independent of the shared fixture's raw/prefixed hash choice.
	if err := database.QueryRowContext(ctx, `
SELECT contract_hash FROM application_build_contracts WHERE id = $1
`, seed.contractID).Scan(&seed.contractHash); err != nil {
		t.Fatal(err)
	}

	mainCandidate := createSandboxCandidateCanary(t, ctx, database, seed, "main")
	mainSessionID := uuid.New()
	insertSandboxSessionCanary(t, ctx, database, seed, mainCandidate.id, mainSessionID, uuid.Nil, true)
	assertSandboxSessionExactLineage(t, ctx, database, mainSessionID)
	assertSandboxSessionSealedConfiguration(t, ctx, database, mainSessionID)
	assertIncompleteSandboxSessionRejected(t, ctx, database, seed, mainCandidate.id)

	if _, err := transitionSandboxSessionCanary(
		ctx, database, mainSessionID, 1, 1, "ready", seed.actorID, "skip start", uuid.Nil,
	); err == nil {
		t.Fatal("illegal provisioning-to-ready transition was accepted")
	}
	assertSandboxTransition(t, ctx, database, mainSessionID, 1, 1, "starting", seed.actorID, "runner allocated", uuid.Nil, 2, 1, 1)
	if _, err := transitionSandboxSessionCanary(
		ctx, database, mainSessionID, 1, 1, "starting", seed.actorID, "stale retry", uuid.Nil,
	); err == nil {
		t.Fatal("stale SandboxSession version CAS was accepted")
	}
	assertSandboxTransition(t, ctx, database, mainSessionID, 2, 1, "ready", seed.actorID, "health checks passed", uuid.Nil, 3, 1, 1)

	mainCandidate = dirtySandboxCandidateCanary(t, ctx, database, seed.actorID, mainCandidate, "main-edit")
	var syncedVersion, syncedEpoch, syncedCandidateVersion int64
	var syncedTreeHash string
	if err := database.QueryRowContext(ctx, `
SELECT session_version, session_epoch, candidate_version, candidate_tree_hash
FROM sync_sandbox_session_candidate($1, 3, 1, $2)
`, mainSessionID, seed.actorID).Scan(
		&syncedVersion, &syncedEpoch, &syncedCandidateVersion, &syncedTreeHash,
	); err != nil {
		t.Fatalf("sync exact dirty Candidate into SandboxSession: %v", err)
	}
	if syncedVersion != 4 || syncedEpoch != 1 || syncedCandidateVersion != 3 || syncedTreeHash != mainCandidate.treeHash {
		t.Fatalf("unexpected Candidate sync: version=%d epoch=%d candidate=%d tree=%q",
			syncedVersion, syncedEpoch, syncedCandidateVersion, syncedTreeHash)
	}
	if _, err := transitionSandboxSessionCanary(
		ctx, database, mainSessionID, 4, 1, "suspending", seed.actorID, "idle hibernate", uuid.Nil,
	); err == nil || !strings.Contains(err.Error(), "exact current CandidateSnapshot") {
		t.Fatalf("dirty suspend without an exact checkpoint was not rejected: %v", err)
	}
	checkpointV3 := createSandboxCheckpointCanary(t, ctx, database, mainCandidate.id, seed.actorID, "pre-suspend")
	var attachedVersion int64
	if err := database.QueryRowContext(ctx, `
SELECT attach_sandbox_session_checkpoint($1, 4, 1, $2, $3)
`, mainSessionID, seed.actorID, checkpointV3).Scan(&attachedVersion); err != nil {
		t.Fatalf("attach exact CandidateSnapshot: %v", err)
	}
	if attachedVersion != 5 {
		t.Fatalf("checkpoint attach advanced to version %d, want 5", attachedVersion)
	}
	assertSandboxTransition(t, ctx, database, mainSessionID, 5, 1, "suspending", seed.actorID, "idle hibernate", uuid.Nil, 6, 1, 3)
	assertSandboxTransition(t, ctx, database, mainSessionID, 6, 1, "suspended", seed.actorID, "volume retained", uuid.Nil, 7, 1, 3)
	assertSandboxTransition(t, ctx, database, mainSessionID, 7, 1, "resuming", seed.actorID, "browser returned", uuid.Nil, 8, 2, 4)
	if _, err := transitionSandboxSessionCanary(
		ctx, database, mainSessionID, 8, 1, "ready", seed.actorID, "old socket", uuid.Nil,
	); err == nil {
		t.Fatal("pre-resume SandboxSession epoch was accepted")
	}
	if _, err := transitionSandboxSessionCanary(
		ctx, database, mainSessionID, 8, 2, "ready", seed.actorID, "runner healthy", uuid.Nil,
	); err == nil || !strings.Contains(err.Error(), "exact current CandidateSnapshot") {
		t.Fatalf("dirty resume without a fresh exact checkpoint was not rejected: %v", err)
	}

	mainCandidate = readSandboxCandidateCanary(t, ctx, database, mainCandidate.id)
	mainCandidate = acquireSandboxCandidateLeaseCanary(t, ctx, database, seed.actorID, mainCandidate)
	if err := database.QueryRowContext(ctx, `
SELECT session_version, session_epoch, candidate_version, candidate_tree_hash
FROM sync_sandbox_session_candidate($1, 8, 2, $2)
`, mainSessionID, seed.actorID).Scan(
		&syncedVersion, &syncedEpoch, &syncedCandidateVersion, &syncedTreeHash,
	); err != nil {
		t.Fatalf("sync post-resume Candidate lease fence: %v", err)
	}
	if syncedVersion != 9 || syncedCandidateVersion != mainCandidate.version {
		t.Fatalf("post-resume sync advanced to session=%d candidate=%d", syncedVersion, syncedCandidateVersion)
	}
	checkpointV5 := createSandboxCheckpointCanary(t, ctx, database, mainCandidate.id, seed.actorID, "post-resume")
	if err := database.QueryRowContext(ctx, `
SELECT attach_sandbox_session_checkpoint($1, 9, 2, $2, $3)
`, mainSessionID, seed.actorID, checkpointV5).Scan(&attachedVersion); err != nil {
		t.Fatalf("attach post-resume exact CandidateSnapshot: %v", err)
	}
	if attachedVersion != 10 {
		t.Fatalf("post-resume checkpoint attach advanced to version %d, want 10", attachedVersion)
	}
	assertSandboxTransition(t, ctx, database, mainSessionID, 10, 2, "ready", seed.actorID, "runner healthy", uuid.Nil, 11, 2, 5)
	assertSandboxTransition(t, ctx, database, mainSessionID, 11, 2, "terminating", seed.actorID, "user finished", uuid.Nil, 12, 2, 5)
	assertSandboxTransition(t, ctx, database, mainSessionID, 12, 2, "failed", seed.actorID, "runner cleanup failed", uuid.Nil, 13, 2, 5)
	assertSandboxTransition(t, ctx, database, mainSessionID, 13, 2, "terminating", seed.actorID, "retry cleanup", uuid.Nil, 14, 2, 5)
	assertSandboxTransition(t, ctx, database, mainSessionID, 14, 2, "terminated", seed.actorID, "resources released", uuid.Nil, 15, 2, 5)
	assertSandboxSessionEventChain(t, ctx, database, mainSessionID, 15, 2)

	cancelCandidate := createSandboxCandidateCanary(t, ctx, database, seed, "cancel")
	cancelCandidate = dirtySandboxCandidateCanary(t, ctx, database, seed.actorID, cancelCandidate, "cancel-edit")
	cancelSessionID := uuid.New()
	insertSandboxSessionCanary(t, ctx, database, seed, cancelCandidate.id, cancelSessionID, uuid.Nil, true)
	if _, err := transitionSandboxSessionCanary(
		ctx, database, cancelSessionID, 1, 1, "terminating", seed.actorID, "cancel", uuid.Nil,
	); err == nil || !strings.Contains(err.Error(), "exact current CandidateSnapshot") {
		t.Fatalf("dirty cancel without checkpoint was not rejected: %v", err)
	}
	cancelCheckpoint := createSandboxCheckpointCanary(t, ctx, database, cancelCandidate.id, seed.actorID, "pre-cancel")
	assertSandboxTransition(t, ctx, database, cancelSessionID, 1, 1, "terminating", seed.actorID, "cancel", cancelCheckpoint, 2, 1, 3)
	assertSandboxTransition(t, ctx, database, cancelSessionID, 2, 1, "terminated", seed.actorID, "cancelled resources released", uuid.Nil, 3, 1, 3)

	failedCandidate := createSandboxCandidateCanary(t, ctx, database, seed, "failed")
	failedCandidate = dirtySandboxCandidateCanary(t, ctx, database, seed.actorID, failedCandidate, "failed-edit")
	failedSessionID := uuid.New()
	insertSandboxSessionCanary(t, ctx, database, seed, failedCandidate.id, failedSessionID, uuid.Nil, true)
	assertSandboxTransition(t, ctx, database, failedSessionID, 1, 1, "failed", seed.actorID, "provisioner failed", uuid.Nil, 2, 1, 3)
	if _, err := transitionSandboxSessionCanary(
		ctx, database, failedSessionID, 2, 1, "terminating", seed.actorID, "cleanup", uuid.Nil,
	); err == nil || !strings.Contains(err.Error(), "exact current CandidateSnapshot") {
		t.Fatalf("dirty failed cleanup fail-opened without checkpoint: %v", err)
	}
	failedCheckpoint := createSandboxCheckpointCanary(t, ctx, database, failedCandidate.id, seed.actorID, "failed-cleanup")
	assertSandboxTransition(t, ctx, database, failedSessionID, 2, 1, "terminating", seed.actorID, "cleanup", failedCheckpoint, 3, 1, 3)
	assertSandboxTransition(t, ctx, database, failedSessionID, 3, 1, "terminated", seed.actorID, "cleanup complete", uuid.Nil, 4, 1, 3)

	cleanCandidate := createSandboxCandidateCanary(t, ctx, database, seed, "clean")
	cleanSessionID := uuid.New()
	insertSandboxSessionCanary(t, ctx, database, seed, cleanCandidate.id, cleanSessionID, uuid.Nil, true)
	assertSandboxTransition(t, ctx, database, cleanSessionID, 1, 1, "starting", seed.actorID, "runner allocated", uuid.Nil, 2, 1, 1)
	assertSandboxTransition(t, ctx, database, cleanSessionID, 2, 1, "ready", seed.actorID, "runner ready", uuid.Nil, 3, 1, 1)
	assertSandboxTransition(t, ctx, database, cleanSessionID, 3, 1, "terminating", seed.actorID, "clean termination", uuid.Nil, 4, 1, 1)
	assertSandboxTransition(t, ctx, database, cleanSessionID, 4, 1, "terminated", seed.actorID, "clean resources released", uuid.Nil, 5, 1, 1)

	var browserVersionBefore, browserVersionAfter int64
	if err := database.QueryRowContext(ctx, `SELECT version FROM sandbox_sessions WHERE id = $1`, cancelSessionID).Scan(&browserVersionBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := transitionSandboxSessionCanary(
		ctx, database, cancelSessionID, browserVersionBefore, 1, "browser.disconnected", seed.actorID, "socket closed", uuid.Nil,
	); err == nil {
		t.Fatal("browser disconnect was accepted as a persisted SandboxSession transition")
	}
	if err := database.QueryRowContext(ctx, `SELECT version FROM sandbox_sessions WHERE id = $1`, cancelSessionID).Scan(&browserVersionAfter); err != nil {
		t.Fatal(err)
	}
	if browserVersionAfter != browserVersionBefore {
		t.Fatalf("browser disconnect changed SandboxSession version from %d to %d", browserVersionBefore, browserVersionAfter)
	}

	assertSandboxDirectMutationsRejected(t, ctx, database, mainSessionID)
	assertSandboxDeferredChainRejectsBypass(t, ctx, database, mainSessionID)

	// Migration 000058 is an intentionally non-downgradable authority boundary.
	// Keep the full-current-schema canary above, but exercise migration 000025's
	// historical standalone rollback contract in a second schema whose forward
	// boundary ends at 000052. This avoids fabricating a downgrade through 000058
	// merely to remove unrelated release-delivery objects.
	rollbackSchema := "sandbox_session_rollback_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+rollbackSchema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+rollbackSchema+`" CASCADE`)
	})
	rollbackDatabase, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, rollbackSchema))
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackDatabase.Close()
	applySandboxSessionMigrationsThrough(
		t, ctx, rollbackDatabase, "000052_verification_execution_cleanup_obligations.up.sql",
	)

	// Remove post-000025 resources in strict reverse order before exercising
	// the SandboxSession migration's standalone rollback.
	for _, name := range []string{
		"000052_verification_execution_cleanup_obligations.down.sql",
		"000051_verification_output_truncation_gate.down.sql",
		"000050_candidate_abandon_sandbox_reconciliation.down.sql",
		"000049_candidate_sandbox_lifecycle_write_gate.down.sql",
		"000048_release_production_head_fencing.down.sql",
		"000047_sandbox_lifecycle_deadlines.down.sql",
		"000046_release_delivery_control_plane.down.sql",
		"000045_release_bundle_publish_gate.down.sql",
		"000044_canonical_verification_execution.down.sql",
		"000043_canonical_quality_release_authority.down.sql",
		"000042_candidate_verification_worker_leases.down.sql",
		"000041_candidate_freeze_verification_gate.down.sql",
		"000040_candidate_verification_receipts.down.sql",
		"000039_candidate_verification_control_plane.down.sql",
		"000038_candidate_freeze_build_contract_guard.down.sql",
		"000037_candidate_freeze_sandbox_projection.down.sql",
		"000036_candidate_implementation_freezes.down.sql",
		"000035_agent_patch_undos.down.sql",
		"000034_agent_patch_merges.down.sql",
		"000033_agent_execution_evidence.down.sql",
		"000032_agent_stream_outbox.down.sql",
		"000031_agent_control_plane.down.sql",
		"000028_sandbox_terminals.down.sql",
		"000027_sandbox_runtime_processes.down.sql",
	} {
		prerequisite, readErr := files.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, execErr := rollbackDatabase.ExecContext(ctx, string(prerequisite)); execErr != nil {
			t.Fatalf("SandboxSession rollback prerequisite %s failed: %v", name, execErr)
		}
	}
	down, err := files.ReadFile("000025_sandbox_sessions.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rollbackDatabase.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("SandboxSession rollback failed: %v", err)
	}
	var rolledBack sql.NullString
	if err := rollbackDatabase.QueryRowContext(ctx, `SELECT to_regclass('sandbox_sessions')::text`).Scan(&rolledBack); err != nil {
		t.Fatal(err)
	}
	if rolledBack.Valid {
		t.Fatalf("sandbox_sessions survived rollback as %q", rolledBack.String)
	}
	var upstream string
	if err := rollbackDatabase.QueryRowContext(ctx, `SELECT to_regclass('candidate_workspaces')::text`).Scan(&upstream); err != nil {
		t.Fatal(err)
	}
	if upstream != "candidate_workspaces" {
		t.Fatalf("000025 rollback removed upstream Candidate persistence: %q", upstream)
	}
}

func applySandboxSessionMigrationsThrough(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	target string,
) {
	t.Helper()
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name > target {
			break
		}
		migration, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, string(migration)); err != nil {
			transaction.Rollback()
			t.Fatalf("apply rollback-canary prerequisite %s: %v", name, err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatalf("commit rollback-canary prerequisite %s: %v", name, err)
		}
	}
}

type sandboxCandidateCanary struct {
	id               uuid.UUID
	snapshotID       uuid.UUID
	version          int64
	journalSequence  int64
	sessionEpoch     int64
	writerLeaseEpoch int64
	treeStore        string
	treeOwnerID      uuid.UUID
	treeRef          string
	treeContentHash  string
	treeHash         string
	fileCount        int
	byteSize         int64
}

func createSandboxCandidateCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	seed repositoryCandidateCanarySeed,
	identity string,
	treeStores ...string,
) sandboxCandidateCanary {
	t.Helper()
	treeStore := "blob"
	if len(treeStores) == 1 {
		treeStore = treeStores[0]
	} else if len(treeStores) > 1 {
		t.Fatal("at most one Candidate tree store override is allowed")
	}
	candidate := sandboxCandidateCanary{
		id:               uuid.New(),
		snapshotID:       uuid.New(),
		version:          1,
		sessionEpoch:     1,
		writerLeaseEpoch: 0,
		treeStore:        treeStore,
		treeContentHash:  applicationBuildContractCanaryDigest("sandbox-tree-content-" + identity),
		treeHash:         applicationBuildContractCanaryDigest("sandbox-tree-" + identity),
		fileCount:        1,
		byteSize:         128,
	}
	candidate.treeOwnerID = candidate.snapshotID
	candidate.treeRef = "blob://repository-snapshots/" + candidate.snapshotID.String() + "/tree"
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_snapshots (
  id, schema_version, project_id,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
  tree_file_count, tree_byte_size, created_by, created_at
) VALUES (
  $1, 'repository-snapshot/v1', $2, $3, $4, $5, $6, $7, $8,
  $9, $10, $11, $12, $1, $13, $14, $15, $16, $17, $18, $19
)
`, candidate.snapshotID, seed.projectID, seed.manifestID, seed.manifestHash,
		seed.contractID, seed.contractHash, seed.fullStackID, seed.fullStackHash,
		seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceHash,
		candidate.treeStore, candidate.treeRef, candidate.treeContentHash, candidate.treeHash,
		candidate.fileCount, candidate.byteSize, seed.actorID, createdAt.Add(-time.Second)); err != nil {
		t.Fatalf("insert Sandbox RepositorySnapshot %s: %v", identity, err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_workspaces (
  id, schema_version, project_id, repository_snapshot_id,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  base_tree_store, base_tree_owner_id, base_tree_ref, base_tree_content_hash, base_tree_hash,
  current_tree_store, current_tree_owner_id, current_tree_ref, current_tree_content_hash, current_tree_hash,
  current_tree_file_count, current_tree_byte_size,
  status, dirty, conflicted, stale, rebase_required,
  session_epoch, version, journal_sequence, writer_lease_epoch,
  created_by, created_at, updated_at
) VALUES (
  $1, 'candidate-workspace/v1', $2, $3, $4, $5, $6, $7, $8, $9,
  $10, $11, $12,
  $13, $3, $14, $15, $16, $13, $3, $14, $15, $16, $17, $18,
  'active', false, false, false, false, 1, 1, 0, 0, $19, $20, $20
)
`, candidate.id, seed.projectID, candidate.snapshotID,
		seed.manifestID, seed.manifestHash, seed.contractID, seed.contractHash,
		seed.fullStackID, seed.fullStackHash,
		seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceHash,
		candidate.treeStore, candidate.treeRef, candidate.treeContentHash, candidate.treeHash,
		candidate.fileCount, candidate.byteSize, seed.actorID, createdAt); err != nil {
		t.Fatalf("insert Sandbox Candidate %s: %v", identity, err)
	}
	return candidate
}

func acquireSandboxCandidateLeaseCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	actorID uuid.UUID,
	candidate sandboxCandidateCanary,
) sandboxCandidateCanary {
	t.Helper()
	var leaseExpires time.Time
	if err := database.QueryRowContext(ctx, `
SELECT candidate_version, session_epoch, writer_lease_epoch, writer_lease_expires_at
FROM acquire_candidate_workspace_lease($1, $2, $3, 300)
`, candidate.id, candidate.version, actorID).Scan(
		&candidate.version, &candidate.sessionEpoch, &candidate.writerLeaseEpoch, &leaseExpires,
	); err != nil {
		t.Fatalf("acquire Candidate lease: %v", err)
	}
	if !leaseExpires.After(time.Now()) {
		t.Fatalf("Candidate lease is not live: %s", leaseExpires)
	}
	return candidate
}

func dirtySandboxCandidateCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	actorID uuid.UUID,
	candidate sandboxCandidateCanary,
	identity string,
) sandboxCandidateCanary {
	t.Helper()
	candidate = acquireSandboxCandidateLeaseCanary(t, ctx, database, actorID, candidate)
	afterContentHash := applicationBuildContractCanaryDigest("sandbox-after-content-" + identity)
	afterTreeHash := applicationBuildContractCanaryDigest("sandbox-after-tree-" + identity)
	afterTreeRef := "blob://candidate-workspaces/" + candidate.id.String() + "/trees/" + strings.TrimPrefix(afterContentHash, "sha256:")
	fileHash := applicationBuildContractCanaryDigest("sandbox-file-" + identity)
	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_workspace_journal (
  candidate_id, sequence, candidate_version_from, candidate_version_to,
  session_epoch, writer_lease_epoch, actor_id, attribution,
  operation_id, operation_kind, path, content_hash, byte_size, file_mode,
  before_tree_store, before_tree_owner_id, before_tree_ref, before_tree_content_hash, before_tree_hash,
  after_tree_store, after_tree_owner_id, after_tree_ref, after_tree_content_hash, after_tree_hash,
  after_tree_file_count, after_tree_byte_size
) VALUES (
  $1, 1, $2::bigint, $2::bigint + 1, $3, $4, $5, 'user',
  $6, 'file.upsert', 'apps/web/page.tsx', $7, 256, '100644',
  $8, $9, $10, $11, $12,
  'blob', $1, $13, $14, $15, 2, 384
)
`, candidate.id, candidate.version, candidate.sessionEpoch, candidate.writerLeaseEpoch,
		actorID, "sandbox-op-"+identity, fileHash,
		candidate.treeStore, candidate.treeOwnerID, candidate.treeRef,
		candidate.treeContentHash, candidate.treeHash,
		afterTreeRef, afterContentHash, afterTreeHash); err != nil {
		t.Fatalf("append dirty Candidate journal %s: %v", identity, err)
	}
	candidate.version++
	candidate.treeStore = "blob"
	candidate.treeOwnerID = candidate.id
	candidate.treeRef = afterTreeRef
	candidate.treeContentHash = afterContentHash
	candidate.treeHash = afterTreeHash
	candidate.fileCount = 2
	candidate.byteSize = 384
	return readSandboxCandidateCanary(t, ctx, database, candidate.id)
}

func readSandboxCandidateCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	candidateID uuid.UUID,
) sandboxCandidateCanary {
	t.Helper()
	candidate := sandboxCandidateCanary{id: candidateID}
	if err := database.QueryRowContext(ctx, `
SELECT repository_snapshot_id, version, journal_sequence, session_epoch, writer_lease_epoch,
       current_tree_store, current_tree_owner_id, current_tree_ref,
       current_tree_content_hash, current_tree_hash,
       current_tree_file_count, current_tree_byte_size
FROM candidate_workspaces
WHERE id = $1
`, candidateID).Scan(
		&candidate.snapshotID, &candidate.version, &candidate.journalSequence,
		&candidate.sessionEpoch, &candidate.writerLeaseEpoch,
		&candidate.treeStore, &candidate.treeOwnerID, &candidate.treeRef,
		&candidate.treeContentHash, &candidate.treeHash, &candidate.fileCount, &candidate.byteSize,
	); err != nil {
		t.Fatalf("read Candidate: %v", err)
	}
	return candidate
}

func insertSandboxSessionCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	seed repositoryCandidateCanarySeed,
	candidateID, sessionID, checkpointID uuid.UUID,
	withConfiguration bool,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	var checkpoint any
	if checkpointID != uuid.Nil {
		checkpoint = checkpointID
	}
	if _, err := transaction.ExecContext(ctx, `
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
  latest_checkpoint_id, state, version, session_epoch,
  cpu_millis, memory_bytes, workspace_bytes, pid_limit, preview_port_limit,
  idle_hibernate_seconds, max_runtime_seconds
)
SELECT $1, 'sandbox-session/v1', candidate.project_id, $2, candidate.id, candidate.repository_snapshot_id,
       candidate.build_manifest_id, candidate.build_manifest_hash,
       candidate.build_contract_id, candidate.build_contract_hash,
       candidate.full_stack_template_id, candidate.full_stack_template_hash,
       candidate.base_workspace_artifact_id, candidate.base_workspace_revision_id,
       candidate.base_workspace_content_hash,
       $3,
       candidate.version, candidate.journal_sequence,
       candidate.session_epoch, candidate.writer_lease_epoch,
       candidate.current_tree_store, candidate.current_tree_owner_id, candidate.current_tree_ref,
       candidate.current_tree_content_hash, candidate.current_tree_hash,
       candidate.dirty, candidate.conflicted, candidate.stale, candidate.rebase_required,
       $4, 'provisioning', 1, candidate.session_epoch,
       2000, 2147483648, 8589934592, 1024, 4, 900, 7200
FROM candidate_workspaces AS candidate
WHERE candidate.id = $5
`, sessionID, seed.actorID, applicationBuildContractCanaryDigest("sandbox-runner-image"), checkpoint, candidateID); err != nil {
		t.Fatalf("insert SandboxSession parent: %v", err)
	}
	if withConfiguration {
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO sandbox_session_template_releases (
  session_id, ordinal, role, template_release_id, template_release_content_hash
)
SELECT $1, ordinal, role, template_release_id, template_release_content_hash
FROM application_build_contract_template_releases
WHERE contract_id = $2
`, sessionID, seed.contractID); err != nil {
			t.Fatalf("insert SandboxSession TemplateRelease projection: %v", err)
		}
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO sandbox_session_services (
  session_id, service_id, kind, profiles,
  template_release_id, template_release_content_hash
)
SELECT $1, role, role, '["dev"]'::jsonb,
       template_release_id, template_release_content_hash
FROM application_build_contract_template_releases
WHERE contract_id = $2
`, sessionID, seed.contractID); err != nil {
			t.Fatalf("insert SandboxSession services: %v", err)
		}
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO sandbox_session_ports (
  session_id, port_name, service_id, port_number, protocol
)
SELECT $1, role || '-http', role, 3000 + ordinal, 'http'
FROM application_build_contract_template_releases
WHERE contract_id = $2
`, sessionID, seed.contractID); err != nil {
			t.Fatalf("insert SandboxSession ports: %v", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit SandboxSession: %v", err)
	}
}

func createSandboxCheckpointCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	candidateID, actorID uuid.UUID,
	reason string,
) uuid.UUID {
	t.Helper()
	checkpointID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_snapshots (
  id, schema_version, candidate_id, project_id,
  candidate_version, journal_sequence, session_epoch, writer_lease_epoch,
  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
  tree_file_count, tree_byte_size, reason, created_by
)
SELECT $1, 'candidate-snapshot/v1', candidate.id, candidate.project_id,
       candidate.version, candidate.journal_sequence,
       candidate.session_epoch, candidate.writer_lease_epoch,
       candidate.current_tree_store, candidate.current_tree_owner_id,
       candidate.current_tree_ref, candidate.current_tree_content_hash,
       candidate.current_tree_hash, candidate.current_tree_file_count,
       candidate.current_tree_byte_size, $2, $3
FROM candidate_workspaces AS candidate
WHERE candidate.id = $4
`, checkpointID, reason, actorID, candidateID); err != nil {
		t.Fatalf("insert exact CandidateSnapshot %s: %v", reason, err)
	}
	return checkpointID
}

type sandboxTransitionCanary struct {
	version          int64
	state            string
	sessionEpoch     int64
	candidateVersion int64
}

func transitionSandboxSessionCanary(
	ctx context.Context,
	database *sql.DB,
	sessionID uuid.UUID,
	expectedVersion, expectedEpoch int64,
	targetState string,
	actorID uuid.UUID,
	reason string,
	checkpointID uuid.UUID,
) (sandboxTransitionCanary, error) {
	var checkpoint any
	if checkpointID != uuid.Nil {
		checkpoint = checkpointID
	}
	var result sandboxTransitionCanary
	err := database.QueryRowContext(ctx, `
SELECT session_version, session_state, session_epoch, candidate_version
FROM transition_sandbox_session($1, $2, $3, $4, $5, $6, $7)
`, sessionID, expectedVersion, expectedEpoch, targetState, actorID, reason, checkpoint).Scan(
		&result.version, &result.state, &result.sessionEpoch, &result.candidateVersion,
	)
	return result, err
}

func assertSandboxTransition(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	sessionID uuid.UUID,
	expectedVersion, expectedEpoch int64,
	targetState string,
	actorID uuid.UUID,
	reason string,
	checkpointID uuid.UUID,
	wantVersion, wantEpoch, wantCandidateVersion int64,
) {
	t.Helper()
	result, err := transitionSandboxSessionCanary(
		ctx, database, sessionID, expectedVersion, expectedEpoch,
		targetState, actorID, reason, checkpointID,
	)
	if err != nil {
		t.Fatalf("transition SandboxSession to %s: %v", targetState, err)
	}
	if result.version != wantVersion || result.state != targetState ||
		result.sessionEpoch != wantEpoch || result.candidateVersion != wantCandidateVersion {
		t.Fatalf("unexpected transition to %s: %+v, want version=%d epoch=%d candidate=%d",
			targetState, result, wantVersion, wantEpoch, wantCandidateVersion)
	}
}

func assertSandboxSessionExactLineage(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	sessionID uuid.UUID,
) {
	t.Helper()
	var exactCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM sandbox_sessions AS session
JOIN candidate_workspaces AS candidate ON candidate.id = session.candidate_id
JOIN repository_snapshots AS snapshot ON snapshot.id = session.repository_snapshot_id
JOIN application_build_contracts AS contract
  ON contract.id = session.build_contract_id
 AND contract.contract_hash = session.build_contract_hash
JOIN full_stack_template_releases AS full_stack
  ON full_stack.id = session.full_stack_template_id
 AND full_stack.content_hash = session.full_stack_template_hash
WHERE session.id = $1
  AND session.project_id = candidate.project_id
  AND session.repository_snapshot_id = candidate.repository_snapshot_id
  AND session.build_manifest_id = candidate.build_manifest_id
  AND session.build_manifest_hash = candidate.build_manifest_hash
  AND session.build_contract_id = candidate.build_contract_id
  AND session.build_contract_hash = candidate.build_contract_hash
  AND session.full_stack_template_id = candidate.full_stack_template_id
  AND session.full_stack_template_hash = candidate.full_stack_template_hash
  AND session.base_workspace_artifact_id = candidate.base_workspace_artifact_id
  AND session.base_workspace_revision_id = candidate.base_workspace_revision_id
  AND session.base_workspace_content_hash = candidate.base_workspace_content_hash
  AND session.candidate_version = candidate.version
  AND session.candidate_journal_sequence = candidate.journal_sequence
  AND session.candidate_session_epoch = candidate.session_epoch
  AND session.candidate_writer_lease_epoch = candidate.writer_lease_epoch
  AND session.candidate_tree_store = candidate.current_tree_store
  AND session.candidate_tree_owner_id = candidate.current_tree_owner_id
  AND session.candidate_tree_ref = candidate.current_tree_ref
  AND session.candidate_tree_content_hash = candidate.current_tree_content_hash
  AND session.candidate_tree_hash = candidate.current_tree_hash
  AND session.candidate_session_epoch = session.session_epoch
  AND session.idle_deadline = session.created_at + make_interval(secs => session.idle_hibernate_seconds)
  AND session.expires_at = session.created_at + make_interval(secs => session.max_runtime_seconds)
  AND session.idle_deadline <= session.expires_at
  AND snapshot.build_contract_id = session.build_contract_id
  AND snapshot.build_contract_hash = session.build_contract_hash
`, sessionID).Scan(&exactCount); err != nil {
		t.Fatal(err)
	}
	if exactCount != 1 {
		t.Fatalf("SandboxSession does not retain exact Candidate/Repository/BuildContract/FullStack lineage: %d", exactCount)
	}
	var releaseDifferenceCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM (
  (SELECT ordinal, role, template_release_id, template_release_content_hash
   FROM sandbox_session_template_releases WHERE session_id = $1
   EXCEPT
   SELECT ordinal, role, template_release_id, template_release_content_hash
   FROM application_build_contract_template_releases
   WHERE contract_id = (SELECT build_contract_id FROM sandbox_sessions WHERE id = $1))
  UNION ALL
  (SELECT ordinal, role, template_release_id, template_release_content_hash
   FROM application_build_contract_template_releases
   WHERE contract_id = (SELECT build_contract_id FROM sandbox_sessions WHERE id = $1)
   EXCEPT
   SELECT ordinal, role, template_release_id, template_release_content_hash
   FROM sandbox_session_template_releases WHERE session_id = $1)
) AS difference
`, sessionID).Scan(&releaseDifferenceCount); err != nil {
		t.Fatal(err)
	}
	if releaseDifferenceCount != 0 {
		t.Fatalf("SandboxSession TemplateRelease lineage differs in %d rows", releaseDifferenceCount)
	}
}

func assertSandboxSessionSealedConfiguration(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	sessionID uuid.UUID,
) {
	t.Helper()
	checks := []struct {
		name  string
		query string
		args  []any
	}{
		{"runner digest", `UPDATE sandbox_sessions SET runner_image_digest = $2 WHERE id = $1`, []any{sessionID, applicationBuildContractCanaryDigest("other-runner")}},
		{"quota", `UPDATE sandbox_sessions SET memory_bytes = memory_bytes + 1 WHERE id = $1`, []any{sessionID}},
		{"TTL", `UPDATE sandbox_sessions SET max_runtime_seconds = max_runtime_seconds + 1 WHERE id = $1`, []any{sessionID}},
		{"service", `UPDATE sandbox_session_services SET profiles = '["dev","debug"]'::jsonb WHERE session_id = $1`, []any{sessionID}},
		{"port", `UPDATE sandbox_session_ports SET protocol = 'tcp' WHERE session_id = $1`, []any{sessionID}},
		{"TemplateRelease", `DELETE FROM sandbox_session_template_releases WHERE session_id = $1`, []any{sessionID}},
	}
	for _, check := range checks {
		if _, err := database.ExecContext(ctx, check.query, check.args...); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("direct %s drift was not rejected: %v", check.name, err)
		}
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO sandbox_session_services (
  session_id, service_id, kind, profiles,
  template_release_id, template_release_content_hash
)
SELECT $1, 'late-service', role, '["dev"]'::jsonb,
       template_release_id, template_release_content_hash
FROM sandbox_session_template_releases
WHERE session_id = $1
LIMIT 1
`, sessionID); err == nil || !strings.Contains(err.Error(), "sealed") {
		t.Fatalf("post-creation service insertion was not rejected: %v", err)
	}
}

func assertIncompleteSandboxSessionRejected(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	seed repositoryCandidateCanarySeed,
	candidateID uuid.UUID,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	sessionID := uuid.New()
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO sandbox_sessions (
  id, schema_version, project_id, actor_id, candidate_id, repository_snapshot_id,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  runner_image_digest, candidate_version, candidate_journal_sequence,
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
       candidate.base_workspace_artifact_id, candidate.base_workspace_revision_id,
       candidate.base_workspace_content_hash, $3,
       candidate.version, candidate.journal_sequence,
       candidate.session_epoch, candidate.writer_lease_epoch,
       candidate.current_tree_store, candidate.current_tree_owner_id, candidate.current_tree_ref,
       candidate.current_tree_content_hash, candidate.current_tree_hash,
       candidate.dirty, candidate.conflicted, candidate.stale, candidate.rebase_required,
       'provisioning', 1, candidate.session_epoch,
       1000, 1073741824, 4294967296, 512, 2, 600, 3600
FROM candidate_workspaces AS candidate WHERE candidate.id = $4
`, sessionID, seed.actorID, applicationBuildContractCanaryDigest("incomplete-runner"), candidateID); err != nil {
		t.Fatalf("insert incomplete SandboxSession parent: %v", err)
	}
	if err := transaction.Commit(); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("deferred configuration completeness guard did not reject parent-only session: %v", err)
	}
}

func assertSandboxSessionEventChain(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	sessionID uuid.UUID,
	wantVersion, wantEpoch int64,
) {
	t.Helper()
	var version, epoch, candidateEpoch, eventCount int64
	if err := database.QueryRowContext(ctx, `
SELECT session.version, session.session_epoch, session.candidate_session_epoch, count(event.sequence)
FROM sandbox_sessions AS session
LEFT JOIN sandbox_session_transition_events AS event ON event.session_id = session.id
WHERE session.id = $1
GROUP BY session.version, session.session_epoch, session.candidate_session_epoch
`, sessionID).Scan(&version, &epoch, &candidateEpoch, &eventCount); err != nil {
		t.Fatal(err)
	}
	if version != wantVersion || eventCount != wantVersion-1 || epoch != wantEpoch || candidateEpoch != wantEpoch {
		t.Fatalf("invalid SandboxSession event projection: version=%d events=%d epoch=%d candidate_epoch=%d",
			version, eventCount, epoch, candidateEpoch)
	}
	var invalidEpochEvents int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM sandbox_session_transition_events
WHERE session_id = $1
  AND (
    session_epoch_to < session_epoch_from
    OR candidate_session_epoch_to < candidate_session_epoch_from
    OR candidate_writer_lease_epoch_to < candidate_writer_lease_epoch_from
    OR candidate_session_epoch_from <> session_epoch_from
    OR candidate_session_epoch_to <> session_epoch_to
    OR (event_kind = 'lifecycle.resume_requested' AND session_epoch_to <> session_epoch_from + 1)
    OR (event_kind <> 'lifecycle.resume_requested' AND session_epoch_to <> session_epoch_from)
  )
`, sessionID).Scan(&invalidEpochEvents); err != nil {
		t.Fatal(err)
	}
	if invalidEpochEvents != 0 {
		t.Fatalf("SandboxSession has %d non-monotonic epoch events", invalidEpochEvents)
	}
}

func assertSandboxDirectMutationsRejected(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	sessionID uuid.UUID,
) {
	t.Helper()
	if _, err := database.ExecContext(ctx, `
UPDATE sandbox_sessions SET state = 'ready', version = version + 1 WHERE id = $1
`, sessionID); err == nil || !strings.Contains(err.Error(), "append-only CAS transition event") {
		t.Fatalf("direct SandboxSession state update was not rejected: %v", err)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM sandbox_sessions WHERE id = $1`, sessionID); err == nil {
		t.Fatalf("direct SandboxSession delete was accepted: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE sandbox_session_transition_events
SET reason = reason || ' tampered'
WHERE session_id = $1 AND sequence = 1
`, sessionID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("SandboxSession event update was not rejected: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
DELETE FROM sandbox_session_transition_events WHERE session_id = $1 AND sequence = 1
`, sessionID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("SandboxSession event delete was not rejected: %v", err)
	}
}

func assertSandboxDeferredChainRejectsBypass(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	sessionID uuid.UUID,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `ALTER TABLE sandbox_sessions DISABLE TRIGGER sandbox_session_mutation_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE sandbox_sessions
SET version = version + 1, updated_at = statement_timestamp()
WHERE id = $1
`, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err == nil ||
		(!strings.Contains(err.Error(), "sequence") && !strings.Contains(err.Error(), "projection")) {
		t.Fatalf("deferred SandboxSession event-chain invariant did not reject a bypass: %v", err)
	}
}
