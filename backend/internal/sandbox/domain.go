package sandbox

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

const SessionSchemaVersion = "sandbox-session/v1"

var (
	ErrInvalidSession           = errors.New("invalid sandbox session")
	ErrInvalidTransition        = errors.New("invalid sandbox session transition")
	ErrVersionConflict          = errors.New("sandbox session version conflict")
	ErrEpochFenced              = errors.New("sandbox session epoch is fenced")
	ErrCandidateVersionConflict = errors.New("sandbox candidate version conflict")
	ErrCandidateMismatch        = errors.New("sandbox candidate does not match the exact session lineage")
	ErrCheckpointRequired       = errors.New("exact candidate checkpoint is required")
	ErrCheckpointMismatch       = errors.New("candidate checkpoint is not exact")
	ErrActionBlocked            = errors.New("sandbox action is blocked")
	ErrImmutableSession         = errors.New("sandbox session is immutable")
)

type State string

const (
	StateProvisioning State = "provisioning"
	StateStarting     State = "starting"
	StateReady        State = "ready"
	StateSuspending   State = "suspending"
	StateSuspended    State = "suspended"
	StateResuming     State = "resuming"
	StateTerminating  State = "terminating"
	StateTerminated   State = "terminated"
	StateFailed       State = "failed"
)

func (state State) String() string { return string(state) }

type Action string

const (
	ActionView              Action = "view"
	ActionCancel            Action = "cancel"
	ActionEdit              Action = "edit"
	ActionPTY               Action = "pty"
	ActionProcess           Action = "process"
	ActionAgent             Action = "agent"
	ActionCheckpoint        Action = "checkpoint"
	ActionVerify            Action = "verify"
	ActionFreeze            Action = "freeze"
	ActionAbandon           Action = "abandon"
	ActionSuspend           Action = "suspend"
	ActionResume            Action = "resume"
	ActionTerminate         Action = "terminate"
	ActionViewLogs          Action = "view_logs"
	ActionRestoreCheckpoint Action = "restore_checkpoint"
	ActionNewSession        Action = "new_session"
	ActionViewAudit         Action = "view_audit"
	ActionViewSnapshots     Action = "view_snapshots"
)

type BlockingCode string

const (
	BlockingSessionNotReady       BlockingCode = "session_not_ready"
	BlockingSessionTransitioning  BlockingCode = "session_transition_in_progress"
	BlockingSessionSuspended      BlockingCode = "session_suspended"
	BlockingSessionFailed         BlockingCode = "session_failed"
	BlockingSessionTerminated     BlockingCode = "session_terminated"
	BlockingCandidateConflicted   BlockingCode = "candidate_conflicted"
	BlockingCandidateStale        BlockingCode = "candidate_stale"
	BlockingCandidateRebase       BlockingCode = "candidate_rebase_required"
	BlockingCandidateFrozen       BlockingCode = "candidate_frozen"
	BlockingCandidateAbandoned    BlockingCode = "candidate_abandoned"
	BlockingExactCheckpointNeeded BlockingCode = "exact_checkpoint_required"
)

type BlockingReason struct {
	Code    BlockingCode `json:"code"`
	Actions []Action     `json:"actions"`
	Detail  string       `json:"detail"`
}

type Quota struct {
	CPUMillis        int64 `json:"cpuMillis"`
	MemoryBytes      int64 `json:"memoryBytes"`
	WorkspaceBytes   int64 `json:"workspaceBytes"`
	PIDLimit         int   `json:"pidLimit"`
	PreviewPortLimit int   `json:"previewPortLimit"`
}

type TTLPolicy struct {
	IdleHibernateAfter time.Duration `json:"idleHibernateAfter"`
	MaxRuntime         time.Duration `json:"maxRuntime"`
}

type TTL struct {
	Policy       TTLPolicy `json:"policy"`
	IdleDeadline time.Time `json:"idleDeadline"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type AllowedService struct {
	ID              string                    `json:"id"`
	Kind            string                    `json:"kind"`
	Profiles        []string                  `json:"profiles"`
	TemplateRelease repository.ExactReference `json:"templateRelease"`
}

type AllowedPort struct {
	Name      string `json:"name"`
	ServiceID string `json:"serviceId"`
	Number    int    `json:"number"`
	Protocol  string `json:"protocol"`
}

type CandidateState struct {
	ID                   string                     `json:"id"`
	RepositorySnapshotID string                     `json:"repositorySnapshotId"`
	Status               repository.CandidateStatus `json:"status"`
	BaseTreeHash         string                     `json:"baseTreeHash"`
	TreeHash             string                     `json:"treeHash"`
	Version              uint64                     `json:"version"`
	JournalSequence      uint64                     `json:"journalSequence"`
	SessionEpoch         uint64                     `json:"sessionEpoch"`
	WriterLeaseEpoch     uint64                     `json:"writerLeaseEpoch"`
	Dirty                bool                       `json:"dirty"`
	Conflicted           bool                       `json:"conflicted"`
	Stale                bool                       `json:"stale"`
	RebaseRequired       bool                       `json:"rebaseRequired"`
	UpdatedAt            time.Time                  `json:"updatedAt"`
}

type CandidateCheckpointRef struct {
	ID               string `json:"id"`
	ContentHash      string `json:"contentHash"`
	CandidateID      string `json:"candidateId"`
	CandidateVersion uint64 `json:"candidateVersion"`
	JournalSequence  uint64 `json:"journalSequence"`
	SessionEpoch     uint64 `json:"sessionEpoch"`
	WriterLeaseEpoch uint64 `json:"writerLeaseEpoch"`
	TreeHash         string `json:"treeHash"`
}

type Transition struct {
	From   State     `json:"from,omitempty"`
	To     State     `json:"to"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

type NewSessionInput struct {
	ID                string
	ActorID           string
	Candidate         repository.CandidateWorkspace
	LatestCheckpoint  *repository.CandidateSnapshot
	RunnerImageDigest string
	Quota             Quota
	TTL               TTLPolicy
	Services          []AllowedService
	Ports             []AllowedPort
}

type SessionView struct {
	SchemaVersion         string                             `json:"schemaVersion"`
	ID                    string                             `json:"id"`
	ProjectID             string                             `json:"projectId"`
	ActorID               string                             `json:"actorId"`
	BuildManifest         repository.ExactReference          `json:"buildManifest"`
	BuildContract         repository.ExactReference          `json:"buildContract"`
	FullStackTemplate     repository.ExactReference          `json:"fullStackTemplate"`
	TemplateReleases      []repository.ExactReference        `json:"templateReleases"`
	BaseWorkspaceRevision *repository.ExactRevisionReference `json:"baseWorkspaceRevision,omitempty"`
	RunnerImageDigest     string                             `json:"runnerImageDigest"`
	Candidate             CandidateState                     `json:"candidate"`
	LatestCheckpoint      *CandidateCheckpointRef            `json:"latestCheckpoint,omitempty"`
	SessionEpoch          uint64                             `json:"sessionEpoch"`
	State                 State                              `json:"state"`
	Version               uint64                             `json:"version"`
	TTL                   TTL                                `json:"ttl"`
	Quota                 Quota                              `json:"quota"`
	AllowedServices       []AllowedService                   `json:"allowedServices"`
	AllowedPorts          []AllowedPort                      `json:"allowedPorts"`
	AllowedActions        []Action                           `json:"allowedActions"`
	BlockingReasons       []BlockingReason                   `json:"blockingReasons"`
	LastTransition        Transition                         `json:"lastTransition"`
	FailureReason         string                             `json:"failureReason,omitempty"`
	CreatedAt             time.Time                          `json:"createdAt"`
	UpdatedAt             time.Time                          `json:"updatedAt"`
}

type ActionError struct {
	Action  Action
	Reasons []BlockingReason
}

func (err *ActionError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("sandbox action %q is blocked", err.Action)
}

func (err *ActionError) Unwrap() error { return ErrActionBlocked }

type sandboxSessionDocument struct {
	SchemaVersion         string
	ID                    string
	ProjectID             string
	ActorID               string
	BuildManifest         repository.ExactReference
	BuildContract         repository.ExactReference
	FullStackTemplate     repository.ExactReference
	TemplateReleases      []repository.ExactReference
	BaseWorkspaceRevision *repository.ExactRevisionReference
	RunnerImageDigest     string
	Candidate             CandidateState
	LatestCheckpoint      *CandidateCheckpointRef
	SessionEpoch          uint64
	State                 State
	Version               uint64
	TTL                   TTL
	Quota                 Quota
	AllowedServices       []AllowedService
	AllowedPorts          []AllowedPort
	LastTransition        Transition
	FailureReason         string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

func (quota Quota) validate() error {
	const (
		minMemory    = int64(64 << 20)
		maxMemory    = int64(256 << 30)
		minWorkspace = int64(1 << 20)
		maxWorkspace = int64(1 << 40)
	)
	if quota.CPUMillis < 100 || quota.CPUMillis > 64_000 ||
		quota.MemoryBytes < minMemory || quota.MemoryBytes > maxMemory ||
		quota.WorkspaceBytes < minWorkspace || quota.WorkspaceBytes > maxWorkspace ||
		quota.PIDLimit < 1 || quota.PIDLimit > 32_768 ||
		quota.PreviewPortLimit < 0 || quota.PreviewPortLimit > 64 {
		return fmt.Errorf("%w: quota is outside bounded platform limits", ErrInvalidSession)
	}
	return nil
}

func (policy TTLPolicy) validate() error {
	if policy.IdleHibernateAfter <= 0 || policy.MaxRuntime <= 0 ||
		policy.IdleHibernateAfter > policy.MaxRuntime || policy.MaxRuntime > 7*24*time.Hour {
		return fmt.Errorf("%w: TTL policy is invalid or unbounded", ErrInvalidSession)
	}
	return nil
}

func normalizeServices(values []AllowedService) ([]AllowedService, error) {
	if len(values) == 0 || len(values) > 16 {
		return nil, fmt.Errorf("%w: allowed services must contain between 1 and 16 entries", ErrInvalidSession)
	}
	result := make([]AllowedService, len(values))
	seen := make(map[string]bool, len(values))
	releaseHashes := make(map[string]string, len(values))
	roleReleases := make(map[string]repository.ExactReference, len(values))
	releaseRoles := make(map[string]string, len(values))
	for index, value := range values {
		value.ID = strings.TrimSpace(value.ID)
		value.Kind = strings.TrimSpace(value.Kind)
		if !slugPattern.MatchString(value.ID) || len(value.ID) > 80 || seen[value.ID] {
			return nil, fmt.Errorf("%w: invalid or duplicate allowed service %q", ErrInvalidSession, value.ID)
		}
		if value.Kind != "web" && value.Kind != "api" && value.Kind != "worker" {
			return nil, fmt.Errorf("%w: service %s kind must be web, api, or worker", ErrInvalidSession, value.ID)
		}
		if err := validateExactReference(value.TemplateRelease); err != nil {
			return nil, fmt.Errorf("service %s: %w", value.ID, err)
		}
		if prior, exists := releaseHashes[value.TemplateRelease.ID]; exists && prior != value.TemplateRelease.ContentHash {
			return nil, fmt.Errorf("%w: template release %s has conflicting exact hashes", ErrInvalidSession, value.TemplateRelease.ID)
		}
		releaseHashes[value.TemplateRelease.ID] = value.TemplateRelease.ContentHash
		if prior, exists := roleReleases[value.Kind]; exists && prior != value.TemplateRelease {
			return nil, fmt.Errorf("%w: service role %s must use exactly one template release", ErrInvalidSession, value.Kind)
		}
		roleReleases[value.Kind] = value.TemplateRelease
		if prior, exists := releaseRoles[value.TemplateRelease.ID]; exists && prior != value.Kind {
			return nil, fmt.Errorf("%w: template release %s cannot span service roles", ErrInvalidSession, value.TemplateRelease.ID)
		}
		releaseRoles[value.TemplateRelease.ID] = value.Kind
		if len(value.Profiles) == 0 || len(value.Profiles) > 8 {
			return nil, fmt.Errorf("%w: service %s requires bounded profiles", ErrInvalidSession, value.ID)
		}
		profiles := make([]string, len(value.Profiles))
		profileSeen := make(map[string]bool, len(value.Profiles))
		for profileIndex, profile := range value.Profiles {
			profile = strings.TrimSpace(profile)
			if !slugPattern.MatchString(profile) || len(profile) > 80 || profileSeen[profile] {
				return nil, fmt.Errorf("%w: invalid or duplicate profile for service %s", ErrInvalidSession, value.ID)
			}
			profileSeen[profile] = true
			profiles[profileIndex] = profile
		}
		sort.Strings(profiles)
		value.Profiles = profiles
		seen[value.ID] = true
		result[index] = value
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func normalizePorts(values []AllowedPort, services []AllowedService, limit int) ([]AllowedPort, error) {
	if len(values) > limit {
		return nil, fmt.Errorf("%w: allowed ports exceed the session preview-port quota", ErrInvalidSession)
	}
	serviceIDs := make(map[string]bool, len(services))
	for _, service := range services {
		serviceIDs[service.ID] = true
	}
	result := make([]AllowedPort, len(values))
	names := make(map[string]bool, len(values))
	numbers := make(map[int]bool, len(values))
	for index, value := range values {
		value.Name = strings.TrimSpace(value.Name)
		value.ServiceID = strings.TrimSpace(value.ServiceID)
		value.Protocol = strings.TrimSpace(value.Protocol)
		if !slugPattern.MatchString(value.Name) || len(value.Name) > 80 || names[value.Name] || !serviceIDs[value.ServiceID] {
			return nil, fmt.Errorf("%w: invalid port name, duplicate, or service reference", ErrInvalidSession)
		}
		if value.Number < 1024 || value.Number > 65535 || numbers[value.Number] {
			return nil, fmt.Errorf("%w: port numbers must be unique and between 1024 and 65535", ErrInvalidSession)
		}
		if value.Protocol != "http" && value.Protocol != "https" && value.Protocol != "tcp" {
			return nil, fmt.Errorf("%w: port protocol must be http, https, or tcp", ErrInvalidSession)
		}
		names[value.Name] = true
		numbers[value.Number] = true
		result[index] = value
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result, nil
}

func templateReleaseReferences(services []AllowedService) []repository.ExactReference {
	byID := make(map[string]repository.ExactReference, len(services))
	for _, service := range services {
		byID[service.TemplateRelease.ID] = service.TemplateRelease
	}
	result := make([]repository.ExactReference, 0, len(byID))
	for _, ref := range byID {
		result = append(result, ref)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

func candidateState(candidate repository.CandidateWorkspace) CandidateState {
	return CandidateState{
		ID: candidate.ID, RepositorySnapshotID: candidate.RepositorySnapshotID,
		Status:       candidate.Status,
		BaseTreeHash: candidate.BaseTreeHash, TreeHash: candidate.CurrentTree.TreeHash,
		Version: candidate.Version, JournalSequence: candidate.JournalSequence,
		SessionEpoch: candidate.SessionEpoch, WriterLeaseEpoch: candidate.WriterLeaseEpoch,
		Dirty: candidate.Dirty, Conflicted: candidate.Conflicted, Stale: candidate.Stale,
		RebaseRequired: candidate.RebaseRequired, UpdatedAt: candidate.UpdatedAt.UTC(),
	}
}

func checkpointReference(snapshot repository.CandidateSnapshot) (CandidateCheckpointRef, error) {
	if snapshot.SchemaVersion != repository.CandidateSnapshotSchemaVersion || !validUUID(snapshot.ID) ||
		!validUUID(snapshot.ProjectID) || !validUUID(snapshot.CandidateID) || !validUUID(snapshot.CreatedBy) ||
		snapshot.CandidateVersion == 0 || strings.TrimSpace(snapshot.Reason) == "" || len(snapshot.Reason) > 1000 || snapshot.CreatedAt.IsZero() {
		return CandidateCheckpointRef{}, fmt.Errorf("%w: checkpoint identity is incomplete", ErrCheckpointMismatch)
	}
	tree, err := repository.ParseTree(snapshot.Tree)
	if err != nil {
		return CandidateCheckpointRef{}, fmt.Errorf("%w: %v", ErrCheckpointMismatch, err)
	}
	hash, err := domain.CanonicalHash(snapshot)
	if err != nil {
		return CandidateCheckpointRef{}, fmt.Errorf("%w: hash checkpoint: %v", ErrCheckpointMismatch, err)
	}
	return CandidateCheckpointRef{
		ID: snapshot.ID, ContentHash: "sha256:" + hash, CandidateID: snapshot.CandidateID,
		CandidateVersion: snapshot.CandidateVersion, JournalSequence: snapshot.JournalSequence,
		SessionEpoch: snapshot.SessionEpoch, WriterLeaseEpoch: snapshot.WriterLeaseEpoch, TreeHash: tree.TreeHash,
	}, nil
}

func validateExactReference(ref repository.ExactReference) error {
	if !validUUID(ref.ID) || !validExternalDigest(ref.ContentHash) {
		return fmt.Errorf("%w: exact reference is invalid", ErrInvalidSession)
	}
	return nil
}

func validateExactRevision(ref repository.ExactRevisionReference) error {
	if !validUUID(ref.ArtifactID) || !validUUID(ref.RevisionID) || !validExternalDigest(ref.ContentHash) {
		return fmt.Errorf("%w: exact workspace revision is invalid", ErrInvalidSession)
	}
	return nil
}

func validUUID(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	_, err := uuid.Parse(value)
	return err == nil
}

func validDigest(value string) bool {
	return len(value) == 71 && strings.HasPrefix(value, "sha256:") && domain.IsCanonicalHash(value) && value == strings.ToLower(value)
}

func validExternalDigest(value string) bool {
	return (len(value) == 64 || len(value) == 71) && value == strings.TrimSpace(value) &&
		value == strings.ToLower(value) && domain.IsCanonicalHash(value)
}

func cloneServices(values []AllowedService) []AllowedService {
	result := make([]AllowedService, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Profiles = append([]string(nil), value.Profiles...)
	}
	return result
}

func clonePorts(values []AllowedPort) []AllowedPort {
	result := make([]AllowedPort, len(values))
	copy(result, values)
	return result
}

func cloneCheckpoint(value *CandidateCheckpointRef) *CandidateCheckpointRef {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneBlockingReasons(values []BlockingReason) []BlockingReason {
	result := make([]BlockingReason, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Actions = append([]Action(nil), value.Actions...)
	}
	return result
}
