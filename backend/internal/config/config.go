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

type Config struct {
	Environment  string
	ServiceName  string
	HTTP         HTTPConfig
	Log          LogConfig
	CORS         CORSConfig
	Security     SecurityConfig
	Dependencies DependencyConfig
	Postgres     PostgresConfig
	Redis        RedisConfig
	Mongo        MongoConfig
	NATS         NATSConfig
	WebSocket    WebSocketConfig
	Startup      StartupConfig
	Content      ContentConfig
	Outbox       OutboxConfig
	Idempotency  IdempotencyConfig
	AI           AIConfig
	Workflow     WorkflowConfig
	Secrets      SecretsConfig
	GitHub       GitHubConfig
	Delivery     DeliveryConfig
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
	Migrate            bool
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

type DeliveryConfig struct {
	SandboxRuntime      string
	SandboxHost         string
	SandboxNodeImage    string
	SandboxGoImage      string
	SandboxTimeout      time.Duration
	SandboxOutputMax    int
	SandboxMemory       string
	SandboxCPUs         string
	SandboxPIDs         int
	ResolverNetwork     string
	ResolverNPMRegistry string
	ResolverGoProxy     string
	ResolverGoSumDB     string
	ResolverTimeout     time.Duration
	ResolverOutputMax   int
	ResolverMemory      string
	ResolverCPUs        string
	ResolverPIDs        int
	QualityTempRoot     string
	PublishRoot         string
	PublishBaseURL      string
}

var deliveryResolverNetworkPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var deliverySumDBPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,255}$`)
var deliveryImageDigestPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:-]{0,255}@sha256:[a-f0-9]{64}$`)

func Load() (Config, error) {
	loader := envLoader{}
	environment := loader.string("APP_ENV", EnvironmentDevelopment)
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
			AllowedHeaders: loader.list("CORS_ALLOWED_HEADERS", []string{"Accept", "Authorization", "Content-Type", "Idempotency-Key", "If-Match", "X-CSRF-Token", "X-Request-ID"}),
			ExposedHeaders: loader.list("CORS_EXPOSED_HEADERS", []string{
				"ETag", "Idempotency-Replayed", "X-Request-ID", "Content-Disposition", "Digest",
				"X-Archive-File-Count", "X-Archive-Redaction-Count", "X-Command-ETag", "X-Command-Location",
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
			DSN:             loader.string("POSTGRES_DSN", "postgres://worksflow:worksflow@localhost:5432/worksflow?sslmode=disable"),
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
			Migrate:            loader.boolean("STARTUP_MIGRATE", mutableStartupDefault),
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
		Delivery: DeliveryConfig{
			SandboxRuntime:      loader.string("DELIVERY_SANDBOX_RUNTIME", "docker"),
			SandboxHost:         loader.string("DELIVERY_SANDBOX_HOST", ""),
			SandboxNodeImage:    loader.string("DELIVERY_SANDBOX_NODE_IMAGE", "node:22-alpine"),
			SandboxGoImage:      loader.string("DELIVERY_SANDBOX_GO_IMAGE", "golang:1.22-alpine"),
			SandboxTimeout:      loader.duration("DELIVERY_SANDBOX_TIMEOUT", 2*time.Minute),
			SandboxOutputMax:    loader.integer("DELIVERY_SANDBOX_OUTPUT_MAX_BYTES", 1<<20),
			SandboxMemory:       loader.string("DELIVERY_SANDBOX_MEMORY", "512m"),
			SandboxCPUs:         loader.string("DELIVERY_SANDBOX_CPUS", "1.0"),
			SandboxPIDs:         loader.integer("DELIVERY_SANDBOX_PIDS", 128),
			ResolverNetwork:     loader.string("DELIVERY_RESOLVER_NETWORK", "bridge"),
			ResolverNPMRegistry: loader.string("DELIVERY_RESOLVER_NPM_REGISTRY", "https://registry.npmjs.org"),
			ResolverGoProxy:     loader.string("DELIVERY_RESOLVER_GO_PROXY", "https://proxy.golang.org"),
			ResolverGoSumDB:     loader.string("DELIVERY_RESOLVER_GO_SUMDB", "sum.golang.org"),
			ResolverTimeout:     loader.duration("DELIVERY_RESOLVER_TIMEOUT", 3*time.Minute),
			ResolverOutputMax:   loader.integer("DELIVERY_RESOLVER_OUTPUT_MAX_BYTES", 1<<20),
			ResolverMemory:      loader.string("DELIVERY_RESOLVER_MEMORY", "512m"),
			ResolverCPUs:        loader.string("DELIVERY_RESOLVER_CPUS", "1.0"),
			ResolverPIDs:        loader.integer("DELIVERY_RESOLVER_PIDS", 128),
			QualityTempRoot:     loader.string("DELIVERY_QUALITY_TEMP_ROOT", ""),
			PublishRoot:         loader.string("DELIVERY_PUBLISH_ROOT", "./var/published"),
			PublishBaseURL:      loader.string("DELIVERY_PUBLISH_BASE_URL", "http://localhost:8080/published"),
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
	if !containsFold(c.CORS.AllowedHeaders, c.Security.CSRF.HeaderName) ||
		!containsFold(c.CORS.AllowedHeaders, "If-Match") ||
		!containsFold(c.CORS.AllowedHeaders, "Idempotency-Key") {
		errs = append(errs, errors.New("CORS_ALLOWED_HEADERS must include the CSRF, If-Match, and Idempotency-Key headers"))
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
	if err := validateURL("POSTGRES_DSN", c.Postgres.DSN, "postgres", "postgresql"); err != nil {
		errs = append(errs, err)
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
	if c.Outbox.BatchSize < 1 || c.Outbox.PollInterval <= 0 || c.Outbox.ClaimTTL <= 0 || c.Outbox.MaxAttempts < 1 || c.Outbox.PublishWait <= 0 {
		errs = append(errs, errors.New("outbox configuration values must be positive"))
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
	if strings.TrimSpace(c.Delivery.SandboxRuntime) == "" || strings.TrimSpace(c.Delivery.SandboxNodeImage) == "" || strings.TrimSpace(c.Delivery.SandboxGoImage) == "" {
		errs = append(errs, errors.New("delivery sandbox runtime and fixed images are required"))
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
	return errors.Join(errs...)
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

func validateResolverEndpoint(name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return fmt.Errorf("%s must be credential-free and contain no query or fragment", name)
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
