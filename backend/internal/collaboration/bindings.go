package collaboration

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) GetMemberBindings(ctx context.Context, artifactID, actorID string) (DocumentMemberBindingSet, error) {
	artifactUUID, err := uuid.Parse(strings.TrimSpace(artifactID))
	if err != nil {
		return DocumentMemberBindingSet{}, fmt.Errorf("%w: artifact id", core.ErrInvalidInput)
	}
	var artifact storage.ArtifactModel
	if err := s.database.WithContext(ctx).Where("id = ?", artifactUUID).Take(&artifact).Error; err != nil {
		return DocumentMemberBindingSet{}, mapCollaborationNotFound(err)
	}
	if _, err := s.access.Authorize(ctx, artifact.ProjectID.String(), actorID, core.ActionView); err != nil {
		return DocumentMemberBindingSet{}, err
	}
	return s.loadMemberBindings(ctx, artifact, nil)
}

func (s *Service) ReplaceMemberBindings(
	ctx context.Context,
	artifactID, actorID, expectedETag string,
	inputs []DocumentMemberBindingInput,
) (DocumentMemberBindingSet, error) {
	artifactUUID, err := uuid.Parse(strings.TrimSpace(artifactID))
	if err != nil {
		return DocumentMemberBindingSet{}, fmt.Errorf("%w: artifact id", core.ErrInvalidInput)
	}
	actorUUID, err := uuid.Parse(strings.TrimSpace(actorID))
	if err != nil {
		return DocumentMemberBindingSet{}, fmt.Errorf("%w: actor id", core.ErrInvalidInput)
	}
	inputs, userIDs, err := normalizeMemberBindingInputs(inputs)
	if err != nil {
		return DocumentMemberBindingSet{}, err
	}
	var artifact storage.ArtifactModel
	var state storage.ArtifactCollaborationStateModel
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", artifactUUID).
			Take(&artifact).Error; err != nil {
			return mapCollaborationNotFound(err)
		}
		if _, err := s.access.Authorize(ctx, artifact.ProjectID.String(), actorID, core.ActionEdit); err != nil {
			return err
		}
		var current storage.ArtifactCollaborationStateModel
		stateErr := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("artifact_id = ?", artifact.ID).
			Take(&current).Error
		version := uint64(0)
		if stateErr == nil {
			if current.ProjectID != artifact.ProjectID {
				return core.ErrConflict
			}
			version = current.Version
		} else if !errors.Is(stateErr, gorm.ErrRecordNotFound) {
			return stateErr
		}
		if strings.TrimSpace(expectedETag) == "" || expectedETag != bindingSetETag(artifact.ID.String(), version) {
			return core.ErrConflict
		}
		var memberCount int64
		if err := transaction.Model(&storage.ProjectMemberModel{}).
			Where("project_id = ? AND user_id IN ?", artifact.ProjectID, userIDs).
			Count(&memberCount).Error; err != nil {
			return err
		}
		if memberCount != int64(len(userIDs)) {
			return fmt.Errorf("%w: every bound user must be a project member", core.ErrInvalidInput)
		}
		if err := transaction.Where("artifact_id = ?", artifact.ID).
			Delete(&storage.ArtifactMemberBindingModel{}).Error; err != nil {
			return err
		}
		now := s.now().UTC()
		models := make([]storage.ArtifactMemberBindingModel, 0, len(inputs))
		for _, input := range inputs {
			models = append(models, storage.ArtifactMemberBindingModel{
				ArtifactID: artifact.ID, ProjectID: artifact.ProjectID, UserID: uuid.MustParse(input.UserID),
				Role: databaseDocumentMemberRole(input.Role), Reason: input.Reason,
				AssignedBy: actorUUID, AssignedAt: now,
			})
		}
		if err := transaction.Create(&models).Error; err != nil {
			return err
		}
		state = storage.ArtifactCollaborationStateModel{
			ArtifactID: artifact.ID, ProjectID: artifact.ProjectID, Version: version + 1,
			UpdatedBy: actorUUID, UpdatedAt: now,
		}
		if version == 0 {
			if err := transaction.Create(&state).Error; err != nil {
				return err
			}
		} else {
			update := transaction.Model(&storage.ArtifactCollaborationStateModel{}).
				Where("artifact_id = ? AND version = ?", artifact.ID, version).
				Updates(map[string]any{"version": version + 1, "updated_by": actorUUID, "updated_at": now})
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return core.ErrConflict
			}
		}
		if err := collaborationAudit(transaction, artifact.ProjectID, actorUUID, "artifact.member_bindings_replaced", "artifact", artifact.ID.String(), map[string]any{
			"bindingCount": len(inputs), "version": state.Version,
		}); err != nil {
			return err
		}
		return collaborationOutbox(transaction, "artifact", artifact.ID.String(), "artifact.member_bindings_replaced", "worksflow.artifact.member_bindings.replaced", map[string]any{
			"projectId": artifact.ProjectID.String(), "artifactId": artifact.ID.String(), "version": state.Version,
		})
	})
	if err != nil {
		return DocumentMemberBindingSet{}, err
	}
	return s.loadMemberBindings(ctx, artifact, &state)
}

func normalizeMemberBindingInputs(inputs []DocumentMemberBindingInput) ([]DocumentMemberBindingInput, []uuid.UUID, error) {
	if len(inputs) == 0 || len(inputs) > 100 {
		return nil, nil, fmt.Errorf("%w: document member bindings", core.ErrInvalidInput)
	}
	normalized := make([]DocumentMemberBindingInput, 0, len(inputs))
	userSet := make(map[uuid.UUID]struct{})
	pairSet := make(map[string]struct{})
	hasOwner := false
	for _, input := range inputs {
		userID, err := uuid.Parse(strings.TrimSpace(input.UserID))
		if err != nil || !validDocumentMemberRole(input.Role) {
			return nil, nil, fmt.Errorf("%w: document member binding", core.ErrInvalidInput)
		}
		input.UserID = userID.String()
		input.Reason = strings.TrimSpace(input.Reason)
		if len(input.Reason) > 1000 {
			return nil, nil, fmt.Errorf("%w: document member binding reason", core.ErrInvalidInput)
		}
		key := input.UserID + "\x00" + string(input.Role)
		if _, duplicate := pairSet[key]; duplicate {
			return nil, nil, fmt.Errorf("%w: duplicate document member binding", core.ErrInvalidInput)
		}
		pairSet[key] = struct{}{}
		userSet[userID] = struct{}{}
		hasOwner = hasOwner || input.Role == DocumentOwner
		normalized = append(normalized, input)
	}
	if !hasOwner {
		return nil, nil, fmt.Errorf("%w: document requires an owner", core.ErrInvalidInput)
	}
	userIDs := make([]uuid.UUID, 0, len(userSet))
	for userID := range userSet {
		userIDs = append(userIDs, userID)
	}
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i].String() < userIDs[j].String() })
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Role == normalized[j].Role {
			return normalized[i].UserID < normalized[j].UserID
		}
		return normalized[i].Role < normalized[j].Role
	})
	return normalized, userIDs, nil
}

func (s *Service) loadMemberBindings(
	ctx context.Context,
	artifact storage.ArtifactModel,
	knownState *storage.ArtifactCollaborationStateModel,
) (DocumentMemberBindingSet, error) {
	state := storage.ArtifactCollaborationStateModel{ArtifactID: artifact.ID, ProjectID: artifact.ProjectID}
	if knownState != nil {
		state = *knownState
	} else {
		err := s.database.WithContext(ctx).Where("artifact_id = ?", artifact.ID).Take(&state).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return DocumentMemberBindingSet{}, err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			state = storage.ArtifactCollaborationStateModel{ArtifactID: artifact.ID, ProjectID: artifact.ProjectID}
		}
	}
	var models []storage.ArtifactMemberBindingModel
	if err := s.database.WithContext(ctx).Where("artifact_id = ?", artifact.ID).
		Order("role ASC, user_id ASC").Find(&models).Error; err != nil {
		return DocumentMemberBindingSet{}, err
	}
	items := make([]DocumentMemberBinding, 0, len(models))
	for _, model := range models {
		items = append(items, DocumentMemberBinding{
			UserID: model.UserID.String(), Role: wireDocumentMemberRole(model.Role),
			Reason: model.Reason, AssignedBy: model.AssignedBy.String(), AssignedAt: model.AssignedAt,
		})
	}
	if len(items) == 0 && state.Version == 0 {
		items = append(items, DocumentMemberBinding{
			UserID: artifact.CreatedBy.String(), Role: DocumentOwner,
			Reason: "Default artifact owner", AssignedBy: artifact.CreatedBy.String(), AssignedAt: artifact.CreatedAt,
		})
	}
	result := DocumentMemberBindingSet{
		ArtifactID: artifact.ID.String(), ProjectID: artifact.ProjectID.String(), Version: state.Version,
		ETag: bindingSetETag(artifact.ID.String(), state.Version), Items: items,
	}
	if !state.UpdatedAt.IsZero() {
		updatedAt := state.UpdatedAt
		result.UpdatedAt = &updatedAt
	}
	return result, nil
}

func mapCollaborationNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return core.ErrNotFound
	}
	return err
}
