package qualificationpromotionv2

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestMemoryServiceFreshExactReplayAndRetiredAuthority(t *testing.T) {
	service, store := mustMemoryService(t, testPrepared())
	command := testCommand()
	fresh, err := service.Consume(context.Background(), command)
	if err != nil {
		t.Fatalf("fresh Consume() error = %v", err)
	}
	if fresh.Idempotent {
		t.Fatal("fresh consumption reported idempotent")
	}
	store.RemoveAuthority(command.WorkflowInputAuthorityID, command.PlanAuthorityID)
	replay, err := service.Consume(context.Background(), command)
	if err != nil {
		t.Fatalf("retired replay Consume() error = %v", err)
	}
	if !replay.Idempotent || !SameImmutableRecord(fresh, replay) {
		t.Fatal("exact replay did not recover the immutable committed result")
	}
	inspected, err := service.InspectOperation(context.Background(), command.OperationID)
	if err != nil || !SameImmutableRecord(fresh, inspected) {
		t.Fatalf("InspectOperation() = %#v, %v", inspected, err)
	}
	handoff, err := store.InspectHandoff(context.Background(), command.HandoffID)
	if err != nil || !SameImmutableRecord(fresh, handoff) {
		t.Fatalf("MemoryStore.InspectHandoff() = %#v, %v", handoff, err)
	}
}

func TestMemoryStoreClonesInstalledAuthorityAndReturnedRecords(t *testing.T) {
	prepared := testPrepared()
	service, _ := mustMemoryService(t, prepared)
	prepared.EvidenceEventSet.Events[0].EventHash = testDigest("caller-mutated-event")
	prepared.PlanReceiptLineage.EvidencePlan.Plan.Artifacts[0].ID = "caller-mutated-plan-artifact"
	prepared.PlanReceiptLineage.EvidencePlan.Receipt.Artifacts[0].ID = "caller-mutated-receipt-artifact"
	fresh, err := service.Consume(context.Background(), testCommand())
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	fresh.RequestBytes[0] ^= 1
	fresh.EvidenceEventSet.Events[0].EventHash = testDigest("caller-mutated-result")
	inspected, err := service.InspectOperation(context.Background(), testCommand().OperationID)
	if err != nil {
		t.Fatalf("InspectOperation() error = %v", err)
	}
	if err := ValidateRecord(inspected); err != nil || inspected.EvidenceEventSet.Events[0].EventHash == fresh.EvidenceEventSet.Events[0].EventHash {
		t.Fatalf("stored clone validation = %v", err)
	}
}

func TestMemoryServiceChangedOperationReplayConflicts(t *testing.T) {
	service, _ := mustMemoryService(t, testPrepared())
	command := testCommand()
	if _, err := service.Consume(context.Background(), command); err != nil {
		t.Fatalf("fresh Consume() error = %v", err)
	}
	command.HandoffID = testUUID("60000000-0000-4000-8000-000000000001")
	if _, err := service.Consume(context.Background(), command); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay error = %v", err)
	}
}

func TestMemoryStoreEnforcesAllSingleUseAndCrossRoleReservations(t *testing.T) {
	service, store := mustMemoryService(t, testPrepared())
	original := testCommand()
	if _, err := service.Consume(context.Background(), original); err != nil {
		t.Fatalf("fresh Consume() error = %v", err)
	}

	cases := map[string]struct {
		command   ConsumeCommand
		receiptID string
	}{
		"workflow and plan": {
			command: ConsumeCommand{
				OperationID: testUUID("61000000-0000-4000-8000-000000000001"), WorkflowInputAuthorityID: original.WorkflowInputAuthorityID,
				PlanAuthorityID: original.PlanAuthorityID, HandoffID: testUUID("61000000-0000-4000-8000-000000000002"),
				OutputRevisionID: testUUID("61000000-0000-4000-8000-000000000003"),
			}, receiptID: "second-receipt",
		},
		"receipt": {
			command: distinctCommand("62000000"), receiptID: "qualification-receipt",
		},
		"input precommit": {
			command: distinctCommand("66000000"), receiptID: "sixth-receipt",
		},
		"handoff": {
			command: distinctCommand("63000000"), receiptID: "third-receipt",
		},
		"output revision": {
			command: distinctCommand("64000000"), receiptID: "fourth-receipt",
		},
		"cross-role operation reservation": {
			command: distinctCommand("65000000"), receiptID: "fifth-receipt",
		},
	}
	candidate := cases["handoff"]
	candidate.command.HandoffID = original.HandoffID
	cases["handoff"] = candidate
	candidate = cases["output revision"]
	candidate.command.OutputRevisionID = original.OutputRevisionID
	cases["output revision"] = candidate
	candidate = cases["cross-role operation reservation"]
	candidate.command.OperationID = original.HandoffID
	cases["cross-role operation reservation"] = candidate

	for name, fixture := range cases {
		t.Run(name, func(t *testing.T) {
			prepared := preparedForCommand(testPrepared(), fixture.command, fixture.receiptID)
			if fixture.command.WorkflowInputAuthorityID != original.WorkflowInputAuthorityID || fixture.command.PlanAuthorityID != original.PlanAuthorityID {
				if err := store.InstallAuthority(prepared); err != nil {
					t.Fatalf("InstallAuthority() error = %v", err)
				}
			}
			if _, err := service.Consume(context.Background(), fixture.command); !errors.Is(err, ErrConflict) {
				t.Fatalf("Consume() error = %v", err)
			}
		})
	}
}

func TestMemoryStoreConflictsWithExistingGlobalArtifactRevisionReservation(t *testing.T) {
	service, store := mustMemoryService(t, testPrepared())
	if err := store.ReserveArtifactRevision(testCommand().OutputRevisionID); err != nil {
		t.Fatalf("ReserveArtifactRevision() error = %v", err)
	}
	if _, err := service.Consume(context.Background(), testCommand()); !errors.Is(err, ErrConflict) {
		t.Fatalf("Consume() error = %v", err)
	}
}

func TestMemoryStoreTreatsInstalledTargetRevisionAsGloballyReserved(t *testing.T) {
	prepared := testPrepared()
	command := testCommand()
	command.OutputRevisionID = testUUID(prepared.Target.TargetRevisionID)
	service, _ := mustMemoryService(t, prepared)
	if _, err := service.Consume(context.Background(), command); !errors.Is(err, ErrConflict) {
		t.Fatalf("Consume() output aliases target error = %v", err)
	}
	if _, err := Compile(command, prepared, testTime()); !errors.Is(err, ErrConflict) {
		t.Fatalf("Compile() output aliases target error = %v", err)
	}
}

func TestMemoryStoreChecksDurableReservationsBeforeAuthorityAvailability(t *testing.T) {
	service, store := mustMemoryService(t, testPrepared())
	original := testCommand()
	if _, err := service.Consume(context.Background(), original); err != nil {
		t.Fatalf("fresh Consume() error = %v", err)
	}
	store.RemoveAuthority(original.WorkflowInputAuthorityID, original.PlanAuthorityID)
	changed := original
	changed.OperationID = testUUID("68000000-0000-4000-8000-000000000001")
	changed.HandoffID = testUUID("68000000-0000-4000-8000-000000000002")
	changed.OutputRevisionID = testUUID("68000000-0000-4000-8000-000000000003")
	if _, err := service.Consume(context.Background(), changed); !errors.Is(err, ErrConflict) || errors.Is(err, ErrNotReady) {
		t.Fatalf("consumed authority without mutable fixture error = %v", err)
	}

	empty, err := NewMemoryStore(testTime)
	if err != nil {
		t.Fatalf("NewMemoryStore() error = %v", err)
	}
	reservedCommand := distinctCommand("69000000")
	if err := empty.ReserveArtifactRevision(reservedCommand.OutputRevisionID); err != nil {
		t.Fatalf("ReserveArtifactRevision() error = %v", err)
	}
	if _, err := empty.Consume(context.Background(), reservedCommand); !errors.Is(err, ErrConflict) || errors.Is(err, ErrNotReady) {
		t.Fatalf("reserved output without mutable fixture error = %v", err)
	}
}

func TestMemoryStoreFailsNonEmptyIndependentPolicyBeforeAnyDurableWrite(t *testing.T) {
	prepared := testPrepared()
	prepared.IndependentRequirements = []IndependentAuthorityRequirement{{
		Kind: IndependentProductionPostgreSQL, AuthorityID: "missing-posture", AuthorityHash: testDigest("missing-posture"),
	}}
	service, _ := mustMemoryService(t, prepared)
	if _, err := service.Consume(context.Background(), testCommand()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Consume() error = %v", err)
	}
	if _, err := service.InspectOperation(context.Background(), testCommand().OperationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("InspectOperation() after rejected non-empty policy error = %v", err)
	}
}

func TestMemoryStoreConcurrentExactAttemptsConverge(t *testing.T) {
	service, _ := mustMemoryService(t, testPrepared())
	const workers = 32
	results := make(chan Record, workers)
	errorsChannel := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			record, err := service.Consume(context.Background(), testCommand())
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- record
		}()
	}
	wait.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Errorf("concurrent Consume() error = %v", err)
	}
	var baseline Record
	fresh := 0
	count := 0
	for record := range results {
		if count == 0 {
			baseline = record
		} else if !SameImmutableRecord(baseline, record) {
			t.Error("concurrent exact attempts returned different immutable bytes")
		}
		if !record.Idempotent {
			fresh++
		}
		count++
	}
	if count != workers || fresh != 1 {
		t.Fatalf("results = %d, fresh = %d", count, fresh)
	}
}

func TestMemoryStoreConcurrentDifferentAttemptsConsumeAuthorityOnce(t *testing.T) {
	service, _ := mustMemoryService(t, testPrepared())
	first := testCommand()
	second := first
	second.OperationID = testUUID("66000000-0000-4000-8000-000000000001")
	second.HandoffID = testUUID("66000000-0000-4000-8000-000000000002")
	second.OutputRevisionID = testUUID("66000000-0000-4000-8000-000000000003")
	commands := []ConsumeCommand{first, second}
	errorsChannel := make(chan error, len(commands))
	var wait sync.WaitGroup
	for _, command := range commands {
		command := command
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.Consume(context.Background(), command)
			errorsChannel <- err
		}()
	}
	wait.Wait()
	close(errorsChannel)
	successes, conflicts := 0, 0
	for err := range errorsChannel {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes = %d, conflicts = %d", successes, conflicts)
	}
}

func TestServiceReconcilesCommitUnknownOnlyByExactOperation(t *testing.T) {
	service, store := mustMemoryService(t, testPrepared())
	store.InjectCommitUnknownOnce()
	record, err := service.Consume(context.Background(), testCommand())
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	validationErr := ValidateRecord(record)
	if !record.Idempotent || validationErr != nil {
		t.Fatalf("reconciled record idempotent = %v, validation = %v", record.Idempotent, validationErr)
	}

	unknownStore := &unknownNoCommitStore{}
	noCommit, _ := NewService(unknownStore)
	if _, err := noCommit.Consume(context.Background(), testCommand()); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("unknown without commit error = %v", err)
	}
	if unknownStore.consumeCalls != 1 || unknownStore.inspectCalls != 1 {
		t.Fatalf("commit-unknown calls = consume:%d inspect:%d", unknownStore.consumeCalls, unknownStore.inspectCalls)
	}

	alternateCommand := distinctCommand("67000000")
	alternatePrepared := preparedForCommand(testPrepared(), alternateCommand, "alternate-receipt")
	alternate, err := Compile(alternateCommand, alternatePrepared, testTime())
	if err != nil {
		t.Fatalf("Compile(alternate) error = %v", err)
	}
	divergent, _ := NewService(unknownDivergentStore{record: alternate})
	if _, err := divergent.Consume(context.Background(), testCommand()); !errors.Is(err, ErrConflict) {
		t.Fatalf("divergent reconciliation error = %v", err)
	}
}

func TestServiceReconcilesCommitUnknownWithBoundedUncancelledContext(t *testing.T) {
	record := compileTestRecord(t)
	ctx, cancel := context.WithCancel(context.Background())
	store := &cancelOnUnknownPromotionStore{record: record, cancel: cancel}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := service.Consume(ctx, testCommand())
	if err != nil || !SameImmutableRecord(resolved, record) || !resolved.Idempotent {
		t.Fatalf("Consume() = %#v, %v", resolved, err)
	}
	if store.consumeCalls != 1 || store.inspectCalls != 1 || store.inspectContextErr != nil ||
		!store.inspectContextBounded || store.inspectedOperationID != testCommand().OperationID {
		t.Fatalf(
			"recovery consume=%d inspect=%d context=%v bounded=%v operation=%s",
			store.consumeCalls,
			store.inspectCalls,
			store.inspectContextErr,
			store.inspectContextBounded,
			store.inspectedOperationID,
		)
	}
}

func TestServiceDoesNotExposeUnclassifiedStoreDetails(t *testing.T) {
	service, err := NewService(failingAtomicStore{})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if _, err := service.Consume(context.Background(), testCommand()); !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, errSensitiveStore) {
		t.Fatalf("Consume() error = %v", err)
	}
	if _, err := service.InspectOperation(context.Background(), testCommand().OperationID); !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, errSensitiveStore) {
		t.Fatalf("InspectOperation() error = %v", err)
	}
}

func TestServiceTreatsConsumeContextFailureAsOutcomeUnknown(t *testing.T) {
	for name, storeErr := range map[string]error{
		"canceled": context.Canceled,
		"deadline": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			service, err := NewService(errorAtomicStore{err: storeErr})
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}
			if _, err := service.Consume(context.Background(), testCommand()); !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, storeErr) {
				t.Fatalf("Consume() error = %v", err)
			}
		})
	}
}

func TestServiceReturnsPreflightCancellationBeforeAtomicStore(t *testing.T) {
	service, _ := mustMemoryService(t, testPrepared())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Consume(ctx, testCommand()); !errors.Is(err, context.Canceled) || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("preflight Consume() error = %v", err)
	}
	if _, err := service.InspectOperation(context.Background(), testCommand().OperationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("preflight cancellation wrote an operation: %v", err)
	}

	trackingStore := &unknownNoCommitStore{}
	trackingService, err := NewService(trackingStore)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trackingService.InspectOperation(ctx, testCommand().OperationID); !errors.Is(err, context.Canceled) {
		t.Fatalf("preflight InspectOperation() error = %v", err)
	}
	if trackingStore.inspectCalls != 0 {
		t.Fatalf("cancelled preflight called AtomicStore.InspectOperation %d times", trackingStore.inspectCalls)
	}
}

func TestNewServiceRejectsTypedNilAtomicStore(t *testing.T) {
	var store *MemoryStore
	if _, err := NewService(store); !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewService() error = %v", err)
	}
}

func distinctCommand(prefix string) ConsumeCommand {
	return ConsumeCommand{
		OperationID:              testUUID(prefix + "-0000-4000-8000-000000000001"),
		WorkflowInputAuthorityID: testUUID(prefix + "-0000-4000-8000-000000000002"),
		PlanAuthorityID:          testUUID(prefix + "-0000-4000-8000-000000000003"),
		HandoffID:                testUUID(prefix + "-0000-4000-8000-000000000004"),
		OutputRevisionID:         testUUID(prefix + "-0000-4000-8000-000000000005"),
	}
}

func preparedForCommand(base PreparedAuthority, command ConsumeCommand, receiptID string) PreparedAuthority {
	prepared := clonePrepared(base)
	prepared.WorkflowInput.AuthorityID = command.WorkflowInputAuthorityID.String()
	prepared.Plan.AuthorityID = command.PlanAuthorityID.String()
	prepared.Plan.InputAuthorityID = command.WorkflowInputAuthorityID.String()
	prepared.InputPrecommit.WorkflowInputAuthorityID = command.WorkflowInputAuthorityID.String()
	prepared.InputPrecommit.QualificationPlanAuthorityID = command.PlanAuthorityID.String()
	prepared.Receipt.ReceiptID = receiptID
	planArtifactID := QualificationPlanArtifactPrefix + command.PlanAuthorityID.String()
	prepared.PlanReceiptLineage.EvidencePlan.Plan.QualificationPlanArtifactID = planArtifactID
	prepared.PlanReceiptLineage.EvidencePlan.Plan.Outputs.ReceiptID = receiptID
	prepared.PlanReceiptLineage.EvidencePlan.Receipt = prepared.PlanReceiptLineage.EvidencePlan.Plan
	prepared.PlanReceiptLineage.EvidencePlan.Receipt.Artifacts = cloneSlice(prepared.PlanReceiptLineage.EvidencePlan.Plan.Artifacts)
	prepared.Plan.EvidencePlanHash = testCanonicalDigest(prepared.PlanReceiptLineage.EvidencePlan.Plan)
	prepared.Evidence.CommandHash = prepared.Plan.EvidencePlanHash
	for _, authority := range []*PlanAuthorityLineageBinding{
		&prepared.PlanReceiptLineage.Authority.Plan,
		&prepared.PlanReceiptLineage.Authority.Receipt,
	} {
		authority.ArtifactID = planArtifactID
		authority.AuthorityID = command.PlanAuthorityID.String()
		authority.EvidencePlanHash = prepared.Plan.EvidencePlanHash
		authority.InputAuthorityID = command.WorkflowInputAuthorityID.String()
	}
	prepared.ReceiptControls.PlanAuthorityID = command.PlanAuthorityID.String()
	for _, request := range []*ReceiptRequestBindings{
		&prepared.ReceiptControls.Requests.SnapshotSeal,
		&prepared.ReceiptControls.Requests.SnapshotVerify,
		&prepared.ReceiptControls.Requests.RunnerSign,
		&prepared.ReceiptControls.Requests.ApproverSign,
	} {
		request.PlanAuthorityID = command.PlanAuthorityID.String()
		request.EvidencePlanHash = prepared.Plan.EvidencePlanHash
		request.EvidenceCommandDigest = prepared.Plan.EvidencePlanHash
	}
	return prepared
}

type unknownNoCommitStore struct {
	consumeCalls int
	inspectCalls int
}

type cancelOnUnknownPromotionStore struct {
	record                Record
	cancel                context.CancelFunc
	consumeCalls          int
	inspectCalls          int
	inspectContextErr     error
	inspectContextBounded bool
	inspectedOperationID  uuid.UUID
}

func (store *cancelOnUnknownPromotionStore) Consume(context.Context, ConsumeCommand) (Record, error) {
	store.consumeCalls++
	store.cancel()
	return Record{}, ErrStoreOutcomeUnknown
}

func (store *cancelOnUnknownPromotionStore) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	store.inspectCalls++
	store.inspectContextErr = ctx.Err()
	_, store.inspectContextBounded = ctx.Deadline()
	store.inspectedOperationID = operationID
	return cloneRecord(store.record), nil
}

func (store *unknownNoCommitStore) Consume(context.Context, ConsumeCommand) (Record, error) {
	store.consumeCalls++
	return Record{}, ErrStoreOutcomeUnknown
}
func (store *unknownNoCommitStore) InspectOperation(context.Context, uuid.UUID) (Record, error) {
	store.inspectCalls++
	return Record{}, ErrNotFound
}
func (*unknownNoCommitStore) InspectHandoff(context.Context, uuid.UUID) (Record, error) {
	return Record{}, ErrNotFound
}

type unknownDivergentStore struct{ record Record }

func (unknownDivergentStore) Consume(context.Context, ConsumeCommand) (Record, error) {
	return Record{}, ErrStoreOutcomeUnknown
}
func (store unknownDivergentStore) InspectOperation(context.Context, uuid.UUID) (Record, error) {
	return CloneRecord(store.record), nil
}
func (unknownDivergentStore) InspectHandoff(context.Context, uuid.UUID) (Record, error) {
	return Record{}, ErrNotFound
}

var errSensitiveStore = errors.New("pq: password=secret relation=private_receipts")

type failingAtomicStore struct{}

func (failingAtomicStore) Consume(context.Context, ConsumeCommand) (Record, error) {
	return Record{}, errSensitiveStore
}
func (failingAtomicStore) InspectOperation(context.Context, uuid.UUID) (Record, error) {
	return Record{}, errSensitiveStore
}
func (failingAtomicStore) InspectHandoff(context.Context, uuid.UUID) (Record, error) {
	return Record{}, errSensitiveStore
}

type errorAtomicStore struct{ err error }

func (store errorAtomicStore) Consume(context.Context, ConsumeCommand) (Record, error) {
	return Record{}, store.err
}
func (store errorAtomicStore) InspectOperation(context.Context, uuid.UUID) (Record, error) {
	return Record{}, store.err
}
func (store errorAtomicStore) InspectHandoff(context.Context, uuid.UUID) (Record, error) {
	return Record{}, store.err
}
