package designimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AccessAPI interface {
	Authorize(context.Context, string, string, core.Action) (core.Role, error)
}

type ArtifactAPI interface {
	Create(context.Context, string, string, core.CreateArtifactInput) (core.VersionedArtifact, error)
	CreateWithID(context.Context, string, string, string, core.CreateArtifactInput) (core.VersionedArtifact, error)
	Get(context.Context, string, string, bool) (core.VersionedArtifact, error)
	GetRevision(context.Context, string, string) (core.ArtifactRevision, error)
	CreateRevision(context.Context, string, string, string, core.CreateRevisionInput) (core.ArtifactRevision, error)
	CreateRevisionWithID(context.Context, string, string, string, string, core.CreateRevisionInput) (core.ArtifactRevision, error)
}

type ProposalAPI interface {
	CreateManifest(context.Context, string, string, core.CreateManifestInput) (domain.InputManifest, error)
	CreateManifestWithID(context.Context, string, string, string, core.CreateManifestInput) (domain.InputManifest, error)
	CreateProposal(context.Context, string, string, core.CreateProposalInput) (domain.OutputProposal, error)
	CreateProposalWithID(context.Context, string, string, string, core.CreateProposalInput) (domain.OutputProposal, error)
	GetManifest(context.Context, string, string) (domain.InputManifest, error)
	GetProposal(context.Context, string, string) (domain.OutputProposal, error)
	Decide(context.Context, string, string, core.DecideProposalInput) (domain.OutputProposal, error)
	Apply(context.Context, string, string, core.ApplyProposalInput) (core.ArtifactDraft, error)
}

type Service struct {
	database       *gorm.DB
	contents       content.Store
	access         AccessAPI
	artifacts      ArtifactAPI
	proposals      ProposalAPI
	now            func() time.Time
	maxUploadBytes int64
	createLease    time.Duration
	creationHook   func(string) error
}

type ServiceConfig struct {
	MaxSnapshotContentBytes int64
	CreateLease             time.Duration
}

const defaultCreateLease = 2 * time.Minute

const (
	stageSnapshotFrozen = "snapshot_frozen"
	stageTargetFrozen   = "target_frozen"
	stageManifestFrozen = "manifest_frozen"
	stageProposalReady  = "proposal_ready"
)

func NewService(database *gorm.DB, contents content.Store, access AccessAPI, artifacts ArtifactAPI, proposals ProposalAPI, configs ...ServiceConfig) (*Service, error) {
	if database == nil || contents == nil || access == nil || artifacts == nil || proposals == nil {
		return nil, errors.New("design import database, content store, access, artifact, and proposal services are required")
	}
	contentLimit := defaultSnapshotContentBytes
	if len(configs) > 0 && configs[0].MaxSnapshotContentBytes > 0 {
		contentLimit = configs[0].MaxSnapshotContentBytes
	}
	createLease := defaultCreateLease
	if len(configs) > 0 && configs[0].CreateLease > 0 {
		createLease = configs[0].CreateLease
	}
	return &Service{
		database: database, contents: contents, access: access, artifacts: artifacts, proposals: proposals,
		now: time.Now, maxUploadBytes: uploadLimitForSnapshotStore(contentLimit), createLease: createLease,
	}, nil
}

func (s *Service) Capabilities(ctx context.Context, projectID, actorID string) (Capabilities, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return Capabilities{}, err
	}
	return supportedCapabilities(s.maxUploadBytes), nil
}

func (s *Service) Create(ctx context.Context, projectID, actorID, commandKey string, input CreateInput) (Import, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, core.ActionEdit); err != nil {
		return Import{}, err
	}
	if strings.TrimSpace(commandKey) == "" || len(commandKey) > 128 {
		return Import{}, invalid("idempotencyKey", "a stable command key of at most 128 characters is required")
	}
	input.Mode = strings.TrimSpace(input.Mode)
	input.Title = strings.TrimSpace(input.Title)
	input.TargetPrototypeArtifactID = strings.TrimSpace(input.TargetPrototypeArtifactID)
	for index := range input.SelectedFrameIDs {
		input.SelectedFrameIDs[index] = strings.TrimSpace(input.SelectedFrameIDs[index])
	}
	if err := validateCreateInput(input); err != nil {
		return Import{}, err
	}
	upload, err := validateUploadWithLimit(input.SourceKind, *input.File, s.maxUploadBytes)
	if err != nil {
		return Import{}, err
	}
	if err := validateSelection(input.SelectedFrameIDs, upload.Catalog); err != nil {
		return Import{}, err
	}
	pageSpec, err := s.loadCurrentApprovedPageSpec(ctx, actorID, input.PageSpecRevision)
	if err != nil {
		return Import{}, err
	}
	projectUUID, actorUUID, err := parseProjectActor(projectID, actorID)
	if err != nil {
		return Import{}, err
	}
	if pageSpec.Artifact.ProjectID != projectID {
		return Import{}, core.ErrNotFound
	}
	var requestedTarget *core.VersionedArtifact
	if input.TargetPrototypeArtifactID != "" {
		target, targetErr := s.loadTargetPrototype(ctx, projectID, actorID, input.TargetPrototypeArtifactID)
		if targetErr != nil {
			return Import{}, targetErr
		}
		if err := ensureTargetPageSpec(target, input.PageSpecRevision); err != nil {
			return Import{}, err
		}
		if target.LatestRevision == nil {
			return Import{}, &Error{Kind: ErrConflict, Field: "targetPrototypeArtifactId", Detail: "target Prototype must have an immutable base revision"}
		}
		requestedTarget = &target
	}
	requestHash := sha256.Sum256([]byte(commandKey))
	requestKeyHash := hex.EncodeToString(requestHash[:])
	var existing importModel
	err = s.database.WithContext(ctx).Where("project_id = ? AND request_key_hash = ?", projectUUID, requestKeyHash).Take(&existing).Error
	if err == nil {
		if !sameCreateRequest(existing, input, upload) {
			return Import{}, ErrConflict
		}
		if creationRecoverable(existing) {
			if err := s.contents.Finalize(ctx, existing.SnapshotRef); err != nil {
				s.recordFailure(ctx, existing.ID, core.ErrContentNotReady, actorID)
				return Import{}, fmt.Errorf("%w: finalize design snapshot: %v", core.ErrContentNotReady, err)
			}
			if err := s.resumeCreate(ctx, &existing, actorID); err != nil {
				return Import{}, err
			}
		}
		return s.Get(ctx, existing.ID.String(), actorID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return Import{}, err
	}

	importID := designImportID(projectUUID, requestKeyHash)
	expectedPrototypeID := designImportResourceID(importID, "prototype-artifact")
	expectedBaseRevisionID := designImportResourceID(importID, "prototype-base-revision")
	if requestedTarget != nil {
		expectedPrototypeID = uuid.MustParse(requestedTarget.Artifact.ID)
		expectedBaseRevisionID = uuid.MustParse(requestedTarget.LatestRevision.ID)
	}
	expectedManifestID := designImportResourceID(importID, "input-manifest")
	expectedProposalID := designImportResourceID(importID, "output-proposal")
	now := s.now().UTC()
	sourceName := input.Title
	if sourceName == "" {
		sourceName = strings.TrimSuffix(upload.FileName, filepathExtension(upload.FileName))
	}
	selected, _ := json.Marshal(append([]string{}, input.SelectedFrameIDs...))
	envelope := SnapshotEnvelope{
		SchemaVersion: 1, SourceKind: input.SourceKind, SourceName: sourceName,
		MediaType: upload.MediaType, FileName: upload.FileName, ByteSize: int64(len(upload.Raw)),
		RawContentHash: upload.RawContentHash, ContentBase64: base64.StdEncoding.EncodeToString(upload.Raw),
		CapturedAt: now, ExtractedCatalog: upload.Catalog,
		Safety: json.RawMessage(`{"activeContentRejected":true,"externalSourceIsFact":false,"remoteFetchPerformed":false}`),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return Import{}, err
	}
	snapshotRef, err := s.contents.PutPending(ctx, projectID, "design_import_snapshot", importID.String(), 1, payload)
	if err != nil {
		return Import{}, err
	}
	abortSnapshot := true
	defer func() {
		if abortSnapshot {
			_ = s.contents.Abort(context.Background(), snapshotRef.ID)
		}
	}()
	fileName := upload.FileName
	model := importModel{
		ID: importID, ProjectID: projectUUID, SourceKind: string(input.SourceKind), SourceMode: "upload",
		SourceName: sourceName, FileName: &fileName, MediaType: upload.MediaType, ByteSize: int64(len(upload.Raw)),
		RawContentHash: upload.RawContentHash, SnapshotStore: "mongo", SnapshotRef: snapshotRef.ID,
		SnapshotContentHash: snapshotRef.ContentHash, SnapshotSchemaVersion: snapshotRef.SchemaVersion,
		SelectedFrameIDs: selected, PageSpecArtifactID: uuid.MustParse(input.PageSpecRevision.ArtifactID),
		PageSpecRevisionID: uuid.MustParse(input.PageSpecRevision.RevisionID), PageSpecContentHash: input.PageSpecRevision.ContentHash,
		CreatesPrototype:            input.TargetPrototypeArtifactID == "",
		ExpectedPrototypeArtifactID: expectedPrototypeID, ExpectedBaseRevisionID: expectedBaseRevisionID,
		ExpectedInputManifestID: expectedManifestID, ExpectedOutputProposalID: expectedProposalID,
		PipelineStage: stageSnapshotFrozen, Status: "creating", RequestKeyHash: requestKeyHash,
		Version: 1, CreatedBy: actorUUID, CreatedAt: now, UpdatedAt: now,
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&model).Error; err != nil {
			return err
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "design_import.snapshot_frozen", importID.String(), map[string]any{
			"sourceKind": input.SourceKind, "mediaType": upload.MediaType, "rawContentHash": upload.RawContentHash,
			"snapshotContentHash": snapshotRef.ContentHash,
		}); err != nil {
			return err
		}
		return enqueue(transaction, importID, "design_import.snapshot_frozen", map[string]any{
			"projectId": projectID, "designImportId": importID.String(), "sourceKind": input.SourceKind,
			"snapshotContentHash": snapshotRef.ContentHash,
		})
	})
	if err != nil {
		if isUniqueViolation(err) {
			var concurrent importModel
			if loadErr := s.database.WithContext(ctx).Where("project_id = ? AND request_key_hash = ?", projectUUID, requestKeyHash).Take(&concurrent).Error; loadErr == nil && sameCreateRequest(concurrent, input, upload) {
				if finalizeErr := s.contents.Finalize(ctx, concurrent.SnapshotRef); finalizeErr != nil {
					return Import{}, finalizeErr
				}
				if creationRecoverable(concurrent) {
					if resumeErr := s.resumeCreate(ctx, &concurrent, actorID); resumeErr != nil {
						return Import{}, resumeErr
					}
				}
				return s.Get(ctx, concurrent.ID.String(), actorID)
			}
		}
		return Import{}, err
	}
	abortSnapshot = false
	if err := s.contents.Finalize(ctx, snapshotRef.ID); err != nil {
		s.recordFailure(ctx, model.ID, core.ErrContentNotReady, actorID)
		return Import{}, fmt.Errorf("%w: finalize design snapshot: %v", core.ErrContentNotReady, err)
	}
	if err := s.resumeCreate(ctx, &model, actorID); err != nil {
		return Import{}, err
	}
	return s.Get(ctx, model.ID.String(), actorID)
}

type creationClaim struct {
	Token     uuid.UUID
	ActorID   uuid.UUID
	Version   uint64
	Stage     string
	ExpiresAt time.Time
}

func creationRecoverable(model importModel) bool {
	return (model.Status == "creating" || model.Status == "failed") && model.PipelineStage != stageProposalReady
}

func (s *Service) resumeCreate(ctx context.Context, model *importModel, actorID string) error {
	claim, err := s.claimCreation(ctx, model, actorID)
	if err != nil {
		return err
	}
	err = s.runClaimedCreation(ctx, model, &claim, actorID)
	if err != nil && !errors.Is(err, ErrProcessing) {
		s.recordCreationFailure(context.WithoutCancel(ctx), model, claim, err)
	}
	return err
}

func (s *Service) claimCreation(ctx context.Context, model *importModel, actorID string) (creationClaim, error) {
	actorUUID, err := uuid.Parse(strings.TrimSpace(actorID))
	if err != nil {
		return creationClaim{}, core.ErrInvalidInput
	}
	now := s.now().UTC()
	claim := creationClaim{
		Token: uuid.New(), ActorID: actorUUID, Version: model.Version + 1,
		Stage: model.PipelineStage, ExpiresAt: now.Add(s.createLease),
	}
	priorStatus := model.Status
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Model(&importModel{}).
			Where("id = ? AND version = ? AND status IN ? AND pipeline_stage <> ?", model.ID, model.Version, []string{"creating", "failed"}, stageProposalReady).
			Where("create_claim_token IS NULL OR create_claim_expires_at <= ?", now).
			Updates(map[string]any{
				"status": "creating", "failure_code": nil, "failure_detail": nil,
				"create_claim_token": claim.Token, "create_claimed_by": actorUUID,
				"create_claimed_at": now, "create_claim_expires_at": claim.ExpiresAt,
				"version": gorm.Expr("version + 1"), "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			var current importModel
			if loadErr := transaction.Where("id = ?", model.ID).Take(&current).Error; loadErr != nil {
				return loadErr
			}
			if current.CreateClaimToken != nil && current.CreateClaimExpiresAt != nil && current.CreateClaimExpiresAt.After(now) {
				return ErrProcessing
			}
			return ErrConflict
		}
		eventType := "design_import.creation_claimed"
		if priorStatus == "failed" {
			eventType = "design_import.creation_recovered"
		}
		metadata := map[string]any{"stage": model.PipelineStage, "leaseExpiresAt": claim.ExpiresAt, "priorStatus": priorStatus}
		if err := insertAudit(transaction, model.ProjectID, actorUUID, eventType, model.ID.String(), metadata); err != nil {
			return err
		}
		return enqueue(transaction, model.ID, eventType, map[string]any{
			"projectId": model.ProjectID.String(), "designImportId": model.ID.String(),
			"stage": model.PipelineStage, "leaseExpiresAt": claim.ExpiresAt,
		})
	})
	if err != nil {
		return creationClaim{}, err
	}
	model.Status = "creating"
	model.FailureCode = nil
	model.FailureDetail = nil
	model.CreateClaimToken = &claim.Token
	model.CreateClaimedBy = &claim.ActorID
	model.CreateClaimedAt = &now
	model.CreateClaimExpiresAt = &claim.ExpiresAt
	model.Version = claim.Version
	return claim, nil
}

func (s *Service) runClaimedCreation(ctx context.Context, model *importModel, claim *creationClaim, actorID string) error {
	pageRef := core.VersionRef{
		ArtifactID: model.PageSpecArtifactID.String(), RevisionID: model.PageSpecRevisionID.String(),
		ContentHash: model.PageSpecContentHash,
	}
	pageSpec, err := s.loadCurrentApprovedPageSpec(ctx, actorID, pageRef)
	if err != nil {
		return err
	}
	envelope, err := s.loadEnvelope(ctx, *model)
	if err != nil {
		return err
	}

	if model.PipelineStage == stageSnapshotFrozen {
		target, baseRevision, targetErr := s.ensureCreationTarget(ctx, *model, actorID, pageRef, pageSpec, envelope)
		if targetErr != nil {
			return targetErr
		}
		targetID := uuid.MustParse(target.Artifact.ID)
		baseID := uuid.MustParse(baseRevision.ID)
		if err := s.checkpointCreation(ctx, model, claim, stageTargetFrozen, "creating", map[string]any{
			"prototype_artifact_id": targetID, "base_revision_id": baseID,
		}, "design_import.target_frozen", map[string]any{
			"prototypeArtifactId": targetID.String(), "baseRevisionId": baseID.String(),
		}); err != nil {
			return err
		}
		model.PrototypeArtifactID = &targetID
		model.BaseRevisionID = &baseID
	}

	if model.PipelineStage == stageTargetFrozen {
		baseRef, baseErr := s.loadCreationBase(ctx, *model, actorID, pageRef)
		if baseErr != nil {
			return baseErr
		}
		manifestInput, inputErr := designImportManifestInput(*model, pageRef, baseRef)
		if inputErr != nil {
			return inputErr
		}
		manifest, manifestErr := s.ensureCreationManifest(ctx, *model, actorID, manifestInput)
		if manifestErr != nil {
			return manifestErr
		}
		if err := s.runCreationHook("input_manifest"); err != nil {
			return err
		}
		manifestID := uuid.MustParse(manifest.ID)
		if err := s.checkpointCreation(ctx, model, claim, stageManifestFrozen, "creating", map[string]any{
			"input_manifest_id": manifestID,
		}, "design_import.manifest_frozen", map[string]any{
			"manifestId": manifest.ID, "manifestHash": manifest.Hash,
		}); err != nil {
			return err
		}
		model.InputManifestID = &manifestID
	}

	if model.PipelineStage == stageManifestFrozen {
		if model.PrototypeArtifactID == nil || model.InputManifestID == nil {
			return core.ErrConflict
		}
		finalContent, buildErr := buildPrototypeContent(*model, envelope, pageSpec.LatestRevision, false)
		if buildErr != nil {
			return buildErr
		}
		operationID := "design-import-" + model.ID.String()
		proposalInput := core.CreateProposalInput{
			ManifestID: model.InputManifestID.String(), ArtifactID: model.PrototypeArtifactID.String(),
			Operations: []domain.ProposalOperation{{
				ID: operationID, Kind: domain.OperationReplace, Path: "", Value: finalContent,
				Rationale: "Convert the frozen external design snapshot into an internal, reviewable Prototype artifact.",
			}},
			Assumptions: []string{"The uploaded design export is untrusted input and does not override approved project facts."},
			Questions:   []string{"Verify page, component, state, and interaction mappings before approving this import."},
		}
		proposal, proposalErr := s.ensureCreationProposal(ctx, *model, actorID, proposalInput)
		if proposalErr != nil {
			return proposalErr
		}
		if err := s.runCreationHook("output_proposal"); err != nil {
			return err
		}
		proposalID := uuid.MustParse(proposal.ID)
		if err := s.checkpointCreation(ctx, model, claim, stageProposalReady, "open", map[string]any{
			"output_proposal_id": proposalID, "operation_id": operationID,
		}, "design_import.proposal_created", map[string]any{
			"prototypeArtifactId": model.PrototypeArtifactID.String(), "proposalId": proposal.ID,
		}); err != nil {
			return err
		}
		model.OutputProposalID = &proposalID
		model.OperationID = &operationID
	}

	if model.PipelineStage != stageProposalReady || model.Status != "open" {
		return core.ErrConflict
	}
	return nil
}

func (s *Service) ensureCreationTarget(
	ctx context.Context,
	model importModel,
	actorID string,
	pageRef core.VersionRef,
	pageSpec core.VersionedArtifact,
	envelope SnapshotEnvelope,
) (core.VersionedArtifact, core.ArtifactRevision, error) {
	expectedArtifactID := model.ExpectedPrototypeArtifactID.String()
	expectedBaseID := model.ExpectedBaseRevisionID.String()
	if !model.CreatesPrototype {
		target, err := s.loadTargetPrototype(ctx, model.ProjectID.String(), actorID, expectedArtifactID)
		if err != nil {
			return core.VersionedArtifact{}, core.ArtifactRevision{}, err
		}
		if err := ensureTargetPageSpec(target, pageRef); err != nil {
			return core.VersionedArtifact{}, core.ArtifactRevision{}, err
		}
		if target.LatestRevision == nil || target.LatestRevision.ID != expectedBaseID {
			return core.VersionedArtifact{}, core.ArtifactRevision{}, &Error{
				Kind: ErrConflict, Field: "targetPrototypeArtifactId",
				Detail: "target Prototype changed before the import acquired its creation claim",
			}
		}
		return target, *target.LatestRevision, nil
	}

	target, err := s.artifacts.Get(ctx, expectedArtifactID, actorID, true)
	if errors.Is(err, core.ErrNotFound) {
		placeholder, buildErr := buildPrototypeContent(model, envelope, pageSpec.LatestRevision, true)
		if buildErr != nil {
			return core.VersionedArtifact{}, core.ArtifactRevision{}, buildErr
		}
		target, err = s.artifacts.CreateWithID(ctx, model.ProjectID.String(), actorID, expectedArtifactID, core.CreateArtifactInput{
			Kind: "prototype", ArtifactKey: importArtifactKey(model.ID), Title: model.SourceName,
			SchemaVersion: 1, Content: placeholder,
			SourceVersions: []core.ArtifactSourceInput{{Ref: pageRef, Purpose: "page_spec", Required: true}},
		})
		if err != nil {
			// A worker whose lease expired may have committed the stable artifact
			// while this worker was still executing. Only the exact reserved ID is
			// recoverable; the original error is returned if it cannot be loaded.
			if recovered, loadErr := s.artifacts.Get(ctx, expectedArtifactID, actorID, true); loadErr == nil {
				target, err = recovered, nil
			}
		}
	}
	if err != nil {
		return core.VersionedArtifact{}, core.ArtifactRevision{}, err
	}
	if target.Artifact.ID != expectedArtifactID || target.Artifact.ProjectID != model.ProjectID.String() ||
		target.Artifact.Kind != "prototype" || target.Artifact.ArtifactKey != importArtifactKey(model.ID) {
		return core.VersionedArtifact{}, core.ArtifactRevision{}, core.ErrConflict
	}
	if target.Draft != nil && !hasExactPageSpecSource(target.Draft.SourceVersions, pageRef) {
		return core.VersionedArtifact{}, core.ArtifactRevision{}, core.ErrConflict
	}
	if err := s.runCreationHook("prototype_artifact"); err != nil {
		return core.VersionedArtifact{}, core.ArtifactRevision{}, err
	}

	baseRevision, err := s.artifacts.GetRevision(ctx, expectedBaseID, actorID)
	if errors.Is(err, core.ErrNotFound) {
		if target.LatestRevision != nil && target.LatestRevision.ID != expectedBaseID {
			return core.VersionedArtifact{}, core.ArtifactRevision{}, core.ErrConflict
		}
		if target.Draft == nil {
			return core.VersionedArtifact{}, core.ArtifactRevision{}, core.ErrConflict
		}
		baseRevision, err = s.artifacts.CreateRevisionWithID(
			ctx, expectedArtifactID, actorID, target.Draft.ETag, expectedBaseID,
			core.CreateRevisionInput{
				ChangeSource:  "system",
				ChangeSummary: "Freeze the clean base for design import " + model.ID.String(),
			},
		)
		if err != nil {
			if recovered, loadErr := s.artifacts.GetRevision(ctx, expectedBaseID, actorID); loadErr == nil {
				baseRevision, err = recovered, nil
			}
		}
	}
	if err != nil {
		return core.VersionedArtifact{}, core.ArtifactRevision{}, err
	}
	if baseRevision.ID != expectedBaseID || baseRevision.ArtifactID != expectedArtifactID {
		return core.VersionedArtifact{}, core.ArtifactRevision{}, core.ErrConflict
	}
	if err := s.runCreationHook("base_revision"); err != nil {
		return core.VersionedArtifact{}, core.ArtifactRevision{}, err
	}
	target, err = s.loadTargetPrototype(ctx, model.ProjectID.String(), actorID, expectedArtifactID)
	if err != nil {
		return core.VersionedArtifact{}, core.ArtifactRevision{}, err
	}
	if err := ensureTargetPageSpec(target, pageRef); err != nil {
		return core.VersionedArtifact{}, core.ArtifactRevision{}, err
	}
	if target.LatestRevision == nil || target.LatestRevision.ID != expectedBaseID ||
		target.LatestRevision.ContentHash != baseRevision.ContentHash {
		return core.VersionedArtifact{}, core.ArtifactRevision{}, core.ErrConflict
	}
	return target, baseRevision, nil
}

func (s *Service) loadCreationBase(ctx context.Context, model importModel, actorID string, pageRef core.VersionRef) (core.VersionRef, error) {
	if model.PrototypeArtifactID == nil || model.BaseRevisionID == nil ||
		*model.PrototypeArtifactID != model.ExpectedPrototypeArtifactID || *model.BaseRevisionID != model.ExpectedBaseRevisionID {
		return core.VersionRef{}, core.ErrConflict
	}
	target, err := s.loadTargetPrototype(ctx, model.ProjectID.String(), actorID, model.ExpectedPrototypeArtifactID.String())
	if err != nil {
		return core.VersionRef{}, err
	}
	if err := ensureTargetPageSpec(target, pageRef); err != nil {
		return core.VersionRef{}, err
	}
	if target.LatestRevision == nil || target.LatestRevision.ID != model.ExpectedBaseRevisionID.String() {
		return core.VersionRef{}, &Error{
			Kind: ErrConflict, Field: "targetPrototypeArtifactId",
			Detail: "target Prototype changed before its import manifest was frozen",
		}
	}
	base, err := s.artifacts.GetRevision(ctx, model.ExpectedBaseRevisionID.String(), actorID)
	if err != nil {
		return core.VersionRef{}, err
	}
	if base.ArtifactID != model.ExpectedPrototypeArtifactID.String() || base.ContentHash != target.LatestRevision.ContentHash {
		return core.VersionRef{}, core.ErrConflict
	}
	return core.VersionRef{ArtifactID: base.ArtifactID, RevisionID: base.ID, ContentHash: base.ContentHash}, nil
}

func designImportManifestInput(model importModel, pageRef, baseRef core.VersionRef) (core.CreateManifestInput, error) {
	constraints, err := domain.CanonicalJSON(map[string]any{
		"schemaVersion": 1,
		"snapshot": map[string]any{
			"designImportId": model.ID.String(), "contentStore": model.SnapshotStore,
			"contentRef": model.SnapshotRef, "contentHash": model.SnapshotContentHash,
			"rawContentHash": model.RawContentHash, "sourceKind": model.SourceKind,
			"mediaType": model.MediaType, "byteSize": model.ByteSize,
		},
		"selectedFrameIds": decodeStringArray(model.SelectedFrameIDs),
		"trust":            map[string]any{"externalSourceIsFact": false, "reviewRequired": true},
	})
	if err != nil {
		return core.CreateManifestInput{}, err
	}
	return core.CreateManifestInput{
		JobType: "design_import_to_prototype", BaseRevision: &baseRef,
		Sources:     []core.ManifestSourceInput{{Ref: pageRef, Purpose: "page_spec"}},
		Constraints: constraints, OutputSchemaVersion: "prototype@1",
	}, nil
}

func (s *Service) ensureCreationManifest(
	ctx context.Context,
	model importModel,
	actorID string,
	input core.CreateManifestInput,
) (domain.InputManifest, error) {
	expectedID := model.ExpectedInputManifestID.String()
	manifest, err := s.proposals.GetManifest(ctx, expectedID, actorID)
	if errors.Is(err, core.ErrNotFound) {
		manifest, err = s.proposals.CreateManifestWithID(ctx, model.ProjectID.String(), actorID, expectedID, input)
		if err != nil {
			if recovered, loadErr := s.proposals.GetManifest(ctx, expectedID, actorID); loadErr == nil {
				manifest, err = recovered, nil
			}
		}
	}
	if err != nil {
		return domain.InputManifest{}, err
	}
	if !manifestMatchesCreationContract(manifest, model, input) {
		return domain.InputManifest{}, core.ErrConflict
	}
	return manifest, nil
}

func manifestMatchesCreationContract(manifest domain.InputManifest, model importModel, input core.CreateManifestInput) bool {
	if manifest.ID != model.ExpectedInputManifestID.String() || manifest.ProjectID != model.ProjectID.String() ||
		manifest.JobType != input.JobType || manifest.OutputSchemaVersion != input.OutputSchemaVersion ||
		manifest.BaseRevision == nil || input.BaseRevision == nil || len(manifest.Sources) != len(input.Sources) {
		return false
	}
	if !domainRefMatchesCore(*manifest.BaseRevision, *input.BaseRevision) {
		return false
	}
	for index, source := range manifest.Sources {
		if source.Purpose != input.Sources[index].Purpose || !domainRefMatchesCore(source.Ref, input.Sources[index].Ref) {
			return false
		}
	}
	return canonicalJSONEqual(manifest.Constraints, input.Constraints)
}

func (s *Service) ensureCreationProposal(
	ctx context.Context,
	model importModel,
	actorID string,
	input core.CreateProposalInput,
) (domain.OutputProposal, error) {
	expectedID := model.ExpectedOutputProposalID.String()
	manifest, err := s.proposals.GetManifest(ctx, input.ManifestID, actorID)
	if err != nil || manifest.ID != model.ExpectedInputManifestID.String() || manifest.BaseRevision == nil {
		if err != nil {
			return domain.OutputProposal{}, err
		}
		return domain.OutputProposal{}, core.ErrConflict
	}
	proposal, err := s.proposals.GetProposal(ctx, expectedID, actorID)
	if errors.Is(err, core.ErrNotFound) {
		proposal, err = s.proposals.CreateProposalWithID(ctx, model.ProjectID.String(), actorID, expectedID, input)
		if err != nil {
			if recovered, loadErr := s.proposals.GetProposal(ctx, expectedID, actorID); loadErr == nil {
				proposal, err = recovered, nil
			}
		}
	}
	if err != nil {
		return domain.OutputProposal{}, err
	}
	if !proposalMatchesCreationContract(proposal, model, input) || proposal.Manifest != manifest.Ref() ||
		!proposal.BaseRevision.Equal(*manifest.BaseRevision) {
		return domain.OutputProposal{}, core.ErrConflict
	}
	return proposal, nil
}

func proposalMatchesCreationContract(proposal domain.OutputProposal, model importModel, input core.CreateProposalInput) bool {
	if proposal.ID != model.ExpectedOutputProposalID.String() || proposal.ProjectID != model.ProjectID.String() ||
		proposal.ArtifactID != input.ArtifactID || proposal.Manifest.ID != input.ManifestID ||
		len(proposal.Operations) != len(input.Operations) || len(proposal.Assumptions) != len(input.Assumptions) ||
		len(proposal.Questions) != len(input.Questions) {
		return false
	}
	for index, operation := range proposal.Operations {
		expected := input.Operations[index]
		if operation.ID != expected.ID || operation.Kind != expected.Kind || operation.Path != expected.Path ||
			operation.Rationale != expected.Rationale || !canonicalJSONEqual(operation.Value, expected.Value) {
			return false
		}
	}
	for index := range input.Assumptions {
		if proposal.Assumptions[index] != input.Assumptions[index] {
			return false
		}
	}
	for index := range input.Questions {
		if proposal.Questions[index] != input.Questions[index] {
			return false
		}
	}
	return proposal.BaseRevision.ArtifactID == model.ExpectedPrototypeArtifactID.String() &&
		proposal.BaseRevision.RevisionID == model.ExpectedBaseRevisionID.String()
}

func (s *Service) checkpointCreation(
	ctx context.Context,
	model *importModel,
	claim *creationClaim,
	nextStage, nextStatus string,
	values map[string]any,
	eventType string,
	metadata map[string]any,
) error {
	now := s.now().UTC()
	nextExpiry := now.Add(s.createLease)
	updates := make(map[string]any, len(values)+10)
	for key, value := range values {
		updates[key] = value
	}
	updates["pipeline_stage"] = nextStage
	updates["status"] = nextStatus
	updates["failure_code"] = nil
	updates["failure_detail"] = nil
	updates["version"] = gorm.Expr("version + 1")
	updates["updated_at"] = now
	if nextStage == stageProposalReady {
		updates["create_claim_token"] = nil
		updates["create_claimed_by"] = nil
		updates["create_claimed_at"] = nil
		updates["create_claim_expires_at"] = nil
	} else {
		updates["create_claim_expires_at"] = nextExpiry
	}
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Model(&importModel{}).
			Where(
				"id = ? AND version = ? AND status = 'creating' AND pipeline_stage = ? AND create_claim_token = ? AND create_claim_expires_at > ?",
				model.ID, claim.Version, claim.Stage, claim.Token, now,
			).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrProcessing
		}
		payload := map[string]any{
			"projectId": model.ProjectID.String(), "designImportId": model.ID.String(),
			"fromStage": claim.Stage, "stage": nextStage,
		}
		for key, value := range metadata {
			payload[key] = value
		}
		if err := insertAudit(transaction, model.ProjectID, claim.ActorID, eventType, model.ID.String(), payload); err != nil {
			return err
		}
		return enqueue(transaction, model.ID, eventType, payload)
	})
	if err != nil {
		return err
	}
	claim.Version++
	claim.Stage = nextStage
	claim.ExpiresAt = nextExpiry
	model.Version = claim.Version
	model.PipelineStage = nextStage
	model.Status = nextStatus
	if nextStage == stageProposalReady {
		model.CreateClaimToken = nil
		model.CreateClaimedBy = nil
		model.CreateClaimedAt = nil
		model.CreateClaimExpiresAt = nil
	} else {
		model.CreateClaimExpiresAt = &nextExpiry
	}
	return nil
}

func (s *Service) runCreationHook(stage string) error {
	if s.creationHook == nil {
		return nil
	}
	return s.creationHook(stage)
}

func domainRefMatchesCore(actual domain.ArtifactRef, expected core.VersionRef) bool {
	anchor := ""
	if expected.AnchorID != nil {
		anchor = strings.TrimSpace(*expected.AnchorID)
	}
	return actual.ArtifactID == expected.ArtifactID && actual.RevisionID == expected.RevisionID &&
		actual.ContentHash == expected.ContentHash && actual.AnchorID == anchor
}

func canonicalJSONEqual(left, right json.RawMessage) bool {
	leftCanonical, leftErr := domain.CanonicalJSON(left)
	rightCanonical, rightErr := domain.CanonicalJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func (s *Service) Get(ctx context.Context, importID, actorID string) (Import, error) {
	id, err := uuid.Parse(strings.TrimSpace(importID))
	if err != nil {
		return Import{}, invalid("designImportId", "must be a UUID")
	}
	var model importModel
	if err := s.database.WithContext(ctx).Where("id = ?", id).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Import{}, core.ErrNotFound
		}
		return Import{}, err
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, core.ActionView); err != nil {
		return Import{}, err
	}
	return s.importFromModel(ctx, model, actorID)
}

func (s *Service) List(ctx context.Context, projectID, actorID, status string) ([]Import, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, invalid("projectId", "must be a UUID")
	}
	query := s.database.WithContext(ctx).Where("project_id = ?", projectUUID)
	if status = strings.TrimSpace(status); status != "" {
		if !validImportStatus(status) {
			return nil, invalid("status", "is not a design import status")
		}
		query = query.Where("status = ?", status)
	}
	var models []importModel
	if err := query.Order("created_at DESC, id DESC").Find(&models).Error; err != nil {
		return nil, err
	}
	result := make([]Import, 0, len(models))
	for _, model := range models {
		item, err := s.importFromModel(ctx, model, actorID)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Service) Decide(ctx context.Context, importID, actorID, expectedETag string, input DecisionInput) (Import, error) {
	id, err := uuid.Parse(strings.TrimSpace(importID))
	if err != nil {
		return Import{}, invalid("designImportId", "must be a UUID")
	}
	input.Decision = strings.TrimSpace(input.Decision)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Decision != "approve" && input.Decision != "reject" {
		return Import{}, invalid("decision", "must be approve or reject")
	}
	if len(input.Reason) > 2000 {
		return Import{}, invalid("reason", "must be at most 2000 characters")
	}
	var model importModel
	if err := s.database.WithContext(ctx).Where("id = ?", id).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Import{}, core.ErrNotFound
		}
		return Import{}, err
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, core.ActionReview); err != nil {
		return Import{}, err
	}
	actorUUID, parseErr := uuid.Parse(actorID)
	if parseErr != nil {
		return Import{}, core.ErrInvalidInput
	}
	// External design snapshots are untrusted input. The actor who introduced
	// that input cannot be the independent reviewer who accepts or rejects its
	// conversion proposal, even when their project role otherwise allows review.
	if actorUUID == model.CreatedBy {
		return Import{}, core.ErrForbidden
	}
	if expectedETag != importETag(model.ID, model.Version) || input.Version != model.Version {
		return Import{}, ErrConflict
	}
	if model.Status == "applied" || model.Status == "rejected" {
		if (model.Status == "applied" && input.Decision == "approve") || (model.Status == "rejected" && input.Decision == "reject") {
			return s.importFromModel(ctx, model, actorID)
		}
		return Import{}, ErrConflict
	}
	if model.OutputProposalID == nil || model.OperationID == nil || model.PrototypeArtifactID == nil {
		return Import{}, &Error{Kind: ErrConflict, Field: "status", Detail: "the import proposal is not ready for review"}
	}
	if input.Decision == "reject" {
		proposal, loadErr := s.proposals.GetProposal(ctx, model.OutputProposalID.String(), actorID)
		if loadErr != nil {
			return Import{}, loadErr
		}
		operation := findOperation(proposal, *model.OperationID)
		if operation == nil {
			return Import{}, core.ErrConflict
		}
		if operation.Decision == domain.DecisionPending {
			proposal, err = s.proposals.Decide(ctx, proposal.ID, actorID, core.DecideProposalInput{
				OperationID: *model.OperationID, Decision: domain.DecisionRejected, Reason: input.Reason, Version: proposal.Version,
			})
			if err != nil {
				return Import{}, err
			}
		} else if operation.Decision != domain.DecisionRejected {
			return Import{}, ErrConflict
		}
		now := s.now().UTC()
		if err := s.finishDecision(ctx, model, actorUUID, "rejected", nil, now); err != nil {
			return Import{}, err
		}
		return s.Get(ctx, model.ID.String(), actorID)
	}

	if err := s.claimApplying(ctx, model, actorUUID); err != nil {
		return Import{}, err
	}
	model.Status = "applying"
	model.Version++
	proposal, err := s.proposals.GetProposal(ctx, model.OutputProposalID.String(), actorID)
	if err != nil {
		s.recordFailure(ctx, model.ID, err, actorID)
		return Import{}, err
	}
	operation := findOperation(proposal, *model.OperationID)
	if operation == nil || operation.Decision == domain.DecisionRejected {
		err = ErrConflict
		s.recordFailure(ctx, model.ID, err, actorID)
		return Import{}, err
	}
	if operation.Decision == domain.DecisionPending {
		proposal, err = s.proposals.Decide(ctx, proposal.ID, actorID, core.DecideProposalInput{
			OperationID: *model.OperationID, Decision: domain.DecisionAccepted, Reason: input.Reason, Version: proposal.Version,
		})
		if err != nil {
			s.recordFailure(ctx, model.ID, err, actorID)
			return Import{}, err
		}
	}
	var draft core.ArtifactDraft
	if proposal.Status != domain.ProposalApplied && proposal.Status != domain.ProposalPartiallyApplied {
		draft, err = s.proposals.Apply(ctx, proposal.ID, actorID, core.ApplyProposalInput{Version: proposal.Version})
		if err != nil {
			s.recordFailure(ctx, model.ID, err, actorID)
			return Import{}, err
		}
	}
	target, err := s.artifacts.Get(ctx, model.PrototypeArtifactID.String(), actorID, true)
	if err != nil {
		s.recordFailure(ctx, model.ID, err, actorID)
		return Import{}, err
	}
	var applied core.ArtifactRevision
	if target.LatestRevision != nil && target.LatestRevision.ProposalID != nil && *target.LatestRevision.ProposalID == proposal.ID {
		applied = *target.LatestRevision
	} else {
		if draft.ID == "" && target.Draft != nil {
			draft = *target.Draft
		}
		if draft.ID == "" {
			err = core.ErrConflict
			s.recordFailure(ctx, model.ID, err, actorID)
			return Import{}, err
		}
		applied, err = s.artifacts.CreateRevision(ctx, model.PrototypeArtifactID.String(), actorID, draft.ETag, core.CreateRevisionInput{
			ChangeSource: "import", ChangeSummary: "Apply reviewed design import " + model.ID.String(),
		})
		if err != nil {
			s.recordFailure(ctx, model.ID, err, actorID)
			return Import{}, err
		}
	}
	appliedID := uuid.MustParse(applied.ID)
	now := s.now().UTC()
	if err := s.finishDecision(ctx, model, actorUUID, "applied", &appliedID, now); err != nil {
		return Import{}, err
	}
	return s.Get(ctx, model.ID.String(), actorID)
}

func (s *Service) loadCurrentApprovedPageSpec(ctx context.Context, actorID string, ref core.VersionRef) (core.VersionedArtifact, error) {
	revision, err := s.artifacts.GetRevision(ctx, ref.RevisionID, actorID)
	if err != nil {
		return core.VersionedArtifact{}, err
	}
	if revision.ArtifactID != ref.ArtifactID || revision.ContentHash != ref.ContentHash || revision.WorkflowStatus != "approved" {
		return core.VersionedArtifact{}, &Error{Kind: ErrConflict, Field: "pageSpecRevision", Detail: "must match an approved immutable PageSpec revision"}
	}
	artifact, err := s.artifacts.Get(ctx, ref.ArtifactID, actorID, true)
	if err != nil {
		return core.VersionedArtifact{}, err
	}
	if artifact.Artifact.Kind != "page_spec" || artifact.Artifact.Lifecycle != "active" || artifact.Artifact.SyncStatus != "current" || artifact.Artifact.LatestApprovedRevisionID == nil || *artifact.Artifact.LatestApprovedRevisionID != ref.RevisionID {
		return core.VersionedArtifact{}, &Error{Kind: ErrConflict, Field: "pageSpecRevision", Detail: "formal imports require the current approved PageSpec revision"}
	}
	artifact.LatestRevision = &revision
	return artifact, nil
}

func (s *Service) loadTargetPrototype(ctx context.Context, projectID, actorID, artifactID string) (core.VersionedArtifact, error) {
	target, err := s.artifacts.Get(ctx, artifactID, actorID, true)
	if err != nil {
		return core.VersionedArtifact{}, err
	}
	if target.Artifact.ProjectID != projectID || target.Artifact.Kind != "prototype" || target.Artifact.Lifecycle != "active" {
		return core.VersionedArtifact{}, core.ErrNotFound
	}
	return target, nil
}

func ensureTargetPageSpec(target core.VersionedArtifact, expected core.VersionRef) error {
	if target.LatestRevision == nil {
		return &Error{Kind: ErrConflict, Field: "targetPrototypeArtifactId", Detail: "target Prototype must have an immutable base revision"}
	}
	if !hasExactPageSpecSource(target.LatestRevision.SourceVersions, expected) {
		return &Error{Kind: ErrConflict, Field: "targetPrototypeArtifactId", Detail: "target Prototype is pinned to a different PageSpec revision"}
	}
	if target.Draft != nil && !hasExactPageSpecSource(target.Draft.SourceVersions, expected) {
		return &Error{Kind: ErrConflict, Field: "targetPrototypeArtifactId", Detail: "target Prototype draft is pinned to a different PageSpec revision"}
	}
	return nil
}

func hasExactPageSpecSource(sources []core.ArtifactSource, expected core.VersionRef) bool {
	matches := 0
	for _, source := range sources {
		if source.Purpose != "page_spec" {
			continue
		}
		if source.Required && source.AnchorID == nil && source.ArtifactID == expected.ArtifactID &&
			source.RevisionID == expected.RevisionID && source.ContentHash == expected.ContentHash {
			matches++
		}
	}
	return matches == 1
}

func (s *Service) loadEnvelope(ctx context.Context, model importModel) (SnapshotEnvelope, error) {
	stored, err := s.contents.Get(ctx, model.SnapshotRef, model.SnapshotContentHash)
	if err != nil {
		return SnapshotEnvelope{}, err
	}
	if stored.ProjectID != model.ProjectID.String() || stored.AggregateType != "design_import_snapshot" || stored.AggregateID != model.ID.String() {
		return SnapshotEnvelope{}, core.ErrConflict
	}
	var envelope SnapshotEnvelope
	if err := json.Unmarshal(stored.Payload, &envelope); err != nil {
		return SnapshotEnvelope{}, err
	}
	if envelope.RawContentHash != model.RawContentHash || envelope.ByteSize != model.ByteSize || envelope.SourceKind != SourceKind(model.SourceKind) {
		return SnapshotEnvelope{}, core.ErrConflict
	}
	return envelope, nil
}

func (s *Service) importFromModel(ctx context.Context, model importModel, actorID string) (Import, error) {
	item := Import{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(), Status: model.Status,
		PipelineStage: model.PipelineStage,
		Version:       model.Version, ETag: importETag(model.ID, model.Version),
		Snapshot: Snapshot{
			ContentHash: model.SnapshotContentHash, RawContentHash: model.RawContentHash,
			SourceKind: SourceKind(model.SourceKind), SourceName: model.SourceName, Mode: model.SourceMode,
			MediaType: model.MediaType, ByteSize: model.ByteSize, CapturedAt: model.CreatedAt,
			SelectedFrames: decodeStringArray(model.SelectedFrameIDs),
		},
		PageSpecRevision: core.VersionRef{ArtifactID: model.PageSpecArtifactID.String(), RevisionID: model.PageSpecRevisionID.String(), ContentHash: model.PageSpecContentHash},
		CreatesPrototype: model.CreatesPrototype, CreatedBy: model.CreatedBy.String(),
		CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt, DecidedAt: model.DecidedAt,
	}
	if model.SourceURL != nil {
		item.Snapshot.SourceURL = *model.SourceURL
	}
	if model.FileName != nil {
		item.Snapshot.FileName = *model.FileName
	}
	if model.PrototypeArtifactID != nil {
		item.PrototypeArtifactID = model.PrototypeArtifactID.String()
	}
	if model.BaseRevisionID != nil {
		item.BaseRevisionID = model.BaseRevisionID.String()
	}
	if model.InputManifestID != nil {
		item.InputManifestID = model.InputManifestID.String()
		manifest, err := s.proposals.GetManifest(ctx, item.InputManifestID, actorID)
		if err != nil {
			return Import{}, err
		}
		item.Manifest = &manifest
	}
	if model.OutputProposalID != nil {
		item.OutputProposalID = model.OutputProposalID.String()
		proposal, err := s.proposals.GetProposal(ctx, item.OutputProposalID, actorID)
		if err != nil {
			return Import{}, err
		}
		item.Proposal = &proposal
	}
	if model.OperationID != nil {
		item.OperationID = *model.OperationID
	}
	if model.AppliedRevisionID != nil {
		item.AppliedRevisionID = model.AppliedRevisionID.String()
	}
	if model.FailureCode != nil {
		item.FailureCode = *model.FailureCode
	}
	if model.FailureDetail != nil {
		item.FailureDetail = *model.FailureDetail
	}
	if model.DecidedBy != nil {
		item.DecidedBy = model.DecidedBy.String()
	}
	return item, nil
}

func (s *Service) recordCreationFailure(ctx context.Context, model *importModel, claim creationClaim, cause error) {
	code := failureCode(cause)
	detail := sanitizeFailure(cause)
	now := s.now().UTC()
	_ = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Model(&importModel{}).
			Where(
				"id = ? AND version = ? AND status = 'creating' AND pipeline_stage = ? AND create_claim_token = ?",
				model.ID, claim.Version, claim.Stage, claim.Token,
			).
			Updates(map[string]any{
				"status": "failed", "failure_code": code, "failure_detail": detail,
				"create_claim_token": nil, "create_claimed_by": nil,
				"create_claimed_at": nil, "create_claim_expires_at": nil,
				"version": gorm.Expr("version + 1"), "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			// A stale worker is deliberately unable to publish failure after a new
			// worker has recovered the lease or advanced the pipeline.
			return nil
		}
		metadata := map[string]any{"failureCode": code, "status": "failed", "stage": claim.Stage, "recoverable": true}
		if err := insertAudit(transaction, model.ProjectID, claim.ActorID, "design_import.failed", model.ID.String(), metadata); err != nil {
			return err
		}
		return enqueue(transaction, model.ID, "design_import.failed", map[string]any{
			"projectId": model.ProjectID.String(), "designImportId": model.ID.String(),
			"failureCode": code, "stage": claim.Stage, "recoverable": true,
		})
	})
}

func (s *Service) claimApplying(ctx context.Context, model importModel, actorID uuid.UUID) error {
	now := s.now().UTC()
	return s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Model(&importModel{}).
			Where("id = ? AND version = ? AND status IN ?", model.ID, model.Version, []string{"open", "failed", "applying"}).
			Updates(map[string]any{"status": "applying", "version": gorm.Expr("version + 1"), "updated_at": now, "failure_code": nil, "failure_detail": nil})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		if err := insertAudit(transaction, model.ProjectID, actorID, "design_import.approval_started", model.ID.String(), map[string]any{"proposalId": model.OutputProposalID.String()}); err != nil {
			return err
		}
		return enqueue(transaction, model.ID, "design_import.approval_started", map[string]any{"projectId": model.ProjectID.String(), "designImportId": model.ID.String()})
	})
}

func (s *Service) finishDecision(ctx context.Context, model importModel, actorID uuid.UUID, status string, appliedRevisionID *uuid.UUID, now time.Time) error {
	return s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var locked importModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", model.ID).Take(&locked).Error; err != nil {
			return err
		}
		if locked.Status == status {
			return nil
		}
		if status == "applied" && locked.Status != "applying" && locked.Status != "failed" || status == "rejected" && locked.Status != "open" && locked.Status != "failed" {
			return ErrConflict
		}
		updates := map[string]any{
			"status": status, "version": gorm.Expr("version + 1"), "decided_by": actorID,
			"decided_at": now, "updated_at": now, "failure_code": nil, "failure_detail": nil,
		}
		if appliedRevisionID != nil {
			updates["applied_revision_id"] = *appliedRevisionID
		}
		if err := transaction.Model(&importModel{}).Where("id = ?", model.ID).Updates(updates).Error; err != nil {
			return err
		}
		eventType := "design_import." + status
		metadata := map[string]any{"proposalId": locked.OutputProposalID.String(), "status": status}
		if appliedRevisionID != nil {
			metadata["prototypeArtifactId"] = locked.PrototypeArtifactID.String()
			metadata["prototypeRevisionId"] = appliedRevisionID.String()
		}
		if err := insertAudit(transaction, locked.ProjectID, actorID, eventType, model.ID.String(), metadata); err != nil {
			return err
		}
		return enqueue(transaction, model.ID, eventType, map[string]any{"projectId": locked.ProjectID.String(), "designImportId": model.ID.String(), "status": status})
	})
}

func (s *Service) recordFailure(ctx context.Context, id uuid.UUID, cause error, actorIDs ...string) {
	code := failureCode(cause)
	detail := sanitizeFailure(cause)
	_ = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var model importModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&model).Error; err != nil {
			return err
		}
		if model.Status == "applied" || model.Status == "rejected" {
			return nil
		}
		if model.CreateClaimToken != nil {
			// Creation failures are token-scoped by recordCreationFailure. A
			// generic failure must never revoke another worker's active lease.
			return nil
		}
		actorID := model.CreatedBy
		if len(actorIDs) > 0 {
			if parsed, err := uuid.Parse(actorIDs[0]); err == nil {
				actorID = parsed
			}
		}
		now := s.now().UTC()
		if err := transaction.Model(&importModel{}).Where("id = ?", id).Updates(map[string]any{
			"status": "failed", "failure_code": code, "failure_detail": detail,
			"version": gorm.Expr("version + 1"), "updated_at": now,
		}).Error; err != nil {
			return err
		}
		metadata := map[string]any{"failureCode": code, "status": "failed"}
		if err := insertAudit(transaction, model.ProjectID, actorID, "design_import.failed", id.String(), metadata); err != nil {
			return err
		}
		return enqueue(transaction, id, "design_import.failed", map[string]any{
			"projectId": model.ProjectID.String(), "designImportId": id.String(), "failureCode": code,
		})
	})
}

func buildPrototypeContent(model importModel, envelope SnapshotEnvelope, pageRevision *core.ArtifactRevision, placeholder bool) (json.RawMessage, error) {
	if pageRevision == nil {
		return nil, core.ErrConflict
	}
	var pageSpec map[string]any
	if err := json.Unmarshal(pageRevision.Content, &pageSpec); err != nil {
		return nil, err
	}
	pageStates, ok := pageSpec["states"].([]any)
	if !ok || len(pageStates) == 0 {
		return nil, &Error{Kind: ErrConflict, Field: "pageSpecRevision", Detail: "PageSpec has no states to map"}
	}
	states := make([]map[string]any, 0, len(pageStates))
	for _, rawState := range pageStates {
		pageState, _ := rawState.(map[string]any)
		stateID := stringValue(pageState["id"])
		if stateID == "" {
			continue
		}
		required := true
		if declared, exists := pageState["required"].(bool); exists {
			required = declared
		}
		states = append(states, map[string]any{
			"id": stateID, "key": stringValue(pageState["key"]), "title": stringValue(pageState["title"]),
			"required": required, "fixtureIds": stringArrayValue(pageState["fixtureIds"]), "pageStateId": stateID,
		})
	}
	if len(states) == 0 {
		return nil, &Error{Kind: ErrConflict, Field: "pageSpecRevision", Detail: "PageSpec states have no stable identifiers"}
	}
	breakpoints := []map[string]any{
		{"id": "desktop", "name": "Desktop", "minWidth": 1024, "viewportWidth": 1440, "viewportHeight": 900},
		{"id": "tablet", "name": "Tablet", "minWidth": 768, "maxWidth": 1023, "viewportWidth": 768, "viewportHeight": 1024},
		{"id": "mobile", "name": "Mobile", "minWidth": 0, "maxWidth": 767, "viewportWidth": 390, "viewportHeight": 844},
	}
	acceptanceCriterionIDs := stringArrayValue(pageSpec["acceptanceCriterionIds"])
	requirementIDs := stringArrayValue(pageSpec["requirementIds"])
	operationID := "design-import-" + model.ID.String()
	changedAt := envelope.CapturedAt.UTC().Format(time.RFC3339Nano)
	if envelope.CapturedAt.IsZero() {
		changedAt = model.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	fieldMetadata := map[string]any{
		"properties": map[string]any{
			"source": "import", "changedBy": model.CreatedBy.String(), "changedAt": changedAt,
			"operationId": operationID, "aiPolicy": "preserve",
		},
	}
	rootProperties := map[string]any{
		"designImportId": model.ID.String(), "importStage": "proposal_base",
		"sourceTrust": "untrusted_external_snapshot", "externalSourceIsFact": false,
	}
	if !placeholder {
		rootProperties["importStage"] = "proposed"
		rootProperties["snapshotContentHash"] = model.SnapshotContentHash
		rootProperties["rawContentHash"] = model.RawContentHash
		rootProperties["sourceKind"] = model.SourceKind
		rootProperties["sourceName"] = model.SourceName
		rootProperties["sourceMediaType"] = model.MediaType
		rootProperties["sourceFileName"] = pointerValue(model.FileName)
		rootProperties["sourceByteSize"] = model.ByteSize
		rootProperties["catalogTruncated"] = envelope.ExtractedCatalog.Truncated
		rootProperties["catalogTruncationReason"] = envelope.ExtractedCatalog.TruncationReason
	}
	rootLayer := map[string]any{
		"id": "import-root", "childIds": []string{}, "kind": "frame", "name": "Imported design snapshot",
		"semanticRole": "main", "layout": map[string]any{"x": 0, "y": 0, "width": 1440, "height": 900},
		"style": map[string]any{"fill": "#171719"}, "properties": rootProperties,
		"requirementIds": requirementIDs, "acceptanceCriterionIds": acceptanceCriterionIDs,
		"fieldMetadata": fieldMetadata,
	}
	layers := map[string]any{
		"import-root": rootLayer,
	}
	frames := make([]map[string]any, 0, len(states)*len(breakpoints))
	for _, state := range states {
		stateID := stringValue(state["id"])
		if stateID == "" {
			continue
		}
		for _, breakpoint := range breakpoints {
			breakpointID := stringValue(breakpoint["id"])
			frames = append(frames, map[string]any{
				"id": stateID + "-" + breakpointID, "stateId": stateID,
				"breakpointId": breakpointID, "rootLayerId": "import-root",
				"title": stringValue(state["title"]) + " · " + stringValue(breakpoint["name"]),
			})
		}
	}
	selected := decodeStringArray(model.SelectedFrameIDs)
	catalog := envelope.ExtractedCatalog
	if len(selected) > 0 {
		catalog.Pages = filterCatalog(catalog.Pages, selected)
		catalog.Components = filterCatalog(catalog.Components, selected)
	}
	if placeholder {
		catalog = ImportCatalog{Pages: []CatalogItem{}, Components: []CatalogItem{}, States: []CatalogItem{}, Interactions: []CatalogItem{}}
	}
	childIDs := make([]string, 0, len(catalog.Pages)+len(catalog.Components))
	componentBindings := make([]map[string]any, 0, len(catalog.Components))
	allItems := append(append([]CatalogItem{}, catalog.Pages...), catalog.Components...)
	for index, item := range allItems {
		layerID := stableImportedLayerID(item.Kind, item.ID, index)
		childIDs = append(childIDs, layerID)
		isComponent := index >= len(catalog.Pages)
		kind := "group"
		semanticRole := "imported-page"
		if isComponent {
			kind = "componentInstance"
			semanticRole = "imported-component"
			componentBindings = append(componentBindings, map[string]any{
				"id": "component-binding-" + layerID, "layerId": layerID,
				"componentId": item.ID, "componentVersion": model.RawContentHash,
				"propertyMapping": map[string]any{"sourceName": item.Name, "sourceKind": item.Kind},
			})
		}
		layers[layerID] = map[string]any{
			"id": layerID, "parentId": "import-root", "childIds": []string{}, "kind": kind,
			"name": item.Name, "semanticRole": semanticRole,
			"layout": map[string]any{"x": 32 + (index%3)*320, "y": 72 + (index/3)*220, "width": 288, "height": 180},
			"style":  map[string]any{"fill": "#25252b", "stroke": "#53535f"},
			"properties": map[string]any{
				"externalId": item.ID, "externalKind": item.Kind, "sourceKind": model.SourceKind,
				"snapshotContentHash": model.SnapshotContentHash, "itemCount": item.Count,
			},
			"requirementIds": requirementIDs, "acceptanceCriterionIds": acceptanceCriterionIDs,
			"fieldMetadata": fieldMetadata,
		}
	}
	rootLayer["childIds"] = childIDs
	traceLinks := []map[string]any{{
		"id": "trace-" + model.ID.String(),
		"source": map[string]any{
			"kind": "pageSpec", "id": model.PageSpecArtifactID.String(),
			"version": map[string]any{"artifactId": model.PageSpecArtifactID.String(), "revisionId": model.PageSpecRevisionID.String(), "contentHash": model.PageSpecContentHash},
		},
		"target":   map[string]any{"kind": "prototypeLayer", "id": "import-root"},
		"relation": "renders", "createdAt": changedAt,
	}}
	content := map[string]any{
		"pageSpecRevision": map[string]any{
			"artifactId": model.PageSpecArtifactID.String(), "revisionId": model.PageSpecRevisionID.String(), "contentHash": model.PageSpecContentHash,
		},
		"exploratory": false, "states": states, "breakpoints": breakpoints,
		"layers": layers, "frames": frames, "overrides": []any{}, "interactions": []any{},
		"fixtures": []any{}, "tokenBindings": []any{}, "componentBindings": componentBindings,
		"assets": []any{}, "traceLinks": traceLinks,
	}
	payload, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	if err := validateCanonicalPrototypeContent(payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func validateSelection(selected []string, catalog ImportCatalog) error {
	if len(selected) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, group := range [][]CatalogItem{catalog.Pages, catalog.Components, catalog.States, catalog.Interactions} {
		for _, item := range group {
			known[item.ID] = true
		}
	}
	for index, id := range selected {
		if !known[id] {
			return invalid(fmt.Sprintf("selectedFrameIds[%d]", index), "does not identify a frame or component in the uploaded snapshot")
		}
	}
	return nil
}

func filterCatalog(items []CatalogItem, selected []string) []CatalogItem {
	wanted := map[string]bool{}
	for _, id := range selected {
		wanted[id] = true
	}
	result := make([]CatalogItem, 0, len(items))
	for _, item := range items {
		if wanted[item.ID] {
			result = append(result, item)
		}
	}
	return result
}

func stableImportedLayerID(kind, externalID string, index int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", kind, externalID, index)))
	return "import-layer-" + hex.EncodeToString(digest[:8])
}

func stringArrayValue(value any) []string {
	switch values := value.(type) {
	case []string:
		return append([]string{}, values...)
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if item, ok := value.(string); ok && strings.TrimSpace(item) != "" {
				result = append(result, strings.TrimSpace(item))
			}
		}
		return result
	default:
		return []string{}
	}
}

func validateCanonicalPrototypeContent(payload json.RawMessage) error {
	var content map[string]any
	if err := json.Unmarshal(payload, &content); err != nil {
		return fmt.Errorf("%w: generated Prototype is not JSON", core.ErrInvalidInput)
	}
	for _, field := range []string{"states", "breakpoints", "frames", "overrides", "interactions", "fixtures", "tokenBindings", "componentBindings", "assets", "traceLinks"} {
		if _, ok := content[field].([]any); !ok {
			return fmt.Errorf("%w: generated Prototype field %s must be an array", core.ErrInvalidInput, field)
		}
	}
	states := sliceValues(content["states"])
	for _, state := range states {
		if stringValue(state["id"]) == "" || stringValue(state["key"]) == "" || stringValue(state["title"]) == "" || stringValue(state["pageStateId"]) == "" {
			return fmt.Errorf("%w: generated Prototype state is not canonical", core.ErrInvalidInput)
		}
		if _, ok := state["required"].(bool); !ok {
			return fmt.Errorf("%w: generated Prototype state required must be boolean", core.ErrInvalidInput)
		}
		if _, ok := state["fixtureIds"].([]any); !ok {
			return fmt.Errorf("%w: generated Prototype state fixtureIds must be an array", core.ErrInvalidInput)
		}
	}
	for _, breakpoint := range sliceValues(content["breakpoints"]) {
		if stringValue(breakpoint["id"]) == "" || stringValue(breakpoint["name"]) == "" || !jsonNumber(breakpoint["minWidth"]) || !jsonNumber(breakpoint["viewportWidth"]) || !jsonNumber(breakpoint["viewportHeight"]) {
			return fmt.Errorf("%w: generated Prototype breakpoint is not canonical", core.ErrInvalidInput)
		}
	}
	layers, ok := content["layers"].(map[string]any)
	if !ok || len(layers) == 0 {
		return fmt.Errorf("%w: generated Prototype layers must be a non-empty record", core.ErrInvalidInput)
	}
	for id, rawLayer := range layers {
		layer, ok := rawLayer.(map[string]any)
		if !ok || stringValue(layer["id"]) != id || stringValue(layer["kind"]) == "" || stringValue(layer["name"]) == "" {
			return fmt.Errorf("%w: generated Prototype layer is not canonical", core.ErrInvalidInput)
		}
		for _, field := range []string{"layout", "style", "properties", "fieldMetadata"} {
			if _, ok := layer[field].(map[string]any); !ok {
				return fmt.Errorf("%w: generated Prototype layer %s must be an object", core.ErrInvalidInput, field)
			}
		}
		for _, field := range []string{"childIds", "requirementIds", "acceptanceCriterionIds"} {
			if _, ok := layer[field].([]any); !ok {
				return fmt.Errorf("%w: generated Prototype layer %s must be an array", core.ErrInvalidInput, field)
			}
		}
	}
	for _, frame := range sliceValues(content["frames"]) {
		if stringValue(frame["id"]) == "" || stringValue(frame["stateId"]) == "" || stringValue(frame["breakpointId"]) == "" || stringValue(frame["rootLayerId"]) == "" || stringValue(frame["title"]) == "" {
			return fmt.Errorf("%w: generated Prototype frame is not canonical", core.ErrInvalidInput)
		}
	}
	return nil
}

func jsonNumber(value any) bool {
	switch value.(type) {
	case float64, float32, int, int32, int64, uint, uint32, uint64, json.Number:
		return true
	default:
		return false
	}
}

func sameCreateRequest(model importModel, input CreateInput, upload validatedUpload) bool {
	expectedName := strings.TrimSpace(input.Title)
	if expectedName == "" {
		expectedName = strings.TrimSuffix(upload.FileName, filepathExtension(upload.FileName))
	}
	targetPrototypeID := model.ExpectedPrototypeArtifactID
	if targetPrototypeID == uuid.Nil && model.PrototypeArtifactID != nil {
		targetPrototypeID = *model.PrototypeArtifactID
	}
	return model.SourceKind == string(input.SourceKind) && model.SourceMode == input.Mode &&
		model.SourceName == expectedName && model.FileName != nil && *model.FileName == upload.FileName &&
		model.MediaType == upload.MediaType && model.RawContentHash == upload.RawContentHash &&
		model.PageSpecArtifactID.String() == input.PageSpecRevision.ArtifactID &&
		model.PageSpecRevisionID.String() == input.PageSpecRevision.RevisionID &&
		model.PageSpecContentHash == input.PageSpecRevision.ContentHash &&
		decodeStringArray(model.SelectedFrameIDs) != nil && equalStrings(decodeStringArray(model.SelectedFrameIDs), input.SelectedFrameIDs) &&
		((model.CreatesPrototype && input.TargetPrototypeArtifactID == "") ||
			(!model.CreatesPrototype && targetPrototypeID.String() == input.TargetPrototypeArtifactID))
}

func findOperation(proposal domain.OutputProposal, operationID string) *domain.ProposalOperation {
	for index := range proposal.Operations {
		if proposal.Operations[index].ID == operationID {
			return &proposal.Operations[index]
		}
	}
	return nil
}

func importArtifactKey(id uuid.UUID) string {
	return "DESIGN-IMPORT-" + strings.ToUpper(strings.ReplaceAll(id.String(), "-", ""))
}

func designImportID(projectID uuid.UUID, requestKeyHash string) uuid.UUID {
	return uuid.NewSHA1(projectID, []byte("worksflow:design-import:"+requestKeyHash))
}

func designImportResourceID(importID uuid.UUID, resource string) uuid.UUID {
	return uuid.NewSHA1(importID, []byte("worksflow:design-import:"+resource))
}

func uploadLimitForSnapshotStore(contentLimit int64) int64 {
	available := contentLimit - snapshotEnvelopeReserve
	if available <= 0 {
		return 0
	}
	limit := available * 3 / 4
	if limit > MaxUploadBytes {
		return MaxUploadBytes
	}
	return limit
}

func importETag(id uuid.UUID, version uint64) string {
	return fmt.Sprintf(`"design-import:%s:%d"`, id, version)
}

func validImportStatus(status string) bool {
	switch status {
	case "creating", "open", "applying", "applied", "rejected", "failed":
		return true
	default:
		return false
	}
}

func parseProjectActor(projectID, actorID string) (uuid.UUID, uuid.UUID, error) {
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return uuid.Nil, uuid.Nil, invalid("projectId", "must be a UUID")
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return uuid.Nil, uuid.Nil, core.ErrInvalidInput
	}
	return projectUUID, actorUUID, nil
}

func insertAudit(transaction *gorm.DB, projectID, actorID uuid.UUID, action, targetID string, metadata map[string]any) error {
	payload, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	var requestID *string
	if value := core.RequestIDFromContext(transaction.Statement.Context); value != "" {
		requestID = &value
	}
	return transaction.Create(&storage.AuditEventModel{
		ID: uuid.New(), ProjectID: &projectID, ActorID: &actorID, RequestID: requestID,
		Action: action, TargetType: "design_import", TargetID: targetID, Metadata: payload, CreatedAt: time.Now().UTC(),
	}).Error
}

func enqueue(transaction *gorm.DB, id uuid.UUID, eventType string, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return transaction.Create(&storage.OutboxEventModel{
		ID: uuid.New(), AggregateType: "design_import", AggregateID: id.String(), EventType: eventType,
		Subject: "worksflow." + strings.ReplaceAll(eventType, "_", "."), Payload: encoded,
		Headers: json.RawMessage(`{}`), AvailableAt: now, CreatedAt: now,
	}).Error
}

func decodeStringArray(value json.RawMessage) []string {
	result := []string{}
	_ = json.Unmarshal(value, &result)
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func filepathExtension(value string) string {
	index := strings.LastIndex(value, ".")
	if index < 0 {
		return ""
	}
	return value[index:]
}

func failureCode(err error) string {
	switch {
	case errors.Is(err, ErrCapabilityUnavailable):
		return "capability_unavailable"
	case errors.Is(err, ErrUnsupportedMediaType):
		return "unsupported_media_type"
	case errors.Is(err, ErrUploadTooLarge), errors.Is(err, content.ErrContentTooLarge):
		return "payload_too_large"
	case errors.Is(err, ErrConflict), errors.Is(err, core.ErrConflict), errors.Is(err, core.ErrProposalStale):
		return "conflict"
	case errors.Is(err, core.ErrBlockingGate):
		return "blocking_gate"
	case errors.Is(err, core.ErrContentNotReady):
		return "content_not_ready"
	default:
		return "import_failed"
	}
}

func sanitizeFailure(err error) string {
	if err == nil {
		return ""
	}
	value := strings.ReplaceAll(err.Error(), "\x00", "")
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}

func isUniqueViolation(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate key") || strings.Contains(message, "unique constraint")
}
