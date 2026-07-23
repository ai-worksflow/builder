package workflowinputauthority

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/migrations"
)

func TestPostgresStoreFreezeMigrationFenceOrderingCanary(t *testing.T) {
	fixture := newPostgresStoreConcurrencyFixture(t)
	relationBlocker := beginPostgresStoreConcurrencyAttempt(t, fixture.ctx, fixture.database)
	defer relationBlocker.close()
	if _, err := relationBlocker.transaction.ExecContext(fixture.ctx, `
SET LOCAL lock_timeout = '5s';
LOCK TABLE workflow_input_authorities IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("hold Workflow Input relation lock: %v", err)
	}

	storeAttempt := beginPostgresStoreConcurrencyAttempt(t, fixture.ctx, fixture.database)
	defer storeAttempt.close()
	storeContext, cancelStore := context.WithCancel(fixture.ctx)
	defer cancelStore()
	storeResult := make(chan postgresStoreConcurrencyResult, 1)
	go func() {
		record, err := fixture.store.Freeze(storeContext, storeAttempt.token, fixture.candidate)
		storeResult <- postgresStoreConcurrencyResult{record: record, err: err}
	}()
	waitForPostgresStoreFenceBeforeInspect(
		t, fixture.ctx, fixture.database, storeAttempt.backendPID, fixture.authorityRelationOID, storeResult,
	)

	migrationAttempt := beginPostgresStoreConcurrencyAttempt(t, fixture.ctx, fixture.database)
	defer migrationAttempt.close()
	migrationContext, cancelMigration := context.WithCancel(fixture.ctx)
	defer cancelMigration()
	migrationResult := make(chan error, 1)
	go func() {
		_, err := migrationAttempt.transaction.ExecContext(migrationContext, `
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended($1, 0)
)`, workflowInputAuthorityMigrationAdvisoryKey)
		migrationResult <- err
	}()
	waitForPostgresStoreAdvisoryLock(
		t, fixture.ctx, fixture.database, migrationAttempt.backendPID, "ExclusiveLock", false, migrationResult,
	)

	if err := relationBlocker.transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	frozen := receivePostgresStoreConcurrencyResult(t, storeResult, "Store freeze after relation lock release")
	if frozen.err != nil {
		t.Fatalf("Store freeze after relation lock release: %v", frozen.err)
	}
	if frozen.record.Idempotent {
		t.Fatal("first Store freeze was reported as an idempotent replay")
	}
	assertPostgresStoreAdvisoryLock(
		t, fixture.ctx, fixture.database, migrationAttempt.backendPID, "ExclusiveLock", false,
	)
	if err := activatePostgresStoreCanary(fixture.ctx, storeAttempt.transaction, frozen.record); err != nil {
		t.Fatalf("activate fenced Store authority: %v", err)
	}
	if err := storeAttempt.transaction.Commit(); err != nil {
		t.Fatalf("commit fenced Store authority: %v", err)
	}

	select {
	case err := <-migrationResult:
		if err != nil {
			t.Fatalf("exclusive migration fence after Store commit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exclusive migration fence did not acquire after Store transaction committed")
	}
}

func TestPostgresStoreConcurrentExactFreezeCanary(t *testing.T) {
	fixture := newPostgresStoreConcurrencyFixture(t)
	relationBlocker := beginPostgresStoreConcurrencyAttempt(t, fixture.ctx, fixture.database)
	defer relationBlocker.close()
	if _, err := relationBlocker.transaction.ExecContext(fixture.ctx, `
SET LOCAL lock_timeout = '5s';
LOCK TABLE workflow_input_authorities IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("hold Workflow Input relation lock: %v", err)
	}

	attempts := []*postgresStoreConcurrencyAttempt{
		beginPostgresStoreConcurrencyAttempt(t, fixture.ctx, fixture.database),
		beginPostgresStoreConcurrencyAttempt(t, fixture.ctx, fixture.database),
	}
	for _, attempt := range attempts {
		defer attempt.close()
	}
	result := make(chan postgresStoreConcurrencyResult, len(attempts))
	start := make(chan struct{})
	for index, attempt := range attempts {
		go func(index int, attempt *postgresStoreConcurrencyAttempt) {
			<-start
			record, err := fixture.store.Freeze(fixture.ctx, attempt.token, fixture.candidate)
			result <- postgresStoreConcurrencyResult{index: index, record: record, err: err}
		}(index, attempt)
	}
	close(start)
	for _, attempt := range attempts {
		waitForPostgresStoreFenceBeforeInspect(
			t, fixture.ctx, fixture.database, attempt.backendPID, fixture.authorityRelationOID, result,
		)
	}
	if err := relationBlocker.transaction.Rollback(); err != nil {
		t.Fatal(err)
	}

	winner := receivePostgresStoreConcurrencyResult(t, result, "first exact concurrent Store freeze")
	if winner.err != nil {
		t.Fatalf("first exact concurrent Store freeze: %v", winner.err)
	}
	if winner.record.Idempotent {
		t.Fatal("first exact concurrent Store freeze was reported as a replay")
	}
	loserIndex := 1 - winner.index
	waitForPostgresStoreAnyLock(
		t, fixture.ctx, fixture.database, attempts[loserIndex].backendPID, result,
	)
	if err := activatePostgresStoreCanary(
		fixture.ctx, attempts[winner.index].transaction, winner.record,
	); err != nil {
		t.Fatalf("activate exact concurrent Store winner: %v", err)
	}
	if err := attempts[winner.index].transaction.Commit(); err != nil {
		t.Fatalf("commit exact concurrent Store winner: %v", err)
	}

	replay := receivePostgresStoreConcurrencyResult(t, result, "second exact concurrent Store freeze")
	if replay.err != nil {
		t.Fatalf("second exact concurrent Store freeze: %v", replay.err)
	}
	if replay.index != loserIndex || !replay.record.Idempotent ||
		!sameImmutableRecord(replay.record, winner.record) {
		t.Fatalf("exact concurrent Store replay = index %d idempotent %t", replay.index, replay.record.Idempotent)
	}
	if err := attempts[loserIndex].transaction.Rollback(); err != nil {
		t.Fatal(err)
	}

	conflicting := fixture.candidate
	conflicting.Request.AuthorityID = uuid.NewString()
	conflictAttempt := beginPostgresStoreConcurrencyAttempt(t, fixture.ctx, fixture.database)
	defer conflictAttempt.close()
	_, err := fixture.store.Freeze(fixture.ctx, conflictAttempt.token, conflicting)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("same operation with conflicting authority bytes error = %v", err)
	}
}

type postgresStoreConcurrencyFixture struct {
	ctx                  context.Context
	database             *sql.DB
	candidate            Candidate
	store                *PostgresStore
	authorityRelationOID int64
}

func newPostgresStoreConcurrencyFixture(t *testing.T) postgresStoreConcurrencyFixture {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	schema := "workflow_input_store_concurrency_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		_ = base.Close()
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
		_ = base.Close()
		cancel()
	})

	database, err := sql.Open("pgx", postgresStoreCanaryDSN(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(12)
	t.Cleanup(func() { _ = database.Close() })
	if err := migrations.Up(ctx, database); err != nil {
		t.Fatalf("apply migrations in temporary schema: %v", err)
	}
	candidate := postgresStoreCanaryCandidate(t)
	candidate = bindPostgresStoreCanaryTemplate(t, ctx, database, candidate)
	seedPostgresStoreCanary(t, ctx, database, candidate)
	candidate = bindPostgresStoreCanaryPolicy(t, ctx, database, candidate)
	store, err := NewPostgresStore(database)
	if err != nil {
		t.Fatal(err)
	}
	var authorityRelationOID int64
	if err := database.QueryRowContext(ctx, `
SELECT relation.oid::bigint
FROM pg_catalog.pg_class AS relation
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE namespace.nspname = $1
  AND relation.relname = 'workflow_input_authorities'`, schema).Scan(&authorityRelationOID); err != nil {
		t.Fatalf("resolve Workflow Input authority relation OID: %v", err)
	}
	return postgresStoreConcurrencyFixture{
		ctx: ctx, database: database, candidate: candidate, store: store,
		authorityRelationOID: authorityRelationOID,
	}
}

type postgresStoreConcurrencyAttempt struct {
	connection  *sql.Conn
	transaction *sql.Tx
	token       *PostgresTransaction
	backendPID  int
}

func beginPostgresStoreConcurrencyAttempt(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) *postgresStoreConcurrencyAttempt {
	t.Helper()
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var backendPID int
	if err := connection.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&backendPID); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	token, err := NewPostgresTransaction(transaction)
	if err != nil {
		_ = transaction.Rollback()
		_ = connection.Close()
		t.Fatal(err)
	}
	return &postgresStoreConcurrencyAttempt{
		connection: connection, transaction: transaction, token: token, backendPID: backendPID,
	}
}

func (attempt *postgresStoreConcurrencyAttempt) close() {
	if attempt == nil {
		return
	}
	_ = attempt.transaction.Rollback()
	_ = attempt.connection.Close()
}

type postgresStoreConcurrencyResult struct {
	index  int
	record Record
	err    error
}

func receivePostgresStoreConcurrencyResult(
	t *testing.T,
	result <-chan postgresStoreConcurrencyResult,
	operation string,
) postgresStoreConcurrencyResult {
	t.Helper()
	select {
	case outcome := <-result:
		return outcome
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not complete", operation)
		return postgresStoreConcurrencyResult{}
	}
}

func waitForPostgresStoreFenceBeforeInspect(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	backendPID int,
	authorityRelationOID int64,
	result <-chan postgresStoreConcurrencyResult,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case outcome := <-result:
			t.Fatalf("Store Freeze completed before the fenced inspect wait: %v", outcome.err)
		default:
		}
		var sharedFence, inspectWaiting bool
		if err := database.QueryRowContext(ctx, `
WITH expected AS (
  SELECT pg_catalog.hashtextextended($3, 0) AS lock_key
)
SELECT
  EXISTS (
    SELECT 1
    FROM pg_catalog.pg_locks AS lock
    CROSS JOIN expected
    WHERE lock.pid = $1
      AND lock.locktype = 'advisory'
      AND lock.mode = 'ShareLock'
      AND lock.granted
      AND lock.objsubid = 1
      AND lock.classid = (((expected.lock_key >> 32) & 4294967295)::oid)
      AND lock.objid = ((expected.lock_key & 4294967295)::oid)
  ),
  EXISTS (
    SELECT 1
    FROM pg_catalog.pg_locks AS lock
    WHERE lock.pid = $1
      AND lock.locktype = 'relation'
      AND lock.relation = $2::oid
      AND lock.mode = 'AccessShareLock'
      AND NOT lock.granted
  )`, backendPID, authorityRelationOID, workflowInputAuthorityMigrationAdvisoryKey).Scan(
			&sharedFence, &inspectWaiting,
		); err != nil {
			t.Fatal(err)
		}
		if sharedFence && inspectWaiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Store backend %d did not acquire the shared migration fence before its authority inspect", backendPID)
}

func waitForPostgresStoreAdvisoryLock(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	backendPID int,
	mode string,
	granted bool,
	result <-chan error,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-result:
			t.Fatalf("advisory-lock operation completed before expected state: %v", err)
		default:
		}
		if postgresStoreAdvisoryLockMatches(t, ctx, database, backendPID, mode, granted) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("backend %d did not expose advisory %s granted=%t", backendPID, mode, granted)
}

func assertPostgresStoreAdvisoryLock(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	backendPID int,
	mode string,
	granted bool,
) {
	t.Helper()
	if !postgresStoreAdvisoryLockMatches(t, ctx, database, backendPID, mode, granted) {
		t.Fatalf("backend %d advisory %s granted=%t is absent", backendPID, mode, granted)
	}
}

func postgresStoreAdvisoryLockMatches(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	backendPID int,
	mode string,
	granted bool,
) bool {
	t.Helper()
	var matches bool
	if err := database.QueryRowContext(ctx, `
WITH expected AS (
  SELECT pg_catalog.hashtextextended($3, 0) AS lock_key
)
SELECT EXISTS (
  SELECT 1
  FROM pg_catalog.pg_locks AS lock
  CROSS JOIN expected
  WHERE lock.pid = $1
    AND lock.locktype = 'advisory'
    AND lock.mode = $2
    AND lock.granted = $4
    AND lock.objsubid = 1
    AND lock.classid = (((expected.lock_key >> 32) & 4294967295)::oid)
    AND lock.objid = ((expected.lock_key & 4294967295)::oid)
)`, backendPID, mode, workflowInputAuthorityMigrationAdvisoryKey, granted).Scan(&matches); err != nil {
		t.Fatal(err)
	}
	return matches
}

func waitForPostgresStoreAnyLock(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	backendPID int,
	result <-chan postgresStoreConcurrencyResult,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case outcome := <-result:
			t.Fatalf("concurrent exact Store freeze completed before winner commit: %v", outcome.err)
		default:
		}
		var waiting bool
		if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM pg_catalog.pg_locks
  WHERE pid = $1 AND NOT granted
)`, backendPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("concurrent exact Store backend %d did not expose a lock wait", backendPID)
}
