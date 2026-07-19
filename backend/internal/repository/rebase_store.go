package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrCandidateRebaseNotFound         = errors.New("repository Candidate rebase was not found")
	ErrCandidateRebaseConflictNotFound = errors.New("repository Candidate rebase conflict was not found")
	ErrCandidateRebaseReplay           = errors.New("repository Candidate rebase operation was replayed with different input")
	ErrCandidateRebaseState            = errors.New("repository Candidate rebase state does not allow the operation")
	ErrCandidateRebaseStoreContract    = errors.New("repository Candidate rebase store violated its contract")
)

type CandidateRebaseStore struct {
	database   *gorm.DB
	candidates *GORMCandidateStore
}

func NewCandidateRebaseStore(database *gorm.DB, candidates *GORMCandidateStore) (*CandidateRebaseStore, error) {
	if database == nil || candidates == nil {
		return nil, errors.New("Candidate rebase database and Candidate store are required")
	}
	return &CandidateRebaseStore{database: database, candidates: candidates}, nil
}

type candidateRebaseRow struct {
	ID                     uuid.UUID `gorm:"column:id"`
	SchemaVersion          string    `gorm:"column:schema_version"`
	ProjectID              uuid.UUID `gorm:"column:project_id"`
	OperationID            string    `gorm:"column:operation_id"`
	PredecessorCandidateID uuid.UUID `gorm:"column:predecessor_candidate_id"`
	SuccessorCandidateID   uuid.UUID `gorm:"column:successor_candidate_id"`
	TargetBuildManifestID  uuid.UUID `gorm:"column:target_build_manifest_id"`
	AncestorTreeHash       string    `gorm:"column:ancestor_tree_hash"`
	PredecessorTreeHash    string    `gorm:"column:predecessor_tree_hash"`
	TargetTreeHash         string    `gorm:"column:target_tree_hash"`
	PlannedTreeHash        string    `gorm:"column:planned_tree_hash"`
	PlanHash               string    `gorm:"column:plan_hash"`
	State                  string    `gorm:"column:state"`
	Version                int64     `gorm:"column:version"`
	CreatedBy              uuid.UUID `gorm:"column:created_by"`
	CreatedAt              time.Time `gorm:"column:created_at"`
	UpdatedAt              time.Time `gorm:"column:updated_at"`
}

func (candidateRebaseRow) TableName() string { return "candidate_rebases" }

type candidateRebaseOperationRow struct {
	RebaseID    uuid.UUID       `gorm:"column:rebase_id"`
	Ordinal     int             `gorm:"column:ordinal"`
	OperationID string          `gorm:"column:operation_id"`
	Operation   json.RawMessage `gorm:"column:operation"`
}

func (candidateRebaseOperationRow) TableName() string { return "candidate_rebase_operations" }

type candidateRebaseConflictRow struct {
	ID                    uuid.UUID       `gorm:"column:id"`
	SchemaVersion         string          `gorm:"column:schema_version"`
	RebaseID              uuid.UUID       `gorm:"column:rebase_id"`
	Ordinal               int             `gorm:"column:ordinal"`
	Path                  string          `gorm:"column:path"`
	AncestorFile          json.RawMessage `gorm:"column:ancestor_file"`
	PredecessorFile       json.RawMessage `gorm:"column:predecessor_file"`
	TargetFile            json.RawMessage `gorm:"column:target_file"`
	State                 string          `gorm:"column:state"`
	Version               int64           `gorm:"column:version"`
	ResolutionStrategy    sql.NullString  `gorm:"column:resolution_strategy"`
	ResolutionContentHash sql.NullString  `gorm:"column:resolution_content_hash"`
	ResolutionDeleted     sql.NullBool    `gorm:"column:resolution_deleted"`
	ResolutionFile        json.RawMessage `gorm:"column:resolution_file"`
	ResolvedBy            *uuid.UUID      `gorm:"column:resolved_by"`
	ResolvedAt            *time.Time      `gorm:"column:resolved_at"`
	CreatedAt             time.Time       `gorm:"column:created_at"`
}

func (candidateRebaseConflictRow) TableName() string { return "candidate_rebase_conflicts" }

type CreateCandidateRebaseCommand struct {
	Rebase                   CandidateRebase
	Plan                     CandidateRebasePlan
	ExpectedCandidateVersion uint64
	ExpectedSessionEpoch     uint64
	ExpectedWriterLeaseEpoch uint64
}

func (store *CandidateRebaseStore) Create(
	ctx context.Context,
	command CreateCandidateRebaseCommand,
) (CandidateRebase, error) {
	if err := validateCreateCandidateRebaseCommand(command); err != nil {
		return CandidateRebase{}, err
	}
	rebase := command.Rebase
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		predecessor, err := lockCandidateRow(transaction, rebase.ProjectID, rebase.PredecessorCandidateID)
		if err != nil {
			return err
		}
		successor, err := lockCandidateRow(transaction, rebase.ProjectID, rebase.SuccessorCandidateID)
		if err != nil {
			return err
		}
		if predecessor.Status != string(CandidateActive) || predecessor.Conflicted || predecessor.Stale || predecessor.RebaseRequired ||
			uint64(predecessor.Version) != command.ExpectedCandidateVersion ||
			uint64(predecessor.SessionEpoch) != command.ExpectedSessionEpoch ||
			uint64(predecessor.WriterLeaseEpoch) != command.ExpectedWriterLeaseEpoch ||
			predecessor.BaseTreeHash != rebase.AncestorTreeHash ||
			predecessor.CurrentTreeHash != rebase.PredecessorTreeHash {
			return ErrCandidateRebaseState
		}
		if successor.Status != string(CandidateActive) || successor.BuildManifestID.String() != rebase.TargetBuildManifestID ||
			successor.Version != 1 || successor.JournalSequence != 0 || successor.Dirty || successor.Conflicted ||
			successor.Stale || successor.RebaseRequired || successor.CurrentTreeHash != rebase.TargetTreeHash ||
			successor.BaseTreeHash != rebase.TargetTreeHash {
			return fmt.Errorf("%w: successor is not the exact clean target snapshot", ErrCandidateRebaseState)
		}

		insert := transaction.Exec(`
INSERT INTO candidate_rebases (
  id, schema_version, project_id, operation_id,
  predecessor_candidate_id, successor_candidate_id, target_build_manifest_id,
  ancestor_tree_hash, predecessor_tree_hash, target_tree_hash, planned_tree_hash, plan_hash,
  state, version, created_by, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'applying', 1, ?, ?, ?)
`, rebase.ID, CandidateRebaseSchemaVersion, rebase.ProjectID, rebase.OperationID,
			rebase.PredecessorCandidateID, rebase.SuccessorCandidateID, rebase.TargetBuildManifestID,
			rebase.AncestorTreeHash, rebase.PredecessorTreeHash, rebase.TargetTreeHash,
			rebase.PlannedTreeHash, rebase.PlanHash, rebase.CreatedBy, rebase.CreatedAt, rebase.CreatedAt)
		if insert.Error != nil || insert.RowsAffected != 1 {
			if insert.Error != nil {
				return insert.Error
			}
			return rebaseStoreContract("rebase insert returned no row", nil)
		}
		for _, operation := range command.Plan.Operations {
			payload, marshalErr := domain.CanonicalJSON(operation.Operation)
			if marshalErr != nil {
				return rebaseStoreContract("encode planned operation", marshalErr)
			}
			result := transaction.Exec(`
INSERT INTO candidate_rebase_operations (rebase_id, ordinal, operation_id, operation)
VALUES (?, ?, ?, ?::jsonb)
`, rebase.ID, operation.Ordinal, operation.Operation.ID, string(payload))
			if result.Error != nil || result.RowsAffected != 1 {
				if result.Error != nil {
					return result.Error
				}
				return rebaseStoreContract("planned operation insert returned no row", nil)
			}
		}
		for _, conflict := range command.Plan.Conflicts {
			ancestor, marshalErr := nullableTreeFileJSON(conflict.AncestorFile)
			if marshalErr != nil {
				return marshalErr
			}
			predecessorFile, marshalErr := nullableTreeFileJSON(conflict.PredecessorFile)
			if marshalErr != nil {
				return marshalErr
			}
			target, marshalErr := nullableTreeFileJSON(conflict.TargetFile)
			if marshalErr != nil {
				return marshalErr
			}
			result := transaction.Exec(`
INSERT INTO candidate_rebase_conflicts (
  id, schema_version, rebase_id, ordinal, path,
  ancestor_file, predecessor_file, target_file, state, version, created_at
) VALUES (?, ?, ?, ?, ?, ?::jsonb, ?::jsonb, ?::jsonb, 'open', 1, ?)
`, conflict.ID, CandidateRebaseConflictSchemaVersion, rebase.ID, conflict.Ordinal, conflict.Path,
				ancestor, predecessorFile, target, rebase.CreatedAt)
			if result.Error != nil || result.RowsAffected != 1 {
				if result.Error != nil {
					return result.Error
				}
				return rebaseStoreContract("rebase conflict insert returned no row", nil)
			}
		}

		var nextVersion int64
		flags := transaction.Raw(`
SELECT update_candidate_workspace_flags(?, ?, ?, ?, ?, ?, true, true, ?, ?, ?) AS next_version
`, rebase.PredecessorCandidateID, int64(command.ExpectedCandidateVersion),
			int64(command.ExpectedSessionEpoch), int64(command.ExpectedWriterLeaseEpoch), rebase.CreatedBy,
			predecessor.Conflicted, "exact upstream BuildManifest advanced; immutable successor Candidate created",
			candidateRebaseEvidenceRef(rebase.ID), rebase.PlanHash).Scan(&nextVersion)
		if flags.Error != nil {
			return flags.Error
		}
		if flags.RowsAffected != 1 || nextVersion != predecessor.Version+1 {
			return rebaseStoreContract("predecessor stale transition returned an invalid version", nil)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CandidateRebase{}, mapCandidateRebaseStoreError(err)
	}
	return store.Get(ctx, rebase.ProjectID, rebase.ID)
}

func (store *CandidateRebaseStore) Get(
	ctx context.Context,
	projectID, rebaseID string,
) (CandidateRebase, error) {
	if ctx == nil || !validUUID(projectID) || !validUUID(rebaseID) {
		return CandidateRebase{}, ErrInvalidRebase
	}
	var row candidateRebaseRow
	result := store.database.WithContext(ctx).Where("project_id = ? AND id = ?", projectID, rebaseID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return CandidateRebase{}, ErrCandidateRebaseNotFound
	}
	if result.Error != nil {
		return CandidateRebase{}, fmt.Errorf("load Candidate rebase: %w", result.Error)
	}
	return store.hydrate(ctx, store.database, row)
}

func (store *CandidateRebaseStore) FindByOperation(
	ctx context.Context,
	projectID, actorID, operationID string,
) (CandidateRebase, bool, error) {
	if ctx == nil || !validUUID(projectID) || !validUUID(actorID) || !bootstrapOperationPattern.MatchString(operationID) {
		return CandidateRebase{}, false, ErrInvalidRebase
	}
	var row candidateRebaseRow
	result := store.database.WithContext(ctx).Where(
		"project_id = ? AND created_by = ? AND operation_id = ?", projectID, actorID, operationID,
	).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return CandidateRebase{}, false, nil
	}
	if result.Error != nil {
		return CandidateRebase{}, false, fmt.Errorf("find Candidate rebase operation: %w", result.Error)
	}
	rebase, err := store.hydrate(ctx, store.database, row)
	return rebase, err == nil, err
}

func (store *CandidateRebaseStore) Transition(
	ctx context.Context,
	projectID, rebaseID string,
	expectedState, targetState CandidateRebaseState,
	expectedVersion uint64,
) (CandidateRebase, error) {
	if ctx == nil || !validUUID(projectID) || !validUUID(rebaseID) || expectedVersion == 0 ||
		!postgresBigint(expectedVersion) ||
		!((expectedState == CandidateRebaseApplying &&
			(targetState == CandidateRebaseConflicted || targetState == CandidateRebaseReady)) ||
			(expectedState == CandidateRebaseConflicted && targetState == CandidateRebaseReady)) {
		return CandidateRebase{}, ErrInvalidRebase
	}
	result := store.database.WithContext(ctx).Exec(`
UPDATE candidate_rebases
SET state = ?, version = version + 1,
    updated_at = GREATEST(statement_timestamp(), updated_at + interval '1 microsecond')
WHERE project_id = ? AND id = ? AND state = ? AND version = ?
`, targetState, projectID, rebaseID, expectedState, int64(expectedVersion))
	if result.Error != nil {
		return CandidateRebase{}, mapCandidateRebaseStoreError(result.Error)
	}
	if result.RowsAffected != 1 {
		return CandidateRebase{}, ErrCandidateRebaseState
	}
	return store.Get(ctx, projectID, rebaseID)
}

type ResolveCandidateRebaseConflictCommand struct {
	ProjectID                         string
	RebaseID                          string
	ConflictID                        string
	ExpectedConflictVersion           uint64
	ExpectedSuccessorCandidateVersion uint64
	ExpectedSessionEpoch              uint64
	ExpectedWriterLeaseEpoch          uint64
	ActorID                           string
	Strategy                          CandidateRebaseResolutionStrategy
	ResolutionFile                    *TreeFile
}

func (store *CandidateRebaseStore) ResolveConflict(
	ctx context.Context,
	command ResolveCandidateRebaseConflictCommand,
) (CandidateRebase, error) {
	if err := validateResolveCandidateRebaseConflictCommand(command); err != nil {
		return CandidateRebase{}, err
	}
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var rebase candidateRebaseRow
		result := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"project_id = ? AND id = ?", command.ProjectID, command.RebaseID,
		).Take(&rebase)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return ErrCandidateRebaseNotFound
		}
		if result.Error != nil {
			return result.Error
		}
		if rebase.State != string(CandidateRebaseConflicted) {
			return ErrCandidateRebaseState
		}
		var conflict candidateRebaseConflictRow
		result = transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"rebase_id = ? AND id = ?", command.RebaseID, command.ConflictID,
		).Take(&conflict)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return ErrCandidateRebaseConflictNotFound
		}
		if result.Error != nil {
			return result.Error
		}
		if conflict.State != string(CandidateRebaseConflictOpen) ||
			uint64(conflict.Version) != command.ExpectedConflictVersion {
			return ErrCandidateRebaseState
		}
		successor, err := lockCandidateRow(transaction, command.ProjectID, rebase.SuccessorCandidateID.String())
		if err != nil {
			return err
		}
		if successor.Status != string(CandidateActive) || !successor.Conflicted || successor.Stale || successor.RebaseRequired ||
			uint64(successor.Version) != command.ExpectedSuccessorCandidateVersion ||
			uint64(successor.SessionEpoch) != command.ExpectedSessionEpoch ||
			uint64(successor.WriterLeaseEpoch) != command.ExpectedWriterLeaseEpoch {
			return ErrCandidateRebaseState
		}

		resolutionJSON, err := nullableTreeFileJSON(command.ResolutionFile)
		if err != nil {
			return err
		}
		deleted := command.ResolutionFile == nil
		var contentHash any
		if command.ResolutionFile != nil {
			contentHash = command.ResolutionFile.ContentHash
		}
		result = transaction.Exec(`
UPDATE candidate_rebase_conflicts
SET state = 'resolved', version = 2,
    resolution_strategy = ?, resolution_content_hash = ?, resolution_deleted = ?,
    resolution_file = ?::jsonb, resolved_by = ?, resolved_at = statement_timestamp()
WHERE rebase_id = ? AND id = ? AND state = 'open' AND version = 1
`, command.Strategy, contentHash, deleted, resolutionJSON, command.ActorID,
			command.RebaseID, command.ConflictID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCandidateRebaseState
		}

		var openConflicts int64
		if err := transaction.Raw(`
SELECT count(*) FROM candidate_rebase_conflicts WHERE rebase_id = ? AND state = 'open'
`, command.RebaseID).Scan(&openConflicts).Error; err != nil {
			return err
		}
		if openConflicts != 0 {
			return nil
		}
		var nextCandidateVersion int64
		flags := transaction.Raw(`
SELECT update_candidate_workspace_flags(?, ?, ?, ?, ?, false, false, false, ?, ?, ?) AS next_version
`, rebase.SuccessorCandidateID, int64(command.ExpectedSuccessorCandidateVersion),
			int64(command.ExpectedSessionEpoch), int64(command.ExpectedWriterLeaseEpoch), command.ActorID,
			"all exact Candidate rebase conflicts resolved", candidateRebaseEvidenceRef(command.RebaseID),
			rebase.PlanHash).Scan(&nextCandidateVersion)
		if flags.Error != nil {
			return flags.Error
		}
		if flags.RowsAffected != 1 || nextCandidateVersion != successor.Version+1 {
			return rebaseStoreContract("successor conflict clear returned an invalid version", nil)
		}
		transition := transaction.Exec(`
UPDATE candidate_rebases
SET state = 'ready', version = version + 1,
    updated_at = GREATEST(statement_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ? AND project_id = ? AND state = 'conflicted' AND version = ?
`, command.RebaseID, command.ProjectID, rebase.Version)
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected != 1 {
			return ErrCandidateRebaseState
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CandidateRebase{}, mapCandidateRebaseStoreError(err)
	}
	return store.Get(ctx, command.ProjectID, command.RebaseID)
}

func (store *CandidateRebaseStore) FlagEvidenceMatches(
	ctx context.Context,
	projectID, candidateID string,
	candidateVersion uint64,
	conflicted bool,
	rebaseID, planHash string,
) (bool, error) {
	if ctx == nil || !validUUID(projectID) || !validUUID(candidateID) || !validUUID(rebaseID) ||
		candidateVersion == 0 || !isCanonicalSHA256(planHash) {
		return false, ErrInvalidRebase
	}
	var matches bool
	err := store.database.WithContext(ctx).Raw(`
SELECT EXISTS (
  SELECT 1
  FROM candidate_workspace_control_events AS event
  JOIN candidate_workspaces AS candidate ON candidate.id = event.candidate_id
  WHERE candidate.project_id = ?
    AND event.candidate_id = ?
    AND event.candidate_version_to = ?
    AND event.event_kind = 'candidate.flags_updated'
    AND event.conflicted_to = ?
    AND event.stale_to = false
    AND event.rebase_required_to = false
    AND event.evidence_ref = ?
    AND event.evidence_hash = ?
)
`, projectID, candidateID, int64(candidateVersion), conflicted,
		candidateRebaseEvidenceRef(rebaseID), planHash).Scan(&matches).Error
	if err != nil {
		return false, fmt.Errorf("verify Candidate rebase flag evidence: %w", err)
	}
	return matches, nil
}

func (store *CandidateRebaseStore) LoadBaseTree(
	ctx context.Context,
	candidate CandidateWorkspace,
) (TreeManifest, error) {
	if ctx == nil || candidate.Validate() != nil {
		return TreeManifest{}, ErrInvalidRebase
	}
	var row struct {
		ID                    uuid.UUID `gorm:"column:id"`
		ProjectID             uuid.UUID `gorm:"column:project_id"`
		BuildManifestID       uuid.UUID `gorm:"column:build_manifest_id"`
		BuildManifestHash     string    `gorm:"column:build_manifest_hash"`
		BuildContractID       uuid.UUID `gorm:"column:build_contract_id"`
		BuildContractHash     string    `gorm:"column:build_contract_hash"`
		FullStackTemplateID   uuid.UUID `gorm:"column:full_stack_template_id"`
		FullStackTemplateHash string    `gorm:"column:full_stack_template_hash"`
		TreeStore             string    `gorm:"column:tree_store"`
		TreeOwnerID           uuid.UUID `gorm:"column:tree_owner_id"`
		TreeRef               string    `gorm:"column:tree_ref"`
		TreeContentHash       string    `gorm:"column:tree_content_hash"`
		TreeHash              string    `gorm:"column:tree_hash"`
		TreeFileCount         int       `gorm:"column:tree_file_count"`
		TreeByteSize          int64     `gorm:"column:tree_byte_size"`
	}
	result := store.database.WithContext(ctx).Raw(`
SELECT id, project_id, build_manifest_id, build_manifest_hash,
       build_contract_id, build_contract_hash,
       full_stack_template_id, full_stack_template_hash,
       tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
       tree_file_count, tree_byte_size
FROM repository_snapshots
WHERE project_id = ? AND id = ?
`, candidate.ProjectID, candidate.RepositorySnapshotID).Scan(&row)
	if result.Error != nil {
		return TreeManifest{}, fmt.Errorf("load Candidate ancestor snapshot: %w", result.Error)
	}
	if result.RowsAffected != 1 || row.ID.String() != candidate.RepositorySnapshotID ||
		row.ProjectID.String() != candidate.ProjectID || row.BuildManifestID.String() != candidate.BuildManifest.ID ||
		row.BuildManifestHash != candidate.BuildManifest.ContentHash || row.BuildContractID.String() != candidate.BuildContract.ID ||
		row.BuildContractHash != candidate.BuildContract.ContentHash ||
		row.FullStackTemplateID.String() != candidate.FullStackTemplate.ID ||
		row.FullStackTemplateHash != candidate.FullStackTemplate.ContentHash || row.TreeHash != candidate.BaseTreeHash {
		return TreeManifest{}, rebaseStoreContract("ancestor snapshot differs from Candidate lineage", nil)
	}
	pointer := TreeBlobPointer{
		Store: row.TreeStore, OwnerID: row.TreeOwnerID.String(), Ref: row.TreeRef,
		ContentObjectHash: row.TreeContentHash, TreeHash: row.TreeHash,
		FileCount: row.TreeFileCount, ByteSize: row.TreeByteSize,
	}
	if err := pointer.validate(); err != nil {
		return TreeManifest{}, rebaseStoreContract("ancestor snapshot tree pointer is invalid", err)
	}
	tree, err := store.candidates.trees.Get(ctx, candidate.ProjectID, pointer.OwnerID, pointer)
	if err != nil {
		return TreeManifest{}, fmt.Errorf("hydrate Candidate ancestor tree: %w", err)
	}
	if tree, err = ParseTree(tree); err != nil || tree.TreeHash != candidate.BaseTreeHash {
		return TreeManifest{}, rebaseStoreContract("hydrated ancestor tree differs from Candidate base", err)
	}
	return tree, nil
}

func (store *CandidateRebaseStore) hydrate(
	ctx context.Context,
	database *gorm.DB,
	row candidateRebaseRow,
) (CandidateRebase, error) {
	if row.ID == uuid.Nil || row.ProjectID == uuid.Nil || row.PredecessorCandidateID == uuid.Nil ||
		row.SuccessorCandidateID == uuid.Nil || row.TargetBuildManifestID == uuid.Nil || row.CreatedBy == uuid.Nil ||
		row.SchemaVersion != CandidateRebaseSchemaVersion || !bootstrapOperationPattern.MatchString(row.OperationID) ||
		!isCanonicalSHA256(row.AncestorTreeHash) || !isCanonicalSHA256(row.PredecessorTreeHash) ||
		!isCanonicalSHA256(row.TargetTreeHash) || !isCanonicalSHA256(row.PlannedTreeHash) ||
		!isCanonicalSHA256(row.PlanHash) || row.Version <= 0 || row.CreatedAt.IsZero() ||
		row.UpdatedAt.Before(row.CreatedAt) {
		return CandidateRebase{}, rebaseStoreContract("invalid Candidate rebase row", nil)
	}
	state := CandidateRebaseState(row.State)
	if state != CandidateRebaseApplying && state != CandidateRebaseConflicted && state != CandidateRebaseReady {
		return CandidateRebase{}, rebaseStoreContract("invalid Candidate rebase state", nil)
	}
	var operationRows []candidateRebaseOperationRow
	if err := database.WithContext(ctx).Where("rebase_id = ?", row.ID).Order("ordinal ASC").Find(&operationRows).Error; err != nil {
		return CandidateRebase{}, fmt.Errorf("load Candidate rebase operations: %w", err)
	}
	operations := make([]CandidateRebaseOperation, len(operationRows))
	for index, operationRow := range operationRows {
		if operationRow.RebaseID != row.ID || operationRow.Ordinal != index {
			return CandidateRebase{}, rebaseStoreContract("non-contiguous Candidate rebase operation plan", nil)
		}
		var operation FileOperation
		if err := decodeExactJSON(operationRow.Operation, &operation); err != nil {
			return CandidateRebase{}, rebaseStoreContract("decode Candidate rebase operation", err)
		}
		normalized, err := NormalizeOperation(operation)
		if err != nil || normalized != operation || operation.ID != operationRow.OperationID {
			return CandidateRebase{}, rebaseStoreContract("invalid Candidate rebase operation", err)
		}
		operations[index] = CandidateRebaseOperation{Ordinal: index, Operation: operation}
	}

	var conflictRows []candidateRebaseConflictRow
	if err := database.WithContext(ctx).Where("rebase_id = ?", row.ID).Order("ordinal ASC").Find(&conflictRows).Error; err != nil {
		return CandidateRebase{}, fmt.Errorf("load Candidate rebase conflicts: %w", err)
	}
	conflicts := make([]CandidateRebaseConflictRecord, len(conflictRows))
	planConflicts := make([]CandidateRebaseConflict, len(conflictRows))
	openConflicts := 0
	for index, conflictRow := range conflictRows {
		conflict, err := candidateRebaseConflictFromRow(row.ID, index, conflictRow)
		if err != nil {
			return CandidateRebase{}, err
		}
		conflicts[index] = conflict
		planConflicts[index] = conflict.CandidateRebaseConflict
		if conflict.State == CandidateRebaseConflictOpen {
			openConflicts++
		}
	}
	if (state == CandidateRebaseConflicted && openConflicts == 0) ||
		(state == CandidateRebaseReady && openConflicts != 0) {
		return CandidateRebase{}, rebaseStoreContract("Candidate rebase state disagrees with conflicts", nil)
	}
	plan := CandidateRebasePlan{
		SchemaVersion: CandidateRebasePlanSchemaVersion, RebaseID: row.ID.String(),
		AncestorTreeHash: row.AncestorTreeHash, PredecessorTreeHash: row.PredecessorTreeHash,
		TargetTreeHash: row.TargetTreeHash, PlannedTreeHash: row.PlannedTreeHash,
		Operations: operations, Conflicts: planConflicts,
	}
	computedHash, err := candidateRebasePlanHash(plan)
	if err != nil || computedHash != row.PlanHash {
		return CandidateRebase{}, rebaseStoreContract("Candidate rebase plan hash drifted", err)
	}
	return CandidateRebase{
		SchemaVersion: row.SchemaVersion, ID: row.ID.String(), ProjectID: row.ProjectID.String(),
		OperationID: row.OperationID, PredecessorCandidateID: row.PredecessorCandidateID.String(),
		SuccessorCandidateID: row.SuccessorCandidateID.String(), TargetBuildManifestID: row.TargetBuildManifestID.String(),
		AncestorTreeHash: row.AncestorTreeHash, PredecessorTreeHash: row.PredecessorTreeHash,
		TargetTreeHash: row.TargetTreeHash, PlannedTreeHash: row.PlannedTreeHash, PlanHash: row.PlanHash,
		State: state, Version: uint64(row.Version), Operations: operations, Conflicts: conflicts,
		CreatedBy: row.CreatedBy.String(), CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}, nil
}

func candidateRebaseConflictFromRow(
	rebaseID uuid.UUID,
	index int,
	row candidateRebaseConflictRow,
) (CandidateRebaseConflictRecord, error) {
	if row.ID == uuid.Nil || row.RebaseID != rebaseID || row.SchemaVersion != CandidateRebaseConflictSchemaVersion ||
		row.Ordinal != index || row.Path == "" || row.Version <= 0 || row.CreatedAt.IsZero() {
		return CandidateRebaseConflictRecord{}, rebaseStoreContract("invalid Candidate rebase conflict row", nil)
	}
	if normalized, err := NormalizePath(row.Path); err != nil || normalized != row.Path {
		return CandidateRebaseConflictRecord{}, rebaseStoreContract("invalid Candidate rebase conflict path", err)
	}
	ancestor, err := decodeNullableTreeFile(row.AncestorFile, row.Path)
	if err != nil {
		return CandidateRebaseConflictRecord{}, err
	}
	predecessor, err := decodeNullableTreeFile(row.PredecessorFile, row.Path)
	if err != nil {
		return CandidateRebaseConflictRecord{}, err
	}
	target, err := decodeNullableTreeFile(row.TargetFile, row.Path)
	if err != nil {
		return CandidateRebaseConflictRecord{}, err
	}
	if equalTreeFile(predecessor, ancestor) || equalTreeFile(target, ancestor) || equalTreeFile(predecessor, target) {
		return CandidateRebaseConflictRecord{}, rebaseStoreContract("persisted conflict is not a divergent three-way change", nil)
	}
	record := CandidateRebaseConflictRecord{
		CandidateRebaseConflict: CandidateRebaseConflict{
			SchemaVersion: row.SchemaVersion, ID: row.ID.String(), Ordinal: row.Ordinal, Path: row.Path,
			AncestorFile: ancestor, PredecessorFile: predecessor, TargetFile: target,
		},
		State: CandidateRebaseConflictState(row.State), Version: uint64(row.Version), CreatedAt: row.CreatedAt.UTC(),
	}
	switch record.State {
	case CandidateRebaseConflictOpen:
		if record.Version != 1 || row.ResolutionStrategy.Valid || row.ResolutionContentHash.Valid ||
			row.ResolutionDeleted.Valid || len(row.ResolutionFile) != 0 || row.ResolvedBy != nil || row.ResolvedAt != nil {
			return CandidateRebaseConflictRecord{}, rebaseStoreContract("open conflict contains resolution fields", nil)
		}
	case CandidateRebaseConflictResolved:
		if record.Version != 2 || !row.ResolutionStrategy.Valid || !row.ResolutionDeleted.Valid ||
			row.ResolvedBy == nil || *row.ResolvedBy == uuid.Nil || row.ResolvedAt == nil || row.ResolvedAt.IsZero() {
			return CandidateRebaseConflictRecord{}, rebaseStoreContract("resolved conflict lacks exact audit fields", nil)
		}
		strategy := CandidateRebaseResolutionStrategy(row.ResolutionStrategy.String)
		if strategy != CandidateRebaseUsePredecessor && strategy != CandidateRebaseUseTarget && strategy != CandidateRebaseUseCurrent {
			return CandidateRebaseConflictRecord{}, rebaseStoreContract("invalid conflict resolution strategy", nil)
		}
		record.ResolutionStrategy = &strategy
		deleted := row.ResolutionDeleted.Bool
		record.ResolutionDeleted = &deleted
		if deleted {
			if row.ResolutionContentHash.Valid || len(row.ResolutionFile) != 0 {
				return CandidateRebaseConflictRecord{}, rebaseStoreContract("deleted resolution contains a file", nil)
			}
		} else {
			file, err := decodeNullableTreeFile(row.ResolutionFile, row.Path)
			if err != nil || file == nil || !row.ResolutionContentHash.Valid ||
				file.ContentHash != row.ResolutionContentHash.String {
				return CandidateRebaseConflictRecord{}, rebaseStoreContract("invalid resolved conflict file", err)
			}
			record.ResolutionFile = file
		}
		if (strategy == CandidateRebaseUsePredecessor && !equalTreeFile(record.ResolutionFile, predecessor)) ||
			(strategy == CandidateRebaseUseTarget && !equalTreeFile(record.ResolutionFile, target)) {
			return CandidateRebaseConflictRecord{}, rebaseStoreContract("resolution strategy differs from its exact source file", nil)
		}
		record.ResolvedBy = row.ResolvedBy.String()
		resolvedAt := row.ResolvedAt.UTC()
		record.ResolvedAt = &resolvedAt
	default:
		return CandidateRebaseConflictRecord{}, rebaseStoreContract("invalid conflict state", nil)
	}
	return record, nil
}

func validateCreateCandidateRebaseCommand(command CreateCandidateRebaseCommand) error {
	rebase := command.Rebase
	if rebase.SchemaVersion != CandidateRebaseSchemaVersion || !validUUID(rebase.ID) || !validUUID(rebase.ProjectID) ||
		!validUUID(rebase.PredecessorCandidateID) || !validUUID(rebase.SuccessorCandidateID) ||
		!validUUID(rebase.TargetBuildManifestID) || !validUUID(rebase.CreatedBy) ||
		rebase.PredecessorCandidateID == rebase.SuccessorCandidateID ||
		!bootstrapOperationPattern.MatchString(rebase.OperationID) || rebase.State != CandidateRebaseApplying ||
		rebase.Version != 1 || rebase.CreatedAt.IsZero() || !rebase.UpdatedAt.Equal(rebase.CreatedAt) ||
		command.ExpectedCandidateVersion == 0 || command.ExpectedSessionEpoch == 0 ||
		!postgresBigint(command.ExpectedCandidateVersion) || !postgresBigint(command.ExpectedSessionEpoch) ||
		!postgresBigint(command.ExpectedWriterLeaseEpoch) {
		return ErrInvalidRebase
	}
	plan := command.Plan
	if plan.SchemaVersion != CandidateRebasePlanSchemaVersion || plan.RebaseID != rebase.ID ||
		plan.AncestorTreeHash != rebase.AncestorTreeHash || plan.PredecessorTreeHash != rebase.PredecessorTreeHash ||
		plan.TargetTreeHash != rebase.TargetTreeHash || plan.PlannedTreeHash != rebase.PlannedTreeHash ||
		plan.PlanHash != rebase.PlanHash || !isCanonicalSHA256(plan.PlanHash) {
		return ErrInvalidRebase
	}
	computedHash, err := candidateRebasePlanHash(plan)
	if err != nil || computedHash != plan.PlanHash {
		return fmt.Errorf("%w: plan hash", ErrInvalidRebase)
	}
	for index, operation := range plan.Operations {
		normalized, normalizeErr := NormalizeOperation(operation.Operation)
		if normalizeErr != nil || operation.Ordinal != index || normalized != operation.Operation {
			return fmt.Errorf("%w: operation %d", ErrInvalidRebase, index)
		}
	}
	for index, conflict := range plan.Conflicts {
		if conflict.SchemaVersion != CandidateRebaseConflictSchemaVersion || !validUUID(conflict.ID) ||
			conflict.Ordinal != index || conflict.ID != deterministicRebaseUUID(rebase.ID, "conflict", conflict.Path) ||
			equalTreeFile(conflict.PredecessorFile, conflict.AncestorFile) ||
			equalTreeFile(conflict.TargetFile, conflict.AncestorFile) ||
			equalTreeFile(conflict.PredecessorFile, conflict.TargetFile) {
			return fmt.Errorf("%w: conflict %d", ErrInvalidRebase, index)
		}
	}
	return nil
}

func validateResolveCandidateRebaseConflictCommand(command ResolveCandidateRebaseConflictCommand) error {
	if !validUUID(command.ProjectID) || !validUUID(command.RebaseID) || !validUUID(command.ConflictID) ||
		!validUUID(command.ActorID) || command.ExpectedConflictVersion != 1 ||
		command.ExpectedSuccessorCandidateVersion == 0 || command.ExpectedSessionEpoch == 0 ||
		!postgresBigint(command.ExpectedSuccessorCandidateVersion) || !postgresBigint(command.ExpectedSessionEpoch) ||
		!postgresBigint(command.ExpectedWriterLeaseEpoch) ||
		(command.Strategy != CandidateRebaseUsePredecessor && command.Strategy != CandidateRebaseUseTarget &&
			command.Strategy != CandidateRebaseUseCurrent) {
		return ErrInvalidRebase
	}
	if command.ResolutionFile != nil {
		file, err := normalizeTreeFile(*command.ResolutionFile)
		if err != nil || file != *command.ResolutionFile {
			return fmt.Errorf("%w: resolution file", ErrInvalidRebase)
		}
	}
	return nil
}

func nullableTreeFileJSON(file *TreeFile) (any, error) {
	if file == nil {
		return nil, nil
	}
	normalized, err := normalizeTreeFile(*file)
	if err != nil || normalized != *file {
		return nil, fmt.Errorf("%w: non-canonical tree file", ErrInvalidRebase)
	}
	payload, err := domain.CanonicalJSON(normalized)
	if err != nil {
		return nil, fmt.Errorf("%w: encode tree file: %v", ErrInvalidRebase, err)
	}
	return string(payload), nil
}

func decodeNullableTreeFile(payload json.RawMessage, expectedPath string) (*TreeFile, error) {
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		return nil, nil
	}
	var file TreeFile
	if err := decodeExactJSON(payload, &file); err != nil {
		return nil, rebaseStoreContract("decode conflict tree file", err)
	}
	normalized, err := normalizeTreeFile(file)
	if err != nil || normalized != file || file.Path != expectedPath {
		return nil, rebaseStoreContract("invalid conflict tree file", err)
	}
	return &file, nil
}

func decodeExactJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func candidateRebaseEvidenceRef(rebaseID string) string {
	return "candidate-rebase:" + strings.TrimSpace(rebaseID)
}

func mapCandidateRebaseStoreError(err error) error {
	if err == nil || errors.Is(err, ErrInvalidRebase) || errors.Is(err, ErrCandidateRebaseNotFound) ||
		errors.Is(err, ErrCandidateRebaseConflictNotFound) || errors.Is(err, ErrCandidateRebaseReplay) ||
		errors.Is(err, ErrCandidateRebaseState) || errors.Is(err, ErrCandidateNotFound) {
		return err
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return errors.Join(ErrCandidateRebaseReplay, err)
		case "40001", "40P01", "55000", "23514", "23503":
			return errors.Join(ErrCandidateRebaseState, err)
		case "22001", "22003", "22023", "22P02", "23502":
			return errors.Join(ErrInvalidRebase, err)
		}
	}
	return fmt.Errorf("Candidate rebase persistence: %w", err)
}

func rebaseStoreContract(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrCandidateRebaseStoreContract, message)
	}
	return errors.Join(ErrCandidateRebaseStoreContract, fmt.Errorf("%s: %w", message, cause))
}
