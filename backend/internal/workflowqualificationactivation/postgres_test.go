package workflowqualificationactivation

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/workflowinputauthority"
)

func TestPostgresContractPinsWIAFirstAndMigration78ActivationOrder(t *testing.T) {
	// The production method contains no Query/Exec between BeginTx and Freeze;
	// pin the DML statements themselves so later refactors cannot merge or
	// reorder the migration-78 closure writes casually.
	for name, query := range map[string]string{
		"node":   postgresActivateNodeQuery,
		"event":  postgresInsertActivationEventQuery,
		"outbox": postgresInsertActivationOutboxQuery,
		"run":    postgresActivateRunQuery,
	} {
		if strings.TrimSpace(query) == "" {
			t.Fatalf("%s activation statement is absent", name)
		}
	}
	if !strings.Contains(postgresActivateNodeQuery, "source_event.id = $1") ||
		!strings.Contains(postgresActivateNodeQuery, "source_event.event_type = 'node.completed'") ||
		!strings.Contains(postgresInsertActivationEventQuery, "external_qualification_activated") ||
		!strings.Contains(postgresInsertActivationOutboxQuery, "worksflow.workflow.run.event") ||
		!strings.Contains(postgresActivateRunQuery, "event_cursor = $2") ||
		postgresImmediateConstraintsQuery != "SET CONSTRAINTS ALL IMMEDIATE" {
		t.Fatal("activation SQL contract drifted")
	}
	if !strings.Contains(postgresInspectActivationClosureQuery, "gate.input_authority_id = $7") ||
		!strings.Contains(postgresInspectActivationClosureQuery, "activation_event.id = $9") ||
		!strings.Contains(postgresInspectActivationClosureQuery, "source_event.id = $1") ||
		!strings.Contains(postgresInspectActivationClosureQuery, "outbox.id = activation_event.id") {
		t.Fatal("commit-unknown inspection does not bind the exact source, authority, event, and outbox closure")
	}
}

func TestPostgresRetryClassificationIsClosed(t *testing.T) {
	for _, code := range []string{"40001", "40P01", "WIA01"} {
		if !isDefinitePostgresRetryable(&pgconn.PgError{Code: code}) {
			t.Fatalf("SQLSTATE %s was not retryable", code)
		}
	}
	wrappedWIA01 := errors.Join(
		workflowinputauthority.ErrConflict,
		&pgconn.PgError{Code: "WIA01"},
	)
	if !isDefinitePostgresRetryable(wrappedWIA01) ||
		!errors.Is(classifyPostgresError(wrappedWIA01), ErrRetryable) {
		t.Fatalf("migration-78 wrapped WIA01 was not retained as bounded retryable: %v", wrappedWIA01)
	}
	for _, err := range []error{
		&pgconn.PgError{Code: "23505"}, errors.New("connection lost"), ErrConflict,
	} {
		if isDefinitePostgresRetryable(err) {
			t.Fatalf("ambiguous/permanent error was retryable: %v", err)
		}
	}
}

func TestNewPostgresStoreRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewPostgresStore(nil, PostgresStoreConfig{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewPostgresStore(nil) error = %v", err)
	}
}

func TestActivationClosureUsesExactNodeEventOutboxRunConstraintOrder(t *testing.T) {
	runID := uuid.MustParse("30303030-3030-4030-8030-303030303030")
	qualityID := uuid.MustParse("40404040-4040-4040-8040-404040404040")
	gateID := uuid.MustParse("50505050-5050-4050-8050-505050505050")
	authorityID := uuid.MustParse("60606060-6060-4060-8060-606060606060")
	activationID := uuid.MustParse("70707070-7070-4070-8070-707070707070")
	harness := &activationSQLHarness{
		runID: runID, gateID: gateID, activationID: activationID,
	}
	database := sql.OpenDB(activationSQLConnector{harness: harness})
	defer database.Close()
	transaction, err := database.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	facts := activationFacts{
		qualityNodeRunID: qualityID, qualityNodeKey: "release-quality",
		activationEventID: activationID,
	}
	facts.completionEventID, _ = ParseCompletionEventID(testCompletionEventID)
	authority := workflowinputauthority.Record{
		AuthorityID: authorityID, WorkflowRunID: runID, NodeRunID: gateID,
		Request: workflowinputauthority.FreezeRequest{ExpectedRunCursor: 7},
		Input: workflowinputauthority.WorkflowInputDocument{
			Project: workflowinputauthority.ProjectBinding{ID: "20202020-2020-4020-8020-202020202020"},
			Gate: workflowinputauthority.GateBinding{
				ActivationEventID: activationID.String(), ActivationEventSequence: 8,
				NodeKey: workflowinputauthority.ExternalQualificationGate,
			},
		},
	}
	if err := activateWorkflowClosure(context.Background(), transaction, facts, authority); err != nil {
		t.Fatal(err)
	}
	_ = transaction.Rollback()
	want := []string{
		"UPDATE workflow_node_runs AS gate",
		"INSERT INTO workflow_run_events",
		"INSERT INTO outbox_events",
		"UPDATE workflow_runs AS run",
		"SET CONSTRAINTS ALL IMMEDIATE",
		"SELECT\n  NOT pg_catalog.pg_is_in_recovery()",
	}
	if got := harness.snapshot(); len(got) != len(want) {
		t.Fatalf("statement order = %v", got)
	} else {
		for index := range want {
			if !strings.HasPrefix(got[index], want[index]) {
				t.Fatalf("statement %d = %q, want prefix %q", index, got[index], want[index])
			}
		}
	}
}

func TestActivateAttemptConvertsMigration78WIA01ToBoundedAttemptRetry(t *testing.T) {
	database := sql.OpenDB(wia01BeginConnector{})
	defer database.Close()
	store, err := NewPostgresStore(database, PostgresStoreConfig{MaxTransactionRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.activateAttempt(
		context.Background(), workflowinputauthority.Candidate{}, activationFacts{},
	)
	if !errors.Is(err, errPostgresRetryableAttempt) {
		t.Fatalf("activateAttempt() error = %v, want the bounded transaction retry sentinel", err)
	}
}

func TestRollbackCleanupAndAmbiguousConnectionDiscardAreBounded(t *testing.T) {
	if got := (&PostgresStore{cleanupTimeout: time.Duration(1<<63 - 1)}).cleanupDuration(); got != postgresCleanupTimeout {
		t.Fatalf("overflowing cleanup duration = %s, want %s", got, postgresCleanupTimeout)
	}
	harness := &blockingRollbackHarness{
		rollbackStarted: make(chan struct{}), rollbackRelease: make(chan struct{}),
		physicalClosed: make(chan struct{}),
	}
	database := sql.OpenDB(blockingRollbackConnector{harness: harness})
	defer database.Close()
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := connection.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	started := time.Now()
	rollbackErr := rollbackTransaction(cleanupCtx, transaction)
	cancel()
	if !errors.Is(rollbackErr, ErrStoreOutcomeUnknown) || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("bounded Rollback() error=%v elapsed=%s", rollbackErr, time.Since(started))
	}
	select {
	case <-harness.rollbackStarted:
	default:
		t.Fatal("driver Rollback was not attempted")
	}

	started = time.Now()
	releaseErr := releasePostgresConnection(connection, true, 20*time.Millisecond)
	if !errors.Is(releaseErr, ErrStoreOutcomeUnknown) || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("bounded connection discard error=%v elapsed=%s", releaseErr, time.Since(started))
	}
	close(harness.rollbackRelease)
	select {
	case <-harness.physicalClosed:
	case <-time.After(time.Second):
		t.Fatal("ambiguous physical connection was not eventually poisoned and closed")
	}
}

type activationSQLHarness struct {
	mu           sync.Mutex
	queries      []string
	runID        uuid.UUID
	gateID       uuid.UUID
	activationID uuid.UUID
}

func (harness *activationSQLHarness) record(query string) {
	harness.mu.Lock()
	defer harness.mu.Unlock()
	harness.queries = append(harness.queries, strings.TrimSpace(query))
}

func (harness *activationSQLHarness) snapshot() []string {
	harness.mu.Lock()
	defer harness.mu.Unlock()
	return append([]string(nil), harness.queries...)
}

type activationSQLConnector struct{ harness *activationSQLHarness }

func (connector activationSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return &activationSQLConnection{harness: connector.harness}, nil
}

func (connector activationSQLConnector) Driver() driver.Driver {
	return activationSQLDriver{harness: connector.harness}
}

type activationSQLDriver struct{ harness *activationSQLHarness }

func (driver activationSQLDriver) Open(string) (driver.Conn, error) {
	return &activationSQLConnection{harness: driver.harness}, nil
}

type activationSQLConnection struct{ harness *activationSQLHarness }

func (connection *activationSQLConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare is not supported")
}

func (connection *activationSQLConnection) Close() error { return nil }
func (connection *activationSQLConnection) Begin() (driver.Tx, error) {
	return &activationSQLTransaction{}, nil
}
func (connection *activationSQLConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &activationSQLTransaction{}, nil
}

func (connection *activationSQLConnection) QueryContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	connection.harness.record(query)
	trimmed := strings.TrimSpace(query)
	switch {
	case strings.HasPrefix(trimmed, "UPDATE workflow_node_runs AS gate"):
		return &activationSQLRows{columns: []string{"id"}, values: [][]driver.Value{{connection.harness.gateID.String()}}}, nil
	case strings.HasPrefix(trimmed, "INSERT INTO workflow_run_events"):
		return &activationSQLRows{columns: []string{"created_at"}, values: [][]driver.Value{{time.Date(2026, 7, 20, 5, 0, 0, 0, time.UTC)}}}, nil
	case strings.HasPrefix(trimmed, "INSERT INTO outbox_events"):
		return &activationSQLRows{columns: []string{"id"}, values: [][]driver.Value{{connection.harness.activationID.String()}}}, nil
	case strings.HasPrefix(trimmed, "UPDATE workflow_runs AS run"):
		return &activationSQLRows{columns: []string{"id"}, values: [][]driver.Value{{connection.harness.runID.String()}}}, nil
	case strings.HasPrefix(trimmed, "SELECT\n  NOT pg_catalog.pg_is_in_recovery()"):
		return &activationSQLRows{columns: []string{"primary", "exact"}, values: [][]driver.Value{{true, true}}}, nil
	default:
		return nil, errors.New("unexpected activation test query")
	}
}

func (connection *activationSQLConnection) ExecContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	connection.harness.record(query)
	if strings.TrimSpace(query) != postgresImmediateConstraintsQuery {
		return nil, errors.New("unexpected activation test exec")
	}
	return driver.RowsAffected(0), nil
}

type activationSQLTransaction struct{}

func (*activationSQLTransaction) Commit() error   { return nil }
func (*activationSQLTransaction) Rollback() error { return nil }

type activationSQLRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (rows *activationSQLRows) Columns() []string { return rows.columns }
func (rows *activationSQLRows) Close() error      { return nil }
func (rows *activationSQLRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

var _ driver.Connector = activationSQLConnector{}
var _ driver.QueryerContext = (*activationSQLConnection)(nil)
var _ driver.ExecerContext = (*activationSQLConnection)(nil)
var _ driver.ConnBeginTx = (*activationSQLConnection)(nil)

type blockingRollbackHarness struct {
	rollbackStarted chan struct{}
	rollbackRelease chan struct{}
	physicalClosed  chan struct{}
	startedOnce     sync.Once
	closedOnce      sync.Once
}

type blockingRollbackConnector struct{ harness *blockingRollbackHarness }

func (connector blockingRollbackConnector) Connect(context.Context) (driver.Conn, error) {
	return &blockingRollbackConnection{harness: connector.harness}, nil
}

func (connector blockingRollbackConnector) Driver() driver.Driver {
	return blockingRollbackDriver{harness: connector.harness}
}

type blockingRollbackDriver struct{ harness *blockingRollbackHarness }

func (value blockingRollbackDriver) Open(string) (driver.Conn, error) {
	return &blockingRollbackConnection{harness: value.harness}, nil
}

type blockingRollbackConnection struct{ harness *blockingRollbackHarness }

func (*blockingRollbackConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare is not supported")
}
func (connection *blockingRollbackConnection) Close() error {
	connection.harness.closedOnce.Do(func() { close(connection.harness.physicalClosed) })
	return nil
}
func (connection *blockingRollbackConnection) Begin() (driver.Tx, error) {
	return &blockingRollbackTransaction{harness: connection.harness}, nil
}
func (connection *blockingRollbackConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &blockingRollbackTransaction{harness: connection.harness}, nil
}

type blockingRollbackTransaction struct{ harness *blockingRollbackHarness }

func (*blockingRollbackTransaction) Commit() error { return nil }
func (transaction *blockingRollbackTransaction) Rollback() error {
	transaction.harness.startedOnce.Do(func() { close(transaction.harness.rollbackStarted) })
	<-transaction.harness.rollbackRelease
	return nil
}

var _ driver.Connector = blockingRollbackConnector{}
var _ driver.ConnBeginTx = (*blockingRollbackConnection)(nil)

type wia01BeginConnector struct{}

func (wia01BeginConnector) Connect(context.Context) (driver.Conn, error) {
	return &wia01BeginConnection{}, nil
}

func (wia01BeginConnector) Driver() driver.Driver { return wia01BeginDriver{} }

type wia01BeginDriver struct{}

func (wia01BeginDriver) Open(string) (driver.Conn, error) { return &wia01BeginConnection{}, nil }

type wia01BeginConnection struct{}

func (*wia01BeginConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare is not supported")
}
func (*wia01BeginConnection) Close() error { return nil }
func (*wia01BeginConnection) Begin() (driver.Tx, error) {
	return nil, errors.Join(
		workflowinputauthority.ErrConflict,
		&pgconn.PgError{Code: "WIA01"},
	)
}
func (connection *wia01BeginConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return connection.Begin()
}

var _ driver.Connector = wia01BeginConnector{}
var _ driver.ConnBeginTx = (*wia01BeginConnection)(nil)
