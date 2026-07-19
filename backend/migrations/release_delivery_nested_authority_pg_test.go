package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/internal/domain"
)

func TestReleaseDeliveryNestedAuthorityPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()

	t.Run("upgrade lock rescans an older writer", func(t *testing.T) {
		schema := releaseOperationTestSchema(t, ctx, base, dsn)
		database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		applyReleaseOperationMigrationsThrough(
			t, ctx, database, "000059_legacy_deployment_release_controller_gate.up.sql",
		)

		actorID, projectID := uuid.New(), uuid.New()
		bundleID, runID, operationID := uuid.New(), uuid.New(), uuid.New()
		workspaceArtifactID, workspaceRevisionID, receiptID := uuid.New(), uuid.New(), uuid.New()
		workspaceHash := releaseOperationDigest("nested-lock-workspace")
		receiptHash := releaseOperationDigest("nested-lock-receipt")
		// Intentionally copied after a nested document change. Migration 56
		// accepts this projection; migration 60 must recompute and reject it.
		copiedBundleHash := releaseOperationDigest("nested-lock-copied-bundle")
		createdAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
		artifacts := releaseOperationArtifacts(t)
		if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'nested authority lock actor', 'not-used')
`, actorID, "nested-lock-"+uuid.NewString()+"@example.com"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, 'nested authority lock project', $2)
`, projectID, actorID); err != nil {
			t.Fatal(err)
		}
		seed, err := database.BeginTx(ctx, nil)
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
`, bundleID, projectID, workspaceArtifactID, workspaceRevisionID, workspaceHash,
			receiptID, receiptHash, artifacts, "blob://nested-lock/"+bundleID.String(),
			releaseOperationDigest("nested-lock-content"), copiedBundleHash, actorID, createdAt); err != nil {
			seed.Rollback()
			t.Fatal(err)
		}
		if err := seed.Commit(); err != nil {
			t.Fatal(err)
		}
		reason := "nested authority writer serialization"
		if _, err := database.ExecContext(ctx, previewRunInsertWithReasonSQL,
			runID, projectID, bundleID, copiedBundleHash, "nested-lock-"+runID.String(),
			releaseOperationDigest("nested-lock-api"), reason, actorID,
		); err != nil {
			t.Fatal(err)
		}

		var releaseArtifacts any
		if err := json.Unmarshal(artifacts, &releaseArtifacts); err != nil {
			t.Fatal(err)
		}
		bundleDocument := map[string]any{
			"schemaVersion": "release-bundle/v1", "id": bundleID.String(), "projectId": projectID.String(),
			"workspace": map[string]any{
				"workspaceArtifactId": workspaceArtifactID.String(), "workspaceRevisionId": workspaceRevisionID.String(),
				"workspaceContentHash": workspaceHash,
			},
			"canonicalReceipt": map[string]any{"id": receiptID.String(), "contentHash": receiptHash},
			"buildManifest": map[string]any{
				"id": uuid.NewString(), "contentHash": releaseOperationDigest("nested-lock-tampered-manifest"),
			},
			"buildContract":       map[string]any{"id": uuid.NewString(), "contentHash": releaseOperationDigest("nested-lock-contract")},
			"fullStackTemplate":   map[string]any{"id": uuid.NewString(), "contentHash": releaseOperationDigest("nested-lock-template")},
			"verificationProfile": map[string]any{"id": "release", "version": 1, "contentHash": releaseOperationDigest("nested-lock-profile")},
			"releaseArtifacts":    releaseArtifacts, "bundleHash": copiedBundleHash,
			"createdBy": actorID.String(), "createdAt": createdAt,
		}
		payload := map[string]any{
			"schemaVersion": "release-preview-operation-payload/v1", "operationId": operationID.String(),
			"runId": runID.String(), "projectId": projectID.String(), "reason": reason,
			"namespace": "nested-lock-preview", "releaseBundle": bundleDocument,
		}
		document, err := domain.CanonicalJSON(map[string]any{
			"schemaVersion": "release-delivery-operation-document/v3", "operationId": operationID.String(),
			"kind": "preview", "projectId": projectID.String(), "payload": payload,
		})
		if err != nil {
			t.Fatal(err)
		}

		writer, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.ExecContext(ctx, `
INSERT INTO release_delivery_operations (
  id, schema_version, project_id, kind, preview_run_id,
  request_schema_version, request_document, request_hash,
  controller_schema_version, controller_id, controller_version,
  controller_protocol, controller_trust_key_digest, remote_state, created_by
) VALUES ($1, 'release-delivery-operation/v1', $2, 'preview', $3,
          'release-delivery-operation-request/v3', $4, $5,
          'release-delivery-controller-identity/v1', 'nested-lock-controller', '3.0.0',
          'worksflow.release-delivery/v3', $6, 'prepared', $7)
`, operationID, projectID, runID, document, releaseOperationBytesDigest(document),
			releaseOperationDigest("nested-lock-trust"), actorID); err != nil {
			writer.Rollback()
			t.Fatalf("migration 56 unexpectedly rejected the outer-valid nested-invalid Operation: %v", err)
		}
		up, err := files.ReadFile("000060_release_delivery_nested_authority.up.sql")
		if err != nil {
			writer.Rollback()
			t.Fatal(err)
		}
		migrationResult := make(chan error, 1)
		go func() {
			migration, beginErr := database.BeginTx(ctx, nil)
			if beginErr != nil {
				migrationResult <- beginErr
				return
			}
			defer migration.Rollback()
			_, applyErr := migration.ExecContext(ctx, string(up))
			if applyErr == nil {
				applyErr = migration.Commit()
			}
			migrationResult <- applyErr
		}()
		assertAdmissionWaitsForSharedProjectLock(t, migrationResult)
		if err := writer.Commit(); err != nil {
			t.Fatal(err)
		}
		applyErr := receiveAdmissionResult(t, migrationResult)
		if applyErr == nil || !strings.Contains(applyErr.Error(), "noncanonical embedded fact") {
			t.Fatalf("serialized nested authority upgrade error=%v, want post-lock rescan failure", applyErr)
		}
	})

	t.Run("hash helper is total for missing input", func(t *testing.T) {
		schema := releaseOperationTestSchema(t, ctx, base, dsn)
		database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		applyReleaseOperationMigrationsThrough(
			t, ctx, database, "000060_release_delivery_nested_authority.up.sql",
		)
		var missingDocument, missingField bool
		if err := database.QueryRowContext(ctx, `
SELECT
  release_delivery_embedded_hash_is_exact(NULL, 'bundleHash'),
  release_delivery_embedded_hash_is_exact('{}'::jsonb, 'bundleHash')
`).Scan(&missingDocument, &missingField); err != nil {
			t.Fatal(err)
		}
		if missingDocument || missingField {
			t.Fatalf("nested authority helper accepted missing document=%v field=%v", missingDocument, missingField)
		}
	})
}

const previewRunInsertWithReasonSQL = `
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES ($1, 'release-preview-run/v2', $2, $3, $4, $5, $6,
          $7, 'queued', 1, $8, $8)
`
