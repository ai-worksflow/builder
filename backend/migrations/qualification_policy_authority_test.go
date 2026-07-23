package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/internal/qualificationevidence"
	qualificationpolicy "github.com/worksflow/builder/backend/internal/qualificationpolicyauthority"
	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
)

const qualificationPolicyIssueSQL = `
SELECT authority_id, authority_hash, generation, status,
       creation_transaction_id = txid_current() AS inserted_here
FROM issue_qualification_policy_authority_v1(
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
  $13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24
)`

func TestQualificationPolicyAuthorityDeclaresClosedPersistenceBoundary(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(workflowInputAuthorityMigration)
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000078_workflow_input_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{
		"CREATE TABLE qualification_policy_authorities",
		"CREATE TABLE qualification_policy_review_defaults",
		"CREATE TABLE qualification_policy_exact_approved_sources",
		"CREATE TABLE qualification_policy_identity_reservations",
		"worksflow-qualification-policy-authority-hash/v1",
		"worksflow.qualification-policy.revision/v1",
		"worksflow.qualification-policy.plan-input-profile/v1",
		"worksflow.qualification-policy.promotion/v1",
		"worksflow.qualification-policy.authority/v1",
		"'policySourceId', v_authority.policy_source_id",
		"creation_transaction_id bigint NOT NULL DEFAULT txid_current()",
		"qualification_policy_authority_scope_generation_unique",
		"CREATE FUNCTION issue_qualification_policy_authority_v1(",
		"CREATE FUNCTION inspect_qualification_policy_operation_v1",
		"CREATE FUNCTION resolve_qualification_policy_authority_v1",
		"CREATE FUNCTION resolve_current_qualification_policy_authority_v1",
		"CREATE FUNCTION assert_current_qualification_policy_authority_v1",
		"CREATE FUNCTION qualification_policy_authority_record_is_exact_v1",
		"CREATE CONSTRAINT TRIGGER qualification_policy_authorities_exact_closure",
		"DEFERRABLE INITIALLY DEFERRED",
		"FROM projects WHERE id = p_project_id FOR UPDATE",
		"FROM artifacts",
		"FROM artifact_revisions",
		"v_artifact.kind <> v_source->>'sourceKind'",
		"v_revision.workflow_status <> 'approved'",
		"ON CONFLICT (identity_value) DO NOTHING",
		"Qualification Policy Authority records are immutable",
		"worksflow_qualification_policy_operator",
		"GRANT EXECUTE ON FUNCTION %I.issue_qualification_policy_authority_v1",
		"GRANT EXECUTE ON FUNCTION %I.resolve_qualification_policy_authority_v1(uuid)",
		"GRANT EXECUTE ON FUNCTION %I.resolve_current_qualification_policy_authority_v1(uuid,text,text)",
		"GRANT EXECUTE ON FUNCTION %I.assert_current_qualification_policy_authority_v1(uuid)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Qualification Policy Authority migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"GRANT SELECT ON TABLE %I.qualification_policy_authorities TO worksflow_application",
		"GRANT EXECUTE ON FUNCTION %I.issue_qualification_policy_authority_v1(%s) TO worksflow_application",
		"CREATE TABLE qualification_policy_authority_heads",
		"ON DELETE CASCADE",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Qualification Policy Authority migration unexpectedly contains %q", forbidden)
		}
	}
	for _, required := range []string{
		"LOCK TABLE qualification_policy_authorities IN ACCESS EXCLUSIVE MODE",
		"EXISTS (SELECT 1 FROM qualification_policy_authorities)",
		"DROP FUNCTION issue_qualification_policy_authority_v1(",
		"DROP TABLE qualification_policy_identity_reservations",
		"DROP TABLE qualification_policy_exact_approved_sources",
		"DROP TABLE qualification_policy_review_defaults",
		"DROP TABLE qualification_policy_authorities",
	} {
		if !strings.Contains(string(down), required) {
			t.Fatalf("Qualification Policy Authority rollback is missing %q", required)
		}
	}
}

type qualificationPolicyMigrationSource struct {
	value qualificationpolicy.ResolvedPolicy
}

func (source *qualificationPolicyMigrationSource) Resolve(_ context.Context, sourceID string) (qualificationpolicy.ResolvedPolicy, error) {
	if source == nil || sourceID != "reviewed-policy-release-2026-07-19" {
		return qualificationpolicy.ResolvedPolicy{}, qualificationpolicy.ErrNotFound
	}
	return source.value, nil
}

type qualificationPolicyFixtureCompiler struct {
	source   *qualificationPolicyMigrationSource
	store    *qualificationpolicy.MemoryStore
	service  *qualificationpolicy.Service
	clockNow time.Time
}

func newQualificationPolicyFixtureCompiler(
	t *testing.T,
	projectID uuid.UUID,
	profileHash string,
	exactSources []qualificationpolicy.ExactApprovedSource,
) *qualificationPolicyFixtureCompiler {
	t.Helper()
	compiler := &qualificationPolicyFixtureCompiler{
		source: &qualificationPolicyMigrationSource{
			value: validQualificationPolicy(projectID, profileHash, exactSources),
		},
		store:    qualificationpolicy.NewMemoryStore(),
		clockNow: time.Date(2026, 7, 19, 20, 0, 0, 123_000_000, time.UTC),
	}
	service, err := qualificationpolicy.NewService(
		compiler.source,
		compiler.store,
		qualificationpolicy.DatabaseClockFunc(func(context.Context) (time.Time, error) {
			return compiler.clockNow, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	compiler.service = service
	return compiler
}

func (compiler *qualificationPolicyFixtureCompiler) compile(
	t *testing.T,
	command qualificationpolicy.IssueCommand,
	status string,
	issuedAt time.Time,
) qualificationpolicy.Record {
	t.Helper()
	compiler.source.value.Status = status
	compiler.clockNow = issuedAt
	record, err := compiler.service.Issue(context.Background(), command)
	if err != nil {
		t.Fatalf("compile Qualification Policy Authority fixture: %v", err)
	}
	return record
}

type qualificationPolicyCanaryFixture struct {
	Record         qualificationpolicy.Record
	AuthorityID    uuid.UUID
	AuthorityHash  string
	RevisionPolicy qualificationpolicy.RevisionPolicy
}

// seedCurrentQualificationPolicy is intentionally package-local and reusable
// by the Workflow Input canary. It authors one current active v1 generation
// from the same strict Go encoder used by production adapters.
func seedCurrentQualificationPolicy(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	projectID uuid.UUID,
	profileHash string,
) qualificationPolicyCanaryFixture {
	t.Helper()
	compiler := newQualificationPolicyFixtureCompiler(t, projectID, profileHash, nil)
	command := qualificationpolicy.IssueCommand{
		OperationID:    uuid.New(),
		AuthorityID:    uuid.New(),
		PolicySourceID: "reviewed-policy-release-2026-07-19",
	}
	record := compiler.compile(
		t,
		command,
		qualificationpolicy.AuthorityStatusActive,
		time.Date(2026, 7, 19, 20, 0, 0, 123_000_000, time.UTC),
	)
	issueQualificationPolicyRecord(t, ctx, database, record)
	return qualificationPolicyCanaryFixture{
		Record: record, AuthorityID: command.AuthorityID,
		AuthorityHash: record.AuthorityHash, RevisionPolicy: record.RevisionPolicy,
	}
}

func TestQualificationPolicyAuthorityPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "qualification_policy_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	applyQualificationPolicyMigrationPrefix(t, ctx, database)

	ownerID := uuid.New()
	projectID := uuid.New()
	seedQualificationPolicyProject(t, ctx, database, ownerID, projectID, "policy-main")
	profileHash := qualificationPolicyDigest("workflow-engine-v3-canary")
	compiler := newQualificationPolicyFixtureCompiler(t, projectID, profileHash, nil)
	firstCommand := qualificationpolicy.IssueCommand{
		OperationID: uuid.New(), AuthorityID: uuid.New(),
		PolicySourceID: "reviewed-policy-release-2026-07-19",
	}
	first := compiler.compile(
		t, firstCommand, qualificationpolicy.AuthorityStatusActive,
		time.Date(2026, 7, 19, 20, 1, 0, 123_000_000, time.UTC),
	)
	issueQualificationPolicyRecord(t, ctx, database, first)
	assertQualificationPolicyHashParity(t, ctx, database, first)
	assertQualificationPolicyRecordIsExact(t, ctx, database, first.Command.AuthorityID)

	var asserted uuid.UUID
	if err := database.QueryRowContext(ctx, `
SELECT authority_id FROM assert_current_qualification_policy_authority_v1($1)`,
		first.Command.AuthorityID,
	).Scan(&asserted); err != nil || asserted != first.Command.AuthorityID {
		t.Fatalf("assert active current policy = %s, %v", asserted, err)
	}
	if inserted := queryQualificationPolicyIssue(t, ctx, database, first); inserted {
		t.Fatal("exact operation replay was reported as a fresh insert")
	}
	var operationCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM qualification_policy_authorities WHERE operation_id=$1`,
		first.Command.OperationID,
	).Scan(&operationCount); err != nil || operationCount != 1 {
		t.Fatalf("exact replay row count = %d, %v", operationCount, err)
	}

	changedReplay := qualificationPolicyIssueArguments(first)
	changedReplay[8] = qualificationpolicy.AuthorityStatusSuspended
	if err := executeQualificationPolicyIssue(ctx, database, changedReplay); err == nil || !strings.Contains(err.Error(), "WPA03") {
		t.Fatalf("changed operation replay error = %v", err)
	}
	nullReplay := qualificationPolicyIssueArguments(first)
	nullReplay[2] = nil
	if err := executeQualificationPolicyIssue(ctx, database, nullReplay); err == nil || !strings.Contains(err.Error(), "WPA03") {
		t.Fatalf("NULL-widened operation replay error = %v", err)
	}

	secondCommand := qualificationpolicy.IssueCommand{
		OperationID: uuid.New(), AuthorityID: uuid.New(),
		PolicySourceID:                first.Command.PolicySourceID,
		ExpectedPreviousAuthorityHash: first.AuthorityHash,
	}
	second := compiler.compile(
		t, secondCommand, qualificationpolicy.AuthorityStatusSuspended,
		time.Date(2026, 7, 19, 20, 2, 0, 123_000_000, time.UTC),
	)
	stale := qualificationPolicyIssueArguments(second)
	stale[3] = qualificationPolicyDigest("stale-policy-cursor")
	if err := executeQualificationPolicyIssue(ctx, database, stale); err == nil || !strings.Contains(err.Error(), "WPA03") {
		t.Fatalf("stale policy CAS error = %v", err)
	}
	issueQualificationPolicyRecord(t, ctx, database, second)

	var currentID uuid.UUID
	var currentGeneration int64
	var currentStatus string
	if err := database.QueryRowContext(ctx, `
SELECT authority_id,generation,status
FROM resolve_current_qualification_policy_authority_v1($1,$2,$3)`,
		projectID, qualificationpolicy.ExecutionProfileV3, profileHash,
	).Scan(&currentID, &currentGeneration, &currentStatus); err != nil {
		t.Fatal(err)
	}
	if currentID != second.Command.AuthorityID || currentGeneration != 2 || currentStatus != qualificationpolicy.AuthorityStatusSuspended {
		t.Fatalf("current suspended head = %s generation=%d status=%s", currentID, currentGeneration, currentStatus)
	}
	for _, authorityID := range []uuid.UUID{first.Command.AuthorityID, second.Command.AuthorityID} {
		if err := database.QueryRowContext(ctx, `
SELECT authority_id FROM assert_current_qualification_policy_authority_v1($1)`, authorityID,
		).Scan(&asserted); err == nil || !strings.Contains(err.Error(), "WPA02") {
			t.Fatalf("stale/suspended current assertion %s error = %v", authorityID, err)
		}
	}
	var inspected, resolved uuid.UUID
	if err := database.QueryRowContext(ctx, `
SELECT authority_id FROM inspect_qualification_policy_operation_v1($1)`,
		second.Command.OperationID,
	).Scan(&inspected); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT authority_id FROM resolve_qualification_policy_authority_v1($1)`,
		second.Command.AuthorityID,
	).Scan(&resolved); err != nil || inspected != second.Command.AuthorityID || resolved != second.Command.AuthorityID {
		t.Fatalf("inspect/resolve = %s/%s, %v", inspected, resolved, err)
	}

	if _, err := database.ExecContext(ctx, `
UPDATE qualification_policy_authorities SET status='active' WHERE authority_id=$1`,
		second.Command.AuthorityID,
	); err == nil || !strings.Contains(err.Error(), "WPA03") {
		t.Fatalf("immutable policy update error = %v", err)
	}
	if _, err := database.ExecContext(ctx, `TRUNCATE qualification_policy_review_defaults`); err == nil || !strings.Contains(err.Error(), "WPA03") {
		t.Fatalf("immutable policy truncate error = %v", err)
	}
	assertQualificationPolicyACL(t, ctx, database)
	assertQualificationPolicyTamperRejected(t, ctx, database, ownerID)
	assertQualificationPolicyExactSourceGuards(t, ctx, database, ownerID, profileHash)
	assertQualificationPolicyConcurrentIssue(t, ctx, database, ownerID)

	down, err := files.ReadFile("000078_workflow_input_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err == nil || !strings.Contains(err.Error(), "Qualification Policy Authority history") {
		t.Fatalf("non-empty policy rollback error = %v", err)
	}
}

func assertQualificationPolicyTamperRejected(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	ownerID uuid.UUID,
) {
	t.Helper()
	projectID := uuid.New()
	seedQualificationPolicyProject(t, ctx, database, ownerID, projectID, "policy-tamper")
	compiler := newQualificationPolicyFixtureCompiler(
		t, projectID, qualificationPolicyDigest("tamper-profile"), nil,
	)
	record := compiler.compile(t, qualificationpolicy.IssueCommand{
		OperationID: uuid.New(), AuthorityID: uuid.New(),
		PolicySourceID: "reviewed-policy-release-2026-07-19",
	}, qualificationpolicy.AuthorityStatusActive,
		time.Date(2026, 7, 19, 21, 30, 0, 123_000_000, time.UTC))
	args := qualificationPolicyIssueArguments(record)
	tamperedBytes := append([]byte(nil), record.RevisionPolicyBytes...)
	tamperedBytes = append(tamperedBytes, ' ')
	args[13] = tamperedBytes
	if err := executeQualificationPolicyIssue(ctx, database, args); err == nil || !strings.Contains(err.Error(), "WPA03") {
		t.Fatalf("tampered canonical policy bytes error = %v", err)
	}
	var count int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM qualification_policy_authorities WHERE authority_id=$1`,
		record.Command.AuthorityID,
	).Scan(&count); err != nil || count != 0 {
		t.Fatalf("tampered policy persisted rows = %d, %v", count, err)
	}
}

func TestQualificationPolicyAuthorityEmptyRollbackPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "qualification_policy_down_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	applyQualificationPolicyMigrationPrefix(t, ctx, database)
	down, err := files.ReadFile("000078_workflow_input_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("empty Qualification Policy Authority rollback: %v", err)
	}
	var remaining int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM information_schema.tables
WHERE table_schema=current_schema() AND table_name LIKE 'qualification_policy_%'`,
	).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("Qualification Policy Authority tables remaining after down = %d", remaining)
	}
}

func assertQualificationPolicyConcurrentIssue(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	ownerID uuid.UUID,
) {
	t.Helper()
	type outcome struct {
		inserted bool
		err      error
	}
	issue := func(record qualificationpolicy.Record, start <-chan struct{}, result chan<- outcome) {
		<-start
		var authorityID uuid.UUID
		var authorityHash, status string
		var generation int64
		var inserted bool
		err := database.QueryRowContext(
			ctx, qualificationPolicyIssueSQL, qualificationPolicyIssueArguments(record)...,
		).Scan(&authorityID, &authorityHash, &generation, &status, &inserted)
		if err == nil && (authorityID != record.Command.AuthorityID || authorityHash != record.AuthorityHash ||
			generation != record.Document.Generation || status != record.Document.Status) {
			err = fmt.Errorf("concurrent issue projection drifted")
		}
		result <- outcome{inserted: inserted, err: err}
	}

	replayProject := uuid.New()
	seedQualificationPolicyProject(t, ctx, database, ownerID, replayProject, "policy-concurrent-replay")
	replayCompiler := newQualificationPolicyFixtureCompiler(
		t, replayProject, qualificationPolicyDigest("concurrent-replay-profile"), nil,
	)
	replay := replayCompiler.compile(t, qualificationpolicy.IssueCommand{
		OperationID: uuid.New(), AuthorityID: uuid.New(),
		PolicySourceID: "reviewed-policy-release-2026-07-19",
	}, qualificationpolicy.AuthorityStatusActive,
		time.Date(2026, 7, 19, 22, 0, 0, 123_000_000, time.UTC))
	start := make(chan struct{})
	results := make(chan outcome, 2)
	go issue(replay, start, results)
	go issue(replay, start, results)
	close(start)
	fresh := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent exact replay: %v", result.err)
		}
		if result.inserted {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("concurrent exact replay fresh responses = %d, want 1", fresh)
	}

	casProject := uuid.New()
	seedQualificationPolicyProject(t, ctx, database, ownerID, casProject, "policy-concurrent-cas")
	profileHash := qualificationPolicyDigest("concurrent-cas-profile")
	leftCompiler := newQualificationPolicyFixtureCompiler(t, casProject, profileHash, nil)
	rightCompiler := newQualificationPolicyFixtureCompiler(t, casProject, profileHash, nil)
	left := leftCompiler.compile(t, qualificationpolicy.IssueCommand{
		OperationID: uuid.New(), AuthorityID: uuid.New(),
		PolicySourceID: "reviewed-policy-release-2026-07-19",
	}, qualificationpolicy.AuthorityStatusActive,
		time.Date(2026, 7, 19, 22, 1, 0, 123_000_000, time.UTC))
	right := rightCompiler.compile(t, qualificationpolicy.IssueCommand{
		OperationID: uuid.New(), AuthorityID: uuid.New(),
		PolicySourceID: "reviewed-policy-release-2026-07-19",
	}, qualificationpolicy.AuthorityStatusActive,
		time.Date(2026, 7, 19, 22, 1, 0, 123_000_000, time.UTC))
	start = make(chan struct{})
	results = make(chan outcome, 2)
	go issue(left, start, results)
	go issue(right, start, results)
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			successes++
		case strings.Contains(result.err.Error(), "WPA03"):
			conflicts++
		default:
			t.Fatalf("concurrent CAS unexpected error: %v", result.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent CAS successes/conflicts = %d/%d, want 1/1", successes, conflicts)
	}
}

func applyQualificationPolicyMigrationPrefix(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `CREATE TABLE schema_migrations (
  version text PRIMARY KEY, checksum text NOT NULL, down_checksum text,
  applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		t.Fatal(err)
	}
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name > workflowInputAuthorityMigration {
			break
		}
		if err := applyFile(ctx, connection, name); err != nil {
			t.Fatalf("apply prerequisite %s: %v", name, err)
		}
	}
}

func seedQualificationPolicyProject(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	ownerID uuid.UUID,
	projectID uuid.UUID,
	label string,
) {
	t.Helper()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users(id,email,display_name,password_hash)
VALUES($1,$2,'Qualification Policy Canary','unused')
ON CONFLICT (id) DO NOTHING`, ownerID, ownerID.String()+"@policy.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects(id,name,created_by,governance_mode)
VALUES($1,$2,$3,'solo')`, projectID, label, ownerID); err != nil {
		t.Fatal(err)
	}
}

func seedQualificationPolicyExactRevision(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	ownerID uuid.UUID,
	projectID uuid.UUID,
	kind string,
	contentHash string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	artifactID, revisionID := uuid.New(), uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO artifacts(id,project_id,kind,artifact_key,title,created_by)
VALUES($1,$2,$3,$4,'Qualification policy exact source',$5)`,
		artifactID, projectID, kind, "policy-source-"+artifactID.String(), ownerID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO artifact_revisions(
  id,artifact_id,revision_number,schema_version,content_store,content_ref,content_hash,
  byte_size,workflow_status,change_source,change_summary,created_by,approved_at
) VALUES($1,$2,1,1,'mongo',$3,$4,2,'approved','human','policy canary',$5,clock_timestamp())`,
		revisionID, artifactID, "policy-source/"+revisionID.String(), contentHash, ownerID,
	); err != nil {
		t.Fatal(err)
	}
	return artifactID, revisionID
}

func assertQualificationPolicyExactSourceGuards(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	ownerID uuid.UUID,
	profileHash string,
) {
	t.Helper()
	type exactCase struct {
		name       string
		projectID  uuid.UUID
		source     qualificationpolicy.ExactApprovedSource
		wantReject bool
	}
	baseProject := uuid.New()
	seedQualificationPolicyProject(t, ctx, database, ownerID, baseProject, "policy-exact-base")
	contentHash := qualificationPolicyDigest("exact-source-content")
	artifactID, revisionID := seedQualificationPolicyExactRevision(
		t, ctx, database, ownerID, baseProject, "blueprint", contentHash,
	)
	valid := qualificationpolicy.ExactApprovedSource{
		SourceKind: "blueprint", Purpose: "blueprint-source",
		ArtifactID: artifactID.String(), RevisionID: revisionID.String(), ContentHash: contentHash,
	}
	cases := []exactCase{
		{name: "absent", projectID: uuid.New(), source: qualificationpolicy.ExactApprovedSource{
			SourceKind: "blueprint", Purpose: "blueprint-source",
			ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: contentHash,
		}, wantReject: true},
		{name: "cross-project", projectID: uuid.New(), source: valid, wantReject: true},
		{name: "content-drift", projectID: baseProject, source: qualificationpolicy.ExactApprovedSource{
			SourceKind: valid.SourceKind, Purpose: valid.Purpose,
			ArtifactID: valid.ArtifactID, RevisionID: valid.RevisionID,
			ContentHash: qualificationPolicyDigest("wrong-content"),
		}, wantReject: true},
		{name: "cross-kind", projectID: baseProject, source: qualificationpolicy.ExactApprovedSource{
			SourceKind: "page_spec", Purpose: valid.Purpose,
			ArtifactID: valid.ArtifactID, RevisionID: valid.RevisionID, ContentHash: valid.ContentHash,
		}, wantReject: true},
	}
	for _, test := range cases {
		t.Run("exact-source-"+test.name, func(t *testing.T) {
			if test.projectID != baseProject {
				seedQualificationPolicyProject(t, ctx, database, ownerID, test.projectID, "policy-"+test.name)
			}
			compiler := newQualificationPolicyFixtureCompiler(
				t, test.projectID, profileHash, []qualificationpolicy.ExactApprovedSource{test.source},
			)
			record := compiler.compile(t, qualificationpolicy.IssueCommand{
				OperationID: uuid.New(), AuthorityID: uuid.New(),
				PolicySourceID: "reviewed-policy-release-2026-07-19",
			}, qualificationpolicy.AuthorityStatusActive,
				time.Date(2026, 7, 19, 21, 0, 0, 123_000_000, time.UTC))
			err := executeQualificationPolicyIssue(ctx, database, qualificationPolicyIssueArguments(record))
			if err == nil || !strings.Contains(err.Error(), "WPA03") {
				t.Fatalf("exact source %s error = %v", test.name, err)
			}
		})
	}

	validCompiler := newQualificationPolicyFixtureCompiler(
		t, baseProject, qualificationPolicyDigest("exact-source-profile"),
		[]qualificationpolicy.ExactApprovedSource{valid},
	)
	first := validCompiler.compile(t, qualificationpolicy.IssueCommand{
		OperationID: uuid.New(), AuthorityID: uuid.New(),
		PolicySourceID: "reviewed-policy-release-2026-07-19",
	}, qualificationpolicy.AuthorityStatusActive,
		time.Date(2026, 7, 19, 21, 1, 0, 123_000_000, time.UTC))
	issueQualificationPolicyRecord(t, ctx, database, first)

	second := validCompiler.compile(t, qualificationpolicy.IssueCommand{
		OperationID: uuid.New(), AuthorityID: uuid.New(),
		PolicySourceID:                first.Command.PolicySourceID,
		ExpectedPreviousAuthorityHash: first.AuthorityHash,
	}, qualificationpolicy.AuthorityStatusActive,
		time.Date(2026, 7, 19, 21, 2, 0, 123_000_000, time.UTC))
	issueQualificationPolicyRecord(t, ctx, database, second)

	if _, err := database.ExecContext(ctx, `
UPDATE artifact_revisions
SET workflow_status='superseded',superseded_at=clock_timestamp()
WHERE id=$1`, revisionID); err != nil {
		t.Fatal(err)
	}
	assertQualificationPolicyRecordIsExact(t, ctx, database, first.Command.AuthorityID)
	assertQualificationPolicyRecordIsExact(t, ctx, database, second.Command.AuthorityID)

	collisionProject := uuid.New()
	seedQualificationPolicyProject(t, ctx, database, ownerID, collisionProject, "policy-identity-collision")
	collisionCompiler := newQualificationPolicyFixtureCompiler(
		t, collisionProject, qualificationPolicyDigest("collision-profile"), nil,
	)
	collision := collisionCompiler.compile(t, qualificationpolicy.IssueCommand{
		OperationID: uuid.New(), AuthorityID: artifactID,
		PolicySourceID: "reviewed-policy-release-2026-07-19",
	}, qualificationpolicy.AuthorityStatusActive,
		time.Date(2026, 7, 19, 21, 3, 0, 123_000_000, time.UTC))
	if err := executeQualificationPolicyIssue(ctx, database, qualificationPolicyIssueArguments(collision)); err == nil {
		t.Fatal("embedded reference was allowed to become a later local authority identity")
	}
}

func issueQualificationPolicyRecord(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	record qualificationpolicy.Record,
) {
	t.Helper()
	args := qualificationPolicyIssueArguments(record)
	var authorityID uuid.UUID
	var authorityHash, status string
	var generation int64
	var inserted bool
	if err := database.QueryRowContext(ctx, qualificationPolicyIssueSQL, args...).Scan(
		&authorityID, &authorityHash, &generation, &status, &inserted,
	); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) {
			t.Fatalf("issue Qualification Policy Authority: %v; detail=%s where=%s", err, postgresError.Detail, postgresError.Where)
		}
		t.Fatalf("issue Qualification Policy Authority: %v", err)
	}
	if authorityID != record.Command.AuthorityID || authorityHash != record.AuthorityHash ||
		generation != record.Document.Generation || status != record.Document.Status {
		t.Fatalf("issued Qualification Policy projection drifted: %s %s %d %s", authorityID, authorityHash, generation, status)
	}
	if !inserted {
		t.Fatal("new Qualification Policy Authority was reported as an idempotent replay")
	}
}

func queryQualificationPolicyIssue(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	record qualificationpolicy.Record,
) bool {
	t.Helper()
	var authorityID uuid.UUID
	var authorityHash, status string
	var generation int64
	var inserted bool
	if err := database.QueryRowContext(
		ctx, qualificationPolicyIssueSQL, qualificationPolicyIssueArguments(record)...,
	).Scan(&authorityID, &authorityHash, &generation, &status, &inserted); err != nil {
		t.Fatalf("replay Qualification Policy Authority: %v", err)
	}
	if authorityID != record.Command.AuthorityID || authorityHash != record.AuthorityHash ||
		generation != record.Document.Generation || status != record.Document.Status {
		t.Fatalf("replayed Qualification Policy projection drifted: %s %s %d %s", authorityID, authorityHash, generation, status)
	}
	return inserted
}

func executeQualificationPolicyIssue(ctx context.Context, database *sql.DB, args []any) error {
	rows, err := database.QueryContext(ctx, qualificationPolicyIssueSQL, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}

func qualificationPolicyIssueArguments(record qualificationpolicy.Record) []any {
	return []any{
		record.Command.OperationID,
		record.Command.AuthorityID,
		record.Command.PolicySourceID,
		record.Command.ExpectedPreviousAuthorityHash,
		uuid.MustParse(record.Document.ProjectID),
		record.Document.ExecutionProfile.Version,
		record.Document.ExecutionProfile.Hash,
		record.Document.Generation,
		record.Document.Status,
		record.IssuedAt,
		record.Document.ExternalGatePolicy,
		record.Document.SupersessionPolicy,
		record.RevisionPolicyHash,
		record.RevisionPolicyBytes,
		string(record.RevisionPolicyBytes),
		record.PlanInputProfileHash,
		record.PlanInputProfileBytes,
		string(record.PlanInputProfileBytes),
		record.PromotionPolicyHash,
		record.PromotionPolicyBytes,
		string(record.PromotionPolicyBytes),
		record.AuthorityHash,
		record.DocumentBytes,
		string(record.DocumentBytes),
	}
}

func assertQualificationPolicyHashParity(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	record qualificationpolicy.Record,
) {
	t.Helper()
	checks := []struct {
		domain string
		bytes  []byte
		want   string
	}{
		{qualificationpolicy.RevisionPolicyHashDomainV1, record.RevisionPolicyBytes, record.RevisionPolicyHash},
		{qualificationpolicy.PlanInputProfileHashDomainV1, record.PlanInputProfileBytes, record.PlanInputProfileHash},
		{qualificationpolicy.PromotionPolicyHashDomainV1, record.PromotionPolicyBytes, record.PromotionPolicyHash},
		{qualificationpolicy.AuthorityHashDomainV1, record.DocumentBytes, record.AuthorityHash},
	}
	for _, check := range checks {
		var got string
		if err := database.QueryRowContext(ctx, `
SELECT qualification_policy_authority_hash($1,$2)`, check.domain, check.bytes,
		).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != check.want {
			t.Fatalf("domain hash %s = %s, want %s", check.domain, got, check.want)
		}
	}
}

func assertQualificationPolicyRecordIsExact(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	authorityID uuid.UUID,
) {
	t.Helper()
	var exact bool
	if err := database.QueryRowContext(ctx, `
SELECT qualification_policy_authority_record_is_exact_v1($1)`, authorityID,
	).Scan(&exact); err != nil {
		t.Fatal(err)
	}
	if !exact {
		t.Fatalf("Qualification Policy Authority %s is not exact", authorityID)
	}
}

func assertQualificationPolicyACL(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	var publicTable, publicIssue bool
	if err := database.QueryRowContext(ctx, `
SELECT
  has_table_privilege('public', format('%I.qualification_policy_authorities', current_schema()), 'SELECT'),
  has_function_privilege(
    'public',
    format('%I.issue_qualification_policy_authority_v1(uuid,uuid,text,text,uuid,text,text,bigint,text,timestamptz,text,text,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb)', current_schema()),
    'EXECUTE'
  )`).Scan(&publicTable, &publicIssue); err != nil {
		t.Fatal(err)
	}
	if publicTable || publicIssue {
		t.Fatalf("PUBLIC policy ACL is open: table=%t issue=%t", publicTable, publicIssue)
	}
	var applicationExists bool
	if err := database.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname='worksflow_application')`,
	).Scan(&applicationExists); err != nil {
		t.Fatal(err)
	}
	if applicationExists {
		var appTable, appIssue bool
		if err := database.QueryRowContext(ctx, `
SELECT
  has_table_privilege('worksflow_application', format('%I.qualification_policy_authorities', current_schema()), 'SELECT'),
  has_function_privilege(
    'worksflow_application',
    format('%I.issue_qualification_policy_authority_v1(uuid,uuid,text,text,uuid,text,text,bigint,text,timestamptz,text,text,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb)', current_schema()),
    'EXECUTE'
  )`).Scan(&appTable, &appIssue); err != nil {
			t.Fatal(err)
		}
		if appTable || appIssue {
			t.Fatalf("application policy ACL is open: table=%t issue=%t", appTable, appIssue)
		}
	}
	var operatorExists bool
	if err := database.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname='worksflow_qualification_policy_operator')`,
	).Scan(&operatorExists); err != nil {
		t.Fatal(err)
	}
	if operatorExists {
		var directTable, issue, inspect, resolve, current bool
		if err := database.QueryRowContext(ctx, `
SELECT
  has_table_privilege('worksflow_qualification_policy_operator', format('%I.qualification_policy_authorities', current_schema()), 'SELECT'),
  has_function_privilege('worksflow_qualification_policy_operator', format('%I.issue_qualification_policy_authority_v1(uuid,uuid,text,text,uuid,text,text,bigint,text,timestamptz,text,text,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb)', current_schema()), 'EXECUTE'),
  has_function_privilege('worksflow_qualification_policy_operator', format('%I.inspect_qualification_policy_operation_v1(uuid)', current_schema()), 'EXECUTE'),
  has_function_privilege('worksflow_qualification_policy_operator', format('%I.resolve_qualification_policy_authority_v1(uuid)', current_schema()), 'EXECUTE'),
  has_function_privilege('worksflow_qualification_policy_operator', format('%I.resolve_current_qualification_policy_authority_v1(uuid,text,text)', current_schema()), 'EXECUTE')`,
		).Scan(&directTable, &issue, &inspect, &resolve, &current); err != nil {
			t.Fatal(err)
		}
		if directTable || !issue || !inspect || !resolve || !current {
			t.Fatalf("policy operator ACL drifted: table=%t issue=%t inspect=%t resolve=%t current=%t", directTable, issue, inspect, resolve, current)
		}
	}
}

func validQualificationPolicy(
	projectID uuid.UUID,
	profileHash string,
	exactSources []qualificationpolicy.ExactApprovedSource,
) qualificationpolicy.ResolvedPolicy {
	if exactSources == nil {
		exactSources = []qualificationpolicy.ExactApprovedSource{}
	}
	return qualificationpolicy.ResolvedPolicy{
		ProjectID: projectID,
		ExecutionProfile: qualificationpolicy.ExecutionProfileBinding{
			Version: qualificationpolicy.ExecutionProfileV3,
			Hash:    profileHash,
		},
		ExternalGatePolicy: qualificationpolicy.ExternalGatePolicyV1,
		Status:             qualificationpolicy.AuthorityStatusActive,
		SupersessionPolicy: qualificationpolicy.SupersessionPolicyV1,
		RevisionPolicy: qualificationpolicy.RevisionPolicy{
			SchemaVersion:        qualificationpolicy.RevisionPolicySchemaV1,
			SourceCurrencyPolicy: qualificationpolicy.CurrencyLatestApproved,
			WorkspaceTarget: qualificationpolicy.WorkspaceTargetPolicy{
				CurrencyPolicy: qualificationpolicy.CurrencyLatestApproved,
			},
			ReviewByChangeSource: []qualificationpolicy.ChangeSourceReviewRule{
				{ChangeSource: qualificationpolicy.ChangeSourceAIProposal, CanonicalReviewRequired: true},
				{ChangeSource: qualificationpolicy.ChangeSourceHuman, CanonicalReviewRequired: true},
				{ChangeSource: qualificationpolicy.ChangeSourceImport, CanonicalReviewRequired: true},
				{ChangeSource: qualificationpolicy.ChangeSourceMerge, CanonicalReviewRequired: true},
				{ChangeSource: qualificationpolicy.ChangeSourceRollback, CanonicalReviewRequired: true},
				{ChangeSource: qualificationpolicy.ChangeSourceSystem, CanonicalReviewRequired: false},
			},
			ExactApprovedSources: exactSources,
		},
		PlanInputProfile: qualificationpolicy.PlanInputProfile{
			SchemaVersion: qualificationpolicy.PlanInputProfileSchemaV1,
			ArtifactPolicy: qualificationpolicy.ArtifactPolicy{
				MaximumArtifacts:            qualificationevidence.MaximumArtifacts,
				RequireRestrictedEncryption: true,
				RequireTrace:                true,
				RequireVideo:                true,
			},
			Artifacts: []qualificationpolicy.ArtifactExpectation{
				{ID: "browser-video", Kind: qualificationevidence.ArtifactKindVideo, Classification: qualificationevidence.ClassificationRestricted},
				{ID: "credential-safe-trace", Kind: qualificationevidence.ArtifactKindTrace, Classification: qualificationevidence.ClassificationRestricted},
				{ID: "run-result", Kind: qualificationevidence.ArtifactKindRunResult, Classification: qualificationevidence.ClassificationDistributable},
			},
			CredentialProfile: qualificationpolicy.CredentialProfile{
				Audience: "urn:worksflow:qualification", AuthorityID: "credential-authority",
				IssuanceArtifactID:     "credential-set-issuance",
				MemberRequestSetDigest: qualificationPolicyDigest("credential-member-request-set"),
				RevocationArtifactID:   "credential-set-revocation",
			},
			GoldenRuntime: qualificationreceipt.GoldenRuntimeBinding{
				AuthorityDocumentArtifactID: "golden-authority-document",
				AuthorityDocumentDigest:     qualificationPolicyDigest("golden-authority-document"),
				FaultOperationSetDigest:     qualificationreceipt.GoldenFaultOperationSetDigestV1,
				FixtureDocumentArtifactID:   "golden-fixture-document",
				FixtureDocumentDigest:       qualificationPolicyDigest("golden-fixture-document"),
				FixtureID:                   "30000000-0000-4000-8000-000000000001",
			},
			OutputPolicy: qualificationpolicy.OutputPolicy{
				CredentialRevocation: qualificationpolicy.CredentialRevocationPolicyV1,
				PlaintextDisposition: qualificationpolicy.PlaintextDispositionPolicyV1,
				SnapshotMode:         qualificationevidence.ImmutableSnapshotMode,
			},
			Outputs: qualificationevidence.OutputExpectation{
				KMSAttestationArtifactID: "kms-encryption-attestation",
				ArtifactIndexID:          "qualification-artifact-index",
				ReceiptID:                "qualification-receipt",
				SnapshotID:               "qualification-snapshot",
			},
			QualificationManifest: qualificationpolicy.QualificationManifestBinding{
				ArtifactID:  "qualification-manifest",
				RevisionID:  "30000000-0000-4000-8000-000000000002",
				ContentHash: qualificationPolicyDigest("qualification-manifest"),
				PlanDigest:  qualificationPolicyDigest("qualification-plan"),
			},
			Recipient: qualificationevidence.EncryptionRecipient{
				KeyResourceID: "qualification-kms-key", KeyVersion: "version-1",
			},
			SourcePolicyDigest: qualificationPolicyDigest("source-policy"),
			TemplateRelease: qualificationreceipt.TemplateReleaseBinding{
				ID:                    "30000000-0000-4000-8000-000000000003",
				ContentHash:           qualificationPolicyDigest("template-release"),
				ApprovalReceiptDigest: qualificationPolicyDigest("template-release-approval"),
			},
			TrustBindings: qualificationevidence.TrustBindings{
				CaptureAuthorityID: "capture-authority", CredentialAuthorityID: "credential-authority",
				EncryptionAuthorityID: "encryption-authority", IndexerAuthorityID: "indexer-authority",
				KMSAuthorityID: "kms-authority", ReceiptAuthorityID: "receipt-authority",
				SealerAuthorityID: "sealer-authority", VerifierAuthorityID: "verifier-authority",
			},
			TrustPolicyDigest: qualificationPolicyDigest("trust-policy"),
		},
		PromotionPolicy: qualificationpolicy.PromotionPolicy{
			SchemaVersion:       qualificationpolicy.PromotionPolicySchemaV1,
			PlanAuthoritySchema: qualificationpolicy.QualificationPlanAuthoritySchemaV1,
			ReceiptSchema:       qualificationpolicy.QualificationReceiptSchemaV3,
			SingleUseProtocol:   qualificationpolicy.QualificationPromotionProtocolV2,
			IndependentRequirements: []qualificationpolicy.IndependentAuthorityBinding{
				{Kind: qualificationpolicy.IndependentModelProfileActivation, AuthorityID: "model-profile-activation-2026-07", AuthorityHash: qualificationPolicyDigest("model-profile-activation")},
				{Kind: qualificationpolicy.IndependentProductionPostgres, AuthorityID: "production-postgresql-posture-2026-07", AuthorityHash: qualificationPolicyDigest("production-postgresql-posture")},
			},
		},
	}
}

func qualificationPolicyDigest(label string) string {
	digest := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(digest[:])
}

var _ qualificationpolicy.PolicySource = (*qualificationPolicyMigrationSource)(nil)
