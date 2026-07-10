package delivery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type publishFakeProvider struct{}

func (publishFakeProvider) Name() string { return "fake" }
func (publishFakeProvider) Deploy(context.Context, ProviderRequest) (ProviderResult, error) {
	return ProviderResult{}, nil
}

func publishExactRef() core.VersionRef {
	return core.VersionRef{
		ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(),
		ContentHash: "sha256:" + strings.Repeat("a", 64),
	}
}

func publishDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 user=worksflow dbname=worksflow sslmode=disable", PreferSimpleProtocol: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

type publishAccessStub struct {
	action core.Action
	err    error
}

func (s *publishAccessStub) Authorize(_ context.Context, _, _ string, action core.Action) (core.Role, error) {
	s.action = action
	return core.RoleOwner, s.err
}

type publishLoaderStub struct {
	workspace        WorkspaceSnapshot
	bundle           core.WorkbenchBundle
	err              error
	manifest         string
	lineageErr       error
	lineageCalled    bool
	lineageRevision  core.VersionRef
	lineageManifest  string
	resolvedManifest string
}

func (s *publishLoaderStub) LoadFrozenWorkspace(_ context.Context, _, _ string, reference core.VersionRef, _ core.Action) (WorkspaceSnapshot, error) {
	if s.err != nil {
		return WorkspaceSnapshot{}, s.err
	}
	workspace := s.workspace
	if workspace.Revision.ArtifactID == "" {
		workspace.Revision = reference
	}
	return workspace, nil
}

func (s *publishLoaderStub) LoadBuildManifest(_ context.Context, _, _, manifestID string, _ core.Action) (core.WorkbenchBundle, error) {
	s.manifest = manifestID
	return s.bundle, s.err
}

func (s *publishLoaderStub) ResolveWorkspaceManifestLineage(_ context.Context, _, _ string, revision core.VersionRef, manifestID string, _ core.Action) (string, error) {
	s.lineageCalled = true
	s.lineageRevision = revision
	s.lineageManifest = manifestID
	if s.lineageErr != nil {
		return "", s.lineageErr
	}
	if s.resolvedManifest != "" {
		return s.resolvedManifest, nil
	}
	return manifestID, nil
}

func TestPublishSourceStoresResolvedDerivedProducerManifest(t *testing.T) {
	t.Parallel()

	projectID := uuid.NewString()
	rootID := uuid.NewString()
	derivedID := uuid.NewString()
	reference := publishExactRef()
	loader := &publishLoaderStub{
		workspace:        WorkspaceSnapshot{ProjectID: projectID, Revision: reference},
		bundle:           core.WorkbenchBundle{ID: rootID, ProjectID: projectID},
		resolvedManifest: derivedID,
	}
	service := &PublishService{loader: loader}
	source, err := service.resolvePublishSource(
		context.Background(), projectID, uuid.NewString(), core.ActionView, reference, rootID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if source.buildManifestID == nil || source.buildManifestID.String() != derivedID || loader.lineageManifest != rootID {
		t.Fatalf("publish source did not replace root selector with exact derived producer: source=%+v loader=%+v", source, loader)
	}
}

type publishQualityStub struct {
	report       QualityReport
	artifact     BuildArtifact
	err          error
	called       bool
	latestCalled bool
	getID        string
	loadCalled   bool
}

func (s *publishQualityStub) LatestPassingForRevision(context.Context, string, string, string) (QualityReport, error) {
	s.called = true
	s.latestCalled = true
	return s.report, s.err
}

func (s *publishQualityStub) Get(_ context.Context, qualityRunID, _ string) (QualityReport, error) {
	s.called = true
	s.getID = qualityRunID
	return s.report, s.err
}

func (s *publishQualityStub) LoadBuildArtifact(context.Context, string, BuildArtifactReference) (BuildArtifact, error) {
	s.loadCalled = true
	return s.artifact, s.err
}

type environmentResolverStub struct {
	called bool
}

func (s *environmentResolverStub) Resolve(_ context.Context, _ string, _ Environment, reference, _ string) (ResolvedEnvironment, error) {
	s.called = true
	return ResolvedEnvironment{Reference: reference, Public: map[string]string{}}, nil
}

func newPublishServiceForBoundaryTest(t *testing.T, access AccessControl, loader publishRevisionLoader, quality publishQualityReader, environments EnvironmentResolver) *PublishService {
	t.Helper()
	service, err := NewPublishService(publishDryRunDB(t), access, loader, quality, publishFakeProvider{}, environments)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestPublishProductionRequiresPassingReportForExactWorkspace(t *testing.T) {
	requested := publishExactRef()
	different := publishExactRef()
	projectID, manifestID, qualityRunID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	access := &publishAccessStub{}
	loader := &publishLoaderStub{workspace: WorkspaceSnapshot{
		ProjectID: projectID, Revision: requested,
		Files: []WorkspaceFile{{Path: "index.html", Content: "<h1>ready</h1>"}},
	}, bundle: core.WorkbenchBundle{ID: manifestID, ProjectID: projectID}}
	quality := &publishQualityStub{report: QualityReport{
		ID: qualityRunID, ProjectID: projectID, Passed: true, WorkspaceRevision: different,
	}}
	environments := &environmentResolverStub{}
	service := newPublishServiceForBoundaryTest(t, access, loader, quality, environments)
	_, err := service.Publish(context.Background(), projectID, uuid.NewString(), "", PublishInput{
		Environment: EnvironmentProduction, WorkspaceRevision: &requested,
		BuildManifestID: manifestID, QualityRunID: qualityRunID,
	})
	typed, ok := AsError(err)
	if !ok || typed.Code != CodeConflict {
		t.Fatalf("mismatched production quality report was accepted: %v", err)
	}
	if access.action != core.ActionPublish || !quality.called || quality.getID != qualityRunID || quality.latestCalled || environments.called {
		t.Fatalf("production boundary ordering is incorrect: action=%s quality=%v get=%q latest=%v environment=%v", access.action, quality.called, quality.getID, quality.latestCalled, environments.called)
	}
	if !loader.lineageCalled || loader.lineageManifest != manifestID || !exactVersionRefEqual(loader.lineageRevision, requested) {
		t.Fatalf("publish did not validate the exact workspace/manifest lineage: %+v", loader)
	}
}

func TestPublishPreviewAlsoRequiresPassingImmutableBuildArtifact(t *testing.T) {
	requested := publishExactRef()
	projectID, manifestID, qualityRunID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	access := &publishAccessStub{}
	loader := &publishLoaderStub{workspace: WorkspaceSnapshot{
		ProjectID: projectID, Revision: requested,
		Files: []WorkspaceFile{{Path: "index.html", Content: "<h1>ready</h1>"}},
	}, bundle: core.WorkbenchBundle{ID: manifestID, ProjectID: projectID}}
	quality := &publishQualityStub{report: QualityReport{
		ID: qualityRunID, ProjectID: projectID, Passed: true, WorkspaceRevision: requested,
	}}
	environments := &environmentResolverStub{}
	service := newPublishServiceForBoundaryTest(t, access, loader, quality, environments)
	_, err := service.Publish(context.Background(), projectID, uuid.NewString(), "", PublishInput{
		Environment: EnvironmentPreview, WorkspaceRevision: &requested,
		BuildManifestID: manifestID, QualityRunID: qualityRunID,
	})
	typed, ok := AsError(err)
	if !ok || typed.Code != CodeConflict {
		t.Fatalf("preview accepted a quality report without an immutable build artifact: %v", err)
	}
	if !quality.called || quality.loadCalled || environments.called {
		t.Fatalf("preview quality boundary ordering is incorrect: quality=%v load=%v environment=%v", quality.called, quality.loadCalled, environments.called)
	}
}

func TestPublishRejectsSensitiveWorkspaceBeforeEnvironmentOrProvider(t *testing.T) {
	requested := publishExactRef()
	projectID, manifestID, qualityRunID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	access := &publishAccessStub{}
	loader := &publishLoaderStub{workspace: WorkspaceSnapshot{
		Revision: requested,
		Files:    []WorkspaceFile{{Path: ".env.production", Content: "TOKEN=secret"}},
	}, bundle: core.WorkbenchBundle{ID: manifestID, ProjectID: projectID}}
	quality := &publishQualityStub{}
	environments := &environmentResolverStub{}
	service := newPublishServiceForBoundaryTest(t, access, loader, quality, environments)
	_, err := service.Publish(context.Background(), projectID, uuid.NewString(), "", PublishInput{
		Environment: EnvironmentPreview, WorkspaceRevision: &requested,
		BuildManifestID: manifestID, QualityRunID: qualityRunID,
	})
	typed, ok := AsError(err)
	if !ok || typed.Code != CodeSensitiveContent {
		t.Fatalf("sensitive workspace was not blocked: %v", err)
	}
	if access.action != core.ActionEdit || quality.called || environments.called {
		t.Fatalf("preview boundary ordering is incorrect: action=%s quality=%v environment=%v", access.action, quality.called, environments.called)
	}
}

func TestPublishRequiresExplicitQualityWorkspaceAndManifestProvenance(t *testing.T) {
	reference := publishExactRef()
	manifestID, qualityRunID := uuid.NewString(), uuid.NewString()
	tests := []struct {
		name  string
		input PublishInput
		field string
	}{
		{name: "quality run", input: PublishInput{
			Environment: EnvironmentPreview, WorkspaceRevision: &reference, BuildManifestID: manifestID,
		}, field: "qualityRunId"},
		{name: "workspace revision", input: PublishInput{
			Environment: EnvironmentPreview, BuildManifestID: manifestID, QualityRunID: qualityRunID,
		}, field: "workspaceRevision"},
		{name: "build manifest", input: PublishInput{
			Environment: EnvironmentPreview, WorkspaceRevision: &reference, QualityRunID: qualityRunID,
		}, field: "buildManifestId"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			access := &publishAccessStub{}
			loader := &publishLoaderStub{}
			quality := &publishQualityStub{}
			service := newPublishServiceForBoundaryTest(t, access, loader, quality, EmptyEnvironmentResolver{})
			_, err := service.Publish(context.Background(), uuid.NewString(), uuid.NewString(), "", test.input)
			typed, ok := AsError(err)
			if !ok || typed.Code != CodeInvalidInput || len(typed.Fields[test.field]) == 0 {
				t.Fatalf("missing %s was accepted: %v", test.field, err)
			}
			if access.action != "" || loader.manifest != "" || quality.called {
				t.Fatalf("invalid provenance crossed the publish boundary: access=%s loader=%+v quality=%+v", access.action, loader, quality)
			}
		})
	}
}

func TestPublishRejectsUntrustedWorkspaceManifestPairBeforeQuality(t *testing.T) {
	reference := publishExactRef()
	projectID, manifestID, qualityRunID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	loader := &publishLoaderStub{
		workspace:  WorkspaceSnapshot{ProjectID: projectID, Revision: reference, Files: []WorkspaceFile{{Path: "index.html", Content: "ready"}}},
		bundle:     core.WorkbenchBundle{ID: manifestID, ProjectID: projectID},
		lineageErr: conflict("workspaceRevision was not produced by the selected buildManifestId"),
	}
	quality := &publishQualityStub{}
	service := newPublishServiceForBoundaryTest(t, &publishAccessStub{}, loader, quality, EmptyEnvironmentResolver{})
	_, err := service.Publish(context.Background(), projectID, uuid.NewString(), "", PublishInput{
		Environment: EnvironmentPreview, WorkspaceRevision: &reference,
		BuildManifestID: manifestID, QualityRunID: qualityRunID,
	})
	if typed, ok := AsError(err); !ok || typed.Code != CodeConflict {
		t.Fatalf("forged workspace/manifest provenance was accepted: %v", err)
	}
	if !loader.lineageCalled || quality.called {
		t.Fatalf("lineage was not fail-closed before quality: loader=%+v quality=%+v", loader, quality)
	}
}

func TestPublishRejectsConflictingHTTPAndWorkflowQualityPins(t *testing.T) {
	reference := publishExactRef()
	service := newPublishServiceForBoundaryTest(t, &publishAccessStub{}, &publishLoaderStub{}, &publishQualityStub{}, EmptyEnvironmentResolver{})
	_, err := service.Publish(context.Background(), uuid.NewString(), uuid.NewString(), "", PublishInput{
		Environment: EnvironmentPreview, WorkspaceRevision: &reference,
		BuildManifestID: uuid.NewString(), QualityRunID: uuid.NewString(), WorkflowQualityRunID: uuid.NewString(),
	})
	if typed, ok := AsError(err); !ok || typed.Code != CodeInvalidInput {
		t.Fatalf("conflicting selected quality runs were accepted: %v", err)
	}
}

func TestPublishAuthorizationFailsBeforeLoadingFrozenContent(t *testing.T) {
	requested := publishExactRef()
	manifestID := uuid.NewString()
	access := &publishAccessStub{err: core.ErrForbidden}
	loader := &publishLoaderStub{err: errors.New("must not load"), bundle: core.WorkbenchBundle{ID: manifestID}}
	service := newPublishServiceForBoundaryTest(t, access, loader, &publishQualityStub{}, EmptyEnvironmentResolver{})
	_, err := service.Publish(context.Background(), uuid.NewString(), uuid.NewString(), "", PublishInput{
		Environment: EnvironmentProduction, WorkspaceRevision: &requested,
		BuildManifestID: manifestID, QualityRunID: uuid.NewString(),
	})
	if !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("authorization error was not preserved: %v", err)
	}
}

func TestResolvedEnvironmentAllowsOnlyExplicitPublicVariables(t *testing.T) {
	valid := ResolvedEnvironment{Reference: "data-runtime:preview", Public: map[string]string{
		"VITE_PUBLIC_API": "https://api.example.test",
	}}
	if err := validateResolvedEnvironment(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.Public = map[string]string{"DATABASE_PASSWORD": "abcdefghijklmnop"}
	if err := validateResolvedEnvironment(invalid); err == nil {
		t.Fatal("non-public environment variable reached publish metadata")
	}
	invalid.Public = map[string]string{"NEXT_PUBLIC_TOKEN": "sk-abcdefghijklmnopq"}
	if err := validateResolvedEnvironment(invalid); err == nil {
		t.Fatal("credential-like public value reached published source")
	}
}

func TestDeploymentVersionPreservesExactQualityBuildRelation(t *testing.T) {
	artifactID := uuid.New()
	qualityRunID := uuid.New()
	contentRef := uuid.NewString()
	contentHash := "sha256:" + strings.Repeat("a", 64)
	buildHash := "sha256:" + strings.Repeat("b", 64)
	entryPath := "index.html"
	fileCount := 2
	totalBytes := int64(42)
	model := deploymentVersionModel{
		ID: uuid.New(), DeploymentID: uuid.New(), Number: 1, Action: "publish",
		WorkspaceArtifactID: uuid.New(), WorkspaceRevisionID: uuid.New(), WorkspaceContentHash: "sha256:" + strings.Repeat("c", 64),
		QualityRunID: &qualityRunID, BuildArtifactID: &artifactID, BuildContentRef: &contentRef,
		BuildContentHash: &contentHash, BuildHash: &buildHash, BuildEntryPath: &entryPath,
		BuildFileCount: &fileCount, BuildTotalBytes: &totalBytes,
		EnvironmentVariableNames: []byte("[]"), EntryPath: entryPath,
	}
	version, err := deploymentVersionFromModel(model)
	if err != nil {
		t.Fatal(err)
	}
	if version.BuildArtifact == nil || version.BuildArtifact.ID != artifactID.String() ||
		version.BuildArtifact.ContentRef != contentRef || version.BuildArtifact.BuildHash != buildHash ||
		version.QualityRunID == nil || *version.QualityRunID != qualityRunID.String() {
		t.Fatalf("deployment lost immutable quality/build relation: %+v", version)
	}
}
