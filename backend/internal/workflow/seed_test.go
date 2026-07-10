package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

func TestMinimumLoopSeedIsPublishedOnlyByInstallerAndValid(t *testing.T) {
	store := NewMemoryStore(nil)
	seed := MinimumLoopSeed{DefinitionID: uuid.NewString(), VersionID: uuid.NewString(), ProjectID: uuid.NewString(), InstallerUserID: uuid.NewString(), Published: true}
	record, err := SeedMinimumLoop(context.Background(), store, seed, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Definition.Validate(); err != nil {
		t.Fatal(err)
	}
	if record.Definition.CreatedBy != seed.InstallerUserID {
		t.Fatal("seed must preserve installer user")
	}
	required := map[domain.WorkflowNodeType]bool{domain.NodeArtifactInput: false, domain.NodeAITransform: false, domain.NodeHumanEdit: false, domain.NodeReviewGate: false, domain.NodeFanOut: false, domain.NodeMerge: false, domain.NodeManifestCompiler: false, domain.NodeWorkbenchBuild: false, domain.NodeQualityGate: false, domain.NodePublish: false}
	for _, node := range record.Definition.Nodes {
		if _, exists := required[node.Type]; exists {
			required[node.Type] = true
		}
	}
	for nodeType, present := range required {
		if !present {
			t.Fatalf("minimum loop missing %s", nodeType)
		}
	}
	encoded, err := MinimumLoopDefinitionJSON(seed.DefinitionID, seed.InstallerUserID, time.Now())
	if err != nil || len(encoded) == 0 {
		t.Fatalf("expected canonical seed JSON: %v", err)
	}
}
