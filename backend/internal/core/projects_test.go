package core

import (
	"encoding/json"
	"testing"
)

func TestInitialProjectBriefUsesCanonicalDocumentContract(t *testing.T) {
	brief := initialProjectBrief("Support portal", "Help customers resolve issues.")

	payload, err := json.Marshal(brief)
	if err != nil {
		t.Fatalf("marshal initial Project Brief: %v", err)
	}
	if report := ValidateArtifactContent("project_brief", payload); report.Valid {
		t.Fatal("expected unresolved blocking questions to keep the initial Project Brief out of review")
	}

	for _, field := range []string{"requirements", "acceptanceCriteria", "openQuestions", "assumptions"} {
		if _, ok := brief[field].([]any); !ok {
			t.Fatalf("expected %s to be a canonical document array, got %#v", field, brief[field])
		}
	}

	blocks, ok := brief["blocks"].([]map[string]any)
	if !ok || len(blocks) != 3 {
		t.Fatalf("expected three canonical blocks, got %#v", brief["blocks"])
	}
	for index, block := range blocks {
		text, _ := block["text"].(string)
		if text == "" {
			t.Fatalf("expected block %d to expose non-empty text", index)
		}
		if _, legacy := block["description"]; legacy {
			t.Fatalf("block %d still exposes the legacy description field", index)
		}
		if _, legacy := block["question"]; legacy {
			t.Fatalf("block %d still exposes the legacy question field", index)
		}
	}
}
