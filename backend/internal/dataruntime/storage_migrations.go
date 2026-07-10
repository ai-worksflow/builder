package dataruntime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type migrationPlan struct {
	Operations []plannedMigrationOperation `json:"operations"`
}

type plannedMigrationOperation struct {
	Type     MigrationOperationType `json:"type"`
	TableID  string                 `json:"tableId,omitempty"`
	ColumnID string                 `json:"columnId,omitempty"`
	Name     string                 `json:"name,omitempty"`
	Table    *Table                 `json:"table,omitempty"`
	Column   *Column                `json:"column,omitempty"`
}

func newConfirmationToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create migration confirmation token: %w", err)
	}
	return "confirm_" + base64.RawURLEncoding.EncodeToString(value), nil
}

func (s *GORMStore) PreviewMigration(ctx context.Context, projectID string, mutation MutationContext, operations []MigrationOperation) (MigrationPreview, error) {
	projectUUID, _ := uuid.Parse(projectID)
	token, err := s.tokenSource()
	if err != nil {
		return MigrationPreview{}, err
	}
	if err := ValidateConfirmationToken(token); err != nil {
		return MigrationPreview{}, fmt.Errorf("confirmation token source returned an invalid token: %w", err)
	}
	now := s.now().UTC()
	expiresAt := now.Add(s.confirmationTTL)
	previewID := uuid.New()
	var changes []MigrationChange
	var resultingTables []Table
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		current, err := s.listTables(ctx, transaction, projectUUID)
		if err != nil {
			return err
		}
		plan, result, compiledChanges, err := compileMigrationPlan(current, operations, now)
		if err != nil {
			return err
		}
		changes = compiledChanges
		resultingTables = result
		planJSON, _ := json.Marshal(plan)
		changesJSON, _ := json.Marshal(changes)
		resultJSON, _ := json.Marshal(resultingTables)
		actorID, _ := uuid.Parse(mutation.ActorID)

		var pending []dataMigrationPreviewModel
		if err := transaction.Where("project_id = ? AND consumed_at IS NULL", projectUUID).
			Order("created_at ASC").Find(&pending).Error; err != nil {
			return err
		}
		if len(pending) >= MaxPendingMigrationsPerProject {
			// Bounded storage: evict only the oldest pending preview. Confirmation
			// tokens are deliberately single-purpose and clients can re-preview.
			if err := transaction.Delete(&pending[0]).Error; err != nil {
				return fmt.Errorf("evict old migration preview: %w", err)
			}
		}
		model := dataMigrationPreviewModel{
			ID: previewID, ProjectID: projectUUID, TokenHash: tokenDigest(token),
			BaseRevision: state.Revision, Plan: planJSON, Changes: changesJSON,
			ResultTables: resultJSON, CreatedBy: actorID, CreatedAt: now, ExpiresAt: expiresAt,
		}
		if err := transaction.Create(&model).Error; err != nil {
			return fmt.Errorf("save migration preview: %w", err)
		}
		return s.recordMutation(transaction, state, mutation, "preview", "migration", previewID.String(), map[string]any{
			"changeCount": len(changes), "expiresAt": expiresAt,
		}, false)
	})
	if err != nil {
		return MigrationPreview{}, err
	}
	return MigrationPreview{
		ID: previewID.String(), ProjectID: projectID, ConfirmationToken: token,
		ExpiresAt: expiresAt, Changes: changes, ResultingTables: resultingTables,
	}, nil
}

func compileMigrationPlan(current []Table, operations []MigrationOperation, now time.Time) (migrationPlan, []Table, []MigrationChange, error) {
	tables := cloneTablesForPlan(current)
	byID := make(map[string]*Table, len(tables))
	for index := range tables {
		byID[tables[index].ID] = &tables[index]
	}
	plan := migrationPlan{Operations: make([]plannedMigrationOperation, 0, len(operations))}
	changes := make([]MigrationChange, 0, len(operations))

	tableByName := func(name, exceptID string) bool {
		for _, table := range tables {
			if table.ID != exceptID && table.Name == name {
				return true
			}
		}
		return false
	}
	removeTable := func(id string) {
		for index := range tables {
			if tables[index].ID == id {
				tables = append(tables[:index], tables[index+1:]...)
				return
			}
		}
	}

	for _, operation := range operations {
		switch operation.Type {
		case MigrationCreateTable:
			if len(tables) >= MaxTablesPerProject {
				return migrationPlan{}, nil, nil, Conflict(fmt.Sprintf("A project may contain at most %d tables", MaxTablesPerProject))
			}
			if tableByName(operation.Table.Name, "") {
				return migrationPlan{}, nil, nil, Conflict("Table " + operation.Table.Name + " already exists")
			}
			table := Table{
				ID: uuid.NewString(), Name: operation.Table.Name, Columns: make([]Column, 0, len(operation.Table.Columns)),
				RecordCount: 0, CreatedAt: now, UpdatedAt: now,
			}
			for _, input := range operation.Table.Columns {
				table.Columns = append(table.Columns, Column{
					ID: uuid.NewString(), Name: input.Name, Type: input.Type, Required: input.Required,
					DefaultValue: cloneRaw(input.DefaultValue), CreatedAt: now,
				})
			}
			tables = append(tables, table)
			byID[table.ID] = &tables[len(tables)-1]
			copy := cloneTableForPlan(table)
			plan.Operations = append(plan.Operations, plannedMigrationOperation{Type: operation.Type, Table: &copy})
			changes = append(changes, MigrationChange{Operation: operation.Type, Summary: "Create table " + table.Name})
		case MigrationRenameTable:
			table := byID[operation.TableID]
			if table == nil {
				return migrationPlan{}, nil, nil, NotFound("Table")
			}
			if tableByName(operation.Name, table.ID) {
				return migrationPlan{}, nil, nil, Conflict("Table " + operation.Name + " already exists")
			}
			previous := table.Name
			table.Name = operation.Name
			table.UpdatedAt = now
			plan.Operations = append(plan.Operations, plannedMigrationOperation{Type: operation.Type, TableID: table.ID, Name: operation.Name})
			changes = append(changes, MigrationChange{Operation: operation.Type, Summary: fmt.Sprintf("Rename table %s to %s", previous, operation.Name)})
		case MigrationDropTable:
			table := byID[operation.TableID]
			if table == nil {
				return migrationPlan{}, nil, nil, NotFound("Table")
			}
			name, count := table.Name, table.RecordCount
			delete(byID, table.ID)
			removeTable(table.ID)
			plan.Operations = append(plan.Operations, plannedMigrationOperation{Type: operation.Type, TableID: operation.TableID})
			changes = append(changes, MigrationChange{Operation: operation.Type, Summary: fmt.Sprintf("Drop table %s and %d record(s)", name, count), Destructive: true})
		case MigrationAddColumn:
			table := byID[operation.TableID]
			if table == nil {
				return migrationPlan{}, nil, nil, NotFound("Table")
			}
			if len(table.Columns) >= MaxColumnsPerTable {
				return migrationPlan{}, nil, nil, Conflict(fmt.Sprintf("A table may contain at most %d columns", MaxColumnsPerTable))
			}
			for _, column := range table.Columns {
				if column.Name == operation.Column.Name {
					return migrationPlan{}, nil, nil, Conflict("Column " + operation.Column.Name + " already exists")
				}
			}
			if table.RecordCount > 0 && operation.Column.Required && len(operation.Column.DefaultValue) == 0 {
				return migrationPlan{}, nil, nil, Conflict("Required column " + operation.Column.Name + " needs a default value for existing records")
			}
			column := Column{
				ID: uuid.NewString(), Name: operation.Column.Name, Type: operation.Column.Type,
				Required: operation.Column.Required, DefaultValue: cloneRaw(operation.Column.DefaultValue), CreatedAt: now,
			}
			table.Columns = append(table.Columns, column)
			table.UpdatedAt = now
			copy := column
			plan.Operations = append(plan.Operations, plannedMigrationOperation{Type: operation.Type, TableID: table.ID, Column: &copy})
			changes = append(changes, MigrationChange{Operation: operation.Type, Summary: fmt.Sprintf("Add %s to %s", column.Name, table.Name)})
		case MigrationRenameColumn, MigrationDropColumn:
			table := byID[operation.TableID]
			if table == nil {
				return migrationPlan{}, nil, nil, NotFound("Table")
			}
			columnIndex := -1
			for index := range table.Columns {
				if table.Columns[index].ID == operation.ColumnID {
					columnIndex = index
					break
				}
			}
			if columnIndex < 0 {
				return migrationPlan{}, nil, nil, NotFound("Column")
			}
			column := table.Columns[columnIndex]
			if operation.Type == MigrationRenameColumn {
				if operation.Name == column.Name {
					plan.Operations = append(plan.Operations, plannedMigrationOperation{Type: operation.Type, TableID: table.ID, ColumnID: column.ID, Name: operation.Name})
					changes = append(changes, MigrationChange{Operation: operation.Type, Summary: fmt.Sprintf("Keep %s.%s unchanged", table.Name, column.Name)})
					break
				}
				for _, candidate := range table.Columns {
					if candidate.ID != column.ID && candidate.Name == operation.Name {
						return migrationPlan{}, nil, nil, Conflict("Column " + operation.Name + " already exists")
					}
				}
				table.Columns[columnIndex].Name = operation.Name
				table.UpdatedAt = now
				plan.Operations = append(plan.Operations, plannedMigrationOperation{Type: operation.Type, TableID: table.ID, ColumnID: column.ID, Name: operation.Name})
				changes = append(changes, MigrationChange{Operation: operation.Type, Summary: fmt.Sprintf("Rename %s.%s to %s", table.Name, column.Name, operation.Name)})
			} else {
				table.Columns = append(table.Columns[:columnIndex], table.Columns[columnIndex+1:]...)
				table.UpdatedAt = now
				plan.Operations = append(plan.Operations, plannedMigrationOperation{Type: operation.Type, TableID: table.ID, ColumnID: column.ID})
				changes = append(changes, MigrationChange{Operation: operation.Type, Summary: fmt.Sprintf("Drop %s.%s", table.Name, column.Name), Destructive: true})
			}
		default:
			return migrationPlan{}, nil, nil, Invalid("operations", "migration operation is not supported")
		}
		// Slice append may move backing memory; refresh pointer map after each op.
		byID = make(map[string]*Table, len(tables))
		for index := range tables {
			byID[tables[index].ID] = &tables[index]
		}
	}
	return plan, tables, changes, nil
}

func (s *GORMStore) ApplyMigration(ctx context.Context, projectID string, mutation MutationContext, confirmationToken string) (ApplyMigrationResult, error) {
	projectUUID, _ := uuid.Parse(projectID)
	var now time.Time
	var applied AppliedMigration
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var preview dataMigrationPreviewModel
		err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND token_hash = ?", projectUUID, tokenDigest(confirmationToken)).Take(&preview).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return NewError(CodeConfirmationRequired, 409, "A valid project-scoped migration confirmation token is required")
		}
		if err != nil {
			return fmt.Errorf("load migration preview: %w", err)
		}
		now = s.now().UTC()
		if preview.ConsumedAt != nil {
			return NewError(CodeConfirmationRequired, 409, "Migration confirmation token has already been consumed")
		}
		if !preview.ExpiresAt.After(now) {
			return NewError(CodeConfirmationExpired, 409, "Migration confirmation has expired")
		}
		state, err := s.lockState(transaction, projectUUID)
		if err != nil {
			return err
		}
		if preview.BaseRevision != state.Revision {
			return Conflict("Project data changed after preview; create a new migration preview")
		}
		var plan migrationPlan
		if err := json.Unmarshal(preview.Plan, &plan); err != nil {
			return fmt.Errorf("decode migration plan: %w", err)
		}
		if err := s.applyPlan(transaction, projectUUID, plan, now); err != nil {
			return err
		}
		var changes []MigrationChange
		if err := json.Unmarshal(preview.Changes, &changes); err != nil {
			return fmt.Errorf("decode migration changes: %w", err)
		}
		actorID, _ := uuid.Parse(mutation.ActorID)
		migration := dataMigrationModel{
			ID: uuid.New(), ProjectID: projectUUID, PreviewID: preview.ID,
			Changes: cloneRaw(preview.Changes), AppliedBy: actorID, AppliedAt: now,
		}
		if err := transaction.Create(&migration).Error; err != nil {
			return fmt.Errorf("record applied migration: %w", err)
		}
		consumed := transaction.Model(&dataMigrationPreviewModel{}).Where("id = ? AND consumed_at IS NULL", preview.ID).
			Update("consumed_at", now)
		if consumed.Error != nil {
			return fmt.Errorf("consume migration preview: %w", consumed.Error)
		}
		if consumed.RowsAffected != 1 {
			return NewError(CodeConfirmationRequired, 409, "Migration confirmation token has already been consumed")
		}
		if err := s.recordMutation(transaction, state, mutation, "apply", "migration", migration.ID.String(), map[string]any{
			"previewId": preview.ID.String(), "changeCount": len(changes),
		}, true); err != nil {
			return err
		}
		applied = AppliedMigration{ID: migration.ID.String(), PreviewID: preview.ID.String(), AppliedAt: now, Changes: changes}
		return nil
	})
	if err != nil {
		return ApplyMigrationResult{}, err
	}
	snapshot, err := s.Snapshot(ctx, projectID)
	if err != nil {
		return ApplyMigrationResult{}, err
	}
	return ApplyMigrationResult{Migration: applied, Tables: snapshot.Tables, Project: snapshot}, nil
}

func (s *GORMStore) applyPlan(transaction *gorm.DB, projectID uuid.UUID, plan migrationPlan, now time.Time) error {
	for _, operation := range plan.Operations {
		switch operation.Type {
		case MigrationCreateTable:
			if operation.Table == nil {
				return errors.New("migration plan create-table is missing table")
			}
			tableID, err := uuid.Parse(operation.Table.ID)
			if err != nil {
				return errors.New("migration plan contains invalid table id")
			}
			model := dataTableModel{ID: tableID, ProjectID: projectID, Name: operation.Table.Name, CreatedAt: operation.Table.CreatedAt, UpdatedAt: now}
			if err := transaction.Create(&model).Error; err != nil {
				return mapStorageError(err, "Table")
			}
			for index, column := range operation.Table.Columns {
				columnID, err := uuid.Parse(column.ID)
				if err != nil {
					return errors.New("migration plan contains invalid column id")
				}
				if err := transaction.Create(&dataColumnModel{
					ID: columnID, TableID: tableID, Name: column.Name, DataType: string(column.Type),
					Required: column.Required, DefaultValue: cloneRaw(column.DefaultValue), Position: index, CreatedAt: column.CreatedAt,
				}).Error; err != nil {
					return mapStorageError(err, "Column")
				}
			}
		case MigrationRenameTable:
			tableID, _ := uuid.Parse(operation.TableID)
			result := transaction.Model(&dataTableModel{}).Where("project_id = ? AND id = ?", projectID, tableID).
				Updates(map[string]any{"name": operation.Name, "updated_at": now})
			if result.Error != nil {
				return mapStorageError(result.Error, "Table")
			}
			if result.RowsAffected != 1 {
				return NotFound("Table")
			}
		case MigrationDropTable:
			tableID, _ := uuid.Parse(operation.TableID)
			result := transaction.Where("project_id = ? AND id = ?", projectID, tableID).Delete(&dataTableModel{})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return NotFound("Table")
			}
		case MigrationAddColumn:
			if operation.Column == nil {
				return errors.New("migration plan add-column is missing column")
			}
			tableID, _ := uuid.Parse(operation.TableID)
			if _, err := s.loadTable(transaction, projectID, tableID); err != nil {
				return err
			}
			columnID, _ := uuid.Parse(operation.Column.ID)
			var nextPosition int
			if err := transaction.Model(&dataColumnModel{}).
				Select("COALESCE(MAX(position), -1) + 1").Where("table_id = ?", tableID).Scan(&nextPosition).Error; err != nil {
				return err
			}
			if err := transaction.Create(&dataColumnModel{
				ID: columnID, TableID: tableID, Name: operation.Column.Name,
				DataType: string(operation.Column.Type), Required: operation.Column.Required,
				DefaultValue: cloneRaw(operation.Column.DefaultValue), Position: nextPosition, CreatedAt: operation.Column.CreatedAt,
			}).Error; err != nil {
				return mapStorageError(err, "Column")
			}
			value := json.RawMessage("null")
			if len(operation.Column.DefaultValue) > 0 {
				value = cloneRaw(operation.Column.DefaultValue)
			}
			if err := rewriteRecordValues(transaction, projectID, tableID, now, func(values map[string]json.RawMessage) {
				values[operation.Column.Name] = cloneRaw(value)
			}); err != nil {
				return err
			}
			if err := touchTable(transaction, tableID, now); err != nil {
				return err
			}
		case MigrationRenameColumn, MigrationDropColumn:
			tableID, _ := uuid.Parse(operation.TableID)
			columnID, _ := uuid.Parse(operation.ColumnID)
			if _, err := s.loadTable(transaction, projectID, tableID); err != nil {
				return err
			}
			var column dataColumnModel
			if err := transaction.Where("table_id = ? AND id = ?", tableID, columnID).Take(&column).Error; err != nil {
				return mapStorageError(err, "Column")
			}
			if operation.Type == MigrationRenameColumn {
				if operation.Name == column.Name {
					continue
				}
				if err := transaction.Model(&dataColumnModel{}).Where("id = ?", columnID).Update("name", operation.Name).Error; err != nil {
					return mapStorageError(err, "Column")
				}
				if err := rewriteRecordValues(transaction, projectID, tableID, now, func(values map[string]json.RawMessage) {
					values[operation.Name] = cloneRaw(values[column.Name])
					delete(values, column.Name)
				}); err != nil {
					return err
				}
				if err := rewritePublicPolicyColumn(transaction, projectID, tableID, column.Name, operation.Name, now); err != nil {
					return err
				}
			} else {
				if err := transaction.Delete(&column).Error; err != nil {
					return err
				}
				if err := rewriteRecordValues(transaction, projectID, tableID, now, func(values map[string]json.RawMessage) {
					delete(values, column.Name)
				}); err != nil {
					return err
				}
				if err := rewritePublicPolicyColumn(transaction, projectID, tableID, column.Name, "", now); err != nil {
					return err
				}
			}
			if err := touchTable(transaction, tableID, now); err != nil {
				return err
			}
		default:
			return errors.New("migration plan contains an unsupported operation")
		}
	}
	return nil
}

func rewriteRecordValues(transaction *gorm.DB, projectID, tableID uuid.UUID, now time.Time, mutate func(map[string]json.RawMessage)) error {
	var records []dataRecordModel
	if err := transaction.Where("project_id = ? AND table_id = ?", projectID, tableID).Find(&records).Error; err != nil {
		return fmt.Errorf("load records for migration: %w", err)
	}
	for _, record := range records {
		values := map[string]json.RawMessage{}
		if err := json.Unmarshal(record.Values, &values); err != nil {
			return fmt.Errorf("decode record for migration: %w", err)
		}
		mutate(values)
		encoded, err := json.Marshal(values)
		if err != nil {
			return err
		}
		if err := transaction.Model(&dataRecordModel{}).Where("id = ?", record.ID).
			Updates(map[string]any{"values": encoded, "updated_at": now}).Error; err != nil {
			return fmt.Errorf("rewrite record for migration: %w", err)
		}
	}
	return nil
}

func cloneTablesForPlan(source []Table) []Table {
	result := make([]Table, len(source))
	for index, table := range source {
		result[index] = cloneTableForPlan(table)
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].CreatedAt.Equal(result[right].CreatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result
}

func cloneTableForPlan(table Table) Table {
	result := table
	result.Columns = make([]Column, len(table.Columns))
	for index, column := range table.Columns {
		result.Columns[index] = column
		result.Columns[index].DefaultValue = cloneRaw(column.DefaultValue)
	}
	return result
}
