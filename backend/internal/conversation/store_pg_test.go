package conversation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	platformdomain "github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/generation"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"github.com/worksflow/builder/backend/internal/testsupport"
	runtime "github.com/worksflow/builder/backend/internal/workflow"
	"github.com/worksflow/builder/backend/migrations"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type legacyWorkbenchExecutionResult struct {
	RunID                    string
	BundleID                 string
	ImplementationProposalID string
}

// validateLegacyWorkbenchResult keeps the pre-015 lineage assertions in this
// historical canary without reintroducing a browser execution contract.
func validateLegacyWorkbenchResult(
	database *gorm.DB,
	projectID uuid.UUID,
	payload CommandPayload,
	result legacyWorkbenchExecutionResult,
) error {
	runID, runErr := uuid.Parse(result.RunID)
	bundleID, bundleErr := uuid.Parse(result.BundleID)
	proposalID, proposalErr := uuid.Parse(result.ImplementationProposalID)
	expectedRunID, expectedRunErr := uuid.Parse(payload.Workbench.ExpectedRunID)
	expectedRootID, expectedRootErr := uuid.Parse(payload.Workbench.ExpectedBundleID)
	definitionID, definitionErr := uuid.Parse(payload.DefinitionVersionID)
	if runErr != nil || bundleErr != nil || proposalErr != nil || expectedRunErr != nil || expectedRootErr != nil || definitionErr != nil || runID != expectedRunID {
		return core.ErrConflict
	}
	var run storage.WorkflowRunModel
	if err := database.Where(
		"id = ? AND project_id = ? AND definition_version_id = ? AND status IN ?",
		runID, projectID, definitionID, activeWorkbenchRunStatuses,
	).Take(&run).Error; err != nil {
		return mapNotFound(err)
	}
	root, leaf, err := loadAuthoritativeWorkbenchLeaf(database, projectID, runID, expectedRootID)
	if err != nil || root.ID != expectedRootID || leaf.ID != bundleID {
		return core.ErrConflict
	}
	workspaceID, err := currentApprovedWorkspaceRevisionID(database, projectID, false)
	if err != nil || !sameOptionalUUID(leaf.WorkspaceRevisionID, workspaceID) {
		return core.ErrConflict
	}
	var proposal storage.ImplementationProposalModel
	if err := database.Where("id = ? AND project_id = ?", proposalID, projectID).Take(&proposal).Error; err != nil {
		return mapNotFound(err)
	}
	if proposal.BuildManifestID != leaf.ID || !containsString(executableImplementationProposalStatuses, proposal.Status) ||
		proposal.AppliedAt != nil || proposal.AppliedBy != nil {
		return core.ErrConflict
	}
	return nil
}

func TestGORMStoreIntentMessageBudgetFailsClosedAndPreservesBoundary(t *testing.T) {
	database, cleanup := conversationPostgresDatabase(t)
	defer cleanup()
	store, err := NewGORMStore(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	actorID, projectID := uuid.New(), uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: actorID, Email: "intent-budget-" + uuid.NewString() + "@example.com", DisplayName: "Intent Budget",
		PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Intent budget", Description: "", Lifecycle: "active", Version: 1,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	insertHistory := func(contents []string) (uuid.UUID, storage.ConversationMessageModel) {
		t.Helper()
		conversationID := uuid.New()
		if err := database.Create(&storage.ConversationModel{
			ID: conversationID, ProjectID: projectID, Title: "Intent budget", Status: string(ConversationActive), Version: 1,
			CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		var trigger storage.ConversationMessageModel
		for index, content := range contents {
			model := storage.ConversationMessageModel{
				ID: uuid.New(), ConversationID: conversationID, Sequence: uint64(index + 1), Role: string(MessageUser),
				Content: content, CreatedBy: actorID, CreatedAt: now.Add(time.Duration(index) * time.Millisecond),
			}
			if err := database.Create(&model).Error; err != nil {
				t.Fatal(err)
			}
			trigger = model
		}
		return conversationID, trigger
	}

	calibrationContents := make([]string, maxIntentConversationMessages)
	for index := range calibrationContents {
		calibrationContents[index] = "x"
	}
	calibrationConversationID, calibrationTrigger := insertHistory(calibrationContents)
	calibrationMessages, err := store.loadIntentMessages(ctx, calibrationConversationID, calibrationTrigger)
	if err != nil {
		t.Fatalf("load canonical boundary calibration: %v", err)
	}
	calibrationContext, err := platformdomain.CanonicalJSON(ProviderConversationContext{TailMessages: calibrationMessages})
	if err != nil {
		t.Fatal(err)
	}
	canonicalBaseBytes := len(calibrationContext)

	insertCanonicalBoundary := func(targetBytes int) (uuid.UUID, storage.ConversationMessageModel, []string) {
		t.Helper()
		conversationID := uuid.New()
		if err := database.Create(&storage.ConversationModel{
			ID: conversationID, ProjectID: projectID, Title: "Canonical intent budget", Status: string(ConversationActive), Version: 1,
			CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		models := make([]storage.ConversationMessageModel, maxIntentConversationMessages)
		for index := range models {
			models[index] = storage.ConversationMessageModel{
				ID: uuid.New(), ConversationID: conversationID, Sequence: uint64(index + 1), Role: string(MessageUser),
				Content: "x", CreatedBy: actorID, CreatedAt: now.Add(time.Duration(index) * time.Millisecond),
			}
		}
		remaining := targetBytes - canonicalBaseBytes
		if remaining < 0 {
			t.Fatalf("canonical boundary base exceeds target: base=%d target=%d", canonicalBaseBytes, targetBytes)
		}
		for index := range models {
			added := min(remaining, 32767)
			models[index].Content += strings.Repeat("a", added)
			remaining -= added
			if remaining == 0 {
				break
			}
		}
		if remaining != 0 {
			t.Fatalf("construct canonical boundary: target=%d remaining=%d", targetBytes, remaining)
		}
		for index := range models {
			if err := database.Create(&models[index]).Error; err != nil {
				t.Fatal(err)
			}
		}
		contents := make([]string, len(models))
		for index := range models {
			contents[index] = models[index].Content
		}
		return conversationID, models[len(models)-1], contents
	}

	conversationID, trigger, boundary := insertCanonicalBoundary(maxIntentConversationMessageBytes)
	messages, err := store.loadIntentMessages(ctx, conversationID, trigger)
	if err != nil || len(messages) != maxIntentConversationMessages {
		t.Fatalf("exact budget boundary failed: messages=%d err=%v", len(messages), err)
	}
	for index, message := range messages {
		if message.Sequence != uint64(index+1) || message.Content != boundary[index] {
			t.Fatalf("boundary history lost order at %d: %+v", index, message)
		}
	}
	tooLargeConversationID, tooLargeTrigger, _ := insertCanonicalBoundary(maxIntentConversationMessageBytes + 1)
	if _, err := store.loadIntentMessages(ctx, tooLargeConversationID, tooLargeTrigger); !errors.Is(err, ErrIntentSummaryCheckpointRequired) {
		t.Fatalf("canonical context one byte over budget did not require a checkpoint: %v", err)
	}
	fiftyOne := make([]string, 51)
	for index := range fiftyOne {
		fiftyOne[index] = "message-" + strconv.Itoa(index+1)
	}
	conversationID, trigger = insertHistory(fiftyOne)
	messages, err = store.loadIntentMessages(ctx, conversationID, trigger)
	if err != nil || len(messages) != 51 || messages[0].Content != "message-1" || messages[50].Content != "message-51" {
		t.Fatalf("51-message history was truncated or rejected: first=%+v count=%d err=%v", messages[0], len(messages), err)
	}

	tooMany := make([]string, maxIntentConversationMessages+1)
	for index := range tooMany {
		tooMany[index] = "x"
	}
	conversationID, trigger = insertHistory(tooMany)
	if _, err := store.loadIntentMessages(ctx, conversationID, trigger); !errors.Is(err, ErrIntentSummaryCheckpointRequired) {
		t.Fatalf("201-message history did not require a controlled summary checkpoint: %v", err)
	}

	tooLarge := []string{
		strings.Repeat("a", 32768), strings.Repeat("b", 32768), strings.Repeat("c", 32768),
		strings.Repeat("d", 32768), "x",
	}
	conversationID, trigger = insertHistory(tooLarge)
	if _, err := store.loadIntentMessages(ctx, conversationID, trigger); !errors.Is(err, ErrIntentSummaryCheckpointRequired) {
		t.Fatalf(">128KiB history did not require a controlled summary checkpoint: %v", err)
	}
}

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
	if err := database.Create(&storage.ProjectMemberModel{
		ProjectID: projectID, UserID: userID, Role: string(core.RoleOwner), JoinedAt: now, UpdatedAt: now,
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
			ExecutionProfileVersion: runtime.LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: runtime.LegacyWorkflowExecutionProfileHash,
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
		Content:                 json.RawMessage(`{"nodes":[{"id":"pages","type":"fan_out","fanOut":{"itemKind":"blueprint_selection_page"}}],"edges":[]}`),
		ContentHash:             conversationTestHash("selection-definition-" + selectionVersionID.String()),
		ExecutionProfileVersion: runtime.LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: runtime.LegacyWorkflowExecutionProfileHash,
		ValidationReport: json.RawMessage(`{}`), Published: true, CreatedBy: userID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	candidateVersionIDs := []uuid.UUID{currentVersionID}
	for index := 1; index < 21; index++ {
		candidateDefinitionID, candidateVersionID := uuid.New(), uuid.New()
		if err := database.Create(&storage.WorkflowDefinitionModel{
			ID: candidateDefinitionID, ProjectID: &projectID, WorkflowKey: "conversation-candidate-" + strconv.Itoa(index),
			Title: "Conversation candidate " + strconv.Itoa(index), Description: "", Lifecycle: "active", CreatedBy: userID,
			CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Create(&storage.WorkflowDefinitionVersionModel{
			ID: candidateVersionID, DefinitionID: candidateDefinitionID, Version: 1, SchemaVersion: 1,
			Content: json.RawMessage(`{"nodes":[],"edges":[]}`), ContentHash: conversationTestHash("candidate-definition-" + candidateVersionID.String()),
			ExecutionProfileVersion: runtime.LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: runtime.LegacyWorkflowExecutionProfileHash,
			ValidationReport: json.RawMessage(`{}`), Published: true, CreatedBy: userID, CreatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		candidateVersionIDs = append(candidateVersionIDs, candidateVersionID)
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
			ExecutionProfileVersion: runtime.LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: runtime.LegacyWorkflowExecutionProfileHash,
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
		ExecutionProfileVersion: runtime.LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: runtime.LegacyWorkflowExecutionProfileHash,
		Scope: json.RawMessage(`{}`), Context: json.RawMessage(`{}`), StartedBy: userID, StartedAt: &now,
		CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second),
	}).Error; err != nil {
		t.Fatal(err)
	}
	selectionRootID, selectionLeafID, _ := createConversationWorkbenchLineage(t, database, projectID, selectionRunID, userID, "selection", now.Add(2*time.Second))
	bulkTargets := make([]intentWorkbenchTargetContext, 0, maxIntentWorkbenchTargets+1)
	for index := 0; index <= maxIntentWorkbenchTargets; index++ {
		group := "bulk-" + strconv.Itoa(index)
		bulkRootID, bulkLeafID, _ := createConversationWorkbenchLineage(t, database, projectID, otherRunID, userID, group, now.Add(3*time.Second))
		bulkTargets = append(bulkTargets, intentWorkbenchTargetContext{
			DefinitionVersionID: oldVersionID.String(), RunID: otherRunID.String(),
			RootBundleID: bulkRootID.String(), ActiveBundleID: bulkLeafID.String(), ManifestGroup: group,
		})
	}

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
		ctx, projectID, conversationID, triggerMessageID, candidateVersionIDs, []platformdomain.ArtifactRef{sourceRef}, manifestIntent, governanceManifest.JobType, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !containsConversationDefinitionVersion(generationContext.Definitions, currentVersionID) ||
		containsConversationDefinitionVersion(generationContext.Definitions, oldVersionID) ||
		containsConversationDefinitionVersion(generationContext.Definitions, selectionVersionID) {
		t.Fatalf("generation context did not isolate full definition content to start candidates: %+v", generationContext.Definitions)
	}
	for _, candidateVersionID := range candidateVersionIDs {
		if !containsConversationDefinitionVersion(generationContext.Definitions, candidateVersionID) {
			t.Fatalf("candidate definition %s was omitted from the complete PostgreSQL context", candidateVersionID)
		}
	}
	if len(candidateVersionIDs) != 21 {
		t.Fatalf("candidate fixture count=%d", len(candidateVersionIDs))
	}
	if len(generationContext.Definitions) != len(candidateVersionIDs) {
		t.Fatalf("PostgreSQL compact source definition count=%d want=%d", len(generationContext.Definitions), len(candidateVersionIDs))
	}
	if !containsConversationWorkbenchTarget(generationContext.WorkbenchTargets, intentWorkbenchTargetContext{
		DefinitionVersionID: oldVersionID.String(), RunID: runID.String(), RootBundleID: rootID.String(), ActiveBundleID: leafID.String(),
	}) {
		t.Fatalf("active old-version run target was not exposed: %+v", generationContext.WorkbenchTargets)
	}
	for _, target := range generationContext.WorkbenchTargets {
		if target.RootBundleID == rootID.String() &&
			(target.DefinitionKey != "conversation-workbench" || target.DefinitionTitle != "Conversation workbench") {
			t.Fatalf("Workbench target lost compact definition metadata: %+v", target)
		}
	}
	if !containsConversationWorkbenchTarget(generationContext.WorkbenchTargets, intentWorkbenchTargetContext{
		DefinitionVersionID: selectionVersionID.String(), RunID: selectionRunID.String(), RootBundleID: selectionRootID.String(), ActiveBundleID: selectionLeafID.String(),
	}) {
		t.Fatalf("active Blueprint-selection run was coupled to the ordinary start candidates: %+v", generationContext.WorkbenchTargets)
	}
	seenTargets := make(map[string]struct{}, len(generationContext.WorkbenchTargets))
	for _, target := range generationContext.WorkbenchTargets {
		identity := target.RunID + "\x00" + target.RootBundleID
		if _, duplicate := seenTargets[identity]; duplicate {
			t.Fatalf("PostgreSQL Workbench target query returned a duplicate: %+v", target)
		}
		seenTargets[identity] = struct{}{}
	}
	for _, target := range bulkTargets {
		if !containsConversationWorkbenchTarget(generationContext.WorkbenchTargets, target) {
			t.Fatalf("Workbench target after the former LIMIT 100 boundary was omitted: %+v", target)
		}
	}
	if len(generationContext.WorkbenchTargets) != len(bulkTargets)+3 {
		t.Fatalf("PostgreSQL Workbench target count=%d want=%d", len(generationContext.WorkbenchTargets), len(bulkTargets)+3)
	}
	// Prove conversation linkage is pushed below the raw candidate LIMIT: these
	// newer unrelated rows would otherwise overflow the explicit project-wide
	// candidate budget before the linked run could be considered.
	noiseManifests := make([]storage.ApplicationBuildManifestModel, 0, 2*(maxIntentWorkbenchTargetCandidates+1))
	for index := 0; index <= maxIntentWorkbenchTargetCandidates; index++ {
		noiseRootID, noiseLeafID := uuid.New(), uuid.New()
		noiseGroup := uuid.NewString()
		noiseSlice := uuid.NewString()
		noiseOrdinal := 0
		otherRunRef := otherRunID
		derivedFromID := noiseRootID
		noiseManifests = append(noiseManifests,
			storage.ApplicationBuildManifestModel{
				ID: noiseRootID, ProjectID: projectID, WorkflowRunID: &otherRunRef, RootManifestID: noiseRootID,
				RootOrdinal: &noiseOrdinal, ManifestGroupKey: &noiseGroup, DeliverySliceID: &noiseSlice, SchemaVersion: 1, ContentStore: "mongo",
				ContentRef: "noise-root-" + strconv.Itoa(index), ContentHash: conversationTestHash("noise-root-" + strconv.Itoa(index)),
				ManifestHash: conversationTestHash("noise-root-manifest-" + strconv.Itoa(index)), Status: "invalidated", CreatedBy: userID, CreatedAt: now.Add(4 * time.Second),
			},
			storage.ApplicationBuildManifestModel{
				ID: noiseLeafID, ProjectID: projectID, WorkflowRunID: &otherRunRef, RootManifestID: noiseRootID, DerivedFromID: &derivedFromID,
				RootOrdinal: &noiseOrdinal, ManifestGroupKey: &noiseGroup, DeliverySliceID: &noiseSlice, SchemaVersion: 1, ContentStore: "mongo",
				ContentRef: "noise-leaf-" + strconv.Itoa(index), ContentHash: conversationTestHash("noise-leaf-" + strconv.Itoa(index)),
				ManifestHash: conversationTestHash("noise-leaf-manifest-" + strconv.Itoa(index)), Status: "frozen", CreatedBy: userID, CreatedAt: now.Add(5 * time.Second),
			},
		)
	}
	if err := database.CreateInBatches(noiseManifests, 200).Error; err != nil {
		t.Fatal(err)
	}
	linkProposalInput := CreateIntentProposalInput{
		TriggerMessageID: triggerMessageID.String(), AssistantContent: "Continue the conversation-linked Workbench run.",
		Kind: IntentWorkbenchInstruction, SuggestedDefinitionVersionID: oldVersionID.String(), Scope: json.RawMessage(`{"slice":"linked"}`),
		SourceRefs: []platformdomain.ArtifactRef{sourceRef}, ManifestIntent: manifestIntent,
		WorkbenchInstruction: WorkbenchInstruction{
			Objective: "Continue the linked application", Constraints: []string{"Keep exact lineage"},
			ExpectedRunID: runID.String(), ExpectedBundleID: rootID.String(),
		},
	}
	linkProposal, _, err := store.CreateIntentProposal(
		ctx, projectID, conversationID, userID, linkProposalInput, ProposalProvenance{Origin: ProposalOriginSubmitted}, governanceManifest.JobType,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, linkedCommand, err := store.DecideProposal(
		ctx, projectID, conversationID, uuid.MustParse(linkProposal.ID), userID, linkProposal.ETag, DecideProposalInput{Decision: DecisionAccept},
	)
	if err != nil || linkedCommand == nil {
		t.Fatalf("create conversation-linked command: command=%+v err=%v", linkedCommand, err)
	}
	executedAt := now.Add(4 * time.Second)
	if err := database.Model(&storage.ConversationCommandModel{}).Where("id = ?", linkedCommand.ID).Updates(map[string]any{
		"status": string(CommandExecuted), "executed_by": userID, "executed_at": executedAt, "updated_at": executedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	linkedContext, err := store.IntentGenerationContext(
		ctx, projectID, conversationID, triggerMessageID, candidateVersionIDs, []platformdomain.ArtifactRef{sourceRef}, manifestIntent, governanceManifest.JobType, nil,
	)
	if err != nil || len(linkedContext.WorkbenchTargets) != 1 || linkedContext.WorkbenchTargets[0].RunID != runID.String() || linkedContext.WorkbenchTargets[0].RootBundleID != rootID.String() {
		t.Fatalf("conversation-linked run did not take priority over project-wide targets: targets=%+v err=%v", linkedContext.WorkbenchTargets, err)
	}
	exactContext, err := store.IntentGenerationContext(
		ctx, projectID, conversationID, triggerMessageID, candidateVersionIDs, []platformdomain.ArtifactRef{sourceRef}, manifestIntent, governanceManifest.JobType,
		&WorkbenchTargetHint{RunID: runID.String(), RootBundleID: rootID.String()},
	)
	if err != nil || len(exactContext.WorkbenchTargets) != 1 || exactContext.WorkbenchTargets[0].SliceTitle == "" {
		t.Fatalf("exact linked Workbench hint lost authoritative page metadata: targets=%+v err=%v", exactContext.WorkbenchTargets, err)
	}
	var persistedRoot storage.ApplicationBuildManifestModel
	if err := database.Where("id = ?", rootID).Take(&persistedRoot).Error; err != nil {
		t.Fatal(err)
	}
	var persistedRun storage.WorkflowRunModel
	if err := database.Where("id = ?", runID).Take(&persistedRun).Error; err != nil {
		t.Fatal(err)
	}
	groupID := uuid.MustParse(*persistedRoot.ManifestGroupKey)
	var compiler storage.WorkflowNodeRunModel
	if err := database.Where("id = ? AND run_id = ?", groupID, runID).Take(&compiler).Error; err != nil {
		t.Fatal(err)
	}
	var tamperedRunContext runtime.RunContext
	if err := json.Unmarshal(persistedRun.Context, &tamperedRunContext); err != nil {
		t.Fatal(err)
	}
	metadata := tamperedRunContext.Nodes[compiler.NodeKey]
	var tamperedManifest runtime.BuildManifest
	if err := json.Unmarshal(metadata.Output, &tamperedManifest); err != nil {
		t.Fatal(err)
	}
	tamperedManifest.SliceIDs[0] = uuid.NewString()
	tamperedManifest.Hash = ""
	if err := tamperedManifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	metadata.Output, err = platformdomain.CanonicalJSON(tamperedManifest)
	if err != nil {
		t.Fatal(err)
	}
	tamperedRunContext.Nodes[compiler.NodeKey] = metadata
	tamperedRunContextJSON, err := platformdomain.CanonicalJSON(tamperedRunContext)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.WorkflowRunModel{}).Where("id = ?", runID).
		Update("context", tamperedRunContextJSON).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.IntentGenerationContext(
		ctx, projectID, conversationID, triggerMessageID, candidateVersionIDs, []platformdomain.ArtifactRef{sourceRef}, manifestIntent, governanceManifest.JobType,
		&WorkbenchTargetHint{RunID: runID.String(), RootBundleID: rootID.String()},
	); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("conversation accepted a compiler SliceIDs tamper against the persisted root identity: %v", err)
	}
	if err := database.Model(&storage.WorkflowRunModel{}).Where("id = ?", runID).
		Update("context", persistedRun.Context).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.IntentGenerationContext(
		ctx, projectID, conversationID, triggerMessageID, candidateVersionIDs, []platformdomain.ArtifactRef{sourceRef}, manifestIntent, governanceManifest.JobType,
		&WorkbenchTargetHint{RunID: otherRunID.String(), RootBundleID: otherRootID.String()},
	); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("forged cross-run hint escaped conversation linkage: %v", err)
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
	validResult := legacyWorkbenchExecutionResult{
		RunID: runID.String(), BundleID: leafID.String(), ImplementationProposalID: implementationProposalID.String(),
	}
	if err := validateLegacyWorkbenchResult(database, projectID, command.Payload, validResult); err != nil {
		t.Fatalf("valid M1-governed result for M0 run was rejected: %v", err)
	}
	for _, status := range []string{"stale", "applied", "partially_applied"} {
		if err := database.Model(&storage.ImplementationProposalModel{}).Where("id = ?", implementationProposalID).
			Updates(map[string]any{"status": status, "applied_by": nil, "applied_at": nil}).Error; err != nil {
			t.Fatal(err)
		}
		if err := validateLegacyWorkbenchResult(database, projectID, command.Payload, validResult); !errors.Is(err, core.ErrConflict) {
			t.Fatalf("proposal status %q result error = %v", status, err)
		}
	}
	if err := database.Model(&storage.ImplementationProposalModel{}).Where("id = ?", implementationProposalID).
		Updates(map[string]any{"status": "ready", "applied_by": nil, "applied_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyWorkbenchResult(database, projectID, command.Payload, validResult); err != nil {
		t.Fatalf("ready unapplied proposal was rejected: %v", err)
	}
	if err := database.Model(&storage.ImplementationProposalModel{}).Where("id = ?", implementationProposalID).
		Updates(map[string]any{"status": "open", "applied_by": userID, "applied_at": now.Add(3 * time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyWorkbenchResult(database, projectID, command.Payload, validResult); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("proposal with applied identity result error = %v", err)
	}
	if err := database.Model(&storage.ImplementationProposalModel{}).Where("id = ?", implementationProposalID).
		Updates(map[string]any{"status": "open", "applied_by": nil, "applied_at": nil}).Error; err != nil {
		t.Fatal(err)
	}

	substitutedProposal := validResult
	substitutedProposal.ImplementationProposalID = otherProposalID.String()
	if err := validateLegacyWorkbenchResult(database, projectID, command.Payload, substitutedProposal); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("proposal substitution error = %v", err)
	}
	otherRunResult := legacyWorkbenchExecutionResult{
		RunID: otherRunID.String(), BundleID: otherLeafID.String(), ImplementationProposalID: otherProposalID.String(),
	}
	if err := validateLegacyWorkbenchResult(database, projectID, command.Payload, otherRunResult); !errors.Is(err, core.ErrConflict) {
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
	if err := validateLegacyWorkbenchResult(database, projectID, command.Payload, validResult); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("leaf without newly-created current workspace result error = %v", err)
	}
	if err := database.Model(&storage.ApplicationBuildManifestModel{}).Where("id = ?", leafID).
		Update("workspace_revision_id", workspaceRevisionID).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyWorkbenchResult(database, projectID, command.Payload, validResult); err != nil {
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
	if err := validateLegacyWorkbenchResult(database, projectID, command.Payload, validResult); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("workspace-advanced result error = %v", err)
	}

	newLeafID := uuid.New()
	derivedFromID := leafID
	var currentLeaf storage.ApplicationBuildManifestModel
	if err := database.Where("id = ?", leafID).Take(&currentLeaf).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ApplicationBuildManifestModel{
		ID: newLeafID, ProjectID: projectID, WorkflowRunID: &runID, RootManifestID: rootID, DerivedFromID: &derivedFromID,
		WorkspaceRevisionID: &workspaceRevision2ID, RootOrdinal: currentLeaf.RootOrdinal, ManifestGroupKey: currentLeaf.ManifestGroupKey,
		DeliverySliceID: currentLeaf.DeliverySliceID, SchemaVersion: 1, ContentStore: "mongo",
		ContentRef: "conversation-primary-new-leaf", ContentHash: conversationTestHash("primary-new-leaf"),
		ManifestHash: conversationTestHash("primary-new-leaf-manifest"), Status: "frozen", CreatedBy: userID, CreatedAt: now.Add(3 * time.Second),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyWorkbenchResult(database, projectID, command.Payload, validResult); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("stale leaf result error = %v", err)
	}
}

func TestWorkbenchCommandCannotAdoptGovernedManualProposalPostgres(t *testing.T) {
	database, cleanup := conversationPostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	fixture := createAtomicWorkbenchFixture(t, database)
	completed, err := fixture.store.CompleteWorkbenchCommand(
		ctx, fixture.claim, fixture.actorID, fixture.receipt,
	)
	if !errors.Is(err, core.ErrConflict) || completed.Status == CommandExecuted {
		t.Fatalf("legacy command adopted a governed manual Proposal: command=%+v err=%v", completed, err)
	}
	var persistedCommand storage.ConversationCommandModel
	if err := database.Where("id = ?", fixture.commandID).Take(&persistedCommand).Error; err != nil {
		t.Fatal(err)
	}
	if persistedCommand.Status != string(CommandPending) || persistedCommand.Version != 2 ||
		persistedCommand.ExecutedAt != nil || persistedCommand.ExecutedBy != nil ||
		persistedCommand.ExecutionClaim == nil || *persistedCommand.ExecutionClaim != fixture.claim.Token {
		t.Fatalf("failed governed writeback mutated command or lease: %+v", persistedCommand)
	}
	var persistedProposal storage.ImplementationProposalModel
	if err := database.Where("id = ?", fixture.implementationProposalID).Take(&persistedProposal).Error; err != nil {
		t.Fatal(err)
	}
	if persistedProposal.ExecutionSource != string(core.ImplementationSourceManualSubmission) ||
		persistedProposal.Status != "open" || persistedProposal.Version != 1 ||
		persistedProposal.ConversationCommandID != nil || persistedProposal.AppliedAt != nil || persistedProposal.AppliedBy != nil {
		t.Fatalf("failed legacy adoption mutated governed Proposal: %+v", persistedProposal)
	}
}

func TestWorkbenchCommandLeaseTakeoverCannotAdoptGovernedManualProposalPostgres(t *testing.T) {
	database, cleanup := conversationPostgresDatabase(t)
	defer cleanup()
	ctx := context.Background()

	for _, mode := range []string{"failed_attempt", "expired_claim"} {
		t.Run(mode, func(t *testing.T) {
			fixture := createAtomicWorkbenchFixture(t, database)
			actorB := uuid.New()
			now := time.Now().UTC()
			if err := database.Create(&storage.UserModel{
				ID: actorB, Email: "takeover-" + uuid.NewString() + "@example.com", DisplayName: "Takeover Editor",
				PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.Create(&storage.ProjectMemberModel{
				ProjectID: fixture.projectID, UserID: actorB, Role: string(core.RoleEditor), JoinedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}

			var retryETag string
			if mode == "failed_attempt" {
				pending, err := fixture.store.FailCommandAttempt(ctx, fixture.claim, fixture.actorID, &CommandFailure{
					Code: "ai_unavailable", Message: "The provider was unavailable; retry safely.",
				})
				if err != nil {
					t.Fatal(err)
				}
				retryETag = pending.ETag
			} else {
				if err := database.Model(&storage.ConversationCommandModel{}).Where("id = ?", fixture.commandID).
					Update("claim_expires_at", now.Add(-time.Minute)).Error; err != nil {
					t.Fatal(err)
				}
				current, err := fixture.store.GetCommand(ctx, fixture.projectID, fixture.conversationID, fixture.commandID)
				if err != nil {
					t.Fatal(err)
				}
				retryETag = current.ETag
			}
			claimB, err := fixture.store.ClaimCommand(
				ctx, fixture.projectID, fixture.conversationID, fixture.commandID, actorB, retryETag, 10*time.Minute,
			)
			if err != nil {
				t.Fatalf("actor B could not take over actor A's %s command: %v", mode, err)
			}
			completed, err := fixture.store.CompleteWorkbenchCommand(ctx, claimB, actorB, fixture.receipt)
			if !errors.Is(err, core.ErrConflict) || completed.Status == CommandExecuted {
				t.Fatalf("actor B adopted actor A's governed manual Proposal: command=%+v err=%v", completed, err)
			}
			var command storage.ConversationCommandModel
			if err := database.Where("id = ?", fixture.commandID).Take(&command).Error; err != nil {
				t.Fatal(err)
			}
			if command.Status != string(CommandPending) || command.Version != claimB.Command.Version ||
				command.ExecutionActorID == nil || *command.ExecutionActorID != actorB ||
				command.ExecutionClaim == nil || *command.ExecutionClaim != claimB.Token || command.ExecutedAt != nil {
				t.Fatalf("failed cross-actor adoption mutated command or takeover lease: %+v", command)
			}
			var proposal storage.ImplementationProposalModel
			if err := database.Where("id = ?", fixture.implementationProposalID).Take(&proposal).Error; err != nil {
				t.Fatal(err)
			}
			if proposal.CreatedBy != fixture.actorID ||
				proposal.ExecutionSource != string(core.ImplementationSourceManualSubmission) ||
				proposal.Status != "open" || proposal.Version != 1 || proposal.ConversationCommandID != nil {
				t.Fatalf("takeover rewrote governed manual Proposal provenance: %+v", proposal)
			}
		})
	}
}

func TestWorkbenchCommandCanBeRejectedWhenOnlyGovernedManualProposalExistsPostgres(t *testing.T) {
	database, cleanup := conversationPostgresDatabase(t)
	defer cleanup()
	ctx := context.Background()
	fixture := createAtomicWorkbenchFixture(t, database)
	if err := database.Model(&storage.ConversationCommandModel{}).Where("id = ?", fixture.commandID).
		Update("claim_expires_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	current, err := fixture.store.GetCommand(ctx, fixture.projectID, fixture.conversationID, fixture.commandID)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := fixture.store.RejectCommand(
		ctx,
		fixture.projectID,
		fixture.conversationID,
		fixture.commandID,
		fixture.actorID,
		current.ETag,
		"reject after generation",
	)
	if err != nil || rejected.Status != CommandRejected {
		t.Fatalf("unbound governed manual Proposal held the legacy command hostage: command=%+v err=%v", rejected, err)
	}
	var persisted storage.ConversationCommandModel
	if err := database.Where("id = ?", fixture.commandID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != string(CommandRejected) || persisted.RejectedAt == nil || persisted.RejectedBy == nil ||
		*persisted.RejectedBy != fixture.actorID {
		t.Fatalf("legacy command rejection was not committed atomically: %+v", persisted)
	}
	var proposal storage.ImplementationProposalModel
	if err := database.Where("id = ?", fixture.implementationProposalID).Take(&proposal).Error; err != nil {
		t.Fatal(err)
	}
	if proposal.ExecutionSource != string(core.ImplementationSourceManualSubmission) || proposal.Status != "open" ||
		proposal.Version != 1 || proposal.ConversationCommandID != nil || proposal.AppliedAt != nil || proposal.AppliedBy != nil {
		t.Fatalf("rejecting the legacy command mutated the unbound governed Proposal: %+v", proposal)
	}
}

func TestWorkbenchCommandCannotBeRejectedWhileGenerationClaimIsLivePostgres(t *testing.T) {
	database, cleanup := conversationPostgresDatabase(t)
	defer cleanup()
	fixture := createAtomicWorkbenchFixtureWithGeneration(t, database, false)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := database.Model(&storage.ConversationCommandModel{}).Where("id = ?", fixture.commandID).
		Update("claim_expires_at", now.Add(-time.Millisecond)).Error; err != nil {
		t.Fatal(err)
	}
	current, err := fixture.store.GetCommand(ctx, fixture.projectID, fixture.conversationID, fixture.commandID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.RejectCommand(
		ctx,
		fixture.projectID,
		fixture.conversationID,
		fixture.commandID,
		fixture.actorID,
		current.ETag,
		"reject while generation still owns its lease",
	); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("command was rejectable while its generation claim was live: %v", err)
	}
	var persisted storage.ConversationCommandModel
	if err := database.Where("id = ?", fixture.commandID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != string(CommandPending) || persisted.RejectedAt != nil {
		t.Fatalf("live generation rejection mutated command: %+v", persisted)
	}
	var generated int64
	if err := database.Model(&storage.ImplementationProposalModel{}).
		Where("conversation_command_id = ?", fixture.commandID).Count(&generated).Error; err != nil {
		t.Fatal(err)
	}
	if generated != 0 {
		t.Fatalf("live-claim fixture unexpectedly had %d generated proposals", generated)
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
	deliverySliceID          string
	claim                    commandClaim
	receipt                  WorkbenchExecutionReceipt
}

func createAtomicWorkbenchFixture(t *testing.T, database *gorm.DB) atomicWorkbenchFixture {
	return createAtomicWorkbenchFixtureWithGeneration(t, database, true)
}

func createAtomicWorkbenchFixtureWithGeneration(
	t *testing.T,
	database *gorm.DB,
	seedGovernedManualProposal bool,
) atomicWorkbenchFixture {
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
	if err := database.Create(&storage.ProjectMemberModel{
		ProjectID: projectID, UserID: actorID, Role: string(core.RoleOwner), JoinedAt: now, UpdatedAt: now,
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
		ExecutionProfileVersion: runtime.LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: runtime.LegacyWorkflowExecutionProfileHash,
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
		ExecutionProfileVersion: runtime.LegacyWorkflowExecutionProfileVersion, ExecutionProfileHash: runtime.LegacyWorkflowExecutionProfileHash,
		InputManifestID: &oldManifestID, Scope: json.RawMessage(`{}`), Context: json.RawMessage(`{}`),
		StartedBy: actorID, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	rootBundleID := uuid.New()
	manifestGroup := "atomic-" + fixtureID
	deliverySliceID := uuid.NewString()
	ordinal := 0
	if err := database.Create(&storage.ApplicationBuildManifestModel{
		ID: rootBundleID, ProjectID: projectID, WorkflowRunID: &runID, RootManifestID: rootBundleID,
		RootOrdinal: &ordinal, ManifestGroupKey: &manifestGroup, DeliverySliceID: &deliverySliceID, SchemaVersion: 1, ContentStore: "mongo",
		ContentRef: "atomic-root-" + fixtureID, ContentHash: conversationTestHash("atomic-root-" + fixtureID),
		ManifestHash: conversationTestHash("atomic-root-manifest-" + fixtureID), Status: "frozen",
		CreatedBy: actorID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	conversationID, triggerID, assistantID, reviewedProposalID, commandID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	implementationProposalID := commandID
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
	governanceSource := platformdomain.ArtifactRef{
		ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: conversationTestHash("atomic-source-" + fixtureID),
	}
	submittedContext := &ConversationContextProvenance{Version: 1, Mode: "submitted"}
	payload := CommandPayload{
		DefinitionVersionID: definitionVersionID.String(), DesiredOutputCapability: platformdomain.WorkflowOutputApplication,
		Scope:      json.RawMessage(`{}`),
		SourceRefs: []platformdomain.ArtifactRef{governanceSource}, ManifestIntent: manifestIntent, Workbench: instruction,
		ConversationContext: submittedContext,
	}
	encodedPayload, err := platformdomain.CanonicalJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	encodedSources, _ := platformdomain.CanonicalJSON(payload.SourceRefs)
	encodedManifest, _ := platformdomain.CanonicalJSON(manifestIntent)
	encodedInstruction, _ := platformdomain.CanonicalJSON(instruction)
	encodedConversationContext, _ := platformdomain.CanonicalJSON(submittedContext)
	err = database.Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&storage.WorkflowIntentProposalModel{
			ID: reviewedProposalID, ProjectID: projectID, ConversationID: conversationID, TriggerMessageID: triggerID,
			AssistantMessageID: assistantID, Kind: string(IntentWorkbenchInstruction), Status: string(ProposalAccepted), Version: 2,
			SuggestedDefinitionVersionID: definitionVersionID, Scope: json.RawMessage(`{}`), SourceRefs: encodedSources,
			ManifestIntent: encodedManifest, WorkbenchInstruction: encodedInstruction, Origin: string(ProposalOriginSubmitted),
			ConversationContext: encodedConversationContext,
			DecisionReason:      "", ProposedBy: actorID, DecidedBy: &actorID, CreatedAt: now, DecidedAt: &now,
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
			ConversationContext: encodedConversationContext,
			AcceptedBy:          actorID, CreatedAt: now, UpdatedAt: now,
		}).Error
	})
	if err != nil {
		t.Fatal(err)
	}
	_, generationInstruction, instructionHash, err := generation.CanonicalImplementationInstruction(instruction.Objective, instruction.Constraints)
	if err != nil {
		t.Fatal(err)
	}
	if seedGovernedManualProposal {
		completeCount := 0
		proposal := &storage.ImplementationProposalModel{
			ID: implementationProposalID, ProjectID: projectID, BuildManifestID: rootBundleID,
			ExecutionSource: string(core.ImplementationSourceManualSubmission),
			Status:          "open", Version: 1, ContentStore: "mongo", ContentRef: "atomic-proposal-" + fixtureID,
			ContentHash: conversationTestHash("atomic-proposal-content-" + fixtureID),
			PayloadHash: conversationTestHash("atomic-proposal-payload-" + fixtureID), OperationCount: 1,
			UnimplementedCount: &completeCount, BlockingDiagnosticCount: &completeCount,
			CreatedBy: actorID, CreatedAt: now.Add(time.Millisecond),
		}
		testsupport.CreateBoundImplementationProposal(t, database, proposal)
	} else {
		generationClaimID, generationToken := uuid.New(), uuid.New()
		generationExpires := now.Add(10 * time.Minute)
		commandIDCopy := commandID
		buildContract := testsupport.ReadyApplicationBuildContract(t, database, projectID, actorID, rootBundleID)
		generationClaim := &storage.ImplementationGenerationClaimModel{
			ID: generationClaimID, BuildManifestID: rootBundleID, ProjectID: projectID, RootManifestID: rootBundleID,
			ApplicationBuildContractID: &buildContract.ID, ApplicationBuildContractHash: &buildContract.ContractHash,
			RequestKey: commandID, ReservedProposalID: implementationProposalID,
			ExecutionSource: string(core.ImplementationSourceConversationCommand), ConversationCommandID: &commandIDCopy,
			GovernanceManifestID: &governanceManifestID, GovernanceManifestHash: &governanceHash,
			GovernanceSourceRefs: encodedSources,
			Instruction:          generationInstruction, InstructionHash: instructionHash,
			RequestedModel: "gpt-5", GenerationContractVersion: "implementation-proposal-generation/v1",
			SystemPromptHash: conversationTestHash("system-prompt"), OutputSchemaHash: conversationTestHash("output-schema"),
			ActorID: actorID, ClaimToken: &generationToken,
			ClaimExpiresAt: &generationExpires, Status: "processing", AttemptCount: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := database.Create(generationClaim).Error; err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewGORMStore(database)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimCommand(
		context.Background(), projectID, conversationID, commandID, actorID,
		CommandETag(commandID.String(), 1), 10*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	return atomicWorkbenchFixture{
		store: store, projectID: projectID, conversationID: conversationID, commandID: commandID, actorID: actorID,
		runID: runID, rootBundleID: rootBundleID, implementationProposalID: implementationProposalID,
		manifestGroup: manifestGroup, deliverySliceID: deliverySliceID, claim: claim,
		receipt: WorkbenchExecutionReceipt{
			RunID: runID.String(), RootBundleID: rootBundleID.String(), ActiveBundleID: rootBundleID.String(),
			ImplementationProposalID: implementationProposalID.String(), InstructionHash: instructionHash,
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
	groupLabel := group
	group = uuid.NewString()
	sliceID := uuid.NewString()
	ordinal := 0
	runRef := runID
	invalidatedAt := now.Add(time.Second)
	reason := "rebased"
	if err := database.Create(&storage.ApplicationBuildManifestModel{
		ID: rootID, ProjectID: projectID, WorkflowRunID: &runRef, RootManifestID: rootID,
		RootOrdinal: &ordinal, ManifestGroupKey: &group, DeliverySliceID: &sliceID, SchemaVersion: 1, ContentStore: "mongo",
		ContentRef: "conversation-" + group + "-root", ContentHash: conversationTestHash(group + "-root"),
		ManifestHash: conversationTestHash(group + "-root-manifest"), Status: "invalidated", CreatedBy: userID, CreatedAt: now,
		InvalidatedAt: &invalidatedAt, InvalidationReason: &reason,
	}).Error; err != nil {
		t.Fatal(err)
	}
	derivedFromID := rootID
	if err := database.Create(&storage.ApplicationBuildManifestModel{
		ID: leafID, ProjectID: projectID, WorkflowRunID: &runRef, RootManifestID: rootID, DerivedFromID: &derivedFromID,
		RootOrdinal: &ordinal, ManifestGroupKey: &group, DeliverySliceID: &sliceID, SchemaVersion: 1, ContentStore: "mongo",
		ContentRef: "conversation-" + group + "-leaf", ContentHash: conversationTestHash(group + "-leaf"),
		ManifestHash: conversationTestHash(group + "-leaf-manifest"), Status: "frozen", CreatedBy: userID, CreatedAt: now.Add(2 * time.Second),
	}).Error; err != nil {
		t.Fatal(err)
	}
	completeCount := 0
	proposal := &storage.ImplementationProposalModel{
		ID: proposalID, ProjectID: projectID, BuildManifestID: leafID,
		ExecutionSource: string(core.ImplementationSourceManualSubmission), Status: "open", Version: 1,
		ContentStore: "mongo", ContentRef: "conversation-" + group + "-proposal",
		ContentHash: conversationTestHash(group + "-proposal-content"), PayloadHash: conversationTestHash(group + "-proposal-payload"),
		OperationCount: 1, UnimplementedCount: &completeCount, BlockingDiagnosticCount: &completeCount,
		CreatedBy: userID, CreatedAt: now.Add(2 * time.Second),
	}
	testsupport.CreateBoundImplementationProposal(t, database, proposal)
	var run storage.WorkflowRunModel
	if err := database.Where("id = ? AND project_id = ?", runID, projectID).Take(&run).Error; err != nil {
		t.Fatal(err)
	}
	var runContext runtime.RunContext
	if err := json.Unmarshal(run.Context, &runContext); err != nil {
		t.Fatal(err)
	}
	if runContext.Nodes == nil {
		runContext.Nodes = map[string]runtime.NodeMetadata{}
	}
	if runContext.Slices == nil {
		runContext.Slices = map[string]runtime.SliceContext{}
	}
	nodeKey := "compiler-" + groupLabel
	sourceRef := platformdomain.ArtifactRef{
		ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: conversationTestHash(groupLabel + "-source"),
	}
	manifest := runtime.BuildManifest{
		SchemaVersion: 1, ProjectID: projectID.String(), RunID: runID.String(), ManifestGroupKey: group,
		SliceIDs: []string{sliceID}, BundleIDs: []string{rootID.String()}, Sources: []platformdomain.ArtifactRef{sourceRef},
		Constraints: json.RawMessage(`{}`), CreatedAt: now,
	}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	output, err := platformdomain.CanonicalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	runContext.Nodes[nodeKey] = runtime.NodeMetadata{Output: output}
	runContext.Slices[sliceID] = runtime.SliceContext{
		ID: sliceID, Key: strings.ToUpper(groupLabel), Title: groupLabel + " page", FanOutNodeID: "pages", Blueprint: sourceRef,
	}
	encodedContext, err := platformdomain.CanonicalJSON(runContext)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.WorkflowRunModel{}).Where("id = ?", runID).Update("context", encodedContext).Error; err != nil {
		t.Fatal(err)
	}
	groupID := uuid.MustParse(group)
	completedAt := now.Add(time.Second)
	if err := database.Create(&storage.WorkflowNodeRunModel{
		ID: groupID, RunID: runID, NodeKey: nodeKey, NodeType: string(platformdomain.NodeManifestCompiler), Status: "completed",
		Attempt: 1, AvailableAt: now, StartedAt: &now, CompletedAt: &completedAt, CreatedAt: now, UpdatedAt: completedAt,
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
