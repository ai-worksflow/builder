package workflowinputauthority

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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresStoreFreezeUsesCallerTransactionAndPrivateFactsOnly(t *testing.T) {
	candidate := goldenCandidate(t)
	proposed, err := Compile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	frozenAt := time.Date(2026, 7, 19, 4, 5, 6, 789000000, time.UTC)
	bundle := postgresRecoveryBundleForRecord(t, proposed, frozenAt)
	harness := &postgresTestHarness{authorityID: proposed.AuthorityID.String(), bundle: bundle}
	database := openPostgresTestDatabase(t, harness)
	store, err := NewPostgresStore(database)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := NewPostgresTransaction(transaction)
	if err != nil {
		t.Fatal(err)
	}

	stored, err := store.Freeze(context.Background(), wrapped, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !sameImmutableRecord(stored, proposed) || !stored.FrozenAt.Equal(frozenAt) {
		t.Fatalf("stored Record differs from compiled/recovered authority: %#v", stored)
	}
	if harness.commits.Load() != 0 || harness.rollbacks.Load() != 0 {
		t.Fatalf("Store changed caller transaction: commits=%d rollbacks=%d", harness.commits.Load(), harness.rollbacks.Load())
	}
	queries, arguments := harness.snapshot()
	if len(queries) != 4 || !strings.Contains(queries[0], "pg_advisory_xact_lock_shared") ||
		!strings.Contains(queries[1], "inspect_workflow_input_authority_operation_v1") ||
		!strings.Contains(queries[2], "freeze_workflow_input_authority_from_quality_precommit_v1") ||
		strings.Contains(queries[2], "FROM freeze_workflow_input_authority_v1(") ||
		!strings.Contains(queries[3], "resolve_workflow_input_authority_for_node_v1") {
		t.Fatalf("queries = %#v", queries)
	}
	if len(arguments[0]) != 1 || arguments[0][0] != workflowInputAuthorityMigrationAdvisoryKey {
		t.Fatalf("migration fence arguments = %#v", arguments[0])
	}
	if len(arguments[2]) != 13 {
		t.Fatalf("freeze argument count = %d, want exact migration signature", len(arguments[2]))
	}
	for index, want := range [][]byte{
		proposed.Materials.Definition, proposed.Materials.RunScope, proposed.Materials.NodeInput,
		proposed.Materials.BuildManifest, proposed.Materials.BuildContract,
	} {
		got, ok := arguments[2][7+index].([]byte)
		if !ok || !bytes.Equal(got, want) {
			t.Fatalf("raw material argument %d = %T %v", index, arguments[2][7+index], arguments[2][7+index])
		}
	}
	document, err := candidateDocumentFromRecord(proposed.Input, proposed.Materials)
	if err != nil {
		t.Fatal(err)
	}
	wantCandidate, err := EncodeFreezeCandidate(document)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := arguments[2][12].(string); !ok || got != string(wantCandidate) {
		t.Fatalf("private candidate argument = %T %v", arguments[2][12], arguments[2][12])
	}
	for _, forbidden := range []string{proposed.RequestHash, proposed.TargetHash, proposed.InputHash, proposed.AuthorityHash} {
		for _, argument := range arguments[2] {
			if value, ok := argument.(string); ok && value == forbidden {
				t.Fatalf("caller-derived public hash %s was sent to issuer", forbidden)
			}
		}
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if harness.rollbacks.Load() != 1 || harness.commits.Load() != 0 {
		t.Fatalf("caller rollback accounting = commits=%d rollbacks=%d", harness.commits.Load(), harness.rollbacks.Load())
	}
}

func TestPostgresStoreConcurrentExactWinnerIsReportedAsReplay(t *testing.T) {
	candidate := goldenCandidate(t)
	proposed, err := Compile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	bundle := postgresRecoveryBundleForRecord(
		t, proposed, time.Date(2026, 7, 19, 7, 8, 9, 123000000, time.UTC),
	)
	harness := &postgresTestHarness{
		authorityID: proposed.AuthorityID.String(), bundle: bundle, freezeReplay: true,
	}
	database := openPostgresTestDatabase(t, harness)
	store, err := NewPostgresStore(database)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	wrapped, err := NewPostgresTransaction(transaction)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.Freeze(context.Background(), wrapped, candidate)
	if err != nil || !replayed.Idempotent {
		t.Fatalf("concurrent exact winner = %#v, %v", replayed, err)
	}
}

func TestPostgresStoreExactReplayAndTransactionalCurrentAssertion(t *testing.T) {
	candidate := goldenCandidate(t)
	proposed, err := Compile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	bundle := postgresRecoveryBundleForRecord(
		t, proposed, time.Date(2026, 7, 19, 7, 8, 9, 123000000, time.UTC),
	)
	harness := &postgresTestHarness{
		authorityID: proposed.AuthorityID.String(), bundle: bundle, inspectBundle: bundle,
	}
	database := openPostgresTestDatabase(t, harness)
	store, err := NewPostgresStore(database)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := NewPostgresTransaction(transaction)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.Freeze(context.Background(), wrapped, candidate)
	if err != nil || !replayed.Idempotent {
		t.Fatalf("exact replay = %#v, %v", replayed, err)
	}
	if harness.commits.Load() != 0 || harness.rollbacks.Load() != 0 {
		t.Fatal("exact replay closed the caller transaction")
	}
	current, err := store.AssertCurrentTx(context.Background(), wrapped, proposed.AuthorityID)
	if err != nil || current.AuthorityID != proposed.AuthorityID {
		t.Fatalf("transactional current assertion = %#v, %v", current, err)
	}
	if harness.commits.Load() != 0 || harness.rollbacks.Load() != 0 {
		t.Fatal("current assertion released caller-owned locks by closing the transaction")
	}
	var typedNil *PostgresTransaction
	if _, err := store.AssertCurrentTx(context.Background(), typedNil, proposed.AuthorityID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed-nil transaction = %v", err)
	}
	if _, err := store.Freeze(context.Background(), typedNil, candidate); !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed-nil freeze transaction = %v", err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if harness.rollbacks.Load() != 1 || harness.commits.Load() != 0 {
		t.Fatalf("caller close accounting = commits=%d rollbacks=%d", harness.commits.Load(), harness.rollbacks.Load())
	}
}

func TestPostgresRecoveryBundleRejectsWideningAndChildDrift(t *testing.T) {
	t.Parallel()
	record := mustCompileGolden(t)
	bundle := postgresRecoveryBundleForRecord(t, record, time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC))
	resolved, err := scanPostgresWorkflowInputBundle(postgresStaticRow{value: bundle})
	if err != nil || !sameImmutableRecord(resolved, record) {
		t.Fatalf("exact recovery = %#v, %v", resolved, err)
	}

	var root map[string]any
	if err := json.Unmarshal(bundle, &root); err != nil {
		t.Fatal(err)
	}
	root["future"] = true
	widened, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanPostgresWorkflowInputBundle(postgresStaticRow{value: widened}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("widened recovery error = %v", err)
	}

	if err := json.Unmarshal(bundle, &root); err != nil {
		t.Fatal(err)
	}
	manifests := root["inputManifests"].([]any)
	manifest := manifests[0].(map[string]any)
	manifest["raw_bytes_hash"] = digest("0")
	drifted, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanPostgresWorkflowInputBundle(postgresStaticRow{value: drifted}); !errors.Is(err, ErrCorrupt) || !errors.Is(err, ErrConflict) {
		t.Fatalf("drifted recovery error = %v", err)
	}
}

func TestPostgresWorkflowInputSQLStateClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		code             string
		currentAssertion bool
		wants            []error
	}{
		{name: "conflict", code: "WIA01", wants: []error{ErrConflict}},
		{name: "corrupt", code: "WIA02", wants: []error{ErrConflict, ErrCorrupt}},
		{name: "stale corrupt", code: "WIA02", currentAssertion: true, wants: []error{ErrStale, ErrCorrupt}},
		{name: "invalid", code: "WIA03", wants: []error{ErrInvalid}},
		{name: "stale locked fact", code: "WIA04", wants: []error{ErrConflict, ErrStale}},
		{name: "serialization", code: "40001", wants: []error{ErrConflict}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := classifyPostgresWorkflowInputError("test", &pgconn.PgError{Code: test.code}, test.currentAssertion)
			for _, want := range test.wants {
				if !errors.Is(err, want) {
					t.Fatalf("classified error = %v, want %v", err, want)
				}
			}
		})
	}
	if got := classifyPostgresWorkflowInputError("test", sql.ErrNoRows, false); !errors.Is(got, ErrNotFound) {
		t.Fatalf("no rows = %v", got)
	}
	if _, err := NewPostgresTransaction(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil transaction = %v", err)
	}
}

type postgresStaticRow struct {
	value []byte
	err   error
}

func (row postgresStaticRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(destinations) != 1 {
		return fmt.Errorf("destination count %d", len(destinations))
	}
	destination, ok := destinations[0].(*[]byte)
	if !ok {
		return fmt.Errorf("destination type %T", destinations[0])
	}
	*destination = append((*destination)[:0], row.value...)
	return nil
}

func postgresRecoveryBundleForRecord(t *testing.T, record Record, frozenAt time.Time) []byte {
	t.Helper()
	startedAt, err := time.Parse(canonicalTimeLayout, record.Input.Run.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	targetArtifactID := ""
	for _, revision := range record.Input.Revisions {
		if revision.Purpose == RevisionPurposeWorkspaceTarget {
			targetArtifactID = revision.ArtifactID
		}
	}
	authority := postgresAuthorityWire{
		ActivationEventID: record.Input.Gate.ActivationEventID, ActivationEventSequence: record.Input.Gate.ActivationEventSequence,
		AuthorityBytes: postgresTestBytea(record.EnvelopeBytes), AuthorityDocument: record.EnvelopeBytes,
		AuthorityHash: record.AuthorityHash, AuthorityID: record.AuthorityID.String(),
		BuildContractContentHash: record.Input.Build.BuildContract.ContentHash,
		BuildContractHash:        record.Input.Build.BuildContract.ContractHash, BuildContractID: record.Input.Build.BuildContract.ID,
		BuildContractRawBytes:     postgresTestBytea(record.Materials.BuildContract),
		BuildContractRawBytesHash: record.Input.Build.BuildContract.RawBytesHash,
		BuildContractRawBytesSize: record.Input.Build.BuildContract.RawBytesSize,
		BuildContractStatus:       record.Input.Build.BuildContract.StatusAtFreeze,
		BuildManifestContentHash:  record.Input.Build.BuildManifest.ContentHash,
		BuildManifestHash:         record.Input.Build.BuildManifest.ManifestHash, BuildManifestID: record.Input.Build.BuildManifest.ID,
		BuildManifestRawBytes:     postgresTestBytea(record.Materials.BuildManifest),
		BuildManifestRawBytesHash: record.Input.Build.BuildManifest.RawBytesHash,
		BuildManifestRawBytesSize: record.Input.Build.BuildManifest.RawBytesSize,
		BuildManifestStatus:       record.Input.Build.BuildManifest.StatusAtFreeze,
		DefinitionHash:            record.Input.Definition.DefinitionHash, DefinitionID: record.Input.Definition.DefinitionID,
		DefinitionNodeID: record.Input.Gate.DefinitionNodeID, DefinitionRawBytes: postgresTestBytea(record.Materials.Definition),
		DefinitionRawBytesHash: record.Input.Definition.RawBytesHash, DefinitionRawBytesSize: record.Input.Definition.RawBytesSize,
		DefinitionVersion: record.Input.Definition.DefinitionVersion, DefinitionVersionID: record.Input.Definition.DefinitionVersionID,
		ExecutionProfileHash:    record.Input.Definition.ExecutionProfileHash,
		ExecutionProfileVersion: record.Input.Definition.ExecutionProfileVersion,
		ExternalGatePolicy:      record.Input.QualificationPolicy.ExternalGatePolicy,
		FrozenAt:                frozenAt, GateName: record.Input.Gate.GateName, GovernanceMode: record.Input.Project.GovernanceMode,
		InputBytes: postgresTestBytea(record.InputBytes), InputDocument: record.InputBytes, InputHash: record.InputHash,
		ManifestCount: int64(len(record.Input.InputManifests)), ManifestSubject: record.Input.Target.ManifestSubject,
		NodeInputBindingCount: record.Input.NodeInput.BindingCount, NodeInputRawBytes: postgresTestBytea(record.Materials.NodeInput),
		NodeInputRawBytesHash: record.Input.NodeInput.RawBytesHash, NodeInputRawBytesSize: record.Input.NodeInput.RawBytesSize,
		NodeInputSemanticHash: record.Input.NodeInput.SemanticHash, NodeKey: record.Input.Gate.NodeKey,
		NodeRunID: record.NodeRunID.String(), NodeType: record.Input.Gate.NodeType, OperationID: record.OperationID.String(),
		PredecessorCount: int64(len(record.Input.Predecessors)), ProjectID: record.Input.Project.ID,
		QualificationPolicyAuthorityHash: record.Input.QualificationPolicy.AuthorityHash,
		QualificationPolicyAuthorityID:   record.Input.QualificationPolicy.AuthorityID,
		QualityPassed:                    record.Input.QualityResult.Passed, QualityRunID: record.Input.QualityResult.QualityRunID,
		QualityWorkspaceRevisionContentHash: record.Input.QualityResult.WorkspaceRevisionContentHash,
		QualityWorkspaceRevisionID:          record.Input.QualityResult.WorkspaceRevisionID,
		RequestBytes:                        postgresTestBytea(record.RequestBytes), RequestDocument: record.RequestBytes, RequestHash: record.RequestHash,
		RevisionCount: int64(len(record.Input.Revisions)), ReviewReceiptCount: int64(len(record.Input.ReviewReceipts)),
		RunInputManifestHash: record.Input.Run.InputManifestHash, RunInputManifestID: record.Input.Run.InputManifestID,
		RunScopeRawBytes: postgresTestBytea(record.Materials.RunScope), RunScopeRawBytesHash: record.Input.Run.ScopeRawBytesHash,
		RunScopeRawBytesSize: record.Input.Run.ScopeRawBytesSize, RunStartedAt: startedAt, RunStartedBy: record.Input.Run.StartedBy,
		SliceID: postgresTestOptionalString(record.Input.Gate.SliceIdentity.ID), SliceKind: record.Input.Gate.SliceIdentity.Kind,
		StageGate: record.Input.Gate.StageGate, TargetArtifactID: targetArtifactID,
		TargetBytes: postgresTestBytea(record.TargetBytes), TargetDocument: record.TargetBytes, TargetHash: record.TargetHash,
		TargetRevisionContentHash: record.Input.Target.TargetRevisionContentHash,
		TargetRevisionID:          record.Input.Target.TargetRevisionID, WorkflowRunID: record.WorkflowRunID.String(),
	}

	reservations := []postgresIdentityReservationWire{
		{AuthorityID: record.AuthorityID.String(), IdentityKind: "activation-event", IdentityValue: record.Input.Gate.ActivationEventID, ReservedAt: frozenAt},
		{AuthorityID: record.AuthorityID.String(), IdentityKind: "authority", IdentityValue: record.AuthorityID.String(), ReservedAt: frozenAt},
		{AuthorityID: record.AuthorityID.String(), IdentityKind: "freeze-operation", IdentityValue: record.OperationID.String(), ReservedAt: frozenAt},
	}
	predecessors := make([]postgresPredecessorWire, len(record.Input.Predecessors))
	for index, member := range record.Input.Predecessors {
		predecessors[index] = postgresPredecessorWire{
			AuthorityID: record.AuthorityID.String(), EdgeID: member.EdgeID, MappingKind: member.MappingKind,
			MappingOrdinal: member.MappingOrdinal, MemberDocument: postgresTestJSON(t, member), Ordinal: int64(index),
			OutputHash: member.OutputHash, OutputRevisionNumber: member.OutputRevisionNumber,
			SourceDefinitionNodeID: member.SourceDefinitionNodeID, SourceNodeKey: member.SourceNodeKey,
			SourceNodeRunID: member.SourceNodeRunID, SourceNodeType: member.SourceNodeType, SourcePort: member.SourcePort,
			SourceSliceID: postgresTestOptionalString(member.SourceSliceIdentity.ID), SourceSliceKind: member.SourceSliceIdentity.Kind,
			SourceStatus: member.SourceStatus, TargetPort: member.TargetPort, ValueHash: member.ValueHash,
		}
	}
	manifests := make([]postgresManifestWire, len(record.Input.InputManifests))
	for index, member := range record.Input.InputManifests {
		raw := record.Materials.InputManifests[index].Bytes
		manifests[index] = postgresManifestWire{
			AuthorityID: record.AuthorityID.String(), ContentHash: member.ContentHash, ContentRef: member.ContentRef,
			ContentStore: member.ContentStore, Kind: member.Kind, ManifestHash: member.ManifestHash, ManifestID: member.ID,
			MemberDocument: postgresTestJSON(t, member), Ordinal: int64(index), ProjectID: member.ProjectID,
			RawBytes: postgresTestBytea(raw), RawBytesHash: member.RawBytesHash, RawBytesSize: member.RawBytesSize,
			Role: member.Role, SchemaVersion: member.SchemaVersion,
		}
	}
	revisions := make([]postgresRevisionWire, len(record.Input.Revisions))
	for index, member := range record.Input.Revisions {
		raw := record.Materials.Revisions[index].Bytes
		revisions[index] = postgresRevisionWire{
			ArtifactID: member.ArtifactID, ArtifactKind: member.ArtifactKind, AuthorityID: record.AuthorityID.String(),
			ByteSize: member.ByteSize, CanonicalReviewRequired: member.CanonicalReviewRequired,
			ChangeSourceAtFreeze: member.ChangeSourceAtFreeze,
			ContentHash:          member.ContentHash, ContentRef: member.ContentRef,
			ContentStore: member.ContentStore, CurrencyPolicy: member.CurrencyPolicy,
			ImplementationProposalID: member.ImplementationProposalID, MemberDocument: postgresTestJSON(t, member),
			Ordinal: int64(index), ProposalID: member.ProposalID, Purpose: member.Purpose, RawBytes: postgresTestBytea(raw),
			RawBytesHash: member.RawBytesHash, RevisionID: member.RevisionID, SchemaVersion: member.SchemaVersion,
			SourceManifestID: member.SourceManifestID, SourceRequiredAtFreeze: member.SourceRequiredAtFreeze,
			WasLatestApproved: member.IsLatestApprovedAtFreeze,
			WasLatestCurrent:  member.IsLatestCurrentAtFreeze, WorkflowStatusAtFreeze: member.WorkflowStatusAtFreeze,
		}
	}
	receipts := make([]postgresReviewReceiptWire, len(record.Input.ReviewReceipts))
	for index, member := range record.Input.ReviewReceipts {
		raw := record.Materials.ReviewReceipts[index].Bytes
		receipts[index] = postgresReviewReceiptWire{
			ArtifactID: member.ArtifactID, AuthorityID: record.AuthorityID.String(), MemberDocument: postgresTestJSON(t, member),
			Ordinal: int64(index), ProjectID: member.ProjectID, Purpose: member.Purpose, ReceiptBytes: postgresTestBytea(raw),
			ReceiptDocument: raw, ReceiptHash: member.ReceiptHash, ReceiptRawBytesHash: member.ReceiptRawBytesHash,
			ReceiptRawBytesSize: member.ReceiptRawBytesSize, ReceiptSchemaVersion: member.ReceiptSchemaVersion,
			ReviewRequestID: member.ReviewRequestID, RevisionContentHash: member.RevisionContentHash, RevisionID: member.RevisionID,
		}
	}
	bundle := struct {
		Authority            postgresAuthorityWire             `json:"authority"`
		IdentityReservations []postgresIdentityReservationWire `json:"identityReservations"`
		InputManifests       []postgresManifestWire            `json:"inputManifests"`
		Predecessors         []postgresPredecessorWire         `json:"predecessors"`
		ReviewReceipts       []postgresReviewReceiptWire       `json:"reviewReceipts"`
		Revisions            []postgresRevisionWire            `json:"revisions"`
	}{authority, reservations, manifests, predecessors, receipts, revisions}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func postgresTestJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func postgresTestBytea(value []byte) string {
	return `\x` + hex.EncodeToString(value)
}

func postgresTestOptionalString(value string) *string {
	if value == "" {
		return nil
	}
	clone := value
	return &clone
}

type postgresTestHarness struct {
	mu            sync.Mutex
	authorityID   string
	bundle        []byte
	inspectBundle []byte
	freezeReplay  bool
	queries       []string
	arguments     [][]any
	commits       atomic.Int64
	rollbacks     atomic.Int64
}

func (harness *postgresTestHarness) snapshot() ([]string, [][]any) {
	harness.mu.Lock()
	defer harness.mu.Unlock()
	queries := append([]string(nil), harness.queries...)
	arguments := make([][]any, len(harness.arguments))
	for index := range harness.arguments {
		arguments[index] = append([]any(nil), harness.arguments[index]...)
	}
	return queries, arguments
}

var (
	postgresTestDriverOnce sync.Once
	postgresTestHarnesses  sync.Map
	postgresTestSequence   atomic.Uint64
)

func openPostgresTestDatabase(t *testing.T, harness *postgresTestHarness) *sql.DB {
	t.Helper()
	postgresTestDriverOnce.Do(func() {
		sql.Register("workflow-input-authority-test", postgresTestDriver{})
	})
	name := fmt.Sprintf("harness-%d", postgresTestSequence.Add(1))
	postgresTestHarnesses.Store(name, harness)
	database, err := sql.Open("workflow-input-authority-test", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Close()
		postgresTestHarnesses.Delete(name)
	})
	return database
}

type postgresTestDriver struct{}

func (postgresTestDriver) Open(name string) (driver.Conn, error) {
	value, exists := postgresTestHarnesses.Load(name)
	if !exists {
		return nil, errors.New("unknown PostgreSQL test harness")
	}
	return &postgresTestConnection{harness: value.(*postgresTestHarness)}, nil
}

type postgresTestConnection struct {
	harness *postgresTestHarness
}

func (*postgresTestConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare is not supported")
}

func (*postgresTestConnection) Close() error { return nil }

func (connection *postgresTestConnection) Begin() (driver.Tx, error) {
	return &postgresTestTransaction{harness: connection.harness}, nil
}

func (connection *postgresTestConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return connection.Begin()
}

func (connection *postgresTestConnection) QueryContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	values := make([]any, len(arguments))
	for index := range arguments {
		value := arguments[index].Value
		if bytesValue, ok := value.([]byte); ok {
			value = append([]byte(nil), bytesValue...)
		}
		values[index] = value
	}
	connection.harness.mu.Lock()
	connection.harness.queries = append(connection.harness.queries, query)
	connection.harness.arguments = append(connection.harness.arguments, values)
	connection.harness.mu.Unlock()
	if strings.Contains(query, "pg_advisory_xact_lock_shared") {
		return &postgresTestRows{columns: []string{"locked"}, values: [][]driver.Value{{true}}}, nil
	}
	if strings.Contains(query, "freeze_workflow_input_authority_from_quality_precommit_v1") {
		return &postgresTestRows{
			columns: []string{"authority_id", "inserted_by_current_transaction"},
			values:  [][]driver.Value{{connection.harness.authorityID, !connection.harness.freezeReplay}},
		}, nil
	}
	if strings.Contains(query, "inspect_workflow_input_authority_operation_v1") {
		if len(connection.harness.inspectBundle) == 0 {
			return &postgresTestRows{columns: []string{"value"}}, nil
		}
		return &postgresTestRows{columns: []string{"value"}, values: [][]driver.Value{{append([]byte(nil), connection.harness.inspectBundle...)}}}, nil
	}
	if strings.Contains(query, "resolve_workflow_input_authority_for_node_v1") {
		return &postgresTestRows{columns: []string{"value"}, values: [][]driver.Value{{append([]byte(nil), connection.harness.bundle...)}}}, nil
	}
	if strings.Contains(query, "assert_current_workflow_input_authority_v1") {
		return &postgresTestRows{columns: []string{"value"}, values: [][]driver.Value{{append([]byte(nil), connection.harness.bundle...)}}}, nil
	}
	return nil, fmt.Errorf("unexpected query: %s", query)
}

type postgresTestTransaction struct {
	harness *postgresTestHarness
}

func (transaction *postgresTestTransaction) Commit() error {
	transaction.harness.commits.Add(1)
	return nil
}

func (transaction *postgresTestTransaction) Rollback() error {
	transaction.harness.rollbacks.Add(1)
	return nil
}

type postgresTestRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (rows *postgresTestRows) Columns() []string { return rows.columns }
func (*postgresTestRows) Close() error           { return nil }

func (rows *postgresTestRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}
