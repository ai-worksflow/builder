package migrations

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const conversationSummaryCheckpointMigrationFile = "000018_conversation_summary_checkpoints.up.sql"

func TestConversationSummaryCheckpointMigrationDeclaresClosedChainAndProvenanceGuards(t *testing.T) {
	t.Parallel()

	up, err := files.ReadFile(conversationSummaryCheckpointMigrationFile)
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000018_conversation_summary_checkpoints.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upSQL := strings.ToLower(string(up))
	for _, required := range []string{
		"create table conversation_summary_checkpoints",
		"conversation_summary_checkpoints_tenant_fk",
		"conversation_summary_checkpoints_message_fk",
		"conversation_summary_checkpoints_previous_fk",
		"conversation_summary_checkpoints_complete_prefix",
		"conversation_summary_checkpoints_no_self_review",
		"conversation_summary_checkpoints_review_shape",
		"conversation_summary_checkpoints_approved_coverage_idx",
		"conversation_summary_checkpoints_approved_child_idx",
		"conversation_summary_checkpoints_approved_genesis_idx",
		"conversation_summary_checkpoints_pending_insert",
		"conversation_summary_checkpoints_controlled_mutation",
		"conversations_summary_checkpoint_head_forward_only",
		"conversation_summary_checkpoint_head_commit",
		"conversation_summary_checkpoints_immutable_delete",
		"workflow_intent_new_conversation_context",
		"workflow_intent_conversation_context_immutable",
		"conversation_commands_context_match",
		"conversation_command_context_immutable",
	} {
		if !strings.Contains(upSQL, required) {
			t.Fatalf("conversation summary checkpoint migration is missing %q", required)
		}
	}
	if strings.Contains(upSQL, "add column conversation_context jsonb not null") ||
		strings.Contains(upSQL, "default '{\"version\":0,\"mode\":\"legacy_unrecorded\"}'") {
		t.Fatal("conversation context columns must use expand/backfill/contract without a legacy default")
	}

	assertMigrationFragmentsInOrder(t, upSQL,
		"create table conversation_summary_checkpoints",
		"add column summary_checkpoint_head_id",
		"add column summary_checkpoint_id",
		"update workflow_intent_proposals",
		"alter column conversation_context set not null",
		"create function validate_new_workflow_intent_conversation_context",
		"create function guard_workflow_intent_conversation_context_identity",
		"create function validate_conversation_command_context",
		"create function guard_conversation_summary_checkpoint_mutation",
		"create function reject_conversation_summary_checkpoint_delete",
	)

	downSQL := strings.ToLower(string(down))
	assertMigrationFragmentsInOrder(t, downSQL,
		"drop constraint if exists conversations_summary_checkpoint_head_fk",
		"drop trigger if exists conversation_command_context_immutable",
		"drop trigger if exists workflow_intent_conversation_context_immutable",
		"alter table conversation_commands",
		"alter table workflow_intent_proposals",
		"drop trigger if exists conversation_summary_checkpoints_immutable_delete",
		"drop trigger if exists conversation_summary_checkpoints_controlled_mutation",
		"drop table if exists conversation_summary_checkpoints",
		"drop constraint if exists conversation_messages_checkpoint_identity_unique",
		"drop constraint if exists conversations_checkpoint_tenant_identity_unique",
	)
}

func TestConversationSummaryCheckpointMigrationPostgresConstraintsAndImmutability(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "conversation_checkpoint_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := Up(ctx, database); err != nil {
		t.Fatalf("migrations.Up failed in temporary schema: %v", err)
	}

	creatorID, reviewerID, otherID := uuid.New(), uuid.New(), uuid.New()
	projectID, otherProjectID := uuid.New(), uuid.New()
	conversationID, otherConversationID := uuid.New(), uuid.New()
	for index, userID := range []uuid.UUID{creatorID, reviewerID, otherID} {
		if _, err := database.ExecContext(ctx,
			`INSERT INTO users (id,email,display_name,password_hash) VALUES ($1,$2,$3,'not-used')`,
			userID, "checkpoint-"+uuid.NewString()+"@example.com", "Checkpoint "+string(rune('A'+index))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.ExecContext(ctx,
		`INSERT INTO projects (id,name,created_by) VALUES ($1,'Checkpoint A',$2),($3,'Checkpoint B',$4)`,
		projectID, creatorID, otherProjectID, otherID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx,
		`INSERT INTO conversations (id,project_id,title,created_by) VALUES ($1,$2,'Chain A',$3),($4,$5,'Chain B',$6)`,
		conversationID, projectID, creatorID, otherConversationID, otherProjectID, otherID); err != nil {
		t.Fatal(err)
	}

	messageIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	for index, messageID := range messageIDs {
		if _, err := database.ExecContext(ctx,
			`INSERT INTO conversation_messages (id,conversation_id,sequence,role,content,created_by) VALUES ($1,$2,$3,'user',$4,$5)`,
			messageID, conversationID, index+1, "message-"+string(rune('1'+index)), creatorID); err != nil {
			t.Fatal(err)
		}
	}
	otherMessageID := uuid.New()
	if _, err := database.ExecContext(ctx,
		`INSERT INTO conversation_messages (id,conversation_id,sequence,role,content,created_by) VALUES ($1,$2,1,'user','other',$3)`,
		otherMessageID, otherConversationID, otherID); err != nil {
		t.Fatal(err)
	}
	assertRejected := func(operation, code string, run func() error) {
		t.Helper()
		assertPostgresCode(t, run(), code, operation)
	}

	definitionID, definitionVersionID := uuid.New(), uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO workflow_definitions (id,project_id,workflow_key,title,description,lifecycle,created_by)
VALUES ($1,$2,'checkpoint-context','Checkpoint context','','active',$3)`, definitionID, projectID, creatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO workflow_definition_versions
  (id,definition_id,version,schema_version,content,content_hash,validation_report,published,created_by,execution_profile_version,execution_profile_hash)
VALUES ($1,$2,1,2,'{}'::jsonb,'sha256:checkpoint','{"valid":true}'::jsonb,true,$3,'legacy-pre-pin/v0','bee729c4921a93fd2e229cd610314359ca420610c195ada00a201507bfd7a14c')`,
		definitionVersionID, definitionID, creatorID); err != nil {
		t.Fatal(err)
	}
	proposalInsertPrefix := `INSERT INTO workflow_intent_proposals
  (id,project_id,conversation_id,trigger_message_id,assistant_message_id,kind,suggested_definition_version_id,scope,source_refs,manifest_intent,workbench_instruction,origin`
	proposalInsertValues := ` VALUES ($1,$2,$3,$4,$5,'start_workflow',$6,'{}'::jsonb,'[{"artifactId":"a","revisionId":"r","contentHash":"sha256:test"}]'::jsonb,'{"mode":"use_existing"}'::jsonb,'{"objective":"Build"}'::jsonb,$7`
	assertRejected("new proposal with omitted conversation provenance", "23514", func() error {
		_, insertErr := database.ExecContext(ctx, proposalInsertPrefix+`,proposed_by)`+proposalInsertValues+`,$8)`,
			uuid.New(), projectID, conversationID, messageIDs[3], uuid.New(), definitionVersionID, "submitted", creatorID)
		return insertErr
	})
	assertRejected("new AI proposal forged as legacy", "23514", func() error {
		_, insertErr := database.ExecContext(ctx, proposalInsertPrefix+`,conversation_context,ai_provider,ai_model,proposed_by)`+proposalInsertValues+`,$8::jsonb,'provider','model',$9)`,
			uuid.New(), projectID, conversationID, messageIDs[3], uuid.New(), definitionVersionID, "ai",
			`{"version":0,"mode":"legacy_unrecorded"}`, creatorID)
		return insertErr
	})
	validProposalID, validAssistantID := uuid.New(), uuid.New()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, proposalInsertPrefix+`,conversation_context,proposed_by)`+proposalInsertValues+`,$8::jsonb,$9)`,
		validProposalID, projectID, conversationID, messageIDs[3], validAssistantID, definitionVersionID, "submitted",
		`{"version":1,"mode":"submitted"}`, creatorID); err != nil {
		transaction.Rollback()
		t.Fatalf("explicit submitted conversation provenance was rejected: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO conversation_messages (id,conversation_id,sequence,role,content,proposal_id,created_by)
VALUES ($1,$2,5,'assistant','Reviewed intent',$3,$4)`, validAssistantID, conversationID, validProposalID, creatorID); err != nil {
		transaction.Rollback()
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	assertRejected("proposal conversation context tamper", "55000", func() error {
		_, updateErr := database.ExecContext(ctx, `
UPDATE workflow_intent_proposals
SET conversation_context='{"version":1,"mode":"submitted","tampered":true}'::jsonb
WHERE id=$1`, validProposalID)
		return updateErr
	})
	assertRejected("proposal provider input hash tamper", "55000", func() error {
		_, updateErr := database.ExecContext(ctx, `
UPDATE workflow_intent_proposals
SET provider_input_hash=decode(repeat('ab',32),'hex')
WHERE id=$1`, validProposalID)
		return updateErr
	})
	assertRejected("new command with omitted conversation provenance", "23514", func() error {
		_, insertErr := database.ExecContext(ctx, `
INSERT INTO conversation_commands (id,project_id,conversation_id,proposal_id,kind,payload,accepted_by)
VALUES ($1,$2,$3,$4,'start_workflow','{}'::jsonb,$5)`, uuid.New(), projectID, conversationID, validProposalID, reviewerID)
		return insertErr
	})
	validCommandID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO conversation_commands
  (id,project_id,conversation_id,proposal_id,kind,payload,conversation_context,accepted_by)
VALUES ($1,$2,$3,$4,'start_workflow','{}'::jsonb,'{"version":1,"mode":"submitted"}'::jsonb,$5)`,
		validCommandID, projectID, conversationID, validProposalID, reviewerID); err != nil {
		t.Fatalf("command with exact proposal conversation provenance was rejected: %v", err)
	}
	assertRejected("command conversation context tamper", "55000", func() error {
		_, updateErr := database.ExecContext(ctx, `
UPDATE conversation_commands
SET conversation_context='{"version":1,"mode":"submitted","tampered":true}'::jsonb
WHERE id=$1`, validCommandID)
		return updateErr
	})
	assertRejected("command provider input hash tamper", "55000", func() error {
		_, updateErr := database.ExecContext(ctx, `
UPDATE conversation_commands
SET provider_input_hash=decode(repeat('cd',32),'hex')
WHERE id=$1`, validCommandID)
		return updateErr
	})

	prefixHash := bytesOf(0x11, 32)
	summaryHash := bytesOf(0x22, 32)
	insertPending := func(checkpointID, rowProjectID, rowConversationID uuid.UUID, previousID *uuid.UUID, throughMessageID uuid.UUID, throughSequence, messageCount int) error {
		_, insertErr := database.ExecContext(ctx, `
INSERT INTO conversation_summary_checkpoints
  (id,project_id,conversation_id,previous_checkpoint_id,through_message_id,through_sequence,message_count,content_bytes,prefix_hash,summary,summary_hash,created_by)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'Bound summary',$10,$11)`,
			checkpointID, rowProjectID, rowConversationID, previousID, throughMessageID, throughSequence,
			messageCount, max(messageCount, 1), prefixHash, summaryHash, creatorID)
		return insertErr
	}
	assertRejected("cross-project conversation checkpoint", "23503", func() error {
		return insertPending(uuid.New(), otherProjectID, conversationID, nil, messageIDs[1], 2, 2)
	})
	assertRejected("checkpoint through-message sequence mismatch", "23503", func() error {
		return insertPending(uuid.New(), projectID, conversationID, nil, messageIDs[1], 1, 1)
	})
	assertRejected("checkpoint incomplete prefix", "23514", func() error {
		return insertPending(uuid.New(), projectID, conversationID, nil, messageIDs[1], 2, 1)
	})

	otherCheckpointID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO conversation_summary_checkpoints
  (id,project_id,conversation_id,through_message_id,through_sequence,message_count,content_bytes,prefix_hash,summary,summary_hash,created_by)
VALUES ($1,$2,$3,$4,1,1,5,$5,'Other summary',$6,$7)`,
		otherCheckpointID, otherProjectID, otherConversationID, otherMessageID, prefixHash, summaryHash, otherID); err != nil {
		t.Fatal(err)
	}
	assertRejected("cross-conversation checkpoint predecessor", "23503", func() error {
		return insertPending(uuid.New(), projectID, conversationID, &otherCheckpointID, messageIDs[1], 2, 2)
	})
	assertRejected("direct approved checkpoint insert", "55000", func() error {
		_, insertErr := database.ExecContext(ctx, `
INSERT INTO conversation_summary_checkpoints
  (id,project_id,conversation_id,through_message_id,through_sequence,message_count,content_bytes,prefix_hash,summary,summary_hash,status,version,created_by,reviewed_by,reviewed_at)
VALUES ($1,$2,$3,$4,2,2,2,$5,'Self reviewed',$6,'approved',2,$7,$7,now())`,
			uuid.New(), projectID, conversationID, messageIDs[1], prefixHash, summaryHash, creatorID)
		return insertErr
	})

	genesisID := uuid.New()
	if err := insertPending(genesisID, projectID, conversationID, nil, messageIDs[1], 2, 2); err != nil {
		t.Fatalf("insert valid genesis checkpoint: %v", err)
	}
	assertRejected("checkpoint identity mutation", "55000", func() error {
		_, updateErr := database.ExecContext(ctx, `UPDATE conversation_summary_checkpoints SET summary='tampered' WHERE id=$1`, genesisID)
		return updateErr
	})
	assertRejected("checkpoint transition without version compare-and-swap", "55000", func() error {
		_, updateErr := database.ExecContext(ctx, `UPDATE conversation_summary_checkpoints SET status='approved',reviewed_by=$2,reviewed_at=now() WHERE id=$1`, genesisID, reviewerID)
		return updateErr
	})
	assertRejected("self-reviewed checkpoint transition", "23514", func() error {
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer transaction.Rollback()
		_, err = transaction.ExecContext(ctx, `
UPDATE conversation_summary_checkpoints
SET status='approved',version=version+1,reviewed_by=$2,reviewed_at=now(),review_reason='self'
WHERE id=$1`, genesisID, creatorID)
		return err
	})
	approveAndAdvance := func(checkpointID uuid.UUID) error {
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer transaction.Rollback()
		if _, err := transaction.ExecContext(ctx, `
UPDATE conversation_summary_checkpoints
SET status='approved',version=version+1,reviewed_by=$2,reviewed_at=now(),review_reason='approved'
		WHERE id=$1`, checkpointID, reviewerID); err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE conversations SET summary_checkpoint_head_id=$2 WHERE id=$1`, conversationID, checkpointID); err != nil {
			return err
		}
		return transaction.Commit()
	}
	if err := approveAndAdvance(genesisID); err != nil {
		t.Fatalf("controlled checkpoint approval and head advance were blocked: %v", err)
	}
	assertRejected("cross-conversation checkpoint head", "55000", func() error {
		_, updateErr := database.ExecContext(ctx, `UPDATE conversations SET summary_checkpoint_head_id=$2 WHERE id=$1`, otherConversationID, genesisID)
		return updateErr
	})
	assertRejected("approved checkpoint second mutation", "55000", func() error {
		_, updateErr := database.ExecContext(ctx, `UPDATE conversation_summary_checkpoints SET review_reason='changed' WHERE id=$1`, genesisID)
		return updateErr
	})
	assertRejected("checkpoint deletion", "55000", func() error {
		_, deleteErr := database.ExecContext(ctx, `DELETE FROM conversation_summary_checkpoints WHERE id=$1`, genesisID)
		return deleteErr
	})

	childOneID, childTwoID := uuid.New(), uuid.New()
	if err := insertPending(childOneID, projectID, conversationID, &genesisID, messageIDs[2], 3, 3); err != nil {
		t.Fatal(err)
	}
	if err := insertPending(childTwoID, projectID, conversationID, &genesisID, messageIDs[3], 4, 4); err != nil {
		t.Fatal(err)
	}
	if err := approveAndAdvance(childOneID); err != nil {
		t.Fatalf("approve first checkpoint child: %v", err)
	}
	assertRejected("second approved checkpoint child", "55000", func() error {
		_, updateErr := database.ExecContext(ctx, `UPDATE conversation_summary_checkpoints SET status='approved',version=2,reviewed_by=$2,reviewed_at=now() WHERE id=$1`, childTwoID, reviewerID)
		return updateErr
	})

	var triggerCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_trigger
WHERE tgrelid = 'conversation_summary_checkpoints'::regclass
  AND NOT tgisinternal
  AND tgname IN (
    'conversation_summary_checkpoints_pending_insert',
    'conversation_summary_checkpoints_controlled_mutation',
    'conversation_summary_checkpoint_head_commit',
    'conversation_summary_checkpoints_immutable_delete'
  )`).Scan(&triggerCount); err != nil {
		t.Fatal(err)
	}
	if triggerCount != 4 {
		t.Fatalf("expected all checkpoint lifecycle triggers, got %d", triggerCount)
	}
}

func assertMigrationFragmentsInOrder(t *testing.T, contents string, fragments ...string) {
	t.Helper()
	previous := -1
	for _, fragment := range fragments {
		index := strings.Index(contents, fragment)
		if index < 0 {
			t.Fatalf("migration is missing %q", fragment)
		}
		if index <= previous {
			t.Fatalf("migration fragment %q is out of dependency order", fragment)
		}
		previous = index
	}
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}
