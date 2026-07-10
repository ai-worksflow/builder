package delivery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type dependencyBuildSandbox struct {
	t              *testing.T
	expected       string
	prepared       bool
	offlineRuns    int
	resolverInputs []string
}

func (s *dependencyBuildSandbox) Kind() string { return "dependency-build-test" }

func (s *dependencyBuildSandbox) PrepareDependencies(_ context.Context, directory string, request DependencyPreparationRequest) (SandboxResult, error) {
	s.prepared = true
	if request.Ecosystem != s.expected {
		s.t.Fatalf("resolver ecosystem=%s want=%s", request.Ecosystem, s.expected)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		s.t.Fatal(err)
	}
	for _, entry := range entries {
		s.resolverInputs = append(s.resolverInputs, entry.Name())
		if entry.Name() == "src" || entry.Name() == "main.go" || entry.Name() == "secret.ts" {
			s.t.Fatalf("source reached network resolver: %s", entry.Name())
		}
	}
	if request.Ecosystem == "node" {
		err = os.Mkdir(filepath.Join(directory, "node_modules"), 0o700)
	} else {
		err = os.MkdirAll(filepath.Join(directory, "pkg", "mod"), 0o700)
	}
	if err != nil {
		s.t.Fatal(err)
	}
	return SandboxResult{ExitCode: 0, Duration: time.Millisecond}, nil
}

func (s *dependencyBuildSandbox) Run(_ context.Context, directory string, request SandboxRequest) (SandboxResult, error) {
	if !s.prepared || request.DependencyDirectory == "" {
		s.t.Fatal("offline quality stage ran without prepared dependencies")
	}
	s.offlineRuns++
	if request.Check == CheckBuild {
		writeTestBuildFile(s.t, directory, "dist/index.html", []byte("<html><body>built</body></html>"))
		writeTestBuildFile(s.t, directory, "dist/assets/app.js", []byte("ready=true"))
	} else if request.Check == CheckTest {
		writeTestBuildFile(s.t, directory, "dist/index.html", []byte("<html><body>tampered by test</body></html>"))
	}
	return SandboxResult{ExitCode: 0, Duration: time.Millisecond}, nil
}

func TestQualityPipelinePreparesDependenciesThenCapturesBuild(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		ecosystem string
		files     []WorkspaceFile
	}{
		{
			name: "node-vite-like", ecosystem: "node",
			files: []WorkspaceFile{
				{Path: "package.json", Content: `{"name":"app","scripts":{"build":"vite build","test":"vitest run"}}`},
				{Path: "package-lock.json", Content: `{"name":"app","lockfileVersion":3,"packages":{"":{"name":"app"}}}`},
				{Path: "src/main.ts", Content: "export const ready = true"},
				{Path: "dist/stale.js", Content: "must be removed before build"},
			},
		},
		{
			name: "go-with-static-output", ecosystem: "go",
			files: []WorkspaceFile{
				{Path: "go.mod", Content: "module example.test/app\n\ngo 1.22\n"},
				{Path: "main.go", Content: "package main\nfunc main() {}\n"},
			},
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			sandbox := &dependencyBuildSandbox{t: t, expected: testCase.ecosystem}
			service := &QualityService{sandbox: sandbox, tempRoot: t.TempDir()}
			checks, artifact := service.runChecksAndCapture(context.Background(), WorkspaceSnapshot{
				Revision: buildTestRef(), Files: testCase.files,
			})
			if artifact == nil || artifact.EntryPath != "index.html" || artifact.FileCount != 2 {
				t.Fatalf("quality did not capture built static output: %+v checks=%+v", artifact, checks)
			}
			for _, file := range artifact.Files {
				if file.Path != "index.html" {
					continue
				}
				content, err := decodeBuildFile(file)
				if err != nil || string(content) != "<html><body>built</body></html>" {
					t.Fatalf("post-build checks changed captured build output: %q %v", content, err)
				}
			}
			if !sandbox.prepared || sandbox.offlineRuns == 0 {
				t.Fatalf("two-stage dependency/build pipeline did not execute: %+v", sandbox)
			}
			for _, check := range checks {
				if check.Status == CheckFailed {
					t.Fatalf("normal dependency workspace failed %s: %+v", check.ID, check.Diagnostics)
				}
			}
		})
	}
}
