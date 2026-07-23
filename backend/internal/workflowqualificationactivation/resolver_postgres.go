package workflowqualificationactivation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/workflowinputauthority"
)

// The resolver is deliberately a single, read-only capability call. UUID
// projections are cast to text so both their canonical spelling and UUIDv4
// version are independently checked in Go before any authority is activated.
const postgresQualityCompletionCandidateQuery = `
SELECT
  classification,
  completion_event_id::text,
  precommit_id::text,
  freeze_request_hash,
  freeze_request_bytes,
  workflow_input_hash,
  workflow_input_bytes,
  freeze_candidate_bytes,
  definition_raw_bytes,
  run_scope_raw_bytes,
  node_input_raw_bytes,
  build_manifest_raw_bytes,
  build_contract_raw_bytes,
  material_bundle,
  snapshot_hash,
  retained_raw_bytes_size
FROM resolve_workflow_v3_quality_completion_candidate_v1($1)`

// Retained bytes are represented as lowercase hexadecimal inside the JSONB
// child bundle. Leave bounded room for member names, hashes, identities, and
// collection punctuation without admitting an unbounded database value.
const maximumPostgresQualityMaterialBundleBytes = (2 * workflowinputauthority.MaximumRetainedBytes) +
	((workflowinputauthority.MaximumManifests + workflowinputauthority.MaximumRevisions + workflowinputauthority.MaximumReviewReceipts) * 512) +
	1024

// PostgresResolver resolves one committed completion event into the exact
// immutable Workflow Input candidate authored by the database precommit. It
// never derives authority facts from the broker delivery or mutable UI state.
type PostgresResolver struct {
	database *sql.DB
}

var _ Resolver = (*PostgresResolver)(nil)

func NewPostgresResolver(database *sql.DB) (*PostgresResolver, error) {
	if database == nil {
		return nil, fmt.Errorf("%w: PostgreSQL database is required", ErrInvalid)
	}
	return &PostgresResolver{database: database}, nil
}

func (resolver *PostgresResolver) Resolve(
	ctx context.Context,
	completionEventID CompletionEventID,
) (Resolution, error) {
	if resolver == nil || resolver.database == nil || isNilInterface(ctx) || !completionEventID.valid() {
		return Resolution{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}

	rows, err := resolver.database.QueryContext(ctx, postgresQualityCompletionCandidateQuery, completionEventID.String())
	if err != nil {
		return Resolution{}, classifyPostgresResolverError("resolve quality completion candidate", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = rows.Close()
		}
	}()

	wire, err := readExactPostgresQualityCompletionCandidateRow(rows)
	if err != nil {
		_ = rows.Close()
		closed = true
		return Resolution{}, err
	}
	// Release the pooled connection before strict JSON decoding, hexadecimal
	// material reconstruction, and Candidate compilation do CPU work.
	if err := rows.Close(); err != nil {
		closed = true
		return Resolution{}, classifyPostgresResolverError("close quality completion candidate result", err)
	}
	closed = true

	return decodePostgresQualityCompletionCandidate(wire, completionEventID, workflowinputauthority.Compile)
}

type postgresQualityCompletionCandidateRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

type postgresQualityCompletionCandidateWire struct {
	classification        sql.NullString
	completionEventID     sql.NullString
	precommitID           sql.NullString
	freezeRequestHash     sql.NullString
	freezeRequestBytes    []byte
	workflowInputHash     sql.NullString
	workflowInputBytes    []byte
	freezeCandidateBytes  []byte
	definitionRawBytes    []byte
	runScopeRawBytes      []byte
	nodeInputRawBytes     []byte
	buildManifestRawBytes []byte
	buildContractRawBytes []byte
	materialBundle        []byte
	snapshotHash          sql.NullString
	retainedRawBytesSize  sql.NullInt64
}

func readExactPostgresQualityCompletionCandidateRow(
	rows postgresQualityCompletionCandidateRows,
) (postgresQualityCompletionCandidateWire, error) {
	if isNilInterface(rows) {
		return postgresQualityCompletionCandidateWire{}, ErrInvalid
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return postgresQualityCompletionCandidateWire{}, classifyPostgresResolverError("read quality completion candidate", err)
		}
		return postgresQualityCompletionCandidateWire{}, ErrNotFound
	}

	var wire postgresQualityCompletionCandidateWire
	if err := rows.Scan(
		&wire.classification,
		&wire.completionEventID,
		&wire.precommitID,
		&wire.freezeRequestHash,
		&wire.freezeRequestBytes,
		&wire.workflowInputHash,
		&wire.workflowInputBytes,
		&wire.freezeCandidateBytes,
		&wire.definitionRawBytes,
		&wire.runScopeRawBytes,
		&wire.nodeInputRawBytes,
		&wire.buildManifestRawBytes,
		&wire.buildContractRawBytes,
		&wire.materialBundle,
		&wire.snapshotHash,
		&wire.retainedRawBytesSize,
	); err != nil {
		if postgresResolverErrorIsDatabase(err) {
			return postgresQualityCompletionCandidateWire{}, classifyPostgresResolverError("scan quality completion candidate", err)
		}
		return postgresQualityCompletionCandidateWire{}, corruptPostgresResolver("16-column resolver ABI scan failed: %v", err)
	}
	if rows.Next() {
		return postgresQualityCompletionCandidateWire{}, corruptPostgresResolver("resolver returned more than one row")
	}
	if err := rows.Err(); err != nil {
		return postgresQualityCompletionCandidateWire{}, classifyPostgresResolverError("finish quality completion candidate result", err)
	}
	return wire, nil
}

type postgresQualityCandidateCompiler func(workflowinputauthority.Candidate) (workflowinputauthority.Record, error)

type postgresQualityCandidateCodecs struct {
	decodeRequest  func([]byte, string) (workflowinputauthority.FreezeRequest, error)
	decodeInput    func([]byte, string) (workflowinputauthority.WorkflowInputDocument, error)
	decodeDocument func([]byte) (workflowinputauthority.FreezeCandidateDocument, error)
	compile        postgresQualityCandidateCompiler
}

func decodePostgresQualityCompletionCandidate(
	wire postgresQualityCompletionCandidateWire,
	completionEventID CompletionEventID,
	compile postgresQualityCandidateCompiler,
) (Resolution, error) {
	return decodePostgresQualityCompletionCandidateWithCodecs(wire, completionEventID, postgresQualityCandidateCodecs{
		decodeRequest:  workflowinputauthority.DecodeFreezeRequest,
		decodeInput:    workflowinputauthority.DecodeInput,
		decodeDocument: workflowinputauthority.DecodeFreezeCandidate,
		compile:        compile,
	})
}

func decodePostgresQualityCompletionCandidateWithCodecs(
	wire postgresQualityCompletionCandidateWire,
	completionEventID CompletionEventID,
	codecs postgresQualityCandidateCodecs,
) (Resolution, error) {
	if !completionEventID.valid() || codecs.decodeRequest == nil || codecs.decodeInput == nil ||
		codecs.decodeDocument == nil || codecs.compile == nil {
		return Resolution{}, ErrInvalid
	}
	if !wire.classification.Valid || !wire.completionEventID.Valid ||
		wire.completionEventID.String != completionEventID.String() {
		return Resolution{}, corruptPostgresResolver("classification or completion event projection is absent or different")
	}
	projectedEventID, err := ParseCompletionEventID(wire.completionEventID.String)
	if err != nil || projectedEventID != completionEventID {
		return Resolution{}, corruptPostgresResolver("completion event projection is not the requested canonical UUIDv4")
	}

	switch Classification(wire.classification.String) {
	case ClassificationNonTarget:
		if !wire.candidateColumnsAreNull() {
			return Resolution{}, corruptPostgresResolver("non-target row carries target authority facts")
		}
		return Resolution{Classification: ClassificationNonTarget}, nil
	case ClassificationTarget:
		if !wire.targetColumnsArePresent() {
			return Resolution{}, corruptPostgresResolver("target row has a null or empty authority fact")
		}
	default:
		return Resolution{}, corruptPostgresResolver("resolver classification %q is outside the closed set", wire.classification.String)
	}

	if !canonicalUUIDv4(wire.precommitID.String) {
		return Resolution{}, corruptPostgresResolver("precommit identity is not a canonical UUIDv4")
	}
	if wire.precommitID.String == completionEventID.String() {
		return Resolution{}, corruptPostgresResolver("precommit identity reuses the completion event identity")
	}
	if !validRawDigest(wire.snapshotHash.String) {
		return Resolution{}, corruptPostgresResolver("snapshot hash is not a canonical SHA-256 digest")
	}
	if wire.retainedRawBytesSize.Int64 < 1 ||
		wire.retainedRawBytesSize.Int64 > workflowinputauthority.MaximumRetainedBytes {
		return Resolution{}, corruptPostgresResolver("retained raw byte total is outside the v1 bound")
	}
	if err := requireBoundedRaw("definition", wire.definitionRawBytes, workflowinputauthority.MaximumDefinitionBytes); err != nil {
		return Resolution{}, err
	}
	if err := requireBoundedRaw("run scope", wire.runScopeRawBytes, workflowinputauthority.MaximumRunScopeBytes); err != nil {
		return Resolution{}, err
	}
	if err := requireBoundedRaw("node input", wire.nodeInputRawBytes, workflowinputauthority.MaximumNodeInputBytes); err != nil {
		return Resolution{}, err
	}
	if err := requireBoundedRaw("build manifest", wire.buildManifestRawBytes, workflowinputauthority.MaximumBuildManifestBytes); err != nil {
		return Resolution{}, err
	}
	if err := requireBoundedRaw("build contract", wire.buildContractRawBytes, workflowinputauthority.MaximumBuildContractBytes); err != nil {
		return Resolution{}, err
	}

	request, err := codecs.decodeRequest(wire.freezeRequestBytes, wire.freezeRequestHash.String)
	if err != nil {
		return Resolution{}, corruptPostgresResolver("freeze request does not decode as exact immutable bytes: %v", err)
	}
	input, err := codecs.decodeInput(wire.workflowInputBytes, wire.workflowInputHash.String)
	if err != nil {
		return Resolution{}, corruptPostgresResolver("workflow input does not decode as exact immutable bytes: %v", err)
	}
	document, err := codecs.decodeDocument(wire.freezeCandidateBytes)
	if err != nil {
		return Resolution{}, corruptPostgresResolver("freeze candidate does not decode as exact private bytes: %v", err)
	}
	children, childRawBytes, err := decodePostgresQualityMaterialBundle(wire.materialBundle)
	if err != nil {
		return Resolution{}, err
	}

	topRawBytes, overflow := addBoundedRawSizes(
		len(wire.definitionRawBytes),
		len(wire.runScopeRawBytes),
		len(wire.nodeInputRawBytes),
		len(wire.buildManifestRawBytes),
		len(wire.buildContractRawBytes),
	)
	if overflow || childRawBytes > workflowinputauthority.MaximumRetainedBytes-topRawBytes ||
		int64(topRawBytes+childRawBytes) != wire.retainedRawBytesSize.Int64 {
		return Resolution{}, corruptPostgresResolver("retained raw byte total does not equal the reconstructed material set")
	}

	candidate := workflowinputauthority.Candidate{
		Document: document,
		Input:    input,
		Materials: workflowinputauthority.RetainedMaterials{
			BuildContract:  cloneResolverBytes(wire.buildContractRawBytes),
			BuildManifest:  cloneResolverBytes(wire.buildManifestRawBytes),
			Definition:     cloneResolverBytes(wire.definitionRawBytes),
			InputManifests: children.inputManifests,
			NodeInput:      cloneResolverBytes(wire.nodeInputRawBytes),
			ReviewReceipts: children.reviewReceipts,
			Revisions:      children.revisions,
			RunScope:       cloneResolverBytes(wire.runScopeRawBytes),
		},
		Request: request,
	}
	compiled, err := codecs.compile(candidate)
	if err != nil {
		return Resolution{}, corruptPostgresResolver("reconstructed Candidate does not compile: %v", err)
	}
	activationEventID, err := uuid.Parse(compiled.Input.Gate.ActivationEventID)
	if err != nil || activationEventID.Version() != 4 || activationEventID.Variant() != uuid.RFC4122 {
		return Resolution{}, corruptPostgresResolver("compiled activation event identity is invalid")
	}
	for role, identity := range map[string]uuid.UUID{
		"operation":  compiled.OperationID,
		"authority":  compiled.AuthorityID,
		"run":        compiled.WorkflowRunID,
		"node":       compiled.NodeRunID,
		"activation": activationEventID,
	} {
		if identity.String() == completionEventID.String() || identity.String() == wire.precommitID.String {
			return Resolution{}, corruptPostgresResolver("%s identity collides with completion or precommit identity", role)
		}
	}
	return Resolution{Classification: ClassificationTarget, Candidate: candidate}, nil
}

func (wire postgresQualityCompletionCandidateWire) candidateColumnsAreNull() bool {
	return !wire.precommitID.Valid &&
		!wire.freezeRequestHash.Valid && wire.freezeRequestBytes == nil &&
		!wire.workflowInputHash.Valid && wire.workflowInputBytes == nil &&
		wire.freezeCandidateBytes == nil && wire.definitionRawBytes == nil &&
		wire.runScopeRawBytes == nil && wire.nodeInputRawBytes == nil &&
		wire.buildManifestRawBytes == nil && wire.buildContractRawBytes == nil &&
		wire.materialBundle == nil && !wire.snapshotHash.Valid && !wire.retainedRawBytesSize.Valid
}

func (wire postgresQualityCompletionCandidateWire) targetColumnsArePresent() bool {
	return wire.precommitID.Valid && wire.precommitID.String != "" &&
		wire.freezeRequestHash.Valid && wire.freezeRequestHash.String != "" && len(wire.freezeRequestBytes) > 0 &&
		wire.workflowInputHash.Valid && wire.workflowInputHash.String != "" && len(wire.workflowInputBytes) > 0 &&
		len(wire.freezeCandidateBytes) > 0 && len(wire.definitionRawBytes) > 0 &&
		len(wire.runScopeRawBytes) > 0 && len(wire.nodeInputRawBytes) > 0 &&
		len(wire.buildManifestRawBytes) > 0 && len(wire.buildContractRawBytes) > 0 &&
		len(wire.materialBundle) > 0 && wire.snapshotHash.Valid && wire.snapshotHash.String != "" &&
		wire.retainedRawBytesSize.Valid
}

type postgresQualityMaterialBundle struct {
	InputManifests []postgresQualityManifestMaterial `json:"inputManifests"`
	Revisions      []postgresQualityRevisionMaterial `json:"revisions"`
	ReviewReceipts []postgresQualityReceiptMaterial  `json:"reviewReceipts"`
}

type postgresQualityManifestMaterial struct {
	ManifestID   string `json:"manifestId"`
	RawBytesHash string `json:"rawBytesHash"`
	RawBytesHex  string `json:"rawBytesHex"`
	RawBytesSize int64  `json:"rawBytesSize"`
	Role         string `json:"role"`
}

type postgresQualityRevisionMaterial struct {
	Purpose      string `json:"purpose"`
	RawBytesHash string `json:"rawBytesHash"`
	RawBytesHex  string `json:"rawBytesHex"`
	RawBytesSize int64  `json:"rawBytesSize"`
	RevisionID   string `json:"revisionId"`
}

type postgresQualityReceiptMaterial struct {
	RawBytesHash    string `json:"rawBytesHash"`
	RawBytesHex     string `json:"rawBytesHex"`
	RawBytesSize    int64  `json:"rawBytesSize"`
	ReviewRequestID string `json:"reviewRequestId"`
}

type decodedPostgresQualityMaterials struct {
	inputManifests []workflowinputauthority.InputManifestMaterial
	revisions      []workflowinputauthority.RevisionMaterial
	reviewReceipts []workflowinputauthority.ReviewReceiptMaterial
}

func decodePostgresQualityMaterialBundle(
	encoded []byte,
) (decodedPostgresQualityMaterials, int, error) {
	if len(encoded) < 1 || len(encoded) > maximumPostgresQualityMaterialBundleBytes ||
		!utf8.Valid(encoded) || bytes.HasPrefix(encoded, []byte{0xef, 0xbb, 0xbf}) {
		return decodedPostgresQualityMaterials{}, 0, corruptPostgresResolver("material bundle is absent, oversized, or invalid UTF-8")
	}
	if err := scanExactJSON(encoded); err != nil {
		return decodedPostgresQualityMaterials{}, 0, corruptPostgresResolver("material bundle JSON is not exact: %v", err)
	}
	var bundle postgresQualityMaterialBundle
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return decodedPostgresQualityMaterials{}, 0, corruptPostgresResolver("material bundle has an unknown or invalid member: %v", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return decodedPostgresQualityMaterials{}, 0, corruptPostgresResolver("material bundle has trailing JSON: %v", err)
	}
	if bundle.InputManifests == nil || len(bundle.InputManifests) < 1 || len(bundle.InputManifests) > workflowinputauthority.MaximumManifests ||
		bundle.Revisions == nil || len(bundle.Revisions) < 1 || len(bundle.Revisions) > workflowinputauthority.MaximumRevisions ||
		bundle.ReviewReceipts == nil || len(bundle.ReviewReceipts) > workflowinputauthority.MaximumReviewReceipts {
		return decodedPostgresQualityMaterials{}, 0, corruptPostgresResolver("material bundle collection presence or bounds are invalid")
	}

	decoded := decodedPostgresQualityMaterials{
		inputManifests: make([]workflowinputauthority.InputManifestMaterial, 0, len(bundle.InputManifests)),
		revisions:      make([]workflowinputauthority.RevisionMaterial, 0, len(bundle.Revisions)),
		reviewReceipts: make([]workflowinputauthority.ReviewReceiptMaterial, 0, len(bundle.ReviewReceipts)),
	}
	total := 0
	previous := ""
	for index, material := range bundle.InputManifests {
		key := material.Role + "\x00" + material.ManifestID
		if (index > 0 && previous >= key) || !validManifestMaterialRole(material.Role) || !canonicalUUIDv4(material.ManifestID) {
			return decodedPostgresQualityMaterials{}, 0, corruptPostgresResolver("manifest materials are not sorted, unique, or closed")
		}
		raw, err := decodePostgresQualityRawMaterial(material.RawBytesHex, material.RawBytesHash, material.RawBytesSize, workflowinputauthority.MaximumManifestBytes)
		if err != nil {
			return decodedPostgresQualityMaterials{}, 0, fmt.Errorf("manifest material %d: %w", index, err)
		}
		if len(raw) > workflowinputauthority.MaximumRetainedBytes-total {
			return decodedPostgresQualityMaterials{}, 0, corruptPostgresResolver("material bundle exceeds the retained byte bound")
		}
		total += len(raw)
		decoded.inputManifests = append(decoded.inputManifests, workflowinputauthority.InputManifestMaterial{
			Bytes: raw, ManifestID: material.ManifestID, Role: material.Role,
		})
		previous = key
	}

	previous = ""
	for index, material := range bundle.Revisions {
		key := material.Purpose + "\x00" + material.RevisionID
		if (index > 0 && previous >= key) || !validEnvelopeString(material.Purpose, 256) || !canonicalUUIDv4(material.RevisionID) {
			return decodedPostgresQualityMaterials{}, 0, corruptPostgresResolver("revision materials are not sorted, unique, or closed")
		}
		raw, err := decodePostgresQualityRawMaterial(material.RawBytesHex, material.RawBytesHash, material.RawBytesSize, workflowinputauthority.MaximumRevisionBytes)
		if err != nil {
			return decodedPostgresQualityMaterials{}, 0, fmt.Errorf("revision material %d: %w", index, err)
		}
		if len(raw) > workflowinputauthority.MaximumRetainedBytes-total {
			return decodedPostgresQualityMaterials{}, 0, corruptPostgresResolver("material bundle exceeds the retained byte bound")
		}
		total += len(raw)
		decoded.revisions = append(decoded.revisions, workflowinputauthority.RevisionMaterial{
			Bytes: raw, Purpose: material.Purpose, RevisionID: material.RevisionID,
		})
		previous = key
	}

	previous = ""
	for index, material := range bundle.ReviewReceipts {
		if (index > 0 && previous >= material.ReviewRequestID) || !canonicalUUIDv4(material.ReviewRequestID) {
			return decodedPostgresQualityMaterials{}, 0, corruptPostgresResolver("review receipt materials are not sorted, unique, or closed")
		}
		raw, err := decodePostgresQualityRawMaterial(material.RawBytesHex, material.RawBytesHash, material.RawBytesSize, workflowinputauthority.MaximumReviewReceiptBytes)
		if err != nil {
			return decodedPostgresQualityMaterials{}, 0, fmt.Errorf("review receipt material %d: %w", index, err)
		}
		if len(raw) > workflowinputauthority.MaximumRetainedBytes-total {
			return decodedPostgresQualityMaterials{}, 0, corruptPostgresResolver("material bundle exceeds the retained byte bound")
		}
		total += len(raw)
		decoded.reviewReceipts = append(decoded.reviewReceipts, workflowinputauthority.ReviewReceiptMaterial{
			Bytes: raw, ReviewRequestID: material.ReviewRequestID,
		})
		previous = material.ReviewRequestID
	}
	return decoded, total, nil
}

func decodePostgresQualityRawMaterial(encoded, expectedHash string, expectedSize int64, maximum int) ([]byte, error) {
	if len(encoded) < 2 || len(encoded)%2 != 0 || len(encoded) > 2*maximum || encoded != strings.ToLower(encoded) ||
		expectedSize < 1 || expectedSize > int64(maximum) || !validRawDigest(expectedHash) {
		return nil, corruptPostgresResolver("raw material hexadecimal, digest, or size is invalid")
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil || int64(len(raw)) != expectedSize || workflowinputauthority.RawSHA256(raw) != expectedHash {
		return nil, corruptPostgresResolver("raw material bytes do not match their exact size and digest")
	}
	return raw, nil
}

func requireBoundedRaw(name string, raw []byte, maximum int) error {
	if len(raw) < 1 || len(raw) > maximum {
		return corruptPostgresResolver("%s raw bytes are absent or oversized", name)
	}
	return nil
}

func validManifestMaterialRole(value string) bool {
	switch value {
	case workflowinputauthority.ManifestRoleRun,
		workflowinputauthority.ManifestRolePredecessor,
		workflowinputauthority.ManifestRoleNode,
		workflowinputauthority.ManifestRoleQualification:
		return true
	default:
		return false
	}
}

func validRawDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func addBoundedRawSizes(values ...int) (int, bool) {
	total := 0
	for _, value := range values {
		if value < 0 || value > workflowinputauthority.MaximumRetainedBytes-total {
			return 0, true
		}
		total += value
	}
	return total, false
}

func cloneResolverBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}

func corruptPostgresResolver(format string, arguments ...any) error {
	detail := fmt.Sprintf(format, arguments...)
	return errors.Join(ErrConflict, workflowinputauthority.ErrCorrupt, fmt.Errorf("PostgreSQL Quality completion resolver: %s", detail))
}

func postgresResolverErrorIsDatabase(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func classifyPostgresResolverError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "WQC02" {
		return errors.Join(ErrConflict, workflowinputauthority.ErrCorrupt, fmt.Errorf("%s: %w", operation, err))
	}
	classified := classifyPostgresError(err)
	if errors.Is(classified, context.Canceled) || errors.Is(classified, context.DeadlineExceeded) {
		return classified
	}
	return errors.Join(classified, fmt.Errorf("%s: %w", operation, err))
}
