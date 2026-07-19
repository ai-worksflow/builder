package release

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"github.com/worksflow/builder/backend/internal/verification"
	"gorm.io/gorm"
)

const bundleAggregateType = "release_bundle"

type ContentStore interface {
	PutPending(context.Context, string, string, string, int, json.RawMessage) (content.Reference, error)
	Finalize(context.Context, string) error
	Abort(context.Context, string) error
	Get(context.Context, string, string) (content.StoredContent, error)
}

type CanonicalReceiptReader interface {
	GetCanonicalReceipt(context.Context, string, string, string) (verification.CanonicalReceipt, error)
}

type Store struct {
	database *gorm.DB
	contents ContentStore
	receipts CanonicalReceiptReader
	now      func() time.Time
}

func NewStore(database *gorm.DB, contents ContentStore, receipts CanonicalReceiptReader) (*Store, error) {
	if database == nil || contents == nil || receipts == nil {
		return nil, errors.New("release database, content store, and Canonical Receipt reader are required")
	}
	return &Store{database: database, contents: contents, receipts: receipts, now: time.Now}, nil
}

type bundleRow struct {
	ID                   string          `gorm:"column:id"`
	SchemaVersion        string          `gorm:"column:schema_version"`
	ProjectID            string          `gorm:"column:project_id"`
	WorkspaceArtifactID  string          `gorm:"column:workspace_artifact_id"`
	WorkspaceRevisionID  string          `gorm:"column:workspace_revision_id"`
	WorkspaceContentHash string          `gorm:"column:workspace_content_hash"`
	CanonicalReceiptID   string          `gorm:"column:canonical_receipt_id"`
	CanonicalReceiptHash string          `gorm:"column:canonical_receipt_hash"`
	ReleaseArtifacts     json.RawMessage `gorm:"column:release_artifacts"`
	ContentStore         string          `gorm:"column:content_store"`
	ContentRef           string          `gorm:"column:content_ref"`
	ContentHash          string          `gorm:"column:content_hash"`
	BundleHash           string          `gorm:"column:bundle_hash"`
	CreatedBy            string          `gorm:"column:created_by"`
	CreatedAt            time.Time       `gorm:"column:created_at"`
}

func (bundleRow) TableName() string { return "release_bundles" }

func (store *Store) Create(
	ctx context.Context,
	projectID, receiptID, receiptHash, bundleID, actorID string,
) (Bundle, error) {
	receipt, err := store.receipts.GetCanonicalReceipt(ctx, projectID, receiptID, receiptHash)
	if err != nil {
		return Bundle{}, fmt.Errorf("load exact passing Canonical Receipt: %w", err)
	}
	bundle, err := NewBundle(NewBundleInput{
		ID: bundleID, Receipt: receipt, CreatedBy: actorID, CreatedAt: store.now().UTC(),
	})
	if err != nil {
		return Bundle{}, err
	}
	payload, err := domain.CanonicalJSON(bundle)
	if err != nil {
		return Bundle{}, fmt.Errorf("%w: encode Bundle: %v", ErrBundleIntegrity, err)
	}
	contentRef, err := store.contents.PutPending(ctx, projectID, bundleAggregateType, bundle.ID, 1, payload)
	if err != nil {
		return Bundle{}, err
	}
	abort := true
	defer func() {
		if abort {
			_ = store.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	artifacts, err := json.Marshal(bundle.ReleaseArtifacts)
	if err != nil {
		return Bundle{}, err
	}
	var persisted bundleRow
	replayed := false
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Exec(`
INSERT INTO release_bundles (
  id, schema_version, project_id,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  canonical_receipt_id, canonical_receipt_hash, release_artifacts,
  content_store, content_ref, content_hash, bundle_hash, created_by, created_at
) VALUES (?, 'release-bundle/v1', ?, ?, ?, ?, ?, ?, ?::jsonb, 'mongo', ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING
`, bundle.ID, bundle.ProjectID, bundle.Workspace.WorkspaceArtifactID,
			bundle.Workspace.WorkspaceRevisionID, bundle.Workspace.WorkspaceContentHash,
			bundle.CanonicalReceipt.ID, bundle.CanonicalReceipt.ContentHash, string(artifacts),
			contentRef.ID, contentRef.ContentHash, bundle.BundleHash, bundle.CreatedBy, bundle.CreatedAt)
		if result.Error != nil {
			return result.Error
		}
		replayed = result.RowsAffected == 0
		query := transaction.Where("id = ?", bundle.ID)
		if replayed {
			query = transaction.Where(
				"workspace_revision_id = ? AND workspace_content_hash = ? AND canonical_receipt_id = ? AND canonical_receipt_hash = ?",
				bundle.Workspace.WorkspaceRevisionID, bundle.Workspace.WorkspaceContentHash,
				bundle.CanonicalReceipt.ID, bundle.CanonicalReceipt.ContentHash,
			)
		}
		if err := query.Take(&persisted).Error; errors.Is(err, gorm.ErrRecordNotFound) && replayed {
			return ErrBundleConflict
		} else if err != nil {
			return err
		}
		if persisted.BundleHash != bundle.BundleHash || persisted.ProjectID != bundle.ProjectID {
			return ErrBundleConflict
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Bundle{}, err
	}
	if replayed {
		_ = store.contents.Abort(context.Background(), contentRef.ID)
		abort = false
		return store.hydrate(ctx, persisted)
	}
	abort = false
	if err := store.contents.Finalize(ctx, contentRef.ID); err != nil {
		return Bundle{}, fmt.Errorf("%w: finalize Bundle content: %v", core.ErrContentNotReady, err)
	}
	return store.Get(ctx, projectID, bundle.ID, bundle.BundleHash)
}

func (store *Store) Get(ctx context.Context, projectID, bundleID, bundleHash string) (Bundle, error) {
	var row bundleRow
	err := store.database.WithContext(ctx).
		Where("id = ? AND project_id = ? AND bundle_hash = ?", bundleID, projectID, bundleHash).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Bundle{}, ErrBundleNotFound
	}
	if err != nil {
		return Bundle{}, err
	}
	return store.hydrate(ctx, row)
}

func (store *Store) GetByReceipt(
	ctx context.Context,
	projectID, receiptID, receiptHash string,
) (Bundle, error) {
	var row bundleRow
	err := store.database.WithContext(ctx).
		Where("project_id = ? AND canonical_receipt_id = ? AND canonical_receipt_hash = ?", projectID, receiptID, receiptHash).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Bundle{}, ErrBundleNotFound
	}
	if err != nil {
		return Bundle{}, err
	}
	return store.hydrate(ctx, row)
}

func (store *Store) hydrate(ctx context.Context, row bundleRow) (Bundle, error) {
	stored, err := store.contents.Get(ctx, row.ContentRef, row.ContentHash)
	if err != nil {
		return Bundle{}, fmt.Errorf("%w: load content: %v", ErrBundleIntegrity, err)
	}
	if stored.ProjectID != row.ProjectID || stored.AggregateType != bundleAggregateType ||
		stored.AggregateID != row.ID || stored.SchemaVersion != 1 || stored.State != content.StateFinalized ||
		row.SchemaVersion != BundleSchemaVersion || row.ContentStore != "mongo" ||
		stored.ID != row.ContentRef || stored.ContentHash != row.ContentHash {
		return Bundle{}, fmt.Errorf("%w: content identity", ErrBundleIntegrity)
	}
	var bundle Bundle
	if err := json.Unmarshal(stored.Payload, &bundle); err != nil {
		return Bundle{}, fmt.Errorf("%w: decode content: %v", ErrBundleIntegrity, err)
	}
	parsed, err := ParseBundle(bundle)
	if err != nil {
		return Bundle{}, fmt.Errorf("%w: %v", ErrBundleIntegrity, err)
	}
	var projected []verification.CanonicalReleaseArtifact
	if err := json.Unmarshal(row.ReleaseArtifacts, &projected); err != nil {
		return Bundle{}, fmt.Errorf("%w: decode artifact projection", ErrBundleIntegrity)
	}
	if parsed.ID != row.ID || parsed.ProjectID != row.ProjectID ||
		parsed.Workspace.WorkspaceArtifactID != row.WorkspaceArtifactID ||
		parsed.Workspace.WorkspaceRevisionID != row.WorkspaceRevisionID ||
		parsed.Workspace.WorkspaceContentHash != row.WorkspaceContentHash ||
		parsed.CanonicalReceipt.ID != row.CanonicalReceiptID ||
		parsed.CanonicalReceipt.ContentHash != row.CanonicalReceiptHash ||
		parsed.BundleHash != row.BundleHash || parsed.CreatedBy != row.CreatedBy ||
		!parsed.CreatedAt.Equal(row.CreatedAt) ||
		!sameArtifacts(parsed.ReleaseArtifacts, projected) {
		return Bundle{}, fmt.Errorf("%w: SQL projection mismatch", ErrBundleIntegrity)
	}
	receipt, err := store.receipts.GetCanonicalReceipt(
		ctx, row.ProjectID, row.CanonicalReceiptID, row.CanonicalReceiptHash,
	)
	if err != nil || receipt.Subject != parsed.Workspace || receipt.BuildManifest != parsed.BuildManifest ||
		receipt.BuildContract != parsed.BuildContract || receipt.FullStackTemplate != parsed.FullStackTemplate ||
		receipt.Profile != parsed.VerificationProfile || !sameArtifacts(receipt.ReleaseArtifacts, parsed.ReleaseArtifacts) {
		return Bundle{}, fmt.Errorf("%w: Canonical Receipt projection mismatch", ErrBundleIntegrity)
	}
	return parsed, nil
}
