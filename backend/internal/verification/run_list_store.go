package verification

import (
	"context"
	"errors"
)

type sessionRunIDRow struct {
	ID string `gorm:"column:id"`
}

func (store *PostgresStore) ListRunViewsForSession(
	ctx context.Context,
	projectID, sessionID string,
	limit int,
) ([]RunView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validUUIDs(projectID, sessionID) || limit < 1 || limit > 100 {
		return nil, runInvalid("Run history project, SandboxSession, or limit")
	}

	var rows []sessionRunIDRow
	err := store.database.WithContext(ctx).
		Table("candidate_verification_runs AS runs").
		Select("runs.id").
		Joins("JOIN candidate_verification_plans AS plans ON plans.id = runs.plan_id AND plans.plan_hash = runs.plan_hash").
		Where("runs.project_id = ? AND plans.project_id = ? AND plans.sandbox_session_id = ?", projectID, projectID, sessionID).
		Order("runs.created_at DESC, runs.id DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, mapRunStoreError("list SandboxSession Runs", err)
	}

	views := make([]RunView, 0, len(rows))
	for _, row := range rows {
		if !validUUIDs(row.ID) {
			return nil, runIntegrity("SandboxSession Run list identity", errors.New("invalid Run ID"))
		}
		view, err := store.GetRunView(ctx, projectID, row.ID)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}
