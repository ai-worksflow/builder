package repositorygc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostgresAuthorityUsesExactOneShotFunctionContract(t *testing.T) {
	runID := uuid.New()
	capabilityID := uuid.New()
	receiptID := uuid.New()
	projectID := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	connector := &contractConnector{responses: []contractResponse{
		{
			contains: "WITH\nreachable_roles",
			columns: []string{
				"rolname", "session_user", "rolsuper", "rolbypassrls", "rolcreaterole", "rolcreatedb", "rolreplication",
				"reachable_elevated", "reachable_admin_option", "owns_database", "can_create_database", "nspname", "can_create",
				"operator_member", "migration_owner_member", "application_member",
				"stable_group_roles_safe",
				"table_count", "owns_table", "can_destroy",
				"function_count", "executable_function_count", "secure_function_contract_count", "forbidden_security_definer_count",
				"reachable_schema_object_owner_count", "privileged_relation_count", "privileged_sequence_count",
				"executable_non_gc_function_count", "grantable_gc_function_count",
				"related_objects_exactly_owned", "internal_function_acl_exact",
				"sandbox_checkpoint_dependency_exact",
			},
			values: [][]driver.Value{{
				"gc_login", "gc_login", false, false, false, false, false, false, false, false, false,
				"public", false, true, false, false, true, int64(10), false, false, int64(4), int64(4), int64(4),
				int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), true, true, true,
			}},
		},
		{
			contains: "repository_exact_tree_literal_index_gc_readiness()",
			columns: []string{
				"ready", "reason", "trusted_schema", "operator_role_exists", "application_role_exists",
				"operator_execute_granted", "application_claim_execute_granted", "public_claim_execute_revoked",
				"public_schema_create_revoked", "migration_owner_role_exists", "objects_owned_by_migration_owner",
				"stable_group_roles_safe", "application_schema_head_read_granted",
			},
			values: [][]driver.Value{{true, "ready", "public", true, true, true, true, true, true, true, true, true, true}},
		},
		{
			contains: "plan_repository_exact_tree_literal_index_gc($1, $2, $3, $4, $5)",
			columns:  []string{"run_id", "capability_id", "project_id", "tree_hash", "publication_created_at", "index_commitment", "planned_rank", "expires_at"},
			values: [][]driver.Value{{
				runID.String(), capabilityID.String(), projectID.String(), testDigest("1"), now.Add(-31 * 24 * time.Hour),
				testDigest("2"), int64(9), now.Add(10 * time.Minute),
			}},
		},
		{
			contains: "execute_repository_exact_tree_literal_index_gc($1)",
			columns: []string{
				"receipt_id", "capability_id", "run_id", "project_id", "tree_hash", "publication_created_at",
				"index_commitment", "deleted_member_count", "deleted_blob_count", "outcome",
				"logical_bytes_released", "blob_bytes_freed", "executed_at", "idempotent",
			},
			values: [][]driver.Value{{
				receiptID.String(), capabilityID.String(), runID.String(), projectID.String(), testDigest("1"),
				now.Add(-31 * 24 * time.Hour), testDigest("2"), int64(2), int64(1), "deleted", int64(128), int64(64), now, false,
			}},
		},
		{
			contains: "inspect_repository_exact_tree_literal_index_gc_run($1)",
			columns: []string{
				"run_id", "run_status", "planned_at", "cutoff_at", "keep_per_project", "batch_size",
				"capability_ttl_milliseconds", "planned_capability_count", "deleted_capability_count",
				"pending_capability_count", "protected_capability_count", "stale_capability_count",
				"expired_capability_count", "logical_bytes_released", "blob_bytes_freed",
			},
			values: [][]driver.Value{{
				runID.String(), "completed", now, now.Add(-30 * 24 * time.Hour), int64(8), int64(25),
				int64((10 * time.Minute).Milliseconds()), int64(1), int64(1), int64(0), int64(0), int64(0), int64(0), int64(128), int64(64),
			}},
		},
	}}
	database := sql.OpenDB(connector)
	defer database.Close()
	authority, err := NewPostgresAuthority(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := authority.Readiness(ctx); err != nil {
		t.Fatalf("Readiness() error = %v", err)
	}
	capabilities, err := authority.Plan(ctx, PlanInput{
		RunID: runID, Retention: 30 * 24 * time.Hour, KeepPerProject: 8,
		BatchSize: 25, CapabilityTTL: 10 * time.Minute,
	})
	if err != nil || len(capabilities) != 1 || capabilities[0].CapabilityID != capabilityID {
		t.Fatalf("Plan() = %#v, %v", capabilities, err)
	}
	receipt, err := authority.Execute(ctx, capabilityID)
	if err != nil || receipt.ReceiptID != receiptID || receipt.Outcome != OutcomeDeleted {
		t.Fatalf("Execute() = %#v, %v", receipt, err)
	}
	inspection, err := authority.Inspect(ctx, runID)
	if err != nil || inspection.State != RunStateCompleted || inspection.Result.LogicalBytesReleased != 128 {
		t.Fatalf("Inspect() = %#v, %v", inspection, err)
	}
	for _, query := range connector.queries() {
		upper := strings.ToUpper(query)
		for _, forbidden := range []string{"DELETE FROM", "TRUNCATE TABLE", "SET ROLE", "UPDATE ", "INSERT INTO"} {
			if strings.Contains(upper, forbidden) {
				t.Fatalf("Go authority performed direct mutation %q in query %q", forbidden, query)
			}
		}
	}
}

func testDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

type contractConnector struct {
	mu        sync.Mutex
	responses []contractResponse
	observed  []string
}

type contractResponse struct {
	contains string
	columns  []string
	values   [][]driver.Value
}

func (connector *contractConnector) Connect(context.Context) (driver.Conn, error) {
	return &contractConnection{connector: connector}, nil
}

func (*contractConnector) Driver() driver.Driver { return contractDriver{} }

func (connector *contractConnector) queries() []string {
	connector.mu.Lock()
	defer connector.mu.Unlock()
	return append([]string(nil), connector.observed...)
}

type contractDriver struct{}

func (contractDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("contract test driver requires connector")
}

type contractConnection struct {
	connector *contractConnector
}

func (*contractConnection) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepared statements are not supported")
}
func (*contractConnection) Close() error { return nil }
func (*contractConnection) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("transactions are not supported")
}
func (*contractConnection) Ping(context.Context) error { return nil }

func (connection *contractConnection) QueryContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	connection.connector.mu.Lock()
	defer connection.connector.mu.Unlock()
	connection.connector.observed = append(connection.connector.observed, query)
	if len(connection.connector.responses) == 0 {
		return nil, fmt.Errorf("unexpected query %q", query)
	}
	response := connection.connector.responses[0]
	connection.connector.responses = connection.connector.responses[1:]
	if !strings.Contains(query, response.contains) {
		return nil, fmt.Errorf("query %q does not contain %q", query, response.contains)
	}
	return &contractRows{columns: response.columns, values: response.values}, nil
}

type contractRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (rows *contractRows) Columns() []string { return rows.columns }
func (rows *contractRows) Close() error      { return nil }
func (rows *contractRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}
