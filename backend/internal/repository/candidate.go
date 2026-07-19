package repository

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	CandidateSchemaVersion         = "candidate-workspace/v1"
	CandidateSnapshotSchemaVersion = "candidate-snapshot/v1"
)

var (
	ErrInvalidCandidate = errors.New("invalid candidate workspace")
	ErrLeaseRequired    = errors.New("candidate writer lease is required")
	ErrLeaseFenced      = errors.New("candidate writer lease is fenced")
	ErrCandidateState   = errors.New("candidate state does not allow the operation")
)

type CandidateStatus string

const (
	CandidateActive    CandidateStatus = "active"
	CandidateFrozen    CandidateStatus = "frozen"
	CandidateAbandoned CandidateStatus = "abandoned"
)

type ExactReference struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

type ExactRevisionReference struct {
	ArtifactID  string `json:"artifactId"`
	RevisionID  string `json:"revisionId"`
	ContentHash string `json:"contentHash"`
}

type RepositorySnapshot struct {
	ID                    string                  `json:"id"`
	ProjectID             string                  `json:"projectId"`
	BuildManifest         ExactReference          `json:"buildManifest"`
	BuildContract         ExactReference          `json:"buildContract"`
	FullStackTemplate     ExactReference          `json:"fullStackTemplate"`
	BaseWorkspaceRevision *ExactRevisionReference `json:"baseWorkspaceRevision,omitempty"`
	Tree                  TreeManifest            `json:"tree"`
	CreatedBy             string                  `json:"createdBy"`
	CreatedAt             time.Time               `json:"createdAt"`
}

type WriterLease struct {
	OwnerID   string    `json:"ownerId"`
	Epoch     uint64    `json:"epoch"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type CandidateWorkspace struct {
	SchemaVersion         string                  `json:"schemaVersion"`
	ID                    string                  `json:"id"`
	ProjectID             string                  `json:"projectId"`
	RepositorySnapshotID  string                  `json:"repositorySnapshotId"`
	BuildManifest         ExactReference          `json:"buildManifest"`
	BuildContract         ExactReference          `json:"buildContract"`
	FullStackTemplate     ExactReference          `json:"fullStackTemplate"`
	BaseWorkspaceRevision *ExactRevisionReference `json:"baseWorkspaceRevision,omitempty"`
	BaseTreeHash          string                  `json:"baseTreeHash"`
	CurrentTree           TreeManifest            `json:"currentTree"`
	Status                CandidateStatus         `json:"status"`
	Dirty                 bool                    `json:"dirty"`
	Conflicted            bool                    `json:"conflicted"`
	Stale                 bool                    `json:"stale"`
	RebaseRequired        bool                    `json:"rebaseRequired"`
	SessionEpoch          uint64                  `json:"sessionEpoch"`
	Version               uint64                  `json:"version"`
	JournalSequence       uint64                  `json:"journalSequence"`
	WriterLeaseEpoch      uint64                  `json:"writerLeaseEpoch"`
	Lease                 *WriterLease            `json:"lease,omitempty"`
	CreatedBy             string                  `json:"createdBy"`
	CreatedAt             time.Time               `json:"createdAt"`
	UpdatedAt             time.Time               `json:"updatedAt"`
}

type JournalEntry struct {
	CandidateID   string        `json:"candidateId"`
	Sequence      uint64        `json:"sequence"`
	CandidateFrom uint64        `json:"candidateVersionFrom"`
	CandidateTo   uint64        `json:"candidateVersionTo"`
	SessionEpoch  uint64        `json:"sessionEpoch"`
	LeaseEpoch    uint64        `json:"leaseEpoch"`
	ActorID       string        `json:"actorId"`
	Attribution   string        `json:"attribution"`
	Operation     FileOperation `json:"operation"`
	BeforeTree    string        `json:"beforeTreeHash"`
	AfterTree     string        `json:"afterTreeHash"`
	CreatedAt     time.Time     `json:"createdAt"`
}

type CandidateSnapshot struct {
	SchemaVersion    string       `json:"schemaVersion"`
	ID               string       `json:"id"`
	ProjectID        string       `json:"projectId"`
	CandidateID      string       `json:"candidateId"`
	CandidateVersion uint64       `json:"candidateVersion"`
	JournalSequence  uint64       `json:"journalSequence"`
	SessionEpoch     uint64       `json:"sessionEpoch"`
	WriterLeaseEpoch uint64       `json:"writerLeaseEpoch"`
	Tree             TreeManifest `json:"tree"`
	Reason           string       `json:"reason"`
	CreatedBy        string       `json:"createdBy"`
	CreatedAt        time.Time    `json:"createdAt"`
}

func NewCandidate(id string, snapshot RepositorySnapshot, actorID string, now time.Time) (CandidateWorkspace, error) {
	if !validUUID(id) || !validUUID(actorID) || now.IsZero() {
		return CandidateWorkspace{}, fmt.Errorf("%w: UUIDs and timestamp are required", ErrInvalidCandidate)
	}
	if err := snapshot.Validate(); err != nil {
		return CandidateWorkspace{}, err
	}
	if now.Before(snapshot.CreatedAt) {
		return CandidateWorkspace{}, fmt.Errorf("%w: candidate predates repository snapshot", ErrInvalidCandidate)
	}
	tree, _ := ParseTree(snapshot.Tree)
	now = now.UTC()
	return CandidateWorkspace{
		SchemaVersion: CandidateSchemaVersion, ID: strings.TrimSpace(id), ProjectID: snapshot.ProjectID,
		RepositorySnapshotID: snapshot.ID, BuildManifest: snapshot.BuildManifest, BuildContract: snapshot.BuildContract,
		FullStackTemplate: snapshot.FullStackTemplate, BaseWorkspaceRevision: cloneRevisionReference(snapshot.BaseWorkspaceRevision),
		BaseTreeHash: tree.TreeHash, CurrentTree: tree, Status: CandidateActive, SessionEpoch: 1, Version: 1,
		CreatedBy: strings.TrimSpace(actorID), CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (snapshot RepositorySnapshot) Validate() error {
	if !validUUID(snapshot.ID) || !validUUID(snapshot.ProjectID) || !validUUID(snapshot.CreatedBy) || snapshot.CreatedAt.IsZero() {
		return fmt.Errorf("%w: invalid repository snapshot identity", ErrInvalidCandidate)
	}
	for _, ref := range []ExactReference{snapshot.BuildManifest, snapshot.BuildContract, snapshot.FullStackTemplate} {
		if err := validateExact(ref); err != nil {
			return err
		}
	}
	if snapshot.BaseWorkspaceRevision != nil {
		ref := snapshot.BaseWorkspaceRevision
		if !validUUID(ref.ArtifactID) || !validUUID(ref.RevisionID) || !isCanonicalExternalHash(ref.ContentHash) {
			return fmt.Errorf("%w: exact workspace revision reference", ErrInvalidCandidate)
		}
	}
	if _, err := ParseTree(snapshot.Tree); err != nil {
		return err
	}
	return nil
}

func (candidate CandidateWorkspace) AcquireLease(expectedVersion uint64, ownerID string, ttl time.Duration, now time.Time) (CandidateWorkspace, WriterLease, error) {
	next, lease, _, err := candidate.AcquireLeaseWithEvent(expectedVersion, ownerID, ttl, now)
	return next, lease, err
}

func (candidate CandidateWorkspace) AcquireLeaseWithEvent(
	expectedVersion uint64,
	ownerID string,
	ttl time.Duration,
	now time.Time,
) (CandidateWorkspace, WriterLease, CandidateControlEvent, error) {
	if err := candidate.Validate(); err != nil {
		return CandidateWorkspace{}, WriterLease{}, CandidateControlEvent{}, err
	}
	if candidate.Status != CandidateActive || expectedVersion != candidate.Version {
		return CandidateWorkspace{}, WriterLease{}, CandidateControlEvent{}, ErrCandidateState
	}
	if !validUUID(ownerID) || ttl <= 0 || ttl > 30*time.Minute || now.IsZero() || now.Before(candidate.UpdatedAt) {
		return CandidateWorkspace{}, WriterLease{}, CandidateControlEvent{}, fmt.Errorf("%w: invalid lease request", ErrInvalidCandidate)
	}
	if candidate.Lease != nil && now.Before(candidate.Lease.ExpiresAt) && candidate.Lease.OwnerID != strings.TrimSpace(ownerID) {
		return CandidateWorkspace{}, WriterLease{}, CandidateControlEvent{}, ErrLeaseRequired
	}
	epoch := candidate.WriterLeaseEpoch + 1
	lease := WriterLease{OwnerID: strings.TrimSpace(ownerID), Epoch: epoch, ExpiresAt: now.UTC().Add(ttl)}
	next := cloneCandidate(candidate)
	next.WriterLeaseEpoch = epoch
	next.Lease = &lease
	next.Version++
	next.UpdatedAt = now.UTC()
	event := candidateControlEvent(candidate, next, ControlLeaseAcquired, ownerID, "writer lease acquired", nil, now)
	return next, lease, event, nil
}

func (candidate CandidateWorkspace) Apply(
	expectedVersion, expectedSessionEpoch, leaseEpoch uint64,
	actorID, attribution string,
	operation FileOperation,
	now time.Time,
) (CandidateWorkspace, JournalEntry, error) {
	if err := candidate.Validate(); err != nil {
		return CandidateWorkspace{}, JournalEntry{}, err
	}
	if candidate.Status != CandidateActive || expectedVersion != candidate.Version {
		return CandidateWorkspace{}, JournalEntry{}, ErrCandidateState
	}
	if expectedSessionEpoch != candidate.SessionEpoch {
		return CandidateWorkspace{}, JournalEntry{}, ErrLeaseFenced
	}
	if candidate.Lease == nil {
		return CandidateWorkspace{}, JournalEntry{}, ErrLeaseRequired
	}
	if candidate.WriterLeaseEpoch != leaseEpoch || candidate.Lease.Epoch != leaseEpoch ||
		candidate.Lease.OwnerID != strings.TrimSpace(actorID) ||
		now.IsZero() || !now.Before(candidate.Lease.ExpiresAt) {
		return CandidateWorkspace{}, JournalEntry{}, ErrLeaseFenced
	}
	if attribution != "user" && attribution != "agent" && attribution != "merge" && attribution != "restore" {
		return CandidateWorkspace{}, JournalEntry{}, fmt.Errorf("%w: attribution", ErrInvalidCandidate)
	}
	if candidate.Stale || candidate.RebaseRequired || (candidate.Conflicted && attribution != "merge") {
		return CandidateWorkspace{}, JournalEntry{}, ErrCandidateState
	}
	if now.Before(candidate.UpdatedAt) {
		return CandidateWorkspace{}, JournalEntry{}, fmt.Errorf("%w: operation predates candidate", ErrInvalidCandidate)
	}
	operation, err := NormalizeOperation(operation)
	if err != nil {
		return CandidateWorkspace{}, JournalEntry{}, err
	}
	nextTree, err := ApplyOperation(candidate.CurrentTree, operation)
	if err != nil {
		return CandidateWorkspace{}, JournalEntry{}, err
	}
	if nextTree.TreeHash == candidate.CurrentTree.TreeHash {
		return CandidateWorkspace{}, JournalEntry{}, fmt.Errorf("%w: operation does not change the tree", ErrTreeConflict)
	}
	next := cloneCandidate(candidate)
	next.CurrentTree = nextTree
	next.Dirty = true
	next.Version++
	next.JournalSequence++
	next.UpdatedAt = now.UTC()
	entry := JournalEntry{
		CandidateID: candidate.ID, Sequence: next.JournalSequence,
		CandidateFrom: candidate.Version, CandidateTo: next.Version,
		SessionEpoch: candidate.SessionEpoch, LeaseEpoch: leaseEpoch,
		ActorID: strings.TrimSpace(actorID), Attribution: attribution,
		Operation: operation, BeforeTree: candidate.CurrentTree.TreeHash, AfterTree: nextTree.TreeHash, CreatedAt: now.UTC(),
	}
	return next, entry, nil
}

func (candidate CandidateWorkspace) Checkpoint(
	expectedVersion, expectedSessionEpoch, expectedWriterLeaseEpoch uint64,
	id, actorID, reason string,
	now time.Time,
) (CandidateSnapshot, error) {
	if err := candidate.Validate(); err != nil {
		return CandidateSnapshot{}, err
	}
	reason = strings.TrimSpace(reason)
	if candidate.Status != CandidateActive || expectedVersion != candidate.Version || expectedSessionEpoch != candidate.SessionEpoch ||
		candidate.Lease == nil || expectedWriterLeaseEpoch != candidate.WriterLeaseEpoch ||
		candidate.Lease.Epoch != expectedWriterLeaseEpoch || candidate.Lease.OwnerID != strings.TrimSpace(actorID) ||
		now.IsZero() || !now.Before(candidate.Lease.ExpiresAt) {
		return CandidateSnapshot{}, ErrLeaseFenced
	}
	if !validUUID(id) || !validUUID(actorID) || reason == "" || len(reason) > 1000 || now.Before(candidate.UpdatedAt) {
		return CandidateSnapshot{}, fmt.Errorf("%w: invalid checkpoint", ErrInvalidCandidate)
	}
	return CandidateSnapshot{
		SchemaVersion: CandidateSnapshotSchemaVersion, ID: strings.TrimSpace(id), ProjectID: candidate.ProjectID,
		CandidateID: candidate.ID, CandidateVersion: candidate.Version, JournalSequence: candidate.JournalSequence,
		SessionEpoch: candidate.SessionEpoch, WriterLeaseEpoch: candidate.WriterLeaseEpoch,
		Tree:   cloneTree(candidate.CurrentTree),
		Reason: reason, CreatedBy: strings.TrimSpace(actorID), CreatedAt: now.UTC(),
	}, nil
}

func (candidate CandidateWorkspace) Validate() error {
	if candidate.SchemaVersion != CandidateSchemaVersion || !validUUID(candidate.ID) || !validUUID(candidate.ProjectID) ||
		!validUUID(candidate.RepositorySnapshotID) || !validUUID(candidate.CreatedBy) || candidate.Version == 0 ||
		candidate.SessionEpoch == 0 || candidate.CreatedAt.IsZero() || candidate.UpdatedAt.Before(candidate.CreatedAt) {
		return ErrInvalidCandidate
	}
	if candidate.Status != CandidateActive && candidate.Status != CandidateFrozen && candidate.Status != CandidateAbandoned {
		return ErrInvalidCandidate
	}
	if candidate.JournalSequence >= candidate.Version || candidate.SessionEpoch > candidate.Version ||
		candidate.WriterLeaseEpoch >= candidate.Version || (candidate.JournalSequence > 0 && !candidate.Dirty) {
		return ErrInvalidCandidate
	}
	if err := validateExact(candidate.BuildManifest); err != nil {
		return err
	}
	if err := validateExact(candidate.BuildContract); err != nil {
		return err
	}
	if err := validateExact(candidate.FullStackTemplate); err != nil {
		return err
	}
	if candidate.BaseWorkspaceRevision != nil {
		ref := candidate.BaseWorkspaceRevision
		if !validUUID(ref.ArtifactID) || !validUUID(ref.RevisionID) || !isCanonicalExternalHash(ref.ContentHash) {
			return ErrInvalidCandidate
		}
	}
	tree, err := ParseTree(candidate.CurrentTree)
	if err != nil || !isCanonicalSHA256(candidate.BaseTreeHash) || tree.TreeHash != candidate.CurrentTree.TreeHash {
		return ErrInvalidCandidate
	}
	if candidate.Lease != nil {
		if candidate.Status != CandidateActive || !validUUID(candidate.Lease.OwnerID) || candidate.Lease.Epoch == 0 ||
			candidate.Lease.Epoch != candidate.WriterLeaseEpoch || candidate.Lease.ExpiresAt.IsZero() {
			return ErrInvalidCandidate
		}
	}
	if candidate.JournalSequence == 0 && candidate.CurrentTree.TreeHash != candidate.BaseTreeHash {
		return ErrInvalidCandidate
	}
	return nil
}

func validateExact(ref ExactReference) error {
	if !validUUID(ref.ID) || !isCanonicalExternalHash(ref.ContentHash) {
		return fmt.Errorf("%w: exact reference", ErrInvalidCandidate)
	}
	return nil
}

func isCanonicalSHA256(value string) bool {
	normalized, err := canonicalSHA256(value)
	return err == nil && normalized == value
}

// Platform lineage hashes predate the repository package and exist in two
// canonical wire forms: raw lowercase sha256 for domain.CanonicalHash values,
// and sha256-prefixed lowercase digests for content/blob identities. Exact
// references preserve whichever form the authoritative parent row uses.
func isCanonicalExternalHash(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	normalized, err := canonicalSHA256(value)
	return err == nil && (value == normalized || value == strings.TrimPrefix(normalized, "sha256:"))
}

func validUUID(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func cloneCandidate(candidate CandidateWorkspace) CandidateWorkspace {
	next := candidate
	next.CurrentTree.Files = append([]TreeFile(nil), candidate.CurrentTree.Files...)
	next.BaseWorkspaceRevision = cloneRevisionReference(candidate.BaseWorkspaceRevision)
	if candidate.Lease != nil {
		lease := *candidate.Lease
		next.Lease = &lease
	}
	return next
}

func cloneRevisionReference(ref *ExactRevisionReference) *ExactRevisionReference {
	if ref == nil {
		return nil
	}
	cloned := *ref
	return &cloned
}

func cloneTree(tree TreeManifest) TreeManifest {
	tree.Files = append([]TreeFile(nil), tree.Files...)
	return tree
}
