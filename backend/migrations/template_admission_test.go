package migrations

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTemplateAdmissionMigrationDeclaresExactImmutableSupplyChainIdentities(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000021_template_admission.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000021_template_admission.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TABLE template_admission_attempts",
		"CREATE TABLE template_releases",
		"CREATE TABLE template_release_policies",
		"CREATE TABLE full_stack_template_releases",
		"CREATE TABLE full_stack_template_components",
		"template_releases_exact_identity_unique UNIQUE (id, content_hash)",
		"full_stack_template_exact_identity_unique UNIQUE (id, content_hash)",
		"template_release_policy_exact_release_fk",
		"full_stack_template_component_release_fk",
		"full_stack_template_component_stack_fk",
		"template_release_immutable",
		"full_stack_template_release_immutable",
		"template_admission_attempt_guard",
		"full_stack_template_complete",
		"template release content is immutable and cannot be updated or deleted",
		"full-stack template release content is immutable and cannot be updated or deleted",
		"'ai_runtime_contract', 'deployment_contract'",
		"'verification_contract'",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("template admission migration is missing %q", expected)
		}
	}
	for _, gate := range []string{
		"source_identity", "manifest_schema", "license_spdx", "dependency_lock",
		"registry_policy", "install", "lint", "typecheck", "unit_test", "build",
		"start_health", "contract_smoke", "container_build", "secret_scan", "sbom",
		"vulnerability", "signature_attestation",
	} {
		if !strings.Contains(text, "'"+gate+"'") {
			t.Fatalf("required admission gate %q is not enforced by the migration", gate)
		}
	}
	downText := string(down)
	for _, expected := range []string{
		"DROP TABLE IF EXISTS full_stack_template_components",
		"DROP TABLE IF EXISTS full_stack_template_releases",
		"DROP TABLE IF EXISTS template_release_policies",
		"DROP TABLE IF EXISTS template_releases",
		"DROP TABLE IF EXISTS template_admission_attempts",
		"ALTER TABLE artifacts DROP CONSTRAINT artifacts_kind_check",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("template admission rollback is missing %q", expected)
		}
	}
	for _, addedKind := range []string{"ai_runtime_contract", "deployment_contract", "verification_contract"} {
		if strings.Contains(downText, "'"+addedKind+"'") {
			t.Fatalf("rollback did not restore the prior artifact kind set: %s remains", addedKind)
		}
	}
}

func TestTemplateAdmissionMigrationPostgresCanary(t *testing.T) {
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
	schema := "template_admission_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	for _, table := range []string{
		"template_admission_attempts",
		"template_releases",
		"template_release_policies",
		"full_stack_template_releases",
		"full_stack_template_components",
	} {
		var actual string
		if err := database.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, table).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != table {
			t.Fatalf("expected %s table, got %q", table, actual)
		}
	}
	var triggerCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_trigger
WHERE NOT tgisinternal
  AND tgrelid IN (
    'template_admission_attempts'::regclass,
    'template_releases'::regclass,
    'template_release_policies'::regclass,
    'full_stack_template_releases'::regclass,
    'full_stack_template_components'::regclass
  )
  AND tgname IN (
    'template_admission_attempt_guard',
    'template_release_exact_admission',
    'template_release_immutable',
    'template_release_policy_guard',
    'full_stack_template_component_guard',
    'full_stack_template_complete',
    'full_stack_template_release_immutable'
  )
`).Scan(&triggerCount); err != nil {
		t.Fatal(err)
	}
	if triggerCount != 7 {
		t.Fatalf("expected seven template invariant triggers, got %d", triggerCount)
	}

	userID, projectID, artifactID, attemptID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'Template admission owner', 'not-used')
`, userID, "template-admission-"+uuid.NewString()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by) VALUES ($1, 'Template admission canary', $2)
`, projectID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO artifacts (id, project_id, kind, artifact_key, title, created_by)
VALUES ($1, $2, 'ai_runtime_contract', 'AI-RUNTIME-CANARY', 'AI runtime contract', $3)
`, artifactID, projectID, userID); err != nil {
		t.Fatalf("new contract artifact kind is not accepted: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO template_admission_attempts (
  id, schema_version, status, version, source, manifest,
  sbom_digest, license_expression, license_digest, subject_hash,
  requested_by
) VALUES (
  $1, 'template-admission-attempt/v1', 'candidate', 1,
  '{"repository":"https://github.com/ai-worksflow/templates.git","branch":"canary","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","treeHash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}'::jsonb,
  '{"schemaVersion":"template-manifest/v1","templateId":"canary","version":"1.0.0"}'::jsonb,
  'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
  'Apache-2.0',
  'sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',
  'sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
  $2
)
`, attemptID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM template_admission_attempts WHERE id = $1`, attemptID); err == nil {
		t.Fatal("append-only admission attempt was deleted")
	}
	var rows int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM template_admission_attempts WHERE id = $1`, attemptID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatal("failed admission delete did not preserve the audit row")
	}
}
