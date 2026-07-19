package migrations

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLegacyAIImplementationProposalGateUpgradePostgresCanary(t *testing.T) {
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

	schema := "legacy_ai_proposal_gate_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	applyLegacyAIProposalMigrationsBetween(
		t, ctx, database, "", "000036_candidate_implementation_freezes.up.sql",
	)
	seed := seedRepositoryCandidateCanary(t, ctx, database)
	candidate := createSandboxCandidateCanary(t, ctx, database, seed, "legacy-ai-proposal-gate")
	candidate = acquireSandboxCandidateLeaseCanary(t, ctx, database, seed.actorID, candidate)
	candidateSnapshotID := createSandboxCheckpointCanary(
		t, ctx, database, candidate.id, seed.actorID, "legacy AI Proposal gate Candidate history",
	)

	legacyAIID := uuid.New()
	legacyWorkflowID := uuid.New()
	legacyCandidateID := uuid.New()
	instructionHash := applicationBuildContractCanaryDigest("legacy-ai-proposal-instruction")

	if _, err := database.ExecContext(ctx, `ALTER TABLE implementation_proposals DISABLE TRIGGER USER`); err != nil {
		t.Fatalf("disable pre-gate Proposal triggers for historical fixture: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO implementation_proposals (
  id, project_id, build_manifest_id, base_workspace_revision_id,
  status, version, content_store, content_ref, content_hash, payload_hash,
  operation_count, accepted_count, rejected_count, created_by, created_at,
  execution_source, instruction_hash, ai_provider, ai_model,
  application_build_contract_id, application_build_contract_hash
) VALUES
  ($1, $2, $3, $4, 'failed', 1, 'blob', $5, $6, $6,
   1, 0, 0, $7, statement_timestamp(), 'manual_generation', $8,
   'legacy-provider', 'legacy-model', $9, $10),
  ($11, $2, $3, $4, 'failed', 1, 'blob', $12, $13, $13,
   1, 0, 0, $7, statement_timestamp(), 'workflow_runner', $8,
   'legacy-provider', 'legacy-model', $9, $10)
`, legacyAIID, seed.projectID, seed.manifestID, seed.workspaceRevisionID,
		"blob://legacy-ai-proposals/"+legacyAIID.String(),
		applicationBuildContractCanaryDigest("legacy-ai-proposal"), seed.actorID,
		instructionHash, seed.contractID, seed.contractHash,
		legacyWorkflowID, "blob://legacy-ai-proposals/"+legacyWorkflowID.String(),
		applicationBuildContractCanaryDigest("legacy-workflow-proposal")); err != nil {
		t.Fatalf("insert historical AI Proposals under migration 36: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO implementation_proposals (
  id, project_id, build_manifest_id, base_workspace_revision_id,
  status, version, content_store, content_ref, content_hash, payload_hash,
  operation_count, accepted_count, rejected_count, created_by, created_at,
  execution_source, candidate_snapshot_id, candidate_base_tree_hash, candidate_tree_hash,
  application_build_contract_id, application_build_contract_hash
) VALUES (
  $1, $2, $3, $4, 'open', 1, 'blob', $5, $6, $6,
  1, 0, 0, $7, statement_timestamp(), 'candidate_freeze', $8, $9, $9, $10, $11
)
`, legacyCandidateID, seed.projectID, seed.manifestID, seed.workspaceRevisionID,
		"blob://legacy-candidate-proposals/"+legacyCandidateID.String(),
		applicationBuildContractCanaryDigest("legacy-candidate-proposal"), seed.actorID,
		candidateSnapshotID, candidate.treeHash, seed.contractID, seed.contractHash); err != nil {
		t.Fatalf("insert historical Candidate Proposal under migration 36: %v", err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE implementation_proposals ENABLE TRIGGER USER`); err != nil {
		t.Fatalf("re-enable pre-gate Proposal triggers: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO implementation_operation_decisions (
  proposal_id, operation_id, decision, reason, decided_by
) VALUES ($1, 'legacy-operation', 'accepted', 'historical decision', $2)
`, legacyAIID, seed.actorID); err != nil {
		t.Fatalf("insert historical AI decision: %v", err)
	}

	applyLegacyAIProposalMigrationsBetween(
		t, ctx, database,
		"000036_candidate_implementation_freezes.up.sql",
		"000052_verification_execution_cleanup_obligations.up.sql",
	)
	up, err := files.ReadFile("000053_legacy_ai_implementation_proposal_gate.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		t.Fatalf("upgrade schema with historical AI/Candidate Proposals and decisions: %v", err)
	}

	for _, proposalID := range []uuid.UUID{legacyAIID, legacyWorkflowID} {
		var unimplemented, blocking sql.NullInt64
		if err := database.QueryRowContext(ctx, `
SELECT unimplemented_count, blocking_diagnostic_count
FROM implementation_proposals WHERE id = $1
`, proposalID).Scan(&unimplemented, &blocking); err != nil {
			t.Fatal(err)
		}
		if unimplemented.Valid || blocking.Valid {
			t.Fatalf("historical AI Proposal %s projections were guessed: unimplemented=%v blocking=%v", proposalID, unimplemented, blocking)
		}
	}
	var candidateUnimplemented, candidateBlocking int
	if err := database.QueryRowContext(ctx, `
SELECT unimplemented_count, blocking_diagnostic_count
FROM implementation_proposals WHERE id = $1
`, legacyCandidateID).Scan(&candidateUnimplemented, &candidateBlocking); err != nil {
		t.Fatal(err)
	}
	if candidateUnimplemented != 0 || candidateBlocking != 0 {
		t.Fatalf("historical Candidate Proposal projections = %d/%d, want 0/0", candidateUnimplemented, candidateBlocking)
	}

	assertLegacyAIProposalGateRejected := func(name, want string, query string, args ...any) {
		t.Helper()
		_, gateErr := database.ExecContext(ctx, query, args...)
		if gateErr == nil || !strings.Contains(strings.ToLower(gateErr.Error()), strings.ToLower(want)) {
			t.Fatalf("%s error = %v, want %q", name, gateErr, want)
		}
	}
	assertLegacyAIProposalGateRejected(
		"historical unverified Candidate decision",
		"decisions require an exact verified freeze receipt", `
INSERT INTO implementation_operation_decisions (
  proposal_id, operation_id, decision, reason, decided_by
) VALUES ($1, 'historical-candidate-operation', 'accepted', 'must fail', $2)
`, legacyCandidateID, seed.actorID)

	// Model an imported/corrupt historical row that claims a complete binding
	// but has no same-Proposal freeze receipt. The decision boundary must prove
	// the receipt instead of trusting the three projected claim columns alone.
	orphanedExactCandidateID := uuid.New()
	orphanedVerificationReceiptID := uuid.New()
	orphanedVerificationReceiptHash := applicationBuildContractCanaryDigest("orphaned-exact-candidate-verification")
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO implementation_proposals (
  id, project_id, build_manifest_id, base_workspace_revision_id,
  status, version, content_store, content_ref, content_hash, payload_hash,
  operation_count, accepted_count, rejected_count, created_by, created_at,
  execution_source, candidate_snapshot_id, candidate_base_tree_hash, candidate_tree_hash,
  application_build_contract_id, application_build_contract_hash,
  candidate_verification_binding_version,
  candidate_verification_receipt_id, candidate_verification_receipt_hash,
  unimplemented_count, blocking_diagnostic_count
) VALUES (
  $1, $2, $3, $4, 'failed', 1, 'blob', $5, $6, $6,
  1, 0, 0, $7, statement_timestamp(), 'candidate_freeze', $8, $9, $9, $10, $11,
  'candidate-verification-binding/v1', $12, $13, 0, 0
)
`, orphanedExactCandidateID, seed.projectID, seed.manifestID, seed.workspaceRevisionID,
		"blob://orphaned-exact-candidate-proposals/"+orphanedExactCandidateID.String(),
		applicationBuildContractCanaryDigest("orphaned-exact-candidate-proposal"), seed.actorID,
		candidateSnapshotID, candidate.treeHash, seed.contractID, seed.contractHash,
		orphanedVerificationReceiptID, orphanedVerificationReceiptHash); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("seed orphaned exact Candidate claim: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit orphaned exact Candidate claim: %v", err)
	}
	assertLegacyAIProposalGateRejected(
		"Candidate decision without same-Proposal freeze receipt",
		"decisions require an exact verified freeze receipt", `
INSERT INTO implementation_operation_decisions (
  proposal_id, operation_id, decision, reason, decided_by
) VALUES ($1, 'orphaned-exact-candidate-operation', 'accepted', 'must fail', $2)
`, orphanedExactCandidateID, seed.actorID)

	assertLegacyAIProposalGateRejected(
		"historical unverified Candidate ready transition",
		"implementation_proposals_candidate_verification_shape", `
UPDATE implementation_proposals
SET status = 'ready', version = version + 1
WHERE id = $1
`, legacyCandidateID)
	assertLegacyAIProposalGateRejected(
		"historical unverified Candidate binding invention",
		"Candidate source and VerificationReceipt identity are immutable", `
UPDATE implementation_proposals
SET candidate_verification_binding_version = 'candidate-verification-binding/v1',
    candidate_verification_receipt_id = $2,
    candidate_verification_receipt_hash = $3
WHERE id = $1
`, legacyCandidateID, uuid.New(), applicationBuildContractCanaryDigest("invented-candidate-verification-receipt"))

	newUnverifiedCandidateID := uuid.New()
	assertLegacyAIProposalGateRejected(
		"new terminal unverified Candidate Proposal",
		"requires an exact VerificationReceipt binding", `
INSERT INTO implementation_proposals (
  id, project_id, build_manifest_id, base_workspace_revision_id,
  status, version, content_store, content_ref, content_hash, payload_hash,
  operation_count, accepted_count, rejected_count, created_by, created_at,
  execution_source, candidate_snapshot_id, candidate_base_tree_hash, candidate_tree_hash,
  application_build_contract_id, application_build_contract_hash,
  unimplemented_count, blocking_diagnostic_count
) VALUES (
  $1, $2, $3, $4, 'stale', 1, 'blob', $5, $6, $6,
  1, 0, 0, $7, statement_timestamp(), 'candidate_freeze', $8, $9, $9, $10, $11, 0, 0
)
`, newUnverifiedCandidateID, seed.projectID, seed.manifestID, seed.workspaceRevisionID,
		"blob://new-unverified-candidate-proposals/"+newUnverifiedCandidateID.String(),
		applicationBuildContractCanaryDigest("new-unverified-candidate-proposal"), seed.actorID,
		candidateSnapshotID, candidate.treeHash, seed.contractID, seed.contractHash)

	if _, err := database.ExecContext(ctx, `
UPDATE implementation_proposals
SET status = 'stale', version = version + 1
WHERE id = $1
`, legacyCandidateID); err != nil {
		t.Fatalf("terminalize historical unverified Candidate Proposal: %v", err)
	}
	var terminalCandidateStatus string
	if err := database.QueryRowContext(ctx, `
SELECT status FROM implementation_proposals WHERE id = $1
`, legacyCandidateID).Scan(&terminalCandidateStatus); err != nil {
		t.Fatal(err)
	}
	if terminalCandidateStatus != "stale" {
		t.Fatalf("historical unverified Candidate status = %q, want stale", terminalCandidateStatus)
	}

	for _, source := range []string{"manual_generation", "workflow_runner", "conversation_command"} {
		proposalID := uuid.New()
		var commandID any
		if source == "conversation_command" {
			commandID = proposalID
		}
		assertLegacyAIProposalGateRejected(
			"new "+source+" Proposal", "legacy AI ImplementationProposal creation is disabled", `
INSERT INTO implementation_proposals (
  id, project_id, build_manifest_id, base_workspace_revision_id,
  status, version, content_store, content_ref, content_hash, payload_hash,
  operation_count, accepted_count, rejected_count, created_by, created_at,
  execution_source, conversation_command_id, instruction_hash, ai_provider, ai_model,
  application_build_contract_id, application_build_contract_hash,
  unimplemented_count, blocking_diagnostic_count
) VALUES (
  $1, $2, $3, $4, 'open', 1, 'blob', $5, $6, $6,
  1, 0, 0, $7, statement_timestamp(), $8, $9, $10,
  'retired-provider', 'retired-model', $11, $12, 0, 0
)
`, proposalID, seed.projectID, seed.manifestID, seed.workspaceRevisionID,
			"blob://new-legacy-ai-proposals/"+proposalID.String(),
			applicationBuildContractCanaryDigest("new-legacy-ai-"+source), seed.actorID,
			source, commandID, instructionHash, seed.contractID, seed.contractHash,
		)
	}

	for _, status := range []string{"reviewing", "ready", "applied", "partially_applied"} {
		assertLegacyAIProposalGateRejected(
			"historical AI transition to "+status,
			"cannot be reviewable or applied",
			`UPDATE implementation_proposals SET status = $2, version = version + 1 WHERE id = $1`,
			legacyAIID, status,
		)
	}
	assertLegacyAIProposalGateRejected(
		"legacy AI decision insert", "legacy AI ImplementationProposal decisions are disabled", `
INSERT INTO implementation_operation_decisions (
  proposal_id, operation_id, decision, reason, decided_by
) VALUES ($1, 'new-operation', 'accepted', 'must fail', $2)
`, legacyAIID, seed.actorID)
	assertLegacyAIProposalGateRejected(
		"legacy AI decision update", "legacy AI ImplementationProposal decisions are disabled", `
UPDATE implementation_operation_decisions
SET reason = 'must fail' WHERE proposal_id = $1 AND operation_id = 'legacy-operation'
`, legacyAIID)
	assertLegacyAIProposalGateRejected(
		"legacy AI decision delete", "legacy AI ImplementationProposal decisions are disabled", `
DELETE FROM implementation_operation_decisions
WHERE proposal_id = $1 AND operation_id = 'legacy-operation'
`, legacyAIID)

	if _, err := database.ExecContext(ctx, `
UPDATE implementation_proposals
SET status = 'stale', version = version + 1
WHERE id = $1
`, legacyAIID); err != nil {
		t.Fatalf("terminalize historical AI Proposal: %v", err)
	}

	missingProjectionID := uuid.New()
	assertLegacyAIProposalGateRejected(
		"new Proposal without projections", "requires exact unimplemented and blocking diagnostic projections", `
INSERT INTO implementation_proposals (
  id, project_id, build_manifest_id, base_workspace_revision_id,
  status, version, content_store, content_ref, content_hash, payload_hash,
  operation_count, accepted_count, rejected_count, created_by, created_at,
  execution_source, application_build_contract_id, application_build_contract_hash
) VALUES (
  $1, $2, $3, $4, 'open', 1, 'blob', $5, $6, $6,
  1, 0, 0, $7, statement_timestamp(), 'manual_submission', $8, $9
)
`, missingProjectionID, seed.projectID, seed.manifestID, seed.workspaceRevisionID,
		"blob://manual-proposals/"+missingProjectionID.String(),
		applicationBuildContractCanaryDigest("missing-projection"), seed.actorID,
		seed.contractID, seed.contractHash)

	exactManualID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO implementation_proposals (
  id, project_id, build_manifest_id, base_workspace_revision_id,
  status, version, content_store, content_ref, content_hash, payload_hash,
  operation_count, accepted_count, rejected_count, created_by, created_at,
  execution_source, application_build_contract_id, application_build_contract_hash,
  unimplemented_count, blocking_diagnostic_count
) VALUES (
  $1, $2, $3, $4, 'open', 1, 'blob', $5, $6, $6,
  1, 0, 0, $7, statement_timestamp(), 'manual_submission', $8, $9, 0, 0
)
`, exactManualID, seed.projectID, seed.manifestID, seed.workspaceRevisionID,
		"blob://manual-proposals/"+exactManualID.String(),
		applicationBuildContractCanaryDigest("exact-manual"), seed.actorID,
		seed.contractID, seed.contractHash); err != nil {
		t.Fatalf("insert exact projected manual Proposal: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO implementation_operation_decisions (
  proposal_id, operation_id, decision, reason, decided_by
) VALUES ($1, 'manual-operation', 'accepted', 'allowed manual decision', $2)
`, exactManualID, seed.actorID); err != nil {
		t.Fatalf("manual Proposal decision was blocked: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE implementation_proposals
SET status = 'reviewing', version = version + 1
WHERE id = $1
`, exactManualID); err != nil {
		t.Fatalf("zero-projection manual Proposal could not enter review: %v", err)
	}
	assertLegacyAIProposalGateRejected(
		"projection mutation", "diagnostic projections are immutable", `
UPDATE implementation_proposals SET unimplemented_count = 1 WHERE id = $1
`, exactManualID)

	blockedManualID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO implementation_proposals (
  id, project_id, build_manifest_id, base_workspace_revision_id,
  status, version, content_store, content_ref, content_hash, payload_hash,
  operation_count, accepted_count, rejected_count, created_by, created_at,
  execution_source, application_build_contract_id, application_build_contract_hash,
  unimplemented_count, blocking_diagnostic_count
) VALUES (
  $1, $2, $3, $4, 'failed', 1, 'blob', $5, $6, $6,
  1, 0, 0, $7, statement_timestamp(), 'manual_submission', $8, $9, 1, 0
)
`, blockedManualID, seed.projectID, seed.manifestID, seed.workspaceRevisionID,
		"blob://manual-proposals/"+blockedManualID.String(),
		applicationBuildContractCanaryDigest("blocked-manual"), seed.actorID,
		seed.contractID, seed.contractHash); err != nil {
		t.Fatalf("insert projected non-reviewable manual Proposal: %v", err)
	}
	assertLegacyAIProposalGateRejected(
		"nonzero projection review transition",
		"requires zero unimplemented items and zero blocking diagnostics",
		`UPDATE implementation_proposals SET status = 'ready', version = version + 1 WHERE id = $1`,
		blockedManualID,
	)

	down, err := files.ReadFile("000053_legacy_ai_implementation_proposal_gate.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("rollback Proposal gate with historical and new rows: %v", err)
	}
	var projectionColumns int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'implementation_proposals'
  AND column_name IN ('unimplemented_count', 'blocking_diagnostic_count')
`).Scan(&projectionColumns); err != nil {
		t.Fatal(err)
	}
	if projectionColumns != 0 {
		t.Fatalf("rollback retained %d Proposal projection columns", projectionColumns)
	}
	if _, err := database.ExecContext(ctx, `
DELETE FROM implementation_operation_decisions
WHERE proposal_id = $1 AND operation_id = 'legacy-operation'
`, legacyAIID); err != nil {
		t.Fatalf("rollback retained legacy AI decision gate: %v", err)
	}
}

func applyLegacyAIProposalMigrationsBetween(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	after, last string,
) {
	t.Helper()
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name <= after {
			continue
		}
		if name > last {
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
}
