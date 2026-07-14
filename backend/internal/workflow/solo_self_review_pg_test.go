package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type postgresWorkflowGovernanceResolver struct {
	database *gorm.DB
}

func (r postgresWorkflowGovernanceResolver) Governance(ctx context.Context, projectID string) (core.ProjectGovernance, error) {
	parsed, err := uuid.Parse(projectID)
	if err != nil {
		return core.ProjectGovernance{}, err
	}
	return core.LoadProjectGovernance(ctx, r.database, parsed)
}

func TestSoloSelfReviewLifecycleThroughFacadeEngineAndGORMStorePostgres(t *testing.T) {
	database, cleanup := multiBundleCompletionPostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	ownerID := uuid.New()
	projectID := uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: ownerID, Email: "solo-workflow-" + uuid.NewString() + "@example.com",
		DisplayName: "Solo Workflow Owner", PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Solo workflow review", Description: "Solo workflow review lifecycle",
		Lifecycle: "active", GovernanceMode: string(core.GovernanceModeSolo), Version: 1,
		CreatedBy: ownerID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectMemberModel{
		ProjectID: projectID, UserID: ownerID, Role: string(core.RoleOwner), JoinedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	schema := engineSchema()
	nodes := []domain.NodeDefinition{
		{
			ID: "input", Name: "Input", Type: domain.NodeArtifactInput,
			InputSchema: schema, OutputSchema: schema,
			ArtifactInput: &domain.ArtifactInputNodeConfig{
				AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1,
			},
		},
		{
			ID: "review", Name: "Owner review", Type: domain.NodeReviewGate,
			InputSchema: schema, OutputSchema: schema,
			ReviewGate: &domain.ReviewGateNodeConfig{
				RequiredRole: string(core.RoleOwner), MinimumApprovals: 1, ProhibitSelfReview: true,
			},
		},
		{
			ID: "publish", Name: "Publish", Type: domain.NodePublish,
			InputSchema: schema, OutputSchema: schema,
			Publish: &domain.PublishNodeConfig{Environment: "preview", RequiredRole: string(core.RoleOwner)},
		},
	}
	definition, err := domain.NewWorkflowDefinition(
		uuid.NewString(), 1, "Solo self review", "2", nodes,
		[]domain.WorkflowEdge{
			{ID: "input-review", From: "input", To: "review"},
			{ID: "review-publish", From: "review", To: "publish"},
		},
		ownerID.String(), now,
	)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewGORMStore(database, InlineContentStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	record := saveEngineDefinition(t, store, projectID.String(), ownerID.String(), definition)
	manifest := testManifestForEngine(t, projectID.String(), ownerID.String(), now)
	if err := store.SaveManifest(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(store)
	if err != nil {
		t.Fatal(err)
	}
	installCompleteTestExecutionProfileRuntime(t, engine, nil)
	access, err := core.NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	facade := &Facade{
		Engine: engine, Store: store, Access: access,
		Governance: postgresWorkflowGovernanceResolver{database: database},
	}

	run, err := facade.Start(ctx, projectID.String(), ownerID.String(), StartRequest{
		RunID: uuid.NewString(), DefinitionVersionID: record.VersionID, InputManifest: manifest.Ref(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if err := engine.ClaimAndExecute(ctx, "solo-workflow-pg-worker"); err != nil {
			t.Fatal(err)
		}
	}

	before, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.GovernanceMode != core.GovernanceModeSolo || before.StartedBy != ownerID.String() ||
		before.Status != RunWaitingReview || before.Nodes["review"].Status != NodeWaitingReview {
		t.Fatalf("run did not reach the solo owner review gate: %+v", before)
	}
	beforeEvents, err := store.ListEvents(ctx, run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	reason := "I verified the exact node output and accept responsibility for this solo approval."
	if err := facade.ResolveReview(
		ctx, projectID.String(), run.ID, "review", ownerID.String(), ReviewApprove, reason, false,
	); !errors.Is(err, core.ErrSoloReviewConfirmation) {
		t.Fatalf("unconfirmed solo self-review error = %v, want %v", err, core.ErrSoloReviewConfirmation)
	}
	afterRejected, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterRejectedEvents, err := store.ListEvents(ctx, run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterRejected, before) || !reflect.DeepEqual(afterRejectedEvents, beforeEvents) {
		t.Fatalf(
			"unconfirmed solo self-review changed persisted state: beforeCursor=%d afterCursor=%d beforeEvents=%d afterEvents=%d",
			before.EventCursor, afterRejected.EventCursor, len(beforeEvents), len(afterRejectedEvents),
		)
	}

	if err := facade.ResolveReview(
		ctx, projectID.String(), run.ID, "review", ownerID.String(), ReviewApprove, reason, true,
	); err != nil {
		t.Fatal(err)
	}
	approved, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	decisionActor := approved.Context.Nodes["review"].ReviewDecisionActor
	if approved.GovernanceMode != core.GovernanceModeSolo || approved.Nodes["review"].Status != NodeCompleted ||
		approved.Nodes["publish"].Status != NodeReady || decisionActor == nil ||
		decisionActor.ActorID != ownerID.String() || decisionActor.Role != core.RoleOwner ||
		decisionActor.Action != core.ActionApprove || decisionActor.Source != ActorSourceAuthenticatedCommand {
		t.Fatalf("confirmed solo self-review snapshot is incomplete: run=%+v actor=%+v", approved, decisionActor)
	}

	var persistedRun runRow
	if err := database.Where("id = ?", run.ID).Take(&persistedRun).Error; err != nil {
		t.Fatal(err)
	}
	var persistedContext RunContext
	if err := json.Unmarshal(persistedRun.Context, &persistedContext); err != nil {
		t.Fatal(err)
	}
	persistedActor := persistedContext.Nodes["review"].ReviewDecisionActor
	if persistedRun.GovernanceMode != string(core.GovernanceModeSolo) || persistedRun.StartedBy != ownerID ||
		persistedActor == nil || persistedActor.ActorID != ownerID.String() {
		t.Fatalf("PostgreSQL run snapshot lost solo governance or actor provenance: row=%+v actor=%+v", persistedRun, persistedActor)
	}

	var persistedEvent eventRow
	if err := database.Where(
		"run_id = ? AND event_type = ? AND node_key = ?", run.ID, "node.review_approve", "review",
	).Take(&persistedEvent).Error; err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Role           core.Role             `json:"role"`
		Action         core.Action           `json:"action"`
		Source         ActorProvenanceSource `json:"source"`
		Reason         string                `json:"reason"`
		SoloSelfReview bool                  `json:"soloSelfReview"`
		GovernanceMode core.GovernanceMode   `json:"governanceMode"`
	}
	if err := json.Unmarshal(persistedEvent.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if persistedEvent.ActorID == nil || *persistedEvent.ActorID != ownerID ||
		payload.Role != core.RoleOwner || payload.Action != core.ActionApprove ||
		payload.Source != ActorSourceAuthenticatedCommand || payload.Reason != reason ||
		!payload.SoloSelfReview || payload.GovernanceMode != core.GovernanceModeSolo {
		t.Fatalf("PostgreSQL solo review event lost governance evidence: actor=%v payload=%+v", persistedEvent.ActorID, payload)
	}
}
