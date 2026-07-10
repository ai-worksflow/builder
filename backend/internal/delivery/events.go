package delivery

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

func recordAuditAndOutbox(
	ctx context.Context,
	transaction *gorm.DB,
	projectID, actorID uuid.UUID,
	action, targetType, targetID, eventType, subject string,
	metadata map[string]any,
) error {
	payload, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	requestID := core.RequestIDFromContext(ctx)
	var requestIDPointer *string
	if requestID != "" {
		requestIDPointer = &requestID
	}
	now := time.Now().UTC()
	if err := transaction.Create(&storage.AuditEventModel{
		ID: uuid.New(), ProjectID: &projectID, ActorID: &actorID, RequestID: requestIDPointer,
		Action: action, TargetType: targetType, TargetID: targetID, Metadata: payload, CreatedAt: now,
	}).Error; err != nil {
		return err
	}
	headers := map[string]string{}
	if requestID != "" {
		headers["requestId"] = requestID
	}
	headerPayload, err := json.Marshal(headers)
	if err != nil {
		return err
	}
	return transaction.Create(&storage.OutboxEventModel{
		ID: uuid.New(), AggregateType: targetType, AggregateID: targetID,
		EventType: eventType, Subject: subject, Payload: payload, Headers: headerPayload,
		Attempts: 0, AvailableAt: now, CreatedAt: now,
	}).Error
}
