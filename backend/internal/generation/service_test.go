package generation

import (
	"encoding/json"
	"testing"
)

func TestRedactSensitiveStructuredValues(t *testing.T) {
	t.Parallel()
	value := map[string]any{
		"title":  "safe",
		"apiKey": "secret-value",
		"nested": map[string]any{"password": "hunter2", "note": "keep"},
	}
	redacted := redact(value, "").(map[string]any)
	if redacted["apiKey"] != "[REDACTED]" {
		t.Fatal("API key was not redacted")
	}
	nested := redacted["nested"].(map[string]any)
	if nested["password"] != "[REDACTED]" || nested["note"] != "keep" {
		t.Fatalf("unexpected nested redaction: %#v", nested)
	}
}

func TestWorkspaceInputIncludesExpectedFileHash(t *testing.T) {
	t.Parallel()
	workspace := map[string]any{"files": []any{map[string]any{"path": "src/a.ts", "content": "hello"}}}
	result := workspaceWithFileHashes(workspace).(map[string]any)
	file := result["files"].([]any)[0].(map[string]any)
	if file["contentHash"] != "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("unexpected hash: %v", file["contentHash"])
	}
}

func TestGenerationSchemasAreValidJSON(t *testing.T) {
	t.Parallel()
	if !json.Valid(artifactProposalSchema) || !json.Valid(implementationProposalSchema) {
		t.Fatal("generation schemas must remain valid JSON")
	}
}
