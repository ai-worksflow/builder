package repositorygc

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeAuthority struct {
	readinessErr error
	plans        []fakePlanResult
	executes     []fakeExecuteResult
	inspects     []fakeInspectResult
	planInputs   []PlanInput
	capabilities []uuid.UUID
}

type fakePlanResult struct {
	capabilities []Capability
	err          error
}

type fakeExecuteResult struct {
	receipt Receipt
	err     error
}

type fakeInspectResult struct {
	inspection Inspection
	err        error
}

func (authority *fakeAuthority) Readiness(context.Context) error { return authority.readinessErr }
func (authority *fakeAuthority) Plan(_ context.Context, input PlanInput) ([]Capability, error) {
	authority.planInputs = append(authority.planInputs, input)
	result := authority.plans[0]
	authority.plans = authority.plans[1:]
	return result.capabilities, result.err
}
func (authority *fakeAuthority) Execute(_ context.Context, capability uuid.UUID) (Receipt, error) {
	authority.capabilities = append(authority.capabilities, capability)
	result := authority.executes[0]
	authority.executes = authority.executes[1:]
	return result.receipt, result.err
}
func (authority *fakeAuthority) Inspect(context.Context, uuid.UUID) (Inspection, error) {
	result := authority.inspects[0]
	authority.inspects = authority.inspects[1:]
	return result.inspection, result.err
}

func TestPolicyDefaultsAndBounds(t *testing.T) {
	if err := DefaultPolicy().Validate(); err != nil {
		t.Fatalf("default policy invalid: %v", err)
	}
	for _, policy := range []Policy{
		{Retention: MinimumRetention - time.Millisecond, KeepPerProject: 8, BatchSize: 25, CapabilityTTL: time.Minute},
		{Retention: MinimumRetention, KeepPerProject: 7, BatchSize: 25, CapabilityTTL: time.Minute},
		{Retention: MinimumRetention, KeepPerProject: 8, BatchSize: 101, CapabilityTTL: time.Minute},
		{Retention: MinimumRetention, KeepPerProject: 8, BatchSize: 25, CapabilityTTL: 15*time.Minute + time.Millisecond},
	} {
		if !errors.Is(policy.Validate(), ErrInvalidPolicy) {
			t.Fatalf("policy %#v should be invalid", policy)
		}
	}
}

func TestOperatorReturnsPostgresInspectionResult(t *testing.T) {
	runID := uuid.New()
	capabilityID := uuid.New()
	capability := testCapability(runID, capabilityID)
	receipt := testReceipt(runID, capabilityID)
	completed := testInspection(runID, RunStateCompleted, 1, 1)
	completed.Result.LogicalBytesReleased = 4096
	authority := &fakeAuthority{
		plans:    []fakePlanResult{{capabilities: []Capability{capability}}},
		executes: []fakeExecuteResult{{receipt: receipt}},
		inspects: []fakeInspectResult{{inspection: completed}},
	}
	operator, err := New(authority)
	if err != nil {
		t.Fatal(err)
	}
	result, err := operator.Run(context.Background(), runID, DefaultPolicy())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != completed.Result {
		t.Fatalf("result = %#v, want %#v", result, completed.Result)
	}
}

func TestOperatorReconcilesAmbiguousExecuteWithSameCapability(t *testing.T) {
	runID := uuid.New()
	capabilityID := uuid.New()
	authority := &fakeAuthority{
		plans: []fakePlanResult{{capabilities: []Capability{testCapability(runID, capabilityID)}}},
		executes: []fakeExecuteResult{
			{err: errors.New("connection lost after commit")},
			{receipt: testReceipt(runID, capabilityID)},
		},
		inspects: []fakeInspectResult{{inspection: testInspection(runID, RunStateCompleted, 1, 1)}},
	}
	operator, _ := New(authority)
	if _, err := operator.Run(context.Background(), runID, DefaultPolicy()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !reflect.DeepEqual(authority.capabilities, []uuid.UUID{capabilityID, capabilityID}) {
		t.Fatalf("execute capabilities = %v", authority.capabilities)
	}
}

func TestOperatorReconcilesLostIdempotentReplayWithTerminalInspection(t *testing.T) {
	runID := uuid.New()
	capabilityID := uuid.New()
	completed := testInspection(runID, RunStateCompleted, 1, 1)
	authority := &fakeAuthority{
		plans: []fakePlanResult{{capabilities: []Capability{testCapability(runID, capabilityID)}}},
		executes: []fakeExecuteResult{
			{err: errors.New("first response lost")},
			{err: errors.New("replay response lost")},
		},
		inspects: []fakeInspectResult{{inspection: completed}},
	}
	operator, _ := New(authority)
	result, err := operator.Run(context.Background(), runID, DefaultPolicy())
	if err != nil || result != completed.Result {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}

func TestOperatorDoesNotExposeOpaqueCapabilityOnUnresolvedExecution(t *testing.T) {
	runID := uuid.New()
	capabilityID := uuid.New()
	authority := &fakeAuthority{
		plans: []fakePlanResult{{capabilities: []Capability{testCapability(runID, capabilityID)}}},
		executes: []fakeExecuteResult{
			{err: errors.New("execute unavailable")},
			{err: errors.New("execute unavailable")},
		},
		inspects: []fakeInspectResult{{inspection: testInspection(runID, RunStatePlanned, 1, 0)}},
	}
	operator, _ := New(authority)
	_, err := operator.Run(context.Background(), runID, DefaultPolicy())
	if !errors.Is(err, ErrExecutionUnresolved) || strings.Contains(err.Error(), capabilityID.String()) {
		t.Fatalf("Run() error exposed capability or lost type: %v", err)
	}
}

func TestOperatorReconcilesAmbiguousPlanWithExactInput(t *testing.T) {
	runID := uuid.New()
	capabilityID := uuid.New()
	authority := &fakeAuthority{
		plans: []fakePlanResult{
			{err: errors.New("connection lost after plan commit")},
			{capabilities: []Capability{testCapability(runID, capabilityID)}},
		},
		executes: []fakeExecuteResult{{receipt: testReceipt(runID, capabilityID)}},
		inspects: []fakeInspectResult{{inspection: testInspection(runID, RunStateCompleted, 1, 1)}},
	}
	operator, _ := New(authority)
	if _, err := operator.Run(context.Background(), runID, DefaultPolicy()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(authority.planInputs) != 2 || !reflect.DeepEqual(authority.planInputs[0], authority.planInputs[1]) {
		t.Fatalf("plan inputs changed across reconciliation: %#v", authority.planInputs)
	}
}

func TestOperatorResumesInterruptedRunWithStableRunID(t *testing.T) {
	runID := uuid.New()
	firstCapabilityID := uuid.New()
	secondCapabilityID := uuid.New()
	capabilities := []Capability{
		testCapability(runID, firstCapabilityID),
		testCapability(runID, secondCapabilityID),
	}
	partial := testInspection(runID, RunStatePartiallyExecuted, 2, 1)
	completed := testInspection(runID, RunStateCompleted, 2, 2)
	replayedReceipt := testReceipt(runID, firstCapabilityID)
	replayedReceipt.Idempotent = true
	authority := &fakeAuthority{
		plans: []fakePlanResult{
			{capabilities: capabilities},
			{capabilities: capabilities},
		},
		executes: []fakeExecuteResult{
			{receipt: testReceipt(runID, firstCapabilityID)},
			{err: errors.New("intentional interruption")},
			{err: errors.New("intentional interruption")},
			{receipt: replayedReceipt},
			{receipt: testReceipt(runID, secondCapabilityID)},
		},
		inspects: []fakeInspectResult{
			{inspection: partial},
			{inspection: completed},
		},
	}
	operator, _ := New(authority)
	if _, err := operator.Run(context.Background(), runID, DefaultPolicy()); !errors.Is(err, ErrExecutionUnresolved) {
		t.Fatalf("interrupted Run() error = %v", err)
	}
	result, err := operator.Run(context.Background(), runID, DefaultPolicy())
	if err != nil || result != completed.Result {
		t.Fatalf("resumed Run() = %#v, %v", result, err)
	}
	if len(authority.planInputs) != 2 || authority.planInputs[0] != authority.planInputs[1] ||
		authority.planInputs[0].RunID != runID {
		t.Fatalf("resume changed durable plan identity: %#v", authority.planInputs)
	}
}

func TestOperatorAcceptsZeroCapabilityCompletedRun(t *testing.T) {
	runID := uuid.New()
	authority := &fakeAuthority{
		plans:    []fakePlanResult{{capabilities: []Capability{}}},
		inspects: []fakeInspectResult{{inspection: testInspection(runID, RunStateCompleted, 0, 0)}},
	}
	operator, _ := New(authority)
	result, err := operator.Run(context.Background(), runID, DefaultPolicy())
	if err != nil || result.Planned != 0 {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}

func TestOperatorAcceptsDatabaseProtectedTerminalOutcome(t *testing.T) {
	runID := uuid.New()
	capabilityID := uuid.New()
	receipt := testReceipt(runID, capabilityID)
	receipt.Outcome = OutcomeProtected
	completed := testInspection(runID, RunStateCompleted, 1, 0)
	completed.Result.Protected = 1
	authority := &fakeAuthority{
		plans:    []fakePlanResult{{capabilities: []Capability{testCapability(runID, capabilityID)}}},
		executes: []fakeExecuteResult{{receipt: receipt}},
		inspects: []fakeInspectResult{{inspection: completed}},
	}
	operator, _ := New(authority)
	result, err := operator.Run(context.Background(), runID, DefaultPolicy())
	if err != nil || result.Protected != 1 || result.Deleted != 0 {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}

func TestOperatorFailsClosedOnReadinessAndContractDrift(t *testing.T) {
	operator, _ := New(&fakeAuthority{readinessErr: errors.New("wrong database role")})
	if _, err := operator.Run(context.Background(), uuid.New(), DefaultPolicy()); err == nil {
		t.Fatal("Run succeeded without readiness")
	}

	runID := uuid.New()
	bad := testInspection(runID, RunStateCompleted, 1, 1)
	bad.Result.Planned = -1
	authority := &fakeAuthority{
		plans:    []fakePlanResult{{capabilities: []Capability{testCapability(runID, uuid.New())}}},
		executes: []fakeExecuteResult{{receipt: Receipt{ReceiptID: uuid.New(), RunID: runID, CapabilityID: uuid.New()}}},
		inspects: []fakeInspectResult{{inspection: bad}},
	}
	operator, _ = New(authority)
	if _, err := operator.Run(context.Background(), runID, DefaultPolicy()); !errors.Is(err, ErrAuthorityContract) {
		t.Fatalf("Run() error = %v, want contract violation", err)
	}
}

func testCapability(runID, capabilityID uuid.UUID) Capability {
	return Capability{RunID: runID, CapabilityID: capabilityID, ExpiresAt: time.Now().Add(10 * time.Minute)}
}

func testReceipt(runID, capabilityID uuid.UUID) Receipt {
	return Receipt{ReceiptID: uuid.New(), RunID: runID, CapabilityID: capabilityID, Outcome: OutcomeDeleted}
}

func testInspection(runID uuid.UUID, state RunState, planned, deleted int64) Inspection {
	return Inspection{
		RunID: runID, State: state,
		Result: Result{SchemaVersion: ResultSchemaVersion, RunID: runID, Planned: planned, Deleted: deleted},
	}
}
