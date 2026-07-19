package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repositorygc"
)

const testCommandRunID = "ea1a6f91-c29b-45da-8b3c-89c14a6270ec"

func TestParseOptionsDefaultsAndBounds(t *testing.T) {
	options, err := parseOptions([]string{"-postgres-schema", "public", "-run-id", testCommandRunID})
	if err != nil {
		t.Fatal(err)
	}
	if options.policy != repositorygc.DefaultPolicy() || options.timeout != repositorygc.DefaultCommandTimeout ||
		options.schema != "public" || options.runID.String() != testCommandRunID {
		t.Fatalf("options = %#v", options)
	}
	if _, err := parseOptions(nil); err == nil {
		t.Fatal("parseOptions succeeded without the required PostgreSQL schema")
	}
	for _, arguments := range [][]string{
		{"-retention", "167h59m59s"},
		{"-keep-per-project", "7"},
		{"-batch-size", "101"},
		{"-capability-ttl", "15m1ms"},
		{"-timeout", "999ms"},
		{"-timeout", "30m1ms"},
		{"-postgres-schema", "QuotedSchema"},
		{"-postgres-schema", "pg_temp_1"},
		{"-postgres-schema", "information_schema"},
		{"-run-id", "00000000-0000-0000-0000-000000000000"},
		{"-run-id", "EA1A6F91-C29B-45DA-8B3C-89C14A6270EC"},
		{"unexpected"},
	} {
		arguments = append([]string{"-postgres-schema", "public", "-run-id", testCommandRunID}, arguments...)
		if _, err := parseOptions(arguments); err == nil {
			t.Fatalf("parseOptions(%v) succeeded", arguments)
		}
	}
}

func TestLoadDSNReadsOnlyDedicatedEnvironment(t *testing.T) {
	const dsn = "postgres://gc_operator:secret@postgres:5432/worksflow?sslmode=require"
	lookedUp := make([]string, 0, 1)
	actual, err := loadDSN(func(key string) (string, bool) {
		lookedUp = append(lookedUp, key)
		if key == postgresDSNEnvironment {
			return dsn, true
		}
		return "postgres://api:forbidden@postgres/worksflow", true
	})
	if err != nil || actual != dsn {
		t.Fatalf("loadDSN() = %q, %v", actual, err)
	}
	if len(lookedUp) != 1 || lookedUp[0] != postgresDSNEnvironment {
		t.Fatalf("environment lookups = %v", lookedUp)
	}

	for _, invalid := range []string{
		"", " postgres://gc@postgres/worksflow", "mysql://gc@postgres/worksflow",
		"postgres://gc@postgres/", "postgres://postgres/worksflow", "postgres://gc@:5432/worksflow",
		"postgres://gc@postgres/%77orksflow",
	} {
		if _, err := loadDSN(func(string) (string, bool) { return invalid, true }); err == nil {
			t.Fatalf("loadDSN accepted %q", invalid)
		}
	}
	for _, option := range []string{
		"password=query-secret", "passfile=/run/secrets/pgpass", "sslpassword=query-secret",
		"role=worksflow_repository_index_gc_operator", "session_authorization=worksflow_repository_index_gc_operator",
		"options=-c%20role%3Dworksflow_repository_index_gc_operator", "service=privileged",
		"search_path=public", "user=worksflow_repository_index_gc_operator",
		"statement_timeout=1000", "arbitrary_option=value",
	} {
		candidate := "postgres://gc_operator@postgres/worksflow?" + option
		if _, err := loadDSN(func(string) (string, bool) { return candidate, true }); err == nil || strings.Contains(err.Error(), "query-secret") {
			t.Fatalf("loadDSN did not safely reject option %q: %v", option, err)
		}
	}
	validOptions := "postgresql://gc_operator@postgres/worksflow?" + strings.Join([]string{
		"sslmode=verify-full",
		"sslcert=%2Fetc%2Fworksflow%2Fclient.crt",
		"sslkey=%2Fetc%2Fworksflow%2Fclient.key",
		"sslrootcert=%2Fetc%2Fworksflow%2Fca.crt",
		"connect_timeout=30",
		"target_session_attrs=read-write",
		"application_name=worksflow-repository-index-gc",
	}, "&")
	if actual, err := loadDSN(func(string) (string, bool) { return validOptions, true }); err != nil || actual != validOptions {
		t.Fatalf("loadDSN valid allowlist = %q, %v", actual, err)
	}
	for _, option := range []string{
		"sslmode=trust", "sslcert=relative.crt", "sslkey=%2Fetc%2Fworksflow%2F..%2Fclient.key",
		"connect_timeout=0", "connect_timeout=301", "target_session_attrs=writer",
		"application_name=", "sslmode=require&sslmode=verify-full",
	} {
		candidate := "postgres://gc_operator@postgres/worksflow?" + option
		if _, err := loadDSN(func(string) (string, bool) { return candidate, true }); err == nil {
			t.Fatalf("loadDSN accepted invalid allowlisted option %q", option)
		}
	}
}

func TestRunUsesBoundedContextAndWritesCanonicalResult(t *testing.T) {
	const dsn = "postgres://gc_operator:secret@postgres/worksflow"
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	result := repositorygc.Result{
		SchemaVersion: repositorygc.ResultSchemaVersion,
		RunID:         uuid.MustParse("ea1a6f91-c29b-45da-8b3c-89c14a6270ec"),
		Planned:       2, Deleted: 1, Protected: 7, Stale: 1,
		LogicalBytesReleased: 512, BlobBytesFreed: 256,
	}
	err = run(
		context.Background(), []string{"-timeout", "2s", "-postgres-schema", "public", "-run-id", testCommandRunID}, &output,
		func(string) (string, bool) { return dsn, true },
		func(actual string) (*sql.DB, error) {
			if actual != dsn+"?search_path=public" {
				t.Fatalf("open DSN = %q", actual)
			}
			return database, nil
		},
		func(ctx context.Context, actual *sql.DB, runID uuid.UUID, policy repositorygc.Policy) (repositorygc.Result, error) {
			if actual != database || policy != repositorygc.DefaultPolicy() {
				t.Fatalf("execute arguments = %p, %#v", actual, policy)
			}
			if runID.String() != testCommandRunID {
				t.Fatalf("execute run ID = %s", runID)
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Second {
				t.Fatalf("execute context is not bounded: %v, %v", deadline, ok)
			}
			return result, nil
		},
	)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	want := `{"blobBytesFreed":256,"deleted":1,"expired":0,"logicalBytesReleased":512,"planned":2,"protected":7,"runId":"ea1a6f91-c29b-45da-8b3c-89c14a6270ec","schemaVersion":"repository-exact-tree-literal-index-gc-result/v1","stale":1}` + "\n"
	if output.String() != want {
		t.Fatalf("output = %s, want %s", output.String(), want)
	}
}

func TestRunRedactsDedicatedDSNAndPasswordFromErrors(t *testing.T) {
	const dsn = "postgres://gc_operator:do-not-log@postgres/worksflow"
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	err = run(
		context.Background(), []string{"-postgres-schema", "public", "-run-id", testCommandRunID}, &bytes.Buffer{},
		func(string) (string, bool) { return dsn, true },
		func(string) (*sql.DB, error) { return database, nil },
		func(context.Context, *sql.DB, uuid.UUID, repositorygc.Policy) (repositorygc.Result, error) {
			return repositorygc.Result{}, errors.New("driver echoed " + dsn + " and do-not-log")
		},
	)
	if err == nil || strings.Contains(err.Error(), dsn) || strings.Contains(err.Error(), "do-not-log") {
		t.Fatalf("redacted error = %v", err)
	}
}
