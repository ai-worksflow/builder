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

func TestVerificationOutputTruncationGateMigrationDeclaresFailClosedReceipts(t *testing.T) {
	up, err := files.ReadFile("000051_verification_output_truncation_gate.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000051_verification_output_truncation_gate.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"candidate_verification_check_truncation_shape",
		"canonical_verification_check_truncation_shape",
		"status <> 'passed' OR NOT truncated",
		"passed Candidate Receipt cannot contain a required check with truncated output evidence",
		"passed Canonical Receipt cannot contain a required check with truncated output evidence",
		"DEFERRABLE INITIALLY DEFERRED",
	} {
		if !strings.Contains(string(up), required) {
			t.Fatalf("verification truncation gate is missing %q", required)
		}
	}
	for _, required := range []string{
		"DROP FUNCTION IF EXISTS validate_candidate_receipt_truncation_gate()",
		"DROP FUNCTION IF EXISTS validate_canonical_receipt_truncation_gate()",
		"DROP COLUMN IF EXISTS truncated",
		"DROP CONSTRAINT IF EXISTS candidate_verification_check_truncation_shape",
	} {
		if !strings.Contains(string(down), required) {
			t.Fatalf("verification truncation rollback is missing %q", required)
		}
	}
}

func TestVerificationOutputTruncationGatePostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "verification_truncation_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
		t.Fatalf("migrations.Up failed: %v", err)
	}

	t.Run("Candidate", func(t *testing.T) {
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer transaction.Rollback()
		if _, err := transaction.ExecContext(ctx, `
ALTER TABLE candidate_verification_checks
DISABLE TRIGGER candidate_verification_check_insert_guard
`); err != nil {
			t.Fatal(err)
		}
		_, err = transaction.ExecContext(ctx, `
INSERT INTO candidate_verification_checks (
  receipt_id, run_id, ordinal, check_id, kind, required, status, attempt_id,
  verifier_image_digest, argv, working_directory, exit_code,
  started_at, completed_at, duration_ms, attempt_count,
  truncated, redaction_count, oracle_ids, acceptance_criterion_ids,
  obligation_ids, diagnostics
) VALUES (
  $1, $2, 0, 'required-check', 'contract', true, 'passed', $3,
  $4, '["check"]'::jsonb, '.', 0,
  statement_timestamp(), statement_timestamp(), 0, 1,
  true, 0, '[]'::jsonb, '[]'::jsonb, '[]'::jsonb, '[]'::jsonb
)
`, uuid.New(), uuid.New(), uuid.New(),
			"registry.example/check@sha256:"+strings.Repeat("a", 64))
		if err == nil || !strings.Contains(err.Error(), "candidate_verification_check_truncation_shape") {
			t.Fatalf("passed Candidate check with truncated evidence reached Receipt commit: %v", err)
		}
	})

	t.Run("Canonical", func(t *testing.T) {
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer transaction.Rollback()
		_, err = transaction.ExecContext(ctx, `
INSERT INTO canonical_verification_checks (
  receipt_id, ordinal, check_id, kind, required, status, attempt_id, truncated
) VALUES ($1, 0, 'required-check', 'contract', true, 'passed', $2, true)
`, uuid.New(), uuid.New())
		if err == nil || !strings.Contains(err.Error(), "canonical_verification_check_truncation_shape") {
			t.Fatalf("passed Canonical check with truncated evidence reached Receipt commit: %v", err)
		}
	})
}
