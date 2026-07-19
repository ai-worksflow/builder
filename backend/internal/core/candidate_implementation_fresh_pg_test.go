package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

func TestFreshCandidateFreezeReviewApplyCreatesFirstWorkspaceRevisionPostgres(t *testing.T) {
	database, cleanup := multiBundlePostgresDatabase(t)
	defer cleanup()
	fixture := seedMultiBundlePostgresFixture(t, database)
	fixture.implementation.now = func() time.Time { return time.Now().UTC() }

	freshProjectID := seedMultiBundleProject(t, database, fixture.ownerID, "fresh-candidate")
	seedMultiBundleMembership(t, database, freshProjectID, fixture.ownerID, RoleOwner)
	pageSpec := seedMultiBundleApprovedRevision(
		t, database, fixture.contents, freshProjectID, fixture.ownerID,
		"page_spec", json.RawMessage(`{"pages":[{"id":"fresh-home"}]}`), nil,
	)
	prototype := seedMultiBundleApprovedRevision(
		t, database, fixture.contents, freshProjectID, fixture.ownerID,
		"prototype", json.RawMessage(`{"frames":[{"id":"fresh-home"}]}`), nil,
	)
	requirements := seedMultiBundleApprovedRevision(
		t, database, fixture.contents, freshProjectID, fixture.ownerID,
		"product_requirements", json.RawMessage(`{"requirements":[{"id":"AC-FREEZE"}]}`), nil,
	)
	blueprint := seedMultiBundleApprovedRevision(
		t, database, fixture.contents, freshProjectID, fixture.ownerID,
		"blueprint", json.RawMessage(`{"nodes":[{"id":"fresh-home","kind":"Page"}]}`), nil,
	)
	freshRoot := seedFreshCandidateRoot(
		t, database, fixture.contents, freshProjectID, fixture.ownerID,
		time.Now().UTC().Add(-time.Hour), pageSpec, prototype, requirements, blueprint,
	)
	seedMultiBundleBuildContracts(
		t, database, fixture.ownerID, freshProjectID, []WorkbenchBundle{freshRoot},
	)
	freshFixture := fixture
	freshFixture.projectID = freshProjectID
	freshFixture.rootA = freshRoot
	freshFixture.workspaceW0 = VersionRef{}

	assertFreshProjectHasNoWorkspace(t, database, freshProjectID, 0, 0)
	loadedRoot, err := freshFixture.workbench.GetBundleForGeneration(
		context.Background(), freshRoot.ID, fixture.ownerID.String(),
	)
	if err != nil {
		t.Fatalf("load fresh BuildManifest: %v", err)
	}
	if loadedRoot.CurrentWorkspaceRevision != nil {
		t.Fatalf("fresh BuildManifest invented current Workspace revision: %#v", loadedRoot.CurrentWorkspaceRevision)
	}
	var persistedManifest struct {
		WorkspaceRevisionID *uuid.UUID
	}
	if err := database.Model(&storage.ApplicationBuildManifestModel{}).
		Select("workspace_revision_id").
		Where("id = ?", freshRoot.ID).
		Scan(&persistedManifest).Error; err != nil {
		t.Fatal(err)
	}
	if persistedManifest.WorkspaceRevisionID != nil {
		t.Fatalf("fresh BuildManifest row invented workspace_revision_id %s", persistedManifest.WorkspaceRevisionID)
	}

	const (
		packageFresh = `{"name":"fresh-candidate","private":true,"dependencies":{}}`
		sourceFresh  = "export const freshCandidate = 'exact'\n"
	)
	emptyTree, err := repository.NewTree([]repository.TreeFile{})
	if err != nil {
		t.Fatal(err)
	}
	intermediateTree, err := repository.NewTree([]repository.TreeFile{
		candidateTreeFile("package.json", "100644", []byte(packageFresh)),
	})
	if err != nil {
		t.Fatal(err)
	}
	candidateTree, err := repository.NewTree([]repository.TreeFile{
		candidateTreeFile("package.json", "100644", []byte(packageFresh)),
		candidateTreeFile("src/candidate.ts", "100644", []byte(sourceFresh)),
	})
	if err != nil {
		t.Fatal(err)
	}

	candidateID := uuid.New()
	repositorySnapshotID := uuid.New()
	checkpointID := uuid.New()
	sessionID := uuid.New()
	now := time.Now().UTC()
	lineage := seedCandidateFreezePostgresRows(
		t, database, freshFixture,
		repositorySnapshotID, candidateID, checkpointID, sessionID,
		nil, emptyTree, intermediateTree, candidateTree, now,
	)
	candidate := repository.CandidateWorkspace{
		SchemaVersion:        repository.CandidateSchemaVersion,
		ID:                   candidateID.String(),
		ProjectID:            freshProjectID.String(),
		RepositorySnapshotID: repositorySnapshotID.String(),
		BuildManifest: repository.ExactReference{
			ID: freshRoot.ID, ContentHash: freshRoot.ManifestHash,
		},
		BuildContract: repository.ExactReference{
			ID: lineage.BuildContractID.String(), ContentHash: lineage.BuildContractHash,
		},
		FullStackTemplate: repository.ExactReference{
			ID: lineage.FullStackTemplateID.String(), ContentHash: lineage.FullStackTemplateHash,
		},
		BaseWorkspaceRevision: nil,
		BaseTreeHash:          emptyTree.TreeHash,
		CurrentTree:           candidateTree,
		Status:                repository.CandidateActive,
		Dirty:                 true,
		SessionEpoch:          1,
		Version:               4,
		JournalSequence:       2,
		WriterLeaseEpoch:      1,
		Lease: &repository.WriterLease{
			OwnerID: fixture.ownerID.String(), Epoch: 1, ExpiresAt: now.Add(20 * time.Minute),
		},
		CreatedBy: fixture.ownerID.String(),
		CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now.Add(-time.Minute),
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
		t, database, freshFixture, requirements, lineage,
		sessionID, candidateID, checkpointID, candidateTree, treePointer, now,
	)
	assertFreshCandidateAuthority(
		t, database, freshProjectID, repositorySnapshotID, candidateID,
		checkpointID, sessionID, verificationReceipt, emptyTree.TreeHash, candidateTree.TreeHash,
	)

	files := candidateImplementationFilesFake{values: map[string][]byte{
		hashBytes([]byte(packageFresh)): []byte(packageFresh),
		hashBytes([]byte(sourceFresh)):  []byte(sourceFresh),
	}}
	request := CandidateImplementationRequest{
		RequestKey:               "freeze-fresh-" + candidateID.String(),
		SessionID:                sessionID.String(),
		CandidateID:              candidateID.String(),
		CheckpointID:             checkpointID.String(),
		VerificationReceipt:      verificationReceipt,
		Reason:                   "Freeze exact fresh Candidate into the first Workspace revision",
		ExpectedSessionVersion:   3,
		ExpectedSessionEpoch:     1,
		ExpectedCandidateVersion: 4,
		ExpectedWriterLeaseEpoch: 1,
	}
	result, err := freshFixture.implementation.CreateCandidateImplementation(
		context.Background(), freshProjectID.String(), fixture.ownerID.String(), request,
		repository.CandidateMutationRecord{Candidate: candidate, CurrentTreePointer: treePointer}, files,
	)
	if err != nil {
		t.Fatalf("freeze fresh Candidate: %v", err)
	}
	if result.Proposal.BaseWorkspaceRevision != nil || result.Receipt.BaseWorkspaceRevision != nil {
		t.Fatalf("fresh Candidate freeze invented a base Workspace revision: proposal=%#v receipt=%#v",
			result.Proposal.BaseWorkspaceRevision, result.Receipt.BaseWorkspaceRevision)
	}
	if result.Proposal.CandidateSource == nil ||
		result.Proposal.CandidateSource.BaseTreeHash != emptyTree.TreeHash ||
		result.Proposal.CandidateSource.TreeHash != candidateTree.TreeHash ||
		result.Proposal.CandidateSource.VerificationReceipt.ID != verificationReceipt.ID ||
		result.Proposal.CandidateSource.VerificationReceipt.ContentHash != verificationReceipt.ContentHash ||
		len(result.Proposal.Operations) != len(candidateTree.Files) {
		t.Fatalf("fresh Candidate Proposal lost exact authority: %#v", result)
	}
	assertFreshProjectHasNoWorkspace(t, database, freshProjectID, 0, 0)

	reviewed := result.Proposal
	for _, operation := range result.Proposal.Operations {
		reviewed, err = freshFixture.implementation.Decide(
			context.Background(), reviewed.ID, fixture.ownerID.String(),
			DecideImplementationInput{
				OperationID: operation.ID, Decision: ImplementationAccepted, Version: reviewed.Version,
			},
		)
		if err != nil {
			t.Fatalf("accept fresh Candidate operation %s: %v", operation.ID, err)
		}
	}
	if reviewed.Status != "ready" {
		t.Fatalf("fully accepted fresh Candidate Proposal status = %q", reviewed.Status)
	}

	revision, err := freshFixture.implementation.Apply(
		context.Background(), reviewed.ID, fixture.ownerID.String(),
		ApplyImplementationInput{Version: reviewed.Version},
	)
	if err != nil {
		t.Fatalf("apply fresh Candidate Proposal: %v", err)
	}
	if revision.RevisionNumber != 1 || revision.ParentRevisionID != nil ||
		revision.WorkflowStatus != "approved" || revision.ChangeSource != "ai_proposal" {
		t.Fatalf("first immutable Workspace revision has wrong identity: %#v", revision)
	}
	assertMultiBundleWorkspaceFile(t, revision.Content, "package.json", packageFresh)
	assertMultiBundleWorkspaceFile(t, revision.Content, "src/candidate.ts", sourceFresh)
	var workspace map[string]any
	if err := json.Unmarshal(revision.Content, &workspace); err != nil {
		t.Fatal(err)
	}
	appliedTree, err := candidateWorkspaceTree(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if appliedTree.TreeHash != candidateTree.TreeHash {
		t.Fatalf("first Workspace revision tree = %s, Candidate tree = %s", appliedTree.TreeHash, candidateTree.TreeHash)
	}
	assertFreshProjectHasNoWorkspace(t, database, freshProjectID, 1, 1)

	var persisted struct {
		ArtifactID               uuid.UUID
		ArtifactKey              string
		LatestApprovedRevisionID *uuid.UUID
		RevisionNumber           uint64
		ParentRevisionID         *uuid.UUID
		ImplementationProposalID *uuid.UUID
	}
	if err := database.Raw(`
SELECT artifact.id AS artifact_id, artifact.artifact_key,
       artifact.latest_approved_revision_id,
       revision.revision_number, revision.parent_revision_id,
       revision.implementation_proposal_id
FROM artifacts AS artifact
JOIN artifact_revisions AS revision ON revision.artifact_id = artifact.id
WHERE artifact.project_id = ? AND artifact.kind = 'workspace'
`, freshProjectID).Scan(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.ArtifactKey != "WORKSPACE-MAIN" ||
		persisted.LatestApprovedRevisionID == nil || *persisted.LatestApprovedRevisionID != uuid.MustParse(revision.ID) ||
		persisted.RevisionNumber != 1 || persisted.ParentRevisionID != nil ||
		persisted.ImplementationProposalID == nil || *persisted.ImplementationProposalID != uuid.MustParse(reviewed.ID) {
		t.Fatalf("persisted first Workspace projection is not exact: %#v", persisted)
	}
}

func seedFreshCandidateRoot(
	t *testing.T,
	database *gorm.DB,
	contents *multiBundleMemoryContentStore,
	projectID uuid.UUID,
	userID uuid.UUID,
	createdAt time.Time,
	pageSpec VersionRef,
	prototype VersionRef,
	requirements VersionRef,
	blueprint VersionRef,
) WorkbenchBundle {
	t.Helper()
	id := uuid.New()
	bundle := WorkbenchBundle{
		ID: id.String(), ProjectID: projectID.String(), RootBuildManifestID: id.String(),
		PageSpecRevision: pageSpec, PrototypeRevision: prototype,
		RequirementRevisions: []VersionRef{requirements}, BlueprintRevision: blueprint,
		CurrentWorkspaceRevision: nil,
		Assumptions:              []string{}, Waivers: []string{}, CreatedBy: userID.String(), CreatedAt: createdAt,
	}
	manifestHash, err := workbenchBundleHash(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundle.ManifestHash = manifestHash
	payload, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	stored := contents.addFinalized(projectID.String(), "application_build_manifest", id.String(), 1, payload)
	model := storage.ApplicationBuildManifestModel{
		ID: id, ProjectID: projectID, RootManifestID: id, WorkspaceRevisionID: nil,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: stored.ID, ContentHash: stored.ContentHash,
		ManifestHash: manifestHash, Status: "frozen", CreatedBy: userID, CreatedAt: createdAt,
	}
	if err := database.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	return bundle
}

func assertFreshProjectHasNoWorkspace(
	t *testing.T,
	database *gorm.DB,
	projectID uuid.UUID,
	wantArtifacts int64,
	wantRevisions int64,
) {
	t.Helper()
	var artifacts int64
	if err := database.Model(&storage.ArtifactModel{}).
		Where("project_id = ? AND kind = 'workspace'", projectID).
		Count(&artifacts).Error; err != nil {
		t.Fatal(err)
	}
	var revisions int64
	if err := database.Table("artifact_revisions AS revision").
		Joins("JOIN artifacts AS artifact ON artifact.id = revision.artifact_id").
		Where("artifact.project_id = ? AND artifact.kind = 'workspace'", projectID).
		Count(&revisions).Error; err != nil {
		t.Fatal(err)
	}
	if artifacts != wantArtifacts || revisions != wantRevisions {
		t.Fatalf("fresh project Workspace artifacts/revisions = %d/%d, want %d/%d",
			artifacts, revisions, wantArtifacts, wantRevisions)
	}
}

func assertFreshCandidateAuthority(
	t *testing.T,
	database *gorm.DB,
	projectID uuid.UUID,
	repositorySnapshotID uuid.UUID,
	candidateID uuid.UUID,
	checkpointID uuid.UUID,
	sessionID uuid.UUID,
	verificationReceipt repository.ExactReference,
	baseTreeHash string,
	candidateTreeHash string,
) {
	t.Helper()
	var exactCount int64
	if err := database.Raw(`
SELECT count(*)
FROM repository_snapshots AS repository_snapshot
JOIN candidate_workspaces AS candidate
  ON candidate.repository_snapshot_id = repository_snapshot.id
 AND candidate.project_id = repository_snapshot.project_id
JOIN candidate_snapshots AS checkpoint
  ON checkpoint.id = ?
 AND checkpoint.candidate_id = candidate.id
 AND checkpoint.project_id = candidate.project_id
JOIN sandbox_sessions AS session
  ON session.id = ?
 AND session.candidate_id = candidate.id
 AND session.repository_snapshot_id = repository_snapshot.id
JOIN candidate_verification_receipts AS receipt
  ON receipt.id = ?
 AND receipt.payload_hash = ?
 AND receipt.sandbox_session_id = session.id
 AND receipt.candidate_snapshot_id = checkpoint.id
WHERE repository_snapshot.id = ?
  AND repository_snapshot.project_id = ?
  AND repository_snapshot.base_workspace_artifact_id IS NULL
  AND repository_snapshot.base_workspace_revision_id IS NULL
  AND repository_snapshot.base_workspace_content_hash IS NULL
  AND repository_snapshot.tree_hash = ?
  AND candidate.id = ?
  AND candidate.base_workspace_artifact_id IS NULL
  AND candidate.base_workspace_revision_id IS NULL
  AND candidate.base_workspace_content_hash IS NULL
  AND candidate.base_tree_hash = ?
  AND candidate.current_tree_hash = ?
  AND checkpoint.tree_hash = ?
  AND session.base_workspace_artifact_id IS NULL
  AND session.base_workspace_revision_id IS NULL
  AND session.base_workspace_content_hash IS NULL
  AND session.candidate_tree_hash = ?
  AND receipt.decision = 'passed'
  AND receipt.blocker_count = 0
  AND receipt.tree_hash = ?
`, checkpointID, sessionID, uuid.MustParse(verificationReceipt.ID), verificationReceipt.ContentHash,
		repositorySnapshotID, projectID, baseTreeHash, candidateID,
		baseTreeHash, candidateTreeHash, candidateTreeHash, candidateTreeHash, candidateTreeHash,
	).Scan(&exactCount).Error; err != nil {
		t.Fatal(err)
	}
	if exactCount != 1 {
		t.Fatalf("fresh Candidate authority chain has %d exact rows, want 1", exactCount)
	}
}
