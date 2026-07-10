package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ArtifactType string

const (
	ArtifactDocument       ArtifactType = "document"
	ArtifactBlueprint      ArtifactType = "blueprint"
	ArtifactPrototype      ArtifactType = "prototype"
	ArtifactImplementation ArtifactType = "implementation"
	ArtifactTest           ArtifactType = "test"
)

func (t ArtifactType) Valid() bool {
	switch t {
	case ArtifactDocument, ArtifactBlueprint, ArtifactPrototype, ArtifactImplementation, ArtifactTest:
		return true
	default:
		return false
	}
}

type ArtifactRef struct {
	ArtifactID  string `json:"artifactId"`
	RevisionID  string `json:"revisionId"`
	ContentHash string `json:"contentHash"`
	AnchorID    string `json:"anchorId,omitempty"`
}

func (r ArtifactRef) Validate() error {
	if strings.TrimSpace(r.ArtifactID) == "" {
		return invalid("artifactRef.artifactId", "is required")
	}
	if strings.TrimSpace(r.RevisionID) == "" {
		return invalid("artifactRef.revisionId", "is required")
	}
	if !IsCanonicalHash(r.ContentHash) {
		return invalid("artifactRef.contentHash", "must be a SHA-256 hash")
	}
	return nil
}

func (r ArtifactRef) Equal(other ArtifactRef) bool {
	return r.ArtifactID == other.ArtifactID &&
		r.RevisionID == other.RevisionID &&
		r.ContentHash == other.ContentHash &&
		r.AnchorID == other.AnchorID
}

type Artifact struct {
	ID                string       `json:"id"`
	ProjectID         string       `json:"projectId"`
	Type              ArtifactType `json:"type"`
	Title             string       `json:"title"`
	CurrentRevisionID string       `json:"currentRevisionId,omitempty"`
	Version           uint64       `json:"version"`
	CreatedAt         time.Time    `json:"createdAt"`
	UpdatedAt         time.Time    `json:"updatedAt"`
	ArchivedAt        *time.Time   `json:"archivedAt,omitempty"`
}

func NewArtifact(id, projectID string, artifactType ArtifactType, title string, now time.Time) (*Artifact, error) {
	artifact := &Artifact{
		ID: id, ProjectID: projectID, Type: artifactType, Title: strings.TrimSpace(title),
		Version: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := artifact.Validate(); err != nil {
		return nil, err
	}
	return artifact, nil
}

func (a Artifact) Validate() error {
	if strings.TrimSpace(a.ID) == "" {
		return invalid("artifact.id", "is required")
	}
	if strings.TrimSpace(a.ProjectID) == "" {
		return invalid("artifact.projectId", "is required")
	}
	if !a.Type.Valid() {
		return invalid("artifact.type", string(a.Type))
	}
	if strings.TrimSpace(a.Title) == "" {
		return invalid("artifact.title", "is required")
	}
	return nil
}

func (a *Artifact) AdvanceRevision(ref ArtifactRef, expectedVersion uint64, now time.Time) error {
	if a.Version != expectedVersion {
		return conflict("artifact", expectedVersion, a.Version)
	}
	if a.ArchivedAt != nil {
		return transition("artifact", "archived", "advance_revision")
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.ArtifactID != a.ID {
		return invalid("revision.artifactId", "does not match artifact")
	}
	a.CurrentRevisionID = ref.RevisionID
	a.Version++
	a.UpdatedAt = now.UTC()
	return nil
}

func (a *Artifact) Archive(expectedVersion uint64, now time.Time) error {
	if a.Version != expectedVersion {
		return conflict("artifact", expectedVersion, a.Version)
	}
	if a.ArchivedAt != nil {
		return transition("artifact", "archived", "archived")
	}
	archivedAt := now.UTC()
	a.ArchivedAt = &archivedAt
	a.Version++
	a.UpdatedAt = archivedAt
	return nil
}

type DraftStatus string

const (
	DraftEditing          DraftStatus = "editing"
	DraftInReview         DraftStatus = "in_review"
	DraftChangesRequested DraftStatus = "changes_requested"
	DraftApproved         DraftStatus = "approved"
	DraftApplied          DraftStatus = "applied"
	DraftAbandoned        DraftStatus = "abandoned"
)

type Draft struct {
	ID               string          `json:"id"`
	ArtifactID       string          `json:"artifactId"`
	BaseRevision     *ArtifactRef    `json:"baseRevision,omitempty"`
	AuthorID         string          `json:"authorId"`
	Content          json.RawMessage `json:"content"`
	ContentHash      string          `json:"contentHash"`
	Status           DraftStatus     `json:"status"`
	SourceManifestID string          `json:"sourceManifestId,omitempty"`
	Version          uint64          `json:"version"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

func NewDraft(id, artifactID, authorID string, base *ArtifactRef, content json.RawMessage, now time.Time) (*Draft, error) {
	canonical, err := CanonicalJSON(content)
	if err != nil {
		return nil, err
	}
	hash, _ := CanonicalHash(canonical)
	if base != nil {
		copyRef := *base
		if err := copyRef.Validate(); err != nil {
			return nil, err
		}
		if copyRef.ArtifactID != artifactID {
			return nil, invalid("draft.baseRevision.artifactId", "does not match draft artifact")
		}
		base = &copyRef
	}
	draft := &Draft{
		ID: id, ArtifactID: artifactID, BaseRevision: base, AuthorID: authorID,
		Content: canonical, ContentHash: hash, Status: DraftEditing, Version: 1,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(artifactID) == "" || strings.TrimSpace(authorID) == "" {
		return nil, invalid("draft", "id, artifactId and authorId are required")
	}
	return draft, nil
}

func (d *Draft) UpdateContent(content json.RawMessage, expectedVersion uint64, now time.Time) error {
	if d.Version != expectedVersion {
		return conflict("draft", expectedVersion, d.Version)
	}
	if d.Status != DraftEditing && d.Status != DraftChangesRequested {
		return transition("draft", string(d.Status), string(DraftEditing))
	}
	canonical, err := CanonicalJSON(content)
	if err != nil {
		return err
	}
	hash, _ := CanonicalHash(canonical)
	d.Content = canonical
	d.ContentHash = hash
	d.Status = DraftEditing
	d.Version++
	d.UpdatedAt = now.UTC()
	return nil
}

func (d *Draft) Submit(expectedVersion uint64, now time.Time) error {
	return d.transition(expectedVersion, DraftInReview, now, DraftEditing, DraftChangesRequested)
}

func (d *Draft) RequestChanges(expectedVersion uint64, now time.Time) error {
	return d.transition(expectedVersion, DraftChangesRequested, now, DraftInReview)
}

func (d *Draft) Approve(expectedVersion uint64, now time.Time) error {
	return d.transition(expectedVersion, DraftApproved, now, DraftInReview)
}

func (d *Draft) MarkApplied(expectedVersion uint64, now time.Time) error {
	return d.transition(expectedVersion, DraftApplied, now, DraftApproved)
}

func (d *Draft) Abandon(expectedVersion uint64, now time.Time) error {
	return d.transition(expectedVersion, DraftAbandoned, now, DraftEditing, DraftChangesRequested)
}

func (d *Draft) transition(expectedVersion uint64, target DraftStatus, now time.Time, allowed ...DraftStatus) error {
	if d.Version != expectedVersion {
		return conflict("draft", expectedVersion, d.Version)
	}
	for _, status := range allowed {
		if d.Status == status {
			d.Status = target
			d.Version++
			d.UpdatedAt = now.UTC()
			return nil
		}
	}
	return transition("draft", string(d.Status), string(target))
}

// Revision intentionally has no mutators. Content returns defensive copies.
type Revision struct {
	id               string
	artifactID       string
	number           int
	parent           *ArtifactRef
	sourceManifestID string
	content          json.RawMessage
	contentHash      string
	createdBy        string
	createdAt        time.Time
}

func NewRevision(id, artifactID string, number int, parent *ArtifactRef, sourceManifestID string, content json.RawMessage, createdBy string, now time.Time) (Revision, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(artifactID) == "" || strings.TrimSpace(createdBy) == "" {
		return Revision{}, invalid("revision", "id, artifactId and createdBy are required")
	}
	if number < 1 {
		return Revision{}, invalid("revision.number", "must be positive")
	}
	canonical, err := CanonicalJSON(content)
	if err != nil {
		return Revision{}, err
	}
	hash, _ := CanonicalHash(canonical)
	if parent != nil {
		copyRef := *parent
		if err := copyRef.Validate(); err != nil {
			return Revision{}, err
		}
		if copyRef.ArtifactID != artifactID {
			return Revision{}, invalid("revision.parent.artifactId", "does not match revision artifact")
		}
		parent = &copyRef
	}
	return Revision{
		id: id, artifactID: artifactID, number: number, parent: parent,
		sourceManifestID: sourceManifestID, content: canonical, contentHash: hash,
		createdBy: createdBy, createdAt: now.UTC(),
	}, nil
}

func (r Revision) ID() string               { return r.id }
func (r Revision) ArtifactID() string       { return r.artifactID }
func (r Revision) Number() int              { return r.number }
func (r Revision) SourceManifestID() string { return r.sourceManifestID }
func (r Revision) ContentHash() string      { return r.contentHash }
func (r Revision) CreatedBy() string        { return r.createdBy }
func (r Revision) CreatedAt() time.Time     { return r.createdAt }
func (r Revision) Content() json.RawMessage { return cloneJSON(r.content) }
func (r Revision) Parent() *ArtifactRef {
	if r.parent == nil {
		return nil
	}
	copyRef := *r.parent
	return &copyRef
}
func (r Revision) Ref(anchorID string) ArtifactRef {
	return ArtifactRef{ArtifactID: r.artifactID, RevisionID: r.id, ContentHash: r.contentHash, AnchorID: anchorID}
}

func (r Revision) String() string {
	return fmt.Sprintf("%s@%d:%s", r.artifactID, r.number, r.contentHash[:12])
}
