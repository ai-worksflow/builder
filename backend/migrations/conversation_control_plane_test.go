package migrations

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestConversationControlPlaneMigrationEnforcesImmutableReviewBoundary(t *testing.T) {
	t.Parallel()
	contents, err := files.ReadFile("000009_conversation_control_plane.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	for _, required := range []string{
		"create table conversations",
		"create table conversation_messages",
		"create table workflow_intent_proposals",
		"create table conversation_commands",
		"unique (conversation_id, sequence)",
		"unique (proposal_id)",
		"role = 'assistant' and proposal_id is not null",
		"status in ('pending', 'accepted', 'rejected')",
		"status in ('pending', 'executed', 'rejected', 'failed')",
		"before update or delete on conversation_messages",
		"conversation messages are immutable",
		"workflow intent proposal identity is immutable",
		"conversation command identity is immutable",
		"on conversations (project_id, created_at desc, id desc)",
		"on conversation_messages (conversation_id, sequence asc, id asc)",
		"where status = 'pending'",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("conversation migration is missing %q", required)
		}
	}
}

func TestConversationControlPlaneDownMigrationRemovesTriggersBeforeTables(t *testing.T) {
	t.Parallel()
	contents, err := files.ReadFile("000009_conversation_control_plane.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	trigger := strings.Index(sql, "drop trigger if exists conversation_messages_immutable_update")
	table := strings.Index(sql, "drop table if exists conversation_messages")
	if trigger < 0 || table < 0 || trigger > table {
		t.Fatal("immutable message trigger must be removed before its table")
	}
}

func TestConversationControlPlaneImmutabilityOnPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()
	transaction, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryLockID); err != nil {
		t.Fatal(err)
	}
	schema := "conversation_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := transaction.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL search_path = "`+schema+`", public`); err != nil {
		t.Fatal(err)
	}
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		contents, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, string(contents)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}

	userID, projectID := uuid.New(), uuid.New()
	definitionID, definitionVersionID := uuid.New(), uuid.New()
	conversationID, userMessageID := uuid.New(), uuid.New()
	proposalID, assistantMessageID, commandID := uuid.New(), uuid.New(), uuid.New()
	seeds := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, email, display_name, password_hash) VALUES ($1, 'conversation@example.com', 'Conversation', 'hash')`, []any{userID}},
		{`INSERT INTO projects (id, name, created_by) VALUES ($1, 'Conversation', $2)`, []any{projectID, userID}},
		{`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner')`, []any{projectID, userID}},
		{`INSERT INTO workflow_definitions (id, project_id, workflow_key, title, description, lifecycle, created_by) VALUES ($1, $2, 'conversation-flow', 'Conversation flow', '', 'active', $3)`, []any{definitionID, projectID, userID}},
		{`INSERT INTO workflow_definition_versions (id, definition_id, version, schema_version, content, content_hash, validation_report, published, created_by, execution_profile_version, execution_profile_hash) VALUES ($1, $2, 1, 2, '{}'::jsonb, 'sha256:test', '{"valid":true}'::jsonb, true, $3, 'legacy-pre-pin/v0', 'bee729c4921a93fd2e229cd610314359ca420610c195ada00a201507bfd7a14c')`, []any{definitionVersionID, definitionID, userID}},
		{`INSERT INTO conversations (id, project_id, title, created_by) VALUES ($1, $2, 'Immutable conversation', $3)`, []any{conversationID, projectID, userID}},
		{`INSERT INTO conversation_messages (id, conversation_id, sequence, role, content, created_by) VALUES ($1, $2, 1, 'user', 'Build the approved app', $3)`, []any{userMessageID, conversationID, userID}},
		{`INSERT INTO workflow_intent_proposals (id, project_id, conversation_id, trigger_message_id, assistant_message_id, kind, suggested_definition_version_id, scope, source_refs, manifest_intent, workbench_instruction, origin, conversation_context, proposed_by) VALUES ($1, $2, $3, $4, $5, 'start_workflow', $6, '{}'::jsonb, '[{"artifactId":"a","revisionId":"r","contentHash":"sha256:test"}]'::jsonb, '{"mode":"use_existing"}'::jsonb, '{"objective":"Build"}'::jsonb, 'submitted', '{"version":1,"mode":"submitted"}'::jsonb, $7)`, []any{proposalID, projectID, conversationID, userMessageID, assistantMessageID, definitionVersionID, userID}},
		{`INSERT INTO conversation_messages (id, conversation_id, sequence, role, content, proposal_id, created_by) VALUES ($1, $2, 2, 'assistant', 'Review this intent', $3, $4)`, []any{assistantMessageID, conversationID, proposalID, userID}},
		{`UPDATE workflow_intent_proposals SET status = 'accepted', version = 2, decided_by = $1, decided_at = now() WHERE id = $2`, []any{userID, proposalID}},
		{`INSERT INTO conversation_commands (id, project_id, conversation_id, proposal_id, kind, payload, conversation_context, accepted_by) VALUES ($1, $2, $3, $4, 'start_workflow', '{"definitionVersionId":"test"}'::jsonb, '{"version":1,"mode":"submitted"}'::jsonb, $5)`, []any{commandID, projectID, conversationID, proposalID, userID}},
	}
	for _, seed := range seeds {
		if _, err := transaction.ExecContext(ctx, seed.query, seed.args...); err != nil {
			t.Fatal(err)
		}
	}

	assertRejected := func(name, query string, args ...any) {
		t.Helper()
		if _, err := transaction.ExecContext(ctx, `SAVEPOINT immutable_check`); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, query, args...); err == nil {
			t.Errorf("%s mutation unexpectedly succeeded", name)
		}
		if _, err := transaction.ExecContext(ctx, `ROLLBACK TO SAVEPOINT immutable_check`); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `RELEASE SAVEPOINT immutable_check`); err != nil {
			t.Fatal(err)
		}
	}
	assertRejected("message update", `UPDATE conversation_messages SET content = 'tampered' WHERE id = $1`, userMessageID)
	assertRejected("message delete", `DELETE FROM conversation_messages WHERE id = $1`, userMessageID)
	assertRejected("proposal scope", `UPDATE workflow_intent_proposals SET scope = '{"tampered":true}'::jsonb WHERE id = $1`, proposalID)
	assertRejected("command payload", `UPDATE conversation_commands SET payload = '{"tampered":true}'::jsonb WHERE id = $1`, commandID)

	if _, err := transaction.ExecContext(ctx, `UPDATE conversation_commands SET version = version + 1, execution_actor_id = $1, execution_claim = $2, claim_expires_at = now() + interval '1 minute' WHERE id = $3`, userID, uuid.New(), commandID); err != nil {
		t.Fatalf("controlled command state transition was blocked: %v", err)
	}
}
