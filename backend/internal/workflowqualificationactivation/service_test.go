package workflowqualificationactivation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/worksflow/builder/backend/internal/workflowinputauthority"
)

const testCompletionEventID = "10101010-1010-4010-8010-101010101010"

type resolverFake struct {
	resolution Resolution
	err        error
	calls      int
	id         CompletionEventID
}

func (resolver *resolverFake) Resolve(_ context.Context, id CompletionEventID) (Resolution, error) {
	resolver.calls++
	resolver.id = id
	return resolver.resolution, resolver.err
}

type atomicStoreFake struct {
	activateCalls int
	inspectCalls  int
}

func (store *atomicStoreFake) Activate(
	context.Context,
	CompletionEventID,
	workflowinputauthority.Candidate,
) (Record, error) {
	store.activateCalls++
	return Record{}, errors.New("unexpected target activation")
}

func (store *atomicStoreFake) Inspect(
	context.Context,
	CompletionEventID,
	workflowinputauthority.Candidate,
) (Record, error) {
	store.inspectCalls++
	return Record{}, errors.New("unexpected target inspection")
}

func TestCompletionEventIDIsStrictOpaqueUUIDv4(t *testing.T) {
	id, err := ParseCompletionEventID(testCompletionEventID)
	if err != nil || id.String() != testCompletionEventID {
		t.Fatalf("ParseCompletionEventID() = %q, %v", id.String(), err)
	}
	for _, value := range []string{
		"", strings.ToUpper("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		"10101010-1010-1010-8010-101010101010",
		"10101010-1010-4010-7010-101010101010",
		" 10101010-1010-4010-8010-101010101010",
	} {
		if _, err := ParseCompletionEventID(value); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid completionEventId %q error = %v", value, err)
		}
	}
}

func TestServiceAcknowledgesResolverClassifiedNonTargetWithoutStoreAccess(t *testing.T) {
	id, _ := ParseCompletionEventID(testCompletionEventID)
	resolver := &resolverFake{resolution: Resolution{Classification: ClassificationNonTarget}}
	store := &atomicStoreFake{}
	service, err := NewService(resolver, store)
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Activate(context.Background(), id)
	if err != nil || record != ignoredRecord(id) {
		t.Fatalf("Activate() = %#v, %v", record, err)
	}
	inspected, err := service.Inspect(context.Background(), id)
	if err != nil || inspected != ignoredRecord(id) {
		t.Fatalf("Inspect() = %#v, %v", inspected, err)
	}
	if resolver.calls != 2 || resolver.id != id || store.activateCalls != 0 || store.inspectCalls != 0 {
		t.Fatalf("resolver calls=%d id=%q store activate=%d inspect=%d",
			resolver.calls, resolver.id.String(), store.activateCalls, store.inspectCalls)
	}
}

func TestServiceRejectsNonTargetResolutionThatSmugglesAuthorityFacts(t *testing.T) {
	id, _ := ParseCompletionEventID(testCompletionEventID)
	resolver := &resolverFake{resolution: Resolution{
		Classification: ClassificationNonTarget,
		Candidate: workflowinputauthority.Candidate{Request: workflowinputauthority.FreezeRequest{
			OperationID: "20202020-2020-4020-8020-202020202020",
		}},
	}}
	store := &atomicStoreFake{}
	service, err := NewService(resolver, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(context.Background(), id); !errors.Is(err, ErrConflict) {
		t.Fatalf("Activate() error = %v", err)
	}
	if store.activateCalls != 0 {
		t.Fatal("smuggled non-target resolution reached the store")
	}
}

func TestServiceSanitizesUnexpectedResolverDiagnostics(t *testing.T) {
	id, _ := ParseCompletionEventID(testCompletionEventID)
	service, err := NewService(&resolverFake{err: errors.New("password=secret /root/key")}, &atomicStoreFake{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Activate(context.Background(), id)
	if !errors.Is(err, ErrOutcomeUnknown) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/root") {
		t.Fatalf("Activate() leaked resolver diagnostics: %v", err)
	}
}

func TestNewServiceRejectsTypedNilDependencies(t *testing.T) {
	var resolver *resolverFake
	var store *atomicStoreFake
	if _, err := NewService(resolver, &atomicStoreFake{}); err == nil {
		t.Fatal("typed nil Resolver was accepted")
	}
	if _, err := NewService(&resolverFake{}, store); err == nil {
		t.Fatal("typed nil AtomicStore was accepted")
	}
}
