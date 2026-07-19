package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
)

func TestMigrationFilesAreOrderedAndPaired(t *testing.T) {
	t.Parallel()

	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("expected at least one migration")
	}
	previous := ""
	for _, name := range names {
		if previous != "" && name <= previous {
			t.Fatalf("migrations are not strictly ordered: %q then %q", previous, name)
		}
		down := strings.TrimSuffix(name, ".up.sql") + ".down.sql"
		if _, err := files.ReadFile(down); err != nil {
			t.Fatalf("migration %s has no matching down file: %v", name, err)
		}
		previous = name
	}
}

func TestVerifyAppliedVersionsRequiresExactOrderedSetAndChecksums(t *testing.T) {
	expected := []Applied{
		{Version: "000001_first", Checksum: "checksum-one", DownChecksum: "down-checksum-one", DownChecksumValid: true},
		{Version: "000002_second", Checksum: "checksum-two", DownChecksum: "down-checksum-two", DownChecksumValid: true},
	}
	if err := verifyAppliedVersions(expected, append([]Applied(nil), expected...)); err != nil {
		t.Fatalf("exact migration set rejected: %v", err)
	}

	tests := []struct {
		name    string
		applied []Applied
		want    string
	}{
		{name: "missing", applied: expected[:1], want: "applied=1 expected=2"},
		{name: "unexpected version", applied: []Applied{
			{Version: "000001_first", Checksum: "checksum-one", DownChecksum: "down-checksum-one", DownChecksumValid: true},
			{Version: "000003_unknown", Checksum: "checksum-two", DownChecksum: "down-checksum-two", DownChecksumValid: true},
		}, want: "expected \"000002_second\""},
		{name: "checksum drift", applied: []Applied{
			{Version: "000001_first", Checksum: "checksum-one", DownChecksum: "down-checksum-one", DownChecksumValid: true},
			{Version: "000002_second", Checksum: "changed", DownChecksum: "down-checksum-two", DownChecksumValid: true},
		}, want: "checksum differs"},
		{name: "down checksum drift", applied: []Applied{
			{Version: "000001_first", Checksum: "checksum-one", DownChecksum: "down-checksum-one", DownChecksumValid: true},
			{Version: "000002_second", Checksum: "checksum-two", DownChecksum: "changed", DownChecksumValid: true},
		}, want: "down checksum differs"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := verifyAppliedVersions(expected, test.applied)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("verifyAppliedVersions() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestVerifyAppliedPrefixAllowsOnlyExactOrderedLegacyPrefix(t *testing.T) {
	expected := []Applied{
		{Version: "000001_first", Checksum: "up-one", DownChecksum: "down-one", DownChecksumValid: true},
		{Version: "000002_second", Checksum: "up-two", DownChecksum: "down-two", DownChecksumValid: true},
	}
	if err := verifyAppliedPrefix(expected, []Applied{{
		Version: "000001_first", Checksum: "up-one",
	}}); err != nil {
		t.Fatalf("legacy NULL exact prefix rejected: %v", err)
	}
	if err := verifyAppliedPrefix(expected, []Applied{{
		Version: "000001_first", Checksum: "up-one", DownChecksum: "down-one", DownChecksumValid: true,
	}}); err != nil {
		t.Fatalf("established exact prefix rejected: %v", err)
	}

	tests := []struct {
		name    string
		applied []Applied
		want    string
	}{
		{
			name: "gap or unknown",
			applied: []Applied{{
				Version: "000002_second", Checksum: "up-two", DownChecksum: "down-two", DownChecksumValid: true,
			}},
			want: "prefix version",
		},
		{
			name: "up drift",
			applied: []Applied{{
				Version: "000001_first", Checksum: "changed",
			}},
			want: "checksum differs",
		},
		{
			name: "established down drift",
			applied: []Applied{{
				Version: "000001_first", Checksum: "up-one", DownChecksum: "changed", DownChecksumValid: true,
			}},
			want: "down checksum differs",
		},
		{
			name: "longer than contract",
			applied: []Applied{
				{Version: "000001_first", Checksum: "up-one", DownChecksum: "down-one", DownChecksumValid: true},
				{Version: "000002_second", Checksum: "up-two", DownChecksum: "down-two", DownChecksumValid: true},
				{Version: "000003_unknown", Checksum: "up-three"},
			},
			want: "longer than",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := verifyAppliedPrefix(expected, test.applied)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("verifyAppliedPrefix() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestExpectedVersionsUseEmbeddedChecksums(t *testing.T) {
	expected, err := expectedVersions()
	if err != nil {
		t.Fatal(err)
	}
	if len(expected) == 0 {
		t.Fatal("embedded migration contract is empty")
	}
	for _, migration := range expected {
		if migration.Version == "" || len(migration.Checksum) != 64 ||
			len(migration.DownChecksum) != 64 || !migration.DownChecksumValid {
			t.Fatalf("invalid embedded migration identity: %#v", migration)
		}
	}
}

func TestMigrationDiscoveryRejectsIncompleteDuplicateAndNoncanonicalPairs(t *testing.T) {
	tests := []struct {
		name       string
		fileSystem fstest.MapFS
		want       string
	}{
		{
			name: "missing down",
			fileSystem: fstest.MapFS{
				"000001_first.up.sql": {Data: []byte("up")},
			},
			want: "exactly one up/down pair",
		},
		{
			name: "orphan down",
			fileSystem: fstest.MapFS{
				"000001_first.down.sql": {Data: []byte("down")},
			},
			want: "exactly one up/down pair",
		},
		{
			name: "duplicate sequence",
			fileSystem: fstest.MapFS{
				"000001_first.up.sql":    {Data: []byte("up")},
				"000001_first.down.sql":  {Data: []byte("down")},
				"000001_second.up.sql":   {Data: []byte("up")},
				"000001_second.down.sql": {Data: []byte("down")},
			},
			want: "sequence 000001 is duplicated",
		},
		{
			name: "noncanonical name",
			fileSystem: fstest.MapFS{
				"1_First.up.sql":   {Data: []byte("up")},
				"1_First.down.sql": {Data: []byte("down")},
			},
			want: "not canonically named",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := migrationPairs(test.fileSystem)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("migrationPairs() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestApplyFileEstablishesLegacyDownChecksumBaselineOnlyAfterUpMatch(t *testing.T) {
	name, version, checksum, downChecksum := firstMigrationIdentity(t)
	state := &migrationTestDatabase{}
	state.query = func(query string, arguments []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(query, "SELECT checksum, down_checksum") {
			t.Fatalf("unexpected query: %s", query)
		}
		assertMigrationArgument(t, arguments, 0, version)
		return &migrationTestRows{
			columns: []string{"checksum", "down_checksum"},
			values:  [][]driver.Value{{checksum, nil}},
		}, nil
	}
	state.exec = func(query string, arguments []driver.NamedValue) (driver.Result, error) {
		if !strings.Contains(query, "SET down_checksum = $2") || !strings.Contains(query, "down_checksum IS NULL") {
			t.Fatalf("unexpected exec: %s", query)
		}
		assertMigrationArgument(t, arguments, 0, version)
		assertMigrationArgument(t, arguments, 1, downChecksum)
		assertMigrationArgument(t, arguments, 2, checksum)
		return driver.RowsAffected(1), nil
	}

	database := sql.OpenDB(migrationTestConnector{state: state})
	t.Cleanup(func() { _ = database.Close() })
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	if err := applyFile(context.Background(), connection, name); err != nil {
		t.Fatalf("applyFile() legacy baseline error = %v", err)
	}
	if state.execCount != 1 {
		t.Fatalf("baseline exec count = %d, want 1", state.execCount)
	}

	state.execCount = 0
	state.query = func(string, []driver.NamedValue) (driver.Rows, error) {
		return &migrationTestRows{
			columns: []string{"checksum", "down_checksum"},
			values:  [][]driver.Value{{"wrong-up-checksum", nil}},
		}, nil
	}
	if err := applyFile(context.Background(), connection, name); err == nil || !strings.Contains(err.Error(), "checksum changed") {
		t.Fatalf("applyFile() mismatched legacy up error = %v", err)
	}
	if state.execCount != 0 {
		t.Fatalf("mismatched legacy up executed %d backfills, want 0", state.execCount)
	}
}

func TestApplyFileRejectsEstablishedDownChecksumDrift(t *testing.T) {
	name, _, checksum, _ := firstMigrationIdentity(t)
	state := &migrationTestDatabase{
		query: func(string, []driver.NamedValue) (driver.Rows, error) {
			return &migrationTestRows{
				columns: []string{"checksum", "down_checksum"},
				values:  [][]driver.Value{{checksum, "changed-down-checksum"}},
			}, nil
		},
	}
	database := sql.OpenDB(migrationTestConnector{state: state})
	t.Cleanup(func() { _ = database.Close() })
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	err = applyFile(context.Background(), connection, name)
	if err == nil || !strings.Contains(err.Error(), "down checksum changed") {
		t.Fatalf("applyFile() error = %v, want down checksum drift", err)
	}
	if state.execCount != 0 {
		t.Fatalf("down drift executed %d statements, want 0", state.execCount)
	}
}

func TestApplyFileRecordsBothChecksumsForNewMigration(t *testing.T) {
	name, version, checksum, downChecksum := firstMigrationIdentity(t)
	state := &migrationTestDatabase{}
	state.query = func(string, []driver.NamedValue) (driver.Rows, error) {
		return &migrationTestRows{columns: []string{"checksum", "down_checksum"}}, nil
	}
	state.exec = func(query string, arguments []driver.NamedValue) (driver.Result, error) {
		if strings.Contains(query, "INSERT INTO schema_migrations") {
			assertMigrationArgument(t, arguments, 0, version)
			assertMigrationArgument(t, arguments, 1, checksum)
			assertMigrationArgument(t, arguments, 2, downChecksum)
			state.insertCount++
		}
		return driver.RowsAffected(1), nil
	}

	database := sql.OpenDB(migrationTestConnector{state: state})
	t.Cleanup(func() { _ = database.Close() })
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	if err := applyFile(context.Background(), connection, name); err != nil {
		t.Fatalf("applyFile() new migration error = %v", err)
	}
	if state.beginCount != 1 || state.commitCount != 1 || state.rollbackCount != 0 {
		t.Fatalf(
			"transaction counts = begin:%d commit:%d rollback:%d, want 1/1/0",
			state.beginCount, state.commitCount, state.rollbackCount,
		)
	}
	if state.insertCount != 1 {
		t.Fatalf("dual-checksum insert count = %d, want 1", state.insertCount)
	}
}

func TestVerifyCurrentIsReadOnlyAndFailsClosedWithoutDownChecksumIntegrity(t *testing.T) {
	t.Run("missing column", func(t *testing.T) {
		state := &migrationTestDatabase{
			query: func(string, []driver.NamedValue) (driver.Rows, error) {
				return nil, errors.New(`column "down_checksum" does not exist`)
			},
		}
		database := sql.OpenDB(migrationTestConnector{state: state})
		t.Cleanup(func() { _ = database.Close() })

		err := VerifyCurrent(context.Background(), database)
		if err == nil || !strings.Contains(err.Error(), "read schema migration head") {
			t.Fatalf("VerifyCurrent() error = %v, want missing integrity column failure", err)
		}
		if state.execCount != 0 {
			t.Fatalf("VerifyCurrent() executed %d statements, want 0", state.execCount)
		}
	})

	t.Run("legacy null values", func(t *testing.T) {
		expected, err := expectedVersions()
		if err != nil {
			t.Fatal(err)
		}
		values := make([][]driver.Value, 0, len(expected))
		for _, migration := range expected {
			values = append(values, []driver.Value{
				migration.Version,
				migration.Checksum,
				nil,
				time.Unix(0, 0),
			})
		}
		state := &migrationTestDatabase{
			query: func(string, []driver.NamedValue) (driver.Rows, error) {
				return &migrationTestRows{
					columns: []string{"version", "checksum", "down_checksum", "applied_at"},
					values:  values,
				}, nil
			},
		}
		database := sql.OpenDB(migrationTestConnector{state: state})
		t.Cleanup(func() { _ = database.Close() })

		err = VerifyCurrent(context.Background(), database)
		if err == nil || !strings.Contains(err.Error(), "down checksum differs") {
			t.Fatalf("VerifyCurrent() error = %v, want legacy null failure", err)
		}
		if state.execCount != 0 {
			t.Fatalf("VerifyCurrent() executed %d backfills, want 0", state.execCount)
		}
	})
}

func TestUpRejectsUnknownMigrationBeforeBaselineOrNewMigrationWrites(t *testing.T) {
	var applyInspectionCount int
	var migrationWriteCount int
	state := &migrationTestDatabase{}
	state.exec = func(query string, _ []driver.NamedValue) (driver.Result, error) {
		if strings.Contains(query, "UPDATE schema_migrations") ||
			strings.Contains(query, "INSERT INTO schema_migrations") {
			migrationWriteCount++
		}
		return driver.RowsAffected(1), nil
	}
	state.query = func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(query, "SELECT version, checksum, down_checksum"):
			return &migrationTestRows{
				columns: []string{"version", "checksum", "down_checksum", "applied_at"},
				values: [][]driver.Value{{
					"999999_unknown", strings.Repeat("a", 64), nil, time.Unix(0, 0),
				}},
			}, nil
		case strings.Contains(query, "SELECT checksum, down_checksum"):
			applyInspectionCount++
			return nil, errors.New("apply inspection must not run before prefix preflight")
		case strings.Contains(query, "pg_advisory_unlock"):
			return &migrationTestRows{
				columns: []string{"pg_advisory_unlock"},
				values:  [][]driver.Value{{true}},
			}, nil
		default:
			return nil, errors.New("unexpected query")
		}
	}

	database := sql.OpenDB(migrationTestConnector{state: state})
	t.Cleanup(func() { _ = database.Close() })
	err := Up(context.Background(), database)
	if err == nil || !strings.Contains(err.Error(), "migration prefix version") {
		t.Fatalf("Up() error = %v, want unknown-prefix rejection", err)
	}
	if applyInspectionCount != 0 || migrationWriteCount != 0 {
		t.Fatalf(
			"Up() reached apply path: inspections=%d migration-writes=%d, want 0/0",
			applyInspectionCount, migrationWriteCount,
		)
	}
}

func TestMigrationPairIntegrityLegacyPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()

	schema := "migration_pair_legacy_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	if _, err := database.ExecContext(ctx, `
CREATE TABLE schema_migrations (
  version text PRIMARY KEY,
  checksum text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		t.Fatal(err)
	}
	name, version, checksum, downChecksum := firstMigrationIdentity(t)
	contents, err := files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(contents)); err != nil {
		t.Fatalf("apply legacy migration fixture: %v", err)
	}
	if _, err := database.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)`,
		version, checksum,
	); err != nil {
		t.Fatal(err)
	}

	if err := VerifyCurrent(ctx, database); err == nil {
		t.Fatal("VerifyCurrent() accepted legacy table without down_checksum")
	}
	if err := Up(ctx, database); err != nil {
		t.Fatalf("Up() legacy pair-integrity upgrade: %v", err)
	}
	var actualDownChecksum string
	var nullable string
	if err := database.QueryRowContext(ctx, `
SELECT down_checksum,
       is_nullable
FROM schema_migrations
JOIN information_schema.columns
  ON table_schema = current_schema()
 AND table_name = 'schema_migrations'
 AND column_name = 'down_checksum'
WHERE version = $1`, version).Scan(&actualDownChecksum, &nullable); err != nil {
		t.Fatal(err)
	}
	if actualDownChecksum != downChecksum || nullable != "NO" {
		t.Fatalf(
			"legacy baseline = checksum:%q nullable:%q, want exact checksum and NO",
			actualDownChecksum, nullable,
		)
	}
	if err := VerifyCurrent(ctx, database); err != nil {
		t.Fatalf("VerifyCurrent() rejected upgraded legacy schema: %v", err)
	}
}

func firstMigrationIdentity(t *testing.T) (name, version, checksum, downChecksum string) {
	t.Helper()
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("embedded migration contract is empty")
	}
	name = names[0]
	version = strings.TrimSuffix(name, ".up.sql")
	upContents, err := files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	downContents, err := files.ReadFile(version + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upDigest := sha256.Sum256(upContents)
	downDigest := sha256.Sum256(downContents)
	return name, version, hex.EncodeToString(upDigest[:]), hex.EncodeToString(downDigest[:])
}

func assertMigrationArgument(t *testing.T, arguments []driver.NamedValue, index int, want string) {
	t.Helper()
	if len(arguments) <= index || arguments[index].Value != want {
		t.Fatalf("argument %d = %#v, want %q", index+1, arguments, want)
	}
}

type migrationTestConnector struct {
	state *migrationTestDatabase
}

func (connector migrationTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &migrationTestConnection{state: connector.state}, nil
}

func (connector migrationTestConnector) Driver() driver.Driver {
	return migrationTestDriver{state: connector.state}
}

type migrationTestDriver struct {
	state *migrationTestDatabase
}

func (testDriver migrationTestDriver) Open(string) (driver.Conn, error) {
	return &migrationTestConnection{state: testDriver.state}, nil
}

type migrationTestDatabase struct {
	query         func(string, []driver.NamedValue) (driver.Rows, error)
	exec          func(string, []driver.NamedValue) (driver.Result, error)
	execCount     int
	insertCount   int
	beginCount    int
	commitCount   int
	rollbackCount int
}

type migrationTestConnection struct {
	state *migrationTestDatabase
}

func (connection *migrationTestConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported by migration test driver")
}

func (connection *migrationTestConnection) Close() error { return nil }

func (connection *migrationTestConnection) Begin() (driver.Tx, error) {
	return connection.BeginTx(context.Background(), driver.TxOptions{})
}

func (connection *migrationTestConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	connection.state.beginCount++
	return &migrationTestTransaction{state: connection.state}, nil
}

func (connection *migrationTestConnection) QueryContext(
	_ context.Context,
	query string,
	arguments []driver.NamedValue,
) (driver.Rows, error) {
	if connection.state.query == nil {
		return nil, errors.New("unexpected query")
	}
	return connection.state.query(query, arguments)
}

func (connection *migrationTestConnection) ExecContext(
	_ context.Context,
	query string,
	arguments []driver.NamedValue,
) (driver.Result, error) {
	connection.state.execCount++
	if connection.state.exec == nil {
		return nil, errors.New("unexpected exec")
	}
	return connection.state.exec(query, arguments)
}

type migrationTestTransaction struct {
	state *migrationTestDatabase
}

func (transaction *migrationTestTransaction) Commit() error {
	transaction.state.commitCount++
	return nil
}

func (transaction *migrationTestTransaction) Rollback() error {
	transaction.state.rollbackCount++
	return nil
}

type migrationTestRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (rows *migrationTestRows) Columns() []string { return rows.columns }

func (rows *migrationTestRows) Close() error { return nil }

func (rows *migrationTestRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}
