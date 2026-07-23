package app

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/workflowqualificationactivation"
)

func TestWorkflowQualificationActivationServiceUsesDedicatedResolverPool(t *testing.T) {
	applicationConnector := &recordingResolverConnector{}
	operatorConnector := &recordingResolverConnector{}
	applicationDatabase := sql.OpenDB(applicationConnector)
	operatorDatabase := sql.OpenDB(operatorConnector)
	t.Cleanup(func() {
		_ = applicationDatabase.Close()
		_ = operatorDatabase.Close()
	})

	composition, err := NewWorkflowQualificationActivationService(
		applicationDatabase,
		operatorDatabase,
		workflowqualificationactivation.PostgresStoreConfig{MaxTransactionRetries: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	if composition.Resolver == nil || composition.Store == nil || composition.Service == nil {
		t.Fatalf("incomplete activation composition: %#v", composition)
	}

	completionEventID, err := workflowqualificationactivation.ParseCompletionEventID(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := composition.Service.Activate(context.Background(), completionEventID); !errors.Is(err, workflowqualificationactivation.ErrNotFound) {
		t.Fatalf("Activate() error = %v, want resolver-owned not found", err)
	}
	if got := operatorConnector.Queries(); len(got) != 1 ||
		!strings.Contains(got[0], "resolve_workflow_v3_quality_completion_candidate_v1") {
		t.Fatalf("operator resolver queries = %#v", got)
	}
	if got := applicationConnector.Queries(); len(got) != 0 {
		t.Fatalf("application Store was reached before resolver authority: %#v", got)
	}
}

func TestWorkflowQualificationActivationServiceRejectsSharedOrInvalidPools(t *testing.T) {
	database := sql.OpenDB(&recordingResolverConnector{})
	t.Cleanup(func() { _ = database.Close() })

	for name, pools := range map[string]struct {
		application *sql.DB
		operator    *sql.DB
	}{
		"missing application": {operator: database},
		"missing operator":    {application: database},
		"shared pool":         {application: database, operator: database},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewWorkflowQualificationActivationService(
				pools.application,
				pools.operator,
				workflowqualificationactivation.PostgresStoreConfig{},
			); err == nil {
				t.Fatal("unsafe activation composition was accepted")
			}
		})
	}

	other := sql.OpenDB(&recordingResolverConnector{})
	t.Cleanup(func() { _ = other.Close() })
	if _, err := NewWorkflowQualificationActivationService(
		database,
		other,
		workflowqualificationactivation.PostgresStoreConfig{MaxTransactionRetries: 11},
	); err == nil {
		t.Fatal("invalid atomic Store retry bound was accepted")
	}
}

func TestWorkflowQualificationActivationRuntimeRecordsTerminalWorkerFailure(t *testing.T) {
	consumer := &immediateFailureActivationConsumer{err: errors.New("transport unavailable")}
	worker, err := workflowqualificationactivation.NewWorker(
		activationServiceUnused{},
		consumer,
		activationQuarantineUnused{},
		workflowqualificationactivation.DefaultWorkerConfig(),
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &WorkflowQualificationActivationRuntime{Worker: worker}
	if err := runtime.Run(context.Background()); err == nil {
		t.Fatal("terminal worker transport failure was hidden")
	}
	runtime.runMu.RLock()
	started, stopped, terminalErr := runtime.runStarted, runtime.runStopped, runtime.runErr
	runtime.runMu.RUnlock()
	if !started || !stopped || terminalErr == nil || consumer.closeCalls != 1 {
		t.Fatalf("runtime terminal state = started:%t stopped:%t err:%v closes:%d", started, stopped, terminalErr, consumer.closeCalls)
	}
	if err := runtime.Run(context.Background()); err == nil {
		t.Fatal("terminal activation runtime was restarted with its closed consumer")
	}
}

func TestWorkflowQualificationActivationPostureQueriesPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := inspectWorkflowQualificationActivationSession(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if session.user == "" || session.database == "" || session.schema == "" ||
		session.systemIdentifier == "" || !session.primary {
		t.Fatalf("incomplete PostgreSQL session identity: %#v", session)
	}
	// The canary DSN owns its disposable test database and must therefore be
	// rejected as a runtime login. The exact matrix error proves that the
	// array ABI and every catalog expression were nevertheless executable.
	if session.safeLogin {
		t.Fatal("database-owner canary was accepted as a runtime login")
	}
	if err := assertWorkflowQualificationActivationCapabilities(ctx, database, false); err == nil ||
		err.Error() != "workflow qualification activation capability matrix differs" {
		t.Fatalf("application capability matrix canary error = %v", err)
	}
}

type immediateFailureActivationConsumer struct {
	err        error
	closeCalls int
}

func (consumer *immediateFailureActivationConsumer) Fetch(context.Context, int) ([]workflowqualificationactivation.Delivery, error) {
	return nil, consumer.err
}

func (*immediateFailureActivationConsumer) Limits() workflowqualificationactivation.ConsumerLimits {
	return workflowqualificationactivation.ConsumerLimits{
		MaxDeliver: -1, MaxAckPending: 8, AckWait: 2 * time.Minute,
		AckConfirmWait: 5 * time.Second, MaxRequestBatch: 8,
		MaxRequestMaxBytes: 1 << 20,
	}
}

func (consumer *immediateFailureActivationConsumer) Close() error {
	consumer.closeCalls++
	return nil
}

type activationServiceUnused struct{}

func (activationServiceUnused) Activate(context.Context, workflowqualificationactivation.CompletionEventID) (workflowqualificationactivation.Record, error) {
	return workflowqualificationactivation.Record{}, errors.New("unused")
}

func (activationServiceUnused) Inspect(context.Context, workflowqualificationactivation.CompletionEventID) (workflowqualificationactivation.Record, error) {
	return workflowqualificationactivation.Record{}, errors.New("unused")
}

type activationQuarantineUnused struct{}

func (activationQuarantineUnused) Quarantine(context.Context, workflowqualificationactivation.QuarantineRecord) error {
	return errors.New("unused")
}

type recordingResolverConnector struct {
	mu      sync.Mutex
	queries []string
}

func (connector *recordingResolverConnector) Connect(context.Context) (driver.Conn, error) {
	return &recordingResolverConnection{connector: connector}, nil
}

func (connector *recordingResolverConnector) Driver() driver.Driver {
	return recordingResolverDriver{}
}

func (connector *recordingResolverConnector) Queries() []string {
	connector.mu.Lock()
	defer connector.mu.Unlock()
	return append([]string(nil), connector.queries...)
}

type recordingResolverDriver struct{}

func (recordingResolverDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("recording resolver driver requires its connector")
}

type recordingResolverConnection struct {
	connector *recordingResolverConnector
}

func (connection *recordingResolverConnection) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (connection *recordingResolverConnection) Close() error {
	return nil
}

func (connection *recordingResolverConnection) Begin() (driver.Tx, error) {
	return nil, driver.ErrSkip
}

func (connection *recordingResolverConnection) QueryContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	connection.connector.mu.Lock()
	connection.connector.queries = append(connection.connector.queries, query)
	connection.connector.mu.Unlock()
	return emptyResolverRows{}, nil
}

type emptyResolverRows struct{}

func (emptyResolverRows) Columns() []string {
	return []string{
		"classification", "completion_event_id", "precommit_id", "freeze_request_hash",
		"freeze_request_bytes", "workflow_input_hash", "workflow_input_bytes",
		"freeze_candidate_bytes", "definition_raw_bytes", "run_scope_raw_bytes",
		"node_input_raw_bytes", "build_manifest_raw_bytes", "build_contract_raw_bytes",
		"material_bundle", "snapshot_hash", "retained_raw_bytes_size",
	}
}

func (emptyResolverRows) Close() error {
	return nil
}

func (emptyResolverRows) Next([]driver.Value) error {
	return io.EOF
}

var _ driver.Connector = (*recordingResolverConnector)(nil)
var _ driver.QueryerContext = (*recordingResolverConnection)(nil)
