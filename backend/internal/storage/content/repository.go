package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const defaultMaxContentBytes int64 = 16 << 20

var (
	ErrContentNotFound = errors.New("content not found")
	ErrHashMismatch    = errors.New("content hash mismatch")
	ErrContentTooLarge = errors.New("content exceeds configured size limit")
)

type State string

const (
	StatePending   State = "pending"
	StateFinalized State = "finalized"
	StateAborted   State = "aborted"
)

type Reference struct {
	ID            string `json:"id"`
	ContentHash   string `json:"contentHash"`
	ByteSize      int64  `json:"byteSize"`
	SchemaVersion int    `json:"schemaVersion"`
}

type StoredContent struct {
	Reference
	ProjectID     string          `json:"projectId"`
	AggregateType string          `json:"aggregateType"`
	AggregateID   string          `json:"aggregateId"`
	State         State           `json:"state"`
	Payload       json.RawMessage `json:"payload"`
	CreatedAt     time.Time       `json:"createdAt"`
	FinalizedAt   *time.Time      `json:"finalizedAt,omitempty"`
}

type Store interface {
	PutPending(ctx context.Context, projectID, aggregateType, aggregateID string, schemaVersion int, payload json.RawMessage) (Reference, error)
	Finalize(ctx context.Context, contentID string) error
	Abort(ctx context.Context, contentID string) error
	Get(ctx context.Context, contentID, expectedHash string) (StoredContent, error)
}

type MongoStore struct {
	collection *mongo.Collection
	maxBytes   int64
	now        func() time.Time
}

type document struct {
	ID            string          `bson:"_id"`
	ProjectID     string          `bson:"projectId"`
	AggregateType string          `bson:"aggregateType"`
	AggregateID   string          `bson:"aggregateId"`
	SchemaVersion int             `bson:"schemaVersion"`
	ContentHash   string          `bson:"contentHash"`
	ByteSize      int64           `bson:"byteSize"`
	State         State           `bson:"state"`
	Payload       json.RawMessage `bson:"payload"`
	CreatedAt     time.Time       `bson:"createdAt"`
	FinalizedAt   *time.Time      `bson:"finalizedAt,omitempty"`
	AbortedAt     *time.Time      `bson:"abortedAt,omitempty"`
}

func NewMongoStore(database *mongo.Database, maxBytes int64) *MongoStore {
	if maxBytes <= 0 {
		maxBytes = defaultMaxContentBytes
	}
	return &MongoStore{
		collection: database.Collection("artifact_contents"),
		maxBytes:   maxBytes,
		now:        time.Now,
	}
}

func (s *MongoStore) EnsureIndexes(ctx context.Context) error {
	// Pending objects must not be deduplicated: two concurrent PostgreSQL
	// transactions cannot safely share a pending object's lifecycle. Remove the
	// earlier unique indexes and use a lookup index instead.
	for _, name := range []string{"project_content_hash_unique", "aggregate_content_hash_unique"} {
		if _, err := s.collection.Indexes().DropOne(ctx, name); err != nil {
			var commandError mongo.CommandError
			if !errors.As(err, &commandError) || commandError.Code != 27 {
				return fmt.Errorf("drop legacy content index %s: %w", name, err)
			}
		}
	}
	_, err := s.collection.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "projectId", Value: 1}, {Key: "aggregateType", Value: 1},
				{Key: "aggregateId", Value: 1}, {Key: "contentHash", Value: 1},
			},
			Options: options.Index().SetName("aggregate_content_hash_lookup"),
		},
		{
			Keys:    bson.D{{Key: "aggregateType", Value: 1}, {Key: "aggregateId", Value: 1}, {Key: "createdAt", Value: -1}},
			Options: options.Index().SetName("aggregate_history"),
		},
		{
			Keys:    bson.D{{Key: "state", Value: 1}, {Key: "createdAt", Value: 1}},
			Options: options.Index().SetName("pending_cleanup"),
		},
	})
	return err
}

func (s *MongoStore) PutPending(
	ctx context.Context,
	projectID, aggregateType, aggregateID string,
	schemaVersion int,
	payload json.RawMessage,
) (Reference, error) {
	if projectID == "" || aggregateType == "" || aggregateID == "" {
		return Reference{}, errors.New("project, aggregate type and aggregate id are required")
	}
	if schemaVersion < 1 {
		return Reference{}, errors.New("schema version must be positive")
	}
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return Reference{}, fmt.Errorf("canonicalize content: %w", err)
	}
	if int64(len(canonical)) > s.maxBytes {
		return Reference{}, ErrContentTooLarge
	}
	hash := contentHash(canonical)
	now := s.now().UTC()
	stored := document{
		ID: uuid.NewString(), ProjectID: projectID, AggregateType: aggregateType,
		AggregateID: aggregateID, SchemaVersion: schemaVersion, ContentHash: hash,
		ByteSize: int64(len(canonical)), State: StatePending, Payload: canonical, CreatedAt: now,
	}
	_, err = s.collection.InsertOne(ctx, stored)
	if err != nil {
		return Reference{}, fmt.Errorf("insert content: %w", err)
	}
	return referenceFromDocument(stored), nil
}

func (s *MongoStore) Finalize(ctx context.Context, contentID string) error {
	now := s.now().UTC()
	result, err := s.collection.UpdateOne(ctx, bson.M{
		"_id":   contentID,
		"state": bson.M{"$in": bson.A{StatePending, StateFinalized}},
	}, bson.M{"$set": bson.M{"state": StateFinalized, "finalizedAt": now}})
	if err != nil {
		return fmt.Errorf("finalize content: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrContentNotFound
	}
	return nil
}

func (s *MongoStore) Abort(ctx context.Context, contentID string) error {
	now := s.now().UTC()
	result, err := s.collection.UpdateOne(ctx, bson.M{
		"_id": contentID, "state": StatePending,
	}, bson.M{"$set": bson.M{"state": StateAborted, "abortedAt": now}})
	if err != nil {
		return fmt.Errorf("abort content: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrContentNotFound
	}
	return nil
}

func (s *MongoStore) Get(ctx context.Context, contentID, expectedHash string) (StoredContent, error) {
	var stored document
	// A PostgreSQL row is the authoritative reachability check. Allow referenced
	// pending content to be read so a process crash after the SQL commit but
	// before Finalize does not make an otherwise committed revision unavailable.
	// Orphan pending objects remain unreachable through business APIs and are
	// eligible for asynchronous cleanup.
	err := s.collection.FindOne(ctx, bson.M{
		"_id": contentID, "state": bson.M{"$in": bson.A{StatePending, StateFinalized}},
	}).Decode(&stored)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return StoredContent{}, ErrContentNotFound
	}
	if err != nil {
		return StoredContent{}, fmt.Errorf("get content: %w", err)
	}
	actualHash := contentHash(stored.Payload)
	if actualHash != stored.ContentHash || (expectedHash != "" && actualHash != expectedHash) {
		return StoredContent{}, ErrHashMismatch
	}
	return StoredContent{
		Reference: referenceFromDocument(stored), ProjectID: stored.ProjectID,
		AggregateType: stored.AggregateType, AggregateID: stored.AggregateID,
		State: stored.State, Payload: cloneBytes(stored.Payload), CreatedAt: stored.CreatedAt,
		FinalizedAt: stored.FinalizedAt,
	}, nil
}

func referenceFromDocument(stored document) Reference {
	return Reference{
		ID: stored.ID, ContentHash: stored.ContentHash,
		ByteSize: stored.ByteSize, SchemaVersion: stored.SchemaVersion,
	}
}

func contentHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func canonicalJSON(payload []byte) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values are not allowed")
		}
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
}
