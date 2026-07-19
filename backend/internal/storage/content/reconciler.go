package content

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
	"gorm.io/gorm"
)

type ReconcileConfig struct {
	GracePeriod time.Duration
	OrphanTTL   time.Duration
	BatchSize   int64
	Now         func() time.Time
}

type ReconcileStats struct {
	Examined  int `json:"examined"`
	Finalized int `json:"finalized"`
	Aborted   int `json:"aborted"`
}

type pendingContent struct {
	ID        string
	CreatedAt time.Time
}

type reconcileStore interface {
	listPending(context.Context, time.Time, int64) ([]pendingContent, error)
	Finalize(context.Context, string) error
	Abort(context.Context, string) error
}

// Reconciler closes the Mongo/PostgreSQL commit gap. Referenced pending
// objects are finalized; old unreferenced objects are aborted. It never treats
// Mongo as the reachability authority.
type Reconciler struct {
	database *gorm.DB
	store    reconcileStore
	config   ReconcileConfig
}

func NewReconciler(database *gorm.DB, store *MongoStore, config ReconcileConfig) (*Reconciler, error) {
	if database == nil || store == nil {
		return nil, errors.New("content reconciler database and store are required")
	}
	return newReconciler(database, store, config)
}

func newReconciler(database *gorm.DB, store reconcileStore, config ReconcileConfig) (*Reconciler, error) {
	if database == nil || store == nil {
		return nil, errors.New("content reconciler database and store are required")
	}
	if config.GracePeriod <= 0 {
		config.GracePeriod = 5 * time.Minute
	}
	if config.OrphanTTL <= config.GracePeriod {
		config.OrphanTTL = 24 * time.Hour
	}
	if config.BatchSize <= 0 || config.BatchSize > 1000 {
		config.BatchSize = 100
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Reconciler{database: database, store: store, config: config}, nil
}

func (s *MongoStore) listPending(ctx context.Context, createdBefore time.Time, limit int64) ([]pendingContent, error) {
	cursor, err := s.collection.Find(ctx, bson.M{
		"state": StatePending, "createdAt": bson.M{"$lte": createdBefore},
	}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var pending []document
	if err := cursor.All(ctx, &pending); err != nil {
		return nil, fmt.Errorf("decode pending content: %w", err)
	}
	items := make([]pendingContent, 0, len(pending))
	for _, item := range pending {
		items = append(items, pendingContent{ID: item.ID, CreatedAt: item.CreatedAt})
	}
	return items, nil
}

func (r *Reconciler) RunOnce(ctx context.Context) (ReconcileStats, error) {
	now := r.config.Now().UTC()
	pending, err := r.store.listPending(ctx, now.Add(-r.config.GracePeriod), r.config.BatchSize)
	if err != nil {
		return ReconcileStats{}, fmt.Errorf("list pending content: %w", err)
	}
	stats := ReconcileStats{Examined: len(pending)}
	for _, item := range pending {
		referenced, err := r.isReferenced(ctx, item.ID)
		if err != nil {
			return stats, err
		}
		if referenced {
			if err := r.store.Finalize(ctx, item.ID); err != nil {
				return stats, err
			}
			stats.Finalized++
			continue
		}
		if !item.CreatedAt.After(now.Add(-r.config.OrphanTTL)) {
			if err := r.store.Abort(ctx, item.ID); err != nil {
				return stats, err
			}
			stats.Aborted++
		}
	}
	return stats, nil
}

const coreReferenceQuery = `
SELECT EXISTS (
  SELECT 1 FROM artifact_drafts WHERE content_ref = @content_id
  UNION ALL SELECT 1 FROM artifact_revisions WHERE content_ref = @content_id
  UNION ALL SELECT 1 FROM input_manifests WHERE content_ref = @content_id
  UNION ALL SELECT 1 FROM output_proposals WHERE content_ref = @content_id
  UNION ALL SELECT 1 FROM application_build_manifests WHERE content_ref = @content_id
  UNION ALL SELECT 1 FROM application_build_contracts WHERE content_ref = @content_id
  UNION ALL SELECT 1 FROM implementation_proposals WHERE content_ref = @content_id
) AS referenced`

const repositoryReferenceQuery = `
SELECT EXISTS (
  SELECT 1 FROM repository_snapshots WHERE tree_ref = @content_id
  UNION ALL SELECT 1 FROM candidate_workspaces WHERE base_tree_ref = @content_id
  UNION ALL SELECT 1 FROM candidate_workspaces WHERE current_tree_ref = @content_id
  UNION ALL SELECT 1 FROM candidate_workspace_journal WHERE before_tree_ref = @content_id
  UNION ALL SELECT 1 FROM candidate_workspace_journal WHERE after_tree_ref = @content_id
  UNION ALL SELECT 1 FROM candidate_snapshots WHERE tree_ref = @content_id
) AS referenced`

const repositoryFileReferenceQuery = `
SELECT EXISTS (
  SELECT 1 FROM repository_file_blobs WHERE content_ref = @content_id
) AS referenced`

const agentEvidenceReferenceQuery = `
SELECT EXISTS (
  SELECT 1
  FROM agent_attempts
  WHERE evidence @> jsonb_build_object('patch', jsonb_build_object('ref', CAST(@content_id AS text)))
     OR evidence @> jsonb_build_object('structuredResult', jsonb_build_object('ref', CAST(@content_id AS text)))
     OR evidence @> jsonb_build_object('stdout', jsonb_build_object('ref', CAST(@content_id AS text)))
     OR evidence @> jsonb_build_object('stderr', jsonb_build_object('ref', CAST(@content_id AS text)))
     OR evidence @> jsonb_build_object('validation', jsonb_build_object('ref', CAST(@content_id AS text)))
) AS referenced`

type repositoryTableAvailability struct {
	RepositorySnapshots       bool `gorm:"column:repository_snapshots"`
	CandidateWorkspaces       bool `gorm:"column:candidate_workspaces"`
	CandidateWorkspaceJournal bool `gorm:"column:candidate_workspace_journal"`
	CandidateSnapshots        bool `gorm:"column:candidate_snapshots"`
	RepositoryFileBlobs       bool `gorm:"column:repository_file_blobs"`
	AgentAttempts             bool `gorm:"column:agent_attempts"`
}

type verificationTableAvailability struct {
	CandidatePlans      bool `gorm:"column:candidate_plans"`
	CandidateReceipts   bool `gorm:"column:candidate_receipts"`
	CanonicalPlans      bool `gorm:"column:canonical_plans"`
	CanonicalReceipts   bool `gorm:"column:canonical_receipts"`
	ReleaseBundles      bool `gorm:"column:release_bundles"`
	PreviewReceipts     bool `gorm:"column:preview_receipts"`
	PromotionApprovals  bool `gorm:"column:promotion_approvals"`
	ProductionReceipts  bool `gorm:"column:production_receipts"`
	DeploymentRevisions bool `gorm:"column:deployment_revisions"`
}

func (r *Reconciler) isReferenced(ctx context.Context, contentID string) (bool, error) {
	var referenced bool
	if err := r.database.WithContext(ctx).Raw(coreReferenceQuery, map[string]any{"content_id": contentID}).Scan(&referenced).Error; err != nil {
		return false, fmt.Errorf("check content references: %w", err)
	}
	if referenced {
		return true, nil
	}
	verificationAvailability, err := r.verificationTables(ctx)
	if err != nil {
		return false, fmt.Errorf("inspect verification content reference schema: %w", err)
	}
	referenced, err = r.verificationContentReferenced(ctx, contentID, verificationAvailability)
	if err != nil {
		return false, err
	}
	if referenced {
		return true, nil
	}

	availability, err := r.repositoryTables(ctx)
	if err != nil {
		return false, fmt.Errorf("inspect repository content reference schema: %w", err)
	}
	if availability.AgentAttempts {
		referenced = false
		if err := r.database.WithContext(ctx).Raw(
			agentEvidenceReferenceQuery,
			map[string]any{"content_id": contentID},
		).Scan(&referenced).Error; err != nil {
			return false, fmt.Errorf("check Agent evidence content references: %w", err)
		}
		if referenced {
			return true, nil
		}
	}
	availableCount := 0
	for _, available := range []bool{
		availability.RepositorySnapshots,
		availability.CandidateWorkspaces,
		availability.CandidateWorkspaceJournal,
		availability.CandidateSnapshots,
	} {
		if available {
			availableCount++
		}
	}
	if availableCount == 0 {
		if availability.RepositoryFileBlobs {
			return false, errors.New("check content references: repository reachability tables are partially migrated")
		}
		// Compatibility for rolling deployments and tests that predate the
		// repository schema. With no repository tables there cannot be a
		// repository-owned tree reference.
		return false, nil
	}
	if availableCount != 4 {
		// Migration 000023 creates these tables atomically. A partial set means
		// the schema is not trustworthy enough to classify content as orphaned.
		return false, errors.New("check content references: repository reachability tables are partially migrated")
	}

	referenced = false
	if err := r.database.WithContext(ctx).Raw(repositoryReferenceQuery, map[string]any{"content_id": contentID}).Scan(&referenced).Error; err != nil {
		return false, fmt.Errorf("check repository content references: %w", err)
	}
	if referenced || !availability.RepositoryFileBlobs {
		// File blobs arrived in migration 000026, after the four atomic 000023
		// tree tables. Its absence is therefore a valid rolling-upgrade state.
		return referenced, nil
	}
	referenced = false
	if err := r.database.WithContext(ctx).Raw(repositoryFileReferenceQuery, map[string]any{"content_id": contentID}).Scan(&referenced).Error; err != nil {
		return false, fmt.Errorf("check repository file content references: %w", err)
	}
	return referenced, nil
}

func (r *Reconciler) verificationTables(ctx context.Context) (verificationTableAvailability, error) {
	const query = `
SELECT
  to_regclass('candidate_verification_plans') IS NOT NULL AS candidate_plans,
  to_regclass('candidate_verification_receipts') IS NOT NULL AS candidate_receipts,
  to_regclass('canonical_verification_plans') IS NOT NULL AS canonical_plans,
  to_regclass('canonical_verification_receipts') IS NOT NULL AS canonical_receipts,
  to_regclass('release_bundles') IS NOT NULL AS release_bundles,
  to_regclass('release_preview_receipts') IS NOT NULL AS preview_receipts,
  to_regclass('release_promotion_approvals') IS NOT NULL AS promotion_approvals,
  to_regclass('release_production_receipts') IS NOT NULL AS production_receipts,
  to_regclass('release_deployment_revisions') IS NOT NULL AS deployment_revisions`
	var availability verificationTableAvailability
	if err := r.database.WithContext(ctx).Raw(query).Scan(&availability).Error; err != nil {
		return verificationTableAvailability{}, err
	}
	return availability, nil
}

func (r *Reconciler) verificationContentReferenced(
	ctx context.Context,
	contentID string,
	availability verificationTableAvailability,
) (bool, error) {
	deliveryTables := []bool{
		availability.PreviewReceipts,
		availability.PromotionApprovals,
		availability.ProductionReceipts,
		availability.DeploymentRevisions,
	}
	deliveryTableCount := 0
	for _, available := range deliveryTables {
		if available {
			deliveryTableCount++
		}
	}
	if deliveryTableCount > 0 && deliveryTableCount != len(deliveryTables) {
		return false, errors.New("check content references: release delivery reachability tables are partially migrated")
	}
	queries := []struct {
		available bool
		query     string
		name      string
	}{
		{availability.CandidatePlans, `SELECT EXISTS (SELECT 1 FROM candidate_verification_plans WHERE content_ref = @content_id)`, "Candidate VerificationPlan"},
		{availability.CandidateReceipts, `SELECT EXISTS (SELECT 1 FROM candidate_verification_receipts WHERE content_ref = @content_id)`, "Candidate VerificationReceipt"},
		{availability.CanonicalPlans, `SELECT EXISTS (SELECT 1 FROM canonical_verification_plans WHERE content_ref = @content_id)`, "Canonical VerificationPlan"},
		{availability.CanonicalReceipts, `SELECT EXISTS (SELECT 1 FROM canonical_verification_receipts WHERE content_ref = @content_id)`, "Canonical VerificationReceipt"},
		{availability.ReleaseBundles, `SELECT EXISTS (SELECT 1 FROM release_bundles WHERE content_ref = @content_id)`, "ReleaseBundle"},
		{availability.PreviewReceipts, `SELECT EXISTS (SELECT 1 FROM release_preview_receipts WHERE content_ref = @content_id)`, "PreviewReceipt"},
		{availability.PromotionApprovals, `SELECT EXISTS (SELECT 1 FROM release_promotion_approvals WHERE content_ref = @content_id)`, "PromotionApproval"},
		{availability.ProductionReceipts, `SELECT EXISTS (SELECT 1 FROM release_production_receipts WHERE content_ref = @content_id)`, "ProductionReceipt"},
		{availability.DeploymentRevisions, `SELECT EXISTS (SELECT 1 FROM release_deployment_revisions WHERE content_ref = @content_id)`, "DeploymentRevision"},
	}
	for _, check := range queries {
		if !check.available {
			continue
		}
		var referenced bool
		if err := r.database.WithContext(ctx).Raw(check.query, map[string]any{"content_id": contentID}).Scan(&referenced).Error; err != nil {
			return false, fmt.Errorf("check %s content references: %w", check.name, err)
		}
		if referenced {
			return true, nil
		}
	}
	return false, nil
}

func (r *Reconciler) repositoryTables(ctx context.Context) (repositoryTableAvailability, error) {
	const query = `
SELECT
  to_regclass('repository_snapshots') IS NOT NULL AS repository_snapshots,
  to_regclass('candidate_workspaces') IS NOT NULL AS candidate_workspaces,
  to_regclass('candidate_workspace_journal') IS NOT NULL AS candidate_workspace_journal,
  to_regclass('candidate_snapshots') IS NOT NULL AS candidate_snapshots,
  to_regclass('repository_file_blobs') IS NOT NULL AS repository_file_blobs,
  to_regclass('agent_attempts') IS NOT NULL AS agent_attempts`
	var availability repositoryTableAvailability
	if err := r.database.WithContext(ctx).Raw(query).Scan(&availability).Error; err != nil {
		return repositoryTableAvailability{}, err
	}
	return availability, nil
}
