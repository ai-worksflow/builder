package lsp

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	languageServerContainerUser       = "65532:65532"
	languageServerWorkspaceMount      = "/workspace"
	languageServerTempMount           = "/tmp"
	languageServerCacheMount          = "/cache"
	languageServerMaximumCLIBytes     = int64(8 << 20)
	languageServerMaximumArchiveBytes = int64(256 << 20)
	languageServerDefaultArchiveBytes = int64(128 << 20)
	languageServerDefaultMaxProcesses = 4
	languageServerMaximumHeaderBytes  = 4096
	languageServerHardCommandTimeout  = 30 * time.Second
)

var (
	ErrContainerRuntimeInvalid       = errors.New("invalid LSP container runtime authority")
	ErrContainerRuntimeUnavailable   = errors.New("LSP container runtime is unavailable")
	ErrContainerRuntimeIdentityDrift = errors.New("LSP container identity drift")
	ErrContainerRuntimeExhausted     = errors.New("LSP container runtime resource exhausted")
	ErrContainerRuntimeClosed        = errors.New("LSP container runtime is closed")
	errContainerNotRunning           = errors.New("LSP container is not running yet")

	containerIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ContainerRuntimeConfig contains host-local runtime authority only. Language
// server image, executable, argv, timeouts, and resources always come from the
// exact approved ProfileIdentity supplied to Start.
type ContainerRuntimeConfig struct {
	RuntimeBinary      string
	DaemonHost         string
	CommandTimeout     time.Duration
	CLIOutputBytes     int64
	MaxProcesses       int
	MaxExecutableBytes int64
}

// ContainerStartInput is the complete non-secret identity required to create
// one language-server process. WorkspaceRoot must be an already-materialized,
// immutable Candidate root whose exact head/tree authority was verified by the
// caller; this layer verifies its host path identity and mounts it read-only,
// but deliberately does not manufacture or re-hash Candidate authority.
// ServiceRoot is its canonical TemplateService.RootPath.
type ContainerStartInput struct {
	Profile       ProfileIdentity
	WorkspaceRoot string
	ServiceRoot   string
	ConnectionID  string
	BindingID     string
}

// ContainerProcessExit describes the local CLI/process outcome. Stderr is
// intentionally not embedded; callers may inspect the separately bounded
// Stderr snapshot without accidentally persisting it as an error string.
type ContainerProcessExit struct {
	FinishedAt time.Time
	Err        error
}

// LanguageServerProcess exposes framed JSON-RPC rather than raw, unbounded
// stdio. Every read/write is limited by the admitted maxFrameBytes. Any framing
// violation closes the streams and force-removes the exact container.
type LanguageServerProcess interface {
	Name() string
	Profile() ProfileIdentity
	WriteFrame(context.Context, []byte) error
	ReadFrame(context.Context) ([]byte, error)
	Stderr() []byte
	Wait(context.Context) (ContainerProcessExit, error)
	Terminate(context.Context) error
}

// LanguageServerRuntime is the gateway-facing lifecycle boundary.
type LanguageServerRuntime interface {
	Readiness(context.Context, ...ProfileIdentity) error
	Start(context.Context, ContainerStartInput) (LanguageServerProcess, error)
	Close() error
}

type containerRuntimeCLI interface {
	Run(context.Context, int64, ...string) ([]byte, error)
	Start(context.Context, ...string) (containerRuntimeStream, error)
	Close() error
}

type containerRuntimeStream interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() error
	Close() error
}

// ContainerRuntime never invokes a shell and never asks the daemon to pull.
// Its isolated CLI environment contains no inherited host credentials.
type ContainerRuntime struct {
	cli                containerRuntimeCLI
	runtimeName        string
	daemonHost         string
	commandTimeout     time.Duration
	cliOutputBytes     int64
	maxProcesses       int
	maxExecutableBytes int64

	mu        sync.Mutex
	closed    bool
	starts    sync.WaitGroup
	closeDone chan struct{}
	closeErr  error
	reserved  map[string]struct{}
	processes map[string]*containerLanguageServerProcess
}

// NewContainerRuntime constructs a Docker/Podman CLI runtime from an exact,
// absolute, non-symlink executable. PATH lookup and ambient Docker contexts are
// deliberately unsupported.
func NewContainerRuntime(config ContainerRuntimeConfig) (*ContainerRuntime, error) {
	binary, runtimeName, err := validateContainerRuntimeBinary(config.RuntimeBinary)
	if err != nil {
		return nil, err
	}
	daemonHost, err := validateContainerDaemonHost(config.DaemonHost)
	if err != nil {
		return nil, err
	}
	if runtimeName == "podman" && daemonHost == "" {
		return nil, fmt.Errorf("%w: Podman requires an explicit local unix service", ErrContainerRuntimeInvalid)
	}
	binaryInfo, err := os.Lstat(binary)
	if err != nil {
		return nil, ErrContainerRuntimeInvalid
	}
	var daemonPath string
	var daemonInfo os.FileInfo
	if daemonHost != "" {
		parsed, _ := url.Parse(daemonHost)
		daemonPath = parsed.Path
		daemonInfo, err = os.Lstat(daemonPath)
		if err != nil {
			return nil, ErrContainerRuntimeInvalid
		}
	}
	configRoot, err := os.MkdirTemp("", "worksflow-lsp-runtime-")
	if err != nil {
		return nil, fmt.Errorf("%w: isolated client configuration", ErrContainerRuntimeUnavailable)
	}
	if err := os.Chmod(configRoot, 0o700); err != nil {
		_ = os.RemoveAll(configRoot)
		return nil, fmt.Errorf("%w: isolate client configuration", ErrContainerRuntimeUnavailable)
	}
	cli := &osContainerRuntimeCLI{
		binary: binary, binaryInfo: binaryInfo, daemonPath: daemonPath, daemonInfo: daemonInfo,
		environment: []string{
			"HOME=" + configRoot,
			"DOCKER_CONFIG=" + configRoot,
			"TMPDIR=" + configRoot,
			"LANG=C",
			"LC_ALL=C",
		},
		configRoot: configRoot,
	}
	runtime, err := newContainerRuntime(config, runtimeName, daemonHost, cli)
	if err != nil {
		_ = cli.Close()
		return nil, err
	}
	return runtime, nil
}

func newContainerRuntime(
	config ContainerRuntimeConfig,
	runtimeName string,
	daemonHost string,
	cli containerRuntimeCLI,
) (*ContainerRuntime, error) {
	if cli == nil || (runtimeName != "docker" && runtimeName != "podman") {
		return nil, ErrContainerRuntimeInvalid
	}
	commandTimeout := config.CommandTimeout
	if commandTimeout <= 0 || commandTimeout > languageServerHardCommandTimeout {
		return nil, fmt.Errorf("%w: command timeout", ErrContainerRuntimeInvalid)
	}
	outputBytes := config.CLIOutputBytes
	if outputBytes < 1024 || outputBytes > languageServerMaximumCLIBytes {
		return nil, fmt.Errorf("%w: CLI output limit", ErrContainerRuntimeInvalid)
	}
	maxProcesses := config.MaxProcesses
	if maxProcesses == 0 {
		maxProcesses = languageServerDefaultMaxProcesses
	}
	if maxProcesses < 1 || maxProcesses > 32 {
		return nil, fmt.Errorf("%w: process limit", ErrContainerRuntimeInvalid)
	}
	maxExecutableBytes := config.MaxExecutableBytes
	if maxExecutableBytes == 0 {
		maxExecutableBytes = languageServerDefaultArchiveBytes
	}
	if maxExecutableBytes < 1<<20 || maxExecutableBytes > languageServerMaximumArchiveBytes-(1<<20) {
		return nil, fmt.Errorf("%w: executable limit", ErrContainerRuntimeInvalid)
	}
	return &ContainerRuntime{
		cli: cli, runtimeName: runtimeName, daemonHost: daemonHost,
		commandTimeout: commandTimeout, cliOutputBytes: outputBytes,
		maxProcesses: maxProcesses, maxExecutableBytes: maxExecutableBytes,
		closeDone: make(chan struct{}),
		reserved:  make(map[string]struct{}), processes: make(map[string]*containerLanguageServerProcess),
	}, nil
}

// Readiness performs no mutation and no pull. With no profiles it proves only
// daemon reachability and makes no image-readiness claim. With profiles it also
// proves that every exact, digest-pinned admitted image is already present.
func (runtime *ContainerRuntime) Readiness(ctx context.Context, profiles ...ProfileIdentity) error {
	if runtime == nil || ctx == nil || len(profiles) > 16 {
		return ErrContainerRuntimeInvalid
	}
	images := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		if profile.Validate() != nil {
			return ErrContainerRuntimeInvalid
		}
		images[profile.Runtime.Image] = struct{}{}
	}
	if err := runtime.beginOperation(); err != nil {
		return err
	}
	defer runtime.starts.Done()
	if _, err := runtime.run(ctx, runtime.cliOutputBytes, "version"); err != nil {
		return fmt.Errorf("%w: daemon version", ErrContainerRuntimeUnavailable)
	}
	ordered := make([]string, 0, len(images))
	for image := range images {
		ordered = append(ordered, image)
	}
	sort.Strings(ordered)
	for _, image := range ordered {
		if _, err := runtime.inspectImage(ctx, image); err != nil {
			return err
		}
	}
	return nil
}

func (runtime *ContainerRuntime) Start(
	ctx context.Context,
	input ContainerStartInput,
) (LanguageServerProcess, error) {
	if runtime == nil || ctx == nil || input.Profile.Validate() != nil ||
		!canonicalUUID(input.ConnectionID) || !canonicalUUID(input.BindingID) ||
		forbiddenContainerExecutablePath(input.Profile.Runtime.ExecutablePath) {
		return nil, ErrContainerRuntimeInvalid
	}
	if runtime.isClosed() {
		return nil, ErrContainerRuntimeClosed
	}
	workspaceRoot, _, workdir, err := validateContainerWorkspace(
		input.WorkspaceRoot, input.ServiceRoot,
	)
	if err != nil {
		return nil, err
	}
	name := languageServerContainerName(input.ConnectionID, input.BindingID)
	if err := runtime.reserve(name); err != nil {
		return nil, err
	}
	defer runtime.starts.Done()
	committed := false
	defer func() {
		if !committed {
			runtime.release(name)
		}
	}()

	startupTimeout := time.Duration(input.Profile.EffectiveLimits.StartupTimeoutMillis) * time.Millisecond
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()

	image, err := runtime.inspectImage(startupCtx, input.Profile.Runtime.Image)
	if err != nil {
		return nil, err
	}
	expected := expectedContainerIdentity{
		input: input, name: name, imageID: image.ID,
		workspaceRoot: workspaceRoot, workdir: workdir,
	}
	if err := expected.captureWorkspaceIdentity(); err != nil {
		return nil, err
	}
	createArgs := runtime.createArgs(input, workspaceRoot, workdir, name)
	created, err := runtime.run(startupCtx, runtime.cliOutputBytes, createArgs...)
	if err != nil {
		cleanupErr := runtime.cleanupAmbiguousCreate(context.Background(), expected)
		return nil, errors.Join(
			fmt.Errorf("%w: create language-server container", ErrContainerRuntimeUnavailable), cleanupErr,
		)
	}
	containerID := strings.TrimSpace(string(created))
	if !containerIDPattern.MatchString(containerID) {
		cleanupErr := runtime.cleanupAmbiguousCreate(context.Background(), expected)
		return nil, errors.Join(
			fmt.Errorf("%w: invalid created container ID", ErrContainerRuntimeIdentityDrift), cleanupErr,
		)
	}
	expected.containerID = containerID
	cleanupCreated := func(cause error) error {
		return errors.Join(
			cause,
			runtime.removeContainer(context.Background(), containerID, input.Profile.EffectiveLimits),
		)
	}

	if err := runtime.inspectContainer(startupCtx, expected, false); err != nil {
		return nil, cleanupCreated(err)
	}
	archive, err := runtime.run(
		startupCtx,
		runtime.maxExecutableBytes+(1<<20),
		"container", "cp", containerID+":"+input.Profile.Runtime.ExecutablePath, "-",
	)
	if err != nil {
		return nil, cleanupCreated(fmt.Errorf("%w: export admitted executable", ErrContainerRuntimeUnavailable))
	}
	if err := verifyContainerExecutable(
		archive, input.Profile.Runtime.ExecutablePath, input.Profile.Runtime.ExecutableDigest,
		runtime.maxExecutableBytes,
	); err != nil {
		return nil, cleanupCreated(err)
	}
	if err := expected.recheckWorkspaceIdentity(); err != nil {
		return nil, cleanupCreated(err)
	}

	processCtx, cancelProcess := context.WithCancel(context.WithoutCancel(ctx))
	stream, err := runtime.cli.Start(processCtx, runtime.arguments("start", "--attach", "--interactive", containerID)...)
	if err != nil {
		cancelProcess()
		return nil, cleanupCreated(fmt.Errorf("%w: attach language-server container", ErrContainerRuntimeUnavailable))
	}
	process := newContainerLanguageServerProcess(
		runtime, stream, cancelProcess, name, containerID, input.Profile,
	)
	runtime.commit(name, process)
	committed = true
	process.startWaiters()
	if err := runtime.waitUntilRunning(startupCtx, expected); err != nil {
		process.failClosed(err)
		cleanupCtx, cancelCleanup := context.WithTimeout(
			context.Background(), process.shutdownTimeout+runtime.commandTimeout,
		)
		_, cleanupErr := process.Wait(cleanupCtx)
		cancelCleanup()
		return nil, errors.Join(err, cleanupErr)
	}
	return process, nil
}

func (runtime *ContainerRuntime) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	if runtime.closed {
		done := runtime.closeDone
		runtime.mu.Unlock()
		<-done
		runtime.mu.Lock()
		err := runtime.closeErr
		runtime.mu.Unlock()
		return err
	}
	runtime.closed = true
	runtime.mu.Unlock()
	runtime.starts.Wait()
	runtime.mu.Lock()
	processes := make([]*containerLanguageServerProcess, 0, len(runtime.processes))
	for _, process := range runtime.processes {
		processes = append(processes, process)
	}
	runtime.mu.Unlock()

	var result error
	for _, process := range processes {
		ctx, cancel := context.WithTimeout(context.Background(), process.shutdownTimeout)
		result = errors.Join(result, process.Terminate(ctx))
		cancel()
		waitCtx, cancelWait := context.WithTimeout(context.Background(), process.shutdownTimeout+runtime.commandTimeout)
		exit, waitErr := process.Wait(waitCtx)
		result = errors.Join(result, waitErr, exit.Err)
		cancelWait()
	}
	result = errors.Join(result, runtime.cli.Close())
	runtime.mu.Lock()
	runtime.closeErr = result
	close(runtime.closeDone)
	runtime.mu.Unlock()
	return result
}

func (runtime *ContainerRuntime) createArgs(
	input ContainerStartInput,
	workspaceRoot string,
	workdir string,
	name string,
) []string {
	limits := input.Profile.EffectiveLimits
	tmpfs := "rw,noexec,nosuid,nodev,size=" + strconv.FormatInt(limits.TempBytes, 10) + ",mode=0700,uid=65532,gid=65532"
	cache := "rw,noexec,nosuid,nodev,size=" + strconv.FormatInt(limits.CacheBytes, 10) + ",mode=0700,uid=65532,gid=65532"
	labels := []string{
		"worksflow.kind=language-server",
		"worksflow.lsp.connection=" + input.ConnectionID,
		"worksflow.lsp.binding=" + input.BindingID,
		"worksflow.lsp.profile=" + input.Profile.ID,
		"worksflow.lsp.profile-hash=" + input.Profile.ContentHash,
		"worksflow.lsp.release=" + input.Profile.TemplateRelease.ID,
		"worksflow.lsp.release-hash=" + input.Profile.TemplateRelease.ContentHash,
	}
	args := []string{
		"container", "create", "--name", name, "--pull", "never", "--interactive", "--no-healthcheck",
		"--network", "none", "--ipc", "none",
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--user", languageServerContainerUser,
		"--pids-limit", strconv.Itoa(limits.PIDLimit),
		"--ulimit", "nofile=4096:4096", "--ulimit", "core=0:0",
		"--memory", strconv.FormatInt(limits.MemoryBytes, 10),
		"--cpus", formatContainerCPUs(limits.CPUMillis),
		"--tmpfs", languageServerTempMount + ":" + tmpfs,
		"--tmpfs", languageServerCacheMount + ":" + cache,
		"--mount", "type=bind,src=" + workspaceRoot + ",dst=" + languageServerWorkspaceMount + ",readonly,bind-propagation=rprivate",
		"--workdir", workdir,
		"--env", "HOME=" + languageServerTempMount,
		"--env", "TMPDIR=" + languageServerTempMount,
		"--env", "XDG_CACHE_HOME=" + languageServerCacheMount,
		"--log-driver", "none",
		"--stop-timeout", strconv.Itoa(max(1, (limits.ShutdownTimeoutMillis+999)/1000)),
		"--entrypoint", input.Profile.Runtime.ExecutablePath,
	}
	if runtime.runtimeName == "podman" {
		args = append(args, "--read-only-tmpfs=false", "--cgroup-conf=memory.swap.max=0")
	} else {
		args = append(args, "--memory-swap", strconv.FormatInt(limits.MemoryBytes, 10))
	}
	for _, label := range labels {
		args = append(args, "--label", label)
	}
	args = append(args, input.Profile.Runtime.Image)
	args = append(args, input.Profile.Runtime.Argv[1:]...)
	return args
}

type inspectedImage struct {
	ID          string   `json:"Id"`
	Digest      string   `json:"Digest"`
	RepoDigests []string `json:"RepoDigests"`
}

func (runtime *ContainerRuntime) inspectImage(ctx context.Context, image string) (inspectedImage, error) {
	output, err := runtime.run(ctx, runtime.cliOutputBytes, "image", "inspect", image)
	if err != nil {
		return inspectedImage{}, fmt.Errorf("%w: admitted image is not pre-provisioned", ErrContainerRuntimeUnavailable)
	}
	var values []inspectedImage
	if json.Unmarshal(output, &values) != nil || len(values) != 1 ||
		!validRuntimeImageID(runtime.runtimeName, values[0].ID) ||
		!containsExactString(values[0].RepoDigests, image) {
		return inspectedImage{}, fmt.Errorf("%w: image inspect", ErrContainerRuntimeIdentityDrift)
	}
	return values[0], nil
}

type expectedContainerIdentity struct {
	input         ContainerStartInput
	name          string
	containerID   string
	imageID       string
	workspaceRoot string
	workdir       string
	workspaceInfo os.FileInfo
	servicePath   string
	serviceInfo   os.FileInfo
}

func (expected *expectedContainerIdentity) captureWorkspaceIdentity() error {
	workspaceInfo, err := os.Lstat(expected.workspaceRoot)
	if err != nil || !workspaceInfo.IsDir() || workspaceInfo.Mode()&os.ModeSymlink != 0 || workspaceInfo.Mode().Perm()&0o5 != 0o5 {
		return ErrContainerRuntimeIdentityDrift
	}
	servicePath := expected.workspaceRoot
	if expected.input.ServiceRoot != "." {
		servicePath = filepath.Join(expected.workspaceRoot, filepath.FromSlash(expected.input.ServiceRoot))
	}
	serviceInfo, err := os.Lstat(servicePath)
	if err != nil || !serviceInfo.IsDir() || serviceInfo.Mode()&os.ModeSymlink != 0 || serviceInfo.Mode().Perm()&0o5 != 0o5 {
		return ErrContainerRuntimeIdentityDrift
	}
	expected.workspaceInfo, expected.servicePath, expected.serviceInfo = workspaceInfo, servicePath, serviceInfo
	return nil
}

func (expected expectedContainerIdentity) recheckWorkspaceIdentity() error {
	workspaceInfo, err := os.Lstat(expected.workspaceRoot)
	if err != nil || !os.SameFile(expected.workspaceInfo, workspaceInfo) || workspaceInfo.Mode()&os.ModeSymlink != 0 ||
		workspaceInfo.Mode().Perm()&0o5 != 0o5 {
		return ErrContainerRuntimeIdentityDrift
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(expected.workspaceRoot)
	if err != nil || resolvedWorkspace != expected.workspaceRoot {
		return ErrContainerRuntimeIdentityDrift
	}
	serviceInfo, err := os.Lstat(expected.servicePath)
	if err != nil || !os.SameFile(expected.serviceInfo, serviceInfo) || serviceInfo.Mode()&os.ModeSymlink != 0 ||
		serviceInfo.Mode().Perm()&0o5 != 0o5 {
		return ErrContainerRuntimeIdentityDrift
	}
	resolvedService, err := filepath.EvalSymlinks(expected.servicePath)
	if err != nil || resolvedService != expected.servicePath {
		return ErrContainerRuntimeIdentityDrift
	}
	return nil
}

type inspectedStringList []string

func (values *inspectedStringList) UnmarshalJSON(value []byte) error {
	if len(value) == 0 {
		return ErrContainerRuntimeIdentityDrift
	}
	if value[0] == '"' {
		var scalar string
		if err := json.Unmarshal(value, &scalar); err != nil {
			return err
		}
		if scalar == "" {
			*values = nil
		} else {
			*values = []string{scalar}
		}
		return nil
	}
	var list []string
	if err := json.Unmarshal(value, &list); err != nil {
		return err
	}
	*values = list
	return nil
}

type inspectedHealthcheck struct {
	Test          []string `json:"Test"`
	Interval      int64    `json:"Interval"`
	Timeout       int64    `json:"Timeout"`
	StartPeriod   int64    `json:"StartPeriod"`
	StartInterval int64    `json:"StartInterval"`
	Retries       int      `json:"Retries"`
}

type inspectedUlimit struct {
	Name string `json:"Name"`
	Soft int64  `json:"Soft"`
	Hard int64  `json:"Hard"`
}

type inspectedContainer struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	Image string `json:"Image"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	Config struct {
		Image       string                `json:"Image"`
		User        string                `json:"User"`
		WorkingDir  string                `json:"WorkingDir"`
		Entrypoint  inspectedStringList   `json:"Entrypoint"`
		Cmd         []string              `json:"Cmd"`
		Env         []string              `json:"Env"`
		Labels      map[string]string     `json:"Labels"`
		OpenStdin   bool                  `json:"OpenStdin"`
		Tty         bool                  `json:"Tty"`
		StopTimeout *int                  `json:"StopTimeout"`
		Healthcheck *inspectedHealthcheck `json:"Healthcheck"`
	} `json:"Config"`
	HostConfig struct {
		ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
		Privileged     bool              `json:"Privileged"`
		NetworkMode    string            `json:"NetworkMode"`
		IpcMode        string            `json:"IpcMode"`
		PidMode        string            `json:"PidMode"`
		CapAdd         []string          `json:"CapAdd"`
		CapDrop        []string          `json:"CapDrop"`
		SecurityOpt    []string          `json:"SecurityOpt"`
		PidsLimit      int               `json:"PidsLimit"`
		Memory         int64             `json:"Memory"`
		MemorySwap     int64             `json:"MemorySwap"`
		NanoCPUs       int64             `json:"NanoCpus"`
		CPUPeriod      int64             `json:"CpuPeriod"`
		CPUQuota       int64             `json:"CpuQuota"`
		CgroupConf     map[string]string `json:"CgroupConf"`
		Tmpfs          map[string]string `json:"Tmpfs"`
		Devices        []json.RawMessage `json:"Devices"`
		DeviceRequests []json.RawMessage `json:"DeviceRequests"`
		OomKillDisable bool              `json:"OomKillDisable"`
		Ulimits        []inspectedUlimit `json:"Ulimits"`
		LogConfig      struct {
			Type string `json:"Type"`
		} `json:"LogConfig"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		Mode        string `json:"Mode"`
		RW          bool   `json:"RW"`
		Propagation string `json:"Propagation"`
	} `json:"Mounts"`
}

func (runtime *ContainerRuntime) inspectContainer(
	ctx context.Context,
	expected expectedContainerIdentity,
	requireRunning bool,
) error {
	output, err := runtime.run(ctx, runtime.cliOutputBytes, "container", "inspect", expected.containerID)
	if err != nil {
		return fmt.Errorf("%w: inspect language-server container", ErrContainerRuntimeUnavailable)
	}
	var values []inspectedContainer
	if json.Unmarshal(output, &values) != nil || len(values) != 1 {
		return fmt.Errorf("%w: malformed container inspect", ErrContainerRuntimeIdentityDrift)
	}
	value := values[0]
	profile, limits := expected.input.Profile, expected.input.Profile.EffectiveLimits
	expectedName := "/" + expected.name
	if runtime.runtimeName == "podman" {
		expectedName = expected.name
	}
	if value.ID != expected.containerID || value.Name != expectedName || value.Image != expected.imageID ||
		value.Config.Image != profile.Runtime.Image || value.Config.User != languageServerContainerUser ||
		value.Config.WorkingDir != expected.workdir || !equalStrings([]string(value.Config.Entrypoint), []string{profile.Runtime.ExecutablePath}) ||
		!equalStrings(value.Config.Cmd, profile.Runtime.Argv[1:]) || !value.Config.OpenStdin || value.Config.Tty ||
		value.Config.StopTimeout == nil || *value.Config.StopTimeout != max(1, (limits.ShutdownTimeoutMillis+999)/1000) ||
		!disabledHealthcheck(value.Config.Healthcheck) ||
		(!requireRunning && value.State.Running) || !value.HostConfig.ReadonlyRootfs || value.HostConfig.Privileged ||
		value.HostConfig.NetworkMode != "none" || value.HostConfig.IpcMode != "none" ||
		(value.HostConfig.PidMode != "" && value.HostConfig.PidMode != "private") || len(value.HostConfig.CapAdd) != 0 ||
		len(value.HostConfig.CapDrop) != 1 || !validAllCapabilityDrop(value.HostConfig.CapDrop[0]) ||
		!containsNoNewPrivileges(value.HostConfig.SecurityOpt) ||
		value.HostConfig.PidsLimit != limits.PIDLimit || value.HostConfig.Memory != limits.MemoryBytes ||
		!validContainerMemory(runtime.runtimeName, value.HostConfig.MemorySwap, value.HostConfig.CgroupConf, limits.MemoryBytes) ||
		!validContainerCPU(
			runtime.runtimeName, value.HostConfig.NanoCPUs, value.HostConfig.CPUPeriod, value.HostConfig.CPUQuota, limits.CPUMillis,
		) ||
		len(value.HostConfig.Devices) != 0 || len(value.HostConfig.DeviceRequests) != 0 || value.HostConfig.OomKillDisable ||
		!hasBoundedContainerUlimits(runtime.runtimeName, value.HostConfig.Ulimits) ||
		value.HostConfig.LogConfig.Type != "none" ||
		!hasFixedRuntimeEnvironment(value.Config.Env) ||
		!hasExactLabels(value.Config.Labels, expected.input) ||
		!validTmpfs(value.HostConfig.Tmpfs[languageServerTempMount], limits.TempBytes) ||
		!validTmpfs(value.HostConfig.Tmpfs[languageServerCacheMount], limits.CacheBytes) ||
		len(value.HostConfig.Tmpfs) != 2 || !hasExactReadonlyWorkspaceMount(value.Mounts, expected.workspaceRoot) {
		return ErrContainerRuntimeIdentityDrift
	}
	if requireRunning && !value.State.Running {
		return errContainerNotRunning
	}
	return nil
}

func (runtime *ContainerRuntime) cleanupAmbiguousCreate(
	ctx context.Context,
	expected expectedContainerIdentity,
) error {
	output, err := runtime.run(ctx, runtime.cliOutputBytes, "container", "inspect", expected.name)
	if err != nil {
		if isMissingContainerError(err) {
			return nil
		}
		return fmt.Errorf("%w: reconcile ambiguous container create", ErrContainerRuntimeUnavailable)
	}
	var values []inspectedContainer
	if json.Unmarshal(output, &values) != nil || len(values) != 1 || !containerIDPattern.MatchString(values[0].ID) {
		return ErrContainerRuntimeIdentityDrift
	}
	expected.containerID = values[0].ID
	if err := runtime.inspectContainer(ctx, expected, false); err != nil {
		return err
	}
	return runtime.removeContainer(context.Background(), expected.containerID, expected.input.Profile.EffectiveLimits)
}

func (runtime *ContainerRuntime) waitUntilRunning(
	ctx context.Context,
	expected expectedContainerIdentity,
) error {
	interval := time.NewTicker(10 * time.Millisecond)
	defer interval.Stop()
	for {
		err := runtime.inspectContainer(ctx, expected, true)
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrContainerRuntimeIdentityDrift) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: startup timeout", ErrContainerRuntimeUnavailable)
		case <-interval.C:
		}
	}
}

func (runtime *ContainerRuntime) removeContainer(
	ctx context.Context,
	containerID string,
	limits EffectiveLimits,
) error {
	if !containerIDPattern.MatchString(containerID) {
		return ErrContainerRuntimeInvalid
	}
	timeout := time.Duration(limits.ShutdownTimeoutMillis) * time.Millisecond
	if timeout <= 0 || timeout > languageServerHardCommandTimeout {
		timeout = languageServerHardCommandTimeout
	}
	commandCtx, cancel := boundedContext(ctx, timeout)
	defer cancel()
	_, err := runtime.cli.Run(commandCtx, runtime.cliOutputBytes, runtime.arguments("container", "rm", "--force", containerID)...)
	if err != nil && !isMissingContainerError(err) {
		return fmt.Errorf("%w: remove language-server container", ErrContainerRuntimeUnavailable)
	}
	return nil
}

func (runtime *ContainerRuntime) stopContainer(
	ctx context.Context,
	containerID string,
	limits EffectiveLimits,
) error {
	if !containerIDPattern.MatchString(containerID) {
		return ErrContainerRuntimeInvalid
	}
	timeout := time.Duration(limits.ShutdownTimeoutMillis) * time.Millisecond
	commandCtx, cancel := boundedContext(ctx, timeout)
	defer cancel()
	seconds := max(1, (limits.ShutdownTimeoutMillis+999)/1000)
	_, err := runtime.cli.Run(
		commandCtx, runtime.cliOutputBytes,
		runtime.arguments("container", "stop", "--time", strconv.Itoa(seconds), containerID)...,
	)
	if err != nil && !isMissingContainerError(err) {
		return fmt.Errorf("%w: stop language-server container", ErrContainerRuntimeUnavailable)
	}
	return nil
}

func (runtime *ContainerRuntime) run(
	ctx context.Context,
	limit int64,
	arguments ...string,
) ([]byte, error) {
	if ctx == nil {
		return nil, ErrContainerRuntimeInvalid
	}
	commandCtx, cancel := boundedContext(ctx, runtime.commandTimeout)
	defer cancel()
	return runtime.cli.Run(commandCtx, limit, runtime.arguments(arguments...)...)
}

func (runtime *ContainerRuntime) arguments(arguments ...string) []string {
	result := make([]string, 0, len(arguments)+2)
	if runtime.daemonHost != "" {
		flag := "--host"
		if runtime.runtimeName == "podman" {
			flag = "--url"
		}
		result = append(result, flag, runtime.daemonHost)
	}
	return append(result, arguments...)
}

func (runtime *ContainerRuntime) reserve(name string) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed {
		return ErrContainerRuntimeClosed
	}
	if _, exists := runtime.reserved[name]; exists {
		return ErrContainerRuntimeInvalid
	}
	if len(runtime.reserved) >= runtime.maxProcesses {
		return ErrContainerRuntimeExhausted
	}
	runtime.reserved[name] = struct{}{}
	runtime.starts.Add(1)
	return nil
}

func (runtime *ContainerRuntime) beginOperation() error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed {
		return ErrContainerRuntimeClosed
	}
	runtime.starts.Add(1)
	return nil
}

func (runtime *ContainerRuntime) commit(name string, process *containerLanguageServerProcess) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.processes[name] = process
}

func (runtime *ContainerRuntime) release(name string) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	delete(runtime.reserved, name)
	delete(runtime.processes, name)
}

func (runtime *ContainerRuntime) isClosed() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.closed
}

type containerLanguageServerProcess struct {
	runtime         *ContainerRuntime
	stream          containerRuntimeStream
	cancel          context.CancelFunc
	name            string
	containerID     string
	profile         ProfileIdentity
	maxFrameBytes   int64
	shutdownTimeout time.Duration
	reader          *bufio.Reader

	readMu     sync.Mutex
	writeMu    sync.Mutex
	mu         sync.Mutex
	stderr     bytes.Buffer
	failure    error
	exit       ContainerProcessExit
	done       chan struct{}
	stderrDone chan struct{}
	wait       sync.Once
	removeMu   sync.Mutex
	removed    bool
}

func newContainerLanguageServerProcess(
	runtime *ContainerRuntime,
	stream containerRuntimeStream,
	cancel context.CancelFunc,
	name string,
	containerID string,
	profile ProfileIdentity,
) *containerLanguageServerProcess {
	return &containerLanguageServerProcess{
		runtime: runtime, stream: stream, cancel: cancel, name: name, containerID: containerID,
		profile:         cloneProfiles([]ProfileIdentity{profile})[0],
		maxFrameBytes:   profile.EffectiveLimits.MaxFrameBytes,
		shutdownTimeout: time.Duration(profile.EffectiveLimits.ShutdownTimeoutMillis) * time.Millisecond,
		reader:          bufio.NewReaderSize(stream.Stdout(), languageServerMaximumHeaderBytes),
		done:            make(chan struct{}),
		stderrDone:      make(chan struct{}),
	}
}

func (process *containerLanguageServerProcess) Name() string { return process.name }

func (process *containerLanguageServerProcess) Profile() ProfileIdentity {
	return cloneProfiles([]ProfileIdentity{process.profile})[0]
}

func (process *containerLanguageServerProcess) startWaiters() {
	go process.captureStderr()
	go process.await()
}

func (process *containerLanguageServerProcess) WriteFrame(ctx context.Context, value []byte) error {
	if process == nil || ctx == nil || len(value) == 0 || int64(len(value)) > process.maxFrameBytes || !json.Valid(value) {
		if process != nil {
			process.failClosed(ErrContainerRuntimeExhausted)
		}
		return ErrContainerRuntimeExhausted
	}
	frame := make([]byte, 0, len(value)+64)
	frame = append(frame, "Content-Length: "...)
	frame = strconv.AppendInt(frame, int64(len(value)), 10)
	frame = append(frame, '\r', '\n', '\r', '\n')
	frame = append(frame, value...)

	process.writeMu.Lock()
	defer process.writeMu.Unlock()
	result := make(chan error, 1)
	go func() { result <- writeAll(process.stream.Stdin(), frame) }()
	select {
	case err := <-result:
		if err != nil {
			process.failClosed(err)
		}
		return err
	case <-process.done:
		return process.currentFailure()
	case <-ctx.Done():
		process.failClosed(ctx.Err())
		return ctx.Err()
	}
}

func (process *containerLanguageServerProcess) ReadFrame(ctx context.Context) ([]byte, error) {
	if process == nil || ctx == nil {
		return nil, ErrContainerRuntimeInvalid
	}
	process.readMu.Lock()
	defer process.readMu.Unlock()
	type result struct {
		value []byte
		err   error
	}
	completed := make(chan result, 1)
	go func() {
		value, err := readLSPFrame(process.reader, process.maxFrameBytes)
		completed <- result{value: value, err: err}
	}()
	select {
	case result := <-completed:
		if result.err != nil {
			process.failClosed(result.err)
		}
		return result.value, result.err
	case <-process.done:
		return nil, process.currentFailure()
	case <-ctx.Done():
		process.failClosed(ctx.Err())
		return nil, ctx.Err()
	}
}

func (process *containerLanguageServerProcess) Stderr() []byte {
	if process == nil {
		return nil
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	return append([]byte(nil), process.stderr.Bytes()...)
}

func (process *containerLanguageServerProcess) Wait(ctx context.Context) (ContainerProcessExit, error) {
	if process == nil || ctx == nil {
		return ContainerProcessExit{}, ErrContainerRuntimeInvalid
	}
	select {
	case <-process.done:
		process.mu.Lock()
		exit := process.exit
		process.mu.Unlock()
		return exit, nil
	case <-ctx.Done():
		return ContainerProcessExit{}, ctx.Err()
	}
}

func (process *containerLanguageServerProcess) Terminate(ctx context.Context) error {
	if process == nil || ctx == nil {
		return ErrContainerRuntimeInvalid
	}
	terminateCtx, cancelTerminate := boundedContext(
		ctx, process.shutdownTimeout+process.runtime.commandTimeout,
	)
	defer cancelTerminate()
	_ = process.stream.Stdin().Close()
	stopErr := process.runtime.stopContainer(terminateCtx, process.containerID, process.profile.EffectiveLimits)
	removeErr := process.removeContainer(terminateCtx)
	process.cancel()
	_ = process.stream.Close()
	select {
	case <-process.done:
		return errors.Join(stopErr, removeErr)
	case <-terminateCtx.Done():
		return errors.Join(stopErr, removeErr, terminateCtx.Err())
	}
}

func (process *containerLanguageServerProcess) captureStderr() {
	defer close(process.stderrDone)
	buffer := make([]byte, 32<<10)
	for {
		count, err := process.stream.Stderr().Read(buffer)
		if count > 0 {
			process.mu.Lock()
			remaining := process.maxFrameBytes - int64(process.stderr.Len())
			if int64(count) > remaining {
				process.failure = errors.Join(process.failure, ErrContainerRuntimeExhausted)
				process.mu.Unlock()
				process.failClosed(ErrContainerRuntimeExhausted)
				return
			}
			_, _ = process.stderr.Write(buffer[:count])
			process.mu.Unlock()
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				process.failClosed(err)
			}
			return
		}
	}
}

func (process *containerLanguageServerProcess) await() {
	process.wait.Do(func() {
		waitErr := process.stream.Wait()
		<-process.stderrDone
		removeErr := process.removeContainer(context.Background())
		process.cancel()
		_ = process.stream.Close()
		process.mu.Lock()
		process.failure = errors.Join(process.failure, waitErr, removeErr)
		process.exit = ContainerProcessExit{FinishedAt: time.Now().UTC(), Err: process.failure}
		process.mu.Unlock()
		process.runtime.release(process.name)
		close(process.done)
	})
}

func (process *containerLanguageServerProcess) removeContainer(ctx context.Context) error {
	process.removeMu.Lock()
	defer process.removeMu.Unlock()
	if process.removed {
		return nil
	}
	err := process.runtime.removeContainer(ctx, process.containerID, process.profile.EffectiveLimits)
	if err == nil {
		process.removed = true
	}
	return err
}

func (process *containerLanguageServerProcess) failClosed(err error) {
	if process == nil {
		return
	}
	process.mu.Lock()
	process.failure = errors.Join(process.failure, err)
	process.mu.Unlock()
	_ = process.stream.Stdin().Close()
	_ = process.stream.Stdout().Close()
	go func() {
		_ = process.removeContainer(context.Background())
		process.cancel()
		_ = process.stream.Close()
	}()
}

func (process *containerLanguageServerProcess) currentFailure() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.failure == nil {
		return io.EOF
	}
	return process.failure
}

func readLSPFrame(reader *bufio.Reader, maximum int64) ([]byte, error) {
	if reader == nil || maximum <= 0 {
		return nil, ErrContainerRuntimeInvalid
	}
	header := make([]byte, 0, 128)
	for !bytes.HasSuffix(header, []byte("\r\n\r\n")) {
		if len(header) >= languageServerMaximumHeaderBytes {
			return nil, ErrContainerRuntimeExhausted
		}
		value, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		header = append(header, value)
	}
	lines := strings.Split(string(header[:len(header)-4]), "\r\n")
	contentLength := int64(-1)
	contentTypeSeen := false
	for _, line := range lines {
		name, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) != name || strings.TrimSpace(value) == "" {
			return nil, ErrContainerRuntimeIdentityDrift
		}
		switch strings.ToLower(name) {
		case "content-length":
			value = strings.TrimSpace(value)
			if contentLength >= 0 || (len(value) > 1 && value[0] == '0') || strings.HasPrefix(value, "+") {
				return nil, ErrContainerRuntimeIdentityDrift
			}
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed <= 0 || parsed > maximum {
				return nil, ErrContainerRuntimeExhausted
			}
			contentLength = parsed
		case "content-type":
			if contentTypeSeen {
				return nil, ErrContainerRuntimeIdentityDrift
			}
			contentTypeSeen = true
			value = strings.ToLower(strings.TrimSpace(value))
			if value != "application/vscode-jsonrpc; charset=utf-8" && value != "application/vscode-jsonrpc; charset=utf8" {
				return nil, ErrContainerRuntimeIdentityDrift
			}
		default:
			return nil, ErrContainerRuntimeIdentityDrift
		}
	}
	if contentLength <= 0 {
		return nil, ErrContainerRuntimeIdentityDrift
	}
	value := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, value); err != nil {
		return nil, err
	}
	if !json.Valid(value) {
		return nil, ErrContainerRuntimeIdentityDrift
	}
	return value, nil
}

func verifyContainerExecutable(archive []byte, executablePath, expectedDigest string, maximumBytes int64) error {
	if maximumBytes <= 0 || maximumBytes > languageServerMaximumArchiveBytes || len(archive) == 0 ||
		int64(len(archive)) > maximumBytes+(1<<20) ||
		!digestPattern.MatchString(expectedDigest) {
		return ErrContainerRuntimeIdentityDrift
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	expectedName := path.Base(executablePath)
	var found bool
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ErrContainerRuntimeIdentityDrift
		}
		clean := path.Clean(strings.TrimPrefix(header.Name, "./"))
		if clean == "." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") ||
			header.Linkname != "" {
			return ErrContainerRuntimeIdentityDrift
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return ErrContainerRuntimeIdentityDrift
		}
		if found || path.Base(clean) != expectedName || header.Size <= 0 ||
			header.Size > maximumBytes || header.Mode&0o7000 != 0 ||
			!runtimeUserCanReadAndExecute(header.Uid, header.Gid, header.Mode) {
			return ErrContainerRuntimeIdentityDrift
		}
		hash := sha256.New()
		written, err := io.CopyN(hash, reader, header.Size)
		if err != nil || written != header.Size {
			return ErrContainerRuntimeIdentityDrift
		}
		actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
		if subtle.ConstantTimeCompare([]byte(actual), []byte(expectedDigest)) != 1 {
			return ErrContainerRuntimeIdentityDrift
		}
		found = true
	}
	if !found {
		return ErrContainerRuntimeIdentityDrift
	}
	return nil
}

func runtimeUserCanReadAndExecute(uid, gid int, mode int64) bool {
	permissions := mode & 0o7
	if uid == 65532 {
		permissions = (mode >> 6) & 0o7
	} else if gid == 65532 {
		permissions = (mode >> 3) & 0o7
	}
	return permissions&0o5 == 0o5
}

func validateContainerRuntimeBinary(value string) (string, string, error) {
	if value == "" || strings.TrimSpace(value) != value || !filepath.IsAbs(value) ||
		filepath.Clean(value) != value || strings.ContainsAny(value, "\x00\r\n") {
		return "", "", ErrContainerRuntimeInvalid
	}
	info, err := os.Lstat(value)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&0o111 == 0 {
		return "", "", ErrContainerRuntimeInvalid
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil || resolved != value {
		return "", "", ErrContainerRuntimeInvalid
	}
	runtimeName := strings.ToLower(filepath.Base(value))
	if runtimeName != "docker" && runtimeName != "podman" {
		return "", "", ErrContainerRuntimeInvalid
	}
	return value, runtimeName, nil
}

func validateContainerDaemonHost(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
		return "", ErrContainerRuntimeInvalid
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "unix" || parsed.Host != "" || parsed.RawQuery != "" ||
		parsed.Fragment != "" || !filepath.IsAbs(parsed.Path) || filepath.Clean(parsed.Path) != parsed.Path {
		return "", ErrContainerRuntimeInvalid
	}
	info, err := os.Lstat(parsed.Path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return "", ErrContainerRuntimeInvalid
	}
	resolved, err := filepath.EvalSymlinks(parsed.Path)
	if err != nil || resolved != parsed.Path {
		return "", ErrContainerRuntimeInvalid
	}
	return value, nil
}

func validateContainerWorkspace(workspaceRoot, serviceRoot string) (string, string, string, error) {
	if workspaceRoot == "" || len(workspaceRoot) > 4096 || strings.TrimSpace(workspaceRoot) != workspaceRoot ||
		!filepath.IsAbs(workspaceRoot) || filepath.Clean(workspaceRoot) != workspaceRoot ||
		strings.ContainsAny(workspaceRoot, ",\x00\r\n") {
		return "", "", "", ErrContainerRuntimeInvalid
	}
	info, err := os.Lstat(workspaceRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o5 != 0o5 {
		return "", "", "", ErrContainerRuntimeInvalid
	}
	resolved, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil || resolved != workspaceRoot {
		return "", "", "", ErrContainerRuntimeInvalid
	}
	if !canonicalServiceRoot(serviceRoot) {
		return "", "", "", ErrContainerRuntimeInvalid
	}
	servicePath := workspaceRoot
	workdir := languageServerWorkspaceMount
	if serviceRoot != "." {
		for _, segment := range strings.Split(serviceRoot, "/") {
			servicePath = filepath.Join(servicePath, segment)
			segmentInfo, segmentErr := os.Lstat(servicePath)
			if segmentErr != nil || !segmentInfo.IsDir() || segmentInfo.Mode()&os.ModeSymlink != 0 ||
				segmentInfo.Mode().Perm()&0o5 != 0o5 {
				return "", "", "", ErrContainerRuntimeInvalid
			}
		}
		workdir += "/" + serviceRoot
	}
	info, err = os.Lstat(servicePath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o5 != 0o5 {
		return "", "", "", ErrContainerRuntimeInvalid
	}
	resolvedService, err := filepath.EvalSymlinks(servicePath)
	if err != nil || resolvedService != servicePath ||
		(resolvedService != workspaceRoot && !strings.HasPrefix(resolvedService, workspaceRoot+string(filepath.Separator))) {
		return "", "", "", ErrContainerRuntimeInvalid
	}
	return workspaceRoot, serviceRoot, workdir, nil
}

func canonicalServiceRoot(value string) bool {
	if value == "." {
		return true
	}
	if value == "" || len(value) > 400 || strings.TrimSpace(value) != value || strings.HasPrefix(value, "/") ||
		strings.Contains(value, `\`) || path.Clean(value) != value {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		lower := strings.ToLower(segment)
		if segment == "" || segment == "." || segment == ".." || lower == ".git" ||
			strings.HasPrefix(lower, ".env") || strings.ContainsAny(segment, "\x00\r\n") {
			return false
		}
	}
	return true
}

func forbiddenContainerExecutablePath(value string) bool {
	for _, root := range []string{
		languageServerWorkspaceMount, languageServerTempMount, languageServerCacheMount,
	} {
		if value == root || strings.HasPrefix(value, root+"/") {
			return true
		}
	}
	return false
}

func languageServerContainerName(connectionID, bindingID string) string {
	return "worksflow-lsp-" + strings.ReplaceAll(connectionID, "-", "") + "-" + strings.ReplaceAll(bindingID, "-", "")
}

func formatContainerCPUs(cpuMillis int) string {
	return strconv.FormatFloat(float64(cpuMillis)/1000, 'f', 3, 64)
}

func validRuntimeImageID(runtimeName, value string) bool {
	if runtimeName == "docker" {
		return digestPattern.MatchString(value)
	}
	return runtimeName == "podman" && (digestPattern.MatchString(value) || containerIDPattern.MatchString(value))
}

func validAllCapabilityDrop(value string) bool {
	return strings.EqualFold(value, "ALL") || strings.EqualFold(value, "CAP_ALL")
}

func disabledHealthcheck(value *inspectedHealthcheck) bool {
	return value != nil && equalStrings(value.Test, []string{"NONE"}) && value.Interval == 0 &&
		value.Timeout == 0 && value.StartPeriod == 0 && value.StartInterval == 0 && value.Retries == 0
}

func validContainerCPU(runtimeName string, nanoCPUs, period, quota int64, cpuMillis int) bool {
	expectedNanoCPUs := int64(cpuMillis) * 1_000_000
	if runtimeName == "docker" {
		return nanoCPUs == expectedNanoCPUs
	}
	if runtimeName != "podman" {
		return false
	}
	if nanoCPUs == expectedNanoCPUs {
		return true
	}
	return nanoCPUs == 0 && period > 0 && period <= 1_000_000_000 && quota > 0 &&
		quota*1000 == period*int64(cpuMillis)
}

func validContainerMemory(runtimeName string, memorySwap int64, cgroupConf map[string]string, memoryBytes int64) bool {
	if runtimeName == "docker" {
		return memorySwap == memoryBytes && len(cgroupConf) == 0
	}
	return runtimeName == "podman" && memorySwap == 0 && len(cgroupConf) == 1 &&
		cgroupConf["memory.swap.max"] == "0"
}

func hasBoundedContainerUlimits(runtimeName string, values []inspectedUlimit) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		name := strings.ToLower(value.Name)
		if seen[name] {
			return false
		}
		seen[name] = true
		switch name {
		case "core":
			if value.Soft != 0 || value.Hard != 0 {
				return false
			}
		case "nofile":
			if value.Soft != 4096 || value.Hard != 4096 {
				return false
			}
		case "nproc":
			if runtimeName != "podman" || value.Soft <= 0 || value.Hard < value.Soft || value.Hard > 1<<30 {
				return false
			}
		default:
			return false
		}
	}
	return seen["core"] && seen["nofile"] &&
		((runtimeName == "docker" && len(values) == 2) || (runtimeName == "podman" && len(values) >= 2 && len(values) <= 3))
}

func hasFixedRuntimeEnvironment(values []string) bool {
	for _, expected := range []string{"HOME=/tmp", "TMPDIR=/tmp", "XDG_CACHE_HOME=/cache"} {
		if !containsExactString(values, expected) {
			return false
		}
	}
	return true
}

func hasExactLabels(values map[string]string, input ContainerStartInput) bool {
	expected := map[string]string{
		"worksflow.kind":             "language-server",
		"worksflow.lsp.connection":   input.ConnectionID,
		"worksflow.lsp.binding":      input.BindingID,
		"worksflow.lsp.profile":      input.Profile.ID,
		"worksflow.lsp.profile-hash": input.Profile.ContentHash,
		"worksflow.lsp.release":      input.Profile.TemplateRelease.ID,
		"worksflow.lsp.release-hash": input.Profile.TemplateRelease.ContentHash,
	}
	for key, expectedValue := range expected {
		if values[key] != expectedValue {
			return false
		}
	}
	return true
}

func validTmpfs(value string, expectedBytes int64) bool {
	if value == "" {
		return false
	}
	tokens := strings.Split(value, ",")
	seen := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		if token == "" || seen[token] {
			return false
		}
		if token != "rw" && token != "noexec" && token != "nosuid" && token != "nodev" &&
			token != "mode=0700" && token != "mode=700" && token != "uid=65532" && token != "gid=65532" &&
			token != "size="+strconv.FormatInt(expectedBytes, 10) {
			return false
		}
		seen[token] = true
	}
	return len(seen) == 8 && seen["rw"] && seen["noexec"] && seen["nosuid"] && seen["nodev"] &&
		(seen["mode=0700"] != seen["mode=700"]) && seen["uid=65532"] && seen["gid=65532"] &&
		seen["size="+strconv.FormatInt(expectedBytes, 10)]
}

func hasExactReadonlyWorkspaceMount(values []struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	Mode        string `json:"Mode"`
	RW          bool   `json:"RW"`
	Propagation string `json:"Propagation"`
}, workspaceRoot string) bool {
	found := false
	for _, value := range values {
		if value.Type == "volume" {
			return false
		}
		if value.Type != "bind" {
			continue
		}
		if found || value.Source != workspaceRoot || value.Destination != languageServerWorkspaceMount ||
			value.RW || (value.Mode != "" && value.Mode != "ro" && value.Mode != "readonly") || value.Propagation != "rprivate" {
			return false
		}
		found = true
	}
	return found
}

func containsExactString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsNoNewPrivileges(values []string) bool {
	if len(values) != 1 {
		return false
	}
	for _, value := range values {
		value = strings.ToLower(value)
		if value == "no-new-privileges" || value == "no-new-privileges:true" {
			return true
		}
	}
	return false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func boundedContext(parent context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= maximum {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, maximum)
}

func writeAll(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		count, err := writer.Write(value)
		if err != nil {
			return err
		}
		if count <= 0 || count > len(value) {
			return io.ErrShortWrite
		}
		value = value[count:]
	}
	return nil
}

func isMissingContainerError(err error) bool {
	if err == nil {
		return false
	}
	value := strings.ToLower(err.Error())
	return strings.Contains(value, "no such container") || strings.Contains(value, "no such object") ||
		(strings.Contains(value, "container") &&
			(strings.Contains(value, "not found") || strings.Contains(value, "does not exist")))
}

type osContainerRuntimeCLI struct {
	binary      string
	binaryInfo  os.FileInfo
	daemonPath  string
	daemonInfo  os.FileInfo
	environment []string
	configRoot  string
	close       sync.Once
	closeErr    error
}

func (cli *osContainerRuntimeCLI) Run(
	ctx context.Context,
	limit int64,
	arguments ...string,
) ([]byte, error) {
	if cli == nil || ctx == nil || cli.binary == "" || limit <= 0 || limit > languageServerMaximumArchiveBytes {
		return nil, ErrContainerRuntimeInvalid
	}
	if err := cli.validateAuthority(); err != nil {
		return nil, err
	}
	output := &strictRuntimeBuffer{limit: limit}
	command := exec.CommandContext(ctx, cli.binary, arguments...)
	command.Env = append([]string(nil), cli.environment...)
	command.Stdout, command.Stderr = output, output
	if err := command.Run(); err != nil {
		if output.wasOverflowed() {
			return nil, ErrContainerRuntimeExhausted
		}
		return nil, &containerCLIError{arguments: append([]string(nil), arguments...), cause: err, output: output.safeString()}
	}
	if output.wasOverflowed() {
		return nil, ErrContainerRuntimeExhausted
	}
	return output.bytes(), nil
}

func (cli *osContainerRuntimeCLI) Start(
	ctx context.Context,
	arguments ...string,
) (containerRuntimeStream, error) {
	if cli == nil || ctx == nil || cli.binary == "" {
		return nil, ErrContainerRuntimeInvalid
	}
	if err := cli.validateAuthority(); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, cli.binary, arguments...)
	command.Env = append([]string(nil), cli.environment...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, &containerCLIError{arguments: append([]string(nil), arguments...), cause: err}
	}
	return &osContainerRuntimeStream{
		command: command, stdin: stdin, stdout: stdout, stderr: stderr,
	}, nil
}

func (cli *osContainerRuntimeCLI) Close() error {
	if cli == nil {
		return nil
	}
	cli.close.Do(func() { cli.closeErr = os.RemoveAll(cli.configRoot) })
	return cli.closeErr
}

func (cli *osContainerRuntimeCLI) validateAuthority() error {
	binaryInfo, err := os.Lstat(cli.binary)
	if err != nil || binaryInfo.Mode()&os.ModeSymlink != 0 || !binaryInfo.Mode().IsRegular() ||
		binaryInfo.Mode()&0o111 == 0 || !sameRuntimeFile(cli.binaryInfo, binaryInfo) {
		return ErrContainerRuntimeIdentityDrift
	}
	if cli.daemonPath != "" {
		daemonInfo, err := os.Lstat(cli.daemonPath)
		if err != nil || daemonInfo.Mode()&os.ModeSymlink != 0 || daemonInfo.Mode()&os.ModeSocket == 0 ||
			!sameRuntimeFile(cli.daemonInfo, daemonInfo) {
			return ErrContainerRuntimeIdentityDrift
		}
	}
	return nil
}

func sameRuntimeFile(expected, actual os.FileInfo) bool {
	return expected != nil && actual != nil && os.SameFile(expected, actual) && expected.Mode() == actual.Mode() &&
		expected.Size() == actual.Size() && expected.ModTime().Equal(actual.ModTime())
}

type osContainerRuntimeStream struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	wait    sync.Once
	waitErr error
	close   sync.Once
}

func (stream *osContainerRuntimeStream) Stdin() io.WriteCloser { return stream.stdin }
func (stream *osContainerRuntimeStream) Stdout() io.ReadCloser { return stream.stdout }
func (stream *osContainerRuntimeStream) Stderr() io.ReadCloser { return stream.stderr }
func (stream *osContainerRuntimeStream) Wait() error {
	stream.wait.Do(func() { stream.waitErr = stream.command.Wait() })
	return stream.waitErr
}
func (stream *osContainerRuntimeStream) Close() error {
	var result error
	stream.close.Do(func() {
		result = errors.Join(stream.stdin.Close(), stream.stdout.Close(), stream.stderr.Close())
	})
	return result
}

type strictRuntimeBuffer struct {
	mu       sync.Mutex
	value    bytes.Buffer
	limit    int64
	overflow bool
}

func (buffer *strictRuntimeBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	remaining := buffer.limit - int64(buffer.value.Len())
	if int64(len(value)) > remaining {
		if remaining > 0 {
			_, _ = buffer.value.Write(value[:remaining])
		}
		buffer.overflow = true
		return len(value), ErrContainerRuntimeExhausted
	}
	_, _ = buffer.value.Write(value)
	return len(value), nil
}

func (buffer *strictRuntimeBuffer) bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.value.Bytes()...)
}

func (buffer *strictRuntimeBuffer) wasOverflowed() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.overflow
}

func (buffer *strictRuntimeBuffer) safeString() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	value := strings.TrimSpace(buffer.value.String())
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}

type containerCLIError struct {
	arguments []string
	cause     error
	output    string
}

func (err *containerCLIError) Error() string {
	if err.output == "" {
		return fmt.Sprintf("container CLI command failed: %v", err.cause)
	}
	return fmt.Sprintf("container CLI command failed: %v (%s)", err.cause, err.output)
}

func (err *containerCLIError) Unwrap() error { return err.cause }

var _ LanguageServerRuntime = (*ContainerRuntime)(nil)
var _ LanguageServerProcess = (*containerLanguageServerProcess)(nil)
