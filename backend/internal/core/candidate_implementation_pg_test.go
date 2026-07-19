package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
	"gorm.io/gorm"
)

func TestCandidateFreezeReviewApplyClosurePostgres(t *testing.T) {
	database, cleanup := multiBundlePostgresDatabase(t)
	defer cleanup()
	fixture := seedMultiBundlePostgresFixture(t, database)
	fixture.implementation.now = func() time.Time { return time.Now().UTC() }

	const (
		packageV0 = `{"name":"multi-bundle","private":true,"dependencies":{}}`
		packageV1 = `{"name":"multi-bundle","private":true,"dependencies":{"candidate":"1.0.0"}}`
		sourceV1  = "export const candidate = 'exact'\n"
	)
	candidateID := uuid.New()
	snapshotID := uuid.New()
	checkpointID := uuid.New()
	sessionID := uuid.New()
	now := time.Now().UTC()
	baseTree, err := repository.NewTree([]repository.TreeFile{
		candidateTreeFile("package.json", "100644", []byte(packageV0)),
	})
	if err != nil {
		t.Fatal(err)
	}
	candidateTree, err := repository.NewTree([]repository.TreeFile{
		candidateTreeFile("package.json", "100644", []byte(packageV1)),
		candidateTreeFile("src/candidate.ts", "100644", []byte(sourceV1)),
	})
	if err != nil {
		t.Fatal(err)
	}
	intermediateTree, err := repository.NewTree([]repository.TreeFile{
		candidateTreeFile("package.json", "100644", []byte(packageV1)),
	})
	if err != nil {
		t.Fatal(err)
	}
	lineage := seedCandidateFreezePostgresRows(
		t,
		database,
		fixture,
		snapshotID,
		candidateID,
		checkpointID,
		sessionID,
		&fixture.workspaceW0,
		baseTree,
		intermediateTree,
		candidateTree,
		now,
	)
	baseRevision := repository.ExactRevisionReference{
		ArtifactID:  fixture.workspaceW0.ArtifactID,
		RevisionID:  fixture.workspaceW0.RevisionID,
		ContentHash: fixture.workspaceW0.ContentHash,
	}
	candidate := repository.CandidateWorkspace{
		SchemaVersion:        repository.CandidateSchemaVersion,
		ID:                   candidateID.String(),
		ProjectID:            fixture.projectID.String(),
		RepositorySnapshotID: snapshotID.String(),
		BuildManifest: repository.ExactReference{
			ID:          fixture.rootA.ID,
			ContentHash: fixture.rootA.ManifestHash,
		},
		BuildContract: repository.ExactReference{
			ID:          lineage.BuildContractID.String(),
			ContentHash: lineage.BuildContractHash,
		},
		FullStackTemplate: repository.ExactReference{
			ID:          lineage.FullStackTemplateID.String(),
			ContentHash: lineage.FullStackTemplateHash,
		},
		BaseWorkspaceRevision: &baseRevision,
		BaseTreeHash:          baseTree.TreeHash,
		CurrentTree:           candidateTree,
		Status:                repository.CandidateActive,
		Dirty:                 true,
		SessionEpoch:          1,
		Version:               4,
		JournalSequence:       2,
		WriterLeaseEpoch:      1,
		Lease: &repository.WriterLease{
			OwnerID:   fixture.ownerID.String(),
			Epoch:     1,
			ExpiresAt: now.Add(20 * time.Minute),
		},
		CreatedBy: fixture.ownerID.String(),
		CreatedAt: now.Add(-2 * time.Minute),
		UpdatedAt: now.Add(-time.Minute),
	}
	treePointer := repository.TreeBlobPointer{
		Store:             "candidate-test",
		Ref:               "candidate-tree-" + candidateID.String(),
		OwnerID:           candidateID.String(),
		ContentObjectHash: hashText("candidate-tree-object:" + candidateID.String()),
		TreeHash:          candidateTree.TreeHash,
		FileCount:         len(candidateTree.Files),
		ByteSize:          candidateTreeByteSize(candidateTree),
	}
	verificationReceipt := seedCandidateFreezeVerificationReceipt(
		t, database, fixture, fixture.workspaceW0, lineage, sessionID, candidateID, checkpointID,
		candidateTree, treePointer, now,
	)
	files := candidateImplementationFilesFake{values: map[string][]byte{
		hashBytes([]byte(packageV1)): []byte(packageV1),
		hashBytes([]byte(sourceV1)):  []byte(sourceV1),
	}}
	request := CandidateImplementationRequest{
		RequestKey:               "freeze-" + candidateID.String(),
		SessionID:                sessionID.String(),
		CandidateID:              candidateID.String(),
		CheckpointID:             checkpointID.String(),
		VerificationReceipt:      verificationReceipt,
		Reason:                   "Freeze exact Candidate into implementation Proposal",
		ExpectedSessionVersion:   3,
		ExpectedSessionEpoch:     1,
		ExpectedCandidateVersion: 4,
		ExpectedWriterLeaseEpoch: 1,
	}
	tampered := request
	tampered.RequestKey = "freeze-tampered-" + candidateID.String()
	tampered.VerificationReceipt.ContentHash = hashText("tampered-verification-receipt")
	if _, err := fixture.implementation.CreateCandidateImplementation(
		context.Background(),
		fixture.projectID.String(),
		fixture.ownerID.String(),
		tampered,
		repository.CandidateMutationRecord{
			Candidate:          candidate,
			CurrentTreePointer: treePointer,
		},
		files,
	); err == nil {
		t.Fatal("tampered VerificationReceipt hash authorized Candidate freeze")
	}
	var activeAfterRejectedFreeze struct {
		Status  string
		Version uint64
	}
	if err := database.Raw(
		"SELECT status, version FROM candidate_workspaces WHERE id = ?",
		candidateID,
	).Scan(&activeAfterRejectedFreeze).Error; err != nil {
		t.Fatal(err)
	}
	if activeAfterRejectedFreeze.Status != "active" || activeAfterRejectedFreeze.Version != 4 {
		t.Fatalf("rejected freeze changed Candidate: %#v", activeAfterRejectedFreeze)
	}

	result, err := fixture.implementation.CreateCandidateImplementation(
		context.Background(),
		fixture.projectID.String(),
		fixture.ownerID.String(),
		request,
		repository.CandidateMutationRecord{
			Candidate:          candidate,
			CurrentTreePointer: treePointer,
		},
		files,
	)
	if err != nil {
		t.Fatalf("freeze exact Candidate: %v", err)
	}
	if result.Proposal.CandidateSource == nil ||
		result.Proposal.CandidateSource.SessionID != sessionID.String() ||
		result.Proposal.CandidateSource.TreeHash != candidateTree.TreeHash ||
		result.Proposal.CandidateSource.VerificationReceipt.ID != verificationReceipt.ID ||
		result.Proposal.CandidateSource.VerificationReceipt.ContentHash != verificationReceipt.ContentHash ||
		result.Receipt.VerificationReceipt != verificationReceipt ||
		result.Receipt.ImplementationProposalID != result.Proposal.ID ||
		len(result.Proposal.Operations) != 2 {
		t.Fatalf("freeze result lost exact identity: %#v", result)
	}
	baseWorkspace, _, _, err := fixture.implementation.loadWorkspace(
		context.Background(), fixture.projectID, result.Proposal.BaseWorkspaceRevision,
	)
	if err != nil {
		t.Fatal(err)
	}
	proposedWorkspace, err := applyFileOperations(baseWorkspace, result.Proposal.Operations)
	if err != nil {
		t.Fatalf("reconstruct exact Candidate Proposal: %v", err)
	}
	proposedTree, err := candidateWorkspaceTree(proposedWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if proposedTree.TreeHash != candidateTree.TreeHash {
		t.Fatalf(
			"Candidate Proposal operations reconstruct tree %#v, want %#v; operations=%#v",
			proposedTree, candidateTree, result.Proposal.Operations,
		)
	}
	var frozenState struct {
		Status             string
		Version            uint64
		WriterLeaseOwnerID *uuid.UUID
	}
	if err := database.Raw(
		"SELECT status, version, writer_lease_owner_id FROM candidate_workspaces WHERE id = ?",
		candidateID,
	).Scan(&frozenState).Error; err != nil {
		t.Fatal(err)
	}
	if frozenState.Status != "frozen" || frozenState.Version != 5 ||
		frozenState.WriterLeaseOwnerID != nil {
		t.Fatalf("Candidate did not freeze atomically: %#v", frozenState)
	}
	var projected struct {
		Version           uint64
		CandidateVersion  uint64
		CandidateTreeHash string
	}
	if err := database.Raw(
		"SELECT version, candidate_version, candidate_tree_hash FROM sandbox_sessions WHERE id = ?",
		sessionID,
	).Scan(&projected).Error; err != nil {
		t.Fatal(err)
	}
	if projected.Version != 4 || projected.CandidateVersion != 5 ||
		projected.CandidateTreeHash != candidateTree.TreeHash {
		t.Fatalf("Sandbox projection did not advance with the freeze: %#v", projected)
	}

	replay, found, err := fixture.implementation.FindCandidateImplementation(
		context.Background(),
		fixture.projectID.String(),
		fixture.ownerID.String(),
		request,
	)
	if err != nil || !found || !replay.Replayed ||
		replay.Proposal.ID != result.Proposal.ID ||
		replay.Receipt.ID != result.Receipt.ID {
		t.Fatalf("committed freeze was not intrinsically replayable: found=%v result=%#v err=%v", found, replay, err)
	}

	reviewed := result.Proposal
	for _, operation := range result.Proposal.Operations {
		reviewed, err = fixture.implementation.Decide(
			context.Background(),
			reviewed.ID,
			fixture.ownerID.String(),
			DecideImplementationInput{
				OperationID: operation.ID,
				Decision:    ImplementationAccepted,
				Version:     reviewed.Version,
			},
		)
		if err != nil {
			t.Fatalf("accept exact Candidate operation %s: %v", operation.ID, err)
		}
	}
	if reviewed.Status != "ready" {
		t.Fatalf("fully accepted Candidate Proposal status = %q", reviewed.Status)
	}
	revision, err := fixture.implementation.Apply(
		context.Background(),
		reviewed.ID,
		fixture.ownerID.String(),
		ApplyImplementationInput{Version: reviewed.Version},
	)
	if err != nil {
		t.Fatalf("apply exact Candidate Proposal: %v", err)
	}
	assertMultiBundleWorkspaceFile(t, revision.Content, "package.json", packageV1)
	assertMultiBundleWorkspaceFile(t, revision.Content, "src/candidate.ts", sourceV1)
	var workspace map[string]any
	if err := json.Unmarshal(revision.Content, &workspace); err != nil {
		t.Fatal(err)
	}
	appliedTree, err := candidateWorkspaceTree(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if appliedTree.TreeHash != candidateTree.TreeHash {
		t.Fatalf("immutable revision tree = %s, frozen Candidate tree = %s", appliedTree.TreeHash, candidateTree.TreeHash)
	}
	applied, err := fixture.implementation.Get(
		context.Background(),
		reviewed.ID,
		fixture.ownerID.String(),
	)
	if err != nil || applied.Status != "applied" || applied.CandidateSource == nil ||
		applied.CandidateSource.FreezeReceiptID != result.Receipt.ID ||
		applied.CandidateSource.VerificationReceipt.ID != verificationReceipt.ID ||
		applied.CandidateSource.VerificationReceipt.ContentHash != verificationReceipt.ContentHash {
		t.Fatalf("applied Proposal lost immutable Candidate receipt: proposal=%#v err=%v", applied, err)
	}
}

type candidateFreezePostgresLineage struct {
	BuildContractID       uuid.UUID `gorm:"column:build_contract_id"`
	BuildContractHash     string    `gorm:"column:build_contract_hash"`
	FullStackTemplateID   uuid.UUID `gorm:"column:full_stack_template_id"`
	FullStackTemplateHash string    `gorm:"column:full_stack_template_hash"`
}

func seedCandidateFreezePostgresRows(
	t *testing.T,
	database *gorm.DB,
	fixture multiBundlePostgresFixture,
	snapshotID uuid.UUID,
	candidateID uuid.UUID,
	checkpointID uuid.UUID,
	sessionID uuid.UUID,
	baseWorkspace *VersionRef,
	baseTree repository.TreeManifest,
	intermediateTree repository.TreeManifest,
	candidateTree repository.TreeManifest,
	now time.Time,
) candidateFreezePostgresLineage {
	t.Helper()
	contract := multiBundleBuildContractRef(fixture.rootA.ID)
	var lineage candidateFreezePostgresLineage
	if err := database.Raw(
		`SELECT id AS build_contract_id, contract_hash AS build_contract_hash,
            full_stack_template_id, full_stack_template_hash
       FROM application_build_contracts
      WHERE id = ? AND project_id = ?`,
		uuid.MustParse(contract.ID),
		fixture.projectID,
	).Scan(&lineage).Error; err != nil {
		t.Fatal(err)
	}
	var baseWorkspaceArtifactID, baseWorkspaceRevisionID, baseWorkspaceContentHash any
	if baseWorkspace != nil {
		baseWorkspaceArtifactID = uuid.MustParse(baseWorkspace.ArtifactID)
		baseWorkspaceRevisionID = uuid.MustParse(baseWorkspace.RevisionID)
		baseWorkspaceContentHash = baseWorkspace.ContentHash
	}
	baseTreeContentHash := hashText("base-tree-object:" + snapshotID.String())
	intermediateTreeContentHash := hashText("intermediate-tree-object:" + candidateID.String())
	candidateTreeContentHash := hashText("candidate-tree-object:" + candidateID.String())
	if err := database.Exec(`
INSERT INTO repository_snapshots (
  id, schema_version, project_id,
  build_manifest_id, build_manifest_hash,
  build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
  tree_file_count, tree_byte_size, created_by, created_at
) VALUES (
  ?, 'repository-snapshot/v1', ?,
  ?, ?, ?, ?, ?, ?,
  ?, ?, ?,
  'candidate-test', ?, ?, ?, ?,
  ?, ?, ?, ?
)`,
		snapshotID,
		fixture.projectID,
		uuid.MustParse(fixture.rootA.ID),
		fixture.rootA.ManifestHash,
		lineage.BuildContractID,
		lineage.BuildContractHash,
		lineage.FullStackTemplateID,
		lineage.FullStackTemplateHash,
		baseWorkspaceArtifactID,
		baseWorkspaceRevisionID,
		baseWorkspaceContentHash,
		snapshotID,
		"base-tree-"+snapshotID.String(),
		baseTreeContentHash,
		baseTree.TreeHash,
		len(baseTree.Files),
		candidateTreeByteSize(baseTree),
		fixture.ownerID,
		now.Add(-3*time.Minute),
	).Error; err != nil {
		t.Fatalf("seed repository snapshot: %v", err)
	}

	candidateCreatedAt := now.Add(-2 * time.Minute)
	if err := database.Exec(`
INSERT INTO candidate_workspaces (
  id, schema_version, project_id, repository_snapshot_id,
  build_manifest_id, build_manifest_hash,
  build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  base_tree_store, base_tree_owner_id, base_tree_ref, base_tree_content_hash, base_tree_hash,
  current_tree_store, current_tree_owner_id, current_tree_ref,
  current_tree_content_hash, current_tree_hash, current_tree_file_count, current_tree_byte_size,
  status, dirty, conflicted, stale, rebase_required,
  session_epoch, version, journal_sequence,
  writer_lease_owner_id, writer_lease_epoch, writer_lease_expires_at,
  created_by, created_at, updated_at
) VALUES (
  ?, 'candidate-workspace/v1', ?, ?,
  ?, ?, ?, ?, ?, ?,
  ?, ?, ?,
  'candidate-test', ?, ?, ?, ?,
  'candidate-test', ?, ?, ?, ?, ?, ?,
  'active', false, false, false, false,
  1, 1, 0, NULL, 0, NULL,
  ?, ?, ?
)`,
		candidateID,
		fixture.projectID,
		snapshotID,
		uuid.MustParse(fixture.rootA.ID),
		fixture.rootA.ManifestHash,
		lineage.BuildContractID,
		lineage.BuildContractHash,
		lineage.FullStackTemplateID,
		lineage.FullStackTemplateHash,
		baseWorkspaceArtifactID,
		baseWorkspaceRevisionID,
		baseWorkspaceContentHash,
		snapshotID,
		"base-tree-"+snapshotID.String(),
		baseTreeContentHash,
		baseTree.TreeHash,
		snapshotID,
		"base-tree-"+snapshotID.String(),
		baseTreeContentHash,
		baseTree.TreeHash,
		len(baseTree.Files),
		candidateTreeByteSize(baseTree),
		fixture.ownerID,
		candidateCreatedAt,
		candidateCreatedAt,
	).Error; err != nil {
		t.Fatalf("seed active Candidate: %v", err)
	}

	var lease struct {
		CandidateVersion uint64
		WriterLeaseEpoch uint64
	}
	if err := database.Raw(
		"SELECT candidate_version, writer_lease_epoch FROM acquire_candidate_workspace_lease(?, 1, ?, 1200)",
		candidateID,
		fixture.ownerID,
	).Scan(&lease).Error; err != nil {
		t.Fatalf("acquire Candidate writer lease: %v", err)
	}
	if lease.CandidateVersion != 2 || lease.WriterLeaseEpoch != 1 {
		t.Fatalf("unexpected Candidate lease fence: %#v", lease)
	}
	var basePackageContentHash any
	for _, file := range baseTree.Files {
		if file.Path == "package.json" {
			basePackageContentHash = file.ContentHash
			break
		}
	}
	if len(baseTree.Files) > 0 && basePackageContentHash == nil {
		t.Fatal("non-empty Candidate base tree has no package.json")
	}
	intermediatePackage := requireCandidateTreeFile(t, intermediateTree, "package.json")
	sourceFile := requireCandidateTreeFile(t, candidateTree, "src/candidate.ts")
	if err := database.Exec(`
INSERT INTO candidate_workspace_journal (
  candidate_id, sequence, candidate_version_from, candidate_version_to,
  session_epoch, writer_lease_epoch, actor_id, attribution,
  operation_id, operation_kind, path,
  expected_content_hash, content_hash, byte_size, file_mode,
  before_tree_store, before_tree_owner_id, before_tree_ref,
  before_tree_content_hash, before_tree_hash,
  after_tree_store, after_tree_owner_id, after_tree_ref,
  after_tree_content_hash, after_tree_hash, after_tree_file_count, after_tree_byte_size
) VALUES (
  ?, 1, 2, 3, 1, 1, ?, 'user',
  'freeze-canary-package', 'file.upsert', 'package.json',
  ?, ?, ?, '100644',
  'candidate-test', ?, ?, ?, ?,
  'candidate-test', ?, ?, ?, ?, ?, ?
)`,
		candidateID,
		fixture.ownerID,
		basePackageContentHash,
		intermediatePackage.ContentHash,
		intermediatePackage.ByteSize,
		snapshotID,
		"base-tree-"+snapshotID.String(),
		baseTreeContentHash,
		baseTree.TreeHash,
		candidateID,
		"candidate-tree-intermediate-"+candidateID.String(),
		intermediateTreeContentHash,
		intermediateTree.TreeHash,
		len(intermediateTree.Files),
		candidateTreeByteSize(intermediateTree),
	).Error; err != nil {
		t.Fatalf("append Candidate package journal: %v", err)
	}
	if err := database.Exec(`
INSERT INTO candidate_workspace_journal (
  candidate_id, sequence, candidate_version_from, candidate_version_to,
  session_epoch, writer_lease_epoch, actor_id, attribution,
  operation_id, operation_kind, path,
  content_hash, byte_size, file_mode,
  before_tree_store, before_tree_owner_id, before_tree_ref,
  before_tree_content_hash, before_tree_hash,
  after_tree_store, after_tree_owner_id, after_tree_ref,
  after_tree_content_hash, after_tree_hash, after_tree_file_count, after_tree_byte_size
) VALUES (
  ?, 2, 3, 4, 1, 1, ?, 'user',
  'freeze-canary-source', 'file.upsert', 'src/candidate.ts',
  ?, ?, '100644',
  'candidate-test', ?, ?, ?, ?,
  'candidate-test', ?, ?, ?, ?, ?, ?
)`,
		candidateID,
		fixture.ownerID,
		sourceFile.ContentHash,
		sourceFile.ByteSize,
		candidateID,
		"candidate-tree-intermediate-"+candidateID.String(),
		intermediateTreeContentHash,
		intermediateTree.TreeHash,
		candidateID,
		"candidate-tree-"+candidateID.String(),
		candidateTreeContentHash,
		candidateTree.TreeHash,
		len(candidateTree.Files),
		candidateTreeByteSize(candidateTree),
	).Error; err != nil {
		t.Fatalf("append Candidate source journal: %v", err)
	}

	if err := database.Exec(`
INSERT INTO candidate_snapshots (
  id, schema_version, candidate_id, project_id,
  candidate_version, journal_sequence, session_epoch, writer_lease_epoch,
  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
  tree_file_count, tree_byte_size, reason, created_by
) VALUES (
  ?, 'candidate-snapshot/v1', ?, ?,
  4, 2, 1, 1,
  'candidate-test', ?, ?, ?, ?,
  ?, ?, 'freeze closure checkpoint', ?
)`,
		checkpointID,
		candidateID,
		fixture.projectID,
		candidateID,
		"candidate-tree-"+candidateID.String(),
		candidateTreeContentHash,
		candidateTree.TreeHash,
		len(candidateTree.Files),
		candidateTreeByteSize(candidateTree),
		fixture.ownerID,
	).Error; err != nil {
		t.Fatalf("seed exact CandidateSnapshot: %v", err)
	}

	disablePostgresTrigger(t, database, "sandbox_sessions", "sandbox_session_configuration_complete")
	if err := database.Exec(`
INSERT INTO sandbox_sessions (
  id, schema_version, project_id, actor_id,
  candidate_id, repository_snapshot_id,
  build_manifest_id, build_manifest_hash,
  build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  runner_image_digest,
  candidate_version, candidate_journal_sequence, candidate_session_epoch,
  candidate_writer_lease_epoch,
  candidate_tree_store, candidate_tree_owner_id, candidate_tree_ref,
  candidate_tree_content_hash, candidate_tree_hash,
  candidate_dirty, candidate_conflicted, candidate_stale, candidate_rebase_required,
  latest_checkpoint_id, state, version, session_epoch,
  cpu_millis, memory_bytes, workspace_bytes, pid_limit, preview_port_limit,
  idle_hibernate_seconds, max_runtime_seconds, idle_deadline, expires_at,
  created_at, updated_at
) VALUES (
  ?, 'sandbox-session/v1', ?, ?,
  ?, ?, ?, ?, ?, ?, ?, ?,
  ?, ?, ?, ?,
  4, 2, 1, 1,
  'candidate-test', ?, ?, ?, ?,
  true, false, false, false,
  ?, 'provisioning', 1, 1,
  2000, 4294967296, 10737418240, 256, 3,
  1800, 28800, ?, ?, ?, ?
)`,
		sessionID,
		fixture.projectID,
		fixture.ownerID,
		candidateID,
		snapshotID,
		uuid.MustParse(fixture.rootA.ID),
		fixture.rootA.ManifestHash,
		lineage.BuildContractID,
		lineage.BuildContractHash,
		lineage.FullStackTemplateID,
		lineage.FullStackTemplateHash,
		baseWorkspaceArtifactID,
		baseWorkspaceRevisionID,
		baseWorkspaceContentHash,
		hashText("candidate-runner"),
		candidateID,
		"candidate-tree-"+candidateID.String(),
		candidateTreeContentHash,
		candidateTree.TreeHash,
		checkpointID,
		now.Add(30*time.Minute),
		now.Add(8*time.Hour),
		now.Add(-30*time.Second),
		now.Add(-30*time.Second),
	).Error; err != nil {
		t.Fatalf("seed ready SandboxSession: %v", err)
	}
	enablePostgresTrigger(t, database, "sandbox_sessions", "sandbox_session_configuration_complete")
	var lifecycle struct {
		SessionVersion uint64
		SessionState   string
	}
	if err := database.Raw(
		"SELECT session_version, session_state FROM transition_sandbox_session(?, 1, 1, 'starting', ?, 'provisioned', ?)",
		sessionID,
		fixture.ownerID,
		checkpointID,
	).Scan(&lifecycle).Error; err != nil {
		t.Fatalf("start SandboxSession: %v", err)
	}
	if lifecycle.SessionVersion != 2 || lifecycle.SessionState != "starting" {
		t.Fatalf("unexpected starting SandboxSession: %#v", lifecycle)
	}
	if err := database.Raw(
		"SELECT session_version, session_state FROM transition_sandbox_session(?, 2, 1, 'ready', ?, 'runtime ready', ?)",
		sessionID,
		fixture.ownerID,
		checkpointID,
	).Scan(&lifecycle).Error; err != nil {
		t.Fatalf("mark SandboxSession ready: %v", err)
	}
	if lifecycle.SessionVersion != 3 || lifecycle.SessionState != "ready" {
		t.Fatalf("unexpected ready SandboxSession: %#v", lifecycle)
	}
	return lineage
}

func seedCandidateFreezeVerificationReceipt(
	t *testing.T,
	database *gorm.DB,
	fixture multiBundlePostgresFixture,
	acceptanceSource VersionRef,
	lineage candidateFreezePostgresLineage,
	sessionID uuid.UUID,
	candidateID uuid.UUID,
	checkpointID uuid.UUID,
	candidateTree repository.TreeManifest,
	treePointer repository.TreeBlobPointer,
	now time.Time,
) repository.ExactReference {
	t.Helper()
	canonicalSHA := func(value string) string {
		if strings.HasPrefix(value, "sha256:") {
			return value
		}
		return "sha256:" + value
	}
	mustJSON := func(value any) string {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(payload)
	}

	profileID := "candidate-freeze-full-stack-v1"
	profileHash := hashText("candidate-freeze-verification-profile")
	planID, runID, attemptID, receiptID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	planHash := hashText("candidate-freeze-plan:" + checkpointID.String())
	receiptHash := hashText("candidate-freeze-receipt:" + checkpointID.String())
	runtimePolicyHash := hashText("candidate-freeze-runtime-policy")
	apiReleaseID, webReleaseID := uuid.New(), uuid.New()
	apiReleaseHash := admitCoreTemplateRelease(
		t, database, apiReleaseID, "candidate-freeze-api", "api", fixture.ownerID, fixture.viewerID,
	)
	webReleaseHash := admitCoreTemplateRelease(
		t, database, webReleaseID, "candidate-freeze-web", "web", fixture.ownerID, fixture.viewerID,
	)
	manifestHash := canonicalSHA(fixture.rootA.ManifestHash)
	contractHash := canonicalSHA(lineage.BuildContractHash)
	verifierImage := "registry.example/quality@" + hashText("candidate-freeze-verifier")
	checkStartedAt := now.UTC().Truncate(time.Microsecond)

	err := database.Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Exec(`ALTER TABLE full_stack_template_components DISABLE TRIGGER full_stack_template_component_guard`).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO full_stack_template_components (
  full_stack_template_id, full_stack_content_hash, role, mount_path,
  template_release_id, template_release_content_hash
) VALUES
  (?, ?, 'api', 'services/api', ?, ?),
  (?, ?, 'web', 'frontend', ?, ?)
`, lineage.FullStackTemplateID, lineage.FullStackTemplateHash, apiReleaseID, apiReleaseHash,
			lineage.FullStackTemplateID, lineage.FullStackTemplateHash, webReleaseID, webReleaseHash).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`ALTER TABLE full_stack_template_components ENABLE TRIGGER full_stack_template_component_guard`).Error; err != nil {
			return err
		}

		if err := transaction.Exec(`SET LOCAL session_replication_role = replica`).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO application_build_contract_template_releases (
  contract_id, ordinal, role, template_release_id, template_release_content_hash
) VALUES (?, 0, 'api', ?, ?), (?, 1, 'web', ?, ?)
`, lineage.BuildContractID, apiReleaseID, apiReleaseHash,
			lineage.BuildContractID, webReleaseID, webReleaseHash).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO application_build_contract_obligations (
  contract_id, obligation_id, level, kind,
  source_artifact_id, source_revision_id, source_content_hash, source_anchor_id,
  oracle_ids, depends_on, waivable, status
) VALUES (
  ?, 'OBL-FREEZE', 'must', 'acceptance',
  ?, ?, ?, 'AC-FREEZE',
  '["oracle-freeze"]'::jsonb, '[]'::jsonb, false, 'ready'
)
`, lineage.BuildContractID, uuid.MustParse(acceptanceSource.ArtifactID),
			uuid.MustParse(acceptanceSource.RevisionID), acceptanceSource.ContentHash).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`SET LOCAL session_replication_role = origin`).Error; err != nil {
			return err
		}

		profileDocument := mustJSON(map[string]any{
			"schemaVersion":          "verification-profile/v1",
			"id":                     profileID,
			"version":                1,
			"profileHash":            profileHash,
			"supportedTemplateRoles": []string{"api", "web"},
			"verifierImages": []map[string]any{{
				"role": "full-stack", "image": verifierImage,
			}},
			"commandImageRoles": map[string]any{"api": "full-stack", "web": "full-stack"},
			"builtInChecks":     []any{},
			"limits":            map[string]any{},
			"networkPolicy":     map[string]any{},
			"hiddenTestBundle":  nil,
			"state":             "active",
		})
		if err := transaction.Exec(`
INSERT INTO verification_profile_versions (
  profile_id, version, schema_version, document, content_hash, created_by
) VALUES (?, 1, 'verification-profile/v1', ?::jsonb, ?, ?)
`, profileID, profileDocument, profileHash, fixture.ownerID).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO verification_profile_policies (
  profile_id, profile_version, profile_hash, state, policy_version, reason, updated_by
) VALUES (?, 1, ?, 'active', 1, 'qualified Candidate freeze fixture', ?)
`, profileID, profileHash, fixture.ownerID).Error; err != nil {
			return err
		}

		templateReleases := mustJSON([]map[string]any{
			{"role": "api", "id": apiReleaseID.String(), "contentHash": apiReleaseHash},
			{"role": "web", "id": webReleaseID.String(), "contentHash": webReleaseHash},
		})
		obligations := mustJSON([]map[string]any{{
			"id": "OBL-FREEZE", "level": "must", "status": "ready",
			"oracleIds": []string{"oracle-freeze"},
		}})
		if err := transaction.Exec(`
INSERT INTO candidate_verification_plans (
  id, schema_version, scope, project_id,
  sandbox_session_id, session_version, candidate_id, candidate_snapshot_id,
  candidate_version, journal_sequence, session_epoch, writer_lease_epoch,
  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  verification_profile_id, verification_profile_version, verification_profile_hash,
  template_releases, obligations, check_ids, required_check_ids,
  check_count, obligation_count, runtime_policy_hash,
  content_store, content_ref, content_hash, plan_hash, created_by
) VALUES (
  ?, 'candidate-verification-plan/v1', 'candidate', ?,
  ?, 3, ?, ?, 4, 2, 1, 1,
  ?, ?, ?, ?, ?,
  ?, ?, ?, ?, ?, ?,
  ?, 1, ?, ?::jsonb, ?::jsonb,
  '["oracle-freeze"]'::jsonb, '["oracle-freeze"]'::jsonb,
  1, 1, ?, 'candidate-test', ?, ?, ?, ?
)
`, planID, fixture.projectID, sessionID, candidateID, checkpointID,
			treePointer.Store, candidateID, treePointer.Ref, treePointer.ContentObjectHash, candidateTree.TreeHash,
			uuid.MustParse(fixture.rootA.ID), manifestHash,
			lineage.BuildContractID, contractHash,
			lineage.FullStackTemplateID, lineage.FullStackTemplateHash,
			profileID, profileHash, templateReleases, obligations,
			runtimePolicyHash, "candidate-freeze-plan-"+planID.String(), planHash, planHash,
			fixture.ownerID).Error; err != nil {
			return err
		}

		if err := transaction.Exec(`
INSERT INTO candidate_verification_runs (
  id, schema_version, project_id, plan_id, plan_hash,
  request_key, request_hash, reason, state, version, fence_epoch,
  created_by, updated_by
) VALUES (
  ?, 'candidate-verification-run/v1', ?, ?, ?,
  ?, ?, 'verify exact Candidate before freeze', 'queued', 1, 0, ?, ?
)
`, runID, fixture.projectID, planID, planHash, "verify-"+candidateID.String(),
			hashText("candidate-freeze-run-request"), fixture.ownerID, fixture.ownerID).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
UPDATE candidate_verification_runs
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = 'quality-worker-freeze', lease_epoch = 1,
    lease_expires_at = statement_timestamp() + interval '5 minutes',
    started_at = statement_timestamp(), updated_by = ?
WHERE id = ?
`, fixture.ownerID, runID).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO candidate_verification_attempts (
  id, schema_version, run_id, project_id, plan_id, plan_hash,
  ordinal, state, version, fence_epoch, created_by, updated_by
) VALUES (
  ?, 'candidate-verification-attempt/v1', ?, ?, ?, ?,
  1, 'queued', 1, 0, ?, ?
)
`, attemptID, runID, fixture.projectID, planID, planHash, fixture.ownerID, fixture.ownerID).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
UPDATE candidate_verification_attempts
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = 'quality-worker-freeze', lease_epoch = 1,
    lease_expires_at = statement_timestamp() + interval '4 minutes',
    started_at = statement_timestamp(), updated_by = ?
WHERE id = ?
`, fixture.ownerID, attemptID).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO verification_execution_cleanups (
  scope, project_id, run_id, attempt_id, attempt_fence_epoch,
  state, created_by, updated_by
) VALUES ('candidate', ?, ?, ?, 1, 'registered', ?, ?)
`, fixture.projectID, runID, attemptID, fixture.ownerID, fixture.ownerID).Error; err != nil {
			return err
		}
		// This fixture builds the complete immutable receipt in one outer transaction.
		// Evaluate the deferred claim-registration gates while the exact Run and Attempt
		// are still claimed; later statements advance the same generation to terminal.
		if err := transaction.Exec(`
SET CONSTRAINTS candidate_verification_run_claim_cleanup_guard,
                candidate_verification_attempt_claim_cleanup_guard IMMEDIATE
`).Error; err != nil {
			return err
		}
		for version, state := range []string{"materializing", "preparing", "running", "collecting"} {
			if err := transaction.Exec(`
UPDATE candidate_verification_runs SET state = ?, version = ?, updated_by = ? WHERE id = ?
`, state, version+3, fixture.ownerID, runID).Error; err != nil {
				return err
			}
			if err := transaction.Exec(`
UPDATE candidate_verification_attempts SET state = ?, version = ?, updated_by = ? WHERE id = ?
`, state, version+3, fixture.ownerID, attemptID).Error; err != nil {
				return err
			}
		}
		if err := transaction.Exec(`
UPDATE verification_execution_cleanups
SET state = 'completed', version = 2,
    completed_at = statement_timestamp(), updated_by = ?
WHERE scope = 'candidate' AND attempt_id = ? AND attempt_fence_epoch = 1
`, fixture.ownerID, attemptID).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
UPDATE candidate_verification_attempts
SET state = 'passed', version = 7, lease_expires_at = NULL,
    finished_at = statement_timestamp(), updated_by = ?
WHERE id = ?
`, fixture.ownerID, attemptID).Error; err != nil {
			return err
		}

		if err := transaction.Exec(`
INSERT INTO candidate_verification_receipts (
  id, schema_version, scope, run_id, project_id, plan_id, plan_hash,
  sandbox_session_id, candidate_id, candidate_snapshot_id,
  candidate_version, journal_sequence, session_epoch, writer_lease_epoch, tree_hash,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  verification_profile_id, verification_profile_version, verification_profile_hash,
  attempt_ids, check_count, coverage_count, must_count, must_passed_count,
  blocker_count, warning_count, decision,
  content_store, content_ref, content_hash, payload_hash, created_by, created_at
) VALUES (
  ?, 'verification-receipt/v1', 'candidate', ?, ?, ?, ?,
  ?, ?, ?, 4, 2, 1, 1, ?,
  ?, ?, ?, ?, ?, ?, ?, 1, ?,
  ?::jsonb, 1, 1, 1, 1, 0, 0, 'passed',
  'candidate-test', ?, ?, ?, ?, ?
)
`, receiptID, runID, fixture.projectID, planID, planHash,
			sessionID, candidateID, checkpointID, candidateTree.TreeHash,
			uuid.MustParse(fixture.rootA.ID), manifestHash,
			lineage.BuildContractID, contractHash,
			lineage.FullStackTemplateID, lineage.FullStackTemplateHash,
			profileID, profileHash, mustJSON([]string{attemptID.String()}),
			"candidate-freeze-receipt-"+receiptID.String(), receiptHash, receiptHash,
			fixture.ownerID, checkStartedAt.Add(2*time.Second)).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO candidate_verification_checks (
  receipt_id, run_id, ordinal, check_id, kind, service_id, command_id,
  required, status, attempt_id, verifier_image_digest, argv, working_directory,
  exit_code, started_at, completed_at, duration_ms, attempt_count,
  truncated, redaction_count, oracle_ids, acceptance_criterion_ids,
  obligation_ids, diagnostics
) VALUES (
  ?, ?, 0, 'oracle-freeze', 'acceptance', 'api', 'verify',
  true, 'passed', ?, ?, '["verify"]'::jsonb, '.',
  0, ?, ?, 1000, 1, false, 0,
  '["oracle-freeze"]'::jsonb, '["AC-FREEZE"]'::jsonb,
  '["OBL-FREEZE"]'::jsonb, '[]'::jsonb
)
`, receiptID, runID, attemptID, verifierImage, checkStartedAt, checkStartedAt.Add(time.Second)).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO candidate_verification_obligation_coverage (
  receipt_id, ordinal, build_contract_id, obligation_id,
  level, oracle_ids, check_ids, status
) VALUES (
  ?, 0, ?, 'OBL-FREEZE', 'must',
  '["oracle-freeze"]'::jsonb, '["oracle-freeze"]'::jsonb, 'passed'
)
`, receiptID, lineage.BuildContractID).Error; err != nil {
			return err
		}
		return transaction.Exec(`
UPDATE candidate_verification_runs
SET state = 'passed', version = 7, lease_expires_at = NULL,
    finished_at = statement_timestamp(), updated_by = ?
WHERE id = ?
`, fixture.ownerID, runID).Error
	})
	if err != nil {
		t.Fatalf("seed exact Candidate VerificationReceipt: %v", err)
	}
	return repository.ExactReference{ID: receiptID.String(), ContentHash: receiptHash}
}

func disablePostgresTrigger(
	t *testing.T,
	database *gorm.DB,
	table string,
	trigger string,
) {
	t.Helper()
	if err := database.Exec("ALTER TABLE " + table + " DISABLE TRIGGER " + trigger).Error; err != nil {
		t.Fatalf("disable %s: %v", trigger, err)
	}
	t.Cleanup(func() {
		_ = database.Exec("ALTER TABLE " + table + " ENABLE TRIGGER " + trigger).Error
	})
}

func enablePostgresTrigger(
	t *testing.T,
	database *gorm.DB,
	table string,
	trigger string,
) {
	t.Helper()
	if err := database.Exec("ALTER TABLE " + table + " ENABLE TRIGGER " + trigger).Error; err != nil {
		t.Fatalf("enable %s: %v", trigger, err)
	}
}

func candidateTreeByteSize(tree repository.TreeManifest) int64 {
	var size int64
	for _, file := range tree.Files {
		size += file.ByteSize
	}
	return size
}

func requireCandidateTreeFile(
	t *testing.T,
	tree repository.TreeManifest,
	path string,
) repository.TreeFile {
	t.Helper()
	for _, file := range tree.Files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("tree %s does not contain %s", tree.TreeHash, path)
	return repository.TreeFile{}
}
