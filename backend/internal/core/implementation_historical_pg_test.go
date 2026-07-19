package core

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

func TestHistoricalManualProposalProjectsExactCompletionOnReviewAndApplyPostgres(t *testing.T) {
	database, cleanup := multiBundlePostgresDatabase(t)
	defer cleanup()
	fixture := seedMultiBundlePostgresFixture(t, database)
	ctx := context.Background()

	fileContent := "export const projected = true\n"
	proposal, err := fixture.implementation.Create(
		ctx,
		fixture.projectID.String(),
		fixture.ownerID.String(),
		CreateImplementationProposalInput{
			BuildManifestID:          fixture.rootA.ID,
			ApplicationBuildContract: multiBundleBuildContractRef(fixture.rootA.ID),
			Operations: []FileOperation{{
				ID: "project-completion", Kind: "file.upsert", Path: "src/projected.ts",
				Content: &fileContent, Language: "typescript",
			}},
		},
	)
	if err != nil {
		t.Fatalf("create complete manual Proposal: %v", err)
	}

	clearHistoricalManualCompletionProjection(t, database, proposal.ID)
	assertImplementationCompletionProjection(t, database, proposal.ID, nil, nil)
	proposal, err = fixture.implementation.Decide(
		ctx,
		proposal.ID,
		fixture.ownerID.String(),
		DecideImplementationInput{
			OperationID: proposal.Operations[0].ID,
			Decision:    ImplementationAccepted,
			Version:     proposal.Version,
		},
	)
	if err != nil {
		t.Fatalf("review historical complete manual Proposal: %v", err)
	}
	if proposal.Status != "ready" {
		t.Fatalf("reviewed Proposal status = %q, want ready", proposal.Status)
	}
	zero := 0
	assertImplementationCompletionProjection(t, database, proposal.ID, &zero, &zero)

	// A pre-053 Proposal could already be ready while both projections were
	// absent. Recreate that storage shape and prove Apply performs the same
	// one-time exact projection atomically with immutable revision creation.
	clearHistoricalManualCompletionProjection(t, database, proposal.ID)
	revision, err := fixture.implementation.Apply(
		ctx,
		proposal.ID,
		fixture.ownerID.String(),
		ApplyImplementationInput{Version: proposal.Version},
	)
	if err != nil {
		t.Fatalf("apply historical ready manual Proposal: %v", err)
	}
	if revision.ID == "" || revision.ProposalID == nil || *revision.ProposalID != proposal.ID {
		t.Fatalf("immutable revision lost Proposal identity: %#v", revision)
	}
	assertImplementationCompletionProjection(t, database, proposal.ID, &zero, &zero)
}

func TestIncompleteManualProposalCanOnlyBeQuarantinedPostgres(t *testing.T) {
	database, cleanup := multiBundlePostgresDatabase(t)
	defer cleanup()
	fixture := seedMultiBundlePostgresFixture(t, database)
	ctx := context.Background()

	fileContent := "export const incomplete = true\n"
	proposal, err := fixture.implementation.Create(
		ctx,
		fixture.projectID.String(),
		fixture.ownerID.String(),
		CreateImplementationProposalInput{
			BuildManifestID:          fixture.rootA.ID,
			ApplicationBuildContract: multiBundleBuildContractRef(fixture.rootA.ID),
			Operations: []FileOperation{{
				ID: "incomplete-operation", Kind: "file.upsert", Path: "src/incomplete.ts",
				Content: &fileContent, Language: "typescript",
			}},
			Diagnostics: []ValidationFinding{{
				Code: "missing_contract", Severity: "blocker", Message: "API contract is absent.",
			}},
			UnimplementedItems: []string{"Persistence is not implemented."},
		},
	)
	if err != nil {
		t.Fatalf("create incomplete manual Proposal: %v", err)
	}
	one := 1
	assertImplementationCompletionProjection(t, database, proposal.ID, &one, &one)

	_, err = fixture.implementation.Decide(
		ctx,
		proposal.ID,
		fixture.ownerID.String(),
		DecideImplementationInput{
			OperationID: proposal.Operations[0].ID,
			Decision:    ImplementationAccepted,
			Version:     proposal.Version,
		},
	)
	if !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("incomplete manual Proposal review error = %v, want blocking gate", err)
	}
	var decisionCount int64
	if err := database.Model(&storage.ImplementationOperationDecisionModel{}).
		Where("proposal_id = ?", uuid.MustParse(proposal.ID)).Count(&decisionCount).Error; err != nil {
		t.Fatal(err)
	}
	if decisionCount != 0 {
		t.Fatalf("blocked Proposal wrote %d decision(s)", decisionCount)
	}

	quarantined, err := fixture.implementation.Quarantine(
		ctx,
		proposal.ID,
		fixture.ownerID.String(),
		QuarantineImplementationInput{
			Version: proposal.Version,
			Reason:  "Replace incomplete immutable output with a governed Candidate.",
		},
	)
	if err != nil {
		t.Fatalf("quarantine incomplete manual Proposal: %v", err)
	}
	if quarantined.Status != "stale" || quarantined.Version != proposal.Version+1 {
		t.Fatalf("quarantined Proposal = %#v", quarantined)
	}

	replacementContent := "export const replacement = true\n"
	replacement, err := fixture.implementation.Create(
		ctx,
		fixture.projectID.String(),
		fixture.ownerID.String(),
		CreateImplementationProposalInput{
			BuildManifestID:          fixture.rootA.ID,
			ApplicationBuildContract: multiBundleBuildContractRef(fixture.rootA.ID),
			Operations: []FileOperation{{
				ID: "replacement-operation", Kind: "file.upsert", Path: "src/replacement.ts",
				Content: &replacementContent, Language: "typescript",
			}},
		},
	)
	if err != nil {
		t.Fatalf("quarantine did not release active Workbench Proposal slot: %v", err)
	}
	if replacement.Status != "open" || replacement.ID == proposal.ID {
		t.Fatalf("unexpected replacement Proposal: %#v", replacement)
	}
}

func clearHistoricalManualCompletionProjection(t *testing.T, database *gorm.DB, proposalID string) {
	t.Helper()
	if err := database.Exec(`
ALTER TABLE implementation_proposals
DISABLE TRIGGER implementation_proposal_00_legacy_ai_gate
`).Error; err != nil {
		t.Fatalf("disable completion projection gate for historical fixture: %v", err)
	}
	updateErr := database.Exec(`
UPDATE implementation_proposals
SET unimplemented_count = NULL,
    blocking_diagnostic_count = NULL
WHERE id = ?
`, uuid.MustParse(proposalID)).Error
	enableErr := database.Exec(`
ALTER TABLE implementation_proposals
ENABLE TRIGGER implementation_proposal_00_legacy_ai_gate
`).Error
	if updateErr != nil {
		t.Fatalf("create historical completion projection fixture: %v", updateErr)
	}
	if enableErr != nil {
		t.Fatalf("restore completion projection gate: %v", enableErr)
	}
}

func assertImplementationCompletionProjection(
	t *testing.T,
	database *gorm.DB,
	proposalID string,
	wantUnimplemented, wantBlocking *int,
) {
	t.Helper()
	var model storage.ImplementationProposalModel
	if err := database.Select("unimplemented_count", "blocking_diagnostic_count").
		Where("id = ?", uuid.MustParse(proposalID)).Take(&model).Error; err != nil {
		t.Fatal(err)
	}
	if !optionalIntEqual(model.UnimplementedCount, wantUnimplemented) ||
		!optionalIntEqual(model.BlockingDiagnosticCount, wantBlocking) {
		t.Fatalf(
			"completion projection = %v/%v, want %v/%v",
			model.UnimplementedCount, model.BlockingDiagnosticCount, wantUnimplemented, wantBlocking,
		)
	}
}

func optionalIntEqual(actual, expected *int) bool {
	if actual == nil || expected == nil {
		return actual == nil && expected == nil
	}
	return *actual == *expected
}
