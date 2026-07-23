package qualificationpolicyauthority

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestIssueFreezesTrustedPolicyAndExposesExactCurrentAuthority(t *testing.T) {
	service, _, source := newTestService(t)
	command := validIssueCommand()

	record, err := service.Issue(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if record.Idempotent || source.callCount() != 1 || source.lastID != command.PolicySourceID {
		t.Fatalf("Issue() = record:%+v source calls:%d last:%q", record, source.callCount(), source.lastID)
	}
	if got := reflect.TypeOf(IssueCommand{}).NumField(); got != 4 {
		t.Fatalf("IssueCommand acquired caller-controlled policy fields: %d", got)
	}
	if record.Document.Generation != 1 || record.Document.PreviousAuthorityHash != nil ||
		record.Document.IssuedAt != "2026-07-19T18:30:00.123Z" || record.IssuedAt != fixedDatabaseTime ||
		record.Document.ProjectID != validResolvedPolicy().ProjectID.String() ||
		record.Document.Status != AuthorityStatusActive {
		t.Fatalf("issued authority root = %+v", record.Document)
	}
	if record.Document.ComponentDigests.RevisionPolicy != record.RevisionPolicyHash ||
		record.Document.ComponentDigests.PlanInputProfile != record.PlanInputProfileHash ||
		record.Document.ComponentDigests.PromotionPolicy != record.PromotionPolicyHash ||
		record.AuthorityHash == record.RevisionPolicyHash || record.AuthorityHash == record.PlanInputProfileHash ||
		record.AuthorityHash == record.PromotionPolicyHash {
		t.Fatalf("component/root hash domains are inconsistent: %+v", record.Document.ComponentDigests)
	}
	if err := ValidateRecord(record); err != nil {
		t.Fatalf("issued record failed independent validation: %v", err)
	}

	resolved, err := service.ResolveAuthority(context.Background(), command.AuthorityID)
	if err != nil || !sameImmutableRecord(record, resolved) {
		t.Fatalf("ResolveAuthority() = record:%+v error:%v", resolved, err)
	}
	current, err := service.ResolveCurrent(context.Background(), validResolvedPolicy().ProjectID, validResolvedPolicy().ExecutionProfile)
	if err != nil || !sameImmutableRecord(record, current) {
		t.Fatalf("ResolveCurrent() = record:%+v error:%v", current, err)
	}
	asserted, err := service.AssertCurrent(context.Background(), command.AuthorityID)
	if err != nil || !sameImmutableRecord(record, asserted) {
		t.Fatalf("AssertCurrent() = record:%+v error:%v", asserted, err)
	}

	resolved.DocumentBytes[0] = 'x'
	resolved.RevisionPolicy.ExactApprovedSources[0].Purpose = "mutated"
	again, err := service.ResolveAuthority(context.Background(), command.AuthorityID)
	if err != nil || again.DocumentBytes[0] == 'x' || again.RevisionPolicy.ExactApprovedSources[0].Purpose == "mutated" {
		t.Fatalf("store returned aliased mutable authority: record:%+v error:%v", again, err)
	}
}

func TestIssueReplayInspectsBeforeSourceAndRejectsCommandDrift(t *testing.T) {
	service, _, source := newTestService(t)
	command := validIssueCommand()
	first, err := service.Issue(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	source.setError(errors.New("reviewed source retired"))
	replayed, err := service.Issue(context.Background(), command)
	if err != nil || !replayed.Idempotent || !sameImmutableRecord(first, replayed) {
		t.Fatalf("exact replay = record:%+v error:%v", replayed, err)
	}
	if source.callCount() != 1 {
		t.Fatalf("replay re-resolved retired source: %d calls", source.callCount())
	}

	for name, mutate := range map[string]func(*IssueCommand){
		"authority": func(value *IssueCommand) { value.AuthorityID = uuid.New() },
		"source":    func(value *IssueCommand) { value.PolicySourceID = "different-reviewed-release" },
		"cursor":    func(value *IssueCommand) { value.ExpectedPreviousAuthorityHash = testDigest("different-head") },
	} {
		t.Run(name, func(t *testing.T) {
			drifted := command
			mutate(&drifted)
			if _, err := service.Issue(context.Background(), drifted); !errors.Is(err, ErrConflict) {
				t.Fatalf("drifted replay error = %v", err)
			}
		})
	}
	if source.callCount() != 1 {
		t.Fatalf("drifted replay reached source: %d calls", source.callCount())
	}
}

func TestDatabaseClockIsTrustedBoundedAndSkippedOnReplay(t *testing.T) {
	command := validIssueCommand()
	source := &fakePolicySource{values: map[string]ResolvedPolicy{
		command.PolicySourceID: validResolvedPolicy(),
	}}
	store := NewMemoryStore()
	var clockCalls atomic.Int64
	clock := DatabaseClockFunc(func(context.Context) (time.Time, error) {
		clockCalls.Add(1)
		return fixedDatabaseTime, nil
	})
	service, err := NewService(source, store, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Issue(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Issue(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if clockCalls.Load() != 1 {
		t.Fatalf("exact replay read database time again: %d calls", clockCalls.Load())
	}

	invalidClockService, err := NewService(
		&fakePolicySource{values: map[string]ResolvedPolicy{command.PolicySourceID: validResolvedPolicy()}},
		NewMemoryStore(),
		DatabaseClockFunc(func(context.Context) (time.Time, error) {
			return fixedDatabaseTime.Add(time.Nanosecond), nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invalidClockService.Issue(context.Background(), command); !errors.Is(err, ErrInvalid) {
		t.Fatalf("sub-millisecond database time error = %v", err)
	}
}

func TestAppendOnlyGenerationSuspensionAndResume(t *testing.T) {
	service, _, source := newTestService(t)
	first, err := service.Issue(context.Background(), validIssueCommand())
	if err != nil {
		t.Fatal(err)
	}

	secondCommand := IssueCommand{
		OperationID:                   uuid.MustParse("10000000-0000-4000-8000-000000000011"),
		AuthorityID:                   uuid.MustParse("10000000-0000-4000-8000-000000000012"),
		PolicySourceID:                validIssueCommand().PolicySourceID,
		ExpectedPreviousAuthorityHash: first.AuthorityHash,
	}
	second, err := service.Issue(context.Background(), secondCommand)
	if err != nil {
		t.Fatal(err)
	}
	if second.Document.Generation != 2 || second.Document.PreviousAuthorityHash == nil ||
		*second.Document.PreviousAuthorityHash != first.AuthorityHash {
		t.Fatalf("second generation = %+v", second.Document)
	}
	if _, err := service.AssertCurrent(context.Background(), first.Command.AuthorityID); !errors.Is(err, ErrStale) {
		t.Fatalf("superseded first authority error = %v", err)
	}

	source.mu.Lock()
	suspendedPolicy := source.values[validIssueCommand().PolicySourceID]
	suspendedPolicy.Status = AuthorityStatusSuspended
	source.values[validIssueCommand().PolicySourceID] = suspendedPolicy
	source.mu.Unlock()
	suspendedCommand := IssueCommand{
		OperationID:                   uuid.MustParse("10000000-0000-4000-8000-000000000021"),
		AuthorityID:                   uuid.MustParse("10000000-0000-4000-8000-000000000022"),
		PolicySourceID:                validIssueCommand().PolicySourceID,
		ExpectedPreviousAuthorityHash: second.AuthorityHash,
	}
	suspended, err := service.Issue(context.Background(), suspendedCommand)
	if err != nil {
		t.Fatal(err)
	}
	if suspended.Document.Generation != 3 || suspended.Document.Status != AuthorityStatusSuspended {
		t.Fatalf("suspension generation = %+v", suspended.Document)
	}
	if _, err := service.AssertCurrent(context.Background(), second.Command.AuthorityID); !errors.Is(err, ErrStale) {
		t.Fatalf("superseded second authority error = %v", err)
	}
	if _, err := service.AssertCurrent(context.Background(), suspended.Command.AuthorityID); !errors.Is(err, ErrStale) {
		t.Fatalf("suspended current authority error = %v", err)
	}
	current, err := service.ResolveCurrent(context.Background(), validResolvedPolicy().ProjectID, validResolvedPolicy().ExecutionProfile)
	if err != nil || current.Command.AuthorityID != suspended.Command.AuthorityID {
		t.Fatalf("diagnostic suspended head = record:%+v error:%v", current, err)
	}

	source.mu.Lock()
	activePolicy := source.values[validIssueCommand().PolicySourceID]
	activePolicy.Status = AuthorityStatusActive
	source.values[validIssueCommand().PolicySourceID] = activePolicy
	source.mu.Unlock()
	resumeCommand := IssueCommand{
		OperationID:                   uuid.MustParse("10000000-0000-4000-8000-000000000031"),
		AuthorityID:                   uuid.MustParse("10000000-0000-4000-8000-000000000032"),
		PolicySourceID:                validIssueCommand().PolicySourceID,
		ExpectedPreviousAuthorityHash: suspended.AuthorityHash,
	}
	resumed, err := service.Issue(context.Background(), resumeCommand)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Document.Generation != 4 || resumed.Document.Status != AuthorityStatusActive {
		t.Fatalf("resumed generation = %+v", resumed.Document)
	}
	if _, err := service.AssertCurrent(context.Background(), resumed.Command.AuthorityID); err != nil {
		t.Fatalf("resumed active head was not current: %v", err)
	}
}

func TestCurrentHeadsAreScopedByProjectAndExecutionProfile(t *testing.T) {
	firstPolicy := validResolvedPolicy()
	secondPolicy := validResolvedPolicy()
	secondPolicy.ProjectID = uuid.MustParse("70000000-0000-4000-8000-000000000001")
	secondPolicy.ExecutionProfile.Hash = testDigest("second-execution-profile")
	source := &fakePolicySource{values: map[string]ResolvedPolicy{
		"reviewed-release-first":  firstPolicy,
		"reviewed-release-second": secondPolicy,
	}}
	store := NewMemoryStore()
	service, err := NewService(source, store, DatabaseClockFunc(func(context.Context) (time.Time, error) {
		return fixedDatabaseTime, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	firstCommand := IssueCommand{
		OperationID:    uuid.MustParse("70000000-0000-4000-8000-000000000002"),
		AuthorityID:    uuid.MustParse("70000000-0000-4000-8000-000000000003"),
		PolicySourceID: "reviewed-release-first",
	}
	secondCommand := IssueCommand{
		OperationID:    uuid.MustParse("70000000-0000-4000-8000-000000000004"),
		AuthorityID:    uuid.MustParse("70000000-0000-4000-8000-000000000005"),
		PolicySourceID: "reviewed-release-second",
	}
	first, err := service.Issue(context.Background(), firstCommand)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Issue(context.Background(), secondCommand)
	if err != nil {
		t.Fatal(err)
	}
	if first.Document.Generation != 1 || second.Document.Generation != 1 {
		t.Fatalf("independent heads did not both start at generation one: %d %d", first.Document.Generation, second.Document.Generation)
	}
	for _, authorityID := range []uuid.UUID{first.Command.AuthorityID, second.Command.AuthorityID} {
		if _, err := service.AssertCurrent(context.Background(), authorityID); err != nil {
			t.Fatalf("scoped head %s is not current: %v", authorityID, err)
		}
	}
}

func TestMemoryStoreRejectsBackwardDatabaseAuthorityTime(t *testing.T) {
	command := validIssueCommand()
	source := &fakePolicySource{values: map[string]ResolvedPolicy{
		command.PolicySourceID: validResolvedPolicy(),
	}}
	store := NewMemoryStore()
	var calls atomic.Int64
	service, err := NewService(source, store, DatabaseClockFunc(func(context.Context) (time.Time, error) {
		if calls.Add(1) == 1 {
			return fixedDatabaseTime, nil
		}
		return fixedDatabaseTime.Add(-time.Millisecond), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Issue(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	secondCommand := IssueCommand{
		OperationID:                   uuid.MustParse("70000000-0000-4000-8000-000000000011"),
		AuthorityID:                   uuid.MustParse("70000000-0000-4000-8000-000000000012"),
		PolicySourceID:                command.PolicySourceID,
		ExpectedPreviousAuthorityHash: first.AuthorityHash,
	}
	if _, err := service.Issue(context.Background(), secondCommand); !errors.Is(err, ErrConflict) {
		t.Fatalf("backward authority time error = %v", err)
	}
}

func TestConcurrentExactReplayConverges(t *testing.T) {
	service, _, _ := newTestService(t)
	command := validIssueCommand()
	const writers = 32
	results := make(chan Record, writers)
	errorsChannel := make(chan error, writers)
	var wait sync.WaitGroup
	wait.Add(writers)
	for range writers {
		go func() {
			defer wait.Done()
			record, err := service.Issue(context.Background(), command)
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
		t.Fatalf("concurrent exact Issue() error = %v", err)
	}
	var authorityHash string
	nonIdempotent := 0
	count := 0
	for record := range results {
		count++
		if authorityHash == "" {
			authorityHash = record.AuthorityHash
		}
		if record.AuthorityHash != authorityHash {
			t.Fatalf("exact concurrent operation diverged: %s != %s", record.AuthorityHash, authorityHash)
		}
		if !record.Idempotent {
			nonIdempotent++
		}
	}
	if count != writers || nonIdempotent != 1 {
		t.Fatalf("concurrent exact results = count:%d original:%d", count, nonIdempotent)
	}
}

func TestConcurrentCASHasOneWinner(t *testing.T) {
	service, _, _ := newTestService(t)
	first, err := service.Issue(context.Background(), validIssueCommand())
	if err != nil {
		t.Fatal(err)
	}
	const writers = 24
	var successes atomic.Int64
	var conflicts atomic.Int64
	var wait sync.WaitGroup
	wait.Add(writers)
	for index := range writers {
		go func(index int) {
			defer wait.Done()
			command := IssueCommand{
				OperationID:                   uuid.MustParse(fmt.Sprintf("40000000-0000-4000-8000-%012d", index*2+1)),
				AuthorityID:                   uuid.MustParse(fmt.Sprintf("40000000-0000-4000-8000-%012d", index*2+2)),
				PolicySourceID:                validIssueCommand().PolicySourceID,
				ExpectedPreviousAuthorityHash: first.AuthorityHash,
			}
			_, err := service.Issue(context.Background(), command)
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, ErrConflict):
				conflicts.Add(1)
			default:
				t.Errorf("concurrent CAS error = %v", err)
			}
		}(index)
	}
	wait.Wait()
	if successes.Load() != 1 || conflicts.Load() != writers-1 {
		t.Fatalf("CAS results = successes:%d conflicts:%d", successes.Load(), conflicts.Load())
	}
}

func TestIssueRecoversCommittedUnknownOutcome(t *testing.T) {
	service, store, _ := newTestService(t)
	store.InjectCommitUnknownOnce()
	record, err := service.Issue(context.Background(), validIssueCommand())
	if err != nil || !record.Idempotent {
		t.Fatalf("commit-unknown recovery = record:%+v error:%v", record, err)
	}
	inspected, err := service.InspectOperation(context.Background(), validIssueCommand().OperationID)
	if err != nil || !sameImmutableRecord(record, inspected) {
		t.Fatalf("recovered operation inspection = record:%+v error:%v", inspected, err)
	}
}

type unknownWithoutCommitStore struct {
	*MemoryStore
}

func (store *unknownWithoutCommitStore) Issue(context.Context, Record) (Record, error) {
	return Record{}, ErrStoreOutcomeUnknown
}

func TestIssueReportsUnknownWhenCommitCannotBeInspected(t *testing.T) {
	source := &fakePolicySource{values: map[string]ResolvedPolicy{
		validIssueCommand().PolicySourceID: validResolvedPolicy(),
	}}
	store := &unknownWithoutCommitStore{MemoryStore: NewMemoryStore()}
	service, err := NewService(source, store, DatabaseClockFunc(func(context.Context) (time.Time, error) {
		return fixedDatabaseTime, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Issue(context.Background(), validIssueCommand()); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("uninspectable unknown outcome error = %v", err)
	}
}

type divergentUnknownStore struct {
	*MemoryStore
	alternate    Record
	inspectCalls atomic.Int64
}

func (store *divergentUnknownStore) Issue(context.Context, Record) (Record, error) {
	return Record{}, ErrStoreOutcomeUnknown
}

func (store *divergentUnknownStore) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	if store.inspectCalls.Add(1) == 1 {
		return Record{}, ErrNotFound
	}
	if operationID != store.alternate.Command.OperationID {
		return Record{}, ErrNotFound
	}
	return cloneRecord(store.alternate), nil
}

func TestIssueRejectsUnknownOutcomeResolvedToDifferentBytes(t *testing.T) {
	command := validIssueCommand()
	divergentPolicy := validResolvedPolicy()
	divergentPolicy.RevisionPolicy.ReviewByChangeSource[5].CanonicalReviewRequired = true
	alternate, err := compileRecord(command, divergentPolicy, 1, nil, fixedDatabaseTime)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakePolicySource{values: map[string]ResolvedPolicy{
		command.PolicySourceID: validResolvedPolicy(),
	}}
	store := &divergentUnknownStore{MemoryStore: NewMemoryStore(), alternate: alternate}
	service, err := NewService(source, store, DatabaseClockFunc(func(context.Context) (time.Time, error) {
		return fixedDatabaseTime, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Issue(context.Background(), command); !errors.Is(err, ErrConflict) {
		t.Fatalf("divergent unknown outcome error = %v", err)
	}
}

func TestMemoryStoreRejectsCrossKindIdentityReuse(t *testing.T) {
	service, _, _ := newTestService(t)
	first, err := service.Issue(context.Background(), validIssueCommand())
	if err != nil {
		t.Fatal(err)
	}
	for name, command := range map[string]IssueCommand{
		"old authority as operation": {
			OperationID:                   first.Command.AuthorityID,
			AuthorityID:                   uuid.MustParse("50000000-0000-4000-8000-000000000001"),
			PolicySourceID:                validIssueCommand().PolicySourceID,
			ExpectedPreviousAuthorityHash: first.AuthorityHash,
		},
		"old operation as authority": {
			OperationID:                   uuid.MustParse("50000000-0000-4000-8000-000000000002"),
			AuthorityID:                   first.Command.OperationID,
			PolicySourceID:                validIssueCommand().PolicySourceID,
			ExpectedPreviousAuthorityHash: first.AuthorityHash,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Issue(context.Background(), command); !errors.Is(err, ErrConflict) {
				t.Fatalf("identity collision error = %v", err)
			}
		})
	}
}

func TestMemoryStoreRejectsLocalIdentityAndEmbeddedReferenceReuseAcrossAuthorities(t *testing.T) {
	service, _, source := newTestService(t)
	first, err := service.Issue(context.Background(), validIssueCommand())
	if err != nil {
		t.Fatal(err)
	}
	retainedArtifactID := uuid.MustParse(first.RevisionPolicy.ExactApprovedSources[0].ArtifactID)

	source.mu.Lock()
	withoutOverride := source.values[validIssueCommand().PolicySourceID]
	withoutOverride.RevisionPolicy.ExactApprovedSources = []ExactApprovedSource{}
	source.values[validIssueCommand().PolicySourceID] = withoutOverride
	source.mu.Unlock()
	localReusesReference := IssueCommand{
		OperationID:                   uuid.MustParse("60000000-0000-4000-8000-000000000001"),
		AuthorityID:                   retainedArtifactID,
		PolicySourceID:                validIssueCommand().PolicySourceID,
		ExpectedPreviousAuthorityHash: first.AuthorityHash,
	}
	if _, err := service.Issue(context.Background(), localReusesReference); !errors.Is(err, ErrConflict) {
		t.Fatalf("local identity reuse of prior embedded artifact error = %v", err)
	}

	source.mu.Lock()
	localAsReference := validResolvedPolicy()
	localAsReference.RevisionPolicy.ExactApprovedSources[0].ArtifactID = first.Command.AuthorityID.String()
	source.values[validIssueCommand().PolicySourceID] = localAsReference
	source.mu.Unlock()
	referenceReusesLocal := IssueCommand{
		OperationID:                   uuid.MustParse("60000000-0000-4000-8000-000000000002"),
		AuthorityID:                   uuid.MustParse("60000000-0000-4000-8000-000000000003"),
		PolicySourceID:                validIssueCommand().PolicySourceID,
		ExpectedPreviousAuthorityHash: first.AuthorityHash,
	}
	if _, err := service.Issue(context.Background(), referenceReusesLocal); !errors.Is(err, ErrConflict) {
		t.Fatalf("embedded reference reuse of prior local authority error = %v", err)
	}
}
