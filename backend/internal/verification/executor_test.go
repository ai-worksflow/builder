package verification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/storage/content"
)

type candidateContainerCommandFake struct {
	mu        sync.Mutex
	calls     [][]string
	stdout    string
	stdoutFor func([]string) string
	stderr    string
	err       error
	onRun     func([]string) error
}

func (command *candidateContainerCommandFake) Run(
	_ context.Context,
	_ string,
	args []string,
	_ []string,
	stdout, stderr io.Writer,
) error {
	command.mu.Lock()
	command.calls = append(command.calls, append([]string(nil), args...))
	command.mu.Unlock()
	if len(args) > 0 && args[0] == "rm" {
		return nil
	}
	if command.onRun != nil {
		if err := command.onRun(args); err != nil {
			return err
		}
	}
	stdoutValue := command.stdout
	if command.stdoutFor != nil {
		stdoutValue = command.stdoutFor(args)
	}
	_, _ = io.WriteString(stdout, stdoutValue)
	_, _ = io.WriteString(stderr, command.stderr)
	return command.err
}

type candidateExitErrorFake struct{ code int }

func (err candidateExitErrorFake) Error() string { return "exit" }
func (err candidateExitErrorFake) ExitCode() int { return err.code }

type verificationContentStoreFake struct {
	mu        sync.Mutex
	values    map[string]content.StoredContent
	finalized map[string]bool
}

func newVerificationContentStoreFake() *verificationContentStoreFake {
	return &verificationContentStoreFake{values: map[string]content.StoredContent{}, finalized: map[string]bool{}}
}

func (store *verificationContentStoreFake) PutPending(
	ctx context.Context,
	projectID, aggregateType, aggregateID string,
	schemaVersion int,
	payload json.RawMessage,
) (content.Reference, error) {
	if err := ctx.Err(); err != nil {
		return content.Reference{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	id := uuid.NewString()
	hash := verificationBytesHash(payload)
	reference := content.Reference{ID: id, ContentHash: hash, ByteSize: int64(len(payload)), SchemaVersion: schemaVersion}
	store.values[id] = content.StoredContent{
		Reference: reference, ProjectID: projectID, AggregateType: aggregateType,
		AggregateID: aggregateID, State: content.StatePending, Payload: append(json.RawMessage(nil), payload...),
		CreatedAt: time.Now().UTC(),
	}
	return reference, nil
}

func (store *verificationContentStoreFake) Finalize(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	value, found := store.values[id]
	if !found {
		return content.ErrContentNotFound
	}
	value.State = content.StateFinalized
	store.values[id] = value
	store.finalized[id] = true
	return nil
}

func (*verificationContentStoreFake) Abort(context.Context, string) error { return nil }

func (store *verificationContentStoreFake) Get(
	_ context.Context,
	id, expectedHash string,
) (content.StoredContent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, found := store.values[id]
	if !found {
		return content.StoredContent{}, content.ErrContentNotFound
	}
	if value.ContentHash != expectedHash {
		return content.StoredContent{}, content.ErrHashMismatch
	}
	return value, nil
}

func TestDockerCandidateExecutorUsesOnlyPlanCommandAndPersistsRedactedLogs(t *testing.T) {
	executor, command, contents, request := candidateExecutorFixture(t)
	command.stdout = "ready\n"
	command.stderr = "Authorization: Bearer secret-value\n"
	outcome, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != CheckPassed || outcome.ExitCode == nil || *outcome.ExitCode != 0 ||
		outcome.RedactionCount != 1 || outcome.Stdout == nil || outcome.Stderr == nil ||
		len(outcome.Diagnostics) != 1 || outcome.Diagnostics[0].Severity != SeverityBlocker {
		t.Fatalf("executor outcome = %#v", outcome)
	}
	command.mu.Lock()
	args := append([]string(nil), command.calls[0]...)
	command.mu.Unlock()
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "--network\x00none") ||
		!strings.Contains(joined, "--read-only") ||
		!strings.Contains(joined, "--cap-drop\x00ALL") ||
		!strings.Contains(joined, request.Check.VerifierImageDigest+"\x00"+strings.Join(request.Check.Argv, "\x00")) {
		t.Fatalf("container args lost security or exact Plan command: %#v", args)
	}
	stored, err := contents.Get(context.Background(), outcome.Stderr.Ref, outcome.Stderr.ContentHash)
	if err != nil || strings.Contains(string(stored.Payload), "secret-value") ||
		!strings.Contains(string(stored.Payload), "[REDACTED]") || !contents.finalized[outcome.Stderr.Ref] {
		t.Fatalf("stderr log was not finalized/redacted: %s, %v", stored.Payload, err)
	}
}

func TestDockerCandidateExecutorMapsCommandExitToFailedCheck(t *testing.T) {
	executor, command, _, request := candidateExecutorFixture(t)
	command.err = candidateExitErrorFake{code: 17}
	outcome, err := executor.Execute(context.Background(), request)
	if err != nil || outcome.Status != CheckFailed || outcome.ExitCode == nil || *outcome.ExitCode != 17 {
		t.Fatalf("failed command outcome = %#v, %v", outcome, err)
	}
	request.Check.VerifierImageDigest = "python:latest"
	if _, err := executor.Execute(context.Background(), request); !errors.Is(err, ErrCandidateExecution) {
		t.Fatalf("mutable verifier image = %v", err)
	}
}

func TestDockerCandidateExecutorStaleFenceCleanupPreservesNewerRuntimeAndSharedNetwork(t *testing.T) {
	executor, command, _, request := candidateExecutorFixture(t)
	containerID := "0123456789abcdef0123456789abcdef"
	networkID := "abcdef012345abcdef012345abcdef01"
	command.stdoutFor = func(args []string) string {
		switch {
		case len(args) > 0 && args[0] == "ps":
			return containerID + "\n"
		case containsArgument(args, "label=worksflow.verification.fence=10"):
			return networkID + "\t" + verificationNetworkName(request.AttemptID) + "\n"
		default:
			return ""
		}
	}
	executor.runtimes[request.AttemptID] = candidateServiceRuntime{
		Fence: 10, Network: verificationNetworkName(request.AttemptID), Secret: "newer-secret",
	}
	stale := VerificationExecutionFence{
		ProjectID: request.ProjectID, RunID: request.RunID, AttemptID: request.AttemptID,
		AttemptFenceEpoch: 9,
	}
	if err := executor.CleanupVerificationEnvironment(context.Background(), VerificationEnvironmentCleanup{
		Fence: stale, OwnsSharedRuntime: false,
	}); err != nil {
		t.Fatal(err)
	}
	executor.mu.RLock()
	runtimeState, found := executor.runtimes[request.AttemptID]
	executor.mu.RUnlock()
	if !found || runtimeState.Fence != 10 || runtimeState.Secret != "newer-secret" {
		t.Fatalf("stale cleanup removed newer in-memory runtime: %#v", runtimeState)
	}
	command.mu.Lock()
	staleCalls := append([][]string(nil), command.calls...)
	command.mu.Unlock()
	joined := make([]string, len(staleCalls))
	for index := range staleCalls {
		joined[index] = strings.Join(staleCalls[index], "\x00")
	}
	all := strings.Join(joined, "\n")
	if !strings.Contains(all, "label=worksflow.verification.attempt="+request.AttemptID) ||
		!strings.Contains(all, "label=worksflow.verification.fence=9") ||
		strings.Contains(all, "network\x00rm\x00"+verificationNetworkName(request.AttemptID)) {
		t.Fatalf("stale exact-fence cleanup calls = %#v", staleCalls)
	}

	command.mu.Lock()
	command.calls = nil
	command.mu.Unlock()
	current := stale
	current.AttemptFenceEpoch = 10
	if err := executor.CleanupVerificationEnvironment(context.Background(), VerificationEnvironmentCleanup{
		Fence: current, OwnsSharedRuntime: true,
	}); err != nil {
		t.Fatal(err)
	}
	executor.mu.RLock()
	_, found = executor.runtimes[request.AttemptID]
	executor.mu.RUnlock()
	if found {
		t.Fatal("current cleanup retained its in-memory runtime")
	}
	command.mu.Lock()
	currentCalls := append([][]string(nil), command.calls...)
	command.mu.Unlock()
	joined = make([]string, len(currentCalls))
	for index := range currentCalls {
		joined[index] = strings.Join(currentCalls[index], "\x00")
	}
	currentJoined := strings.Join(joined, "\n")
	if !strings.Contains(currentJoined, "label=worksflow.verification.fence=10") ||
		!strings.Contains(currentJoined, "--format\x00"+verificationNetworkListFormat) ||
		!strings.Contains(currentJoined, "network\x00rm\x00"+networkID) {
		t.Fatalf("current cleanup did not remove shared network: %#v", currentCalls)
	}
}

func TestDockerCandidateExecutorNetworkCleanupFailsClosedAndRetries(t *testing.T) {
	resourceID := "0123456789abcdef0123456789abcdef"
	tests := []struct {
		name      string
		output    string
		failList  bool
		failFirst bool
		invalid   bool
		wrongName bool
	}{
		{name: "empty is idempotent"},
		{name: "list failure", failList: true},
		{name: "remove failure retries", output: resourceID, failFirst: true},
		{name: "invalid network identity", output: "not-a-runtime-id", invalid: true},
		{name: "mismatched exact network name", output: resourceID, invalid: true, wrongName: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor, command, _, request := candidateExecutorFixture(t)
			removeAttempts := 0
			command.stdoutFor = func(args []string) string {
				if len(args) > 1 && args[0] == "network" && args[1] == "ls" &&
					containsArgument(args, "label=worksflow.verification.fence=9") {
					if test.output == "" {
						return ""
					}
					name := verificationNetworkName(request.AttemptID)
					if test.wrongName {
						name = "worksflow-vnet-unrelated"
					}
					return test.output + "\t" + name + "\n"
				}
				return ""
			}
			command.onRun = func(args []string) error {
				if len(args) > 1 && args[0] == "network" && args[1] == "ls" && test.failList {
					return errors.New("verification daemon unavailable")
				}
				if len(args) > 1 && args[0] == "network" && args[1] == "rm" {
					removeAttempts++
					if test.failFirst && removeAttempts == 1 {
						return errors.New("network still has an endpoint")
					}
				}
				return nil
			}
			cleanup := VerificationEnvironmentCleanup{Fence: VerificationExecutionFence{
				ProjectID: request.ProjectID, RunID: request.RunID, AttemptID: request.AttemptID,
				AttemptFenceEpoch: request.AttemptFenceEpoch,
			}}
			err := executor.CleanupVerificationEnvironment(context.Background(), cleanup)
			wantError := test.failList || test.failFirst || test.invalid
			if (err != nil) != wantError || (err != nil && !errors.Is(err, ErrCandidateExecution)) {
				t.Fatalf("first network cleanup error = %v", err)
			}
			if test.failFirst {
				if err := executor.CleanupVerificationEnvironment(context.Background(), cleanup); err != nil {
					t.Fatalf("network cleanup retry = %v", err)
				}
				if removeAttempts != 2 {
					t.Fatalf("network remove attempts = %d", removeAttempts)
				}
			}
		})
	}
}

func TestDockerCandidateExecutorLegacyNetworkCleanupRequiresMarkerOwnership(t *testing.T) {
	executor, command, _, request := candidateExecutorFixture(t)
	legacyID := "abcdef012345abcdef012345abcdef01"
	command.stdoutFor = func(args []string) string {
		if containsArgument(args, "name=^"+regexp.QuoteMeta(verificationNetworkName(request.AttemptID))+"$") {
			return legacyID + "\t" + verificationNetworkName(request.AttemptID) + "\n"
		}
		return ""
	}
	cleanup := VerificationEnvironmentCleanup{Fence: VerificationExecutionFence{
		ProjectID: request.ProjectID, RunID: request.RunID, AttemptID: request.AttemptID,
		AttemptFenceEpoch: request.AttemptFenceEpoch,
	}}
	if err := executor.CleanupVerificationEnvironment(context.Background(), cleanup); err != nil {
		t.Fatal(err)
	}
	command.mu.Lock()
	withoutOwnership := append([][]string(nil), command.calls...)
	command.calls = nil
	command.mu.Unlock()
	if joinedCommandCalls(withoutOwnership, "network", "rm", legacyID) {
		t.Fatal("legacy Attempt network was removed without marker ownership")
	}
	cleanup.OwnsSharedRuntime = true
	if err := executor.CleanupVerificationEnvironment(context.Background(), cleanup); err != nil {
		t.Fatal(err)
	}
	command.mu.Lock()
	withOwnership := append([][]string(nil), command.calls...)
	command.mu.Unlock()
	if !joinedCommandCalls(withOwnership, "network", "rm", legacyID) {
		t.Fatalf("owned legacy Attempt network was not removed: %#v", withOwnership)
	}
}

func TestDockerCandidateExecutorRequiresExplicitPodmanDaemonAndTargetsSelectedRuntime(t *testing.T) {
	binRoot := t.TempDir()
	podman := filepath.Join(binRoot, "podman")
	if err := os.WriteFile(podman, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := DockerCandidateExecutorConfig{
		RuntimeBinary: podman, WorkspaceRoot: t.TempDir(), Memory: "512m", CPUs: "1.0",
		PIDs: 64, OutputLimit: 1024, TempBytes: 1 << 20, User: "10001:10001",
	}
	if _, err := NewDockerCandidateExecutor(
		config, newVerificationContentStoreFake(), &candidateContainerCommandFake{},
	); !errors.Is(err, ErrCandidateExecution) || !strings.Contains(err.Error(), "explicit daemon host") {
		t.Fatalf("local Podman verification was accepted without an explicit daemon: %v", err)
	}
	config.DaemonHost = "unix:///run/podman/podman.sock"
	executor, err := NewDockerCandidateExecutor(
		config, newVerificationContentStoreFake(), &candidateContainerCommandFake{},
	)
	if err != nil || executor.runtimeName != "podman" {
		t.Fatalf("explicit remote Podman verification executor = %#v, %v", executor, err)
	}
	podmanEnvironment := verificationContainerEnvironment(config.WorkspaceRoot, "podman", config.DaemonHost)
	if !containsArgument(podmanEnvironment, "CONTAINER_HOST="+config.DaemonHost) ||
		containsArgument(podmanEnvironment, "DOCKER_HOST="+config.DaemonHost) {
		t.Fatalf("Podman daemon environment = %#v", podmanEnvironment)
	}
	dockerEnvironment := verificationContainerEnvironment(config.WorkspaceRoot, "docker", "tcp://127.0.0.1:2375")
	if !containsArgument(dockerEnvironment, "DOCKER_HOST=tcp://127.0.0.1:2375") ||
		containsArgument(dockerEnvironment, "CONTAINER_HOST=tcp://127.0.0.1:2375") {
		t.Fatalf("Docker daemon environment = %#v", dockerEnvironment)
	}
}

func containsArgument(arguments []string, expected string) bool {
	for _, argument := range arguments {
		if argument == expected {
			return true
		}
	}
	return false
}

func joinedCommandCalls(calls [][]string, expected ...string) bool {
	target := strings.Join(expected, "\x00")
	for _, call := range calls {
		if strings.Contains(strings.Join(call, "\x00"), target) {
			return true
		}
	}
	return false
}

func TestDockerCandidateExecutorFailsClosedWhenSecretMayBeInDiscardedTail(t *testing.T) {
	executor, command, contents, request := candidateExecutorFixture(t)
	executor.outputLimit = 16
	command.stdout = "normal-prefix\n" + strings.Repeat("x", 32) +
		"Authorization: Bearer secret-only-in-discarded-tail\n"

	outcome, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != CheckError || outcome.ExitCode != nil || !outcome.Truncated ||
		outcome.RedactionCount != 0 || outcome.Stdout == nil || len(outcome.Diagnostics) != 1 ||
		outcome.Diagnostics[0].ID != "verification-output-truncated" ||
		outcome.Diagnostics[0].Code != "output_truncated" ||
		outcome.Diagnostics[0].Severity != SeverityBlocker ||
		outcome.Diagnostics[0].Message != "Command output exceeded the platform capture limit; discarded output is untrusted, so this check cannot pass." {
		t.Fatalf("truncated executor outcome = %#v", outcome)
	}
	stored, err := contents.Get(context.Background(), outcome.Stdout.Ref, outcome.Stdout.ContentHash)
	if err != nil {
		t.Fatal(err)
	}
	var logPayload struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(stored.Payload, &logPayload); err != nil {
		t.Fatal(err)
	}
	if logPayload.Value != "normal-prefix\nxx" || len(logPayload.Value) != executor.outputLimit ||
		strings.Contains(logPayload.Value, "secret-only-in-discarded-tail") ||
		!contents.finalized[outcome.Stdout.Ref] {
		t.Fatalf("bounded stdout evidence = %q", logPayload.Value)
	}
}

func TestDockerCandidateExecutorPersistsTerminalLogsAfterCheckContextCancellation(t *testing.T) {
	executor, command, contents, request := candidateExecutorFixture(t)
	command.stdout = "terminal stdout\n"
	command.stderr = "terminal stderr\n"
	command.err = context.Canceled

	if _, err := executor.Execute(context.Background(), request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled execution error = %v", err)
	}
	contents.mu.Lock()
	defer contents.mu.Unlock()
	if len(contents.values) != 2 || len(contents.finalized) != 2 {
		t.Fatalf("terminal log evidence was not finalized after cancellation: values=%d finalized=%d",
			len(contents.values), len(contents.finalized))
	}
}

func TestDockerCandidateExecutorHoldsAttemptLockUntilRuntimeCommandQuiesces(t *testing.T) {
	executor, command, _, request := candidateExecutorFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	command.onRun = func(args []string) error {
		if len(args) > 0 && args[0] == "run" {
			close(started)
			<-release
		}
		return nil
	}
	completed := make(chan error, 1)
	go func() {
		_, err := executor.Execute(context.Background(), request)
		completed <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runtime command did not start")
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := withVerificationAttemptLock(
		cleanupCtx, executor.root, request.AttemptID, func(string) error { return nil },
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup acquired the Attempt lock while execution was active: %v", err)
	}
	close(release)
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("runtime execution after release = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime command did not quiesce")
	}
}

func TestDockerCandidateExecutorCreatesIsolatedEphemeralPostgresFromPlan(t *testing.T) {
	executor, command, _, request := candidateExecutorFixture(t)
	postgresImage := "postgres@sha256:" + strings.Repeat("a", 64)
	apiImage := "python@sha256:" + strings.Repeat("b", 64)
	policy := map[string]any{"postgres": map[string]any{
		"image": postgresImage, "database": "verification", "user": "verification", "runtimeUser": "999:999",
	}, "services": []any{map[string]any{
		"id": "api", "image": apiImage, "workingDirectory": ".",
		"argv":       []any{"python", "-m", "api"},
		"healthArgv": []any{"python", "-m", "api.health"},
	}}}
	request.RuntimePolicy.NetworkPolicy = policy
	spec := CandidateExecutionSpec{
		RunID: request.RunID, AttemptID: request.AttemptID,
		AttemptFenceEpoch: request.AttemptFenceEpoch,
		PlanID:            uuid.NewString(), PlanHash: verificationTestHash("service-plan"),
		Content: PlanContent{
			SchemaVersion: PlanContentSchemaVersion, Scope: ScopeCandidate,
			ProjectID: request.ProjectID, Subject: request.Subject,
			RuntimePolicy: PlanRuntimePolicy{Limits: map[string]any{}, NetworkPolicy: policy},
		},
	}
	if err := executor.Prepare(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	command.stdoutFor = func(args []string) string {
		if len(args) > 0 && args[0] == "run" {
			return "database ready\n"
		}
		return ""
	}
	outcome, err := executor.Execute(context.Background(), request)
	if err != nil || outcome.Status != CheckPassed {
		t.Fatalf("service-backed check = %#v, %v", outcome, err)
	}
	command.mu.Lock()
	calls := append([][]string(nil), command.calls...)
	command.mu.Unlock()
	joinedCalls := make([]string, len(calls))
	for index := range calls {
		joinedCalls[index] = strings.Join(calls[index], "\x00")
	}
	joined := strings.Join(joinedCalls, "\n")
	if !strings.Contains(joined, "network\x00create\x00--internal") ||
		!strings.Contains(joined, postgresImage) ||
		!strings.Contains(joined, apiImage) ||
		!strings.Contains(joined, "--network-alias\x00api") ||
		!strings.Contains(joined, "exec\x00"+verificationServiceName(request.AttemptID, "api")+"\x00python\x00-m\x00api.health") ||
		!strings.Contains(joined, "--network\x00"+verificationNetworkName(request.AttemptID)) ||
		!strings.Contains(joined, "--env-file") || strings.Contains(joined, "POSTGRES_PASSWORD") {
		t.Fatalf("ephemeral service boundary = %s", joined)
	}
	if err := executor.Collect(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	request.RuntimePolicy.NetworkPolicy = map[string]any{"postgres": map[string]any{
		"image": "postgres:latest", "database": "verification", "user": "verification", "runtimeUser": "999:999",
	}}
	if _, err := executor.Execute(context.Background(), request); !errors.Is(err, ErrCandidateExecution) {
		t.Fatalf("mutable PostgreSQL service image = %v", err)
	}
}

func TestDockerCandidateExecutorResolvesOnlyPinnedManifestsAndMountsReadOnlyCache(t *testing.T) {
	executor, command, _, request := candidateExecutorFixture(t)
	workspace := filepath.Join(executor.root, request.AttemptID, "9", "workspace")
	serviceRoot := filepath.Join(workspace, "apps", "web")
	if err := os.MkdirAll(serviceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"dependencies":{"react":"18.3.1"}}`)
	lock := []byte(`{
  "lockfileVersion": 3,
  "packages": {
    "": {"dependencies":{"react":"18.3.1"}},
    "node_modules/react": {
      "version":"18.3.1",
      "resolved":"https://registry.npmjs.org/react/-/react-18.3.1.tgz",
      "integrity":"sha512-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
    }
  }
}`)
	if err := os.WriteFile(filepath.Join(serviceRoot, "package.json"), manifest, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceRoot, "package-lock.json"), lock, 0o400); err != nil {
		t.Fatal(err)
	}
	registry := "https://registry.npmjs.org"
	dependency := PlanDependency{
		ID: "dependency-web", ServiceID: "web", Ecosystem: "node", WorkingDirectory: "apps/web",
		ToolchainImageDigest: "node@sha256:" + strings.Repeat("d", 64),
		ManifestPaths:        []string{"apps/web/package.json"},
		Lockfiles: []PlanDependencyLock{{
			Path: "apps/web/package-lock.json", Digest: hashVerificationBytes(lock), Registry: registry,
		}},
		CacheKey: "sha256:" + strings.Repeat("e", 64),
	}
	dependency.ResolverArgv = expectedDependencyResolverArgv(dependency)
	resolverRuns := 0
	command.onRun = func(args []string) error {
		if !containsArgumentSequence(args, []string{"npm", "ci"}) {
			return nil
		}
		resolverRuns++
		for index, argument := range args {
			if argument != "--mount" || index+1 >= len(args) || !strings.HasSuffix(args[index+1], ",dst=/resolver") {
				continue
			}
			mount := strings.TrimSuffix(strings.TrimPrefix(args[index+1], "type=bind,src="), ",dst=/resolver")
			return os.Mkdir(filepath.Join(mount, "node_modules"), 0o700)
		}
		return errors.New("resolver staging mount was not provided")
	}
	policy := map[string]any{
		"mode": "none", "dependencyResolver": map[string]any{"network": "resolver-egress"},
	}
	spec := CandidateExecutionSpec{
		RunID: request.RunID, AttemptID: request.AttemptID, AttemptFenceEpoch: 9,
		PlanID: uuid.NewString(), PlanHash: verificationTestHash("dependency-plan"),
		Content: PlanContent{
			SchemaVersion: PlanContentSchemaVersion, Scope: ScopeCandidate,
			ProjectID: request.ProjectID, Subject: request.Subject,
			Dependencies:  []PlanDependency{dependency},
			RuntimePolicy: PlanRuntimePolicy{Limits: map[string]any{}, NetworkPolicy: policy},
		},
	}
	if err := executor.Prepare(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if err := executor.Prepare(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if resolverRuns != 1 {
		t.Fatalf("immutable dependency cache was not reused: resolver runs=%d", resolverRuns)
	}
	request.RuntimePolicy.NetworkPolicy = policy
	request.Dependencies = []PlanDependency{dependency}
	request.Check = PlanCheck{
		ID: "web-unit", Kind: "unit", ServiceID: "web", Required: true,
		VerifierImageDigest: "node@sha256:" + strings.Repeat("f", 64),
		Argv:                []string{"npm", "test"}, WorkingDirectory: "apps/web", TimeoutSeconds: 60,
	}
	if _, err := executor.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	command.mu.Lock()
	calls := append([][]string(nil), command.calls...)
	command.mu.Unlock()
	joined := ""
	for _, call := range calls {
		joined += strings.Join(call, "\x00") + "\n"
	}
	if !strings.Contains(joined, "--network\x00resolver-egress") ||
		!strings.Contains(joined, dependency.ToolchainImageDigest+"\x00npm\x00ci") ||
		strings.Contains(joined, workspace+",dst=/resolver") ||
		!strings.Contains(joined, "node_modules,dst=/workspace/apps/web/node_modules,readonly") ||
		!strings.Contains(joined, "npm_config_offline=true") {
		t.Fatalf("dependency resolver/cache boundary = %s", joined)
	}

	bad := spec
	bad.Content.Dependencies = clonePlanDependencies(spec.Content.Dependencies)
	bad.Content.Dependencies[0].CacheKey = "sha256:" + strings.Repeat("1", 64)
	bad.Content.Dependencies[0].Lockfiles[0].Digest = "sha256:" + strings.Repeat("2", 64)
	if err := executor.Prepare(context.Background(), bad); !errors.Is(err, ErrCandidateExecution) {
		t.Fatalf("drifted dependency lock was accepted: %v", err)
	}
}

func TestValidHashLockedGoSum(t *testing.T) {
	valid := []byte("github.com/google/uuid v1.6.0 h1:NIvaJDMOsjHA8rEtSDVpk7EE4dBeE7mybJP0UuF4dO8=\n" +
		"github.com/google/uuid v1.6.0/go.mod h1:TIyPZe4Mgqvfe9pImFwG2Rd0Vz9SFlf86jG1GtPaR2s=\n")
	if !validHashLockedGoSum(valid) {
		t.Fatal("valid Go checksum lock was rejected")
	}
	for _, invalid := range [][]byte{
		{},
		[]byte("github.com/google/uuid v1.6.0\n"),
		[]byte("https://example.test/module v1.0.0 h1:NIvaJDMOsjHA8rEtSDVpk7EE4dBeE7mybJP0UuF4dO8=\n"),
		[]byte("github.com/google/uuid v1.6.0 sha256:bad\n"),
	} {
		if validHashLockedGoSum(invalid) {
			t.Fatalf("invalid Go checksum lock was accepted: %q", invalid)
		}
	}
}

func containsArgumentSequence(arguments, expected []string) bool {
	for start := 0; start+len(expected) <= len(arguments); start++ {
		match := true
		for offset := range expected {
			if arguments[start+offset] != expected[offset] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestPythonDependencyLockRequiresHashForEveryRequirement(t *testing.T) {
	valid := []byte("fastapi==0.111.0 --hash=sha256:" + strings.Repeat("a", 64) + "\n" +
		"uvicorn==0.30.1 \\\n  --hash=sha256:" + strings.Repeat("b", 64) + "\n")
	if !validHashLockedPythonRequirements(valid) {
		t.Fatal("fully hash-locked Python requirements were rejected")
	}
	missing := []byte("fastapi==0.111.0 --hash=sha256:" + strings.Repeat("a", 64) + "\nuvicorn==0.30.1\n")
	if validHashLockedPythonRequirements(missing) {
		t.Fatal("Python requirement without a hash was accepted")
	}
	if validHashLockedPythonRequirements([]byte("pkg @ https://evil.example/pkg.whl --hash=sha256:" + strings.Repeat("c", 64))) {
		t.Fatal("direct URL Python dependency was accepted")
	}
}

func candidateExecutorFixture(
	t *testing.T,
) (*DockerCandidateExecutor, *candidateContainerCommandFake, *verificationContentStoreFake, CheckExecutionRequest) {
	t.Helper()
	binRoot := t.TempDir()
	binary := filepath.Join(binRoot, "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	contents := newVerificationContentStoreFake()
	command := &candidateContainerCommandFake{}
	executor, err := NewDockerCandidateExecutor(DockerCandidateExecutorConfig{
		RuntimeBinary: binary, WorkspaceRoot: root, Memory: "512m", CPUs: "1.0",
		PIDs: 64, OutputLimit: 1024, TempBytes: 1 << 20, User: "10001:10001",
	}, contents, command)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := (PlanCompiler{}).Compile(validCandidatePlanInput())
	if err != nil {
		t.Fatal(err)
	}
	attemptID := uuid.NewString()
	fence := uint64(9)
	check := compiled.Content.Checks[0]
	workspace := filepath.Join(root, attemptID, "9", "workspace")
	workingDirectory := check.WorkingDirectory
	if workingDirectory == "." {
		workingDirectory = ""
	}
	if err := os.MkdirAll(filepath.Join(workspace, filepath.FromSlash(workingDirectory)), 0o700); err != nil {
		t.Fatal(err)
	}
	request := CheckExecutionRequest{
		ProjectID: compiled.Content.ProjectID, RunID: uuid.NewString(), AttemptID: attemptID,
		AttemptFenceEpoch: fence, AttemptCount: 1, Subject: compiled.Content.Subject,
		RuntimePolicy: compiled.Content.RuntimePolicy, Check: check,
	}
	return executor, command, contents, request
}
