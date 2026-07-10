package workflow

import (
	"context"
	"encoding/json"
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
	for _, fragment := range []string{"for update skip locked", "status = 'ready'", "lease_expires_at < @now", "attempt = attempt + 1", "returning node.*"} {
		if !strings.Contains(normalized, fragment) {
			t.Fatalf("claim SQL missing %q", fragment)
		}
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
	run := &RunRecord{ID: uuid.NewString(), ProjectID: uuid.NewString(), DefinitionVersionID: uuid.NewString(), Definition: domain.WorkflowDefinitionRef{ID: uuid.NewString(), Version: 1, Hash: definitionHash}, InputManifest: &domain.ManifestRef{ID: uuid.NewString(), Hash: manifestHash}, Status: RunRunning, Scope: json.RawMessage(`{"slice":"all"}`), Context: NewRunContext(), StartedBy: uuid.NewString(), CreatedAt: now, UpdatedAt: now, Nodes: map[string]*NodeRecord{}}
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
	if len(nodes) != 1 || !strings.Contains(string(row.Context), "maxAttempts") || !strings.Contains(string(row.Context), executionActorID) || !strings.Contains(string(row.Context), string(ActorSourceAuthenticatedCommand)) {
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
