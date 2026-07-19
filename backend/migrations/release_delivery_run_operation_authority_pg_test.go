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

type releaseRunOperationAuthorityFixture struct {
	actorID               uuid.UUID
	projectID             uuid.UUID
	bundleID              uuid.UUID
	bundleHash            string
	bundleDocument        map[string]any
	previewReceiptID      uuid.UUID
	previewReceiptHash    string
	promotionApprovalID   uuid.UUID
	promotionApprovalHash string
}

func TestReleaseDeliveryRunOperationAuthorityPostgresMigration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()

	t.Run("upgrade rejects an orphan v2 Run before installing guards", func(t *testing.T) {
		database := releaseRunOperationAuthorityDatabase(t, ctx, base, dsn)
		applyReleaseOperationMigrationsThrough(
			t, ctx, database, "000060_release_delivery_nested_authority.up.sql",
		)
		fixture := seedReleaseRunOperationAuthorityFixture(t, ctx, database)
		insertReleaseRunOperationAuthorityPreviewRun(
			t, ctx, database, fixture, uuid.New(), "upgrade-orphan",
		)

		up, err := files.ReadFile("000061_release_delivery_run_operation_authority.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, applyErr := transaction.ExecContext(ctx, string(up))
		_ = transaction.Rollback()
		if applyErr == nil || !strings.Contains(applyErr.Error(), "Preview Run lacks exactly one matching delivery Operation") {
			t.Fatalf("orphan v2 Run migration error=%v, want explicit fail-closed rejection", applyErr)
		}
		assertReleaseRunOperationAuthorityObjects(t, ctx, database, false)
	})

	t.Run("deferred guards reject Run-only commits and allow Run then Operation", func(t *testing.T) {
		database := releaseRunOperationAuthorityDatabase(t, ctx, base, dsn)
		applyReleaseOperationMigrationsThrough(
			t, ctx, database, "000061_release_delivery_run_operation_authority.up.sql",
		)
		assertReleaseRunOperationAuthorityObjects(t, ctx, database, true)
		fixture := seedReleaseRunOperationAuthorityFixture(t, ctx, database)

		orphanPreviewID := uuid.New()
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := insertReleaseRunOperationAuthorityPreviewRunTx(
			ctx, transaction, fixture, orphanPreviewID, "direct-preview-orphan",
		); err != nil {
			transaction.Rollback()
			t.Fatalf("insert direct orphan Preview Run before deferred check: %v", err)
		}
		if err := transaction.Commit(); err == nil || !strings.Contains(err.Error(), "v2 Preview Run must commit with exactly one matching delivery Operation") {
			t.Fatalf("direct orphan Preview Run commit=%v, want deferred authority rejection", err)
		}

		orphanProductionID := uuid.New()
		transaction, err = database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := insertReleaseRunOperationAuthorityProductionRunTx(
			ctx, transaction, fixture, orphanProductionID, "direct-production-orphan",
		); err != nil {
			transaction.Rollback()
			t.Fatalf("insert direct orphan Deployment Run before deferred check: %v", err)
		}
		if err := transaction.Commit(); err == nil || !strings.Contains(err.Error(), "v2 Deployment Run must commit with exactly one matching delivery Operation") {
			t.Fatalf("direct orphan Deployment Run commit=%v, want deferred authority rejection", err)
		}

		runID, operationID := uuid.New(), uuid.New()
		reason := "atomic Run and exact Operation"
		requestKey := "atomic-" + runID.String()
		apiRequestHash := releaseOperationDigest("api-" + runID.String())
		payload := map[string]any{
			"schemaVersion": "release-preview-operation-payload/v1",
			"operationId":   operationID.String(),
			"runId":         runID.String(),
			"projectId":     fixture.projectID.String(),
			"reason":        reason,
			"namespace":     "preview-atomic",
			"releaseBundle": fixture.bundleDocument,
		}
		requestDocument, err := domain.CanonicalJSON(map[string]any{
			"schemaVersion": "release-delivery-operation-document/v3",
			"operationId":   operationID.String(),
			"kind":          "preview",
			"projectId":     fixture.projectID.String(),
			"payload":       payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		transaction, err = database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES ($1, 'release-preview-run/v2', $2, $3, $4, $5, $6, $7,
          'queued', 1, $8, $8)
`, runID, fixture.projectID, fixture.bundleID, fixture.bundleHash,
			requestKey, apiRequestHash, reason, fixture.actorID); err != nil {
			transaction.Rollback()
			t.Fatalf("insert atomic v2 Preview Run: %v", err)
		}
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_delivery_operations (
  id, schema_version, project_id, kind, preview_run_id,
  request_schema_version, request_document, request_hash,
  controller_schema_version, controller_id, controller_version,
  controller_protocol, controller_trust_key_digest, remote_state, created_by
) VALUES ($1, 'release-delivery-operation/v1', $2, 'preview', $3,
          'release-delivery-operation-request/v3', $4, $5,
          'release-delivery-controller-identity/v1', 'authority-canary', '3.0.0',
          'worksflow.release-delivery/v3', $6, 'prepared', $7)
`, operationID, fixture.projectID, runID, requestDocument,
			releaseOperationBytesDigest(requestDocument), releaseOperationDigest("authority-canary-trust"),
			fixture.actorID); err != nil {
			transaction.Rollback()
			t.Fatalf("insert exact Operation after its Run: %v", err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatalf("commit atomic Run then Operation: %v", err)
		}
		var matching int
		if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM release_preview_runs AS run
JOIN release_delivery_operations AS operation
  ON operation.preview_run_id = run.id
 AND operation.project_id = run.project_id
 AND operation.kind = 'preview'
WHERE run.id = $1 AND run.schema_version = 'release-preview-run/v2'
`, runID).Scan(&matching); err != nil || matching != 1 {
			t.Fatalf("committed Run/Operation authority count=%d err=%v", matching, err)
		}

		down, err := files.ReadFile("000061_release_delivery_run_operation_authority.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, string(down)); err == nil || !strings.Contains(err.Error(), "cannot downgrade v2 Run operation authority") {
			t.Fatalf("nonempty v2 authority downgrade=%v, want fail-closed rejection", err)
		}
		assertReleaseRunOperationAuthorityObjects(t, ctx, database, true)
	})

	t.Run("historical v1 Run does not block install or empty-v2 downgrade", func(t *testing.T) {
		database := releaseRunOperationAuthorityDatabase(t, ctx, base, dsn)
		applyReleaseOperationMigrationsThrough(
			t, ctx, database, "000060_release_delivery_nested_authority.up.sql",
		)
		fixture := seedReleaseRunOperationAuthorityFixture(t, ctx, database)
		legacyRunID := uuid.New()
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			transaction.Rollback()
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES ($1, 'release-preview-run/v1', $2, $3, $4, $5, $6,
          'historical v1 authority', 'reconcile_blocked', 1, $7, $7)
`, legacyRunID, fixture.projectID, fixture.bundleID, fixture.bundleHash,
			"legacy-"+legacyRunID.String(), releaseOperationDigest("legacy-"+legacyRunID.String()),
			fixture.actorID); err != nil {
			transaction.Rollback()
			t.Fatalf("seed historical v1 Run: %v", err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}

		applyReleaseOperationMigration(
			t, ctx, database, "000061_release_delivery_run_operation_authority.up.sql",
		)
		assertReleaseRunOperationAuthorityObjects(t, ctx, database, true)
		down, err := files.ReadFile("000061_release_delivery_run_operation_authority.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		transaction, err = database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, string(down)); err != nil {
			transaction.Rollback()
			t.Fatalf("downgrade empty-v2 authority with v1 history: %v", err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
		assertReleaseRunOperationAuthorityObjects(t, ctx, database, false)
		var legacyCount int
		if err := database.QueryRowContext(ctx,
			`SELECT count(*) FROM release_preview_runs WHERE id = $1 AND schema_version = 'release-preview-run/v1'`,
			legacyRunID,
		).Scan(&legacyCount); err != nil || legacyCount != 1 {
			t.Fatalf("historical v1 Run count=%d err=%v after authority downgrade", legacyCount, err)
		}
	})
}

func releaseRunOperationAuthorityDatabase(
	t *testing.T,
	ctx context.Context,
	base *sql.DB,
	dsn string,
) *sql.DB {
	t.Helper()
	schema := releaseOperationTestSchema(t, ctx, base, dsn)
	database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func seedReleaseRunOperationAuthorityFixture(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) releaseRunOperationAuthorityFixture {
	t.Helper()
	fixture := releaseRunOperationAuthorityFixture{
		actorID: uuid.New(), projectID: uuid.New(), bundleID: uuid.New(),
		previewReceiptID: uuid.New(), previewReceiptHash: releaseOperationDigest("authority-preview-receipt-" + uuid.NewString()),
		promotionApprovalID: uuid.New(), promotionApprovalHash: releaseOperationDigest("authority-approval-" + uuid.NewString()),
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'Run operation authority actor', 'not-used')
`, fixture.actorID, "run-operation-authority-"+uuid.NewString()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, 'Run operation authority project', $2)
`, fixture.projectID, fixture.actorID); err != nil {
		t.Fatal(err)
	}

	workspaceArtifactID, workspaceRevisionID := uuid.New(), uuid.New()
	canonicalReceiptID := uuid.New()
	workspaceHash := releaseOperationDigest("authority-workspace-" + fixture.bundleID.String())
	canonicalReceiptHash := releaseOperationDigest("authority-canonical-receipt-" + fixture.bundleID.String())
	createdAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	artifacts := releaseOperationArtifacts(t)
	fixture.bundleDocument = map[string]any{
		"schemaVersion": "release-bundle/v1",
		"id":            fixture.bundleID.String(),
		"projectId":     fixture.projectID.String(),
		"workspace": map[string]any{
			"workspaceArtifactId":  workspaceArtifactID.String(),
			"workspaceRevisionId":  workspaceRevisionID.String(),
			"workspaceContentHash": workspaceHash,
		},
		"canonicalReceipt": map[string]any{
			"id":          canonicalReceiptID.String(),
			"contentHash": canonicalReceiptHash,
		},
		"buildManifest": map[string]any{
			"id": uuid.NewString(), "contentHash": releaseOperationDigest("authority-manifest-" + fixture.bundleID.String()),
		},
		"buildContract": map[string]any{
			"id": uuid.NewString(), "contentHash": releaseOperationDigest("authority-contract-" + fixture.bundleID.String()),
		},
		"fullStackTemplate": map[string]any{
			"id": uuid.NewString(), "contentHash": releaseOperationDigest("authority-template-" + fixture.bundleID.String()),
		},
		"verificationProfile": map[string]any{
			"id": "release", "version": 1, "contentHash": releaseOperationDigest("authority-profile-" + fixture.bundleID.String()),
		},
		"releaseArtifacts": json.RawMessage(artifacts),
		"bundleHash":       "",
		"createdBy":        fixture.actorID.String(),
		"createdAt":        createdAt,
	}
	blankBundle, err := domain.CanonicalJSON(fixture.bundleDocument)
	if err != nil {
		t.Fatal(err)
	}
	fixture.bundleHash = releaseOperationBytesDigest(blankBundle)
	fixture.bundleDocument["bundleHash"] = fixture.bundleHash

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		transaction.Rollback()
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_bundles (
  id, schema_version, project_id, workspace_artifact_id, workspace_revision_id,
  workspace_content_hash, canonical_receipt_id, canonical_receipt_hash,
  release_artifacts, content_store, content_ref, content_hash, bundle_hash,
  created_by, created_at
) VALUES ($1, 'release-bundle/v1', $2, $3, $4, $5, $6, $7,
          $8::jsonb, 'blob', $9, $10, $11, $12, $13)
`, fixture.bundleID, fixture.projectID, workspaceArtifactID, workspaceRevisionID,
		workspaceHash, canonicalReceiptID, canonicalReceiptHash, artifacts,
		"blob://run-operation-authority/"+fixture.bundleID.String(),
		releaseOperationDigest("authority-bundle-content-"+fixture.bundleID.String()),
		fixture.bundleHash, fixture.actorID, createdAt); err != nil {
		transaction.Rollback()
		t.Fatalf("seed authority Bundle: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_preview_receipts (
  id, schema_version, run_id, project_id, release_bundle_id, release_bundle_hash,
  canonical_receipt_id, canonical_receipt_hash,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  release_artifacts, namespace, provider, provider_ref, checks, decision,
  content_store, content_ref, content_hash, payload_hash, created_by, created_at
) VALUES (
  $1, 'release-preview-receipt/v1', $2, $3, $4, $5, $6, $7,
  $8, $9, $10, $11::jsonb, 'authority-preview', 'canary', 'preview/ref',
  '[{"id":"migration","kind":"migration","status":"passed"}]'::jsonb, 'passed',
  'blob', $12, $13, $14, $15, $16
)
`, fixture.previewReceiptID, uuid.New(), fixture.projectID, fixture.bundleID, fixture.bundleHash,
		canonicalReceiptID, canonicalReceiptHash, workspaceArtifactID, workspaceRevisionID, workspaceHash,
		artifacts, "blob://run-operation-authority/preview/"+fixture.previewReceiptID.String(),
		releaseOperationDigest("authority-preview-content-"+fixture.bundleID.String()),
		fixture.previewReceiptHash, fixture.actorID, createdAt); err != nil {
		transaction.Rollback()
		t.Fatalf("seed authority PreviewReceipt dependency: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_promotion_approvals (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  preview_receipt_id, preview_receipt_hash, reason,
  content_store, content_ref, content_hash, payload_hash, created_by, created_at
) VALUES ($1, 'release-promotion-approval/v1', $2, $3, $4, $5, $6,
          'authority canary approval', 'blob', $7, $8, $9, $10, $11)
`, fixture.promotionApprovalID, fixture.projectID, fixture.bundleID, fixture.bundleHash,
		fixture.previewReceiptID, fixture.previewReceiptHash,
		"blob://run-operation-authority/approval/"+fixture.promotionApprovalID.String(),
		releaseOperationDigest("authority-approval-content-"+fixture.bundleID.String()),
		fixture.promotionApprovalHash, fixture.actorID, createdAt); err != nil {
		transaction.Rollback()
		t.Fatalf("seed authority PromotionApproval dependency: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func insertReleaseRunOperationAuthorityPreviewRun(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture releaseRunOperationAuthorityFixture,
	runID uuid.UUID,
	label string,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertReleaseRunOperationAuthorityPreviewRunTx(ctx, transaction, fixture, runID, label); err != nil {
		transaction.Rollback()
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func insertReleaseRunOperationAuthorityPreviewRunTx(
	ctx context.Context,
	transaction *sql.Tx,
	fixture releaseRunOperationAuthorityFixture,
	runID uuid.UUID,
	label string,
) error {
	_, err := transaction.ExecContext(ctx, `
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES ($1, 'release-preview-run/v2', $2, $3, $4, $5, $6, $7,
          'queued', 1, $8, $8)
`, runID, fixture.projectID, fixture.bundleID, fixture.bundleHash,
		label+"-"+runID.String(), releaseOperationDigest(label+"-"+runID.String()),
		label, fixture.actorID)
	return err
}

func insertReleaseRunOperationAuthorityProductionRunTx(
	ctx context.Context,
	transaction *sql.Tx,
	fixture releaseRunOperationAuthorityFixture,
	runID uuid.UUID,
	label string,
) error {
	_, err := transaction.ExecContext(ctx, `
INSERT INTO release_deployment_runs (
  id, schema_version, project_id, environment, operation,
  release_bundle_id, release_bundle_hash,
  preview_receipt_id, preview_receipt_hash,
  promotion_approval_id, promotion_approval_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES ($1, 'release-deployment-run/v2', $2, 'production', 'promote',
          $3, $4, $5, $6, $7, $8, $9, $10, $11, 'queued', 1, $12, $12)
`, runID, fixture.projectID, fixture.bundleID, fixture.bundleHash,
		fixture.previewReceiptID, fixture.previewReceiptHash,
		fixture.promotionApprovalID, fixture.promotionApprovalHash,
		label+"-"+runID.String(), releaseOperationDigest(label+"-"+runID.String()),
		label, fixture.actorID)
	return err
}

func assertReleaseRunOperationAuthorityObjects(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	want bool,
) {
	t.Helper()
	var triggerCount int
	var functionPresent bool
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM pg_trigger
   WHERE tgname IN (
     'release_preview_run_operation_authority_guard',
     'release_deployment_run_operation_authority_guard'
   )
     AND tgrelid IN (
       'release_preview_runs'::regclass,
       'release_deployment_runs'::regclass
     )
     AND NOT tgisinternal),
  to_regprocedure('validate_release_delivery_run_operation_authority()') IS NOT NULL
`).Scan(&triggerCount, &functionPresent); err != nil {
		t.Fatal(err)
	}
	wantTriggers := 0
	if want {
		wantTriggers = 2
	}
	if triggerCount != wantTriggers || functionPresent != want {
		t.Fatalf("authority objects triggers=%d function=%t, want triggers=%d function=%t",
			triggerCount, functionPresent, wantTriggers, want)
	}
}
