package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/workflow"
)

type workflowQualityAPI interface {
	Evaluate(context.Context, string, string, QualityRunInput) (QualityReport, error)
	LatestPassingForWorkflow(context.Context, string, string, string) (QualityReport, error)
}

type workflowPublishAPI interface {
	Publish(context.Context, string, string, string, PublishInput) (Deployment, error)
	List(context.Context, string, string) ([]Deployment, error)
}

// WorkflowQualityEvaluator binds the workflow quality node to the exact
// WorkspaceRevision produced by the latest completed workbench node.
type WorkflowQualityEvaluator struct {
	Quality workflowQualityAPI
}

func (a WorkflowQualityEvaluator) Evaluate(ctx context.Context, execution workflow.Execution) (workflow.QualityResult, error) {
	if a.Quality == nil {
		return workflow.QualityResult{}, fmt.Errorf("delivery quality service is required")
	}
	reference, err := workspaceRevisionFromExecution(execution)
	if err != nil {
		return workflow.QualityResult{}, err
	}
	runID := execution.Run.ID
	report, err := a.Quality.Evaluate(ctx, execution.Run.ProjectID, execution.Run.StartedBy, QualityRunInput{
		WorkspaceRevision: reference,
		WorkflowRunID:     &runID,
	})
	if err != nil {
		return workflow.QualityResult{}, err
	}
	findings, err := json.Marshal(map[string]any{
		"qualityRunId": report.ID, "score": report.Score,
		"reportArtifactId": report.ReportArtifactID, "reportRevisionId": report.ReportRevisionID,
		"workspaceRevision": report.WorkspaceRevision, "checks": report.Checks, "diagnostics": report.Diagnostics,
	})
	if err != nil {
		return workflow.QualityResult{}, err
	}
	return workflow.QualityResult{Passed: report.Passed, Findings: findings}, nil
}

// WorkflowPublisher requires a passing quality report from the same workflow
// run, then publishes the exact WorkspaceRevision pinned by that report.
type WorkflowPublisher struct {
	Quality   workflowQualityAPI
	Publisher workflowPublishAPI
}

func (a WorkflowPublisher) Publish(
	ctx context.Context,
	projectID, runID, actorID, environment string,
	manifest workflow.BuildManifest,
) (workflow.PublishResult, error) {
	if a.Quality == nil || a.Publisher == nil {
		return workflow.PublishResult{}, fmt.Errorf("delivery quality and publish services are required")
	}
	if err := manifest.Validate(); err != nil {
		return workflow.PublishResult{}, err
	}
	if manifest.ProjectID != projectID || manifest.RunID != runID {
		return workflow.PublishResult{}, conflict("workflow build manifest does not match the publish invocation")
	}
	targetEnvironment := Environment(strings.TrimSpace(environment))
	if !targetEnvironment.Valid() {
		return workflow.PublishResult{}, Invalid("environment", "workflow publish environment must be preview or production")
	}
	report, err := a.Quality.LatestPassingForWorkflow(ctx, projectID, runID, actorID)
	if err != nil {
		if deliveryError, ok := AsError(err); ok && deliveryError.Code == CodeNotFound {
			return workflow.PublishResult{}, conflict("workflow publishing requires a passing quality report from the same run")
		}
		return workflow.PublishResult{}, err
	}
	if !report.Passed {
		return workflow.PublishResult{}, conflict("workflow publishing requires a passing quality report")
	}
	expectedETag := ""
	deploymentID := ""
	deployments, err := a.Publisher.List(ctx, projectID, actorID)
	if err != nil {
		return workflow.PublishResult{}, err
	}
	for _, deployment := range deployments {
		if deployment.Environment == targetEnvironment {
			expectedETag, deploymentID = deployment.ETag, deployment.ID
			break
		}
	}
	workspaceReference := report.WorkspaceRevision
	deployment, err := a.Publisher.Publish(ctx, projectID, actorID, expectedETag, PublishInput{
		DeploymentID: deploymentID, Environment: targetEnvironment, EnvironmentRef: "workflow:" + runID,
		WorkspaceRevision: &workspaceReference, Message: "Publish workflow run " + runID,
	})
	if err != nil {
		return workflow.PublishResult{}, err
	}
	return workflow.PublishResult{URL: deployment.PublicURL, DeploymentID: deployment.ID}, nil
}

func workspaceRevisionFromExecution(execution workflow.Execution) (core.VersionRef, error) {
	type candidate struct {
		completed bool
		at        int64
		key       string
		output    json.RawMessage
	}
	candidates := make([]candidate, 0, len(execution.Run.Nodes))
	for key, node := range execution.Run.Nodes {
		if node == nil || node.Status != workflow.NodeCompleted || node.Type != domain.NodeWorkbenchBuild {
			continue
		}
		metadata, exists := execution.Run.Context.Nodes[key]
		if !exists || len(metadata.Output) == 0 {
			continue
		}
		at := int64(0)
		if node.CompletedAt != nil {
			at = node.CompletedAt.UnixNano()
		}
		candidates = append(candidates, candidate{completed: node.CompletedAt != nil, at: at, key: key, output: metadata.Output})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].completed != candidates[j].completed {
			return candidates[i].completed
		}
		if candidates[i].at != candidates[j].at {
			return candidates[i].at > candidates[j].at
		}
		return candidates[i].key > candidates[j].key
	})
	for _, candidate := range candidates {
		if reference, ok := decodeWorkspaceReference(candidate.output); ok {
			return reference, nil
		}
	}
	return core.VersionRef{}, conflict("quality gate requires an exact WorkspaceRevision output from a completed workbench node")
}

func decodeWorkspaceReference(raw json.RawMessage) (core.VersionRef, bool) {
	var direct domain.ArtifactRef
	if err := json.Unmarshal(raw, &direct); err == nil && direct.Validate() == nil {
		return workflowArtifactReference(direct), true
	}
	var envelope struct {
		WorkspaceRevision *domain.ArtifactRef `json:"workspaceRevision"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.WorkspaceRevision == nil || envelope.WorkspaceRevision.Validate() != nil {
		return core.VersionRef{}, false
	}
	return workflowArtifactReference(*envelope.WorkspaceRevision), true
}

func workflowArtifactReference(reference domain.ArtifactRef) core.VersionRef {
	var anchor *string
	if reference.AnchorID != "" {
		value := reference.AnchorID
		anchor = &value
	}
	return core.VersionRef{
		ArtifactID: reference.ArtifactID, RevisionID: reference.RevisionID,
		ContentHash: reference.ContentHash, AnchorID: anchor,
	}
}
