package domain

import (
	"strings"
	"time"
)

type Dependency struct {
	ID         string      `json:"id"`
	ProjectID  string      `json:"projectId"`
	Upstream   ArtifactRef `json:"upstream"`
	Downstream ArtifactRef `json:"downstream"`
	Kind       string      `json:"kind"`
	Required   bool        `json:"required"`
	CreatedBy  string      `json:"createdBy"`
	CreatedAt  time.Time   `json:"createdAt"`
}

func NewDependency(id, projectID string, upstream, downstream ArtifactRef, kind, createdBy string, required bool, now time.Time) (Dependency, error) {
	dependency := Dependency{
		ID: id, ProjectID: projectID, Upstream: upstream, Downstream: downstream,
		Kind: strings.TrimSpace(kind), Required: required, CreatedBy: createdBy, CreatedAt: now.UTC(),
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(projectID) == "" || strings.TrimSpace(createdBy) == "" {
		return Dependency{}, invalid("dependency", "id, projectId and createdBy are required")
	}
	if dependency.Kind == "" {
		return Dependency{}, invalid("dependency.kind", "is required")
	}
	if err := upstream.Validate(); err != nil {
		return Dependency{}, err
	}
	if err := downstream.Validate(); err != nil {
		return Dependency{}, err
	}
	if upstream.Equal(downstream) {
		return Dependency{}, invalid("dependency", "self-dependencies are not allowed")
	}
	return dependency, nil
}

type TraceLink struct {
	ID           string            `json:"id"`
	ProjectID    string            `json:"projectId"`
	Source       ArtifactRef       `json:"source"`
	Target       ArtifactRef       `json:"target"`
	Relationship string            `json:"relationship"`
	Rationale    string            `json:"rationale,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	CreatedBy    string            `json:"createdBy"`
	CreatedAt    time.Time         `json:"createdAt"`
}

func NewTraceLink(id, projectID string, source, target ArtifactRef, relationship, rationale, createdBy string, metadata map[string]string, now time.Time) (TraceLink, error) {
	link := TraceLink{
		ID: id, ProjectID: projectID, Source: source, Target: target,
		Relationship: strings.TrimSpace(relationship), Rationale: strings.TrimSpace(rationale),
		Metadata: cloneStringMap(metadata), CreatedBy: createdBy, CreatedAt: now.UTC(),
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(projectID) == "" || strings.TrimSpace(createdBy) == "" {
		return TraceLink{}, invalid("traceLink", "id, projectId and createdBy are required")
	}
	if link.Relationship == "" {
		return TraceLink{}, invalid("traceLink.relationship", "is required")
	}
	if err := source.Validate(); err != nil {
		return TraceLink{}, err
	}
	if err := target.Validate(); err != nil {
		return TraceLink{}, err
	}
	if source.Equal(target) {
		return TraceLink{}, invalid("traceLink", "source and target must differ")
	}
	return link, nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

type ReviewStatus string

const (
	ReviewPending          ReviewStatus = "pending"
	ReviewApproved         ReviewStatus = "approved"
	ReviewChangesRequested ReviewStatus = "changes_requested"
	ReviewDismissed        ReviewStatus = "dismissed"
)

type Review struct {
	ID         string       `json:"id"`
	DraftID    string       `json:"draftId"`
	ArtifactID string       `json:"artifactId"`
	AuthorID   string       `json:"authorId"`
	ReviewerID string       `json:"reviewerId"`
	Status     ReviewStatus `json:"status"`
	Summary    string       `json:"summary,omitempty"`
	Version    uint64       `json:"version"`
	CreatedAt  time.Time    `json:"createdAt"`
	DecidedAt  *time.Time   `json:"decidedAt,omitempty"`
	DecidedBy  string       `json:"decidedBy,omitempty"`
}

func NewReview(id string, draft Draft, reviewerID string, now time.Time) (*Review, error) {
	if draft.Status != DraftInReview {
		return nil, transition("draft", string(draft.Status), string(DraftInReview))
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(reviewerID) == "" {
		return nil, invalid("review", "id and reviewerId are required")
	}
	return &Review{
		ID: id, DraftID: draft.ID, ArtifactID: draft.ArtifactID, AuthorID: draft.AuthorID,
		ReviewerID: reviewerID, Status: ReviewPending, Version: 1, CreatedAt: now.UTC(),
	}, nil
}

func (r *Review) Approve(actorID, summary string, expectedVersion uint64, now time.Time) error {
	if actorID == r.AuthorID {
		return &DomainError{Kind: ErrSelfApproval, Field: "review.reviewerId", Message: "draft author cannot approve their own draft"}
	}
	return r.decide(actorID, summary, expectedVersion, ReviewApproved, now)
}

func (r *Review) RequestChanges(actorID, summary string, expectedVersion uint64, now time.Time) error {
	return r.decide(actorID, summary, expectedVersion, ReviewChangesRequested, now)
}

func (r *Review) Dismiss(actorID, summary string, expectedVersion uint64, now time.Time) error {
	return r.decide(actorID, summary, expectedVersion, ReviewDismissed, now)
}

func (r *Review) decide(actorID, summary string, expectedVersion uint64, target ReviewStatus, now time.Time) error {
	if r.Version != expectedVersion {
		return conflict("review", expectedVersion, r.Version)
	}
	if r.Status != ReviewPending {
		return transition("review", string(r.Status), string(target))
	}
	if actorID != r.ReviewerID {
		return invalid("review.actorId", "must match the assigned reviewer")
	}
	if target == ReviewChangesRequested && strings.TrimSpace(summary) == "" {
		return invalid("review.summary", "is required when requesting changes")
	}
	decidedAt := now.UTC()
	r.Status = target
	r.Summary = strings.TrimSpace(summary)
	r.DecidedAt = &decidedAt
	r.DecidedBy = actorID
	r.Version++
	return nil
}

type CommentStatus string

const (
	CommentOpen     CommentStatus = "open"
	CommentResolved CommentStatus = "resolved"
)

type CommentAnchor struct {
	ArtifactID string `json:"artifactId"`
	RevisionID string `json:"revisionId,omitempty"`
	DraftID    string `json:"draftId,omitempty"`
	NodeID     string `json:"nodeId,omitempty"`
	FieldPath  string `json:"fieldPath,omitempty"`
}

func (a CommentAnchor) Validate() error {
	if strings.TrimSpace(a.ArtifactID) == "" {
		return invalid("comment.anchor.artifactId", "is required")
	}
	if (a.RevisionID == "") == (a.DraftID == "") {
		return invalid("comment.anchor", "exactly one of revisionId or draftId is required")
	}
	return nil
}

type Comment struct {
	ID         string        `json:"id"`
	ParentID   string        `json:"parentId,omitempty"`
	Anchor     CommentAnchor `json:"anchor"`
	AuthorID   string        `json:"authorId"`
	Body       string        `json:"body"`
	Status     CommentStatus `json:"status"`
	Version    uint64        `json:"version"`
	CreatedAt  time.Time     `json:"createdAt"`
	UpdatedAt  time.Time     `json:"updatedAt"`
	ResolvedBy string        `json:"resolvedBy,omitempty"`
}

func NewComment(id, parentID string, anchor CommentAnchor, authorID, body string, now time.Time) (*Comment, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(authorID) == "" || strings.TrimSpace(body) == "" {
		return nil, invalid("comment", "id, authorId and body are required")
	}
	if err := anchor.Validate(); err != nil {
		return nil, err
	}
	return &Comment{
		ID: id, ParentID: parentID, Anchor: anchor, AuthorID: authorID, Body: strings.TrimSpace(body),
		Status: CommentOpen, Version: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}, nil
}

func (c *Comment) Resolve(actorID string, expectedVersion uint64, now time.Time) error {
	if c.Version != expectedVersion {
		return conflict("comment", expectedVersion, c.Version)
	}
	if c.Status != CommentOpen {
		return transition("comment", string(c.Status), string(CommentResolved))
	}
	if strings.TrimSpace(actorID) == "" {
		return invalid("comment.actorId", "is required")
	}
	c.Status = CommentResolved
	c.ResolvedBy = actorID
	c.Version++
	c.UpdatedAt = now.UTC()
	return nil
}

func (c *Comment) Reopen(actorID string, expectedVersion uint64, now time.Time) error {
	if c.Version != expectedVersion {
		return conflict("comment", expectedVersion, c.Version)
	}
	if c.Status != CommentResolved {
		return transition("comment", string(c.Status), string(CommentOpen))
	}
	if strings.TrimSpace(actorID) == "" {
		return invalid("comment.actorId", "is required")
	}
	c.Status = CommentOpen
	c.ResolvedBy = ""
	c.Version++
	c.UpdatedAt = now.UTC()
	return nil
}
