package conversation

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/worksflow/builder/backend/internal/core"
)

func TestStartDefinitionCompatibilityRequiresBlueprintSelectionManifest(t *testing.T) {
	selectionDefinition := json.RawMessage(`{"nodes":[{"id":"pages","type":"fan_out","fanOut":{"itemKind":"blueprint_selection_page"}}],"edges":[]}`)
	ordinaryDefinition := json.RawMessage(`{"nodes":[{"id":"input","type":"artifact_input"}],"edges":[]}`)

	if err := validateStartDefinitionCompatibility(core.BlueprintSelectionJobType, selectionDefinition); err != nil {
		t.Fatalf("selection manifest was rejected for selection workflow: %v", err)
	}
	if err := validateStartDefinitionCompatibility("conversation.workflow_intent", ordinaryDefinition); err != nil {
		t.Fatalf("conversation manifest was rejected for ordinary workflow: %v", err)
	}
	if err := validateStartDefinitionCompatibility("conversation.workflow_intent", selectionDefinition); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("conversation manifest selection workflow error = %v", err)
	}
	if err := validateStartDefinitionCompatibility(core.BlueprintSelectionJobType, ordinaryDefinition); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("selection manifest ordinary workflow error = %v", err)
	}
	if err := validateStartDefinitionCompatibility("", ordinaryDefinition); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("missing manifest job type error = %v", err)
	}
	if err := validateStartDefinitionCompatibility("conversation.workflow_intent", json.RawMessage(`{"nodes":`)); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("malformed definition content error = %v", err)
	}
}
