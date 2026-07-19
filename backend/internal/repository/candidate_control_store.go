package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrCandidateNotFound = errors.New("repository candidate was not found")
	ErrCheckpointExists  = errors.New("an exact candidate checkpoint already exists")
)

// CandidateControlStore is the project-scoped PostgreSQL boundary for
// Candidate lifecycle operations. It deliberately calls only the migration
// 000023 append functions for mutable state; transports never update the
// Candidate projection directly.
type CandidateControlStore struct {
	database   *gorm.DB
	candidates *GORMCandidateStore
}

func NewCandidateControlStore(database *gorm.DB, candidates *GORMCandidateStore) (*CandidateControlStore, error) {
	if database == nil || candidates == nil {
		return nil, errors.New("repository candidate control database and candidate store are required")
	}
	return &CandidateControlStore{database: database, candidates: candidates}, nil
}

func (store *CandidateControlStore) Get(
	ctx context.Context,
	projectID, candidateID string,
) (CandidateMutationRecord, error) {
	if ctx == nil {
		return CandidateMutationRecord{}, ErrInvalidCandidate
	}
	record, err := store.candidates.LoadMutationCandidate(ctx, projectID, candidateID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CandidateMutationRecord{}, ErrCandidateNotFound
	}
	return record, err
}

func (store *CandidateControlStore) AcquireLease(
	ctx context.Context,
	projectID, candidateID string,
	expectedVersion uint64,
	actorID string,
	ttl time.Duration,
) (CandidateMutationRecord, error) {
	if err := validateControlInput(ctx, projectID, candidateID, actorID, expectedVersion); err != nil {
		return CandidateMutationRecord{}, err
	}
	if ttl <= 0 || ttl > 30*time.Minute || ttl%time.Second != 0 {
		return CandidateMutationRecord{}, fmt.Errorf("%w: lease TTL must be whole seconds between 1 and 1800", ErrInvalidCandidate)
	}

	return store.mutate(ctx, projectID, candidateID, func(transaction *gorm.DB, before candidateWorkspaceRow) error {
		if before.Status != string(CandidateActive) || uint64(before.Version) != expectedVersion {
			return ErrCandidateState
		}
		var result struct {
			Version    int64     `gorm:"column:candidate_version"`
			Session    int64     `gorm:"column:session_epoch"`
			LeaseEpoch int64     `gorm:"column:writer_lease_epoch"`
			ExpiresAt  time.Time `gorm:"column:writer_lease_expires_at"`
		}
		query := transaction.Raw(`
SELECT candidate_version, session_epoch, writer_lease_epoch, writer_lease_expires_at
FROM acquire_candidate_workspace_lease(?, ?, ?, ?)
`, candidateID, int64(expectedVersion), actorID, int(ttl/time.Second)).Scan(&result)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected != 1 || result.Version != before.Version+1 || result.Session != before.SessionEpoch ||
			result.LeaseEpoch != before.WriterLeaseEpoch+1 || result.ExpiresAt.IsZero() {
			return candidateControlContract("lease function returned an invalid projection", nil)
		}
		return nil
	})
}

func (store *CandidateControlStore) RotateSession(
	ctx context.Context,
	projectID, candidateID string,
	expectedVersion, expectedSessionEpoch uint64,
	actorID string,
) (CandidateMutationRecord, error) {
	if err := validateControlInput(ctx, projectID, candidateID, actorID, expectedVersion); err != nil || expectedSessionEpoch == 0 {
		if err != nil {
			return CandidateMutationRecord{}, err
		}
		return CandidateMutationRecord{}, ErrInvalidCandidate
	}

	return store.mutate(ctx, projectID, candidateID, func(transaction *gorm.DB, before candidateWorkspaceRow) error {
		if before.Status != string(CandidateActive) || uint64(before.Version) != expectedVersion {
			return ErrCandidateState
		}
		if uint64(before.SessionEpoch) != expectedSessionEpoch {
			return ErrLeaseFenced
		}
		var result struct {
			Version    int64 `gorm:"column:candidate_version"`
			Session    int64 `gorm:"column:session_epoch"`
			LeaseEpoch int64 `gorm:"column:writer_lease_epoch"`
		}
		query := transaction.Raw(`
SELECT candidate_version, session_epoch, writer_lease_epoch
FROM rotate_candidate_workspace_session(?, ?, ?, ?)
`, candidateID, int64(expectedVersion), int64(expectedSessionEpoch), actorID).Scan(&result)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected != 1 || result.Version != before.Version+1 || result.Session != before.SessionEpoch+1 ||
			result.LeaseEpoch != before.WriterLeaseEpoch+1 {
			return candidateControlContract("session rotation returned an invalid projection", nil)
		}
		return nil
	})
}

type UpdateCandidateFlagsInput struct {
	ProjectID                string
	CandidateID              string
	ExpectedCandidateVersion uint64
	ExpectedSessionEpoch     uint64
	ExpectedWriterLeaseEpoch uint64
	ActorID                  string
	Flags                    CandidateFlags
	Reason                   string
	EvidenceRef              string
	EvidenceHash             string
}

// UpdateFlags persists a fully fenced, evidence-bound Candidate control
// event. Stale/rebase flags are monotonic: clearing them would mutate an old
// lineage in place instead of selecting its immutable successor Candidate.
func (store *CandidateControlStore) UpdateFlags(
	ctx context.Context,
	input UpdateCandidateFlagsInput,
) (CandidateMutationRecord, error) {
	if err := validateControlInput(
		ctx, input.ProjectID, input.CandidateID, input.ActorID, input.ExpectedCandidateVersion,
	); err != nil || input.ExpectedSessionEpoch == 0 || !postgresBigint(input.ExpectedSessionEpoch) ||
		!postgresBigint(input.ExpectedWriterLeaseEpoch) {
		if err != nil {
			return CandidateMutationRecord{}, err
		}
		return CandidateMutationRecord{}, ErrInvalidCandidate
	}
	input.Reason = strings.TrimSpace(input.Reason)
	input.EvidenceRef = strings.TrimSpace(input.EvidenceRef)
	input.EvidenceHash = strings.TrimSpace(input.EvidenceHash)
	if input.Reason == "" || len(input.Reason) > 1000 || input.EvidenceRef == "" || len(input.EvidenceRef) > 2000 ||
		!isCanonicalSHA256(input.EvidenceHash) || input.Flags.Stale != input.Flags.RebaseRequired {
		return CandidateMutationRecord{}, fmt.Errorf("%w: invalid flag transition evidence", ErrInvalidCandidate)
	}

	return store.mutate(ctx, input.ProjectID, input.CandidateID, func(transaction *gorm.DB, before candidateWorkspaceRow) error {
		if before.Status != string(CandidateActive) || uint64(before.Version) != input.ExpectedCandidateVersion {
			return ErrCandidateState
		}
		if uint64(before.SessionEpoch) != input.ExpectedSessionEpoch ||
			uint64(before.WriterLeaseEpoch) != input.ExpectedWriterLeaseEpoch {
			return ErrLeaseFenced
		}
		if (before.Stale && !input.Flags.Stale) || (before.RebaseRequired && !input.Flags.RebaseRequired) ||
			(before.Conflicted == input.Flags.Conflicted && before.Stale == input.Flags.Stale &&
				before.RebaseRequired == input.Flags.RebaseRequired) {
			return ErrCandidateState
		}
		var nextVersion int64
		query := transaction.Raw(`
SELECT update_candidate_workspace_flags(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) AS next_version
`, input.CandidateID, int64(input.ExpectedCandidateVersion), int64(input.ExpectedSessionEpoch),
			int64(input.ExpectedWriterLeaseEpoch), input.ActorID, input.Flags.Conflicted,
			input.Flags.Stale, input.Flags.RebaseRequired, input.Reason,
			input.EvidenceRef, input.EvidenceHash).Scan(&nextVersion)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected != 1 || nextVersion != before.Version+1 {
			return candidateControlContract("flag function returned an invalid projection", nil)
		}
		return nil
	})
}

type CreateCheckpointInput struct {
	ID                       string
	ProjectID                string
	CandidateID              string
	ExpectedCandidateVersion uint64
	ExpectedSessionEpoch     uint64
	ExpectedWriterLeaseEpoch uint64
	ActorID                  string
	Reason                   string
}

func (store *CandidateControlStore) CreateCheckpoint(
	ctx context.Context,
	input CreateCheckpointInput,
) (CandidateSnapshot, error) {
	if err := validateControlInput(ctx, input.ProjectID, input.CandidateID, input.ActorID, input.ExpectedCandidateVersion); err != nil ||
		!validUUID(input.ID) || input.ExpectedSessionEpoch == 0 || input.ExpectedWriterLeaseEpoch == 0 {
		if err != nil {
			return CandidateSnapshot{}, err
		}
		return CandidateSnapshot{}, fmt.Errorf("%w: invalid checkpoint identity or fence", ErrInvalidCandidate)
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" || len(input.Reason) > 1000 {
		return CandidateSnapshot{}, fmt.Errorf("%w: checkpoint reason is required and bounded", ErrInvalidCandidate)
	}

	var snapshot CandidateSnapshot
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		row, err := lockCandidateRow(transaction, input.ProjectID, input.CandidateID)
		if err != nil {
			return err
		}
		if row.Status != string(CandidateActive) || uint64(row.Version) != input.ExpectedCandidateVersion {
			return ErrCandidateState
		}
		if uint64(row.SessionEpoch) != input.ExpectedSessionEpoch || uint64(row.WriterLeaseEpoch) != input.ExpectedWriterLeaseEpoch {
			return ErrLeaseFenced
		}

		// There is exactly one immutable checkpoint per exact Candidate
		// version/tree. Returning it makes autosave retries idempotent even when
		// an HTTP acknowledgement was lost and the retry carries a new UUID.
		var existing candidateSnapshotRow
		lookup := transaction.Raw(`
SELECT *
FROM candidate_snapshots
WHERE project_id = ? AND candidate_id = ? AND candidate_version = ? AND tree_hash = ?
LIMIT 1
`, input.ProjectID, input.CandidateID, row.Version, row.CurrentTreeHash).Scan(&existing)
		if lookup.Error != nil {
			return lookup.Error
		}
		if lookup.RowsAffected == 1 {
			value, hydrateErr := store.hydrateCheckpoint(ctx, existing)
			if hydrateErr != nil {
				return hydrateErr
			}
			snapshot = value
			return nil
		}

		var createdAt time.Time
		insert := transaction.Raw(`
INSERT INTO candidate_snapshots (
  id, schema_version, candidate_id, project_id,
  candidate_version, journal_sequence, session_epoch, writer_lease_epoch,
  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
  tree_file_count, tree_byte_size, reason, created_by
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING created_at
`, input.ID, CandidateSnapshotSchemaVersion, input.CandidateID, input.ProjectID,
			row.Version, row.JournalSequence, row.SessionEpoch, row.WriterLeaseEpoch,
			row.CurrentTreeStore, row.CurrentTreeOwnerID, row.CurrentTreeRef,
			row.CurrentTreeContentHash, row.CurrentTreeHash,
			row.CurrentTreeFileCount, row.CurrentTreeByteSize, input.Reason, input.ActorID,
		).Scan(&createdAt)
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected != 1 || createdAt.IsZero() {
			return candidateControlContract("checkpoint insert returned no exact row", nil)
		}
		value, hydrateErr := store.hydrateCheckpoint(ctx, candidateSnapshotRow{
			ID: input.ID, SchemaVersion: CandidateSnapshotSchemaVersion,
			CandidateID: input.CandidateID, ProjectID: input.ProjectID,
			CandidateVersion: row.Version, JournalSequence: row.JournalSequence,
			SessionEpoch: row.SessionEpoch, WriterLeaseEpoch: row.WriterLeaseEpoch,
			TreeStore: row.CurrentTreeStore, TreeOwnerID: row.CurrentTreeOwnerID.String(),
			TreeRef: row.CurrentTreeRef, TreeContentHash: row.CurrentTreeContentHash,
			TreeHash: row.CurrentTreeHash, TreeFileCount: row.CurrentTreeFileCount,
			TreeByteSize: row.CurrentTreeByteSize, Reason: input.Reason,
			CreatedBy: input.ActorID, CreatedAt: createdAt,
		})
		if hydrateErr != nil {
			return hydrateErr
		}
		snapshot = value
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CandidateSnapshot{}, mapCandidateControlError(err)
	}
	return snapshot, nil
}

func (store *CandidateControlStore) Freeze(
	ctx context.Context,
	projectID, candidateID string,
	expectedVersion, expectedSessionEpoch, expectedWriterLeaseEpoch uint64,
	actorID, checkpointID, reason string,
) (CandidateMutationRecord, error) {
	return store.terminal(ctx, projectID, candidateID, expectedVersion, expectedSessionEpoch,
		expectedWriterLeaseEpoch, actorID, checkpointID, reason, CandidateFrozen)
}

func (store *CandidateControlStore) Abandon(
	ctx context.Context,
	projectID, candidateID string,
	expectedVersion, expectedSessionEpoch, expectedWriterLeaseEpoch uint64,
	actorID, checkpointID, reason string,
) (CandidateMutationRecord, error) {
	return store.terminal(ctx, projectID, candidateID, expectedVersion, expectedSessionEpoch,
		expectedWriterLeaseEpoch, actorID, checkpointID, reason, CandidateAbandoned)
}

func (store *CandidateControlStore) terminal(
	ctx context.Context,
	projectID, candidateID string,
	expectedVersion, expectedSessionEpoch, expectedWriterLeaseEpoch uint64,
	actorID, checkpointID, reason string,
	target CandidateStatus,
) (CandidateMutationRecord, error) {
	if err := validateControlInput(ctx, projectID, candidateID, actorID, expectedVersion); err != nil ||
		expectedSessionEpoch == 0 || expectedWriterLeaseEpoch == 0 {
		if err != nil {
			return CandidateMutationRecord{}, err
		}
		return CandidateMutationRecord{}, ErrInvalidCandidate
	}
	if target == CandidateFrozen && !validUUID(checkpointID) {
		return CandidateMutationRecord{}, fmt.Errorf("%w: freeze checkpoint is required", ErrInvalidCandidate)
	}
	if checkpointID != "" && !validUUID(checkpointID) {
		return CandidateMutationRecord{}, fmt.Errorf("%w: checkpoint ID", ErrInvalidCandidate)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 1000 {
		return CandidateMutationRecord{}, fmt.Errorf("%w: terminal reason is required and bounded", ErrInvalidCandidate)
	}

	return store.mutate(ctx, projectID, candidateID, func(transaction *gorm.DB, before candidateWorkspaceRow) error {
		if before.Status != string(CandidateActive) || uint64(before.Version) != expectedVersion {
			return ErrCandidateState
		}
		if uint64(before.SessionEpoch) != expectedSessionEpoch || uint64(before.WriterLeaseEpoch) != expectedWriterLeaseEpoch {
			return ErrLeaseFenced
		}
		functionName := "freeze_candidate_workspace"
		var checkpoint any = checkpointID
		if target == CandidateAbandoned {
			functionName = "abandon_candidate_workspace"
			if checkpointID == "" {
				checkpoint = nil
			}
		}
		var nextVersion int64
		query := transaction.Raw(fmt.Sprintf(`
SELECT %s(?, ?, ?, ?, ?, ?, ?) AS next_version
`, functionName), candidateID, int64(expectedVersion), int64(expectedSessionEpoch),
			int64(expectedWriterLeaseEpoch), actorID, checkpoint, reason).Scan(&nextVersion)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected != 1 || nextVersion != before.Version+1 {
			return candidateControlContract("terminal function returned an invalid projection", nil)
		}
		return nil
	})
}

func (store *CandidateControlStore) mutate(
	ctx context.Context,
	projectID, candidateID string,
	operation func(*gorm.DB, candidateWorkspaceRow) error,
) (CandidateMutationRecord, error) {
	var record CandidateMutationRecord
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		before, err := lockCandidateRow(transaction, projectID, candidateID)
		if err != nil {
			return err
		}
		if err := operation(transaction, before); err != nil {
			return err
		}
		after, err := lockCandidateRow(transaction, projectID, candidateID)
		if err != nil {
			return err
		}
		record, err = store.hydrateCandidateRow(ctx, after)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CandidateMutationRecord{}, mapCandidateControlError(err)
	}
	return record, nil
}

func lockCandidateRow(database *gorm.DB, projectID, candidateID string) (candidateWorkspaceRow, error) {
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return candidateWorkspaceRow{}, ErrInvalidCandidate
	}
	candidateUUID, err := uuid.Parse(candidateID)
	if err != nil {
		return candidateWorkspaceRow{}, ErrInvalidCandidate
	}
	var row candidateWorkspaceRow
	result := database.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"project_id = ? AND id = ?", projectUUID, candidateUUID,
	).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return candidateWorkspaceRow{}, ErrCandidateNotFound
	}
	if result.Error != nil {
		return candidateWorkspaceRow{}, result.Error
	}
	return row, nil
}

func (store *CandidateControlStore) hydrateCandidateRow(
	ctx context.Context,
	row candidateWorkspaceRow,
) (CandidateMutationRecord, error) {
	pointer := TreeBlobPointer{
		Store: row.CurrentTreeStore, Ref: row.CurrentTreeRef, OwnerID: row.CurrentTreeOwnerID.String(),
		TreeHash: row.CurrentTreeHash, FileCount: row.CurrentTreeFileCount,
		ByteSize: row.CurrentTreeByteSize, ContentObjectHash: row.CurrentTreeContentHash,
	}
	if err := pointer.validate(); err != nil {
		return CandidateMutationRecord{}, candidateControlContract("invalid current tree pointer", err)
	}
	tree, err := store.candidates.trees.Get(ctx, row.ProjectID.String(), pointer.OwnerID, pointer)
	if err != nil {
		return CandidateMutationRecord{}, fmt.Errorf("hydrate controlled Candidate tree: %w", err)
	}
	candidate, err := candidateFromWorkspaceRow(row, tree)
	if err != nil {
		return CandidateMutationRecord{}, err
	}
	return CandidateMutationRecord{Candidate: candidate, CurrentTreePointer: pointer}, nil
}

type candidateSnapshotRow struct {
	ID               string    `gorm:"column:id"`
	SchemaVersion    string    `gorm:"column:schema_version"`
	CandidateID      string    `gorm:"column:candidate_id"`
	ProjectID        string    `gorm:"column:project_id"`
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

func (store *CandidateControlStore) hydrateCheckpoint(
	ctx context.Context,
	row candidateSnapshotRow,
) (CandidateSnapshot, error) {
	if !validUUID(row.ID) || !validUUID(row.ProjectID) || !validUUID(row.CandidateID) || !validUUID(row.CreatedBy) ||
		row.SchemaVersion != CandidateSnapshotSchemaVersion || row.CandidateVersion <= 0 || row.JournalSequence < 0 ||
		row.SessionEpoch <= 0 || row.WriterLeaseEpoch <= 0 || row.CreatedAt.IsZero() {
		return CandidateSnapshot{}, candidateControlContract("invalid checkpoint row", nil)
	}
	pointer := TreeBlobPointer{
		Store: row.TreeStore, Ref: row.TreeRef, OwnerID: row.TreeOwnerID,
		TreeHash: row.TreeHash, FileCount: row.TreeFileCount,
		ByteSize: row.TreeByteSize, ContentObjectHash: row.TreeContentHash,
	}
	if err := pointer.validate(); err != nil {
		return CandidateSnapshot{}, candidateControlContract("invalid checkpoint tree pointer", err)
	}
	tree, err := store.candidates.trees.Get(ctx, row.ProjectID, row.TreeOwnerID, pointer)
	if err != nil {
		return CandidateSnapshot{}, fmt.Errorf("hydrate candidate checkpoint tree: %w", err)
	}
	snapshot := CandidateSnapshot{
		SchemaVersion: row.SchemaVersion, ID: row.ID, ProjectID: row.ProjectID, CandidateID: row.CandidateID,
		CandidateVersion: uint64(row.CandidateVersion), JournalSequence: uint64(row.JournalSequence),
		SessionEpoch: uint64(row.SessionEpoch), WriterLeaseEpoch: uint64(row.WriterLeaseEpoch),
		Tree: tree, Reason: row.Reason, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt.UTC(),
	}
	if _, err := ParseTree(snapshot.Tree); err != nil {
		return CandidateSnapshot{}, candidateControlContract("checkpoint tree is invalid", err)
	}
	return snapshot, nil
}

func validateControlInput(
	ctx context.Context,
	projectID, candidateID, actorID string,
	expectedVersion uint64,
) error {
	if ctx == nil || !validUUID(projectID) || !validUUID(candidateID) || !validUUID(actorID) ||
		expectedVersion == 0 || !postgresBigint(expectedVersion) {
		return ErrInvalidCandidate
	}
	return nil
}

func mapCandidateControlError(err error) error {
	if err == nil || errors.Is(err, ErrCandidateNotFound) || errors.Is(err, ErrCandidateState) ||
		errors.Is(err, ErrLeaseFenced) || errors.Is(err, ErrLeaseRequired) || errors.Is(err, ErrInvalidCandidate) {
		return err
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "40001", "40P01":
			return errors.Join(ErrCandidateState, err)
		case "23514", "23503":
			return errors.Join(ErrCandidateState, err)
		case "23505":
			return errors.Join(ErrCheckpointExists, err)
		case "22001", "22003", "22023", "22P02", "23502":
			return errors.Join(ErrInvalidCandidate, err)
		}
	}
	return fmt.Errorf("repository candidate control persistence: %w", err)
}

func candidateControlContract(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrMutationStoreContract, message)
	}
	return errors.Join(ErrMutationStoreContract, fmt.Errorf("%s: %w", message, cause))
}
