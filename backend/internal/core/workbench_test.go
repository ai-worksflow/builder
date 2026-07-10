package core

import (
	"testing"
	"time"
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
