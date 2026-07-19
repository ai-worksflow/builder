package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	verificationstore "github.com/worksflow/builder/backend/internal/verification"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TestVerificationExecutionCleanupObligationsUpgradePostgresCanary exercises
// migration 52 as an upgrade, not as part of an empty-schema bootstrap. It
// deliberately creates execution facts under the migration-51 contract first.
func TestVerificationExecutionCleanupObligationsUpgradePostgresCanary(t *testing.T) {
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
	schema := "verification_cleanup_upgrade_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	applyCleanupObligationMigrationsThrough(t, ctx, database, "000051_verification_output_truncation_gate.up.sql")

	fixture := prepareCleanupObligationCandidateFixture(t, ctx, database)
	live := createLegacyCleanupAttempt(t, ctx, database, fixture, "live", time.Now().Add(5*time.Minute))
	expired := createLegacyCleanupAttempt(t, ctx, database, fixture, "expired", time.Now().Add(750*time.Millisecond))
	terminal := createLegacyCleanupAttempt(t, ctx, database, fixture, "terminal", time.Now().Add(5*time.Minute))
	cancelLegacyCleanupAttempt(t, ctx, database, terminal)
	receipted := createLegacyCleanupAttempt(t, ctx, database, fixture, "receipted", time.Now().Add(5*time.Minute))
	persistLegacyCleanupReceipt(t, ctx, fixture, receipted)

	// Let only the explicitly short execution lease expire before the migration
	// takes its backfill snapshot.
	time.Sleep(time.Second)
	up, err := files.ReadFile("000052_verification_execution_cleanup_obligations.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		t.Fatalf("upgrade schema from migration 51 to 52: %v", err)
	}

	wantStates := map[string]string{
		live.attemptID:      "registered",
		expired.attemptID:   "pending",
		terminal.attemptID:  "pending",
		receipted.attemptID: "pending",
	}
	for attemptID, want := range wantStates {
		var state string
		var fence int64
		if err := database.QueryRowContext(ctx, `
SELECT state, attempt_fence_epoch
FROM verification_execution_cleanups
WHERE scope = 'candidate' AND attempt_id = $1
`, attemptID).Scan(&state, &fence); err != nil {
			t.Fatalf("load backfilled cleanup for Attempt %s: %v", attemptID, err)
		}
		if state != want || fence != 1 {
			t.Fatalf("backfilled cleanup for Attempt %s = %s/fence-%d, want %s/fence-1", attemptID, state, fence, want)
		}
	}

	// Drain two of the three pending obligations, leaving exactly one eligible
	// row for the concurrent-claim canary below.
	for pass := 0; pass < 2; pass++ {
		lease, found, claimErr := fixture.store.ClaimVerificationCleanup(ctx, verificationstore.ClaimVerificationCleanupInput{
			Scope: verificationstore.ScopeCandidate, ActorID: fixture.actorID,
			WorkerID: fmt.Sprintf("cleanup-upgrade-drain-%d", pass), LeaseDuration: time.Minute,
		})
		if claimErr != nil || !found {
			t.Fatalf("claim backfilled cleanup pass %d: found=%t err=%v", pass, found, claimErr)
		}
		if err := fixture.store.CompleteVerificationCleanup(ctx, verificationstore.CompleteVerificationCleanupInput{
			Lease: lease, ActorID: fixture.actorID,
		}); err != nil {
			t.Fatalf("complete backfilled cleanup pass %d: %v", pass, err)
		}
	}

	claims := make(chan cleanupObligationClaimResult, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			lease, found, claimErr := fixture.store.ClaimVerificationCleanup(ctx, verificationstore.ClaimVerificationCleanupInput{
				Scope: verificationstore.ScopeCandidate, ActorID: fixture.actorID,
				WorkerID: fmt.Sprintf("cleanup-upgrade-racer-%d", index), LeaseDuration: time.Second,
			})
			claims <- cleanupObligationClaimResult{lease: lease, found: found, err: claimErr}
		}(index)
	}
	close(start)
	wait.Wait()
	close(claims)
	var winner verificationstore.VerificationCleanupLease
	foundCount := 0
	for result := range claims {
		if result.err != nil {
			t.Fatalf("concurrent cleanup claim: %v", result.err)
		}
		if result.found {
			foundCount++
			winner = result.lease
		}
	}
	if foundCount != 1 || winner.LeaseEpoch != 1 {
		t.Fatalf("concurrent cleanup claims found=%d winner=%#v, want one epoch-1 lease", foundCount, winner)
	}

	// Simulate a cleaner crash: do not call Fail. The expired cleaning lease must
	// be reclaimable, while its stale completion remains fenced out.
	time.Sleep(1200 * time.Millisecond)
	takeover, found, err := fixture.store.ClaimVerificationCleanup(ctx, verificationstore.ClaimVerificationCleanupInput{
		Scope: verificationstore.ScopeCandidate, ActorID: fixture.actorID,
		WorkerID: "cleanup-upgrade-takeover", LeaseDuration: time.Minute,
	})
	if err != nil || !found || takeover.Fence != winner.Fence || takeover.LeaseEpoch != 2 {
		t.Fatalf("take over expired cleaning lease = %#v found=%t err=%v; winner=%#v", takeover, found, err, winner)
	}
	if err := fixture.store.CompleteVerificationCleanup(ctx, verificationstore.CompleteVerificationCleanupInput{
		Lease: winner, ActorID: fixture.actorID,
	}); !errors.Is(err, verificationstore.ErrWorkerLeaseLost) {
		t.Fatalf("stale cleaner completed takeover lease: %v", err)
	}
	if err := fixture.store.CompleteVerificationCleanup(ctx, verificationstore.CompleteVerificationCleanupInput{
		Lease: takeover, ActorID: fixture.actorID,
	}); err != nil {
		t.Fatalf("complete takeover cleanup: %v", err)
	}

	// Exercise rollback with both completed and actively cleaning facts present.
	if _, err := database.ExecContext(ctx, `
UPDATE verification_execution_cleanups
SET state = 'cleaning', version = version + 1, lease_worker_id = 'cleanup-upgrade-down',
    lease_epoch = lease_epoch + 1, claimed_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() + interval '1 minute', updated_by = $2
WHERE scope = 'candidate' AND attempt_id = $1 AND attempt_fence_epoch = 1
  AND state = 'registered'
`, live.attemptID, fixture.actorID); err != nil {
		t.Fatalf("prepare nonempty cleaning rollback fixture: %v", err)
	}
	down, err := files.ReadFile("000052_verification_execution_cleanup_obligations.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("rollback migration 52 with nonempty cleanup facts: %v", err)
	}
	var cleanupTable sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT to_regclass('verification_execution_cleanups')::text`).Scan(&cleanupTable); err != nil {
		t.Fatal(err)
	}
	if cleanupTable.Valid {
		t.Fatalf("migration 52 rollback retained cleanup table %q", cleanupTable.String)
	}
	var v2Function sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT to_regprocedure('guard_canonical_verification_run_transition_v2()')::text`).Scan(&v2Function); err != nil {
		t.Fatal(err)
	}
	if v2Function.Valid {
		t.Fatalf("migration 52 rollback retained v2 Canonical guard %q", v2Function.String)
	}
}

type cleanupObligationCandidateFixture struct {
	projectID string
	actorID   string
	plan      verificationstore.Plan
	store     *verificationstore.PostgresStore
	database  *sql.DB
}

type legacyCleanupAttempt struct {
	runID     string
	attemptID string
	workerID  string
}

type cleanupObligationClaimResult struct {
	lease verificationstore.VerificationCleanupLease
	found bool
	err   error
}

func applyCleanupObligationMigrationsThrough(
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
		migration, readErr := files.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, execErr := database.ExecContext(ctx, string(migration)); execErr != nil {
			t.Fatalf("apply prerequisite migration %s: %v", name, execErr)
		}
	}
}

func prepareCleanupObligationCandidateFixture(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) cleanupObligationCandidateFixture {
	t.Helper()
	seed := seedRepositoryCandidateCanary(t, ctx, database)
	contents := newVerificationCanaryContentStore()
	prepareVerificationPlanningCanary(t, ctx, database, &seed, contents)
	candidate := createSandboxCandidateCanary(t, ctx, database, seed, "cleanup-upgrade")
	candidate = acquireSandboxCandidateLeaseCanary(t, ctx, database, seed.actorID, candidate)
	sessionID := uuid.New()
	insertSandboxSessionCanary(t, ctx, database, seed, candidate.id, sessionID, uuid.Nil, true)
	assertSandboxTransition(t, ctx, database, sessionID, 1, 1, "starting", seed.actorID, "cleanup upgrade runner allocated", uuid.Nil, 2, 1, candidate.version)
	assertSandboxTransition(t, ctx, database, sessionID, 2, 1, "ready", seed.actorID, "cleanup upgrade runner ready", uuid.Nil, 3, 1, candidate.version)
	checkpointID := createSandboxCheckpointCanary(t, ctx, database, candidate.id, seed.actorID, "cleanup upgrade checkpoint")
	var sessionVersion int64
	if err := database.QueryRowContext(ctx, `
SELECT attach_sandbox_session_checkpoint($1, 3, 1, $2, $3)
`, sessionID, seed.actorID, checkpointID).Scan(&sessionVersion); err != nil {
		t.Fatalf("attach cleanup upgrade checkpoint: %v", err)
	}

	profileID := "cleanup-upgrade-v1"
	profileHash := applicationBuildContractCanaryDigest("cleanup-upgrade-profile")
	profileDocument := mustJSON(t, map[string]any{
		"schemaVersion": "verification-profile/v1", "id": profileID, "version": 1,
		"profileHash": profileHash, "supportedTemplateRoles": []string{"web", "api"},
		"verifierImages": []map[string]any{{
			"role": "node", "image": "registry.example/quality-node@" + applicationBuildContractCanaryDigest("cleanup-upgrade-node"),
		}, {
			"role": "python", "image": "registry.example/quality-python@" + applicationBuildContractCanaryDigest("cleanup-upgrade-python"),
		}},
		"commandImageRoles": map[string]any{"web": "node", "api": "python"},
		"builtInChecks":     []any{}, "limits": map[string]any{},
		"networkPolicy":    map[string]any{"dependencyResolver": map[string]any{"network": "bridge"}},
		"hiddenTestBundle": nil, "state": "active",
	})
	if _, err := database.ExecContext(ctx, `
INSERT INTO verification_profile_versions (
  profile_id, version, schema_version, document, content_hash, created_by
) VALUES ($1, 1, 'verification-profile/v1', $2, $3, $4)
`, profileID, profileDocument, profileHash, seed.actorID); err != nil {
		t.Fatalf("insert cleanup upgrade VerificationProfile: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO verification_profile_policies (
  profile_id, profile_version, profile_hash, state, policy_version, reason, updated_by
) VALUES ($1, 1, $2, 'active', 1, 'cleanup upgrade policy', $3)
`, profileID, profileHash, seed.actorID); err != nil {
		t.Fatalf("activate cleanup upgrade VerificationProfile: %v", err)
	}

	gormDatabase, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: database}), &gorm.Config{})
	if err != nil {
		t.Fatalf("open cleanup upgrade GORM store: %v", err)
	}
	store, err := verificationstore.NewPostgresStore(gormDatabase, contents)
	if err != nil {
		t.Fatal(err)
	}
	source, err := verificationstore.NewPostgresCandidatePlanSource(gormDatabase, contents)
	if err != nil {
		t.Fatal(err)
	}
	service, err := verificationstore.NewControlService(store, source, verificationCanaryAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.CreateCandidateRun(ctx, verificationstore.CreateCandidateRunRequest{
		ProjectID: seed.projectID.String(), SessionID: sessionID.String(), CandidateID: candidate.id.String(),
		CheckpointID: checkpointID.String(), ExpectedSessionVersion: uint64(sessionVersion),
		ExpectedSessionEpoch: uint64(candidate.sessionEpoch), ExpectedCandidateVersion: uint64(candidate.version),
		ExpectedWriterLeaseEpoch: uint64(candidate.writerLeaseEpoch),
		VerificationProfile:      verificationstore.ProfileReference{ID: profileID, Version: 1, ContentHash: profileHash},
		Reason:                   "compile cleanup upgrade Plan", ActorID: seed.actorID.String(), OperationID: "cleanup-upgrade-plan",
	})
	if err != nil {
		t.Fatalf("compile cleanup upgrade Candidate Run: %v", err)
	}
	plan, err := store.GetPlan(ctx, seed.projectID.String(), view.Run.Plan.ID)
	if err != nil {
		t.Fatalf("load cleanup upgrade Candidate Plan: %v", err)
	}
	return cleanupObligationCandidateFixture{
		projectID: seed.projectID.String(), actorID: seed.actorID.String(), plan: plan, store: store,
		database: database,
	}
}

func createLegacyCleanupAttempt(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture cleanupObligationCandidateFixture,
	suffix string,
	leaseExpiresAt time.Time,
) legacyCleanupAttempt {
	t.Helper()
	runID, attemptID := uuid.NewString(), uuid.NewString()
	prepared, err := verificationstore.PrepareCreateRunInput(verificationstore.CreateRunInput{
		ID: runID, ProjectID: fixture.projectID,
		Plan:       verificationstore.PlanReference{ID: fixture.plan.ID, ContentHash: fixture.plan.PlanHash},
		RequestKey: "cleanup-upgrade-" + suffix, Reason: "cleanup upgrade " + suffix,
		CreatedBy: fixture.actorID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateRun(ctx, prepared); err != nil {
		t.Fatalf("create cleanup upgrade Run %s: %v", suffix, err)
	}
	workerID := "cleanup-upgrade-worker-" + suffix
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
UPDATE candidate_verification_runs
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = $2, lease_epoch = 1, lease_expires_at = $3,
    started_at = statement_timestamp(), updated_by = $4
WHERE id = $1 AND state = 'queued' AND version = 1 AND fence_epoch = 0
`, runID, workerID, leaseExpiresAt, fixture.actorID); err != nil {
		t.Fatalf("claim legacy cleanup Run %s: %v", suffix, err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO candidate_verification_attempts (
  id, schema_version, run_id, project_id, plan_id, plan_hash,
  ordinal, state, version, fence_epoch, created_by, updated_by
) VALUES ($1, 'candidate-verification-attempt/v1', $2, $3, $4, $5,
          1, 'queued', 1, 0, $6, $6)
`, attemptID, runID, fixture.projectID, fixture.plan.ID, fixture.plan.PlanHash, fixture.actorID); err != nil {
		t.Fatalf("insert legacy cleanup Attempt %s: %v", suffix, err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE candidate_verification_attempts
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = $2, lease_epoch = 1, lease_expires_at = $3,
    started_at = statement_timestamp(), updated_by = $4
WHERE id = $1 AND state = 'queued' AND version = 1 AND fence_epoch = 0
`, attemptID, workerID, leaseExpiresAt, fixture.actorID); err != nil {
		t.Fatalf("claim legacy cleanup Attempt %s: %v", suffix, err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit legacy cleanup claim %s: %v", suffix, err)
	}
	return legacyCleanupAttempt{runID: runID, attemptID: attemptID, workerID: workerID}
}

func cancelLegacyCleanupAttempt(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	attempt legacyCleanupAttempt,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	for _, target := range []struct {
		table      string
		identifier string
	}{
		{table: "candidate_verification_attempts", identifier: attempt.attemptID},
		{table: "candidate_verification_runs", identifier: attempt.runID},
	} {
		if _, err := transaction.ExecContext(ctx, `UPDATE `+target.table+`
SET state = 'cancelled', version = version + 1, lease_expires_at = NULL,
    terminal_reason = 'legacy execution cancelled', execution_error = NULL,
    finished_at = statement_timestamp()
WHERE id = $1 AND state = 'claimed' AND fence_epoch = 1
`, target.identifier); err != nil {
			t.Fatalf("terminalize legacy cleanup %s: %v", target.table, err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit legacy terminal cleanup fixture: %v", err)
	}
}

func persistLegacyCleanupReceipt(
	t *testing.T,
	ctx context.Context,
	fixture cleanupObligationCandidateFixture,
	attempt legacyCleanupAttempt,
) {
	t.Helper()
	database := fixture.database
	for _, state := range []string{"materializing", "preparing", "running", "collecting"} {
		if _, err := database.ExecContext(ctx, `
UPDATE candidate_verification_runs SET state = $2, version = version + 1
WHERE id = $1 AND fence_epoch = 1
`, attempt.runID, state); err != nil {
			t.Fatalf("advance legacy receipt Run to %s: %v", state, err)
		}
		if _, err := database.ExecContext(ctx, `
UPDATE candidate_verification_attempts SET state = $2, version = version + 1
WHERE id = $1 AND fence_epoch = 1
`, attempt.attemptID, state); err != nil {
			t.Fatalf("advance legacy receipt Attempt to %s: %v", state, err)
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	exitCode := 0
	checks := make([]verificationstore.CheckResult, 0, len(fixture.plan.Content.Checks))
	for _, check := range fixture.plan.Content.Checks {
		checks = append(checks, verificationstore.CheckResult{
			ID: check.ID, Kind: check.Kind, ServiceID: check.ServiceID, CommandID: check.CommandID,
			Required: check.Required, Status: verificationstore.CheckPassed, AttemptID: attempt.attemptID,
			VerifierImageDigest: check.VerifierImageDigest, Argv: append([]string(nil), check.Argv...),
			WorkingDirectory: check.WorkingDirectory, ExitCode: &exitCode,
			StartedAt: now, CompletedAt: now.Add(time.Second), DurationMS: 1000, AttemptCount: 1,
			OracleIDs:              append([]string(nil), check.OracleIDs...),
			AcceptanceCriterionIDs: append([]string(nil), check.AcceptanceCriterionIDs...),
			ObligationIDs:          append([]string(nil), check.ObligationIDs...), Diagnostics: []verificationstore.Diagnostic{},
		})
	}
	obligations := make([]verificationstore.ObligationRequirement, 0, len(fixture.plan.Content.Obligations))
	for _, obligation := range fixture.plan.Content.Obligations {
		obligations = append(obligations, verificationstore.ObligationRequirement{
			ID: obligation.ID, Level: obligation.Level, OracleIDs: append([]string(nil), obligation.OracleIDs...),
		})
	}
	sort.Slice(checks, func(left, right int) bool { return checks[left].ID < checks[right].ID })
	sort.Slice(obligations, func(left, right int) bool { return obligations[left].ID < obligations[right].ID })
	subject := fixture.plan.Content.Subject
	receipt, err := verificationstore.NewCandidateReceipt(verificationstore.NewCandidateReceiptInput{
		ID: uuid.NewString(), RunID: attempt.runID, ProjectID: fixture.projectID,
		Subject: verificationstore.CandidateSubject{
			SessionID: subject.SessionID, CandidateID: subject.CandidateID,
			CandidateSnapshotID: subject.CandidateSnapshotID, CandidateVersion: subject.CandidateVersion,
			JournalSequence: subject.JournalSequence, SessionEpoch: subject.SessionEpoch,
			WriterLeaseEpoch: subject.WriterLeaseEpoch, TreeHash: subject.TreeHash,
		},
		BuildManifest: fixture.plan.Content.BuildManifest, BuildContract: fixture.plan.Content.BuildContract,
		FullStackTemplate: fixture.plan.Content.FullStackTemplate, Profile: fixture.plan.Content.Profile,
		Plan:       verificationstore.PlanReference{ID: fixture.plan.ID, ContentHash: fixture.plan.PlanHash},
		AttemptIDs: []string{attempt.attemptID}, Checks: checks, Obligations: obligations,
		CreatedBy: fixture.actorID, CreatedAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("construct legacy cleanup Receipt: %v", err)
	}
	if _, err := fixture.store.PersistReceipt(ctx, verificationstore.PersistReceiptInput{
		Receipt: receipt, ExpectedRunVersion: 6, ExpectedRunFenceEpoch: 1,
		ExpectedRunLeaseWorker: attempt.workerID, ExpectedAttemptID: attempt.attemptID,
		ExpectedAttemptVersion: 6, ExpectedAttemptFence: 1,
	}); err != nil {
		t.Fatalf("persist pre-migration-52 Receipt: %v", err)
	}
}
