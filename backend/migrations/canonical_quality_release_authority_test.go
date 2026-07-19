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
	verificationstore "github.com/worksflow/builder/backend/internal/verification"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCanonicalQualityReleaseAuthorityMigrationDeclaresExactImmutableBoundary(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000043_canonical_quality_release_authority.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000043_canonical_quality_release_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TABLE canonical_verification_plans",
		"CREATE TABLE canonical_verification_runs",
		"CREATE TABLE canonical_verification_receipts",
		"CREATE TABLE release_bundles",
		"release_bundle_artifact_set_is_complete",
		"verification_normalize_sha256(proposal.application_build_contract_hash)",
		"Canonical VerificationPlan requires one exact approved WorkspaceRevision",
		"Canonical VerificationPlan requires release artifact, SBOM, vulnerability, and container policy checks",
		"Canonical VerificationReceipt requires its exact terminal Run",
		"ReleaseBundle requires one exact passed Canonical Receipt",
		"Canonical quality and ReleaseBundle facts are immutable",
		"deployment_versions_canonical_receipt_exact_fk",
		"deployment_versions_release_bundle_exact_fk",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Canonical quality migration is missing %q", expected)
		}
	}
	for _, forbidden := range []string{
		"REFERENCES candidate_verification_receipts(id, payload_hash)",
		"REFERENCES quality_runs",
		"content_hash = payload_hash",
		"content_hash = bundle_hash",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Canonical publish authority unexpectedly trusts %q", forbidden)
		}
	}
	for _, expected := range []string{
		"DROP TABLE IF EXISTS release_bundles",
		"DROP FUNCTION IF EXISTS release_bundle_artifact_set_is_complete",
		"DROP TABLE IF EXISTS canonical_verification_receipts",
		"DROP TABLE IF EXISTS canonical_verification_runs",
		"DROP TABLE IF EXISTS canonical_verification_plans",
	} {
		if !strings.Contains(string(down), expected) {
			t.Fatalf("Canonical quality rollback is missing %q", expected)
		}
	}
}

func TestCanonicalVerificationExecutionMigrationDeclaresFencedCompleteReceipts(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000044_canonical_verification_execution.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	down, err := files.ReadFile("000044_canonical_verification_execution.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"CREATE TABLE canonical_verification_attempts",
		"CREATE TABLE canonical_verification_checks",
		"CREATE TABLE canonical_verification_obligation_coverage",
		"Canonical VerificationRun claim must establish a live worker fence",
		"Canonical Receipt Attempt/check/coverage projection is incomplete",
		"passed Canonical Receipt requires every planned check, Must coverage, and release security check",
		"DEFERRABLE INITIALLY DEFERRED",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Canonical execution migration is missing %q", expected)
		}
	}
	if !strings.Contains(string(down), "run.state = NEW.decision") ||
		strings.Contains(string(down), "run.state IN ('collecting', NEW.decision)") {
		t.Fatal("Canonical execution rollback does not restore the terminal-only Receipt boundary")
	}
}

func TestCanonicalQualityReleaseAuthorityMigrationPostgresCanary(t *testing.T) {
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
	schema := "canonical_quality_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name > "000043_canonical_quality_release_authority.up.sql" {
			break
		}
		migration, readErr := files.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, execErr := database.ExecContext(ctx, string(migration)); execErr != nil {
			t.Fatalf("apply prerequisite migration %s: %v", name, execErr)
		}
	}

	seed := seedRepositoryCandidateCanary(t, ctx, database)
	profileID := "canonical-release-v1"
	profileHash := applicationBuildContractCanaryDigest("canonical-release-profile")
	profileDocument := applicationBuildContractCanaryJSON(t, map[string]any{
		"schemaVersion": "verification-profile/v1",
		"id":            profileID, "version": 1, "profileHash": profileHash,
		"supportedTemplateRoles": []string{"web", "api"},
		"verifierImages": []map[string]any{{
			"role": "node", "image": "registry.example/quality-node@" + applicationBuildContractCanaryDigest("canonical-node-image"),
		}},
		"builtInChecks": []any{}, "limits": map[string]any{}, "networkPolicy": map[string]any{},
		"state": "active",
	})
	if _, err := database.ExecContext(ctx, `
INSERT INTO verification_profile_versions (
  profile_id, version, schema_version, document, content_hash, created_by
) VALUES ($1, 1, 'verification-profile/v1', $2, $3, $4)
`, profileID, profileDocument, profileHash, seed.actorID); err != nil {
		t.Fatalf("insert canonical VerificationProfile: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO verification_profile_policies (
  profile_id, profile_version, profile_hash, state, policy_version, reason, updated_by
) VALUES ($1, 1, $2, 'active', 1, 'canonical release policy', $3)
`, profileID, profileHash, seed.actorID); err != nil {
		t.Fatalf("activate canonical VerificationProfile: %v", err)
	}

	proposalID := uuid.New()
	proposalHash := applicationBuildContractCanaryDigest("canonical-applied-proposal")
	if _, err := database.ExecContext(ctx, `
INSERT INTO implementation_proposals (
  id, project_id, build_manifest_id, base_workspace_revision_id,
  status, version, content_store, content_ref, content_hash, payload_hash,
  operation_count, accepted_count, rejected_count, created_by, created_at,
  applied_by, applied_at, execution_source,
  application_build_contract_id, application_build_contract_hash
) VALUES (
  $1, $2, $3, $4,
  'applied', 1, 'blob', $5, $6, $6,
  1, 1, 0, $7, $8, $7, $8, 'manual_submission', $9, $10
)
`, proposalID, seed.projectID, seed.manifestID, seed.workspaceRevisionID,
		"blob://canonical-proposals/"+proposalID.String(), proposalHash,
		seed.actorID, seed.createdAt, seed.contractID, seed.contractHash); err != nil {
		t.Fatalf("insert exact applied Proposal: %v", err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE artifact_revisions DISABLE TRIGGER artifact_revisions_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE artifact_revisions SET implementation_proposal_id = $2 WHERE id = $1`, seed.workspaceRevisionID, proposalID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE artifact_revisions ENABLE TRIGGER artifact_revisions_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE application_build_manifests SET status = 'consumed' WHERE id = $1`, seed.manifestID); err != nil {
		t.Fatalf("consume exact BuildManifest: %v", err)
	}

	var templateReleasesJSON, obligationsJSON []byte
	if err := database.QueryRowContext(ctx, `
SELECT jsonb_agg(jsonb_build_object(
         'role', role, 'id', template_release_id::text,
         'contentHash', verification_normalize_sha256(template_release_content_hash)
       ) ORDER BY ordinal)
FROM application_build_contract_template_releases WHERE contract_id = $1
`, seed.contractID).Scan(&templateReleasesJSON); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT jsonb_agg(jsonb_build_object(
         'id', obligation_id, 'level', level, 'status', status, 'oracleIds', oracle_ids
       ) ORDER BY obligation_id)
FROM application_build_contract_obligations WHERE contract_id = $1
`, seed.contractID).Scan(&obligationsJSON); err != nil {
		t.Fatal(err)
	}

	planID := uuid.New()
	planHash := applicationBuildContractCanaryDigest("canonical-plan")
	runtimePolicyHash := applicationBuildContractCanaryDigest("canonical-runtime-policy")
	manifestHash := applicationBuildContractCanaryDigest("repository-manifest")
	contractHash := applicationBuildContractCanaryDigest("contract-repository-candidate")
	insertPlan := func(id uuid.UUID, checks string, checkCount int) error {
		_, insertErr := database.ExecContext(ctx, `
INSERT INTO canonical_verification_plans (
  id, schema_version, scope, project_id,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  verification_profile_id, verification_profile_version, verification_profile_hash,
  template_releases, obligations, check_ids, required_check_ids,
  check_count, obligation_count, runtime_policy_hash,
  content_store, content_ref, content_hash, plan_hash, created_by
) VALUES (
  $1, 'canonical-verification-plan/v1', 'canonical', $2,
  $3, $4, $5, $6, $7, $8, $9, $10, $11,
  $12, 1, $13, $14::jsonb, $15::jsonb, $16::jsonb, $16::jsonb,
  $17, 1, $18, 'blob', $19, $20, $20, $21
)
`, id, seed.projectID, seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceHash,
			seed.manifestID, manifestHash, seed.contractID, contractHash, seed.fullStackID, seed.fullStackHash,
			profileID, profileHash, templateReleasesJSON, obligationsJSON, checks, checkCount,
			runtimePolicyHash, "blob://canonical-plans/"+id.String(), planHash, seed.actorID)
		return insertErr
	}
	if err := insertPlan(uuid.New(), `["oracle-repository"]`, 1); err == nil || !strings.Contains(err.Error(), "requires release artifact") {
		t.Fatalf("Canonical Plan without supply-chain checks was accepted: %v", err)
	}
	checks := `["oracle-repository","release-artifacts","release-container-policy","release-sbom","release-vulnerability"]`
	if err := insertPlan(planID, checks, 5); err != nil {
		t.Fatalf("insert exact Canonical VerificationPlan: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE canonical_verification_plans SET plan_hash = $2 WHERE id = $1`, planID, applicationBuildContractCanaryDigest("tampered-plan")); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("Canonical Plan mutation was not rejected: %v", err)
	}

	runID, attemptID := uuid.New(), uuid.New()
	finishedAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := database.ExecContext(ctx, `
INSERT INTO canonical_verification_runs (
  id, schema_version, project_id, plan_id, plan_hash,
  request_key, request_hash, reason, state, version, fence_epoch,
  started_at, finished_at, created_by, updated_by
) VALUES (
  $1, 'canonical-verification-run/v1', $2, $3, $4,
  $5, $6, 'canonical quality canary', 'passed', 7, 1,
  $7, $7, $8, $8
)
`, runID, seed.projectID, planID, planHash, "canonical-"+runID.String(),
		applicationBuildContractCanaryDigest("canonical-request"), finishedAt, seed.actorID); err != nil {
		t.Fatalf("insert terminal Canonical VerificationRun: %v", err)
	}

	staticBuildHash := applicationBuildContractCanaryDigest("canonical-web-static")
	releaseArtifactValues := []map[string]any{
		{
			"id": "application-image", "kind": "oci-image", "store": "oci",
			"ref":         "registry.example/application@" + applicationBuildContractCanaryDigest("canonical-image"),
			"contentHash": applicationBuildContractCanaryDigest("canonical-image"),
			"mediaType":   "application/vnd.oci.image.manifest.v1+json", "byteSize": 4096,
		},
		{
			"id": "web-static", "kind": "web-static", "store": "content",
			"ref": "content://canonical-web-static", "contentHash": staticBuildHash,
			"mediaType": "application/vnd.worksflow.static-tree", "byteSize": 1024,
		},
		canonicalReleaseMetadataArtifact("health-contract", "health-readiness-contract", "application/schema+json"),
		canonicalReleaseMetadataArtifact("migration", "migration", "application/vnd.worksflow.migration"),
		canonicalReleaseMetadataArtifact("provenance", "provenance", "application/vnd.in-toto+json"),
		canonicalReleaseMetadataArtifact("runtime-config", "runtime-config-schema", "application/schema+json"),
		canonicalReleaseMetadataArtifact("sbom", "sbom", "application/spdx+json"),
		canonicalReleaseMetadataArtifact("signature", "signature", "application/vnd.dev.cosign.simplesigning.v1+json"),
		canonicalReleaseMetadataArtifact("vulnerability", "vulnerability-report", "application/vnd.worksflow.vulnerability+json"),
	}
	releaseArtifacts, err := json.Marshal(releaseArtifactValues)
	if err != nil {
		t.Fatal(err)
	}
	receiptID := uuid.New()
	receiptHash := applicationBuildContractCanaryDigest("canonical-receipt")
	receiptContentHash := applicationBuildContractCanaryDigest("canonical-receipt-content")
	if _, err := database.ExecContext(ctx, `
INSERT INTO canonical_verification_receipts (
  id, schema_version, scope, run_id, project_id, plan_id, plan_hash,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  verification_profile_id, verification_profile_version, verification_profile_hash,
  attempt_ids, release_artifacts, check_count, coverage_count,
  must_count, must_passed_count, blocker_count, warning_count, decision,
  content_store, content_ref, content_hash, payload_hash, created_by
) VALUES (
  $1, 'canonical-verification-receipt/v1', 'canonical', $2, $3, $4, $5,
  $6, $7, $8, $9, $10, $11, $12, $13, $14,
	  $15, 1, $16, $17::jsonb, $18::jsonb, 5, 1, 1, 1, 0, 0, 'passed',
	  'blob', $19, $20, $21, $22
)
`, receiptID, runID, seed.projectID, planID, planHash,
		seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceHash,
		seed.manifestID, manifestHash, seed.contractID, contractHash, seed.fullStackID, seed.fullStackHash,
		profileID, profileHash, `["`+attemptID.String()+`"]`, releaseArtifacts,
		"blob://canonical-receipts/"+receiptID.String(), receiptContentHash, receiptHash, seed.actorID); err != nil {
		t.Fatalf("insert exact passed Canonical VerificationReceipt: %v", err)
	}

	bundleID := uuid.New()
	bundleHash := applicationBuildContractCanaryDigest("release-bundle")
	bundleContentHash := applicationBuildContractCanaryDigest("release-bundle-content")
	incompleteArtifacts, marshalErr := json.Marshal(releaseArtifactValues[:2])
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO release_bundles (
  id, schema_version, project_id, workspace_artifact_id, workspace_revision_id,
  workspace_content_hash, canonical_receipt_id, canonical_receipt_hash,
  release_artifacts, content_store, content_ref, content_hash, bundle_hash, created_by
) VALUES ($1, 'release-bundle/v1', $2, $3, $4, $5, $6, $7, $8::jsonb, 'blob', $9, $10, $11, $12)
`, uuid.New(), seed.projectID, seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceHash,
		receiptID, receiptHash, incompleteArtifacts, "blob://release-bundles/incomplete", bundleContentHash,
		applicationBuildContractCanaryDigest("incomplete-release-bundle"), seed.actorID); err == nil {
		t.Fatalf("incomplete ReleaseBundle artifact set was accepted: %v", err)
	}
	wrongArtifacts := strings.Replace(string(releaseArtifacts), "4096", "4097", 1)
	if _, err := database.ExecContext(ctx, `
INSERT INTO release_bundles (
  id, schema_version, project_id, workspace_artifact_id, workspace_revision_id,
  workspace_content_hash, canonical_receipt_id, canonical_receipt_hash,
  release_artifacts, content_store, content_ref, content_hash, bundle_hash, created_by
) VALUES ($1, 'release-bundle/v1', $2, $3, $4, $5, $6, $7, $8::jsonb, 'blob', $9, $10, $11, $12)
`, uuid.New(), seed.projectID, seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceHash,
		receiptID, receiptHash, wrongArtifacts, "blob://release-bundles/wrong", bundleContentHash, bundleHash, seed.actorID); err == nil || !strings.Contains(err.Error(), "identical release artifacts") {
		t.Fatalf("ReleaseBundle with non-identical artifacts was accepted: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO release_bundles (
  id, schema_version, project_id, workspace_artifact_id, workspace_revision_id,
  workspace_content_hash, canonical_receipt_id, canonical_receipt_hash,
  release_artifacts, content_store, content_ref, content_hash, bundle_hash, created_by
) VALUES ($1, 'release-bundle/v1', $2, $3, $4, $5, $6, $7, $8::jsonb, 'blob', $9, $10, $11, $12)
`, bundleID, seed.projectID, seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceHash,
		receiptID, receiptHash, releaseArtifacts, "blob://release-bundles/"+bundleID.String(), bundleContentHash, bundleHash, seed.actorID); err != nil {
		t.Fatalf("insert exact ReleaseBundle: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE canonical_verification_receipts SET warning_count = 1 WHERE id = $1`, receiptID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("Canonical Receipt mutation was not rejected: %v", err)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM release_bundles WHERE id = $1`, bundleID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("ReleaseBundle deletion was not rejected: %v", err)
	}

	var referencedTable string
	if err := database.QueryRowContext(ctx, `
SELECT ccu.table_name
FROM information_schema.table_constraints AS tc
JOIN information_schema.constraint_column_usage AS ccu
  ON ccu.constraint_schema = tc.constraint_schema AND ccu.constraint_name = tc.constraint_name
WHERE tc.table_schema = current_schema()
  AND tc.table_name = 'release_bundles'
  AND tc.constraint_name = 'release_bundle_canonical_receipt_exact_fk'
`).Scan(&referencedTable); err != nil {
		t.Fatal(err)
	}
	if referencedTable != "canonical_verification_receipts" {
		t.Fatalf("ReleaseBundle trusts %q instead of canonical_verification_receipts", referencedTable)
	}
	executionMigration, err := files.ReadFile("000044_canonical_verification_execution.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(executionMigration)); err != nil {
		t.Fatalf("apply Canonical execution migration: %v", err)
	}
	publishGateMigration, err := files.ReadFile("000045_release_bundle_publish_gate.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(publishGateMigration)); err != nil {
		t.Fatalf("apply ReleaseBundle publish gate migration: %v", err)
	}
	assertReleaseBundlePublishGate(
		t, ctx, database, seed, receiptID, receiptHash, bundleID, bundleHash, staticBuildHash,
	)
	deliveryMigration, err := files.ReadFile("000046_release_delivery_control_plane.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(deliveryMigration)); err != nil {
		t.Fatalf("apply release delivery control-plane migration: %v", err)
	}
	deliveryHead := assertReleaseDeliveryControlPlane(
		t, ctx, database, seed, bundleID, bundleHash, receiptID, receiptHash, releaseArtifacts,
	)
	headFencingMigration, err := files.ReadFile("000048_release_production_head_fencing.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(headFencingMigration)); err != nil {
		t.Fatalf("apply release production head fencing migration: %v", err)
	}
	assertReleaseProductionHeadFencing(
		t, ctx, database, seed, bundleID, bundleHash, deliveryHead,
	)
	for _, migrationName := range []string{
		"000047_sandbox_lifecycle_deadlines.up.sql",
		"000049_candidate_sandbox_lifecycle_write_gate.up.sql",
		"000050_candidate_abandon_sandbox_reconciliation.up.sql",
		"000051_verification_output_truncation_gate.up.sql",
		"000052_verification_execution_cleanup_obligations.up.sql",
	} {
		migration, readErr := files.ReadFile(migrationName)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, execErr := database.ExecContext(ctx, string(migration)); execErr != nil {
			t.Fatalf("apply %s: %v", migrationName, execErr)
		}
	}
	assertCanonicalExecutionGuards(
		t, ctx, database, seed, planID, planHash, manifestHash, contractHash,
		profileID, profileHash, releaseArtifacts,
	)
	assertCanonicalCleanupPolicyReconciliation(
		t, ctx, database, seed, planID, planHash, profileID,
	)
}

func assertCanonicalCleanupPolicyReconciliation(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	seed repositoryCandidateCanarySeed,
	planID uuid.UUID,
	planHash, profileID string,
) {
	t.Helper()
	gormDatabase, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: database}), &gorm.Config{})
	if err != nil {
		t.Fatalf("open Canonical cleanup GORM store: %v", err)
	}
	store, err := verificationstore.NewPostgresStore(gormDatabase, newVerificationCanaryContentStore())
	if err != nil {
		t.Fatal(err)
	}
	runID, attemptID := uuid.NewString(), uuid.NewString()
	run, err := store.CreateCanonicalRun(ctx, verificationstore.CreateCanonicalRunInput{
		ID: runID, ProjectID: seed.projectID.String(),
		Plan:       verificationstore.PlanReference{ID: planID.String(), ContentHash: planHash},
		RequestKey: "canonical-cleanup-" + runID, Reason: "exercise revoked-policy cleanup",
		CreatedBy: seed.actorID.String(),
	})
	if err != nil {
		t.Fatalf("create Canonical cleanup Run: %v", err)
	}
	legacyAttemptID := uuid.New()
	legacyClaim, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyClaim.ExecContext(ctx, `
UPDATE canonical_verification_runs
SET state = 'claimed', version = version + 1, fence_epoch = fence_epoch + 1,
    lease_worker_id = 'legacy-canonical-worker', lease_epoch = fence_epoch + 1,
    lease_expires_at = statement_timestamp() + interval '5 minutes',
    started_at = statement_timestamp(), updated_by = $2
WHERE id = $1 AND state = 'queued'
`, runID, seed.actorID); err != nil {
		t.Fatalf("legacy Canonical Run claim: %v", err)
	}
	if _, err := legacyClaim.ExecContext(ctx, `
INSERT INTO canonical_verification_attempts (
  id, schema_version, run_id, project_id, plan_id, plan_hash,
  ordinal, state, version, fence_epoch, created_by, updated_by
) VALUES ($1, 'canonical-verification-attempt/v1', $2, $3, $4, $5,
          1, 'queued', 1, 0, $6, $6)
`, legacyAttemptID, runID, seed.projectID, planID, planHash, seed.actorID); err != nil {
		t.Fatalf("legacy Canonical Attempt insert: %v", err)
	}
	if _, err := legacyClaim.ExecContext(ctx, `
UPDATE canonical_verification_attempts AS attempt
SET state = 'claimed', version = attempt.version + 1, fence_epoch = run.fence_epoch,
    lease_worker_id = run.lease_worker_id, lease_epoch = run.lease_epoch,
    lease_expires_at = run.lease_expires_at, started_at = statement_timestamp(), updated_by = $3
FROM canonical_verification_runs AS run
WHERE attempt.id = $1 AND run.id = $2
`, legacyAttemptID, runID, seed.actorID); err != nil {
		t.Fatalf("legacy Canonical Attempt claim: %v", err)
	}
	if err := legacyClaim.Commit(); err == nil || !strings.Contains(err.Error(), "exact-fence cleanup registration") {
		t.Fatalf("legacy Canonical claim committed without exact cleanup registration: %v", err)
	}

	lease, found, err := store.ClaimCanonicalExecution(ctx, verificationstore.ClaimCanonicalExecutionInput{
		AttemptID: attemptID, ActorID: seed.actorID.String(),
		WorkerID: "canonical-policy-worker", LeaseDuration: time.Second,
	})
	if err != nil || !found || lease.RunID != run.ID || lease.AttemptID != attemptID {
		t.Fatalf("claim Canonical policy Run = %#v found=%t err=%v", lease, found, err)
	}
	var claimCleanupState string
	if err := database.QueryRowContext(ctx, `
SELECT state FROM verification_execution_cleanups
WHERE scope = 'canonical' AND project_id = $1 AND run_id = $2
  AND attempt_id = $3 AND attempt_fence_epoch = $4
`, seed.projectID, lease.RunID, lease.AttemptID,
		lease.AttemptFenceEpoch).Scan(&claimCleanupState); err != nil || claimCleanupState != "registered" {
		t.Fatalf("Canonical claim cleanup registration = %q, err=%v", claimCleanupState, err)
	}

	queuedRunID := uuid.NewString()
	queuedRun, err := store.CreateCanonicalRun(ctx, verificationstore.CreateCanonicalRunInput{
		ID: queuedRunID, ProjectID: seed.projectID.String(),
		Plan:       verificationstore.PlanReference{ID: planID.String(), ContentHash: planHash},
		RequestKey: "canonical-inactive-queued-" + queuedRunID,
		Reason:     "exercise inactive policy before claim",
		CreatedBy:  seed.actorID.String(),
	})
	if err != nil {
		t.Fatalf("create queued Canonical policy Run: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE verification_profile_policies
SET state = 'deprecated', policy_version = policy_version + 1,
    reason = 'cleanup must survive policy revocation', updated_by = $2
WHERE profile_id = $1 AND state = 'active'
`, profileID, seed.actorID); err != nil {
		t.Fatalf("deprecate Canonical cleanup profile: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO verification_execution_cleanups (
  scope, project_id, run_id, attempt_id, attempt_fence_epoch,
  state, version, lease_epoch, created_by, updated_by
) VALUES ('canonical', $1, $2, $3, 1, 'registered', 1, 0, $4, $4)
`, seed.projectID, queuedRun.ID, uuid.New(), seed.actorID); err == nil ||
		!strings.Contains(err.Error(), "exact persisted Attempt fence") {
		t.Fatalf("queued Canonical reconciliation accepted fabricated cleanup: %v", err)
	}
	queuedReconciled, err := store.ReconcileInactiveVerificationExecution(
		ctx, verificationstore.ScopeCanonical, seed.actorID.String(),
	)
	if err != nil || !queuedReconciled {
		t.Fatalf("reconcile queued inactive Canonical Run: reconciled=%t err=%v", queuedReconciled, err)
	}
	var queuedState, queuedReason string
	var queuedAttemptCount, queuedCleanupCount int
	if err := database.QueryRowContext(ctx, `
SELECT run.state, run.terminal_reason,
       (SELECT count(*) FROM canonical_verification_attempts AS attempt
        WHERE attempt.run_id = run.id),
       (SELECT count(*) FROM verification_execution_cleanups AS cleanup
        WHERE cleanup.scope = 'canonical' AND cleanup.run_id = run.id)
FROM canonical_verification_runs AS run
WHERE run.id = $1
`, queuedRun.ID).Scan(
		&queuedState, &queuedReason, &queuedAttemptCount, &queuedCleanupCount,
	); err != nil {
		t.Fatal(err)
	}
	if queuedState != "cancelled" ||
		queuedReason != "verification profile policy is inactive before Canonical execution claim" ||
		queuedAttemptCount != 0 || queuedCleanupCount != 0 {
		t.Fatalf("queued Canonical policy reconciliation = state=%s reason=%q attempts=%d cleanups=%d",
			queuedState, queuedReason, queuedAttemptCount, queuedCleanupCount)
	}

	time.Sleep(1200 * time.Millisecond)
	targetCleaned := false
	for pass := 0; pass < 8; pass++ {
		cleanup, cleanupFound, cleanupErr := store.ClaimVerificationCleanup(
			ctx, verificationstore.ClaimVerificationCleanupInput{
				Scope: verificationstore.ScopeCanonical, ActorID: seed.actorID.String(),
				WorkerID: "canonical-policy-cleaner", LeaseDuration: time.Minute,
			},
		)
		if cleanupErr != nil {
			t.Fatalf("claim Canonical cleanup with inactive policy: %v", cleanupErr)
		}
		if !cleanupFound {
			break
		}
		if err := store.CompleteVerificationCleanup(ctx, verificationstore.CompleteVerificationCleanupInput{
			Lease: cleanup, ActorID: seed.actorID.String(),
		}); err != nil {
			t.Fatalf("complete Canonical cleanup with inactive policy: %v", err)
		}
		if cleanup.Fence.AttemptID == attemptID && cleanup.Fence.AttemptFenceEpoch == 1 {
			targetCleaned = true
			break
		}
	}
	if !targetCleaned {
		t.Fatal("inactive Canonical policy blocked exact cleanup claim")
	}
	reconciled, err := store.ReconcileInactiveVerificationExecution(
		ctx, verificationstore.ScopeCanonical, seed.actorID.String(),
	)
	if err != nil || !reconciled {
		t.Fatalf("reconcile inactive Canonical execution: reconciled=%t err=%v", reconciled, err)
	}
	var runState, attemptState, reason string
	if err := database.QueryRowContext(ctx, `
SELECT run.state, attempt.state, run.terminal_reason
FROM canonical_verification_runs AS run
JOIN canonical_verification_attempts AS attempt ON attempt.run_id = run.id
WHERE run.id = $1 AND attempt.id = $2
`, runID, attemptID).Scan(&runState, &attemptState, &reason); err != nil {
		t.Fatal(err)
	}
	if runState != "cancelled" || attemptState != "cancelled" ||
		!strings.Contains(reason, "policy is inactive") {
		t.Fatalf("Canonical policy reconciliation = %s/%s %q", runState, attemptState, reason)
	}
}

func assertReleaseBundlePublishGate(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	seed repositoryCandidateCanarySeed,
	receiptID uuid.UUID,
	receiptHash string,
	bundleID uuid.UUID,
	bundleHash string,
	staticBuildHash string,
) {
	t.Helper()
	qualityID, buildArtifactID := uuid.New(), uuid.New()
	buildContentHash := applicationBuildContractCanaryDigest("publish-gate-build-content")
	if _, err := database.ExecContext(ctx, `
INSERT INTO quality_runs (
  id, project_id, workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  status, score, runner_version, sandbox_kind, version, created_by, started_at, completed_at,
  build_artifact_id, build_content_ref, build_content_hash, build_hash,
  build_entry_path, build_file_count, build_total_bytes
) VALUES (
  $1, $2, $3, $4, $5, 'passed', 100, 'canonical-publish-canary', 'test', 1, $6, $7, $7,
  $8, $9, $10, $11, 'index.html', 1, 1024
)
`, qualityID, seed.projectID, seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceHash,
		seed.actorID, seed.createdAt, buildArtifactID, "content://canonical-web-static", buildContentHash, staticBuildHash); err != nil {
		t.Fatalf("insert publish gate quality artifact: %v", err)
	}
	deploymentID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO deployments (
  id, project_id, environment, environment_ref, provider, status,
  version, created_by, created_at, updated_at
) VALUES ($1, $2, 'preview', 'canary', 'canary', 'deploying', 1, $3, $4, $4)
`, deploymentID, seed.projectID, seed.actorID, seed.createdAt); err != nil {
		t.Fatalf("insert publish gate deployment: %v", err)
	}
	insertVersion := func(id uuid.UUID, withAuthority bool) error {
		query := `
INSERT INTO deployment_versions (
  id, deployment_id, number, action,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  build_manifest_id, quality_run_id,
  build_artifact_id, build_content_ref, build_content_hash, build_hash,
  build_entry_path, build_file_count, build_total_bytes,
  provider_ref, entry_path, checksum, file_count, total_bytes,
  environment_ref, environment_variable_names, status, message, created_by,
  canonical_receipt_id, canonical_receipt_hash, release_bundle_id, release_bundle_hash
) VALUES (
  $1, $2, 1, 'publish', $3, $4, $5, $6, $7,
  $8, $9, $10, $11, 'index.html', 1, 1024,
  '', '', '', 0, 0, 'canary', '[]'::jsonb, 'deploying', '', $12,
  $13, $14, $15, $16
)
`
		var authorityReceipt, authorityReceiptHash, authorityBundle, authorityBundleHash any
		if withAuthority {
			authorityReceipt, authorityReceiptHash = receiptID, receiptHash
			authorityBundle, authorityBundleHash = bundleID, bundleHash
		}
		_, err := database.ExecContext(ctx, query,
			id, deploymentID, seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceHash,
			seed.manifestID, qualityID, buildArtifactID, "content://canonical-web-static",
			buildContentHash, staticBuildHash, seed.actorID,
			authorityReceipt, authorityReceiptHash, authorityBundle, authorityBundleHash,
		)
		return err
	}
	if err := insertVersion(uuid.New(), false); err == nil || !strings.Contains(err.Error(), "require an exact Canonical Receipt") {
		t.Fatalf("deployment version without release authority was accepted: %v", err)
	}
	if err := insertVersion(uuid.New(), true); err != nil {
		t.Fatalf("deployment version with exact ReleaseBundle authority was rejected: %v", err)
	}
}

func canonicalReleaseMetadataArtifact(id, kind, mediaType string) map[string]any {
	return map[string]any{
		"id": id, "kind": kind, "store": "content", "ref": "content://canonical-" + id,
		"contentHash": applicationBuildContractCanaryDigest("canonical-" + id),
		"mediaType":   mediaType, "byteSize": 512,
	}
}

func assertCanonicalExecutionGuards(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	seed repositoryCandidateCanarySeed,
	planID uuid.UUID,
	planHash, manifestHash, contractHash, profileID, profileHash string,
	releaseArtifacts []byte,
) {
	t.Helper()
	runID, attemptID, receiptID := uuid.New(), uuid.New(), uuid.New()
	workerID := "canonical-worker-canary"
	if _, err := database.ExecContext(ctx, `
INSERT INTO canonical_verification_runs (
  id, schema_version, project_id, plan_id, plan_hash,
  request_key, request_hash, reason, state, version, fence_epoch, created_by, updated_by
) VALUES ($1, 'canonical-verification-run/v1', $2, $3, $4, $5, $6,
          'canonical fenced execution', 'queued', 1, 0, $7, $7)
`, runID, seed.projectID, planID, planHash, "canonical-execution-"+runID.String(),
		applicationBuildContractCanaryDigest("canonical-execution-request"), seed.actorID); err != nil {
		t.Fatalf("insert queued Canonical Run: %v", err)
	}
	claimTransaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimTransaction.ExecContext(ctx, `
UPDATE canonical_verification_runs
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = $2, lease_epoch = 1,
    lease_expires_at = statement_timestamp() + interval '5 minutes',
    started_at = statement_timestamp(), updated_by = $3
WHERE id = $1
`, runID, workerID, seed.actorID); err != nil {
		t.Fatalf("claim Canonical Run: %v", err)
	}
	if _, err := claimTransaction.ExecContext(ctx, `
INSERT INTO canonical_verification_attempts (
  id, schema_version, run_id, project_id, plan_id, plan_hash,
  ordinal, state, version, fence_epoch, created_by, updated_by
) VALUES ($1, 'canonical-verification-attempt/v1', $2, $3, $4, $5,
          1, 'queued', 1, 0, $6, $6)
`, attemptID, runID, seed.projectID, planID, planHash, seed.actorID); err != nil {
		t.Fatalf("insert Canonical Attempt: %v", err)
	}
	if _, err := claimTransaction.ExecContext(ctx, `
UPDATE canonical_verification_attempts AS attempt
SET state = 'claimed', version = 2, fence_epoch = run.fence_epoch,
    lease_worker_id = run.lease_worker_id, lease_epoch = run.lease_epoch,
    lease_expires_at = run.lease_expires_at, started_at = statement_timestamp(), updated_by = $3
FROM canonical_verification_runs AS run
WHERE attempt.id = $1 AND run.id = $2
`, attemptID, runID, seed.actorID); err != nil {
		t.Fatalf("claim Canonical Attempt: %v", err)
	}
	if _, err := claimTransaction.ExecContext(ctx, `
INSERT INTO verification_execution_cleanups (
  scope, project_id, run_id, attempt_id, attempt_fence_epoch,
  state, version, lease_epoch, created_by, updated_by
) VALUES ('canonical', $1, $2, $3, 1, 'registered', 1, 0, $4, $4)
`, seed.projectID, runID, attemptID, seed.actorID); err != nil {
		t.Fatalf("register Canonical execution cleanup: %v", err)
	}
	if err := claimTransaction.Commit(); err != nil {
		t.Fatalf("commit Canonical Run, Attempt, and cleanup atomically: %v", err)
	}
	version := 2
	for _, state := range []string{"materializing", "preparing", "running", "collecting"} {
		version++
		if _, err := database.ExecContext(ctx, `
UPDATE canonical_verification_runs
SET state = $2, version = $3, updated_by = $4
WHERE id = $1
`, runID, state, version, seed.actorID); err != nil {
			t.Fatalf("transition Canonical Run to %s: %v", state, err)
		}
		if _, err := database.ExecContext(ctx, `
UPDATE canonical_verification_attempts
SET state = $2, version = $3, updated_by = $4
WHERE id = $1
`, attemptID, state, version, seed.actorID); err != nil {
			t.Fatalf("transition Canonical Attempt to %s: %v", state, err)
		}
	}

	receiptHash := applicationBuildContractCanaryDigest("canonical-fenced-receipt")
	receiptContentHash := applicationBuildContractCanaryDigest("canonical-fenced-receipt-content")
	receiptInsertSQL := `
INSERT INTO canonical_verification_receipts (
  id, schema_version, scope, run_id, project_id, plan_id, plan_hash,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  verification_profile_id, verification_profile_version, verification_profile_hash,
  attempt_ids, release_artifacts, check_count, coverage_count,
  must_count, must_passed_count, blocker_count, warning_count, decision,
  content_store, content_ref, content_hash, payload_hash, created_by
) VALUES (
  $1, 'canonical-verification-receipt/v1', 'canonical', $2, $3, $4, $5,
  $6, $7, $8, $9, $10, $11, $12, $13, $14,
	  $15, 1, $16, $17::jsonb, $18::jsonb, 5, 1, 1, 1, 0, 0, 'passed',
	  'blob', $19, $20, $21, $22
)
	`
	receiptArgs := []any{receiptID, runID, seed.projectID, planID, planHash,
		seed.workspaceArtifactID, seed.workspaceRevisionID, seed.workspaceHash,
		seed.manifestID, manifestHash, seed.contractID, contractHash, seed.fullStackID, seed.fullStackHash,
		profileID, profileHash, `["` + attemptID.String() + `"]`, releaseArtifacts,
		"blob://canonical-receipts/" + receiptID.String(), receiptContentHash, receiptHash, seed.actorID}
	if _, err := database.ExecContext(ctx, receiptInsertSQL, receiptArgs...); err == nil ||
		!strings.Contains(err.Error(), "requires completed cleanup") {
		t.Fatalf("Canonical Receipt committed before exact cleanup completion: %v", err)
	}
	gormDatabase, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: database}), &gorm.Config{})
	if err != nil {
		t.Fatalf("open Canonical Receipt cleanup GORM store: %v", err)
	}
	executionStore, err := verificationstore.NewPostgresStore(gormDatabase, newVerificationCanaryContentStore())
	if err != nil {
		t.Fatal(err)
	}
	if err := executionStore.CompleteCanonicalExecutionCleanup(
		ctx,
		verificationstore.CanonicalExecutionLease{
			ProjectID: seed.projectID.String(), RunID: runID.String(), AttemptID: attemptID.String(),
			Plan:           verificationstore.PlanReference{ID: planID.String(), ContentHash: planHash},
			AttemptOrdinal: 1, State: verificationstore.RunCollecting,
			RunVersion: 6, RunFenceEpoch: 1, AttemptVersion: 6, AttemptFenceEpoch: 1,
			WorkerID: workerID, LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
		},
		seed.actorID.String(),
	); err != nil {
		t.Fatalf("complete exact live-fence Canonical execution cleanup: %v", err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, receiptInsertSQL, receiptArgs...); err != nil {
		t.Fatalf("insert fenced Canonical Receipt: %v", err)
	}
	checkKinds := []struct{ id, kind string }{
		{"oracle-repository", "contract"},
		{"release-artifacts", "release-manifest"},
		{"release-container-policy", "container-policy"},
		{"release-sbom", "sbom"},
		{"release-vulnerability", "vulnerability"},
	}
	for ordinal, check := range checkKinds {
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO canonical_verification_checks (
  receipt_id, ordinal, check_id, kind, required, status, attempt_id, truncated
) VALUES ($1, $2, $3, $4, true, 'passed', $5, false)
`, receiptID, ordinal, check.id, check.kind, attemptID); err != nil {
			t.Fatalf("insert Canonical check %s: %v", check.id, err)
		}
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO canonical_verification_obligation_coverage (
  receipt_id, ordinal, obligation_id, level, check_ids, status
) VALUES ($1, 0, 'OBL-REPOSITORY', 'must', '["oracle-repository"]'::jsonb, 'passed')
`, receiptID); err != nil {
		t.Fatalf("insert Canonical coverage: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE canonical_verification_attempts
SET state = 'passed', version = 7, lease_worker_id = NULL, lease_epoch = NULL,
    lease_expires_at = NULL, finished_at = statement_timestamp(), updated_by = $2
WHERE id = $1
`, attemptID, seed.actorID); err != nil {
		t.Fatalf("terminalize Canonical Attempt: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE canonical_verification_runs
SET state = 'passed', version = 7, lease_worker_id = NULL, lease_epoch = NULL,
    lease_expires_at = NULL, finished_at = statement_timestamp(), updated_by = $2
WHERE id = $1
`, runID, seed.actorID); err != nil {
		t.Fatalf("terminalize Canonical Run: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit complete Canonical Receipt: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE canonical_verification_attempts SET version = 8 WHERE id = $1`, attemptID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("terminal Canonical Attempt mutation was not rejected: %v", err)
	}
}
