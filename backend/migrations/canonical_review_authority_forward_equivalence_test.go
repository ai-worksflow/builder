package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	canonicalReviewForwardEquivalenceUp   = "000083_canonical_review_authority_forward_equivalence.up.sql"
	canonicalReviewForwardEquivalenceDown = "000083_canonical_review_authority_forward_equivalence.down.sql"
	canonicalReviewTimestampComment       = "UTC microsecond timestamp predicate that rejects PostgreSQL-normalized noncanonical calendar values."
	canonicalReviewBoundaryWhitespace     = `U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'`
)

func TestCanonicalReviewAuthorityForwardEquivalenceMigrationContract(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(canonicalReviewForwardEquivalenceUp)
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile(canonicalReviewForwardEquivalenceDown)
	if err != nil {
		t.Fatal(err)
	}
	hardened, err := files.ReadFile(canonicalReviewAuthorityHardeningMigration)
	if err != nil {
		t.Fatal(err)
	}

	upText := string(up)
	downText := string(down)
	for _, required := range []string{
		"$canonical_review_83_timestamp_security$;",
		"REVOKE ALL ON FUNCTION %I.canonical_review_timestamp_is_exact(text) FROM PUBLIC",
		"ALTER FUNCTION %I.canonical_review_timestamp_is_exact(text) OWNER TO worksflow_migration_owner",
		"canonical_review_83_legacy_release_acl_provenance",
		"GRANT EXECUTE ON FUNCTION %I.%s TO worksflow_migration_owner",
		"release_delivery_canonical_json(jsonb)",
		"release_delivery_embedded_hash_is_exact(jsonb,text)",
		"release_delivery_rfc3339_microsecond(timestamptz)",
		"'worksflow_application','worksflow_schema_migrator','worksflow_auditor'",
		canonicalReviewBoundaryWhitespace,
		canonicalReviewTimestampComment,
	} {
		if !strings.Contains(upText, required) {
			t.Fatalf("forward-equivalence up migration is missing %q", required)
		}
	}
	if strings.Count(upText, "$function$;") != 3 {
		t.Fatalf("up function terminators = %d, want 3", strings.Count(upText, "$function$;"))
	}
	if strings.Count(downText, "$function$;") != 2 {
		t.Fatalf("down function terminators = %d, want 2", strings.Count(downText, "$function$;"))
	}
	if bytes.Contains(up, []byte{'\r'}) || bytes.Contains(down, []byte{'\r'}) {
		t.Fatal("forward-equivalence SQL must encode carriage return instead of embedding it")
	}
	if !strings.Contains(downText, canonicalReviewBoundaryWhitespace) {
		t.Fatal("forward-equivalence down migration lost the canonical carriage-return trim member")
	}
	if !strings.Contains(downText,
		"REVOKE EXECUTE ON FUNCTION %I.%s FROM worksflow_migration_owner",
	) || !strings.Contains(downText,
		"DROP TABLE IF EXISTS canonical_review_83_legacy_release_acl_provenance",
	) {
		t.Fatal("forward-equivalence down migration does not restore legacy Release helper ACL")
	}

	upCheck := canonicalReviewForwardEquivalenceCheck(t, upText)
	hardenedCheck := canonicalReviewForwardEquivalenceCheck(t, string(hardened))
	if upCheck != hardenedCheck {
		t.Fatal("forward-equivalence CHECK is not the exact hardened 000077 CHECK")
	}
	downCheck := canonicalReviewForwardEquivalenceCheck(t, downText)
	for _, unexpected := range []string{
		"reviewer_role_at_decision IS NOT NULL",
		"governance_mode_at_decision IS NOT NULL",
		"owner_count_at_decision IS NOT NULL",
	} {
		if strings.Contains(downCheck, unexpected) {
			t.Fatalf("down migration no longer restores the historical CHECK: found %q", unexpected)
		}
	}
}

func canonicalReviewForwardEquivalenceCheck(t *testing.T, migration string) string {
	t.Helper()
	const start = "ADD CONSTRAINT review_decisions_authority_facts_check CHECK ("
	startIndex := strings.Index(migration, start)
	if startIndex < 0 {
		t.Fatalf("migration is missing %q", start)
	}
	const end = "\n  );"
	endIndex := strings.Index(migration[startIndex:], end)
	if endIndex < 0 {
		t.Fatal("migration CHECK has no canonical terminator")
	}
	endIndex += startIndex + len(end)
	return migration[startIndex:endIndex]
}

type canonicalReviewForwardFunctionState struct {
	Definition string
	Owner      string
	ACL        string
	Comment    string
}

func TestCanonicalReviewAuthorityForwardEquivalencePostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	applyPostgresMigrationsForCanary(t, database)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	baselineFunctions := canonicalReviewForwardFunctionCatalog(t, ctx, database)
	baselineCheck := canonicalReviewForwardConstraint(t, ctx, database)
	canonicalReviewAssertTimestampSecurity(t, ctx, database, baselineFunctions)
	if !strings.Contains(baselineCheck, "\r") {
		t.Fatal("hardened review decision CHECK does not retain carriage return")
	}

	up, err := files.ReadFile(canonicalReviewForwardEquivalenceUp)
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile(canonicalReviewForwardEquivalenceDown)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("apply forward-equivalence down migration: %v", err)
	}
	var timestampAbsent bool
	if err := transaction.QueryRowContext(ctx, `
SELECT to_regprocedure('public.canonical_review_timestamp_is_exact(text)') IS NULL
`).Scan(&timestampAbsent); err != nil {
		t.Fatal(err)
	}
	if !timestampAbsent {
		t.Fatal("down migration retained canonical_review_timestamp_is_exact(text)")
	}
	if _, err := transaction.ExecContext(ctx, string(up)); err != nil {
		t.Fatalf("reapply forward-equivalence up migration: %v", err)
	}

	upgradedFunctions := canonicalReviewForwardFunctionCatalog(t, ctx, transaction)
	upgradedCheck := canonicalReviewForwardConstraint(t, ctx, transaction)
	if !reflect.DeepEqual(upgradedFunctions, baselineFunctions) {
		t.Fatalf("historical->000083 function catalog differs from fresh path:\nupgraded=%#v\nbaseline=%#v", upgradedFunctions, baselineFunctions)
	}
	if upgradedCheck != baselineCheck {
		t.Fatalf("historical->000083 CHECK differs from fresh path:\nupgraded=%s\nbaseline=%s", upgradedCheck, baselineCheck)
	}
	canonicalReviewAssertTimestampSecurity(t, ctx, transaction, upgradedFunctions)
	canonicalReviewAssertCarriageReturnRejected(t, ctx, transaction, upgradedCheck)
}

type canonicalReviewForwardQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func canonicalReviewForwardFunctionCatalog(
	t *testing.T,
	ctx context.Context,
	queryer canonicalReviewForwardQueryer,
) map[string]canonicalReviewForwardFunctionState {
	t.Helper()
	result := make(map[string]canonicalReviewForwardFunctionState, 3)
	for _, name := range []string{
		"canonical_review_approval_receipt_record_is_exact",
		"canonical_review_timestamp_is_exact",
		"issue_canonical_review_approval_receipt",
	} {
		var state canonicalReviewForwardFunctionState
		err := queryer.QueryRowContext(ctx, `
SELECT pg_catalog.pg_get_functiondef(routine.oid),
       owner.rolname,
       COALESCE(routine.proacl::text, ''),
       COALESCE(pg_catalog.obj_description(routine.oid, 'pg_proc'), '')
FROM pg_catalog.pg_proc AS routine
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = routine.pronamespace
JOIN pg_catalog.pg_roles AS owner ON owner.oid = routine.proowner
WHERE namespace.nspname = 'public'
  AND routine.proname = $1
`, name).Scan(&state.Definition, &state.Owner, &state.ACL, &state.Comment)
		if err != nil {
			t.Fatalf("read %s catalog: %v", name, err)
		}
		result[name] = state
	}
	return result
}

func canonicalReviewForwardConstraint(
	t *testing.T,
	ctx context.Context,
	queryer canonicalReviewForwardQueryer,
) string {
	t.Helper()
	var definition string
	err := queryer.QueryRowContext(ctx, `
SELECT pg_catalog.pg_get_constraintdef(constraint_record.oid, true)
FROM pg_catalog.pg_constraint AS constraint_record
WHERE constraint_record.conrelid = 'public.review_decisions'::pg_catalog.regclass
  AND constraint_record.conname = 'review_decisions_authority_facts_check'
`).Scan(&definition)
	if err != nil {
		t.Fatalf("read review decision CHECK: %v", err)
	}
	return definition
}

func canonicalReviewAssertTimestampSecurity(
	t *testing.T,
	ctx context.Context,
	queryer canonicalReviewForwardQueryer,
	functions map[string]canonicalReviewForwardFunctionState,
) {
	t.Helper()
	state := functions["canonical_review_timestamp_is_exact"]
	if state.Comment != canonicalReviewTimestampComment {
		t.Fatalf("timestamp comment = %q", state.Comment)
	}
	var migrationOwnerExists bool
	if err := queryer.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'worksflow_migration_owner')
`).Scan(&migrationOwnerExists); err != nil {
		t.Fatal(err)
	}
	if migrationOwnerExists && state.Owner != "worksflow_migration_owner" {
		t.Fatalf("timestamp owner = %q, want worksflow_migration_owner", state.Owner)
	}
	var ownerOnly bool
	err := queryer.QueryRowContext(ctx, `
SELECT NOT EXISTS (
  SELECT 1
  FROM pg_catalog.pg_proc AS routine
  CROSS JOIN LATERAL pg_catalog.aclexplode(
    COALESCE(routine.proacl, pg_catalog.acldefault('f', routine.proowner))
  ) AS routine_acl
  WHERE routine.oid = 'public.canonical_review_timestamp_is_exact(text)'::pg_catalog.regprocedure
    AND (
      routine_acl.grantee <> routine.proowner
      OR routine_acl.privilege_type <> 'EXECUTE'
      OR routine_acl.is_grantable
    )
)
`).Scan(&ownerOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !ownerOnly {
		t.Fatal("timestamp function ACL is not owner-only")
	}
}

func canonicalReviewAssertCarriageReturnRejected(
	t *testing.T,
	ctx context.Context,
	transaction *sql.Tx,
	constraintDefinition string,
) {
	t.Helper()
	if _, err := transaction.ExecContext(ctx, `
CREATE TEMPORARY TABLE canonical_review_forward_equivalence_decision_check (
  review_authority_version integer,
  reviewer_role_at_decision text,
  governance_mode_at_decision text,
  owner_count_at_decision integer,
  sole_owner_id_at_decision uuid,
  reviewer_id uuid,
  solo_review_confirmed boolean,
  precondition_etag text,
  summary text,
  solo_self_review boolean,
  decision text
) ON COMMIT DROP
`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx,
		"ALTER TABLE canonical_review_forward_equivalence_decision_check "+
			"ADD CONSTRAINT canonical_review_forward_equivalence_check "+constraintDefinition,
	); err != nil {
		t.Fatalf("install copied decision CHECK: %v", err)
	}
	const insert = `
INSERT INTO canonical_review_forward_equivalence_decision_check (
  review_authority_version, reviewer_role_at_decision, governance_mode_at_decision,
  owner_count_at_decision, sole_owner_id_at_decision, reviewer_id,
  solo_review_confirmed, precondition_etag, summary, solo_self_review, decision
) VALUES (
  1, 'owner', 'solo', 1,
  '00000000-0000-4000-8000-000000000001',
  '00000000-0000-4000-8000-000000000001',
  false, 'etag', $1, false, 'approve'
)
`
	if _, err := transaction.ExecContext(ctx, insert, "canonical summary"); err != nil {
		t.Fatalf("clean decision summary rejected: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, "SAVEPOINT canonical_review_cr_rejection"); err != nil {
		t.Fatal(err)
	}
	_, insertErr := transaction.ExecContext(ctx, insert, "\rcanonical summary\r")
	if _, err := transaction.ExecContext(ctx, "ROLLBACK TO SAVEPOINT canonical_review_cr_rejection"); err != nil {
		t.Fatalf("rollback expected CR rejection: %v", err)
	}
	if insertErr == nil {
		t.Fatal("review decision CHECK accepted leading/trailing carriage return")
	}
	var postgresError *pgconn.PgError
	if !errors.As(insertErr, &postgresError) || postgresError.Code != "23514" {
		t.Fatalf("CR rejection error = %v, want PostgreSQL 23514", insertErr)
	}
}
