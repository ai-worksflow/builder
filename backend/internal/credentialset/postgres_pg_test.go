package credentialset

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

type postgresCredentialRig struct {
	broker  *fakeBroker
	command IssueCommand
	service *Service
	signer  *fakeSigner
}

type ambiguousPostgresStore struct {
	delegate Store
	kind     EventKind
	commit   bool
	once     atomic.Bool
}

func (store *ambiguousPostgresStore) TrustedTime(ctx context.Context) (time.Time, error) {
	return store.delegate.TrustedTime(ctx)
}

func (store *ambiguousPostgresStore) CreateIssue(ctx context.Context, setID string, event Event) (Snapshot, bool, error) {
	return store.delegate.CreateIssue(ctx, setID, event)
}

func (store *ambiguousPostgresStore) Load(ctx context.Context, setID string) (Snapshot, error) {
	return store.delegate.Load(ctx, setID)
}

func (store *ambiguousPostgresStore) Events(ctx context.Context, setID string) ([]Event, error) {
	return store.delegate.Events(ctx, setID)
}

func (store *ambiguousPostgresStore) Append(ctx context.Context, setID string, version uint64, event Event) (Snapshot, error) {
	if event.Kind != store.kind || !store.once.CompareAndSwap(false, true) {
		return store.delegate.Append(ctx, setID, version, event)
	}
	if store.commit {
		if _, err := store.delegate.Append(ctx, setID, version, event); err != nil {
			return Snapshot{}, err
		}
	}
	return Snapshot{}, ErrStoreOutcomeUnknown
}

func TestPostgresCredentialSetStoreRealPostgresClosure(t *testing.T) {
	database, store, cleanup := openPostgresCredentialSetTestStore(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	t.Run("service issue revoke and exact persistence", func(t *testing.T) {
		trig := newPostgresCredentialRig(t, store)
		issued, err := trig.service.Issue(ctx, trig.command)
		if err != nil || issued.Delivery == nil {
			t.Fatalf("Issue() = %#v, %v", issued, err)
		}
		revocationOperationID := uuid.NewString()
		revoked, err := trig.service.Revoke(ctx, RevokeCommand{
			Binding: issued.Binding, OperationID: revocationOperationID,
		})
		if err != nil {
			t.Fatalf("Revoke() = %#v, %v", revoked, err)
		}
		snapshot, err := store.Load(ctx, trig.command.SetID)
		if err != nil || snapshot.Phase != PhaseComplete || snapshot.Version != 12 ||
			snapshot.RevocationAttestation == nil || snapshot.IssueAttestation == nil {
			t.Fatalf("final snapshot = %#v, %v", snapshot, err)
		}
		events, err := store.Events(ctx, trig.command.SetID)
		if err != nil || len(events) != 12 {
			t.Fatalf("event ledger length = %d, %v", len(events), err)
		}
		expectedKinds := []EventKind{
			EventIssueReserved, EventPrepareStarted, EventPrepared, EventActivationStarted,
			EventActivated, EventIssuanceSignStarted, EventIssued, EventRevocationReserved,
			EventRevocationStarted, EventRevoked, EventRevocationSignStarted, EventRevocationAttested,
		}
		for index, event := range events {
			if event.Kind != expectedKinds[index] || event.At.IsZero() || event.EventID == "" ||
				event.At != event.At.UTC().Truncate(time.Millisecond) {
				t.Fatalf("event %d has non-canonical DB authority: %#v", index, event)
			}
		}
		const goldenMemberDigest = "sha256:22e80fe83cca008f8f985929832eeee593b04c347c0f235341cdf7401d53e086"
		var storedMemberDigest string
		if err := database.QueryRowContext(ctx, `
SELECT binding_document->>'memberBindingsDigest'
FROM credential_set_events
WHERE set_id=$1 AND event_kind='prepared'`, trig.command.SetID).Scan(&storedMemberDigest); err != nil {
			t.Fatal(err)
		}
		if storedMemberDigest != goldenMemberDigest || issued.Binding.MemberBindingsDigest != goldenMemberDigest {
			t.Fatalf("cross-language Golden member digest = stored:%s Go:%s", storedMemberDigest, issued.Binding.MemberBindingsDigest)
		}
		var forbiddenCount int
		if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM credential_set_events
WHERE convert_from(request_bytes, 'UTF8') ~ '(raw-token|session-cookie|/tmp/credential|Authorization)'`).Scan(&forbiddenCount); err != nil {
			t.Fatal(err)
		}
		if forbiddenCount != 0 {
			t.Fatalf("non-secret event ledger contains %d forbidden bearer values", forbiddenCount)
		}
		// Exact event replay is inspectable even though the head has advanced.
		last := events[len(events)-1]
		replayed, err := store.Append(ctx, trig.command.SetID, 11, last)
		if err != nil || replayed.Version != 12 || replayed.LastEventID != last.EventID {
			t.Fatalf("exact event replay = %#v, %v", replayed, err)
		}
		drift := last
		drift.OperationID = "credential-sign/" + uuid.NewString() + "/attestation"
		if _, err := store.Append(ctx, trig.command.SetID, 11, drift); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("same event ID drift error = %v", err)
		}
		other := newPostgresCredentialRig(t, store)
		otherIssued, err := other.service.Issue(ctx, other.command)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := other.service.Revoke(ctx, RevokeCommand{
			Binding: otherIssued.Binding, OperationID: revocationOperationID,
		}); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("globally reused revoke operation error = %v", err)
		}
	})

	t.Run("concurrent issue identity and version CAS", func(t *testing.T) {
		now, err := store.TrustedTime(ctx)
		if err != nil {
			t.Fatal(err)
		}
		setID, operationID := uuid.NewString(), uuid.NewString()
		base := Event{
			At: now, EventID: uuid.NewString(), Kind: EventIssueReserved, OperationID: operationID,
			IssueCommandHash: sha256Digest([]byte("concurrent-issue")),
			IssuedAt:         now.Format(canonicalTimeLayout), ExpiresAt: now.Add(10 * time.Minute).Format(canonicalTimeLayout),
		}
		var winners atomic.Int64
		var failures atomic.Int64
		var wait sync.WaitGroup
		for range 16 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				event := base
				event.EventID = uuid.NewString()
				_, created, createErr := store.CreateIssue(ctx, setID, event)
				if createErr != nil {
					failures.Add(1)
				} else if created {
					winners.Add(1)
				}
			}()
		}
		wait.Wait()
		if winners.Load() != 1 || failures.Load() != 0 {
			t.Fatalf("same command concurrency winners=%d failures=%d", winners.Load(), failures.Load())
		}
		different := base
		different.EventID = uuid.NewString()
		different.OperationID = uuid.NewString()
		if _, _, err := store.CreateIssue(ctx, setID, different); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("different issue operation error = %v", err)
		}
		foreignSet := base
		foreignSet.EventID = uuid.NewString()
		if _, _, err := store.CreateIssue(ctx, uuid.NewString(), foreignSet); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("globally reused issue operation error = %v", err)
		}

		left := Event{At: now, EventID: uuid.NewString(), Kind: EventPrepareStarted, OperationID: operationID}
		right := left
		right.EventID = uuid.NewString()
		results := make(chan error, 2)
		for _, event := range []Event{left, right} {
			wait.Add(1)
			go func(value Event) {
				defer wait.Done()
				_, appendErr := store.Append(ctx, setID, 1, value)
				results <- appendErr
			}(event)
		}
		wait.Wait()
		close(results)
		var appended, conflicts int
		for result := range results {
			if result == nil {
				appended++
			} else if errors.Is(result, ErrCASConflict) {
				conflicts++
			} else {
				t.Fatalf("unexpected concurrent append error: %v", result)
			}
		}
		if appended != 1 || conflicts != 1 {
			t.Fatalf("CAS outcomes appended=%d conflicts=%d", appended, conflicts)
		}
	})

	t.Run("post-lock database time", func(t *testing.T) {
		connection, err := database.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		transaction, err := connection.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `LOCK TABLE credential_set_heads IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		requestedAt, _ := store.TrustedTime(ctx)
		setID := uuid.NewString()
		result := make(chan struct {
			snapshot Snapshot
			err      error
		}, 1)
		go func() {
			snapshot, _, createErr := store.CreateIssue(ctx, setID, Event{
				At: requestedAt, EventID: uuid.NewString(), Kind: EventIssueReserved, OperationID: uuid.NewString(),
				IssueCommandHash: sha256Digest([]byte("post-lock-clock")),
				IssuedAt:         requestedAt.Format(canonicalTimeLayout), ExpiresAt: requestedAt.Add(10 * time.Minute).Format(canonicalTimeLayout),
			})
			result <- struct {
				snapshot Snapshot
				err      error
			}{snapshot, createErr}
		}()
		time.Sleep(150 * time.Millisecond)
		var releaseTime time.Time
		if err := transaction.QueryRowContext(ctx, `SELECT date_trunc('milliseconds', clock_timestamp())`).Scan(&releaseTime); err != nil {
			t.Fatal(err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
		outcome := <-result
		if outcome.err != nil || outcome.snapshot.LastEventAt.Before(releaseTime.UTC()) {
			t.Fatalf("post-lock event time = %s, release = %s, err = %v", outcome.snapshot.LastEventAt, releaseTime, outcome.err)
		}
	})

	t.Run("committed and uncommitted unknown never repeat activation", func(t *testing.T) {
		for _, commit := range []bool{true, false} {
			t.Run(fmt.Sprintf("commit-%t", commit), func(t *testing.T) {
				wrapper := &ambiguousPostgresStore{delegate: store, kind: EventActivated, commit: commit}
				trig := newPostgresCredentialRig(t, wrapper)
				first, firstErr := trig.service.Issue(ctx, trig.command)
				if commit {
					if firstErr != nil || first.Delivery == nil {
						t.Fatalf("committed unknown reconciliation = %#v, %v", first, firstErr)
					}
				} else if !errors.Is(firstErr, ErrOutcomeUnknown) {
					t.Fatalf("uncommitted unknown error = %v", firstErr)
				}
				if !commit {
					if _, err := trig.service.Issue(ctx, trig.command); err != nil {
						t.Fatal(err)
					}
				}
				prepare, activate, inspect, _, _ := trig.broker.counts()
				if prepare != 1 || activate != 1 || (!commit && inspect < 1) {
					t.Fatalf("side effects after store unknown = prepare:%d activate:%d inspect:%d", prepare, activate, inspect)
				}
			})
		}
	})

	assertPostgresCredentialSetSecurity(t, ctx, database)
	down, err := os.ReadFile("../../migrations/000072_credential_set_event_store.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, `LOCK TABLE credential_set_events IN ACCESS SHARE MODE`); err != nil {
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
		!strings.Contains(err.Error(), "cannot roll back CredentialSet store while immutable audit state is nonempty") {
		t.Fatalf("nonempty immutable rollback error = %v", err)
	}
}

func newPostgresCredentialRig(t *testing.T, store Store) postgresCredentialRig {
	t.Helper()
	now, err := store.TrustedTime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	issuedAt := now.Add(-time.Second).UTC().Truncate(time.Millisecond)
	broker := &fakeBroker{errorText: "raw-token=session-cookie;/tmp/credential.json"}
	signer := newFakeSigner(t)
	service, err := NewGoldenService(Config{
		Audience: testAudience, Broker: broker, Issuer: testIssuer, Signer: signer, Store: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	return postgresCredentialRig{
		broker: broker, service: service, signer: signer,
		command: IssueCommand{
			Audience: testAudience, ExpiresAt: issuedAt.Add(10 * time.Minute), FixtureID: uuid.NewString(),
			IssuedAt: issuedAt, Issuer: testIssuer, Members: goldenMembers(), OperationID: uuid.NewString(),
			RunID: uuid.NewString(), SetID: uuid.NewString(),
		},
	}
}

func openPostgresCredentialSetTestStore(t *testing.T) (*sql.DB, *PostgresStore, func()) {
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
	schema := "credential_set_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`" AUTHORIZATION worksflow_migration_owner`); err != nil {
		base.Close()
		t.Fatal(err)
	}
	database, err := sql.Open("pgx", postgresCredentialSetDSN(t, dsn, schema))
	if err != nil {
		base.Close()
		t.Fatal(err)
	}
	up, err := os.ReadFile("../../migrations/000072_credential_set_event_store.up.sql")
	if err != nil {
		database.Close()
		base.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) {
			t.Fatalf("apply 000072: sqlstate=%s position=%d message=%s", postgresError.Code, postgresError.Position, postgresError.Message)
		}
		t.Fatalf("apply 000072: %v", err)
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

func assertPostgresCredentialSetSecurity(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	if _, err := database.ExecContext(ctx, `UPDATE credential_set_events SET operation_id=operation_id`); !postgresCredentialStateError(err, "WSC03") {
		t.Fatalf("direct immutable event UPDATE error = %v", err)
	}
	if _, err := database.ExecContext(ctx, `TRUNCATE credential_set_operations`); !postgresCredentialStateError(err, "WSC03") {
		t.Fatalf("direct immutable operation TRUNCATE error = %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE credential_set_heads SET phase=phase`); !postgresCredentialStateError(err, "WSC03") {
		t.Fatalf("direct head UPDATE error = %v", err)
	}
	if _, err := database.ExecContext(ctx, `
SELECT append_credential_set_event(
  'sha256:0000000000000000000000000000000000000000000000000000000000000000',
  '{}'::bytea, '{}'::jsonb, NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL
)`); !postgresCredentialStateError(err, "WSC03") {
		t.Fatalf("malformed direct append error = %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := database.QueryRowContext(ctx, `SELECT date_trunc('milliseconds', clock_timestamp())`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	now = now.UTC().Truncate(time.Millisecond)
	validEvent := Event{
		At: now, EventID: uuid.NewString(), Kind: EventIssueReserved, OperationID: uuid.NewString(),
		IssueCommandHash: sha256Digest([]byte("direct-shape-canary")), IssuedAt: now.Format(canonicalTimeLayout),
		ExpiresAt: now.Add(10 * time.Minute).Format(canonicalTimeLayout),
	}
	validMaterials, err := buildPostgresEventMaterials(uuid.NewString(), 0, validEvent)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unknown root metadata", mutate: func(value map[string]any) { value["metadata"] = map[string]any{"token": "forbidden"} }},
		{name: "nullable scalar", mutate: func(value map[string]any) { value["event"].(map[string]any)["operationId"] = nil }},
		{name: "widened version", mutate: func(value map[string]any) { value["expectedVersion"] = "0" }},
	}
	for _, test := range tests {
		t.Run("direct "+test.name, func(t *testing.T) {
			var changed map[string]any
			if err := json.Unmarshal(validMaterials.requestBytes, &changed); err != nil {
				t.Fatal(err)
			}
			test.mutate(changed)
			changedBytes, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.ExecContext(ctx, `
SELECT append_credential_set_event(
  $1,$2,$3::jsonb,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL
)`, sha256Digest(changedBytes), changedBytes, string(changedBytes)); !postgresCredentialStateError(err, "WSC03") {
				t.Fatalf("closed request shape error = %v", err)
			}
		})
	}
	if _, err := database.ExecContext(ctx, `
SELECT append_credential_set_event(
  $1,$2,'{}'::jsonb,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL
)`, validMaterials.requestHash, validMaterials.requestBytes); !postgresCredentialStateError(err, "WSC03") {
		t.Fatalf("raw JSON/document mismatch error = %v", err)
	}
	var publicExecute, applicationExecute bool
	if err := database.QueryRowContext(ctx, `
SELECT
  has_function_privilege('public', current_schema() || '.append_credential_set_event(text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb)', 'EXECUTE'),
  has_function_privilege('worksflow_application', current_schema() || '.append_credential_set_event(text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb)', 'EXECUTE')
`).Scan(&publicExecute, &applicationExecute); err != nil {
		t.Fatal(err)
	}
	if publicExecute || applicationExecute {
		t.Fatalf("append routine execute ACL = public:%t application:%t", publicExecute, applicationExecute)
	}
	var owner, searchPath, guardSearchPath string
	var securityDefiner bool
	if err := database.QueryRowContext(ctx, `
SELECT owner.rolname, routine.prosecdef, array_to_string(routine.proconfig, ',')
FROM pg_proc AS routine
JOIN pg_namespace AS namespace ON namespace.oid=routine.pronamespace
JOIN pg_roles AS owner ON owner.oid=routine.proowner
WHERE namespace.nspname=current_schema() AND routine.proname='append_credential_set_event'`).Scan(&owner, &securityDefiner, &searchPath); err != nil {
		t.Fatal(err)
	}
	if owner != "worksflow_migration_owner" || !securityDefiner ||
		!strings.Contains(searchPath, "search_path=pg_catalog,") || !strings.Contains(searchPath, "pg_temp") {
		t.Fatalf("append posture owner=%s definer=%t path=%q", owner, securityDefiner, searchPath)
	}
	if err := database.QueryRowContext(ctx, `
SELECT array_to_string(routine.proconfig, ',')
FROM pg_proc AS routine
JOIN pg_namespace AS namespace ON namespace.oid=routine.pronamespace
WHERE namespace.nspname=current_schema() AND routine.proname='guard_credential_set_head_projection'`).Scan(&guardSearchPath); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(guardSearchPath, "search_path=pg_catalog,") || strings.Contains(guardSearchPath, "pg_temp") {
		t.Fatalf("head guard search path = %q", guardSearchPath)
	}
	var applicationTablePrivileges int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM information_schema.role_table_grants
WHERE grantee='worksflow_application'
  AND table_schema=current_schema()
  AND table_name IN ('credential_set_events','credential_set_operations','credential_set_heads','credential_set_projection_authorizations')`).Scan(&applicationTablePrivileges); err != nil {
		t.Fatal(err)
	}
	if applicationTablePrivileges != 0 {
		t.Fatalf("ordinary application has %d CredentialSet table privileges", applicationTablePrivileges)
	}
	// Even an accidental future head UPDATE grant cannot be combined with a
	// pg_temp shadow authorization table to bypass the trusted-schema guard.
	var schema string
	if err := database.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	deniedTransaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, directInsertErr := deniedTransaction.ExecContext(ctx, `
SET LOCAL ROLE worksflow_application;
INSERT INTO "`+schema+`".credential_set_events DEFAULT VALUES`)
	_ = deniedTransaction.Rollback()
	if directInsertErr == nil {
		t.Fatal("ordinary application performed direct CredentialSet event INSERT")
	}
	if _, err := database.ExecContext(ctx, `GRANT USAGE ON SCHEMA "`+schema+`" TO worksflow_application`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `GRANT UPDATE ON credential_set_heads TO worksflow_application`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.ExecContext(context.Background(), `REVOKE UPDATE ON credential_set_heads FROM worksflow_application`)
		_, _ = database.ExecContext(context.Background(), `REVOKE USAGE ON SCHEMA "`+schema+`" FROM worksflow_application`)
	}()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, shadowErr := transaction.ExecContext(ctx, `
SET LOCAL ROLE worksflow_application;
CREATE TEMP TABLE credential_set_projection_authorizations(transaction_id bigint, backend_pid integer);
INSERT INTO pg_temp.credential_set_projection_authorizations VALUES (txid_current(),pg_backend_pid());
UPDATE credential_set_heads SET phase=phase`)
	_ = transaction.Rollback()
	if shadowErr == nil {
		t.Fatal("pg_temp shadow table bypassed the trusted CredentialSet head guard")
	}
}

func postgresCredentialStateError(err error, code string) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == code
}

func postgresCredentialSetDSN(t *testing.T, dsn, schema string) string {
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
