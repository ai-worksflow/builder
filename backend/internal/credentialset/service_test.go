package credentialset

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
	"github.com/worksflow/builder/backend/internal/templateauthority"
)

const (
	testIssuer            = "spiffe://golden.example/credential-issuer"
	testAudience          = "urn:worksflow:golden-stack"
	testSetID             = "10000000-0000-4000-8000-000000000001"
	testRunID             = "10000000-0000-4000-8000-000000000002"
	testFixtureID         = "10000000-0000-4000-8000-000000000003"
	testIssueOperationID  = "10000000-0000-4000-8000-000000000004"
	testRevokeOperationID = "10000000-0000-4000-8000-000000000005"
)

type fixedClock struct {
	mu  sync.RWMutex
	now time.Time
}

type typedNilContext struct{}

func (*typedNilContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*typedNilContext) Done() <-chan struct{}       { return nil }
func (*typedNilContext) Err() error                  { return nil }
func (*typedNilContext) Value(any) any               { return nil }

func (clock *fixedClock) Now() time.Time {
	clock.mu.RLock()
	defer clock.mu.RUnlock()
	return clock.now
}

func (clock *fixedClock) Set(value time.Time) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = value
}

type testDelivery struct {
	hash   string
	secret string
}

func (*testDelivery) CredentialSetDeliveryHandle()             {}
func (delivery *testDelivery) CredentialSetHandleHash() string { return delivery.hash }

type fakeBroker struct {
	mu sync.Mutex

	binding     SetBinding
	delivery    *testDelivery
	issueStage  BrokerIssueStage
	revokeStage BrokerRevokeStage
	revokedAt   string

	prepareCalls       int
	activateCalls      int
	inspectIssueCalls  int
	revokeCalls        int
	inspectRevokeCalls int

	prepareUnknown  bool
	activateUnknown bool
	revokeUnknown   bool
	partialPrepare  bool
	errorText       string
	afterPrepare    func()
	afterActivate   func()
	afterRevoke     func()
}

func (broker *fakeBroker) PrepareSet(_ context.Context, request BrokerPrepareRequest) (BrokerIssueObservation, error) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	broker.prepareCalls++
	bindings := make([]MemberBinding, len(request.Members))
	for index, member := range request.Members {
		bindings[index] = MemberBinding{
			ActorID: member.ActorID, Kind: member.Kind, Slot: member.Slot,
			CredentialHandleHash: sha256Digest([]byte("member-handle/" + member.Slot)),
		}
	}
	if broker.partialPrepare {
		bindings = bindings[:len(bindings)-1]
	}
	digest, _ := MemberBindingsDigest(bindings)
	setHash := sha256Digest([]byte("opaque-delivery-capability"))
	broker.binding = SetBinding{
		Audience: request.Audience, ExpiresAt: request.ExpiresAt, FixtureID: request.FixtureID,
		IssuedAt: request.IssuedAt, Issuer: request.Issuer, MemberBindingsDigest: digest,
		MemberCount: len(bindings), Members: bindings, RunID: request.RunID,
		SetHandleHash: setHash, SetID: request.SetID,
	}
	broker.delivery = &testDelivery{hash: setHash, secret: "raw-token=session-cookie;/tmp/credential.json"}
	broker.issueStage = BrokerIssuePrepared
	observation := BrokerIssueObservation{Binding: cloneBinding(broker.binding), OperationID: request.OperationID, Stage: BrokerIssuePrepared}
	if broker.afterPrepare != nil {
		broker.afterPrepare()
	}
	if broker.prepareUnknown {
		broker.prepareUnknown = false
		return BrokerIssueObservation{}, errors.New(broker.errorText)
	}
	return observation, nil
}

func (broker *fakeBroker) ActivateSet(_ context.Context, reference BrokerOperationRef) (BrokerIssueObservation, error) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	broker.activateCalls++
	broker.issueStage = BrokerIssueActive
	observation := BrokerIssueObservation{
		Binding: cloneBinding(broker.binding), Delivery: broker.delivery,
		OperationID: reference.OperationID, Stage: BrokerIssueActive,
	}
	if broker.afterActivate != nil {
		broker.afterActivate()
	}
	if broker.activateUnknown {
		broker.activateUnknown = false
		return BrokerIssueObservation{}, errors.New(broker.errorText)
	}
	return observation, nil
}

func (broker *fakeBroker) InspectIssue(_ context.Context, reference BrokerOperationRef) (BrokerIssueObservation, error) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	broker.inspectIssueCalls++
	observation := BrokerIssueObservation{
		Binding: cloneBinding(broker.binding), OperationID: reference.OperationID, Stage: broker.issueStage,
	}
	if broker.issueStage == BrokerIssueActive {
		observation.Delivery = broker.delivery
	}
	return observation, nil
}

func (broker *fakeBroker) RevokeSet(_ context.Context, request BrokerRevokeRequest) (BrokerRevokeObservation, error) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	broker.revokeCalls++
	broker.revokeStage = BrokerRevokeDone
	broker.revokedAt = request.RevokedAt
	observation := BrokerRevokeObservation{
		Binding: cloneBinding(request.Binding), OperationID: request.OperationID,
		RevokedAt: request.RevokedAt, Stage: BrokerRevokeDone,
	}
	if broker.afterRevoke != nil {
		broker.afterRevoke()
	}
	if broker.revokeUnknown {
		broker.revokeUnknown = false
		return BrokerRevokeObservation{}, errors.New(broker.errorText)
	}
	return observation, nil
}

func (broker *fakeBroker) InspectRevocation(_ context.Context, reference BrokerOperationRef) (BrokerRevokeObservation, error) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	broker.inspectRevokeCalls++
	return BrokerRevokeObservation{
		Binding: cloneBinding(broker.binding), OperationID: reference.OperationID,
		RevokedAt: broker.revokedAt, Stage: broker.revokeStage,
	}, nil
}

func (broker *fakeBroker) counts() (int, int, int, int, int) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	return broker.prepareCalls, broker.activateCalls, broker.inspectIssueCalls, broker.revokeCalls, broker.inspectRevokeCalls
}

type fakeSigner struct {
	mu           sync.Mutex
	private      ed25519.PrivateKey
	public       ed25519.PublicKey
	results      map[string]SignObservation
	signCalls    int
	inspectCalls int
	unknownNext  bool
	errorText    string
}

func newFakeSigner(t *testing.T) *fakeSigner {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeSigner{private: private, public: public, results: make(map[string]SignObservation)}
}

func (signer *fakeSigner) Sign(_ context.Context, request SignRequest) (SignObservation, error) {
	signer.mu.Lock()
	defer signer.mu.Unlock()
	signer.signCalls++
	observation := SignObservation{
		KeyID: "credential-issuer-ed25519", OperationID: request.OperationID,
		Signature: ed25519.Sign(signer.private, request.PAE),
	}
	signer.results[request.OperationID] = observation
	if signer.unknownNext {
		signer.unknownNext = false
		return SignObservation{}, errors.New(signer.errorText)
	}
	return cloneSignObservation(observation), nil
}

func (signer *fakeSigner) Inspect(_ context.Context, operationID string) (SignObservation, error) {
	signer.mu.Lock()
	defer signer.mu.Unlock()
	signer.inspectCalls++
	observation, exists := signer.results[operationID]
	if !exists {
		return SignObservation{}, errors.New("pending")
	}
	return cloneSignObservation(observation), nil
}

func cloneSignObservation(value SignObservation) SignObservation {
	value.Signature = append([]byte(nil), value.Signature...)
	return value
}

func (signer *fakeSigner) counts() (int, int) {
	signer.mu.Lock()
	defer signer.mu.Unlock()
	return signer.signCalls, signer.inspectCalls
}

func goldenMembers() []MemberRequest {
	actor := func(value int) string { return fmt.Sprintf("10000000-0000-4000-8000-%012d", value) }
	actors := map[string]string{
		"platform-admin": actor(101), "platform-user-a": actor(102), "platform-user-b": actor(103),
		"fault-operator": actor(104), "platform-owner": actor(105),
		"reference-user-a": actor(106), "reference-user-b": actor(107),
	}
	members := make([]MemberRequest, len(goldenSlots))
	for index, definition := range goldenSlots {
		members[index] = MemberRequest{ActorID: actors[definition.group], Kind: definition.kind, Slot: definition.slot}
	}
	return members
}

type testRig struct {
	broker  *fakeBroker
	clock   *fixedClock
	service *Service
	signer  *fakeSigner
	store   *MemoryStore
	command IssueCommand
}

type ambiguousStore struct {
	base   *MemoryStore
	commit bool
	kind   EventKind
	mu     sync.Mutex
	once   bool
}

func (store *ambiguousStore) CreateIssue(ctx context.Context, setID string, event Event) (Snapshot, bool, error) {
	return store.base.CreateIssue(ctx, setID, event)
}

func (store *ambiguousStore) TrustedTime(ctx context.Context) (time.Time, error) {
	return store.base.TrustedTime(ctx)
}

func (store *ambiguousStore) Load(ctx context.Context, setID string) (Snapshot, error) {
	return store.base.Load(ctx, setID)
}

func (store *ambiguousStore) Events(ctx context.Context, setID string) ([]Event, error) {
	return store.base.Events(ctx, setID)
}

func (store *ambiguousStore) Append(ctx context.Context, setID string, version uint64, event Event) (Snapshot, error) {
	store.mu.Lock()
	lose := !store.once && event.Kind == store.kind
	if lose {
		store.once = true
	}
	store.mu.Unlock()
	if !lose {
		return store.base.Append(ctx, setID, version, event)
	}
	if store.commit {
		if _, err := store.base.Append(ctx, setID, version, event); err != nil {
			return Snapshot{}, err
		}
	}
	return Snapshot{}, ErrStoreOutcomeUnknown
}

func useStore(t *testing.T, rig *testRig, store Store) {
	t.Helper()
	service, err := NewGoldenService(Config{
		Audience: testAudience, Broker: rig.broker, Clock: rig.clock, Issuer: testIssuer,
		Signer: rig.signer, Store: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	rig.service = service
}

func newTestRig(t *testing.T) testRig {
	t.Helper()
	issuedAt := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := &fixedClock{now: issuedAt.Add(10 * time.Second)}
	broker := &fakeBroker{errorText: "raw-token=session-cookie;/tmp/credential.json"}
	signer := newFakeSigner(t)
	store := NewMemoryStore(clock)
	service, err := NewGoldenService(Config{
		Audience: testAudience, Broker: broker, Clock: clock, Issuer: testIssuer,
		Signer: signer, Store: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	return testRig{
		broker: broker, clock: clock, service: service, signer: signer, store: store,
		command: IssueCommand{
			Audience: testAudience, ExpiresAt: issuedAt.Add(10 * time.Minute), FixtureID: testFixtureID,
			IssuedAt: issuedAt, Issuer: testIssuer, Members: goldenMembers(),
			OperationID: testIssueOperationID, RunID: testRunID, SetID: testSetID,
		},
	}
}

func TestAtomicIssueAndRevokeProduceCompatibleAttestationsWithoutPersistingDelivery(t *testing.T) {
	trig := newTestRig(t)
	ctx := context.Background()
	issued, err := trig.service.Issue(ctx, trig.command)
	if err != nil {
		t.Fatal(err)
	}
	delivery, ok := issued.Delivery.(*testDelivery)
	if !ok || delivery.secret == "" || delivery.CredentialSetHandleHash() != issued.Binding.SetHandleHash {
		t.Fatal("service did not pass through the broker-owned opaque delivery handle")
	}
	publicResult, err := json.Marshal(issued)
	if err != nil || strings.Contains(string(publicResult), delivery.secret) {
		t.Fatal("opaque broker delivery capability entered a serializable domain result")
	}
	verifyCredentialEnvelope(t, trig.signer.public, issued.Attestation, IssuancePredicateTypeV1, issued.Binding, false)

	// Idempotent replay returns public artifacts but never replays the one-time
	// delivery capability or any mutating broker/signing operation.
	replayed, err := trig.service.Issue(ctx, trig.command)
	if !errors.Is(err, ErrDeliveryOutcomeUnknown) {
		t.Fatalf("completed replay error = %v", err)
	}
	if replayed.Delivery != nil || replayed.Attestation.PayloadDigest != issued.Attestation.PayloadDigest {
		t.Fatal("completed issue replay leaked delivery state or changed its attestation")
	}
	// Even after the credential window, status replay returns the immutable
	// public artifact plus an explicit delivery blocker; it never re-inspects
	// the broker to mint or expose another bearer capability.
	trig.clock.Set(time.Date(2026, 7, 19, 12, 20, 0, 0, time.UTC))
	lateReplay, err := trig.service.Issue(ctx, trig.command)
	if !errors.Is(err, ErrDeliveryOutcomeUnknown) || lateReplay.Delivery != nil ||
		lateReplay.Attestation.PayloadDigest != issued.Attestation.PayloadDigest {
		t.Fatalf("late delivery replay = %#v, %v", lateReplay, err)
	}

	trig.clock.Set(time.Date(2026, 7, 19, 12, 6, 0, 0, time.UTC))
	revoked, err := trig.service.Revoke(ctx, RevokeCommand{Binding: issued.Binding, OperationID: testRevokeOperationID})
	if err != nil {
		t.Fatal(err)
	}
	verifyCredentialEnvelope(t, trig.signer.public, revoked.Attestation, RevocationPredicateTypeV1, revoked.Binding, true)
	if prepare, activate, inspect, revoke, _ := trig.broker.counts(); prepare != 1 || activate != 1 || inspect != 0 || revoke != 1 {
		t.Fatalf("atomic broker calls = prepare %d activate %d inspect %d revoke %d; replay must not re-deliver", prepare, activate, inspect, revoke)
	}
	if sign, _ := trig.signer.counts(); sign != 2 {
		t.Fatalf("sign calls = %d, want one issuance plus one revocation", sign)
	}

	snapshot, err := trig.store.Load(ctx, testSetID)
	if err != nil || snapshot.Phase != PhaseComplete {
		t.Fatalf("final snapshot = %#v, %v", snapshot, err)
	}
	events, err := trig.store.Events(ctx, testSetID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := json.Marshal(struct {
		Events   []Event
		Snapshot Snapshot
	}{events, snapshot})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"raw-token", "session-cookie", "/tmp/credential.json", "Authorization"} {
		if strings.Contains(string(persisted), forbidden) {
			t.Fatalf("persisted non-secret ledger contains %q", forbidden)
		}
	}
}

func TestUnknownOutcomesOnlyInspectSameOperationAndNeverRepeatMutations(t *testing.T) {
	t.Run("prepare", func(t *testing.T) {
		trig := newTestRig(t)
		trig.broker.prepareUnknown = true
		if _, err := trig.service.Issue(context.Background(), trig.command); !errors.Is(err, ErrOutcomeUnknown) || strings.Contains(err.Error(), "raw-token") {
			t.Fatalf("first issue error = %v", err)
		}
		if _, err := trig.service.Issue(context.Background(), trig.command); err != nil {
			t.Fatal(err)
		}
		prepare, activate, inspect, _, _ := trig.broker.counts()
		if prepare != 1 || activate != 1 || inspect < 1 {
			t.Fatalf("calls after unknown prepare = prepare %d activate %d inspect %d", prepare, activate, inspect)
		}
	})

	t.Run("activation", func(t *testing.T) {
		trig := newTestRig(t)
		trig.broker.activateUnknown = true
		if _, err := trig.service.Issue(context.Background(), trig.command); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("first issue error = %v", err)
		}
		if _, err := trig.service.Issue(context.Background(), trig.command); err != nil {
			t.Fatal(err)
		}
		prepare, activate, inspect, _, _ := trig.broker.counts()
		if prepare != 1 || activate != 1 || inspect < 1 {
			t.Fatalf("calls after unknown activation = prepare %d activate %d inspect %d", prepare, activate, inspect)
		}
	})

	t.Run("sign-after-activation", func(t *testing.T) {
		trig := newTestRig(t)
		trig.signer.unknownNext = true
		trig.signer.errorText = "raw-token=/tmp/credential.json"
		if _, err := trig.service.Issue(context.Background(), trig.command); !errors.Is(err, ErrOutcomeUnknown) || strings.Contains(err.Error(), "raw-token") {
			t.Fatalf("first issue error = %v", err)
		}
		result, err := trig.service.Issue(context.Background(), trig.command)
		if err != nil {
			t.Fatal(err)
		}
		if result.Delivery == nil {
			t.Fatal("read-only broker inspection did not recover the opaque delivery handle")
		}
		prepare, activate, inspect, _, _ := trig.broker.counts()
		sign, signInspect := trig.signer.counts()
		if prepare != 1 || activate != 1 || inspect < 1 || sign != 1 || signInspect != 1 {
			t.Fatalf("recovery calls = broker %d/%d/%d signer %d/%d", prepare, activate, inspect, sign, signInspect)
		}
	})

	t.Run("revocation", func(t *testing.T) {
		trig := newTestRig(t)
		issued, err := trig.service.Issue(context.Background(), trig.command)
		if err != nil {
			t.Fatal(err)
		}
		trig.clock.Set(time.Date(2026, 7, 19, 12, 6, 0, 0, time.UTC))
		trig.broker.revokeUnknown = true
		command := RevokeCommand{Binding: issued.Binding, OperationID: testRevokeOperationID}
		if _, err := trig.service.Revoke(context.Background(), command); !errors.Is(err, ErrOutcomeUnknown) || strings.Contains(err.Error(), "raw-token") {
			t.Fatalf("first revoke error = %v", err)
		}
		if _, err := trig.service.Revoke(context.Background(), command); err != nil {
			t.Fatal(err)
		}
		_, _, _, revoke, inspect := trig.broker.counts()
		if revoke != 1 || inspect != 1 {
			t.Fatalf("calls after unknown revoke = revoke %d inspect %d", revoke, inspect)
		}
	})
}

func TestStoreAppendResponseLossReconcilesExactEventWithoutRepeatingSideEffects(t *testing.T) {
	issueEvents := []EventKind{
		EventPrepareStarted, EventPrepared, EventActivationStarted,
		EventActivated, EventIssuanceSignStarted, EventIssued,
	}
	for _, kind := range issueEvents {
		t.Run(string(kind), func(t *testing.T) {
			trig := newTestRig(t)
			useStore(t, &trig, &ambiguousStore{base: trig.store, commit: true, kind: kind})
			result, err := trig.service.Issue(context.Background(), trig.command)
			if err != nil || result.Delivery == nil {
				t.Fatalf("issue with committed response loss = %#v, %v", result, err)
			}
			prepare, activate, _, _, _ := trig.broker.counts()
			sign, _ := trig.signer.counts()
			if prepare != 1 || activate != 1 || sign != 1 {
				t.Fatalf("response loss repeated side effects: prepare %d activate %d sign %d", prepare, activate, sign)
			}
		})
	}

	revocationEvents := []EventKind{
		EventRevocationReserved, EventRevocationStarted, EventRevoked,
		EventRevocationSignStarted, EventRevocationAttested,
	}
	for _, kind := range revocationEvents {
		t.Run(string(kind), func(t *testing.T) {
			trig := newTestRig(t)
			useStore(t, &trig, &ambiguousStore{base: trig.store, commit: true, kind: kind})
			issued, err := trig.service.Issue(context.Background(), trig.command)
			if err != nil {
				t.Fatal(err)
			}
			trig.clock.Set(time.Date(2026, 7, 19, 12, 6, 0, 0, time.UTC))
			if _, err := trig.service.Revoke(context.Background(), RevokeCommand{
				Binding: issued.Binding, OperationID: testRevokeOperationID,
			}); err != nil {
				t.Fatal(err)
			}
			_, _, _, revoke, _ := trig.broker.counts()
			sign, _ := trig.signer.counts()
			if revoke != 1 || sign != 2 {
				t.Fatalf("revocation response loss repeated side effects: revoke %d sign %d", revoke, sign)
			}
		})
	}
}

func TestUncommittedStoreUnknownFallsBackToInspectWithoutRepeatingActivation(t *testing.T) {
	trig := newTestRig(t)
	useStore(t, &trig, &ambiguousStore{base: trig.store, commit: false, kind: EventActivated})
	if _, err := trig.service.Issue(context.Background(), trig.command); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("uncommitted store outcome error = %v", err)
	}
	if _, err := trig.service.Issue(context.Background(), trig.command); err != nil {
		t.Fatal(err)
	}
	prepare, activate, inspect, _, _ := trig.broker.counts()
	if prepare != 1 || activate != 1 || inspect < 1 {
		t.Fatalf("store recovery repeated activation: prepare %d activate %d inspect %d", prepare, activate, inspect)
	}
}

func TestPartialBrokerSetAndIdempotencyDriftFailClosed(t *testing.T) {
	t.Run("partial prepare", func(t *testing.T) {
		trig := newTestRig(t)
		trig.broker.partialPrepare = true
		if _, err := trig.service.Issue(context.Background(), trig.command); !errors.Is(err, ErrInvalid) {
			t.Fatalf("partial result error = %v", err)
		}
		prepare, activate, _, _, _ := trig.broker.counts()
		if prepare != 1 || activate != 0 {
			t.Fatalf("partial set reached activation: prepare %d activate %d", prepare, activate)
		}
	})

	t.Run("same set different operation", func(t *testing.T) {
		trig := newTestRig(t)
		if _, err := trig.service.Issue(context.Background(), trig.command); err != nil {
			t.Fatal(err)
		}
		drift := trig.command
		drift.OperationID = "10000000-0000-4000-8000-000000000099"
		if _, err := trig.service.Issue(context.Background(), drift); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("drift error = %v", err)
		}
		prepare, activate, _, _, _ := trig.broker.counts()
		if prepare != 1 || activate != 1 {
			t.Fatal("idempotency drift reached broker")
		}
	})
}

func TestIssueAuthorityAndLifetimeFailClosedBeforeBroker(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testRig)
	}{
		{
			name: "over thirty minutes",
			mutate: func(value *testRig) {
				value.command.ExpiresAt = value.command.IssuedAt.Add(30*time.Minute + time.Millisecond)
			},
		},
		{
			name: "below Golden minimum",
			mutate: func(value *testRig) {
				value.command.ExpiresAt = value.command.IssuedAt.Add(time.Minute)
			},
		},
		{
			name: "audience substitution",
			mutate: func(value *testRig) {
				value.command.Audience = "urn:worksflow:foreign-stack"
			},
		},
		{
			name: "sub-millisecond time",
			mutate: func(value *testRig) {
				value.command.IssuedAt = value.command.IssuedAt.Add(time.Nanosecond)
			},
		},
		{
			name: "historical issuance beyond skew",
			mutate: func(value *testRig) {
				issuedAt := value.clock.Now().Add(-MaximumClockSkew - time.Millisecond)
				value.command.IssuedAt = issuedAt
				value.command.ExpiresAt = issuedAt.Add(10 * time.Minute)
			},
		},
		{
			name: "future issuance beyond skew",
			mutate: func(value *testRig) {
				issuedAt := value.clock.Now().Add(MaximumClockSkew + time.Millisecond)
				value.command.IssuedAt = issuedAt
				value.command.ExpiresAt = issuedAt.Add(10 * time.Minute)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trig := newTestRig(t)
			test.mutate(&trig)
			if _, err := trig.service.Issue(context.Background(), trig.command); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v", err)
			}
			prepare, activate, inspect, revoke, inspectRevoke := trig.broker.counts()
			if prepare+activate+inspect+revoke+inspectRevoke != 0 {
				t.Fatal("invalid authority input reached the broker")
			}
		})
	}
}

func TestTypedNilDependenciesAndContextsFailClosed(t *testing.T) {
	base := newTestRig(t)
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "broker", mutate: func(config *Config) { var value *fakeBroker; config.Broker = value }},
		{name: "signer", mutate: func(config *Config) { var value *fakeSigner; config.Signer = value }},
		{name: "store", mutate: func(config *Config) { var value *MemoryStore; config.Store = value }},
		{name: "clock", mutate: func(config *Config) { var value *fixedClock; config.Clock = value }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := Config{
				Audience: testAudience, Broker: base.broker, Clock: base.clock, Issuer: testIssuer,
				Signer: base.signer, Store: base.store,
			}
			test.mutate(&config)
			if _, err := NewGoldenService(config); !errors.Is(err, ErrInvalid) {
				t.Fatalf("typed-nil %s error = %v", test.name, err)
			}
		})
	}
	if _, err := base.service.Issue(nil, base.command); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil Issue context error = %v", err)
	}
	var typedNil *typedNilContext
	if _, err := base.service.Issue(typedNil, base.command); !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed-nil Issue context error = %v", err)
	}
	if _, err := base.store.Load(nil, testSetID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil Store context error = %v", err)
	}
}

func TestPostMutationClockFailureIsPropagatedAndRecoveredByInspection(t *testing.T) {
	trig := newTestRig(t)
	trig.broker.afterPrepare = func() { trig.clock.Set(time.Time{}) }
	if _, err := trig.service.Issue(context.Background(), trig.command); !errors.Is(err, ErrInvalid) {
		t.Fatalf("post-prepare clock error = %v", err)
	}
	prepare, activate, _, _, _ := trig.broker.counts()
	if prepare != 1 || activate != 0 {
		t.Fatalf("mutation calls before clock recovery = prepare %d activate %d", prepare, activate)
	}
	trig.broker.afterPrepare = nil
	trig.clock.Set(trig.command.IssuedAt.Add(11 * time.Second))
	if _, err := trig.service.Issue(context.Background(), trig.command); err != nil {
		t.Fatal(err)
	}
	prepare, activate, inspect, _, _ := trig.broker.counts()
	if prepare != 1 || activate != 1 || inspect < 1 {
		t.Fatalf("clock recovery repeated mutation: prepare %d activate %d inspect %d", prepare, activate, inspect)
	}
}

func TestGoldenMembershipAndCanonicalDigestVectors(t *testing.T) {
	if err := ValidateGoldenMembers(goldenMembers()); err != nil {
		t.Fatal(err)
	}
	drift := goldenMembers()
	drift[2].ActorID = drift[1].ActorID
	if err := ValidateGoldenMembers(drift); err == nil {
		t.Fatal("distinct Golden principal groups were allowed to share an actor")
	}
	partial := goldenMembers()[:10]
	if err := ValidateGoldenMembers(partial); err == nil {
		t.Fatal("partial Golden member set was accepted")
	}

	vector := []MemberBinding{
		{Slot: "api-a", ActorID: "2ada99cd-d941-4e4f-96c0-ad21b0ddcb57", Kind: MemberKindToken, CredentialHandleHash: sha256Digest([]byte("api-a-credential"))},
		{Slot: "browser-a", ActorID: "0d87efc5-006e-454c-8d1d-e32d459d0808", Kind: MemberKindStorageState, CredentialHandleHash: sha256Digest([]byte("browser-a-credential"))},
	}
	digest, err := MemberBindingsDigest(vector)
	if err != nil {
		t.Fatal(err)
	}
	const expected = "sha256:d9f0a3dbf9240ac7010c65eff8fa43bad8614135ad954a73402121b23a61475f"
	if digest != expected {
		t.Fatalf("cross-language credential member digest changed: got %s want %s", digest, expected)
	}
	goldenBindings := make([]MemberBinding, len(goldenMembers()))
	for index, member := range goldenMembers() {
		goldenBindings[index] = MemberBinding{
			ActorID: member.ActorID, Kind: member.Kind, Slot: member.Slot,
			CredentialHandleHash: sha256Digest([]byte("member-handle/" + member.Slot)),
		}
	}
	goldenDigest, err := MemberBindingsDigest(goldenBindings)
	if err != nil {
		t.Fatal(err)
	}
	const expectedGolden = "sha256:22e80fe83cca008f8f985929832eeee593b04c347c0f235341cdf7401d53e086"
	if goldenDigest != expectedGolden {
		t.Fatalf("exact 11-slot member digest changed: got %s want %s", goldenDigest, expectedGolden)
	}
}

func verifyCredentialEnvelope(t *testing.T, public ed25519.PublicKey, attestation Attestation, predicateType string, binding SetBinding, revocation bool) {
	t.Helper()
	verifier, err := templateauthority.NewDSSEVerifier(templateauthority.DSSETrustPolicy{
		Keys: map[string]templateauthority.TrustedSigner{
			"credential-issuer-ed25519": {Algorithm: templateauthority.AlgorithmEd25519, PublicKey: public, Identity: testIssuer},
		},
		AllowedPayloadTypes: []string{InTotoPayloadType}, AllowedPredicateTypes: []string{predicateType}, MinSignatures: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	name, _ := credentialSubject(binding)
	verified, err := verifier.Verify(attestation.Envelope, templateauthority.ExpectedSubject{Name: name, SHA256Digest: binding.SetHandleHash})
	if err != nil {
		t.Fatalf("qualification-compatible DSSE verification failed: %v", err)
	}
	if verified.PayloadDigest != attestation.PayloadDigest || !bytesEqual(verified.Payload, attestation.Payload) {
		t.Fatal("verified payload did not match the control-plane attestation")
	}
	var statement struct {
		Predicate json.RawMessage `json:"predicate"`
	}
	if err := json.Unmarshal(verified.Payload, &statement); err != nil {
		t.Fatal(err)
	}
	if revocation {
		var value qualificationreceipt.CredentialSetRevocation
		if err := json.Unmarshal(statement.Predicate, &value); err != nil || value.SchemaVersion != qualificationreceipt.CredentialSetRevocationSchemaV1 ||
			value.Status != "revoked" || value.MemberCount != GoldenMemberCount {
			t.Fatalf("qualification revocation predicate compatibility failed: %#v, %v", value, err)
		}
		assertExactPredicateShape(t, statement.Predicate, value)
		return
	}
	var value qualificationreceipt.CredentialSetIssuance
	if err := json.Unmarshal(statement.Predicate, &value); err != nil || value.SchemaVersion != qualificationreceipt.CredentialSetIssuanceSchemaV1 ||
		value.Status != "issued" || value.MemberCount != GoldenMemberCount {
		t.Fatalf("qualification issuance predicate compatibility failed: %#v, %v", value, err)
	}
	assertExactPredicateShape(t, statement.Predicate, value)
}

func assertExactPredicateShape(t *testing.T, encoded json.RawMessage, typed any) {
	t.Helper()
	var actual any
	if err := json.Unmarshal(encoded, &actual); err != nil {
		t.Fatal(err)
	}
	expectedJSON, err := json.Marshal(typed)
	if err != nil {
		t.Fatal(err)
	}
	var expected any
	if err := json.Unmarshal(expectedJSON, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("credential predicate has fields outside the frozen qualificationreceipt schema: actual %#v expected %#v", actual, expected)
	}
}
