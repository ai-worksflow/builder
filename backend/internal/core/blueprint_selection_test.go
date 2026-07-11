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

func TestPrototypeEligibleForBlueprintSelection(t *testing.T) {
	tests := []struct {
		name    string
		payload json.RawMessage
		formal  bool
		wantErr bool
	}{
		{name: "explicit formal", payload: json.RawMessage(`{"exploratory":false}`), formal: true},
		{name: "legacy formal", payload: json.RawMessage(`{"frames":[]}`), formal: true},
		{name: "exploratory", payload: json.RawMessage(`{"exploratory":true}`)},
		{name: "wrong type", payload: json.RawMessage(`{"exploratory":"false"}`), wantErr: true},
		{name: "null flag", payload: json.RawMessage(`{"exploratory":null}`), wantErr: true},
		{name: "non object", payload: json.RawMessage(`null`), wantErr: true},
		{name: "invalid JSON", payload: json.RawMessage(`{`), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			formal, err := prototypeEligibleForBlueprintSelection(test.payload)
			if (err != nil) != test.wantErr || formal != test.formal {
				t.Fatalf("formal=%t err=%v, want formal=%t wantErr=%t", formal, err, test.formal, test.wantErr)
			}
		})
	}
}
