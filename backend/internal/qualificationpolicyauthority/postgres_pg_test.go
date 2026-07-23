package qualificationpolicyauthority

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/migrations"
)

// This canary crosses the complete production boundary: PostgreSQL authors the
// clock value, migration 78 atomically appends/replays generations, and the
// adapter independently reconstructs every byte, JSONB, and scalar projection.
func TestPostgresStoreQualificationPolicyAuthorityCanary(t *testing.T) {
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
	schema := "qualification_policy_store_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})

	database, err := sql.Open("pgx", postgresQualificationPolicyCanaryDSN(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(8)
	defer database.Close()
	if err := migrations.Up(ctx, database); err != nil {
		t.Fatalf("apply migrations in temporary schema: %v", err)
	}

	resolved := validResolvedPolicy()
	seedPostgresQualificationPolicyCanary(t, ctx, database, resolved)
	clock, err := NewPostgresClock(database)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresStore(database)
	if err != nil {
		t.Fatal(err)
	}

	firstTime, err := clock.Now(ctx)
	if err != nil {
		t.Fatalf("read first database authority time: %v", err)
	}
	first, err := compileRecord(validIssueCommand(), resolved, 1, nil, firstTime)
	if err != nil {
		t.Fatalf("compile first policy authority: %v", err)
	}
	issued, err := store.Issue(ctx, first)
	if err != nil {
		t.Fatalf("issue first policy authority through PostgresStore: %v", err)
	}
	if issued.Idempotent || !sameImmutableRecord(issued, first) {
		t.Fatal("first PostgreSQL issue did not reproduce the compiled aggregate")
	}

	replayed, err := store.Issue(ctx, first)
	if err != nil {
		t.Fatalf("replay first policy authority through PostgresStore: %v", err)
	}
	if !replayed.Idempotent || !sameImmutableRecord(replayed, first) {
		t.Fatal("exact PostgreSQL replay was not identified as idempotent")
	}
	postgresQualificationPolicyAssertReads(t, ctx, store, first)

	assertionTransaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.AssertCurrentTx(ctx, assertionTransaction, first.Command.AuthorityID)
	if err != nil || !sameImmutableRecord(locked, first) {
		_ = assertionTransaction.Rollback()
		t.Fatalf("transaction-scoped first authority assertion: %#v, %v", locked, err)
	}
	if err := assertionTransaction.Rollback(); err != nil {
		t.Fatal(err)
	}

	secondTime, err := clock.Now(ctx)
	if err != nil {
		t.Fatalf("read second database authority time: %v", err)
	}
	previousHash := first.AuthorityHash
	secondCommand := IssueCommand{
		OperationID:                   uuid.New(),
		AuthorityID:                   uuid.New(),
		PolicySourceID:                first.Command.PolicySourceID,
		ExpectedPreviousAuthorityHash: previousHash,
	}
	second, err := compileRecord(secondCommand, resolved, 2, &previousHash, secondTime)
	if err != nil {
		t.Fatalf("compile second policy authority: %v", err)
	}
	issuedSecond, err := store.Issue(ctx, second)
	if err != nil {
		t.Fatalf("issue second policy authority through PostgresStore: %v", err)
	}
	if issuedSecond.Idempotent || !sameImmutableRecord(issuedSecond, second) {
		t.Fatal("second PostgreSQL issue did not reproduce the compiled aggregate")
	}
	if _, err := store.AssertCurrent(ctx, first.Command.AuthorityID); !errors.Is(err, ErrStale) {
		t.Fatalf("superseded first authority error = %v", err)
	}
	current, err := store.ResolveCurrent(ctx, resolved.ProjectID, resolved.ExecutionProfile)
	if err != nil || !sameImmutableRecord(current, second) {
		t.Fatalf("resolve second current authority: %#v, %v", current, err)
	}
	if _, err := store.AssertCurrent(ctx, second.Command.AuthorityID); err != nil {
		t.Fatalf("assert active second authority: %v", err)
	}
	postgresQualificationPolicyConcurrentExactCanary(t, ctx, store, clock, resolved)
}

func postgresQualificationPolicyConcurrentExactCanary(
	t *testing.T,
	ctx context.Context,
	store *PostgresStore,
	clock *PostgresClock,
	resolved ResolvedPolicy,
) {
	t.Helper()
	resolved.ExecutionProfile.Hash = testDigest("postgres-concurrent-execution-profile")
	issuedAt, err := clock.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := compileRecord(IssueCommand{
		OperationID:    uuid.New(),
		AuthorityID:    uuid.New(),
		PolicySourceID: "reviewed-release-concurrent-canary",
	}, resolved, 1, nil, issuedAt)
	if err != nil {
		t.Fatal(err)
	}

	const writers = 4
	type outcome struct {
		record Record
		err    error
	}
	start := make(chan struct{})
	results := make(chan outcome, writers)
	var wait sync.WaitGroup
	wait.Add(writers)
	for range writers {
		go func() {
			defer wait.Done()
			<-start
			record, issueErr := store.Issue(ctx, candidate)
			results <- outcome{record: record, err: issueErr}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	fresh, replay := 0, 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent exact issue: %v", result.err)
		}
		if !sameImmutableRecord(result.record, candidate) {
			t.Fatal("concurrent exact issue returned different immutable bytes")
		}
		if result.record.Idempotent {
			replay++
		} else {
			fresh++
		}
	}
	if fresh != 1 || replay != writers-1 {
		t.Fatalf("concurrent exact fresh/replay = %d/%d, want 1/%d", fresh, replay, writers-1)
	}
}

func postgresQualificationPolicyAssertReads(
	t *testing.T,
	ctx context.Context,
	store *PostgresStore,
	want Record,
) {
	t.Helper()
	reads := map[string]func() (Record, error){
		"operation": func() (Record, error) {
			return store.InspectOperation(ctx, want.Command.OperationID)
		},
		"authority": func() (Record, error) {
			return store.ResolveAuthority(ctx, want.Command.AuthorityID)
		},
		"current": func() (Record, error) {
			return store.ResolveCurrent(ctx, uuid.MustParse(want.Document.ProjectID), want.Document.ExecutionProfile)
		},
		"assertion": func() (Record, error) {
			return store.AssertCurrent(ctx, want.Command.AuthorityID)
		},
	}
	for name, read := range reads {
		record, err := read()
		if err != nil || !sameImmutableRecord(record, want) {
			t.Fatalf("%s read = %#v, %v", name, record, err)
		}
	}
}

func seedPostgresQualificationPolicyCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	resolved ResolvedPolicy,
) {
	t.Helper()
	ownerID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users(id,email,display_name,password_hash)
VALUES($1,$2,'Qualification Policy Store canary','unused')`,
		ownerID,
		ownerID.String()+"@qualification-policy-store.test",
	); err != nil {
		t.Fatalf("seed canary owner: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects(id,name,created_by,governance_mode)
VALUES($1,'Qualification Policy Store canary',$2,'solo')`,
		resolved.ProjectID,
		ownerID,
	); err != nil {
		t.Fatalf("seed canary project: %v", err)
	}
	if len(resolved.RevisionPolicy.ExactApprovedSources) != 1 {
		t.Fatalf("canary exact source count = %d, want 1", len(resolved.RevisionPolicy.ExactApprovedSources))
	}
	source := resolved.RevisionPolicy.ExactApprovedSources[0]
	artifactID := uuid.MustParse(source.ArtifactID)
	revisionID := uuid.MustParse(source.RevisionID)
	if _, err := database.ExecContext(ctx, `
INSERT INTO artifacts(id,project_id,kind,artifact_key,title,created_by)
VALUES($1,$2,$3,$4,'Qualification Policy exact source',$5)`,
		artifactID,
		resolved.ProjectID,
		source.SourceKind,
		"qualification-policy-source-"+artifactID.String(),
		ownerID,
	); err != nil {
		t.Fatalf("seed canary source artifact: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO artifact_revisions(
  id,artifact_id,revision_number,schema_version,content_store,content_ref,content_hash,
  byte_size,workflow_status,change_source,change_summary,created_by,approved_at
) VALUES($1,$2,1,1,'mongo',$3,$4,2,'approved','human','policy store canary',$5,clock_timestamp())`,
		revisionID,
		artifactID,
		"qualification-policy-source/"+revisionID.String(),
		source.ContentHash,
		ownerID,
	); err != nil {
		t.Fatalf("seed canary approved revision: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE artifacts
SET latest_revision_id=$2,latest_approved_revision_id=$2
WHERE id=$1`, artifactID, revisionID); err != nil {
		t.Fatalf("seed canary artifact head: %v", err)
	}
}

func postgresQualificationPolicyCanaryDSN(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}
