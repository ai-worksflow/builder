package migrations

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/dataruntime"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type publicRuntimeAccessStub struct{}

func (publicRuntimeAccessStub) Authorize(context.Context, string, string, core.Action) (core.Role, error) {
	return core.RoleOwner, nil
}

type reviewGateContentStub struct {
	payload json.RawMessage
}

func (s reviewGateContentStub) PutPending(context.Context, string, string, string, int, json.RawMessage) (content.Reference, error) {
	return content.Reference{}, nil
}

func (s reviewGateContentStub) Finalize(context.Context, string) error { return nil }
func (s reviewGateContentStub) Abort(context.Context, string) error    { return nil }

func (s reviewGateContentStub) Get(_ context.Context, contentID, expectedHash string) (content.StoredContent, error) {
	return content.StoredContent{
		Reference: content.Reference{ID: contentID, ContentHash: expectedHash, SchemaVersion: 1},
		State:     content.StateFinalized, Payload: append(json.RawMessage(nil), s.payload...),
	}, nil
}

// This test executes the complete migration chain in an isolated PostgreSQL
// schema and rolls the transaction back. Set WORKSFLOW_TEST_POSTGRES_DSN to run
// it against CI or a local PostgreSQL instance without resetting a database.
func TestPublicDataRuntimeMigrationAppliesToPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()
	if err := sqlDB.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	transaction, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryLockID); err != nil {
		t.Fatal(err)
	}

	schema := "public_runtime_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := transaction.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL search_path = "`+schema+`", public`); err != nil {
		t.Fatal(err)
	}
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		contents, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, string(contents)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	var migrationLedgerAbsent bool
	if err := transaction.QueryRowContext(ctx, `SELECT to_regclass($1) IS NULL`,
		schema+".schema_migrations").Scan(&migrationLedgerAbsent); err != nil {
		t.Fatal(err)
	}
	if !migrationLedgerAbsent {
		t.Fatal("raw migration chain synthesized an external schema ledger")
	}
	// This test exercises public data-policy migrations with an already-existing
	// legacy production deployment. New legacy production writes and their
	// ReleaseBundle authority are covered independently by release canaries, so
	// isolate both admission triggers instead of synthesizing unrelated receipts
	// or weakening the production-only public-capability scenario.
	if _, err := transaction.ExecContext(ctx, `
ALTER TABLE deployment_versions
DISABLE TRIGGER deployment_version_release_authority_insert_guard;
ALTER TABLE deployment_versions
DISABLE TRIGGER deployment_version_controller_singleflight_guard
`); err != nil {
		t.Fatalf("isolate historical release authority from public runtime fixture: %v", err)
	}

	var policyTable, capabilityTable string
	if err := transaction.QueryRowContext(ctx, `
SELECT to_regclass($1)::text, to_regclass($2)::text
`, schema+`.data_public_table_policies`, schema+`.data_public_capabilities`).Scan(&policyTable, &capabilityTable); err != nil {
		t.Fatal(err)
	}
	if policyTable == "" || capabilityTable == "" {
		t.Fatalf("public runtime tables were not created: policies=%q capabilities=%q", policyTable, capabilityTable)
	}

	var activeIndex string
	if err := transaction.QueryRowContext(ctx, `
SELECT indexdef
FROM pg_indexes
WHERE schemaname = $1 AND indexname = 'data_public_capabilities_one_active_idx'
`, schema).Scan(&activeIndex); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(activeIndex), "where (status = 'active'::text)") {
		t.Fatalf("active capability uniqueness is not partial: %s", activeIndex)
	}

	gormInTransaction, err := gorm.Open(postgres.New(postgres.Config{Conn: transaction}), &gorm.Config{
		DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := dataruntime.NewPlatformPublicRuntime(dataruntime.PublicRuntimePlatformDependencies{
		Database: gormInTransaction, Access: publicRuntimeAccessStub{}, EncryptionKey: bytes.Repeat([]byte{0x33}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	userID, projectID := uuid.New(), uuid.New()
	reviewerID := uuid.New()
	artifactID, revisionID := uuid.New(), uuid.New()
	sourceArtifactID, sourceRevisionID := uuid.New(), uuid.New()
	dependencyID, blockingCommentID := uuid.New(), uuid.New()
	deploymentID, versionID := uuid.New(), uuid.New()
	tableID, columnID := uuid.New(), uuid.New()
	seeds := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, email, display_name, password_hash) VALUES ($1, 'public-runtime@example.com', 'Public Runtime', 'hash')`, []any{userID}},
		{`INSERT INTO users (id, email, display_name, password_hash) VALUES ($1, 'reviewer@example.com', 'Reviewer', 'hash')`, []any{reviewerID}},
		{`INSERT INTO projects (id, name, created_by) VALUES ($1, 'Public runtime integration', $2)`, []any{projectID, userID}},
		{`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner')`, []any{projectID, userID}},
		{`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'editor')`, []any{projectID, reviewerID}},
		{`INSERT INTO artifacts (id, project_id, kind, artifact_key, title, created_by) VALUES ($1, $2, 'workspace', 'workspace', 'Workspace', $3)`, []any{artifactID, projectID, userID}},
		{`INSERT INTO artifact_revisions (
  id, artifact_id, revision_number, schema_version, content_ref, content_hash,
  workflow_status, change_source, created_by
) VALUES ($1, $2, 1, 1, 'mongo:test', 'sha256:test', 'approved', 'system', $3)`, []any{revisionID, artifactID, userID}},
		{`UPDATE artifact_revisions SET approved_at = now() WHERE id = $1`, []any{revisionID}},
		{`UPDATE artifacts SET latest_revision_id = $1, latest_approved_revision_id = $1 WHERE id = $2`, []any{revisionID, artifactID}},
		{`INSERT INTO artifact_health (artifact_id, sync_status, delivery_status) VALUES ($1, 'current', 'complete')`, []any{artifactID}},
		{`INSERT INTO artifacts (id, project_id, kind, artifact_key, title, created_by) VALUES ($1, $2, 'project_brief', 'source-brief', 'Source brief', $3)`, []any{sourceArtifactID, projectID, reviewerID}},
		{`INSERT INTO artifact_revisions (
  id, artifact_id, revision_number, schema_version, content_ref, content_hash,
  workflow_status, change_source, created_by
) VALUES ($1, $2, 1, 1, 'mongo:source', 'sha256:source', 'approved', 'system', $3)`, []any{sourceRevisionID, sourceArtifactID, reviewerID}},
		{`INSERT INTO artifact_dependencies (
  id, project_id, source_artifact_id, source_revision_id, source_content_hash,
  target_artifact_id, target_revision_id, relation, required, created_by
) VALUES ($1, $2, $3, $4, 'sha256:source', $5, $6, 'derives_from', true, $7)`, []any{dependencyID, projectID, sourceArtifactID, sourceRevisionID, artifactID, revisionID, reviewerID}},
		{`INSERT INTO comment_threads (
  id, project_id, artifact_id, revision_id, anchor, severity, created_by
) VALUES ($1, $2, $3, $4, '{}'::jsonb, 'blocking', $5)`, []any{blockingCommentID, projectID, artifactID, revisionID, reviewerID}},
		{`INSERT INTO deployments (
  id, project_id, environment, environment_ref, provider, status, created_by
) VALUES ($1, $2, 'production', 'production', 'local-static', 'deploying', $3)`, []any{deploymentID, projectID, userID}},
		{`INSERT INTO deployment_versions (
  id, deployment_id, number, action, workspace_artifact_id, workspace_revision_id,
  workspace_content_hash, provider_ref, entry_path, checksum, file_count, total_bytes,
  environment_ref, status, created_by
) VALUES ($1, $2, 1, 'publish', $3, $4, 'sha256:test', 'provider:test', 'index.html',
  'sha256:build', 1, 32, 'production', 'ready', $5)`, []any{versionID, deploymentID, artifactID, revisionID, userID}},
		{`UPDATE deployments SET status = 'ready', active_version_id = $1 WHERE id = $2`, []any{versionID, deploymentID}},
		{`INSERT INTO data_tables (id, project_id, name) VALUES ($1, $2, 'messages')`, []any{tableID, projectID}},
		{`INSERT INTO data_columns (id, table_id, name, data_type, required, position) VALUES ($1, $2, 'message', 'text', true, 0)`, []any{columnID, tableID}},
	}
	for _, seed := range seeds {
		if _, err := transaction.ExecContext(ctx, seed.query, seed.args...); err != nil {
			t.Fatal(err)
		}
	}

	policyInput := dataruntime.PublicTablePolicyInput{
		AllowRead: true, AllowCreate: true, ReadableFields: []string{"message"}, WritableFields: []string{"message"},
	}
	policy, err := service.PutPolicy(ctx, projectID.String(), tableID.String(), userID.String(), 0, policyInput)
	if err != nil || !policy.AllowRead || policy.Version != 1 || policy.ETag != dataruntime.PublicTablePolicyETag(projectID.String(), tableID.String(), 1) {
		t.Fatalf("persist public policy: policy=%+v err=%v", policy, err)
	}
	if _, err := service.PutPolicy(ctx, projectID.String(), tableID.String(), userID.String(), 0, policyInput); err == nil {
		t.Fatal("stale public policy version overwrote the current policy")
	} else if typed, ok := dataruntime.AsRuntimeError(err); !ok || typed.Code != dataruntime.CodePreconditionFailed {
		t.Fatalf("stale public policy returned the wrong error: %v", err)
	}
	dataService, err := dataruntime.NewPlatformService(dataruntime.PlatformDependencies{
		Database: gormInTransaction, Access: publicRuntimeAccessStub{}, EncryptionKey: bytes.Repeat([]byte{0x33}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := dataService.PreviewMigration(ctx, projectID.String(), userID.String(), []dataruntime.MigrationOperation{{
		Type: dataruntime.MigrationRenameColumn, TableID: tableID.String(), ColumnID: columnID.String(), Name: "body",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dataService.ApplyMigration(ctx, projectID.String(), userID.String(), preview.ConfirmationToken); err != nil {
		t.Fatal(err)
	}
	policies, err := service.ListPolicies(ctx, projectID.String(), userID.String())
	if err != nil || len(policies) != 1 || len(policies[0].ReadableFields) != 1 || policies[0].ReadableFields[0] != "body" || policies[0].WritableFields[0] != "body" {
		t.Fatalf("schema migration did not reconcile public field allowlists: policies=%+v err=%v", policies, err)
	}
	prepared, err := service.PrepareDeploymentCapability(ctx, dataruntime.PreparePublicCapabilityInput{
		ProjectID: projectID.String(), DeploymentID: deploymentID.String(), DeploymentVersionID: versionID.String(),
		Environment: dataruntime.ScopeProduction, AllowedOrigins: []string{"https://app.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, deploymentID.String(), prepared.CapabilityToken); err == nil {
		t.Fatal("pending capability authorized public access before delivery activation")
	}
	if _, err := service.ActivateDeploymentCapability(ctx, projectID.String(), deploymentID.String(), prepared.CapabilityID); err != nil {
		t.Fatal(err)
	}
	capability, err := service.Authenticate(ctx, deploymentID.String(), prepared.CapabilityToken)
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.CreatePublicRecord(ctx, capability, tableID.String(), "integration-request", dataruntime.RecordInput{
		Values: map[string]json.RawMessage{"body": json.RawMessage(`"hello"`)},
	})
	if err != nil || string(record.Values["body"]) != `"hello"` {
		t.Fatalf("public record create failed: record=%+v err=%v", record, err)
	}
	page, err := service.ListPublicRecords(ctx, capability, tableID.String(), 10, 0)
	if err != nil || page.Total != 1 || len(page.Records) != 1 {
		t.Fatalf("public record read failed: page=%+v err=%v", page, err)
	}
	var persistedDigest []byte
	if err := transaction.QueryRowContext(ctx, `SELECT token_digest FROM data_public_capabilities WHERE id = $1`, prepared.CapabilityID).Scan(&persistedDigest); err != nil {
		t.Fatal(err)
	}
	if len(persistedDigest) != 32 || bytes.Contains(persistedDigest, []byte(prepared.CapabilityToken)) {
		t.Fatalf("capability plaintext leaked into persistence: %x", persistedDigest)
	}
	nextPrepared, err := service.PrepareDeploymentCapability(ctx, dataruntime.PreparePublicCapabilityInput{
		ProjectID: projectID.String(), DeploymentID: deploymentID.String(), DeploymentVersionID: versionID.String(),
		Environment: dataruntime.ScopeProduction, AllowedOrigins: []string{"https://app.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, deploymentID.String(), prepared.CapabilityToken); err != nil {
		t.Fatalf("preparing a replacement invalidated the live application early: %v", err)
	}
	if _, err := service.ActivateDeploymentCapability(ctx, projectID.String(), deploymentID.String(), nextPrepared.CapabilityID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, deploymentID.String(), prepared.CapabilityToken); err == nil {
		t.Fatal("superseded capability continued to authorize public access")
	}
	if _, err := service.Authenticate(ctx, deploymentID.String(), nextPrepared.CapabilityToken); err != nil {
		t.Fatalf("activated replacement capability was rejected: %v", err)
	}
	if err := service.RevokeDeploymentCapability(ctx, projectID.String(), deploymentID.String(), nextPrepared.CapabilityID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, deploymentID.String(), nextPrepared.CapabilityToken); err == nil {
		t.Fatal("revoked capability continued to authorize public access")
	}

	var publicAuditCount int64
	if err := transaction.QueryRowContext(ctx, `
SELECT count(*) FROM audit_events
WHERE project_id = $1 AND actor_id IS NULL AND target_type IN ('data.public-capability', 'data.record')
`, projectID).Scan(&publicAuditCount); err != nil {
		t.Fatal(err)
	}
	if publicAuditCount < 4 {
		t.Fatalf("public runtime mutations were not audited with deployment identity: count=%d", publicAuditCount)
	}
	var auditedCapabilityID string
	if err := transaction.QueryRowContext(ctx, `
SELECT metadata->>'publicCapabilityId'
FROM audit_events
WHERE project_id = $1 AND action = 'data.record.create'
ORDER BY created_at DESC
LIMIT 1
`, projectID).Scan(&auditedCapabilityID); err != nil {
		t.Fatal(err)
	}
	if auditedCapabilityID != prepared.CapabilityID {
		t.Fatalf("public record audit is not attributable to its capability: %q", auditedCapabilityID)
	}

	access, err := core.NewAccessControl(gormInTransaction)
	if err != nil {
		t.Fatal(err)
	}
	artifactService, err := core.NewArtifactService(gormInTransaction, reviewGateContentStub{payload: json.RawMessage(`{}`)}, access)
	if err != nil {
		t.Fatal(err)
	}
	blockedGate, err := artifactService.ReviewGate(ctx, artifactID.String(), userID.String())
	if err != nil {
		t.Fatal(err)
	}
	if blockedGate.Passed || blockedGate.TraceCoverage != 0 || len(blockedGate.UnresolvedBlockingCommentIDs) != 1 || blockedGate.UnresolvedBlockingCommentIDs[0] != blockingCommentID.String() {
		t.Fatalf("live blockers were not reflected by artifact review gate: %+v", blockedGate)
	}
	if _, err := artifactService.ReviewGate(ctx, artifactID.String(), uuid.NewString()); err == nil {
		t.Fatal("artifact review gate bypassed project ActionView authorization")
	}

	traceID, reviewID, decisionID := uuid.New(), uuid.New(), uuid.New()
	policyJSON, _ := json.Marshal(core.ReviewPolicy{
		ReviewerIDs: []string{reviewerID.String()}, MinimumApprovals: 1, ProhibitSelfReview: true,
	})
	gateSeeds := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO trace_links (
  id, project_id, source_artifact_id, source_revision_id, target_artifact_id,
  target_revision_id, relation, metadata, created_by
) VALUES ($1, $2, $3, $4, $5, $6, 'derives_from', '{}'::jsonb, $7)`, []any{traceID, projectID, sourceArtifactID, sourceRevisionID, artifactID, revisionID, reviewerID}},
		{`INSERT INTO review_requests (
  id, project_id, artifact_id, revision_id, content_hash, status, policy,
  requested_by, closed_at
) VALUES ($1, $2, $3, $4, 'sha256:test', 'approved', $5, $6, now())`, []any{reviewID, projectID, artifactID, revisionID, policyJSON, userID}},
		{`INSERT INTO review_decisions (
  id, review_request_id, reviewer_id, decision, summary
) VALUES ($1, $2, $3, 'approve', 'Approved')`, []any{decisionID, reviewID, reviewerID}},
		{`UPDATE comment_threads SET resolved_by = $1, resolved_at = now() WHERE id = $2`, []any{reviewerID, blockingCommentID}},
	}
	for _, seed := range gateSeeds {
		if _, err := transaction.ExecContext(ctx, seed.query, seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	passedGate, err := artifactService.ReviewGate(ctx, artifactID.String(), userID.String())
	if err != nil {
		t.Fatal(err)
	}
	if !passedGate.Passed || passedGate.TraceCoverage != 1 || len(passedGate.UnresolvedBlockingCommentIDs) != 0 {
		t.Fatalf("complete artifact review evidence did not pass: %+v", passedGate)
	}
}
