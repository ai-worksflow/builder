package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	deliveryruntime "github.com/worksflow/builder/backend/internal/delivery"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	workflowruntime "github.com/worksflow/builder/backend/internal/workflow"
)

func TestDocumentGraphAndDeliveryExposeManifestOnlyAfterCompilerCASPostgres(t *testing.T) {
	database, cleanup := collaborationPostgresDatabase(t)
	defer cleanup()
	fixture := seedCollaborationFixture(t, database, false)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	prototype := seedApprovedCollaborationArtifact(
		t, database, fixture.contents, fixture.projectID, fixture.ownerID,
		"prototype", "ACTIVATION-PROTOTYPE", "Activation prototype",
		json.RawMessage(`{"title":"Activation prototype"}`), nil,
	)
	definitionID, definitionVersionID := uuid.New(), uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionModel{
		ID: definitionID, ProjectID: &fixture.projectID, WorkflowKey: "manifest-activation-graph",
		Title: "Manifest activation graph", Lifecycle: "active", CreatedBy: fixture.ownerID,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.WorkflowDefinitionVersionModel{
		ID: definitionVersionID, DefinitionID: definitionID, Version: 1, SchemaVersion: 1,
		Content: json.RawMessage(`{"nodes":[],"edges":[]}`), ContentHash: collaborationHash("activation-definition"),
		ExecutionProfileVersion: workflowruntime.LegacyWorkflowExecutionProfileVersion,
		ExecutionProfileHash:    workflowruntime.LegacyWorkflowExecutionProfileHash,
		ValidationReport:        json.RawMessage(`{}`), Published: true, CreatedBy: fixture.ownerID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	runID, compilerID, rootID := uuid.New(), uuid.New(), uuid.New()
	compilerKey, worker := "compile-application", "activation-worker"
	initialContext := workflowruntime.NewRunContext()
	initialContext.Nodes[compilerKey] = workflowruntime.NodeMetadata{DefinitionNodeID: compilerKey}
	initialContextJSON, err := json.Marshal(initialContext)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.WorkflowRunModel{
		ID: runID, ProjectID: fixture.projectID, DefinitionVersionID: definitionVersionID,
		ExecutionProfileVersion: workflowruntime.LegacyWorkflowExecutionProfileVersion,
		ExecutionProfileHash:    workflowruntime.LegacyWorkflowExecutionProfileHash,
		Status:                  "running", Scope: json.RawMessage(`{}`), Context: initialContextJSON,
		StartedBy: fixture.ownerID, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	leaseExpiresAt := now.Add(time.Minute)
	if err := database.Create(&storage.WorkflowNodeRunModel{
		ID: compilerID, RunID: runID, NodeKey: compilerKey,
		NodeType: string(domain.NodeManifestCompiler), Status: "running", Attempt: 1,
		LeaseOwner: &worker, LeaseExpiresAt: &leaseExpiresAt,
		AvailableAt: now, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	runIDString, groupIDString, sliceID := runID.String(), compilerID.String(), uuid.NewString()
	bundle := core.WorkbenchBundle{
		ID: rootID.String(), ProjectID: fixture.projectID.String(), RootBuildManifestID: rootID.String(),
		WorkflowRunID: &runIDString, ManifestGroupKey: &groupIDString, DeliverySliceID: &sliceID,
		PrototypeRevision: prototype, CreatedBy: fixture.ownerID.String(), CreatedAt: now,
	}
	bundle.ManifestHash, err = domain.CanonicalHash(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundlePayload, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundleContent := fixture.contents.addFinalized(
		fixture.projectID.String(), "application_build_manifest", rootID.String(), bundlePayload,
	)
	ordinal := 0
	if err := database.Create(&storage.ApplicationBuildManifestModel{
		ID: rootID, ProjectID: fixture.projectID, RootManifestID: rootID,
		WorkflowRunID: &runID, ManifestGroupKey: &groupIDString, RootOrdinal: &ordinal, DeliverySliceID: &sliceID,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: bundleContent.ID,
		ContentHash: bundleContent.ContentHash, ManifestHash: bundle.ManifestHash,
		Status: "frozen", CreatedBy: fixture.ownerID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// More than the old projection limit of newer, non-activated rows must not
	// starve an older usable manifest. The graph query prefilters by compiler
	// completion before applying its activated-result limit, then still runs the
	// shared exact activation validator on every candidate.
	standaloneID := uuid.New()
	if err := database.Create(&storage.ApplicationBuildManifestModel{
		ID: standaloneID, ProjectID: fixture.projectID, RootManifestID: standaloneID,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: "standalone-content",
		ContentHash: collaborationHash("standalone-content"), ManifestHash: collaborationHash("standalone-manifest"),
		Status: "frozen", CreatedBy: fixture.ownerID, CreatedAt: now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
	const newerInactiveCount = 1001
	inactiveNodes := make([]storage.WorkflowNodeRunModel, 0, newerInactiveCount)
	inactiveManifests := make([]storage.ApplicationBuildManifestModel, 0, newerInactiveCount)
	for index := 0; index < newerInactiveCount; index++ {
		inactiveNodeID, inactiveRootID := uuid.New(), uuid.New()
		inactiveGroup := inactiveNodeID.String()
		inactiveSlice := uuid.NewString()
		inactiveOrdinal := 0
		createdAt := now.Add(time.Duration(index+1) * time.Microsecond)
		inactiveNodes = append(inactiveNodes, storage.WorkflowNodeRunModel{
			ID: inactiveNodeID, RunID: runID, NodeKey: "inactive-compiler-" + inactiveNodeID.String(),
			NodeType: string(domain.NodeManifestCompiler), Status: "running", Attempt: 1,
			AvailableAt: createdAt, CreatedAt: createdAt, UpdatedAt: createdAt,
		})
		inactiveManifests = append(inactiveManifests, storage.ApplicationBuildManifestModel{
			ID: inactiveRootID, ProjectID: fixture.projectID, RootManifestID: inactiveRootID,
			WorkflowRunID: &runID, ManifestGroupKey: &inactiveGroup, RootOrdinal: &inactiveOrdinal, DeliverySliceID: &inactiveSlice,
			SchemaVersion: 1, ContentStore: "mongo", ContentRef: "inactive-" + inactiveRootID.String(),
			ContentHash:  collaborationHash("inactive-content-" + inactiveRootID.String()),
			ManifestHash: collaborationHash("inactive-manifest-" + inactiveRootID.String()),
			Status:       "frozen", CreatedBy: fixture.ownerID, CreatedAt: createdAt,
		})
	}
	if err := database.CreateInBatches(&inactiveNodes, 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.CreateInBatches(&inactiveManifests, 100).Error; err != nil {
		t.Fatal(err)
	}

	loader, err := deliveryruntime.NewRevisionLoader(database, fixture.contents, fixture.service.access)
	if err != nil {
		t.Fatal(err)
	}
	exporter, err := deliveryruntime.NewExportService(database, loader)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := fixture.service.GetDocumentGraph(ctx, fixture.projectID.String(), fixture.viewerID.String())
	if err != nil {
		t.Fatal(err)
	}
	if graphHasNode(graph, "workbenchVersion", rootID.String()) {
		t.Fatal("document graph exposed a root before the ManifestCompiler CAS")
	}
	if !graphHasNode(graph, "workbenchVersion", standaloneID.String()) {
		t.Fatal("more than 1000 newer inactive manifests starved an older activated graph node")
	}
	if _, err := loader.LoadBuildManifest(
		ctx, fixture.projectID.String(), fixture.viewerID.String(), rootID.String(), core.ActionView,
	); !errors.Is(err, core.ErrBlockingGate) {
		t.Fatalf("direct partial-root read error = %v, want blocking gate", err)
	}
	if _, err := exporter.Export(ctx, fixture.projectID.String(), fixture.viewerID.String(), deliveryruntime.ExportInput{
		Kind: deliveryruntime.ExportPrototype, BuildManifestID: rootID.String(), RedactSensitive: true,
	}); !errors.Is(err, core.ErrBlockingGate) {
		t.Fatalf("partial-root export error = %v, want blocking gate", err)
	}

	activation := workflowruntime.BuildManifest{
		SchemaVersion: 1, ProjectID: fixture.projectID.String(), RunID: runID.String(),
		ManifestGroupKey: compilerID.String(), SliceIDs: []string{sliceID}, BundleIDs: []string{rootID.String()},
		Sources: []domain.ArtifactRef{{
			ArtifactID: prototype.ArtifactID, RevisionID: prototype.RevisionID, ContentHash: prototype.ContentHash,
		}},
		Constraints: json.RawMessage(`{}`), CreatedAt: now,
	}
	if err := activation.Freeze(); err != nil {
		t.Fatal(err)
	}
	activationOutput, err := json.Marshal(activation)
	if err != nil {
		t.Fatal(err)
	}
	completedContext := workflowruntime.NewRunContext()
	completedContext.Nodes[compilerKey] = workflowruntime.NodeMetadata{
		DefinitionNodeID: compilerKey, Output: activationOutput,
	}
	completedAt := now.Add(time.Second)
	mutation := workflowruntime.RunMutation{
		RunID: runID.String(), ExpectedCursor: 0, Status: workflowruntime.RunRunning,
		Context: completedContext,
		Nodes: []workflowruntime.NodeMutation{{
			Node: workflowruntime.NodeRecord{
				ID: compilerID.String(), RunID: runID.String(), Key: compilerKey, DefinitionNodeID: compilerKey,
				Type: domain.NodeManifestCompiler, Status: workflowruntime.NodeCompleted, Attempt: 1,
				AvailableAt: now, StartedAt: &now, CompletedAt: &completedAt, CreatedAt: now, UpdatedAt: completedAt,
			},
			ExpectedStatus: workflowruntime.NodeRunning, ExpectedOwner: worker,
		}},
		Events: []workflowruntime.Event{{
			ID: uuid.NewString(), RunID: runID.String(), Type: "node.completed", NodeKey: compilerKey,
			Payload: json.RawMessage(`{"attempt":1}`), CreatedAt: completedAt,
		}},
		UpdatedAt: completedAt,
	}
	store, err := workflowruntime.NewGORMStore(database, workflowruntime.InlineContentStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, mutation); err != nil {
		t.Fatalf("commit exact ManifestCompiler CAS: %v", err)
	}

	graph, err = fixture.service.GetDocumentGraph(ctx, fixture.projectID.String(), fixture.viewerID.String())
	if err != nil {
		t.Fatal(err)
	}
	if !graphHasNode(graph, "workbenchVersion", rootID.String()) {
		t.Fatal("document graph omitted the root after the exact ManifestCompiler CAS")
	}
	if _, err := loader.LoadBuildManifest(
		ctx, fixture.projectID.String(), fixture.viewerID.String(), rootID.String(), core.ActionView,
	); err != nil {
		t.Fatalf("activated direct root read: %v", err)
	}
	archive, err := exporter.Export(ctx, fixture.projectID.String(), fixture.viewerID.String(), deliveryruntime.ExportInput{
		Kind: deliveryruntime.ExportPrototype, BuildManifestID: rootID.String(), RedactSensitive: true,
	})
	if err != nil || archive.FileCount == 0 || len(archive.Data) == 0 {
		t.Fatalf("activated root export failed: archive=%+v err=%v", archive, err)
	}

	tamperedActivation := activation
	tamperedActivation.SliceIDs = []string{uuid.NewString()}
	tamperedActivation.Hash = ""
	if err := tamperedActivation.Freeze(); err != nil {
		t.Fatal(err)
	}
	tamperedOutput, err := json.Marshal(tamperedActivation)
	if err != nil {
		t.Fatal(err)
	}
	tamperedContext := workflowruntime.NewRunContext()
	tamperedContext.Nodes[compilerKey] = workflowruntime.NodeMetadata{
		DefinitionNodeID: compilerKey, Output: tamperedOutput,
	}
	tamperedContextJSON, err := json.Marshal(tamperedContext)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.WorkflowRunModel{}).Where("id = ?", runID).
		Update("context", tamperedContextJSON).Error; err != nil {
		t.Fatal(err)
	}
	graph, err = fixture.service.GetDocumentGraph(ctx, fixture.projectID.String(), fixture.viewerID.String())
	if err != nil {
		t.Fatal(err)
	}
	if graphHasNode(graph, "workbenchVersion", rootID.String()) {
		t.Fatal("document graph exposed a root after its persisted delivery slice diverged from compiler output")
	}
	if _, err := loader.LoadBuildManifest(
		ctx, fixture.projectID.String(), fixture.viewerID.String(), rootID.String(), core.ActionView,
	); !errors.Is(err, core.ErrBlockingGate) {
		t.Fatalf("slice-tampered direct read error = %v, want blocking gate", err)
	}
	if _, err := exporter.Export(ctx, fixture.projectID.String(), fixture.viewerID.String(), deliveryruntime.ExportInput{
		Kind: deliveryruntime.ExportPrototype, BuildManifestID: rootID.String(), RedactSensitive: true,
	}); !errors.Is(err, core.ErrBlockingGate) {
		t.Fatalf("slice-tampered export error = %v, want blocking gate", err)
	}
}
