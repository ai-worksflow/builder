package dataruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *GORMStore) ListMetadata(ctx context.Context, projectID string, kind MetadataKind) ([]MetadataItem, error) {
	projectUUID, _ := uuid.Parse(projectID)
	return s.listMetadata(ctx, s.database, projectUUID, kind)
}

func (s *GORMStore) listMetadata(ctx context.Context, query *gorm.DB, projectID uuid.UUID, kind MetadataKind) ([]MetadataItem, error) {
	var models []dataMetadataModel
	if err := query.WithContext(ctx).Where("project_id = ? AND kind = ?", projectID, string(kind)).
		Order("created_at ASC, id ASC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list data metadata: %w", err)
	}
	items := make([]MetadataItem, 0, len(models))
	for _, model := range models {
		items = append(items, metadataFromModel(model))
	}
	return items, nil
}

func (s *GORMStore) GetMetadata(ctx context.Context, projectID string, kind MetadataKind, itemID string) (MetadataItem, error) {
	projectUUID, _ := uuid.Parse(projectID)
	itemUUID, _ := uuid.Parse(itemID)
	model, err := s.loadMetadata(s.database.WithContext(ctx), projectUUID, kind, itemUUID)
	if err != nil {
		return MetadataItem{}, err
	}
	return metadataFromModel(model), nil
}

func (s *GORMStore) loadMetadata(query *gorm.DB, projectID uuid.UUID, kind MetadataKind, itemID uuid.UUID) (dataMetadataModel, error) {
	var model dataMetadataModel
	if err := query.Where("project_id = ? AND kind = ? AND id = ?", projectID, string(kind), itemID).Take(&model).Error; err != nil {
		return dataMetadataModel{}, mapStorageError(err, "Metadata item")
	}
	return model, nil
}

func metadataFromModel(model dataMetadataModel) MetadataItem {
	return MetadataItem{
		ID: model.ID.String(), Kind: MetadataKind(model.Kind), Payload: cloneRaw(model.Payload),
		CreatedAt: model.CreatedAt.UTC(), UpdatedAt: model.UpdatedAt.UTC(),
	}
}

func (s *GORMStore) CreateMetadata(
	ctx context.Context,
	projectID string,
	kind MetadataKind,
	mutation MutationContext,
	payload json.RawMessage,
	uniqueKey string,
) (MetadataItem, error) {
	projectUUID, _ := uuid.Parse(projectID)
	now := s.now().UTC()
	model := dataMetadataModel{
		ID: uuid.New(), ProjectID: projectUUID, Kind: string(kind),
		UniqueKey: nullableUniqueKey(uniqueKey), Payload: cloneRaw(payload), CreatedAt: now, UpdatedAt: now,
	}
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		var count int64
		if err := transaction.Model(&dataMetadataModel{}).
			Where("project_id = ? AND kind = ?", projectUUID, string(kind)).Count(&count).Error; err != nil {
			return err
		}
		if count >= MaxMetadataItemsPerKind {
			return Conflict(fmt.Sprintf("%s may contain at most %d items", kind, MaxMetadataItemsPerKind))
		}
		if err := transaction.Create(&model).Error; err != nil {
			return mapStorageError(err, string(kind))
		}
		return s.recordMutation(transaction, state, mutation, "create", string(kind), model.ID.String(), nil, true)
	})
	if err != nil {
		return MetadataItem{}, err
	}
	return metadataFromModel(model), nil
}

func (s *GORMStore) UpdateMetadata(
	ctx context.Context,
	projectID string,
	kind MetadataKind,
	itemID string,
	mutation MutationContext,
	patch map[string]json.RawMessage,
) (MetadataItem, error) {
	projectUUID, _ := uuid.Parse(projectID)
	itemUUID, _ := uuid.Parse(itemID)
	var model dataMetadataModel
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		model, err = s.loadMetadata(transaction, projectUUID, kind, itemUUID)
		if err != nil {
			return err
		}
		payload, uniqueKey, err := NormalizeMetadataPatch(kind, patch, model.Payload)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		model.Payload = cloneRaw(payload)
		model.UniqueKey = nullableUniqueKey(uniqueKey)
		model.UpdatedAt = now
		if err := transaction.Model(&dataMetadataModel{}).Where("id = ?", itemUUID).
			Updates(map[string]any{"payload": payload, "unique_key": model.UniqueKey, "updated_at": now}).Error; err != nil {
			return mapStorageError(err, string(kind))
		}
		return s.recordMutation(transaction, state, mutation, "update", string(kind), itemID, nil, true)
	})
	if err != nil {
		return MetadataItem{}, err
	}
	return metadataFromModel(model), nil
}

func (s *GORMStore) DeleteMetadata(ctx context.Context, projectID string, kind MetadataKind, itemID string, mutation MutationContext) error {
	projectUUID, _ := uuid.Parse(projectID)
	itemUUID, _ := uuid.Parse(itemID)
	return s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		model, err := s.loadMetadata(transaction, projectUUID, kind, itemUUID)
		if err != nil {
			return err
		}
		if err := transaction.Delete(&model).Error; err != nil {
			return fmt.Errorf("delete data metadata: %w", err)
		}
		return s.recordMutation(transaction, state, mutation, "delete", string(kind), itemID, nil, true)
	})
}

func nullableUniqueKey(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func (s *GORMStore) ListVariables(ctx context.Context, projectID string) ([]EnvironmentVariable, error) {
	projectUUID, _ := uuid.Parse(projectID)
	return s.listVariables(ctx, s.database, projectUUID)
}

func (s *GORMStore) listVariables(ctx context.Context, query *gorm.DB, projectID uuid.UUID) ([]EnvironmentVariable, error) {
	// encrypted_value and plain_value are intentionally omitted from the SELECT.
	// Neither can reach a metadata DTO through accidental struct serialization.
	var models []dataEnvironmentVariableModel
	if err := query.WithContext(ctx).
		Select("id", "project_id", "name", "scope", "kind", "value_bytes", "created_at", "updated_at").
		Where("project_id = ?", projectID).Order("scope ASC, name ASC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list data environment variables: %w", err)
	}
	items := make([]EnvironmentVariable, 0, len(models))
	for _, model := range models {
		items = append(items, variableFromModel(model))
	}
	return items, nil
}

func (s *GORMStore) PublicEnvironment(ctx context.Context, projectID string, scope EnvironmentScope) (map[string]string, error) {
	projectUUID, _ := uuid.Parse(projectID)
	type publicVariable struct {
		Name       string
		PlainValue *string
	}
	var models []publicVariable
	if err := s.database.WithContext(ctx).Model(&dataEnvironmentVariableModel{}).
		Select("name", "plain_value").
		Where("project_id = ? AND scope = ? AND kind = ?", projectUUID, string(scope), string(VariablePlain)).
		Order("name ASC").Scan(&models).Error; err != nil {
		return nil, fmt.Errorf("list public data environment variables: %w", err)
	}
	result := make(map[string]string, len(models))
	for _, model := range models {
		if !IsPublicEnvironmentName(model.Name) {
			continue
		}
		if model.PlainValue == nil {
			return nil, errors.New("plain environment variable has no public value")
		}
		result[model.Name] = *model.PlainValue
	}
	return result, nil
}

func variableFromModel(model dataEnvironmentVariableModel) EnvironmentVariable {
	return EnvironmentVariable{
		ID: model.ID.String(), Name: model.Name, Scope: EnvironmentScope(model.Scope),
		Kind: EnvironmentVariableKind(model.Kind), MaskedValue: "••••••••",
		ValueBytes: model.ValueBytes, CreatedAt: model.CreatedAt.UTC(), UpdatedAt: model.UpdatedAt.UTC(),
	}
}

func (s *GORMStore) SetVariable(ctx context.Context, projectID string, mutation MutationContext, input EnvironmentVariableInput) (EnvironmentVariable, error) {
	projectUUID, _ := uuid.Parse(projectID)
	var model dataEnvironmentVariableModel
	var existed bool
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		err = transaction.Where("project_id = ? AND name = ? AND scope = ?", projectUUID, input.Name, string(input.Scope)).Take(&model).Error
		if err == nil {
			existed = true
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			var count int64
			if err := transaction.Model(&dataEnvironmentVariableModel{}).Where("project_id = ?", projectUUID).Count(&count).Error; err != nil {
				return err
			}
			if count >= MaxVariablesPerProject {
				return Conflict(fmt.Sprintf("A project may contain at most %d environment variables", MaxVariablesPerProject))
			}
			model.ID = uuid.New()
			model.ProjectID = projectUUID
			model.CreatedAt = s.now().UTC()
		} else {
			return fmt.Errorf("load data environment variable: %w", err)
		}
		now := s.now().UTC()
		model.Name = input.Name
		model.Scope = string(input.Scope)
		model.Kind = string(input.Kind)
		model.ValueBytes = len([]byte(input.Value))
		model.UpdatedAt = now
		if input.Kind == VariableSecret {
			associatedData := variableAssociatedData(projectUUID, model.ID, input)
			model.EncryptedValue, err = s.sealer.Seal([]byte(input.Value), associatedData)
			if err != nil {
				return fmt.Errorf("encrypt data environment variable: %w", err)
			}
			model.PlainValue = nil
		} else {
			plainValue := input.Value
			model.PlainValue = &plainValue
			model.EncryptedValue = nil
		}
		if existed {
			if err := transaction.Model(&dataEnvironmentVariableModel{}).Where("id = ?", model.ID).
				Updates(map[string]any{
					"kind": model.Kind, "encrypted_value": model.EncryptedValue, "plain_value": model.PlainValue,
					"value_bytes": model.ValueBytes, "updated_at": now,
				}).Error; err != nil {
				return fmt.Errorf("update data environment variable: %w", err)
			}
		} else if err := transaction.Create(&model).Error; err != nil {
			return mapStorageError(err, "Environment variable")
		}
		action := "create"
		if existed {
			action = "update"
		}
		return s.recordMutation(transaction, state, mutation, action, "environment-variable", model.ID.String(), map[string]any{
			"name": model.Name, "scope": model.Scope, "kind": model.Kind,
		}, true)
	})
	if err != nil {
		return EnvironmentVariable{}, err
	}
	// Make the plaintext unreachable as early as possible. Strings cannot be
	// zeroed in Go, so no copy is retained on the store or returned DTO.
	return variableFromModel(model), nil
}

func (s *GORMStore) DeleteVariable(ctx context.Context, projectID, variableID string, mutation MutationContext) error {
	projectUUID, _ := uuid.Parse(projectID)
	variableUUID, _ := uuid.Parse(variableID)
	return s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		var model dataEnvironmentVariableModel
		if err := transaction.Where("project_id = ? AND id = ?", projectUUID, variableUUID).Take(&model).Error; err != nil {
			return mapStorageError(err, "Environment variable")
		}
		if err := transaction.Delete(&model).Error; err != nil {
			return fmt.Errorf("delete data environment variable: %w", err)
		}
		return s.recordMutation(transaction, state, mutation, "delete", "environment-variable", variableID, map[string]any{
			"name": model.Name, "scope": model.Scope,
		}, true)
	})
}

func variableAssociatedData(projectID, variableID uuid.UUID, input EnvironmentVariableInput) string {
	return "worksflow:data-variable:v1:" + projectID.String() + ":" + variableID.String() + ":" + input.Name + ":" + string(input.Scope) + ":" + string(input.Kind)
}
