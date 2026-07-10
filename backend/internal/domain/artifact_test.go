package domain

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

var testNow = time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)

func testRevision(t *testing.T, artifactID, revisionID string, number int, content string) Revision {
	t.Helper()
	revision, err := NewRevision(revisionID, artifactID, number, nil, "", json.RawMessage(content), "author", testNow)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestDraftStrictTransitionsAndOptimisticConcurrency(t *testing.T) {
	draft, err := NewDraft("draft-1", "artifact-1", "author", nil, json.RawMessage(`{"title":"one"}`), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := draft.Submit(99, testNow); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if err := draft.Submit(1, testNow); err != nil {
		t.Fatal(err)
	}
	if err := draft.UpdateContent(json.RawMessage(`{}`), 2, testNow); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected invalid transition, got %v", err)
	}
	if err := draft.RequestChanges(2, testNow); err != nil {
		t.Fatal(err)
	}
	if err := draft.UpdateContent(json.RawMessage(`{"title":"two"}`), 3, testNow); err != nil {
		t.Fatal(err)
	}
	if draft.Status != DraftEditing || draft.Version != 4 {
		t.Fatalf("unexpected draft state: %+v", draft)
	}
}

func TestRevisionContentIsImmutableByDefensiveCopy(t *testing.T) {
	revision := testRevision(t, "artifact-1", "revision-1", 1, `{"value":1}`)
	copyContent := revision.Content()
	copyContent[0] = '['
	if string(revision.Content()) != `{"value":1}` {
		t.Fatalf("revision content was mutated: %s", revision.Content())
	}
	if revision.Ref("REQ-1").AnchorID != "REQ-1" {
		t.Fatal("expected an anchored immutable reference")
	}
}

func TestArtifactAdvanceRevisionChecksVersionAndOwnership(t *testing.T) {
	artifact, err := NewArtifact("artifact-1", "project-1", ArtifactDocument, "Requirements", testNow)
	if err != nil {
		t.Fatal(err)
	}
	other := testRevision(t, "artifact-2", "revision-1", 1, `{}`)
	if err := artifact.AdvanceRevision(other.Ref(""), 1, testNow); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ownership error, got %v", err)
	}
	revision := testRevision(t, artifact.ID, "revision-1", 1, `{}`)
	if err := artifact.AdvanceRevision(revision.Ref(""), 1, testNow); err != nil {
		t.Fatal(err)
	}
	if err := artifact.Archive(1, testNow); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected stale version conflict, got %v", err)
	}
}

func TestReviewPreventsSelfApprovalAndRequiresAssignedReviewer(t *testing.T) {
	draft, _ := NewDraft("draft-1", "artifact-1", "author", nil, json.RawMessage(`{}`), testNow)
	if err := draft.Submit(1, testNow); err != nil {
		t.Fatal(err)
	}
	review, err := NewReview("review-1", *draft, "reviewer", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := review.Approve("author", "looks good", 1, testNow); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("expected self approval guard, got %v", err)
	}
	if err := review.Approve("other", "looks good", 1, testNow); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected assigned reviewer guard, got %v", err)
	}
	if err := review.Approve("reviewer", "looks good", 1, testNow); err != nil {
		t.Fatal(err)
	}
	if err := review.RequestChanges("reviewer", "change it", 2, testNow); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected terminal review state, got %v", err)
	}
}

func TestDependencyAndCommentAnchorsAreVersionSpecific(t *testing.T) {
	upstream := testRevision(t, "requirements", "req-v1", 1, `{}`).Ref("REQ-1")
	downstream := testRevision(t, "blueprint", "bp-v1", 1, `{}`).Ref("PAGE-1")
	if _, err := NewDependency("dep-1", "project-1", upstream, downstream, "drives", "author", true, testNow); err != nil {
		t.Fatal(err)
	}
	anchor := CommentAnchor{ArtifactID: "blueprint", RevisionID: "bp-v1", NodeID: "PAGE-1", FieldPath: "/route"}
	comment, err := NewComment("comment-1", "", anchor, "reviewer", "Use a stable route", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := comment.Resolve("author", 1, testNow); err != nil {
		t.Fatal(err)
	}
	if err := comment.Resolve("author", 2, testNow); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected resolve to be one-way until reopen, got %v", err)
	}
}
