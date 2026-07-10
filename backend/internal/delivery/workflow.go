package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"github.com/worksflow/builder/backend/internal/workflow"
	"gorm.io/gorm"
)

type workflowQualityAPI interface {
	Evaluate(context.Context, string, string, QualityRunInput) (QualityReport, error)
	Get(context.Context, string, string) (QualityReport, error)
}

type workflowPublishAPI interface {
	Publish(context.Context, string, string, string, PublishInput) (Deployment, error)
	List(context.Context, string, string) ([]Deployment, error)
}

// WorkflowQualityEvaluator binds the workflow quality node to the exact
// WorkspaceRevision carried by the quality node's exact typed input lineage.
type WorkflowQualityEvaluator struct {
	Quality  workflowQualityAPI
	Database *gorm.DB
}

func (a WorkflowQualityEvaluator) Evaluate(ctx context.Context, execution workflow.Execution) (workflow.QualityResult, error) {
	if a.Quality == nil {
		return workflow.QualityResult{}, fmt.Errorf("delivery quality service is required")
	}
	actor, err := execution.ExecutionActor()
	if err != nil {
		return workflow.QualityResult{}, err
	}
	reference, err := workspaceRevisionForQuality(ctx, a.Database, execution)
	if err != nil {
		return workflow.QualityResult{}, err
	}
	runID := execution.Run.ID
	report, err := a.Quality.Evaluate(ctx, execution.Run.ProjectID, actor.ActorID, QualityRunInput{
		WorkspaceRevision: reference,
		WorkflowRunID:     &runID,
	})
	if err != nil {
		return workflow.QualityResult{}, err
	}
	if report.ProjectID != execution.Run.ProjectID || report.WorkflowRunID == nil || *report.WorkflowRunID != runID || !exactVersionRefEqual(report.WorkspaceRevision, reference) {
		return workflow.QualityResult{}, conflict("quality service result does not match the exact typed workflow input")
	}
	findings, err := json.Marshal(map[string]any{
		"qualityRunId": report.ID, "score": report.Score,
		"reportArtifactId": report.ReportArtifactID, "reportRevisionId": report.ReportRevisionID,
		"workspaceRevision": report.WorkspaceRevision, "checks": report.Checks, "diagnostics": report.Diagnostics,
	})
	if err != nil {
		return workflow.QualityResult{}, err
	}
	workspaceRevision := deliveryArtifactReference(report.WorkspaceRevision)
	return workflow.QualityResult{
		Passed: report.Passed, Findings: findings, QualityRunID: report.ID,
		WorkspaceRevision: &workspaceRevision,
	}, nil
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
	input workflow.WorkflowPublishInput,
) (workflow.PublishResult, error) {
	if a.Quality == nil || a.Publisher == nil {
		return workflow.PublishResult{}, fmt.Errorf("delivery quality and publish services are required")
	}
	if err := input.BuildManifest.Validate(); err != nil {
		return workflow.PublishResult{}, err
	}
	if input.BuildManifest.ProjectID != projectID || input.BuildManifest.RunID != runID {
		return workflow.PublishResult{}, conflict("workflow build manifest does not match the publish invocation")
	}
	if err := input.WorkspaceRevision.Validate(); err != nil {
		return workflow.PublishResult{}, err
	}
	targetEnvironment := Environment(strings.TrimSpace(environment))
	if !targetEnvironment.Valid() {
		return workflow.PublishResult{}, Invalid("environment", "workflow publish environment must be preview or production")
	}
	report, err := a.Quality.Get(ctx, input.QualityRunID, actorID)
	if err != nil {
		if deliveryError, ok := AsError(err); ok && deliveryError.Code == CodeNotFound {
			return workflow.PublishResult{}, conflict("workflow publishing requires its exact passing quality report")
		}
		return workflow.PublishResult{}, err
	}
	workspaceReference := workflowArtifactReference(input.WorkspaceRevision)
	if report.ProjectID != projectID || report.WorkflowRunID == nil || *report.WorkflowRunID != runID || !report.Passed || !exactVersionRefEqual(report.WorkspaceRevision, workspaceReference) {
		return workflow.PublishResult{}, conflict("workflow publishing requires the exact passing quality result from its typed input lineage")
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
	deployment, err := a.Publisher.Publish(ctx, projectID, actorID, expectedETag, PublishInput{
		DeploymentID: deploymentID, Environment: targetEnvironment, EnvironmentRef: "workflow:" + runID,
		WorkspaceRevision:    &workspaceReference,
		BuildManifestID:      input.BuildManifest.BundleIDs[len(input.BuildManifest.BundleIDs)-1],
		WorkflowQualityRunID: report.ID,
		Message:              "Publish workflow run " + runID,
	})
	if err != nil {
		return workflow.PublishResult{}, err
	}
	return workflow.PublishResult{URL: deployment.PublicURL, DeploymentID: deployment.ID}, nil
}

func workspaceRevisionFromExecution(execution workflow.Execution) (core.VersionRef, error) {
	references := workspaceRevisionsFromExecution(execution)
	if len(references) != 1 {
		return core.VersionRef{}, conflict(fmt.Sprintf("quality gate requires exactly one WorkspaceRevision from its typed inputs, got %d", len(references)))
	}
	for _, reference := range references {
		return reference, nil
	}
	return core.VersionRef{}, conflict("quality gate has no incoming WorkspaceRevision")
}

func workspaceRevisionsFromExecution(execution workflow.Execution) map[string]core.VersionRef {
	references := make(map[string]core.VersionRef)
	for _, binding := range execution.Inputs.Bindings() {
		for _, raw := range []json.RawMessage{binding.Value, binding.Output} {
			reference, ok := decodeWorkspaceReference(raw)
			if !ok {
				continue
			}
			key := reference.ArtifactID + "\x00" + reference.RevisionID + "\x00" + reference.ContentHash
			if reference.AnchorID != nil {
				key += "\x00" + *reference.AnchorID
			}
			references[key] = reference
		}
	}
	return references
}

// workspaceRevisionForQuality accepts several Workbench outputs only when one
// is the current workspace and every other input is its exact ancestor. This
// is the assembly rule for parallel or sequential compiler groups converging
// on one application; unrelated workspace branches remain ambiguous.
func workspaceRevisionForQuality(
	ctx context.Context,
	database *gorm.DB,
	execution workflow.Execution,
) (core.VersionRef, error) {
	references := workspaceRevisionsFromExecution(execution)
	if len(references) == 1 {
		for _, reference := range references {
			return reference, nil
		}
	}
	if len(references) == 0 {
		return core.VersionRef{}, conflict("quality gate has no incoming WorkspaceRevision")
	}
	if database == nil {
		return core.VersionRef{}, conflict("quality gate cannot assemble multiple WorkspaceRevisions without the authoritative store")
	}
	projectID, err := uuid.Parse(execution.Run.ProjectID)
	if err != nil {
		return core.VersionRef{}, conflict("quality workflow project id is invalid")
	}
	var artifactID uuid.UUID
	byRevision := make(map[uuid.UUID]core.VersionRef, len(references))
	for _, reference := range references {
		if err := ValidateVersionRef(reference); err != nil || reference.AnchorID != nil {
			return core.VersionRef{}, conflict("quality workspace input is not an exact whole revision")
		}
		parsedArtifactID, parseErr := uuid.Parse(reference.ArtifactID)
		if parseErr != nil {
			return core.VersionRef{}, conflict("quality workspace artifact id is invalid")
		}
		if artifactID == uuid.Nil {
			artifactID = parsedArtifactID
		} else if artifactID != parsedArtifactID {
			return core.VersionRef{}, conflict("quality WorkspaceRevisions belong to different workspace artifacts")
		}
		parsedRevisionID, parseErr := uuid.Parse(reference.RevisionID)
		if parseErr != nil {
			return core.VersionRef{}, conflict("quality workspace revision id is invalid")
		}
		byRevision[parsedRevisionID] = reference
	}
	var artifact storage.ArtifactModel
	if err := database.WithContext(ctx).Where(
		"id = ? AND project_id = ? AND kind = ? AND lifecycle = ? AND latest_approved_revision_id IS NOT NULL",
		artifactID, projectID, "workspace", "active",
	).Take(&artifact).Error; err != nil {
		return core.VersionRef{}, conflict("quality workspace artifact is not the active project workspace")
	}
	currentID := *artifact.LatestApprovedRevisionID
	currentReference, exists := byRevision[currentID]
	if !exists {
		return core.VersionRef{}, conflict("quality inputs do not contain the exact current WorkspaceRevision")
	}
	visited := make(map[uuid.UUID]bool)
	current := currentID
	for current != uuid.Nil {
		if visited[current] || len(visited) > 10_000 {
			return core.VersionRef{}, conflict("workspace revision ancestry is cyclic or too deep")
		}
		visited[current] = true
		var revision storage.ArtifactRevisionModel
		if err := database.WithContext(ctx).Where("id = ? AND artifact_id = ?", current, artifactID).Take(&revision).Error; err != nil {
			return core.VersionRef{}, conflict("workspace revision ancestry is incomplete")
		}
		if expected, selected := byRevision[current]; selected && expected.ContentHash != revision.ContentHash {
			return core.VersionRef{}, conflict("workspace revision ancestry hash does not match the typed input")
		}
		if current == currentID && (revision.ContentHash != currentReference.ContentHash || revision.WorkflowStatus != "approved") {
			return core.VersionRef{}, conflict("current WorkspaceRevision is not the exact approved typed input")
		}
		if revision.ParentRevisionID == nil {
			break
		}
		current = *revision.ParentRevisionID
	}
	for revisionID := range byRevision {
		if !visited[revisionID] {
			return core.VersionRef{}, conflict("quality WorkspaceRevision inputs are not one linear application ancestry")
		}
	}
	return currentReference, nil
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

func deliveryArtifactReference(reference core.VersionRef) domain.ArtifactRef {
	anchor := ""
	if reference.AnchorID != nil {
		anchor = *reference.AnchorID
	}
	return domain.ArtifactRef{
		ArtifactID: reference.ArtifactID, RevisionID: reference.RevisionID,
		ContentHash: reference.ContentHash, AnchorID: anchor,
	}
}
