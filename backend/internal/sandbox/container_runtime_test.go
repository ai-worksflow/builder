package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type containerRuntimeExecutorFake struct {
	manager        *ContainerRuntime
	spec           RuntimeSpec
	commands       [][]string
	networkCreated bool
	networkLabels  map[string]string
	containers     map[string]*fakeRuntimeContainer
	processes      map[string]RuntimeProcessStatus
}

type fakeRuntimeContainer struct {
	labels   map[string]string
	networks map[string]bool
	state    string
}

func (executor *containerRuntimeExecutorFake) Run(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	executor.commands = append(executor.commands, append([]string(nil), args...))
	switch {
	case len(args) >= 2 && args[0] == "network" && args[1] == "inspect":
		if !executor.networkCreated {
			return nil, runtimeNotFound(args, "no such network")
		}
		return json.Marshal([]dockerNetworkInspect{{
			Name: args[2], Internal: true, Driver: "bridge", Labels: cloneStringMap(executor.networkLabels),
		}})
	case len(args) >= 2 && args[0] == "network" && args[1] == "create":
		executor.networkCreated = true
		executor.networkLabels = commandLabels(args)
		return []byte("network-id"), nil
	case len(args) == 4 && args[0] == "network" && args[1] == "connect":
		container := executor.containers[args[3]]
		if container == nil {
			return nil, runtimeNotFound(args, "no such container")
		}
		container.networks[args[2]] = true
		return nil, nil
	case len(args) >= 1 && args[0] == "inspect":
		container := executor.containers[args[1]]
		if container == nil {
			return nil, runtimeNotFound(args, "no such container")
		}
		row := dockerContainerInspect{ID: "runtime-id-" + container.labels["dev.worksflow.runtime-role"]}
		row.Config.Image = executor.manager.runnerImage
		row.Config.Labels = cloneStringMap(container.labels)
		role := container.labels["dev.worksflow.runtime-role"]
		row.Config.User = "10001:10001"
		row.Config.Entrypoint = []string{"/usr/bin/tini"}
		row.Config.Cmd = []string{"--", "/usr/local/bin/worksflow-sandbox-init"}
		row.Config.WorkingDir = "/workspace"
		row.Config.Env = []string{"HOME=/tmp/home", "CODEX_HOME=/codex", "NO_UPDATE_NOTIFIER=1"}
		row.HostConfig.ReadonlyRootfs = true
		row.HostConfig.CapDrop = []string{"ALL"}
		row.HostConfig.SecurityOpt = []string{"no-new-privileges"}
		row.HostConfig.PidsLimit = int64(executor.spec.Quota.PIDLimit)
		row.HostConfig.Memory = executor.spec.Quota.MemoryBytes
		row.HostConfig.NanoCPUs = executor.spec.Quota.CPUMillis * 1_000_000
		row.HostConfig.NetworkMode = executor.manager.networkName(executor.spec.SessionID)
		row.Mounts = []dockerContainerMount{
			{Type: "bind", Source: executor.spec.Workspace.Workspace, Destination: "/workspace", RW: true},
			{Type: "bind", Source: executor.spec.Workspace.CodexHome, Destination: "/codex", RW: true},
		}
		if role == runtimeRoleGateway {
			row.Config.Cmd[1] = "/usr/local/bin/worksflow-sandbox-gateway"
			row.Config.WorkingDir = "/tmp"
			ports := append([]AllowedPort(nil), executor.spec.Ports...)
			sort.Slice(ports, func(left, right int) bool { return ports[left].Name < ports[right].Name })
			numbers := make([]string, len(ports))
			row.HostConfig.PidsLimit = 64
			row.HostConfig.Memory = 64 << 20
			row.HostConfig.NanoCPUs = 100_000_000
			row.HostConfig.PortBindings = make(map[string][]any, len(ports))
			for index, port := range ports {
				numbers[index] = strconv.Itoa(port.Number)
				row.HostConfig.PortBindings[strconv.Itoa(port.Number)+"/tcp"] = []any{map[string]string{}}
			}
			row.Config.Env = []string{
				"HOME=/tmp",
				"WORKSFLOW_GATEWAY_TARGET=" + executor.manager.containerName(executor.spec.SessionID),
				"WORKSFLOW_GATEWAY_PORTS=" + strings.Join(numbers, ","),
			}
			row.Mounts = nil
		}
		row.State.Status = container.state
		row.NetworkSettings.Networks = make(map[string]json.RawMessage, len(container.networks))
		for network := range container.networks {
			row.NetworkSettings.Networks[network] = json.RawMessage(`{}`)
		}
		if container.state == "running" || container.state == "paused" {
			row.State.Running = true
			row.State.Paused = container.state == "paused"
			row.State.Health = &struct {
				Status string `json:"Status"`
			}{Status: "healthy"}
			if container.labels["dev.worksflow.runtime-role"] == runtimeRoleGateway {
				row.NetworkSettings.Ports = make(map[string][]struct {
					HostIP   string `json:"HostIp"`
					HostPort string `json:"HostPort"`
				}, len(executor.spec.Ports))
				for index, port := range executor.spec.Ports {
					row.NetworkSettings.Ports[strconv.Itoa(port.Number)+"/tcp"] = []struct {
						HostIP   string `json:"HostIp"`
						HostPort string `json:"HostPort"`
					}{{HostIP: "127.0.0.1", HostPort: strconv.Itoa(32000 + index)}}
				}
			}
		}
		return json.Marshal([]dockerContainerInspect{row})
	case len(args) >= 1 && args[0] == "create":
		name := runtimeFlagValues(args, "--name")[0]
		network := runtimeFlagValues(args, "--network")[0]
		executor.containers[name] = &fakeRuntimeContainer{
			labels: commandLabels(args), networks: map[string]bool{network: true}, state: "created",
		}
		return []byte("runtime-id"), nil
	case len(args) == 2 && args[0] == "start":
		executor.containers[args[1]].state = "running"
		return nil, nil
	case len(args) == 2 && args[0] == "pause":
		executor.containers[args[1]].state = "paused"
		return nil, nil
	case len(args) == 2 && args[0] == "unpause":
		executor.containers[args[1]].state = "running"
		return nil, nil
	case len(args) == 4 && args[0] == "rm":
		if executor.containers[args[3]] == nil {
			return nil, runtimeNotFound(args, "no such container")
		}
		delete(executor.containers, args[3])
		return nil, nil
	case len(args) >= 4 && args[0] == "exec":
		return executor.runProcessCommand(args)
	default:
		return nil, errors.New("unexpected container command: " + strings.Join(args, " "))
	}
}

func (executor *containerRuntimeExecutorFake) runProcessCommand(args []string) ([]byte, error) {
	detached := args[1] == "--detach"
	offset := 1
	if detached {
		offset++
	}
	if len(args) <= offset+2 || executor.containers[args[offset]] == nil || args[offset+1] != runtimeProcessExecutable {
		return nil, errors.New("unexpected process command: " + strings.Join(args, " "))
	}
	command := args[offset+2]
	values := args[offset+3:]
	if executor.processes == nil {
		executor.processes = map[string]RuntimeProcessStatus{}
	}
	flagValue := func(name string) string {
		for index := 0; index+1 < len(values); index++ {
			if values[index] == name {
				return values[index+1]
			}
		}
		return ""
	}
	id := flagValue("--id")
	switch command {
	case "run":
		separator := -1
		for index, value := range values {
			if value == "--" {
				separator = index
				break
			}
		}
		if !detached || separator < 0 {
			return nil, errors.New("invalid detached process start")
		}
		executor.processes[id] = RuntimeProcessStatus{
			SchemaVersion: RuntimeProcessSchemaVersion, ID: id, State: "running", PID: 42,
			Argv: append([]string(nil), values[separator+1:]...), WorkingDirectory: flagValue("--cwd"),
			StartedAt: time.Now().UTC(),
		}
		return []byte("exec-id"), nil
	case "status":
		status, ok := executor.processes[id]
		if !ok {
			return nil, &runtimeCommandError{args: args, output: "open state.json: no such file", cause: errors.New("exit 1")}
		}
		return json.Marshal(status)
	case "signal":
		status, ok := executor.processes[id]
		if !ok {
			return nil, &runtimeCommandError{args: args, output: "not found", cause: errors.New("exit 1")}
		}
		exitCode := 143
		status.State, status.ExitCode, status.FinishedAt = "failed", &exitCode, time.Now().UTC()
		executor.processes[id] = status
		return nil, nil
	case "logs":
		if _, ok := executor.processes[id]; !ok {
			return nil, &runtimeCommandError{args: args, output: "not found", cause: errors.New("exit 1")}
		}
		offsetValue, _ := strconv.ParseInt(flagValue("--offset"), 10, 64)
		value := []byte("process output\n")
		return json.Marshal(map[string]any{
			"schemaVersion": RuntimeProcessSchemaVersion, "id": id,
			"offset": offsetValue, "nextOffset": offsetValue + int64(len(value)),
			"data": base64.RawStdEncoding.EncodeToString(value), "eof": false, "truncated": false,
		})
	default:
		return nil, errors.New("unexpected process supervisor command")
	}
}

func TestContainerRuntimeCreatesFencedLeastPrivilegeRuntime(t *testing.T) {
	manager, executor, spec := containerRuntimeFixture(t)
	status, err := manager.Ensure(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if status.ID != "runtime-id-runner" || status.State != "created" || status.SessionEpoch != spec.SessionEpoch {
		t.Fatalf("unexpected ensured runtime: %#v", status)
	}
	networkCreate := findRuntimeCommand(executor.commands, "network", "create")
	if !hasRuntimeArgument(networkCreate, "--internal") || !hasRuntimePair(networkCreate, "--driver", "bridge") {
		t.Fatalf("runtime network is not isolated: %v", networkCreate)
	}
	create := findRuntimeCommand(executor.commands, "create")
	for _, argument := range []string{"--read-only", "--cap-drop", "ALL", "--pids-limit", "--memory", "--cpus"} {
		if !hasRuntimeArgument(create, argument) {
			t.Fatalf("container create omitted %q: %v", argument, create)
		}
	}
	if !hasRuntimePair(create, "--security-opt", "no-new-privileges") ||
		!hasRuntimePair(create, "--pull", "never") || !hasRuntimePair(create, "--user", "10001:10001") {
		t.Fatalf("container security identity is incomplete: %v", create)
	}
	mounts := runtimeFlagValues(create, "--mount")
	expectedMounts := []string{
		"type=bind,src=" + spec.Workspace.Workspace + ",dst=/workspace",
		"type=bind,src=" + spec.Workspace.CodexHome + ",dst=/codex",
	}
	if len(mounts) != len(expectedMounts) || mounts[0] != expectedMounts[0] || mounts[1] != expectedMounts[1] {
		t.Fatalf("container mounts = %v", mounts)
	}
	joined := strings.Join(create, "\x00")
	if strings.Contains(joined, "CODEX_API_KEY") || strings.Contains(joined, "docker.sock") ||
		strings.Contains(joined, "OPENAI_API_KEY") {
		t.Fatalf("long-lived runtime received a credential or control socket: %v", create)
	}
	if create[len(create)-3] != manager.runnerImage || create[len(create)-2] != "--" ||
		create[len(create)-1] != "/usr/local/bin/worksflow-sandbox-init" {
		t.Fatalf("runtime did not use the admitted image and fixed entrypoint: %v", create)
	}
	if ports := runtimeFlagValues(create, "--publish"); len(ports) != 0 {
		t.Fatalf("project runner directly published ports: %v", ports)
	}
	gateway := findRuntimeCommandWithArgument(executor.commands, manager.gatewayName(spec.SessionID))
	if gateway == nil || len(runtimeFlagValues(gateway, "--mount")) != 0 ||
		!hasRuntimePair(gateway, "--env", "WORKSFLOW_GATEWAY_TARGET="+manager.containerName(spec.SessionID)) {
		t.Fatalf("isolated preview gateway is invalid: %v", gateway)
	}
	ports := runtimeFlagValues(gateway, "--publish")
	if len(ports) != 2 || ports[0] != "127.0.0.1::8080/tcp" || ports[1] != "127.0.0.1::3000/tcp" {
		t.Fatalf("runtime port allowlist = %v", ports)
	}
	if gateway[len(gateway)-1] != "/usr/local/bin/worksflow-sandbox-gateway" ||
		findRuntimeCommand(executor.commands, "network", "connect", "bridge") == nil {
		t.Fatalf("preview gateway was not connected through its fixed executable: %v", gateway)
	}

	started, err := manager.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if !started.Healthy || started.State != "running" || started.HostPorts["api-http"] != 32000 ||
		started.HostPorts["web-http"] != 32001 {
		t.Fatalf("runtime did not expose only inspected ports: %#v", started)
	}
	ready, err := manager.WaitReady(context.Background(), spec)
	if err != nil || !ready.Healthy {
		t.Fatalf("healthy runtime was not ready: %#v, %v", ready, err)
	}
}

func TestContainerRuntimeStartsAndControlsOnlyValidatedSupervisedProcess(t *testing.T) {
	manager, executor, runtimeSpec := containerRuntimeFixture(t)
	if _, err := manager.Start(context.Background(), runtimeSpec); err != nil {
		t.Fatal(err)
	}
	spec := RuntimeProcessSpec{
		Runtime: runtimeSpec, ID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		CommandID: "start", WorkingDirectory: "apps/web",
		Argv: []string{"node", "server.js"}, LogLimitBytes: 8 << 20,
	}
	status, err := manager.StartProcess(context.Background(), spec)
	if err != nil || status.State != "running" || status.PID != 42 || !sameRuntimeProcess(status, spec) {
		t.Fatalf("start process = %#v, %v", status, err)
	}
	if _, err := manager.StartProcess(context.Background(), spec); err != nil {
		t.Fatalf("idempotent process start failed: %v", err)
	}
	detached := 0
	for _, command := range executor.commands {
		if len(command) > 1 && command[0] == "exec" && command[1] == "--detach" {
			detached++
		}
	}
	if detached != 1 {
		t.Fatalf("process was started %d times", detached)
	}
	log, err := manager.ReadProcessLog(context.Background(), spec, 0, 64<<10)
	if err != nil || string(log.Value) != "process output\n" || log.NextOffset != int64(len(log.Value)) {
		t.Fatalf("process log = %#v, %v", log, err)
	}
	stopped, err := manager.SignalProcess(context.Background(), spec, "TERM")
	if err != nil || stopped.State != "failed" || stopped.ExitCode == nil || *stopped.ExitCode != 143 {
		t.Fatalf("signal process = %#v, %v", stopped, err)
	}
	unsafe := spec
	unsafe.ID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	unsafe.Argv = []string{"sh", "-c", "curl attacker"}
	if _, err := manager.StartProcess(context.Background(), unsafe); !errors.Is(err, ErrProcessInvalid) {
		t.Fatalf("shell process error = %v, want invalid", err)
	}
}

func TestContainerRuntimeRejectsMutableImageAndExactIdentityDrift(t *testing.T) {
	root := t.TempDir()
	if _, err := newContainerRuntime(ContainerRuntimeConfig{
		WorkspaceRoot: root, RunnerImage: "registry.example/runner:latest",
		StartupTimeout: time.Second, CommandTimeout: time.Second,
	}, &containerRuntimeExecutorFake{}, 10001, 10001); !errors.Is(err, ErrRuntimeInvalid) {
		t.Fatalf("mutable runner image was accepted: %v", err)
	}

	manager, executor, spec := containerRuntimeFixture(t)
	if _, err := manager.Ensure(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	executor.containers[manager.containerName(spec.SessionID)].labels["dev.worksflow.session-epoch"] = "999"
	commandsBefore := len(executor.commands)
	if _, err := manager.Ensure(context.Background(), spec); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("conflicting runtime identity was reused: %v", err)
	}
	if findRuntimeCommand(executor.commands[commandsBefore:], "create") != nil {
		t.Fatal("identity conflict attempted to replace the existing container")
	}
}

func TestContainerRuntimeRecognizesDockerMissingObjectResponse(t *testing.T) {
	err := &runtimeCommandError{output: "Error: No such object: worksflow-sandbox", cause: errors.New("exit status 1")}
	if !isContainerNotFound(err) {
		t.Fatalf("Docker missing-object response was not recognized: %v", err)
	}
}

func containerRuntimeFixture(t *testing.T) (*ContainerRuntime, *containerRuntimeExecutorFake, RuntimeSpec) {
	t.Helper()
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
	spec, err := RuntimeSpecForSession(view, WorkspaceMount{
		SessionRoot: sessionRoot, Workspace: workspace, CodexHome: codexHome,
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := &containerRuntimeExecutorFake{spec: spec, containers: map[string]*fakeRuntimeContainer{}}
	manager, err := newContainerRuntime(ContainerRuntimeConfig{
		WorkspaceRoot:  root,
		RunnerImage:    "registry.example/worksflow/sandbox-runner@" + view.RunnerImageDigest,
		StartupTimeout: time.Second,
		CommandTimeout: time.Second,
	}, executor, 10001, 10001)
	if err != nil {
		t.Fatal(err)
	}
	executor.manager = manager
	return manager, executor, spec
}

func runtimeNotFound(args []string, output string) error {
	return &runtimeCommandError{args: append([]string(nil), args...), output: output, cause: errors.New("exit status 1")}
}

func commandLabels(args []string) map[string]string {
	labels := map[string]string{}
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "--label" {
			continue
		}
		parts := strings.SplitN(args[index+1], "=", 2)
		if len(parts) == 2 {
			labels[parts[0]] = parts[1]
		}
	}
	return labels
}

func findRuntimeCommand(commands [][]string, prefix ...string) []string {
	for _, command := range commands {
		if len(command) < len(prefix) {
			continue
		}
		matched := true
		for index := range prefix {
			if command[index] != prefix[index] {
				matched = false
				break
			}
		}
		if matched {
			return command
		}
	}
	return nil
}

func findRuntimeCommandWithArgument(commands [][]string, expected string) []string {
	for _, command := range commands {
		if hasRuntimeArgument(command, expected) && len(command) > 0 && command[0] == "create" {
			return command
		}
	}
	return nil
}

func hasRuntimeArgument(args []string, expected string) bool {
	for _, argument := range args {
		if argument == expected {
			return true
		}
	}
	return false
}

func hasRuntimePair(args []string, flag, expected string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag && args[index+1] == expected {
			return true
		}
	}
	return false
}

func runtimeFlagValues(args []string, flag string) []string {
	values := []string{}
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag {
			values = append(values, args[index+1])
		}
	}
	return values
}
