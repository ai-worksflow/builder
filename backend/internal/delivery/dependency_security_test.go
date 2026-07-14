package delivery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validSHA512Integrity = "sha512-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="

func TestNodeDependencyValidationRequiresNPMRegistryIntegrityLock(t *testing.T) {
	t.Parallel()
	manifest := WorkspaceFile{Path: "package.json", Content: `{"dependencies":{"react":"18.3.1"}}`}
	_, diagnostics := validateDependencyWorkspace([]WorkspaceFile{manifest}, DependencyPolicy{NPMRegistry: defaultNPMRegistry})
	assertDiagnosticCode(t, diagnostics, "package_lock_required")

	lock := WorkspaceFile{Path: "package-lock.json", Content: `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {"dependencies":{"react":"18.3.1"}},
	    "node_modules/react": {
	      "version":"18.3.1",
	      "resolved":"https://user:token@evil.example/react.tgz",
	      "integrity":"sha512-YWJjZA=="
	    }
	  }
	}`}
	_, diagnostics = validateDependencyWorkspace([]WorkspaceFile{manifest, lock}, DependencyPolicy{NPMRegistry: defaultNPMRegistry})
	assertDiagnosticCode(t, diagnostics, "package_lock_registry_mismatch")
	assertDiagnosticCode(t, diagnostics, "package_lock_integrity_missing")

	lock.Content = strings.ReplaceAll(lock.Content, "https://user:token@evil.example/react.tgz", "https://registry.npmjs.org/react/-/react-18.3.1.tgz")
	lock.Content = strings.ReplaceAll(lock.Content, "sha512-YWJjZA==", validSHA512Integrity)
	_, diagnostics = validateDependencyWorkspace([]WorkspaceFile{manifest, lock}, DependencyPolicy{NPMRegistry: defaultNPMRegistry})
	if hasErrorDiagnostic(diagnostics) {
		t.Fatalf("valid npm dependency lock was rejected: %+v", diagnostics)
	}
}

func TestGoDependencyValidationRejectsLocalReplaceAndMissingSum(t *testing.T) {
	t.Parallel()
	manifest := WorkspaceFile{Path: "go.mod", Content: "module example.test/app\n\ngo 1.22\n\nrequire example.test/lib v1.2.3\nreplace example.test/lib => ../lib\n"}
	_, diagnostics := validateDependencyWorkspace([]WorkspaceFile{manifest}, DependencyPolicy{GoProxy: defaultGoProxy, GoSumDB: defaultGoSumDB})
	assertDiagnosticCode(t, diagnostics, "go_local_replace_forbidden")
	assertDiagnosticCode(t, diagnostics, "go_sum_required")

	manifest.Content = "module example.test/app\n\ngo 1.22\n\nrequire example.test/lib v1.2.3\nreplace example.test/lib => example.test/fork v1.2.4\n"
	_, diagnostics = validateDependencyWorkspace([]WorkspaceFile{manifest, {Path: "go.sum", Content: "example.test/lib v1.2.3 h1:sum\n"}}, DependencyPolicy{GoProxy: defaultGoProxy, GoSumDB: defaultGoSumDB})
	if hasErrorDiagnostic(diagnostics) {
		t.Fatalf("remote versioned Go replacement was rejected: %+v", diagnostics)
	}
}

func TestResolverAndBuildContainerArgumentsKeepNetworkBoundary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dependencies := filepath.Join(root, "dependencies")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(dependencies, "node_modules"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	sandbox := &ContainerSandbox{
		workspaceRoot: root, nodeImage: "node:22-alpine", goImage: "golang:1.22-alpine",
		memory: "256m", cpus: "0.5", pids: 64,
		resolverNetwork: "resolver-egress",
		resolverPolicy: DependencyPolicy{
			NPMRegistry: defaultNPMRegistry, GoProxy: defaultGoProxy, GoSumDB: defaultGoSumDB,
		},
		resolverMemory: "128m", resolverCPUs: "0.25", resolverPIDs: 32,
	}
	resolverArgs := sandbox.dependencyRunArgs("resolver", dependencies, "node", sandbox.nodeImage, []string{
		"npm", "ci", "--ignore-scripts", "--registry=" + defaultNPMRegistry,
	})
	resolverText := strings.Join(resolverArgs, " ")
	if !strings.Contains(resolverText, "--network resolver-egress") ||
		!strings.Contains(resolverText, "src="+dependencies+",dst=/resolver") ||
		strings.Contains(resolverText, "dst=/resolver,rw") ||
		!strings.Contains(resolverText, "--ignore-scripts") ||
		strings.Contains(resolverText, workspace) {
		t.Fatalf("resolver boundary is unsafe: %s", resolverText)
	}

	buildArgs, err := sandbox.qualityRunArgs("quality", workspace, SandboxRequest{
		Ecosystem: "node", Check: CheckBuild, DependencyDirectory: dependencies,
	}, sandbox.nodeImage, []string{"npm", "run", "build"})
	if err != nil {
		t.Fatal(err)
	}
	buildText := strings.Join(buildArgs, " ")
	if !strings.Contains(buildText, "--network none") ||
		!strings.Contains(buildText, "src="+workspace+",dst=/workspace") ||
		strings.Contains(buildText, "dst=/workspace,rw") ||
		!strings.Contains(buildText, "src="+filepath.Join(dependencies, "node_modules")+",dst=/workspace/node_modules,readonly") ||
		!strings.Contains(buildText, "npm_config_offline=true") ||
		strings.Contains(buildText, "resolver-egress") {
		t.Fatalf("offline build boundary is unsafe: %s", buildText)
	}
	goDependencies := filepath.Join(root, "go-dependencies")
	if err := os.MkdirAll(filepath.Join(goDependencies, "pkg", "mod"), 0o700); err != nil {
		t.Fatal(err)
	}
	goResolverText := strings.Join(sandbox.dependencyRunArgs("go-resolver", goDependencies, "go", sandbox.goImage, []string{"go", "mod", "download", "all"}), " ")
	if !strings.Contains(goResolverText, "GOPROXY="+defaultGoProxy) ||
		!strings.Contains(goResolverText, "GOSUMDB="+defaultGoSumDB) || strings.Contains(goResolverText, ",direct") {
		t.Fatalf("Go resolver policy is not fixed: %s", goResolverText)
	}
	goBuildArgs, err := sandbox.qualityRunArgs("go-quality", workspace, SandboxRequest{
		Ecosystem: "go", Check: CheckBuild, DependencyDirectory: goDependencies,
	}, sandbox.goImage, []string{"go", "build", "./..."})
	if err != nil {
		t.Fatal(err)
	}
	goBuildText := strings.Join(goBuildArgs, " ")
	if !strings.Contains(goBuildText, "--network none") || !strings.Contains(goBuildText, "GOPROXY=off") ||
		!strings.Contains(goBuildText, "dst=/go/pkg/mod,readonly") || !strings.Contains(goBuildText, "GOFLAGS=-mod=readonly") {
		t.Fatalf("Go offline build policy is unsafe: %s", goBuildText)
	}
}

func TestSandboxImageReproducibilityRequiresDigestPins(t *testing.T) {
	t.Parallel()
	mutable := &ContainerSandbox{nodeImage: "node:22-alpine", goImage: "golang:1.22-alpine"}
	if mutable.ImagesDigestPinned() {
		t.Fatal("mutable development tags were reported as reproducible")
	}
	pinned := &ContainerSandbox{
		nodeImage: "node:22-alpine@sha256:" + strings.Repeat("a", 64),
		goImage:   "golang:1.22-alpine@sha256:" + strings.Repeat("b", 64),
	}
	if !pinned.ImagesDigestPinned() {
		t.Fatal("valid image digests were not recognized")
	}
}

func TestNodeTestCommandUsesCIWithoutFrameworkSpecificArguments(t *testing.T) {
	t.Parallel()
	sandbox := &ContainerSandbox{nodeImage: "node:22-alpine"}
	_, command, err := sandbox.fixedCommand(SandboxRequest{Ecosystem: "node", Check: CheckTest})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(command, " ") != "npm test --if-present" {
		t.Fatalf("Node test command is not framework-neutral: %#v", command)
	}
}

func TestRemoteDaemonRequiresSharedWorkspaceRoot(t *testing.T) {
	t.Parallel()
	if _, err := validateWorkspaceRoot("", "tcp://sandbox:2375"); err == nil {
		t.Fatal("remote daemon accepted an implicit host-only temporary directory")
	}
	root := t.TempDir()
	if actual, err := validateWorkspaceRoot(root, "tcp://sandbox:2375"); err != nil || actual != root {
		t.Fatalf("shared remote workspace root rejected: %q %v", actual, err)
	}
}

func TestPreparedDependencyRootCannotBeSymlinkedOutsideResolverDirectory(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(directory, "node_modules")); err != nil {
		t.Fatal(err)
	}
	if err := ensurePreparedDependencyLayout(directory, "node"); err == nil {
		t.Fatal("resolver-controlled dependency root symlink was accepted")
	}
	goDirectory := t.TempDir()
	if err := ensurePreparedDependencyLayout(goDirectory, "go"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(goDirectory, "pkg", "mod")); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("empty Go module cache was not safely materialized: %v %v", info, err)
	}
}

func assertDiagnosticCode(t *testing.T, diagnostics []Diagnostic, code string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return
		}
	}
	t.Fatalf("expected diagnostic %s in %+v", code, diagnostics)
}
