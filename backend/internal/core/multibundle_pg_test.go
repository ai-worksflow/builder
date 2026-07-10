package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"github.com/worksflow/builder/backend/migrations"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type multiBundleMemoryContentStore struct {
	mu       sync.Mutex
	sequence uint64
	items    map[string]content.StoredContent
}

func newMultiBundleMemoryContentStore() *multiBundleMemoryContentStore {
	return &multiBundleMemoryContentStore{items: make(map[string]content.StoredContent)}
}

func (s *multiBundleMemoryContentStore) PutPending(
	_ context.Context,
	projectID string,
	aggregateType string,
	aggregateID string,
	schemaVersion int,
	payload json.RawMessage,
) (content.Reference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	id := fmt.Sprintf("multi-bundle-%d", s.sequence)
	reference := multiBundleContentReference(id, schemaVersion, payload)
	s.items[id] = content.StoredContent{
		Reference: reference, ProjectID: projectID, AggregateType: aggregateType, AggregateID: aggregateID,
		State: content.StatePending, Payload: append(json.RawMessage(nil), payload...), CreatedAt: time.Now().UTC(),
	}
	return reference, nil
}

func (s *multiBundleMemoryContentStore) Finalize(_ context.Context, contentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.items[contentID]
	if !exists {
		return content.ErrContentNotFound
	}
	now := time.Now().UTC()
	stored.State = content.StateFinalized
	stored.FinalizedAt = &now
	s.items[contentID] = stored
	return nil
}

func (s *multiBundleMemoryContentStore) Abort(_ context.Context, contentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.items[contentID]
	if !exists {
		return content.ErrContentNotFound
	}
	stored.State = content.StateAborted
	s.items[contentID] = stored
	return nil
}

func (s *multiBundleMemoryContentStore) Get(
	_ context.Context,
	contentID string,
	expectedHash string,
) (content.StoredContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.items[contentID]
	if !exists || stored.State == content.StateAborted {
		return content.StoredContent{}, content.ErrContentNotFound
	}
	if stored.ContentHash != expectedHash {
		return content.StoredContent{}, content.ErrHashMismatch
	}
	stored.Payload = append(json.RawMessage(nil), stored.Payload...)
	return stored, nil
}

func (s *multiBundleMemoryContentStore) addFinalized(
	projectID string,
	aggregateType string,
	aggregateID string,
	schemaVersion int,
	payload json.RawMessage,
) content.Reference {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	id := fmt.Sprintf("multi-bundle-seed-%d", s.sequence)
	reference := multiBundleContentReference(id, schemaVersion, payload)
	now := time.Now().UTC()
	s.items[id] = content.StoredContent{
		Reference: reference, ProjectID: projectID, AggregateType: aggregateType, AggregateID: aggregateID,
		State: content.StateFinalized, Payload: append(json.RawMessage(nil), payload...),
		CreatedAt: now, FinalizedAt: &now,
	}
	return reference
}

func multiBundleContentReference(id string, schemaVersion int, payload json.RawMessage) content.Reference {
	digest := sha256.Sum256(payload)
	return content.Reference{
		ID: id, ContentHash: "sha256:" + hex.EncodeToString(digest[:]),
		ByteSize: int64(len(payload)), SchemaVersion: schemaVersion,
	}
}

type multiBundlePostgresFixture struct {
	database       *gorm.DB
	contents       *multiBundleMemoryContentStore
	workbench      *WorkbenchService
	implementation *ImplementationService
	projectID      uuid.UUID
	otherProjectID uuid.UUID
	ownerID        uuid.UUID
	viewerID       uuid.UUID
	otherOwnerID   uuid.UUID
	workspaceW0    VersionRef
	otherWorkspace VersionRef
	rootA          WorkbenchBundle
	rootB          WorkbenchBundle
	otherRoot      WorkbenchBundle
}

func TestMultiBundleSequentialRebasePostgres(t *testing.T) {
	database, cleanup := multiBundlePostgresDatabase(t)
	defer cleanup()

	fixture := seedMultiBundlePostgresFixture(t, database)
	ctx := context.Background()
	packageV0 := `{"name":"multi-bundle","private":true,"dependencies":{}}`
	packageA := `{"name":"multi-bundle","private":true,"dependencies":{"alpha":"1.0.0"}}`
	packageB := `{"name":"multi-bundle","private":true,"dependencies":{"alpha":"1.0.0","beta":"1.0.0"}}`

	proposalA := createReadyMultiBundleProposal(
		t, fixture.implementation, fixture.projectID, fixture.ownerID, fixture.rootA.ID,
		"package-a", packageA, hashText(packageV0),
	)
	proposalB := createReadyMultiBundleProposal(
		t, fixture.implementation, fixture.projectID, fixture.ownerID, fixture.rootB.ID,
		"package-b-old-base", packageB, hashText(packageV0),
	)
	proposalBDecisionContent := packageB
	proposalBDecision, err := fixture.implementation.Create(
		context.Background(), fixture.projectID.String(), fixture.ownerID.String(),
		CreateImplementationProposalInput{
			BuildManifestID: fixture.rootB.ID,
			Operations: []FileOperation{{
				ID: "package-b-old-base-decision", Kind: "file.upsert", Path: "package.json",
				Content: &proposalBDecisionContent, Language: "json", ExpectedHash: hashText(packageV0),
			}},
		},
	)
	if err != nil {
		t.Fatalf("create pending bundle B decision proposal: %v", err)
	}

	workspaceW1, err := fixture.implementation.Apply(
		ctx, proposalA.ID, fixture.ownerID.String(), ApplyImplementationInput{Version: proposalA.Version},
	)
	if err != nil {
		t.Fatalf("apply bundle A proposal at W0: %v", err)
	}
	assertMultiBundleWorkspaceFile(t, workspaceW1.Content, "package.json", packageA)
	workspaceW1Ref := VersionRef{
		ArtifactID: workspaceW1.ArtifactID, RevisionID: workspaceW1.ID, ContentHash: workspaceW1.ContentHash,
	}
	if _, err := fixture.workbench.GetBundleForGeneration(
		ctx, fixture.rootB.ID, fixture.ownerID.String(),
	); !errors.Is(err, ErrProposalStale) {
		t.Fatalf("bundle B generated against W0 after workspace advanced to W1: %v", err)
	}
	if _, err := fixture.implementation.Decide(
		ctx, proposalBDecision.ID, fixture.ownerID.String(), DecideImplementationInput{
			OperationID: "package-b-old-base-decision", Decision: ImplementationAccepted,
			Version: proposalBDecision.Version,
		},
	); !errors.Is(err, ErrProposalStale) {
		t.Fatalf("old bundle B proposal decision did not persist stale after W1: %v", err)
	}
	staleDecision, err := fixture.implementation.Get(ctx, proposalBDecision.ID, fixture.ownerID.String())
	if err != nil || staleDecision.Status != "stale" || staleDecision.Version != proposalBDecision.Version+1 {
		t.Fatalf("old decision proposal stale state = %#v err=%v", staleDecision, err)
	}
	lateContent := packageA
	if _, err := fixture.implementation.Create(
		ctx, fixture.projectID.String(), fixture.ownerID.String(), CreateImplementationProposalInput{
			BuildManifestID: fixture.rootA.ID,
			Operations: []FileOperation{{
				ID: "consumed-root-regeneration", Kind: "file.upsert", Path: "package.json",
				Content: &lateContent, Language: "json", ExpectedHash: hashText(packageA),
			}},
		},
	); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("consumed/applied bundle accepted proposal regeneration: %v", err)
	}
	if _, err := fixture.workbench.GetBundleForGeneration(
		ctx, fixture.rootA.ID, fixture.ownerID.String(),
	); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("consumed bundle crossed the pre-AI generation gate: %v", err)
	}
	if _, err := fixture.workbench.Rebase(
		ctx, fixture.rootA.ID, fixture.ownerID.String(),
		RebaseWorkbenchBundleInput{WorkspaceRevision: workspaceW1Ref},
	); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("consumed bundle accepted another rebase: %v", err)
	}

	if _, err := fixture.implementation.Apply(
		ctx, proposalB.ID, fixture.ownerID.String(), ApplyImplementationInput{Version: proposalB.Version},
	); !errors.Is(err, ErrProposalStale) {
		t.Fatalf("bundle B proposal with W0 base must become stale after A applies: %v", err)
	}
	staleB, err := fixture.implementation.Get(ctx, proposalB.ID, fixture.ownerID.String())
	if err != nil {
		t.Fatalf("stale bundle B proposal must remain readable: %v", err)
	}
	if staleB.Status != "stale" || staleB.Version != proposalB.Version+1 || staleB.BaseWorkspaceRevision == nil ||
		staleB.BaseWorkspaceRevision.RevisionID != fixture.workspaceW0.RevisionID {
		t.Fatalf("unexpected persisted stale proposal: %#v", staleB)
	}

	staleLineage, err := fixture.workbench.GetLineageState(ctx, fixture.rootB.ID, fixture.ownerID.String())
	if err != nil {
		t.Fatalf("read bundle B lineage after stale proposal: %v", err)
	}
	assertMultiBundleStaleLineageState(t, staleLineage, fixture.rootB.ID, workspaceW1Ref)

	t.Run("rebase guards", func(t *testing.T) {
		input := RebaseWorkbenchBundleInput{WorkspaceRevision: workspaceW1Ref}
		if _, err := fixture.workbench.Rebase(ctx, fixture.rootB.ID, fixture.viewerID.String(), input); !errors.Is(err, ErrForbidden) {
			t.Fatalf("viewer rebase error = %v, want ErrForbidden", err)
		}
		if _, err := fixture.workbench.Rebase(
			ctx, fixture.rootB.ID, fixture.ownerID.String(),
			RebaseWorkbenchBundleInput{WorkspaceRevision: fixture.otherWorkspace},
		); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-project workspace rebase error = %v, want ErrNotFound", err)
		}
		wrongHash := workspaceW1Ref
		wrongHash.ContentHash = "sha256:" + strings.Repeat("f", 64)
		if _, err := fixture.workbench.Rebase(
			ctx, fixture.rootB.ID, fixture.ownerID.String(),
			RebaseWorkbenchBundleInput{WorkspaceRevision: wrongHash},
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("wrong workspace hash rebase error = %v, want ErrConflict", err)
		}
		if _, err := fixture.workbench.GetLineageState(
			ctx, fixture.otherRoot.ID, fixture.ownerID.String(),
		); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-project lineage state error = %v, want ErrNotFound", err)
		}
	})

	rebasedB, err := fixture.workbench.Rebase(
		ctx, fixture.rootB.ID, fixture.ownerID.String(),
		RebaseWorkbenchBundleInput{WorkspaceRevision: workspaceW1Ref},
	)
	if err != nil {
		t.Fatalf("rebase bundle B at W1: %v", err)
	}
	reusedB, err := fixture.workbench.Rebase(
		ctx, fixture.rootB.ID, fixture.ownerID.String(),
		RebaseWorkbenchBundleInput{WorkspaceRevision: workspaceW1Ref},
	)
	if err != nil {
		t.Fatalf("repeat rebase across a distinct idempotency request: %v", err)
	}
	if reusedB.ID != rebasedB.ID || reusedB.ManifestHash != rebasedB.ManifestHash {
		t.Fatalf("rebase retry did not deterministically reuse the frozen bundle: first=%#v retry=%#v", rebasedB, reusedB)
	}
	var rebasedCount int64
	if err := fixture.database.Model(&storage.ApplicationBuildManifestModel{}).Where(
		"root_manifest_id = ? AND workspace_revision_id = ?", uuid.MustParse(fixture.rootB.ID), uuid.MustParse(workspaceW1.ID),
	).Count(&rebasedCount).Error; err != nil {
		t.Fatal(err)
	}
	if rebasedCount != 1 {
		t.Fatalf("duplicate rebase persisted %d W1 bundles, want 1", rebasedCount)
	}

	proposalBRebased := createReadyMultiBundleProposal(
		t, fixture.implementation, fixture.projectID, fixture.ownerID, rebasedB.ID,
		"package-b-rebased", packageB, hashText(packageA),
	)
	workspaceW2, err := fixture.implementation.Apply(
		ctx, proposalBRebased.ID, fixture.ownerID.String(), ApplyImplementationInput{Version: proposalBRebased.Version},
	)
	if err != nil {
		t.Fatalf("apply rebased bundle B proposal at W1: %v", err)
	}
	if workspaceW2.ParentRevisionID == nil || *workspaceW2.ParentRevisionID != workspaceW1.ID {
		t.Fatalf("workspace W2 parent = %v, want W1 %s", workspaceW2.ParentRevisionID, workspaceW1.ID)
	}
	assertMultiBundleWorkspaceFile(t, workspaceW2.Content, "package.json", packageB)
	workspaceW2Ref := VersionRef{
		ArtifactID: workspaceW2.ArtifactID, RevisionID: workspaceW2.ID, ContentHash: workspaceW2.ContentHash,
	}
	if _, err := fixture.workbench.Rebase(
		ctx, fixture.rootB.ID, fixture.ownerID.String(),
		RebaseWorkbenchBundleInput{WorkspaceRevision: workspaceW2Ref},
	); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("applied root lineage accepted a second derived bundle: %v", err)
	}

	recoveredLineage, err := fixture.workbench.GetLineageState(ctx, fixture.rootB.ID, fixture.ownerID.String())
	if err != nil {
		t.Fatalf("refresh bundle B lineage after rebased apply: %v", err)
	}
	assertMultiBundleRecoveredLineageState(
		t, recoveredLineage, fixture.rootB.ID, rebasedB.ID, proposalBRebased.ID,
		workspaceW2Ref,
	)
	refreshedFromDerived, err := fixture.workbench.GetLineageState(ctx, rebasedB.ID, fixture.ownerID.String())
	if err != nil {
		t.Fatalf("refresh lineage through the active derived bundle: %v", err)
	}
	assertMultiBundleRecoveredLineageState(
		t, refreshedFromDerived, fixture.rootB.ID, rebasedB.ID, proposalBRebased.ID,
		workspaceW2Ref,
	)
}

func TestWorkflowManifestCompilerRetryAndGroupIsolationPostgres(t *testing.T) {
	database, cleanup := multiBundlePostgresDatabase(t)
	defer cleanup()

	fixture := seedMultiBundlePostgresFixture(t, database)
	ctx := context.Background()
	now := time.Now().UTC()
	definitionID := uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionModel{
		ID: definitionID, ProjectID: &fixture.projectID, WorkflowKey: "manifest-groups-" + uuid.NewString(),
		Title: "Manifest groups", Lifecycle: "active", CreatedBy: fixture.ownerID,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	definitionVersionID := uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionVersionModel{
		ID: definitionVersionID, DefinitionID: definitionID, Version: 1, SchemaVersion: 1,
		Content: json.RawMessage(`{"nodes":[],"edges":[]}`), ContentHash: hashText("manifest-groups"),
		ValidationReport: json.RawMessage(`{}`), Published: true, CreatedBy: fixture.ownerID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	runID := uuid.New()
	if err := database.Create(&storage.WorkflowRunModel{
		ID: runID, ProjectID: fixture.projectID, DefinitionVersionID: definitionVersionID, Status: "running",
		Scope: json.RawMessage(`{}`), Context: json.RawMessage(`{}`), StartedBy: fixture.ownerID,
		StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	groupIDs := []uuid.UUID{uuid.New(), uuid.New()}
	for index, groupID := range groupIDs {
		if err := database.Create(&storage.WorkflowNodeRunModel{
			ID: groupID, RunID: runID, NodeKey: fmt.Sprintf("compile-%d", index),
			NodeType: "manifest_compiler", Status: "running", Attempt: 1,
			AvailableAt: now, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	sliceIDs := []string{uuid.NewString(), uuid.NewString()}
	workflowRoots := make([]WorkbenchBundle, 0, len(groupIDs))
	for index, groupID := range groupIDs {
		workflowRoots = append(workflowRoots, seedMultiBundleWorkflowRoot(
			t, database, fixture.contents, fixture.rootA, runID, groupID.String(), 0,
			sliceIDs[index], now.Add(time.Duration(index)*time.Second),
		))
	}
	for _, root := range workflowRoots {
		if _, err := fixture.workbench.GetBundleForGeneration(ctx, root.ID, fixture.ownerID.String()); err != nil {
			t.Fatalf("same-run compiler group ordinal zero blocked another group: %v", err)
		}
	}

	exactGroup := groupIDs[0].String()
	exactRun := runID.String()
	ordinal := 0
	reused, err := fixture.workbench.CreateBundle(
		ctx, fixture.projectID.String(), fixture.ownerID.String(), CreateWorkbenchBundleInput{
			PrototypeRevision: workflowRoots[0].PrototypeRevision, WorkflowRunID: &exactRun,
			ManifestGroupKey: &exactGroup, RootOrdinal: &ordinal, DeliverySliceID: &sliceIDs[0],
		},
	)
	if err != nil {
		t.Fatalf("exact compiler retry did not reuse frozen root: %v", err)
	}
	if reused.ID != workflowRoots[0].ID || reused.ManifestHash != workflowRoots[0].ManifestHash {
		t.Fatalf("compiler retry returned another root: got=%#v want=%#v", reused, workflowRoots[0])
	}
	mismatchedPrototype := workflowRoots[0].PrototypeRevision
	mismatchedPrototype.RevisionID = uuid.NewString()
	if _, err := fixture.workbench.CreateBundle(
		ctx, fixture.projectID.String(), fixture.ownerID.String(), CreateWorkbenchBundleInput{
			PrototypeRevision: mismatchedPrototype, WorkflowRunID: &exactRun,
			ManifestGroupKey: &exactGroup, RootOrdinal: &ordinal, DeliverySliceID: &sliceIDs[0],
		},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("compiler retry reused root for a different immutable input: %v", err)
	}

	foreignRunID, foreignGroupID := uuid.New(), uuid.New()
	if err := database.Create(&storage.WorkflowRunModel{
		ID: foreignRunID, ProjectID: fixture.otherProjectID, DefinitionVersionID: definitionVersionID,
		Status: "running", Scope: json.RawMessage(`{}`), Context: json.RawMessage(`{}`),
		StartedBy: fixture.otherOwnerID, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.WorkflowNodeRunModel{
		ID: foreignGroupID, RunID: foreignRunID, NodeKey: "foreign-compile",
		NodeType: "manifest_compiler", Status: "running", Attempt: 1,
		AvailableAt: now, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	foreignRun, foreignGroup := foreignRunID.String(), foreignGroupID.String()
	if _, err := fixture.workbench.CreateBundle(
		ctx, fixture.projectID.String(), fixture.ownerID.String(), CreateWorkbenchBundleInput{
			PrototypeRevision: fixture.rootA.PrototypeRevision, WorkflowRunID: &foreignRun,
			ManifestGroupKey: &foreignGroup, RootOrdinal: &ordinal, DeliverySliceID: &sliceIDs[0],
		},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign-project workflow run could squat a local compiler ordinal: %v", err)
	}
}

func TestWorkbenchConcurrentRebaseStaysLinearPostgres(t *testing.T) {
	database, cleanup := multiBundlePostgresDatabase(t)
	defer cleanup()

	fixture := seedMultiBundlePostgresFixture(t, database)
	ctx := context.Background()
	workspaceW1 := advanceMultiBundleWorkspaceRevision(
		t, database, fixture.contents, fixture.projectID, fixture.ownerID, fixture.workspaceW0, "linear-w1",
	)
	inputW1 := RebaseWorkbenchBundleInput{WorkspaceRevision: workspaceW1}

	const callers = 8
	results := make(chan WorkbenchBundle, callers)
	errorsChannel := make(chan error, callers)
	var waitGroup sync.WaitGroup
	for index := 0; index < callers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			bundle, err := fixture.workbench.Rebase(
				ctx, fixture.rootB.ID, fixture.ownerID.String(), inputW1,
			)
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- bundle
		}()
	}
	waitGroup.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatalf("concurrent same-parent rebase failed: %v", err)
	}
	d1IDs := map[string]bool{}
	var derivedW1 WorkbenchBundle
	for bundle := range results {
		d1IDs[bundle.ID] = true
		derivedW1 = bundle
	}
	if len(d1IDs) != 1 || derivedW1.ID == "" {
		t.Fatalf("same-parent retries produced sibling bundles: %#v", d1IDs)
	}
	rootBID := uuid.MustParse(fixture.rootB.ID)
	var rootChildren int64
	if err := database.Model(&storage.ApplicationBuildManifestModel{}).
		Where("derived_from_id = ?", rootBID).Count(&rootChildren).Error; err != nil {
		t.Fatal(err)
	}
	if rootChildren != 1 {
		t.Fatalf("root has %d direct children after concurrent rebase, want 1", rootChildren)
	}
	retryW1, err := fixture.workbench.Rebase(
		ctx, fixture.rootB.ID, fixture.ownerID.String(), inputW1,
	)
	if err != nil || retryW1.ID != derivedW1.ID {
		t.Fatalf("same-parent retry did not reuse d1: got=%s err=%v want=%s", retryW1.ID, err, derivedW1.ID)
	}

	workspaceW2 := advanceMultiBundleWorkspaceRevision(
		t, database, fixture.contents, fixture.projectID, fixture.ownerID, workspaceW1, "linear-w2",
	)
	inputW2 := RebaseWorkbenchBundleInput{WorkspaceRevision: workspaceW2}
	derivedW2, err := fixture.workbench.Rebase(
		ctx, derivedW1.ID, fixture.ownerID.String(), inputW2,
	)
	if err != nil {
		t.Fatalf("rebase d1 to d2: %v", err)
	}
	retryW2, err := fixture.workbench.Rebase(
		ctx, derivedW1.ID, fixture.ownerID.String(), inputW2,
	)
	if err != nil || retryW2.ID != derivedW2.ID {
		t.Fatalf("d1 retry did not reuse d2: got=%s err=%v want=%s", retryW2.ID, err, derivedW2.ID)
	}
	if _, err := fixture.workbench.Rebase(
		ctx, fixture.rootB.ID, fixture.ownerID.String(), inputW2,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("root created a sibling that skipped d1: %v", err)
	}
	if _, err := fixture.workbench.GetBundleForGeneration(
		ctx, derivedW1.ID, fixture.ownerID.String(),
	); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("invalidated d1 remained generatable after d2: %v", err)
	}

	d1ID := uuid.MustParse(derivedW1.ID)
	d2ID := uuid.MustParse(derivedW2.ID)
	var d1Children, d2Children int64
	if err := database.Model(&storage.ApplicationBuildManifestModel{}).
		Where("derived_from_id = ?", d1ID).Count(&d1Children).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ApplicationBuildManifestModel{}).
		Where("derived_from_id = ?", d2ID).Count(&d2Children).Error; err != nil {
		t.Fatal(err)
	}
	if d1Children != 1 || d2Children != 0 {
		t.Fatalf("lineage direct-child counts root/d1/d2 = %d/%d/%d, want 1/1/0", rootChildren, d1Children, d2Children)
	}
	state, err := fixture.workbench.GetLineageState(
		ctx, fixture.rootB.ID, fixture.ownerID.String(),
	)
	if err != nil {
		t.Fatalf("load root-d1-d2 lineage state: %v", err)
	}
	if state.ActiveBundle.ID != derivedW2.ID || len(state.Lineage) != 3 ||
		state.Lineage[0].BundleID != fixture.rootB.ID || state.Lineage[0].Status != "invalidated" ||
		state.Lineage[1].BundleID != derivedW1.ID || state.Lineage[1].Status != "invalidated" ||
		state.Lineage[2].BundleID != derivedW2.ID || state.Lineage[2].Status != "frozen" {
		t.Fatalf("lineage state did not select structural d2 leaf: %#v", state)
	}
	assertMultiBundleVersionRef(t, state.CurrentWorkspaceRevision, workspaceW2, "linear lineage current workspace")
}

func advanceMultiBundleWorkspaceRevision(
	t *testing.T,
	database *gorm.DB,
	contents *multiBundleMemoryContentStore,
	projectID uuid.UUID,
	actorID uuid.UUID,
	parent VersionRef,
	marker string,
) VersionRef {
	t.Helper()
	artifactID := uuid.MustParse(parent.ArtifactID)
	parentRevisionID := uuid.MustParse(parent.RevisionID)
	var artifact storage.ArtifactModel
	if err := database.Where("id = ? AND project_id = ? AND kind = 'workspace'", artifactID, projectID).
		Take(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.LatestApprovedRevisionID == nil || *artifact.LatestApprovedRevisionID != parentRevisionID {
		t.Fatalf("workspace parent %s is not current before %s", parent.RevisionID, marker)
	}
	payload := json.RawMessage(fmt.Sprintf(`{
		"schemaVersion":1,
		"id":"workspace-main",
		"name":"Application Workspace",
		"revision":1,
		"files":[{"path":"package.json","content":"{\"name\":\"%s\"}","language":"json","revision":1,"dirty":false}],
		"checkpoints":[],"branches":[],"activeBranchId":"main","diagnostics":[]
	}`, marker))
	revisionID := uuid.New()
	stored := contents.addFinalized(projectID.String(), "artifact_revision", revisionID.String(), 1, payload)
	now := time.Now().UTC()
	var latest uint64
	if err := database.Model(&storage.ArtifactRevisionModel{}).Where("artifact_id = ?", artifactID).
		Select("COALESCE(MAX(revision_number), 0)").Scan(&latest).Error; err != nil {
		t.Fatal(err)
	}
	revision := storage.ArtifactRevisionModel{
		ID: revisionID, ArtifactID: artifactID, RevisionNumber: latest + 1, ParentRevisionID: &parentRevisionID,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: stored.ID, ContentHash: stored.ContentHash,
		ByteSize: stored.ByteSize, WorkflowStatus: "approved", ChangeSource: "system",
		ChangeSummary: "Advance workspace for linear rebase canary", CreatedBy: actorID,
		CreatedAt: now, ApprovedAt: &now,
	}
	if err := database.Create(&revision).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).Updates(map[string]any{
		"latest_revision_id": revisionID, "latest_approved_revision_id": revisionID,
		"version": gorm.Expr("version + 1"), "updated_at": now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return VersionRef{ArtifactID: artifactID.String(), RevisionID: revisionID.String(), ContentHash: stored.ContentHash}
}

func seedMultiBundleWorkflowRoot(
	t *testing.T,
	database *gorm.DB,
	contents *multiBundleMemoryContentStore,
	template WorkbenchBundle,
	runID uuid.UUID,
	manifestGroupKey string,
	rootOrdinal int,
	deliverySliceID string,
	createdAt time.Time,
) WorkbenchBundle {
	t.Helper()
	id := uuid.New()
	runIDString := runID.String()
	bundle := template
	bundle.ID = id.String()
	bundle.RootBuildManifestID = id.String()
	bundle.DerivedFromBuildManifestID = nil
	bundle.WorkflowRunID = &runIDString
	bundle.ManifestGroupKey = &manifestGroupKey
	bundle.DeliverySliceID = &deliverySliceID
	bundle.CreatedAt = createdAt
	bundle.ManifestHash = ""
	manifestHash, err := workbenchBundleHash(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundle.ManifestHash = manifestHash
	payload, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	stored := contents.addFinalized(bundle.ProjectID, "application_build_manifest", bundle.ID, 1, payload)
	workspaceRevisionID := uuid.MustParse(bundle.CurrentWorkspaceRevision.RevisionID)
	modelRunID := runID
	modelGroupKey := manifestGroupKey
	modelOrdinal := rootOrdinal
	model := storage.ApplicationBuildManifestModel{
		ID: id, ProjectID: uuid.MustParse(bundle.ProjectID), WorkflowRunID: &modelRunID,
		RootManifestID: id, WorkspaceRevisionID: &workspaceRevisionID,
		RootOrdinal: &modelOrdinal, ManifestGroupKey: &modelGroupKey,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: stored.ID,
		ContentHash: stored.ContentHash, ManifestHash: manifestHash, Status: "frozen",
		CreatedBy: uuid.MustParse(bundle.CreatedBy), CreatedAt: createdAt,
	}
	if err := database.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	return bundle
}

func createReadyMultiBundleProposal(
	t *testing.T,
	service *ImplementationService,
	projectID uuid.UUID,
	actorID uuid.UUID,
	bundleID string,
	operationID string,
	newContent string,
	expectedHash string,
) ImplementationProposal {
	t.Helper()
	proposal, err := service.Create(
		context.Background(), projectID.String(), actorID.String(),
		CreateImplementationProposalInput{
			BuildManifestID: bundleID,
			Operations: []FileOperation{{
				ID: operationID, Kind: "file.upsert", Path: "package.json", Content: &newContent,
				Language: "json", ExpectedHash: expectedHash,
			}},
		},
	)
	if err != nil {
		t.Fatalf("create implementation proposal for bundle %s: %v", bundleID, err)
	}
	proposal, err = service.Decide(
		context.Background(), proposal.ID, actorID.String(),
		DecideImplementationInput{
			OperationID: operationID, Decision: ImplementationAccepted, Version: proposal.Version,
		},
	)
	if err != nil {
		t.Fatalf("accept implementation operation %s: %v", operationID, err)
	}
	if proposal.Status != "ready" {
		t.Fatalf("accepted proposal status = %q, want ready", proposal.Status)
	}
	return proposal
}

func seedMultiBundlePostgresFixture(t *testing.T, database *gorm.DB) multiBundlePostgresFixture {
	t.Helper()
	now := time.Now().UTC().Add(-time.Hour)
	ownerID := seedMultiBundleUser(t, database, "owner")
	viewerID := seedMultiBundleUser(t, database, "viewer")
	otherOwnerID := seedMultiBundleUser(t, database, "other-owner")
	projectID := seedMultiBundleProject(t, database, ownerID, "sequential")
	otherProjectID := seedMultiBundleProject(t, database, otherOwnerID, "other")
	seedMultiBundleMembership(t, database, projectID, ownerID, RoleOwner)
	seedMultiBundleMembership(t, database, projectID, viewerID, RoleViewer)
	seedMultiBundleMembership(t, database, otherProjectID, otherOwnerID, RoleOwner)

	contents := newMultiBundleMemoryContentStore()
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	workbench, err := NewWorkbenchService(database, contents, access)
	if err != nil {
		t.Fatal(err)
	}
	implementation, err := NewImplementationService(database, contents, access)
	if err != nil {
		t.Fatal(err)
	}
	workbench.now = func() time.Time { return now.Add(30 * time.Minute) }
	implementation.now = func() time.Time { return now.Add(20 * time.Minute) }

	pageSpec := seedMultiBundleApprovedRevision(t, database, contents, projectID, ownerID, "page_spec", json.RawMessage(`{"pages":[]}`), nil)
	prototype := seedMultiBundleApprovedRevision(t, database, contents, projectID, ownerID, "prototype", json.RawMessage(`{"frames":[]}`), nil)
	requirements := seedMultiBundleApprovedRevision(t, database, contents, projectID, ownerID, "product_requirements", json.RawMessage(`{"requirements":[]}`), nil)
	blueprint := seedMultiBundleApprovedRevision(t, database, contents, projectID, ownerID, "blueprint", json.RawMessage(`{"nodes":[]}`), nil)
	workspacePayload := json.RawMessage(`{
		"schemaVersion":1,
		"id":"workspace-main",
		"name":"Application Workspace",
		"revision":0,
		"files":[{"path":"package.json","content":"{\"name\":\"multi-bundle\",\"private\":true,\"dependencies\":{}}","language":"json","revision":1,"dirty":false}],
		"checkpoints":[],"branches":[],"activeBranchId":"main","diagnostics":[]
	}`)
	workspaceW0 := seedMultiBundleApprovedRevision(t, database, contents, projectID, ownerID, "workspace", workspacePayload, nil)
	otherWorkspace := seedMultiBundleApprovedRevision(
		t, database, contents, otherProjectID, otherOwnerID, "workspace", workspacePayload, nil,
	)

	rootA := seedMultiBundleRoot(
		t, database, contents, projectID, ownerID, now, workspaceW0,
		pageSpec, prototype, requirements, blueprint,
	)
	rootB := seedMultiBundleRoot(
		t, database, contents, projectID, ownerID, now.Add(time.Second), workspaceW0,
		pageSpec, prototype, requirements, blueprint,
	)
	otherRoot := seedMultiBundleRoot(
		t, database, contents, otherProjectID, otherOwnerID, now, otherWorkspace,
		VersionRef{}, VersionRef{}, VersionRef{}, VersionRef{},
	)

	return multiBundlePostgresFixture{
		database: database, contents: contents, workbench: workbench, implementation: implementation,
		projectID: projectID, otherProjectID: otherProjectID, ownerID: ownerID, viewerID: viewerID,
		otherOwnerID: otherOwnerID, workspaceW0: workspaceW0, otherWorkspace: otherWorkspace,
		rootA: rootA, rootB: rootB, otherRoot: otherRoot,
	}
}

func seedMultiBundleUser(t *testing.T, database *gorm.DB, label string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	now := time.Now().UTC()
	if err := database.Create(&storage.UserModel{
		ID: id, Email: label + "-" + uuid.NewString() + "@example.com", DisplayName: label,
		PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func seedMultiBundleProject(t *testing.T, database *gorm.DB, ownerID uuid.UUID, label string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	now := time.Now().UTC()
	if err := database.Create(&storage.ProjectModel{
		ID: id, Name: "Multi bundle " + label, Lifecycle: "active", Version: 1,
		CreatedBy: ownerID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func seedMultiBundleMembership(
	t *testing.T,
	database *gorm.DB,
	projectID uuid.UUID,
	userID uuid.UUID,
	role Role,
) {
	t.Helper()
	now := time.Now().UTC()
	if err := database.Create(&storage.ProjectMemberModel{
		ProjectID: projectID, UserID: userID, Role: string(role), JoinedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func seedMultiBundleApprovedRevision(
	t *testing.T,
	database *gorm.DB,
	contents *multiBundleMemoryContentStore,
	projectID uuid.UUID,
	userID uuid.UUID,
	kind string,
	payload json.RawMessage,
	parentRevisionID *uuid.UUID,
) VersionRef {
	t.Helper()
	now := time.Now().UTC().Add(-time.Hour)
	artifactID := uuid.New()
	revisionID := uuid.New()
	stored := contents.addFinalized(projectID.String(), "artifact_revision", revisionID.String(), 1, payload)
	artifact := storage.ArtifactModel{
		ID: artifactID, ProjectID: projectID, Kind: kind,
		ArtifactKey: strings.ToUpper(strings.ReplaceAll(kind, "_", "-")) + "-" + strings.ToUpper(artifactID.String()[:8]),
		Title:       kind, Lifecycle: "active", Version: 1, CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	revision := storage.ArtifactRevisionModel{
		ID: revisionID, ArtifactID: artifactID, RevisionNumber: 1, ParentRevisionID: parentRevisionID,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: stored.ID, ContentHash: stored.ContentHash,
		ByteSize: stored.ByteSize, WorkflowStatus: "approved", ChangeSource: "human",
		ChangeSummary: "multi-bundle seed", CreatedBy: userID, CreatedAt: now, ApprovedAt: &now,
	}
	if err := database.Create(&revision).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).Updates(map[string]any{
		"latest_revision_id": revisionID, "latest_approved_revision_id": revisionID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return VersionRef{ArtifactID: artifactID.String(), RevisionID: revisionID.String(), ContentHash: stored.ContentHash}
}

func seedMultiBundleRoot(
	t *testing.T,
	database *gorm.DB,
	contents *multiBundleMemoryContentStore,
	projectID uuid.UUID,
	userID uuid.UUID,
	createdAt time.Time,
	workspace VersionRef,
	pageSpec VersionRef,
	prototype VersionRef,
	requirements VersionRef,
	blueprint VersionRef,
) WorkbenchBundle {
	t.Helper()
	id := uuid.New()
	bundle := WorkbenchBundle{
		ID: id.String(), ProjectID: projectID.String(), RootBuildManifestID: id.String(),
		PageSpecRevision: pageSpec, PrototypeRevision: prototype,
		RequirementRevisions: []VersionRef{requirements}, BlueprintRevision: blueprint,
		CurrentWorkspaceRevision: &workspace, Assumptions: []string{}, Waivers: []string{},
		CreatedBy: userID.String(), CreatedAt: createdAt,
	}
	manifestHash, err := workbenchBundleHash(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundle.ManifestHash = manifestHash
	payload, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	stored := contents.addFinalized(projectID.String(), "application_build_manifest", id.String(), 1, payload)
	workspaceRevisionID := uuid.MustParse(workspace.RevisionID)
	model := storage.ApplicationBuildManifestModel{
		ID: id, ProjectID: projectID, RootManifestID: id, WorkspaceRevisionID: &workspaceRevisionID,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: stored.ID, ContentHash: stored.ContentHash,
		ManifestHash: manifestHash, Status: "frozen", CreatedBy: userID, CreatedAt: createdAt,
	}
	if err := database.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	return bundle
}

func assertMultiBundleWorkspaceFile(t *testing.T, payload json.RawMessage, filePath string, want string) {
	t.Helper()
	var workspace map[string]any
	if err := json.Unmarshal(payload, &workspace); err != nil {
		t.Fatal(err)
	}
	for _, file := range objectSlice(workspace["files"]) {
		if firstString(file, "path") == filePath {
			if got := firstString(file, "content"); got != want {
				t.Fatalf("workspace file %s = %q, want %q", filePath, got, want)
			}
			return
		}
	}
	t.Fatalf("workspace file %s was not found", filePath)
}

func assertMultiBundleStaleLineageState(
	t *testing.T,
	state WorkbenchLineageState,
	rootID string,
	currentWorkspace VersionRef,
) {
	t.Helper()
	if state.RootBundleID != rootID || state.ActiveBundle.ID != rootID {
		t.Fatalf("stale lineage root/active bundle = %q/%q, want %q/%q", state.RootBundleID, state.ActiveBundle.ID, rootID, rootID)
	}
	if state.CurrentProposal != nil {
		t.Fatalf("stale proposal leaked into resumable lineage state: %#v", state.CurrentProposal)
	}
	assertMultiBundleVersionRef(t, state.CurrentWorkspaceRevision, currentWorkspace, "stale lineage current workspace")
	if len(state.Lineage) != 1 || state.Lineage[0].BundleID != rootID || state.Lineage[0].LatestProposal != nil {
		t.Fatalf("unexpected stale lineage state: %#v", state.Lineage)
	}
	if state.Lineage[0].Status != "frozen" {
		t.Fatalf("stale root bundle status = %q, want frozen", state.Lineage[0].Status)
	}
}

func assertMultiBundleRecoveredLineageState(
	t *testing.T,
	state WorkbenchLineageState,
	rootID string,
	activeBundleID string,
	proposalID string,
	currentWorkspace VersionRef,
) {
	t.Helper()
	if state.RootBundleID != rootID || state.ActiveBundle.ID != activeBundleID {
		t.Fatalf("recovered lineage root/active bundle = %q/%q, want %q/%q", state.RootBundleID, state.ActiveBundle.ID, rootID, activeBundleID)
	}
	if state.CurrentProposal == nil || state.CurrentProposal.ID != proposalID || state.CurrentProposal.Status != "applied" {
		t.Fatalf("recovered lineage current proposal = %#v, want applied %s", state.CurrentProposal, proposalID)
	}
	assertMultiBundleVersionRef(t, state.CurrentWorkspaceRevision, currentWorkspace, "recovered lineage current workspace")
	if len(state.Lineage) != 2 {
		t.Fatalf("recovered lineage entries = %d, want 2: %#v", len(state.Lineage), state.Lineage)
	}
	rootEntry := state.Lineage[0]
	derivedEntry := state.Lineage[1]
	if rootEntry.BundleID != rootID || rootEntry.DerivedFromBuildManifestID != nil || rootEntry.LatestProposal != nil {
		t.Fatalf("unexpected recovered root entry: %#v", rootEntry)
	}
	if derivedEntry.BundleID != activeBundleID || derivedEntry.DerivedFromBuildManifestID == nil ||
		*derivedEntry.DerivedFromBuildManifestID != rootID || derivedEntry.Status != "consumed" ||
		derivedEntry.LatestProposal == nil || derivedEntry.LatestProposal.ID != proposalID ||
		derivedEntry.LatestProposal.Status != "applied" {
		t.Fatalf("unexpected recovered derived entry: %#v", derivedEntry)
	}
}

func assertMultiBundleVersionRef(t *testing.T, got *VersionRef, want VersionRef, label string) {
	t.Helper()
	if got == nil || got.ArtifactID != want.ArtifactID || got.RevisionID != want.RevisionID ||
		got.ContentHash != want.ContentHash || got.AnchorID != nil {
		t.Fatalf("%s = %#v, want %#v", label, got, want)
	}
}

func multiBundlePostgresDatabase(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	base, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := "multi_bundle_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	testDSN := multiBundlePostgresDSNWithSearchPath(t, dsn, schema)
	database, err := gorm.Open(gormpostgres.Open(testDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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

func multiBundlePostgresDSNWithSearchPath(t *testing.T, dsn string, schema string) string {
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
