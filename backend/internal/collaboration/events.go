package collaboration

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

func collaborationAudit(
	transaction *gorm.DB,
	projectID, actorID uuid.UUID,
	action, targetType, targetID string,
	metadata any,
) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return transaction.Create(&storage.AuditEventModel{
		ID: uuid.New(), ProjectID: &projectID, ActorID: &actorID,
		Action: action, TargetType: targetType, TargetID: targetID,
		Metadata: encoded, CreatedAt: time.Now().UTC(),
	}).Error
}

func collaborationOutbox(
	transaction *gorm.DB,
	aggregateType, aggregateID, eventType, subject string,
	payload any,
) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return transaction.Create(&storage.OutboxEventModel{
		ID: uuid.New(), AggregateType: aggregateType, AggregateID: aggregateID,
		EventType: eventType, Subject: subject, Payload: encoded, Headers: json.RawMessage(`{}`),
		AvailableAt: now, CreatedAt: now,
	}).Error
}
