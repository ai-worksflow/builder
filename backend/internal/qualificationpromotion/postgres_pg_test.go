package qualificationpromotion

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
)

func TestPostgresStoreAtomicConsumeRaceACLAndRecovery(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	if err := base.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	roleLock, err := base.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer roleLock.Close()
	if _, err := roleLock.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext('worksflow-model-governance-postgres-role-test'))`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = roleLock.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext('worksflow-model-governance-postgres-role-test'))`)
	}()
	createdRoles := createQualificationPromotionTestRoles(t, ctx, roleLock)
	defer func() {
		for index := len(createdRoles) - 1; index >= 0; index-- {
			_, _ = roleLock.ExecContext(context.Background(), `DROP ROLE IF EXISTS `+createdRoles[index])
		}
	}()
	if _, err := base.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		t.Fatal(err)
	}
	schema := "qualification_promotion_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	if _, err := base.ExecContext(ctx, `ALTER SCHEMA "`+schema+`" OWNER TO worksflow_migration_owner`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`) }()
	database, err := sql.Open("pgx", qualificationPromotionSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	up, err := os.ReadFile("../../migrations/000071_qualification_promotion_consume.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) {
			t.Fatalf("apply 000071: sqlstate=%s position=%d message=%s", postgresError.Code, postgresError.Position, postgresError.Message)
		}
		t.Fatalf("apply 000071: %v", err)
	}
	store, err := NewPostgresStore(database)
	if err != nil {
		t.Fatal(err)
	}
	now, err := store.trustedTime(ctx)
	if err != nil || now.IsZero() {
		t.Fatalf("trustedTime() = %s, %v", now, err)
	}
	verified := validVerifiedPromotion(now)
	service, verifier, authority := newPostgresTestService(t, store, verified)
	command := validConsumeCommand()
	first, err := service.Consume(ctx, command)
	if err != nil || first.Idempotent || first.ConsumedAt.Before(now) || first.Handoff.CreatedAt != first.ConsumedAt {
		t.Fatalf("PostgreSQL first Consume() = record:%+v error:%v", first, err)
	}
	assertPostgresCounts(t, ctx, database, 1, 1)
	replayed, err := service.Consume(ctx, command)
	if err != nil || !replayed.Idempotent || !sameImmutableRecord(replayed, first) {
		t.Fatalf("PostgreSQL exact replay = record:%+v error:%v", replayed, err)
	}

	// A handoff uniqueness failure must roll back the ledger insert in the same
	// transaction; no orphan consumption can remain.
	atomicVerified := validVerifiedPromotion(now)
	verifier.verified = atomicVerified
	authority.resolution.Target = atomicVerified.PromotionTarget
	authority.resolution.Verification.Expected.PromotionTarget = atomicVerified.PromotionTarget
	atomicFailure := validConsumeCommand()
	atomicFailure.OutputRevisionID = command.OutputRevisionID
	if _, err := service.Consume(ctx, atomicFailure); !errors.Is(err, ErrConflict) {
		t.Fatalf("handoff collision error = %v", err)
	}
	assertPostgresCounts(t, ctx, database, 1, 1)

	// A lost commit acknowledgement is inspected by the exact operation ID and
	// reconstructs the immutable ledger/handoff response.
	unknownVerified := validVerifiedPromotion(now)
	verifier.verified = unknownVerified
	authority.resolution.Target = unknownVerified.PromotionTarget
	authority.resolution.Verification.Expected.PromotionTarget = unknownVerified.PromotionTarget
	unknownCommand := validConsumeCommand()
	store.commit = func(transaction *sql.Tx) error {
		if err := transaction.Commit(); err != nil {
			return err
		}
		return errors.New("simulated lost PostgreSQL commit acknowledgement")
	}
	unknownRecord, err := service.Consume(ctx, unknownCommand)
	store.commit = func(transaction *sql.Tx) error { return transaction.Commit() }
	if err != nil || !unknownRecord.Idempotent || unknownRecord.OperationID != unknownCommand.OperationID {
		t.Fatalf("PostgreSQL unknown commit reconstruction = record:%+v error:%v", unknownRecord, err)
	}
	assertPostgresCounts(t, ctx, database, 2, 2)

	// Concurrent first writers using one nonce but different operations cannot
	// both consume it. The global lock makes the post-wait DB clock authoritative.
	raceVerified := validVerifiedPromotion(now)
	leftVerifier := &fakeVerifier{verified: raceVerified}
	rightVerifier := &fakeVerifier{verified: raceVerified}
	leftAuthority := &fakeExpectationAuthority{resolution: authorityResolutionFor(raceVerified)}
	rightAuthority := &fakeExpectationAuthority{resolution: authorityResolutionFor(raceVerified)}
	leftService, _ := NewService(leftVerifier, leftAuthority, store)
	rightService, _ := NewService(rightVerifier, rightAuthority, store)
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, candidate := range []struct {
		service *Service
		command ConsumeCommand
	}{{leftService, validConsumeCommand()}, {rightService, validConsumeCommand()}} {
		wait.Add(1)
		go func(value struct {
			service *Service
			command ConsumeCommand
		}) {
			defer wait.Done()
			_, consumeErr := value.service.Consume(ctx, value.command)
			results <- consumeErr
		}(candidate)
	}
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for result := range results {
		switch {
		case result == nil:
			successes++
		case errors.Is(result, ErrConflict):
			conflicts++
		default:
			t.Fatalf("PostgreSQL nonce race error = %v", result)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("PostgreSQL nonce race successes=%d conflicts=%d", successes, conflicts)
	}
	assertPostgresCounts(t, ctx, database, 3, 3)

	// Replay does not re-evaluate expiry, while a new operation for an expired
	// verified capability is rejected by the database's own post-lock clock.
	expiringNow, err := store.trustedTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expiring := validVerifiedPromotion(expiringNow)
	expiring.AuthorityExpiresAt = expiringNow.Add(900 * time.Millisecond).Format(canonicalTimeLayout)
	verifier.verified = expiring
	verifier.err = nil
	authority.err = nil
	authority.resolution = authorityResolutionFor(expiring)
	expiringCommand := validConsumeCommand()
	expiringRecord, err := service.Consume(ctx, expiringCommand)
	if err != nil {
		t.Fatalf("consume before short expiry: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	verifier.err = errors.New("evidence retired after commit")
	authority.err = errors.New("authority retired after commit")
	postExpiryReplay, err := service.Consume(ctx, expiringCommand)
	if err != nil || !postExpiryReplay.Idempotent || !sameImmutableRecord(postExpiryReplay, expiringRecord) {
		t.Fatalf("PostgreSQL post-expiry replay = record:%+v error:%v", postExpiryReplay, err)
	}
	verifier.err = nil
	authority.err = nil
	expiredDistinct := expiring
	expiredDistinct.AuthorityNonce = uuid.NewString()
	verifier.verified = expiredDistinct
	authority.resolution = authorityResolutionFor(expiredDistinct)
	expiredNew := validConsumeCommand()
	if _, err := service.Consume(ctx, expiredNew); !errors.Is(err, ErrAuthorityExpired) {
		t.Fatalf("PostgreSQL expired new operation error = %v", err)
	}

	assertPostgresQualificationPromotionACL(t, ctx, database, schema)
	down, err := os.ReadFile("../../migrations/000071_qualification_promotion_consume.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, `LOCK TABLE qualification_promotion_consumptions IN ACCESS SHARE MODE`); err != nil {
		_ = blocker.Rollback()
		t.Fatal(err)
	}
	downResult := make(chan error, 1)
	go func() {
		_, rollbackErr := database.ExecContext(ctx, string(down))
		downResult <- rollbackErr
	}()
	select {
	case early := <-downResult:
		_ = blocker.Rollback()
		t.Fatalf("rollback bypassed ACCESS EXCLUSIVE promotion fence: %v", early)
	case <-time.After(100 * time.Millisecond):
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-downResult; err == nil ||
		!strings.Contains(err.Error(), "cannot roll back qualification promotion consumption") {
		t.Fatalf("nonempty qualification promotion rollback error = %v", err)
	}
}

func newPostgresTestService(t *testing.T, store *PostgresStore, verified qualificationreceipt.VerifiedPromotion) (*Service, *fakeVerifier, *fakeExpectationAuthority) {
	t.Helper()
	verifier := &fakeVerifier{verified: verified}
	authority := &fakeExpectationAuthority{resolution: authorityResolutionFor(verified)}
	service, err := NewService(verifier, authority, store)
	if err != nil {
		t.Fatal(err)
	}
	return service, verifier, authority
}

func authorityResolutionFor(verified qualificationreceipt.VerifiedPromotion) AuthorityResolution {
	return AuthorityResolution{
		Target: verified.PromotionTarget,
		Verification: VerificationInput{
			ReceiptPath: "/sealed/receipt.dsse.json", IndexPath: "/sealed/index.json", ArtifactRoot: "/sealed/artifacts",
			Expected: qualificationreceipt.ExpectedPromotion{PromotionTarget: verified.PromotionTarget},
		},
	}
}

func createQualificationPromotionTestRoles(t *testing.T, ctx context.Context, connection *sql.Conn) []string {
	t.Helper()
	var created []string
	for _, role := range []string{"worksflow_application", "worksflow_migration_owner", "worksflow_qualification_promotion_operator"} {
		var exists bool
		if err := connection.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			continue
		}
		if _, err := connection.ExecContext(ctx, `CREATE ROLE `+role+` NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION`); err != nil {
			t.Fatalf("create test role %s: %v", role, err)
		}
		created = append(created, role)
	}
	return created
}

func assertPostgresCounts(t *testing.T, ctx context.Context, database *sql.DB, consumptions, handoffs int) {
	t.Helper()
	var consumptionCount, handoffCount int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM qualification_promotion_consumptions`).Scan(&consumptionCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM qualification_promotion_handoffs`).Scan(&handoffCount); err != nil {
		t.Fatal(err)
	}
	if consumptionCount != consumptions || handoffCount != handoffs {
		t.Fatalf("PostgreSQL atomic counts = consumptions:%d handoffs:%d, want %d/%d", consumptionCount, handoffCount, consumptions, handoffs)
	}
}

func assertPostgresQualificationPromotionACL(t *testing.T, ctx context.Context, database *sql.DB, schema string) {
	t.Helper()
	var securityDefiner, publicExecute, applicationExecute, operatorExecute bool
	var owner string
	var config string
	if err := database.QueryRowContext(ctx, `
SELECT procedure.prosecdef,
       pg_get_userbyid(procedure.proowner),
	   coalesce(array_to_string(procedure.proconfig, E'\\n'), ''),
       has_function_privilege('public', procedure.oid, 'EXECUTE'),
       has_function_privilege('worksflow_application', procedure.oid, 'EXECUTE'),
       has_function_privilege('worksflow_qualification_promotion_operator', procedure.oid, 'EXECUTE')
FROM pg_proc AS procedure
JOIN pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
WHERE namespace.nspname = $1 AND procedure.proname = 'consume_verified_qualification_promotion'`, schema).Scan(
		&securityDefiner, &owner, &config, &publicExecute, &applicationExecute, &operatorExecute,
	); err != nil {
		t.Fatal(err)
	}
	wantPath := "search_path=pg_catalog, " + schema + ", pg_temp"
	if !securityDefiner || owner != "worksflow_migration_owner" || publicExecute || applicationExecute || !operatorExecute ||
		config != wantPath {
		t.Fatalf("function posture definer=%t owner=%s config=%v public=%t app=%t operator=%t",
			securityDefiner, owner, config, publicExecute, applicationExecute, operatorExecute)
	}
	for _, table := range []string{"qualification_promotion_consumptions", "qualification_promotion_handoffs"} {
		var publicInsert, applicationInsert, operatorInsert, operatorSelect bool
		if err := database.QueryRowContext(ctx, `
SELECT has_table_privilege('public', $1, 'INSERT'),
       has_table_privilege('worksflow_application', $1, 'INSERT'),
       has_table_privilege('worksflow_qualification_promotion_operator', $1, 'INSERT'),
       has_table_privilege('worksflow_qualification_promotion_operator', $1, 'SELECT')`, schema+"."+table).Scan(
			&publicInsert, &applicationInsert, &operatorInsert, &operatorSelect,
		); err != nil {
			t.Fatal(err)
		}
		if publicInsert || applicationInsert || operatorInsert || !operatorSelect {
			t.Fatalf("table %s ACL public-insert=%t app-insert=%t operator-insert=%t operator-select=%t",
				table, publicInsert, applicationInsert, operatorInsert, operatorSelect)
		}
	}
}

func qualificationPromotionSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}
