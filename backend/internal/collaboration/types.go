package collaboration

import (
	"fmt"
	"strings"
	"time"
)

type DocumentMemberRole string

const (
	DocumentOwner           DocumentMemberRole = "owner"
	DocumentAssignee        DocumentMemberRole = "assignee"
	DocumentDownstreamOwner DocumentMemberRole = "downstreamOwner"
	DocumentReviewer        DocumentMemberRole = "reviewer"
	DocumentWatcher         DocumentMemberRole = "watcher"
)

type DocumentMemberBinding struct {
	UserID     string             `json:"userId"`
	Role       DocumentMemberRole `json:"role"`
	Reason     string             `json:"reason,omitempty"`
	AssignedBy string             `json:"assignedBy"`
	AssignedAt time.Time          `json:"assignedAt"`
}

type DocumentMemberBindingInput struct {
	UserID string             `json:"userId"`
	Role   DocumentMemberRole `json:"role"`
	Reason string             `json:"reason,omitempty"`
}

type DocumentMemberBindingSet struct {
	ArtifactID string                  `json:"artifactId"`
	ProjectID  string                  `json:"projectId"`
	Version    uint64                  `json:"version"`
	ETag       string                  `json:"etag"`
	Items      []DocumentMemberBinding `json:"items"`
	UpdatedAt  *time.Time              `json:"updatedAt,omitempty"`
}

func bindingSetETag(artifactID string, version uint64) string {
	return fmt.Sprintf(`"artifact-bindings:%s:v%d"`, artifactID, version)
}

func validDocumentMemberRole(role DocumentMemberRole) bool {
	switch role {
	case DocumentOwner, DocumentAssignee, DocumentDownstreamOwner, DocumentReviewer, DocumentWatcher:
		return true
	default:
		return false
	}
}

func databaseDocumentMemberRole(role DocumentMemberRole) string {
	if role == DocumentDownstreamOwner {
		return "downstream_owner"
	}
	return string(role)
}

func wireDocumentMemberRole(role string) DocumentMemberRole {
	if strings.TrimSpace(role) == "downstream_owner" {
		return DocumentDownstreamOwner
	}
	return DocumentMemberRole(strings.TrimSpace(role))
}
