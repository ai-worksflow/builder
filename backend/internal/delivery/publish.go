package delivery

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxDeploymentHistory = 200

var (
	publicEnvironmentNamePattern = regexp.MustCompile(`^(?:PUBLIC_|NEXT_PUBLIC_|VITE_|REACT_APP_)[A-Z][A-Z0-9_]{0,127}$`)
	environmentReferencePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,511}$`)
	providerNamePattern          = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

type publishRevisionLoader interface {
	LoadFrozenWorkspace(context.Context, string, string, core.VersionRef, core.Action) (WorkspaceSnapshot, error)
	LoadBuildManifest(context.Context, string, string, string, core.Action) (core.WorkbenchBundle, error)
}

type publishQualityReader interface {
	LatestPassingForRevision(context.Context, string, string, string) (QualityReport, error)
	LoadBuildArtifact(context.Context, string, BuildArtifactReference) (BuildArtifact, error)
}

type PublishService struct {
	database     *gorm.DB
	access       AccessControl
	loader       publishRevisionLoader
	quality      publishQualityReader
	provider     PublishProvider
	environments EnvironmentResolver
	now          func() time.Time
}

func NewPublishService(
	database *gorm.DB,
	access AccessControl,
	loader publishRevisionLoader,
	quality publishQualityReader,
	provider PublishProvider,
	environments EnvironmentResolver,
) (*PublishService, error) {
	if database == nil || access == nil || loader == nil || quality == nil || provider == nil {
		return nil, errors.New("publish database, access control, revision loader, quality service and provider are required")
	}
	if environments == nil {
		environments = EmptyEnvironmentResolver{}
	}
	if !providerNamePattern.MatchString(provider.Name()) {
		return nil, errors.New("publish provider name must be a stable lowercase identifier")
	}
	return &PublishService{
		database: database, access: access, loader: loader, quality: quality,
		provider: provider, environments: environments, now: time.Now,
	}, nil
}

type publishSource struct {
	workspace       WorkspaceSnapshot
	buildManifestID *uuid.UUID
	qualityRunID    *uuid.UUID
	buildReference  *BuildArtifactReference
	buildArtifact   BuildArtifact
}

type publishReservation struct {
	deployment deploymentModel
	version    deploymentVersionModel
	public     map[string]string
	artifact   BuildArtifact
}

// Publish creates an immutable deployment version. expectedETag is required when
// the project already has a deployment for the selected environment.
func (s *PublishService) Publish(ctx context.Context, projectID, actorID, expectedETag string, input PublishInput) (Deployment, error) {
	if !input.Environment.Valid() {
		return Deployment{}, Invalid("environment", "environment must be preview or production")
	}
	if (input.WorkspaceRevision == nil) == (strings.TrimSpace(input.BuildManifestID) == "") {
		return Deployment{}, Invalid("workspaceRevision", "provide exactly one exact workspaceRevision or buildManifestId")
	}
	if err := validatePublishText(input.EnvironmentRef, input.Message); err != nil {
		return Deployment{}, err
	}
	action := actionForEnvironment(input.Environment)
	if _, err := s.access.Authorize(ctx, projectID, actorID, action); err != nil {
		return Deployment{}, err
	}
	source, err := s.resolvePublishSource(ctx, projectID, actorID, action, input.WorkspaceRevision, input.BuildManifestID)
	if err != nil {
		return Deployment{}, err
	}
	if err := validatePublishableWorkspace(source.workspace); err != nil {
		return Deployment{}, err
	}
	report, err := s.quality.LatestPassingForRevision(ctx, projectID, source.workspace.Revision.RevisionID, actorID)
	if err != nil {
		if deliveryError, ok := AsError(err); ok && deliveryError.Code == CodeNotFound {
			return Deployment{}, conflict("publishing requires a passing quality report and immutable build artifact for the exact frozen WorkspaceRevision")
		}
		return Deployment{}, err
	}
	if !exactVersionRefEqual(report.WorkspaceRevision, source.workspace.Revision) || !report.Passed || report.BuildArtifact == nil {
		return Deployment{}, conflict("the passing quality report and build artifact do not match the exact frozen WorkspaceRevision")
	}
	buildArtifact, err := s.quality.LoadBuildArtifact(ctx, projectID, *report.BuildArtifact)
	if err != nil {
		return Deployment{}, err
	}
	if !exactVersionRefEqual(buildArtifact.WorkspaceRevision, source.workspace.Revision) {
		return Deployment{}, conflict("immutable quality build artifact does not match the publish workspace")
	}
	qualityRunID, _ := uuid.Parse(report.ID)
	source.qualityRunID = &qualityRunID
	source.buildReference = report.BuildArtifact
	source.buildArtifact = buildArtifact
	resolved, err := s.environments.Resolve(ctx, projectID, input.Environment, strings.TrimSpace(input.EnvironmentRef), actorID)
	if err != nil {
		return Deployment{}, err
	}
	if err := validateResolvedEnvironment(resolved); err != nil {
		return Deployment{}, err
	}
	projectUUID, actorUUID, err := parsePublishActors(projectID, actorID)
	if err != nil {
		return Deployment{}, err
	}
	reservation, err := s.reserve(ctx, projectUUID, actorUUID, expectedETag, input, source, resolved, "publish", nil)
	if err != nil {
		return Deployment{}, err
	}
	return s.deployReservation(ctx, actorUUID, reservation)
}

// Rollback creates a new immutable deployment version from a prior ready
// version. It never rewrites or reactivates a mutable directory in place.
func (s *PublishService) Rollback(ctx context.Context, deploymentID, actorID, expectedETag string, input RollbackInput) (Deployment, error) {
	deploymentUUID, err := uuid.Parse(deploymentID)
	if err != nil {
		return Deployment{}, Invalid("deploymentId", "deploymentId must be a UUID")
	}
	targetUUID, err := uuid.Parse(input.TargetVersionID)
	if err != nil {
		return Deployment{}, Invalid("targetVersionId", "targetVersionId must be a UUID")
	}
	if len(input.Message) > 1_000 || strings.ContainsRune(input.Message, '\x00') {
		return Deployment{}, Invalid("message", "message must contain at most 1000 safe characters")
	}
	var deployment deploymentModel
	if err := s.database.WithContext(ctx).Where("id = ?", deploymentUUID).Take(&deployment).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Deployment{}, notFound("deployment was not found")
		}
		return Deployment{}, wrapInternal("load deployment", err)
	}
	environment := Environment(deployment.Environment)
	if _, err := s.access.Authorize(ctx, deployment.ProjectID.String(), actorID, actionForEnvironment(environment)); err != nil {
		return Deployment{}, err
	}
	var target deploymentVersionModel
	if err := s.database.WithContext(ctx).Where("id = ? AND deployment_id = ? AND status = 'ready'", targetUUID, deploymentUUID).Take(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Deployment{}, notFound("ready rollback target version was not found")
		}
		return Deployment{}, wrapInternal("load rollback target", err)
	}
	reference := core.VersionRef{
		ArtifactID: target.WorkspaceArtifactID.String(), RevisionID: target.WorkspaceRevisionID.String(),
		ContentHash: target.WorkspaceContentHash,
	}
	workspace, err := s.loader.LoadFrozenWorkspace(ctx, deployment.ProjectID.String(), actorID, reference, actionForEnvironment(environment))
	if err != nil {
		return Deployment{}, err
	}
	if err := validatePublishableWorkspace(workspace); err != nil {
		return Deployment{}, err
	}
	resolved, err := s.environments.Resolve(ctx, deployment.ProjectID.String(), environment, target.EnvironmentRef, actorID)
	if err != nil {
		return Deployment{}, err
	}
	if err := validateResolvedEnvironment(resolved); err != nil {
		return Deployment{}, err
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return Deployment{}, Invalid("actorId", "actorId must be a UUID")
	}
	if target.QualityRunID == nil || target.BuildArtifactID == nil || target.BuildContentRef == nil || target.BuildContentHash == nil || target.BuildHash == nil || target.BuildEntryPath == nil || target.BuildFileCount == nil || target.BuildTotalBytes == nil {
		return Deployment{}, conflict("rollback target has no reusable immutable quality build artifact")
	}
	buildReference := BuildArtifactReference{
		ID: target.BuildArtifactID.String(), ContentRef: *target.BuildContentRef,
		ContentHash: *target.BuildContentHash, BuildHash: *target.BuildHash,
		EntryPath: *target.BuildEntryPath, FileCount: *target.BuildFileCount, TotalBytes: *target.BuildTotalBytes,
	}
	buildArtifact, err := s.quality.LoadBuildArtifact(ctx, deployment.ProjectID.String(), buildReference)
	if err != nil {
		return Deployment{}, err
	}
	if !exactVersionRefEqual(buildArtifact.WorkspaceRevision, workspace.Revision) {
		return Deployment{}, conflict("rollback build artifact no longer matches its exact workspace relation")
	}
	source := publishSource{
		workspace: workspace, buildManifestID: target.BuildManifestID, qualityRunID: target.QualityRunID,
		buildReference: &buildReference, buildArtifact: buildArtifact,
	}
	rollbackInput := PublishInput{
		DeploymentID: deploymentID, Environment: environment, EnvironmentRef: resolved.Reference,
		WorkspaceRevision: &reference, Message: strings.TrimSpace(input.Message),
	}
	reservation, err := s.reserve(ctx, deployment.ProjectID, actorUUID, expectedETag, rollbackInput, source, resolved, "rollback", &targetUUID)
	if err != nil {
		return Deployment{}, err
	}
	return s.deployReservation(ctx, actorUUID, reservation)
}

func (s *PublishService) resolvePublishSource(ctx context.Context, projectID, actorID string, action core.Action, revision *core.VersionRef, manifestID string) (publishSource, error) {
	if revision != nil {
		workspace, err := s.loader.LoadFrozenWorkspace(ctx, projectID, actorID, *revision, action)
		return publishSource{workspace: workspace}, err
	}
	bundle, err := s.loader.LoadBuildManifest(ctx, projectID, actorID, strings.TrimSpace(manifestID), action)
	if err != nil {
		return publishSource{}, err
	}
	if bundle.CurrentWorkspaceRevision == nil {
		return publishSource{}, conflict("build manifest does not pin a current WorkspaceRevision")
	}
	workspace, err := s.loader.LoadFrozenWorkspace(ctx, projectID, actorID, *bundle.CurrentWorkspaceRevision, action)
	if err != nil {
		return publishSource{}, err
	}
	parsed, _ := uuid.Parse(bundle.ID)
	return publishSource{workspace: workspace, buildManifestID: &parsed}, nil
}

func (s *PublishService) reserve(
	ctx context.Context,
	projectID, actorID uuid.UUID,
	expectedETag string,
	input PublishInput,
	source publishSource,
	resolved ResolvedEnvironment,
	action string,
	sourceVersionID *uuid.UUID,
) (publishReservation, error) {
	deploymentID := uuid.New()
	if strings.TrimSpace(input.DeploymentID) != "" {
		parsed, err := uuid.Parse(input.DeploymentID)
		if err != nil {
			return publishReservation{}, Invalid("deploymentId", "deploymentId must be a UUID")
		}
		deploymentID = parsed
	}
	versionID := uuid.New()
	now := s.now().UTC()
	publicNames, _ := json.Marshal(sortedStrings(resolved.Public))
	if source.qualityRunID == nil || source.buildReference == nil {
		return publishReservation{}, conflict("publishing requires a passing quality build artifact")
	}
	reservation := publishReservation{public: cloneStringMap(resolved.Public), artifact: source.buildArtifact}
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var deployment deploymentModel
		err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND environment = ?", projectID, input.Environment).Take(&deployment).Error
		created := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !created {
			return err
		}
		if created {
			if strings.TrimSpace(expectedETag) != "" {
				return &DeliveryError{Code: CodePrecondition, Status: 412, Detail: "If-Match cannot target a deployment that does not exist"}
			}
			deployment = deploymentModel{
				ID: deploymentID, ProjectID: projectID, Environment: string(input.Environment),
				EnvironmentRef: resolved.Reference, Provider: s.provider.Name(), Status: "deploying",
				Version: 1, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
			}
			if err := transaction.Create(&deployment).Error; err != nil {
				return conflict("a deployment for this environment was created concurrently; reload and retry with its ETag")
			}
		} else {
			if deployment.ID != deploymentID && strings.TrimSpace(input.DeploymentID) != "" {
				return conflict("deploymentId does not identify the project's selected environment")
			}
			if strings.TrimSpace(expectedETag) == "" || expectedETag != deploymentETag(deployment.ID.String(), deployment.Version) {
				return &DeliveryError{Code: CodePrecondition, Status: 412, Detail: "If-Match is required and must match the current deployment ETag"}
			}
			if deployment.Status == "deploying" {
				return conflict("a deployment operation is already in progress")
			}
			deployment.Version++
			deployment.Status = "deploying"
			deployment.EnvironmentRef = resolved.Reference
			deployment.Provider = s.provider.Name()
			deployment.LastError = nil
			deployment.UpdatedAt = now
			if err := transaction.Model(&deploymentModel{}).
				Where("id = ? AND version = ?", deployment.ID, deployment.Version-1).
				Updates(map[string]any{
					"version": deployment.Version, "status": deployment.Status, "environment_ref": deployment.EnvironmentRef,
					"provider": deployment.Provider, "last_error": nil, "updated_at": deployment.UpdatedAt,
				}).Error; err != nil {
				return err
			}
		}
		var lastVersion deploymentVersionModel
		number := uint64(1)
		if err := transaction.Where("deployment_id = ?", deployment.ID).Order("number DESC").Take(&lastVersion).Error; err == nil {
			number = lastVersion.Number + 1
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		version := deploymentVersionModel{
			ID: versionID, DeploymentID: deployment.ID, Number: number, Action: action, SourceVersionID: sourceVersionID,
			WorkspaceArtifactID: mustUUID(source.workspace.Revision.ArtifactID), WorkspaceRevisionID: mustUUID(source.workspace.Revision.RevisionID),
			WorkspaceContentHash: source.workspace.Revision.ContentHash, BuildManifestID: source.buildManifestID, QualityRunID: source.qualityRunID,
			BuildArtifactID: mustUUIDPointer(source.buildReference.ID), BuildContentRef: stringPointer(source.buildReference.ContentRef),
			BuildContentHash: stringPointer(source.buildReference.ContentHash), BuildHash: stringPointer(source.buildReference.BuildHash),
			BuildEntryPath: stringPointer(source.buildReference.EntryPath), BuildFileCount: intPointer(source.buildReference.FileCount), BuildTotalBytes: int64Pointer(source.buildReference.TotalBytes),
			ProviderRef: "", EntryPath: "", Checksum: "", FileCount: 0, TotalBytes: 0,
			EnvironmentRef: resolved.Reference, EnvironmentVariableNames: publicNames,
			Status: "deploying", Message: strings.TrimSpace(input.Message), CreatedBy: actorID, CreatedAt: now,
		}
		if err := transaction.Create(&version).Error; err != nil {
			return err
		}
		if err := createDeploymentLog(transaction, deployment.ID, &version.ID, "info", "Immutable deployment version reserved.", now); err != nil {
			return err
		}
		if err := recordAuditAndOutbox(ctx, transaction, projectID, actorID,
			"deployment.requested", "deployment", deployment.ID.String(), "deployment.requested", "worksflow.deployment.requested",
			map[string]any{
				"projectId": projectID.String(), "deploymentId": deployment.ID.String(), "deploymentVersionId": version.ID.String(),
				"environment": input.Environment, "workspaceRevisionId": source.workspace.Revision.RevisionID,
			},
		); err != nil {
			return err
		}
		reservation.deployment, reservation.version = deployment, version
		return nil
	})
	if err != nil {
		if deliveryError, ok := AsError(err); ok {
			return publishReservation{}, deliveryError
		}
		return publishReservation{}, wrapInternal("reserve deployment version", err)
	}
	return reservation, nil
}

func (s *PublishService) deployReservation(ctx context.Context, actorID uuid.UUID, reservation publishReservation) (Deployment, error) {
	request := ProviderRequest{
		DeploymentID: reservation.deployment.ID.String(), VersionID: reservation.version.ID.String(),
		Environment: Environment(reservation.deployment.Environment), EnvironmentRef: reservation.version.EnvironmentRef,
		BuildArtifact: reservation.artifact, PublicEnvironment: cloneStringMap(reservation.public),
	}
	result, providerErr := s.provider.Deploy(ctx, request)
	finalizeContext := context.WithoutCancel(ctx)
	if providerErr != nil {
		message := "The configured publish provider could not deploy the immutable workspace."
		if err := s.completeFailure(finalizeContext, actorID, reservation, message); err != nil {
			return Deployment{}, err
		}
		return Deployment{}, &DeliveryError{Code: CodeProviderFailure, Status: http.StatusFailedDependency, Detail: message, Cause: providerErr}
	}
	if err := validateProviderResult(result); err != nil {
		message := "The publish provider returned an invalid deployment result."
		if finalizeErr := s.completeFailure(finalizeContext, actorID, reservation, message); finalizeErr != nil {
			return Deployment{}, finalizeErr
		}
		return Deployment{}, &DeliveryError{Code: CodeProviderFailure, Status: http.StatusFailedDependency, Detail: message, Cause: err}
	}
	if err := s.completeSuccess(finalizeContext, actorID, reservation, result); err != nil {
		return Deployment{}, err
	}
	return s.Get(finalizeContext, reservation.deployment.ID.String(), actorID.String())
}

func (s *PublishService) completeSuccess(ctx context.Context, actorID uuid.UUID, reservation publishReservation, result ProviderResult) error {
	now := s.now().UTC()
	return s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		versionURL := result.PublicURL
		updated := transaction.Model(&deploymentVersionModel{}).
			Where("id = ? AND status = 'deploying'", reservation.version.ID).
			Updates(map[string]any{
				"provider_ref": result.Reference, "public_url": &versionURL, "entry_path": result.EntryPath,
				"checksum": result.Checksum, "file_count": result.FileCount, "total_bytes": result.TotalBytes, "status": "ready",
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return conflict("deployment version changed while the provider was completing")
		}
		nextVersion := reservation.deployment.Version + 1
		updated = transaction.Model(&deploymentModel{}).
			Where("id = ? AND version = ? AND status = 'deploying'", reservation.deployment.ID, reservation.deployment.Version).
			Updates(map[string]any{
				"status": "ready", "active_version_id": reservation.version.ID, "public_url": &versionURL,
				"last_error": nil, "version": nextVersion, "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return conflict("deployment changed while the provider was completing")
		}
		if err := createDeploymentLog(transaction, reservation.deployment.ID, &reservation.version.ID, "info", "Immutable deployment version is ready.", now); err != nil {
			return err
		}
		return recordAuditAndOutbox(ctx, transaction, reservation.deployment.ProjectID, actorID,
			"deployment.completed", "deployment", reservation.deployment.ID.String(), "deployment.completed", "worksflow.deployment.completed",
			map[string]any{
				"projectId": reservation.deployment.ProjectID.String(), "deploymentId": reservation.deployment.ID.String(),
				"deploymentVersionId": reservation.version.ID.String(), "environment": reservation.deployment.Environment,
				"workspaceRevisionId": reservation.artifact.WorkspaceRevision.RevisionID,
				"buildArtifactId":     reservation.artifact.ID, "publicUrl": result.PublicURL,
			},
		)
	})
}

func (s *PublishService) completeFailure(ctx context.Context, actorID uuid.UUID, reservation publishReservation, message string) error {
	now := s.now().UTC()
	return s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Model(&deploymentVersionModel{}).
			Where("id = ? AND status = 'deploying'", reservation.version.ID).
			Updates(map[string]any{"status": "failed"}).Error; err != nil {
			return err
		}
		nextVersion := reservation.deployment.Version + 1
		updated := transaction.Model(&deploymentModel{}).
			Where("id = ? AND version = ? AND status = 'deploying'", reservation.deployment.ID, reservation.deployment.Version).
			Updates(map[string]any{"status": "failed", "last_error": message, "version": nextVersion, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return conflict("deployment changed while the provider failure was being recorded")
		}
		if err := createDeploymentLog(transaction, reservation.deployment.ID, &reservation.version.ID, "error", message, now); err != nil {
			return err
		}
		return recordAuditAndOutbox(ctx, transaction, reservation.deployment.ProjectID, actorID,
			"deployment.failed", "deployment", reservation.deployment.ID.String(), "deployment.failed", "worksflow.deployment.failed",
			map[string]any{
				"projectId": reservation.deployment.ProjectID.String(), "deploymentId": reservation.deployment.ID.String(),
				"deploymentVersionId": reservation.version.ID.String(), "environment": reservation.deployment.Environment,
			},
		)
	})
}

func (s *PublishService) Get(ctx context.Context, deploymentID, actorID string) (Deployment, error) {
	id, err := uuid.Parse(deploymentID)
	if err != nil {
		return Deployment{}, Invalid("deploymentId", "deploymentId must be a UUID")
	}
	var model deploymentModel
	if err := s.database.WithContext(ctx).Where("id = ?", id).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Deployment{}, notFound("deployment was not found")
		}
		return Deployment{}, wrapInternal("load deployment", err)
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, core.ActionView); err != nil {
		return Deployment{}, err
	}
	return s.deploymentFromModel(ctx, model, true)
}

func (s *PublishService) List(ctx context.Context, projectID, actorID string) ([]Deployment, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, Invalid("projectId", "projectId must be a UUID")
	}
	var models []deploymentModel
	if err := s.database.WithContext(ctx).Where("project_id = ?", projectUUID).Order("updated_at DESC").Find(&models).Error; err != nil {
		return nil, wrapInternal("list deployments", err)
	}
	result := make([]Deployment, 0, len(models))
	for _, model := range models {
		deployment, err := s.deploymentFromModel(ctx, model, false)
		if err != nil {
			return nil, err
		}
		result = append(result, deployment)
	}
	return result, nil
}

func (s *PublishService) Logs(ctx context.Context, deploymentID, actorID string) ([]DeploymentLog, error) {
	id, err := uuid.Parse(deploymentID)
	if err != nil {
		return nil, Invalid("deploymentId", "deploymentId must be a UUID")
	}
	var deployment deploymentModel
	if err := s.database.WithContext(ctx).Where("id = ?", id).Take(&deployment).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, notFound("deployment was not found")
		}
		return nil, wrapInternal("load deployment", err)
	}
	if _, err := s.access.Authorize(ctx, deployment.ProjectID.String(), actorID, core.ActionView); err != nil {
		return nil, err
	}
	var models []deploymentLogModel
	if err := s.database.WithContext(ctx).Where("deployment_id = ?", id).Order("sequence ASC").Limit(1_000).Find(&models).Error; err != nil {
		return nil, wrapInternal("load deployment logs", err)
	}
	result := make([]DeploymentLog, 0, len(models))
	for _, model := range models {
		var versionID *string
		if model.DeploymentVersionID != nil {
			value := model.DeploymentVersionID.String()
			versionID = &value
		}
		result = append(result, DeploymentLog{
			ID: model.ID.String(), DeploymentID: model.DeploymentID.String(), DeploymentVersionID: versionID,
			Sequence: model.Sequence, Level: model.Level, Message: model.Message, CreatedAt: model.CreatedAt,
		})
	}
	return result, nil
}

func (s *PublishService) deploymentFromModel(ctx context.Context, model deploymentModel, includeVersions bool) (Deployment, error) {
	result := Deployment{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(), Environment: Environment(model.Environment),
		EnvironmentRef: model.EnvironmentRef, Provider: model.Provider, Status: model.Status,
		Version: model.Version, ETag: deploymentETag(model.ID.String(), model.Version),
		CreatedBy: model.CreatedBy.String(), CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
	}
	if model.ActiveVersionID != nil {
		value := model.ActiveVersionID.String()
		result.ActiveVersionID = &value
	}
	if model.PublicURL != nil {
		result.PublicURL = *model.PublicURL
	}
	if model.LastError != nil {
		result.LastError = *model.LastError
	}
	if !includeVersions {
		return result, nil
	}
	var models []deploymentVersionModel
	if err := s.database.WithContext(ctx).Where("deployment_id = ?", model.ID).Order("number DESC").Limit(maxDeploymentHistory).Find(&models).Error; err != nil {
		return Deployment{}, wrapInternal("load deployment versions", err)
	}
	result.Versions = make([]DeploymentVersion, 0, len(models))
	for _, version := range models {
		value, err := deploymentVersionFromModel(version)
		if err != nil {
			return Deployment{}, err
		}
		result.Versions = append(result.Versions, value)
	}
	return result, nil
}

func deploymentVersionFromModel(model deploymentVersionModel) (DeploymentVersion, error) {
	variableNames := []string{}
	if len(model.EnvironmentVariableNames) > 0 {
		if err := json.Unmarshal(model.EnvironmentVariableNames, &variableNames); err != nil {
			return DeploymentVersion{}, wrapInternal("decode deployment environment variable names", err)
		}
	}
	result := DeploymentVersion{
		ID: model.ID.String(), Number: model.Number, Action: model.Action,
		WorkspaceRevision: core.VersionRef{
			ArtifactID: model.WorkspaceArtifactID.String(), RevisionID: model.WorkspaceRevisionID.String(), ContentHash: model.WorkspaceContentHash,
		},
		Status: model.Status, EntryPath: model.EntryPath, Checksum: model.Checksum, FileCount: model.FileCount,
		TotalBytes: model.TotalBytes, EnvironmentRef: model.EnvironmentRef, EnvironmentVariableNames: variableNames,
		Message: model.Message, CreatedBy: model.CreatedBy.String(), CreatedAt: model.CreatedAt,
	}
	if model.SourceVersionID != nil {
		value := model.SourceVersionID.String()
		result.SourceVersionID = &value
	}
	if model.BuildManifestID != nil {
		value := model.BuildManifestID.String()
		result.BuildManifestID = &value
	}
	if model.QualityRunID != nil {
		value := model.QualityRunID.String()
		result.QualityRunID = &value
	}
	if model.BuildArtifactID != nil {
		if model.BuildContentRef == nil || model.BuildContentHash == nil || model.BuildHash == nil || model.BuildEntryPath == nil || model.BuildFileCount == nil || model.BuildTotalBytes == nil {
			return DeploymentVersion{}, conflict("deployment build artifact relation is incomplete")
		}
		result.BuildArtifact = &BuildArtifactReference{
			ID: model.BuildArtifactID.String(), ContentRef: *model.BuildContentRef,
			ContentHash: *model.BuildContentHash, BuildHash: *model.BuildHash,
			EntryPath: *model.BuildEntryPath, FileCount: *model.BuildFileCount, TotalBytes: *model.BuildTotalBytes,
		}
	}
	if model.PublicURL != nil {
		result.PublicURL = *model.PublicURL
	}
	return result, nil
}

func createDeploymentLog(transaction *gorm.DB, deploymentID uuid.UUID, versionID *uuid.UUID, level, message string, createdAt time.Time) error {
	var sequence uint64
	if err := transaction.Model(&deploymentLogModel{}).Where("deployment_id = ?", deploymentID).
		Select("COALESCE(MAX(sequence), 0)").Scan(&sequence).Error; err != nil {
		return err
	}
	return transaction.Create(&deploymentLogModel{
		ID: uuid.New(), DeploymentID: deploymentID, DeploymentVersionID: versionID,
		Sequence: sequence + 1, Level: level, Message: message, CreatedAt: createdAt,
	}).Error
}

func actionForEnvironment(environment Environment) core.Action {
	if environment == EnvironmentProduction {
		return core.ActionPublish
	}
	return core.ActionEdit
}

func validatePublishText(environmentRef, message string) error {
	if environmentRef != "" && !environmentReferencePattern.MatchString(environmentRef) {
		return Invalid("environmentRef", "environmentRef must be a safe opaque identifier of at most 512 characters")
	}
	if len(message) > 1_000 || strings.ContainsRune(message, '\x00') {
		return Invalid("message", "message must contain at most 1000 safe characters")
	}
	return nil
}

func validatePublishableWorkspace(workspace WorkspaceSnapshot) error {
	if len(workspace.Files) == 0 {
		return conflict("the frozen workspace contains no publishable files")
	}
	for _, file := range workspace.Files {
		if SensitivePath(file.Path) {
			return NewError(CodeSensitiveContent, 409, "secret-bearing files cannot be published")
		}
		if _, found := SensitiveFinding(file.Content); found {
			return NewError(CodeSensitiveContent, 409, "embedded credentials cannot be published")
		}
	}
	return nil
}

func validateResolvedEnvironment(environment ResolvedEnvironment) error {
	if !environmentReferencePattern.MatchString(environment.Reference) {
		return Invalid("environmentRef", "resolved environment reference must be a safe opaque identifier")
	}
	if len(environment.Public) > 128 {
		return Invalid("environmentRef", "resolved environment contains too many public variables")
	}
	for name, value := range environment.Public {
		if !publicEnvironmentNamePattern.MatchString(name) {
			return Invalid("environmentRef", "resolved environment contains a non-public variable name")
		}
		if len(value) > 8_192 || strings.ContainsRune(value, '\x00') {
			return Invalid("environmentRef", "resolved public environment value exceeds its safe size limit")
		}
		if _, found := SensitiveFinding(value); found {
			return NewError(CodeSensitiveContent, 409, "resolved public environment contains a credential-like value")
		}
	}
	if _, err := publicConnectOrigins(environment.Public); err != nil {
		return err
	}
	return nil
}

func validateProviderResult(result ProviderResult) error {
	if err := ensureLength("provider.reference", result.Reference, 1_024); err != nil {
		return err
	}
	if err := ensureLength("provider.publicUrl", result.PublicURL, 4_096); err != nil {
		return err
	}
	digest := strings.TrimPrefix(result.Checksum, "sha256:")
	decoded, digestErr := hex.DecodeString(digest)
	if !strings.HasPrefix(result.Checksum, "sha256:") || len(decoded) != 32 || digestErr != nil || result.Checksum != "sha256:"+strings.ToLower(digest) {
		return Invalid("provider.checksum", "provider checksum must be a sha256 digest")
	}
	if err := validatePublicURL(result.PublicURL); err != nil {
		return err
	}
	if _, err := SanitizePath(result.EntryPath); err != nil {
		return err
	}
	if result.FileCount <= 0 || result.FileCount > MaxWorkspaceFiles || result.TotalBytes < 0 || result.TotalBytes > MaxWorkspaceBytes {
		return Invalid("provider.result", "provider result counts exceed the configured limits")
	}
	return nil
}

func parsePublishActors(projectID, actorID string) (uuid.UUID, uuid.UUID, error) {
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return uuid.Nil, uuid.Nil, Invalid("projectId", "projectId must be a UUID")
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return uuid.Nil, uuid.Nil, Invalid("actorId", "actorId must be a UUID")
	}
	return projectUUID, actorUUID, nil
}

func mustUUID(value string) uuid.UUID {
	parsed, _ := uuid.Parse(value)
	return parsed
}

func mustUUIDPointer(value string) *uuid.UUID {
	parsed := mustUUID(value)
	return &parsed
}

func stringPointer(value string) *string { return &value }
func intPointer(value int) *int          { return &value }
func int64Pointer(value int64) *int64    { return &value }

func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func deploymentETag(id string, version uint64) string {
	return fmt.Sprintf(`"deployment:%s:%d"`, id, version)
}
