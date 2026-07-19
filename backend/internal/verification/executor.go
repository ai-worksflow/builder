package verification

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/storage/content"
)

var (
	ErrCandidateExecution = errors.New("candidate verification container execution failed")

	verificationRuntimeResourcePattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?[a-zA-Z]*$`)
	verificationRuntimeUserPattern     = regexp.MustCompile(`^[0-9]{1,10}:[0-9]{1,10}$`)
	verificationServiceIDPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	verificationDaemonTCPPattern       = regexp.MustCompile(`^tcp://(?:[A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\]):[0-9]{1,5}$`)
	verificationPythonHashPattern      = regexp.MustCompile(`--hash=sha256:[0-9a-fA-F]{64}(?:\s|\\|$)`)
	verificationSecretPatterns         = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)[^\s]+`),
		regexp.MustCompile(`(?i)((?:password|api[_-]?key|access[_-]?token|secret)\s*[=:]\s*)[^\s]+`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`),
	}
)

const verificationCheckLogAggregate = "verification_check_log"

type CandidateContainerCommand interface {
	Run(context.Context, string, []string, []string, io.Writer, io.Writer) error
}

type OSCandidateContainerCommand struct{}

func (OSCandidateContainerCommand) Run(
	ctx context.Context,
	binary string,
	args []string,
	environment []string,
	stdout, stderr io.Writer,
) error {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = environment
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

type DockerCandidateExecutorConfig struct {
	RuntimeBinary string
	DaemonHost    string
	WorkspaceRoot string
	Memory        string
	CPUs          string
	PIDs          int
	OutputLimit   int
	TempBytes     int64
	User          string
}

type DockerCandidateExecutor struct {
	runtimePath  string
	runtimeName  string
	daemonHost   string
	root         string
	memory       string
	cpus         string
	pids         int
	outputLimit  int
	tempBytes    int64
	user         string
	contents     content.Store
	commands     CandidateContainerCommand
	mu           sync.RWMutex
	dependencyMu sync.Mutex
	runtimes     map[string]candidateServiceRuntime
}

type candidatePostgresPolicy struct {
	Image       string
	Database    string
	User        string
	RuntimeUser string
}

type candidateServiceRuntime struct {
	Fence        uint64
	Network      string
	Postgres     string
	Environment  string
	Secret       string
	Dependencies map[string]candidatePreparedDependency
}

type candidatePreparedDependency struct {
	ID               string
	ServiceID        string
	Ecosystem        string
	WorkingDirectory string
	Root             string
}

type candidateRuntimeServicePolicy struct {
	ID               string
	Image            string
	Argv             []string
	WorkingDirectory string
	HealthArgv       []string
}

type candidateDependencyResolverPolicy struct {
	Network string
}

func NewDockerCandidateExecutor(
	config DockerCandidateExecutorConfig,
	contents content.Store,
	commands CandidateContainerCommand,
) (*DockerCandidateExecutor, error) {
	binary := strings.TrimSpace(config.RuntimeBinary)
	if binary == "" {
		binary = "docker"
	}
	runtimePath, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("%w: locate runtime: %v", ErrCandidateExecution, err)
	}
	base := strings.ToLower(filepath.Base(runtimePath))
	if base != "docker" && base != "podman" {
		return nil, fmt.Errorf("%w: runtime must be docker or podman", ErrCandidateExecution)
	}
	daemonHost, err := validateVerificationDaemonHost(config.DaemonHost)
	if err != nil {
		return nil, err
	}
	if base == "podman" && daemonHost == "" {
		return nil, fmt.Errorf(
			"%w: podman verification requires an explicit daemon host",
			ErrCandidateExecution,
		)
	}
	root, err := prepareVerificationWorkspaceRoot(config.WorkspaceRoot)
	if err != nil || contents == nil {
		return nil, fmt.Errorf("%w: private workspace root and content store are required", ErrCandidateExecution)
	}
	config.Memory = strings.TrimSpace(config.Memory)
	config.CPUs = strings.TrimSpace(config.CPUs)
	config.User = strings.TrimSpace(config.User)
	if config.Memory == "" {
		config.Memory = "1g"
	}
	if config.CPUs == "" {
		config.CPUs = "1.0"
	}
	if config.User == "" {
		config.User = "10001:10001"
	}
	if !verificationRuntimeResourcePattern.MatchString(config.Memory) ||
		!verificationRuntimeResourcePattern.MatchString(config.CPUs) ||
		!verificationRuntimeUserPattern.MatchString(config.User) {
		return nil, fmt.Errorf("%w: invalid resource or user boundary", ErrCandidateExecution)
	}
	if config.PIDs <= 0 || config.PIDs > 4096 {
		config.PIDs = 256
	}
	if config.OutputLimit <= 0 || config.OutputLimit > 8<<20 {
		config.OutputLimit = 1 << 20
	}
	if config.TempBytes <= 0 || config.TempBytes > 8<<30 {
		config.TempBytes = 512 << 20
	}
	if commands == nil {
		commands = OSCandidateContainerCommand{}
	}
	return &DockerCandidateExecutor{
		runtimePath: runtimePath, runtimeName: base, daemonHost: daemonHost, root: root,
		memory: config.Memory, cpus: config.CPUs, pids: config.PIDs,
		outputLimit: config.OutputLimit, tempBytes: config.TempBytes,
		user: config.User, contents: contents, commands: commands,
		runtimes: map[string]candidateServiceRuntime{},
	}, nil
}

func (executor *DockerCandidateExecutor) Prepare(
	ctx context.Context,
	spec CandidateExecutionSpec,
) error {
	if err := validateCandidateExecutionSpec(spec); err != nil {
		return err
	}
	policy, enabled, err := parseCandidatePostgresPolicy(spec.Content.RuntimePolicy.NetworkPolicy)
	if err != nil {
		return err
	}
	services, err := parseCandidateRuntimeServices(spec.Content.RuntimePolicy.NetworkPolicy)
	if err != nil {
		return err
	}
	dependencies, err := executor.prepareDependencyCaches(ctx, spec)
	if err != nil {
		return err
	}
	if len(services) > 0 && !enabled {
		return fmt.Errorf("%w: runtime services require the exact ephemeral PostgreSQL network", ErrCandidateExecution)
	}
	if !enabled {
		if len(dependencies) > 0 {
			executor.mu.Lock()
			executor.runtimes[spec.AttemptID] = candidateServiceRuntime{
				Fence: spec.AttemptFenceEpoch, Dependencies: dependencies,
			}
			executor.mu.Unlock()
		}
		return nil
	}
	if err := executor.CleanupVerificationEnvironment(ctx, VerificationEnvironmentCleanup{
		Fence: candidateSpecFence(spec),
	}); err != nil {
		return fmt.Errorf("%w: clean prior exact execution resources: %v", ErrCandidateExecution, err)
	}
	environment := verificationContainerEnvironment(executor.root, executor.runtimeName, executor.daemonHost)
	network := verificationNetworkName(spec.AttemptID)
	if err := executor.commands.Run(
		ctx, executor.runtimePath,
		[]string{
			"network", "create", "--internal", "--driver", "bridge",
			"--label", "worksflow.verification.attempt=" + spec.AttemptID,
			"--label", "worksflow.verification.fence=" + strconv.FormatUint(spec.AttemptFenceEpoch, 10),
			network,
		},
		environment, io.Discard, io.Discard,
	); err != nil {
		return fmt.Errorf("%w: create isolated service network: %v", ErrCandidateExecution, err)
	}
	runtimeRoot := filepath.Join(
		executor.root, spec.AttemptID, strconv.FormatUint(spec.AttemptFenceEpoch, 10), "runtime",
	)
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		return fmt.Errorf("%w: create service runtime directory: %v", ErrCandidateExecution, err)
	}
	secretBytes := make([]byte, 24)
	if _, err := rand.Read(secretBytes); err != nil {
		return fmt.Errorf("%w: generate ephemeral database credential: %v", ErrCandidateExecution, err)
	}
	secret := hex.EncodeToString(secretBytes)
	envFile := filepath.Join(runtimeRoot, "postgres.env")
	envValue := "POSTGRES_USER=" + policy.User + "\nPOSTGRES_PASSWORD=" + secret +
		"\nPOSTGRES_DB=" + policy.Database + "\nPGDATA=/var/lib/postgresql/data/pgdata\n"
	if err := os.WriteFile(envFile, []byte(envValue), 0o600); err != nil {
		return fmt.Errorf("%w: write ephemeral database environment: %v", ErrCandidateExecution, err)
	}
	postgresName := verificationPostgresName(spec.AttemptID)
	runtimeUID, runtimeGID, _ := strings.Cut(policy.RuntimeUser, ":")
	args := []string{
		"run", "-d", "--pull", "always", "--name", postgresName,
		"--label", "worksflow.verification.attempt=" + spec.AttemptID,
		"--label", "worksflow.verification.fence=" + strconv.FormatUint(spec.AttemptFenceEpoch, 10),
		"--network", network, "--network-alias", "postgres", "--read-only",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", strconv.Itoa(executor.pids), "--memory", executor.memory, "--cpus", executor.cpus,
		"--user", policy.RuntimeUser,
		"--tmpfs", "/var/lib/postgresql/data:rw,nosuid,nodev,size=" + strconv.FormatInt(executor.tempBytes, 10) +
			",uid=" + runtimeUID + ",gid=" + runtimeGID + ",mode=0700",
		"--tmpfs", "/var/run/postgresql:rw,nosuid,nodev,size=16m,uid=" + runtimeUID + ",gid=" + runtimeGID + ",mode=0750",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=64m", "--env-file", envFile,
		policy.Image,
	}
	if err := executor.commands.Run(ctx, executor.runtimePath, args, environment, io.Discard, io.Discard); err != nil {
		return fmt.Errorf("%w: start ephemeral PostgreSQL: %v", ErrCandidateExecution, err)
	}
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		err = executor.commands.Run(
			readyCtx, executor.runtimePath,
			[]string{"exec", postgresName, "pg_isready", "-U", policy.User, "-d", policy.Database},
			environment, io.Discard, io.Discard,
		)
		if err == nil {
			break
		}
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("%w: ephemeral PostgreSQL did not become ready: %v", ErrCandidateExecution, readyCtx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	checkEnv := filepath.Join(runtimeRoot, "checks.env")
	databaseURL := "postgresql://" + policy.User + ":" + secret + "@postgres:5432/" + policy.Database + "?sslmode=disable"
	if err := os.WriteFile(checkEnv, []byte("DATABASE_URL="+databaseURL+"\n"), 0o600); err != nil {
		return fmt.Errorf("%w: write check environment: %v", ErrCandidateExecution, err)
	}
	workspace := filepath.Join(
		executor.root, spec.AttemptID, strconv.FormatUint(spec.AttemptFenceEpoch, 10), "workspace",
	)
	for _, service := range services {
		workingDirectory, workdirErr := verificationContainerWorkingDirectory(workspace, service.WorkingDirectory)
		if workdirErr != nil {
			return workdirErr
		}
		serviceName := verificationServiceName(spec.AttemptID, service.ID)
		serviceArgs := []string{
			"run", "-d", "--pull", "always", "--name", serviceName,
			"--label", "worksflow.verification.attempt=" + spec.AttemptID,
			"--label", "worksflow.verification.fence=" + strconv.FormatUint(spec.AttemptFenceEpoch, 10),
			"--network", network, "--network-alias", service.ID,
			"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
			"--pids-limit", strconv.Itoa(executor.pids), "--memory", executor.memory, "--cpus", executor.cpus,
			"--tmpfs", "/tmp:rw,nosuid,nodev,size=256m", "--user", executor.user,
			"--mount", "type=bind,src=" + workspace + ",dst=/workspace,readonly",
			"--workdir", workingDirectory, "--env-file", checkEnv,
			"--env", "HOME=/tmp", "--env", "CI=1",
		}
		serviceArgs = executor.appendDependencyArguments(serviceArgs, dependencies[service.ID])
		serviceArgs = append(serviceArgs, service.Image)
		serviceArgs = append(serviceArgs, service.Argv...)
		if err := executor.commands.Run(
			ctx, executor.runtimePath, serviceArgs, environment, io.Discard, io.Discard,
		); err != nil {
			return fmt.Errorf("%w: start service %s: %v", ErrCandidateExecution, service.ID, err)
		}
		serviceReadyCtx, serviceCancel := context.WithTimeout(ctx, 30*time.Second)
		for {
			healthArgs := append([]string{"exec", serviceName}, service.HealthArgv...)
			err = executor.commands.Run(
				serviceReadyCtx, executor.runtimePath, healthArgs, environment, io.Discard, io.Discard,
			)
			if err == nil {
				break
			}
			select {
			case <-serviceReadyCtx.Done():
				serviceCancel()
				return fmt.Errorf("%w: service %s did not become healthy: %v", ErrCandidateExecution, service.ID, serviceReadyCtx.Err())
			case <-time.After(200 * time.Millisecond):
			}
		}
		serviceCancel()
	}
	executor.mu.Lock()
	executor.runtimes[spec.AttemptID] = candidateServiceRuntime{
		Fence: spec.AttemptFenceEpoch, Network: network, Postgres: postgresName,
		Environment: checkEnv, Secret: secret, Dependencies: dependencies,
	}
	executor.mu.Unlock()
	return nil
}

func (executor *DockerCandidateExecutor) Collect(
	ctx context.Context,
	spec CandidateExecutionSpec,
) error {
	if err := validateCandidateExecutionSpec(spec); err != nil {
		return err
	}
	return executor.CleanupVerificationEnvironment(ctx, VerificationEnvironmentCleanup{
		Fence: candidateSpecFence(spec),
	})
}

// PrepareCanonical and CollectCanonical reuse the same digest-pinned runtime
// mechanics as Candidate verification. The adapter creates no Candidate
// authority: the canonical Worker, Plan, Attempt, Receipt, and workspace
// identity remain separate exact facts.
func (executor *DockerCandidateExecutor) PrepareCanonical(
	ctx context.Context,
	spec CanonicalExecutionSpec,
) error {
	candidate, err := canonicalRuntimeSpec(spec)
	if err != nil {
		return err
	}
	return executor.Prepare(ctx, candidate)
}

func (executor *DockerCandidateExecutor) CollectCanonical(
	ctx context.Context,
	spec CanonicalExecutionSpec,
) error {
	candidate, err := canonicalRuntimeSpec(spec)
	if err != nil {
		return err
	}
	return executor.Collect(ctx, candidate)
}

func (executor *DockerCandidateExecutor) ExecuteCanonical(
	ctx context.Context,
	request CanonicalCheckExecutionRequest,
) (CheckExecutionOutcome, error) {
	if normalized, err := normalizeCanonicalPlanSubject(request.Subject); err != nil || normalized != request.Subject {
		return CheckExecutionOutcome{}, fmt.Errorf("%w: invalid exact canonical subject", ErrCandidateExecution)
	}
	return executor.Execute(ctx, CheckExecutionRequest{
		ProjectID: request.ProjectID, RunID: request.RunID, AttemptID: request.AttemptID,
		AttemptFenceEpoch: request.AttemptFenceEpoch, AttemptCount: request.AttemptCount,
		Subject: CandidatePlanSubject{
			CandidateSnapshotID: request.Subject.WorkspaceRevisionID,
			TreeHash:            request.Subject.WorkspaceContentHash,
		},
		RuntimePolicy: request.RuntimePolicy,
		Dependencies:  request.Dependencies,
		Check:         request.Check,
	})
}

func canonicalRuntimeSpec(spec CanonicalExecutionSpec) (CandidateExecutionSpec, error) {
	if err := validateCanonicalExecutionSpec(spec); err != nil {
		return CandidateExecutionSpec{}, err
	}
	return CandidateExecutionSpec{
		RunID: spec.RunID, AttemptID: spec.AttemptID, AttemptFenceEpoch: spec.AttemptFenceEpoch,
		PlanID: spec.PlanID, PlanHash: spec.PlanHash,
		Content: PlanContent{
			SchemaVersion: PlanContentSchemaVersion, Scope: ScopeCandidate,
			ProjectID: spec.Content.ProjectID,
			Subject: CandidatePlanSubject{
				CandidateSnapshotID: spec.Content.Subject.WorkspaceRevisionID,
				TreeHash:            spec.Content.Subject.WorkspaceContentHash,
			},
			BuildManifest: spec.Content.BuildManifest, BuildContract: spec.Content.BuildContract,
			FullStackTemplate: spec.Content.FullStackTemplate, Profile: spec.Content.Profile,
			TemplateReleases: spec.Content.TemplateReleases,
			Dependencies:     clonePlanDependencies(spec.Content.Dependencies),
			Checks:           append([]PlanCheck(nil), spec.Content.Checks...),
			Obligations:      append([]PlanObligation(nil), spec.Content.Obligations...),
			RuntimePolicy:    cloneRuntimePolicy(spec.Content.RuntimePolicy),
		},
	}, nil
}

func (executor *DockerCandidateExecutor) prepareDependencyCaches(
	ctx context.Context,
	spec CandidateExecutionSpec,
) (map[string]candidatePreparedDependency, error) {
	result := map[string]candidatePreparedDependency{}
	if len(spec.Content.Dependencies) == 0 {
		return result, nil
	}
	resolver, err := parseCandidateDependencyResolverPolicy(spec.Content.RuntimePolicy.NetworkPolicy)
	if err != nil {
		return nil, err
	}
	workspace := filepath.Join(
		executor.root, spec.AttemptID, strconv.FormatUint(spec.AttemptFenceEpoch, 10), "workspace",
	)
	executor.dependencyMu.Lock()
	defer executor.dependencyMu.Unlock()
	for _, dependency := range spec.Content.Dependencies {
		prepared, prepareErr := executor.prepareDependencyCache(ctx, workspace, dependency, resolver)
		if prepareErr != nil {
			return nil, prepareErr
		}
		if _, exists := result[prepared.ServiceID]; exists {
			return nil, fmt.Errorf("%w: duplicate dependency service", ErrCandidateExecution)
		}
		result[prepared.ServiceID] = prepared
	}
	return result, nil
}

func (executor *DockerCandidateExecutor) prepareDependencyCache(
	ctx context.Context,
	workspace string,
	dependency PlanDependency,
	resolver candidateDependencyResolverPolicy,
) (candidatePreparedDependency, error) {
	if !stableIDPattern.MatchString(dependency.ID) || !stableIDPattern.MatchString(dependency.ServiceID) ||
		(dependency.Ecosystem != "node" && dependency.Ecosystem != "python") ||
		!imagePattern.MatchString(dependency.ToolchainImageDigest) || !exactSHA256(dependency.CacheKey) ||
		len(dependency.Lockfiles) != 1 || !equalStrings(expectedDependencyResolverArgv(dependency), dependency.ResolverArgv) {
		return candidatePreparedDependency{}, fmt.Errorf("%w: invalid immutable dependency request", ErrCandidateExecution)
	}
	cacheRoot := filepath.Join(executor.root, ".dependency-cache", strings.TrimPrefix(dependency.CacheKey, "sha256:"))
	if !pathWithinVerificationRoot(executor.root, cacheRoot) {
		return candidatePreparedDependency{}, fmt.Errorf("%w: dependency cache escaped execution root", ErrCandidateExecution)
	}
	if validPreparedDependencyCache(cacheRoot, dependency) {
		return candidatePreparedDependency{
			ID: dependency.ID, ServiceID: dependency.ServiceID, Ecosystem: dependency.Ecosystem,
			WorkingDirectory: dependency.WorkingDirectory, Root: cacheRoot,
		}, nil
	}
	if err := os.RemoveAll(cacheRoot); err != nil {
		return candidatePreparedDependency{}, fmt.Errorf("%w: replace invalid dependency cache: %v", ErrCandidateExecution, err)
	}
	cacheParent := filepath.Dir(cacheRoot)
	if err := os.MkdirAll(cacheParent, 0o700); err != nil {
		return candidatePreparedDependency{}, fmt.Errorf("%w: create dependency cache root: %v", ErrCandidateExecution, err)
	}
	staging, err := os.MkdirTemp(cacheParent, ".staging-"+dependency.ID+"-")
	if err != nil {
		return candidatePreparedDependency{}, fmt.Errorf("%w: create dependency staging root: %v", ErrCandidateExecution, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()
	inputs := append([]string(nil), dependency.ManifestPaths...)
	for _, lockfile := range dependency.Lockfiles {
		inputs = append(inputs, lockfile.Path)
	}
	seenNames := map[string]bool{}
	for _, sourcePath := range inputs {
		normalized, normalizeErr := repository.NormalizePath(sourcePath)
		source := filepath.Join(workspace, filepath.FromSlash(sourcePath))
		info, statErr := os.Lstat(source)
		if normalizeErr != nil || normalized != sourcePath || statErr != nil || !info.Mode().IsRegular() ||
			info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > 16<<20 ||
			!pathWithinVerificationRoot(workspace, source) {
			return candidatePreparedDependency{}, fmt.Errorf("%w: resolver input %s is unavailable", ErrCandidateExecution, sourcePath)
		}
		name := filepath.Base(sourcePath)
		if seenNames[name] {
			return candidatePreparedDependency{}, fmt.Errorf("%w: resolver input basenames are ambiguous", ErrCandidateExecution)
		}
		value, readErr := os.ReadFile(source)
		if readErr != nil {
			return candidatePreparedDependency{}, fmt.Errorf("%w: read resolver input %s: %v", ErrCandidateExecution, sourcePath, readErr)
		}
		for _, lockfile := range dependency.Lockfiles {
			if lockfile.Path == sourcePath && hashVerificationBytes(value) != lockfile.Digest {
				return candidatePreparedDependency{}, fmt.Errorf("%w: dependency lockfile digest drifted", ErrCandidateExecution)
			}
		}
		if dependency.Ecosystem == "node" && name == "package-lock.json" &&
			!validLockedNodeDependencies(value, dependency.Lockfiles[0].Registry) {
			return candidatePreparedDependency{}, fmt.Errorf("%w: Node dependency lock is not registry and integrity pinned", ErrCandidateExecution)
		}
		if dependency.Ecosystem == "python" && name == filepath.Base(dependency.Lockfiles[0].Path) &&
			!validHashLockedPythonRequirements(value) {
			return candidatePreparedDependency{}, fmt.Errorf("%w: Python dependency lock is not hash-locked", ErrCandidateExecution)
		}
		if err := os.WriteFile(filepath.Join(staging, name), value, 0o600); err != nil {
			return candidatePreparedDependency{}, fmt.Errorf("%w: stage resolver input: %v", ErrCandidateExecution, err)
		}
		seenNames[name] = true
	}
	args := []string{
		"run", "--rm", "--pull", "always", "--network", resolver.Network,
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", strconv.Itoa(executor.pids), "--memory", executor.memory, "--cpus", executor.cpus,
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=128m", "--user", executor.user,
		"--mount", "type=bind,src=" + staging + ",dst=/resolver",
		"--workdir", "/resolver", "--env", "HOME=/tmp", "--env", "CI=1",
	}
	if dependency.Ecosystem == "node" {
		args = append(args,
			"--env", "npm_config_ignore_scripts=true", "--env", "npm_config_audit=false",
			"--env", "npm_config_fund=false", "--env", "npm_config_update_notifier=false",
		)
	} else {
		args = append(args, "--env", "PIP_DISABLE_PIP_VERSION_CHECK=1", "--env", "PIP_NO_INPUT=1")
	}
	args = append(args, dependency.ToolchainImageDigest)
	args = append(args, dependency.ResolverArgv...)
	output := newBoundedVerificationOutput(executor.outputLimit)
	environment := verificationContainerEnvironment(executor.root, executor.runtimeName, executor.daemonHost)
	if err := executor.commands.Run(ctx, executor.runtimePath, args, environment, output, output); err != nil {
		redacted, _ := redactVerificationOutput(output.Bytes())
		return candidatePreparedDependency{}, fmt.Errorf(
			"%w: dependency resolver %s failed: %s", ErrCandidateExecution, dependency.ID,
			boundedResolverError(redacted),
		)
	}
	if err := validateDependencyOutput(staging, dependency); err != nil {
		return candidatePreparedDependency{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, ".worksflow-cache-identity"), []byte(dependency.CacheKey+"\n"), 0o400); err != nil {
		return candidatePreparedDependency{}, fmt.Errorf("%w: write dependency cache identity: %v", ErrCandidateExecution, err)
	}
	if err := sealDependencyCache(staging); err != nil {
		return candidatePreparedDependency{}, err
	}
	if err := os.Rename(staging, cacheRoot); err != nil {
		return candidatePreparedDependency{}, fmt.Errorf("%w: commit dependency cache: %v", ErrCandidateExecution, err)
	}
	committed = true
	return candidatePreparedDependency{
		ID: dependency.ID, ServiceID: dependency.ServiceID, Ecosystem: dependency.Ecosystem,
		WorkingDirectory: dependency.WorkingDirectory, Root: cacheRoot,
	}, nil
}

func validateDependencyOutput(root string, dependency PlanDependency) error {
	target := filepath.Join(root, "node_modules")
	if dependency.Ecosystem == "python" {
		target = filepath.Join(root, "site-packages")
	}
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: dependency resolver did not produce the required cache", ErrCandidateExecution)
	}
	return nil
}

func validPreparedDependencyCache(root string, dependency PlanDependency) bool {
	identity, err := os.ReadFile(filepath.Join(root, ".worksflow-cache-identity"))
	if err != nil || string(identity) != dependency.CacheKey+"\n" {
		return false
	}
	return validateDependencyOutput(root, dependency) == nil
}

func sealDependencyCache(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(value string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeDevice|os.ModeNamedPipe|os.ModeSocket|os.ModeSetuid|os.ModeSetgid) != 0 {
			return fmt.Errorf("%w: unsafe dependency cache entry", ErrCandidateExecution)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(value)
			if err != nil || !pathWithinVerificationRoot(root, target) {
				return fmt.Errorf("%w: dependency cache symlink escaped", ErrCandidateExecution)
			}
			return nil
		}
		if entry.IsDir() {
			directories = append(directories, value)
			return nil
		}
		if !info.Mode().IsRegular() || os.Chmod(value, 0o400) != nil {
			return fmt.Errorf("%w: seal dependency cache file", ErrCandidateExecution)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := os.Chmod(directories[index], 0o500); err != nil {
			return fmt.Errorf("%w: seal dependency cache directory: %v", ErrCandidateExecution, err)
		}
	}
	return nil
}

func hashVerificationBytes(value []byte) string {
	hash := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func validHashLockedPythonRequirements(value []byte) bool {
	text := string(value)
	if text == "" || strings.ContainsRune(text, '\x00') ||
		strings.Contains(text, "--index-url") || strings.Contains(text, "--extra-index-url") ||
		strings.Contains(text, "-e ") || strings.Contains(text, "git+") || strings.Contains(text, "file:") {
		return false
	}
	seenRequirement, currentRequirement, currentHash := false, false, false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || line == "\\" {
			continue
		}
		if strings.HasPrefix(line, "--hash=sha256:") {
			if !currentRequirement || !verificationPythonHashPattern.MatchString(line) {
				return false
			}
			currentHash = true
			continue
		}
		if strings.HasPrefix(line, "-") || strings.Contains(line, "://") {
			return false
		}
		if currentRequirement && !currentHash {
			return false
		}
		currentRequirement, currentHash, seenRequirement = true, verificationPythonHashPattern.MatchString(line), true
	}
	return seenRequirement && currentRequirement && currentHash
}

func validLockedNodeDependencies(value []byte, registry string) bool {
	registryURL, err := url.Parse(registry)
	if err != nil || registryURL.Host == "" {
		return false
	}
	var lock struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			Resolved  string `json:"resolved"`
			Integrity string `json:"integrity"`
			Link      bool   `json:"link"`
		} `json:"packages"`
	}
	if json.Unmarshal(value, &lock) != nil || lock.LockfileVersion < 2 || len(lock.Packages) == 0 {
		return false
	}
	for packagePath, item := range lock.Packages {
		if packagePath == "" {
			continue
		}
		resolved, err := url.Parse(strings.TrimSpace(item.Resolved))
		if item.Link || !strings.HasPrefix(packagePath, "node_modules/") || err != nil ||
			resolved.Scheme != "https" || !strings.EqualFold(resolved.Host, registryURL.Host) ||
			resolved.User != nil || resolved.Fragment != "" || !validNodeIntegrity(item.Integrity) {
			return false
		}
	}
	return true
}

func validNodeIntegrity(value string) bool {
	expected := map[string]int{"sha256": 32, "sha384": 48, "sha512": 64}
	tokens := strings.Fields(strings.TrimSpace(value))
	if len(tokens) == 0 {
		return false
	}
	for _, token := range tokens {
		parts := strings.SplitN(token, "-", 2)
		if len(parts) != 2 || expected[parts[0]] == 0 {
			return false
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(parts[1])
		if err != nil || len(decoded) != expected[parts[0]] {
			return false
		}
	}
	return true
}

func boundedResolverError(value []byte) string {
	result := strings.TrimSpace(string(value))
	if len(result) > 512 {
		result = result[:512]
	}
	if result == "" {
		return "container exited without bounded output"
	}
	return result
}

func (executor *DockerCandidateExecutor) Execute(
	ctx context.Context,
	request CheckExecutionRequest,
) (CheckExecutionOutcome, error) {
	if executor == nil || ctx == nil || !validUUIDs(request.ProjectID, request.RunID, request.AttemptID) ||
		request.AttemptFenceEpoch == 0 || request.AttemptCount == 0 ||
		!exactSHA256(request.Subject.TreeHash) ||
		!imagePattern.MatchString(request.Check.VerifierImageDigest) || len(request.Check.Argv) == 0 {
		return CheckExecutionOutcome{}, fmt.Errorf("%w: invalid immutable check request", ErrCandidateExecution)
	}
	var outcome CheckExecutionOutcome
	lockErr := withVerificationAttemptLock(
		ctx, executor.root, request.AttemptID,
		func(string) error {
			var executeErr error
			outcome, executeErr = executor.executeCheckLocked(ctx, request)
			return executeErr
		},
	)
	if lockErr != nil {
		return outcome, errors.Join(ErrCandidateExecution, lockErr)
	}
	return outcome, nil
}

// executeCheckLocked runs while holding the same cross-process Attempt lock
// used by materialization and cleanup. Cleanup therefore cannot be confirmed
// while the old fenced runtime client command is still active. Qualification
// of delayed mutations inside a remote daemon remains a deployment concern.
func (executor *DockerCandidateExecutor) executeCheckLocked(
	ctx context.Context,
	request CheckExecutionRequest,
) (CheckExecutionOutcome, error) {
	for _, argument := range request.Check.Argv {
		if argument == "" || strings.ContainsRune(argument, '\x00') || len(argument) > 16<<10 {
			return CheckExecutionOutcome{}, fmt.Errorf("%w: invalid argv", ErrCandidateExecution)
		}
	}
	workspace := filepath.Join(
		executor.root, request.AttemptID, strconv.FormatUint(request.AttemptFenceEpoch, 10), "workspace",
	)
	if !pathWithinVerificationRoot(executor.root, workspace) {
		return CheckExecutionOutcome{}, fmt.Errorf("%w: workspace escaped execution root", ErrCandidateExecution)
	}
	workspaceInfo, err := os.Lstat(workspace)
	if err != nil || !workspaceInfo.IsDir() || workspaceInfo.Mode()&os.ModeSymlink != 0 {
		return CheckExecutionOutcome{}, fmt.Errorf("%w: exact materialized workspace is unavailable", ErrCandidateExecution)
	}
	containerWorkdir, err := verificationContainerWorkingDirectory(workspace, request.Check.WorkingDirectory)
	if err != nil {
		return CheckExecutionOutcome{}, err
	}
	name := verificationContainerName(request.AttemptID, request.AttemptFenceEpoch, request.Check.ID)
	policy, servicesEnabled, err := parseCandidatePostgresPolicy(request.RuntimePolicy.NetworkPolicy)
	if err != nil {
		return CheckExecutionOutcome{}, err
	}
	_ = policy
	runtimeState := candidateServiceRuntime{}
	if servicesEnabled || len(request.Dependencies) > 0 {
		executor.mu.RLock()
		runtimeState = executor.runtimes[request.AttemptID]
		executor.mu.RUnlock()
		if runtimeState.Fence != request.AttemptFenceEpoch ||
			(servicesEnabled && (runtimeState.Network == "" || runtimeState.Environment == "")) ||
			(len(request.Dependencies) > 0 && len(runtimeState.Dependencies) == 0) {
			return CheckExecutionOutcome{}, fmt.Errorf("%w: exact ephemeral service environment is unavailable", ErrCandidateExecution)
		}
	}
	configDirectory, err := os.MkdirTemp("", "worksflow-verification-runtime-")
	if err != nil {
		return CheckExecutionOutcome{}, fmt.Errorf("%w: create runtime client directory: %v", ErrCandidateExecution, err)
	}
	defer os.RemoveAll(configDirectory)
	args := executor.containerArguments(
		name, workspace, containerWorkdir, request.AttemptID, request.AttemptFenceEpoch,
		request.Check, runtimeState,
	)
	environment := verificationContainerEnvironment(configDirectory, executor.runtimeName, executor.daemonHost)
	stdout, stderr := newBoundedVerificationOutput(executor.outputLimit), newBoundedVerificationOutput(executor.outputLimit)
	defer executor.removeContainer(name, environment)
	err = executor.commands.Run(ctx, executor.runtimePath, args, environment, stdout, stderr)
	stdoutValue, stdoutRedactions := redactVerificationOutput(stdout.Bytes(), runtimeState.Secret)
	stderrValue, stderrRedactions := redactVerificationOutput(stderr.Bytes(), runtimeState.Secret)
	stdoutCtx, cancelStdout := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	stdoutRef, stdoutErr := executor.persistLog(stdoutCtx, request, "stdout", stdoutValue)
	cancelStdout()
	stderrCtx, cancelStderr := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	stderrRef, stderrErr := executor.persistLog(stderrCtx, request, "stderr", stderrValue)
	cancelStderr()
	if stdoutErr != nil || stderrErr != nil {
		return CheckExecutionOutcome{}, errors.Join(ErrCandidateExecution, stdoutErr, stderrErr)
	}
	outcome := CheckExecutionOutcome{
		Stdout: stdoutRef, Stderr: stderrRef,
		Truncated:      stdout.Truncated() || stderr.Truncated(),
		RedactionCount: stdoutRedactions + stderrRedactions,
		Diagnostics:    []Diagnostic{},
	}
	if outcome.Truncated {
		outcome = failClosedTruncatedOutcome(outcome)
	}
	if outcome.RedactionCount > 0 {
		outcome.Diagnostics = append(outcome.Diagnostics, Diagnostic{
			ID: "verification-secret-redacted", Code: "secret_detected", Severity: SeverityBlocker,
			Message: "Potential secret material was detected and redacted from command output.",
		})
	}
	if ctx.Err() != nil {
		return CheckExecutionOutcome{}, ctx.Err()
	}
	if outcome.Truncated {
		return outcome, nil
	}
	if err == nil {
		code := 0
		outcome.Status, outcome.ExitCode = CheckPassed, &code
		return outcome, nil
	}
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		outcome.Status, outcome.ExitCode = CheckFailed, &code
		return outcome, nil
	}
	return CheckExecutionOutcome{}, errors.Join(
		ErrCandidateExecution,
		fmt.Errorf("start or monitor container: %w", err),
	)
}

func (executor *DockerCandidateExecutor) containerArguments(
	name, workspace, workingDirectory, attemptID string,
	fence uint64,
	check PlanCheck,
	runtimeState candidateServiceRuntime,
) []string {
	network := "none"
	if runtimeState.Network != "" {
		network = runtimeState.Network
	}
	args := []string{
		"run", "--rm", "--pull", "always", "--name", name,
		"--label", "worksflow.verification.attempt=" + attemptID,
		"--label", "worksflow.verification.fence=" + strconv.FormatUint(fence, 10),
		"--network", network, "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--pids-limit", strconv.Itoa(executor.pids),
		"--memory", executor.memory, "--cpus", executor.cpus,
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=64m",
		"--tmpfs", "/work-output:rw,noexec,nosuid,size=" + strconv.FormatInt(executor.tempBytes, 10),
		"--user", executor.user,
		"--mount", "type=bind,src=" + workspace + ",dst=/workspace,readonly",
		"--workdir", workingDirectory,
		"--env", "HOME=/tmp", "--env", "CI=1", "--env", "NO_UPDATE_NOTIFIER=1",
	}
	if runtimeState.Environment != "" {
		args = append(args, "--env-file", runtimeState.Environment)
	}
	args = executor.appendDependencyArguments(args, runtimeState.Dependencies[check.ServiceID])
	args = append(args, check.VerifierImageDigest)
	return append(args, check.Argv...)
}

func (executor *DockerCandidateExecutor) appendDependencyArguments(
	args []string,
	dependency candidatePreparedDependency,
) []string {
	if dependency.Root == "" {
		return args
	}
	switch dependency.Ecosystem {
	case "node":
		target := "/workspace/" + dependency.WorkingDirectory + "/node_modules"
		if dependency.WorkingDirectory == "." {
			target = "/workspace/node_modules"
		}
		return append(args,
			"--mount", "type=bind,src="+filepath.Join(dependency.Root, "node_modules")+",dst="+target+",readonly",
			"--env", "npm_config_offline=true", "--env", "npm_config_ignore_scripts=true",
		)
	case "python":
		target := "/dependencies/" + dependency.ID + "/site-packages"
		return append(args,
			"--mount", "type=bind,src="+filepath.Join(dependency.Root, "site-packages")+",dst="+target+",readonly",
			"--env", "PYTHONPATH="+target, "--env", "PIP_NO_INDEX=1", "--env", "PIP_DISABLE_PIP_VERSION_CHECK=1",
		)
	default:
		return args
	}
}

func (executor *DockerCandidateExecutor) persistLog(
	ctx context.Context,
	request CheckExecutionRequest,
	stream string,
	value []byte,
) (*BlobReference, error) {
	payload, err := json.Marshal(map[string]any{
		"schemaVersion": "verification-check-log/v1", "stream": stream,
		"checkId": request.Check.ID, "value": string(value),
	})
	if err != nil {
		return nil, err
	}
	reference, err := executor.contents.PutPending(
		ctx, request.ProjectID, verificationCheckLogAggregate, request.AttemptID, 1, payload,
	)
	if err != nil {
		return nil, fmt.Errorf("persist %s log: %w", stream, err)
	}
	if err := executor.contents.Finalize(ctx, reference.ID); err != nil {
		return nil, fmt.Errorf("finalize %s log: %w", stream, err)
	}
	return &BlobReference{
		Store: "content", OwnerID: request.AttemptID, Ref: reference.ID,
		ContentHash: reference.ContentHash, ByteSize: reference.ByteSize,
	}, nil
}

func (executor *DockerCandidateExecutor) removeContainer(name string, environment []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = executor.commands.Run(
		ctx, executor.runtimePath, []string{"rm", "-f", name}, environment, io.Discard, io.Discard,
	)
}

func (executor *DockerCandidateExecutor) removeAttemptContainers(attemptID string, environment []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := executor.commands.Run(
		ctx, executor.runtimePath,
		[]string{"ps", "-aq", "--filter", "label=worksflow.verification.attempt=" + attemptID},
		environment, &output, io.Discard,
	); err != nil {
		return
	}
	identities := strings.Fields(output.String())
	if len(identities) == 0 {
		return
	}
	_ = executor.commands.Run(
		ctx, executor.runtimePath, append([]string{"rm", "-f"}, identities...),
		environment, io.Discard, io.Discard,
	)
}

func verificationContainerWorkingDirectory(workspace, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "." {
		return "/workspace", nil
	}
	normalized, err := repository.NormalizePath(value)
	if err != nil || normalized != value {
		return "", fmt.Errorf("%w: invalid Plan working directory", ErrCandidateExecution)
	}
	hostPath := filepath.Join(workspace, filepath.FromSlash(value))
	info, err := os.Lstat(hostPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		!pathWithinVerificationRoot(workspace, hostPath) {
		return "", fmt.Errorf("%w: Plan working directory is unavailable", ErrCandidateExecution)
	}
	return "/workspace/" + value, nil
}

func verificationContainerName(attemptID string, fence uint64, checkID string) string {
	cleanCheck := regexp.MustCompile(`[^a-zA-Z0-9_.-]+`).ReplaceAllString(checkID, "-")
	cleanCheck = strings.Trim(cleanCheck, "-.")
	if len(cleanCheck) > 32 {
		cleanCheck = cleanCheck[:32]
	}
	if cleanCheck == "" {
		cleanCheck = "check"
	}
	return "worksflow-verify-" + strings.ReplaceAll(attemptID, "-", "")[:20] + "-" +
		strconv.FormatUint(fence, 10) + "-" + cleanCheck
}

func validateVerificationDaemonHost(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("%w: invalid daemon host", ErrCandidateExecution)
	}
	if strings.HasPrefix(value, "unix:///") {
		path := strings.TrimPrefix(value, "unix://")
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return "", fmt.Errorf("%w: invalid daemon unix socket", ErrCandidateExecution)
		}
		return value, nil
	}
	if verificationDaemonTCPPattern.MatchString(value) {
		return value, nil
	}
	return "", fmt.Errorf("%w: daemon host must use unix:/// or bounded tcp://", ErrCandidateExecution)
}

func verificationContainerEnvironment(configDirectory, runtimeName, daemonHost string) []string {
	path := "/usr/local/bin:/usr/bin:/bin"
	if runtime.GOOS == "windows" {
		path = os.Getenv("PATH")
	}
	result := []string{"PATH=" + path, "HOME=" + configDirectory, "DOCKER_CONFIG=" + configDirectory}
	if daemonHost != "" {
		if runtimeName == "podman" {
			result = append(result, "CONTAINER_HOST="+daemonHost)
		} else {
			result = append(result, "DOCKER_HOST="+daemonHost)
		}
	}
	return result
}

type boundedVerificationOutput struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func newBoundedVerificationOutput(limit int) *boundedVerificationOutput {
	return &boundedVerificationOutput{limit: limit}
}

func (output *boundedVerificationOutput) Write(value []byte) (int, error) {
	original := len(value)
	remaining := output.limit - output.buffer.Len()
	if remaining <= 0 {
		output.truncated = output.truncated || original > 0
		return original, nil
	}
	if len(value) > remaining {
		_, _ = output.buffer.Write(value[:remaining])
		output.truncated = true
		return original, nil
	}
	_, _ = output.buffer.Write(value)
	return original, nil
}

func (output *boundedVerificationOutput) Bytes() []byte   { return output.buffer.Bytes() }
func (output *boundedVerificationOutput) Truncated() bool { return output.truncated }

func redactVerificationOutput(value []byte, exactSecrets ...string) ([]byte, int) {
	result := string(value)
	count := 0
	for _, secret := range exactSecrets {
		if secret != "" {
			matches := strings.Count(result, secret)
			if matches > 0 {
				result = strings.ReplaceAll(result, secret, "[REDACTED]")
				count += matches
			}
		}
	}
	for index, pattern := range verificationSecretPatterns {
		result = pattern.ReplaceAllStringFunc(result, func(match string) string {
			count++
			if index < 2 {
				parts := pattern.FindStringSubmatch(match)
				if len(parts) > 1 {
					return parts[1] + "[REDACTED]"
				}
			}
			return "[REDACTED]"
		})
	}
	return []byte(result), count
}

func parseCandidatePostgresPolicy(policy map[string]any) (candidatePostgresPolicy, bool, error) {
	if policy == nil {
		return candidatePostgresPolicy{}, false, fmt.Errorf("%w: runtime network policy is missing", ErrCandidateExecution)
	}
	raw, exists := policy["postgres"]
	if !exists {
		return candidatePostgresPolicy{}, false, nil
	}
	object, ok := raw.(map[string]any)
	if !ok || len(object) != 4 {
		return candidatePostgresPolicy{}, false, fmt.Errorf("%w: PostgreSQL service policy is invalid", ErrCandidateExecution)
	}
	image, imageOK := object["image"].(string)
	database, databaseOK := object["database"].(string)
	user, userOK := object["user"].(string)
	runtimeUser, runtimeUserOK := object["runtimeUser"].(string)
	image, database, user, runtimeUser = strings.TrimSpace(image), strings.TrimSpace(database), strings.TrimSpace(user), strings.TrimSpace(runtimeUser)
	if !imageOK || !databaseOK || !userOK || !runtimeUserOK || !imagePattern.MatchString(image) ||
		!stableIDPattern.MatchString(database) || !stableIDPattern.MatchString(user) ||
		strings.ContainsAny(database+user, "/:@") || !verificationRuntimeUserPattern.MatchString(runtimeUser) {
		return candidatePostgresPolicy{}, false, fmt.Errorf("%w: PostgreSQL service policy must be digest-pinned and canonical", ErrCandidateExecution)
	}
	return candidatePostgresPolicy{Image: image, Database: database, User: user, RuntimeUser: runtimeUser}, true, nil
}

func parseCandidateDependencyResolverPolicy(policy map[string]any) (candidateDependencyResolverPolicy, error) {
	if policy == nil {
		return candidateDependencyResolverPolicy{}, fmt.Errorf("%w: runtime network policy is missing", ErrCandidateExecution)
	}
	raw, exists := policy["dependencyResolver"]
	if !exists {
		return candidateDependencyResolverPolicy{}, fmt.Errorf("%w: dependency resolver network policy is missing", ErrCandidateExecution)
	}
	object, ok := raw.(map[string]any)
	if !ok || len(object) != 1 {
		return candidateDependencyResolverPolicy{}, fmt.Errorf("%w: dependency resolver network policy is invalid", ErrCandidateExecution)
	}
	network, ok := object["network"].(string)
	network = strings.TrimSpace(network)
	if !ok || !verificationServiceIDPattern.MatchString(network) || network == "host" || network == "none" ||
		strings.HasPrefix(network, "container") {
		return candidateDependencyResolverPolicy{}, fmt.Errorf("%w: dependency resolver requires an explicit bounded egress network", ErrCandidateExecution)
	}
	return candidateDependencyResolverPolicy{Network: network}, nil
}

func parseCandidateRuntimeServices(policy map[string]any) ([]candidateRuntimeServicePolicy, error) {
	raw, exists := policy["services"]
	if !exists {
		return []candidateRuntimeServicePolicy{}, nil
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 || len(items) > 8 {
		return nil, fmt.Errorf("%w: runtime services policy is invalid", ErrCandidateExecution)
	}
	result := make([]candidateRuntimeServicePolicy, 0, len(items))
	seen := map[string]bool{}
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || len(item) != 5 {
			return nil, fmt.Errorf("%w: runtime service %d is invalid", ErrCandidateExecution, index)
		}
		id, idOK := item["id"].(string)
		image, imageOK := item["image"].(string)
		workingDirectory, workdirOK := item["workingDirectory"].(string)
		argv, argvOK := candidateRuntimeStringList(item["argv"], 1, 64)
		healthArgv, healthOK := candidateRuntimeStringList(item["healthArgv"], 1, 16)
		id, image, workingDirectory = strings.TrimSpace(id), strings.TrimSpace(image), strings.TrimSpace(workingDirectory)
		if !idOK || !imageOK || !workdirOK || !argvOK || !healthOK || seen[id] ||
			!verificationServiceIDPattern.MatchString(id) || !imagePattern.MatchString(image) ||
			(workingDirectory != "." && !validRelativePath(workingDirectory)) {
			return nil, fmt.Errorf("%w: runtime service %d must be exact and digest-pinned", ErrCandidateExecution, index)
		}
		seen[id] = true
		result = append(result, candidateRuntimeServicePolicy{
			ID: id, Image: image, Argv: argv, WorkingDirectory: workingDirectory, HealthArgv: healthArgv,
		})
	}
	return result, nil
}

func candidateRuntimeStringList(raw any, minimum, maximum int) ([]string, bool) {
	var values []string
	switch typed := raw.(type) {
	case []string:
		values = append([]string(nil), typed...)
	case []any:
		values = make([]string, len(typed))
		for index := range typed {
			value, ok := typed[index].(string)
			if !ok {
				return nil, false
			}
			values[index] = value
		}
	default:
		return nil, false
	}
	if len(values) < minimum || len(values) > maximum {
		return nil, false
	}
	for index := range values {
		if values[index] == "" || len(values[index]) > 4096 || strings.ContainsRune(values[index], '\x00') {
			return nil, false
		}
	}
	return values, true
}

func verificationNetworkName(attemptID string) string {
	return "worksflow-vnet-" + strings.ReplaceAll(attemptID, "-", "")[:24]
}

func verificationPostgresName(attemptID string) string {
	return "worksflow-vpg-" + strings.ReplaceAll(attemptID, "-", "")[:24]
}

func verificationServiceName(attemptID, serviceID string) string {
	return "worksflow-vsvc-" + strings.ReplaceAll(attemptID, "-", "")[:16] + "-" + serviceID
}
