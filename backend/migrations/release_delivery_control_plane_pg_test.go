package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/release"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"github.com/worksflow/builder/backend/internal/verification"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type releaseHeadContentStub struct{}

func (releaseHeadContentStub) PutPending(context.Context, string, string, string, int, json.RawMessage) (content.Reference, error) {
	panic("content store is not used while creating a production Run")
}
func (releaseHeadContentStub) Finalize(context.Context, string) error { panic("not used") }
func (releaseHeadContentStub) Abort(context.Context, string) error    { panic("not used") }
func (releaseHeadContentStub) Get(context.Context, string, string) (content.StoredContent, error) {
	panic("not used")
}

type releaseHeadReceiptStub struct{}

func (releaseHeadReceiptStub) GetCanonicalReceipt(context.Context, string, string, string) (verification.CanonicalReceipt, error) {
	panic("Canonical Receipt reader is not used while creating a production Run")
}

type releaseDeliveryHeadSeed struct {
	previewReceiptID   uuid.UUID
	previewReceiptHash string
	approvalID         uuid.UUID
	approvalHash       string
	revisionID         uuid.UUID
	revisionHash       string
	receiptID          uuid.UUID
	receiptHash        string
}

func assertReleaseDeliveryControlPlane(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	seed repositoryCandidateCanarySeed,
	bundleID uuid.UUID,
	bundleHash string,
	canonicalReceiptID uuid.UUID,
	canonicalReceiptHash string,
	releaseArtifacts []byte,
) releaseDeliveryHeadSeed {
	t.Helper()
	previewRunID, previewReceiptID, approvalID := uuid.New(), uuid.New(), uuid.New()
	previewReceiptHash := applicationBuildContractCanaryDigest("delivery-preview-receipt")
	approvalHash := applicationBuildContractCanaryDigest("delivery-promotion-approval")
	previewPassedChecks := `[{"id":"contract","kind":"contract","status":"passed"},{"id":"health","kind":"health","status":"passed"},{"id":"migration","kind":"migration","status":"passed"},{"id":"playwright","kind":"e2e","status":"passed"},{"id":"smoke","kind":"smoke","status":"passed"}]`
	productionPassedChecks := `[{"id":"readiness","kind":"health","status":"passed"},{"id":"rollout-health","kind":"rollout","status":"passed"}]`
	if _, err := database.ExecContext(ctx, `
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES ($1, 'release-preview-run/v1', $2, $3, $4, $5, $6,
          'delivery preview canary', 'queued', 1, $7, $7)
`, previewRunID, seed.projectID, bundleID, bundleHash, "preview-"+previewRunID.String(),
		applicationBuildContractCanaryDigest("delivery-preview-request"), seed.actorID); err != nil {
		t.Fatalf("insert Preview Run: %v", err)
	}
	claimReleaseDeliveryRun(t, ctx, database, "release_preview_runs", previewRunID, seed.actorID)
	if _, err := database.ExecContext(ctx, `
INSERT INTO release_preview_receipts (
  id, schema_version, run_id, project_id, release_bundle_id, release_bundle_hash,
  canonical_receipt_id, canonical_receipt_hash,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  release_artifacts, namespace, provider, provider_ref, checks, decision,
  content_store, content_ref, content_hash, payload_hash, created_by
) VALUES (
  $1, 'release-preview-receipt/v1', $2, $3, $4, $5, $6, $7,
  $8, $9, $10, $11::jsonb, 'preview-canary', 'canary', 'preview/ref', $12::jsonb, 'passed',
  'blob', $13, $14, $15, $16
)
`, previewReceiptID, previewRunID, seed.projectID, bundleID, bundleHash,
		canonicalReceiptID, canonicalReceiptHash, seed.workspaceArtifactID, seed.workspaceRevisionID,
		seed.workspaceHash, releaseArtifacts, previewPassedChecks, "blob://delivery-preview/"+previewReceiptID.String(),
		applicationBuildContractCanaryDigest("delivery-preview-content"), previewReceiptHash, seed.actorID); err != nil {
		t.Fatalf("insert PreviewReceipt: %v", err)
	}
	terminalReleaseDeliveryRun(t, ctx, database, "release_preview_runs", previewRunID, "passed", seed.actorID)
	if _, err := database.ExecContext(ctx, `
INSERT INTO release_promotion_approvals (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  preview_receipt_id, preview_receipt_hash, reason,
  content_store, content_ref, content_hash, payload_hash, created_by
) VALUES ($1, 'release-promotion-approval/v1', $2, $3, $4, $5, $6,
          'exact preview approved', 'blob', $7, $8, $9, $10)
`, approvalID, seed.projectID, bundleID, bundleHash, previewReceiptID, previewReceiptHash,
		"blob://delivery-approval/"+approvalID.String(), applicationBuildContractCanaryDigest("delivery-approval-content"),
		approvalHash, seed.actorID); err != nil {
		t.Fatalf("insert PromotionApproval: %v", err)
	}

	failedRunID := insertProductionRunCanary(
		t, ctx, database, seed, bundleID, bundleHash, previewReceiptID, previewReceiptHash,
		approvalID, approvalHash, "promote", nil, nil, "failed-production",
	)
	claimReleaseDeliveryRun(t, ctx, database, "release_deployment_runs", failedRunID, seed.actorID)
	failedReceiptID := uuid.New()
	failedReceiptHash := applicationBuildContractCanaryDigest("delivery-failed-production-receipt")
	failedChecks := `[{"id":"health","kind":"health","status":"failed","detail":"503"}]`
	insertProductionReceiptCanary(
		t, ctx, database, seed, failedRunID, failedReceiptID, failedReceiptHash,
		bundleID, bundleHash, previewReceiptID, previewReceiptHash, approvalID, approvalHash,
		"promote", nil, nil, "", failedChecks, "failed",
	)
	if err := insertDeploymentRevisionCanary(
		ctx, database, seed, failedRunID, uuid.New(), applicationBuildContractCanaryDigest("invalid-failed-revision"),
		bundleID, bundleHash, previewReceiptID, previewReceiptHash, approvalID, approvalHash,
		failedReceiptID, failedReceiptHash, "promote", nil, nil, "https://invalid.example", failedChecks,
	); err == nil || !strings.Contains(err.Error(), "exact passed ProductionReceipt") {
		t.Fatalf("failed ProductionReceipt created a DeploymentRevision: %v", err)
	}
	terminalReleaseDeliveryRun(t, ctx, database, "release_deployment_runs", failedRunID, "failed", seed.actorID)
	if _, err := database.ExecContext(ctx, `UPDATE release_production_receipts SET provider_ref = 'tampered' WHERE id = $1`, failedReceiptID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("ProductionReceipt mutation was not rejected: %v", err)
	}

	promoteRunID := insertProductionRunCanary(
		t, ctx, database, seed, bundleID, bundleHash, previewReceiptID, previewReceiptHash,
		approvalID, approvalHash, "promote", nil, nil, "healthy-production",
	)
	claimReleaseDeliveryRun(t, ctx, database, "release_deployment_runs", promoteRunID, seed.actorID)
	promoteReceiptID, promoteRevisionID := uuid.New(), uuid.New()
	promoteReceiptHash := applicationBuildContractCanaryDigest("delivery-passed-production-receipt")
	promoteRevisionHash := applicationBuildContractCanaryDigest("delivery-passed-deployment-revision")
	insertProductionReceiptCanary(
		t, ctx, database, seed, promoteRunID, promoteReceiptID, promoteReceiptHash,
		bundleID, bundleHash, previewReceiptID, previewReceiptHash, approvalID, approvalHash,
		"promote", nil, nil, "https://production.example", productionPassedChecks, "passed",
	)
	if err := insertDeploymentRevisionCanary(
		ctx, database, seed, promoteRunID, promoteRevisionID, promoteRevisionHash,
		bundleID, bundleHash, previewReceiptID, previewReceiptHash, approvalID, approvalHash,
		promoteReceiptID, promoteReceiptHash, "promote", nil, nil, "https://production.example", productionPassedChecks,
	); err != nil {
		t.Fatalf("insert exact DeploymentRevision: %v", err)
	}
	terminalReleaseDeliveryRun(t, ctx, database, "release_deployment_runs", promoteRunID, "healthy", seed.actorID)

	rollbackRunID := insertProductionRunCanary(
		t, ctx, database, seed, bundleID, bundleHash, previewReceiptID, previewReceiptHash,
		approvalID, approvalHash, "rollback", &promoteRevisionID, &promoteRevisionHash, "rollback-production",
	)
	claimReleaseDeliveryRun(t, ctx, database, "release_deployment_runs", rollbackRunID, seed.actorID)
	rollbackReceiptID, rollbackRevisionID := uuid.New(), uuid.New()
	rollbackReceiptHash := applicationBuildContractCanaryDigest("delivery-rollback-production-receipt")
	rollbackRevisionHash := applicationBuildContractCanaryDigest("delivery-rollback-deployment-revision")
	insertProductionReceiptCanary(
		t, ctx, database, seed, rollbackRunID, rollbackReceiptID, rollbackReceiptHash,
		bundleID, bundleHash, previewReceiptID, previewReceiptHash, approvalID, approvalHash,
		"rollback", &promoteRevisionID, &promoteRevisionHash, "https://production.example", productionPassedChecks, "passed",
	)
	if err := insertDeploymentRevisionCanary(
		ctx, database, seed, rollbackRunID, rollbackRevisionID, rollbackRevisionHash,
		bundleID, bundleHash, previewReceiptID, previewReceiptHash, approvalID, approvalHash,
		rollbackReceiptID, rollbackReceiptHash, "rollback", &promoteRevisionID, &promoteRevisionHash,
		"https://production.example", productionPassedChecks,
	); err != nil {
		t.Fatalf("insert rollback DeploymentRevision: %v", err)
	}
	terminalReleaseDeliveryRun(t, ctx, database, "release_deployment_runs", rollbackRunID, "healthy", seed.actorID)

	bypassRunID := insertProductionRunCanary(
		t, ctx, database, seed, bundleID, bundleHash, previewReceiptID, previewReceiptHash,
		approvalID, approvalHash, "promote", nil, nil, "terminal-bypass",
	)
	claimReleaseDeliveryRun(t, ctx, database, "release_deployment_runs", bypassRunID, seed.actorID)
	if _, err := database.ExecContext(ctx, `
UPDATE release_deployment_runs
SET state = 'healthy', version = 5, lease_worker_id = NULL, lease_epoch = NULL,
    lease_expires_at = NULL, finished_at = statement_timestamp(), updated_by = $2
WHERE id = $1
`, bypassRunID, seed.actorID); err == nil || !strings.Contains(err.Error(), "ProductionReceipt and DeploymentRevision") {
		t.Fatalf("production Run terminal bypass was accepted: %v", err)
	}
	terminalReleaseDeliveryRun(t, ctx, database, "release_deployment_runs", bypassRunID, "error", seed.actorID)
	return releaseDeliveryHeadSeed{
		previewReceiptID: previewReceiptID, previewReceiptHash: previewReceiptHash,
		approvalID: approvalID, approvalHash: approvalHash,
		revisionID: rollbackRevisionID, revisionHash: rollbackRevisionHash,
		receiptID: rollbackReceiptID, receiptHash: rollbackReceiptHash,
	}
}

func assertReleaseProductionHeadFencing(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	seed repositoryCandidateCanarySeed,
	bundleID uuid.UUID,
	bundleHash string,
	previous releaseDeliveryHeadSeed,
) {
	t.Helper()
	var headRevisionID, headReceiptID uuid.UUID
	var headRevisionHash, headReceiptHash string
	var generation int64
	if err := database.QueryRowContext(ctx, `
SELECT deployment_revision_id, deployment_revision_hash,
       production_receipt_id, production_receipt_hash, generation
FROM release_production_heads
WHERE project_id = $1 AND environment = 'production'
`, seed.projectID).Scan(
		&headRevisionID, &headRevisionHash, &headReceiptID, &headReceiptHash, &generation,
	); err != nil {
		t.Fatalf("load backfilled production head: %v", err)
	}
	if headRevisionID != previous.revisionID || headRevisionHash != previous.revisionHash ||
		headReceiptID != previous.receiptID || headReceiptHash != previous.receiptHash || generation != 1 {
		t.Fatalf("backfilled production head is not the latest immutable healthy revision: revision=%s/%s receipt=%s/%s generation=%d",
			headRevisionID, headRevisionHash, headReceiptID, headReceiptHash, generation)
	}

	insertRun := func(runID uuid.UUID, label string, expected bool) error {
		var expectedRevisionID, expectedRevisionHash any
		var expectedReceiptID, expectedReceiptHash any
		if expected {
			expectedRevisionID, expectedRevisionHash = headRevisionID, headRevisionHash
			expectedReceiptID, expectedReceiptHash = headReceiptID, headReceiptHash
		}
		_, err := database.ExecContext(ctx, `
INSERT INTO release_deployment_runs (
  id, schema_version, project_id, environment, operation,
  release_bundle_id, release_bundle_hash,
  preview_receipt_id, preview_receipt_hash,
  promotion_approval_id, promotion_approval_hash,
  expected_revision_id, expected_revision_hash,
  expected_production_receipt_id, expected_production_receipt_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES (
  $1, 'release-deployment-run/v1', $2, 'production', 'promote',
  $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
  $13, $14, 'production head fencing canary', 'queued', 1, $15, $15
)
`, runID, seed.projectID, bundleID, bundleHash,
			previous.previewReceiptID, previous.previewReceiptHash,
			previous.approvalID, previous.approvalHash,
			expectedRevisionID, expectedRevisionHash, expectedReceiptID, expectedReceiptHash,
			label+"-"+runID.String(), applicationBuildContractCanaryDigest(label+"-request"), seed.actorID)
		return err
	}

	if err := insertRun(uuid.New(), "stale-head", false); err == nil || !strings.Contains(err.Error(), "expected production head is stale") {
		t.Fatalf("production Run without the exact current head was accepted: %v", err)
	}

	gormDatabase, err := gorm.Open(postgres.New(postgres.Config{Conn: database}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	productionStore, err := release.NewStore(gormDatabase, releaseHeadContentStub{}, releaseHeadReceiptStub{})
	if err != nil {
		t.Fatal(err)
	}
	// These store calls model two API replicas that observed the same head. The
	// transaction's head-row lock and partial unique index must allow exactly
	// one to commit, while the winner persists the exact expected head.
	type insertOutcome struct {
		run   release.ProductionRun
		input release.CreateProductionRunInput
		err   error
	}
	outcomes := make(chan insertOutcome, 2)
	for index := 0; index < 2; index++ {
		runID := uuid.New()
		label := "concurrent-head-" + string(rune('a'+index))
		go func() {
			input := release.CreateProductionRunInput{
				ID: runID.String(), ProjectID: seed.projectID.String(), Environment: "production",
				Operation:         release.DeploymentPromote,
				ReleaseBundle:     repository.ExactReference{ID: bundleID.String(), ContentHash: bundleHash},
				PreviewReceipt:    repository.ExactReference{ID: previous.previewReceiptID.String(), ContentHash: previous.previewReceiptHash},
				PromotionApproval: repository.ExactReference{ID: previous.approvalID.String(), ContentHash: previous.approvalHash},
				RequestKey:        label + "-" + runID.String(), RequestHash: applicationBuildContractCanaryDigest(label + "-request"),
				Reason: "production head fencing canary", CreatedBy: seed.actorID.String(),
			}
			run, _, createErr := productionStore.CreateProductionRun(ctx, input)
			outcomes <- insertOutcome{run: run, input: input, err: createErr}
		}()
	}
	var winningRunID uuid.UUID
	var winningInput release.CreateProductionRunInput
	successes, conflicts := 0, 0
	for index := 0; index < 2; index++ {
		outcome := <-outcomes
		if outcome.err == nil {
			successes++
			winningRunID = uuid.MustParse(outcome.run.ID)
			winningInput = outcome.input
			if outcome.run.Environment != "production" || outcome.run.ExpectedRevision == nil ||
				*outcome.run.ExpectedRevision != (repository.ExactReference{ID: headRevisionID.String(), ContentHash: headRevisionHash}) ||
				outcome.run.ExpectedReceipt == nil ||
				*outcome.run.ExpectedReceipt != (repository.ExactReference{ID: headReceiptID.String(), ContentHash: headReceiptHash}) {
				t.Fatalf("winning Run did not persist the exact expected head: %+v", outcome.run)
			}
		} else if errors.Is(outcome.err, release.ErrProductionHeadConflict) {
			conflicts++
		} else {
			t.Fatalf("concurrent production insert failed for an unexpected reason: %v", outcome.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent production serialization successes=%d conflicts=%d", successes, conflicts)
	}

	claimReleaseDeliveryRun(t, ctx, database, "release_deployment_runs", winningRunID, seed.actorID)
	receiptID, revisionID := uuid.New(), uuid.New()
	receiptHash := applicationBuildContractCanaryDigest("head-cas-production-receipt")
	revisionHash := applicationBuildContractCanaryDigest("head-cas-deployment-revision")
	checks := `[{"id":"readiness","kind":"health","status":"passed"},{"id":"rollout-health","kind":"rollout","status":"passed"}]`
	insertProductionReceiptCanary(
		t, ctx, database, seed, winningRunID, receiptID, receiptHash,
		bundleID, bundleHash, previous.previewReceiptID, previous.previewReceiptHash,
		previous.approvalID, previous.approvalHash, "promote", nil, nil,
		"https://production-head.example", checks, "passed",
	)
	if err := insertDeploymentRevisionCanary(
		ctx, database, seed, winningRunID, revisionID, revisionHash,
		bundleID, bundleHash, previous.previewReceiptID, previous.previewReceiptHash,
		previous.approvalID, previous.approvalHash, receiptID, receiptHash,
		"promote", nil, nil, "https://production-head.example", checks,
	); err != nil {
		t.Fatalf("insert head CAS DeploymentRevision: %v", err)
	}

	if _, err := database.ExecContext(ctx, `
UPDATE release_deployment_runs
SET state = 'healthy', version = 5, lease_worker_id = NULL, lease_epoch = NULL,
    lease_expires_at = NULL, finished_at = statement_timestamp(), updated_by = $2
WHERE id = $1
`, winningRunID, seed.actorID); err == nil || !strings.Contains(err.Error(), "committed production head CAS") {
		t.Fatalf("healthy Run bypassed production head CAS: %v", err)
	}

	result, err := database.ExecContext(ctx, `
UPDATE release_production_heads
SET deployment_revision_id = $1, deployment_revision_hash = $2,
    production_receipt_id = $3, production_receipt_hash = $4,
    generation = generation + 1, updated_by = $5, updated_at = statement_timestamp()
WHERE project_id = $6 AND environment = 'production'
  AND deployment_revision_id = $7 AND deployment_revision_hash = $8
  AND production_receipt_id = $9 AND production_receipt_hash = $10
`, revisionID, revisionHash, receiptID, receiptHash, seed.actorID, seed.projectID,
		headRevisionID, headRevisionHash, headReceiptID, headReceiptHash)
	if err != nil {
		t.Fatalf("advance production head by exact CAS: %v", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		t.Fatalf("production head CAS affected %d rows, want 1", affected)
	}
	terminalReleaseDeliveryRun(t, ctx, database, "release_deployment_runs", winningRunID, "healthy", seed.actorID)

	result, err = database.ExecContext(ctx, `
UPDATE release_production_heads
SET deployment_revision_id = $1, deployment_revision_hash = $2,
    production_receipt_id = $3, production_receipt_hash = $4,
    generation = generation + 1, updated_by = $5, updated_at = statement_timestamp()
WHERE project_id = $6 AND environment = 'production'
  AND deployment_revision_id = $7 AND deployment_revision_hash = $8
  AND production_receipt_id = $9 AND production_receipt_hash = $10
`, previous.revisionID, previous.revisionHash, previous.receiptID, previous.receiptHash,
		seed.actorID, seed.projectID, headRevisionID, headRevisionHash, headReceiptID, headReceiptHash)
	if err != nil {
		t.Fatalf("stale production head CAS query failed instead of fencing: %v", err)
	}
	if affected, _ := result.RowsAffected(); affected != 0 {
		t.Fatalf("stale production head CAS affected %d rows, want 0", affected)
	}

	var persistedGeneration int64
	if err := database.QueryRowContext(ctx, `
SELECT generation FROM release_production_heads
WHERE project_id = $1 AND environment = 'production'
  AND deployment_revision_id = $2 AND deployment_revision_hash = $3
  AND production_receipt_id = $4 AND production_receipt_hash = $5
`, seed.projectID, revisionID, revisionHash, receiptID, receiptHash).Scan(&persistedGeneration); err != nil {
		t.Fatalf("load advanced production head: %v", err)
	}
	if persistedGeneration != 2 {
		t.Fatalf("advanced production head generation=%d, want 2", persistedGeneration)
	}
	var historicalCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM release_deployment_revisions
WHERE (id = $1 AND payload_hash = $2) OR (id = $3 AND payload_hash = $4)
`, previous.revisionID, previous.revisionHash, revisionID, revisionHash).Scan(&historicalCount); err != nil {
		t.Fatal(err)
	}
	if historicalCount != 2 {
		t.Fatalf("head advance overwrote immutable deployment history; count=%d", historicalCount)
	}

	nextRunID := uuid.New()
	nextInput := release.CreateProductionRunInput{
		ID: nextRunID.String(), ProjectID: seed.projectID.String(), Environment: "production",
		Operation:         release.DeploymentPromote,
		ReleaseBundle:     repository.ExactReference{ID: bundleID.String(), ContentHash: bundleHash},
		PreviewReceipt:    repository.ExactReference{ID: previous.previewReceiptID.String(), ContentHash: previous.previewReceiptHash},
		PromotionApproval: repository.ExactReference{ID: previous.approvalID.String(), ContentHash: previous.approvalHash},
		RequestKey:        "next-active-" + nextRunID.String(), RequestHash: applicationBuildContractCanaryDigest("next-active-request"),
		Reason: "next active production Run", CreatedBy: seed.actorID.String(),
	}
	if _, replayed, err := productionStore.CreateProductionRun(ctx, nextInput); err != nil || replayed {
		t.Fatalf("create next production Run: replayed=%v err=%v", replayed, err)
	}
	replayedRun, replayed, err := productionStore.CreateProductionRun(ctx, winningInput)
	if err != nil || !replayed || replayedRun.ID != winningRunID.String() || replayedRun.State != release.DeliveryHealthy {
		t.Fatalf("idempotent replay changed behind a different active Run: run=%+v replayed=%v err=%v", replayedRun, replayed, err)
	}
}

func claimReleaseDeliveryRun(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	table string,
	runID uuid.UUID,
	actorID uuid.UUID,
) {
	t.Helper()
	if table != "release_preview_runs" && table != "release_deployment_runs" {
		t.Fatalf("unsupported delivery Run table %q", table)
	}
	if _, err := database.ExecContext(ctx, `UPDATE `+table+`
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = 'delivery-canary', lease_epoch = 1,
    lease_expires_at = statement_timestamp() + interval '5 minutes',
    started_at = statement_timestamp(), updated_by = $2
WHERE id = $1`, runID, actorID); err != nil {
		t.Fatalf("claim %s: %v", table, err)
	}
	for version, state := range []string{"deploying", "verifying"} {
		if _, err := database.ExecContext(ctx, `UPDATE `+table+`
SET state = $2, version = $3, updated_by = $4 WHERE id = $1`,
			runID, state, version+3, actorID); err != nil {
			t.Fatalf("transition %s to %s: %v", table, state, err)
		}
	}
}

func terminalReleaseDeliveryRun(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	table string,
	runID uuid.UUID,
	state string,
	actorID uuid.UUID,
) {
	t.Helper()
	if _, err := database.ExecContext(ctx, `UPDATE `+table+`
SET state = $2, version = 5, lease_worker_id = NULL, lease_epoch = NULL,
    lease_expires_at = NULL, finished_at = statement_timestamp(), updated_by = $3
WHERE id = $1`, runID, state, actorID); err != nil {
		t.Fatalf("terminalize %s as %s: %v", table, state, err)
	}
}

func insertProductionRunCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	seed repositoryCandidateCanarySeed,
	bundleID uuid.UUID,
	bundleHash string,
	previewReceiptID uuid.UUID,
	previewReceiptHash string,
	approvalID uuid.UUID,
	approvalHash string,
	operation string,
	sourceID *uuid.UUID,
	sourceHash *string,
	label string,
) uuid.UUID {
	t.Helper()
	runID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO release_deployment_runs (
  id, schema_version, project_id, operation, release_bundle_id, release_bundle_hash,
  preview_receipt_id, preview_receipt_hash, promotion_approval_id, promotion_approval_hash,
  source_revision_id, source_revision_hash, request_key, request_hash, reason,
  state, version, created_by, updated_by
) VALUES ($1, 'release-deployment-run/v1', $2, $3, $4, $5, $6, $7, $8, $9,
          $10, $11, $12, $13, $14, 'queued', 1, $15, $15)
`, runID, seed.projectID, operation, bundleID, bundleHash, previewReceiptID, previewReceiptHash,
		approvalID, approvalHash, sourceID, sourceHash, label+"-"+runID.String(),
		applicationBuildContractCanaryDigest(label+"-request"), label, seed.actorID); err != nil {
		t.Fatalf("insert production Run %s: %v", label, err)
	}
	return runID
}

func insertProductionReceiptCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	seed repositoryCandidateCanarySeed,
	runID uuid.UUID,
	receiptID uuid.UUID,
	receiptHash string,
	bundleID uuid.UUID,
	bundleHash string,
	previewReceiptID uuid.UUID,
	previewReceiptHash string,
	approvalID uuid.UUID,
	approvalHash string,
	operation string,
	sourceID *uuid.UUID,
	sourceHash *string,
	publicURL string,
	checks string,
	decision string,
) {
	t.Helper()
	if _, err := database.ExecContext(ctx, `
INSERT INTO release_production_receipts (
  id, schema_version, run_id, project_id, operation,
  release_bundle_id, release_bundle_hash, preview_receipt_id, preview_receipt_hash,
  promotion_approval_id, promotion_approval_hash, source_revision_id, source_revision_hash,
  provider, provider_ref, public_url, checks, decision,
  content_store, content_ref, content_hash, payload_hash, created_by
) VALUES ($1, 'release-production-receipt/v1', $2, $3, $4, $5, $6, $7, $8, $9,
          $10, $11, $12, 'canary', $13, $14, $15::jsonb, $16,
          'blob', $17, $18, $19, $20)
`, receiptID, runID, seed.projectID, operation, bundleID, bundleHash, previewReceiptID,
		previewReceiptHash, approvalID, approvalHash, sourceID, sourceHash,
		"production/ref/"+runID.String(), publicURL, checks, decision,
		"blob://production-receipt/"+receiptID.String(), applicationBuildContractCanaryDigest("content-"+receiptID.String()),
		receiptHash, seed.actorID); err != nil {
		t.Fatalf("insert %s ProductionReceipt: %v", decision, err)
	}
}

func insertDeploymentRevisionCanary(
	ctx context.Context,
	database *sql.DB,
	seed repositoryCandidateCanarySeed,
	runID uuid.UUID,
	revisionID uuid.UUID,
	revisionHash string,
	bundleID uuid.UUID,
	bundleHash string,
	previewReceiptID uuid.UUID,
	previewReceiptHash string,
	approvalID uuid.UUID,
	approvalHash string,
	productionReceiptID uuid.UUID,
	productionReceiptHash string,
	operation string,
	sourceID *uuid.UUID,
	sourceHash *string,
	publicURL string,
	checks string,
) error {
	_, err := database.ExecContext(ctx, `
INSERT INTO release_deployment_revisions (
  id, schema_version, run_id, project_id, operation,
  release_bundle_id, release_bundle_hash, preview_receipt_id, preview_receipt_hash,
  promotion_approval_id, promotion_approval_hash, production_receipt_id, production_receipt_hash,
  source_revision_id, source_revision_hash, provider, provider_ref, public_url, checks,
  content_store, content_ref, content_hash, payload_hash, created_by
) VALUES ($1, 'release-deployment-revision/v1', $2, $3, $4, $5, $6, $7, $8, $9,
          $10, $11, $12, $13, $14, 'canary', $15, $16, $17::jsonb,
          'blob', $18, $19, $20, $21)
`, revisionID, runID, seed.projectID, operation, bundleID, bundleHash, previewReceiptID,
		previewReceiptHash, approvalID, approvalHash, productionReceiptID, productionReceiptHash,
		sourceID, sourceHash, "production/ref/"+runID.String(), publicURL, checks,
		"blob://deployment-revision/"+revisionID.String(), applicationBuildContractCanaryDigest("content-"+revisionID.String()),
		revisionHash, seed.actorID)
	return err
}
