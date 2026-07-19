package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type reviewStoreFake struct {
	attempt AgentAttempt
}

func (store reviewStoreFake) ResolveAttemptProject(context.Context, string) (string, error) {
	return store.attempt.ProjectID, nil
}

func (store reviewStoreFake) GetAttempt(context.Context, string, string) (AgentAttempt, error) {
	return store.attempt, nil
}

type finalizedEvidenceReaderFake struct {
	value     []byte
	kind      EvidenceKind
	reference BlobReference
}

func (reader *finalizedEvidenceReaderFake) GetFinalized(
	_ context.Context,
	_ AgentAttempt,
	kind EvidenceKind,
	reference BlobReference,
) ([]byte, error) {
	reader.kind, reader.reference = kind, reference
	return append([]byte(nil), reader.value...), nil
}

func TestReviewServiceReturnsOnlyAuthorizedCommittedEvidence(t *testing.T) {
	fixture := newAgentFixture(t)
	pack, err := NewContextPack(fixture.contextInput, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := NewTaskCapsule(fixture.taskInput, pack, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := NewAttempt(NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: fixture.actorID, Executor: testExecutor(),
	}, capsule, pack, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	reference := BlobReference{
		Store: AgentEvidenceStore, OwnerID: attempt.ID, Ref: uuid.NewString(),
		ContentHash: testHash("d"), ByteSize: 128,
	}
	attempt.Evidence.StructuredResult = &reference
	reader := &finalizedEvidenceReaderFake{value: []byte(`{"summary":"exact"}`)}
	access := &agentAccessFake{}
	service, err := NewReviewService(reviewStoreFake{attempt: attempt}, reader, access)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ReadEvidence(
		context.Background(), attempt.ID, fixture.actorID, EvidenceStructuredResult,
	)
	if err != nil || access.viewCalls != 1 || reader.kind != EvidenceStructuredResult ||
		reader.reference != reference || result.Reference != reference || result.RawHash != rawEvidenceHash(reader.value) {
		t.Fatalf("review result=%#v reader=%#v access=%#v err=%v", result, reader, access, err)
	}
	if _, err := service.ReadEvidence(
		context.Background(), attempt.ID, fixture.actorID, EvidencePatch,
	); !errors.Is(err, ErrEvidenceUnavailable) {
		t.Fatalf("missing Patch evidence error = %v", err)
	}

	reader.value = []byte(`not-json`)
	if _, err := service.ReadEvidence(
		context.Background(), attempt.ID, fixture.actorID, EvidenceStructuredResult,
	); !errors.Is(err, ErrEvidenceIntegrity) {
		t.Fatalf("invalid structured evidence error = %v", err)
	}
}
