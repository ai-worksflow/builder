package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCandidateRebaseStorePostgresPersistsConflictAndImmutableSuccessorResolution(t *testing.T) {
	fixture := openCandidateStorePostgresFixture(t)
	predecessorSeed := fixture.seedCandidate(t)
	targetSeed := fixture.seedCandidate(t)
	resetCandidateToCleanSnapshot(t, fixture, targetSeed.candidateID)

	predecessor, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), predecessorSeed.candidateID.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	predecessorChange := FileOperation{
		ID: "predecessor-change", Kind: OperationUpsert, Path: "README.md",
		ExpectedHash: predecessorSeed.baseTree.Files[0].ContentHash,
		ContentHash:  digestFixture("rebase-predecessor-change"), ByteSize: 8, Mode: "100644",
	}
	command, changedTree := candidateRebaseAppendCommand(
		t, fixture, predecessor, predecessorChange, "user",
	)
	if _, err := fixture.store.AppendOperation(fixture.context, command); err != nil {
		t.Fatalf("append predecessor change: %v", err)
	}
	fixture.trees.trees[command.AfterTree.Ref] = changedTree
	predecessor, err = fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), predecessorSeed.candidateID.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	target, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), targetSeed.candidateID.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.ExecContext(fixture.context, `
INSERT INTO application_build_manifests (
  id, project_id, root_manifest_id, schema_version, content_store, content_ref,
  content_hash, manifest_hash, status, created_by, created_at
) VALUES ($1, $2, $1, 1, 'mongo', $3, $4, $5, 'frozen', $6, $7)
`, targetSeed.manifestID, fixture.projectID, "rebase-target-"+targetSeed.manifestID.String(),
		digestFixture("rebase-target-content-"+targetSeed.manifestID.String()), targetSeed.manifestHash,
		fixture.actorID, targetSeed.candidateCreated); err != nil {
		t.Fatalf("insert target BuildManifest identity: %v", err)
	}

	rebaseID := uuid.NewString()
	plan, err := PlanCandidateRebase(
		rebaseID, predecessorSeed.baseTree, predecessor.Candidate.CurrentTree, target.Candidate.CurrentTree,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 0 || len(plan.Conflicts) != 1 || plan.Conflicts[0].Path != "README.md" {
		t.Fatalf("unexpected conflict plan: %#v", plan)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	store, err := NewCandidateRebaseStore(fixture.gorm, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	rebase, err := store.Create(fixture.context, CreateCandidateRebaseCommand{
		Rebase: CandidateRebase{
			SchemaVersion: CandidateRebaseSchemaVersion, ID: rebaseID,
			ProjectID: fixture.projectID.String(), OperationID: "rebase-store-canary",
			PredecessorCandidateID: predecessor.Candidate.ID, SuccessorCandidateID: target.Candidate.ID,
			TargetBuildManifestID: target.Candidate.BuildManifest.ID,
			AncestorTreeHash:      plan.AncestorTreeHash, PredecessorTreeHash: plan.PredecessorTreeHash,
			TargetTreeHash: plan.TargetTreeHash, PlannedTreeHash: plan.PlannedTreeHash,
			PlanHash: plan.PlanHash, State: CandidateRebaseApplying, Version: 1,
			CreatedBy: fixture.actorID.String(), CreatedAt: now, UpdatedAt: now,
		},
		Plan: plan, ExpectedCandidateVersion: predecessor.Candidate.Version,
		ExpectedSessionEpoch:     predecessor.Candidate.SessionEpoch,
		ExpectedWriterLeaseEpoch: predecessor.Candidate.WriterLeaseEpoch,
	})
	if err != nil {
		t.Fatalf("create Candidate rebase: %v", err)
	}
	if rebase.State != CandidateRebaseApplying || rebase.PlanHash != plan.PlanHash || len(rebase.Conflicts) != 1 ||
		rebase.Conflicts[0].State != CandidateRebaseConflictOpen {
		t.Fatalf("hydrated rebase = %#v", rebase)
	}
	discovery := &CandidateBootstrapService{
		database: fixture.gorm, candidates: fixture.store, access: bootstrapAccessFake{},
	}
	heads, err := discovery.ListHeads(
		fixture.context, fixture.projectID.String(), fixture.actorID.String(),
	)
	if err != nil || len(heads.Candidates) != 1 ||
		heads.Candidates[0].Candidate.ID != target.Candidate.ID || heads.Candidates[0].RebaseID != rebase.ID {
		t.Fatalf("successor was not the only durable Candidate head: %#v err=%v", heads, err)
	}
	stale, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), predecessor.Candidate.ID,
	)
	if err != nil || !stale.Candidate.Stale || !stale.Candidate.RebaseRequired || stale.Candidate.Lease != nil {
		t.Fatalf("predecessor was not atomically fenced: %#v err=%v", stale.Candidate, err)
	}
	if _, err := store.Create(fixture.context, CreateCandidateRebaseCommand{}); !errors.Is(err, ErrInvalidRebase) {
		t.Fatalf("invalid rebase create error = %v", err)
	}

	controls, err := NewCandidateControlStore(fixture.gorm, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	flagged, err := controls.UpdateFlags(fixture.context, UpdateCandidateFlagsInput{
		ProjectID: fixture.projectID.String(), CandidateID: target.Candidate.ID,
		ExpectedCandidateVersion: target.Candidate.Version, ExpectedSessionEpoch: target.Candidate.SessionEpoch,
		ExpectedWriterLeaseEpoch: target.Candidate.WriterLeaseEpoch, ActorID: fixture.actorID.String(),
		Flags: CandidateFlags{Conflicted: true}, Reason: "test exact rebase conflict",
		EvidenceRef: candidateRebaseEvidenceRef(rebase.ID), EvidenceHash: rebase.PlanHash,
	})
	if err != nil {
		t.Fatalf("flag successor conflicted: %v", err)
	}
	rebase, err = store.Transition(
		fixture.context, fixture.projectID.String(), rebase.ID,
		CandidateRebaseApplying, CandidateRebaseConflicted, rebase.Version,
	)
	if err != nil || rebase.State != CandidateRebaseConflicted || rebase.Version != 2 {
		t.Fatalf("transition conflicted = %#v err=%v", rebase, err)
	}

	leased, err := controls.AcquireLease(
		fixture.context, fixture.projectID.String(), target.Candidate.ID,
		flagged.Candidate.Version, fixture.actorID.String(), time.Minute,
	)
	if err != nil {
		t.Fatalf("lease conflicted successor: %v", err)
	}
	selected := plan.Conflicts[0].PredecessorFile
	resolution := rebaseFileOperation(rebase.ID, 0, "README.md", plan.Conflicts[0].TargetFile, selected)
	resolution.ID = "rebase-resolve:" + plan.Conflicts[0].ID
	resolutionCommand, resolvedTree := candidateRebaseAppendCommand(
		t, fixture, leased, resolution, "merge",
	)
	if _, err := fixture.store.AppendOperation(fixture.context, resolutionCommand); err != nil {
		t.Fatalf("append exact conflict resolution: %v", err)
	}
	fixture.trees.trees[resolutionCommand.AfterTree.Ref] = resolvedTree
	resolvedCandidate, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), target.Candidate.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	rebase, err = store.ResolveConflict(fixture.context, ResolveCandidateRebaseConflictCommand{
		ProjectID: fixture.projectID.String(), RebaseID: rebase.ID, ConflictID: plan.Conflicts[0].ID,
		ExpectedConflictVersion: 1, ExpectedSuccessorCandidateVersion: resolvedCandidate.Candidate.Version,
		ExpectedSessionEpoch:     resolvedCandidate.Candidate.SessionEpoch,
		ExpectedWriterLeaseEpoch: resolvedCandidate.Candidate.WriterLeaseEpoch,
		ActorID:                  fixture.actorID.String(), Strategy: CandidateRebaseUsePredecessor,
		ResolutionFile: cloneTreeFile(selected),
	})
	if err != nil {
		t.Fatalf("resolve final conflict: %v", err)
	}
	if rebase.State != CandidateRebaseReady || rebase.Version != 3 ||
		rebase.Conflicts[0].State != CandidateRebaseConflictResolved ||
		rebase.Conflicts[0].ResolutionStrategy == nil ||
		*rebase.Conflicts[0].ResolutionStrategy != CandidateRebaseUsePredecessor ||
		!equalTreeFile(rebase.Conflicts[0].ResolutionFile, selected) {
		t.Fatalf("resolved rebase = %#v", rebase)
	}
	ready, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), target.Candidate.ID,
	)
	if err != nil || ready.Candidate.Conflicted || ready.Candidate.Lease != nil {
		t.Fatalf("ready successor remains blocked: %#v err=%v", ready.Candidate, err)
	}
}

func TestCandidateRebaseServicePostgresResumesAutomaticPlanToReadySuccessor(t *testing.T) {
	fixture := openCandidateStorePostgresFixture(t)
	predecessorSeed := fixture.seedCandidate(t)
	targetSeed := fixture.seedCandidate(t)
	alignCleanCandidateWithTree(t, fixture, targetSeed, predecessorSeed.baseTree)

	predecessor, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), predecessorSeed.candidateID.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	change := FileOperation{
		ID: "predecessor-automatic-change", Kind: OperationUpsert, Path: "README.md",
		ExpectedHash: predecessorSeed.baseTree.Files[0].ContentHash,
		ContentHash:  digestFixture("automatic-rebase-file"), ByteSize: 12, Mode: "100755",
	}
	command, changedTree := candidateRebaseAppendCommand(t, fixture, predecessor, change, "user")
	if _, err := fixture.store.AppendOperation(fixture.context, command); err != nil {
		t.Fatal(err)
	}
	fixture.trees.trees[command.AfterTree.Ref] = changedTree
	predecessor, err = fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), predecessorSeed.candidateID.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	target, err := fixture.store.LoadMutationCandidate(
		fixture.context, fixture.projectID.String(), targetSeed.candidateID.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	insertRebaseTargetManifest(t, fixture, targetSeed)

	rebaseID := uuid.NewString()
	plan, err := PlanCandidateRebase(
		rebaseID, predecessorSeed.baseTree, predecessor.Candidate.CurrentTree, target.Candidate.CurrentTree,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 || len(plan.Conflicts) != 0 || plan.PlannedTreeHash != changedTree.TreeHash {
		t.Fatalf("automatic plan = %#v", plan)
	}
	rebaseStore, err := NewCandidateRebaseStore(fixture.gorm, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	rebase, err := rebaseStore.Create(fixture.context, CreateCandidateRebaseCommand{
		Rebase: CandidateRebase{
			SchemaVersion: CandidateRebaseSchemaVersion, ID: rebaseID,
			ProjectID: fixture.projectID.String(), OperationID: "automatic-rebase-canary",
			PredecessorCandidateID: predecessor.Candidate.ID, SuccessorCandidateID: target.Candidate.ID,
			TargetBuildManifestID: target.Candidate.BuildManifest.ID,
			AncestorTreeHash:      plan.AncestorTreeHash, PredecessorTreeHash: plan.PredecessorTreeHash,
			TargetTreeHash: plan.TargetTreeHash, PlannedTreeHash: plan.PlannedTreeHash,
			PlanHash: plan.PlanHash, State: CandidateRebaseApplying, Version: 1,
			CreatedBy: fixture.actorID.String(), CreatedAt: now, UpdatedAt: now,
		},
		Plan: plan, ExpectedCandidateVersion: predecessor.Candidate.Version,
		ExpectedSessionEpoch:     predecessor.Candidate.SessionEpoch,
		ExpectedWriterLeaseEpoch: predecessor.Candidate.WriterLeaseEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}

	trees := &fakeMutationTreeStore{manifests: make(map[string]TreeManifest)}
	for ref, tree := range fixture.trees.trees {
		trees.manifests[ref] = cloneTree(tree)
	}
	candidates, err := NewGORMCandidateStore(fixture.gorm, trees)
	if err != nil {
		t.Fatal(err)
	}
	controls, err := NewCandidateControlStore(fixture.gorm, candidates)
	if err != nil {
		t.Fatal(err)
	}
	rebaseStore, err = NewCandidateRebaseStore(fixture.gorm, candidates)
	if err != nil {
		t.Fatal(err)
	}
	mutations, err := NewMutationService(
		candidates, trees, &fakeMutationFileResolver{},
		&fakePathPolicyResolver{policy: PathPolicy{ExtensionPaths: []string{"src"}, ProtectedPaths: []string{"protected"}}},
		&fakeMutationAuthorizer{}, time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	service := &CandidateRebaseService{
		store: rebaseStore, controls: controls, candidates: candidates, mutations: mutations, now: time.Now,
	}
	result, err := service.resume(fixture.context, rebase, fixture.actorID.String(), true, false)
	if err != nil {
		t.Fatalf("resume automatic rebase: %v", err)
	}
	if result.Rebase.State != CandidateRebaseReady || result.Candidate.CurrentTree.TreeHash != plan.PlannedTreeHash ||
		result.Candidate.Conflicted || result.Candidate.Stale || result.Candidate.RebaseRequired ||
		result.Candidate.JournalSequence != 1 || result.Candidate.Lease == nil {
		t.Fatalf("automatic rebase result = %#v", result)
	}
	committed, found, err := candidates.FindCommittedOperation(
		fixture.context, fixture.projectID.String(), target.Candidate.ID, plan.Operations[0].Operation.ID,
	)
	if err != nil || !found || committed.Entry.Attribution != "merge" ||
		committed.Entry.Operation != plan.Operations[0].Operation {
		t.Fatalf("automatic merge journal = %#v found=%v err=%v", committed, found, err)
	}
	replayed, err := service.resume(
		fixture.context, result.Rebase, fixture.actorID.String(), false, true,
	)
	if err != nil || !replayed.Recovered || replayed.Candidate.ID != result.Candidate.ID {
		t.Fatalf("ready rebase replay = %#v err=%v", replayed, err)
	}
}

func resetCandidateToCleanSnapshot(
	t *testing.T,
	fixture *candidateStorePostgresFixture,
	candidateID uuid.UUID,
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
	if _, err := transaction.ExecContext(
		fixture.context, `DELETE FROM candidate_workspace_control_events WHERE candidate_id = $1`, candidateID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
UPDATE candidate_workspaces
SET version = 1, writer_lease_owner_id = NULL, writer_lease_epoch = 0,
    writer_lease_expires_at = NULL, updated_at = created_at
WHERE id = $1
`, candidateID); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func alignCleanCandidateWithTree(
	t *testing.T,
	fixture *candidateStorePostgresFixture,
	seed candidateStoreSeed,
	tree TreeManifest,
) {
	t.Helper()
	ref := "aligned-rebase-tree-" + uuid.NewString()
	contentHash := digestFixture("aligned-rebase-object-" + ref)
	transaction, err := fixture.database.BeginTx(fixture.context, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(fixture.context, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(
		fixture.context, `DELETE FROM candidate_workspace_control_events WHERE candidate_id = $1`, seed.candidateID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
UPDATE repository_snapshots
SET tree_ref = $2, tree_content_hash = $3, tree_hash = $4,
    tree_file_count = $5, tree_byte_size = $6
WHERE id = $1
`, seed.snapshotID, ref, contentHash, tree.TreeHash, len(tree.Files), treeByteSize(tree)); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
UPDATE candidate_workspaces
SET base_tree_ref = $1, base_tree_content_hash = $2, base_tree_hash = $3,
    current_tree_ref = $1, current_tree_content_hash = $2, current_tree_hash = $3,
    current_tree_file_count = $4, current_tree_byte_size = $5,
    version = 1, writer_lease_owner_id = NULL, writer_lease_epoch = 0,
    writer_lease_expires_at = NULL, updated_at = created_at
WHERE id = $6
`, ref, contentHash, tree.TreeHash, len(tree.Files), treeByteSize(tree), seed.candidateID); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	fixture.trees.trees[ref] = cloneTree(tree)
}

func insertRebaseTargetManifest(
	t *testing.T,
	fixture *candidateStorePostgresFixture,
	seed candidateStoreSeed,
) {
	t.Helper()
	if _, err := fixture.database.ExecContext(fixture.context, `
INSERT INTO application_build_manifests (
  id, project_id, root_manifest_id, schema_version, content_store, content_ref,
  content_hash, manifest_hash, status, created_by, created_at
) VALUES ($1, $2, $1, 1, 'mongo', $3, $4, $5, 'frozen', $6, $7)
`, seed.manifestID, fixture.projectID, "rebase-target-"+seed.manifestID.String(),
		digestFixture("rebase-target-content-"+seed.manifestID.String()), seed.manifestHash,
		fixture.actorID, seed.candidateCreated); err != nil {
		t.Fatalf("insert target BuildManifest identity: %v", err)
	}
}

func candidateRebaseAppendCommand(
	t *testing.T,
	fixture *candidateStorePostgresFixture,
	record CandidateMutationRecord,
	operation FileOperation,
	attribution string,
) (AppendOperationCommand, TreeManifest) {
	t.Helper()
	now := record.Candidate.UpdatedAt.Add(time.Millisecond)
	if record.Candidate.Lease == nil || !now.Before(record.Candidate.Lease.ExpiresAt) {
		t.Fatal("test Candidate has no valid writer lease")
	}
	next, entry, err := record.Candidate.Apply(
		record.Candidate.Version, record.Candidate.SessionEpoch, record.Candidate.WriterLeaseEpoch,
		fixture.actorID.String(), attribution, operation, now,
	)
	if err != nil {
		t.Fatalf("derive rebase append command: %v", err)
	}
	afterPointer := TreeBlobPointer{
		Store: TreeContentStore, Ref: "candidate-rebase-tree-" + uuid.NewString(), OwnerID: record.Candidate.ID,
		TreeHash: next.CurrentTree.TreeHash, FileCount: len(next.CurrentTree.Files),
		ByteSize: treeByteSize(next.CurrentTree), ContentObjectHash: digestFixture("rebase-tree-object-" + operation.ID),
	}
	return AppendOperationCommand{
		ProjectID: record.Candidate.ProjectID, CandidateAfter: next, Entry: entry,
		BeforeTree: record.CurrentTreePointer, AfterTree: afterPointer,
	}, next.CurrentTree
}
