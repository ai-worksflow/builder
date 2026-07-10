package generation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/worksflow/builder/backend/internal/ai"
	"github.com/worksflow/builder/backend/internal/core"
)

type blockedImplementationWorkbench struct{ calls int }

func (w *blockedImplementationWorkbench) GetBundleForGeneration(context.Context, string, string) (core.WorkbenchBundle, error) {
	w.calls++
	return core.WorkbenchBundle{}, core.ErrBlockingGate
}

type staleImplementationWorkbench struct{ calls int }

func (w *staleImplementationWorkbench) GetBundleForGeneration(context.Context, string, string) (core.WorkbenchBundle, error) {
	w.calls++
	return core.WorkbenchBundle{}, core.ErrProposalStale
}

type generationProviderSpy struct{ calls int }

func (p *generationProviderSpy) Generate(context.Context, ai.Request) (ai.Result, error) {
	p.calls++
	return ai.Result{}, nil
}

func TestImplementationGenerationRejectsCompletedManifestBeforeAI(t *testing.T) {
	t.Parallel()

	workbench := &blockedImplementationWorkbench{}
	provider := &generationProviderSpy{}
	service := &Service{workbench: workbench, provider: provider}
	if _, err := service.GenerateImplementation(
		context.Background(), "consumed-bundle", "actor", "model", "instruction",
	); !errors.Is(err, core.ErrBlockingGate) {
		t.Fatalf("expected completed manifest to block generation, got %v", err)
	}
	if workbench.calls != 1 || provider.calls != 0 {
		t.Fatalf("AI ran before manifest readiness gate: workbench=%d provider=%d", workbench.calls, provider.calls)
	}
}

func TestImplementationGenerationRequiresCurrentWorkspaceBeforeAI(t *testing.T) {
	t.Parallel()

	workbench := &staleImplementationWorkbench{}
	provider := &generationProviderSpy{}
	service := &Service{workbench: workbench, provider: provider}
	if _, err := service.GenerateImplementation(
		context.Background(), "old-workspace-bundle", "actor", "model", "instruction",
	); !errors.Is(err, core.ErrProposalStale) {
		t.Fatalf("expected stale workspace manifest to block generation, got %v", err)
	}
	if workbench.calls != 1 || provider.calls != 0 {
		t.Fatalf("AI ran before exact workspace gate: workbench=%d provider=%d", workbench.calls, provider.calls)
	}
}

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
