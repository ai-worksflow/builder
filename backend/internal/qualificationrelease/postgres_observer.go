package qualificationrelease

import (
	"context"
	"database/sql"
	"errors"
)

const nextCandidateQuery = `
SELECT run.id::text,node.id::text
FROM workflow_node_runs AS node
JOIN workflow_runs AS run ON run.id=node.run_id
WHERE run.execution_profile_version=$1
  AND run.execution_profile_hash=$2
  AND run.status='running'
  AND run.completed_at IS NULL
  AND run.cancelled_at IS NULL
  AND run.failure IS NULL
  AND node.node_type='publish'
  AND (
    (node.status='ready' AND node.available_at<=statement_timestamp())
    OR
    (node.status='running' AND node.lease_expires_at<statement_timestamp())
  )
ORDER BY node.available_at,node.id
LIMIT 1
`

const observeControllerQuery = `
SELECT run.state,run.version,operation.remote_state
FROM release_deployment_runs AS run
JOIN release_delivery_operations AS operation
  ON operation.deployment_run_id=run.id
 AND operation.preview_run_id IS NULL
WHERE run.id=$1
  AND run.project_id=$2
  AND run.schema_version='release-deployment-run/v2'
  AND run.environment='production'
  AND run.operation='promote'
  AND operation.id=$3
  AND operation.kind='production'
  AND operation.project_id=$2
  AND operation.request_hash=$4
  AND operation.controller_schema_version=$5
  AND operation.controller_id=$6
  AND operation.controller_version=$7
  AND operation.controller_protocol=$8
  AND operation.controller_trust_key_digest=$9
`

// PostgresCandidateSource is a read-only scheduler projection on the Workflow
// application pool. Claim authority remains exclusively in migration 84.
type PostgresCandidateSource struct {
	database *sql.DB
}

func NewPostgresCandidateSource(database *sql.DB) (*PostgresCandidateSource, error) {
	if database == nil {
		return nil, ErrInvalid
	}
	return &PostgresCandidateSource{database: database}, nil
}

func (source *PostgresCandidateSource) Next(ctx context.Context) (Target, error) {
	if source == nil || source.database == nil || isNilInterface(ctx) {
		return Target{}, ErrInvalid
	}
	var workflowRunID, publishNodeRunID string
	err := source.database.QueryRowContext(ctx, nextCandidateQuery,
		WorkflowExecutionProfileVersion, WorkflowExecutionProfileHash,
	).Scan(&workflowRunID, &publishNodeRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return Target{}, ErrNotReady
	}
	if err != nil {
		return Target{}, ErrOutcomeUnknown
	}
	runID, err := parseUUID(workflowRunID)
	if err != nil {
		return Target{}, ErrConflict
	}
	nodeID, err := parseUUID(publishNodeRunID)
	if err != nil {
		return Target{}, ErrConflict
	}
	target := Target{WorkflowRunID: runID, PublishNodeRunID: nodeID}
	if err := target.Validate(); err != nil {
		return Target{}, ErrConflict
	}
	return target, nil
}

func (source *PostgresCandidateSource) Readiness(ctx context.Context) error {
	if source == nil || source.database == nil || isNilInterface(ctx) {
		return ErrInvalid
	}
	var primary, schemaReady bool
	err := source.database.QueryRowContext(ctx, `
SELECT NOT pg_catalog.pg_is_in_recovery()
         AND pg_catalog.current_setting('transaction_read_only')='off',
       to_regclass('workflow_runs') IS NOT NULL
         AND to_regclass('workflow_node_runs') IS NOT NULL
`).Scan(&primary, &schemaReady)
	if err != nil || !primary || !schemaReady {
		return wrap(ErrNotReady, "qualified release Workflow candidate source is unavailable")
	}
	return nil
}

type PostgresControllerObserver struct {
	database *sql.DB
}

func NewPostgresControllerObserver(database *sql.DB) (*PostgresControllerObserver, error) {
	if database == nil {
		return nil, ErrInvalid
	}
	return &PostgresControllerObserver{database: database}, nil
}

// Observe polls only the durable Run/Operation pair created by the immutable
// binding. It performs no remote Controller call and cannot submit or resubmit.
func (observer *PostgresControllerObserver) Observe(
	ctx context.Context,
	binding ControllerBinding,
) (ControllerOutcome, error) {
	if observer == nil || observer.database == nil || isNilInterface(ctx) ||
		binding.Controller.Validate() != nil || !validUUID(binding.ProductionRunID) ||
		!validUUID(binding.ControllerOperationID) || !validUUID(binding.ProjectID) ||
		!exactHashPattern.MatchString(binding.RequestHash) {
		return ControllerOutcome{}, ErrInvalid
	}
	var outcome ControllerOutcome
	err := observer.database.QueryRowContext(ctx, observeControllerQuery,
		binding.ProductionRunID, binding.ProjectID, binding.ControllerOperationID,
		binding.RequestHash, binding.Controller.SchemaVersion, binding.Controller.ID,
		binding.Controller.Version, binding.Controller.Protocol, binding.Controller.TrustKeyDigest,
	).Scan(&outcome.RunState, &outcome.RunVersion, &outcome.RemoteState)
	if errors.Is(err, sql.ErrNoRows) {
		return ControllerOutcome{}, ErrNotFound
	}
	if err != nil {
		return ControllerOutcome{}, ErrOutcomeUnknown
	}
	switch outcome.RunState {
	case "healthy":
		outcome.Kind = OutcomeHealthy
	case "failed", "error", "cancelled":
		outcome.Kind = OutcomeFailed
	default:
		outcome.Kind = OutcomeActive
	}
	if err := outcome.Validate(); err != nil || !validControllerStatePair(outcome) {
		return ControllerOutcome{}, ErrConflict
	}
	return outcome, nil
}

func validControllerStatePair(outcome ControllerOutcome) bool {
	switch outcome.RunState {
	case "healthy", "failed":
		return outcome.RemoteState == "completed"
	case "verifying":
		return outcome.RemoteState == "completed"
	case "error":
		return outcome.RemoteState == "rejected"
	case "cancelled":
		return outcome.RemoteState == "prepared"
	default:
		return oneOf(outcome.RemoteState, "prepared", "submit_unknown", "accepted", "running")
	}
}

func (observer *PostgresControllerObserver) Readiness(ctx context.Context) error {
	if observer == nil || observer.database == nil || isNilInterface(ctx) {
		return ErrInvalid
	}
	var schemaReady bool
	err := observer.database.QueryRowContext(ctx, `
SELECT to_regclass('release_deployment_runs') IS NOT NULL
   AND to_regclass('release_delivery_operations') IS NOT NULL
`).Scan(&schemaReady)
	if err != nil || !schemaReady {
		return wrap(ErrNotReady, "qualified release Controller observer is unavailable")
	}
	return nil
}

var _ interface {
	Next(context.Context) (Target, error)
	Readiness(context.Context) error
} = (*PostgresCandidateSource)(nil)

var _ interface {
	Observe(context.Context, ControllerBinding) (ControllerOutcome, error)
	Readiness(context.Context) error
} = (*PostgresControllerObserver)(nil)
