package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestSelectionDocumentationJobRequiresParentManifestContract(t *testing.T) {
	service := &ProposalService{}
	for _, constraints := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`{"instruction":"draft docs"}`)} {
		err := service.validateParentBlueprintSelection(context.Background(), uuid.New(), uuid.NewString(), CreateManifestInput{
			JobType: SelectionDocumentationJobType, Constraints: constraints,
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("constraints %s error = %v, want ErrInvalidInput", constraints, err)
		}
	}
	if err := service.validateParentBlueprintSelection(context.Background(), uuid.New(), uuid.NewString(), CreateManifestInput{
		JobType: "ordinary.documentation", Constraints: json.RawMessage(`{"instruction":"draft docs"}`),
	}); err != nil {
		t.Fatalf("ordinary manifest without selection parent was rejected: %v", err)
	}
}
