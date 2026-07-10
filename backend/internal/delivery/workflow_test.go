package delivery

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/workflow"
)

func workflowRef() domain.ArtifactRef {
	return domain.ArtifactRef{
		ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(),
		ContentHash: "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func workbenchOutput(reference domain.ArtifactRef) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{"workspaceRevision": reference})
	return payload
}

func workflowInputs(t *testing.T, runID, nodeKey, definitionNodeID string, output json.RawMessage) domain.NodeInputEnvelope {
	t.Helper()
	inputs, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "edge-" + nodeKey, FromPort: "default", ToPort: "default",
		Source: domain.NodeOutputReference{RunID: runID, NodeKey: nodeKey, DefinitionNodeID: definitionNodeID},
		Output: output, Value: output,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return inputs
}

func TestWorkspaceRevisionComesOnlyFromTypedInputBranch(t *testing.T) {
	unrelated, current := workflowRef(), workflowRef()
	now := time.Now().UTC()
	earlier := now.Add(-time.Minute)
	runID := uuid.NewString()
	execution := workflow.Execution{Run: workflow.RunRecord{
		ID:      runID,
		Context: workflow.NewRunContext(),
		Nodes: map[string]*workflow.NodeRecord{
			"untrusted":             {Key: "untrusted", Type: domain.NodeHumanEdit, Status: workflow.NodeCompleted, CompletedAt: &now},
			"selected-build":        {Key: "selected-build", DefinitionNodeID: "selected-build", Type: domain.NodeWorkbenchBuild, Status: workflow.NodeCompleted, CompletedAt: &earlier},
			"unrelated-later-build": {Key: "unrelated-later-build", Type: domain.NodeWorkbenchBuild, Status: workflow.NodeCompleted, CompletedAt: &now},
		},
	}, Inputs: workflowInputs(t, runID, "selected-build", "selected-build", workbenchOutput(current))}
	execution.Run.Context.Values["workspaceRevision"] = workbenchOutput(unrelated)
	execution.Run.Context.Nodes["untrusted"] = workflow.NodeMetadata{Output: workbenchOutput(unrelated)}
	execution.Run.Context.Nodes["selected-build"] = workflow.NodeMetadata{Output: workbenchOutput(current)}
	execution.Run.Context.Nodes["unrelated-later-build"] = workflow.NodeMetadata{Output: workbenchOutput(unrelated)}

	actual, err := workspaceRevisionFromExecution(execution)
	if err != nil {
		t.Fatal(err)
	}
	if actual.ArtifactID != current.ArtifactID || actual.RevisionID != current.RevisionID || actual.ContentHash != current.ContentHash {
		t.Fatalf("quality selected a stale/untrusted revision: %+v", actual)
	}
}

type workflowQualityStub struct {
	evaluated QualityRunInput
	actor     string
	report    QualityReport
	err       error
}

func (s *workflowQualityStub) Evaluate(_ context.Context, _, actorID string, input QualityRunInput) (QualityReport, error) {
	s.evaluated = input
	s.actor = actorID
	return s.report, s.err
}

func (s *workflowQualityStub) Get(context.Context, string, string) (QualityReport, error) {
	return s.report, s.err
}

type workflowPublishStub struct {
	listed  []Deployment
	input   PublishInput
	etag    string
	project string
	actor   string
	result  Deployment
}

func (s *workflowPublishStub) Publish(_ context.Context, projectID, actorID, etag string, input PublishInput) (Deployment, error) {
	s.project, s.actor, s.etag, s.input = projectID, actorID, etag, input
	return s.result, nil
}

func (s *workflowPublishStub) List(context.Context, string, string) ([]Deployment, error) {
	return s.listed, nil
}

func TestWorkflowQualityPinsRunAndWorkbenchRevision(t *testing.T) {
	reference := workflowRef()
	now := time.Now().UTC()
	starter, actorID := uuid.NewString(), uuid.NewString()
	runID := uuid.NewString()
	projectID := uuid.NewString()
	quality := &workflowQualityStub{report: QualityReport{ID: uuid.NewString(), ProjectID: projectID, WorkflowRunID: &runID, Passed: true, Score: 100, WorkspaceRevision: workflowArtifactReference(reference)}}
	execution := workflow.Execution{Node: workflow.NodeRecord{Key: "quality", Type: domain.NodeQualityGate}, Definition: domain.NodeDefinition{ID: "quality", Name: "Quality", Type: domain.NodeQualityGate, QualityGate: &domain.QualityGateNodeConfig{GateName: "release", RequiredRole: "editor"}}, Run: workflow.RunRecord{
		ID: runID, ProjectID: projectID, StartedBy: starter,
		Context: workflow.NewRunContext(), Nodes: map[string]*workflow.NodeRecord{
			"build": {Key: "build", DefinitionNodeID: "build", Type: domain.NodeWorkbenchBuild, Status: workflow.NodeCompleted, CompletedAt: &now},
		},
	}, Inputs: workflowInputs(t, runID, "build", "build", workbenchOutput(reference))}
	execution.Run.Context.Nodes["build"] = workflow.NodeMetadata{Output: workbenchOutput(reference)}
	execution.Run.Context.Nodes["quality"] = workflow.NodeMetadata{ExecutionActor: &workflow.ActorProvenance{ActorID: actorID, Role: core.RoleAdmin, Action: core.ActionEdit, Source: workflow.ActorSourceAuthenticatedCommand, AuthorizedAt: now}}
	result, err := (WorkflowQualityEvaluator{Quality: quality}).Evaluate(context.Background(), execution)
	if err != nil || !result.Passed {
		t.Fatalf("quality adapter failed: result=%+v err=%v", result, err)
	}
	if quality.evaluated.WorkflowRunID == nil || *quality.evaluated.WorkflowRunID != execution.Run.ID || quality.evaluated.WorkspaceRevision.RevisionID != reference.RevisionID {
		t.Fatalf("quality input was not pinned: %+v", quality.evaluated)
	}
	if quality.actor != actorID || quality.actor == starter {
		t.Fatalf("quality used starter instead of authorized actor: got=%q starter=%q", quality.actor, starter)
	}
}

func TestWorkflowPublisherUsesPassingRunReportAndCurrentETag(t *testing.T) {
	projectID, runID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	reference := exactRef()
	qualityRunID := uuid.NewString()
	quality := &workflowQualityStub{report: QualityReport{ID: qualityRunID, ProjectID: projectID, WorkflowRunID: &runID, Passed: true, WorkspaceRevision: reference}}
	existing := Deployment{ID: uuid.NewString(), Environment: EnvironmentPreview, ETag: `"deployment:current:7"`}
	publisher := &workflowPublishStub{
		listed: []Deployment{existing}, result: Deployment{ID: existing.ID, PublicURL: "/published/ready/"},
	}
	manifest := workflow.BuildManifest{
		SchemaVersion: 1, ProjectID: projectID, RunID: runID,
		SliceIDs: []string{uuid.NewString()}, BundleIDs: []string{uuid.NewString()},
		Sources: []domain.ArtifactRef{workflowRef()}, Constraints: json.RawMessage(`{}`), CreatedAt: time.Now().UTC(),
	}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	input := workflow.WorkflowPublishInput{QualityRunID: qualityRunID, WorkspaceRevision: deliveryArtifactReference(reference), BuildManifest: manifest}
	result, err := (WorkflowPublisher{Quality: quality, Publisher: publisher}).Publish(context.Background(), projectID, runID, actorID, string(EnvironmentPreview), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.URL == "" || publisher.etag != existing.ETag || publisher.input.DeploymentID != existing.ID || publisher.input.WorkspaceRevision == nil || publisher.input.WorkspaceRevision.RevisionID != reference.RevisionID || publisher.input.BuildManifestID != manifest.BundleIDs[len(manifest.BundleIDs)-1] || publisher.input.WorkflowQualityRunID != qualityRunID || publisher.input.QualityRunID != "" {
		t.Fatalf("workflow publish was not exact/conditional: result=%+v input=%+v etag=%s", result, publisher.input, publisher.etag)
	}
	if _, err := (WorkflowPublisher{Quality: quality, Publisher: publisher}).Publish(context.Background(), projectID, uuid.NewString(), actorID, string(EnvironmentPreview), input); err == nil {
		t.Fatal("manifest/run mismatch was accepted")
	}
}

func TestWorkflowPublisherRejectsMissingPassingReport(t *testing.T) {
	projectID, runID := uuid.NewString(), uuid.NewString()
	quality := &workflowQualityStub{err: notFound("missing")}
	publisher := &workflowPublishStub{}
	manifest := workflow.BuildManifest{
		SchemaVersion: 1, ProjectID: projectID, RunID: runID,
		SliceIDs: []string{"slice"}, BundleIDs: []string{"bundle"}, Sources: []domain.ArtifactRef{workflowRef()}, CreatedAt: time.Now().UTC(),
	}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	input := workflow.WorkflowPublishInput{QualityRunID: uuid.NewString(), WorkspaceRevision: workflowRef(), BuildManifest: manifest}
	_, err := (WorkflowPublisher{Quality: quality, Publisher: publisher}).Publish(context.Background(), projectID, runID, uuid.NewString(), string(EnvironmentProduction), input)
	if typed, ok := AsError(err); !ok || typed.Code != CodeConflict {
		t.Fatalf("missing quality report should block publish: %v", err)
	}
}
