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

func TestSandboxTerminalsMigrationDeclaresFencedAppendOnlyLifecycle(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000028_sandbox_terminals.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000028_sandbox_terminals.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TABLE sandbox_terminals",
		"CREATE TABLE sandbox_terminal_events",
		"shell_path text NOT NULL CHECK (shell_path = '/bin/bash')",
		"parent.state <> 'ready'",
		"LEAST(parent.pid_limit, 8)",
		"Sandbox terminal identity and fixed shell are immutable",
		"Sandbox terminal events are append-only",
		"event_count <> parent.version - 1",
		"DEFERRABLE INITIALLY DEFERRED",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Sandbox terminal migration is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"DROP TABLE IF EXISTS sandbox_terminal_events",
		"DROP TABLE IF EXISTS sandbox_terminals",
	} {
		if !strings.Contains(string(down), expected) {
			t.Fatalf("Sandbox terminal rollback is missing %q", expected)
		}
	}
}

func TestSandboxTerminalsMigrationPostgresCanary(t *testing.T) {
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
	schema := "sandbox_terminal_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	candidate := createSandboxCandidateCanary(t, ctx, database, seed, "terminal")
	sessionID := uuid.New()
	insertSandboxSessionCanary(t, ctx, database, seed, candidate.id, sessionID, uuid.Nil, true)
	assertSandboxTransition(t, ctx, database, sessionID, 1, 1, "starting", seed.actorID, "runner allocated", uuid.Nil, 2, 1, 1)
	assertSandboxTransition(t, ctx, database, sessionID, 2, 1, "ready", seed.actorID, "runner ready", uuid.Nil, 3, 1, 1)

	terminalID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO sandbox_terminals (
  id, schema_version, project_id, session_id, session_epoch,
  session_version_at_creation, actor_id, working_directory, shell_path,
  rows, columns, output_limit_bytes, state, version, output_bytes, output_truncated
)
SELECT $1, 'sandbox-terminal/v1', project_id, id, session_epoch,
       version, $2, '.', '/bin/bash', 24, 80, 1048576,
       'opening', 1, 0, false
FROM sandbox_sessions WHERE id = $3
`, terminalID, seed.actorID, sessionID); err != nil {
		t.Fatalf("insert exact terminal: %v", err)
	}

	if _, err := database.ExecContext(ctx, `
INSERT INTO sandbox_terminal_events (
  terminal_id, sequence, terminal_version_from, terminal_version_to,
  session_epoch, event_kind, state_from, state_to, actor_id, request_id,
  reason, rows_to, columns_to, output_bytes_to, output_truncated_to,
  runtime_started_at_to
) VALUES ($1, 1, 1, 2, 1, 'runtime.opened', 'opening', 'running', $2, $1,
          'fixed shell opened', 24, 80, 0, false, statement_timestamp())
`, terminalID, seed.actorID); err != nil {
		t.Fatalf("append terminal opened event: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO sandbox_terminal_events (
  terminal_id, sequence, terminal_version_from, terminal_version_to,
  session_epoch, event_kind, state_from, state_to, actor_id, request_id,
  reason, rows_to, columns_to, output_bytes_to, output_truncated_to,
  runtime_started_at_to
)
SELECT id, 2, 2, 3, session_epoch, 'resized', 'running', 'running', $2, $3,
       'browser resized terminal', 40, 120, output_bytes, output_truncated,
       runtime_started_at
FROM sandbox_terminals WHERE id = $1
`, terminalID, seed.actorID, uuid.New()); err != nil {
		t.Fatalf("append terminal resize event: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO sandbox_terminal_events (
  terminal_id, sequence, terminal_version_from, terminal_version_to,
  session_epoch, event_kind, state_from, state_to, actor_id, request_id,
  reason, rows_to, columns_to, exit_code_to, output_bytes_to,
  output_truncated_to, runtime_started_at_to, finished_at_to
)
SELECT id, 3, 3, 4, session_epoch, 'runtime.exited', 'running', 'exited', $2, $1,
       'fixed shell exited', rows, columns, 0, 12, false,
       runtime_started_at, statement_timestamp()
FROM sandbox_terminals WHERE id = $1
`, terminalID, seed.actorID); err != nil {
		t.Fatalf("append terminal exit event: %v", err)
	}

	var state string
	var version, eventCount int64
	if err := database.QueryRowContext(ctx, `
SELECT terminal.state, terminal.version, count(event.sequence)
FROM sandbox_terminals AS terminal
LEFT JOIN sandbox_terminal_events AS event ON event.terminal_id = terminal.id
WHERE terminal.id = $1
GROUP BY terminal.state, terminal.version
`, terminalID).Scan(&state, &version, &eventCount); err != nil {
		t.Fatal(err)
	}
	if state != "exited" || version != 4 || eventCount != 3 {
		t.Fatalf("unexpected terminal projection: state=%s version=%d events=%d", state, version, eventCount)
	}
	if _, err := database.ExecContext(ctx, `UPDATE sandbox_terminals SET state = 'running' WHERE id = $1`, terminalID); err == nil {
		t.Fatal("direct terminal projection update was accepted")
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM sandbox_terminal_events WHERE terminal_id = $1`, terminalID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("terminal event deletion was accepted: %v", err)
	}
}
