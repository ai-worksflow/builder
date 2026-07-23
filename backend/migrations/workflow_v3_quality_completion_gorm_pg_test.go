package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	workflowengine "github.com/worksflow/builder/backend/internal/workflow"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This canary deliberately enters migration 85 through the application store,
// not through direct SQL. The lower-level migration canary proves the routine
// contract; this one proves GORMStore.Commit composes material admission,
// immutable precommit, aggregate mutation, event, and outbox in one atomic
// commit.
func TestWorkflowV3QualityCompletionGORMStoreCommitAtomicPostgresCanary(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		ctx, database, store, fixture := workflowV3QualityGORMCommitFixture(t, "quality_gorm_commit_")
		mutation := workflowV3QualityGORMCommitMutation(t, fixture)

		if err := store.Commit(ctx, mutation); err != nil {
			t.Fatalf("commit Quality completion through GORMStore: %v", err)
		}
		assertWorkflowV3QualityGORMCommit(t, ctx, database, fixture, mutation)
	})

	t.Run("post-precommit failure rolls everything back", func(t *testing.T) {
		ctx, database, store, fixture := workflowV3QualityGORMCommitFixture(t, "quality_gorm_rollback_")
		mutation := workflowV3QualityGORMCommitMutation(t, fixture)
		missingStartedAt := fixture.completedAt.Add(-time.Minute)
		missingCompletedAt := fixture.completedAt
		mutation.Nodes = append(mutation.Nodes, workflowengine.NodeMutation{
			Node: workflowengine.NodeRecord{
				ID: uuid.NewString(), RunID: fixture.runID.String(), Key: "missing-after-precommit",
				DefinitionNodeID: "missing-after-precommit", Type: domain.NodeTransform,
				Status: workflowengine.NodeCompleted, Attempt: 1,
				AvailableAt: fixture.completedAt.Add(-2 * time.Minute), StartedAt: &missingStartedAt,
				CompletedAt: &missingCompletedAt, CreatedAt: fixture.completedAt.Add(-2 * time.Minute),
				UpdatedAt: fixture.completedAt,
			},
			ExpectedStatus: workflowengine.NodeRunning,
		})

		if err := store.Commit(ctx, mutation); !errors.Is(err, workflowengine.ErrLeaseLost) {
			t.Fatalf("post-precommit missing-node commit error = %v, want ErrLeaseLost", err)
		}
		assertWorkflowV3QualityGORMRollback(t, ctx, database, fixture, mutation)
	})
}

type workflowV3QualityGORMFixture struct {
	workflowInputCanary
	completedAt time.Time
	leaseOwner  string
}

func workflowV3QualityGORMCommitFixture(
	t *testing.T,
	prefix string,
) (context.Context, *sql.DB, *workflowengine.GORMStore, workflowV3QualityGORMFixture) {
	t.Helper()
	ctx, base, dsn := qualificationReceiptV3Postgres(t)
	ensureQualificationReleaseHardenedRoles(t, ctx, base)
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, prefix)
	applyPostgresMigrationsForCanary(t, database)
	fixture := seedWorkflowV3QualityGORMPrecommitState(t, ctx, database)
	if _, err := database.ExecContext(ctx, `
GRANT SELECT,UPDATE ON TABLE
  projects,workflow_runs,workflow_node_runs
TO worksflow_application;
GRANT SELECT,INSERT ON TABLE
  workflow_run_events,outbox_events
TO worksflow_application
`); err != nil {
		t.Fatalf("grant shared workflow tables to GORM application canary: %v", err)
	}
	applicationDatabase := workflowV3QualityRoleDatabase(
		t, ctx, base, database, dsn, "worksflow_application", prefix+"application_",
	)

	gormDatabase, err := gorm.Open(
		gormpostgres.New(gormpostgres.Config{Conn: applicationDatabase}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatalf("open GORM over Quality canary database: %v", err)
	}
	store, err := workflowengine.NewGORMStore(
		gormDatabase, workflowV3QualityContentStoreFor(fixture.workflowInputCanary), nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, database, store, fixture
}

// seedWorkflowV3QualityGORMPrecommitState mirrors the pre-Quality boundary in
// TestWorkflowV3QualityCompletionAtomicHappyPathPostgresCanary. It is local to
// this independent test so the migration SQL canary remains untouched.
func seedWorkflowV3QualityGORMPrecommitState(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) workflowV3QualityGORMFixture {
	t.Helper()
	fixture := seedWorkflowInputCanary(t, ctx, database)
	inputManifest, err := domain.NewInputManifest(
		fixture.manifestID.String(), fixture.projectID.String(), "workflow-input-canary",
		fixture.deliverySliceID.String(), nil,
		[]domain.ManifestSource{{
			Ref: domain.ArtifactRef{
				ArtifactID: fixture.targetArtifactID.String(), RevisionID: fixture.targetRevisionID.String(),
				ContentHash: workflowInputDigest(fixture.revisionRaw),
			},
			Purpose: "workspace-target",
		}},
		json.RawMessage(`{}`), "workflow-input/v1", fixture.userID.String(),
		time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("construct valid InputManifest fixture: %v", err)
	}
	manifestRaw, err := json.Marshal(inputManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := inputManifest.Validate(); err != nil {
		t.Fatalf("validate InputManifest fixture: %v", err)
	}
	fixture.manifestRaw = manifestRaw
	definition, _ := workflowExecutionProfileV3MigrationDefinition(t)
	definitionDocument, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	var definitionValue map[string]any
	if err := json.Unmarshal(definitionDocument, &definitionValue); err != nil {
		t.Fatal(err)
	}
	definitionValue["id"] = fixture.definitionID.String()
	definitionValue["createdBy"] = fixture.userID.String()
	workflowExecutionProfileV3RehashDocument(t, definitionValue)
	definitionDocument, err = json.Marshal(definitionValue)
	if err != nil {
		t.Fatal(err)
	}
	fixture.definitionRaw = definitionDocument

	var gateEdgeID string
	for _, rawEdge := range definitionValue["edges"].([]any) {
		edge := rawEdge.(map[string]any)
		if edge["from"] == "quality" && edge["to"] == "external-qualification" {
			gateEdgeID = edge["id"].(string)
			break
		}
	}
	var gateInput domain.NodeInputEnvelope
	if gateEdgeID == "" || json.Unmarshal(fixture.nodeInputRaw, &gateInput) != nil {
		t.Fatal("cannot reconstruct exact v3 Quality gate input fixture")
	}
	bindings := gateInput.Bindings()
	if len(bindings) != 1 {
		t.Fatalf("Quality gate fixture has %d bindings, want 1", len(bindings))
	}
	bindings[0].EdgeID = gateEdgeID
	gateInput, err = domain.NewNodeInputEnvelope(bindings)
	if err != nil {
		t.Fatal(err)
	}
	fixture.nodeInputRaw = gateInput.Canonical()

	completedAt := time.Now().UTC().Truncate(time.Millisecond)
	leaseOwner := "quality-gorm-canary"
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	rollback := func(message string, cause error) {
		_ = transaction.Rollback()
		t.Fatalf("%s: %v", message, cause)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		rollback("disable fixture triggers", err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE input_manifests
SET content_hash=$2,manifest_hash=$3
WHERE id=$1
`, fixture.manifestID, workflowInputDigest(fixture.manifestRaw), "sha256:"+inputManifest.Hash); err != nil {
		rollback("install valid InputManifest fixture", err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE workflow_definition_versions
SET content=$2::jsonb,content_hash=$3
WHERE id=$1
`, fixture.definitionVersionID, definitionDocument, definitionValue["hash"]); err != nil {
		rollback("install exact v3 definition fixture", err)
	}
	for _, rawNode := range definitionValue["nodes"].([]any) {
		node := rawNode.(map[string]any)
		nodeKey := node["id"].(string)
		if nodeKey == "quality" || nodeKey == "external-qualification" {
			continue
		}
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO workflow_node_runs(
  id,run_id,node_key,node_type,status,definition_node_id,slice_kind,
  attempt,available_at,created_at,updated_at
) VALUES($1,$2,$3,$4,'pending',$3,'root',0,$5,$5,$5)
`, uuid.New(), fixture.runID, nodeKey, node["type"].(string), completedAt); err != nil {
			rollback("insert exact v3 node "+nodeKey, err)
		}
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE workflow_runs
SET context='{"nodes":{"external-qualification":{}}}'::jsonb,
    event_cursor=5,status='running',updated_at=$2
WHERE id=$1
`, fixture.runID, completedAt); err != nil {
		rollback("reset pre-Quality run fixture", err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE workflow_node_runs
SET status='running',attempt=1,output_revision_id=NULL,
    lease_owner=$2,lease_expires_at=$3,started_at=$4,
    completed_at=NULL,failure=NULL,updated_at=$4
WHERE id=$1
`, fixture.qualityNodeID, leaseOwner, completedAt.Add(time.Minute),
		completedAt.Add(-time.Minute)); err != nil {
		rollback("reset pre-Quality node fixture", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit pre-Quality GORM fixture: %v", err)
	}
	return workflowV3QualityGORMFixture{
		workflowInputCanary: fixture,
		completedAt:         completedAt,
		leaseOwner:          leaseOwner,
	}
}

func workflowV3QualityGORMCommitMutation(
	t *testing.T,
	fixture workflowV3QualityGORMFixture,
) workflowengine.RunMutation {
	t.Helper()
	var gateInput domain.NodeInputEnvelope
	if err := json.Unmarshal(fixture.nodeInputRaw, &gateInput); err != nil {
		t.Fatal(err)
	}
	bindings := gateInput.Bindings()
	if len(bindings) != 1 {
		t.Fatalf("Quality gate fixture has %d bindings, want 1", len(bindings))
	}
	semanticHash := gateInput.Hash()
	if !strings.HasPrefix(semanticHash, "sha256:") {
		semanticHash = "sha256:" + semanticHash
	}
	completionEventID := uuid.NewString()
	completionPayload := json.RawMessage(`{"attempt":1}`)
	contextValue := workflowengine.NewRunContext()
	contextValue.Nodes["quality"] = workflowengine.NodeMetadata{
		DefinitionNodeID: "quality", Output: append(json.RawMessage(nil), bindings[0].Output...),
	}
	contextValue.Nodes["external-qualification"] = workflowengine.NodeMetadata{
		DefinitionNodeID: "external-qualification",
		Input:            append(json.RawMessage(nil), fixture.nodeInputRaw...),
	}
	startedAt := fixture.completedAt.Add(-time.Minute)
	completedAt := fixture.completedAt
	precommit := &workflowengine.QualityCompletionPrecommitMutation{
		PrecommitID: uuid.NewString(), WorkflowInputOperationID: uuid.NewString(),
		WorkflowInputAuthorityID: uuid.NewString(), ActivationEventID: uuid.NewString(),
		ProjectID: fixture.projectID.String(), WorkflowRunID: fixture.runID.String(),
		QualityNodeRunID: fixture.qualityNodeID.String(), QualityNodeKey: "quality",
		GateNodeRunID: fixture.gateNodeID.String(), GateNodeKey: "external-qualification",
		ExpectedRunCursor: 5, CompletionEventID: completionEventID, CompletionEventSequence: 6,
		CompletionEventPayload: completionPayload, CompletionEventActorID: fixture.userID.String(),
		LeaseOwner: fixture.leaseOwner, LeaseAttempt: 1, CompletedAt: completedAt,
		OutputRevisionID:   fixture.targetRevisionID.String(),
		GateInputCanonical: append(json.RawMessage(nil), fixture.nodeInputRaw...),
		GateInputRawHash:   workflowInputDigest(fixture.nodeInputRaw), GateInputRawSize: int64(len(fixture.nodeInputRaw)),
		GateInputSemanticHash: semanticHash, GateInputBindingCount: 1,
	}
	return workflowengine.RunMutation{
		RunID: fixture.runID.String(), ExpectedCursor: 5, Status: workflowengine.RunRunning,
		Context: contextValue,
		Nodes: []workflowengine.NodeMutation{{
			Node: workflowengine.NodeRecord{
				ID: fixture.qualityNodeID.String(), RunID: fixture.runID.String(), Key: "quality",
				DefinitionNodeID: "quality", Type: domain.NodeQualityGate,
				Status: workflowengine.NodeCompleted, Attempt: 1,
				OutputRevisionID: fixture.targetRevisionID.String(),
				AvailableAt:      fixture.completedAt.Add(-2 * time.Minute), StartedAt: &startedAt,
				CompletedAt: &completedAt, CreatedAt: fixture.completedAt.Add(-2 * time.Minute),
				UpdatedAt: fixture.completedAt,
			},
			ExpectedStatus: workflowengine.NodeRunning, ExpectedOwner: fixture.leaseOwner,
		}},
		Events: []workflowengine.Event{{
			ID: completionEventID, RunID: fixture.runID.String(), Sequence: 6,
			Type: "node.completed", NodeKey: "quality", Payload: completionPayload,
			ActorID: fixture.userID.String(), CreatedAt: fixture.completedAt,
		}},
		QualityCompletionPrecommit: precommit,
		UpdatedAt:                  fixture.completedAt,
	}
}

type workflowV3QualityContent struct {
	hash string
	raw  []byte
}

type workflowV3QualityContentStore map[string]workflowV3QualityContent

func workflowV3QualityContentStoreFor(fixture workflowInputCanary) workflowV3QualityContentStore {
	contents := workflowV3QualityContentStore{}
	add := func(reference string, raw []byte) {
		contents[reference] = workflowV3QualityContent{
			hash: workflowInputDigest(raw), raw: append([]byte(nil), raw...),
		}
	}
	add("manifest/"+fixture.manifestID.String(), fixture.manifestRaw)
	add("revision/"+fixture.targetRevisionID.String(), fixture.revisionRaw)
	add("build-manifest/"+fixture.buildManifestID.String(), fixture.buildManifestRaw)
	add("build-contract/"+fixture.buildContractID.String(), fixture.buildContractRaw)
	return contents
}

func (workflowV3QualityContentStore) Put(
	context.Context, string, string, []byte,
) (string, string, string, error) {
	return "", "", "", errors.New("Quality completion canary content store is read-only")
}

func (store workflowV3QualityContentStore) Get(
	_ context.Context,
	contentStore string,
	contentRef string,
	contentHash string,
) ([]byte, error) {
	content, exists := store[contentRef]
	if contentStore != "mongo" || !exists || content.hash != contentHash {
		return nil, fmt.Errorf(
			"unexpected Quality material reference %s/%s@%s", contentStore, contentRef, contentHash,
		)
	}
	return append([]byte(nil), content.raw...), nil
}

func assertWorkflowV3QualityGORMCommit(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture workflowV3QualityGORMFixture,
	mutation workflowengine.RunMutation,
) {
	t.Helper()
	precommit := mutation.QualityCompletionPrecommit
	var materialCount, precommitCount, snapshotCount, reservationCount int
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM workflow_v3_quality_completion_materials WHERE precommit_id=$1),
  (SELECT count(*) FROM workflow_v3_quality_completion_precommits WHERE precommit_id=$1),
  (SELECT count(*) FROM workflow_v3_quality_completion_candidate_snapshots WHERE precommit_id=$1),
  (SELECT count(*) FROM workflow_v3_quality_completion_identity_reservations WHERE precommit_id=$1)
`, precommit.PrecommitID).Scan(
		&materialCount, &precommitCount, &snapshotCount, &reservationCount,
	); err != nil {
		t.Fatal(err)
	}
	if materialCount != 1 || precommitCount != 1 || snapshotCount != 1 || reservationCount != 4 {
		t.Fatalf(
			"committed Quality authority counts material/precommit/snapshot/reservations = %d/%d/%d/%d, want 1/1/1/4",
			materialCount, precommitCount, snapshotCount, reservationCount,
		)
	}

	// PL/pgSQL exception blocks may assign subtransaction xmins even though all
	// rows belong to one top-level transaction, so raw xmin equality is not a
	// valid atomicity test. Migration 85 records the top-level transaction ID in
	// both retained material and snapshot rows; its deferred exact-closure
	// predicate then proves the matching run, node, event, and outbox projection.
	var materialTransactionID, snapshotTransactionID string
	var exactClosure bool
	row := database.QueryRowContext(ctx, `
SELECT material.creation_transaction_id,snapshot.creation_transaction_id,
       workflow_v3_quality_completion_commit_is_exact_v1(precommit.precommit_id)
FROM workflow_v3_quality_completion_materials AS material
JOIN workflow_v3_quality_completion_precommits AS precommit
  ON precommit.precommit_id=material.precommit_id
JOIN workflow_v3_quality_completion_candidate_snapshots AS snapshot
  ON snapshot.precommit_id=precommit.precommit_id
WHERE precommit.precommit_id=$1
`, precommit.PrecommitID)
	if err := row.Scan(&materialTransactionID, &snapshotTransactionID, &exactClosure); err != nil {
		t.Fatal(err)
	}
	if materialTransactionID == "" || materialTransactionID != snapshotTransactionID || !exactClosure {
		t.Fatalf(
			"Quality atomic closure transaction material/snapshot/exact = %q/%q/%t",
			materialTransactionID, snapshotTransactionID, exactClosure,
		)
	}

	var cursor uint64
	var runStatus, nodeStatus string
	var gateInputExact bool
	var outputRevision uuid.UUID
	var leaseOwner sql.NullString
	var leaseExpiresAt sql.NullTime
	if err := database.QueryRowContext(ctx, `
SELECT run.event_cursor,run.status,
       run.context#>'{nodes,external-qualification,input}'=$3::jsonb,
       node.status,node.output_revision_id,node.lease_owner,node.lease_expires_at
FROM workflow_runs AS run
JOIN workflow_node_runs AS node ON node.run_id=run.id AND node.id=$2
WHERE run.id=$1
`, fixture.runID, fixture.qualityNodeID, fixture.nodeInputRaw).Scan(
		&cursor, &runStatus, &gateInputExact, &nodeStatus, &outputRevision, &leaseOwner, &leaseExpiresAt,
	); err != nil {
		t.Fatal(err)
	}
	if cursor != 6 || runStatus != "running" || !gateInputExact || nodeStatus != "completed" ||
		outputRevision != fixture.targetRevisionID || leaseOwner.Valid || leaseExpiresAt.Valid {
		t.Fatalf(
			"committed aggregate drifted: cursor=%d run=%s inputExact=%t node=%s revision=%s owner=%v expiry=%v",
			cursor, runStatus, gateInputExact, nodeStatus, outputRevision, leaseOwner, leaseExpiresAt,
		)
	}
}

func assertWorkflowV3QualityGORMRollback(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture workflowV3QualityGORMFixture,
	mutation workflowengine.RunMutation,
) {
	t.Helper()
	precommit := mutation.QualityCompletionPrecommit
	var retained, eventCount, outboxCount int
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM workflow_v3_quality_completion_materials WHERE precommit_id=$1)
  +(SELECT count(*) FROM workflow_v3_quality_completion_precommits WHERE precommit_id=$1)
  +(SELECT count(*) FROM workflow_v3_quality_completion_candidate_snapshots WHERE precommit_id=$1)
  +(SELECT count(*) FROM workflow_v3_quality_completion_identity_reservations WHERE precommit_id=$1),
  (SELECT count(*) FROM workflow_run_events WHERE id=$2),
  (SELECT count(*) FROM outbox_events WHERE id=$2)
`, precommit.PrecommitID, precommit.CompletionEventID).Scan(&retained, &eventCount, &outboxCount); err != nil {
		t.Fatal(err)
	}
	if retained != 0 || eventCount != 0 || outboxCount != 0 {
		t.Fatalf(
			"failed Quality commit retained authority/event/outbox rows = %d/%d/%d, want 0/0/0",
			retained, eventCount, outboxCount,
		)
	}

	var cursor uint64
	var runStatus, nodeStatus string
	var gateInputAbsent bool
	var outputRevision uuid.NullUUID
	var leaseOwner sql.NullString
	if err := database.QueryRowContext(ctx, `
SELECT run.event_cursor,run.status,
       run.context#>'{nodes,external-qualification,input}' IS NULL,
       node.status,node.output_revision_id,node.lease_owner
FROM workflow_runs AS run
JOIN workflow_node_runs AS node ON node.run_id=run.id AND node.id=$2
WHERE run.id=$1
`, fixture.runID, fixture.qualityNodeID).Scan(
		&cursor, &runStatus, &gateInputAbsent, &nodeStatus, &outputRevision, &leaseOwner,
	); err != nil {
		t.Fatal(err)
	}
	if cursor != 5 || runStatus != "running" || !gateInputAbsent || nodeStatus != "running" ||
		outputRevision.Valid || !leaseOwner.Valid || leaseOwner.String != fixture.leaseOwner {
		t.Fatalf(
			"failed Quality commit changed aggregate: cursor=%d run=%s inputAbsent=%t node=%s revision=%v owner=%v",
			cursor, runStatus, gateInputAbsent, nodeStatus, outputRevision, leaseOwner,
		)
	}
}
