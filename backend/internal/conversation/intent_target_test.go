package conversation

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	platformdomain "github.com/worksflow/builder/backend/internal/domain"
	runtime "github.com/worksflow/builder/backend/internal/workflow"
)

func TestWorkbenchTargetSliceRequiresExactCompilerOrdinalAndRunMetadata(t *testing.T) {
	projectID, runID, rootID, groupID, sliceID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.NewString()
	nodeKey := "compile-application"
	nodeType := string(platformdomain.NodeManifestCompiler)
	nodeStatus := string(runtime.NodeCompleted)
	manifest := runtime.BuildManifest{
		SchemaVersion: 1, ProjectID: projectID.String(), RunID: runID.String(), ManifestGroupKey: groupID.String(),
		SliceIDs: []string{sliceID}, BundleIDs: []string{rootID.String()},
		Sources: []platformdomain.ArtifactRef{{
			ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: conversationTestHash("target-slice-source"),
		}},
		Constraints: json.RawMessage(`{}`), CreatedAt: time.Now().UTC(),
	}
	if err := manifest.Freeze(); err != nil {
		t.Fatal(err)
	}
	output, err := platformdomain.CanonicalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	runContext := runtime.RunContext{
		Nodes: map[string]runtime.NodeMetadata{nodeKey: {Output: output}},
		Slices: map[string]runtime.SliceContext{sliceID: {
			ID: sliceID, Key: "CHECKOUT", Title: "Checkout", FanOutNodeID: "pages",
		}},
	}
	rawContext, err := platformdomain.CanonicalJSON(runContext)
	if err != nil {
		t.Fatal(err)
	}
	slice, err := workbenchTargetSliceFromRuntime(
		projectID, runID, rootID, groupID.String(), 0, rawContext, &nodeKey, &nodeType, &nodeStatus, &sliceID,
	)
	if err != nil || slice.ID != sliceID || slice.Key != "CHECKOUT" || slice.Title != "Checkout" {
		t.Fatalf("valid compiler target slice was not resolved exactly: slice=%+v err=%v", slice, err)
	}
	if _, err := workbenchTargetSliceFromRuntime(
		projectID, runID, rootID, groupID.String(), 1, rawContext, &nodeKey, &nodeType, &nodeStatus, &sliceID,
	); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("tampered root ordinal was accepted: %v", err)
	}
	wrongPersistedSliceID := uuid.NewString()
	if _, err := workbenchTargetSliceFromRuntime(
		projectID, runID, rootID, groupID.String(), 0, rawContext, &nodeKey, &nodeType, &nodeStatus, &wrongPersistedSliceID,
	); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("tampered persisted root slice was accepted: %v", err)
	}
	runContext.Slices[sliceID] = runtime.SliceContext{ID: sliceID, Key: "", Title: "Checkout", FanOutNodeID: "pages"}
	tamperedContext, err := platformdomain.CanonicalJSON(runContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workbenchTargetSliceFromRuntime(
		projectID, runID, rootID, groupID.String(), 0, tamperedContext, &nodeKey, &nodeType, &nodeStatus, &sliceID,
	); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("missing immutable slice key was accepted: %v", err)
	}
}
