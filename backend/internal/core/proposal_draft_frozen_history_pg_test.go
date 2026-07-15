package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
)

func TestPrototypeProposalDiscardIgnoresAppliedProposalsFrozenInBaseHistoryPostgres(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, artifacts, projectID, ownerID := newArtifactLineageFixture(t, database)
	pageSpecRef := seedArtifactSemanticPageSpecRevision(
		t, database, store, projectID, ownerID, "approved", "current",
		json.RawMessage(`{
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
		}`),
	)
	created, err := artifacts.Create(ctx, projectID.String(), ownerID.String(), CreateArtifactInput{
		Kind: "prototype", Title: "Proposal revision rounds",
		Content:        prototypeLineageCoveragePayload(t, pageSpecRef),
		SourceVersions: []ArtifactSourceInput{{Ref: pageSpecRef, Purpose: "page_spec", Required: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	baseR1, err := artifacts.CreateRevision(
		ctx, created.Artifact.ID, ownerID.String(), created.Draft.ETag,
		CreateRevisionInput{ChangeSummary: "Initialize Proposal rounds", ChangeSource: "system"},
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

	p1 := createAcceptedPrototypeRoundProposal(
		t, ctx, proposals, projectID.String(), ownerID.String(), created.Artifact.ID, baseR1, pageSpecRef,
		domain.ProposalOperation{
			ID: "round-p1", Kind: domain.OperationAdd, Path: "/proposalRound", Value: json.RawMessage(`"P1"`),
		},
	)
	unfrozenCompetitor := createAcceptedPrototypeRoundProposal(
		t, ctx, proposals, projectID.String(), ownerID.String(), created.Artifact.ID, baseR1, pageSpecRef,
		domain.ProposalOperation{
			ID: "round-unfrozen", Kind: domain.OperationAdd, Path: "/proposalRound", Value: json.RawMessage(`"unfrozen"`),
		},
	)
	draftAfterP1, err := proposals.Apply(
		ctx, p1.ID, ownerID.String(), ApplyProposalInput{Version: p1.Version},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proposals.Apply(ctx, unfrozenCompetitor.ID, ownerID.String(), ApplyProposalInput{
		Version: unfrozenCompetitor.Version, DiscardUnrevisionedChanges: true,
		ExpectedDraftID: draftAfterP1.ID, ExpectedDraftETag: draftAfterP1.ETag,
		ExpectedDraftContentHash: draftAfterP1.ContentHash,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("an applied Proposal not yet frozen in a Revision did not protect the draft: %v", err)
	}
	restoredBase, err := artifacts.UpdateDraft(
		ctx, draftAfterP1.ID, ownerID.String(), draftAfterP1.ETag,
		UpdateDraftInput{Content: baseR1.Content},
	)
	if err != nil {
		t.Fatalf("restore the reused draft to its exact base payload: %v", err)
	}
	if restoredBase.ContentHash != baseR1.ContentHash {
		t.Fatalf("restored draft hash = %s, want exact base %s", restoredBase.ContentHash, baseR1.ContentHash)
	}
	if _, err := proposals.Apply(
		ctx, unfrozenCompetitor.ID, ownerID.String(),
		ApplyProposalInput{Version: unfrozenCompetitor.Version},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("a clean-looking reused draft orphaned its unfrozen applied Proposal: %v", err)
	}
	draftAfterP1, err = artifacts.UpdateDraft(
		ctx, restoredBase.ID, ownerID.String(), restoredBase.ETag,
		UpdateDraftInput{Content: draftAfterP1.Content},
	)
	if err != nil {
		t.Fatalf("restore P1 content for immutable revision creation: %v", err)
	}

	baseR2, err := artifacts.CreateRevision(
		ctx, created.Artifact.ID, ownerID.String(), draftAfterP1.ETag,
		CreateRevisionInput{ChangeSummary: "Freeze P1", ChangeSource: "ai_proposal"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if baseR2.ProposalID == nil || *baseR2.ProposalID != p1.ID {
		t.Fatalf("R2 did not freeze P1: %+v", baseR2)
	}

	p2 := createAcceptedPrototypeRoundProposal(
		t, ctx, proposals, projectID.String(), ownerID.String(), created.Artifact.ID, baseR2, pageSpecRef,
		domain.ProposalOperation{
			ID: "round-p2", Kind: domain.OperationReplace, Path: "/proposalRound", Value: json.RawMessage(`"P2"`),
		},
	)
	dirtyBeforeP2 := updatePrototypeRoundDraft(
		t, ctx, artifacts, ownerID.String(), draftAfterP1, "manual edit before P2",
	)
	draftAfterP2 := applyPrototypeRoundWithConfirmedDiscard(
		t, ctx, proposals, ownerID.String(), p2, dirtyBeforeP2,
	)
	baseR3, err := artifacts.CreateRevision(
		ctx, created.Artifact.ID, ownerID.String(), draftAfterP2.ETag,
		CreateRevisionInput{ChangeSummary: "Freeze P2", ChangeSource: "ai_proposal"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if baseR3.ProposalID == nil || *baseR3.ProposalID != p2.ID {
		t.Fatalf("R3 did not freeze P2: %+v", baseR3)
	}

	p3 := createAcceptedPrototypeRoundProposal(
		t, ctx, proposals, projectID.String(), ownerID.String(), created.Artifact.ID, baseR3, pageSpecRef,
		domain.ProposalOperation{
			ID: "round-p3", Kind: domain.OperationReplace, Path: "/proposalRound", Value: json.RawMessage(`"P3"`),
		},
	)
	dirtyBeforeP3 := updatePrototypeRoundDraft(
		t, ctx, artifacts, ownerID.String(), draftAfterP2, "manual edit before P3",
	)
	draftAfterP3 := applyPrototypeRoundWithConfirmedDiscard(
		t, ctx, proposals, ownerID.String(), p3, dirtyBeforeP3,
	)
	baseR4, err := artifacts.CreateRevision(
		ctx, created.Artifact.ID, ownerID.String(), draftAfterP3.ETag,
		CreateRevisionInput{ChangeSummary: "Freeze P3", ChangeSource: "ai_proposal"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if baseR4.ProposalID == nil || *baseR4.ProposalID != p3.ID {
		t.Fatalf("R4 did not freeze P3: %+v", baseR4)
	}

	var frozenCount int64
	if err := database.Model(&storage.ArtifactRevisionModel{}).
		Where("proposal_id IN ?", []uuid.UUID{uuid.MustParse(p1.ID), uuid.MustParse(p2.ID), uuid.MustParse(p3.ID)}).
		Count(&frozenCount).Error; err != nil {
		t.Fatal(err)
	}
	if frozenCount != 3 {
		t.Fatalf("revision history froze %d of 3 applied Proposal rounds", frozenCount)
	}
}

func TestProposalApplyRejectsAcceptedNoOpBeforeAppliedStatePostgres(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, artifacts, projectID, ownerID := newArtifactLineageFixture(t, database)
	canonicalPayload, err := domain.CanonicalJSON(json.RawMessage(`{
		"summary":"Before",
		"blocks":[{
			"id":"requirement-1","type":"requirement","requirementId":"REQ-1","priority":"must",
			"text":"A no-op Proposal must remain reviewable rather than becoming applied.",
			"acceptanceCriteria":[{"id":"AC-1","statement":"No-op application cannot strand immutable lineage."}]
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	created, err := artifacts.Create(ctx, projectID.String(), ownerID.String(), CreateArtifactInput{
		Kind: "product_requirements", Title: "No-op Proposal", Content: canonicalPayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	base, err := artifacts.CreateRevision(
		ctx, created.Artifact.ID, ownerID.String(), created.Draft.ETag,
		CreateRevisionInput{ChangeSummary: "Initialize no-op Proposal test"},
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
		Constraints: json.RawMessage(`{}`), OutputSchemaVersion: "requirements-proposal/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := proposals.CreateProposal(ctx, projectID.String(), ownerID.String(), CreateProposalInput{
		ManifestID: manifest.ID, ArtifactID: created.Artifact.ID,
		Operations: []domain.ProposalOperation{{
			ID: "same-summary", Kind: domain.OperationReplace, Path: "/summary", Value: json.RawMessage(`"Before"`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err = proposals.Decide(ctx, proposal.ID, ownerID.String(), DecideProposalInput{
		OperationID: "same-summary", Decision: domain.DecisionAccepted, Version: proposal.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	abortsBefore := store.abortCalls
	if _, err := proposals.Apply(
		ctx, proposal.ID, ownerID.String(), ApplyProposalInput{Version: proposal.Version},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepted no-op Proposal became applied without a revisionable draft: %v", err)
	}
	if store.abortCalls != abortsBefore+1 {
		t.Fatalf("no-op Apply aborted %d pending contents, want exactly one", store.abortCalls-abortsBefore)
	}
	var storedProposal storage.OutputProposalModel
	if err := database.Where("id = ?", uuid.MustParse(proposal.ID)).Take(&storedProposal).Error; err != nil {
		t.Fatal(err)
	}
	if storedProposal.Status != string(domain.ProposalReady) || storedProposal.AppliedAt != nil ||
		storedProposal.BaseDraftID != nil {
		t.Fatalf("no-op Apply mutated Proposal state: %+v", storedProposal)
	}
	var storedDraft storage.ArtifactDraftModel
	if err := database.Where("id = ?", uuid.MustParse(created.Draft.ID)).Take(&storedDraft).Error; err != nil {
		t.Fatal(err)
	}
	if storedDraft.ContentHash != base.ContentHash || storedDraft.ETag != created.Draft.ETag {
		t.Fatalf("no-op Apply mutated the base draft: %+v", storedDraft)
	}
}

func createAcceptedPrototypeRoundProposal(
	t *testing.T,
	ctx context.Context,
	proposals *ProposalService,
	projectID string,
	ownerID string,
	artifactID string,
	base ArtifactRevision,
	pageSpecRef VersionRef,
	operation domain.ProposalOperation,
) domain.OutputProposal {
	t.Helper()
	baseRef := VersionRef{ArtifactID: base.ArtifactID, RevisionID: base.ID, ContentHash: base.ContentHash}
	manifest, err := proposals.CreateManifest(ctx, projectID, ownerID, CreateManifestInput{
		JobType: "generate_prototype", BaseRevision: &baseRef,
		Sources:     []ManifestSourceInput{{Ref: pageSpecRef, Purpose: "page_spec"}},
		Constraints: json.RawMessage(`{}`), OutputSchemaVersion: "prototype-proposal/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := proposals.CreateProposal(ctx, projectID, ownerID, CreateProposalInput{
		ManifestID: manifest.ID, ArtifactID: artifactID, Operations: []domain.ProposalOperation{operation},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err = proposals.Decide(ctx, proposal.ID, ownerID, DecideProposalInput{
		OperationID: operation.ID, Decision: domain.DecisionAccepted, Version: proposal.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

func updatePrototypeRoundDraft(
	t *testing.T,
	ctx context.Context,
	artifacts *ArtifactService,
	ownerID string,
	draft ArtifactDraft,
	note string,
) ArtifactDraft {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(draft.Content, &payload); err != nil {
		t.Fatal(err)
	}
	payload["manualRoundNote"] = note
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := artifacts.UpdateDraft(
		ctx, draft.ID, ownerID, draft.ETag, UpdateDraftInput{Content: encoded},
	)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func applyPrototypeRoundWithConfirmedDiscard(
	t *testing.T,
	ctx context.Context,
	proposals *ProposalService,
	ownerID string,
	proposal domain.OutputProposal,
	draft ArtifactDraft,
) ArtifactDraft {
	t.Helper()
	applied, err := proposals.Apply(ctx, proposal.ID, ownerID, ApplyProposalInput{
		Version: proposal.Version, DiscardUnrevisionedChanges: true,
		ExpectedDraftID: draft.ID, ExpectedDraftETag: draft.ETag,
		ExpectedDraftContentHash: draft.ContentHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	return applied
}
