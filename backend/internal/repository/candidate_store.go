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
)

const defaultAppendRecoveryTimeout = 2 * time.Second

// CandidateTreeReader is the narrow content boundary needed to hydrate the
// semantic CurrentTree from the complete pointer stored by PostgreSQL.
type CandidateTreeReader interface {
	Get(context.Context, string, string, TreeBlobPointer) (TreeManifest, error)
}

type candidateTransactionRunner func(context.Context, func(*gorm.DB) error) error

type committedOperationLookup func(
	context.Context,
	string,
	string,
	string,
) (CommittedMutation, bool, error)

// GORMCandidateStore implements CandidateStore against the append-only
// CandidateWorkspace journal introduced by migration 000023.
type GORMCandidateStore struct {
	database        *gorm.DB
	trees           CandidateTreeReader
	runTransaction  candidateTransactionRunner
	recoveryLookup  committedOperationLookup
	recoveryTimeout time.Duration
}

func NewGORMCandidateStore(database *gorm.DB, trees CandidateTreeReader) (*GORMCandidateStore, error) {
	if database == nil || trees == nil {
		return nil, errors.New("repository candidate database and tree reader are required")
	}
	store := &GORMCandidateStore{
		database:        database,
		trees:           trees,
		recoveryTimeout: defaultAppendRecoveryTimeout,
	}
	store.runTransaction = func(ctx context.Context, operation func(*gorm.DB) error) error {
		return database.WithContext(ctx).Transaction(operation)
	}
	store.recoveryLookup = func(
		ctx context.Context,
		projectID, candidateID, operationID string,
	) (CommittedMutation, bool, error) {
		return store.findCommittedOperation(ctx, database, projectID, candidateID, operationID)
	}
	return store, nil
}

type candidateWorkspaceRow struct {
	ID                       uuid.UUID  `gorm:"column:id"`
	SchemaVersion            string     `gorm:"column:schema_version"`
	ProjectID                uuid.UUID  `gorm:"column:project_id"`
	RepositorySnapshotID     uuid.UUID  `gorm:"column:repository_snapshot_id"`
	BuildManifestID          uuid.UUID  `gorm:"column:build_manifest_id"`
	BuildManifestHash        string     `gorm:"column:build_manifest_hash"`
	BuildContractID          uuid.UUID  `gorm:"column:build_contract_id"`
	BuildContractHash        string     `gorm:"column:build_contract_hash"`
	FullStackTemplateID      uuid.UUID  `gorm:"column:full_stack_template_id"`
	FullStackTemplateHash    string     `gorm:"column:full_stack_template_hash"`
	BaseWorkspaceArtifactID  *uuid.UUID `gorm:"column:base_workspace_artifact_id"`
	BaseWorkspaceRevisionID  *uuid.UUID `gorm:"column:base_workspace_revision_id"`
	BaseWorkspaceContentHash *string    `gorm:"column:base_workspace_content_hash"`
	BaseTreeHash             string     `gorm:"column:base_tree_hash"`
	CurrentTreeStore         string     `gorm:"column:current_tree_store"`
	CurrentTreeOwnerID       uuid.UUID  `gorm:"column:current_tree_owner_id"`
	CurrentTreeRef           string     `gorm:"column:current_tree_ref"`
	CurrentTreeContentHash   string     `gorm:"column:current_tree_content_hash"`
	CurrentTreeHash          string     `gorm:"column:current_tree_hash"`
	CurrentTreeFileCount     int        `gorm:"column:current_tree_file_count"`
	CurrentTreeByteSize      int64      `gorm:"column:current_tree_byte_size"`
	Status                   string     `gorm:"column:status"`
	Dirty                    bool       `gorm:"column:dirty"`
	Conflicted               bool       `gorm:"column:conflicted"`
	Stale                    bool       `gorm:"column:stale"`
	RebaseRequired           bool       `gorm:"column:rebase_required"`
	SessionEpoch             int64      `gorm:"column:session_epoch"`
	Version                  int64      `gorm:"column:version"`
	JournalSequence          int64      `gorm:"column:journal_sequence"`
	WriterLeaseOwnerID       *uuid.UUID `gorm:"column:writer_lease_owner_id"`
	WriterLeaseEpoch         int64      `gorm:"column:writer_lease_epoch"`
	WriterLeaseExpiresAt     *time.Time `gorm:"column:writer_lease_expires_at"`
	CreatedBy                uuid.UUID  `gorm:"column:created_by"`
	CreatedAt                time.Time  `gorm:"column:created_at"`
	UpdatedAt                time.Time  `gorm:"column:updated_at"`
}

func (candidateWorkspaceRow) TableName() string { return "candidate_workspaces" }

func (store *GORMCandidateStore) LoadMutationCandidate(
	ctx context.Context,
	projectID, candidateID string,
) (CandidateMutationRecord, error) {
	if !validUUID(projectID) || !validUUID(candidateID) {
		return CandidateMutationRecord{}, ErrInvalidCandidate
	}
	projectUUID, _ := uuid.Parse(strings.TrimSpace(projectID))
	candidateUUID, _ := uuid.Parse(strings.TrimSpace(candidateID))
	var row candidateWorkspaceRow
	err := store.database.WithContext(ctx).Where(
		"project_id = ? AND id = ?", projectUUID, candidateUUID,
	).Take(&row).Error
	if err != nil {
		return CandidateMutationRecord{}, fmt.Errorf("load repository candidate row: %w", err)
	}

	pointer := TreeBlobPointer{
		Store: row.CurrentTreeStore, Ref: row.CurrentTreeRef,
		OwnerID: row.CurrentTreeOwnerID.String(), TreeHash: row.CurrentTreeHash,
		FileCount: row.CurrentTreeFileCount, ByteSize: row.CurrentTreeByteSize,
		ContentObjectHash: row.CurrentTreeContentHash,
	}
	if err := pointer.validate(); err != nil {
		return CandidateMutationRecord{}, candidateStoreContract("invalid persisted current tree pointer", err)
	}
	tree, err := store.trees.Get(ctx, row.ProjectID.String(), pointer.OwnerID, pointer)
	if err != nil {
		return CandidateMutationRecord{}, fmt.Errorf("hydrate repository candidate tree: %w", err)
	}
	tree, err = ParseTree(tree)
	if err != nil {
		return CandidateMutationRecord{}, candidateStoreContract("tree reader returned an invalid manifest", err)
	}
	if tree.TreeHash != pointer.TreeHash || len(tree.Files) != pointer.FileCount || treeByteSize(tree) != pointer.ByteSize {
		return CandidateMutationRecord{}, errors.Join(
			ErrCandidateTreeDrift,
			candidateStoreContract("hydrated tree facts differ from the persisted pointer", nil),
		)
	}
	candidate, err := candidateFromWorkspaceRow(row, tree)
	if err != nil {
		return CandidateMutationRecord{}, err
	}
	return CandidateMutationRecord{Candidate: candidate, CurrentTreePointer: pointer}, nil
}

func candidateFromWorkspaceRow(row candidateWorkspaceRow, tree TreeManifest) (CandidateWorkspace, error) {
	if row.ID == uuid.Nil || row.ProjectID == uuid.Nil || row.RepositorySnapshotID == uuid.Nil ||
		row.BuildManifestID == uuid.Nil || row.BuildContractID == uuid.Nil || row.FullStackTemplateID == uuid.Nil ||
		row.CreatedBy == uuid.Nil || row.SessionEpoch <= 0 || row.Version <= 0 || row.JournalSequence < 0 ||
		row.WriterLeaseEpoch < 0 {
		return CandidateWorkspace{}, candidateStoreContract("invalid persisted candidate scalar", nil)
	}
	baseRevision, err := candidateBaseRevision(row)
	if err != nil {
		return CandidateWorkspace{}, err
	}
	var lease *WriterLease
	if row.WriterLeaseOwnerID == nil && row.WriterLeaseExpiresAt == nil {
		lease = nil
	} else if row.WriterLeaseOwnerID == nil || row.WriterLeaseExpiresAt == nil || *row.WriterLeaseOwnerID == uuid.Nil {
		return CandidateWorkspace{}, candidateStoreContract("invalid persisted writer lease shape", nil)
	} else {
		lease = &WriterLease{
			OwnerID: row.WriterLeaseOwnerID.String(), Epoch: uint64(row.WriterLeaseEpoch),
			ExpiresAt: row.WriterLeaseExpiresAt.UTC(),
		}
	}
	candidate := CandidateWorkspace{
		SchemaVersion: row.SchemaVersion, ID: row.ID.String(), ProjectID: row.ProjectID.String(),
		RepositorySnapshotID:  row.RepositorySnapshotID.String(),
		BuildManifest:         ExactReference{ID: row.BuildManifestID.String(), ContentHash: row.BuildManifestHash},
		BuildContract:         ExactReference{ID: row.BuildContractID.String(), ContentHash: row.BuildContractHash},
		FullStackTemplate:     ExactReference{ID: row.FullStackTemplateID.String(), ContentHash: row.FullStackTemplateHash},
		BaseWorkspaceRevision: baseRevision, BaseTreeHash: row.BaseTreeHash, CurrentTree: tree,
		Status: CandidateStatus(row.Status), Dirty: row.Dirty, Conflicted: row.Conflicted,
		Stale: row.Stale, RebaseRequired: row.RebaseRequired,
		SessionEpoch: uint64(row.SessionEpoch), Version: uint64(row.Version),
		JournalSequence: uint64(row.JournalSequence), WriterLeaseEpoch: uint64(row.WriterLeaseEpoch),
		Lease: lease, CreatedBy: row.CreatedBy.String(), CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
	if err := candidate.Validate(); err != nil {
		return CandidateWorkspace{}, candidateStoreContract("invalid persisted candidate aggregate", err)
	}
	return candidate, nil
}

func candidateBaseRevision(row candidateWorkspaceRow) (*ExactRevisionReference, error) {
	if row.BaseWorkspaceArtifactID == nil && row.BaseWorkspaceRevisionID == nil && row.BaseWorkspaceContentHash == nil {
		return nil, nil
	}
	if row.BaseWorkspaceArtifactID == nil || row.BaseWorkspaceRevisionID == nil || row.BaseWorkspaceContentHash == nil ||
		*row.BaseWorkspaceArtifactID == uuid.Nil || *row.BaseWorkspaceRevisionID == uuid.Nil {
		return nil, candidateStoreContract("invalid persisted base workspace revision shape", nil)
	}
	return &ExactRevisionReference{
		ArtifactID: row.BaseWorkspaceArtifactID.String(), RevisionID: row.BaseWorkspaceRevisionID.String(),
		ContentHash: *row.BaseWorkspaceContentHash,
	}, nil
}

type candidateJournalRow struct {
	ProjectID             uuid.UUID      `gorm:"column:project_id"`
	CandidateID           uuid.UUID      `gorm:"column:candidate_id"`
	Sequence              int64          `gorm:"column:sequence"`
	CandidateVersionFrom  int64          `gorm:"column:candidate_version_from"`
	CandidateVersionTo    int64          `gorm:"column:candidate_version_to"`
	SessionEpoch          int64          `gorm:"column:session_epoch"`
	WriterLeaseEpoch      int64          `gorm:"column:writer_lease_epoch"`
	ActorID               uuid.UUID      `gorm:"column:actor_id"`
	Attribution           string         `gorm:"column:attribution"`
	OperationID           string         `gorm:"column:operation_id"`
	OperationKind         string         `gorm:"column:operation_kind"`
	Path                  string         `gorm:"column:path"`
	FromPath              sql.NullString `gorm:"column:from_path"`
	ExpectedContentHash   sql.NullString `gorm:"column:expected_content_hash"`
	ContentHash           sql.NullString `gorm:"column:content_hash"`
	ByteSize              sql.NullInt64  `gorm:"column:byte_size"`
	FileMode              sql.NullString `gorm:"column:file_mode"`
	BeforeTreeStore       string         `gorm:"column:before_tree_store"`
	BeforeTreeOwnerID     uuid.UUID      `gorm:"column:before_tree_owner_id"`
	BeforeTreeRef         string         `gorm:"column:before_tree_ref"`
	BeforeTreeContentHash string         `gorm:"column:before_tree_content_hash"`
	BeforeTreeHash        string         `gorm:"column:before_tree_hash"`
	BeforeTreeFileCount   sql.NullInt64  `gorm:"column:before_tree_file_count"`
	BeforeTreeByteSize    sql.NullInt64  `gorm:"column:before_tree_byte_size"`
	AfterTreeStore        string         `gorm:"column:after_tree_store"`
	AfterTreeOwnerID      uuid.UUID      `gorm:"column:after_tree_owner_id"`
	AfterTreeRef          string         `gorm:"column:after_tree_ref"`
	AfterTreeContentHash  string         `gorm:"column:after_tree_content_hash"`
	AfterTreeHash         string         `gorm:"column:after_tree_hash"`
	AfterTreeFileCount    int64          `gorm:"column:after_tree_file_count"`
	AfterTreeByteSize     int64          `gorm:"column:after_tree_byte_size"`
	CreatedAt             time.Time      `gorm:"column:created_at"`
}

const committedOperationQuery = `
SELECT candidate.project_id,
       journal.candidate_id, journal.sequence,
       journal.candidate_version_from, journal.candidate_version_to,
       journal.session_epoch, journal.writer_lease_epoch,
       journal.actor_id, journal.attribution,
       journal.operation_id, journal.operation_kind, journal.path, journal.from_path,
       journal.expected_content_hash, journal.content_hash, journal.byte_size, journal.file_mode,
       journal.before_tree_store, journal.before_tree_owner_id,
       journal.before_tree_ref, journal.before_tree_content_hash, journal.before_tree_hash,
       CASE WHEN journal.sequence = 1
         THEN snapshot.tree_file_count ELSE previous.after_tree_file_count
       END AS before_tree_file_count,
       CASE WHEN journal.sequence = 1
         THEN snapshot.tree_byte_size ELSE previous.after_tree_byte_size
       END AS before_tree_byte_size,
       journal.after_tree_store, journal.after_tree_owner_id,
       journal.after_tree_ref, journal.after_tree_content_hash, journal.after_tree_hash,
       journal.after_tree_file_count, journal.after_tree_byte_size,
       journal.created_at
FROM candidate_workspace_journal AS journal
JOIN candidate_workspaces AS candidate ON candidate.id = journal.candidate_id
LEFT JOIN repository_snapshots AS snapshot
  ON snapshot.id = candidate.repository_snapshot_id
 AND snapshot.project_id = candidate.project_id
LEFT JOIN candidate_workspace_journal AS previous
  ON previous.candidate_id = journal.candidate_id
 AND previous.sequence = journal.sequence - 1
WHERE candidate.project_id = @project_id
  AND journal.candidate_id = @candidate_id
  AND journal.operation_id = @operation_id
LIMIT 1`

func (store *GORMCandidateStore) FindCommittedOperation(
	ctx context.Context,
	projectID, candidateID, operationID string,
) (CommittedMutation, bool, error) {
	return store.findCommittedOperation(ctx, store.database, projectID, candidateID, operationID)
}

func (store *GORMCandidateStore) findCommittedOperation(
	ctx context.Context,
	database *gorm.DB,
	projectID, candidateID, operationID string,
) (CommittedMutation, bool, error) {
	if !validUUID(projectID) || !validUUID(candidateID) || !validOperationID(operationID) {
		return CommittedMutation{}, false, ErrInvalidMutation
	}
	projectUUID, _ := uuid.Parse(strings.TrimSpace(projectID))
	candidateUUID, _ := uuid.Parse(strings.TrimSpace(candidateID))
	var row candidateJournalRow
	result := database.WithContext(ctx).Raw(committedOperationQuery, map[string]any{
		"project_id": projectUUID, "candidate_id": candidateUUID, "operation_id": operationID,
	}).Scan(&row)
	if result.Error != nil {
		return CommittedMutation{}, false, fmt.Errorf("find committed repository operation row: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return CommittedMutation{}, false, nil
	}
	committed, err := committedMutationFromJournalRow(row)
	if err != nil {
		return CommittedMutation{}, false, err
	}
	return committed, true, nil
}

func committedMutationFromJournalRow(row candidateJournalRow) (CommittedMutation, error) {
	if row.ProjectID == uuid.Nil || row.CandidateID == uuid.Nil || row.ActorID == uuid.Nil ||
		row.Sequence <= 0 || row.CandidateVersionFrom <= 0 || row.CandidateVersionTo <= 0 ||
		row.SessionEpoch <= 0 || row.WriterLeaseEpoch <= 0 || row.CreatedAt.IsZero() ||
		!row.BeforeTreeFileCount.Valid || !row.BeforeTreeByteSize.Valid ||
		row.BeforeTreeFileCount.Int64 < 0 || row.BeforeTreeByteSize.Int64 < 0 ||
		row.BeforeTreeFileCount.Int64 > MaxTreeFiles || row.BeforeTreeByteSize.Int64 > MaxTreeBytes ||
		row.AfterTreeFileCount < 0 || row.AfterTreeByteSize < 0 ||
		row.AfterTreeFileCount > MaxTreeFiles || row.AfterTreeByteSize > MaxTreeBytes ||
		!validAttribution(row.Attribution) {
		return CommittedMutation{}, candidateStoreContract("invalid persisted journal scalar", nil)
	}
	operation, err := operationFromJournalRow(row)
	if err != nil {
		return CommittedMutation{}, err
	}
	before := TreeBlobPointer{
		Store: row.BeforeTreeStore, Ref: row.BeforeTreeRef, OwnerID: row.BeforeTreeOwnerID.String(),
		TreeHash: row.BeforeTreeHash, FileCount: int(row.BeforeTreeFileCount.Int64),
		ByteSize: row.BeforeTreeByteSize.Int64, ContentObjectHash: row.BeforeTreeContentHash,
	}
	after := TreeBlobPointer{
		Store: row.AfterTreeStore, Ref: row.AfterTreeRef, OwnerID: row.AfterTreeOwnerID.String(),
		TreeHash: row.AfterTreeHash, FileCount: int(row.AfterTreeFileCount),
		ByteSize: row.AfterTreeByteSize, ContentObjectHash: row.AfterTreeContentHash,
	}
	entry := JournalEntry{
		CandidateID: row.CandidateID.String(), Sequence: uint64(row.Sequence),
		CandidateFrom: uint64(row.CandidateVersionFrom), CandidateTo: uint64(row.CandidateVersionTo),
		SessionEpoch: uint64(row.SessionEpoch), LeaseEpoch: uint64(row.WriterLeaseEpoch),
		ActorID: row.ActorID.String(), Attribution: row.Attribution, Operation: operation,
		BeforeTree: row.BeforeTreeHash, AfterTree: row.AfterTreeHash, CreatedAt: row.CreatedAt.UTC(),
	}
	committed := CommittedMutation{ProjectID: row.ProjectID.String(), Entry: entry, BeforeTree: before, AfterTree: after}
	if err := validateCommittedMutation(committed); err != nil {
		return CommittedMutation{}, candidateStoreContract("invalid persisted committed mutation", err)
	}
	return committed, nil
}

func operationFromJournalRow(row candidateJournalRow) (FileOperation, error) {
	kind := OperationKind(row.OperationKind)
	switch kind {
	case OperationUpsert:
		if row.FromPath.Valid || !row.ContentHash.Valid || !row.ByteSize.Valid || !row.FileMode.Valid {
			return FileOperation{}, candidateStoreContract("invalid persisted upsert shape", nil)
		}
	case OperationDelete:
		if row.FromPath.Valid || row.ContentHash.Valid || row.ByteSize.Valid || row.FileMode.Valid {
			return FileOperation{}, candidateStoreContract("invalid persisted delete shape", nil)
		}
	case OperationRename:
		if !row.FromPath.Valid || row.ContentHash.Valid || row.ByteSize.Valid || row.FileMode.Valid {
			return FileOperation{}, candidateStoreContract("invalid persisted rename shape", nil)
		}
	default:
		return FileOperation{}, candidateStoreContract("invalid persisted operation kind", nil)
	}
	operation := FileOperation{
		ID: row.OperationID, Kind: kind, Path: row.Path,
		FromPath: row.FromPath.String, ExpectedHash: row.ExpectedContentHash.String,
		ContentHash: row.ContentHash.String, ByteSize: row.ByteSize.Int64, Mode: row.FileMode.String,
	}
	normalized, err := NormalizeOperation(operation)
	if err != nil || normalized != operation {
		return FileOperation{}, candidateStoreContract("persisted operation is not canonical", err)
	}
	return operation, nil
}

func (store *GORMCandidateStore) AppendOperation(
	ctx context.Context,
	command AppendOperationCommand,
) (CommittedMutation, error) {
	if err := validateAppendOperationCommand(command); err != nil {
		return CommittedMutation{}, err
	}
	var inserted CommittedMutation
	transactionErr := store.runTransaction(ctx, func(transaction *gorm.DB) error {
		if err := store.insertOperation(ctx, transaction, command); err != nil {
			return err
		}
		// Surface migration 000023's deferred chain guards before the commit
		// acknowledgement phase. This statement does not mutate the aggregate.
		if err := transaction.WithContext(ctx).Exec(`
SET CONSTRAINTS candidate_workspace_journal_parent_guard,
                candidate_workspace_journal_chain_guard IMMEDIATE`).Error; err != nil {
			return err
		}
		committed, found, err := store.findCommittedOperation(
			ctx, transaction, command.ProjectID, command.Entry.CandidateID, command.Entry.Operation.ID,
		)
		if err != nil {
			return err
		}
		if !found {
			return candidateStoreContract("inserted journal row could not be read back", nil)
		}
		if err := committed.matchesCommand(command); err != nil {
			return candidateStoreContract("inserted journal row differs from the server command", err)
		}
		inserted = committed
		return nil
	})
	if transactionErr == nil {
		if err := inserted.matchesCommand(command); err != nil {
			// A successful transaction with no exact read-back is unsafe to treat
			// as uncommitted because its after-tree may now be reachable.
			return CommittedMutation{}, errors.Join(
				ErrAppendOutcomeUnknown,
				candidateStoreContract("acknowledged append returned no exact committed row", err),
			)
		}
		return inserted, nil
	}

	return store.recoverAppendOutcome(ctx, command, transactionErr)
}

// AppendOperations commits one ordered operation batch in a single PostgreSQL
// transaction. Migration 000023's row triggers advance the Candidate after
// each insert, while the deferred chain guards prove the complete contiguous
// journal before the transaction can commit.
func (store *GORMCandidateStore) AppendOperations(
	ctx context.Context,
	commands []AppendOperationCommand,
) ([]CommittedMutation, error) {
	if err := validateAppendOperationCommands(commands); err != nil {
		return nil, err
	}
	inserted := make([]CommittedMutation, len(commands))
	transactionErr := store.runTransaction(ctx, func(transaction *gorm.DB) error {
		for _, command := range commands {
			if err := store.insertOperation(ctx, transaction, command); err != nil {
				return err
			}
		}
		if err := transaction.WithContext(ctx).Exec(`
SET CONSTRAINTS candidate_workspace_journal_parent_guard,
                candidate_workspace_journal_chain_guard IMMEDIATE`).Error; err != nil {
			return err
		}
		for index, command := range commands {
			committed, found, err := store.findCommittedOperation(
				ctx, transaction, command.ProjectID, command.Entry.CandidateID, command.Entry.Operation.ID,
			)
			if err != nil {
				return err
			}
			if !found {
				return candidateStoreContract("inserted batch journal row could not be read back", nil)
			}
			if err := committed.matchesCommand(command); err != nil {
				return candidateStoreContract("inserted batch journal row differs from the server command", err)
			}
			inserted[index] = committed
		}
		return nil
	})
	if transactionErr == nil {
		for index := range inserted {
			if err := inserted[index].matchesCommand(commands[index]); err != nil {
				return nil, errors.Join(
					ErrAppendOutcomeUnknown,
					candidateStoreContract("acknowledged batch append returned no exact committed row", err),
				)
			}
		}
		return inserted, nil
	}
	return store.recoverAppendOperationsOutcome(ctx, commands, transactionErr)
}

func (store *GORMCandidateStore) recoverAppendOperationsOutcome(
	ctx context.Context,
	commands []AppendOperationCommand,
	transactionErr error,
) ([]CommittedMutation, error) {
	base := ctx
	if base == nil {
		base = context.Background()
	} else {
		base = context.WithoutCancel(base)
	}
	timeout := store.recoveryTimeout
	if timeout <= 0 {
		timeout = defaultAppendRecoveryTimeout
	}
	recoveryContext, cancel := context.WithTimeout(base, timeout)
	defer cancel()
	committed := make([]CommittedMutation, len(commands))
	foundCount := 0
	for index, command := range commands {
		value, found, lookupErr := store.recoveryLookup(
			recoveryContext, command.ProjectID, command.Entry.CandidateID, command.Entry.Operation.ID,
		)
		if lookupErr != nil {
			return nil, errors.Join(
				ErrAppendOutcomeUnknown,
				fmt.Errorf("append repository Candidate operation batch: %w", transactionErr),
				fmt.Errorf("recover repository Candidate batch outcome: %w", lookupErr),
			)
		}
		if !found {
			continue
		}
		foundCount++
		if err := value.matchesCommand(command); err != nil {
			return nil, errors.Join(
				ErrOperationReplay,
				fmt.Errorf("append repository Candidate operation batch: %w", transactionErr),
				fmt.Errorf("operation ID resolved to a different committed command: %w", err),
			)
		}
		committed[index] = value
	}
	if foundCount == len(commands) {
		return committed, nil
	}
	if foundCount != 0 {
		return nil, errors.Join(
			ErrAppendOutcomeUnknown,
			fmt.Errorf("atomic Candidate batch has an impossible partial committed projection (%d/%d)", foundCount, len(commands)),
		)
	}
	return nil, classifyCandidateAppendError(transactionErr)
}

func validateAppendOperationCommands(commands []AppendOperationCommand) error {
	if len(commands) == 0 || len(commands) > MaxBatchMutationOperations {
		return candidateStoreContract("invalid append operation batch size", nil)
	}
	seen := make(map[string]bool, len(commands))
	for index, command := range commands {
		if err := validateAppendOperationCommand(command); err != nil {
			return err
		}
		if seen[command.Entry.Operation.ID] {
			return candidateStoreContract("append operation batch contains duplicate operation IDs", nil)
		}
		seen[command.Entry.Operation.ID] = true
		if index == 0 {
			continue
		}
		previous := commands[index-1]
		if command.ProjectID != previous.ProjectID ||
			command.Entry.CandidateID != previous.Entry.CandidateID ||
			command.Entry.Sequence != previous.Entry.Sequence+1 ||
			command.Entry.CandidateFrom != previous.Entry.CandidateTo ||
			command.Entry.SessionEpoch != previous.Entry.SessionEpoch ||
			command.Entry.LeaseEpoch != previous.Entry.LeaseEpoch ||
			command.Entry.ActorID != previous.Entry.ActorID ||
			command.Entry.Attribution != previous.Entry.Attribution ||
			command.BeforeTree != previous.AfterTree ||
			command.CandidateAfter.Version != previous.CandidateAfter.Version+1 ||
			command.CandidateAfter.JournalSequence != previous.CandidateAfter.JournalSequence+1 {
			return candidateStoreContract("append operation batch is not one contiguous fenced chain", nil)
		}
	}
	return nil
}

const appendOperationQuery = `
INSERT INTO candidate_workspace_journal (
  candidate_id, sequence, candidate_version_from, candidate_version_to,
  session_epoch, writer_lease_epoch, actor_id, attribution,
  operation_id, operation_kind, path, from_path,
  expected_content_hash, content_hash, byte_size, file_mode,
  before_tree_store, before_tree_owner_id, before_tree_ref,
  before_tree_content_hash, before_tree_hash,
  after_tree_store, after_tree_owner_id, after_tree_ref,
  after_tree_content_hash, after_tree_hash,
  after_tree_file_count, after_tree_byte_size
)
SELECT candidate.id, @sequence, @candidate_version_from, @candidate_version_to,
       @session_epoch, @writer_lease_epoch, @actor_id, @attribution,
       @operation_id, @operation_kind, @path, @from_path,
       @expected_content_hash, @content_hash, @byte_size, @file_mode,
       @before_tree_store, @before_tree_owner_id, @before_tree_ref,
       @before_tree_content_hash, @before_tree_hash,
       @after_tree_store, @after_tree_owner_id, @after_tree_ref,
       @after_tree_content_hash, @after_tree_hash,
       @after_tree_file_count, @after_tree_byte_size
FROM candidate_workspaces AS candidate
WHERE candidate.id = @candidate_id
  AND candidate.project_id = @project_id
  AND candidate.schema_version = @candidate_schema_version
  AND candidate.repository_snapshot_id = @repository_snapshot_id
  AND candidate.build_manifest_id = @build_manifest_id
  AND candidate.build_manifest_hash = @build_manifest_hash
  AND candidate.build_contract_id = @build_contract_id
  AND candidate.build_contract_hash = @build_contract_hash
  AND candidate.full_stack_template_id = @full_stack_template_id
  AND candidate.full_stack_template_hash = @full_stack_template_hash
  AND candidate.base_workspace_artifact_id IS NOT DISTINCT FROM @base_workspace_artifact_id
  AND candidate.base_workspace_revision_id IS NOT DISTINCT FROM @base_workspace_revision_id
  AND candidate.base_workspace_content_hash IS NOT DISTINCT FROM @base_workspace_content_hash
  AND candidate.base_tree_hash = @base_tree_hash
  AND candidate.created_by = @candidate_created_by
  AND candidate.created_at = @candidate_created_at`

func (store *GORMCandidateStore) insertOperation(
	ctx context.Context,
	transaction *gorm.DB,
	command AppendOperationCommand,
) error {
	projectID, _ := uuid.Parse(command.ProjectID)
	candidateID, _ := uuid.Parse(command.Entry.CandidateID)
	actorID, _ := uuid.Parse(command.Entry.ActorID)
	beforeOwnerID, _ := uuid.Parse(command.BeforeTree.OwnerID)
	afterOwnerID, _ := uuid.Parse(command.AfterTree.OwnerID)
	repositorySnapshotID, _ := uuid.Parse(command.CandidateAfter.RepositorySnapshotID)
	buildManifestID, _ := uuid.Parse(command.CandidateAfter.BuildManifest.ID)
	buildContractID, _ := uuid.Parse(command.CandidateAfter.BuildContract.ID)
	fullStackTemplateID, _ := uuid.Parse(command.CandidateAfter.FullStackTemplate.ID)
	candidateCreatedBy, _ := uuid.Parse(command.CandidateAfter.CreatedBy)
	var baseWorkspaceArtifactID any
	var baseWorkspaceRevisionID any
	var baseWorkspaceContentHash any
	if revision := command.CandidateAfter.BaseWorkspaceRevision; revision != nil {
		baseWorkspaceArtifactID, _ = uuid.Parse(revision.ArtifactID)
		baseWorkspaceRevisionID, _ = uuid.Parse(revision.RevisionID)
		baseWorkspaceContentHash = revision.ContentHash
	}
	parameters := map[string]any{
		"project_id": projectID, "candidate_id": candidateID,
		"candidate_schema_version": command.CandidateAfter.SchemaVersion,
		"repository_snapshot_id":   repositorySnapshotID,
		"build_manifest_id":        buildManifestID, "build_manifest_hash": command.CandidateAfter.BuildManifest.ContentHash,
		"build_contract_id": buildContractID, "build_contract_hash": command.CandidateAfter.BuildContract.ContentHash,
		"full_stack_template_id":      fullStackTemplateID,
		"full_stack_template_hash":    command.CandidateAfter.FullStackTemplate.ContentHash,
		"base_workspace_artifact_id":  baseWorkspaceArtifactID,
		"base_workspace_revision_id":  baseWorkspaceRevisionID,
		"base_workspace_content_hash": baseWorkspaceContentHash,
		"base_tree_hash":              command.CandidateAfter.BaseTreeHash,
		"candidate_created_by":        candidateCreatedBy, "candidate_created_at": command.CandidateAfter.CreatedAt,
		"sequence":               int64(command.Entry.Sequence),
		"candidate_version_from": int64(command.Entry.CandidateFrom),
		"candidate_version_to":   int64(command.Entry.CandidateTo),
		"session_epoch":          int64(command.Entry.SessionEpoch),
		"writer_lease_epoch":     int64(command.Entry.LeaseEpoch),
		"actor_id":               actorID, "attribution": command.Entry.Attribution,
		"operation_id": command.Entry.Operation.ID, "operation_kind": command.Entry.Operation.Kind,
		"path":                  command.Entry.Operation.Path,
		"from_path":             nullableString(command.Entry.Operation.FromPath),
		"expected_content_hash": nullableString(command.Entry.Operation.ExpectedHash),
		"content_hash":          nullableString(command.Entry.Operation.ContentHash),
		"byte_size":             nullableOperationByteSize(command.Entry.Operation),
		"file_mode":             nullableString(command.Entry.Operation.Mode),
		"before_tree_store":     command.BeforeTree.Store, "before_tree_owner_id": beforeOwnerID,
		"before_tree_ref":          command.BeforeTree.Ref,
		"before_tree_content_hash": command.BeforeTree.ContentObjectHash,
		"before_tree_hash":         command.BeforeTree.TreeHash,
		"after_tree_store":         command.AfterTree.Store, "after_tree_owner_id": afterOwnerID,
		"after_tree_ref":          command.AfterTree.Ref,
		"after_tree_content_hash": command.AfterTree.ContentObjectHash,
		"after_tree_hash":         command.AfterTree.TreeHash,
		"after_tree_file_count":   command.AfterTree.FileCount,
		"after_tree_byte_size":    command.AfterTree.ByteSize,
	}
	result := transaction.WithContext(ctx).Exec(appendOperationQuery, parameters)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		var candidateExists bool
		if err := transaction.WithContext(ctx).Raw(`
SELECT EXISTS (
  SELECT 1 FROM candidate_workspaces WHERE project_id = ? AND id = ?
)`, projectID, candidateID).Scan(&candidateExists).Error; err != nil {
			return err
		}
		if candidateExists {
			return candidateStoreContract("append command immutable lineage differs from the persisted candidate", nil)
		}
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (store *GORMCandidateStore) recoverAppendOutcome(
	ctx context.Context,
	command AppendOperationCommand,
	transactionErr error,
) (CommittedMutation, error) {
	base := ctx
	if base == nil {
		base = context.Background()
	} else {
		base = context.WithoutCancel(base)
	}
	timeout := store.recoveryTimeout
	if timeout <= 0 {
		timeout = defaultAppendRecoveryTimeout
	}
	recoveryContext, cancel := context.WithTimeout(base, timeout)
	defer cancel()
	committed, found, lookupErr := store.recoveryLookup(
		recoveryContext,
		command.ProjectID,
		command.Entry.CandidateID,
		command.Entry.Operation.ID,
	)
	if lookupErr != nil {
		return CommittedMutation{}, errors.Join(
			ErrAppendOutcomeUnknown,
			fmt.Errorf("append repository candidate operation: %w", transactionErr),
			fmt.Errorf("recover repository candidate append outcome: %w", lookupErr),
		)
	}
	if found {
		if err := committed.matchesCommand(command); err != nil {
			return CommittedMutation{}, errors.Join(
				ErrOperationReplay,
				fmt.Errorf("append repository candidate operation: %w", transactionErr),
				fmt.Errorf("operation id resolved to a different committed command: %w", err),
			)
		}
		return committed, nil
	}
	return CommittedMutation{}, classifyCandidateAppendError(transactionErr)
}

func validateAppendOperationCommand(command AppendOperationCommand) error {
	if !validUUID(command.ProjectID) || command.CandidateAfter.ProjectID != command.ProjectID ||
		command.Entry.CandidateID != command.CandidateAfter.ID || !validUUID(command.Entry.ActorID) ||
		command.Entry.Sequence == 0 || command.Entry.CandidateFrom == 0 ||
		command.Entry.CandidateTo != command.Entry.CandidateFrom+1 || command.Entry.SessionEpoch == 0 ||
		command.Entry.LeaseEpoch == 0 || command.Entry.CreatedAt.IsZero() ||
		!postgresBigint(command.Entry.Sequence) || !postgresBigint(command.Entry.CandidateFrom) ||
		!postgresBigint(command.Entry.CandidateTo) || !postgresBigint(command.Entry.SessionEpoch) ||
		!postgresBigint(command.Entry.LeaseEpoch) || !postgresBigint(command.CandidateAfter.Version) ||
		!postgresBigint(command.CandidateAfter.JournalSequence) ||
		!postgresBigint(command.CandidateAfter.SessionEpoch) ||
		!postgresBigint(command.CandidateAfter.WriterLeaseEpoch) {
		return candidateStoreContract("invalid append command identity or cursor", nil)
	}
	if err := command.CandidateAfter.Validate(); err != nil {
		return candidateStoreContract("invalid append command candidate", err)
	}
	operation, err := NormalizeOperation(command.Entry.Operation)
	if err != nil || operation != command.Entry.Operation {
		return candidateStoreContract("append command operation is not canonical", err)
	}
	if !validAttribution(command.Entry.Attribution) {
		return candidateStoreContract("invalid append command attribution", nil)
	}
	if err := command.BeforeTree.validate(); err != nil {
		return candidateStoreContract("invalid append command before pointer", err)
	}
	if err := command.AfterTree.validate(); err != nil {
		return candidateStoreContract("invalid append command after pointer", err)
	}
	candidate := command.CandidateAfter
	if candidate.Status != CandidateActive || !candidate.Dirty || candidate.Lease == nil ||
		candidate.Version != command.Entry.CandidateTo || candidate.JournalSequence != command.Entry.Sequence ||
		candidate.SessionEpoch != command.Entry.SessionEpoch ||
		candidate.WriterLeaseEpoch != command.Entry.LeaseEpoch ||
		candidate.Lease.Epoch != command.Entry.LeaseEpoch || candidate.Lease.OwnerID != command.Entry.ActorID ||
		!command.Entry.CreatedAt.Before(candidate.Lease.ExpiresAt) ||
		!candidate.UpdatedAt.Equal(command.Entry.CreatedAt) ||
		command.Entry.BeforeTree != command.BeforeTree.TreeHash ||
		command.Entry.AfterTree != command.AfterTree.TreeHash ||
		command.BeforeTree.TreeHash == command.AfterTree.TreeHash ||
		command.AfterTree.OwnerID != candidate.ID ||
		command.AfterTree.TreeHash != candidate.CurrentTree.TreeHash ||
		command.AfterTree.FileCount != len(candidate.CurrentTree.Files) ||
		command.AfterTree.ByteSize != treeByteSize(candidate.CurrentTree) {
		return candidateStoreContract("append command fields do not describe one exact transition", nil)
	}
	if command.Entry.Sequence == 1 {
		if command.BeforeTree.OwnerID != candidate.RepositorySnapshotID ||
			command.BeforeTree.TreeHash != candidate.BaseTreeHash {
			return candidateStoreContract("first append does not start at the repository snapshot", nil)
		}
	} else if command.BeforeTree.OwnerID != candidate.ID {
		return candidateStoreContract("subsequent append before tree is not candidate-owned", nil)
	}
	return nil
}

func classifyCandidateAppendError(err error) error {
	wrapped := fmt.Errorf("append repository candidate operation: %w", err)
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return wrapped
	}
	switch postgresError.Code {
	case "40001", "40P01":
		return errors.Join(ErrCandidateState, wrapped)
	case "42501":
		return errors.Join(ErrPathPolicyDenied, wrapped)
	case "23502", "23503", "23514", "22001", "22003", "22023", "22P02":
		return errors.Join(ErrMutationStoreContract, wrapped)
	default:
		return wrapped
	}
}

func candidateStoreContract(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrMutationStoreContract, message)
	}
	return errors.Join(ErrMutationStoreContract, fmt.Errorf("%s: %w", message, cause))
}

func validOperationID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 160
}

func validAttribution(value string) bool {
	return value == "user" || value == "agent" || value == "merge" || value == "restore"
}

func postgresBigint(value uint64) bool {
	return value <= uint64(1<<63-1)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableOperationByteSize(operation FileOperation) any {
	if operation.Kind != OperationUpsert {
		return nil
	}
	return operation.ByteSize
}

var _ CandidateStore = (*GORMCandidateStore)(nil)
