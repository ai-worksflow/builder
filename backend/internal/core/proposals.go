package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ManifestSourceInput struct {
	Ref     VersionRef `json:"ref"`
	Purpose string     `json:"purpose"`
}

type CreateManifestInput struct {
	JobType               string                   `json:"jobType"`
	DeliverySliceID       string                   `json:"deliverySliceId,omitempty"`
	BaseRevision          *VersionRef              `json:"baseRevision,omitempty"`
	Sources               []ManifestSourceInput    `json:"sources"`
	Constraints           json.RawMessage          `json:"constraints"`
	OutputSchemaVersion   string                   `json:"outputSchemaVersion"`
	BlueprintSelection    *BlueprintSelectionInput `json:"blueprintSelection,omitempty"`
	ExpectedBlueprintETag string                   `json:"-"`
}

const DocumentSyncBackJobType = "document.sync_back"

type CreateDocumentSyncBackManifestInput struct {
	BaseRevision      VersionRef
	WorkspaceRevision VersionRef
	Constraints       json.RawMessage
}

type CreateProposalInput struct {
	ManifestID  string                     `json:"inputManifestId"`
	ArtifactID  string                     `json:"artifactId"`
	Operations  []domain.ProposalOperation `json:"operations"`
	Assumptions []string                   `json:"assumptions,omitempty"`
	Questions   []string                   `json:"questions,omitempty"`
	AIProvider  string                     `json:"-"`
	AIModel     string                     `json:"-"`
}

type DecideProposalInput struct {
	OperationID string                  `json:"operationId"`
	Decision    domain.ProposalDecision `json:"decision"`
	Reason      string                  `json:"reason,omitempty"`
	Version     uint64                  `json:"version"`
}

type ApplyProposalInput struct {
	Version uint64 `json:"version"`
}

type ProposalService struct {
	database *gorm.DB
	contents content.Store
	access   *AccessControl
	trace    *TraceService
	now      func() time.Time
}

func NewProposalService(database *gorm.DB, contents content.Store, access *AccessControl) (*ProposalService, error) {
	if database == nil || contents == nil || access == nil {
		return nil, errors.New("proposal database, content store and access control are required")
	}
	trace, _ := NewTraceService(database, access, contents)
	return &ProposalService{database: database, contents: contents, access: access, trace: trace, now: time.Now}, nil
}

func (s *ProposalService) ValidateArtifactProposalTarget(
	ctx context.Context,
	projectID string,
	artifactID string,
	actorID string,
) error {
	artifactUUID, artifactProjectID, err := (&ArtifactService{database: s.database, access: s.access}).
		authorizeArtifact(ctx, artifactID, actorID, ActionEdit)
	if err != nil {
		return err
	}
	if artifactProjectID.String() != projectID {
		return ErrConflict
	}
	var artifact storage.ArtifactModel
	if err := s.database.WithContext(ctx).Select("id", "kind").Where("id = ?", artifactUUID).Take(&artifact).Error; err != nil {
		return err
	}
	return ensureGenericArtifactMutationAllowed(artifact.Kind)
}

func (s *ProposalService) ValidateArtifactProposalBase(
	ctx context.Context,
	projectID string,
	actorID string,
	base VersionRef,
) error {
	if err := s.ValidateArtifactProposalTarget(ctx, projectID, base.ArtifactID, actorID); err != nil {
		return err
	}
	artifactID, err := uuid.Parse(base.ArtifactID)
	if err != nil {
		return fmt.Errorf("%w: base artifact id", ErrInvalidInput)
	}
	revisionID, err := uuid.Parse(base.RevisionID)
	if err != nil {
		return fmt.Errorf("%w: base revision id", ErrInvalidInput)
	}
	var artifact storage.ArtifactModel
	if err := s.database.WithContext(ctx).Where("id = ?", artifactID).Take(&artifact).Error; err != nil {
		return err
	}
	if artifact.LatestRevisionID == nil || *artifact.LatestRevisionID != revisionID {
		return ErrProposalStale
	}
	var revision storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Where(
		"id = ? AND artifact_id = ? AND content_hash = ?", revisionID, artifactID, base.ContentHash,
	).Take(&revision).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrProposalStale
		}
		return err
	}
	if artifact.LatestDraftID == nil {
		return nil
	}
	var draft storage.ArtifactDraftModel
	if err := s.database.WithContext(ctx).Where("id = ? AND artifact_id = ?", *artifact.LatestDraftID, artifactID).
		Take(&draft).Error; err != nil {
		return err
	}
	return ensureExactCleanProposalDraft(s.database.WithContext(ctx), draft, revision)
}

func ensureExactCleanProposalDraft(
	database *gorm.DB,
	draft storage.ArtifactDraftModel,
	base storage.ArtifactRevisionModel,
) error {
	if draft.ArtifactID != base.ArtifactID || draft.BaseRevisionID == nil || *draft.BaseRevisionID != base.ID ||
		draft.Status != "draft" || draft.SchemaVersion != base.SchemaVersion || draft.ContentHash != base.ContentHash {
		return ErrProposalStale
	}
	var draftSources []storage.ArtifactDraftSourceModel
	if err := database.Where("draft_id = ?", draft.ID).Find(&draftSources).Error; err != nil {
		return err
	}
	var revisionSources []storage.ArtifactRevisionSourceModel
	if err := database.Where("revision_id = ?", base.ID).Find(&revisionSources).Error; err != nil {
		return err
	}
	if len(draftSources) != len(revisionSources) {
		return ErrProposalStale
	}
	sort.Slice(draftSources, func(i, j int) bool {
		return proposalDraftSourceKey(draftSources[i]) < proposalDraftSourceKey(draftSources[j])
	})
	sort.Slice(revisionSources, func(i, j int) bool {
		return proposalRevisionSourceKey(revisionSources[i]) < proposalRevisionSourceKey(revisionSources[j])
	})
	for index := range draftSources {
		draftSource, revisionSource := draftSources[index], revisionSources[index]
		if draftSource.SourceArtifactID != revisionSource.SourceArtifactID ||
			draftSource.SourceRevisionID != revisionSource.SourceRevisionID ||
			draftSource.SourceContentHash != revisionSource.SourceContentHash ||
			!stringPointerEqual(draftSource.SourceAnchorID, revisionSource.SourceAnchorID) ||
			draftSource.Purpose != revisionSource.Purpose || draftSource.Required != revisionSource.Required {
			return ErrProposalStale
		}
	}
	return nil
}

func proposalDraftSourceKey(source storage.ArtifactDraftSourceModel) string {
	return source.SourceArtifactID.String() + "\x00" + source.SourceRevisionID.String() + "\x00" + source.Purpose
}

func proposalRevisionSourceKey(source storage.ArtifactRevisionSourceModel) string {
	return source.SourceArtifactID.String() + "\x00" + source.SourceRevisionID.String() + "\x00" + source.Purpose
}

func (s *ProposalService) CreateManifest(ctx context.Context, projectID, actorID string, input CreateManifestInput) (domain.InputManifest, error) {
	return s.createManifest(ctx, projectID, actorID, uuid.New(), input)
}

// CreateManifestWithID retains the normal manifest authorization and
// validation path while allowing a durable internal command to pre-allocate a
// stable identity for crash recovery.
func (s *ProposalService) CreateManifestWithID(
	ctx context.Context,
	projectID, actorID, manifestID string,
	input CreateManifestInput,
) (domain.InputManifest, error) {
	stableID, err := uuid.Parse(strings.TrimSpace(manifestID))
	if err != nil || stableID == uuid.Nil {
		return domain.InputManifest{}, fmt.Errorf("%w: manifest id", ErrInvalidInput)
	}
	return s.createManifest(ctx, projectID, actorID, stableID, input)
}

// CreateDocumentSyncBackManifest is the narrow trusted path by which an
// approved system-managed Workspace revision may be frozen as proposal input.
// It does not relax the generic proposal target guard: the base remains an
// editable document revision, while the Workspace is read-only evidence.
func (s *ProposalService) CreateDocumentSyncBackManifest(
	ctx context.Context,
	projectID, actorID string,
	input CreateDocumentSyncBackManifestInput,
) (domain.InputManifest, error) {
	projectUUID, err := uuid.Parse(strings.TrimSpace(projectID))
	if err != nil {
		return domain.InputManifest{}, fmt.Errorf("%w: project id", ErrInvalidInput)
	}
	workspaceArtifactID, err := uuid.Parse(strings.TrimSpace(input.WorkspaceRevision.ArtifactID))
	if err != nil {
		return domain.InputManifest{}, fmt.Errorf("%w: workspace artifact id", ErrInvalidInput)
	}
	workspaceRevisionID, err := uuid.Parse(strings.TrimSpace(input.WorkspaceRevision.RevisionID))
	if err != nil || !strings.HasPrefix(input.WorkspaceRevision.ContentHash, "sha256:") || input.WorkspaceRevision.AnchorID != nil {
		return domain.InputManifest{}, fmt.Errorf("%w: workspace revision", ErrInvalidInput)
	}
	var count int64
	if err := s.database.WithContext(ctx).Table("artifact_revisions AS revision").
		Joins("JOIN artifacts AS artifact ON artifact.id = revision.artifact_id").
		Where(
			"artifact.id = ? AND artifact.project_id = ? AND artifact.kind = 'workspace' AND artifact.lifecycle = 'active' AND revision.id = ? AND revision.content_hash = ? AND revision.workflow_status = 'approved'",
			workspaceArtifactID, projectUUID, workspaceRevisionID, input.WorkspaceRevision.ContentHash,
		).Count(&count).Error; err != nil {
		return domain.InputManifest{}, err
	}
	if count != 1 {
		return domain.InputManifest{}, ErrNotFound
	}
	return s.CreateManifest(ctx, projectID, actorID, CreateManifestInput{
		JobType:      DocumentSyncBackJobType,
		BaseRevision: &input.BaseRevision,
		Sources: []ManifestSourceInput{{
			Ref: input.WorkspaceRevision, Purpose: "implementation_workspace",
		}},
		Constraints: input.Constraints, OutputSchemaVersion: "document.patch.v1",
	})
}

func (s *ProposalService) createManifest(
	ctx context.Context,
	projectID, actorID string,
	manifestID uuid.UUID,
	input CreateManifestInput,
) (domain.InputManifest, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionEdit); err != nil {
		return domain.InputManifest{}, err
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return domain.InputManifest{}, err
	}
	input.JobType = strings.TrimSpace(input.JobType)
	input.OutputSchemaVersion = strings.TrimSpace(input.OutputSchemaVersion)
	if input.BlueprintSelection != nil || input.JobType == BlueprintSelectionJobType {
		if input.BlueprintSelection == nil {
			return domain.InputManifest{}, fmt.Errorf("%w: blueprint selection", ErrInvalidInput)
		}
		input, err = s.compileBlueprintSelection(ctx, projectUUID, input)
		if err != nil {
			return domain.InputManifest{}, err
		}
	}
	if input.JobType == "" || input.OutputSchemaVersion == "" || len(input.OutputSchemaVersion) > 64 {
		return domain.InputManifest{}, fmt.Errorf("%w: manifest job type or output schema", ErrInvalidInput)
	}
	if err := s.validateParentBlueprintSelection(ctx, projectUUID, actorID, input); err != nil {
		return domain.InputManifest{}, err
	}
	manifestSources := make([]domain.ManifestSource, 0, len(input.Sources))
	for _, source := range input.Sources {
		artifactID, revisionID, err := s.trace.validateRef(ctx, projectUUID, source.Ref)
		if err != nil {
			return domain.InputManifest{}, err
		}
		var revision storage.ArtifactRevisionModel
		if err := s.database.WithContext(ctx).Where("id = ? AND artifact_id = ?", revisionID, artifactID).Take(&revision).Error; err != nil {
			return domain.InputManifest{}, err
		}
		// Formal upstream sources are approved. A base revision may be a draft
		// snapshot of the artifact currently being edited and is validated below.
		if revision.WorkflowStatus != "approved" &&
			!manifestSourceIsExactBase(input.BaseRevision, source.Ref) {
			return domain.InputManifest{}, fmt.Errorf("%w: source revision %s is not approved", ErrBlockingGate, source.Ref.RevisionID)
		}
		manifestSources = append(manifestSources, domain.ManifestSource{
			Ref: domain.ArtifactRef{
				ArtifactID: source.Ref.ArtifactID, RevisionID: source.Ref.RevisionID,
				ContentHash: source.Ref.ContentHash, AnchorID: dereferenceString(source.Ref.AnchorID),
			},
			Purpose: strings.TrimSpace(source.Purpose),
		})
	}
	var baseRevision *domain.ArtifactRef
	if input.BaseRevision != nil {
		if _, _, err := s.trace.validateRef(ctx, projectUUID, *input.BaseRevision); err != nil {
			return domain.InputManifest{}, err
		}
		if err := s.ValidateArtifactProposalBase(ctx, projectID, actorID, *input.BaseRevision); err != nil {
			return domain.InputManifest{}, err
		}
		baseRevision = &domain.ArtifactRef{
			ArtifactID: input.BaseRevision.ArtifactID, RevisionID: input.BaseRevision.RevisionID,
			ContentHash: input.BaseRevision.ContentHash, AnchorID: dereferenceString(input.BaseRevision.AnchorID),
		}
	}
	manifest, err := domain.NewInputManifest(
		manifestID.String(), projectID, input.JobType, input.DeliverySliceID,
		baseRevision, manifestSources, input.Constraints, input.OutputSchemaVersion,
		actorID, s.now().UTC(),
	)
	if err != nil {
		return domain.InputManifest{}, err
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return domain.InputManifest{}, err
	}
	contentRef, err := s.contents.PutPending(ctx, projectID, "input_manifest", manifestID.String(), 1, payload)
	if err != nil {
		return domain.InputManifest{}, err
	}
	abortPending := true
	defer func() {
		if abortPending {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	model := storage.InputManifestModel{
		ID: manifestID, ProjectID: projectUUID, Kind: input.JobType, SchemaVersion: 1,
		ContentStore: "mongo", ContentRef: contentRef.ID, ContentHash: contentRef.ContentHash,
		ManifestHash: manifest.Hash, CreatedBy: actorUUID, CreatedAt: manifest.CreatedAt,
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if input.JobType == BlueprintSelectionJobType && input.BlueprintSelection != nil {
			blueprintID, parseErr := uuid.Parse(input.BlueprintSelection.BlueprintRevision.ArtifactID)
			if parseErr != nil {
				return fmt.Errorf("%w: Blueprint artifact id", ErrInvalidInput)
			}
			var lockedBlueprint storage.ArtifactModel
			if queryErr := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND project_id = ? AND kind = 'blueprint' AND lifecycle = 'active'", blueprintID, projectUUID).
				Take(&lockedBlueprint).Error; queryErr != nil {
				return queryErr
			}
			if artifactETag(lockedBlueprint.ID, lockedBlueprint.Version) != strings.TrimSpace(input.ExpectedBlueprintETag) {
				return ErrConflict
			}
		}
		if err := transaction.Create(&model).Error; err != nil {
			return err
		}
		action := "manifest.created"
		topic := "worksflow.manifest.created"
		metadata := map[string]any{"jobType": input.JobType}
		if input.JobType == BlueprintSelectionJobType {
			action = "blueprint.selection.compiled"
			topic = "worksflow.blueprint.selection.compiled"
			var constraints struct {
				BlueprintSelection struct {
					SelectionID string   `json:"selectionId"`
					NodeIDs     []string `json:"nodeIds"`
				} `json:"blueprintSelection"`
			}
			if json.Unmarshal(input.Constraints, &constraints) == nil {
				metadata["selectionId"] = constraints.BlueprintSelection.SelectionID
				metadata["nodeCount"] = len(constraints.BlueprintSelection.NodeIDs)
			}
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, action, "input_manifest", manifestID.String(), metadata); err != nil {
			return err
		}
		return enqueue(transaction, "input_manifest", manifestID.String(), action, topic, map[string]any{
			"projectId": projectID, "manifestId": manifestID.String(), "jobType": input.JobType,
		})
	})
	if err != nil {
		return domain.InputManifest{}, err
	}
	abortPending = false
	if err := s.contents.Finalize(ctx, contentRef.ID); err != nil {
		return domain.InputManifest{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return manifest, nil
}

func manifestSourceIsExactBase(base *VersionRef, source VersionRef) bool {
	return base != nil &&
		base.ArtifactID == source.ArtifactID &&
		base.RevisionID == source.RevisionID &&
		base.ContentHash == source.ContentHash &&
		dereferenceString(base.AnchorID) == dereferenceString(source.AnchorID)
}

func (s *ProposalService) CreateProposal(ctx context.Context, projectID, actorID string, input CreateProposalInput) (domain.OutputProposal, error) {
	return s.createProposal(ctx, projectID, actorID, uuid.New(), input)
}

// CreateProposalWithID retains the normal proposal authorization, immutable
// manifest binding, exact-base validation, audit, and outbox path while letting
// a durable internal command pre-allocate the proposal identity. Callers must
// still recover an already-created proposal through GetProposal and verify its
// full contract; duplicate IDs are not silently treated as success here.
func (s *ProposalService) CreateProposalWithID(
	ctx context.Context,
	projectID, actorID, proposalID string,
	input CreateProposalInput,
) (domain.OutputProposal, error) {
	stableID, err := uuid.Parse(strings.TrimSpace(proposalID))
	if err != nil || stableID == uuid.Nil {
		return domain.OutputProposal{}, fmt.Errorf("%w: proposal id", ErrInvalidInput)
	}
	return s.createProposal(ctx, projectID, actorID, stableID, input)
}

func (s *ProposalService) createProposal(
	ctx context.Context,
	projectID, actorID string,
	proposalID uuid.UUID,
	input CreateProposalInput,
) (domain.OutputProposal, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionEdit); err != nil {
		return domain.OutputProposal{}, err
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return domain.OutputProposal{}, err
	}
	manifest, manifestModel, err := s.loadManifest(ctx, input.ManifestID)
	if err != nil {
		return domain.OutputProposal{}, err
	}
	if manifest.ProjectID != projectID || manifest.BaseRevision == nil || manifest.BaseRevision.ArtifactID != input.ArtifactID {
		return domain.OutputProposal{}, ErrConflict
	}
	base := VersionRef{
		ArtifactID: manifest.BaseRevision.ArtifactID, RevisionID: manifest.BaseRevision.RevisionID,
		ContentHash: manifest.BaseRevision.ContentHash,
	}
	if manifest.BaseRevision.AnchorID != "" {
		anchor := manifest.BaseRevision.AnchorID
		base.AnchorID = &anchor
	}
	if err := s.ValidateArtifactProposalBase(ctx, projectID, actorID, base); err != nil {
		return domain.OutputProposal{}, err
	}
	proposal, err := domain.NewOutputProposal(
		proposalID.String(), projectID, input.ArtifactID, manifest.Ref(), *manifest.BaseRevision,
		input.Operations, input.Assumptions, input.Questions, actorID, s.now().UTC(),
	)
	if err != nil {
		return domain.OutputProposal{}, err
	}
	payload, err := json.Marshal(proposal)
	if err != nil {
		return domain.OutputProposal{}, err
	}
	contentRef, err := s.contents.PutPending(ctx, projectID, "output_proposal", proposalID.String(), 1, payload)
	if err != nil {
		return domain.OutputProposal{}, err
	}
	abortPending := true
	defer func() {
		if abortPending {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	artifactUUID := uuid.MustParse(input.ArtifactID)
	baseRevisionUUID := uuid.MustParse(manifest.BaseRevision.RevisionID)
	baseHash := manifest.BaseRevision.ContentHash
	model := storage.OutputProposalModel{
		ID: proposalID, ProjectID: projectUUID, ArtifactID: &artifactUUID, Kind: manifest.JobType,
		InputManifestID: manifestModel.ID, BaseRevisionID: &baseRevisionUUID,
		BaseContentHash: &baseHash, Status: string(proposal.Status), Version: proposal.Version,
		ContentStore: "mongo", ContentRef: contentRef.ID, ContentHash: contentRef.ContentHash,
		PayloadHash: proposal.PayloadHash, OperationCount: len(proposal.Operations),
		AIProvider: trimmedStringPointer(input.AIProvider), AIModel: trimmedStringPointer(input.AIModel),
		CreatedBy: actorUUID, CreatedAt: proposal.CreatedAt,
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&model).Error; err != nil {
			return err
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "proposal.created", "output_proposal", proposalID.String(), map[string]any{"manifestId": input.ManifestID}); err != nil {
			return err
		}
		return enqueue(transaction, "output_proposal", proposalID.String(), "proposal.created", "worksflow.proposal.created", map[string]any{
			"projectId": projectID, "artifactId": input.ArtifactID, "proposalId": proposalID.String(),
		})
	})
	if err != nil {
		return domain.OutputProposal{}, err
	}
	abortPending = false
	if err := s.contents.Finalize(ctx, contentRef.ID); err != nil {
		return domain.OutputProposal{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return *proposal, nil
}

func (s *ProposalService) GetManifest(ctx context.Context, manifestID, actorID string) (domain.InputManifest, error) {
	manifest, model, err := s.loadManifest(ctx, manifestID)
	if err != nil {
		return domain.InputManifest{}, err
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, ActionView); err != nil {
		return domain.InputManifest{}, err
	}
	return manifest, nil
}

func (s *ProposalService) GetProposal(ctx context.Context, proposalID, actorID string) (domain.OutputProposal, error) {
	proposal, model, err := s.loadProposal(ctx, proposalID)
	if err != nil {
		return domain.OutputProposal{}, err
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, ActionView); err != nil {
		return domain.OutputProposal{}, err
	}
	return proposal, nil
}

func (s *ProposalService) ListProposals(ctx context.Context, projectID, actorID, status string) ([]domain.OutputProposal, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionView); err != nil {
		return nil, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("%w: project id", ErrInvalidInput)
	}
	query := s.database.WithContext(ctx).Where("project_id = ?", projectUUID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var models []storage.OutputProposalModel
	if err := query.Order("created_at DESC").Find(&models).Error; err != nil {
		return nil, err
	}
	result := make([]domain.OutputProposal, 0, len(models))
	for _, model := range models {
		proposal, err := s.proposalFromModel(ctx, model)
		if err != nil {
			return nil, err
		}
		result = append(result, proposal)
	}
	return result, nil
}

func (s *ProposalService) ensureIndependentDesignImportMutation(
	ctx context.Context,
	proposal storage.OutputProposalModel,
	actorID uuid.UUID,
) error {
	if proposal.Kind != "design_import_to_prototype" {
		return nil
	}
	var owner struct {
		CreatedBy uuid.UUID
	}
	err := s.database.WithContext(ctx).Table("design_imports").
		Select("created_by").
		Where("project_id = ? AND (output_proposal_id = ? OR expected_output_proposal_id = ?)", proposal.ProjectID, proposal.ID, proposal.ID).
		Take(&owner).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if owner.CreatedBy == actorID {
		return ErrForbidden
	}
	return nil
}

func (s *ProposalService) Decide(ctx context.Context, proposalID, actorID string, input DecideProposalInput) (domain.OutputProposal, error) {
	proposal, model, err := s.loadProposal(ctx, proposalID)
	if err != nil {
		return domain.OutputProposal{}, err
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, ActionEdit); err != nil {
		return domain.OutputProposal{}, err
	}
	if model.ArtifactID == nil {
		return domain.OutputProposal{}, ErrConflict
	}
	var artifact storage.ArtifactModel
	if err := s.database.WithContext(ctx).Select("id", "kind").Where("id = ?", *model.ArtifactID).Take(&artifact).Error; err != nil {
		return domain.OutputProposal{}, err
	}
	if err := ensureGenericArtifactMutationAllowed(artifact.Kind); err != nil {
		return domain.OutputProposal{}, err
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return domain.OutputProposal{}, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	if err := s.ensureIndependentDesignImportMutation(ctx, model, actorUUID); err != nil {
		return domain.OutputProposal{}, err
	}
	if input.Version == 0 {
		input.Version = proposal.Version
	}
	if err := proposal.Decide(input.OperationID, input.Decision, actorID, input.Reason, input.Version); err != nil {
		return domain.OutputProposal{}, err
	}
	payload, err := json.Marshal(proposal)
	if err != nil {
		return domain.OutputProposal{}, err
	}
	contentRef, err := s.contents.PutPending(ctx, model.ProjectID.String(), "output_proposal", proposalID, 1, payload)
	if err != nil {
		return domain.OutputProposal{}, err
	}
	abortPending := true
	defer func() {
		if abortPending {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	accepted, rejected := proposalDecisionCounts(proposal.Operations)
	now := s.now().UTC()
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Model(&storage.OutputProposalModel{}).
			Where("id = ? AND version = ?", model.ID, input.Version).
			Updates(map[string]any{
				"status": proposal.Status, "version": proposal.Version, "content_ref": contentRef.ID,
				"content_hash": contentRef.ContentHash, "accepted_count": accepted, "rejected_count": rejected,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		decision := storage.ProposalOperationDecisionModel{
			ProposalID: model.ID, OperationID: input.OperationID, Decision: string(input.Decision),
			Reason: strings.TrimSpace(input.Reason), DecidedBy: actorUUID, DecidedAt: now,
		}
		if err := transaction.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "proposal_id"}, {Name: "operation_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"decision", "reason", "decided_by", "decided_at"}),
		}).Create(&decision).Error; err != nil {
			return err
		}
		if err := insertAudit(transaction, model.ProjectID, actorUUID, "proposal.operation_decided", "output_proposal", proposalID, map[string]any{"operationId": input.OperationID, "decision": input.Decision}); err != nil {
			return err
		}
		return enqueue(transaction, "output_proposal", proposalID, "proposal.operation_decided", "worksflow.proposal.operation.decided", map[string]any{
			"projectId": model.ProjectID.String(), "proposalId": proposalID, "operationId": input.OperationID,
		})
	})
	if err != nil {
		return domain.OutputProposal{}, err
	}
	abortPending = false
	if err := s.contents.Finalize(ctx, contentRef.ID); err != nil {
		return domain.OutputProposal{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return proposal, nil
}

func (s *ProposalService) Apply(ctx context.Context, proposalID, actorID string, input ApplyProposalInput) (ArtifactDraft, error) {
	proposal, proposalModel, err := s.loadProposal(ctx, proposalID)
	if err != nil {
		return ArtifactDraft{}, err
	}
	if _, err := s.access.Authorize(ctx, proposalModel.ProjectID.String(), actorID, ActionEdit); err != nil {
		return ArtifactDraft{}, err
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return ArtifactDraft{}, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	if err := s.ensureIndependentDesignImportMutation(ctx, proposalModel, actorUUID); err != nil {
		return ArtifactDraft{}, err
	}
	manifest, _, err := s.loadManifest(ctx, proposal.Manifest.ID)
	if err != nil || manifest.Ref() != proposal.Manifest {
		if err != nil {
			return ArtifactDraft{}, err
		}
		return ArtifactDraft{}, ErrConflict
	}
	if input.Version == 0 {
		input.Version = proposal.Version
	}
	if proposal.Version != input.Version {
		return ArtifactDraft{}, ErrConflict
	}
	accepted, err := proposal.AcceptedOperations()
	if err != nil {
		return ArtifactDraft{}, err
	}
	if proposalModel.BaseRevisionID == nil || proposalModel.ArtifactID == nil {
		return ArtifactDraft{}, ErrConflict
	}
	var base storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Where("id = ?", *proposalModel.BaseRevisionID).Take(&base).Error; err != nil {
		return ArtifactDraft{}, err
	}
	storedBase, err := s.contents.Get(ctx, base.ContentRef, base.ContentHash)
	if err != nil {
		return ArtifactDraft{}, err
	}
	patched, err := domain.ApplyProposalPatch(storedBase.Payload, accepted)
	if err != nil {
		return ArtifactDraft{}, err
	}
	draftID := uuid.New()
	var existingDraft *storage.ArtifactDraftModel
	var artifact storage.ArtifactModel
	if err := s.database.WithContext(ctx).Where("id = ?", *proposalModel.ArtifactID).Take(&artifact).Error; err != nil {
		return ArtifactDraft{}, err
	}
	if err := ensureGenericArtifactMutationAllowed(artifact.Kind); err != nil {
		return ArtifactDraft{}, err
	}
	if err := validateProposalPatchedContent(artifact.Kind, patched); err != nil {
		return ArtifactDraft{}, err
	}
	if artifact.LatestRevisionID == nil || *artifact.LatestRevisionID != base.ID {
		return ArtifactDraft{}, ErrProposalStale
	}
	if artifact.LatestDraftID != nil {
		var draft storage.ArtifactDraftModel
		if err := s.database.WithContext(ctx).Where("id = ?", *artifact.LatestDraftID).Take(&draft).Error; err != nil {
			return ArtifactDraft{}, err
		}
		if err := ensureExactCleanProposalDraft(s.database.WithContext(ctx), draft, base); err != nil {
			return ArtifactDraft{}, err
		}
		existingDraft = &draft
		draftID = draft.ID
	}
	draftContentRef, err := s.contents.PutPending(ctx, proposalModel.ProjectID.String(), "artifact_draft", draftID.String(), base.SchemaVersion, patched)
	if err != nil {
		return ArtifactDraft{}, err
	}
	if err := proposal.MarkApplied(input.Version, s.now().UTC()); err != nil {
		_ = s.contents.Abort(context.Background(), draftContentRef.ID)
		return ArtifactDraft{}, err
	}
	proposalPayload, err := json.Marshal(proposal)
	if err != nil {
		_ = s.contents.Abort(context.Background(), draftContentRef.ID)
		return ArtifactDraft{}, err
	}
	proposalContentRef, err := s.contents.PutPending(ctx, proposalModel.ProjectID.String(), "output_proposal", proposalID, 1, proposalPayload)
	if err != nil {
		_ = s.contents.Abort(context.Background(), draftContentRef.ID)
		return ArtifactDraft{}, err
	}
	pending := []string{draftContentRef.ID, proposalContentRef.ID}
	defer func() {
		for _, contentID := range pending {
			_ = s.contents.Abort(context.Background(), contentID)
		}
	}()
	now := s.now().UTC()
	var draftModel storage.ArtifactDraftModel
	sourceInputs := make([]ArtifactSourceInput, 0, len(manifest.Sources))
	for _, source := range manifest.Sources {
		anchor := source.Ref.AnchorID
		var anchorPointer *string
		if anchor != "" {
			anchorPointer = &anchor
		}
		sourceInputs = append(sourceInputs, ArtifactSourceInput{
			Ref:     VersionRef{ArtifactID: source.Ref.ArtifactID, RevisionID: source.Ref.RevisionID, ContentHash: source.Ref.ContentHash, AnchorID: anchorPointer},
			Purpose: source.Purpose, Required: true,
		})
	}
	sourceModels, err := (&ArtifactService{database: s.database, contents: s.contents, access: s.access, now: s.now}).
		validateSourceModels(ctx, proposalModel.ProjectID, draftID, actorUUID, sourceInputs)
	if err != nil {
		return ArtifactDraft{}, err
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var lockedArtifact storage.ArtifactModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", artifact.ID).Take(&lockedArtifact).Error; err != nil {
			return err
		}
		if lockedArtifact.LatestRevisionID == nil || *lockedArtifact.LatestRevisionID != base.ID {
			return ErrProposalStale
		}
		if (existingDraft == nil && lockedArtifact.LatestDraftID != nil) ||
			(existingDraft != nil && (lockedArtifact.LatestDraftID == nil || *lockedArtifact.LatestDraftID != existingDraft.ID)) {
			return ErrProposalStale
		}
		if existingDraft == nil {
			draftModel = storage.ArtifactDraftModel{
				ID: draftID, ArtifactID: artifact.ID, BaseRevisionID: &base.ID, Sequence: 1,
				ETag: draftETag(draftID, 1, draftContentRef.ContentHash), SchemaVersion: base.SchemaVersion,
				ContentStore: "mongo", ContentRef: draftContentRef.ID, ContentHash: draftContentRef.ContentHash,
				ByteSize: draftContentRef.ByteSize, Status: "draft", CreatedBy: actorUUID,
				UpdatedBy: actorUUID, CreatedAt: now, UpdatedAt: now,
			}
			if err := transaction.Create(&draftModel).Error; err != nil {
				return err
			}
		} else {
			var locked storage.ArtifactDraftModel
			if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", existingDraft.ID).Take(&locked).Error; err != nil {
				return err
			}
			if err := ensureExactCleanProposalDraft(transaction, locked, base); err != nil {
				return err
			}
			nextSequence := locked.Sequence + 1
			nextETag := draftETag(locked.ID, nextSequence, draftContentRef.ContentHash)
			if err := transaction.Model(&storage.ArtifactDraftModel{}).Where("id = ? AND etag = ?", locked.ID, locked.ETag).
				Updates(map[string]any{
					"sequence": nextSequence, "etag": nextETag, "content_ref": draftContentRef.ID,
					"content_hash": draftContentRef.ContentHash, "byte_size": draftContentRef.ByteSize,
					"updated_by": actorUUID, "updated_at": now,
				}).Error; err != nil {
				return err
			}
			locked.Sequence = nextSequence
			locked.ETag = nextETag
			locked.ContentRef = draftContentRef.ID
			locked.ContentHash = draftContentRef.ContentHash
			locked.ByteSize = draftContentRef.ByteSize
			locked.UpdatedBy = actorUUID
			locked.UpdatedAt = now
			draftModel = locked
		}
		if err := transaction.Where("draft_id = ?", draftModel.ID).Delete(&storage.ArtifactDraftSourceModel{}).Error; err != nil {
			return err
		}
		if len(sourceModels) > 0 {
			if err := transaction.Create(&sourceModels).Error; err != nil {
				return err
			}
		}
		if err := transaction.Model(&storage.ArtifactModel{}).Where("id = ?", artifact.ID).
			Updates(map[string]any{"latest_draft_id": draftModel.ID, "version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		acceptedCount, rejectedCount := proposalDecisionCounts(proposal.Operations)
		result := transaction.Model(&storage.OutputProposalModel{}).
			Where("id = ? AND version = ?", proposalModel.ID, input.Version).
			Updates(map[string]any{
				"status": proposal.Status, "version": proposal.Version,
				"content_ref": proposalContentRef.ID, "content_hash": proposalContentRef.ContentHash,
				"accepted_count": acceptedCount, "rejected_count": rejectedCount,
				"base_draft_id": draftModel.ID, "applied_by": actorUUID, "applied_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		if err := insertAudit(transaction, proposalModel.ProjectID, actorUUID, "proposal.applied", "output_proposal", proposalID, map[string]any{"draftId": draftModel.ID.String()}); err != nil {
			return err
		}
		return enqueue(transaction, "output_proposal", proposalID, "proposal.applied", "worksflow.proposal.applied", map[string]any{
			"projectId": proposalModel.ProjectID.String(), "artifactId": artifact.ID.String(),
			"proposalId": proposalID, "draftId": draftModel.ID.String(),
		})
	})
	if err != nil {
		return ArtifactDraft{}, err
	}
	pending = nil
	var finalizeErrors []error
	for _, contentID := range []string{draftContentRef.ID, proposalContentRef.ID} {
		if err := s.contents.Finalize(ctx, contentID); err != nil {
			finalizeErrors = append(finalizeErrors, err)
		}
	}
	if err := errors.Join(finalizeErrors...); err != nil {
		return ArtifactDraft{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return draftFromModel(draftModel, patched, sourcesFromModels(sourceModels)), nil
}

func validateProposalPatchedContent(kind string, payload json.RawMessage) error {
	report := ValidateArtifactContent(kind, payload)
	if report.Valid {
		return nil
	}
	encoded, _ := json.Marshal(report.Findings)
	return fmt.Errorf("%w: validation findings %s", ErrBlockingGate, encoded)
}

func (s *ProposalService) loadManifest(ctx context.Context, manifestID string) (domain.InputManifest, storage.InputManifestModel, error) {
	id, err := uuid.Parse(manifestID)
	if err != nil {
		return domain.InputManifest{}, storage.InputManifestModel{}, fmt.Errorf("%w: manifest id", ErrInvalidInput)
	}
	var model storage.InputManifestModel
	if err := s.database.WithContext(ctx).Where("id = ?", id).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.InputManifest{}, model, ErrNotFound
		}
		return domain.InputManifest{}, model, err
	}
	stored, err := s.contents.Get(ctx, model.ContentRef, model.ContentHash)
	if err != nil {
		return domain.InputManifest{}, model, err
	}
	var manifest domain.InputManifest
	if err := json.Unmarshal(stored.Payload, &manifest); err != nil {
		return domain.InputManifest{}, model, err
	}
	if err := manifest.Validate(); err != nil {
		return domain.InputManifest{}, model, err
	}
	if manifest.Hash != model.ManifestHash {
		return domain.InputManifest{}, model, ErrConflict
	}
	return manifest, model, nil
}

func (s *ProposalService) loadProposal(ctx context.Context, proposalID string) (domain.OutputProposal, storage.OutputProposalModel, error) {
	id, err := uuid.Parse(proposalID)
	if err != nil {
		return domain.OutputProposal{}, storage.OutputProposalModel{}, fmt.Errorf("%w: proposal id", ErrInvalidInput)
	}
	var model storage.OutputProposalModel
	if err := s.database.WithContext(ctx).Where("id = ?", id).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.OutputProposal{}, model, ErrNotFound
		}
		return domain.OutputProposal{}, model, err
	}
	proposal, err := s.proposalFromModel(ctx, model)
	return proposal, model, err
}

func (s *ProposalService) proposalFromModel(ctx context.Context, model storage.OutputProposalModel) (domain.OutputProposal, error) {
	stored, err := s.contents.Get(ctx, model.ContentRef, model.ContentHash)
	if err != nil {
		return domain.OutputProposal{}, err
	}
	var proposal domain.OutputProposal
	if err := json.Unmarshal(stored.Payload, &proposal); err != nil {
		return domain.OutputProposal{}, err
	}
	if err := proposal.ValidatePayloadHash(); err != nil {
		return domain.OutputProposal{}, err
	}
	if proposal.PayloadHash != model.PayloadHash || proposal.Version != model.Version || string(proposal.Status) != model.Status {
		return domain.OutputProposal{}, ErrConflict
	}
	return proposal, nil
}

func proposalDecisionCounts(operations []domain.ProposalOperation) (int, int) {
	accepted, rejected := 0, 0
	for _, operation := range operations {
		switch operation.Decision {
		case domain.DecisionAccepted, domain.DecisionApplied:
			accepted++
		case domain.DecisionRejected:
			rejected++
		}
	}
	return accepted, rejected
}

func dereferenceString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func trimmedStringPointer(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
