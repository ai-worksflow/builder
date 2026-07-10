package config

import (
	"os"
	"strings"
	"testing"
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
	if !cfg.Startup.Migrate || !cfg.Startup.EnsureMongoIndexes || !cfg.Startup.EnsureNATSStream || !cfg.Outbox.Enabled {
		t.Fatalf("development startup defaults = %#v, outbox = %#v", cfg.Startup, cfg.Outbox)
	}
	if cfg.AI.Provider != "openai" || !cfg.Workflow.WorkerEnabled || cfg.Idempotency.TTL <= cfg.Idempotency.LockTTL {
		t.Fatalf("platform runtime defaults = AI %#v, workflow %#v, idempotency %#v", cfg.AI, cfg.Workflow, cfg.Idempotency)
	}
	if _, err := parseEncryptionKey(cfg.Secrets.EncryptionKey); err != nil || cfg.GitHub.CredentialTTL <= 0 {
		t.Fatalf("secret/integration defaults are invalid: secrets=%#v GitHub=%#v", cfg.Secrets, cfg.GitHub)
	}
	if cfg.Delivery.SandboxRuntime != "docker" || cfg.Delivery.SandboxNodeImage == "" || cfg.Delivery.PublishRoot == "" {
		t.Fatalf("delivery defaults are invalid: %#v", cfg.Delivery)
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
	if cfg.Startup.Migrate || cfg.Startup.EnsureMongoIndexes || cfg.Startup.EnsureNATSStream {
		t.Fatalf("production startup defaults = %#v, outbox = %#v", cfg.Startup, cfg.Outbox)
	}
	if !cfg.Outbox.Enabled {
		t.Fatal("the embedded outbox worker must default on unless a separately deployed worker is configured")
	}
	if !cfg.Security.Session.CookieSecure {
		t.Fatal("production session cookie must default to secure")
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

func clearConfigEnvironment(t *testing.T) {
	t.Helper()
	keys := []string{
		"APP_ENV", "SERVICE_NAME", "HTTP_HOST", "HTTP_PORT", "HTTP_READ_TIMEOUT",
		"HTTP_READ_HEADER_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT",
		"HTTP_SHUTDOWN_TIMEOUT", "HTTP_MAX_HEADER_BYTES", "HTTP_TRUSTED_PROXIES",
		"LOG_LEVEL", "LOG_FORMAT", "CORS_ALLOWED_ORIGINS", "CORS_ALLOWED_METHODS",
		"CORS_ALLOWED_HEADERS", "CORS_EXPOSED_HEADERS", "CORS_ALLOW_CREDENTIALS",
		"CORS_MAX_AGE", "SECURITY_ENABLE_HSTS", "DEPENDENCY_CONNECT_TIMEOUT",
		"DEPENDENCY_READINESS_TIMEOUT", "POSTGRES_DSN", "POSTGRES_MAX_OPEN_CONNS",
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
		"AI_PROVIDER", "OPENAI_API_KEY", "OPENAI_RESPONSES_URL", "AI_DEFAULT_MODEL", "AI_TIMEOUT",
		"AI_MAX_INPUT_BYTES", "AI_MAX_OUTPUT_BYTES", "AI_MAX_RETRIES", "OPENAI_ORGANIZATION", "OPENAI_PROJECT",
		"WORKFLOW_WORKER_ENABLED", "WORKFLOW_WORKER_ID", "WORKFLOW_POLL_INTERVAL", "WORKFLOW_LEASE_DURATION",
		"WORKFLOW_HEARTBEAT",
		"PLATFORM_ENCRYPTION_KEY", "GITHUB_API_BASE_URL", "GITHUB_REQUEST_TIMEOUT", "GITHUB_CREDENTIAL_TTL", "GITHUB_REDIS_PREFIX",
		"DELIVERY_SANDBOX_RUNTIME", "DELIVERY_SANDBOX_HOST", "DELIVERY_SANDBOX_NODE_IMAGE", "DELIVERY_SANDBOX_GO_IMAGE",
		"DELIVERY_SANDBOX_TIMEOUT", "DELIVERY_SANDBOX_OUTPUT_MAX_BYTES", "DELIVERY_SANDBOX_MEMORY",
		"DELIVERY_SANDBOX_CPUS", "DELIVERY_SANDBOX_PIDS", "DELIVERY_QUALITY_TEMP_ROOT",
		"DELIVERY_RESOLVER_NETWORK", "DELIVERY_RESOLVER_NPM_REGISTRY", "DELIVERY_RESOLVER_GO_PROXY",
		"DELIVERY_RESOLVER_GO_SUMDB", "DELIVERY_RESOLVER_TIMEOUT", "DELIVERY_RESOLVER_OUTPUT_MAX_BYTES",
		"DELIVERY_RESOLVER_MEMORY", "DELIVERY_RESOLVER_CPUS", "DELIVERY_RESOLVER_PIDS",
		"DELIVERY_PUBLISH_ROOT", "DELIVERY_PUBLISH_BASE_URL",
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
