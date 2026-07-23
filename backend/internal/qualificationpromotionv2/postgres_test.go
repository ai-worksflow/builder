package qualificationpromotionv2

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestNewPostgresStoreRequiresVerifiedSessionAffinity(t *testing.T) {
	database := openPromotionPostgresTestDatabase(t, newPromotionPostgresHarness(t))

	for name, config := range map[string]PostgresStoreConfig{
		"zero value":       {},
		"unverified":       {SessionAffinityMode: PostgresSessionAffinityUnverified},
		"transaction pool": {SessionAffinityMode: PostgresSessionAffinityTransactionPool},
		"unknown":          {SessionAffinityMode: "magic-proxy"},
		"negative retries": {SessionAffinityMode: PostgresSessionAffinityDirect, MaxTransactionRetries: -1},
		"excessive retries": {
			SessionAffinityMode:   PostgresSessionAffinityDirect,
			MaxTransactionRetries: maximumPostgresTransactionRetries + 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewPostgresStore(database, config); !errors.Is(err, ErrInvalid) {
				t.Fatalf("NewPostgresStore() error = %v, want ErrInvalid", err)
			}
		})
	}
	if _, err := NewPostgresStore(nil, PostgresStoreConfig{SessionAffinityMode: PostgresSessionAffinityDirect}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewPostgresStore(nil) error = %v, want ErrInvalid", err)
	}

	for _, mode := range []PostgresSessionAffinityMode{
		PostgresSessionAffinityDirect,
		PostgresSessionAffinitySessionPool,
	} {
		store, err := NewPostgresStore(database, PostgresStoreConfig{SessionAffinityMode: mode})
		if err != nil {
			t.Fatalf("NewPostgresStore(%q) error = %v", mode, err)
		}
		if store.maxTransactionRetries != defaultPostgresTransactionRetries {
			t.Fatalf("default retries = %d, want %d", store.maxTransactionRetries, defaultPostgresTransactionRetries)
		}
	}
}

func TestPostgresStoreConsumeUsesSerializableSessionLockedAttempt(t *testing.T) {
	harness := newPromotionPostgresHarness(t)
	database := openPromotionPostgresTestDatabase(t, harness)
	store := newPromotionPostgresTestStore(t, database, 1)

	record, err := store.Consume(context.Background(), testCommand())
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if !SameImmutableRecord(record, harness.record) {
		t.Fatal("Consume() returned different immutable bytes")
	}
	snapshot := harness.snapshot()
	if snapshot.acquires != 1 || snapshot.begins != 1 || snapshot.primaryChecks != 1 || snapshot.consumes != 1 ||
		snapshot.commits != 1 || snapshot.unlocks != 1 {
		t.Fatalf("attempt accounting = %#v", snapshot)
	}
	if snapshot.rollbacks != 0 || snapshot.isolation != driver.IsolationLevel(sql.LevelSerializable) {
		t.Fatalf("transaction accounting = %#v", snapshot)
	}
	if got, want := strings.Join(snapshot.events, ","), "acquire,begin,primary,consume,commit,unlock"; got != want {
		t.Fatalf("attempt order = %q, want %q", got, want)
	}
	assertPromotionPostgresExactCommands(t, snapshot.commands, 1)
}

func TestPostgresStoreRequiresPrimaryReadWriteAtExecutionTime(t *testing.T) {
	t.Run("consume", func(t *testing.T) {
		harness := newPromotionPostgresHarness(t)
		harness.primaryResults = []bool{false}
		database := openPromotionPostgresTestDatabase(t, harness)
		store := newPromotionPostgresTestStore(t, database, 1)

		if _, err := store.Consume(context.Background(), testCommand()); !errors.Is(err, ErrNotReady) {
			t.Fatalf("Consume() error = %v, want ErrNotReady", err)
		}
		snapshot := harness.snapshot()
		if snapshot.primaryChecks != 1 || snapshot.consumes != 0 || snapshot.rollbacks != 1 || snapshot.unlocks != 1 {
			t.Fatalf("non-primary consume accounting = %#v", snapshot)
		}
	})

	t.Run("inspect", func(t *testing.T) {
		harness := newPromotionPostgresHarness(t)
		harness.primaryResults = []bool{false}
		database := openPromotionPostgresTestDatabase(t, harness)
		store := newPromotionPostgresTestStore(t, database, 1)

		if _, err := store.InspectOperation(context.Background(), testCommand().OperationID); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("InspectOperation() error = %v, want ErrOutcomeUnknown", err)
		}
		if snapshot := harness.snapshot(); snapshot.primaryChecks != 1 {
			t.Fatalf("non-primary inspect accounting = %#v", snapshot)
		}
	})
}

func TestPostgresStoreRetriesCompleteAttemptAfterDefiniteQueryAbort(t *testing.T) {
	harness := newPromotionPostgresHarness(t)
	harness.consumeErrors = []error{&pgconn.PgError{Code: "40001", Message: "serialization abort"}}
	database := openPromotionPostgresTestDatabase(t, harness)
	store := newPromotionPostgresTestStore(t, database, 1)

	record, err := store.Consume(context.Background(), testCommand())
	if err != nil || !SameImmutableRecord(record, harness.record) {
		t.Fatalf("Consume() = %#v, %v", record, err)
	}
	snapshot := harness.snapshot()
	if snapshot.acquires != 2 || snapshot.begins != 2 || snapshot.consumes != 2 ||
		snapshot.rollbacks != 1 || snapshot.commits != 1 || snapshot.unlocks != 2 {
		t.Fatalf("retried attempt accounting = %#v", snapshot)
	}
	assertPromotionPostgresExactCommands(t, snapshot.commands, 2)
}

func TestPostgresStoreRetriesCompleteAttemptAfterDefiniteCommitAbort(t *testing.T) {
	harness := newPromotionPostgresHarness(t)
	harness.commitErrors = []error{&pgconn.PgError{Code: "40P01", Message: "deadlock abort"}}
	database := openPromotionPostgresTestDatabase(t, harness)
	store := newPromotionPostgresTestStore(t, database, 1)

	record, err := store.Consume(context.Background(), testCommand())
	if err != nil || !SameImmutableRecord(record, harness.record) {
		t.Fatalf("Consume() = %#v, %v", record, err)
	}
	snapshot := harness.snapshot()
	if snapshot.acquires != 2 || snapshot.begins != 2 || snapshot.consumes != 2 ||
		snapshot.commits != 2 || snapshot.unlocks != 2 {
		t.Fatalf("commit-retried attempt accounting = %#v", snapshot)
	}
	assertPromotionPostgresExactCommands(t, snapshot.commands, 2)
}

func TestPostgresStoreReturnsRetryableAfterBoundedDefiniteAborts(t *testing.T) {
	harness := newPromotionPostgresHarness(t)
	harness.consumeErrors = []error{
		&pgconn.PgError{Code: "40001", Message: "first abort"},
		&pgconn.PgError{Code: "40P01", Message: "second abort"},
	}
	database := openPromotionPostgresTestDatabase(t, harness)
	store := newPromotionPostgresTestStore(t, database, 1)

	if _, err := store.Consume(context.Background(), testCommand()); !errors.Is(err, ErrRetryable) ||
		errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Consume() error = %v, want only ErrRetryable", err)
	}
	snapshot := harness.snapshot()
	if snapshot.acquires != 2 || snapshot.consumes != 2 || snapshot.unlocks != 2 {
		t.Fatalf("bounded retry accounting = %#v", snapshot)
	}
	assertPromotionPostgresExactCommands(t, snapshot.commands, 2)
}

func TestPostgresStoreDoesNotRetryWhenRetryableAbortCleanupIsUnknown(t *testing.T) {
	harness := newPromotionPostgresHarness(t)
	harness.consumeErrors = []error{&pgconn.PgError{Code: "40001", Message: "serialization abort"}}
	harness.unlockResults = []bool{false}
	database := openPromotionPostgresTestDatabase(t, harness)
	store := newPromotionPostgresTestStore(t, database, 3)

	if _, err := store.Consume(context.Background(), testCommand()); !errors.Is(err, ErrOutcomeUnknown) ||
		errors.Is(err, ErrRetryable) {
		t.Fatalf("Consume() error = %v, want only ErrOutcomeUnknown", err)
	}
	snapshot := harness.snapshot()
	if snapshot.acquires != 1 || snapshot.consumes != 1 || snapshot.unlocks != 1 || snapshot.closes == 0 {
		t.Fatalf("cleanup-unknown attempt was retried or pooled: %#v", snapshot)
	}
}

func TestPostgresStoreNeverRetriesAmbiguousCommit(t *testing.T) {
	harness := newPromotionPostgresHarness(t)
	harness.commitErrors = []error{errors.New("transport disappeared during commit")}
	database := openPromotionPostgresTestDatabase(t, harness)
	store := newPromotionPostgresTestStore(t, database, 3)

	if _, err := store.Consume(context.Background(), testCommand()); !errors.Is(err, ErrStoreOutcomeUnknown) ||
		errors.Is(err, ErrRetryable) {
		t.Fatalf("Consume() error = %v, want only ErrStoreOutcomeUnknown", err)
	}
	snapshot := harness.snapshot()
	if snapshot.acquires != 1 || snapshot.consumes != 1 || snapshot.commits != 1 ||
		snapshot.unlocks != 0 || snapshot.closes == 0 {
		t.Fatalf("ambiguous commit was retried, unlocked, or pooled: %#v", snapshot)
	}
}

func TestPostgresServiceRecoversAmbiguousCommitOnFreshConnection(t *testing.T) {
	harness := newPromotionPostgresHarness(t)
	harness.commitErrors = []error{errors.New("commit acknowledgement disappeared")}
	database := openPromotionPostgresTestDatabase(t, harness)
	store := newPromotionPostgresTestStore(t, database, 3)
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}

	record, err := service.Consume(context.Background(), testCommand())
	if err != nil || !SameImmutableRecord(record, harness.record) || !record.Idempotent {
		t.Fatalf("Consume() = %#v, %v", record, err)
	}
	snapshot := harness.snapshot()
	if snapshot.acquires != 1 || snapshot.commits != 1 || snapshot.unlocks != 0 ||
		snapshot.opens < 2 || snapshot.closes < 1 || snapshot.inspections != 1 {
		t.Fatalf("ambiguous commit recovery did not discard and inspect on a fresh connection: %#v", snapshot)
	}
}

func TestPostgresStorePoisonsUnknownBeginAndRollbackStates(t *testing.T) {
	t.Run("begin", func(t *testing.T) {
		harness := newPromotionPostgresHarness(t)
		harness.beginErrors = []error{errors.New("BEGIN acknowledgement disappeared")}
		database := openPromotionPostgresTestDatabase(t, harness)
		store := newPromotionPostgresTestStore(t, database, 3)

		if _, err := store.Consume(context.Background(), testCommand()); !errors.Is(err, ErrOutcomeUnknown) ||
			errors.Is(err, ErrRetryable) {
			t.Fatalf("Consume() error = %v, want only ErrOutcomeUnknown", err)
		}
		snapshot := harness.snapshot()
		if snapshot.acquires != 1 || snapshot.begins != 1 || snapshot.consumes != 0 ||
			snapshot.unlocks != 0 || snapshot.closes == 0 {
			t.Fatalf("unknown BEGIN was retried, unlocked, or pooled: %#v", snapshot)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		harness := newPromotionPostgresHarness(t)
		harness.encoded = []byte(`{"schemaVersion":"wrong"}`)
		harness.rollbackErrors = []error{errors.New("ROLLBACK acknowledgement disappeared")}
		database := openPromotionPostgresTestDatabase(t, harness)
		store := newPromotionPostgresTestStore(t, database, 3)

		if _, err := store.Consume(context.Background(), testCommand()); !errors.Is(err, ErrStoreOutcomeUnknown) ||
			errors.Is(err, ErrRetryable) {
			t.Fatalf("Consume() error = %v, want only ErrStoreOutcomeUnknown", err)
		}
		snapshot := harness.snapshot()
		if snapshot.acquires != 1 || snapshot.rollbacks != 1 || snapshot.unlocks != 0 || snapshot.closes == 0 {
			t.Fatalf("unknown ROLLBACK was retried, unlocked, or pooled: %#v", snapshot)
		}
	})
}

func TestPostgresStorePoisonsUnknownSessionLockStates(t *testing.T) {
	for name, mutate := range map[string]func(*promotionPostgresHarness){
		"acquire error": func(harness *promotionPostgresHarness) {
			harness.acquireErrors = []error{errors.New("lock result was lost")}
		},
		"unlock error": func(harness *promotionPostgresHarness) {
			harness.unlockErrors = []error{errors.New("unlock result was lost")}
		},
		"unlock false": func(harness *promotionPostgresHarness) {
			harness.unlockResults = []bool{false}
		},
	} {
		t.Run(name, func(t *testing.T) {
			harness := newPromotionPostgresHarness(t)
			mutate(harness)
			database := openPromotionPostgresTestDatabase(t, harness)
			store := newPromotionPostgresTestStore(t, database, 3)

			_, err := store.Consume(context.Background(), testCommand())
			if name == "acquire error" {
				if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrRetryable) {
					t.Fatalf("Consume() error = %v", err)
				}
			} else if !errors.Is(err, ErrStoreOutcomeUnknown) || errors.Is(err, ErrRetryable) {
				t.Fatalf("Consume() error = %v", err)
			}
			snapshot := harness.snapshot()
			if snapshot.closes == 0 {
				t.Fatalf("unknown lock state did not poison/close physical connection: %#v", snapshot)
			}
			if snapshot.acquires != 1 || snapshot.unlocks > 1 {
				t.Fatalf("unknown lock state was retried: %#v", snapshot)
			}
		})
	}
}

func TestPostgresStoreRejectsCorruptConsumeBundleBeforeCommit(t *testing.T) {
	harness := newPromotionPostgresHarness(t)
	harness.encoded = []byte(`{"schemaVersion":"wrong"}`)
	database := openPromotionPostgresTestDatabase(t, harness)
	store := newPromotionPostgresTestStore(t, database, 1)

	if _, err := store.Consume(context.Background(), testCommand()); !errors.Is(err, ErrConflict) {
		t.Fatalf("Consume() error = %v, want ErrConflict", err)
	}
	snapshot := harness.snapshot()
	if snapshot.commits != 0 || snapshot.rollbacks != 1 || snapshot.unlocks != 1 {
		t.Fatalf("corrupt bundle transaction accounting = %#v", snapshot)
	}
}

func TestPostgresStoreInspectOperationStrictlyDecodesBundle(t *testing.T) {
	harness := newPromotionPostgresHarness(t)
	database := openPromotionPostgresTestDatabase(t, harness)
	store := newPromotionPostgresTestStore(t, database, 1)

	record, err := store.InspectOperation(context.Background(), testCommand().OperationID)
	if err != nil || !SameImmutableRecord(record, harness.record) {
		t.Fatalf("InspectOperation() = %#v, %v", record, err)
	}
	harness.mu.Lock()
	harness.inspectEncoded = []byte(`{"schemaVersion":"wrong"}`)
	harness.mu.Unlock()
	if _, err := store.InspectOperation(context.Background(), testCommand().OperationID); !errors.Is(err, ErrConflict) {
		t.Fatalf("corrupt InspectOperation() error = %v", err)
	}
}

func TestDefinitePostgresRetryableClassification(t *testing.T) {
	serialization := &pgconn.PgError{Code: "40001", Message: "serialization abort"}
	deadlock := &pgconn.PgError{Code: "40P01", Message: "deadlock abort"}
	for name, err := range map[string]error{
		"serialization": serialization,
		"deadlock":      fmt.Errorf("commit: %w", deadlock),
	} {
		if !isDefinitePostgresRetryable(err) {
			t.Errorf("%s was not classified retryable", name)
		}
	}
	for name, err := range map[string]error{
		"constraint":                          &pgconn.PgError{Code: "23505", Message: "unique"},
		"transport":                           errors.New("EOF"),
		"bad conn joined with serialization":  errors.Join(serialization, driver.ErrBadConn),
		"cancellation joined with deadlock":   errors.Join(deadlock, context.Canceled),
		"transport joined with serialization": errors.Join(serialization, errors.New("transport disappeared")),
	} {
		if isDefinitePostgresRetryable(err) {
			t.Errorf("%s was incorrectly classified retryable", name)
		}
	}
}

func TestServicePreservesRetryableClassification(t *testing.T) {
	service, err := NewService(errorAtomicStore{err: ErrRetryable})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Consume(context.Background(), testCommand()); !errors.Is(err, ErrRetryable) ||
		errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Consume() error = %v, want only ErrRetryable", err)
	}

	ambiguous, err := NewService(errorAtomicStore{err: errors.Join(ErrRetryable, ErrOutcomeUnknown)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ambiguous.Consume(context.Background(), testCommand()); !errors.Is(err, ErrOutcomeUnknown) ||
		errors.Is(err, ErrRetryable) {
		t.Fatalf("joined retryable/unknown Consume() error = %v, want only ErrOutcomeUnknown", err)
	}
}

func newPromotionPostgresTestStore(t *testing.T, database *sql.DB, retries int) *PostgresStore {
	t.Helper()
	store, err := NewPostgresStore(database, PostgresStoreConfig{
		SessionAffinityMode:   PostgresSessionAffinityDirect,
		MaxTransactionRetries: retries,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func assertPromotionPostgresExactCommands(t *testing.T, commands [][]driver.Value, count int) {
	t.Helper()
	if len(commands) != count {
		t.Fatalf("consume commands = %d, want %d", len(commands), count)
	}
	want := testCommand()
	wantValues := []string{
		want.OperationID.String(),
		want.WorkflowInputAuthorityID.String(),
		want.PlanAuthorityID.String(),
		want.HandoffID.String(),
		want.OutputRevisionID.String(),
	}
	for attempt, command := range commands {
		if len(command) != len(wantValues) {
			t.Fatalf("attempt %d command length = %d", attempt, len(command))
		}
		for index, value := range command {
			if fmt.Sprint(value) != wantValues[index] {
				t.Fatalf("attempt %d command[%d] = %v, want %s", attempt, index, value, wantValues[index])
			}
		}
	}
}

type promotionPostgresHarness struct {
	mu sync.Mutex

	record         Record
	encoded        []byte
	inspectEncoded []byte

	acquireErrors  []error
	beginErrors    []error
	consumeErrors  []error
	commitErrors   []error
	rollbackErrors []error
	unlockErrors   []error
	unlockResults  []bool
	primaryErrors  []error
	primaryResults []bool

	commands [][]driver.Value
	events   []string

	acquires      int
	begins        int
	consumes      int
	commits       int
	rollbacks     int
	unlocks       int
	primaryChecks int
	inspections   int
	opens         int
	closes        int
	isolation     driver.IsolationLevel
}

type promotionPostgresSnapshot struct {
	commands [][]driver.Value
	events   []string

	acquires      int
	begins        int
	consumes      int
	commits       int
	rollbacks     int
	unlocks       int
	primaryChecks int
	inspections   int
	opens         int
	closes        int
	isolation     driver.IsolationLevel
}

func newPromotionPostgresHarness(t *testing.T) *promotionPostgresHarness {
	t.Helper()
	record := compileTestRecord(t)
	consumeBundle, err := ConsumeStoreBundleFromRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(consumeBundle)
	if err != nil {
		t.Fatal(err)
	}
	storeBundle, err := StoreBundleFromRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	inspectEncoded, err := json.Marshal(storeBundle)
	if err != nil {
		t.Fatal(err)
	}
	return &promotionPostgresHarness{record: record, encoded: encoded, inspectEncoded: inspectEncoded}
}

func (harness *promotionPostgresHarness) snapshot() promotionPostgresSnapshot {
	harness.mu.Lock()
	defer harness.mu.Unlock()
	commands := make([][]driver.Value, len(harness.commands))
	for index := range harness.commands {
		commands[index] = append([]driver.Value(nil), harness.commands[index]...)
	}
	return promotionPostgresSnapshot{
		commands: commands,
		events:   append([]string(nil), harness.events...),
		acquires: harness.acquires, begins: harness.begins, consumes: harness.consumes,
		commits: harness.commits, rollbacks: harness.rollbacks, unlocks: harness.unlocks,
		primaryChecks: harness.primaryChecks,
		inspections:   harness.inspections,
		opens:         harness.opens, closes: harness.closes, isolation: harness.isolation,
	}
}

func shiftPromotionPostgresError(values *[]error) error {
	if len(*values) == 0 {
		return nil
	}
	value := (*values)[0]
	*values = (*values)[1:]
	return value
}

var (
	promotionPostgresDriverOnce sync.Once
	promotionPostgresHarnesses  sync.Map
	promotionPostgresSequence   atomic.Uint64
)

func openPromotionPostgresTestDatabase(t *testing.T, harness *promotionPostgresHarness) *sql.DB {
	t.Helper()
	promotionPostgresDriverOnce.Do(func() {
		sql.Register("qualification-promotion-v2-test", promotionPostgresDriver{})
	})
	name := fmt.Sprintf("harness-%d", promotionPostgresSequence.Add(1))
	promotionPostgresHarnesses.Store(name, harness)
	database, err := sql.Open("qualification-promotion-v2-test", name)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxIdleConns(1)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		_ = database.Close()
		promotionPostgresHarnesses.Delete(name)
	})
	return database
}

type promotionPostgresDriver struct{}

func (promotionPostgresDriver) Open(name string) (driver.Conn, error) {
	value, exists := promotionPostgresHarnesses.Load(name)
	if !exists {
		return nil, errors.New("unknown Qualification Promotion v2 test harness")
	}
	harness := value.(*promotionPostgresHarness)
	harness.mu.Lock()
	harness.opens++
	harness.mu.Unlock()
	return &promotionPostgresConnection{harness: harness}, nil
}

type promotionPostgresConnection struct {
	harness *promotionPostgresHarness
}

func (*promotionPostgresConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare is not supported")
}

func (connection *promotionPostgresConnection) Close() error {
	connection.harness.mu.Lock()
	connection.harness.closes++
	connection.harness.mu.Unlock()
	return nil
}

func (connection *promotionPostgresConnection) Begin() (driver.Tx, error) {
	return connection.BeginTx(context.Background(), driver.TxOptions{})
}

func (connection *promotionPostgresConnection) BeginTx(
	_ context.Context,
	options driver.TxOptions,
) (driver.Tx, error) {
	connection.harness.mu.Lock()
	defer connection.harness.mu.Unlock()
	connection.harness.begins++
	connection.harness.events = append(connection.harness.events, "begin")
	connection.harness.isolation = options.Isolation
	if err := shiftPromotionPostgresError(&connection.harness.beginErrors); err != nil {
		return nil, err
	}
	return &promotionPostgresTransaction{harness: connection.harness}, nil
}

func (connection *promotionPostgresConnection) ExecContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	if !strings.Contains(query, "pg_advisory_lock") || strings.Contains(query, "pg_advisory_unlock") {
		return nil, fmt.Errorf("unexpected exec: %s", query)
	}
	connection.harness.mu.Lock()
	defer connection.harness.mu.Unlock()
	connection.harness.acquires++
	connection.harness.events = append(connection.harness.events, "acquire")
	if err := shiftPromotionPostgresError(&connection.harness.acquireErrors); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (connection *promotionPostgresConnection) QueryContext(
	_ context.Context,
	query string,
	arguments []driver.NamedValue,
) (driver.Rows, error) {
	connection.harness.mu.Lock()
	defer connection.harness.mu.Unlock()
	switch {
	case strings.Contains(query, "pg_advisory_unlock"):
		connection.harness.unlocks++
		connection.harness.events = append(connection.harness.events, "unlock")
		if err := shiftPromotionPostgresError(&connection.harness.unlockErrors); err != nil {
			return nil, err
		}
		unlocked := true
		if len(connection.harness.unlockResults) > 0 {
			unlocked = connection.harness.unlockResults[0]
			connection.harness.unlockResults = connection.harness.unlockResults[1:]
		}
		return &promotionPostgresRows{values: [][]driver.Value{{unlocked}}}, nil
	case strings.Contains(query, "inspect_qualification_promotion_v2_operation"):
		connection.harness.primaryChecks++
		connection.harness.inspections++
		connection.harness.events = append(connection.harness.events, "inspect")
		if err := shiftPromotionPostgresError(&connection.harness.primaryErrors); err != nil {
			return nil, err
		}
		primaryReadWrite := true
		if len(connection.harness.primaryResults) > 0 {
			primaryReadWrite = connection.harness.primaryResults[0]
			connection.harness.primaryResults = connection.harness.primaryResults[1:]
		}
		var encoded driver.Value
		if primaryReadWrite && len(connection.harness.inspectEncoded) > 0 {
			encoded = append([]byte(nil), connection.harness.inspectEncoded...)
		}
		return &promotionPostgresRows{
			columns: []string{"primary_read_write", "value"},
			values:  [][]driver.Value{{primaryReadWrite, encoded}},
		}, nil
	case strings.Contains(query, "pg_is_in_recovery"):
		connection.harness.primaryChecks++
		connection.harness.events = append(connection.harness.events, "primary")
		if err := shiftPromotionPostgresError(&connection.harness.primaryErrors); err != nil {
			return nil, err
		}
		primaryReadWrite := true
		if len(connection.harness.primaryResults) > 0 {
			primaryReadWrite = connection.harness.primaryResults[0]
			connection.harness.primaryResults = connection.harness.primaryResults[1:]
		}
		return &promotionPostgresRows{values: [][]driver.Value{{primaryReadWrite}}}, nil
	case strings.Contains(query, "consume_qualification_promotion_v2"):
		connection.harness.consumes++
		connection.harness.events = append(connection.harness.events, "consume")
		command := make([]driver.Value, len(arguments))
		for index := range arguments {
			command[index] = arguments[index].Value
		}
		connection.harness.commands = append(connection.harness.commands, command)
		if err := shiftPromotionPostgresError(&connection.harness.consumeErrors); err != nil {
			return nil, err
		}
		return &promotionPostgresRows{values: [][]driver.Value{{append([]byte(nil), connection.harness.encoded...)}}}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

type promotionPostgresTransaction struct {
	harness *promotionPostgresHarness
}

func (transaction *promotionPostgresTransaction) Commit() error {
	transaction.harness.mu.Lock()
	defer transaction.harness.mu.Unlock()
	transaction.harness.commits++
	transaction.harness.events = append(transaction.harness.events, "commit")
	return shiftPromotionPostgresError(&transaction.harness.commitErrors)
}

func (transaction *promotionPostgresTransaction) Rollback() error {
	transaction.harness.mu.Lock()
	defer transaction.harness.mu.Unlock()
	transaction.harness.rollbacks++
	transaction.harness.events = append(transaction.harness.events, "rollback")
	return shiftPromotionPostgresError(&transaction.harness.rollbackErrors)
}

type promotionPostgresRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (rows *promotionPostgresRows) Columns() []string {
	if len(rows.columns) > 0 {
		return rows.columns
	}
	return []string{"value"}
}
func (*promotionPostgresRows) Close() error { return nil }

func (rows *promotionPostgresRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

var (
	_ driver.Driver         = promotionPostgresDriver{}
	_ driver.Conn           = (*promotionPostgresConnection)(nil)
	_ driver.ConnBeginTx    = (*promotionPostgresConnection)(nil)
	_ driver.ExecerContext  = (*promotionPostgresConnection)(nil)
	_ driver.QueryerContext = (*promotionPostgresConnection)(nil)
)
