package agent

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
	AgentEvidenceAggregateType = "agent_attempt_evidence"
	AgentEvidenceStore         = "content"
	AgentEvidenceSchemaVersion = 1
	AgentEvidenceDocument      = "agent-attempt-evidence/v1"
	maxAgentEvidenceBytes      = 4 << 20
)

type EvidenceKind string

const (
	EvidencePatch            EvidenceKind = "patch"
	EvidenceStructuredResult EvidenceKind = "structured_result"
	EvidenceStdout           EvidenceKind = "stdout"
	EvidenceStderr           EvidenceKind = "stderr"
	EvidenceValidation       EvidenceKind = "validation"
)

var (
	ErrEvidenceInvalid   = errors.New("Agent evidence is invalid")
	ErrEvidenceIntegrity = errors.New("Agent evidence integrity check failed")
	ErrEvidencePending   = errors.New("Agent evidence is not finalized")
)

type evidenceDocument struct {
	SchemaVersion string       `json:"schemaVersion"`
	Kind          EvidenceKind `json:"kind"`
	MediaType     string       `json:"mediaType"`
	Encoding      string       `json:"encoding"`
	RawHash       string       `json:"rawHash"`
	ByteSize      int64        `json:"byteSize"`
	Data          string       `json:"data"`
}

type EvidenceStore struct {
	contents content.Store
}

func NewEvidenceStore(contents content.Store) (*EvidenceStore, error) {
	if contents == nil {
		return nil, errors.New("Agent evidence content store is required")
	}
	return &EvidenceStore{contents: contents}, nil
}

func (store *EvidenceStore) PutPending(
	ctx context.Context,
	attempt AgentAttempt,
	kind EvidenceKind,
	mediaType string,
	value []byte,
) (BlobReference, error) {
	if store == nil || ctx == nil || !validUUIDs(attempt.ID, attempt.ProjectID) || !knownEvidenceKind(kind) {
		return BlobReference{}, ErrEvidenceInvalid
	}
	mediaType = strings.TrimSpace(mediaType)
	if mediaType == "" || len(mediaType) > 160 || strings.ContainsAny(mediaType, "\r\n\x00") {
		return BlobReference{}, fmt.Errorf("%w: media type", ErrEvidenceInvalid)
	}
	document := evidenceDocument{
		SchemaVersion: AgentEvidenceDocument,
		Kind:          kind,
		MediaType:     mediaType,
		Encoding:      "base64",
		RawHash:       rawEvidenceHash(value),
		ByteSize:      int64(len(value)),
		Data:          base64.StdEncoding.EncodeToString(value),
	}
	payload, err := domain.CanonicalJSON(document)
	if err != nil || len(payload) == 0 || len(payload) > maxAgentEvidenceBytes {
		return BlobReference{}, fmt.Errorf("%w: encoded evidence exceeds its bound", ErrEvidenceInvalid)
	}
	reference, err := store.contents.PutPending(
		ctx,
		attempt.ProjectID,
		AgentEvidenceAggregateType,
		attempt.ID,
		AgentEvidenceSchemaVersion,
		json.RawMessage(payload),
	)
	if err != nil {
		return BlobReference{}, fmt.Errorf("put pending Agent evidence: %w", err)
	}
	result := BlobReference{
		Store: AgentEvidenceStore, OwnerID: attempt.ID, Ref: reference.ID,
		ContentHash: reference.ContentHash, ByteSize: reference.ByteSize,
	}
	if result.validate() != nil || reference.SchemaVersion != AgentEvidenceSchemaVersion ||
		reference.ByteSize != int64(len(payload)) || reference.ContentHash != rawEvidenceHash(payload) {
		if reference.ID != "" {
			_ = store.contents.Abort(context.WithoutCancel(ctx), reference.ID)
		}
		return BlobReference{}, fmt.Errorf("%w: content store returned a malformed reference", ErrEvidenceIntegrity)
	}
	return result, nil
}

func (store *EvidenceStore) Get(
	ctx context.Context,
	attempt AgentAttempt,
	kind EvidenceKind,
	reference BlobReference,
) ([]byte, error) {
	return store.get(ctx, attempt, kind, reference, false)
}

func (store *EvidenceStore) GetFinalized(
	ctx context.Context,
	attempt AgentAttempt,
	kind EvidenceKind,
	reference BlobReference,
) ([]byte, error) {
	return store.get(ctx, attempt, kind, reference, true)
}

func (store *EvidenceStore) get(
	ctx context.Context,
	attempt AgentAttempt,
	kind EvidenceKind,
	reference BlobReference,
	requireFinalized bool,
) ([]byte, error) {
	if store == nil || ctx == nil || !validUUIDs(attempt.ID, attempt.ProjectID) ||
		!knownEvidenceKind(kind) || reference.validate() != nil ||
		reference.Store != AgentEvidenceStore || reference.OwnerID != attempt.ID {
		return nil, ErrEvidenceInvalid
	}
	stored, err := store.contents.Get(ctx, reference.Ref, reference.ContentHash)
	if err != nil {
		return nil, fmt.Errorf("get Agent evidence: %w", err)
	}
	if stored.ID != reference.Ref || stored.ProjectID != attempt.ProjectID ||
		stored.AggregateType != AgentEvidenceAggregateType || stored.AggregateID != attempt.ID ||
		stored.SchemaVersion != AgentEvidenceSchemaVersion ||
		(stored.State != content.StatePending && stored.State != content.StateFinalized) ||
		stored.ContentHash != reference.ContentHash || stored.ByteSize != reference.ByteSize ||
		stored.ByteSize != int64(len(stored.Payload)) || rawEvidenceHash(stored.Payload) != stored.ContentHash {
		return nil, fmt.Errorf("%w: stored content identity", ErrEvidenceIntegrity)
	}
	if requireFinalized && stored.State != content.StateFinalized {
		return nil, ErrEvidencePending
	}
	document, err := decodeEvidenceDocument(stored.Payload)
	if err != nil {
		return nil, err
	}
	canonical, err := domain.CanonicalJSON(document)
	if err != nil || !bytes.Equal(canonical, stored.Payload) ||
		document.SchemaVersion != AgentEvidenceDocument || document.Kind != kind ||
		document.Encoding != "base64" || document.MediaType == "" ||
		document.MediaType != strings.TrimSpace(document.MediaType) || document.ByteSize < 0 {
		return nil, fmt.Errorf("%w: non-canonical evidence envelope", ErrEvidenceIntegrity)
	}
	value, err := base64.StdEncoding.Strict().DecodeString(document.Data)
	if err != nil || int64(len(value)) != document.ByteSize || rawEvidenceHash(value) != document.RawHash {
		return nil, fmt.Errorf("%w: evidence bytes", ErrEvidenceIntegrity)
	}
	return append([]byte(nil), value...), nil
}

func (store *EvidenceStore) Finalize(
	ctx context.Context,
	attempt AgentAttempt,
	kind EvidenceKind,
	reference BlobReference,
) error {
	if _, err := store.Get(ctx, attempt, kind, reference); err != nil {
		return err
	}
	if err := store.contents.Finalize(ctx, reference.Ref); err != nil {
		return fmt.Errorf("finalize Agent evidence: %w", err)
	}
	return nil
}

func (store *EvidenceStore) Abort(
	ctx context.Context,
	attempt AgentAttempt,
	kind EvidenceKind,
	reference BlobReference,
) error {
	if _, err := store.Get(ctx, attempt, kind, reference); err != nil {
		return err
	}
	if err := store.contents.Abort(ctx, reference.Ref); err != nil {
		return fmt.Errorf("abort Agent evidence: %w", err)
	}
	return nil
}

func decodeEvidenceDocument(payload []byte) (evidenceDocument, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var document evidenceDocument
	if err := decoder.Decode(&document); err != nil {
		return evidenceDocument{}, fmt.Errorf("%w: decode envelope: %v", ErrEvidenceIntegrity, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return evidenceDocument{}, fmt.Errorf("%w: trailing envelope JSON", ErrEvidenceIntegrity)
	}
	return document, nil
}

func knownEvidenceKind(kind EvidenceKind) bool {
	switch kind {
	case EvidencePatch, EvidenceStructuredResult, EvidenceStdout, EvidenceStderr, EvidenceValidation:
		return true
	default:
		return false
	}
}

func rawEvidenceHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
