package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLoadMigrationSettingsRequiresCanonicalDedicatedDSN(t *testing.T) {
	validDSN := "postgres://migration:secret@postgres:5432/worksflow?sslmode=disable"
	tests := []struct {
		name        string
		environment map[string]string
		wantError   string
	}{
		{name: "missing", environment: map[string]string{}, wantError: migrationDSNEnvironment + " is required"},
		{name: "empty", environment: map[string]string{migrationDSNEnvironment: ""}, wantError: migrationDSNEnvironment + " is required"},
		{name: "whitespace", environment: map[string]string{migrationDSNEnvironment: " " + validDSN}, wantError: "canonical PostgreSQL URL"},
		{name: "wrong scheme", environment: map[string]string{migrationDSNEnvironment: "mysql://migration@postgres/worksflow"}, wantError: "postgres/postgresql URL"},
		{name: "missing user", environment: map[string]string{migrationDSNEnvironment: "postgres://postgres/worksflow"}, wantError: "user, host, and database name"},
		{name: "missing database", environment: map[string]string{migrationDSNEnvironment: "postgres://migration@postgres/"}, wantError: "database name"},
		{name: "fragment", environment: map[string]string{migrationDSNEnvironment: validDSN + "#secret"}, wantError: "postgres/postgresql URL"},
		{name: "valid", environment: map[string]string{migrationDSNEnvironment: validDSN}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings, err := loadMigrationSettings(mapLookup(test.environment))
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("loadMigrationSettings() error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadMigrationSettings() error = %v", err)
			}
			if settings.DSN != validDSN || settings.Schema != "public" || settings.Timeout != defaultMigrationTimeout {
				t.Fatalf("settings = %#v", settings)
			}
		})
	}
}

func TestLoadMigrationSettingsValidatesAllowedConnectionOptions(t *testing.T) {
	validBase := "postgres://migration@postgres/worksflow?"
	for _, query := range []string{
		"sslmode=verify-full",
		"sslcert=%2Fetc%2Fworksflow%2Fclient.crt&sslkey=%2Fetc%2Fworksflow%2Fclient.key&sslrootcert=%2Fetc%2Fworksflow%2Fca.crt",
		"connect_timeout=30",
		"target_session_attrs=read-write",
		"application_name=worksflow-migrate",
	} {
		if _, err := loadMigrationSettings(mapLookup(map[string]string{
			migrationDSNEnvironment: validBase + query,
		})); err != nil {
			t.Fatalf("allowed query %q rejected: %v", query, err)
		}
	}

	for _, query := range []string{
		"sslmode=unsafe",
		"sslcert=relative.crt",
		"connect_timeout=0",
		"connect_timeout=301",
		"target_session_attrs=writer",
		"application_name=",
		"statement_timeout=1",
		"foo=bar",
		"sslmode=require&sslmode=verify-full",
	} {
		if _, err := loadMigrationSettings(mapLookup(map[string]string{
			migrationDSNEnvironment: validBase + query,
		})); err == nil {
			t.Fatalf("invalid query %q accepted", query)
		}
	}
}

func TestLoadMigrationSettingsBoundsTimeout(t *testing.T) {
	dsn := "postgresql://migration@postgres/worksflow"
	for _, test := range []struct {
		value     string
		want      time.Duration
		wantError bool
	}{
		{value: "1s", want: time.Second},
		{value: "2h", want: 2 * time.Hour},
		{value: "999ms", wantError: true},
		{value: "2h1ms", wantError: true},
		{value: "unbounded", wantError: true},
		{value: "", wantError: true},
	} {
		t.Run(test.value, func(t *testing.T) {
			settings, err := loadMigrationSettings(mapLookup(map[string]string{
				migrationDSNEnvironment:     dsn,
				migrationTimeoutEnvironment: test.value,
			}))
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), migrationTimeoutEnvironment) {
					t.Fatalf("loadMigrationSettings() error = %v", err)
				}
				return
			}
			if err != nil || settings.Timeout != test.want {
				t.Fatalf("settings = %#v, error = %v", settings, err)
			}
		})
	}
}

func TestLoadMigrationSettingsRejectsQueryCredentialsWithoutEcho(t *testing.T) {
	for _, key := range []string{"password", "PASSFILE", "sslpassword"} {
		t.Run(key, func(t *testing.T) {
			secret := "migration-secret-" + key
			_, err := loadMigrationSettings(mapLookup(map[string]string{
				migrationDSNEnvironment: "postgres://migration@postgres/worksflow?sslmode=disable&" + key + "=" + secret,
			}))
			if err == nil || !strings.Contains(err.Error(), "unsupported query parameter") {
				t.Fatalf("loadMigrationSettings() error = %v", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("configuration error exposed query credential: %v", err)
			}
		})
	}
}

func TestLoadMigrationSettingsUsesASeparateCanonicalSchema(t *testing.T) {
	dsn := "postgres://migration@postgres/worksflow?sslmode=disable"
	settings, err := loadMigrationSettings(mapLookup(map[string]string{
		migrationDSNEnvironment:    dsn,
		migrationSchemaEnvironment: "worksflow_app",
	}))
	if err != nil || settings.Schema != "worksflow_app" {
		t.Fatalf("settings = %#v, error = %v", settings, err)
	}
	for _, schema := range []string{
		"", "Public", "tenant-app", "tenant,public", "pg_catalog", "pg_temp",
		"information_schema", strings.Repeat("a", 64),
	} {
		_, err := loadMigrationSettings(mapLookup(map[string]string{
			migrationDSNEnvironment:    dsn,
			migrationSchemaEnvironment: schema,
		}))
		if err == nil || !strings.Contains(err.Error(), migrationSchemaEnvironment) {
			t.Fatalf("invalid schema %q accepted: %v", schema, err)
		}
	}
}

func TestRunMigrationUsesBoundedContextAndDoesNotLogDSN(t *testing.T) {
	var output bytes.Buffer
	settings := migrationSettings{
		DSN:     "postgres://migration:do-not-log@postgres/worksflow",
		Schema:  "worksflow_app",
		Timeout: time.Second,
	}
	deadlineObserved := false
	err := runMigration(context.Background(), settings, migrationLogger(&output), func(ctx context.Context, dsn, schema string) (migrationSummary, error) {
		if dsn != settings.DSN || schema != settings.Schema {
			t.Fatalf("migration identity = %q, %q", dsn, schema)
		}
		deadline, ok := ctx.Deadline()
		deadlineObserved = ok && time.Until(deadline) > 0 && time.Until(deadline) <= settings.Timeout
		return migrationSummary{TotalAppliedCount: 66, Latest: "000066_repository_exact_tree_literal_index_gc"}, nil
	})
	if err != nil {
		t.Fatalf("runMigration() error = %v", err)
	}
	if !deadlineObserved {
		t.Fatal("migration executor did not receive the bounded context")
	}
	logs := output.String()
	for _, event := range []string{"database_migration_started", "database_migration_completed"} {
		if !strings.Contains(logs, `"event":"`+event+`"`) {
			t.Fatalf("structured logs omit %s: %s", event, logs)
		}
	}
	if !strings.Contains(logs, `"total_applied_count":66`) || strings.Contains(logs, `"applied_count"`) {
		t.Fatalf("migration summary did not distinguish the cumulative count: %s", logs)
	}
	if strings.Contains(logs, "do-not-log") || strings.Contains(logs, settings.DSN) {
		t.Fatalf("structured logs exposed migration credentials: %s", logs)
	}
}

func TestRunMigrationReturnsDeadlineCause(t *testing.T) {
	settings := migrationSettings{DSN: "postgres://migration@postgres/worksflow", Schema: "public", Timeout: 10 * time.Millisecond}
	err := runMigration(context.Background(), settings, migrationLogger(&bytes.Buffer{}), func(ctx context.Context, _, _ string) (migrationSummary, error) {
		<-ctx.Done()
		return migrationSummary{}, errors.New("driver-specific timeout containing implementation detail")
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runMigration() error = %v, want deadline exceeded", err)
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
