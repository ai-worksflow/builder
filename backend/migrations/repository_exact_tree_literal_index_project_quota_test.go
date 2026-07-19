package migrations

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const repositoryExactTreeLiteralIndexProjectQuotaMigration = "000064_repository_exact_tree_literal_index_project_quota"

func TestRepositoryExactTreeLiteralIndexProjectQuotaMigrationContract(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(repositoryExactTreeLiteralIndexProjectQuotaMigration + ".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile(repositoryExactTreeLiteralIndexProjectQuotaMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{
		"DROP FUNCTION acquire_repository_exact_tree_literal_index_build_claim(uuid, text, uuid, integer)",
		"DELETE FROM repository_exact_tree_literal_index_build_claims",
		"reserved_source_bytes bigint NOT NULL",
		"repository_exact_tree_literal_index_build_claim_project_quota_idx",
		"pg_advisory_xact_lock",
		"manifest.status = 'ready'",
		"claim.lease_expires_at > observed_at",
		"decision := 'quota_trees'",
		"decision := 'quota_source_bytes'",
		"decision := 'quota_active_builds'",
		"reserved_source_bytes = EXCLUDED.reserved_source_bytes",
		"expired claims cease consuming project quota",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("project quota migration is missing %q", required)
		}
	}
	for _, required := range []string{
		"DROP FUNCTION IF EXISTS acquire_repository_exact_tree_literal_index_build_claim",
		"DELETE FROM repository_exact_tree_literal_index_build_claims",
		"DROP COLUMN IF EXISTS reserved_source_bytes",
		"CREATE OR REPLACE FUNCTION acquire_repository_exact_tree_literal_index_build_claim",
		"uuid, text, uuid, integer",
	} {
		if !strings.Contains(string(down), required) {
			t.Fatalf("project quota rollback is missing %q", required)
		}
	}
}

func TestRepositoryExactTreeLiteralIndexProjectQuotaUpgradeFencesUnaccountedClaimsPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "repository_literal_quota_upgrade_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	applyExactTreeLiteralQuotaMigrationsThrough(
		t, ctx, database, "000063_repository_exact_tree_literal_index_build_claims.up.sql",
	)
	actorID, projectID := uuid.New(), uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id,email,display_name,password_hash)
VALUES ($1,$2,'Quota upgrade actor','not-used')`,
		actorID, "quota-upgrade-"+uuid.NewString()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id,name,created_by)
VALUES ($1,'Quota upgrade project',$2)`, projectID, actorID); err != nil {
		t.Fatal(err)
	}
	treeHash := "sha256:" + strings.Repeat("a", 64)
	if _, err := database.ExecContext(ctx, `
SELECT * FROM acquire_repository_exact_tree_literal_index_build_claim($1,$2,$3,5000)`,
		projectID, treeHash, uuid.New()); err != nil {
		t.Fatal(err)
	}

	applyExactTreeLiteralQuotaMigration(
		t, ctx, database, repositoryExactTreeLiteralIndexProjectQuotaMigration+".up.sql",
	)
	var claims int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM repository_exact_tree_literal_index_build_claims`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if claims != 0 {
		t.Fatalf("upgrade retained %d claims without source reservations", claims)
	}
	decision := acquireExactTreeLiteralQuotaClaim(
		t, ctx, database, projectID, treeHash, uuid.New(), 11, 5_000, 16, 268435456, 2,
	)
	if decision.decision != "acquired" || decision.reservedBytes.Int64 != 11 {
		t.Fatalf("post-upgrade quota claim=%#v", decision)
	}

	applyExactTreeLiteralQuotaMigration(
		t, ctx, database, repositoryExactTreeLiteralIndexProjectQuotaMigration+".down.sql",
	)
	var oldFunction, newFunction sql.NullString
	if err := database.QueryRowContext(ctx, `
SELECT
  to_regprocedure('acquire_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,integer)')::text,
  to_regprocedure('acquire_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer,integer,bigint,integer)')::text`,
	).Scan(&oldFunction, &newFunction); err != nil {
		t.Fatal(err)
	}
	if !oldFunction.Valid || newFunction.Valid {
		t.Fatalf("rollback function identities old=%v new=%v", oldFunction, newFunction)
	}
}

func TestRepositoryExactTreeLiteralIndexProjectQuotaPostgresCanary(t *testing.T) {
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
	schema := "repository_literal_quota_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	if err := Up(ctx, database); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	actorID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1,$2,'Literal quota actor','not-used')`,
		actorID, "literal-quota-"+uuid.NewString()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	projects := make([]uuid.UUID, 8)
	for index := range projects {
		projects[index] = uuid.New()
		if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by) VALUES ($1,$2,$3)`,
			projects[index], "Literal quota project "+uuid.NewString(), actorID); err != nil {
			t.Fatal(err)
		}
	}
	tree := func(label string) string { return "sha256:" + strings.Repeat(label, 64) }

	first := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[0], tree("a"), uuid.New(), 8, 5_000, 1, 100, 1)
	if first.decision != "acquired" || first.reservedBytes.Int64 != 8 {
		t.Fatalf("first tree quota claim=%#v", first)
	}
	waiting := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[0], tree("a"), uuid.New(), 8, 5_000, 1, 100, 1)
	if waiting.decision != "waiting" || waiting.owner.UUID != first.owner.UUID || waiting.attempt.Int64 != first.attempt.Int64 {
		t.Fatalf("same tree was counted twice instead of waiting: %#v", waiting)
	}
	treeQuota := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[0], tree("b"), uuid.New(), 1, 5_000, 1, 100, 1)
	if treeQuota.decision != "quota_trees" {
		t.Fatalf("tree quota decision=%#v", treeQuota)
	}

	if claim := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[1], tree("c"), uuid.New(), 8, 5_000, 3, 10, 3); claim.decision != "acquired" {
		t.Fatalf("source quota seed=%#v", claim)
	}
	bytesQuota := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[1], tree("d"), uuid.New(), 3, 5_000, 3, 10, 3)
	if bytesQuota.decision != "quota_source_bytes" {
		t.Fatalf("source-byte quota decision=%#v", bytesQuota)
	}

	if claim := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[2], tree("e"), uuid.New(), 1, 5_000, 3, 100, 1); claim.decision != "acquired" {
		t.Fatalf("active quota seed=%#v", claim)
	}
	activeQuota := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[2], tree("f"), uuid.New(), 1, 5_000, 3, 100, 1)
	if activeQuota.decision != "quota_active_builds" {
		t.Fatalf("active-build quota decision=%#v", activeQuota)
	}

	expired := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[3], tree("1"), uuid.New(), 9, 100, 1, 9, 1)
	if expired.decision != "acquired" {
		t.Fatalf("expiring quota seed=%#v", expired)
	}
	time.Sleep(150 * time.Millisecond)
	afterExpiry := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[3], tree("2"), uuid.New(), 9, 5_000, 1, 9, 1)
	if afterExpiry.decision != "acquired" {
		t.Fatalf("expired claim retained quota=%#v", afterExpiry)
	}

	expiredSameTree := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[4], tree("3"), uuid.New(), 7, 100, 1, 7, 1)
	time.Sleep(150 * time.Millisecond)
	takeover := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[4], tree("3"), uuid.New(), 7, 5_000, 1, 7, 1)
	if takeover.decision != "acquired" || takeover.attempt.Int64 != expiredSameTree.attempt.Int64+1 || takeover.reservedBytes.Int64 != 7 {
		t.Fatalf("expired same-tree takeover=%#v after=%#v", takeover, expiredSameTree)
	}

	released := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[5], tree("4"), uuid.New(), 5, 5_000, 1, 5, 1)
	if !releaseExactTreeLiteralQuotaClaim(t, ctx, database, projects[5], tree("4"), released) {
		t.Fatal("exact claim release failed")
	}
	afterRelease := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[5], tree("5"), uuid.New(), 5, 5_000, 1, 5, 1)
	if afterRelease.decision != "acquired" {
		t.Fatalf("released claim retained quota=%#v", afterRelease)
	}

	seedReadyLiteralQuotaManifest(t, ctx, database, projects[6], tree("6"), 10)
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_build_claims (
  project_id, tree_hash, owner_token, attempt, reserved_source_bytes,
  acquired_at, renewed_at, lease_expires_at
) VALUES ($1,$2,$3,1,10,statement_timestamp(),statement_timestamp(),statement_timestamp()+interval '5 seconds')`,
		projects[6], tree("6"), uuid.New()); err != nil {
		t.Fatal(err)
	}
	readyHandoff := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[6], tree("7"), uuid.New(), 10, 5_000, 2, 20, 1)
	if readyHandoff.decision != "acquired" {
		t.Fatalf("ready manifest and residual claim were double counted: %#v", readyHandoff)
	}
	readyTreeQuota := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[6], tree("9"), uuid.New(), 0, 5_000, 2, 100, 2)
	if readyTreeQuota.decision != "quota_trees" {
		t.Fatalf("ready plus active trees were not enforced: %#v", readyTreeQuota)
	}
	readyBytesQuota := acquireExactTreeLiteralQuotaClaim(t, ctx, database, projects[6], tree("8"), uuid.New(), 1, 5_000, 3, 20, 2)
	if readyBytesQuota.decision != "quota_source_bytes" {
		t.Fatalf("ready plus active source bytes were not enforced: %#v", readyBytesQuota)
	}

	start := make(chan struct{})
	type concurrentDecision struct {
		decision string
		err      error
	}
	decisions := make(chan concurrentDecision, 2)
	var group sync.WaitGroup
	for _, candidateTree := range []string{tree("8"), tree("9")} {
		group.Add(1)
		go func(treeHash string) {
			defer group.Done()
			<-start
			decision, decisionErr := queryExactTreeLiteralQuotaClaim(
				ctx, database, projects[7], treeHash, uuid.New(), 1, 5_000, 1, 100, 1,
			)
			decisions <- concurrentDecision{decision: decision.decision, err: decisionErr}
		}(candidateTree)
	}
	close(start)
	group.Wait()
	close(decisions)
	counts := map[string]int{}
	for result := range decisions {
		if result.err != nil {
			t.Fatalf("concurrent quota acquisition: %v", result.err)
		}
		counts[result.decision]++
	}
	if counts["acquired"] != 1 || counts["quota_trees"] != 1 {
		t.Fatalf("concurrent final quota slot decisions=%v", counts)
	}
}

type exactTreeLiteralQuotaClaimDecision struct {
	decision      string
	owner         uuid.NullUUID
	attempt       sql.NullInt64
	reservedBytes sql.NullInt64
	expiresAt     sql.NullTime
}

func acquireExactTreeLiteralQuotaClaim(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	projectID uuid.UUID,
	treeHash string,
	owner uuid.UUID,
	sourceBytes int64,
	ttlMilliseconds, maxTrees int,
	maxSourceBytes int64,
	maxActiveBuilds int,
) exactTreeLiteralQuotaClaimDecision {
	t.Helper()
	result, err := queryExactTreeLiteralQuotaClaim(
		ctx, database, projectID, treeHash, owner, sourceBytes,
		ttlMilliseconds, maxTrees, maxSourceBytes, maxActiveBuilds,
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func queryExactTreeLiteralQuotaClaim(
	ctx context.Context,
	database *sql.DB,
	projectID uuid.UUID,
	treeHash string,
	owner uuid.UUID,
	sourceBytes int64,
	ttlMilliseconds, maxTrees int,
	maxSourceBytes int64,
	maxActiveBuilds int,
) (exactTreeLiteralQuotaClaimDecision, error) {
	var result exactTreeLiteralQuotaClaimDecision
	if err := database.QueryRowContext(ctx, `
SELECT
  decision, current_owner_token, current_attempt,
  current_reserved_source_bytes, current_lease_expires_at
FROM acquire_repository_exact_tree_literal_index_build_claim(
  $1,$2,$3,$4,$5,$6,$7,$8
)`, projectID, treeHash, owner, sourceBytes, ttlMilliseconds,
		maxTrees, maxSourceBytes, maxActiveBuilds).Scan(
		&result.decision, &result.owner, &result.attempt,
		&result.reservedBytes, &result.expiresAt,
	); err != nil {
		return exactTreeLiteralQuotaClaimDecision{}, err
	}
	return result, nil
}

func releaseExactTreeLiteralQuotaClaim(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	projectID uuid.UUID,
	treeHash string,
	claim exactTreeLiteralQuotaClaimDecision,
) bool {
	t.Helper()
	var released bool
	if err := database.QueryRowContext(ctx, `
SELECT release_repository_exact_tree_literal_index_build_claim($1,$2,$3,$4)`,
		projectID, treeHash, claim.owner.UUID, claim.attempt.Int64).Scan(&released); err != nil {
		t.Fatal(err)
	}
	return released
}

func seedReadyLiteralQuotaManifest(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	projectID uuid.UUID,
	treeHash string,
	byteSize int64,
) {
	t.Helper()
	contentHash := "sha256:" + strings.Repeat("b", 64)
	commitment := "sha256:" + strings.Repeat("c", 64)
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_blobs (
  project_id, content_hash, byte_size, is_text, body
) VALUES ($1,$2,$3,true,$4)`,
		projectID, contentHash, byteSize, strings.Repeat("x", int(byteSize))); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_manifests (
  project_id, tree_hash, schema_version, status,
  file_count, text_file_count, skipped_file_count, total_bytes,
  tree_commitment, index_commitment
) VALUES ($1,$2,'repository-exact-tree-literal-index/v1','building',1,1,0,$3,$4,$4)`,
		projectID, treeHash, byteSize, commitment); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_members (
  project_id, tree_hash, path, mode, content_hash, byte_size, indexed
) VALUES ($1,$2,'quota.txt','100644',$3,$4,true)`,
		projectID, treeHash, contentHash, byteSize); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE repository_exact_tree_literal_index_manifests
SET status='ready', ready_at=statement_timestamp()
WHERE project_id=$1 AND tree_hash=$2`, projectID, treeHash); err != nil {
		t.Fatal(err)
	}
}

func applyExactTreeLiteralQuotaMigrationsThrough(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	last string,
) {
	t.Helper()
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name > last {
			break
		}
		applyExactTreeLiteralQuotaMigration(t, ctx, database, name)
	}
}

func applyExactTreeLiteralQuotaMigration(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	name string,
) {
	t.Helper()
	migration, err := files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("apply migration %s: %v", name, err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit migration %s: %v", name, err)
	}
}
