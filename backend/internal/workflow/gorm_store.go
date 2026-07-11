package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type UUIDGenerator struct{}

func (UUIDGenerator) NewID() string { return uuid.NewString() }

type GORMStore struct {
	db      *gorm.DB
	content ContentStore
	ids     IDGenerator
}

const claimRunnableSQL = `WITH candidate AS (
  SELECT node.id FROM workflow_node_runs AS node
  JOIN workflow_runs AS run ON run.id = node.run_id
  JOIN jsonb_to_recordset(CAST(@profiles AS jsonb)) AS profile(version text, hash text)
    ON profile.version = run.execution_profile_version AND profile.hash = run.execution_profile_hash
	WHERE run.status NOT IN ('completed', 'failed', 'cancelled', 'stale')
	  AND (
	    (node.status = 'ready' AND node.available_at <= @now)
	    OR (node.status = 'running' AND node.lease_expires_at < @now)
	  )
  ORDER BY node.available_at, node.id
  FOR UPDATE SKIP LOCKED
  LIMIT 1
)
UPDATE workflow_node_runs AS node
SET status = 'running', attempt = attempt + 1, lease_owner = @owner,
    lease_expires_at = @expires, started_at = COALESCE(started_at, @now), updated_at = @now
FROM candidate
WHERE node.id = candidate.id
RETURNING node.*`

func NewGORMStore(db *gorm.DB, content ContentStore, ids IDGenerator) (*GORMStore, error) {
	if db == nil {
		return nil, fmt.Errorf("gorm database is required")
	}
	if content == nil {
		content = InlineContentStore{}
	}
	if ids == nil {
		ids = UUIDGenerator{}
	}
	return &GORMStore{db: db, content: content, ids: ids}, nil
}

type definitionRow struct {
	ID          uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID   *uuid.UUID `gorm:"type:uuid"`
	WorkflowKey string
	Title       string
	Description string
	Lifecycle   string
	CreatedBy   uuid.UUID `gorm:"type:uuid"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (definitionRow) TableName() string { return "workflow_definitions" }

type definitionVersionRow struct {
	ID                      uuid.UUID `gorm:"type:uuid;primaryKey"`
	DefinitionID            uuid.UUID `gorm:"type:uuid"`
	Version                 int
	SchemaVersion           int
	Content                 json.RawMessage `gorm:"type:jsonb"`
	ContentHash             string
	ExecutionProfileVersion string
	ExecutionProfileHash    string
	ValidationReport        json.RawMessage `gorm:"type:jsonb"`
	Published               bool
	CreatedBy               uuid.UUID `gorm:"type:uuid"`
	CreatedAt               time.Time
}

func (definitionVersionRow) TableName() string { return "workflow_definition_versions" }

type manifestRow struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey"`
	ProjectID     uuid.UUID `gorm:"type:uuid"`
	Kind          string
	SchemaVersion int
	ContentStore  string
	ContentRef    string
	ContentHash   string
	ManifestHash  string
	CreatedBy     uuid.UUID `gorm:"type:uuid"`
	CreatedAt     time.Time
}

func (manifestRow) TableName() string { return "input_manifests" }

type proposalRow struct {
	ID              uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID       uuid.UUID  `gorm:"type:uuid"`
	ArtifactID      *uuid.UUID `gorm:"type:uuid"`
	Kind            string
	InputManifestID uuid.UUID  `gorm:"type:uuid"`
	BaseRevisionID  *uuid.UUID `gorm:"type:uuid"`
	BaseDraftID     *uuid.UUID `gorm:"type:uuid"`
	BaseContentHash *string
	Status          string
	Version         uint64
	ContentStore    string
	ContentRef      string
	ContentHash     string
	PayloadHash     string
	OperationCount  int
	AcceptedCount   int
	RejectedCount   int
	CreatedBy       uuid.UUID `gorm:"type:uuid"`
	CreatedAt       time.Time
	AppliedBy       *uuid.UUID `gorm:"type:uuid"`
	AppliedAt       *time.Time
}

func (proposalRow) TableName() string { return "output_proposals" }

type runRow struct {
	ID                      uuid.UUID `gorm:"type:uuid;primaryKey"`
	ProjectID               uuid.UUID `gorm:"type:uuid"`
	DefinitionVersionID     uuid.UUID `gorm:"type:uuid"`
	ExecutionProfileVersion string
	ExecutionProfileHash    string
	Status                  string
	InputManifestID         *uuid.UUID      `gorm:"type:uuid"`
	Scope                   json.RawMessage `gorm:"type:jsonb"`
	Context                 json.RawMessage `gorm:"type:jsonb"`
	EventCursor             uint64
	StartedBy               uuid.UUID `gorm:"type:uuid"`
	StartedAt               *time.Time
	CompletedAt             *time.Time
	CancelledAt             *time.Time
	Failure                 json.RawMessage `gorm:"type:jsonb"`
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

func (runRow) TableName() string { return "workflow_runs" }

type nodeRunRow struct {
	ID               uuid.UUID `gorm:"type:uuid;primaryKey"`
	RunID            uuid.UUID `gorm:"type:uuid"`
	NodeKey          string
	NodeType         string
	Status           string
	Attempt          int
	InputManifestID  *uuid.UUID `gorm:"type:uuid"`
	OutputProposalID *uuid.UUID `gorm:"type:uuid"`
	OutputRevisionID *uuid.UUID `gorm:"type:uuid"`
	LeaseOwner       *string
	LeaseExpiresAt   *time.Time
	AvailableAt      time.Time
	StartedAt        *time.Time
	CompletedAt      *time.Time
	Failure          json.RawMessage `gorm:"type:jsonb"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (nodeRunRow) TableName() string { return "workflow_node_runs" }

type eventRow struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	RunID     uuid.UUID `gorm:"type:uuid"`
	Sequence  uint64
	EventType string
	NodeKey   *string
	Payload   json.RawMessage `gorm:"type:jsonb"`
	ActorID   *uuid.UUID      `gorm:"type:uuid"`
	CreatedAt time.Time
}

func (eventRow) TableName() string { return "workflow_run_events" }

type sliceRow struct {
	ID                  uuid.UUID `gorm:"type:uuid;primaryKey"`
	ProjectID           uuid.UUID `gorm:"type:uuid"`
	SliceKey            string
	Title               string
	BlueprintRevisionID uuid.UUID  `gorm:"type:uuid"`
	PageSpecRevisionID  *uuid.UUID `gorm:"type:uuid"`
	PrototypeRevisionID *uuid.UUID `gorm:"type:uuid"`
	SyncStatus          string
	WorkflowStatus      string
	OwnerID             *uuid.UUID `gorm:"type:uuid"`
	BlockerReason       string
	UpdatedAt           time.Time
}

func (sliceRow) TableName() string { return "delivery_slices" }

func (s *GORMStore) SaveDefinition(ctx context.Context, record DefinitionRecord) error {
	if err := normalizeDefinitionRecordProfile(&record); err != nil {
		return err
	}
	if err := ValidateDefinitionForExecutionProfile(record.Definition, record.ExecutionProfile); err != nil {
		return err
	}
	definitionID, err := parseUUID("definition.id", record.Definition.ID)
	if err != nil {
		return err
	}
	versionID, err := parseUUID("definition.versionId", record.VersionID)
	if err != nil {
		return err
	}
	createdBy, err := parseUUID("definition.createdBy", record.Definition.CreatedBy)
	if err != nil {
		return err
	}
	var projectID *uuid.UUID
	if record.ProjectID != "" {
		parsed, err := parseUUID("definition.projectId", record.ProjectID)
		if err != nil {
			return err
		}
		projectID = &parsed
	}
	content, err := json.Marshal(record.Definition)
	if err != nil {
		return err
	}
	schemaVersion, err := numericVersion(record.Definition.SchemaVersion)
	if err != nil {
		return err
	}
	base := definitionRow{ID: definitionID, ProjectID: projectID, WorkflowKey: record.Key, Title: record.Title, Description: record.Description, Lifecycle: "active", CreatedBy: createdBy, CreatedAt: record.Definition.CreatedAt, UpdatedAt: record.Definition.CreatedAt}
	version := definitionVersionRow{ID: versionID, DefinitionID: definitionID, Version: record.Definition.Version, SchemaVersion: schemaVersion, Content: content, ContentHash: record.Definition.Hash, ExecutionProfileVersion: record.ExecutionProfile.Version, ExecutionProfileHash: record.ExecutionProfile.Hash, ValidationReport: json.RawMessage(`{"valid":true}`), Published: record.Published, CreatedBy: createdBy, CreatedAt: record.Definition.CreatedAt}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing definitionRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&existing, "id = ?", definitionID).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			if version.Version != 1 {
				return ErrCASConflict
			}
			if err := tx.Create(&base).Error; err != nil {
				if workflowUniqueViolation(err) {
					return ErrCASConflict
				}
				return err
			}
			existing = base
		case err != nil:
			return err
		default:
			if !sameOptionalUUID(existing.ProjectID, base.ProjectID) || existing.WorkflowKey != base.WorkflowKey ||
				existing.Title != base.Title || existing.Description != base.Description || existing.Lifecycle != "active" {
				return ErrCASConflict
			}
			var latest int
			if err := tx.Model(&definitionVersionRow{}).Where("definition_id = ?", definitionID).
				Select("COALESCE(MAX(version), 0)").Scan(&latest).Error; err != nil {
				return err
			}
			if version.Version != latest+1 {
				return ErrCASConflict
			}
		}
		if err := tx.Create(&version).Error; err != nil {
			if workflowUniqueViolation(err) {
				return ErrCASConflict
			}
			return err
		}
		return insertDefinitionActivity(tx, existing, version, createdBy, "workflow.definition_version_created", "worksflow.workflow.definition.version.created", version.CreatedAt)
	})
	return err
}

func (s *GORMStore) PublishDefinitionVersion(ctx context.Context, projectID, definitionID, versionID, actorID string) (DefinitionRecord, error) {
	record, err := s.GetDefinitionVersion(ctx, versionID)
	if err != nil {
		return DefinitionRecord{}, err
	}
	if record.Definition.ID != definitionID || record.ProjectID != projectID {
		return DefinitionRecord{}, domain.ErrNotFound
	}
	if record.ExecutionProfile != CurrentWorkflowExecutionProfileRef() {
		return DefinitionRecord{}, &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.executionProfile", Message: "only the current execution profile can be newly published"}
	}
	if err := ValidateDefinitionForExecutionProfile(record.Definition, record.ExecutionProfile); err != nil {
		return DefinitionRecord{}, err
	}
	projectUUID, err := parseUUID("definition.projectId", projectID)
	if err != nil {
		return DefinitionRecord{}, err
	}
	definitionUUID, err := parseUUID("definition.id", definitionID)
	if err != nil {
		return DefinitionRecord{}, err
	}
	versionUUID, err := parseUUID("definition.versionId", versionID)
	if err != nil {
		return DefinitionRecord{}, err
	}
	actorUUID, err := parseUUID("definition.actorId", actorID)
	if err != nil {
		return DefinitionRecord{}, err
	}
	var version definitionVersionRow
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var base definitionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND project_id = ? AND lifecycle = 'active'", definitionUUID, projectUUID).Take(&base).Error; err != nil {
			return mapGORMError(err)
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND definition_id = ?", versionUUID, definitionUUID).Take(&version).Error; err != nil {
			return mapGORMError(err)
		}
		if version.Published {
			return nil
		}
		result := tx.Model(&definitionVersionRow{}).
			Where("id = ? AND definition_id = ? AND published = false", versionUUID, definitionUUID).
			Update("published", true)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCASConflict
		}
		version.Published = true
		now := time.Now().UTC()
		return insertDefinitionActivity(tx, base, version, actorUUID, "workflow.definition_version_published", "worksflow.workflow.definition.version.published", now)
	})
	if err != nil {
		return DefinitionRecord{}, err
	}
	return s.loadDefinitionRecord(ctx, version)
}

func (s *GORMStore) UnpublishDefinitionVersion(ctx context.Context, projectID, definitionID, versionID, actorID string) (DefinitionRecord, error) {
	projectUUID, err := parseUUID("definition.projectId", projectID)
	if err != nil {
		return DefinitionRecord{}, err
	}
	definitionUUID, err := parseUUID("definition.id", definitionID)
	if err != nil {
		return DefinitionRecord{}, err
	}
	versionUUID, err := parseUUID("definition.versionId", versionID)
	if err != nil {
		return DefinitionRecord{}, err
	}
	actorUUID, err := parseUUID("definition.actorId", actorID)
	if err != nil {
		return DefinitionRecord{}, err
	}
	var version definitionVersionRow
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var base definitionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND project_id = ? AND lifecycle = 'active'", definitionUUID, projectUUID).Take(&base).Error; err != nil {
			return mapGORMError(err)
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND definition_id = ?", versionUUID, definitionUUID).Take(&version).Error; err != nil {
			return mapGORMError(err)
		}
		if !version.Published {
			return nil
		}
		result := tx.Model(&definitionVersionRow{}).
			Where("id = ? AND definition_id = ? AND published = true", versionUUID, definitionUUID).
			Update("published", false)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCASConflict
		}
		version.Published = false
		now := time.Now().UTC()
		return insertDefinitionActivity(tx, base, version, actorUUID, "workflow.definition_version_unpublished", "worksflow.workflow.definition.version.unpublished", now)
	})
	if err != nil {
		return DefinitionRecord{}, err
	}
	return s.loadDefinitionRecord(ctx, version)
}

func (s *GORMStore) GetDefinition(ctx context.Context, id string, version int) (DefinitionRecord, error) {
	definitionID, err := parseUUID("definition.id", id)
	if err != nil {
		return DefinitionRecord{}, err
	}
	var versionRow definitionVersionRow
	if err := s.db.WithContext(ctx).Where("definition_id = ? AND version = ?", definitionID, version).First(&versionRow).Error; err != nil {
		return DefinitionRecord{}, mapGORMError(err)
	}
	return s.loadDefinitionRecord(ctx, versionRow)
}

func (s *GORMStore) GetDefinitionVersion(ctx context.Context, versionID string) (DefinitionRecord, error) {
	id, err := parseUUID("definition.versionId", versionID)
	if err != nil {
		return DefinitionRecord{}, err
	}
	var versionRow definitionVersionRow
	if err := s.db.WithContext(ctx).First(&versionRow, "id = ?", id).Error; err != nil {
		return DefinitionRecord{}, mapGORMError(err)
	}
	return s.loadDefinitionRecord(ctx, versionRow)
}

func (s *GORMStore) ListDefinitions(ctx context.Context, projectID string) ([]DefinitionRecord, error) {
	projectUUID, err := parseUUID("definition.projectId", projectID)
	if err != nil {
		return nil, err
	}
	var bases []definitionRow
	if err := s.db.WithContext(ctx).Where("project_id = ? OR project_id IS NULL", projectUUID).Order("workflow_key").Find(&bases).Error; err != nil {
		return nil, err
	}
	result := make([]DefinitionRecord, 0, len(bases))
	for _, base := range bases {
		var version definitionVersionRow
		if err := s.db.WithContext(ctx).Where("definition_id = ?", base.ID).Order("version DESC").First(&version).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		} else if err != nil {
			return nil, err
		}
		record, err := s.loadDefinitionRecord(ctx, version)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, nil
}

func (s *GORMStore) ListDefinitionVersions(ctx context.Context, id string) ([]DefinitionRecord, error) {
	definitionID, err := parseUUID("definition.id", id)
	if err != nil {
		return nil, err
	}
	var rows []definitionVersionRow
	if err := s.db.WithContext(ctx).Where("definition_id = ?", definitionID).Order("version").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, domain.ErrNotFound
	}
	result := make([]DefinitionRecord, 0, len(rows))
	for _, row := range rows {
		record, err := s.loadDefinitionRecord(ctx, row)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, nil
}

func (s *GORMStore) loadDefinitionRecord(ctx context.Context, version definitionVersionRow) (DefinitionRecord, error) {
	var base definitionRow
	if err := s.db.WithContext(ctx).First(&base, "id = ?", version.DefinitionID).Error; err != nil {
		return DefinitionRecord{}, mapGORMError(err)
	}
	var definition domain.WorkflowDefinition
	if err := json.Unmarshal(version.Content, &definition); err != nil {
		return DefinitionRecord{}, fmt.Errorf("decode workflow definition: %w", err)
	}
	if definition.Hash != version.ContentHash {
		return DefinitionRecord{}, fmt.Errorf("workflow definition content hash mismatch")
	}
	profile := domain.WorkflowExecutionProfileRef{Version: version.ExecutionProfileVersion, Hash: version.ExecutionProfileHash}
	record := DefinitionRecord{ExecutionProfile: profile, Definition: definition}
	if err := normalizeDefinitionRecordProfile(&record); err != nil {
		return DefinitionRecord{}, err
	}
	if err := ValidateDefinitionForExecutionProfile(definition, profile); err != nil {
		return DefinitionRecord{}, err
	}
	projectID := ""
	if base.ProjectID != nil {
		projectID = base.ProjectID.String()
	}
	return DefinitionRecord{VersionID: version.ID.String(), ProjectID: projectID, Key: base.WorkflowKey, Title: base.Title, Description: base.Description, Published: version.Published, ExecutionProfile: profile, Definition: definition}, nil
}

func (s *GORMStore) SaveManifest(ctx context.Context, manifest domain.InputManifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	id, err := parseUUID("manifest.id", manifest.ID)
	if err != nil {
		return err
	}
	projectID, err := parseUUID("manifest.projectId", manifest.ProjectID)
	if err != nil {
		return err
	}
	createdBy, err := parseUUID("manifest.createdBy", manifest.CreatedBy)
	if err != nil {
		return err
	}
	content, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	store, ref, contentHash, err := s.content.Put(ctx, "input-manifest", manifest.ID, content)
	if err != nil {
		return err
	}
	schemaVersion, err := numericVersion(manifest.OutputSchemaVersion)
	if err != nil {
		return err
	}
	row := manifestRow{ID: id, ProjectID: projectID, Kind: manifest.JobType, SchemaVersion: schemaVersion, ContentStore: store, ContentRef: ref, ContentHash: contentHash, ManifestHash: manifest.Hash, CreatedBy: createdBy, CreatedAt: manifest.CreatedAt}
	return s.db.WithContext(ctx).Create(&row).Error
}

func (s *GORMStore) GetManifest(ctx context.Context, id string) (domain.InputManifest, error) {
	manifestID, err := parseUUID("manifest.id", id)
	if err != nil {
		return domain.InputManifest{}, err
	}
	var row manifestRow
	if err := s.db.WithContext(ctx).First(&row, "id = ?", manifestID).Error; err != nil {
		return domain.InputManifest{}, mapGORMError(err)
	}
	content, err := s.content.Get(ctx, row.ContentStore, row.ContentRef, row.ContentHash)
	if err != nil {
		return domain.InputManifest{}, err
	}
	var manifest domain.InputManifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return domain.InputManifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return domain.InputManifest{}, err
	}
	if manifest.Hash != row.ManifestHash {
		return domain.InputManifest{}, fmt.Errorf("stored manifest hash mismatch")
	}
	return manifest, nil
}

func (s *GORMStore) SaveProposal(ctx context.Context, proposal *domain.OutputProposal) error {
	if proposal == nil {
		return fmt.Errorf("proposal is required")
	}
	if err := proposal.ValidatePayloadHash(); err != nil {
		return err
	}
	row, err := s.proposalToRow(ctx, proposal)
	if err != nil {
		return err
	}
	var current proposalRow
	query := s.db.WithContext(ctx).First(&current, "id = ?", row.ID)
	if errors.Is(query.Error, gorm.ErrRecordNotFound) {
		return s.db.WithContext(ctx).Create(&row).Error
	}
	if query.Error != nil {
		return query.Error
	}
	if proposal.Version <= current.Version {
		return ErrCASConflict
	}
	result := s.db.WithContext(ctx).Model(&proposalRow{}).Where("id = ? AND version = ?", row.ID, current.Version).Updates(row)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCASConflict
	}
	return nil
}

func (s *GORMStore) proposalToRow(ctx context.Context, proposal *domain.OutputProposal) (proposalRow, error) {
	id, err := parseUUID("proposal.id", proposal.ID)
	if err != nil {
		return proposalRow{}, err
	}
	projectID, err := parseUUID("proposal.projectId", proposal.ProjectID)
	if err != nil {
		return proposalRow{}, err
	}
	artifactID, err := parseUUID("proposal.artifactId", proposal.ArtifactID)
	if err != nil {
		return proposalRow{}, err
	}
	manifestID, err := parseUUID("proposal.manifestId", proposal.Manifest.ID)
	if err != nil {
		return proposalRow{}, err
	}
	baseID, err := parseUUID("proposal.baseRevisionId", proposal.BaseRevision.RevisionID)
	if err != nil {
		return proposalRow{}, err
	}
	createdBy, err := parseUUID("proposal.createdBy", proposal.CreatedBy)
	if err != nil {
		return proposalRow{}, err
	}
	content, err := json.Marshal(proposal)
	if err != nil {
		return proposalRow{}, err
	}
	store, ref, contentHash, err := s.content.Put(ctx, "output-proposal", proposal.ID, content)
	if err != nil {
		return proposalRow{}, err
	}
	accepted, rejected := 0, 0
	for _, operation := range proposal.Operations {
		if operation.Decision == domain.DecisionAccepted || operation.Decision == domain.DecisionApplied {
			accepted++
		}
		if operation.Decision == domain.DecisionRejected {
			rejected++
		}
	}
	return proposalRow{ID: id, ProjectID: projectID, ArtifactID: &artifactID, Kind: "artifact_patch", InputManifestID: manifestID, BaseRevisionID: &baseID, BaseContentHash: &proposal.BaseRevision.ContentHash, Status: string(proposal.Status), Version: proposal.Version, ContentStore: store, ContentRef: ref, ContentHash: contentHash, PayloadHash: proposal.PayloadHash, OperationCount: len(proposal.Operations), AcceptedCount: accepted, RejectedCount: rejected, CreatedBy: createdBy, CreatedAt: proposal.CreatedAt, AppliedAt: proposal.AppliedAt}, nil
}

func (s *GORMStore) GetProposal(ctx context.Context, id string) (*domain.OutputProposal, error) {
	proposalID, err := parseUUID("proposal.id", id)
	if err != nil {
		return nil, err
	}
	var row proposalRow
	if err := s.db.WithContext(ctx).First(&row, "id = ?", proposalID).Error; err != nil {
		return nil, mapGORMError(err)
	}
	content, err := s.content.Get(ctx, row.ContentStore, row.ContentRef, row.ContentHash)
	if err != nil {
		return nil, err
	}
	var proposal domain.OutputProposal
	if err := json.Unmarshal(content, &proposal); err != nil {
		return nil, err
	}
	if err := proposal.ValidatePayloadHash(); err != nil {
		return nil, err
	}
	if proposal.PayloadHash != row.PayloadHash || proposal.Version != row.Version {
		return nil, fmt.Errorf("stored proposal metadata mismatch")
	}
	return &proposal, nil
}

func (s *GORMStore) CreateRun(ctx context.Context, run *RunRecord, events []Event) error {
	if run == nil {
		return fmt.Errorf("run is required")
	}
	if err := run.Validate(); err != nil {
		return err
	}
	runModel, nodeModels, err := s.runToRows(run)
	if err != nil {
		return err
	}
	eventModels, err := s.eventsToRows(run.ID, events, 0)
	if err != nil {
		return err
	}
	runModel.EventCursor = uint64(len(eventModels))
	run.EventCursor = runModel.EventCursor
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&runModel).Error; err != nil {
			return err
		}
		if len(nodeModels) > 0 {
			if err := tx.Create(&nodeModels).Error; err != nil {
				return err
			}
		}
		if len(eventModels) > 0 {
			if err := tx.Create(&eventModels).Error; err != nil {
				return err
			}
		}
		return s.enqueueEventRowsTx(tx, runModel.ProjectID, runModel.ID, eventModels)
	})
}

func (s *GORMStore) runToRows(run *RunRecord) (runRow, []nodeRunRow, error) {
	id, err := parseUUID("run.id", run.ID)
	if err != nil {
		return runRow{}, nil, err
	}
	projectID, err := parseUUID("run.projectId", run.ProjectID)
	if err != nil {
		return runRow{}, nil, err
	}
	versionID, err := parseUUID("run.definitionVersionId", run.DefinitionVersionID)
	if err != nil {
		return runRow{}, nil, err
	}
	startedBy, err := parseUUID("run.startedBy", run.StartedBy)
	if err != nil {
		return runRow{}, nil, err
	}
	var manifestID *uuid.UUID
	if run.InputManifest != nil {
		parsed, err := parseUUID("run.inputManifestId", run.InputManifest.ID)
		if err != nil {
			return runRow{}, nil, err
		}
		manifestID = &parsed
	}
	run.Context.ensureMaps()
	contextJSON, err := json.Marshal(run.Context)
	if err != nil {
		return runRow{}, nil, err
	}
	scope := run.Scope
	if len(scope) == 0 {
		scope = json.RawMessage(`{}`)
	}
	row := runRow{ID: id, ProjectID: projectID, DefinitionVersionID: versionID, ExecutionProfileVersion: run.ExecutionProfile.Version, ExecutionProfileHash: run.ExecutionProfile.Hash, Status: string(run.Status), InputManifestID: manifestID, Scope: scope, Context: contextJSON, EventCursor: run.EventCursor, StartedBy: startedBy, StartedAt: run.StartedAt, CompletedAt: run.CompletedAt, CancelledAt: run.CancelledAt, Failure: run.Failure, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt}
	nodes := make([]nodeRunRow, 0, len(run.Nodes))
	for _, node := range run.Nodes {
		converted, err := nodeToRow(node)
		if err != nil {
			return runRow{}, nil, err
		}
		nodes = append(nodes, converted)
	}
	return row, nodes, nil
}

func nodeToRow(node *NodeRecord) (nodeRunRow, error) {
	id, err := parseUUID("node.id", node.ID)
	if err != nil {
		return nodeRunRow{}, err
	}
	runID, err := parseUUID("node.runId", node.RunID)
	if err != nil {
		return nodeRunRow{}, err
	}
	var manifestID, proposalID, revisionID *uuid.UUID
	if node.InputManifest != nil {
		parsed, err := parseUUID("node.inputManifestId", node.InputManifest.ID)
		if err != nil {
			return nodeRunRow{}, err
		}
		manifestID = &parsed
	}
	if node.OutputProposal != nil {
		parsed, err := parseUUID("node.outputProposalId", node.OutputProposal.ID)
		if err != nil {
			return nodeRunRow{}, err
		}
		proposalID = &parsed
	}
	if node.OutputRevisionID != "" {
		parsed, err := parseUUID("node.outputRevisionId", node.OutputRevisionID)
		if err != nil {
			return nodeRunRow{}, err
		}
		revisionID = &parsed
	}
	var owner *string
	if node.LeaseOwner != "" {
		value := node.LeaseOwner
		owner = &value
	}
	return nodeRunRow{ID: id, RunID: runID, NodeKey: node.Key, NodeType: string(node.Type), Status: string(node.Status), Attempt: node.Attempt, InputManifestID: manifestID, OutputProposalID: proposalID, OutputRevisionID: revisionID, LeaseOwner: owner, LeaseExpiresAt: node.LeaseExpiresAt, AvailableAt: node.AvailableAt, StartedAt: node.StartedAt, CompletedAt: node.CompletedAt, Failure: node.Failure, CreatedAt: node.CreatedAt, UpdatedAt: node.UpdatedAt}, nil
}

func (s *GORMStore) GetRun(ctx context.Context, id string) (*RunRecord, error) {
	runID, err := parseUUID("run.id", id)
	if err != nil {
		return nil, err
	}
	var row runRow
	if err := s.db.WithContext(ctx).First(&row, "id = ?", runID).Error; err != nil {
		return nil, mapGORMError(err)
	}
	definition, err := s.GetDefinitionVersion(ctx, row.DefinitionVersionID.String())
	if err != nil {
		return nil, err
	}
	profile := domain.WorkflowExecutionProfileRef{Version: row.ExecutionProfileVersion, Hash: row.ExecutionProfileHash}
	if profile != definition.ExecutionProfile {
		return nil, fmt.Errorf("workflow run execution profile does not match its definition version")
	}
	var runContext RunContext
	if err := json.Unmarshal(row.Context, &runContext); err != nil {
		return nil, fmt.Errorf("decode workflow run context: %w", err)
	}
	runContext.ensureMaps()
	var manifestRef *domain.ManifestRef
	if row.InputManifestID != nil {
		manifest, err := s.GetManifest(ctx, row.InputManifestID.String())
		if err != nil {
			return nil, err
		}
		ref := manifest.Ref()
		manifestRef = &ref
	}
	var nodeRows []nodeRunRow
	if err := s.db.WithContext(ctx).Where("run_id = ?", runID).Find(&nodeRows).Error; err != nil {
		return nil, err
	}
	run := &RunRecord{ID: row.ID.String(), ProjectID: row.ProjectID.String(), DefinitionVersionID: row.DefinitionVersionID.String(), Definition: definition.Definition.RefForExecutionProfile(profile), ExecutionProfile: profile, InputManifest: manifestRef, Status: RunStatus(row.Status), Scope: row.Scope, Context: runContext, EventCursor: row.EventCursor, StartedBy: row.StartedBy.String(), StartedAt: row.StartedAt, CompletedAt: row.CompletedAt, CancelledAt: row.CancelledAt, Failure: row.Failure, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Nodes: map[string]*NodeRecord{}}
	for _, nodeRow := range nodeRows {
		node, err := s.rowToNode(ctx, nodeRow, runContext)
		if err != nil {
			return nil, err
		}
		run.Nodes[node.Key] = node
	}
	return run, nil
}

func (s *GORMStore) ListRuns(ctx context.Context, projectID string, filter StoreRunFilter) ([]RunSummary, error) {
	projectUUID, err := parseUUID("project.id", projectID)
	if err != nil {
		return nil, err
	}
	query := s.db.WithContext(ctx).Where("project_id = ?", projectUUID)
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.BeforeCreatedAt != nil {
		beforeID, err := parseUUID("cursor.id", filter.BeforeID)
		if err != nil {
			return nil, err
		}
		query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", filter.BeforeCreatedAt.UTC(), filter.BeforeCreatedAt.UTC(), beforeID)
	}
	limit := filter.Limit
	if limit < 1 || limit > 101 {
		limit = 26
	}
	var rows []runRow
	if err := query.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]RunSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, summaryFromRow(row))
	}
	return items, nil
}

func summaryFromRow(row runRow) RunSummary {
	return RunSummary{
		ID: row.ID.String(), ProjectID: row.ProjectID.String(), DefinitionVersionID: row.DefinitionVersionID.String(),
		ExecutionProfile: domain.WorkflowExecutionProfileRef{Version: row.ExecutionProfileVersion, Hash: row.ExecutionProfileHash},
		Status:           RunStatus(row.Status), EventCursor: row.EventCursor, StartedBy: row.StartedBy.String(),
		StartedAt: row.StartedAt, CompletedAt: row.CompletedAt, CancelledAt: row.CancelledAt,
		Failure: cloneRaw(row.Failure), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func (s *GORMStore) ListActiveExecutionProfiles(ctx context.Context) ([]domain.WorkflowExecutionProfileRef, error) {
	var rows []struct {
		ExecutionProfileVersion string
		ExecutionProfileHash    string
	}
	if err := s.db.WithContext(ctx).Model(&runRow{}).
		Select("DISTINCT execution_profile_version, execution_profile_hash").
		Where("status NOT IN ?", []RunStatus{RunCompleted, RunFailed, RunCancelled, RunStale}).
		Order("execution_profile_version, execution_profile_hash").Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]domain.WorkflowExecutionProfileRef, 0, len(rows))
	for _, row := range rows {
		ref := domain.WorkflowExecutionProfileRef{Version: row.ExecutionProfileVersion, Hash: row.ExecutionProfileHash}
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("active workflow run has an invalid execution profile: %w", err)
		}
		result = append(result, ref)
	}
	return result, nil
}

func (s *GORMStore) rowToNode(ctx context.Context, row nodeRunRow, runContext RunContext) (*NodeRecord, error) {
	metadata := runContext.Nodes[row.NodeKey]
	node := &NodeRecord{ID: row.ID.String(), RunID: row.RunID.String(), Key: row.NodeKey, DefinitionNodeID: metadata.DefinitionNodeID, SliceID: metadata.SliceID, Type: domain.WorkflowNodeType(row.NodeType), Status: NodeStatus(row.Status), Attempt: row.Attempt, AvailableAt: row.AvailableAt, LeaseExpiresAt: row.LeaseExpiresAt, StartedAt: row.StartedAt, CompletedAt: row.CompletedAt, Failure: row.Failure, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.LeaseOwner != nil {
		node.LeaseOwner = *row.LeaseOwner
	}
	if row.InputManifestID != nil {
		manifest, err := s.GetManifest(ctx, row.InputManifestID.String())
		if err != nil {
			return nil, err
		}
		ref := manifest.Ref()
		node.InputManifest = &ref
	}
	if row.OutputProposalID != nil {
		proposal, err := s.GetProposal(ctx, row.OutputProposalID.String())
		if err != nil {
			return nil, err
		}
		node.OutputProposal = &domain.ProposalRef{ID: proposal.ID, PayloadHash: proposal.PayloadHash}
	}
	if row.OutputRevisionID != nil {
		node.OutputRevisionID = row.OutputRevisionID.String()
	}
	return node, nil
}

func (s *GORMStore) ClaimRunnable(ctx context.Context, workerID string, now time.Time, leaseDuration time.Duration, supported ...domain.WorkflowExecutionProfileRef) (Lease, error) {
	if strings.TrimSpace(workerID) == "" || leaseDuration <= 0 {
		return Lease{}, fmt.Errorf("worker and positive lease duration are required")
	}
	profiles := make([]domain.WorkflowExecutionProfileRef, 0, len(supported))
	seen := map[domain.WorkflowExecutionProfileRef]bool{}
	for _, ref := range supported {
		if ref.Validate() != nil || seen[ref] {
			continue
		}
		seen[ref] = true
		profiles = append(profiles, ref)
	}
	if len(profiles) == 0 {
		return Lease{}, ErrNoRunnableNode
	}
	profileJSON, err := json.Marshal(profiles)
	if err != nil {
		return Lease{}, err
	}
	var claimed nodeRunRow
	expires := now.UTC().Add(leaseDuration)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Raw(claimRunnableSQL, map[string]any{"now": now.UTC(), "owner": workerID, "expires": expires, "profiles": string(profileJSON)}).Scan(&claimed)
		if result.Error != nil {
			return result.Error
		}
		if claimed.ID == uuid.Nil {
			return ErrNoRunnableNode
		}
		_, err := s.appendEventTx(tx, claimed.RunID, Event{ID: s.ids.NewID(), RunID: claimed.RunID.String(), Type: "node.claimed", NodeKey: claimed.NodeKey, Payload: mustJSON(map[string]any{"workerId": workerID, "attempt": claimed.Attempt, "leaseExpiresAt": expires}), CreatedAt: now.UTC()})
		return err
	})
	if err != nil {
		return Lease{}, err
	}
	return Lease{RunID: claimed.RunID.String(), NodeID: claimed.ID.String(), NodeKey: claimed.NodeKey, WorkerID: workerID, Attempt: claimed.Attempt, LeaseExpiresAt: expires}, nil
}

func (s *GORMStore) RenewLease(ctx context.Context, lease Lease, now time.Time, duration time.Duration) (Lease, error) {
	expires := now.UTC().Add(duration)
	result := s.db.WithContext(ctx).Model(&nodeRunRow{}).Where("id = ? AND run_id = ? AND status = 'running' AND lease_owner = ? AND lease_expires_at >= ?", lease.NodeID, lease.RunID, lease.WorkerID, now.UTC()).Updates(map[string]any{"lease_expires_at": expires, "updated_at": now.UTC()})
	if result.Error != nil {
		return Lease{}, result.Error
	}
	if result.RowsAffected != 1 {
		return Lease{}, ErrLeaseLost
	}
	lease.LeaseExpiresAt = expires
	return lease, nil
}

func (s *GORMStore) Commit(ctx context.Context, mutation RunMutation) error {
	if len(mutation.Events) == 0 {
		return fmt.Errorf("run mutation must emit at least one event")
	}
	runID, err := parseUUID("run.id", mutation.RunID)
	if err != nil {
		return err
	}
	mutation.Context.ensureMaps()
	contextJSON, err := json.Marshal(mutation.Context)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		newCursor := mutation.ExpectedCursor + uint64(len(mutation.Events))
		result := tx.Model(&runRow{}).Where("id = ? AND event_cursor = ?", runID, mutation.ExpectedCursor).Updates(map[string]any{"status": mutation.Status, "context": contextJSON, "event_cursor": newCursor, "failure": nullableJSON(mutation.Failure), "completed_at": mutation.CompletedAt, "cancelled_at": mutation.CancelledAt, "updated_at": mutation.UpdatedAt})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCASConflict
		}
		for _, nodeMutation := range mutation.Nodes {
			row, err := nodeToRow(&nodeMutation.Node)
			if err != nil {
				return err
			}
			updates := map[string]any{"status": row.Status, "attempt": row.Attempt, "input_manifest_id": row.InputManifestID, "output_proposal_id": row.OutputProposalID, "output_revision_id": row.OutputRevisionID, "lease_owner": nil, "lease_expires_at": nil, "available_at": row.AvailableAt, "started_at": row.StartedAt, "completed_at": row.CompletedAt, "failure": nullableJSON(row.Failure), "updated_at": row.UpdatedAt}
			query := tx.Model(&nodeRunRow{}).Where("id = ? AND run_id = ? AND status = ?", row.ID, runID, nodeMutation.ExpectedStatus)
			if nodeMutation.ExpectedOwner != "" {
				query = query.Where("lease_owner = ? AND lease_expires_at >= ?", nodeMutation.ExpectedOwner, mutation.UpdatedAt)
			}
			updated := query.Updates(updates)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrLeaseLost
			}
		}
		if err := validateCompletedManifestCompilerGroupsTx(ctx, tx, runID, mutation); err != nil {
			return err
		}
		if len(mutation.NewNodes) > 0 {
			rows := make([]nodeRunRow, 0, len(mutation.NewNodes))
			for index := range mutation.NewNodes {
				row, err := nodeToRow(&mutation.NewNodes[index])
				if err != nil {
					return err
				}
				rows = append(rows, row)
			}
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
		if len(mutation.Slices) > 0 {
			rows := make([]sliceRow, 0, len(mutation.Slices))
			for _, slice := range mutation.Slices {
				row, err := toSliceRow(slice)
				if err != nil {
					return err
				}
				rows = append(rows, row)
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "project_id"}, {Name: "slice_key"}, {Name: "blueprint_revision_id"}}, DoUpdates: clause.AssignmentColumns([]string{"title", "page_spec_revision_id", "prototype_revision_id", "sync_status", "workflow_status", "owner_id", "blocker_reason", "updated_at"})}).Create(&rows).Error; err != nil {
				return err
			}
		}
		events, err := s.eventsToRows(mutation.RunID, mutation.Events, mutation.ExpectedCursor)
		if err != nil {
			return err
		}
		if err := tx.Create(&events).Error; err != nil {
			return err
		}
		var identity struct {
			ProjectID uuid.UUID
		}
		if err := tx.Model(&runRow{}).Select("project_id").First(&identity, "id = ?", runID).Error; err != nil {
			return err
		}
		return s.enqueueEventRowsTx(tx, identity.ProjectID, runID, events)
	})
}

func (s *GORMStore) ListEvents(ctx context.Context, runID string, after uint64, limit int) ([]Event, error) {
	id, err := parseUUID("run.id", runID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var rows []eventRow
	if err := s.db.WithContext(ctx).Where("run_id = ? AND sequence > ?", id, after).Order("sequence").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	events := make([]Event, len(rows))
	for index, row := range rows {
		events[index] = eventFromRow(row)
	}
	return events, nil
}

func (s *GORMStore) appendEventTx(tx *gorm.DB, runID uuid.UUID, event Event) (uint64, error) {
	var run runRow
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, "id = ?", runID).Error; err != nil {
		return 0, err
	}
	sequence := run.EventCursor + 1
	if err := tx.Model(&runRow{}).Where("id = ? AND event_cursor = ?", runID, run.EventCursor).Updates(map[string]any{"event_cursor": sequence, "updated_at": event.CreatedAt}).Error; err != nil {
		return 0, err
	}
	rows, err := s.eventsToRows(runID.String(), []Event{event}, run.EventCursor)
	if err != nil {
		return 0, err
	}
	if err := tx.Create(&rows[0]).Error; err != nil {
		return 0, err
	}
	if err := s.enqueueEventRowsTx(tx, run.ProjectID, runID, rows); err != nil {
		return 0, err
	}
	return sequence, nil
}

func (s *GORMStore) enqueueEventRowsTx(tx *gorm.DB, projectID, runID uuid.UUID, rows []eventRow) error {
	outbox, err := eventRowsToOutbox(projectID, runID, rows)
	if err != nil {
		return err
	}
	if len(outbox) == 0 {
		return nil
	}
	return tx.Create(&outbox).Error
}

func eventRowsToOutbox(projectID, runID uuid.UUID, rows []eventRow) ([]storage.OutboxEventModel, error) {
	outbox := make([]storage.OutboxEventModel, len(rows))
	for index, row := range rows {
		payload := map[string]any{
			"id": row.ID.String(), "projectId": projectID.String(), "runId": runID.String(),
			"sequence": row.Sequence, "type": row.EventType,
			"occurredAt": row.CreatedAt.UTC().Format(time.RFC3339Nano),
			"payload":    json.RawMessage(row.Payload),
		}
		if row.NodeKey != nil {
			payload["nodeKey"] = *row.NodeKey
		}
		if row.ActorID != nil {
			payload["actorId"] = row.ActorID.String()
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		outbox[index] = storage.OutboxEventModel{
			ID: row.ID, AggregateType: "workflow_run", AggregateID: runID.String(),
			EventType: row.EventType, Subject: "worksflow.workflow.run.event",
			Payload: encoded, Headers: json.RawMessage(`{}`),
			AvailableAt: row.CreatedAt.UTC(), CreatedAt: row.CreatedAt.UTC(),
		}
	}
	return outbox, nil
}

func insertDefinitionActivity(
	tx *gorm.DB,
	base definitionRow,
	version definitionVersionRow,
	actorID uuid.UUID,
	eventType string,
	subject string,
	occurredAt time.Time,
) error {
	metadata := mustJSON(map[string]any{
		"definitionId": base.ID.String(), "versionId": version.ID.String(),
		"version": version.Version, "contentHash": version.ContentHash, "published": version.Published,
	})
	if err := tx.Create(&storage.AuditEventModel{
		ID: uuid.New(), ProjectID: base.ProjectID, ActorID: &actorID, Action: eventType,
		TargetType: "workflow_definition_version", TargetID: version.ID.String(),
		Metadata: metadata, CreatedAt: occurredAt.UTC(),
	}).Error; err != nil {
		return err
	}
	if base.ProjectID == nil {
		return nil
	}
	payload := mustJSON(map[string]any{
		"projectId": base.ProjectID.String(), "definitionId": base.ID.String(),
		"versionId": version.ID.String(), "version": version.Version,
		"contentHash": version.ContentHash, "published": version.Published,
		"actorId": actorID.String(),
	})
	return tx.Create(&storage.OutboxEventModel{
		ID: uuid.New(), AggregateType: "workflow_definition", AggregateID: base.ID.String(),
		EventType: eventType, Subject: subject, Payload: payload, Headers: json.RawMessage(`{}`),
		AvailableAt: occurredAt.UTC(), CreatedAt: occurredAt.UTC(),
	}).Error
}

func (s *GORMStore) eventsToRows(runID string, events []Event, cursor uint64) ([]eventRow, error) {
	runUUID, err := parseUUID("event.runId", runID)
	if err != nil {
		return nil, err
	}
	rows := make([]eventRow, len(events))
	for index, event := range events {
		id := event.ID
		if id == "" {
			id = s.ids.NewID()
		}
		eventID, err := parseUUID("event.id", id)
		if err != nil {
			return nil, err
		}
		var nodeKey *string
		if event.NodeKey != "" {
			value := event.NodeKey
			nodeKey = &value
		}
		var actorID *uuid.UUID
		if event.ActorID != "" {
			parsed, err := parseUUID("event.actorId", event.ActorID)
			if err != nil {
				return nil, err
			}
			actorID = &parsed
		}
		payload := event.Payload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		rows[index] = eventRow{ID: eventID, RunID: runUUID, Sequence: cursor + uint64(index) + 1, EventType: event.Type, NodeKey: nodeKey, Payload: payload, ActorID: actorID, CreatedAt: event.CreatedAt.UTC()}
	}
	return rows, nil
}

func eventFromRow(row eventRow) Event {
	event := Event{ID: row.ID.String(), RunID: row.RunID.String(), Sequence: row.Sequence, Type: row.EventType, Payload: row.Payload, CreatedAt: row.CreatedAt}
	if row.NodeKey != nil {
		event.NodeKey = *row.NodeKey
	}
	if row.ActorID != nil {
		event.ActorID = row.ActorID.String()
	}
	return event
}

func toSliceRow(slice SliceRecord) (sliceRow, error) {
	id, err := parseUUID("slice.id", slice.ID)
	if err != nil {
		return sliceRow{}, err
	}
	projectID, err := parseUUID("slice.projectId", slice.ProjectID)
	if err != nil {
		return sliceRow{}, err
	}
	blueprintID, err := parseUUID("slice.blueprintRevisionId", slice.BlueprintRevisionID)
	if err != nil {
		return sliceRow{}, err
	}
	pageID, err := optionalUUID("slice.pageSpecRevisionId", slice.PageSpecRevisionID)
	if err != nil {
		return sliceRow{}, err
	}
	prototypeID, err := optionalUUID("slice.prototypeRevisionId", slice.PrototypeRevisionID)
	if err != nil {
		return sliceRow{}, err
	}
	ownerID, err := optionalUUID("slice.ownerId", slice.OwnerID)
	if err != nil {
		return sliceRow{}, err
	}
	return sliceRow{ID: id, ProjectID: projectID, SliceKey: slice.Key, Title: slice.Title, BlueprintRevisionID: blueprintID, PageSpecRevisionID: pageID, PrototypeRevisionID: prototypeID, SyncStatus: slice.SyncStatus, WorkflowStatus: slice.WorkflowStatus, OwnerID: ownerID, BlockerReason: slice.BlockerReason, UpdatedAt: slice.UpdatedAt}, nil
}

func parseUUID(field, value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%s must be UUID: %w", field, err)
	}
	return parsed, nil
}
func optionalUUID(field, value string) (*uuid.UUID, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := parseUUID(field, value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
func numericVersion(value string) (int, error) {
	trimmed := strings.TrimLeft(strings.TrimSpace(value), "vV")
	for index := len(trimmed) - 1; index >= 0; index-- {
		if trimmed[index] < '0' || trimmed[index] > '9' {
			trimmed = trimmed[index+1:]
			break
		}
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("schema version %q must end in a positive integer", value)
	}
	return parsed, nil
}
func mapGORMError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ErrNotFound
	}
	return err
}
func workflowUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate key") || strings.Contains(message, "unique constraint") || strings.Contains(message, "sqlstate 23505")
}
func sameOptionalUUID(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
func mustJSON(value any) json.RawMessage { encoded, _ := json.Marshal(value); return encoded }
