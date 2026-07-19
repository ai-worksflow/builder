package lsp

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/templates"
)

const lspRuntimeBinding = "10000000-0000-4000-8000-000000000013"

type containerRuntimeCLIFake struct {
	mu sync.Mutex

	profile        ProfileIdentity
	workspaceRoot  string
	executable     []byte
	executableMode int64
	executableUID  int
	executableGID  int
	containerID    string
	imageID        string
	containerName  string
	calls          [][]string
	events         []string
	started        bool
	removed        bool
	closed         bool
	closeCalls     int
	closeErr       error
	closeStarted   chan struct{}
	closeRelease   <-chan struct{}
	createErr      error
	stream         *containerRuntimeStreamFake
	mutateInspect  func(map[string]any)
}

func (fake *containerRuntimeCLIFake) Run(
	ctx context.Context,
	limit int64,
	arguments ...string,
) ([]byte, error) {
	if ctx == nil || limit <= 0 {
		return nil, ErrContainerRuntimeInvalid
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, append([]string(nil), arguments...))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := strings.Join(arguments, " ")
	switch {
	case command == "version":
		fake.events = append(fake.events, "version")
		return []byte("Docker version 27"), nil
	case len(arguments) == 3 && arguments[0] == "image" && arguments[1] == "inspect":
		fake.events = append(fake.events, "image-inspect")
		return json.Marshal([]map[string]any{{
			"Id": fake.imageID, "RepoDigests": []string{fake.profile.Runtime.Image},
		}})
	case len(arguments) > 2 && arguments[0] == "container" && arguments[1] == "create":
		fake.events = append(fake.events, "create")
		for index, argument := range arguments {
			if argument == "--name" && index+1 < len(arguments) {
				fake.containerName = arguments[index+1]
			}
		}
		if fake.createErr != nil {
			return nil, fake.createErr
		}
		return []byte(fake.containerID + "\n"), nil
	case len(arguments) == 3 && arguments[0] == "container" && arguments[1] == "inspect":
		fake.events = append(fake.events, "container-inspect")
		return fake.inspectJSON(arguments[2])
	case len(arguments) == 4 && arguments[0] == "container" && arguments[1] == "cp":
		fake.events = append(fake.events, "cp")
		archive := lspExecutableTar(
			fake.profile.Runtime.ExecutablePath, fake.executable,
			fake.executableMode, fake.executableUID, fake.executableGID,
		)
		if int64(len(archive)) > limit {
			return nil, ErrContainerRuntimeExhausted
		}
		return archive, nil
	case len(arguments) == 4 && arguments[0] == "container" && arguments[1] == "rm":
		fake.events = append(fake.events, "rm")
		fake.removed = true
		if fake.stream != nil {
			fake.stream.finish(nil)
		}
		return nil, nil
	case len(arguments) == 5 && arguments[0] == "container" && arguments[1] == "stop":
		fake.events = append(fake.events, "stop")
		if fake.stream != nil {
			fake.stream.finish(nil)
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected fake CLI command: %q", command)
	}
}

func (fake *containerRuntimeCLIFake) Start(
	ctx context.Context,
	arguments ...string,
) (containerRuntimeStream, error) {
	if ctx == nil {
		return nil, ErrContainerRuntimeInvalid
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, append([]string(nil), arguments...))
	fake.events = append(fake.events, "start")
	fake.started = true
	fake.stream = newContainerRuntimeStreamFake()
	return fake.stream, nil
}

func (fake *containerRuntimeCLIFake) Close() error {
	fake.mu.Lock()
	fake.closed = true
	fake.closeCalls++
	closeErr, started, release := fake.closeErr, fake.closeStarted, fake.closeRelease
	fake.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	return closeErr
}

func (fake *containerRuntimeCLIFake) inspectJSON(_ string) ([]byte, error) {
	limits := fake.profile.EffectiveLimits
	name := fake.containerName
	if name == "" {
		name = languageServerContainerName(testConnection, lspRuntimeBinding)
	}
	document := map[string]any{
		"Id": fake.containerID, "Name": "/" + name, "Image": fake.imageID,
		"State": map[string]any{"Running": fake.started && !fake.removed},
		"Config": map[string]any{
			"Image": fake.profile.Runtime.Image, "User": languageServerContainerUser,
			"WorkingDir": languageServerWorkspaceMount + "/apps/web",
			"Entrypoint": []string{fake.profile.Runtime.ExecutablePath},
			"Cmd":        append([]string(nil), fake.profile.Runtime.Argv[1:]...),
			"Env":        []string{"PATH=/usr/bin", "HOME=/tmp", "TMPDIR=/tmp", "XDG_CACHE_HOME=/cache"},
			"Labels": map[string]string{
				"worksflow.kind":             "language-server",
				"worksflow.lsp.connection":   testConnection,
				"worksflow.lsp.binding":      lspRuntimeBinding,
				"worksflow.lsp.profile":      fake.profile.ID,
				"worksflow.lsp.profile-hash": fake.profile.ContentHash,
				"worksflow.lsp.release":      fake.profile.TemplateRelease.ID,
				"worksflow.lsp.release-hash": fake.profile.TemplateRelease.ContentHash,
			},
			"OpenStdin": true, "Tty": false,
			"StopTimeout": max(1, (limits.ShutdownTimeoutMillis+999)/1000),
			"Healthcheck": map[string]any{
				"Test": []string{"NONE"}, "Interval": 0, "Timeout": 0,
				"StartPeriod": 0, "StartInterval": 0, "Retries": 0,
			},
		},
		"HostConfig": map[string]any{
			"ReadonlyRootfs": true, "Privileged": false, "NetworkMode": "none",
			"IpcMode": "none", "PidMode": "private", "CapAdd": []string{}, "CapDrop": []string{"ALL"},
			"SecurityOpt": []string{"no-new-privileges"}, "PidsLimit": limits.PIDLimit,
			"Memory": limits.MemoryBytes, "MemorySwap": limits.MemoryBytes,
			"NanoCpus": int64(limits.CPUMillis) * 1_000_000,
			"Tmpfs": map[string]string{
				languageServerTempMount:  "rw,noexec,nosuid,nodev,size=" + fmt.Sprint(limits.TempBytes) + ",mode=0700,uid=65532,gid=65532",
				languageServerCacheMount: "rw,noexec,nosuid,nodev,size=" + fmt.Sprint(limits.CacheBytes) + ",mode=0700,uid=65532,gid=65532",
			},
			"Devices": []any{}, "DeviceRequests": []any{}, "OomKillDisable": false,
			"Ulimits": []map[string]any{
				{"Name": "core", "Soft": 0, "Hard": 0},
				{"Name": "nofile", "Soft": 4096, "Hard": 4096},
			},
			"LogConfig": map[string]any{"Type": "none"},
		},
		"Mounts": []map[string]any{{
			"Type": "bind", "Source": fake.workspaceRoot, "Destination": languageServerWorkspaceMount,
			"Mode": "ro", "RW": false, "Propagation": "rprivate",
		}},
	}
	if fake.mutateInspect != nil {
		fake.mutateInspect(document)
	}
	return json.Marshal([]any{document})
}

func (fake *containerRuntimeCLIFake) callsSnapshot() [][]string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	result := make([][]string, len(fake.calls))
	for index := range fake.calls {
		result[index] = append([]string(nil), fake.calls[index]...)
	}
	return result
}

func (fake *containerRuntimeCLIFake) eventsSnapshot() []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string(nil), fake.events...)
}

func (fake *containerRuntimeCLIFake) closeSnapshot() (bool, int) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.closed, fake.closeCalls
}

type synchronizedWriteCloser struct {
	mu     sync.Mutex
	value  bytes.Buffer
	closed bool
}

func (writer *synchronizedWriteCloser) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return 0, os.ErrClosed
	}
	return writer.value.Write(value)
}

func (writer *synchronizedWriteCloser) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.closed = true
	return nil
}

func (writer *synchronizedWriteCloser) bytes() []byte {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return append([]byte(nil), writer.value.Bytes()...)
}

type containerRuntimeStreamFake struct {
	stdin       *synchronizedWriteCloser
	stdout      *io.PipeReader
	stdoutWrite *io.PipeWriter
	stderr      *io.PipeReader
	stderrWrite *io.PipeWriter
	done        chan struct{}
	once        sync.Once
	err         error
}

func newContainerRuntimeStreamFake() *containerRuntimeStreamFake {
	stdout, stdoutWrite := io.Pipe()
	stderr, stderrWrite := io.Pipe()
	return &containerRuntimeStreamFake{
		stdin: &synchronizedWriteCloser{}, stdout: stdout, stdoutWrite: stdoutWrite,
		stderr: stderr, stderrWrite: stderrWrite, done: make(chan struct{}),
	}
}

func (stream *containerRuntimeStreamFake) Stdin() io.WriteCloser { return stream.stdin }
func (stream *containerRuntimeStreamFake) Stdout() io.ReadCloser { return stream.stdout }
func (stream *containerRuntimeStreamFake) Stderr() io.ReadCloser { return stream.stderr }
func (stream *containerRuntimeStreamFake) Wait() error {
	<-stream.done
	return stream.err
}
func (stream *containerRuntimeStreamFake) Close() error {
	stream.finish(nil)
	return nil
}
func (stream *containerRuntimeStreamFake) finish(err error) {
	stream.once.Do(func() {
		stream.err = err
		_ = stream.stdin.Close()
		_ = stream.stdoutWrite.Close()
		_ = stream.stderrWrite.Close()
		_ = stream.stdout.Close()
		_ = stream.stderr.Close()
		close(stream.done)
	})
}

func lspContainerRuntimeFixture(t *testing.T) (
	*ContainerRuntime,
	*containerRuntimeCLIFake,
	ContainerStartInput,
) {
	t.Helper()
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "apps", "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(workspace, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(workspace, "apps", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	executable := []byte("qualified-typescript-language-server-binary")
	profile := lspTestProfile("typescript")
	profile.Runtime.ExecutableDigest = lspRawDigest(executable)
	profile.ContentHash, _ = templates.ComputeLanguageServerProfileContentHash(profile.LanguageServerProfile)
	profile.EffectiveLimits = profile.Limits
	fake := &containerRuntimeCLIFake{
		profile: profile, workspaceRoot: workspace, executable: executable,
		executableMode: 0o555,
		containerID:    strings.Repeat("a", 64), imageID: "sha256:" + strings.Repeat("b", 64),
	}
	runtime, err := newContainerRuntime(ContainerRuntimeConfig{
		CommandTimeout: time.Second, CLIOutputBytes: 1 << 20,
	}, "docker", "", fake)
	if err != nil {
		t.Fatal(err)
	}
	input := ContainerStartInput{
		Profile: profile, WorkspaceRoot: workspace, ServiceRoot: "apps/web",
		ConnectionID: testConnection, BindingID: lspRuntimeBinding,
	}
	return runtime, fake, input
}

func TestContainerRuntimeCreatesVerifiesAndAttachesExactContainer(t *testing.T) {
	runtime, fake, input := lspContainerRuntimeFixture(t)
	process, err := runtime.Start(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if process.Name() != languageServerContainerName(testConnection, lspRuntimeBinding) ||
		process.Profile().ContentHash != input.Profile.ContentHash {
		t.Fatalf("unexpected process identity: %s %#v", process.Name(), process.Profile())
	}
	events := fake.eventsSnapshot()
	if strings.Join(events, ",") != "image-inspect,create,container-inspect,cp,start,container-inspect" {
		t.Fatalf("supply-chain order = %v", events)
	}
	calls := fake.callsSnapshot()
	create := calls[1]
	joined := strings.Join(create, "\x00")
	for _, exact := range []string{
		"container\x00create", "--pull\x00never", "--no-healthcheck", "--network\x00none", "--read-only",
		"--cap-drop\x00ALL", "--security-opt\x00no-new-privileges", "--user\x0065532:65532",
		"--pids-limit\x0064", "--memory\x00536870912", "--memory-swap\x00536870912",
		"--cpus\x001.000", "/tmp:rw,noexec,nosuid,nodev,size=268435456,mode=0700,uid=65532,gid=65532",
		"/cache:rw,noexec,nosuid,nodev,size=268435456,mode=0700,uid=65532,gid=65532",
		"--ulimit\x00nofile=4096:4096\x00--ulimit\x00core=0:0",
		"type=bind,src=" + input.WorkspaceRoot + ",dst=/workspace,readonly,bind-propagation=rprivate",
		"--workdir\x00/workspace/apps/web", "--entrypoint\x00/opt/lsp/typescript-language-server",
		input.Profile.Runtime.Image + "\x00--stdio",
	} {
		if !strings.Contains(joined, exact) {
			t.Fatalf("create args missing %q: %#v", exact, create)
		}
	}
	if strings.Contains(joined, "/bin/sh") || strings.Contains(joined, "bash") {
		t.Fatalf("create used a shell: %#v", create)
	}
	for _, call := range calls {
		if len(call) > 0 && call[0] == "pull" {
			t.Fatalf("runtime attempted a pull: %#v", call)
		}
	}
	containerName := languageServerContainerName(testConnection, lspRuntimeBinding)
	for _, call := range calls[2:] {
		if containsExactString(call, containerName) {
			t.Fatalf("post-create operation used predictable name instead of exact ID: %#v", call)
		}
	}
	if want := []string{
		"container", "cp", fake.containerID + ":" + input.Profile.Runtime.ExecutablePath, "-",
	}; !equalStrings(calls[3], want) {
		t.Fatalf("executable copy = %#v, want %#v", calls[3], want)
	}
	if want := []string{"start", "--attach", "--interactive", fake.containerID}; !equalStrings(calls[4], want) {
		t.Fatalf("attached start = %#v, want %#v", calls[4], want)
	}

	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if err := process.WriteFrame(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	wantWire := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(payload), payload)
	if got := string(fake.stream.stdin.bytes()); got != wantWire {
		t.Fatalf("stdin frame = %q, want %q", got, wantWire)
	}
	response := []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)
	go func() {
		_, _ = fmt.Fprintf(fake.stream.stdoutWrite, "Content-Length: %d\r\n\r\n%s", len(response), response)
	}()
	got, err := process.ReadFrame(context.Background())
	if err != nil || !bytes.Equal(got, response) {
		t.Fatalf("ReadFrame = %s, %v", got, err)
	}
	if _, err := fake.stream.stderrWrite.Write([]byte("bounded server log")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(process.Stderr()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := string(process.Stderr()); got != "bounded server log" {
		t.Fatalf("stderr = %q", got)
	}
	if err := process.Terminate(context.Background()); err != nil {
		t.Fatal(err)
	}
	exit, err := process.Wait(context.Background())
	if err != nil || exit.FinishedAt.IsZero() {
		t.Fatalf("Wait = %#v, %v", exit, err)
	}
	if !fake.removed {
		t.Fatal("container was not force-removed")
	}
	if err := runtime.Close(); err != nil || !fake.closed {
		t.Fatalf("Close = %v, fake.closed=%t", err, fake.closed)
	}
}

func TestContainerRuntimeExecutableHashDriftFailsBeforeStartAndCleans(t *testing.T) {
	runtime, fake, input := lspContainerRuntimeFixture(t)
	fake.executable = []byte("different-executable-after-image-admission")
	process, err := runtime.Start(context.Background(), input)
	if process != nil || !errors.Is(err, ErrContainerRuntimeIdentityDrift) {
		t.Fatalf("Start = %#v, %v", process, err)
	}
	events := strings.Join(fake.eventsSnapshot(), ",")
	if events != "image-inspect,create,container-inspect,cp,rm" || fake.started || !fake.removed {
		t.Fatalf("events=%s started=%t removed=%t", events, fake.started, fake.removed)
	}
}

func TestContainerRuntimeExecutableArchiveIsHostBounded(t *testing.T) {
	runtime, fake, input := lspContainerRuntimeFixture(t)
	runtime.maxExecutableBytes = 1 << 20
	fake.executable = bytes.Repeat([]byte("x"), int(runtime.maxExecutableBytes)+1)
	input.Profile.Runtime.ExecutableDigest = lspRawDigest(fake.executable)
	input.Profile.ContentHash, _ = templates.ComputeLanguageServerProfileContentHash(input.Profile.LanguageServerProfile)
	fake.profile = input.Profile
	process, err := runtime.Start(context.Background(), input)
	if process != nil || !errors.Is(err, ErrContainerRuntimeIdentityDrift) {
		t.Fatalf("Start = %#v, %v", process, err)
	}
	if fake.started || !fake.removed {
		t.Fatalf("oversized executable reached execution: started=%t removed=%t", fake.started, fake.removed)
	}
}

func TestContainerRuntimeReconcilesAmbiguousCreateByExactIdentity(t *testing.T) {
	runtime, fake, input := lspContainerRuntimeFixture(t)
	fake.createErr = errors.New("transport closed after daemon accepted create")
	process, err := runtime.Start(context.Background(), input)
	if process != nil || !errors.Is(err, ErrContainerRuntimeUnavailable) {
		t.Fatalf("Start = %#v, %v", process, err)
	}
	if got := strings.Join(fake.eventsSnapshot(), ","); got != "image-inspect,create,container-inspect,container-inspect,rm" {
		t.Fatalf("ambiguous-create reconciliation = %s", got)
	}
	if fake.started || !fake.removed {
		t.Fatalf("ambiguous create was not safely removed: started=%t removed=%t", fake.started, fake.removed)
	}
}

func TestContainerRuntimeRejectsExecutableUnavailableToFixedRuntimeUser(t *testing.T) {
	runtime, fake, input := lspContainerRuntimeFixture(t)
	fake.executableMode = 0o700
	process, err := runtime.Start(context.Background(), input)
	if process != nil || !errors.Is(err, ErrContainerRuntimeIdentityDrift) || fake.started || !fake.removed {
		t.Fatalf("Start = %#v, %v; started=%t removed=%t", process, err, fake.started, fake.removed)
	}
}

func TestContainerRuntimeRejectsRootUserAndResourceDriftBeforeExecution(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "root user", mutate: func(document map[string]any) {
			document["Config"].(map[string]any)["User"] = "0:0"
		}},
		{name: "writable rootfs", mutate: func(document map[string]any) {
			document["HostConfig"].(map[string]any)["ReadonlyRootfs"] = false
		}},
		{name: "network", mutate: func(document map[string]any) {
			document["HostConfig"].(map[string]any)["NetworkMode"] = "bridge"
		}},
		{name: "pid limit", mutate: func(document map[string]any) {
			document["HostConfig"].(map[string]any)["PidsLimit"] = 65
		}},
		{name: "memory", mutate: func(document map[string]any) {
			document["HostConfig"].(map[string]any)["Memory"] = int64(1 << 30)
		}},
		{name: "cpu", mutate: func(document map[string]any) {
			document["HostConfig"].(map[string]any)["NanoCpus"] = int64(2_000_000_000)
		}},
		{name: "tmp exec", mutate: func(document map[string]any) {
			document["HostConfig"].(map[string]any)["Tmpfs"].(map[string]string)["/tmp"] = "rw,exec,nosuid,nodev,size=268435456,mode=0700,uid=65532,gid=65532"
		}},
		{name: "writable workspace", mutate: func(document map[string]any) {
			document["Mounts"].([]map[string]any)[0]["RW"] = true
		}},
		{name: "image healthcheck", mutate: func(document map[string]any) {
			document["Config"].(map[string]any)["Healthcheck"] = map[string]any{
				"Test": []string{"CMD-SHELL", "curl localhost"},
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, fake, input := lspContainerRuntimeFixture(t)
			fake.mutateInspect = test.mutate
			if process, err := runtime.Start(context.Background(), input); process != nil ||
				!errors.Is(err, ErrContainerRuntimeIdentityDrift) {
				t.Fatalf("Start = %#v, %v", process, err)
			}
			if fake.started || !fake.removed || strings.Contains(strings.Join(fake.eventsSnapshot(), ","), "cp") {
				t.Fatalf("server ran before drift rejection: %v", fake.eventsSnapshot())
			}
		})
	}
}

func TestContainerRuntimeAcceptsEquivalentExactPodmanInspectShapes(t *testing.T) {
	runtime, fake, input := lspContainerRuntimeFixture(t)
	runtime.runtimeName = "podman"
	fake.imageID = strings.Repeat("b", 64)
	fake.mutateInspect = func(document map[string]any) {
		document["Name"] = languageServerContainerName(testConnection, lspRuntimeBinding)
		config := document["Config"].(map[string]any)
		config["Entrypoint"] = input.Profile.Runtime.ExecutablePath
		host := document["HostConfig"].(map[string]any)
		host["CapDrop"] = []string{"CAP_ALL"}
		host["NanoCpus"] = int64(0)
		host["CpuPeriod"] = int64(100_000)
		host["CpuQuota"] = int64(100_000)
		host["MemorySwap"] = int64(0)
		host["CgroupConf"] = map[string]string{"memory.swap.max": "0"}
	}
	process, err := runtime.Start(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Terminate(context.Background()); err != nil {
		t.Fatal(err)
	}
	create := fake.callsSnapshot()[1]
	if !containsExactString(create, "--read-only-tmpfs=false") {
		t.Fatalf("Podman create did not disable implicit writable tmpfs mounts: %#v", create)
	}
	if !containsExactString(create, "--cgroup-conf=memory.swap.max=0") || containsExactString(create, "--memory-swap") {
		t.Fatalf("Podman create did not use an exact cgroup-v2 no-swap policy: %#v", create)
	}
}

func TestContainerRuntimeRejectsMutableProfileShellAndUnsafeWorkspacePaths(t *testing.T) {
	runtime, fake, input := lspContainerRuntimeFixture(t)
	tests := []struct {
		name   string
		mutate func(*ContainerStartInput, *testing.T)
	}{
		{name: "image tag", mutate: func(value *ContainerStartInput, _ *testing.T) {
			value.Profile.Runtime.Image = "ghcr.io/worksflow/lsp:latest"
		}},
		{name: "shell", mutate: func(value *ContainerStartInput, _ *testing.T) {
			value.Profile.Runtime.ExecutablePath = "/bin/sh"
			value.Profile.Runtime.Argv = []string{"/bin/sh", "-c", "server --stdio"}
		}},
		{name: "path traversal", mutate: func(value *ContainerStartInput, _ *testing.T) {
			value.ServiceRoot = "apps/../outside"
		}},
		{name: "relative workspace", mutate: func(value *ContainerStartInput, _ *testing.T) {
			value.WorkspaceRoot = "workspace"
		}},
		{name: "workspace symlink", mutate: func(value *ContainerStartInput, test *testing.T) {
			realRoot := test.TempDir()
			link := filepath.Join(test.TempDir(), "workspace")
			if err := os.Symlink(realRoot, link); err != nil {
				test.Fatal(err)
			}
			value.WorkspaceRoot = link
		}},
		{name: "service symlink", mutate: func(value *ContainerStartInput, test *testing.T) {
			root := test.TempDir()
			outside := test.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "apps"), 0o700); err != nil {
				test.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "apps", "web")); err != nil {
				test.Fatal(err)
			}
			value.WorkspaceRoot = root
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := input
			test.mutate(&value, t)
			if process, err := runtime.Start(context.Background(), value); process != nil || err == nil {
				t.Fatalf("Start accepted unsafe input: %#v, %v", process, err)
			}
		})
	}
	if len(fake.callsSnapshot()) != 0 {
		t.Fatalf("unsafe input reached the CLI: %#v", fake.callsSnapshot())
	}
}

func TestContainerRuntimeFrameAndStderrOverflowFailClosed(t *testing.T) {
	t.Run("server frame", func(t *testing.T) {
		runtime, fake, input := lspContainerRuntimeFixture(t)
		process, err := runtime.Start(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			_, _ = fmt.Fprintf(fake.stream.stdoutWrite, "Content-Length: %d\r\n\r\n", input.Profile.EffectiveLimits.MaxFrameBytes+1)
		}()
		if _, err := process.ReadFrame(context.Background()); !errors.Is(err, ErrContainerRuntimeExhausted) {
			t.Fatalf("ReadFrame overflow = %v", err)
		}
		if _, err := process.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !fake.removed {
			t.Fatal("overflow did not remove container")
		}
		runtime.Close()
	})

	t.Run("client frame", func(t *testing.T) {
		runtime, fake, input := lspContainerRuntimeFixture(t)
		process, err := runtime.Start(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		value := bytes.Repeat([]byte("x"), int(input.Profile.EffectiveLimits.MaxFrameBytes)+1)
		if err := process.WriteFrame(context.Background(), value); !errors.Is(err, ErrContainerRuntimeExhausted) {
			t.Fatalf("WriteFrame overflow = %v", err)
		}
		if _, err := process.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !fake.removed {
			t.Fatal("overflow did not remove container")
		}
		runtime.Close()
	})

	t.Run("stderr", func(t *testing.T) {
		runtime, fake, input := lspContainerRuntimeFixture(t)
		process, err := runtime.Start(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			_, _ = fake.stream.stderrWrite.Write(bytes.Repeat([]byte("e"), int(input.Profile.EffectiveLimits.MaxFrameBytes)+1))
		}()
		exit, err := process.Wait(context.Background())
		if err != nil || !errors.Is(exit.Err, ErrContainerRuntimeExhausted) {
			t.Fatalf("Wait stderr overflow = %#v, %v", exit, err)
		}
		if int64(len(process.Stderr())) > input.Profile.EffectiveLimits.MaxFrameBytes || !fake.removed {
			t.Fatalf("stderr was not bounded: bytes=%d removed=%t", len(process.Stderr()), fake.removed)
		}
		runtime.Close()
	})
}

func TestContainerRuntimeReadinessIsVersionAndExactInspectOnly(t *testing.T) {
	t.Run("daemon baseline", func(t *testing.T) {
		runtime, fake, _ := lspContainerRuntimeFixture(t)
		if err := runtime.Readiness(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(fake.eventsSnapshot(), ","); got != "version" {
			t.Fatalf("baseline readiness claimed image state: %s", got)
		}
	})

	t.Run("exact images", func(t *testing.T) {
		runtime, fake, input := lspContainerRuntimeFixture(t)
		if err := runtime.Readiness(context.Background(), input.Profile, input.Profile); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(fake.eventsSnapshot(), ","); got != "version,image-inspect" {
			t.Fatalf("readiness performed mutation or duplicate inspect: %s", got)
		}
		for _, call := range fake.callsSnapshot() {
			if strings.Contains(strings.Join(call, " "), "pull") || containsExactString(call, "create") {
				t.Fatalf("readiness performed a mutation: %#v", call)
			}
		}
	})

	t.Run("invalid profile", func(t *testing.T) {
		runtime, fake, input := lspContainerRuntimeFixture(t)
		input.Profile.ContentHash = lspDigest("f")
		if err := runtime.Readiness(context.Background(), input.Profile); !errors.Is(err, ErrContainerRuntimeInvalid) {
			t.Fatalf("Readiness = %v", err)
		}
		if calls := fake.callsSnapshot(); len(calls) != 0 {
			t.Fatalf("invalid readiness profile reached CLI: %#v", calls)
		}
	})
}

func TestContainerRuntimeEnforcesActiveProcessLimitBeforeCLI(t *testing.T) {
	runtime, fake, input := lspContainerRuntimeFixture(t)
	runtime.maxProcesses = 1
	first, err := runtime.Start(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	callCount := len(fake.callsSnapshot())
	secondInput := input
	secondInput.BindingID = "10000000-0000-4000-8000-000000000014"
	second, err := runtime.Start(context.Background(), secondInput)
	if second != nil || !errors.Is(err, ErrContainerRuntimeExhausted) {
		t.Fatalf("second Start = %#v, %v", second, err)
	}
	if got := len(fake.callsSnapshot()); got != callCount {
		t.Fatalf("exhausted Start reached CLI: calls %d -> %d", callCount, got)
	}
	if err := first.Terminate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestContainerRuntimeCanceledTerminateRetriesExactRemoval(t *testing.T) {
	runtime, fake, input := lspContainerRuntimeFixture(t)
	process, err := runtime.Start(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := process.Terminate(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Terminate = %v", err)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if _, err := process.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
	if !fake.removed {
		t.Fatal("background waiter did not retry exact container removal")
	}
}

func TestContainerRuntimeConcurrentCloseIsCached(t *testing.T) {
	runtime, fake, input := lspContainerRuntimeFixture(t)
	sentinel := errors.New("close failed")
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fake.closeErr = sentinel
	fake.closeStarted = started
	fake.closeRelease = release

	const callers = 8
	results := make(chan error, callers)
	for range callers {
		go func() { results <- runtime.Close() }()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first Close did not reach CLI")
	}
	if err := runtime.Readiness(context.Background()); !errors.Is(err, ErrContainerRuntimeClosed) {
		t.Fatalf("Readiness during Close = %v", err)
	}
	if process, err := runtime.Start(context.Background(), input); process != nil || !errors.Is(err, ErrContainerRuntimeClosed) {
		t.Fatalf("Start during Close = %#v, %v", process, err)
	}
	close(release)
	for range callers {
		if err := <-results; !errors.Is(err, sentinel) {
			t.Fatalf("Close = %v", err)
		}
	}
	closed, closeCalls := fake.closeSnapshot()
	if !closed || closeCalls != 1 {
		t.Fatalf("CLI close state = closed:%t calls:%d", closed, closeCalls)
	}
}

func TestContainerRuntimeValidatesBinaryAndDaemonHostWithoutPATHOrSymlinks(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "docker")
	if err := os.WriteFile(binary, []byte("not executed"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := ContainerRuntimeConfig{
		RuntimeBinary: binary, CommandTimeout: time.Second, CLIOutputBytes: 1 << 20,
	}
	runtime, err := NewContainerRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	osCLI, ok := runtime.cli.(*osContainerRuntimeCLI)
	if !ok {
		t.Fatalf("CLI = %T", runtime.cli)
	}
	if err := os.Chmod(binary, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := osCLI.validateAuthority(); !errors.Is(err, ErrContainerRuntimeIdentityDrift) {
		t.Fatalf("mutated runtime binary authority = %v", err)
	}
	if err := os.Chmod(binary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}

	for _, invalid := range []string{"docker", filepath.Join(root, "not-docker")} {
		bad := config
		bad.RuntimeBinary = invalid
		if got, err := NewContainerRuntime(bad); got != nil || err == nil {
			t.Fatalf("runtime binary %q accepted: %#v, %v", invalid, got, err)
		}
	}
	symlink := filepath.Join(root, "podman")
	if err := os.Symlink(binary, symlink); err != nil {
		t.Fatal(err)
	}
	bad := config
	bad.RuntimeBinary = symlink
	if got, err := NewContainerRuntime(bad); got != nil || err == nil {
		t.Fatalf("runtime symlink accepted: %#v, %v", got, err)
	}
	bad = config
	bad.DaemonHost = "tcp://127.0.0.1:2375"
	if got, err := NewContainerRuntime(bad); got != nil || err == nil {
		t.Fatalf("plaintext daemon accepted: %#v, %v", got, err)
	}

	socketPath := filepath.Join(root, "runtime.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	bad = config
	bad.DaemonHost = "unix://" + socketPath
	accepted, err := NewContainerRuntime(bad)
	if err != nil {
		t.Fatal(err)
	}
	acceptedCLI, ok := accepted.cli.(*osContainerRuntimeCLI)
	if !ok {
		t.Fatalf("CLI = %T", accepted.cli)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := acceptedCLI.validateAuthority(); !errors.Is(err, ErrContainerRuntimeIdentityDrift) {
		t.Fatalf("mutated daemon socket authority = %v", err)
	}
	accepted.Close()
	socketLink := filepath.Join(root, "runtime-link.sock")
	if err := os.Symlink(socketPath, socketLink); err != nil {
		t.Fatal(err)
	}
	bad.DaemonHost = "unix://" + socketLink
	if got, err := NewContainerRuntime(bad); got != nil || err == nil {
		t.Fatalf("daemon symlink accepted: %#v, %v", got, err)
	}

	podman := filepath.Join(root, "podman-bin", "podman")
	if err := os.MkdirAll(filepath.Dir(podman), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(podman, []byte("not executed"), 0o700); err != nil {
		t.Fatal(err)
	}
	bad = config
	bad.RuntimeBinary = podman
	bad.DaemonHost = ""
	if got, err := NewContainerRuntime(bad); got != nil || err == nil {
		t.Fatalf("daemonless Podman authority accepted: %#v, %v", got, err)
	}
	bad.DaemonHost = "unix://" + socketPath
	podmanRuntime, err := NewContainerRuntime(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := podmanRuntime.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestContainerRuntimeStartupTimeoutFailsClosedAndCleans(t *testing.T) {
	runtime, fake, input := lspContainerRuntimeFixture(t)
	input.Profile.Limits.StartupTimeoutMillis = 25
	input.Profile.EffectiveLimits.StartupTimeoutMillis = 25
	input.Profile.ContentHash, _ = templates.ComputeLanguageServerProfileContentHash(input.Profile.LanguageServerProfile)
	fake.profile = input.Profile
	fake.mutateInspect = func(document map[string]any) {
		document["State"].(map[string]any)["Running"] = false
	}
	process, err := runtime.Start(context.Background(), input)
	if process != nil || err == nil || !fake.removed {
		t.Fatalf("Start timeout = %#v, %v, removed=%t", process, err, fake.removed)
	}
}

func TestContainerRuntimeCLIOutputBufferIsBounded(t *testing.T) {
	buffer := &strictRuntimeBuffer{limit: 3}
	count, err := buffer.Write([]byte("overflow"))
	if count != len("overflow") || !errors.Is(err, ErrContainerRuntimeExhausted) ||
		!buffer.wasOverflowed() || string(buffer.bytes()) != "ove" {
		t.Fatalf("bounded write = count:%d err:%v overflow:%t value:%q", count, err, buffer.wasOverflowed(), buffer.bytes())
	}
}

// This is intentionally not a default CI qualification. A real run requires
// an externally approved exact ProfileIdentity and its pre-provisioned image;
// a local tag, mock server, or skipped run is never production evidence.
func TestContainerRuntimeRealQualifiedImage(t *testing.T) {
	encoded := os.Getenv("WORKSFLOW_LSP_QUALIFIED_PROFILE_JSON")
	headJSON := os.Getenv("WORKSFLOW_LSP_QUALIFIED_HEAD_JSON")
	binary := os.Getenv("WORKSFLOW_LSP_QUALIFIED_RUNTIME_BINARY")
	workspace := os.Getenv("WORKSFLOW_LSP_QUALIFIED_WORKSPACE")
	serviceRoot := os.Getenv("WORKSFLOW_LSP_QUALIFIED_SERVICE_ROOT")
	if encoded == "" || headJSON == "" || binary == "" || workspace == "" || serviceRoot == "" {
		t.Skip("approved digest-pinned LSP image/profile fixture is unavailable; this skip is not qualification evidence")
	}
	var profile ProfileIdentity
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&profile)
	var trailing any
	trailingErr := decoder.Decode(&trailing)
	if decodeErr != nil || !errors.Is(trailingErr, io.EOF) || profile.Validate() != nil {
		t.Fatalf("qualified profile is invalid: %v / %v", decodeErr, trailingErr)
	}
	head, err := DecodeSandboxHeadFence([]byte(headJSON))
	if err != nil {
		t.Fatalf("qualified SandboxHeadFence is invalid: %v", err)
	}
	runtime, err := NewContainerRuntime(ContainerRuntimeConfig{
		RuntimeBinary: binary, DaemonHost: os.Getenv("WORKSFLOW_LSP_QUALIFIED_DAEMON_HOST"),
		CommandTimeout: 10 * time.Second, CLIOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := runtime.Readiness(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	process, err := runtime.Start(context.Background(), ContainerStartInput{
		Profile: profile, WorkspaceRoot: workspace,
		ServiceRoot:  serviceRoot,
		ConnectionID: testConnection, BindingID: lspRuntimeBinding,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		terminateCtx, cancel := context.WithTimeout(
			context.Background(),
			time.Duration(profile.EffectiveLimits.ShutdownTimeoutMillis)*time.Millisecond,
		)
		defer cancel()
		if err := process.Terminate(terminateCtx); err != nil {
			t.Errorf("qualified process termination: %v", err)
		}
	}()
	request, err := BuildServerInitializeRequest(ServerInitializeInput{
		Head: head, Profile: profile, WorkspaceRootPath: serviceRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	initializeCtx, cancelInitialize := context.WithTimeout(
		context.Background(),
		time.Duration(profile.EffectiveLimits.StartupTimeoutMillis)*time.Millisecond,
	)
	defer cancelInitialize()
	if err := process.WriteFrame(initializeCtx, request); err != nil {
		t.Fatal(err)
	}
	response, err := process.ReadFrame(initializeCtx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeServerInitializeResponse(response, profile); err != nil {
		t.Fatalf("qualified server initialize identity/capability response: %v", err)
	}
}

func lspExecutableTar(executablePath string, value []byte, mode int64, uid, gid int) []byte {
	var result bytes.Buffer
	writer := tar.NewWriter(&result)
	_ = writer.WriteHeader(&tar.Header{
		Name: filepath.Base(executablePath), Mode: mode, Uid: uid, Gid: gid,
		Size: int64(len(value)), Typeflag: tar.TypeReg,
	})
	_, _ = writer.Write(value)
	_ = writer.Close()
	return result.Bytes()
}

func lspRawDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
