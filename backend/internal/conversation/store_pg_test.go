package conversation

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
	platformdomain "github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"github.com/worksflow/builder/backend/migrations"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func TestConversationWorkbenchTargetUsesIndependentGovernanceManifestPostgres(t *testing.T) {
	database, cleanup := conversationPostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID, projectID := uuid.New(), uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: userID, Email: "conversation-" + uuid.NewString() + "@example.com", DisplayName: "Conversation Owner",
		PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Conversation workbench manifest isolation", Description: "",
		Lifecycle: "active", Version: 1, CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	sourceArtifactID, sourceRevisionID := uuid.New(), uuid.New()
	sourceHash := conversationTestHash("source")
	if err := database.Create(&storage.ArtifactModel{
		ID: sourceArtifactID, ProjectID: projectID, Kind: "product_requirements", ArtifactKey: "CONVERSATION-SOURCE",
		Title: "Conversation source", Lifecycle: "active", Version: 1, CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ArtifactRevisionModel{
		ID: sourceRevisionID, ArtifactID: sourceArtifactID, RevisionNumber: 1, SchemaVersion: 1,
		ContentStore: "mongo", ContentRef: "conversation-source", ContentHash: sourceHash, ByteSize: 1,
		WorkflowStatus: "approved", ChangeSource: "human", ChangeSummary: "Reviewed requirement",
		CreatedBy: userID, CreatedAt: now, ApprovedAt: &now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	sourceRef := platformdomain.ArtifactRef{
		ArtifactID: sourceArtifactID.String(), RevisionID: sourceRevisionID.String(), ContentHash: sourceHash,
	}

	definitionID, oldVersionID, currentVersionID := uuid.New(), uuid.New(), uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionModel{
		ID: definitionID, ProjectID: &projectID, WorkflowKey: "conversation-workbench",
		Title: "Conversation workbench", Description: "", Lifecycle: "active", CreatedBy: userID,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for index, versionID := range []uuid.UUID{oldVersionID, currentVersionID} {
		if err := database.Create(&storage.WorkflowDefinitionVersionModel{
			ID: versionID, DefinitionID: definitionID, Version: index + 1, SchemaVersion: 1,
			Content: json.RawMessage(`{"nodes":[],"edges":[]}`), ContentHash: conversationTestHash("definition-" + versionID.String()),
			ValidationReport: json.RawMessage(`{}`), Published: index == 1, CreatedBy: userID, CreatedAt: now.Add(time.Duration(index) * time.Second),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	selectionDefinitionID, selectionVersionID := uuid.New(), uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionModel{
		ID: selectionDefinitionID, ProjectID: &projectID, WorkflowKey: "blueprint-selection-app",
		Title: "Blueprint selection application", Description: "", Lifecycle: "active", CreatedBy: userID,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.WorkflowDefinitionVersionModel{
		ID: selectionVersionID, DefinitionID: selectionDefinitionID, Version: 1, SchemaVersion: 1,
		Content:          json.RawMessage(`{"nodes":[{"id":"pages","type":"fan_out","fanOut":{"itemKind":"blueprint_selection_page"}}],"edges":[]}`),
		ContentHash:      conversationTestHash("selection-definition-" + selectionVersionID.String()),
		ValidationReport: json.RawMessage(`{}`), Published: true, CreatedBy: userID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	oldManifest := conversationInputManifest(t, uuid.New(), projectID, userID, sourceRef, "old-run-input", now)
	governanceManifest := conversationInputManifest(t, uuid.New(), projectID, userID, sourceRef, "new-conversation-governance", now.Add(time.Second))
	selectionManifest, err := platformdomain.NewInputManifest(
		uuid.NewString(), projectID.String(), core.BlueprintSelectionJobType, "", nil,
		[]platformdomain.ManifestSource{{Ref: sourceRef, Purpose: "frozen selection"}}, json.RawMessage(`{"selectionId":"selection-active"}`),
		"blueprint-selection/v1", userID.String(), now.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if oldManifest.ID == governanceManifest.ID || oldManifest.Hash == governanceManifest.Hash {
		t.Fatal("test fixture did not create distinct run and governance manifests")
	}
	for index, manifest := range []platformdomain.InputManifest{oldManifest, governanceManifest, selectionManifest} {
		if err := database.Create(&storage.InputManifestModel{
			ID: uuid.MustParse(manifest.ID), ProjectID: projectID, Kind: manifest.JobType, SchemaVersion: 1,
			ContentStore: "mongo", ContentRef: "conversation-manifest-" + manifest.ID,
			ContentHash: conversationTestHash("manifest-content-" + manifest.ID), ManifestHash: manifest.Hash,
			CreatedBy: userID, CreatedAt: now.Add(time.Duration(index) * time.Second),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	oldManifestID := uuid.MustParse(oldManifest.ID)
	runID, otherRunID := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{runID, otherRunID} {
		manifestID := oldManifestID
		if err := database.Create(&storage.WorkflowRunModel{
			ID: id, ProjectID: projectID, DefinitionVersionID: oldVersionID, Status: "running", InputManifestID: &manifestID,
			Scope: json.RawMessage(`{}`), Context: json.RawMessage(`{}`), StartedBy: userID, StartedAt: &now,
			CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	rootID, leafID, implementationProposalID := createConversationWorkbenchLineage(t, database, projectID, runID, userID, "primary", now)
	otherRootID, otherLeafID, otherProposalID := createConversationWorkbenchLineage(t, database, projectID, otherRunID, userID, "other", now)
	selectionRunID := uuid.New()
	selectionManifestID := uuid.MustParse(selectionManifest.ID)
	if err := database.Create(&storage.WorkflowRunModel{
		ID: selectionRunID, ProjectID: projectID, DefinitionVersionID: selectionVersionID, Status: "waiting_input", InputManifestID: &selectionManifestID,
		Scope: json.RawMessage(`{}`), Context: json.RawMessage(`{}`), StartedBy: userID, StartedAt: &now,
		CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second),
	}).Error; err != nil {
		t.Fatal(err)
	}
	selectionRootID, selectionLeafID, _ := createConversationWorkbenchLineage(t, database, projectID, selectionRunID, userID, "selection", now.Add(2*time.Second))

	conversationID, triggerMessageID := uuid.New(), uuid.New()
	if err := database.Create(&storage.ConversationModel{
		ID: conversationID, ProjectID: projectID, Title: "Continue existing application", Status: "active", Version: 1,
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ConversationMessageModel{
		ID: triggerMessageID, ConversationID: conversationID, Sequence: 1, Role: "user",
		Content: "Continue the reviewed application", CreatedBy: userID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	store, err := NewGORMStore(database)
	if err != nil {
		t.Fatal(err)
	}
	manifestIntent := ManifestIntent{Mode: "use_existing", InputManifest: governanceManifest.Ref(), Purpose: "govern conversation intent"}
	generationContext, err := store.IntentGenerationContext(
		ctx, projectID, conversationID, triggerMessageID, []uuid.UUID{currentVersionID}, []platformdomain.ArtifactRef{sourceRef}, manifestIntent, governanceManifest.JobType,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !containsConversationDefinitionVersion(generationContext.Definitions, currentVersionID) ||
		!containsConversationDefinitionVersion(generationContext.Definitions, oldVersionID) ||
		!containsConversationDefinitionVersion(generationContext.Definitions, selectionVersionID) {
		t.Fatalf("generation context did not include the start candidate and all active historical target definitions: %+v", generationContext.Definitions)
	}
	if !containsConversationWorkbenchTarget(generationContext.WorkbenchTargets, intentWorkbenchTargetContext{
		DefinitionVersionID: oldVersionID.String(), RunID: runID.String(), RootBundleID: rootID.String(), ActiveBundleID: leafID.String(),
	}) {
		t.Fatalf("active old-version run target was not exposed: %+v", generationContext.WorkbenchTargets)
	}
	if !containsConversationWorkbenchTarget(generationContext.WorkbenchTargets, intentWorkbenchTargetContext{
		DefinitionVersionID: selectionVersionID.String(), RunID: selectionRunID.String(), RootBundleID: selectionRootID.String(), ActiveBundleID: selectionLeafID.String(),
	}) {
		t.Fatalf("active Blueprint-selection run was coupled to the ordinary start candidates: %+v", generationContext.WorkbenchTargets)
	}

	proposalInput := CreateIntentProposalInput{
		TriggerMessageID: triggerMessageID.String(), AssistantContent: "Continue the exact active workbench target.",
		Kind: IntentWorkbenchInstruction, SuggestedDefinitionVersionID: oldVersionID.String(), Scope: json.RawMessage(`{"slice":"all"}`),
		SourceRefs: []platformdomain.ArtifactRef{sourceRef}, ManifestIntent: manifestIntent,
		WorkbenchInstruction: WorkbenchInstruction{
			Objective: "Continue the reviewed application", Constraints: []string{"Keep exact lineage"},
			ExpectedRunID: runID.String(), ExpectedBundleID: rootID.String(),
		},
	}
	unpublishedStart := proposalInput
	unpublishedStart.Kind = IntentStartWorkflow
	if _, _, err := store.CreateIntentProposal(
		ctx, projectID, conversationID, userID, unpublishedStart, ProposalProvenance{Origin: ProposalOriginSubmitted}, governanceManifest.JobType,
	); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unpublished historical version start error = %v", err)
	}
	incompatibleSelectionStart := proposalInput
	incompatibleSelectionStart.Kind = IntentStartWorkflow
	incompatibleSelectionStart.SuggestedDefinitionVersionID = selectionVersionID.String()
	incompatibleSelectionStart.WorkbenchInstruction.ExpectedRunID = ""
	incompatibleSelectionStart.WorkbenchInstruction.ExpectedBundleID = ""
	if _, _, err := store.CreateIntentProposal(
		ctx, projectID, conversationID, userID, incompatibleSelectionStart, ProposalProvenance{Origin: ProposalOriginSubmitted}, governanceManifest.JobType,
	); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("conversation manifest started Blueprint-selection definition: %v", err)
	}
	proposal, _, err := store.CreateIntentProposal(
		ctx, projectID, conversationID, userID, proposalInput, ProposalProvenance{Origin: ProposalOriginSubmitted}, governanceManifest.JobType,
	)
	if err != nil {
		t.Fatalf("M1 governance manifest was incorrectly coupled to run M0: %v", err)
	}
	accepted, command, err := store.DecideProposal(
		ctx, projectID, conversationID, uuid.MustParse(proposal.ID), userID, proposal.ETag,
		DecideProposalInput{Decision: DecisionAccept},
	)
	if err != nil || command == nil || accepted.Status != ProposalAccepted {
		t.Fatalf("accept workbench proposal: proposal=%+v command=%+v err=%v", accepted, command, err)
	}
	if command.Payload.ManifestIntent.InputManifest != governanceManifest.Ref() || command.Payload.DefinitionVersionID != oldVersionID.String() {
		t.Fatalf("accepted command lost exact governance/target identity: %+v", command.Payload)
	}
	validResult := WorkbenchExecutionResult{
		RunID: runID.String(), BundleID: leafID.String(), ImplementationProposalID: implementationProposalID.String(),
	}
	if err := store.ValidateWorkbenchResult(ctx, projectID, command.Payload, validResult); err != nil {
		t.Fatalf("valid M1-governed result for M0 run was rejected: %v", err)
	}
	for _, status := range []string{"stale", "applied", "partially_applied"} {
		if err := database.Model(&storage.ImplementationProposalModel{}).Where("id = ?", implementationProposalID).
			Updates(map[string]any{"status": status, "applied_by": nil, "applied_at": nil}).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.ValidateWorkbenchResult(ctx, projectID, command.Payload, validResult); !errors.Is(err, core.ErrConflict) {
			t.Fatalf("proposal status %q result error = %v", status, err)
		}
	}
	if err := database.Model(&storage.ImplementationProposalModel{}).Where("id = ?", implementationProposalID).
		Updates(map[string]any{"status": "ready", "applied_by": nil, "applied_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateWorkbenchResult(ctx, projectID, command.Payload, validResult); err != nil {
		t.Fatalf("ready unapplied proposal was rejected: %v", err)
	}
	if err := database.Model(&storage.ImplementationProposalModel{}).Where("id = ?", implementationProposalID).
		Updates(map[string]any{"status": "open", "applied_by": userID, "applied_at": now.Add(3 * time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateWorkbenchResult(ctx, projectID, command.Payload, validResult); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("proposal with applied identity result error = %v", err)
	}
	if err := database.Model(&storage.ImplementationProposalModel{}).Where("id = ?", implementationProposalID).
		Updates(map[string]any{"status": "open", "applied_by": nil, "applied_at": nil}).Error; err != nil {
		t.Fatal(err)
	}

	substitutedProposal := validResult
	substitutedProposal.ImplementationProposalID = otherProposalID.String()
	if err := store.ValidateWorkbenchResult(ctx, projectID, command.Payload, substitutedProposal); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("proposal substitution error = %v", err)
	}
	otherRunResult := WorkbenchExecutionResult{
		RunID: otherRunID.String(), BundleID: otherLeafID.String(), ImplementationProposalID: otherProposalID.String(),
	}
	if err := store.ValidateWorkbenchResult(ctx, projectID, command.Payload, otherRunResult); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("other-run result error = %v (other root %s)", err, otherRootID)
	}

	workspaceArtifactID, workspaceRevisionID := uuid.New(), uuid.New()
	if err := database.Create(&storage.ArtifactModel{
		ID: workspaceArtifactID, ProjectID: projectID, Kind: "workspace", ArtifactKey: "WORKSPACE-MAIN",
		Title: "Application workspace", Lifecycle: "active", Version: 1, CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ArtifactRevisionModel{
		ID: workspaceRevisionID, ArtifactID: workspaceArtifactID, RevisionNumber: 1, SchemaVersion: 1,
		ContentStore: "mongo", ContentRef: "conversation-workspace-v1", ContentHash: conversationTestHash("workspace-v1"),
		ByteSize: 1, WorkflowStatus: "approved", ChangeSource: "human", ChangeSummary: "Workspace v1",
		CreatedBy: userID, CreatedAt: now.Add(3 * time.Second), ApprovedAt: conversationTimePointer(now.Add(3 * time.Second)),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", workspaceArtifactID).
		Updates(map[string]any{"latest_revision_id": workspaceRevisionID, "latest_approved_revision_id": workspaceRevisionID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateWorkbenchResult(ctx, projectID, command.Payload, validResult); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("leaf without newly-created current workspace result error = %v", err)
	}
	if err := database.Model(&storage.ApplicationBuildManifestModel{}).Where("id = ?", leafID).
		Update("workspace_revision_id", workspaceRevisionID).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateWorkbenchResult(ctx, projectID, command.Payload, validResult); err != nil {
		t.Fatalf("leaf pinned to exact current workspace was rejected: %v", err)
	}
	workspaceRevision2ID := uuid.New()
	if err := database.Create(&storage.ArtifactRevisionModel{
		ID: workspaceRevision2ID, ArtifactID: workspaceArtifactID, RevisionNumber: 2, ParentRevisionID: &workspaceRevisionID,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: "conversation-workspace-v2",
		ContentHash: conversationTestHash("workspace-v2"), ByteSize: 1, WorkflowStatus: "approved", ChangeSource: "human",
		ChangeSummary: "Workspace advanced after generation", CreatedBy: userID, CreatedAt: now.Add(4 * time.Second),
		ApprovedAt: conversationTimePointer(now.Add(4 * time.Second)),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", workspaceRevisionID).
		Updates(map[string]any{"workflow_status": "superseded", "superseded_at": now.Add(4 * time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", workspaceArtifactID).
		Updates(map[string]any{"latest_revision_id": workspaceRevision2ID, "latest_approved_revision_id": workspaceRevision2ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateWorkbenchResult(ctx, projectID, command.Payload, validResult); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("workspace-advanced result error = %v", err)
	}

	newLeafID := uuid.New()
	derivedFromID := leafID
	group := "primary"
	ordinal := 0
	if err := database.Create(&storage.ApplicationBuildManifestModel{
		ID: newLeafID, ProjectID: projectID, WorkflowRunID: &runID, RootManifestID: rootID, DerivedFromID: &derivedFromID,
		WorkspaceRevisionID: &workspaceRevision2ID, RootOrdinal: &ordinal, ManifestGroupKey: &group, SchemaVersion: 1, ContentStore: "mongo",
		ContentRef: "conversation-primary-new-leaf", ContentHash: conversationTestHash("primary-new-leaf"),
		ManifestHash: conversationTestHash("primary-new-leaf-manifest"), Status: "frozen", CreatedBy: userID, CreatedAt: now.Add(3 * time.Second),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateWorkbenchResult(ctx, projectID, command.Payload, validResult); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("stale leaf result error = %v", err)
	}
}

func TestAtomicWorkbenchCommandCompletionPostgres(t *testing.T) {
	database, cleanup := conversationPostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	fixture := createAtomicWorkbenchFixture(t, database)
	completed, err := fixture.store.CompleteWorkbenchCommand(
		ctx, fixture.projectID, fixture.conversationID, fixture.commandID, fixture.actorID,
		fixture.etag, 2*time.Minute, fixture.result,
	)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != CommandExecuted || completed.Version != 3 ||
		!strings.Contains(string(completed.Result), fixture.implementationProposalID.String()) {
		t.Fatalf("unexpected atomic completion: %+v", completed)
	}

	cases := []struct {
		name   string
		mutate func(atomicWorkbenchFixture) error
	}{
		{
			name: "workspace advanced",
			mutate: func(fixture atomicWorkbenchFixture) error {
				now := time.Now().UTC()
				artifactID, revisionID := uuid.New(), uuid.New()
				if err := database.Create(&storage.ArtifactModel{
					ID: artifactID, ProjectID: fixture.projectID, Kind: "workspace", ArtifactKey: "WORKSPACE-MAIN",
					Title: "Concurrent workspace", Lifecycle: "active", Version: 1, CreatedBy: fixture.actorID,
					CreatedAt: now, UpdatedAt: now,
				}).Error; err != nil {
					return err
				}
				if err := database.Create(&storage.ArtifactRevisionModel{
					ID: revisionID, ArtifactID: artifactID, RevisionNumber: 1, SchemaVersion: 1,
					ContentStore: "mongo", ContentRef: "atomic-workspace", ContentHash: conversationTestHash("atomic-workspace-" + fixture.commandID.String()),
					ByteSize: 1, WorkflowStatus: "approved", ChangeSource: "human", ChangeSummary: "Concurrent advance",
					CreatedBy: fixture.actorID, CreatedAt: now, ApprovedAt: &now,
				}).Error; err != nil {
					return err
				}
				return database.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).
					Updates(map[string]any{"latest_revision_id": revisionID, "latest_approved_revision_id": revisionID}).Error
			},
		},
		{
			name: "proposal stale",
			mutate: func(fixture atomicWorkbenchFixture) error {
				return database.Model(&storage.ImplementationProposalModel{}).
					Where("id = ?", fixture.implementationProposalID).Update("status", "stale").Error
			},
		},
		{
			name: "proposal applied",
			mutate: func(fixture atomicWorkbenchFixture) error {
				now := time.Now().UTC()
				return database.Model(&storage.ImplementationProposalModel{}).
					Where("id = ?", fixture.implementationProposalID).
					Updates(map[string]any{"status": "applied", "applied_by": fixture.actorID, "applied_at": now}).Error
			},
		},
		{
			name: "leaf rebased",
			mutate: func(fixture atomicWorkbenchFixture) error {
				derivedID := uuid.New()
				derivedFromID := fixture.rootBundleID
				group := fixture.manifestGroup
				ordinal := 0
				now := time.Now().UTC()
				if err := database.Create(&storage.ApplicationBuildManifestModel{
					ID: derivedID, ProjectID: fixture.projectID, WorkflowRunID: &fixture.runID,
					RootManifestID: fixture.rootBundleID, DerivedFromID: &derivedFromID,
					RootOrdinal: &ordinal, ManifestGroupKey: &group, SchemaVersion: 1, ContentStore: "mongo",
					ContentRef: "atomic-rebased-" + derivedID.String(), ContentHash: conversationTestHash("atomic-rebased-" + derivedID.String()),
					ManifestHash: conversationTestHash("atomic-rebased-manifest-" + derivedID.String()), Status: "frozen",
					CreatedBy: fixture.actorID, CreatedAt: now,
				}).Error; err != nil {
					return err
				}
				reason := "concurrent_rebase"
				return database.Model(&storage.ApplicationBuildManifestModel{}).Where("id = ?", fixture.rootBundleID).
					Updates(map[string]any{"status": "invalidated", "invalidated_at": now, "invalidation_reason": reason}).Error
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := createAtomicWorkbenchFixture(t, database)
			blocker := database.Begin()
			if blocker.Error != nil {
				t.Fatal(blocker.Error)
			}
			defer blocker.Rollback()
			var locked storage.ConversationCommandModel
			if err := blocker.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", fixture.commandID).
				Take(&locked).Error; err != nil {
				t.Fatal(err)
			}
			type completion struct {
				command ConversationCommand
				err     error
			}
			completed := make(chan completion, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				command, err := fixture.store.CompleteWorkbenchCommand(
					ctx, fixture.projectID, fixture.conversationID, fixture.commandID, fixture.actorID,
					fixture.etag, 2*time.Minute, fixture.result,
				)
				completed <- completion{command: command, err: err}
			}()
			<-started
			if err := testCase.mutate(fixture); err != nil {
				_ = blocker.Rollback().Error
				t.Fatal(err)
			}
			if err := blocker.Commit().Error; err != nil {
				t.Fatal(err)
			}
			outcome := <-completed
			if !errors.Is(outcome.err, core.ErrConflict) || outcome.command.Status == CommandExecuted {
				t.Fatalf("completion crossed concurrent mutation: command=%+v err=%v", outcome.command, outcome.err)
			}
			var persisted storage.ConversationCommandModel
			if err := database.Where("id = ?", fixture.commandID).Take(&persisted).Error; err != nil {
				t.Fatal(err)
			}
			if persisted.Status != string(CommandPending) || persisted.Version != 1 || persisted.ExecutedAt != nil {
				t.Fatalf("failed atomic completion mutated command: %+v", persisted)
			}
		})
	}
}

type atomicWorkbenchFixture struct {
	store                    *GORMStore
	projectID                uuid.UUID
	conversationID           uuid.UUID
	commandID                uuid.UUID
	actorID                  uuid.UUID
	runID                    uuid.UUID
	rootBundleID             uuid.UUID
	implementationProposalID uuid.UUID
	manifestGroup            string
	etag                     string
	result                   WorkbenchExecutionResult
}

func createAtomicWorkbenchFixture(t *testing.T, database *gorm.DB) atomicWorkbenchFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	fixtureID := uuid.NewString()
	actorID, projectID := uuid.New(), uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: actorID, Email: "atomic-" + fixtureID + "@example.com", DisplayName: "Atomic Owner",
		PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Atomic workbench " + fixtureID, Description: "", Lifecycle: "active", Version: 1,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	definitionID, definitionVersionID := uuid.New(), uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionModel{
		ID: definitionID, ProjectID: &projectID, WorkflowKey: "atomic-" + fixtureID,
		Title: "Atomic workbench", Description: "", Lifecycle: "active", CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.WorkflowDefinitionVersionModel{
		ID: definitionVersionID, DefinitionID: definitionID, Version: 1, SchemaVersion: 1,
		Content: json.RawMessage(`{"nodes":[],"edges":[]}`), ContentHash: conversationTestHash("atomic-definition-" + fixtureID),
		ValidationReport: json.RawMessage(`{}`), Published: true, CreatedBy: actorID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	oldManifestID, governanceManifestID := uuid.New(), uuid.New()
	governanceHash := conversationTestHash("atomic-governance-" + fixtureID)
	for _, manifest := range []struct {
		id   uuid.UUID
		hash string
	}{
		{id: oldManifestID, hash: conversationTestHash("atomic-old-manifest-" + fixtureID)},
		{id: governanceManifestID, hash: governanceHash},
	} {
		if err := database.Create(&storage.InputManifestModel{
			ID: manifest.id, ProjectID: projectID, Kind: "conversation.workflow_intent", SchemaVersion: 1,
			ContentStore: "mongo", ContentRef: "atomic-manifest-" + manifest.id.String(),
			ContentHash: conversationTestHash("atomic-manifest-content-" + manifest.id.String()), ManifestHash: manifest.hash,
			CreatedBy: actorID, CreatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	runID := uuid.New()
	if err := database.Create(&storage.WorkflowRunModel{
		ID: runID, ProjectID: projectID, DefinitionVersionID: definitionVersionID, Status: "running",
		InputManifestID: &oldManifestID, Scope: json.RawMessage(`{}`), Context: json.RawMessage(`{}`),
		StartedBy: actorID, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	rootBundleID := uuid.New()
	manifestGroup := "atomic-" + fixtureID
	ordinal := 0
	if err := database.Create(&storage.ApplicationBuildManifestModel{
		ID: rootBundleID, ProjectID: projectID, WorkflowRunID: &runID, RootManifestID: rootBundleID,
		RootOrdinal: &ordinal, ManifestGroupKey: &manifestGroup, SchemaVersion: 1, ContentStore: "mongo",
		ContentRef: "atomic-root-" + fixtureID, ContentHash: conversationTestHash("atomic-root-" + fixtureID),
		ManifestHash: conversationTestHash("atomic-root-manifest-" + fixtureID), Status: "frozen",
		CreatedBy: actorID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	implementationProposalID := uuid.New()
	if err := database.Create(&storage.ImplementationProposalModel{
		ID: implementationProposalID, ProjectID: projectID, BuildManifestID: rootBundleID,
		Status: "open", Version: 1, ContentStore: "mongo", ContentRef: "atomic-proposal-" + fixtureID,
		ContentHash: conversationTestHash("atomic-proposal-content-" + fixtureID),
		PayloadHash: conversationTestHash("atomic-proposal-payload-" + fixtureID), OperationCount: 1,
		CreatedBy: actorID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	conversationID, triggerID, assistantID, reviewedProposalID, commandID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if err := database.Create(&storage.ConversationModel{
		ID: conversationID, ProjectID: projectID, Title: "Atomic receipt", Status: "active", Version: 1,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ConversationMessageModel{
		ID: triggerID, ConversationID: conversationID, Sequence: 1, Role: "user", Content: "Complete atomically",
		CreatedBy: actorID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	manifestIntent := ManifestIntent{
		Mode: "use_existing", InputManifest: platformdomain.ManifestRef{ID: governanceManifestID.String(), Hash: governanceHash},
		Purpose: "atomic workbench receipt",
	}
	instruction := WorkbenchInstruction{
		Objective: "Complete exact proposal", ExpectedRunID: runID.String(), ExpectedBundleID: rootBundleID.String(),
	}
	payload := CommandPayload{
		DefinitionVersionID: definitionVersionID.String(), Scope: json.RawMessage(`{}`),
		ManifestIntent: manifestIntent, Workbench: instruction,
	}
	encodedPayload, err := platformdomain.CanonicalJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	encodedSources := json.RawMessage(`[{"artifactId":"` + uuid.NewString() + `","revisionId":"` + uuid.NewString() + `","contentHash":"` + conversationTestHash("atomic-source-"+fixtureID) + `"}]`)
	encodedManifest, _ := platformdomain.CanonicalJSON(manifestIntent)
	encodedInstruction, _ := platformdomain.CanonicalJSON(instruction)
	err = database.Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&storage.WorkflowIntentProposalModel{
			ID: reviewedProposalID, ProjectID: projectID, ConversationID: conversationID, TriggerMessageID: triggerID,
			AssistantMessageID: assistantID, Kind: string(IntentWorkbenchInstruction), Status: string(ProposalAccepted), Version: 2,
			SuggestedDefinitionVersionID: definitionVersionID, Scope: json.RawMessage(`{}`), SourceRefs: encodedSources,
			ManifestIntent: encodedManifest, WorkbenchInstruction: encodedInstruction, Origin: string(ProposalOriginSubmitted),
			DecisionReason: "", ProposedBy: actorID, DecidedBy: &actorID, CreatedAt: now, DecidedAt: &now,
		}).Error; err != nil {
			return err
		}
		if err := transaction.Create(&storage.ConversationMessageModel{
			ID: assistantID, ConversationID: conversationID, Sequence: 2, Role: "assistant", Content: "Ready",
			ProposalID: &reviewedProposalID, CreatedBy: actorID, CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		return transaction.Create(&storage.ConversationCommandModel{
			ID: commandID, ProjectID: projectID, ConversationID: conversationID, ProposalID: reviewedProposalID,
			Kind: string(IntentWorkbenchInstruction), Status: string(CommandPending), Version: 1, Payload: encodedPayload,
			AcceptedBy: actorID, CreatedAt: now, UpdatedAt: now,
		}).Error
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewGORMStore(database)
	if err != nil {
		t.Fatal(err)
	}
	return atomicWorkbenchFixture{
		store: store, projectID: projectID, conversationID: conversationID, commandID: commandID, actorID: actorID,
		runID: runID, rootBundleID: rootBundleID, implementationProposalID: implementationProposalID,
		manifestGroup: manifestGroup, etag: CommandETag(commandID.String(), 1),
		result: WorkbenchExecutionResult{
			RunID: runID.String(), BundleID: rootBundleID.String(), ImplementationProposalID: implementationProposalID.String(),
		},
	}
}

func conversationInputManifest(
	t *testing.T,
	id uuid.UUID,
	projectID uuid.UUID,
	userID uuid.UUID,
	source platformdomain.ArtifactRef,
	purpose string,
	now time.Time,
) platformdomain.InputManifest {
	t.Helper()
	manifest, err := platformdomain.NewInputManifest(
		id.String(), projectID.String(), "conversation.workflow_intent", "", nil,
		[]platformdomain.ManifestSource{{Ref: source, Purpose: purpose}}, json.RawMessage(`{}`), "v1", userID.String(), now,
	)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func createConversationWorkbenchLineage(
	t *testing.T,
	database *gorm.DB,
	projectID uuid.UUID,
	runID uuid.UUID,
	userID uuid.UUID,
	group string,
	now time.Time,
) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	rootID, leafID, proposalID := uuid.New(), uuid.New(), uuid.New()
	ordinal := 0
	runRef := runID
	invalidatedAt := now.Add(time.Second)
	reason := "rebased"
	if err := database.Create(&storage.ApplicationBuildManifestModel{
		ID: rootID, ProjectID: projectID, WorkflowRunID: &runRef, RootManifestID: rootID,
		RootOrdinal: &ordinal, ManifestGroupKey: &group, SchemaVersion: 1, ContentStore: "mongo",
		ContentRef: "conversation-" + group + "-root", ContentHash: conversationTestHash(group + "-root"),
		ManifestHash: conversationTestHash(group + "-root-manifest"), Status: "invalidated", CreatedBy: userID, CreatedAt: now,
		InvalidatedAt: &invalidatedAt, InvalidationReason: &reason,
	}).Error; err != nil {
		t.Fatal(err)
	}
	derivedFromID := rootID
	if err := database.Create(&storage.ApplicationBuildManifestModel{
		ID: leafID, ProjectID: projectID, WorkflowRunID: &runRef, RootManifestID: rootID, DerivedFromID: &derivedFromID,
		RootOrdinal: &ordinal, ManifestGroupKey: &group, SchemaVersion: 1, ContentStore: "mongo",
		ContentRef: "conversation-" + group + "-leaf", ContentHash: conversationTestHash(group + "-leaf"),
		ManifestHash: conversationTestHash(group + "-leaf-manifest"), Status: "frozen", CreatedBy: userID, CreatedAt: now.Add(2 * time.Second),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ImplementationProposalModel{
		ID: proposalID, ProjectID: projectID, BuildManifestID: leafID, Status: "open", Version: 1,
		ContentStore: "mongo", ContentRef: "conversation-" + group + "-proposal",
		ContentHash: conversationTestHash(group + "-proposal-content"), PayloadHash: conversationTestHash(group + "-proposal-payload"),
		OperationCount: 1, CreatedBy: userID, CreatedAt: now.Add(2 * time.Second),
	}).Error; err != nil {
		t.Fatal(err)
	}
	return rootID, leafID, proposalID
}

func containsConversationDefinitionVersion(definitions []intentDefinitionContext, id uuid.UUID) bool {
	for _, definition := range definitions {
		if definition.VersionID == id.String() {
			return true
		}
	}
	return false
}

func containsConversationWorkbenchTarget(targets []intentWorkbenchTargetContext, expected intentWorkbenchTargetContext) bool {
	for _, target := range targets {
		if target.DefinitionVersionID == expected.DefinitionVersionID && target.RunID == expected.RunID &&
			target.RootBundleID == expected.RootBundleID && target.ActiveBundleID == expected.ActiveBundleID &&
			target.ManifestGroup != "" && target.Ordinal == 0 {
			return true
		}
	}
	return false
}

func conversationTestHash(seed string) string {
	digest := sha256.Sum256([]byte("conversation:" + seed))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func conversationTimePointer(value time.Time) *time.Time {
	return &value
}

func conversationPostgresDatabase(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	base, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := "conversation_workbench_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	database, err := gorm.Open(
		gormpostgres.Open(conversationPostgresDSNWithSearchPath(t, dsn, schema)),
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

func conversationPostgresDSNWithSearchPath(t *testing.T, dsn string, schema string) string {
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
