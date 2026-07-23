package qualificationinputauthority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const inputPostgresRoleCanaryLockID int64 = 804357060326886689

// This canary runs the production adapter through three independently
// authenticated LOGINs against the real migration-80 capability routines.
// Migration tests separately cover a successful append with the full WIA /
// Policy / Plan fixture; this package test deliberately uses absent upstream
// IDs so it can also prove WIP03 is surfaced without granting table reads.
func TestPostgresStoreIndependentRoleCapabilityCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	if err := base.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	roleLock, err := base.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer roleLock.Close()
	if _, err := roleLock.ExecContext(
		ctx, `SELECT pg_catalog.pg_advisory_lock($1)`, inputPostgresRoleCanaryLockID,
	); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = roleLock.ExecContext(
			context.Background(), `SELECT pg_catalog.pg_advisory_unlock($1)`, inputPostgresRoleCanaryLockID,
		)
	}()

	schema := "qualification_input_adapter_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		t.Fatal(err)
	}
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA `+postgresTestIdentifier(schema)); err != nil {
		t.Fatal(err)
	}

	operatorRoles := []string{
		"worksflow_qualification_input_precommit_operator",
		"worksflow_qualification_source_verifier_operator",
		"worksflow_qualification_credential_resolver_operator",
	}
	loginSuffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	loginRoles := map[string]string{
		postgresTestRoleInput:      "worksflow_qia_input_" + loginSuffix,
		postgresTestRoleSource:     "worksflow_qia_source_" + loginSuffix,
		postgresTestRoleCredential: "worksflow_qia_credential_" + loginSuffix,
	}
	createdOperatorRoles := make([]string, 0, len(operatorRoles))
	createdLoginRoles := make([]string, 0, len(loginRoles))
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = base.ExecContext(cleanup, `DROP SCHEMA IF EXISTS `+postgresTestIdentifier(schema)+` CASCADE`)
		for index := len(createdLoginRoles) - 1; index >= 0; index-- {
			_, _ = base.ExecContext(cleanup, `DROP ROLE IF EXISTS `+postgresTestIdentifier(createdLoginRoles[index]))
		}
		for index := len(createdOperatorRoles) - 1; index >= 0; index-- {
			_, _ = base.ExecContext(cleanup, `DROP ROLE IF EXISTS `+postgresTestIdentifier(createdOperatorRoles[index]))
		}
	})
	for _, role := range operatorRoles {
		var exists bool
		if err := base.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=$1)`, role).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Skipf("global role %s is already in use despite the role-test lock", role)
		}
	}
	for _, role := range operatorRoles {
		if _, err := base.ExecContext(ctx, `CREATE ROLE `+postgresTestIdentifier(role)+`
NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`); err != nil {
			t.Fatal(err)
		}
		createdOperatorRoles = append(createdOperatorRoles, role)
	}

	password := "qualification-input-adapter-test-password"
	for role, login := range loginRoles {
		operator := operatorRoles[0]
		switch role {
		case postgresTestRoleSource:
			operator = operatorRoles[1]
		case postgresTestRoleCredential:
			operator = operatorRoles[2]
		}
		if _, err := base.ExecContext(ctx, `CREATE ROLE `+postgresTestIdentifier(login)+`
LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD `+
			postgresTestLiteral(password)); err != nil {
			t.Fatal(err)
		}
		createdLoginRoles = append(createdLoginRoles, login)
		if _, err := base.ExecContext(ctx, `GRANT `+postgresTestIdentifier(operator)+` TO `+
			postgresTestIdentifier(login)+` WITH INHERIT TRUE, SET FALSE, ADMIN FALSE`); err != nil {
			t.Fatal(err)
		}
	}

	migrationDatabase, err := sql.Open("pgx", inputPostgresDSN(t, dsn, "", "", schema))
	if err != nil {
		t.Fatal(err)
	}
	defer migrationDatabase.Close()
	applyInputPostgresMigrationPrefix(t, ctx, migrationDatabase)

	resolved := testResolvedAuthorities()
	for _, binding := range []struct {
		role    string
		binding ExecutableBinding
	}{
		{ReceiptKindSource, resolved.SourceVerifier},
		{ReceiptKindCredential, resolved.CredentialResolver},
	} {
		var generation int64
		if err := migrationDatabase.QueryRowContext(ctx, `
SELECT generation
FROM review_qualification_input_precommit_executable_binding_v1($1,1,$2,$3,NULL)`,
			binding.role, binding.binding.AuthorityID, binding.binding.ExecutableDigest,
		).Scan(&generation); err != nil {
			t.Fatalf("review %s binding: %v", binding.role, err)
		}
		if generation != 1 {
			t.Fatalf("reviewed generation = %d", generation)
		}
	}

	roleDatabases := make(map[string]*sql.DB, len(loginRoles))
	for role, login := range loginRoles {
		database, err := sql.Open("pgx", inputPostgresDSN(t, dsn, login, password, schema))
		if err != nil {
			t.Fatal(err)
		}
		database.SetMaxOpenConns(4)
		database.SetMaxIdleConns(4)
		roleDatabases[role] = database
		defer database.Close()
		var sessionUser, currentRole string
		if err := database.QueryRowContext(ctx, `
SELECT session_user::text, pg_catalog.current_setting('role')`,
		).Scan(&sessionUser, &currentRole); err != nil {
			t.Fatal(err)
		}
		if sessionUser != login || currentRole != "none" {
			t.Fatalf("%s session posture = %q/%q", role, sessionUser, currentRole)
		}
	}

	store, err := NewPostgresStore(PostgresStoreConfig{
		InputPrecommit: PostgresRoleDatabase{
			Database: roleDatabases[postgresTestRoleInput], SessionAffinityMode: PostgresSessionAffinityDirect,
		},
		SourceVerifier: PostgresRoleDatabase{
			Database: roleDatabases[postgresTestRoleSource], SessionAffinityMode: PostgresSessionAffinityDirect,
		},
		CredentialResolver: PostgresRoleDatabase{
			Database: roleDatabases[postgresTestRoleCredential], SessionAffinityMode: PostgresSessionAffinityDirect,
		},
		MaxTransactionRetries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock, err := NewPostgresClock(roleDatabases[postgresTestRoleInput])
	if err != nil {
		t.Fatal(err)
	}
	issuedAt, err := clock.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	command := IssueCommand{
		OperationID: uuid.New(), AuthorityID: uuid.New(),
		WorkflowInputAuthorityID:       uuid.MustParse(resolved.WorkflowInput.AuthorityID),
		QualificationPolicyAuthorityID: uuid.MustParse(resolved.Policy.AuthorityID),
		QualificationPlanAuthorityID:   uuid.MustParse(resolved.Plan.AuthorityID),
	}
	candidate, sourceAdmission, credentialAdmission := compileInputPostgresCandidate(
		t, command, resolved, issuedAt,
	)

	storedSource, err := store.admitSourceReceipt(ctx, verifiedSourceGrant{
		proof: candidate.Document.SourceProof, requestBytes: candidate.SourceRequestBytes,
	})
	if err != nil || !sameReceiptAdmission(storedSource, sourceAdmission) {
		t.Fatalf("real source admission = %#v, %v", storedSource, err)
	}
	storedCredential, err := store.admitCredentialReceipt(ctx, verifiedCredentialGrant{
		proof: candidate.Document.CredentialProof, requestBytes: candidate.CredentialRequestBytes,
	})
	if err != nil || !sameReceiptAdmission(storedCredential, credentialAdmission) {
		t.Fatalf("real credential admission = %#v, %v", storedCredential, err)
	}
	for _, admission := range []ReceiptAdmissionRecord{storedSource, storedCredential} {
		byRequest, err := store.resolveReceiptAdmissionForRequest(
			ctx, admission.Document.Kind, admission.Document.RequestHash,
		)
		if err != nil || !sameReceiptAdmission(byRequest, admission) {
			t.Fatalf("real request recovery = %#v, %v", byRequest, err)
		}
		byAdmission, err := store.resolveReceiptAdmission(
			ctx, admission.Document.Kind, admission.AdmissionHash,
		)
		if err != nil || !sameReceiptAdmission(byAdmission, admission) {
			t.Fatalf("real admission recovery = %#v, %v", byAdmission, err)
		}
	}

	if _, err := store.Issue(ctx, candidate); !errors.Is(err, ErrStale) {
		t.Fatalf("real absent-upstream Issue() error = %v, want ErrStale", err)
	}
	if _, err := store.InspectOperation(ctx, command.OperationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("real InspectOperation() error = %v", err)
	}
	if _, err := store.ResolveAuthority(ctx, command.AuthorityID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("real ResolveAuthority() error = %v", err)
	}
	if _, err := store.resolveReceiptAdmission(
		ctx, ReceiptKindCredential, storedSource.AdmissionHash,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("real cross-kind recovery error = %v", err)
	}

	// Each LOGIN has only its capability routines, never table data or the
	// sibling verifier's routine.
	if _, err := roleDatabases[postgresTestRoleInput].ExecContext(ctx,
		`SELECT count(*) FROM qualification_input_precommit_authorities`); !postgresPermissionDenied(err) {
		t.Fatalf("input LOGIN direct table read error = %v, want 42501", err)
	}
	if _, err := roleDatabases[postgresTestRoleSource].ExecContext(ctx,
		`SELECT * FROM inspect_qualification_input_credential_receipt_v1($1)`,
		storedCredential.Document.RequestHash); !postgresPermissionDenied(err) {
		t.Fatalf("source LOGIN sibling capability error = %v, want 42501", err)
	}
	if _, err := roleDatabases[postgresTestRoleInput].ExecContext(ctx,
		`SELECT * FROM inspect_qualification_input_source_receipt_v1($1)`,
		storedSource.Document.RequestHash); !postgresPermissionDenied(err) {
		t.Fatalf("input LOGIN verifier capability error = %v, want 42501", err)
	}
	if _, err := roleDatabases[postgresTestRoleCredential].ExecContext(ctx,
		`SELECT * FROM inspect_qualification_input_precommit_operation_v1($1)`,
		command.OperationID); !postgresPermissionDenied(err) {
		t.Fatalf("credential LOGIN composition capability error = %v, want 42501", err)
	}
	if _, err := roleDatabases[postgresTestRoleCredential].ExecContext(ctx,
		`SELECT count(*) FROM qualification_input_credential_receipt_admissions`); !postgresPermissionDenied(err) {
		t.Fatalf("credential LOGIN direct table read error = %v, want 42501", err)
	}
}

func compileInputPostgresCandidate(
	t *testing.T,
	command IssueCommand,
	resolved ResolvedAuthorities,
	issuedAt time.Time,
) (Record, ReceiptAdmissionRecord, ReceiptAdmissionRecord) {
	t.Helper()
	request := issueRequestFromCommand(command)
	requestBytes, requestHash, err := EncodeIssueRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	sourceRequest := sourceRequestFromAuthoritySet(resolved)
	sourceBytes, sourceHash, err := EncodeSourceRequest(sourceRequest)
	if err != nil {
		t.Fatal(err)
	}
	credentialRequest := credentialRequestFromAuthoritySet(resolved)
	credentialBytes, credentialHash, err := EncodeCredentialRequest(credentialRequest)
	if err != nil {
		t.Fatal(err)
	}
	sourceProof := VerificationProof{
		AuthorityID:      resolved.SourceVerifier.AuthorityID,
		ExecutableDigest: resolved.SourceVerifier.ExecutableDigest,
		ReceiptHash:      testDigest("postgres-real-source-receipt"), RequestHash: sourceHash,
	}
	sourceAdmission, err := compileReceiptAdmission(ReceiptKindSource, sourceProof, sourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	sourceProof.AdmissionHash = sourceAdmission.AdmissionHash
	credentialProof := VerificationProof{
		AuthorityID:      resolved.CredentialResolver.AuthorityID,
		ExecutableDigest: resolved.CredentialResolver.ExecutableDigest,
		ReceiptHash:      testDigest("postgres-real-credential-receipt"), RequestHash: credentialHash,
	}
	credentialAdmission, err := compileReceiptAdmission(ReceiptKindCredential, credentialProof, credentialBytes)
	if err != nil {
		t.Fatal(err)
	}
	credentialProof.AdmissionHash = credentialAdmission.AdmissionHash
	document := AuthorityDocument{
		AuthorityID: command.AuthorityID.String(), CredentialProof: credentialProof,
		CredentialRequestHash: credentialHash, IssuedAt: issuedAt.Format(canonicalTimeLayout),
		OperationID: command.OperationID.String(), Plan: resolved.Plan, Policy: resolved.Policy,
		RequestHash: requestHash, SchemaVersion: AuthoritySchemaV1, SourceProof: sourceProof,
		SourceRequestHash: sourceHash, WorkflowInput: resolved.WorkflowInput,
	}
	documentBytes, authorityHash, err := EncodeAuthority(document)
	if err != nil {
		t.Fatal(err)
	}
	record := Record{
		Command: command,
		Request: request, RequestBytes: requestBytes, RequestHash: requestHash,
		SourceRequest: sourceRequest, SourceRequestBytes: sourceBytes, SourceRequestHash: sourceHash,
		CredentialRequest: credentialRequest, CredentialRequestBytes: credentialBytes,
		CredentialRequestHash: credentialHash,
		Document:              document, DocumentBytes: documentBytes, AuthorityHash: authorityHash, IssuedAt: issuedAt,
	}
	if err := ValidateRecord(record); err != nil {
		t.Fatal(err)
	}
	return record, sourceAdmission, credentialAdmission
}

func applyInputPostgresMigrationPrefix(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	if _, err := database.ExecContext(ctx, `CREATE TABLE schema_migrations (
  version text PRIMARY KEY,
  checksum text NOT NULL,
  down_checksum text,
  applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		base := filepath.Base(name)
		if base > "000080_qualification_input_precommit_authority.up.sql" {
			break
		}
		contents, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, string(contents)); err != nil {
			var postgresError *pgconn.PgError
			if errors.As(err, &postgresError) {
				t.Fatalf("apply %s SQLSTATE=%s position=%d: %v", base, postgresError.Code, postgresError.Position, err)
			}
			t.Fatalf("apply %s: %v", base, err)
		}
	}
}

func inputPostgresDSN(t *testing.T, dsn, user, password, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		if user != "" {
			parsed.User = url.UserPassword(user, password)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	if user != "" {
		t.Fatalf("role canary requires a URL PostgreSQL DSN, got %q", dsn)
	}
	return fmt.Sprintf("%s search_path=%s", dsn, schema)
}

func postgresTestIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func postgresTestLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func postgresPermissionDenied(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "42501"
}
