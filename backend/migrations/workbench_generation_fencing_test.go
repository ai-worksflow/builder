package migrations

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const workbenchGenerationMigrationFile = "000015_workbench_generation_fencing.up.sql"

func workbenchGenerationMigrationFiles(t *testing.T) []string {
	t.Helper()
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	result := make([]string, 0, len(names))
	for _, name := range names {
		if name > workbenchGenerationMigrationFile {
			break
		}
		result = append(result, name)
		if name == workbenchGenerationMigrationFile {
			return result
		}
	}
	t.Fatalf("migration set does not contain %s", workbenchGenerationMigrationFile)
	return nil
}

func TestWorkbenchGenerationFencingMigrationDeclaresDurableAuthorityBoundaries(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000015_workbench_generation_fencing.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000015_workbench_generation_fencing.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"implementation_proposals_one_active_per_leaf_idx",
		"implementation_generation_claims_one_processing_leaf_idx",
		"no proposal was modified automatically",
		"conversation_command_id",
		"supersedes_proposal_id",
		"expected_active_proposal_version",
		"governance_manifest_id",
		"governance_source_refs",
		"instruction jsonb not null",
		"requested_model text not null",
		"generation_contract_version text not null",
		"system_prompt_hash text not null",
		"output_schema_hash text not null",
		"generated implementation proposal has no matching live generation claim",
		"conversation implementation proposal cannot become reviewable before command receipt",
		"conversation implementation decisions require a committed command receipt",
		"implementation generation supersede target is not the exact undecided open proposal",
		"implementation generation replay identity is immutable",
		"accepted same-project Workbench proposal",
		"conversation implementation generation claim requires a pending command",
		"{workbench,expectedRunId}",
		"{workbench,expectedBundleId}",
		"{definitionVersionId}",
		"workflow_intent_desired_output_immutable",
		"implementation proposal generation identity is immutable",
	} {
		if !strings.Contains(strings.ToLower(string(up)), strings.ToLower(required)) {
			t.Fatalf("workbench generation migration is missing %q", required)
		}
	}
	for _, required := range []string{
		"drop trigger if exists conversation_implementation_decision_receipt_gate",
		"drop table if exists implementation_generation_claims",
		"drop column if exists conversation_command_id",
		"drop column if exists supersedes_proposal_id",
		"drop column if exists desired_output_capability",
	} {
		if !strings.Contains(strings.ToLower(string(down)), strings.ToLower(required)) {
			t.Fatalf("workbench generation rollback is missing %q", required)
		}
	}
}

func TestWorkbenchGenerationFencingMigrationUpAndDownPostgres(t *testing.T) {
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
	schema := "workbench_fencing_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := transaction.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL search_path = "`+schema+`", public`); err != nil {
		t.Fatal(err)
	}
	names := workbenchGenerationMigrationFiles(t)
	for _, name := range names {
		contents, readErr := files.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := transaction.ExecContext(ctx, string(contents)); applyErr != nil {
			t.Fatalf("apply %s: %v", name, applyErr)
		}
	}
	var tableName sql.NullString
	if err := transaction.QueryRowContext(ctx, `SELECT to_regclass('implementation_generation_claims')::text`).Scan(&tableName); err != nil || !tableName.Valid {
		t.Fatalf("up migration did not create generation claims: table=%v err=%v", tableName, err)
	}
	down, err := files.ReadFile("000015_workbench_generation_fencing.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("apply migration 015 down: %v", err)
	}
	if err := transaction.QueryRowContext(ctx, `SELECT to_regclass('implementation_generation_claims')::text`).Scan(&tableName); err != nil {
		t.Fatal(err)
	}
	if tableName.Valid {
		t.Fatalf("down migration left generation claims table %q", tableName.String)
	}
	var proposalColumns int
	if err := transaction.QueryRowContext(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = 'implementation_proposals'
  AND column_name IN ('execution_source','conversation_command_id','supersedes_proposal_id','instruction_hash','ai_provider','ai_model')`).Scan(&proposalColumns); err != nil {
		t.Fatal(err)
	}
	if proposalColumns != 0 {
		t.Fatalf("down migration left %d implementation proposal fencing columns", proposalColumns)
	}
}

func TestWorkbenchGenerationFencingBindsConversationClaimToAcceptedTargetPostgres(t *testing.T) {
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
	schema := "workbench_target_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := transaction.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL search_path = "`+schema+`", public`); err != nil {
		t.Fatal(err)
	}
	names := workbenchGenerationMigrationFiles(t)
	for _, name := range names {
		contents, readErr := files.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := transaction.ExecContext(ctx, string(contents)); applyErr != nil {
			t.Fatalf("apply %s: %v", name, applyErr)
		}
	}

	userID, projectID := uuid.New(), uuid.New()
	definitionID, definitionVersionID, otherDefinitionVersionID := uuid.New(), uuid.New(), uuid.New()
	governanceManifestID, runID, otherRunID := uuid.New(), uuid.New(), uuid.New()
	rootID, wrongLeafID := uuid.New(), uuid.New()
	conversationID, userMessageID := uuid.New(), uuid.New()
	governanceHash := "sha256:" + strings.Repeat("a", 64)
	sourceContentHash := "sha256:" + strings.Repeat("b", 64)
	sourceRefs := `[{"artifactId":"source-artifact","revisionId":"source-revision","contentHash":"` + sourceContentHash + `"}]`
	seeds := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id,email,display_name,password_hash) VALUES ($1,$2,'Generation target','hash')`, []any{userID, "generation-target-" + userID.String() + "@example.com"}},
		{`INSERT INTO projects (id,name,created_by) VALUES ($1,'Generation target',$2)`, []any{projectID, userID}},
		{`INSERT INTO project_members (project_id,user_id,role) VALUES ($1,$2,'owner')`, []any{projectID, userID}},
		{`INSERT INTO workflow_definitions (id,project_id,workflow_key,title,description,lifecycle,created_by) VALUES ($1,$2,$3,'Generation target','','active',$4)`, []any{definitionID, projectID, "generation-target-" + definitionID.String(), userID}},
		{`INSERT INTO workflow_definition_versions (id,definition_id,version,schema_version,content,content_hash,validation_report,published,created_by) VALUES ($1,$2,1,2,'{}'::jsonb,$3,'{"valid":true}'::jsonb,true,$4),($5,$2,2,2,'{}'::jsonb,$6,'{"valid":true}'::jsonb,true,$4)`, []any{definitionVersionID, definitionID, "sha256:" + strings.Repeat("c", 64), userID, otherDefinitionVersionID, "sha256:" + strings.Repeat("d", 64)}},
		{`INSERT INTO input_manifests (id,project_id,kind,schema_version,content_store,content_ref,content_hash,manifest_hash,created_by) VALUES ($1,$2,'conversation',1,'mongo',$3,$4,$5,$6)`, []any{governanceManifestID, projectID, "governance-" + governanceManifestID.String(), "sha256:" + strings.Repeat("e", 64), governanceHash, userID}},
		{`INSERT INTO workflow_runs (id,project_id,definition_version_id,status,input_manifest_id,scope,context,started_by,started_at) VALUES ($1,$2,$3,'running',$4,'{}'::jsonb,'{}'::jsonb,$5,now()),($6,$2,$7,'running',$4,'{}'::jsonb,'{}'::jsonb,$5,now())`, []any{runID, projectID, definitionVersionID, governanceManifestID, userID, otherRunID, otherDefinitionVersionID}},
		{`INSERT INTO application_build_manifests (id,project_id,workflow_run_id,schema_version,content_store,content_ref,content_hash,manifest_hash,status,created_by,root_manifest_id,root_ordinal,manifest_group_key) VALUES ($1,$2,$3,1,'mongo',$4,$5,$6,'frozen',$7,$1,0,'target')`, []any{rootID, projectID, runID, "root-" + rootID.String(), "sha256:" + strings.Repeat("f", 64), "sha256:" + strings.Repeat("1", 64), userID}},
		{`INSERT INTO application_build_manifests (id,project_id,workflow_run_id,schema_version,content_store,content_ref,content_hash,manifest_hash,status,created_by,root_manifest_id,derived_from_id,root_ordinal,manifest_group_key) VALUES ($1,$2,$3,1,'mongo',$4,$5,$6,'frozen',$7,$8,$8,0,'target')`, []any{wrongLeafID, projectID, otherRunID, "wrong-leaf-" + wrongLeafID.String(), "sha256:" + strings.Repeat("2", 64), "sha256:" + strings.Repeat("3", 64), userID, rootID}},
		{`INSERT INTO conversations (id,project_id,title,created_by) VALUES ($1,$2,'Generation target',$3)`, []any{conversationID, projectID, userID}},
		{`INSERT INTO conversation_messages (id,conversation_id,sequence,role,content,created_by) VALUES ($1,$2,1,'user','Build the reviewed application',$3)`, []any{userMessageID, conversationID, userID}},
	}
	for _, seed := range seeds {
		if _, err := transaction.ExecContext(ctx, seed.query, seed.args...); err != nil {
			t.Fatal(err)
		}
	}

	type commandFixture struct {
		proposalID uuid.UUID
		commandID  uuid.UUID
	}
	seedCommand := func(sequence int, status string, suggestedVersionID, payloadVersionID uuid.UUID) commandFixture {
		t.Helper()
		fixture := commandFixture{proposalID: uuid.New(), commandID: uuid.New()}
		assistantMessageID := uuid.New()
		decisionColumns := ""
		decisionValues := ""
		version := 1
		if status == "accepted" {
			decisionColumns = ",decided_by,decided_at"
			decisionValues = ",$12,now()"
			version = 2
		}
		proposalQuery := `INSERT INTO workflow_intent_proposals (
  id,project_id,conversation_id,trigger_message_id,assistant_message_id,kind,status,version,
  suggested_definition_version_id,scope,source_refs,manifest_intent,workbench_instruction,origin,proposed_by` + decisionColumns + `
) VALUES (
  $1,$2,$3,$4,$5,'workbench_instruction',$6,$7,$8,'{}'::jsonb,CAST($9 AS jsonb),
  jsonb_build_object('mode','use_existing','purpose','reviewed generation','inputManifest',jsonb_build_object('id',$10::text,'hash',$11::text)),
  jsonb_build_object('objective','Build reviewed app','constraints','[]'::jsonb,'expectedRunId',$13::text,'expectedBundleId',$14::text),
  'submitted',$12` + decisionValues + `
)`
		if _, err := transaction.ExecContext(ctx, proposalQuery,
			fixture.proposalID, projectID, conversationID, userMessageID, assistantMessageID,
			status, version, suggestedVersionID, sourceRefs, governanceManifestID, governanceHash,
			userID, runID, rootID,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO conversation_messages (id,conversation_id,sequence,role,content,proposal_id,created_by) VALUES ($1,$2,$3,'assistant','Review this Workbench intent',$4,$5)`, assistantMessageID, conversationID, sequence, fixture.proposalID, userID); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO conversation_commands (id,project_id,conversation_id,proposal_id,kind,payload,accepted_by) VALUES (
  $1,$2,$3,$4,'workbench_instruction',
  jsonb_build_object(
    'definitionVersionId',$5::text,'desiredOutputCapability','application','scope','{}'::jsonb,
    'sourceRefs',CAST($6 AS jsonb),
    'manifestIntent',jsonb_build_object('mode','use_existing','purpose','reviewed generation','inputManifest',jsonb_build_object('id',$7::text,'hash',$8::text)),
    'workbench',jsonb_build_object('objective','Build reviewed app','constraints','[]'::jsonb,'expectedRunId',$9::text,'expectedBundleId',$10::text)
  ),$11
)`, fixture.commandID, projectID, conversationID, fixture.proposalID, payloadVersionID,
			sourceRefs, governanceManifestID, governanceHash, runID, rootID, userID,
		); err != nil {
			t.Fatal(err)
		}
		return fixture
	}
	validCommand := seedCommand(2, "accepted", definitionVersionID, definitionVersionID)
	wrongDefinitionCommand := seedCommand(3, "accepted", otherDefinitionVersionID, otherDefinitionVersionID)
	pendingProposalCommand := seedCommand(4, "pending", definitionVersionID, definitionVersionID)
	rejectedCommand := seedCommand(5, "accepted", definitionVersionID, definitionVersionID)
	if _, err := transaction.ExecContext(ctx, `UPDATE conversation_commands SET
  status = 'rejected', version = version + 1, result = '{"reason":"rejected before generation"}'::jsonb,
  rejected_by = $1, rejected_at = now(), updated_at = now()
WHERE id = $2`, userID, rejectedCommand.commandID); err != nil {
		t.Fatal(err)
	}

	instructionHash := "sha256:" + strings.Repeat("4", 64)
	systemPromptHash := "sha256:" + strings.Repeat("5", 64)
	outputSchemaHash := "sha256:" + strings.Repeat("6", 64)
	insertClaim := func(claimID, buildManifestID, rootManifestID, commandID uuid.UUID) error {
		_, err := transaction.ExecContext(ctx, `INSERT INTO implementation_generation_claims (
  id,build_manifest_id,project_id,root_manifest_id,request_key,reserved_proposal_id,
  execution_source,conversation_command_id,governance_manifest_id,governance_manifest_hash,
  governance_source_refs,instruction,instruction_hash,requested_model,generation_contract_version,
  system_prompt_hash,output_schema_hash,actor_id,claim_token,claim_expires_at,status,attempt_count
) VALUES (
  $1,$2,$3,$4,$5,$5,'conversation_command',$5,$6,$7,CAST($8 AS jsonb),
  '{"objective":"Build reviewed app","constraints":[]}'::jsonb,$9,'gpt-5','implementation-proposal-generation/v1',
  $10,$11,$12,$13,now() + interval '5 minutes','processing',1
)`, claimID, buildManifestID, projectID, rootManifestID, commandID, governanceManifestID,
			governanceHash, sourceRefs, instructionHash, systemPromptHash, outputSchemaHash, userID, uuid.New())
		return err
	}
	assertClaimRejected := func(name, expectedMessage string, buildManifestID, rootManifestID, commandID uuid.UUID) {
		t.Helper()
		if _, err := transaction.ExecContext(ctx, `SAVEPOINT target_binding_check`); err != nil {
			t.Fatal(err)
		}
		claimErr := insertClaim(uuid.New(), buildManifestID, rootManifestID, commandID)
		if claimErr == nil {
			t.Errorf("%s claim unexpectedly succeeded", name)
		} else if !strings.Contains(strings.ToLower(claimErr.Error()), strings.ToLower(expectedMessage)) {
			t.Errorf("%s claim error = %v, want %q", name, claimErr, expectedMessage)
		}
		if _, err := transaction.ExecContext(ctx, `ROLLBACK TO SAVEPOINT target_binding_check`); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `RELEASE SAVEPOINT target_binding_check`); err != nil {
			t.Fatal(err)
		}
	}
	assertClaimRejected("wrong same-project leaf", "target differs", wrongLeafID, rootID, validCommand.commandID)
	if _, err := transaction.ExecContext(ctx, `DELETE FROM application_build_manifests WHERE id = $1`, wrongLeafID); err != nil {
		t.Fatal(err)
	}
	assertClaimRejected("wrong definition version", "target differs", rootID, rootID, wrongDefinitionCommand.commandID)
	assertClaimRejected("unaccepted workflow proposal", "accepted same-project", rootID, rootID, pendingProposalCommand.commandID)
	assertClaimRejected("rejected command", "requires a pending command", rootID, rootID, rejectedCommand.commandID)

	claimID := uuid.New()
	if err := insertClaim(claimID, rootID, rootID, validCommand.commandID); err != nil {
		t.Fatalf("pending accepted command could not acquire a valid claim: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO implementation_proposals (
  id,project_id,build_manifest_id,status,version,content_store,content_ref,content_hash,payload_hash,
  operation_count,accepted_count,rejected_count,created_by,created_at,execution_source,
  conversation_command_id,instruction_hash,ai_provider,ai_model
) VALUES ($1,$2,$3,'open',1,'mongo',$4,$5,$6,1,0,0,$7,now(),
  'conversation_command',$1,$8,'test-provider','test-model'
)`, validCommand.commandID, projectID, rootID, "proposal-"+validCommand.commandID.String(),
		"sha256:"+strings.Repeat("7", 64), "sha256:"+strings.Repeat("8", 64), userID, instructionHash,
	); err != nil {
		t.Fatalf("create deterministic generated proposal: %v", err)
	}
	assertReceiptGateRejected := func(name, expectedMessage, query string, args ...any) {
		t.Helper()
		if _, err := transaction.ExecContext(ctx, `SAVEPOINT receipt_gate_check`); err != nil {
			t.Fatal(err)
		}
		_, gateErr := transaction.ExecContext(ctx, query, args...)
		if gateErr == nil {
			t.Errorf("%s unexpectedly succeeded before the command receipt", name)
		} else if !strings.Contains(strings.ToLower(gateErr.Error()), strings.ToLower(expectedMessage)) {
			t.Errorf("%s error = %v, want %q", name, gateErr, expectedMessage)
		}
		if _, err := transaction.ExecContext(ctx, `ROLLBACK TO SAVEPOINT receipt_gate_check`); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `RELEASE SAVEPOINT receipt_gate_check`); err != nil {
			t.Fatal(err)
		}
	}
	assertReceiptGateRejected(
		"proposal review transition",
		"cannot become reviewable before command receipt",
		`UPDATE implementation_proposals SET status = 'reviewing', version = version + 1 WHERE id = $1`,
		validCommand.commandID,
	)
	assertReceiptGateRejected(
		"operation decision",
		"decisions require a committed command receipt",
		`INSERT INTO implementation_operation_decisions (proposal_id,operation_id,decision,reason,decided_by) VALUES ($1,'operation-1','accepted','',$2)`,
		validCommand.commandID,
		userID,
	)
	if _, err := transaction.ExecContext(ctx, `UPDATE implementation_generation_claims SET
  status = 'completed', claim_token = NULL, claim_expires_at = NULL,
  completed_proposal_id = $1, updated_at = now()
WHERE id = $2`, validCommand.commandID, claimID); err != nil {
		t.Fatalf("complete claim while command is still pending: %v", err)
	}
	var commandStatus string
	if err := transaction.QueryRowContext(ctx, `SELECT status FROM conversation_commands WHERE id = $1`, validCommand.commandID).Scan(&commandStatus); err != nil {
		t.Fatal(err)
	}
	if commandStatus != "pending" {
		t.Fatalf("receipt-loss recovery fixture command status = %q, want pending", commandStatus)
	}
}
