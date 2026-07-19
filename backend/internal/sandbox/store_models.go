package sandbox

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
	"gorm.io/gorm"
)

type sandboxSessionRow struct {
	ID                       string         `gorm:"column:id"`
	SchemaVersion            string         `gorm:"column:schema_version"`
	ProjectID                string         `gorm:"column:project_id"`
	ActorID                  string         `gorm:"column:actor_id"`
	CandidateID              string         `gorm:"column:candidate_id"`
	RepositorySnapshotID     string         `gorm:"column:repository_snapshot_id"`
	BuildManifestID          string         `gorm:"column:build_manifest_id"`
	BuildManifestHash        string         `gorm:"column:build_manifest_hash"`
	BuildContractID          string         `gorm:"column:build_contract_id"`
	BuildContractHash        string         `gorm:"column:build_contract_hash"`
	FullStackTemplateID      string         `gorm:"column:full_stack_template_id"`
	FullStackTemplateHash    string         `gorm:"column:full_stack_template_hash"`
	BaseWorkspaceArtifactID  sql.NullString `gorm:"column:base_workspace_artifact_id"`
	BaseWorkspaceRevisionID  sql.NullString `gorm:"column:base_workspace_revision_id"`
	BaseWorkspaceContentHash sql.NullString `gorm:"column:base_workspace_content_hash"`
	RunnerImageDigest        string         `gorm:"column:runner_image_digest"`
	CandidateVersion         int64          `gorm:"column:candidate_version"`
	CandidateJournalSequence int64          `gorm:"column:candidate_journal_sequence"`
	CandidateSessionEpoch    int64          `gorm:"column:candidate_session_epoch"`
	CandidateWriterEpoch     int64          `gorm:"column:candidate_writer_lease_epoch"`
	CandidateTreeStore       string         `gorm:"column:candidate_tree_store"`
	CandidateTreeOwnerID     string         `gorm:"column:candidate_tree_owner_id"`
	CandidateTreeRef         string         `gorm:"column:candidate_tree_ref"`
	CandidateTreeContentHash string         `gorm:"column:candidate_tree_content_hash"`
	CandidateTreeHash        string         `gorm:"column:candidate_tree_hash"`
	CandidateDirty           bool           `gorm:"column:candidate_dirty"`
	CandidateConflicted      bool           `gorm:"column:candidate_conflicted"`
	CandidateStale           bool           `gorm:"column:candidate_stale"`
	CandidateRebaseRequired  bool           `gorm:"column:candidate_rebase_required"`
	LatestCheckpointID       sql.NullString `gorm:"column:latest_checkpoint_id"`
	State                    string         `gorm:"column:state"`
	Version                  int64          `gorm:"column:version"`
	SessionEpoch             int64          `gorm:"column:session_epoch"`
	CPUMillis                int64          `gorm:"column:cpu_millis"`
	MemoryBytes              int64          `gorm:"column:memory_bytes"`
	WorkspaceBytes           int64          `gorm:"column:workspace_bytes"`
	PIDLimit                 int            `gorm:"column:pid_limit"`
	PreviewPortLimit         int            `gorm:"column:preview_port_limit"`
	IdleHibernateSeconds     int64          `gorm:"column:idle_hibernate_seconds"`
	MaxRuntimeSeconds        int64          `gorm:"column:max_runtime_seconds"`
	IdleDeadline             time.Time      `gorm:"column:idle_deadline"`
	ExpiresAt                time.Time      `gorm:"column:expires_at"`
	FailureReason            sql.NullString `gorm:"column:failure_reason"`
	CreatedAt                time.Time      `gorm:"column:created_at"`
	UpdatedAt                time.Time      `gorm:"column:updated_at"`
}

func (sandboxSessionRow) TableName() string { return "sandbox_sessions" }

type sandboxSessionActivityRow struct {
	SessionID      string    `gorm:"column:session_id"`
	SessionEpoch   int64     `gorm:"column:session_epoch"`
	LastActivityAt time.Time `gorm:"column:last_activity_at"`
	IdleDeadline   time.Time `gorm:"column:idle_deadline"`
}

func (sandboxSessionActivityRow) TableName() string { return "sandbox_session_activity" }

type candidateStoreRow struct {
	ID                       string         `gorm:"column:id"`
	SchemaVersion            string         `gorm:"column:schema_version"`
	ProjectID                string         `gorm:"column:project_id"`
	RepositorySnapshotID     string         `gorm:"column:repository_snapshot_id"`
	BuildManifestID          string         `gorm:"column:build_manifest_id"`
	BuildManifestHash        string         `gorm:"column:build_manifest_hash"`
	BuildContractID          string         `gorm:"column:build_contract_id"`
	BuildContractHash        string         `gorm:"column:build_contract_hash"`
	FullStackTemplateID      string         `gorm:"column:full_stack_template_id"`
	FullStackTemplateHash    string         `gorm:"column:full_stack_template_hash"`
	BaseWorkspaceArtifactID  sql.NullString `gorm:"column:base_workspace_artifact_id"`
	BaseWorkspaceRevisionID  sql.NullString `gorm:"column:base_workspace_revision_id"`
	BaseWorkspaceContentHash sql.NullString `gorm:"column:base_workspace_content_hash"`
	BaseTreeStore            string         `gorm:"column:base_tree_store"`
	BaseTreeOwnerID          string         `gorm:"column:base_tree_owner_id"`
	BaseTreeRef              string         `gorm:"column:base_tree_ref"`
	BaseTreeContentHash      string         `gorm:"column:base_tree_content_hash"`
	BaseTreeHash             string         `gorm:"column:base_tree_hash"`
	CurrentTreeStore         string         `gorm:"column:current_tree_store"`
	CurrentTreeOwnerID       string         `gorm:"column:current_tree_owner_id"`
	CurrentTreeRef           string         `gorm:"column:current_tree_ref"`
	CurrentTreeContentHash   string         `gorm:"column:current_tree_content_hash"`
	CurrentTreeHash          string         `gorm:"column:current_tree_hash"`
	CurrentTreeFileCount     int            `gorm:"column:current_tree_file_count"`
	CurrentTreeByteSize      int64          `gorm:"column:current_tree_byte_size"`
	Status                   string         `gorm:"column:status"`
	Dirty                    bool           `gorm:"column:dirty"`
	Conflicted               bool           `gorm:"column:conflicted"`
	Stale                    bool           `gorm:"column:stale"`
	RebaseRequired           bool           `gorm:"column:rebase_required"`
	SessionEpoch             int64          `gorm:"column:session_epoch"`
	Version                  int64          `gorm:"column:version"`
	JournalSequence          int64          `gorm:"column:journal_sequence"`
	WriterLeaseOwnerID       sql.NullString `gorm:"column:writer_lease_owner_id"`
	WriterLeaseEpoch         int64          `gorm:"column:writer_lease_epoch"`
	WriterLeaseExpiresAt     sql.NullTime   `gorm:"column:writer_lease_expires_at"`
	CreatedBy                string         `gorm:"column:created_by"`
	CreatedAt                time.Time      `gorm:"column:created_at"`
	UpdatedAt                time.Time      `gorm:"column:updated_at"`
}

func (candidateStoreRow) TableName() string { return "candidate_workspaces" }

type sandboxTemplateReleaseRow struct {
	Ordinal                    int    `gorm:"column:ordinal"`
	Role                       string `gorm:"column:role"`
	TemplateReleaseID          string `gorm:"column:template_release_id"`
	TemplateReleaseContentHash string `gorm:"column:template_release_content_hash"`
}

type sandboxServiceRow struct {
	ServiceID                  string `gorm:"column:service_id"`
	Kind                       string `gorm:"column:kind"`
	Profiles                   []byte `gorm:"column:profiles"`
	TemplateReleaseID          string `gorm:"column:template_release_id"`
	TemplateReleaseContentHash string `gorm:"column:template_release_content_hash"`
}

type sandboxPortRow struct {
	PortName  string `gorm:"column:port_name"`
	ServiceID string `gorm:"column:service_id"`
	Number    int    `gorm:"column:port_number"`
	Protocol  string `gorm:"column:protocol"`
}

type sandboxCheckpointRow struct {
	ID               string    `gorm:"column:id"`
	ProjectID        string    `gorm:"column:project_id"`
	CandidateID      string    `gorm:"column:candidate_id"`
	CandidateVersion int64     `gorm:"column:candidate_version"`
	JournalSequence  int64     `gorm:"column:journal_sequence"`
	SessionEpoch     int64     `gorm:"column:session_epoch"`
	WriterLeaseEpoch int64     `gorm:"column:writer_lease_epoch"`
	TreeStore        string    `gorm:"column:tree_store"`
	TreeOwnerID      string    `gorm:"column:tree_owner_id"`
	TreeRef          string    `gorm:"column:tree_ref"`
	TreeContentHash  string    `gorm:"column:tree_content_hash"`
	TreeHash         string    `gorm:"column:tree_hash"`
	TreeFileCount    int       `gorm:"column:tree_file_count"`
	TreeByteSize     int64     `gorm:"column:tree_byte_size"`
	Reason           string    `gorm:"column:reason"`
	CreatedBy        string    `gorm:"column:created_by"`
	CreatedAt        time.Time `gorm:"column:created_at"`
}

type sandboxEventStats struct {
	Count       int64         `gorm:"column:event_count"`
	MinSequence sql.NullInt64 `gorm:"column:min_sequence"`
	MaxSequence sql.NullInt64 `gorm:"column:max_sequence"`
}

type sandboxEventTail struct {
	Sequence                   int64          `gorm:"column:sequence"`
	SessionVersionTo           int64          `gorm:"column:session_version_to"`
	StateFrom                  string         `gorm:"column:state_from"`
	StateTo                    string         `gorm:"column:state_to"`
	SessionEpochTo             int64          `gorm:"column:session_epoch_to"`
	Reason                     string         `gorm:"column:reason"`
	CandidateVersionTo         int64          `gorm:"column:candidate_version_to"`
	CandidateJournalSequenceTo int64          `gorm:"column:candidate_journal_sequence_to"`
	CandidateSessionEpochTo    int64          `gorm:"column:candidate_session_epoch_to"`
	CandidateWriterEpochTo     int64          `gorm:"column:candidate_writer_lease_epoch_to"`
	CandidateTreeStoreTo       string         `gorm:"column:candidate_tree_store_to"`
	CandidateTreeOwnerIDTo     string         `gorm:"column:candidate_tree_owner_id_to"`
	CandidateTreeRefTo         string         `gorm:"column:candidate_tree_ref_to"`
	CandidateTreeContentHashTo string         `gorm:"column:candidate_tree_content_hash_to"`
	CandidateTreeHashTo        string         `gorm:"column:candidate_tree_hash_to"`
	CandidateDirtyTo           bool           `gorm:"column:candidate_dirty_to"`
	CandidateConflictedTo      bool           `gorm:"column:candidate_conflicted_to"`
	CandidateStaleTo           bool           `gorm:"column:candidate_stale_to"`
	CandidateRebaseRequiredTo  bool           `gorm:"column:candidate_rebase_required_to"`
	LatestCheckpointIDTo       sql.NullString `gorm:"column:latest_checkpoint_id_to"`
	FailureReasonTo            sql.NullString `gorm:"column:failure_reason_to"`
	CreatedAt                  time.Time      `gorm:"column:created_at"`
}

func hydrateSandboxSession(database *gorm.DB, row sandboxSessionRow) (SandboxSession, error) {
	var activity sandboxSessionActivityRow
	activityQuery := database.Where("session_id = ?", row.ID).Take(&activity)
	if activityQuery.Error != nil {
		return SandboxSession{}, integrityError("load SandboxSession activity", activityQuery.Error)
	}
	if activity.SessionID != row.ID || activity.SessionEpoch != row.SessionEpoch ||
		activity.LastActivityAt.Before(row.UpdatedAt) || activity.IdleDeadline.Before(activity.LastActivityAt) ||
		activity.IdleDeadline.After(row.ExpiresAt) {
		return SandboxSession{}, integrityError("validate SandboxSession activity", ErrStoreIntegrity)
	}
	row.IdleDeadline = activity.IdleDeadline.UTC()
	if err := validatePersistedSessionRow(row); err != nil {
		return SandboxSession{}, err
	}

	var candidate candidateStoreRow
	query := database.Where("id = ? AND project_id = ?", row.CandidateID, row.ProjectID).Take(&candidate)
	if query.Error != nil {
		return SandboxSession{}, integrityError("load exact Candidate lineage", query.Error)
	}
	if err := validatePersistedCandidateLineage(row, candidate); err != nil {
		return SandboxSession{}, err
	}

	releases, services, ports, err := loadSandboxConfiguration(database, row)
	if err != nil {
		return SandboxSession{}, err
	}
	checkpoint, err := loadSandboxCheckpoint(database, row)
	if err != nil {
		return SandboxSession{}, err
	}
	lastTransition, err := loadSandboxLastTransition(database, row)
	if err != nil {
		return SandboxSession{}, err
	}

	document := sandboxSessionDocument{
		SchemaVersion: row.SchemaVersion,
		ID:            row.ID,
		ProjectID:     row.ProjectID,
		ActorID:       row.ActorID,
		BuildManifest: repository.ExactReference{
			ID: row.BuildManifestID, ContentHash: row.BuildManifestHash,
		},
		BuildContract: repository.ExactReference{
			ID: row.BuildContractID, ContentHash: row.BuildContractHash,
		},
		FullStackTemplate: repository.ExactReference{
			ID: row.FullStackTemplateID, ContentHash: row.FullStackTemplateHash,
		},
		TemplateReleases:      releases,
		BaseWorkspaceRevision: sandboxBaseWorkspaceRevision(row),
		RunnerImageDigest:     row.RunnerImageDigest,
		Candidate: CandidateState{
			ID: row.CandidateID, RepositorySnapshotID: row.RepositorySnapshotID,
			Status:       repository.CandidateStatus(candidate.Status),
			BaseTreeHash: candidate.BaseTreeHash, TreeHash: row.CandidateTreeHash,
			Version: uint64(row.CandidateVersion), JournalSequence: uint64(row.CandidateJournalSequence),
			SessionEpoch: uint64(row.CandidateSessionEpoch), WriterLeaseEpoch: uint64(row.CandidateWriterEpoch),
			Dirty: row.CandidateDirty, Conflicted: row.CandidateConflicted,
			Stale: row.CandidateStale, RebaseRequired: row.CandidateRebaseRequired,
			// Candidate updated_at is mutable beyond this session projection. The
			// append-only session event timestamp is the exact persisted projection time.
			UpdatedAt: row.UpdatedAt.UTC(),
		},
		LatestCheckpoint: checkpoint,
		SessionEpoch:     uint64(row.SessionEpoch),
		State:            State(row.State),
		Version:          uint64(row.Version),
		TTL: TTL{
			Policy: TTLPolicy{
				IdleHibernateAfter: time.Duration(row.IdleHibernateSeconds) * time.Second,
				MaxRuntime:         time.Duration(row.MaxRuntimeSeconds) * time.Second,
			},
			IdleDeadline: row.IdleDeadline.UTC(), ExpiresAt: row.ExpiresAt.UTC(),
		},
		Quota: Quota{
			CPUMillis: row.CPUMillis, MemoryBytes: row.MemoryBytes,
			WorkspaceBytes: row.WorkspaceBytes, PIDLimit: row.PIDLimit,
			PreviewPortLimit: row.PreviewPortLimit,
		},
		AllowedServices: services,
		AllowedPorts:    ports,
		LastTransition:  lastTransition,
		CreatedAt:       row.CreatedAt.UTC(),
		UpdatedAt:       row.UpdatedAt.UTC(),
	}
	if row.FailureReason.Valid {
		document.FailureReason = row.FailureReason.String
	}
	if err := validateDocument(document); err != nil {
		return SandboxSession{}, integrityError("validate hydrated SandboxSession", err)
	}
	return SandboxSession{document: cloneDocument(document)}, nil
}

func loadSandboxConfiguration(
	database *gorm.DB,
	row sandboxSessionRow,
) ([]repository.ExactReference, []AllowedService, []AllowedPort, error) {
	var releaseRows []sandboxTemplateReleaseRow
	query := database.Raw(`
SELECT ordinal, role, template_release_id, template_release_content_hash
FROM sandbox_session_template_releases
WHERE session_id = ?
ORDER BY ordinal
`, row.ID).Scan(&releaseRows)
	if query.Error != nil {
		return nil, nil, nil, integrityError("load TemplateRelease projection", query.Error)
	}
	if len(releaseRows) == 0 || len(releaseRows) > 3 {
		return nil, nil, nil, integrityError("load TemplateRelease projection", fmt.Errorf("invalid release count %d", len(releaseRows)))
	}

	roleRelease := make(map[string]repository.ExactReference, len(releaseRows))
	releaseRole := make(map[string]string, len(releaseRows))
	for _, value := range releaseRows {
		ref := repository.ExactReference{ID: value.TemplateReleaseID, ContentHash: value.TemplateReleaseContentHash}
		if (value.Role != "web" && value.Role != "api" && value.Role != "worker") || validateExactReference(ref) != nil {
			return nil, nil, nil, integrityError("load TemplateRelease projection", ErrInvalidSession)
		}
		if _, exists := roleRelease[value.Role]; exists {
			return nil, nil, nil, integrityError("load TemplateRelease projection", fmt.Errorf("duplicate role %q", value.Role))
		}
		if priorRole, exists := releaseRole[value.TemplateReleaseID]; exists && priorRole != value.Role {
			return nil, nil, nil, integrityError("load TemplateRelease projection", fmt.Errorf("release %s spans roles", value.TemplateReleaseID))
		}
		roleRelease[value.Role] = ref
		releaseRole[value.TemplateReleaseID] = value.Role
	}

	var serviceRows []sandboxServiceRow
	query = database.Raw(`
SELECT service_id, kind, profiles, template_release_id, template_release_content_hash
FROM sandbox_session_services
WHERE session_id = ?
ORDER BY service_id
`, row.ID).Scan(&serviceRows)
	if query.Error != nil {
		return nil, nil, nil, integrityError("load service projection", query.Error)
	}
	services := make([]AllowedService, len(serviceRows))
	coveredRoles := make(map[string]bool, len(roleRelease))
	for index, value := range serviceRows {
		var profiles []string
		if err := json.Unmarshal(value.Profiles, &profiles); err != nil {
			return nil, nil, nil, integrityError("decode service profiles", err)
		}
		ref := repository.ExactReference{ID: value.TemplateReleaseID, ContentHash: value.TemplateReleaseContentHash}
		selected, exists := roleRelease[value.Kind]
		if !exists || selected != ref {
			return nil, nil, nil, integrityError("load service projection", fmt.Errorf("service %s differs from selected role", value.ServiceID))
		}
		coveredRoles[value.Kind] = true
		services[index] = AllowedService{
			ID: value.ServiceID, Kind: value.Kind, Profiles: profiles, TemplateRelease: ref,
		}
	}
	normalizedServices, err := normalizeServices(services)
	if err != nil || len(coveredRoles) != len(roleRelease) {
		if err == nil {
			err = fmt.Errorf("not every selected role has a service")
		}
		return nil, nil, nil, integrityError("validate service projection", err)
	}
	if !servicesEqual(normalizedServices, services) {
		return nil, nil, nil, integrityError("validate service projection", fmt.Errorf("services are not canonical"))
	}

	var portRows []sandboxPortRow
	query = database.Raw(`
SELECT port_name, service_id, port_number, protocol
FROM sandbox_session_ports
WHERE session_id = ?
ORDER BY port_name
`, row.ID).Scan(&portRows)
	if query.Error != nil {
		return nil, nil, nil, integrityError("load port projection", query.Error)
	}
	ports := make([]AllowedPort, len(portRows))
	for index, value := range portRows {
		ports[index] = AllowedPort{
			Name: value.PortName, ServiceID: value.ServiceID, Number: value.Number, Protocol: value.Protocol,
		}
	}
	normalizedPorts, err := normalizePorts(ports, services, row.PreviewPortLimit)
	if err != nil || !portsEqual(normalizedPorts, ports) {
		if err == nil {
			err = fmt.Errorf("ports are not canonical")
		}
		return nil, nil, nil, integrityError("validate port projection", err)
	}

	releases := templateReleaseReferences(services)
	if len(releases) != len(releaseRows) {
		return nil, nil, nil, integrityError("validate TemplateRelease projection", fmt.Errorf("release set differs from services"))
	}
	return releases, services, ports, nil
}

func loadSandboxCheckpoint(database *gorm.DB, row sandboxSessionRow) (*CandidateCheckpointRef, error) {
	if !row.LatestCheckpointID.Valid {
		return nil, nil
	}
	var checkpoint sandboxCheckpointRow
	query := database.Raw(`
SELECT id, project_id, candidate_id, candidate_version, journal_sequence,
       session_epoch, writer_lease_epoch, tree_store, tree_owner_id, tree_ref,
       tree_content_hash, tree_hash, tree_file_count, tree_byte_size,
       reason, created_by, created_at
FROM candidate_snapshots
WHERE id = ? AND project_id = ? AND candidate_id = ?
`, row.LatestCheckpointID.String, row.ProjectID, row.CandidateID).Scan(&checkpoint)
	if query.Error != nil {
		return nil, integrityError("load CandidateSnapshot", query.Error)
	}
	if query.RowsAffected != 1 || checkpoint.ID == "" ||
		checkpoint.CandidateVersion <= 0 || checkpoint.JournalSequence < 0 ||
		checkpoint.SessionEpoch <= 0 || checkpoint.WriterLeaseEpoch < 0 ||
		!validUUID(checkpoint.ID) || !validUUID(checkpoint.ProjectID) ||
		!validUUID(checkpoint.CandidateID) || !validUUID(checkpoint.TreeOwnerID) ||
		!validUUID(checkpoint.CreatedBy) || strings.TrimSpace(checkpoint.Reason) == "" ||
		strings.TrimSpace(checkpoint.TreeStore) == "" || strings.TrimSpace(checkpoint.TreeRef) == "" ||
		!validDigest(checkpoint.TreeContentHash) || !validDigest(checkpoint.TreeHash) ||
		checkpoint.TreeFileCount < 0 || checkpoint.TreeByteSize < 0 || checkpoint.CreatedAt.IsZero() {
		return nil, integrityError("load CandidateSnapshot", ErrCheckpointMismatch)
	}
	if checkpoint.CandidateVersion == row.CandidateVersion &&
		(checkpoint.JournalSequence != row.CandidateJournalSequence ||
			checkpoint.SessionEpoch != row.CandidateSessionEpoch ||
			checkpoint.WriterLeaseEpoch != row.CandidateWriterEpoch ||
			checkpoint.TreeStore != row.CandidateTreeStore ||
			checkpoint.TreeOwnerID != row.CandidateTreeOwnerID ||
			checkpoint.TreeRef != row.CandidateTreeRef ||
			checkpoint.TreeContentHash != row.CandidateTreeContentHash ||
			checkpoint.TreeHash != row.CandidateTreeHash) {
		return nil, integrityError("load CandidateSnapshot", ErrCheckpointMismatch)
	}
	return &CandidateCheckpointRef{
		ID: checkpoint.ID, ContentHash: checkpoint.TreeContentHash,
		CandidateID: checkpoint.CandidateID, CandidateVersion: uint64(checkpoint.CandidateVersion),
		JournalSequence: uint64(checkpoint.JournalSequence), SessionEpoch: uint64(checkpoint.SessionEpoch),
		WriterLeaseEpoch: uint64(checkpoint.WriterLeaseEpoch), TreeHash: checkpoint.TreeHash,
	}, nil
}

func loadSandboxLastTransition(database *gorm.DB, row sandboxSessionRow) (Transition, error) {
	var stats sandboxEventStats
	query := database.Raw(`
SELECT count(*) AS event_count, min(sequence) AS min_sequence, max(sequence) AS max_sequence
FROM sandbox_session_transition_events
WHERE session_id = ?
`, row.ID).Scan(&stats)
	if query.Error != nil {
		return Transition{}, integrityError("load event projection", query.Error)
	}
	if row.Version == 1 {
		if stats.Count != 0 {
			return Transition{}, integrityError("load event projection", fmt.Errorf("initial session has events"))
		}
		return Transition{To: StateProvisioning, Reason: "created", At: row.CreatedAt.UTC()}, nil
	}
	if stats.Count != row.Version-1 || !stats.MinSequence.Valid || !stats.MaxSequence.Valid ||
		stats.MinSequence.Int64 != 1 || stats.MaxSequence.Int64 != stats.Count {
		return Transition{}, integrityError("load event projection", fmt.Errorf("event sequence is not contiguous"))
	}

	var tail sandboxEventTail
	query = database.Raw(`
SELECT sequence, session_version_to, state_from, state_to, session_epoch_to, reason,
       candidate_version_to, candidate_journal_sequence_to,
       candidate_session_epoch_to, candidate_writer_lease_epoch_to,
       candidate_tree_store_to, candidate_tree_owner_id_to, candidate_tree_ref_to,
       candidate_tree_content_hash_to, candidate_tree_hash_to,
       candidate_dirty_to, candidate_conflicted_to, candidate_stale_to,
       candidate_rebase_required_to, latest_checkpoint_id_to, failure_reason_to, created_at
FROM sandbox_session_transition_events
WHERE session_id = ?
ORDER BY sequence DESC
LIMIT 1
`, row.ID).Scan(&tail)
	if query.Error != nil || query.RowsAffected != 1 {
		if query.Error == nil {
			query.Error = gorm.ErrRecordNotFound
		}
		return Transition{}, integrityError("load event tail", query.Error)
	}
	if tail.Sequence != row.Version-1 || tail.SessionVersionTo != row.Version ||
		tail.StateTo != row.State || tail.SessionEpochTo != row.SessionEpoch ||
		tail.CandidateVersionTo != row.CandidateVersion ||
		tail.CandidateJournalSequenceTo != row.CandidateJournalSequence ||
		tail.CandidateSessionEpochTo != row.CandidateSessionEpoch ||
		tail.CandidateWriterEpochTo != row.CandidateWriterEpoch ||
		tail.CandidateTreeStoreTo != row.CandidateTreeStore ||
		tail.CandidateTreeOwnerIDTo != row.CandidateTreeOwnerID ||
		tail.CandidateTreeRefTo != row.CandidateTreeRef ||
		tail.CandidateTreeContentHashTo != row.CandidateTreeContentHash ||
		tail.CandidateTreeHashTo != row.CandidateTreeHash ||
		tail.CandidateDirtyTo != row.CandidateDirty ||
		tail.CandidateConflictedTo != row.CandidateConflicted ||
		tail.CandidateStaleTo != row.CandidateStale ||
		tail.CandidateRebaseRequiredTo != row.CandidateRebaseRequired ||
		!nullStringsEqual(tail.LatestCheckpointIDTo, row.LatestCheckpointID) ||
		!nullStringsEqual(tail.FailureReasonTo, row.FailureReason) ||
		tail.CreatedAt.UTC() != row.UpdatedAt.UTC() {
		return Transition{}, integrityError("load event tail", fmt.Errorf("event tail differs from parent projection"))
	}
	return Transition{
		From: State(tail.StateFrom), To: State(tail.StateTo), Reason: tail.Reason, At: tail.CreatedAt.UTC(),
	}, nil
}

func validatePersistedSessionRow(row sandboxSessionRow) error {
	if row.CandidateVersion <= 0 || row.CandidateJournalSequence < 0 || row.CandidateSessionEpoch <= 0 ||
		row.CandidateWriterEpoch < 0 || row.Version <= 0 || row.SessionEpoch <= 0 ||
		row.IdleHibernateSeconds <= 0 || row.MaxRuntimeSeconds <= 0 ||
		row.CandidateSessionEpoch != row.SessionEpoch ||
		!validUUID(row.CandidateTreeOwnerID) || strings.TrimSpace(row.CandidateTreeStore) == "" ||
		strings.TrimSpace(row.CandidateTreeRef) == "" || !validDigest(row.CandidateTreeContentHash) ||
		!nullableSandboxBaseShape(row) {
		return integrityError("validate SandboxSession row", ErrInvalidSession)
	}
	return nil
}

func validatePersistedCandidateLineage(row sandboxSessionRow, candidate candidateStoreRow) error {
	if candidate.ID != row.CandidateID || candidate.ProjectID != row.ProjectID ||
		candidate.RepositorySnapshotID != row.RepositorySnapshotID ||
		candidate.BuildManifestID != row.BuildManifestID || candidate.BuildManifestHash != row.BuildManifestHash ||
		candidate.BuildContractID != row.BuildContractID || candidate.BuildContractHash != row.BuildContractHash ||
		candidate.FullStackTemplateID != row.FullStackTemplateID || candidate.FullStackTemplateHash != row.FullStackTemplateHash ||
		!nullStringsEqual(candidate.BaseWorkspaceArtifactID, row.BaseWorkspaceArtifactID) ||
		!nullStringsEqual(candidate.BaseWorkspaceRevisionID, row.BaseWorkspaceRevisionID) ||
		!nullStringsEqual(candidate.BaseWorkspaceContentHash, row.BaseWorkspaceContentHash) ||
		!validDigest(candidate.BaseTreeHash) || !validUUID(candidate.BaseTreeOwnerID) ||
		strings.TrimSpace(candidate.BaseTreeStore) == "" || strings.TrimSpace(candidate.BaseTreeRef) == "" ||
		!validDigest(candidate.BaseTreeContentHash) {
		return integrityError("validate Candidate lineage", ErrCandidateMismatch)
	}
	return nil
}

func nullableSandboxBaseShape(row sandboxSessionRow) bool {
	valid := row.BaseWorkspaceArtifactID.Valid
	return row.BaseWorkspaceRevisionID.Valid == valid && row.BaseWorkspaceContentHash.Valid == valid
}

func sandboxBaseWorkspaceRevision(row sandboxSessionRow) *repository.ExactRevisionReference {
	if !nullableSandboxBaseShape(row) || !row.BaseWorkspaceArtifactID.Valid {
		return nil
	}
	return &repository.ExactRevisionReference{
		ArtifactID:  row.BaseWorkspaceArtifactID.String,
		RevisionID:  row.BaseWorkspaceRevisionID.String,
		ContentHash: row.BaseWorkspaceContentHash.String,
	}
}

func nullStringsEqual(left, right sql.NullString) bool {
	return left.Valid == right.Valid && (!left.Valid || left.String == right.String)
}

func servicesEqual(left, right []AllowedService) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID || left[index].Kind != right[index].Kind ||
			left[index].TemplateRelease != right[index].TemplateRelease ||
			len(left[index].Profiles) != len(right[index].Profiles) {
			return false
		}
		for profileIndex := range left[index].Profiles {
			if left[index].Profiles[profileIndex] != right[index].Profiles[profileIndex] {
				return false
			}
		}
	}
	return true
}

func portsEqual(left, right []AllowedPort) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortedReleaseRows(values []sandboxTemplateReleaseRow) []sandboxTemplateReleaseRow {
	result := append([]sandboxTemplateReleaseRow(nil), values...)
	sort.Slice(result, func(left, right int) bool { return result[left].Ordinal < result[right].Ordinal })
	return result
}
