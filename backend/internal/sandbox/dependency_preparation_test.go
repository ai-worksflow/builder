package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/worksflow/builder/backend/internal/delivery"
)

type interactiveDependencyResolverFake struct {
	runs int
}

func (resolver *interactiveDependencyResolverFake) PrepareDependencies(
	_ context.Context,
	directory string,
	request delivery.DependencyPreparationRequest,
) (delivery.SandboxResult, error) {
	resolver.runs++
	target := filepath.Join(directory, "node_modules", ".bin")
	file := "vite"
	if request.Ecosystem == "go" {
		target = filepath.Join(directory, "pkg", "mod", "example.com", "module@v1.0.0")
		file = "module.go"
	} else if request.Ecosystem != "node" {
		return delivery.SandboxResult{ExitCode: 1}, nil
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return delivery.SandboxResult{}, err
	}
	if err := os.WriteFile(filepath.Join(target, file), []byte("prepared\n"), 0o700); err != nil {
		return delivery.SandboxResult{}, err
	}
	return delivery.SandboxResult{ExitCode: 0}, nil
}

func TestProcessDependencyPreparerHydratesGoModuleCache(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	api := filepath.Join(workspace, "services", "api")
	if err := os.MkdirAll(api, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(api, "go.mod"), []byte("module example.com/api\n\ngo 1.22\n\nrequire example.com/module v1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(api, "go.sum"), []byte("example.com/module v1.0.0 h1:fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := &interactiveDependencyResolverFake{}
	preparer, err := NewProcessDependencyPreparer(resolver)
	if err != nil {
		t.Fatal(err)
	}
	mount := WorkspaceMount{SessionRoot: root, Workspace: workspace}
	command := ResolvedProcessCommand{ServiceID: "api", CommandID: "build", WorkingDirectory: "services/api"}
	if err := preparer.Prepare(context.Background(), mount, command); err != nil {
		t.Fatal(err)
	}
	module := filepath.Join(workspace, ".worksflow", "dependencies", "go", "pkg", "mod", "example.com", "module@v1.0.0", "module.go")
	if _, err := os.Stat(module); err != nil {
		t.Fatalf("prepared Go module: %v", err)
	}
	if err := preparer.Prepare(context.Background(), mount, command); err != nil {
		t.Fatal(err)
	}
	if resolver.runs != 1 {
		t.Fatalf("resolver runs = %d, want 1", resolver.runs)
	}
}

func TestNodeProcessDependencyPreparerHydratesAndReusesLockProjection(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	web := filepath.Join(workspace, "apps", "web")
	if err := os.MkdirAll(web, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"dependencies":{"vite":"1.0.0"}}`
	lock := `{"lockfileVersion":3,"packages":{"":{"dependencies":{"vite":"1.0.0"}},"node_modules/vite":{"version":"1.0.0","resolved":"https://registry.npmjs.org/vite/-/vite-1.0.0.tgz","integrity":"sha512-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="}}}`
	if err := os.WriteFile(filepath.Join(web, "package.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "package-lock.json"), []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := &interactiveDependencyResolverFake{}
	preparer, err := NewProcessDependencyPreparer(resolver)
	if err != nil {
		t.Fatal(err)
	}
	mount := WorkspaceMount{SessionRoot: root, Workspace: workspace}
	command := ResolvedProcessCommand{ServiceID: "web", CommandID: "start", WorkingDirectory: "apps/web"}
	if err := preparer.Prepare(context.Background(), mount, command); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(web, "node_modules", ".bin", "vite")); err != nil {
		t.Fatalf("prepared vite executable: %v", err)
	}
	if err := preparer.Prepare(context.Background(), mount, command); err != nil {
		t.Fatal(err)
	}
	if resolver.runs != 1 {
		t.Fatalf("resolver runs = %d, want 1", resolver.runs)
	}
}
