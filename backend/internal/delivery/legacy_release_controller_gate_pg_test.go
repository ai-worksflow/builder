package delivery

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	platformdomain "github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/release"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/verification"
	"gorm.io/gorm"
)

func TestLegacyPublishServicePostgresHonorsReleaseControllerGateAndHistoricalReads(t *testing.T) {
	database, cleanup := publishLineagePostgresDatabase(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	actorID, projectID := uuid.New(), uuid.New()
	publishLineageSeedUserProject(t, database, actorID, projectID, "legacy-controller-service")
	bundleID := uuid.New()
	createdAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	bundle := legacyControllerReleaseBundle(t, bundleID, projectID, actorID, createdAt)
	releaseArtifacts, err := json.Marshal(bundle.ReleaseArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	seed, err := sqlDatabase.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		seed.Rollback()
		t.Fatal(err)
	}
	if _, err := seed.ExecContext(ctx, `
INSERT INTO release_bundles (
  id, schema_version, project_id, workspace_artifact_id, workspace_revision_id,
  workspace_content_hash, canonical_receipt_id, canonical_receipt_hash,
  release_artifacts, content_store, content_ref, content_hash, bundle_hash,
  created_by, created_at
) VALUES ($1, 'release-bundle/v1', $2, $3, $4, $5, $6, $7,
          $8::jsonb, 'blob', $9, $10, $11, $12, $13)
`, bundleID, projectID, bundle.Workspace.WorkspaceArtifactID, bundle.Workspace.WorkspaceRevisionID,
		bundle.Workspace.WorkspaceContentHash, bundle.CanonicalReceipt.ID, bundle.CanonicalReceipt.ContentHash,
		releaseArtifacts,
		"blob://legacy-controller/"+bundleID.String(), publishLineagePGHash("legacy-controller-content"),
		bundle.BundleHash, actorID, createdAt); err != nil {
		seed.Rollback()
		t.Fatal(err)
	}
	if err := seed.Commit(); err != nil {
		t.Fatal(err)
	}
	runID := uuid.New()
	reason := "service gate canary"
	operation, err := release.NewDeliveryOperationRequest(
		runID.String(), release.DeliveryOperationPreview, projectID.String(),
		release.PreviewDeliveryOperationPayload{
			SchemaVersion: release.PreviewDeliveryOperationPayloadSchema,
			OperationID:   runID.String(), RunID: runID.String(), ProjectID: projectID.String(),
			Reason: reason, Namespace: "legacy-controller-service", ReleaseBundle: bundle,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if result := transaction.Exec(`
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES (?, 'release-preview-run/v2', ?, ?, ?, ?, ?,
		  ?, 'queued', 1, ?, ?)
`, runID, projectID, bundleID, bundle.BundleHash, "service-gate-"+runID.String(),
			publishLineagePGHash("legacy-controller-request"), reason, actorID, actorID); result.Error != nil {
			return result.Error
		}
		return transaction.Exec(`
INSERT INTO release_delivery_operations (
  id, schema_version, project_id, kind, preview_run_id,
  request_schema_version, request_document, request_hash,
  controller_schema_version, controller_id, controller_version,
  controller_protocol, controller_trust_key_digest, remote_state, created_by
) VALUES (?, 'release-delivery-operation/v1', ?, 'preview', ?,
          ?, ?, ?, 'release-delivery-controller-identity/v1',
          'legacy-controller-service', 'test-v3', 'worksflow.release-delivery/v3',
          ?, 'prepared', ?)
`, runID, projectID, runID, operation.SchemaVersion, string(operation.RequestDocument),
			operation.RequestHash, publishLineagePGHash("legacy-controller-trust"), actorID).Error
	}); err != nil {
		t.Fatal(err)
	}

	err = database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		return lockLegacyPreviewDeliveryAuthority(transaction, projectID)
	})
	if typed, ok := AsError(err); !ok || typed.Code != CodeConflict ||
		typed.Status != http.StatusConflict || typed.Detail != legacyPreviewControllerConflictDetail {
		t.Fatalf("active v2 authority was not mapped to the stable preview conflict: %v", err)
	}

	productionID := uuid.New()
	if err := database.Exec(`
INSERT INTO deployments (
  id, project_id, environment, environment_ref, provider, status,
  version, created_by, created_at, updated_at
) VALUES (?, ?, 'production', 'historical', 'local-static', 'ready',
          7, ?, statement_timestamp(), statement_timestamp())
`, productionID, projectID, actorID).Error; err != nil {
		t.Fatal(err)
	}
	service := &PublishService{database: database, access: publishLineagePGAccess{}}
	historical, err := service.Get(ctx, productionID.String(), actorID.String())
	if err != nil || historical.ID != productionID.String() || historical.Environment != EnvironmentProduction ||
		historical.Status != "ready" || historical.Version != 7 || len(historical.Versions) != 0 {
		t.Fatalf("historical production deployment read = %+v err=%v", historical, err)
	}
	_, err = service.Rollback(ctx, productionID.String(), actorID.String(), historical.ETag, RollbackInput{
		TargetVersionID: uuid.NewString(),
	})
	if typed, ok := AsError(err); !ok || typed.Code != CodeConflict || typed.Detail != legacyProductionControllerConflictDetail {
		t.Fatalf("legacy production rollback did not fail closed: %v", err)
	}
	_, err = service.Publish(ctx, projectID.String(), actorID.String(), "", PublishInput{Environment: EnvironmentProduction})
	if typed, ok := AsError(err); !ok || typed.Code != CodeConflict || typed.Detail != legacyProductionControllerConflictDetail {
		t.Fatalf("legacy production publish did not fail closed: %v", err)
	}

	var versionCount int64
	if err := database.Model(&deploymentVersionModel{}).Where("deployment_id = ?", productionID).Count(&versionCount).Error; err != nil {
		t.Fatal(err)
	}
	if versionCount != 0 {
		t.Fatalf("production rejection wrote %d legacy versions", versionCount)
	}
}

func legacyControllerReleaseBundle(
	t *testing.T,
	bundleID, projectID, actorID uuid.UUID,
	createdAt time.Time,
) release.Bundle {
	t.Helper()
	kinds := []string{
		"health-readiness-contract", "migration", "provenance", "runtime-config-schema",
		"sbom", "signature", "vulnerability-report", "web-static",
	}
	artifacts := make([]verification.CanonicalReleaseArtifact, 0, len(kinds))
	for _, kind := range kinds {
		artifacts = append(artifacts, verification.CanonicalReleaseArtifact{
			ID: "legacy-controller-" + kind, Kind: kind, Store: "blob",
			Ref:         "blob://legacy-controller/" + kind,
			ContentHash: publishLineagePGHash("legacy-controller-" + kind),
			MediaType:   "application/octet-stream", ByteSize: 1,
		})
	}
	bundle := release.Bundle{
		SchemaVersion: release.BundleSchemaVersion,
		ID:            bundleID.String(), ProjectID: projectID.String(),
		Workspace: verification.CanonicalPlanSubject{
			WorkspaceArtifactID: uuid.NewString(), WorkspaceRevisionID: uuid.NewString(),
			WorkspaceContentHash: publishLineagePGHash("legacy-controller-workspace"),
		},
		CanonicalReceipt: repository.ExactReference{
			ID: uuid.NewString(), ContentHash: publishLineagePGHash("legacy-controller-receipt"),
		},
		BuildManifest: repository.ExactReference{
			ID: uuid.NewString(), ContentHash: publishLineagePGHash("legacy-controller-manifest"),
		},
		BuildContract: repository.ExactReference{
			ID: uuid.NewString(), ContentHash: publishLineagePGHash("legacy-controller-contract"),
		},
		FullStackTemplate: repository.ExactReference{
			ID: uuid.NewString(), ContentHash: publishLineagePGHash("legacy-controller-template"),
		},
		VerificationProfile: verification.ProfileReference{
			ID: "legacy-controller-profile", Version: 1,
			ContentHash: publishLineagePGHash("legacy-controller-profile"),
		},
		ReleaseArtifacts: artifacts, CreatedBy: actorID.String(), CreatedAt: createdAt,
	}
	hash, err := platformdomain.CanonicalHash(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundle.BundleHash = "sha256:" + hash
	parsed, err := release.ParseBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestLegacyPreviewGateFailsClosedWithoutPostgresFencing(t *testing.T) {
	err := lockLegacyPreviewDeliveryAuthority(nil, uuid.New())
	if typed, ok := AsError(err); !ok || typed.Code != CodeConflict {
		t.Fatalf("missing PostgreSQL fencing was not fail-closed: %v", err)
	}
}
