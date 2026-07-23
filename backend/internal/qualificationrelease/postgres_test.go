package qualificationrelease

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresMutationRetriesOnlyDefiniteAbortWithSameArguments(t *testing.T) {
	harness := &postgresHarness{
		queryErrors: []error{&pgconn.PgError{Code: "40001", Message: "serialization"}, nil},
		rows:        [][]byte{[]byte(`{"ok":true}`)},
	}
	database := openPostgresHarness(t, harness)
	store, err := NewPostgresStore(database, PostgresStoreConfig{MaxTransactionRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := store.mutate(context.Background(), "qualified mutation", "stable-id")
	if err != nil || string(encoded) != `{"ok":true}` {
		t.Fatalf("mutate() = %s, %v", encoded, err)
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	if harness.begins != 2 || harness.queries != 2 || harness.commits != 1 ||
		harness.rollbacks != 1 || harness.isolation != driver.IsolationLevel(sql.LevelSerializable) {
		t.Fatalf("retry accounting = %+v", harness)
	}
	if len(harness.arguments) != 2 || harness.arguments[0][0].Value != "stable-id" ||
		harness.arguments[1][0].Value != "stable-id" {
		t.Fatalf("retry changed immutable arguments: %#v", harness.arguments)
	}
}

func TestPostgresMutationDiscardsConnectionAfterUnknownCommit(t *testing.T) {
	harness := &postgresHarness{rows: [][]byte{[]byte(`{"ok":true}`)}, commitErrors: []error{errors.New("acknowledgement lost")}}
	database := openPostgresHarness(t, harness)
	store, err := NewPostgresStore(database, PostgresStoreConfig{MaxTransactionRetries: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.mutate(context.Background(), "qualified mutation", "stable-id"); !errors.Is(err, ErrStoreOutcomeUnknown) || errors.Is(err, ErrRetryable) {
		t.Fatalf("unknown commit error = %v", err)
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	// database/sql marks a transaction done after Commit returns, including
	// when the driver's acknowledgement is lost. A subsequent Rollback then
	// returns sql.ErrTxDone without invoking the driver. Safety comes from
	// poisoning and closing the underlying connection, not a second driver
	// transaction call.
	if harness.queries != 1 || harness.commits != 1 || harness.rollbacks != 0 ||
		harness.closes == 0 {
		t.Fatalf("unknown commit was retried or pooled: %+v", harness)
	}
}

func TestPostgresMutationTreatsTransportQueryOutcomeAsUnknownAndDiscards(t *testing.T) {
	harness := &postgresHarness{queryErrors: []error{errors.New("connection vanished")}}
	database := openPostgresHarness(t, harness)
	store, err := NewPostgresStore(database, PostgresStoreConfig{MaxTransactionRetries: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.mutate(context.Background(), "qualified mutation", "stable-id"); !errors.Is(err, ErrStoreOutcomeUnknown) || errors.Is(err, ErrRetryable) {
		t.Fatalf("transport query error = %v", err)
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	if harness.queries != 1 || harness.commits != 0 || harness.rollbacks != 1 || harness.closes == 0 {
		t.Fatalf("unknown query was retried or pooled: %+v", harness)
	}
}

type postgresHarness struct {
	mu sync.Mutex

	queryErrors  []error
	commitErrors []error
	rows         [][]byte
	arguments    [][]driver.NamedValue
	begins       int
	queries      int
	commits      int
	rollbacks    int
	closes       int
	isolation    driver.IsolationLevel
}

func openPostgresHarness(t *testing.T, harness *postgresHarness) *sql.DB {
	t.Helper()
	database := sql.OpenDB(postgresHarnessConnector{harness: harness})
	t.Cleanup(func() { _ = database.Close() })
	return database
}

type postgresHarnessConnector struct{ harness *postgresHarness }

func (connector postgresHarnessConnector) Connect(context.Context) (driver.Conn, error) {
	return &postgresHarnessConnection{harness: connector.harness}, nil
}

func (connector postgresHarnessConnector) Driver() driver.Driver {
	return postgresHarnessDriver{harness: connector.harness}
}

type postgresHarnessDriver struct{ harness *postgresHarness }

func (driverValue postgresHarnessDriver) Open(string) (driver.Conn, error) {
	return &postgresHarnessConnection{harness: driverValue.harness}, nil
}

type postgresHarnessConnection struct{ harness *postgresHarness }

func (connection *postgresHarnessConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (connection *postgresHarnessConnection) Begin() (driver.Tx, error) {
	return connection.BeginTx(context.Background(), driver.TxOptions{})
}

func (connection *postgresHarnessConnection) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	connection.harness.mu.Lock()
	defer connection.harness.mu.Unlock()
	connection.harness.begins++
	connection.harness.isolation = options.Isolation
	return &postgresHarnessTransaction{harness: connection.harness}, nil
}

func (connection *postgresHarnessConnection) QueryContext(
	_ context.Context,
	_ string,
	arguments []driver.NamedValue,
) (driver.Rows, error) {
	connection.harness.mu.Lock()
	defer connection.harness.mu.Unlock()
	connection.harness.queries++
	copyArguments := append([]driver.NamedValue(nil), arguments...)
	connection.harness.arguments = append(connection.harness.arguments, copyArguments)
	if len(connection.harness.queryErrors) > 0 {
		err := connection.harness.queryErrors[0]
		connection.harness.queryErrors = connection.harness.queryErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	if len(connection.harness.rows) == 0 {
		return &postgresHarnessRows{}, nil
	}
	row := append([]byte(nil), connection.harness.rows[0]...)
	connection.harness.rows = connection.harness.rows[1:]
	return &postgresHarnessRows{values: [][]byte{row}}, nil
}

func (connection *postgresHarnessConnection) Close() error {
	connection.harness.mu.Lock()
	defer connection.harness.mu.Unlock()
	connection.harness.closes++
	return nil
}

type postgresHarnessTransaction struct{ harness *postgresHarness }

func (transaction *postgresHarnessTransaction) Commit() error {
	transaction.harness.mu.Lock()
	defer transaction.harness.mu.Unlock()
	transaction.harness.commits++
	if len(transaction.harness.commitErrors) == 0 {
		return nil
	}
	err := transaction.harness.commitErrors[0]
	transaction.harness.commitErrors = transaction.harness.commitErrors[1:]
	return err
}

func (transaction *postgresHarnessTransaction) Rollback() error {
	transaction.harness.mu.Lock()
	defer transaction.harness.mu.Unlock()
	transaction.harness.rollbacks++
	return nil
}

type postgresHarnessRows struct {
	values [][]byte
	index  int
}

func (*postgresHarnessRows) Columns() []string { return []string{"value"} }
func (*postgresHarnessRows) Close() error      { return nil }

func (rows *postgresHarnessRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	destination[0] = rows.values[rows.index]
	rows.index++
	return nil
}

var _ driver.ConnBeginTx = (*postgresHarnessConnection)(nil)
var _ driver.QueryerContext = (*postgresHarnessConnection)(nil)
