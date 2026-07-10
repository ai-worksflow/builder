package core

import (
	"testing"

	"github.com/google/uuid"
)

func TestEvaluateArtifactReviewGatePassesOnlyCompleteServerEvidence(t *testing.T) {
	t.Parallel()

	revisionID := uuid.NewString()
	gate := evaluateArtifactReviewGate(artifactReviewGateEvidence{
		ArtifactID: "artifact-1", ArtifactKind: "blueprint", ArtifactLifecycle: "active",
		RevisionID: revisionID, RevisionNumber: 4, RevisionContentHash: "sha256:current",
		DraftContentHash: "sha256:current", HealthKnown: true, SyncStatus: "current",
		RequiredDependencies: 2, CoveredDependencies: 2,
		ReviewApproved: true, ReviewStatus: "approved", ReviewID: uuid.NewString(),
		ContentLoaded: true,
	})
	if !gate.Passed || gate.TraceCoverage != 1 || len(gate.UnresolvedBlockingCommentIDs) != 0 {
		t.Fatalf("complete gate did not pass: %+v", gate)
	}
	for _, required := range []string{
		"artifact_active", "latest_revision_present", "draft_matches_latest_revision",
		"artifact_content_valid", "blocking_comments_resolved", "artifact_sync_current",
		"required_trace_coverage", "canonical_review_approved",
	} {
		check := reviewGateCheckByCode(gate, required)
		if check == nil || check.Severity != "info" {
			t.Fatalf("check %q did not pass: %+v", required, check)
		}
	}
}

func TestEvaluateArtifactReviewGateReportsEveryLiveBlocker(t *testing.T) {
	t.Parallel()

	firstComment, secondComment := uuid.NewString(), uuid.NewString()
	gate := evaluateArtifactReviewGate(artifactReviewGateEvidence{
		ArtifactID: "artifact-2", ArtifactKind: "prototype", ArtifactLifecycle: "active",
		RevisionID: uuid.NewString(), RevisionNumber: 2, RevisionContentHash: "sha256:reviewed",
		DraftContentHash: "sha256:changed", HealthKnown: true, SyncStatus: "needs_sync",
		RequiredDependencies: 2, CoveredDependencies: 1,
		BlockingCommentIDs: []string{secondComment, firstComment},
		ReviewStatus:       "open", ReviewID: uuid.NewString(), ContentLoaded: true,
		ContentFindings: []ValidationFinding{{
			Code: "prototype.frame_required", Severity: "blocker", Path: "$.frames", Message: "A frame is required.",
		}},
	})
	if gate.Passed || gate.TraceCoverage != 0.5 {
		t.Fatalf("blocked gate passed: %+v", gate)
	}
	if len(gate.UnresolvedBlockingCommentIDs) != 2 || gate.UnresolvedBlockingCommentIDs[0] > gate.UnresolvedBlockingCommentIDs[1] {
		t.Fatalf("blocking comment ids are not deterministic: %v", gate.UnresolvedBlockingCommentIDs)
	}
	for _, blocked := range []string{
		"draft_matches_latest_revision", "prototype.frame_required", "blocking_comments_resolved",
		"artifact_sync_current", "required_trace_coverage", "canonical_review_approved",
	} {
		check := reviewGateCheckByCode(gate, blocked)
		if check == nil || check.Severity != "error" || check.Message == "" {
			t.Fatalf("check %q did not expose its blocker: %+v", blocked, check)
		}
	}
}

func TestEvaluateArtifactReviewGateWithoutRevisionFailsClosed(t *testing.T) {
	t.Parallel()

	gate := evaluateArtifactReviewGate(artifactReviewGateEvidence{
		ArtifactID: "artifact-3", ArtifactKind: "project_brief", ArtifactLifecycle: "active",
	})
	if gate.Passed || gate.TraceCoverage != 0 || reviewGateCheckByCode(gate, "latest_revision_present").Severity != "error" || reviewGateCheckByCode(gate, "canonical_review_approved").Severity != "error" {
		t.Fatalf("revision-less artifact did not fail closed: %+v", gate)
	}
	if gate.Checks == nil || gate.UnresolvedBlockingCommentIDs == nil {
		t.Fatalf("gate arrays must serialize as arrays: %+v", gate)
	}
}

func reviewGateCheckByCode(gate ArtifactReviewGate, code string) *ReviewGateCheck {
	for index := range gate.Checks {
		if gate.Checks[index].Code == code {
			return &gate.Checks[index]
		}
	}
	return nil
}
