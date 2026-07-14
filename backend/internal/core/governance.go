package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GovernanceMode string

const (
	GovernanceModeSolo GovernanceMode = "solo"
	GovernanceModeTeam GovernanceMode = "team"
)

type ProjectGovernance struct {
	Mode        GovernanceMode `json:"mode"`
	OwnerCount  int64          `json:"ownerCount"`
	SoleOwnerID string         `json:"soleOwnerId,omitempty"`
}

func ValidGovernanceMode(mode GovernanceMode) bool {
	return mode == GovernanceModeSolo || mode == GovernanceModeTeam
}

func normalizeGovernanceMode(value string) GovernanceMode {
	mode := GovernanceMode(strings.TrimSpace(value))
	if mode == "" {
		// Models created by old fixtures and rows predating the migration retain
		// the strict team semantics rather than silently enabling self-review.
		return GovernanceModeTeam
	}
	return mode
}

func LoadProjectGovernance(ctx context.Context, database *gorm.DB, projectID uuid.UUID) (ProjectGovernance, error) {
	return loadProjectGovernance(ctx, database, projectID, false)
}

func LockProjectGovernance(ctx context.Context, database *gorm.DB, projectID uuid.UUID) (ProjectGovernance, error) {
	return loadProjectGovernance(ctx, database, projectID, true)
}

func loadProjectGovernance(ctx context.Context, database *gorm.DB, projectID uuid.UUID, lock bool) (ProjectGovernance, error) {
	if database == nil {
		return ProjectGovernance{}, errors.New("project governance database is required")
	}
	var project storage.ProjectModel
	query := database.WithContext(ctx).Select("id", "governance_mode").Where("id = ?", projectID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Take(&project).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ProjectGovernance{}, ErrNotFound
		}
		return ProjectGovernance{}, fmt.Errorf("load project governance: %w", err)
	}
	mode := normalizeGovernanceMode(project.GovernanceMode)
	if !ValidGovernanceMode(mode) {
		return ProjectGovernance{}, fmt.Errorf("stored project governance mode is invalid")
	}
	var ownerCount int64
	if err := database.WithContext(ctx).Model(&storage.ProjectMemberModel{}).
		Where("project_id = ? AND role = ?", projectID, RoleOwner).Count(&ownerCount).Error; err != nil {
		return ProjectGovernance{}, fmt.Errorf("count project owners: %w", err)
	}
	governance := ProjectGovernance{Mode: mode, OwnerCount: ownerCount}
	if ownerCount == 1 {
		var owner storage.ProjectMemberModel
		if err := database.WithContext(ctx).Select("user_id").
			Where("project_id = ? AND role = ?", projectID, RoleOwner).Take(&owner).Error; err != nil {
			return ProjectGovernance{}, fmt.Errorf("load project owner: %w", err)
		}
		governance.SoleOwnerID = owner.UserID.String()
	}
	return governance, nil
}

func RequireSoloSelfReview(governance ProjectGovernance, role Role, confirmed bool, explanation string) error {
	if governance.Mode != GovernanceModeSolo || governance.OwnerCount != 1 || role != RoleOwner {
		return ErrSelfApproval
	}
	if !confirmed || strings.TrimSpace(explanation) == "" {
		return ErrSoloReviewConfirmation
	}
	return nil
}
