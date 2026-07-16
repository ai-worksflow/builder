package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
)

func TestLegacyWorkbenchBundleWithoutContextRevisionsKeepsHashShape(t *testing.T) {
	t.Parallel()
	bundle := WorkbenchBundle{
		ID: "legacy-bundle", ProjectID: "legacy-project",
		PageSpecRevision:  VersionRef{ArtifactID: "page", RevisionID: "page-r1", ContentHash: "sha256:page"},
		PrototypeRevision: VersionRef{ArtifactID: "prototype", RevisionID: "prototype-r1", ContentHash: "sha256:prototype"},
		BlueprintRevision: VersionRef{ArtifactID: "blueprint", RevisionID: "blueprint-r1", ContentHash: "sha256:blueprint"},
	}
	hash, err := workbenchBundleHash(bundle)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("contextRevisions")) || bytes.Contains(payload, []byte("workflowContext")) {
		t.Fatalf("empty context/workflow context changed legacy v1 payload shape: %s", payload)
	}
	var replayed WorkbenchBundle
	if err := json.Unmarshal(payload, &replayed); err != nil {
		t.Fatal(err)
	}
	replayedHash, err := workbenchBundleHash(replayed)
	if err != nil || replayedHash != hash {
		t.Fatalf("legacy bundle replay hash changed: before=%s after=%s err=%v", hash, replayedHash, err)
	}
}

func TestNewWorkbenchBundleEmptyCollectionsMarshalAsArrays(t *testing.T) {
	t.Parallel()
	bundle := WorkbenchBundle{
		RequirementRevisions:  []VersionRef{},
		ContractRevisions:     []VersionRef{},
		DesignSystemRevisions: []VersionRef{},
		ContextRevisions:      []WorkbenchContextRevision{},
		RenderedFrames:        []RenderedFrameRef{},
		Assumptions:           []string{},
		Waivers:               []string{},
	}
	payload, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"requirementRevisions", "contractRevisions", "designSystemRevisions", "contextRevisions",
		"renderedFrames", "assumptions", "waivers",
	} {
		if got := string(object[field]); got != "[]" {
			t.Errorf("new Workbench bundle field %s encoded as %s, want []", field, got)
		}
	}
}

func TestApplicationBuildContextProfileCompatibilityKeepsHistoricalBundleHash(t *testing.T) {
	t.Parallel()
	legacy := WorkbenchBundle{ID: "legacy-profile-bundle", ProjectID: "legacy-project", WorkflowContext: &ApplicationBuildContext{
		Definition:      domain.WorkflowDefinitionRef{ID: "legacy-definition", Version: 3, Hash: strings.Repeat("a", 64)},
		DeliverySliceID: "legacy-slice", RunScope: json.RawMessage(`{}`),
	}}
	before, err := workbenchBundleHash(legacy)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("executionProfile")) {
		t.Fatalf("historical Workbench payload was rewritten with an empty execution profile: %s", payload)
	}
	var replay WorkbenchBundle
	if err := json.Unmarshal(payload, &replay); err != nil {
		t.Fatal(err)
	}
	after, err := workbenchBundleHash(replay)
	if err != nil || after != before || !legacyApplicationBuildContext(*replay.WorkflowContext) {
		t.Fatalf("historical Workbench hash/profile drifted: before=%s after=%s err=%v context=%+v", before, after, err, replay.WorkflowContext)
	}
	if _, err := normalizeApplicationBuildContext(replay.WorkflowContext); err == nil {
		t.Fatal("historical profile-less context was accepted for a newly created bundle")
	}
}

func TestNewApplicationBuildContextRequiresExactProfileAtBothLevels(t *testing.T) {
	t.Parallel()
	profile := domain.WorkflowExecutionProfileRef{Version: "workflow-engine/v1", Hash: "648034d2edc8f82ac2b2959b89e181b8b67db80dadbfcd354672f386d81cbdc1"}
	contentHash := strings.Repeat("b", 64)
	source := domain.ArtifactRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: contentHash}
	manifest, err := domain.NewInputManifest(uuid.NewString(), uuid.NewString(), "workflow_start", "", nil,
		[]domain.ManifestSource{{Ref: source, Purpose: "project_brief"}}, json.RawMessage(`{}`), "workflow-input/v1", uuid.NewString(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	value := &ApplicationBuildContext{
		Definition:       domain.WorkflowDefinitionRef{ID: uuid.NewString(), Version: 4, Hash: strings.Repeat("c", 64), ExecutionProfile: profile},
		ExecutionProfile: profile, InputManifest: manifest, DeliverySliceID: uuid.NewString(), RunScope: json.RawMessage(`{}`),
		OutputContract: &domain.WorkflowOutputContract{Capability: domain.WorkflowOutputApplication, ProducedArtifactKinds: []string{"workspace"}, TerminalOutcome: domain.WorkflowOutcomeDeployment, TerminalNodeType: domain.NodePublish},
	}
	normalized, err := normalizeApplicationBuildContext(value)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(normalized)
	if bytes.Count(encoded, []byte("executionProfile")) != 2 {
		t.Fatalf("new Workbench context did not freeze nested and top-level profile refs: %s", encoded)
	}
	drifted := *value
	drifted.ExecutionProfile.Hash = strings.Repeat("d", 64)
	if _, err := normalizeApplicationBuildContext(&drifted); err == nil {
		t.Fatal("Workbench context with mismatched profile refs was accepted")
	}
}

func TestTrustedLegacyWorkflowContextIsCreationOnlyAndKeepsZeroProfileShape(t *testing.T) {
	t.Parallel()
	contentHash := strings.Repeat("b", 64)
	source := domain.ArtifactRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: contentHash}
	manifest, err := domain.NewInputManifest(
		uuid.NewString(), uuid.NewString(), "workflow_start", "", nil,
		[]domain.ManifestSource{{Ref: source, Purpose: "project_brief"}},
		json.RawMessage(`{}`), "workflow-input/v1", uuid.NewString(), time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	legacy := ApplicationBuildContext{
		Definition:      domain.WorkflowDefinitionRef{ID: uuid.NewString(), Version: 3, Hash: strings.Repeat("c", 64)},
		InputManifest:   manifest,
		DeliverySliceID: uuid.NewString(),
		RunScope:        json.RawMessage(`{}`),
		OutputContract: &domain.WorkflowOutputContract{
			Capability: domain.WorkflowOutputApplication, ProducedArtifactKinds: []string{"workspace"},
			TerminalOutcome: domain.WorkflowOutcomeDeployment, TerminalNodeType: domain.NodePublish,
		},
	}
	if _, err := normalizeApplicationBuildContext(&legacy); err == nil {
		t.Fatal("ordinary new bundle accepted a profile-less workflow context")
	}
	input := NewLegacyWorkflowWorkbenchBundleInput(CreateWorkbenchBundleInput{}, legacy)
	if !input.allowLegacyWorkflowContext || input.workflowContext == nil {
		t.Fatal("trusted legacy constructor did not carry its private compatibility provenance")
	}
	normalized, err := normalizeApplicationBuildContextForCreation(input.workflowContext, input.allowLegacyWorkflowContext)
	if err != nil {
		t.Fatalf("exact trusted legacy context was rejected before persisted-row verification: %v", err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("executionProfile")) {
		t.Fatalf("legacy creation compatibility polluted the historical payload shape: %s", encoded)
	}
	partial := legacy
	partial.ExecutionProfile = domain.WorkflowExecutionProfileRef{Version: legacyWorkflowExecutionProfileVersion, Hash: legacyWorkflowExecutionProfileHash}
	if _, err := normalizeApplicationBuildContextForCreation(&partial, true); err == nil {
		t.Fatal("legacy compatibility accepted only one of the two zero profile refs")
	}
}

func TestWorkbenchBundleHashIsStableAndExcludesHashField(t *testing.T) {
	t.Parallel()
	bundle := WorkbenchBundle{
		ID: "bundle-1", ProjectID: "project-1", PageSpecRevision: VersionRef{ArtifactID: "a", RevisionID: "r", ContentHash: "sha256:x"},
		PrototypeRevision:    VersionRef{ArtifactID: "p", RevisionID: "pr", ContentHash: "sha256:y"},
		BlueprintRevision:    VersionRef{ArtifactID: "b", RevisionID: "br", ContentHash: "sha256:z"},
		RequirementRevisions: []VersionRef{}, ContractRevisions: []VersionRef{}, DesignSystemRevisions: []VersionRef{},
		RenderedFrames: []RenderedFrameRef{}, Assumptions: []string{}, Waivers: []string{},
		CreatedBy: "user-1", CreatedAt: time.Unix(10, 0).UTC(), ManifestHash: "mutated",
	}
	first, err := workbenchBundleHash(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundle.ManifestHash = "something-else"
	second, err := workbenchBundleHash(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("manifest hash field must not recursively affect its own hash")
	}
}

func TestFormalPrototypeWorkbenchInputRequiresExactCurrentApprovedRevision(t *testing.T) {
	t.Parallel()
	artifactID := uuid.New()
	revisionID := uuid.New()
	validArtifact := storage.ArtifactModel{
		ID: artifactID, Kind: "prototype", Lifecycle: "active", LatestApprovedRevisionID: &revisionID,
	}
	validRevision := storage.ArtifactRevisionModel{
		ID: revisionID, ArtifactID: artifactID, WorkflowStatus: "approved",
	}
	validHealth := storage.ArtifactHealthModel{ArtifactID: artifactID, SyncStatus: "current"}

	tests := []struct {
		name   string
		mutate func(*storage.ArtifactModel, *storage.ArtifactRevisionModel, *storage.ArtifactHealthModel)
	}{
		{name: "exact current approved"},
		{name: "wrong kind", mutate: func(artifact *storage.ArtifactModel, _ *storage.ArtifactRevisionModel, _ *storage.ArtifactHealthModel) {
			artifact.Kind = "page_spec"
		}},
		{name: "archived artifact", mutate: func(artifact *storage.ArtifactModel, _ *storage.ArtifactRevisionModel, _ *storage.ArtifactHealthModel) {
			artifact.Lifecycle = "archived"
		}},
		{name: "missing latest approval", mutate: func(artifact *storage.ArtifactModel, _ *storage.ArtifactRevisionModel, _ *storage.ArtifactHealthModel) {
			artifact.LatestApprovedRevisionID = nil
		}},
		{name: "different latest approval", mutate: func(artifact *storage.ArtifactModel, _ *storage.ArtifactRevisionModel, _ *storage.ArtifactHealthModel) {
			other := uuid.New()
			artifact.LatestApprovedRevisionID = &other
		}},
		{name: "revision belongs to another artifact", mutate: func(_ *storage.ArtifactModel, revision *storage.ArtifactRevisionModel, _ *storage.ArtifactHealthModel) {
			revision.ArtifactID = uuid.New()
		}},
		{name: "revision is not approved", mutate: func(_ *storage.ArtifactModel, revision *storage.ArtifactRevisionModel, _ *storage.ArtifactHealthModel) {
			revision.WorkflowStatus = "changes_requested"
		}},
		{name: "missing health", mutate: func(_ *storage.ArtifactModel, _ *storage.ArtifactRevisionModel, health *storage.ArtifactHealthModel) {
			*health = storage.ArtifactHealthModel{}
		}},
		{name: "health needs sync", mutate: func(_ *storage.ArtifactModel, _ *storage.ArtifactRevisionModel, health *storage.ArtifactHealthModel) {
			health.SyncStatus = "needs_sync"
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			artifact, revision, health := validArtifact, validRevision, validHealth
			if test.mutate != nil {
				test.mutate(&artifact, &revision, &health)
			}
			got := formalPrototypeWorkbenchInput(artifact, revision, health)
			if want := test.mutate == nil; got != want {
				t.Fatalf("formal eligibility = %t, want %t: artifact=%+v revision=%+v health=%+v", got, want, artifact, revision, health)
			}
		})
	}
}

func TestFrozenPrototypePageSpecSourceUsesCanonicalLineagePurposes(t *testing.T) {
	t.Parallel()
	pageSpec := VersionRef{
		ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: "sha256:page-spec",
	}
	other := pageSpec
	other.RevisionID = uuid.NewString()
	wrongHash := pageSpec
	wrongHash.ContentHash = "sha256:different-page-spec"
	anchorID := "page-home"
	anchored := pageSpec
	anchored.AnchorID = &anchorID
	tests := []struct {
		name   string
		source frozenWorkbenchSource
		want   bool
	}{
		{name: "legacy page spec", source: frozenWorkbenchSource{Ref: pageSpec, Purpose: "page_spec", Required: true}, want: true},
		{name: "delivery slice page spec", source: frozenWorkbenchSource{Ref: pageSpec, Purpose: "delivery_slice_page_spec", Required: true}, want: true},
		{name: "workflow review node", source: frozenWorkbenchSource{Ref: pageSpec, Purpose: "workflow_node:page-spec-review", Required: true}, want: true},
		{name: "empty workflow node", source: frozenWorkbenchSource{Ref: pageSpec, Purpose: "workflow_node:", Required: true}},
		{name: "arbitrary purpose", source: frozenWorkbenchSource{Ref: pageSpec, Purpose: "reference_context", Required: true}},
		{name: "optional page spec", source: frozenWorkbenchSource{Ref: pageSpec, Purpose: "page_spec"}},
		{name: "different exact revision", source: frozenWorkbenchSource{Ref: other, Purpose: "workflow_node:page-spec-review", Required: true}},
		{name: "different content hash", source: frozenWorkbenchSource{Ref: wrongHash, Purpose: "workflow_node:page-spec-review", Required: true}},
		{name: "anchored source", source: frozenWorkbenchSource{Ref: anchored, Purpose: "workflow_node:page-spec-review", Required: true}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := hasFrozenPrototypePageSpecSource([]frozenWorkbenchSource{test.source}, pageSpec); got != test.want {
				t.Fatalf("source acceptance = %t, want %t: %+v", got, test.want, test.source)
			}
		})
	}
}

func TestRenderedFrameAssetsIgnorePrototypeSelfReportedAssets(t *testing.T) {
	t.Parallel()
	contents := newMultiBundleMemoryContentStore()
	service := &WorkbenchService{contents: contents}
	revision := storage.ArtifactRevisionModel{ID: uuid.New()}
	prototype := map[string]any{
		"states": []any{map[string]any{"id": "state-ready", "key": "ready", "title": "Ready"}},
		"breakpoints": []any{map[string]any{
			"id": "bp-desktop", "name": "Desktop", "viewportWidth": 1440, "viewportHeight": 900,
		}},
		"scene": map[string]any{"layers": map[string]any{
			"layer-root": map[string]any{
				"id": "layer-root", "name": "Canonical root", "kind": "frame", "childIds": []any{"layer-child"},
			},
			"layer-child": map[string]any{
				"id": "layer-child", "name": "Reachable child", "kind": "text", "childIds": []any{},
			},
			"layer-sibling": map[string]any{
				"id": "layer-sibling", "name": "Unreachable sibling", "kind": "text", "childIds": []any{},
			},
		}},
		"frames": []any{map[string]any{
			"id": "frame-ready-desktop", "stateId": "state-ready",
			"breakpointId": "bp-desktop", "rootLayerId": "layer-root",
		}},
		"renderedFrames": []any{map[string]any{
			"assetId": "attacker-controlled-asset", "contentHash": "sha256:attacker",
			"stateId": "state-ready", "breakpointId": "bp-desktop", "mediaType": "text/html",
		}},
	}

	rendered, pending, err := service.renderedFrameAssets(
		context.Background(), uuid.NewString(), uuid.New(), revision, prototype,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered) != 1 || len(pending) != 1 || rendered[0].AssetID == "attacker-controlled-asset" ||
		rendered[0].MediaType != "image/svg+xml" || rendered[0].AssetID != pending[0] {
		t.Fatalf("Workbench trusted self-reported renderedFrames instead of rendering canonical frames: rendered=%+v pending=%+v", rendered, pending)
	}
	stored, err := contents.Get(context.Background(), rendered[0].AssetID, rendered[0].ContentHash)
	var envelope struct {
		Data string `json:"data"`
	}
	decodeErr := json.Unmarshal(stored.Payload, &envelope)
	if err != nil || decodeErr != nil || !strings.Contains(envelope.Data, "<svg") ||
		!strings.Contains(envelope.Data, "Canonical root") || !strings.Contains(envelope.Data, "Reachable child") ||
		strings.Contains(envelope.Data, "Unreachable sibling") {
		t.Fatalf("server-rendered SVG was not stored under the returned immutable asset: payload=%s err=%v", stored.Payload, err)
	}
}

func TestRenderedFrameAssetsRejectMissingCanonicalRootLayer(t *testing.T) {
	t.Parallel()
	contents := newMultiBundleMemoryContentStore()
	service := &WorkbenchService{contents: contents}
	prototype := map[string]any{
		"states": []any{map[string]any{"id": "state-ready"}},
		"breakpoints": []any{map[string]any{
			"id": "bp-desktop", "viewportWidth": 1440, "viewportHeight": 900,
		}},
		"layers": map[string]any{
			"layer-canonical": map[string]any{"id": "layer-canonical", "kind": "frame"},
		},
		"frames": []any{map[string]any{
			"id": "frame-ready-desktop", "stateId": "state-ready",
			"breakpointId": "bp-desktop", "rootLayerId": "layer-missing",
		}},
	}

	rendered, pending, err := service.renderedFrameAssets(
		context.Background(), uuid.NewString(), uuid.New(), storage.ArtifactRevisionModel{ID: uuid.New()}, prototype,
	)
	if !errors.Is(err, ErrBlockingGate) || rendered != nil || pending != nil {
		t.Fatalf("missing canonical root layer rendered a frame: rendered=%+v pending=%+v err=%v", rendered, pending, err)
	}
	if len(contents.items) != 0 {
		t.Fatalf("invalid frame staged content before root-layer validation: %+v", contents.items)
	}
}

func TestDerivedWorkbenchBundleChangesOnlyRebaseIdentity(t *testing.T) {
	t.Parallel()

	parentID := uuid.New()
	rootID := uuid.New()
	workspaceBefore := VersionRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: "sha256:before"}
	workspaceAfter := VersionRef{ArtifactID: workspaceBefore.ArtifactID, RevisionID: uuid.NewString(), ContentHash: "sha256:after"}
	runID, manifestGroup, deliverySliceID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	parent := WorkbenchBundle{
		ID: parentID.String(), ProjectID: uuid.NewString(), RootBuildManifestID: rootID.String(),
		WorkflowRunID: &runID, ManifestGroupKey: &manifestGroup, DeliverySliceID: &deliverySliceID,
		PageSpecRevision: implementationTestVersionRef("page"), PrototypeRevision: implementationTestVersionRef("prototype"),
		BlueprintRevision:        implementationTestVersionRef("blueprint"),
		RequirementRevisions:     []VersionRef{implementationTestVersionRef("requirement")},
		ContractRevisions:        []VersionRef{implementationTestVersionRef("contract")},
		DesignSystemRevisions:    []VersionRef{implementationTestVersionRef("design")},
		CurrentWorkspaceRevision: &workspaceBefore,
		SceneGraph:               AssetRef{AssetID: "scene", ContentHash: "sha256:scene", MediaType: "application/json"},
		RenderedFrames: []RenderedFrameRef{{
			AssetRef: AssetRef{AssetID: "frame", ContentHash: "sha256:frame", MediaType: "image/svg+xml"},
			StateID:  "default", BreakpointID: "desktop",
		}},
		InteractionManifest: AssetRef{AssetID: "interactions", ContentHash: "sha256:interactions"},
		AcceptanceManifest:  AssetRef{AssetID: "acceptance", ContentHash: "sha256:acceptance"},
		Assumptions:         []string{"frozen assumption"}, Waivers: []string{},
		CreatedBy: uuid.NewString(), CreatedAt: time.Unix(10, 0).UTC(), ManifestHash: "sha256:parent",
	}
	derivedID := uuid.New()
	createdBy := uuid.NewString()
	createdAt := time.Unix(20, 0).UTC()
	derived, err := deriveWorkbenchBundle(parent, derivedID, rootID, parentID, workspaceAfter, createdBy, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if derived.ID != derivedID.String() || derived.RootBuildManifestID != rootID.String() ||
		derived.DerivedFromBuildManifestID == nil || *derived.DerivedFromBuildManifestID != parentID.String() ||
		derived.DeliverySliceID == nil || *derived.DeliverySliceID != deliverySliceID ||
		derived.CurrentWorkspaceRevision == nil || *derived.CurrentWorkspaceRevision != workspaceAfter ||
		derived.CreatedBy != createdBy || !derived.CreatedAt.Equal(createdAt) || derived.ManifestHash == "" {
		t.Fatalf("derived bundle lost exact rebase identity: %+v", derived)
	}
	normalized := derived
	normalized.ID = parent.ID
	normalized.RootBuildManifestID = parent.RootBuildManifestID
	normalized.DerivedFromBuildManifestID = parent.DerivedFromBuildManifestID
	normalized.CurrentWorkspaceRevision = parent.CurrentWorkspaceRevision
	normalized.CreatedBy = parent.CreatedBy
	normalized.CreatedAt = parent.CreatedAt
	normalized.ManifestHash = parent.ManifestHash
	if !reflect.DeepEqual(normalized, parent) {
		t.Fatalf("rebase mutated frozen sources or assets:\n got=%+v\nwant=%+v", normalized, parent)
	}
}

func TestWorkbenchBundleHashIncludesRootAndDerivedLineage(t *testing.T) {
	t.Parallel()

	bundle := WorkbenchBundle{ID: uuid.NewString(), ProjectID: uuid.NewString(), RootBuildManifestID: uuid.NewString()}
	rootHash, err := workbenchBundleHash(bundle)
	if err != nil {
		t.Fatal(err)
	}
	parentID := uuid.NewString()
	bundle.DerivedFromBuildManifestID = &parentID
	derivedHash, err := workbenchBundleHash(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if rootHash == derivedHash {
		t.Fatal("manifest hash must freeze root/direct-parent lineage")
	}
}

func TestOrderedBuildManifestLineageUsesParentLinksNotTimestamps(t *testing.T) {
	t.Parallel()

	rootID, firstID, leafID := uuid.New(), uuid.New(), uuid.New()
	root := storage.ApplicationBuildManifestModel{
		ID: rootID, RootManifestID: rootID, CreatedAt: time.Unix(30, 0),
	}
	first := storage.ApplicationBuildManifestModel{
		ID: firstID, RootManifestID: rootID, DerivedFromID: &rootID, CreatedAt: time.Unix(20, 0),
	}
	leaf := storage.ApplicationBuildManifestModel{
		ID: leafID, RootManifestID: rootID, DerivedFromID: &firstID, CreatedAt: time.Unix(10, 0),
	}

	ordered, err := orderedBuildManifestLineage(
		[]storage.ApplicationBuildManifestModel{leaf, root, first}, rootID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 3 || ordered[0].ID != rootID || ordered[1].ID != firstID || ordered[2].ID != leafID {
		t.Fatalf("lineage order follows mutable timestamps instead of parent links: %#v", ordered)
	}
}

func TestOrderedBuildManifestLineageRejectsBranchesAndDisconnectedCycles(t *testing.T) {
	t.Parallel()

	rootID, firstID, secondID := uuid.New(), uuid.New(), uuid.New()
	root := storage.ApplicationBuildManifestModel{ID: rootID, RootManifestID: rootID}
	first := storage.ApplicationBuildManifestModel{ID: firstID, RootManifestID: rootID, DerivedFromID: &rootID}
	second := storage.ApplicationBuildManifestModel{ID: secondID, RootManifestID: rootID, DerivedFromID: &rootID}
	if _, err := orderedBuildManifestLineage(
		[]storage.ApplicationBuildManifestModel{root, first, second}, rootID,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("branched lineage accepted: %v", err)
	}

	first.DerivedFromID = &secondID
	second.DerivedFromID = &firstID
	if _, err := orderedBuildManifestLineage(
		[]storage.ApplicationBuildManifestModel{root, first, second}, rootID,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("disconnected lineage cycle accepted: %v", err)
	}
}
