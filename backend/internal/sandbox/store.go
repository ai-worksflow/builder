package sandbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrSessionNotFound  = errors.New("sandbox session was not found")
	ErrSessionExists    = errors.New("sandbox session already exists")
	ErrStoreIntegrity   = errors.New("sandbox session persistence integrity failure")
	ErrStoreUnavailable = errors.New("sandbox session persistence is unavailable")
)

// Store is the PostgreSQL source of truth for immutable SandboxSession
// configuration and its append-only lifecycle projection.
type Store struct {
	database *gorm.DB
}

func NewStore(database *gorm.DB) (*Store, error) {
	if database == nil {
		return nil, fmt.Errorf("%w: database is required", ErrStoreUnavailable)
	}
	return &Store{database: database}, nil
}

// Create validates the full domain aggregate, then seals the parent, exact
// BuildContract TemplateRelease selection, services, and ports in one
// serializable transaction. PostgreSQL derives authoritative timestamps and
// enforces the deferred configuration-completeness check at commit.
func (store *Store) Create(
	ctx context.Context,
	input NewSessionInput,
	now time.Time,
) (SandboxSession, error) {
	if err := validateStoreContext(ctx); err != nil {
		return SandboxSession{}, err
	}
	prepared, err := NewSession(input, now)
	if err != nil {
		return SandboxSession{}, err
	}
	view := prepared.Snapshot()
	idleSeconds, err := durationSeconds(view.TTL.Policy.IdleHibernateAfter)
	if err != nil {
		return SandboxSession{}, err
	}
	maxSeconds, err := durationSeconds(view.TTL.Policy.MaxRuntime)
	if err != nil {
		return SandboxSession{}, err
	}

	var created SandboxSession
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		candidate, loadErr := loadCandidateForCreate(transaction, input.Candidate.ProjectID, input.Candidate.ID)
		if loadErr != nil {
			return loadErr
		}
		if validateErr := validateCandidateForCreate(input.Candidate, candidate); validateErr != nil {
			return validateErr
		}
		if input.LatestCheckpoint != nil {
			if checkpointErr := validateInitialCheckpointForCreate(transaction, *input.LatestCheckpoint, candidate); checkpointErr != nil {
				return checkpointErr
			}
		}

		selected, selectedErr := loadSelectedTemplateReleases(transaction, candidate)
		if selectedErr != nil {
			return selectedErr
		}
		if bindingErr := validateSelectedReleaseBindings(selected, view.AllowedServices); bindingErr != nil {
			return bindingErr
		}

		var checkpointID any
		if input.LatestCheckpoint != nil {
			checkpointID = input.LatestCheckpoint.ID
		}
		insert := transaction.Exec(`
INSERT INTO sandbox_sessions (
  id, schema_version, project_id, actor_id, candidate_id, repository_snapshot_id,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  runner_image_digest,
  candidate_version, candidate_journal_sequence,
  candidate_session_epoch, candidate_writer_lease_epoch,
  candidate_tree_store, candidate_tree_owner_id, candidate_tree_ref,
  candidate_tree_content_hash, candidate_tree_hash,
  candidate_dirty, candidate_conflicted, candidate_stale, candidate_rebase_required,
  latest_checkpoint_id, state, version, session_epoch,
  cpu_millis, memory_bytes, workspace_bytes, pid_limit, preview_port_limit,
  idle_hibernate_seconds, max_runtime_seconds
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'provisioning', 1, ?,
  ?, ?, ?, ?, ?, ?, ?
)
`,
			view.ID, SessionSchemaVersion, candidate.ProjectID, view.ActorID,
			candidate.ID, candidate.RepositorySnapshotID,
			candidate.BuildManifestID, candidate.BuildManifestHash,
			candidate.BuildContractID, candidate.BuildContractHash,
			candidate.FullStackTemplateID, candidate.FullStackTemplateHash,
			nullStringValue(candidate.BaseWorkspaceArtifactID), nullStringValue(candidate.BaseWorkspaceRevisionID),
			nullStringValue(candidate.BaseWorkspaceContentHash), view.RunnerImageDigest,
			candidate.Version, candidate.JournalSequence,
			candidate.SessionEpoch, candidate.WriterLeaseEpoch,
			candidate.CurrentTreeStore, candidate.CurrentTreeOwnerID, candidate.CurrentTreeRef,
			candidate.CurrentTreeContentHash, candidate.CurrentTreeHash,
			candidate.Dirty, candidate.Conflicted, candidate.Stale, candidate.RebaseRequired,
			checkpointID, candidate.SessionEpoch,
			view.Quota.CPUMillis, view.Quota.MemoryBytes, view.Quota.WorkspaceBytes,
			view.Quota.PIDLimit, view.Quota.PreviewPortLimit, idleSeconds, maxSeconds,
		)
		if insert.Error != nil || insert.RowsAffected != 1 {
			if insert.Error != nil {
				return insert.Error
			}
			return integrityError("insert SandboxSession", fmt.Errorf("insert affected %d rows", insert.RowsAffected))
		}

		for _, release := range selected {
			result := transaction.Exec(`
INSERT INTO sandbox_session_template_releases (
  session_id, ordinal, role, template_release_id, template_release_content_hash
) VALUES (?, ?, ?, ?, ?)
`, view.ID, release.Ordinal, release.Role, release.TemplateReleaseID, release.TemplateReleaseContentHash)
			if result.Error != nil || result.RowsAffected != 1 {
				if result.Error != nil {
					return result.Error
				}
				return integrityError("insert TemplateRelease projection", fmt.Errorf("insert affected %d rows", result.RowsAffected))
			}
		}
		for _, service := range view.AllowedServices {
			profiles, encodeErr := json.Marshal(service.Profiles)
			if encodeErr != nil {
				return fmt.Errorf("%w: encode service profiles: %v", ErrInvalidSession, encodeErr)
			}
			result := transaction.Exec(`
INSERT INTO sandbox_session_services (
  session_id, service_id, kind, profiles,
  template_release_id, template_release_content_hash
) VALUES (?, ?, ?, ?::jsonb, ?, ?)
`, view.ID, service.ID, service.Kind, string(profiles),
				service.TemplateRelease.ID, service.TemplateRelease.ContentHash)
			if result.Error != nil || result.RowsAffected != 1 {
				if result.Error != nil {
					return result.Error
				}
				return integrityError("insert service projection", fmt.Errorf("insert affected %d rows", result.RowsAffected))
			}
		}
		for _, port := range view.AllowedPorts {
			result := transaction.Exec(`
INSERT INTO sandbox_session_ports (
  session_id, port_name, service_id, port_number, protocol
) VALUES (?, ?, ?, ?, ?)
`, view.ID, port.Name, port.ServiceID, port.Number, port.Protocol)
			if result.Error != nil || result.RowsAffected != 1 {
				if result.Error != nil {
					return result.Error
				}
				return integrityError("insert port projection", fmt.Errorf("insert affected %d rows", result.RowsAffected))
			}
		}

		row, rowErr := loadSandboxSessionRow(transaction, view.ProjectID, view.ID, false)
		if rowErr != nil {
			return rowErr
		}
		created, rowErr = hydrateSandboxSession(transaction, row)
		return rowErr
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return SandboxSession{}, mapStoreError("create", err)
	}
	return created, nil
}

func (store *Store) Get(ctx context.Context, projectID, sessionID string) (SandboxSession, error) {
	if err := validateSessionIdentity(ctx, projectID, sessionID); err != nil {
		return SandboxSession{}, err
	}
	var session SandboxSession
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		row, loadErr := loadSandboxSessionRow(transaction, projectID, sessionID, false)
		if loadErr != nil {
			return loadErr
		}
		session, loadErr = hydrateSandboxSession(transaction, row)
		return loadErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return SandboxSession{}, mapStoreError("get", err)
	}
	return session, nil
}

// ResolveProject returns only the immutable project identity needed to route
// an authenticated top-level /sandbox-sessions/{id} request into project RBAC.
// It exposes no session configuration or Candidate facts before authorization.
func (store *Store) ResolveProject(ctx context.Context, sessionID string) (string, error) {
	if ctx == nil || !validUUID(sessionID) {
		return "", fmt.Errorf("%w: session ID is required", ErrInvalidSession)
	}
	var row struct {
		ProjectID string `gorm:"column:project_id"`
	}
	query := store.database.WithContext(ctx).Raw(`
SELECT project_id
FROM sandbox_sessions
WHERE id = ?
`, sessionID).Scan(&row)
	if query.Error != nil {
		return "", mapStoreError("resolve project", query.Error)
	}
	if query.RowsAffected != 1 || !validUUID(row.ProjectID) {
		return "", ErrSessionNotFound
	}
	return row.ProjectID, nil
}

// SyncCandidate appends candidate.synced through the database function. It
// never updates the parent projection directly.
func (store *Store) SyncCandidate(
	ctx context.Context,
	projectID, sessionID string,
	expectedVersion, expectedSessionEpoch uint64,
	actorID string,
) (SandboxSession, error) {
	if err := validateMutationInput(ctx, projectID, sessionID, actorID, expectedVersion, expectedSessionEpoch); err != nil {
		return SandboxSession{}, err
	}
	return store.mutate(ctx, "sync candidate", projectID, sessionID, expectedVersion, expectedSessionEpoch,
		func(transaction *gorm.DB, parent sandboxSessionRow) error {
			if State(parent.State) != StateReady && State(parent.State) != StateResuming {
				return invalidTransition(State(parent.State), State(parent.State))
			}
			var result struct {
				SessionVersion    int64  `gorm:"column:session_version"`
				SessionEpoch      int64  `gorm:"column:session_epoch"`
				CandidateVersion  int64  `gorm:"column:candidate_version"`
				CandidateTreeHash string `gorm:"column:candidate_tree_hash"`
			}
			query := transaction.Raw(`
SELECT session_version, session_epoch, candidate_version, candidate_tree_hash
FROM sync_sandbox_session_candidate(?, ?, ?, ?)
`, sessionID, int64(expectedVersion), int64(expectedSessionEpoch), actorID).Scan(&result)
			if query.Error != nil {
				return query.Error
			}
			if query.RowsAffected != 1 || result.SessionVersion != int64(expectedVersion)+1 ||
				result.SessionEpoch != int64(expectedSessionEpoch) || result.CandidateVersion <= 0 ||
				!validDigest(result.CandidateTreeHash) {
				return integrityError("sync Candidate", fmt.Errorf("invalid function result"))
			}
			return nil
		})
}

// AttachCheckpoint appends checkpoint.attached through the database function.
func (store *Store) AttachCheckpoint(
	ctx context.Context,
	projectID, sessionID string,
	expectedVersion, expectedSessionEpoch uint64,
	actorID, checkpointID string,
) (SandboxSession, error) {
	if err := validateMutationInput(ctx, projectID, sessionID, actorID, expectedVersion, expectedSessionEpoch); err != nil {
		return SandboxSession{}, err
	}
	if !validUUID(checkpointID) {
		return SandboxSession{}, fmt.Errorf("%w: checkpoint ID is required", ErrInvalidSession)
	}
	return store.mutate(ctx, "attach checkpoint", projectID, sessionID, expectedVersion, expectedSessionEpoch,
		func(transaction *gorm.DB, parent sandboxSessionRow) error {
			if State(parent.State) != StateReady && State(parent.State) != StateResuming {
				return invalidTransition(State(parent.State), State(parent.State))
			}
			var nextVersion int64
			query := transaction.Raw(`
SELECT attach_sandbox_session_checkpoint(?, ?, ?, ?, ?) AS next_version
`, sessionID, int64(expectedVersion), int64(expectedSessionEpoch), actorID, checkpointID).Scan(&nextVersion)
			if query.Error != nil {
				return query.Error
			}
			if query.RowsAffected != 1 || nextVersion != int64(expectedVersion)+1 {
				return integrityError("attach checkpoint", fmt.Errorf("invalid function result"))
			}
			return nil
		})
}

// Transition appends one lifecycle event through transition_sandbox_session.
// An empty checkpointID is sent as SQL NULL; the database retains the current
// exact checkpoint unless this transition explicitly supplies another one.
func (store *Store) Transition(
	ctx context.Context,
	projectID, sessionID string,
	expectedVersion, expectedSessionEpoch uint64,
	target State,
	actorID, reason, checkpointID string,
) (SandboxSession, error) {
	if err := validateMutationInput(ctx, projectID, sessionID, actorID, expectedVersion, expectedSessionEpoch); err != nil {
		return SandboxSession{}, err
	}
	if !knownState(target) {
		return SandboxSession{}, fmt.Errorf("%w: unknown target state %q", ErrInvalidSession, target)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 2000 {
		return SandboxSession{}, fmt.Errorf("%w: transition reason is required and bounded", ErrInvalidSession)
	}
	var checkpoint any
	if checkpointID != "" {
		if !validUUID(checkpointID) {
			return SandboxSession{}, fmt.Errorf("%w: checkpoint ID is invalid", ErrInvalidSession)
		}
		checkpoint = checkpointID
	}
	return store.mutate(ctx, "transition", projectID, sessionID, expectedVersion, expectedSessionEpoch,
		func(transaction *gorm.DB, _ sandboxSessionRow) error {
			var result struct {
				SessionVersion   int64  `gorm:"column:session_version"`
				SessionState     string `gorm:"column:session_state"`
				SessionEpoch     int64  `gorm:"column:session_epoch"`
				CandidateVersion int64  `gorm:"column:candidate_version"`
			}
			query := transaction.Raw(`
SELECT session_version, session_state, session_epoch, candidate_version
FROM transition_sandbox_session(?, ?, ?, ?, ?, ?, ?)
`, sessionID, int64(expectedVersion), int64(expectedSessionEpoch), target.String(), actorID, reason, checkpoint).Scan(&result)
			if query.Error != nil {
				return query.Error
			}
			if query.RowsAffected != 1 || result.SessionVersion != int64(expectedVersion)+1 ||
				result.SessionState != target.String() || result.SessionEpoch <= 0 || result.CandidateVersion <= 0 {
				return integrityError("transition SandboxSession", fmt.Errorf("invalid function result"))
			}
			return nil
		})
}

// AbandonCandidate commits the Candidate terminal control event and the
// SandboxSession's transition into terminating in one serializable database
// transaction. Runtime cleanup happens only after this fence is durable.
func (store *Store) AbandonCandidate(
	ctx context.Context,
	projectID, sessionID, candidateID string,
	expectedVersion, expectedSessionEpoch, expectedCandidateVersion, expectedWriterLeaseEpoch uint64,
	actorID, checkpointID, reason string,
) (SandboxSession, error) {
	if err := validateMutationInput(
		ctx, projectID, sessionID, actorID, expectedVersion, expectedSessionEpoch,
	); err != nil {
		return SandboxSession{}, err
	}
	if !validUUID(candidateID) || expectedCandidateVersion == 0 ||
		expectedWriterLeaseEpoch == 0 || expectedCandidateVersion > math.MaxInt64 ||
		expectedWriterLeaseEpoch > math.MaxInt64 {
		return SandboxSession{}, fmt.Errorf("%w: exact Candidate fences are required", ErrInvalidSession)
	}
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID != "" && !validUUID(checkpointID) {
		return SandboxSession{}, fmt.Errorf("%w: checkpoint ID is invalid", ErrInvalidSession)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 1000 {
		return SandboxSession{}, fmt.Errorf("%w: abandonment reason is required and bounded", ErrInvalidSession)
	}
	var checkpoint any
	if checkpointID != "" {
		checkpoint = checkpointID
	}
	return store.mutate(ctx, "abandon Candidate", projectID, sessionID, expectedVersion, expectedSessionEpoch,
		func(transaction *gorm.DB, parent sandboxSessionRow) error {
			if parent.CandidateID != candidateID || parent.CandidateVersion != int64(expectedCandidateVersion) ||
				parent.CandidateWriterEpoch != int64(expectedWriterLeaseEpoch) {
				return ErrCandidateVersionConflict
			}
			var result struct {
				SessionVersion   int64  `gorm:"column:session_version"`
				SessionState     string `gorm:"column:session_state"`
				SessionEpoch     int64  `gorm:"column:session_epoch"`
				CandidateVersion int64  `gorm:"column:candidate_version"`
			}
			query := transaction.Raw(`
SELECT session_version, session_state, session_epoch, candidate_version
FROM abandon_sandbox_session_candidate(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, sessionID, candidateID, int64(expectedVersion), int64(expectedSessionEpoch),
				int64(expectedCandidateVersion), int64(expectedWriterLeaseEpoch), actorID,
				checkpoint, reason, projectID).Scan(&result)
			if query.Error != nil {
				return query.Error
			}
			if query.RowsAffected != 1 || result.SessionVersion != int64(expectedVersion)+1 ||
				result.SessionState != StateTerminating.String() ||
				result.SessionEpoch != int64(expectedSessionEpoch) ||
				result.CandidateVersion != int64(expectedCandidateVersion)+1 {
				return integrityError("abandon Candidate", fmt.Errorf("invalid function result"))
			}
			return nil
		})
}

// CompleteCandidateAbandon records terminal reconciliation only after runtime,
// terminal/process resources, and the materialized workspace were cleaned up.
func (store *Store) CompleteCandidateAbandon(
	ctx context.Context,
	projectID, sessionID string,
	expectedVersion, expectedSessionEpoch uint64,
	actorID string,
) (SandboxSession, error) {
	if err := validateMutationInput(
		ctx, projectID, sessionID, actorID, expectedVersion, expectedSessionEpoch,
	); err != nil {
		return SandboxSession{}, err
	}
	return store.mutate(ctx, "complete Candidate abandon", projectID, sessionID, expectedVersion, expectedSessionEpoch,
		func(transaction *gorm.DB, _ sandboxSessionRow) error {
			var result struct {
				SessionVersion int64  `gorm:"column:session_version"`
				SessionState   string `gorm:"column:session_state"`
				SessionEpoch   int64  `gorm:"column:session_epoch"`
			}
			query := transaction.Raw(`
SELECT session_version, session_state, session_epoch
FROM complete_abandoned_sandbox_session(?, ?, ?, ?)
`, sessionID, int64(expectedVersion), int64(expectedSessionEpoch), actorID).Scan(&result)
			if query.Error != nil {
				return query.Error
			}
			if query.RowsAffected != 1 || result.SessionVersion != int64(expectedVersion)+1 ||
				result.SessionState != StateTerminated.String() ||
				result.SessionEpoch != int64(expectedSessionEpoch) {
				return integrityError("complete Candidate abandon", fmt.Errorf("invalid function result"))
			}
			return nil
		})
}

func (store *Store) mutate(
	ctx context.Context,
	operation, projectID, sessionID string,
	expectedVersion, expectedSessionEpoch uint64,
	mutation func(*gorm.DB, sandboxSessionRow) error,
) (SandboxSession, error) {
	var result SandboxSession
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		parent, loadErr := loadSandboxSessionRow(transaction, projectID, sessionID, true)
		if loadErr != nil {
			return loadErr
		}
		// Check the epoch first so a pre-resume client receives the stronger
		// fencing signal even when its aggregate version is stale as well.
		if parent.SessionEpoch != int64(expectedSessionEpoch) {
			return ErrEpochFenced
		}
		if parent.Version != int64(expectedVersion) {
			return ErrVersionConflict
		}
		if mutationErr := mutation(transaction, parent); mutationErr != nil {
			return mutationErr
		}
		updated, loadErr := loadSandboxSessionRow(transaction, projectID, sessionID, false)
		if loadErr != nil {
			return loadErr
		}
		result, loadErr = hydrateSandboxSession(transaction, updated)
		return loadErr
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return SandboxSession{}, mapStoreError(operation, err)
	}
	return result, nil
}

func loadSandboxSessionRow(
	database *gorm.DB,
	projectID, sessionID string,
	lock bool,
) (sandboxSessionRow, error) {
	var row sandboxSessionRow
	query := database.Where("project_id = ? AND id = ?", projectID, sessionID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sandboxSessionRow{}, ErrSessionNotFound
	}
	if err != nil {
		return sandboxSessionRow{}, err
	}
	return row, nil
}

func loadCandidateForCreate(database *gorm.DB, projectID, candidateID string) (candidateStoreRow, error) {
	var candidate candidateStoreRow
	err := database.Clauses(clause.Locking{Strength: "SHARE"}).
		Where("project_id = ? AND id = ?", projectID, candidateID).
		Take(&candidate).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return candidateStoreRow{}, fmt.Errorf("%w: exact same-project Candidate does not exist", ErrCandidateMismatch)
	}
	if err != nil {
		return candidateStoreRow{}, err
	}
	return candidate, nil
}

func validateCandidateForCreate(input repository.CandidateWorkspace, row candidateStoreRow) error {
	if input.Version == 0 || input.Version > math.MaxInt64 ||
		input.JournalSequence > math.MaxInt64 || input.SessionEpoch == 0 || input.SessionEpoch > math.MaxInt64 ||
		input.WriterLeaseEpoch > math.MaxInt64 {
		return fmt.Errorf("%w: Candidate fences exceed PostgreSQL bounds", ErrInvalidSession)
	}
	tree, err := repository.ParseTree(input.CurrentTree)
	if err != nil {
		return fmt.Errorf("%w: Candidate tree: %v", ErrCandidateMismatch, err)
	}
	var treeBytes int64
	for _, file := range tree.Files {
		if file.ByteSize > math.MaxInt64-treeBytes {
			return fmt.Errorf("%w: Candidate tree size overflows", ErrCandidateMismatch)
		}
		treeBytes += file.ByteSize
	}
	if row.SchemaVersion != input.SchemaVersion || row.ID != input.ID || row.ProjectID != input.ProjectID ||
		row.RepositorySnapshotID != input.RepositorySnapshotID ||
		row.BuildManifestID != input.BuildManifest.ID || row.BuildManifestHash != input.BuildManifest.ContentHash ||
		row.BuildContractID != input.BuildContract.ID || row.BuildContractHash != input.BuildContract.ContentHash ||
		row.FullStackTemplateID != input.FullStackTemplate.ID || row.FullStackTemplateHash != input.FullStackTemplate.ContentHash ||
		!candidateBaseWorkspaceMatches(input.BaseWorkspaceRevision, row) ||
		row.BaseTreeHash != input.BaseTreeHash || row.CurrentTreeHash != tree.TreeHash ||
		row.CurrentTreeFileCount != len(tree.Files) || row.CurrentTreeByteSize != treeBytes ||
		row.Status != string(input.Status) || row.Dirty != input.Dirty || row.Conflicted != input.Conflicted ||
		row.Stale != input.Stale || row.RebaseRequired != input.RebaseRequired ||
		row.SessionEpoch != int64(input.SessionEpoch) || row.Version != int64(input.Version) ||
		row.JournalSequence != int64(input.JournalSequence) || row.WriterLeaseEpoch != int64(input.WriterLeaseEpoch) ||
		row.CreatedBy != input.CreatedBy || !postgresTimesEqual(row.CreatedAt, input.CreatedAt) ||
		!postgresTimesEqual(row.UpdatedAt, input.UpdatedAt) {
		return fmt.Errorf("%w: supplied Candidate differs from its exact PostgreSQL projection", ErrCandidateMismatch)
	}
	if err := validateTreePointer(row.BaseTreeStore, row.BaseTreeOwnerID, row.BaseTreeRef, row.BaseTreeContentHash, row.BaseTreeHash); err != nil {
		return err
	}
	if err := validateTreePointer(row.CurrentTreeStore, row.CurrentTreeOwnerID, row.CurrentTreeRef, row.CurrentTreeContentHash, row.CurrentTreeHash); err != nil {
		return err
	}
	if input.Lease == nil {
		if row.WriterLeaseOwnerID.Valid || row.WriterLeaseExpiresAt.Valid {
			return fmt.Errorf("%w: supplied Candidate omits its live writer lease", ErrCandidateMismatch)
		}
	} else if !row.WriterLeaseOwnerID.Valid || !row.WriterLeaseExpiresAt.Valid ||
		row.WriterLeaseOwnerID.String != input.Lease.OwnerID || row.WriterLeaseEpoch != int64(input.Lease.Epoch) ||
		!postgresTimesEqual(row.WriterLeaseExpiresAt.Time, input.Lease.ExpiresAt) {
		return fmt.Errorf("%w: supplied Candidate writer lease is not exact", ErrCandidateMismatch)
	}
	return nil
}

func nullStringValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func candidateBaseWorkspaceMatches(reference *repository.ExactRevisionReference, row candidateStoreRow) bool {
	if reference == nil {
		return !row.BaseWorkspaceArtifactID.Valid && !row.BaseWorkspaceRevisionID.Valid && !row.BaseWorkspaceContentHash.Valid
	}
	return row.BaseWorkspaceArtifactID.Valid && row.BaseWorkspaceArtifactID.String == reference.ArtifactID &&
		row.BaseWorkspaceRevisionID.Valid && row.BaseWorkspaceRevisionID.String == reference.RevisionID &&
		row.BaseWorkspaceContentHash.Valid && row.BaseWorkspaceContentHash.String == reference.ContentHash
}

func validateInitialCheckpointForCreate(
	database *gorm.DB,
	input repository.CandidateSnapshot,
	candidate candidateStoreRow,
) error {
	if input.CandidateVersion == 0 || input.CandidateVersion > math.MaxInt64 ||
		input.JournalSequence > math.MaxInt64 || input.SessionEpoch == 0 || input.SessionEpoch > math.MaxInt64 ||
		input.WriterLeaseEpoch > math.MaxInt64 {
		return fmt.Errorf("%w: CandidateSnapshot fences exceed PostgreSQL bounds", ErrInvalidSession)
	}
	var row sandboxCheckpointRow
	query := database.Raw(`
SELECT id, project_id, candidate_id, candidate_version, journal_sequence,
       session_epoch, writer_lease_epoch, tree_store, tree_owner_id, tree_ref,
       tree_content_hash, tree_hash, tree_file_count, tree_byte_size,
       reason, created_by, created_at
FROM candidate_snapshots
WHERE id = ? AND project_id = ? AND candidate_id = ?
FOR SHARE
`, input.ID, candidate.ProjectID, candidate.ID).Scan(&row)
	if query.Error != nil {
		return query.Error
	}
	if query.RowsAffected != 1 {
		return fmt.Errorf("%w: exact same-project CandidateSnapshot does not exist", ErrCheckpointMismatch)
	}
	tree, err := repository.ParseTree(input.Tree)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCheckpointMismatch, err)
	}
	var byteSize int64
	for _, file := range tree.Files {
		byteSize += file.ByteSize
	}
	if row.ID != input.ID || row.ProjectID != input.ProjectID || row.CandidateID != input.CandidateID ||
		row.CandidateVersion != int64(input.CandidateVersion) || row.JournalSequence != int64(input.JournalSequence) ||
		row.SessionEpoch != int64(input.SessionEpoch) || row.WriterLeaseEpoch != int64(input.WriterLeaseEpoch) ||
		row.TreeHash != tree.TreeHash || row.TreeFileCount != len(tree.Files) || row.TreeByteSize != byteSize ||
		row.Reason != input.Reason || row.CreatedBy != input.CreatedBy || !postgresTimesEqual(row.CreatedAt, input.CreatedAt) ||
		row.CandidateVersion != candidate.Version || row.JournalSequence != candidate.JournalSequence ||
		row.SessionEpoch != candidate.SessionEpoch || row.WriterLeaseEpoch != candidate.WriterLeaseEpoch ||
		row.TreeStore != candidate.CurrentTreeStore || row.TreeOwnerID != candidate.CurrentTreeOwnerID ||
		row.TreeRef != candidate.CurrentTreeRef || row.TreeContentHash != candidate.CurrentTreeContentHash ||
		row.TreeHash != candidate.CurrentTreeHash {
		return fmt.Errorf("%w: supplied CandidateSnapshot differs from its exact PostgreSQL projection", ErrCheckpointMismatch)
	}
	return nil
}

func loadSelectedTemplateReleases(
	database *gorm.DB,
	candidate candidateStoreRow,
) ([]sandboxTemplateReleaseRow, error) {
	var values []sandboxTemplateReleaseRow
	query := database.Raw(`
SELECT selected.ordinal, selected.role,
       selected.template_release_id, selected.template_release_content_hash
FROM application_build_contracts AS contract
JOIN application_build_contract_template_releases AS selected
  ON selected.contract_id = contract.id
WHERE contract.id = ?
  AND contract.project_id = ?
  AND contract.contract_hash = ?
  AND contract.status = 'ready'
  AND contract.build_manifest_id = ?
  AND contract.build_manifest_hash = ?
  AND contract.full_stack_template_id = ?
  AND contract.full_stack_template_hash = ?
ORDER BY selected.ordinal
`, candidate.BuildContractID, candidate.ProjectID, candidate.BuildContractHash,
		candidate.BuildManifestID, candidate.BuildManifestHash,
		candidate.FullStackTemplateID, candidate.FullStackTemplateHash).Scan(&values)
	if query.Error != nil {
		return nil, query.Error
	}
	if len(values) == 0 || len(values) > 3 {
		return nil, fmt.Errorf("%w: exact ready BuildContract TemplateRelease selection is unavailable", ErrCandidateMismatch)
	}
	return sortedReleaseRows(values), nil
}

func validateSelectedReleaseBindings(values []sandboxTemplateReleaseRow, services []AllowedService) error {
	selected := make(map[string]repository.ExactReference, len(values))
	releaseRole := make(map[string]string, len(values))
	for _, value := range values {
		ref := repository.ExactReference{ID: value.TemplateReleaseID, ContentHash: value.TemplateReleaseContentHash}
		if validateExactReference(ref) != nil || (value.Role != "web" && value.Role != "api" && value.Role != "worker") {
			return fmt.Errorf("%w: BuildContract selected release is invalid", ErrCandidateMismatch)
		}
		if _, exists := selected[value.Role]; exists {
			return fmt.Errorf("%w: BuildContract selects a role more than once", ErrCandidateMismatch)
		}
		if prior, exists := releaseRole[ref.ID]; exists && prior != value.Role {
			return fmt.Errorf("%w: one TemplateRelease cannot span roles", ErrInvalidSession)
		}
		selected[value.Role] = ref
		releaseRole[ref.ID] = value.Role
	}
	covered := make(map[string]bool, len(selected))
	for _, service := range services {
		ref, exists := selected[service.Kind]
		if !exists || ref != service.TemplateRelease {
			return fmt.Errorf("%w: service %s is not bound to its exact BuildContract role", ErrInvalidSession, service.ID)
		}
		covered[service.Kind] = true
	}
	if len(covered) != len(selected) {
		return fmt.Errorf("%w: every selected BuildContract role requires a service", ErrInvalidSession)
	}
	return nil
}

func validateTreePointer(store, ownerID, ref, contentHash, treeHash string) error {
	if strings.TrimSpace(store) == "" || store != strings.TrimSpace(store) ||
		!validUUID(ownerID) || strings.TrimSpace(ref) == "" || ref != strings.TrimSpace(ref) ||
		!validDigest(contentHash) || !validDigest(treeHash) {
		return fmt.Errorf("%w: Candidate tree pointer is invalid", ErrCandidateMismatch)
	}
	return nil
}

func validateStoreContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalidSession)
	}
	return nil
}

func validateSessionIdentity(ctx context.Context, projectID, sessionID string) error {
	if err := validateStoreContext(ctx); err != nil {
		return err
	}
	if !validUUID(projectID) || !validUUID(sessionID) {
		return fmt.Errorf("%w: project and session IDs are required", ErrInvalidSession)
	}
	return nil
}

func validateMutationInput(
	ctx context.Context,
	projectID, sessionID, actorID string,
	expectedVersion, expectedSessionEpoch uint64,
) error {
	if err := validateSessionIdentity(ctx, projectID, sessionID); err != nil {
		return err
	}
	if !validUUID(actorID) || expectedVersion == 0 || expectedSessionEpoch == 0 ||
		expectedVersion > math.MaxInt64 || expectedSessionEpoch > math.MaxInt64 {
		return fmt.Errorf("%w: actor and positive bounded CAS fences are required", ErrInvalidSession)
	}
	return nil
}

func durationSeconds(value time.Duration) (int64, error) {
	if value <= 0 || value%time.Second != 0 {
		return 0, fmt.Errorf("%w: TTL values must be positive whole seconds", ErrInvalidSession)
	}
	seconds := int64(value / time.Second)
	if seconds <= 0 || seconds > math.MaxInt32 {
		return 0, fmt.Errorf("%w: TTL seconds exceed PostgreSQL bounds", ErrInvalidSession)
	}
	return seconds, nil
}

func knownState(state State) bool {
	switch state {
	case StateProvisioning, StateStarting, StateReady, StateSuspending, StateSuspended,
		StateResuming, StateTerminating, StateTerminated, StateFailed:
		return true
	default:
		return false
	}
}

func postgresTimesEqual(left, right time.Time) bool {
	return !left.IsZero() && !right.IsZero() &&
		left.UTC().Truncate(time.Microsecond).Equal(right.UTC().Truncate(time.Microsecond))
}

type storeError struct {
	operation string
	kind      error
	cause     error
}

func (err *storeError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("sandbox persistence %s: %v: %v", err.operation, err.kind, err.cause)
}

func (err *storeError) Unwrap() []error {
	if err == nil {
		return nil
	}
	return []error{err.kind, err.cause}
}

func integrityError(operation string, cause error) error {
	return &storeError{operation: operation, kind: ErrStoreIntegrity, cause: cause}
}

func mapStoreError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var persisted *storeError
	if errors.As(err, &persisted) {
		return err
	}
	for _, known := range []error{
		ErrInvalidSession, ErrInvalidTransition, ErrVersionConflict, ErrEpochFenced,
		ErrCandidateVersionConflict, ErrCandidateMismatch, ErrCheckpointRequired,
		ErrCheckpointMismatch, ErrSessionNotFound, ErrSessionExists,
	} {
		if errors.Is(err, known) {
			return err
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		message := strings.ToLower(postgres.Message)
		switch {
		case postgres.Code == "23505" && operation == "create":
			return &storeError{operation: operation, kind: ErrSessionExists, cause: err}
		case strings.Contains(message, "dirty resumed candidate") ||
			strings.Contains(message, "dirty candidate requires") ||
			strings.Contains(message, "dirty candidate abandonment requires"):
			return &storeError{operation: operation, kind: ErrCheckpointRequired, cause: err}
		case strings.Contains(message, "checkpoint") &&
			(strings.Contains(message, "not exact") || strings.Contains(message, "not the attached exact")):
			return &storeError{operation: operation, kind: ErrCheckpointMismatch, cause: err}
		case strings.Contains(message, "invalid sandboxsession lifecycle transition"):
			return &storeError{operation: operation, kind: ErrInvalidTransition, cause: err}
		case strings.Contains(message, "candidate sync failed") ||
			strings.Contains(message, "rotate") && strings.Contains(message, "candidate"):
			return &storeError{operation: operation, kind: ErrCandidateVersionConflict, cause: err}
		case strings.Contains(message, "exact active candidate") ||
			strings.Contains(message, "exact live candidate") ||
			strings.Contains(message, "smuggle a candidate"):
			return &storeError{operation: operation, kind: ErrCandidateMismatch, cause: err}
		case postgres.Code == "22023":
			return &storeError{operation: operation, kind: ErrInvalidSession, cause: err}
		case postgres.Code == "40001":
			return &storeError{operation: operation, kind: ErrVersionConflict, cause: err}
		case strings.HasPrefix(postgres.Code, "08") || postgres.Code == "57P01" || postgres.Code == "57P02" || postgres.Code == "57P03":
			return &storeError{operation: operation, kind: ErrStoreUnavailable, cause: err}
		default:
			return &storeError{operation: operation, kind: ErrStoreIntegrity, cause: err}
		}
	}
	return &storeError{operation: operation, kind: ErrStoreUnavailable, cause: err}
}
