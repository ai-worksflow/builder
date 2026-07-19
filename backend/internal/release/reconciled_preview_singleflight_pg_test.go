package release

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
	"gorm.io/gorm"
)

func TestReleaseBundleStorePostgresPreservesCanonicalCreatedAt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	database, contents, receipt, seededBundle, cleanup := reconciledDeliveryPostgresFixture(t, ctx)
	defer cleanup()

	// The shared fixture seeds downstream lineage with replica mode because its
	// purpose is delivery reconciliation.  Establish the missing Canonical Run
	// and remove only its pre-seeded Bundle under that fixture boundary.  The
	// Store.Create call below runs with every normal trigger enabled.
	if err := database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Exec(`SET LOCAL session_replication_role = replica`).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO canonical_verification_runs (
  id, schema_version, project_id, plan_id, plan_hash,
  request_key, request_hash, reason, state, version, fence_epoch,
  finished_at, created_by, updated_by, created_at, updated_at
) VALUES (?, 'canonical-verification-run/v1', ?, ?, ?, ?, ?, ?, 'passed', 1, 0,
          ?, ?, ?, ?, ?)
`, receipt.RunID, receipt.ProjectID, receipt.Plan.ID, receipt.Plan.ContentHash,
			"bundle-created-at-"+receipt.RunID, reconciledDeliveryDigest("bundle-created-at-run"),
			"establish exact passing Canonical authority for Bundle timestamp regression",
			receipt.CreatedAt, receipt.CreatedBy, receipt.CreatedBy, receipt.CreatedAt, receipt.CreatedAt).Error; err != nil {
			return err
		}
		return transaction.Exec(`DELETE FROM release_bundles WHERE id = ?`, seededBundle.ID).Error
	}); err != nil {
		t.Fatalf("prepare exact Canonical lineage fixture: %v", err)
	}

	store, err := NewStore(database, contents, reconciledDeliveryReceiptReader{receipt: receipt})
	if err != nil {
		t.Fatal(err)
	}
	fixedCreatedAt := time.Date(2024, time.February, 3, 4, 5, 6, 789123000, time.UTC)
	store.now = func() time.Time { return fixedCreatedAt }
	bundle, err := store.Create(
		ctx, receipt.ProjectID, receipt.ID, receipt.PayloadHash, uuid.NewString(), receipt.CreatedBy,
	)
	if err != nil {
		t.Fatalf("Store.Create with normal ReleaseBundle trigger: %v", err)
	}
	if !bundle.CreatedAt.Equal(fixedCreatedAt) {
		t.Fatalf("canonical Bundle createdAt=%s, want %s", bundle.CreatedAt, fixedCreatedAt)
	}
	var projectedCreatedAt time.Time
	var creationTransactionID int64
	if err := database.WithContext(ctx).Raw(`
SELECT created_at, creation_transaction_id
FROM release_bundles
WHERE id = ? AND bundle_hash = ?
`, bundle.ID, bundle.BundleHash).Row().Scan(&projectedCreatedAt, &creationTransactionID); err != nil {
		t.Fatalf("load Bundle SQL timestamp projection: %v", err)
	}
	if !projectedCreatedAt.Equal(fixedCreatedAt) || creationTransactionID <= 0 {
		t.Fatalf("Bundle SQL createdAt=%s transaction=%d, want canonical %s and database transaction", projectedCreatedAt, creationTransactionID, fixedCreatedAt)
	}

	artifacts, err := json.Marshal(receipt.ReleaseArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		createdAt time.Time
	}{
		{name: "zero", createdAt: time.Time{}},
		{name: "future", createdAt: time.Now().UTC().Add(24 * time.Hour)},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalidID := uuid.NewString()
			err := database.WithContext(ctx).Exec(`
INSERT INTO release_bundles (
  id, schema_version, project_id,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  canonical_receipt_id, canonical_receipt_hash, release_artifacts,
  content_store, content_ref, content_hash, bundle_hash, created_by, created_at
) VALUES (?, 'release-bundle/v1', ?, ?, ?, ?, ?, ?, ?::jsonb,
          'mongo', ?, ?, ?, ?, ?)
`, invalidID, receipt.ProjectID,
				receipt.Subject.WorkspaceArtifactID, receipt.Subject.WorkspaceRevisionID, receipt.Subject.WorkspaceContentHash,
				receipt.ID, receipt.PayloadHash, string(artifacts),
				"invalid-bundle-created-at-"+invalidID,
				reconciledDeliveryDigest("invalid-bundle-content-"+invalidID),
				reconciledDeliveryDigest("invalid-bundle-hash-"+invalidID), receipt.CreatedBy, test.createdAt).Error
			if err == nil || !strings.Contains(err.Error(), "explicit nonzero canonical time") {
				t.Fatalf("ReleaseBundle %s createdAt error=%v, want trigger rejection", test.name, err)
			}
		})
	}
}

func TestReconciledPreviewSingleFlightPostgresSerializesExactBundleLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	database, contents, receipt, bundle, cleanup := reconciledDeliveryPostgresFixture(t, ctx)
	defer cleanup()
	baseStore, err := NewStore(database, contents, reconciledDeliveryReceiptReader{receipt: receipt})
	if err != nil {
		t.Fatal(err)
	}
	controller := DeliveryControllerIdentity{
		SchemaVersion:  DeliveryControllerIdentitySchemaVersion,
		ID:             "postgres-preview-singleflight-controller",
		Version:        "2026.07.18+singleflight",
		Protocol:       DeliveryControllerProtocolV3,
		TrustKeyDigest: reconciledDeliveryDigest("preview-singleflight-controller-spki"),
	}
	store, err := NewReconciledDeliveryStore(baseStore, controller)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Readiness(ctx); err != nil {
		t.Fatalf("reconciled store readiness: %v", err)
	}

	inputs := []CreatePreviewRunInput{
		reconciledPreviewSingleFlightInput(bundle, "concurrent-a"),
		reconciledPreviewSingleFlightInput(bundle, "concurrent-b"),
	}
	type createResult struct {
		input    CreatePreviewRunInput
		run      PreviewRun
		replayed bool
		err      error
	}
	start := make(chan struct{})
	results := make(chan createResult, len(inputs))
	var wait sync.WaitGroup
	for _, input := range inputs {
		input := input
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			run, replayed, createErr := store.CreatePreviewRun(ctx, input)
			results <- createResult{input: input, run: run, replayed: replayed, err: createErr}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	var winner createResult
	successes, conflicts := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			winner = result
			if result.replayed || result.run.State != DeliveryQueued {
				t.Fatalf("concurrent Preview winner=%+v replayed=%v", result.run, result.replayed)
			}
		case errors.Is(result.err, ErrPreviewRunConflict):
			conflicts++
		default:
			t.Fatalf("concurrent Preview loser returned unclassified error: %v", result.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent exact Bundle results successes=%d conflicts=%d", successes, conflicts)
	}

	var runCount, operationCount int64
	if err := database.WithContext(ctx).Raw(`
SELECT
  (SELECT count(*) FROM release_preview_runs
    WHERE project_id = ? AND release_bundle_id = ? AND release_bundle_hash = ?),
  (SELECT count(*) FROM release_delivery_operations AS operation
    JOIN release_preview_runs AS run ON run.id = operation.preview_run_id
    WHERE run.project_id = ? AND run.release_bundle_id = ? AND run.release_bundle_hash = ?)
`, bundle.ProjectID, bundle.ID, bundle.BundleHash,
		bundle.ProjectID, bundle.ID, bundle.BundleHash).Row().Scan(&runCount, &operationCount); err != nil {
		t.Fatalf("count exact Preview authority: %v", err)
	}
	if runCount != 1 || operationCount != 1 {
		t.Fatalf("concurrent exact Bundle persisted Runs=%d Operations=%d, want one atomic pair", runCount, operationCount)
	}

	replayed, wasReplay, err := store.CreatePreviewRun(ctx, winner.input)
	if err != nil || !wasReplay || replayed.ID != winner.run.ID {
		t.Fatalf("same request must replay its owning Run: run=%+v replayed=%v err=%v", replayed, wasReplay, err)
	}

	firstReceipt := completeSingleFlightPreview(t, ctx, store, controller, winner.run.ID)
	if firstReceipt.Decision != PreviewPassed {
		t.Fatalf("first exact Preview decision=%s, want passed", firstReceipt.Decision)
	}

	uncertainInput := reconciledPreviewSingleFlightInput(bundle, "uncertain-owner")
	uncertain, wasReplay, err := store.CreatePreviewRun(ctx, uncertainInput)
	if err != nil || wasReplay || uncertain.State != DeliveryQueued {
		t.Fatalf("terminal Preview must release exact Bundle lock: run=%+v replayed=%v err=%v", uncertain, wasReplay, err)
	}
	claim, err := store.ClaimDeliveryOperation(ctx, "preview-singleflight-submitter", 30*time.Second)
	if err != nil || claim == nil || claim.Preview == nil || claim.Preview.Run.ID != uncertain.ID {
		t.Fatalf("claim uncertain Preview: claim=%+v err=%v", claim, err)
	}
	attempt, err := store.BeginDeliveryAttempt(ctx, *claim, DeliveryAttemptSubmit)
	if err != nil {
		t.Fatalf("begin uncertain Preview submission: %v", err)
	}
	if err := store.RecordDeliveryUnknown(
		ctx, *claim, attempt, "controller-outcome-unknown", "lost response after PUT",
		time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond),
	); err != nil {
		t.Fatalf("record uncertain Preview outcome: %v", err)
	}
	assertPreviewSingleFlightConflict(t, ctx, store, bundle, "while-reconcile-wait")

	reconcile, err := store.ClaimDeliveryOperation(ctx, "preview-singleflight-reconciler", 30*time.Second)
	if err != nil || reconcile == nil || reconcile.Preview == nil || reconcile.Preview.Run.ID != uncertain.ID ||
		reconcile.RunState != DeliveryReconciling {
		t.Fatalf("claim uncertain Preview reconciliation: claim=%+v err=%v", reconcile, err)
	}
	reconcileAttempt, err := store.BeginDeliveryAttempt(ctx, *reconcile, DeliveryAttemptReconcile)
	if err != nil {
		t.Fatalf("begin uncertain Preview reconciliation: %v", err)
	}
	if err := store.QuarantineDeliveryOperation(
		ctx, *reconcile, reconcileAttempt,
		"controller-authority-conflict", "exact controller authority cannot be reconciled",
	); err != nil {
		t.Fatalf("quarantine uncertain Preview authority: %v", err)
	}
	assertPreviewSingleFlightConflict(t, ctx, store, bundle, "while-reconcile-blocked")

	blocked, err := store.GetPreviewRun(ctx, bundle.ProjectID, uncertain.ID)
	if err != nil || blocked.State != DeliveryReconcileBlocked {
		t.Fatalf("blocked Preview remains visible and owns single-flight: run=%+v err=%v", blocked, err)
	}
	assertProductionSingleFlightUnchanged(t, ctx, database)
}

func reconciledPreviewSingleFlightInput(bundle Bundle, label string) CreatePreviewRunInput {
	runID := uuid.NewString()
	return CreatePreviewRunInput{
		ID: runID, ProjectID: bundle.ProjectID,
		ReleaseBundle:  repository.ExactReference{ID: bundle.ID, ContentHash: bundle.BundleHash},
		BundleDocument: bundle,
		RequestKey:     "preview-singleflight-" + label + "-" + runID,
		RequestHash:    reconciledDeliveryDigest("preview-singleflight-api-" + label + "-" + runID),
		Reason:         "exercise preview single-flight " + label,
		CreatedBy:      bundle.CreatedBy,
	}
}

func completeSingleFlightPreview(
	t *testing.T,
	ctx context.Context,
	store *ReconciledDeliveryStore,
	controller DeliveryControllerIdentity,
	runID string,
) PreviewReceipt {
	t.Helper()
	claim, err := store.ClaimDeliveryOperation(ctx, "preview-singleflight-completer", 30*time.Second)
	if err != nil || claim == nil || claim.Preview == nil || claim.Preview.Run.ID != runID {
		t.Fatalf("claim Preview completion: claim=%+v err=%v", claim, err)
	}
	attempt, err := store.BeginDeliveryAttempt(ctx, *claim, DeliveryAttemptSubmit)
	if err != nil {
		t.Fatalf("begin Preview completion Attempt: %v", err)
	}
	result := reconciledDeliveryCompletedResult(
		t, controller, claim.Request, passingReleasePreviewChecks(), "", nil,
	)
	observation := DeliveryOperationObservation{
		SchemaVersion: DeliveryOperationObservationSchema,
		Controller:    controller,
		OperationID:   claim.Request.OperationID,
		RequestHash:   claim.Request.RequestHash,
		State:         DeliveryRemoteCompleted,
		Sequence:      1,
		ObservedAt:    result.CompletedAt.Add(time.Microsecond),
		Result:        &result,
	}
	if err := store.RecordDeliveryObservation(ctx, *claim, attempt, observation, time.Time{}); err != nil {
		t.Fatalf("record completed Preview Result: %v", err)
	}
	if err := store.FinalizeDeliveryOperation(ctx, *claim); err != nil {
		t.Fatalf("finalize completed Preview: %v", err)
	}
	run, err := store.GetPreviewRun(ctx, claim.Preview.Run.ProjectID, runID)
	if err != nil || run.Receipt == nil {
		t.Fatalf("load completed Preview Run: run=%+v err=%v", run, err)
	}
	receipt, err := store.GetPreviewReceipt(
		ctx, run.ProjectID, run.Receipt.ID, run.Receipt.ContentHash,
	)
	if err != nil {
		t.Fatalf("load completed PreviewReceipt: %v", err)
	}
	return receipt
}

func assertPreviewSingleFlightConflict(
	t *testing.T,
	ctx context.Context,
	store *ReconciledDeliveryStore,
	bundle Bundle,
	label string,
) {
	t.Helper()
	_, _, err := store.CreatePreviewRun(ctx, reconciledPreviewSingleFlightInput(bundle, label))
	if !errors.Is(err, ErrPreviewRunConflict) {
		t.Fatalf("%s create error=%v, want ErrPreviewRunConflict", label, err)
	}
}

func assertProductionSingleFlightUnchanged(t *testing.T, ctx context.Context, database *gorm.DB) {
	t.Helper()
	var definition string
	if err := database.WithContext(ctx).Raw(`
SELECT indexdef
FROM pg_indexes
WHERE schemaname = current_schema()
  AND indexname = 'release_deployment_runs_one_nonterminal_environment_idx'
`).Row().Scan(&definition); err != nil {
		t.Fatalf("load production single-flight definition: %v", err)
	}
	for _, state := range []string{"queued", "reconcile_wait", "reconcile_blocked"} {
		if !strings.Contains(definition, state) {
			t.Fatalf("production single-flight lost %s authority: %s", state, definition)
		}
	}
}
