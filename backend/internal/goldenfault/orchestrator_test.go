package goldenfault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestConsumerLoadsTrustedAuthorityAndReturnsCanonicalIdempotentReceipt(t *testing.T) {
	rig := newConsumerRig(t, OperationAgentRunnerCrash, AdapterOutcomeApplied, nil)

	first, err := rig.consumer.Consume(context.Background(), rig.principal, rig.command)
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	firstReceipt, err := CanonicalConsumeReceipt(first)
	if err != nil {
		t.Fatalf("CanonicalConsumeReceipt() error = %v", err)
	}
	var decoded ConsumeReceipt
	if err := json.Unmarshal(firstReceipt, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != ReceiptSchemaV1 || decoded.AuthorityID != rig.command.AuthorityID ||
		decoded.RunID != rig.command.RunID || decoded.FixtureID != rig.command.FixtureID {
		t.Fatalf("receipt = %+v", decoded)
	}

	second, err := rig.consumer.Consume(context.Background(), rig.principal, rig.command)
	if err != nil {
		t.Fatalf("replayed Consume() error = %v", err)
	}
	secondReceipt, err := CanonicalConsumeReceipt(second)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Idempotent || !bytes.Equal(firstReceipt, secondReceipt) || rig.adapter.executeCalls.Load() != 1 ||
		rig.adapter.resolveCalls.Load() != 1 {
		t.Fatalf("replay=%t execute=%d resolve=%d", second.Idempotent, rig.adapter.executeCalls.Load(), rig.adapter.resolveCalls.Load())
	}
}

func TestConsumerRejectsCredentialAndCommandScopeDrift(t *testing.T) {
	tests := []struct {
		name          string
		expectedLoads int64
		mutate        func(*RunPrincipal, *ConsumeCommand)
	}{
		{name: "owner role", mutate: func(principal *RunPrincipal, _ *ConsumeCommand) { principal.Role = "owner" }},
		{name: "admin role", mutate: func(principal *RunPrincipal, _ *ConsumeCommand) { principal.Role = "admin" }},
		{name: "user role", mutate: func(principal *RunPrincipal, _ *ConsumeCommand) { principal.Role = "user" }},
		{name: "actor", expectedLoads: 1, mutate: func(principal *RunPrincipal, _ *ConsumeCommand) { principal.ActorID = uuid.New() }},
		{name: "tenant", expectedLoads: 1, mutate: func(principal *RunPrincipal, _ *ConsumeCommand) { principal.TenantID = uuid.New() }},
		{name: "project", expectedLoads: 1, mutate: func(principal *RunPrincipal, _ *ConsumeCommand) { principal.ProjectID = uuid.New() }},
		{name: "audience", expectedLoads: 1, mutate: func(principal *RunPrincipal, _ *ConsumeCommand) { principal.Audience = "urn:worksflow:other-stack" }},
		{name: "run claim", mutate: func(principal *RunPrincipal, _ *ConsumeCommand) { principal.RunID = uuid.New() }},
		{name: "fixture claim", mutate: func(principal *RunPrincipal, _ *ConsumeCommand) { principal.FixtureID = uuid.New() }},
		{name: "run body and claim", expectedLoads: 1, mutate: func(principal *RunPrincipal, command *ConsumeCommand) {
			principal.RunID = uuid.New()
			command.RunID = principal.RunID
		}},
		{name: "fixture body and claim", expectedLoads: 1, mutate: func(principal *RunPrincipal, command *ConsumeCommand) {
			principal.FixtureID = uuid.New()
			command.FixtureID = principal.FixtureID
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rig := newConsumerRig(t, OperationAgentRunnerCrash, AdapterOutcomeApplied, nil)
			principal, command := rig.principal, rig.command
			test.mutate(&principal, &command)
			if _, err := rig.consumer.Consume(context.Background(), principal, command); !errors.Is(err, ErrFaultCredentialForbidden) {
				t.Fatalf("Consume() error = %v", err)
			}
			if rig.repository.calls.Load() != test.expectedLoads || rig.ledgerBoundary.calls.Load() != 0 ||
				rig.adapter.resolveCalls.Load() != 0 || rig.adapter.executeCalls.Load() != 0 || rig.ledger.reservationCount() != 0 {
				t.Fatalf("loads=%d ledger calls=%d resolve=%d execute=%d reservations=%d",
					rig.repository.calls.Load(), rig.ledgerBoundary.calls.Load(), rig.adapter.resolveCalls.Load(),
					rig.adapter.executeCalls.Load(), rig.ledger.reservationCount())
			}
		})
	}
}

func TestConsumerRejectsTypedNilContextBeforeDependencies(t *testing.T) {
	rig := newConsumerRig(t, OperationAgentRunnerCrash, AdapterOutcomeApplied, nil)
	var ctx *typedNilFaultContext
	if _, err := rig.consumer.Consume(ctx, rig.principal, rig.command); err == nil {
		t.Fatal("Consume() accepted typed-nil context")
	}
	if rig.repository.calls.Load() != 0 || rig.ledgerBoundary.calls.Load() != 0 ||
		rig.adapter.resolveCalls.Load() != 0 || rig.adapter.executeCalls.Load() != 0 {
		t.Fatal("typed-nil context reached a dependency")
	}
}

func TestConsumerUnknownAuthorityAndRepositoryFailureAreNonLeaking(t *testing.T) {
	rig := newConsumerRig(t, OperationAgentRunnerCrash, AdapterOutcomeApplied, nil)
	rig.repository.record = TrustedAuthorityRecord{}
	if _, err := rig.consumer.Consume(context.Background(), rig.principal, rig.command); !errors.Is(err, ErrTrustedAuthorityNotFound) {
		t.Fatalf("unknown authority error = %v", err)
	}

	rig = newConsumerRig(t, OperationAgentRunnerCrash, AdapterOutcomeApplied, nil)
	rig.repository.err = errors.New("secret backend credential: do-not-leak")
	_, err := rig.consumer.Consume(context.Background(), rig.principal, rig.command)
	if !errors.Is(err, ErrTrustedAuthorityUnavailable) || strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("repository error = %v", err)
	}
}

func TestConsumerRejectsTrustedEnvelopeAndTrustPolicyDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*TrustedAuthorityRecord)
	}{
		{name: "envelope", mutate: func(record *TrustedAuthorityRecord) { record.Envelope = append(record.Envelope, '\n') }},
		{name: "expected operation", mutate: func(record *TrustedAuthorityRecord) { record.Expected.OperationKind = OperationControllerTimeout }},
		{name: "trust role", mutate: func(record *TrustedAuthorityRecord) {
			for keyID, signer := range record.TrustPolicy.Signers {
				signer.Role = "qualification-runner"
				record.TrustPolicy.Signers[keyID] = signer
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rig := newConsumerRig(t, OperationAgentRunnerCrash, AdapterOutcomeApplied, nil)
			record := rig.repository.record
			test.mutate(&record)
			rig.repository.record = record
			if _, err := rig.consumer.Consume(context.Background(), rig.principal, rig.command); !errors.Is(err, ErrTrustedAuthorityUnavailable) {
				t.Fatalf("Consume() error = %v", err)
			}
			if rig.adapter.executeCalls.Load() != 0 || rig.ledger.reservationCount() != 0 {
				t.Fatal("trusted authority drift reached adapter or reservation")
			}
		})
	}
}

func TestConsumerUnknownOutcomeReplayOnlyInspects(t *testing.T) {
	rig := newConsumerRig(t, OperationAgentRunnerTimeout, AdapterOutcomeApplied, errors.New("downstream response lost"))
	if _, err := rig.consumer.Consume(context.Background(), rig.principal, rig.command); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("first Consume() error = %v", err)
	}
	if _, err := rig.consumer.Consume(context.Background(), rig.principal, rig.command); !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, ErrConflict) {
		t.Fatalf("replayed Consume() error = %v", err)
	}
	if rig.adapter.executeCalls.Load() != 1 || rig.adapter.resolveCalls.Load() != 1 {
		t.Fatalf("execute=%d resolve=%d", rig.adapter.executeCalls.Load(), rig.adapter.resolveCalls.Load())
	}
}

func TestConsumerSecurityCanaryIsTypedAndRefused(t *testing.T) {
	rig := newConsumerRig(t, OperationAgentSecurityCanary, AdapterOutcomeRefused, nil)
	record, err := rig.consumer.Consume(context.Background(), rig.principal, rig.command)
	if err != nil {
		t.Fatal(err)
	}
	if record.Terminal == nil || record.Terminal.Receipt.OperationKind != OperationAgentSecurityCanary ||
		record.Terminal.Receipt.Outcome != AdapterOutcomeRefused || rig.adapter.executeCalls.Load() != 1 {
		t.Fatalf("record = %+v", record)
	}
}

func TestDecodeConsumeRequestRejectsExtraCommandAndMalformedShape(t *testing.T) {
	authorityID, fixtureID, runID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	valid := `{"fixtureId":"` + fixtureID + `","runId":"` + runID + `","schemaVersion":"` + ConsumeRequestSchemaV1 + `"}`
	command, err := DecodeConsumeRequest([]byte(valid), authorityID)
	if err != nil || command.AuthorityID.String() != authorityID || command.FixtureID.String() != fixtureID || command.RunID.String() != runID {
		t.Fatalf("DecodeConsumeRequest() = %+v, %v", command, err)
	}
	invalid := []string{
		strings.TrimSuffix(valid, "}") + `,"operationKind":"agent-runner-crash"}`,
		strings.TrimSuffix(valid, "}") + `,"resourceId":"runner/1"}`,
		strings.Replace(valid, `"runId":"`+runID+`"`, `"runId":"`+runID+`","runId":"`+runID+`"`, 1),
		strings.Replace(valid, `"fixtureId":"`+fixtureID+`"`, `"fixtureId":null`, 1),
		valid + `{}`,
	}
	for _, input := range invalid {
		if _, err := DecodeConsumeRequest([]byte(input), authorityID); err == nil {
			t.Fatalf("accepted invalid input %s", input)
		}
	}
}

type consumerRig struct {
	consumer       *Consumer
	repository     *fixedAuthorityRepository
	ledger         *memoryFaultLedger
	ledgerBoundary *countingFaultLedger
	adapter        *recordingFaultAdapter
	principal      RunPrincipal
	command        ConsumeCommand
}

func newConsumerRig(t *testing.T, operation OperationKind, outcome AdapterOutcome, executeErr error) consumerRig {
	t.Helper()
	fixture := signedFaultFixtureForOperation(t, operation)
	ledger := newMemoryFaultLedger(fixture.now)
	adapter := &recordingFaultAdapter{
		resolution: ResourceResolution{
			ResourceID: "qualification-resource/exact-1", HeadDigest: testFaultDigest("head-before"),
			FenceDigest: fixture.expected.ExpectedFenceDigest,
		},
		result: AdapterResult{
			Outcome: outcome, ResultDigest: testFaultDigest("typed-adapter-result"),
			ObservedHeadDigest: testFaultDigest("head-after"), ObservedFenceDigest: testFaultDigest("fence-after"),
		},
		executeErr: executeErr, ledger: ledger,
	}
	actorID, tenantID, projectID := uuid.New(), uuid.New(), uuid.New()
	record := TrustedAuthorityRecord{
		AuthorityID: fixture.expected.AuthorityID, Audience: "urn:worksflow:golden-stack",
		Envelope: append([]byte(nil), fixture.envelope...), Expected: fixture.expected,
		FaultOperatorActorID: actorID, ProjectID: projectID, TenantID: tenantID,
		TrustPolicy: TrustPolicy{
			MinimumSignatures: 1,
			Signers:           map[string]SignerTrust{fixture.key.keyID: testSignerTrust(fixture.key, fixture.now)},
		},
	}
	repository := &fixedAuthorityRepository{record: record}
	ledgerBoundary := &countingFaultLedger{delegate: ledger}
	consumer, err := NewConsumer(repository, ledgerBoundary, map[OperationKind]Adapter{operation: adapter})
	if err != nil {
		t.Fatal(err)
	}
	principal := RunPrincipal{
		ActorID: actorID, Audience: record.Audience, FixtureID: record.Expected.FixtureID,
		ProjectID: projectID, Role: FaultOperatorRole, RunID: record.Expected.RunID, TenantID: tenantID,
	}
	return consumerRig{
		consumer: consumer, repository: repository, ledger: ledger, ledgerBoundary: ledgerBoundary,
		adapter: adapter, principal: principal,
		command: ConsumeCommand{AuthorityID: record.AuthorityID, FixtureID: record.Expected.FixtureID, RunID: record.Expected.RunID},
	}
}

func signedFaultFixtureForOperation(t *testing.T, operation OperationKind) signedFaultFixture {
	t.Helper()
	fixture := newSignedFaultFixture(t)
	selector, allowed := selectorForOperation(operation)
	if !allowed {
		t.Fatalf("operation %q is not allowed", operation)
	}
	fixture.predicate.OperationKind = operation
	fixture.predicate.ResourceSelector = selector
	payload, err := canonicalJSON(fixture.predicate)
	if err != nil {
		t.Fatal(err)
	}
	fixture.envelope = signFaultPayload(t, fixture.key, payload)
	fixture.expected.OperationKind = operation
	fixture.expected.ResourceSelector = selector
	fixture.expected.EnvelopeDigest = sha256Digest(fixture.envelope)
	fixture.expected.PayloadDigest = sha256Digest(payload)
	return fixture
}

type fixedAuthorityRepository struct {
	record TrustedAuthorityRecord
	err    error
	calls  atomic.Int64
}

func (repository *fixedAuthorityRepository) Load(_ context.Context, authorityID uuid.UUID) (TrustedAuthorityRecord, error) {
	repository.calls.Add(1)
	if repository.err != nil {
		return TrustedAuthorityRecord{}, repository.err
	}
	if repository.record.AuthorityID != authorityID {
		return TrustedAuthorityRecord{}, ErrTrustedAuthorityNotFound
	}
	return repository.record, nil
}

var _ TrustedAuthorityRepository = (*fixedAuthorityRepository)(nil)

type countingFaultLedger struct {
	delegate *memoryFaultLedger
	calls    atomic.Int64
}

func (ledger *countingFaultLedger) TrustedTime(ctx context.Context) (time.Time, error) {
	ledger.calls.Add(1)
	return ledger.delegate.TrustedTime(ctx)
}

func (ledger *countingFaultLedger) Reserve(ctx context.Context, reservation Reservation) (ConsumeRecord, error) {
	ledger.calls.Add(1)
	return ledger.delegate.Reserve(ctx, reservation)
}

func (ledger *countingFaultLedger) CommitTerminal(ctx context.Context, terminal TerminalResult) (ConsumeRecord, error) {
	ledger.calls.Add(1)
	return ledger.delegate.CommitTerminal(ctx, terminal)
}

func (ledger *countingFaultLedger) Inspect(ctx context.Context, authorityID uuid.UUID) (ConsumeRecord, error) {
	ledger.calls.Add(1)
	return ledger.delegate.Inspect(ctx, authorityID)
}

type typedNilFaultContext struct{}

func (*typedNilFaultContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*typedNilFaultContext) Done() <-chan struct{}       { return nil }
func (*typedNilFaultContext) Err() error                  { return nil }
func (*typedNilFaultContext) Value(any) any               { return nil }

var (
	_ Ledger          = (*countingFaultLedger)(nil)
	_ context.Context = (*typedNilFaultContext)(nil)
)
