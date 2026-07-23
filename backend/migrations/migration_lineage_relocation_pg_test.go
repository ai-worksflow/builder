package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestLegacyCandidateMigrationRelocationPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	for _, role := range []string{"worksflow_migration_owner", "worksflow_schema_migrator"} {
		var exists bool
		if err := base.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Skipf("%s is not provisioned on the opt-in PostgreSQL canary", role)
		}
	}

	schema := "migration_lineage_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := base.ExecContext(
			context.Background(),
			`DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`,
		); err != nil {
			t.Errorf("drop migration-lineage canary schema: %v", err)
		}
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
  down_checksum text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
);
GRANT USAGE ON SCHEMA `+quotePostgresIdentifier(schema)+` TO worksflow_migration_owner`); err != nil {
		t.Fatal(err)
	}

	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	expected, err := expectedVersions()
	if err != nil {
		t.Fatal(err)
	}
	predecessorIndex := appliedMigrationIndex(
		expected, acceptedMigrationVersionRelocation.requiredPredecessor,
	)
	if predecessorIndex < 0 {
		t.Fatal("embedded relocation predecessor is absent")
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names[:predecessorIndex+1] {
		if err := applyFile(ctx, connection, name); err != nil {
			_ = connection.Close()
			t.Fatalf("apply legacy predecessor %s: %v", name, err)
		}
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}

	old83 := acceptedMigrationChecksumRotations["000083_canonical_review_authority_forward_equivalence"][0]
	old82 := acceptedMigrationChecksumRotations["000082_qualification_handoff_v1"][0]
	if _, err := database.ExecContext(ctx, `
DROP TABLE canonical_review_83_legacy_release_acl_provenance;
REVOKE EXECUTE ON FUNCTION release_delivery_canonical_json(jsonb)
  FROM worksflow_migration_owner;
REVOKE EXECUTE ON FUNCTION release_delivery_embedded_hash_is_exact(jsonb,text)
  FROM worksflow_migration_owner;
REVOKE EXECUTE ON FUNCTION release_delivery_rfc3339_microsecond(timestamptz)
  FROM worksflow_migration_owner`); err != nil {
		t.Fatalf("establish exact old checksum and critical physical catalog: %v", err)
	}
	for _, rotation := range []migrationChecksumRotation{old82, old83} {
		if _, err := database.ExecContext(ctx, `
UPDATE schema_migrations
SET checksum = $1, down_checksum = $2
WHERE version = $3`,
			rotation.fromChecksum, rotation.fromDown, rotation.version,
		); err != nil {
			t.Fatalf("establish old ledger identity for %s: %v", rotation.version, err)
		}
	}
	assertLegacy83RepairState(t, ctx, database, old83.fromChecksum, false, 0)

	relocation := acceptedMigrationVersionRelocation
	candidateSQL, err := files.ReadFile(relocation.toVersion + ".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, string(candidateSQL)); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("apply legacy sequence-84 Candidate migration bytes: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO schema_migrations(version,checksum,down_checksum)
VALUES ($1,$2,$3)`, relocation.fromVersion, relocation.checksum, relocation.downChecksum); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	wrongLegacyChecksum := strings.Repeat("f", 64)
	if _, err := database.ExecContext(ctx, `
UPDATE schema_migrations SET checksum=$1 WHERE version=$2`,
		wrongLegacyChecksum, relocation.fromVersion,
	); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, database); err == nil || !strings.Contains(err.Error(), "accepted relocation identity") {
		t.Fatalf("wrong legacy checksum Up() error = %v", err)
	}
	assertMigrationLedgerIdentity(t, ctx, database, relocation.fromVersion, wrongLegacyChecksum, relocation.downChecksum)
	assertMigrationVersionAbsent(t, ctx, database, relocation.toVersion)
	assertMigrationVersionAbsent(t, ctx, database, expected[predecessorIndex+1].Version)
	assertLegacy83RepairState(t, ctx, database, old83.fromChecksum, false, 0)
	if _, err := database.ExecContext(ctx, `
UPDATE schema_migrations SET checksum=$1 WHERE version=$2`,
		relocation.checksum, relocation.fromVersion,
	); err != nil {
		t.Fatal(err)
	}

	lowPrivilege, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lowPrivilege.ExecContext(ctx, `SET ROLE worksflow_schema_migrator`); err != nil {
		_ = lowPrivilege.Close()
		t.Fatal(err)
	}
	err = applyMigrationChecksumRotation(ctx, lowPrivilege, old83)
	_, resetErr := lowPrivilege.ExecContext(ctx, `RESET ROLE`)
	_ = lowPrivilege.Close()
	if resetErr != nil {
		t.Fatal(resetErr)
	}
	var permissionError *pgconn.PgError
	if err == nil || !errors.As(err, &permissionError) || permissionError.Code != "42501" {
		t.Fatalf("low-privilege repair error = %v, want SQLSTATE 42501", err)
	}
	assertLegacy83RepairState(t, ctx, database, old83.fromChecksum, false, 0)

	if _, err := database.ExecContext(ctx, `
CREATE FUNCTION qualification_release_v1_hash(text,bytea)
RETURNS text LANGUAGE sql IMMUTABLE STRICT AS 'SELECT $1'`); err != nil {
		t.Fatal(err)
	}
	err = Up(ctx, database)
	if err == nil || !strings.Contains(err.Error(), "000084_workflow_execution_profile_v3_qualified_release") {
		t.Fatalf("blocked migration 84 Up() error = %v", err)
	}
	assertMigrationLedgerIdentity(t, ctx, database, old82.version, old82.fromChecksum, old82.fromDown)
	assertMigrationVersionAbsent(t, ctx, database, expected[predecessorIndex+1].Version)
	assertMigrationLedgerIdentity(t, ctx, database, relocation.toVersion, relocation.checksum, relocation.downChecksum)
	assertLegacy83RepairState(t, ctx, database, old83.toChecksum, true, 3)
	if _, err := database.ExecContext(ctx, `
DROP FUNCTION qualification_release_v1_hash(text,bytea)`); err != nil {
		t.Fatal(err)
	}

	if err := Up(ctx, database); err != nil {
		t.Fatalf("repair and complete relocated migration lineage: %v", err)
	}
	if err := VerifyCurrent(ctx, database); err != nil {
		t.Fatalf("VerifyCurrent() rejected repaired exact head: %v", err)
	}
	assertMigrationLedgerIdentity(t, ctx, database, old82.version, old82.toChecksum, old82.toDown)
	assertLegacy83RepairState(t, ctx, database, old83.toChecksum, true, 3)
	if err := Up(ctx, database); err != nil {
		t.Fatalf("idempotent Up() replay rejected repaired exact head: %v", err)
	}
	if err := exerciseLegacyReleaseHelpersAsMigrationOwner(ctx, database); err != nil {
		t.Fatalf("repaired legacy Release helper authority is unusable: %v", err)
	}
}

func assertLegacy83RepairState(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	wantChecksum string,
	wantProvenance bool,
	wantGrantCount int,
) {
	t.Helper()
	assertMigrationLedgerIdentity(
		t, ctx, database,
		"000083_canonical_review_authority_forward_equivalence",
		wantChecksum,
		map[string]string{
			"8012de08459951e4c9aaff9aed21bb86d4c451d1e6e4932cff60fd99756b4e42": "fa23583719be79393ead487515142836d71ac19f62a5ea7ed537fceabaae4a5e",
			"08383d054fb24b3cfc8542391bce9971584c9543a5621e1be3a1302b5409ed20": "df418ab352c1ddc8c41b4919d23dbb46b4c404b7eb001502141cfc6a04a79a16",
		}[wantChecksum],
	)
	var provenanceExists bool
	if err := database.QueryRowContext(ctx, `
SELECT pg_catalog.to_regclass(
  pg_catalog.format('%I.canonical_review_83_legacy_release_acl_provenance',current_schema())
) IS NOT NULL`).Scan(&provenanceExists); err != nil {
		t.Fatal(err)
	}
	if provenanceExists != wantProvenance {
		t.Fatalf("83 ACL provenance exists = %t, want %t", provenanceExists, wantProvenance)
	}
	var grantCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM (VALUES
  ('release_delivery_canonical_json(jsonb)'),
  ('release_delivery_embedded_hash_is_exact(jsonb,text)'),
  ('release_delivery_rfc3339_microsecond(timestamptz)')
) AS helper(signature)
WHERE pg_catalog.has_function_privilege(
  'worksflow_migration_owner',
  pg_catalog.to_regprocedure(pg_catalog.format('%I.%s',current_schema(),signature)),
  'EXECUTE'
)`).Scan(&grantCount); err != nil {
		t.Fatal(err)
	}
	if grantCount != wantGrantCount {
		t.Fatalf("migration owner legacy helper grants = %d, want %d", grantCount, wantGrantCount)
	}
	if provenanceExists {
		var provenanceCount int
		if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM canonical_review_83_legacy_release_acl_provenance`).Scan(&provenanceCount); err != nil {
			t.Fatal(err)
		}
		if provenanceCount != wantGrantCount {
			t.Fatalf("83 ACL provenance rows = %d, want %d", provenanceCount, wantGrantCount)
		}
	}
}

func assertMigrationLedgerIdentity(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	version string,
	checksum string,
	downChecksum string,
) {
	t.Helper()
	var actualChecksum string
	var actualDown string
	if err := database.QueryRowContext(ctx, `
SELECT checksum,down_checksum FROM schema_migrations WHERE version=$1`, version).Scan(
		&actualChecksum, &actualDown,
	); err != nil {
		t.Fatal(err)
	}
	if actualChecksum != checksum || actualDown != downChecksum {
		t.Fatalf(
			"migration %s ledger = %s/%s, want %s/%s",
			version, actualChecksum, actualDown, checksum, downChecksum,
		)
	}
}

func assertMigrationVersionAbsent(t *testing.T, ctx context.Context, database *sql.DB, version string) {
	t.Helper()
	var exists bool
	if err := database.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatalf("migration %s unexpectedly exists", version)
	}
}

func exerciseLegacyReleaseHelpersAsMigrationOwner(ctx context.Context, database *sql.DB) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `SET LOCAL ROLE worksflow_migration_owner`); err != nil {
		return err
	}
	var canonical string
	var embeddedExact bool
	var timestamp string
	if err := transaction.QueryRowContext(ctx, `
SELECT release_delivery_canonical_json('{}'::jsonb),
       release_delivery_embedded_hash_is_exact('{}'::jsonb,'hash'),
       release_delivery_rfc3339_microsecond('2026-07-21T00:00:00Z'::timestamptz)`).Scan(
		&canonical, &embeddedExact, &timestamp,
	); err != nil {
		return err
	}
	if canonical != "{}" || embeddedExact || timestamp != "2026-07-21T00:00:00Z" {
		return fmt.Errorf("unexpected helper results: %q/%t/%q", canonical, embeddedExact, timestamp)
	}
	return transaction.Rollback()
}

func quotePostgresIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
