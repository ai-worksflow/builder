package dataruntime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type dataProjectStateModel struct {
	ProjectID uuid.UUID `gorm:"type:uuid;primaryKey"`
	Revision  uint64    `gorm:"not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (dataProjectStateModel) TableName() string { return "data_project_states" }

type dataTableModel struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	ProjectID uuid.UUID `gorm:"type:uuid;not null;index"`
	Name      string    `gorm:"not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (dataTableModel) TableName() string { return "data_tables" }

type dataColumnModel struct {
	ID           uuid.UUID       `gorm:"type:uuid;primaryKey"`
	TableID      uuid.UUID       `gorm:"type:uuid;not null;index"`
	Name         string          `gorm:"not null"`
	DataType     string          `gorm:"not null"`
	Required     bool            `gorm:"not null"`
	DefaultValue json.RawMessage `gorm:"type:jsonb"`
	Position     int             `gorm:"not null"`
	CreatedAt    time.Time
}

func (dataColumnModel) TableName() string { return "data_columns" }

type dataRecordModel struct {
	ID        uuid.UUID       `gorm:"type:uuid;primaryKey"`
	ProjectID uuid.UUID       `gorm:"type:uuid;not null;index"`
	TableID   uuid.UUID       `gorm:"type:uuid;not null;index"`
	Values    json.RawMessage `gorm:"type:jsonb;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (dataRecordModel) TableName() string { return "data_records" }

type dataMetadataModel struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	ProjectID uuid.UUID `gorm:"type:uuid;not null;index"`
	Kind      string    `gorm:"not null"`
	UniqueKey *string
	Payload   json.RawMessage `gorm:"type:jsonb;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (dataMetadataModel) TableName() string { return "data_metadata_items" }

type dataEnvironmentVariableModel struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey"`
	ProjectID      uuid.UUID `gorm:"type:uuid;not null;index"`
	Name           string    `gorm:"not null"`
	Scope          string    `gorm:"not null"`
	Kind           string    `gorm:"not null"`
	EncryptedValue []byte
	PlainValue     *string
	ValueBytes     int `gorm:"not null"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (dataEnvironmentVariableModel) TableName() string { return "data_environment_variables" }

type dataMigrationPreviewModel struct {
	ID           uuid.UUID       `gorm:"type:uuid;primaryKey"`
	ProjectID    uuid.UUID       `gorm:"type:uuid;not null;index"`
	TokenHash    []byte          `gorm:"not null;uniqueIndex"`
	BaseRevision uint64          `gorm:"not null"`
	Plan         json.RawMessage `gorm:"type:jsonb;not null"`
	Changes      json.RawMessage `gorm:"type:jsonb;not null"`
	ResultTables json.RawMessage `gorm:"type:jsonb;not null"`
	CreatedBy    uuid.UUID       `gorm:"type:uuid;not null"`
	CreatedAt    time.Time
	ExpiresAt    time.Time `gorm:"not null;index"`
	ConsumedAt   *time.Time
}

func (dataMigrationPreviewModel) TableName() string { return "data_migration_previews" }

type dataMigrationModel struct {
	ID        uuid.UUID       `gorm:"type:uuid;primaryKey"`
	ProjectID uuid.UUID       `gorm:"type:uuid;not null;index"`
	PreviewID uuid.UUID       `gorm:"type:uuid;not null"`
	Changes   json.RawMessage `gorm:"type:jsonb;not null"`
	AppliedBy uuid.UUID       `gorm:"type:uuid;not null"`
	AppliedAt time.Time
}

func (dataMigrationModel) TableName() string { return "data_migrations" }

type dataConnectionModel struct {
	ProjectID    uuid.UUID       `gorm:"type:uuid;primaryKey"`
	Provider     string          `gorm:"not null"`
	Endpoint     string          `gorm:"not null"`
	Status       string          `gorm:"not null"`
	HTTPStatus   int             `gorm:"not null"`
	LatencyMS    int64           `gorm:"not null"`
	SchemaTables json.RawMessage `gorm:"type:jsonb;not null"`
	ConnectedAt  time.Time
	UpdatedAt    time.Time
}

func (dataConnectionModel) TableName() string { return "data_connections" }

type GORMStoreOptions struct {
	Now             func() time.Time
	ConfirmationTTL time.Duration
	TokenSource     func() (string, error)
}

type GORMStore struct {
	database        *gorm.DB
	sealer          ValueSealer
	now             func() time.Time
	confirmationTTL time.Duration
	tokenSource     func() (string, error)
}

func NewGORMStore(database *gorm.DB, sealer ValueSealer, options GORMStoreOptions) (*GORMStore, error) {
	if database == nil || sealer == nil {
		return nil, errors.New("data runtime database and value sealer are required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ConfirmationTTL <= 0 {
		options.ConfirmationTTL = DefaultMigrationConfirmationTTL
	}
	if options.ConfirmationTTL > 24*time.Hour {
		return nil, errors.New("data runtime confirmation TTL may not exceed 24 hours")
	}
	if options.TokenSource == nil {
		options.TokenSource = newConfirmationToken
	}
	return &GORMStore{
		database: database, sealer: sealer, now: options.Now,
		confirmationTTL: options.ConfirmationTTL, tokenSource: options.TokenSource,
	}, nil
}

func (s *GORMStore) Snapshot(ctx context.Context, projectID string) (ProjectSnapshot, error) {
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return ProjectSnapshot{}, Invalid("projectId", "projectId must be a UUID")
	}
	var snapshot ProjectSnapshot
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		value, err := s.snapshotWithQuery(ctx, transaction, projectUUID)
		if err == nil {
			snapshot = value
		}
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ProjectSnapshot{}, err
	}
	return snapshot, nil
}

func (s *GORMStore) snapshotWithQuery(ctx context.Context, query *gorm.DB, projectUUID uuid.UUID) (ProjectSnapshot, error) {
	updatedAt, err := s.projectUpdatedAt(ctx, query, projectUUID)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	tables, err := s.listTables(ctx, query, projectUUID)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	authUsers, err := s.listMetadata(ctx, query, projectUUID, MetadataAuthUsers)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	storageObjects, err := s.listMetadata(ctx, query, projectUUID, MetadataStorageObjects)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	serverFunctions, err := s.listMetadata(ctx, query, projectUUID, MetadataServerFunctions)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	variables, err := s.listVariables(ctx, query, projectUUID)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	migrations, err := s.listMigrations(ctx, query, projectUUID)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	audit, err := s.listAudit(ctx, query, projectUUID)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	connection, err := s.getConnection(ctx, query, projectUUID)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	return ProjectSnapshot{
		ProjectID: projectUUID.String(), Tables: tables, AuthUsers: authUsers,
		StorageObjects: storageObjects, ServerFunctions: serverFunctions,
		Variables: variables, Migrations: migrations, Audit: audit,
		Connection: connection, UpdatedAt: updatedAt,
	}, nil
}

func (s *GORMStore) projectUpdatedAt(ctx context.Context, query *gorm.DB, projectID uuid.UUID) (time.Time, error) {
	var state dataProjectStateModel
	err := query.WithContext(ctx).Where("project_id = ?", projectID).Take(&state).Error
	if err == nil {
		return state.UpdatedAt.UTC(), nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, fmt.Errorf("load data project state: %w", err)
	}
	var project storage.ProjectModel
	if err := query.WithContext(ctx).Select("updated_at").Where("id = ?", projectID).Take(&project).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return time.Time{}, NotFound("Project")
		}
		return time.Time{}, fmt.Errorf("load project timestamp: %w", err)
	}
	return project.UpdatedAt.UTC(), nil
}

func (s *GORMStore) lockState(transaction *gorm.DB, projectID uuid.UUID) (*dataProjectStateModel, error) {
	now := s.now().UTC()
	seed := dataProjectStateModel{ProjectID: projectID, Revision: 0, CreatedAt: now, UpdatedAt: now}
	if err := transaction.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
		return nil, fmt.Errorf("initialize data project state: %w", err)
	}
	var state dataProjectStateModel
	if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("project_id = ?", projectID).Take(&state).Error; err != nil {
		return nil, fmt.Errorf("lock data project state: %w", err)
	}
	return &state, nil
}

func (s *GORMStore) recordMutation(
	transaction *gorm.DB,
	state *dataProjectStateModel,
	mutation MutationContext,
	action, resource, resourceID string,
	details map[string]any,
	touch bool,
) error {
	var actorID *uuid.UUID
	var publicDeploymentID *uuid.UUID
	if strings.TrimSpace(mutation.ActorID) != "" {
		parsed, err := uuid.Parse(mutation.ActorID)
		if err != nil {
			return Invalid("actorId", "actorId must be a UUID")
		}
		actorID = &parsed
	}
	if strings.TrimSpace(mutation.PublicDeploymentID) != "" {
		parsed, err := uuid.Parse(mutation.PublicDeploymentID)
		if err != nil {
			return Invalid("publicDeploymentId", "publicDeploymentId must be a UUID")
		}
		publicDeploymentID = &parsed
	}
	if (actorID == nil) == (publicDeploymentID == nil) {
		return Invalid("mutation", "exactly one authenticated actor or public deployment is required")
	}
	if details == nil {
		details = map[string]any{}
	}
	if publicDeploymentID != nil {
		details["publicDeploymentId"] = publicDeploymentID.String()
	}
	metadata, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode data audit metadata: %w", err)
	}
	now := s.now().UTC()
	if touch {
		state.Revision++
		state.UpdatedAt = now
		if err := transaction.Model(&dataProjectStateModel{}).Where("project_id = ?", state.ProjectID).
			Updates(map[string]any{"revision": state.Revision, "updated_at": now}).Error; err != nil {
			return fmt.Errorf("advance data project revision: %w", err)
		}
	}
	targetID := resourceID
	if targetID == "" {
		targetID = state.ProjectID.String()
	}
	var requestID *string
	if value := strings.TrimSpace(mutation.RequestID); value != "" {
		requestID = &value
	}
	if err := transaction.Create(&storage.AuditEventModel{
		ID: uuid.New(), ProjectID: &state.ProjectID, ActorID: actorID,
		RequestID: requestID, Action: "data." + resource + "." + action,
		TargetType: "data." + resource, TargetID: targetID,
		Metadata: metadata, CreatedAt: now,
	}).Error; err != nil {
		return fmt.Errorf("write data audit event: %w", err)
	}
	payload := map[string]any{
		"projectId": state.ProjectID.String(),
		"action": action, "resource": resource, "resourceId": targetID,
		"details": details, "revision": state.Revision,
	}
	if actorID != nil {
		payload["actorId"] = actorID.String()
	}
	if publicDeploymentID != nil {
		payload["publicDeploymentId"] = publicDeploymentID.String()
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode data outbox event: %w", err)
	}
	eventType := "data." + resource + "." + action
	if err := transaction.Create(&storage.OutboxEventModel{
		ID: uuid.New(), AggregateType: "data-project", AggregateID: state.ProjectID.String(),
		EventType: eventType, Subject: "worksflow.data." + resource + "." + action,
		Payload: encodedPayload, Headers: json.RawMessage(`{}`), AvailableAt: now, CreatedAt: now,
	}).Error; err != nil {
		return fmt.Errorf("enqueue data outbox event: %w", err)
	}
	return nil
}

func (s *GORMStore) listAudit(ctx context.Context, query *gorm.DB, projectID uuid.UUID) ([]AuditEvent, error) {
	var models []storage.AuditEventModel
	if err := query.WithContext(ctx).
		Where("project_id = ? AND target_type LIKE ?", projectID, "data.%").
		Order("created_at DESC").Limit(200).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list data audit events: %w", err)
	}
	items := make([]AuditEvent, 0, len(models))
	for _, model := range models {
		details := map[string]json.RawMessage{}
		if len(model.Metadata) > 0 {
			_ = json.Unmarshal(model.Metadata, &details)
		}
		resource := strings.TrimPrefix(model.TargetType, "data.")
		action := strings.TrimPrefix(model.Action, "data."+resource+".")
		resourceID := model.TargetID
		if resource == "supabase" && resourceID == projectID.String() {
			resourceID = ""
		}
		items = append(items, AuditEvent{
			ID: model.ID.String(), Action: action, Resource: resource,
			ResourceID: resourceID, CreatedAt: model.CreatedAt.UTC(), Details: details,
		})
	}
	return items, nil
}

func (s *GORMStore) listMigrations(ctx context.Context, query *gorm.DB, projectID uuid.UUID) ([]AppliedMigration, error) {
	var models []dataMigrationModel
	if err := query.WithContext(ctx).Where("project_id = ?", projectID).
		Order("applied_at DESC").Limit(100).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list data migrations: %w", err)
	}
	result := make([]AppliedMigration, 0, len(models))
	for _, model := range models {
		var changes []MigrationChange
		if err := json.Unmarshal(model.Changes, &changes); err != nil {
			return nil, fmt.Errorf("decode data migration changes: %w", err)
		}
		result = append(result, AppliedMigration{
			ID: model.ID.String(), PreviewID: model.PreviewID.String(),
			AppliedAt: model.AppliedAt.UTC(), Changes: changes,
		})
	}
	return result, nil
}

func (s *GORMStore) getConnection(ctx context.Context, query *gorm.DB, projectID uuid.UUID) (*ConnectionMetadata, error) {
	var model dataConnectionModel
	err := query.WithContext(ctx).Where("project_id = ?", projectID).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load data connection: %w", err)
	}
	var tables []string
	if err := json.Unmarshal(model.SchemaTables, &tables); err != nil {
		return nil, fmt.Errorf("decode data connection schema: %w", err)
	}
	return &ConnectionMetadata{
		Provider: model.Provider, Endpoint: model.Endpoint, Status: model.Status,
		ConnectedAt: model.ConnectedAt.UTC(), UpdatedAt: model.UpdatedAt.UTC(),
		HTTPStatus: model.HTTPStatus, LatencyMS: model.LatencyMS, SchemaTables: tables,
	}, nil
}

func (s *GORMStore) SaveConnection(ctx context.Context, projectID string, mutation MutationContext, result SupabaseConnectionResult) (ConnectionMetadata, error) {
	projectUUID, _ := uuid.Parse(projectID)
	if err := validateSuccessfulConnectionResult(&result); err != nil {
		return ConnectionMetadata{}, err
	}
	tables := append([]string(nil), result.SchemaTables...)
	if len(tables) > 256 {
		return ConnectionMetadata{}, NewError(CodeConnectionFailed, 502, "Supabase schema summary is invalid")
	}
	encodedTables, _ := json.Marshal(tables)
	now := s.now().UTC()
	model := dataConnectionModel{
		ProjectID: projectUUID, Provider: "supabase", Endpoint: result.Endpoint,
		Status: "connected", HTTPStatus: result.Status, LatencyMS: result.LatencyMS,
		SchemaTables: encodedTables, ConnectedAt: now, UpdatedAt: now,
	}
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		if err := transaction.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "project_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"provider", "endpoint", "status", "http_status", "latency_ms", "schema_tables", "connected_at", "updated_at",
			}),
		}).Create(&model).Error; err != nil {
			return fmt.Errorf("save data connection: %w", err)
		}
		return s.recordMutation(transaction, state, mutation, "connect", "supabase", "", map[string]any{
			"endpoint": result.Endpoint, "status": "connected", "schemaTableCount": len(tables),
		}, true)
	})
	if err != nil {
		return ConnectionMetadata{}, err
	}
	return ConnectionMetadata{
		Provider: model.Provider, Endpoint: model.Endpoint, Status: model.Status,
		ConnectedAt: model.ConnectedAt, UpdatedAt: model.UpdatedAt,
		HTTPStatus: model.HTTPStatus, LatencyMS: model.LatencyMS, SchemaTables: tables,
	}, nil
}

func tokenDigest(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}

func mapStorageError(err error, resource string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return NotFound(resource)
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "duplicate key") || strings.Contains(message, "unique constraint") {
		return Conflict(resource + " conflicts with an existing resource")
	}
	return err
}
