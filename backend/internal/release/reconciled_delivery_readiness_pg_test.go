package release

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestReconciledDeliveryReadinessRejectsCrossWriterAuthorityPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database, contents, receipt, bundle, cleanup := reconciledDeliveryPostgresFixture(t, ctx)
	defer cleanup()
	baseStore, err := NewStore(database, contents, reconciledDeliveryReceiptReader{receipt: receipt})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewReconciledDeliveryStore(baseStore, DeliveryControllerIdentity{
		SchemaVersion:  DeliveryControllerIdentitySchemaVersion,
		ID:             "readiness-cross-writer-controller",
		Version:        "2026.07.18+readiness",
		Protocol:       DeliveryControllerProtocolV3,
		TrustKeyDigest: reconciledDeliveryDigest("readiness-cross-writer-spki"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Readiness(ctx); err != nil {
		t.Fatalf("clean reconciled delivery readiness: %v", err)
	}

	deploymentID, runID := uuid.New(), uuid.New()
	err = database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		// Model an already-split authority left by an older binary. Migration 59
		// rejects this state during upgrade; readiness independently detects it
		// if triggers were bypassed or restored from an unsafe backup.
		if err := transaction.Exec(`SET LOCAL session_replication_role = replica`).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO deployments (
  id, project_id, environment, environment_ref, provider, status,
  version, created_by, created_at, updated_at
) VALUES (?, ?, 'preview', 'readiness-conflict', 'local-static', 'deploying',
          1, ?, statement_timestamp(), statement_timestamp())
`, deploymentID, bundle.ProjectID, bundle.CreatedBy).Error; err != nil {
			return err
		}
		return transaction.Exec(`
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES (?, 'release-preview-run/v2', ?, ?, ?, ?, ?,
          'readiness cross-writer conflict', 'queued', 1, ?, ?)
`, runID, bundle.ProjectID, bundle.ID, bundle.BundleHash,
			"readiness-cross-writer-"+runID.String(), reconciledDeliveryDigest("readiness-cross-writer-request"),
			bundle.CreatedBy, bundle.CreatedBy).Error
	})
	if err != nil {
		t.Fatal(err)
	}
	readinessErr := store.Readiness(ctx)
	if readinessErr == nil || !strings.Contains(readinessErr.Error(), "deploying legacy and active Release Controller v3 authority coexist") {
		t.Fatalf("cross-writer authority readiness error=%v, want explicit fail-closed result", readinessErr)
	}
}

func TestReconciledDeliveryReadinessRejectsReleaseAuthoritySchemaDriftPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database, contents, receipt, _, cleanup := reconciledDeliveryPostgresFixture(t, ctx)
	defer cleanup()
	baseStore, err := NewStore(database, contents, reconciledDeliveryReceiptReader{receipt: receipt})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewReconciledDeliveryStore(baseStore, DeliveryControllerIdentity{
		SchemaVersion: DeliveryControllerIdentitySchemaVersion,
		ID:            "readiness-nested-authority-controller", Version: "2026.07.18+nested-readiness",
		Protocol:       DeliveryControllerProtocolV3,
		TrustKeyDigest: reconciledDeliveryDigest("readiness-nested-authority-spki"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Readiness(ctx); err != nil {
		t.Fatalf("clean nested authority readiness: %v", err)
	}
	if err := database.WithContext(ctx).Exec(`
DROP TRIGGER release_delivery_operation_nested_authority_guard
ON release_delivery_operations
`).Error; err != nil {
		t.Fatal(err)
	}
	readinessErr := store.Readiness(ctx)
	if readinessErr == nil || !strings.Contains(readinessErr.Error(), requiredDeliveryReconciliationMigration) {
		t.Fatalf("missing nested authority readiness error=%v, want migration %s failure",
			readinessErr, requiredDeliveryReconciliationMigration)
	}
	if err := database.WithContext(ctx).Exec(`
CREATE TRIGGER release_delivery_operation_nested_authority_guard
BEFORE INSERT ON release_delivery_operations
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_nested_authority_insert()
`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.WithContext(ctx).Exec(`
ALTER TABLE release_delivery_operations
ENABLE REPLICA TRIGGER release_delivery_operation_nested_authority_guard
`).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Readiness(ctx); err == nil {
		t.Fatal("readiness accepted a nested authority trigger that does not fire for normal writers")
	}
	if err := database.WithContext(ctx).Exec(`
ALTER TABLE release_delivery_operations
ENABLE TRIGGER release_delivery_operation_nested_authority_guard
`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.WithContext(ctx).Exec(`
DROP TRIGGER release_preview_run_v2_insert_guard ON release_preview_runs
`).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Readiness(ctx); err == nil {
		t.Fatal("readiness accepted a missing v2 Preview Run admission trigger")
	}
	if err := database.WithContext(ctx).Exec(`
CREATE TRIGGER release_preview_run_v2_insert_guard
BEFORE INSERT ON release_preview_runs
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_run_insert_v2()
`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.WithContext(ctx).Exec(`
DROP TRIGGER release_preview_run_operation_authority_guard ON release_preview_runs
`).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Readiness(ctx); err == nil {
		t.Fatal("readiness accepted a missing deferred Preview Run/Operation authority guard")
	}
	if err := database.WithContext(ctx).Exec(`
CREATE CONSTRAINT TRIGGER release_preview_run_operation_authority_guard
AFTER INSERT ON release_preview_runs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_run_operation_authority()
`).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Readiness(ctx); err != nil {
		t.Fatalf("readiness did not recover after restoring exact authority objects: %v", err)
	}
	if err := database.WithContext(ctx).Exec(`DELETE FROM schema_migrations WHERE version = ?`,
		requiredDeliveryReconciliationMigration).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Readiness(ctx); err == nil {
		t.Fatal("readiness accepted authority objects without the exact recorded migration")
	}
}

func TestReconciledDeliveryReadinessRejectsSplitLegacyVersionAuthorityPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database, contents, receipt, bundle, cleanup := reconciledDeliveryPostgresFixture(t, ctx)
	defer cleanup()
	store := reconciledDeliveryReadinessStore(
		t, database, contents, reconciledDeliveryReceiptReader{receipt: receipt}, "split-legacy",
	)
	deploymentID := uuid.New()
	if err := database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Exec(`SET LOCAL session_replication_role = replica`).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO deployments (
  id, project_id, environment, environment_ref, provider, status,
  version, created_by, created_at, updated_at
) VALUES (?, ?, 'preview', 'split-readiness', 'local-static', 'ready',
          1, ?, statement_timestamp(), statement_timestamp())
`, deploymentID, bundle.ProjectID, bundle.CreatedBy).Error; err != nil {
			return err
		}
		return transaction.Exec(`
INSERT INTO deployment_versions (
  id, deployment_id, number, action,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  provider_ref, entry_path, checksum, file_count, total_bytes,
  environment_ref, environment_variable_names, status, message, created_by
) VALUES (?, ?, 1, 'publish', ?, ?, ?, '', '', '', 0, 0,
          'split-readiness', '[]'::jsonb, 'deploying', '', ?)
`, uuid.New(), deploymentID, uuid.New(), uuid.New(), reconciledDeliveryDigest("split-legacy-workspace"),
			bundle.CreatedBy).Error
	}); err != nil {
		t.Fatal(err)
	}
	readinessErr := store.Readiness(ctx)
	if readinessErr == nil || !strings.Contains(readinessErr.Error(), "deploying legacy version has a non-deploying parent") {
		t.Fatalf("split legacy authority readiness error=%v", readinessErr)
	}
}

func TestReconciledDeliveryReadinessRejectsOrphanV2RunPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database, contents, receipt, bundle, cleanup := reconciledDeliveryPostgresFixture(t, ctx)
	defer cleanup()
	store := reconciledDeliveryReadinessStore(
		t, database, contents, reconciledDeliveryReceiptReader{receipt: receipt}, "orphan-v2",
	)
	runID := uuid.New()
	if err := database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Exec(`SET LOCAL session_replication_role = replica`).Error; err != nil {
			return err
		}
		return transaction.Exec(`
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES (?, 'release-preview-run/v2', ?, ?, ?, ?, ?,
          'orphan readiness canary', 'queued', 1, ?, ?)
`, runID, bundle.ProjectID, bundle.ID, bundle.BundleHash, "orphan-v2-"+runID.String(),
			reconciledDeliveryDigest("orphan-v2-request"), bundle.CreatedBy, bundle.CreatedBy).Error
	}); err != nil {
		t.Fatal(err)
	}
	readinessErr := store.Readiness(ctx)
	if readinessErr == nil || !strings.Contains(readinessErr.Error(), "missing its exact Operation authority") {
		t.Fatalf("orphan v2 Run readiness error=%v", readinessErr)
	}
}

func reconciledDeliveryReadinessStore(
	t *testing.T,
	database *gorm.DB,
	contents *reconciledDeliveryMemoryContentStore,
	receipt CanonicalReceiptReader,
	label string,
) *ReconciledDeliveryStore {
	t.Helper()
	baseStore, err := NewStore(database, contents, receipt)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewReconciledDeliveryStore(baseStore, DeliveryControllerIdentity{
		SchemaVersion:  DeliveryControllerIdentitySchemaVersion,
		ID:             "readiness-" + label + "-controller",
		Version:        "2026.07.18+" + label,
		Protocol:       DeliveryControllerProtocolV3,
		TrustKeyDigest: reconciledDeliveryDigest("readiness-" + label + "-spki"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}
