package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeFileBlobCatalog struct {
	items         map[string]FileBlobPointer
	registerCalls int
	findCalls     int
	registerErr   error
	findErr       error
	commitOnError bool
	malicious     *FileBlobPointer
}

func newFakeFileBlobCatalog() *fakeFileBlobCatalog {
	return &fakeFileBlobCatalog{items: map[string]FileBlobPointer{}}
}

func (catalog *fakeFileBlobCatalog) RegisterFileBlob(
	_ context.Context,
	registration FileBlobRegistration,
) (FileBlobPointer, error) {
	catalog.registerCalls++
	key := fileBlobCatalogTestKey(registration.ProjectID, registration.Pointer.ContentHash, registration.Pointer.ByteSize)
	if existing, found := catalog.items[key]; found {
		if catalog.malicious != nil {
			return *catalog.malicious, nil
		}
		return existing, nil
	}
	if catalog.registerErr != nil {
		if catalog.commitOnError {
			catalog.items[key] = registration.Pointer
		}
		return FileBlobPointer{}, catalog.registerErr
	}
	catalog.items[key] = registration.Pointer
	if catalog.malicious != nil {
		return *catalog.malicious, nil
	}
	return registration.Pointer, nil
}

func (catalog *fakeFileBlobCatalog) FindFileBlob(
	_ context.Context,
	projectID, contentHash string,
	byteSize int64,
) (FileBlobPointer, bool, error) {
	catalog.findCalls++
	if catalog.findErr != nil {
		return FileBlobPointer{}, false, catalog.findErr
	}
	pointer, found := catalog.items[fileBlobCatalogTestKey(projectID, contentHash, byteSize)]
	return pointer, found, nil
}

func fileBlobCatalogTestKey(projectID, contentHash string, byteSize int64) string {
	return fmt.Sprintf("%s:%s:%d", projectID, contentHash, byteSize)
}

func TestFileBlobServicePutResolveAndDeduplicate(t *testing.T) {
	ctx := context.Background()
	projectID, actorID := uuid.NewString(), uuid.NewString()
	contents := newFakeTreeContentStore()
	objects, _ := NewFileStore(contents)
	catalog := newFakeFileBlobCatalog()
	ids := []string{uuid.NewString(), uuid.NewString()}
	service, err := newFileBlobService(catalog, objects, func() time.Time {
		return time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	}, func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	})
	if err != nil {
		t.Fatal(err)
	}
	value := []byte("server-derived file bytes")

	first, err := service.Put(ctx, projectID, actorID, value)
	if err != nil {
		t.Fatalf("put first file: %v", err)
	}
	if first.Reused || first.Recovered || first.FinalizationPending ||
		contents.items[first.Pointer.Ref].State != "finalized" {
		t.Fatalf("unexpected first result: %#v", first)
	}
	pointer, resolved, err := service.Resolve(ctx, projectID, first.Pointer.ContentHash, first.Pointer.ByteSize)
	if err != nil {
		t.Fatalf("resolve file: %v", err)
	}
	if pointer != first.Pointer || string(resolved) != string(value) {
		t.Fatalf("resolved pointer/bytes drifted: pointer=%#v bytes=%q", pointer, resolved)
	}

	second, err := service.Put(ctx, projectID, actorID, value)
	if err != nil {
		t.Fatalf("put duplicate file: %v", err)
	}
	if !second.Reused || second.Recovered || second.Pointer != first.Pointer {
		t.Fatalf("semantic duplicate was not reused: %#v", second)
	}
	if len(contents.aborted) != 1 || contents.aborted[0] == first.Pointer.Ref {
		t.Fatalf("duplicate pending object was not aborted safely: %#v", contents.aborted)
	}
}

func TestFileBlobServiceRecoversCommitAcknowledgementLoss(t *testing.T) {
	ctx := context.Background()
	projectID, actorID, blobID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	contents := newFakeTreeContentStore()
	objects, _ := NewFileStore(contents)
	catalog := newFakeFileBlobCatalog()
	catalog.registerErr = errors.New("connection closed after commit")
	catalog.commitOnError = true
	service, _ := newFileBlobService(catalog, objects, time.Now, func() string { return blobID })

	result, err := service.Put(ctx, projectID, actorID, []byte("committed"))
	if err != nil {
		t.Fatalf("commit acknowledgement recovery failed: %v", err)
	}
	if !result.Recovered || result.Reused || result.FinalizationPending ||
		result.Pointer.OwnerID != blobID || len(contents.aborted) != 0 || len(contents.finalized) != 1 {
		t.Fatalf("unexpected recovery result: %#v finalized=%#v aborted=%#v", result, contents.finalized, contents.aborted)
	}
}

func TestFileBlobServiceAbortsOnlyKnownUnregisteredObject(t *testing.T) {
	ctx := context.Background()
	projectID, actorID := uuid.NewString(), uuid.NewString()

	t.Run("definite rollback", func(t *testing.T) {
		contents := newFakeTreeContentStore()
		objects, _ := NewFileStore(contents)
		catalog := newFakeFileBlobCatalog()
		catalog.registerErr = errors.New("constraint failure")
		service, _ := newFileBlobService(catalog, objects, time.Now, uuid.NewString)
		if _, err := service.Put(ctx, projectID, actorID, []byte("not committed")); err == nil {
			t.Fatal("expected registration failure")
		}
		if len(contents.aborted) != 1 {
			t.Fatalf("known orphan was not aborted: %#v", contents.aborted)
		}
	})

	t.Run("ambiguous lookup", func(t *testing.T) {
		contents := newFakeTreeContentStore()
		objects, _ := NewFileStore(contents)
		catalog := newFakeFileBlobCatalog()
		catalog.registerErr = errors.New("commit response lost")
		catalog.findErr = errors.New("database unavailable")
		service, _ := newFileBlobService(catalog, objects, time.Now, uuid.NewString)
		result, err := service.Put(ctx, projectID, actorID, []byte("possibly committed"))
		if !errors.Is(err, ErrFileBlobReconciliation) || !result.FinalizationPending {
			t.Fatalf("ambiguous result = %#v error=%v", result, err)
		}
		if len(contents.aborted) != 0 {
			t.Fatalf("possibly reachable object was aborted: %#v", contents.aborted)
		}
	})
}

func TestFileBlobServiceRetryClosesFinalizationGap(t *testing.T) {
	ctx := context.Background()
	projectID, actorID := uuid.NewString(), uuid.NewString()
	contents := newFakeTreeContentStore()
	contents.finalizeErr = errors.New("injected finalize outage")
	objects, _ := NewFileStore(contents)
	catalog := newFakeFileBlobCatalog()
	service, _ := newFileBlobService(catalog, objects, time.Now, uuid.NewString)
	value := []byte("durably registered")

	first, err := service.Put(ctx, projectID, actorID, value)
	if !errors.Is(err, ErrFileBlobFinalizationPending) || !first.FinalizationPending {
		t.Fatalf("first result = %#v error=%v", first, err)
	}
	contents.finalizeErr = nil
	second, err := service.Put(ctx, projectID, actorID, value)
	if err != nil {
		t.Fatalf("retry did not close finalization gap: %v", err)
	}
	if !second.Reused || second.Pointer != first.Pointer || second.FinalizationPending {
		t.Fatalf("unexpected retry result: %#v", second)
	}
	if contents.items[first.Pointer.Ref].State != "finalized" {
		t.Fatal("registered pending object was not finalized on retry")
	}
}

func TestFileBlobServiceRejectsCatalogPointerDriftAndCrossTenantResolve(t *testing.T) {
	ctx := context.Background()
	projectID, actorID := uuid.NewString(), uuid.NewString()
	contents := newFakeTreeContentStore()
	objects, _ := NewFileStore(contents)
	catalog := newFakeFileBlobCatalog()
	malicious := FileBlobPointer{
		Store: FileContentStore, Ref: "attacker-ref", OwnerID: uuid.NewString(),
		ContentHash: digestFixture("attacker"), ByteSize: 8,
		ContentObjectHash: digestFixture("attacker-object"),
	}
	catalog.malicious = &malicious
	service, _ := newFileBlobService(catalog, objects, time.Now, uuid.NewString)
	if result, err := service.Put(ctx, projectID, actorID, []byte("legitimate")); !errors.Is(err, ErrFileBlobCatalogContract) || !result.FinalizationPending {
		t.Fatalf("malicious catalog result = %#v error=%v", result, err)
	}

	validCatalog := newFakeFileBlobCatalog()
	validService, _ := newFileBlobService(validCatalog, objects, time.Now, uuid.NewString)
	created, err := validService.Put(ctx, projectID, actorID, []byte("tenant-scoped"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := validService.Resolve(ctx, uuid.NewString(), created.Pointer.ContentHash, created.Pointer.ByteSize); !errors.Is(err, ErrFileBlobNotFound) {
		t.Fatalf("cross-tenant resolve error = %v, want not found", err)
	}
}

func TestNewFileBlobServiceRequiresDependencies(t *testing.T) {
	objects, _ := NewFileStore(newFakeTreeContentStore())
	catalog := newFakeFileBlobCatalog()
	if _, err := newFileBlobService(nil, objects, time.Now, uuid.NewString); err == nil {
		t.Fatal("nil catalog was accepted")
	}
	if _, err := newFileBlobService(catalog, nil, time.Now, uuid.NewString); err == nil {
		t.Fatal("nil object store was accepted")
	}
}
