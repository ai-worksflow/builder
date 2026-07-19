package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	FileContentStore         = TreeContentStore
	FileContentAggregateType = "repository_file"
	FileContentSchemaVersion = 1
	FileDocumentSchema       = "repository-file/v1"
)

var (
	ErrInvalidFilePointer = errors.New("invalid repository file pointer")
	ErrFileBlobIntegrity  = errors.New("repository file blob integrity check failed")
)

// FileBlobPointer is the exact storage identity registered in PostgreSQL for
// one repository file body. TreeFile stores only ContentHash and ByteSize;
// Repository Service resolves those facts through the tenant-scoped catalog
// and never accepts a content-store reference from a browser or agent.
type FileBlobPointer struct {
	Store             string `json:"store"`
	Ref               string `json:"ref"`
	OwnerID           string `json:"ownerId"`
	ContentHash       string `json:"contentHash"`
	ByteSize          int64  `json:"byteSize"`
	ContentObjectHash string `json:"contentObjectHash"`
}

type fileBlobDocument struct {
	SchemaVersion string `json:"schemaVersion"`
	Encoding      string `json:"encoding"`
	ContentHash   string `json:"contentHash"`
	ByteSize      int64  `json:"byteSize"`
	Data          string `json:"data"`
}

// FileStore adapts content.Store to arbitrary repository file bytes. The
// bytes are wrapped in a canonical base64 JSON envelope because content.Store
// deliberately accepts canonical JSON documents. The semantic ContentHash is
// still SHA-256 over the original bytes, not over that storage envelope.
type FileStore struct {
	contents content.Store
}

func NewFileStore(contents content.Store) (*FileStore, error) {
	if contents == nil {
		return nil, errors.New("repository file content store is required")
	}
	return &FileStore{contents: contents}, nil
}

func (store *FileStore) PutPending(
	ctx context.Context,
	projectID, ownerID string,
	value []byte,
) (FileBlobPointer, error) {
	if err := validateTreeAggregate(projectID, ownerID); err != nil {
		return FileBlobPointer{}, err
	}
	if int64(len(value)) > MaxFileBytes {
		return FileBlobPointer{}, fmt.Errorf("%w: file exceeds %d bytes", ErrTreeLimit, MaxFileBytes)
	}
	document := fileBlobDocument{
		SchemaVersion: FileDocumentSchema,
		Encoding:      "base64",
		ContentHash:   rawFileContentHash(value),
		ByteSize:      int64(len(value)),
		Data:          base64.StdEncoding.EncodeToString(value),
	}
	payload, err := domain.CanonicalJSON(document)
	if err != nil {
		return FileBlobPointer{}, fmt.Errorf("encode canonical repository file: %w", err)
	}
	expectedObjectHash := treeContentHash(payload)
	reference, err := store.contents.PutPending(
		ctx,
		strings.TrimSpace(projectID),
		FileContentAggregateType,
		strings.TrimSpace(ownerID),
		FileContentSchemaVersion,
		json.RawMessage(payload),
	)
	if err != nil {
		return FileBlobPointer{}, fmt.Errorf("put pending repository file: %w", err)
	}
	if reference.ID == "" || reference.ID != strings.TrimSpace(reference.ID) || len(reference.ID) > 512 ||
		reference.SchemaVersion != FileContentSchemaVersion ||
		reference.ByteSize != int64(len(payload)) || reference.ContentHash != expectedObjectHash {
		if reference.ID != "" {
			_ = store.contents.Abort(context.Background(), reference.ID)
		}
		return FileBlobPointer{}, fmt.Errorf("%w: content store returned a malformed reference", ErrFileBlobIntegrity)
	}
	return FileBlobPointer{
		Store: FileContentStore, Ref: reference.ID, OwnerID: strings.TrimSpace(ownerID),
		ContentHash: document.ContentHash, ByteSize: document.ByteSize,
		ContentObjectHash: reference.ContentHash,
	}, nil
}

func (store *FileStore) Get(
	ctx context.Context,
	projectID, ownerID string,
	pointer FileBlobPointer,
) ([]byte, error) {
	if err := validateTreeAggregate(projectID, ownerID); err != nil {
		return nil, err
	}
	if err := pointer.validate(); err != nil {
		return nil, err
	}
	stored, err := store.contents.Get(ctx, pointer.Ref, pointer.ContentObjectHash)
	if err != nil {
		return nil, fmt.Errorf("get repository file content: %w", err)
	}
	if pointer.OwnerID != strings.TrimSpace(ownerID) || stored.ID != pointer.Ref ||
		stored.ProjectID != strings.TrimSpace(projectID) || stored.AggregateType != FileContentAggregateType ||
		stored.AggregateID != pointer.OwnerID || stored.SchemaVersion != FileContentSchemaVersion ||
		(stored.State != content.StatePending && stored.State != content.StateFinalized) {
		return nil, fmt.Errorf("%w: content identity, schema, or state mismatch", ErrFileBlobIntegrity)
	}
	if stored.ContentHash != pointer.ContentObjectHash || stored.ByteSize != int64(len(stored.Payload)) ||
		treeContentHash(stored.Payload) != stored.ContentHash {
		return nil, fmt.Errorf("%w: %w", ErrFileBlobIntegrity, content.ErrHashMismatch)
	}

	document, err := decodeFileBlobDocument(stored.Payload)
	if err != nil {
		return nil, err
	}
	canonical, err := domain.CanonicalJSON(document)
	if err != nil || !bytes.Equal(canonical, stored.Payload) {
		return nil, fmt.Errorf("%w: file envelope is not canonical", ErrFileBlobIntegrity)
	}
	if document.SchemaVersion != FileDocumentSchema || document.Encoding != "base64" ||
		document.ContentHash != pointer.ContentHash || document.ByteSize != pointer.ByteSize {
		return nil, fmt.Errorf("%w: file envelope facts drifted", ErrFileBlobIntegrity)
	}
	value, err := base64.StdEncoding.Strict().DecodeString(document.Data)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid base64 file envelope", ErrFileBlobIntegrity)
	}
	if int64(len(value)) != pointer.ByteSize || int64(len(value)) > MaxFileBytes ||
		rawFileContentHash(value) != pointer.ContentHash {
		return nil, fmt.Errorf("%w: file bytes do not match pointer", ErrFileBlobIntegrity)
	}
	return append([]byte(nil), value...), nil
}

func (store *FileStore) Finalize(
	ctx context.Context,
	projectID, ownerID string,
	pointer FileBlobPointer,
) error {
	if _, err := store.Get(ctx, projectID, ownerID, pointer); err != nil {
		return err
	}
	if err := store.contents.Finalize(ctx, pointer.Ref); err != nil {
		return fmt.Errorf("finalize repository file: %w", err)
	}
	return nil
}

func (store *FileStore) Abort(
	ctx context.Context,
	projectID, ownerID string,
	pointer FileBlobPointer,
) error {
	if _, err := store.Get(ctx, projectID, ownerID, pointer); err != nil {
		return err
	}
	if err := store.contents.Abort(ctx, pointer.Ref); err != nil {
		return fmt.Errorf("abort repository file: %w", err)
	}
	return nil
}

func decodeFileBlobDocument(payload []byte) (fileBlobDocument, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var document fileBlobDocument
	if err := decoder.Decode(&document); err != nil {
		return fileBlobDocument{}, fmt.Errorf("%w: decode file envelope: %v", ErrFileBlobIntegrity, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values are not allowed")
		}
		return fileBlobDocument{}, fmt.Errorf("%w: decode file envelope: %v", ErrFileBlobIntegrity, err)
	}
	return document, nil
}

func (pointer FileBlobPointer) validate() error {
	if pointer.Store != FileContentStore || pointer.Ref == "" || pointer.Ref != strings.TrimSpace(pointer.Ref) || len(pointer.Ref) > 512 ||
		!validUUID(pointer.OwnerID) || !isCanonicalSHA256(pointer.ContentHash) ||
		pointer.ByteSize < 0 || pointer.ByteSize > MaxFileBytes ||
		!isCanonicalSHA256(pointer.ContentObjectHash) {
		return ErrInvalidFilePointer
	}
	return nil
}

func rawFileContentHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
