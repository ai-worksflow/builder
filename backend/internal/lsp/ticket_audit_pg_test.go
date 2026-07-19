package lsp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPostgresTicketAuditSinkPersistsPrivacyBoundedEvent(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is required for the LSP audit canary")
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

	now := time.Now().UTC().Truncate(time.Microsecond)
	actorID, projectID := uuid.New(), uuid.New()

	sink, err := NewPostgresTicketAuditSink(tx)
	if err != nil {
		t.Fatal(err)
	}
	head := validHead()
	head.ProjectID = projectID.String()
	event := TicketAuditEvent{
		Action: TicketAuditIssue, Outcome: "issued", Code: "ok", TicketID: testTicket,
		ProjectID: projectID.String(), ActorID: actorID.String(), SessionID: testSession,
		Mode: TicketModeSnapshot, OriginHash: hashAuditOrigin("https://builder.example"),
		Head: head, TemplateRelease: ExactTemplateRelease{ID: testRelease, ContentHash: lspDigest("2")},
		Profiles: auditProfiles([]ProfileIdentity{lspTestProfile("typescript")}), OccurredAt: now,
	}
	if err := sink.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	var stored storage.AuditEventModel
	if err := tx.Where("action = ? AND target_id = ?", "lsp.ticket.issue", testTicket).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	metadata := string(stored.Metadata)
	if stored.ProjectID == nil || *stored.ProjectID != projectID || stored.ActorID == nil ||
		*stored.ActorID != actorID || stored.TargetType != "lsp_connection_ticket" ||
		!strings.Contains(metadata, `"sandbox-lsp-ticket-audit/v1"`) ||
		!strings.Contains(metadata, event.OriginHash) || strings.Contains(metadata, "builder.example") ||
		strings.Contains(metadata, strings.Repeat("A", 43)) {
		t.Fatalf("stored audit escaped privacy contract: %#v metadata=%s", stored, metadata)
	}
}
