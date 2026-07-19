package repository

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/migrations"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type exactTreeLiteralIndexPostgresFixture struct {
	context        context.Context
	database       *sql.DB
	gorm           *gorm.DB
	store          *GORMExactTreeLiteralIndexStore
	actorID        uuid.UUID
	projectID      uuid.UUID
	otherProjectID uuid.UUID
}

func TestGORMExactTreeLiteralIndexConcurrentPublishQueryAndTenantIsolationPostgres(t *testing.T) {
	fixture := openExactTreeLiteralIndexPostgresFixture(t)
	text := []byte("RareNeedleXYZ 100% exact body\n")
	unicodeDecoy := []byte("Kelvin decoy only\n")
	binary := []byte{'R', 'a', 'r', 'e', 0, 0xff}
	textHash := rawFileContentHash(text)
	decoyHash := rawFileContentHash(unicodeDecoy)
	binaryHash := rawFileContentHash(binary)
	tree, err := NewTree([]TreeFile{
		{Path: "src/z-copy.ts", Mode: "100644", ContentHash: textHash, ByteSize: int64(len(text))},
		{Path: "src/a.ts", Mode: "100755", ContentHash: textHash, ByteSize: int64(len(text))},
		{Path: "src/kelvin.ts", Mode: "100644", ContentHash: decoyHash, ByteSize: int64(len(unicodeDecoy))},
		{Path: "assets/raw.bin", Mode: "100644", ContentHash: binaryHash, ByteSize: int64(len(binary))},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolver := &exactTreeLiteralIndexResolverFake{
		projectID: fixture.projectID.String(),
		values: map[string][]byte{
			textHash: text, decoyHash: unicodeDecoy, binaryHash: binary,
		},
	}
	service, err := NewExactTreeLiteralIndexService(fixture.store, resolver)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 8
	start := make(chan struct{})
	results := make(chan ExactTreeLiteralIndexManifest, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			manifest, buildErr := service.Build(fixture.context, fixture.projectID.String(), tree)
			if buildErr != nil {
				errorsChannel <- buildErr
				return
			}
			results <- manifest
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsChannel)
	for buildErr := range errorsChannel {
		t.Fatalf("concurrent exact-tree publication: %v", buildErr)
	}
	fresh, reused := 0, 0
	var commitment string
	for manifest := range results {
		if manifest.Reused {
			reused++
		} else {
			fresh++
		}
		if commitment == "" {
			commitment = manifest.IndexCommitment
		} else if manifest.IndexCommitment != commitment {
			t.Fatalf("concurrent publications disagreed on commitment: %q vs %q", manifest.IndexCommitment, commitment)
		}
	}
	if fresh != 1 || reused != workers-1 || resolver.calls != len(tree.Files) {
		t.Fatalf("publication outcomes fresh=%d reused=%d resolveCalls=%d", fresh, reused, resolver.calls)
	}
	var manifests, members, blobs int
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT
  (SELECT count(*) FROM repository_exact_tree_literal_index_manifests WHERE project_id=$1),
  (SELECT count(*) FROM repository_exact_tree_literal_index_members WHERE project_id=$1),
  (SELECT count(*) FROM repository_exact_tree_literal_index_blobs WHERE project_id=$1)`,
		fixture.projectID).Scan(&manifests, &members, &blobs); err != nil {
		t.Fatal(err)
	}
	if manifests != 1 || members != 4 || blobs != 3 {
		t.Fatalf("deduplicated row counts manifests=%d members=%d blobs=%d", manifests, members, blobs)
	}

	assertExactTreeLiteralIndexDocuments(t, service, ExactTreeLiteralIndexQuery{
		ProjectID: fixture.projectID.String(), TreeHash: tree.TreeHash,
		Query: "RareNeedleXYZ", CaseSensitive: true,
	}, "src/a.ts", "src/z-copy.ts")
	assertExactTreeLiteralIndexDocuments(t, service, ExactTreeLiteralIndexQuery{
		ProjectID: fixture.projectID.String(), TreeHash: tree.TreeHash,
		Query: "rareneedlexyz", CaseSensitive: true,
	})
	assertExactTreeLiteralIndexDocuments(t, service, ExactTreeLiteralIndexQuery{
		ProjectID: fixture.projectID.String(), TreeHash: tree.TreeHash,
		Query: "rareneedlexyz", CaseSensitive: false,
	}, "src/a.ts", "src/z-copy.ts")
	assertExactTreeLiteralIndexDocuments(t, service, ExactTreeLiteralIndexQuery{
		ProjectID: fixture.projectID.String(), TreeHash: tree.TreeHash,
		Query: "100%", CaseSensitive: true,
	}, "src/a.ts", "src/z-copy.ts")
	// PostgreSQL lower() would treat the Kelvin sign as an ASCII k in common
	// collations. The indexed expression must preserve Candidate search's ASCII
	// fold and therefore return no candidate for this query.
	assertExactTreeLiteralIndexDocuments(t, service, ExactTreeLiteralIndexQuery{
		ProjectID: fixture.projectID.String(), TreeHash: tree.TreeHash,
		Query: "kelvin", CaseSensitive: false,
	})
	if _, err := service.QueryCandidateDocuments(fixture.context, ExactTreeLiteralIndexQuery{
		ProjectID: fixture.projectID.String(), TreeHash: tree.TreeHash,
		Query: "xy", CaseSensitive: true,
	}); !errors.Is(err, ErrExactTreeLiteralQueryTooShort) {
		t.Fatalf("short query error=%v", err)
	}
	limited, err := service.QueryCandidateDocuments(fixture.context, ExactTreeLiteralIndexQuery{
		ProjectID: fixture.projectID.String(), TreeHash: tree.TreeHash,
		Query: "RareNeedleXYZ", CaseSensitive: true, MaxDocuments: 1,
	})
	if err != nil || !limited.Truncated || len(limited.Documents) != 1 || limited.Documents[0].Path != "src/a.ts" {
		t.Fatalf("max+1 candidate bound = %#v err=%v", limited, err)
	}

	_, err = service.QueryCandidateDocuments(fixture.context, ExactTreeLiteralIndexQuery{
		ProjectID: fixture.otherProjectID.String(), TreeHash: tree.TreeHash,
		Query: "RareNeedleXYZ", CaseSensitive: true,
	})
	if !errors.Is(err, ErrExactTreeLiteralIndexNotReady) {
		t.Fatalf("foreign tenant lookup error=%v, want not-ready", err)
	}
	otherResolver := &exactTreeLiteralIndexResolverFake{values: resolver.values}
	otherService, err := NewExactTreeLiteralIndexService(fixture.store, otherResolver)
	if err != nil {
		t.Fatal(err)
	}
	otherManifest, err := otherService.Build(fixture.context, fixture.otherProjectID.String(), tree)
	if err != nil {
		t.Fatalf("publish same source hashes for second tenant: %v", err)
	}
	if otherManifest.ProjectID != fixture.otherProjectID.String() ||
		otherManifest.IndexCommitment == commitment {
		t.Fatalf("tenant-scoped commitment/publication = %#v", otherManifest)
	}
	assertExactTreeLiteralIndexDocuments(t, otherService, ExactTreeLiteralIndexQuery{
		ProjectID: fixture.otherProjectID.String(), TreeHash: tree.TreeHash,
		Query: "RareNeedleXYZ", CaseSensitive: true,
	}, "src/a.ts", "src/z-copy.ts")
}

func TestGORMExactTreeLiteralIndexQueryHoldsSharedTreeLockAcrossPublicationReadPostgres(t *testing.T) {
	fixture := openExactTreeLiteralIndexPostgresFixture(t)
	body := []byte("one consistent publication needle\n")
	hash := rawFileContentHash(body)
	tree, err := NewTree([]TreeFile{{
		Path: "src/consistent.ts", Mode: "100644", ContentHash: hash, ByteSize: int64(len(body)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewExactTreeLiteralIndexService(
		fixture.store,
		&exactTreeLiteralIndexResolverFake{
			projectID: fixture.projectID.String(), values: map[string][]byte{hash: body},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	published, err := service.Build(fixture.context, fixture.projectID.String(), tree)
	if err != nil {
		t.Fatalf("publish exact-tree query-lock fixture: %v", err)
	}

	// Pin the query to one backend so pg_locks can prove exactly where it is.
	queryConnection, err := fixture.database.Conn(fixture.context)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queryConnection.Close() })
	var queryBackendPID int
	if err := queryConnection.QueryRowContext(
		fixture.context, "SELECT pg_backend_pid()",
	).Scan(&queryBackendPID); err != nil {
		t.Fatal(err)
	}
	queryDatabase, err := gorm.Open(
		postgres.New(postgres.Config{Conn: queryConnection}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatal(err)
	}
	queryStore, err := NewGORMExactTreeLiteralIndexStore(queryDatabase)
	if err != nil {
		t.Fatal(err)
	}

	// Stop the query immediately after its manifest read. It must already own
	// the shared advisory lock while waiting to read the member commitment.
	memberBlocker, err := fixture.database.BeginTx(fixture.context, nil)
	if err != nil {
		t.Fatal(err)
	}
	memberBlockerClosed := false
	t.Cleanup(func() {
		if !memberBlockerClosed {
			_ = memberBlocker.Rollback()
		}
	})
	if _, err := memberBlocker.ExecContext(fixture.context, `
LOCK TABLE repository_exact_tree_literal_index_members IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}

	type queryOutcome struct {
		result ExactTreeLiteralIndexStoreQueryResult
		err    error
	}
	queryFinished := make(chan queryOutcome, 1)
	go func() {
		result, queryErr := queryStore.QueryExactTreeLiteralIndex(
			fixture.context,
			ExactTreeLiteralIndexStoreQuery{
				ProjectID: fixture.projectID.String(), TreeHash: tree.TreeHash,
				Query: "publication needle", CaseSensitive: true, MaxDocuments: 10,
			},
		)
		queryFinished <- queryOutcome{result: result, err: queryErr}
	}()
	waitForExactTreeLiteralIndexPostgresLock(
		t, fixture, queryBackendPID, "advisory", "ShareLock", true, "",
	)
	waitForExactTreeLiteralIndexPostgresLock(
		t, fixture, queryBackendPID, "relation", "AccessShareLock", false,
		"repository_exact_tree_literal_index_members",
	)

	exclusiveConnection, err := fixture.database.Conn(fixture.context)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exclusiveConnection.Close() })
	var exclusiveBackendPID int
	if err := exclusiveConnection.QueryRowContext(
		fixture.context, "SELECT pg_backend_pid()",
	).Scan(&exclusiveBackendPID); err != nil {
		t.Fatal(err)
	}
	exclusiveAcquired := make(chan struct{})
	exclusiveFinished := make(chan error, 1)
	go func() {
		transaction, beginErr := exclusiveConnection.BeginTx(fixture.context, nil)
		if beginErr != nil {
			exclusiveFinished <- beginErr
			return
		}
		if _, lockErr := transaction.ExecContext(fixture.context,
			"SELECT pg_advisory_xact_lock(hashtextextended(CAST($1 AS text), 0))",
			exactTreeLiteralIndexAdvisoryKey(fixture.projectID.String(), tree.TreeHash),
		); lockErr != nil {
			_ = transaction.Rollback()
			exclusiveFinished <- lockErr
			return
		}
		close(exclusiveAcquired)

		// This is the retention-side observation boundary: after the exclusive
		// lock is granted, the same immutable publication must still be complete.
		var manifests, members, blobs int
		row := transaction.QueryRowContext(fixture.context, `
SELECT
  (SELECT count(*) FROM repository_exact_tree_literal_index_manifests
   WHERE project_id=$1 AND tree_hash=$2),
  (SELECT count(*) FROM repository_exact_tree_literal_index_members
   WHERE project_id=$1 AND tree_hash=$2),
  (SELECT count(*) FROM repository_exact_tree_literal_index_blobs
   WHERE project_id=$1 AND content_hash=$3)`, fixture.projectID, tree.TreeHash, hash)
		if scanErr := row.Scan(&manifests, &members, &blobs); scanErr != nil {
			_ = transaction.Rollback()
			exclusiveFinished <- scanErr
			return
		}
		if manifests != 1 || members != 1 || blobs != 1 {
			_ = transaction.Rollback()
			exclusiveFinished <- errors.New("exclusive reader observed an incomplete publication")
			return
		}
		exclusiveFinished <- transaction.Commit()
	}()
	waitForExactTreeLiteralIndexPostgresLock(
		t, fixture, exclusiveBackendPID, "advisory", "ExclusiveLock", false, "",
	)
	select {
	case <-exclusiveAcquired:
		t.Fatal("exclusive tenant/tree lock proceeded while query held its shared lock")
	case outcome := <-queryFinished:
		t.Fatalf("query escaped the member-table barrier: result=%#v err=%v", outcome.result, outcome.err)
	default:
	}

	if err := memberBlocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	memberBlockerClosed = true
	var outcome queryOutcome
	select {
	case outcome = <-queryFinished:
	case <-fixture.context.Done():
		t.Fatalf("query did not finish after member barrier released: %v", fixture.context.Err())
	}
	if outcome.err != nil {
		t.Fatalf("query consistent publication: %v", outcome.err)
	}
	if outcome.result.Manifest.IndexCommitment != published.IndexCommitment ||
		len(outcome.result.Documents) != 1 ||
		outcome.result.Documents[0].Path != "src/consistent.ts" || outcome.result.More {
		t.Fatalf("query observed an inconsistent publication: %#v", outcome.result)
	}
	select {
	case <-exclusiveAcquired:
	case <-fixture.context.Done():
		t.Fatalf("exclusive tenant/tree lock did not proceed after query release: %v", fixture.context.Err())
	}
	select {
	case exclusiveErr := <-exclusiveFinished:
		if exclusiveErr != nil {
			t.Fatalf("exclusive tenant/tree observation: %v", exclusiveErr)
		}
	case <-fixture.context.Done():
		t.Fatalf("exclusive tenant/tree transaction did not finish: %v", fixture.context.Err())
	}
}

func TestGORMExactTreeLiteralIndexTamperPartialAndRollbackFailClosedPostgres(t *testing.T) {
	fixture := openExactTreeLiteralIndexPostgresFixture(t)
	freshBody := []byte("fresh exact source")
	authoritativeBody := []byte("authoritative needle")
	tamperedBody := []byte(strings.Repeat("x", len(authoritativeBody)))
	freshHash := rawFileContentHash(freshBody)
	authoritativeHash := rawFileContentHash(authoritativeBody)
	tree, err := NewTree([]TreeFile{
		{Path: "a-fresh.ts", Mode: "100644", ContentHash: freshHash, ByteSize: int64(len(freshBody))},
		{Path: "z-tampered.ts", Mode: "100644", ContentHash: authoritativeHash, ByteSize: int64(len(authoritativeBody))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database.ExecContext(fixture.context, `
INSERT INTO repository_exact_tree_literal_index_blobs (
  project_id, content_hash, byte_size, is_text, body
) VALUES ($1,$2,$3,true,$4)`,
		fixture.projectID, authoritativeHash, len(tamperedBody), string(tamperedBody)); err != nil {
		t.Fatalf("seed privileged derived-cache tamper: %v", err)
	}
	resolver := &exactTreeLiteralIndexResolverFake{
		projectID: fixture.projectID.String(),
		values:    map[string][]byte{freshHash: freshBody, authoritativeHash: authoritativeBody},
	}
	service, err := NewExactTreeLiteralIndexService(fixture.store, resolver)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Build(fixture.context, fixture.projectID.String(), tree)
	if !errors.Is(err, ErrExactTreeLiteralIndexConflict) {
		t.Fatalf("tampered deduplicated blob error=%v", err)
	}
	var freshBlobs, manifests, members int
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT
  (SELECT count(*) FROM repository_exact_tree_literal_index_blobs WHERE project_id=$1 AND content_hash=$2),
  (SELECT count(*) FROM repository_exact_tree_literal_index_manifests WHERE project_id=$1 AND tree_hash=$3),
  (SELECT count(*) FROM repository_exact_tree_literal_index_members WHERE project_id=$1 AND tree_hash=$3)`,
		fixture.projectID, freshHash, tree.TreeHash).Scan(&freshBlobs, &manifests, &members); err != nil {
		t.Fatal(err)
	}
	if freshBlobs != 0 || manifests != 0 || members != 0 {
		t.Fatalf("failed publication did not roll back fresh=%d manifests=%d members=%d", freshBlobs, manifests, members)
	}

	partialBody := []byte("partial manifest source")
	partialHash := rawFileContentHash(partialBody)
	partialTree, err := NewTree([]TreeFile{{
		Path: "partial.ts", Mode: "100644", ContentHash: partialHash, ByteSize: int64(len(partialBody)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	capture := &exactTreeLiteralIndexStoreFake{}
	partialResolver := &exactTreeLiteralIndexResolverFake{
		projectID: fixture.projectID.String(), values: map[string][]byte{partialHash: partialBody},
	}
	captureService, _ := NewExactTreeLiteralIndexService(capture, partialResolver)
	if _, err := captureService.Build(fixture.context, fixture.projectID.String(), partialTree); err != nil {
		t.Fatal(err)
	}
	build := capture.builds[0]
	if _, err := fixture.database.ExecContext(fixture.context, `
INSERT INTO repository_exact_tree_literal_index_manifests (
  project_id, tree_hash, schema_version, status,
  file_count, text_file_count, skipped_file_count, total_bytes,
  tree_commitment, index_commitment
) VALUES ($1,$2,$3,'building',$4,$5,$6,$7,$8,$9)`,
		fixture.projectID, build.TreeHash, build.SchemaVersion, build.FileCount,
		build.TextFileCount, build.SkippedFileCount, build.TotalBytes,
		build.TreeCommitment, build.IndexCommitment); err != nil {
		t.Fatal(err)
	}
	partialService, _ := NewExactTreeLiteralIndexService(fixture.store, partialResolver)
	_, err = partialService.QueryCandidateDocuments(fixture.context, ExactTreeLiteralIndexQuery{
		ProjectID: fixture.projectID.String(), TreeHash: partialTree.TreeHash,
		Query: "partial", CaseSensitive: true,
	})
	if !errors.Is(err, ErrExactTreeLiteralIndexConflict) || errors.Is(err, ErrExactTreeLiteralIndexNotReady) {
		t.Fatalf("persisted partial query error=%v", err)
	}
	_, err = partialService.Build(fixture.context, fixture.projectID.String(), partialTree)
	if !errors.Is(err, ErrExactTreeLiteralIndexConflict) {
		t.Fatalf("persisted partial rebuild error=%v", err)
	}
	if partialResolver.calls != 1 {
		t.Fatalf("persisted partial reached blob resolution: calls=%d", partialResolver.calls)
	}
}

func TestGORMExactTreeLiteralIndexBuildClaimRenewTakeoverAndTenantFencePostgres(t *testing.T) {
	fixture := openExactTreeLiteralIndexPostgresFixture(t)
	treeHash := digestFixture("durable-build-claim-tree")
	owner1, owner2 := uuid.NewString(), uuid.NewString()
	first, err := fixture.store.AcquireExactTreeLiteralIndexBuildClaim(
		fixture.context,
		exactTreeLiteralIndexClaimRequest(
			fixture.projectID.String(), treeHash, owner1, 1, 250*time.Millisecond,
		),
	)
	if err != nil || first.Disposition != ExactTreeLiteralBuildClaimAcquired ||
		first.Claim.OwnerToken != owner1 || first.Claim.Attempt != 1 {
		t.Fatalf("first durable claim=%#v err=%v", first, err)
	}
	busy, err := fixture.store.AcquireExactTreeLiteralIndexBuildClaim(
		fixture.context,
		exactTreeLiteralIndexClaimRequest(
			fixture.projectID.String(), treeHash, owner2, 1, 250*time.Millisecond,
		),
	)
	if err != nil || busy.Disposition != ExactTreeLiteralBuildClaimWaiting || busy.Claim.OwnerToken != owner1 {
		t.Fatalf("busy durable claim=%#v err=%v", busy, err)
	}
	renewed, err := fixture.store.RenewExactTreeLiteralIndexBuildClaim(
		fixture.context, first.Claim, 450*time.Millisecond,
	)
	if err != nil || !renewed.LeaseExpiresAt.After(first.Claim.LeaseExpiresAt) {
		t.Fatalf("renew durable claim=%#v err=%v", renewed, err)
	}
	time.Sleep(300 * time.Millisecond)
	stillBusy, err := fixture.store.AcquireExactTreeLiteralIndexBuildClaim(
		fixture.context,
		exactTreeLiteralIndexClaimRequest(
			fixture.projectID.String(), treeHash, owner2, 1, 250*time.Millisecond,
		),
	)
	if err != nil || stillBusy.Disposition != ExactTreeLiteralBuildClaimWaiting {
		t.Fatalf("renewed claim expired early: %#v err=%v", stillBusy, err)
	}

	otherTenant, err := fixture.store.AcquireExactTreeLiteralIndexBuildClaim(
		fixture.context,
		exactTreeLiteralIndexClaimRequest(
			fixture.otherProjectID.String(), treeHash, owner2, 1, 250*time.Millisecond,
		),
	)
	if err != nil || otherTenant.Disposition != ExactTreeLiteralBuildClaimAcquired ||
		otherTenant.Claim.Attempt != 1 {
		t.Fatalf("tenant-isolated claim=%#v err=%v", otherTenant, err)
	}
	if err := fixture.store.ReleaseExactTreeLiteralIndexBuildClaim(fixture.context, otherTenant.Claim); err != nil {
		t.Fatal(err)
	}

	lossBody := []byte("claim ownership loss source")
	lossHash := rawFileContentHash(lossBody)
	lossTree, err := NewTree([]TreeFile{{
		Path: "src/lost.ts", Mode: "100644", ContentHash: lossHash, ByteSize: int64(len(lossBody)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	expiryTreeHash := lossTree.TreeHash
	expiring, err := fixture.store.AcquireExactTreeLiteralIndexBuildClaim(
		fixture.context,
		exactTreeLiteralIndexClaimRequest(
			fixture.projectID.String(), expiryTreeHash, owner1, int64(len(lossBody)), 100*time.Millisecond,
		),
	)
	if err != nil || expiring.Disposition != ExactTreeLiteralBuildClaimAcquired {
		t.Fatalf("expiring claim=%#v err=%v", expiring, err)
	}
	time.Sleep(150 * time.Millisecond)
	takeover, err := fixture.store.AcquireExactTreeLiteralIndexBuildClaim(
		fixture.context,
		exactTreeLiteralIndexClaimRequest(
			fixture.projectID.String(), expiryTreeHash, owner2, int64(len(lossBody)), 300*time.Millisecond,
		),
	)
	if err != nil || takeover.Disposition != ExactTreeLiteralBuildClaimAcquired ||
		takeover.Claim.Attempt != expiring.Claim.Attempt+1 {
		t.Fatalf("expired takeover=%#v err=%v", takeover, err)
	}
	capture := &exactTreeLiteralIndexStoreFake{}
	captureService, _ := NewExactTreeLiteralIndexService(
		capture,
		&exactTreeLiteralIndexResolverFake{
			projectID: fixture.projectID.String(), values: map[string][]byte{lossHash: lossBody},
		},
	)
	if _, err := captureService.Build(fixture.context, fixture.projectID.String(), lossTree); err != nil {
		t.Fatal(err)
	}
	lostBuild := capture.builds[0]
	lostBuild.ClaimOwnerToken = expiring.Claim.OwnerToken
	lostBuild.ClaimAttempt = expiring.Claim.Attempt
	if _, err := fixture.store.PublishExactTreeLiteralIndex(fixture.context, lostBuild); !errors.Is(err, ErrExactTreeLiteralBuildClaimLost) {
		t.Fatalf("expired owner publication error=%v", err)
	}
	var lostManifests int
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT count(*)
FROM repository_exact_tree_literal_index_manifests
WHERE project_id=$1 AND tree_hash=$2`, fixture.projectID, lossTree.TreeHash).Scan(&lostManifests); err != nil {
		t.Fatal(err)
	}
	if lostManifests != 0 {
		t.Fatalf("lost owner published %d manifests", lostManifests)
	}
	if _, err := fixture.store.RenewExactTreeLiteralIndexBuildClaim(
		fixture.context, expiring.Claim, 300*time.Millisecond,
	); !errors.Is(err, ErrExactTreeLiteralBuildClaimLost) {
		t.Fatalf("expired owner renewal error=%v", err)
	}
	if err := fixture.store.ReleaseExactTreeLiteralIndexBuildClaim(
		fixture.context, expiring.Claim,
	); !errors.Is(err, ErrExactTreeLiteralBuildClaimLost) {
		t.Fatalf("expired owner release error=%v", err)
	}
	if err := fixture.store.ReleaseExactTreeLiteralIndexBuildClaim(fixture.context, takeover.Claim); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.ReleaseExactTreeLiteralIndexBuildClaim(fixture.context, renewed); err != nil {
		t.Fatal(err)
	}
}

func TestGORMExactTreeLiteralIndexProjectQuotaTypedErrorsAndReservedPublicationPostgres(t *testing.T) {
	fixture := openExactTreeLiteralIndexPostgresFixture(t)
	owner := uuid.NewString()
	firstTree := digestFixture("typed-project-quota-first")
	secondTree := digestFixture("typed-project-quota-second")

	request := exactTreeLiteralIndexClaimRequest(
		fixture.projectID.String(), firstTree, owner, 8, 5*time.Second,
	)
	request.MaxProjectTrees, request.MaxProjectSourceBytes, request.MaxProjectActiveBuilds = 1, 100, 1
	first, err := fixture.store.AcquireExactTreeLiteralIndexBuildClaim(fixture.context, request)
	if err != nil || first.Disposition != ExactTreeLiteralBuildClaimAcquired || first.Claim.ReservedSourceBytes != 8 {
		t.Fatalf("tree quota seed=%#v err=%v", first, err)
	}
	treeRequest := exactTreeLiteralIndexClaimRequest(
		fixture.projectID.String(), secondTree, uuid.NewString(), 1, 5*time.Second,
	)
	treeRequest.MaxProjectTrees, treeRequest.MaxProjectSourceBytes, treeRequest.MaxProjectActiveBuilds = 1, 100, 1
	if _, err := fixture.store.AcquireExactTreeLiteralIndexBuildClaim(
		fixture.context, treeRequest,
	); !errors.Is(err, ErrExactTreeLiteralProjectTreeQuota) {
		t.Fatalf("tree quota error=%v", err)
	}
	if err := fixture.store.ReleaseExactTreeLiteralIndexBuildClaim(fixture.context, first.Claim); err != nil {
		t.Fatal(err)
	}

	request = exactTreeLiteralIndexClaimRequest(
		fixture.projectID.String(), firstTree, owner, 8, 5*time.Second,
	)
	request.MaxProjectTrees, request.MaxProjectSourceBytes, request.MaxProjectActiveBuilds = 3, 10, 3
	first, err = fixture.store.AcquireExactTreeLiteralIndexBuildClaim(fixture.context, request)
	if err != nil {
		t.Fatal(err)
	}
	bytesRequest := exactTreeLiteralIndexClaimRequest(
		fixture.projectID.String(), secondTree, uuid.NewString(), 3, 5*time.Second,
	)
	bytesRequest.MaxProjectTrees, bytesRequest.MaxProjectSourceBytes, bytesRequest.MaxProjectActiveBuilds = 3, 10, 3
	if _, err := fixture.store.AcquireExactTreeLiteralIndexBuildClaim(
		fixture.context, bytesRequest,
	); !errors.Is(err, ErrExactTreeLiteralProjectSourceBytesQuota) {
		t.Fatalf("source-byte quota error=%v", err)
	}
	if err := fixture.store.ReleaseExactTreeLiteralIndexBuildClaim(fixture.context, first.Claim); err != nil {
		t.Fatal(err)
	}

	request = exactTreeLiteralIndexClaimRequest(
		fixture.projectID.String(), firstTree, owner, 1, 5*time.Second,
	)
	request.MaxProjectTrees, request.MaxProjectSourceBytes, request.MaxProjectActiveBuilds = 3, 100, 1
	first, err = fixture.store.AcquireExactTreeLiteralIndexBuildClaim(fixture.context, request)
	if err != nil {
		t.Fatal(err)
	}
	activeRequest := exactTreeLiteralIndexClaimRequest(
		fixture.projectID.String(), secondTree, uuid.NewString(), 1, 5*time.Second,
	)
	activeRequest.MaxProjectTrees, activeRequest.MaxProjectSourceBytes, activeRequest.MaxProjectActiveBuilds = 3, 100, 1
	if _, err := fixture.store.AcquireExactTreeLiteralIndexBuildClaim(
		fixture.context, activeRequest,
	); !errors.Is(err, ErrExactTreeLiteralProjectActiveBuildQuota) {
		t.Fatalf("active-build quota error=%v", err)
	}
	if err := fixture.store.ReleaseExactTreeLiteralIndexBuildClaim(fixture.context, first.Claim); err != nil {
		t.Fatal(err)
	}

	body := []byte("reserved publication source")
	hash := rawFileContentHash(body)
	tree, err := NewTree([]TreeFile{{
		Path: "src/reserved.ts", Mode: "100644", ContentHash: hash, ByteSize: int64(len(body)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	mismatchedRequest := exactTreeLiteralIndexClaimRequest(
		fixture.projectID.String(), tree.TreeHash, uuid.NewString(), int64(len(body))+1, 5*time.Second,
	)
	claim, err := fixture.store.AcquireExactTreeLiteralIndexBuildClaim(fixture.context, mismatchedRequest)
	if err != nil {
		t.Fatal(err)
	}
	capture := &exactTreeLiteralIndexStoreFake{}
	captureService, err := NewExactTreeLiteralIndexService(
		capture,
		&exactTreeLiteralIndexResolverFake{
			projectID: fixture.projectID.String(), values: map[string][]byte{hash: body},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := captureService.Build(fixture.context, fixture.projectID.String(), tree); err != nil {
		t.Fatal(err)
	}
	build := capture.builds[0]
	build.ClaimOwnerToken = claim.Claim.OwnerToken
	build.ClaimAttempt = claim.Claim.Attempt
	if _, err := fixture.store.PublishExactTreeLiteralIndex(
		fixture.context, build,
	); !errors.Is(err, ErrExactTreeLiteralBuildClaimLost) {
		t.Fatalf("publication with mismatched reservation error=%v", err)
	}
	var manifests int
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT count(*) FROM repository_exact_tree_literal_index_manifests
WHERE project_id=$1 AND tree_hash=$2`, fixture.projectID, tree.TreeHash).Scan(&manifests); err != nil {
		t.Fatal(err)
	}
	if manifests != 0 {
		t.Fatalf("mismatched source reservation published %d manifests", manifests)
	}
	if err := fixture.store.ReleaseExactTreeLiteralIndexBuildClaim(fixture.context, claim.Claim); err != nil {
		t.Fatal(err)
	}
}

func TestGORMExactTreeLiteralIndexWaitersDoNotHoldConnectionDuringOwnerBuildPostgres(t *testing.T) {
	fixture := openExactTreeLiteralIndexPostgresFixture(t)
	fixture.database.SetMaxOpenConns(1)
	fixture.database.SetMaxIdleConns(1)
	body := []byte("single connection owner source")
	hash := rawFileContentHash(body)
	tree, err := NewTree([]TreeFile{{
		Path: "src/owner.ts", Mode: "100644", ContentHash: hash, ByteSize: int64(len(body)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	base := &exactTreeLiteralIndexResolverFake{
		projectID: fixture.projectID.String(), values: map[string][]byte{hash: body},
	}
	service, err := NewExactTreeLiteralIndexService(
		fixture.store,
		&exactTreeLiteralIndexSlowResolver{base: base, delay: 300 * time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	service.claimLease = 500 * time.Millisecond
	service.claimHeartbeat = 75 * time.Millisecond
	service.claimPoll = 15 * time.Millisecond

	const workers = 4
	start := make(chan struct{})
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, buildErr := service.Build(fixture.context, fixture.projectID.String(), tree)
			errorsChannel <- buildErr
		}()
	}
	close(start)
	group.Wait()
	close(errorsChannel)
	for buildErr := range errorsChannel {
		if buildErr != nil {
			t.Fatalf("single-connection coordinated build: %v", buildErr)
		}
	}
	if base.calls != 1 {
		t.Fatalf("waiters amplified source resolution %d times", base.calls)
	}
	if stats := fixture.database.Stats(); stats.InUse != 0 || stats.MaxOpenConnections != 1 {
		t.Fatalf("database connection stats after coordinated wait: %#v", stats)
	}
}

func openExactTreeLiteralIndexPostgresFixture(t *testing.T) *exactTreeLiteralIndexPostgresFixture {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	schema := "repository_exact_literal_store_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	database, err := sql.Open("pgx", repositoryCatalogDSNWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := migrations.Up(ctx, database); err != nil {
		t.Fatalf("apply migrations (pg_trgm is required): %v", err)
	}
	gormDatabase, err := gorm.Open(postgres.New(postgres.Config{Conn: database}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &exactTreeLiteralIndexPostgresFixture{
		context: ctx, database: database, gorm: gormDatabase,
		actorID: uuid.New(), projectID: uuid.New(), otherProjectID: uuid.New(),
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'Exact-tree literal store actor', 'not-used')`,
		fixture.actorID, "exact-literal-store-"+uuid.NewString()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, 'Exact literal store', $3), ($2, 'Other exact literal store', $3)`,
		fixture.projectID, fixture.otherProjectID, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	fixture.store, err = NewGORMExactTreeLiteralIndexStore(gormDatabase)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func waitForExactTreeLiteralIndexPostgresLock(
	t *testing.T,
	fixture *exactTreeLiteralIndexPostgresFixture,
	backendPID int,
	lockType, mode string,
	granted bool,
	relation string,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var present bool
		if relation == "" {
			err := fixture.database.QueryRowContext(fixture.context, `
SELECT EXISTS (
  SELECT 1
  FROM pg_locks
  WHERE pid=$1 AND locktype=$2 AND mode=$3 AND granted=$4
)`, backendPID, lockType, mode, granted).Scan(&present)
			if err != nil {
				t.Fatalf("inspect PostgreSQL %s lock: %v", lockType, err)
			}
		} else {
			err := fixture.database.QueryRowContext(fixture.context, `
SELECT EXISTS (
  SELECT 1
  FROM pg_locks
  WHERE pid=$1
    AND locktype=$2
    AND mode=$3
    AND granted=$4
    AND relation=to_regclass($5)
)`, backendPID, lockType, mode, granted, relation).Scan(&present)
			if err != nil {
				t.Fatalf("inspect PostgreSQL %s lock on %s: %v", lockType, relation, err)
			}
		}
		if present {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"backend %d did not expose %s %s granted=%t relation=%q",
				backendPID, lockType, mode, granted, relation,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func exactTreeLiteralIndexClaimRequest(
	projectID, treeHash, ownerToken string,
	sourceBytes int64,
	lease time.Duration,
) ExactTreeLiteralIndexBuildClaimRequest {
	return ExactTreeLiteralIndexBuildClaimRequest{
		ProjectID: projectID, TreeHash: treeHash, OwnerToken: ownerToken,
		SourceBytes: sourceBytes, Lease: lease,
		MaxProjectTrees:        DefaultExactTreeLiteralIndexProjectTrees,
		MaxProjectSourceBytes:  DefaultExactTreeLiteralIndexProjectSourceBytes,
		MaxProjectActiveBuilds: DefaultExactTreeLiteralIndexProjectActiveBuilds,
	}
}

func assertExactTreeLiteralIndexDocuments(
	t *testing.T,
	service *ExactTreeLiteralIndexService,
	query ExactTreeLiteralIndexQuery,
	wantPaths ...string,
) {
	t.Helper()
	result, err := service.QueryCandidateDocuments(context.Background(), query)
	if err != nil {
		t.Fatalf("query exact-tree candidate documents: %v", err)
	}
	if result.Truncated || len(result.Documents) != len(wantPaths) {
		t.Fatalf("candidate documents=%#v, want paths=%#v", result, wantPaths)
	}
	for index, want := range wantPaths {
		if result.Documents[index].Path != want {
			t.Fatalf("candidate[%d].path=%q, want %q", index, result.Documents[index].Path, want)
		}
	}
}
