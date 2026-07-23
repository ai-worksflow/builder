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
	"github.com/worksflow/builder/backend/internal/workflow"
)

const workflowInputAuthorityMigrationFenceKey = "worksflow:workflow-input-authority-migration:v1"

func TestWorkflowInputAuthorityConcurrencyPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "workflow_input_concurrency_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	database.SetMaxOpenConns(8)
	defer database.Close()
	applyPostgresMigrationsForCanary(t, database)

	t.Run("runtime readers share migration fence", func(t *testing.T) {
		assertWorkflowInputRuntimeMigrationFence(t, ctx, database)
	})

	fixture := seedWorkflowInputCanary(t, ctx, database)
	fixture = bindWorkflowInputConcurrencyCanaryToAdmissibleV3(t, ctx, database, fixture)
	assertWorkflowInputExactFreeze(t, ctx, database, fixture)
	t.Run("current assertion retains current fact locks", func(t *testing.T) {
		assertWorkflowInputCurrentFactWriterWaits(t, ctx, database, fixture)
	})
}

func bindWorkflowInputConcurrencyCanaryToAdmissibleV3(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture workflowInputCanary,
) workflowInputCanary {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	seeded, err := workflow.MinimumLoopDefinition(fixture.definitionID.String(), fixture.userID.String(), now)
	if err != nil {
		t.Fatalf("build concurrency canary production workflow: %v", err)
	}
	nodes := append([]domain.NodeDefinition(nil), seeded.Nodes...)
	var qualitySchema json.RawMessage
	for _, node := range nodes {
		if node.ID == "quality" {
			qualitySchema = append(json.RawMessage(nil), node.OutputSchema...)
			break
		}
	}
	if len(qualitySchema) == 0 {
		t.Fatal("concurrency canary production workflow lacks its release quality gate")
	}
	external := domain.ExactExternalQualificationGateConfig()
	nodes = append(nodes, domain.NodeDefinition{
		ID: "external-qualification", Name: "External qualification", Type: domain.NodeExternalQualificationGate,
		InputSchema: qualitySchema, OutputSchema: qualitySchema, ExternalQualificationGate: &external,
	})
	edges := make([]domain.WorkflowEdge, 0, len(seeded.Edges)+1)
	for _, edge := range seeded.Edges {
		if edge.From == "quality" && edge.To == "publish" {
			edges = append(edges,
				domain.WorkflowEdge{ID: "quality-to-external", From: "quality", To: "external-qualification"},
				domain.WorkflowEdge{ID: "external-publish", From: "external-qualification", To: "publish"},
			)
			continue
		}
		edges = append(edges, edge)
	}
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		fixture.definitionID.String(), 1, "Workflow Input concurrency canary", "6", nodes, edges,
		workflow.ProjectBriefInputContract(), workflow.ApplicationOutputContract(),
		workflow.WorkflowExecutionProfileV3Ref(), fixture.userID.String(), now,
	)
	if err != nil {
		t.Fatalf("compile concurrency canary production workflow: %v", err)
	}
	if err := workflow.ValidateDefinitionForExecutionProfile(definition, workflow.WorkflowExecutionProfileV3Ref()); err != nil {
		t.Fatalf("validate concurrency canary production workflow: %v", err)
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE workflow_definition_versions
		SET schema_version=6,content=$2,content_hash=$3,
		    execution_profile_version=$4,execution_profile_hash=$5
		WHERE id=$1`, fixture.definitionVersionID, encoded, definition.Hash,
		definition.ExecutionProfile.Version, definition.ExecutionProfile.Hash); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("bind concurrency canary to production workflow: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit concurrency canary production workflow: %v", err)
	}
	var admissible bool
	if err := database.QueryRowContext(ctx, `SELECT workflow_execution_profile_v3_definition_is_database_admissible(content)
		FROM workflow_definition_versions WHERE id=$1`, fixture.definitionVersionID).Scan(&admissible); err != nil || !admissible {
		t.Fatalf("concurrency canary production workflow admissible=%t error=%v", admissible, err)
	}
	fixture.definitionRaw = encoded
	return fixture
}

func assertWorkflowInputRuntimeMigrationFence(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	firstConnection, firstTransaction, firstPID := beginWorkflowInputConcurrencyTransaction(t, ctx, database)
	defer firstConnection.Close()
	defer firstTransaction.Rollback()
	assertWorkflowInputRuntimeFenceHeld(t, ctx, firstTransaction)

	secondConnection, secondTransaction, secondPID := beginWorkflowInputConcurrencyTransaction(t, ctx, database)
	defer secondConnection.Close()
	defer secondTransaction.Rollback()
	assertWorkflowInputRuntimeFenceHeld(t, ctx, secondTransaction)

	var sharedHolders int
	if err := database.QueryRowContext(ctx, `
WITH expected AS (
  SELECT pg_catalog.hashtextextended($3, 0) AS lock_key
)
SELECT count(DISTINCT lock.pid)
FROM pg_catalog.pg_locks AS lock
CROSS JOIN expected
WHERE lock.pid IN ($1, $2)
  AND lock.locktype = 'advisory'
  AND lock.mode = 'ShareLock'
  AND lock.granted
  AND lock.objsubid = 1
  AND lock.classid = (((expected.lock_key >> 32) & 4294967295)::oid)
  AND lock.objid = ((expected.lock_key & 4294967295)::oid)`,
		firstPID, secondPID, workflowInputAuthorityMigrationFenceKey,
	).Scan(&sharedHolders); err != nil {
		t.Fatal(err)
	}
	if sharedHolders != 2 {
		t.Fatalf("runtime migration-fence shared holders = %d, want 2", sharedHolders)
	}

	exclusiveConnection, exclusiveTransaction, exclusivePID := beginWorkflowInputConcurrencyTransaction(t, ctx, database)
	defer exclusiveConnection.Close()
	defer exclusiveTransaction.Rollback()
	exclusiveContext, cancelExclusive := context.WithCancel(ctx)
	defer cancelExclusive()
	exclusiveResult := make(chan error, 1)
	go func() {
		_, err := exclusiveTransaction.ExecContext(exclusiveContext, `
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended($1, 0)
)`, workflowInputAuthorityMigrationFenceKey)
		exclusiveResult <- err
	}()

	waitForWorkflowInputPostgresLock(t, ctx, database, exclusivePID, "advisory", "ExclusiveLock", exclusiveResult)
	if err := firstTransaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertWorkflowInputPostgresLockWaiting(t, ctx, database, exclusivePID, "advisory", "ExclusiveLock")
	if err := secondTransaction.Rollback(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-exclusiveResult:
		if err != nil {
			t.Fatalf("exclusive migration fence after shared holders released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exclusive migration fence did not acquire after both runtime holders released")
	}
}

func assertWorkflowInputRuntimeFenceHeld(t *testing.T, ctx context.Context, transaction *sql.Tx) {
	t.Helper()
	var authorityCount int
	if err := transaction.QueryRowContext(ctx, `
SELECT count(*)
FROM assert_current_workflow_input_authority_v1($1)`, uuid.New()).Scan(&authorityCount); err != nil {
		t.Fatalf("invoke Workflow Input current assertion: %v", err)
	}
	if authorityCount != 0 {
		t.Fatalf("missing Workflow Input authority assertion rows = %d, want 0", authorityCount)
	}
}

func assertWorkflowInputCurrentFactWriterWaits(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture workflowInputCanary,
) {
	t.Helper()
	assertConnection, assertTransaction, _ := beginWorkflowInputConcurrencyTransaction(t, ctx, database)
	defer assertConnection.Close()
	defer assertTransaction.Rollback()
	var authorityCount int
	if err := assertTransaction.QueryRowContext(ctx, `
SELECT count(*)
FROM assert_current_workflow_input_authority_v1($1)`, fixture.authorityID).Scan(&authorityCount); err != nil {
		t.Fatalf("assert current Workflow Input authority: %v", err)
	}
	if authorityCount != 1 {
		t.Fatalf("current Workflow Input authority assertion rows = %d, want 1", authorityCount)
	}

	writerConnection, writerTransaction, writerPID := beginWorkflowInputConcurrencyTransaction(t, ctx, database)
	defer writerConnection.Close()
	defer writerTransaction.Rollback()
	writerContext, cancelWriter := context.WithCancel(ctx)
	defer cancelWriter()
	writerResult := make(chan error, 1)
	go func() {
		_, err := writerTransaction.ExecContext(writerContext, `
UPDATE workflow_runs
SET event_cursor = event_cursor
WHERE id = $1`, fixture.runID)
		writerResult <- err
	}()

	waitForWorkflowInputPostgresLock(t, ctx, database, writerPID, "transactionid", "ShareLock", writerResult)
	if err := assertTransaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-writerResult:
		if err != nil {
			t.Fatalf("current-fact writer after assertion transaction ended: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("current-fact writer remained blocked after assertion transaction ended")
	}
}

func beginWorkflowInputConcurrencyTransaction(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) (*sql.Conn, *sql.Tx, int) {
	t.Helper()
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	var backendPID int
	if err := transaction.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&backendPID); err != nil {
		_ = transaction.Rollback()
		_ = connection.Close()
		t.Fatal(err)
	}
	return connection, transaction, backendPID
}

func waitForWorkflowInputPostgresLock(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	backendPID int,
	lockType string,
	mode string,
	result <-chan error,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-result:
			t.Fatalf("PostgreSQL operation completed before exposing %s/%s wait: %v", lockType, mode, err)
		default:
		}
		var waiting bool
		if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM pg_catalog.pg_locks
  WHERE pid = $1
    AND locktype = $2
    AND mode = $3
    AND NOT granted
)`, backendPID, lockType, mode).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("PostgreSQL backend %d did not expose %s/%s wait", backendPID, lockType, mode)
}

func assertWorkflowInputPostgresLockWaiting(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	backendPID int,
	lockType string,
	mode string,
) {
	t.Helper()
	var waiting bool
	if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM pg_catalog.pg_locks
  WHERE pid = $1
    AND locktype = $2
    AND mode = $3
    AND NOT granted
)`, backendPID, lockType, mode).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	if !waiting {
		t.Fatalf("PostgreSQL backend %d stopped waiting for %s/%s while a conflicting holder remained", backendPID, lockType, mode)
	}
}
