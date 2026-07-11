package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"github.com/worksflow/builder/backend/migrations"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestWorkbenchCompletionRejectsDerivedBundleFromAnotherWorkflowRunPostgres(t *testing.T) {
	database, cleanup := multiBundleCompletionPostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	userID := uuid.New()
	projectID := uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: userID, Email: "completion-" + uuid.NewString() + "@example.com",
		DisplayName: "Completion Owner", PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Completion run isolation", Lifecycle: "active", Version: 1,
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	definitionID := uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionModel{
		ID: definitionID, ProjectID: &projectID, WorkflowKey: "completion-run-isolation",
		Title: "Completion run isolation", Lifecycle: "active", CreatedBy: userID,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	definitionVersionID := uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionVersionModel{
		ID: definitionVersionID, DefinitionID: definitionID, Version: 1, SchemaVersion: 1,
		Content: json.RawMessage(`{"nodes":[],"edges":[]}`), ContentHash: completionTestHash("definition"),
		ExecutionProfileVersion: LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: LegacyWorkflowExecutionProfileHash,
		ValidationReport: json.RawMessage(`{}`), Published: true, CreatedBy: userID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	runID := uuid.New()
	foreignRunID := uuid.New()
	manifestGroupKey := uuid.NewString()
	for _, id := range []uuid.UUID{runID, foreignRunID} {
		if err := database.Create(&storage.WorkflowRunModel{
			ID: id, ProjectID: projectID, DefinitionVersionID: definitionVersionID, Status: "running",
			ExecutionProfileVersion: LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: LegacyWorkflowExecutionProfileHash,
			Scope: json.RawMessage(`{}`), Context: json.RawMessage(`{}`), StartedBy: userID,
			StartedAt: &now, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	rootID := uuid.New()
	rootRunID := runID
	rootOrdinal := 0
	deliverySliceID := uuid.NewString()
	root := storage.ApplicationBuildManifestModel{
		ID: rootID, ProjectID: projectID, WorkflowRunID: &rootRunID, RootManifestID: rootID,
		RootOrdinal: &rootOrdinal, ManifestGroupKey: &manifestGroupKey, DeliverySliceID: &deliverySliceID,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: "completion-root",
		ContentHash: completionTestHash("root-content"), ManifestHash: completionTestHash("root-manifest"),
		Status: "frozen", CreatedBy: userID, CreatedAt: now,
	}
	if err := database.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	derivedID := uuid.New()
	foreignDerivedRunID := foreignRunID
	derivedFromID := rootID
	derived := storage.ApplicationBuildManifestModel{
		ID: derivedID, ProjectID: projectID, WorkflowRunID: &foreignDerivedRunID,
		RootManifestID: rootID, DerivedFromID: &derivedFromID,
		RootOrdinal: &rootOrdinal, ManifestGroupKey: &manifestGroupKey, DeliverySliceID: &deliverySliceID,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: "completion-derived",
		ContentHash: completionTestHash("derived-content"), ManifestHash: completionTestHash("derived-manifest"),
		Status: "consumed", CreatedBy: userID, CreatedAt: now.Add(time.Second),
	}
	if err := database.Create(&derived).Error; err != nil {
		t.Fatal(err)
	}

	proposalID := uuid.New()
	appliedAt := now.Add(2 * time.Second)
	proposal := storage.ImplementationProposalModel{
		ID: proposalID, ProjectID: projectID, BuildManifestID: derivedID,
		Status: "applied", Version: 2, ContentStore: "mongo", ContentRef: "foreign-proposal",
		ContentHash: completionTestHash("foreign-proposal-content"), PayloadHash: completionTestHash("foreign-proposal-payload"),
		OperationCount: 1, AcceptedCount: 1, CreatedBy: userID, CreatedAt: now.Add(time.Second),
		AppliedBy: &userID, AppliedAt: &appliedAt,
	}
	if err := database.Create(&proposal).Error; err != nil {
		t.Fatal(err)
	}

	workspaceArtifactID := uuid.New()
	workspaceRevisionID := uuid.New()
	workspaceHash := completionTestHash("workspace-from-foreign-run")
	workspaceArtifact := storage.ArtifactModel{
		ID: workspaceArtifactID, ProjectID: projectID, Kind: "workspace", ArtifactKey: "WORKSPACE-RUN-ISOLATION",
		Title: "Application Workspace", Lifecycle: "active", Version: 1,
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&workspaceArtifact).Error; err != nil {
		t.Fatal(err)
	}
	workspaceRevision := storage.ArtifactRevisionModel{
		ID: workspaceRevisionID, ArtifactID: workspaceArtifactID, RevisionNumber: 1,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: "foreign-workspace",
		ContentHash: workspaceHash, WorkflowStatus: "approved", ChangeSource: "ai_proposal",
		ChangeSummary: "Applied substituted proposal", ImplementationProposalID: &proposalID,
		CreatedBy: userID, CreatedAt: appliedAt, ApprovedAt: &appliedAt,
	}
	if err := database.Create(&workspaceRevision).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", workspaceArtifactID).Updates(map[string]any{
		"latest_revision_id": workspaceRevisionID, "latest_approved_revision_id": workspaceRevisionID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	manifest := BuildManifest{
		SchemaVersion: 1, ProjectID: projectID.String(), RunID: runID.String(),
		ManifestGroupKey: manifestGroupKey,
		SliceIDs:         []string{deliverySliceID}, BundleIDs: []string{rootID.String()},
		Sources: []domain.ArtifactRef{{
			ArtifactID: workspaceArtifactID.String(), RevisionID: workspaceRevisionID.String(), ContentHash: workspaceHash,
		}},
		Constraints: json.RawMessage(`{}`), CreatedAt: now,
	}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	manifestPayload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "manifest", FromPort: "default", ToPort: "default",
		Source: domain.NodeOutputReference{
			RunID: runID.String(), NodeKey: "manifest", DefinitionNodeID: "manifest",
		},
		Output: manifestPayload, Value: manifestPayload,
	}})
	if err != nil {
		t.Fatal(err)
	}
	execution := Execution{
		Run:    RunRecord{ID: runID.String(), ProjectID: projectID.String()},
		Inputs: inputs,
	}
	output, err := json.Marshal(map[string]any{
		"implementationProposalIds": []string{proposalID.String()},
		"workspaceRevision": domain.ArtifactRef{
			ArtifactID: workspaceArtifactID.String(), RevisionID: workspaceRevisionID.String(), ContentHash: workspaceHash,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := (CoreWorkbenchCompletionValidator{Database: database}).ValidateCompletion(ctx, execution, output); err == nil {
		t.Fatal("completion accepted a same-project derived bundle and proposal from another workflow run")
	} else if !strings.Contains(strings.ToLower(err.Error()), "workflow run") {
		t.Fatalf("completion failed for the wrong reason, want workflow run isolation: %v", err)
	}
}

func TestWorkbenchCompletionAcceptsOrderedSameRunDerivedLineagePostgres(t *testing.T) {
	database, cleanup := multiBundleCompletionPostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	userID, projectID := uuid.New(), uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: userID, Email: "completion-positive-" + uuid.NewString() + "@example.com",
		DisplayName: "Completion Owner", PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Completion positive", Lifecycle: "active", Version: 1,
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	definitionID, definitionVersionID := uuid.New(), uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionModel{
		ID: definitionID, ProjectID: &projectID, WorkflowKey: "completion-positive",
		Title: "Completion positive", Lifecycle: "active", CreatedBy: userID,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.WorkflowDefinitionVersionModel{
		ID: definitionVersionID, DefinitionID: definitionID, Version: 1, SchemaVersion: 1,
		Content: json.RawMessage(`{"nodes":[],"edges":[]}`), ContentHash: completionTestHash("positive-definition"),
		ExecutionProfileVersion: LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: LegacyWorkflowExecutionProfileHash,
		ValidationReport: json.RawMessage(`{}`), Published: true, CreatedBy: userID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	runID := uuid.New()
	if err := database.Create(&storage.WorkflowRunModel{
		ID: runID, ProjectID: projectID, DefinitionVersionID: definitionVersionID, Status: "running",
		ExecutionProfileVersion: LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: LegacyWorkflowExecutionProfileHash,
		Scope: json.RawMessage(`{}`), Context: json.RawMessage(`{}`), StartedBy: userID,
		StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	groupKey := uuid.NewString()
	roots := []uuid.UUID{uuid.New(), uuid.New()}
	deliverySliceIDs := []string{uuid.NewString(), uuid.NewString()}
	for ordinal, rootID := range roots {
		rootOrdinal := ordinal
		status := "consumed"
		if ordinal == 1 {
			status = "invalidated"
		}
		modelRunID := runID
		modelGroupKey := groupKey
		if err := database.Create(&storage.ApplicationBuildManifestModel{
			ID: rootID, ProjectID: projectID, WorkflowRunID: &modelRunID, RootManifestID: rootID,
			RootOrdinal: &rootOrdinal, ManifestGroupKey: &modelGroupKey, DeliverySliceID: &deliverySliceIDs[ordinal],
			SchemaVersion: 1, ContentStore: "mongo", ContentRef: "positive-root-" + rootID.String(),
			ContentHash:  completionTestHash("root-content-" + rootID.String()),
			ManifestHash: completionTestHash("root-manifest-" + rootID.String()),
			Status:       status, CreatedBy: userID, CreatedAt: now.Add(time.Duration(ordinal) * time.Second),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	derivedID, derivedFromID, derivedOrdinal := uuid.New(), roots[1], 1
	derivedRunID, derivedGroupKey := runID, groupKey
	if err := database.Create(&storage.ApplicationBuildManifestModel{
		ID: derivedID, ProjectID: projectID, WorkflowRunID: &derivedRunID,
		RootManifestID: roots[1], DerivedFromID: &derivedFromID,
		RootOrdinal: &derivedOrdinal, ManifestGroupKey: &derivedGroupKey, DeliverySliceID: &deliverySliceIDs[1],
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: "positive-derived",
		ContentHash: completionTestHash("derived-content"), ManifestHash: completionTestHash("derived-manifest"),
		Status: "consumed", CreatedBy: userID, CreatedAt: now.Add(2 * time.Second),
	}).Error; err != nil {
		t.Fatal(err)
	}
	proposalIDs := []uuid.UUID{uuid.New(), uuid.New()}
	producerManifests := []uuid.UUID{roots[0], derivedID}
	for index, proposalID := range proposalIDs {
		appliedAt := now.Add(time.Duration(3+index) * time.Second)
		if err := database.Create(&storage.ImplementationProposalModel{
			ID: proposalID, ProjectID: projectID, BuildManifestID: producerManifests[index],
			Status: "applied", Version: 2, ContentStore: "mongo", ContentRef: "positive-proposal-" + proposalID.String(),
			ContentHash:    completionTestHash("proposal-content-" + proposalID.String()),
			PayloadHash:    completionTestHash("proposal-payload-" + proposalID.String()),
			OperationCount: 1, AcceptedCount: 1, CreatedBy: userID,
			CreatedAt: now.Add(time.Duration(index) * time.Second), AppliedBy: &userID, AppliedAt: &appliedAt,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	workspaceArtifactID := uuid.New()
	if err := database.Create(&storage.ArtifactModel{
		ID: workspaceArtifactID, ProjectID: projectID, Kind: "workspace", ArtifactKey: "WORKSPACE-POSITIVE",
		Title: "Workspace", Lifecycle: "active", Version: 2, CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	workspaceRevisionIDs := []uuid.UUID{uuid.New(), uuid.New()}
	workspaceHashes := []string{completionTestHash("workspace-1"), completionTestHash("workspace-2")}
	for index, revisionID := range workspaceRevisionIDs {
		var parentID *uuid.UUID
		if index > 0 {
			parent := workspaceRevisionIDs[index-1]
			parentID = &parent
		}
		proposalID := proposalIDs[index]
		approvedAt := now.Add(time.Duration(5+index) * time.Second)
		if err := database.Create(&storage.ArtifactRevisionModel{
			ID: revisionID, ArtifactID: workspaceArtifactID, RevisionNumber: uint64(index + 1), ParentRevisionID: parentID,
			SchemaVersion: 1, ContentStore: "mongo", ContentRef: "positive-workspace-" + revisionID.String(),
			ContentHash: workspaceHashes[index], WorkflowStatus: "approved", ChangeSource: "ai_proposal",
			ChangeSummary: "Apply ordered proposal", ImplementationProposalID: &proposalID,
			CreatedBy: userID, CreatedAt: approvedAt, ApprovedAt: &approvedAt,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", workspaceArtifactID).Updates(map[string]any{
		"latest_revision_id": workspaceRevisionIDs[1], "latest_approved_revision_id": workspaceRevisionIDs[1],
	}).Error; err != nil {
		t.Fatal(err)
	}
	manifest := BuildManifest{
		SchemaVersion: 1, ProjectID: projectID.String(), RunID: runID.String(), ManifestGroupKey: groupKey,
		SliceIDs: deliverySliceIDs, BundleIDs: []string{roots[0].String(), roots[1].String()},
		Sources: []domain.ArtifactRef{{
			ArtifactID: workspaceArtifactID.String(), RevisionID: workspaceRevisionIDs[1].String(), ContentHash: workspaceHashes[1],
		}}, Constraints: json.RawMessage(`{}`), CreatedAt: now,
	}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	manifestPayload, _ := json.Marshal(manifest)
	inputs, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "manifest", FromPort: "default", ToPort: "default",
		Source: domain.NodeOutputReference{RunID: runID.String(), NodeKey: "manifest", DefinitionNodeID: "manifest"},
		Output: manifestPayload, Value: manifestPayload,
	}})
	if err != nil {
		t.Fatal(err)
	}
	execution := Execution{Run: RunRecord{ID: runID.String(), ProjectID: projectID.String()}, Inputs: inputs}
	workspaceRef := domain.ArtifactRef{
		ArtifactID: workspaceArtifactID.String(), RevisionID: workspaceRevisionIDs[1].String(), ContentHash: workspaceHashes[1],
	}
	output, _ := json.Marshal(map[string]any{
		"implementationProposalIds": []string{proposalIDs[0].String(), proposalIDs[1].String()},
		"workspaceRevision":         workspaceRef,
	})
	got, err := (CoreWorkbenchCompletionValidator{Database: database}).ValidateCompletion(ctx, execution, output)
	if err != nil || got != workspaceRevisionIDs[1].String() {
		t.Fatalf("valid ordered same-run derived completion rejected: got=%q err=%v", got, err)
	}
	swapped, _ := json.Marshal(map[string]any{
		"implementationProposalIds": []string{proposalIDs[1].String(), proposalIDs[0].String()},
		"workspaceRevision":         workspaceRef,
	})
	if _, err := (CoreWorkbenchCompletionValidator{Database: database}).ValidateCompletion(ctx, execution, swapped); err == nil {
		t.Fatal("completion accepted implementation proposals outside frozen root order")
	}
}

func TestQualitySelectsFinalWorkspaceProducerAcrossManifestGroupsPostgres(t *testing.T) {
	database, cleanup := multiBundleCompletionPostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	actorID, projectID := uuid.New(), uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: actorID, Email: "quality-assembly-" + uuid.NewString() + "@example.com",
		DisplayName: "Quality Assembly Owner", PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Quality assembly", Lifecycle: "active", Version: 1,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	definitionID, definitionVersionID := uuid.New(), uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionModel{
		ID: definitionID, ProjectID: &projectID, WorkflowKey: "quality-assembly-" + uuid.NewString(),
		Title: "Quality assembly", Lifecycle: "active", CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.WorkflowDefinitionVersionModel{
		ID: definitionVersionID, DefinitionID: definitionID, Version: 1, SchemaVersion: 1,
		Content: json.RawMessage(`{"nodes":[],"edges":[]}`), ContentHash: completionTestHash("quality-assembly-definition"),
		ExecutionProfileVersion: LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: LegacyWorkflowExecutionProfileHash,
		ValidationReport: json.RawMessage(`{}`), Published: true, CreatedBy: actorID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	runID := uuid.New()
	if err := database.Create(&storage.WorkflowRunModel{
		ID: runID, ProjectID: projectID, DefinitionVersionID: definitionVersionID, Status: "running",
		ExecutionProfileVersion: LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: LegacyWorkflowExecutionProfileHash,
		Scope: json.RawMessage(`{}`), Context: json.RawMessage(`{}`), StartedBy: actorID,
		StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	groupIDs := []string{uuid.NewString(), uuid.NewString()}
	rootIDs := []uuid.UUID{uuid.New(), uuid.New()}
	deliverySliceIDs := []string{uuid.NewString(), uuid.NewString()}
	proposalIDs := []uuid.UUID{uuid.New(), uuid.New()}
	for index := range rootIDs {
		ordinal := 0
		modelRunID := runID
		group := groupIDs[index]
		if err := database.Create(&storage.ApplicationBuildManifestModel{
			ID: rootIDs[index], ProjectID: projectID, WorkflowRunID: &modelRunID,
			RootManifestID: rootIDs[index], RootOrdinal: &ordinal, ManifestGroupKey: &group, DeliverySliceID: &deliverySliceIDs[index],
			SchemaVersion: 1, ContentStore: "mongo", ContentRef: "quality-root-" + rootIDs[index].String(),
			ContentHash:  completionTestHash("quality-root-content-" + rootIDs[index].String()),
			ManifestHash: completionTestHash("quality-root-manifest-" + rootIDs[index].String()),
			Status:       "consumed", CreatedBy: actorID, CreatedAt: now.Add(time.Duration(index) * time.Second),
		}).Error; err != nil {
			t.Fatal(err)
		}
		appliedAt := now.Add(time.Duration(index+2) * time.Second)
		if err := database.Create(&storage.ImplementationProposalModel{
			ID: proposalIDs[index], ProjectID: projectID, BuildManifestID: rootIDs[index],
			Status: "applied", Version: 2, ContentStore: "mongo", ContentRef: "quality-proposal-" + proposalIDs[index].String(),
			ContentHash:    completionTestHash("quality-proposal-content-" + proposalIDs[index].String()),
			PayloadHash:    completionTestHash("quality-proposal-payload-" + proposalIDs[index].String()),
			OperationCount: 1, AcceptedCount: 1, CreatedBy: actorID, CreatedAt: now,
			AppliedBy: &actorID, AppliedAt: &appliedAt,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	workspaceArtifactID := uuid.New()
	workspaceRevisionIDs := []uuid.UUID{uuid.New(), uuid.New()}
	workspaceHashes := []string{completionTestHash("quality-workspace-a"), completionTestHash("quality-workspace-b")}
	if err := database.Create(&storage.ArtifactModel{
		ID: workspaceArtifactID, ProjectID: projectID, Kind: "workspace", ArtifactKey: "WORKSPACE-QUALITY-ASSEMBLY",
		Title: "Application Workspace", Lifecycle: "active", Version: 2,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for index, revisionID := range workspaceRevisionIDs {
		var parent *uuid.UUID
		status := "superseded"
		if index > 0 {
			value := workspaceRevisionIDs[index-1]
			parent = &value
			status = "approved"
		}
		approvedAt := now.Add(time.Duration(index+4) * time.Second)
		proposalID := proposalIDs[index]
		if err := database.Create(&storage.ArtifactRevisionModel{
			ID: revisionID, ArtifactID: workspaceArtifactID, RevisionNumber: uint64(index + 1), ParentRevisionID: parent,
			SchemaVersion: 1, ContentStore: "mongo", ContentRef: "quality-workspace-" + revisionID.String(),
			ContentHash: workspaceHashes[index], WorkflowStatus: status, ChangeSource: "ai_proposal",
			ChangeSummary: "Apply compiler group", ImplementationProposalID: &proposalID,
			CreatedBy: actorID, CreatedAt: approvedAt, ApprovedAt: &approvedAt,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", workspaceArtifactID).Updates(map[string]any{
		"latest_revision_id": workspaceRevisionIDs[1], "latest_approved_revision_id": workspaceRevisionIDs[1],
	}).Error; err != nil {
		t.Fatal(err)
	}

	manifests := make([]BuildManifest, 2)
	for index := range manifests {
		manifests[index] = BuildManifest{
			SchemaVersion: 1, ProjectID: projectID.String(), RunID: runID.String(), ManifestGroupKey: groupIDs[index],
			SliceIDs: []string{deliverySliceIDs[index]}, BundleIDs: []string{rootIDs[index].String()},
			Sources: []domain.ArtifactRef{{
				ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: completionTestHash("quality-source-" + string(rune('a'+index))),
			}}, Constraints: json.RawMessage(`{}`), CreatedAt: now.Add(time.Duration(index) * time.Second),
		}
		if err := manifests[index].Freeze(); err != nil {
			t.Fatal(err)
		}
	}

	run := &RunRecord{
		ID: runID.String(), ProjectID: projectID.String(), Nodes: map[string]*NodeRecord{},
		Context: RunContext{Values: map[string]json.RawMessage{}, Nodes: map[string]NodeMetadata{}, Slices: map[string]SliceContext{}},
	}
	directBindings := make([]domain.NodeInputBinding, 0, 2)
	for index, key := range []string{"workbench-a", "workbench-b"} {
		compilerKey := "compiler-" + key
		manifestPayload, _ := json.Marshal(manifests[index])
		manifestInputs, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
			EdgeID: compilerKey, FromPort: "default", ToPort: "default",
			Source: domain.NodeOutputReference{RunID: run.ID, NodeKey: compilerKey, DefinitionNodeID: compilerKey},
			Value:  manifestPayload, Output: manifestPayload,
		}})
		if err != nil {
			t.Fatal(err)
		}
		storedInputs, _ := json.Marshal(manifestInputs)
		run.Nodes[compilerKey] = &NodeRecord{ID: uuid.NewString(), RunID: run.ID, Key: compilerKey, DefinitionNodeID: compilerKey, Type: domain.NodeManifestCompiler, Status: NodeCompleted}
		run.Context.Nodes[compilerKey] = NodeMetadata{DefinitionNodeID: compilerKey}
		run.Nodes[key] = &NodeRecord{ID: uuid.NewString(), RunID: run.ID, Key: key, DefinitionNodeID: key, Type: domain.NodeWorkbenchBuild, Status: NodeCompleted}
		run.Context.Nodes[key] = NodeMetadata{DefinitionNodeID: key, Input: storedInputs}
		workspaceOutput, _ := json.Marshal(map[string]any{"workspaceRevision": domain.ArtifactRef{
			ArtifactID: workspaceArtifactID.String(), RevisionID: workspaceRevisionIDs[index].String(), ContentHash: workspaceHashes[index],
		}})
		directBindings = append(directBindings, domain.NodeInputBinding{
			EdgeID: "quality-" + key, FromPort: "default", ToPort: "default",
			Source: domain.NodeOutputReference{RunID: run.ID, NodeKey: key, DefinitionNodeID: key},
			Value:  workspaceOutput, Output: workspaceOutput,
		})
	}
	qualityKey := "quality"
	run.Nodes[qualityKey] = &NodeRecord{ID: uuid.NewString(), RunID: run.ID, Key: qualityKey, DefinitionNodeID: qualityKey, Type: domain.NodeQualityGate, Status: NodeRunning}
	run.Context.Nodes[qualityKey] = NodeMetadata{DefinitionNodeID: qualityKey, ExecutionActor: &ActorProvenance{
		ActorID: actorID.String(), Role: "editor", Action: "edit", Source: ActorSourceAuthenticatedCommand, AuthorizedAt: now,
	}}
	inputs, err := domain.NewNodeInputEnvelope(directBindings)
	if err != nil {
		t.Fatal(err)
	}
	finalWorkspace := domain.ArtifactRef{
		ArtifactID: workspaceArtifactID.String(), RevisionID: workspaceRevisionIDs[1].String(), ContentHash: workspaceHashes[1],
	}
	execution := Execution{
		Run: *run, Node: *run.Nodes[qualityKey], Inputs: inputs,
		Definition: domain.NodeDefinition{ID: qualityKey, Name: "Quality", Type: domain.NodeQualityGate,
			QualityGate: &domain.QualityGateNodeConfig{Blocking: true}},
	}
	runner := QualityGateRunner{
		Access: actorTestAccess{roles: map[string]core.Role{actorID.String(): core.RoleEditor}},
		Evaluator: QualityEvaluatorFunc(func(context.Context, Execution) (QualityResult, error) {
			return QualityResult{Passed: true, QualityRunID: uuid.NewString(), WorkspaceRevision: &finalWorkspace}, nil
		}),
		ManifestResolver: CoreQualityManifestResolver{Database: database},
	}
	result, err := runner.Run(ctx, execution)
	if err != nil {
		t.Fatal(err)
	}
	var quality QualityResult
	if err := json.Unmarshal(result.Output, &quality); err != nil {
		t.Fatal(err)
	}
	if quality.BuildManifest == nil || quality.BuildManifest.Hash != manifests[1].Hash || quality.BuildManifest.ManifestGroupKey != groupIDs[1] {
		t.Fatalf("quality selected the wrong compiler group: %+v", quality.BuildManifest)
	}

	unrelated := manifests[0]
	unrelated.ManifestGroupKey = uuid.NewString()
	unrelated.Hash = ""
	if err := unrelated.Freeze(); err != nil {
		t.Fatal(err)
	}
	if _, err := (CoreQualityManifestResolver{Database: database}).Resolve(
		ctx, execution, finalWorkspace, []BuildManifest{manifests[0], unrelated},
	); err == nil {
		t.Fatal("quality accepted inputs without the final workspace producer group")
	}
}

func completionTestHash(seed string) string {
	digest := sha256.Sum256([]byte("completion:" + seed))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func multiBundleCompletionPostgresDatabase(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	base, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := "completion_run_isolation_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	database, err := gorm.Open(
		gormpostgres.Open(completionPostgresDSNWithSearchPath(t, dsn, schema)),
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

func completionPostgresDSNWithSearchPath(t *testing.T, dsn string, schema string) string {
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
