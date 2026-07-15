package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type proposalApplyLineagePrecheckContextKey struct{}

type proposalApplyLineageLocksContextKey struct{}

func TestProposalApplyLocksExactSourceThroughCommitPostgres(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, artifacts, projectID, ownerID := newArtifactLineageFixture(t, database)
	source := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "reference_source", "approved", "current",
		json.RawMessage(`{"title":"Frozen proposal evidence"}`),
	)
	created, err := artifacts.Create(ctx, projectID.String(), ownerID.String(), CreateArtifactInput{
		Kind: "product_requirements", Title: "Proposal source lock", Content: json.RawMessage(`{
			"summary":"Before",
			"blocks":[{
				"id":"requirement-1","type":"requirement","requirementId":"REQ-1","priority":"must",
				"text":"Freeze exact Proposal sources through commit.",
				"acceptanceCriteria":[{"id":"AC-1","statement":"Source state cannot change during Apply."}]
			}]
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	base, err := artifacts.CreateRevision(
		ctx, created.Artifact.ID, ownerID.String(), created.Draft.ETag,
		CreateRevisionInput{ChangeSummary: "Initialize Proposal source lock"},
	)
	if err != nil {
		t.Fatal(err)
	}
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := NewProposalService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	baseRef := VersionRef{ArtifactID: base.ArtifactID, RevisionID: base.ID, ContentHash: base.ContentHash}
	manifest, err := proposals.CreateManifest(ctx, projectID.String(), ownerID.String(), CreateManifestInput{
		JobType: "derive_requirements", BaseRevision: &baseRef,
		Sources:     []ManifestSourceInput{{Ref: source, Purpose: "evidence"}},
		Constraints: json.RawMessage(`{}`), OutputSchemaVersion: "requirements-proposal/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := proposals.CreateProposal(ctx, projectID.String(), ownerID.String(), CreateProposalInput{
		ManifestID: manifest.ID, ArtifactID: created.Artifact.ID,
		Operations: []domain.ProposalOperation{{
			ID: "replace-summary", Kind: domain.OperationReplace, Path: "/summary", Value: json.RawMessage(`"After"`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err = proposals.Decide(ctx, proposal.ID, ownerID.String(), DecideProposalInput{
		OperationID: "replace-summary", Decision: domain.DecisionAccepted, Version: proposal.Version,
	})
	if err != nil {
		t.Fatal(err)
	}

	marker := &struct{}{}
	lineageLocked := make(chan struct{})
	releaseApply := make(chan struct{})
	var pauseOnce sync.Once
	callbackName := "test:pause_after_proposal_lineage_locks_" + uuid.NewString()
	if err := database.Callback().Query().After("gorm:query").Register(callbackName, func(query *gorm.DB) {
		if query.Statement.Context.Value(proposalApplyLineageLocksContextKey{}) != marker ||
			query.Statement.Table != (storage.ArtifactHealthModel{}).TableName() || query.Error != nil {
			return
		}
		if _, locked := query.Statement.Clauses["FOR"]; !locked {
			return
		}
		pauseOnce.Do(func() {
			close(lineageLocked)
			<-releaseApply
		})
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = database.Callback().Query().Remove(callbackName)
	}()
	released := false
	defer func() {
		if !released {
			close(releaseApply)
		}
	}()

	type applyResult struct {
		draft ArtifactDraft
		err   error
	}
	result := make(chan applyResult, 1)
	applyContext := context.WithValue(ctx, proposalApplyLineageLocksContextKey{}, marker)
	go func() {
		draft, applyErr := proposals.Apply(
			applyContext, proposal.ID, ownerID.String(), ApplyProposalInput{Version: proposal.Version},
		)
		result <- applyResult{draft: draft, err: applyErr}
	}()

	select {
	case <-lineageLocked:
	case <-time.After(5 * time.Second):
		t.Fatal("Proposal Apply did not reach the locked lineage checkpoint")
	}

	sourceArtifactID := uuid.MustParse(source.ArtifactID)
	sourceRevisionID := uuid.MustParse(source.RevisionID)
	targetArtifactID := uuid.MustParse(created.Artifact.ID)
	assertProposalApplyLineageRowLocked(t, database,
		`UPDATE artifacts SET updated_at = updated_at WHERE id = ?`, sourceArtifactID, "source artifact")
	assertProposalApplyLineageRowLocked(t, database,
		`UPDATE artifact_revisions SET change_summary = change_summary WHERE id = ?`, sourceRevisionID, "source revision")
	assertProposalApplyLineageRowLocked(t, database,
		`UPDATE artifact_health SET computed_at = computed_at WHERE artifact_id = ?`, sourceArtifactID, "source health")
	assertProposalApplyLineageRowLocked(t, database,
		`UPDATE artifacts SET updated_at = updated_at WHERE id = ?`, targetArtifactID, "target artifact")

	close(releaseApply)
	released = true
	select {
	case outcome := <-result:
		if outcome.err != nil {
			t.Fatalf("Apply after releasing exact source locks: %v", outcome.err)
		}
		if len(outcome.draft.Content) == 0 {
			t.Fatal("successful Apply returned an empty draft")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Proposal Apply did not complete after releasing lineage locks")
	}

	if err := database.Exec(
		`UPDATE artifact_health SET sync_status = 'needs_sync' WHERE artifact_id = ?`, sourceArtifactID,
	).Error; err != nil {
		t.Fatalf("Proposal source health remained locked after Apply commit: %v", err)
	}
}

func TestProposalApplyRevalidatesLineageAfterConcurrentSourceApprovalPostgres(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, artifacts, projectID, ownerID := newArtifactLineageFixture(t, database)
	pageSpecPayload := json.RawMessage(`{
		"blueprintPageNodeId":"page-orders","title":"Orders","route":"/orders","userGoal":"Review orders",
		"acceptanceCriterionIds":["AC-1"],"requiredRoles":["orders-reader"],
		"states":[
			{"id":"state-ready","key":"ready","title":"Ready","required":true,"fixtureIds":["fixture-ready"]},
			{"id":"state-loading","key":"loading","title":"Loading","required":true,"fixtureIds":[]},
			{"id":"state-empty","key":"empty","title":"Empty","required":true,"fixtureIds":[]},
			{"id":"state-error","key":"error","title":"Error","required":true,"fixtureIds":[]}
		],
		"interactions":[{"id":"interaction-open","trigger":"click","outcome":"Open details"}],
		"dataBindings":[{"id":"binding-orders","name":"Orders","source":"api","operationId":"api-orders","required":true}]
	}`)
	pageSpecRef := seedArtifactSemanticPageSpecRevision(
		t, database, store, projectID, ownerID, "approved", "current", pageSpecPayload,
	)
	pageSpecSource := ArtifactSourceInput{Ref: pageSpecRef, Purpose: "page_spec", Required: true}
	prototypePayload := prototypeLineageCoveragePayload(t, pageSpecRef)
	created, err := artifacts.Create(ctx, projectID.String(), ownerID.String(), CreateArtifactInput{
		Kind: "prototype", Title: "Lineage race Prototype", Content: prototypePayload,
		SourceVersions: []ArtifactSourceInput{pageSpecSource},
	})
	if err != nil {
		t.Fatal(err)
	}
	base, err := artifacts.CreateRevision(
		ctx, created.Artifact.ID, ownerID.String(), created.Draft.ETag,
		CreateRevisionInput{ChangeSummary: "Initialize lineage race target", ChangeSource: "system"},
	)
	if err != nil {
		t.Fatal(err)
	}
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := NewProposalService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	baseRef := VersionRef{ArtifactID: base.ArtifactID, RevisionID: base.ID, ContentHash: base.ContentHash}
	manifest, err := proposals.CreateManifest(ctx, projectID.String(), ownerID.String(), CreateManifestInput{
		JobType: "generate_prototype", BaseRevision: &baseRef,
		Sources:     []ManifestSourceInput{{Ref: pageSpecRef, Purpose: "page_spec"}},
		Constraints: json.RawMessage(`{}`), OutputSchemaVersion: "prototype-proposal/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := proposals.CreateProposal(ctx, projectID.String(), ownerID.String(), CreateProposalInput{
		ManifestID: manifest.ID, ArtifactID: created.Artifact.ID,
		Operations: []domain.ProposalOperation{{
			ID: "replace-prototype", Kind: domain.OperationReplace, Value: prototypePayload,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err = proposals.Decide(ctx, proposal.ID, ownerID.String(), DecideProposalInput{
		OperationID: "replace-prototype", Decision: domain.DecisionAccepted, Version: proposal.Version,
	})
	if err != nil {
		t.Fatal(err)
	}

	pageSpecArtifactID := uuid.MustParse(pageSpecRef.ArtifactID)
	oldPageSpecRevisionID := uuid.MustParse(pageSpecRef.RevisionID)
	newPageSpecRevisionID := uuid.New()
	newPageSpecPayload := append(json.RawMessage(nil), pageSpecPayload...)
	newPageSpecPayload = append(newPageSpecPayload, '\n')
	newPageSpecContent := store.addFinalized("lineage-race-page-spec-"+newPageSpecRevisionID.String(), newPageSpecPayload)
	now := time.Now().UTC()
	if err := database.Create(&storage.ArtifactRevisionModel{
		ID: newPageSpecRevisionID, ArtifactID: pageSpecArtifactID, RevisionNumber: 2, ParentRevisionID: &oldPageSpecRevisionID,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: newPageSpecContent.ID,
		ContentHash: newPageSpecContent.ContentHash, ByteSize: newPageSpecContent.ByteSize,
		WorkflowStatus: "in_review", ChangeSource: "human", ChangeSummary: "Concurrent PageSpec approval",
		CreatedBy: ownerID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", pageSpecArtifactID).
		Updates(map[string]any{"latest_revision_id": newPageSpecRevisionID, "updated_at": now}).Error; err != nil {
		t.Fatal(err)
	}

	marker := &struct{}{}
	precheckComplete := make(chan struct{})
	releaseApply := make(chan struct{})
	var pauseOnce sync.Once
	callbackName := "test:pause_after_proposal_lineage_precheck_" + uuid.NewString()
	if err := database.Callback().Query().After("gorm:query").Register(callbackName, func(query *gorm.DB) {
		if query.Statement.Context.Value(proposalApplyLineagePrecheckContextKey{}) != marker ||
			query.Statement.Table != (storage.ArtifactHealthModel{}).TableName() || query.Error != nil {
			return
		}
		if _, locked := query.Statement.Clauses["FOR"]; locked {
			return
		}
		pauseOnce.Do(func() {
			close(precheckComplete)
			<-releaseApply
		})
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = database.Callback().Query().Remove(callbackName)
	}()
	released := false
	defer func() {
		if !released {
			close(releaseApply)
		}
	}()

	type applyResult struct {
		draft ArtifactDraft
		err   error
	}
	result := make(chan applyResult, 1)
	applyContext := context.WithValue(ctx, proposalApplyLineagePrecheckContextKey{}, marker)
	go func() {
		draft, applyErr := proposals.Apply(
			applyContext, proposal.ID, ownerID.String(), ApplyProposalInput{Version: proposal.Version},
		)
		result <- applyResult{draft: draft, err: applyErr}
	}()

	select {
	case <-precheckComplete:
	case <-time.After(5 * time.Second):
		t.Fatal("Proposal Apply did not reach the completed external lineage precheck")
	}

	approvedAt := time.Now().UTC()
	if err := database.Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", oldPageSpecRevisionID).
			Updates(map[string]any{"workflow_status": "superseded", "superseded_at": approvedAt}).Error; err != nil {
			return err
		}
		if err := transaction.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", newPageSpecRevisionID).
			Updates(map[string]any{"workflow_status": "approved", "approved_at": approvedAt}).Error; err != nil {
			return err
		}
		return transaction.Model(&storage.ArtifactModel{}).Where("id = ?", pageSpecArtifactID).
			Updates(map[string]any{
				"latest_approved_revision_id": newPageSpecRevisionID,
				"version":                     gorm.Expr("version + 1"),
				"updated_at":                  approvedAt,
			}).Error
	}); err != nil {
		t.Fatal(err)
	}
	close(releaseApply)
	released = true

	select {
	case outcome := <-result:
		if !errors.Is(outcome.err, ErrBlockingGate) {
			t.Fatalf("Proposal Apply committed after its exact PageSpec source was superseded: draft=%+v err=%v", outcome.draft, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Proposal Apply did not finish after releasing the external precheck")
	}

	var storedProposal storage.OutputProposalModel
	if err := database.Where("id = ?", uuid.MustParse(proposal.ID)).Take(&storedProposal).Error; err != nil {
		t.Fatal(err)
	}
	if storedProposal.Status == "applied" || storedProposal.Status == "partially_applied" || storedProposal.AppliedAt != nil {
		t.Fatalf("failed lineage revalidation mutated Proposal application state: %+v", storedProposal)
	}
	var target storage.ArtifactModel
	if err := database.Where("id = ?", uuid.MustParse(created.Artifact.ID)).Take(&target).Error; err != nil {
		t.Fatal(err)
	}
	if target.LatestDraftID == nil {
		t.Fatal("failed lineage revalidation removed the target draft")
	}
	var targetDraft storage.ArtifactDraftModel
	if err := database.Where("id = ?", *target.LatestDraftID).Take(&targetDraft).Error; err != nil {
		t.Fatal(err)
	}
	if targetDraft.ContentHash != base.ContentHash {
		t.Fatalf("failed lineage revalidation changed the target draft: got %s want %s", targetDraft.ContentHash, base.ContentHash)
	}
}

func assertProposalApplyLineageRowLocked(
	t *testing.T,
	database *gorm.DB,
	statement string,
	id uuid.UUID,
	label string,
) {
	t.Helper()
	err := database.Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Exec(`SET LOCAL lock_timeout = '250ms'`).Error; err != nil {
			return err
		}
		return transaction.Exec(statement, id).Error
	})
	if err == nil {
		t.Fatalf("%s was writable while Proposal Apply was open", label)
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "55p03") && !strings.Contains(message, "lock timeout") {
		t.Fatalf("%s update failed for an unexpected reason: %v", label, err)
	}
}
