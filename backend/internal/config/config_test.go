package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	clearConfigEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Environment != EnvironmentDevelopment {
		t.Fatalf("Environment = %q, want %q", cfg.Environment, EnvironmentDevelopment)
	}
	if cfg.HTTP.Address() != "0.0.0.0:8080" {
		t.Fatalf("HTTP address = %q", cfg.HTTP.Address())
	}
	if cfg.WebSocket.AllowAnonymous {
		t.Fatal("anonymous WebSocket access must be disabled by default")
	}
	if !cfg.Startup.EnsureMongoIndexes || !cfg.Startup.EnsureNATSStream || !cfg.Outbox.Enabled {
		t.Fatalf("development startup defaults = %#v, outbox = %#v", cfg.Startup, cfg.Outbox)
	}
	if cfg.AI.Provider != "openai" || !cfg.Workflow.WorkerEnabled || cfg.Idempotency.TTL <= cfg.Idempotency.LockTTL {
		t.Fatalf("platform runtime defaults = AI %#v, workflow %#v, idempotency %#v", cfg.AI, cfg.Workflow, cfg.Idempotency)
	}
	if cfg.WorkflowQualificationActivation.WorkerEnabled ||
		cfg.WorkflowQualificationActivation.PostgresDSN != "" ||
		cfg.WorkflowQualificationActivation.MaxTransactionRetries != 3 ||
		cfg.WorkflowQualificationActivation.MaxOpenConns != 10 ||
		cfg.WorkflowQualificationActivation.MaxIdleConns != 4 {
		t.Fatalf("workflow qualification activation defaults must be bounded and disabled: %#v", cfg.WorkflowQualificationActivation)
	}
	if cfg.AI.Timeout != 12*time.Minute || cfg.AI.MaxRetries != 0 {
		t.Fatalf("structured generation timeout/retry defaults = %#v", cfg.AI)
	}
	if _, err := parseEncryptionKey(cfg.Secrets.EncryptionKey); err != nil || cfg.GitHub.CredentialTTL <= 0 {
		t.Fatalf("secret/integration defaults are invalid: secrets=%#v GitHub=%#v", cfg.Secrets, cfg.GitHub)
	}
	if cfg.TemplateSource.GitBinary != "git" || cfg.TemplateSource.CacheRoot != "/var/lib/worksflow/template-sources" ||
		len(cfg.TemplateSource.AllowedHosts) != 1 || cfg.TemplateSource.AllowedHosts[0] != "github.com" ||
		cfg.TemplateSource.FetchTimeout != 2*time.Minute {
		t.Fatalf("template source defaults are invalid: %#v", cfg.TemplateSource)
	}
	if cfg.Repository.SearchIndex.MaxTrees != 16 ||
		cfg.Repository.SearchIndex.MaxSourceBytes != 256<<20 ||
		cfg.Repository.SearchIndex.MaxActiveBuilds != 2 ||
		cfg.Repository.SearchAdmission.RedisPrefix != "worksflow:repository:exact-tree-search-admission:" ||
		cfg.Repository.SearchAdmission.Timeout != 250*time.Millisecond ||
		cfg.Repository.SearchAdmission.QueryProject != (RepositorySearchRateBucketConfig{
			RefillTokens: 20, RefillInterval: time.Second, Burst: 40,
		}) || cfg.Repository.SearchAdmission.QueryActor != (RepositorySearchRateBucketConfig{
		RefillTokens: 4, RefillInterval: time.Second, Burst: 8,
	}) || cfg.Repository.SearchAdmission.BuildProject != (RepositorySearchRateBucketConfig{
		RefillTokens: 1, RefillInterval: 15 * time.Second, Burst: 2,
	}) || cfg.Repository.SearchAdmission.BuildActor != (RepositorySearchRateBucketConfig{
		RefillTokens: 1, RefillInterval: 30 * time.Second, Burst: 1,
	}) {
		t.Fatalf("repository search defaults are invalid: %#v", cfg.Repository)
	}
	if cfg.Delivery.SandboxRuntime != "docker" || cfg.Delivery.SandboxNodeImage == "" || cfg.Delivery.PublishRoot == "" {
		t.Fatalf("delivery defaults are invalid: %#v", cfg.Delivery)
	}
	if cfg.Sandbox.Enabled || cfg.Sandbox.RunnerImage != "" || cfg.Sandbox.CPUMillis != 2_000 ||
		cfg.Sandbox.MemoryBytes != 4<<30 || cfg.Sandbox.IdleHibernateAfter != 30*time.Minute ||
		cfg.Sandbox.RuntimeBinary != "docker" || !strings.HasPrefix(cfg.Sandbox.WorkspaceRoot, "/") ||
		cfg.Sandbox.GatewayNetwork != "bridge" ||
		cfg.Sandbox.GatewayBindAddress != "127.0.0.1" || cfg.Sandbox.PreviewDialHost != "127.0.0.1" ||
		cfg.Sandbox.PreviewPublicOrigin != "http://preview.localhost:8080" ||
		cfg.Sandbox.PreviewTicketTTL != 15*time.Minute || cfg.Sandbox.PreviewProbeTimeout != 750*time.Millisecond ||
		cfg.Sandbox.StartupTimeout != 2*time.Minute || cfg.Sandbox.CommandTimeout != 30*time.Second ||
		cfg.Sandbox.LifecyclePoll != 5*time.Second || cfg.Sandbox.LifecycleLease != 15*time.Minute ||
		cfg.Sandbox.LifecycleRetry != 30*time.Second ||
		cfg.Sandbox.ConnectionTicketTTL != 30*time.Second || cfg.Sandbox.StreamMaxEvents != 4_096 ||
		cfg.Sandbox.StreamRetention != 24*time.Hour || cfg.Sandbox.RedisPrefix == "" {
		t.Fatalf("interactive sandbox defaults must be safe and disabled: %#v", cfg.Sandbox)
	}
	if cfg.LSP.Enabled || cfg.LSP.RuntimeBinary != "/usr/bin/docker" || cfg.LSP.DaemonHost != "" ||
		cfg.LSP.RequireTLS || cfg.LSP.TicketTTL != 20*time.Second || cfg.LSP.BindTimeout != 5*time.Second ||
		cfg.LSP.WriteWait != 2*time.Second || cfg.LSP.CommandTimeout != 30*time.Second ||
		cfg.LSP.CLIOutputMax != 1<<20 || cfg.LSP.RedisPrefix != "worksflow:sandbox:lsp:" ||
		cfg.LSP.RefillPerSecond != 30 || cfg.LSP.Burst != 60 || len(cfg.LSP.AllowedOrigins) != 2 {
		t.Fatalf("LSP defaults must be bounded and disabled: %#v", cfg.LSP)
	}
	if cfg.Agent.Enabled || cfg.Agent.WorkerEnabled || cfg.Agent.RunnerImage != "" ||
		cfg.Agent.ProfileID != "codex-default" || cfg.Agent.OutputSchemaHash != "" ||
		cfg.Agent.WallTime != 30*time.Minute || cfg.Agent.MaxContextFiles != 256 ||
		cfg.Agent.RuntimeBinary != "docker" || cfg.Agent.WorktreeRoot != "/var/lib/worksflow/agent-worktrees" ||
		cfg.Agent.RunnerNetwork != "worksflow-agent-model" || cfg.Agent.GatewayBaseURL != "" ||
		cfg.Agent.ClaimBatch != 10 {
		t.Fatalf("Agent defaults must be bounded and disabled: %#v", cfg.Agent)
	}
	if cfg.Verification.WorkerEnabled || cfg.Verification.RuntimeBinary != "docker" ||
		cfg.Verification.WorkspaceRoot != "/var/lib/worksflow/verification" ||
		cfg.Verification.LeaseDuration != 2*time.Minute || cfg.Verification.Heartbeat != 30*time.Second ||
		cfg.Verification.OutputMax != 1<<20 || cfg.Verification.TempBytes != 512<<20 {
		t.Fatalf("Verification worker defaults must be bounded and disabled: %#v", cfg.Verification)
	}
	for _, header := range []string{
		"X-Sandbox-Session-Epoch", "X-Expected-Candidate-ID", "X-Candidate-Version",
		"X-Candidate-Journal-Sequence", "X-Candidate-Tree-Hash", "X-Expected-File-Hash",
	} {
		if !containsString(cfg.CORS.AllowedHeaders, header) {
			t.Fatalf("default CORS request headers omit %s: %#v", header, cfg.CORS.AllowedHeaders)
		}
	}
	for _, header := range []string{
		"X-Sandbox-Session-ETag", "X-Candidate-ID", "X-Candidate-Journal-Sequence",
		"X-Candidate-Tree-Hash", "X-Content-Hash", "X-Content-Object-Hash",
		"X-File-Exists", "X-Byte-Size", "X-Patch-Content-Hash", "Retry-After",
	} {
		if !containsString(cfg.CORS.ExposedHeaders, header) {
			t.Fatalf("default CORS response headers omit %s: %#v", header, cfg.CORS.ExposedHeaders)
		}
	}
}

func TestLoadRejectsCORSOverridesThatHideIntegrityFences(t *testing.T) {
	t.Run("request fence", func(t *testing.T) {
		clearConfigEnvironment(t)
		t.Setenv("CORS_ALLOWED_HEADERS", "Accept,Authorization,Content-Type,Idempotency-Key,If-Match,X-CSRF-Token,X-Request-ID")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "X-Sandbox-Session-Epoch") {
			t.Fatalf("Load() error = %v, want missing Sandbox request fence", err)
		}
	})

	t.Run("response integrity", func(t *testing.T) {
		clearConfigEnvironment(t)
		t.Setenv("CORS_EXPOSED_HEADERS", strings.Join([]string{
			"ETag", "Retry-After", "X-Sandbox-Session-ETag", "X-Sandbox-Session-Epoch",
			"X-Candidate-ID", "X-Candidate-Version", "X-Candidate-Journal-Sequence",
			"X-Writer-Lease-Epoch", "X-Candidate-Tree-Hash", "X-Candidate-Tree-ETag",
			"X-Content-Hash", "X-File-Mode", "X-File-Exists", "X-Byte-Size",
			"X-Patch-Content-Hash", "X-Agent-Attempt-State", "X-Agent-Fence-Epoch",
		}, ","))
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "X-Content-Object-Hash") {
			t.Fatalf("Load() error = %v, want missing raw evidence object hash", err)
		}
	})
}

func TestRepositorySearchGuardrailsAreConfigurableAndBounded(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("REPOSITORY_SEARCH_INDEX_MAX_TREES", "24")
	t.Setenv("REPOSITORY_SEARCH_INDEX_MAX_SOURCE_BYTES", "536870912")
	t.Setenv("REPOSITORY_SEARCH_INDEX_MAX_ACTIVE_BUILDS", "3")
	t.Setenv("REPOSITORY_SEARCH_RATE_REDIS_PREFIX", "worksflow:custom:search-rate:")
	t.Setenv("REPOSITORY_SEARCH_RATE_TIMEOUT", "75ms")
	t.Setenv("REPOSITORY_SEARCH_QUERY_PROJECT_REFILL_TOKENS", "10")
	t.Setenv("REPOSITORY_SEARCH_QUERY_PROJECT_REFILL_INTERVAL", "2s")
	t.Setenv("REPOSITORY_SEARCH_QUERY_PROJECT_BURST", "30")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("bounded repository search configuration was rejected: %v", err)
	}
	if cfg.Repository.SearchIndex != (RepositorySearchIndexConfig{
		MaxTrees: 24, MaxSourceBytes: 512 << 20, MaxActiveBuilds: 3,
	}) || cfg.Repository.SearchAdmission.RedisPrefix != "worksflow:custom:search-rate:" ||
		cfg.Repository.SearchAdmission.Timeout != 75*time.Millisecond ||
		cfg.Repository.SearchAdmission.QueryProject != (RepositorySearchRateBucketConfig{
			RefillTokens: 10, RefillInterval: 2 * time.Second, Burst: 30,
		}) {
		t.Fatalf("custom repository search config = %#v", cfg.Repository)
	}

	for name, value := range map[string]string{
		"REPOSITORY_SEARCH_INDEX_MAX_TREES":             "0",
		"REPOSITORY_SEARCH_INDEX_MAX_SOURCE_BYTES":      "1099511627777",
		"REPOSITORY_SEARCH_INDEX_MAX_ACTIVE_BUILDS":     "25",
		"REPOSITORY_SEARCH_RATE_REDIS_PREFIX":           "unsafe:{caller}:",
		"REPOSITORY_SEARCH_RATE_TIMEOUT":                "251ms",
		"REPOSITORY_SEARCH_QUERY_ACTOR_REFILL_INTERVAL": "1ms",
		"REPOSITORY_SEARCH_BUILD_PROJECT_REFILL_TOKENS": "10001",
		"REPOSITORY_SEARCH_BUILD_PROJECT_BURST":         "0",
		"REPOSITORY_SEARCH_BUILD_ACTOR_REFILL_INTERVAL": "1h1ms",
		"REPOSITORY_SEARCH_BUILD_ACTOR_BURST":           "100000",
	} {
		t.Run(name, func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv(name, value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "repository exact-tree search") {
				t.Fatalf("invalid %s=%q was accepted: %v", name, value, err)
			}
		})
	}
}

func TestReleaseDeliveryWorkerRequiresExactV3ControllerAuthority(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("RELEASE_DELIVERY_WORKER_ENABLED", "true")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_URL", "https://delivery-controller.example.test")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_TOKEN", strings.Repeat("secret", 8))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "exact id, version") {
		t.Fatalf("controller without exact authority pins was accepted: %v", err)
	}

	t.Setenv("RELEASE_DELIVERY_CONTROLLER_ID", "production-delivery-controller")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_VERSION", "2026.07.18+build.42")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_TRUST_KEY_DIGEST", "sha256:"+strings.Repeat("a", 64))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("qualified v3 controller was rejected: %v", err)
	}
	if !cfg.Delivery.ReleaseWorkerEnabled || cfg.Delivery.ReleaseControllerProtocol != "worksflow.release-delivery/v3" ||
		cfg.Delivery.ReleaseRequestTimeout != 2*time.Minute || cfg.Delivery.ReleaseReconcileDelay != 5*time.Second {
		t.Fatalf("qualified release delivery config = %#v", cfg.Delivery)
	}
}

func TestReleaseDeliveryWorkerRejectsInsecureOrUnboundedControllerCalls(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("RELEASE_DELIVERY_WORKER_ENABLED", "true")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_URL", "http://delivery-controller.example.test")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_TOKEN", strings.Repeat("secret", 8))
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_ID", "production-delivery-controller")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_VERSION", "2026.07.18+build.42")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_TRUST_KEY_DIGEST", "sha256:"+strings.Repeat("a", 64))
	t.Setenv("RELEASE_DELIVERY_REQUEST_TIMEOUT", "6m")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "RELEASE_DELIVERY_CONTROLLER_URL") ||
		!strings.Contains(err.Error(), "request timeout") {
		t.Fatalf("insecure or unbounded release controller was accepted: %v", err)
	}
}

func TestQualificationReleasePublisherIsDefaultOffAndRequiresClosedUpstreamConfiguration(t *testing.T) {
	clearConfigEnvironment(t)
	defaultConfig, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if defaultConfig.Workflow.ProfileV3RuntimeEnabled || defaultConfig.QualificationRelease.Enabled ||
		defaultConfig.QualificationRelease.PostgresDSN != "" {
		t.Fatalf("qualified release defaults are not fail-closed: %#v", defaultConfig.QualificationRelease)
	}

	t.Setenv("QUALIFICATION_RELEASE_PUBLISHER_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WORKFLOW_PROFILE_V3_RUNTIME_ENABLED") ||
		!strings.Contains(err.Error(), "RELEASE_DELIVERY_WORKER_ENABLED") ||
		!strings.Contains(err.Error(), "QUALIFICATION_RELEASE_POSTGRES_DSN") {
		t.Fatalf("incomplete qualified release chain was accepted: %v", err)
	}

	t.Setenv("WORKFLOW_PROFILE_V3_RUNTIME_ENABLED", "true")
	t.Setenv("WORKFLOW_QUALIFICATION_ACTIVATION_WORKER_ENABLED", "true")
	t.Setenv("WORKFLOW_INPUT_AUTHORITY_OPERATOR_POSTGRES_DSN", "postgres://workflow_input_operator:secret@localhost:5432/worksflow?sslmode=disable")
	t.Setenv("RELEASE_DELIVERY_WORKER_ENABLED", "true")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_URL", "https://delivery-controller.example.test")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_TOKEN", strings.Repeat("secret", 8))
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_ID", "production-delivery-controller")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_VERSION", "2026.07.20+qualified")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_TRUST_KEY_DIGEST", "sha256:"+strings.Repeat("a", 64))
	t.Setenv("QUALIFICATION_RELEASE_POSTGRES_DSN", "postgres://qualified_release:secret@localhost:5432/worksflow?sslmode=disable")
	config, err := Load()
	if err != nil {
		t.Fatalf("closed qualified release chain was rejected: %v", err)
	}
	if !config.Workflow.ProfileV3RuntimeEnabled || !config.QualificationRelease.Enabled ||
		config.QualificationRelease.WorkerConcurrency != 4 ||
		config.QualificationRelease.LeaseDuration != 2*time.Minute ||
		config.QualificationRelease.ControllerPollInterval != 2*time.Second {
		t.Fatalf("qualified release config = %#v", config.QualificationRelease)
	}
}

func TestQualificationReleasePublisherRejectsSharedCredentialAndUnboundedLease(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("QUALIFICATION_RELEASE_PUBLISHER_ENABLED", "true")
	t.Setenv("WORKFLOW_PROFILE_V3_RUNTIME_ENABLED", "true")
	t.Setenv("WORKFLOW_QUALIFICATION_ACTIVATION_WORKER_ENABLED", "true")
	t.Setenv("WORKFLOW_INPUT_AUTHORITY_OPERATOR_POSTGRES_DSN", "postgres://workflow_input_operator:secret@localhost:5432/worksflow?sslmode=disable")
	t.Setenv("RELEASE_DELIVERY_WORKER_ENABLED", "true")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_URL", "https://delivery-controller.example.test")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_TOKEN", strings.Repeat("secret", 8))
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_ID", "production-delivery-controller")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_VERSION", "2026.07.20+qualified")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_TRUST_KEY_DIGEST", "sha256:"+strings.Repeat("a", 64))
	t.Setenv("QUALIFICATION_RELEASE_POSTGRES_DSN", "postgres://worksflow:worksflow@localhost:5432/worksflow?sslmode=disable")
	t.Setenv("QUALIFICATION_RELEASE_LEASE_DURATION", "5m")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "credential distinct") ||
		!strings.Contains(err.Error(), "concurrency, polling, lease") {
		t.Fatalf("shared credential or unbounded lease was accepted: %v", err)
	}
}

func TestWorkflowQualificationActivationRequiresDedicatedOperatorAndV3Runtime(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("WORKFLOW_QUALIFICATION_ACTIVATION_WORKER_ENABLED", "true")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "WORKFLOW_PROFILE_V3_RUNTIME_ENABLED") ||
		!strings.Contains(err.Error(), "WORKFLOW_INPUT_AUTHORITY_OPERATOR_POSTGRES_DSN") {
		t.Fatalf("incomplete workflow qualification activation was accepted: %v", err)
	}

	t.Setenv("WORKFLOW_PROFILE_V3_RUNTIME_ENABLED", "true")
	t.Setenv("QUALIFICATION_RELEASE_PUBLISHER_ENABLED", "true")
	t.Setenv("QUALIFICATION_RELEASE_POSTGRES_DSN", "postgres://qualified_release:secret@localhost:5432/worksflow?sslmode=disable")
	t.Setenv("RELEASE_DELIVERY_WORKER_ENABLED", "true")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_URL", "https://delivery-controller.example.test")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_TOKEN", strings.Repeat("secret", 8))
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_ID", "production-delivery-controller")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_VERSION", "2026.07.20+qualified")
	t.Setenv("RELEASE_DELIVERY_CONTROLLER_TRUST_KEY_DIGEST", "sha256:"+strings.Repeat("a", 64))
	t.Setenv("WORKFLOW_INPUT_AUTHORITY_OPERATOR_POSTGRES_DSN", "postgres://workflow_input_operator:secret@localhost:5432/worksflow?sslmode=disable")
	loaded, err := Load()
	if err != nil {
		t.Fatalf("closed workflow qualification activation chain was rejected: %v", err)
	}
	if !loaded.WorkflowQualificationActivation.WorkerEnabled ||
		loaded.WorkflowQualificationActivation.MaxOpenConns != 10 ||
		loaded.WorkflowQualificationActivation.MaxIdleConns != 4 ||
		loaded.WorkflowQualificationActivation.MaxTransactionRetries != 3 {
		t.Fatalf("workflow qualification activation config = %#v", loaded.WorkflowQualificationActivation)
	}
}

func TestWorkflowQualificationActivationRejectsSharedCredentialOrNarrowPool(t *testing.T) {
	for name, configure := range map[string]func(*testing.T){
		"shared credential": func(t *testing.T) {
			t.Setenv("WORKFLOW_INPUT_AUTHORITY_OPERATOR_POSTGRES_DSN", "postgres://worksflow:worksflow@localhost:5432/worksflow?sslmode=disable")
		},
		"narrow pool": func(t *testing.T) {
			t.Setenv("WORKFLOW_INPUT_AUTHORITY_OPERATOR_POSTGRES_DSN", "postgres://workflow_input_operator:secret@localhost:5432/worksflow?sslmode=disable")
			t.Setenv("WORKFLOW_INPUT_AUTHORITY_OPERATOR_POSTGRES_MAX_OPEN_CONNS", "8")
		},
		"different authority database": func(t *testing.T) {
			t.Setenv("WORKFLOW_INPUT_AUTHORITY_OPERATOR_POSTGRES_DSN", "postgres://workflow_input_operator:secret@localhost:5432/another?sslmode=disable")
		},
	} {
		t.Run(name, func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv("WORKFLOW_QUALIFICATION_ACTIVATION_WORKER_ENABLED", "true")
			t.Setenv("WORKFLOW_PROFILE_V3_RUNTIME_ENABLED", "true")
			t.Setenv("QUALIFICATION_RELEASE_PUBLISHER_ENABLED", "true")
			t.Setenv("QUALIFICATION_RELEASE_POSTGRES_DSN", "postgres://qualified_release:secret@localhost:5432/worksflow?sslmode=disable")
			t.Setenv("RELEASE_DELIVERY_WORKER_ENABLED", "true")
			t.Setenv("RELEASE_DELIVERY_CONTROLLER_URL", "https://delivery-controller.example.test")
			t.Setenv("RELEASE_DELIVERY_CONTROLLER_TOKEN", strings.Repeat("secret", 8))
			t.Setenv("RELEASE_DELIVERY_CONTROLLER_ID", "production-delivery-controller")
			t.Setenv("RELEASE_DELIVERY_CONTROLLER_VERSION", "2026.07.20+qualified")
			t.Setenv("RELEASE_DELIVERY_CONTROLLER_TRUST_KEY_DIGEST", "sha256:"+strings.Repeat("a", 64))
			configure(t)
			if _, err := Load(); err == nil ||
				(!strings.Contains(err.Error(), "credential distinct") &&
					!strings.Contains(err.Error(), "pool limits") &&
					!strings.Contains(err.Error(), "same PostgreSQL endpoint")) {
				t.Fatalf("unsafe activation configuration was accepted: %v", err)
			}
		})
	}
}

func TestSamePostgresAuthorityScopeIgnoresOnlyCredentialAndClientTuning(t *testing.T) {
	application := "postgres://application:one@DB.EXAMPLE.test/worksflow?sslmode=verify-full&sslrootcert=%2Fetc%2Fca.pem&target_session_attrs=primary&application_name=api"
	operator := "postgresql://operator:two@db.example.test:5432/worksflow?sslmode=verify-full&sslrootcert=%2Fetc%2Fca.pem&target_session_attrs=primary&application_name=activation&connect_timeout=5"
	if !samePostgresAuthorityScope(application, operator) {
		t.Fatal("credential-only PostgreSQL DSN variation changed the authority scope")
	}
	for name, candidate := range map[string]string{
		"host":     "postgres://operator:two@other.example.test/worksflow?sslmode=verify-full&sslrootcert=%2Fetc%2Fca.pem&target_session_attrs=primary",
		"database": "postgres://operator:two@db.example.test/other?sslmode=verify-full&sslrootcert=%2Fetc%2Fca.pem&target_session_attrs=primary",
		"tls mode": "postgres://operator:two@db.example.test/worksflow?sslmode=require&sslrootcert=%2Fetc%2Fca.pem&target_session_attrs=primary",
		"trust":    "postgres://operator:two@db.example.test/worksflow?sslmode=verify-full&sslrootcert=%2Fetc%2Fother.pem&target_session_attrs=primary",
		"target":   "postgres://operator:two@db.example.test/worksflow?sslmode=verify-full&sslrootcert=%2Fetc%2Fca.pem&target_session_attrs=any",
	} {
		t.Run(name, func(t *testing.T) {
			if samePostgresAuthorityScope(application, candidate) {
				t.Fatal("different PostgreSQL authority scope was accepted")
			}
		})
	}
}

func TestAgentRequiresFullyQualifiedImmutableExecutorWhenEnabled(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("AGENT_ENABLED", "true")
	t.Setenv("AGENT_RUNNER_IMAGE", "worksflow/codex-agent:latest")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AGENT_RUNNER_IMAGE") ||
		!strings.Contains(err.Error(), "AGENT_OUTPUT_SCHEMA_HASH") {
		t.Fatalf("incomplete Agent executor was accepted: %v", err)
	}

	hash := "sha256:" + strings.Repeat("a", 64)
	t.Setenv("AGENT_RUNNER_IMAGE", "worksflow/codex-agent@sha256:"+strings.Repeat("b", 64))
	t.Setenv("AGENT_MODEL_POLICY_HASH", hash)
	t.Setenv("AGENT_PARAMETERS_HASH", hash)
	t.Setenv("AGENT_PROMPT_HASH", hash)
	t.Setenv("AGENT_OUTPUT_SCHEMA_HASH", hash)
	t.Setenv("AGENT_TOOLCHAIN_HASH", hash)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("qualified Agent executor was rejected: %v", err)
	}
	if !cfg.Agent.Enabled || cfg.Agent.WorkerEnabled || cfg.Agent.Model != "gpt-5" {
		t.Fatalf("qualified Agent config = %#v", cfg.Agent)
	}
}

func TestAgentRejectsExecutorIdentityThatDoesNotMatchRuntime(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{name: "adapter", key: "AGENT_EXECUTOR_ADAPTER", value: "custom-runner", want: "AGENT_EXECUTOR_ADAPTER must be codex-cli"},
		{name: "provider", key: "AGENT_EXECUTOR_PROVIDER", value: "anthropic", want: "AGENT_EXECUTOR_PROVIDER must be openai"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearConfigEnvironment(t)
			hash := "sha256:" + strings.Repeat("a", 64)
			t.Setenv("AGENT_ENABLED", "true")
			t.Setenv("AGENT_RUNNER_IMAGE", "worksflow/codex-agent@sha256:"+strings.Repeat("b", 64))
			t.Setenv("AGENT_MODEL_POLICY_HASH", hash)
			t.Setenv("AGENT_PARAMETERS_HASH", hash)
			t.Setenv("AGENT_PROMPT_HASH", hash)
			t.Setenv("AGENT_OUTPUT_SCHEMA_HASH", hash)
			t.Setenv("AGENT_TOOLCHAIN_HASH", hash)
			t.Setenv(test.key, test.value)

			if _, err := Load(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("mismatched Agent %s identity was accepted: %v", test.name, err)
			}
		})
	}
}

func TestAgentWorkerCannotRunWithoutControlPlane(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("AGENT_WORKER_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AGENT_WORKER_ENABLED requires AGENT_ENABLED") {
		t.Fatalf("orphan Agent worker was accepted: %v", err)
	}
}

func TestAgentWorkerRequiresQualifiedGatewayAndProviderSecret(t *testing.T) {
	clearConfigEnvironment(t)
	hash := "sha256:" + strings.Repeat("a", 64)
	t.Setenv("AGENT_ENABLED", "true")
	t.Setenv("AGENT_WORKER_ENABLED", "true")
	t.Setenv("AGENT_RUNNER_IMAGE", "worksflow/codex-agent@sha256:"+strings.Repeat("b", 64))
	t.Setenv("AGENT_MODEL_POLICY_HASH", hash)
	t.Setenv("AGENT_PARAMETERS_HASH", hash)
	t.Setenv("AGENT_PROMPT_HASH", hash)
	t.Setenv("AGENT_OUTPUT_SCHEMA_HASH", hash)
	t.Setenv("AGENT_TOOLCHAIN_HASH", hash)
	t.Setenv("AGENT_WALL_TIME", "4m")
	t.Setenv("AGENT_MODEL_GATEWAY_BASE_URL", "http://localhost:8080/internal/agent-model/v1")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AGENT_MODEL_GATEWAY_BASE_URL") ||
		!strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("unreachable or uncredentialed Model Gateway was accepted: %v", err)
	}

	t.Setenv("AGENT_MODEL_GATEWAY_BASE_URL", "http://worksflow-agent-gateway:8080/internal/agent-model/v1")
	t.Setenv("OPENAI_API_KEY", "platform-provider-secret")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("qualified Agent worker was rejected: %v", err)
	}
	if !cfg.Agent.WorkerEnabled || cfg.Agent.GatewayRedisPrefix != "worksflow:agent:model:" ||
		cfg.Agent.RunnerPIDs != 256 || cfg.Agent.RunnerOutputMax != 8<<20 {
		t.Fatalf("qualified Agent worker config = %#v", cfg.Agent)
	}
}

func TestInteractiveSandboxRequiresImmutableRunnerWhenEnabled(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("SANDBOX_ENABLED", "true")
	t.Setenv("SANDBOX_RUNNER_IMAGE", "worksflow/codex-runner:latest")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SANDBOX_RUNNER_IMAGE") {
		t.Fatalf("mutable interactive runner was accepted: %v", err)
	}
	t.Setenv("SANDBOX_RUNNER_IMAGE", "worksflow/codex-runner@sha256:"+strings.Repeat("c", 64))
	if _, err := Load(); err != nil {
		t.Fatalf("digest-pinned interactive runner was rejected: %v", err)
	}
}

func TestLSPRequiresExactSandboxAndBoundedLocalRuntime(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("LSP_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "LSP_ENABLED requires SANDBOX_ENABLED") {
		t.Fatalf("LSP without exact SandboxSession workspace was accepted: %v", err)
	}

	t.Setenv("SANDBOX_ENABLED", "true")
	t.Setenv("SANDBOX_RUNNER_IMAGE", "worksflow/codex-runner@sha256:"+strings.Repeat("c", 64))
	t.Setenv("LSP_ALLOWED_ORIGINS", "https://builder.example.test")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("bounded LSP configuration was rejected: %v", err)
	}
	if !cfg.LSP.Enabled || cfg.LSP.RequireTLS || cfg.LSP.RuntimeBinary != "/usr/bin/docker" {
		t.Fatalf("bounded development LSP config = %#v", cfg.LSP)
	}

	t.Setenv("LSP_DAEMON_HOST", "tcp://runtime.example.test:2376")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "LSP_DAEMON_HOST") {
		t.Fatalf("remote or credential-bearing LSP daemon was accepted: %v", err)
	}
	t.Setenv("LSP_DAEMON_HOST", "unix:///var/run/docker.sock")
	t.Setenv("LSP_ALLOWED_ORIGINS", "http://builder.example.test")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "LSP_ALLOWED_ORIGINS") {
		t.Fatalf("non-local cleartext LSP Origin was accepted: %v", err)
	}
	t.Setenv("LSP_ALLOWED_ORIGINS", "https://builder.example.test")
	t.Setenv("LSP_TICKET_TTL", "31s")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "LSP ticket") {
		t.Fatalf("unbounded LSP ticket was accepted: %v", err)
	}
}

func TestSharedLSPCannotDisableTLS(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("APP_ENV", EnvironmentProduction)
	t.Setenv("PLATFORM_ENCRYPTION_KEY", strings.Repeat("1", 64))
	t.Setenv("DELIVERY_SANDBOX_NODE_IMAGE", "node:22-alpine@sha256:"+strings.Repeat("a", 64))
	t.Setenv("DELIVERY_SANDBOX_GO_IMAGE", "golang:1.22-alpine@sha256:"+strings.Repeat("b", 64))
	t.Setenv("SANDBOX_ENABLED", "true")
	t.Setenv("SANDBOX_RUNNER_IMAGE", "worksflow/codex-runner@sha256:"+strings.Repeat("c", 64))
	t.Setenv("LSP_ENABLED", "true")
	t.Setenv("LSP_ALLOWED_ORIGINS", "https://builder.example.test")
	t.Setenv("LSP_REQUIRE_TLS", "false")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "LSP_REQUIRE_TLS") {
		t.Fatalf("shared LSP without TLS was accepted: %v", err)
	}
	t.Setenv("LSP_REQUIRE_TLS", "true")
	if _, err := Load(); err != nil {
		t.Fatalf("TLS-fenced shared LSP was rejected: %v", err)
	}
}

func TestVerificationWorkerRequiresBoundedFencedRuntime(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("VERIFICATION_WORKER_ENABLED", "true")
	t.Setenv("VERIFICATION_HEARTBEAT", "2m")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "verification poll, lease, and heartbeat") {
		t.Fatalf("unfenced Verification worker was accepted: %v", err)
	}
	t.Setenv("VERIFICATION_HEARTBEAT", "15s")
	t.Setenv("VERIFICATION_DAEMON_HOST", "tcp://sandbox:2375")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("bounded Verification worker was rejected: %v", err)
	}
	if !cfg.Verification.WorkerEnabled || cfg.Verification.PIDs != 256 {
		t.Fatalf("Verification worker config = %#v", cfg.Verification)
	}
	t.Setenv("VERIFICATION_RUNTIME", "podman")
	t.Setenv("VERIFICATION_DAEMON_HOST", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "VERIFICATION_DAEMON_HOST") {
		t.Fatalf("Podman verification without an explicit daemon was accepted: %v", err)
	}
	t.Setenv("VERIFICATION_DAEMON_HOST", "unix:///run/podman/podman.sock")
	if _, err := Load(); err != nil {
		t.Fatalf("explicit Podman verification daemon was rejected: %v", err)
	}
}

func TestSandboxPreviewOriginAndGatewayBoundaryFailClosed(t *testing.T) {
	clearConfigEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Sandbox.GatewayBindAddress = "0.0.0.0"
	cfg.Sandbox.DaemonHost = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "GATEWAY_BIND_ADDRESS") {
		t.Fatalf("unisolated wildcard gateway binding was accepted: %v", err)
	}
	cfg.Sandbox.DaemonHost = "tcp://sandbox:2375"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit isolated daemon gateway was rejected: %v", err)
	}
	cfg.Security.Session.CookieDomain = ".localhost"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "SESSION_COOKIE_DOMAIN") {
		t.Fatalf("preview under platform cookie domain was accepted: %v", err)
	}
	cfg.Security.Session.CookieDomain = ""
	cfg.Sandbox.PreviewPublicOrigin = "https://preview.example/path"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "PREVIEW_PUBLIC_ORIGIN") {
		t.Fatalf("non-origin preview URL was accepted: %v", err)
	}
}

func TestProductionRequiresExplicitStartupMutations(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("APP_ENV", EnvironmentProduction)
	t.Setenv("PLATFORM_ENCRYPTION_KEY", "1111111111111111111111111111111111111111111111111111111111111111")
	t.Setenv("DELIVERY_SANDBOX_NODE_IMAGE", "node:22-alpine@sha256:"+strings.Repeat("a", 64))
	t.Setenv("DELIVERY_SANDBOX_GO_IMAGE", "golang:1.22-alpine@sha256:"+strings.Repeat("b", 64))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Startup.EnsureMongoIndexes || cfg.Startup.EnsureNATSStream {
		t.Fatalf("production startup defaults = %#v, outbox = %#v", cfg.Startup, cfg.Outbox)
	}
	if !cfg.Outbox.Enabled {
		t.Fatal("the embedded outbox worker must default on unless a separately deployed worker is configured")
	}
	if !cfg.Security.Session.CookieSecure {
		t.Fatal("production session cookie must default to secure")
	}
}

func TestStartupMigrateIsRetiredInEveryEnvironment(t *testing.T) {
	for _, environment := range []string{EnvironmentDevelopment, EnvironmentStaging, EnvironmentProduction} {
		t.Run(environment, func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv("APP_ENV", environment)
			t.Setenv("STARTUP_MIGRATE", "false")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "STARTUP_MIGRATE is retired") ||
				!strings.Contains(err.Error(), "WORKSFLOW_MIGRATION_POSTGRES_DSN") {
				t.Fatalf("retired API migration switch was accepted in %s: %v", environment, err)
			}
		})
	}
}

func TestPostgresDSNRejectsQueryCredentialsAndIdentityOverridesWithoutEcho(t *testing.T) {
	for _, valid := range []string{
		"postgres://api:secret@postgres:5432/worksflow?sslmode=disable",
		"postgresql://api@postgres/worksflow?sslmode=verify-full&sslrootcert=%2Fetc%2Fworksflow%2Fca.pem&connect_timeout=10&application_name=worksflow-api",
	} {
		if err := validatePostgresDSN(valid); err != nil {
			t.Fatalf("valid PostgreSQL DSN rejected: %v", err)
		}
	}

	for _, key := range []string{
		"password", "sslpassword", "passfile", "service", "servicefile",
		"options", "role", "session_authorization", "search_path",
		"user", "host", "port", "database", "dbname",
	} {
		t.Run(key, func(t *testing.T) {
			secret := "api-secret-" + key
			raw := "postgres://api@postgres/worksflow?sslmode=disable&" + key + "=" + secret
			err := validatePostgresDSN(raw)
			if err == nil || !strings.Contains(err.Error(), "unsupported query parameter") {
				t.Fatalf("unsafe PostgreSQL DSN accepted: %v", err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), raw) {
				t.Fatalf("PostgreSQL DSN error exposed a query value: %v", err)
			}
		})
	}
}

func TestPostgresSchemaIsSeparateAndCanonical(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("POSTGRES_SCHEMA", "tenant_app")
	cfg, err := Load()
	if err != nil || cfg.Postgres.Schema != "tenant_app" {
		t.Fatalf("canonical PostgreSQL schema rejected: %#v, %v", cfg.Postgres, err)
	}
	for _, schema := range []string{
		"", "Public", "tenant-app", "tenant,public", "pg_catalog", "pg_temp",
		"information_schema", strings.Repeat("a", 64),
	} {
		cfg.Postgres.Schema = schema
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "POSTGRES_SCHEMA") {
			t.Fatalf("invalid PostgreSQL schema %q accepted: %v", schema, err)
		}
	}
}

func TestSharedEnvironmentsRequireDigestPinnedSandboxImages(t *testing.T) {
	clearConfigEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, environment := range []string{EnvironmentStaging, EnvironmentProduction} {
		cfg.Environment = environment
		cfg.Delivery.SandboxNodeImage = "node:22-alpine"
		cfg.Delivery.SandboxGoImage = "golang:1.22-alpine"
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "@sha256") {
			t.Fatalf("%s accepted mutable sandbox tags: %v", environment, err)
		}
		cfg.Delivery.SandboxNodeImage = "node:22-alpine@sha256:" + strings.Repeat("a", 64)
		cfg.Delivery.SandboxGoImage = "golang:1.22-alpine@sha256:" + strings.Repeat("b", 64)
		if err := cfg.Validate(); err != nil && strings.Contains(err.Error(), "@sha256") {
			t.Fatalf("%s rejected digest-pinned sandbox images: %v", environment, err)
		}
	}
}

func TestProductionRequiresExplicitEncryptionKey(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("APP_ENV", EnvironmentProduction)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PLATFORM_ENCRYPTION_KEY") {
		t.Fatalf("Load() error = %v, want explicit encryption key failure", err)
	}
}

func TestStagingRequiresExplicitEncryptionKey(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("APP_ENV", EnvironmentStaging)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PLATFORM_ENCRYPTION_KEY") {
		t.Fatalf("Load() error = %v, want explicit staging encryption key failure", err)
	}
}

func TestLoadReportsInvalidEnvironmentValue(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("HTTP_PORT", "not-a-number")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "HTTP_PORT") {
		t.Fatalf("Load() error = %v, want HTTP_PORT validation error", err)
	}
}

func TestValidateRejectsCredentialedWildcardCORS(t *testing.T) {
	clearConfigEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cfg.CORS.AllowedOrigins = []string{"*"}
	cfg.CORS.AllowCredentials = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "wildcard") {
		t.Fatalf("Validate() error = %v, want wildcard validation error", err)
	}
}

func TestValidateRejectsAnonymousWebSocketInProduction(t *testing.T) {
	clearConfigEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cfg.Environment = EnvironmentProduction
	cfg.WebSocket.AllowAnonymous = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "anonymous WebSocket") {
		t.Fatalf("Validate() error = %v, want anonymous WebSocket validation error", err)
	}
}

func TestDeliveryResolverConfigurationFailsClosed(t *testing.T) {
	clearConfigEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Delivery.ResolverNetwork = "host"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "resolver network") {
		t.Fatalf("host resolver network was accepted: %v", err)
	}
	cfg.Delivery.ResolverNetwork = "bridge"
	cfg.Delivery.ResolverNPMRegistry = "https://user:password@registry.npmjs.org"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "credential-free") {
		t.Fatalf("credentialed npm resolver was accepted: %v", err)
	}
	cfg.Delivery.ResolverNPMRegistry = "https://registry.npmjs.org"
	cfg.Delivery.ResolverGoProxy = "https://proxy.golang.org,direct"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "fallback") {
		t.Fatalf("Go direct fallback resolver was accepted: %v", err)
	}
}

func TestLoadOpenAICompatibilityAliases(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("OPENAI_BASE_URL", "https://gateway.example")
	t.Setenv("OPENAI_RESPONSES_URL", "")
	t.Setenv("OPENAI_DEFAULT_MODEL", "gateway-model")
	t.Setenv("AI_DEFAULT_MODEL", "legacy-model")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.BaseURL != "https://gateway.example/v1/responses" {
		t.Fatalf("AI BaseURL = %q", cfg.AI.BaseURL)
	}
	if cfg.AI.DefaultModel != "gateway-model" {
		t.Fatalf("AI DefaultModel = %q", cfg.AI.DefaultModel)
	}

	t.Setenv("OPENAI_RESPONSES_URL", "https://responses.example/custom")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.BaseURL != "https://responses.example/custom" {
		t.Fatalf("explicit AI BaseURL = %q", cfg.AI.BaseURL)
	}
}

func TestLoadValidatesGitHubAppConfigurationAsAUnit(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("GITHUB_APP_ID", "123")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "must be configured together") {
		t.Fatalf("partial GitHub App configuration error = %v", err)
	}

	t.Setenv("GITHUB_APP_INSTALLATION_ID", "456")
	t.Setenv("GITHUB_APP_ORGANIZATION", "ai-worksflow")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_FILE", "/run/secrets/github-app.pem")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GitHub.PlatformAppEnabled() {
		t.Fatalf("GitHub App configuration was not enabled: %#v", cfg.GitHub)
	}
}

func clearConfigEnvironment(t *testing.T) {
	t.Helper()
	keys := []string{
		"APP_ENV", "SERVICE_NAME", "HTTP_HOST", "HTTP_PORT", "HTTP_READ_TIMEOUT",
		"HTTP_READ_HEADER_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT",
		"HTTP_SHUTDOWN_TIMEOUT", "HTTP_MAX_HEADER_BYTES", "HTTP_TRUSTED_PROXIES",
		"LOG_LEVEL", "LOG_FORMAT", "CORS_ALLOWED_ORIGINS", "CORS_ALLOWED_METHODS",
		"CORS_ALLOWED_HEADERS", "CORS_EXPOSED_HEADERS", "CORS_ALLOW_CREDENTIALS",
		"CORS_MAX_AGE", "SECURITY_ENABLE_HSTS", "DEPENDENCY_CONNECT_TIMEOUT",
		"DEPENDENCY_READINESS_TIMEOUT", "POSTGRES_DSN", "POSTGRES_SCHEMA", "POSTGRES_MAX_OPEN_CONNS",
		"POSTGRES_MAX_IDLE_CONNS", "POSTGRES_CONN_MAX_LIFETIME",
		"POSTGRES_CONN_MAX_IDLE_TIME", "REDIS_ADDRESS", "REDIS_PASSWORD", "REDIS_DB",
		"REDIS_USE_TLS", "MONGO_URI", "MONGO_DATABASE", "NATS_URL", "NATS_CLIENT_NAME",
		"WS_ALLOWED_ORIGINS", "WS_ALLOW_ANONYMOUS", "WS_WRITE_WAIT", "WS_PONG_WAIT",
		"WS_PING_PERIOD", "WS_MAX_MESSAGE_BYTES", "WS_SEND_BUFFER", "WS_MAX_REPLAY_EVENTS",
		"HTTP_MAX_JSON_BODY_BYTES", "SESSION_TTL", "SESSION_CACHE_PREFIX", "SESSION_COOKIE_NAME",
		"SESSION_COOKIE_PATH", "SESSION_COOKIE_DOMAIN", "SESSION_COOKIE_SECURE", "SESSION_COOKIE_SAME_SITE",
		"CSRF_COOKIE_NAME", "CSRF_HEADER_NAME", "CSRF_TOKEN_BYTES", "ARGON2_MEMORY_KIB",
		"ARGON2_ITERATIONS", "ARGON2_PARALLELISM", "ARGON2_SALT_BYTES", "ARGON2_KEY_BYTES",
		"WS_AUTH_TIMEOUT", "WS_MAX_SUBSCRIPTIONS", "STARTUP_MIGRATE", "STARTUP_ENSURE_MONGO_INDEXES",
		"STARTUP_ENSURE_NATS_STREAM", "STARTUP_TIMEOUT", "CONTENT_MAX_BYTES", "CONTENT_RECONCILE_ENABLED",
		"CONTENT_RECONCILE_INTERVAL", "CONTENT_PENDING_GRACE", "CONTENT_ORPHAN_TTL", "CONTENT_RECONCILE_BATCH", "OUTBOX_ENABLED",
		"OUTBOX_BATCH_SIZE", "OUTBOX_POLL_INTERVAL", "OUTBOX_CLAIM_TTL", "OUTBOX_MAX_ATTEMPTS",
		"OUTBOX_PUBLISH_WAIT",
		"IDEMPOTENCY_TTL", "IDEMPOTENCY_LOCK_TTL", "IDEMPOTENCY_MAX_RESPONSE_BYTES",
		"AI_PROVIDER", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_RESPONSES_URL", "OPENAI_DEFAULT_MODEL", "AI_DEFAULT_MODEL", "AI_TIMEOUT",
		"AI_MAX_INPUT_BYTES", "AI_MAX_OUTPUT_BYTES", "AI_MAX_RETRIES", "OPENAI_ORGANIZATION", "OPENAI_PROJECT",
		"WORKFLOW_WORKER_ENABLED", "WORKFLOW_WORKER_ID", "WORKFLOW_POLL_INTERVAL", "WORKFLOW_LEASE_DURATION",
		"WORKFLOW_HEARTBEAT", "WORKFLOW_PROFILE_V3_RUNTIME_ENABLED",
		"WORKFLOW_QUALIFICATION_ACTIVATION_WORKER_ENABLED", "WORKFLOW_INPUT_AUTHORITY_OPERATOR_POSTGRES_DSN",
		"WORKFLOW_QUALIFICATION_ACTIVATION_MAX_TRANSACTION_RETRIES",
		"WORKFLOW_INPUT_AUTHORITY_OPERATOR_POSTGRES_MAX_OPEN_CONNS",
		"WORKFLOW_INPUT_AUTHORITY_OPERATOR_POSTGRES_MAX_IDLE_CONNS",
		"QUALIFICATION_RELEASE_PUBLISHER_ENABLED", "QUALIFICATION_RELEASE_POSTGRES_DSN",
		"QUALIFICATION_RELEASE_WORKER_ID", "QUALIFICATION_RELEASE_WORKER_CONCURRENCY",
		"QUALIFICATION_RELEASE_SCHEDULER_POLL_INTERVAL", "QUALIFICATION_RELEASE_CONTROLLER_POLL_INTERVAL",
		"QUALIFICATION_RELEASE_LEASE_DURATION", "QUALIFICATION_RELEASE_MAX_TRANSACTION_RETRIES",
		"QUALIFICATION_RELEASE_POSTGRES_MAX_OPEN_CONNS", "QUALIFICATION_RELEASE_POSTGRES_MAX_IDLE_CONNS",
		"PLATFORM_ENCRYPTION_KEY", "GITHUB_API_BASE_URL", "GITHUB_REQUEST_TIMEOUT", "GITHUB_CREDENTIAL_TTL", "GITHUB_REDIS_PREFIX",
		"GITHUB_APP_ID", "GITHUB_APP_INSTALLATION_ID", "GITHUB_APP_ORGANIZATION", "GITHUB_APP_PRIVATE_KEY_FILE",
		"TEMPLATE_SOURCE_GIT_BINARY", "TEMPLATE_SOURCE_CACHE_ROOT", "TEMPLATE_SOURCE_ALLOWED_HOSTS", "TEMPLATE_SOURCE_FETCH_TIMEOUT",
		"REPOSITORY_SEARCH_INDEX_MAX_TREES", "REPOSITORY_SEARCH_INDEX_MAX_SOURCE_BYTES",
		"REPOSITORY_SEARCH_INDEX_MAX_ACTIVE_BUILDS", "REPOSITORY_SEARCH_RATE_REDIS_PREFIX",
		"REPOSITORY_SEARCH_RATE_TIMEOUT", "REPOSITORY_SEARCH_QUERY_PROJECT_REFILL_TOKENS",
		"REPOSITORY_SEARCH_QUERY_PROJECT_REFILL_INTERVAL", "REPOSITORY_SEARCH_QUERY_PROJECT_BURST",
		"REPOSITORY_SEARCH_QUERY_ACTOR_REFILL_TOKENS", "REPOSITORY_SEARCH_QUERY_ACTOR_REFILL_INTERVAL",
		"REPOSITORY_SEARCH_QUERY_ACTOR_BURST", "REPOSITORY_SEARCH_BUILD_PROJECT_REFILL_TOKENS",
		"REPOSITORY_SEARCH_BUILD_PROJECT_REFILL_INTERVAL", "REPOSITORY_SEARCH_BUILD_PROJECT_BURST",
		"REPOSITORY_SEARCH_BUILD_ACTOR_REFILL_TOKENS", "REPOSITORY_SEARCH_BUILD_ACTOR_REFILL_INTERVAL",
		"REPOSITORY_SEARCH_BUILD_ACTOR_BURST",
		"DELIVERY_SANDBOX_RUNTIME", "DELIVERY_SANDBOX_HOST", "DELIVERY_SANDBOX_NODE_IMAGE", "DELIVERY_SANDBOX_GO_IMAGE",
		"DELIVERY_SANDBOX_TIMEOUT", "DELIVERY_SANDBOX_OUTPUT_MAX_BYTES", "DELIVERY_SANDBOX_MEMORY",
		"DELIVERY_SANDBOX_CPUS", "DELIVERY_SANDBOX_PIDS", "DELIVERY_QUALITY_TEMP_ROOT",
		"DELIVERY_RESOLVER_NETWORK", "DELIVERY_RESOLVER_NPM_REGISTRY", "DELIVERY_RESOLVER_GO_PROXY",
		"DELIVERY_RESOLVER_GO_SUMDB", "DELIVERY_RESOLVER_TIMEOUT", "DELIVERY_RESOLVER_OUTPUT_MAX_BYTES",
		"DELIVERY_RESOLVER_MEMORY", "DELIVERY_RESOLVER_CPUS", "DELIVERY_RESOLVER_PIDS",
		"DELIVERY_PUBLISH_ROOT", "DELIVERY_PUBLISH_BASE_URL",
		"RELEASE_DELIVERY_WORKER_ENABLED", "RELEASE_DELIVERY_WORKER_ID",
		"RELEASE_DELIVERY_CONTROLLER_URL", "RELEASE_DELIVERY_CONTROLLER_TOKEN",
		"RELEASE_DELIVERY_CONTROLLER_ID", "RELEASE_DELIVERY_CONTROLLER_VERSION",
		"RELEASE_DELIVERY_CONTROLLER_PROTOCOL", "RELEASE_DELIVERY_CONTROLLER_TRUST_KEY_DIGEST",
		"RELEASE_DELIVERY_LEASE_DURATION", "RELEASE_DELIVERY_POLL_INTERVAL",
		"RELEASE_DELIVERY_RECONCILE_DELAY",
		"RELEASE_DELIVERY_REQUEST_TIMEOUT", "RELEASE_DELIVERY_RESPONSE_MAX_BYTES",
		"SANDBOX_ENABLED", "SANDBOX_RUNNER_IMAGE", "SANDBOX_CPU_MILLIS", "SANDBOX_MEMORY_BYTES",
		"SANDBOX_RUNTIME", "SANDBOX_DAEMON_HOST", "SANDBOX_WORKSPACE_ROOT", "SANDBOX_STARTUP_TIMEOUT",
		"SANDBOX_GATEWAY_NETWORK",
		"SANDBOX_GATEWAY_BIND_ADDRESS", "SANDBOX_PREVIEW_DIAL_HOST", "SANDBOX_PREVIEW_PUBLIC_ORIGIN",
		"SANDBOX_PREVIEW_TICKET_TTL", "SANDBOX_PREVIEW_PROBE_TIMEOUT",
		"SANDBOX_COMMAND_TIMEOUT", "SANDBOX_RUNTIME_OUTPUT_MAX_BYTES",
		"SANDBOX_WORKSPACE_BYTES", "SANDBOX_PID_LIMIT", "SANDBOX_PREVIEW_PORT_LIMIT",
		"SANDBOX_IDLE_HIBERNATE_AFTER", "SANDBOX_MAX_RUNTIME",
		"SANDBOX_LIFECYCLE_WORKER_ID", "SANDBOX_LIFECYCLE_POLL_INTERVAL",
		"SANDBOX_LIFECYCLE_LEASE_DURATION", "SANDBOX_LIFECYCLE_RETRY_DELAY",
		"SANDBOX_CONNECTION_TICKET_TTL", "SANDBOX_REDIS_PREFIX",
		"SANDBOX_STREAM_MAX_EVENTS", "SANDBOX_STREAM_RETENTION",
		"LSP_ENABLED", "LSP_RUNTIME_BINARY", "LSP_DAEMON_HOST", "LSP_ALLOWED_ORIGINS",
		"LSP_REQUIRE_TLS", "LSP_TICKET_TTL", "LSP_BIND_TIMEOUT", "LSP_WRITE_WAIT",
		"LSP_COMMAND_TIMEOUT", "LSP_CLI_OUTPUT_MAX_BYTES", "LSP_REDIS_PREFIX",
		"LSP_RATE_REFILL_PER_SECOND", "LSP_RATE_BURST",
		"VERIFICATION_WORKER_ENABLED", "VERIFICATION_WORKER_ID", "VERIFICATION_RUNTIME",
		"VERIFICATION_DAEMON_HOST", "VERIFICATION_WORKSPACE_ROOT", "VERIFICATION_MEMORY",
		"VERIFICATION_CPUS", "VERIFICATION_PIDS", "VERIFICATION_OUTPUT_MAX_BYTES",
		"VERIFICATION_TEMP_BYTES", "VERIFICATION_POLL_INTERVAL", "VERIFICATION_LEASE_DURATION",
		"VERIFICATION_HEARTBEAT",
		"AGENT_ENABLED", "AGENT_WORKER_ENABLED", "AGENT_WORKER_ID", "AGENT_EXECUTOR_PROFILE_ID", "AGENT_EXECUTOR_ADAPTER",
		"AGENT_EXECUTOR_PROVIDER", "AGENT_EXECUTOR_MODEL", "AGENT_RUNNER_IMAGE", "AGENT_MODEL_POLICY_HASH",
		"AGENT_PARAMETERS_HASH", "AGENT_PROMPT_HASH", "AGENT_OUTPUT_SCHEMA_HASH", "AGENT_TOOLCHAIN_HASH",
		"AGENT_RUNTIME", "AGENT_DAEMON_HOST", "AGENT_WORKTREE_ROOT", "AGENT_RUNNER_NETWORK",
		"AGENT_RUNNER_MEMORY", "AGENT_RUNNER_CPUS", "AGENT_RUNNER_PIDS", "AGENT_RUNNER_OUTPUT_MAX_BYTES",
		"AGENT_MODEL_GATEWAY_BASE_URL", "AGENT_MODEL_GATEWAY_REDIS_PREFIX",
		"AGENT_ALLOWED_TOOLS", "AGENT_WALL_TIME", "AGENT_MAX_INPUT_TOKENS", "AGENT_MAX_OUTPUT_TOKENS",
		"AGENT_MAX_COMMANDS", "AGENT_MAX_LOG_BYTES", "AGENT_MAX_PATCH_BYTES", "AGENT_MAX_CONTEXT_FILES",
		"AGENT_CLAIM_BATCH", "AGENT_POLL_INTERVAL", "AGENT_LEASE_DURATION", "AGENT_HEARTBEAT",
	}
	for _, key := range keys {
		previous, existed := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		key := key
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(key, previous)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
