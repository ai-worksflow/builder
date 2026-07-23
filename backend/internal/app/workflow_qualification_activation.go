package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nats-io/nats.go"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/workflowqualificationactivation"
)

// WorkflowQualificationActivationServiceComposition exposes the exact
// production service graph. The resolver is deliberately backed by a
// different pool from the application-role atomic Store.
type WorkflowQualificationActivationServiceComposition struct {
	Resolver *workflowqualificationactivation.PostgresResolver
	Store    *workflowqualificationactivation.PostgresStore
	Service  *workflowqualificationactivation.Service
}

// NewWorkflowQualificationActivationService composes the server-side
// resolver and atomic activation Store without starting a transport worker.
// The distinct-pool check prevents accidentally granting resolver authority
// to the application transaction owner through one shared *sql.DB.
func NewWorkflowQualificationActivationService(
	applicationDatabase *sql.DB,
	operatorDatabase *sql.DB,
	storeConfig workflowqualificationactivation.PostgresStoreConfig,
) (WorkflowQualificationActivationServiceComposition, error) {
	if applicationDatabase == nil || operatorDatabase == nil || applicationDatabase == operatorDatabase {
		return WorkflowQualificationActivationServiceComposition{}, errors.New("workflow qualification activation requires distinct application and WIA operator PostgreSQL pools")
	}
	resolver, err := workflowqualificationactivation.NewPostgresResolver(operatorDatabase)
	if err != nil {
		return WorkflowQualificationActivationServiceComposition{}, fmt.Errorf("create workflow qualification activation resolver: %w", err)
	}
	store, err := workflowqualificationactivation.NewPostgresStore(applicationDatabase, storeConfig)
	if err != nil {
		return WorkflowQualificationActivationServiceComposition{}, fmt.Errorf("create workflow qualification activation atomic store: %w", err)
	}
	service, err := workflowqualificationactivation.NewService(resolver, store)
	if err != nil {
		return WorkflowQualificationActivationServiceComposition{}, fmt.Errorf("create workflow qualification activation service: %w", err)
	}
	return WorkflowQualificationActivationServiceComposition{
		Resolver: resolver,
		Store:    store,
		Service:  service,
	}, nil
}

// WorkflowQualificationActivationRuntime is the production durable-consumer
// composition. Messages can reach activation only through Service, whose
// Resolver accepts the opaque completion-event identity and reconstructs the
// immutable database-authored Candidate.
type WorkflowQualificationActivationRuntime struct {
	WorkflowQualificationActivationServiceComposition
	Worker   *workflowqualificationactivation.Worker
	consumer *workflowqualificationactivation.NATSConsumer

	applicationDatabase *sql.DB
	operatorDatabase    *sql.DB
	runMu               sync.RWMutex
	runStarted          bool
	runStopped          bool
	runErr              error
}

type WorkflowQualificationActivationRuntimeDependencies struct {
	ApplicationDatabase *sql.DB
	OperatorDatabase    *sql.DB
	JetStream           nats.JetStreamContext
}

// NewWorkflowQualificationActivationRuntime binds the exact production
// Resolver -> Service -> JetStream Worker path after verifying that the two
// PostgreSQL pools use distinct, direct, read-write-primary login sessions.
func NewWorkflowQualificationActivationRuntime(
	ctx context.Context,
	dependencies WorkflowQualificationActivationRuntimeDependencies,
	storeConfig workflowqualificationactivation.PostgresStoreConfig,
) (*WorkflowQualificationActivationRuntime, error) {
	if ctx == nil || dependencies.ApplicationDatabase == nil || dependencies.OperatorDatabase == nil ||
		dependencies.ApplicationDatabase == dependencies.OperatorDatabase || dependencies.JetStream == nil {
		return nil, errors.New("workflow qualification activation runtime dependencies are invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	composition, err := NewWorkflowQualificationActivationService(
		dependencies.ApplicationDatabase,
		dependencies.OperatorDatabase,
		storeConfig,
	)
	if err != nil {
		return nil, err
	}
	if err := assertWorkflowQualificationActivationPostgresPosture(
		ctx,
		dependencies.ApplicationDatabase,
		dependencies.OperatorDatabase,
		composition.Resolver,
	); err != nil {
		return nil, err
	}

	streamReadiness, err := workflowqualificationactivation.NewJetStreamReadiness(dependencies.JetStream)
	if err != nil {
		return nil, fmt.Errorf("create workflow qualification activation stream readiness: %w", err)
	}
	consumerConfig := workflowqualificationactivation.DefaultNATSConsumerConfig()
	consumer, err := workflowqualificationactivation.NewNATSConsumer(
		ctx,
		dependencies.JetStream,
		streamReadiness,
		consumerConfig,
	)
	if err != nil {
		return nil, fmt.Errorf("create workflow qualification activation durable consumer: %w", err)
	}
	quarantine, err := workflowqualificationactivation.NewNATSQuarantineSink(
		dependencies.JetStream,
		consumerConfig.AckConfirmWait,
	)
	if err != nil {
		_ = consumer.Close()
		return nil, fmt.Errorf("create workflow qualification activation quarantine sink: %w", err)
	}
	worker, err := workflowqualificationactivation.NewWorker(
		composition.Service,
		consumer,
		quarantine,
		workflowqualificationactivation.DefaultWorkerConfig(),
	)
	if err != nil {
		_ = consumer.Close()
		return nil, fmt.Errorf("create workflow qualification activation worker: %w", err)
	}
	return &WorkflowQualificationActivationRuntime{
		WorkflowQualificationActivationServiceComposition: composition,
		Worker:              worker,
		consumer:            consumer,
		applicationDatabase: dependencies.ApplicationDatabase,
		operatorDatabase:    dependencies.OperatorDatabase,
	}, nil
}

func (runtime *WorkflowQualificationActivationRuntime) Run(ctx context.Context) error {
	if runtime == nil || runtime.Worker == nil || ctx == nil {
		return errors.New("workflow qualification activation runtime and context are required")
	}
	runtime.runMu.Lock()
	if runtime.runStarted {
		runtime.runMu.Unlock()
		return errors.New("workflow qualification activation runtime can only be started once")
	}
	runtime.runStarted = true
	runtime.runMu.Unlock()

	err := runtime.Worker.Run(ctx)
	runtime.runMu.Lock()
	runtime.runStopped = true
	runtime.runErr = err
	runtime.runMu.Unlock()
	return err
}

// Readiness continuously verifies credential separation and the resolver's
// least-privilege capability. The random UUID must resolve as not found; any
// row or another error indicates resolver or database posture drift.
func (runtime *WorkflowQualificationActivationRuntime) Readiness(ctx context.Context) error {
	if runtime == nil || runtime.Resolver == nil || runtime.consumer == nil {
		return errors.New("workflow qualification activation runtime is unavailable")
	}
	runtime.runMu.RLock()
	stopped := runtime.runStopped
	runtimeErr := runtime.runErr
	runtime.runMu.RUnlock()
	if stopped {
		if runtimeErr != nil {
			return errors.New("workflow qualification activation worker stopped after a runtime failure")
		}
		return errors.New("workflow qualification activation worker stopped unexpectedly")
	}
	if err := runtime.consumer.Readiness(ctx); err != nil {
		return err
	}
	return assertWorkflowQualificationActivationPostgresPosture(
		ctx,
		runtime.applicationDatabase,
		runtime.operatorDatabase,
		runtime.Resolver,
	)
}

func assertWorkflowQualificationActivationPostgresPosture(
	ctx context.Context,
	applicationDatabase *sql.DB,
	operatorDatabase *sql.DB,
	resolver *workflowqualificationactivation.PostgresResolver,
) error {
	if ctx == nil || applicationDatabase == nil || operatorDatabase == nil || resolver == nil ||
		applicationDatabase == operatorDatabase {
		return errors.New("workflow qualification activation PostgreSQL posture dependencies are invalid")
	}
	application, err := inspectWorkflowQualificationActivationSession(ctx, applicationDatabase)
	if err != nil || !application.primary || !application.safeLogin {
		return errors.New("workflow qualification activation application database is not a direct read-write primary session")
	}
	operator, err := inspectWorkflowQualificationActivationSession(ctx, operatorDatabase)
	if err != nil || !operator.primary || !operator.safeLogin || operator.user == application.user {
		return errors.New("workflow qualification activation WIA operator database is not a distinct direct read-write primary session")
	}
	if application.systemIdentifier != operator.systemIdentifier ||
		application.database != operator.database || application.schema != operator.schema {
		return errors.New("workflow qualification activation PostgreSQL pools do not share one database authority")
	}
	if err := assertWorkflowQualificationActivationCapabilities(ctx, applicationDatabase, false); err != nil {
		return errors.New("workflow qualification activation application capability posture is not exact")
	}
	if err := assertWorkflowQualificationActivationCapabilities(ctx, operatorDatabase, true); err != nil {
		return errors.New("workflow qualification activation WIA operator capability posture is not exact")
	}
	var applicationProbeExists bool
	if err := applicationDatabase.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM inspect_workflow_v3_quality_completion_precommit_v1($1)
)`, uuid.New()).Scan(&applicationProbeExists); err != nil || applicationProbeExists {
		return errors.New("workflow qualification activation application caller posture is not ready")
	}

	probeID, err := workflowqualificationactivation.ParseCompletionEventID(uuid.NewString())
	if err != nil {
		return errors.New("create workflow qualification activation readiness probe identity")
	}
	if _, err := resolver.Resolve(ctx, probeID); !errors.Is(err, workflowqualificationactivation.ErrNotFound) {
		return errors.New("workflow qualification activation resolver capability is not ready")
	}
	return nil
}

type workflowQualificationActivationSession struct {
	user             string
	database         string
	schema           string
	systemIdentifier string
	primary          bool
	safeLogin        bool
}

func inspectWorkflowQualificationActivationSession(
	ctx context.Context,
	database *sql.DB,
) (workflowQualificationActivationSession, error) {
	var session workflowQualificationActivationSession
	var currentUser, roleSetting string
	err := database.QueryRowContext(ctx, `
SELECT session_user::text,current_user::text,
       pg_catalog.current_setting('role'),
	   pg_catalog.current_database()::text,
	   pg_catalog.current_schema()::text,
	   (pg_catalog.pg_control_system()).system_identifier::text,
       NOT pg_catalog.pg_is_in_recovery()
	     AND pg_catalog.current_setting('transaction_read_only')='off',
	   login.rolcanlogin AND login.rolinherit
	     AND NOT (
	       login.rolsuper OR login.rolbypassrls OR login.rolcreaterole
	       OR login.rolcreatedb OR login.rolreplication
	     )
	     AND NOT EXISTS (
	       SELECT 1 FROM pg_catalog.pg_database AS owned
	       WHERE owned.datdba=login.oid
	     )
FROM pg_catalog.pg_roles AS login
WHERE login.rolname=session_user::text
`).Scan(
		&session.user, &currentUser, &roleSetting,
		&session.database, &session.schema, &session.systemIdentifier,
		&session.primary, &session.safeLogin,
	)
	if err != nil {
		return workflowQualificationActivationSession{}, err
	}
	if session.user == "" || session.database == "" || session.schema == "" ||
		session.systemIdentifier == "" || currentUser != session.user || roleSetting != "none" {
		return workflowQualificationActivationSession{}, errors.New("PostgreSQL session uses an indirect or unidentified role")
	}
	return session, nil
}

var workflowQualificationActivationCapabilitySignatures = []string{
	"freeze_workflow_input_authority_v1(uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb)",
	"inspect_workflow_input_authority_operation_v1(uuid)",
	"resolve_workflow_input_authority_for_node_v1(uuid,uuid)",
	"resolve_workflow_v3_quality_completion_material_plan_v1(uuid,uuid,bytea)",
	"admit_workflow_v3_quality_completion_materials_v1(uuid,uuid,bytea,bytea,bytea,bytea,bytea,jsonb)",
	"precommit_workflow_v3_quality_completion_v1(uuid,uuid,uuid,uuid,uuid,uuid,uuid,bigint,uuid,text,integer,timestamptz,jsonb,uuid,bytea)",
	"inspect_workflow_v3_quality_completion_precommit_v1(uuid)",
	"freeze_workflow_input_authority_from_quality_precommit_v1(uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb)",
	"resolve_workflow_v3_quality_completion_candidate_v1(uuid)",
	"resolve_workflow_input_authority_v1(uuid)",
	"assert_current_workflow_input_authority_v1(uuid)",
}

func assertWorkflowQualificationActivationCapabilities(
	ctx context.Context,
	database *sql.DB,
	operator bool,
) error {
	applicationAllowed := []bool{
		false, true, true, true, true, true, true, true, false, false, false,
	}
	operatorAllowed := []bool{
		false, false, false, false, false, false, false, false, true, false, false,
	}
	expected := applicationAllowed
	if operator {
		expected = operatorAllowed
	}
	var exact bool
	err := database.QueryRowContext(ctx, `
WITH expected(signature,allowed) AS (
  SELECT *
  FROM ROWS FROM (
    pg_catalog.unnest($1::text[]),
    pg_catalog.unnest($2::boolean[])
  )
), checked AS (
  SELECT expected.signature,expected.allowed,
         pg_catalog.to_regprocedure(
           pg_catalog.current_schema()||'.'||expected.signature
         ) AS routine
  FROM expected
)
SELECT pg_catalog.count(*)=pg_catalog.cardinality($1::text[])
   AND pg_catalog.bool_and(
     routine IS NOT NULL
     AND pg_catalog.has_function_privilege(
       session_user,routine,'EXECUTE'
     ) IS NOT DISTINCT FROM allowed
   )
   AND NOT EXISTS (
     SELECT 1
     FROM pg_catalog.pg_class AS relation
     JOIN pg_catalog.pg_namespace AS namespace
       ON namespace.oid=relation.relnamespace
     WHERE namespace.nspname=pg_catalog.current_schema()
       AND relation.relkind='r'
       AND relation.relname LIKE 'workflow_v3_quality_completion_%'
       AND pg_catalog.has_table_privilege(
         session_user,relation.oid,
         'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER'
       )
   )
FROM checked
`, workflowQualificationActivationCapabilitySignatures, expected).Scan(&exact)
	if err != nil {
		return err
	}
	if !exact {
		return errors.New("workflow qualification activation capability matrix differs")
	}
	return nil
}

func openWorkflowInputAuthorityOperatorPool(
	ctx context.Context,
	configuration config.Config,
) (*sql.DB, error) {
	activation := configuration.WorkflowQualificationActivation
	if ctx == nil || strings.TrimSpace(activation.PostgresDSN) != activation.PostgresDSN ||
		activation.PostgresDSN == "" || activation.MaxTransactionRetries < 1 ||
		activation.MaxTransactionRetries > 10 || activation.MaxOpenConns < 9 ||
		activation.MaxIdleConns < 0 || activation.MaxIdleConns > activation.MaxOpenConns {
		return nil, errors.New("workflow input authority operator PostgreSQL configuration is invalid")
	}
	parsed, err := pgx.ParseConfig(activation.PostgresDSN)
	if err != nil {
		return nil, errors.New("workflow input authority operator PostgreSQL configuration is invalid")
	}
	if parsed.RuntimeParams == nil {
		parsed.RuntimeParams = map[string]string{}
	}
	if _, exists := parsed.RuntimeParams["role"]; exists {
		return nil, errors.New("workflow input authority operator PostgreSQL role override is forbidden")
	}
	parsed.RuntimeParams["search_path"] = configuration.Postgres.Schema
	database := stdlib.OpenDB(*parsed)
	database.SetMaxOpenConns(activation.MaxOpenConns)
	database.SetMaxIdleConns(activation.MaxIdleConns)
	database.SetConnMaxLifetime(configuration.Postgres.ConnMaxLifetime)
	database.SetConnMaxIdleTime(configuration.Postgres.ConnMaxIdleTime)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, errors.New("workflow input authority operator PostgreSQL readiness probe failed")
	}
	return database, nil
}

func newWorkflowQualificationActivationRuntimeFromConfig(
	ctx context.Context,
	configuration config.Config,
	applicationDatabase *sql.DB,
	jetStream nats.JetStreamContext,
) (*WorkflowQualificationActivationRuntime, *sql.DB, error) {
	connectCtx, cancelConnect := context.WithTimeout(ctx, configuration.Dependencies.ConnectTimeout)
	operatorDatabase, err := openWorkflowInputAuthorityOperatorPool(connectCtx, configuration)
	cancelConnect()
	if err != nil {
		return nil, nil, err
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, configuration.Startup.Timeout)
	runtime, err := NewWorkflowQualificationActivationRuntime(
		startupCtx,
		WorkflowQualificationActivationRuntimeDependencies{
			ApplicationDatabase: applicationDatabase,
			OperatorDatabase:    operatorDatabase,
			JetStream:           jetStream,
		},
		workflowqualificationactivation.PostgresStoreConfig{
			MaxTransactionRetries: configuration.WorkflowQualificationActivation.MaxTransactionRetries,
		},
	)
	cancelStartup()
	if err != nil {
		_ = operatorDatabase.Close()
		return nil, nil, err
	}
	return runtime, operatorDatabase, nil
}

var _ interface {
	Run(context.Context) error
	Readiness(context.Context) error
} = (*WorkflowQualificationActivationRuntime)(nil)
