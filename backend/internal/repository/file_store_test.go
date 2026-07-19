package repository

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
)

func TestFileStoreDerivesBytesHashAndSupportsLifecycle(t *testing.T) {
	ctx := context.Background()
	projectID, ownerID := uuid.NewString(), uuid.NewString()
	value := []byte{0, 1, 2, '\n', 255, 'a', 'b'}
	contents := newFakeTreeContentStore()
	store, err := NewFileStore(contents)
	if err != nil {
		t.Fatal(err)
	}

	pointer, err := store.PutPending(ctx, projectID, ownerID, value)
	if err != nil {
		t.Fatalf("put pending file: %v", err)
	}
	if pointer.Store != FileContentStore || pointer.Ref == "" || pointer.OwnerID != ownerID ||
		pointer.ContentHash != rawFileContentHash(value) || pointer.ByteSize != int64(len(value)) ||
		pointer.ContentObjectHash == pointer.ContentHash {
		t.Fatalf("unexpected file pointer: %#v", pointer)
	}
	if len(contents.puts) != 1 || contents.puts[0].aggregateType != FileContentAggregateType ||
		contents.puts[0].aggregateID != ownerID || contents.puts[0].schemaVersion != FileContentSchemaVersion {
		t.Fatalf("unexpected file content binding: %#v", contents.puts)
	}

	pending, err := store.Get(ctx, projectID, ownerID, pointer)
	if err != nil {
		t.Fatalf("get pending file: %v", err)
	}
	if !bytes.Equal(pending, value) {
		t.Fatalf("pending bytes = %#v, want %#v", pending, value)
	}
	pending[0] = 42
	again, err := store.Get(ctx, projectID, ownerID, pointer)
	if err != nil || !bytes.Equal(again, value) {
		t.Fatalf("Get leaked mutable bytes: value=%#v err=%v", again, err)
	}

	if err := store.Finalize(ctx, projectID, ownerID, pointer); err != nil {
		t.Fatalf("finalize file: %v", err)
	}
	if err := store.Finalize(ctx, projectID, ownerID, pointer); err != nil {
		t.Fatalf("idempotent finalize file: %v", err)
	}
	if contents.items[pointer.Ref].State != content.StateFinalized {
		t.Fatalf("file was not finalized: %#v", contents.items[pointer.Ref])
	}

	abortPointer, err := store.PutPending(ctx, projectID, ownerID, []byte("discard"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Abort(ctx, projectID, ownerID, abortPointer); err != nil {
		t.Fatalf("abort file: %v", err)
	}
	if _, err := store.Get(ctx, projectID, ownerID, abortPointer); !errors.Is(err, content.ErrContentNotFound) {
		t.Fatalf("aborted file remained readable: %v", err)
	}
}

func TestFileStoreRejectsOversizeIdentityAndPointerDrift(t *testing.T) {
	ctx := context.Background()
	projectID, ownerID := uuid.NewString(), uuid.NewString()
	contents := newFakeTreeContentStore()
	store, _ := NewFileStore(contents)
	if _, err := store.PutPending(ctx, projectID, ownerID, make([]byte, MaxFileBytes+1)); !errors.Is(err, ErrTreeLimit) {
		t.Fatalf("oversize error = %v, want tree limit", err)
	}
	if _, err := store.PutPending(ctx, "bad-project", ownerID, nil); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("invalid project error = %v", err)
	}
	pointer, err := store.PutPending(ctx, projectID, ownerID, []byte("exact"))
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		projectID string
		ownerID   string
		pointer   FileBlobPointer
	}{
		"project": {projectID: uuid.NewString(), ownerID: ownerID, pointer: pointer},
		"owner":   {projectID: projectID, ownerID: uuid.NewString(), pointer: pointer},
		"store": {projectID: projectID, ownerID: ownerID, pointer: replaceFilePointer(pointer, func(value *FileBlobPointer) {
			value.Store = "other"
		})},
		"raw hash": {projectID: projectID, ownerID: ownerID, pointer: replaceFilePointer(pointer, func(value *FileBlobPointer) {
			value.ContentHash = digestFixture("other-file")
		})},
		"size": {projectID: projectID, ownerID: ownerID, pointer: replaceFilePointer(pointer, func(value *FileBlobPointer) {
			value.ByteSize++
		})},
		"object hash": {projectID: projectID, ownerID: ownerID, pointer: replaceFilePointer(pointer, func(value *FileBlobPointer) {
			value.ContentObjectHash = digestFixture("other-object")
		})},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Get(ctx, test.projectID, test.ownerID, test.pointer); err == nil {
				t.Fatal("drifted identity was accepted")
			}
		})
	}
}

func TestFileStoreRejectsEnvelopeAndStoredMetadataTampering(t *testing.T) {
	ctx := context.Background()
	projectID, ownerID := uuid.NewString(), uuid.NewString()

	for name, mutate := range map[string]func(*content.StoredContent){
		"project":        func(item *content.StoredContent) { item.ProjectID = uuid.NewString() },
		"aggregate type": func(item *content.StoredContent) { item.AggregateType = "other" },
		"aggregate id":   func(item *content.StoredContent) { item.AggregateID = uuid.NewString() },
		"schema":         func(item *content.StoredContent) { item.SchemaVersion++ },
		"state":          func(item *content.StoredContent) { item.State = content.StateAborted },
		"object size":    func(item *content.StoredContent) { item.ByteSize++ },
	} {
		t.Run(name, func(t *testing.T) {
			contents := newFakeTreeContentStore()
			contents.bypassGetHashCheck = true
			store, _ := NewFileStore(contents)
			pointer, err := store.PutPending(ctx, projectID, ownerID, []byte("immutable"))
			if err != nil {
				t.Fatal(err)
			}
			item := contents.items[pointer.Ref]
			mutate(&item)
			contents.items[pointer.Ref] = item
			if _, err := store.Get(ctx, projectID, ownerID, pointer); !errors.Is(err, ErrFileBlobIntegrity) {
				t.Fatalf("tamper error = %v, want integrity error", err)
			}
		})
	}

	tests := map[string]func(fileBlobDocument) fileBlobDocument{
		"schema": func(document fileBlobDocument) fileBlobDocument {
			document.SchemaVersion = "repository-file/v0"
			return document
		},
		"encoding": func(document fileBlobDocument) fileBlobDocument {
			document.Encoding = "plain"
			return document
		},
		"semantic hash": func(document fileBlobDocument) fileBlobDocument {
			document.ContentHash = digestFixture("forged")
			return document
		},
		"semantic size": func(document fileBlobDocument) fileBlobDocument {
			document.ByteSize++
			return document
		},
		"bytes": func(document fileBlobDocument) fileBlobDocument {
			document.Data = base64.StdEncoding.EncodeToString([]byte("different"))
			return document
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			contents := newFakeTreeContentStore()
			store, _ := NewFileStore(contents)
			pointer, err := store.PutPending(ctx, projectID, ownerID, []byte("immutable"))
			if err != nil {
				t.Fatal(err)
			}
			item := contents.items[pointer.Ref]
			document, err := decodeFileBlobDocument(item.Payload)
			if err != nil {
				t.Fatal(err)
			}
			document = mutate(document)
			payload, _ := domain.CanonicalJSON(document)
			item.Payload = append(json.RawMessage(nil), payload...)
			item.ContentHash = treeContentHash(payload)
			item.ByteSize = int64(len(payload))
			contents.items[pointer.Ref] = item
			pointer.ContentObjectHash = item.ContentHash
			if _, err := store.Get(ctx, projectID, ownerID, pointer); !errors.Is(err, ErrFileBlobIntegrity) {
				t.Fatalf("tamper error = %v, want integrity error", err)
			}
		})
	}
}

func TestFileStoreRejectsMalformedContentReference(t *testing.T) {
	contents := newFakeTreeContentStore()
	contents.mutatePutReference = func(reference content.Reference) content.Reference {
		reference.ContentHash = digestFixture("wrong-object")
		return reference
	}
	store, _ := NewFileStore(contents)
	if _, err := store.PutPending(context.Background(), uuid.NewString(), uuid.NewString(), []byte("value")); !errors.Is(err, ErrFileBlobIntegrity) {
		t.Fatalf("malformed reference error = %v", err)
	}
	if !reflect.DeepEqual(contents.aborted, []string{"tree-object-1"}) {
		t.Fatalf("malformed pending file was not aborted: %#v", contents.aborted)
	}
}

func replaceFilePointer(pointer FileBlobPointer, mutate func(*FileBlobPointer)) FileBlobPointer {
	mutate(&pointer)
	return pointer
}
