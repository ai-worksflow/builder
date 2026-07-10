package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestDocumentCollaborationMigrationDeclaresTenantAndLineageGuards(t *testing.T) {
	up, err := files.ReadFile("000014_document_collaboration.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000014_document_collaboration.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"CREATE TABLE artifact_collaboration_states",
		"CREATE TABLE artifact_member_bindings",
		"CREATE TABLE document_generation_commands",
		"validate_artifact_collaboration_tenant_refs",
		"validate_artifact_member_binding_tenant_refs",
		"prevent_artifact_member_binding_identity_mutation",
		"validate_artifact_member_binding_owner",
		"validate_document_generation_command_refs",
		"prevent_document_generation_command_identity_mutation",
		"document_generation_commands_completion_check",
		"document_generation_commands_attempt_count_check",
		"document_generation_commands_failure_shape_check",
		"ai_provider",
		"ai_model",
	} {
		if !strings.Contains(string(up), expected) {
			t.Fatalf("document collaboration migration is missing %q", expected)
		}
	}
	platform, err := files.ReadFile("000001_platform.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"'owner'", "'assignee'", "'downstream_owner'", "'reviewer'", "'watcher'"} {
		if !strings.Contains(string(platform), role) {
			t.Fatalf("artifact responsibility role constraint is missing %s", role)
		}
	}
	for _, expected := range []string{
		"DROP FUNCTION IF EXISTS prevent_document_generation_command_identity_mutation",
		"DROP FUNCTION IF EXISTS validate_document_generation_command_refs",
		"DROP FUNCTION IF EXISTS validate_artifact_member_binding_owner",
		"DROP FUNCTION IF EXISTS prevent_artifact_member_binding_identity_mutation",
		"DROP FUNCTION IF EXISTS validate_artifact_member_binding_tenant_refs",
		"DROP FUNCTION IF EXISTS validate_artifact_collaboration_tenant_refs",
		"DROP TABLE IF EXISTS document_generation_commands",
		"DROP TABLE IF EXISTS artifact_member_bindings",
	} {
		if !strings.Contains(string(down), expected) {
			t.Fatalf("document collaboration rollback is missing %q", expected)
		}
	}
}

func TestDocumentCollaborationTenantInvariantPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "document_collaboration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	userA, userB, projectA, projectB := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for index, userID := range []uuid.UUID{userA, userB} {
		if _, err := database.ExecContext(ctx, `INSERT INTO users (id,email,display_name,password_hash) VALUES ($1,$2,$3,'not-used')`,
			userID, "collab-canary-"+uuid.NewString()+"@example.com", "Canary "+string(rune('A'+index))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO projects (id,name,description,created_by) VALUES ($1,'A','',$2),($3,'B','',$4)`, projectA, userA, projectB, userB); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO project_members (project_id,user_id,role,joined_at,updated_at) VALUES ($1,$2,'owner',now(),now()),($3,$4,'owner',now(),now())`, projectA, userA, projectB, userB); err != nil {
		t.Fatal(err)
	}
	artifactA, artifactB := uuid.New(), uuid.New()
	if _, err := database.ExecContext(ctx, `INSERT INTO artifacts (id,project_id,kind,artifact_key,title,created_by) VALUES ($1,$2,'product_requirements','A-DOC','A',$3),($4,$5,'api_contract','B-DOC','B',$6)`, artifactA, projectA, userA, artifactB, projectB, userB); err != nil {
		t.Fatal(err)
	}
	revisionB := uuid.New()
	if _, err := database.ExecContext(ctx, `INSERT INTO artifact_revisions (id,artifact_id,revision_number,schema_version,content_ref,content_hash,workflow_status,change_source,created_by) VALUES ($1,$2,1,1,'fixture','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','approved','human',$3)`, revisionB, artifactB, userB); err != nil {
		t.Fatal(err)
	}

	_, err = database.ExecContext(ctx, `INSERT INTO artifact_collaboration_states (artifact_id,project_id,version,updated_by) VALUES ($1,$2,1,$3)`, artifactA, projectB, userB)
	assertPostgresCode(t, err, "23503", "cross-project collaboration state")
	_, err = database.ExecContext(ctx, `INSERT INTO artifact_member_bindings (artifact_id,project_id,user_id,role,assigned_by) VALUES ($1,$2,$3,'owner',$3)`, artifactA, projectB, userB)
	assertPostgresCode(t, err, "23503", "cross-project member binding")
	_, err = database.ExecContext(ctx, `INSERT INTO document_generation_commands (id,project_id,actor_id,command_key,request_hash,source_bindings_etag,resolved_owner_ids,target_artifact_id,base_revision_id) VALUES ($1,$2,$3::uuid,'cross-tenant-command','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','"bindings:v0"',jsonb_build_array(($3::uuid)::text),$4,$5)`, uuid.New(), projectA, userA, artifactB, revisionB)
	assertPostgresCode(t, err, "23503", "cross-project generation command")

	commandID := uuid.New()
	if _, err := database.ExecContext(ctx, `INSERT INTO document_generation_commands (id,project_id,actor_id,command_key,request_hash,source_bindings_etag,resolved_owner_ids) VALUES ($1,$2,$3::uuid,'stable-command','sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc','"bindings:v0"',jsonb_build_array(($3::uuid)::text))`, commandID, projectA, userA); err != nil {
		t.Fatal(err)
	}
	_, err = database.ExecContext(ctx, `UPDATE document_generation_commands SET command_key='mutated-command' WHERE id=$1`, commandID)
	assertPostgresCode(t, err, "55000", "generation command identity mutation")
	if _, err := database.ExecContext(ctx, `INSERT INTO artifact_member_bindings (artifact_id,project_id,user_id,role,assigned_by) VALUES ($1,$2,$3,'owner',$3)`, artifactA, projectA, userA); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO artifact_collaboration_states (artifact_id,project_id,version,updated_by) VALUES ($1,$2,1,$3)`, artifactA, projectA, userA); err != nil {
		t.Fatal(err)
	}
	_, err = database.ExecContext(ctx, `UPDATE artifact_member_bindings SET role='watcher' WHERE artifact_id=$1 AND role='owner'`, artifactA)
	assertPostgresCode(t, err, "55000", "member binding identity mutation")
	_, err = database.ExecContext(ctx, `DELETE FROM artifact_member_bindings WHERE artifact_id=$1 AND role='owner'`, artifactA)
	assertPostgresCode(t, err, "23514", "removing the last persisted owner")

	var triggers int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_trigger AS trigger
JOIN pg_class AS relation ON relation.oid = trigger.tgrelid
JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE NOT trigger.tgisinternal
  AND namespace.nspname = current_schema()
  AND tgname IN (
    'artifact_collaboration_tenant_refs',
    'artifact_member_binding_tenant_refs',
    'artifact_member_binding_identity_immutable',
    'artifact_member_binding_owner_required',
    'document_generation_command_refs',
    'document_generation_command_identity_immutable'
  )`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if triggers != 6 {
		t.Fatalf("expected six collaboration invariant triggers, got %d", triggers)
	}
}

func assertPostgresCode(t *testing.T, err error, code, operation string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s unexpectedly succeeded", operation)
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != code {
		t.Fatalf("%s error=%v code=%q", operation, err, postgresErrorCode(postgresError))
	}
}

func postgresErrorCode(err *pgconn.PgError) string {
	if err == nil {
		return ""
	}
	return err.Code
}
