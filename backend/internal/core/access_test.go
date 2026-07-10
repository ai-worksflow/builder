package core

import (
	"strings"
	"testing"
	"unicode/utf8"

	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
)

func TestRoleActionMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		role    Role
		action  Action
		allowed bool
	}{
		{RoleOwner, ActionAdmin, true},
		{RoleAdmin, ActionApprove, true},
		{RoleEditor, ActionReview, true},
		{RoleEditor, ActionApprove, false},
		{RoleCommenter, ActionComment, true},
		{RoleCommenter, ActionEdit, false},
		{RoleViewer, ActionView, true},
		{RoleViewer, ActionComment, false},
	}
	for _, test := range tests {
		_, allowed := roleActions[test.role][test.action]
		if allowed != test.allowed {
			t.Fatalf("role %s action %s allowed=%v, want %v", test.role, test.action, allowed, test.allowed)
		}
	}
}

func TestProjectAndDraftETagsAreVersioned(t *testing.T) {
	t.Parallel()
	projectID := mustUUID(t, "e5dc14f8-fac7-4336-b7fe-0c130c94ca68")
	draftID := mustUUID(t, "1f26fc9d-3895-4acc-a983-333a546f94d1")
	if projectETag(projectID, 1) == projectETag(projectID, 2) {
		t.Fatal("project ETag must change with version")
	}
	if draftETag(draftID, 1, "sha256:abc") == draftETag(draftID, 2, "sha256:abc") {
		t.Fatal("draft ETag must change with sequence")
	}
}

func TestTruncateKeepsUTF8Valid(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("界", 10)
	truncated := truncate(value, 10)
	if truncated == "" || !strings.HasPrefix(value, truncated) || !utf8.ValidString(truncated) {
		t.Fatalf("truncate() = %q", truncated)
	}
}

func TestArtifactStatusPrefersCurrentWorkAndArchivedLifecycle(t *testing.T) {
	t.Parallel()
	approved := mustUUID(t, "505e18c0-e783-41ed-a829-f9af59c6bf35")
	inReview := "in_review"
	needsSync := "needs_sync"
	model := storage.ArtifactModel{ID: approved, LatestApprovedRevisionID: &approved, Lifecycle: "active"}
	artifact := artifactFromModels(model, &artifactStatusFields{RevisionStatus: &inReview})
	if artifact.Status != "inReview" {
		t.Fatalf("current work status = %q", artifact.Status)
	}
	model.Lifecycle = "archived"
	artifact = artifactFromModels(model, &artifactStatusFields{RevisionStatus: &inReview, SyncStatus: &needsSync})
	if artifact.Status != "archived" {
		t.Fatalf("archived status = %q", artifact.Status)
	}
}

func TestContainsStableAnchorSearchesNestedStructuredIdentifiers(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"states":[{"id":"STATE-1","layers":[{"layerId":"LAYER-9"}]}]}`)
	if !containsStableAnchor(payload, "LAYER-9") || containsStableAnchor(payload, "missing") {
		t.Fatal("nested stable anchor lookup returned an unexpected result")
	}
}
