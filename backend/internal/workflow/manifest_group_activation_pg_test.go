package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

func TestManifestCompilerRootsPublishOnlyAtExactLeaseCASPostgres(t *testing.T) {
	database, cleanup := multiBundleCompletionPostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	userID, projectID := uuid.New(), uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: userID, Email: "manifest-activation-" + uuid.NewString() + "@example.com",
		DisplayName: "Manifest Activation", PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Manifest activation", Lifecycle: "active", Version: 1,
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectMemberModel{
		ProjectID: projectID, UserID: userID, Role: "owner", JoinedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	definitionID, definitionVersionID := uuid.New(), uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionModel{
		ID: definitionID, ProjectID: &projectID, WorkflowKey: "manifest-activation",
		Title: "Manifest activation", Lifecycle: "active", CreatedBy: userID,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.WorkflowDefinitionVersionModel{
		ID: definitionVersionID, DefinitionID: definitionID, Version: 1, SchemaVersion: 1,
		Content: json.RawMessage(`{"nodes":[],"edges":[]}`), ContentHash: platformHash("manifest-activation-definition"),
		ExecutionProfileVersion: LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: LegacyWorkflowExecutionProfileHash,
		ValidationReport: json.RawMessage(`{}`), Published: true, CreatedBy: userID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	runID, compilerID := uuid.New(), uuid.New()
	compilerKey := "compile-manifest"
	initialContext := NewRunContext()
	initialContext.Nodes[compilerKey] = NodeMetadata{DefinitionNodeID: compilerKey}
	initialContextJSON, err := json.Marshal(initialContext)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.WorkflowRunModel{
		ID: runID, ProjectID: projectID, DefinitionVersionID: definitionVersionID,
		ExecutionProfileVersion: LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: LegacyWorkflowExecutionProfileHash,
		Status: "running", Scope: json.RawMessage(`{}`), Context: initialContextJSON,
		StartedBy: userID, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	worker := "compiler-worker"
	leaseExpiresAt := now.Add(time.Minute)
	if err := database.Create(&storage.WorkflowNodeRunModel{
		ID: compilerID, RunID: runID, NodeKey: compilerKey, NodeType: string(domain.NodeManifestCompiler),
		Status: "running", Attempt: 1, LeaseOwner: &worker, LeaseExpiresAt: &leaseExpiresAt,
		AvailableAt: now, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	contents := &fakeContentStore{}
	rootIDs := []uuid.UUID{uuid.New(), uuid.New()}
	sliceIDs := []string{uuid.NewString(), uuid.NewString()}
	seedManifestActivationRoot(t, ctx, database, contents, projectID, userID, runID, compilerID, rootIDs[0], 0, sliceIDs[0], now)

	manifest := BuildManifest{
		SchemaVersion: 1, ProjectID: projectID.String(), RunID: runID.String(), ManifestGroupKey: compilerID.String(),
		SliceIDs: sliceIDs, BundleIDs: []string{rootIDs[0].String(), rootIDs[1].String()},
		Sources: []domain.ArtifactRef{{
			ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: platformHash("manifest-activation-source"),
		}},
		Constraints: json.RawMessage(`{}`), CreatedAt: now,
	}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	output, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	completedContext := NewRunContext()
	completedContext.Nodes[compilerKey] = NodeMetadata{DefinitionNodeID: compilerKey, Output: output}
	completedAt := now.Add(time.Second)
	mutation := RunMutation{
		RunID: runID.String(), ExpectedCursor: 0, Status: RunRunning, Context: completedContext,
		Nodes: []NodeMutation{{
			Node: NodeRecord{
				ID: compilerID.String(), RunID: runID.String(), Key: compilerKey, DefinitionNodeID: compilerKey,
				Type: domain.NodeManifestCompiler, Status: NodeCompleted, Attempt: 1,
				AvailableAt: now, StartedAt: &now, CompletedAt: &completedAt, CreatedAt: now, UpdatedAt: completedAt,
			},
			ExpectedStatus: NodeRunning, ExpectedOwner: worker,
		}},
		Events: []Event{{
			ID: uuid.NewString(), RunID: runID.String(), Type: "node.completed", NodeKey: compilerKey,
			Payload: json.RawMessage(`{"attempt":1}`), CreatedAt: completedAt,
		}},
		UpdatedAt: completedAt,
	}
	store, err := NewGORMStore(database, InlineContentStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	access, err := core.NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	workbench, err := core.NewWorkbenchService(database, contents, access)
	if err != nil {
		t.Fatal(err)
	}

	// The first root exists physically, but a failed second root keeps the
	// compiler transaction from publishing any member of the group.
	if err := store.Commit(ctx, mutation); err == nil {
		t.Fatal("compiler commit accepted a partial two-root group")
	}
	assertManifestActivationBlocked(t, ctx, workbench, rootIDs[0], userID)

	seedManifestActivationRoot(t, ctx, database, contents, projectID, userID, runID, compilerID, rootIDs[1], 1, sliceIDs[1], now.Add(time.Millisecond))
	otherWorker := "replacement-worker"
	if err := database.Model(&storage.WorkflowNodeRunModel{}).Where("id = ?", compilerID).
		Update("lease_owner", otherWorker).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, mutation); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("compiler commit after lease loss = %v, want ErrLeaseLost", err)
	}
	for _, rootID := range rootIDs {
		assertManifestActivationBlocked(t, ctx, workbench, rootID, userID)
	}

	if err := database.Model(&storage.WorkflowNodeRunModel{}).Where("id = ?", compilerID).Updates(map[string]any{
		"lease_owner": worker, "lease_expires_at": leaseExpiresAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	swapped := manifest
	swapped.SliceIDs = []string{manifest.SliceIDs[1], manifest.SliceIDs[0]}
	swapped.Hash = ""
	if err := swapped.Freeze(); err != nil {
		t.Fatal(err)
	}
	swappedOutput, err := json.Marshal(swapped)
	if err != nil {
		t.Fatal(err)
	}
	swappedContext := completedContext
	swappedContext.Nodes = map[string]NodeMetadata{
		compilerKey: {DefinitionNodeID: compilerKey, Output: swappedOutput},
	}
	swappedMutation := mutation
	swappedMutation.Context = swappedContext
	if err := store.Commit(ctx, swappedMutation); err == nil {
		t.Fatal("compiler CAS accepted SliceIDs swapped across the exact persisted roots")
	}
	for _, rootID := range rootIDs {
		assertManifestActivationBlocked(t, ctx, workbench, rootID, userID)
	}
	if err := store.Commit(ctx, mutation); err != nil {
		t.Fatalf("commit exact two-root compiler output: %v", err)
	}
	for _, rootID := range rootIDs {
		if _, err := workbench.GetBundle(ctx, rootID.String(), userID.String()); err != nil {
			t.Fatalf("committed root %s is not discoverable: %v", rootID, err)
		}
	}
	if _, err := workbench.GetBundleForGeneration(ctx, rootIDs[0].String(), userID.String()); err != nil {
		t.Fatalf("first committed root is not generatable: %v", err)
	}
}

func seedManifestActivationRoot(
	t *testing.T,
	ctx context.Context,
	database *gorm.DB,
	contents *fakeContentStore,
	projectID uuid.UUID,
	userID uuid.UUID,
	runID uuid.UUID,
	compilerID uuid.UUID,
	rootID uuid.UUID,
	ordinal int,
	sliceID string,
	createdAt time.Time,
) {
	t.Helper()
	runIDString, groupIDString := runID.String(), compilerID.String()
	bundle := core.WorkbenchBundle{
		ID: rootID.String(), ProjectID: projectID.String(), RootBuildManifestID: rootID.String(),
		WorkflowRunID: &runIDString, ManifestGroupKey: &groupIDString, DeliverySliceID: &sliceID,
		CreatedBy: userID.String(), CreatedAt: createdAt,
	}
	hashPayload := bundle
	hashPayload.ManifestHash = ""
	manifestHash, err := domain.CanonicalHash(hashPayload)
	if err != nil {
		t.Fatal(err)
	}
	bundle.ManifestHash = manifestHash
	payload, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := contents.PutPending(ctx, projectID.String(), "application_build_manifest", rootID.String(), 1, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := contents.Finalize(ctx, reference.ID); err != nil {
		t.Fatal(err)
	}
	modelRunID, modelGroupID, modelOrdinal := runID, compilerID.String(), ordinal
	model := storage.ApplicationBuildManifestModel{
		ID: rootID, ProjectID: projectID, WorkflowRunID: &modelRunID, RootManifestID: rootID,
		RootOrdinal: &modelOrdinal, ManifestGroupKey: &modelGroupID, DeliverySliceID: &sliceID,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: reference.ID, ContentHash: reference.ContentHash,
		ManifestHash: manifestHash, Status: "frozen", CreatedBy: userID, CreatedAt: createdAt,
	}
	if err := database.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
}

func assertManifestActivationBlocked(
	t *testing.T,
	ctx context.Context,
	workbench *core.WorkbenchService,
	rootID uuid.UUID,
	userID uuid.UUID,
) {
	t.Helper()
	if _, err := workbench.GetBundle(ctx, rootID.String(), userID.String()); !errors.Is(err, core.ErrBlockingGate) {
		t.Fatalf("partial root %s discovery error = %v, want blocking gate", rootID, err)
	}
	if _, err := workbench.GetBundleForGeneration(ctx, rootID.String(), userID.String()); !errors.Is(err, core.ErrBlockingGate) {
		t.Fatalf("partial root %s generation error = %v, want blocking gate", rootID, err)
	}
}
