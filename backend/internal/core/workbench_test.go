package core

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
)

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

func TestDerivedWorkbenchBundleChangesOnlyRebaseIdentity(t *testing.T) {
	t.Parallel()

	parentID := uuid.New()
	rootID := uuid.New()
	workspaceBefore := VersionRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: "sha256:before"}
	workspaceAfter := VersionRef{ArtifactID: workspaceBefore.ArtifactID, RevisionID: uuid.NewString(), ContentHash: "sha256:after"}
	parent := WorkbenchBundle{
		ID: parentID.String(), ProjectID: uuid.NewString(), RootBuildManifestID: rootID.String(),
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
