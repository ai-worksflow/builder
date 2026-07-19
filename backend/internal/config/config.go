package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	EnvironmentDevelopment = "development"
	EnvironmentTest        = "test"
	EnvironmentStaging     = "staging"
	EnvironmentProduction  = "production"
)

var sandboxRequestFenceHeaders = []string{
	"X-Sandbox-Session-Epoch", "X-Expected-Candidate-ID", "X-Candidate-Version",
	"X-Candidate-Journal-Sequence", "X-Writer-Lease-Epoch", "X-Candidate-Tree-Hash",
	"X-Expected-File-Hash", "X-File-Mode",
}

var sandboxAndAgentResponseFenceHeaders = []string{
	"ETag", "Retry-After", "X-Sandbox-Session-ETag", "X-Sandbox-Session-Epoch",
	"X-Candidate-ID", "X-Candidate-Version", "X-Candidate-Journal-Sequence",
	"X-Writer-Lease-Epoch", "X-Candidate-Tree-Hash", "X-Candidate-Tree-ETag",
	"X-Content-Hash", "X-File-Mode", "X-Content-Object-Hash", "X-File-Exists",
	"X-Byte-Size", "X-Patch-Content-Hash", "X-Agent-Attempt-State", "X-Agent-Fence-Epoch",
}

type Config struct {
	Environment    string
	ServiceName    string
	HTTP           HTTPConfig
	Log            LogConfig
	CORS           CORSConfig
	Security       SecurityConfig
	Dependencies   DependencyConfig
	Postgres       PostgresConfig
	Redis          RedisConfig
	Mongo          MongoConfig
	NATS           NATSConfig
	WebSocket      WebSocketConfig
	Startup        StartupConfig
	Content        ContentConfig
	Outbox         OutboxConfig
	Idempotency    IdempotencyConfig
	AI             AIConfig
	Workflow       WorkflowConfig
	Secrets        SecretsConfig
	GitHub         GitHubConfig
	TemplateSource TemplateSourceConfig
	Repository     RepositoryConfig
	Delivery       DeliveryConfig
	Sandbox        SandboxConfig
	LSP            LSPConfig
	Verification   VerificationConfig
	Agent          AgentConfig
}

type HTTPConfig struct {
	Host              string
	Port              int
	ReadTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
	MaxJSONBodyBytes  int64
	TrustedProxies    []string
}

func (c HTTPConfig) Address() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

type LogConfig struct {
	Level  string
	Format string
}

type CORSConfig struct {
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	ExposedHeaders   []string
	AllowCredentials bool
	MaxAge           time.Duration
}

type SecurityConfig struct {
	EnableHSTS bool
	Session    SessionSecurityConfig
	CSRF       CSRFConfig
	Argon2     Argon2Config
}

type SessionSecurityConfig struct {
	TTL            time.Duration
	CachePrefix    string
	CookieName     string
	CookiePath     string
	CookieDomain   string
	CookieSecure   bool
	CookieSameSite string
}

type CSRFConfig struct {
	CookieName string
	HeaderName string
	TokenBytes int
}

type Argon2Config struct {
	MemoryKiB   int
	Iterations  int
	Parallelism int
	SaltBytes   int
	KeyBytes    int
}

type DependencyConfig struct {
	ConnectTimeout   time.Duration
	ReadinessTimeout time.Duration
}

type PostgresConfig struct {
	DSN             string
	Schema          string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

type RedisConfig struct {
	Address  string
	Password string
	DB       int
	UseTLS   bool
}

type MongoConfig struct {
	URI      string
	Database string
}

type NATSConfig struct {
	URL  string
	Name string
}

type WebSocketConfig struct {
	AllowedOrigins   []string
	AllowAnonymous   bool
	AuthTimeout      time.Duration
	WriteWait        time.Duration
	PongWait         time.Duration
	PingPeriod       time.Duration
	MaxMessageBytes  int64
	SendBuffer       int
	MaxSubscriptions int
	MaxReplayEvents  int
}

type StartupConfig struct {
	EnsureMongoIndexes bool
	EnsureNATSStream   bool
	Timeout            time.Duration
}

type ContentConfig struct {
	MaxBytes          int64
	ReconcileEnabled  bool
	ReconcileInterval time.Duration
	PendingGrace      time.Duration
	OrphanTTL         time.Duration
	ReconcileBatch    int
}

type OutboxConfig struct {
	Enabled      bool
	BatchSize    int
	PollInterval time.Duration
	ClaimTTL     time.Duration
	MaxAttempts  int
	PublishWait  time.Duration
}

type IdempotencyConfig struct {
	TTL              time.Duration
	LockTTL          time.Duration
	MaxResponseBytes int
}

type AIConfig struct {
	Provider       string
	APIKey         string
	BaseURL        string
	DefaultModel   string
	Timeout        time.Duration
	MaxInputBytes  int64
	MaxOutputBytes int64
	MaxRetries     int
	Organization   string
	Project        string
}

type WorkflowConfig struct {
	WorkerEnabled bool
	WorkerID      string
	PollInterval  time.Duration
	LeaseDuration time.Duration
	Heartbeat     time.Duration
}

type SecretsConfig struct {
	EncryptionKey string
}

type GitHubConfig struct {
	APIBaseURL     string
	RequestTimeout time.Duration
	CredentialTTL  time.Duration
	RedisPrefix    string
}

type TemplateSourceConfig struct {
	GitBinary    string
	CacheRoot    string
	AllowedHosts []string
	FetchTimeout time.Duration
}

// RepositoryConfig bounds the derived exact-tree search accelerator. Source
// authority and Candidate fencing remain independent of these cache controls.
type RepositoryConfig struct {
	SearchIndex     RepositorySearchIndexConfig
	SearchAdmission RepositorySearchAdmissionConfig
}

type RepositorySearchIndexConfig struct {
	MaxTrees        int
	MaxSourceBytes  int64
	MaxActiveBuilds int
}

type RepositorySearchAdmissionConfig struct {
	RedisPrefix  string
	Timeout      time.Duration
	QueryProject RepositorySearchRateBucketConfig
	QueryActor   RepositorySearchRateBucketConfig
	BuildProject RepositorySearchRateBucketConfig
	BuildActor   RepositorySearchRateBucketConfig
}

type RepositorySearchRateBucketConfig struct {
	RefillTokens   int64
	RefillInterval time.Duration
	Burst          int64
}

type DeliveryConfig struct {
	SandboxRuntime                  string
	SandboxHost                     string
	SandboxNodeImage                string
	SandboxGoImage                  string
	SandboxTimeout                  time.Duration
	SandboxOutputMax                int
	SandboxMemory                   string
	SandboxCPUs                     string
	SandboxPIDs                     int
	ResolverNetwork                 string
	ResolverNPMRegistry             string
	ResolverGoProxy                 string
	ResolverGoSumDB                 string
	ResolverTimeout                 time.Duration
	ResolverOutputMax               int
	ResolverMemory                  string
	ResolverCPUs                    string
	ResolverPIDs                    int
	QualityTempRoot                 string
	PublishRoot                     string
	PublishBaseURL                  string
	ReleaseWorkerEnabled            bool
	ReleaseWorkerID                 string
	ReleaseControllerURL            string
	ReleaseControllerToken          string
	ReleaseControllerID             string
	ReleaseControllerVersion        string
	ReleaseControllerProtocol       string
	ReleaseControllerTrustKeyDigest string
	ReleaseLeaseDuration            time.Duration
	ReleasePollInterval             time.Duration
	ReleaseReconcileDelay           time.Duration
	ReleaseRequestTimeout           time.Duration
	ReleaseResponseMax              int64
}

type SandboxConfig struct {
	Enabled             bool
	RunnerImage         string
	RuntimeBinary       string
	DaemonHost          string
	WorkspaceRoot       string
	GatewayNetwork      string
	GatewayBindAddress  string
	PreviewDialHost     string
	PreviewPublicOrigin string
	PreviewTicketTTL    time.Duration
	PreviewProbeTimeout time.Duration
	StartupTimeout      time.Duration
	CommandTimeout      time.Duration
	RuntimeOutputMax    int
	CPUMillis           int64
	MemoryBytes         int64
	WorkspaceBytes      int64
	PIDLimit            int
	PreviewPortLimit    int
	IdleHibernateAfter  time.Duration
	MaxRuntime          time.Duration
	LifecycleWorkerID   string
	LifecyclePoll       time.Duration
	LifecycleLease      time.Duration
	LifecycleRetry      time.Duration
	ConnectionTicketTTL time.Duration
	StreamMaxEvents     int
	StreamRetention     time.Duration
	RedisPrefix         string
}

// LSPConfig controls the optional, server-owned language-server gateway. The
// exact image, executable, argv, capabilities, and resource limits do not
// belong here: they come only from an admitted immutable TemplateRelease
// profile at connection time.
type LSPConfig struct {
	Enabled         bool
	RuntimeBinary   string
	DaemonHost      string
	AllowedOrigins  []string
	RequireTLS      bool
	TicketTTL       time.Duration
	BindTimeout     time.Duration
	WriteWait       time.Duration
	CommandTimeout  time.Duration
	CLIOutputMax    int64
	RedisPrefix     string
	RefillPerSecond int64
	Burst           int64
}

// VerificationConfig controls the server-owned Candidate quality worker. All
// executable image and argv identities still come from immutable Plans.
type VerificationConfig struct {
	WorkerEnabled bool
	WorkerID      string
	RuntimeBinary string
	DaemonHost    string
	WorkspaceRoot string
	Memory        string
	CPUs          string
	PIDs          int
	OutputMax     int
	TempBytes     int64
	PollInterval  time.Duration
	LeaseDuration time.Duration
	Heartbeat     time.Duration
}

// AgentConfig describes one server-qualified executor profile and bounded
// planning/worker limits. Raw model, prompt, toolchain, and image identities
// are never accepted from browser requests.
type AgentConfig struct {
	Enabled            bool
	WorkerEnabled      bool
	WorkerID           string
	ProfileID          string
	Adapter            string
	Provider           string
	Model              string
	RunnerImage        string
	RuntimeBinary      string
	DaemonHost         string
	WorktreeRoot       string
	RunnerNetwork      string
	RunnerMemory       string
	RunnerCPUs         string
	RunnerPIDs         int
	RunnerOutputMax    int64
	GatewayBaseURL     string
	GatewayRedisPrefix string
	ModelPolicyHash    string
	ParametersHash     string
	PromptHash         string
	OutputSchemaHash   string
	ToolchainHash      string
	AllowedTools       []string
	WallTime           time.Duration
	MaxInputTokens     int64
	MaxOutputTokens    int64
	MaxCommands        int64
	MaxLogBytes        int64
	MaxPatchBytes      int64
	MaxContextFiles    int
	ClaimBatch         int
	PollInterval       time.Duration
	LeaseDuration      time.Duration
	Heartbeat          time.Duration
}

var deliveryResolverNetworkPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var deliverySumDBPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,255}$`)
var deliveryImageDigestPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:-]{0,255}@sha256:[a-f0-9]{64}$`)
var agentHashPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var agentStableIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:~/@-]*$`)
var postgresSchemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

func Load() (Config, error) {
	loader := envLoader{}
	environment := loader.string("APP_ENV", EnvironmentDevelopment)
	if _, configured := os.LookupEnv("STARTUP_MIGRATE"); configured {
		loader.errs = append(loader.errs, errors.New("STARTUP_MIGRATE is retired; run the standalone cmd/migrate process with WORKSFLOW_MIGRATION_POSTGRES_DSN"))
	}
	mutableStartupDefault := environment != EnvironmentProduction
	developmentEncryptionKey := ""
	if environment == EnvironmentDevelopment || environment == EnvironmentTest {
		developmentEncryptionKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	}
	cfg := Config{
		Environment: environment,
		ServiceName: loader.string("SERVICE_NAME", "worksflow-api"),
		HTTP: HTTPConfig{
			Host:              loader.string("HTTP_HOST", "0.0.0.0"),
			Port:              loader.integer("HTTP_PORT", 8080),
			ReadTimeout:       loader.duration("HTTP_READ_TIMEOUT", 15*time.Second),
			ReadHeaderTimeout: loader.duration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
			WriteTimeout:      loader.duration("HTTP_WRITE_TIMEOUT", 5*time.Minute),
			IdleTimeout:       loader.duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
			ShutdownTimeout:   loader.duration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
			MaxHeaderBytes:    loader.integer("HTTP_MAX_HEADER_BYTES", 1<<20),
			MaxJSONBodyBytes:  int64(loader.integer("HTTP_MAX_JSON_BODY_BYTES", 1<<20)),
			TrustedProxies:    loader.list("HTTP_TRUSTED_PROXIES", nil),
		},
		Log: LogConfig{
			Level:  strings.ToLower(loader.string("LOG_LEVEL", "info")),
			Format: strings.ToLower(loader.string("LOG_FORMAT", "json")),
		},
		CORS: CORSConfig{
			AllowedOrigins: loader.list("CORS_ALLOWED_ORIGINS", []string{"http://localhost:3000", "http://127.0.0.1:3000"}),
			AllowedMethods: loader.list("CORS_ALLOWED_METHODS", []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}),
			AllowedHeaders: loader.list("CORS_ALLOWED_HEADERS", []string{
				"Accept", "Authorization", "Content-Type", "Idempotency-Key", "If-Match", "X-CSRF-Token", "X-Request-ID",
				"X-Sandbox-Session-Epoch", "X-Expected-Candidate-ID", "X-Candidate-Version", "X-Candidate-Journal-Sequence",
				"X-Writer-Lease-Epoch", "X-Candidate-Tree-Hash", "X-Expected-File-Hash", "X-File-Mode",
			}),
			ExposedHeaders: loader.list("CORS_EXPOSED_HEADERS", []string{
				"ETag", "Idempotency-Replayed", "Retry-After", "X-Request-ID", "Content-Disposition", "Digest",
				"X-Archive-File-Count", "X-Archive-Redaction-Count", "X-Command-ETag", "X-Command-Location",
				"X-Sandbox-Session-ETag", "X-Sandbox-Session-Epoch", "X-Candidate-ID", "X-Candidate-Version",
				"X-Candidate-Journal-Sequence", "X-Writer-Lease-Epoch",
				"X-Candidate-Tree-Hash", "X-Candidate-Tree-ETag", "X-Content-Hash", "X-File-Mode",
				"X-Content-Object-Hash", "X-File-Exists", "X-Byte-Size", "X-Patch-Content-Hash",
				"X-Agent-Attempt-State", "X-Agent-Fence-Epoch",
			}),
			AllowCredentials: loader.boolean("CORS_ALLOW_CREDENTIALS", true),
			MaxAge:           loader.duration("CORS_MAX_AGE", 12*time.Hour),
		},
		Security: SecurityConfig{
			EnableHSTS: loader.boolean("SECURITY_ENABLE_HSTS", false),
			Session: SessionSecurityConfig{
				TTL:            loader.duration("SESSION_TTL", 7*24*time.Hour),
				CachePrefix:    loader.string("SESSION_CACHE_PREFIX", "worksflow:session:"),
				CookieName:     loader.string("SESSION_COOKIE_NAME", "worksflow_session"),
				CookiePath:     loader.string("SESSION_COOKIE_PATH", "/"),
				CookieDomain:   loader.string("SESSION_COOKIE_DOMAIN", ""),
				CookieSecure:   loader.boolean("SESSION_COOKIE_SECURE", environment == EnvironmentProduction),
				CookieSameSite: strings.ToLower(loader.string("SESSION_COOKIE_SAME_SITE", "lax")),
			},
			CSRF: CSRFConfig{
				CookieName: loader.string("CSRF_COOKIE_NAME", "worksflow_csrf"),
				HeaderName: loader.string("CSRF_HEADER_NAME", "X-CSRF-Token"),
				TokenBytes: loader.integer("CSRF_TOKEN_BYTES", 32),
			},
			Argon2: Argon2Config{
				MemoryKiB:   loader.integer("ARGON2_MEMORY_KIB", 64*1024),
				Iterations:  loader.integer("ARGON2_ITERATIONS", 3),
				Parallelism: loader.integer("ARGON2_PARALLELISM", 2),
				SaltBytes:   loader.integer("ARGON2_SALT_BYTES", 16),
				KeyBytes:    loader.integer("ARGON2_KEY_BYTES", 32),
			},
		},
		Dependencies: DependencyConfig{
			ConnectTimeout:   loader.duration("DEPENDENCY_CONNECT_TIMEOUT", 10*time.Second),
			ReadinessTimeout: loader.duration("DEPENDENCY_READINESS_TIMEOUT", 2*time.Second),
		},
		Postgres: PostgresConfig{
			DSN:             loader.rawString("POSTGRES_DSN", "postgres://worksflow:worksflow@localhost:5432/worksflow?sslmode=disable"),
			Schema:          loader.string("POSTGRES_SCHEMA", "public"),
			MaxOpenConns:    loader.integer("POSTGRES_MAX_OPEN_CONNS", 25),
			MaxIdleConns:    loader.integer("POSTGRES_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime: loader.duration("POSTGRES_CONN_MAX_LIFETIME", 30*time.Minute),
			ConnMaxIdleTime: loader.duration("POSTGRES_CONN_MAX_IDLE_TIME", 5*time.Minute),
		},
		Redis: RedisConfig{
			Address:  loader.string("REDIS_ADDRESS", "localhost:6379"),
			Password: loader.string("REDIS_PASSWORD", ""),
			DB:       loader.integer("REDIS_DB", 0),
			UseTLS:   loader.boolean("REDIS_USE_TLS", false),
		},
		Mongo: MongoConfig{
			URI:      loader.string("MONGO_URI", "mongodb://localhost:27017"),
			Database: loader.string("MONGO_DATABASE", "worksflow"),
		},
		NATS: NATSConfig{
			URL:  loader.string("NATS_URL", "nats://localhost:4222"),
			Name: loader.string("NATS_CLIENT_NAME", "worksflow-api"),
		},
		WebSocket: WebSocketConfig{
			AllowedOrigins:   loader.list("WS_ALLOWED_ORIGINS", []string{"http://localhost:3000", "http://127.0.0.1:3000"}),
			AllowAnonymous:   loader.boolean("WS_ALLOW_ANONYMOUS", false),
			AuthTimeout:      loader.duration("WS_AUTH_TIMEOUT", 10*time.Second),
			WriteWait:        loader.duration("WS_WRITE_WAIT", 10*time.Second),
			PongWait:         loader.duration("WS_PONG_WAIT", 60*time.Second),
			PingPeriod:       loader.duration("WS_PING_PERIOD", 50*time.Second),
			MaxMessageBytes:  int64(loader.integer("WS_MAX_MESSAGE_BYTES", 64<<10)),
			SendBuffer:       loader.integer("WS_SEND_BUFFER", 64),
			MaxSubscriptions: loader.integer("WS_MAX_SUBSCRIPTIONS", 128),
			MaxReplayEvents:  loader.integer("WS_MAX_REPLAY_EVENTS", 50),
		},
		Startup: StartupConfig{
			EnsureMongoIndexes: loader.boolean("STARTUP_ENSURE_MONGO_INDEXES", mutableStartupDefault),
			EnsureNATSStream:   loader.boolean("STARTUP_ENSURE_NATS_STREAM", mutableStartupDefault),
			Timeout:            loader.duration("STARTUP_TIMEOUT", 30*time.Second),
		},
		Content: ContentConfig{
			MaxBytes:          int64(loader.integer("CONTENT_MAX_BYTES", 16<<20)),
			ReconcileEnabled:  loader.boolean("CONTENT_RECONCILE_ENABLED", true),
			ReconcileInterval: loader.duration("CONTENT_RECONCILE_INTERVAL", time.Minute),
			PendingGrace:      loader.duration("CONTENT_PENDING_GRACE", 5*time.Minute),
			OrphanTTL:         loader.duration("CONTENT_ORPHAN_TTL", 24*time.Hour),
			ReconcileBatch:    loader.integer("CONTENT_RECONCILE_BATCH", 100),
		},
		Outbox: OutboxConfig{
			Enabled:      loader.boolean("OUTBOX_ENABLED", true),
			BatchSize:    loader.integer("OUTBOX_BATCH_SIZE", 50),
			PollInterval: loader.duration("OUTBOX_POLL_INTERVAL", 500*time.Millisecond),
			ClaimTTL:     loader.duration("OUTBOX_CLAIM_TTL", 30*time.Second),
			MaxAttempts:  loader.integer("OUTBOX_MAX_ATTEMPTS", 20),
			PublishWait:  loader.duration("OUTBOX_PUBLISH_WAIT", 5*time.Second),
		},
		Idempotency: IdempotencyConfig{
			TTL:              loader.duration("IDEMPOTENCY_TTL", 24*time.Hour),
			LockTTL:          loader.duration("IDEMPOTENCY_LOCK_TTL", 2*time.Minute),
			MaxResponseBytes: loader.integer("IDEMPOTENCY_MAX_RESPONSE_BYTES", 8<<20),
		},
		AI: AIConfig{
			Provider:       strings.ToLower(loader.string("AI_PROVIDER", "openai")),
			APIKey:         loader.string("OPENAI_API_KEY", ""),
			BaseURL:        loader.openAIResponsesURL(),
			DefaultModel:   loader.openAIDefaultModel(),
			Timeout:        loader.duration("AI_TIMEOUT", 12*time.Minute),
			MaxInputBytes:  int64(loader.integer("AI_MAX_INPUT_BYTES", 4<<20)),
			MaxOutputBytes: int64(loader.integer("AI_MAX_OUTPUT_BYTES", 16<<20)),
			MaxRetries:     loader.integer("AI_MAX_RETRIES", 0),
			Organization:   loader.string("OPENAI_ORGANIZATION", ""),
			Project:        loader.string("OPENAI_PROJECT", ""),
		},
		Workflow: WorkflowConfig{
			WorkerEnabled: loader.boolean("WORKFLOW_WORKER_ENABLED", true),
			WorkerID:      loader.string("WORKFLOW_WORKER_ID", ""),
			PollInterval:  loader.duration("WORKFLOW_POLL_INTERVAL", 500*time.Millisecond),
			LeaseDuration: loader.duration("WORKFLOW_LEASE_DURATION", 2*time.Minute),
			Heartbeat:     loader.duration("WORKFLOW_HEARTBEAT", 30*time.Second),
		},
		Secrets: SecretsConfig{
			EncryptionKey: loader.string("PLATFORM_ENCRYPTION_KEY", developmentEncryptionKey),
		},
		GitHub: GitHubConfig{
			APIBaseURL:     loader.string("GITHUB_API_BASE_URL", "https://api.github.com"),
			RequestTimeout: loader.duration("GITHUB_REQUEST_TIMEOUT", 12*time.Second),
			CredentialTTL:  loader.duration("GITHUB_CREDENTIAL_TTL", 8*time.Hour),
			RedisPrefix:    loader.string("GITHUB_REDIS_PREFIX", "worksflow:github:credential:"),
		},
		TemplateSource: TemplateSourceConfig{
			GitBinary:    loader.string("TEMPLATE_SOURCE_GIT_BINARY", "git"),
			CacheRoot:    loader.string("TEMPLATE_SOURCE_CACHE_ROOT", "/var/lib/worksflow/template-sources"),
			AllowedHosts: loader.list("TEMPLATE_SOURCE_ALLOWED_HOSTS", []string{"github.com"}),
			FetchTimeout: loader.duration("TEMPLATE_SOURCE_FETCH_TIMEOUT", 2*time.Minute),
		},
		Repository: RepositoryConfig{
			SearchIndex: RepositorySearchIndexConfig{
				MaxTrees:        loader.integer("REPOSITORY_SEARCH_INDEX_MAX_TREES", 16),
				MaxSourceBytes:  int64(loader.integer("REPOSITORY_SEARCH_INDEX_MAX_SOURCE_BYTES", 256<<20)),
				MaxActiveBuilds: loader.integer("REPOSITORY_SEARCH_INDEX_MAX_ACTIVE_BUILDS", 2),
			},
			SearchAdmission: RepositorySearchAdmissionConfig{
				RedisPrefix: loader.string(
					"REPOSITORY_SEARCH_RATE_REDIS_PREFIX",
					"worksflow:repository:exact-tree-search-admission:",
				),
				Timeout: loader.duration("REPOSITORY_SEARCH_RATE_TIMEOUT", 250*time.Millisecond),
				QueryProject: RepositorySearchRateBucketConfig{
					RefillTokens: int64(loader.integer("REPOSITORY_SEARCH_QUERY_PROJECT_REFILL_TOKENS", 20)),
					RefillInterval: loader.duration(
						"REPOSITORY_SEARCH_QUERY_PROJECT_REFILL_INTERVAL", time.Second,
					),
					Burst: int64(loader.integer("REPOSITORY_SEARCH_QUERY_PROJECT_BURST", 40)),
				},
				QueryActor: RepositorySearchRateBucketConfig{
					RefillTokens: int64(loader.integer("REPOSITORY_SEARCH_QUERY_ACTOR_REFILL_TOKENS", 4)),
					RefillInterval: loader.duration(
						"REPOSITORY_SEARCH_QUERY_ACTOR_REFILL_INTERVAL", time.Second,
					),
					Burst: int64(loader.integer("REPOSITORY_SEARCH_QUERY_ACTOR_BURST", 8)),
				},
				BuildProject: RepositorySearchRateBucketConfig{
					RefillTokens: int64(loader.integer("REPOSITORY_SEARCH_BUILD_PROJECT_REFILL_TOKENS", 1)),
					RefillInterval: loader.duration(
						"REPOSITORY_SEARCH_BUILD_PROJECT_REFILL_INTERVAL", 15*time.Second,
					),
					Burst: int64(loader.integer("REPOSITORY_SEARCH_BUILD_PROJECT_BURST", 2)),
				},
				BuildActor: RepositorySearchRateBucketConfig{
					RefillTokens: int64(loader.integer("REPOSITORY_SEARCH_BUILD_ACTOR_REFILL_TOKENS", 1)),
					RefillInterval: loader.duration(
						"REPOSITORY_SEARCH_BUILD_ACTOR_REFILL_INTERVAL", 30*time.Second,
					),
					Burst: int64(loader.integer("REPOSITORY_SEARCH_BUILD_ACTOR_BURST", 1)),
				},
			},
		},
		Delivery: DeliveryConfig{
			SandboxRuntime:                  loader.string("DELIVERY_SANDBOX_RUNTIME", "docker"),
			SandboxHost:                     loader.string("DELIVERY_SANDBOX_HOST", ""),
			SandboxNodeImage:                loader.string("DELIVERY_SANDBOX_NODE_IMAGE", "node:22-alpine"),
			SandboxGoImage:                  loader.string("DELIVERY_SANDBOX_GO_IMAGE", "golang:1.22-alpine"),
			SandboxTimeout:                  loader.duration("DELIVERY_SANDBOX_TIMEOUT", 2*time.Minute),
			SandboxOutputMax:                loader.integer("DELIVERY_SANDBOX_OUTPUT_MAX_BYTES", 1<<20),
			SandboxMemory:                   loader.string("DELIVERY_SANDBOX_MEMORY", "512m"),
			SandboxCPUs:                     loader.string("DELIVERY_SANDBOX_CPUS", "1.0"),
			SandboxPIDs:                     loader.integer("DELIVERY_SANDBOX_PIDS", 128),
			ResolverNetwork:                 loader.string("DELIVERY_RESOLVER_NETWORK", "bridge"),
			ResolverNPMRegistry:             loader.string("DELIVERY_RESOLVER_NPM_REGISTRY", "https://registry.npmjs.org"),
			ResolverGoProxy:                 loader.string("DELIVERY_RESOLVER_GO_PROXY", "https://proxy.golang.org"),
			ResolverGoSumDB:                 loader.string("DELIVERY_RESOLVER_GO_SUMDB", "sum.golang.org"),
			ResolverTimeout:                 loader.duration("DELIVERY_RESOLVER_TIMEOUT", 3*time.Minute),
			ResolverOutputMax:               loader.integer("DELIVERY_RESOLVER_OUTPUT_MAX_BYTES", 1<<20),
			ResolverMemory:                  loader.string("DELIVERY_RESOLVER_MEMORY", "512m"),
			ResolverCPUs:                    loader.string("DELIVERY_RESOLVER_CPUS", "1.0"),
			ResolverPIDs:                    loader.integer("DELIVERY_RESOLVER_PIDS", 128),
			QualityTempRoot:                 loader.string("DELIVERY_QUALITY_TEMP_ROOT", ""),
			PublishRoot:                     loader.string("DELIVERY_PUBLISH_ROOT", "./var/published"),
			PublishBaseURL:                  loader.string("DELIVERY_PUBLISH_BASE_URL", "http://localhost:8080/published"),
			ReleaseWorkerEnabled:            loader.boolean("RELEASE_DELIVERY_WORKER_ENABLED", false),
			ReleaseWorkerID:                 loader.string("RELEASE_DELIVERY_WORKER_ID", ""),
			ReleaseControllerURL:            loader.string("RELEASE_DELIVERY_CONTROLLER_URL", ""),
			ReleaseControllerToken:          loader.string("RELEASE_DELIVERY_CONTROLLER_TOKEN", ""),
			ReleaseControllerID:             loader.string("RELEASE_DELIVERY_CONTROLLER_ID", ""),
			ReleaseControllerVersion:        loader.string("RELEASE_DELIVERY_CONTROLLER_VERSION", ""),
			ReleaseControllerProtocol:       loader.string("RELEASE_DELIVERY_CONTROLLER_PROTOCOL", "worksflow.release-delivery/v3"),
			ReleaseControllerTrustKeyDigest: loader.string("RELEASE_DELIVERY_CONTROLLER_TRUST_KEY_DIGEST", ""),
			ReleaseLeaseDuration:            loader.duration("RELEASE_DELIVERY_LEASE_DURATION", 5*time.Minute),
			ReleasePollInterval:             loader.duration("RELEASE_DELIVERY_POLL_INTERVAL", time.Second),
			ReleaseReconcileDelay:           loader.duration("RELEASE_DELIVERY_RECONCILE_DELAY", 5*time.Second),
			ReleaseRequestTimeout:           loader.duration("RELEASE_DELIVERY_REQUEST_TIMEOUT", 2*time.Minute),
			ReleaseResponseMax:              int64(loader.integer("RELEASE_DELIVERY_RESPONSE_MAX_BYTES", 1<<20)),
		},
		Sandbox: SandboxConfig{
			Enabled:             loader.boolean("SANDBOX_ENABLED", false),
			RunnerImage:         loader.string("SANDBOX_RUNNER_IMAGE", ""),
			RuntimeBinary:       loader.string("SANDBOX_RUNTIME", "docker"),
			DaemonHost:          loader.string("SANDBOX_DAEMON_HOST", ""),
			WorkspaceRoot:       loader.string("SANDBOX_WORKSPACE_ROOT", "/var/lib/worksflow/sandboxes"),
			GatewayNetwork:      loader.string("SANDBOX_GATEWAY_NETWORK", "bridge"),
			GatewayBindAddress:  loader.string("SANDBOX_GATEWAY_BIND_ADDRESS", "127.0.0.1"),
			PreviewDialHost:     loader.string("SANDBOX_PREVIEW_DIAL_HOST", "127.0.0.1"),
			PreviewPublicOrigin: loader.string("SANDBOX_PREVIEW_PUBLIC_ORIGIN", "http://preview.localhost:8080"),
			PreviewTicketTTL:    loader.duration("SANDBOX_PREVIEW_TICKET_TTL", 15*time.Minute),
			PreviewProbeTimeout: loader.duration("SANDBOX_PREVIEW_PROBE_TIMEOUT", 750*time.Millisecond),
			StartupTimeout:      loader.duration("SANDBOX_STARTUP_TIMEOUT", 2*time.Minute),
			CommandTimeout:      loader.duration("SANDBOX_COMMAND_TIMEOUT", 30*time.Second),
			RuntimeOutputMax:    loader.integer("SANDBOX_RUNTIME_OUTPUT_MAX_BYTES", 1<<20),
			CPUMillis:           int64(loader.integer("SANDBOX_CPU_MILLIS", 2_000)),
			MemoryBytes:         int64(loader.integer("SANDBOX_MEMORY_BYTES", 4<<30)),
			WorkspaceBytes:      int64(loader.integer("SANDBOX_WORKSPACE_BYTES", 10<<30)),
			PIDLimit:            loader.integer("SANDBOX_PID_LIMIT", 256),
			PreviewPortLimit:    loader.integer("SANDBOX_PREVIEW_PORT_LIMIT", 3),
			IdleHibernateAfter:  loader.duration("SANDBOX_IDLE_HIBERNATE_AFTER", 30*time.Minute),
			MaxRuntime:          loader.duration("SANDBOX_MAX_RUNTIME", 8*time.Hour),
			LifecycleWorkerID:   loader.string("SANDBOX_LIFECYCLE_WORKER_ID", ""),
			LifecyclePoll:       loader.duration("SANDBOX_LIFECYCLE_POLL_INTERVAL", 5*time.Second),
			LifecycleLease:      loader.duration("SANDBOX_LIFECYCLE_LEASE_DURATION", 15*time.Minute),
			LifecycleRetry:      loader.duration("SANDBOX_LIFECYCLE_RETRY_DELAY", 30*time.Second),
			ConnectionTicketTTL: loader.duration("SANDBOX_CONNECTION_TICKET_TTL", 30*time.Second),
			StreamMaxEvents:     loader.integer("SANDBOX_STREAM_MAX_EVENTS", 4_096),
			StreamRetention:     loader.duration("SANDBOX_STREAM_RETENTION", 24*time.Hour),
			RedisPrefix:         loader.string("SANDBOX_REDIS_PREFIX", "worksflow:sandbox:"),
		},
		LSP: LSPConfig{
			Enabled:         loader.boolean("LSP_ENABLED", false),
			RuntimeBinary:   loader.string("LSP_RUNTIME_BINARY", "/usr/bin/docker"),
			DaemonHost:      loader.string("LSP_DAEMON_HOST", ""),
			AllowedOrigins:  loader.list("LSP_ALLOWED_ORIGINS", []string{"http://localhost:3000", "http://127.0.0.1:3000"}),
			RequireTLS:      loader.boolean("LSP_REQUIRE_TLS", environment == EnvironmentStaging || environment == EnvironmentProduction),
			TicketTTL:       loader.duration("LSP_TICKET_TTL", 20*time.Second),
			BindTimeout:     loader.duration("LSP_BIND_TIMEOUT", 5*time.Second),
			WriteWait:       loader.duration("LSP_WRITE_WAIT", 2*time.Second),
			CommandTimeout:  loader.duration("LSP_COMMAND_TIMEOUT", 30*time.Second),
			CLIOutputMax:    int64(loader.integer("LSP_CLI_OUTPUT_MAX_BYTES", 1<<20)),
			RedisPrefix:     loader.string("LSP_REDIS_PREFIX", "worksflow:sandbox:lsp:"),
			RefillPerSecond: int64(loader.integer("LSP_RATE_REFILL_PER_SECOND", 30)),
			Burst:           int64(loader.integer("LSP_RATE_BURST", 60)),
		},
		Verification: VerificationConfig{
			WorkerEnabled: loader.boolean("VERIFICATION_WORKER_ENABLED", false),
			WorkerID:      loader.string("VERIFICATION_WORKER_ID", ""),
			RuntimeBinary: loader.string("VERIFICATION_RUNTIME", "docker"),
			DaemonHost:    loader.string("VERIFICATION_DAEMON_HOST", ""),
			WorkspaceRoot: loader.string("VERIFICATION_WORKSPACE_ROOT", "/var/lib/worksflow/verification"),
			Memory:        loader.string("VERIFICATION_MEMORY", "1g"),
			CPUs:          loader.string("VERIFICATION_CPUS", "1.0"),
			PIDs:          loader.integer("VERIFICATION_PIDS", 256),
			OutputMax:     loader.integer("VERIFICATION_OUTPUT_MAX_BYTES", 1<<20),
			TempBytes:     int64(loader.integer("VERIFICATION_TEMP_BYTES", 512<<20)),
			PollInterval:  loader.duration("VERIFICATION_POLL_INTERVAL", time.Second),
			LeaseDuration: loader.duration("VERIFICATION_LEASE_DURATION", 2*time.Minute),
			Heartbeat:     loader.duration("VERIFICATION_HEARTBEAT", 30*time.Second),
		},
		Agent: AgentConfig{
			Enabled:            loader.boolean("AGENT_ENABLED", false),
			WorkerEnabled:      loader.boolean("AGENT_WORKER_ENABLED", false),
			WorkerID:           loader.string("AGENT_WORKER_ID", ""),
			ProfileID:          loader.string("AGENT_EXECUTOR_PROFILE_ID", "codex-default"),
			Adapter:            loader.string("AGENT_EXECUTOR_ADAPTER", "codex-cli"),
			Provider:           loader.string("AGENT_EXECUTOR_PROVIDER", "openai"),
			Model:              loader.string("AGENT_EXECUTOR_MODEL", loader.openAIDefaultModel()),
			RunnerImage:        loader.string("AGENT_RUNNER_IMAGE", ""),
			RuntimeBinary:      loader.string("AGENT_RUNTIME", "docker"),
			DaemonHost:         loader.string("AGENT_DAEMON_HOST", ""),
			WorktreeRoot:       loader.string("AGENT_WORKTREE_ROOT", "/var/lib/worksflow/agent-worktrees"),
			RunnerNetwork:      loader.string("AGENT_RUNNER_NETWORK", "worksflow-agent-model"),
			RunnerMemory:       loader.string("AGENT_RUNNER_MEMORY", "4g"),
			RunnerCPUs:         loader.string("AGENT_RUNNER_CPUS", "2.0"),
			RunnerPIDs:         loader.integer("AGENT_RUNNER_PIDS", 256),
			RunnerOutputMax:    int64(loader.integer("AGENT_RUNNER_OUTPUT_MAX_BYTES", 8<<20)),
			GatewayBaseURL:     loader.string("AGENT_MODEL_GATEWAY_BASE_URL", ""),
			GatewayRedisPrefix: loader.string("AGENT_MODEL_GATEWAY_REDIS_PREFIX", "worksflow:agent:model:"),
			ModelPolicyHash:    loader.string("AGENT_MODEL_POLICY_HASH", ""),
			ParametersHash:     loader.string("AGENT_PARAMETERS_HASH", ""),
			PromptHash:         loader.string("AGENT_PROMPT_HASH", ""),
			OutputSchemaHash:   loader.string("AGENT_OUTPUT_SCHEMA_HASH", ""),
			ToolchainHash:      loader.string("AGENT_TOOLCHAIN_HASH", ""),
			AllowedTools: loader.list("AGENT_ALLOWED_TOOLS", []string{
				"file.read", "file.write", "file.search", "shell.exec", "diagnostic.read",
			}),
			WallTime:        loader.duration("AGENT_WALL_TIME", 30*time.Minute),
			MaxInputTokens:  int64(loader.integer("AGENT_MAX_INPUT_TOKENS", 200_000)),
			MaxOutputTokens: int64(loader.integer("AGENT_MAX_OUTPUT_TOKENS", 50_000)),
			MaxCommands:     int64(loader.integer("AGENT_MAX_COMMANDS", 200)),
			MaxLogBytes:     int64(loader.integer("AGENT_MAX_LOG_BYTES", 8<<20)),
			MaxPatchBytes:   int64(loader.integer("AGENT_MAX_PATCH_BYTES", 16<<20)),
			MaxContextFiles: loader.integer("AGENT_MAX_CONTEXT_FILES", 256),
			ClaimBatch:      loader.integer("AGENT_CLAIM_BATCH", 10),
			PollInterval:    loader.duration("AGENT_POLL_INTERVAL", time.Second),
			LeaseDuration:   loader.duration("AGENT_LEASE_DURATION", 2*time.Minute),
			Heartbeat:       loader.duration("AGENT_HEARTBEAT", 30*time.Second),
		},
	}

	if err := errors.Join(loader.errs...); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var errs []error
	if !oneOf(c.Environment, EnvironmentDevelopment, EnvironmentTest, EnvironmentStaging, EnvironmentProduction) {
		errs = append(errs, fmt.Errorf("APP_ENV must be one of development, test, staging, production"))
	}
	if strings.TrimSpace(c.ServiceName) == "" {
		errs = append(errs, errors.New("SERVICE_NAME must not be empty"))
	}
	if strings.TrimSpace(c.HTTP.Host) == "" || c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		errs = append(errs, errors.New("HTTP_HOST and HTTP_PORT must form a valid listen address"))
	}
	if c.HTTP.ReadTimeout <= 0 || c.HTTP.ReadHeaderTimeout <= 0 || c.HTTP.WriteTimeout <= 0 || c.HTTP.IdleTimeout <= 0 || c.HTTP.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("HTTP timeouts must be positive"))
	}
	if c.HTTP.MaxHeaderBytes < 1024 {
		errs = append(errs, errors.New("HTTP_MAX_HEADER_BYTES must be at least 1024"))
	}
	if c.HTTP.MaxJSONBodyBytes < 1024 || c.HTTP.MaxJSONBodyBytes > 16<<20 {
		errs = append(errs, errors.New("HTTP_MAX_JSON_BODY_BYTES must be between 1024 and 16777216"))
	}
	if !oneOf(c.Log.Level, "debug", "info", "warn", "error") {
		errs = append(errs, errors.New("LOG_LEVEL must be debug, info, warn, or error"))
	}
	if !oneOf(c.Log.Format, "json", "text") {
		errs = append(errs, errors.New("LOG_FORMAT must be json or text"))
	}
	if len(c.CORS.AllowedOrigins) == 0 {
		errs = append(errs, errors.New("CORS_ALLOWED_ORIGINS must not be empty"))
	}
	if c.CORS.AllowCredentials && contains(c.CORS.AllowedOrigins, "*") {
		errs = append(errs, errors.New("CORS wildcard origin cannot be used with credentials"))
	}
	if c.Environment == EnvironmentProduction && contains(c.CORS.AllowedOrigins, "*") {
		errs = append(errs, errors.New("CORS wildcard origin is not allowed in production"))
	}
	missingAllowedHeaders := missingFold(c.CORS.AllowedHeaders, append([]string{
		c.Security.CSRF.HeaderName, "If-Match", "Idempotency-Key",
	}, sandboxRequestFenceHeaders...))
	if len(missingAllowedHeaders) != 0 {
		errs = append(errs, fmt.Errorf("CORS_ALLOWED_HEADERS omits required mutation or Sandbox fence headers: %s", strings.Join(missingAllowedHeaders, ", ")))
	}
	missingExposedHeaders := missingFold(c.CORS.ExposedHeaders, sandboxAndAgentResponseFenceHeaders)
	if len(missingExposedHeaders) != 0 {
		errs = append(errs, fmt.Errorf("CORS_EXPOSED_HEADERS omits required Sandbox or Agent integrity headers: %s", strings.Join(missingExposedHeaders, ", ")))
	}
	if c.Security.Session.TTL <= 0 || c.Security.Session.TTL > 90*24*time.Hour {
		errs = append(errs, errors.New("SESSION_TTL must be positive and no longer than 90 days"))
	}
	if !validCookieName(c.Security.Session.CookieName) || !validCookieName(c.Security.CSRF.CookieName) {
		errs = append(errs, errors.New("session and CSRF cookie names are invalid"))
	}
	if c.Security.Session.CookiePath == "" || !strings.HasPrefix(c.Security.Session.CookiePath, "/") {
		errs = append(errs, errors.New("SESSION_COOKIE_PATH must begin with /"))
	}
	if !oneOf(c.Security.Session.CookieSameSite, "lax", "strict", "none") {
		errs = append(errs, errors.New("SESSION_COOKIE_SAME_SITE must be lax, strict, or none"))
	}
	if c.Security.Session.CookieSameSite == "none" && !c.Security.Session.CookieSecure {
		errs = append(errs, errors.New("SameSite=None session cookies must be secure"))
	}
	if c.Environment == EnvironmentProduction && !c.Security.Session.CookieSecure {
		errs = append(errs, errors.New("production session cookies must be secure"))
	}
	if strings.TrimSpace(c.Security.Session.CachePrefix) == "" || !validCookieName(c.Security.CSRF.HeaderName) || c.Security.CSRF.TokenBytes < 16 || c.Security.CSRF.TokenBytes > 64 {
		errs = append(errs, errors.New("session cache and CSRF configuration is invalid"))
	}
	if c.Security.Argon2.MemoryKiB < 8*1024 || c.Security.Argon2.MemoryKiB > 256*1024 ||
		c.Security.Argon2.Iterations < 1 || c.Security.Argon2.Iterations > 10 ||
		c.Security.Argon2.Parallelism < 1 || c.Security.Argon2.Parallelism > 16 ||
		c.Security.Argon2.SaltBytes < 16 || c.Security.Argon2.SaltBytes > 64 ||
		c.Security.Argon2.KeyBytes < 16 || c.Security.Argon2.KeyBytes > 64 {
		errs = append(errs, errors.New("Argon2id configuration is outside safe limits"))
	}
	if c.Dependencies.ConnectTimeout <= 0 || c.Dependencies.ReadinessTimeout <= 0 {
		errs = append(errs, errors.New("dependency timeouts must be positive"))
	}
	if err := validatePostgresDSN(c.Postgres.DSN); err != nil {
		errs = append(errs, err)
	}
	if !validPostgresSchema(c.Postgres.Schema) {
		errs = append(errs, errors.New("POSTGRES_SCHEMA must be one canonical unquoted non-system PostgreSQL identifier"))
	}
	if c.Postgres.MaxOpenConns < 1 || c.Postgres.MaxIdleConns < 0 || c.Postgres.MaxIdleConns > c.Postgres.MaxOpenConns {
		errs = append(errs, errors.New("Postgres connection pool limits are invalid"))
	}
	if _, _, err := net.SplitHostPort(c.Redis.Address); err != nil {
		errs = append(errs, fmt.Errorf("REDIS_ADDRESS must be host:port: %w", err))
	}
	if c.Redis.DB < 0 {
		errs = append(errs, errors.New("REDIS_DB must not be negative"))
	}
	if err := validateURL("MONGO_URI", c.Mongo.URI, "mongodb", "mongodb+srv"); err != nil {
		errs = append(errs, err)
	}
	if strings.TrimSpace(c.Mongo.Database) == "" {
		errs = append(errs, errors.New("MONGO_DATABASE must not be empty"))
	}
	if err := validateURL("NATS_URL", c.NATS.URL, "nats", "tls", "ws", "wss"); err != nil {
		errs = append(errs, err)
	}
	if len(c.WebSocket.AllowedOrigins) == 0 {
		errs = append(errs, errors.New("WS_ALLOWED_ORIGINS must not be empty"))
	}
	if c.Environment == EnvironmentProduction && contains(c.WebSocket.AllowedOrigins, "*") {
		errs = append(errs, errors.New("WebSocket wildcard origin is not allowed in production"))
	}
	if c.Environment == EnvironmentProduction && c.WebSocket.AllowAnonymous {
		errs = append(errs, errors.New("anonymous WebSocket access is not allowed in production"))
	}
	if c.WebSocket.AuthTimeout <= 0 || c.WebSocket.WriteWait <= 0 || c.WebSocket.PongWait <= 0 || c.WebSocket.PingPeriod <= 0 || c.WebSocket.PingPeriod >= c.WebSocket.PongWait {
		errs = append(errs, errors.New("WebSocket timeouts are invalid; ping period must be shorter than pong wait"))
	}
	if c.WebSocket.MaxMessageBytes < 1 || c.WebSocket.SendBuffer < 1 || c.WebSocket.MaxSubscriptions < 1 ||
		c.WebSocket.MaxReplayEvents < 1 || c.WebSocket.MaxReplayEvents+1 > c.WebSocket.SendBuffer {
		errs = append(errs, errors.New("WebSocket limits must be positive"))
	}
	if c.Startup.Timeout <= 0 {
		errs = append(errs, errors.New("STARTUP_TIMEOUT must be positive"))
	}
	if c.Content.MaxBytes < 1024 || c.Content.MaxBytes > 64<<20 {
		errs = append(errs, errors.New("CONTENT_MAX_BYTES must be between 1024 and 67108864"))
	}
	if c.Content.ReconcileInterval <= 0 || c.Content.PendingGrace <= 0 || c.Content.OrphanTTL <= c.Content.PendingGrace || c.Content.ReconcileBatch < 1 || c.Content.ReconcileBatch > 1000 {
		errs = append(errs, errors.New("content reconciliation intervals and batch size are invalid"))
	}
	if c.Outbox.BatchSize < 1 || c.Outbox.BatchSize > 500 || c.Outbox.PollInterval <= 0 || c.Outbox.PollInterval > time.Minute ||
		c.Outbox.ClaimTTL < time.Second || c.Outbox.ClaimTTL > 10*time.Minute || c.Outbox.MaxAttempts < 1 ||
		c.Outbox.MaxAttempts > 100 || c.Outbox.PublishWait <= 0 || c.Outbox.PublishWait >= c.Outbox.ClaimTTL {
		errs = append(errs, errors.New("outbox batch, poll, claim, attempt, and publish limits are invalid"))
	}
	if c.Idempotency.TTL <= 0 || c.Idempotency.TTL > 30*24*time.Hour || c.Idempotency.LockTTL <= 0 || c.Idempotency.LockTTL >= c.Idempotency.TTL || c.Idempotency.MaxResponseBytes < 1024 || c.Idempotency.MaxResponseBytes > 64<<20 {
		errs = append(errs, errors.New("idempotency TTL, lock TTL, or response limit is invalid"))
	}
	if c.AI.Provider != "openai" {
		errs = append(errs, errors.New("AI_PROVIDER must be openai"))
	}
	if err := validateURL("OPENAI_RESPONSES_URL", c.AI.BaseURL, "http", "https"); err != nil {
		errs = append(errs, err)
	}
	if c.Environment == EnvironmentProduction && strings.HasPrefix(c.AI.BaseURL, "http://") {
		errs = append(errs, errors.New("OPENAI_RESPONSES_URL must use HTTPS in production"))
	}
	if c.AI.APIKey != "" && strings.TrimSpace(c.AI.DefaultModel) == "" {
		errs = append(errs, errors.New("AI_DEFAULT_MODEL is required when OPENAI_API_KEY is configured"))
	}
	if c.AI.Timeout <= 0 || c.AI.MaxInputBytes < 1024 || c.AI.MaxInputBytes > 64<<20 || c.AI.MaxOutputBytes < 1024 || c.AI.MaxOutputBytes > 64<<20 || c.AI.MaxRetries < 0 || c.AI.MaxRetries > 5 {
		errs = append(errs, errors.New("AI timeout, byte limits, or retry count is invalid"))
	}
	if c.Workflow.PollInterval <= 0 || c.Workflow.LeaseDuration <= 0 || c.Workflow.Heartbeat <= 0 || c.Workflow.Heartbeat >= c.Workflow.LeaseDuration {
		errs = append(errs, errors.New("workflow poll, lease, and heartbeat durations are invalid"))
	}
	if _, err := parseEncryptionKey(c.Secrets.EncryptionKey); err != nil {
		errs = append(errs, fmt.Errorf("PLATFORM_ENCRYPTION_KEY: %w", err))
	}
	if (c.Environment == EnvironmentStaging || c.Environment == EnvironmentProduction) && c.Secrets.EncryptionKey == "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f" {
		errs = append(errs, errors.New("the documented development encryption key is forbidden in shared environments"))
	}
	if err := validateURL("GITHUB_API_BASE_URL", c.GitHub.APIBaseURL, "https"); err != nil {
		errs = append(errs, err)
	}
	if c.GitHub.RequestTimeout <= 0 || c.GitHub.CredentialTTL <= 0 || c.GitHub.CredentialTTL > 7*24*time.Hour || strings.TrimSpace(c.GitHub.RedisPrefix) == "" {
		errs = append(errs, errors.New("GitHub timeout, credential TTL, and Redis prefix are invalid"))
	}
	if strings.TrimSpace(c.TemplateSource.GitBinary) == "" ||
		!filepath.IsAbs(c.TemplateSource.CacheRoot) || filepath.Clean(c.TemplateSource.CacheRoot) != c.TemplateSource.CacheRoot ||
		strings.ContainsAny(c.TemplateSource.CacheRoot, ",\r\n\x00") ||
		c.TemplateSource.FetchTimeout <= 0 || c.TemplateSource.FetchTimeout > 10*time.Minute ||
		len(c.TemplateSource.AllowedHosts) == 0 {
		errs = append(errs, errors.New("Template source Git binary, cache root, host allowlist, or timeout is invalid"))
	}
	for _, host := range c.TemplateSource.AllowedHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || strings.ContainsAny(host, "/:@\r\n\x00") {
			errs = append(errs, errors.New("TEMPLATE_SOURCE_ALLOWED_HOSTS contains an invalid exact hostname"))
			break
		}
	}
	if err := validateRepositoryConfig(c.Repository); err != nil {
		errs = append(errs, err)
	}
	if strings.TrimSpace(c.Delivery.SandboxRuntime) == "" || strings.TrimSpace(c.Delivery.SandboxNodeImage) == "" || strings.TrimSpace(c.Delivery.SandboxGoImage) == "" {
		errs = append(errs, errors.New("delivery sandbox runtime and fixed images are required"))
	}
	if c.Sandbox.Enabled && !deliveryImageDigestPattern.MatchString(c.Sandbox.RunnerImage) {
		errs = append(errs, errors.New("SANDBOX_RUNNER_IMAGE must use an immutable @sha256: digest when SANDBOX_ENABLED is true"))
	}
	runtimeBase := strings.ToLower(filepath.Base(strings.TrimSpace(c.Sandbox.RuntimeBinary)))
	if runtimeBase != "docker" && runtimeBase != "podman" {
		errs = append(errs, errors.New("SANDBOX_RUNTIME must select docker or podman"))
	}
	if c.Sandbox.DaemonHost != "" && !strings.HasPrefix(c.Sandbox.DaemonHost, "unix:///") &&
		!strings.HasPrefix(c.Sandbox.DaemonHost, "tcp://") {
		errs = append(errs, errors.New("SANDBOX_DAEMON_HOST must use unix:/// or tcp://"))
	}
	if !filepath.IsAbs(c.Sandbox.WorkspaceRoot) || filepath.Clean(c.Sandbox.WorkspaceRoot) != c.Sandbox.WorkspaceRoot ||
		strings.ContainsAny(c.Sandbox.WorkspaceRoot, ",\r\n\x00") ||
		!deliveryResolverNetworkPattern.MatchString(c.Sandbox.GatewayNetwork) ||
		c.Sandbox.GatewayNetwork == "host" || c.Sandbox.GatewayNetwork == "none" ||
		c.Sandbox.StartupTimeout <= 0 || c.Sandbox.StartupTimeout > 10*time.Minute ||
		c.Sandbox.CommandTimeout <= 0 || c.Sandbox.CommandTimeout > time.Minute ||
		c.Sandbox.RuntimeOutputMax < 1024 || c.Sandbox.RuntimeOutputMax > 8<<20 {
		errs = append(errs, errors.New("interactive sandbox runtime path, timeouts, or output limit is invalid"))
	}
	if err := validateSandboxPreviewConfig(c.Sandbox, c.Security.Session.CookieDomain); err != nil {
		errs = append(errs, err)
	}
	if c.Sandbox.CPUMillis < 100 || c.Sandbox.CPUMillis > 64_000 ||
		c.Sandbox.MemoryBytes < 64<<20 || c.Sandbox.MemoryBytes > 256<<30 ||
		c.Sandbox.WorkspaceBytes < 1<<20 || c.Sandbox.WorkspaceBytes > 1<<40 ||
		c.Sandbox.PIDLimit < 1 || c.Sandbox.PIDLimit > 32_768 ||
		c.Sandbox.PreviewPortLimit < 0 || c.Sandbox.PreviewPortLimit > 64 ||
		c.Sandbox.IdleHibernateAfter <= 0 || c.Sandbox.MaxRuntime <= 0 ||
		c.Sandbox.IdleHibernateAfter > c.Sandbox.MaxRuntime || c.Sandbox.MaxRuntime > 7*24*time.Hour ||
		c.Sandbox.LifecyclePoll < time.Second || c.Sandbox.LifecyclePoll > time.Minute ||
		c.Sandbox.LifecycleLease < time.Second || c.Sandbox.LifecycleLease > time.Hour ||
		c.Sandbox.LifecycleRetry < time.Second || c.Sandbox.LifecycleRetry > time.Hour ||
		len(c.Sandbox.LifecycleWorkerID) > 200 || strings.ContainsAny(c.Sandbox.LifecycleWorkerID, "\r\n\x00") {
		errs = append(errs, errors.New("interactive sandbox quota or TTL configuration is invalid"))
	}
	if c.Sandbox.ConnectionTicketTTL < 5*time.Second || c.Sandbox.ConnectionTicketTTL > 2*time.Minute ||
		c.Sandbox.StreamMaxEvents < 16 || c.Sandbox.StreamMaxEvents > 100_000 ||
		c.Sandbox.StreamRetention < time.Minute || c.Sandbox.StreamRetention > 7*24*time.Hour ||
		strings.TrimSpace(c.Sandbox.RedisPrefix) == "" || len(c.Sandbox.RedisPrefix) > 200 ||
		strings.ContainsAny(c.Sandbox.RedisPrefix, "\r\n\x00") {
		errs = append(errs, errors.New("interactive sandbox connection ticket configuration is invalid"))
	}
	if err := validateLSPConfig(c.LSP, c.Sandbox.Enabled, c.Environment); err != nil {
		errs = append(errs, err)
	}
	if err := validateVerificationConfig(c.Verification); err != nil {
		errs = append(errs, err)
	}
	if err := validateAgentConfig(c.Agent); err != nil {
		errs = append(errs, err)
	}
	if c.Agent.WorkerEnabled && strings.TrimSpace(c.AI.APIKey) == "" {
		errs = append(errs, errors.New("AGENT_WORKER_ENABLED requires OPENAI_API_KEY for the platform Model Gateway"))
	}
	if c.Agent.WorkerEnabled && c.HTTP.WriteTimeout < c.Agent.WallTime+30*time.Second {
		errs = append(errs, errors.New("HTTP_WRITE_TIMEOUT must cover AGENT_WALL_TIME plus the Model Gateway shutdown margin"))
	}
	if (c.Environment == EnvironmentStaging || c.Environment == EnvironmentProduction) &&
		(!deliveryImageDigestPattern.MatchString(c.Delivery.SandboxNodeImage) || !deliveryImageDigestPattern.MatchString(c.Delivery.SandboxGoImage)) {
		errs = append(errs, errors.New("DELIVERY_SANDBOX_NODE_IMAGE and DELIVERY_SANDBOX_GO_IMAGE must use immutable @sha256: digests in staging and production"))
	}
	if c.Delivery.SandboxHost != "" && !strings.HasPrefix(c.Delivery.SandboxHost, "unix:///") && !strings.HasPrefix(c.Delivery.SandboxHost, "tcp://") {
		errs = append(errs, errors.New("DELIVERY_SANDBOX_HOST must use unix:/// or tcp://"))
	}
	if c.Delivery.SandboxTimeout <= 0 || c.Delivery.SandboxTimeout > 30*time.Minute ||
		c.Delivery.SandboxOutputMax < 1024 || c.Delivery.SandboxOutputMax > 8<<20 ||
		strings.TrimSpace(c.Delivery.SandboxMemory) == "" || strings.TrimSpace(c.Delivery.SandboxCPUs) == "" ||
		c.Delivery.SandboxPIDs < 16 || c.Delivery.SandboxPIDs > 4096 {
		errs = append(errs, errors.New("delivery sandbox limits are invalid"))
	}
	if !deliveryResolverNetworkPattern.MatchString(c.Delivery.ResolverNetwork) || c.Delivery.ResolverNetwork == "host" || c.Delivery.ResolverNetwork == "none" || strings.HasPrefix(c.Delivery.ResolverNetwork, "container") ||
		c.Delivery.ResolverTimeout <= 0 || c.Delivery.ResolverTimeout > 30*time.Minute ||
		c.Delivery.ResolverOutputMax < 1024 || c.Delivery.ResolverOutputMax > 8<<20 ||
		strings.TrimSpace(c.Delivery.ResolverMemory) == "" || strings.TrimSpace(c.Delivery.ResolverCPUs) == "" ||
		c.Delivery.ResolverPIDs < 16 || c.Delivery.ResolverPIDs > 4096 || !deliverySumDBPattern.MatchString(c.Delivery.ResolverGoSumDB) || strings.EqualFold(c.Delivery.ResolverGoSumDB, "off") {
		errs = append(errs, errors.New("delivery dependency resolver network and limits are invalid"))
	}
	if err := validateURL("DELIVERY_RESOLVER_NPM_REGISTRY", c.Delivery.ResolverNPMRegistry, "https"); err != nil {
		errs = append(errs, err)
	} else if err := validateResolverEndpoint("DELIVERY_RESOLVER_NPM_REGISTRY", c.Delivery.ResolverNPMRegistry); err != nil {
		errs = append(errs, err)
	}
	if strings.Contains(c.Delivery.ResolverGoProxy, ",") {
		errs = append(errs, errors.New("DELIVERY_RESOLVER_GO_PROXY cannot include direct or fallback resolvers"))
	} else if err := validateURL("DELIVERY_RESOLVER_GO_PROXY", c.Delivery.ResolverGoProxy, "https"); err != nil {
		errs = append(errs, err)
	} else if err := validateResolverEndpoint("DELIVERY_RESOLVER_GO_PROXY", c.Delivery.ResolverGoProxy); err != nil {
		errs = append(errs, err)
	}
	if strings.HasPrefix(c.Delivery.SandboxHost, "tcp://") && !filepath.IsAbs(c.Delivery.QualityTempRoot) {
		errs = append(errs, errors.New("remote delivery sandbox requires absolute DELIVERY_QUALITY_TEMP_ROOT shared by API and daemon"))
	}
	if strings.TrimSpace(c.Delivery.PublishRoot) == "" || strings.TrimSpace(c.Delivery.PublishBaseURL) == "" {
		errs = append(errs, errors.New("delivery publish root and base URL are required"))
	}
	if c.Delivery.ReleaseWorkerEnabled {
		schemes := []string{"https"}
		if err := validateURL("RELEASE_DELIVERY_CONTROLLER_URL", c.Delivery.ReleaseControllerURL, schemes...); err != nil {
			errs = append(errs, err)
		}
		controller, err := url.Parse(strings.TrimSpace(c.Delivery.ReleaseControllerURL))
		if err != nil || controller.User != nil || controller.RawQuery != "" || controller.Fragment != "" {
			errs = append(errs, errors.New("RELEASE_DELIVERY_CONTROLLER_URL cannot contain credentials, query, or fragment"))
		}
		if len(strings.TrimSpace(c.Delivery.ReleaseControllerToken)) < 32 ||
			strings.ContainsAny(c.Delivery.ReleaseControllerToken, "\r\n\x00") {
			errs = append(errs, errors.New("RELEASE_DELIVERY_CONTROLLER_TOKEN must be a non-header-injectable secret of at least 32 characters"))
		}
		if strings.TrimSpace(c.Delivery.ReleaseControllerID) != c.Delivery.ReleaseControllerID ||
			len(c.Delivery.ReleaseControllerID) == 0 || len(c.Delivery.ReleaseControllerID) > 200 ||
			strings.ContainsAny(c.Delivery.ReleaseControllerID, "\r\n\x00") ||
			strings.TrimSpace(c.Delivery.ReleaseControllerVersion) != c.Delivery.ReleaseControllerVersion ||
			len(c.Delivery.ReleaseControllerVersion) == 0 || len(c.Delivery.ReleaseControllerVersion) > 120 ||
			strings.ContainsAny(c.Delivery.ReleaseControllerVersion, "\r\n\x00") ||
			c.Delivery.ReleaseControllerProtocol != "worksflow.release-delivery/v3" ||
			!agentHashPattern.MatchString(c.Delivery.ReleaseControllerTrustKeyDigest) {
			errs = append(errs, errors.New("release delivery controller exact id, version, v3 protocol, and TLS SPKI digest are required"))
		}
		if c.Delivery.ReleaseLeaseDuration < 30*time.Second || c.Delivery.ReleaseLeaseDuration > time.Hour ||
			c.Delivery.ReleasePollInterval <= 0 || c.Delivery.ReleasePollInterval > time.Minute ||
			c.Delivery.ReleaseReconcileDelay < time.Second || c.Delivery.ReleaseReconcileDelay > time.Hour ||
			c.Delivery.ReleaseRequestTimeout < time.Second || c.Delivery.ReleaseRequestTimeout > 5*time.Minute ||
			c.Delivery.ReleaseResponseMax < 1024 || c.Delivery.ReleaseResponseMax > 8<<20 {
			errs = append(errs, errors.New("release delivery worker lease, poll, request timeout, or response limit is invalid"))
		}
	} else if strings.TrimSpace(c.Delivery.ReleaseControllerURL) != "" || strings.TrimSpace(c.Delivery.ReleaseControllerToken) != "" ||
		strings.TrimSpace(c.Delivery.ReleaseControllerID) != "" || strings.TrimSpace(c.Delivery.ReleaseControllerVersion) != "" ||
		c.Delivery.ReleaseControllerProtocol != "worksflow.release-delivery/v3" || strings.TrimSpace(c.Delivery.ReleaseControllerTrustKeyDigest) != "" {
		errs = append(errs, errors.New("release delivery controller settings require RELEASE_DELIVERY_WORKER_ENABLED"))
	}
	return errors.Join(errs...)
}

func validateRepositoryConfig(repository RepositoryConfig) error {
	index := repository.SearchIndex
	if index.MaxTrees < 1 || index.MaxTrees > 10_000 ||
		index.MaxSourceBytes < 1 || index.MaxSourceBytes > 1<<40 ||
		index.MaxActiveBuilds < 1 || index.MaxActiveBuilds > index.MaxTrees {
		return errors.New("repository exact-tree search index project quota is invalid")
	}
	admission := repository.SearchAdmission
	if admission.RedisPrefix == "" || admission.RedisPrefix != strings.TrimSpace(admission.RedisPrefix) ||
		len(admission.RedisPrefix) > 200 || strings.ContainsAny(admission.RedisPrefix, "{}\r\n\x00") ||
		admission.Timeout < time.Millisecond || admission.Timeout > 250*time.Millisecond ||
		admission.Timeout%time.Millisecond != 0 {
		return errors.New("repository exact-tree search Redis admission boundary is invalid")
	}
	for _, bucket := range []RepositorySearchRateBucketConfig{
		admission.QueryProject, admission.QueryActor,
		admission.BuildProject, admission.BuildActor,
	} {
		if bucket.RefillTokens < 1 || bucket.RefillTokens > 10_000 ||
			bucket.RefillInterval < 10*time.Millisecond || bucket.RefillInterval > time.Hour ||
			bucket.RefillInterval%time.Millisecond != 0 ||
			bucket.Burst < bucket.RefillTokens || bucket.Burst > 100_000 {
			return errors.New("repository exact-tree search rate bucket is invalid")
		}
		fillMilliseconds := bucket.Burst * bucket.RefillInterval.Milliseconds() / bucket.RefillTokens
		if fillMilliseconds <= 0 || fillMilliseconds > int64((24*time.Hour)/time.Millisecond) {
			return errors.New("repository exact-tree search rate bucket fill horizon is invalid")
		}
	}
	return nil
}

func validateVerificationConfig(verification VerificationConfig) error {
	var errs []error
	runtimeBase := strings.ToLower(filepath.Base(strings.TrimSpace(verification.RuntimeBinary)))
	if runtimeBase != "docker" && runtimeBase != "podman" {
		errs = append(errs, errors.New("VERIFICATION_RUNTIME must select docker or podman"))
	}
	if verification.WorkerEnabled && runtimeBase == "podman" && strings.TrimSpace(verification.DaemonHost) == "" {
		errs = append(errs, errors.New("VERIFICATION_DAEMON_HOST is required for the Podman verification client"))
	}
	if verification.DaemonHost != "" && !strings.HasPrefix(verification.DaemonHost, "unix:///") &&
		!strings.HasPrefix(verification.DaemonHost, "tcp://") {
		errs = append(errs, errors.New("VERIFICATION_DAEMON_HOST must use unix:/// or tcp://"))
	}
	if !filepath.IsAbs(verification.WorkspaceRoot) ||
		filepath.Clean(verification.WorkspaceRoot) != verification.WorkspaceRoot ||
		strings.ContainsAny(verification.WorkspaceRoot, ",\r\n\x00") {
		errs = append(errs, errors.New("VERIFICATION_WORKSPACE_ROOT must be an absolute normalized private path"))
	}
	if strings.TrimSpace(verification.Memory) == "" || strings.TrimSpace(verification.CPUs) == "" ||
		verification.PIDs < 16 || verification.PIDs > 4096 ||
		verification.OutputMax < 1024 || verification.OutputMax > 8<<20 ||
		verification.TempBytes < 1<<20 || verification.TempBytes > 8<<30 {
		errs = append(errs, errors.New("verification resource and output limits are invalid"))
	}
	if verification.PollInterval <= 0 || verification.PollInterval > time.Minute ||
		verification.LeaseDuration < time.Second || verification.LeaseDuration > 24*time.Hour ||
		verification.Heartbeat <= 0 || verification.Heartbeat >= verification.LeaseDuration {
		errs = append(errs, errors.New("verification poll, lease, and heartbeat durations are invalid"))
	}
	workerID := strings.TrimSpace(verification.WorkerID)
	if verification.WorkerID != workerID || len(workerID) > 160 || strings.ContainsAny(workerID, "\r\n\x00") {
		errs = append(errs, errors.New("VERIFICATION_WORKER_ID must be a bounded canonical worker identity"))
	}
	return errors.Join(errs...)
}

func validateAgentConfig(agent AgentConfig) error {
	var errs []error
	if agent.WorkerEnabled && !agent.Enabled {
		errs = append(errs, errors.New("AGENT_WORKER_ENABLED requires AGENT_ENABLED"))
	}
	if agent.Enabled {
		// Until a versioned executor/provider registry exists, the only runtime
		// wired by app.New is the digest-pinned Codex CLI Runner backed by the
		// OpenAI Responses Model Gateway. Do not let configurable evidence labels
		// claim a different executor than the one that actually ran.
		if agent.Adapter != "codex-cli" {
			errs = append(errs, errors.New("AGENT_EXECUTOR_ADAPTER must be codex-cli for the current Agent Runner"))
		}
		if agent.Provider != "openai" {
			errs = append(errs, errors.New("AGENT_EXECUTOR_PROVIDER must be openai for the current Model Gateway"))
		}
		for name, value := range map[string]string{
			"AGENT_EXECUTOR_PROFILE_ID": agent.ProfileID,
			"AGENT_EXECUTOR_ADAPTER":    agent.Adapter,
			"AGENT_EXECUTOR_PROVIDER":   agent.Provider,
		} {
			if strings.TrimSpace(value) != value || len(value) == 0 || len(value) > 80 ||
				!agentStableIDPattern.MatchString(value) {
				errs = append(errs, fmt.Errorf("%s must be a canonical stable identifier", name))
			}
		}
		if strings.TrimSpace(agent.Model) != agent.Model || agent.Model == "" || len(agent.Model) > 160 ||
			strings.ContainsAny(agent.Model, "\r\n\x00") {
			errs = append(errs, errors.New("AGENT_EXECUTOR_MODEL must be a bounded canonical model identity"))
		}
		if !deliveryImageDigestPattern.MatchString(agent.RunnerImage) {
			errs = append(errs, errors.New("AGENT_RUNNER_IMAGE must use an immutable @sha256: digest when AGENT_ENABLED is true"))
		}
		for name, value := range map[string]string{
			"AGENT_MODEL_POLICY_HASH":  agent.ModelPolicyHash,
			"AGENT_PARAMETERS_HASH":    agent.ParametersHash,
			"AGENT_PROMPT_HASH":        agent.PromptHash,
			"AGENT_OUTPUT_SCHEMA_HASH": agent.OutputSchemaHash,
			"AGENT_TOOLCHAIN_HASH":     agent.ToolchainHash,
		} {
			if !agentHashPattern.MatchString(value) {
				errs = append(errs, fmt.Errorf("%s must be a canonical sha256 digest", name))
			}
		}
		if len(agent.AllowedTools) == 0 || len(agent.AllowedTools) > 64 {
			errs = append(errs, errors.New("AGENT_ALLOWED_TOOLS must contain one to 64 server-qualified tools"))
		} else {
			seen := make(map[string]struct{}, len(agent.AllowedTools))
			for _, tool := range agent.AllowedTools {
				if strings.TrimSpace(tool) != tool || len(tool) == 0 || len(tool) > 120 ||
					!agentStableIDPattern.MatchString(tool) {
					errs = append(errs, errors.New("AGENT_ALLOWED_TOOLS contains an invalid stable tool ID"))
					break
				}
				if _, duplicate := seen[tool]; duplicate {
					errs = append(errs, errors.New("AGENT_ALLOWED_TOOLS contains a duplicate tool ID"))
					break
				}
				seen[tool] = struct{}{}
			}
		}
		if agent.MaxLogBytes > 64<<20 {
			errs = append(errs, errors.New("AGENT_MAX_LOG_BYTES exceeds the qualified Runner bound"))
		}
	}
	if agent.WorkerID != "" {
		if strings.TrimSpace(agent.WorkerID) != agent.WorkerID || len(agent.WorkerID) > 160 ||
			!agentStableIDPattern.MatchString(agent.WorkerID) {
			errs = append(errs, errors.New("AGENT_WORKER_ID must be an optional canonical stable identifier"))
		}
	}
	if agent.WorkerEnabled {
		runtimeBase := strings.ToLower(filepath.Base(strings.TrimSpace(agent.RuntimeBinary)))
		if runtimeBase != "docker" && runtimeBase != "podman" {
			errs = append(errs, errors.New("AGENT_RUNTIME must select docker or podman"))
		}
		if agent.DaemonHost != "" && !strings.HasPrefix(agent.DaemonHost, "unix:///") &&
			!strings.HasPrefix(agent.DaemonHost, "tcp://") {
			errs = append(errs, errors.New("AGENT_DAEMON_HOST must use unix:/// or tcp://"))
		}
		if !filepath.IsAbs(agent.WorktreeRoot) || filepath.Clean(agent.WorktreeRoot) != agent.WorktreeRoot ||
			agent.WorktreeRoot == string(filepath.Separator) || strings.ContainsAny(agent.WorktreeRoot, ",\r\n\x00") {
			errs = append(errs, errors.New("AGENT_WORKTREE_ROOT must be a clean, absolute, non-root shared path"))
		}
		if !deliveryResolverNetworkPattern.MatchString(agent.RunnerNetwork) ||
			agent.RunnerNetwork == "bridge" || agent.RunnerNetwork == "host" || agent.RunnerNetwork == "none" ||
			strings.HasPrefix(agent.RunnerNetwork, "container") {
			errs = append(errs, errors.New("AGENT_RUNNER_NETWORK must name a dedicated internal container network"))
		}
		if strings.TrimSpace(agent.RunnerMemory) == "" || strings.TrimSpace(agent.RunnerCPUs) == "" ||
			strings.ContainsAny(agent.RunnerMemory+agent.RunnerCPUs, "\r\n\x00") ||
			agent.RunnerPIDs < 16 || agent.RunnerPIDs > 4096 ||
			agent.RunnerOutputMax < 1024 || agent.RunnerOutputMax > 64<<20 {
			errs = append(errs, errors.New("Agent Runner memory, CPU, PID, or output limits are invalid"))
		}
		if err := validateAgentGatewayURL(agent.GatewayBaseURL); err != nil {
			errs = append(errs, err)
		}
		if strings.TrimSpace(agent.GatewayRedisPrefix) != agent.GatewayRedisPrefix ||
			agent.GatewayRedisPrefix == "" || len(agent.GatewayRedisPrefix) > 200 ||
			strings.ContainsAny(agent.GatewayRedisPrefix, "\r\n\x00") {
			errs = append(errs, errors.New("AGENT_MODEL_GATEWAY_REDIS_PREFIX must be a bounded non-empty prefix"))
		}
	}
	if agent.WallTime < time.Second || agent.WallTime > 8*time.Hour || agent.WallTime%time.Second != 0 ||
		agent.MaxInputTokens < 1 || agent.MaxInputTokens > 4_000_000 ||
		agent.MaxOutputTokens < 1 || agent.MaxOutputTokens > 1_000_000 ||
		agent.MaxCommands < 1 || agent.MaxCommands > 10_000 ||
		agent.MaxLogBytes < 1024 || agent.MaxLogBytes > 1<<30 ||
		agent.MaxPatchBytes < 1 || agent.MaxPatchBytes > 64<<20 ||
		agent.MaxContextFiles < 1 || agent.MaxContextFiles > 480 ||
		agent.ClaimBatch < 1 || agent.ClaimBatch > 100 {
		errs = append(errs, errors.New("Agent task budgets or ContextPack file limit are outside platform bounds"))
	}
	if agent.PollInterval <= 0 || agent.PollInterval > time.Minute ||
		agent.LeaseDuration < time.Second || agent.LeaseDuration > 10*time.Minute || agent.LeaseDuration%time.Second != 0 ||
		agent.Heartbeat <= 0 || agent.Heartbeat >= agent.LeaseDuration {
		errs = append(errs, errors.New("Agent worker poll, lease, and heartbeat durations are invalid"))
	}
	return errors.Join(errs...)
}

func validateAgentGatewayURL(value string) error {
	parsed, err := url.Parse(value)
	host := ""
	if err == nil {
		host = parsed.Hostname()
	}
	ip := net.ParseIP(host)
	if value == "" || value != strings.TrimSpace(value) || err != nil || parsed.User != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/internal/agent-model/v1" ||
		host == "" || strings.EqualFold(host, "localhost") ||
		(ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) {
		return errors.New("AGENT_MODEL_GATEWAY_BASE_URL must be an internal non-loopback HTTP(S) URL ending in /internal/agent-model/v1")
	}
	return nil
}

func parseEncryptionKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.StdEncoding} {
		if decoded, err := encoding.DecodeString(value); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, errors.New("must encode exactly 32 bytes as hex or base64")
}

type envLoader struct {
	errs []error
}

func (l *envLoader) string(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

func (l *envLoader) rawString(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func (l *envLoader) openAIResponsesURL() string {
	if value := l.string("OPENAI_RESPONSES_URL", ""); value != "" {
		return value
	}
	base := l.string("OPENAI_BASE_URL", "")
	if base == "" {
		return "https://api.openai.com/v1/responses"
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/responses") {
		if !strings.HasSuffix(path, "/v1") {
			path += "/v1"
		}
		path += "/responses"
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed.String()
}

func (l *envLoader) openAIDefaultModel() string {
	if value := l.string("OPENAI_DEFAULT_MODEL", ""); value != "" {
		return value
	}
	return l.string("AI_DEFAULT_MODEL", "gpt-5")
}

func (l *envLoader) integer(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s must be an integer: %w", key, err))
		return fallback
	}
	return value
}

func (l *envLoader) boolean(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s must be a boolean: %w", key, err))
		return fallback
	}
	return value
}

func (l *envLoader) duration(key string, fallback time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s must be a duration: %w", key, err))
		return fallback
	}
	return value
}

func (l *envLoader) list(key string, fallback []string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return append([]string(nil), fallback...)
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func validateURL(name, raw string, schemes ...string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || !oneOf(parsed.Scheme, schemes...) {
		return fmt.Errorf("%s must be a valid %s URL", name, strings.Join(schemes, "/"))
	}
	return nil
}

func validatePostgresDSN(raw string) error {
	invalid := func(detail string) error {
		return fmt.Errorf("POSTGRES_DSN %s", detail)
	}
	if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "\r\n\x00") {
		return invalid("must be a canonical PostgreSQL URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" ||
		(parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") ||
		parsed.User == nil || parsed.User.Username() == "" ||
		strings.Trim(parsed.Path, "/") == "" || parsed.Fragment != "" {
		return invalid("must be a postgres/postgresql URL with user, host, and database name")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return invalid("contains an invalid query")
	}
	for key, values := range query {
		if len(values) != 1 {
			return invalid("contains a duplicate or unsupported query parameter")
		}
		value := values[0]
		switch key {
		case "sslmode":
			if !oneOf(value, "disable", "allow", "prefer", "require", "verify-ca", "verify-full") {
				return invalid("contains an invalid sslmode")
			}
		case "sslcert", "sslkey", "sslrootcert":
			if !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsAny(value, "\r\n\x00") {
				return invalid("contains an invalid TLS file path")
			}
		case "connect_timeout":
			seconds, parseErr := strconv.Atoi(value)
			if parseErr != nil || seconds < 1 || seconds > 300 {
				return invalid("contains an invalid connect_timeout")
			}
		case "target_session_attrs":
			if !oneOf(value, "any", "read-write", "read-only", "primary", "standby", "prefer-standby") {
				return invalid("contains invalid target_session_attrs")
			}
		case "application_name":
			if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
				return invalid("contains an invalid application_name")
			}
		default:
			// Identity/role/search-path overrides and query-carried secrets are
			// intentionally unsupported. POSTGRES_SCHEMA is the sole schema input.
			return invalid("contains an unsupported query parameter")
		}
	}
	return nil
}

func validPostgresSchema(value string) bool {
	return postgresSchemaPattern.MatchString(value) &&
		!strings.HasPrefix(value, "pg_") && value != "information_schema"
}

func validateResolverEndpoint(name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return fmt.Errorf("%s must be credential-free and contain no query or fragment", name)
	}
	return nil
}

func validateLSPConfig(value LSPConfig, sandboxEnabled bool, environment string) error {
	if value.Enabled && !sandboxEnabled {
		return errors.New("LSP_ENABLED requires SANDBOX_ENABLED because LSP binds an exact ready SandboxSession workspace")
	}
	runtimeBinary := strings.TrimSpace(value.RuntimeBinary)
	runtimeBase := strings.ToLower(filepath.Base(runtimeBinary))
	if runtimeBinary != value.RuntimeBinary || !filepath.IsAbs(runtimeBinary) ||
		filepath.Clean(runtimeBinary) != runtimeBinary || strings.ContainsAny(runtimeBinary, "\r\n\x00") ||
		(runtimeBase != "docker" && runtimeBase != "podman") {
		return errors.New("LSP_RUNTIME_BINARY must be a clean absolute docker or podman executable path")
	}
	if value.DaemonHost != "" {
		parsed, err := url.Parse(value.DaemonHost)
		if err != nil || parsed.Scheme != "unix" || parsed.Host != "" || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" ||
			!filepath.IsAbs(parsed.Path) || filepath.Clean(parsed.Path) != parsed.Path {
			return errors.New("LSP_DAEMON_HOST must be an absolute unix:/// socket URL")
		}
	}
	shared := environment == EnvironmentStaging || environment == EnvironmentProduction
	if value.Enabled && shared && !value.RequireTLS {
		return errors.New("LSP_REQUIRE_TLS must be true in staging and production")
	}
	if len(value.AllowedOrigins) == 0 || len(value.AllowedOrigins) > 64 {
		return errors.New("LSP_ALLOWED_ORIGINS must contain between one and 64 exact browser origins")
	}
	seenOrigins := make(map[string]bool, len(value.AllowedOrigins))
	for _, raw := range value.AllowedOrigins {
		origin, err := canonicalLSPConfigOrigin(raw)
		if err != nil || seenOrigins[origin] || value.Enabled && shared && !strings.HasPrefix(origin, "https://") {
			return errors.New("LSP_ALLOWED_ORIGINS contains an invalid, duplicate, or insecure shared-environment origin")
		}
		seenOrigins[origin] = true
	}
	if value.TicketTTL < 5*time.Second || value.TicketTTL > 30*time.Second ||
		value.BindTimeout < time.Millisecond || value.BindTimeout > 5*time.Second ||
		value.WriteWait < time.Millisecond || value.WriteWait > 5*time.Second ||
		value.CommandTimeout <= 0 || value.CommandTimeout > 30*time.Second ||
		value.CLIOutputMax < 1024 || value.CLIOutputMax > 8<<20 {
		return errors.New("LSP ticket, transport, runtime timeouts, or CLI output limit is invalid")
	}
	if value.RedisPrefix == "" || strings.TrimSpace(value.RedisPrefix) != value.RedisPrefix ||
		len(value.RedisPrefix) > 160 || strings.ContainsAny(value.RedisPrefix, "\r\n\x00") ||
		value.RefillPerSecond < 1 || value.RefillPerSecond > 10_000 ||
		value.Burst < value.RefillPerSecond || value.Burst > 20_000 {
		return errors.New("LSP Redis prefix or admission rate limit is invalid")
	}
	return nil
}

func canonicalLSPConfigOrigin(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\x00") {
		return "", errors.New("invalid LSP origin")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Opaque != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid LSP origin")
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "" {
		return "", errors.New("invalid LSP origin")
	}
	if parsed.Scheme == "http" {
		address := net.ParseIP(hostname)
		if hostname != "localhost" && !strings.HasSuffix(hostname, ".localhost") &&
			(address == nil || !address.IsLoopback()) {
			return "", errors.New("invalid LSP origin")
		}
	}
	host := hostname
	if port := parsed.Port(); port != "" {
		value, parseErr := strconv.Atoi(port)
		if parseErr != nil || value < 1 || value > 65535 {
			return "", errors.New("invalid LSP origin")
		}
		host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	return parsed.Scheme + "://" + host, nil
}

var sandboxPreviewHostPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)

func validateSandboxPreviewConfig(sandbox SandboxConfig, sessionCookieDomain string) error {
	bindAddress := strings.TrimSpace(sandbox.GatewayBindAddress)
	if ip := net.ParseIP(bindAddress); ip == nil || ip.IsUnspecified() && strings.TrimSpace(sandbox.DaemonHost) == "" {
		return errors.New("SANDBOX_GATEWAY_BIND_ADDRESS must be an IP address; wildcard binding requires an explicit isolated daemon host")
	}
	dialHost := strings.TrimSpace(sandbox.PreviewDialHost)
	if dialHost == "" || len(dialHost) > 253 || strings.ContainsAny(dialHost, "/?#@:\r\n\x00") {
		return errors.New("SANDBOX_PREVIEW_DIAL_HOST must be a bounded host name or IP address")
	}
	origin, err := url.Parse(strings.TrimSpace(sandbox.PreviewPublicOrigin))
	if err != nil || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" ||
		(origin.Path != "" && origin.Path != "/") || origin.RawPath != "" ||
		!oneOf(strings.ToLower(origin.Scheme), "http", "https") || origin.Host == "" {
		return errors.New("SANDBOX_PREVIEW_PUBLIC_ORIGIN must be an http/https origin without credentials, path, query, or fragment")
	}
	baseHost := strings.ToLower(strings.TrimSuffix(origin.Hostname(), "."))
	if net.ParseIP(baseHost) != nil || !sandboxPreviewHostPattern.MatchString(baseHost) ||
		strings.Contains(baseHost, "..") || !strings.Contains(baseHost, ".") {
		return errors.New("SANDBOX_PREVIEW_PUBLIC_ORIGIN must use a DNS base domain that accepts capability subdomains")
	}
	if port := origin.Port(); port != "" {
		parsed, parseErr := strconv.Atoi(port)
		if parseErr != nil || parsed < 1 || parsed > 65535 {
			return errors.New("SANDBOX_PREVIEW_PUBLIC_ORIGIN contains an invalid port")
		}
	}
	cookieDomain := strings.ToLower(strings.Trim(strings.TrimSpace(sessionCookieDomain), "."))
	if cookieDomain != "" && (baseHost == cookieDomain || strings.HasSuffix(baseHost, "."+cookieDomain)) {
		return errors.New("SANDBOX_PREVIEW_PUBLIC_ORIGIN must not fall under SESSION_COOKIE_DOMAIN")
	}
	if sandbox.PreviewTicketTTL < 30*time.Second || sandbox.PreviewTicketTTL > time.Hour ||
		sandbox.PreviewProbeTimeout < 50*time.Millisecond || sandbox.PreviewProbeTimeout > 5*time.Second {
		return errors.New("sandbox preview ticket TTL or probe timeout is invalid")
	}
	return nil
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(value, expected) {
			return true
		}
	}
	return false
}

func missingFold(values []string, required []string) []string {
	missing := make([]string, 0)
	for _, expected := range required {
		if !containsFold(values, expected) {
			missing = append(missing, expected)
		}
	}
	return missing
}

func oneOf(value string, allowed ...string) bool {
	return contains(allowed, value)
}

func validCookieName(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", character) {
			return false
		}
	}
	return true
}
