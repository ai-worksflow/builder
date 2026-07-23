package constructor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type AccessAPI interface {
	Authorize(context.Context, string, string, core.Action) (core.Role, error)
}

type WorkbenchAPI interface {
	GetBundle(context.Context, string, string) (core.WorkbenchBundle, error)
	GetBundleForGeneration(context.Context, string, string) (core.WorkbenchBundle, error)
}

type FullStackTemplateSelection struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

type ResolvedFullStackTemplate struct {
	Template FullStackTemplateRef
	Releases []TemplateReleaseRef
	Runtime  *TemplateRuntimeFacts
}

type TemplateResolver interface {
	ResolveFullStack(context.Context, FullStackTemplateSelection) (ResolvedFullStackTemplate, error)
}

type CompileForManifestInput struct {
	FullStackTemplate FullStackTemplateSelection `json:"fullStackTemplate"`
}

type BuildContractBinding struct {
	ID              string `json:"id"`
	ContentHash     string `json:"contentHash"`
	ContractHash    string `json:"contractHash"`
	BuildManifestID string `json:"buildManifestId"`
}

// ExactBuildContractSelection is the caller-pinned canonical identity used by
// generation and release gates. ContentHash identifies the storage object;
// ContractHash identifies the canonical BuildContract semantics and is the
// value callers must pin across state transitions.
type ExactBuildContractSelection struct {
	ID           string `json:"id"`
	ContractHash string `json:"contractHash"`
}

type Service struct {
	database  *gorm.DB
	contents  content.Store
	access    AccessAPI
	workbench WorkbenchAPI
	templates TemplateResolver
	compiler  Compiler
	now       func() time.Time
}

func NewService(database *gorm.DB, contents content.Store, access AccessAPI, workbench WorkbenchAPI, templates TemplateResolver) (*Service, error) {
	if database == nil || contents == nil || access == nil || workbench == nil || templates == nil {
		return nil, errors.New("constructor database, content store, access, Workbench, and Template Registry are required")
	}
	return &Service{database: database, contents: contents, access: access, workbench: workbench, templates: templates, compiler: Compiler{}, now: time.Now}, nil
}

func (s *Service) CompileForManifest(ctx context.Context, buildManifestID, actorID string, input CompileForManifestInput) (ApplicationBuildContract, error) {
	manifestUUID, err := uuid.Parse(strings.TrimSpace(buildManifestID))
	if err != nil {
		return ApplicationBuildContract{}, fmt.Errorf("%w: build manifest id", core.ErrInvalidInput)
	}
	templateUUID, err := uuid.Parse(strings.TrimSpace(input.FullStackTemplate.ID))
	if err != nil || !domain.IsCanonicalHash(input.FullStackTemplate.ContentHash) {
		return ApplicationBuildContract{}, fmt.Errorf("%w: exact FullStackTemplate reference", core.ErrInvalidInput)
	}
	bundle, err := s.workbench.GetBundle(ctx, manifestUUID.String(), actorID)
	if err != nil {
		return ApplicationBuildContract{}, err
	}
	if _, err := s.access.Authorize(ctx, bundle.ProjectID, actorID, core.ActionEdit); err != nil {
		return ApplicationBuildContract{}, err
	}
	resolved, err := s.templates.ResolveFullStack(ctx, FullStackTemplateSelection{ID: templateUUID.String(), ContentHash: strings.TrimSpace(input.FullStackTemplate.ContentHash)})
	if err != nil {
		return ApplicationBuildContract{}, err
	}
	if resolved.Template.ID != templateUUID.String() || resolved.Template.ContentHash != strings.TrimSpace(input.FullStackTemplate.ContentHash) {
		return ApplicationBuildContract{}, core.ErrConflict
	}
	sources, err := s.loadPinnedSources(ctx, bundle)
	if err != nil {
		return ApplicationBuildContract{}, err
	}
	compileInput := compileInputForManifest(bundle, sources, resolved)
	compiled, err := s.compiler.Compile(compileInput)
	if err != nil {
		return ApplicationBuildContract{}, err
	}
	projectUUID, actorUUID, err := parseProjectActor(bundle.ProjectID, actorID)
	if err != nil {
		return ApplicationBuildContract{}, err
	}
	return s.persist(ctx, projectUUID, actorUUID, manifestUUID, compiled)
}

func compileInputForManifest(bundle core.WorkbenchBundle, sources []PinnedBuildSource, resolved ResolvedFullStackTemplate) CompileInput {
	return CompileInput{
		ProjectID: bundle.ProjectID, DeliverySliceID: stringPointerValue(bundle.DeliverySliceID),
		DeliverySlicePageNodeID: stringPointerValue(bundle.BlueprintRevision.AnchorID),
		BuildManifest:           BuildManifestRef{ID: bundle.ID, ContentHash: bundle.ManifestHash},
		BaseWorkspace:           workspaceRef(bundle.CurrentWorkspaceRevision), Sources: sources,
		FullStackTemplate: resolved.Template, TemplateReleaseRefs: resolved.Releases,
		TemplateRuntime: resolved.Runtime,
	}
}

func (s *Service) Get(ctx context.Context, contractID, actorID string) (ApplicationBuildContract, error) {
	id, err := uuid.Parse(strings.TrimSpace(contractID))
	if err != nil {
		return ApplicationBuildContract{}, fmt.Errorf("%w: build contract id", core.ErrInvalidInput)
	}
	var model contractModel
	if err := s.database.WithContext(ctx).Where("id = ?", id).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ApplicationBuildContract{}, core.ErrNotFound
		}
		return ApplicationBuildContract{}, err
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, core.ActionView); err != nil {
		return ApplicationBuildContract{}, err
	}
	return s.fromModel(ctx, model)
}

func (s *Service) GetForManifest(ctx context.Context, buildManifestID, actorID string) (ApplicationBuildContract, error) {
	manifestID, err := uuid.Parse(strings.TrimSpace(buildManifestID))
	if err != nil {
		return ApplicationBuildContract{}, fmt.Errorf("%w: build manifest id", core.ErrInvalidInput)
	}
	currentCompiler, err := compilerIdentity()
	if err != nil {
		return ApplicationBuildContract{}, fmt.Errorf("resolve current Application Build Contract compiler identity: %w", err)
	}
	var model contractModel
	if err := s.database.WithContext(ctx).Where(
		"build_manifest_id = ? AND compiler_version = ? AND compiler_hash = ? AND status <> 'superseded'",
		manifestID, currentCompiler.Version, currentCompiler.Hash,
	).
		Order("created_at DESC, id DESC").Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ApplicationBuildContract{}, core.ErrNotFound
		}
		return ApplicationBuildContract{}, err
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, core.ActionView); err != nil {
		return ApplicationBuildContract{}, err
	}
	return s.fromModel(ctx, model)
}

func (s *Service) RequireReady(
	ctx context.Context,
	projectID, buildManifestID, actorID string,
	selection ExactBuildContractSelection,
) (BuildContractBinding, error) {
	projectUUID, err := uuid.Parse(strings.TrimSpace(projectID))
	if err != nil {
		return BuildContractBinding{}, fmt.Errorf("%w: project id", core.ErrInvalidInput)
	}
	manifestUUID, err := uuid.Parse(strings.TrimSpace(buildManifestID))
	if err != nil {
		return BuildContractBinding{}, fmt.Errorf("%w: build manifest id", core.ErrInvalidInput)
	}
	contractUUID, err := uuid.Parse(strings.TrimSpace(selection.ID))
	if err != nil || !domain.IsCanonicalHash(strings.TrimSpace(selection.ContractHash)) {
		return BuildContractBinding{}, fmt.Errorf("%w: exact Application Build Contract reference", core.ErrInvalidInput)
	}
	if _, err := s.access.Authorize(ctx, projectUUID.String(), actorID, core.ActionEdit); err != nil {
		return BuildContractBinding{}, err
	}
	bundle, err := s.workbench.GetBundleForGeneration(ctx, manifestUUID.String(), actorID)
	if err != nil {
		return BuildContractBinding{}, err
	}
	if bundle.ProjectID != projectUUID.String() || bundle.ManifestHash == "" {
		return BuildContractBinding{}, core.ErrConflict
	}
	var model contractModel
	if err := s.database.WithContext(ctx).Where(
		"id = ? AND project_id = ? AND build_manifest_id = ? AND contract_hash = ? AND status <> 'superseded'",
		contractUUID, projectUUID, manifestUUID, strings.TrimSpace(selection.ContractHash),
	).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return BuildContractBinding{}, fmt.Errorf("%w: exact Application Build Contract is missing or stale", core.ErrBlockingGate)
		}
		return BuildContractBinding{}, err
	}
	contract, err := s.hydrateModel(ctx, model, true)
	if err != nil {
		return BuildContractBinding{}, fmt.Errorf("%w: exact Application Build Contract content is unavailable or inconsistent: %v", core.ErrBlockingGate, err)
	}
	currentCompiler, err := compilerIdentity()
	if err != nil {
		return BuildContractBinding{}, fmt.Errorf("resolve current Application Build Contract compiler identity: %w", err)
	}
	if model.CompilerVersion != currentCompiler.Version || model.CompilerHash != currentCompiler.Hash {
		return BuildContractBinding{}, fmt.Errorf("%w: Application Build Contract compiler identity is obsolete; recompile the active Build Manifest", core.ErrBlockingGate)
	}
	if contract.Status != StatusReady || contract.BlockingCount != 0 || contract.ConflictCount != 0 || contract.MustCount == 0 || contract.MustReadyCount != contract.MustCount {
		return BuildContractBinding{}, fmt.Errorf("%w: Application Build Contract %s is blocked", core.ErrBlockingGate, model.ID)
	}
	if model.BuildManifestHash != bundle.ManifestHash {
		return BuildContractBinding{}, fmt.Errorf("%w: Application Build Contract does not match the active Build Manifest", core.ErrBlockingGate)
	}
	if _, err := s.templates.ResolveFullStack(ctx, FullStackTemplateSelection{ID: model.FullStackTemplateID.String(), ContentHash: model.FullStackTemplateHash}); err != nil {
		return BuildContractBinding{}, fmt.Errorf("%w: pinned FullStackTemplate is no longer selectable: %v", core.ErrBlockingGate, err)
	}
	return BuildContractBinding{ID: contract.ID, ContentHash: contract.ContentHash, ContractHash: contract.ContractHash, BuildManifestID: contract.BuildManifestID}, nil
}

// RequireReadyForImplementation adapts the constructor's richer binding to
// the small core verification interface. It never resolves "latest": callers
// must provide the exact id and canonical contract hash they reviewed.
func (s *Service) RequireReadyForImplementation(
	ctx context.Context,
	projectID, buildManifestID, actorID string,
	selection core.ApplicationBuildContractRef,
) (core.ApplicationBuildContractRef, error) {
	binding, err := s.RequireReady(ctx, projectID, buildManifestID, actorID, ExactBuildContractSelection{
		ID: selection.ID, ContractHash: selection.ContractHash,
	})
	if err != nil {
		return core.ApplicationBuildContractRef{}, err
	}
	if binding.ID != selection.ID || binding.ContractHash != selection.ContractHash || binding.BuildManifestID != buildManifestID {
		return core.ApplicationBuildContractRef{}, core.ErrConflict
	}
	return core.ApplicationBuildContractRef{ID: binding.ID, ContractHash: binding.ContractHash}, nil
}

func (s *Service) loadPinnedSources(ctx context.Context, bundle core.WorkbenchBundle) ([]PinnedBuildSource, error) {
	type pendingRef struct {
		ref     core.VersionRef
		purpose string
	}
	values := []pendingRef{
		{bundle.BlueprintRevision, "blueprint"}, {bundle.PageSpecRevision, "page_spec"}, {bundle.PrototypeRevision, "prototype"},
	}
	for _, ref := range bundle.RequirementRevisions {
		values = append(values, pendingRef{ref, "requirement"})
	}
	for _, ref := range bundle.ContractRevisions {
		values = append(values, pendingRef{ref, "contract"})
	}
	for _, ref := range bundle.DesignSystemRevisions {
		values = append(values, pendingRef{ref, "design_system"})
	}
	for _, contextRevision := range bundle.ContextRevisions {
		values = append(values, pendingRef{contextRevision.Revision, contextRevision.Kind})
	}
	deduplicated := map[string]pendingRef{}
	for _, value := range values {
		key := value.ref.ArtifactID + ":" + value.ref.RevisionID + ":" + value.ref.ContentHash
		if previous, exists := deduplicated[key]; exists && previous.purpose != value.purpose {
			return nil, core.ErrConflict
		}
		deduplicated[key] = value
	}
	keys := make([]string, 0, len(deduplicated))
	for key := range deduplicated {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]PinnedBuildSource, 0, len(keys))
	projectUUID, err := uuid.Parse(bundle.ProjectID)
	if err != nil {
		return nil, core.ErrConflict
	}
	for _, key := range keys {
		value := deduplicated[key]
		artifactID, err := uuid.Parse(value.ref.ArtifactID)
		if err != nil {
			return nil, core.ErrConflict
		}
		revisionID, err := uuid.Parse(value.ref.RevisionID)
		if err != nil || !domain.IsCanonicalHash(value.ref.ContentHash) {
			return nil, core.ErrConflict
		}
		var artifact storage.ArtifactModel
		if err := s.database.WithContext(ctx).Where("id = ? AND project_id = ?", artifactID, projectUUID).Take(&artifact).Error; err != nil {
			return nil, normalizeNotFound(err)
		}
		var revision storage.ArtifactRevisionModel
		if err := s.database.WithContext(ctx).Where(
			"id = ? AND artifact_id = ? AND content_hash = ? AND workflow_status = 'approved'", revisionID, artifactID, value.ref.ContentHash,
		).Take(&revision).Error; err != nil {
			return nil, normalizeNotFound(err)
		}
		stored, err := s.contents.Get(ctx, revision.ContentRef, revision.ContentHash)
		if err != nil {
			return nil, err
		}
		result = append(result, PinnedBuildSource{
			Ref: ExactRevisionRef{
				Kind: artifact.Kind, Purpose: value.purpose, Required: true,
				ArtifactID: artifact.ID.String(), RevisionID: revision.ID.String(), ContentHash: revision.ContentHash, ApprovalStatus: revision.WorkflowStatus,
			},
			Content: append(json.RawMessage(nil), stored.Payload...),
		})
	}
	return result, nil
}

func (s *Service) persist(ctx context.Context, projectID, actorID, manifestID uuid.UUID, compiled CompiledContract) (ApplicationBuildContract, error) {
	templateID, err := uuid.Parse(compiled.Content.FullStackTemplate.ID)
	if err != nil {
		return ApplicationBuildContract{}, core.ErrConflict
	}
	var existing contractModel
	err = s.database.WithContext(ctx).Where(
		"project_id = ? AND build_manifest_id = ? AND full_stack_template_id = ? AND full_stack_template_hash = ? AND compiler_hash = ?",
		projectID, manifestID, templateID, compiled.Content.FullStackTemplate.ContentHash, compiled.Content.Compiler.Hash,
	).Take(&existing).Error
	if err == nil {
		if existing.ContractHash != compiled.ContentHash {
			return ApplicationBuildContract{}, core.ErrConflict
		}
		return s.fromModel(ctx, existing)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return ApplicationBuildContract{}, err
	}

	contractID := uuid.New()
	payload, err := json.Marshal(compiled.Content)
	if err != nil {
		return ApplicationBuildContract{}, err
	}
	contentRef, err := s.contents.PutPending(ctx, projectID.String(), "application_build_contract", contractID.String(), 2, payload)
	if err != nil {
		return ApplicationBuildContract{}, err
	}
	abort := true
	defer func() {
		if abort {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	mustCount, mustReadyCount := obligationCounts(compiled.Content.Obligations)
	now := s.now().UTC()
	model := contractModel{
		ID: contractID, ProjectID: projectID, BuildManifestID: manifestID, BuildManifestHash: compiled.Content.BuildManifest.ContentHash,
		FullStackTemplateID: templateID, FullStackTemplateHash: compiled.Content.FullStackTemplate.ContentHash,
		SchemaVersion: compiled.Content.SchemaVersion, CompilerVersion: compiled.Content.Compiler.Version, CompilerHash: compiled.Content.Compiler.Hash,
		ContentStore: "mongo", ContentRef: contentRef.ID, ContentHash: contentRef.ContentHash, ContractHash: compiled.ContentHash,
		Status: compiled.Content.Status, MustCount: mustCount, MustReadyCount: mustReadyCount,
		ObligationCount: len(compiled.Content.Obligations), SourceCount: len(compiled.Content.SourceRevisions),
		TemplateReleaseCount: len(compiled.Content.TemplateReleaseRefs), BlockingCount: blockingGapCount(compiled.Content.Gaps), ConflictCount: blockingConflictCount(compiled.Content.Conflicts),
		Version: 1, CreatedBy: actorID, CreatedAt: now,
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&model).Error; err != nil {
			return err
		}
		for ordinal, source := range compiled.Content.SourceRevisions {
			artifactID, artifactErr := uuid.Parse(source.ArtifactID)
			revisionID, revisionErr := uuid.Parse(source.RevisionID)
			if artifactErr != nil || revisionErr != nil {
				return core.ErrConflict
			}
			if err := transaction.Create(&contractSourceModel{
				ContractID: contractID, Ordinal: ordinal, SourceKind: source.Kind, Purpose: source.Purpose, Required: source.Required,
				ArtifactID: artifactID, RevisionID: revisionID, ContentHash: source.ContentHash,
			}).Error; err != nil {
				return err
			}
		}
		for ordinal, release := range compiled.Content.TemplateReleaseRefs {
			releaseID, parseErr := uuid.Parse(release.ID)
			if parseErr != nil {
				return core.ErrConflict
			}
			if err := transaction.Create(&contractTemplateReleaseModel{
				ContractID: contractID, Ordinal: ordinal, Role: release.Role,
				TemplateReleaseID: releaseID, TemplateReleaseContentHash: release.ReleaseHash,
			}).Error; err != nil {
				return err
			}
		}
		for _, obligation := range compiled.Content.Obligations {
			artifactID, artifactErr := uuid.Parse(obligation.SourceRevision.ArtifactID)
			revisionID, revisionErr := uuid.Parse(obligation.SourceRevision.RevisionID)
			if artifactErr != nil || revisionErr != nil {
				return core.ErrConflict
			}
			oracleIDs, _ := json.Marshal(obligation.OracleIDs)
			dependsOn, _ := json.Marshal(obligation.DependsOn)
			var blockingReason *string
			if obligation.BlockingReasonID != "" {
				value := obligation.BlockingReasonID
				blockingReason = &value
			}
			if err := transaction.Create(&contractObligationModel{
				ContractID: contractID, ObligationID: obligation.ID, Level: obligation.Level, Kind: obligation.Kind,
				SourceArtifactID: artifactID, SourceRevisionID: revisionID, SourceContentHash: obligation.SourceRevision.ContentHash,
				SourceAnchorID: obligation.SourceAnchorID, OracleIDs: oracleIDs, DependsOn: dependsOn,
				Waivable: obligation.Waivable, Status: obligation.Status, BlockingReasonID: blockingReason,
			}).Error; err != nil {
				return err
			}
		}
		if err := insertAudit(transaction, projectID, actorID, model, compiled.Content); err != nil {
			return err
		}
		return enqueue(transaction, model, compiled.Content)
	})
	if err != nil {
		if uniqueViolation(err) {
			var concurrent contractModel
			if loadErr := s.database.WithContext(ctx).Where(
				"project_id = ? AND build_manifest_id = ? AND full_stack_template_id = ? AND full_stack_template_hash = ? AND compiler_hash = ?",
				projectID, manifestID, templateID, compiled.Content.FullStackTemplate.ContentHash, compiled.Content.Compiler.Hash,
			).Take(&concurrent).Error; loadErr == nil && concurrent.ContractHash == compiled.ContentHash {
				return s.fromModel(ctx, concurrent)
			}
		}
		return ApplicationBuildContract{}, err
	}
	abort = false
	if err := s.contents.Finalize(ctx, contentRef.ID); err != nil {
		return ApplicationBuildContract{}, fmt.Errorf("%w: finalize Application Build Contract content: %v", core.ErrContentNotReady, err)
	}
	return s.fromModel(ctx, model)
}

func (s *Service) fromModel(ctx context.Context, model contractModel) (ApplicationBuildContract, error) {
	return s.hydrateModel(ctx, model, false)
}

func (s *Service) hydrateModel(ctx context.Context, model contractModel, requireFinalized bool) (ApplicationBuildContract, error) {
	stored, err := s.contents.Get(ctx, model.ContentRef, model.ContentHash)
	if err != nil {
		return ApplicationBuildContract{}, err
	}
	if stored.ProjectID != model.ProjectID.String() || stored.AggregateType != "application_build_contract" ||
		stored.AggregateID != model.ID.String() || stored.SchemaVersion != 2 {
		return ApplicationBuildContract{}, core.ErrConflict
	}
	if requireFinalized && stored.State != content.StateFinalized {
		return ApplicationBuildContract{}, core.ErrContentNotReady
	}
	var contract ContractContent
	if err := json.Unmarshal(stored.Payload, &contract); err != nil {
		return ApplicationBuildContract{}, core.ErrConflict
	}
	hash, err := domain.CanonicalHash(contract)
	if err != nil || hash != model.ContractHash || !contractMatchesModel(contract, model) {
		return ApplicationBuildContract{}, core.ErrConflict
	}
	if err := s.validateChildProjections(ctx, model.ID, contract); err != nil {
		return ApplicationBuildContract{}, core.ErrConflict
	}
	return ApplicationBuildContract{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(), BuildManifestID: model.BuildManifestID.String(), Status: model.Status,
		Version: model.Version, ETag: contractETag(model.ID, model.Version), ContentHash: model.ContentHash, ContractHash: model.ContractHash, Contract: contract,
		MustCount: model.MustCount, MustReadyCount: model.MustReadyCount, BlockingCount: model.BlockingCount, ConflictCount: model.ConflictCount,
		CreatedBy: model.CreatedBy.String(), CreatedAt: model.CreatedAt, SupersededAt: model.SupersededAt,
	}, nil
}

func contractMatchesModel(contract ContractContent, model contractModel) bool {
	if contract.SchemaVersion != model.SchemaVersion || contract.Compiler.Version != model.CompilerVersion ||
		contract.Compiler.Hash != model.CompilerHash || contract.ProjectID != model.ProjectID.String() ||
		contract.BuildManifest.ID != model.BuildManifestID.String() || contract.BuildManifest.ContentHash != model.BuildManifestHash ||
		contract.FullStackTemplate.ID != model.FullStackTemplateID.String() || contract.FullStackTemplate.ContentHash != model.FullStackTemplateHash {
		return false
	}
	if model.Status == StatusSuperseded {
		if contract.Status != StatusReady && contract.Status != StatusBlocked {
			return false
		}
	} else if contract.Status != model.Status {
		return false
	}
	mustCount, mustReadyCount := obligationCounts(contract.Obligations)
	return model.MustCount == mustCount && model.MustReadyCount == mustReadyCount &&
		model.ObligationCount == len(contract.Obligations) && model.SourceCount == len(contract.SourceRevisions) &&
		model.TemplateReleaseCount == len(contract.TemplateReleaseRefs) &&
		model.BlockingCount == blockingGapCount(contract.Gaps) && model.ConflictCount == blockingConflictCount(contract.Conflicts)
}

func (s *Service) validateChildProjections(ctx context.Context, contractID uuid.UUID, contract ContractContent) error {
	var sources []contractSourceModel
	if err := s.database.WithContext(ctx).Where("contract_id = ?", contractID).Order("ordinal ASC").Find(&sources).Error; err != nil {
		return err
	}
	if len(sources) != len(contract.SourceRevisions) {
		return core.ErrConflict
	}
	for index, projected := range sources {
		expected := contract.SourceRevisions[index]
		if projected.Ordinal != index || projected.SourceKind != expected.Kind || projected.Purpose != expected.Purpose ||
			projected.Required != expected.Required || projected.ArtifactID.String() != expected.ArtifactID ||
			projected.RevisionID.String() != expected.RevisionID || projected.ContentHash != expected.ContentHash {
			return core.ErrConflict
		}
	}

	var releases []contractTemplateReleaseModel
	if err := s.database.WithContext(ctx).Where("contract_id = ?", contractID).Order("ordinal ASC").Find(&releases).Error; err != nil {
		return err
	}
	if len(releases) != len(contract.TemplateReleaseRefs) {
		return core.ErrConflict
	}
	for index, projected := range releases {
		expected := contract.TemplateReleaseRefs[index]
		if projected.Ordinal != index || projected.Role != expected.Role || projected.TemplateReleaseID.String() != expected.ID ||
			projected.TemplateReleaseContentHash != expected.ReleaseHash {
			return core.ErrConflict
		}
	}

	var obligations []contractObligationModel
	if err := s.database.WithContext(ctx).Where("contract_id = ?", contractID).Order("obligation_id ASC").Find(&obligations).Error; err != nil {
		return err
	}
	if len(obligations) != len(contract.Obligations) {
		return core.ErrConflict
	}
	expectedObligations := make(map[string]Obligation, len(contract.Obligations))
	for _, obligation := range contract.Obligations {
		if _, duplicate := expectedObligations[obligation.ID]; duplicate {
			return core.ErrConflict
		}
		expectedObligations[obligation.ID] = obligation
	}
	for _, projected := range obligations {
		expected, exists := expectedObligations[projected.ObligationID]
		if !exists {
			return core.ErrConflict
		}
		var oracleIDs, dependsOn []string
		if err := json.Unmarshal(projected.OracleIDs, &oracleIDs); err != nil {
			return core.ErrConflict
		}
		if err := json.Unmarshal(projected.DependsOn, &dependsOn); err != nil {
			return core.ErrConflict
		}
		blockingReason := ""
		if projected.BlockingReasonID != nil {
			blockingReason = *projected.BlockingReasonID
		}
		if projected.Level != expected.Level || projected.Kind != expected.Kind ||
			projected.SourceArtifactID.String() != expected.SourceRevision.ArtifactID ||
			projected.SourceRevisionID.String() != expected.SourceRevision.RevisionID ||
			projected.SourceContentHash != expected.SourceRevision.ContentHash || projected.SourceAnchorID != expected.SourceAnchorID ||
			!equalStrings(oracleIDs, expected.OracleIDs) || !equalStrings(dependsOn, expected.DependsOn) ||
			projected.Waivable != expected.Waivable || projected.Status != expected.Status || blockingReason != expected.BlockingReasonID {
			return core.ErrConflict
		}
	}
	return nil
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

func workspaceRef(value *core.VersionRef) *WorkspaceRevisionRef {
	if value == nil {
		return nil
	}
	return &WorkspaceRevisionRef{ArtifactID: value.ArtifactID, RevisionID: value.RevisionID, ContentHash: value.ContentHash}
}

func parseProjectActor(projectID, actorID string) (uuid.UUID, uuid.UUID, error) {
	projectUUID, err := uuid.Parse(strings.TrimSpace(projectID))
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("%w: project id", core.ErrInvalidInput)
	}
	actorUUID, err := uuid.Parse(strings.TrimSpace(actorID))
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("%w: actor id", core.ErrInvalidInput)
	}
	return projectUUID, actorUUID, nil
}

func normalizeNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return core.ErrConflict
	}
	return err
}

func obligationCounts(obligations []Obligation) (int, int) {
	must, ready := 0, 0
	for _, obligation := range obligations {
		if obligation.Level != "must" {
			continue
		}
		must++
		if obligation.Status == StatusReady || obligation.Status == "waived" {
			ready++
		}
	}
	return must, ready
}

func blockingGapCount(gaps []BuildGap) int {
	count := 0
	for _, gap := range gaps {
		if gap.Blocking {
			count++
		}
	}
	return count
}

func blockingConflictCount(conflicts []BuildConflict) int {
	count := 0
	for _, conflict := range conflicts {
		if conflict.Blocking {
			count++
		}
	}
	return count
}

func contractETag(id uuid.UUID, version uint64) string {
	return fmt.Sprintf(`"application-build-contract:%s:%d"`, id, version)
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func uniqueViolation(err error) bool {
	message := strings.ToLower(fmt.Sprint(err))
	return strings.Contains(message, "duplicate key") || strings.Contains(message, "unique constraint")
}

func insertAudit(transaction *gorm.DB, projectID, actorID uuid.UUID, model contractModel, content ContractContent) error {
	metadata, err := json.Marshal(map[string]any{
		"buildManifestId": model.BuildManifestID.String(), "contractHash": model.ContractHash,
		"status": model.Status, "mustCount": model.MustCount, "mustReadyCount": model.MustReadyCount,
		"blockingCount": model.BlockingCount, "conflictCount": model.ConflictCount,
		"fullStackTemplate": content.FullStackTemplate,
	})
	if err != nil {
		return err
	}
	var requestID *string
	if value := core.RequestIDFromContext(transaction.Statement.Context); value != "" {
		requestID = &value
	}
	return transaction.Create(&storage.AuditEventModel{
		ID: uuid.New(), ProjectID: &projectID, ActorID: &actorID, RequestID: requestID,
		Action: "application_build_contract.compiled", TargetType: "application_build_contract", TargetID: model.ID.String(),
		Metadata: metadata, CreatedAt: model.CreatedAt,
	}).Error
}

func enqueue(transaction *gorm.DB, model contractModel, content ContractContent) error {
	payload, err := json.Marshal(map[string]any{
		"projectId": model.ProjectID.String(), "applicationBuildContractId": model.ID.String(),
		"buildManifestId": model.BuildManifestID.String(), "contractHash": model.ContractHash,
		"status": model.Status, "fullStackTemplate": content.FullStackTemplate,
	})
	if err != nil {
		return err
	}
	return transaction.Create(&storage.OutboxEventModel{
		ID: uuid.New(), AggregateType: "application_build_contract", AggregateID: model.ID.String(),
		EventType: "application_build_contract.compiled", Subject: "worksflow.application.build.contract.compiled",
		Payload: payload, Headers: json.RawMessage(`{}`), AvailableAt: model.CreatedAt, CreatedAt: model.CreatedAt,
	}).Error
}
