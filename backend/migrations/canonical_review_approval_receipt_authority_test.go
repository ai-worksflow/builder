package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	canonicalReviewAuthorityMigration          = "000076_canonical_review_approval_receipt_authority.up.sql"
	canonicalReviewAuthorityHardeningMigration = "000077_canonical_review_authority_hardening.up.sql"
)

func TestCanonicalReviewApprovalReceiptAuthorityDeclaresExactAtomicBoundary(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(canonicalReviewAuthorityMigration)
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000076_canonical_review_approval_receipt_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{
		"UPDATE review_requests SET review_authority_version = 0",
		"UPDATE review_decisions SET review_authority_version = 0",
		"ALTER COLUMN review_authority_version SET DEFAULT 1",
		"CREATE TABLE canonical_review_approval_receipts",
		"CONSTRAINT canonical_review_receipts_hash_key UNIQUE (receipt_hash)",
		"CONSTRAINT canonical_review_receipts_revision_key UNIQUE (revision_id)",
		"worksflow-canonical-review-authority-hash/v1",
		"worksflow-canonical-review-approval-receipt/v1",
		"application/vnd.worksflow.canonical-review-approval-receipt+json;version=1",
		"CREATE FUNCTION canonical_review_jsonb_bytes(p_value jsonb)",
		"CREATE FUNCTION canonical_review_approval_receipt_record_is_exact(",
		"CREATE FUNCTION issue_canonical_review_approval_receipt(p_review_request_id uuid)",
		"CREATE FUNCTION resolve_canonical_review_approval_receipt(",
		"CREATE FUNCTION canonical_review_approval_receipt_is_exact(",
		"CREATE FUNCTION require_canonical_review_approval_receipt()",
		"CREATE CONSTRAINT TRIGGER canonical_review_approved_requires_receipt",
		"DEFERRABLE INITIALLY DEFERRED",
		"Version 1 approved review requires its exact atomic Canonical Review receipt",
		"CREATE FUNCTION guard_canonical_review_source_mutation()",
		"Closed review requests are immutable",
		"Canonical Review approval receipts are immutable",
		"FROM projects WHERE id = v_project_id FOR UPDATE",
		"v_revision.superseded_at IS NOT NULL",
		"current_member.role = decision.reviewer_role_at_decision",
		"decision.governance_mode_at_decision <> v_policy_mode",
		"SELECT * INTO v_existing FROM resolve_canonical_review_approval_receipt(",
		"REVOKE ALL ON TABLE %I.canonical_review_approval_receipts FROM %I",
		"GRANT EXECUTE ON FUNCTION %I.issue_canonical_review_approval_receipt(uuid) TO worksflow_application",
		"GRANT EXECUTE ON FUNCTION %I.canonical_review_approval_receipt_is_exact(uuid,uuid,uuid) TO worksflow_application",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Canonical Review authority migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"UPDATE review_requests SET review_authority_version = 1",
		"UPDATE review_decisions SET review_authority_version = 1",
		"INSERT INTO canonical_review_approval_receipts SELECT",
		"ON DELETE CASCADE",
		"GRANT SELECT ON TABLE %I.canonical_review_approval_receipts TO worksflow_application",
		"GRANT INSERT ON TABLE %I.canonical_review_approval_receipts TO worksflow_application",
		"GRANT UPDATE ON TABLE %I.canonical_review_approval_receipts TO worksflow_application",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Canonical Review authority migration unexpectedly contains %q", forbidden)
		}
	}
	for _, required := range []string{
		"cannot roll back Canonical Review authority after version 1 review state exists",
		"DROP TRIGGER IF EXISTS canonical_review_approved_requires_receipt",
		"DROP FUNCTION IF EXISTS canonical_review_approval_receipt_is_exact(uuid,uuid,uuid)",
		"DROP FUNCTION IF EXISTS resolve_canonical_review_approval_receipt(uuid,uuid,text)",
		"DROP FUNCTION IF EXISTS issue_canonical_review_approval_receipt(uuid)",
		"DROP TABLE canonical_review_approval_receipts",
		"DROP COLUMN review_authority_version",
	} {
		if !strings.Contains(string(down), required) {
			t.Fatalf("Canonical Review authority rollback is missing %q", required)
		}
	}
}

func TestCanonicalReviewAuthorityHardeningDeclaresAppendOnlyOCCBoundary(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(canonicalReviewAuthorityHardeningMigration)
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000077_canonical_review_authority_hardening.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{
		"LOCK TABLE canonical_review_approval_receipts IN ACCESS EXCLUSIVE MODE",
		"CREATE FUNCTION canonical_review_uuid_is_exact(p_value text)",
		"CREATE FUNCTION canonical_review_text_is_trimmed(p_value text)",
		"CREATE FUNCTION canonical_review_timestamp_is_exact(p_value text)",
		"Review decisions are append-only",
		"reviewer_role_at_decision IS NOT NULL",
		"governance_mode_at_decision IS NOT NULL",
		"owner_count_at_decision IS NOT NULL",
		"v_expected_precondition := format(",
		"history.prior_latest_ns",
		"canonical_review_uuid_is_exact(v_request.id::text) IS NOT TRUE",
		"NOT canonical_review_text_is_trimmed(decision.summary)",
		"canonical_review_timestamp_is_exact(v_decision->>'createdAt') IS NOT TRUE",
		"v_revision->>'createdBy' IS DISTINCT FROM v_value->>'soloSelfReviewOwnerId'",
		"v_request.requested_at < v_revision.created_at",
		"1678-01-01T00:00:00.000000Z",
		"GREATEST(",
		"(v_revision->>'byteSize')::bigint > 9007199254740991",
		"v_artifact.version < 1 OR v_artifact.version > 9007199254740991",
		"'ai_runtime_contract','deployment_contract','verification_contract'",
		"cannot apply Canonical Review hardening: an existing 000076 receipt is incompatible",
		"ALTER FUNCTION %I.canonical_review_uuid_is_exact(text) OWNER TO worksflow_migration_owner",
		"REVOKE ALL ON FUNCTION %I.canonical_review_text_is_trimmed(text) FROM %I",
		"REVOKE ALL ON FUNCTION %I.canonical_review_timestamp_is_exact(text) FROM %I",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Canonical Review hardening migration is missing %q", required)
		}
	}
	for _, required := range []string{
		"cannot roll back Canonical Review hardening after a receipt exists",
		"review_authority_version = 1",
		"DROP FUNCTION canonical_review_text_is_trimmed(text)",
		"DROP FUNCTION canonical_review_timestamp_is_exact(text)",
		"DROP FUNCTION canonical_review_uuid_is_exact(text)",
	} {
		if !strings.Contains(string(down), required) {
			t.Fatalf("Canonical Review hardening rollback is missing %q", required)
		}
	}
}

func TestCanonicalReviewApprovalReceiptAuthorityPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "canonical_review_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	applyCanonicalReviewAuthorityPrerequisites(t, ctx, database)
	history := seedCanonicalReviewHistoryBeforeAuthority(t, ctx, database)
	applyCanonicalReviewAuthorityBaseMigration(t, ctx, database)
	assertCanonicalReviewHistoryRemainsUntrusted(t, ctx, database, history)
	publishedTransaction, publishedFixture := beginCanonicalReviewCanaryFixture(t, ctx, database, false)
	closeCanonicalReviewCanaryRequest(t, ctx, publishedTransaction, publishedFixture)
	var publishedHash string
	var publishedCreated bool
	if err := publishedTransaction.QueryRowContext(ctx, `
SELECT (issued.receipt_record).receipt_hash, issued.created
FROM issue_canonical_review_approval_receipt($1) AS issued
`, publishedFixture.requestID).Scan(&publishedHash, &publishedCreated); err != nil {
		_ = publishedTransaction.Rollback()
		t.Fatalf("issue compatible receipt under published 000076: %v", err)
	}
	if !publishedCreated || !canonicalReviewCanaryDigest(publishedHash) {
		_ = publishedTransaction.Rollback()
		t.Fatalf("published 000076 receipt created=%t hash=%q", publishedCreated, publishedHash)
	}
	if err := publishedTransaction.Commit(); err != nil {
		t.Fatalf("commit compatible published 000076 receipt: %v", err)
	}
	applyCanonicalReviewAuthorityHardeningMigration(t, ctx, database)
	assertCanonicalReviewContractArtifactKindsIssueExactly(t, ctx, database)
	var publishedExact bool
	if err := database.QueryRowContext(ctx, `
SELECT canonical_review_approval_receipt_is_exact($1,$2,$3)
`, publishedFixture.projectID, publishedFixture.revisionID, publishedFixture.requestID).Scan(&publishedExact); err != nil || !publishedExact {
		t.Fatalf("compatible published 000076 receipt did not survive 000077: exact=%t error=%v", publishedExact, err)
	}
	assertCanonicalReviewJSONCrossRuntimeVector(t, ctx, database)

	transaction, fixture := beginCanonicalReviewCanaryFixture(t, ctx, database, false)
	closeCanonicalReviewCanaryRequest(t, ctx, transaction, fixture)
	var receiptHash string
	var created bool
	if err := transaction.QueryRowContext(ctx, `
SELECT (issued.receipt_record).receipt_hash, issued.created
FROM issue_canonical_review_approval_receipt($1) AS issued
`, fixture.requestID).Scan(&receiptHash, &created); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("issue valid Canonical Review receipt: %v", err)
	}
	if !created || !canonicalReviewCanaryDigest(receiptHash) {
		_ = transaction.Rollback()
		t.Fatalf("valid receipt created=%t hash=%q", created, receiptHash)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit atomic Canonical Review approval: %v", err)
	}

	var replayHash string
	if err := database.QueryRowContext(ctx, `
SELECT (issued.receipt_record).receipt_hash, issued.created
FROM issue_canonical_review_approval_receipt($1) AS issued
`, fixture.requestID).Scan(&replayHash, &created); err != nil {
		t.Fatalf("replay exact Canonical Review receipt: %v", err)
	}
	if created || replayHash != receiptHash {
		t.Fatalf("receipt replay created=%t hash=%q, want false/%q", created, replayHash, receiptHash)
	}
	var exact bool
	if err := database.QueryRowContext(ctx, `
SELECT canonical_review_approval_receipt_is_exact($1,$2,$3)
`, fixture.projectID, fixture.revisionID, fixture.requestID).Scan(&exact); err != nil {
		t.Fatal(err)
	}
	if !exact {
		t.Fatal("exact receipt probe rejected the valid durable receipt")
	}
	var resolved int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM resolve_canonical_review_approval_receipt($1,$2,$3)
`, fixture.projectID, fixture.revisionID, receiptHash).Scan(&resolved); err != nil {
		t.Fatalf("resolve exact receipt: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("resolved receipt count = %d, want 1", resolved)
	}

	assertCanonicalReviewApprovalWithoutReceiptRollsBack(t, ctx, database)
	assertCanonicalReviewIssueDriftRejections(t, ctx, database)
	assertCanonicalReviewOpenDecisionIsAppendOnly(t, ctx, database)
	assertCanonicalReviewConcurrentFinalApproval(t, ctx, database)
	assertCanonicalReviewClosedSourcesImmutable(t, ctx, database, fixture)
	assertCanonicalReviewApplicationBoundary(t, ctx, database, fixture)
	assertCanonicalReviewRecomputedNullScalarRejected(t, ctx, database, fixture)
	assertCanonicalReviewRecomputedUnusedSoloOwnerRejected(t, ctx, database, fixture)
	assertCanonicalReviewReceiptTamperDetection(t, ctx, database, fixture, receiptHash)
}

func TestCanonicalReviewAuthorityHardeningRejectsIncompatiblePublishedReceiptPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "canonical_review_upgrade_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	applyCanonicalReviewAuthorityPrerequisites(t, ctx, database)
	applyCanonicalReviewAuthorityBaseMigration(t, ctx, database)
	transaction, fixture := beginCanonicalReviewCanaryFixtureWithPrecondition(
		t, ctx, database, false, `"review:forged-under-000076:open:0:0"`,
	)
	closeCanonicalReviewCanaryRequest(t, ctx, transaction, fixture)
	var receiptHash string
	var created bool
	if err := transaction.QueryRowContext(ctx, `
SELECT (issued.receipt_record).receipt_hash, issued.created
FROM issue_canonical_review_approval_receipt($1) AS issued
`, fixture.requestID).Scan(&receiptHash, &created); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("issue 000076-compatible/000077-incompatible receipt: %v", err)
	}
	if !created || !canonicalReviewCanaryDigest(receiptHash) {
		_ = transaction.Rollback()
		t.Fatalf("incompatible published receipt created=%t hash=%q", created, receiptHash)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	definitionBefore := canonicalReviewVerifierDefinition(t, ctx, database)
	hardening, err := files.ReadFile(canonicalReviewAuthorityHardeningMigration)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.ExecContext(ctx, string(hardening))
	assertCanonicalReviewCanaryError(t, err, "existing 000076 receipt is incompatible")
	definitionAfter := canonicalReviewVerifierDefinition(t, ctx, database)
	if definitionAfter != definitionBefore {
		t.Fatal("failed 000077 compatibility preflight did not atomically restore the 000076 verifier")
	}
	var helperAbsent bool
	if err := database.QueryRowContext(ctx, `
SELECT to_regprocedure('canonical_review_uuid_is_exact(text)') IS NULL
`).Scan(&helperAbsent); err != nil || !helperAbsent {
		t.Fatalf("failed 000077 preflight retained a hardening helper: absent=%t error=%v", helperAbsent, err)
	}
	var stillExact bool
	if err := database.QueryRowContext(ctx, `
SELECT canonical_review_approval_receipt_is_exact($1,$2,$3)
`, fixture.projectID, fixture.revisionID, fixture.requestID).Scan(&stillExact); err != nil || !stillExact {
		t.Fatalf("failed 000077 preflight damaged published 000076 authority: exact=%t error=%v", stillExact, err)
	}
}

func canonicalReviewVerifierDefinition(t *testing.T, ctx context.Context, database *sql.DB) string {
	t.Helper()
	var definition string
	if err := database.QueryRowContext(ctx, `
SELECT pg_catalog.pg_get_functiondef(procedure.oid)
FROM pg_catalog.pg_proc AS procedure
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
WHERE namespace.nspname = current_schema()
  AND procedure.proname = 'canonical_review_approval_receipt_record_is_exact'
`).Scan(&definition); err != nil {
		t.Fatalf("load Canonical Review verifier definition: %v", err)
	}
	return definition
}

func assertCanonicalReviewOpenDecisionIsAppendOnly(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	nullTransaction, nullFixture := beginCanonicalReviewCanaryFixture(t, ctx, database, false)
	_, nullErr := nullTransaction.ExecContext(ctx, `
INSERT INTO review_decisions (
  id,review_request_id,reviewer_id,decision,summary,created_at,solo_self_review,
  review_authority_version,reviewer_role_at_decision,governance_mode_at_decision,
  owner_count_at_decision,sole_owner_id_at_decision,solo_review_confirmed,precondition_etag
) VALUES ($1,$2,$3,'approve','invalid null authority',$4,false,1,NULL,NULL,NULL,NULL,false,$5)
`, uuid.New(), nullFixture.requestID, uuid.New(), nullFixture.closedAt.Add(time.Millisecond),
		fmt.Sprintf(`"review:%s:open:1:%d"`, nullFixture.requestID, nullFixture.closedAt.UnixNano()))
	if nullErr == nil {
		_ = nullTransaction.Rollback()
		t.Fatal("version 1 decision accepted NULL authority facts")
	}
	_ = nullTransaction.Rollback()

	transaction, fixture := beginCanonicalReviewCanaryFixture(t, ctx, database, false)
	defer transaction.Rollback()
	_, err := transaction.ExecContext(ctx, `
UPDATE review_decisions SET summary = 'changed after insert' WHERE id = $1
`, fixture.decisionID)
	assertCanonicalReviewCanaryError(t, err, "Review decisions are append-only")
}

func TestCanonicalReviewApprovalReceiptAuthorityDownFencePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "canonical_review_down_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	applyCanonicalReviewAuthorityPrerequisites(t, ctx, database)
	legacyPolicyBefore := loadCanonicalReviewLegacyPolicyCatalog(t, ctx, database)
	applyCanonicalReviewAuthorityMigration(t, ctx, database)
	hardeningDown, err := files.ReadFile("000077_canonical_review_authority_hardening.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	baseDown, err := files.ReadFile("000076_canonical_review_approval_receipt_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	liveState, _ := beginCanonicalReviewCanaryFixture(t, ctx, database, false)
	_, liveStateDownErr := liveState.ExecContext(ctx, string(hardeningDown))
	assertCanonicalReviewCanaryError(t, liveStateDownErr, "version 1 review state exists")
	if err := liveState.Rollback(); err != nil {
		t.Fatal(err)
	}

	writer, _ := beginCanonicalReviewCanaryFixture(t, ctx, database, false)
	downCtx, cancelDown := context.WithTimeout(ctx, 250*time.Millisecond)
	startedAt := time.Now()
	_, downErr := database.ExecContext(downCtx, string(hardeningDown))
	cancelDown()
	if downErr == nil || time.Since(startedAt) < 100*time.Millisecond {
		_ = writer.Rollback()
		t.Fatalf("down migration bypassed the active writer fence after %s: %v", time.Since(startedAt), downErr)
	}
	if err := writer.Rollback(); err != nil {
		t.Fatal(err)
	}
	var receiptTableBeforeDown sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT to_regclass('canonical_review_approval_receipts')::text`).Scan(&receiptTableBeforeDown); err != nil {
		t.Fatal(err)
	}
	if !receiptTableBeforeDown.Valid {
		t.Fatal("cancelled down migration removed the authority table while a writer was active")
	}

	if _, err := database.ExecContext(ctx, string(hardeningDown)); err != nil {
		t.Fatalf("empty Canonical Review hardening rollback: %v", err)
	}
	baseWriter, _ := beginCanonicalReviewCanaryFixture(t, ctx, database, false)
	baseDownCtx, cancelBaseDown := context.WithTimeout(ctx, 250*time.Millisecond)
	baseStartedAt := time.Now()
	_, baseDownErr := database.ExecContext(baseDownCtx, string(baseDown))
	cancelBaseDown()
	if baseDownErr == nil || time.Since(baseStartedAt) < 100*time.Millisecond {
		_ = baseWriter.Rollback()
		t.Fatalf("base down migration bypassed the active writer fence after %s: %v", time.Since(baseStartedAt), baseDownErr)
	}
	if err := baseWriter.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(baseDown)); err != nil {
		t.Fatalf("empty Canonical Review base rollback: %v", err)
	}
	var receiptTableAfterDown sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT to_regclass('canonical_review_approval_receipts')::text`).Scan(&receiptTableAfterDown); err != nil {
		t.Fatal(err)
	}
	if receiptTableAfterDown.Valid {
		t.Fatalf("empty authority rollback retained %q", receiptTableAfterDown.String)
	}
	legacyPolicyAfter := loadCanonicalReviewLegacyPolicyCatalog(t, ctx, database)
	legacyPolicyBeforeACL := legacyPolicyBefore.ACL
	legacyPolicyAfterACL := legacyPolicyAfter.ACL
	legacyPolicyBefore.ACL = ""
	legacyPolicyAfter.ACL = ""
	if legacyPolicyAfter != legacyPolicyBefore {
		t.Fatalf("empty authority rollback changed the legacy policy catalog\n before: %#v\n  after: %#v",
			legacyPolicyBefore, legacyPolicyAfter)
	}
	// Migration 000076 is a published immutable contract. Its down migration
	// recreates this trigger helper without the otherwise inert explicit EXECUTE
	// grant to worksflow_application. Trigger execution does not consult that
	// function ACL, so pin the sole known catalog delta while continuing to
	// require an exact semantic/security restore for every other attribute.
	expectedLegacyPolicyAfterACL := canonicalReviewACLWithoutPrincipal(
		legacyPolicyBeforeACL,
		"worksflow_application",
	)
	if legacyPolicyAfterACL != expectedLegacyPolicyAfterACL {
		t.Fatalf("empty authority rollback legacy policy ACL = %q, want the immutable 000076 baseline %q (before %q)",
			legacyPolicyAfterACL, expectedLegacyPolicyAfterACL, legacyPolicyBeforeACL)
	}

	applyCanonicalReviewAuthorityMigration(t, ctx, database)
	transaction, fixture := beginCanonicalReviewCanaryFixture(t, ctx, database, false)
	closeCanonicalReviewCanaryRequest(t, ctx, transaction, fixture)
	var receiptHash string
	var created bool
	if err := transaction.QueryRowContext(ctx, `
SELECT (issued.receipt_record).receipt_hash, issued.created
FROM issue_canonical_review_approval_receipt($1) AS issued
`, fixture.requestID).Scan(&receiptHash, &created); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if !created || !canonicalReviewCanaryDigest(receiptHash) {
		_ = transaction.Rollback()
		t.Fatalf("down-fence receipt created=%t hash=%q", created, receiptHash)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(hardeningDown)); err == nil || !strings.Contains(err.Error(), "after a receipt exists") {
		t.Fatalf("nonempty authority rollback error = %v, want durable receipt refusal", err)
	}
	var exact bool
	if err := database.QueryRowContext(ctx, `
SELECT canonical_review_approval_receipt_is_exact($1,$2,$3)
`, fixture.projectID, fixture.revisionID, fixture.requestID).Scan(&exact); err != nil || !exact {
		t.Fatalf("refused rollback did not preserve exact authority: exact=%t error=%v", exact, err)
	}
}

func canonicalReviewACLWithoutPrincipal(acl, principal string) string {
	entries := strings.Split(acl, ",")
	kept := entries[:0]
	for _, entry := range entries {
		if entry == "" || strings.HasPrefix(entry, principal+":") {
			continue
		}
		kept = append(kept, entry)
	}
	return strings.Join(kept, ",")
}

type canonicalReviewLegacyPolicyCatalog struct {
	Owner                    string
	Language                 string
	IdentityArguments        string
	Result                   string
	Volatility               string
	Parallel                 string
	Config                   string
	ACL                      string
	Source                   string
	SecurityDefiner          bool
	Strict                   bool
	TriggerType              int
	TriggerEnabled           string
	TriggerDeferrable        bool
	TriggerInitiallyDeferred bool
}

func loadCanonicalReviewLegacyPolicyCatalog(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) canonicalReviewLegacyPolicyCatalog {
	t.Helper()
	var catalog canonicalReviewLegacyPolicyCatalog
	if err := database.QueryRowContext(ctx, `
SELECT pg_catalog.pg_get_userbyid(procedure.proowner),
       language.lanname,
       pg_catalog.pg_get_function_identity_arguments(procedure.oid),
       pg_catalog.pg_get_function_result(procedure.oid),
       procedure.provolatile::text,
       procedure.proparallel::text,
       COALESCE((
         SELECT string_agg(setting, ',' ORDER BY setting)
         FROM unnest(procedure.proconfig) AS config(setting)
       ), ''),
       COALESCE((
         SELECT string_agg(
           CASE WHEN privilege.grantee = 0 THEN 'PUBLIC'
                ELSE pg_catalog.pg_get_userbyid(privilege.grantee) END
           || ':' || privilege.privilege_type || ':' || privilege.is_grantable::text,
           ',' ORDER BY privilege.grantee, privilege.privilege_type
         )
         FROM pg_catalog.aclexplode(
           COALESCE(procedure.proacl, pg_catalog.acldefault('f', procedure.proowner))
         ) AS privilege
       ), ''),
       procedure.prosrc,
       procedure.prosecdef,
       procedure.proisstrict,
       trigger.tgtype::integer,
       trigger.tgenabled::text,
       trigger.tgdeferrable,
       trigger.tginitdeferred
FROM pg_catalog.pg_proc AS procedure
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
JOIN pg_catalog.pg_language AS language ON language.oid = procedure.prolang
JOIN pg_catalog.pg_trigger AS trigger ON trigger.tgfoid = procedure.oid
WHERE namespace.nspname = current_schema()
  AND procedure.proname = 'review_request_policy_immutable'
  AND pg_catalog.pg_get_function_identity_arguments(procedure.oid) = ''
  AND trigger.tgname = 'review_request_policy_immutable'
`).Scan(
		&catalog.Owner, &catalog.Language, &catalog.IdentityArguments, &catalog.Result,
		&catalog.Volatility, &catalog.Parallel, &catalog.Config, &catalog.ACL,
		&catalog.Source, &catalog.SecurityDefiner, &catalog.Strict, &catalog.TriggerType,
		&catalog.TriggerEnabled, &catalog.TriggerDeferrable, &catalog.TriggerInitiallyDeferred,
	); err != nil {
		t.Fatalf("load legacy review policy catalog: %v", err)
	}
	return catalog
}

func applyCanonicalReviewAuthorityPrerequisites(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	last := ""
	for _, name := range names {
		if name == canonicalReviewAuthorityMigration {
			break
		}
		migration, readErr := files.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, execErr := database.ExecContext(ctx, string(migration)); execErr != nil {
			t.Fatalf("apply Canonical Review prerequisite %s: %v", name, execErr)
		}
		last = name
	}
	if !strings.HasPrefix(last, "000075_") {
		t.Fatalf("Canonical Review prerequisite head = %q, want 000075", last)
	}
}

func applyCanonicalReviewAuthorityMigration(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	applyCanonicalReviewAuthorityBaseMigration(t, ctx, database)
	applyCanonicalReviewAuthorityHardeningMigration(t, ctx, database)
}

func applyCanonicalReviewAuthorityBaseMigration(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	applyCanonicalReviewAuthorityMigrationFile(t, ctx, database, canonicalReviewAuthorityMigration)
}

func applyCanonicalReviewAuthorityHardeningMigration(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	applyCanonicalReviewAuthorityMigrationFile(t, ctx, database, canonicalReviewAuthorityHardeningMigration)
}

func applyCanonicalReviewAuthorityMigrationFile(t *testing.T, ctx context.Context, database *sql.DB, name string) {
	t.Helper()
	migration, err := files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
}

type canonicalReviewHistoryFixture struct {
	requestID  uuid.UUID
	decisionID uuid.UUID
}

func seedCanonicalReviewHistoryBeforeAuthority(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) canonicalReviewHistoryFixture {
	t.Helper()
	ownerID, reviewerID := uuid.New(), uuid.New()
	projectID, artifactID, revisionID := uuid.New(), uuid.New(), uuid.New()
	fixture := canonicalReviewHistoryFixture{requestID: uuid.New(), decisionID: uuid.New()}
	contentHash := "sha256:" + strings.Repeat("1", 64)
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO users (id,email,display_name,password_hash) VALUES
  ($1,$2,'Historical owner','not-used'),
  ($3,$4,'Historical reviewer','not-used')
`, ownerID, "history-owner-"+ownerID.String()+"@example.test",
		reviewerID, "history-reviewer-"+reviewerID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects (id,name,created_by) VALUES ($1,'Historical review',$2)`, []any{projectID, ownerID}},
		{`INSERT INTO project_members (project_id,user_id,role) VALUES ($1,$2,'owner'),($1,$3,'editor')`, []any{projectID, ownerID, reviewerID}},
		{`INSERT INTO artifacts (id,project_id,kind,artifact_key,title,created_by)
VALUES ($1,$2,'project_brief',$3,'Historical brief',$4)`, []any{artifactID, projectID, "HISTORY-" + artifactID.String(), ownerID}},
		{`INSERT INTO artifact_revisions (
  id,artifact_id,revision_number,schema_version,content_store,content_ref,content_hash,
  byte_size,workflow_status,change_source,change_summary,created_by
) VALUES ($1,$2,1,1,'blob',$3,$4,7,'in_review','human','historical review',$5)`, []any{
			revisionID, artifactID, "blob://history/" + revisionID.String(), contentHash, ownerID,
		}},
		{`INSERT INTO review_requests (
  id,project_id,artifact_id,revision_id,content_hash,status,policy,requested_by
) VALUES ($1,$2,$3,$4,$5,'open','{}'::jsonb,$6)`, []any{
			fixture.requestID, projectID, artifactID, revisionID, contentHash, ownerID,
		}},
		{`INSERT INTO review_decisions (
  id,review_request_id,reviewer_id,decision,summary,solo_self_review
) VALUES ($1,$2,$3,'approve','historical approval',false)`, []any{
			fixture.decisionID, fixture.requestID, reviewerID,
		}},
	}
	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func assertCanonicalReviewHistoryRemainsUntrusted(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture canonicalReviewHistoryFixture,
) {
	t.Helper()
	var requestVersion, decisionVersion int
	var requestCloseUnset, decisionFactsUnset bool
	if err := database.QueryRowContext(ctx, `
SELECT review_authority_version, closed_by_decision_id IS NULL
FROM review_requests WHERE id = $1
`, fixture.requestID).Scan(&requestVersion, &requestCloseUnset); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT review_authority_version,
       reviewer_role_at_decision IS NULL
       AND governance_mode_at_decision IS NULL
       AND owner_count_at_decision IS NULL
       AND sole_owner_id_at_decision IS NULL
       AND solo_review_confirmed IS NULL
       AND precondition_etag IS NULL
FROM review_decisions WHERE id = $1
`, fixture.decisionID).Scan(&decisionVersion, &decisionFactsUnset); err != nil {
		t.Fatal(err)
	}
	if requestVersion != 0 || decisionVersion != 0 || !requestCloseUnset || !decisionFactsUnset {
		t.Fatalf("history trust marker request=%d decision=%d closeUnset=%t factsUnset=%t, want 0/0/true/true",
			requestVersion, decisionVersion, requestCloseUnset, decisionFactsUnset)
	}
	var receiptCount int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM canonical_review_approval_receipts`).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if receiptCount != 0 {
		t.Fatalf("migration backfilled %d trusted Canonical Review receipts", receiptCount)
	}
	var ignored bool
	err := database.QueryRowContext(ctx, `
SELECT issued.created FROM issue_canonical_review_approval_receipt($1) AS issued
`, fixture.requestID).Scan(&ignored)
	assertCanonicalReviewCanaryError(t, err, "invalid or legacy")
}

func assertCanonicalReviewJSONCrossRuntimeVector(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	input := `{"中文":"评审","separators":"\u2028\u2029","quote":"\"\\","control":"line\n\t\u0001","amp":"<>&"}`
	expected := "{\"amp\":\"<>&\",\"control\":\"line\\n\\t\\u0001\",\"quote\":\"\\\"\\\\\",\"separators\":\"\u2028\u2029\",\"中文\":\"评审\"}"
	const expectedHash = "sha256:3d3b4445993483defd7efb5c8056202cc12b07d138688f8f460683cd47f35eb4"
	var actual, hash string
	if err := database.QueryRowContext(ctx, `
SELECT convert_from(canonical_review_jsonb_bytes($1::jsonb),'UTF8'),
       canonical_review_authority_hash(
         'worksflow.canonical-review.test-vector/v1',
         canonical_review_jsonb_bytes($1::jsonb)
       )
`, input).Scan(&actual, &hash); err != nil {
		t.Fatal(err)
	}
	if actual != expected || hash != expectedHash {
		t.Fatalf("canonical JSON vector bytes=%q hash=%q, want %q/%q", actual, hash, expected, expectedHash)
	}
	var plain, tab, nonBreaking, ideographic, arbitraryVersion, zero bool
	var timestamp, normalizedMidnight, normalizedLeapSecond, invalidCalendar bool
	if err := database.QueryRowContext(ctx, `
SELECT canonical_review_text_is_trimmed('approved'),
       canonical_review_text_is_trimmed(E'\tapproved'),
       canonical_review_text_is_trimmed(U&'\00A0approved'),
       canonical_review_text_is_trimmed(U&'approved\3000'),
       canonical_review_uuid_is_exact('11111111-1111-1111-8111-111111111111'),
       canonical_review_uuid_is_exact('00000000-0000-0000-0000-000000000000'),
       canonical_review_timestamp_is_exact('2026-07-18T23:59:59.123456Z'),
       canonical_review_timestamp_is_exact('2026-07-18T24:00:00.000000Z'),
       canonical_review_timestamp_is_exact('2026-07-18T23:59:60.000000Z'),
       canonical_review_timestamp_is_exact('2026-02-30T00:00:00.000000Z')
`).Scan(
		&plain, &tab, &nonBreaking, &ideographic, &arbitraryVersion, &zero,
		&timestamp, &normalizedMidnight, &normalizedLeapSecond, &invalidCalendar,
	); err != nil {
		t.Fatal(err)
	}
	if !plain || tab || nonBreaking || ideographic || !arbitraryVersion || zero ||
		!timestamp || normalizedMidnight || normalizedLeapSecond || invalidCalendar {
		t.Fatalf("canonical primitive parity plain/tab/nbsp/ideographic/uuid/zero/time/24h/leap/calendar = %t/%t/%t/%t/%t/%t/%t/%t/%t/%t",
			plain, tab, nonBreaking, ideographic, arbitraryVersion, zero,
			timestamp, normalizedMidnight, normalizedLeapSecond, invalidCalendar)
	}
}

type canonicalReviewCanaryFixture struct {
	ownerID    uuid.UUID
	reviewerID uuid.UUID
	projectID  uuid.UUID
	artifactID uuid.UUID
	revisionID uuid.UUID
	requestID  uuid.UUID
	decisionID uuid.UUID
	closedAt   time.Time
}

func beginCanonicalReviewCanaryFixture(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	policyReviewerIsAlternate bool,
) (*sql.Tx, canonicalReviewCanaryFixture) {
	t.Helper()
	return beginCanonicalReviewCanaryFixtureWithPrecondition(t, ctx, database, policyReviewerIsAlternate, "")
}

func beginCanonicalReviewCanaryFixtureWithPrecondition(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	policyReviewerIsAlternate bool,
	preconditionETag string,
) (*sql.Tx, canonicalReviewCanaryFixture) {
	t.Helper()
	fixture := canonicalReviewCanaryFixture{
		ownerID:    uuid.New(),
		reviewerID: uuid.New(),
		projectID:  uuid.New(),
		artifactID: uuid.New(),
		revisionID: uuid.New(),
		requestID:  uuid.New(),
		decisionID: uuid.New(),
		closedAt:   time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond),
	}
	alternateReviewerID := uuid.New()
	policyReviewerID := fixture.reviewerID
	if policyReviewerIsAlternate {
		policyReviewerID = alternateReviewerID
	}
	if preconditionETag == "" {
		preconditionETag = fmt.Sprintf(`"review:%s:open:0:0"`, fixture.requestID)
	}
	contentHash := "sha256:" + strings.Repeat("a", 64)
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	fail := func(err error) {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO users (id,email,display_name,password_hash) VALUES
  ($1,$2,'Canonical owner','not-used'),
  ($3,$4,'Canonical reviewer','not-used'),
  ($5,$6,'Canonical alternate reviewer','not-used')
`, fixture.ownerID, "canonical-owner-"+fixture.ownerID.String()+"@example.test",
		fixture.reviewerID, "canonical-reviewer-"+fixture.reviewerID.String()+"@example.test",
		alternateReviewerID, "canonical-alternate-"+alternateReviewerID.String()+"@example.test"); err != nil {
		fail(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects (id,name,governance_mode,created_by)
VALUES ($1,'Canonical review canary','team',$2)`, []any{fixture.projectID, fixture.ownerID}},
		{`INSERT INTO project_members (project_id,user_id,role) VALUES
  ($1,$2,'owner'),($1,$3,'editor'),($1,$4,'editor')`, []any{
			fixture.projectID, fixture.ownerID, fixture.reviewerID, alternateReviewerID,
		}},
		{`INSERT INTO artifacts (
  id,project_id,kind,artifact_key,title,lifecycle,version,created_by
) VALUES ($1,$2,'project_brief',$3,'Canonical review brief','active',1,$4)`, []any{
			fixture.artifactID, fixture.projectID, "CANONICAL-" + fixture.artifactID.String(), fixture.ownerID,
		}},
		{`INSERT INTO artifact_revisions (
  id,artifact_id,revision_number,schema_version,content_store,content_ref,content_hash,
  byte_size,workflow_status,change_source,change_summary,created_by,created_at,approved_at
) VALUES ($1,$2,1,1,'blob',$3,$4,42,'approved','human',$5,$6,$7,$8)`, []any{
			fixture.revisionID, fixture.artifactID,
			"blob://canonical/评审/" + fixture.revisionID.String(), contentHash,
			"Canonical 评审 <>& quote \" slash \\ newline\ncontrol-safe", fixture.ownerID,
			fixture.closedAt.Add(-2 * time.Minute), fixture.closedAt,
		}},
		{`UPDATE artifacts
SET latest_revision_id = $2, latest_approved_revision_id = $2
WHERE id = $1`, []any{fixture.artifactID, fixture.revisionID}},
		{`INSERT INTO review_requests (
  id,project_id,artifact_id,revision_id,content_hash,status,policy,requested_by,requested_at,
  review_authority_version
) VALUES (
  $1,$2,$3,$4,$5,'open',jsonb_build_object(
    'reviewerIds',jsonb_build_array($6::text),
    'minimumApprovals',1,
    'prohibitSelfReview',true,
    'governanceMode','team'
  ),$7,$8,1
)`, []any{
			fixture.requestID, fixture.projectID, fixture.artifactID, fixture.revisionID,
			contentHash, policyReviewerID, fixture.ownerID, fixture.closedAt.Add(-time.Minute),
		}},
		{`INSERT INTO review_decisions (
  id,review_request_id,reviewer_id,decision,summary,created_at,solo_self_review,
  review_authority_version,reviewer_role_at_decision,governance_mode_at_decision,
  owner_count_at_decision,sole_owner_id_at_decision,solo_review_confirmed,precondition_etag
) VALUES (
  $1,$2,$3,'approve',$4,$5,false,
  1,'editor','team',1,$6,false,$7
)`, []any{
			fixture.decisionID, fixture.requestID, fixture.reviewerID,
			"Approved 评审 <>& quote \" slash \\ newline\ncontrol-safe", fixture.closedAt,
			fixture.ownerID, preconditionETag,
		}},
	}
	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement.query, statement.args...); err != nil {
			fail(err)
		}
	}
	return transaction, fixture
}

func closeCanonicalReviewCanaryRequest(
	t *testing.T,
	ctx context.Context,
	transaction *sql.Tx,
	fixture canonicalReviewCanaryFixture,
) {
	t.Helper()
	if _, err := transaction.ExecContext(ctx, `
UPDATE review_requests
SET status = 'approved', closed_at = $2, closed_by_decision_id = $3
WHERE id = $1
`, fixture.requestID, fixture.closedAt, fixture.decisionID); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
}

func assertCanonicalReviewContractArtifactKindsIssueExactly(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) {
	t.Helper()
	for _, artifactKind := range []string{"ai_runtime_contract", "deployment_contract", "verification_contract"} {
		transaction, fixture := beginCanonicalReviewCanaryFixture(t, ctx, database, false)
		if _, err := transaction.ExecContext(ctx, `UPDATE artifacts SET kind = $2 WHERE id = $1`, fixture.artifactID, artifactKind); err != nil {
			_ = transaction.Rollback()
			t.Fatalf("set Canonical Review artifact kind %q: %v", artifactKind, err)
		}
		closeCanonicalReviewCanaryRequest(t, ctx, transaction, fixture)
		var receiptHash string
		var created bool
		if err := transaction.QueryRowContext(ctx, `
SELECT (issued.receipt_record).receipt_hash, issued.created
FROM issue_canonical_review_approval_receipt($1) AS issued
`, fixture.requestID).Scan(&receiptHash, &created); err != nil {
			_ = transaction.Rollback()
			t.Fatalf("issue %q Canonical Review receipt: %v", artifactKind, err)
		}
		if !created || !canonicalReviewCanaryDigest(receiptHash) {
			_ = transaction.Rollback()
			t.Fatalf("%q Canonical Review receipt created=%t hash=%q", artifactKind, created, receiptHash)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatalf("commit %q Canonical Review receipt: %v", artifactKind, err)
		}
		var exact bool
		if err := database.QueryRowContext(ctx, `
SELECT canonical_review_approval_receipt_is_exact($1,$2,$3)
`, fixture.projectID, fixture.revisionID, fixture.requestID).Scan(&exact); err != nil || !exact {
			t.Fatalf("%q Canonical Review receipt exact=%t: %v", artifactKind, exact, err)
		}
	}
}

func assertCanonicalReviewApprovalWithoutReceiptRollsBack(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) {
	t.Helper()
	transaction, fixture := beginCanonicalReviewCanaryFixture(t, ctx, database, false)
	closeCanonicalReviewCanaryRequest(t, ctx, transaction, fixture)
	err := transaction.Commit()
	assertCanonicalReviewCanaryError(t, err, "requires its exact atomic Canonical Review receipt")
	var requests int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM review_requests WHERE id = $1`, fixture.requestID).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatal("receipt-less version 1 approval left durable request state after failed commit")
	}
}

func assertCanonicalReviewIssueDriftRejections(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	tests := []struct {
		name              string
		alternatePolicyID bool
		preconditionETag  string
		mutate            func(*sql.Tx, canonicalReviewCanaryFixture) error
		want              string
	}{
		{
			name: "governance",
			mutate: func(transaction *sql.Tx, fixture canonicalReviewCanaryFixture) error {
				_, err := transaction.ExecContext(ctx, `UPDATE projects SET governance_mode = 'solo' WHERE id = $1`, fixture.projectID)
				return err
			},
			want: "threshold is invalid",
		},
		{
			name: "reviewer role",
			mutate: func(transaction *sql.Tx, fixture canonicalReviewCanaryFixture) error {
				_, err := transaction.ExecContext(ctx, `
UPDATE project_members SET role = 'viewer' WHERE project_id = $1 AND user_id = $2
`, fixture.projectID, fixture.reviewerID)
				return err
			},
			want: "reviewer authority drifted",
		},
		{
			name:              "policy reviewer set",
			alternatePolicyID: true,
			mutate:            func(*sql.Tx, canonicalReviewCanaryFixture) error { return nil },
			want:              "decision set is invalid or incomplete",
		},
		{
			name:             "forged precondition ETag",
			preconditionETag: `"review:forged:open:0:0"`,
			mutate:           func(*sql.Tx, canonicalReviewCanaryFixture) error { return nil },
			want:             "decision set is invalid or incomplete",
		},
		{
			name: "superseded revision",
			mutate: func(transaction *sql.Tx, fixture canonicalReviewCanaryFixture) error {
				_, err := transaction.ExecContext(ctx, `
UPDATE artifact_revisions SET superseded_at = $2 WHERE id = $1
`, fixture.revisionID, fixture.closedAt)
				return err
			},
			want: "request, revision, or artifact closure is invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transaction, fixture := beginCanonicalReviewCanaryFixtureWithPrecondition(
				t, ctx, database, test.alternatePolicyID, test.preconditionETag,
			)
			defer transaction.Rollback()
			if err := test.mutate(transaction, fixture); err != nil {
				t.Fatal(err)
			}
			closeCanonicalReviewCanaryRequest(t, ctx, transaction, fixture)
			var ignored bool
			err := transaction.QueryRowContext(ctx, `
SELECT issued.created FROM issue_canonical_review_approval_receipt($1) AS issued
`, fixture.requestID).Scan(&ignored)
			assertCanonicalReviewCanaryError(t, err, test.want)
		})
	}
}

func assertCanonicalReviewConcurrentFinalApproval(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	setup, fixture := beginCanonicalReviewCanaryFixture(t, ctx, database, false)
	if err := setup.Commit(); err != nil {
		t.Fatalf("commit concurrent approval setup: %v", err)
	}

	type result struct {
		hash    string
		created bool
		err     error
	}
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	results := make(chan result, 2)
	for attempt := 0; attempt < 2; attempt++ {
		go func() {
			transaction, err := database.BeginTx(ctx, nil)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer transaction.Rollback()
			ready <- struct{}{}
			<-start
			if _, err := transaction.ExecContext(ctx, `
UPDATE review_requests
SET status = 'approved', closed_at = $2, closed_by_decision_id = $3
WHERE id = $1
`, fixture.requestID, fixture.closedAt, fixture.decisionID); err != nil {
				results <- result{err: err}
				return
			}
			var outcome result
			if err := transaction.QueryRowContext(ctx, `
SELECT (issued.receipt_record).receipt_hash, issued.created
FROM issue_canonical_review_approval_receipt($1) AS issued
`, fixture.requestID).Scan(&outcome.hash, &outcome.created); err != nil {
				outcome.err = err
				results <- outcome
				return
			}
			outcome.err = transaction.Commit()
			results <- outcome
		}()
	}
	<-ready
	<-ready
	close(start)

	successes := 0
	loserErrors := 0
	winnerHash := ""
	for attempt := 0; attempt < 2; attempt++ {
		outcome := <-results
		if outcome.err != nil {
			if strings.Contains(outcome.err.Error(), "Closed review requests are immutable") {
				loserErrors++
				continue
			}
			t.Fatalf("concurrent final approval returned unexpected error: %v", outcome.err)
		}
		if !outcome.created || !canonicalReviewCanaryDigest(outcome.hash) {
			t.Fatalf("concurrent winner created=%t hash=%q", outcome.created, outcome.hash)
		}
		successes++
		winnerHash = outcome.hash
	}
	if successes != 1 || loserErrors != 1 {
		t.Fatalf("concurrent final approval successes=%d loserErrors=%d, want 1/1", successes, loserErrors)
	}
	var receiptCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM canonical_review_approval_receipts WHERE review_request_id = $1
`, fixture.requestID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if receiptCount != 1 {
		t.Fatalf("concurrent final approval receipt count = %d, want 1", receiptCount)
	}
	var replayHash string
	var replayCreated bool
	if err := database.QueryRowContext(ctx, `
SELECT (issued.receipt_record).receipt_hash, issued.created
FROM issue_canonical_review_approval_receipt($1) AS issued
`, fixture.requestID).Scan(&replayHash, &replayCreated); err != nil {
		t.Fatal(err)
	}
	if replayCreated || replayHash != winnerHash {
		t.Fatalf("concurrent approval replay created=%t hash=%q, want false/%q", replayCreated, replayHash, winnerHash)
	}
}

func assertCanonicalReviewClosedSourcesImmutable(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture canonicalReviewCanaryFixture,
) {
	t.Helper()
	tests := []struct {
		name  string
		query string
		args  []any
		want  string
	}{
		{
			name:  "request update",
			query: `UPDATE review_requests SET status = status WHERE id = $1`,
			args:  []any{fixture.requestID},
			want:  "Closed review requests are immutable",
		},
		{
			name:  "request delete",
			query: `DELETE FROM review_requests WHERE id = $1`,
			args:  []any{fixture.requestID},
			want:  "Review requests cannot be deleted",
		},
		{
			name:  "decision update",
			query: `UPDATE review_decisions SET summary = summary WHERE id = $1`,
			args:  []any{fixture.decisionID},
			want:  "Review decisions are append-only",
		},
		{
			name:  "decision delete",
			query: `DELETE FROM review_decisions WHERE id = $1`,
			args:  []any{fixture.decisionID},
			want:  "Review decisions cannot be deleted",
		},
		{
			name:  "receipt truncate",
			query: `TRUNCATE canonical_review_approval_receipts`,
			want:  "Canonical Review approval receipts are immutable",
		},
	}
	for _, test := range tests {
		t.Run("closed "+test.name, func(t *testing.T) {
			_, err := database.ExecContext(ctx, test.query, test.args...)
			assertCanonicalReviewCanaryError(t, err, test.want)
		})
	}
}

func assertCanonicalReviewApplicationBoundary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture canonicalReviewCanaryFixture,
) {
	t.Helper()
	var publicRead, publicWrite, publicIssue, publicProbe bool
	if err := database.QueryRowContext(ctx, `
SELECT has_table_privilege('public','canonical_review_approval_receipts','SELECT'),
       has_table_privilege('public','canonical_review_approval_receipts','INSERT,UPDATE,DELETE,TRUNCATE'),
       has_function_privilege('public','issue_canonical_review_approval_receipt(uuid)','EXECUTE'),
       has_function_privilege('public','canonical_review_approval_receipt_is_exact(uuid,uuid,uuid)','EXECUTE')
`).Scan(&publicRead, &publicWrite, &publicIssue, &publicProbe); err != nil {
		t.Fatal(err)
	}
	if publicRead || publicWrite || publicIssue || publicProbe {
		t.Fatalf("PUBLIC Canonical Review ACL read=%t write=%t issue=%t probe=%t, want all false",
			publicRead, publicWrite, publicIssue, publicProbe)
	}

	var applicationExists bool
	if err := database.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'worksflow_application')
`).Scan(&applicationExists); err != nil {
		t.Fatal(err)
	}
	if !applicationExists {
		t.Log("worksflow_application is absent; PUBLIC revocation and static application ACL contract were verified")
		return
	}
	var appRead, appWrite, appIssue, appProbe, appResolve bool
	if err := database.QueryRowContext(ctx, `
SELECT has_table_privilege('worksflow_application','canonical_review_approval_receipts','SELECT'),
       has_table_privilege('worksflow_application','canonical_review_approval_receipts','INSERT,UPDATE,DELETE,TRUNCATE'),
       has_function_privilege('worksflow_application','issue_canonical_review_approval_receipt(uuid)','EXECUTE'),
       has_function_privilege('worksflow_application','canonical_review_approval_receipt_is_exact(uuid,uuid,uuid)','EXECUTE'),
       has_function_privilege('worksflow_application','resolve_canonical_review_approval_receipt(uuid,uuid,text)','EXECUTE')
`).Scan(&appRead, &appWrite, &appIssue, &appProbe, &appResolve); err != nil {
		t.Fatal(err)
	}
	if appRead || appWrite || !appIssue || !appProbe || appResolve {
		t.Fatalf("application Canonical Review ACL read=%t write=%t issue=%t probe=%t resolve=%t",
			appRead, appWrite, appIssue, appProbe, appResolve)
	}
	var maySetRole bool
	if err := database.QueryRowContext(ctx, `
SELECT pg_has_role(current_user,'worksflow_application','MEMBER')
`).Scan(&maySetRole); err != nil {
		t.Fatal(err)
	}
	if !maySetRole {
		t.Log("current canary principal cannot SET ROLE worksflow_application; catalog ACLs were verified")
		return
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `SET ROLE worksflow_application`); err != nil {
		t.Fatal(err)
	}
	defer connection.ExecContext(context.Background(), `RESET ROLE`)
	var ignored int
	if err := connection.QueryRowContext(ctx, `SELECT count(*) FROM canonical_review_approval_receipts`).Scan(&ignored); err == nil {
		t.Fatal("application selected the owner-only Canonical Review receipt table")
	}
	if _, err := connection.ExecContext(ctx, `UPDATE canonical_review_approval_receipts SET approval_count = approval_count`); err == nil {
		t.Fatal("application mutated the owner-only Canonical Review receipt table")
	}
	var exact bool
	if err := connection.QueryRowContext(ctx, `
SELECT canonical_review_approval_receipt_is_exact($1,$2,$3)
`, fixture.projectID, fixture.revisionID, fixture.requestID).Scan(&exact); err != nil || !exact {
		t.Fatalf("application exact probe exact=%t error=%v", exact, err)
	}
}

func assertCanonicalReviewReceiptTamperDetection(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture canonicalReviewCanaryFixture,
	receiptHash string,
) {
	t.Helper()
	if _, err := database.ExecContext(ctx, `
UPDATE canonical_review_approval_receipts SET approval_count = approval_count
WHERE review_request_id = $1
`, fixture.requestID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("direct receipt mutation error = %v, want immutable rejection", err)
	}

	type tamperCase struct {
		name              string
		query             string
		args              []any
		disableAll        bool
		resolveRevisionID uuid.UUID
		resolveHash       string
	}
	tamperedReceiptHash := "sha256:" + strings.Repeat("c", 64)
	tamperedRevisionID := uuid.New()
	tests := []tamperCase{
		{
			name:  "root bytes",
			query: `UPDATE canonical_review_approval_receipts SET receipt_bytes = receipt_bytes || decode('20','hex') WHERE review_request_id = $1`,
			args:  []any{fixture.requestID},
		},
		{
			name:        "root hash",
			query:       `UPDATE canonical_review_approval_receipts SET receipt_hash = $2 WHERE review_request_id = $1`,
			args:        []any{fixture.requestID, tamperedReceiptHash},
			resolveHash: tamperedReceiptHash,
		},
		{
			name: "root document",
			query: `UPDATE canonical_review_approval_receipts
SET receipt_document = jsonb_set(receipt_document,'{schemaVersion}','"tampered"'::jsonb,false)
WHERE review_request_id = $1`,
			args: []any{fixture.requestID},
		},
	}
	for _, component := range []string{
		"review_request_snapshot",
		"revision_snapshot",
		"policy_snapshot",
		"decisions_snapshot",
		"governance_snapshot",
		"approval_snapshot",
	} {
		tests = append(tests,
			tamperCase{
				name:  component + " bytes",
				query: fmt.Sprintf(`UPDATE canonical_review_approval_receipts SET %s_bytes = %s_bytes || decode('20','hex') WHERE review_request_id = $1`, component, component),
				args:  []any{fixture.requestID},
			},
			tamperCase{
				name:  component + " hash",
				query: fmt.Sprintf(`UPDATE canonical_review_approval_receipts SET %s_hash = $2 WHERE review_request_id = $1`, component),
				args:  []any{fixture.requestID, "sha256:" + strings.Repeat("b", 64)},
			},
			tamperCase{
				name: component + " document",
				query: fmt.Sprintf(`UPDATE canonical_review_approval_receipts
SET %s_document = jsonb_set(%s_document,'{schemaVersion}','"tampered"'::jsonb,false)
WHERE review_request_id = $1`, component, component),
				args: []any{fixture.requestID},
			},
		)
	}
	tests = append(tests,
		tamperCase{
			name:       "review_request_id scalar",
			query:      `UPDATE canonical_review_approval_receipts SET review_request_id = $2 WHERE review_request_id = $1`,
			args:       []any{fixture.requestID, uuid.New()},
			disableAll: true,
		},
		tamperCase{
			name:       "artifact_id scalar",
			query:      `UPDATE canonical_review_approval_receipts SET artifact_id = $2 WHERE review_request_id = $1`,
			args:       []any{fixture.requestID, uuid.New()},
			disableAll: true,
		},
		tamperCase{
			name:              "revision_id scalar",
			query:             `UPDATE canonical_review_approval_receipts SET revision_id = $2 WHERE review_request_id = $1`,
			args:              []any{fixture.requestID, tamperedRevisionID},
			disableAll:        true,
			resolveRevisionID: tamperedRevisionID,
		},
		tamperCase{
			name:  "revision_content_hash scalar",
			query: `UPDATE canonical_review_approval_receipts SET revision_content_hash = $2 WHERE review_request_id = $1`,
			args:  []any{fixture.requestID, "sha256:" + strings.Repeat("d", 64)},
		},
		tamperCase{
			name: "approval_count scalar",
			query: `UPDATE canonical_review_approval_receipts
SET approval_count = 2, minimum_approvals = 2
WHERE review_request_id = $1`,
			args: []any{fixture.requestID},
		},
		tamperCase{
			name:  "governance_mode scalar",
			query: `UPDATE canonical_review_approval_receipts SET governance_mode = 'solo' WHERE review_request_id = $1`,
			args:  []any{fixture.requestID},
		},
		tamperCase{
			name:  "owner_count scalar",
			query: `UPDATE canonical_review_approval_receipts SET owner_count = 2, sole_owner_id = NULL WHERE review_request_id = $1`,
			args:  []any{fixture.requestID},
		},
		tamperCase{
			name:  "sole_owner_id scalar",
			query: `UPDATE canonical_review_approval_receipts SET sole_owner_id = $2 WHERE review_request_id = $1`,
			args:  []any{fixture.requestID, fixture.reviewerID},
		},
		tamperCase{
			name:  "issued_at scalar",
			query: `UPDATE canonical_review_approval_receipts SET issued_at = issued_at + interval '1 millisecond' WHERE review_request_id = $1`,
			args:  []any{fixture.requestID},
		},
		tamperCase{
			name:       "closed_by_decision scalar",
			query:      `UPDATE canonical_review_approval_receipts SET closed_by_decision_id = $2 WHERE review_request_id = $1`,
			args:       []any{fixture.requestID, uuid.New()},
			disableAll: true,
		},
	)
	for _, test := range tests {
		t.Run("tamper "+test.name, func(t *testing.T) {
			transaction, err := database.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer transaction.Rollback()
			disable := `ALTER TABLE canonical_review_approval_receipts DISABLE TRIGGER canonical_review_approval_receipts_immutable`
			if test.disableAll {
				disable = `ALTER TABLE canonical_review_approval_receipts DISABLE TRIGGER ALL`
			}
			if _, err := transaction.ExecContext(ctx, disable); err != nil {
				t.Fatalf("disable receipt triggers for owner-only tamper canary: %v", err)
			}
			if _, err := transaction.ExecContext(ctx, test.query, test.args...); err != nil {
				t.Fatal(err)
			}
			var exact bool
			if err := transaction.QueryRowContext(ctx, `
SELECT canonical_review_approval_receipt_is_exact($1,$2,$3)
`, fixture.projectID, fixture.revisionID, fixture.requestID).Scan(&exact); err != nil {
				t.Fatal(err)
			}
			if exact {
				t.Fatal("exact receipt probe accepted owner-tampered durable receipt")
			}
			resolveRevisionID := test.resolveRevisionID
			if resolveRevisionID == uuid.Nil {
				resolveRevisionID = fixture.revisionID
			}
			resolveHash := test.resolveHash
			if resolveHash == "" {
				resolveHash = receiptHash
			}
			var ignored int
			err = transaction.QueryRowContext(ctx, `
SELECT count(*) FROM resolve_canonical_review_approval_receipt($1,$2,$3)
`, fixture.projectID, resolveRevisionID, resolveHash).Scan(&ignored)
			assertCanonicalReviewCanaryError(t, err, "durable receipt is corrupt")
		})
	}
}

func assertCanonicalReviewRecomputedUnusedSoloOwnerRejected(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture canonicalReviewCanaryFixture,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
ALTER TABLE canonical_review_approval_receipts
DISABLE TRIGGER canonical_review_approval_receipts_immutable
`); err != nil {
		t.Fatalf("disable receipt mutation guard for recomputed Solo Owner vector: %v", err)
	}
	newAuthorID := uuid.New()
	if _, err := transaction.ExecContext(ctx, `
WITH original AS (
  SELECT * FROM canonical_review_approval_receipts WHERE review_request_id = $1
), documents AS (
  SELECT
    original.*,
    jsonb_set(original.revision_snapshot_document, '{createdBy}', to_jsonb($2::text), false) AS new_revision,
    jsonb_set(original.approval_snapshot_document, '{subjectAuthorId}', to_jsonb($2::text), false) AS new_approval,
    jsonb_set(
      jsonb_set(
        jsonb_set(original.policy_snapshot_document, '{value,governanceMode}', to_jsonb('solo'::text), false),
        '{value,soloSelfReviewOwnerId}', to_jsonb($3::text), true
      ),
      '{value,reviewerIds}', jsonb_build_array($3::text, $4::text), false
    ) AS new_policy,
    jsonb_set(original.governance_snapshot_document, '{mode}', to_jsonb('solo'::text), false) AS new_governance,
    jsonb_set(
      original.decisions_snapshot_document,
      '{decisions,0,authorityFacts,governanceMode}', to_jsonb('solo'::text), false
    ) AS new_decisions
  FROM original
), material AS (
  SELECT
    documents.*,
    canonical_review_jsonb_bytes(new_revision) AS new_revision_bytes,
    canonical_review_jsonb_bytes(new_approval) AS new_approval_bytes,
    canonical_review_jsonb_bytes(new_policy) AS new_policy_bytes,
    canonical_review_jsonb_bytes(new_governance) AS new_governance_bytes,
    canonical_review_jsonb_bytes(new_decisions) AS new_decisions_bytes
  FROM documents
), component_hashes AS (
  SELECT
    material.*,
    canonical_review_authority_hash('worksflow.canonical-review.revision/v1', new_revision_bytes) AS new_revision_hash,
    canonical_review_authority_hash('worksflow.canonical-review.approval/v1', new_approval_bytes) AS new_approval_hash,
    canonical_review_authority_hash('worksflow.canonical-review.policy/v1', new_policy_bytes) AS new_policy_hash,
    canonical_review_authority_hash('worksflow.canonical-review.governance/v1', new_governance_bytes) AS new_governance_hash,
    canonical_review_authority_hash('worksflow.canonical-review.decisions/v1', new_decisions_bytes) AS new_decisions_hash
  FROM material
), root_documents AS (
  SELECT
    component_hashes.*,
    jsonb_set(
      jsonb_set(
        jsonb_set(
          jsonb_set(
            jsonb_set(
              jsonb_set(receipt_document, '{revision}', new_revision, false),
              '{approval}', new_approval, false
            ),
            '{policy}', new_policy, false
          ),
          '{governance}', new_governance, false
        ),
        '{decisions}', new_decisions, false
      ),
      '{componentDigests}', jsonb_build_object(
        'approval', new_approval_hash,
        'decisions', new_decisions_hash,
        'governance', new_governance_hash,
        'policy', new_policy_hash,
        'reviewRequest', review_request_snapshot_hash,
        'revision', new_revision_hash
      ), false
    ) AS new_root
  FROM component_hashes
), root_material AS (
  SELECT root_documents.*, canonical_review_jsonb_bytes(new_root) AS new_root_bytes
  FROM root_documents
)
UPDATE canonical_review_approval_receipts AS receipt
SET revision_snapshot_document = root_material.new_revision,
    revision_snapshot_bytes = root_material.new_revision_bytes,
    revision_snapshot_hash = root_material.new_revision_hash,
    approval_snapshot_document = root_material.new_approval,
    approval_snapshot_bytes = root_material.new_approval_bytes,
    approval_snapshot_hash = root_material.new_approval_hash,
    policy_snapshot_document = root_material.new_policy,
    policy_snapshot_bytes = root_material.new_policy_bytes,
    policy_snapshot_hash = root_material.new_policy_hash,
    governance_snapshot_document = root_material.new_governance,
    governance_snapshot_bytes = root_material.new_governance_bytes,
    governance_snapshot_hash = root_material.new_governance_hash,
    decisions_snapshot_document = root_material.new_decisions,
    decisions_snapshot_bytes = root_material.new_decisions_bytes,
    decisions_snapshot_hash = root_material.new_decisions_hash,
    receipt_document = root_material.new_root,
    receipt_bytes = root_material.new_root_bytes,
    receipt_hash = canonical_review_authority_hash(
      'worksflow.canonical-review.receipt/v1', root_material.new_root_bytes
    ),
    governance_mode = 'solo'
FROM root_material
WHERE receipt.review_request_id = root_material.review_request_id
`, fixture.requestID, newAuthorID, fixture.ownerID, fixture.reviewerID); err != nil {
		t.Fatalf("recompute unused Solo Owner receipt vector: %v", err)
	}
	var structurallyRecomputed, exact bool
	if err := transaction.QueryRowContext(ctx, `
SELECT
  receipt_bytes = canonical_review_jsonb_bytes(receipt_document)
  AND receipt_hash = canonical_review_authority_hash('worksflow.canonical-review.receipt/v1', receipt_bytes)
  AND revision_snapshot_bytes = canonical_review_jsonb_bytes(revision_snapshot_document)
  AND revision_snapshot_hash = canonical_review_authority_hash('worksflow.canonical-review.revision/v1', revision_snapshot_bytes)
  AND approval_snapshot_bytes = canonical_review_jsonb_bytes(approval_snapshot_document)
  AND approval_snapshot_hash = canonical_review_authority_hash('worksflow.canonical-review.approval/v1', approval_snapshot_bytes)
  AND policy_snapshot_bytes = canonical_review_jsonb_bytes(policy_snapshot_document)
  AND policy_snapshot_hash = canonical_review_authority_hash('worksflow.canonical-review.policy/v1', policy_snapshot_bytes)
  AND governance_snapshot_bytes = canonical_review_jsonb_bytes(governance_snapshot_document)
  AND governance_snapshot_hash = canonical_review_authority_hash('worksflow.canonical-review.governance/v1', governance_snapshot_bytes)
  AND decisions_snapshot_bytes = canonical_review_jsonb_bytes(decisions_snapshot_document)
  AND decisions_snapshot_hash = canonical_review_authority_hash('worksflow.canonical-review.decisions/v1', decisions_snapshot_bytes),
  canonical_review_approval_receipt_record_is_exact(receipt)
FROM canonical_review_approval_receipts AS receipt
WHERE review_request_id = $1
`, fixture.requestID).Scan(&structurallyRecomputed, &exact); err != nil {
		t.Fatal(err)
	}
	if !structurallyRecomputed || exact {
		t.Fatalf("recomputed unused Solo Owner vector structural=%t exact=%t, want true/false", structurallyRecomputed, exact)
	}
}

func assertCanonicalReviewRecomputedNullScalarRejected(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture canonicalReviewCanaryFixture,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
ALTER TABLE canonical_review_approval_receipts
DISABLE TRIGGER canonical_review_approval_receipts_immutable
`); err != nil {
		t.Fatalf("disable receipt mutation guard for recomputed NULL vector: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
WITH changed AS (
  SELECT review_request_id,
         jsonb_set(receipt_document, '{schemaVersion}', 'null'::jsonb, false) AS document
  FROM canonical_review_approval_receipts
  WHERE review_request_id = $1
), material AS (
  SELECT review_request_id, document, canonical_review_jsonb_bytes(document) AS bytes
  FROM changed
)
UPDATE canonical_review_approval_receipts AS receipt
SET receipt_document = material.document,
    receipt_bytes = material.bytes,
    receipt_hash = canonical_review_authority_hash(
      'worksflow.canonical-review.receipt/v1', material.bytes
    )
FROM material
WHERE receipt.review_request_id = material.review_request_id
`, fixture.requestID); err != nil {
		t.Fatalf("recompute NULL scalar receipt vector: %v", err)
	}
	var structurallyRecomputed, exact bool
	if err := transaction.QueryRowContext(ctx, `
SELECT
  receipt_bytes = canonical_review_jsonb_bytes(receipt_document)
  AND receipt_hash = canonical_review_authority_hash('worksflow.canonical-review.receipt/v1', receipt_bytes),
  canonical_review_approval_receipt_record_is_exact(receipt)
FROM canonical_review_approval_receipts AS receipt
WHERE review_request_id = $1
`, fixture.requestID).Scan(&structurallyRecomputed, &exact); err != nil {
		t.Fatal(err)
	}
	if !structurallyRecomputed || exact {
		t.Fatalf("recomputed NULL scalar vector structural=%t exact=%t, want true/false", structurallyRecomputed, exact)
	}
}

func assertCanonicalReviewCanaryError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Canonical Review SQL error = %v, want substring %q", err, want)
	}
}

func canonicalReviewCanaryDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
