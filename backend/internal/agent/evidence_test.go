package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/storage/content"
)

type evidenceContentStoreFake struct {
	items map[string]content.StoredContent
}

func newEvidenceContentStoreFake() *evidenceContentStoreFake {
	return &evidenceContentStoreFake{items: map[string]content.StoredContent{}}
}

func (store *evidenceContentStoreFake) PutPending(
	_ context.Context,
	projectID, aggregateType, aggregateID string,
	schemaVersion int,
	payload json.RawMessage,
) (content.Reference, error) {
	id := uuid.NewString()
	reference := content.Reference{
		ID: id, ContentHash: rawEvidenceHash(payload), ByteSize: int64(len(payload)),
		SchemaVersion: schemaVersion,
	}
	store.items[id] = content.StoredContent{
		Reference: reference, ProjectID: projectID, AggregateType: aggregateType,
		AggregateID: aggregateID, State: content.StatePending,
		Payload: append(json.RawMessage(nil), payload...), CreatedAt: time.Now().UTC(),
	}
	return reference, nil
}

func (store *evidenceContentStoreFake) Finalize(_ context.Context, id string) error {
	stored, ok := store.items[id]
	if !ok {
		return content.ErrContentNotFound
	}
	stored.State = content.StateFinalized
	store.items[id] = stored
	return nil
}

func (store *evidenceContentStoreFake) Abort(_ context.Context, id string) error {
	stored, ok := store.items[id]
	if !ok || stored.State != content.StatePending {
		return content.ErrContentNotFound
	}
	stored.State = content.StateAborted
	store.items[id] = stored
	return nil
}

func (store *evidenceContentStoreFake) Get(
	_ context.Context,
	id, expectedHash string,
) (content.StoredContent, error) {
	stored, ok := store.items[id]
	if !ok || stored.State == content.StateAborted {
		return content.StoredContent{}, content.ErrContentNotFound
	}
	if expectedHash != "" && expectedHash != stored.ContentHash {
		return content.StoredContent{}, content.ErrHashMismatch
	}
	stored.Payload = append(json.RawMessage(nil), stored.Payload...)
	return stored, nil
}

func TestEvidenceStoreRoundTripsBoundExactAttemptEnvelope(t *testing.T) {
	contents := newEvidenceContentStoreFake()
	store, err := NewEvidenceStore(contents)
	if err != nil {
		t.Fatal(err)
	}
	attempt := AgentAttempt{ID: uuid.NewString(), ProjectID: uuid.NewString()}
	value := []byte(`{"schemaVersion":"agent-platform-patch/v1","operations":[]}`)
	reference, err := store.PutPending(
		context.Background(), attempt, EvidencePatch, "application/json", value,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reference.Store != AgentEvidenceStore || reference.OwnerID != attempt.ID ||
		reference.Ref == "" || reference.ContentHash == rawEvidenceHash(value) {
		t.Fatalf("evidence reference = %#v", reference)
	}
	loaded, err := store.Get(context.Background(), attempt, EvidencePatch, reference)
	if err != nil || string(loaded) != string(value) {
		t.Fatalf("loaded=%q err=%v", loaded, err)
	}
	if err := store.Finalize(context.Background(), attempt, EvidencePatch, reference); err != nil {
		t.Fatal(err)
	}
	if contents.items[reference.Ref].State != content.StateFinalized {
		t.Fatal("evidence was not finalized")
	}
	if _, err := store.Get(
		context.Background(),
		AgentAttempt{ID: uuid.NewString(), ProjectID: attempt.ProjectID},
		EvidencePatch,
		reference,
	); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("cross-Attempt evidence error = %v", err)
	}
}

func TestEvidenceStoreRejectsTamperingKindAndOversize(t *testing.T) {
	contents := newEvidenceContentStoreFake()
	store, _ := NewEvidenceStore(contents)
	attempt := AgentAttempt{ID: uuid.NewString(), ProjectID: uuid.NewString()}
	reference, err := store.PutPending(
		context.Background(), attempt, EvidenceStdout, "application/x-ndjson", []byte("one\n"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(
		context.Background(), attempt, EvidenceStderr, reference,
	); !errors.Is(err, ErrEvidenceIntegrity) {
		t.Fatalf("kind drift error = %v", err)
	}
	stored := contents.items[reference.Ref]
	stored.Payload[0] ^= 1
	contents.items[reference.Ref] = stored
	if _, err := store.Get(
		context.Background(), attempt, EvidenceStdout, reference,
	); !errors.Is(err, ErrEvidenceIntegrity) {
		t.Fatalf("tamper error = %v", err)
	}
	if _, err := store.PutPending(
		context.Background(), attempt, EvidenceStdout, "text/plain", make([]byte, maxAgentEvidenceBytes),
	); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("oversize error = %v", err)
	}
}
