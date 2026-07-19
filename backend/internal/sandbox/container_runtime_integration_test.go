package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContainerRuntimeRealDockerCanary(t *testing.T) {
	runnerImage := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_SANDBOX_RUNNER_IMAGE"))
	if runnerImage == "" {
		t.Skip("WORKSFLOW_TEST_SANDBOX_RUNNER_IMAGE is required for the real container-runtime canary")
	}
	root := t.TempDir()
	sessionRoot := filepath.Join(root, testSessionID)
	workspace := filepath.Join(sessionRoot, "workspace")
	codexHome := filepath.Join(sessionRoot, "runtime", "codex")
	for _, directory := range []string{workspace, codexHome} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	view := newTestSession(t, cleanCandidate(t), sandboxBaseTime).Snapshot()
	digestIndex := strings.LastIndex(runnerImage, "@")
	if digestIndex < 0 {
		t.Fatal("WORKSFLOW_TEST_SANDBOX_RUNNER_IMAGE must be digest-pinned")
	}
	view.RunnerImageDigest = runnerImage[digestIndex+1:]
	spec, err := RuntimeSpecForSession(view, WorkspaceMount{
		SessionRoot: sessionRoot, Workspace: workspace, CodexHome: codexHome,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewContainerRuntime(ContainerRuntimeConfig{
		RuntimeBinary: "docker", WorkspaceRoot: root, RunnerImage: runnerImage,
		StartupTimeout: 45 * time.Second, CommandTimeout: 15 * time.Second, OutputLimit: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_ = manager.Terminate(ctx, spec)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		_ = manager.Terminate(cleanupCtx, spec)
	})
	if err := manager.Readiness(ctx); err != nil {
		t.Fatal(err)
	}
	ensured, err := manager.Ensure(ctx, spec)
	if err != nil || ensured.State != "created" {
		t.Fatalf("ensure = %#v, %v", ensured, err)
	}
	if _, err := manager.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	ready, err := manager.WaitReady(ctx, spec)
	if err != nil || !ready.Healthy || ready.State != "running" || len(ready.HostPorts) != len(spec.Ports) {
		t.Fatalf("ready = %#v, %v", ready, err)
	}
	if _, err := manager.runCommand(
		ctx, "exec", "--detach", manager.containerName(spec.SessionID),
		"node", "-e", `require('http').createServer((_,res)=>res.end('gateway-ok')).listen(3000,'0.0.0.0')`,
	); err != nil {
		t.Fatal(err)
	}
	previewURL := fmt.Sprintf("http://127.0.0.1:%d/", ready.HostPorts["web-http"])
	client := &http.Client{Timeout: 2 * time.Second}
	var previewBody string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		response, requestErr := client.Get(previewURL)
		if requestErr != nil {
			continue
		}
		value, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		if readErr == nil && response.StatusCode == http.StatusOK {
			previewBody = string(value)
			break
		}
	}
	if previewBody != "gateway-ok" {
		t.Fatalf("isolated preview gateway response = %q", previewBody)
	}
	terminal, err := manager.OpenTerminal(ctx, RuntimeTerminalSpec{
		Runtime: spec, ID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		WorkingDirectory: ".", Rows: 24, Columns: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := terminal.WriteInput([]byte("echo PTY_RUNTIME_OK; id -u; exit\r")); err != nil {
		t.Fatal(err)
	}
	var terminalOutput bytes.Buffer
	terminalDeadline := time.After(10 * time.Second)
	terminalOpen := true
	for terminalOpen {
		select {
		case value, open := <-terminal.Output():
			if !open {
				terminalOpen = false
				continue
			}
			_, _ = terminalOutput.Write(value)
		case <-terminalDeadline:
			t.Fatalf("fixed PTY did not exit: %q", terminalOutput.String())
		}
	}
	exit := <-terminal.Done()
	if exit.ExitCode != 0 || exit.Failure != "" || !strings.Contains(terminalOutput.String(), "PTY_RUNTIME_OK") ||
		!strings.Contains(terminalOutput.String(), fmt.Sprintf("\n%d\r", os.Getuid())) {
		t.Fatalf("fixed non-root PTY output=%q exit=%#v", terminalOutput.String(), exit)
	}
	paused, err := manager.Suspend(ctx, spec)
	if err != nil || paused.State != "paused" {
		t.Fatalf("suspend = %#v, %v", paused, err)
	}
	resumed, err := manager.Resume(ctx, spec)
	if err != nil || resumed.State != "running" {
		t.Fatalf("resume = %#v, %v", resumed, err)
	}
	if err := manager.Terminate(ctx, spec); err != nil {
		t.Fatal(err)
	}
}
