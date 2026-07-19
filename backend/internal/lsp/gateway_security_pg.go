package lsp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PostgresGatewayAuditSink struct {
	database *gorm.DB
}

func NewPostgresGatewayAuditSink(database *gorm.DB) (*PostgresGatewayAuditSink, error) {
	if database == nil {
		return nil, ErrGatewaySecurityUnavailable
	}
	return &PostgresGatewayAuditSink{database: database}, nil
}

func (sink *PostgresGatewayAuditSink) AppendGatewayAudit(
	ctx context.Context,
	event GatewayAuditEvent,
) error {
	if sink == nil || sink.database == nil || ctx == nil || validateGatewayAuditEvent(event) != nil {
		return ErrGatewaySecurityUnavailable
	}
	projectID, projectErr := uuid.Parse(event.ProjectID)
	actorID, actorErr := uuid.Parse(event.ActorID)
	if projectErr != nil || actorErr != nil || projectID.String() != event.ProjectID ||
		actorID.String() != event.ActorID {
		return ErrGatewaySecurityUnavailable
	}
	metadata, err := encodeGatewayAuditMetadata(event)
	if err != nil {
		return ErrGatewaySecurityUnavailable
	}
	var requestID *string
	if event.RequestID != "" {
		value := event.RequestID
		requestID = &value
	}
	model := storage.AuditEventModel{
		ID: gatewayAuditEventID(event), ProjectID: &projectID, ActorID: &actorID, RequestID: requestID,
		Action: "lsp.gateway." + event.Action, TargetType: "lsp_gateway_binding",
		TargetID: event.BindingID, Metadata: metadata, CreatedAt: event.OccurredAt.UTC(),
	}
	if err := sink.database.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&model).Error; err != nil {
		return errors.Join(ErrGatewaySecurityUnavailable, err)
	}
	return nil
}

// gatewayAuditEventID makes retries and competing terminal paths converge on
// one append-only fact. All request terminal actions intentionally share the
// same phase ID; whichever exact terminal fact commits first is durable and a
// late response/cancel/timeout cannot append a second one.
func gatewayAuditEventID(event GatewayAuditEvent) uuid.UUID {
	phase := event.Action
	values := []string{"sandbox-lsp-gateway-audit-event/v1", event.BindingID}
	switch event.Action {
	case GatewayAuditRequestAdmitted:
		phase = "request.admission"
		values = append(values, phase, event.RequestID)
	case GatewayAuditRequestCompleted, GatewayAuditRequestCancel, GatewayAuditRequestTimeout,
		GatewayAuditRequestStale, GatewayAuditRequestError:
		phase = "request.terminal"
		values = append(values, phase, event.RequestID)
	case GatewayAuditServerViolation:
		method, ordinal, count, code := "invalid/serverViolation", "0", "0", "invalid"
		if event.ServerViolation != nil &&
			auditableServerControlMethod(event.ServerViolation.Method) &&
			gatewayServerViolationCode(event.Code) {
			method = event.ServerViolation.Method
			ordinal = strconv.FormatUint(uint64(event.ServerViolation.Ordinal), 10)
			count = strconv.FormatUint(uint64(event.ServerViolation.Count), 10)
			code = event.Code
		}
		values = append(values, phase,
			method, ordinal, count, code,
		)
	case GatewayAuditBindingRebind:
		values = append(values, phase, event.Head.TreeHash,
			strconv.FormatUint(event.Head.SessionEpoch, 10),
			strconv.FormatUint(event.Head.Version, 10),
			strconv.FormatUint(event.Head.JournalSequence, 10),
			strconv.FormatUint(event.Head.WriterLeaseEpoch, 10),
		)
	default:
		values = append(values, phase)
	}
	digest := sha256.New()
	for _, value := range values {
		_, _ = digest.Write([]byte(strconv.Itoa(len(value))))
		_, _ = digest.Write([]byte{':'})
		_, _ = digest.Write([]byte(value))
	}
	sum := digest.Sum(nil)
	var id uuid.UUID
	copy(id[:], sum[:16])
	id[6] = id[6]&0x0f | 0x50
	id[8] = id[8]&0x3f | 0x80
	return id
}

type gatewayAuditMetadata struct {
	SchemaVersion   string                       `json:"schemaVersion"`
	Outcome         string                       `json:"outcome"`
	Code            string                       `json:"code"`
	TicketID        string                       `json:"ticketId"`
	SessionID       string                       `json:"sessionId"`
	ConnectionID    string                       `json:"connectionId"`
	BindingID       string                       `json:"bindingId"`
	Mode            TicketMode                   `json:"mode"`
	Head            SandboxHeadFence             `json:"sandboxHeadFence"`
	Document        *DocumentFence               `json:"documentFence,omitempty"`
	TemplateRelease ExactTemplateRelease         `json:"templateRelease"`
	Profile         TicketAuditProfile           `json:"profile"`
	RequestID       string                       `json:"requestId,omitempty"`
	Method          string                       `json:"method,omitempty"`
	LatencyMillis   int64                        `json:"latencyMillis"`
	ServerViolation *GatewayServerViolationAudit `json:"serverViolation,omitempty"`
}

func encodeGatewayAuditMetadata(event GatewayAuditEvent) ([]byte, error) {
	if validateGatewayAuditEvent(event) != nil {
		return nil, ErrGatewaySecurityUnavailable
	}
	metadata := gatewayAuditMetadata{
		SchemaVersion: "sandbox-lsp-gateway-audit/v1", Outcome: event.Outcome, Code: event.Code,
		TicketID: event.TicketID, SessionID: event.SessionID,
		ConnectionID: event.ConnectionID, BindingID: event.BindingID, Mode: event.Mode,
		Head: event.Head, Document: cloneDocumentPointer(event.Document),
		TemplateRelease: event.TemplateRelease, Profile: event.Profile,
		RequestID: event.RequestID, Method: event.Method, LatencyMillis: event.LatencyMillis,
		ServerViolation: cloneGatewayServerViolation(event.ServerViolation),
	}
	return json.Marshal(metadata)
}

func cloneGatewayServerViolation(value *GatewayServerViolationAudit) *GatewayServerViolationAudit {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

var _ GatewayAuditSink = (*PostgresGatewayAuditSink)(nil)
