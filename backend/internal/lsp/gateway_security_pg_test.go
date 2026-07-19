package lsp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func gatewayAuditFixture(t *testing.T, action, outcome, code string) GatewayAuditEvent {
	t.Helper()
	head := validHead()
	uri, err := CandidateModelURI(head.ProjectID, head.CandidateID, "apps/web/page.ts")
	if err != nil {
		t.Fatal(err)
	}
	document := DocumentFence{
		ModelURI: uri, OpenID: testOpen, ModelVersion: 3, SavedContentHash: lspDigest("b"),
	}
	profile := lspTestProfile("typescript")
	event := GatewayAuditEvent{
		Action: action, Outcome: outcome, Code: code,
		TicketID: testTicket, ProjectID: testProject, ActorID: testActor, SessionID: testSession,
		ConnectionID: testConnection, BindingID: gatewayBindingID,
		Mode: TicketModeEditor, Head: head,
		TemplateRelease: profile.TemplateRelease, Profile: gatewayProfileAuditIdentity(profile),
		OccurredAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if _, requestEvent := gatewayAuditShape(action); requestEvent {
		event.Document = &document
		event.RequestID = gatewayBrowserRequestID
		event.Method = "textDocument/hover"
		if action != GatewayAuditRequestAdmitted {
			event.LatencyMillis = 17
		}
	}
	if action == GatewayAuditServerViolation {
		event.ServerViolation = &GatewayServerViolationAudit{
			Method: "workspace/configuration", Ordinal: 2, Count: 2,
		}
	}
	return event
}

func gatewayAuditPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is required for the LSP Gateway audit canary")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDatabase.Close() })
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	if err := tx.Exec(`
CREATE TEMPORARY TABLE audit_events (
    id uuid PRIMARY KEY,
    project_id uuid,
    actor_id uuid,
    request_id text,
    action text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    metadata jsonb NOT NULL,
    created_at timestamptz NOT NULL
) ON COMMIT DROP
`).Error; err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestPostgresGatewayAuditSinkIsPrivacyBoundedAndRetryIdempotent(t *testing.T) {
	database := gatewayAuditPostgres(t)
	sink, err := NewPostgresGatewayAuditSink(database)
	if err != nil {
		t.Fatal(err)
	}
	event := gatewayAuditFixture(t, GatewayAuditRequestCompleted, "completed", "ok")
	const writers = 16
	var group sync.WaitGroup
	errorsSeen := make(chan error, writers)
	for index := 0; index < writers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsSeen <- sink.AppendGatewayAudit(context.Background(), event)
		}()
	}
	group.Wait()
	close(errorsSeen)
	for appendErr := range errorsSeen {
		if appendErr != nil {
			t.Fatalf("idempotent append failed: %v", appendErr)
		}
	}
	var count int64
	if err := database.Model(&storage.AuditEventModel{}).
		Where("id = ?", gatewayAuditEventID(event)).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("durable retry count = %d, %v", count, err)
	}
	var stored storage.AuditEventModel
	if err := database.Where("id = ?", gatewayAuditEventID(event)).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	metadata := string(stored.Metadata)
	for _, forbidden := range []string{
		"ticketSecret", "payload", "params", "sourceText", "unsavedText", "diagnostics", "result",
	} {
		if strings.Contains(metadata, forbidden) {
			t.Fatalf("Gateway audit metadata exposed forbidden field %q: %s", forbidden, metadata)
		}
	}
	if stored.Action != "lsp.gateway.request.completed" ||
		stored.TargetType != "lsp_gateway_binding" || stored.TargetID != gatewayBindingID ||
		stored.RequestID == nil || *stored.RequestID != gatewayBrowserRequestID ||
		!strings.Contains(metadata, `"sandbox-lsp-gateway-audit/v1"`) ||
		!strings.Contains(metadata, `"textDocument/hover"`) {
		t.Fatalf("stored Gateway audit identity drifted: %#v metadata=%s", stored, metadata)
	}
}

func TestPostgresGatewayAuditSinkConvergesCompetingTerminalFacts(t *testing.T) {
	database := gatewayAuditPostgres(t)
	sink, err := NewPostgresGatewayAuditSink(database)
	if err != nil {
		t.Fatal(err)
	}
	canceled := gatewayAuditFixture(t, GatewayAuditRequestCancel, "canceled", "client_cancel")
	timedOut := gatewayAuditFixture(t, GatewayAuditRequestTimeout, "timed_out", "request_timeout")
	if gatewayAuditEventID(canceled) != gatewayAuditEventID(timedOut) {
		t.Fatal("competing terminal facts did not share one durable identity")
	}
	var group sync.WaitGroup
	for _, event := range []GatewayAuditEvent{canceled, timedOut} {
		event := event
		group.Add(1)
		go func() {
			defer group.Done()
			if appendErr := sink.AppendGatewayAudit(context.Background(), event); appendErr != nil {
				t.Errorf("terminal append failed: %v", appendErr)
			}
		}()
	}
	group.Wait()
	var count int64
	if err := database.Model(&storage.AuditEventModel{}).
		Where("id = ?", gatewayAuditEventID(canceled)).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("competing terminal count = %d, %v", count, err)
	}
}

func TestPostgresGatewayEditorLeaseAuditIsDurableAndPayloadFree(t *testing.T) {
	database := gatewayAuditPostgres(t)
	sink, err := NewPostgresGatewayAuditSink(database)
	if err != nil {
		t.Fatal(err)
	}
	facts := []GatewayAuditEvent{
		gatewayAuditFixture(t, GatewayAuditEditorLeaseAcquire, "acquired", "ok"),
		gatewayAuditFixture(t, GatewayAuditEditorLeaseConflict, "conflict", "active_owner"),
		gatewayAuditFixture(t, GatewayAuditEditorLeaseLost, "lost", "owner_fenced"),
		gatewayAuditFixture(t, GatewayAuditEditorLeaseRelease, "released", "ok"),
	}
	for _, fact := range facts {
		if err := sink.AppendGatewayAudit(context.Background(), fact); err != nil {
			t.Fatal(err)
		}
	}
	var stored []storage.AuditEventModel
	if err := database.Where("target_id = ? AND action LIKE ?", gatewayBindingID, "lsp.gateway.editor_lease.%").
		Order("action ASC").Find(&stored).Error; err != nil || len(stored) != len(facts) {
		t.Fatalf("durable editor lease facts = %#v, %v", stored, err)
	}
	for _, event := range stored {
		metadata := string(event.Metadata)
		for _, forbidden := range []string{
			"payload", "params", "sourceText", "unsavedText", "diagnostics", "result", "ownerBindingId",
		} {
			if strings.Contains(metadata, forbidden) {
				t.Fatalf("editor lease audit exposed %q: %s", forbidden, metadata)
			}
		}
		if event.RequestID != nil {
			t.Fatalf("editor lease fact unexpectedly carried a request: %#v", event)
		}
	}
}

func TestPostgresGatewayServerViolationAuditIsDeterministicAndPayloadFree(t *testing.T) {
	database := gatewayAuditPostgres(t)
	sink, err := NewPostgresGatewayAuditSink(database)
	if err != nil {
		t.Fatal(err)
	}
	event := gatewayAuditFixture(
		t, GatewayAuditServerViolation, "rejected", "server_request_rejected",
	)
	firstID := gatewayAuditEventID(event)
	retry := event
	retry.OccurredAt = retry.OccurredAt.Add(time.Second)
	if retryID := gatewayAuditEventID(retry); retryID != firstID {
		t.Fatalf("retry ID drifted: %s != %s", retryID, firstID)
	}
	for _, fact := range []GatewayAuditEvent{event, retry} {
		if err := sink.AppendGatewayAudit(context.Background(), fact); err != nil {
			t.Fatal(err)
		}
	}
	var stored storage.AuditEventModel
	if err := database.Where("id = ?", firstID).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	metadata := string(stored.Metadata)
	var decoded gatewayAuditMetadata
	var fields map[string]json.RawMessage
	decodeErr := json.Unmarshal(stored.Metadata, &decoded)
	fieldsErr := json.Unmarshal(stored.Metadata, &fields)
	_, hasRequestID := fields["requestId"]
	_, hasDocument := fields["documentFence"]
	if stored.Action != "lsp.gateway.server.violation" || stored.RequestID != nil ||
		decodeErr != nil || fieldsErr != nil || decoded.ServerViolation == nil ||
		decoded.ServerViolation.Method != "workspace/configuration" ||
		decoded.ServerViolation.Ordinal != 2 || decoded.ServerViolation.Count != 2 ||
		hasRequestID || hasDocument {
		t.Fatalf("server violation metadata drifted: %#v metadata=%s", stored, metadata)
	}
	for _, forbidden := range []string{
		"payload", "params", "sourceText", "unsavedText", "diagnostics", "result", "serverRequestId",
	} {
		if strings.Contains(metadata, forbidden) {
			t.Fatalf("server violation metadata exposed %q: %s", forbidden, metadata)
		}
	}
	var count int64
	if err := database.Model(&storage.AuditEventModel{}).Where("id = ?", firstID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("server violation retry count = %d, %v", count, err)
	}

	malformed := gatewayAuditFixture(
		t, GatewayAuditServerViolation, "rejected", "server_message_malformed",
	)
	malformed.ServerViolation = &GatewayServerViolationAudit{
		Method: serverControlInvalidMessageAuditMethod, Ordinal: 1, Count: 1,
	}
	if err := sink.AppendGatewayAudit(context.Background(), malformed); err != nil {
		t.Fatal(err)
	}
	var malformedStored storage.AuditEventModel
	if err := database.Where("id = ?", gatewayAuditEventID(malformed)).Take(&malformedStored).Error; err != nil {
		t.Fatal(err)
	}
	malformedMetadata := string(malformedStored.Metadata)
	if malformedStored.RequestID != nil ||
		!strings.Contains(malformedMetadata, serverControlInvalidMessageAuditMethod) ||
		!strings.Contains(malformedMetadata, "server_message_malformed") {
		t.Fatalf("malformed server audit metadata drifted: %#v %s", malformedStored, malformedMetadata)
	}
}

func TestPostgresGatewayUnknownServerMethodCannotBecomeAuditCovertChannel(t *testing.T) {
	database := gatewayAuditPostgres(t)
	sink, err := NewPostgresGatewayAuditSink(database)
	if err != nil {
		t.Fatal(err)
	}
	decodeUnknown := func(method, requestID, source string) ServerControlMessage {
		t.Helper()
		message, decodeErr := DecodeServerControlMessage([]byte(
			`{"jsonrpc":"2.0","id":"`+requestID+`","method":"`+method+`","params":{"source":"`+source+`"}}`,
		), serverControlLimits())
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		return message
	}
	first := decodeUnknown("databaseSecretAlpha/callback", "private-id-alpha", "private-source-alpha")
	second := decodeUnknown("databaseSecretBeta/callback", "private-id-beta", "private-source-beta")
	if first.Method != serverControlUnknownRequestAuditMethod || second.Method != first.Method {
		t.Fatalf("unknown methods were not normalized: %#v %#v", first, second)
	}

	event := gatewayAuditFixture(
		t, GatewayAuditServerViolation, "rejected", "server_request_forbidden",
	)
	event.ServerViolation = &GatewayServerViolationAudit{Method: first.Method, Ordinal: 1, Count: 1}
	alternate := event
	alternate.ServerViolation = &GatewayServerViolationAudit{Method: second.Method, Ordinal: 1, Count: 1}
	if gatewayAuditEventID(event) != gatewayAuditEventID(alternate) {
		t.Fatal("normalized unknown methods produced different durable event IDs")
	}

	invalidAlpha := event
	invalidAlpha.ServerViolation = &GatewayServerViolationAudit{
		Method: "databaseSecretAlpha/callback", Ordinal: 1, Count: 1,
	}
	invalidBeta := event
	invalidBeta.ServerViolation = &GatewayServerViolationAudit{
		Method: "databaseSecretBeta/callback", Ordinal: 1, Count: 1,
	}
	if validateGatewayAuditEvent(invalidAlpha) == nil || validateGatewayAuditEvent(invalidBeta) == nil {
		t.Fatal("arbitrary server methods were admitted by the durable audit boundary")
	}
	if gatewayAuditEventID(invalidAlpha) != gatewayAuditEventID(invalidBeta) {
		t.Fatal("invalid raw methods influenced defensive event ID derivation")
	}
	if appendErr := sink.AppendGatewayAudit(context.Background(), invalidAlpha); appendErr == nil {
		t.Fatal("PostgreSQL sink admitted an arbitrary server audit method")
	}

	if err := sink.AppendGatewayAudit(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	var stored storage.AuditEventModel
	if err := database.Where("id = ?", gatewayAuditEventID(event)).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	metadata := string(stored.Metadata)
	if stored.RequestID != nil || !strings.Contains(metadata, serverControlUnknownRequestAuditMethod) {
		t.Fatalf("normalized unknown audit metadata = %#v %s", stored, metadata)
	}
	for _, forbidden := range []string{
		"databaseSecretAlpha", "databaseSecretBeta", "private-id-alpha", "private-id-beta",
		"private-source-alpha", "private-source-beta",
	} {
		if strings.Contains(metadata, forbidden) {
			t.Fatalf("unknown method covert channel retained %q: %s", forbidden, metadata)
		}
	}
}
