package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
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

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repositorygc"
)

const postgresDSNEnvironment = "WORKSFLOW_REPOSITORY_INDEX_GC_POSTGRES_DSN"

var postgresSchemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

type commandOptions struct {
	policy  repositorygc.Policy
	timeout time.Duration
	schema  string
	runID   uuid.UUID
}

type databaseOpener func(string) (*sql.DB, error)
type commandExecutor func(context.Context, *sql.DB, uuid.UUID, repositorygc.Policy) (repositorygc.Result, error)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).With(
		"service", "worksflow-repository-index-gc",
		"component", "repository-exact-tree-index-gc",
	)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.LookupEnv, openDatabase, executeGC); err != nil {
		logger.Error("repository exact-tree index GC failed",
			"event", "repository_exact_tree_index_gc_failed",
			"error", err,
		)
		os.Exit(1)
	}
}

func run(
	parent context.Context,
	arguments []string,
	output io.Writer,
	lookup func(string) (string, bool),
	open databaseOpener,
	execute commandExecutor,
) error {
	if parent == nil || output == nil || lookup == nil || open == nil || execute == nil {
		return errors.New("repository exact-tree index GC command dependencies are required")
	}
	options, err := parseOptions(arguments)
	if err != nil {
		return err
	}
	rawDSN, err := loadDSN(lookup)
	if err != nil {
		return err
	}
	dsn, err := scopeDSN(rawDSN, options.schema)
	if err != nil {
		return err
	}

	database, err := open(dsn)
	if err != nil {
		return redactDatabaseError(fmt.Errorf("open GC-only PostgreSQL connection: %w", err), dsn)
	}
	if database == nil {
		return errors.New("open GC-only PostgreSQL connection: opener returned nil")
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(parent, options.timeout)
	defer cancel()
	result, err := execute(ctx, database, options.runID, options.policy)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return fmt.Errorf("repository exact-tree index GC did not complete: %w", cause)
		}
		return redactDatabaseError(err, dsn)
	}
	encoded, err := domain.CanonicalJSON(result)
	if err != nil {
		return fmt.Errorf("encode canonical GC result: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := output.Write(encoded); err != nil {
		return fmt.Errorf("write canonical GC result: %w", err)
	}
	return nil
}

func parseOptions(arguments []string) (commandOptions, error) {
	options := commandOptions{
		policy: repositorygc.DefaultPolicy(), timeout: repositorygc.DefaultCommandTimeout,
	}
	flags := flag.NewFlagSet("repository-index-gc", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	rawRunID := ""
	flags.DurationVar(&options.policy.Retention, "retention", options.policy.Retention, "minimum age of collectable tree publications")
	flags.IntVar(&options.policy.KeepPerProject, "keep-per-project", options.policy.KeepPerProject, "minimum recent trees retained per project")
	flags.IntVar(&options.policy.BatchSize, "batch-size", options.policy.BatchSize, "maximum capabilities planned in this run")
	flags.DurationVar(&options.policy.CapabilityTTL, "capability-ttl", options.policy.CapabilityTTL, "lifetime of database-issued deletion capabilities")
	flags.DurationVar(&options.timeout, "timeout", options.timeout, "whole one-shot command timeout")
	flags.StringVar(&options.schema, "postgres-schema", options.schema, "required trusted canonical PostgreSQL schema")
	flags.StringVar(&rawRunID, "run-id", rawRunID, "required stable scheduler invocation UUID")
	if err := flags.Parse(arguments); err != nil {
		return commandOptions{}, err
	}
	if flags.NArg() != 0 {
		return commandOptions{}, errors.New("positional arguments are not accepted")
	}
	if err := options.policy.Validate(); err != nil {
		return commandOptions{}, err
	}
	if options.timeout < repositorygc.MinimumCommandTimeout || options.timeout > repositorygc.MaximumCommandTimeout {
		return commandOptions{}, fmt.Errorf("timeout must be between %s and %s", repositorygc.MinimumCommandTimeout, repositorygc.MaximumCommandTimeout)
	}
	if !postgresSchemaPattern.MatchString(options.schema) || strings.HasPrefix(options.schema, "pg_") || options.schema == "information_schema" {
		return commandOptions{}, errors.New("postgres schema must be a canonical lowercase unquoted non-system identifier of at most 63 bytes")
	}
	parsedRunID, err := uuid.Parse(rawRunID)
	if err != nil || parsedRunID == uuid.Nil || parsedRunID.String() != rawRunID {
		return commandOptions{}, errors.New("run id must be a canonical lowercase non-zero UUID")
	}
	options.runID = parsedRunID
	return options, nil
}

func loadDSN(lookup func(string) (string, bool)) (string, error) {
	raw, present := lookup(postgresDSNEnvironment)
	if !present || raw == "" {
		return "", fmt.Errorf("%s is required", postgresDSNEnvironment)
	}
	if raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "\r\n\x00") {
		return "", fmt.Errorf("%s must be a canonical PostgreSQL URL without surrounding whitespace", postgresDSNEnvironment)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" ||
		(parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") ||
		parsed.Host == "" || parsed.Hostname() == "" || parsed.User == nil || parsed.User.Username() == "" ||
		strings.Trim(parsed.Path, "/") == "" || parsed.RawPath != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%s must be a valid postgres/postgresql URL with user, host, and database name", postgresDSNEnvironment)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", fmt.Errorf("%s contains an invalid query", postgresDSNEnvironment)
	}
	for key, values := range query {
		if len(values) != 1 {
			return "", fmt.Errorf("%s contains a duplicate or unsupported query parameter", postgresDSNEnvironment)
		}
		value := values[0]
		switch key {
		case "sslmode":
			if !oneOf(value, "disable", "allow", "prefer", "require", "verify-ca", "verify-full") {
				return "", fmt.Errorf("%s contains an invalid sslmode", postgresDSNEnvironment)
			}
		case "sslcert", "sslkey", "sslrootcert":
			if !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsAny(value, "\r\n\x00") {
				return "", fmt.Errorf("%s contains an invalid TLS file path", postgresDSNEnvironment)
			}
		case "connect_timeout":
			seconds, parseErr := strconv.Atoi(value)
			if parseErr != nil || seconds < 1 || seconds > 300 {
				return "", fmt.Errorf("%s contains an invalid connect_timeout", postgresDSNEnvironment)
			}
		case "target_session_attrs":
			if !oneOf(value, "any", "read-write", "read-only", "primary", "standby", "prefer-standby") {
				return "", fmt.Errorf("%s contains invalid target_session_attrs", postgresDSNEnvironment)
			}
		case "application_name":
			if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
				return "", fmt.Errorf("%s contains an invalid application_name", postgresDSNEnvironment)
			}
		default:
			return "", fmt.Errorf("%s contains an unsupported query parameter", postgresDSNEnvironment)
		}
	}
	return raw, nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func scopeDSN(dsn, schema string) (string, error) {
	if !postgresSchemaPattern.MatchString(schema) || strings.HasPrefix(schema, "pg_") || schema == "information_schema" {
		return "", errors.New("postgres schema is invalid")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", errors.New("scope GC-only PostgreSQL connection: invalid URL")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func openDatabase(dsn string) (*sql.DB, error) {
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(0)
	database.SetConnMaxLifetime(15 * time.Minute)
	database.SetConnMaxIdleTime(time.Minute)
	return database, nil
}

func executeGC(ctx context.Context, database *sql.DB, runID uuid.UUID, policy repositorygc.Policy) (repositorygc.Result, error) {
	authority, err := repositorygc.NewPostgresAuthority(database)
	if err != nil {
		return repositorygc.Result{}, err
	}
	operator, err := repositorygc.New(authority)
	if err != nil {
		return repositorygc.Result{}, err
	}
	return operator.Run(ctx, runID, policy)
}

func redactDatabaseError(err error, dsn string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	secrets := []string{dsn}
	if parsed, parseErr := url.Parse(dsn); parseErr == nil && parsed.User != nil {
		secrets = append(secrets, parsed.User.String())
		if password, present := parsed.User.Password(); present {
			secrets = append(secrets, password)
		}
	}
	if parsed, parseErr := url.Parse(dsn); parseErr == nil {
		for key, values := range parsed.Query() {
			normalized := strings.ToLower(key)
			if strings.Contains(normalized, "password") || strings.Contains(normalized, "secret") ||
				strings.Contains(normalized, "token") || strings.Contains(normalized, "credential") {
				secrets = append(secrets, values...)
			}
		}
	}
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return errors.New(message)
}
