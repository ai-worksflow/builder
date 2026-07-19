package qualificationevidence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresQualificationEvidenceStoreRealPostgresClosure(t *testing.T) {
	database, store, cleanup := openPostgresQualificationEvidenceTestStore(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	t.Run("complete Execute exact replay and restart", func(t *testing.T) {
		plan, trust := uniquePostgresQualificationEvidencePlan(t), testTrust()
		authorities := newPostgresQualificationEvidenceAuthorities(t, store, plan, trust)
		service := newPostgresQualificationEvidenceService(t, store, plan, trust, authorities)
		result, err := service.executePlan(ctx, plan)
		if err != nil || result.OrchestrationID != plan.OrchestrationID {
			var postgresError *pgconn.PgError
			if errors.As(err, &postgresError) {
				t.Fatalf("Execute() = %#v, %v detail=%s where=%s", result, err, postgresError.Detail, postgresError.Where)
			}
			t.Fatalf("Execute() = %#v, %v", result, err)
		}
		snapshot, err := store.Load(ctx, plan.OrchestrationID)
		if err != nil || snapshot.Phase != PhaseComplete || snapshot.Version != 20 || snapshot.Verification == nil {
			t.Fatalf("final snapshot = %#v, %v", snapshot, err)
		}
		events, err := store.Events(ctx, plan.OrchestrationID)
		if err != nil || len(events) != 20 {
			t.Fatalf("event ledger length = %d, %v", len(events), err)
		}
		for index, event := range events {
			if event.EventID == "" || event.At == "" {
				t.Fatalf("event %d lacks DB authority: %#v", index, event)
			}
			if _, err := parseCanonicalTime(event.At); err != nil {
				t.Fatalf("event %d DB time is not canonical: %v", index, err)
			}
		}
		last := events[len(events)-1]
		replayed, err := store.Append(ctx, plan.OrchestrationID, snapshot.Version-1, last)
		if err != nil || replayed.Version != snapshot.Version || replayed.LastEventID != last.EventID {
			t.Fatalf("advanced-head exact EventID replay = %#v, %v", replayed, err)
		}
		drift := cloneEvent(last)
		drift.OperationID = plan.Operations.ReceiptSign
		if _, err := store.Append(ctx, plan.OrchestrationID, snapshot.Version-1, drift); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("same EventID drift error = %v", err)
		}

		before := authorities.calls
		restartedStore, err := NewPostgresStore(database)
		if err != nil {
			t.Fatal(err)
		}
		restarted := newPostgresQualificationEvidenceService(t, restartedStore, plan, trust, authorities)
		if _, err := restarted.executePlan(ctx, plan); err != nil {
			t.Fatalf("restart replay Execute() = %v", err)
		}
		if authorities.calls != before {
			t.Fatalf("completed restart repeated an authority mutation: before=%#v after=%#v", before, authorities.calls)
		}
		var forbiddenCount int
		if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM qualification_evidence_events
WHERE convert_from(event_bytes,'UTF8') ~ '(raw-token|session-cookie|Authorization|/tmp/credential|private key)'`).Scan(&forbiddenCount); err != nil {
			t.Fatal(err)
		}
		if forbiddenCount != 0 {
			t.Fatalf("non-secret event ledger contains %d forbidden bearer values", forbiddenCount)
		}
		var materialDrift int
		if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM qualification_evidence_events
WHERE qualification_evidence_sha256(request_bytes) <> request_hash
   OR qualification_evidence_sha256(event_bytes) <> event_hash
   OR convert_from(request_bytes,'UTF8')::jsonb <> request_document
   OR convert_from(event_bytes,'UTF8')::jsonb <> event_document`).Scan(&materialDrift); err != nil {
			t.Fatal(err)
		}
		if materialDrift != 0 {
			t.Fatalf("stored raw material drift rows = %d", materialDrift)
		}
		var operationCount, artifactBindingDrift int
		if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM qualification_evidence_operations
WHERE orchestration_id=$1`, plan.OrchestrationID).Scan(&operationCount); err != nil {
			t.Fatal(err)
		}
		if operationCount != 10 {
			t.Fatalf("reserved operation closure count = %d", operationCount)
		}
		if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM qualification_evidence_events AS event
JOIN qualification_evidence_operations AS operation USING (operation_id,orchestration_id)
WHERE event.orchestration_id=$1
  AND event.event_kind IN ('encryption-started','encryption-committed')
  AND event.active_artifact_id <> operation.artifact_id`, plan.OrchestrationID).Scan(&artifactBindingDrift); err != nil {
			t.Fatal(err)
		}
		if artifactBindingDrift != 0 {
			t.Fatalf("active artifact/operation binding drift rows = %d", artifactBindingDrift)
		}
	})

	t.Run("cross connection Create and version CAS", func(t *testing.T) {
		plan, trust := uniquePostgresQualificationEvidencePlan(t), testTrust()
		base := postgresQualificationEvidenceReservation(t, store, plan, trust)
		var created, reused, unexpected atomic.Int64
		var wait sync.WaitGroup
		for range 16 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				event := cloneEvent(base)
				event.EventID = uuid.NewString()
				_, won, err := store.Create(ctx, plan.OrchestrationID, event)
				switch {
				case err != nil:
					unexpected.Add(1)
				case won:
					created.Add(1)
				default:
					reused.Add(1)
				}
			}()
		}
		wait.Wait()
		if created.Load() != 1 || reused.Load() != 15 || unexpected.Load() != 0 {
			t.Fatalf("concurrent Create created=%d reused=%d unexpected=%d", created.Load(), reused.Load(), unexpected.Load())
		}
		snapshot, err := store.Load(ctx, plan.OrchestrationID)
		if err != nil || snapshot.Version != 1 {
			t.Fatalf("reserved concurrent snapshot = %#v, %v", snapshot, err)
		}
		left := Event{
			At: postgresQualificationEvidenceNow(t, store), EventID: uuid.NewString(),
			Kind: EventCredentialIssueStarted, OperationID: plan.Operations.CredentialIssue,
		}
		right := cloneEvent(left)
		right.EventID = uuid.NewString()
		outcomes := make(chan error, 2)
		for _, event := range []Event{left, right} {
			wait.Add(1)
			go func(candidate Event) {
				defer wait.Done()
				_, appendErr := store.Append(ctx, plan.OrchestrationID, 1, candidate)
				outcomes <- appendErr
			}(event)
		}
		wait.Wait()
		close(outcomes)
		var appended, conflicts int
		for outcome := range outcomes {
			switch {
			case outcome == nil:
				appended++
			case errors.Is(outcome, ErrCASConflict):
				conflicts++
			default:
				t.Fatalf("unexpected concurrent append error: %v", outcome)
			}
		}
		if appended != 1 || conflicts != 1 {
			t.Fatalf("CAS outcomes appended=%d conflicts=%d", appended, conflicts)
		}

		foreign := uniquePostgresQualificationEvidencePlan(t)
		foreign.Operations.CredentialIssue = plan.Operations.CredentialIssue
		if err := ValidatePlan(foreign); err != nil {
			t.Fatal(err)
		}
		foreignEvent := postgresQualificationEvidenceReservation(t, store, foreign, trust)
		if _, _, err := store.Create(ctx, foreign.OrchestrationID, foreignEvent); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("globally reused OperationID error = %v", err)
		}
	})

	t.Run("database time is sampled after waiting append locks", func(t *testing.T) {
		plan, trust := uniquePostgresQualificationEvidencePlan(t), testTrust()
		blocker, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := blocker.ExecContext(ctx, `LOCK TABLE qualification_evidence_heads IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			_ = blocker.Rollback()
			t.Fatal(err)
		}
		event := postgresQualificationEvidenceReservation(t, store, plan, trust)
		result := make(chan struct {
			snapshot Snapshot
			err      error
		}, 1)
		go func() {
			snapshot, _, createErr := store.Create(ctx, plan.OrchestrationID, event)
			result <- struct {
				snapshot Snapshot
				err      error
			}{snapshot: snapshot, err: createErr}
		}()
		time.Sleep(150 * time.Millisecond)
		var releaseTime time.Time
		if err := blocker.QueryRowContext(ctx, `SELECT date_trunc('milliseconds',clock_timestamp())`).Scan(&releaseTime); err != nil {
			_ = blocker.Rollback()
			t.Fatal(err)
		}
		if err := blocker.Commit(); err != nil {
			t.Fatal(err)
		}
		outcome := <-result
		observed, parseErr := parseCanonicalTime(outcome.snapshot.LastEventAt)
		if outcome.err != nil || parseErr != nil || observed.Before(releaseTime.UTC()) {
			t.Fatalf("post-lock DB event time=%s release=%s errors=%v/%v", outcome.snapshot.LastEventAt, releaseTime, outcome.err, parseErr)
		}
	})

	t.Run("Load rejects a drifted head while Events remains ledger-derived", func(t *testing.T) {
		plan, trust := uniquePostgresQualificationEvidencePlan(t), testTrust()
		event := postgresQualificationEvidenceReservation(t, store, plan, trust)
		if _, _, err := store.Create(ctx, plan.OrchestrationID, event); err != nil {
			t.Fatal(err)
		}
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO qualification_evidence_projection_authorizations(transaction_id,backend_pid)
VALUES (txid_current(),pg_backend_pid())`); err != nil {
			_ = transaction.Rollback()
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `
UPDATE qualification_evidence_heads SET phase='complete' WHERE orchestration_id=$1`, plan.OrchestrationID); err != nil {
			_ = transaction.Rollback()
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `
DELETE FROM qualification_evidence_projection_authorizations
WHERE transaction_id=txid_current() AND backend_pid=pg_backend_pid()`); err != nil {
			_ = transaction.Rollback()
			t.Fatal(err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(ctx, plan.OrchestrationID); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("drifted head Load error = %v", err)
		}
		events, err := store.Events(ctx, plan.OrchestrationID)
		if err != nil || len(events) != 1 || events[0].Kind != EventReserved {
			t.Fatalf("immutable ledger Events after head drift = %#v, %v", events, err)
		}
	})

	t.Run("started event survives restart as Inspect-only", func(t *testing.T) {
		plan, trust := uniquePostgresQualificationEvidencePlan(t), testTrust()
		authorities := newPostgresQualificationEvidenceAuthorities(t, store, plan, trust)
		authorities.preMutation = func(kind, _ string) error {
			if kind == "issue" {
				return errors.New("external issue call outcome unavailable")
			}
			return nil
		}
		service := newPostgresQualificationEvidenceService(t, store, plan, trust, authorities)
		if _, err := service.executePlan(ctx, plan); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("first started Execute error = %v", err)
		}
		snapshot, err := store.Load(ctx, plan.OrchestrationID)
		if err != nil || snapshot.Phase != PhaseCredentialIssueStarted {
			t.Fatalf("durable started state = %#v, %v", snapshot, err)
		}
		authorities.preMutation = nil
		restartedStore, _ := NewPostgresStore(database)
		restarted := newPostgresQualificationEvidenceService(t, restartedStore, plan, trust, authorities)
		if _, err := restarted.executePlan(ctx, plan); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("Inspect-only restart error = %v", err)
		}
		if authorities.calls.issue != 1 || authorities.calls.inspectIssue < 1 {
			t.Fatalf("restart repeated issue instead of Inspect: %#v", authorities.calls)
		}
	})

	t.Run("store commit unknown reconciles exact EventID", func(t *testing.T) {
		for _, commit := range []bool{true, false} {
			t.Run(fmt.Sprintf("commit-%t", commit), func(t *testing.T) {
				plan, trust := uniquePostgresQualificationEvidencePlan(t), testTrust()
				authorities := newPostgresQualificationEvidenceAuthorities(t, store, plan, trust)
				ambiguous := &ambiguousStore{delegate: store, kind: EventEncryptionCommitted, commit: commit}
				service := newPostgresQualificationEvidenceService(t, ambiguous, plan, trust, authorities)
				_, firstErr := service.executePlan(ctx, plan)
				if commit {
					if firstErr != nil {
						t.Fatal(firstErr)
					}
				} else {
					if !errors.Is(firstErr, ErrOutcomeUnknown) {
						t.Fatalf("uncommitted unknown error = %v", firstErr)
					}
					if _, err := service.executePlan(ctx, plan); err != nil {
						t.Fatal(err)
					}
				}
				if authorities.calls.encrypt != 2 {
					t.Fatalf("unknown store outcome repeated encryption: %#v", authorities.calls)
				}
				if !commit && authorities.calls.inspectEncrypt < 1 {
					t.Fatal("uncommitted completion did not recover through Inspect")
				}
			})
		}
	})

	assertPostgresQualificationEvidenceSecurity(t, ctx, database, store)
	assertPostgresQualificationEvidenceDownFence(t, ctx, database)
}

func TestPostgresQualificationEvidenceDownWriterRace(t *testing.T) {
	t.Run("queued append commits before rollback observes emptiness", func(t *testing.T) {
		database, _, cleanup := openPostgresQualificationEvidenceTestStore(t)
		defer cleanup()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		schema := postgresQualificationEvidenceCurrentSchema(t, ctx, database)
		writerDatabase := openPostgresQualificationEvidenceDedicatedDatabase(t, ctx, schema)
		downDatabase := openPostgresQualificationEvidenceDedicatedDatabase(t, ctx, schema)
		writerStore, err := NewPostgresStore(writerDatabase)
		if err != nil {
			t.Fatal(err)
		}
		writerPID := postgresQualificationEvidenceBackendPID(t, ctx, writerDatabase)
		downPID := postgresQualificationEvidenceBackendPID(t, ctx, downDatabase)
		eventsOID := postgresQualificationEvidenceEventsOID(t, ctx, database)
		down := postgresQualificationEvidenceDownMigration(t)

		plan, trust := uniquePostgresQualificationEvidencePlan(t), testTrust()
		reservation := postgresQualificationEvidenceReservation(t, writerStore, plan, trust)
		blocker, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		if _, err := blocker.ExecContext(ctx, `LOCK TABLE qualification_evidence_events IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}

		type createResult struct {
			snapshot Snapshot
			created  bool
			err      error
		}
		writerResult := make(chan createResult, 1)
		go func() {
			snapshot, created, createErr := writerStore.Create(ctx, plan.OrchestrationID, reservation)
			writerResult <- createResult{snapshot: snapshot, created: created, err: createErr}
		}()
		waitForPostgresQualificationEvidenceLock(t, ctx, database, writerPID, eventsOID, "ShareRowExclusiveLock")

		downResult := make(chan error, 1)
		go func() {
			_, downErr := downDatabase.ExecContext(ctx, down)
			downResult <- downErr
		}()
		waitForPostgresQualificationEvidenceLock(t, ctx, database, downPID, eventsOID, "AccessExclusiveLock")

		if err := blocker.Commit(); err != nil {
			t.Fatal(err)
		}
		writer := receivePostgresQualificationEvidenceCreateResult(t, ctx, writerResult)
		if writer.err != nil || !writer.created || writer.snapshot.Version != 1 {
			t.Fatalf("first-queued append result = %#v", writer)
		}
		downErr := receivePostgresQualificationEvidenceError(t, ctx, downResult)
		if downErr == nil || !strings.Contains(downErr.Error(), "cannot roll back Qualification Evidence store while immutable audit state is nonempty") {
			t.Fatalf("rollback after committed append error = %v", downErr)
		}
	})

	t.Run("committed empty rollback prevents a waiting append", func(t *testing.T) {
		database, _, cleanup := openPostgresQualificationEvidenceTestStore(t)
		defer cleanup()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		schema := postgresQualificationEvidenceCurrentSchema(t, ctx, database)
		writerDatabase := openPostgresQualificationEvidenceDedicatedDatabase(t, ctx, schema)
		writerStore, err := NewPostgresStore(writerDatabase)
		if err != nil {
			t.Fatal(err)
		}
		writerPID := postgresQualificationEvidenceBackendPID(t, ctx, writerDatabase)
		eventsOID := postgresQualificationEvidenceEventsOID(t, ctx, database)
		down := postgresQualificationEvidenceDownMigration(t)
		plan, trust := uniquePostgresQualificationEvidencePlan(t), testTrust()
		reservation := postgresQualificationEvidenceReservation(t, writerStore, plan, trust)

		downTransaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer downTransaction.Rollback()
		if _, err := downTransaction.ExecContext(ctx, down); err != nil {
			t.Fatal(err)
		}

		type createResult struct {
			snapshot Snapshot
			created  bool
			err      error
		}
		writerResult := make(chan createResult, 1)
		go func() {
			snapshot, created, createErr := writerStore.Create(ctx, plan.OrchestrationID, reservation)
			writerResult <- createResult{snapshot: snapshot, created: created, err: createErr}
		}()
		waitForPostgresQualificationEvidenceLock(t, ctx, database, writerPID, eventsOID, "AccessShareLock")

		if err := downTransaction.Commit(); err != nil {
			t.Fatal(err)
		}
		writer := receivePostgresQualificationEvidenceCreateResult(t, ctx, writerResult)
		if writer.err == nil || writer.created || writer.snapshot.Version != 0 {
			t.Fatalf("append after committed rollback result = %#v", writer)
		}
		var eventsDropped bool
		if err := database.QueryRowContext(ctx, `SELECT to_regclass($1) IS NULL`, schema+".qualification_evidence_events").Scan(&eventsDropped); err != nil {
			t.Fatal(err)
		}
		if !eventsDropped {
			t.Fatal("rollback committed without dropping the empty Qualification Evidence ledger")
		}
	})
}

func uniquePostgresQualificationEvidencePlan(t *testing.T) Plan {
	t.Helper()
	plan := testPlan(t)
	plan.OrchestrationID, plan.RunID, plan.FixtureID = uuid.NewString(), uuid.NewString(), uuid.NewString()
	plan.CredentialSet.SetID = uuid.NewString()
	plan.Operations = OperationIDs{
		Reserve: uuid.NewString(), CredentialIssue: uuid.NewString(), RunClosure: uuid.NewString(),
		KMSAttestation: uuid.NewString(), CredentialRevocation: uuid.NewString(), ArtifactIndex: uuid.NewString(),
		ReceiptSign: uuid.NewString(), SnapshotSeal: uuid.NewString(),
	}
	for index := range plan.Artifacts {
		if plan.Artifacts[index].Classification == ClassificationRestricted {
			plan.Artifacts[index].EncryptionOperationID = uuid.NewString()
		}
	}
	if err := ValidatePlan(plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func postgresQualificationEvidenceNow(t *testing.T, store Store) string {
	t.Helper()
	now, err := store.TrustedTime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	value, err := canonicalTime(now.UTC().Truncate(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func postgresQualificationEvidenceReservation(t *testing.T, store Store, plan Plan, trust TrustBindings) Event {
	t.Helper()
	commandHash, err := CanonicalDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	trustDigest, err := CanonicalDigest(trust)
	if err != nil {
		t.Fatal(err)
	}
	copy := clonePlan(plan)
	return Event{
		At: postgresQualificationEvidenceNow(t, store), EventID: uuid.NewString(), Kind: EventReserved,
		OperationID: plan.Operations.Reserve, CommandHash: commandHash,
		TrustBindingsDigest: trustDigest, Plan: &copy,
	}
}

func newPostgresQualificationEvidenceAuthorities(t *testing.T, store Store, plan Plan, trust TrustBindings) *fakeAuthorities {
	t.Helper()
	now, err := store.TrustedTime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	base := now.UTC().Truncate(time.Millisecond)
	formatted := func(offset time.Duration) string {
		value, err := canonicalTime(base.Add(offset))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	authorities := newFakeAuthorities(plan, trust)
	authorities.mutateIssue = func(value *CredentialIssueObservation) {
		value.Binding.IssuedAt, value.Binding.ExpiresAt = formatted(-5*time.Minute), formatted(15*time.Minute)
		value.Attestation.IssuedAt = value.Binding.IssuedAt
	}
	authorities.mutateCapture = func(value *RunClosureObservation) {
		value.CompletedAt = formatted(-4 * time.Minute)
	}
	authorities.mutateEncryption = func(value *EncryptionCommitment) {
		offset := -3 * time.Minute
		if value.ArtifactID == "credential-safe-trace" {
			offset = -2 * time.Minute
		}
		value.EncryptedAt = formatted(offset)
		value.PlaintextDispositionAt = value.EncryptedAt
		if value.PlaintextDisposition == PlaintextDeleted {
			value.PlaintextDispositionAt = formatted(offset + time.Second)
		}
	}
	authorities.mutateKMS = func(value *KMSAttestationObservation) {
		value.Attestation.IssuedAt = formatted(-90 * time.Second)
	}
	authorities.mutateRevoke = func(value *CredentialRevocationObservation) {
		value.RevokedAt = formatted(-60 * time.Second)
		value.Attestation.IssuedAt = value.RevokedAt
	}
	authorities.mutateReceipt = func(value *QualificationReceiptCommitment) {
		value.IssuedAt = formatted(-40 * time.Second)
	}
	authorities.mutateSeal = func(value *SnapshotCommitment) {
		value.SealedAt = formatted(-30 * time.Second)
	}
	authorities.mutateVerify = func(value *SnapshotVerification) {
		value.VerifiedAt = formatted(-20 * time.Second)
	}
	return authorities
}

func newPostgresQualificationEvidenceService(t *testing.T, store Store, plan Plan, trust TrustBindings, authorities *fakeAuthorities) *Service {
	t.Helper()
	service, err := NewService(Config{
		Store: store, Plans: newFakePlanAuthority(t, plan, trust), Credentials: authorities, Capture: authorities, Encryptor: authorities,
		KMS: authorities, Indexer: authorities, Receipt: authorities, Sealer: authorities,
		Verifier: authorities, TrustBindings: trust,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func openPostgresQualificationEvidenceTestStore(t *testing.T) (*sql.DB, *PostgresStore, func()) {
	t.Helper()
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
	if err := base.PingContext(ctx); err != nil {
		base.Close()
		t.Fatal(err)
	}
	if _, err := base.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		base.Close()
		t.Fatal(err)
	}
	schema := "qualification_evidence_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`" AUTHORIZATION worksflow_migration_owner`); err != nil {
		base.Close()
		t.Fatal(err)
	}
	database, err := sql.Open("pgx", postgresQualificationEvidenceDSN(t, dsn, schema))
	if err != nil {
		base.Close()
		t.Fatal(err)
	}
	database.SetMaxOpenConns(32)
	up, err := os.ReadFile("../../migrations/000073_qualification_evidence_event_store.up.sql")
	if err != nil {
		database.Close()
		base.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) {
			t.Fatalf("apply 000073: sqlstate=%s position=%d message=%s", postgresError.Code, postgresError.Position, postgresError.Message)
		}
		t.Fatalf("apply 000073: %v", err)
	}
	store, err := NewPostgresStore(database)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_ = database.Close()
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
		_ = base.Close()
	}
	return database, store, cleanup
}

func openPostgresQualificationEvidenceDedicatedDatabase(t *testing.T, ctx context.Context, schema string) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	database, err := sql.Open("pgx", postgresQualificationEvidenceDSN(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func postgresQualificationEvidenceCurrentSchema(t *testing.T, ctx context.Context, database *sql.DB) string {
	t.Helper()
	var schema string
	if err := database.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

func postgresQualificationEvidenceBackendPID(t *testing.T, ctx context.Context, database *sql.DB) int {
	t.Helper()
	var pid int
	if err := database.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return pid
}

func postgresQualificationEvidenceEventsOID(t *testing.T, ctx context.Context, database *sql.DB) int64 {
	t.Helper()
	var oid int64
	if err := database.QueryRowContext(ctx, `SELECT 'qualification_evidence_events'::regclass::oid::bigint`).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	return oid
}

func postgresQualificationEvidenceDownMigration(t *testing.T) string {
	t.Helper()
	down, err := os.ReadFile("../../migrations/000073_qualification_evidence_event_store.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(down)
}

func waitForPostgresQualificationEvidenceLock(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	pid int,
	relationOID int64,
	mode string,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_locks
  WHERE pid=$1 AND relation=$2::oid AND mode=$3 AND NOT granted
)`, pid, relationOID, mode).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("backend %d did not wait for %s on relation %d", pid, mode, relationOID)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func receivePostgresQualificationEvidenceCreateResult[T any](t *testing.T, ctx context.Context, result <-chan T) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		var zero T
		return zero
	}
}

func receivePostgresQualificationEvidenceError(t *testing.T, ctx context.Context, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return ctx.Err()
	}
}

func postgresQualificationEvidenceDSN(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema+",public")
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema + ",public"
}

func assertPostgresQualificationEvidenceSecurity(t *testing.T, ctx context.Context, database *sql.DB, store *PostgresStore) {
	t.Helper()
	if _, err := database.ExecContext(ctx, `UPDATE qualification_evidence_events SET event_kind=event_kind`); !postgresQualificationEvidenceStateError(err, "WQE03") {
		t.Fatalf("direct immutable event UPDATE error = %v", err)
	}
	if _, err := database.ExecContext(ctx, `TRUNCATE qualification_evidence_operations`); !postgresQualificationEvidenceStateError(err, "WQE03") {
		t.Fatalf("direct immutable operation TRUNCATE error = %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE qualification_evidence_heads SET phase=phase`); !postgresQualificationEvidenceStateError(err, "WQE03") {
		t.Fatalf("direct head UPDATE error = %v", err)
	}
	if _, err := database.ExecContext(ctx, `
SELECT append_qualification_evidence_event(
  'sha256:0000000000000000000000000000000000000000000000000000000000000000',
  '{}'::bytea, '{}'::jsonb,
  'sha256:0000000000000000000000000000000000000000000000000000000000000000',
  '{}'::bytea, '{}'::jsonb
)`); !postgresQualificationEvidenceStateError(err, "WQE03") {
		t.Fatalf("malformed direct append error = %v", err)
	}

	plan, trust := uniquePostgresQualificationEvidencePlan(t), testTrust()
	reservation := postgresQualificationEvidenceReservation(t, store, plan, trust)
	materials, err := buildPostgresQualificationEvidenceMaterials(plan.OrchestrationID, 0, reservation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
SELECT append_qualification_evidence_event($1,$2,$3::jsonb,$4,$5,'{}'::jsonb)`,
		materials.requestHash, materials.requestBytes, materials.requestDocument,
		materials.eventHash, materials.eventBytes,
	); !postgresQualificationEvidenceStateError(err, "WQE03") {
		t.Fatalf("raw event/document mismatch error = %v", err)
	}
	var eventMap map[string]any
	if err := json.Unmarshal(materials.eventBytes, &eventMap); err != nil {
		t.Fatal(err)
	}
	eventMap["metadata"] = map[string]any{"token": "forbidden"}
	widenedEvent, err := CanonicalJSON(eventMap)
	if err != nil {
		t.Fatal(err)
	}
	widenedEventHash := sha256Digest(widenedEvent)
	var requestMap map[string]any
	if err := json.Unmarshal(materials.requestBytes, &requestMap); err != nil {
		t.Fatal(err)
	}
	requestMap["event"], requestMap["eventHash"] = eventMap, widenedEventHash
	widenedRequest, err := CanonicalJSON(requestMap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
SELECT append_qualification_evidence_event($1,$2,$3::jsonb,$4,$5,$6::jsonb)`,
		sha256Digest(widenedRequest), widenedRequest, string(widenedRequest),
		widenedEventHash, widenedEvent, string(widenedEvent),
	); !postgresQualificationEvidenceStateError(err, "WQE03") {
		t.Fatalf("unknown event metadata error = %v", err)
	}

	if _, _, err := store.Create(ctx, plan.OrchestrationID, reservation); err != nil {
		t.Fatal(err)
	}
	outOfOrder := Event{
		At: postgresQualificationEvidenceNow(t, store), EventID: uuid.NewString(),
		Kind: EventKMSAttestationStarted, OperationID: plan.Operations.KMSAttestation,
	}
	outOfOrderMaterials, err := buildPostgresQualificationEvidenceMaterials(plan.OrchestrationID, 1, outOfOrder)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
SELECT append_qualification_evidence_event($1,$2,$3::jsonb,$4,$5,$6::jsonb)`,
		outOfOrderMaterials.requestHash, outOfOrderMaterials.requestBytes, outOfOrderMaterials.requestDocument,
		outOfOrderMaterials.eventHash, outOfOrderMaterials.eventBytes, outOfOrderMaterials.eventDocument,
	); !postgresQualificationEvidenceStateError(err, "WQE03") {
		t.Fatalf("direct out-of-order transition error = %v", err)
	}

	var publicExecute, applicationExecute bool
	if err := database.QueryRowContext(ctx, `
SELECT
  has_function_privilege('public', current_schema() || '.append_qualification_evidence_event(text,bytea,jsonb,text,bytea,jsonb)', 'EXECUTE'),
  has_function_privilege('worksflow_application', current_schema() || '.append_qualification_evidence_event(text,bytea,jsonb,text,bytea,jsonb)', 'EXECUTE')
`).Scan(&publicExecute, &applicationExecute); err != nil {
		t.Fatal(err)
	}
	if publicExecute || applicationExecute {
		t.Fatalf("append routine execute ACL = public:%t application:%t", publicExecute, applicationExecute)
	}
	var widenedFunctionACLs int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_proc AS routine
JOIN pg_namespace AS namespace ON namespace.oid=routine.pronamespace
WHERE namespace.nspname=current_schema()
  AND routine.proname IN (
    'qualification_evidence_sha256','reject_qualification_evidence_immutable_mutation',
    'guard_qualification_evidence_head_projection','append_qualification_evidence_event'
  )
  AND (
    has_function_privilege('public',routine.oid,'EXECUTE')
    OR has_function_privilege('worksflow_application',routine.oid,'EXECUTE')
  )`).Scan(&widenedFunctionACLs); err != nil {
		t.Fatal(err)
	}
	if widenedFunctionACLs != 0 {
		t.Fatalf("PUBLIC/application EXECUTE privilege exists on %d Qualification Evidence functions", widenedFunctionACLs)
	}
	var owner, appendSearchPath, guardSearchPath, hashSearchPath, rejectSearchPath string
	var securityDefiner bool
	if err := database.QueryRowContext(ctx, `
SELECT owner.rolname, routine.prosecdef, array_to_string(routine.proconfig, ',')
FROM pg_proc AS routine
JOIN pg_namespace AS namespace ON namespace.oid=routine.pronamespace
JOIN pg_roles AS owner ON owner.oid=routine.proowner
WHERE namespace.nspname=current_schema() AND routine.proname='append_qualification_evidence_event'`).Scan(
		&owner, &securityDefiner, &appendSearchPath,
	); err != nil {
		t.Fatal(err)
	}
	if owner != "worksflow_migration_owner" || !securityDefiner ||
		!strings.Contains(appendSearchPath, "search_path=pg_catalog,") || !strings.Contains(appendSearchPath, "pg_temp") {
		t.Fatalf("append posture owner=%s definer=%t path=%q", owner, securityDefiner, appendSearchPath)
	}
	if err := database.QueryRowContext(ctx, `
SELECT array_to_string(routine.proconfig, ',')
FROM pg_proc AS routine JOIN pg_namespace AS namespace ON namespace.oid=routine.pronamespace
WHERE namespace.nspname=current_schema() AND routine.proname='guard_qualification_evidence_head_projection'`).Scan(&guardSearchPath); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(guardSearchPath, "search_path=pg_catalog,") || strings.Contains(guardSearchPath, "pg_temp") {
		t.Fatalf("head guard search path = %q", guardSearchPath)
	}
	if err := database.QueryRowContext(ctx, `
SELECT array_to_string(routine.proconfig, ',')
FROM pg_proc AS routine JOIN pg_namespace AS namespace ON namespace.oid=routine.pronamespace
WHERE namespace.nspname=current_schema() AND routine.proname='qualification_evidence_sha256'`).Scan(&hashSearchPath); err != nil {
		t.Fatal(err)
	}
	if hashSearchPath != "search_path=pg_catalog" {
		t.Fatalf("hash routine search path = %q", hashSearchPath)
	}
	if err := database.QueryRowContext(ctx, `
SELECT array_to_string(routine.proconfig, ',')
FROM pg_proc AS routine JOIN pg_namespace AS namespace ON namespace.oid=routine.pronamespace
WHERE namespace.nspname=current_schema() AND routine.proname='reject_qualification_evidence_immutable_mutation'`).Scan(&rejectSearchPath); err != nil {
		t.Fatal(err)
	}
	if rejectSearchPath != "search_path=pg_catalog" {
		t.Fatalf("immutable reject routine search path = %q", rejectSearchPath)
	}
	var wrongOwnerCount int
	if err := database.QueryRowContext(ctx, `
WITH owned_objects(owner_name) AS (
  SELECT owner.rolname
  FROM pg_class AS object
  JOIN pg_namespace AS namespace ON namespace.oid=object.relnamespace
  JOIN pg_roles AS owner ON owner.oid=object.relowner
  WHERE namespace.nspname=current_schema()
    AND object.relname IN (
      'qualification_evidence_events','qualification_evidence_operations',
      'qualification_evidence_heads','qualification_evidence_projection_authorizations'
    )
  UNION ALL
  SELECT owner.rolname
  FROM pg_proc AS routine
  JOIN pg_namespace AS namespace ON namespace.oid=routine.pronamespace
  JOIN pg_roles AS owner ON owner.oid=routine.proowner
  WHERE namespace.nspname=current_schema()
    AND routine.proname IN (
      'qualification_evidence_sha256','reject_qualification_evidence_immutable_mutation',
      'guard_qualification_evidence_head_projection','append_qualification_evidence_event'
    )
)
SELECT count(*) FILTER (WHERE owner_name <> 'worksflow_migration_owner')
       + CASE WHEN count(*) = 8 THEN 0 ELSE 100 END
FROM owned_objects`).Scan(&wrongOwnerCount); err != nil {
		t.Fatal(err)
	}
	if wrongOwnerCount != 0 {
		t.Fatalf("Qualification Evidence exact eight-object owner posture drift count = %d", wrongOwnerCount)
	}
	var applicationTablePrivileges int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM information_schema.role_table_grants
WHERE grantee='worksflow_application' AND table_schema=current_schema()
  AND table_name IN (
    'qualification_evidence_events','qualification_evidence_operations',
    'qualification_evidence_heads','qualification_evidence_projection_authorizations'
  )`).Scan(&applicationTablePrivileges); err != nil {
		t.Fatal(err)
	}
	if applicationTablePrivileges != 0 {
		t.Fatalf("ordinary application has %d Qualification Evidence table privileges", applicationTablePrivileges)
	}
	var schema string
	if err := database.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	denied, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, directInsertErr := denied.ExecContext(ctx, `
SET LOCAL ROLE worksflow_application;
INSERT INTO "`+schema+`".qualification_evidence_events DEFAULT VALUES`)
	_ = denied.Rollback()
	if directInsertErr == nil {
		t.Fatal("ordinary application performed direct Qualification Evidence event INSERT")
	}
	if _, err := database.ExecContext(ctx, `GRANT USAGE ON SCHEMA "`+schema+`" TO worksflow_application`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `GRANT UPDATE ON qualification_evidence_heads TO worksflow_application`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.ExecContext(context.Background(), `REVOKE UPDATE ON qualification_evidence_heads FROM worksflow_application`)
		_, _ = database.ExecContext(context.Background(), `REVOKE USAGE ON SCHEMA "`+schema+`" FROM worksflow_application`)
	}()
	shadow, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, shadowErr := shadow.ExecContext(ctx, `
SET LOCAL ROLE worksflow_application;
CREATE TEMP TABLE qualification_evidence_projection_authorizations(transaction_id bigint, backend_pid integer);
INSERT INTO pg_temp.qualification_evidence_projection_authorizations VALUES (txid_current(),pg_backend_pid());
UPDATE qualification_evidence_heads SET phase=phase`)
	_ = shadow.Rollback()
	if shadowErr == nil {
		t.Fatal("pg_temp shadow table bypassed the trusted Qualification Evidence head guard")
	}
}

func assertPostgresQualificationEvidenceDownFence(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	down, err := os.ReadFile("../../migrations/000073_qualification_evidence_event_store.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, `LOCK TABLE qualification_evidence_events IN ACCESS SHARE MODE`); err != nil {
		_ = blocker.Rollback()
		t.Fatal(err)
	}
	downResult := make(chan error, 1)
	go func() {
		_, downErr := database.ExecContext(ctx, string(down))
		downResult <- downErr
	}()
	select {
	case early := <-downResult:
		_ = blocker.Rollback()
		t.Fatalf("rollback bypassed ACCESS EXCLUSIVE ledger fence: %v", early)
	case <-time.After(100 * time.Millisecond):
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-downResult; err == nil ||
		!strings.Contains(err.Error(), "cannot roll back Qualification Evidence store while immutable audit state is nonempty") {
		t.Fatalf("nonempty immutable rollback error = %v", err)
	}
}

func postgresQualificationEvidenceStateError(err error, code string) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == code
}
