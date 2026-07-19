package release

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
	"gorm.io/gorm"
)

func TestGovernedBlockedDeliveryReconciliationPostgresClosesPreviewWithGETOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	database, contents, receipt, bundle, cleanup := reconciledDeliveryPostgresFixture(t, ctx)
	defer cleanup()
	if err := database.WithContext(ctx).Exec(`
INSERT INTO project_members (project_id, user_id, role) VALUES (?, ?, 'owner')
`, bundle.ProjectID, bundle.CreatedBy).Error; err != nil {
		t.Fatal(err)
	}
	base, err := NewStore(database, contents, reconciledDeliveryReceiptReader{receipt: receipt})
	if err != nil {
		t.Fatal(err)
	}
	controller := DeliveryControllerIdentity{
		SchemaVersion: DeliveryControllerIdentitySchemaVersion,
		ID:            "operator-reconciliation-controller", Version: "2026.07.18+repaired",
		Protocol:       DeliveryControllerProtocolV3,
		TrustKeyDigest: reconciledDeliveryDigest("operator-reconciliation-controller-spki"),
	}
	store, err := NewReconciledDeliveryStore(base, controller)
	if err != nil {
		t.Fatal(err)
	}

	blocked, request := quarantinePreviewForOperatorReconciliation(t, ctx, store, bundle, "operator-main")
	snapshot, err := base.GetBlockedDeliveryReconciliationSnapshot(
		ctx, bundle.ProjectID, DeliveryOperationPreview, blocked.ID,
	)
	if err != nil {
		t.Fatalf("read blocked reconciliation CAS snapshot during worker maintenance: %v", err)
	}
	if snapshot.ExpectedRunVersion != blocked.Version || snapshot.OperationID != request.OperationID ||
		snapshot.OperationRequestHash != request.RequestHash ||
		snapshot.LastError.Code != "controller-authority-conflict" || snapshot.Controller != controller {
		t.Fatalf("blocked snapshot lost exact authority: %+v", snapshot)
	}

	operationBypass := database.WithContext(ctx).Exec(`
UPDATE release_delivery_operations
SET remote_state = 'submit_unknown', next_attempt_at = statement_timestamp(),
    last_error_code = NULL, last_error_detail = NULL,
    updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ?
`, request.OperationID)
	if operationBypass.Error == nil {
		t.Fatal("raw quarantined Operation update succeeded without immutable reconciliation Case")
	}
	runBypass := database.WithContext(ctx).Exec(`
UPDATE release_preview_runs
SET state = 'reconcile_wait', version = version + 1,
    updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ?
`, blocked.ID)
	if runBypass.Error == nil {
		t.Fatal("raw reconcile_blocked Run update succeeded without immutable reconciliation Case")
	}

	caseInput := operatorReconciliationInput(t, snapshot, bundle.CreatedBy, "resume-operator-main", "controller history was repaired")
	base.now = func() time.Time { return time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC) }
	resolution, replayed, err := store.ResumeBlockedDelivery(ctx, caseInput)
	if err != nil || replayed {
		t.Fatalf("resume blocked exact Operation: case=%+v replayed=%v err=%v", resolution, replayed, err)
	}
	var databaseNow time.Time
	if err := database.WithContext(ctx).Raw(`SELECT statement_timestamp()`).Row().Scan(&databaseNow); err != nil {
		t.Fatal(err)
	}
	if resolution.CreatedAt.After(databaseNow.Add(time.Second)) || resolution.CreatedAt.Year() == 2099 {
		t.Fatalf("reconciliation audit time trusted skewed app clock: case=%s db=%s", resolution.CreatedAt, databaseNow)
	}
	replayedCase, replayed, err := store.ResumeBlockedDelivery(ctx, caseInput)
	if err != nil || !replayed || replayedCase.CaseHash != resolution.CaseHash {
		t.Fatalf("deterministic reconciliation replay: case=%+v replayed=%v err=%v", replayedCase, replayed, err)
	}
	conflicting := caseInput
	conflicting.RequestHash = reconciledDeliveryDigest("different-idempotency-payload")
	if _, _, err := store.ResumeBlockedDelivery(ctx, conflicting); !errors.Is(err, ErrDeliveryReconciliationConflict) {
		t.Fatalf("same key with different payload err=%v, want conflict", err)
	}

	var runState, remoteState string
	var runVersion uint64
	if err := database.WithContext(ctx).Raw(`
SELECT run.state, run.version, operation.remote_state
FROM release_preview_runs AS run
JOIN release_delivery_operations AS operation ON operation.preview_run_id = run.id
WHERE run.id = ?
`, blocked.ID).Row().Scan(&runState, &runVersion, &remoteState); err != nil ||
		runState != string(DeliveryReconcileWait) || runVersion != blocked.Version+1 || remoteState != "submit_unknown" {
		t.Fatalf("resolution projection state=%s version=%d remote=%s err=%v", runState, runVersion, remoteState, err)
	}

	provider := &fakeDeliveryOperationProvider{}
	provider.reconcile = func(_ context.Context, got DeliveryOperationRequest) (DeliveryOperationObservation, error) {
		if got.OperationID != request.OperationID || got.RequestHash != request.RequestHash {
			t.Fatalf("GET changed exact Operation: got=%s@%s want=%s@%s",
				got.OperationID, got.RequestHash, request.OperationID, request.RequestHash)
		}
		result := reconciledDeliveryCompletedResult(t, controller, got, passingReleasePreviewChecks(), "", nil)
		return DeliveryOperationObservation{
			SchemaVersion: DeliveryOperationObservationSchema, Controller: controller,
			OperationID: got.OperationID, RequestHash: got.RequestHash,
			State: DeliveryRemoteCompleted, Sequence: 1,
			ObservedAt: result.CompletedAt.Add(time.Microsecond), Result: &result,
		}, nil
	}
	provider.submit = func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error) {
		t.Fatal("operator-reconciled Operation was submitted instead of reconciled with GET")
		return DeliveryOperationObservation{}, nil
	}
	worker, err := NewReconciledDeliveryWorker(store, provider, "operator-get-only-worker", 30*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	worked, err := worker.RunOne(ctx)
	if err != nil || !worked {
		t.Fatalf("GET exact reconciled Result and finalize: worked=%v err=%v", worked, err)
	}
	if len(provider.reconcileRequests) != 1 || len(provider.submitRequests) != 0 {
		t.Fatalf("controller calls GET=%d PUT=%d", len(provider.reconcileRequests), len(provider.submitRequests))
	}
	completed, err := store.GetPreviewRun(ctx, bundle.ProjectID, blocked.ID)
	if err != nil || completed.State != DeliveryPassed || completed.Receipt == nil {
		t.Fatalf("operator-reconciled preview did not finalize: run=%+v err=%v", completed, err)
	}

	loaded, err := base.GetDeliveryReconciliationCase(ctx, bundle.ProjectID, resolution.ID)
	if err != nil || loaded.CaseHash != resolution.CaseHash {
		t.Fatalf("maintenance read of immutable Case=%+v err=%v", loaded, err)
	}
	cases, err := base.ListDeliveryReconciliationCases(ctx, bundle.ProjectID, 10)
	if err != nil || len(cases) != 1 || cases[0].ID != resolution.ID {
		t.Fatalf("maintenance audit Cases=%+v err=%v", cases, err)
	}
	maintenanceAccess := &deliveryAccessFake{}
	maintenanceService, err := NewDeliveryService(base, maintenanceAccess)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maintenanceService.ListDeliveryReconciliationCases(ctx, bundle.ProjectID, bundle.CreatedBy); err != nil ||
		maintenanceAccess.action != "view" {
		t.Fatalf("worker-disabled immutable audit err=%v action=%q", err, maintenanceAccess.action)
	}
	if _, _, err := maintenanceService.ResumeBlockedDelivery(ctx, ResumeBlockedDeliveryRequest{
		ProjectID: bundle.ProjectID, RunKind: DeliveryOperationPreview, RunID: blocked.ID,
		ExpectedVersion: blocked.Version, ExpectedErrorCode: "controller-authority-conflict",
		Reason: "must remain disabled", ActorID: bundle.CreatedBy, OperationID: "maintenance-mutation",
	}); !errors.Is(err, ErrDeliveryReconciliationDisabled) {
		t.Fatalf("worker-disabled service exposed reconciliation mutation: %v", err)
	}
	if err := database.WithContext(ctx).Exec(`
UPDATE release_delivery_reconciliation_cases SET reason = 'rewritten' WHERE id = ?
`, resolution.ID).Error; err == nil {
		t.Fatal("immutable reconciliation Case was updated")
	}
	if err := database.WithContext(ctx).Exec(`
DELETE FROM release_delivery_reconciliation_cases WHERE id = ?
`, resolution.ID).Error; err == nil {
		t.Fatal("immutable reconciliation Case was deleted")
	}
	forged := database.WithContext(ctx).Exec(`
INSERT INTO release_delivery_reconciliation_cases
SELECT gen_random_uuid(), schema_version, project_id, run_kind, run_id, run_schema_version,
       expected_run_version + 100, operation_id, operation_request_hash,
       controller_schema_version, controller_id, controller_version, controller_protocol,
       controller_trust_key_digest, previous_remote_state, resume_remote_state,
       submit_attempt_count, reconcile_attempt_count,
       last_attempt_ordinal, last_attempt_kind, last_attempt_worker_id, last_attempt_fence_epoch,
       last_attempt_started_at, last_attempt_completed_at, last_attempt_outcome,
       last_observation_sequence, last_observed_at,
       quarantine_error_code, quarantine_error_detail, actor_id, reason,
       idempotency_key || '-forged', request_hash, case_document, case_hash, created_at, created_txid
FROM release_delivery_reconciliation_cases WHERE id = ?
`, resolution.ID)
	if forged.Error == nil {
		t.Fatal("forged reconciliation Case copy was accepted")
	}

	assertOperatorReconciliationRaceFailsClosed(t, ctx, database, store, bundle)
}

func quarantinePreviewForOperatorReconciliation(
	t *testing.T,
	ctx context.Context,
	store *ReconciledDeliveryStore,
	bundle Bundle,
	key string,
) (PreviewRun, DeliveryOperationRequest) {
	t.Helper()
	runID := uuid.NewString()
	_, _, err := store.CreatePreviewRun(ctx, CreatePreviewRunInput{
		ID: runID, ProjectID: bundle.ProjectID,
		ReleaseBundle:  repository.ExactReference{ID: bundle.ID, ContentHash: bundle.BundleHash},
		BundleDocument: bundle, RequestKey: key + "-" + runID,
		RequestHash: reconciledDeliveryDigest("api-" + key + "-" + runID),
		Reason:      "quarantine exact Operation for governed recovery", CreatedBy: bundle.CreatedBy,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDeliveryOperation(ctx, "quarantine-"+key, 30*time.Second)
	if err != nil || claim == nil || claim.Preview == nil || claim.Preview.Run.ID != runID {
		t.Fatalf("claim quarantine preview: claim=%+v err=%v", claim, err)
	}
	attempt, err := store.BeginDeliveryAttempt(ctx, *claim, DeliveryAttemptSubmit)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.QuarantineDeliveryOperation(
		ctx, *claim, attempt, "controller-authority-conflict", "pinned controller protocol history was unavailable",
	); err != nil {
		t.Fatal(err)
	}
	blocked, err := store.GetPreviewRun(ctx, bundle.ProjectID, runID)
	if err != nil || blocked.State != DeliveryReconcileBlocked {
		t.Fatalf("blocked preview=%+v err=%v", blocked, err)
	}
	return blocked, claim.Request
}

func operatorReconciliationInput(
	t *testing.T,
	snapshot DeliveryReconciliationBlockSnapshot,
	actorID, key, reason string,
) ResumeBlockedDeliveryInput {
	t.Helper()
	requestHash, err := deliveryRequestHash(map[string]any{
		"operation": "resume-blocked-delivery", "projectId": snapshot.ProjectID,
		"runKind": snapshot.RunKind, "runId": snapshot.RunID,
		"expectedVersion":   snapshot.ExpectedRunVersion,
		"expectedErrorCode": snapshot.LastError.Code, "reason": reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ResumeBlockedDeliveryInput{
		ID: uuid.NewString(), ProjectID: snapshot.ProjectID, RunKind: snapshot.RunKind,
		RunID: snapshot.RunID, ExpectedVersion: snapshot.ExpectedRunVersion,
		ExpectedErrorCode: snapshot.LastError.Code, Reason: reason, ActorID: actorID,
		IdempotencyKey: key, RequestHash: requestHash,
	}
}

func assertOperatorReconciliationRaceFailsClosed(
	t *testing.T,
	ctx context.Context,
	database *gorm.DB,
	store *ReconciledDeliveryStore,
	bundle Bundle,
) {
	t.Helper()
	blocked, request := quarantinePreviewForOperatorReconciliation(t, ctx, store, bundle, "operator-race")
	snapshot, err := store.GetBlockedDeliveryReconciliationSnapshot(
		ctx, bundle.ProjectID, DeliveryOperationPreview, blocked.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []ResumeBlockedDeliveryInput{
		operatorReconciliationInput(t, snapshot, bundle.CreatedBy, "operator-race-a", "first operator repair"),
		operatorReconciliationInput(t, snapshot, bundle.CreatedBy, "operator-race-b", "second operator repair"),
	}
	var wait sync.WaitGroup
	errorsSeen := make([]error, len(inputs))
	for index := range inputs {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, _, errorsSeen[index] = store.ResumeBlockedDelivery(ctx, inputs[index])
		}(index)
	}
	wait.Wait()
	succeeded, conflicted := 0, 0
	for _, err := range errorsSeen {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrDeliveryReconciliationConflict) {
			conflicted++
		} else {
			t.Fatalf("unexpected operator race error: %v", err)
		}
	}
	var caseCount int64
	if err := database.WithContext(ctx).Raw(`
SELECT count(*) FROM release_delivery_reconciliation_cases WHERE operation_id = ?
`, request.OperationID).Row().Scan(&caseCount); err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 || conflicted != 1 || caseCount != 1 {
		t.Fatalf("operator race succeeded=%d conflicted=%d Cases=%d errors=%v", succeeded, conflicted, caseCount, errorsSeen)
	}

	provider := &fakeDeliveryOperationProvider{
		reconcile: func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error) {
			return DeliveryOperationObservation{}, ErrDeliveryOperationNotFound
		},
		submit: func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error) {
			t.Fatal("operator-reconciled 404 must never cause PUT")
			return DeliveryOperationObservation{}, nil
		},
	}
	worker, err := NewReconciledDeliveryWorker(store, provider, "operator-404-get-only", 30*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := worker.RunOne(ctx); err != nil || !worked {
		t.Fatalf("GET 404 governed reconciliation: worked=%v err=%v", worked, err)
	}
	if len(provider.reconcileRequests) != 1 || len(provider.submitRequests) != 0 {
		t.Fatalf("GET 404 controller calls GET=%d PUT=%d", len(provider.reconcileRequests), len(provider.submitRequests))
	}
	reblocked, err := store.GetPreviewRun(ctx, bundle.ProjectID, blocked.ID)
	if err != nil || reblocked.State != DeliveryReconcileBlocked {
		t.Fatalf("GET 404 did not return to blocked: run=%+v err=%v", reblocked, err)
	}
	secondSnapshot, err := store.GetBlockedDeliveryReconciliationSnapshot(
		ctx, bundle.ProjectID, DeliveryOperationPreview, blocked.ID,
	)
	if err != nil || secondSnapshot.LastError.Code != "controller-history-still-unavailable" ||
		secondSnapshot.ExpectedRunVersion != reblocked.Version {
		t.Fatalf("second blocked snapshot=%+v err=%v", secondSnapshot, err)
	}
	secondInput := operatorReconciliationInput(
		t, secondSnapshot, bundle.CreatedBy, "operator-race-second-repair", "controller history restored after second repair",
	)
	if _, replayed, err := store.ResumeBlockedDelivery(ctx, secondInput); err != nil || replayed {
		t.Fatalf("second governed Case after GET 404: replayed=%v err=%v", replayed, err)
	}
	if err := database.WithContext(ctx).Raw(`
SELECT count(*) FROM release_delivery_reconciliation_cases WHERE operation_id = ?
`, request.OperationID).Row().Scan(&caseCount); err != nil || caseCount != 2 {
		t.Fatalf("second immutable Case count=%d err=%v", caseCount, err)
	}

	claim, err := store.ClaimDeliveryOperation(ctx, "operator-resubmit-canary", 30*time.Second)
	if err != nil || claim == nil || claim.Preview == nil || claim.Preview.Run.ID != blocked.ID || !claim.ReconcileOnly {
		t.Fatalf("claim second GET-only reconciliation: claim=%+v err=%v", claim, err)
	}
	resubmit := database.WithContext(ctx).Exec(`
INSERT INTO release_delivery_operation_attempts (
  operation_id, ordinal, schema_version, kind, worker_id, fence_epoch
) VALUES (?, 1, 'release-delivery-operation-attempt/v1', 'resubmit', ?, ?)
`, claim.Request.OperationID, claim.Lease().WorkerID, claim.Lease().Epoch)
	if resubmit.Error == nil || !strings.Contains(resubmit.Error.Error(), "GET-only") {
		t.Fatalf("database resubmit guard error=%v, want permanent GET-only rejection", resubmit.Error)
	}
}
