package delivery

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
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

func TestWorkspaceRevisionComesOnlyFromLatestCompletedWorkbench(t *testing.T) {
	stale, current := workflowRef(), workflowRef()
	now := time.Now().UTC()
	earlier := now.Add(-time.Minute)
	execution := workflow.Execution{Run: workflow.RunRecord{
		Context: workflow.NewRunContext(),
		Nodes: map[string]*workflow.NodeRecord{
			"untrusted": {Key: "untrusted", Type: domain.NodeHumanEdit, Status: workflow.NodeCompleted, CompletedAt: &now},
			"old-build": {Key: "old-build", Type: domain.NodeWorkbenchBuild, Status: workflow.NodeCompleted, CompletedAt: &earlier},
			"new-build": {Key: "new-build", Type: domain.NodeWorkbenchBuild, Status: workflow.NodeCompleted, CompletedAt: &now},
		},
	}}
	execution.Run.Context.Values["workspaceRevision"] = workbenchOutput(stale)
	execution.Run.Context.Nodes["untrusted"] = workflow.NodeMetadata{Output: workbenchOutput(stale)}
	execution.Run.Context.Nodes["old-build"] = workflow.NodeMetadata{Output: workbenchOutput(stale)}
	execution.Run.Context.Nodes["new-build"] = workflow.NodeMetadata{Output: workbenchOutput(current)}

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
	report    QualityReport
	err       error
}

func (s *workflowQualityStub) Evaluate(_ context.Context, _, _ string, input QualityRunInput) (QualityReport, error) {
	s.evaluated = input
	return s.report, s.err
}

func (s *workflowQualityStub) LatestPassingForWorkflow(context.Context, string, string, string) (QualityReport, error) {
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
	quality := &workflowQualityStub{report: QualityReport{ID: uuid.NewString(), Passed: true, Score: 100}}
	execution := workflow.Execution{Run: workflow.RunRecord{
		ID: uuid.NewString(), ProjectID: uuid.NewString(), StartedBy: uuid.NewString(),
		Context: workflow.NewRunContext(), Nodes: map[string]*workflow.NodeRecord{
			"build": {Key: "build", Type: domain.NodeWorkbenchBuild, Status: workflow.NodeCompleted, CompletedAt: &now},
		},
	}}
	execution.Run.Context.Nodes["build"] = workflow.NodeMetadata{Output: workbenchOutput(reference)}
	result, err := (WorkflowQualityEvaluator{Quality: quality}).Evaluate(context.Background(), execution)
	if err != nil || !result.Passed {
		t.Fatalf("quality adapter failed: result=%+v err=%v", result, err)
	}
	if quality.evaluated.WorkflowRunID == nil || *quality.evaluated.WorkflowRunID != execution.Run.ID || quality.evaluated.WorkspaceRevision.RevisionID != reference.RevisionID {
		t.Fatalf("quality input was not pinned: %+v", quality.evaluated)
	}
}

func TestWorkflowPublisherUsesPassingRunReportAndCurrentETag(t *testing.T) {
	projectID, runID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	reference := exactRef()
	quality := &workflowQualityStub{report: QualityReport{ID: uuid.NewString(), Passed: true, WorkspaceRevision: reference}}
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
	result, err := (WorkflowPublisher{Quality: quality, Publisher: publisher}).Publish(context.Background(), projectID, runID, actorID, string(EnvironmentPreview), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if result.URL == "" || publisher.etag != existing.ETag || publisher.input.DeploymentID != existing.ID || publisher.input.WorkspaceRevision == nil || publisher.input.WorkspaceRevision.RevisionID != reference.RevisionID {
		t.Fatalf("workflow publish was not exact/conditional: result=%+v input=%+v etag=%s", result, publisher.input, publisher.etag)
	}
	if _, err := (WorkflowPublisher{Quality: quality, Publisher: publisher}).Publish(context.Background(), projectID, uuid.NewString(), actorID, string(EnvironmentPreview), manifest); err == nil {
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
	_, err := (WorkflowPublisher{Quality: quality, Publisher: publisher}).Publish(context.Background(), projectID, runID, uuid.NewString(), string(EnvironmentProduction), manifest)
	if typed, ok := AsError(err); !ok || typed.Code != CodeConflict {
		t.Fatalf("missing quality report should block publish: %v", err)
	}
}
