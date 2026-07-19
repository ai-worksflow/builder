package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
)

type treeContentPut struct {
	projectID     string
	aggregateType string
	aggregateID   string
	schemaVersion int
	payload       json.RawMessage
}

type fakeTreeContentStore struct {
	items              map[string]content.StoredContent
	nextID             int
	puts               []treeContentPut
	finalized          []string
	aborted            []string
	bypassGetHashCheck bool
	mutatePutReference func(content.Reference) content.Reference
	finalizeErr        error
	abortErr           error
}

func newFakeTreeContentStore() *fakeTreeContentStore {
	return &fakeTreeContentStore{items: make(map[string]content.StoredContent)}
}

func (store *fakeTreeContentStore) PutPending(
	_ context.Context,
	projectID, aggregateType, aggregateID string,
	schemaVersion int,
	payload json.RawMessage,
) (content.Reference, error) {
	store.nextID++
	id := fmt.Sprintf("tree-object-%d", store.nextID)
	cloned := append(json.RawMessage(nil), payload...)
	reference := content.Reference{
		ID: id, ContentHash: treeContentHash(cloned), ByteSize: int64(len(cloned)), SchemaVersion: schemaVersion,
	}
	stored := content.StoredContent{
		Reference: reference, ProjectID: projectID, AggregateType: aggregateType, AggregateID: aggregateID,
		State: content.StatePending, Payload: cloned, CreatedAt: time.Now().UTC(),
	}
	store.items[id] = stored
	store.puts = append(store.puts, treeContentPut{
		projectID: projectID, aggregateType: aggregateType, aggregateID: aggregateID,
		schemaVersion: schemaVersion, payload: append(json.RawMessage(nil), payload...),
	})
	if store.mutatePutReference != nil {
		return store.mutatePutReference(reference), nil
	}
	return reference, nil
}

func (store *fakeTreeContentStore) Finalize(_ context.Context, id string) error {
	if store.finalizeErr != nil {
		return store.finalizeErr
	}
	item, exists := store.items[id]
	if !exists || item.State == content.StateAborted {
		return content.ErrContentNotFound
	}
	now := time.Now().UTC()
	item.State = content.StateFinalized
	item.FinalizedAt = &now
	store.items[id] = item
	store.finalized = append(store.finalized, id)
	return nil
}

func (store *fakeTreeContentStore) Abort(_ context.Context, id string) error {
	store.aborted = append(store.aborted, id)
	if store.abortErr != nil {
		return store.abortErr
	}
	item, exists := store.items[id]
	if !exists || item.State != content.StatePending {
		return content.ErrContentNotFound
	}
	item.State = content.StateAborted
	store.items[id] = item
	return nil
}

func (store *fakeTreeContentStore) Get(
	_ context.Context,
	id, expectedHash string,
) (content.StoredContent, error) {
	item, exists := store.items[id]
	if !exists || (item.State == content.StateAborted && !store.bypassGetHashCheck) {
		return content.StoredContent{}, content.ErrContentNotFound
	}
	if !store.bypassGetHashCheck {
		actualHash := treeContentHash(item.Payload)
		if actualHash != item.ContentHash || (expectedHash != "" && actualHash != expectedHash) {
			return content.StoredContent{}, content.ErrHashMismatch
		}
	}
	item.Payload = append(json.RawMessage(nil), item.Payload...)
	return item, nil
}

func TestTreeStorePutPendingGetFinalizeAndAbort(t *testing.T) {
	ctx := context.Background()
	projectID, candidateID := uuid.NewString(), uuid.NewString()
	manifest := treeStoreFixture(t)
	contents := newFakeTreeContentStore()
	store, err := NewTreeStore(contents)
	if err != nil {
		t.Fatalf("construct tree store: %v", err)
	}

	pointer, err := store.PutPending(ctx, projectID, candidateID, manifest)
	if err != nil {
		t.Fatalf("put pending tree: %v", err)
	}
	wantPayload, err := domain.CanonicalJSON(manifest)
	if err != nil {
		t.Fatalf("canonical tree fixture: %v", err)
	}
	if len(contents.puts) != 1 {
		t.Fatalf("put calls = %d, want 1", len(contents.puts))
	}
	put := contents.puts[0]
	if put.projectID != projectID || put.aggregateType != TreeContentAggregateType ||
		put.aggregateID != candidateID || put.schemaVersion != TreeContentSchemaVersion ||
		!bytes.Equal(put.payload, wantPayload) {
		t.Fatalf("unexpected content binding or payload: %#v payload=%s", put, put.payload)
	}
	if pointer.Store != TreeContentStore || pointer.Ref == "" || pointer.OwnerID != candidateID || pointer.TreeHash != manifest.TreeHash ||
		pointer.FileCount != len(manifest.Files) || pointer.ByteSize != treeByteSize(manifest) ||
		pointer.ContentObjectHash != treeContentHash(wantPayload) {
		t.Fatalf("unexpected tree pointer: %#v", pointer)
	}

	// Pending reads are intentional: SQL may have committed this pointer before
	// the process had a chance to finalize the content object.
	pending, err := store.Get(ctx, projectID, candidateID, pointer)
	if err != nil {
		t.Fatalf("get SQL-referenced pending tree: %v", err)
	}
	if !reflect.DeepEqual(pending, manifest) {
		t.Fatalf("pending tree mismatch: got %#v want %#v", pending, manifest)
	}

	if err := store.Finalize(ctx, projectID, candidateID, pointer); err != nil {
		t.Fatalf("finalize tree: %v", err)
	}
	if err := store.Finalize(ctx, projectID, candidateID, pointer); err != nil {
		t.Fatalf("idempotent finalize tree: %v", err)
	}
	if contents.items[pointer.Ref].State != content.StateFinalized || len(contents.finalized) != 2 {
		t.Fatalf("tree was not finalized idempotently: %#v", contents.items[pointer.Ref])
	}
	if _, err := store.Get(ctx, projectID, candidateID, pointer); err != nil {
		t.Fatalf("get finalized tree: %v", err)
	}

	abortPointer, err := store.PutPending(ctx, projectID, candidateID, manifest)
	if err != nil {
		t.Fatalf("put tree to abort: %v", err)
	}
	if err := store.Abort(ctx, projectID, candidateID, abortPointer); err != nil {
		t.Fatalf("abort tree: %v", err)
	}
	if contents.items[abortPointer.Ref].State != content.StateAborted {
		t.Fatalf("tree was not aborted: %#v", contents.items[abortPointer.Ref])
	}
	if _, err := store.Get(ctx, projectID, candidateID, abortPointer); !errors.Is(err, content.ErrContentNotFound) {
		t.Fatalf("aborted tree remained readable: %v", err)
	}
}

func TestTreeStoreRejectsWrongPointerAndAggregateIdentity(t *testing.T) {
	ctx := context.Background()
	projectID, candidateID := uuid.NewString(), uuid.NewString()
	otherProjectID, otherCandidateID := uuid.NewString(), uuid.NewString()
	contents := newFakeTreeContentStore()
	store, _ := NewTreeStore(contents)
	pointer, err := store.PutPending(ctx, projectID, candidateID, treeStoreFixture(t))
	if err != nil {
		t.Fatalf("put pending tree: %v", err)
	}
	otherPointer, err := store.PutPending(ctx, projectID, otherCandidateID, treeStoreFixture(t))
	if err != nil {
		t.Fatalf("put other pending tree: %v", err)
	}

	for name, test := range map[string]struct {
		projectID   string
		candidateID string
		pointer     TreeBlobPointer
		want        error
	}{
		"wrong project":   {otherProjectID, candidateID, pointer, ErrTreeBlobIntegrity},
		"wrong candidate": {projectID, otherCandidateID, pointer, ErrTreeBlobIntegrity},
		"wrong valid ref": {projectID, candidateID, otherPointer, ErrTreeBlobIntegrity},
		"missing ref": {
			projectID, candidateID, replaceTreePointer(pointer, func(value *TreeBlobPointer) { value.Ref = "missing" }),
			content.ErrContentNotFound,
		},
		"wrong store": {
			projectID, candidateID, replaceTreePointer(pointer, func(value *TreeBlobPointer) { value.Store = "other" }),
			ErrInvalidTreePointer,
		},
		"blank ref": {
			projectID, candidateID, replaceTreePointer(pointer, func(value *TreeBlobPointer) { value.Ref = " " }),
			ErrInvalidTreePointer,
		},
		"invalid project":   {"not-a-uuid", candidateID, pointer, ErrInvalidCandidate},
		"invalid candidate": {projectID, "not-a-uuid", pointer, ErrInvalidCandidate},
	} {
		t.Run(name, func(t *testing.T) {
			_, getErr := store.Get(ctx, test.projectID, test.candidateID, test.pointer)
			if !errors.Is(getErr, test.want) {
				t.Fatalf("Get error = %v, want errors.Is(%v)", getErr, test.want)
			}
		})
	}
}

func TestTreeStoreRejectsPointerFactDrift(t *testing.T) {
	ctx := context.Background()
	projectID, candidateID := uuid.NewString(), uuid.NewString()
	contents := newFakeTreeContentStore()
	store, _ := NewTreeStore(contents)
	pointer, err := store.PutPending(ctx, projectID, candidateID, treeStoreFixture(t))
	if err != nil {
		t.Fatalf("put pending tree: %v", err)
	}
	otherHash := "sha256:" + string(bytes.Repeat([]byte{'f'}, 64))

	for name, mutated := range map[string]TreeBlobPointer{
		"owner id":    replaceTreePointer(pointer, func(value *TreeBlobPointer) { value.OwnerID = uuid.NewString() }),
		"tree hash":   replaceTreePointer(pointer, func(value *TreeBlobPointer) { value.TreeHash = otherHash }),
		"file count":  replaceTreePointer(pointer, func(value *TreeBlobPointer) { value.FileCount++ }),
		"byte size":   replaceTreePointer(pointer, func(value *TreeBlobPointer) { value.ByteSize++ }),
		"object hash": replaceTreePointer(pointer, func(value *TreeBlobPointer) { value.ContentObjectHash = otherHash }),
	} {
		t.Run(name, func(t *testing.T) {
			_, getErr := store.Get(ctx, projectID, candidateID, mutated)
			if getErr == nil {
				t.Fatal("Get accepted a drifted tree pointer")
			}
		})
	}

	invalidHash := replaceTreePointer(pointer, func(value *TreeBlobPointer) { value.TreeHash = "ABC" })
	if _, err := store.Get(ctx, projectID, candidateID, invalidHash); !errors.Is(err, ErrInvalidTreePointer) {
		t.Fatalf("non-canonical hash was not rejected as an invalid pointer: %v", err)
	}
}

func TestTreeStoreRejectsStoredContentMetadataAndHashTampering(t *testing.T) {
	ctx := context.Background()
	projectID, candidateID := uuid.NewString(), uuid.NewString()
	manifest := treeStoreFixture(t)

	for name, mutate := range map[string]func(*content.StoredContent){
		"project":        func(item *content.StoredContent) { item.ProjectID = uuid.NewString() },
		"aggregate type": func(item *content.StoredContent) { item.AggregateType = "other" },
		"aggregate id":   func(item *content.StoredContent) { item.AggregateID = uuid.NewString() },
		"schema":         func(item *content.StoredContent) { item.SchemaVersion++ },
		"ref":            func(item *content.StoredContent) { item.ID = "different-ref" },
		"state":          func(item *content.StoredContent) { item.State = content.StateAborted },
		"object size":    func(item *content.StoredContent) { item.ByteSize++ },
		"stored hash":    func(item *content.StoredContent) { item.ContentHash = digestFixture("other-object") },
	} {
		t.Run(name, func(t *testing.T) {
			contents := newFakeTreeContentStore()
			contents.bypassGetHashCheck = true
			store, _ := NewTreeStore(contents)
			pointer, err := store.PutPending(ctx, projectID, candidateID, manifest)
			if err != nil {
				t.Fatalf("put pending tree: %v", err)
			}
			item := contents.items[pointer.Ref]
			mutate(&item)
			contents.items[pointer.Ref] = item
			if _, err := store.Get(ctx, projectID, candidateID, pointer); !errors.Is(err, ErrTreeBlobIntegrity) {
				t.Fatalf("tampered content error = %v, want integrity error", err)
			}
		})
	}

	contents := newFakeTreeContentStore()
	store, _ := NewTreeStore(contents)
	pointer, err := store.PutPending(ctx, projectID, candidateID, manifest)
	if err != nil {
		t.Fatalf("put pending tree: %v", err)
	}
	item := contents.items[pointer.Ref]
	item.Payload[0] = '['
	contents.items[pointer.Ref] = item
	if _, err := store.Get(ctx, projectID, candidateID, pointer); !errors.Is(err, content.ErrHashMismatch) {
		t.Fatalf("payload tampering error = %v, want content hash mismatch", err)
	}
}

func TestTreeStoreRejectsNonCanonicalOrSemanticallyDriftedPayload(t *testing.T) {
	ctx := context.Background()
	projectID, candidateID := uuid.NewString(), uuid.NewString()

	tests := map[string]func(TreeManifest, []byte) []byte{
		"non-canonical encoding": func(_ TreeManifest, canonical []byte) []byte {
			return append([]byte(" \n"), canonical...)
		},
		"unknown field": func(_ TreeManifest, canonical []byte) []byte {
			var value map[string]any
			if err := json.Unmarshal(canonical, &value); err != nil {
				t.Fatalf("decode canonical payload: %v", err)
			}
			value["unexpected"] = true
			payload, _ := domain.CanonicalJSON(value)
			return payload
		},
		"semantic tree change": func(manifest TreeManifest, _ []byte) []byte {
			changed := append([]TreeFile(nil), manifest.Files...)
			changed[0].ByteSize++
			tree, err := NewTree(changed)
			if err != nil {
				t.Fatalf("build changed tree: %v", err)
			}
			payload, _ := domain.CanonicalJSON(tree)
			return payload
		},
		"semantic schema change": func(manifest TreeManifest, _ []byte) []byte {
			manifest.SchemaVersion = "repository-tree/v0"
			payload, _ := domain.CanonicalJSON(manifest)
			return payload
		},
	}

	for name, payloadFor := range tests {
		t.Run(name, func(t *testing.T) {
			manifest := treeStoreFixture(t)
			contents := newFakeTreeContentStore()
			store, _ := NewTreeStore(contents)
			pointer, err := store.PutPending(ctx, projectID, candidateID, manifest)
			if err != nil {
				t.Fatalf("put pending tree: %v", err)
			}
			original := contents.items[pointer.Ref]
			payload := payloadFor(manifest, original.Payload)
			original.Payload = append(json.RawMessage(nil), payload...)
			original.ContentHash = treeContentHash(payload)
			original.ByteSize = int64(len(payload))
			contents.items[pointer.Ref] = original
			pointer.ContentObjectHash = original.ContentHash

			if _, err := store.Get(ctx, projectID, candidateID, pointer); !errors.Is(err, ErrTreeBlobIntegrity) {
				t.Fatalf("payload drift error = %v, want integrity error", err)
			}
		})
	}
}

func TestTreeStoreRejectsInvalidWritesAndMalformedStoreReference(t *testing.T) {
	ctx := context.Background()
	projectID, candidateID := uuid.NewString(), uuid.NewString()
	contents := newFakeTreeContentStore()
	store, _ := NewTreeStore(contents)
	manifest := treeStoreFixture(t)

	invalid := manifest
	invalid.TreeHash = digestFixture("forged-tree")
	if _, err := store.PutPending(ctx, projectID, candidateID, invalid); !errors.Is(err, ErrInvalidTree) {
		t.Fatalf("invalid tree write error = %v, want invalid tree", err)
	}
	if _, err := store.PutPending(ctx, "bad-project", candidateID, manifest); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("invalid aggregate write error = %v, want invalid candidate", err)
	}
	if len(contents.puts) != 0 {
		t.Fatalf("invalid write reached content store %d times", len(contents.puts))
	}

	contents.mutatePutReference = func(reference content.Reference) content.Reference {
		reference.ContentHash = digestFixture("wrong-returned-hash")
		return reference
	}
	if _, err := store.PutPending(ctx, projectID, candidateID, manifest); !errors.Is(err, ErrTreeBlobIntegrity) {
		t.Fatalf("malformed content reference error = %v, want integrity error", err)
	}
	if len(contents.aborted) != 1 || contents.aborted[0] != "tree-object-1" {
		t.Fatalf("malformed pending object was not aborted: %#v", contents.aborted)
	}
}

func TestTreeStoreLifecyclePropagatesUnderlyingFailures(t *testing.T) {
	ctx := context.Background()
	projectID, candidateID := uuid.NewString(), uuid.NewString()
	contents := newFakeTreeContentStore()
	store, _ := NewTreeStore(contents)
	pointer, err := store.PutPending(ctx, projectID, candidateID, treeStoreFixture(t))
	if err != nil {
		t.Fatalf("put pending tree: %v", err)
	}

	contents.finalizeErr = errors.New("finalize unavailable")
	if err := store.Finalize(ctx, projectID, candidateID, pointer); err == nil || err.Error() != "finalize repository tree: finalize unavailable" {
		t.Fatalf("finalize error was not preserved: %v", err)
	}
	contents.finalizeErr = nil
	contents.abortErr = errors.New("abort unavailable")
	if err := store.Abort(ctx, projectID, candidateID, pointer); err == nil || err.Error() != "abort repository tree: abort unavailable" {
		t.Fatalf("abort error was not preserved: %v", err)
	}
}

func TestNewTreeStoreRequiresContentStore(t *testing.T) {
	if _, err := NewTreeStore(nil); err == nil {
		t.Fatal("NewTreeStore accepted a nil content store")
	}
}

func treeStoreFixture(t *testing.T) TreeManifest {
	t.Helper()
	tree, err := NewTree([]TreeFile{
		{Path: "services/api/main.go", Mode: "100644", ContentHash: digestFixture("api"), ByteSize: 19},
		{Path: "apps/web/page.tsx", Mode: "100755", ContentHash: digestFixture("web"), ByteSize: 23},
	})
	if err != nil {
		t.Fatalf("build tree fixture: %v", err)
	}
	return tree
}

func digestFixture(label string) string {
	return treeContentHash([]byte(label))
}

func replaceTreePointer(pointer TreeBlobPointer, replace func(*TreeBlobPointer)) TreeBlobPointer {
	replace(&pointer)
	return pointer
}
