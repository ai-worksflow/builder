package conversation

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type ProjectInitializer struct{}

func DefaultProjectConversationID(projectID uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(projectID, []byte("worksflow:conversation:project-discovery"))
}

func (ProjectInitializer) InitializeProject(ctx context.Context, transaction *gorm.DB, projectID, actorID uuid.UUID, now time.Time) error {
	if transaction == nil || projectID == uuid.Nil || actorID == uuid.Nil {
		return errors.New("conversation project initializer requires transaction, project and actor")
	}
	conversationID := DefaultProjectConversationID(projectID)
	model := storage.ConversationModel{
		ID: conversationID, ProjectID: projectID, Title: "Project discovery",
		Status: string(ConversationActive), Version: 1, CreatedBy: actorID,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	database := transaction.WithContext(ctx)
	if err := database.Create(&model).Error; err != nil {
		return err
	}
	if err := conversationAudit(database, projectID, actorID, "conversation.created", "conversation", conversationID.String(), map[string]any{
		"origin": "project_initializer",
	}); err != nil {
		return err
	}
	return conversationOutbox(database, "conversation", conversationID.String(), "conversation.created", map[string]any{
		"projectId": projectID.String(), "conversationId": conversationID.String(), "actorId": actorID.String(), "origin": "project_initializer",
	})
}
