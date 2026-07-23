package workflowqualificationactivation

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/workflowinputauthority"
)

const (
	resolverTestPrecommitID = "20202020-2020-4020-8020-202020202020"
	resolverTestManifestID  = "30303030-3030-4030-8030-303030303030"
	resolverTestRevisionID  = "40404040-4040-4040-8040-404040404040"
	resolverTestReviewID    = "50505050-5050-4050-8050-505050505050"
)

func TestPostgresResolverUsesOneClosedCapabilityQuery(t *testing.T) {
	harness := &qualityResolverSQLHarness{values: qualityResolverNonTargetValues(testCompletionEventID)}
	database := sql.OpenDB(qualityResolverSQLConnector{harness: harness})
	t.Cleanup(func() { _ = database.Close() })
	resolver, err := NewPostgresResolver(database)
	if err != nil {
		t.Fatal(err)
	}
	eventID, _ := ParseCompletionEventID(testCompletionEventID)

	resolution, err := resolver.Resolve(context.Background(), eventID)
	if err != nil || resolution.Classification != ClassificationNonTarget ||
		!reflect.DeepEqual(resolution.Candidate, workflowinputauthority.Candidate{}) {
		t.Fatalf("Resolve() = %#v, %v", resolution, err)
	}
	query, arguments, rowCloses := harness.snapshot()
	if harness.calls != 1 || !strings.Contains(query, "resolve_workflow_v3_quality_completion_candidate_v1($1)") ||
		strings.Contains(query, "?") || len(arguments) != 1 || arguments[0].Value != testCompletionEventID || rowCloses != 1 {
		t.Fatalf("query calls=%d query=%q arguments=%#v rowCloses=%d", harness.calls, query, arguments, rowCloses)
	}
	if countResolverSelectColumns(query) != 16 {
		t.Fatalf("resolver projection is not the frozen 16-column ABI: %s", query)
	}
}

func TestReadExactPostgresQualityCompletionCandidateRows(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		_, err := readExactPostgresQualityCompletionCandidateRow(&qualityResolverFakeRows{})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing row error = %v", err)
		}
	})
	t.Run("one row", func(t *testing.T) {
		rows := &qualityResolverFakeRows{values: [][]driver.Value{qualityResolverNonTargetValues(testCompletionEventID)}}
		wire, err := readExactPostgresQualityCompletionCandidateRow(rows)
		if err != nil || !wire.classification.Valid || wire.classification.String != string(ClassificationNonTarget) ||
			!wire.candidateColumnsAreNull() {
			t.Fatalf("wire = %#v, %v", wire, err)
		}
	})
	t.Run("more than one row", func(t *testing.T) {
		values := qualityResolverNonTargetValues(testCompletionEventID)
		rows := &qualityResolverFakeRows{values: [][]driver.Value{values, values}}
		_, err := readExactPostgresQualityCompletionCandidateRow(rows)
		assertResolverCorrupt(t, err)
	})
	t.Run("terminal PostgreSQL error", func(t *testing.T) {
		rows := &qualityResolverFakeRows{terminalErr: &pgconn.PgError{Code: "40001"}}
		_, err := readExactPostgresQualityCompletionCandidateRow(rows)
		if !errors.Is(err, ErrRetryable) {
			t.Fatalf("terminal error = %v", err)
		}
	})
	t.Run("ABI scan drift", func(t *testing.T) {
		rows := &qualityResolverFakeRows{values: [][]driver.Value{{"only-one-column"}}}
		_, err := readExactPostgresQualityCompletionCandidateRow(rows)
		assertResolverCorrupt(t, err)
	})
}

func TestDecodePostgresQualityCompletionCandidateClassificationIsClosed(t *testing.T) {
	eventID, _ := ParseCompletionEventID(testCompletionEventID)
	nonTarget := postgresQualityCompletionCandidateWire{
		classification:    sql.NullString{String: string(ClassificationNonTarget), Valid: true},
		completionEventID: sql.NullString{String: eventID.String(), Valid: true},
	}
	resolution, err := decodePostgresQualityCompletionCandidate(nonTarget, eventID, workflowinputauthority.Compile)
	if err != nil || resolution.Classification != ClassificationNonTarget {
		t.Fatalf("non-target = %#v, %v", resolution, err)
	}

	tests := []struct {
		name   string
		mutate func(*postgresQualityCompletionCandidateWire)
	}{
		{name: "unknown classification", mutate: func(wire *postgresQualityCompletionCandidateWire) {
			wire.classification.String = "not_target"
		}},
		{name: "different event", mutate: func(wire *postgresQualityCompletionCandidateWire) {
			wire.completionEventID.String = "11111111-1111-4111-8111-111111111111"
		}},
		{name: "smuggled target column", mutate: func(wire *postgresQualityCompletionCandidateWire) {
			wire.freezeRequestBytes = []byte{}
		}},
		{name: "target with absent facts", mutate: func(wire *postgresQualityCompletionCandidateWire) {
			wire.classification.String = string(ClassificationTarget)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire := nonTarget
			test.mutate(&wire)
			_, err := decodePostgresQualityCompletionCandidate(wire, eventID, workflowinputauthority.Compile)
			assertResolverCorrupt(t, err)
		})
	}
}

func TestDecodePostgresQualityCompletionCandidateRebuildsAndCompilesTarget(t *testing.T) {
	eventID, _ := ParseCompletionEventID(testCompletionEventID)
	requestBytes := []byte(`{"request":"exact"}`)
	inputBytes := []byte(`{"input":"exact"}`)
	documentBytes := []byte(`{"candidate":"exact"}`)
	topMaterials := [][]byte{
		[]byte(`{"definition":1}`),
		[]byte(`{"scope":1}`),
		[]byte(`{"nodeInput":1}`),
		[]byte(`{"buildManifest":1}`),
		[]byte(`{"buildContract":1}`),
	}
	manifestRaw := []byte(`{"manifest":1}`)
	revisionRaw := []byte(`{"revision":1}`)
	receiptRaw := []byte(`{"receipt":1}`)
	materialBundle := mustMarshalResolverBundle(t, postgresQualityMaterialBundle{
		InputManifests: []postgresQualityManifestMaterial{qualityResolverManifestMaterial(
			resolverTestManifestID, workflowinputauthority.ManifestRoleRun, manifestRaw,
		)},
		Revisions: []postgresQualityRevisionMaterial{qualityResolverRevisionMaterial(
			resolverTestRevisionID, "workspace-target", revisionRaw,
		)},
		ReviewReceipts: []postgresQualityReceiptMaterial{qualityResolverReceiptMaterial(
			resolverTestReviewID, receiptRaw,
		)},
	})
	total := len(manifestRaw) + len(revisionRaw) + len(receiptRaw)
	for _, raw := range topMaterials {
		total += len(raw)
	}
	wire := postgresQualityCompletionCandidateWire{
		classification:        sql.NullString{String: string(ClassificationTarget), Valid: true},
		completionEventID:     sql.NullString{String: eventID.String(), Valid: true},
		precommitID:           sql.NullString{String: resolverTestPrecommitID, Valid: true},
		freezeRequestHash:     sql.NullString{String: workflowinputauthority.RawSHA256(requestBytes), Valid: true},
		freezeRequestBytes:    requestBytes,
		workflowInputHash:     sql.NullString{String: workflowinputauthority.RawSHA256(inputBytes), Valid: true},
		workflowInputBytes:    inputBytes,
		freezeCandidateBytes:  documentBytes,
		definitionRawBytes:    topMaterials[0],
		runScopeRawBytes:      topMaterials[1],
		nodeInputRawBytes:     topMaterials[2],
		buildManifestRawBytes: topMaterials[3],
		buildContractRawBytes: topMaterials[4],
		materialBundle:        materialBundle,
		snapshotHash:          sql.NullString{String: workflowinputauthority.RawSHA256([]byte("snapshot")), Valid: true},
		retainedRawBytesSize:  sql.NullInt64{Int64: int64(total), Valid: true},
	}
	request := workflowinputauthority.FreezeRequest{OperationID: "60606060-6060-4060-8060-606060606060"}
	input := workflowinputauthority.WorkflowInputDocument{}
	document := workflowinputauthority.FreezeCandidateDocument{ManifestSubject: "typed"}
	decodeCalls := 0
	compileCalls := 0
	resolution, err := decodePostgresQualityCompletionCandidateWithCodecs(wire, eventID, postgresQualityCandidateCodecs{
		decodeRequest: func(raw []byte, hash string) (workflowinputauthority.FreezeRequest, error) {
			decodeCalls++
			if !bytes.Equal(raw, requestBytes) || hash != wire.freezeRequestHash.String {
				t.Fatal("request decoder received different bytes or hash")
			}
			return request, nil
		},
		decodeInput: func(raw []byte, hash string) (workflowinputauthority.WorkflowInputDocument, error) {
			decodeCalls++
			if !bytes.Equal(raw, inputBytes) || hash != wire.workflowInputHash.String {
				t.Fatal("input decoder received different bytes or hash")
			}
			return input, nil
		},
		decodeDocument: func(raw []byte) (workflowinputauthority.FreezeCandidateDocument, error) {
			decodeCalls++
			if !bytes.Equal(raw, documentBytes) {
				t.Fatal("candidate decoder received different bytes")
			}
			return document, nil
		},
		compile: func(candidate workflowinputauthority.Candidate) (workflowinputauthority.Record, error) {
			compileCalls++
			if candidate.Request != request || candidate.Document.ManifestSubject != document.ManifestSubject ||
				!bytes.Equal(candidate.Materials.Definition, topMaterials[0]) ||
				len(candidate.Materials.InputManifests) != 1 || !bytes.Equal(candidate.Materials.InputManifests[0].Bytes, manifestRaw) ||
				len(candidate.Materials.Revisions) != 1 || !bytes.Equal(candidate.Materials.Revisions[0].Bytes, revisionRaw) ||
				len(candidate.Materials.ReviewReceipts) != 1 || !bytes.Equal(candidate.Materials.ReviewReceipts[0].Bytes, receiptRaw) {
				t.Fatalf("compiler received different Candidate: %#v", candidate)
			}
			return qualityResolverCompiledRecord(), nil
		},
	})
	if err != nil || resolution.Classification != ClassificationTarget || decodeCalls != 3 || compileCalls != 1 ||
		resolution.Candidate.Request != request || resolution.Candidate.Document.ManifestSubject != "typed" {
		t.Fatalf("target = %#v err=%v decodeCalls=%d compileCalls=%d", resolution, err, decodeCalls, compileCalls)
	}
	if _, err := decodePostgresQualityCompletionCandidate(wire, eventID, workflowinputauthority.Compile); err == nil {
		t.Fatal("non-authority request/input/candidate bytes passed the production strict decoders")
	} else {
		assertResolverCorrupt(t, err)
	}

	wire.retainedRawBytesSize.Int64++
	if _, err := decodePostgresQualityCompletionCandidateWithCodecs(wire, eventID, postgresQualityCandidateCodecs{
		decodeRequest:  func([]byte, string) (workflowinputauthority.FreezeRequest, error) { return request, nil },
		decodeInput:    func([]byte, string) (workflowinputauthority.WorkflowInputDocument, error) { return input, nil },
		decodeDocument: func([]byte) (workflowinputauthority.FreezeCandidateDocument, error) { return document, nil },
		compile: func(workflowinputauthority.Candidate) (workflowinputauthority.Record, error) {
			t.Fatal("byte-total drift reached Compile")
			return workflowinputauthority.Record{}, nil
		},
	}); err == nil {
		t.Fatal("retained byte-total drift was accepted")
	} else {
		assertResolverCorrupt(t, err)
	}
}

func TestDecodePostgresQualityMaterialBundleReconstructsExactSortedChildren(t *testing.T) {
	manifestRaw := []byte(`{"manifest":1}`)
	revisionRaw := []byte(`{"revision":1}`)
	receiptRaw := []byte(`{"receipt":1}`)
	bundle := postgresQualityMaterialBundle{
		InputManifests: []postgresQualityManifestMaterial{qualityResolverManifestMaterial(
			resolverTestManifestID, workflowinputauthority.ManifestRoleRun, manifestRaw,
		)},
		Revisions: []postgresQualityRevisionMaterial{qualityResolverRevisionMaterial(
			resolverTestRevisionID, "workspace-target", revisionRaw,
		)},
		ReviewReceipts: []postgresQualityReceiptMaterial{qualityResolverReceiptMaterial(
			resolverTestReviewID, receiptRaw,
		)},
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	decoded, total, err := decodePostgresQualityMaterialBundle(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if total != len(manifestRaw)+len(revisionRaw)+len(receiptRaw) ||
		len(decoded.inputManifests) != 1 || !bytes.Equal(decoded.inputManifests[0].Bytes, manifestRaw) ||
		len(decoded.revisions) != 1 || !bytes.Equal(decoded.revisions[0].Bytes, revisionRaw) ||
		len(decoded.reviewReceipts) != 1 || !bytes.Equal(decoded.reviewReceipts[0].Bytes, receiptRaw) {
		t.Fatalf("decoded = %#v total=%d", decoded, total)
	}
}

func TestDecodePostgresQualityMaterialBundleFailsClosed(t *testing.T) {
	rawA := []byte(`{"a":1}`)
	rawB := []byte(`{"b":2}`)
	valid := postgresQualityMaterialBundle{
		InputManifests: []postgresQualityManifestMaterial{
			qualityResolverManifestMaterial("30303030-3030-4030-8030-303030303030", workflowinputauthority.ManifestRolePredecessor, rawA),
			qualityResolverManifestMaterial("31313131-3131-4131-8131-313131313131", workflowinputauthority.ManifestRoleRun, rawB),
		},
		Revisions: []postgresQualityRevisionMaterial{
			qualityResolverRevisionMaterial("40404040-4040-4040-8040-404040404040", "a", rawA),
			qualityResolverRevisionMaterial("41414141-4141-4141-8141-414141414141", "b", rawB),
		},
		ReviewReceipts: []postgresQualityReceiptMaterial{},
	}

	tests := []struct {
		name    string
		encoded func(t *testing.T) []byte
	}{
		{name: "unknown root member", encoded: func(t *testing.T) []byte {
			return []byte(`{"inputManifests":[],"revisions":[],"reviewReceipts":[],"future":true}`)
		}},
		{name: "duplicate root member", encoded: func(t *testing.T) []byte {
			return []byte(`{"inputManifests":[],"inputManifests":[],"revisions":[],"reviewReceipts":[]}`)
		}},
		{name: "null collection", encoded: func(t *testing.T) []byte {
			return []byte(`{"inputManifests":null,"revisions":[],"reviewReceipts":[]}`)
		}},
		{name: "uppercase hex", encoded: func(t *testing.T) []byte {
			copy := valid
			copy.InputManifests = append([]postgresQualityManifestMaterial(nil), valid.InputManifests...)
			copy.InputManifests[0].RawBytesHex = strings.ToUpper(copy.InputManifests[0].RawBytesHex)
			return mustMarshalResolverBundle(t, copy)
		}},
		{name: "digest mismatch", encoded: func(t *testing.T) []byte {
			copy := valid
			copy.Revisions = append([]postgresQualityRevisionMaterial(nil), valid.Revisions...)
			copy.Revisions[0].RawBytesHash = workflowinputauthority.RawSHA256([]byte("different"))
			return mustMarshalResolverBundle(t, copy)
		}},
		{name: "size mismatch", encoded: func(t *testing.T) []byte {
			copy := valid
			copy.Revisions = append([]postgresQualityRevisionMaterial(nil), valid.Revisions...)
			copy.Revisions[0].RawBytesSize++
			return mustMarshalResolverBundle(t, copy)
		}},
		{name: "unsorted manifests", encoded: func(t *testing.T) []byte {
			copy := valid
			copy.InputManifests = []postgresQualityManifestMaterial{valid.InputManifests[1], valid.InputManifests[0]}
			return mustMarshalResolverBundle(t, copy)
		}},
		{name: "duplicate revisions", encoded: func(t *testing.T) []byte {
			copy := valid
			copy.Revisions = []postgresQualityRevisionMaterial{valid.Revisions[0], valid.Revisions[0]}
			return mustMarshalResolverBundle(t, copy)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := decodePostgresQualityMaterialBundle(test.encoded(t))
			assertResolverCorrupt(t, err)
		})
	}
}

func qualityResolverCompiledRecord() workflowinputauthority.Record {
	record := workflowinputauthority.Record{}
	record.OperationID = mustResolverUUID("60606060-6060-4060-8060-606060606060")
	record.AuthorityID = mustResolverUUID("61616161-6161-4161-8161-616161616161")
	record.WorkflowRunID = mustResolverUUID("62626262-6262-4262-8262-626262626262")
	record.NodeRunID = mustResolverUUID("63636363-6363-4363-8363-636363636363")
	record.Input.Gate.ActivationEventID = "64646464-6464-4464-8464-646464646464"
	return record
}

func mustResolverUUID(value string) uuid.UUID {
	return uuid.MustParse(value)
}

func TestPostgresQualityResolverSQLStateClassification(t *testing.T) {
	tests := []struct {
		code  string
		wants []error
	}{
		{code: "WQC01", wants: []error{ErrInvalid}},
		{code: "WQC02", wants: []error{ErrConflict, workflowinputauthority.ErrCorrupt}},
		{code: "WQC03", wants: []error{ErrConflict}},
		{code: "WQC04", wants: []error{ErrConflict}},
		{code: "40001", wants: []error{ErrRetryable}},
		{code: "40P01", wants: []error{ErrRetryable}},
		{code: "42P01", wants: []error{ErrNotReady}},
		{code: "XX000", wants: []error{ErrOutcomeUnknown}},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			err := classifyPostgresResolverError("test", &pgconn.PgError{Code: test.code})
			for _, want := range test.wants {
				if !errors.Is(err, want) {
					t.Fatalf("error = %v, want %v", err, want)
				}
			}
		})
	}
	if err := classifyPostgresResolverError("test", context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context = %v", err)
	}
	if _, err := NewPostgresResolver(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil database = %v", err)
	}
}

func qualityResolverManifestMaterial(id, role string, raw []byte) postgresQualityManifestMaterial {
	return postgresQualityManifestMaterial{
		ManifestID: id, RawBytesHash: workflowinputauthority.RawSHA256(raw), RawBytesHex: hex.EncodeToString(raw),
		RawBytesSize: int64(len(raw)), Role: role,
	}
}

func qualityResolverRevisionMaterial(id, purpose string, raw []byte) postgresQualityRevisionMaterial {
	return postgresQualityRevisionMaterial{
		Purpose: purpose, RawBytesHash: workflowinputauthority.RawSHA256(raw), RawBytesHex: hex.EncodeToString(raw),
		RawBytesSize: int64(len(raw)), RevisionID: id,
	}
}

func qualityResolverReceiptMaterial(id string, raw []byte) postgresQualityReceiptMaterial {
	return postgresQualityReceiptMaterial{
		RawBytesHash: workflowinputauthority.RawSHA256(raw), RawBytesHex: hex.EncodeToString(raw),
		RawBytesSize: int64(len(raw)), ReviewRequestID: id,
	}
}

func mustMarshalResolverBundle(t *testing.T, bundle postgresQualityMaterialBundle) []byte {
	t.Helper()
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertResolverCorrupt(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrConflict) || !errors.Is(err, workflowinputauthority.ErrCorrupt) {
		t.Fatalf("error = %v, want ErrConflict + workflowinputauthority.ErrCorrupt", err)
	}
}

func qualityResolverNonTargetValues(eventID string) []driver.Value {
	return []driver.Value{
		string(ClassificationNonTarget), eventID,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	}
}

func countResolverSelectColumns(query string) int {
	start := strings.Index(query, "SELECT")
	end := strings.Index(query, "FROM resolve_workflow")
	if start < 0 || end < 0 || end <= start {
		return 0
	}
	projection := query[start+len("SELECT") : end]
	return len(strings.Split(projection, ","))
}

type qualityResolverFakeRows struct {
	values      [][]driver.Value
	index       int
	current     int
	terminalErr error
	closed      bool
}

func (rows *qualityResolverFakeRows) Next() bool {
	if rows.index >= len(rows.values) {
		return false
	}
	rows.current = rows.index
	rows.index++
	return true
}

func (rows *qualityResolverFakeRows) Scan(destinations ...any) error {
	if rows.current < 0 || rows.current >= len(rows.values) {
		return errors.New("Scan called without a current row")
	}
	values := rows.values[rows.current]
	if len(values) != len(destinations) {
		return fmt.Errorf("column count %d, destination count %d", len(values), len(destinations))
	}
	for index, value := range values {
		switch destination := destinations[index].(type) {
		case *sql.NullString:
			if err := destination.Scan(value); err != nil {
				return err
			}
		case *sql.NullInt64:
			if err := destination.Scan(value); err != nil {
				return err
			}
		case *[]byte:
			if value == nil {
				*destination = nil
				continue
			}
			raw, ok := value.([]byte)
			if !ok {
				return fmt.Errorf("column %d value type %T is not []byte", index, value)
			}
			*destination = append((*destination)[:0], raw...)
		default:
			return fmt.Errorf("destination %d type %T is unsupported", index, destination)
		}
	}
	return nil
}

func (rows *qualityResolverFakeRows) Err() error {
	if rows.index >= len(rows.values) {
		return rows.terminalErr
	}
	return nil
}

func (rows *qualityResolverFakeRows) Close() error {
	rows.closed = true
	return nil
}

type qualityResolverSQLHarness struct {
	mu        sync.Mutex
	values    []driver.Value
	query     string
	arguments []driver.NamedValue
	calls     int
	rowCloses int
}

func (harness *qualityResolverSQLHarness) snapshot() (string, []driver.NamedValue, int) {
	harness.mu.Lock()
	defer harness.mu.Unlock()
	return harness.query, append([]driver.NamedValue(nil), harness.arguments...), harness.rowCloses
}

type qualityResolverSQLConnector struct{ harness *qualityResolverSQLHarness }

func (connector qualityResolverSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return &qualityResolverSQLConnection{harness: connector.harness}, nil
}

func (connector qualityResolverSQLConnector) Driver() driver.Driver {
	return qualityResolverSQLDriver{harness: connector.harness}
}

type qualityResolverSQLDriver struct{ harness *qualityResolverSQLHarness }

func (value qualityResolverSQLDriver) Open(string) (driver.Conn, error) {
	return &qualityResolverSQLConnection{harness: value.harness}, nil
}

type qualityResolverSQLConnection struct{ harness *qualityResolverSQLHarness }

func (*qualityResolverSQLConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare is not supported")
}
func (*qualityResolverSQLConnection) Close() error { return nil }
func (*qualityResolverSQLConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}
func (connection *qualityResolverSQLConnection) QueryContext(
	_ context.Context,
	query string,
	arguments []driver.NamedValue,
) (driver.Rows, error) {
	connection.harness.mu.Lock()
	connection.harness.calls++
	connection.harness.query = query
	connection.harness.arguments = append([]driver.NamedValue(nil), arguments...)
	values := append([]driver.Value(nil), connection.harness.values...)
	connection.harness.mu.Unlock()
	return &qualityResolverSQLRows{harness: connection.harness, values: values}, nil
}

type qualityResolverSQLRows struct {
	harness *qualityResolverSQLHarness
	values  []driver.Value
	done    bool
}

func (*qualityResolverSQLRows) Columns() []string {
	return []string{
		"classification", "completion_event_id", "precommit_id", "freeze_request_hash",
		"freeze_request_bytes", "workflow_input_hash", "workflow_input_bytes", "freeze_candidate_bytes",
		"definition_raw_bytes", "run_scope_raw_bytes", "node_input_raw_bytes", "build_manifest_raw_bytes",
		"build_contract_raw_bytes", "material_bundle", "snapshot_hash", "retained_raw_bytes_size",
	}
}

func (rows *qualityResolverSQLRows) Close() error {
	rows.harness.mu.Lock()
	rows.harness.rowCloses++
	rows.harness.mu.Unlock()
	return nil
}

func (rows *qualityResolverSQLRows) Next(destination []driver.Value) error {
	if rows.done {
		return io.EOF
	}
	copy(destination, rows.values)
	rows.done = true
	return nil
}

var _ driver.Connector = qualityResolverSQLConnector{}
var _ driver.QueryerContext = (*qualityResolverSQLConnection)(nil)
