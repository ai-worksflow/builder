package goldenfault

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresLedgerReservationUnknownTerminalAndReplay(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "golden_fault_ledger_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	database, err := sql.Open("pgx", goldenFaultPostgresSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, `
CREATE TABLE golden_fault_consume_reservations (
  authority_id uuid PRIMARY KEY,
  fixture_id uuid NOT NULL,
  run_id uuid NOT NULL,
  operation_kind text NOT NULL,
  resource_selector text NOT NULL,
  expected_fence_digest text NOT NULL,
  envelope_digest text NOT NULL,
  payload_digest text NOT NULL,
  predicate_digest text NOT NULL,
  authority_issued_at timestamptz NOT NULL,
  authority_expires_at timestamptz NOT NULL,
  signer_identities jsonb NOT NULL,
  resolved_resource_id text NOT NULL,
  resolved_head_digest text NOT NULL,
  resolved_fence_digest text NOT NULL,
  resolution_digest text NOT NULL,
  adapter_invocation_id uuid NOT NULL UNIQUE,
  reserved_at timestamptz NOT NULL
);
CREATE TABLE golden_fault_consume_results (
  authority_id uuid PRIMARY KEY REFERENCES golden_fault_consume_reservations(authority_id),
  result_id uuid NOT NULL UNIQUE,
  outcome text NOT NULL,
  adapter_result_digest text NOT NULL,
  observed_head_digest text NOT NULL,
  observed_fence_digest text NOT NULL,
  completed_at timestamptz NOT NULL,
  receipt_bytes bytea NOT NULL,
  receipt_document jsonb NOT NULL,
  receipt_digest text NOT NULL
);`); err != nil {
		t.Fatal(err)
	}
	ledger, err := NewPostgresLedger(database)
	if err != nil {
		t.Fatal(err)
	}
	now, err := ledger.TrustedTime(ctx)
	if err != nil || now.IsZero() {
		t.Fatalf("TrustedTime() = %s, %v", now, err)
	}
	resolution := ResourceResolution{
		ResourceID: "agent-runner/postgres-ledger", HeadDigest: testFaultDigest("pg-head-before"),
		FenceDigest: testFaultDigest("pg-fence-before"),
	}
	authorityID := uuid.New()
	resolutionDigest, err := hashResolution(authorityID, resolution)
	if err != nil {
		t.Fatal(err)
	}
	reservation := Reservation{
		AuthorityID: authorityID, FixtureID: uuid.New(), RunID: uuid.New(), OperationKind: OperationAgentRunnerCrash,
		ResourceSelector: "agent.runner", ExpectedFenceDigest: resolution.FenceDigest,
		EnvelopeDigest: testFaultDigest("pg-envelope"), PayloadDigest: testFaultDigest("pg-payload"),
		PredicateDigest: testFaultDigest("pg-predicate"), AuthorityIssuedAt: now.Add(-time.Minute),
		AuthorityExpiresAt: now.Add(10 * time.Minute), SignerIdentities: []string{"fault-operator@golden.example"},
		ResolvedResourceID: resolution.ResourceID, ResolvedHeadDigest: resolution.HeadDigest,
		ResolvedFenceDigest: resolution.FenceDigest, ResolutionDigest: resolutionDigest,
		AdapterInvocationID: uuid.New(), ReservedAt: now,
	}
	first, err := ledger.Reserve(ctx, reservation)
	if err != nil || first.State != ConsumeStateReserved || first.Idempotent {
		t.Fatalf("first Reserve() = record:%+v error:%v", first, err)
	}
	replayedReservation, err := ledger.Reserve(ctx, reservation)
	if err != nil || replayedReservation.State != ConsumeStateReserved || !replayedReservation.Idempotent {
		t.Fatalf("replayed Reserve() = record:%+v error:%v", replayedReservation, err)
	}
	inspected, err := ledger.Inspect(ctx, authorityID)
	if err != nil || inspected.State != ConsumeStateReserved || inspected.Terminal != nil {
		t.Fatalf("reserved Inspect() = record:%+v error:%v", inspected, err)
	}

	terminal, err := buildTerminalResult(reservation, AdapterResult{
		Outcome: AdapterOutcomeApplied, ResultDigest: testFaultDigest("pg-result"),
		ObservedHeadDigest: testFaultDigest("pg-head-after"), ObservedFenceDigest: testFaultDigest("pg-fence-after"),
	}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	committed, err := ledger.CommitTerminal(ctx, terminal)
	if err != nil || committed.State != ConsumeStateTerminal || committed.Terminal == nil || committed.Idempotent {
		t.Fatalf("CommitTerminal() = record:%+v error:%v", committed, err)
	}
	replayedTerminal, err := ledger.CommitTerminal(ctx, terminal)
	if err != nil || replayedTerminal.Terminal == nil || !replayedTerminal.Idempotent ||
		replayedTerminal.Terminal.ReceiptDigest != terminal.ReceiptDigest {
		t.Fatalf("replayed CommitTerminal() = record:%+v error:%v", replayedTerminal, err)
	}
	different := terminal
	different.ResultID = uuid.New()
	different.Receipt.ResultID = different.ResultID
	different.ReceiptJSON, _ = canonicalJSON(different.Receipt)
	different.ReceiptDigest = sha256Digest(different.ReceiptJSON)
	if _, err := ledger.CommitTerminal(ctx, different); !errors.Is(err, ErrConflict) {
		t.Fatalf("different terminal CAS error = %v", err)
	}
}

func goldenFaultPostgresSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}
