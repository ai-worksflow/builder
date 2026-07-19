package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/internal/domain"
)

func TestReleaseDeliveryOperationReconciliationPostgresCanary(t *testing.T) {
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

	t.Run("exact result closes a run while API and controller hashes stay distinct", func(t *testing.T) {
		schema := releaseOperationTestSchema(t, ctx, base, dsn)
		database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		applyReleaseOperationMigrationsThrough(t, ctx, database, "000055_template_artifact_authority_receipts.up.sql")

		actorID, projectID := uuid.New(), uuid.New()
		bundleID, workspaceArtifactID, workspaceRevisionID := uuid.New(), uuid.New(), uuid.New()
		canonicalReceiptID := uuid.New()
		bundleHash := releaseOperationDigest("bundle")
		workspaceHash := releaseOperationDigest("workspace")
		canonicalReceiptHash := releaseOperationDigest("canonical-receipt")
		createdAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
		if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'release operation actor', 'not-used')
`, actorID, "release-operation-"+uuid.NewString()+"@example.com"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, 'release operation project', $2)
`, projectID, actorID); err != nil {
			t.Fatal(err)
		}
		artifacts := releaseOperationArtifacts(t)
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			transaction.Rollback()
			t.Fatalf("canary requires the isolated PostgreSQL test role to disable lineage triggers: %v", err)
		}
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_bundles (
  id, schema_version, project_id, workspace_artifact_id, workspace_revision_id,
  workspace_content_hash, canonical_receipt_id, canonical_receipt_hash,
  release_artifacts, content_store, content_ref, content_hash, bundle_hash,
  created_by, created_at
) VALUES ($1, 'release-bundle/v1', $2, $3, $4, $5, $6, $7,
          $8::jsonb, 'blob', $9, $10, $11, $12, $13)
`, bundleID, projectID, workspaceArtifactID, workspaceRevisionID, workspaceHash,
			canonicalReceiptID, canonicalReceiptHash, artifacts,
			"blob://release-operation-bundle/"+bundleID.String(),
			releaseOperationDigest("bundle-content"), bundleHash, actorID, createdAt); err != nil {
			transaction.Rollback()
			t.Fatalf("seed immutable Bundle: %v", err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}

		legacyRunID := uuid.New()
		if _, err := database.ExecContext(ctx, `
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES ($1, 'release-preview-run/v1', $2, $3, $4, $5, $6,
          'legacy active delivery', 'queued', 1, $7, $7)
`, legacyRunID, projectID, bundleID, bundleHash, "legacy-"+legacyRunID.String(),
			releaseOperationDigest("legacy-api-request"), actorID); err != nil {
			t.Fatalf("insert legacy active Run: %v", err)
		}
		applyReleaseOperationMigration(t, ctx, database, "000056_release_delivery_operation_reconciliation.up.sql")

		var legacyState string
		if err := database.QueryRowContext(ctx, `SELECT state FROM release_preview_runs WHERE id = $1`, legacyRunID).Scan(&legacyState); err != nil || legacyState != "reconcile_blocked" {
			t.Fatalf("legacy active Run state=%q err=%v, want reconcile_blocked", legacyState, err)
		}
		var productionSingleFlight string
		if err := database.QueryRowContext(ctx, `
SELECT indexdef FROM pg_indexes
WHERE schemaname = current_schema()
  AND indexname = 'release_deployment_runs_one_nonterminal_environment_idx'
`).Scan(&productionSingleFlight); err != nil ||
			!strings.Contains(productionSingleFlight, "reconcile_wait") ||
			!strings.Contains(productionSingleFlight, "reconcile_blocked") {
			t.Fatalf("production single-flight does not retain uncertain authority: %q err=%v", productionSingleFlight, err)
		}

		canonicalFixture := map[string]any{
			"integer": 42,
			"reason":  "deploy <>& \"quoted\" \\\\ slash\u2028line\u2029end",
			"nested":  []any{map[string]any{"detail": "雪 & <tag>"}, true, nil},
		}
		canonicalBytes, err := domain.CanonicalJSON(canonicalFixture)
		if err != nil {
			t.Fatal(err)
		}
		var postgresCanonical string
		if err := database.QueryRowContext(ctx,
			`SELECT release_delivery_canonical_json($1::jsonb)`, canonicalBytes,
		).Scan(&postgresCanonical); err != nil {
			t.Fatal(err)
		}
		if postgresCanonical != string(canonicalBytes) {
			t.Fatalf("Go/PG canonical bytes differ\nGo: %s\nPG: %s", canonicalBytes, postgresCanonical)
		}
		if _, err := database.ExecContext(ctx, `SELECT release_delivery_canonical_json('{"n":1.5}'::jsonb)`); err == nil {
			t.Fatal("canonical PostgreSQL function accepted a non-integral number")
		}

		runID, operationID := uuid.New(), uuid.New()
		apiRequestHash := releaseOperationDigest("api-idempotency-request")
		reason := "preview <>& replay boundary"
		if _, err := database.ExecContext(ctx, `
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES ($1, 'release-preview-run/v2', $2, $3, $4, $5, $6, $7,
          'queued', 1, $8, $8)
`, runID, projectID, bundleID, bundleHash, "v2-"+runID.String(), apiRequestHash, reason, actorID); err != nil {
			t.Fatalf("insert v2 Run: %v", err)
		}
		bundleDocument := map[string]any{
			"schemaVersion": "release-bundle/v1", "id": bundleID.String(), "projectId": projectID.String(),
			"workspace":           map[string]any{"workspaceArtifactId": workspaceArtifactID.String(), "workspaceRevisionId": workspaceRevisionID.String(), "workspaceContentHash": workspaceHash},
			"canonicalReceipt":    map[string]any{"id": canonicalReceiptID.String(), "contentHash": canonicalReceiptHash},
			"buildManifest":       map[string]any{"id": uuid.NewString(), "contentHash": releaseOperationDigest("manifest")},
			"buildContract":       map[string]any{"id": uuid.NewString(), "contentHash": releaseOperationDigest("contract")},
			"fullStackTemplate":   map[string]any{"id": uuid.NewString(), "contentHash": releaseOperationDigest("template")},
			"verificationProfile": map[string]any{"id": "release", "version": 1, "contentHash": releaseOperationDigest("profile")},
			"releaseArtifacts":    json.RawMessage(artifacts), "bundleHash": bundleHash,
			"createdBy": actorID.String(), "createdAt": createdAt,
		}
		payload := map[string]any{
			"schemaVersion": "release-preview-operation-payload/v1", "operationId": operationID.String(),
			"runId": runID.String(), "projectId": projectID.String(), "reason": reason,
			"namespace": "preview-<>&", "releaseBundle": bundleDocument,
		}
		document := map[string]any{
			"schemaVersion": "release-delivery-operation-document/v3", "operationId": operationID.String(),
			"kind": "preview", "projectId": projectID.String(), "payload": payload,
		}
		requestDocument, err := domain.CanonicalJSON(document)
		if err != nil {
			t.Fatal(err)
		}
		controllerRequestHash := releaseOperationBytesDigest(requestDocument)
		if controllerRequestHash == apiRequestHash {
			t.Fatal("test fixture accidentally reused the API idempotency hash")
		}
		trustDigest := releaseOperationDigest("controller-trust")
		if _, err := database.ExecContext(ctx, `
INSERT INTO release_delivery_operations (
  id, schema_version, project_id, kind, preview_run_id,
  request_schema_version, request_document, request_hash,
  controller_schema_version, controller_id, controller_version,
  controller_protocol, controller_trust_key_digest, remote_state, created_by
) VALUES ($1, 'release-delivery-operation/v1', $2, 'preview', $3,
          'release-delivery-operation-request/v3', $4, $5,
          'release-delivery-controller-identity/v1', 'canary-controller', '3.0.0',
          'worksflow.release-delivery/v3', $6, 'prepared', $7)
`, operationID, projectID, runID, requestDocument, controllerRequestHash, trustDigest, actorID); err != nil {
			t.Fatalf("insert exact Operation: %v", err)
		}
		worker := "release-operation-canary"
		if _, err := database.ExecContext(ctx, `
UPDATE release_preview_runs
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = $2, lease_epoch = 1,
    lease_expires_at = statement_timestamp() + interval '5 minutes',
	started_at = statement_timestamp(), updated_at = clock_timestamp()
WHERE id = $1
`, runID, worker); err != nil {
			t.Fatalf("claim v2 Run with distinct hashes: %v", err)
		}
		if _, err := database.ExecContext(ctx, `
UPDATE release_preview_runs
SET state = 'submitting', version = 3, updated_at = clock_timestamp()
WHERE id = $1
`, runID); err != nil {
			t.Fatalf("advance v2 Run to submitting: %v", err)
		}
		if _, err := database.ExecContext(ctx, `
UPDATE release_preview_runs
SET state = 'error', version = 4, lease_worker_id = NULL, lease_epoch = NULL,
    lease_expires_at = NULL, finished_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = $1
`, runID); err == nil || !strings.Contains(err.Error(), "active-to-error") {
			t.Fatalf("active Run reached error without an exact rejected Result: %v", err)
		}
		if _, err := database.ExecContext(ctx, `
INSERT INTO release_delivery_operation_attempts (
  operation_id, schema_version, kind, worker_id, fence_epoch
) VALUES ($1, 'release-delivery-operation-attempt/v1', 'submit', $2, 1)
`, operationID, worker); err != nil {
			t.Fatalf("insert submit Attempt: %v", err)
		}

		completedAt := time.Now().UTC().Truncate(time.Microsecond)
		observedAt := completedAt.Add(time.Microsecond)
		controller := map[string]any{
			"schemaVersion": "release-delivery-controller-identity/v1", "id": "canary-controller",
			"version": "3.0.0", "protocol": "worksflow.release-delivery/v3", "trustKeyDigest": trustDigest,
		}
		resultPayload := map[string]any{
			"schemaVersion": "release-delivery-operation-result/v1", "controller": controller,
			"operationId": operationID.String(), "requestHash": controllerRequestHash,
			"kind": "preview", "projectId": projectID.String(), "status": "rejected",
			"checks": []any{}, "noMutation": true, "rejectionCode": "POLICY_REJECTED",
			"rejectionDetail": "no mutation <>&", "completedAt": completedAt, "resultHash": "",
		}
		resultHashBytes, err := domain.CanonicalJSON(resultPayload)
		if err != nil {
			t.Fatal(err)
		}
		resultHash := releaseOperationBytesDigest(resultHashBytes)
		resultPayload["resultHash"] = resultHash
		resultDocument, err := domain.CanonicalJSON(resultPayload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `
UPDATE release_delivery_operation_attempts
SET outcome = 'rejected', http_status = 409, response_hash = $2,
    observation_sequence = 1, observed_at = $3
WHERE operation_id = $1 AND kind = 'submit' AND fence_epoch = 1
`, operationID, resultHash, observedAt); err != nil {
			t.Fatalf("complete rejected Attempt: %v", err)
		}

		transaction, err = database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_delivery_operation_results (
  operation_id, schema_version, request_hash, project_id, kind, status,
  controller_schema_version, controller_id, controller_version, controller_protocol,
  controller_trust_key_digest, checks, no_mutation, rejection_code, rejection_detail,
  worker_id, fence_epoch, completed_at, result_document, result_hash
) VALUES ($1, 'release-delivery-operation-result/v1', $2, $3, 'preview', 'rejected',
          'release-delivery-controller-identity/v1', 'canary-controller', '3.0.0',
          'worksflow.release-delivery/v3', $4, '[]'::jsonb, true,
          'POLICY_REJECTED', 'no mutation <>&', $5, 1, $6, $7, $8)
`, operationID, controllerRequestHash, projectID, trustDigest, worker,
			completedAt, resultDocument, resultHash); err != nil {
			transaction.Rollback()
			t.Fatalf("insert exact controller Result: %v", err)
		}
		if _, err := transaction.ExecContext(ctx, `
UPDATE release_delivery_operations
SET remote_state = 'rejected', last_observation_sequence = 1,
    last_observed_at = $2, terminal_result_hash = $3, updated_at = clock_timestamp()
WHERE id = $1
`, operationID, observedAt, resultHash); err != nil {
			transaction.Rollback()
			t.Fatalf("terminalize exact Operation: %v", err)
		}
		if _, err := transaction.ExecContext(ctx, `
UPDATE release_preview_runs
SET state = 'error', version = 4, lease_worker_id = NULL, lease_epoch = NULL,
    lease_expires_at = NULL, finished_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = $1
`, runID); err != nil {
			transaction.Rollback()
			t.Fatalf("commit Result -> Operation -> Run exact transaction: %v", err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatalf("commit exact terminal transaction: %v", err)
		}
		var state, remoteState string
		if err := database.QueryRowContext(ctx, `
SELECT run.state, operation.remote_state
FROM release_preview_runs AS run
JOIN release_delivery_operations AS operation ON operation.preview_run_id = run.id
WHERE run.id = $1 AND run.request_hash = $2 AND operation.request_hash = $3
`, runID, apiRequestHash, controllerRequestHash).Scan(&state, &remoteState); err != nil || state != "error" || remoteState != "rejected" {
			t.Fatalf("terminal exact authority state=%q remote=%q err=%v", state, remoteState, err)
		}
		if _, err := database.ExecContext(ctx, `UPDATE release_delivery_operation_results SET rejection_detail = 'tampered' WHERE operation_id = $1`, operationID); err == nil {
			t.Fatal("immutable controller Result was mutable")
		}

		// A reconcile transport timeout after an accepted observation must retain
		// accepted remote authority, preserve its sequence, and schedule later
		// reconciliation. It must not fabricate submit_unknown or busy-loop.
		timeoutRunID, timeoutOperationID := uuid.New(), uuid.New()
		timeoutAPIHash := releaseOperationDigest("timeout-api-idempotency")
		timeoutReason := "accepted reconcile timeout"
		if _, err := database.ExecContext(ctx, `
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES ($1, 'release-preview-run/v2', $2, $3, $4, $5, $6, $7,
          'queued', 1, $8, $8)
`, timeoutRunID, projectID, bundleID, bundleHash, "timeout-"+timeoutRunID.String(),
			timeoutAPIHash, timeoutReason, actorID); err != nil {
			t.Fatalf("insert timeout v2 Run: %v", err)
		}
		timeoutPayload := map[string]any{
			"schemaVersion": "release-preview-operation-payload/v1", "operationId": timeoutOperationID.String(),
			"runId": timeoutRunID.String(), "projectId": projectID.String(), "reason": timeoutReason,
			"namespace": "preview-timeout", "releaseBundle": bundleDocument,
		}
		timeoutDocument, err := domain.CanonicalJSON(map[string]any{
			"schemaVersion": "release-delivery-operation-document/v3", "operationId": timeoutOperationID.String(),
			"kind": "preview", "projectId": projectID.String(), "payload": timeoutPayload,
		})
		if err != nil {
			t.Fatal(err)
		}
		timeoutControllerHash := releaseOperationBytesDigest(timeoutDocument)
		if _, err := database.ExecContext(ctx, `
INSERT INTO release_delivery_operations (
  id, schema_version, project_id, kind, preview_run_id,
  request_schema_version, request_document, request_hash,
  controller_schema_version, controller_id, controller_version,
  controller_protocol, controller_trust_key_digest, remote_state, created_by
) VALUES ($1, 'release-delivery-operation/v1', $2, 'preview', $3,
          'release-delivery-operation-request/v3', $4, $5,
          'release-delivery-controller-identity/v1', 'canary-controller', '3.0.0',
          'worksflow.release-delivery/v3', $6, 'prepared', $7)
`, timeoutOperationID, projectID, timeoutRunID, timeoutDocument,
			timeoutControllerHash, trustDigest, actorID); err != nil {
			t.Fatalf("insert timeout Operation: %v", err)
		}
		if _, err := database.ExecContext(ctx, `
UPDATE release_preview_runs
SET state = 'claimed', version = 2, fence_epoch = 1, lease_worker_id = $2,
    lease_epoch = 1, lease_expires_at = statement_timestamp() + interval '5 minutes',
    started_at = statement_timestamp(), updated_at = clock_timestamp()
WHERE id = $1
`, timeoutRunID, worker); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `
UPDATE release_preview_runs SET state = 'submitting', version = 3, updated_at = clock_timestamp()
WHERE id = $1
`, timeoutRunID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `
INSERT INTO release_delivery_operation_attempts (
  operation_id, schema_version, kind, worker_id, fence_epoch
) VALUES ($1, 'release-delivery-operation-attempt/v1', 'submit', $2, 1)
`, timeoutOperationID, worker); err != nil {
			t.Fatal(err)
		}
		acceptedObservedAt := time.Now().UTC().Truncate(time.Microsecond)
		if _, err := database.ExecContext(ctx, `
UPDATE release_delivery_operation_attempts
SET outcome = 'accepted', http_status = 202, response_hash = $2,
    observation_sequence = 1, observed_at = $3
WHERE operation_id = $1 AND kind = 'submit' AND fence_epoch = 1
`, timeoutOperationID, releaseOperationDigest("accepted-observation"), acceptedObservedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `
UPDATE release_delivery_operations
SET remote_state = 'accepted', next_attempt_at = statement_timestamp(),
    last_observation_sequence = 1, last_observed_at = $2, updated_at = clock_timestamp()
WHERE id = $1
`, timeoutOperationID, acceptedObservedAt); err != nil {
			t.Fatalf("persist accepted observation: %v", err)
		}
		if _, err := database.ExecContext(ctx, `
UPDATE release_preview_runs
SET state = 'reconcile_wait', version = 4, lease_worker_id = NULL, lease_epoch = NULL,
    lease_expires_at = NULL, updated_at = clock_timestamp()
WHERE id = $1
`, timeoutRunID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `
UPDATE release_preview_runs
SET state = 'reconciling', version = 5, fence_epoch = 2, lease_worker_id = $2,
    lease_epoch = 2, lease_expires_at = statement_timestamp() + interval '5 minutes',
    updated_at = clock_timestamp()
WHERE id = $1
`, timeoutRunID, worker); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `
INSERT INTO release_delivery_operation_attempts (
  operation_id, schema_version, kind, worker_id, fence_epoch
) VALUES ($1, 'release-delivery-operation-attempt/v1', 'reconcile', $2, 2)
`, timeoutOperationID, worker); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `
UPDATE release_delivery_operation_attempts
SET outcome = 'unknown', http_status = 504,
    error_code = 'OUTCOME_UNKNOWN', error_detail = 'EOF after accepted observation'
WHERE operation_id = $1 AND kind = 'reconcile' AND fence_epoch = 2
`, timeoutOperationID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `
UPDATE release_delivery_operations
SET next_attempt_at = clock_timestamp() + interval '1 minute',
    last_error_code = 'OUTCOME_UNKNOWN', last_error_detail = 'EOF after accepted observation',
    updated_at = clock_timestamp()
WHERE id = $1
`, timeoutOperationID); err != nil {
			t.Fatalf("retain accepted authority after reconcile timeout: %v", err)
		}
		var retainedState string
		var retainedSequence int64
		var laterAttempt bool
		if err := database.QueryRowContext(ctx, `
SELECT remote_state, last_observation_sequence, next_attempt_at > statement_timestamp()
FROM release_delivery_operations WHERE id = $1
`, timeoutOperationID).Scan(&retainedState, &retainedSequence, &laterAttempt); err != nil ||
			retainedState != "accepted" || retainedSequence != 1 || !laterAttempt {
			t.Fatalf("timeout authority state=%q sequence=%d later=%t err=%v", retainedState, retainedSequence, laterAttempt, err)
		}
		down, _ := files.ReadFile("000056_release_delivery_operation_reconciliation.down.sql")
		if _, err := database.ExecContext(ctx, string(down)); err == nil || !strings.Contains(err.Error(), "cannot downgrade") {
			t.Fatalf("rollback did not fail closed over v2 authority: %v", err)
		}
	})

	t.Run("empty authority downgrade is reversible", func(t *testing.T) {
		schema := releaseOperationTestSchema(t, ctx, base, dsn)
		database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		applyReleaseOperationMigrationsThrough(t, ctx, database, "000056_release_delivery_operation_reconciliation.up.sql")
		down, err := files.ReadFile("000056_release_delivery_operation_reconciliation.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, string(down)); err != nil {
			transaction.Rollback()
			t.Fatalf("downgrade empty 056 authority: %v", err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
		var table sql.NullString
		if err := database.QueryRowContext(ctx, `SELECT to_regclass('release_delivery_operations')::text`).Scan(&table); err != nil {
			t.Fatal(err)
		}
		if table.Valid {
			t.Fatalf("Operation authority survived clean downgrade as %q", table.String)
		}
	})
}

func applyReleaseOperationMigrationsThrough(t *testing.T, ctx context.Context, database *sql.DB, target string) {
	t.Helper()
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name > target {
			break
		}
		applyReleaseOperationMigration(t, ctx, database, name)
	}
}

func applyReleaseOperationMigration(t *testing.T, ctx context.Context, database *sql.DB, name string) {
	t.Helper()
	migration, err := files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, string(migration)); err != nil {
		transaction.Rollback()
		t.Fatalf("apply migration %s: %v", name, err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit migration %s: %v", name, err)
	}
}

func releaseOperationTestSchema(t *testing.T, ctx context.Context, base *sql.DB, dsn string) string {
	t.Helper()
	schema := "release_operation_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	return schema
}

func releaseOperationArtifacts(t *testing.T) []byte {
	t.Helper()
	kinds := []string{"web-static", "migration", "runtime-config-schema", "health-readiness-contract", "sbom", "vulnerability-report", "provenance", "signature"}
	values := make([]map[string]any, 0, len(kinds))
	for index, kind := range kinds {
		values = append(values, map[string]any{
			"id": "artifact-" + kind, "kind": kind, "store": "blob",
			"ref": "blob://release-operation/" + kind, "contentHash": releaseOperationDigest(kind),
			"mediaType": "application/octet-stream", "byteSize": 100 + index,
		})
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func releaseOperationDigest(value string) string {
	return releaseOperationBytesDigest([]byte(value))
}

func releaseOperationBytesDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
