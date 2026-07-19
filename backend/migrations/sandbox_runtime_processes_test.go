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

func TestSandboxRuntimeProcessesMigrationDeclaresExactAppendOnlyPersistence(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000027_sandbox_runtime_processes.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000027_sandbox_runtime_processes.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TABLE sandbox_runtime_processes",
		"CREATE TABLE sandbox_runtime_process_events",
		"session_version_at_creation bigint NOT NULL",
		"release.manifest->'commands'->NEW.command_id->'argv' = NEW.argv",
		"component.mount_path",
		"Sandbox process identity and exact command are immutable",
		"Sandbox process events are append-only",
		"event_count <> parent.version - 1",
		"DEFERRABLE INITIALLY DEFERRED",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Sandbox process migration is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"DROP TABLE IF EXISTS sandbox_runtime_process_events",
		"DROP TABLE IF EXISTS sandbox_runtime_processes",
		"DROP FUNCTION IF EXISTS sandbox_process_argv_is_valid",
	} {
		if !strings.Contains(string(down), expected) {
			t.Fatalf("Sandbox process rollback is missing %q", expected)
		}
	}
}

func TestSandboxRuntimeProcessesMigrationPostgresCanary(t *testing.T) {
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
	schema := "sandbox_process_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	candidate := createSandboxCandidateCanary(t, ctx, database, seed, "process")
	sessionID := uuid.New()
	insertSandboxSessionCanary(t, ctx, database, seed, candidate.id, sessionID, uuid.Nil, true)
	assertSandboxTransition(t, ctx, database, sessionID, 1, 1, "starting", seed.actorID, "runner allocated", uuid.Nil, 2, 1, 1)
	assertSandboxTransition(t, ctx, database, sessionID, 2, 1, "ready", seed.actorID, "runner ready", uuid.Nil, 3, 1, 1)

	// The shared migration fixture predates executable template commands. Add
	// one while its immutable trigger is deliberately disabled, then verify the
	// process guard binds argv/cwd to that exact JSON document.
	if _, err := database.ExecContext(ctx, `ALTER TABLE template_releases DISABLE TRIGGER template_release_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE template_releases AS release
SET manifest = jsonb_set(
  release.manifest,
  '{commands}',
  '{"dev":{"workingDirectory":".","argv":["node","server.js"]}}'::jsonb,
  true
)
FROM sandbox_session_services AS service
WHERE service.session_id = $1
  AND service.service_id = 'web'
  AND release.id = service.template_release_id
  AND release.content_hash = service.template_release_content_hash
`, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE template_releases ENABLE TRIGGER template_release_immutable`); err != nil {
		t.Fatal(err)
	}

	processID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO sandbox_runtime_processes (
  id, schema_version, project_id, session_id, session_epoch,
  session_version_at_creation, actor_id, service_id, command_id,
  template_release_id, template_release_content_hash,
  working_directory, argv, log_limit_bytes,
  state, version, log_bytes, log_truncated
)
SELECT $1, 'sandbox-process/v1', session.project_id, session.id, session.session_epoch,
       session.version, $2, service.service_id, 'dev',
       service.template_release_id, service.template_release_content_hash,
       component.mount_path, '["node","server.js"]'::jsonb, 1048576,
       'starting', 1, 0, false
FROM sandbox_sessions AS session
JOIN sandbox_session_services AS service
  ON service.session_id = session.id AND service.service_id = 'web'
JOIN full_stack_template_components AS component
  ON component.full_stack_template_id = session.full_stack_template_id
 AND component.full_stack_content_hash = session.full_stack_template_hash
 AND component.role = service.kind
WHERE session.id = $3
`, processID, seed.actorID, sessionID); err != nil {
		t.Fatalf("insert exact process: %v", err)
	}

	if _, err := database.ExecContext(ctx, `
INSERT INTO sandbox_runtime_processes (
  id, schema_version, project_id, session_id, session_epoch,
  session_version_at_creation, actor_id, service_id, command_id,
  template_release_id, template_release_content_hash,
  working_directory, argv, log_limit_bytes,
  state, version, log_bytes, log_truncated
)
SELECT $1, 'sandbox-process/v1', session.project_id, session.id, session.session_epoch,
       session.version, $2, service.service_id, 'dev',
       service.template_release_id, service.template_release_content_hash,
       'apps/web', '["node","different.js"]'::jsonb, 1048576,
       'starting', 1, 0, false
FROM sandbox_sessions AS session
JOIN sandbox_session_services AS service
  ON service.session_id = session.id AND service.service_id = 'web'
WHERE session.id = $3
`, uuid.New(), seed.actorID, sessionID); err == nil || !strings.Contains(err.Error(), "exact session service") {
		t.Fatalf("process with caller-chosen argv was accepted: %v", err)
	}

	if _, err := database.ExecContext(ctx, `
INSERT INTO sandbox_runtime_process_events (
  process_id, sequence, process_version_from, process_version_to,
  session_epoch, event_kind, state_from, state_to, actor_id, reason,
  pid_to, log_bytes_to, log_truncated_to, runtime_started_at_to
) VALUES ($1, 1, 1, 2, 1, 'runtime.observed', 'starting', 'running', $2,
          'supervisor reported running', 200, 0, false, statement_timestamp())
`, processID, seed.actorID); err != nil {
		t.Fatalf("append running observation: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO sandbox_runtime_process_events (
  process_id, sequence, process_version_from, process_version_to,
  session_epoch, event_kind, state_from, state_to, actor_id, signal, reason,
  pid_to, log_bytes_to, log_truncated_to, runtime_started_at_to
)
SELECT id, 2, 2, 3, session_epoch, 'signal.sent', 'running', 'running', $2, 'TERM',
       'TERM delivered to process group', pid, 12, false, runtime_started_at
FROM sandbox_runtime_processes WHERE id = $1
`, processID, seed.actorID); err != nil {
		t.Fatalf("append process signal: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO sandbox_runtime_process_events (
  process_id, sequence, process_version_from, process_version_to,
  session_epoch, event_kind, state_from, state_to, actor_id, reason,
  pid_to, exit_code_to, log_bytes_to, log_truncated_to,
  runtime_started_at_to, finished_at_to
)
SELECT id, 3, 3, 4, session_epoch, 'runtime.observed', 'running', 'exited', $2,
       'supervisor reported exit', pid, 0, 24, false,
       runtime_started_at, statement_timestamp()
FROM sandbox_runtime_processes WHERE id = $1
`, processID, seed.actorID); err != nil {
		t.Fatalf("append exited observation: %v", err)
	}

	var state string
	var version, eventCount int64
	if err := database.QueryRowContext(ctx, `
SELECT process.state, process.version, count(event.sequence)
FROM sandbox_runtime_processes AS process
LEFT JOIN sandbox_runtime_process_events AS event ON event.process_id = process.id
WHERE process.id = $1
GROUP BY process.state, process.version
`, processID).Scan(&state, &version, &eventCount); err != nil {
		t.Fatal(err)
	}
	if state != "exited" || version != 4 || eventCount != 3 {
		t.Fatalf("unexpected process projection: state=%s version=%d events=%d", state, version, eventCount)
	}
	if _, err := database.ExecContext(ctx, `UPDATE sandbox_runtime_processes SET state = 'running' WHERE id = $1`, processID); err == nil {
		t.Fatal("direct process projection update was accepted")
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM sandbox_runtime_process_events WHERE process_id = $1`, processID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("process event deletion was accepted: %v", err)
	}
}
