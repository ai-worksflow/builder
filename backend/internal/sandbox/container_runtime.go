package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sandboxRuntimeContract = "sandbox-runtime/v1"

const (
	runtimeRoleRunner  = "runner"
	runtimeRoleGateway = "gateway"
)

var immutableRunnerImagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:-]{0,255}@sha256:[a-f0-9]{64}$`)
var runtimeNetworkPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type ContainerRuntimeConfig struct {
	RuntimeBinary      string
	DaemonHost         string
	WorkspaceRoot      string
	RunnerImage        string
	GatewayNetwork     string
	GatewayBindAddress string
	StartupTimeout     time.Duration
	CommandTimeout     time.Duration
	OutputLimit        int
}

type runtimeCommandExecutor interface {
	Run(context.Context, ...string) ([]byte, error)
}

type ContainerRuntime struct {
	executor           runtimeCommandExecutor
	streamer           runtimeStreamExecutor
	workspaceRoot      string
	runnerImage        string
	runnerDigest       string
	gatewayNetwork     string
	gatewayBindAddress string
	startupTimeout     time.Duration
	commandTimeout     time.Duration
	uid                int
	gid                int
	closeExecutor      func() error
}

func NewContainerRuntime(config ContainerRuntimeConfig) (*ContainerRuntime, error) {
	binary := strings.TrimSpace(config.RuntimeBinary)
	if binary == "" {
		binary = "docker"
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("%w: locate container runtime: %v", ErrRuntimeUnavailable, err)
	}
	base := strings.ToLower(filepath.Base(path))
	if base != "docker" && base != "podman" {
		return nil, fmt.Errorf("%w: runtime must be docker or podman", ErrRuntimeInvalid)
	}
	daemonHost, err := normalizeRuntimeDaemonHost(config.DaemonHost)
	if err != nil {
		return nil, err
	}
	configDirectory, err := os.MkdirTemp("", "worksflow-sandbox-runtime-config-")
	if err != nil {
		return nil, fmt.Errorf("%w: create isolated client configuration: %v", ErrRuntimeUnavailable, err)
	}
	executor := &containerCLIExecutor{
		path: path, environment: runtimeClientEnvironment(configDirectory, daemonHost),
		outputLimit: config.OutputLimit,
	}
	manager, err := newContainerRuntime(config, executor, os.Getuid(), os.Getgid())
	if err != nil {
		_ = os.RemoveAll(configDirectory)
		return nil, err
	}
	manager.closeExecutor = func() error { return os.RemoveAll(configDirectory) }
	return manager, nil
}

func newContainerRuntime(
	config ContainerRuntimeConfig,
	executor runtimeCommandExecutor,
	uid, gid int,
) (*ContainerRuntime, error) {
	root := strings.TrimSpace(config.WorkspaceRoot)
	if executor == nil || root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root ||
		strings.ContainsAny(root, ",\r\n\x00") {
		return nil, ErrRuntimeInvalid
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: workspace root must be an existing real directory", ErrRuntimeInvalid)
	}
	image := strings.TrimSpace(config.RunnerImage)
	if !immutableRunnerImagePattern.MatchString(image) {
		return nil, fmt.Errorf("%w: runner image must use an immutable repository digest", ErrRuntimeInvalid)
	}
	digest := image[strings.LastIndex(image, "@")+1:]
	if !validDigest(digest) {
		return nil, ErrRuntimeInvalid
	}
	if config.StartupTimeout <= 0 || config.StartupTimeout > 10*time.Minute ||
		config.CommandTimeout <= 0 || config.CommandTimeout > time.Minute || uid < 1 || gid < 1 {
		return nil, ErrRuntimeInvalid
	}
	gatewayNetwork := strings.TrimSpace(config.GatewayNetwork)
	if gatewayNetwork == "" {
		gatewayNetwork = "bridge"
	}
	if !runtimeNetworkPattern.MatchString(gatewayNetwork) || gatewayNetwork == "host" || gatewayNetwork == "none" {
		return nil, fmt.Errorf("%w: gateway network is invalid", ErrRuntimeInvalid)
	}
	gatewayBindAddress := strings.TrimSpace(config.GatewayBindAddress)
	if gatewayBindAddress == "" {
		gatewayBindAddress = "127.0.0.1"
	}
	if net.ParseIP(gatewayBindAddress) == nil {
		return nil, fmt.Errorf("%w: gateway bind address must be an IP address", ErrRuntimeInvalid)
	}
	streamer, _ := executor.(runtimeStreamExecutor)
	return &ContainerRuntime{
		executor: executor, workspaceRoot: root, runnerImage: image, runnerDigest: digest,
		streamer:           streamer,
		gatewayNetwork:     gatewayNetwork,
		gatewayBindAddress: gatewayBindAddress,
		startupTimeout:     config.StartupTimeout, commandTimeout: config.CommandTimeout,
		uid: uid, gid: gid,
	}, nil
}

func (manager *ContainerRuntime) Close() error {
	if manager == nil || manager.closeExecutor == nil {
		return nil
	}
	err := manager.closeExecutor()
	manager.closeExecutor = nil
	return err
}

func (manager *ContainerRuntime) Readiness(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return ErrRuntimeUnavailable
	}
	if _, err := manager.runCommand(ctx, "version"); err != nil {
		return fmt.Errorf("%w: daemon version: %v", ErrRuntimeUnavailable, err)
	}
	if _, err := manager.runCommand(ctx, "image", "inspect", manager.runnerImage); err != nil {
		return fmt.Errorf("%w: admitted runner image is not pre-provisioned: %v", ErrRuntimeUnavailable, err)
	}
	return nil
}

func (manager *ContainerRuntime) Ensure(ctx context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	if err := manager.validate(ctx, spec); err != nil {
		return RuntimeStatus{}, err
	}
	if err := manager.ensureNetwork(ctx, spec); err != nil {
		return RuntimeStatus{}, err
	}
	if _, err := manager.ensureContainer(ctx, spec, runtimeRoleRunner); err != nil {
		return RuntimeStatus{}, err
	}
	if len(spec.Ports) > 0 {
		if _, err := manager.ensureContainer(ctx, spec, runtimeRoleGateway); err != nil {
			return RuntimeStatus{}, err
		}
	}
	return manager.inspect(ctx, spec)
}

func (manager *ContainerRuntime) Start(ctx context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	if _, err := manager.Ensure(ctx, spec); err != nil {
		return RuntimeStatus{}, err
	}
	roles := []string{runtimeRoleRunner}
	if len(spec.Ports) > 0 {
		roles = append(roles, runtimeRoleGateway)
	}
	for _, role := range roles {
		status, err := manager.inspectRole(ctx, spec, role)
		if err != nil {
			return RuntimeStatus{}, err
		}
		if status.State == "paused" {
			return RuntimeStatus{}, fmt.Errorf("%w: paused %s container must use resume", ErrRuntimeConflict, role)
		}
		if status.State == "running" {
			continue
		}
		if _, err := manager.runCommand(ctx, "start", manager.roleContainerName(spec.SessionID, role)); err != nil {
			return RuntimeStatus{}, fmt.Errorf("%w: start %s container: %v", ErrRuntimeUnavailable, role, err)
		}
	}
	return manager.inspect(ctx, spec)
}

func (manager *ContainerRuntime) WaitReady(ctx context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	if err := manager.validate(ctx, spec); err != nil {
		return RuntimeStatus{}, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, manager.startupTimeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := manager.inspect(waitCtx, spec)
		if err == nil && status.State == "running" && status.Healthy {
			return status, nil
		}
		if err != nil && !isContainerNotFound(err) {
			return RuntimeStatus{}, err
		}
		select {
		case <-waitCtx.Done():
			return RuntimeStatus{}, fmt.Errorf("%w: %v", ErrRuntimeNotReady, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (manager *ContainerRuntime) Suspend(ctx context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	if err := manager.validate(ctx, spec); err != nil {
		return RuntimeStatus{}, err
	}
	roles := []string{runtimeRoleRunner}
	if len(spec.Ports) > 0 {
		roles = []string{runtimeRoleGateway, runtimeRoleRunner}
	}
	for _, role := range roles {
		status, err := manager.inspectRole(ctx, spec, role)
		if err != nil {
			return RuntimeStatus{}, err
		}
		if status.State == "paused" {
			continue
		}
		if status.State != "running" {
			return RuntimeStatus{}, fmt.Errorf("%w: %s container cannot be suspended from %s", ErrRuntimeConflict, role, status.State)
		}
		if _, err := manager.runCommand(ctx, "pause", manager.roleContainerName(spec.SessionID, role)); err != nil {
			return RuntimeStatus{}, fmt.Errorf("%w: pause %s container: %v", ErrRuntimeUnavailable, role, err)
		}
	}
	return manager.inspect(ctx, spec)
}

func (manager *ContainerRuntime) Resume(ctx context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	if err := manager.validate(ctx, spec); err != nil {
		return RuntimeStatus{}, err
	}
	roles := []string{runtimeRoleRunner}
	if len(spec.Ports) > 0 {
		roles = append(roles, runtimeRoleGateway)
	}
	for _, role := range roles {
		status, err := manager.inspectRole(ctx, spec, role)
		if err != nil {
			return RuntimeStatus{}, err
		}
		if status.State == "running" {
			continue
		}
		if status.State != "paused" {
			return RuntimeStatus{}, fmt.Errorf("%w: %s container cannot resume from %s", ErrRuntimeConflict, role, status.State)
		}
		if _, err := manager.runCommand(ctx, "unpause", manager.roleContainerName(spec.SessionID, role)); err != nil {
			return RuntimeStatus{}, fmt.Errorf("%w: unpause %s container: %v", ErrRuntimeUnavailable, role, err)
		}
	}
	return manager.inspect(ctx, spec)
}

func (manager *ContainerRuntime) Terminate(ctx context.Context, spec RuntimeSpec) error {
	if err := manager.validate(ctx, spec); err != nil {
		return err
	}
	var cleanupErrors []error
	for _, role := range []string{runtimeRoleGateway, runtimeRoleRunner} {
		if _, err := manager.runCommand(ctx, "rm", "--force", "--volumes", manager.roleContainerName(spec.SessionID, role)); err != nil && !isContainerNotFound(err) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove %s container: %w", role, err))
		}
	}
	if _, err := manager.runCommand(ctx, "network", "rm", manager.networkName(spec.SessionID)); err != nil && !isContainerNotFound(err) {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("remove network: %w", err))
	}
	if len(cleanupErrors) > 0 {
		return errors.Join(append([]error{ErrRuntimeUnavailable}, cleanupErrors...)...)
	}
	return nil
}

func (manager *ContainerRuntime) Inspect(ctx context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	if err := manager.validate(ctx, spec); err != nil {
		return RuntimeStatus{}, err
	}
	return manager.inspect(ctx, spec)
}

func (manager *ContainerRuntime) validate(ctx context.Context, spec RuntimeSpec) error {
	if manager == nil || ctx == nil || manager.executor == nil {
		return ErrRuntimeInvalid
	}
	if err := validateRuntimeSpec(spec); err != nil {
		return err
	}
	if spec.RunnerImageDigest != manager.runnerDigest {
		return ErrRuntimeConflict
	}
	for _, directory := range []string{spec.Workspace.SessionRoot, spec.Workspace.Workspace, spec.Workspace.CodexHome} {
		absolute, err := filepath.Abs(directory)
		info, statErr := os.Lstat(directory)
		if err != nil || statErr != nil || absolute != directory || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			!pathWithin(manager.workspaceRoot, directory) {
			return ErrRuntimeInvalid
		}
	}
	if filepath.Dir(spec.Workspace.Workspace) != spec.Workspace.SessionRoot ||
		filepath.Dir(filepath.Dir(spec.Workspace.CodexHome)) != spec.Workspace.SessionRoot {
		return ErrRuntimeInvalid
	}
	return nil
}

func (manager *ContainerRuntime) ensureNetwork(ctx context.Context, spec RuntimeSpec) error {
	name := manager.networkName(spec.SessionID)
	network, err := manager.inspectNetwork(ctx, name)
	if err == nil {
		return validateRuntimeNetwork(network, spec)
	}
	if !isContainerNotFound(err) {
		return err
	}
	labels := manager.labels(spec)
	args := []string{"network", "create", "--driver", "bridge", "--internal"}
	for _, key := range sortedStringKeys(labels) {
		args = append(args, "--label", key+"="+labels[key])
	}
	args = append(args, name)
	commandCtx, cancel := context.WithTimeout(ctx, manager.commandTimeout)
	defer cancel()
	if _, err := manager.executor.Run(commandCtx, args...); err != nil {
		if recovered, inspectErr := manager.inspectNetwork(ctx, name); inspectErr == nil {
			return validateRuntimeNetwork(recovered, spec)
		}
		return fmt.Errorf("%w: create internal network: %v", ErrRuntimeUnavailable, err)
	}
	network, err = manager.inspectNetwork(ctx, name)
	if err != nil {
		return err
	}
	return validateRuntimeNetwork(network, spec)
}

func (manager *ContainerRuntime) createArgs(spec RuntimeSpec) []string {
	labels := manager.containerLabels(spec, runtimeRoleRunner)
	args := []string{
		"create", "--name", manager.containerName(spec.SessionID), "--pull", "never",
		"--network", manager.networkName(spec.SessionID), "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--pids-limit", strconv.Itoa(spec.Quota.PIDLimit),
		"--memory", strconv.FormatInt(spec.Quota.MemoryBytes, 10),
		"--cpus", strconv.FormatFloat(float64(spec.Quota.CPUMillis)/1000, 'f', 3, 64),
		"--ulimit", "nofile=1024:1024",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=268435456",
		"--tmpfs", "/run:rw,nosuid,nodev,noexec,size=16777216",
		"--user", strconv.Itoa(manager.uid) + ":" + strconv.Itoa(manager.gid),
		"--mount", "type=bind,src=" + spec.Workspace.Workspace + ",dst=/workspace",
		"--mount", "type=bind,src=" + spec.Workspace.CodexHome + ",dst=/codex",
		"--workdir", "/workspace", "--env", "HOME=/tmp/home", "--env", "CODEX_HOME=/codex",
		"--env", "NO_UPDATE_NOTIFIER=1", "--health-cmd", "test -f /tmp/worksflow-runner-ready",
		"--health-interval", "1s", "--health-timeout", "2s", "--health-retries", "30",
		"--entrypoint", "/usr/bin/tini",
	}
	for _, key := range sortedStringKeys(labels) {
		args = append(args, "--label", key+"="+labels[key])
	}
	args = append(args, manager.runnerImage, "--", "/usr/local/bin/worksflow-sandbox-init")
	return args
}

func (manager *ContainerRuntime) gatewayCreateArgs(spec RuntimeSpec) []string {
	labels := manager.containerLabels(spec, runtimeRoleGateway)
	ports := append([]AllowedPort(nil), spec.Ports...)
	sort.Slice(ports, func(left, right int) bool { return ports[left].Name < ports[right].Name })
	numbers := make([]string, len(ports))
	for index, port := range ports {
		numbers[index] = strconv.Itoa(port.Number)
	}
	args := []string{
		"create", "--name", manager.gatewayName(spec.SessionID), "--pull", "never",
		"--network", manager.networkName(spec.SessionID), "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--pids-limit", "64",
		"--memory", strconv.FormatInt(64<<20, 10), "--cpus", "0.100",
		"--ulimit", "nofile=1024:1024",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=16777216",
		"--tmpfs", "/run:rw,nosuid,nodev,noexec,size=4194304",
		"--user", strconv.Itoa(manager.uid) + ":" + strconv.Itoa(manager.gid),
		"--workdir", "/tmp",
		"--env", "HOME=/tmp", "--env", "WORKSFLOW_GATEWAY_TARGET=" + manager.containerName(spec.SessionID),
		"--env", "WORKSFLOW_GATEWAY_PORTS=" + strings.Join(numbers, ","),
		"--health-cmd", "test -f /tmp/worksflow-gateway-ready",
		"--health-interval", "1s", "--health-timeout", "2s", "--health-retries", "30",
		"--entrypoint", "/usr/bin/tini",
	}
	for _, key := range sortedStringKeys(labels) {
		args = append(args, "--label", key+"="+labels[key])
	}
	for _, port := range ports {
		args = append(args, "--publish", manager.gatewayBindAddress+"::"+strconv.Itoa(port.Number)+"/tcp")
	}
	args = append(args, manager.runnerImage, "--", "/usr/local/bin/worksflow-sandbox-gateway")
	return args
}

type dockerContainerMount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

type dockerContainerInspect struct {
	ID     string `json:"Id"`
	Image  string `json:"Image"`
	Config struct {
		Image      string            `json:"Image"`
		Labels     map[string]string `json:"Labels"`
		User       string            `json:"User"`
		Env        []string          `json:"Env"`
		Entrypoint []string          `json:"Entrypoint"`
		Cmd        []string          `json:"Cmd"`
		WorkingDir string            `json:"WorkingDir"`
	} `json:"Config"`
	HostConfig struct {
		ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
		Privileged     bool              `json:"Privileged"`
		CapDrop        []string          `json:"CapDrop"`
		SecurityOpt    []string          `json:"SecurityOpt"`
		PidsLimit      int64             `json:"PidsLimit"`
		Memory         int64             `json:"Memory"`
		NanoCPUs       int64             `json:"NanoCpus"`
		NetworkMode    string            `json:"NetworkMode"`
		Tmpfs          map[string]string `json:"Tmpfs"`
		PortBindings   map[string][]any  `json:"PortBindings"`
	} `json:"HostConfig"`
	Mounts []dockerContainerMount `json:"Mounts"`
	State  struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
		Paused  bool   `json:"Paused"`
		Health  *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (manager *ContainerRuntime) inspect(ctx context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	runner, err := manager.inspectRole(ctx, spec, runtimeRoleRunner)
	if err != nil {
		return RuntimeStatus{}, err
	}
	if len(spec.Ports) == 0 {
		return runner, nil
	}
	gateway, err := manager.inspectRole(ctx, spec, runtimeRoleGateway)
	if err != nil {
		return RuntimeStatus{}, err
	}
	state := runner.State
	if gateway.State != state {
		state = "partial"
	}
	runner.State = state
	runner.Healthy = runner.Healthy && gateway.Healthy
	runner.HostPorts = cloneIntMap(gateway.HostPorts)
	return runner, nil
}

func (manager *ContainerRuntime) ensureContainer(
	ctx context.Context,
	spec RuntimeSpec,
	role string,
) (RuntimeStatus, error) {
	row, err := manager.inspectContainerRow(ctx, manager.roleContainerName(spec.SessionID, role))
	if err != nil && !isContainerNotFound(err) {
		return RuntimeStatus{}, err
	}
	if isContainerNotFound(err) {
		args := manager.createArgs(spec)
		if role == runtimeRoleGateway {
			args = manager.gatewayCreateArgs(spec)
		}
		if _, createErr := manager.runCommand(ctx, args...); createErr != nil {
			row, err = manager.inspectContainerRow(ctx, manager.roleContainerName(spec.SessionID, role))
			if err != nil {
				return RuntimeStatus{}, fmt.Errorf("%w: create %s container: %v", ErrRuntimeUnavailable, role, createErr)
			}
		} else {
			row, err = manager.inspectContainerRow(ctx, manager.roleContainerName(spec.SessionID, role))
			if err != nil {
				return RuntimeStatus{}, err
			}
		}
	}
	if err := manager.validateContainerIdentity(row, spec, role); err != nil {
		return RuntimeStatus{}, err
	}
	if role == runtimeRoleGateway {
		if _, connected := row.NetworkSettings.Networks[manager.gatewayNetwork]; !connected {
			if _, err := manager.runCommand(ctx, "network", "connect", manager.gatewayNetwork, manager.gatewayName(spec.SessionID)); err != nil {
				return RuntimeStatus{}, fmt.Errorf("%w: connect preview gateway network: %v", ErrRuntimeUnavailable, err)
			}
			row, err = manager.inspectContainerRow(ctx, manager.gatewayName(spec.SessionID))
			if err != nil {
				return RuntimeStatus{}, err
			}
		}
	}
	return manager.runtimeStatus(row, spec, role)
}

func (manager *ContainerRuntime) inspectRole(
	ctx context.Context,
	spec RuntimeSpec,
	role string,
) (RuntimeStatus, error) {
	row, err := manager.inspectContainerRow(ctx, manager.roleContainerName(spec.SessionID, role))
	if err != nil {
		return RuntimeStatus{}, err
	}
	return manager.runtimeStatus(row, spec, role)
}

func (manager *ContainerRuntime) inspectContainerRow(
	ctx context.Context,
	name string,
) (dockerContainerInspect, error) {
	encoded, err := manager.runCommand(ctx, "inspect", name)
	if err != nil {
		return dockerContainerInspect{}, errors.Join(ErrRuntimeUnavailable, fmt.Errorf("inspect container: %w", err))
	}
	var rows []dockerContainerInspect
	if err := json.Unmarshal(encoded, &rows); err != nil || len(rows) != 1 {
		return dockerContainerInspect{}, fmt.Errorf("%w: invalid container inspection", ErrRuntimeUnavailable)
	}
	return rows[0], nil
}

func (manager *ContainerRuntime) validateContainerIdentity(
	row dockerContainerInspect,
	spec RuntimeSpec,
	role string,
) error {
	expectedLabels := manager.containerLabels(spec, role)
	if row.ID == "" {
		return fmt.Errorf("%w: %s container ID is missing", ErrRuntimeConflict, role)
	}
	if row.Config.Image != manager.runnerImage {
		return fmt.Errorf("%w: %s container image reference differs", ErrRuntimeConflict, role)
	}
	for key, value := range expectedLabels {
		if row.Config.Labels[key] != value {
			return fmt.Errorf("%w: %s container label %s differs", ErrRuntimeConflict, role, key)
		}
	}
	if err := manager.validateContainerSecurity(row, spec, role); err != nil {
		return err
	}
	return nil
}

func (manager *ContainerRuntime) validateContainerSecurity(
	row dockerContainerInspect,
	spec RuntimeSpec,
	role string,
) error {
	host := row.HostConfig
	if !host.ReadonlyRootfs || host.Privileged || !equalStringSet(host.CapDrop, []string{"ALL"}) ||
		!equalStringSet(host.SecurityOpt, []string{"no-new-privileges"}) ||
		row.Config.User != strconv.Itoa(manager.uid)+":"+strconv.Itoa(manager.gid) ||
		host.NetworkMode != manager.networkName(spec.SessionID) ||
		len(row.Config.Entrypoint) != 1 || row.Config.Entrypoint[0] != "/usr/bin/tini" ||
		len(row.Config.Cmd) != 2 || row.Config.Cmd[0] != "--" {
		return fmt.Errorf("%w: %s container security configuration differs", ErrRuntimeConflict, role)
	}
	for _, value := range row.Config.Env {
		key := strings.SplitN(value, "=", 2)[0]
		switch key {
		case "CODEX_API_KEY", "OPENAI_API_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN":
			return fmt.Errorf("%w: %s container received a forbidden credential", ErrRuntimeConflict, role)
		}
	}
	if role == runtimeRoleRunner {
		if host.PidsLimit != int64(spec.Quota.PIDLimit) || host.Memory != spec.Quota.MemoryBytes ||
			host.NanoCPUs != spec.Quota.CPUMillis*1_000_000 || row.Config.WorkingDir != "/workspace" ||
			row.Config.Cmd[1] != "/usr/local/bin/worksflow-sandbox-init" || len(host.PortBindings) != 0 ||
			len(row.Mounts) != 2 ||
			!hasExactRuntimeMount(row.Mounts, spec.Workspace.Workspace, "/workspace") ||
			!hasExactRuntimeMount(row.Mounts, spec.Workspace.CodexHome, "/codex") {
			return fmt.Errorf("%w: runner quotas or mounts differ", ErrRuntimeConflict)
		}
		return nil
	}
	if host.PidsLimit != 64 || host.Memory != 64<<20 || host.NanoCPUs != 100_000_000 ||
		row.Config.WorkingDir != "/tmp" || row.Config.Cmd[1] != "/usr/local/bin/worksflow-sandbox-gateway" ||
		len(row.Mounts) != 0 || len(host.PortBindings) != len(spec.Ports) {
		return fmt.Errorf("%w: gateway quotas, mounts, or ports differ", ErrRuntimeConflict)
	}
	environment := environmentMap(row.Config.Env)
	ports := append([]AllowedPort(nil), spec.Ports...)
	sort.Slice(ports, func(left, right int) bool { return ports[left].Name < ports[right].Name })
	numbers := make([]string, len(ports))
	for index, port := range ports {
		numbers[index] = strconv.Itoa(port.Number)
		bindings, exists := host.PortBindings[strconv.Itoa(port.Number)+"/tcp"]
		if !exists || len(bindings) != 1 {
			return fmt.Errorf("%w: gateway port binding differs", ErrRuntimeConflict)
		}
	}
	if environment["WORKSFLOW_GATEWAY_TARGET"] != manager.containerName(spec.SessionID) ||
		environment["WORKSFLOW_GATEWAY_PORTS"] != strings.Join(numbers, ",") {
		return fmt.Errorf("%w: gateway target allowlist differs", ErrRuntimeConflict)
	}
	return nil
}

func (manager *ContainerRuntime) runtimeStatus(
	row dockerContainerInspect,
	spec RuntimeSpec,
	role string,
) (RuntimeStatus, error) {
	if err := manager.validateContainerIdentity(row, spec, role); err != nil {
		return RuntimeStatus{}, err
	}
	internalName := manager.networkName(spec.SessionID)
	_, internal := row.NetworkSettings.Networks[internalName]
	_, gatewayNetwork := row.NetworkSettings.Networks[manager.gatewayNetwork]
	if role == runtimeRoleRunner && (!internal || gatewayNetwork || len(row.NetworkSettings.Networks) != 1) {
		return RuntimeStatus{}, fmt.Errorf("%w: runner is not confined to the internal network", ErrRuntimeConflict)
	}
	if role == runtimeRoleGateway && (!internal || !gatewayNetwork || len(row.NetworkSettings.Networks) != 2) {
		return RuntimeStatus{}, fmt.Errorf("%w: gateway networks differ", ErrRuntimeConflict)
	}
	state := strings.ToLower(strings.TrimSpace(row.State.Status))
	if row.State.Paused {
		state = "paused"
	}
	healthy := row.State.Running && row.State.Health != nil && row.State.Health.Status == "healthy"
	hostPorts := map[string]int{}
	ports := []AllowedPort(nil)
	if role == runtimeRoleGateway {
		ports = spec.Ports
	}
	for _, port := range ports {
		bindings := row.NetworkSettings.Ports[strconv.Itoa(port.Number)+"/tcp"]
		if len(bindings) != 1 || bindings[0].HostIP != manager.gatewayBindAddress {
			if row.State.Running {
				hostIP := ""
				if len(bindings) > 0 {
					hostIP = bindings[0].HostIP
				}
				return RuntimeStatus{}, fmt.Errorf(
					"%w: host binding for %s differs (count=%d, firstIP=%q)",
					ErrRuntimeConflict, port.Name, len(bindings), hostIP,
				)
			}
			continue
		}
		hostPort, err := strconv.Atoi(bindings[0].HostPort)
		if err != nil || hostPort < 1 || hostPort > 65535 {
			return RuntimeStatus{}, fmt.Errorf("%w: host port for %s is invalid", ErrRuntimeConflict, port.Name)
		}
		hostPorts[port.Name] = hostPort
	}
	return RuntimeStatus{
		ID: row.ID, SessionID: spec.SessionID, SessionEpoch: spec.SessionEpoch,
		State: state, Healthy: healthy, HostPorts: hostPorts, Labels: cloneStringMap(row.Config.Labels),
	}, nil
}

type dockerNetworkInspect struct {
	Name     string            `json:"Name"`
	Internal bool              `json:"Internal"`
	Driver   string            `json:"Driver"`
	Labels   map[string]string `json:"Labels"`
}

func (manager *ContainerRuntime) inspectNetwork(ctx context.Context, name string) (dockerNetworkInspect, error) {
	commandCtx, cancel := context.WithTimeout(ctx, manager.commandTimeout)
	defer cancel()
	encoded, err := manager.executor.Run(commandCtx, "network", "inspect", name)
	if err != nil {
		return dockerNetworkInspect{}, errors.Join(ErrRuntimeUnavailable, fmt.Errorf("inspect network: %w", err))
	}
	var rows []dockerNetworkInspect
	if err := json.Unmarshal(encoded, &rows); err != nil || len(rows) != 1 {
		return dockerNetworkInspect{}, fmt.Errorf("%w: invalid network inspection", ErrRuntimeUnavailable)
	}
	return rows[0], nil
}

func validateRuntimeNetwork(network dockerNetworkInspect, spec RuntimeSpec) error {
	if !network.Internal || network.Driver != "bridge" {
		return fmt.Errorf("%w: network is not an internal bridge", ErrRuntimeConflict)
	}
	expected := map[string]string{
		"dev.worksflow.runtime-contract": sandboxRuntimeContract,
		"dev.worksflow.project-id":       spec.ProjectID,
		"dev.worksflow.session-id":       spec.SessionID,
		"dev.worksflow.session-epoch":    strconv.FormatUint(spec.SessionEpoch, 10),
		"dev.worksflow.runner-digest":    spec.RunnerImageDigest,
	}
	for key, value := range expected {
		if network.Labels[key] != value {
			return fmt.Errorf("%w: network label %s differs", ErrRuntimeConflict, key)
		}
	}
	return nil
}

func (manager *ContainerRuntime) labels(spec RuntimeSpec) map[string]string {
	return map[string]string{
		"dev.worksflow.runtime-contract": sandboxRuntimeContract,
		"dev.worksflow.project-id":       spec.ProjectID,
		"dev.worksflow.session-id":       spec.SessionID,
		"dev.worksflow.session-epoch":    strconv.FormatUint(spec.SessionEpoch, 10),
		"dev.worksflow.runner-digest":    spec.RunnerImageDigest,
	}
}

func (manager *ContainerRuntime) containerLabels(spec RuntimeSpec, role string) map[string]string {
	labels := manager.labels(spec)
	labels["dev.worksflow.runtime-role"] = role
	return labels
}

func (*ContainerRuntime) containerName(sessionID string) string {
	return "worksflow-sandbox-" + strings.ReplaceAll(sessionID, "-", "")
}

func (*ContainerRuntime) networkName(sessionID string) string {
	return "worksflow-sandbox-net-" + strings.ReplaceAll(sessionID, "-", "")
}

func (*ContainerRuntime) gatewayName(sessionID string) string {
	return "worksflow-sandbox-gateway-" + strings.ReplaceAll(sessionID, "-", "")
}

func (manager *ContainerRuntime) roleContainerName(sessionID, role string) string {
	if role == runtimeRoleGateway {
		return manager.gatewayName(sessionID)
	}
	return manager.containerName(sessionID)
}

func (manager *ContainerRuntime) runCommand(ctx context.Context, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, manager.commandTimeout)
	defer cancel()
	return manager.executor.Run(commandCtx, args...)
}

type runtimeCommandError struct {
	args   []string
	output string
	cause  error
}

func (err *runtimeCommandError) Error() string {
	return fmt.Sprintf("container CLI %s failed: %v (%s)", strings.Join(err.args, " "), err.cause, err.output)
}

func (err *runtimeCommandError) Unwrap() error { return err.cause }

func isContainerNotFound(err error) bool {
	var commandError *runtimeCommandError
	if !errors.As(err, &commandError) {
		return false
	}
	value := strings.ToLower(commandError.output)
	return strings.Contains(value, "no such container") || strings.Contains(value, "no such network") ||
		strings.Contains(value, "no such object") || strings.Contains(value, "not found")
}

type containerCLIExecutor struct {
	path        string
	environment []string
	outputLimit int
}

func (executor *containerCLIExecutor) Run(ctx context.Context, args ...string) ([]byte, error) {
	if ctx == nil || executor == nil || executor.path == "" {
		return nil, ErrRuntimeUnavailable
	}
	limit := executor.outputLimit
	if limit <= 0 || limit > 8<<20 {
		limit = 1 << 20
	}
	buffer := &boundedRuntimeBuffer{limit: limit}
	command := exec.CommandContext(ctx, executor.path, args...)
	command.Env = append([]string(nil), executor.environment...)
	command.Stdout, command.Stderr = buffer, buffer
	if err := command.Run(); err != nil {
		return nil, &runtimeCommandError{args: append([]string(nil), args...), output: buffer.String(), cause: err}
	}
	return buffer.Bytes(), nil
}

type boundedRuntimeBuffer struct {
	mu    sync.Mutex
	value bytes.Buffer
	limit int
}

func (buffer *boundedRuntimeBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	remaining := buffer.limit - buffer.value.Len()
	if remaining > 0 {
		_, _ = buffer.value.Write(value[:min(remaining, len(value))])
	}
	return len(value), nil
}

func (buffer *boundedRuntimeBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.value.Bytes()...)
}

func (buffer *boundedRuntimeBuffer) String() string { return string(buffer.Bytes()) }

func normalizeRuntimeDaemonHost(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
		return "", ErrRuntimeInvalid
	}
	if strings.HasPrefix(value, "unix:///") {
		path := strings.TrimPrefix(value, "unix://")
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return "", ErrRuntimeInvalid
		}
		return value, nil
	}
	if strings.HasPrefix(value, "tcp://") {
		host := strings.TrimPrefix(value, "tcp://")
		if _, port, err := net.SplitHostPort(host); err != nil || port == "" || strings.ContainsAny(host, "/?#@") {
			return "", ErrRuntimeInvalid
		}
		return value, nil
	}
	return "", ErrRuntimeInvalid
}

func runtimeClientEnvironment(configDirectory, daemonHost string) []string {
	path := "/usr/local/bin:/usr/bin:/bin"
	if runtime.GOOS == "windows" {
		path = os.Getenv("PATH")
	}
	environment := []string{"PATH=" + path, "HOME=" + configDirectory, "DOCKER_CONFIG=" + configDirectory}
	if daemonHost != "" {
		environment = append(environment, "DOCKER_HOST="+daemonHost)
	}
	return environment
}

func sortedStringKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneIntMap(values map[string]int) map[string]int {
	result := make(map[string]int, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func equalStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy, rightCopy := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}

func hasExactRuntimeMount(mounts []dockerContainerMount, source, destination string) bool {
	for _, mount := range mounts {
		if mount.Type == "bind" && mount.Source == source && mount.Destination == destination && mount.RW {
			return true
		}
	}
	return false
}

func environmentMap(values []string) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		parts := strings.SplitN(value, "=", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

var _ RuntimeManager = (*ContainerRuntime)(nil)
var _ io.Writer = (*boundedRuntimeBuffer)(nil)
