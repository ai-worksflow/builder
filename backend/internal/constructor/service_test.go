package constructor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type constructorServiceContentStore struct {
	item          content.StoredContent
	getCalls      int
	lastID        string
	lastHash      string
	getError      error
	finalizeCalls int
}

func (s *constructorServiceContentStore) PutPending(context.Context, string, string, string, int, json.RawMessage) (content.Reference, error) {
	return content.Reference{}, errors.New("PutPending is not expected in this test")
}

func (s *constructorServiceContentStore) Finalize(context.Context, string) error {
	s.finalizeCalls++
	return nil
}

func (s *constructorServiceContentStore) Abort(context.Context, string) error { return nil }

func (s *constructorServiceContentStore) Get(_ context.Context, id, expectedHash string) (content.StoredContent, error) {
	s.getCalls++
	s.lastID = id
	s.lastHash = expectedHash
	if s.getError != nil {
		return content.StoredContent{}, s.getError
	}
	if id != s.item.ID {
		return content.StoredContent{}, content.ErrContentNotFound
	}
	actualHash := constructorServicePayloadHash(s.item.Payload)
	if actualHash != s.item.ContentHash || expectedHash != s.item.ContentHash {
		return content.StoredContent{}, content.ErrHashMismatch
	}
	result := s.item
	result.Payload = append(json.RawMessage(nil), s.item.Payload...)
	return result, nil
}

type constructorServiceAccess struct {
	calls     int
	projectID string
	actorID   string
	action    core.Action
	err       error
}

func (s *constructorServiceAccess) Authorize(_ context.Context, projectID, actorID string, action core.Action) (core.Role, error) {
	s.calls++
	s.projectID = projectID
	s.actorID = actorID
	s.action = action
	return core.RoleOwner, s.err
}

type constructorServiceWorkbench struct {
	getCalls            int
	generationCalls     int
	lastGenerationID    string
	lastGenerationActor string
	bundle              core.WorkbenchBundle
	generationBundle    core.WorkbenchBundle
	getError            error
	generationError     error
}

func (s *constructorServiceWorkbench) GetBundle(context.Context, string, string) (core.WorkbenchBundle, error) {
	s.getCalls++
	return s.bundle, s.getError
}

func (s *constructorServiceWorkbench) GetBundleForGeneration(_ context.Context, bundleID, actorID string) (core.WorkbenchBundle, error) {
	s.generationCalls++
	s.lastGenerationID = bundleID
	s.lastGenerationActor = actorID
	return s.generationBundle, s.generationError
}

type constructorServiceTemplates struct {
	calls     int
	selection FullStackTemplateSelection
	resolved  ResolvedFullStackTemplate
	err       error
}

func (s *constructorServiceTemplates) ResolveFullStack(_ context.Context, selection FullStackTemplateSelection) (ResolvedFullStackTemplate, error) {
	s.calls++
	s.selection = selection
	return s.resolved, s.err
}

func TestCompileInputForManifestPreservesTrustedTemplateRuntime(t *testing.T) {
	t.Parallel()

	deliverySliceID := "page-messages"
	workspace := core.VersionRef{ArtifactID: "workspace", RevisionID: "workspace-r1", ContentHash: strings.Repeat("c", 64)}
	bundle := core.WorkbenchBundle{
		ID: "manifest", ProjectID: "project", DeliverySliceID: &deliverySliceID,
		ManifestHash: strings.Repeat("a", 64), CurrentWorkspaceRevision: &workspace,
	}
	sources := []PinnedBuildSource{{Ref: ExactRevisionRef{Kind: "blueprint", RevisionID: "blueprint-r1"}}}
	runtime := &TemplateRuntimeFacts{FullStackTemplateID: "stack", FullStackTemplateHash: strings.Repeat("b", 64)}
	resolved := ResolvedFullStackTemplate{
		Template: FullStackTemplateRef{ID: "stack", ContentHash: strings.Repeat("b", 64)},
		Releases: []TemplateReleaseRef{{ID: "web-release", Role: "web"}}, Runtime: runtime,
	}

	input := compileInputForManifest(bundle, sources, resolved)
	if input.ProjectID != bundle.ProjectID || input.DeliverySliceID != deliverySliceID || input.BuildManifest.ID != bundle.ID ||
		input.BuildManifest.ContentHash != bundle.ManifestHash || input.TemplateRuntime != runtime ||
		input.FullStackTemplate != resolved.Template || len(input.TemplateReleaseRefs) != 1 || len(input.Sources) != 1 {
		t.Fatalf("compile input = %#v", input)
	}
	if input.BaseWorkspace == nil || input.BaseWorkspace.ArtifactID != workspace.ArtifactID ||
		input.BaseWorkspace.RevisionID != workspace.RevisionID || input.BaseWorkspace.ContentHash != workspace.ContentHash {
		t.Fatalf("base workspace = %#v", input.BaseWorkspace)
	}
}

func TestRequireReadyRejectsNonExactSelectionBeforeDependencies(t *testing.T) {
	t.Parallel()

	database := constructorServiceDryRunDatabase(t)
	projectID, manifestID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	validHash := strings.Repeat("a", 64)
	tests := []struct {
		name      string
		selection ExactBuildContractSelection
	}{
		{name: "invalid contract id", selection: ExactBuildContractSelection{ID: "latest", ContractHash: validHash}},
		{name: "missing contract hash", selection: ExactBuildContractSelection{ID: uuid.NewString()}},
		{name: "invalid contract hash", selection: ExactBuildContractSelection{ID: uuid.NewString(), ContractHash: "main"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			access := &constructorServiceAccess{}
			workbench := &constructorServiceWorkbench{}
			store := &constructorServiceContentStore{}
			templates := &constructorServiceTemplates{}
			service, err := NewService(database, store, access, workbench, templates)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.RequireReady(context.Background(), projectID, manifestID, actorID, test.selection)
			if !errors.Is(err, core.ErrInvalidInput) {
				t.Fatalf("RequireReady() error = %v, want ErrInvalidInput", err)
			}
			if access.calls != 0 || workbench.generationCalls != 0 || workbench.getCalls != 0 || store.getCalls != 0 || templates.calls != 0 {
				t.Fatalf("invalid selection reached dependencies: access=%d generation=%d get=%d content=%d templates=%d", access.calls, workbench.generationCalls, workbench.getCalls, store.getCalls, templates.calls)
			}
		})
	}
}

func TestRequireReadyUsesGenerationWorkbenchGate(t *testing.T) {
	t.Parallel()

	database := constructorServiceDryRunDatabase(t)
	projectID, manifestID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	gateError := errors.New("generation workbench gate rejected bundle")
	access := &constructorServiceAccess{}
	workbench := &constructorServiceWorkbench{generationError: gateError}
	store := &constructorServiceContentStore{}
	templates := &constructorServiceTemplates{}
	service, err := NewService(database, store, access, workbench, templates)
	if err != nil {
		t.Fatal(err)
	}
	selection := ExactBuildContractSelection{ID: uuid.NewString(), ContractHash: strings.Repeat("a", 64)}
	_, err = service.RequireReady(context.Background(), projectID, manifestID, actorID, selection)
	if !errors.Is(err, gateError) {
		t.Fatalf("RequireReady() error = %v, want generation gate error", err)
	}
	if access.calls != 1 || access.projectID != projectID || access.actorID != actorID || access.action != core.ActionEdit {
		t.Fatalf("authorization call = %#v", access)
	}
	if workbench.generationCalls != 1 || workbench.getCalls != 0 || workbench.lastGenerationID != manifestID || workbench.lastGenerationActor != actorID {
		t.Fatalf("Workbench calls = %#v", workbench)
	}
	if store.getCalls != 0 || templates.calls != 0 {
		t.Fatalf("generation gate failure reached content/template dependencies: content=%d templates=%d", store.getCalls, templates.calls)
	}
}

func TestContractMatchesModelChecksEveryParentProjectionCount(t *testing.T) {
	t.Parallel()

	record := constructorServiceRecord(t)
	if !contractMatchesModel(record.contract, record.model) {
		t.Fatal("valid parent projection did not match canonical contract")
	}
	tests := []struct {
		name   string
		mutate func(*contractModel)
	}{
		{name: "must count", mutate: func(model *contractModel) { model.MustCount++ }},
		{name: "must ready count", mutate: func(model *contractModel) { model.MustReadyCount-- }},
		{name: "obligation count", mutate: func(model *contractModel) { model.ObligationCount++ }},
		{name: "source count", mutate: func(model *contractModel) { model.SourceCount++ }},
		{name: "template release count", mutate: func(model *contractModel) { model.TemplateReleaseCount++ }},
		{name: "blocking count", mutate: func(model *contractModel) { model.BlockingCount++ }},
		{name: "conflict count", mutate: func(model *contractModel) { model.ConflictCount++ }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			model := record.model
			test.mutate(&model)
			if contractMatchesModel(record.contract, model) {
				t.Fatal("tampered parent count matched canonical contract")
			}
		})
	}
}

func TestServiceRequireReadyPostgresExactFinalizedContract(t *testing.T) {
	constructorServiceRequirePostgres(t)
	fixture := newConstructorServicePostgresFixture(t)

	binding, err := fixture.service.RequireReady(
		context.Background(), fixture.record.model.ProjectID.String(), fixture.record.model.BuildManifestID.String(), fixture.record.model.CreatedBy.String(),
		ExactBuildContractSelection{ID: fixture.record.model.ID.String(), ContractHash: fixture.record.model.ContractHash},
	)
	if err != nil {
		t.Fatal(err)
	}
	if binding.ID != fixture.record.model.ID.String() || binding.ContractHash != fixture.record.model.ContractHash ||
		binding.ContentHash != fixture.record.model.ContentHash || binding.BuildManifestID != fixture.record.model.BuildManifestID.String() {
		t.Fatalf("binding = %#v", binding)
	}
	if fixture.workbench.generationCalls != 1 || fixture.workbench.getCalls != 0 {
		t.Fatalf("Workbench calls = generation %d ordinary %d", fixture.workbench.generationCalls, fixture.workbench.getCalls)
	}
	if fixture.contents.getCalls != 1 || fixture.contents.lastID != fixture.record.model.ContentRef || fixture.contents.lastHash != fixture.record.model.ContentHash {
		t.Fatalf("content lookup = calls %d id %q hash %q", fixture.contents.getCalls, fixture.contents.lastID, fixture.contents.lastHash)
	}
	if fixture.templates.calls != 1 || fixture.templates.selection.ID != fixture.record.model.FullStackTemplateID.String() ||
		fixture.templates.selection.ContentHash != fixture.record.model.FullStackTemplateHash {
		t.Fatalf("template lookup = calls %d selection %#v", fixture.templates.calls, fixture.templates.selection)
	}

	contentCalls := fixture.contents.getCalls
	wrongSelections := []ExactBuildContractSelection{
		{ID: uuid.NewString(), ContractHash: fixture.record.model.ContractHash},
		{ID: fixture.record.model.ID.String(), ContractHash: strings.Repeat("b", 64)},
	}
	for _, selection := range wrongSelections {
		if _, err := fixture.service.RequireReady(
			context.Background(), fixture.record.model.ProjectID.String(), fixture.record.model.BuildManifestID.String(), fixture.record.model.CreatedBy.String(), selection,
		); !errors.Is(err, core.ErrBlockingGate) {
			t.Fatalf("selection %#v error = %v, want ErrBlockingGate", selection, err)
		}
	}
	if fixture.contents.getCalls != contentCalls {
		t.Fatalf("non-exact selections reached hydrate: before=%d after=%d", contentCalls, fixture.contents.getCalls)
	}
}

func TestServiceRequireReadyPostgresRejectsPendingAndHashDrift(t *testing.T) {
	constructorServiceRequirePostgres(t)

	t.Run("pending content", func(t *testing.T) {
		fixture := newConstructorServicePostgresFixture(t)
		fixture.contents.item.State = content.StatePending
		_, err := fixture.service.RequireReady(
			context.Background(), fixture.record.model.ProjectID.String(), fixture.record.model.BuildManifestID.String(), fixture.record.model.CreatedBy.String(),
			ExactBuildContractSelection{ID: fixture.record.model.ID.String(), ContractHash: fixture.record.model.ContractHash},
		)
		if !errors.Is(err, core.ErrBlockingGate) || fixture.contents.getCalls != 1 {
			t.Fatalf("pending content error=%v calls=%d", err, fixture.contents.getCalls)
		}
		if fixture.templates.calls != 0 {
			t.Fatalf("pending content reached template selection: %d calls", fixture.templates.calls)
		}
	})

	t.Run("canonical contract hash drift", func(t *testing.T) {
		fixture := newConstructorServicePostgresFixture(t)
		driftedHash := strings.Repeat("b", 64)
		if err := fixture.database.Model(&contractModel{}).Where("id = ?", fixture.record.model.ID).Update("contract_hash", driftedHash).Error; err != nil {
			t.Fatal(err)
		}
		_, err := fixture.service.RequireReady(
			context.Background(), fixture.record.model.ProjectID.String(), fixture.record.model.BuildManifestID.String(), fixture.record.model.CreatedBy.String(),
			ExactBuildContractSelection{ID: fixture.record.model.ID.String(), ContractHash: driftedHash},
		)
		if !errors.Is(err, core.ErrBlockingGate) || fixture.contents.getCalls != 1 {
			t.Fatalf("hash drift error=%v calls=%d", err, fixture.contents.getCalls)
		}
	})

	t.Run("obsolete compiler identity", func(t *testing.T) {
		fixture := newConstructorServicePostgresFixture(t)
		legacyContractHash := rewriteConstructorFixtureCompilerIdentity(t, &fixture)
		_, err := fixture.service.RequireReady(
			context.Background(), fixture.record.model.ProjectID.String(), fixture.record.model.BuildManifestID.String(), fixture.record.model.CreatedBy.String(),
			ExactBuildContractSelection{ID: fixture.record.model.ID.String(), ContractHash: legacyContractHash},
		)
		if !errors.Is(err, core.ErrBlockingGate) || !strings.Contains(err.Error(), "compiler identity is obsolete") {
			t.Fatalf("obsolete compiler error = %v", err)
		}
		if fixture.templates.calls != 0 {
			t.Fatalf("obsolete compiler reached template selection: %d calls", fixture.templates.calls)
		}
	})

	t.Run("blob identity drift", func(t *testing.T) {
		fixture := newConstructorServicePostgresFixture(t)
		fixture.contents.item.AggregateID = uuid.NewString()
		_, err := fixture.service.RequireReady(
			context.Background(), fixture.record.model.ProjectID.String(), fixture.record.model.BuildManifestID.String(), fixture.record.model.CreatedBy.String(),
			ExactBuildContractSelection{ID: fixture.record.model.ID.String(), ContractHash: fixture.record.model.ContractHash},
		)
		if !errors.Is(err, core.ErrBlockingGate) || fixture.contents.getCalls != 1 {
			t.Fatalf("blob identity error=%v calls=%d", err, fixture.contents.getCalls)
		}
	})
}

func TestServiceGetForManifestHidesObsoleteCompilerButGetByIDRetainsAudit(t *testing.T) {
	constructorServiceRequirePostgres(t)
	fixture := newConstructorServicePostgresFixture(t)
	legacyContractHash := rewriteConstructorFixtureCompilerIdentity(t, &fixture)

	_, err := fixture.service.GetForManifest(
		context.Background(), fixture.record.model.BuildManifestID.String(), fixture.record.model.CreatedBy.String(),
	)
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("GetForManifest() error = %v, want ErrNotFound for obsolete compiler", err)
	}
	historical, err := fixture.service.Get(
		context.Background(), fixture.record.model.ID.String(), fixture.record.model.CreatedBy.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if historical.ContractHash != legacyContractHash || historical.Contract.Compiler.Version != "worksflow-constraint-compiler/v6" {
		t.Fatalf("historical contract = %#v", historical)
	}
}

func rewriteConstructorFixtureCompilerIdentity(t *testing.T, fixture *constructorServicePostgresFixture) string {
	t.Helper()
	legacy := fixture.record.contract
	legacy.Compiler = CompilerIdentity{Version: "worksflow-constraint-compiler/v6", Hash: strings.Repeat("d", 64)}
	legacyContractHash, err := domain.CanonicalHash(legacy)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	storedHash := constructorServicePayloadHash(payload)
	fixture.contents.item.Payload = payload
	fixture.contents.item.Reference.ContentHash = storedHash
	fixture.contents.item.Reference.ByteSize = int64(len(payload))
	if err := fixture.database.Model(&contractModel{}).Where("id = ?", fixture.record.model.ID).Updates(map[string]any{
		"compiler_version": legacy.Compiler.Version,
		"compiler_hash":    legacy.Compiler.Hash,
		"contract_hash":    legacyContractHash,
		"content_hash":     storedHash,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return legacyContractHash
}

func TestHydrateModelPostgresRejectsEveryProjectionDrift(t *testing.T) {
	constructorServiceRequirePostgres(t)
	fixture := newConstructorServicePostgresFixture(t)
	ctx := context.Background()

	if _, err := fixture.service.hydrateModel(ctx, fixture.record.model, true); err != nil {
		t.Fatalf("valid hydrate failed: %v", err)
	}

	t.Run("source projection", func(t *testing.T) {
		expected := fixture.record.contract.SourceRevisions[0]
		query := fixture.database.Model(&contractSourceModel{}).Where("contract_id = ? AND ordinal = 0", fixture.record.model.ID)
		if err := query.Update("purpose", "tampered").Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.service.hydrateModel(ctx, fixture.record.model, true); !errors.Is(err, core.ErrConflict) {
			t.Fatalf("source drift error = %v, want ErrConflict", err)
		}
		if err := query.Update("purpose", expected.Purpose).Error; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("template release projection", func(t *testing.T) {
		expected := fixture.record.contract.TemplateReleaseRefs[0]
		query := fixture.database.Model(&contractTemplateReleaseModel{}).Where("contract_id = ? AND ordinal = 0", fixture.record.model.ID)
		if err := query.Update("role", "tampered").Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.service.hydrateModel(ctx, fixture.record.model, true); !errors.Is(err, core.ErrConflict) {
			t.Fatalf("release drift error = %v, want ErrConflict", err)
		}
		if err := query.Update("role", expected.Role).Error; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("obligation projection", func(t *testing.T) {
		expected := fixture.record.contract.Obligations[0]
		query := fixture.database.Model(&contractObligationModel{}).Where("contract_id = ? AND obligation_id = ?", fixture.record.model.ID, expected.ID)
		if err := query.Update("source_anchor_id", "tampered").Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.service.hydrateModel(ctx, fixture.record.model, true); !errors.Is(err, core.ErrConflict) {
			t.Fatalf("obligation drift error = %v, want ErrConflict", err)
		}
		if err := query.Update("source_anchor_id", expected.SourceAnchorID).Error; err != nil {
			t.Fatal(err)
		}
	})
}

type constructorServiceTestRecord struct {
	model    contractModel
	contract ContractContent
	stored   content.StoredContent
}

func constructorServiceRecord(t *testing.T) constructorServiceTestRecord {
	t.Helper()
	projectID, manifestID, contractID, actorID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	templateID := uuid.New()
	input := readyCompileInput(t)
	input.ProjectID = projectID.String()
	input.BuildManifest.ID = manifestID.String()
	input.FullStackTemplate.ID = templateID.String()
	for index := range input.TemplateReleaseRefs {
		input.TemplateReleaseRefs[index].ID = uuid.NewString()
	}
	input.TemplateRuntime = compatibleTemplateRuntime(input.FullStackTemplate, input.TemplateReleaseRefs)
	for index := range input.Sources {
		input.Sources[index].Ref.ArtifactID = uuid.NewString()
		input.Sources[index].Ref.RevisionID = uuid.NewString()
	}
	var pageSpec PinnedBuildSource
	prototypeIndex := -1
	for index, source := range input.Sources {
		switch source.Ref.Kind {
		case "page_spec":
			pageSpec = source
		case "prototype":
			prototypeIndex = index
		}
	}
	if pageSpec.Ref.RevisionID == "" || prototypeIndex < 0 {
		t.Fatal("constructor fixture is missing PageSpec or Prototype")
	}
	prototypeArtifactID := input.Sources[prototypeIndex].Ref.ArtifactID
	prototypeRevisionID := input.Sources[prototypeIndex].Ref.RevisionID
	input.Sources[prototypeIndex] = readyPrototypeSource(t, pageSpec)
	input.Sources[prototypeIndex].Ref.ArtifactID = prototypeArtifactID
	input.Sources[prototypeIndex].Ref.RevisionID = prototypeRevisionID
	compiled, err := (Compiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Status != StatusReady {
		t.Fatalf("fixture contract is %s: gaps=%#v conflicts=%#v", compiled.Content.Status, compiled.Content.Gaps, compiled.Content.Conflicts)
	}
	payload, err := domain.CanonicalJSON(compiled.Content)
	if err != nil {
		t.Fatal(err)
	}
	contentID := uuid.NewString()
	contentHash := constructorServicePayloadHash(payload)
	mustCount, mustReadyCount := obligationCounts(compiled.Content.Obligations)
	createdAt := time.Unix(1_700_000_000, 0).UTC()
	model := contractModel{
		ID: contractID, ProjectID: projectID, BuildManifestID: manifestID, BuildManifestHash: compiled.Content.BuildManifest.ContentHash,
		FullStackTemplateID: templateID, FullStackTemplateHash: compiled.Content.FullStackTemplate.ContentHash,
		SchemaVersion: compiled.Content.SchemaVersion, CompilerVersion: compiled.Content.Compiler.Version, CompilerHash: compiled.Content.Compiler.Hash,
		ContentStore: "mongo", ContentRef: contentID, ContentHash: contentHash, ContractHash: compiled.ContractHash,
		Status: compiled.Content.Status, MustCount: mustCount, MustReadyCount: mustReadyCount,
		ObligationCount: len(compiled.Content.Obligations), SourceCount: len(compiled.Content.SourceRevisions),
		TemplateReleaseCount: len(compiled.Content.TemplateReleaseRefs), BlockingCount: blockingGapCount(compiled.Content.Gaps), ConflictCount: blockingConflictCount(compiled.Content.Conflicts),
		Version: 1, CreatedBy: actorID, CreatedAt: createdAt,
	}
	stored := content.StoredContent{
		Reference: content.Reference{ID: contentID, ContentHash: contentHash, ByteSize: int64(len(payload)), SchemaVersion: 2},
		ProjectID: projectID.String(), AggregateType: "application_build_contract", AggregateID: contractID.String(),
		State: content.StateFinalized, Payload: payload, CreatedAt: createdAt,
	}
	return constructorServiceTestRecord{model: model, contract: compiled.Content, stored: stored}
}

type constructorServicePostgresFixture struct {
	database  *gorm.DB
	service   *Service
	record    constructorServiceTestRecord
	contents  *constructorServiceContentStore
	access    *constructorServiceAccess
	workbench *constructorServiceWorkbench
	templates *constructorServiceTemplates
}

func newConstructorServicePostgresFixture(t *testing.T) constructorServicePostgresFixture {
	t.Helper()
	database := constructorServicePostgresDatabase(t)
	record := constructorServiceRecord(t)
	if err := database.Create(&record.model).Error; err != nil {
		t.Fatal(err)
	}
	for ordinal, source := range record.contract.SourceRevisions {
		artifactID, artifactErr := uuid.Parse(source.ArtifactID)
		revisionID, revisionErr := uuid.Parse(source.RevisionID)
		if artifactErr != nil || revisionErr != nil {
			t.Fatalf("invalid source fixture IDs: %v %v", artifactErr, revisionErr)
		}
		if err := database.Create(&contractSourceModel{
			ContractID: record.model.ID, Ordinal: ordinal, SourceKind: source.Kind, Purpose: source.Purpose,
			Required: source.Required, ArtifactID: artifactID, RevisionID: revisionID, ContentHash: source.ContentHash,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for ordinal, release := range record.contract.TemplateReleaseRefs {
		releaseID, err := uuid.Parse(release.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Create(&contractTemplateReleaseModel{
			ContractID: record.model.ID, Ordinal: ordinal, Role: release.Role,
			TemplateReleaseID: releaseID, TemplateReleaseContentHash: release.ReleaseHash,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, obligation := range record.contract.Obligations {
		artifactID, artifactErr := uuid.Parse(obligation.SourceRevision.ArtifactID)
		revisionID, revisionErr := uuid.Parse(obligation.SourceRevision.RevisionID)
		if artifactErr != nil || revisionErr != nil {
			t.Fatalf("invalid obligation fixture IDs: %v %v", artifactErr, revisionErr)
		}
		oracleIDs, _ := json.Marshal(obligation.OracleIDs)
		dependsOn, _ := json.Marshal(obligation.DependsOn)
		var blockingReason *string
		if obligation.BlockingReasonID != "" {
			value := obligation.BlockingReasonID
			blockingReason = &value
		}
		if err := database.Create(&contractObligationModel{
			ContractID: record.model.ID, ObligationID: obligation.ID, Level: obligation.Level, Kind: obligation.Kind,
			SourceArtifactID: artifactID, SourceRevisionID: revisionID, SourceContentHash: obligation.SourceRevision.ContentHash,
			SourceAnchorID: obligation.SourceAnchorID, OracleIDs: oracleIDs, DependsOn: dependsOn,
			Waivable: obligation.Waivable, Status: obligation.Status, BlockingReasonID: blockingReason,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	contents := &constructorServiceContentStore{item: record.stored}
	access := &constructorServiceAccess{}
	workbench := &constructorServiceWorkbench{generationBundle: core.WorkbenchBundle{
		ID: record.model.BuildManifestID.String(), ProjectID: record.model.ProjectID.String(), ManifestHash: record.model.BuildManifestHash,
	}}
	templates := &constructorServiceTemplates{resolved: ResolvedFullStackTemplate{
		Template: record.contract.FullStackTemplate, Releases: record.contract.TemplateReleaseRefs,
	}}
	service, err := NewService(database, contents, access, workbench, templates)
	if err != nil {
		t.Fatal(err)
	}
	return constructorServicePostgresFixture{
		database: database, service: service, record: record, contents: contents,
		access: access, workbench: workbench, templates: templates,
	}
}

func constructorServicePostgresDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	base, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := "constructor_service_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	database, err := gorm.Open(
		gormpostgres.Open(constructorServicePostgresDSNWithSearchPath(t, dsn, schema)),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		_ = base.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&contractModel{}, &contractSourceModel{}, &contractTemplateReleaseModel{}, &contractObligationModel{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDatabase, sqlErr := database.DB(); sqlErr == nil {
			_ = sqlDatabase.Close()
		}
		_ = base.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`).Error
		if baseSQL, sqlErr := base.DB(); sqlErr == nil {
			_ = baseSQL.Close()
		}
	})
	return database
}

func constructorServicePostgresDSNWithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}

func constructorServiceDryRunDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(
		gormpostgres.New(gormpostgres.Config{
			DSN: "host=127.0.0.1 user=constructor_test dbname=constructor_test sslmode=disable", PreferSimpleProtocol: true,
		}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func constructorServicePayloadHash(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func constructorServiceRequirePostgres(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN")) == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
}

var _ content.Store = (*constructorServiceContentStore)(nil)
var _ AccessAPI = (*constructorServiceAccess)(nil)
var _ WorkbenchAPI = (*constructorServiceWorkbench)(nil)
var _ TemplateResolver = (*constructorServiceTemplates)(nil)
