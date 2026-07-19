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
)

func TestAgentControlPlaneMigrationDeclaresImmutableFencedLineage(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000031_agent_control_plane.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000031_agent_control_plane.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TABLE agent_context_packs",
		"CREATE TABLE agent_task_capsules",
		"CREATE TABLE agent_attempts",
		"CREATE TABLE agent_attempt_events",
		"ContextPack and TaskCapsule records are immutable",
		"AgentAttempt can change only through an append-only CAS event",
		"AgentAttempt events are append-only",
		"AgentAttempt event CAS update lost its worker fence",
		"AgentAttempt retry must preserve the exact terminal request and executor",
		"patch_ready requires platform Patch and structured result evidence",
		"review_ready requires platform validation evidence",
		"lease.reclaimed",
		"fence_epoch_to <> parent.fence_epoch + 1",
		"DEFERRABLE INITIALLY DEFERRED",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Agent control-plane migration is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"DROP TABLE IF EXISTS agent_attempt_events",
		"DROP TABLE IF EXISTS agent_attempts",
		"DROP TABLE IF EXISTS agent_task_capsules",
		"DROP TABLE IF EXISTS agent_context_packs",
		"DROP FUNCTION IF EXISTS validate_agent_attempt_event",
	} {
		if !strings.Contains(string(down), expected) {
			t.Fatalf("Agent control-plane rollback is missing %q", expected)
		}
	}
}

func TestAgentControlPlaneMigrationPostgresCanary(t *testing.T) {
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
	schema := "agent_control_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	if err := database.QueryRowContext(ctx, `
SELECT contract_hash FROM application_build_contracts WHERE id = $1
`, seed.contractID).Scan(&seed.contractHash); err != nil {
		t.Fatal(err)
	}
	candidate := createSandboxCandidateCanary(t, ctx, database, seed, "agent")
	sessionID := uuid.New()
	insertSandboxSessionCanary(t, ctx, database, seed, candidate.id, sessionID, uuid.Nil, true)
	assertSandboxTransition(t, ctx, database, sessionID, 1, 1, "starting", seed.actorID, "runner allocated", uuid.Nil, 2, 1, 1)
	assertSandboxTransition(t, ctx, database, sessionID, 2, 1, "ready", seed.actorID, "runner ready", uuid.Nil, 3, 1, 1)

	contextPackID := uuid.New()
	contextHash := applicationBuildContractCanaryDigest("agent-context-pack")
	contextItems := mustJSON(t, []map[string]any{{
		"key": "build-contract", "kind": "build_contract", "required": true,
		"source":  map[string]any{"id": seed.contractID.String(), "contentHash": seed.contractHash},
		"content": agentCanaryBlob(seed.contractID, "context", 512),
	}})
	var itemsValid, blobValid, sourceValid bool
	if err := database.QueryRowContext(ctx, `
SELECT agent_context_items_are_valid($1::jsonb),
       agent_blob_reference_is_valid(($1::jsonb)->0->'content'),
       agent_exact_reference_is_valid(($1::jsonb)->0->'source')
`, contextItems).Scan(&itemsValid, &blobValid, &sourceValid); err != nil {
		t.Fatal(err)
	}
	if !itemsValid || !blobValid || !sourceValid {
		t.Fatalf("ContextPack item validators: items=%t blob=%t source=%t payload=%s",
			itemsValid, blobValid, sourceValid, contextItems)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO agent_context_packs (
  id, schema_version, project_id, candidate_id, base_candidate_tree_hash,
  build_contract_id, build_contract_hash, items, content_hash, created_by
) VALUES ($1, 'agent-context-pack/v1', $2, $3, $4, $5, $6, $7, $8, $9)
`, contextPackID, seed.projectID, candidate.id, candidate.treeHash,
		seed.contractID, seed.contractHash, contextItems, contextHash, seed.actorID); err != nil {
		t.Fatalf("insert ContextPack: %v", err)
	}

	var templateReleases []byte
	if err := database.QueryRowContext(ctx, `
SELECT jsonb_agg(jsonb_build_object(
  'id', template_release_id::text,
  'contentHash', template_release_content_hash
) ORDER BY template_release_id::text)
FROM sandbox_session_template_releases
WHERE session_id = $1
`, sessionID).Scan(&templateReleases); err != nil {
		t.Fatal(err)
	}
	taskID := uuid.New()
	taskHash := applicationBuildContractCanaryDigest("agent-task-capsule")
	if _, err := database.ExecContext(ctx, `
INSERT INTO agent_task_capsules (
  id, schema_version, task_key, project_id, sandbox_session_id, candidate_id,
  candidate_version, candidate_session_epoch, candidate_writer_lease_epoch,
  base_candidate_tree_hash, build_contract_id, build_contract_hash,
  template_releases, context_pack_id, context_pack_hash, objective,
  obligation_ids, acceptance_criterion_ids, read_set, write_set, protected_paths,
  preconditions, postconditions, verification_command_ids, allowed_tools,
  network_policy, budgets, output_schema_hash, content_hash, created_by
) VALUES (
  $1, 'agent-task-capsule/v1', 'vertical-conversation', $2, $3, $4,
  $5, $6, $7, $8, $9, $10, $11, $12, $13,
  'Implement one exact vertical conversation slice.',
  '["OBL-1"]', '["AC-1"]', '["apps"]', '["apps/web"]', '[".github"]',
  '["BuildContract ready"]', '["Contract tests pass"]', '["test-contract"]',
  '["file.read","file.write","shell.exec"]',
  '{"mode":"none","allowedHosts":[]}',
  '{"wallTimeSeconds":900,"maxInputTokens":200000,"maxOutputTokens":50000,"maxCommands":100,"maxLogBytes":4194304,"maxPatchBytes":16777216}',
  $14, $15, $16
)
`, taskID, seed.projectID, sessionID, candidate.id, candidate.version,
		candidate.sessionEpoch, candidate.writerLeaseEpoch, candidate.treeHash,
		seed.contractID, seed.contractHash, templateReleases, contextPackID, contextHash,
		applicationBuildContractCanaryDigest("agent-output-schema"), taskHash, seed.actorID); err != nil {
		t.Fatalf("insert TaskCapsule: %v", err)
	}

	attemptID := uuid.New()
	executor := mustJSON(t, map[string]any{
		"adapter": "codex-cli", "provider": "openai", "model": "qualified-model",
		"runnerImageDigest": applicationBuildContractCanaryDigest("agent-runner"),
		"modelPolicyHash":   applicationBuildContractCanaryDigest("agent-model-policy"),
		"parametersHash":    applicationBuildContractCanaryDigest("agent-parameters"),
		"promptHash":        applicationBuildContractCanaryDigest("agent-prompt"),
		"outputSchemaHash":  applicationBuildContractCanaryDigest("agent-output-schema"),
		"toolchainHash":     applicationBuildContractCanaryDigest("agent-toolchain"),
	})
	requestHash := applicationBuildContractCanaryDigest("agent-request")
	configurationHash := applicationBuildContractCanaryDigest("agent-configuration")
	if _, err := database.ExecContext(ctx, `
INSERT INTO agent_attempts (
  id, schema_version, operation_id, project_id, sandbox_session_id, candidate_id,
  task_capsule_id, task_capsule_hash, context_pack_id, context_pack_hash,
  base_candidate_tree_hash, build_contract_hash, executor,
  request_key_hash, configuration_hash, state, version, fence_epoch,
  evidence, created_by
) VALUES (
  $1, 'agent-attempt/v1', 'attempt-create-1', $2, $3, $4,
  $5, $6, $7, $8, $9, $10, $11, $12, $13,
  'pending', 1, 0, '{}', $14
)
`, attemptID, seed.projectID, sessionID, candidate.id, taskID, taskHash,
		contextPackID, contextHash, candidate.treeHash, seed.contractHash,
		executor, requestHash, configurationHash, seed.actorID); err != nil {
		t.Fatalf("insert AgentAttempt: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE agent_attempts SET state = 'ready' WHERE id = $1`, attemptID); err == nil {
		t.Fatal("direct AgentAttempt projection update unexpectedly succeeded")
	}

	insertAgentCanaryEvent(t, ctx, database, attemptID, seed.actorID, "ready", "lifecycle.advanced", "", nil)
	insertAgentCanaryEvent(t, ctx, database, attemptID, seed.actorID, "queued", "lifecycle.advanced", "", nil)
	insertAgentCanaryEvent(t, ctx, database, attemptID, seed.actorID, "claimed", "lease.claimed", "runner-a", nil)
	if _, err := database.ExecContext(ctx, `SELECT pg_sleep(0.03)`); err != nil {
		t.Fatal(err)
	}
	insertAgentCanaryEvent(t, ctx, database, attemptID, seed.actorID, "claimed", "lease.reclaimed", "runner-b", nil)

	if _, err := database.ExecContext(ctx, `
INSERT INTO agent_attempt_events (
  attempt_id, sequence, version_from, version_to, state_from, state_to,
  fence_epoch_from, fence_epoch_to, event_kind, actor_id, worker_id, reason,
  lease_worker_id_to, lease_epoch_to, lease_expires_at_to, evidence_to
)
SELECT id, version, version, version + 1, state, 'running',
       fence_epoch - 1, fence_epoch - 1, 'lifecycle.advanced', $2, 'runner-a', 'stale worker',
       lease_worker_id, lease_epoch, lease_expires_at, evidence
FROM agent_attempts WHERE id = $1
`, attemptID, seed.actorID); err == nil || !strings.Contains(err.Error(), "fence precondition") {
		t.Fatalf("stale worker fence was not rejected: %v", err)
	}

	insertAgentCanaryEvent(t, ctx, database, attemptID, seed.actorID, "running", "lifecycle.advanced", "runner-b", nil)
	if _, err := database.ExecContext(ctx, `
INSERT INTO agent_attempt_events (
  attempt_id, sequence, version_from, version_to, state_from, state_to,
  fence_epoch_from, fence_epoch_to, event_kind, actor_id, worker_id, reason,
  lease_worker_id_to, lease_epoch_to, lease_expires_at_to, evidence_to,
  started_at_to
)
SELECT id, version, version, version + 1, state, 'patch_ready',
       fence_epoch, fence_epoch, 'lifecycle.advanced', $2, lease_worker_id, 'model claim only',
       lease_worker_id, lease_epoch, lease_expires_at, evidence, started_at
FROM agent_attempts WHERE id = $1
`, attemptID, seed.actorID); err == nil || !strings.Contains(err.Error(), "platform Patch") {
		t.Fatalf("patch_ready without platform evidence was not rejected: %v", err)
	}
	patchEvidence := map[string]any{
		"patch":            agentCanaryBlob(attemptID, "patch", 1024),
		"structuredResult": agentCanaryBlob(attemptID, "result", 512),
	}
	insertAgentCanaryEvent(t, ctx, database, attemptID, seed.actorID, "patch_ready", "lifecycle.advanced", "runner-b", patchEvidence)
	insertAgentCanaryEvent(t, ctx, database, attemptID, seed.actorID, "validating", "lifecycle.advanced", "runner-b", nil)
	validation := map[string]any{"validation": agentCanaryBlob(attemptID, "validation", 256)}
	insertAgentCanaryEvent(t, ctx, database, attemptID, seed.actorID, "review_ready", "lifecycle.advanced", "runner-b", validation)

	var state string
	var version, eventCount int64
	if err := database.QueryRowContext(ctx, `
SELECT attempt.state, attempt.version, count(event.sequence)
FROM agent_attempts AS attempt
JOIN agent_attempt_events AS event ON event.attempt_id = attempt.id
WHERE attempt.id = $1
GROUP BY attempt.id
`, attemptID).Scan(&state, &version, &eventCount); err != nil {
		t.Fatal(err)
	}
	if state != "review_ready" || version != 9 || eventCount != 8 {
		t.Fatalf("unexpected AgentAttempt projection: state=%s version=%d events=%d", state, version, eventCount)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM agent_attempt_events WHERE attempt_id = $1`, attemptID); err == nil {
		t.Fatal("append-only AgentAttempt event deletion unexpectedly succeeded")
	}
}

func insertAgentCanaryEvent(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	attemptID, actorID uuid.UUID,
	targetState, eventKind, workerID string,
	evidencePatch map[string]any,
) {
	t.Helper()
	var worker any
	if workerID != "" {
		worker = workerID
	}
	var evidence []byte
	if err := database.QueryRowContext(ctx, `SELECT evidence FROM agent_attempts WHERE id = $1`, attemptID).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if evidencePatch != nil {
		var current map[string]any
		if err := json.Unmarshal(evidence, &current); err != nil {
			t.Fatal(err)
		}
		for key, value := range evidencePatch {
			current[key] = value
		}
		evidence = mustJSON(t, current)
	}
	if eventKind == "lease.claimed" || eventKind == "lease.reclaimed" {
		leaseDuration := "5 minutes"
		if eventKind == "lease.claimed" {
			leaseDuration = "10 milliseconds"
		}
		if _, err := database.ExecContext(ctx, `
INSERT INTO agent_attempt_events (
  attempt_id, sequence, version_from, version_to, state_from, state_to,
  fence_epoch_from, fence_epoch_to, event_kind, actor_id, worker_id, reason,
  lease_worker_id_to, lease_epoch_to, lease_expires_at_to, evidence_to,
  started_at_to, finished_at_to, exit_reason_to
)
SELECT id, version, version, version + 1, state, $2,
       fence_epoch, fence_epoch + 1, $3, $4, $5, 'claim exact Attempt',
       $5, fence_epoch + 1, statement_timestamp() + $7::interval, $6,
       started_at, finished_at, exit_reason
FROM agent_attempts WHERE id = $1
`, attemptID, targetState, eventKind, actorID, worker, evidence, leaseDuration); err != nil {
			t.Fatalf("insert AgentAttempt %s event: %v", targetState, err)
		}
		return
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO agent_attempt_events (
  attempt_id, sequence, version_from, version_to, state_from, state_to,
  fence_epoch_from, fence_epoch_to, event_kind, actor_id, worker_id, reason,
  lease_worker_id_to, lease_epoch_to, lease_expires_at_to, evidence_to,
  started_at_to, finished_at_to, exit_reason_to
)
SELECT id, version, version, version + 1, state, $2,
       fence_epoch, fence_epoch, $3, $4, $5, 'advance exact Attempt',
       CASE WHEN $2 IN ('review_ready','verification_failed','failed','timed_out') THEN NULL ELSE lease_worker_id END,
       CASE WHEN $2 IN ('review_ready','verification_failed','failed','timed_out') THEN NULL ELSE lease_epoch END,
       CASE WHEN $2 IN ('review_ready','verification_failed','failed','timed_out') THEN NULL ELSE lease_expires_at END,
       $6, started_at, finished_at,
       CASE WHEN $2 IN ('verification_failed','failed','timed_out') THEN 'test exit' ELSE exit_reason END
FROM agent_attempts WHERE id = $1
`, attemptID, targetState, eventKind, actorID, worker, evidence); err != nil {
		t.Fatalf("insert AgentAttempt %s event: %v", targetState, err)
	}
}

func agentCanaryBlob(owner uuid.UUID, ref string, byteSize int64) map[string]any {
	return map[string]any{
		"store": "content", "ownerId": owner.String(), "ref": "agent-canary-" + ref,
		"contentHash": applicationBuildContractCanaryDigest("agent-canary-" + ref),
		"byteSize":    byteSize,
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
