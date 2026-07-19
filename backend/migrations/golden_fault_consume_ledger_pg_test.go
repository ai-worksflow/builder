package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestGoldenFaultConsumeLedgerPostgresCASAndAppendOnly(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	if _, err := base.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		t.Fatal(err)
	}
	schema := "golden_fault_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	up, err := files.ReadFile("000068_golden_fault_consume_ledger.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		t.Fatalf("apply Golden fault ledger migration: %v", err)
	}

	authorityID, fixtureID, runID := uuid.New(), uuid.New(), uuid.New()
	invocationID, resultID := uuid.New(), uuid.New()
	issuedAt := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	reservedAt := issuedAt.Add(time.Minute)
	completedAt := reservedAt.Add(time.Second)
	expiresAt := issuedAt.Add(10 * time.Minute)
	expectedFence := goldenFaultMigrationDigest("expected-fence")
	values := []any{
		authorityID, fixtureID, runID, "agent-runner-crash", "agent.runner",
		expectedFence, goldenFaultMigrationDigest("envelope"), goldenFaultMigrationDigest("payload"),
		goldenFaultMigrationDigest("predicate"), issuedAt, expiresAt, `["fault-operator@golden.example"]`,
		"agent-runner/session-42", goldenFaultMigrationDigest("head-before"), expectedFence,
		goldenFaultMigrationDigest("resolution"), invocationID, reservedAt,
	}
	result, err := database.ExecContext(ctx, `
INSERT INTO golden_fault_consume_reservations (
  authority_id, fixture_id, run_id, operation_kind, resource_selector,
  expected_fence_digest, envelope_digest, payload_digest, predicate_digest,
  authority_issued_at, authority_expires_at, signer_identities,
  resolved_resource_id, resolved_head_digest, resolved_fence_digest,
  resolution_digest, adapter_invocation_id, reserved_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb,
  $13, $14, $15, $16, $17, $18
)
ON CONFLICT (authority_id) DO NOTHING`, values...)
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		t.Fatalf("first reservation affected %d rows, want 1", affected)
	}
	result, err = database.ExecContext(ctx, `
INSERT INTO golden_fault_consume_reservations (
  authority_id, fixture_id, run_id, operation_kind, resource_selector,
  expected_fence_digest, envelope_digest, payload_digest, predicate_digest,
  authority_issued_at, authority_expires_at, signer_identities,
  resolved_resource_id, resolved_head_digest, resolved_fence_digest,
  resolution_digest, adapter_invocation_id, reserved_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb,
  $13, $14, $15, $16, $17, $18
)
ON CONFLICT (authority_id) DO NOTHING`, values...)
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := result.RowsAffected(); affected != 0 {
		t.Fatalf("duplicate reservation affected %d rows, want 0", affected)
	}
	if _, err := database.ExecContext(ctx,
		`UPDATE golden_fault_consume_reservations SET resolved_resource_id = 'agent-runner/other' WHERE authority_id = $1`,
		authorityID,
	); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("reservation UPDATE error = %v, want append-only rejection", err)
	}

	adapterResult := goldenFaultMigrationDigest("adapter-result")
	observedHead := goldenFaultMigrationDigest("head-after")
	observedFence := goldenFaultMigrationDigest("fence-after")
	receipt := map[string]any{
		"adapterInvocationId": invocationID.String(), "adapterResultDigest": adapterResult,
		"authorityId": authorityID.String(), "completedAt": goldenFaultMigrationTime(completedAt),
		"envelopeDigest": values[6], "expectedFenceDigest": expectedFence, "fixtureId": fixtureID.String(),
		"observedFenceDigest": observedFence, "observedHeadDigest": observedHead,
		"operationKind": "agent-runner-crash", "outcome": "applied", "payloadDigest": values[7],
		"predicateDigest": values[8], "reservedAt": goldenFaultMigrationTime(reservedAt),
		"resolutionDigest": values[15], "resolvedFenceDigest": expectedFence,
		"resolvedHeadDigest": values[13], "resolvedResourceId": values[12],
		"resourceSelector": "agent.runner", "resultId": resultID.String(), "runId": runID.String(),
		"schemaVersion": "worksflow-golden-fault-consume-receipt/v1",
	}
	receiptBytes, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptDigest := goldenFaultMigrationDigestBytes(receiptBytes)
	result, err = database.ExecContext(ctx, `
INSERT INTO golden_fault_consume_results (
  authority_id, result_id, outcome, adapter_result_digest,
  observed_head_digest, observed_fence_digest, completed_at,
  receipt_bytes, receipt_document, receipt_digest
) VALUES ($1, $2, 'applied', $3, $4, $5, $6, $7, $8::jsonb, $9)
ON CONFLICT (authority_id) DO NOTHING`,
		authorityID, resultID, adapterResult, observedHead, observedFence, completedAt,
		receiptBytes, string(receiptBytes), receiptDigest,
	)
	if err != nil {
		t.Fatalf("insert terminal result: %v", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		t.Fatalf("first terminal result affected %d rows, want 1", affected)
	}
	if _, err := database.ExecContext(ctx,
		`DELETE FROM golden_fault_consume_results WHERE authority_id = $1`, authorityID,
	); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("terminal DELETE error = %v, want append-only rejection", err)
	}
	var reservations, terminalResults int
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM golden_fault_consume_reservations WHERE authority_id = $1),
  (SELECT count(*) FROM golden_fault_consume_results WHERE authority_id = $1)`, authorityID,
	).Scan(&reservations, &terminalResults); err != nil {
		t.Fatal(err)
	}
	if reservations != 1 || terminalResults != 1 {
		t.Fatalf("ledger closure = reservations:%d results:%d, want 1/1", reservations, terminalResults)
	}

	down, err := files.ReadFile("000068_golden_fault_consume_ledger.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("rollback Golden fault ledger: %v", err)
	}
}

func goldenFaultMigrationTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func goldenFaultMigrationDigest(seed string) string {
	return goldenFaultMigrationDigestBytes([]byte(seed))
}

func goldenFaultMigrationDigestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
