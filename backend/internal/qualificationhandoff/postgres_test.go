package qualificationhandoff

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestNewPostgresStoreRequiresVerifiedSessionAffinity(t *testing.T) {
	database := openHandoffPostgresTestDatabase(t, newHandoffPostgresHarness(t))
	for name, config := range map[string]PostgresStoreConfig{
		"zero":             {},
		"unverified":       {SessionAffinityMode: PostgresSessionAffinityUnverified},
		"transaction pool": {SessionAffinityMode: PostgresSessionAffinityTransactionPool},
		"unknown":          {SessionAffinityMode: "unknown"},
		"negative retries": {
			SessionAffinityMode: PostgresSessionAffinityDirect, MaxTransactionRetries: -1,
		},
		"excessive retries": {
			SessionAffinityMode:   PostgresSessionAffinityDirect,
			MaxTransactionRetries: maximumPostgresTransactionRetries + 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewPostgresStore(database, config); !errors.Is(err, ErrInvalid) {
				t.Fatalf("NewPostgresStore() error = %v", err)
			}
		})
	}
	if _, err := NewPostgresStore(nil, PostgresStoreConfig{SessionAffinityMode: PostgresSessionAffinityDirect}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewPostgresStore(nil) error = %v", err)
	}
	for _, mode := range []PostgresSessionAffinityMode{PostgresSessionAffinityDirect, PostgresSessionAffinitySessionPool} {
		store, err := NewPostgresStore(database, PostgresStoreConfig{SessionAffinityMode: mode})
		if err != nil || store.maxTransactionRetries != defaultPostgresTransactionRetries {
			t.Fatalf("NewPostgresStore(%q) = %#v, %v", mode, store, err)
		}
	}
}

func TestPostgresStoreReadinessUsesExactOperatorInspectionCapability(t *testing.T) {
	harness := newHandoffPostgresHarness(t)
	store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 1)
	if err := store.Readiness(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := harness.snapshot()
	if snapshot.inspects != 1 || snapshot.primaryChecks != 1 || snapshot.completes != 0 ||
		snapshot.begins != 0 || snapshot.acquires != 0 {
		t.Fatalf("readiness accounting = %#v", snapshot)
	}
	if len(snapshot.inspectArguments) != 1 || len(snapshot.inspectArguments[0]) != 1 ||
		fmt.Sprint(snapshot.inspectArguments[0][0]) != postgresReadinessProbeHandoffID {
		t.Fatalf("readiness probe arguments = %#v", snapshot.inspectArguments)
	}

	harness = newHandoffPostgresHarness(t)
	harness.primaryResults = []bool{false}
	store = newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 1)
	if err := store.Readiness(context.Background()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("standby readiness error = %v", err)
	}
}

func TestPostgresStoreCompleteUsesOneSessionSerializableAttempt(t *testing.T) {
	harness := newHandoffPostgresHarness(t)
	store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 1)
	record, err := store.Complete(context.Background(), harness.record.HandoffID)
	if err != nil || !SameImmutableRecord(record, harness.record) {
		t.Fatalf("Complete() = %#v, %v", record, err)
	}
	snapshot := harness.snapshot()
	if snapshot.acquires != 1 || snapshot.begins != 1 || snapshot.primaryChecks != 1 ||
		snapshot.completes != 1 || snapshot.commits != 1 || snapshot.rollbacks != 0 || snapshot.unlocks != 1 ||
		snapshot.isolation != driver.IsolationLevel(sql.LevelSerializable) {
		t.Fatalf("attempt accounting = %#v", snapshot)
	}
	if strings.Join(snapshot.events, ",") != "acquire,begin,primary,complete,commit,unlock" {
		t.Fatalf("session operation order = %v", snapshot.events)
	}
	if len(snapshot.arguments) != 1 || len(snapshot.arguments[0]) != 1 ||
		fmt.Sprint(snapshot.arguments[0][0]) != harness.record.HandoffID.String() {
		t.Fatalf("complete arguments = %#v", snapshot.arguments)
	}
}

func TestPostgresStoreCompleteRequiresPrimaryReadWrite(t *testing.T) {
	harness := newHandoffPostgresHarness(t)
	harness.primaryResults = []bool{false}
	store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 1)
	if _, err := store.Complete(context.Background(), harness.record.HandoffID); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Complete() error = %v", err)
	}
	snapshot := harness.snapshot()
	if snapshot.completes != 0 || snapshot.commits != 0 || snapshot.rollbacks != 1 || snapshot.unlocks != 1 {
		t.Fatalf("non-primary accounting = %#v", snapshot)
	}
}

func TestPostgresStoreRetriesOnlyDefiniteAborts(t *testing.T) {
	t.Run("query abort", func(t *testing.T) {
		harness := newHandoffPostgresHarness(t)
		harness.completeErrors = []error{&pgconn.PgError{Code: "40001", Message: "serialization"}}
		store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 1)
		if record, err := store.Complete(context.Background(), harness.record.HandoffID); err != nil || !SameImmutableRecord(record, harness.record) {
			t.Fatalf("Complete() = %#v, %v", record, err)
		}
		snapshot := harness.snapshot()
		if snapshot.acquires != 2 || snapshot.begins != 2 || snapshot.completes != 2 ||
			snapshot.commits != 1 || snapshot.rollbacks != 1 || snapshot.unlocks != 2 {
			t.Fatalf("query retry accounting = %#v", snapshot)
		}
	})

	t.Run("commit abort", func(t *testing.T) {
		harness := newHandoffPostgresHarness(t)
		harness.commitErrors = []error{&pgconn.PgError{Code: "40P01", Message: "deadlock"}}
		store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 1)
		if _, err := store.Complete(context.Background(), harness.record.HandoffID); err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		snapshot := harness.snapshot()
		if snapshot.acquires != 2 || snapshot.completes != 2 || snapshot.commits != 2 || snapshot.unlocks != 2 {
			t.Fatalf("commit retry accounting = %#v", snapshot)
		}
	})

	t.Run("bounded", func(t *testing.T) {
		harness := newHandoffPostgresHarness(t)
		harness.completeErrors = []error{
			&pgconn.PgError{Code: "40001", Message: "serialization"},
			&pgconn.PgError{Code: "40P01", Message: "deadlock"},
		}
		store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 1)
		if _, err := store.Complete(context.Background(), harness.record.HandoffID); !errors.Is(err, ErrRetryable) || errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("Complete() error = %v", err)
		}
		if snapshot := harness.snapshot(); snapshot.completes != 2 || snapshot.unlocks != 2 {
			t.Fatalf("bounded retry accounting = %#v", snapshot)
		}
	})
}

func TestPostgresStoreNeverRetriesUnknownCommitOrCleanup(t *testing.T) {
	t.Run("ambiguous commit", func(t *testing.T) {
		harness := newHandoffPostgresHarness(t)
		harness.commitErrors = []error{errors.New("commit acknowledgement lost")}
		store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 3)
		if _, err := store.Complete(context.Background(), harness.record.HandoffID); !errors.Is(err, ErrStoreOutcomeUnknown) || errors.Is(err, ErrRetryable) {
			t.Fatalf("Complete() error = %v", err)
		}
		if snapshot := harness.snapshot(); snapshot.completes != 1 || snapshot.commits != 1 ||
			snapshot.unlocks != 0 || snapshot.closes == 0 {
			t.Fatalf("ambiguous commit accounting = %#v", snapshot)
		}
	})

	t.Run("post-commit unlock false", func(t *testing.T) {
		harness := newHandoffPostgresHarness(t)
		harness.unlockResults = []bool{false}
		store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 3)
		if _, err := store.Complete(context.Background(), harness.record.HandoffID); !errors.Is(err, ErrStoreOutcomeUnknown) ||
			errors.Is(err, ErrRetryable) {
			t.Fatalf("Complete() error = %v", err)
		}
		if snapshot := harness.snapshot(); snapshot.commits != 1 || snapshot.unlocks != 1 || snapshot.closes == 0 {
			t.Fatalf("post-commit unlock failure was pooled = %#v", snapshot)
		}
	})

	for name, configure := range map[string]func(*handoffPostgresHarness){
		"unlock false": func(harness *handoffPostgresHarness) { harness.unlockResults = []bool{false} },
		"unlock error": func(harness *handoffPostgresHarness) {
			harness.unlockErrors = []error{errors.New("unlock result lost")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			harness := newHandoffPostgresHarness(t)
			harness.completeErrors = []error{&pgconn.PgError{Code: "40001", Message: "serialization"}}
			configure(harness)
			store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 3)
			if _, err := store.Complete(context.Background(), harness.record.HandoffID); !errors.Is(err, ErrStoreOutcomeUnknown) || errors.Is(err, ErrRetryable) {
				t.Fatalf("Complete() error = %v", err)
			}
			snapshot := harness.snapshot()
			if snapshot.completes != 1 || snapshot.unlocks != 1 || snapshot.closes == 0 {
				t.Fatalf("unknown cleanup was retried or pooled = %#v", snapshot)
			}
		})
	}
}

func TestServiceRecoversUnknownPostgresCommitOnFreshConnection(t *testing.T) {
	harness := newHandoffPostgresHarness(t)
	harness.commitErrors = []error{errors.New("commit acknowledgement lost")}
	store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 3)
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Complete(context.Background(), harness.record.HandoffID)
	if err != nil || !SameImmutableRecord(record, harness.record) || !record.Idempotent {
		t.Fatalf("Service.Complete() = %#v, %v", record, err)
	}
	snapshot := harness.snapshot()
	if snapshot.completes != 1 || snapshot.commits != 1 || snapshot.inspects != 1 ||
		snapshot.unlocks != 0 || snapshot.opens < 2 || snapshot.closes == 0 {
		t.Fatalf("commit-unknown recovery did not use one fresh inspection = %#v", snapshot)
	}
}

func TestPostgresStorePoisonsUnknownBeginAndRollbackStates(t *testing.T) {
	t.Run("begin acknowledgement", func(t *testing.T) {
		harness := newHandoffPostgresHarness(t)
		harness.beginErrors = []error{errors.New("BEGIN acknowledgement lost")}
		store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 3)
		if _, err := store.Complete(context.Background(), harness.record.HandoffID); !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrRetryable) {
			t.Fatalf("Complete() error = %v", err)
		}
		snapshot := harness.snapshot()
		if snapshot.begins != 1 || snapshot.completes != 0 || snapshot.unlocks != 0 || snapshot.closes == 0 {
			t.Fatalf("unknown BEGIN state was pooled or unlocked = %#v", snapshot)
		}
	})

	t.Run("rollback acknowledgement", func(t *testing.T) {
		harness := newHandoffPostgresHarness(t)
		harness.primaryResults = []bool{false}
		harness.rollbackErrors = []error{errors.New("ROLLBACK acknowledgement lost")}
		store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 3)
		if _, err := store.Complete(context.Background(), harness.record.HandoffID); !errors.Is(err, ErrStoreOutcomeUnknown) ||
			errors.Is(err, ErrNotReady) || errors.Is(err, ErrRetryable) {
			t.Fatalf("Complete() error = %v", err)
		}
		snapshot := harness.snapshot()
		if snapshot.rollbacks != 1 || snapshot.unlocks != 0 || snapshot.closes == 0 {
			t.Fatalf("unknown ROLLBACK state was pooled or unlocked = %#v", snapshot)
		}
	})
}

func TestPostgresRollbackCleanupIsActuallyBounded(t *testing.T) {
	if got := (&PostgresStore{cleanupTimeout: time.Duration(1<<63 - 1)}).cleanupDuration(); got != postgresSessionCleanupTimeout {
		t.Fatalf("overflowing cleanup duration = %s, want %s", got, postgresSessionCleanupTimeout)
	}
	harness := &handoffBlockingRollbackHarness{
		started: make(chan struct{}), release: make(chan struct{}),
	}
	database := sql.OpenDB(handoffBlockingRollbackConnector{harness: harness})
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
	startedAt := time.Now()
	rollbackErr := rollbackHandoffTransaction(cleanupCtx, transaction)
	cancel()
	if !errors.Is(rollbackErr, ErrStoreOutcomeUnknown) || time.Since(startedAt) > 250*time.Millisecond {
		t.Fatalf("bounded Rollback() error=%v elapsed=%s", rollbackErr, time.Since(startedAt))
	}
	select {
	case <-harness.started:
	default:
		t.Fatal("driver Rollback was not attempted")
	}
	close(harness.release)
	if err := releaseHandoffPostgresConnection(connection, true, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStorePoisonsUnknownPreBeginLockState(t *testing.T) {
	harness := newHandoffPostgresHarness(t)
	harness.acquireErrors = []error{errors.New("lock acknowledgement lost")}
	store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 3)
	if _, err := store.Complete(context.Background(), harness.record.HandoffID); !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrRetryable) {
		t.Fatalf("Complete() error = %v", err)
	}
	snapshot := harness.snapshot()
	if snapshot.acquires != 1 || snapshot.begins != 0 || snapshot.closes == 0 {
		t.Fatalf("unknown pre-BEGIN lock state was not discarded = %#v", snapshot)
	}
}

func TestPostgresStoreRejectsCorruptBundleBeforeCommit(t *testing.T) {
	harness := newHandoffPostgresHarness(t)
	harness.completeEncoded = []byte(`{"schemaVersion":"wrong"}`)
	store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 1)
	if _, err := store.Complete(context.Background(), harness.record.HandoffID); !errors.Is(err, ErrConflict) {
		t.Fatalf("Complete() error = %v", err)
	}
	if snapshot := harness.snapshot(); snapshot.commits != 0 || snapshot.rollbacks != 1 || snapshot.unlocks != 1 {
		t.Fatalf("corrupt bundle accounting = %#v", snapshot)
	}
}

func TestPostgresStoreInspectUsesOnePrimaryReadWriteStatement(t *testing.T) {
	harness := newHandoffPostgresHarness(t)
	store := newHandoffPostgresTestStore(t, openHandoffPostgresTestDatabase(t, harness), 1)
	record, err := store.Inspect(context.Background(), harness.record.HandoffID)
	if err != nil || !SameImmutableRecord(record, harness.record) || !record.Idempotent {
		t.Fatalf("Inspect() = %#v, %v", record, err)
	}
	snapshot := harness.snapshot()
	if snapshot.inspects != 1 || snapshot.primaryChecks != 1 || snapshot.begins != 0 || snapshot.acquires != 0 {
		t.Fatalf("Inspect accounting = %#v", snapshot)
	}
	if len(snapshot.inspectQueries) != 1 || !strings.Contains(snapshot.inspectQueries[0], "CASE WHEN posture.primary_read_write") ||
		!strings.Contains(snapshot.inspectQueries[0], "inspect_qualification_promotion_v2_handoff_completion") {
		t.Fatalf("Inspect did not bind posture and authority read in one statement: %q", snapshot.inspectQueries)
	}

	harness.mu.Lock()
	harness.primaryResults = []bool{false}
	harness.mu.Unlock()
	if _, err := store.Inspect(context.Background(), harness.record.HandoffID); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("non-primary Inspect() error = %v", err)
	}

	harness.mu.Lock()
	harness.primaryResults = nil
	harness.inspectEncoded = []byte(`{"schemaVersion":"wrong"}`)
	harness.mu.Unlock()
	if _, err := store.Inspect(context.Background(), harness.record.HandoffID); !errors.Is(err, ErrConflict) {
		t.Fatalf("corrupt Inspect() error = %v", err)
	}
}

func TestHandoffPostgresErrorClassification(t *testing.T) {
	for code, want := range map[string]error{
		"WPH01": ErrInvalid, "WPH02": ErrConflict, "WPH03": ErrNotReady,
		"23503": ErrConflict,
	} {
		if got := classifyPostgresCompleteError(&pgconn.PgError{Code: code}); !errors.Is(got, want) {
			t.Errorf("complete code %s = %v, want %v", code, got, want)
		}
	}
	serialization := &pgconn.PgError{Code: "40001"}
	deadlock := &pgconn.PgError{Code: "40P01"}
	for _, err := range []error{serialization, fmt.Errorf("wrapped: %w", deadlock)} {
		if !isDefinitePostgresRetryable(err) {
			t.Errorf("definite abort was not retryable: %v", err)
		}
	}
	for _, err := range []error{
		context.Canceled, driver.ErrBadConn, errors.New("EOF"),
		errors.Join(serialization, driver.ErrBadConn), errors.Join(deadlock, errors.New("transport")),
	} {
		if isDefinitePostgresRetryable(err) {
			t.Errorf("ambiguous error was retryable: %v", err)
		}
	}
}

func newHandoffPostgresTestStore(t *testing.T, database *sql.DB, retries int) *PostgresStore {
	t.Helper()
	store, err := NewPostgresStore(database, PostgresStoreConfig{
		SessionAffinityMode: PostgresSessionAffinityDirect, MaxTransactionRetries: retries,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type handoffPostgresHarness struct {
	mu sync.Mutex

	record          Record
	completeEncoded []byte
	inspectEncoded  []byte

	acquireErrors  []error
	beginErrors    []error
	primaryErrors  []error
	primaryResults []bool
	completeErrors []error
	commitErrors   []error
	rollbackErrors []error
	unlockErrors   []error
	unlockResults  []bool

	events           []string
	arguments        [][]driver.Value
	inspectArguments [][]driver.Value
	inspectQueries   []string
	acquires         int
	begins           int
	primaryChecks    int
	completes        int
	inspects         int
	commits          int
	rollbacks        int
	unlocks          int
	opens            int
	closes           int
	isolation        driver.IsolationLevel
}

type handoffPostgresSnapshot struct {
	events           []string
	arguments        [][]driver.Value
	inspectArguments [][]driver.Value
	inspectQueries   []string
	acquires         int
	begins           int
	primaryChecks    int
	completes        int
	inspects         int
	commits          int
	rollbacks        int
	unlocks          int
	opens            int
	closes           int
	isolation        driver.IsolationLevel
}

func newHandoffPostgresHarness(t *testing.T) *handoffPostgresHarness {
	t.Helper()
	record := testRecord(t)
	completeEncoded := encodeCompleteRecord(t, record)
	inspectEncoded := encodeInspectRecord(t, record)
	return &handoffPostgresHarness{
		record: record, completeEncoded: completeEncoded, inspectEncoded: inspectEncoded,
	}
}

func (harness *handoffPostgresHarness) snapshot() handoffPostgresSnapshot {
	harness.mu.Lock()
	defer harness.mu.Unlock()
	arguments := make([][]driver.Value, len(harness.arguments))
	for index := range harness.arguments {
		arguments[index] = append([]driver.Value(nil), harness.arguments[index]...)
	}
	inspectArguments := make([][]driver.Value, len(harness.inspectArguments))
	for index := range harness.inspectArguments {
		inspectArguments[index] = append([]driver.Value(nil), harness.inspectArguments[index]...)
	}
	return handoffPostgresSnapshot{
		events: append([]string(nil), harness.events...), arguments: arguments,
		inspectArguments: inspectArguments,
		inspectQueries:   append([]string(nil), harness.inspectQueries...),
		acquires:         harness.acquires, begins: harness.begins, primaryChecks: harness.primaryChecks,
		completes: harness.completes, inspects: harness.inspects, commits: harness.commits,
		rollbacks: harness.rollbacks, unlocks: harness.unlocks, opens: harness.opens,
		closes: harness.closes, isolation: harness.isolation,
	}
}

func shiftHandoffPostgresError(values *[]error) error {
	if len(*values) == 0 {
		return nil
	}
	value := (*values)[0]
	*values = (*values)[1:]
	return value
}

var (
	handoffPostgresDriverOnce sync.Once
	handoffPostgresHarnesses  sync.Map
	handoffPostgresSequence   atomic.Uint64
)

type handoffBlockingRollbackHarness struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type handoffBlockingRollbackConnector struct {
	harness *handoffBlockingRollbackHarness
}

func (connector handoffBlockingRollbackConnector) Connect(context.Context) (driver.Conn, error) {
	return &handoffBlockingRollbackConnection{harness: connector.harness}, nil
}

func (connector handoffBlockingRollbackConnector) Driver() driver.Driver {
	return handoffBlockingRollbackDriver{harness: connector.harness}
}

type handoffBlockingRollbackDriver struct {
	harness *handoffBlockingRollbackHarness
}

func (driver handoffBlockingRollbackDriver) Open(string) (driver.Conn, error) {
	return &handoffBlockingRollbackConnection{harness: driver.harness}, nil
}

type handoffBlockingRollbackConnection struct {
	harness *handoffBlockingRollbackHarness
}

func (*handoffBlockingRollbackConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare is unsupported")
}
func (*handoffBlockingRollbackConnection) Close() error { return nil }
func (connection *handoffBlockingRollbackConnection) Begin() (driver.Tx, error) {
	return connection.BeginTx(context.Background(), driver.TxOptions{})
}
func (connection *handoffBlockingRollbackConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &handoffBlockingRollbackTransaction{harness: connection.harness}, nil
}

type handoffBlockingRollbackTransaction struct {
	harness *handoffBlockingRollbackHarness
}

func (*handoffBlockingRollbackTransaction) Commit() error { return nil }
func (transaction *handoffBlockingRollbackTransaction) Rollback() error {
	transaction.harness.once.Do(func() { close(transaction.harness.started) })
	<-transaction.harness.release
	return nil
}

var (
	_ driver.Connector   = handoffBlockingRollbackConnector{}
	_ driver.Conn        = (*handoffBlockingRollbackConnection)(nil)
	_ driver.ConnBeginTx = (*handoffBlockingRollbackConnection)(nil)
)

func openHandoffPostgresTestDatabase(t *testing.T, harness *handoffPostgresHarness) *sql.DB {
	t.Helper()
	handoffPostgresDriverOnce.Do(func() {
		sql.Register("qualification-handoff-test", handoffPostgresDriver{})
	})
	name := fmt.Sprintf("handoff-%d", handoffPostgresSequence.Add(1))
	handoffPostgresHarnesses.Store(name, harness)
	database, err := sql.Open("qualification-handoff-test", name)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxIdleConns(1)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		_ = database.Close()
		handoffPostgresHarnesses.Delete(name)
	})
	return database
}

type handoffPostgresDriver struct{}

func (handoffPostgresDriver) Open(name string) (driver.Conn, error) {
	value, ok := handoffPostgresHarnesses.Load(name)
	if !ok {
		return nil, errors.New("unknown Handoff PostgreSQL test harness")
	}
	harness := value.(*handoffPostgresHarness)
	harness.mu.Lock()
	harness.opens++
	harness.mu.Unlock()
	return &handoffPostgresConnection{harness: harness}, nil
}

type handoffPostgresConnection struct{ harness *handoffPostgresHarness }

func (*handoffPostgresConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare is unsupported")
}

func (connection *handoffPostgresConnection) Close() error {
	connection.harness.mu.Lock()
	connection.harness.closes++
	connection.harness.mu.Unlock()
	return nil
}

func (connection *handoffPostgresConnection) Begin() (driver.Tx, error) {
	return connection.BeginTx(context.Background(), driver.TxOptions{})
}

func (connection *handoffPostgresConnection) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	connection.harness.mu.Lock()
	defer connection.harness.mu.Unlock()
	connection.harness.begins++
	connection.harness.events = append(connection.harness.events, "begin")
	connection.harness.isolation = options.Isolation
	if err := shiftHandoffPostgresError(&connection.harness.beginErrors); err != nil {
		return nil, err
	}
	return &handoffPostgresTransaction{harness: connection.harness}, nil
}

func (connection *handoffPostgresConnection) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if !strings.Contains(query, "pg_advisory_lock") || strings.Contains(query, "pg_advisory_unlock") {
		return nil, fmt.Errorf("unexpected exec: %s", query)
	}
	connection.harness.mu.Lock()
	defer connection.harness.mu.Unlock()
	connection.harness.acquires++
	connection.harness.events = append(connection.harness.events, "acquire")
	if err := shiftHandoffPostgresError(&connection.harness.acquireErrors); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (connection *handoffPostgresConnection) QueryContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	connection.harness.mu.Lock()
	defer connection.harness.mu.Unlock()
	switch {
	case strings.Contains(query, "pg_advisory_unlock"):
		connection.harness.unlocks++
		connection.harness.events = append(connection.harness.events, "unlock")
		if err := shiftHandoffPostgresError(&connection.harness.unlockErrors); err != nil {
			return nil, err
		}
		unlocked := true
		if len(connection.harness.unlockResults) > 0 {
			unlocked = connection.harness.unlockResults[0]
			connection.harness.unlockResults = connection.harness.unlockResults[1:]
		}
		return &handoffPostgresRows{values: [][]driver.Value{{unlocked}}}, nil
	case strings.Contains(query, "inspect_qualification_promotion_v2_handoff_completion"):
		connection.harness.inspects++
		connection.harness.primaryChecks++
		connection.harness.events = append(connection.harness.events, "inspect")
		connection.harness.inspectQueries = append(connection.harness.inspectQueries, query)
		values := make([]driver.Value, len(arguments))
		for index := range arguments {
			values[index] = arguments[index].Value
		}
		connection.harness.inspectArguments = append(connection.harness.inspectArguments, values)
		if err := shiftHandoffPostgresError(&connection.harness.primaryErrors); err != nil {
			return nil, err
		}
		primary := true
		if len(connection.harness.primaryResults) > 0 {
			primary = connection.harness.primaryResults[0]
			connection.harness.primaryResults = connection.harness.primaryResults[1:]
		}
		var encoded driver.Value
		if primary && len(connection.harness.inspectEncoded) > 0 {
			encoded = append([]byte(nil), connection.harness.inspectEncoded...)
		}
		return &handoffPostgresRows{
			columns: []string{"primary_read_write", "value"}, values: [][]driver.Value{{primary, encoded}},
		}, nil
	case strings.Contains(query, "pg_is_in_recovery"):
		connection.harness.primaryChecks++
		connection.harness.events = append(connection.harness.events, "primary")
		if err := shiftHandoffPostgresError(&connection.harness.primaryErrors); err != nil {
			return nil, err
		}
		primary := true
		if len(connection.harness.primaryResults) > 0 {
			primary = connection.harness.primaryResults[0]
			connection.harness.primaryResults = connection.harness.primaryResults[1:]
		}
		return &handoffPostgresRows{values: [][]driver.Value{{primary}}}, nil
	case strings.Contains(query, "complete_qualification_promotion_v2_handoff"):
		connection.harness.completes++
		connection.harness.events = append(connection.harness.events, "complete")
		values := make([]driver.Value, len(arguments))
		for index := range arguments {
			values[index] = arguments[index].Value
		}
		connection.harness.arguments = append(connection.harness.arguments, values)
		if err := shiftHandoffPostgresError(&connection.harness.completeErrors); err != nil {
			return nil, err
		}
		return &handoffPostgresRows{values: [][]driver.Value{{append([]byte(nil), connection.harness.completeEncoded...)}}}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

type handoffPostgresTransaction struct{ harness *handoffPostgresHarness }

func (transaction *handoffPostgresTransaction) Commit() error {
	transaction.harness.mu.Lock()
	defer transaction.harness.mu.Unlock()
	transaction.harness.commits++
	transaction.harness.events = append(transaction.harness.events, "commit")
	return shiftHandoffPostgresError(&transaction.harness.commitErrors)
}

func (transaction *handoffPostgresTransaction) Rollback() error {
	transaction.harness.mu.Lock()
	defer transaction.harness.mu.Unlock()
	transaction.harness.rollbacks++
	transaction.harness.events = append(transaction.harness.events, "rollback")
	return shiftHandoffPostgresError(&transaction.harness.rollbackErrors)
}

type handoffPostgresRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (rows *handoffPostgresRows) Columns() []string {
	if len(rows.columns) > 0 {
		return rows.columns
	}
	return []string{"value"}
}

func (*handoffPostgresRows) Close() error { return nil }

func (rows *handoffPostgresRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

var (
	_ driver.Driver         = handoffPostgresDriver{}
	_ driver.Conn           = (*handoffPostgresConnection)(nil)
	_ driver.ConnBeginTx    = (*handoffPostgresConnection)(nil)
	_ driver.ExecerContext  = (*handoffPostgresConnection)(nil)
	_ driver.QueryerContext = (*handoffPostgresConnection)(nil)
)
