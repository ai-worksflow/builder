package verification

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

type canonicalWorkspaceSourceFake struct {
	snapshot CanonicalWorkspaceSnapshot
	err      error
}

type canonicalExecutionEnvironmentCleanupFake struct {
	cleanups []VerificationEnvironmentCleanup
	err      error
}

func (*canonicalExecutionEnvironmentCleanupFake) PrepareCanonical(context.Context, CanonicalExecutionSpec) error {
	return nil
}

func (environment *canonicalExecutionEnvironmentCleanupFake) CleanupVerificationEnvironment(
	_ context.Context,
	input VerificationEnvironmentCleanup,
) error {
	environment.cleanups = append(environment.cleanups, input)
	return environment.err
}

func (source canonicalWorkspaceSourceFake) LoadCanonicalWorkspace(
	context.Context,
	string,
	CanonicalPlanSubject,
) (CanonicalWorkspaceSnapshot, error) {
	return source.snapshot, source.err
}

func TestCanonicalWorkspaceMaterializerSealsExactApprovedSnapshot(t *testing.T) {
	compiled, err := (PlanCompiler{}).CompileCanonical(validCanonicalPlanInput())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	spec := CanonicalExecutionSpec{
		RunID: uuid.NewString(), AttemptID: uuid.NewString(), AttemptFenceEpoch: 3,
		PlanID: uuid.NewString(), PlanHash: compiled.PlanHash, Content: compiled.Content,
	}
	source := canonicalWorkspaceSourceFake{snapshot: CanonicalWorkspaceSnapshot{
		ProjectID: compiled.Content.ProjectID, Subject: compiled.Content.Subject,
		ContentHash: compiled.Content.Subject.WorkspaceContentHash,
		Files: []CanonicalWorkspaceFile{
			{Path: "apps/api/main.go", Content: []byte("package main\n"), Mode: "100644"},
			{Path: "apps/web/start.sh", Content: []byte("#!/bin/sh\nexit 0\n"), Mode: "100755"},
		},
	}}
	materializer, err := NewCanonicalWorkspaceMaterializer(source, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := materializer.MaterializeCanonical(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if err := materializer.PrepareCanonical(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, spec.AttemptID, "3", "workspace")
	value, err := os.ReadFile(filepath.Join(workspace, "apps/api/main.go"))
	if err != nil || string(value) != "package main\n" {
		t.Fatalf("materialized file = %q, %v", value, err)
	}
	for path, expected := range map[string]os.FileMode{
		"apps/api/main.go":  0o400,
		"apps/web/start.sh": 0o500,
	} {
		info, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("stat materialized file %s: %v", path, err)
		}
		if actual := info.Mode().Perm(); actual != expected {
			t.Fatalf("materialized mode for %s = %v, want %v", path, actual, expected)
		}
	}
	if err := materializer.CollectCanonical(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, spec.AttemptID, "3")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canonical workspace survived collection: %v", err)
	}
}

func TestCanonicalWorkspaceTakeoverCleansOlderFenceWithoutGrantingStaleSharedCleanup(t *testing.T) {
	compiled, err := (PlanCompiler{}).CompileCanonical(validCanonicalPlanInput())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	spec := CanonicalExecutionSpec{
		RunID: uuid.NewString(), AttemptID: uuid.NewString(), AttemptFenceEpoch: 3,
		PlanID: uuid.NewString(), PlanHash: compiled.PlanHash, Content: compiled.Content,
	}
	source := canonicalWorkspaceSourceFake{snapshot: CanonicalWorkspaceSnapshot{
		ProjectID: compiled.Content.ProjectID, Subject: compiled.Content.Subject,
		ContentHash: compiled.Content.Subject.WorkspaceContentHash,
		Files: []CanonicalWorkspaceFile{{
			Path: "apps/api/main.go", Content: []byte("package main\n"), Mode: "100644",
		}},
	}}
	environment := &canonicalExecutionEnvironmentCleanupFake{}
	materializer, err := NewCanonicalWorkspaceMaterializer(source, root, environment)
	if err != nil {
		t.Fatal(err)
	}
	if err := materializer.MaterializeCanonical(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if err := materializer.PrepareCanonical(context.Background(), spec); err != nil {
		t.Fatal(err)
	}

	newer := spec
	newer.AttemptFenceEpoch = 4
	environment.err = errors.New("verification daemon unavailable")
	if err := materializer.MaterializeCanonical(context.Background(), newer); !errors.Is(err, ErrCanonicalMaterialization) {
		t.Fatalf("Canonical takeover cleanup failure = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, spec.AttemptID, "3", "workspace")); err != nil {
		t.Fatalf("failed takeover removed retryable Canonical workspace: %v", err)
	}
	if marker, err := readVerificationRuntimeFence(filepath.Join(root, spec.AttemptID)); err != nil || marker != 3 {
		t.Fatalf("failed takeover removed Canonical runtime marker: marker=%d err=%v", marker, err)
	}
	environment.err = nil
	if err := materializer.MaterializeCanonical(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	if len(environment.cleanups) != 2 || environment.cleanups[1].Fence.AttemptFenceEpoch != 3 ||
		!environment.cleanups[1].OwnsSharedRuntime {
		t.Fatalf("Canonical takeover cleanup = %#v", environment.cleanups)
	}
	if err := materializer.PrepareCanonical(context.Background(), newer); err != nil {
		t.Fatal(err)
	}

	if err := materializer.CleanupCanonical(context.Background(), canonicalSpecFence(spec)); err != nil {
		t.Fatal(err)
	}
	staleCleanup := environment.cleanups[len(environment.cleanups)-1]
	if staleCleanup.Fence.AttemptFenceEpoch != 3 || staleCleanup.OwnsSharedRuntime {
		t.Fatalf("stale Canonical cleanup gained shared-runtime authority: %#v", staleCleanup)
	}
	newerWorkspace := filepath.Join(root, newer.AttemptID, "4", "workspace")
	if _, err := os.Stat(newerWorkspace); err != nil {
		t.Fatalf("stale Canonical cleanup removed newer workspace: %v", err)
	}
	marker, err := readVerificationRuntimeFence(filepath.Join(root, newer.AttemptID))
	if err != nil || marker != 4 {
		t.Fatalf("stale Canonical cleanup changed newer runtime marker: marker=%d err=%v", marker, err)
	}
}

func TestCanonicalWorkspaceCleanupRetainsFenceUntilEnvironmentCleanupSucceeds(t *testing.T) {
	compiled, err := (PlanCompiler{}).CompileCanonical(validCanonicalPlanInput())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	spec := CanonicalExecutionSpec{
		RunID: uuid.NewString(), AttemptID: uuid.NewString(), AttemptFenceEpoch: 7,
		PlanID: uuid.NewString(), PlanHash: compiled.PlanHash, Content: compiled.Content,
	}
	source := canonicalWorkspaceSourceFake{snapshot: CanonicalWorkspaceSnapshot{
		ProjectID: compiled.Content.ProjectID, Subject: compiled.Content.Subject,
		ContentHash: compiled.Content.Subject.WorkspaceContentHash,
		Files: []CanonicalWorkspaceFile{{
			Path: "apps/api/main.go", Content: []byte("package main\n"), Mode: "100644",
		}},
	}}
	environment := &canonicalExecutionEnvironmentCleanupFake{}
	materializer, err := NewCanonicalWorkspaceMaterializer(source, root, environment)
	if err != nil {
		t.Fatal(err)
	}
	if err := materializer.MaterializeCanonical(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if err := materializer.PrepareCanonical(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	environment.err = errors.New("network removal failed")
	if err := materializer.CleanupCanonical(context.Background(), canonicalSpecFence(spec)); !errors.Is(err, ErrCanonicalMaterialization) {
		t.Fatalf("Canonical cleanup failure = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, spec.AttemptID, "7", "workspace")); err != nil {
		t.Fatalf("failed Canonical cleanup removed workspace evidence: %v", err)
	}
	if marker, err := readVerificationRuntimeFence(filepath.Join(root, spec.AttemptID)); err != nil || marker != 7 {
		t.Fatalf("failed Canonical cleanup removed runtime marker: marker=%d err=%v", marker, err)
	}
	environment.err = nil
	if err := materializer.CleanupCanonical(context.Background(), canonicalSpecFence(spec)); err != nil {
		t.Fatalf("Canonical cleanup retry = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, spec.AttemptID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Canonical cleanup retry retained Attempt root: %v", err)
	}
}

func TestDecodeCanonicalWorkspaceRejectsUnsafeOrEmptyTrees(t *testing.T) {
	for _, payload := range []string{
		`{"name":"empty","files":[]}`,
		`{"name":"escape","files":[{"path":"../secret","content":"x","mode":"100644"}]}`,
		`{"name":"generated","files":[{"path":"node_modules/x","content":"x","mode":"100644"}]}`,
	} {
		if _, _, err := decodeCanonicalWorkspace([]byte(payload)); !errors.Is(err, ErrCanonicalMaterialization) {
			t.Fatalf("unsafe WorkspaceRevision was accepted: %s, %v", payload, err)
		}
	}
}

func TestDecodeCanonicalWorkspaceRequiresCanonicalFileModes(t *testing.T) {
	for name, payload := range map[string]string{
		"missing":     `{"name":"missing","files":[{"path":"start.sh","content":"x"}]}`,
		"empty":       `{"name":"empty","files":[{"path":"start.sh","content":"x","mode":""}]}`,
		"unsupported": `{"name":"unsupported","files":[{"path":"start.sh","content":"x","mode":"100777"}]}`,
		"numeric":     `{"name":"numeric","files":[{"path":"start.sh","content":"x","mode":493}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeCanonicalWorkspace([]byte(payload)); !errors.Is(err, ErrCanonicalMaterialization) {
				t.Fatalf("non-canonical workspace mode was accepted: %s, %v", payload, err)
			}
		})
	}
}

func TestDecodeCanonicalWorkspacePreservesCanonicalFileModes(t *testing.T) {
	_, files, err := decodeCanonicalWorkspace([]byte(`{
		"name":"modes",
		"files":[
			{"path":"start.sh","content":"#!/bin/sh\n","mode":"100755"},
			{"path":"README.md","content":"ready\n","mode":"100644"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "README.md" || files[0].Mode != "100644" ||
		files[1].Path != "start.sh" || files[1].Mode != "100755" {
		t.Fatalf("decoded canonical file modes = %#v", files)
	}
}
