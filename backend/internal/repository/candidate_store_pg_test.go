package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/migrations"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type candidateTreeRead struct {
	projectID string
	ownerID   string
	pointer   TreeBlobPointer
}

type candidateTreeReaderFake struct {
	trees map[string]TreeManifest
	reads []candidateTreeRead
}

func (reader *candidateTreeReaderFake) Get(
	_ context.Context,
	projectID, ownerID string,
	pointer TreeBlobPointer,
) (TreeManifest, error) {
	reader.reads = append(reader.reads, candidateTreeRead{
		projectID: projectID, ownerID: ownerID, pointer: pointer,
	})
	tree, found := reader.trees[pointer.Ref]
	if !found {
		return TreeManifest{}, fmt.Errorf("missing test tree %s", pointer.Ref)
	}
	return cloneTree(tree), nil
}

type candidateStorePostgresFixture struct {
	context        context.Context
	database       *sql.DB
	gorm           *gorm.DB
	trees          *candidateTreeReaderFake
	store          *GORMCandidateStore
	actorID        uuid.UUID
	projectID      uuid.UUID
	otherProjectID uuid.UUID
}

type candidateStoreSeed struct {
	candidateID          uuid.UUID
	snapshotID           uuid.UUID
	manifestID           uuid.UUID
	manifestHash         string
	contractID           uuid.UUID
	contractHash         string
	fullStackID          uuid.UUID
	fullStackHash        string
	workspaceArtifactID  uuid.UUID
	workspaceRevisionID  uuid.UUID
	workspaceContentHash string
	baseTree             TreeManifest
	basePointer          TreeBlobPointer
	candidateCreated     time.Time
}

func TestGORMCandidateStorePostgresExactHydrationAppendReplayAndTenantIsolation(t *testing.T) {
	fixture := openCandidateStorePostgresFixture(t)
	seed := fixture.seedCandidate(t)

	record, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), seed.candidateID.String(),
	)
	if err != nil {
		t.Fatalf("load mutation candidate: %v", err)
	}
	assertLoadedCandidate(t, fixture, seed, record)
	if len(fixture.trees.reads) != 1 || fixture.trees.reads[0] != (candidateTreeRead{
		projectID: fixture.projectID.String(), ownerID: seed.snapshotID.String(), pointer: seed.basePointer,
	}) {
		t.Fatalf("tree hydration did not receive the exact persisted pointer: %#v", fixture.trees.reads)
	}
	if _, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.otherProjectID.String(), seed.candidateID.String(),
	); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-tenant load error = %v, want record not found", err)
	}
	if _, found, err := fixture.store.FindCommittedOperation(
		fixture.context, fixture.otherProjectID.String(), seed.candidateID.String(), "operation-1",
	); err != nil || found {
		t.Fatalf("cross-tenant pre-append lookup leaked a row: found=%v err=%v", found, err)
	}

	command, firstAfterTree := fixture.commandFor(t, record, FileOperation{
		ID: "operation-1", Kind: OperationUpsert, Path: "apps/web/new.ts",
		ContentHash: digestFixture("candidate-store-first-file"), ByteSize: 17,
	})
	committed, err := fixture.store.AppendOperation(fixture.context, command)
	if err != nil {
		t.Fatalf("append first operation: %v", err)
	}
	if err := committed.matchesCommand(command); err != nil {
		t.Fatalf("committed mutation does not match command: %v", err)
	}
	var persistedCreatedAt time.Time
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT created_at
FROM candidate_workspace_journal
WHERE candidate_id = $1 AND operation_id = $2
`, seed.candidateID, command.Entry.Operation.ID).Scan(&persistedCreatedAt); err != nil {
		t.Fatal(err)
	}
	if committed.Entry.CreatedAt.IsZero() || !committed.Entry.CreatedAt.Equal(persistedCreatedAt) {
		t.Fatalf("journal createdAt was not read back from the database: persisted=%s committed=%s",
			persistedCreatedAt, committed.Entry.CreatedAt)
	}
	assertCandidateProjection(t, fixture, seed.candidateID, command)

	found, ok, err := fixture.store.FindCommittedOperation(
		fixture.context, fixture.projectID.String(), seed.candidateID.String(), command.Entry.Operation.ID,
	)
	if err != nil || !ok {
		t.Fatalf("find first committed operation: found=%v err=%v", ok, err)
	}
	if found != committed || found.BeforeTree != seed.basePointer {
		t.Fatalf("first committed operation lost exact before/after facts: %#v", found)
	}
	if _, ok, err := fixture.store.FindCommittedOperation(
		fixture.context, fixture.otherProjectID.String(), seed.candidateID.String(), command.Entry.Operation.ID,
	); err != nil || ok {
		t.Fatalf("cross-tenant committed lookup leaked a row: found=%v err=%v", ok, err)
	}

	replayed, err := fixture.store.AppendOperation(fixture.context, command)
	if err != nil || replayed != committed {
		t.Fatalf("exact operation replay was not recovered: committed=%#v err=%v", replayed, err)
	}
	differentReplay := command
	differentReplay.Entry.Attribution = "merge"
	if _, err := fixture.store.AppendOperation(fixture.context, differentReplay); !errors.Is(err, ErrOperationReplay) {
		t.Fatalf("different operation replay error = %v, want ErrOperationReplay", err)
	}

	fixture.trees.trees[command.AfterTree.Ref] = firstAfterTree
	loadedAfterFirst, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), seed.candidateID.String(),
	)
	if err != nil {
		t.Fatalf("load candidate after first append: %v", err)
	}
	if loadedAfterFirst.CurrentTreePointer != command.AfterTree ||
		!equalTrees(loadedAfterFirst.Candidate.CurrentTree, firstAfterTree) ||
		loadedAfterFirst.CurrentTreePointer.OwnerID != seed.candidateID.String() {
		t.Fatalf("advanced candidate was not exactly hydrated: %#v", loadedAfterFirst)
	}
	secondCommand, secondAfterTree := fixture.commandFor(t, loadedAfterFirst, FileOperation{
		ID: "operation-2", Kind: OperationDelete, Path: "apps/web/new.ts",
		ExpectedHash: digestFixture("candidate-store-first-file"),
	})
	secondCommitted, err := fixture.store.AppendOperation(fixture.context, secondCommand)
	if err != nil {
		t.Fatalf("append second operation: %v", err)
	}
	if secondCommitted.BeforeTree != command.AfterTree {
		t.Fatalf("later journal before pointer was not reconstructed from the previous after pointer: got=%#v want=%#v",
			secondCommitted.BeforeTree, command.AfterTree)
	}
	if secondCommitted.Entry.Operation.ExpectedHash != secondCommand.Entry.Operation.ExpectedHash ||
		secondCommitted.Entry.Operation.ExpectedHash == "" {
		t.Fatalf("delete expected_content_hash was not preserved: %#v", secondCommitted.Entry.Operation)
	}
	fixture.trees.trees[secondCommand.AfterTree.Ref] = secondAfterTree

	tenantSeed := fixture.seedCandidate(t)
	tenantRecord, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), tenantSeed.candidateID.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	tenantCommand, _ := fixture.commandFor(t, tenantRecord, FileOperation{
		ID: "tenant-operation", Kind: OperationUpsert, Path: "apps/web/tenant.ts",
		ContentHash: digestFixture("tenant-file"), ByteSize: 11,
	})
	tenantCommand.ProjectID = fixture.otherProjectID.String()
	tenantCommand.CandidateAfter.ProjectID = fixture.otherProjectID.String()
	if _, err := fixture.store.AppendOperation(fixture.context, tenantCommand); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-tenant append error = %v, want tenant-safe not found", err)
	}
	assertJournalCount(t, fixture, tenantSeed.candidateID, 0)
}

func TestGORMCandidateStorePostgresRejectsStaleFencesAndDefendsCommand(t *testing.T) {
	fixture := openCandidateStorePostgresFixture(t)

	tests := []struct {
		name   string
		mutate func(*testing.T, candidateStorePostgresFixture, candidateStoreSeed)
	}{
		{
			name: "version",
			mutate: func(t *testing.T, fixture candidateStorePostgresFixture, seed candidateStoreSeed) {
				t.Helper()
				if _, err := fixture.database.ExecContext(fixture.context, `
SELECT * FROM acquire_candidate_workspace_lease($1, 2, $2, 300)
`, seed.candidateID, fixture.actorID); err != nil {
					t.Fatalf("advance candidate version fence: %v", err)
				}
			},
		},
		{
			name: "session",
			mutate: func(t *testing.T, fixture candidateStorePostgresFixture, seed candidateStoreSeed) {
				t.Helper()
				fixture.replicaUpdateCandidate(t, seed.candidateID, "session_epoch = session_epoch + 1")
			},
		},
		{
			name: "lease epoch",
			mutate: func(t *testing.T, fixture candidateStorePostgresFixture, seed candidateStoreSeed) {
				t.Helper()
				fixture.replicaUpdateCandidate(t, seed.candidateID, "writer_lease_epoch = writer_lease_epoch + 1")
			},
		},
		{
			name: "expired lease",
			mutate: func(t *testing.T, fixture candidateStorePostgresFixture, seed candidateStoreSeed) {
				t.Helper()
				fixture.replicaUpdateCandidate(t, seed.candidateID, "writer_lease_expires_at = statement_timestamp() - interval '1 minute'")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seed := fixture.seedCandidate(t)
			record, err := fixture.store.LoadMutationCandidate(
				fixture.context, fixture.projectID.String(), seed.candidateID.String(),
			)
			if err != nil {
				t.Fatal(err)
			}
			command, _ := fixture.commandFor(t, record, FileOperation{
				ID:   "stale-" + strings.ReplaceAll(test.name, " ", "-"),
				Kind: OperationUpsert, Path: "apps/web/stale.ts",
				ContentHash: digestFixture("stale-" + test.name), ByteSize: 9,
			})
			test.mutate(t, *fixture, seed)

			_, err = fixture.store.AppendOperation(fixture.context, command)
			if !errors.Is(err, ErrCandidateState) {
				t.Fatalf("stale %s error = %v, want ErrCandidateState", test.name, err)
			}
			assertPostgresErrorCode(t, err, "40001")
			assertJournalCount(t, fixture, seed.candidateID, 0)
		})
	}

	seed := fixture.seedCandidate(t)
	record, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), seed.candidateID.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	command, _ := fixture.commandFor(t, record, FileOperation{
		ID: "defensive-command", Kind: OperationUpsert, Path: "apps/web/defensive.ts",
		ContentHash: digestFixture("defensive-file"), ByteSize: 14,
	})
	invalid := command
	invalid.AfterTree.FileCount++
	if _, err := fixture.store.AppendOperation(fixture.context, invalid); !errors.Is(err, ErrMutationStoreContract) {
		t.Fatalf("invalid after pointer facts error = %v, want store contract", err)
	}
	assertJournalCount(t, fixture, seed.candidateID, 0)

	wrongBeforeCount := command
	wrongBeforeCount.BeforeTree.FileCount++
	if _, err := fixture.store.AppendOperation(fixture.context, wrongBeforeCount); !errors.Is(err, ErrMutationStoreContract) {
		t.Fatalf("wrong before pointer count error = %v, want store contract", err)
	}
	assertJournalCount(t, fixture, seed.candidateID, 0)

	wrongLineage := command
	wrongLineage.CandidateAfter.BuildManifest.ContentHash = strings.TrimPrefix(
		digestFixture("forged-build-manifest"), "sha256:",
	)
	if _, err := fixture.store.AppendOperation(fixture.context, wrongLineage); !errors.Is(err, ErrMutationStoreContract) {
		t.Fatalf("forged immutable candidate lineage error = %v, want store contract", err)
	}
	assertJournalCount(t, fixture, seed.candidateID, 0)
}

func TestGORMCandidateStorePostgresRecoversCommitAcknowledgementAmbiguity(t *testing.T) {
	fixture := openCandidateStorePostgresFixture(t)
	seed := fixture.seedCandidate(t)
	record, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), seed.candidateID.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	command, _ := fixture.commandFor(t, record, FileOperation{
		ID: "acknowledgement-lost", Kind: OperationUpsert, Path: "apps/web/recovered.ts",
		ContentHash: digestFixture("recovered-file"), ByteSize: 19,
	})

	acknowledgementErr := errors.New("commit acknowledgement lost")
	realRunner := fixture.store.runTransaction
	realLookup := fixture.store.recoveryLookup
	requestContext, cancelRequest := context.WithCancel(fixture.context)
	fixture.store.runTransaction = func(ctx context.Context, operation func(*gorm.DB) error) error {
		if err := realRunner(ctx, operation); err != nil {
			return err
		}
		cancelRequest()
		return acknowledgementErr
	}
	recoveryCalled := false
	fixture.store.recoveryLookup = func(
		ctx context.Context,
		projectID, candidateID, operationID string,
	) (CommittedMutation, bool, error) {
		recoveryCalled = true
		if ctx.Err() != nil {
			return CommittedMutation{}, false, fmt.Errorf("recovery inherited cancellation: %w", ctx.Err())
		}
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			return CommittedMutation{}, false, errors.New("recovery context has no short deadline")
		}
		return realLookup(ctx, projectID, candidateID, operationID)
	}
	recovered, err := fixture.store.AppendOperation(requestContext, command)
	if err != nil {
		t.Fatalf("recover committed append after acknowledgement loss: %v", err)
	}
	if !recoveryCalled {
		t.Fatal("commit acknowledgement error did not trigger recovery lookup")
	}
	if err := recovered.matchesCommand(command); err != nil {
		t.Fatalf("recovered committed mutation mismatch: %v", err)
	}
	assertJournalCount(t, fixture, seed.candidateID, 1)

	fixture.store.runTransaction = realRunner
	fixture.store.recoveryLookup = realLookup
	uncommittedSeed := fixture.seedCandidate(t)
	uncommittedRecord, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), uncommittedSeed.candidateID.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	uncommittedCommand, _ := fixture.commandFor(t, uncommittedRecord, FileOperation{
		ID: "confirmed-uncommitted", Kind: OperationUpsert, Path: "apps/web/uncommitted.ts",
		ContentHash: digestFixture("uncommitted-file"), ByteSize: 13,
	})
	fixture.store.runTransaction = func(context.Context, func(*gorm.DB) error) error {
		return acknowledgementErr
	}
	if _, err := fixture.store.AppendOperation(fixture.context, uncommittedCommand); !errors.Is(err, acknowledgementErr) ||
		errors.Is(err, ErrAppendOutcomeUnknown) {
		t.Fatalf("confirmed uncommitted error = %v, want original acknowledgement error only", err)
	}
	assertJournalCount(t, fixture, uncommittedSeed.candidateID, 0)

	lookupErr := errors.New("recovery database unavailable")
	fixture.store.recoveryLookup = func(
		context.Context, string, string, string,
	) (CommittedMutation, bool, error) {
		return CommittedMutation{}, false, lookupErr
	}
	unknownSeed := fixture.seedCandidate(t)
	unknownRecord, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), unknownSeed.candidateID.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	unknownCommand, _ := fixture.commandFor(t, unknownRecord, FileOperation{
		ID: "unknown-outcome", Kind: OperationUpsert, Path: "apps/web/unknown.ts",
		ContentHash: digestFixture("unknown-file"), ByteSize: 7,
	})
	_, err = fixture.store.AppendOperation(fixture.context, unknownCommand)
	if !errors.Is(err, ErrAppendOutcomeUnknown) || !errors.Is(err, acknowledgementErr) || !errors.Is(err, lookupErr) {
		t.Fatalf("unknown outcome error did not preserve sentinel and causes: %v", err)
	}
	assertJournalCount(t, fixture, unknownSeed.candidateID, 0)
}

func openCandidateStorePostgresFixture(t *testing.T) *candidateStorePostgresFixture {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	schema := "repository_candidate_store_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	scopedDSN := candidateStoreDSNWithSearchPath(t, dsn, schema)
	database, err := sql.Open("pgx", scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := migrations.Up(ctx, database); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	gormDatabase, err := gorm.Open(postgres.New(postgres.Config{Conn: database}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &candidateStorePostgresFixture{
		context: ctx, database: database, gorm: gormDatabase,
		trees:   &candidateTreeReaderFake{trees: make(map[string]TreeManifest)},
		actorID: uuid.New(), projectID: uuid.New(), otherProjectID: uuid.New(),
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'Candidate store actor', 'not-used')
`, fixture.actorID, "candidate-store-"+uuid.NewString()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, 'Candidate store', $3), ($2, 'Other candidate store', $3)
`, fixture.projectID, fixture.otherProjectID, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	fixture.store, err = NewGORMCandidateStore(gormDatabase, fixture.trees)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *candidateStorePostgresFixture) seedCandidate(t *testing.T) candidateStoreSeed {
	t.Helper()
	baseTree, err := NewTree([]TreeFile{{
		Path: "README.md", Mode: "100644", ContentHash: digestFixture("candidate-store-base-file-" + uuid.NewString()),
		ByteSize: 4,
	}})
	if err != nil {
		t.Fatal(err)
	}
	seed := candidateStoreSeed{
		candidateID: uuid.New(), snapshotID: uuid.New(), manifestID: uuid.New(), contractID: uuid.New(),
		fullStackID: uuid.New(), workspaceArtifactID: uuid.New(), workspaceRevisionID: uuid.New(), baseTree: baseTree,
		manifestHash:         strings.TrimPrefix(digestFixture("candidate-store-manifest-"+uuid.NewString()), "sha256:"),
		contractHash:         strings.TrimPrefix(digestFixture("candidate-store-contract-"+uuid.NewString()), "sha256:"),
		fullStackHash:        digestFixture("candidate-store-full-stack-" + uuid.NewString()),
		workspaceContentHash: digestFixture("candidate-store-workspace-" + uuid.NewString()),
		candidateCreated:     time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond),
	}
	seed.basePointer = TreeBlobPointer{
		Store: TreeContentStore, Ref: "candidate-tree-" + uuid.NewString(), OwnerID: seed.snapshotID.String(),
		TreeHash: baseTree.TreeHash, FileCount: len(baseTree.Files), ByteSize: treeByteSize(baseTree),
		ContentObjectHash: digestFixture("candidate-store-base-object-" + uuid.NewString()),
	}
	transaction, err := fixture.database.BeginTx(fixture.context, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(fixture.context, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatalf("disable seed triggers: %v", err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO repository_snapshots (
  id, schema_version, project_id,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
  tree_file_count, tree_byte_size, created_by, created_at
) VALUES (
  $1, 'repository-snapshot/v1', $2,
  $3, $4, $5, $6, $7, $8,
  $9, $10, $11,
  $12, $1, $13, $14, $15, $16, $17, $18, $19
)
`, seed.snapshotID, fixture.projectID,
		seed.manifestID, seed.manifestHash, seed.contractID, seed.contractHash,
		seed.fullStackID, seed.fullStackHash,
		seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceContentHash,
		seed.basePointer.Store, seed.basePointer.Ref, seed.basePointer.ContentObjectHash,
		seed.basePointer.TreeHash, seed.basePointer.FileCount, seed.basePointer.ByteSize,
		fixture.actorID, seed.candidateCreated); err != nil {
		t.Fatalf("seed repository snapshot: %v", err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
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
  $1, 'candidate-workspace/v1', $2, $3,
  $4, $5, $6, $7, $8, $9,
  $10, $11, $12,
  $13, $3, $14, $15, $16,
  $13, $3, $14, $15, $16, $17, $18,
  'active', false, false, false, false,
  1, 1, 0, 0, $19, $20, $20
)
`, seed.candidateID, fixture.projectID, seed.snapshotID,
		seed.manifestID, seed.manifestHash, seed.contractID, seed.contractHash,
		seed.fullStackID, seed.fullStackHash,
		seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceContentHash,
		seed.basePointer.Store, seed.basePointer.Ref, seed.basePointer.ContentObjectHash,
		seed.basePointer.TreeHash, seed.basePointer.FileCount, seed.basePointer.ByteSize,
		fixture.actorID, seed.candidateCreated); err != nil {
		t.Fatalf("seed candidate workspace: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit candidate seed: %v", err)
	}
	var candidateVersion, sessionEpoch, leaseEpoch int64
	var leaseExpiresAt time.Time
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT candidate_version, session_epoch, writer_lease_epoch, writer_lease_expires_at
FROM acquire_candidate_workspace_lease($1, 1, $2, 300)
`, seed.candidateID, fixture.actorID).Scan(
		&candidateVersion, &sessionEpoch, &leaseEpoch, &leaseExpiresAt,
	); err != nil {
		t.Fatalf("acquire candidate lease: %v", err)
	}
	if candidateVersion != 2 || sessionEpoch != 1 || leaseEpoch != 1 || !leaseExpiresAt.After(time.Now()) {
		t.Fatalf("unexpected seeded lease: version=%d session=%d lease=%d expires=%s",
			candidateVersion, sessionEpoch, leaseEpoch, leaseExpiresAt)
	}
	fixture.trees.trees[seed.basePointer.Ref] = baseTree
	return seed
}

func (fixture *candidateStorePostgresFixture) commandFor(
	t *testing.T,
	record CandidateMutationRecord,
	operation FileOperation,
) (AppendOperationCommand, TreeManifest) {
	t.Helper()
	now := record.Candidate.UpdatedAt.Add(time.Millisecond)
	if !now.Before(record.Candidate.Lease.ExpiresAt) {
		t.Fatal("test candidate lease expired")
	}
	next, entry, err := record.Candidate.Apply(
		record.Candidate.Version,
		record.Candidate.SessionEpoch,
		record.Candidate.WriterLeaseEpoch,
		fixture.actorID.String(),
		"user",
		operation,
		now,
	)
	if err != nil {
		t.Fatalf("derive append command: %v", err)
	}
	afterPointer := TreeBlobPointer{
		Store: TreeContentStore, Ref: "candidate-tree-" + uuid.NewString(), OwnerID: record.Candidate.ID,
		TreeHash: next.CurrentTree.TreeHash, FileCount: len(next.CurrentTree.Files),
		ByteSize:          treeByteSize(next.CurrentTree),
		ContentObjectHash: digestFixture("candidate-store-after-object-" + operation.ID),
	}
	return AppendOperationCommand{
		ProjectID: record.Candidate.ProjectID, CandidateAfter: next, Entry: entry,
		BeforeTree: record.CurrentTreePointer, AfterTree: afterPointer,
	}, next.CurrentTree
}

func (fixture *candidateStorePostgresFixture) replicaUpdateCandidate(
	t *testing.T,
	candidateID uuid.UUID,
	setClause string,
) {
	t.Helper()
	transaction, err := fixture.database.BeginTx(fixture.context, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(fixture.context, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	query := "UPDATE candidate_workspaces SET " + setClause + " WHERE id = $1"
	if _, err := transaction.ExecContext(fixture.context, query, candidateID); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func assertLoadedCandidate(
	t *testing.T,
	fixture *candidateStorePostgresFixture,
	seed candidateStoreSeed,
	record CandidateMutationRecord,
) {
	t.Helper()
	candidate := record.Candidate
	if record.CurrentTreePointer != seed.basePointer || !equalTrees(candidate.CurrentTree, seed.baseTree) ||
		candidate.SchemaVersion != CandidateSchemaVersion || candidate.ID != seed.candidateID.String() ||
		candidate.ProjectID != fixture.projectID.String() || candidate.RepositorySnapshotID != seed.snapshotID.String() ||
		candidate.BuildManifest != (ExactReference{ID: seed.manifestID.String(), ContentHash: seed.manifestHash}) ||
		candidate.BuildContract != (ExactReference{ID: seed.contractID.String(), ContentHash: seed.contractHash}) ||
		candidate.FullStackTemplate != (ExactReference{ID: seed.fullStackID.String(), ContentHash: seed.fullStackHash}) ||
		candidate.BaseWorkspaceRevision == nil ||
		*candidate.BaseWorkspaceRevision != (ExactRevisionReference{
			ArtifactID: seed.workspaceArtifactID.String(), RevisionID: seed.workspaceRevisionID.String(),
			ContentHash: seed.workspaceContentHash,
		}) || candidate.BaseTreeHash != seed.baseTree.TreeHash ||
		candidate.Status != CandidateActive || candidate.Dirty || candidate.SessionEpoch != 1 || candidate.Version != 2 ||
		candidate.JournalSequence != 0 || candidate.WriterLeaseEpoch != 1 || candidate.Lease == nil ||
		candidate.Lease.OwnerID != fixture.actorID.String() || candidate.Lease.Epoch != 1 {
		t.Fatalf("loaded candidate lost exact persisted facts: %#v pointer=%#v", candidate, record.CurrentTreePointer)
	}
}

func assertCandidateProjection(
	t *testing.T,
	fixture *candidateStorePostgresFixture,
	candidateID uuid.UUID,
	command AppendOperationCommand,
) {
	t.Helper()
	var version, sequence int64
	var dirty bool
	var store string
	var owner uuid.UUID
	var ref, contentHash, treeHash string
	var fileCount int
	var byteSize int64
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT version, journal_sequence, dirty,
       current_tree_store, current_tree_owner_id, current_tree_ref,
       current_tree_content_hash, current_tree_hash,
       current_tree_file_count, current_tree_byte_size
FROM candidate_workspaces
WHERE project_id = $1 AND id = $2
`, fixture.projectID, candidateID).Scan(
		&version, &sequence, &dirty, &store, &owner, &ref, &contentHash, &treeHash, &fileCount, &byteSize,
	); err != nil {
		t.Fatal(err)
	}
	if version != int64(command.Entry.CandidateTo) || sequence != int64(command.Entry.Sequence) || !dirty ||
		store != command.AfterTree.Store || owner.String() != command.AfterTree.OwnerID || ref != command.AfterTree.Ref ||
		contentHash != command.AfterTree.ContentObjectHash || treeHash != command.AfterTree.TreeHash ||
		fileCount != command.AfterTree.FileCount || byteSize != command.AfterTree.ByteSize {
		t.Fatalf("Candidate trigger projection drifted: version=%d sequence=%d dirty=%v pointer=%s/%s/%s/%s/%s/%d/%d",
			version, sequence, dirty, store, owner, ref, contentHash, treeHash, fileCount, byteSize)
	}
}

func assertJournalCount(
	t *testing.T,
	fixture *candidateStorePostgresFixture,
	candidateID uuid.UUID,
	want int,
) {
	t.Helper()
	var count int
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT count(*) FROM candidate_workspace_journal WHERE candidate_id = $1
`, candidateID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("journal count = %d, want %d", count, want)
	}
}

func assertPostgresErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != want {
		t.Fatalf("PostgreSQL error = %#v in %v, want SQLSTATE %s", postgresError, err, want)
	}
}

func candidateStoreDSNWithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	if parsed, err := url.Parse(strings.TrimSpace(dsn)); err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}
