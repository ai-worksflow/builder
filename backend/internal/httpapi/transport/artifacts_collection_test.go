package transport

import (
	"encoding/json"
	"testing"

	"github.com/worksflow/builder/backend/internal/core"
)

func TestCollectionArtifactInputBindsBlueprintToRequirementBaseline(t *testing.T) {
	reference := core.VersionRef{
		ArtifactID:  "11111111-1111-4111-8111-111111111111",
		RevisionID:  "22222222-2222-4222-8222-222222222222",
		ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}

	input, err := collectionArtifactInput("blueprints", createCollectionArtifactInput{
		Title:               "Video platform Blueprint",
		Content:             json.RawMessage(`{"nodes":[],"edges":[]}`),
		RequirementVersions: []core.VersionRef{reference},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(input.SourceVersions) != 1 {
		t.Fatalf("source versions = %#v", input.SourceVersions)
	}
	source := input.SourceVersions[0]
	if source.Ref != reference || source.Purpose != "requirement_baseline" || !source.Required {
		t.Fatalf("Blueprint Requirement Baseline source = %#v", source)
	}
}
