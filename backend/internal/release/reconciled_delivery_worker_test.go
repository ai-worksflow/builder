package release

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type reconciledClaimResult struct {
	claim *ReconciledDeliveryClaim
	err   error
}

type recordedDeliveryUnknown struct {
	claim   ReconciledDeliveryClaim
	attempt DeliveryOperationAttempt
	code    string
	detail  string
	next    time.Time
}

type recordedDeliveryObservation struct {
	claim       ReconciledDeliveryClaim
	attempt     DeliveryOperationAttempt
	observation DeliveryOperationObservation
	next        time.Time
}

type recordedDeliveryQuarantine struct {
	claim   ReconciledDeliveryClaim
	attempt DeliveryOperationAttempt
	code    string
	detail  string
}

type fakeReconciledDeliveryStore struct {
	mu sync.Mutex

	claimResults []reconciledClaimResult
	claimCalls   int
	renewCalls   int
	attempts     []DeliveryOperationAttempt
	unknowns     []recordedDeliveryUnknown
	notFound     []DeliveryOperationAttempt
	observations []recordedDeliveryObservation
	quarantines  []recordedDeliveryQuarantine
	finalized    []ReconciledDeliveryClaim
	events       []string

	onObservation func()
}

func (store *fakeReconciledDeliveryStore) ClaimDeliveryOperation(
	_ context.Context,
	workerID string,
	_ time.Duration,
) (*ReconciledDeliveryClaim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.claimCalls++
	if len(store.claimResults) == 0 {
		return nil, ErrNoDeliveryWork
	}
	result := store.claimResults[0]
	store.claimResults = store.claimResults[1:]
	if result.claim != nil {
		copy := *result.claim
		if copy.Preview != nil {
			preview := *copy.Preview
			preview.Lease.WorkerID = workerID
			copy.Preview = &preview
		}
		if copy.Production != nil {
			production := *copy.Production
			production.Lease.WorkerID = workerID
			copy.Production = &production
		}
		return &copy, result.err
	}
	return nil, result.err
}

func (store *fakeReconciledDeliveryStore) RenewDeliveryOperation(
	_ context.Context,
	_ ReconciledDeliveryClaim,
	_ time.Duration,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.renewCalls++
	return nil
}

func (store *fakeReconciledDeliveryStore) BeginDeliveryAttempt(
	_ context.Context,
	claim ReconciledDeliveryClaim,
	kind DeliveryAttemptKind,
) (DeliveryOperationAttempt, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	attempt := DeliveryOperationAttempt{
		OperationID: claim.Request.OperationID,
		Ordinal:     uint64(len(store.attempts) + 1),
		Kind:        kind,
		WorkerID:    claim.Lease().WorkerID,
		FenceEpoch:  claim.Lease().Epoch,
	}
	store.attempts = append(store.attempts, attempt)
	store.events = append(store.events, "begin:"+string(kind))
	return attempt, nil
}

func (store *fakeReconciledDeliveryStore) RecordDeliveryObservation(
	_ context.Context,
	claim ReconciledDeliveryClaim,
	attempt DeliveryOperationAttempt,
	observation DeliveryOperationObservation,
	next time.Time,
) error {
	store.mu.Lock()
	store.observations = append(store.observations, recordedDeliveryObservation{
		claim: claim, attempt: attempt, observation: observation, next: next,
	})
	store.events = append(store.events, "observation:"+string(observation.State))
	onObservation := store.onObservation
	store.mu.Unlock()
	if onObservation != nil {
		onObservation()
	}
	return nil
}

func (store *fakeReconciledDeliveryStore) RecordDeliveryUnknown(
	_ context.Context,
	claim ReconciledDeliveryClaim,
	attempt DeliveryOperationAttempt,
	code string,
	detail string,
	next time.Time,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.unknowns = append(store.unknowns, recordedDeliveryUnknown{
		claim: claim, attempt: attempt, code: code, detail: detail, next: next,
	})
	store.events = append(store.events, "unknown")
	return nil
}

func (store *fakeReconciledDeliveryStore) RecordDeliveryNotFound(
	_ context.Context,
	_ ReconciledDeliveryClaim,
	attempt DeliveryOperationAttempt,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.notFound = append(store.notFound, attempt)
	store.events = append(store.events, "not-found")
	return nil
}

func (store *fakeReconciledDeliveryStore) QuarantineDeliveryOperation(
	_ context.Context,
	claim ReconciledDeliveryClaim,
	attempt DeliveryOperationAttempt,
	code string,
	detail string,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.quarantines = append(store.quarantines, recordedDeliveryQuarantine{
		claim: claim, attempt: attempt, code: code, detail: detail,
	})
	store.events = append(store.events, "quarantine:"+code)
	return nil
}

func (store *fakeReconciledDeliveryStore) FinalizeDeliveryOperation(
	_ context.Context,
	claim ReconciledDeliveryClaim,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.finalized = append(store.finalized, claim)
	store.events = append(store.events, "finalize")
	return nil
}

type fakeDeliveryOperationProvider struct {
	mu sync.Mutex

	submitRequests    []DeliveryOperationRequest
	reconcileRequests []DeliveryOperationRequest
	submit            func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error)
	reconcile         func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error)
}

func (provider *fakeDeliveryOperationProvider) Submit(
	ctx context.Context,
	request DeliveryOperationRequest,
) (DeliveryOperationObservation, error) {
	provider.mu.Lock()
	provider.submitRequests = append(provider.submitRequests, cloneDeliveryOperationRequest(request))
	call := provider.submit
	provider.mu.Unlock()
	if call == nil {
		return DeliveryOperationObservation{}, errors.New("unexpected Submit")
	}
	return call(ctx, request)
}

func (provider *fakeDeliveryOperationProvider) Reconcile(
	ctx context.Context,
	request DeliveryOperationRequest,
) (DeliveryOperationObservation, error) {
	provider.mu.Lock()
	provider.reconcileRequests = append(provider.reconcileRequests, cloneDeliveryOperationRequest(request))
	call := provider.reconcile
	provider.mu.Unlock()
	if call == nil {
		return DeliveryOperationObservation{}, errors.New("unexpected Reconcile")
	}
	return call(ctx, request)
}

func (*fakeDeliveryOperationProvider) Readiness(context.Context) error { return nil }

func cloneDeliveryOperationRequest(request DeliveryOperationRequest) DeliveryOperationRequest {
	request.RequestDocument = append([]byte(nil), request.RequestDocument...)
	return request
}

func reconciledWorkerClaim(t *testing.T, state DeliveryRunState, remoteState string) ReconciledDeliveryClaim {
	t.Helper()
	return ReconciledDeliveryClaim{
		Request:     deliveryOperationRequestFixture(t, DeliveryOperationPreview),
		Controller:  deliveryControllerIdentityFixture(),
		RemoteState: remoteState,
		RunState:    state,
		Preview: &PreviewClaim{Lease: DeliveryLease{
			WorkerID: "worker-a",
			Epoch:    7,
		}},
	}
}

func reconciledWorkerObservation(
	t *testing.T,
	request DeliveryOperationRequest,
	state DeliveryRemoteState,
) DeliveryOperationObservation {
	t.Helper()
	observation := DeliveryOperationObservation{
		SchemaVersion: DeliveryOperationObservationSchema,
		Controller:    deliveryControllerIdentityFixture(),
		OperationID:   request.OperationID,
		RequestHash:   request.RequestHash,
		State:         state,
		Sequence:      9,
		ObservedAt:    time.Date(2026, 7, 18, 10, 12, 0, 123456000, time.UTC),
	}
	if state == DeliveryRemoteCompleted {
		result := completedDeliveryOperationResultFixture(t, request)
		observation.Result = &result
	}
	return observation
}

func newReconciledWorkerForTest(
	t *testing.T,
	store ReconciledDeliveryWorkerStore,
	provider DeliveryOperationProvider,
) *ReconciledDeliveryWorker {
	t.Helper()
	worker, err := NewReconciledDeliveryWorker(store, provider, "worker-a", 5*time.Second, 7*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	worker.now = func() time.Time {
		return time.Date(2026, 7, 18, 10, 20, 30, 987654000, time.UTC)
	}
	return worker
}

func TestDeliveryClaimOrderAlternatesAndPrioritizesProductionFirst(t *testing.T) {
	want := [][2]DeliveryOperationKind{
		{DeliveryOperationProduction, DeliveryOperationPreview},
		{DeliveryOperationPreview, DeliveryOperationProduction},
		{DeliveryOperationProduction, DeliveryOperationPreview},
		{DeliveryOperationPreview, DeliveryOperationProduction},
	}
	for index, expected := range want {
		if actual := deliveryClaimOrder(uint64(index + 1)); actual != expected {
			t.Fatalf("claim sequence %d order=%v, want %v", index+1, actual, expected)
		}
	}
}

func assertExactDeliveryRequest(t *testing.T, got, want DeliveryOperationRequest) {
	t.Helper()
	if got.SchemaVersion != want.SchemaVersion || got.OperationID != want.OperationID ||
		got.Kind != want.Kind || got.ProjectID != want.ProjectID || got.RequestHash != want.RequestHash ||
		!bytes.Equal(got.RequestDocument, want.RequestDocument) {
		t.Fatalf("delivery request changed: got=%+v want=%+v", got, want)
	}
}

func TestReconciledDeliveryWorkerPersistsUnknownSubmitOutcomeWithoutTerminalFailure(t *testing.T) {
	for name, providerErr := range map[string]error{
		"timeout": errors.Join(ErrDeliveryOutcomeUnknown, context.DeadlineExceeded),
		"EOF":     errors.Join(ErrDeliveryOutcomeUnknown, io.EOF),
	} {
		t.Run(name, func(t *testing.T) {
			claim := reconciledWorkerClaim(t, DeliveryClaimed, "prepared")
			store := &fakeReconciledDeliveryStore{claimResults: []reconciledClaimResult{{claim: &claim}}}
			provider := &fakeDeliveryOperationProvider{
				submit: func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error) {
					return DeliveryOperationObservation{}, providerErr
				},
			}
			worker := newReconciledWorkerForTest(t, store, provider)

			worked, err := worker.RunOne(context.Background())
			if err != nil || !worked {
				t.Fatalf("RunOne worked=%v err=%v", worked, err)
			}
			if len(provider.submitRequests) != 1 || len(provider.reconcileRequests) != 0 {
				t.Fatalf("provider calls submit=%d reconcile=%d", len(provider.submitRequests), len(provider.reconcileRequests))
			}
			assertExactDeliveryRequest(t, provider.submitRequests[0], claim.Request)
			if len(store.attempts) != 1 || store.attempts[0].Kind != DeliveryAttemptSubmit ||
				store.attempts[0].OperationID != claim.Request.OperationID {
				t.Fatalf("attempts=%+v", store.attempts)
			}
			if len(store.unknowns) != 1 || store.unknowns[0].code != "controller-outcome-unknown" ||
				store.unknowns[0].attempt.Kind != DeliveryAttemptSubmit {
				t.Fatalf("unknown records=%+v", store.unknowns)
			}
			if got, want := store.unknowns[0].next, worker.now().UTC().Add(7*time.Second).Truncate(time.Microsecond); !got.Equal(want) {
				t.Fatalf("unknown next=%s want=%s", got, want)
			}
			if len(store.observations) != 0 || len(store.quarantines) != 0 || len(store.finalized) != 0 {
				t.Fatalf("ambiguous submit reached terminal path: observations=%d quarantine=%d finalized=%d",
					len(store.observations), len(store.quarantines), len(store.finalized))
			}
		})
	}
}

func TestReconciledDeliveryWorkerReclaimsCompletedOperationWithGETOnly(t *testing.T) {
	claim := reconciledWorkerClaim(t, DeliveryReconciling, "accepted")
	completed := reconciledWorkerObservation(t, claim.Request, DeliveryRemoteCompleted)
	store := &fakeReconciledDeliveryStore{claimResults: []reconciledClaimResult{{claim: &claim}}}
	provider := &fakeDeliveryOperationProvider{
		reconcile: func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error) {
			return completed, nil
		},
	}
	worker := newReconciledWorkerForTest(t, store, provider)

	worked, err := worker.RunOne(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOne worked=%v err=%v", worked, err)
	}
	if len(provider.submitRequests) != 0 || len(provider.reconcileRequests) != 1 {
		t.Fatalf("reclaimed provider calls submit=%d reconcile=%d", len(provider.submitRequests), len(provider.reconcileRequests))
	}
	assertExactDeliveryRequest(t, provider.reconcileRequests[0], claim.Request)
	if len(store.observations) != 1 || len(store.finalized) != 1 {
		t.Fatalf("observation/finalization counts=%d/%d", len(store.observations), len(store.finalized))
	}
	wantEvents := []string{"begin:reconcile", "observation:completed", "finalize"}
	if !equalStrings(store.events, wantEvents) {
		t.Fatalf("events=%v want=%v", store.events, wantEvents)
	}
}

func TestReconciledDeliveryWorkerResubmits404OnlyForUnacknowledgedExactOperation(t *testing.T) {
	for _, remoteState := range []string{"prepared", "submit_unknown"} {
		t.Run(remoteState, func(t *testing.T) {
			claim := reconciledWorkerClaim(t, DeliveryReconciling, remoteState)
			accepted := reconciledWorkerObservation(t, claim.Request, DeliveryRemoteAccepted)
			store := &fakeReconciledDeliveryStore{claimResults: []reconciledClaimResult{{claim: &claim}}}
			provider := &fakeDeliveryOperationProvider{
				reconcile: func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error) {
					return DeliveryOperationObservation{}, ErrDeliveryOperationNotFound
				},
				submit: func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error) {
					return accepted, nil
				},
			}
			worker := newReconciledWorkerForTest(t, store, provider)

			worked, err := worker.RunOne(context.Background())
			if err != nil || !worked {
				t.Fatalf("RunOne worked=%v err=%v", worked, err)
			}
			if len(provider.reconcileRequests) != 1 || len(provider.submitRequests) != 1 {
				t.Fatalf("provider calls reconcile=%d submit=%d", len(provider.reconcileRequests), len(provider.submitRequests))
			}
			assertExactDeliveryRequest(t, provider.reconcileRequests[0], claim.Request)
			assertExactDeliveryRequest(t, provider.submitRequests[0], claim.Request)
			if len(store.notFound) != 1 || len(store.attempts) != 2 ||
				store.attempts[0].Kind != DeliveryAttemptReconcile || store.attempts[1].Kind != DeliveryAttemptResubmit ||
				store.attempts[0].OperationID != store.attempts[1].OperationID {
				t.Fatalf("not-found=%+v attempts=%+v", store.notFound, store.attempts)
			}
			if len(store.observations) != 1 || store.observations[0].attempt.Kind != DeliveryAttemptResubmit ||
				len(store.unknowns) != 0 || len(store.quarantines) != 0 {
				t.Fatalf("post-resubmit state observations=%+v unknown=%+v quarantine=%+v",
					store.observations, store.unknowns, store.quarantines)
			}
		})
	}
}

func TestReconciledDeliveryWorkerQuarantines404AfterAcknowledgementWithoutResubmit(t *testing.T) {
	claim := reconciledWorkerClaim(t, DeliveryReconciling, "accepted")
	store := &fakeReconciledDeliveryStore{claimResults: []reconciledClaimResult{{claim: &claim}}}
	provider := &fakeDeliveryOperationProvider{
		reconcile: func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error) {
			return DeliveryOperationObservation{}, ErrDeliveryOperationNotFound
		},
	}
	worker := newReconciledWorkerForTest(t, store, provider)

	worked, err := worker.RunOne(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOne worked=%v err=%v", worked, err)
	}
	if len(provider.reconcileRequests) != 1 || len(provider.submitRequests) != 0 || len(store.notFound) != 0 {
		t.Fatalf("provider calls reconcile=%d submit=%d notFound=%d",
			len(provider.reconcileRequests), len(provider.submitRequests), len(store.notFound))
	}
	if len(store.quarantines) != 1 || store.quarantines[0].code != "controller-history-lost" ||
		len(store.unknowns) != 0 || len(store.finalized) != 0 {
		t.Fatalf("quarantine=%+v unknown=%+v finalized=%+v", store.quarantines, store.unknowns, store.finalized)
	}
}

func TestReconciledDeliveryWorkerNeverResubmitsAfterOperatorReconciliation(t *testing.T) {
	claim := reconciledWorkerClaim(t, DeliveryReconciling, "submit_unknown")
	claim.ReconcileOnly = true
	store := &fakeReconciledDeliveryStore{claimResults: []reconciledClaimResult{{claim: &claim}}}
	provider := &fakeDeliveryOperationProvider{
		reconcile: func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error) {
			return DeliveryOperationObservation{}, ErrDeliveryOperationNotFound
		},
		submit: func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error) {
			t.Fatal("operator-reconciled Operation must never be PUT again")
			return DeliveryOperationObservation{}, nil
		},
	}
	worker := newReconciledWorkerForTest(t, store, provider)

	worked, err := worker.RunOne(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOne worked=%v err=%v", worked, err)
	}
	if len(provider.reconcileRequests) != 1 || len(provider.submitRequests) != 0 || len(store.notFound) != 0 {
		t.Fatalf("provider calls reconcile=%d submit=%d notFound=%d",
			len(provider.reconcileRequests), len(provider.submitRequests), len(store.notFound))
	}
	if len(store.quarantines) != 1 ||
		store.quarantines[0].code != "controller-history-still-unavailable" {
		t.Fatalf("quarantine=%+v", store.quarantines)
	}
}

func TestReconciledDeliveryWorkerQuarantinesControllerConflictAndTrustFailure(t *testing.T) {
	for name, providerErr := range map[string]error{
		"request conflict": ErrDeliveryOperationConflict,
		"protocol drift":   ErrDeliveryControllerProtocol,
		"SPKI trust":       ErrDeliveryControllerTrust,
	} {
		t.Run(name, func(t *testing.T) {
			claim := reconciledWorkerClaim(t, DeliveryClaimed, "prepared")
			store := &fakeReconciledDeliveryStore{claimResults: []reconciledClaimResult{{claim: &claim}}}
			provider := &fakeDeliveryOperationProvider{
				submit: func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error) {
					return DeliveryOperationObservation{}, providerErr
				},
			}
			worker := newReconciledWorkerForTest(t, store, provider)

			worked, err := worker.RunOne(context.Background())
			if err != nil || !worked {
				t.Fatalf("RunOne worked=%v err=%v", worked, err)
			}
			if len(store.quarantines) != 1 || store.quarantines[0].code != "controller-authority-conflict" ||
				len(store.unknowns) != 0 || len(store.observations) != 0 || len(store.finalized) != 0 {
				t.Fatalf("quarantine=%+v unknown=%+v observations=%+v finalized=%+v",
					store.quarantines, store.unknowns, store.observations, store.finalized)
			}
		})
	}
}

func TestReconciledDeliveryWorkerSchedulesAcknowledgedOperationReconciliation(t *testing.T) {
	for _, state := range []DeliveryRemoteState{DeliveryRemoteAccepted, DeliveryRemoteRunning} {
		t.Run(string(state), func(t *testing.T) {
			runState := DeliveryClaimed
			remoteState := "prepared"
			provider := &fakeDeliveryOperationProvider{}
			claim := reconciledWorkerClaim(t, runState, remoteState)
			observation := reconciledWorkerObservation(t, claim.Request, state)
			if state == DeliveryRemoteRunning {
				claim.RunState = DeliveryReconciling
				claim.RemoteState = "accepted"
				provider.reconcile = func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error) {
					return observation, nil
				}
			} else {
				provider.submit = func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error) {
					return observation, nil
				}
			}
			store := &fakeReconciledDeliveryStore{claimResults: []reconciledClaimResult{{claim: &claim}}}
			worker := newReconciledWorkerForTest(t, store, provider)

			worked, err := worker.RunOne(context.Background())
			if err != nil || !worked {
				t.Fatalf("RunOne worked=%v err=%v", worked, err)
			}
			if len(store.observations) != 1 || store.observations[0].observation.State != state {
				t.Fatalf("observations=%+v", store.observations)
			}
			if got, want := store.observations[0].next, worker.now().UTC().Add(7*time.Second).Truncate(time.Microsecond); !got.Equal(want) {
				t.Fatalf("next reconciliation=%s want=%s", got, want)
			}
			if len(store.finalized) != 0 || len(store.unknowns) != 0 || len(store.quarantines) != 0 {
				t.Fatalf("acknowledged operation terminated: finalized=%d unknown=%d quarantine=%d",
					len(store.finalized), len(store.unknowns), len(store.quarantines))
			}
		})
	}
}

func TestReconciledDeliveryWorkerVerifyingCrashRecoveryOnlyFinalizes(t *testing.T) {
	claim := reconciledWorkerClaim(t, DeliveryVerifying, "completed")
	store := &fakeReconciledDeliveryStore{claimResults: []reconciledClaimResult{{claim: &claim}}}
	provider := &fakeDeliveryOperationProvider{}
	worker := newReconciledWorkerForTest(t, store, provider)

	worked, err := worker.RunOne(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOne worked=%v err=%v", worked, err)
	}
	if len(store.finalized) != 1 || len(store.attempts) != 0 || len(store.observations) != 0 ||
		len(provider.submitRequests) != 0 || len(provider.reconcileRequests) != 0 {
		t.Fatalf("verifying recovery finalized=%d attempts=%d observations=%d submit=%d reconcile=%d",
			len(store.finalized), len(store.attempts), len(store.observations),
			len(provider.submitRequests), len(provider.reconcileRequests))
	}
}

func TestReconciledDeliveryWorkerServiceContinuesAfterOneTaskError(t *testing.T) {
	claim := reconciledWorkerClaim(t, DeliveryClaimed, "prepared")
	store := &fakeReconciledDeliveryStore{claimResults: []reconciledClaimResult{
		{err: errors.New("temporary database error")},
		{claim: &claim},
	}}
	processed := make(chan struct{})
	var once sync.Once
	store.onObservation = func() { once.Do(func() { close(processed) }) }
	provider := &fakeDeliveryOperationProvider{
		submit: func(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error) {
			return reconciledWorkerObservation(t, claim.Request, DeliveryRemoteAccepted), nil
		},
	}
	worker := newReconciledWorkerForTest(t, store, provider)
	service, err := NewReconciledDeliveryWorkerService(worker, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()

	select {
	case <-processed:
		cancel()
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("service stopped after the first task error")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("service Run error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("service did not stop after cancellation")
	}
	if store.claimCalls < 2 || len(store.observations) != 1 || len(provider.submitRequests) != 1 {
		t.Fatalf("claimCalls=%d observations=%d submit=%d", store.claimCalls, len(store.observations), len(provider.submitRequests))
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

var _ ReconciledDeliveryWorkerStore = (*fakeReconciledDeliveryStore)(nil)
var _ DeliveryOperationProvider = (*fakeDeliveryOperationProvider)(nil)
