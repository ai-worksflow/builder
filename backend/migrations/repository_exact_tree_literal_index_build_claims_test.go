package migrations

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const repositoryExactTreeLiteralIndexBuildClaimsMigration = "000063_repository_exact_tree_literal_index_build_claims"

func TestRepositoryExactTreeLiteralIndexBuildClaimsMigrationDeclaresFencedLeaseCAS(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(repositoryExactTreeLiteralIndexBuildClaimsMigration + ".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile(repositoryExactTreeLiteralIndexBuildClaimsMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{
		"CREATE TABLE repository_exact_tree_literal_index_build_claims",
		"project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE",
		"PRIMARY KEY (project_id, tree_hash)",
		"owner_token uuid NOT NULL",
		"attempt bigint NOT NULL",
		"lease_expires_at timestamptz NOT NULL",
		"lease_expires_at <= renewed_at + interval '5 minutes'",
		"acquire_repository_exact_tree_literal_index_build_claim",
		"claim.lease_expires_at <= observed_at",
		"renew_repository_exact_tree_literal_index_build_claim",
		"claim.attempt = target_attempt",
		"release_repository_exact_tree_literal_index_build_claim",
		"REVOKE INSERT, UPDATE, DELETE, TRUNCATE",
		"Expired owners are fenced by owner token plus attempt",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("build claim migration is missing %q", required)
		}
	}
	for _, required := range []string{
		"DROP FUNCTION IF EXISTS release_repository_exact_tree_literal_index_build_claim",
		"DROP FUNCTION IF EXISTS renew_repository_exact_tree_literal_index_build_claim",
		"DROP FUNCTION IF EXISTS acquire_repository_exact_tree_literal_index_build_claim",
		"DROP TABLE IF EXISTS repository_exact_tree_literal_index_build_claims",
	} {
		if !strings.Contains(string(down), required) {
			t.Fatalf("build claim rollback is missing %q", required)
		}
	}
}

func TestRepositoryExactTreeLiteralIndexBuildClaimsMigrationPostgresCanary(t *testing.T) {
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
	schema := "repository_literal_claim_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	actorID, projectID, otherProjectID := uuid.New(), uuid.New(), uuid.New()
	activeClaimProjectID, expiredClaimProjectID := uuid.New(), uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1,$2,'Build claim actor','not-used')`,
		actorID, "build-claim-"+uuid.NewString()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1,'Build claim project',$5),
       ($2,'Other build claim project',$5),
       ($3,'Active claim cascade project',$5),
       ($4,'Expired claim cascade project',$5)`,
		projectID, otherProjectID, activeClaimProjectID, expiredClaimProjectID, actorID); err != nil {
		t.Fatal(err)
	}
	treeHash := "sha256:" + strings.Repeat("a", 64)
	owner1, owner2 := uuid.New(), uuid.New()
	first := acquireExactTreeLiteralBuildClaimCanary(t, ctx, database, projectID, treeHash, owner1, 300)
	if !first.acquired || first.owner != owner1 || first.attempt != 1 {
		t.Fatalf("first claim=%#v", first)
	}
	busy := acquireExactTreeLiteralBuildClaimCanary(t, ctx, database, projectID, treeHash, owner2, 300)
	if busy.acquired || busy.owner != owner1 || busy.attempt != first.attempt {
		t.Fatalf("busy claim=%#v", busy)
	}
	var renewed bool
	var renewedExpiry sql.NullTime
	if err := database.QueryRowContext(ctx, `
SELECT renewed, current_lease_expires_at
FROM renew_repository_exact_tree_literal_index_build_claim($1,$2,$3,$4,500)`,
		projectID, treeHash, owner1, first.attempt).Scan(&renewed, &renewedExpiry); err != nil {
		t.Fatal(err)
	}
	if !renewed || !renewedExpiry.Valid || !renewedExpiry.Time.After(first.expiresAt) {
		t.Fatalf("renewed=%v expiry=%v first=%v", renewed, renewedExpiry, first.expiresAt)
	}
	if released := releaseExactTreeLiteralBuildClaimCanary(
		t, ctx, database, projectID, treeHash, owner2, first.attempt,
	); released {
		t.Fatal("foreign owner released active claim")
	}
	if released := releaseExactTreeLiteralBuildClaimCanary(
		t, ctx, database, projectID, treeHash, owner1, first.attempt,
	); !released {
		t.Fatal("owner could not release active claim")
	}

	expiring := acquireExactTreeLiteralBuildClaimCanary(t, ctx, database, projectID, treeHash, owner1, 100)
	time.Sleep(150 * time.Millisecond)
	takeover := acquireExactTreeLiteralBuildClaimCanary(t, ctx, database, projectID, treeHash, owner2, 300)
	if !takeover.acquired || takeover.owner != owner2 || takeover.attempt != expiring.attempt+1 {
		t.Fatalf("expired takeover=%#v after %#v", takeover, expiring)
	}
	if err := database.QueryRowContext(ctx, `
SELECT renewed, current_lease_expires_at
FROM renew_repository_exact_tree_literal_index_build_claim($1,$2,$3,$4,300)`,
		projectID, treeHash, owner1, expiring.attempt).Scan(&renewed, &renewedExpiry); err != nil {
		t.Fatal(err)
	}
	if renewed || renewedExpiry.Valid {
		t.Fatalf("expired owner renewed takeover claim: renewed=%v expiry=%v", renewed, renewedExpiry)
	}
	if releaseExactTreeLiteralBuildClaimCanary(t, ctx, database, projectID, treeHash, owner1, expiring.attempt) {
		t.Fatal("expired owner released takeover claim")
	}

	other := acquireExactTreeLiteralBuildClaimCanary(t, ctx, database, otherProjectID, treeHash, owner1, 300)
	if !other.acquired || other.owner != owner1 || other.attempt != 1 {
		t.Fatalf("same tree hash in other tenant was not independent: %#v", other)
	}
	if _, err := database.ExecContext(ctx, `
SELECT * FROM acquire_repository_exact_tree_literal_index_build_claim(
  $1,$2,$3,1,10,16,268435456,2
)`,
		projectID, treeHash, uuid.New()); err == nil {
		t.Fatal("claim function accepted a subminimum TTL")
	}

	activeCascadeClaim := acquireExactTreeLiteralBuildClaimCanary(
		t, ctx, database, activeClaimProjectID, treeHash, uuid.New(), 5000,
	)
	if !activeCascadeClaim.acquired {
		t.Fatalf("active cascade claim was not acquired: %#v", activeCascadeClaim)
	}
	expiredCascadeClaim := acquireExactTreeLiteralBuildClaimCanary(
		t, ctx, database, expiredClaimProjectID, treeHash, uuid.New(), 100,
	)
	if !expiredCascadeClaim.acquired {
		t.Fatalf("expiring cascade claim was not acquired: %#v", expiredCascadeClaim)
	}
	time.Sleep(150 * time.Millisecond)
	var activeClaims, expiredClaims int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FILTER (WHERE lease_expires_at > clock_timestamp()),
       count(*) FILTER (WHERE lease_expires_at <= clock_timestamp())
FROM repository_exact_tree_literal_index_build_claims
WHERE project_id IN ($1,$2)`, activeClaimProjectID, expiredClaimProjectID).Scan(
		&activeClaims, &expiredClaims,
	); err != nil {
		t.Fatal(err)
	}
	if activeClaims != 1 || expiredClaims != 1 {
		t.Fatalf("cascade fixtures were not one active and one expired claim: active=%d expired=%d",
			activeClaims, expiredClaims)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM projects WHERE id IN ($1,$2)`,
		activeClaimProjectID, expiredClaimProjectID); err != nil {
		t.Fatalf("delete projects with unfinished or expired build claims: %v", err)
	}
	var remainingClaims int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM repository_exact_tree_literal_index_build_claims
WHERE project_id IN ($1,$2)`, activeClaimProjectID, expiredClaimProjectID).Scan(&remainingClaims); err != nil {
		t.Fatal(err)
	}
	if remainingClaims != 0 {
		t.Fatalf("project deletion left %d unfinished or expired build claims", remainingClaims)
	}
	var remainingProjects int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM projects WHERE id IN ($1,$2)`,
		activeClaimProjectID, expiredClaimProjectID).Scan(&remainingProjects); err != nil {
		t.Fatal(err)
	}
	if remainingProjects != 0 {
		t.Fatalf("project deletion was blocked for %d claim-only projects", remainingProjects)
	}

	quotaDownSQL, err := files.ReadFile("000064_repository_exact_tree_literal_index_project_quota.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(quotaDownSQL)); err != nil {
		t.Fatalf("rollback project quota migration: %v", err)
	}
	downSQL, err := files.ReadFile(repositoryExactTreeLiteralIndexBuildClaimsMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(downSQL)); err != nil {
		t.Fatalf("rollback build claim migration: %v", err)
	}
	var table sql.NullString
	if err := database.QueryRowContext(ctx,
		`SELECT to_regclass('repository_exact_tree_literal_index_build_claims')::text`,
	).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table.Valid {
		t.Fatalf("build claim table survived rollback as %q", table.String)
	}
}

type exactTreeLiteralBuildClaimCanary struct {
	acquired  bool
	owner     uuid.UUID
	attempt   int64
	expiresAt time.Time
}

func acquireExactTreeLiteralBuildClaimCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	projectID uuid.UUID,
	treeHash string,
	owner uuid.UUID,
	ttlMilliseconds int,
) exactTreeLiteralBuildClaimCanary {
	t.Helper()
	var result exactTreeLiteralBuildClaimCanary
	if err := database.QueryRowContext(ctx, `
SELECT
  decision = 'acquired' AS acquired,
  current_owner_token,
  current_attempt,
  current_lease_expires_at
FROM acquire_repository_exact_tree_literal_index_build_claim(
  $1,$2,$3,1,$4,16,268435456,2
)`,
		projectID, treeHash, owner, ttlMilliseconds,
	).Scan(&result.acquired, &result.owner, &result.attempt, &result.expiresAt); err != nil {
		t.Fatal(err)
	}
	return result
}

func releaseExactTreeLiteralBuildClaimCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	projectID uuid.UUID,
	treeHash string,
	owner uuid.UUID,
	attempt int64,
) bool {
	t.Helper()
	var released bool
	if err := database.QueryRowContext(ctx, `
SELECT release_repository_exact_tree_literal_index_build_claim($1,$2,$3,$4)`,
		projectID, treeHash, owner, attempt,
	).Scan(&released); err != nil {
		t.Fatal(err)
	}
	return released
}
