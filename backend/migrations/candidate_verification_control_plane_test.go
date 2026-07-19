package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/constructor"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/storage/content"
	verificationstore "github.com/worksflow/builder/backend/internal/verification"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCandidateVerificationControlPlaneMigrationDeclaresExactFencedAuthority(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000039_candidate_verification_control_plane.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000039_candidate_verification_control_plane.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TABLE verification_profile_versions",
		"CREATE TABLE verification_profile_policies",
		"CREATE TABLE candidate_verification_plans",
		"CREATE TABLE candidate_verification_runs",
		"CREATE TABLE candidate_verification_attempts",
		"VerificationProfile version content is immutable",
		"VerificationPlan must bind one exact current checkpoint, ready session, lineage, and active profile",
		"Candidate VerificationRun transition lost its live worker fence",
		"VerificationAttempt retry requires the previous terminal Attempt and explicit reason",
		"Terminal VerificationAttempt facts are immutable",
		"verification_normalize_sha256",
		"required_check_ids jsonb NOT NULL",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Candidate verification control-plane migration is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"DROP TABLE IF EXISTS candidate_verification_attempts",
		"DROP TABLE IF EXISTS candidate_verification_runs",
		"DROP TABLE IF EXISTS candidate_verification_plans",
		"DROP TABLE IF EXISTS verification_profile_policies",
		"DROP TABLE IF EXISTS verification_profile_versions",
	} {
		if !strings.Contains(string(down), expected) {
			t.Fatalf("Candidate verification control-plane rollback is missing %q", expected)
		}
	}
}

func TestCandidateVerificationReceiptMigrationDeclaresAtomicImmutableEvidence(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000040_candidate_verification_receipts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000040_candidate_verification_receipts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TABLE candidate_verification_receipts",
		"CREATE TABLE candidate_verification_checks",
		"CREATE TABLE candidate_verification_obligation_coverage",
		"VerificationReceipt is sealed; checks require its exact creation transaction and Attempt",
		"VerificationReceipt must list every exact terminal Attempt for its Run",
		"VerificationReceipt check projection is incomplete, reordered, or differs from its Plan",
		"VerificationReceipt obligation coverage is not derived from passed exact checks",
		"Terminal Candidate VerificationRun requires its exact immutable Receipt",
		"DEFERRABLE INITIALLY DEFERRED",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Candidate VerificationReceipt migration is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"DROP TABLE IF EXISTS candidate_verification_obligation_coverage",
		"DROP TABLE IF EXISTS candidate_verification_checks",
		"DROP TABLE IF EXISTS candidate_verification_receipts",
		"DROP FUNCTION IF EXISTS validate_candidate_verification_receipt_complete",
	} {
		if !strings.Contains(string(down), expected) {
			t.Fatalf("Candidate VerificationReceipt rollback is missing %q", expected)
		}
	}
}

func TestCandidateVerificationWorkerLeaseMigrationDeclaresFencedRecovery(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000042_candidate_verification_worker_leases.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000042_candidate_verification_worker_leases.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"verification-worker@system.worksflow",
		"guard_candidate_verification_run_worker_transition_v2",
		"guard_candidate_verification_attempt_worker_transition_v2",
		"Candidate VerificationRun heartbeat lost its live worker fence",
		"VerificationAttempt heartbeat lost its live worker fence",
		"OLD.lease_expires_at <= statement_timestamp()",
		"NEW.fence_epoch <> OLD.fence_epoch + 1",
		"NEW.state = 'claimed'",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Candidate verification worker lease migration is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"guard_candidate_verification_attempt_transition()",
		"guard_candidate_verification_run_transition()",
		"DROP FUNCTION IF EXISTS guard_candidate_verification_attempt_worker_transition_v2",
		"DROP FUNCTION IF EXISTS guard_candidate_verification_run_worker_transition_v2",
	} {
		if !strings.Contains(string(down), expected) {
			t.Fatalf("Candidate verification worker lease rollback is missing %q", expected)
		}
	}
}

func TestCandidateVerificationControlPlaneMigrationPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "candidate_verification_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	if err := Up(ctx, database); err != nil {
		t.Fatalf("migrations.Up failed: %v", err)
	}

	seed := seedRepositoryCandidateCanary(t, ctx, database)
	contents := newVerificationCanaryContentStore()
	prepareVerificationPlanningCanary(t, ctx, database, &seed, contents)
	candidate := createSandboxCandidateCanary(t, ctx, database, seed, "verification")
	candidate = acquireSandboxCandidateLeaseCanary(t, ctx, database, seed.actorID, candidate)
	sessionID := uuid.New()
	insertSandboxSessionCanary(t, ctx, database, seed, candidate.id, sessionID, uuid.Nil, true)
	assertSandboxTransition(t, ctx, database, sessionID, 1, 1, "starting", seed.actorID, "quality runner allocated", uuid.Nil, 2, 1, candidate.version)
	assertSandboxTransition(t, ctx, database, sessionID, 2, 1, "ready", seed.actorID, "quality runner ready", uuid.Nil, 3, 1, candidate.version)
	checkpointID := createSandboxCheckpointCanary(t, ctx, database, candidate.id, seed.actorID, "verify exact Candidate")
	var sessionVersion int64
	if err := database.QueryRowContext(ctx, `
SELECT attach_sandbox_session_checkpoint($1, 3, 1, $2, $3)
`, sessionID, seed.actorID, checkpointID).Scan(&sessionVersion); err != nil {
		t.Fatalf("attach verification checkpoint: %v", err)
	}
	if sessionVersion != 4 {
		t.Fatalf("checkpoint attach advanced Session to %d, want 4", sessionVersion)
	}

	profileID := "react-fastapi-postgres-v1"
	profileHash := applicationBuildContractCanaryDigest("verification-profile")
	profileDocument := mustJSON(t, map[string]any{
		"schemaVersion":          "verification-profile/v1",
		"id":                     profileID,
		"version":                1,
		"profileHash":            profileHash,
		"supportedTemplateRoles": []string{"web", "api"},
		"verifierImages": []map[string]any{{
			"role":  "node",
			"image": "registry.example/quality-node@" + applicationBuildContractCanaryDigest("quality-node-image"),
		}, {
			"role":  "python",
			"image": "registry.example/quality-python@" + applicationBuildContractCanaryDigest("quality-python-image"),
		}},
		"commandImageRoles": map[string]any{"web": "node", "api": "python"},
		"builtInChecks":     []any{},
		"limits":            map[string]any{},
		"networkPolicy": map[string]any{
			"dependencyResolver": map[string]any{"network": "bridge"},
		},
		"hiddenTestBundle": nil,
		"state":            "active",
	})
	if _, err := database.ExecContext(ctx, `
INSERT INTO verification_profile_versions (
  profile_id, version, schema_version, document, content_hash, created_by
) VALUES ($1, 1, 'verification-profile/v1', $2, $3, $4)
`, profileID, profileDocument, profileHash, seed.actorID); err != nil {
		t.Fatalf("insert VerificationProfile: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO verification_profile_policies (
  profile_id, profile_version, profile_hash, state, policy_version, reason, updated_by
) VALUES ($1, 1, $2, 'active', 1, 'qualified profile', $3)
`, profileID, profileHash, seed.actorID); err != nil {
		t.Fatalf("activate VerificationProfile: %v", err)
	}

	incompatibleProfileID := "worker-only-v1"
	incompatibleProfileHash := applicationBuildContractCanaryDigest("worker-only-verification-profile")
	incompatibleProfileDocument := mustJSON(t, map[string]any{
		"schemaVersion":          "verification-profile/v1",
		"id":                     incompatibleProfileID,
		"version":                1,
		"profileHash":            incompatibleProfileHash,
		"supportedTemplateRoles": []string{"worker"},
		"verifierImages": []map[string]any{{
			"role":  "node",
			"image": "registry.example/quality-node@" + applicationBuildContractCanaryDigest("quality-node-image"),
		}},
		"commandImageRoles": map[string]any{"worker": "node"},
		"builtInChecks":     []any{},
		"limits":            map[string]any{},
		"networkPolicy":     map[string]any{},
		"hiddenTestBundle":  nil,
		"state":             "active",
	})
	if _, err := database.ExecContext(ctx, `
INSERT INTO verification_profile_versions (
  profile_id, version, schema_version, document, content_hash, created_by
) VALUES ($1, 1, 'verification-profile/v1', $2, $3, $4)
`, incompatibleProfileID, incompatibleProfileDocument, incompatibleProfileHash, seed.actorID); err != nil {
		t.Fatalf("insert incompatible VerificationProfile: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO verification_profile_policies (
  profile_id, profile_version, profile_hash, state, policy_version, reason, updated_by
) VALUES ($1, 1, $2, 'active', 1, 'qualified incompatible profile', $3)
`, incompatibleProfileID, incompatibleProfileHash, seed.actorID); err != nil {
		t.Fatalf("activate incompatible VerificationProfile: %v", err)
	}

	if _, err := database.ExecContext(ctx, `
UPDATE verification_profile_versions SET content_hash = $2 WHERE profile_id = $1 AND version = 1
`, profileID, applicationBuildContractCanaryDigest("tampered-profile")); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("VerificationProfile mutation was not rejected: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO verification_profile_versions (
  profile_id, version, schema_version, document, content_hash, created_by
) VALUES ($1, 3, 'verification-profile/v1', $2, $3, $4)
`, profileID, profileDocument, profileHash, seed.actorID); err == nil || !strings.Contains(err.Error(), "contiguous") {
		t.Fatalf("noncontiguous VerificationProfile version was not rejected: %v", err)
	}

	var templateReleasesJSON, obligationsJSON []byte
	if err := database.QueryRowContext(ctx, `
SELECT jsonb_agg(jsonb_build_object(
         'role', role,
         'id', template_release_id::text,
         'contentHash', verification_normalize_sha256(template_release_content_hash)
       ) ORDER BY ordinal)
FROM application_build_contract_template_releases
WHERE contract_id = $1
`, seed.contractID).Scan(&templateReleasesJSON); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT jsonb_agg(jsonb_build_object(
         'id', obligation_id,
         'level', level,
         'status', status,
         'oracleIds', oracle_ids
       ) ORDER BY obligation_id)
FROM application_build_contract_obligations
WHERE contract_id = $1
`, seed.contractID).Scan(&obligationsJSON); err != nil {
		t.Fatal(err)
	}

	planID := uuid.New()
	manifestHash := verificationCanaryDigest(seed.manifestHash)
	contractHash := verificationCanaryDigest(seed.contractHash)
	type templateProjection struct {
		Role        string `json:"role"`
		ID          string `json:"id"`
		ContentHash string `json:"contentHash"`
	}
	var projectedTemplates []templateProjection
	if err := json.Unmarshal(templateReleasesJSON, &projectedTemplates); err != nil {
		t.Fatalf("decode exact TemplateRelease projection: %v", err)
	}
	planTemplates := make([]verificationstore.PlanTemplateRelease, 0, len(projectedTemplates))
	for _, template := range projectedTemplates {
		mountPath := "services/" + template.Role
		if template.Role == "web" {
			mountPath = "frontend"
		}
		planTemplates = append(planTemplates, verificationstore.PlanTemplateRelease{
			Role: template.Role, MountPath: mountPath,
			Release:     repository.ExactReference{ID: template.ID, ContentHash: template.ContentHash},
			SubjectHash: template.ContentHash,
		})
	}
	var planObligations []verificationstore.PlanObligation
	if err := json.Unmarshal(obligationsJSON, &planObligations); err != nil {
		t.Fatalf("decode exact obligation projection: %v", err)
	}
	planContent := verificationstore.PlanContent{
		SchemaVersion: verificationstore.PlanContentSchemaVersionV1,
		Scope:         verificationstore.ScopeCandidate,
		ProjectID:     seed.projectID.String(),
		Subject: verificationstore.CandidatePlanSubject{
			SessionID: sessionID.String(), SessionVersion: uint64(sessionVersion),
			CandidateID: candidate.id.String(), CandidateSnapshotID: checkpointID.String(),
			CandidateVersion: uint64(candidate.version), JournalSequence: uint64(candidate.journalSequence),
			SessionEpoch: uint64(candidate.sessionEpoch), WriterLeaseEpoch: uint64(candidate.writerLeaseEpoch),
			TreeStore: candidate.treeStore, TreeOwnerID: candidate.treeOwnerID.String(),
			TreeRef: candidate.treeRef, TreeContentHash: candidate.treeContentHash, TreeHash: candidate.treeHash,
		},
		BuildManifest:     repository.ExactReference{ID: seed.manifestID.String(), ContentHash: manifestHash},
		BuildContract:     repository.ExactReference{ID: seed.contractID.String(), ContentHash: contractHash},
		FullStackTemplate: repository.ExactReference{ID: seed.fullStackID.String(), ContentHash: seed.fullStackHash},
		Profile: verificationstore.ProfileReference{
			ID: profileID, Version: 1, ContentHash: profileHash,
		},
		TemplateReleases: planTemplates,
		Checks: []verificationstore.PlanCheck{{
			ID: "contract", Kind: "contract", ServiceID: "api", CommandID: "test-contract", Required: true,
			VerifierImageDigest: "registry.example/quality-python@" + applicationBuildContractCanaryDigest("quality-python-image"),
			Argv:                []string{"pytest", "tests/contract"}, WorkingDirectory: "services/api",
			OracleIDs: []string{"oracle-repository"}, AcceptanceCriterionIDs: []string{"AC-REPOSITORY"},
			ObligationIDs: []string{"OBL-REPOSITORY"}, DependsOn: []string{}, TimeoutSeconds: 900,
		}},
		Obligations: planObligations,
		RuntimePolicy: verificationstore.PlanRuntimePolicy{
			Limits: map[string]any{}, NetworkPolicy: map[string]any{},
		},
	}
	planDigest, err := domain.CanonicalHash(planContent)
	if err != nil {
		t.Fatalf("hash exact Candidate VerificationPlan: %v", err)
	}
	planHash := "sha256:" + planDigest
	compiledPlan := verificationstore.CompiledPlan{Content: planContent, PlanHash: planHash}
	gormDatabase, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: database}), &gorm.Config{})
	if err != nil {
		t.Fatalf("open verification GORM store: %v", err)
	}
	verificationStore, err := verificationstore.NewPostgresStore(gormDatabase, contents)
	if err != nil {
		t.Fatal(err)
	}
	planningSource, err := verificationstore.NewPostgresCandidatePlanSource(gormDatabase, contents)
	if err != nil {
		t.Fatal(err)
	}
	controlService, err := verificationstore.NewControlService(
		verificationStore, planningSource, verificationCanaryAuthorizer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	sourceRequest := verificationstore.CreateCandidateRunRequest{
		ProjectID: seed.projectID.String(), SessionID: sessionID.String(),
		CandidateID: candidate.id.String(), CheckpointID: checkpointID.String(),
		ExpectedSessionVersion:   uint64(sessionVersion),
		ExpectedSessionEpoch:     uint64(candidate.sessionEpoch),
		ExpectedCandidateVersion: uint64(candidate.version),
		ExpectedWriterLeaseEpoch: uint64(candidate.writerLeaseEpoch),
		VerificationProfile: verificationstore.ProfileReference{
			ID: profileID, Version: 1, ContentHash: profileHash,
		},
		Reason:  "compile exact verification from canonical sources",
		ActorID: seed.actorID.String(), OperationID: "verify-source-control",
	}
	sourceView, err := controlService.CreateCandidateRun(ctx, sourceRequest)
	if err != nil {
		t.Fatalf("create source-derived Candidate VerificationRun: %v", err)
	}
	if sourceView.Run.State != verificationstore.RunQueued || sourceView.Run.Replayed ||
		sourceView.Stale || sourceView.CheckCount != 1 || sourceView.RequiredCheckCount != 1 ||
		len(sourceView.AllowedActions) != 1 ||
		sourceView.AllowedActions[0] != verificationstore.RunActionCancel ||
		sourceView.Subject.CandidateSnapshotID != checkpointID.String() ||
		sourceView.BuildContract.ID != seed.contractID.String() ||
		sourceView.BuildContract.ContentHash != verificationCanaryDigest(seed.contractHash) {
		t.Fatalf("source-derived Candidate VerificationRun = %#v", sourceView)
	}
	sourcePlan, err := verificationStore.GetPlan(
		ctx, seed.projectID.String(), sourceView.Run.Plan.ID,
	)
	if err != nil {
		t.Fatalf("load source-derived Candidate VerificationPlan: %v", err)
	}
	if len(sourcePlan.Content.Checks) != 1 ||
		sourcePlan.Content.Checks[0].ID != "oracle-repository" ||
		!sourcePlan.Content.Checks[0].Required ||
		sourcePlan.Content.Checks[0].ServiceID != "api" ||
		sourcePlan.Content.Checks[0].WorkingDirectory != "apps/api" ||
		sourcePlan.Content.Checks[0].CommandID != "test-contract" {
		t.Fatalf("source-derived Candidate VerificationPlan lost exact constraints: %#v", sourcePlan)
	}
	replayedSourceView, err := controlService.CreateCandidateRun(ctx, sourceRequest)
	if err != nil || !replayedSourceView.Run.Replayed ||
		replayedSourceView.Run.ID != sourceView.Run.ID {
		t.Fatalf("replay source-derived Candidate VerificationRun = %#v, %v", replayedSourceView, err)
	}
	if _, err := controlService.CancelCandidateRun(ctx, verificationstore.CancelCandidateRunRequest{
		ProjectID: seed.projectID.String(), RunID: sourceView.Run.ID,
		ExpectedVersion: 1, ExpectedFenceEpoch: 0,
		Reason: "close source-derived canary Run", ActorID: seed.actorID.String(),
	}); err != nil {
		t.Fatalf("cancel source-derived Candidate VerificationRun: %v", err)
	}

	persistedPlan, err := verificationStore.SavePlan(ctx, planID.String(), seed.actorID.String(), compiledPlan)
	if err != nil {
		t.Fatalf("persist exact Candidate VerificationPlan: %v", err)
	}
	if persistedPlan.ID != planID.String() || persistedPlan.PlanHash != planHash || persistedPlan.Replayed {
		t.Fatalf("persisted Candidate VerificationPlan = %#v", persistedPlan)
	}
	replayedPlan, err := verificationStore.SavePlan(ctx, uuid.NewString(), seed.actorID.String(), compiledPlan)
	if err != nil || replayedPlan.ID != planID.String() || !replayedPlan.Replayed {
		t.Fatalf("idempotent Candidate VerificationPlan replay = %#v, %v", replayedPlan, err)
	}
	conflictingContent := planContent
	conflictingContent.Checks = append([]verificationstore.PlanCheck{}, planContent.Checks...)
	conflictingContent.Checks[0].TimeoutSeconds = 901
	conflictingDigest, err := domain.CanonicalHash(conflictingContent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verificationStore.SavePlan(ctx, planID.String(), seed.actorID.String(), verificationstore.CompiledPlan{
		Content: conflictingContent, PlanHash: "sha256:" + conflictingDigest,
	}); !errors.Is(err, verificationstore.ErrPlanConflict) {
		t.Fatalf("conflicting Candidate VerificationPlan replay = %v, want ErrPlanConflict", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE candidate_verification_plans SET check_count = 2 WHERE id = $1
`, planID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("VerificationPlan mutation was not rejected: %v", err)
	}

	queuedCancelInput, err := verificationstore.PrepareCreateRunInput(verificationstore.CreateRunInput{
		ID: uuid.NewString(), ProjectID: seed.projectID.String(),
		Plan:       verificationstore.PlanReference{ID: planID.String(), ContentHash: planHash},
		RequestKey: "verify-cancel-queued", Reason: "exercise queued cancellation",
		CreatedBy: seed.actorID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	queuedCancelRun, err := verificationStore.CreateRun(ctx, queuedCancelInput)
	if err != nil {
		t.Fatalf("create queued cancellation Run: %v", err)
	}
	if _, err := verificationStore.CancelRun(ctx, verificationstore.CancelRunInput{
		ProjectID: seed.projectID.String(), RunID: queuedCancelRun.ID,
		ExpectedVersion: 2, ExpectedFenceEpoch: 0,
		ActorID: seed.actorID.String(), Reason: "stale cancellation must fail",
	}); !errors.Is(err, verificationstore.ErrRunVersionConflict) {
		t.Fatalf("stale queued Run cancellation = %v, want ErrRunVersionConflict", err)
	}
	cancelledView, err := verificationStore.CancelRun(ctx, verificationstore.CancelRunInput{
		ProjectID: seed.projectID.String(), RunID: queuedCancelRun.ID,
		ExpectedVersion: 1, ExpectedFenceEpoch: 0,
		ActorID: seed.actorID.String(), Reason: "user cancelled queued verification",
	})
	if err != nil {
		t.Fatalf("cancel queued Candidate VerificationRun: %v", err)
	}
	if cancelledView.Run.State != verificationstore.RunCancelled || cancelledView.Run.Version != 2 ||
		len(cancelledView.AllowedActions) != 1 || cancelledView.AllowedActions[0] != verificationstore.RunActionRetry {
		t.Fatalf("cancelled Candidate VerificationRun view = %#v", cancelledView)
	}
	var queuedCleanupCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM verification_execution_cleanups
WHERE scope = 'candidate' AND run_id = $1
`, queuedCancelRun.ID).Scan(&queuedCleanupCount); err != nil || queuedCleanupCount != 0 {
		t.Fatalf("queued cancellation created cleanup obligations: count=%d err=%v", queuedCleanupCount, err)
	}
	activeCancelInput, err := verificationstore.PrepareCreateRunInput(verificationstore.CreateRunInput{
		ID: uuid.NewString(), ProjectID: seed.projectID.String(),
		Plan:       verificationstore.PlanReference{ID: planID.String(), ContentHash: planHash},
		RequestKey: "verify-cancel-active", Reason: "exercise active Attempt cancellation",
		CreatedBy: seed.actorID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	activeCancelRun, err := verificationStore.CreateRun(ctx, activeCancelInput)
	if err != nil {
		t.Fatalf("create active cancellation Run: %v", err)
	}
	activeAttemptID := uuid.New()
	activeClaim, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activeClaim.ExecContext(ctx, `
UPDATE candidate_verification_runs
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = 'quality-worker-cancel', lease_epoch = 1,
    lease_expires_at = statement_timestamp() + interval '5 minutes',
    started_at = statement_timestamp(), updated_by = $2
WHERE id = $1
`, activeCancelRun.ID, seed.actorID); err != nil {
		t.Fatalf("claim active cancellation Run: %v", err)
	}
	if _, err := activeClaim.ExecContext(ctx, `
INSERT INTO candidate_verification_attempts (
  id, schema_version, run_id, project_id, plan_id, plan_hash,
  ordinal, state, version, fence_epoch, created_by, updated_by
) VALUES (
  $1, 'candidate-verification-attempt/v1', $2, $3, $4, $5,
  1, 'queued', 1, 0, $6, $6
)
`, activeAttemptID, activeCancelRun.ID, seed.projectID, planID, planHash, seed.actorID); err != nil {
		t.Fatalf("insert active cancellation Attempt: %v", err)
	}
	if _, err := activeClaim.ExecContext(ctx, `
UPDATE candidate_verification_attempts
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = 'quality-worker-cancel', lease_epoch = 1,
    lease_expires_at = statement_timestamp() + interval '4 minutes',
    started_at = statement_timestamp(), updated_by = $2
WHERE id = $1
`, activeAttemptID, seed.actorID); err != nil {
		t.Fatalf("claim active cancellation Attempt: %v", err)
	}
	if _, err := activeClaim.ExecContext(ctx, `
INSERT INTO verification_execution_cleanups (
  scope, project_id, run_id, attempt_id, attempt_fence_epoch,
  state, version, lease_epoch, created_by, updated_by
) VALUES ('candidate', $1, $2, $3, 1, 'registered', 1, 0, $4, $4)
`, seed.projectID, activeCancelRun.ID, activeAttemptID, seed.actorID); err != nil {
		t.Fatalf("register active cancellation cleanup: %v", err)
	}
	if err := activeClaim.Commit(); err != nil {
		t.Fatalf("commit active cancellation Run, Attempt, and cleanup atomically: %v", err)
	}
	activeCancelledView, err := verificationStore.CancelRun(ctx, verificationstore.CancelRunInput{
		ProjectID: seed.projectID.String(), RunID: activeCancelRun.ID,
		ExpectedVersion: 2, ExpectedFenceEpoch: 1,
		ActorID: seed.actorID.String(), Reason: "user cancelled active verification",
	})
	if err != nil {
		t.Fatalf("atomically cancel active Run and Attempt: %v", err)
	}
	if activeCancelledView.Run.State != verificationstore.RunCancelled ||
		activeCancelledView.Run.Version != 3 || activeCancelledView.Run.FenceEpoch != 1 ||
		activeCancelledView.AttemptCount != 1 || activeCancelledView.LatestAttempt == nil ||
		activeCancelledView.LatestAttempt.ID != activeAttemptID.String() ||
		activeCancelledView.LatestAttempt.State != verificationstore.RunCancelled ||
		activeCancelledView.LatestAttempt.Version != 3 {
		t.Fatalf("active cancellation did not close exact Run and Attempt: %#v", activeCancelledView)
	}
	var activeCleanupState string
	if err := database.QueryRowContext(ctx, `
SELECT state FROM verification_execution_cleanups
WHERE scope = 'candidate' AND attempt_id = $1 AND attempt_fence_epoch = 1
`, activeAttemptID).Scan(&activeCleanupState); err != nil || activeCleanupState != "pending" {
		t.Fatalf("active cancellation cleanup state=%q err=%v, want pending", activeCleanupState, err)
	}
	if err := verificationStore.ConfirmVerificationOperationQuiesced(
		ctx, verificationstore.ScopeCandidate, verificationstore.VerificationExecutionFence{
			ProjectID: seed.projectID.String(), RunID: activeCancelRun.ID,
			AttemptID: activeAttemptID.String(), AttemptFenceEpoch: 1,
		}, "quality-worker-cancel", seed.actorID.String(),
	); err != nil {
		t.Fatalf("confirm cancelled execution quiescence: %v", err)
	}
	firstCleanup, found, err := verificationStore.ClaimVerificationCleanup(
		ctx, verificationstore.ClaimVerificationCleanupInput{
			Scope: verificationstore.ScopeCandidate, ActorID: seed.actorID.String(),
			WorkerID: "quality-cleanup-cancel-a", LeaseDuration: time.Minute,
		},
	)
	if err != nil || !found || firstCleanup.Fence.AttemptID != activeAttemptID.String() || firstCleanup.LeaseEpoch != 1 {
		t.Fatalf("claim cancelled cleanup = %#v, found=%t err=%v", firstCleanup, found, err)
	}
	if err := verificationStore.FailVerificationCleanup(ctx, verificationstore.FailVerificationCleanupInput{
		Lease: firstCleanup, ActorID: seed.actorID.String(), Reason: "daemon temporarily unavailable",
	}); err != nil {
		t.Fatalf("persist cancelled cleanup failure: %v", err)
	}
	if _, found, err := verificationStore.ClaimVerificationCleanup(
		ctx, verificationstore.ClaimVerificationCleanupInput{
			Scope: verificationstore.ScopeCandidate, ActorID: seed.actorID.String(),
			WorkerID: "quality-cleanup-cancel-hot-loop", LeaseDuration: time.Minute,
		},
	); err != nil || found {
		t.Fatalf("cleanup retry ignored bounded backoff: found=%t err=%v", found, err)
	}
	time.Sleep(300 * time.Millisecond)
	secondCleanup, found, err := verificationStore.ClaimVerificationCleanup(
		ctx, verificationstore.ClaimVerificationCleanupInput{
			Scope: verificationstore.ScopeCandidate, ActorID: seed.actorID.String(),
			WorkerID: "quality-cleanup-cancel-b", LeaseDuration: time.Minute,
		},
	)
	if err != nil || !found || secondCleanup.Fence.AttemptID != activeAttemptID.String() || secondCleanup.LeaseEpoch != 2 {
		t.Fatalf("retry cancelled cleanup = %#v, found=%t err=%v", secondCleanup, found, err)
	}
	if err := verificationStore.CompleteVerificationCleanup(ctx, verificationstore.CompleteVerificationCleanupInput{
		Lease: firstCleanup, ActorID: seed.actorID.String(),
	}); !errors.Is(err, verificationstore.ErrWorkerLeaseLost) {
		t.Fatalf("stale cleanup lease completed newer claim: %v", err)
	}
	if err := verificationStore.CompleteVerificationCleanup(ctx, verificationstore.CompleteVerificationCleanupInput{
		Lease: secondCleanup, ActorID: seed.actorID.String(),
	}); err != nil {
		t.Fatalf("complete retried cancelled cleanup: %v", err)
	}

	retryInput, err := verificationstore.PrepareCreateRunInput(verificationstore.CreateRunInput{
		ID: uuid.NewString(), ProjectID: seed.projectID.String(),
		Plan:       verificationstore.PlanReference{ID: planID.String(), ContentHash: planHash},
		RequestKey: "verify-cancel-retry", Reason: "retry exact cancelled Plan",
		ParentRunID: queuedCancelRun.ID, RetryReason: "explicit user retry after cancellation",
		CreatedBy: seed.actorID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	retryRun, err := verificationStore.CreateRun(ctx, retryInput)
	if err != nil || retryRun.ParentRunID != queuedCancelRun.ID || retryRun.RetryReason == "" {
		t.Fatalf("create explicit Candidate VerificationRun retry = %#v, %v", retryRun, err)
	}
	if _, err := verificationStore.CancelRun(ctx, verificationstore.CancelRunInput{
		ProjectID: seed.projectID.String(), RunID: retryRun.ID,
		ExpectedVersion: 1, ExpectedFenceEpoch: 0,
		ActorID: seed.actorID.String(), Reason: "close retry canary",
	}); err != nil {
		t.Fatalf("cancel retry canary Run: %v", err)
	}

	workerRunInput, err := verificationstore.PrepareCreateRunInput(verificationstore.CreateRunInput{
		ID: uuid.NewString(), ProjectID: seed.projectID.String(),
		Plan:       verificationstore.PlanReference{ID: planID.String(), ContentHash: planHash},
		RequestKey: "verify-worker-store", Reason: "exercise fenced worker persistence",
		CreatedBy: seed.actorID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	workerRun, err := verificationStore.CreateRun(ctx, workerRunInput)
	if err != nil {
		t.Fatalf("create worker persistence Run: %v", err)
	}
	legacyAttemptID := uuid.New()
	legacyClaim, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyClaim.ExecContext(ctx, `
UPDATE candidate_verification_runs
SET state = 'claimed', version = version + 1, fence_epoch = fence_epoch + 1,
    lease_worker_id = 'legacy-candidate-worker', lease_epoch = fence_epoch + 1,
    lease_expires_at = statement_timestamp() + interval '5 minutes',
    started_at = statement_timestamp(), updated_by = $2
WHERE id = $1 AND state = 'queued'
`, workerRun.ID, seed.actorID); err != nil {
		t.Fatalf("legacy Candidate Run claim: %v", err)
	}
	if _, err := legacyClaim.ExecContext(ctx, `
INSERT INTO candidate_verification_attempts (
  id, schema_version, run_id, project_id, plan_id, plan_hash,
  ordinal, state, version, fence_epoch, created_by, updated_by
) VALUES ($1, 'candidate-verification-attempt/v1', $2, $3, $4, $5,
          1, 'queued', 1, 0, $6, $6)
`, legacyAttemptID, workerRun.ID, seed.projectID, planID, planHash, seed.actorID); err != nil {
		t.Fatalf("legacy Candidate Attempt insert: %v", err)
	}
	if _, err := legacyClaim.ExecContext(ctx, `
UPDATE candidate_verification_attempts AS attempt
SET state = 'claimed', version = attempt.version + 1, fence_epoch = run.fence_epoch,
    lease_worker_id = run.lease_worker_id, lease_epoch = run.lease_epoch,
    lease_expires_at = run.lease_expires_at, started_at = statement_timestamp(), updated_by = $3
FROM candidate_verification_runs AS run
WHERE attempt.id = $1 AND run.id = $2
`, legacyAttemptID, workerRun.ID, seed.actorID); err != nil {
		t.Fatalf("legacy Candidate Attempt claim: %v", err)
	}
	if err := legacyClaim.Commit(); err == nil || !strings.Contains(err.Error(), "exact-fence cleanup registration") {
		t.Fatalf("legacy Candidate claim committed without exact cleanup registration: %v", err)
	}

	workerAttemptID := uuid.NewString()
	workerLease, found, err := verificationStore.ClaimCandidateExecution(
		ctx,
		verificationstore.ClaimCandidateExecutionInput{
			AttemptID: workerAttemptID, ActorID: seed.actorID.String(),
			WorkerID: "quality-worker-store-a", LeaseDuration: time.Second,
		},
	)
	if err != nil || !found || workerLease.RunID != workerRun.ID ||
		workerLease.AttemptID != workerAttemptID || workerLease.State != verificationstore.RunClaimed ||
		workerLease.RunVersion != 2 || workerLease.AttemptVersion != 2 ||
		workerLease.RunFenceEpoch != 1 || workerLease.AttemptFenceEpoch != 1 {
		t.Fatalf("claim exact worker Run/Attempt = %#v, found=%t err=%v", workerLease, found, err)
	}
	var workerCleanupState string
	if err := database.QueryRowContext(ctx, `
SELECT state FROM verification_execution_cleanups
WHERE scope = 'candidate' AND project_id = $1 AND run_id = $2
  AND attempt_id = $3 AND attempt_fence_epoch = $4
`, seed.projectID, workerLease.RunID, workerLease.AttemptID,
		workerLease.AttemptFenceEpoch).Scan(&workerCleanupState); err != nil || workerCleanupState != "registered" {
		t.Fatalf("Candidate claim cleanup registration = %q, err=%v", workerCleanupState, err)
	}
	workerLease, err = verificationStore.HeartbeatCandidateExecution(
		ctx,
		verificationstore.HeartbeatCandidateExecutionInput{
			Lease: workerLease, ActorID: seed.actorID.String(), LeaseDuration: time.Second,
		},
	)
	if err != nil || workerLease.RunVersion != 3 || workerLease.AttemptVersion != 3 {
		t.Fatalf("heartbeat claimed worker lease = %#v, %v", workerLease, err)
	}
	workerLease, err = verificationStore.TransitionCandidateExecution(
		ctx,
		verificationstore.TransitionCandidateExecutionInput{
			Lease: workerLease, ActorID: seed.actorID.String(), Target: verificationstore.RunMaterializing,
		},
	)
	if err != nil || workerLease.State != verificationstore.RunMaterializing ||
		workerLease.RunVersion != 4 || workerLease.AttemptVersion != 4 {
		t.Fatalf("advance worker lease = %#v, %v", workerLease, err)
	}
	workerLease, err = verificationStore.HeartbeatCandidateExecution(
		ctx,
		verificationstore.HeartbeatCandidateExecutionInput{
			Lease: workerLease, ActorID: seed.actorID.String(), LeaseDuration: time.Second,
		},
	)
	if err != nil || workerLease.RunVersion != 5 || workerLease.AttemptVersion != 5 {
		t.Fatalf("heartbeat materializing worker lease = %#v, %v", workerLease, err)
	}
	time.Sleep(1200 * time.Millisecond)
	cleanedWorkerFence := false
	for cleanupPass := 0; cleanupPass < 4; cleanupPass++ {
		cleanupLease, cleanupFound, cleanupErr := verificationStore.ClaimVerificationCleanup(
			ctx, verificationstore.ClaimVerificationCleanupInput{
				Scope: verificationstore.ScopeCandidate, ActorID: seed.actorID.String(),
				WorkerID: "quality-cleanup-store", LeaseDuration: time.Minute,
			},
		)
		if cleanupErr != nil {
			t.Fatalf("claim exact expired cleanup: %v", cleanupErr)
		}
		if !cleanupFound {
			break
		}
		if err := verificationStore.CompleteVerificationCleanup(
			ctx, verificationstore.CompleteVerificationCleanupInput{
				Lease: cleanupLease, ActorID: seed.actorID.String(),
			},
		); err != nil {
			t.Fatalf("complete exact expired cleanup: %v", err)
		}
		if cleanupLease.Fence.AttemptID == workerAttemptID && cleanupLease.Fence.AttemptFenceEpoch == 1 {
			cleanedWorkerFence = true
			break
		}
	}
	if !cleanedWorkerFence {
		t.Fatal("expired worker fence did not become an independently claimable cleanup")
	}
	reclaimed, found, err := verificationStore.ClaimCandidateExecution(
		ctx,
		verificationstore.ClaimCandidateExecutionInput{
			AttemptID: uuid.NewString(), ActorID: seed.actorID.String(),
			WorkerID: "quality-worker-store-b", LeaseDuration: time.Minute,
		},
	)
	if err != nil || !found || reclaimed.RunID != workerRun.ID ||
		reclaimed.AttemptID != workerAttemptID || reclaimed.State != verificationstore.RunClaimed ||
		reclaimed.RunVersion != 6 || reclaimed.AttemptVersion != 6 ||
		reclaimed.RunFenceEpoch != 2 || reclaimed.AttemptFenceEpoch != 2 {
		t.Fatalf("reclaim expired worker lease = %#v, found=%t err=%v", reclaimed, found, err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT state FROM verification_execution_cleanups
WHERE scope = 'candidate' AND project_id = $1 AND run_id = $2
  AND attempt_id = $3 AND attempt_fence_epoch = $4
`, seed.projectID, reclaimed.RunID, reclaimed.AttemptID,
		reclaimed.AttemptFenceEpoch).Scan(&workerCleanupState); err != nil || workerCleanupState != "registered" {
		t.Fatalf("Candidate reclaim cleanup registration = %q, err=%v", workerCleanupState, err)
	}
	workerCancelled, err := verificationStore.CancelRun(ctx, verificationstore.CancelRunInput{
		ProjectID: seed.projectID.String(), RunID: workerRun.ID,
		ExpectedVersion: reclaimed.RunVersion, ExpectedFenceEpoch: reclaimed.RunFenceEpoch,
		ActorID: seed.actorID.String(), Reason: "close worker persistence canary",
	})
	if err != nil || workerCancelled.Run.State != verificationstore.RunCancelled ||
		workerCancelled.LatestAttempt == nil ||
		workerCancelled.LatestAttempt.ID != workerAttemptID ||
		workerCancelled.LatestAttempt.State != verificationstore.RunCancelled {
		t.Fatalf("close reclaimed worker lease = %#v, %v", workerCancelled, err)
	}

	runID := uuid.New()
	runInput, err := verificationstore.PrepareCreateRunInput(verificationstore.CreateRunInput{
		ID: runID.String(), ProjectID: seed.projectID.String(),
		Plan:       verificationstore.PlanReference{ID: planID.String(), ContentHash: planHash},
		RequestKey: "verify-request-1", Reason: "verify before Candidate freeze",
		CreatedBy: seed.actorID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	createdRun, err := verificationStore.CreateRun(ctx, runInput)
	if err != nil || createdRun.ID != runID.String() || createdRun.Replayed {
		t.Fatalf("create Candidate VerificationRun = %#v, %v", createdRun, err)
	}
	replayRunInput := runInput
	replayRunInput.ID = uuid.NewString()
	replayedRun, err := verificationStore.CreateRun(ctx, replayRunInput)
	if err != nil || replayedRun.ID != runID.String() || !replayedRun.Replayed {
		t.Fatalf("idempotent Candidate VerificationRun replay = %#v, %v", replayedRun, err)
	}
	conflictingRunInput := runInput
	conflictingRunInput.ID, conflictingRunInput.Reason = uuid.NewString(), "different verification purpose"
	conflictingRunInput, err = verificationstore.PrepareCreateRunInput(conflictingRunInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verificationStore.CreateRun(ctx, conflictingRunInput); !errors.Is(err, verificationstore.ErrRunIdempotencyConflict) {
		t.Fatalf("conflicting Candidate VerificationRun request = %v, want ErrRunIdempotencyConflict", err)
	}
	attemptID := uuid.New()
	receiptFixtureClaim, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiptFixtureClaim.ExecContext(ctx, `
UPDATE candidate_verification_runs
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = 'quality-worker-a', lease_epoch = 1,
    lease_expires_at = statement_timestamp() + interval '5 minutes',
    started_at = statement_timestamp(), updated_by = $2
WHERE id = $1
`, runID, seed.actorID); err != nil {
		t.Fatalf("claim Candidate VerificationRun: %v", err)
	}

	if _, err := receiptFixtureClaim.ExecContext(ctx, `
INSERT INTO candidate_verification_attempts (
  id, schema_version, run_id, project_id, plan_id, plan_hash,
  ordinal, state, version, fence_epoch, created_by, updated_by
) VALUES (
  $1, 'candidate-verification-attempt/v1', $2, $3, $4, $5,
  1, 'queued', 1, 0, $6, $6
)
`, attemptID, runID, seed.projectID, planID, planHash, seed.actorID); err != nil {
		t.Fatalf("insert VerificationAttempt: %v", err)
	}
	if _, err := receiptFixtureClaim.ExecContext(ctx, `
UPDATE candidate_verification_attempts
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = 'quality-worker-a', lease_epoch = 1,
    lease_expires_at = statement_timestamp() + interval '4 minutes',
    started_at = statement_timestamp(), updated_by = $2
WHERE id = $1
`, attemptID, seed.actorID); err != nil {
		t.Fatalf("claim VerificationAttempt: %v", err)
	}
	if _, err := receiptFixtureClaim.ExecContext(ctx, `
INSERT INTO verification_execution_cleanups (
  scope, project_id, run_id, attempt_id, attempt_fence_epoch,
  state, version, lease_epoch, created_by, updated_by
) VALUES ('candidate', $1, $2, $3, 1, 'registered', 1, 0, $4, $4)
`, seed.projectID, runID, attemptID, seed.actorID); err != nil {
		t.Fatalf("register VerificationAttempt cleanup: %v", err)
	}
	if err := receiptFixtureClaim.Commit(); err != nil {
		t.Fatalf("commit Candidate Run, Attempt, and cleanup atomically: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE candidate_verification_attempts
SET state = 'materializing', version = 3, fence_epoch = 0, updated_by = $2
WHERE id = $1
`, attemptID, seed.actorID); err == nil || !strings.Contains(err.Error(), "worker fence") {
		t.Fatalf("stale VerificationAttempt fence was not rejected: %v", err)
	}
	for version, state := range []string{"materializing", "preparing", "running", "collecting"} {
		if _, err := database.ExecContext(ctx, `
UPDATE candidate_verification_attempts
SET state = $2, version = $3, updated_by = $4
WHERE id = $1
`, attemptID, state, version+3, seed.actorID); err != nil {
			t.Fatalf("advance VerificationAttempt to %s: %v", state, err)
		}
	}
	if _, err := database.ExecContext(ctx, `
UPDATE candidate_verification_attempts
SET state = 'failed', version = 7, lease_expires_at = NULL,
    terminal_reason = 'required check failed', finished_at = statement_timestamp(), updated_by = $2
WHERE id = $1
`, attemptID, seed.actorID); err != nil {
		t.Fatalf("finish failed VerificationAttempt: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE candidate_verification_attempts SET terminal_reason = 'tampered', version = 8, updated_by = $2
WHERE id = $1
`, attemptID, seed.actorID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("terminal VerificationAttempt mutation was not rejected: %v", err)
	}

	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_verification_attempts (
  id, schema_version, run_id, project_id, plan_id, plan_hash,
  ordinal, parent_attempt_id, state, version, fence_epoch, created_by, updated_by
) VALUES (
  $1, 'candidate-verification-attempt/v1', $2, $3, $4, $5,
  2, $6, 'queued', 1, 0, $7, $7
)
`, uuid.New(), runID, seed.projectID, planID, planHash, attemptID, seed.actorID); err == nil {
		t.Fatal("VerificationAttempt retry without reason was accepted")
	}
	retryAttemptID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_verification_attempts (
  id, schema_version, run_id, project_id, plan_id, plan_hash,
  ordinal, parent_attempt_id, retry_reason, state, version, fence_epoch, created_by, updated_by
) VALUES (
  $1, 'candidate-verification-attempt/v1', $2, $3, $4, $5,
  2, $6, 'fix deterministic fixture', 'queued', 1, 0, $7, $7
)
`, retryAttemptID, runID, seed.projectID, planID, planHash, attemptID, seed.actorID); err != nil {
		t.Fatalf("insert explicit VerificationAttempt retry: %v", err)
	}

	if _, err := database.ExecContext(ctx, `
UPDATE verification_profile_policies
SET state = 'deprecated', policy_version = 2, reason = 'canary deprecation', updated_by = $2
WHERE profile_id = $1 AND profile_version = 1 AND policy_version = 1
`, profileID, seed.actorID); err != nil {
		t.Fatalf("deprecate VerificationProfile policy: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_verification_runs (
  id, schema_version, project_id, plan_id, plan_hash,
  request_key, request_hash, reason, state, version, fence_epoch, created_by, updated_by
) VALUES (
  $1, 'candidate-verification-run/v1', $2, $3, $4,
  'verify-deprecated-profile', $5, 'must fail closed', 'queued', 1, 0, $6, $6
)
`, uuid.New(), seed.projectID, planID, planHash,
		applicationBuildContractCanaryDigest("deprecated-profile-request"), seed.actorID); err == nil || !strings.Contains(err.Error(), "active profile") {
		t.Fatalf("deprecated VerificationProfile created a new Run: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE verification_profile_policies
SET state = 'active', policy_version = 3, reason = 'canary reactivation', updated_by = $2
WHERE profile_id = $1 AND profile_version = 1 AND policy_version = 2
`, profileID, seed.actorID); err != nil {
		t.Fatalf("reactivate VerificationProfile policy: %v", err)
	}

	retryClaim, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retryClaim.ExecContext(ctx, `
UPDATE candidate_verification_attempts
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = 'quality-worker-a', lease_epoch = 1,
    lease_expires_at = statement_timestamp() + interval '4 minutes',
    started_at = statement_timestamp(), updated_by = $2
WHERE id = $1
`, retryAttemptID, seed.actorID); err != nil {
		t.Fatalf("claim retry VerificationAttempt: %v", err)
	}
	if _, err := retryClaim.ExecContext(ctx, `
INSERT INTO verification_execution_cleanups (
  scope, project_id, run_id, attempt_id, attempt_fence_epoch,
  state, version, lease_epoch, created_by, updated_by
) VALUES ('candidate', $1, $2, $3, 1, 'registered', 1, 0, $4, $4)
`, seed.projectID, runID, retryAttemptID, seed.actorID); err != nil {
		t.Fatalf("register retry VerificationAttempt cleanup: %v", err)
	}
	if err := retryClaim.Commit(); err != nil {
		t.Fatalf("commit retry Attempt and cleanup atomically: %v", err)
	}
	for version, state := range []string{"materializing", "preparing", "running", "collecting"} {
		if _, err := database.ExecContext(ctx, `
UPDATE candidate_verification_attempts
SET state = $2, version = $3, updated_by = $4
WHERE id = $1
`, retryAttemptID, state, version+3, seed.actorID); err != nil {
			t.Fatalf("advance retry VerificationAttempt to %s: %v", state, err)
		}
	}
	for version, state := range []string{"materializing", "preparing", "running", "collecting"} {
		if _, err := database.ExecContext(ctx, `
UPDATE candidate_verification_runs
SET state = $2, version = $3, updated_by = $4
WHERE id = $1
`, runID, state, version+3, seed.actorID); err != nil {
			t.Fatalf("advance Candidate VerificationRun to %s: %v", state, err)
		}
	}
	receiptID := uuid.New()
	checkStartedAt := time.Now().UTC().Truncate(time.Microsecond)
	receipt, err := verificationstore.NewCandidateReceipt(verificationstore.NewCandidateReceiptInput{
		ID: receiptID.String(), RunID: runID.String(), ProjectID: seed.projectID.String(),
		Subject: verificationstore.CandidateSubject{
			SessionID: sessionID.String(), CandidateID: candidate.id.String(),
			CandidateSnapshotID: checkpointID.String(),
			CandidateVersion:    uint64(candidate.version), JournalSequence: uint64(candidate.journalSequence),
			SessionEpoch: uint64(candidate.sessionEpoch), WriterLeaseEpoch: uint64(candidate.writerLeaseEpoch),
			TreeHash: candidate.treeHash,
		},
		BuildManifest:     repository.ExactReference{ID: seed.manifestID.String(), ContentHash: manifestHash},
		BuildContract:     repository.ExactReference{ID: seed.contractID.String(), ContentHash: contractHash},
		FullStackTemplate: repository.ExactReference{ID: seed.fullStackID.String(), ContentHash: seed.fullStackHash},
		Profile:           verificationstore.ProfileReference{ID: profileID, Version: 1, ContentHash: profileHash},
		Plan:              verificationstore.PlanReference{ID: planID.String(), ContentHash: planHash},
		AttemptIDs:        []string{attemptID.String(), retryAttemptID.String()},
		Checks: []verificationstore.CheckResult{{
			ID: "contract", Kind: "contract", ServiceID: "api", CommandID: "test-contract",
			Required: true, Status: verificationstore.CheckPassed, AttemptID: retryAttemptID.String(),
			VerifierImageDigest: "registry.example/quality-python@" + applicationBuildContractCanaryDigest("quality-python-image"),
			Argv:                []string{"pytest", "tests/contract"}, WorkingDirectory: "services/api",
			ExitCode: verificationCanaryExitCode(0), StartedAt: checkStartedAt,
			CompletedAt: checkStartedAt.Add(time.Second), DurationMS: 1000, AttemptCount: 2,
			OracleIDs: []string{"oracle-repository"}, AcceptanceCriterionIDs: []string{"AC-REPOSITORY"},
			ObligationIDs: []string{"OBL-REPOSITORY"}, Diagnostics: []verificationstore.Diagnostic{},
		}},
		Obligations: []verificationstore.ObligationRequirement{{
			ID: "OBL-REPOSITORY", Level: "must", OracleIDs: []string{"oracle-repository"},
		}},
		CreatedBy: seed.actorID.String(), CreatedAt: checkStartedAt.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("construct exact Candidate VerificationReceipt: %v", err)
	}
	if _, err := verificationStore.PersistReceipt(ctx, verificationstore.PersistReceiptInput{
		Receipt: receipt, ExpectedRunVersion: 6, ExpectedRunFenceEpoch: 1,
		ExpectedRunLeaseWorker: "quality-worker-a", ExpectedAttemptID: retryAttemptID.String(),
		ExpectedAttemptVersion: 6, ExpectedAttemptFence: 1,
	}); err == nil || !strings.Contains(err.Error(), "requires completed cleanup") {
		t.Fatalf("Receipt was committed before exact cleanup completion: %v", err)
	}
	for _, cleanedAttemptID := range []uuid.UUID{attemptID, retryAttemptID} {
		result, err := database.ExecContext(ctx, `
UPDATE verification_execution_cleanups
SET state = 'completed', version = 2, completed_at = statement_timestamp(), updated_by = $4
WHERE scope = 'candidate' AND project_id = $1 AND run_id = $2
  AND attempt_id = $3 AND attempt_fence_epoch = 1 AND state = 'registered'
`, seed.projectID, runID, cleanedAttemptID, seed.actorID)
		if err != nil {
			t.Fatalf("complete Receipt fixture cleanup: %v", err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			t.Fatalf("complete exact Receipt fixture cleanup rows=%d err=%v", rows, err)
		}
	}
	persisted, err := verificationStore.PersistReceipt(ctx, verificationstore.PersistReceiptInput{
		Receipt: receipt, ExpectedRunVersion: 6, ExpectedRunFenceEpoch: 1,
		ExpectedRunLeaseWorker: "quality-worker-a", ExpectedAttemptID: retryAttemptID.String(),
		ExpectedAttemptVersion: 6, ExpectedAttemptFence: 1,
	})
	if err != nil {
		t.Fatalf("persist exact Candidate VerificationReceipt: %v", err)
	}
	if persisted.PayloadHash != receipt.PayloadHash || persisted.Decision != verificationstore.DecisionPassed {
		t.Fatalf("persisted Candidate VerificationReceipt = %#v", persisted)
	}
	replayed, err := verificationStore.PersistReceipt(ctx, verificationstore.PersistReceiptInput{
		Receipt: receipt, ExpectedRunVersion: 6, ExpectedRunFenceEpoch: 1,
		ExpectedRunLeaseWorker: "quality-worker-a",
	})
	if err != nil || replayed.PayloadHash != receipt.PayloadHash {
		t.Fatalf("idempotent Candidate VerificationReceipt replay = %#v, %v", replayed, err)
	}

	activeProfiles, err := verificationStore.ListActiveProfiles(ctx, seed.projectID.String(), sessionID.String())
	if err != nil || len(activeProfiles) != 1 ||
		activeProfiles[0].VerificationProfile.ID != profileID ||
		activeProfiles[0].VerificationProfile.ContentHash != profileHash {
		t.Fatalf("active VerificationProfile catalog = %#v, %v", activeProfiles, err)
	}
	resolvedReceiptProject, err := verificationStore.ResolveReceiptProject(ctx, receiptID.String())
	if err != nil || resolvedReceiptProject != seed.projectID.String() {
		t.Fatalf("resolve Receipt project = %q, %v", resolvedReceiptProject, err)
	}
	loadedReceipt, err := verificationStore.GetReceipt(
		ctx, seed.projectID.String(), receiptID.String(),
	)
	if err != nil || loadedReceipt.PayloadHash != receipt.PayloadHash ||
		loadedReceipt.Decision != verificationstore.DecisionPassed {
		t.Fatalf("load immutable Receipt = %#v, %v", loadedReceipt, err)
	}

	var receiptDecision, runState string
	if err := database.QueryRowContext(ctx, `
SELECT receipt.decision, run.state
FROM candidate_verification_receipts AS receipt
JOIN candidate_verification_runs AS run ON run.id = receipt.run_id
WHERE receipt.id = $1 AND receipt.payload_hash = $2
`, receiptID, receipt.PayloadHash).Scan(&receiptDecision, &runState); err != nil {
		t.Fatal(err)
	}
	if receiptDecision != "passed" || runState != "passed" {
		t.Fatalf("committed Receipt/Run = %s/%s, want passed/passed", receiptDecision, runState)
	}
	passedView, err := verificationStore.GetRunView(ctx, seed.projectID.String(), runID.String())
	if err != nil {
		t.Fatalf("load passed Candidate VerificationRun view: %v", err)
	}
	if passedView.Stale || passedView.Receipt == nil ||
		passedView.Receipt.ID != receiptID.String() ||
		passedView.Receipt.ContentHash != receipt.PayloadHash ||
		passedView.ReceiptDecision == nil ||
		*passedView.ReceiptDecision != verificationstore.DecisionPassed ||
		passedView.CompletedCheckCount != 1 || passedView.MustCount != 1 ||
		passedView.MustPassedCount != 1 || len(passedView.BlockingReasons) != 0 ||
		len(passedView.AllowedActions) != 2 ||
		passedView.AllowedActions[0] != verificationstore.RunActionViewReceipt ||
		passedView.AllowedActions[1] != verificationstore.RunActionFreeze {
		t.Fatalf("fresh passed Run did not expose exact Receipt and Freeze action: %#v", passedView)
	}

	runHistory, err := verificationStore.ListRunViewsForSession(
		ctx, seed.projectID.String(), sessionID.String(), 20,
	)
	if err != nil {
		t.Fatalf("list SandboxSession VerificationRuns: %v", err)
	}
	foundPassedRun := false
	for _, historical := range runHistory {
		if historical.Run.ID == runID.String() &&
			historical.Receipt != nil &&
			historical.Receipt.ContentHash == receipt.PayloadHash {
			foundPassedRun = true
			break
		}
	}
	if !foundPassedRun {
		t.Fatalf("SandboxSession Run history omitted exact passed Run: %#v", runHistory)
	}

	if _, err := database.ExecContext(ctx, `
UPDATE candidate_verification_receipts SET warning_count = 1 WHERE id = $1
`, receiptID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("VerificationReceipt mutation was not rejected: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_verification_checks (
  receipt_id, run_id, ordinal, check_id, kind, required, status, attempt_id,
  verifier_image_digest, argv, working_directory, exit_code,
  started_at, completed_at, duration_ms, attempt_count,
  truncated, redaction_count, oracle_ids, acceptance_criterion_ids,
  obligation_ids, diagnostics
) VALUES (
  $1, $2, 1, 'late-check', 'contract', false, 'passed', $3,
  $4, '["true"]', '.', 0, $5, $5, 0, 1,
  false, 0, '[]', '[]', '[]', '[]'
)
`, receiptID, runID, retryAttemptID,
		"registry.example/quality-python@"+applicationBuildContractCanaryDigest("quality-python-image"),
		checkStartedAt); err == nil || !strings.Contains(err.Error(), "sealed") {
		t.Fatalf("late VerificationReceipt check append was not rejected: %v", err)
	}

	assertCandidateVerificationRunCannotPassWithoutReceipt(
		t, ctx, database, seed.actorID, seed.projectID, planID, planHash,
	)
}

func assertCandidateVerificationRunCannotPassWithoutReceipt(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	actorID, projectID, planID uuid.UUID,
	planHash string,
) {
	t.Helper()
	runID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_verification_runs (
  id, schema_version, project_id, plan_id, plan_hash,
  request_key, request_hash, reason, state, version, fence_epoch, created_by, updated_by
) VALUES (
  $1, 'candidate-verification-run/v1', $2, $3, $4,
  $5, $6, 'negative receipt gate', 'queued', 1, 0, $7, $7
)
`, runID, projectID, planID, planHash, "no-receipt-"+runID.String(),
		applicationBuildContractCanaryDigest("no-receipt-"+runID.String()), actorID); err != nil {
		t.Fatalf("insert negative Candidate VerificationRun: %v", err)
	}
	attemptID := uuid.New()
	claim, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claim.ExecContext(ctx, `
UPDATE candidate_verification_runs
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = 'quality-worker-negative', lease_epoch = 1,
    lease_expires_at = statement_timestamp() + interval '5 minutes',
    started_at = statement_timestamp(), updated_by = $2
WHERE id = $1
`, runID, actorID); err != nil {
		t.Fatalf("claim negative Candidate VerificationRun: %v", err)
	}
	if _, err := claim.ExecContext(ctx, `
INSERT INTO candidate_verification_attempts (
  id, schema_version, run_id, project_id, plan_id, plan_hash,
  ordinal, state, version, fence_epoch, created_by, updated_by
) VALUES (
  $1, 'candidate-verification-attempt/v1', $2, $3, $4, $5,
  1, 'queued', 1, 0, $6, $6
)
`, attemptID, runID, projectID, planID, planHash, actorID); err != nil {
		t.Fatalf("insert negative Candidate VerificationAttempt: %v", err)
	}
	if _, err := claim.ExecContext(ctx, `
UPDATE candidate_verification_attempts
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = 'quality-worker-negative', lease_epoch = 1,
    lease_expires_at = statement_timestamp() + interval '5 minutes',
    started_at = statement_timestamp(), updated_by = $2
WHERE id = $1
`, attemptID, actorID); err != nil {
		t.Fatalf("claim negative Candidate VerificationAttempt: %v", err)
	}
	if _, err := claim.ExecContext(ctx, `
INSERT INTO verification_execution_cleanups (
  scope, project_id, run_id, attempt_id, attempt_fence_epoch,
  state, version, lease_epoch, created_by, updated_by
) VALUES ('candidate', $1, $2, $3, 1, 'registered', 1, 0, $4, $4)
`, projectID, runID, attemptID, actorID); err != nil {
		t.Fatalf("register negative Candidate cleanup: %v", err)
	}
	if err := claim.Commit(); err != nil {
		t.Fatalf("commit negative Candidate claim fixture: %v", err)
	}
	for version, state := range []string{"materializing", "preparing", "running", "collecting"} {
		if _, err := database.ExecContext(ctx, `
UPDATE candidate_verification_runs
SET state = $2, version = $3, updated_by = $4
WHERE id = $1
`, runID, state, version+3, actorID); err != nil {
			t.Fatalf("advance negative Candidate VerificationRun to %s: %v", state, err)
		}
	}
	if _, err := database.ExecContext(ctx, `
UPDATE candidate_verification_runs
SET state = 'passed', version = 7, lease_expires_at = NULL,
    finished_at = statement_timestamp(), updated_by = $2
WHERE id = $1
`, runID, actorID); err == nil || !strings.Contains(err.Error(), "immutable Receipt") {
		t.Fatalf("Candidate VerificationRun passed without Receipt: %v", err)
	}
}

type verificationCanaryAuthorizer struct{}

func (verificationCanaryAuthorizer) RequireProjectView(context.Context, string, string) error {
	return nil
}

func (verificationCanaryAuthorizer) RequireProjectEdit(context.Context, string, string) error {
	return nil
}

func prepareVerificationPlanningCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	seed *repositoryCandidateCanarySeed,
	contents *verificationCanaryContentStore,
) {
	t.Helper()
	var source constructor.ExactRevisionRef
	if err := database.QueryRowContext(ctx, `
SELECT source_kind, purpose, required, artifact_id::text, revision_id::text,
       verification_normalize_sha256(content_hash)
FROM application_build_contract_sources
WHERE contract_id = $1
ORDER BY ordinal
LIMIT 1
`, seed.contractID).Scan(
		&source.Kind, &source.Purpose, &source.Required, &source.ArtifactID,
		&source.RevisionID, &source.ContentHash,
	); err != nil {
		t.Fatalf("load verification BuildContract source: %v", err)
	}
	source.ApprovalStatus = "approved"

	rows, err := database.QueryContext(ctx, `
SELECT role, template_release_id::text,
       verification_normalize_sha256(template_release_content_hash)
FROM application_build_contract_template_releases
WHERE contract_id = $1
ORDER BY ordinal
`, seed.contractID)
	if err != nil {
		t.Fatalf("load verification BuildContract TemplateReleases: %v", err)
	}
	defer rows.Close()
	releases := []constructor.TemplateReleaseRef{}
	for rows.Next() {
		var release constructor.TemplateReleaseRef
		if err := rows.Scan(&release.Role, &release.ID, &release.ReleaseHash); err != nil {
			t.Fatalf("scan verification TemplateRelease: %v", err)
		}
		release.Certification = "approved"
		release.PolicyStatus = "active"
		releases = append(releases, release)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate verification TemplateReleases: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("verification fixture has %d TemplateReleases, want 2", len(releases))
	}

	var compilerVersion, compilerHash string
	if err := database.QueryRowContext(ctx, `
SELECT compiler_version, verification_normalize_sha256(compiler_hash)
FROM application_build_contracts
WHERE id = $1
`, seed.contractID).Scan(&compilerVersion, &compilerHash); err != nil {
		t.Fatalf("load verification BuildContract compiler: %v", err)
	}
	contract := constructor.ContractContent{
		SchemaVersion: constructor.BuildContractSchemaVersion,
		Compiler: constructor.CompilerIdentity{
			Version: compilerVersion,
			Hash:    compilerHash,
		},
		ProjectID:       seed.projectID.String(),
		DeliverySliceID: "verification-canary",
		BuildManifest: constructor.BuildManifestRef{
			ID:          seed.manifestID.String(),
			ContentHash: verificationCanaryDigest(seed.manifestHash),
		},
		BaseWorkspace:   nil,
		SourceRevisions: []constructor.ExactRevisionRef{source},
		FullStackTemplate: constructor.FullStackTemplateRef{
			ID:            seed.fullStackID.String(),
			ContentHash:   seed.fullStackHash,
			Certification: "approved",
			PolicyStatus:  "active",
		},
		TemplateReleaseRefs: releases,
		Routes:              []constructor.RouteConstraint{},
		States:              []constructor.StateConstraint{},
		ContractBindings:    []constructor.ContractBinding{},
		AcceptanceCriteria: []constructor.AcceptanceCriterion{{
			ID: "AC-REPOSITORY", Statement: "The repository contract check passes.",
			RequirementIDs: []string{"REQ-REPOSITORY"}, SourceRevision: source,
		}},
		Oracles: []constructor.Oracle{{
			ID: "oracle-repository", AcceptanceCriterionIDs: []string{"AC-REPOSITORY"},
			Kind: "contract", Target: "repository", CommandID: "test-contract",
			SourceRevision: source,
		}},
		Obligations: []constructor.Obligation{{
			ID: "OBL-REPOSITORY", Level: "must", Kind: "acceptance",
			SourceRevision: source, SourceAnchorID: "AC-REPOSITORY",
			OracleIDs: []string{"oracle-repository"}, DependsOn: []string{},
			Waivable: false, Status: constructor.StatusReady,
		}},
		Waivers:         []constructor.Waiver{},
		Gaps:            []constructor.BuildGap{},
		Conflicts:       []constructor.BuildConflict{},
		ForbiddenClaims: []string{},
		Status:          constructor.StatusReady,
	}
	payload, err := domain.CanonicalJSON(contract)
	if err != nil {
		t.Fatalf("encode exact verification BuildContract: %v", err)
	}
	hash, err := domain.CanonicalHash(contract)
	if err != nil {
		t.Fatalf("hash exact verification BuildContract: %v", err)
	}
	contentHash := "sha256:" + hash
	contentRef := "verification-contract-" + seed.contractID.String()

	if _, err := database.ExecContext(ctx,
		`ALTER TABLE application_build_contracts DISABLE TRIGGER application_build_contract_immutable`,
	); err != nil {
		t.Fatalf("disable BuildContract immutability for exact fixture setup: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE application_build_contracts
SET content_store = 'memory', content_ref = $2, content_hash = $3, contract_hash = $4
WHERE id = $1
`, seed.contractID, contentRef, contentHash, hash); err != nil {
		t.Fatalf("bind exact verification BuildContract content: %v", err)
	}
	if _, err := database.ExecContext(ctx,
		`ALTER TABLE application_build_contracts ENABLE TRIGGER application_build_contract_immutable`,
	); err != nil {
		t.Fatalf("restore BuildContract immutability after fixture setup: %v", err)
	}
	seed.contractHash = hash

	if _, err := database.ExecContext(ctx,
		`ALTER TABLE template_releases DISABLE TRIGGER template_release_immutable`,
	); err != nil {
		t.Fatalf("disable TemplateRelease immutability for exact fixture setup: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE template_releases
SET manifest = manifest || jsonb_build_object(
  'commands', '{"test-contract":{"workingDirectory":".","argv":["pytest","tests/contract"]}}'::jsonb,
  'toolchains', jsonb_build_array(jsonb_build_object(
    'name', 'python', 'version', '3.12.4',
    'image', 'registry.example/python@' || $2::text
  )),
  'lockfiles', jsonb_build_array(jsonb_build_object(
    'path', 'requirements.lock', 'digest', $3::text,
    'registry', 'https://pypi.org/simple'
  ))
)
WHERE id = (
  SELECT template_release_id
  FROM application_build_contract_template_releases
  WHERE contract_id = $1 AND role = 'api'
)
`, seed.contractID, applicationBuildContractCanaryDigest("python-toolchain-image"), applicationBuildContractCanaryDigest("api-requirements-lock")); err != nil {
		t.Fatalf("add exact verification command to API TemplateRelease fixture: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE template_releases
SET manifest = manifest || jsonb_build_object(
  'toolchains', jsonb_build_array(jsonb_build_object(
    'name', 'node', 'version', '22.4.1',
    'image', 'registry.example/node@' || $2::text
  )),
  'lockfiles', jsonb_build_array(jsonb_build_object(
    'path', 'package-lock.json', 'digest', $3::text,
    'registry', 'https://registry.npmjs.org'
  ))
)
WHERE id = (
  SELECT template_release_id
  FROM application_build_contract_template_releases
  WHERE contract_id = $1 AND role = 'web'
)
`, seed.contractID, applicationBuildContractCanaryDigest("node-toolchain-image"), applicationBuildContractCanaryDigest("web-package-lock")); err != nil {
		t.Fatalf("add exact dependency identity to Web TemplateRelease fixture: %v", err)
	}
	if _, err := database.ExecContext(ctx,
		`ALTER TABLE template_releases ENABLE TRIGGER template_release_immutable`,
	); err != nil {
		t.Fatalf("restore TemplateRelease immutability after fixture setup: %v", err)
	}

	now := time.Now().UTC()
	contents.mu.Lock()
	contents.values[contentRef] = content.StoredContent{
		Reference: content.Reference{
			ID: contentRef, ContentHash: contentHash,
			ByteSize: int64(len(payload)), SchemaVersion: 2,
		},
		ProjectID: seed.projectID.String(), AggregateType: "application_build_contract",
		AggregateID: seed.contractID.String(), State: content.StateFinalized,
		Payload: append(json.RawMessage(nil), payload...), CreatedAt: now, FinalizedAt: &now,
	}
	contents.mu.Unlock()
}

func verificationCanaryDigest(value string) string {
	if strings.HasPrefix(value, "sha256:") {
		return value
	}
	return "sha256:" + value
}

func verificationCanaryExitCode(value int) *int {
	return &value
}

type verificationCanaryContentStore struct {
	mu     sync.Mutex
	values map[string]content.StoredContent
}

func newVerificationCanaryContentStore() *verificationCanaryContentStore {
	return &verificationCanaryContentStore{values: map[string]content.StoredContent{}}
}

func (store *verificationCanaryContentStore) PutPending(
	_ context.Context,
	projectID, aggregateType, aggregateID string,
	schemaVersion int,
	payload json.RawMessage,
) (content.Reference, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	digest := sha256.Sum256(payload)
	reference := content.Reference{
		ID: uuid.NewString(), ContentHash: "sha256:" + hex.EncodeToString(digest[:]),
		ByteSize: int64(len(payload)), SchemaVersion: schemaVersion,
	}
	store.values[reference.ID] = content.StoredContent{
		Reference: reference, ProjectID: projectID, AggregateType: aggregateType,
		AggregateID: aggregateID, State: content.StatePending,
		Payload: append(json.RawMessage(nil), payload...), CreatedAt: time.Now().UTC(),
	}
	return reference, nil
}

func (store *verificationCanaryContentStore) Finalize(_ context.Context, contentID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	stored, ok := store.values[contentID]
	if !ok || stored.State == content.StateAborted {
		return content.ErrContentNotFound
	}
	now := time.Now().UTC()
	stored.State = content.StateFinalized
	stored.FinalizedAt = &now
	store.values[contentID] = stored
	return nil
}

func (store *verificationCanaryContentStore) Abort(_ context.Context, contentID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	stored, ok := store.values[contentID]
	if !ok {
		return content.ErrContentNotFound
	}
	if stored.State == content.StatePending {
		stored.State = content.StateAborted
		store.values[contentID] = stored
	}
	return nil
}

func (store *verificationCanaryContentStore) Get(
	_ context.Context,
	contentID, expectedHash string,
) (content.StoredContent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	stored, ok := store.values[contentID]
	if !ok || stored.State == content.StateAborted {
		return content.StoredContent{}, content.ErrContentNotFound
	}
	if expectedHash != "" && stored.ContentHash != expectedHash {
		return content.StoredContent{}, content.ErrHashMismatch
	}
	stored.Payload = append(json.RawMessage(nil), stored.Payload...)
	return stored, nil
}
