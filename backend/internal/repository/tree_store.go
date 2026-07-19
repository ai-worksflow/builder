package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
)

const (
	// TreeContentStore is the logical store name persisted alongside a tree
	// reference. It deliberately names the content.Store abstraction rather
	// than its current MongoDB implementation.
	TreeContentStore = "content"

	// TreeContentAggregateType keeps every tree object scoped to its immutable
	// RepositorySnapshot or mutable Candidate owner. The same tree may be written by multiple owners without
	// sharing a pending object's lifecycle.
	TreeContentAggregateType = "repository_tree"

	// TreeContentSchemaVersion is the storage envelope version. The semantic
	// schema remains TreeSchemaVersion inside the TreeManifest.
	TreeContentSchemaVersion = 1
)

var (
	ErrInvalidTreePointer = errors.New("invalid repository tree pointer")
	ErrTreeBlobIntegrity  = errors.New("repository tree blob integrity check failed")
)

// TreeBlobPointer is the complete, bounded value persisted in PostgreSQL for
// a tree body held by content.Store. TreeHash and ContentObjectHash protect
// different facts: TreeHash describes the repository tree, while
// ContentObjectHash describes the exact canonical JSON object in blob storage.
type TreeBlobPointer struct {
	Store             string `json:"store"`
	Ref               string `json:"ref"`
	OwnerID           string `json:"ownerId"`
	TreeHash          string `json:"treeHash"`
	FileCount         int    `json:"fileCount"`
	ByteSize          int64  `json:"byteSize"`
	ContentObjectHash string `json:"contentObjectHash"`
}

// TreeStore adapts content.Store to repository tree semantics. PostgreSQL
// stores only TreeBlobPointer; the TreeManifest body remains in content.Store.
type TreeStore struct {
	contents content.Store
}

func NewTreeStore(contents content.Store) (*TreeStore, error) {
	if contents == nil {
		return nil, errors.New("repository tree content store is required")
	}
	return &TreeStore{contents: contents}, nil
}

// PutPending writes one canonical tree object. Callers should persist the
// returned pointer in their PostgreSQL transaction and call Finalize only
// after that transaction commits. A pending object referenced by committed SQL
// remains readable, so a crash between those two steps is recoverable.
func (s *TreeStore) PutPending(
	ctx context.Context,
	projectID, ownerID string,
	manifest TreeManifest,
) (TreeBlobPointer, error) {
	if err := validateTreeAggregate(projectID, ownerID); err != nil {
		return TreeBlobPointer{}, err
	}
	tree, err := ParseTree(manifest)
	if err != nil {
		return TreeBlobPointer{}, err
	}
	payload, err := domain.CanonicalJSON(tree)
	if err != nil {
		return TreeBlobPointer{}, fmt.Errorf("encode canonical repository tree: %w", err)
	}
	expectedObjectHash := treeContentHash(payload)
	reference, err := s.contents.PutPending(
		ctx,
		strings.TrimSpace(projectID),
		TreeContentAggregateType,
		strings.TrimSpace(ownerID),
		TreeContentSchemaVersion,
		json.RawMessage(payload),
	)
	if err != nil {
		return TreeBlobPointer{}, fmt.Errorf("put pending repository tree: %w", err)
	}

	if reference.ID == "" || reference.ID != strings.TrimSpace(reference.ID) ||
		reference.SchemaVersion != TreeContentSchemaVersion ||
		reference.ByteSize != int64(len(payload)) ||
		reference.ContentHash != expectedObjectHash {
		// A malformed reference must never reach PostgreSQL. Best-effort abort
		// keeps the invalid pending object eligible for immediate cleanup.
		_ = s.contents.Abort(context.Background(), reference.ID)
		return TreeBlobPointer{}, fmt.Errorf("%w: content store returned a malformed reference", ErrTreeBlobIntegrity)
	}

	return TreeBlobPointer{
		Store:             TreeContentStore,
		Ref:               reference.ID,
		OwnerID:           strings.TrimSpace(ownerID),
		TreeHash:          tree.TreeHash,
		FileCount:         len(tree.Files),
		ByteSize:          treeByteSize(tree),
		ContentObjectHash: reference.ContentHash,
	}, nil
}

// Finalize is idempotent for pending or already-finalized objects. The full
// identity and integrity check prevents a caller from finalizing a blob owned
// by another project or candidate through this adapter.
func (s *TreeStore) Finalize(
	ctx context.Context,
	projectID, ownerID string,
	pointer TreeBlobPointer,
) error {
	if _, err := s.Get(ctx, projectID, ownerID, pointer); err != nil {
		return err
	}
	if err := s.contents.Finalize(ctx, pointer.Ref); err != nil {
		return fmt.Errorf("finalize repository tree: %w", err)
	}
	return nil
}

// Abort removes reachability for an uncommitted pending object. It performs
// the same binding check as Finalize, so an arbitrary content reference cannot
// be aborted through the repository adapter.
func (s *TreeStore) Abort(
	ctx context.Context,
	projectID, ownerID string,
	pointer TreeBlobPointer,
) error {
	if _, err := s.Get(ctx, projectID, ownerID, pointer); err != nil {
		return err
	}
	if err := s.contents.Abort(ctx, pointer.Ref); err != nil {
		return fmt.Errorf("abort repository tree: %w", err)
	}
	return nil
}

// Get resolves both pending and finalized objects. PostgreSQL is the
// authoritative reachability check: a caller must supply the exact pointer it
// read from the owning SQL aggregate.
func (s *TreeStore) Get(
	ctx context.Context,
	projectID, ownerID string,
	pointer TreeBlobPointer,
) (TreeManifest, error) {
	stored, err := s.getStored(ctx, projectID, ownerID, pointer)
	if err != nil {
		return TreeManifest{}, err
	}
	return decodeStoredTree(stored, pointer)
}

func (s *TreeStore) getStored(
	ctx context.Context,
	projectID, ownerID string,
	pointer TreeBlobPointer,
) (content.StoredContent, error) {
	if err := validateTreeAggregate(projectID, ownerID); err != nil {
		return content.StoredContent{}, err
	}
	if err := pointer.validate(); err != nil {
		return content.StoredContent{}, err
	}
	stored, err := s.contents.Get(ctx, pointer.Ref, pointer.ContentObjectHash)
	if err != nil {
		return content.StoredContent{}, fmt.Errorf("get repository tree content: %w", err)
	}
	if pointer.OwnerID != strings.TrimSpace(ownerID) || stored.ID != pointer.Ref || stored.ProjectID != strings.TrimSpace(projectID) ||
		stored.AggregateType != TreeContentAggregateType || stored.AggregateID != pointer.OwnerID ||
		stored.SchemaVersion != TreeContentSchemaVersion ||
		(stored.State != content.StatePending && stored.State != content.StateFinalized) {
		return content.StoredContent{}, fmt.Errorf("%w: content identity, schema, or state mismatch", ErrTreeBlobIntegrity)
	}
	if stored.ContentHash != pointer.ContentObjectHash ||
		stored.ByteSize != int64(len(stored.Payload)) ||
		treeContentHash(stored.Payload) != stored.ContentHash {
		return content.StoredContent{}, fmt.Errorf("%w: %w", ErrTreeBlobIntegrity, content.ErrHashMismatch)
	}
	return stored, nil
}

func decodeStoredTree(stored content.StoredContent, pointer TreeBlobPointer) (TreeManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(stored.Payload))
	decoder.DisallowUnknownFields()
	var manifest TreeManifest
	if err := decoder.Decode(&manifest); err != nil {
		return TreeManifest{}, fmt.Errorf("%w: decode tree manifest: %v", ErrTreeBlobIntegrity, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values are not allowed")
		}
		return TreeManifest{}, fmt.Errorf("%w: decode tree manifest: %v", ErrTreeBlobIntegrity, err)
	}

	tree, err := ParseTree(manifest)
	if err != nil {
		return TreeManifest{}, fmt.Errorf("%w: %v", ErrTreeBlobIntegrity, err)
	}
	canonical, err := domain.CanonicalJSON(tree)
	if err != nil {
		return TreeManifest{}, fmt.Errorf("%w: canonicalize tree manifest: %v", ErrTreeBlobIntegrity, err)
	}
	if !bytes.Equal(canonical, stored.Payload) {
		return TreeManifest{}, fmt.Errorf("%w: tree payload is not canonically encoded", ErrTreeBlobIntegrity)
	}
	if tree.TreeHash != pointer.TreeHash || len(tree.Files) != pointer.FileCount ||
		treeByteSize(tree) != pointer.ByteSize {
		return TreeManifest{}, fmt.Errorf("%w: semantic tree facts do not match pointer", ErrTreeBlobIntegrity)
	}
	return tree, nil
}

func (pointer TreeBlobPointer) validate() error {
	if pointer.Store != TreeContentStore || pointer.Ref == "" || pointer.Ref != strings.TrimSpace(pointer.Ref) || !validUUID(pointer.OwnerID) ||
		!isCanonicalSHA256(pointer.TreeHash) || pointer.FileCount < 0 || pointer.FileCount > MaxTreeFiles ||
		pointer.ByteSize < 0 || pointer.ByteSize > MaxTreeBytes ||
		!isCanonicalSHA256(pointer.ContentObjectHash) {
		return ErrInvalidTreePointer
	}
	return nil
}

func validateTreeAggregate(projectID, ownerID string) error {
	if !validUUID(projectID) || !validUUID(ownerID) {
		return fmt.Errorf("%w: project and tree owner UUIDs are required", ErrInvalidCandidate)
	}
	return nil
}

func treeByteSize(tree TreeManifest) int64 {
	var total int64
	for _, file := range tree.Files {
		total += file.ByteSize
	}
	return total
}

func treeContentHash(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}
