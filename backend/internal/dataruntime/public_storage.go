package dataruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type dataPublicTablePolicyModel struct {
	ProjectID      uuid.UUID       `gorm:"type:uuid;primaryKey"`
	TableID        uuid.UUID       `gorm:"type:uuid;primaryKey"`
	AllowRead      bool            `gorm:"not null"`
	AllowCreate    bool            `gorm:"not null"`
	AllowUpdate    bool            `gorm:"not null"`
	AllowDelete    bool            `gorm:"not null"`
	ReadableFields json.RawMessage `gorm:"type:jsonb;not null"`
	WritableFields json.RawMessage `gorm:"type:jsonb;not null"`
	Version        uint64          `gorm:"not null"`
	CreatedBy      uuid.UUID       `gorm:"type:uuid;not null"`
	UpdatedBy      uuid.UUID       `gorm:"type:uuid;not null"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (dataPublicTablePolicyModel) TableName() string { return "data_public_table_policies" }

type dataPublicCapabilityModel struct {
	ID                  uuid.UUID       `gorm:"type:uuid;primaryKey"`
	ProjectID           uuid.UUID       `gorm:"type:uuid;not null;index"`
	DeploymentID        uuid.UUID       `gorm:"type:uuid;not null;index"`
	DeploymentVersionID uuid.UUID       `gorm:"type:uuid;not null"`
	TokenDigest         []byte          `gorm:"not null"`
	AllowedOrigins      json.RawMessage `gorm:"type:jsonb;not null"`
	Status              string          `gorm:"not null"`
	ExpiresAt           time.Time       `gorm:"not null;index"`
	ActivatedAt         *time.Time
	RevokedAt           *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (dataPublicCapabilityModel) TableName() string { return "data_public_capabilities" }

type publicDeploymentModel struct {
	ID              uuid.UUID  `gorm:"type:uuid;primaryKey"`
	ProjectID       uuid.UUID  `gorm:"type:uuid;not null"`
	Environment     string     `gorm:"not null"`
	Status          string     `gorm:"not null"`
	ActiveVersionID *uuid.UUID `gorm:"type:uuid"`
}

func (publicDeploymentModel) TableName() string { return "deployments" }

type publicDeploymentVersionModel struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	DeploymentID uuid.UUID `gorm:"type:uuid;not null"`
	Status       string    `gorm:"not null"`
}

func (publicDeploymentVersionModel) TableName() string { return "deployment_versions" }

func (s *GORMStore) ListPublicTablePolicies(ctx context.Context, projectID string) ([]PublicTablePolicy, error) {
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, Invalid("projectId", "projectId must be a UUID")
	}
	type policyWithTable struct {
		dataPublicTablePolicyModel
		TableName string
	}
	var models []policyWithTable
	err = s.database.WithContext(ctx).
		Table("data_public_table_policies AS policies").
		Select("policies.*, tables.name AS table_name").
		Joins("JOIN data_tables AS tables ON tables.project_id = policies.project_id AND tables.id = policies.table_id").
		Where("policies.project_id = ?", projectUUID).
		Order("tables.name ASC, policies.table_id ASC").
		Scan(&models).Error
	if err != nil {
		return nil, fmt.Errorf("list public data policies: %w", err)
	}
	result := make([]PublicTablePolicy, 0, len(models))
	for _, model := range models {
		policy, err := publicPolicyFromModel(model.dataPublicTablePolicyModel, model.TableName)
		if err != nil {
			return nil, err
		}
		result = append(result, policy)
	}
	return result, nil
}

func (s *GORMStore) GetPublicTablePolicy(ctx context.Context, projectID, tableID string) (PublicTablePolicy, error) {
	projectUUID, projectErr := uuid.Parse(projectID)
	tableUUID, tableErr := uuid.Parse(tableID)
	if projectErr != nil || tableErr != nil {
		return PublicTablePolicy{}, Invalid("tableId", "projectId and tableId must be UUIDs")
	}
	var model dataPublicTablePolicyModel
	if err := s.database.WithContext(ctx).Where("project_id = ? AND table_id = ?", projectUUID, tableUUID).Take(&model).Error; err != nil {
		return PublicTablePolicy{}, mapStorageError(err, "Public table policy")
	}
	var table dataTableModel
	if err := s.database.WithContext(ctx).Select("name").Where("project_id = ? AND id = ?", projectUUID, tableUUID).Take(&table).Error; err != nil {
		return PublicTablePolicy{}, mapStorageError(err, "Table")
	}
	return publicPolicyFromModel(model, table.Name)
}

func (s *GORMStore) PutPublicTablePolicy(ctx context.Context, projectID, tableID, actorID string, input PublicTablePolicyInput) (PublicTablePolicy, error) {
	projectUUID, _ := uuid.Parse(projectID)
	tableUUID, _ := uuid.Parse(tableID)
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return PublicTablePolicy{}, Invalid("actorId", "actorId must be a UUID")
	}
	readable, _ := json.Marshal(input.ReadableFields)
	writable, _ := json.Marshal(input.WritableFields)
	now := s.now().UTC()
	var saved dataPublicTablePolicyModel
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		if _, err := s.loadTable(transaction, projectUUID, tableUUID); err != nil {
			return err
		}
		var existing dataPublicTablePolicyModel
		err = transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND table_id = ?", projectUUID, tableUUID).Take(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			saved = dataPublicTablePolicyModel{
				ProjectID: projectUUID, TableID: tableUUID,
				AllowRead: input.AllowRead, AllowCreate: input.AllowCreate,
				AllowUpdate: input.AllowUpdate, AllowDelete: input.AllowDelete,
				ReadableFields: readable, WritableFields: writable, Version: 1,
				CreatedBy: actorUUID, UpdatedBy: actorUUID, CreatedAt: now, UpdatedAt: now,
			}
			if err := transaction.Create(&saved).Error; err != nil {
				return fmt.Errorf("create public data policy: %w", err)
			}
		case err != nil:
			return fmt.Errorf("load public data policy: %w", err)
		default:
			saved = existing
			saved.AllowRead = input.AllowRead
			saved.AllowCreate = input.AllowCreate
			saved.AllowUpdate = input.AllowUpdate
			saved.AllowDelete = input.AllowDelete
			saved.ReadableFields = readable
			saved.WritableFields = writable
			saved.Version++
			saved.UpdatedBy = actorUUID
			saved.UpdatedAt = now
			if err := transaction.Model(&dataPublicTablePolicyModel{}).
				Where("project_id = ? AND table_id = ? AND version = ?", projectUUID, tableUUID, existing.Version).
				Updates(map[string]any{
					"allow_read": saved.AllowRead, "allow_create": saved.AllowCreate,
					"allow_update": saved.AllowUpdate, "allow_delete": saved.AllowDelete,
					"readable_fields": readable, "writable_fields": writable,
					"version": saved.Version, "updated_by": actorUUID, "updated_at": now,
				}).Error; err != nil {
				return fmt.Errorf("update public data policy: %w", err)
			}
		}
		return s.recordMutation(transaction, state, MutationContext{ActorID: actorID}, "configure", "public-policy", tableID, map[string]any{
			"allowRead": input.AllowRead, "allowCreate": input.AllowCreate,
			"allowUpdate": input.AllowUpdate, "allowDelete": input.AllowDelete,
			"version": saved.Version,
		}, true)
	})
	if err != nil {
		return PublicTablePolicy{}, err
	}
	return s.GetPublicTablePolicy(ctx, projectID, tableID)
}

func (s *GORMStore) DeletePublicTablePolicy(ctx context.Context, projectID, tableID, actorID string) error {
	projectUUID, _ := uuid.Parse(projectID)
	tableUUID, _ := uuid.Parse(tableID)
	return s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		result := transaction.Where("project_id = ? AND table_id = ?", projectUUID, tableUUID).Delete(&dataPublicTablePolicyModel{})
		if result.Error != nil {
			return fmt.Errorf("delete public data policy: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return NotFound("Public table policy")
		}
		return s.recordMutation(transaction, state, MutationContext{ActorID: actorID}, "delete", "public-policy", tableID, nil, true)
	})
}

func (s *GORMStore) PreparePublicCapability(
	ctx context.Context,
	input PreparePublicCapabilityInput,
	capabilityID string,
	tokenDigest []byte,
	origins []string,
	expiresAt time.Time,
) (publicCapabilityRecord, error) {
	projectUUID, _ := uuid.Parse(input.ProjectID)
	deploymentUUID, _ := uuid.Parse(input.DeploymentID)
	versionUUID, _ := uuid.Parse(input.DeploymentVersionID)
	capabilityUUID, _ := uuid.Parse(capabilityID)
	if len(tokenDigest) != sha256DigestBytes {
		return publicCapabilityRecord{}, errors.New("public capability digest must be SHA-256")
	}
	encodedOrigins, _ := json.Marshal(origins)
	now := s.now().UTC()
	model := dataPublicCapabilityModel{
		ID: capabilityUUID, ProjectID: projectUUID, DeploymentID: deploymentUUID,
		DeploymentVersionID: versionUUID, TokenDigest: append([]byte(nil), tokenDigest...),
		AllowedOrigins: encodedOrigins, Status: "pending", ExpiresAt: expiresAt.UTC(),
		CreatedAt: now, UpdatedAt: now,
	}
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		var deployment publicDeploymentModel
		if err := transaction.Clauses(clause.Locking{Strength: "SHARE"}).
			Where("id = ? AND project_id = ?", deploymentUUID, projectUUID).Take(&deployment).Error; err != nil {
			return mapStorageError(err, "Deployment")
		}
		var version publicDeploymentVersionModel
		if err := transaction.Where("id = ? AND deployment_id = ?", versionUUID, deploymentUUID).Take(&version).Error; err != nil {
			return mapStorageError(err, "Deployment version")
		}
		if version.Status == "failed" {
			return Conflict("A failed deployment version cannot receive a public data capability")
		}
		if err := transaction.Create(&model).Error; err != nil {
			return fmt.Errorf("prepare public data capability: %w", err)
		}
		return s.recordMutation(transaction, state, MutationContext{PublicDeploymentID: input.DeploymentID}, "prepare", "public-capability", capabilityID, map[string]any{
			"deploymentId": input.DeploymentID, "deploymentVersionId": input.DeploymentVersionID,
			"expiresAt": expiresAt.UTC(),
		}, true)
	})
	if err != nil {
		return publicCapabilityRecord{}, err
	}
	return publicCapabilityFromModel(model)
}

func (s *GORMStore) ActivatePublicCapability(ctx context.Context, projectID, deploymentID, capabilityID string) (publicCapabilityRecord, error) {
	projectUUID, _ := uuid.Parse(projectID)
	deploymentUUID, _ := uuid.Parse(deploymentID)
	capabilityUUID, _ := uuid.Parse(capabilityID)
	now := s.now().UTC()
	var activated dataPublicCapabilityModel
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		var deployment publicDeploymentModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND project_id = ?", deploymentUUID, projectUUID).Take(&deployment).Error; err != nil {
			return mapStorageError(err, "Deployment")
		}
		if deployment.Status != "ready" || deployment.ActiveVersionID == nil {
			return Conflict("The matching deployment version must be ready and active before its public data capability is activated")
		}
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND project_id = ? AND deployment_id = ?", capabilityUUID, projectUUID, deploymentUUID).
			Take(&activated).Error; err != nil {
			return mapStorageError(err, "Public data capability")
		}
		if activated.Status == "active" && activated.DeploymentVersionID == *deployment.ActiveVersionID {
			return nil
		}
		if activated.Status != "pending" || !activated.ExpiresAt.After(now) || activated.DeploymentVersionID != *deployment.ActiveVersionID {
			return Conflict("The public data capability is not pending for the active deployment version")
		}
		if err := transaction.Model(&dataPublicCapabilityModel{}).
			Where("project_id = ? AND deployment_id = ? AND status = ?", projectUUID, deploymentUUID, "active").
			Updates(map[string]any{"status": "revoked", "revoked_at": now, "updated_at": now}).Error; err != nil {
			return fmt.Errorf("supersede public data capability: %w", err)
		}
		if err := transaction.Model(&dataPublicCapabilityModel{}).
			Where("id = ? AND status = ?", capabilityUUID, "pending").
			Updates(map[string]any{"status": "active", "activated_at": now, "updated_at": now}).Error; err != nil {
			return fmt.Errorf("activate public data capability: %w", err)
		}
		activated.Status = "active"
		activated.ActivatedAt = &now
		activated.UpdatedAt = now
		return s.recordMutation(transaction, state, MutationContext{PublicDeploymentID: deploymentID}, "activate", "public-capability", capabilityID, map[string]any{
			"deploymentId": deploymentID, "deploymentVersionId": activated.DeploymentVersionID.String(),
		}, true)
	})
	if err != nil {
		return publicCapabilityRecord{}, err
	}
	return publicCapabilityFromModel(activated)
}

func (s *GORMStore) RevokePublicCapability(ctx context.Context, projectID, deploymentID, capabilityID string) error {
	projectUUID, _ := uuid.Parse(projectID)
	deploymentUUID, _ := uuid.Parse(deploymentID)
	capabilityUUID, _ := uuid.Parse(capabilityID)
	now := s.now().UTC()
	return s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		result := transaction.Model(&dataPublicCapabilityModel{}).
			Where("id = ? AND project_id = ? AND deployment_id = ? AND status <> ?", capabilityUUID, projectUUID, deploymentUUID, "revoked").
			Updates(map[string]any{"status": "revoked", "revoked_at": now, "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("revoke public data capability: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			var existing int64
			if err := transaction.Model(&dataPublicCapabilityModel{}).
				Where("id = ? AND project_id = ? AND deployment_id = ?", capabilityUUID, projectUUID, deploymentUUID).Count(&existing).Error; err != nil {
				return err
			}
			if existing == 0 {
				return NotFound("Public data capability")
			}
			return nil
		}
		return s.recordMutation(transaction, state, MutationContext{PublicDeploymentID: deploymentID}, "revoke", "public-capability", capabilityID, map[string]any{
			"deploymentId": deploymentID,
		}, true)
	})
}

func (s *GORMStore) RevokeDeploymentPublicCapabilities(ctx context.Context, projectID, deploymentID string) error {
	projectUUID, _ := uuid.Parse(projectID)
	deploymentUUID, _ := uuid.Parse(deploymentID)
	now := s.now().UTC()
	return s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		var deployment publicDeploymentModel
		if err := transaction.Where("id = ? AND project_id = ?", deploymentUUID, projectUUID).Take(&deployment).Error; err != nil {
			return mapStorageError(err, "Deployment")
		}
		result := transaction.Model(&dataPublicCapabilityModel{}).
			Where("project_id = ? AND deployment_id = ? AND status <> ?", projectUUID, deploymentUUID, "revoked").
			Updates(map[string]any{"status": "revoked", "revoked_at": now, "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("revoke deployment public data capabilities: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}
		return s.recordMutation(transaction, state, MutationContext{PublicDeploymentID: deploymentID}, "revoke-all", "public-capability", deploymentID, map[string]any{
			"deploymentId": deploymentID, "revokedCount": result.RowsAffected,
		}, true)
	})
}

func (s *GORMStore) GetActivePublicDeploymentRuntime(ctx context.Context, projectID, deploymentID string) (publicCapabilityRecord, error) {
	projectUUID, _ := uuid.Parse(projectID)
	deploymentUUID, _ := uuid.Parse(deploymentID)
	var model dataPublicCapabilityModel
	err := s.database.WithContext(ctx).
		Table("data_public_capabilities AS capabilities").
		Select("capabilities.*").
		Joins("JOIN deployments ON deployments.id = capabilities.deployment_id AND deployments.project_id = capabilities.project_id").
		Where("capabilities.project_id = ? AND capabilities.deployment_id = ? AND capabilities.status = ?", projectUUID, deploymentUUID, "active").
		Where("deployments.status = ? AND deployments.active_version_id = capabilities.deployment_version_id", "ready").
		Where("capabilities.expires_at > ?", s.now().UTC()).
		Take(&model).Error
	if err != nil {
		return publicCapabilityRecord{}, mapStorageError(err, "Active public data runtime")
	}
	return publicCapabilityFromModel(model)
}

func (s *GORMStore) FindPublicCapability(ctx context.Context, capabilityID string) (publicCapabilityRecord, error) {
	capabilityUUID, err := uuid.Parse(capabilityID)
	if err != nil {
		return publicCapabilityRecord{}, NotFound("Public data capability")
	}
	var model dataPublicCapabilityModel
	err = s.database.WithContext(ctx).
		Table("data_public_capabilities AS capabilities").
		Select("capabilities.*").
		Joins("JOIN deployments ON deployments.id = capabilities.deployment_id AND deployments.project_id = capabilities.project_id").
		Where("capabilities.id = ? AND capabilities.status = ?", capabilityUUID, "active").
		Where("deployments.status = ? AND deployments.active_version_id = capabilities.deployment_version_id", "ready").
		Take(&model).Error
	if err != nil {
		return publicCapabilityRecord{}, mapStorageError(err, "Public data capability")
	}
	return publicCapabilityFromModel(model)
}

func (s *GORMStore) PublicPreflightOrigins(ctx context.Context, deploymentID string) ([]string, error) {
	deploymentUUID, err := uuid.Parse(deploymentID)
	if err != nil {
		return nil, NotFound("Deployment")
	}
	var model dataPublicCapabilityModel
	err = s.database.WithContext(ctx).
		Table("data_public_capabilities AS capabilities").
		Select("capabilities.allowed_origins").
		Joins("JOIN deployments ON deployments.id = capabilities.deployment_id AND deployments.project_id = capabilities.project_id").
		Where("capabilities.deployment_id = ? AND capabilities.status = ?", deploymentUUID, "active").
		Where("deployments.status = ? AND deployments.active_version_id = capabilities.deployment_version_id", "ready").
		Where("capabilities.expires_at > ?", s.now().UTC()).
		Take(&model).Error
	if err != nil {
		return nil, mapStorageError(err, "Deployment")
	}
	var origins []string
	if err := json.Unmarshal(model.AllowedOrigins, &origins); err != nil {
		return nil, fmt.Errorf("decode public data origins: %w", err)
	}
	return origins, nil
}

func publicPolicyFromModel(model dataPublicTablePolicyModel, tableName string) (PublicTablePolicy, error) {
	var readable, writable []string
	if err := json.Unmarshal(model.ReadableFields, &readable); err != nil {
		return PublicTablePolicy{}, fmt.Errorf("decode readable public fields: %w", err)
	}
	if err := json.Unmarshal(model.WritableFields, &writable); err != nil {
		return PublicTablePolicy{}, fmt.Errorf("decode writable public fields: %w", err)
	}
	return PublicTablePolicy{
		ProjectID: model.ProjectID.String(), TableID: model.TableID.String(), TableName: tableName,
		AllowRead: model.AllowRead, AllowCreate: model.AllowCreate,
		AllowUpdate: model.AllowUpdate, AllowDelete: model.AllowDelete,
		ReadableFields: readable, WritableFields: writable, Version: model.Version,
		CreatedAt: model.CreatedAt.UTC(), UpdatedAt: model.UpdatedAt.UTC(),
	}, nil
}

func publicCapabilityFromModel(model dataPublicCapabilityModel) (publicCapabilityRecord, error) {
	var origins []string
	if err := json.Unmarshal(model.AllowedOrigins, &origins); err != nil {
		return publicCapabilityRecord{}, fmt.Errorf("decode public data origins: %w", err)
	}
	return publicCapabilityRecord{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(), DeploymentID: model.DeploymentID.String(),
		DeploymentVersionID: model.DeploymentVersionID.String(), TokenDigest: append([]byte(nil), model.TokenDigest...),
		AllowedOrigins: origins, Status: model.Status, ExpiresAt: model.ExpiresAt.UTC(), ActivatedAt: model.ActivatedAt,
	}, nil
}

const sha256DigestBytes = 32
