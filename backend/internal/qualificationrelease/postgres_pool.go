package qualificationrelease

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

var postgresSchemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

type PostgresPoolConfig struct {
	DSN             string
	Schema          string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

func OpenPostgresPool(ctx context.Context, config PostgresPoolConfig) (*sql.DB, error) {
	if isNilInterface(ctx) || strings.TrimSpace(config.DSN) != config.DSN || config.DSN == "" ||
		!postgresSchemaPattern.MatchString(config.Schema) || strings.HasPrefix(config.Schema, "pg_") ||
		config.Schema == "information_schema" || config.MaxOpenConns < 1 ||
		config.MaxIdleConns < 0 || config.MaxIdleConns > config.MaxOpenConns ||
		config.ConnMaxLifetime <= 0 || config.ConnMaxIdleTime <= 0 {
		return nil, ErrInvalid
	}
	parsed, err := pgx.ParseConfig(config.DSN)
	if err != nil {
		return nil, wrap(ErrInvalid, "qualification release PostgreSQL configuration is invalid")
	}
	if parsed.RuntimeParams == nil {
		parsed.RuntimeParams = map[string]string{}
	}
	if _, exists := parsed.RuntimeParams["role"]; exists {
		return nil, wrap(ErrInvalid, "qualification release PostgreSQL role override is forbidden")
	}
	parsed.RuntimeParams["search_path"] = config.Schema
	database := stdlib.OpenDB(*parsed)
	database.SetMaxOpenConns(config.MaxOpenConns)
	database.SetMaxIdleConns(config.MaxIdleConns)
	database.SetConnMaxLifetime(config.ConnMaxLifetime)
	database.SetConnMaxIdleTime(config.ConnMaxIdleTime)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, wrap(ErrNotReady, "qualification release PostgreSQL readiness probe failed")
	}
	return database, nil
}
