package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrFileBlobNotFound            = errors.New("repository file blob not found")
	ErrFileBlobCatalogContract     = errors.New("repository file blob catalog violated its contract")
	ErrFileBlobReconciliation      = errors.New("repository file blob requires reconciliation")
	ErrFileBlobFinalizationPending = errors.New("repository file blob was registered but finalization is pending")
)

type FileBlobRegistration struct {
	ID        string
	ProjectID string
	Pointer   FileBlobPointer
	CreatedBy string
	CreatedAt time.Time
}

type FileBlobCatalog interface {
	RegisterFileBlob(context.Context, FileBlobRegistration) (FileBlobPointer, error)
	FindFileBlob(context.Context, string, string, int64) (FileBlobPointer, bool, error)
}

type fileBlobObjectStore interface {
	PutPending(context.Context, string, string, []byte) (FileBlobPointer, error)
	Get(context.Context, string, string, FileBlobPointer) ([]byte, error)
	Finalize(context.Context, string, string, FileBlobPointer) error
	Abort(context.Context, string, string, FileBlobPointer) error
}

type FileBlobWriteResult struct {
	Pointer             FileBlobPointer `json:"pointer"`
	Reused              bool            `json:"reused"`
	Recovered           bool            `json:"recovered"`
	FinalizationPending bool            `json:"finalizationPending"`
}

// FileBlobService is the only path from request bytes to a tree-eligible
// ContentHash. It derives the semantic hash on the server, registers the exact
// content.Store pointer in PostgreSQL, and closes commit/finalize ambiguity by
// resolving the tenant-scoped semantic key before deciding whether to abort.
type FileBlobService struct {
	catalog FileBlobCatalog
	objects fileBlobObjectStore
	now     func() time.Time
	newID   func() string
}

func NewFileBlobService(
	catalog FileBlobCatalog,
	objects *FileStore,
	now func() time.Time,
) (*FileBlobService, error) {
	return newFileBlobService(catalog, objects, now, uuid.NewString)
}

func newFileBlobService(
	catalog FileBlobCatalog,
	objects fileBlobObjectStore,
	now func() time.Time,
	newID func() string,
) (*FileBlobService, error) {
	if catalog == nil || objects == nil || now == nil || newID == nil {
		return nil, errors.New("repository file blob catalog, object store, clock, and id source are required")
	}
	return &FileBlobService{catalog: catalog, objects: objects, now: now, newID: newID}, nil
}

func (service *FileBlobService) Put(
	ctx context.Context,
	projectID, actorID string,
	value []byte,
) (FileBlobWriteResult, error) {
	if !validUUID(projectID) || !validUUID(actorID) {
		return FileBlobWriteResult{}, fmt.Errorf("%w: project and actor IDs are required", ErrInvalidCandidate)
	}
	createdAt := service.now().UTC()
	if createdAt.IsZero() {
		return FileBlobWriteResult{}, fmt.Errorf("%w: file blob timestamp", ErrInvalidCandidate)
	}
	blobID := service.newID()
	if !validUUID(blobID) {
		return FileBlobWriteResult{}, fmt.Errorf("%w: generated file blob ID", ErrInvalidCandidate)
	}
	pending, err := service.objects.PutPending(ctx, projectID, blobID, value)
	if err != nil {
		return FileBlobWriteResult{}, err
	}
	registration := FileBlobRegistration{
		ID: blobID, ProjectID: projectID, Pointer: pending, CreatedBy: actorID, CreatedAt: createdAt,
	}
	registered, registerErr := service.catalog.RegisterFileBlob(ctx, registration)
	if registerErr == nil {
		return service.settleRegistered(ctx, projectID, pending, registered, false)
	}

	// A driver/connection error may arrive after PostgreSQL committed. Resolve
	// with a short non-cancelled context before deciding whether pending content
	// is unreachable and safe to abort.
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	reconciled, found, findErr := service.catalog.FindFileBlob(
		reconcileCtx, projectID, pending.ContentHash, pending.ByteSize,
	)
	if findErr != nil {
		return FileBlobWriteResult{Pointer: pending, FinalizationPending: true}, errors.Join(
			ErrFileBlobReconciliation,
			fmt.Errorf("register repository file blob: %w", registerErr),
			fmt.Errorf("resolve ambiguous repository file blob registration: %w", findErr),
		)
	}
	if found {
		result, settleErr := service.settleRegistered(reconcileCtx, projectID, pending, reconciled, true)
		if settleErr != nil {
			return result, errors.Join(fmt.Errorf("register repository file blob: %w", registerErr), settleErr)
		}
		return result, nil
	}
	abortErr := service.objects.Abort(reconcileCtx, projectID, pending.OwnerID, pending)
	if abortErr != nil {
		return FileBlobWriteResult{}, errors.Join(
			fmt.Errorf("register repository file blob: %w", registerErr),
			fmt.Errorf("abort unregistered repository file blob: %w", abortErr),
		)
	}
	return FileBlobWriteResult{}, fmt.Errorf("register repository file blob: %w", registerErr)
}

func (service *FileBlobService) Resolve(
	ctx context.Context,
	projectID, contentHash string,
	byteSize int64,
) (FileBlobPointer, []byte, error) {
	if !validUUID(projectID) || !isCanonicalSHA256(contentHash) || byteSize < 0 || byteSize > MaxFileBytes {
		return FileBlobPointer{}, nil, ErrInvalidFilePointer
	}
	pointer, found, err := service.catalog.FindFileBlob(ctx, projectID, contentHash, byteSize)
	if err != nil {
		return FileBlobPointer{}, nil, fmt.Errorf("find repository file blob: %w", err)
	}
	if !found {
		return FileBlobPointer{}, nil, ErrFileBlobNotFound
	}
	if err := validateCatalogPointer(pointer, projectID, contentHash, byteSize); err != nil {
		return FileBlobPointer{}, nil, err
	}
	value, err := service.objects.Get(ctx, projectID, pointer.OwnerID, pointer)
	if err != nil {
		return FileBlobPointer{}, nil, err
	}
	return pointer, value, nil
}

func (service *FileBlobService) VerifyFileBlob(
	ctx context.Context,
	projectID, contentHash string,
	byteSize int64,
) error {
	_, _, err := service.Resolve(ctx, projectID, contentHash, byteSize)
	return err
}

// Settle makes a catalog-reachable semantic file object durable. It is safe to
// repeat after a lost acknowledgement and is used before RepositorySnapshot
// bootstrap is allowed to return a completed receipt.
func (service *FileBlobService) Settle(
	ctx context.Context,
	projectID, contentHash string,
	byteSize int64,
) error {
	pointer, _, err := service.Resolve(ctx, projectID, contentHash, byteSize)
	if err != nil {
		return err
	}
	if err := service.objects.Finalize(ctx, projectID, pointer.OwnerID, pointer); err != nil {
		return fmt.Errorf("finalize repository file blob: %w", err)
	}
	return nil
}

func (service *FileBlobService) settleRegistered(
	ctx context.Context,
	projectID string,
	pending, registered FileBlobPointer,
	recovered bool,
) (FileBlobWriteResult, error) {
	if err := validateCatalogPointer(registered, projectID, pending.ContentHash, pending.ByteSize); err != nil {
		return FileBlobWriteResult{Pointer: pending, FinalizationPending: true}, errors.Join(ErrFileBlobReconciliation, err)
	}
	if registered == pending {
		result := FileBlobWriteResult{Pointer: registered, Recovered: recovered}
		if err := service.objects.Finalize(ctx, projectID, registered.OwnerID, registered); err != nil {
			result.FinalizationPending = true
			return result, errors.Join(ErrFileBlobFinalizationPending, err)
		}
		return result, nil
	}

	// A semantic duplicate already owns the catalog key. Verify it is readable
	// before discarding the newly-created unregistered pending object.
	if _, err := service.objects.Get(ctx, projectID, registered.OwnerID, registered); err != nil {
		abortErr := service.objects.Abort(context.WithoutCancel(ctx), projectID, pending.OwnerID, pending)
		return FileBlobWriteResult{}, errors.Join(
			ErrFileBlobCatalogContract,
			fmt.Errorf("read registered duplicate repository file blob: %w", err),
			abortErr,
		)
	}
	if err := service.objects.Abort(context.WithoutCancel(ctx), projectID, pending.OwnerID, pending); err != nil {
		return FileBlobWriteResult{Pointer: registered, Reused: true, Recovered: recovered}, errors.Join(
			ErrFileBlobReconciliation,
			fmt.Errorf("abort duplicate pending repository file blob: %w", err),
		)
	}
	result := FileBlobWriteResult{Pointer: registered, Reused: true, Recovered: recovered}
	if err := service.objects.Finalize(ctx, projectID, registered.OwnerID, registered); err != nil {
		result.FinalizationPending = true
		return result, errors.Join(ErrFileBlobFinalizationPending, err)
	}
	return result, nil
}

func validateCatalogPointer(pointer FileBlobPointer, projectID, contentHash string, byteSize int64) error {
	if !validUUID(projectID) || pointer.validate() != nil ||
		pointer.ContentHash != contentHash || pointer.ByteSize != byteSize {
		return ErrFileBlobCatalogContract
	}
	return nil
}
