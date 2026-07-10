package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

type workflowEntryArtifactValidator struct {
	status string
}

func (v workflowEntryArtifactValidator) Validate(
	_ context.Context,
	execution Execution,
	manifest domain.InputManifest,
) (json.RawMessage, error) {
	if manifest.BaseRevision == nil {
		return nil, errors.New("workflow entry manifest must pin its Project Brief base revision")
	}
	refs := artifactInputRefs(manifest)
	accepted := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		artifactType, err := validateArtifactInputRevision(
			*execution.Definition.ArtifactInput,
			manifest.ProjectID, manifest.ProjectID, v.status, "project_brief",
		)
		if err != nil {
			return nil, err
		}
		accepted = append(accepted, map[string]any{
			"ref": ref, "artifactType": artifactType,
			"kind": "project_brief", "status": v.status,
		})
	}
	return artifactInputOutput(manifest, refs, accepted)
}

func workflowEntryDefinition(t *testing.T, requireApproved bool) domain.WorkflowDefinition {
	t.Helper()
	schema := engineSchema()
	userID := uuid.NewString()
	nodes := []domain.NodeDefinition{
		{ID: "source", Name: "Project Brief input", Type: domain.NodeArtifactInput, InputSchema: schema, OutputSchema: schema, ArtifactInput: &domain.ArtifactInputNodeConfig{AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, RequireApproved: requireApproved, MinimumArtifacts: 1}},
		{ID: "interview", Name: "Interview and edit", Type: domain.NodeHumanEdit, InputSchema: schema, OutputSchema: schema, HumanEdit: &domain.HumanEditNodeConfig{ArtifactType: domain.ArtifactDocument, RequiredRole: "editor"}},
		{ID: "review", Name: "Approve Project Brief", Type: domain.NodeReviewGate, InputSchema: schema, OutputSchema: schema, ReviewGate: &domain.ReviewGateNodeConfig{RequiredRole: "owner", MinimumApprovals: 1, ProhibitSelfReview: true}},
		{ID: "ai", Name: "Generate requirements", Type: domain.NodeAITransform, InputSchema: schema, OutputSchema: schema, AITransform: &domain.AITransformNodeConfig{JobType: "derive_requirements", ModelPolicy: "project-default", OutputSchemaVersion: "requirements/v1", MaxAttempts: 1, Timeout: time.Minute}},
	}
	definition, err := domain.NewWorkflowDefinition(
		uuid.NewString(), 1, "Project Brief entry", "2", nodes,
		[]domain.WorkflowEdge{
			{ID: "source-interview", From: "source", To: "interview"},
			{ID: "interview-review", From: "interview", To: "review"},
			{ID: "review-ai", From: "review", To: "ai"},
		},
		userID, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func exactEntryManifest(
	t *testing.T,
	projectID, userID string,
	source domain.ArtifactRef,
	now time.Time,
) domain.InputManifest {
	t.Helper()
	manifest, err := domain.NewInputManifest(
		uuid.NewString(), projectID, "workflow_start", "", &source,
		[]domain.ManifestSource{{Ref: source, Purpose: "project_brief"}},
		json.RawMessage(`{"entryArtifact":"project_brief"}`),
		"workflow-input/v1", userID, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestUnapprovedProjectBriefEntersInterviewButAIWaitsForCanonicalApproval(t *testing.T) {
	definition := workflowEntryDefinition(t, false)
	registry := NewMapRegistry()
	editedRef := domain.ArtifactRef{}
	engine, store, clock, record, initial, projectID, startedBy := newTestEngine(t, definition, registry)
	entry := exactEntryManifest(t, projectID, startedBy, initial.Sources[0].Ref, clock.Now())
	if err := store.SaveManifest(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	engine.ArtifactInputs = workflowEntryArtifactValidator{status: "in_review"}
	canonicalApproved := false
	gateBlocked := errors.New("canonical Project Brief review is not approved")
	engine.ReviewGate = ReviewGateVerifierFunc(func(_ context.Context, _ string, refs []domain.ArtifactRef, _ domain.ReviewGateNodeConfig) error {
		if len(refs) != 1 || !refs[0].Equal(editedRef) {
			return errors.New("review gate did not receive the edited exact revision")
		}
		if !canonicalApproved {
			return gateBlocked
		}
		return nil
	})
	run, err := engine.Start(context.Background(), StartRequest{
		RunID: uuid.NewString(), ProjectID: projectID,
		DefinitionVersionID: record.VersionID, InputManifest: entry.Ref(), StartedBy: startedBy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	waitingInterview, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waitingInterview.Nodes["interview"].Status != NodeWaitingInput || waitingInterview.Nodes["ai"].Status != NodePending {
		t.Fatalf("unapproved entry did not stop at human interview: interview=%s ai=%s", waitingInterview.Nodes["interview"].Status, waitingInterview.Nodes["ai"].Status)
	}

	editedHash, _ := domain.CanonicalHash(map[string]any{"projectBrief": "edited"})
	editedRef = domain.ArtifactRef{ArtifactID: entry.BaseRevision.ArtifactID, RevisionID: uuid.NewString(), ContentHash: editedHash}
	output, _ := json.Marshal(map[string]any{"payload": map[string]any{}, "artifactRevision": editedRef})
	if err := engine.SubmitHumanInput(context.Background(), run.ID, "interview", output, startedBy); err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ResolveReview(
		context.Background(), run.ID, "review",
		engineReviewDecision(uuid.NewString(), ReviewApprove, ""),
	); !errors.Is(err, gateBlocked) {
		t.Fatalf("AI gate accepted an unapproved Project Brief: %v", err)
	}
	blocked, _ := store.GetRun(context.Background(), run.ID)
	if blocked.Nodes["review"].Status != NodeWaitingReview || blocked.Nodes["ai"].Status != NodePending {
		t.Fatalf("unapproved Project Brief flowed into AI: review=%s ai=%s", blocked.Nodes["review"].Status, blocked.Nodes["ai"].Status)
	}

	canonicalApproved = true
	if err := engine.ResolveReview(
		context.Background(), run.ID, "review",
		engineReviewDecision(uuid.NewString(), ReviewApprove, ""),
	); err != nil {
		t.Fatal(err)
	}
	approved, _ := store.GetRun(context.Background(), run.ID)
	if approved.Nodes["ai"].Status != NodeReady {
		t.Fatalf("approved Project Brief did not unlock AI: %s", approved.Nodes["ai"].Status)
	}
}

func TestCustomRequireApprovedInputStillRejectsUnapprovedEntry(t *testing.T) {
	definition := workflowEntryDefinition(t, true)
	engine, store, clock, record, initial, projectID, startedBy := newTestEngine(t, definition, NewMapRegistry())
	entry := exactEntryManifest(t, projectID, startedBy, initial.Sources[0].Ref, clock.Now())
	if err := store.SaveManifest(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	engine.ArtifactInputs = workflowEntryArtifactValidator{status: "in_review"}
	run, err := engine.Start(context.Background(), StartRequest{
		RunID: uuid.NewString(), ProjectID: projectID,
		DefinitionVersionID: record.VersionID, InputManifest: entry.Ref(), StartedBy: startedBy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.ClaimAndExecute(context.Background(), "worker"); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("requireApproved input did not reject the unapproved Project Brief: %v", err)
	}
	blocked, _ := store.GetRun(context.Background(), run.ID)
	if blocked.Nodes["interview"].Status != NodePending || blocked.Nodes["ai"].Status != NodePending {
		t.Fatalf("rejected input advanced the workflow: interview=%s ai=%s", blocked.Nodes["interview"].Status, blocked.Nodes["ai"].Status)
	}
}
