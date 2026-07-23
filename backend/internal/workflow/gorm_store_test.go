package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func dryRunPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 user=worksflow dbname=worksflow sslmode=disable", PreferSimpleProtocol: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestGORMLeaseClaimUsesSkipLockedAndRecoveryPredicate(t *testing.T) {
	normalized := strings.ToLower(claimRunnableSQL)
	for _, fragment := range []string{"for update skip locked", "run.status not in ('completed', 'failed', 'cancelled', 'stale')", "node.node_type <> 'external_qualification_gate'", "status = 'ready'", "lease_expires_at < @now", "attempt = attempt + 1", "returning node.*"} {
		if !strings.Contains(normalized, fragment) {
			t.Fatalf("claim SQL missing %q", fragment)
		}
	}
}

func TestWorkflowRunMutationsDeclareRollingMigrationFence(t *testing.T) {
	if workflowInputAuthorityMigrationAdvisoryKey != "worksflow:workflow-input-authority-migration:v1" {
		t.Fatalf("unexpected Workflow Input migration fence key %q", workflowInputAuthorityMigrationAdvisoryKey)
	}
	database := dryRunPostgres(t)
	statement := database.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Exec(
			"SELECT pg_catalog.pg_advisory_xact_lock_shared(pg_catalog.hashtextextended(CAST(? AS text), 0))",
			workflowInputAuthorityMigrationAdvisoryKey,
		)
	})
	for _, fragment := range []string{"pg_advisory_xact_lock_shared", workflowInputAuthorityMigrationAdvisoryKey} {
		if !strings.Contains(statement, fragment) {
			t.Fatalf("workflow rolling-migration fence SQL missing %q: %s", fragment, statement)
		}
	}
}

func TestGORMClaimRunnableDoesNotConsumeTerminalRunNodesPostgres(t *testing.T) {
	database, cleanup := multiBundleCompletionPostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	userID, projectID, definitionID, definitionVersionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	profile := CurrentWorkflowExecutionProfileRef()
	if err := database.Exec(
		`INSERT INTO users (id,email,display_name,password_hash) VALUES (?,?,?,?)`,
		userID, userID.String()+"@claim.test", "Claim owner", "unused",
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(
		`INSERT INTO projects (id,name,created_by) VALUES (?,?,?)`, projectID, "Claim project", userID,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(
		`INSERT INTO workflow_definitions (id,project_id,workflow_key,title,created_by) VALUES (?,?,?,?,?)`,
		definitionID, projectID, "terminal-claim", "Terminal claim", userID,
	).Error; err != nil {
		t.Fatal(err)
	}
	definitionContent, err := json.Marshal(map[string]any{"executionProfile": profile})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(
		`INSERT INTO workflow_definition_versions (id,definition_id,version,schema_version,content,content_hash,execution_profile_version,execution_profile_hash,created_by) VALUES (?,?,?,?,?,?,?,?,?)`,
		definitionVersionID, definitionID, 1, 1, definitionContent, strings.Repeat("d", 64), profile.Version, profile.Hash, userID,
	).Error; err != nil {
		t.Fatal(err)
	}

	type seededNode struct {
		runID  uuid.UUID
		nodeID uuid.UUID
		status NodeStatus
	}
	seeded := []seededNode{
		{runID: uuid.New(), nodeID: uuid.New(), status: NodeReady},
		{runID: uuid.New(), nodeID: uuid.New(), status: NodeRunning},
	}
	for index, item := range seeded {
		if err := database.Exec(
			`INSERT INTO workflow_runs (id,project_id,definition_version_id,status,scope,context,started_by,execution_profile_version,execution_profile_hash,started_at,created_at,updated_at) VALUES (?,?,?,'failed','{}','{}',?,?,?,?,?,?)`,
			item.runID, projectID, definitionVersionID, userID, profile.Version, profile.Hash, now, now, now,
		).Error; err != nil {
			t.Fatal(err)
		}
		leaseOwner := any(nil)
		leaseExpiry := any(nil)
		if item.status == NodeRunning {
			leaseOwner = "expired-worker"
			leaseExpiry = now.Add(-time.Minute)
		}
		if err := database.Exec(
			`INSERT INTO workflow_node_runs (id,run_id,node_key,node_type,status,attempt,lease_owner,lease_expires_at,available_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			item.nodeID, item.runID, "terminal-node-"+string(rune('a'+index)), string(domain.NodeTransform), item.status, 7, leaseOwner, leaseExpiry, now.Add(-time.Hour), now, now,
		).Error; err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewGORMStore(database, InlineContentStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRunnable(ctx, "new-worker", now, time.Minute, profile); !errors.Is(err, ErrNoRunnableNode) {
		t.Fatalf("terminal run node was claimed: %v", err)
	}
	for _, item := range seeded {
		var row nodeRunRow
		if err := database.First(&row, "id = ?", item.nodeID).Error; err != nil {
			t.Fatal(err)
		}
		if row.Attempt != 7 || NodeStatus(row.Status) != item.status || (item.status == NodeReady && row.LeaseOwner != nil) || (item.status == NodeRunning && (row.LeaseOwner == nil || *row.LeaseOwner != "expired-worker")) {
			t.Fatalf("terminal node lease state was consumed: %+v", row)
		}
	}

	// The same ready node becomes claimable once its aggregate is active, proving
	// the terminal filter did not accidentally suppress supported work.
	if err := database.Model(&runRow{}).Where("id = ?", seeded[0].runID).Update("status", RunRunning).Error; err != nil {
		t.Fatal(err)
	}
	lease, err := store.ClaimRunnable(ctx, "new-worker", now, time.Minute, profile)
	if err != nil {
		t.Fatal(err)
	}
	if lease.NodeID != seeded[0].nodeID.String() || lease.Attempt != 8 {
		t.Fatalf("active control node was not claimed exactly: %+v", lease)
	}
}

func TestGORMCASAndLeaseUpdatesContainExpectedPredicates(t *testing.T) {
	db := dryRunPostgres(t)
	runID := uuid.New()
	runSQL := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Model(&runRow{}).Where("id = ? AND event_cursor = ?", runID, uint64(7)).Updates(map[string]any{"status": RunRunning, "event_cursor": uint64(8)})
	})
	if !strings.Contains(runSQL, "event_cursor") || !strings.Contains(runSQL, "= 7") {
		t.Fatalf("run CAS predicate missing: %s", runSQL)
	}
	nodeSQL := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Model(&nodeRunRow{}).Where("id = ? AND status = ? AND lease_owner = ? AND lease_expires_at >= ?", uuid.New(), NodeRunning, "worker", time.Now()).Updates(map[string]any{"status": NodeCompleted})
	})
	for _, fragment := range []string{"lease_owner", "lease_expires_at", "status"} {
		if !strings.Contains(nodeSQL, fragment) {
			t.Fatalf("lease CAS predicate missing %q: %s", fragment, nodeSQL)
		}
	}
}

func TestGORMMappingPersistsAggregateContextAndEventSequence(t *testing.T) {
	store, err := NewGORMStore(dryRunPostgres(t), InlineContentStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifestHash, _ := domain.CanonicalHash(map[string]any{"manifest": 1})
	definitionHash, _ := domain.CanonicalHash(map[string]any{"definition": 1})
	profile := CurrentWorkflowExecutionProfileRef()
	run := &RunRecord{ID: uuid.NewString(), ProjectID: uuid.NewString(), DefinitionVersionID: uuid.NewString(), Definition: domain.WorkflowDefinitionRef{ID: uuid.NewString(), Version: 1, Hash: definitionHash, ExecutionProfile: profile}, ExecutionProfile: profile, InputManifest: &domain.ManifestRef{ID: uuid.NewString(), Hash: manifestHash}, Status: RunRunning, Scope: json.RawMessage(`{"slice":"all"}`), Context: NewRunContext(), StartedBy: uuid.NewString(), CreatedAt: now, UpdatedAt: now, Nodes: map[string]*NodeRecord{}}
	node := &NodeRecord{ID: uuid.NewString(), RunID: run.ID, Key: "input", DefinitionNodeID: "input", Type: domain.NodeArtifactInput, Status: NodeReady, AvailableAt: now, CreatedAt: now, UpdatedAt: now}
	run.Nodes[node.Key] = node
	executionActorID := uuid.NewString()
	run.Context.Nodes[node.Key] = NodeMetadata{
		DefinitionNodeID: "input", MaxAttempts: 1, TimeoutNanos: int64(time.Minute),
		ExecutionActor: &ActorProvenance{ActorID: executionActorID, Role: core.RoleAdmin, Action: core.ActionPublish, Source: ActorSourceAuthenticatedCommand, AuthorizedAt: now},
	}
	row, nodes, err := store.runToRows(run)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || row.ExecutionProfileVersion != profile.Version || row.ExecutionProfileHash != profile.Hash || !strings.Contains(string(row.Context), "maxAttempts") || !strings.Contains(string(row.Context), executionActorID) || !strings.Contains(string(row.Context), string(ActorSourceAuthenticatedCommand)) {
		t.Fatalf("aggregate context was not persisted: %s", row.Context)
	}
	events, err := store.eventsToRows(run.ID, []Event{{ID: uuid.NewString(), Type: "one", CreatedAt: now}, {ID: uuid.NewString(), Type: "two", CreatedAt: now}}, 9)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Sequence != 10 || events[1].Sequence != 11 {
		t.Fatalf("unexpected event sequence: %+v", events)
	}
	content := []byte(`{"safe":true}`)
	kind, ref, hash, _ := InlineContentStore{}.Put(context.Background(), "test", "id", content)
	loaded, err := (InlineContentStore{}).Get(context.Background(), kind, ref, hash)
	if err != nil || string(loaded) != string(content) {
		t.Fatalf("inline content roundtrip failed: %v", err)
	}
}

func TestWorkflowEventsAreProjectedToTransactionalOutbox(t *testing.T) {
	projectID := uuid.New()
	runID := uuid.New()
	actorID := uuid.New()
	nodeKey := "requirements-review"
	now := time.Now().UTC()
	rows := []eventRow{{
		ID: uuid.New(), RunID: runID, Sequence: 14, EventType: "node.review_approved",
		NodeKey: &nodeKey, ActorID: &actorID, Payload: json.RawMessage(`{"reason":"ready"}`), CreatedAt: now,
	}}
	outbox, err := eventRowsToOutbox(projectID, runID, rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox) != 1 || outbox[0].ID != rows[0].ID || outbox[0].AggregateID != runID.String() ||
		outbox[0].EventType != rows[0].EventType || outbox[0].Subject != "worksflow.workflow.run.event" {
		t.Fatalf("unexpected outbox projection: %+v", outbox)
	}
	var payload map[string]any
	if err := json.Unmarshal(outbox[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["projectId"] != projectID.String() || payload["runId"] != runID.String() ||
		payload["nodeKey"] != nodeKey || payload["actorId"] != actorID.String() || payload["sequence"] != float64(14) {
		t.Fatalf("unexpected realtime payload: %#v", payload)
	}
}
