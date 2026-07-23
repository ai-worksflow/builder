package qualificationpolicyauthority

import (
	"bytes"
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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresStoreIssueFreshAndExactReplay(t *testing.T) {
	candidate := postgresQualificationPolicyTestRecord(t)

	t.Run("fresh", func(t *testing.T) {
		harness := &postgresQualificationPolicyTestHarness{
			row:   postgresQualificationPolicyTestRow(candidate),
			fresh: true,
			clock: fixedDatabaseTime,
		}
		database := openPostgresQualificationPolicyTestDatabase(t, harness)
		store, err := NewPostgresStore(database)
		if err != nil {
			t.Fatal(err)
		}

		stored, err := store.Issue(context.Background(), candidate)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Idempotent || !sameImmutableRecord(stored, candidate) {
			t.Fatalf("fresh issue = %#v", stored)
		}
		if harness.commits.Load() != 1 || harness.rollbacks.Load() != 0 {
			t.Fatalf("fresh transaction accounting = commits %d rollbacks %d", harness.commits.Load(), harness.rollbacks.Load())
		}
		if got := sql.IsolationLevel(harness.isolation.Load()); got != sql.LevelSerializable {
			t.Fatalf("issue isolation = %v, want serializable", got)
		}
		queries, arguments := harness.snapshot()
		if len(queries) != 1 || !strings.Contains(queries[0], "issue_qualification_policy_authority_v1") ||
			!strings.Contains(queries[0], "creation_transaction_id = txid_current()") {
			t.Fatalf("issue queries = %#v", queries)
		}
		if len(arguments[0]) != 24 {
			t.Fatalf("issue argument count = %d, want exact SQL API", len(arguments[0]))
		}
		for _, index := range []int{14, 17, 20, 23} {
			if _, ok := arguments[0][index].(string); !ok {
				t.Fatalf("JSONB argument %d = %T, want canonical JSON string", index+1, arguments[0][index])
			}
		}
		for _, check := range []struct {
			index int
			want  []byte
		}{
			{13, candidate.RevisionPolicyBytes},
			{16, candidate.PlanInputProfileBytes},
			{19, candidate.PromotionPolicyBytes},
			{22, candidate.DocumentBytes},
		} {
			got, ok := arguments[0][check.index].([]byte)
			if !ok || !bytes.Equal(got, check.want) {
				t.Fatalf("retained byte argument %d = %T", check.index+1, arguments[0][check.index])
			}
		}
	})

	t.Run("exact replay", func(t *testing.T) {
		harness := &postgresQualificationPolicyTestHarness{
			row:   postgresQualificationPolicyTestRow(candidate),
			fresh: false,
			clock: fixedDatabaseTime,
		}
		database := openPostgresQualificationPolicyTestDatabase(t, harness)
		store, err := NewPostgresStore(database)
		if err != nil {
			t.Fatal(err)
		}

		stored, err := store.Issue(context.Background(), candidate)
		if err != nil {
			t.Fatal(err)
		}
		if !stored.Idempotent || !sameImmutableRecord(stored, candidate) {
			t.Fatalf("exact replay = %#v", stored)
		}
		if harness.commits.Load() != 0 || harness.rollbacks.Load() != 1 {
			t.Fatalf("replay transaction accounting = commits %d rollbacks %d", harness.commits.Load(), harness.rollbacks.Load())
		}
	})
}

func TestPostgresStoreIssueCommitFailureIsOutcomeUnknown(t *testing.T) {
	candidate := postgresQualificationPolicyTestRecord(t)
	harness := &postgresQualificationPolicyTestHarness{
		row:   postgresQualificationPolicyTestRow(candidate),
		fresh: true,
		clock: fixedDatabaseTime,
	}
	database := openPostgresQualificationPolicyTestDatabase(t, harness)
	store, err := NewPostgresStore(database)
	if err != nil {
		t.Fatal(err)
	}
	commitFailure := errors.New("connection disappeared while committing")
	store.commit = func(*sql.Tx) error { return commitFailure }

	_, err = store.Issue(context.Background(), candidate)
	if !errors.Is(err, ErrStoreOutcomeUnknown) || !errors.Is(err, commitFailure) {
		t.Fatalf("commit failure = %v", err)
	}
	if harness.commits.Load() != 0 || harness.rollbacks.Load() != 1 {
		t.Fatalf("failed commit hook accounting = commits %d rollbacks %d", harness.commits.Load(), harness.rollbacks.Load())
	}
}

func TestPostgresStoreIssueReconcilesSerializableExactWinner(t *testing.T) {
	candidate := postgresQualificationPolicyTestRecord(t)
	harness := &postgresQualificationPolicyTestHarness{
		row:        postgresQualificationPolicyTestRow(candidate),
		fresh:      true,
		clock:      fixedDatabaseTime,
		issueError: &pgconn.PgError{Code: "40001", Message: "concurrent exact winner"},
	}
	database := openPostgresQualificationPolicyTestDatabase(t, harness)
	store, err := NewPostgresStore(database)
	if err != nil {
		t.Fatal(err)
	}

	replayed, err := store.Issue(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Idempotent || !sameImmutableRecord(replayed, candidate) {
		t.Fatalf("reconciled exact winner = %#v", replayed)
	}
	if harness.commits.Load() != 0 || harness.rollbacks.Load() != 1 {
		t.Fatalf("reconciled transaction accounting = commits %d rollbacks %d", harness.commits.Load(), harness.rollbacks.Load())
	}
	queries, _ := harness.snapshot()
	if len(queries) != 2 || !strings.Contains(queries[0], "issue_qualification_policy_authority_v1") ||
		!strings.Contains(queries[1], "inspect_qualification_policy_operation_v1") {
		t.Fatalf("reconciliation queries = %#v", queries)
	}
}

func TestPostgresStoreReadsAndCallerOwnedCurrentAssertion(t *testing.T) {
	candidate := postgresQualificationPolicyTestRecord(t)
	harness := &postgresQualificationPolicyTestHarness{
		row:   postgresQualificationPolicyTestRow(candidate),
		fresh: true,
		clock: fixedDatabaseTime,
	}
	database := openPostgresQualificationPolicyTestDatabase(t, harness)
	store, err := NewPostgresStore(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	for name, resolve := range map[string]func() (Record, error){
		"inspect": func() (Record, error) {
			return store.InspectOperation(ctx, candidate.Command.OperationID)
		},
		"authority": func() (Record, error) {
			return store.ResolveAuthority(ctx, candidate.Command.AuthorityID)
		},
		"current": func() (Record, error) {
			return store.ResolveCurrent(ctx, uuid.MustParse(candidate.Document.ProjectID), candidate.Document.ExecutionProfile)
		},
		"diagnostic assertion": func() (Record, error) {
			return store.AssertCurrent(ctx, candidate.Command.AuthorityID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			record, err := resolve()
			if err != nil || !sameImmutableRecord(record, candidate) {
				t.Fatalf("read = %#v, %v", record, err)
			}
		})
	}

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.AssertCurrentTx(ctx, transaction, candidate.Command.AuthorityID)
	if err != nil || !sameImmutableRecord(record, candidate) {
		t.Fatalf("transactional assertion = %#v, %v", record, err)
	}
	if harness.commits.Load() != 0 || harness.rollbacks.Load() != 0 {
		t.Fatal("AssertCurrentTx closed the caller-owned transaction")
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if harness.rollbacks.Load() != 1 {
		t.Fatalf("caller rollback count = %d", harness.rollbacks.Load())
	}
	if _, err := store.AssertCurrentTx(ctx, nil, candidate.Command.AuthorityID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil transaction error = %v", err)
	}
}

func TestPostgresStoreRejectsDurableProjectionAndScalarDrift(t *testing.T) {
	candidate := postgresQualificationPolicyTestRecord(t)
	tests := map[string]func([]driver.Value){
		"JSONB projection": func(row []driver.Value) {
			row[16] = []byte(`{"unexpected":true}`)
		},
		"scalar projection": func(row []driver.Value) {
			row[10] = AuthorityStatusSuspended
		},
		"retained bytes": func(row []driver.Value) {
			row[15] = append(append([]byte(nil), candidate.RevisionPolicyBytes...), ' ')
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			row := postgresQualificationPolicyTestRow(candidate)
			mutate(row)
			harness := &postgresQualificationPolicyTestHarness{row: row, fresh: true, clock: fixedDatabaseTime}
			database := openPostgresQualificationPolicyTestDatabase(t, harness)
			store, err := NewPostgresStore(database)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.ResolveAuthority(context.Background(), candidate.Command.AuthorityID)
			if !errors.Is(err, ErrCorrupt) || !errors.Is(err, ErrConflict) {
				t.Fatalf("durable drift error = %v", err)
			}
		})
	}
}

func TestPostgresQualificationPolicyErrorClassification(t *testing.T) {
	tests := []struct {
		name      string
		code      string
		mode      postgresQualificationPolicyErrorMode
		want      error
		also      error
		forbidden error
	}{
		{"invalid issuer input", "WPA01", postgresQualificationPolicyIssueError, ErrInvalid, nil, nil},
		{"read absence", "WPA02", postgresQualificationPolicyReadError, ErrNotFound, nil, ErrStale},
		{"assertion is stale", "WPA02", postgresQualificationPolicyAssertError, ErrStale, nil, ErrNotFound},
		{"issue immutable conflict", "WPA03", postgresQualificationPolicyIssueError, ErrConflict, nil, ErrCorrupt},
		{"read corruption", "WPA03", postgresQualificationPolicyReadError, ErrConflict, ErrCorrupt, nil},
		{"serialization", "40001", postgresQualificationPolicyIssueError, ErrConflict, nil, nil},
		{"unique conflict", "23505", postgresQualificationPolicyIssueError, ErrConflict, nil, nil},
		{"issue data exception", "22P02", postgresQualificationPolicyIssueError, ErrInvalid, nil, nil},
		{"read data exception", "22P02", postgresQualificationPolicyReadError, ErrConflict, ErrCorrupt, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classifyPostgresQualificationPolicyError(
				"test",
				&pgconn.PgError{Code: test.code, Message: "test failure"},
				test.mode,
			)
			if !errors.Is(err, test.want) || test.also != nil && !errors.Is(err, test.also) {
				t.Fatalf("classified error = %v", err)
			}
			if test.forbidden != nil && errors.Is(err, test.forbidden) {
				t.Fatalf("classified error unexpectedly includes %v: %v", test.forbidden, err)
			}
		})
	}
	if err := classifyPostgresQualificationPolicyError(
		"read", sql.ErrNoRows, postgresQualificationPolicyReadError,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty read = %v", err)
	}
	if err := classifyPostgresQualificationPolicyError(
		"issue", sql.ErrNoRows, postgresQualificationPolicyIssueError,
	); !errors.Is(err, ErrCorrupt) || !errors.Is(err, ErrConflict) {
		t.Fatalf("empty issuer result = %v", err)
	}
}

func TestPostgresClockUsesDatabaseUTCMilliseconds(t *testing.T) {
	databaseValue := time.Date(
		2026, 7, 19, 23, 30, 0, 456_000_000,
		time.FixedZone("database-session-zone", -5*60*60),
	)
	harness := &postgresQualificationPolicyTestHarness{clock: databaseValue}
	database := openPostgresQualificationPolicyTestDatabase(t, harness)
	clock, err := NewPostgresClock(database)
	if err != nil {
		t.Fatal(err)
	}
	value, err := clock.Now(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if value.Location() != time.UTC || value.Nanosecond()%int(time.Millisecond) != 0 ||
		!value.Equal(databaseValue) {
		t.Fatalf("database clock = %s (%s)", value, value.Location())
	}
	queries, _ := harness.snapshot()
	if len(queries) != 1 || !strings.Contains(queries[0], "date_trunc('milliseconds', clock_timestamp())") {
		t.Fatalf("clock queries = %#v", queries)
	}
}

func postgresQualificationPolicyTestRecord(t *testing.T) Record {
	t.Helper()
	record, err := compileRecord(validIssueCommand(), validResolvedPolicy(), 1, nil, fixedDatabaseTime)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func postgresQualificationPolicyTestRow(record Record) []driver.Value {
	var expectedPrevious driver.Value
	if record.Command.ExpectedPreviousAuthorityHash != "" {
		expectedPrevious = record.Command.ExpectedPreviousAuthorityHash
	}
	var previous driver.Value
	if record.Document.PreviousAuthorityHash != nil {
		previous = *record.Document.PreviousAuthorityHash
	}
	return []driver.Value{
		record.Command.AuthorityID.String(),
		record.Command.OperationID.String(),
		int64(101),
		record.Command.PolicySourceID,
		expectedPrevious,
		record.Document.ProjectID,
		record.Document.ExecutionProfile.Version,
		record.Document.ExecutionProfile.Hash,
		record.Document.Generation,
		previous,
		record.Document.Status,
		record.IssuedAt,
		record.Document.ExternalGatePolicy,
		record.Document.SupersessionPolicy,
		record.RevisionPolicyHash,
		append([]byte(nil), record.RevisionPolicyBytes...),
		postgresQualificationPolicyIndentedJSON(record.RevisionPolicyBytes),
		record.PlanInputProfileHash,
		append([]byte(nil), record.PlanInputProfileBytes...),
		postgresQualificationPolicyIndentedJSON(record.PlanInputProfileBytes),
		record.PromotionPolicyHash,
		append([]byte(nil), record.PromotionPolicyBytes...),
		postgresQualificationPolicyIndentedJSON(record.PromotionPolicyBytes),
		record.AuthorityHash,
		append([]byte(nil), record.DocumentBytes...),
		postgresQualificationPolicyIndentedJSON(record.DocumentBytes),
	}
}

func postgresQualificationPolicyIndentedJSON(encoded []byte) []byte {
	var destination bytes.Buffer
	if err := json.Indent(&destination, encoded, "", "  "); err != nil {
		panic(err)
	}
	return destination.Bytes()
}

type postgresQualificationPolicyTestHarness struct {
	mu         sync.Mutex
	row        []driver.Value
	fresh      bool
	clock      time.Time
	queryError error
	issueError error
	queries    []string
	arguments  [][]any
	isolation  atomic.Int64
	commits    atomic.Int64
	rollbacks  atomic.Int64
}

func (harness *postgresQualificationPolicyTestHarness) snapshot() ([]string, [][]any) {
	harness.mu.Lock()
	defer harness.mu.Unlock()
	queries := append([]string(nil), harness.queries...)
	arguments := make([][]any, len(harness.arguments))
	for index := range harness.arguments {
		arguments[index] = append([]any(nil), harness.arguments[index]...)
	}
	return queries, arguments
}

var (
	postgresQualificationPolicyTestDriverOnce sync.Once
	postgresQualificationPolicyTestHarnesses  sync.Map
	postgresQualificationPolicyTestSequence   atomic.Uint64
)

func openPostgresQualificationPolicyTestDatabase(
	t *testing.T,
	harness *postgresQualificationPolicyTestHarness,
) *sql.DB {
	t.Helper()
	postgresQualificationPolicyTestDriverOnce.Do(func() {
		sql.Register("qualification-policy-authority-test", postgresQualificationPolicyTestDriver{})
	})
	name := fmt.Sprintf("harness-%d", postgresQualificationPolicyTestSequence.Add(1))
	postgresQualificationPolicyTestHarnesses.Store(name, harness)
	database, err := sql.Open("qualification-policy-authority-test", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Close()
		postgresQualificationPolicyTestHarnesses.Delete(name)
	})
	return database
}

type postgresQualificationPolicyTestDriver struct{}

func (postgresQualificationPolicyTestDriver) Open(name string) (driver.Conn, error) {
	value, exists := postgresQualificationPolicyTestHarnesses.Load(name)
	if !exists {
		return nil, errors.New("unknown Qualification Policy Authority test harness")
	}
	return &postgresQualificationPolicyTestConnection{
		harness: value.(*postgresQualificationPolicyTestHarness),
	}, nil
}

type postgresQualificationPolicyTestConnection struct {
	harness *postgresQualificationPolicyTestHarness
}

func (*postgresQualificationPolicyTestConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare is not supported")
}

func (*postgresQualificationPolicyTestConnection) Close() error { return nil }

func (connection *postgresQualificationPolicyTestConnection) Begin() (driver.Tx, error) {
	return &postgresQualificationPolicyTestTransaction{harness: connection.harness}, nil
}

func (connection *postgresQualificationPolicyTestConnection) BeginTx(
	_ context.Context,
	options driver.TxOptions,
) (driver.Tx, error) {
	connection.harness.isolation.Store(int64(options.Isolation))
	return connection.Begin()
}

func (connection *postgresQualificationPolicyTestConnection) QueryContext(
	_ context.Context,
	query string,
	arguments []driver.NamedValue,
) (driver.Rows, error) {
	values := make([]any, len(arguments))
	for index := range arguments {
		value := arguments[index].Value
		if bytesValue, ok := value.([]byte); ok {
			value = append([]byte(nil), bytesValue...)
		}
		values[index] = value
	}
	connection.harness.mu.Lock()
	connection.harness.queries = append(connection.harness.queries, query)
	connection.harness.arguments = append(connection.harness.arguments, values)
	connection.harness.mu.Unlock()
	if connection.harness.queryError != nil {
		return nil, connection.harness.queryError
	}
	if connection.harness.issueError != nil && strings.Contains(query, "issue_qualification_policy_authority_v1") {
		return nil, connection.harness.issueError
	}
	if strings.Contains(query, "clock_timestamp()") {
		return &postgresQualificationPolicyTestRows{
			columns: []string{"database_time"},
			values:  [][]driver.Value{{connection.harness.clock}},
		}, nil
	}
	if !strings.Contains(query, "qualification_policy_authority_v1") &&
		!strings.Contains(query, "qualification_policy_operation_v1") {
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
	row := clonePostgresQualificationPolicyDriverValues(connection.harness.row)
	if strings.Contains(query, "issue_qualification_policy_authority_v1") {
		row = append(row, connection.harness.fresh)
	}
	columns := make([]string, len(row))
	for index := range columns {
		columns[index] = fmt.Sprintf("column_%d", index)
	}
	return &postgresQualificationPolicyTestRows{columns: columns, values: [][]driver.Value{row}}, nil
}

type postgresQualificationPolicyTestTransaction struct {
	harness *postgresQualificationPolicyTestHarness
}

func (transaction *postgresQualificationPolicyTestTransaction) Commit() error {
	transaction.harness.commits.Add(1)
	return nil
}

func (transaction *postgresQualificationPolicyTestTransaction) Rollback() error {
	transaction.harness.rollbacks.Add(1)
	return nil
}

type postgresQualificationPolicyTestRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (rows *postgresQualificationPolicyTestRows) Columns() []string { return rows.columns }
func (*postgresQualificationPolicyTestRows) Close() error           { return nil }

func (rows *postgresQualificationPolicyTestRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

func clonePostgresQualificationPolicyDriverValues(values []driver.Value) []driver.Value {
	clone := append([]driver.Value(nil), values...)
	for index, value := range clone {
		if bytesValue, ok := value.([]byte); ok {
			clone[index] = append([]byte(nil), bytesValue...)
		}
	}
	return clone
}
