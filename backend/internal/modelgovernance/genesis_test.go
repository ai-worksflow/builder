package modelgovernance

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestGenesisCanonicalDocumentsAreStrictAndDigestFenced(t *testing.T) {
	now := governanceTestNow()
	keys := newGovernanceTestKeys(t, now, "", time.Time{})
	ordinary := genesisOrdinaryFixture(t, now, keys, validModelProfile())
	fixture := buildGenesisFixture(t, ordinary, keys, keys.revocations, RoleGenesisApprover)
	genesisJSON, err := CanonicalGovernanceGenesisArtifactJSON(fixture.genesis)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseGovernanceGenesisArtifact(genesisJSON, sha256Digest(genesisJSON))
	if err != nil || parsed != fixture.genesis {
		t.Fatalf("canonical Genesis round trip: %v", err)
	}
	receiptJSON, err := CanonicalModelGovernanceGenesisReceiptJSON(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	parsedReceipt, err := ParseModelGovernanceGenesisReceipt(receiptJSON, sha256Digest(receiptJSON))
	if err != nil || parsedReceipt != fixture.receipt {
		t.Fatalf("canonical Genesis receipt round trip: %v", err)
	}
	for label, encoded := range map[string][]byte{
		"noncanonical whitespace": append([]byte(" "), genesisJSON...),
		"invalid UTF-8":           append(bytes.Clone(genesisJSON), 0xff),
		"unknown field":           append(bytes.TrimSuffix(genesisJSON, []byte("}")), []byte(",\"unknown\":true}")...),
	} {
		if _, err := ParseGovernanceGenesisArtifact(encoded, sha256Digest(encoded)); err == nil {
			t.Fatalf("%s Genesis JSON was accepted", label)
		}
	}
	if _, err := ParseGovernanceGenesisArtifact(genesisJSON, testDigest("wrong-Genesis-payload-hash")); err == nil {
		t.Fatal("Genesis payload digest drift was accepted")
	}
}

type genesisGovernanceFixture struct {
	materials     GenesisGovernanceMaterials
	receiptDigest string
	genesis       GovernanceGenesisArtifact
	receipt       ModelGovernanceGenesisReceipt
	verified      VerifiedGovernance
}

func buildGenesisFixture(t *testing.T, ordinary governanceFixture, keys governanceTestKeys, revocations GovernanceRevocationAuthority, signerRole string) genesisGovernanceFixture {
	t.Helper()
	binding := GovernanceRevocationAuthorityBinding{
		AuthorityHash: revocations.AuthorityHash,
		AuthorityID:   GovernanceRevocationAuthorityID,
		Epoch:         revocations.Epoch,
	}
	genesis := GovernanceGenesisArtifact{
		Approval: ordinary.approvalRef, ArtifactID: "61000000-0000-4000-8000-000000000001",
		Conformance: ordinary.conformanceRef, Decision: GenesisDecisionBootstrap,
		DecisionHash: testDigest("signed-genesis-decision-" + ordinary.profile.ID),
		ExpiresAt:    formatGovernanceTime(ordinary.now.Add(14 * time.Hour)), Fence: testDigest("signed-genesis-fence-" + ordinary.profile.ID),
		Generation: 1, IssuedAt: formatGovernanceTime(ordinary.now.Add(-4 * time.Hour)),
		PreviousFence: testDigest("explicit-empty-head-fence-" + ordinary.profile.ID), PreviousGeneration: 0,
		RevocationAuthority: binding, SchemaVersion: GovernanceGenesisSchemaVersion, Subject: ordinary.receipt.Subject,
	}
	genesisPayload, err := CanonicalGovernanceGenesisArtifactJSON(genesis)
	if err != nil {
		t.Fatal(err)
	}
	genesisEnvelope, genesisRef := signGovernanceFixture(
		t, genesis.ArtifactID, GovernanceEnvelopePayloadTypeGenesis, genesisPayload, signerRole, keys.private[signerRole],
	)
	receipt := ModelGovernanceGenesisReceipt{
		Approval: ordinary.approvalRef, ArtifactID: "61000000-0000-4000-8000-000000000002",
		Conformance: ordinary.conformanceRef, ExpiresAt: formatGovernanceTime(ordinary.now.Add(12 * time.Hour)),
		Fence: genesis.Fence, Generation: 1, Genesis: genesisRef,
		IssuedAt: formatGovernanceTime(ordinary.now.Add(-3 * time.Hour)), RevocationAuthority: binding,
		SchemaVersion: GovernanceGenesisReceiptSchemaVersion, Subject: ordinary.receipt.Subject,
	}
	receiptPayload, err := CanonicalModelGovernanceGenesisReceiptJSON(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptEnvelope, _ := signGovernanceFixture(
		t, receipt.ArtifactID, GovernanceEnvelopePayloadTypeGenesisReceipt, receiptPayload, RoleReceiptIssuer, keys.private[RoleReceiptIssuer],
	)
	materials := GenesisGovernanceMaterials{
		ModelProfileJSON:           ordinary.materials.ModelProfileJSON,
		FrozenCorpusJSON:           ordinary.materials.FrozenCorpusJSON,
		ProviderRouteAuthorityJSON: ordinary.materials.ProviderRouteAuthorityJSON,
		ConformanceEnvelope:        ordinary.materials.ConformanceEnvelope,
		ApprovalEnvelope:           ordinary.materials.ApprovalEnvelope,
		GenesisEnvelope:            genesisEnvelope, ReceiptEnvelope: receiptEnvelope,
	}
	receiptDigest := sha256Digest(receiptEnvelope)
	verified := VerifiedGovernance{}
	if signerRole == RoleGenesisApprover && ValidateGovernanceRevocationAuthority(revocations, ordinary.now) == nil {
		verified, err = NewGovernanceVerifier().VerifyGenesis(materials, receiptDigest, keys.policy, revocations, ordinary.now)
		if err != nil {
			t.Fatalf("verify Genesis fixture: %v", err)
		}
	}
	return genesisGovernanceFixture{materials: materials, receiptDigest: receiptDigest, genesis: genesis, receipt: receipt, verified: verified}
}

func TestSignedGenesisBootstrapResolveAndFirstNormalActivation(t *testing.T) {
	now := governanceTestNow()
	keys := newGovernanceTestKeys(t, now, "", time.Time{})
	profile := validModelProfile()
	profile.ID = "22222222-2222-4222-8222-222222222222"
	ordinary := buildGovernanceFixture(t, now, keys, profile, 1, testDigest("unused-ordinary-previous"), BaselineBinding{
		ActivationFence: testDigest("unused-baseline-fence"), Generation: 8, MetricsHash: testDigest("unused-baseline-metrics"),
		Profile:       CorpusProfileBinding{ID: "99999999-9999-4999-8999-999999999999", ContentHash: testDigest("unused-profile"), Workload: profile.Workload},
		ReceiptDigest: testDigest("unused-receipt"),
	}, RoleProfileApprover)
	genesis := buildGenesisFixture(t, ordinary, keys, keys.revocations, RoleGenesisApprover)
	store, err := NewMemoryActivationStore(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewMemoryGovernanceAuthority(keys.policy, keys.revocations)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.PutGenesisGovernanceMaterials(genesis.receiptDigest, genesis.materials); err != nil {
		t.Fatal(err)
	}
	if err := authority.SetCurrentProviderRouteAuthority(genesis.materials.ProviderRouteAuthorityJSON); err != nil {
		t.Fatal(err)
	}
	request := GenesisBootstrapRequest{
		OperationID: "72000000-0000-4000-8000-000000000001", ReceiptDigest: genesis.receiptDigest,
		ExpectedEmptyFence: genesis.genesis.PreviousFence,
	}
	requestHash, _ := genesisBootstrapRequestHash(request)
	predicted := genesisRecordFromVerified(request, requestHash, genesis.verified, now)
	setEnabledState(t, authority, predicted, now, now.Add(MaximumDisableStateLifetime))
	bootstrap, err := NewGenesisBootstrapService(store, authority, NewGovernanceVerifier())
	if err != nil {
		t.Fatal(err)
	}
	record, err := bootstrap.Bootstrap(context.Background(), request)
	if err != nil {
		t.Fatalf("bootstrap signed Genesis: %v", err)
	}
	replayed, err := bootstrap.Bootstrap(context.Background(), request)
	if err != nil || !sameActivationRecord(replayed, record) {
		t.Fatalf("replay signed Genesis: %v", err)
	}
	activation, _ := NewActivationService(store, authority, NewGovernanceVerifier())
	if _, err := activation.ResolveActive(context.Background(), profile.Workload); err != nil {
		t.Fatalf("resolve active Genesis: %v", err)
	}

	candidate := validModelProfile()
	baseline := BaselineBinding{
		ActivationFence: record.Fence, Generation: 1, MetricsHash: testDigest("Genesis-shadow-metrics"),
		Profile:       CorpusProfileBinding{ID: record.ProfileID, ContentHash: record.ProfileContentHash, Workload: record.Workload},
		ReceiptDigest: record.ReceiptDigest,
	}
	normal := buildGovernanceFixture(t, now, keys, candidate, 2, record.Fence, baseline, RoleProfileApprover)
	if err := authority.PutGovernanceMaterials(normal.receiptDigest, normal.materials); err != nil {
		t.Fatal(err)
	}
	normalRequest := ActivationRequest{
		OperationID: "72000000-0000-4000-8000-000000000002", ReceiptDigest: normal.receiptDigest,
		ExpectedGeneration: 1, ExpectedFence: record.Fence,
	}
	normalHash, _ := activationRequestHash(normalRequest)
	normalRecord := activationRecordFromVerified(normalRequest, normalHash, mustVerifyFixture(t, normal, keys.policy, now), now)
	setEnabledState(t, authority, normalRecord, now, now.Add(MaximumDisableStateLifetime))
	if _, err := activation.Activate(context.Background(), normalRequest); err != nil {
		t.Fatalf("first ordinary activation from Genesis: %v", err)
	}
}

func TestGenesisRejectsRoleSubstitutionTamperRevocationAndExistingHead(t *testing.T) {
	now := governanceTestNow()
	keys := newGovernanceTestKeys(t, now, "", time.Time{})
	ordinary := buildGovernanceFixture(t, now, keys, validModelProfile(), 1, testDigest("unused"), BaselineBinding{
		ActivationFence: testDigest("unused-baseline"), Generation: 7, MetricsHash: testDigest("unused-metrics"),
		Profile:       CorpusProfileBinding{ID: "99999999-9999-4999-8999-999999999999", ContentHash: testDigest("unused-profile"), Workload: validModelProfile().Workload},
		ReceiptDigest: testDigest("unused-receipt"),
	}, RoleProfileApprover)
	wrongRole := buildGenesisFixture(t, ordinary, keys, keys.revocations, RoleActivationApprover)
	if _, err := NewGovernanceVerifier().VerifyGenesis(wrongRole.materials, wrongRole.receiptDigest, keys.policy, keys.revocations, now); !errors.Is(err, ErrGovernanceUntrusted) {
		t.Fatalf("activation approver substituted for Genesis approver: %v", err)
	}

	valid := buildGenesisFixture(t, ordinary, keys, keys.revocations, RoleGenesisApprover)
	tampered := cloneGenesisGovernanceMaterials(valid.materials)
	tampered.ModelProfileJSON = append([]byte{}, tampered.ModelProfileJSON...)
	tampered.ModelProfileJSON[len(tampered.ModelProfileJSON)-1] ^= 1
	if _, err := NewGovernanceVerifier().VerifyGenesis(tampered, valid.receiptDigest, keys.policy, keys.revocations, now); err == nil {
		t.Fatal("tampered Genesis profile bytes were accepted")
	}

	revoked := cloneGovernanceRevocationAuthority(keys.revocations)
	signer := keys.policy.Signers[RoleGenesisApprover]
	revoked.SignerRevocations = []GovernanceSignerRevocation{{
		PolicyHash: keys.policy.PolicyHash, KeyID: RoleGenesisApprover, PublicKeyHash: sha256Digest(signer.PublicKey),
		ReasonHash: testDigest("revoked-Genesis-key"), RevokedAt: now.Add(-time.Millisecond),
	}}
	revoked.AuthorityHash = mustGovernanceRevocationAuthorityHash(t, revoked)
	if _, err := NewGovernanceVerifier().VerifyGenesis(valid.materials, valid.receiptDigest, keys.policy, revoked, now); err == nil {
		t.Fatal("revoked Genesis signing key was accepted")
	}

	substituted := cloneGovernanceTrustPolicy(keys.policy)
	genesisSigner := substituted.Signers[RoleGenesisApprover]
	activationSigner := substituted.Signers[RoleActivationApprover]
	genesisSigner.Identity = activationSigner.Identity
	substituted.Signers[RoleGenesisApprover] = genesisSigner
	if err := ValidateGovernanceTrustPolicy(substituted); err == nil {
		t.Fatal("Genesis approver identity shared with an existing authority role")
	}
	substituted = cloneGovernanceTrustPolicy(keys.policy)
	genesisSigner = substituted.Signers[RoleGenesisApprover]
	genesisSigner.PublicKey = append(genesisSigner.PublicKey[:0:0], activationSigner.PublicKey...)
	substituted.Signers[RoleGenesisApprover] = genesisSigner
	if err := ValidateGovernanceTrustPolicy(substituted); err == nil {
		t.Fatal("Genesis approver public key shared with an existing authority role")
	}
}

func TestGenesisPayloadTypesWindowsAndCurrentAuthorityDriftFailClosed(t *testing.T) {
	now := governanceTestNow()
	keys := newGovernanceTestKeys(t, now, "", time.Time{})
	ordinary := genesisOrdinaryFixture(t, now, keys, validModelProfile())
	genesis := buildGenesisFixture(t, ordinary, keys, keys.revocations, RoleGenesisApprover)

	interchangedGenesis := genesis.materials
	interchangedGenesis.ReceiptEnvelope = ordinary.materials.ReceiptEnvelope
	if _, err := NewGovernanceVerifier().VerifyGenesis(interchangedGenesis, ordinary.receiptDigest, keys.policy, keys.revocations, now); err == nil {
		t.Fatal("ordinary receipt payload type was accepted as Genesis receipt")
	}
	interchangedActivation := ordinary.materials
	interchangedActivation.ReceiptEnvelope = genesis.materials.ReceiptEnvelope
	if _, err := NewGovernanceVerifier().Verify(interchangedActivation, genesis.receiptDigest, keys.policy, keys.revocations, now); err == nil {
		t.Fatal("Genesis receipt payload type was accepted as ordinary activation receipt")
	}

	expiredOrdinary := genesisOrdinaryFixture(t, now.Add(-20*time.Hour), keys, validModelProfile())
	expired := buildGenesisFixture(t, expiredOrdinary, keys, keys.revocations, RoleGenesisApprover)
	if _, err := NewGovernanceVerifier().VerifyGenesis(expired.materials, expired.receiptDigest, keys.policy, keys.revocations, now); err == nil {
		t.Fatal("expired Genesis authority chain was accepted")
	}
	futureOrdinary := genesisOrdinaryFixture(t, now.Add(10*time.Hour), keys, validModelProfile())
	future := buildGenesisFixture(t, futureOrdinary, keys, keys.revocations, RoleGenesisApprover)
	if _, err := NewGovernanceVerifier().VerifyGenesis(future.materials, future.receiptDigest, keys.policy, keys.revocations, now); err == nil {
		t.Fatal("future-issued Genesis authority chain was accepted")
	}

	rotated := newRotatedGovernanceTestKeys(t, now, "Genesis-trust-drift")
	if _, err := NewGovernanceVerifier().VerifyGenesis(genesis.materials, genesis.receiptDigest, rotated.policy, keys.revocations, now); err == nil {
		t.Fatal("Genesis signed under a different trust policy was accepted")
	}
	nextRevocations := nextGovernanceRevocationAuthority(t, keys.revocations, now, nil, nil)
	store, _ := NewMemoryActivationStore(func() time.Time { return now })
	authority, _ := NewMemoryGovernanceAuthority(keys.policy, keys.revocations)
	if err := authority.PutGenesisGovernanceMaterials(genesis.receiptDigest, genesis.materials); err != nil {
		t.Fatal(err)
	}
	if err := authority.SetCurrentProviderRouteAuthority(genesis.materials.ProviderRouteAuthorityJSON); err != nil {
		t.Fatal(err)
	}
	if err := authority.SetRevocationAuthority(nextRevocations); err != nil {
		t.Fatal(err)
	}
	request := GenesisBootstrapRequest{OperationID: "73000000-0000-4000-8000-000000000001", ReceiptDigest: genesis.receiptDigest, ExpectedEmptyFence: genesis.genesis.PreviousFence}
	hash, _ := genesisBootstrapRequestHash(request)
	predicted := genesisRecordFromVerified(request, hash, genesis.verified, now)
	setEnabledState(t, authority, predicted, now, now.Add(MaximumDisableStateLifetime))
	service, _ := NewGenesisBootstrapService(store, authority, NewGovernanceVerifier())
	if _, err := service.Bootstrap(context.Background(), request); !errors.Is(err, ErrGovernanceUntrusted) {
		t.Fatalf("Genesis initial/current revocation drift was accepted: %v", err)
	}
}

func TestGenesisStoreIdempotencyExistingHeadConcurrencyAndUnknownOutcome(t *testing.T) {
	now := governanceTestNow()
	keys := newGovernanceTestKeys(t, now, "", time.Time{})
	store, _ := NewMemoryActivationStore(func() time.Time { return now })
	authority, _ := NewMemoryGovernanceAuthority(keys.policy, keys.revocations)
	makeRequest := func(operationID string, profile ModelProfile) (GenesisBootstrapRequest, ActivationRecord) {
		ordinary := genesisOrdinaryFixture(t, now, keys, profile)
		genesis := buildGenesisFixture(t, ordinary, keys, keys.revocations, RoleGenesisApprover)
		if err := authority.PutGenesisGovernanceMaterials(genesis.receiptDigest, genesis.materials); err != nil {
			t.Fatal(err)
		}
		if err := authority.SetCurrentProviderRouteAuthority(genesis.materials.ProviderRouteAuthorityJSON); err != nil {
			t.Fatal(err)
		}
		request := GenesisBootstrapRequest{OperationID: operationID, ReceiptDigest: genesis.receiptDigest, ExpectedEmptyFence: genesis.genesis.PreviousFence}
		hash, _ := genesisBootstrapRequestHash(request)
		record := genesisRecordFromVerified(request, hash, genesis.verified, now)
		setEnabledState(t, authority, record, now, now.Add(MaximumDisableStateLifetime))
		return request, record
	}
	firstProfile := validModelProfile()
	firstProfile.ID = "21111111-1111-4111-8111-111111111111"
	request, record := makeRequest("73000000-0000-4000-8000-000000000011", firstProfile)
	service, _ := NewGenesisBootstrapService(store, authority, NewGovernanceVerifier())
	stored, err := service.Bootstrap(context.Background(), request)
	if err != nil || !sameActivationRecord(stored, record) {
		t.Fatalf("first Genesis bootstrap: %v", err)
	}
	differentRequest := request
	differentRequest.ReceiptDigest = testDigest("same-operation-different-receipt")
	if _, err := service.Bootstrap(context.Background(), differentRequest); !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("same Genesis operation with different bytes: %v", err)
	}
	secondProfile := validModelProfile()
	secondProfile.ID = "23333333-3333-4333-8333-333333333333"
	secondRequest, _ := makeRequest("73000000-0000-4000-8000-000000000012", secondProfile)
	if _, err := service.Bootstrap(context.Background(), secondRequest); !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("Genesis over an existing workload head: %v", err)
	}

	raceStore, _ := NewMemoryActivationStore(func() time.Time { return now })
	raceAuthority, _ := NewMemoryGovernanceAuthority(keys.policy, keys.revocations)
	raceMake := func(operationID, profileID string) GenesisBootstrapRequest {
		profile := validModelProfile()
		profile.ID = profileID
		ordinary := genesisOrdinaryFixture(t, now, keys, profile)
		genesis := buildGenesisFixture(t, ordinary, keys, keys.revocations, RoleGenesisApprover)
		_ = raceAuthority.PutGenesisGovernanceMaterials(genesis.receiptDigest, genesis.materials)
		_ = raceAuthority.SetCurrentProviderRouteAuthority(genesis.materials.ProviderRouteAuthorityJSON)
		request := GenesisBootstrapRequest{OperationID: operationID, ReceiptDigest: genesis.receiptDigest, ExpectedEmptyFence: genesis.genesis.PreviousFence}
		hash, _ := genesisBootstrapRequestHash(request)
		setEnabledState(t, raceAuthority, genesisRecordFromVerified(request, hash, genesis.verified, now), now, now.Add(MaximumDisableStateLifetime))
		return request
	}
	left := raceMake("73000000-0000-4000-8000-000000000021", "24444444-4444-4444-8444-444444444444")
	right := raceMake("73000000-0000-4000-8000-000000000022", "25555555-5555-4555-8555-555555555555")
	raceService, _ := NewGenesisBootstrapService(raceStore, raceAuthority, NewGovernanceVerifier())
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, item := range []GenesisBootstrapRequest{left, right} {
		wait.Add(1)
		go func(value GenesisBootstrapRequest) {
			defer wait.Done()
			_, bootstrapErr := raceService.Bootstrap(context.Background(), value)
			results <- bootstrapErr
		}(item)
	}
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for result := range results {
		if result == nil {
			successes++
		} else if errors.Is(result, ErrActivationConflict) {
			conflicts++
		} else {
			t.Fatalf("Genesis race: %v", result)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("Genesis race successes=%d conflicts=%d", successes, conflicts)
	}

	unknownStore, _ := NewMemoryActivationStore(func() time.Time { return now })
	unknownAuthority, _ := NewMemoryGovernanceAuthority(keys.policy, keys.revocations)
	unknownProfile := validModelProfile()
	unknownProfile.ID = "26666666-6666-4666-8666-666666666666"
	ordinary := genesisOrdinaryFixture(t, now, keys, unknownProfile)
	genesis := buildGenesisFixture(t, ordinary, keys, keys.revocations, RoleGenesisApprover)
	_ = unknownAuthority.PutGenesisGovernanceMaterials(genesis.receiptDigest, genesis.materials)
	_ = unknownAuthority.SetCurrentProviderRouteAuthority(genesis.materials.ProviderRouteAuthorityJSON)
	unknownRequest := GenesisBootstrapRequest{OperationID: "73000000-0000-4000-8000-000000000031", ReceiptDigest: genesis.receiptDigest, ExpectedEmptyFence: genesis.genesis.PreviousFence}
	unknownHash, _ := genesisBootstrapRequestHash(unknownRequest)
	setEnabledState(t, unknownAuthority, genesisRecordFromVerified(unknownRequest, unknownHash, genesis.verified, now), now, now.Add(MaximumDisableStateLifetime))
	wrapper := &unknownGenesisStore{GenesisBootstrapStore: unknownStore, commit: true}
	unknownService, _ := NewGenesisBootstrapService(wrapper, unknownAuthority, NewGovernanceVerifier())
	if _, err := unknownService.Bootstrap(context.Background(), unknownRequest); err != nil || wrapper.calls != 1 {
		t.Fatalf("committed unknown Genesis was not recovered without retry: calls=%d err=%v", wrapper.calls, err)
	}
}

func TestMemoryTrustPolicyObservationIsRevocationEpochFenced(t *testing.T) {
	now := governanceTestNow()
	keys := newGovernanceTestKeys(t, now, "", time.Time{})
	store, _ := NewMemoryActivationStore(func() time.Time { return now })
	if err := store.ObserveGovernanceRevocationAuthority(context.Background(), keys.revocations); err != nil {
		t.Fatal(err)
	}
	first := GovernanceTrustPolicyObservation{
		PolicyHash: keys.policy.PolicyHash, RevocationAuthorityHash: keys.revocations.AuthorityHash, RevocationEpoch: keys.revocations.Epoch,
	}
	if err := store.ObserveGovernanceTrustPolicy(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveGovernanceTrustPolicy(context.Background(), first); err != nil {
		t.Fatalf("exact trust observation replay: %v", err)
	}
	equivocation := first
	equivocation.PolicyHash = testDigest("same-epoch-trust-equivocation")
	if err := store.ObserveGovernanceTrustPolicy(context.Background(), equivocation); !errors.Is(err, ErrGovernanceUntrusted) {
		t.Fatalf("same-epoch trust equivocation: %v", err)
	}
	next := nextGovernanceRevocationAuthority(t, keys.revocations, now, nil, nil)
	if err := store.ObserveGovernanceRevocationAuthority(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	rotated := GovernanceTrustPolicyObservation{
		PolicyHash: testDigest("rotated-policy"), RevocationAuthorityHash: next.AuthorityHash, RevocationEpoch: next.Epoch,
	}
	if err := store.ObserveGovernanceTrustPolicy(context.Background(), rotated); err != nil {
		t.Fatalf("higher-epoch trust rotation: %v", err)
	}
	if err := store.ObserveGovernanceTrustPolicy(context.Background(), first); !errors.Is(err, ErrGovernanceUntrusted) {
		t.Fatalf("trust-policy rollback: %v", err)
	}
}

type unknownGenesisStore struct {
	GenesisBootstrapStore
	commit bool
	calls  int
}

func (store *unknownGenesisStore) AppendGenesis(ctx context.Context, command GenesisAppend) (ActivationRecord, error) {
	store.calls++
	if store.commit {
		if _, err := store.GenesisBootstrapStore.AppendGenesis(ctx, command); err != nil {
			return ActivationRecord{}, err
		}
	}
	return ActivationRecord{}, ErrActivationOutcomeUnknown
}

func genesisOrdinaryFixture(t *testing.T, now time.Time, keys governanceTestKeys, profile ModelProfile) governanceFixture {
	t.Helper()
	return buildGovernanceFixture(t, now, keys, profile, 1, testDigest("unused-ordinary-fence"), BaselineBinding{
		ActivationFence: testDigest("unused-ordinary-baseline"), Generation: 7, MetricsHash: testDigest("unused-ordinary-metrics"),
		Profile:       CorpusProfileBinding{ID: "99999999-9999-4999-8999-999999999999", ContentHash: testDigest("unused-ordinary-profile"), Workload: profile.Workload},
		ReceiptDigest: testDigest("unused-ordinary-receipt"),
	}, RoleProfileApprover)
}
