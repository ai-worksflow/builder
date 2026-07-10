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

// Reconciler closes the Mongo/PostgreSQL commit gap. Referenced pending
// objects are finalized; old unreferenced objects are aborted. It never treats
// Mongo as the reachability authority.
type Reconciler struct {
	database *gorm.DB
	store    *MongoStore
	config   ReconcileConfig
}

func NewReconciler(database *gorm.DB, store *MongoStore, config ReconcileConfig) (*Reconciler, error) {
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

func (r *Reconciler) RunOnce(ctx context.Context) (ReconcileStats, error) {
	now := r.config.Now().UTC()
	cursor, err := r.store.collection.Find(ctx, bson.M{
		"state": StatePending, "createdAt": bson.M{"$lte": now.Add(-r.config.GracePeriod)},
	}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(r.config.BatchSize))
	if err != nil {
		return ReconcileStats{}, fmt.Errorf("list pending content: %w", err)
	}
	defer cursor.Close(ctx)
	var pending []document
	if err := cursor.All(ctx, &pending); err != nil {
		return ReconcileStats{}, fmt.Errorf("decode pending content: %w", err)
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

func (r *Reconciler) isReferenced(ctx context.Context, contentID string) (bool, error) {
	const query = `
SELECT EXISTS (
  SELECT 1 FROM artifact_drafts WHERE content_ref = @content_id
  UNION ALL SELECT 1 FROM artifact_revisions WHERE content_ref = @content_id
  UNION ALL SELECT 1 FROM input_manifests WHERE content_ref = @content_id
  UNION ALL SELECT 1 FROM output_proposals WHERE content_ref = @content_id
  UNION ALL SELECT 1 FROM application_build_manifests WHERE content_ref = @content_id
  UNION ALL SELECT 1 FROM implementation_proposals WHERE content_ref = @content_id
) AS referenced`
	var referenced bool
	if err := r.database.WithContext(ctx).Raw(query, map[string]any{"content_id": contentID}).Scan(&referenced).Error; err != nil {
		return false, fmt.Errorf("check content references: %w", err)
	}
	return referenced, nil
}
