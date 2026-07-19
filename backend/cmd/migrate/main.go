package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/migrations"
)

const (
	migrationDSNEnvironment     = "WORKSFLOW_MIGRATION_POSTGRES_DSN"
	migrationSchemaEnvironment  = "WORKSFLOW_MIGRATION_POSTGRES_SCHEMA"
	migrationTimeoutEnvironment = "WORKSFLOW_MIGRATION_TIMEOUT"
	defaultMigrationTimeout     = 30 * time.Minute
	minimumMigrationTimeout     = time.Second
	maximumMigrationTimeout     = 2 * time.Hour
)

var migrationSchemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

type migrationSettings struct {
	DSN     string
	Schema  string
	Timeout time.Duration
}

type migrationSummary struct {
	TotalAppliedCount int
	Latest            string
}

type migrationExecutor func(context.Context, string, string) (migrationSummary, error)

func main() {
	logger := migrationLogger(os.Stdout)
	settings, err := loadMigrationSettings(os.LookupEnv)
	if err != nil {
		logger.Error("database migration configuration is invalid",
			"event", "database_migration_configuration_invalid",
			"error", err,
		)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runMigration(ctx, settings, logger, migrateDatabase); err != nil {
		logger.Error("database migration failed",
			"event", "database_migration_failed",
			"error", err,
		)
		os.Exit(1)
	}
}

func migrationLogger(output io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo})).With(
		"service", "worksflow-migrate",
		"component", "database-migration",
	)
}

func loadMigrationSettings(lookup func(string) (string, bool)) (migrationSettings, error) {
	rawDSN, ok := lookup(migrationDSNEnvironment)
	if !ok || rawDSN == "" {
		return migrationSettings{}, fmt.Errorf("%s is required", migrationDSNEnvironment)
	}
	if rawDSN != strings.TrimSpace(rawDSN) || strings.ContainsAny(rawDSN, "\r\n\x00") {
		return migrationSettings{}, fmt.Errorf("%s must be a canonical PostgreSQL URL without surrounding whitespace", migrationDSNEnvironment)
	}
	parsed, err := url.Parse(rawDSN)
	if err != nil || parsed.Opaque != "" || parsed.RawPath != "" ||
		(parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") ||
		parsed.Host == "" || parsed.Hostname() == "" || parsed.User == nil ||
		parsed.User.Username() == "" || strings.Trim(parsed.Path, "/") == "" ||
		parsed.Fragment != "" {
		return migrationSettings{}, fmt.Errorf("%s must be a valid postgres/postgresql URL with a user, host, and database name", migrationDSNEnvironment)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return migrationSettings{}, fmt.Errorf("%s contains an invalid query", migrationDSNEnvironment)
	}
	for key, values := range query {
		if len(values) != 1 {
			return migrationSettings{}, fmt.Errorf("%s contains a duplicate or unsupported query parameter", migrationDSNEnvironment)
		}
		value := values[0]
		switch key {
		case "sslmode":
			if !migrationOneOf(value, "disable", "allow", "prefer", "require", "verify-ca", "verify-full") {
				return migrationSettings{}, fmt.Errorf("%s contains an invalid sslmode", migrationDSNEnvironment)
			}
		case "sslcert", "sslkey", "sslrootcert":
			if !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsAny(value, "\r\n\x00") {
				return migrationSettings{}, fmt.Errorf("%s contains an invalid TLS file path", migrationDSNEnvironment)
			}
		case "connect_timeout":
			seconds, parseErr := strconv.Atoi(value)
			if parseErr != nil || seconds < 1 || seconds > 300 {
				return migrationSettings{}, fmt.Errorf("%s contains an invalid connect_timeout", migrationDSNEnvironment)
			}
		case "target_session_attrs":
			if !migrationOneOf(value, "any", "read-write", "read-only", "primary", "standby", "prefer-standby") {
				return migrationSettings{}, fmt.Errorf("%s contains invalid target_session_attrs", migrationDSNEnvironment)
			}
		case "application_name":
			if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
				return migrationSettings{}, fmt.Errorf("%s contains an invalid application_name", migrationDSNEnvironment)
			}
		default:
			return migrationSettings{}, fmt.Errorf(
				"%s contains an unsupported query parameter; credentials and role/schema/identity overrides are forbidden",
				migrationDSNEnvironment,
			)
		}
	}
	schema := "public"
	if rawSchema, configured := lookup(migrationSchemaEnvironment); configured {
		schema = rawSchema
	}
	if !validMigrationSchema(schema) {
		return migrationSettings{}, fmt.Errorf(
			"%s must be one canonical unquoted non-system PostgreSQL identifier",
			migrationSchemaEnvironment,
		)
	}

	timeout := defaultMigrationTimeout
	if rawTimeout, configured := lookup(migrationTimeoutEnvironment); configured {
		if rawTimeout == "" || rawTimeout != strings.TrimSpace(rawTimeout) {
			return migrationSettings{}, fmt.Errorf("%s must be a duration", migrationTimeoutEnvironment)
		}
		timeout, err = time.ParseDuration(rawTimeout)
		if err != nil {
			return migrationSettings{}, fmt.Errorf("%s must be a duration: %w", migrationTimeoutEnvironment, err)
		}
	}
	if timeout < minimumMigrationTimeout || timeout > maximumMigrationTimeout {
		return migrationSettings{}, fmt.Errorf("%s must be between %s and %s", migrationTimeoutEnvironment, minimumMigrationTimeout, maximumMigrationTimeout)
	}
	return migrationSettings{DSN: rawDSN, Schema: schema, Timeout: timeout}, nil
}

func migrationOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validMigrationSchema(value string) bool {
	return migrationSchemaPattern.MatchString(value) &&
		!strings.HasPrefix(value, "pg_") && value != "information_schema"
}

func runMigration(
	ctx context.Context,
	settings migrationSettings,
	logger *slog.Logger,
	execute migrationExecutor,
) error {
	if logger == nil {
		return errors.New("migration logger is required")
	}
	if execute == nil {
		return errors.New("migration executor is required")
	}
	migrationCtx, cancel := context.WithTimeout(ctx, settings.Timeout)
	defer cancel()

	started := time.Now()
	logger.Info("database migration started",
		"event", "database_migration_started",
		"schema", settings.Schema,
		"timeout", settings.Timeout.String(),
	)
	summary, err := execute(migrationCtx, settings.DSN, settings.Schema)
	if err != nil {
		if cause := context.Cause(migrationCtx); cause != nil {
			return fmt.Errorf("apply database migrations: %w", cause)
		}
		return fmt.Errorf("apply database migrations: %w", err)
	}
	logger.Info("database migration completed",
		"event", "database_migration_completed",
		"total_applied_count", summary.TotalAppliedCount,
		"latest_version", summary.Latest,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	return nil
}

func migrateDatabase(ctx context.Context, dsn, schema string) (migrationSummary, error) {
	scopedDSN, err := migrationDSNWithSchema(dsn, schema)
	if err != nil {
		return migrationSummary{}, errors.New("prepare migration-only PostgreSQL connection configuration")
	}
	database, err := sql.Open("pgx", scopedDSN)
	if err != nil {
		// Driver configuration errors can reproduce arbitrary DSN query values.
		// The validated URL carries credentials only in redaction-aware userinfo,
		// but keep this boundary generic as defense in depth.
		return migrationSummary{}, errors.New("open migration-only PostgreSQL connection: driver configuration was rejected")
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(0)
	defer database.Close()

	if err := database.PingContext(ctx); err != nil {
		return migrationSummary{}, errors.New("connect migration-only PostgreSQL connection failed")
	}
	if err := migrations.Up(ctx, database); err != nil {
		return migrationSummary{}, err
	}
	applied, err := migrations.AppliedVersions(ctx, database)
	if err != nil {
		return migrationSummary{}, fmt.Errorf("read applied migration versions: %w", err)
	}
	summary := migrationSummary{TotalAppliedCount: len(applied)}
	if len(applied) > 0 {
		summary.Latest = applied[len(applied)-1].Version
	}
	return summary, nil
}

func migrationDSNWithSchema(rawDSN, schema string) (string, error) {
	parsed, err := url.Parse(rawDSN)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
