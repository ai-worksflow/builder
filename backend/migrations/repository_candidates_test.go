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

func TestRepositoryCandidatesMigrationDeclaresImmutableFencedPersistence(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000023_repository_candidates.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000023_repository_candidates.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TABLE repository_snapshots",
		"CREATE TABLE candidate_workspaces",
		"CREATE TABLE candidate_workspace_journal",
		"CREATE TABLE candidate_workspace_control_events",
		"CREATE TABLE candidate_snapshots",
		"repository-snapshot/v1",
		"candidate-workspace/v1",
		"candidate-snapshot/v1",
		"RepositorySnapshot is immutable",
		"CandidateSnapshot is immutable",
		"Candidate journal is append-only",
		"Candidate control events are append-only",
		"acquire_candidate_workspace_lease",
		"rotate_candidate_workspace_session",
		"update_candidate_workspace_flags",
		"freeze_candidate_workspace",
		"abandon_candidate_workspace",
		"candidate.flags_updated",
		"Candidate freeze requires the exact current fenced CandidateSnapshot",
		"Dirty Candidate abandonment requires an exact current CandidateSnapshot",
		"candidate.writer_lease_epoch, candidate.writer_lease_epoch + 1",
		"candidate.version = expected_version",
		"journal.sequence = NEW.journal_sequence",
		"journal.creation_transaction_id = txid_current()",
		"Candidate journal sequence is not contiguous",
		"snapshot.full_stack_template_id = NEW.full_stack_template_id",
		"snapshot.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id",
		"snapshot.tree_hash = NEW.current_tree_hash",
		"repository_path_is_safe",
		"repository_path_matches_template_policy",
		"current_tree_content_hash",
		"current_tree_owner_id",
		"REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON candidate_workspace_journal FROM PUBLIC",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Repository/Candidate migration is missing %q", expected)
		}
	}
	for _, forbidden := range []string{
		"file_body",
		"file_contents",
		"workspace_body",
		"transition_candidate_workspace_status",
	} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("Repository metadata migration must not persist file bodies: found %q", forbidden)
		}
	}
	downText := string(down)
	for _, expected := range []string{
		"DROP TABLE IF EXISTS candidate_snapshots",
		"DROP TABLE IF EXISTS candidate_workspace_journal",
		"DROP TABLE IF EXISTS candidate_workspace_control_events",
		"DROP TABLE IF EXISTS candidate_workspaces",
		"DROP TABLE IF EXISTS repository_snapshots",
		"DROP FUNCTION IF EXISTS acquire_candidate_workspace_lease",
		"DROP FUNCTION IF EXISTS freeze_candidate_workspace",
		"DROP FUNCTION IF EXISTS repository_path_matches_template_policy",
		"DROP FUNCTION IF EXISTS repository_path_is_safe",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("Repository/Candidate rollback is missing %q", expected)
		}
	}
}

func TestRepositoryCandidatesMigrationPostgresCanary(t *testing.T) {
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
	schema := "repository_candidate_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name > "000023_repository_candidates.up.sql" {
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

	for _, table := range []string{
		"repository_snapshots", "candidate_workspaces",
		"candidate_workspace_journal", "candidate_snapshots",
	} {
		var actual string
		if err := database.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, table).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != table {
			t.Fatalf("expected %s table, got %q", table, actual)
		}
	}

	var bodyColumnCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name IN (
    'repository_snapshots', 'candidate_workspaces',
    'candidate_workspace_journal', 'candidate_workspace_control_events',
    'candidate_snapshots'
  )
  AND column_name IN ('body', 'content', 'document', 'files', 'payload', 'file_contents')
`).Scan(&bodyColumnCount); err != nil {
		t.Fatal(err)
	}
	if bodyColumnCount != 0 {
		t.Fatalf("repository persistence unexpectedly exposes %d body/content columns", bodyColumnCount)
	}

	seed := seedRepositoryCandidateCanary(t, ctx, database)
	assertRepositoryCandidateGuards(t, ctx, database, seed)

	rebaseDown, err := files.ReadFile("000030_candidate_rebases.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(rebaseDown)); err != nil {
		t.Fatalf("Candidate rebase rollback before Repository/Candidate rollback failed: %v", err)
	}
	down, err := files.ReadFile("000023_repository_candidates.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("Repository/Candidate rollback failed: %v", err)
	}
	var rolledBack sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT to_regclass('candidate_workspaces')::text`).Scan(&rolledBack); err != nil {
		t.Fatal(err)
	}
	if rolledBack.Valid {
		t.Fatalf("candidate_workspaces survived rollback as %q", rolledBack.String)
	}
}

type repositoryCandidateCanarySeed struct {
	actorID             uuid.UUID
	secondActorID       uuid.UUID
	projectID           uuid.UUID
	otherProjectID      uuid.UUID
	manifestID          uuid.UUID
	manifestHash        string
	contractID          uuid.UUID
	contractHash        string
	fullStackID         uuid.UUID
	fullStackHash       string
	workspaceArtifactID uuid.UUID
	workspaceRevisionID uuid.UUID
	workspaceHash       string
	createdAt           time.Time
}

func seedRepositoryCandidateCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) repositoryCandidateCanarySeed {
	t.Helper()
	seed := repositoryCandidateCanarySeed{
		actorID:             uuid.New(),
		secondActorID:       uuid.New(),
		projectID:           uuid.New(),
		otherProjectID:      uuid.New(),
		manifestID:          uuid.New(),
		manifestHash:        strings.TrimPrefix(applicationBuildContractCanaryDigest("repository-manifest"), "sha256:"),
		contractID:          uuid.New(),
		contractHash:        strings.TrimPrefix(applicationBuildContractCanaryDigest("contract-repository-candidate"), "sha256:"),
		fullStackID:         uuid.New(),
		workspaceArtifactID: uuid.New(),
		workspaceRevisionID: uuid.New(),
		workspaceHash:       applicationBuildContractCanaryDigest("repository-workspace"),
		createdAt:           time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond),
	}
	sourceArtifactID, sourceRevisionID := uuid.New(), uuid.New()
	sourceHash := applicationBuildContractCanaryDigest("repository-contract-source")
	reviewerID := uuid.New()

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'Repository actor', 'not-used'),
       ($3, $4, 'Repository second actor', 'not-used'),
       ($5, $6, 'Repository reviewer', 'not-used')
`, seed.actorID, "repository-actor-"+uuid.NewString()+"@example.com",
		seed.secondActorID, "repository-second-"+uuid.NewString()+"@example.com",
		reviewerID, "repository-reviewer-"+uuid.NewString()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, 'Repository canary', $2),
       ($3, 'Repository other project', $2)
`, seed.projectID, seed.actorID, seed.otherProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO artifacts (id, project_id, kind, artifact_key, title, created_by)
VALUES ($1, $2, 'workspace', 'WORKSPACE-CANARY', 'Repository workspace', $3),
       ($4, $2, 'api_contract', 'REPOSITORY-SOURCE', 'Repository source', $3)
`, seed.workspaceArtifactID, seed.projectID, seed.actorID, sourceArtifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO artifact_revisions (
  id, artifact_id, revision_number, schema_version, content_ref, content_hash,
  workflow_status, change_source, change_summary, created_by, created_at, approved_at
) VALUES
  ($1, $2, 1, 1, $3, $4, 'approved', 'system', 'repository canary', $5, $6, $6),
  ($7, $8, 1, 1, $9, $10, 'approved', 'system', 'repository canary source', $5, $6, $6)
`, seed.workspaceRevisionID, seed.workspaceArtifactID,
		"repository-workspace-"+seed.workspaceRevisionID.String(), seed.workspaceHash,
		seed.actorID, seed.createdAt,
		sourceRevisionID, sourceArtifactID, "repository-source-"+sourceRevisionID.String(), sourceHash); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO application_build_manifests (
  id, project_id, root_manifest_id, workspace_revision_id, schema_version,
  content_ref, content_hash, manifest_hash, status, created_by, created_at
) VALUES ($1, $2, $1, $3, 1, $4, $5, $6, 'frozen', $7, $8)
`, seed.manifestID, seed.projectID, seed.workspaceRevisionID,
		"repository-manifest-"+seed.manifestID.String(),
		applicationBuildContractCanaryDigest("repository-manifest-content"),
		seed.manifestHash, seed.actorID, seed.createdAt); err != nil {
		t.Fatal(err)
	}

	web := insertApplicationBuildContractCanaryRelease(t, ctx, transaction, seed.actorID, reviewerID, "repository-web", seed.createdAt)
	web.role, web.mountPath = "web", "apps/web"
	api := insertApplicationBuildContractCanaryRelease(t, ctx, transaction, seed.actorID, reviewerID, "repository-api", seed.createdAt)
	api.role, api.mountPath = "api", "apps/api"
	if _, err := transaction.ExecContext(ctx, `ALTER TABLE template_releases DISABLE TRIGGER template_release_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE template_releases
SET manifest = jsonb_set(
                 jsonb_set(manifest, '{extensionPaths}', '["src"]'::jsonb, true),
                 '{protectedPaths}', '["protected"]'::jsonb, true
               )
WHERE id IN ($1, $2)
`, web.id, api.id); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `ALTER TABLE template_releases ENABLE TRIGGER template_release_immutable`); err != nil {
		t.Fatal(err)
	}
	seed.fullStackHash = applicationBuildContractCanaryDigest("repository-full-stack")
	fullStackDocument := applicationBuildContractCanaryJSON(t, map[string]any{
		"id": seed.fullStackID.String(), "schemaVersion": "full-stack-template/v1",
		"templateId": "repository-canary", "version": "1.0.0", "contentHash": seed.fullStackHash,
		"components": []any{
			map[string]any{"role": api.role, "mountPath": api.mountPath, "release": map[string]any{"id": api.id.String(), "contentHash": api.contentHash, "subjectHash": api.subjectHash}},
			map[string]any{"role": web.role, "mountPath": web.mountPath, "release": map[string]any{"id": web.id.String(), "contentHash": web.contentHash, "subjectHash": web.subjectHash}},
		},
		"layout":    map[string]any{"contractTruthSource": "openapi"},
		"createdBy": reviewerID.String(), "createdAt": seed.createdAt.Format(time.RFC3339Nano),
	})
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO full_stack_template_releases (
  id, schema_version, template_id, release_version, document, content_hash, created_by, created_at
) VALUES ($1, 'full-stack-template/v1', 'repository-canary', '1.0.0', $2::jsonb, $3, $4, $5)
`, seed.fullStackID, fullStackDocument, seed.fullStackHash, reviewerID, seed.createdAt); err != nil {
		t.Fatal(err)
	}
	for _, component := range []applicationBuildContractCanaryTemplate{api, web} {
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO full_stack_template_components (
  full_stack_template_id, full_stack_content_hash, role, mount_path,
  template_release_id, template_release_content_hash
) VALUES ($1, $2, $3, $4, $5, $6)
`, seed.fullStackID, seed.fullStackHash, component.role, component.mountPath,
			component.id, component.contentHash); err != nil {
			t.Fatal(err)
		}
	}

	insertApplicationBuildContractCanaryParent(
		t, ctx, transaction, seed.contractID, seed.projectID, seed.manifestID, seed.manifestHash,
		seed.fullStackID, seed.fullStackHash, seed.actorID, "repository-candidate",
	)
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO application_build_contract_sources (
  contract_id, ordinal, source_kind, purpose, required, artifact_id, revision_id, content_hash
) VALUES ($1, 0, 'api_contract', 'repository contract', true, $2, $3, $4)
`, seed.contractID, sourceArtifactID, sourceRevisionID, sourceHash); err != nil {
		t.Fatal(err)
	}
	for ordinal, component := range []applicationBuildContractCanaryTemplate{api, web} {
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO application_build_contract_template_releases (
  contract_id, ordinal, role, template_release_id, template_release_content_hash
) VALUES ($1, $2, $3, $4, $5)
`, seed.contractID, ordinal, component.role, component.id, component.contentHash); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO application_build_contract_obligations (
  contract_id, obligation_id, level, kind, source_artifact_id, source_revision_id,
  source_content_hash, source_anchor_id, oracle_ids, depends_on, waivable, status
) VALUES ($1, 'OBL-REPOSITORY', 'must', 'acceptance', $2, $3, $4,
          'AC-REPOSITORY', '["oracle-repository"]'::jsonb, '[]'::jsonb, false, 'ready')
`, seed.contractID, sourceArtifactID, sourceRevisionID, sourceHash); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit repository canary prerequisites: %v", err)
	}
	// The shared canary helper predates the production service's raw CanonicalHash
	// representation. Keep this fixture exact with the real parent row while the
	// migration explicitly supports both legacy-prefixed and raw parent hashes.
	if _, err := database.ExecContext(ctx, `ALTER TABLE application_build_contracts DISABLE TRIGGER application_build_contract_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE application_build_contracts SET contract_hash = $2 WHERE id = $1`, seed.contractID, seed.contractHash); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE application_build_contracts ENABLE TRIGGER application_build_contract_immutable`); err != nil {
		t.Fatal(err)
	}
	return seed
}

func assertRepositoryCandidateGuards(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	seed repositoryCandidateCanarySeed,
) {
	t.Helper()
	snapshotID, candidateID, checkpointID := uuid.New(), uuid.New(), uuid.New()
	baseTreeHash := applicationBuildContractCanaryDigest("repository-base-tree")
	baseTreeContentHash := applicationBuildContractCanaryDigest("repository-base-tree-content-object")
	baseTreeRef := "blob://repository-trees/" + strings.TrimPrefix(baseTreeContentHash, "sha256:")
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_snapshots (
  id, schema_version, project_id,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
	  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash, tree_file_count, tree_byte_size,
  created_by, created_at
) VALUES (
  $1, 'repository-snapshot/v1', $2,
  $3, $4, $5, $6, $7, $8,
	  $9, $10, $11, 'blob', $1, $12, $13, $14, 1, 128, $15, $16
	)
	`, snapshotID, seed.projectID, seed.manifestID, seed.manifestHash,
		seed.contractID, seed.contractHash, seed.fullStackID, seed.fullStackHash,
		seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceHash,
		baseTreeRef, baseTreeContentHash, baseTreeHash, seed.actorID, createdAt); err != nil {
		t.Fatalf("insert exact RepositorySnapshot: %v", err)
	}

	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_snapshots (
  id, schema_version, project_id,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
	  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash, tree_file_count, tree_byte_size, created_by
) VALUES (
  $1, 'repository-snapshot/v1', $2, $3, $4, $5, $6, $7, $8,
	  $9, $10, $11, 'blob', $1, $12, $13, $14, 1, 128, $15
	)
	`, uuid.New(), seed.otherProjectID, seed.manifestID, seed.manifestHash,
		seed.contractID, seed.contractHash, seed.fullStackID, seed.fullStackHash,
		seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceHash,
		baseTreeRef, baseTreeContentHash, baseTreeHash, seed.actorID); err == nil {
		t.Fatal("cross-project RepositorySnapshot was accepted")
	}
	if _, err := database.ExecContext(ctx, `UPDATE repository_snapshots SET tree_ref = tree_ref || '-tampered' WHERE id = $1`, snapshotID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("RepositorySnapshot mutation was not rejected: %v", err)
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
  session_epoch, version, journal_sequence,
  writer_lease_epoch, created_by, created_at, updated_at
) VALUES (
  $1, 'candidate-workspace/v1', $2, $3,
  $4, $5, $6, $7, $8, $9,
	  $10, $11, $12,
	  'blob', $3, $13, $14, $15, 'blob', $3, $13, $14, $15, 1, 128,
	  'active', false, false, false, false,
	  1, 1, 0, 0, $16, $17, $17
	)
`, candidateID, seed.projectID, snapshotID,
		seed.manifestID, seed.manifestHash, seed.contractID, seed.contractHash,
		seed.fullStackID, seed.fullStackHash,
		seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceHash,
		baseTreeRef, baseTreeContentHash, baseTreeHash, seed.actorID, createdAt); err != nil {
		t.Fatalf("insert exact CandidateWorkspace: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE candidate_workspaces
SET writer_lease_owner_id = $2,
    writer_lease_epoch = writer_lease_epoch + 1,
    writer_lease_expires_at = statement_timestamp() + interval '5 minutes',
    version = version + 1
WHERE id = $1
`, candidateID, seed.actorID); err == nil || !strings.Contains(err.Error(), "can change only") {
		t.Fatalf("direct writer lease mutation bypassed the append-only control event: %v", err)
	}

	var version, sessionEpoch, leaseEpoch int64
	var leaseExpires time.Time
	if err := database.QueryRowContext(ctx, `
SELECT candidate_version, session_epoch, writer_lease_epoch, writer_lease_expires_at
FROM acquire_candidate_workspace_lease($1, 1, $2, 300)
`, candidateID, seed.actorID).Scan(&version, &sessionEpoch, &leaseEpoch, &leaseExpires); err != nil {
		t.Fatalf("acquire candidate lease: %v", err)
	}
	if version != 2 || sessionEpoch != 1 || leaseEpoch != 1 || !leaseExpires.After(time.Now()) {
		t.Fatalf("unexpected first lease: version=%d session=%d lease=%d expires=%s", version, sessionEpoch, leaseEpoch, leaseExpires)
	}
	if _, err := database.ExecContext(ctx, `SELECT * FROM acquire_candidate_workspace_lease($1, 1, $2, 300)`, candidateID, seed.actorID); err == nil {
		t.Fatal("stale candidate lease CAS was accepted")
	}
	if _, err := database.ExecContext(ctx, `SELECT * FROM acquire_candidate_workspace_lease($1, 2, $2, 300)`, candidateID, seed.secondActorID); err == nil {
		t.Fatal("live candidate lease was stolen by another writer")
	}

	afterTreeHash := applicationBuildContractCanaryDigest("repository-after-tree")
	afterTreeContentHash := applicationBuildContractCanaryDigest("repository-after-tree-content-object")
	afterTreeRef := "blob://repository-trees/" + strings.TrimPrefix(afterTreeContentHash, "sha256:")
	fileHash := applicationBuildContractCanaryDigest("repository-file")
	insertUpsert := func(
		sequence, versionFrom, versionTo, operationSessionEpoch, operationLeaseEpoch int64,
		attribution, operationID, targetPath string,
		beforeOwner uuid.UUID, beforeRef, beforeContentHash, beforeTreeHash string,
		afterOwner uuid.UUID, afterRef, afterContentHash, afterTreeHash string,
		afterFileCount int, afterByteSize int64,
	) error {
		_, insertErr := database.ExecContext(ctx, `
INSERT INTO candidate_workspace_journal (
  candidate_id, sequence, candidate_version_from, candidate_version_to,
  session_epoch, writer_lease_epoch, actor_id, attribution,
  operation_id, operation_kind, path, content_hash, byte_size, file_mode,
  before_tree_store, before_tree_owner_id, before_tree_ref, before_tree_content_hash, before_tree_hash,
  after_tree_store, after_tree_owner_id, after_tree_ref, after_tree_content_hash, after_tree_hash,
  after_tree_file_count, after_tree_byte_size
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8,
  $9, 'file.upsert', $10, $11, 256, '100644',
  'blob', $12, $13, $14, $15,
	  'blob', $16, $17, $18, $19, $20, $21
)
`, candidateID, sequence, versionFrom, versionTo, operationSessionEpoch, operationLeaseEpoch,
			seed.actorID, attribution, operationID, targetPath, fileHash,
			beforeOwner, beforeRef, beforeContentHash, beforeTreeHash,
			afterOwner, afterRef, afterContentHash, afterTreeHash, afterFileCount, afterByteSize)
		return insertErr
	}
	if err := insertUpsert(
		1, 2, 3, 1, 99, "user", "OP-WRONG-EPOCH", "apps/web/page.tsx",
		snapshotID, baseTreeRef, baseTreeContentHash, baseTreeHash,
		candidateID, afterTreeRef, afterTreeContentHash, afterTreeHash, 2, 384,
	); err == nil {
		t.Fatal("fenced journal epoch was accepted")
	}
	if err := insertUpsert(
		1, 2, 3, 1, 1, "user", "OP-PROTECTED", ".git/config",
		snapshotID, baseTreeRef, baseTreeContentHash, baseTreeHash,
		candidateID, afterTreeRef, afterTreeContentHash, afterTreeHash, 2, 384,
	); err == nil {
		t.Fatal("protected repository path was accepted")
	}
	if err := insertUpsert(
		1, 2, 3, 1, 1, "user", "OP-WRONG-AFTER-OWNER", "apps/web/page.tsx",
		snapshotID, baseTreeRef, baseTreeContentHash, baseTreeHash,
		snapshotID, afterTreeRef, afterTreeContentHash, afterTreeHash, 2, 384,
	); err == nil {
		t.Fatal("Candidate journal accepted an after-tree owned by another aggregate")
	}
	if err := insertUpsert(
		1, 2, 3, 1, 1, "user", "OP-VALID", "apps/web/page.tsx",
		snapshotID, baseTreeRef, baseTreeContentHash, baseTreeHash,
		candidateID, afterTreeRef, afterTreeContentHash, afterTreeHash, 2, 384,
	); err != nil {
		t.Fatalf("append valid candidate journal operation: %v", err)
	}

	agentTreeHash := applicationBuildContractCanaryDigest("repository-agent-tree")
	agentTreeContentHash := applicationBuildContractCanaryDigest("repository-agent-tree-content-object")
	agentTreeRef := "blob://repository-trees/" + strings.TrimPrefix(agentTreeContentHash, "sha256:")
	if err := insertUpsert(
		2, 3, 4, 1, 1, "user", "OP-TEMPLATE-PROTECTED", "apps/web/protected/settings.ts",
		candidateID, afterTreeRef, afterTreeContentHash, afterTreeHash,
		candidateID, agentTreeRef, agentTreeContentHash, agentTreeHash, 3, 512,
	); err == nil || !strings.Contains(err.Error(), "TemplateRelease protected path") {
		t.Fatalf("dynamic TemplateRelease protected path was not rejected: %v", err)
	}
	if err := insertUpsert(
		2, 3, 4, 1, 1, "agent", "OP-AGENT-OUTSIDE-EXTENSION", "apps/web/page-agent.tsx",
		candidateID, afterTreeRef, afterTreeContentHash, afterTreeHash,
		candidateID, agentTreeRef, agentTreeContentHash, agentTreeHash, 3, 512,
	); err == nil || !strings.Contains(err.Error(), "extension paths") {
		t.Fatalf("agent write outside exact extension paths was not rejected: %v", err)
	}
	if err := insertUpsert(
		2, 3, 4, 1, 1, "agent", "OP-AGENT-VALID", "apps/web/src/agent.tsx",
		candidateID, afterTreeRef, afterTreeContentHash, afterTreeHash,
		candidateID, agentTreeRef, agentTreeContentHash, agentTreeHash, 3, 512,
	); err != nil {
		t.Fatalf("agent write inside exact extension path was rejected: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_workspace_journal (
  candidate_id, sequence, candidate_version_from, candidate_version_to,
  session_epoch, writer_lease_epoch, actor_id, attribution,
  operation_id, operation_kind, path, from_path, expected_content_hash,
  before_tree_store, before_tree_owner_id, before_tree_ref, before_tree_content_hash, before_tree_hash,
  after_tree_store, after_tree_owner_id, after_tree_ref, after_tree_content_hash, after_tree_hash,
  after_tree_file_count, after_tree_byte_size
) VALUES (
  $1, 3, 4, 5, 1, 1, $2, 'user',
  'OP-TEMPLATE-PROTECTED-RENAME', 'file.rename', 'apps/web/src/moved.ts', 'apps/web/protected/settings.ts', $9,
  'blob', $1, $3, $4, $5, 'blob', $1, $6, $7, $8, 3, 512
)
`, candidateID, seed.actorID, agentTreeRef, agentTreeContentHash, agentTreeHash,
		afterTreeRef, afterTreeContentHash, afterTreeHash,
		applicationBuildContractCanaryDigest("repository-protected-rename-expected")); err == nil || !strings.Contains(err.Error(), "TemplateRelease protected path") {
		t.Fatalf("dynamic protected rename source was not rejected: %v", err)
	}

	var actualTreeHash string
	var actualSequence int64
	if err := database.QueryRowContext(ctx, `
SELECT version, journal_sequence, current_tree_hash
FROM candidate_workspaces WHERE id = $1
`, candidateID).Scan(&version, &actualSequence, &actualTreeHash); err != nil {
		t.Fatal(err)
	}
	if version != 4 || actualSequence != 2 || actualTreeHash != agentTreeHash {
		t.Fatalf("journal did not atomically advance Candidate: version=%d sequence=%d tree=%q", version, actualSequence, actualTreeHash)
	}
	if _, err := database.ExecContext(ctx, `UPDATE candidate_workspace_journal SET attribution = 'agent' WHERE candidate_id = $1 AND sequence = 1`, candidateID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("journal mutation was not rejected: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE candidate_workspaces
SET current_tree_ref = current_tree_ref || '-tampered', version = version + 1
WHERE id = $1
`, candidateID); err == nil || !strings.Contains(err.Error(), "can change only") {
		t.Fatalf("direct Candidate tree mutation was not rejected: %v", err)
	}

	insertCheckpoint := func(
		id uuid.UUID,
		checkpointVersion, checkpointSequence, checkpointSessionEpoch, checkpointLeaseEpoch int64,
		treeOwner uuid.UUID,
		treeRef, treeContentHash, treeHash, reason string,
		createdBy uuid.UUID,
	) error {
		_, insertErr := database.ExecContext(ctx, `
INSERT INTO candidate_snapshots (
  id, schema_version, candidate_id, project_id, candidate_version, journal_sequence,
  session_epoch, writer_lease_epoch,
  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash, tree_file_count, tree_byte_size,
  reason, created_by
) VALUES ($1, 'candidate-snapshot/v1', $2, $3, $4, $5, $6, $7,
          'blob', $8, $9, $10, $11, 3, 512, $12, $13)
`, id, candidateID, seed.projectID, checkpointVersion, checkpointSequence,
			checkpointSessionEpoch, checkpointLeaseEpoch, treeOwner, treeRef, treeContentHash, treeHash,
			reason, createdBy)
		return insertErr
	}
	if err := insertCheckpoint(
		uuid.New(), 4, 2, 99, 1, candidateID,
		agentTreeRef, agentTreeContentHash, agentTreeHash, "wrong session", seed.actorID,
	); err == nil {
		t.Fatal("CandidateSnapshot with a stale session epoch was accepted")
	}
	if err := insertCheckpoint(
		uuid.New(), 4, 2, 1, 1, candidateID,
		agentTreeRef, agentTreeContentHash, agentTreeHash, "wrong actor", seed.secondActorID,
	); err == nil {
		t.Fatal("CandidateSnapshot from a non-lease owner was accepted")
	}
	if err := insertCheckpoint(
		checkpointID, 4, 2, 1, 1, candidateID,
		agentTreeRef, agentTreeContentHash, agentTreeHash, "disconnect checkpoint", seed.actorID,
	); err != nil {
		t.Fatalf("insert exact CandidateSnapshot: %v", err)
	}
	if err := insertCheckpoint(
		uuid.New(), 4, 2, 1, 1, snapshotID,
		baseTreeRef, baseTreeContentHash, baseTreeHash, "wrong tree", seed.actorID,
	); err == nil {
		t.Fatal("CandidateSnapshot with a non-current tree was accepted")
	}
	if _, err := database.ExecContext(ctx, `UPDATE candidate_snapshots SET reason = 'tampered' WHERE id = $1`, checkpointID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("CandidateSnapshot mutation was not rejected: %v", err)
	}

	if err := database.QueryRowContext(ctx, `
SELECT candidate_version, session_epoch, writer_lease_epoch
FROM rotate_candidate_workspace_session($1, 4, 1, $2)
`, candidateID, seed.actorID).Scan(&version, &sessionEpoch, &leaseEpoch); err != nil {
		t.Fatalf("rotate Candidate session: %v", err)
	}
	if version != 5 || sessionEpoch != 2 || leaseEpoch != 2 {
		t.Fatalf("session rotation did not fence prior writer: version=%d session=%d lease=%d", version, sessionEpoch, leaseEpoch)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_workspace_journal (
  candidate_id, sequence, candidate_version_from, candidate_version_to,
  session_epoch, writer_lease_epoch, actor_id, attribution,
  operation_id, operation_kind, path, expected_content_hash,
  before_tree_store, before_tree_owner_id, before_tree_ref, before_tree_content_hash, before_tree_hash,
  after_tree_store, after_tree_owner_id, after_tree_ref, after_tree_content_hash, after_tree_hash,
  after_tree_file_count, after_tree_byte_size
) VALUES (
	  $1, 3, 5, 6, 1, 1, $2, 'user',
  'OP-OLD-SESSION', 'file.delete', 'apps/web/page.tsx', $9,
	  'blob', $1, $3, $4, $5, 'blob', $1, $6, $7, $8, 1, 128
)
`, candidateID, seed.actorID, agentTreeRef, agentTreeContentHash, agentTreeHash,
		baseTreeRef, baseTreeContentHash, baseTreeHash,
		applicationBuildContractCanaryDigest("repository-old-session-expected")); err == nil {
		t.Fatal("journal from an old session/lease epoch was accepted")
	}

	assertRepositoryCandidateDeferredChainGuard(t, ctx, database, candidateID)

	evidenceHash := applicationBuildContractCanaryDigest("repository-upstream-drift-evidence")
	var nextVersion int64
	if err := database.QueryRowContext(ctx, `
SELECT update_candidate_workspace_flags(
  $1, 5, 2, 2, $2, true, false, true,
  'canonical workspace head advanced', 'workspace-revision://new-head', $3
)
`, candidateID, seed.actorID, evidenceHash).Scan(&nextVersion); err != nil {
		t.Fatalf("append typed Candidate flags.updated event: %v", err)
	}
	if nextVersion != 6 {
		t.Fatalf("flag transition returned version %d", nextVersion)
	}
	var conflicted, stale, rebaseRequired bool
	if err := database.QueryRowContext(ctx, `
SELECT conflicted, stale, rebase_required, writer_lease_epoch
FROM candidate_workspaces WHERE id = $1
`, candidateID).Scan(&conflicted, &stale, &rebaseRequired, &leaseEpoch); err != nil {
		t.Fatal(err)
	}
	if !conflicted || stale || !rebaseRequired || leaseEpoch != 3 {
		t.Fatalf("typed flags transition did not fence and persist independent flags: conflicted=%t stale=%t rebase=%t lease=%d", conflicted, stale, rebaseRequired, leaseEpoch)
	}
	if _, err := database.ExecContext(ctx, `
SELECT freeze_candidate_workspace($1, 6, 2, 3, $2, $3, 'unsafe freeze')
`, candidateID, seed.actorID, checkpointID); err == nil || !strings.Contains(err.Error(), "cannot freeze") {
		t.Fatalf("freeze with blocking Candidate flags was not rejected: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
SELECT update_candidate_workspace_flags(
  $1, 6, 2, 2, $2, false, false, false,
  'stale lease fence', 'workspace-revision://resolved', $3
)
`, candidateID, seed.actorID, evidenceHash); err == nil {
		t.Fatal("Candidate flags update accepted a stale writer lease epoch")
	}
	if err := database.QueryRowContext(ctx, `
SELECT update_candidate_workspace_flags(
  $1, 6, 2, 3, $2, false, false, false,
  'explicit rebase resolution', 'workspace-revision://rebased', $3
)
`, candidateID, seed.actorID, evidenceHash).Scan(&nextVersion); err != nil || nextVersion != 7 {
		t.Fatalf("clear Candidate flags with a fenced event: version=%d err=%v", nextVersion, err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT candidate_version, session_epoch, writer_lease_epoch, writer_lease_expires_at
FROM acquire_candidate_workspace_lease($1, 7, $2, 300)
`, candidateID, seed.actorID).Scan(&version, &sessionEpoch, &leaseEpoch, &leaseExpires); err != nil {
		t.Fatalf("reacquire Candidate lease after flag fences: %v", err)
	}
	if version != 8 || sessionEpoch != 2 || leaseEpoch != 5 {
		t.Fatalf("reacquired lease was not monotonic: version=%d session=%d lease=%d", version, sessionEpoch, leaseEpoch)
	}
	if _, err := database.ExecContext(ctx, `
SELECT abandon_candidate_workspace($1, 8, 2, 5, $2, NULL, 'discard dirty Candidate')
`, candidateID, seed.actorID); err == nil || !strings.Contains(err.Error(), "requires an exact current") {
		t.Fatalf("dirty Candidate abandonment without checkpoint was not rejected: %v", err)
	}

	freezeCheckpointID := uuid.New()
	if err := insertCheckpoint(
		freezeCheckpointID, 8, 2, 2, 5, candidateID,
		agentTreeRef, agentTreeContentHash, agentTreeHash, "freeze checkpoint", seed.actorID,
	); err != nil {
		t.Fatalf("insert exact fenced freeze checkpoint: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
SELECT freeze_candidate_workspace($1, 8, 2, 5, $2, $3, 'stale checkpoint freeze')
`, candidateID, seed.actorID, checkpointID); err == nil || !strings.Contains(err.Error(), "exact current") {
		t.Fatalf("freeze accepted a stale CandidateSnapshot: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
SELECT abandon_candidate_workspace($1, 8, 2, 5, $2, $3, '')
`, candidateID, seed.actorID, freezeCheckpointID); err == nil {
		t.Fatal("Candidate abandonment accepted an empty reason")
	}
	if err := database.QueryRowContext(ctx, `
SELECT freeze_candidate_workspace($1, 8, 2, 5, $2, $3, 'implementation proposal freeze')
`, candidateID, seed.actorID, freezeCheckpointID).Scan(&nextVersion); err != nil {
		t.Fatalf("freeze exact clean Candidate checkpoint: %v", err)
	}
	var status string
	var leaseOwner sql.NullString
	if err := database.QueryRowContext(ctx, `
SELECT status, version, writer_lease_epoch, writer_lease_owner_id::text
FROM candidate_workspaces WHERE id = $1
`, candidateID).Scan(&status, &version, &leaseEpoch, &leaseOwner); err != nil {
		t.Fatal(err)
	}
	if status != "frozen" || version != 9 || leaseEpoch != 6 || leaseOwner.Valid {
		t.Fatalf("freeze did not terminally fence Candidate: status=%s version=%d lease=%d owner=%v", status, version, leaseEpoch, leaseOwner)
	}

	cleanCandidateID := uuid.New()
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
)
SELECT $1, 'candidate-workspace/v1', snapshot.project_id, snapshot.id,
       snapshot.build_manifest_id, snapshot.build_manifest_hash,
       snapshot.build_contract_id, snapshot.build_contract_hash,
       snapshot.full_stack_template_id, snapshot.full_stack_template_hash,
       snapshot.base_workspace_artifact_id, snapshot.base_workspace_revision_id, snapshot.base_workspace_content_hash,
       snapshot.tree_store, snapshot.tree_owner_id, snapshot.tree_ref, snapshot.tree_content_hash, snapshot.tree_hash,
       snapshot.tree_store, snapshot.tree_owner_id, snapshot.tree_ref, snapshot.tree_content_hash, snapshot.tree_hash,
       snapshot.tree_file_count, snapshot.tree_byte_size,
       'active', false, false, false, false, 1, 1, 0, 0, $2,
       statement_timestamp(), statement_timestamp()
FROM repository_snapshots AS snapshot WHERE snapshot.id = $3
`, cleanCandidateID, seed.actorID, snapshotID); err != nil {
		t.Fatalf("insert clean Candidate for abandonment: %v", err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT abandon_candidate_workspace($1, 1, 1, 0, $2, NULL, 'explicit clean Candidate abandonment')
`, cleanCandidateID, seed.actorID).Scan(&nextVersion); err != nil || nextVersion != 2 {
		t.Fatalf("abandon clean Candidate with reason: version=%d err=%v", nextVersion, err)
	}
}

func assertRepositoryCandidateDeferredChainGuard(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	candidateID uuid.UUID,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `ALTER TABLE candidate_workspaces DISABLE TRIGGER candidate_workspace_mutation_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE candidate_workspaces
SET journal_sequence = journal_sequence + 1,
    version = version + 1,
    updated_at = statement_timestamp()
WHERE id = $1
`, candidateID); err != nil {
		t.Fatal(err)
	}
	_, err = transaction.ExecContext(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
	if err == nil || !strings.Contains(err.Error(), "not contiguous") {
		t.Fatalf("deferred journal-chain invariant did not reject a bypassed cursor: %v", err)
	}
}
