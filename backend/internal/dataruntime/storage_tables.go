package dataruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *GORMStore) ListTables(ctx context.Context, projectID string) ([]Table, error) {
	projectUUID, _ := uuid.Parse(projectID)
	return s.listTables(ctx, s.database.WithContext(ctx), projectUUID)
}

func (s *GORMStore) listTables(ctx context.Context, query *gorm.DB, projectID uuid.UUID) ([]Table, error) {
	var models []dataTableModel
	if err := query.WithContext(ctx).Where("project_id = ?", projectID).
		Order("created_at ASC, id ASC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list data tables: %w", err)
	}
	result := make([]Table, 0, len(models))
	for _, model := range models {
		table, err := s.tableFromModel(ctx, query, model)
		if err != nil {
			return nil, err
		}
		result = append(result, table)
	}
	return result, nil
}

func (s *GORMStore) GetTable(ctx context.Context, projectID, tableID string) (Table, error) {
	projectUUID, _ := uuid.Parse(projectID)
	tableUUID, _ := uuid.Parse(tableID)
	model, err := s.loadTable(s.database.WithContext(ctx), projectUUID, tableUUID)
	if err != nil {
		return Table{}, err
	}
	return s.tableFromModel(ctx, s.database.WithContext(ctx), model)
}

func (s *GORMStore) loadTable(query *gorm.DB, projectID, tableID uuid.UUID) (dataTableModel, error) {
	var model dataTableModel
	if err := query.Where("project_id = ? AND id = ?", projectID, tableID).Take(&model).Error; err != nil {
		return dataTableModel{}, mapStorageError(err, "Table")
	}
	return model, nil
}

func (s *GORMStore) tableFromModel(ctx context.Context, query *gorm.DB, model dataTableModel) (Table, error) {
	var columns []dataColumnModel
	if err := query.WithContext(ctx).Where("table_id = ?", model.ID).
		Order("position ASC, id ASC").Find(&columns).Error; err != nil {
		return Table{}, fmt.Errorf("list data table columns: %w", err)
	}
	var count int64
	if err := query.WithContext(ctx).Model(&dataRecordModel{}).Where("table_id = ?", model.ID).Count(&count).Error; err != nil {
		return Table{}, fmt.Errorf("count data table records: %w", err)
	}
	result := Table{
		ID: model.ID.String(), Name: model.Name, RecordCount: count,
		CreatedAt: model.CreatedAt.UTC(), UpdatedAt: model.UpdatedAt.UTC(),
		Columns: make([]Column, 0, len(columns)),
	}
	for _, column := range columns {
		result.Columns = append(result.Columns, columnFromModel(column))
	}
	return result, nil
}

func columnFromModel(model dataColumnModel) Column {
	return Column{
		ID: model.ID.String(), Name: model.Name, Type: ColumnType(model.DataType),
		Required: model.Required, DefaultValue: cloneRaw(model.DefaultValue),
		CreatedAt: model.CreatedAt.UTC(),
	}
}

func (s *GORMStore) CreateTable(ctx context.Context, projectID string, mutation MutationContext, input TableInput) (Table, error) {
	projectUUID, _ := uuid.Parse(projectID)
	now := s.now().UTC()
	model := dataTableModel{ID: uuid.New(), ProjectID: projectUUID, Name: input.Name, CreatedAt: now, UpdatedAt: now}
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		var count int64
		if err := transaction.Model(&dataTableModel{}).Where("project_id = ?", projectUUID).Count(&count).Error; err != nil {
			return err
		}
		if count >= MaxTablesPerProject {
			return Conflict(fmt.Sprintf("A project may contain at most %d tables", MaxTablesPerProject))
		}
		if err := transaction.Create(&model).Error; err != nil {
			return mapStorageError(err, "Table")
		}
		for index, inputColumn := range input.Columns {
			column := dataColumnModel{
				ID: uuid.New(), TableID: model.ID, Name: inputColumn.Name,
				DataType: string(inputColumn.Type), Required: inputColumn.Required,
				DefaultValue: cloneRaw(inputColumn.DefaultValue), Position: index, CreatedAt: now,
			}
			if err := transaction.Create(&column).Error; err != nil {
				return mapStorageError(err, "Column")
			}
		}
		return s.recordMutation(transaction, state, mutation, "create", "table", model.ID.String(), map[string]any{"name": model.Name}, true)
	})
	if err != nil {
		return Table{}, err
	}
	return s.GetTable(ctx, projectID, model.ID.String())
}

func (s *GORMStore) RenameTable(ctx context.Context, projectID, tableID string, mutation MutationContext, name string) (Table, error) {
	projectUUID, _ := uuid.Parse(projectID)
	tableUUID, _ := uuid.Parse(tableID)
	now := s.now().UTC()
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		model, err := s.loadTable(transaction, projectUUID, tableUUID)
		if err != nil {
			return err
		}
		if err := transaction.Model(&dataTableModel{}).Where("id = ?", tableUUID).
			Updates(map[string]any{"name": name, "updated_at": now}).Error; err != nil {
			return mapStorageError(err, "Table")
		}
		return s.recordMutation(transaction, state, mutation, "rename", "table", model.ID.String(), map[string]any{"name": name}, true)
	})
	if err != nil {
		return Table{}, err
	}
	return s.GetTable(ctx, projectID, tableID)
}

func (s *GORMStore) DeleteTable(ctx context.Context, projectID, tableID string, mutation MutationContext) error {
	projectUUID, _ := uuid.Parse(projectID)
	tableUUID, _ := uuid.Parse(tableID)
	return s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		model, err := s.loadTable(transaction, projectUUID, tableUUID)
		if err != nil {
			return err
		}
		var records int64
		if err := transaction.Model(&dataRecordModel{}).Where("table_id = ?", tableUUID).Count(&records).Error; err != nil {
			return err
		}
		if err := transaction.Delete(&model).Error; err != nil {
			return fmt.Errorf("delete data table: %w", err)
		}
		return s.recordMutation(transaction, state, mutation, "delete", "table", model.ID.String(), map[string]any{
			"name": model.Name, "deletedRecords": records,
		}, true)
	})
}

func (s *GORMStore) ListRecords(ctx context.Context, projectID, tableID string, limit, offset int) (RecordPage, error) {
	projectUUID, _ := uuid.Parse(projectID)
	tableUUID, _ := uuid.Parse(tableID)
	if _, err := s.loadTable(s.database.WithContext(ctx), projectUUID, tableUUID); err != nil {
		return RecordPage{}, err
	}
	var total int64
	if err := s.database.WithContext(ctx).Model(&dataRecordModel{}).Where("table_id = ?", tableUUID).Count(&total).Error; err != nil {
		return RecordPage{}, fmt.Errorf("count data records: %w", err)
	}
	var models []dataRecordModel
	if err := s.database.WithContext(ctx).Where("project_id = ? AND table_id = ?", projectUUID, tableUUID).
		Order("created_at ASC, id ASC").Limit(limit).Offset(offset).Find(&models).Error; err != nil {
		return RecordPage{}, fmt.Errorf("list data records: %w", err)
	}
	items := make([]Record, 0, len(models))
	for _, model := range models {
		record, err := recordFromModel(model)
		if err != nil {
			return RecordPage{}, err
		}
		items = append(items, record)
	}
	return RecordPage{Records: items, Total: total, Limit: limit, Offset: offset}, nil
}

func (s *GORMStore) GetRecord(ctx context.Context, projectID, tableID, recordID string) (Record, error) {
	projectUUID, _ := uuid.Parse(projectID)
	tableUUID, _ := uuid.Parse(tableID)
	recordUUID, _ := uuid.Parse(recordID)
	if _, err := s.loadTable(s.database.WithContext(ctx), projectUUID, tableUUID); err != nil {
		return Record{}, err
	}
	model, err := s.loadRecord(s.database.WithContext(ctx), projectUUID, tableUUID, recordUUID)
	if err != nil {
		return Record{}, err
	}
	return recordFromModel(model)
}

func (s *GORMStore) loadRecord(query *gorm.DB, projectID, tableID, recordID uuid.UUID) (dataRecordModel, error) {
	var model dataRecordModel
	if err := query.Where("project_id = ? AND table_id = ? AND id = ?", projectID, tableID, recordID).Take(&model).Error; err != nil {
		return dataRecordModel{}, mapStorageError(err, "Record")
	}
	return model, nil
}

func recordFromModel(model dataRecordModel) (Record, error) {
	values := map[string]json.RawMessage{}
	if err := json.Unmarshal(model.Values, &values); err != nil {
		return Record{}, fmt.Errorf("decode stored data record: %w", err)
	}
	return Record{
		ID: model.ID.String(), Values: values,
		CreatedAt: model.CreatedAt.UTC(), UpdatedAt: model.UpdatedAt.UTC(),
	}, nil
}

func (s *GORMStore) CreateRecord(ctx context.Context, projectID, tableID string, mutation MutationContext, input RecordInput) (Record, error) {
	projectUUID, _ := uuid.Parse(projectID)
	tableUUID, _ := uuid.Parse(tableID)
	now := s.now().UTC()
	model := dataRecordModel{ID: uuid.New(), ProjectID: projectUUID, TableID: tableUUID, CreatedAt: now, UpdatedAt: now}
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		table, err := s.loadTable(transaction, projectUUID, tableUUID)
		if err != nil {
			return err
		}
		var count int64
		if err := transaction.Model(&dataRecordModel{}).Where("table_id = ?", tableUUID).Count(&count).Error; err != nil {
			return err
		}
		if count >= MaxRecordsPerTable {
			return Conflict(fmt.Sprintf("A table may contain at most %d records", MaxRecordsPerTable))
		}
		values, err := s.normalizeRecordValues(transaction, tableUUID, input.Values, nil, false)
		if err != nil {
			return err
		}
		model.Values, err = json.Marshal(values)
		if err != nil {
			return err
		}
		if err := transaction.Create(&model).Error; err != nil {
			return fmt.Errorf("create data record: %w", err)
		}
		if err := transaction.Model(&dataTableModel{}).Where("id = ?", table.ID).Update("updated_at", now).Error; err != nil {
			return err
		}
		return s.recordMutation(transaction, state, mutation, "create", "record", model.ID.String(), map[string]any{"tableId": tableID}, true)
	})
	if err != nil {
		return Record{}, err
	}
	return recordFromModel(model)
}

func (s *GORMStore) UpdateRecord(ctx context.Context, projectID, tableID, recordID string, mutation MutationContext, input RecordInput) (Record, error) {
	projectUUID, _ := uuid.Parse(projectID)
	tableUUID, _ := uuid.Parse(tableID)
	recordUUID, _ := uuid.Parse(recordID)
	var updated dataRecordModel
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		if _, err := s.loadTable(transaction, projectUUID, tableUUID); err != nil {
			return err
		}
		existing, err := s.loadRecord(transaction, projectUUID, tableUUID, recordUUID)
		if err != nil {
			return err
		}
		current := map[string]json.RawMessage{}
		if err := json.Unmarshal(existing.Values, &current); err != nil {
			return fmt.Errorf("decode stored data record: %w", err)
		}
		values, err := s.normalizeRecordValues(transaction, tableUUID, input.Values, current, true)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if err := transaction.Model(&dataRecordModel{}).Where("id = ?", recordUUID).
			Updates(map[string]any{"values": encoded, "updated_at": now}).Error; err != nil {
			return fmt.Errorf("update data record: %w", err)
		}
		if err := transaction.Model(&dataTableModel{}).Where("id = ?", tableUUID).Update("updated_at", now).Error; err != nil {
			return err
		}
		updated = existing
		updated.Values = encoded
		updated.UpdatedAt = now
		return s.recordMutation(transaction, state, mutation, "update", "record", recordID, map[string]any{"tableId": tableID}, true)
	})
	if err != nil {
		return Record{}, err
	}
	return recordFromModel(updated)
}

func (s *GORMStore) DeleteRecord(ctx context.Context, projectID, tableID, recordID string, mutation MutationContext) error {
	projectUUID, _ := uuid.Parse(projectID)
	tableUUID, _ := uuid.Parse(tableID)
	recordUUID, _ := uuid.Parse(recordID)
	return s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		if _, err := s.loadTable(transaction, projectUUID, tableUUID); err != nil {
			return err
		}
		model, err := s.loadRecord(transaction, projectUUID, tableUUID, recordUUID)
		if err != nil {
			return err
		}
		if err := transaction.Delete(&model).Error; err != nil {
			return fmt.Errorf("delete data record: %w", err)
		}
		now := s.now().UTC()
		if err := transaction.Model(&dataTableModel{}).Where("id = ?", tableUUID).Update("updated_at", now).Error; err != nil {
			return err
		}
		return s.recordMutation(transaction, state, mutation, "delete", "record", recordID, map[string]any{"tableId": tableID}, true)
	})
}

func (s *GORMStore) normalizeRecordValues(
	query *gorm.DB,
	tableID uuid.UUID,
	submitted map[string]json.RawMessage,
	existing map[string]json.RawMessage,
	update bool,
) (map[string]json.RawMessage, error) {
	var models []dataColumnModel
	if err := query.Where("table_id = ?", tableID).Order("position ASC").Find(&models).Error; err != nil {
		return nil, err
	}
	columns := make(map[string]dataColumnModel, len(models))
	for _, model := range models {
		columns[model.Name] = model
	}
	for name := range submitted {
		if _, ok := columns[name]; !ok {
			return nil, Invalid("values."+name, "values."+name+" does not match a table column")
		}
	}
	values := map[string]json.RawMessage{}
	if update {
		for name, value := range existing {
			values[name] = cloneRaw(value)
		}
	}
	for _, column := range models {
		raw, present := submitted[column.Name]
		if present {
			value, err := validateJSONValue(raw, "values."+column.Name, MaxRecordBytes)
			if err != nil {
				return nil, err
			}
			if value == nil && column.Required {
				return nil, Invalid("values."+column.Name, "values."+column.Name+" is required")
			}
			if !valueMatchesColumn(value, ColumnType(column.DataType)) {
				return nil, Invalid("values."+column.Name, "values."+column.Name+" must match column type "+column.DataType)
			}
			values[column.Name] = cloneRaw(raw)
			continue
		}
		if update {
			continue
		}
		if len(column.DefaultValue) > 0 {
			values[column.Name] = cloneRaw(column.DefaultValue)
		} else if column.Required {
			return nil, Invalid("values."+column.Name, "values."+column.Name+" is required")
		} else {
			values[column.Name] = json.RawMessage("null")
		}
	}
	return values, nil
}

func cloneRaw(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}

func cloneRecordValues(source map[string]json.RawMessage) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(source))
	for key, value := range source {
		result[key] = cloneRaw(value)
	}
	return result
}

func touchTable(query *gorm.DB, tableID uuid.UUID, now time.Time) error {
	result := query.Model(&dataTableModel{}).Where("id = ?", tableID).Update("updated_at", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return NotFound("Table")
	}
	return nil
}
