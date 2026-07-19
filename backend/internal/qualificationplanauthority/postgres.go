package qualificationplanauthority

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/qualificationevidence"
)

const (
	maximumPostgresFreezeRequestBytes = 64 << 10
	maximumPostgresInputBytes         = 4 << 20
	maximumPostgresProjectionBytes    = 16 << 20
	maximumPostgresEvidencePlanBytes  = 2 << 20
	maximumPostgresTrustBytes         = 1 << 20
	maximumPostgresTargetBytes        = 256 << 10
	maximumPostgresEnvelopeBytes      = 1 << 20
)

// PostgresStore persists the owner-only authority created by migration
// 000074. It is not a browser/application adapter and deliberately supplies no
// runtime role, DSN, or InputAuthority. Every read independently validates the
// raw canonical bytes, hashes, JSONB projections, scalar projections, and
// database-assigned freeze time before returning a Record.
type PostgresStore struct {
	database *sql.DB
	commit   func(*sql.Tx) error
}

func NewPostgresStore(database *sql.DB) (*PostgresStore, error) {
	if database == nil {
		return nil, fmt.Errorf("%w: PostgreSQL qualification plan authority database is required", ErrInvalid)
	}
	return &PostgresStore{
		database: database,
		commit:   func(transaction *sql.Tx) error { return transaction.Commit() },
	}, nil
}

func (store *PostgresStore) Freeze(ctx context.Context, candidate Record) (Record, error) {
	if store == nil || store.database == nil || store.commit == nil || isNilInterface(ctx) {
		return Record{}, fmt.Errorf("%w: PostgreSQL qualification plan Store or context is incomplete", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	candidate.Idempotent = false
	if err := validateRecordMaterials(candidate, false); err != nil {
		return Record{}, err
	}
	if err := validatePostgresRecordBounds(candidate); err != nil {
		return Record{}, err
	}

	// Exact operation replay is decided before entering the freeze routine. It
	// therefore remains available after the original InputAuthority has been
	// retired and does not sample a new frozenAt.
	existing, inspectErr := store.InspectOperation(ctx, candidate.OperationID)
	if inspectErr == nil {
		if !sameImmutableRecord(existing, candidate) {
			return Record{}, fmt.Errorf("%w: PostgreSQL operation is bound to different canonical bytes", ErrConflict)
		}
		existing.Idempotent = true
		return cloneRecord(existing), nil
	}
	if !errors.Is(inspectErr, ErrNotFound) {
		return Record{}, inspectErr
	}

	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Record{}, fmt.Errorf("begin PostgreSQL qualification plan freeze: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	// Serialize supported writers for one operation before the in-transaction
	// inspection. The migration routine contains an EXCEPTION block, so a new
	// row can carry a subtransaction xmin; neither xmin nor timestamps are a
	// sound way to distinguish insert from exact replay. A hash collision only
	// adds conservative serialization and cannot alias immutable identities.
	if _, err := transaction.ExecContext(ctx, `
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended($1::text, 740074)
)`, candidate.OperationID); err != nil {
		return Record{}, fmt.Errorf("acquire PostgreSQL qualification plan operation lock: %w", err)
	}
	// Migration 000074 down takes the three Evidence relations before either
	// Plan relation. Take the same relation locks before the replay SELECT: the
	// freeze routine repeats this sequence, but PostgreSQL relation locks are
	// transaction-scoped and re-entrant. Without this pre-lock, the SELECT would
	// hold AccessShare on the Plan table while down held AccessExclusive on the
	// Evidence tables, and each side could then wait for the other.
	if _, err := transaction.ExecContext(ctx, `
LOCK TABLE qualification_evidence_events IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_evidence_operations IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_evidence_heads IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_plan_authorities IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_plan_identity_reservations IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return Record{}, fmt.Errorf("acquire PostgreSQL qualification plan migration-order locks: %w", err)
	}
	current, currentErr := scanPostgresQualificationPlanRecord(transaction.QueryRowContext(ctx,
		postgresQualificationPlanSelect+` WHERE operation_id=$1`, candidate.OperationID))
	if currentErr == nil {
		if !sameImmutableRecord(current, candidate) {
			return Record{}, fmt.Errorf("%w: PostgreSQL operation is bound to different canonical bytes", ErrConflict)
		}
		if err := transaction.Rollback(); err != nil {
			return Record{}, fmt.Errorf("close PostgreSQL exact-replay inspection: %w", err)
		}
		committed = true
		current.Idempotent = true
		return cloneRecord(current), nil
	}
	if !errors.Is(currentErr, ErrNotFound) {
		return Record{}, currentErr
	}

	stored, err := scanPostgresQualificationPlanRecord(transaction.QueryRowContext(ctx, postgresQualificationPlanFreezeQuery,
		candidate.OperationID, candidate.AuthorityID,
		candidate.RequestHash, candidate.RequestBytes, string(candidate.RequestBytes),
		candidate.InputHash, candidate.InputBytes, string(candidate.InputBytes),
		candidate.ProjectionHash, candidate.ProjectionBytes, string(candidate.ProjectionBytes),
		candidate.EvidencePlanHash, candidate.EvidencePlanBytes, string(candidate.EvidencePlanBytes),
		candidate.TrustHash, candidate.TrustBytes, string(candidate.TrustBytes),
		candidate.TargetHash, candidate.TargetBytes, string(candidate.TargetBytes),
		candidate.EnvelopeHash, candidate.EnvelopeBytes, string(candidate.EnvelopeBytes),
	))
	if err != nil {
		classified := classifyPostgresQualificationPlanFreezeError(err)
		_ = transaction.Rollback()
		if errors.Is(classified, ErrConflict) {
			// A SERIALIZABLE waiter may observe the winning exact operation as a
			// conflict. Reconcile through a new snapshot and only accept byte-exact
			// equality; a cross-identity collision remains a conflict.
			current, reconcileErr := store.InspectOperation(ctx, candidate.OperationID)
			if reconcileErr == nil && sameImmutableRecord(current, candidate) {
				current.Idempotent = true
				return cloneRecord(current), nil
			}
			if reconcileErr != nil && !errors.Is(reconcileErr, ErrNotFound) {
				return Record{}, reconcileErr
			}
		}
		return Record{}, classified
	}
	if !sameImmutableRecord(stored, candidate) {
		return Record{}, fmt.Errorf("%w: PostgreSQL freeze returned different immutable bytes", ErrConflict)
	}

	if err := store.commit(transaction); err != nil {
		return Record{}, errors.Join(ErrStoreOutcomeUnknown, fmt.Errorf("commit PostgreSQL qualification plan freeze: %w", err))
	}
	committed = true
	stored.Idempotent = false
	return cloneRecord(stored), nil
}

func (store *PostgresStore) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) || operationID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	return scanPostgresQualificationPlanRecord(store.database.QueryRowContext(ctx,
		postgresQualificationPlanSelect+` WHERE operation_id=$1`, operationID))
}

func (store *PostgresStore) ResolveAuthority(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) || authorityID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	return scanPostgresQualificationPlanRecord(store.database.QueryRowContext(ctx,
		postgresQualificationPlanResolveQuery, authorityID))
}

const postgresQualificationPlanColumns = `
authority_id, operation_id, input_authority_id, plan_artifact_id,
orchestration_id, qualification_run_id, fixture_id, credential_set_id,
request_hash, request_bytes, request_document,
input_hash, input_bytes, input_document,
projection_hash, projection_bytes, projection_document,
evidence_plan_hash, evidence_plan_bytes, evidence_plan_document,
trust_hash, trust_bytes, trust_document, trust_bindings_digest, trust_policy_digest,
target_hash, target_bytes, target_document, project_id, workflow_run_id, node_key,
target_revision_id, target_revision_content_hash, subject, stage_gate,
envelope_hash, envelope_bytes, envelope_document,
source_tree_digest, template_release_digest, frozen_at`

const postgresQualificationPlanSelect = `SELECT ` + postgresQualificationPlanColumns + `
FROM qualification_plan_authorities`

const postgresQualificationPlanResolveQuery = `SELECT ` + postgresQualificationPlanColumns + `
FROM resolve_qualification_plan_authority($1)`

const postgresQualificationPlanFreezeQuery = `SELECT ` + postgresQualificationPlanColumns + `
FROM freeze_qualification_plan_authority(
  $1,$2,$3,$4,$5::jsonb,$6,$7,$8::jsonb,$9,$10,$11::jsonb,
  $12,$13,$14::jsonb,$15,$16,$17::jsonb,$18,$19,$20::jsonb,$21,$22,$23::jsonb
)`

type postgresQualificationPlanRow interface {
	Scan(...any) error
}

func scanPostgresQualificationPlanRecord(row postgresQualificationPlanRow) (Record, error) {
	var (
		authorityIDText, operationIDText, inputAuthorityIDText             string
		planArtifactID, orchestrationID, runID, fixtureID, credentialSetID string

		requestHash                                                                                 string
		requestBytes, requestDocument                                                               []byte
		inputHash                                                                                   string
		inputBytes, inputDocument                                                                   []byte
		projectionHash                                                                              string
		projectionBytes, projectionDocument                                                         []byte
		evidencePlanHash                                                                            string
		evidencePlanBytes, evidencePlanDocument                                                     []byte
		trustHash                                                                                   string
		trustBytes, trustDocument                                                                   []byte
		trustBindingsDigest, trustPolicyDigest                                                      string
		targetHash                                                                                  string
		targetBytes, targetDocument                                                                 []byte
		projectID, workflowRunID, nodeKey, targetRevisionID, targetRevisionHash, subject, stageGate string
		envelopeHash                                                                                string
		envelopeBytes, envelopeDocument                                                             []byte
		sourceTreeDigest, templateReleaseDigest                                                     string
		frozenAt                                                                                    time.Time
	)
	if err := row.Scan(
		&authorityIDText, &operationIDText, &inputAuthorityIDText, &planArtifactID,
		&orchestrationID, &runID, &fixtureID, &credentialSetID,
		&requestHash, &requestBytes, &requestDocument,
		&inputHash, &inputBytes, &inputDocument,
		&projectionHash, &projectionBytes, &projectionDocument,
		&evidencePlanHash, &evidencePlanBytes, &evidencePlanDocument,
		&trustHash, &trustBytes, &trustDocument, &trustBindingsDigest, &trustPolicyDigest,
		&targetHash, &targetBytes, &targetDocument, &projectID, &workflowRunID, &nodeKey,
		&targetRevisionID, &targetRevisionHash, &subject, &stageGate,
		&envelopeHash, &envelopeBytes, &envelopeDocument,
		&sourceTreeDigest, &templateReleaseDigest, &frozenAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, ErrNotFound
		}
		return Record{}, fmt.Errorf("scan PostgreSQL qualification plan authority: %w", err)
	}

	rawAndDocuments := [][2][]byte{
		{requestBytes, requestDocument}, {inputBytes, inputDocument}, {projectionBytes, projectionDocument},
		{evidencePlanBytes, evidencePlanDocument}, {trustBytes, trustDocument},
		{targetBytes, targetDocument}, {envelopeBytes, envelopeDocument},
	}
	for _, pair := range rawAndDocuments {
		canonicalDocument, err := canonicalRawJSON(pair[1])
		if err != nil || !bytes.Equal(canonicalDocument, pair[0]) {
			return Record{}, fmt.Errorf("%w: PostgreSQL JSONB projection drifted from canonical raw bytes", ErrConflict)
		}
	}

	var request FreezeRequest
	var input ResolvedInputDocument
	var plan qualificationevidence.Plan
	var trust TrustDocument
	var target TargetDocument
	var envelope AuthorityEnvelope
	for _, decoded := range []struct {
		raw    []byte
		target any
	}{
		{requestBytes, &request}, {inputBytes, &input}, {evidencePlanBytes, &plan},
		{trustBytes, &trust}, {targetBytes, &target}, {envelopeBytes, &envelope},
	} {
		if err := decodePostgresQualificationPlanJSON(decoded.raw, decoded.target); err != nil {
			return Record{}, err
		}
	}
	authorityID, authorityErr := uuid.Parse(authorityIDText)
	operationID, operationErr := uuid.Parse(operationIDText)
	inputAuthorityID, inputErr := uuid.Parse(inputAuthorityIDText)
	if authorityErr != nil || operationErr != nil || inputErr != nil {
		return Record{}, fmt.Errorf("%w: PostgreSQL qualification plan UUID projection is invalid", ErrConflict)
	}
	record := Record{
		OperationID: operationID, AuthorityID: authorityID, InputAuthorityID: inputAuthorityID,
		RequestHash: requestHash, RequestBytes: append([]byte(nil), requestBytes...), Request: request,
		InputHash: inputHash, InputBytes: append([]byte(nil), inputBytes...), Input: input,
		ProjectionHash: projectionHash, ProjectionBytes: append([]byte(nil), projectionBytes...),
		ProjectionDocument: append(json.RawMessage(nil), projectionBytes...),
		EvidencePlanHash:   evidencePlanHash, EvidencePlanBytes: append([]byte(nil), evidencePlanBytes...), EvidencePlan: plan,
		TrustHash: trustHash, TrustBytes: append([]byte(nil), trustBytes...), Trust: trust,
		TargetHash: targetHash, TargetBytes: append([]byte(nil), targetBytes...), Target: target,
		EnvelopeHash: envelopeHash, EnvelopeBytes: append([]byte(nil), envelopeBytes...), Envelope: envelope,
		FrozenAt: frozenAt.UTC(),
	}
	if err := validatePostgresRecordBounds(record); err != nil {
		return Record{}, fmt.Errorf("%w: stored PostgreSQL material exceeds its table bound: %v", ErrConflict, err)
	}
	if err := validateStoredRecord(record); err != nil {
		return Record{}, err
	}
	if planArtifactID != record.Envelope.ArtifactID || orchestrationID != record.EvidencePlan.OrchestrationID ||
		runID != record.EvidencePlan.RunID || fixtureID != record.EvidencePlan.FixtureID ||
		credentialSetID != record.EvidencePlan.CredentialSet.SetID ||
		trustBindingsDigest != record.Envelope.TrustBindingsDigest || trustPolicyDigest != record.Trust.TrustPolicyDigest ||
		projectID != record.Target.PromotionTarget.ProjectID || workflowRunID != record.Target.PromotionTarget.WorkflowRunID ||
		nodeKey != record.Target.PromotionTarget.NodeKey || targetRevisionID != record.Target.PromotionTarget.TargetRevision.ID ||
		targetRevisionHash != record.Target.PromotionTarget.TargetRevision.ContentHash ||
		subject != record.Target.PromotionTarget.Subject || stageGate != record.Target.PromotionTarget.StageGate ||
		sourceTreeDigest != record.EvidencePlan.SourceTreeDigest || templateReleaseDigest != record.EvidencePlan.TemplateReleaseDigest {
		return Record{}, fmt.Errorf("%w: PostgreSQL scalar projection drifted from immutable documents", ErrConflict)
	}
	return record, nil
}

func decodePostgresQualificationPlanJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: decode PostgreSQL qualification plan raw bytes: %v", ErrConflict, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: PostgreSQL qualification plan raw bytes have trailing JSON", ErrConflict)
	}
	return nil
}

func validatePostgresRecordBounds(record Record) error {
	bounds := []struct {
		name    string
		value   []byte
		maximum int
	}{
		{"request", record.RequestBytes, maximumPostgresFreezeRequestBytes},
		{"input", record.InputBytes, maximumPostgresInputBytes},
		{"projection", record.ProjectionBytes, maximumPostgresProjectionBytes},
		{"evidence plan", record.EvidencePlanBytes, maximumPostgresEvidencePlanBytes},
		{"trust", record.TrustBytes, maximumPostgresTrustBytes},
		{"target", record.TargetBytes, maximumPostgresTargetBytes},
		{"envelope", record.EnvelopeBytes, maximumPostgresEnvelopeBytes},
	}
	for _, bound := range bounds {
		if len(bound.value) == 0 || len(bound.value) > bound.maximum {
			return fmt.Errorf("%w: PostgreSQL qualification plan %s bytes exceed the database bound", ErrInvalid, bound.name)
		}
	}
	return nil
}

func classifyPostgresQualificationPlanFreezeError(err error) error {
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "WQP01", "40001", "40P01", "23503", "23505":
			return errors.Join(ErrConflict, fmt.Errorf("freeze PostgreSQL qualification plan authority: %w", err))
		case "WQP03", "23502", "23514", "22001", "22003", "22007", "22021", "22023", "22P02":
			return errors.Join(ErrInvalid, fmt.Errorf("freeze PostgreSQL qualification plan authority: %w", err))
		default:
			return fmt.Errorf("freeze PostgreSQL qualification plan authority: %w", err)
		}
	}
	return errors.Join(ErrStoreOutcomeUnknown, fmt.Errorf("freeze PostgreSQL qualification plan authority: %w", err))
}

var _ Store = (*PostgresStore)(nil)
