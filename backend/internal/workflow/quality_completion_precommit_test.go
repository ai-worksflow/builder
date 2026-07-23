package workflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/domain"
)

type orderedQualityCompletionIDGenerator struct {
	values []string
	index  int
}

func (generator *orderedQualityCompletionIDGenerator) NewID() string {
	if generator == nil || generator.index >= len(generator.values) {
		panic("ordered Quality completion ID generator exhausted")
	}
	value := generator.values[generator.index]
	generator.index++
	return value
}

func TestApplyResultV3PreallocatesStableWorkflowInputIdentities(t *testing.T) {
	fixture := newApplyFixtureV3(t, applyFixtureV3Options{})
	identities := []string{
		uuid.NewString(), // Quality completion event.
		uuid.NewString(), // Quality completion precommit.
		uuid.NewString(), // WIA operation.
		uuid.NewString(), // WIA authority.
		uuid.NewString(), // WIA activation event.
	}
	fixture.engine.IDs = &orderedQualityCompletionIDGenerator{values: identities}

	if err := applyResultV3(
		context.Background(), fixture.engine, workflowExecutionRuntime{}, fixture.run,
		fixture.definition, fixture.node, fixture.lease, fixture.execution, fixture.result,
	); err != nil {
		t.Fatal(err)
	}
	precommit := firstMemoryQualityCompletionPrecommit(t, fixture.store)
	if precommit.CompletionEventID != identities[0] || precommit.PrecommitID != identities[1] ||
		precommit.WorkflowInputOperationID != identities[2] ||
		precommit.WorkflowInputAuthorityID != identities[3] || precommit.ActivationEventID != identities[4] {
		t.Fatalf("stable Quality/WIA identities were not preallocated in order: %+v", precommit)
	}
	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	for _, identity := range identities[2:] {
		if owner := fixture.store.qualityCompletionPrecommitByIdentity[identity]; owner != precommit.PrecommitID {
			t.Fatalf("stable identity %s owner = %q, want %s", identity, owner, precommit.PrecommitID)
		}
	}
}

func TestApplyResultV3PrecommitsLosslessScientificNumberInput(t *testing.T) {
	fixture := newApplyFixtureV3(t, applyFixtureV3Options{})
	var result QualityResult
	if err := json.Unmarshal(fixture.result.Output, &result); err != nil {
		t.Fatal(err)
	}
	result.BuildManifest.Constraints = json.RawMessage(`{"ratio":1e0}`)
	result.BuildManifest.Hash = ""
	if err := result.BuildManifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	fixture.result.Output = mustJSON(result)

	if err := applyResultV3(
		context.Background(), fixture.engine, workflowExecutionRuntime{}, fixture.run,
		fixture.definition, fixture.node, fixture.lease, fixture.execution, fixture.result,
	); err != nil {
		t.Fatal(err)
	}
	precommit := firstMemoryQualityCompletionPrecommit(t, fixture.store)
	if err := precommit.Validate(); err != nil {
		t.Fatalf("stored precommit is invalid: %v", err)
	}
	if !bytes.Contains(precommit.GateInputCanonical, []byte(`"ratio":1e0`)) {
		t.Fatalf("lossless gate bytes do not retain scientific number: %s", precommit.GateInputCanonical)
	}
	if precommit.GateInputRawHash != qualityCompletionRawSHA256(precommit.GateInputCanonical) ||
		precommit.GateInputRawSize != int64(len(precommit.GateInputCanonical)) ||
		precommit.GateInputBindingCount != 1 {
		t.Fatalf("stored byte authority drifted: %+v", precommit)
	}
}

func TestQualityCompletionPrecommitRejectsInvalidOrCollidingStableIdentities(t *testing.T) {
	fixture := newApplyFixtureV3(t, applyFixtureV3Options{})
	if err := applyResultV3(
		context.Background(), fixture.engine, workflowExecutionRuntime{}, fixture.run,
		fixture.definition, fixture.node, fixture.lease, fixture.execution, fixture.result,
	); err != nil {
		t.Fatal(err)
	}
	base := firstMemoryQualityCompletionPrecommit(t, fixture.store)
	type identityField struct {
		name string
		get  func(*QualityCompletionPrecommitMutation) string
		set  func(*QualityCompletionPrecommitMutation, string)
	}
	stable := []identityField{
		{name: "operation", get: func(value *QualityCompletionPrecommitMutation) string { return value.WorkflowInputOperationID }, set: func(value *QualityCompletionPrecommitMutation, identity string) {
			value.WorkflowInputOperationID = identity
		}},
		{name: "authority", get: func(value *QualityCompletionPrecommitMutation) string { return value.WorkflowInputAuthorityID }, set: func(value *QualityCompletionPrecommitMutation, identity string) {
			value.WorkflowInputAuthorityID = identity
		}},
		{name: "activation event", get: func(value *QualityCompletionPrecommitMutation) string { return value.ActivationEventID }, set: func(value *QualityCompletionPrecommitMutation, identity string) { value.ActivationEventID = identity }},
	}
	actorID := uuid.NewString()
	all := []identityField{
		{name: "precommit", get: func(value *QualityCompletionPrecommitMutation) string { return value.PrecommitID }},
		stable[0], stable[1], stable[2],
		{name: "project", get: func(value *QualityCompletionPrecommitMutation) string { return value.ProjectID }},
		{name: "run", get: func(value *QualityCompletionPrecommitMutation) string { return value.WorkflowRunID }},
		{name: "Quality node", get: func(value *QualityCompletionPrecommitMutation) string { return value.QualityNodeRunID }},
		{name: "gate node", get: func(value *QualityCompletionPrecommitMutation) string { return value.GateNodeRunID }},
		{name: "completion event", get: func(value *QualityCompletionPrecommitMutation) string { return value.CompletionEventID }},
		{name: "output revision", get: func(value *QualityCompletionPrecommitMutation) string { return value.OutputRevisionID }},
		{name: "completion actor", get: func(*QualityCompletionPrecommitMutation) string { return actorID }},
	}
	for _, field := range stable {
		field := field
		t.Run(field.name+" must be UUIDv4", func(t *testing.T) {
			candidate := cloneQualityCompletionPrecommitMutation(base)
			field.set(candidate, uuid.Nil.String())
			if err := candidate.Validate(); err == nil {
				t.Fatalf("invalid %s identity was accepted", field.name)
			}
		})
		for _, other := range all {
			other := other
			if other.name == field.name {
				continue
			}
			t.Run(field.name+" collides with "+other.name, func(t *testing.T) {
				candidate := cloneQualityCompletionPrecommitMutation(base)
				if other.name == "completion actor" {
					candidate.CompletionEventActorID = actorID
				}
				field.set(candidate, other.get(candidate))
				if err := candidate.Validate(); err == nil {
					t.Fatalf("%s identity collision with %s was accepted", field.name, other.name)
				}
			})
		}
	}
}

func TestMemoryStoreRejectsReusedStableWorkflowInputIdentity(t *testing.T) {
	for stableIndex, name := range []string{"operation", "authority", "activation event"} {
		t.Run(name, func(t *testing.T) {
			fixture := newApplyFixtureV3(t, applyFixtureV3Options{})
			identities := []string{
				uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(),
			}
			fixture.engine.IDs = &orderedQualityCompletionIDGenerator{values: identities}
			fixture.store.mu.Lock()
			fixture.store.qualityCompletionPrecommitByIdentity[identities[2+stableIndex]] = uuid.NewString()
			fixture.store.mu.Unlock()

			err := applyResultV3(
				context.Background(), fixture.engine, workflowExecutionRuntime{}, fixture.run,
				fixture.definition, fixture.node, fixture.lease, fixture.execution, fixture.result,
			)
			if !errors.Is(err, ErrQualityCompletionPrecommitCorrupt) || !errors.Is(err, ErrCASConflict) {
				t.Fatalf("reused stable %s identity error = %v", name, err)
			}
			assertApplyV3DidNotCommit(t, fixture)
		})
	}
}

type stripQualityCompletionPrecommitStore struct{ Store }

func (store stripQualityCompletionPrecommitStore) Commit(ctx context.Context, mutation RunMutation) error {
	mutation.QualityCompletionPrecommit = nil
	return store.Store.Commit(ctx, mutation)
}

func TestMemoryStoreRejectsV3QualityCompletionWithoutPrecommit(t *testing.T) {
	fixture := newApplyFixtureV3(t, applyFixtureV3Options{})
	fixture.engine.Store = stripQualityCompletionPrecommitStore{Store: fixture.store}
	err := applyResultV3(
		context.Background(), fixture.engine, workflowExecutionRuntime{}, fixture.run,
		fixture.definition, fixture.node, fixture.lease, fixture.execution, fixture.result,
	)
	if !errors.Is(err, ErrQualityCompletionPrecommitClosure) {
		t.Fatalf("missing precommit error = %v", err)
	}
	assertApplyV3DidNotCommit(t, fixture)
}

func TestMutationBuilderDeepClonesQualityCompletionPrecommit(t *testing.T) {
	fixture := newApplyFixtureV3(t, applyFixtureV3Options{})
	if err := applyResultV3(
		context.Background(), fixture.engine, workflowExecutionRuntime{}, fixture.run,
		fixture.definition, fixture.node, fixture.lease, fixture.execution, fixture.result,
	); err != nil {
		t.Fatal(err)
	}
	original := firstMemoryQualityCompletionPrecommit(t, fixture.store)
	builder := newMutationBuilder(fixture.engine, fixture.run, original.CompletedAt)
	builder.setQualityCompletionPrecommit(original)
	first := builder.build().QualityCompletionPrecommit
	wantGate := append([]byte(nil), first.GateInputCanonical...)
	wantPayload := append([]byte(nil), first.CompletionEventPayload...)
	wantOperationID := first.WorkflowInputOperationID
	wantAuthorityID := first.WorkflowInputAuthorityID
	wantActivationEventID := first.ActivationEventID
	original.GateInputCanonical[0] = 'X'
	original.CompletionEventPayload[0] = 'X'
	original.WorkflowInputOperationID = uuid.NewString()
	original.WorkflowInputAuthorityID = uuid.NewString()
	original.ActivationEventID = uuid.NewString()
	if !bytes.Equal(first.GateInputCanonical, wantGate) || !bytes.Equal(first.CompletionEventPayload, wantPayload) ||
		first.WorkflowInputOperationID != wantOperationID || first.WorkflowInputAuthorityID != wantAuthorityID ||
		first.ActivationEventID != wantActivationEventID {
		t.Fatal("builder retained aliases to the source precommit")
	}
	first.GateInputCanonical[0] = 'Y'
	first.CompletionEventPayload[0] = 'Y'
	first.WorkflowInputOperationID = uuid.NewString()
	first.WorkflowInputAuthorityID = uuid.NewString()
	first.ActivationEventID = uuid.NewString()
	second := builder.build().QualityCompletionPrecommit
	if !bytes.Equal(second.GateInputCanonical, wantGate) || !bytes.Equal(second.CompletionEventPayload, wantPayload) ||
		second.WorkflowInputOperationID != wantOperationID || second.WorkflowInputAuthorityID != wantAuthorityID ||
		second.ActivationEventID != wantActivationEventID {
		t.Fatal("built mutation retained aliases to builder state")
	}
}

type submillisecondQualityCompletionPrecommitStore struct{ Store }

func (store submillisecondQualityCompletionPrecommitStore) Commit(ctx context.Context, mutation RunMutation) error {
	mutation.QualityCompletionPrecommit = cloneQualityCompletionPrecommitMutation(mutation.QualityCompletionPrecommit)
	mutation.QualityCompletionPrecommit.CompletedAt = mutation.QualityCompletionPrecommit.CompletedAt.Add(time.Nanosecond)
	return store.Store.Commit(ctx, mutation)
}

func TestQualityCompletionPrecommitRequiresExactMillisecondTimestamp(t *testing.T) {
	fixture := newApplyFixtureV3(t, applyFixtureV3Options{})
	fixture.engine.Store = submillisecondQualityCompletionPrecommitStore{Store: fixture.store}
	err := applyResultV3(
		context.Background(), fixture.engine, workflowExecutionRuntime{}, fixture.run,
		fixture.definition, fixture.node, fixture.lease, fixture.execution, fixture.result,
	)
	if err == nil || !strings.Contains(err.Error(), "exact millisecond precision") {
		t.Fatalf("submillisecond MemoryStore precommit error = %v", err)
	}
	assertApplyV3DidNotCommit(t, fixture)

	committedFixture := newApplyFixtureV3(t, applyFixtureV3Options{})
	if err := applyResultV3(
		context.Background(), committedFixture.engine, workflowExecutionRuntime{}, committedFixture.run,
		committedFixture.definition, committedFixture.node, committedFixture.lease,
		committedFixture.execution, committedFixture.result,
	); err != nil {
		t.Fatal(err)
	}
	precommit := firstMemoryQualityCompletionPrecommit(t, committedFixture.store)
	if !precommit.CompletedAt.Equal(precommit.CompletedAt.Truncate(time.Millisecond)) {
		t.Fatalf("runtime did not precommit an exact millisecond timestamp: %s", precommit.CompletedAt)
	}
	record := qualityCompletionDatabaseRecordFromMutation(precommit)
	if !record.matches(precommit) {
		t.Fatal("exact millisecond database record did not match MemoryStore precommit")
	}
	record.CompletedAt = record.CompletedAt.Add(time.Nanosecond)
	if record.matches(precommit) {
		t.Fatal("submillisecond database timestamp matched an exact MemoryStore precommit")
	}
}

func TestQualityCompletionCommitUnknownReconciliation(t *testing.T) {
	fixture := newApplyFixtureV3(t, applyFixtureV3Options{})
	if err := applyResultV3(
		context.Background(), fixture.engine, workflowExecutionRuntime{}, fixture.run,
		fixture.definition, fixture.node, fixture.lease, fixture.execution, fixture.result,
	); err != nil {
		t.Fatal(err)
	}
	precommit := firstMemoryQualityCompletionPrecommit(t, fixture.store)
	record := qualityCompletionDatabaseRecordFromMutation(precommit)
	commitErr := errors.New("commit acknowledgement lost")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := reconcileQualityCompletionCommitOutcome(cancelled, precommit, commitErr,
		func(ctx context.Context, id string) (qualityCompletionPrecommitDatabaseRecord, error) {
			if ctx.Err() != nil || id != precommit.PrecommitID {
				t.Fatalf("detached inspection context/id = %v/%s", ctx.Err(), id)
			}
			return record, nil
		}); err != nil {
		t.Fatalf("exact committed record was not reconciled: %v", err)
	}
	if err := reconcileQualityCompletionCommitOutcome(context.Background(), precommit, commitErr,
		func(context.Context, string) (qualityCompletionPrecommitDatabaseRecord, error) {
			return qualityCompletionPrecommitDatabaseRecord{}, sql.ErrNoRows
		}); !errors.Is(err, commitErr) || errors.Is(err, ErrQualityCompletionOutcomeUnknown) {
		t.Fatalf("definite rollback error = %v", err)
	}
	if err := reconcileQualityCompletionCommitOutcome(context.Background(), precommit, commitErr,
		func(context.Context, string) (qualityCompletionPrecommitDatabaseRecord, error) {
			return qualityCompletionPrecommitDatabaseRecord{}, errors.New("inspection unavailable")
		}); !errors.Is(err, ErrQualityCompletionOutcomeUnknown) || errors.Is(err, commitErr) {
		t.Fatalf("uninspectable outcome error = %v", err)
	}
	divergent := record
	divergent.GateInputRawHash = "sha256:" + strings.Repeat("0", 64)
	if err := reconcileQualityCompletionCommitOutcome(context.Background(), precommit, commitErr,
		func(context.Context, string) (qualityCompletionPrecommitDatabaseRecord, error) { return divergent, nil }); !errors.Is(err, ErrQualityCompletionPrecommitCorrupt) || !errors.Is(err, ErrCASConflict) {
		t.Fatalf("divergent committed record error = %v", err)
	}
	for name, mutate := range map[string]func(*qualityCompletionPrecommitDatabaseRecord){
		"operation": func(value *qualityCompletionPrecommitDatabaseRecord) {
			value.WorkflowInputOperationID = uuid.NewString()
		},
		"authority": func(value *qualityCompletionPrecommitDatabaseRecord) {
			value.WorkflowInputAuthorityID = uuid.NewString()
		},
		"activation event": func(value *qualityCompletionPrecommitDatabaseRecord) { value.ActivationEventID = uuid.NewString() },
	} {
		t.Run(name+" mismatch", func(t *testing.T) {
			divergent := record
			mutate(&divergent)
			if divergent.matches(precommit) {
				t.Fatalf("database record with another %s identity matched", name)
			}
		})
	}
}

func TestQualityCompletionPostgresContractAndSQLSTATEClassification(t *testing.T) {
	if !strings.Contains(postgresQualityCompletionPrecommitQuery, "precommit_workflow_v3_quality_completion_v1") ||
		!strings.Contains(postgresQualityCompletionInspectQuery, "inspect_workflow_v3_quality_completion_precommit_v1") ||
		strings.Contains(postgresQualityCompletionPrecommitQuery, "freeze_workflow_input_authority_v1") ||
		strings.Count(postgresQualityCompletionPrecommitQuery, "?") != 15 ||
		!strings.Contains(postgresQualityCompletionPrecommitQuery, "CAST(? AS uuid)") {
		t.Fatalf("unexpected Quality completion SQL contract")
	}
	for _, query := range []string{postgresQualityCompletionPrecommitQuery, postgresQualityCompletionInspectQuery} {
		identityProjection := "precommit_id::text,\n  workflow_input_operation_id::text, workflow_input_authority_id::text,\n  activation_event_id::text"
		if !strings.Contains(query, identityProjection) {
			t.Fatalf("Quality completion SQL does not return stable identities immediately after precommit ID: %s", query)
		}
	}
	tests := []struct {
		code string
		want error
	}{
		{code: "WQC01", want: domain.ErrInvalidArgument},
		{code: "WQC02", want: ErrQualityCompletionPrecommitCorrupt},
		{code: "WQC03", want: ErrQualityCompletionPrecommitStale},
		{code: "WQC04", want: ErrQualityCompletionPrecommitClosure},
		{code: "40001", want: ErrQualityCompletionRetryable},
		{code: "40P01", want: ErrQualityCompletionRetryable},
	}
	for _, test := range tests {
		err := mapQualityCompletionPostgresError("test", &pgconn.PgError{Code: test.code})
		if !errors.Is(err, test.want) {
			t.Errorf("SQLSTATE %s error = %v", test.code, err)
		}
	}
}

func TestGORMCommitErrorMappingIsScopedToQualityPrecommit(t *testing.T) {
	serialization := &pgconn.PgError{Code: "40001", Message: "serialization failure"}
	if got := mapWorkflowCommitPostgresError(nil, serialization); got != serialization ||
		errors.Is(got, ErrQualityCompletionRetryable) || errors.Is(got, ErrCASConflict) {
		t.Fatalf("ordinary workflow commit error semantics changed: %v", got)
	}
	if got := reconcileQualityCompletionCommitOutcome(
		context.Background(), nil, serialization, nil,
	); got != serialization || errors.Is(got, ErrQualityCompletionRetryable) {
		t.Fatalf("ordinary reconcile path mapped a Quality-only SQLSTATE: %v", got)
	}
	if got := mapWorkflowCommitPostgresError(&QualityCompletionPrecommitMutation{}, serialization); !errors.Is(got, ErrQualityCompletionRetryable) || !errors.Is(got, ErrCASConflict) {
		t.Fatalf("Quality workflow commit did not map retryable SQLSTATE: %v", got)
	}
}

func firstMemoryQualityCompletionPrecommit(t *testing.T, store *MemoryStore) *QualityCompletionPrecommitMutation {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.qualityCompletionPrecommits) != 1 {
		t.Fatalf("precommit count = %d, want 1", len(store.qualityCompletionPrecommits))
	}
	for _, precommit := range store.qualityCompletionPrecommits {
		return cloneQualityCompletionPrecommitMutation(precommit)
	}
	return nil
}

func qualityCompletionDatabaseRecordFromMutation(value *QualityCompletionPrecommitMutation) qualityCompletionPrecommitDatabaseRecord {
	return qualityCompletionPrecommitDatabaseRecord{
		PrecommitID:              value.PrecommitID,
		WorkflowInputOperationID: value.WorkflowInputOperationID,
		WorkflowInputAuthorityID: value.WorkflowInputAuthorityID,
		ActivationEventID:        value.ActivationEventID,
		ProjectID:                value.ProjectID, WorkflowRunID: value.WorkflowRunID,
		QualityNodeRunID: value.QualityNodeRunID, QualityNodeKey: value.QualityNodeKey,
		GateNodeRunID: value.GateNodeRunID, GateNodeKey: value.GateNodeKey,
		ExpectedRunCursor: value.ExpectedRunCursor, CompletionEventSequence: value.CompletionEventSequence,
		CompletionEventID: value.CompletionEventID, CompletionEventPayload: cloneRaw(value.CompletionEventPayload),
		CompletionEventActorID: value.CompletionEventActorID, CompletedAt: value.CompletedAt,
		LeaseOwner: value.LeaseOwner, LeaseAttempt: value.LeaseAttempt, OutputRevisionID: value.OutputRevisionID,
		GateInputCanonical: cloneRaw(value.GateInputCanonical), GateInputRawHash: value.GateInputRawHash,
		GateInputRawSize: value.GateInputRawSize, GateInputSemanticHash: value.GateInputSemanticHash,
		GateInputBindingCount: value.GateInputBindingCount,
	}
}
