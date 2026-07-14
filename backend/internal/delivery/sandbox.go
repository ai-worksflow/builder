package delivery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type SandboxRequest struct {
	Ecosystem           string
	Check               CheckID
	DependencyDirectory string
}

type SandboxResult struct {
	ExitCode  int
	Output    string
	Truncated bool
	Duration  time.Duration
}

type Sandbox interface {
	Kind() string
	Run(context.Context, string, SandboxRequest) (SandboxResult, error)
}

type ContainerSandboxConfig struct {
	RuntimeBinary       string
	DaemonHost          string
	WorkspaceRoot       string
	NodeImage           string
	GoImage             string
	Timeout             time.Duration
	OutputLimit         int
	MemoryLimit         string
	CPULimit            string
	PIDsLimit           int
	ResolverNetwork     string
	ResolverNPMRegistry string
	ResolverGoProxy     string
	ResolverGoSumDB     string
	ResolverTimeout     time.Duration
	ResolverOutputLimit int
	ResolverMemoryLimit string
	ResolverCPULimit    string
	ResolverPIDsLimit   int
}

type ContainerSandbox struct {
	runtimePath         string
	daemonHost          string
	workspaceRoot       string
	nodeImage           string
	goImage             string
	timeout             time.Duration
	outputLimit         int
	memory              string
	cpus                string
	pids                int
	resolverNetwork     string
	resolverPolicy      DependencyPolicy
	resolverTimeout     time.Duration
	resolverOutputLimit int
	resolverMemory      string
	resolverCPUs        string
	resolverPIDs        int
}

var containerImagePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]{0,255}$`)
var containerNetworkPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func NewContainerSandbox(config ContainerSandboxConfig) (*ContainerSandbox, error) {
	binary := strings.TrimSpace(config.RuntimeBinary)
	if binary == "" {
		binary = "docker"
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("locate container runtime: %w", err)
	}
	base := strings.ToLower(filepath.Base(path))
	if base != "docker" && base != "podman" {
		return nil, errors.New("quality container runtime must be docker or podman")
	}
	daemonHost, err := validateContainerDaemonHost(config.DaemonHost)
	if err != nil {
		return nil, err
	}
	if !containerImagePattern.MatchString(config.NodeImage) || !containerImagePattern.MatchString(config.GoImage) {
		return nil, errors.New("fixed node and go sandbox images are required")
	}
	workspaceRoot, err := validateWorkspaceRoot(config.WorkspaceRoot, daemonHost)
	if err != nil {
		return nil, err
	}
	if config.Timeout <= 0 {
		config.Timeout = 2 * time.Minute
	}
	if config.OutputLimit <= 0 || config.OutputLimit > 8<<20 {
		config.OutputLimit = MaxCommandOutput
	}
	if config.MemoryLimit == "" {
		config.MemoryLimit = "512m"
	}
	if config.CPULimit == "" {
		config.CPULimit = "1.0"
	}
	if config.PIDsLimit <= 0 {
		config.PIDsLimit = 128
	}
	resolverNetwork := strings.TrimSpace(config.ResolverNetwork)
	if resolverNetwork == "" {
		resolverNetwork = "bridge"
	}
	if !containerNetworkPattern.MatchString(resolverNetwork) || resolverNetwork == "host" || resolverNetwork == "none" || strings.HasPrefix(resolverNetwork, "container") {
		return nil, errors.New("dependency resolver network must be an explicit bounded bridge network")
	}
	npmRegistry := strings.TrimSpace(config.ResolverNPMRegistry)
	if npmRegistry == "" {
		npmRegistry = defaultNPMRegistry
	}
	if _, err := validateResolverURL(npmRegistry); err != nil {
		return nil, fmt.Errorf("validate npm resolver registry: %w", err)
	}
	goProxy := strings.TrimSpace(config.ResolverGoProxy)
	if goProxy == "" {
		goProxy = defaultGoProxy
	}
	if strings.Contains(goProxy, ",") {
		return nil, errors.New("Go resolver proxy must be one fixed HTTPS origin without direct fallback")
	}
	if _, err := validateResolverURL(goProxy); err != nil {
		return nil, fmt.Errorf("validate Go resolver proxy: %w", err)
	}
	goSumDB := strings.TrimSpace(config.ResolverGoSumDB)
	if goSumDB == "" {
		goSumDB = defaultGoSumDB
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,255}$`).MatchString(goSumDB) || strings.EqualFold(goSumDB, "off") {
		return nil, errors.New("Go resolver checksum database must be a fixed public verifier name")
	}
	if config.ResolverTimeout <= 0 {
		config.ResolverTimeout = 3 * time.Minute
	}
	if config.ResolverOutputLimit <= 0 || config.ResolverOutputLimit > 8<<20 {
		config.ResolverOutputLimit = config.OutputLimit
	}
	if config.ResolverMemoryLimit == "" {
		config.ResolverMemoryLimit = config.MemoryLimit
	}
	if config.ResolverCPULimit == "" {
		config.ResolverCPULimit = config.CPULimit
	}
	if config.ResolverPIDsLimit <= 0 {
		config.ResolverPIDsLimit = config.PIDsLimit
	}
	return &ContainerSandbox{
		runtimePath: path, daemonHost: daemonHost, workspaceRoot: workspaceRoot, nodeImage: config.NodeImage, goImage: config.GoImage,
		timeout: config.Timeout, outputLimit: config.OutputLimit,
		memory: config.MemoryLimit, cpus: config.CPULimit, pids: config.PIDsLimit,
		resolverNetwork: resolverNetwork,
		resolverPolicy:  DependencyPolicy{NPMRegistry: npmRegistry, GoProxy: goProxy, GoSumDB: goSumDB},
		resolverTimeout: config.ResolverTimeout, resolverOutputLimit: config.ResolverOutputLimit,
		resolverMemory: config.ResolverMemoryLimit, resolverCPUs: config.ResolverCPULimit, resolverPIDs: config.ResolverPIDsLimit,
	}, nil
}

func (*ContainerSandbox) Kind() string { return "container" }

func (s *ContainerSandbox) DependencyPolicy() DependencyPolicy { return s.resolverPolicy }

func (s *ContainerSandbox) ImagesDigestPinned() bool {
	return digestPinnedContainerImage(s.nodeImage) && digestPinnedContainerImage(s.goImage)
}

func digestPinnedContainerImage(value string) bool {
	parts := strings.Split(value, "@sha256:")
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 64 {
		return false
	}
	for _, character := range parts[1] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// Readiness verifies both daemon reachability and the immutable image pins.
// It never pulls images; a missing pre-provisioned image keeps the API out of
// rotation instead of failing the first quality run.
func (s *ContainerSandbox) Readiness(ctx context.Context) error {
	configDirectory, err := os.MkdirTemp("", "worksflow-readiness-config-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(configDirectory)
	for _, args := range [][]string{
		{"version"},
		{"image", "inspect", s.nodeImage, s.goImage},
	} {
		command := exec.CommandContext(ctx, s.runtimePath, args...)
		command.Env = sandboxClientEnvironment(configDirectory, s.daemonHost)
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		if err := command.Run(); err != nil {
			return fmt.Errorf("quality sandbox readiness failed: %w", err)
		}
	}
	return nil
}

func (s *ContainerSandbox) Run(ctx context.Context, workspaceDirectory string, request SandboxRequest) (SandboxResult, error) {
	image, command, err := s.fixedCommand(request)
	if err != nil {
		return SandboxResult{}, err
	}
	absolute, err := s.validateMountedDirectory(workspaceDirectory, "quality workspace")
	if err != nil {
		return SandboxResult{}, err
	}
	name := "worksflow-quality-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	configDirectory, err := os.MkdirTemp("", "worksflow-container-config-")
	if err != nil {
		return SandboxResult{}, wrapInternal("create isolated container client config", err)
	}
	defer os.RemoveAll(configDirectory)

	args, err := s.qualityRunArgs(name, absolute, request, image, command)
	if err != nil {
		return SandboxResult{}, err
	}
	return s.executeContainer(ctx, name, configDirectory, args, s.timeout, s.outputLimit, "quality check")
}

func (s *ContainerSandbox) qualityRunArgs(name, workspace string, request SandboxRequest, image string, command []string) ([]string, error) {
	args := s.baseRunArgs(name, "none", s.memory, s.cpus, s.pids)
	args = append(args,
		"--mount", "type=bind,src="+workspace+",dst=/workspace",
		"--workdir", "/workspace",
		"--env", "HOME=/tmp", "--env", "CI=1", "--env", "NO_UPDATE_NOTIFIER=1",
	)
	if strings.TrimSpace(request.DependencyDirectory) != "" {
		dependencies, err := s.validateMountedDirectory(request.DependencyDirectory, "dependency cache")
		if err != nil {
			return nil, err
		}
		switch request.Ecosystem {
		case "node":
			nodeModules := filepath.Join(dependencies, "node_modules")
			if info, statErr := os.Lstat(nodeModules); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, Invalid("dependencies", "prepared node_modules directory does not exist")
			}
			args = append(args,
				"--mount", "type=bind,src="+nodeModules+",dst=/workspace/node_modules,readonly",
				"--env", "npm_config_offline=true", "--env", "npm_config_ignore_scripts=true",
			)
		case "go":
			moduleCache := filepath.Join(dependencies, "pkg", "mod")
			if info, statErr := os.Lstat(moduleCache); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, Invalid("dependencies", "prepared Go module cache does not exist")
			}
			args = append(args,
				"--mount", "type=bind,src="+moduleCache+",dst=/go/pkg/mod,readonly",
				"--env", "GOMODCACHE=/go/pkg/mod", "--env", "GOPROXY=off",
				"--env", "GOSUMDB="+s.resolverPolicy.GoSumDB, "--env", "GOTOOLCHAIN=local",
				"--env", "GONOSUMDB=", "--env", "GONOPROXY=", "--env", "GOPRIVATE=",
				"--env", "GOFLAGS=-mod=readonly", "--env", "GOCACHE=/tmp/go-build",
			)
		default:
			return nil, Invalid("dependencies", "dependency cache ecosystem is unsupported")
		}
	}
	args = append(args, image)
	args = append(args, command...)
	return args, nil
}

func (s *ContainerSandbox) PrepareDependencies(ctx context.Context, dependencyDirectory string, request DependencyPreparationRequest) (SandboxResult, error) {
	absolute, err := s.validateMountedDirectory(dependencyDirectory, "dependency resolver")
	if err != nil {
		return SandboxResult{}, err
	}
	var image string
	var command []string
	switch request.Ecosystem {
	case "node":
		if !regularFile(filepath.Join(absolute, "package.json")) || !regularFile(filepath.Join(absolute, "package-lock.json")) {
			return SandboxResult{}, Invalid("dependencies", "Node resolver requires only package.json and package-lock.json")
		}
		image = s.nodeImage
		command = []string{"npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--registry=" + s.resolverPolicy.NPMRegistry}
	case "go":
		if !regularFile(filepath.Join(absolute, "go.mod")) {
			return SandboxResult{}, Invalid("dependencies", "Go resolver requires go.mod")
		}
		image = s.goImage
		command = []string{"go", "mod", "download", "all"}
	default:
		return SandboxResult{}, Invalid("dependencies", "dependency resolver ecosystem is unsupported")
	}
	if err := validateResolverDirectoryContents(absolute, request.Ecosystem); err != nil {
		return SandboxResult{}, err
	}
	name := "worksflow-resolver-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	configDirectory, err := os.MkdirTemp("", "worksflow-container-config-")
	if err != nil {
		return SandboxResult{}, wrapInternal("create isolated container client config", err)
	}
	defer os.RemoveAll(configDirectory)
	args := s.dependencyRunArgs(name, absolute, request.Ecosystem, image, command)
	return s.executeContainer(ctx, name, configDirectory, args, s.resolverTimeout, s.resolverOutputLimit, "dependency resolution")
}

func (s *ContainerSandbox) dependencyRunArgs(name, directory, ecosystem, image string, command []string) []string {
	args := s.baseRunArgs(name, s.resolverNetwork, s.resolverMemory, s.resolverCPUs, s.resolverPIDs)
	args = append(args,
		"--mount", "type=bind,src="+directory+",dst=/resolver",
		"--workdir", "/resolver", "--env", "HOME=/tmp", "--env", "CI=1",
	)
	if ecosystem == "node" {
		args = append(args,
			"--env", "npm_config_registry="+s.resolverPolicy.NPMRegistry,
			"--env", "npm_config_ignore_scripts=true", "--env", "npm_config_audit=false",
			"--env", "npm_config_fund=false", "--env", "npm_config_update_notifier=false",
		)
	} else {
		args = append(args,
			"--env", "GOPROXY="+s.resolverPolicy.GoProxy, "--env", "GOSUMDB="+s.resolverPolicy.GoSumDB,
			"--env", "GONOSUMDB=", "--env", "GONOPROXY=", "--env", "GOPRIVATE=",
			"--env", "GOTOOLCHAIN=local", "--env", "GOMODCACHE=/resolver/pkg/mod",
			"--env", "GOCACHE=/tmp/go-build", "--env", "GOFLAGS=-mod=readonly",
		)
	}
	args = append(args, image)
	args = append(args, command...)
	return args
}

func (s *ContainerSandbox) baseRunArgs(name, network, memory, cpus string, pids int) []string {
	return []string{
		"run", "--rm", "--pull", "never", "--name", name,
		"--network", network, "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--pids-limit", strconv.Itoa(pids),
		"--memory", memory, "--cpus", cpus,
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=64m",
		"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
	}
}

func (s *ContainerSandbox) executeContainer(ctx context.Context, name, configDirectory string, args []string, timeout time.Duration, outputLimit int, operation string) (SandboxResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	buffer := newLimitedBuffer(outputLimit)
	started := time.Now()
	cmd := exec.CommandContext(runCtx, s.runtimePath, args...)
	cmd.Env = sandboxClientEnvironment(configDirectory, s.daemonHost)
	cmd.Stdout, cmd.Stderr = buffer, buffer
	err := cmd.Run()
	result := SandboxResult{ExitCode: 0, Output: buffer.String(), Truncated: buffer.Truncated(), Duration: time.Since(started)}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			result.ExitCode = exitError.ExitCode()
		} else if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			result.ExitCode = -1
			s.cleanupContainer(name, configDirectory)
			return result, NewError(CodeSandboxTimeout, httpStatusGatewayTimeout, operation+" exceeded its fixed sandbox timeout")
		} else {
			result.ExitCode = -1
			s.cleanupContainer(name, configDirectory)
			return result, &DeliveryError{Code: CodeSandboxUnavailable, Status: 503, Detail: operation + " container could not start", Cause: err}
		}
	}
	s.cleanupContainer(name, configDirectory)
	return result, nil
}

const httpStatusGatewayTimeout = 504

func (s *ContainerSandbox) fixedCommand(request SandboxRequest) (string, []string, error) {
	commands := map[string]map[CheckID][]string{
		"node": {
			CheckBuild: {"npm", "run", "build", "--if-present"},
			CheckType:  {"npx", "--no-install", "tsc", "--noEmit", "--pretty", "false"},
			CheckLint:  {"npm", "run", "lint", "--if-present"},
			CheckTest:  {"npm", "test", "--if-present"},
		},
		"go": {
			CheckBuild: {"go", "build", "./..."},
			CheckType:  {"go", "vet", "./..."},
			CheckLint:  {"gofmt", "-d", "."},
			CheckTest:  {"go", "test", "./..."},
		},
	}
	command := commands[request.Ecosystem][request.Check]
	if len(command) == 0 {
		return "", nil, Invalid("check", "requested quality check has no fixed sandbox command")
	}
	image := s.nodeImage
	if request.Ecosystem == "go" {
		image = s.goImage
	}
	return image, append([]string(nil), command...), nil
}

func (s *ContainerSandbox) cleanupContainer(name, configDirectory string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.runtimePath, "rm", "-f", name)
	cmd.Env = sandboxClientEnvironment(configDirectory, s.daemonHost)
	_ = cmd.Run()
}

func sandboxClientEnvironment(configDirectory, daemonHost string) []string {
	path := "/usr/local/bin:/usr/bin:/bin"
	if runtime.GOOS == "windows" {
		path = os.Getenv("PATH")
	}
	environment := []string{"PATH=" + path, "DOCKER_CONFIG=" + configDirectory, "HOME=" + configDirectory}
	if daemonHost != "" {
		environment = append(environment, "DOCKER_HOST="+daemonHost)
	}
	return environment
}

func validateContainerDaemonHost(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("quality container daemon host is invalid")
	}
	if strings.HasPrefix(value, "unix:///") {
		path := strings.TrimPrefix(value, "unix://")
		if filepath.Clean(path) != path || !filepath.IsAbs(path) {
			return "", errors.New("quality container daemon unix socket must be an absolute normalized path")
		}
		return value, nil
	}
	if strings.HasPrefix(value, "tcp://") {
		host := strings.TrimPrefix(value, "tcp://")
		if host == "" || strings.ContainsAny(host, "/?#@") {
			return "", errors.New("quality container daemon TCP host must be host:port")
		}
		if _, port, err := net.SplitHostPort(host); err != nil || port == "" {
			return "", errors.New("quality container daemon TCP host must be host:port")
		}
		return value, nil
	}
	return "", errors.New("quality container daemon host must use unix:/// or tcp://")
}

func validateWorkspaceRoot(value, daemonHost string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if strings.HasPrefix(daemonHost, "tcp://") {
			return "", errors.New("remote quality daemon requires an explicit shared workspace root")
		}
		return "", nil
	}
	absolute, err := filepath.Abs(value)
	if err != nil || !filepath.IsAbs(absolute) || filepath.Clean(absolute) != absolute {
		return "", errors.New("quality shared workspace root must be an absolute normalized path")
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() {
		return "", errors.New("quality shared workspace root must already exist")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("quality shared workspace root must not be a symbolic link")
	}
	return absolute, nil
}

func (s *ContainerSandbox) validateMountedDirectory(value, field string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil || strings.TrimSpace(value) == "" {
		return "", Invalid("workspace", field+" path is invalid")
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", Invalid("workspace", field+" directory does not exist or is not a real directory")
	}
	if s.workspaceRoot != "" {
		relative, err := filepath.Rel(s.workspaceRoot, absolute)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
			return "", Invalid("workspace", field+" must be a child of the configured shared workspace root")
		}
	}
	return absolute, nil
}

func validateResolverDirectoryContents(directory, ecosystem string) error {
	allowed := map[string]bool{}
	switch ecosystem {
	case "node":
		allowed["package.json"] = true
		allowed["package-lock.json"] = true
	case "go":
		allowed["go.mod"] = true
		allowed["go.sum"] = true
	default:
		return Invalid("dependencies", "unsupported resolver ecosystem")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return wrapInternal("inspect isolated dependency directory", err)
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return Invalid("dependencies", "network resolver directory may contain only the selected manifest and lock files")
		}
	}
	return nil
}

type limitedBuffer struct {
	mu        sync.Mutex
	buffer    []byte
	limit     int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit, buffer: make([]byte, 0, min(limit, 4096))}
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.buffer)
	if remaining > 0 {
		if remaining > len(value) {
			remaining = len(value)
		}
		b.buffer = append(b.buffer, value[:remaining]...)
	}
	if remaining < len(value) {
		b.truncated = true
	}
	return len(value), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.buffer...))
}

func (b *limitedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}
