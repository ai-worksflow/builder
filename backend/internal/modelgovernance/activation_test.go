package modelgovernance

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"
)

type activationServiceTestFixture struct {
	now              time.Time
	clock            *time.Time
	keys             governanceTestKeys
	store            *MemoryActivationStore
	authority        *MemoryGovernanceAuthority
	service          *ActivationService
	baselineFixture  governanceFixture
	baselineRecord   ActivationRecord
	candidateFixture governanceFixture
	candidateRequest ActivationRequest
}

type governanceTypedNilContext struct{}

func (*governanceTypedNilContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*governanceTypedNilContext) Done() <-chan struct{}       { return nil }
func (*governanceTypedNilContext) Err() error                  { return nil }
func (*governanceTypedNilContext) Value(any) any               { return nil }

type currentPolicyOverrideAuthority struct {
	GovernanceAuthority
	policy GovernanceTrustPolicy
}

func (authority currentPolicyOverrideAuthority) CurrentGovernanceTrustPolicy(context.Context) (GovernanceTrustPolicy, error) {
	return authority.policy, nil
}

func TestActivationServiceCASResolveAndIdempotentInspection(t *testing.T) {
	fixture := newActivationServiceTestFixture(t, false, false)
	ctx := context.Background()
	record, err := fixture.service.Activate(ctx, fixture.candidateRequest)
	if err != nil {
		t.Fatalf("activate exact governance receipt: %v", err)
	}
	if record.Generation != 2 || record.ReceiptDigest != fixture.candidateFixture.receiptDigest {
		t.Fatal("activation result lost generation or receipt authority")
	}
	replayed, err := fixture.service.Activate(ctx, fixture.candidateRequest)
	if err != nil || !sameActivationRecord(replayed, record) {
		t.Fatalf("same operation did not resolve idempotently: %v", err)
	}
	resolved, err := fixture.service.ResolveActive(ctx, record.Workload)
	if err != nil {
		t.Fatalf("resolve active authority: %v", err)
	}
	if !sameActivationRecord(resolved.Primary, record) || len(resolved.Graph) != 1 || resolved.Graph[0].Verified.Profile.ID != record.ProfileID {
		t.Fatal("runtime resolution did not close the exact active graph")
	}
	if !resolved.AuthorityObservedAt.Equal(fixture.now) || resolved.GovernanceRevocationEpoch != fixture.keys.revocations.Epoch ||
		resolved.GovernanceRevocationHash != fixture.keys.revocations.AuthorityHash {
		t.Fatal("runtime resolution did not expose the exact revocation authority observation")
	}
}

func TestActivationServiceNormalizesTrustedTimeAndRejectsNilBoundaries(t *testing.T) {
	fixture := newActivationServiceTestFixture(t, false, false)
	*fixture.clock = fixture.now.Add(789 * time.Microsecond)
	record, err := fixture.service.Activate(context.Background(), fixture.candidateRequest)
	if err != nil {
		t.Fatalf("activate with sub-millisecond trusted clock: %v", err)
	}
	if !canonicalGovernanceTime(record.ActivatedAt) || !record.ActivatedAt.Equal(fixture.now) {
		t.Fatalf("trusted time was not normalized to canonical UTC milliseconds: %s", record.ActivatedAt)
	}

	nilFixture := newActivationServiceTestFixture(t, false, false)
	var typedNilContext *governanceTypedNilContext
	if _, err := nilFixture.service.Activate(typedNilContext, nilFixture.candidateRequest); err == nil {
		t.Fatal("typed-nil activation context was accepted")
	}
	if _, err := nilFixture.service.ResolveActive(typedNilContext, nilFixture.baselineRecord.Workload); err == nil {
		t.Fatal("typed-nil resolution context was accepted")
	}
	var typedNilStore *MemoryActivationStore
	if _, err := NewActivationService(typedNilStore, nilFixture.authority, NewGovernanceVerifier()); err == nil {
		t.Fatal("typed-nil activation store was accepted")
	}
	var typedNilAuthority *MemoryGovernanceAuthority
	if _, err := NewActivationService(nilFixture.store, typedNilAuthority, NewGovernanceVerifier()); err == nil {
		t.Fatal("typed-nil governance authority was accepted")
	}
	var nilService *ActivationService
	if _, err := nilService.Activate(context.Background(), nilFixture.candidateRequest); err == nil {
		t.Fatal("nil activation service receiver was accepted")
	}
}

func TestActivationRejectsInvalidCurrentPolicyBeforeDurableObservation(t *testing.T) {
	fixture := newActivationServiceTestFixture(t, false, false)
	nextRevocations := nextGovernanceRevocationAuthority(t, fixture.keys.revocations, fixture.now, nil, nil)
	if err := fixture.authority.SetRevocationAuthority(nextRevocations); err != nil {
		t.Fatal(err)
	}
	invalidPolicy := cloneGovernanceTrustPolicy(fixture.keys.policy)
	delete(invalidPolicy.Signers, RoleGenesisApprover)
	invalidPolicy.PolicyHash = testDigest("invalid-current-policy")
	service, err := NewActivationService(fixture.store, currentPolicyOverrideAuthority{
		GovernanceAuthority: fixture.authority,
		policy:              invalidPolicy,
	}, NewGovernanceVerifier())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(context.Background(), fixture.candidateRequest); err == nil || !errors.Is(err, ErrRuntimeAuthority) {
		t.Fatalf("invalid current policy was accepted or misclassified: %v", err)
	}
	fixture.store.mu.RLock()
	defer fixture.store.mu.RUnlock()
	if fixture.store.trustPolicyAnchor == nil || fixture.store.trustPolicyAnchor.PolicyHash != fixture.keys.policy.PolicyHash ||
		fixture.store.trustPolicyAnchor.RevocationEpoch != fixture.keys.revocations.Epoch {
		t.Fatal("invalid current policy poisoned the durable trust-policy observation")
	}
}

func TestActivationServiceRejectsMissingBootstrapAndBaselineDrift(t *testing.T) {
	fixture := newActivationServiceTestFixture(t, false, false)
	emptyStore, err := NewMemoryActivationStore(func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	emptyService, err := NewActivationService(emptyStore, fixture.authority, NewGovernanceVerifier())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emptyService.Activate(context.Background(), fixture.candidateRequest); err == nil || !errors.Is(err, ErrRuntimeAuthority) {
		t.Fatalf("first activation without signed bootstrap authority was accepted: %v", err)
	}
	structuralBootstrap := structuralActivationRecord(
		"71000000-0000-4000-8000-000000000099", "88888888-8888-4888-8888-888888888888",
		fixture.baselineRecord.Workload, 1, testDigest("unsigned-bootstrap-fence"), fixture.now,
	)
	if _, err := emptyStore.AppendActivation(context.Background(), ActivationAppend{
		ExpectedGeneration: 0, ExpectedFence: structuralBootstrap.PreviousFence, Record: structuralBootstrap,
	}); err == nil || !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("memory store bypassed the ordinary no-empty-head fence: %v", err)
	}

	driftedBaseline := BaselineBinding{
		ActivationFence: fixture.baselineRecord.Fence, Generation: fixture.baselineRecord.Generation,
		MetricsHash: testDigest("drifted-baseline-metrics"),
		Profile: CorpusProfileBinding{
			ID: fixture.baselineRecord.ProfileID, ContentHash: fixture.baselineRecord.ProfileContentHash, Workload: fixture.baselineRecord.Workload,
		},
		ReceiptDigest: testDigest("wrong-baseline-receipt"),
	}
	drifted := buildGovernanceFixture(t, fixture.now, fixture.keys, validModelProfile(), 2, fixture.baselineRecord.Fence, driftedBaseline, RoleProfileApprover)
	if err := fixture.authority.PutGovernanceMaterials(drifted.receiptDigest, drifted.materials); err != nil {
		t.Fatal(err)
	}
	driftedRequest := fixture.candidateRequest
	driftedRequest.OperationID = "70000000-0000-4000-8000-000000000099"
	driftedRequest.ReceiptDigest = drifted.receiptDigest
	driftedRequestHash, err := activationRequestHash(driftedRequest)
	if err != nil {
		t.Fatal(err)
	}
	driftedRecord := activationRecordFromVerified(driftedRequest, driftedRequestHash, mustVerifyFixture(t, drifted, fixture.keys.policy, fixture.now), fixture.now)
	setEnabledState(t, fixture.authority, driftedRecord, fixture.now, fixture.now.Add(MaximumDisableStateLifetime))
	if _, err := fixture.service.Activate(context.Background(), driftedRequest); err == nil || !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("shadow baseline receipt drift was accepted: %v", err)
	}
}

func TestActivationCanReplaceDisabledOrRouteDriftedPredecessor(t *testing.T) {
	disabled := newActivationServiceTestFixture(t, false, false)
	if err := disabled.authority.SetProfileDisableState(ProfileDisableState{
		Query: disableQueryForRecord(disabled.baselineRecord), ActiveConditions: []string{"security-policy-violation"},
		CheckedAt: disabled.now, ExpiresAt: disabled.now.Add(MaximumDisableStateLifetime),
	}); err != nil {
		t.Fatal(err)
	}
	record, err := disabled.service.Activate(context.Background(), disabled.candidateRequest)
	if err != nil {
		t.Fatalf("disabled predecessor could not be replaced by a currently enabled candidate: %v", err)
	}
	if _, err := disabled.service.ResolveActive(context.Background(), record.Workload); err != nil {
		t.Fatalf("replacement candidate was not runtime-authorized: %v", err)
	}

	routeDrifted := newActivationServiceTestFixtureWithCandidateRoute(t, false, false, "provider-route-v2")
	driftedRoute := routeDrifted.baselineFixture.route
	driftedRoute.EndpointDigest = testDigest("retired-predecessor-endpoint")
	driftedRouteJSON, err := CanonicalProviderRouteAuthorityJSON(driftedRoute)
	if err != nil {
		t.Fatal(err)
	}
	if err := routeDrifted.authority.SetCurrentProviderRouteAuthority(driftedRouteJSON); err != nil {
		t.Fatal(err)
	}
	if _, err := routeDrifted.service.ResolveActive(context.Background(), routeDrifted.baselineRecord.Workload); err == nil {
		t.Fatal("route-drifted predecessor remained runtime-authorized")
	}
	record, err = routeDrifted.service.Activate(context.Background(), routeDrifted.candidateRequest)
	if err != nil {
		t.Fatalf("route-drifted predecessor could not be replaced through historical-only verification: %v", err)
	}
	if _, err := routeDrifted.service.ResolveActive(context.Background(), record.Workload); err != nil {
		t.Fatalf("new-route replacement was not runtime-authorized: %v", err)
	}
}

func TestActivationServiceClosesActivatedFallbackGraph(t *testing.T) {
	fixture := newActivationServiceTestFixture(t, true, false)
	record, err := fixture.service.Activate(context.Background(), fixture.candidateRequest)
	if err != nil {
		t.Fatalf("activate profile with exact activated fallback: %v", err)
	}
	resolved, err := fixture.service.ResolveActive(context.Background(), record.Workload)
	if err != nil {
		t.Fatalf("resolve activated fallback graph: %v", err)
	}
	if len(resolved.Graph) != 2 {
		t.Fatalf("expected primary plus fallback, got %d graph members", len(resolved.Graph))
	}

	unresolved := newActivationServiceTestFixture(t, true, true)
	if _, err := unresolved.service.Activate(context.Background(), unresolved.candidateRequest); err == nil || !errors.Is(err, ErrRuntimeAuthority) {
		t.Fatalf("unactivated fallback was accepted: %v", err)
	}
}

func TestActivationServiceRejectsFallbackDependencyDrift(t *testing.T) {
	fixture := newActivationServiceTestFixture(t, true, false)
	drifting := &driftingActivationStore{ActivationStore: fixture.store, driftAfter: 3}
	service, err := NewActivationService(drifting, fixture.authority, NewGovernanceVerifier())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(context.Background(), fixture.candidateRequest); err == nil || !errors.Is(err, ErrRuntimeAuthority) {
		t.Fatalf("fallback exact-profile index drift was accepted: %v", err)
	}
	if _, err := fixture.store.GetActivationOperation(context.Background(), fixture.candidateRequest.OperationID); !errors.Is(err, ErrActivationNotFound) {
		t.Fatalf("drifted fallback reached append: %v", err)
	}
}

func TestResolveActiveReverifiesDisableRouteAndCurrentTrustEveryCall(t *testing.T) {
	fixture := newActivationServiceTestFixture(t, false, false)
	record, err := fixture.service.Activate(context.Background(), fixture.candidateRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ResolveActive(context.Background(), record.Workload); err != nil {
		t.Fatalf("initial resolve: %v", err)
	}
	if err := fixture.authority.SetProfileDisableState(ProfileDisableState{
		Query: disableQueryForRecord(record), ActiveConditions: []string{}, CheckedAt: fixture.now,
		ExpiresAt: fixture.now.Add(MaximumDisableStateLifetime + time.Millisecond),
	}); err == nil {
		t.Fatal("overlong disable-state authority was accepted")
	}
	disabled := ProfileDisableState{
		Query: disableQueryForRecord(record), ActiveConditions: []string{"provider-route-drift"},
		CheckedAt: fixture.now, ExpiresAt: fixture.now.Add(MaximumDisableStateLifetime),
	}
	if err := fixture.authority.SetProfileDisableState(disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ResolveActive(context.Background(), record.Workload); err == nil || !errors.Is(err, ErrProfileDisabled) {
		t.Fatalf("fresh disable state was ignored: %v", err)
	}
	setEnabledState(t, fixture.authority, record, fixture.now, fixture.now.Add(MaximumDisableStateLifetime))
	driftedRoute := fixture.candidateFixture.route
	driftedRoute.EndpointDigest = testDigest("rotated-provider-endpoint")
	driftedRouteJSON, err := CanonicalProviderRouteAuthorityJSON(driftedRoute)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.authority.SetCurrentProviderRouteAuthority(driftedRouteJSON); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ResolveActive(context.Background(), record.Workload); err == nil || !errors.Is(err, ErrRuntimeAuthority) {
		t.Fatalf("current provider route drift was ignored: %v", err)
	}
	if err := fixture.authority.SetCurrentProviderRouteAuthority(fixture.candidateFixture.materials.ProviderRouteAuthorityJSON); err != nil {
		t.Fatal(err)
	}

	rotated := cloneGovernanceTrustPolicy(fixture.keys.policy)
	signer := rotated.Signers[RoleShadowVerifier]
	signer.Identity = "rotated-shadow-verifier-identity"
	rotated.Signers[RoleShadowVerifier] = signer
	rotated.PolicyHash = ""
	rotatedHash, err := GovernanceTrustPolicyHash(rotated)
	if err != nil {
		t.Fatal(err)
	}
	rotated.PolicyHash = rotatedHash
	if err := fixture.authority.SetTrustPolicy(rotated); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ResolveActive(context.Background(), record.Workload); err == nil || !errors.Is(err, ErrRuntimeAuthority) {
		t.Fatalf("same-revocation-epoch trust-policy rotation was not fenced: %v", err)
	}
	rotatedEpoch := nextGovernanceRevocationAuthority(t, fixture.keys.revocations, fixture.now, nil, nil)
	if err := fixture.authority.SetRevocationAuthority(rotatedEpoch); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ResolveActive(context.Background(), record.Workload); err != nil {
		t.Fatalf("receipt-bound historical trust policy was not retained across signer-policy rotation: %v", err)
	}
	*fixture.clock = fixture.now.Add(11 * time.Hour)
	setEnabledState(t, fixture.authority, record, *fixture.clock, fixture.clock.Add(MaximumDisableStateLifetime))
	if _, err := fixture.service.ResolveActive(context.Background(), record.Workload); err == nil || !errors.Is(err, ErrRuntimeAuthority) {
		t.Fatalf("expired provider route authority was ignored: %v", err)
	}
}

func TestSignerPolicyRotationUsesReceiptBoundHistoricalPredecessor(t *testing.T) {
	fixture := newActivationServiceTestFixture(t, false, false)
	rotated := newRotatedGovernanceTestKeys(t, fixture.now, "rotation-two")
	if err := fixture.authority.SetTrustPolicy(rotated.policy); err != nil {
		t.Fatal(err)
	}
	oldReceiptIssuer := fixture.keys.policy.Signers[RoleReceiptIssuer]
	revokedOldSigner := nextGovernanceRevocationAuthority(t, fixture.keys.revocations, fixture.now, nil, []GovernanceSignerRevocation{{
		PolicyHash: fixture.keys.policy.PolicyHash, KeyID: RoleReceiptIssuer, PublicKeyHash: sha256Digest(oldReceiptIssuer.PublicKey),
		ReasonHash: testDigest("retired-old-receipt-issuer"), RevokedAt: fixture.now.Add(time.Millisecond),
	}})
	if err := fixture.authority.SetRevocationAuthority(revokedOldSigner); err != nil {
		t.Fatal(err)
	}
	*fixture.clock = fixture.now.Add(time.Second)
	if _, err := fixture.service.ResolveActive(context.Background(), fixture.baselineRecord.Workload); err == nil {
		t.Fatal("currently revoked old signer remained runtime-authorized")
	}
	profile := validModelProfile()
	baseline := BaselineBinding{
		ActivationFence: fixture.baselineRecord.Fence, Generation: fixture.baselineRecord.Generation,
		MetricsHash: testDigest("rotated-policy-baseline-metrics"),
		Profile: CorpusProfileBinding{
			ID: fixture.baselineRecord.ProfileID, ContentHash: fixture.baselineRecord.ProfileContentHash, Workload: fixture.baselineRecord.Workload,
		},
		ReceiptDigest: fixture.baselineRecord.ReceiptDigest,
	}
	candidate := buildGovernanceFixture(t, fixture.now, rotated, profile, 2, fixture.baselineRecord.Fence, baseline, RoleProfileApprover)
	if err := fixture.authority.PutGovernanceMaterials(candidate.receiptDigest, candidate.materials); err != nil {
		t.Fatal(err)
	}
	request := ActivationRequest{
		OperationID: "70000000-0000-4000-8000-000000000077", ReceiptDigest: candidate.receiptDigest,
		ExpectedGeneration: fixture.baselineRecord.Generation, ExpectedFence: fixture.baselineRecord.Fence,
	}
	requestHash, err := activationRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := NewGovernanceVerifier().Verify(candidate.materials, candidate.receiptDigest, rotated.policy, revokedOldSigner, *fixture.clock)
	if err != nil {
		t.Fatal(err)
	}
	candidateRecord := activationRecordFromVerified(request, requestHash, verified, *fixture.clock)
	setEnabledState(t, fixture.authority, candidateRecord, *fixture.clock, fixture.clock.Add(MaximumDisableStateLifetime))
	record, err := fixture.service.Activate(context.Background(), request)
	if err != nil {
		t.Fatalf("candidate under rotated signer policy could not replace historical-policy predecessor: %v", err)
	}
	if record.TrustPolicyHash != rotated.policy.PolicyHash || record.TrustPolicyHash == fixture.keys.policy.PolicyHash {
		t.Fatal("activation record did not bind the rotated signer policy")
	}
	if _, err := fixture.service.ResolveActive(context.Background(), record.Workload); err != nil {
		t.Fatalf("runtime could not load receipt-bound rotated policy: %v", err)
	}
}

func TestCurrentCumulativeRevocationsAreSelectiveAndRollbackFenced(t *testing.T) {
	fixture := newActivationServiceTestFixture(t, false, false)
	record, err := fixture.service.Activate(context.Background(), fixture.candidateRequest)
	if err != nil {
		t.Fatal(err)
	}
	current := fixture.keys.revocations
	unrelated := nextGovernanceRevocationAuthority(t, current, fixture.now.Add(-30*time.Second), []GovernanceRevocation{{
		Digest: testDigest("unrelated-revoked-artifact"), ReasonHash: testDigest("unrelated-revocation-reason"), RevokedAt: fixture.now,
	}}, nil)
	if err := fixture.authority.SetRevocationAuthority(unrelated); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ResolveActive(context.Background(), record.Workload); err != nil {
		t.Fatalf("unrelated digest revocation invalidated an otherwise valid receipt: %v", err)
	}

	activeDigest := nextGovernanceRevocationAuthority(t, unrelated, fixture.now, []GovernanceRevocation{{
		Digest:     fixture.candidateFixture.profile.Runner.ImmutableDigest,
		ReasonHash: testDigest("active-runner-revocation-reason"), RevokedAt: fixture.now,
	}}, nil)
	if err := fixture.authority.SetRevocationAuthority(activeDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ResolveActive(context.Background(), record.Workload); err == nil || !errors.Is(err, ErrGovernanceUntrusted) {
		t.Fatalf("active runner digest revocation was ignored: %v", err)
	}

	if err := fixture.store.ObserveGovernanceRevocationAuthority(context.Background(), unrelated); err == nil {
		t.Fatal("durable revocation observation accepted a lower epoch rollback")
	}
	if err := fixture.authority.SetRevocationAuthority(unrelated); err == nil {
		t.Fatal("memory revocation authority accepted a lower epoch rollback")
	}
	sameEpochDifferent := cloneGovernanceRevocationAuthority(activeDigest)
	sameEpochDifferent.ExpiresAt = sameEpochDifferent.ExpiresAt.Add(-time.Millisecond)
	sameEpochDifferent.AuthorityHash = mustGovernanceRevocationAuthorityHash(t, sameEpochDifferent)
	if err := fixture.store.ObserveGovernanceRevocationAuthority(context.Background(), sameEpochDifferent); err == nil {
		t.Fatal("durable revocation observation accepted same-epoch equivocation")
	}
	if err := fixture.authority.SetRevocationAuthority(sameEpochDifferent); err == nil {
		t.Fatal("memory revocation authority accepted same-epoch equivocation")
	}
	dropping := cloneGovernanceRevocationAuthority(activeDigest)
	dropping.Epoch++
	dropping.IssuedAt = dropping.IssuedAt.Add(time.Millisecond)
	dropping.DigestRevocations = []GovernanceRevocation{}
	dropping.AuthorityHash = mustGovernanceRevocationAuthorityHash(t, dropping)
	if err := fixture.store.ObserveGovernanceRevocationAuthority(context.Background(), dropping); err == nil {
		t.Fatal("durable revocation observation accepted a higher epoch that deleted prior revocations")
	}
	if err := fixture.authority.SetRevocationAuthority(dropping); err == nil {
		t.Fatal("memory revocation authority accepted a higher epoch that deleted prior revocations")
	}
}

func TestMemoryActivationStoreCASRaceAndExactProfileOverwriteFence(t *testing.T) {
	now := governanceTestNow()
	store, err := NewMemoryActivationStore(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	baseline := structuralActivationRecord("71000000-0000-4000-8000-000000000001", "22222222-2222-4222-8222-222222222222", "constructor-implementation", 1, testDigest("genesis"), now)
	seedMemoryActivationStore(t, store, baseline)

	overwrite := structuralActivationRecord("71000000-0000-4000-8000-000000000002", baseline.ProfileID, baseline.Workload, 2, baseline.Fence, now.Add(time.Second))
	overwrite.ProfileContentHash = baseline.ProfileContentHash
	overwrite.RequestHash, err = activationRequestHash(ActivationRequest{
		OperationID: overwrite.OperationID, ReceiptDigest: overwrite.ReceiptDigest,
		ExpectedGeneration: overwrite.PreviousGeneration, ExpectedFence: overwrite.PreviousFence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendActivation(context.Background(), ActivationAppend{ExpectedGeneration: 1, ExpectedFence: baseline.Fence, Record: overwrite}); err == nil || !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("same exact profile was rebound to another receipt: %v", err)
	}

	candidates := []ActivationRecord{
		structuralActivationRecord("71000000-0000-4000-8000-000000000003", "33333333-3333-4333-8333-333333333333", baseline.Workload, 2, baseline.Fence, now.Add(time.Second)),
		structuralActivationRecord("71000000-0000-4000-8000-000000000004", "44444444-4444-4444-8444-444444444444", baseline.Workload, 2, baseline.Fence, now.Add(time.Second)),
	}
	var wait sync.WaitGroup
	results := make(chan error, len(candidates))
	for _, candidate := range candidates {
		candidate := candidate
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, appendErr := store.AppendActivation(context.Background(), ActivationAppend{ExpectedGeneration: 1, ExpectedFence: baseline.Fence, Record: candidate})
			results <- appendErr
		}()
	}
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for result := range results {
		switch {
		case result == nil:
			successes++
		case errors.Is(result, ErrActivationConflict):
			conflicts++
		default:
			t.Fatalf("unexpected CAS result: %v", result)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("CAS race outcomes: successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestActivationUnknownOutcomeInspectsWithoutRetry(t *testing.T) {
	fixture := newActivationServiceTestFixture(t, false, false)
	wrapped := &unknownActivationStore{ActivationStore: fixture.store, commit: true}
	service, err := NewActivationService(wrapped, fixture.authority, NewGovernanceVerifier())
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Activate(context.Background(), fixture.candidateRequest)
	if err != nil || record.ReceiptDigest != fixture.candidateFixture.receiptDigest {
		t.Fatalf("committed unknown outcome was not recovered by inspection: %v", err)
	}
	if wrapped.appendCalls != 1 {
		t.Fatalf("unknown outcome retried append %d times", wrapped.appendCalls)
	}

	uncommitted := newActivationServiceTestFixture(t, false, false)
	wrapped = &unknownActivationStore{ActivationStore: uncommitted.store, commit: false}
	service, err = NewActivationService(wrapped, uncommitted.authority, NewGovernanceVerifier())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(context.Background(), uncommitted.candidateRequest); err == nil || !errors.Is(err, ErrActivationOutcomeUnknown) {
		t.Fatalf("uncommitted unknown outcome was not preserved: %v", err)
	}
	if wrapped.appendCalls != 1 {
		t.Fatalf("uncommitted unknown outcome retried append %d times", wrapped.appendCalls)
	}
}

func TestActivationRejectsMismatchedReturnAndIncompleteAtomicProjections(t *testing.T) {
	returned := newActivationServiceTestFixture(t, false, false)
	returningWrongBytes := &corruptActivationReturnStore{ActivationStore: returned.store}
	service, err := NewActivationService(returningWrongBytes, returned.authority, NewGovernanceVerifier())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(context.Background(), returned.candidateRequest); err == nil || !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("store return bytes differing from the persisted append were accepted: %v", err)
	}

	incomplete := newActivationServiceTestFixture(t, false, false)
	missingHistory := &missingCommittedProjectionStore{
		ActivationStore: incomplete.store, missingGeneration: incomplete.baselineRecord.Generation + 1,
	}
	service, err = NewActivationService(missingHistory, incomplete.authority, NewGovernanceVerifier())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(context.Background(), incomplete.candidateRequest); err == nil || !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("operation-only unknown reconciliation accepted a missing history projection: %v", err)
	}
	if missingHistory.appendCalls != 1 {
		t.Fatalf("incomplete unknown outcome retried append %d times", missingHistory.appendCalls)
	}
}

func TestActivationOutcomeRejectsIncompleteDescendantProjections(t *testing.T) {
	now := governanceTestNow()
	store, err := NewMemoryActivationStore(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	baseline := structuralActivationRecord(
		"72000000-0000-4000-8000-000000000001", "22222222-2222-4222-8222-222222222222",
		"constructor-implementation", 1, testDigest("descendant-genesis"), now,
	)
	seedMemoryActivationStore(t, store, baseline)
	descendant := structuralActivationRecord(
		"72000000-0000-4000-8000-000000000002", "33333333-3333-4333-8333-333333333333",
		baseline.Workload, 2, baseline.Fence, now.Add(time.Millisecond),
	)
	if _, err := store.AppendActivation(context.Background(), ActivationAppend{
		ExpectedGeneration: baseline.Generation, ExpectedFence: baseline.Fence, Record: descendant,
	}); err != nil {
		t.Fatal(err)
	}
	wrapped := &missingDescendantOperationStore{ActivationStore: store, missingOperationID: descendant.OperationID}
	service := &ActivationService{store: wrapped}
	if _, err := service.confirmActivationOutcome(context.Background(), baseline, &baseline); err == nil || !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("head descendant with missing operation projection was accepted: %v", err)
	}
}

type unknownActivationStore struct {
	ActivationStore
	appendCalls int
	commit      bool
}

type corruptActivationReturnStore struct{ ActivationStore }

func (store *corruptActivationReturnStore) AppendActivation(ctx context.Context, command ActivationAppend) (ActivationRecord, error) {
	record, err := store.ActivationStore.AppendActivation(ctx, command)
	if err == nil {
		record.Fence = testDigest("corrupt-return-fence")
	}
	return record, err
}

type missingCommittedProjectionStore struct {
	ActivationStore
	appendCalls       int
	missingGeneration uint64
}

type missingDescendantOperationStore struct {
	ActivationStore
	missingOperationID string
}

func (store *missingDescendantOperationStore) GetActivationOperation(ctx context.Context, operationID string) (ActivationRecord, error) {
	if operationID == store.missingOperationID {
		return ActivationRecord{}, ErrActivationNotFound
	}
	return store.ActivationStore.GetActivationOperation(ctx, operationID)
}

func (store *missingCommittedProjectionStore) AppendActivation(ctx context.Context, command ActivationAppend) (ActivationRecord, error) {
	store.appendCalls++
	if _, err := store.ActivationStore.AppendActivation(ctx, command); err != nil {
		return ActivationRecord{}, err
	}
	return ActivationRecord{}, ErrActivationOutcomeUnknown
}

func (store *missingCommittedProjectionStore) GetActivationGeneration(ctx context.Context, workload string, generation uint64) (ActivationRecord, error) {
	if store.appendCalls > 0 && generation == store.missingGeneration {
		return ActivationRecord{}, ErrActivationNotFound
	}
	return store.ActivationStore.GetActivationGeneration(ctx, workload, generation)
}

type driftingActivationStore struct {
	ActivationStore
	mu         sync.Mutex
	reads      int
	driftAfter int
}

func (store *driftingActivationStore) GetActivatedProfile(ctx context.Context, binding CorpusProfileBinding) (ActivationRecord, error) {
	record, err := store.ActivationStore.GetActivatedProfile(ctx, binding)
	if err != nil {
		return ActivationRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.reads++
	if store.reads >= store.driftAfter {
		record.ReceiptDigest = testDigest("concurrent-fallback-receipt-drift")
	}
	return record, nil
}

func (store *unknownActivationStore) AppendActivation(ctx context.Context, command ActivationAppend) (ActivationRecord, error) {
	store.appendCalls++
	if store.commit {
		if _, err := store.ActivationStore.AppendActivation(ctx, command); err != nil {
			return ActivationRecord{}, err
		}
	}
	return ActivationRecord{}, ErrActivationOutcomeUnknown
}

func newActivationServiceTestFixture(t *testing.T, withFallback, unresolvedFallback bool) activationServiceTestFixture {
	return newActivationServiceTestFixtureWithCandidateRoute(t, withFallback, unresolvedFallback, "")
}

func newActivationServiceTestFixtureWithCandidateRoute(t *testing.T, withFallback, unresolvedFallback bool, candidateRouteID string) activationServiceTestFixture {
	t.Helper()
	now := governanceTestNow()
	clock := now
	keys := newGovernanceTestKeys(t, now, "", time.Time{})
	store, err := NewMemoryActivationStore(func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewMemoryGovernanceAuthority(keys.policy, keys.revocations)
	if err != nil {
		t.Fatal(err)
	}

	baselineProfile := validModelProfile()
	baselineProfile.ID = "22222222-2222-4222-8222-222222222222"
	baseline := buildGovernanceFixture(t, now, keys, baselineProfile, 1, testDigest("unused-ordinary-genesis-fence"), BaselineBinding{
		ActivationFence: testDigest("pre-bootstrap-fence"), Generation: 9, MetricsHash: testDigest("pre-bootstrap-metrics"),
		Profile:       CorpusProfileBinding{ID: "99999999-9999-4999-8999-999999999999", ContentHash: testDigest("pre-bootstrap-profile"), Workload: baselineProfile.Workload},
		ReceiptDigest: testDigest("pre-bootstrap-receipt"),
	}, RoleProfileApprover)
	genesis := buildGenesisFixture(t, baseline, keys, keys.revocations, RoleGenesisApprover)
	baselineRequest := GenesisBootstrapRequest{
		OperationID: "70000000-0000-4000-8000-000000000001", ReceiptDigest: baseline.receiptDigest,
		ExpectedEmptyFence: genesis.genesis.PreviousFence,
	}
	baselineRequest.ReceiptDigest = genesis.receiptDigest
	baselineRequestHash, err := genesisBootstrapRequestHash(baselineRequest)
	if err != nil {
		t.Fatal(err)
	}
	baselineRecord := genesisRecordFromVerified(baselineRequest, baselineRequestHash, genesis.verified, now)
	if err := authority.PutGenesisGovernanceMaterials(genesis.receiptDigest, genesis.materials); err != nil {
		t.Fatal(err)
	}
	if err := authority.SetCurrentProviderRouteAuthority(baseline.materials.ProviderRouteAuthorityJSON); err != nil {
		t.Fatal(err)
	}
	setEnabledState(t, authority, baselineRecord, now, now.Add(MaximumDisableStateLifetime))
	bootstrapService, err := NewGenesisBootstrapService(store, authority, NewGovernanceVerifier())
	if err != nil {
		t.Fatal(err)
	}
	baselineRecord, err = bootstrapService.Bootstrap(context.Background(), baselineRequest)
	if err != nil {
		t.Fatalf("bootstrap activation fixture through signed Genesis: %v", err)
	}

	candidateProfile := validModelProfile()
	if candidateRouteID != "" {
		candidateProfile.Provider.RouteID = candidateRouteID
	}
	if withFallback {
		fallbackID := baseline.profile.ID
		fallbackHash := baseline.profileHash
		if unresolvedFallback {
			fallbackID = "33333333-3333-4333-8333-333333333333"
			fallbackHash = testDigest("unactivated-fallback-profile")
		}
		candidateProfile.Fallback = FallbackPolicy{
			Enabled:      true,
			Profiles:     []FallbackProfileBinding{{ID: fallbackID, ContentHash: fallbackHash, Workload: candidateProfile.Workload}},
			OnConditions: []string{"provider-unavailable"},
		}
	}
	candidateBaseline := BaselineBinding{
		ActivationFence: baselineRecord.Fence, Generation: baselineRecord.Generation, MetricsHash: testDigest("baseline-shadow-metrics"),
		Profile:       CorpusProfileBinding{ID: baselineRecord.ProfileID, ContentHash: baselineRecord.ProfileContentHash, Workload: baselineRecord.Workload},
		ReceiptDigest: baselineRecord.ReceiptDigest,
	}
	candidate := buildGovernanceFixture(t, now, keys, candidateProfile, 2, baselineRecord.Fence, candidateBaseline, RoleProfileApprover)
	if err := authority.PutGovernanceMaterials(candidate.receiptDigest, candidate.materials); err != nil {
		t.Fatal(err)
	}
	if candidate.profile.Provider.RouteID != baseline.profile.Provider.RouteID {
		if err := authority.SetCurrentProviderRouteAuthority(candidate.materials.ProviderRouteAuthorityJSON); err != nil {
			t.Fatal(err)
		}
	}
	candidateRequest := ActivationRequest{
		OperationID: "70000000-0000-4000-8000-000000000002", ReceiptDigest: candidate.receiptDigest,
		ExpectedGeneration: baselineRecord.Generation, ExpectedFence: baselineRecord.Fence,
	}
	candidateRequestHash, err := activationRequestHash(candidateRequest)
	if err != nil {
		t.Fatal(err)
	}
	candidateRecord := activationRecordFromVerified(candidateRequest, candidateRequestHash, mustVerifyFixture(t, candidate, keys.policy, now), now)
	setEnabledState(t, authority, candidateRecord, now, now.Add(MaximumDisableStateLifetime))
	service, err := NewActivationService(store, authority, NewGovernanceVerifier())
	if err != nil {
		t.Fatal(err)
	}
	return activationServiceTestFixture{
		now: now, clock: &clock, keys: keys, store: store, authority: authority, service: service,
		baselineFixture: baseline, baselineRecord: baselineRecord, candidateFixture: candidate, candidateRequest: candidateRequest,
	}
}

func mustVerifyFixture(t *testing.T, fixture governanceFixture, policy GovernanceTrustPolicy, now time.Time) VerifiedGovernance {
	t.Helper()
	keys := newGovernanceTestKeys(t, now, "", time.Time{})
	if keys.policy.PolicyHash != policy.PolicyHash {
		t.Fatal("fixture policy hash does not match test authority")
	}
	verified, err := NewGovernanceVerifier().Verify(fixture.materials, fixture.receiptDigest, policy, keys.revocations, now)
	if err != nil {
		t.Fatalf("verify governance fixture: %v", err)
	}
	return verified
}

func setEnabledState(t *testing.T, authority *MemoryGovernanceAuthority, record ActivationRecord, checkedAt, expiresAt time.Time) {
	t.Helper()
	if err := authority.SetProfileDisableState(ProfileDisableState{
		Query: disableQueryForRecord(record), ActiveConditions: []string{}, CheckedAt: checkedAt, ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatalf("set enabled profile state: %v", err)
	}
}

func structuralActivationRecord(operationID, profileID, workload string, generation uint64, previousFence string, activatedAt time.Time) ActivationRecord {
	seed := operationID + profileID
	record := ActivationRecord{
		AuthorityKind: ActivationAuthorityKind,
		OperationID:   operationID, Workload: workload,
		ProfileID: profileID, ProfileContentHash: testDigest("profile-" + profileID), ReceiptDigest: testDigest("receipt-" + seed),
		ReceiptPayloadDigest: testDigest("receipt-payload-" + seed), ActivationEnvelopeDigest: testDigest("activation-envelope-" + seed),
		ActivationPayloadDigest: testDigest("activation-payload-" + seed), PreviousGeneration: generation - 1, Generation: generation,
		PreviousFence: previousFence, Fence: testDigest("fence-" + seed), CorpusContentHash: testDigest("corpus-" + seed),
		ProviderRouteAuthorityHash: testDigest("route-" + seed), RunnerImmutableDigest: testDigest("runner-" + seed),
		SourceTreeDigest: testDigest("source-" + seed), TrustPolicyHash: testDigest("structural-test-trust-policy"), ActivatedAt: activatedAt,
	}
	record.RequestHash, _ = activationRequestHash(ActivationRequest{
		OperationID: operationID, ReceiptDigest: record.ReceiptDigest,
		ExpectedGeneration: record.PreviousGeneration, ExpectedFence: record.PreviousFence,
	})
	return record
}

// seedMemoryActivationStore creates a deliberately out-of-band predecessor for
// structural store/projection tests only. Service fixtures bootstrap through
// the signed Genesis authority, and no production API exposes this helper.
func seedMemoryActivationStore(t *testing.T, store *MemoryActivationStore, record ActivationRecord) {
	t.Helper()
	if err := validateActivationAppend(ActivationAppend{
		ExpectedGeneration: record.PreviousGeneration, ExpectedFence: record.PreviousFence, Record: record,
	}); err != nil {
		t.Fatalf("seed activation record is invalid: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.operations[record.OperationID] = record
	store.history[activationGenerationKey(record.Workload, record.Generation)] = record
	store.heads[record.Workload] = record
	store.activatedByExact[activationProfileKey(CorpusProfileBinding{
		ID: record.ProfileID, ContentHash: record.ProfileContentHash, Workload: record.Workload,
	})] = record
	anchor := GovernanceRevocationAuthority{
		AuthorityHash: testDigest("structural-test-revocation-authority"), Epoch: 1,
		IssuedAt: record.ActivatedAt.Add(-time.Minute), ExpiresAt: record.ActivatedAt.Add(MaximumRevocationAuthorityLifetime - time.Minute),
		DigestRevocations: []GovernanceRevocation{}, SignerRevocations: []GovernanceSignerRevocation{},
	}
	store.revocationAnchor = &anchor
	trust := GovernanceTrustPolicyObservation{
		PolicyHash: record.TrustPolicyHash, RevocationAuthorityHash: anchor.AuthorityHash, RevocationEpoch: anchor.Epoch,
	}
	store.trustPolicyAnchor = &trust
}

func nextGovernanceRevocationAuthority(
	t *testing.T,
	current GovernanceRevocationAuthority,
	issuedAt time.Time,
	digestAdditions []GovernanceRevocation,
	signerAdditions []GovernanceSignerRevocation,
) GovernanceRevocationAuthority {
	t.Helper()
	next := cloneGovernanceRevocationAuthority(current)
	next.Epoch++
	next.IssuedAt = issuedAt.UTC().Truncate(time.Millisecond)
	next.ExpiresAt = next.IssuedAt.Add(MaximumRevocationAuthorityLifetime)
	next.DigestRevocations = append(next.DigestRevocations, digestAdditions...)
	next.SignerRevocations = append(next.SignerRevocations, signerAdditions...)
	sort.Slice(next.DigestRevocations, func(i, j int) bool {
		return next.DigestRevocations[i].Digest < next.DigestRevocations[j].Digest
	})
	sort.Slice(next.SignerRevocations, func(i, j int) bool {
		left := next.SignerRevocations[i].PolicyHash + "\x00" + next.SignerRevocations[i].KeyID
		right := next.SignerRevocations[j].PolicyHash + "\x00" + next.SignerRevocations[j].KeyID
		return left < right
	})
	next.AuthorityHash = mustGovernanceRevocationAuthorityHash(t, next)
	return next
}

func mustGovernanceRevocationAuthorityHash(t *testing.T, authority GovernanceRevocationAuthority) string {
	t.Helper()
	authority.AuthorityHash = ""
	digest, err := GovernanceRevocationAuthorityHash(authority)
	if err != nil {
		t.Fatalf("hash governance revocation authority: %v", err)
	}
	return digest
}
