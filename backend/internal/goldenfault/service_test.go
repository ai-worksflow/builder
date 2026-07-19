package goldenfault

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestServiceReservesBeforeExecuteAndReplaysCommittedResult(t *testing.T) {
	fixture := newSignedFaultFixture(t)
	ledger := newMemoryFaultLedger(fixture.now)
	adapter := &recordingFaultAdapter{
		resolution: ResourceResolution{
			ResourceID: "agent-runner/session-42", HeadDigest: testFaultDigest("head-before"),
			FenceDigest: fixture.expected.ExpectedFenceDigest,
		},
		result: AdapterResult{
			Outcome: AdapterOutcomeApplied, ResultDigest: testFaultDigest("adapter-result"),
			ObservedHeadDigest: testFaultDigest("head-after"), ObservedFenceDigest: testFaultDigest("fence-after"),
		},
		ledger: ledger,
	}
	service := testFaultService(t, fixture, ledger, map[OperationKind]Adapter{fixture.expected.OperationKind: adapter})

	first, err := service.Consume(context.Background(), fixture.envelope, fixture.expected)
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if first.State != ConsumeStateTerminal || first.Terminal == nil || first.Idempotent {
		t.Fatalf("first consume record = %+v", first)
	}
	second, err := service.Consume(context.Background(), fixture.envelope, fixture.expected)
	if err != nil {
		t.Fatalf("replayed Consume() error = %v", err)
	}
	if !second.Idempotent || second.Terminal == nil ||
		second.Terminal.ReceiptDigest != first.Terminal.ReceiptDigest ||
		second.Terminal.ResultID != first.Terminal.ResultID {
		t.Fatalf("replayed consume record = %+v, first = %+v", second, first)
	}
	if adapter.executeCalls.Load() != 1 {
		t.Fatalf("adapter Execute calls = %d, want 1", adapter.executeCalls.Load())
	}
	if adapter.resolveCalls.Load() != 1 {
		t.Fatalf("adapter Resolve calls = %d, want 1 after terminal read shortcut", adapter.resolveCalls.Load())
	}
}

func TestServiceUnknownOutcomeIsQueryableAndNeverReexecuted(t *testing.T) {
	fixture := newSignedFaultFixture(t)
	ledger := newMemoryFaultLedger(fixture.now)
	adapter := &recordingFaultAdapter{
		resolution: ResourceResolution{
			ResourceID: "agent-runner/session-unknown", HeadDigest: testFaultDigest("head-before"),
			FenceDigest: fixture.expected.ExpectedFenceDigest,
		},
		executeErr: errors.New("connection closed after request write"), ledger: ledger,
	}
	service := testFaultService(t, fixture, ledger, map[OperationKind]Adapter{fixture.expected.OperationKind: adapter})

	first, err := service.Consume(context.Background(), fixture.envelope, fixture.expected)
	if !errors.Is(err, ErrOutcomeUnknown) || first.State != ConsumeStateReserved {
		t.Fatalf("first Consume() = record:%+v error:%v", first, err)
	}
	second, err := service.Consume(context.Background(), fixture.envelope, fixture.expected)
	if !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, ErrConflict) || second.State != ConsumeStateReserved {
		t.Fatalf("second Consume() = record:%+v error:%v", second, err)
	}
	query := AuthorityQuery{
		AuthorityID: fixture.expected.AuthorityID, FixtureID: fixture.expected.FixtureID, RunID: fixture.expected.RunID,
		EnvelopeDigest: fixture.expected.EnvelopeDigest, PayloadDigest: fixture.expected.PayloadDigest,
	}
	inspected, err := service.Inspect(context.Background(), query)
	if !errors.Is(err, ErrOutcomeUnknown) || inspected.State != ConsumeStateReserved {
		t.Fatalf("Inspect() = record:%+v error:%v", inspected, err)
	}
	query.PayloadDigest = testFaultDigest("substituted")
	if _, err := service.Inspect(context.Background(), query); !errors.Is(err, ErrConflict) {
		t.Fatalf("Inspect() substituted authority error = %v", err)
	}
	if adapter.executeCalls.Load() != 1 {
		t.Fatalf("adapter Execute calls = %d, want 1", adapter.executeCalls.Load())
	}
}

func TestServiceClosedRegistryAndFenceFailBeforeReservation(t *testing.T) {
	fixture := newSignedFaultFixture(t)
	ledger := newMemoryFaultLedger(fixture.now)
	service := testFaultService(t, fixture, ledger, nil)
	if _, err := service.Consume(context.Background(), fixture.envelope, fixture.expected); !errors.Is(err, ErrAdapterMissing) {
		t.Fatalf("missing adapter error = %v", err)
	}
	if ledger.reservationCount() != 0 {
		t.Fatal("missing adapter created a reservation")
	}

	adapter := &recordingFaultAdapter{resolution: ResourceResolution{
		ResourceID: "agent-runner/session-42", HeadDigest: testFaultDigest("head"),
		FenceDigest: testFaultDigest("wrong-fence"),
	}}
	service = testFaultService(t, fixture, ledger, map[OperationKind]Adapter{fixture.expected.OperationKind: adapter})
	if _, err := service.Consume(context.Background(), fixture.envelope, fixture.expected); !errors.Is(err, ErrConflict) {
		t.Fatalf("fence mismatch error = %v", err)
	}
	if ledger.reservationCount() != 0 || adapter.executeCalls.Load() != 0 {
		t.Fatal("fence mismatch reserved or executed a fault")
	}

	if _, err := NewService(testFaultVerifier(t, fixture.key), ledger, map[OperationKind]Adapter{
		OperationKind("arbitrary-exec"): adapter,
	}); !errors.Is(err, ErrAdapterMissing) {
		t.Fatalf("unknown registry operation error = %v", err)
	}
}

func TestServiceConcurrentConsumeExecutesAdapterAtMostOnce(t *testing.T) {
	fixture := newSignedFaultFixture(t)
	ledger := newMemoryFaultLedger(fixture.now)
	adapter := &recordingFaultAdapter{
		resolution: ResourceResolution{
			ResourceID: "agent-runner/concurrent", HeadDigest: testFaultDigest("head-before"),
			FenceDigest: fixture.expected.ExpectedFenceDigest,
		},
		result: AdapterResult{
			Outcome: AdapterOutcomeApplied, ResultDigest: testFaultDigest("result"),
			ObservedHeadDigest: testFaultDigest("head-after"), ObservedFenceDigest: testFaultDigest("fence-after"),
		},
		ledger: ledger,
	}
	service := testFaultService(t, fixture, ledger, map[OperationKind]Adapter{fixture.expected.OperationKind: adapter})

	const callers = 24
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := 0; index < callers; index++ {
		go func() {
			defer wait.Done()
			<-start
			_, _ = service.Consume(context.Background(), fixture.envelope, fixture.expected)
		}()
	}
	close(start)
	wait.Wait()
	if adapter.executeCalls.Load() != 1 {
		t.Fatalf("concurrent adapter Execute calls = %d, want 1", adapter.executeCalls.Load())
	}
	replayed, err := service.Consume(context.Background(), fixture.envelope, fixture.expected)
	if err != nil || replayed.State != ConsumeStateTerminal || !replayed.Idempotent {
		t.Fatalf("post-concurrency replay = record:%+v error:%v", replayed, err)
	}
}

func TestServiceRejectsTerminalReceiptStorageDrift(t *testing.T) {
	fixture := newSignedFaultFixture(t)
	ledger := newMemoryFaultLedger(fixture.now)
	adapter := &recordingFaultAdapter{
		resolution: ResourceResolution{
			ResourceID: "agent-runner/tamper", HeadDigest: testFaultDigest("head-before"),
			FenceDigest: fixture.expected.ExpectedFenceDigest,
		},
		result: AdapterResult{
			Outcome: AdapterOutcomeApplied, ResultDigest: testFaultDigest("result"),
			ObservedHeadDigest: testFaultDigest("head-after"), ObservedFenceDigest: testFaultDigest("fence-after"),
		},
		ledger: ledger,
	}
	service := testFaultService(t, fixture, ledger, map[OperationKind]Adapter{fixture.expected.OperationKind: adapter})
	if _, err := service.Consume(context.Background(), fixture.envelope, fixture.expected); err != nil {
		t.Fatal(err)
	}
	ledger.mu.Lock()
	stored := ledger.records[fixture.expected.AuthorityID]
	stored.Terminal.Receipt.ResourceSelector = "release.controller"
	ledger.records[fixture.expected.AuthorityID] = stored
	ledger.mu.Unlock()

	query := AuthorityQuery{
		AuthorityID: fixture.expected.AuthorityID, FixtureID: fixture.expected.FixtureID, RunID: fixture.expected.RunID,
		EnvelopeDigest: fixture.expected.EnvelopeDigest, PayloadDigest: fixture.expected.PayloadDigest,
	}
	if _, err := service.Inspect(context.Background(), query); !errors.Is(err, ErrConflict) {
		t.Fatalf("Inspect() storage drift error = %v", err)
	}
}

type recordingFaultAdapter struct {
	resolution   ResourceResolution
	result       AdapterResult
	resolveErr   error
	executeErr   error
	ledger       *memoryFaultLedger
	resolveCalls atomic.Int64
	executeCalls atomic.Int64
}

func (adapter *recordingFaultAdapter) Resolve(context.Context, VerifiedAuthority) (ResourceResolution, error) {
	adapter.resolveCalls.Add(1)
	return adapter.resolution, adapter.resolveErr
}

func (adapter *recordingFaultAdapter) Execute(_ context.Context, request AdapterRequest) (AdapterResult, error) {
	adapter.executeCalls.Add(1)
	if adapter.ledger != nil && !adapter.ledger.hasReservation(request.Authority.Predicate.AuthorityID, request.AdapterInvocationID) {
		return AdapterResult{}, errors.New("adapter was called before durable reservation")
	}
	return adapter.result, adapter.executeErr
}

type memoryFaultLedger struct {
	mu      sync.Mutex
	now     time.Time
	records map[uuid.UUID]ConsumeRecord
}

func newMemoryFaultLedger(now time.Time) *memoryFaultLedger {
	return &memoryFaultLedger{now: now, records: map[uuid.UUID]ConsumeRecord{}}
}

func (ledger *memoryFaultLedger) TrustedTime(context.Context) (time.Time, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.now, nil
}

func (ledger *memoryFaultLedger) Reserve(_ context.Context, reservation Reservation) (ConsumeRecord, error) {
	if err := validateReservation(reservation); err != nil {
		return ConsumeRecord{}, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if existing, exists := ledger.records[reservation.AuthorityID]; exists {
		existing = cloneConsumeRecord(existing)
		existing.Idempotent = true
		return existing, nil
	}
	record := ConsumeRecord{State: ConsumeStateReserved, Reservation: cloneReservation(reservation)}
	ledger.records[reservation.AuthorityID] = record
	return cloneConsumeRecord(record), nil
}

func (ledger *memoryFaultLedger) CommitTerminal(_ context.Context, terminal TerminalResult) (ConsumeRecord, error) {
	if err := validateTerminalResult(terminal); err != nil {
		return ConsumeRecord{}, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	record, exists := ledger.records[terminal.AuthorityID]
	if !exists {
		return ConsumeRecord{}, ErrNotFound
	}
	if record.Terminal != nil {
		if !terminalResultsEqual(*record.Terminal, terminal) {
			return ConsumeRecord{}, ErrConflict
		}
		record.Idempotent = true
		return cloneConsumeRecord(record), nil
	}
	copy := cloneTerminalResult(terminal)
	record.State = ConsumeStateTerminal
	record.Terminal = &copy
	ledger.records[terminal.AuthorityID] = record
	return cloneConsumeRecord(record), nil
}

func (ledger *memoryFaultLedger) Inspect(_ context.Context, authorityID uuid.UUID) (ConsumeRecord, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	record, exists := ledger.records[authorityID]
	if !exists {
		return ConsumeRecord{}, ErrNotFound
	}
	return cloneConsumeRecord(record), nil
}

func (ledger *memoryFaultLedger) hasReservation(authorityID string, invocationID uuid.UUID) bool {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	record, exists := ledger.records[uuid.MustParse(authorityID)]
	return exists && record.Reservation.AdapterInvocationID == invocationID
}

func (ledger *memoryFaultLedger) reservationCount() int {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return len(ledger.records)
}

func cloneConsumeRecord(input ConsumeRecord) ConsumeRecord {
	result := input
	result.Reservation = cloneReservation(input.Reservation)
	if input.Terminal != nil {
		terminal := cloneTerminalResult(*input.Terminal)
		result.Terminal = &terminal
	}
	return result
}

func cloneReservation(input Reservation) Reservation {
	result := input
	result.SignerIdentities = append([]string(nil), input.SignerIdentities...)
	return result
}

func cloneTerminalResult(input TerminalResult) TerminalResult {
	result := input
	result.ReceiptJSON = bytes.Clone(input.ReceiptJSON)
	return result
}

func testFaultService(
	t *testing.T,
	fixture signedFaultFixture,
	ledger Ledger,
	adapters map[OperationKind]Adapter,
) *Service {
	t.Helper()
	service, err := NewService(testFaultVerifier(t, fixture.key), ledger, adapters)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

var _ Ledger = (*memoryFaultLedger)(nil)
var _ Adapter = (*recordingFaultAdapter)(nil)
var _ = time.Second
