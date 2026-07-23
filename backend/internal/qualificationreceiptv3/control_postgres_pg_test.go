package qualificationreceiptv3

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type postgresControlPlanResolver struct {
	base                  *fakeControlPlanResolver
	evidenceHeadVersion   uint64
	evidenceLastEventID   string
	evidenceLastEventHash string
	evidenceCommandDigest string
	evidenceTrustDigest   string
}

func (resolver *postgresControlPlanResolver) ResolveControl(ctx context.Context, lookup ControlLookup) (ControlResolution, error) {
	resolution, err := resolver.base.ResolveControl(ctx, lookup)
	if err != nil {
		return ControlResolution{}, err
	}
	for index := range resolution.Requests {
		request := &resolution.Requests[index]
		request.Request.EvidenceHeadVersion = resolver.evidenceHeadVersion
		request.Request.EvidenceLastEventID = resolver.evidenceLastEventID
		request.Request.EvidenceLastEventHash = resolver.evidenceLastEventHash
		request.Request.EvidenceCommandDigest = resolver.evidenceCommandDigest
		request.Request.EvidenceTrustDigest = resolver.evidenceTrustDigest
		request.RequestBytes, err = CanonicalJSON(request.Request)
		if err != nil {
			return ControlResolution{}, err
		}
		request.RequestHash = SHA256Digest(request.RequestBytes)
	}
	return resolution, nil
}

func (resolver *postgresControlPlanResolver) setReceipt(receipt Receipt) {
	resolver.base.mu.Lock()
	defer resolver.base.mu.Unlock()
	resolver.base.receipt = receipt
}

type postgresControlFixture struct {
	database     *sql.DB
	store        *PostgresStore
	expected     *PostgresExpectedResolver
	plan         *postgresControlPlanResolver
	observations *fakeControlObservationResolver
	service      *ControlService
	receipt      Receipt
	cleanup      func()
}

func TestPostgresControlStoreAndServiceLifecycle(t *testing.T) {
	fixture := openPostgresControlFixture(t)
	defer fixture.cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	authorityID := uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID)
	snapshotOperationID := uuid.MustParse(fixture.receipt.Snapshot.OperationID)

	// A canonical but Plan-drifted row must be rejected by the real migration,
	// not merely by an in-memory mock or by JSONB equality.
	driftedResolution, err := fixture.plan.ResolveControl(ctx, ControlLookup{
		AuthorityID: authorityID, OperationID: snapshotOperationID, Kind: RequestKindSnapshotSeal,
	})
	if err != nil {
		t.Fatal(err)
	}
	drifted, err := recordsFromResolution(ControlLookup{
		AuthorityID: authorityID, OperationID: snapshotOperationID, Kind: RequestKindSnapshotSeal,
	}, driftedResolution)
	if err != nil {
		t.Fatal(err)
	}
	drifted[0].Request.EvidenceCommandDigest = testDigest("PostgreSQL Plan drift")
	drifted[0].RequestBytes, _ = CanonicalJSON(drifted[0].Request)
	drifted[0].RequestHash = SHA256Digest(drifted[0].RequestBytes)
	if _, err := fixture.store.StartBatch(ctx, drifted); !errors.Is(err, ErrControlInvalid) {
		t.Fatalf("real PostgreSQL accepted Plan-drifted request: %v", err)
	}

	seal := concurrentPostgresControlStart(t, ctx, 12, func() (StartOutcome, error) {
		return fixture.service.StartSnapshotSeal(ctx, StartCommand{AuthorityID: authorityID, OperationID: snapshotOperationID})
	})
	sealRequest := seal.Requests[0]
	fixture.receipt.Snapshot.SealedAt = sealRequest.StartedAt.Format(canonicalTimeLayout)
	fixture.plan.setReceipt(fixture.receipt)

	pendingID := fixture.addObservation(t, sealRequest, 1, 1, ObservationPending, nil, nil, nil)
	pending := concurrentPostgresControlObserve(t, ctx, 12, func() (ObservationRecord, bool, error) {
		record, err := fixture.service.Observe(ctx, ObservationCommand{Request: sealRequest.Key, ObservationAuthorityID: pendingID})
		return record, false, err
	})
	notInvoked := fixture.observe(t, sealRequest, 1, 2, ObservationNotInvoked, nil, nil, &pending)
	if notInvoked.Status != ObservationNotInvoked {
		t.Fatal("real PostgreSQL did not persist authenticated not-invoked terminal")
	}
	retryID := fixture.addObservation(t, sealRequest, 2, 3, ObservationPending, nil, nil, nil)
	retry, err := fixture.service.AcquireRetry(ctx, ObservationCommand{Request: sealRequest.Key, ObservationAuthorityID: retryID})
	if err != nil || !retry.CallOwnership {
		t.Fatalf("acquire real PostgreSQL retry = %+v, %v", retry, err)
	}
	retryPending := retry.Observation
	if _, err := fixture.store.InspectTerminalObservation(ctx, sealRequest.RequestHash); !errors.Is(err, ErrControlNotFound) {
		t.Fatalf("terminal inspection fell back behind latest retry pending: %v", err)
	}
	fixture.observe(t, sealRequest, 2, 4, ObservationCommitted, fixture.receipt.Snapshot, nil, nil)
	if retryPending.Generation != 2 {
		t.Fatalf("retry generation = %d", retryPending.Generation)
	}

	verify, err := fixture.service.StartSnapshotVerification(ctx, StartCommand{AuthorityID: authorityID, OperationID: snapshotOperationID})
	if err != nil || !verify.CallOwnership || len(verify.Requests) != 1 {
		t.Fatalf("start PostgreSQL verification = %+v, %v", verify, err)
	}
	verifyRequest := verify.Requests[0]
	fixture.receipt.SnapshotVerification.VerifiedAt = verifyRequest.StartedAt.Format(canonicalTimeLayout)
	fixture.receipt.CompletedAt = fixture.receipt.SnapshotVerification.VerifiedAt
	fixture.receipt.IssuedAt = fixture.receipt.SnapshotVerification.VerifiedAt
	if _, err := Compile(fixture.receipt); err != nil {
		t.Fatalf("dynamic PostgreSQL Receipt time closure: %v", err)
	}
	fixture.plan.setReceipt(fixture.receipt)
	verifyPendingID := fixture.addObservation(t, verifyRequest, 1, 1, ObservationPending, nil, nil, nil)
	fixture.store.commit = func(transaction *sql.Tx) error {
		if err := transaction.Commit(); err != nil {
			return err
		}
		return errors.New("injected observation commit acknowledgement loss")
	}
	verifyPending, err := fixture.service.Observe(ctx, ObservationCommand{
		Request: verifyRequest.Key, ObservationAuthorityID: verifyPendingID,
	})
	fixture.store.commit = func(transaction *sql.Tx) error { return transaction.Commit() }
	if err != nil || !verifyPending.Idempotent {
		t.Fatalf("PostgreSQL observation commit-unknown reconciliation = %+v, %v", verifyPending, err)
	}
	fixture.observe(t, verifyRequest, 1, 2, ObservationCommitted, fixture.receipt.SnapshotVerification, nil, nil)

	signing := concurrentPostgresControlStart(t, ctx, 12, func() (StartOutcome, error) {
		return fixture.service.StartSigning(ctx, StartCommand{
			AuthorityID: authorityID, OperationID: uuid.MustParse(fixture.receipt.OperationID),
		})
	})
	runnerKey, approverKey := testKeys()
	keys := map[ControlRole]testSigningKey{ControlRoleRunner: runnerKey, ControlRoleReleaseApprover: approverKey}
	for _, request := range signing.Requests {
		fixture.observe(t, request, 1, 1, ObservationPending, nil, nil, nil)
		fixture.observe(t, request, 1, 2, ObservationCommitted, nil, ed25519.Sign(keys[request.Key.Role].private, request.PAE), nil)
	}

	expected, err := fixture.expected.ResolveExpected(ctx, authorityID.String(), fixture.receipt.ReceiptID)
	if err != nil || expected.PayloadDigest != signing.Requests[0].PayloadHash || !bytes.Equal(expected.Payload, signing.Requests[0].Payload) {
		t.Fatalf("PostgreSQL expected resolver = %+v, %v", expected, err)
	}
	if _, err := fixture.expected.ResolveExpected(ctx, authorityID.String(), "different-receipt"); !errors.Is(err, ErrControlNotFound) {
		t.Fatalf("expected resolver accepted another Receipt ID: %v", err)
	}

	// Force a genuinely ambiguous commit: the hook commits on PostgreSQL, then
	// reports a transport-style error. ControlService must reconcile through a
	// fresh exact InspectCompletion and may only return an idempotent result.
	time.Sleep(3 * time.Millisecond)
	fixture.store.commit = func(transaction *sql.Tx) error {
		if err := transaction.Commit(); err != nil {
			return err
		}
		return errors.New("injected completion commit acknowledgement loss")
	}
	completed, err := fixture.service.Complete(ctx, CompletionCommand{
		AuthorityID: authorityID, SnapshotOperationID: snapshotOperationID,
		ReceiptSignOperationID: uuid.MustParse(fixture.receipt.OperationID),
	})
	fixture.store.commit = func(transaction *sql.Tx) error { return transaction.Commit() }
	if err != nil || !completed.Idempotent || completed.verificationEnvelopeHash != completed.EnvelopeDigest {
		t.Fatalf("PostgreSQL completion commit-unknown reconciliation = %+v, %v", completed, err)
	}

	// A new Store and resolver reconstruct the exact immutable bytes and the
	// private verifier grant solely from the owner ledger.
	restartedStore, err := NewPostgresStore(fixture.database)
	if err != nil {
		t.Fatal(err)
	}
	restartedExpected, err := NewPostgresExpectedResolver(fixture.database)
	if err != nil {
		t.Fatal(err)
	}
	restartedVerifier, err := NewVerifier(testPolicy(), restartedExpected)
	if err != nil {
		t.Fatal(err)
	}
	restartedService, err := NewControlService(fixture.plan, fixture.observations, restartedStore, restartedVerifier)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restartedStore.InspectCompletion(ctx, authorityID)
	if err != nil || recovered.DocumentHash != completed.DocumentHash || recovered.verificationEnvelopeHash != recovered.EnvelopeDigest {
		t.Fatalf("restart completion recovery = %+v, %v", recovered, err)
	}
	replay, err := restartedService.Complete(ctx, CompletionCommand{
		AuthorityID: authorityID, SnapshotOperationID: snapshotOperationID,
		ReceiptSignOperationID: uuid.MustParse(fixture.receipt.OperationID),
	})
	if err != nil || !replay.Idempotent || !bytes.Equal(replay.Envelope, completed.Envelope) {
		t.Fatalf("restart completion replay = %+v, %v", replay, err)
	}
}

func TestPostgresControlCommitUnknownStartNeverOwnsCall(t *testing.T) {
	fixture := openPostgresControlFixture(t)
	defer fixture.cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fixture.store.commit = func(transaction *sql.Tx) error {
		if err := transaction.Commit(); err != nil {
			return err
		}
		return errors.New("injected start commit acknowledgement loss")
	}
	result, err := fixture.service.StartSnapshotSeal(ctx, StartCommand{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(fixture.receipt.Snapshot.OperationID),
	})
	if err != nil || result.CallOwnership || len(result.Requests) != 1 {
		t.Fatalf("PostgreSQL start commit-unknown = %+v, %v", result, err)
	}
}

func openPostgresControlFixture(t *testing.T) *postgresControlFixture {
	t.Helper()
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
	if err := base.PingContext(ctx); err != nil {
		_ = base.Close()
		t.Fatal(err)
	}
	if _, err := base.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		_ = base.Close()
		t.Fatal(err)
	}
	schema := "receipt_v3_control_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		_ = base.Close()
		t.Fatal(err)
	}
	database, err := sql.Open("pgx", postgresControlTestDSN(t, dsn, schema))
	if err != nil {
		_ = base.Close()
		t.Fatal(err)
	}
	database.SetMaxOpenConns(32)
	for _, migration := range []string{
		"../../migrations/000071_qualification_promotion_consume.up.sql",
		"../../migrations/000073_qualification_evidence_event_store.up.sql",
		"../../migrations/000074_qualification_plan_authority.up.sql",
		"../../migrations/000075_qualification_receipt_v3_store.up.sql",
	} {
		contents, readErr := os.ReadFile(migration)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, migrationErr := database.ExecContext(ctx, string(contents)); migrationErr != nil {
			postgresControlFatal(t, "apply "+migration, migrationErr)
		}
	}
	now := postgresControlTrustedTime(t, ctx, database)
	receipt, projectionBytes, inputBytes := postgresControlReceipt(t, now)
	eventID, eventHash, headVersion := seedPostgresControlAuthorities(t, ctx, database, receipt, projectionBytes, inputBytes, now)

	basePlan := &fakeControlPlanResolver{receipt: receipt}
	plan := &postgresControlPlanResolver{
		base: basePlan, evidenceHeadVersion: headVersion,
		evidenceLastEventID: eventID, evidenceLastEventHash: eventHash,
		evidenceCommandDigest: receipt.PlanAuthority.EvidencePlanHash,
		evidenceTrustDigest:   receipt.PlanAuthority.TrustBindingsDigest,
	}
	sealResolution, err := plan.ResolveControl(ctx, ControlLookup{
		AuthorityID: uuid.MustParse(receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(receipt.Snapshot.OperationID), Kind: RequestKindSnapshotSeal,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Snapshot.RequestDigest = sealResolution.Requests[0].RequestHash
	if _, err := Compile(receipt); err != nil {
		t.Fatal(err)
	}
	plan.setReceipt(receipt)
	observations := &fakeControlObservationResolver{entries: make(map[uuid.UUID]ResolvedObservation)}
	store, err := NewPostgresStore(database)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := NewPostgresExpectedResolver(database)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(testPolicy(), expected)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewControlService(plan, observations, store, verifier)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_ = database.Close()
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
		_ = base.Close()
	}
	return &postgresControlFixture{
		database: database, store: store, expected: expected, plan: plan,
		observations: observations, service: service, receipt: receipt, cleanup: cleanup,
	}
}

func postgresControlReceipt(t *testing.T, now time.Time) (Receipt, []byte, []byte) {
	t.Helper()
	receipt := validReceipt(t)
	projectionBytes, err := CanonicalJSON(map[string]any{"fixture": "PostgreSQL Receipt v3 projection"})
	if err != nil {
		t.Fatal(err)
	}
	inputBytes, err := CanonicalJSON(map[string]any{"fixture": "PostgreSQL Receipt v3 input"})
	if err != nil {
		t.Fatal(err)
	}
	projectionHash := SHA256Digest(projectionBytes)
	receipt.EvidencePlan.PlanDigest = projectionHash
	receipt.PlanAuthority.PlanDigest = projectionHash
	receipt.PlanAuthority.ProjectionHash = projectionHash
	receipt.PlanAuthority.InputHash = SHA256Digest(inputBytes)
	receipt.CredentialSet.IssuedAt = now.Add(-5 * time.Minute).Format(canonicalTimeLayout)
	receipt.QualificationStartedAt = now.Add(-4 * time.Minute).Format(canonicalTimeLayout)
	receipt.CredentialSet.RevokedAt = now.Add(-3 * time.Minute).Format(canonicalTimeLayout)
	receipt.CredentialSet.ExpiresAt = now.Add(30 * time.Minute).Format(canonicalTimeLayout)
	receipt.ArtifactIndex.CommittedAt = now.Add(-2 * time.Minute).Format(canonicalTimeLayout)
	receipt.Snapshot.SealedAt = now.Add(-time.Minute).Format(canonicalTimeLayout)
	receipt.SnapshotVerification.VerifiedAt = receipt.Snapshot.SealedAt
	receipt.CompletedAt = receipt.Snapshot.SealedAt
	receipt.IssuedAt = receipt.Snapshot.SealedAt
	refreshAuthority(t, &receipt)
	if _, err := Compile(receipt); err != nil {
		t.Fatal(err)
	}
	return receipt, projectionBytes, inputBytes
}

func seedPostgresControlAuthorities(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	receipt Receipt,
	projectionBytes, inputBytes []byte,
	now time.Time,
) (string, string, uint64) {
	t.Helper()
	planBytes, _ := CanonicalJSON(receipt.EvidencePlan)
	trustBytes, _ := CanonicalJSON(receipt.Trust)
	targetBytes, _ := CanonicalJSON(receipt.Target)
	envelopeBytes, _ := CanonicalJSON(authorityEnvelope{
		ArtifactID: receipt.PlanAuthority.ArtifactID, AuthorityID: receipt.PlanAuthority.AuthorityID,
		EvidencePlanHash: receipt.PlanAuthority.EvidencePlanHash, InputAuthorityID: receipt.PlanAuthority.InputAuthorityID,
		InputHash: receipt.PlanAuthority.InputHash, ManifestPlanDigest: receipt.PlanAuthority.PlanDigest,
		OperationID: receipt.PlanAuthority.FreezeOperationID, ProjectionHash: receipt.PlanAuthority.ProjectionHash,
		SchemaVersion: PlanAuthoritySchemaV1, TargetHash: receipt.PlanAuthority.TargetHash,
		TrustBindingsDigest: receipt.PlanAuthority.TrustBindingsDigest, TrustHash: receipt.PlanAuthority.TrustHash,
	})
	requestBytes, _ := CanonicalJSON(map[string]any{"fixture": "PostgreSQL Plan freeze request"})
	target := receipt.Target.PromotionTarget
	_, err := database.ExecContext(ctx, `
INSERT INTO qualification_plan_authorities (
  authority_id, operation_id, input_authority_id, plan_artifact_id,
  orchestration_id, qualification_run_id, fixture_id, credential_set_id,
  request_hash, request_bytes, request_document,
  input_hash, input_bytes, input_document,
  projection_hash, projection_bytes, projection_document,
  evidence_plan_hash, evidence_plan_bytes, evidence_plan_document,
  trust_hash, trust_bytes, trust_document, trust_bindings_digest, trust_policy_digest,
  target_hash, target_bytes, target_document, project_id, workflow_run_id, node_key,
  target_revision_id, target_revision_content_hash, subject, stage_gate,
  envelope_hash, envelope_bytes, envelope_document,
  source_tree_digest, template_release_digest, frozen_at
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,$13,$14::jsonb,
  $15,$16,$17::jsonb,$18,$19,$20::jsonb,$21,$22,$23::jsonb,$24,$25,
  $26,$27,$28::jsonb,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38::jsonb,$39,$40,$41
)`,
		receipt.PlanAuthority.AuthorityID, receipt.PlanAuthority.FreezeOperationID,
		receipt.PlanAuthority.InputAuthorityID, receipt.PlanAuthority.ArtifactID,
		receipt.EvidencePlan.OrchestrationID, receipt.EvidencePlan.RunID, receipt.EvidencePlan.FixtureID,
		receipt.EvidencePlan.CredentialSet.SetID,
		SHA256Digest(requestBytes), requestBytes, string(requestBytes),
		receipt.PlanAuthority.InputHash, inputBytes, string(inputBytes),
		receipt.PlanAuthority.ProjectionHash, projectionBytes, string(projectionBytes),
		receipt.PlanAuthority.EvidencePlanHash, planBytes, string(planBytes),
		receipt.PlanAuthority.TrustHash, trustBytes, string(trustBytes),
		receipt.PlanAuthority.TrustBindingsDigest, receipt.Trust.TrustPolicyDigest,
		receipt.PlanAuthority.TargetHash, targetBytes, string(targetBytes),
		target.ProjectID, target.WorkflowRunID, target.NodeKey, target.TargetRevision.ID,
		target.TargetRevision.ContentHash, target.Subject, target.StageGate,
		receipt.PlanAuthority.AuthorityHash, envelopeBytes, string(envelopeBytes),
		receipt.EvidencePlan.SourceTreeDigest, receipt.EvidencePlan.TemplateReleaseDigest, now,
	)
	if err != nil {
		postgresControlFatal(t, "seed Plan Authority", err)
	}

	eventID := uuid.NewString()
	const headVersion = uint64(17)
	eventBytes, _ := CanonicalJSON(map[string]any{"artifactIndex": receipt.ArtifactIndex})
	eventHash := SHA256Digest(eventBytes)
	eventRequestBytes, _ := CanonicalJSON(map[string]any{"fixture": "PostgreSQL indexed event request"})
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO qualification_evidence_projection_authorizations(transaction_id,backend_pid)
VALUES (txid_current(),pg_backend_pid())`); err != nil {
		postgresControlFatal(t, "authorize Evidence head seed", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO qualification_evidence_events (
  event_id, orchestration_id, version, expected_version, event_kind, operation_id,
  active_artifact_id, event_at, requested_at, request_hash, request_bytes,
  request_document, event_hash, event_bytes, event_document
) VALUES ($1,$2,$3,$4,'artifact-indexed',$5,'',$6,$6,$7,$8,$9::jsonb,$10,$11,$12::jsonb)`,
		eventID, receipt.EvidencePlan.OrchestrationID, int64(headVersion), int64(headVersion-1),
		receipt.EvidencePlan.Operations.ArtifactIndex, now,
		SHA256Digest(eventRequestBytes), eventRequestBytes, string(eventRequestBytes),
		eventHash, eventBytes, string(eventBytes),
	); err != nil {
		postgresControlFatal(t, "seed indexed Evidence event", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO qualification_evidence_heads (
  orchestration_id, version, phase, last_event_id, last_event_at, command_hash,
  trust_bindings_digest, active_operation_id, active_artifact_id, plan_document
) VALUES ($1,$2,'artifact-indexed',$3,$4,$5,$6,NULL,'',$7::jsonb)`,
		receipt.EvidencePlan.OrchestrationID, int64(headVersion), eventID, now,
		receipt.PlanAuthority.EvidencePlanHash, receipt.PlanAuthority.TrustBindingsDigest, string(planBytes),
	); err != nil {
		postgresControlFatal(t, "seed indexed Evidence head", err)
	}
	if _, err := transaction.ExecContext(ctx, `
DELETE FROM qualification_evidence_projection_authorizations
WHERE transaction_id=txid_current() AND backend_pid=pg_backend_pid()`); err != nil {
		postgresControlFatal(t, "clear Evidence head seed authorization", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	return eventID, eventHash, headVersion
}

func (fixture *postgresControlFixture) addObservation(
	t *testing.T,
	request RequestRecord,
	generation, sequence uint64,
	status ObservationStatus,
	result any,
	signature []byte,
	pending *ObservationRecord,
) uuid.UUID {
	t.Helper()
	resolved := resolvedControlObservation(t, request, generation, sequence, status, result, signature, pending)
	resolved.ObservedAt = postgresControlTrustedTime(t, context.Background(), fixture.database)
	resolved.AuthenticationPayload.ObservedAt = resolved.ObservedAt.Format(canonicalTimeLayout)
	refreshResolvedAuthentication(t, request, &resolved)
	id := uuid.New()
	fixture.observations.add(id, resolved)
	return id
}

func (fixture *postgresControlFixture) observe(
	t *testing.T,
	request RequestRecord,
	generation, sequence uint64,
	status ObservationStatus,
	result any,
	signature []byte,
	pending *ObservationRecord,
) ObservationRecord {
	t.Helper()
	id := fixture.addObservation(t, request, generation, sequence, status, result, signature, pending)
	record, err := fixture.service.Observe(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: id})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func concurrentPostgresControlStart(
	t *testing.T,
	ctx context.Context,
	contenders int,
	start func() (StartOutcome, error),
) StartOutcome {
	t.Helper()
	results := make(chan StartOutcome, contenders)
	errorsCh := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := start()
			results <- result
			errorsCh <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	owners := 0
	var exact StartOutcome
	for result := range results {
		if result.CallOwnership {
			owners++
		}
		if len(exact.Requests) == 0 {
			exact = result
		} else if !sameRequestBatch(exact.Requests, result.Requests, true) {
			t.Fatal("concurrent PostgreSQL starts returned different exact records")
		}
	}
	if owners != 1 {
		t.Fatalf("PostgreSQL concurrent start owners = %d, want 1", owners)
	}
	return exact
}

func concurrentPostgresControlObserve(
	t *testing.T,
	ctx context.Context,
	contenders int,
	observe func() (ObservationRecord, bool, error),
) ObservationRecord {
	t.Helper()
	type result struct {
		record    ObservationRecord
		ownership bool
		err       error
	}
	results := make(chan result, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			record, ownership, err := observe()
			results <- result{record: record, ownership: ownership, err: err}
		}()
	}
	wait.Wait()
	close(results)
	owners := 0
	var exact ObservationRecord
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.ownership {
			owners++
		}
		if exact.Sequence == 0 {
			exact = result.record
		} else if !sameObservation(exact, result.record, true) {
			t.Fatal("concurrent PostgreSQL observations returned different exact records")
		}
	}
	if owners > 1 {
		t.Fatalf("PostgreSQL concurrent observation owners = %d", owners)
	}
	return exact
}

func postgresControlTrustedTime(t *testing.T, ctx context.Context, database *sql.DB) time.Time {
	t.Helper()
	var now time.Time
	if err := database.QueryRowContext(ctx, `SELECT date_trunc('milliseconds',clock_timestamp())`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now.UTC()
}

func postgresControlTestDSN(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema+",public")
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema + ",public"
}

func postgresControlFatal(t *testing.T, operation string, err error) {
	t.Helper()
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		t.Fatalf("%s: %v sqlstate=%s detail=%s where=%s", operation, err, postgresError.Code, postgresError.Detail, postgresError.Where)
	}
	t.Fatalf("%s: %v", operation, err)
}
