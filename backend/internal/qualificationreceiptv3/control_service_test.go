package qualificationreceiptv3

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/templateauthority"
)

type fakeControlPlanResolver struct {
	mu      sync.Mutex
	receipt Receipt
	calls   int
	mutate  func(*ResolvedControlRequest)
}

func (resolver *fakeControlPlanResolver) ResolveControl(_ context.Context, lookup ControlLookup) (ControlResolution, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.calls++
	receipt := resolver.receipt
	base := ControlRequest{
		ArtifactIndexDigest:   receipt.ArtifactIndex.ContentDigest,
		EvidenceClosureDigest: receipt.Evidence.ClosureDigest,
		EvidenceCommandDigest: testDigest("evidence-command"),
		EvidenceHeadVersion:   17,
		EvidenceLastEventHash: testDigest("evidence-last-event"),
		EvidenceLastEventID:   "20000000-0000-4000-8000-000000000001",
		EvidencePlanHash:      receipt.PlanAuthority.EvidencePlanHash,
		EvidenceTrustDigest:   testDigest("evidence-trust"),
		InputHash:             receipt.PlanAuthority.InputHash,
		Kind:                  lookup.Kind,
		OperationID:           lookup.OperationID.String(),
		OrchestrationID:       receipt.Evidence.OrchestrationID,
		PlanAuthorityHash:     receipt.PlanAuthority.AuthorityHash,
		PlanAuthorityID:       lookup.AuthorityID.String(),
		ProjectionHash:        receipt.PlanAuthority.ProjectionHash,
		SchemaVersion:         ControlRequestSchemaV1,
		SnapshotID:            receipt.Snapshot.SnapshotID,
		TargetHash:            receipt.PlanAuthority.TargetHash,
		TrustBindingsDigest:   receipt.PlanAuthority.TrustBindingsDigest,
		TrustHash:             receipt.PlanAuthority.TrustHash,
		TrustPolicyDigest:     receipt.Trust.TrustPolicyDigest,
	}
	var requests []ResolvedControlRequest
	switch lookup.Kind {
	case RequestKindSnapshotSeal:
		base.Role = ControlRoleSealer
		base.OperationalAuthorityID = receipt.Trust.TrustBindings.SealerAuthorityID
		base.AuthenticationKeyID = "sealer-key-v1"
		requests = []ResolvedControlRequest{resolvedControlMaterial(tinyTestingT{}, base, nil)}
	case RequestKindSnapshotVerify:
		base.Role = ControlRoleVerifier
		base.OperationalAuthorityID = receipt.Trust.TrustBindings.VerifierAuthorityID
		base.AuthenticationKeyID = "verifier-key-v1"
		base.SnapshotDigest = receipt.Snapshot.SnapshotDigest
		requests = []ResolvedControlRequest{resolvedControlMaterial(tinyTestingT{}, base, nil)}
	case RequestKindReceiptSign:
		compiled, err := Compile(receipt)
		if err != nil {
			return ControlResolution{}, err
		}
		base.OperationalAuthorityID = receipt.Trust.TrustBindings.ReceiptAuthorityID
		base.SnapshotDigest = receipt.Snapshot.SnapshotDigest
		base.ReceiptID = receipt.ReceiptID
		base.PayloadDigest = compiled.PayloadDigest
		base.PAEDigest = SHA256Digest(templateauthority.DSSEPAE(InTotoPayloadType, compiled.Payload))
		for _, signer := range []struct {
			role    ControlRole
			binding SignerIdentityBinding
		}{{ControlRoleRunner, receipt.Signers.Runner}, {ControlRoleReleaseApprover, receipt.Signers.Approver}} {
			request := base
			request.Role = signer.role
			request.AuthenticationKeyID = signer.binding.KeyID
			request.SignerIdentity = signer.binding.Identity
			request.SignerKeyID = signer.binding.KeyID
			requests = append(requests, resolvedControlMaterial(tinyTestingT{}, request, compiled.Payload))
		}
	default:
		return ControlResolution{}, ErrControlInvalid
	}
	if resolver.mutate != nil {
		resolver.mutate(&requests[0])
	}
	return ControlResolution{Requests: requests}, nil
}

// tinyTestingT lets fixture builders share one panic-on-impossible canonical
// helper without retaining *testing.T in concurrent resolver calls.
type tinyTestingT struct{}

func (tinyTestingT) fatal(err error) { panic(err) }

func resolvedControlMaterial(helper tinyTestingT, request ControlRequest, payload []byte) ResolvedControlRequest {
	requestBytes, err := CanonicalJSON(request)
	if err != nil {
		helper.fatal(err)
	}
	result := ResolvedControlRequest{Request: request, RequestBytes: requestBytes, RequestHash: SHA256Digest(requestBytes)}
	if len(payload) != 0 {
		result.Payload = bytes.Clone(payload)
		result.PayloadHash = SHA256Digest(payload)
		result.PAE = templateauthority.DSSEPAE(InTotoPayloadType, payload)
		result.PAEHash = SHA256Digest(result.PAE)
	}
	return result
}

type fakeControlObservationResolver struct {
	mu      sync.Mutex
	entries map[uuid.UUID]ResolvedObservation
}

func (resolver *fakeControlObservationResolver) ResolveObservation(_ context.Context, lookup ObservationLookup) (ResolvedObservation, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	entry, ok := resolver.entries[lookup.ObservationAuthorityID]
	if !ok || entry.AuthenticationPayload.RequestHash != lookup.RequestHash {
		return ResolvedObservation{}, ErrControlNotFound
	}
	return cloneResolvedControlObservation(entry), nil
}

func (resolver *fakeControlObservationResolver) add(id uuid.UUID, observation ResolvedObservation) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.entries[id] = cloneResolvedControlObservation(observation)
}

func cloneResolvedControlObservation(value ResolvedObservation) ResolvedObservation {
	cloned := value
	cloned.AuthenticationPayloadBytes = bytes.Clone(value.AuthenticationPayloadBytes)
	cloned.AuthenticationBytes = bytes.Clone(value.AuthenticationBytes)
	cloned.Result = append([]byte(nil), value.Result...)
	cloned.ResultBytes = bytes.Clone(value.ResultBytes)
	cloned.Signature = bytes.Clone(value.Signature)
	cloned.ClaimBytes = bytes.Clone(value.ClaimBytes)
	cloned.AckBytes = bytes.Clone(value.AckBytes)
	return cloned
}

type controlFixture struct {
	receipt Receipt
	plan    *fakeControlPlanResolver
	obs     *fakeControlObservationResolver
	store   *MemoryControlStore
	service *ControlService
}

type reconciliationControlStore struct {
	ControlStore
	appendObservationErr error
	completeErr          error
	corruptObservation   bool
	corruptCompletion    bool
}

func (store *reconciliationControlStore) AppendObservation(ctx context.Context, candidate ObservationRecord) (ObservationRecord, error) {
	stored, err := store.ControlStore.AppendObservation(ctx, candidate)
	if err != nil {
		return ObservationRecord{}, err
	}
	if store.appendObservationErr != nil {
		return ObservationRecord{}, store.appendObservationErr
	}
	return stored, nil
}

func (store *reconciliationControlStore) InspectObservation(ctx context.Context, requestHash string, sequence uint64) (ObservationRecord, error) {
	stored, err := store.ControlStore.InspectObservation(ctx, requestHash, sequence)
	if err == nil && store.corruptObservation {
		stored.RecordedAt = time.Time{}
		stored.RecordHash = ""
	}
	return stored, err
}

func (store *reconciliationControlStore) Complete(ctx context.Context, candidate CompletionRecord) (CompletionRecord, error) {
	stored, err := store.ControlStore.Complete(ctx, candidate)
	if err != nil {
		return CompletionRecord{}, err
	}
	if store.completeErr != nil {
		return CompletionRecord{}, store.completeErr
	}
	return stored, nil
}

func (store *reconciliationControlStore) InspectCompletion(ctx context.Context, authorityID uuid.UUID) (CompletionRecord, error) {
	stored, err := store.ControlStore.InspectCompletion(ctx, authorityID)
	if err == nil && store.corruptCompletion {
		stored.CompletedAt = time.Time{}
		stored.Document = CompletionDocument{}
		stored.DocumentBytes = nil
		stored.DocumentHash = ""
	}
	return stored, err
}

func newControlFixture(t *testing.T) *controlFixture {
	t.Helper()
	receipt := validReceipt(t)
	bootstrap := &fakeControlPlanResolver{receipt: receipt}
	sealResolution, err := bootstrap.ResolveControl(context.Background(), ControlLookup{
		AuthorityID: uuid.MustParse(receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(receipt.Snapshot.OperationID),
		Kind:        RequestKindSnapshotSeal,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Snapshot.RequestDigest = sealResolution.Requests[0].RequestHash
	if _, err := Compile(receipt); err != nil {
		t.Fatal(err)
	}
	plan := &fakeControlPlanResolver{receipt: receipt}
	observations := &fakeControlObservationResolver{entries: make(map[uuid.UUID]ResolvedObservation)}
	current := time.Date(2026, 7, 19, 12, 1, 31, 0, time.UTC)
	store := NewMemoryControlStore(func() time.Time {
		result := current
		current = current.Add(20 * time.Second)
		return result
	})
	verifier, _ := verifierForReceipt(t, receipt)
	service, err := NewControlService(plan, observations, store, verifier)
	if err != nil {
		t.Fatal(err)
	}
	return &controlFixture{receipt: receipt, plan: plan, obs: observations, store: store, service: service}
}

func (fixture *controlFixture) replaceMemoryStore(t *testing.T, store *MemoryControlStore) {
	t.Helper()
	service, err := NewControlService(fixture.plan, fixture.obs, store, fixture.service.verifier)
	if err != nil {
		t.Fatal(err)
	}
	fixture.store = store
	fixture.service = service
}

func controlClockSequence(t *testing.T, values ...time.Time) func() time.Time {
	t.Helper()
	index := 0
	return func() time.Time {
		if index >= len(values) {
			t.Fatalf("control Store clock called %d times, only %d values supplied", index+1, len(values))
			return time.Time{}
		}
		value := values[index]
		index++
		return value
	}
}

func controlTestTime(minute, second int) time.Time {
	return time.Date(2026, 7, 19, 12, minute, second, 0, time.UTC)
}

func (fixture *controlFixture) observe(
	t *testing.T,
	request RequestRecord,
	generation, sequence uint64,
	status ObservationStatus,
	result any,
	signature []byte,
	pending *ObservationRecord,
) ObservationRecord {
	t.Helper()
	id := uuid.New()
	resolved := resolvedControlObservation(t, request, generation, sequence, status, result, signature, pending)
	fixture.obs.add(id, resolved)
	record, err := fixture.service.Observe(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: id})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func resolvedControlObservation(
	t *testing.T,
	request RequestRecord,
	generation, sequence uint64,
	status ObservationStatus,
	result any,
	signature []byte,
	pending *ObservationRecord,
) ResolvedObservation {
	t.Helper()
	value := ResolvedObservation{
		Generation: generation, Sequence: sequence, Status: status,
		ObservedAt:          time.Date(2026, 7, 19, 13, 10, int(sequence), 0, time.UTC),
		AuthenticationKeyID: request.Request.AuthenticationKeyID,
		Signature:           bytes.Clone(signature),
	}
	if len(signature) != 0 {
		value.SignatureHash = SHA256Digest(signature)
	}
	if result != nil {
		resultBytes, err := CanonicalJSON(result)
		if err != nil {
			t.Fatal(err)
		}
		value.Result = append([]byte(nil), resultBytes...)
		value.ResultBytes = resultBytes
		value.ResultHash = SHA256Digest(resultBytes)
	}
	if status == ObservationNotInvoked {
		if pending == nil {
			t.Fatal("not-invoked requires pending")
		}
		value.Claim = ClaimToken{
			PlanAuthorityID: request.Key.AuthorityID.String(), OperationalAuthorityID: request.Request.OperationalAuthorityID,
			ClaimID: uuid.NewString(), Generation: generation, Kind: request.Key.Kind, OperationID: request.Key.OperationID.String(),
			PendingEnvelopeHash: pending.AuthenticationEnvelopeHash, RequestHash: request.RequestHash,
			Role: request.Key.Role, SchemaVersion: ControlClaimTokenSchemaV1,
		}
		value.ClaimBytes, _ = CanonicalJSON(value.Claim)
		value.ClaimTokenHash = SHA256Digest(value.ClaimBytes)
		value.Acknowledgement = AcknowledgementToken{
			AcknowledgementID: uuid.NewString(), ClaimTokenHash: value.ClaimTokenHash, RequestHash: request.RequestHash,
			SchemaVersion: ControlAcknowledgementSchemaV1, Status: ObservationNotInvoked,
		}
		value.AckBytes, _ = CanonicalJSON(value.Acknowledgement)
		value.AckTokenHash = SHA256Digest(value.AckBytes)
	}
	value.AuthenticationPayload = ObservationAuthenticationPayload{
		AcknowledgementTokenHash: value.AckTokenHash, AuthenticationKeyID: request.Request.AuthenticationKeyID,
		OperationalAuthorityID: request.Request.OperationalAuthorityID, PlanAuthorityID: request.Key.AuthorityID.String(),
		ClaimTokenHash: value.ClaimTokenHash, Generation: generation, Kind: request.Key.Kind,
		ObservedAt: value.ObservedAt.Format(canonicalTimeLayout), OperationID: request.Key.OperationID.String(),
		RequestHash: request.RequestHash, ResultHash: value.ResultHash, Role: request.Key.Role,
		SchemaVersion: ControlObservationPayloadSchemaV1, Sequence: sequence, SignatureHash: value.SignatureHash, Status: status,
	}
	refreshResolvedAuthentication(t, request, &value)
	return value
}

func refreshResolvedAuthentication(t *testing.T, request RequestRecord, value *ResolvedObservation) {
	t.Helper()
	value.AuthenticationPayloadBytes, _ = CanonicalJSON(value.AuthenticationPayload)
	value.AuthenticationPayloadHash = SHA256Digest(value.AuthenticationPayloadBytes)
	seed := sha256.Sum256([]byte("control-auth:" + request.Request.AuthenticationKeyID))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	value.AuthenticationEnvelope = ObservationAuthenticationEnvelope{
		Algorithm: ControlAuthenticationEd25519, KeyID: request.Request.AuthenticationKeyID,
		OperationalAuthorityID: request.Request.OperationalAuthorityID,
		Payload:                base64.StdEncoding.EncodeToString(value.AuthenticationPayloadBytes), PayloadType: ControlObservationPayloadType,
		SchemaVersion: ControlObservationProofSchemaV1,
		Signature:     base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, value.AuthenticationPayloadBytes)),
	}
	value.AuthenticationBytes, _ = CanonicalJSON(value.AuthenticationEnvelope)
	value.AuthenticationEnvelopeHash = SHA256Digest(value.AuthenticationBytes)
}

func (fixture *controlFixture) commitSnapshots(t *testing.T) (RequestRecord, RequestRecord) {
	t.Helper()
	snapshotOperation := uuid.MustParse(fixture.receipt.Snapshot.OperationID)
	seal, err := fixture.service.StartSnapshotSeal(context.Background(), StartCommand{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID), OperationID: snapshotOperation,
	})
	if err != nil || !seal.CallOwnership || len(seal.Requests) != 1 {
		t.Fatalf("start seal = %+v, %v", seal, err)
	}
	sealPending := fixture.observe(t, seal.Requests[0], 1, 1, ObservationPending, nil, nil, nil)
	_ = sealPending
	fixture.observe(t, seal.Requests[0], 1, 2, ObservationCommitted, fixture.receipt.Snapshot, nil, nil)

	verify, err := fixture.service.StartSnapshotVerification(context.Background(), StartCommand{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID), OperationID: snapshotOperation,
	})
	if err != nil || !verify.CallOwnership || len(verify.Requests) != 1 {
		t.Fatalf("start verify = %+v, %v", verify, err)
	}
	fixture.observe(t, verify.Requests[0], 1, 1, ObservationPending, nil, nil, nil)
	fixture.observe(t, verify.Requests[0], 1, 2, ObservationCommitted, fixture.receipt.SnapshotVerification, nil, nil)
	return seal.Requests[0], verify.Requests[0]
}

func (fixture *controlFixture) commitSigning(t *testing.T) []RequestRecord {
	t.Helper()
	signing, err := fixture.service.StartSigning(context.Background(), StartCommand{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(fixture.receipt.OperationID),
	})
	if err != nil || !signing.CallOwnership || len(signing.Requests) != 2 {
		t.Fatalf("start signing = %+v, %v", signing, err)
	}
	runnerKey, approverKey := testKeys()
	keys := map[ControlRole]testSigningKey{ControlRoleRunner: runnerKey, ControlRoleReleaseApprover: approverKey}
	for _, request := range signing.Requests {
		fixture.observe(t, request, 1, 1, ObservationPending, nil, nil, nil)
		fixture.observe(t, request, 1, 2, ObservationCommitted, nil, ed25519.Sign(keys[request.Key.Role].private, request.PAE), nil)
	}
	return signing.Requests
}

func TestControlLifecycleCompletesOnlyFourDurableCommittedRequests(t *testing.T) {
	fixture := newControlFixture(t)
	seal, verification := fixture.commitSnapshots(t)
	if len(seal.Payload) != 0 || seal.Request.SnapshotDigest != "" || len(verification.Payload) != 0 || verification.Request.SnapshotDigest == "" {
		t.Fatal("snapshot request role closure drifted")
	}
	signing, err := fixture.service.StartSigning(context.Background(), StartCommand{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(fixture.receipt.OperationID),
	})
	if err != nil || !signing.CallOwnership || len(signing.Requests) != 2 || !samePayloadClosure(signing.Requests) {
		t.Fatalf("start signing = %+v, %v", signing, err)
	}
	runnerKey, approverKey := testKeys()
	keys := map[ControlRole]testSigningKey{ControlRoleRunner: runnerKey, ControlRoleReleaseApprover: approverKey}
	for _, request := range signing.Requests {
		pending := fixture.observe(t, request, 1, 1, ObservationPending, nil, nil, nil)
		if pending.RecordedAt.IsZero() || pending.RecordedAt.Equal(pending.ObservedAt) {
			t.Fatal("Store did not independently assign RecordedAt")
		}
		signature := ed25519.Sign(keys[request.Key.Role].private, request.PAE)
		fixture.observe(t, request, 1, 2, ObservationCommitted, nil, signature, nil)
	}
	requestByRole := map[ControlRole]RequestRecord{
		ControlRoleSealer: seal, ControlRoleVerifier: verification,
	}
	observationByRole := make(map[ControlRole]ObservationRecord, 4)
	for _, request := range append([]RequestRecord{seal, verification}, signing.Requests...) {
		requestByRole[request.Key.Role] = request
		observationByRole[request.Key.Role], err = fixture.store.InspectTerminalObservation(context.Background(), request.RequestHash)
		if err != nil {
			t.Fatal(err)
		}
	}
	tamperedEnvelope, err := buildControlDSSEEnvelope(
		signing.Requests[0].Payload, observationByRole[ControlRoleRunner], observationByRole[ControlRoleReleaseApprover],
	)
	if err != nil {
		t.Fatal(err)
	}
	tamperedCandidate := buildCompletionCandidate(CompletionCommand{
		AuthorityID:            uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		SnapshotOperationID:    uuid.MustParse(fixture.receipt.Snapshot.OperationID),
		ReceiptSignOperationID: uuid.MustParse(fixture.receipt.OperationID),
	}, requestByRole, observationByRole, tamperedEnvelope, fixture.receipt)
	tamperedCandidate.verificationEnvelopeHash = tamperedCandidate.EnvelopeDigest
	mixedCandidate := cloneCompletion(tamperedCandidate)
	mixedCandidate.RequestHashes.ApproverSign = mixedCandidate.RequestHashes.RunnerSign
	if _, err := fixture.store.Complete(context.Background(), mixedCandidate); !errors.Is(err, ErrControlNotReady) {
		t.Fatalf("Store accepted mixed-role completion sources: %v", err)
	}
	tamperedCandidate.Operations.Snapshot = uuid.NewString()
	if _, err := fixture.store.Complete(context.Background(), tamperedCandidate); !errors.Is(err, ErrControlConflict) {
		t.Fatalf("Store accepted spliced completion operations: %v", err)
	}
	fixture.store.InjectCompletionCommitUnknownOnce()
	completed, err := fixture.service.Complete(context.Background(), CompletionCommand{
		AuthorityID:            uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		SnapshotOperationID:    uuid.MustParse(fixture.receipt.Snapshot.OperationID),
		ReceiptSignOperationID: uuid.MustParse(fixture.receipt.OperationID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !completed.Idempotent {
		t.Fatal("completion commit-unknown did not reconcile as non-owning replay")
	}
	if completed.Document.SchemaVersion != ControlCompletionSchemaV1 || completed.EnvelopeDigest != SHA256Digest(completed.Envelope) ||
		!bytes.Equal(completed.Payload, signing.Requests[0].Payload) || completed.PayloadDigest != SHA256Digest(completed.Payload) {
		t.Fatalf("completion closure = %+v", completed.Document)
	}
	replay, err := fixture.service.Complete(context.Background(), CompletionCommand{
		AuthorityID:            uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		SnapshotOperationID:    uuid.MustParse(fixture.receipt.Snapshot.OperationID),
		ReceiptSignOperationID: uuid.MustParse(fixture.receipt.OperationID),
	})
	if err != nil || !replay.Idempotent || !bytes.Equal(replay.Envelope, completed.Envelope) {
		t.Fatalf("completion replay = %+v, %v", replay, err)
	}
}

func TestControlDualSigningStartIsAtomicAndSingleOwner(t *testing.T) {
	fixture := newControlFixture(t)
	fixture.commitSnapshots(t)
	command := StartCommand{AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID), OperationID: uuid.MustParse(fixture.receipt.OperationID)}
	const contenders = 24
	results := make(chan StartOutcome, contenders)
	errorsCh := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := fixture.service.StartSigning(context.Background(), command)
			results <- result
			errorsCh <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errorsCh)
	owners := 0
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		if len(result.Requests) != 2 {
			t.Fatalf("non-atomic signing rows = %d", len(result.Requests))
		}
		if result.CallOwnership {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("call owners = %d, want 1", owners)
	}
}

func TestControlCommitUnknownNeverGrantsCallOwnership(t *testing.T) {
	fixture := newControlFixture(t)
	fixture.store.InjectStartCommitUnknownOnce()
	result, err := fixture.service.StartSnapshotSeal(context.Background(), StartCommand{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(fixture.receipt.Snapshot.OperationID),
	})
	if err != nil || result.CallOwnership || len(result.Requests) != 1 {
		t.Fatalf("commit unknown start = %+v, %v", result, err)
	}
	result.Requests[0].RequestBytes[0] ^= 1
	inspected, err := fixture.store.InspectRequest(context.Background(), result.Requests[0].Key)
	if err != nil || bytes.Equal(inspected.RequestBytes, result.Requests[0].RequestBytes) {
		t.Fatal("store did not clone request bytes")
	}
}

func TestControlObservationCommitUnknownReconcilesOneExactRow(t *testing.T) {
	fixture := newControlFixture(t)
	start, err := fixture.service.StartSnapshotSeal(context.Background(), StartCommand{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(fixture.receipt.Snapshot.OperationID),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := start.Requests[0]
	id := uuid.New()
	fixture.obs.add(id, resolvedControlObservation(t, request, 1, 1, ObservationPending, nil, nil, nil))
	fixture.store.InjectObservationCommitUnknownOnce()
	reconciled, err := fixture.service.Observe(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: id})
	if err != nil || !reconciled.Idempotent || reconciled.Sequence != 1 {
		t.Fatalf("observation commit-unknown = %+v, %v", reconciled, err)
	}
	replay, err := fixture.service.Observe(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: id})
	if err != nil || !replay.Idempotent || replay.RecordHash != reconciled.RecordHash {
		t.Fatalf("observation replay = %+v, %v", replay, err)
	}
}

func TestControlObservationReconciliationRequiresStoredClosure(t *testing.T) {
	for _, test := range []struct {
		name      string
		appendErr error
		wantErr   error
	}{
		{name: "commit unknown", appendErr: ErrControlStoreOutcomeUnknown, wantErr: ErrControlOutcomeUnknown},
		{name: "conflict replay", appendErr: ErrControlConflict, wantErr: ErrControlConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newControlFixture(t)
			start, err := fixture.service.StartSnapshotSeal(context.Background(), StartCommand{
				AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
				OperationID: uuid.MustParse(fixture.receipt.Snapshot.OperationID),
			})
			if err != nil {
				t.Fatal(err)
			}
			request := start.Requests[0]
			observationID := uuid.New()
			fixture.obs.add(observationID, resolvedControlObservation(t, request, 1, 1, ObservationPending, nil, nil, nil))
			store := &reconciliationControlStore{
				ControlStore: fixture.store, appendObservationErr: test.appendErr, corruptObservation: true,
			}
			service, err := NewControlService(fixture.plan, fixture.obs, store, fixture.service.verifier)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Observe(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: observationID})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("reconciliation error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestControlRetryReplayRequiresStoredClosure(t *testing.T) {
	fixture := newControlFixture(t)
	start, err := fixture.service.StartSnapshotSeal(context.Background(), StartCommand{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(fixture.receipt.Snapshot.OperationID),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := start.Requests[0]
	pending := fixture.observe(t, request, 1, 1, ObservationPending, nil, nil, nil)
	fixture.observe(t, request, 1, 2, ObservationNotInvoked, nil, nil, &pending)
	retryID := uuid.New()
	fixture.obs.add(retryID, resolvedControlObservation(t, request, 2, 3, ObservationPending, nil, nil, nil))
	if _, err := fixture.service.AcquireRetry(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: retryID}); err != nil {
		t.Fatal(err)
	}
	store := &reconciliationControlStore{ControlStore: fixture.store, corruptObservation: true}
	service, err := NewControlService(fixture.plan, fixture.obs, store, fixture.service.verifier)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcquireRetry(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: retryID}); !errors.Is(err, ErrControlConflict) {
		t.Fatalf("retry replay with incomplete Store closure error = %v", err)
	}
}

func TestControlCompletionCommitUnknownRequiresStoredClosure(t *testing.T) {
	fixture := newControlFixture(t)
	fixture.commitSnapshots(t)
	fixture.commitSigning(t)
	store := &reconciliationControlStore{
		ControlStore: fixture.store, completeErr: ErrControlStoreOutcomeUnknown, corruptCompletion: true,
	}
	service, err := NewControlService(fixture.plan, fixture.obs, store, fixture.service.verifier)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Complete(context.Background(), CompletionCommand{
		AuthorityID:            uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		SnapshotOperationID:    uuid.MustParse(fixture.receipt.Snapshot.OperationID),
		ReceiptSignOperationID: uuid.MustParse(fixture.receipt.OperationID),
	})
	if !errors.Is(err, ErrControlOutcomeUnknown) {
		t.Fatalf("completion reconciliation error = %v", err)
	}
}

func TestControlStoreClockRegressionFailsClosed(t *testing.T) {
	fixture := newControlFixture(t)
	times := []time.Time{
		time.Date(2026, 7, 19, 12, 1, 31, 0, time.UTC),
		time.Date(2026, 7, 19, 12, 1, 30, 0, time.UTC),
	}
	index := 0
	store := NewMemoryControlStore(func() time.Time {
		result := times[index]
		if index < len(times)-1 {
			index++
		}
		return result
	})
	service, err := NewControlService(fixture.plan, fixture.obs, store, fixture.service.verifier)
	if err != nil {
		t.Fatal(err)
	}
	start, err := service.StartSnapshotSeal(context.Background(), StartCommand{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(fixture.receipt.Snapshot.OperationID),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := start.Requests[0]
	id := uuid.New()
	fixture.obs.add(id, resolvedControlObservation(t, request, 1, 1, ObservationPending, nil, nil, nil))
	if _, err := service.Observe(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: id}); !errors.Is(err, ErrControlInvalid) {
		t.Fatalf("regressed Store clock error = %v", err)
	}
}

func TestControlStageStartRejectsPrerequisiteClockRegression(t *testing.T) {
	t.Run("snapshot verification", func(t *testing.T) {
		fixture := newControlFixture(t)
		store := NewMemoryControlStore(controlClockSequence(t,
			controlTestTime(1, 0),
			controlTestTime(1, 30),
			controlTestTime(2, 10),
			controlTestTime(2, 9),
		))
		fixture.replaceMemoryStore(t, store)
		start, err := fixture.service.StartSnapshotSeal(context.Background(), StartCommand{
			AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
			OperationID: uuid.MustParse(fixture.receipt.Snapshot.OperationID),
		})
		if err != nil {
			t.Fatal(err)
		}
		request := start.Requests[0]
		fixture.observe(t, request, 1, 1, ObservationPending, nil, nil, nil)
		fixture.observe(t, request, 1, 2, ObservationCommitted, fixture.receipt.Snapshot, nil, nil)
		_, err = fixture.service.StartSnapshotVerification(context.Background(), StartCommand{
			AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
			OperationID: uuid.MustParse(fixture.receipt.Snapshot.OperationID),
		})
		if !errors.Is(err, ErrControlInvalid) {
			t.Fatalf("regressed verification start error = %v", err)
		}
	})

	t.Run("receipt signing", func(t *testing.T) {
		fixture := newControlFixture(t)
		store := NewMemoryControlStore(controlClockSequence(t,
			controlTestTime(1, 0),
			controlTestTime(1, 30),
			controlTestTime(2, 10),
			controlTestTime(2, 10),
			controlTestTime(2, 20),
			controlTestTime(3, 10),
			controlTestTime(3, 9),
		))
		fixture.replaceMemoryStore(t, store)
		fixture.commitSnapshots(t)
		_, err := fixture.service.StartSigning(context.Background(), StartCommand{
			AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
			OperationID: uuid.MustParse(fixture.receipt.OperationID),
		})
		if !errors.Is(err, ErrControlInvalid) {
			t.Fatalf("regressed signing start error = %v", err)
		}
	})
}

func TestControlCompletionTimeMustStrictlyFollowEverySource(t *testing.T) {
	fixture := newControlFixture(t)
	store := NewMemoryControlStore(controlClockSequence(t,
		controlTestTime(1, 0),
		controlTestTime(1, 30),
		controlTestTime(2, 10),
		controlTestTime(2, 10),
		controlTestTime(2, 20),
		controlTestTime(3, 10),
		controlTestTime(3, 10),
		controlTestTime(3, 20),
		controlTestTime(3, 30),
		controlTestTime(3, 40),
		controlTestTime(3, 50),
		controlTestTime(3, 50),
	))
	fixture.replaceMemoryStore(t, store)
	fixture.commitSnapshots(t)
	fixture.commitSigning(t)
	_, err := fixture.service.Complete(context.Background(), CompletionCommand{
		AuthorityID:            uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		SnapshotOperationID:    uuid.MustParse(fixture.receipt.Snapshot.OperationID),
		ReceiptSignOperationID: uuid.MustParse(fixture.receipt.OperationID),
	})
	if !errors.Is(err, ErrControlInvalid) {
		t.Fatalf("completion equal to latest source time error = %v", err)
	}
}

func TestControlRetryRequiresExactAuthenticatedNotInvokedClosure(t *testing.T) {
	fixture := newControlFixture(t)
	start, err := fixture.service.StartSnapshotSeal(context.Background(), StartCommand{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(fixture.receipt.Snapshot.OperationID),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := start.Requests[0]
	pending := fixture.observe(t, request, 1, 1, ObservationPending, nil, nil, nil)

	badID := uuid.New()
	bad := resolvedControlObservation(t, request, 1, 2, ObservationNotInvoked, nil, nil, &pending)
	bad.Claim.PendingEnvelopeHash = testDigest("wrong-pending")
	bad.ClaimBytes, _ = CanonicalJSON(bad.Claim)
	bad.ClaimTokenHash = SHA256Digest(bad.ClaimBytes)
	bad.Acknowledgement.ClaimTokenHash = bad.ClaimTokenHash
	bad.AckBytes, _ = CanonicalJSON(bad.Acknowledgement)
	bad.AckTokenHash = SHA256Digest(bad.AckBytes)
	bad.AuthenticationPayload.ClaimTokenHash = bad.ClaimTokenHash
	bad.AuthenticationPayload.AcknowledgementTokenHash = bad.AckTokenHash
	refreshResolvedAuthentication(t, request, &bad)
	fixture.obs.add(badID, bad)
	if _, err := fixture.service.Observe(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: badID}); !errors.Is(err, ErrControlConflict) {
		t.Fatalf("forged pending hash error = %v", err)
	}

	notInvoked := fixture.observe(t, request, 1, 2, ObservationNotInvoked, nil, nil, &pending)
	if notInvoked.Status != ObservationNotInvoked {
		t.Fatal("not-invoked was not stored")
	}
	retryID := uuid.New()
	retryPending := resolvedControlObservation(t, request, 2, 3, ObservationPending, nil, nil, nil)
	fixture.obs.add(retryID, retryPending)
	retry, err := fixture.service.AcquireRetry(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: retryID})
	if err != nil || !retry.CallOwnership || retry.Observation.Generation != 2 {
		t.Fatalf("retry acquire = %+v, %v", retry, err)
	}
	replay, err := fixture.service.AcquireRetry(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: retryID})
	if err != nil || replay.CallOwnership {
		t.Fatalf("retry replay = %+v, %v", replay, err)
	}

	oldID := uuid.New()
	oldGeneration := resolvedControlObservation(t, request, 1, 4, ObservationRejected, nil, nil, nil)
	fixture.obs.add(oldID, oldGeneration)
	if _, err := fixture.service.Observe(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: oldID}); !errors.Is(err, ErrControlConflict) {
		t.Fatalf("old generation append error = %v", err)
	}
	generationTwoNotInvoked := fixture.observe(t, request, 2, 4, ObservationNotInvoked, nil, nil, &retry.Observation)
	if generationTwoNotInvoked.Generation != 2 {
		t.Fatal("generation two not-invoked drifted")
	}
	unknownID := uuid.New()
	fixture.obs.add(unknownID, resolvedControlObservation(t, request, 3, 5, ObservationPending, nil, nil, nil))
	fixture.store.InjectObservationCommitUnknownOnce()
	unknown, err := fixture.service.AcquireRetry(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: unknownID})
	if err != nil || unknown.CallOwnership || unknown.Observation.Generation != 3 {
		t.Fatalf("commit-unknown retry = %+v, %v", unknown, err)
	}
}

func TestControlRejectsPrematureAndTamperedPhaseMaterial(t *testing.T) {
	fixture := newControlFixture(t)
	authorityID := uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID)
	snapshotOperation := uuid.MustParse(fixture.receipt.Snapshot.OperationID)
	if _, err := fixture.service.StartSnapshotVerification(context.Background(), StartCommand{AuthorityID: authorityID, OperationID: snapshotOperation}); !errors.Is(err, ErrControlNotReady) {
		t.Fatalf("premature verify error = %v", err)
	}
	fixture = newControlFixture(t)
	fixture.plan.mutate = func(request *ResolvedControlRequest) {
		request.Request.SnapshotDigest = testDigest("illegal-presealed-snapshot")
		request.RequestBytes, _ = CanonicalJSON(request.Request)
		request.RequestHash = SHA256Digest(request.RequestBytes)
	}
	if _, err := fixture.service.StartSnapshotSeal(context.Background(), StartCommand{AuthorityID: authorityID, OperationID: snapshotOperation}); !errors.Is(err, ErrControlInvalid) {
		t.Fatalf("seal role tamper error = %v", err)
	}
}

func TestControlSigningRejectsHistoricalV2Payload(t *testing.T) {
	fixture := newControlFixture(t)
	fixture.commitSnapshots(t)
	fixture.plan.mutate = func(request *ResolvedControlRequest) {
		request.Payload = bytes.ReplaceAll(request.Payload, []byte("receipt/v3"), []byte("receipt/v2"))
		request.PayloadHash = SHA256Digest(request.Payload)
		request.PAE = templateauthority.DSSEPAE(InTotoPayloadType, request.Payload)
		request.PAEHash = SHA256Digest(request.PAE)
		request.Request.PayloadDigest = request.PayloadHash
		request.Request.PAEDigest = request.PAEHash
		request.RequestBytes, _ = CanonicalJSON(request.Request)
		request.RequestHash = SHA256Digest(request.RequestBytes)
	}
	_, err := fixture.service.StartSigning(context.Background(), StartCommand{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(fixture.receipt.OperationID),
	})
	if !errors.Is(err, ErrControlInvalid) {
		t.Fatalf("historical v2 signing payload error = %v", err)
	}
}

func TestControlSealResultMustBindExactStartedRequest(t *testing.T) {
	fixture := newControlFixture(t)
	start, err := fixture.service.StartSnapshotSeal(context.Background(), StartCommand{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(fixture.receipt.Snapshot.OperationID),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := start.Requests[0]
	badProof := resolvedControlObservation(t, request, 1, 1, ObservationPending, nil, nil, nil)
	badProof.AuthenticationEnvelope.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize-1))
	badProof.AuthenticationBytes, _ = CanonicalJSON(badProof.AuthenticationEnvelope)
	badProof.AuthenticationEnvelopeHash = SHA256Digest(badProof.AuthenticationBytes)
	badProofID := uuid.New()
	fixture.obs.add(badProofID, badProof)
	if _, err := fixture.service.Observe(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: badProofID}); !errors.Is(err, ErrControlInvalid) {
		t.Fatalf("invalid durable auth proof error = %v", err)
	}
	badPayload := resolvedControlObservation(t, request, 1, 1, ObservationPending, nil, nil, nil)
	badPayload.AuthenticationEnvelope.Payload = base64.StdEncoding.EncodeToString([]byte(`{"wrong":"payload"}`))
	badPayload.AuthenticationBytes, _ = CanonicalJSON(badPayload.AuthenticationEnvelope)
	badPayload.AuthenticationEnvelopeHash = SHA256Digest(badPayload.AuthenticationBytes)
	badPayloadID := uuid.New()
	fixture.obs.add(badPayloadID, badPayload)
	if _, err := fixture.service.Observe(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: badPayloadID}); !errors.Is(err, ErrControlInvalid) {
		t.Fatalf("authentication payload substitution error = %v", err)
	}
	badBase64 := resolvedControlObservation(t, request, 1, 1, ObservationPending, nil, nil, nil)
	badBase64.AuthenticationEnvelope.Payload = "***"
	badBase64.AuthenticationBytes, _ = CanonicalJSON(badBase64.AuthenticationEnvelope)
	badBase64.AuthenticationEnvelopeHash = SHA256Digest(badBase64.AuthenticationBytes)
	badBase64ID := uuid.New()
	fixture.obs.add(badBase64ID, badBase64)
	if _, err := fixture.service.Observe(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: badBase64ID}); !errors.Is(err, ErrControlInvalid) {
		t.Fatalf("authentication base64 error = %v", err)
	}
	fixture.observe(t, request, 1, 1, ObservationPending, nil, nil, nil)
	tampered := fixture.receipt.Snapshot
	tampered.RequestDigest = testDigest("another-seal-request")
	resolved := resolvedControlObservation(t, request, 1, 2, ObservationCommitted, tampered, nil, nil)
	id := uuid.New()
	fixture.obs.add(id, resolved)
	if _, err := fixture.service.Observe(context.Background(), ObservationCommand{Request: request.Key, ObservationAuthorityID: id}); !errors.Is(err, ErrControlInvalid) {
		t.Fatalf("seal request digest tamper error = %v", err)
	}
}

func TestControlClaimAndAcknowledgementIdentitiesAreGloballyOneUse(t *testing.T) {
	fixture := newControlFixture(t)
	fixture.commitSnapshots(t)
	signing, err := fixture.service.StartSigning(context.Background(), StartCommand{
		AuthorityID: uuid.MustParse(fixture.receipt.PlanAuthority.AuthorityID),
		OperationID: uuid.MustParse(fixture.receipt.OperationID),
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := make(map[ControlRole]ObservationRecord, 2)
	for _, request := range signing.Requests {
		pending[request.Key.Role] = fixture.observe(t, request, 1, 1, ObservationPending, nil, nil, nil)
	}
	firstRequest := signing.Requests[0]
	firstPending := pending[firstRequest.Key.Role]
	first := fixture.observe(t, firstRequest, 1, 2, ObservationNotInvoked, nil, nil, &firstPending)
	secondRequest := signing.Requests[1]
	secondPending := pending[secondRequest.Key.Role]
	reused := resolvedControlObservation(t, secondRequest, 1, 2, ObservationNotInvoked, nil, nil, &secondPending)
	reused.Claim.ClaimID = first.Claim.ClaimID
	reused.ClaimBytes, _ = CanonicalJSON(reused.Claim)
	reused.ClaimTokenHash = SHA256Digest(reused.ClaimBytes)
	reused.Acknowledgement.AcknowledgementID = first.Acknowledgement.AcknowledgementID
	reused.Acknowledgement.ClaimTokenHash = reused.ClaimTokenHash
	reused.AckBytes, _ = CanonicalJSON(reused.Acknowledgement)
	reused.AckTokenHash = SHA256Digest(reused.AckBytes)
	reused.AuthenticationPayload.ClaimTokenHash = reused.ClaimTokenHash
	reused.AuthenticationPayload.AcknowledgementTokenHash = reused.AckTokenHash
	refreshResolvedAuthentication(t, secondRequest, &reused)
	id := uuid.New()
	fixture.obs.add(id, reused)
	if _, err := fixture.service.Observe(context.Background(), ObservationCommand{Request: secondRequest.Key, ObservationAuthorityID: id}); !errors.Is(err, ErrControlConflict) {
		t.Fatalf("cross-request claim/ACK reuse error = %v", err)
	}
}
