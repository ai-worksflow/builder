package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	runtime "github.com/worksflow/builder/backend/internal/workflow"
	"github.com/worksflow/builder/backend/migrations"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type publishLineagePGContentStore struct {
	mu    sync.Mutex
	items map[string]content.StoredContent
}

func newPublishLineagePGContentStore() *publishLineagePGContentStore {
	return &publishLineagePGContentStore{items: map[string]content.StoredContent{}}
}

func (s *publishLineagePGContentStore) put(
	projectID string,
	aggregateType string,
	aggregateID string,
	payload json.RawMessage,
) content.Reference {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.NewString()
	reference := content.Reference{
		ID: id, ContentHash: publishLineagePGHashBytes(payload), ByteSize: int64(len(payload)), SchemaVersion: 1,
	}
	now := time.Now().UTC()
	s.items[id] = content.StoredContent{
		Reference: reference, ProjectID: projectID, AggregateType: aggregateType, AggregateID: aggregateID,
		State: content.StateFinalized, Payload: append(json.RawMessage(nil), payload...),
		CreatedAt: now, FinalizedAt: &now,
	}
	return reference
}

func (s *publishLineagePGContentStore) PutPending(
	_ context.Context,
	projectID string,
	aggregateType string,
	aggregateID string,
	_ int,
	payload json.RawMessage,
) (content.Reference, error) {
	return s.put(projectID, aggregateType, aggregateID, payload), nil
}

func (*publishLineagePGContentStore) Finalize(context.Context, string) error { return nil }
func (*publishLineagePGContentStore) Abort(context.Context, string) error    { return nil }

func (s *publishLineagePGContentStore) Get(
	_ context.Context,
	contentID string,
	expectedHash string,
) (content.StoredContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.items[contentID]
	if !exists {
		return content.StoredContent{}, content.ErrContentNotFound
	}
	if stored.ContentHash != expectedHash {
		return content.StoredContent{}, content.ErrHashMismatch
	}
	stored.Payload = append(json.RawMessage(nil), stored.Payload...)
	return stored, nil
}

type publishLineagePGAccess struct{}

func (publishLineagePGAccess) Authorize(context.Context, string, string, core.Action) (core.Role, error) {
	return core.RoleOwner, nil
}

type publishLineagePGQuality struct {
	report   QualityReport
	artifact BuildArtifact
}

func (q publishLineagePGQuality) Get(context.Context, string, string) (QualityReport, error) {
	return q.report, nil
}

func (q publishLineagePGQuality) LoadBuildArtifact(
	context.Context,
	string,
	BuildArtifactReference,
) (BuildArtifact, error) {
	return q.artifact, nil
}

type publishLineagePGProvider struct{}

func (publishLineagePGProvider) Name() string { return "lineage-pg" }

func (publishLineagePGProvider) Deploy(_ context.Context, request ProviderRequest) (ProviderResult, error) {
	return ProviderResult{
		Reference:  "lineage-pg:" + request.VersionID,
		PublicURL:  "https://publish.example.test/" + request.VersionID,
		EntryPath:  request.BuildArtifact.EntryPath,
		Checksum:   request.BuildArtifact.BuildHash,
		FileCount:  request.BuildArtifact.FileCount,
		TotalBytes: request.BuildArtifact.TotalBytes,
	}, nil
}

type publishLineagePGEnvironment struct{}

func (publishLineagePGEnvironment) Resolve(
	_ context.Context,
	_ string,
	_ Environment,
	_ string,
	_ string,
) (ResolvedEnvironment, error) {
	return ResolvedEnvironment{Reference: "lineage-pg-preview", Public: map[string]string{}}, nil
}

func TestPublishResolvesAndStoresDerivedWorkspaceProducerPostgres(t *testing.T) {
	database, cleanup := publishLineagePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	contents := newPublishLineagePGContentStore()
	actorID := uuid.New()
	projectID := uuid.New()
	otherProjectID := uuid.New()
	publishLineageSeedUserProject(t, database, actorID, projectID, "publish-lineage")
	publishLineageSeedProject(t, database, actorID, otherProjectID, "publish-lineage-other")
	definitionVersionID := publishLineageSeedWorkflowDefinition(t, database, actorID, projectID, now)
	runID := publishLineageSeedWorkflowRun(t, database, actorID, projectID, definitionVersionID, now)
	otherRunID := publishLineageSeedWorkflowRun(t, database, actorID, projectID, definitionVersionID, now.Add(time.Second))
	// This fixture models a pre-activation lineage. Migration-owned historical
	// rows are the only workflow manifests allowed to bypass a compiler CAS.
	groupKey := "legacy"

	rootID := uuid.New()
	rootRunID := runID
	rootOrdinal := 0
	rootGroupKey := groupKey
	rootBundle := core.WorkbenchBundle{
		ID: rootID.String(), ProjectID: projectID.String(), RootBuildManifestID: rootID.String(),
		WorkflowRunID: stringPointer(runID.String()), ManifestGroupKey: &rootGroupKey,
		CreatedBy: actorID.String(), CreatedAt: now, ManifestHash: publishLineagePGHash("root-manifest"),
	}
	rootPayload, err := json.Marshal(rootBundle)
	if err != nil {
		t.Fatal(err)
	}
	rootContent := contents.put(projectID.String(), "application_build_manifest", rootID.String(), rootPayload)
	root := storage.ApplicationBuildManifestModel{
		ID: rootID, ProjectID: projectID, WorkflowRunID: &rootRunID, RootManifestID: rootID,
		RootOrdinal: &rootOrdinal, ManifestGroupKey: &rootGroupKey,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: rootContent.ID,
		ContentHash: rootContent.ContentHash, ManifestHash: rootBundle.ManifestHash,
		Status: "invalidated", CreatedBy: actorID, CreatedAt: now,
	}
	if err := database.Create(&root).Error; err != nil {
		t.Fatal(err)
	}

	derivedID := uuid.New()
	derivedFromID := rootID
	derivedRunID := runID
	derivedOrdinal := 0
	derivedGroupKey := groupKey
	derivedBundle := rootBundle
	derivedBundle.ID = derivedID.String()
	derivedBundle.DerivedFromBuildManifestID = stringPointer(rootID.String())
	derivedBundle.CreatedAt = now.Add(time.Second)
	derivedBundle.ManifestHash = publishLineagePGHash("derived-manifest")
	derivedPayload, err := json.Marshal(derivedBundle)
	if err != nil {
		t.Fatal(err)
	}
	derivedContent := contents.put(projectID.String(), "application_build_manifest", derivedID.String(), derivedPayload)
	derived := storage.ApplicationBuildManifestModel{
		ID: derivedID, ProjectID: projectID, WorkflowRunID: &derivedRunID,
		RootManifestID: rootID, DerivedFromID: &derivedFromID,
		RootOrdinal: &derivedOrdinal, ManifestGroupKey: &derivedGroupKey,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: derivedContent.ID,
		ContentHash: derivedContent.ContentHash, ManifestHash: derivedBundle.ManifestHash,
		Status: "consumed", CreatedBy: actorID, CreatedAt: now.Add(time.Second),
	}
	if err := database.Create(&derived).Error; err != nil {
		t.Fatal(err)
	}

	proposalID := uuid.New()
	appliedAt := now.Add(2 * time.Second)
	if err := database.Create(&storage.ImplementationProposalModel{
		ID: proposalID, ProjectID: projectID, BuildManifestID: derivedID,
		Status: "applied", Version: 2, ContentStore: "mongo", ContentRef: uuid.NewString(),
		ContentHash: publishLineagePGHash("proposal-content"), PayloadHash: publishLineagePGHash("proposal-payload"),
		OperationCount: 1, AcceptedCount: 1, CreatedBy: actorID, CreatedAt: now.Add(time.Second),
		AppliedBy: &actorID, AppliedAt: &appliedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}

	workspaceArtifactID := uuid.New()
	workspaceRevisionID := uuid.New()
	workspacePayload := json.RawMessage(`{"name":"Published lineage","files":[{"path":"index.html","content":"<html>derived</html>","language":"html"}]}`)
	workspaceContent := contents.put(projectID.String(), "artifact_revision", workspaceRevisionID.String(), workspacePayload)
	workspace := storage.ArtifactModel{
		ID: workspaceArtifactID, ProjectID: projectID, Kind: "workspace", ArtifactKey: "WORKSPACE-LINEAGE-PG",
		Title: "Workspace", Lifecycle: "active", Version: 1, CreatedBy: actorID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&workspace).Error; err != nil {
		t.Fatal(err)
	}
	approvedAt := now.Add(3 * time.Second)
	workspaceRevision := storage.ArtifactRevisionModel{
		ID: workspaceRevisionID, ArtifactID: workspaceArtifactID, RevisionNumber: 1,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: workspaceContent.ID,
		ContentHash: workspaceContent.ContentHash, ByteSize: workspaceContent.ByteSize,
		WorkflowStatus: "approved", ChangeSource: "ai_proposal", ChangeSummary: "Apply derived proposal",
		ImplementationProposalID: &proposalID, CreatedBy: actorID, CreatedAt: approvedAt, ApprovedAt: &approvedAt,
	}
	if err := database.Create(&workspaceRevision).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", workspaceArtifactID).Updates(map[string]any{
		"latest_revision_id": workspaceRevisionID, "latest_approved_revision_id": workspaceRevisionID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	workspaceRef := core.VersionRef{
		ArtifactID: workspaceArtifactID.String(), RevisionID: workspaceRevisionID.String(),
		ContentHash: workspaceContent.ContentHash,
	}
	access := publishLineagePGAccess{}
	loader, err := NewRevisionLoader(database, contents, access)
	if err != nil {
		t.Fatal(err)
	}
	for label, selectorID := range map[string]string{"root": rootID.String(), "derived": derivedID.String()} {
		resolved, err := loader.ResolveWorkspaceManifestLineage(
			ctx, projectID.String(), actorID.String(), workspaceRef, selectorID, core.ActionEdit,
		)
		if err != nil || resolved != derivedID.String() {
			t.Fatalf("%s selector resolved producer = %q, err=%v, want derived %s", label, resolved, err, derivedID)
		}
	}

	buildFile := []byte("<html>derived</html>")
	buildArtifact := BuildArtifact{
		ID: uuid.NewString(), WorkspaceRevision: workspaceRef, EntryPath: "index.html",
		Files:     []BuildArtifactFile{{Path: "index.html", ContentBase64: base64.StdEncoding.EncodeToString(buildFile)}},
		FileCount: 1, TotalBytes: int64(len(buildFile)),
	}
	buildArtifact.BuildHash, err = hashBuildFiles(buildArtifact.Files)
	if err != nil {
		t.Fatal(err)
	}
	qualityRunID := uuid.NewString()
	buildReference := BuildArtifactReference{
		ID: buildArtifact.ID, ContentRef: uuid.NewString(), ContentHash: publishLineagePGHash("build-content"),
		BuildHash: buildArtifact.BuildHash, EntryPath: buildArtifact.EntryPath,
		FileCount: buildArtifact.FileCount, TotalBytes: buildArtifact.TotalBytes,
	}
	quality := publishLineagePGQuality{
		report: QualityReport{
			ID: qualityRunID, ProjectID: projectID.String(), Passed: true,
			WorkspaceRevision: workspaceRef, BuildArtifact: &buildReference,
		},
		artifact: buildArtifact,
	}
	qualityRunUUID := uuid.MustParse(qualityRunID)
	completedAt := now.Add(4 * time.Second)
	if err := database.Create(&qualityRunModel{
		ID: qualityRunUUID, ProjectID: projectID, WorkflowRunID: &runID,
		WorkspaceArtifactID: workspaceArtifactID, WorkspaceRevisionID: workspaceRevisionID,
		WorkspaceContentHash: workspaceRef.ContentHash, Status: "passed", Score: 100,
		RunnerVersion: "lineage-pg", SandboxKind: "test", Version: 1,
		CreatedBy: actorID, StartedAt: now, CompletedAt: &completedAt, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	publishService, err := NewPublishService(
		database, access, loader, quality, publishLineagePGProvider{}, publishLineagePGEnvironment{},
	)
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := publishService.Publish(
		ctx, projectID.String(), actorID.String(), "", PublishInput{
			Environment: EnvironmentPreview, WorkspaceRevision: &workspaceRef,
			BuildManifestID: rootID.String(), QualityRunID: qualityRunID,
		},
	)
	if err != nil {
		t.Fatalf("publish through root selector: %v (cause: %v)", err, errors.Unwrap(err))
	}
	var storedVersion deploymentVersionModel
	if err := database.Where("deployment_id = ?", deployment.ID).Order("number DESC").Take(&storedVersion).Error; err != nil {
		t.Fatal(err)
	}
	if storedVersion.BuildManifestID == nil || *storedVersion.BuildManifestID != derivedID {
		t.Fatalf("deployment stored build manifest = %v, want exact derived producer %s", storedVersion.BuildManifestID, derivedID)
	}

	otherRootID := publishLineageSeedRootManifest(
		t, database, actorID, projectID, runID, uuid.NewString(), 0, now.Add(4*time.Second), "other-root",
	)
	if _, err := loader.ResolveWorkspaceManifestLineage(
		ctx, projectID.String(), actorID.String(), workspaceRef, otherRootID.String(), core.ActionEdit,
	); !publishLineageIsConflict(err) {
		t.Fatalf("other root selector was accepted: %v", err)
	}
	otherProjectRootID := publishLineageSeedRootManifest(
		t, database, actorID, otherProjectID, uuid.Nil, "", 0, now.Add(5*time.Second), "other-project-root",
	)
	if _, err := loader.ResolveWorkspaceManifestLineage(
		ctx, projectID.String(), actorID.String(), workspaceRef, otherProjectRootID.String(), core.ActionEdit,
	); !publishLineageIsConflict(err) {
		t.Fatalf("other project selector was accepted: %v", err)
	}

	if err := database.Model(&storage.ApplicationBuildManifestModel{}).Where("id = ?", derivedID).
		Update("status", "frozen").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := loader.ResolveWorkspaceManifestLineage(
		ctx, projectID.String(), actorID.String(), workspaceRef, rootID.String(), core.ActionEdit,
	); !publishLineageIsConflict(err) {
		t.Fatalf("non-consumed producer was accepted: %v", err)
	}
	if err := database.Model(&storage.ApplicationBuildManifestModel{}).Where("id = ?", derivedID).
		Update("status", "consumed").Error; err != nil {
		t.Fatal(err)
	}

	foreignRunID := otherRunID
	foreignGroupKey := "legacy"
	foreignOrdinal := 0
	foreignChildID := uuid.New()
	foreignParentID := derivedID
	if err := database.Create(&storage.ApplicationBuildManifestModel{
		ID: foreignChildID, ProjectID: projectID, WorkflowRunID: &foreignRunID,
		RootManifestID: rootID, DerivedFromID: &foreignParentID,
		RootOrdinal: &foreignOrdinal, ManifestGroupKey: &foreignGroupKey,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: uuid.NewString(),
		ContentHash: publishLineagePGHash("foreign-child-content"), ManifestHash: publishLineagePGHash("foreign-child-manifest"),
		Status: "frozen", CreatedBy: actorID, CreatedAt: now.Add(6 * time.Second),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := loader.ResolveWorkspaceManifestLineage(
		ctx, projectID.String(), actorID.String(), workspaceRef, foreignChildID.String(), core.ActionEdit,
	); !publishLineageIsConflict(err) {
		t.Fatalf("selector from another workflow run was accepted: %v", err)
	}
	if _, err := loader.ResolveWorkspaceManifestLineage(
		ctx, projectID.String(), actorID.String(), workspaceRef, rootID.String(), core.ActionEdit,
	); !publishLineageIsConflict(err) {
		t.Fatalf("non-leaf producer was accepted: %v", err)
	}
}

func publishLineageSeedUserProject(
	t *testing.T,
	database *gorm.DB,
	userID uuid.UUID,
	projectID uuid.UUID,
	label string,
) {
	t.Helper()
	now := time.Now().UTC()
	if err := database.Create(&storage.UserModel{
		ID: userID, Email: label + "-" + uuid.NewString() + "@example.com", DisplayName: label,
		PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	publishLineageSeedProject(t, database, userID, projectID, label)
}

func publishLineageSeedProject(
	t *testing.T,
	database *gorm.DB,
	userID uuid.UUID,
	projectID uuid.UUID,
	label string,
) {
	t.Helper()
	now := time.Now().UTC()
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: label, Lifecycle: "active", Version: 1,
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func publishLineageSeedWorkflowDefinition(
	t *testing.T,
	database *gorm.DB,
	actorID uuid.UUID,
	projectID uuid.UUID,
	now time.Time,
) uuid.UUID {
	t.Helper()
	definitionID := uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionModel{
		ID: definitionID, ProjectID: &projectID, WorkflowKey: "publish-lineage-" + uuid.NewString(),
		Title: "Publish lineage", Lifecycle: "active", CreatedBy: actorID,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	versionID := uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionVersionModel{
		ID: versionID, DefinitionID: definitionID, Version: 1, SchemaVersion: 1,
		Content: json.RawMessage(`{"nodes":[],"edges":[]}`), ContentHash: publishLineagePGHash("definition"),
		ExecutionProfileVersion: runtime.LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: runtime.LegacyWorkflowExecutionProfileHash,
		ValidationReport: json.RawMessage(`{}`), Published: true, CreatedBy: actorID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return versionID
}

func publishLineageSeedWorkflowRun(
	t *testing.T,
	database *gorm.DB,
	actorID uuid.UUID,
	projectID uuid.UUID,
	definitionVersionID uuid.UUID,
	now time.Time,
) uuid.UUID {
	t.Helper()
	runID := uuid.New()
	if err := database.Create(&storage.WorkflowRunModel{
		ID: runID, ProjectID: projectID, DefinitionVersionID: definitionVersionID, Status: "running",
		ExecutionProfileVersion: runtime.LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: runtime.LegacyWorkflowExecutionProfileHash,
		Scope: json.RawMessage(`{}`), Context: json.RawMessage(`{}`), StartedBy: actorID,
		StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return runID
}

func publishLineageSeedRootManifest(
	t *testing.T,
	database *gorm.DB,
	actorID uuid.UUID,
	projectID uuid.UUID,
	runID uuid.UUID,
	groupKey string,
	ordinal int,
	createdAt time.Time,
	seed string,
) uuid.UUID {
	t.Helper()
	id := uuid.New()
	model := storage.ApplicationBuildManifestModel{
		ID: id, ProjectID: projectID, RootManifestID: id,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: uuid.NewString(),
		ContentHash: publishLineagePGHash(seed + "-content"), ManifestHash: publishLineagePGHash(seed + "-manifest"),
		Status: "frozen", CreatedBy: actorID, CreatedAt: createdAt,
	}
	if runID != uuid.Nil {
		modelRunID := runID
		modelGroupKey := groupKey
		modelOrdinal := ordinal
		model.WorkflowRunID = &modelRunID
		model.ManifestGroupKey = &modelGroupKey
		model.RootOrdinal = &modelOrdinal
		if groupKey != "legacy" {
			deliverySliceID := uuid.NewString()
			model.DeliverySliceID = &deliverySliceID
		}
	}
	if err := database.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func publishLineageIsConflict(err error) bool {
	if err == nil {
		return false
	}
	typed, ok := AsError(err)
	return ok && typed.Code == CodeConflict
}

func publishLineagePGHash(seed string) string {
	return publishLineagePGHashBytes([]byte("publish-lineage:" + seed))
}

func publishLineagePGHashBytes(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func publishLineagePostgresDatabase(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	base, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := "publish_lineage_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	database, err := gorm.Open(
		gormpostgres.Open(publishLineagePostgresDSNWithSearchPath(t, dsn, schema)),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		_ = base.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error
		t.Fatal(err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(context.Background(), sqlDatabase); err != nil {
		_ = sqlDatabase.Close()
		_ = base.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error
		t.Fatal(err)
	}
	cleanup := func() {
		_ = sqlDatabase.Close()
		_ = base.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error
		if baseSQL, sqlErr := base.DB(); sqlErr == nil {
			_ = baseSQL.Close()
		}
	}
	return database, cleanup
}

func publishLineagePostgresDSNWithSearchPath(t *testing.T, dsn string, schema string) string {
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

var _ content.Store = (*publishLineagePGContentStore)(nil)
