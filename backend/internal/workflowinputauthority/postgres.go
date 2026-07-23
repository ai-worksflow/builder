package workflowinputauthority

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrCorrupt = errors.New("workflow input authority durable aggregate is corrupt")

const maximumPostgresRecoveryBundleBytes = (4 * MaximumRetainedBytes) + (3 * MaximumInputBytes)

const workflowInputAuthorityMigrationAdvisoryKey = "worksflow:workflow-input-authority-migration:v1"

// PostgresTransaction is an opaque admission token for an already-open
// caller-owned transaction. It deliberately exposes no commit or rollback
// method: the Workflow activation boundary remains the transaction owner.
type PostgresTransaction struct {
	transaction *sql.Tx
}

func (*PostgresTransaction) workflowInputAuthorityTransaction() {}

// NewPostgresTransaction wraps an existing transaction without beginning,
// committing, or rolling it back.
func NewPostgresTransaction(transaction *sql.Tx) (*PostgresTransaction, error) {
	if transaction == nil {
		return nil, invalid("store.transaction", "PostgreSQL transaction is required")
	}
	return &PostgresTransaction{transaction: transaction}, nil
}

// PostgresStore uses migration 000078's capability functions. Freeze joins a
// caller-owned Workflow transaction; historical reads use the supplied pool.
type PostgresStore struct {
	database *sql.DB
}

func NewPostgresStore(database *sql.DB) (*PostgresStore, error) {
	if database == nil {
		return nil, invalid("store.database", "PostgreSQL database is required")
	}
	return &PostgresStore{database: database}, nil
}

func (store *PostgresStore) Freeze(ctx context.Context, transaction Transaction, candidate Candidate) (Record, error) {
	if store == nil || store.database == nil || ctx == nil {
		return Record{}, invalid("store", "PostgreSQL store and context are required")
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	postgresTransaction, err := requirePostgresTransaction(transaction)
	if err != nil {
		return Record{}, err
	}

	proposed, err := Compile(candidate)
	if err != nil {
		return Record{}, err
	}
	document, err := candidateDocumentFromRecord(proposed.Input, proposed.Materials)
	if err != nil {
		return Record{}, err
	}
	privateCandidate, err := EncodeFreezeCandidate(document)
	if err != nil {
		return Record{}, err
	}
	// This must be the first database statement in the caller-owned
	// transaction. The preflight inspection reads the WIA relation, so taking
	// the shared migration fence afterward can invert the migration's
	// advisory/project/WIA lock order. Callers must likewise avoid touching
	// workflow relations before entering Freeze unless their transaction
	// entrypoint already holds this same shared fence.
	if err := store.AcquireMigrationFence(ctx, postgresTransaction); err != nil {
		return Record{}, err
	}

	// Preflight gives an early immutable conflict diagnostic. The issuer-side
	// transaction discriminator below is authoritative for the concurrent case
	// where another exact freeze commits after this inspection.
	wasExisting := false
	existing, inspectErr := scanPostgresWorkflowInputBundle(postgresTransaction.transaction.QueryRowContext(
		ctx, postgresWorkflowInputInspectOperationQuery, proposed.OperationID,
	))
	if inspectErr == nil {
		if !sameImmutableRecord(existing, proposed) {
			return Record{}, fmt.Errorf("%w: operation is already bound to different immutable bytes", ErrConflict)
		}
		wasExisting = true
	} else if !errors.Is(inspectErr, ErrNotFound) {
		return Record{}, classifyPostgresWorkflowInputError("inspect operation before freeze", inspectErr, false)
	}

	var issuedAuthorityID string
	var insertedByCurrentTransaction bool
	err = postgresTransaction.transaction.QueryRowContext(ctx, postgresWorkflowInputFreezeQuery,
		proposed.OperationID,
		proposed.AuthorityID,
		proposed.WorkflowRunID,
		proposed.NodeRunID,
		proposed.Request.ExpectedRunCursor,
		proposed.Input.Gate.ActivationEventID,
		proposed.Input.Gate.ActivationEventSequence,
		proposed.Materials.Definition,
		proposed.Materials.RunScope,
		proposed.Materials.NodeInput,
		proposed.Materials.BuildManifest,
		proposed.Materials.BuildContract,
		string(privateCandidate),
	).Scan(&issuedAuthorityID, &insertedByCurrentTransaction)
	if err != nil {
		return Record{}, classifyPostgresWorkflowInputError("freeze", err, false)
	}
	if issuedAuthorityID != proposed.AuthorityID.String() {
		return Record{}, corruptPostgresWorkflowInput("freeze returned authority %q, expected %s", issuedAuthorityID, proposed.AuthorityID)
	}

	// Re-read through the least-privilege node capability in the same
	// transaction. This verifies the complete durable closure, including child
	// rows and raw bytes, before the caller is allowed to commit activation.
	recovered, err := scanPostgresWorkflowInputBundle(postgresTransaction.transaction.QueryRowContext(
		ctx, postgresWorkflowInputResolveNodeQuery, proposed.WorkflowRunID, proposed.NodeRunID,
	))
	if err != nil {
		return Record{}, classifyPostgresWorkflowInputError("recover frozen node", err, false)
	}
	if !sameImmutableRecord(recovered, proposed) {
		return Record{}, corruptPostgresWorkflowInput("database-authored immutable bytes differ from the compiled candidate")
	}
	recovered.Idempotent = wasExisting || !insertedByCurrentTransaction
	return cloneRecord(recovered), nil
}

func (store *PostgresStore) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	if !validPostgresLookup(store, ctx, operationID) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	record, err := scanPostgresWorkflowInputBundle(store.database.QueryRowContext(
		ctx, postgresWorkflowInputInspectOperationQuery, operationID,
	))
	if err != nil {
		return Record{}, classifyPostgresWorkflowInputError("inspect operation", err, false)
	}
	return cloneRecord(record), nil
}

func (store *PostgresStore) ResolveAuthority(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if !validPostgresLookup(store, ctx, authorityID) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	record, err := scanPostgresWorkflowInputBundle(store.database.QueryRowContext(
		ctx, postgresWorkflowInputResolveAuthorityQuery, authorityID,
	))
	if err != nil {
		return Record{}, classifyPostgresWorkflowInputError("resolve authority", err, false)
	}
	return cloneRecord(record), nil
}

func (store *PostgresStore) ResolveNode(ctx context.Context, workflowRunID, nodeRunID uuid.UUID) (Record, error) {
	if !validPostgresLookup(store, ctx, workflowRunID) || nodeRunID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	record, err := scanPostgresWorkflowInputBundle(store.database.QueryRowContext(
		ctx, postgresWorkflowInputResolveNodeQuery, workflowRunID, nodeRunID,
	))
	if err != nil {
		return Record{}, classifyPostgresWorkflowInputError("resolve node", err, false)
	}
	return cloneRecord(record), nil
}

// AssertCurrent is a read-side diagnostic. It must never authorize Promotion
// consumption because its implicit statement transaction releases row locks
// before a subsequent consume can run; use AssertCurrentTx for that boundary.
func (store *PostgresStore) AssertCurrent(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if !validPostgresLookup(store, ctx, authorityID) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	record, err := scanPostgresWorkflowInputBundle(store.database.QueryRowContext(
		ctx, postgresWorkflowInputAssertCurrentQuery, authorityID,
	))
	if err != nil {
		return Record{}, classifyPostgresWorkflowInputError("assert current authority", err, true)
	}
	return cloneRecord(record), nil
}

// AssertCurrentTx is the consumption-authority path. Locks acquired by the
// database assertion remain held by the caller-owned transaction until the
// caller atomically consumes the authority and commits. AssertCurrent above is
// intentionally diagnostic only because its implicit statement transaction
// releases all assertion locks when the query returns.
func (store *PostgresStore) AssertCurrentTx(
	ctx context.Context,
	transaction Transaction,
	authorityID uuid.UUID,
) (Record, error) {
	if store == nil || store.database == nil || ctx == nil || authorityID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	postgresTransaction, err := requirePostgresTransaction(transaction)
	if err != nil {
		return Record{}, err
	}
	if err := store.AcquireMigrationFence(ctx, postgresTransaction); err != nil {
		return Record{}, err
	}
	record, err := scanPostgresWorkflowInputBundle(postgresTransaction.transaction.QueryRowContext(
		ctx, postgresWorkflowInputAssertCurrentQuery, authorityID,
	))
	if err != nil {
		return Record{}, classifyPostgresWorkflowInputError("assert current authority in caller transaction", err, true)
	}
	return cloneRecord(record), nil
}

// AcquireMigrationFence joins the shared runtime side of migrations 78/79.
// A Promotion or rollback-safe caller must invoke this as the first database
// statement in its caller-owned transaction, before locking project, run,
// node, Workflow Input, Plan, Evidence, Receipt, or Promotion relations.
// Freeze and AssertCurrentTx call it defensively, but they cannot detect a
// caller that already touched a relation and inverted the deployment lock
// order.
func (store *PostgresStore) AcquireMigrationFence(ctx context.Context, transaction Transaction) error {
	if store == nil || store.database == nil || ctx == nil {
		return invalid("store", "PostgreSQL store and context are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	postgresTransaction, err := requirePostgresTransaction(transaction)
	if err != nil {
		return err
	}
	var held bool
	if err := postgresTransaction.transaction.QueryRowContext(
		ctx, postgresWorkflowInputMigrationFenceQuery, workflowInputAuthorityMigrationAdvisoryKey,
	).Scan(&held); err != nil {
		return classifyPostgresWorkflowInputError("acquire rolling migration fence", err, false)
	}
	if !held {
		return corruptPostgresWorkflowInput("rolling migration fence did not report acquisition")
	}
	return nil
}

func requirePostgresTransaction(transaction Transaction) (*PostgresTransaction, error) {
	postgresTransaction, ok := transaction.(*PostgresTransaction)
	if !ok || postgresTransaction == nil || postgresTransaction.transaction == nil {
		return nil, invalid("store.transaction", "an existing PostgreSQL transaction is required")
	}
	return postgresTransaction, nil
}

func validPostgresLookup(store *PostgresStore, ctx context.Context, identity uuid.UUID) bool {
	return store != nil && store.database != nil && ctx != nil && identity.Version() == 4
}

const postgresWorkflowInputFreezeQuery = `
SELECT authority_id::text, creation_transaction_id = txid_current()
FROM freeze_workflow_input_authority_from_quality_precommit_v1(
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb
)`

const postgresWorkflowInputMigrationFenceQuery = `
SELECT true
FROM (
  SELECT pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended(CAST($1 AS text), 0)
  )
) AS workflow_input_migration_fence`

const postgresWorkflowInputInspectOperationQuery = `
SELECT value
FROM inspect_workflow_input_authority_operation_v1($1) AS value`

const postgresWorkflowInputResolveAuthorityQuery = `
SELECT value
FROM resolve_workflow_input_authority_v1($1) AS value`

const postgresWorkflowInputResolveNodeQuery = `
SELECT value
FROM resolve_workflow_input_authority_for_node_v1($1,$2) AS value`

const postgresWorkflowInputAssertCurrentQuery = `
SELECT value
FROM assert_current_workflow_input_authority_v1($1) AS value`

type postgresWorkflowInputRow interface {
	Scan(...any) error
}

func classifyPostgresWorkflowInputError(operation string, err error, currentAssertion bool) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if errors.Is(err, ErrCorrupt) {
		return err
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return fmt.Errorf("%s PostgreSQL Workflow Input Authority: %w", operation, err)
	}
	switch postgresError.Code {
	case "WIA01", "40001", "40P01", "23503", "23505":
		return errors.Join(ErrConflict, fmt.Errorf("%s PostgreSQL Workflow Input Authority: %w", operation, err))
	case "WIA02":
		classified := error(ErrCorrupt)
		if currentAssertion {
			classified = errors.Join(ErrStale, ErrCorrupt)
		} else {
			classified = errors.Join(ErrConflict, ErrCorrupt)
		}
		return errors.Join(classified, fmt.Errorf("%s PostgreSQL Workflow Input Authority: %w", operation, err))
	case "WIA03", "23502", "23514", "22001", "22003", "22007", "22021", "22023", "22P02":
		return errors.Join(ErrInvalid, fmt.Errorf("%s PostgreSQL Workflow Input Authority: %w", operation, err))
	case "WIA04":
		return errors.Join(ErrConflict, ErrStale, fmt.Errorf("%s PostgreSQL Workflow Input Authority: %w", operation, err))
	default:
		return fmt.Errorf("%s PostgreSQL Workflow Input Authority: %w", operation, err)
	}
}

func corruptPostgresWorkflowInput(format string, arguments ...any) error {
	detail := fmt.Sprintf(format, arguments...)
	return errors.Join(ErrConflict, ErrCorrupt, fmt.Errorf("PostgreSQL Workflow Input Authority: %s", detail))
}

type postgresRecoveryBundleWire struct {
	Authority            json.RawMessage `json:"authority"`
	IdentityReservations json.RawMessage `json:"identityReservations"`
	InputManifests       json.RawMessage `json:"inputManifests"`
	Predecessors         json.RawMessage `json:"predecessors"`
	ReviewReceipts       json.RawMessage `json:"reviewReceipts"`
	Revisions            json.RawMessage `json:"revisions"`
}

type postgresAuthorityWire struct {
	ActivationEventID                   string          `json:"activation_event_id"`
	ActivationEventSequence             int64           `json:"activation_event_sequence"`
	AuthorityBytes                      string          `json:"authority_bytes"`
	AuthorityDocument                   json.RawMessage `json:"authority_document"`
	AuthorityHash                       string          `json:"authority_hash"`
	AuthorityID                         string          `json:"authority_id"`
	BuildContractContentHash            string          `json:"build_contract_content_hash"`
	BuildContractHash                   string          `json:"build_contract_hash"`
	BuildContractID                     string          `json:"build_contract_id"`
	BuildContractRawBytes               string          `json:"build_contract_raw_bytes"`
	BuildContractRawBytesHash           string          `json:"build_contract_raw_bytes_hash"`
	BuildContractRawBytesSize           int64           `json:"build_contract_raw_bytes_size"`
	BuildContractStatus                 string          `json:"build_contract_status"`
	BuildManifestContentHash            string          `json:"build_manifest_content_hash"`
	BuildManifestHash                   string          `json:"build_manifest_hash"`
	BuildManifestID                     string          `json:"build_manifest_id"`
	BuildManifestRawBytes               string          `json:"build_manifest_raw_bytes"`
	BuildManifestRawBytesHash           string          `json:"build_manifest_raw_bytes_hash"`
	BuildManifestRawBytesSize           int64           `json:"build_manifest_raw_bytes_size"`
	BuildManifestStatus                 string          `json:"build_manifest_status"`
	DefinitionHash                      string          `json:"definition_hash"`
	DefinitionID                        string          `json:"definition_id"`
	DefinitionNodeID                    string          `json:"definition_node_id"`
	DefinitionRawBytes                  string          `json:"definition_raw_bytes"`
	DefinitionRawBytesHash              string          `json:"definition_raw_bytes_hash"`
	DefinitionRawBytesSize              int64           `json:"definition_raw_bytes_size"`
	DefinitionVersion                   int64           `json:"definition_version"`
	DefinitionVersionID                 string          `json:"definition_version_id"`
	ExecutionProfileHash                string          `json:"execution_profile_hash"`
	ExecutionProfileVersion             string          `json:"execution_profile_version"`
	ExternalGatePolicy                  string          `json:"external_gate_policy"`
	FrozenAt                            time.Time       `json:"frozen_at"`
	GateName                            string          `json:"gate_name"`
	GovernanceMode                      string          `json:"governance_mode"`
	InputBytes                          string          `json:"input_bytes"`
	InputDocument                       json.RawMessage `json:"input_document"`
	InputHash                           string          `json:"input_hash"`
	ManifestCount                       int64           `json:"manifest_count"`
	ManifestSubject                     string          `json:"manifest_subject"`
	NodeInputBindingCount               int64           `json:"node_input_binding_count"`
	NodeInputRawBytes                   string          `json:"node_input_raw_bytes"`
	NodeInputRawBytesHash               string          `json:"node_input_raw_bytes_hash"`
	NodeInputRawBytesSize               int64           `json:"node_input_raw_bytes_size"`
	NodeInputSemanticHash               string          `json:"node_input_semantic_hash"`
	NodeKey                             string          `json:"node_key"`
	NodeRunID                           string          `json:"node_run_id"`
	NodeType                            string          `json:"node_type"`
	OperationID                         string          `json:"operation_id"`
	PredecessorCount                    int64           `json:"predecessor_count"`
	ProjectID                           string          `json:"project_id"`
	QualificationPolicyAuthorityHash    string          `json:"qualification_policy_authority_hash"`
	QualificationPolicyAuthorityID      string          `json:"qualification_policy_authority_id"`
	QualityPassed                       bool            `json:"quality_passed"`
	QualityRunID                        string          `json:"quality_run_id"`
	QualityWorkspaceRevisionContentHash string          `json:"quality_workspace_revision_content_hash"`
	QualityWorkspaceRevisionID          string          `json:"quality_workspace_revision_id"`
	RequestBytes                        string          `json:"request_bytes"`
	RequestDocument                     json.RawMessage `json:"request_document"`
	RequestHash                         string          `json:"request_hash"`
	RevisionCount                       int64           `json:"revision_count"`
	ReviewReceiptCount                  int64           `json:"review_receipt_count"`
	RunInputManifestHash                string          `json:"run_input_manifest_hash"`
	RunInputManifestID                  string          `json:"run_input_manifest_id"`
	RunScopeRawBytes                    string          `json:"run_scope_raw_bytes"`
	RunScopeRawBytesHash                string          `json:"run_scope_raw_bytes_hash"`
	RunScopeRawBytesSize                int64           `json:"run_scope_raw_bytes_size"`
	RunStartedAt                        time.Time       `json:"run_started_at"`
	RunStartedBy                        string          `json:"run_started_by"`
	SliceID                             *string         `json:"slice_id"`
	SliceKind                           string          `json:"slice_kind"`
	StageGate                           string          `json:"stage_gate"`
	TargetArtifactID                    string          `json:"target_artifact_id"`
	TargetBytes                         string          `json:"target_bytes"`
	TargetDocument                      json.RawMessage `json:"target_document"`
	TargetHash                          string          `json:"target_hash"`
	TargetRevisionContentHash           string          `json:"target_revision_content_hash"`
	TargetRevisionID                    string          `json:"target_revision_id"`
	WorkflowRunID                       string          `json:"workflow_run_id"`
}

type postgresIdentityReservationWire struct {
	AuthorityID   string    `json:"authority_id"`
	IdentityKind  string    `json:"identity_kind"`
	IdentityValue string    `json:"identity_value"`
	Ordinal       int64     `json:"ordinal"`
	ReservedAt    time.Time `json:"reserved_at"`
}

type postgresPredecessorWire struct {
	AuthorityID            string          `json:"authority_id"`
	EdgeID                 string          `json:"edge_id"`
	MappingKind            string          `json:"mapping_kind"`
	MappingOrdinal         int64           `json:"mapping_ordinal"`
	MemberDocument         json.RawMessage `json:"member_document"`
	Ordinal                int64           `json:"ordinal"`
	OutputHash             string          `json:"output_hash"`
	OutputRevisionNumber   int64           `json:"output_revision_number"`
	SourceDefinitionNodeID string          `json:"source_definition_node_id"`
	SourceNodeKey          string          `json:"source_node_key"`
	SourceNodeRunID        string          `json:"source_node_run_id"`
	SourceNodeType         string          `json:"source_node_type"`
	SourcePort             string          `json:"source_port"`
	SourceSliceID          *string         `json:"source_slice_id"`
	SourceSliceKind        string          `json:"source_slice_kind"`
	SourceStatus           string          `json:"source_status"`
	TargetPort             string          `json:"target_port"`
	ValueHash              string          `json:"value_hash"`
}

type postgresManifestWire struct {
	AuthorityID    string          `json:"authority_id"`
	ContentHash    string          `json:"content_hash"`
	ContentRef     string          `json:"content_ref"`
	ContentStore   string          `json:"content_store"`
	Kind           string          `json:"kind"`
	ManifestHash   string          `json:"manifest_hash"`
	ManifestID     string          `json:"manifest_id"`
	MemberDocument json.RawMessage `json:"member_document"`
	Ordinal        int64           `json:"ordinal"`
	ProjectID      string          `json:"project_id"`
	RawBytes       string          `json:"raw_bytes"`
	RawBytesHash   string          `json:"raw_bytes_hash"`
	RawBytesSize   int64           `json:"raw_bytes_size"`
	Role           string          `json:"role"`
	SchemaVersion  int64           `json:"schema_version"`
}

type postgresRevisionWire struct {
	ArtifactID               string          `json:"artifact_id"`
	ArtifactKind             string          `json:"artifact_kind"`
	AuthorityID              string          `json:"authority_id"`
	ByteSize                 int64           `json:"byte_size"`
	CanonicalReviewRequired  bool            `json:"canonical_review_required"`
	ChangeSourceAtFreeze     string          `json:"change_source_at_freeze"`
	ContentHash              string          `json:"content_hash"`
	ContentRef               string          `json:"content_ref"`
	ContentStore             string          `json:"content_store"`
	CurrencyPolicy           string          `json:"currency_policy"`
	ImplementationProposalID *string         `json:"implementation_proposal_id"`
	MemberDocument           json.RawMessage `json:"member_document"`
	Ordinal                  int64           `json:"ordinal"`
	ProposalID               *string         `json:"proposal_id"`
	Purpose                  string          `json:"purpose"`
	RawBytes                 string          `json:"raw_bytes"`
	RawBytesHash             string          `json:"raw_bytes_hash"`
	RevisionID               string          `json:"revision_id"`
	SchemaVersion            int64           `json:"schema_version"`
	SourceRequiredAtFreeze   bool            `json:"source_required_at_freeze"`
	SourceManifestID         *string         `json:"source_manifest_id"`
	WasLatestApproved        bool            `json:"was_latest_approved"`
	WasLatestCurrent         bool            `json:"was_latest_current"`
	WorkflowStatusAtFreeze   string          `json:"workflow_status_at_freeze"`
}

type postgresReviewReceiptWire struct {
	ArtifactID           string          `json:"artifact_id"`
	AuthorityID          string          `json:"authority_id"`
	MemberDocument       json.RawMessage `json:"member_document"`
	Ordinal              int64           `json:"ordinal"`
	ProjectID            string          `json:"project_id"`
	Purpose              string          `json:"purpose"`
	ReceiptBytes         string          `json:"receipt_bytes"`
	ReceiptDocument      json.RawMessage `json:"receipt_document"`
	ReceiptHash          string          `json:"receipt_hash"`
	ReceiptRawBytesHash  string          `json:"receipt_raw_bytes_hash"`
	ReceiptRawBytesSize  int64           `json:"receipt_raw_bytes_size"`
	ReceiptSchemaVersion string          `json:"receipt_schema_version"`
	ReviewRequestID      string          `json:"review_request_id"`
	RevisionContentHash  string          `json:"revision_content_hash"`
	RevisionID           string          `json:"revision_id"`
}

func scanPostgresWorkflowInputBundle(row postgresWorkflowInputRow) (Record, error) {
	var encoded []byte
	if err := row.Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, ErrNotFound
		}
		return Record{}, err
	}
	if len(encoded) == 0 || len(encoded) > maximumPostgresRecoveryBundleBytes {
		return Record{}, corruptPostgresWorkflowInput("recovery bundle is absent or oversized")
	}

	var bundle postgresRecoveryBundleWire
	if err := decodeExactPostgresObject(encoded, &bundle); err != nil {
		return Record{}, corruptPostgresWorkflowInput("decode recovery bundle: %v", err)
	}
	var authority postgresAuthorityWire
	if err := decodeExactPostgresObject(bundle.Authority, &authority); err != nil {
		return Record{}, corruptPostgresWorkflowInput("decode authority row: %v", err)
	}

	requestBytes, err := decodePostgresBytea(authority.RequestBytes, MaximumRequestBytes)
	if err != nil {
		return Record{}, corruptPostgresWorkflowInput("request bytes: %v", err)
	}
	inputBytes, err := decodePostgresBytea(authority.InputBytes, MaximumInputBytes)
	if err != nil {
		return Record{}, corruptPostgresWorkflowInput("input bytes: %v", err)
	}
	targetBytes, err := decodePostgresBytea(authority.TargetBytes, MaximumTargetBytes)
	if err != nil {
		return Record{}, corruptPostgresWorkflowInput("target bytes: %v", err)
	}
	authorityBytes, err := decodePostgresBytea(authority.AuthorityBytes, MaximumAuthorityBytes)
	if err != nil {
		return Record{}, corruptPostgresWorkflowInput("authority bytes: %v", err)
	}
	definitionBytes, err := decodePostgresBytea(authority.DefinitionRawBytes, MaximumDefinitionBytes)
	if err != nil {
		return Record{}, corruptPostgresWorkflowInput("definition bytes: %v", err)
	}
	runScopeBytes, err := decodePostgresBytea(authority.RunScopeRawBytes, MaximumRunScopeBytes)
	if err != nil {
		return Record{}, corruptPostgresWorkflowInput("run scope bytes: %v", err)
	}
	nodeInputBytes, err := decodePostgresBytea(authority.NodeInputRawBytes, MaximumNodeInputBytes)
	if err != nil {
		return Record{}, corruptPostgresWorkflowInput("NodeInput bytes: %v", err)
	}
	buildManifestBytes, err := decodePostgresBytea(authority.BuildManifestRawBytes, MaximumBuildManifestBytes)
	if err != nil {
		return Record{}, corruptPostgresWorkflowInput("build manifest bytes: %v", err)
	}
	buildContractBytes, err := decodePostgresBytea(authority.BuildContractRawBytes, MaximumBuildContractBytes)
	if err != nil {
		return Record{}, corruptPostgresWorkflowInput("build contract bytes: %v", err)
	}

	request, err := DecodeFreezeRequest(requestBytes, authority.RequestHash)
	if err != nil {
		return Record{}, corruptPostgresWorkflowInput("request validation: %v", err)
	}
	input, err := DecodeInput(inputBytes, authority.InputHash)
	if err != nil {
		return Record{}, corruptPostgresWorkflowInput("input validation: %v", err)
	}
	target, err := DecodeTarget(targetBytes, authority.TargetHash)
	if err != nil {
		return Record{}, corruptPostgresWorkflowInput("target validation: %v", err)
	}
	envelope, err := DecodeAuthority(authorityBytes, authority.AuthorityHash)
	if err != nil {
		return Record{}, corruptPostgresWorkflowInput("authority validation: %v", err)
	}
	operationID, operationErr := uuid.Parse(authority.OperationID)
	authorityID, authorityErr := uuid.Parse(authority.AuthorityID)
	workflowRunID, runErr := uuid.Parse(authority.WorkflowRunID)
	nodeRunID, nodeErr := uuid.Parse(authority.NodeRunID)
	if operationErr != nil || authorityErr != nil || runErr != nil || nodeErr != nil ||
		operationID.Version() != 4 || authorityID.Version() != 4 || workflowRunID.Version() != 4 || nodeRunID.Version() != 4 {
		return Record{}, corruptPostgresWorkflowInput("authority identity projection is not UUIDv4")
	}

	record := Record{
		OperationID: operationID, AuthorityID: authorityID, WorkflowRunID: workflowRunID, NodeRunID: nodeRunID,
		Request: request, RequestBytes: requestBytes, RequestHash: authority.RequestHash,
		Target: target, TargetBytes: targetBytes, TargetHash: authority.TargetHash,
		Input: input, InputBytes: inputBytes, InputHash: authority.InputHash,
		Envelope: envelope, EnvelopeBytes: authorityBytes, AuthorityHash: authority.AuthorityHash,
		Materials: RetainedMaterials{
			BuildContract: buildContractBytes, BuildManifest: buildManifestBytes, Definition: definitionBytes,
			NodeInput: nodeInputBytes, RunScope: runScopeBytes,
		},
		FrozenAt: authority.FrozenAt.UTC(),
	}

	if err := rebuildPostgresWorkflowInputChildren(&record, authority, bundle); err != nil {
		return Record{}, err
	}
	if err := validatePostgresAuthorityProjection(record, authority); err != nil {
		return Record{}, err
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, corruptPostgresWorkflowInput("independent Record validation failed: %v", err)
	}
	return record, nil
}

func rebuildPostgresWorkflowInputChildren(record *Record, authority postgresAuthorityWire, bundle postgresRecoveryBundleWire) error {
	reservations, err := decodePostgresArray(bundle.IdentityReservations)
	if err != nil || len(reservations) != 3 {
		return corruptPostgresWorkflowInput("identity reservations are not an exact three-member array: %v", err)
	}
	wantReservations := map[string]string{
		"activation-event": record.Input.Gate.ActivationEventID,
		"authority":        record.AuthorityID.String(),
		"freeze-operation": record.OperationID.String(),
	}
	seenReservations := map[string]struct{}{}
	for _, encoded := range reservations {
		var reservation postgresIdentityReservationWire
		if err := decodeExactPostgresObject(encoded, &reservation); err != nil {
			return corruptPostgresWorkflowInput("decode identity reservation: %v", err)
		}
		want, exists := wantReservations[reservation.IdentityKind]
		if !exists || reservation.IdentityValue != want || reservation.AuthorityID != record.AuthorityID.String() ||
			reservation.Ordinal != 0 || !reservation.ReservedAt.UTC().Equal(record.FrozenAt) {
			return corruptPostgresWorkflowInput("identity reservation projection drifted")
		}
		if _, duplicate := seenReservations[reservation.IdentityKind]; duplicate {
			return corruptPostgresWorkflowInput("identity reservation kind is duplicated")
		}
		seenReservations[reservation.IdentityKind] = struct{}{}
	}

	predecessors, err := decodePostgresArray(bundle.Predecessors)
	if err != nil || len(predecessors) != len(record.Input.Predecessors) || int64(len(predecessors)) != authority.PredecessorCount {
		return corruptPostgresWorkflowInput("predecessor closure count drifted: %v", err)
	}
	for index, encoded := range predecessors {
		var row postgresPredecessorWire
		if err := decodeExactPostgresObject(encoded, &row); err != nil {
			return corruptPostgresWorkflowInput("decode predecessor %d: %v", index, err)
		}
		var member PredecessorBinding
		if err := decodeExactPostgresObject(row.MemberDocument, &member); err != nil {
			return corruptPostgresWorkflowInput("decode predecessor member %d: %v", index, err)
		}
		want := record.Input.Predecessors[index]
		if !reflect.DeepEqual(member, want) || row.AuthorityID != record.AuthorityID.String() || row.Ordinal != int64(index) ||
			row.EdgeID != want.EdgeID || row.SourceNodeRunID != want.SourceNodeRunID || row.SourceNodeKey != want.SourceNodeKey ||
			row.SourceDefinitionNodeID != want.SourceDefinitionNodeID || row.SourceNodeType != want.SourceNodeType ||
			row.SourceSliceKind != want.SourceSliceIdentity.Kind || !equalPostgresOptionalString(row.SourceSliceID, want.SourceSliceIdentity.ID) ||
			row.SourceStatus != want.SourceStatus || row.OutputRevisionNumber != want.OutputRevisionNumber ||
			row.SourcePort != want.SourcePort || row.TargetPort != want.TargetPort || row.MappingKind != want.MappingKind ||
			row.MappingOrdinal != want.MappingOrdinal || row.OutputHash != want.OutputHash || row.ValueHash != want.ValueHash {
			return corruptPostgresWorkflowInput("predecessor %d scalar or member projection drifted", index)
		}
	}

	manifests, err := decodePostgresArray(bundle.InputManifests)
	if err != nil || len(manifests) != len(record.Input.InputManifests) || int64(len(manifests)) != authority.ManifestCount {
		return corruptPostgresWorkflowInput("input manifest closure count drifted: %v", err)
	}
	for index, encoded := range manifests {
		var row postgresManifestWire
		if err := decodeExactPostgresObject(encoded, &row); err != nil {
			return corruptPostgresWorkflowInput("decode input manifest %d: %v", index, err)
		}
		var member InputManifestBinding
		if err := decodeExactPostgresObject(row.MemberDocument, &member); err != nil {
			return corruptPostgresWorkflowInput("decode input manifest member %d: %v", index, err)
		}
		raw, err := decodePostgresBytea(row.RawBytes, MaximumManifestBytes)
		if err != nil {
			return corruptPostgresWorkflowInput("input manifest %d raw bytes: %v", index, err)
		}
		want := record.Input.InputManifests[index]
		if !reflect.DeepEqual(member, want) || row.AuthorityID != record.AuthorityID.String() || row.Ordinal != int64(index) ||
			row.Role != want.Role || row.ManifestID != want.ID || row.ProjectID != want.ProjectID || row.Kind != want.Kind ||
			row.SchemaVersion != want.SchemaVersion || row.ManifestHash != want.ManifestHash || row.ContentStore != want.ContentStore ||
			row.ContentRef != want.ContentRef || row.ContentHash != want.ContentHash || row.RawBytesHash != want.RawBytesHash ||
			row.RawBytesSize != want.RawBytesSize || RawSHA256(raw) != row.RawBytesHash || int64(len(raw)) != row.RawBytesSize {
			return corruptPostgresWorkflowInput("input manifest %d scalar, member, or raw projection drifted", index)
		}
		record.Materials.InputManifests = append(record.Materials.InputManifests, InputManifestMaterial{
			Bytes: raw, ManifestID: row.ManifestID, Role: row.Role,
		})
	}

	revisions, err := decodePostgresArray(bundle.Revisions)
	if err != nil || len(revisions) != len(record.Input.Revisions) || int64(len(revisions)) != authority.RevisionCount {
		return corruptPostgresWorkflowInput("revision closure count drifted: %v", err)
	}
	for index, encoded := range revisions {
		var row postgresRevisionWire
		if err := decodeExactPostgresObject(encoded, &row); err != nil {
			return corruptPostgresWorkflowInput("decode revision %d: %v", index, err)
		}
		var member RevisionBinding
		if err := decodeExactPostgresObject(row.MemberDocument, &member); err != nil {
			return corruptPostgresWorkflowInput("decode revision member %d: %v", index, err)
		}
		raw, err := decodePostgresBytea(row.RawBytes, MaximumRevisionBytes)
		if err != nil {
			return corruptPostgresWorkflowInput("revision %d raw bytes: %v", index, err)
		}
		want := record.Input.Revisions[index]
		if !reflect.DeepEqual(member, want) || row.AuthorityID != record.AuthorityID.String() || row.Ordinal != int64(index) ||
			row.Purpose != want.Purpose || row.ArtifactKind != want.ArtifactKind || row.ArtifactID != want.ArtifactID ||
			row.RevisionID != want.RevisionID || row.ContentHash != want.ContentHash || row.ContentStore != want.ContentStore ||
			row.ContentRef != want.ContentRef || row.SchemaVersion != want.SchemaVersion || row.ByteSize != want.ByteSize ||
			row.ChangeSourceAtFreeze != want.ChangeSourceAtFreeze ||
			row.SourceRequiredAtFreeze != want.SourceRequiredAtFreeze ||
			row.CanonicalReviewRequired != want.CanonicalReviewRequired ||
			row.RawBytesHash != want.RawBytesHash || row.WorkflowStatusAtFreeze != want.WorkflowStatusAtFreeze ||
			!equalPostgresStringPointers(row.SourceManifestID, want.SourceManifestID) ||
			!equalPostgresStringPointers(row.ProposalID, want.ProposalID) ||
			!equalPostgresStringPointers(row.ImplementationProposalID, want.ImplementationProposalID) ||
			row.WasLatestCurrent != want.IsLatestCurrentAtFreeze || row.WasLatestApproved != want.IsLatestApprovedAtFreeze ||
			row.CurrencyPolicy != want.CurrencyPolicy || RawSHA256(raw) != row.RawBytesHash {
			return corruptPostgresWorkflowInput("revision %d scalar, member, or raw projection drifted", index)
		}
		record.Materials.Revisions = append(record.Materials.Revisions, RevisionMaterial{
			Bytes: raw, Purpose: row.Purpose, RevisionID: row.RevisionID,
		})
	}

	receipts, err := decodePostgresArray(bundle.ReviewReceipts)
	if err != nil || len(receipts) != len(record.Input.ReviewReceipts) || int64(len(receipts)) != authority.ReviewReceiptCount {
		return corruptPostgresWorkflowInput("review receipt closure count drifted: %v", err)
	}
	for index, encoded := range receipts {
		var row postgresReviewReceiptWire
		if err := decodeExactPostgresObject(encoded, &row); err != nil {
			return corruptPostgresWorkflowInput("decode review receipt %d: %v", index, err)
		}
		var member ReviewReceiptBinding
		if err := decodeExactPostgresObject(row.MemberDocument, &member); err != nil {
			return corruptPostgresWorkflowInput("decode review receipt member %d: %v", index, err)
		}
		raw, err := decodePostgresBytea(row.ReceiptBytes, MaximumReviewReceiptBytes)
		if err != nil {
			return corruptPostgresWorkflowInput("review receipt %d raw bytes: %v", index, err)
		}
		want := record.Input.ReviewReceipts[index]
		if !reflect.DeepEqual(member, want) || row.AuthorityID != record.AuthorityID.String() || row.Ordinal != int64(index) ||
			row.Purpose != want.Purpose || row.ReviewRequestID != want.ReviewRequestID || row.ReceiptHash != want.ReceiptHash ||
			row.ProjectID != want.ProjectID || row.ArtifactID != want.ArtifactID || row.RevisionID != want.RevisionID ||
			row.RevisionContentHash != want.RevisionContentHash || row.ReceiptSchemaVersion != want.ReceiptSchemaVersion ||
			row.ReceiptRawBytesHash != want.ReceiptRawBytesHash || row.ReceiptRawBytesSize != want.ReceiptRawBytesSize ||
			RawSHA256(raw) != row.ReceiptRawBytesHash || int64(len(raw)) != row.ReceiptRawBytesSize ||
			!postgresJSONProjectionEquals(row.ReceiptDocument, raw) {
			return corruptPostgresWorkflowInput("review receipt %d scalar, member, or raw projection drifted", index)
		}
		record.Materials.ReviewReceipts = append(record.Materials.ReviewReceipts, ReviewReceiptMaterial{
			Bytes: raw, ReviewRequestID: row.ReviewRequestID,
		})
	}
	return nil
}

func validatePostgresAuthorityProjection(record Record, row postgresAuthorityWire) error {
	input := record.Input
	request := record.Request
	envelope := record.Envelope
	if !postgresJSONProjectionEquals(row.RequestDocument, record.RequestBytes) ||
		!postgresJSONProjectionEquals(row.InputDocument, record.InputBytes) ||
		!postgresJSONProjectionEquals(row.TargetDocument, record.TargetBytes) ||
		!postgresJSONProjectionEquals(row.AuthorityDocument, record.EnvelopeBytes) {
		return corruptPostgresWorkflowInput("JSONB document projection differs from canonical bytes")
	}
	if row.AuthorityID != request.AuthorityID || row.OperationID != request.OperationID ||
		row.ProjectID != request.ProjectID || row.WorkflowRunID != request.WorkflowRunID || row.NodeRunID != request.NodeRunID ||
		row.RequestHash != record.RequestHash || row.InputHash != record.InputHash || row.TargetHash != record.TargetHash ||
		row.AuthorityHash != record.AuthorityHash || envelope.AuthorityID != row.AuthorityID || envelope.OperationID != row.OperationID {
		return corruptPostgresWorkflowInput("top-level identity or canonical hash projection drifted")
	}
	if row.GovernanceMode != input.Project.GovernanceMode || row.DefinitionID != input.Definition.DefinitionID ||
		row.DefinitionVersionID != input.Definition.DefinitionVersionID || row.DefinitionVersion != input.Definition.DefinitionVersion ||
		row.DefinitionHash != input.Definition.DefinitionHash || row.DefinitionRawBytesHash != input.Definition.RawBytesHash ||
		row.DefinitionRawBytesSize != input.Definition.RawBytesSize || row.ExecutionProfileVersion != input.Definition.ExecutionProfileVersion ||
		row.ExecutionProfileHash != input.Definition.ExecutionProfileHash || RawSHA256(record.Materials.Definition) != row.DefinitionRawBytesHash ||
		int64(len(record.Materials.Definition)) != row.DefinitionRawBytesSize {
		return corruptPostgresWorkflowInput("definition projection drifted")
	}
	startedAt, err := time.Parse(canonicalTimeLayout, input.Run.StartedAt)
	if err != nil || !row.RunStartedAt.UTC().Equal(startedAt) || row.RunInputManifestID != input.Run.InputManifestID ||
		row.RunInputManifestHash != input.Run.InputManifestHash || row.RunScopeRawBytesHash != input.Run.ScopeRawBytesHash ||
		row.RunScopeRawBytesSize != input.Run.ScopeRawBytesSize || row.RunStartedBy != input.Run.StartedBy ||
		RawSHA256(record.Materials.RunScope) != row.RunScopeRawBytesHash || int64(len(record.Materials.RunScope)) != row.RunScopeRawBytesSize {
		return corruptPostgresWorkflowInput("workflow run projection drifted")
	}
	if row.NodeKey != input.Gate.NodeKey || row.NodeType != input.Gate.NodeType || row.DefinitionNodeID != input.Gate.DefinitionNodeID ||
		row.SliceKind != input.Gate.SliceIdentity.Kind || !equalPostgresOptionalString(row.SliceID, input.Gate.SliceIdentity.ID) ||
		row.GateName != input.Gate.GateName || row.StageGate != input.Gate.StageGate ||
		row.ActivationEventID != input.Gate.ActivationEventID || row.ActivationEventSequence != input.Gate.ActivationEventSequence ||
		row.NodeInputRawBytesHash != input.NodeInput.RawBytesHash || row.NodeInputRawBytesSize != input.NodeInput.RawBytesSize ||
		row.NodeInputSemanticHash != input.NodeInput.SemanticHash || row.NodeInputBindingCount != input.NodeInput.BindingCount ||
		RawSHA256(record.Materials.NodeInput) != row.NodeInputRawBytesHash || int64(len(record.Materials.NodeInput)) != row.NodeInputRawBytesSize {
		return corruptPostgresWorkflowInput("external gate or NodeInput projection drifted")
	}
	targetArtifactID := ""
	for _, revision := range input.Revisions {
		if revision.Purpose == RevisionPurposeWorkspaceTarget && revision.RevisionID == input.Target.TargetRevisionID {
			targetArtifactID = revision.ArtifactID
			break
		}
	}
	if targetArtifactID == "" || row.ManifestSubject != input.Target.ManifestSubject || row.TargetArtifactID != targetArtifactID ||
		row.TargetRevisionID != input.Target.TargetRevisionID || row.TargetRevisionContentHash != input.Target.TargetRevisionContentHash ||
		row.QualityRunID != input.QualityResult.QualityRunID || !row.QualityPassed ||
		row.QualityWorkspaceRevisionID != input.QualityResult.WorkspaceRevisionID ||
		row.QualityWorkspaceRevisionContentHash != input.QualityResult.WorkspaceRevisionContentHash {
		return corruptPostgresWorkflowInput("target or quality-result projection drifted")
	}
	if row.BuildManifestID != input.Build.BuildManifest.ID || row.BuildManifestHash != input.Build.BuildManifest.ManifestHash ||
		row.BuildManifestContentHash != input.Build.BuildManifest.ContentHash || row.BuildManifestStatus != input.Build.BuildManifest.StatusAtFreeze ||
		row.BuildManifestRawBytesHash != input.Build.BuildManifest.RawBytesHash || row.BuildManifestRawBytesSize != input.Build.BuildManifest.RawBytesSize ||
		RawSHA256(record.Materials.BuildManifest) != row.BuildManifestRawBytesHash ||
		int64(len(record.Materials.BuildManifest)) != row.BuildManifestRawBytesSize ||
		row.BuildContractID != input.Build.BuildContract.ID || row.BuildContractHash != input.Build.BuildContract.ContractHash ||
		row.BuildContractContentHash != input.Build.BuildContract.ContentHash || row.BuildContractStatus != input.Build.BuildContract.StatusAtFreeze ||
		row.BuildContractRawBytesHash != input.Build.BuildContract.RawBytesHash || row.BuildContractRawBytesSize != input.Build.BuildContract.RawBytesSize ||
		RawSHA256(record.Materials.BuildContract) != row.BuildContractRawBytesHash ||
		int64(len(record.Materials.BuildContract)) != row.BuildContractRawBytesSize {
		return corruptPostgresWorkflowInput("build manifest or contract projection drifted")
	}
	if row.QualificationPolicyAuthorityID != input.QualificationPolicy.AuthorityID ||
		row.QualificationPolicyAuthorityHash != input.QualificationPolicy.AuthorityHash ||
		row.ExternalGatePolicy != input.QualificationPolicy.ExternalGatePolicy ||
		row.PredecessorCount != int64(len(input.Predecessors)) || row.ManifestCount != int64(len(input.InputManifests)) ||
		row.RevisionCount != int64(len(input.Revisions)) || row.ReviewReceiptCount != int64(len(input.ReviewReceipts)) {
		return corruptPostgresWorkflowInput("qualification policy or child count projection drifted")
	}
	if row.FrozenAt.Location() == nil || !record.FrozenAt.Equal(record.FrozenAt.UTC().Truncate(time.Millisecond)) ||
		record.FrozenAt.Year() < 1678 || record.FrozenAt.Year() >= 2262 {
		return corruptPostgresWorkflowInput("database freeze time is non-canonical")
	}
	return nil
}

func decodeExactPostgresObject(encoded []byte, destination any) error {
	if len(encoded) == 0 || !json.Valid(encoded) {
		return errors.New("invalid JSON")
	}
	if err := rejectDuplicateNames(encoded); err != nil {
		return err
	}
	value := reflect.ValueOf(destination)
	if value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Struct {
		return errors.New("destination must be a non-nil struct pointer")
	}
	var members map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(&members); err != nil || members == nil {
		return errors.New("JSON value is not an object")
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	want := map[string]struct{}{}
	typeOf := value.Elem().Type()
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			return fmt.Errorf("destination field %s has no closed JSON name", field.Name)
		}
		want[name] = struct{}{}
	}
	if len(members) != len(want) {
		return fmt.Errorf("object has %d members, expected %d", len(members), len(want))
	}
	for name := range members {
		if _, exists := want[name]; !exists {
			return fmt.Errorf("object contains unknown member %q", name)
		}
	}
	for name := range want {
		if _, exists := members[name]; !exists {
			return fmt.Errorf("object omits member %q", name)
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireEOF(decoder)
}

func decodePostgresArray(encoded []byte) ([]json.RawMessage, error) {
	if len(encoded) == 0 || !json.Valid(encoded) {
		return nil, errors.New("invalid JSON array")
	}
	if err := rejectDuplicateNames(encoded); err != nil {
		return nil, err
	}
	var values []json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(&values); err != nil || values == nil {
		return nil, errors.New("JSON value is not a non-null array")
	}
	if err := requireEOF(decoder); err != nil {
		return nil, err
	}
	return values, nil
}

func decodePostgresBytea(value string, maximum int) ([]byte, error) {
	if !strings.HasPrefix(value, `\x`) || len(value) < 4 || len(value) > 2+(maximum*2) {
		return nil, errors.New("bytea value is absent, non-hex, or oversized")
	}
	hexadecimal := value[2:]
	if strings.ToLower(hexadecimal) != hexadecimal || len(hexadecimal)%2 != 0 {
		return nil, errors.New("bytea hex is non-canonical")
	}
	decoded, err := hex.DecodeString(hexadecimal)
	if err != nil || len(decoded) == 0 || len(decoded) > maximum {
		return nil, errors.New("bytea hex is invalid or outside its bound")
	}
	return decoded, nil
}

func postgresJSONProjectionEquals(document, canonical []byte) bool {
	if len(document) == 0 || len(canonical) == 0 || !json.Valid(document) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil || requireEOF(decoder) != nil {
		return false
	}
	rebuilt, err := canonicalJSONWithLimit(value, MaximumCandidateBytes)
	return err == nil && bytes.Equal(rebuilt, canonical)
}

func equalPostgresOptionalString(value *string, expected string) bool {
	if expected == "" {
		return value == nil
	}
	return value != nil && *value == expected
}

func equalPostgresStringPointers(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

// TransactionalCurrentStore is the only interface suitable for Promotion
// consumption. Promotion first acquires the migration fence, then takes the
// project mutex, and finally asserts currentness; every lock lives in the same
// transaction as consume.
type TransactionalCurrentStore interface {
	AcquireMigrationFence(context.Context, Transaction) error
	AssertCurrentTx(context.Context, Transaction, uuid.UUID) (Record, error)
}

var _ Store = (*PostgresStore)(nil)
var _ TransactionalCurrentStore = (*PostgresStore)(nil)
var _ Transaction = (*PostgresTransaction)(nil)
