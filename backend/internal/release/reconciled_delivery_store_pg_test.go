package release

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"github.com/worksflow/builder/backend/internal/verification"
	"github.com/worksflow/builder/backend/migrations"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestReconciledDeliveryStorePostgresClosesExactControllerOperations(t *testing.T) {
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
		ID:             "postgres-reconciliation-controller",
		Version:        "2026.07.18+integration",
		Protocol:       DeliveryControllerProtocolV3,
		TrustKeyDigest: reconciledDeliveryDigest("controller-spki"),
	}
	store, err := NewReconciledDeliveryStore(baseStore, controller)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Readiness(ctx); err != nil {
		t.Fatalf("reconciled store readiness: %v", err)
	}

	preview := closeReconciledPreviewPostgres(t, ctx, database, store, controller, bundle)
	closeReconciledProductionPostgres(t, ctx, database, store, controller, bundle, preview)
	assertReconciledRunCannotTerminalizeWithoutResult(t, ctx, database, store, bundle)
}

func closeReconciledPreviewPostgres(
	t *testing.T,
	ctx context.Context,
	database *gorm.DB,
	store *ReconciledDeliveryStore,
	controller DeliveryControllerIdentity,
	bundle Bundle,
) PreviewReceipt {
	t.Helper()
	runID := uuid.NewString()
	apiRequestHash := reconciledDeliveryDigest("preview-api-idempotency-" + runID)
	run, replayed, err := store.CreatePreviewRun(ctx, CreatePreviewRunInput{
		ID: runID, ProjectID: bundle.ProjectID,
		ReleaseBundle:  repository.ExactReference{ID: bundle.ID, ContentHash: bundle.BundleHash},
		BundleDocument: bundle,
		RequestKey:     "preview-" + runID, RequestHash: apiRequestHash,
		Reason: "exercise exact preview reconciliation", CreatedBy: bundle.CreatedBy,
	})
	if err != nil || replayed || run.State != DeliveryQueued {
		t.Fatalf("create v2 preview Run: run=%+v replayed=%v err=%v", run, replayed, err)
	}

	var operation deliveryOperationRow
	if err := database.WithContext(ctx).Where("preview_run_id = ?", runID).Take(&operation).Error; err != nil {
		t.Fatalf("load exact preview Operation: %v", err)
	}
	request, err := store.deliveryOperationRequestFromRow(operation)
	if err != nil {
		t.Fatalf("parse stored preview Operation: %v", err)
	}
	if request.RequestHash == apiRequestHash {
		t.Fatalf("controller request hash reused API idempotency hash %s", request.RequestHash)
	}
	payload := decodeDeliveryOperationPayload[PreviewDeliveryOperationPayload](t, request)
	if payload.OperationID != runID || payload.RunID != runID ||
		payload.ProjectID != bundle.ProjectID || !reflect.DeepEqual(payload.ReleaseBundle, bundle) {
		t.Fatalf("stored controller request lost its complete immutable payload: %+v", payload)
	}
	var storedRequestDocument string
	if err := database.WithContext(ctx).Raw(`
SELECT request_document FROM release_delivery_operations WHERE id = ?
`, request.OperationID).Row().Scan(&storedRequestDocument); err != nil || storedRequestDocument != string(request.RequestDocument) {
		t.Fatalf("stored request bytes differ from submitted canonical document: err=%v", err)
	}
	assertNestedDeliveryAuthorityRejectsTamper(t, ctx, database, store, operation, request, bundle.CreatedBy)

	originalClock := store.now
	store.now = func() time.Time {
		return time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	firstClaim, err := store.ClaimDeliveryOperation(ctx, "preview-submitter", 30*time.Second)
	if err != nil || firstClaim == nil || firstClaim.Preview == nil ||
		firstClaim.Preview.Run.ID != runID || firstClaim.RunState != DeliveryClaimed {
		t.Fatalf("claim preview submission: claim=%+v err=%v", firstClaim, err)
	}
	var claimedLease, databaseNow time.Time
	if err := database.WithContext(ctx).Raw(`
SELECT lease_expires_at, statement_timestamp()
FROM release_preview_runs WHERE id = ?
`, runID).Row().Scan(&claimedLease, &databaseNow); err != nil {
		t.Fatalf("inspect database-authoritative claimed lease: %v", err)
	}
	if !firstClaim.Lease().ExpiresAt.Equal(claimedLease) ||
		claimedLease.Before(databaseNow.Add(29*time.Second)) ||
		claimedLease.After(databaseNow.Add(31*time.Second)) {
		t.Fatalf("claim lease=%s projected=%s databaseNow=%s, want databaseNow+30s despite past app clock",
			firstClaim.Lease().ExpiresAt, claimedLease, databaseNow)
	}
	store.now = func() time.Time {
		return time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if err := store.RenewDeliveryOperation(ctx, *firstClaim, 45*time.Second); err != nil {
		t.Fatalf("renew lease with skewed application clock: %v", err)
	}
	if err := database.WithContext(ctx).Raw(`
SELECT lease_expires_at, statement_timestamp()
FROM release_preview_runs WHERE id = ?
`, runID).Row().Scan(&claimedLease, &databaseNow); err != nil {
		t.Fatalf("inspect database-authoritative renewed lease: %v", err)
	}
	if claimedLease.Before(databaseNow.Add(44*time.Second)) || claimedLease.After(databaseNow.Add(46*time.Second)) {
		t.Fatalf("renewed lease=%s databaseNow=%s, want databaseNow+45s despite future app clock", claimedLease, databaseNow)
	}
	store.now = originalClock
	firstAttempt, err := store.BeginDeliveryAttempt(ctx, *firstClaim, DeliveryAttemptSubmit)
	if err != nil || firstAttempt.Ordinal != 1 {
		t.Fatalf("begin first submit Attempt: attempt=%+v err=%v", firstAttempt, err)
	}
	nextAttemptAt := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	if err := store.RecordDeliveryUnknown(
		ctx, *firstClaim, firstAttempt,
		"controller-outcome-unknown", "the PUT response was lost after send", nextAttemptAt,
	); err != nil {
		t.Fatalf("record unknown submit outcome: %v", err)
	}

	secondClaim, err := store.ClaimDeliveryOperation(ctx, "preview-reconciler", 30*time.Second)
	if err != nil || secondClaim == nil || secondClaim.Preview == nil ||
		secondClaim.Preview.Run.ID != runID || secondClaim.RunState != DeliveryReconciling ||
		secondClaim.RemoteState != "submit_unknown" || secondClaim.Lease().Epoch <= firstClaim.Lease().Epoch {
		t.Fatalf("reclaim unknown preview operation: claim=%+v err=%v", secondClaim, err)
	}
	if err := store.RecordDeliveryUnknown(
		ctx, *firstClaim, firstAttempt,
		"stale-worker", "a fenced worker must not write", nextAttemptAt,
	); !errors.Is(err, ErrDeliveryFence) {
		t.Fatalf("stale worker write error=%v, want ErrDeliveryFence", err)
	}
	reconcileAttempt, err := store.BeginDeliveryAttempt(ctx, *secondClaim, DeliveryAttemptReconcile)
	if err != nil || reconcileAttempt.Ordinal != 2 || reconcileAttempt.FenceEpoch != secondClaim.Lease().Epoch {
		t.Fatalf("begin exact reconcile Attempt: attempt=%+v err=%v", reconcileAttempt, err)
	}

	result := reconciledDeliveryCompletedResult(
		t, controller, secondClaim.Request, passingReleasePreviewChecks(), "", nil,
	)
	observation := DeliveryOperationObservation{
		SchemaVersion: DeliveryOperationObservationSchema,
		Controller:    controller,
		OperationID:   secondClaim.Request.OperationID,
		RequestHash:   secondClaim.Request.RequestHash,
		State:         DeliveryRemoteCompleted,
		Sequence:      1,
		ObservedAt:    result.CompletedAt.Add(time.Microsecond),
		Result:        &result,
	}
	if err := store.RecordDeliveryObservation(ctx, *secondClaim, reconcileAttempt, observation, time.Time{}); err != nil {
		t.Fatalf("persist exact reconciled preview Result: %v", err)
	}
	if err := store.FinalizeDeliveryOperation(ctx, *secondClaim); err != nil {
		t.Fatalf("finalize operation-backed v2 PreviewReceipt: %v", err)
	}

	completed, err := store.GetPreviewRun(ctx, bundle.ProjectID, runID)
	if err != nil || completed.State != DeliveryPassed || completed.Receipt == nil {
		t.Fatalf("completed preview Run: run=%+v err=%v", completed, err)
	}
	receipt, err := store.GetPreviewReceipt(
		ctx, bundle.ProjectID, completed.Receipt.ID, completed.Receipt.ContentHash,
	)
	if err != nil {
		t.Fatalf("load v2 PreviewReceipt: %v", err)
	}
	if receipt.SchemaVersion != PreviewReceiptSchemaVersionV2 || receipt.ControllerOperation == nil ||
		receipt.ControllerOperation.OperationID != request.OperationID ||
		receipt.ControllerOperation.ResultHash != result.ResultHash {
		t.Fatalf("PreviewReceipt lost exact controller authority: %+v", receipt.ControllerOperation)
	}
	var resultCount, attemptCount int64
	if err := database.WithContext(ctx).Raw(`
SELECT
  (SELECT count(*) FROM release_delivery_operation_results WHERE operation_id = ?),
  (SELECT count(*) FROM release_delivery_operation_attempts WHERE operation_id = ?)
`, request.OperationID, request.OperationID).Row().Scan(&resultCount, &attemptCount); err != nil ||
		resultCount != 1 || attemptCount != 2 {
		t.Fatalf("immutable reconciliation evidence counts result=%d attempts=%d err=%v", resultCount, attemptCount, err)
	}
	return receipt
}

func assertNestedDeliveryAuthorityRejectsTamper(
	t *testing.T,
	ctx context.Context,
	database *gorm.DB,
	store *ReconciledDeliveryStore,
	operation deliveryOperationRow,
	request DeliveryOperationRequest,
	createdBy string,
) {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(request.RequestDocument, &document); err != nil {
		t.Fatal(err)
	}
	payload, ok := document["payload"].(map[string]any)
	if !ok {
		t.Fatal("preview request payload is not an object")
	}
	bundle, ok := payload["releaseBundle"].(map[string]any)
	if !ok {
		t.Fatal("preview request Bundle is not an object")
	}
	manifest, ok := bundle["buildManifest"].(map[string]any)
	if !ok {
		t.Fatal("preview request BuildManifest is not an object")
	}
	manifest["contentHash"] = reconciledDeliveryDigest("tampered-nested-build-manifest")
	tamperedDocument, err := domain.CanonicalJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	tamperedHash := reconciledDeliveryBytesDigest(tamperedDocument)

	if err := database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Exec(`SET LOCAL session_replication_role = replica`).Error; err != nil {
			return err
		}
		return transaction.Exec(`DELETE FROM release_delivery_operations WHERE id = ?`, operation.ID).Error
	}); err != nil {
		t.Fatalf("prepare isolated nested authority tamper canary: %v", err)
	}
	insert := database.WithContext(ctx).Exec(`
INSERT INTO release_delivery_operations (
  id, schema_version, project_id, kind, preview_run_id,
  request_schema_version, request_document, request_hash,
  controller_schema_version, controller_id, controller_version,
  controller_protocol, controller_trust_key_digest, remote_state, created_by
) VALUES (?, 'release-delivery-operation/v1', ?, 'preview', ?,
          'release-delivery-operation-request/v3', ?, ?, ?, ?, ?, ?, ?, 'prepared', ?)
`, operation.ID, operation.ProjectID, *operation.PreviewRunID,
		string(tamperedDocument), tamperedHash,
		operation.ControllerSchemaVersion, operation.ControllerID, operation.ControllerVersion,
		operation.ControllerProtocol, operation.ControllerTrustKeyDigest, createdBy)
	if insert.Error == nil || !strings.Contains(insert.Error.Error(), "noncanonical embedded ReleaseBundle") {
		t.Fatalf("outer-hash-valid nested Bundle tamper error=%v, want nested authority guard", insert.Error)
	}
	if err := database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		return store.insertDeliveryOperation(transaction, request, createdBy, *operation.PreviewRunID, "")
	}); err != nil {
		t.Fatalf("restore exact Operation after nested tamper canary: %v", err)
	}
}

func closeReconciledProductionPostgres(
	t *testing.T,
	ctx context.Context,
	database *gorm.DB,
	store *ReconciledDeliveryStore,
	controller DeliveryControllerIdentity,
	bundle Bundle,
	preview PreviewReceipt,
) {
	t.Helper()
	approval, err := NewPromotionApproval(NewPromotionApprovalInput{
		ID: uuid.NewString(), Preview: preview, Reason: "approve exact reconciled preview",
		CreatedBy: bundle.CreatedBy, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	approval, replayed, err := store.SavePromotionApproval(ctx, approval)
	if err != nil || replayed {
		t.Fatalf("save promotion approval: replayed=%v err=%v", replayed, err)
	}
	runID := uuid.NewString()
	apiRequestHash := reconciledDeliveryDigest("production-api-idempotency-" + runID)
	run, replayed, err := store.CreateProductionRun(ctx, CreateProductionRunInput{
		ID: runID, ProjectID: bundle.ProjectID, Environment: "production", Operation: DeploymentPromote,
		ReleaseBundle:     repository.ExactReference{ID: bundle.ID, ContentHash: bundle.BundleHash},
		PreviewReceipt:    repository.ExactReference{ID: preview.ID, ContentHash: preview.PayloadHash},
		PromotionApproval: repository.ExactReference{ID: approval.ID, ContentHash: approval.PayloadHash},
		BundleDocument:    bundle, PreviewDocument: preview, ApprovalDocument: approval,
		RequestKey: "production-" + runID, RequestHash: apiRequestHash,
		Reason: "promote exact operation-backed preview", CreatedBy: bundle.CreatedBy,
	})
	if err != nil || replayed || run.State != DeliveryQueued {
		t.Fatalf("create v2 production Run: run=%+v replayed=%v err=%v", run, replayed, err)
	}
	claim, err := store.ClaimDeliveryOperation(ctx, "production-submitter", 30*time.Second)
	if err != nil || claim == nil || claim.Production == nil || claim.Production.Run.ID != runID {
		t.Fatalf("claim production operation: claim=%+v err=%v", claim, err)
	}
	if claim.Request.RequestHash == apiRequestHash {
		t.Fatalf("production controller hash reused API request hash %s", apiRequestHash)
	}
	payload := decodeDeliveryOperationPayload[ProductionDeliveryOperationPayload](t, claim.Request)
	if !reflect.DeepEqual(payload.ReleaseBundle, bundle) || !reflect.DeepEqual(payload.PreviewReceipt, preview) ||
		!reflect.DeepEqual(payload.PromotionApproval, approval) || payload.SourceRevision != nil ||
		payload.ExpectedHead.Revision != nil || payload.ExpectedHead.ProductionReceipt != nil {
		t.Fatalf("production Operation did not persist complete reviewed input and empty exact head: %+v", payload)
	}
	attempt, err := store.BeginDeliveryAttempt(ctx, *claim, DeliveryAttemptSubmit)
	if err != nil {
		t.Fatalf("begin production submission: %v", err)
	}
	result := reconciledDeliveryCompletedResult(
		t, controller, claim.Request, passingReleaseProductionChecks(),
		"https://application.example.test", nil,
	)
	observation := DeliveryOperationObservation{
		SchemaVersion: DeliveryOperationObservationSchema, Controller: controller,
		OperationID: claim.Request.OperationID, RequestHash: claim.Request.RequestHash,
		State: DeliveryRemoteCompleted, Sequence: 1,
		ObservedAt: result.CompletedAt.Add(time.Microsecond), Result: &result,
	}
	if err := store.RecordDeliveryObservation(ctx, *claim, attempt, observation, time.Time{}); err != nil {
		t.Fatalf("persist production Result: %v", err)
	}
	if err := store.FinalizeDeliveryOperation(ctx, *claim); err != nil {
		t.Fatalf("finalize production Receipt and Revision: %v", err)
	}
	completed, err := store.GetProductionRun(ctx, bundle.ProjectID, runID)
	if err != nil || completed.State != DeliveryHealthy || completed.Receipt == nil || completed.Revision == nil {
		t.Fatalf("healthy production Run: run=%+v err=%v", completed, err)
	}
	receipt, err := store.GetProductionReceipt(ctx, bundle.ProjectID, completed.Receipt.ID, completed.Receipt.ContentHash)
	if err != nil || receipt.ControllerOperation == nil ||
		receipt.ControllerOperation.OperationID != claim.Request.OperationID ||
		receipt.ControllerOperation.ResultHash != result.ResultHash {
		t.Fatalf("production Receipt authority=%+v err=%v", receipt.ControllerOperation, err)
	}
	revision, err := store.GetDeploymentRevision(ctx, bundle.ProjectID, completed.Revision.ID, completed.Revision.ContentHash)
	if err != nil || revision.ControllerOperation == nil ||
		revision.ControllerOperation.OperationID != claim.Request.OperationID ||
		revision.ControllerOperation.ResultHash != result.ResultHash {
		t.Fatalf("production Revision authority=%+v err=%v", revision.ControllerOperation, err)
	}
	var headRevisionID, headRevisionHash, headReceiptID, headReceiptHash string
	var generation uint64
	if err := database.WithContext(ctx).Raw(`
SELECT deployment_revision_id::text, deployment_revision_hash,
       production_receipt_id::text, production_receipt_hash, generation
FROM release_production_heads WHERE project_id = ? AND environment = 'production'
`, bundle.ProjectID).Row().Scan(
		&headRevisionID, &headRevisionHash, &headReceiptID, &headReceiptHash, &generation,
	); err != nil || generation != 1 || headRevisionID != revision.ID || headRevisionHash != revision.PayloadHash ||
		headReceiptID != receipt.ID || headReceiptHash != receipt.PayloadHash {
		t.Fatalf("production head did not advance exactly once: revision=%s@%s receipt=%s@%s generation=%d err=%v",
			headRevisionID, headRevisionHash, headReceiptID, headReceiptHash, generation, err)
	}
}

func assertReconciledRunCannotTerminalizeWithoutResult(
	t *testing.T,
	ctx context.Context,
	database *gorm.DB,
	store *ReconciledDeliveryStore,
	bundle Bundle,
) {
	t.Helper()
	runID := uuid.NewString()
	_, _, err := store.CreatePreviewRun(ctx, CreatePreviewRunInput{
		ID: runID, ProjectID: bundle.ProjectID,
		ReleaseBundle:  repository.ExactReference{ID: bundle.ID, ContentHash: bundle.BundleHash},
		BundleDocument: bundle,
		RequestKey:     "no-result-" + runID,
		RequestHash:    reconciledDeliveryDigest("no-result-api-" + runID),
		Reason:         "prove a Result is mandatory", CreatedBy: bundle.CreatedBy,
	})
	if err != nil {
		t.Fatalf("create no-Result Run: %v", err)
	}
	claim, err := store.ClaimDeliveryOperation(ctx, "no-result-worker", 30*time.Second)
	if err != nil || claim == nil || claim.Preview == nil || claim.Preview.Run.ID != runID {
		t.Fatalf("claim no-Result Run: claim=%+v err=%v", claim, err)
	}
	if _, err := store.BeginDeliveryAttempt(ctx, *claim, DeliveryAttemptSubmit); err != nil {
		t.Fatalf("begin no-Result Attempt: %v", err)
	}
	if err := store.FinalizeDeliveryOperation(ctx, *claim); err == nil {
		t.Fatal("finalization succeeded without an exact controller Result")
	}
	terminalize := database.WithContext(ctx).Exec(`
UPDATE release_preview_runs
SET state = 'passed', version = version + 1,
    lease_worker_id = NULL, lease_epoch = NULL, lease_expires_at = NULL,
    finished_at = statement_timestamp(),
    updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ?
`, runID)
	if terminalize.Error == nil {
		t.Fatal("PostgreSQL authority guard allowed terminal Run state without a Result-backed Receipt")
	}
	var state string
	var resultCount int64
	if err := database.WithContext(ctx).Raw(`
SELECT state, (SELECT count(*) FROM release_delivery_operation_results AS result
               JOIN release_delivery_operations AS operation ON operation.id = result.operation_id
               WHERE operation.preview_run_id = release_preview_runs.id)
FROM release_preview_runs WHERE id = ?
`, runID).Row().Scan(&state, &resultCount); err != nil || state != string(DeliverySubmitting) || resultCount != 0 {
		t.Fatalf("no-Result Run changed state=%q results=%d err=%v", state, resultCount, err)
	}
}

func reconciledDeliveryCompletedResult(
	t *testing.T,
	controller DeliveryControllerIdentity,
	request DeliveryOperationRequest,
	checks []PreviewCheck,
	publicURL string,
	previousHead *repository.ExactReference,
) DeliveryOperationResult {
	t.Helper()
	result := DeliveryOperationResult{
		SchemaVersion: DeliveryOperationResultSchemaVersion,
		Controller:    controller,
		OperationID:   request.OperationID,
		RequestHash:   request.RequestHash,
		Kind:          request.Kind,
		ProjectID:     request.ProjectID,
		Status:        DeliveryRemoteCompleted,
		Provider:      "kubernetes",
		ProviderRef:   "deployment/application@integration",
		PublicURL:     publicURL,
		Checks:        append([]PreviewCheck(nil), checks...),
		PreviousHead:  previousHead,
		CompletedAt:   time.Now().UTC().Truncate(time.Microsecond),
	}
	hash, err := domain.CanonicalHash(deliveryOperationResultHashPayload(result))
	if err != nil {
		t.Fatal(err)
	}
	result.ResultHash = "sha256:" + hash
	parsed, err := ParseDeliveryOperationResult(result, controller, request)
	if err != nil {
		t.Fatalf("build completed controller Result: %v", err)
	}
	return parsed
}

type reconciledDeliveryMemoryContentStore struct {
	mu      sync.Mutex
	records map[string]content.StoredContent
}

func newReconciledDeliveryMemoryContentStore() *reconciledDeliveryMemoryContentStore {
	return &reconciledDeliveryMemoryContentStore{records: map[string]content.StoredContent{}}
}

func (store *reconciledDeliveryMemoryContentStore) PutPending(
	_ context.Context,
	projectID, aggregateType, aggregateID string,
	schemaVersion int,
	payload json.RawMessage,
) (content.Reference, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	canonical, err := canonicalReconciledDeliveryJSON(payload)
	if err != nil {
		return content.Reference{}, err
	}
	id := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	reference := content.Reference{
		ID: id, ContentHash: reconciledDeliveryBytesDigest(canonical),
		ByteSize: int64(len(canonical)), SchemaVersion: schemaVersion,
	}
	store.records[id] = content.StoredContent{
		Reference: reference, ProjectID: projectID, AggregateType: aggregateType,
		AggregateID: aggregateID, State: content.StatePending,
		Payload: append(json.RawMessage(nil), canonical...), CreatedAt: now,
	}
	return reference, nil
}

func (store *reconciledDeliveryMemoryContentStore) Finalize(_ context.Context, id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[id]
	if !ok || record.State == content.StateAborted {
		return content.ErrContentNotFound
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	record.State, record.FinalizedAt = content.StateFinalized, &now
	store.records[id] = record
	return nil
}

func (store *reconciledDeliveryMemoryContentStore) Abort(_ context.Context, id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[id]
	if !ok {
		return content.ErrContentNotFound
	}
	if record.State == content.StatePending {
		record.State = content.StateAborted
		store.records[id] = record
	}
	return nil
}

func (store *reconciledDeliveryMemoryContentStore) Get(
	_ context.Context,
	id, expectedHash string,
) (content.StoredContent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[id]
	if !ok || record.State == content.StateAborted {
		return content.StoredContent{}, content.ErrContentNotFound
	}
	if record.ContentHash != expectedHash || record.ContentHash != reconciledDeliveryBytesDigest(record.Payload) {
		return content.StoredContent{}, content.ErrHashMismatch
	}
	record.Payload = append(json.RawMessage(nil), record.Payload...)
	return record, nil
}

type reconciledDeliveryReceiptReader struct {
	receipt verification.CanonicalReceipt
}

func (reader reconciledDeliveryReceiptReader) GetCanonicalReceipt(
	_ context.Context,
	projectID, receiptID, receiptHash string,
) (verification.CanonicalReceipt, error) {
	if reader.receipt.ProjectID != projectID || reader.receipt.ID != receiptID ||
		reader.receipt.PayloadHash != receiptHash {
		return verification.CanonicalReceipt{}, errors.New("exact Canonical Receipt was not found")
	}
	return reader.receipt, nil
}

func reconciledDeliveryPostgresFixture(
	t *testing.T,
	ctx context.Context,
) (*gorm.DB, *reconciledDeliveryMemoryContentStore, verification.CanonicalReceipt, Bundle, func()) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "release_reconciliation_store_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		_ = base.Close()
		t.Fatal(err)
	}
	scoped, err := sql.Open("pgx", reconciledDeliveryPostgresDSN(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(ctx, scoped); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	database, err := gorm.Open(
		postgres.New(postgres.Config{Conn: scoped}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_ = scoped.Close()
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
		_ = base.Close()
	}

	receipt := passingCanonicalReceipt(t)
	bundle, err := NewBundle(NewBundleInput{
		ID: uuid.NewString(), Receipt: receipt, CreatedBy: receipt.CreatedBy,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	if _, err := scoped.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'Release reconciliation actor', 'not-used')
`, bundle.CreatedBy, "release-reconcile-"+uuid.NewString()+"@example.com"); err != nil {
		cleanup()
		t.Fatal(err)
	}
	if _, err := scoped.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, 'Release reconciliation project', $2)
`, bundle.ProjectID, bundle.CreatedBy); err != nil {
		cleanup()
		t.Fatal(err)
	}
	contents := newReconciledDeliveryMemoryContentStore()
	bundlePayload, err := domain.CanonicalJSON(bundle)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	bundleContent, err := contents.PutPending(
		ctx, bundle.ProjectID, bundleAggregateType, bundle.ID, 1, bundlePayload,
	)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	if err := contents.Finalize(ctx, bundleContent.ID); err != nil {
		cleanup()
		t.Fatal(err)
	}
	artifacts, err := json.Marshal(bundle.ReleaseArtifacts)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	err = database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Exec(`SET LOCAL session_replication_role = replica`).Error; err != nil {
			return fmt.Errorf("disable isolated lineage triggers: %w", err)
		}
		if err := transaction.Exec(`
INSERT INTO artifacts (
  id, project_id, kind, artifact_key, title, created_by
) VALUES (?, ?, 'workspace', ?, 'Release reconciliation workspace', ?)
`, bundle.Workspace.WorkspaceArtifactID, bundle.ProjectID,
			"release-reconciliation-workspace-"+bundle.Workspace.WorkspaceArtifactID, bundle.CreatedBy).Error; err != nil {
			return fmt.Errorf("seed Workspace artifact: %w", err)
		}
		if err := transaction.Exec(`
INSERT INTO artifact_revisions (
  id, artifact_id, revision_number, schema_version,
  content_store, content_ref, content_hash, byte_size,
  workflow_status, change_source, change_summary, created_by, approved_at
) VALUES (?, ?, 1, 1, 'mongo', ?, ?, 1,
          'approved', 'system', 'release reconciliation fixture', ?, ?)
`, bundle.Workspace.WorkspaceRevisionID, bundle.Workspace.WorkspaceArtifactID,
			"workspace-revision-"+bundle.Workspace.WorkspaceRevisionID,
			bundle.Workspace.WorkspaceContentHash, bundle.CreatedBy, bundle.CreatedAt).Error; err != nil {
			return fmt.Errorf("seed Workspace revision: %w", err)
		}
		attemptIDs, marshalErr := json.Marshal(receipt.AttemptIDs)
		if marshalErr != nil {
			return marshalErr
		}
		if err := transaction.Exec(`
INSERT INTO canonical_verification_receipts (
  id, schema_version, scope, run_id, project_id,
  plan_id, plan_hash,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  build_manifest_id, build_manifest_hash,
  build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  verification_profile_id, verification_profile_version, verification_profile_hash,
  attempt_ids, release_artifacts,
  check_count, coverage_count, must_count, must_passed_count,
  blocker_count, warning_count, decision,
  content_store, content_ref, content_hash, payload_hash, created_by, created_at
) VALUES (
  ?, 'canonical-verification-receipt/v1', 'canonical', ?, ?,
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
  ?::jsonb, ?::jsonb, ?, ?, ?, ?, ?, ?, 'passed',
  'mongo', ?, ?, ?, ?, ?
)
`, receipt.ID, receipt.RunID, receipt.ProjectID,
			receipt.Plan.ID, receipt.Plan.ContentHash,
			receipt.Subject.WorkspaceArtifactID, receipt.Subject.WorkspaceRevisionID, receipt.Subject.WorkspaceContentHash,
			receipt.BuildManifest.ID, receipt.BuildManifest.ContentHash,
			receipt.BuildContract.ID, receipt.BuildContract.ContentHash,
			receipt.FullStackTemplate.ID, receipt.FullStackTemplate.ContentHash,
			receipt.Profile.ID, receipt.Profile.Version, receipt.Profile.ContentHash,
			string(attemptIDs), string(artifacts), len(receipt.Checks), len(receipt.ObligationCoverage),
			receipt.MustCount, receipt.MustPassedCount, receipt.BlockerCount, receipt.WarningCount,
			"canonical-receipt-"+receipt.ID, reconciledDeliveryDigest("canonical-content-"+receipt.ID),
			receipt.PayloadHash, receipt.CreatedBy, receipt.CreatedAt).Error; err != nil {
			return fmt.Errorf("seed Canonical Receipt projection: %w", err)
		}
		return transaction.Exec(`
INSERT INTO release_bundles (
  id, schema_version, project_id,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  canonical_receipt_id, canonical_receipt_hash, release_artifacts,
  content_store, content_ref, content_hash, bundle_hash, created_by, created_at
) VALUES (?, 'release-bundle/v1', ?, ?, ?, ?, ?, ?, ?::jsonb,
          'mongo', ?, ?, ?, ?, ?)
`, bundle.ID, bundle.ProjectID,
			bundle.Workspace.WorkspaceArtifactID, bundle.Workspace.WorkspaceRevisionID, bundle.Workspace.WorkspaceContentHash,
			bundle.CanonicalReceipt.ID, bundle.CanonicalReceipt.ContentHash, string(artifacts),
			bundleContent.ID, bundleContent.ContentHash, bundle.BundleHash, bundle.CreatedBy, bundle.CreatedAt).Error
	})
	if err != nil {
		cleanup()
		t.Fatalf("seed exact immutable ReleaseBundle: %v", err)
	}
	return database, contents, receipt, bundle, cleanup
}

func canonicalReconciledDeliveryJSON(payload json.RawMessage) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, err
	}
	return domain.CanonicalJSON(value)
}

func reconciledDeliveryDigest(seed string) string {
	return reconciledDeliveryBytesDigest([]byte(seed))
}

func reconciledDeliveryBytesDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func reconciledDeliveryPostgresDSN(t *testing.T, dsn, schema string) string {
	t.Helper()
	if parsed, err := url.Parse(strings.TrimSpace(dsn)); err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}
