package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestQualificationPlanAuthorityMigrationIsCanonicalImmutableAndOwnerOnly(t *testing.T) {
	up, err := files.ReadFile("000074_qualification_plan_authority.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000074_qualification_plan_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upText := string(up)
	for _, expected := range []string{
		"CREATE TABLE qualification_plan_authorities",
		"CREATE TABLE qualification_plan_identity_reservations",
		"CREATE FUNCTION freeze_qualification_plan_authority(",
		"CREATE FUNCTION resolve_qualification_plan_authority(p_authority_id uuid)",
		"worksflow-qualification-plan-freeze-request/v1",
		"worksflow-qualification-plan-input/v1",
		"worksflow-qualification-plan/v1",
		"worksflow-qualification-evidence-plan/v1",
		"worksflow-qualification-plan-trust/v1",
		"worksflow-qualification-plan-target/v1",
		"worksflow-qualification-plan-authority/v1",
		"pg_catalog.sha256($1)",
		"qualification_plan_sha256(p_request_bytes) <> p_request_hash",
		"qualification_plan_sha256(p_input_bytes) <> p_input_hash",
		"qualification_plan_sha256(p_projection_bytes) <> p_projection_hash",
		"qualification_plan_sha256(p_evidence_plan_bytes) <> p_evidence_plan_hash",
		"qualification_plan_sha256(p_trust_bytes) <> p_trust_hash",
		"qualification_plan_sha256(p_target_bytes) <> p_target_hash",
		"qualification_plan_sha256(p_envelope_bytes) <> p_envelope_hash",
		"jsonb_array_length(p_projection_document->'sourceDocuments') = 0",
		"jsonb_array_length(p_projection_document->'suites') = 0",
		"jsonb_array_length(p_projection_document->'supportFiles') = 0",
		"evidence_artifact->'classification' IS DISTINCT FROM input_artifact->'classification'",
		"evidence_artifact->'id' IS DISTINCT FROM input_artifact->'id'",
		"evidence_artifact->'kind' IS DISTINCT FROM input_artifact->'kind'",
		"artifact->>'classification' = 'restricted'",
		"artifact->>'encryptionOperationId' !~",
		"UNION ALL SELECT v_input_authority_id::text, 'input-authority', 0",
		"SELECT 1 FROM qualification_evidence_events",
		"event_id::text = ANY(v_reserved_identity_values)",
		"WHERE identity_value = NEW.event_id::text",
		"Qualification Evidence EventID collides with an immutable Plan identity",
		"IF NEW.event_kind <> 'reserved' THEN",
		"v_authority.evidence_plan_document <> NEW.event_document->'plan'",
		"BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_plan_authorities",
		"BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_plan_identity_reservations",
		"BEFORE INSERT ON qualification_evidence_events",
		"SECURITY INVOKER",
		"ALTER TABLE %I.qualification_plan_authorities OWNER TO worksflow_migration_owner",
		"ALTER TABLE %I.qualification_plan_identity_reservations OWNER TO worksflow_migration_owner",
		"GRANT USAGE ON SCHEMA %I TO worksflow_migration_owner",
		"REVOKE ALL ON FUNCTION %I.freeze_qualification_plan_authority(",
		"REVOKE ALL ON FUNCTION %I.resolve_qualification_plan_authority(uuid)",
	} {
		if !strings.Contains(upText, expected) {
			t.Fatalf("Qualification Plan authority migration is missing %q", expected)
		}
	}
	if strings.Count(upText, "CREATE TABLE ") != 2 {
		t.Fatalf("Qualification Plan authority table count = %d, want 2", strings.Count(upText, "CREATE TABLE "))
	}
	if strings.Count(upText, "CREATE FUNCTION") != 5 {
		t.Fatalf("Qualification Plan authority function count = %d, want 5", strings.Count(upText, "CREATE FUNCTION"))
	}
	if strings.Count(upText, "CREATE TRIGGER") != 3 {
		t.Fatalf("Qualification Plan authority trigger count = %d, want 3", strings.Count(upText, "CREATE TRIGGER"))
	}
	if strings.Count(upText, "\nSECURITY DEFINER\n") != 1 {
		t.Fatalf("Qualification Plan authority SECURITY DEFINER count = %d, want 1", strings.Count(upText, "\nSECURITY DEFINER\n"))
	}
	for _, forbidden := range []string{
		"p_evidence_plan_document->'artifacts' <> p_input_document->'artifacts'",
		"UNION ALL SELECT v_fixture_id::text, 'fixture'",
		"GRANT EXECUTE", "GRANT SELECT", "GRANT INSERT", "GRANT UPDATE", "GRANT DELETE",
		"raw_token", "raw-token", "cookie bytea", "storage_state", "storage-state bytea",
		"header bytea", "environment json", "file_path", "capability bytea", "metadata json",
		"ON DELETE CASCADE",
		".digest($1",
	} {
		if strings.Contains(strings.ToLower(upText), strings.ToLower(forbidden)) {
			t.Fatalf("Qualification Plan authority migration contains forbidden SQL %q", forbidden)
		}
	}
	if !strings.Contains(upText,
		"input_authority_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'",
	) {
		t.Fatal("Qualification Plan authority table does not accept the exact canonical UUIDv4 form for InputAuthorityID")
	}
	reverseCollision := strings.Index(upText, "WHERE identity_value = NEW.event_id::text")
	legacyRecovery := strings.Index(upText, "IF NEW.event_kind <> 'reserved' THEN")
	if reverseCollision < 0 || legacyRecovery < 0 || reverseCollision >= legacyRecovery {
		t.Fatal("Qualification Evidence reverse EventID collision guard must precede legacy non-reservation recovery")
	}
	assertQualificationPlanLockOrder(t, upText, []string{
		"LOCK TABLE qualification_evidence_events IN SHARE ROW EXCLUSIVE MODE",
		"LOCK TABLE qualification_evidence_operations IN SHARE ROW EXCLUSIVE MODE",
		"LOCK TABLE qualification_evidence_heads IN SHARE ROW EXCLUSIVE MODE",
		"LOCK TABLE qualification_plan_authorities IN SHARE ROW EXCLUSIVE MODE",
		"LOCK TABLE qualification_plan_identity_reservations IN SHARE ROW EXCLUSIVE MODE",
	})
	downText := string(down)
	for _, expected := range []string{
		"LOCK TABLE qualification_evidence_events IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_evidence_operations IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_evidence_heads IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_plan_authorities IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_plan_identity_reservations IN ACCESS EXCLUSIVE MODE",
		"IF EXISTS (SELECT 1 FROM qualification_plan_authorities)",
		"OR EXISTS (SELECT 1 FROM qualification_plan_identity_reservations)",
		"cannot roll back Qualification Plan authority while immutable authority state is nonempty",
		"DROP TRIGGER IF EXISTS qualification_evidence_plan_authority_guard",
		"DROP FUNCTION IF EXISTS freeze_qualification_plan_authority(",
		"DROP TABLE IF EXISTS qualification_plan_identity_reservations",
		"DROP TABLE IF EXISTS qualification_plan_authorities",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("Qualification Plan authority rollback is missing %q", expected)
		}
	}
	assertQualificationPlanLockOrder(t, downText, []string{
		"LOCK TABLE qualification_evidence_events IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_evidence_operations IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_evidence_heads IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_plan_authorities IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_plan_identity_reservations IN ACCESS EXCLUSIVE MODE",
	})
}

func assertQualificationPlanLockOrder(t *testing.T, text string, locks []string) {
	t.Helper()
	previous := -1
	for _, lock := range locks {
		position := strings.Index(text, lock)
		if position < 0 || position <= previous {
			t.Fatalf("Qualification Plan lock %q is absent or out of order", lock)
		}
		previous = position
	}
}

func TestQualificationPlanAuthorityMigrationPostgresCanary(t *testing.T) {
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
	t.Cleanup(func() { _ = base.Close() })
	if _, err := base.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		t.Fatal(err)
	}

	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_plan_authority_")
	applyQualificationPlanMigrations(t, ctx, database)

	var tableCount, functionCount, definerCount, triggerCount, indexCount int
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM pg_catalog.pg_class
   WHERE relnamespace = pg_catalog.current_schema()::regnamespace AND relkind = 'r'
     AND relname IN ('qualification_plan_authorities','qualification_plan_identity_reservations')),
  (SELECT count(*) FROM pg_catalog.pg_proc
   WHERE pronamespace = pg_catalog.current_schema()::regnamespace
     AND proname IN ('qualification_plan_sha256','reject_qualification_plan_immutable_mutation',
       'freeze_qualification_plan_authority','resolve_qualification_plan_authority',
       'guard_qualification_evidence_plan_authority')),
  (SELECT count(*) FROM pg_catalog.pg_proc
   WHERE pronamespace = pg_catalog.current_schema()::regnamespace AND prosecdef
     AND proname IN ('qualification_plan_sha256','reject_qualification_plan_immutable_mutation',
       'freeze_qualification_plan_authority','resolve_qualification_plan_authority',
       'guard_qualification_evidence_plan_authority')),
  (SELECT count(*) FROM pg_catalog.pg_trigger AS trigger
   JOIN pg_catalog.pg_class AS relation ON relation.oid = trigger.tgrelid
   WHERE relation.relnamespace = pg_catalog.current_schema()::regnamespace
     AND NOT trigger.tgisinternal AND trigger.tgenabled = 'O'
     AND (
       (trigger.tgname IN ('qualification_plan_authorities_immutable',
          'qualification_plan_identity_reservations_immutable') AND trigger.tgtype = 58)
       OR (trigger.tgname = 'qualification_evidence_plan_authority_guard' AND trigger.tgtype = 7)
     )),
  (SELECT count(*) FROM pg_catalog.pg_index AS index
   JOIN pg_catalog.pg_class AS relation ON relation.oid = index.indrelid
   WHERE relation.relnamespace = pg_catalog.current_schema()::regnamespace
     AND relation.relname IN ('qualification_plan_authorities','qualification_plan_identity_reservations'))
`).Scan(&tableCount, &functionCount, &definerCount, &triggerCount, &indexCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 2 || functionCount != 5 || definerCount != 1 || triggerCount != 3 || indexCount != 8 {
		t.Fatalf("Qualification Plan PG objects tables=%d functions=%d definers=%d triggers=%d indexes=%d, want 2/5/1/3/8",
			tableCount, functionCount, definerCount, triggerCount, indexCount)
	}
	var freezeDefiner, freezeSearchPath, freezeOwnerSchemaUsage bool
	if err := database.QueryRowContext(ctx, `
SELECT
  routine.prosecdef,
  routine.proconfig = ARRAY['search_path=pg_catalog, ' || pg_catalog.current_schema() || ', pg_temp']::text[],
  pg_catalog.has_schema_privilege(
    pg_catalog.pg_get_userbyid(routine.proowner), pg_catalog.current_schema(), 'USAGE'
  )
FROM pg_catalog.pg_proc AS routine
WHERE routine.pronamespace = pg_catalog.current_schema()::regnamespace
  AND routine.proname = 'freeze_qualification_plan_authority'
  AND routine.pronargs = 23
`).Scan(&freezeDefiner, &freezeSearchPath, &freezeOwnerSchemaUsage); err != nil {
		t.Fatal(err)
	}
	if !freezeDefiner || !freezeSearchPath || !freezeOwnerSchemaUsage {
		t.Fatalf("freeze posture definer=%t exactSearchPath=%t ownerSchemaUsage=%t, want true/true/true",
			freezeDefiner, freezeSearchPath, freezeOwnerSchemaUsage)
	}

	sharedFixtureID := uuid.New()
	first := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{fixtureID: sharedFixtureID})
	if err := freezeQualificationPlanMigrationFixture(ctx, database, first); err != nil {
		t.Fatalf("freeze canonical authority: %v", err)
	}
	if err := freezeQualificationPlanMigrationFixture(ctx, database, first); err != nil {
		t.Fatalf("exact freeze replay: %v", err)
	}
	second := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{fixtureID: sharedFixtureID})
	if err := freezeQualificationPlanMigrationFixture(ctx, database, second); err != nil {
		t.Fatalf("reuse upstream Golden FixtureID in another authority: %v", err)
	}
	if err := insertQualificationPlanMigrationEvidenceEvent(
		ctx, database, "reserved", first, true, first.authorityID,
	); err == nil || !strings.Contains(err.Error(), "EventID collides with an immutable Plan identity") {
		t.Fatalf("reserved Plan-identity EventID collision error = %v, want rejection", err)
	}
	if err := insertQualificationPlanMigrationEvidenceEvent(
		ctx, database, "credential-issue-started", first, false, first.inputAuthorityID,
	); err == nil || !strings.Contains(err.Error(), "EventID collides with an immutable Plan identity") {
		t.Fatalf("non-reserved Plan-identity EventID collision error = %v, want rejection", err)
	}
	legacyEventID := uuid.New()
	legacyEventFixture := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{})
	if err := insertQualificationPlanMigrationEvidenceEvent(
		ctx, database, "credential-issue-started", legacyEventFixture, false, legacyEventID,
	); err != nil {
		t.Fatalf("seed legacy Evidence EventID: %v", err)
	}
	legacyCollision := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{
		inputAuthorityID: legacyEventID,
	})
	if err := freezeQualificationPlanMigrationFixture(ctx, database, legacyCollision); err == nil ||
		!strings.Contains(err.Error(), "legacy Evidence") {
		t.Fatalf("legacy Evidence EventID to Plan identity collision error = %v, want rejection", err)
	}

	reusedInput := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{
		inputAuthorityID: first.inputAuthorityID,
	})
	if err := freezeQualificationPlanMigrationFixture(ctx, database, reusedInput); err == nil ||
		!strings.Contains(err.Error(), "globally reserved") {
		t.Fatalf("reused InputAuthorityID error = %v, want global reservation rejection", err)
	}
	mismatchedArtifact := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{artifactMismatch: true})
	if err := freezeQualificationPlanMigrationFixture(ctx, database, mismatchedArtifact); err == nil ||
		!strings.Contains(err.Error(), "evidence operations or artifacts") {
		t.Fatalf("input/evidence artifact mismatch error = %v, want artifact closure rejection", err)
	}
	invalidEncryption := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{invalidEncryption: true})
	if err := freezeQualificationPlanMigrationFixture(ctx, database, invalidEncryption); err == nil ||
		!strings.Contains(err.Error(), "evidence operations or artifacts") {
		t.Fatalf("invalid restricted encryption operation error = %v, want artifact rejection", err)
	}
	emptyProjection := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{emptyProjection: true})
	if err := freezeQualificationPlanMigrationFixture(ctx, database, emptyProjection); err == nil ||
		!strings.Contains(err.Error(), "projection bytes") {
		t.Fatalf("empty projection roots error = %v, want projection rejection", err)
	}

	var authorityCount, identityCount, inputIdentityCount, fixtureIdentityCount int
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM qualification_plan_authorities),
  (SELECT count(*) FROM qualification_plan_identity_reservations),
  (SELECT count(*) FROM qualification_plan_identity_reservations WHERE identity_kind = 'input-authority'),
  (SELECT count(*) FROM qualification_plan_identity_reservations WHERE identity_value = $1)
`, sharedFixtureID.String()).Scan(&authorityCount, &identityCount, &inputIdentityCount, &fixtureIdentityCount); err != nil {
		t.Fatal(err)
	}
	if authorityCount != 2 || identityCount != 34 || inputIdentityCount != 2 || fixtureIdentityCount != 0 {
		t.Fatalf("authority identity closure authorities=%d identities=%d inputs=%d fixtures=%d, want 2/34/2/0",
			authorityCount, identityCount, inputIdentityCount, fixtureIdentityCount)
	}
	var resolved int
	if err := database.QueryRowContext(ctx,
		`SELECT count(*) FROM resolve_qualification_plan_authority($1)`, first.authorityID,
	).Scan(&resolved); err != nil || resolved != 1 {
		t.Fatalf("resolve frozen authority count=%d error=%v", resolved, err)
	}
	if err := database.QueryRowContext(ctx,
		`SELECT count(*) FROM resolve_qualification_plan_authority($1)`, uuid.New(),
	).Scan(&resolved); err != nil || resolved != 0 {
		t.Fatalf("resolve unknown authority count=%d error=%v", resolved, err)
	}
	if _, err := database.ExecContext(ctx,
		`UPDATE qualification_plan_authorities SET subject = subject WHERE authority_id = $1`, first.authorityID,
	); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("authority UPDATE error = %v, want immutable rejection", err)
	}

	if err := insertQualificationPlanMigrationEvidenceEvent(ctx, database, "reserved", first, true); err != nil {
		t.Fatalf("insert reservation bound to exact authority: %v", err)
	}
	unbound := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{})
	if err := insertQualificationPlanMigrationEvidenceEvent(ctx, database, "reserved", unbound, true); err == nil ||
		!strings.Contains(err.Error(), "not bound to one exact immutable Plan authority") {
		t.Fatalf("unbound reservation error = %v, want Plan authority guard rejection", err)
	}
	if err := insertQualificationPlanMigrationEvidenceEvent(ctx, database, "credential-issue-started", unbound, false); err != nil {
		t.Fatalf("legacy non-reservation recovery insert was rechecked: %v", err)
	}

	down, err := files.ReadFile("000074_qualification_plan_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err == nil ||
		!strings.Contains(err.Error(), "immutable authority state is nonempty") {
		t.Fatalf("nonempty Qualification Plan rollback error = %v, want refusal", err)
	}

	legacyDatabase := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_plan_legacy_")
	applyQualificationPlanMigrations(t, ctx, legacyDatabase)
	legacyFixture := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{})
	if err := insertQualificationPlanMigrationEvidenceEvent(ctx, legacyDatabase, "credential-issue-started", legacyFixture, false); err != nil {
		t.Fatalf("seed legacy Evidence recovery row: %v", err)
	}
	if _, err := legacyDatabase.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("rollback empty Plan authority over legacy Evidence state: %v", err)
	}
	var evidenceRows int
	var planTableRemoved bool
	if err := legacyDatabase.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM qualification_evidence_events),
  pg_catalog.to_regclass(pg_catalog.current_schema() || '.qualification_plan_authorities') IS NULL
`).Scan(&evidenceRows, &planTableRemoved); err != nil {
		t.Fatal(err)
	}
	if evidenceRows != 1 || !planTableRemoved {
		t.Fatalf("legacy rollback evidenceRows=%d planTableRemoved=%t, want 1/true", evidenceRows, planTableRemoved)
	}
}

func TestQualificationPlanAuthorityRollbackWriterFencingPostgres(t *testing.T) {
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
	t.Cleanup(func() { _ = base.Close() })
	if _, err := base.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000074_qualification_plan_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("writer lock order commits before waiting rollback observes state", func(t *testing.T) {
		database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_plan_freeze_first_")
		applyQualificationPlanMigrations(t, ctx, database)
		fixture := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{})

		planGate, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer planGate.Rollback()
		if _, err := planGate.ExecContext(ctx,
			`LOCK TABLE qualification_plan_authorities IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}

		writerConnection, err := database.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer writerConnection.Close()
		var writerPID int
		if err := writerConnection.QueryRowContext(ctx, `SELECT pg_catalog.pg_backend_pid()`).Scan(&writerPID); err != nil {
			t.Fatal(err)
		}
		writerTransaction, err := writerConnection.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer writerTransaction.Rollback()
		writerCtx, writerCancel := context.WithCancel(ctx)
		defer writerCancel()
		writerLocksFinished := make(chan error, 1)
		go func() {
			_, lockErr := writerTransaction.ExecContext(writerCtx, `
LOCK TABLE qualification_evidence_events IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_evidence_operations IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_evidence_heads IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_plan_authorities IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_plan_identity_reservations IN SHARE ROW EXCLUSIVE MODE;
`)
			writerLocksFinished <- lockErr
		}()
		lockCtx, lockCancel := context.WithTimeout(ctx, 5*time.Second)
		defer lockCancel()
		for _, relation := range []string{
			"qualification_evidence_events",
			"qualification_evidence_operations",
			"qualification_evidence_heads",
		} {
			if err := waitForQualificationPlanMigrationLock(
				lockCtx, database, writerPID, relation, "ShareRowExclusiveLock", true, writerLocksFinished,
			); err != nil {
				t.Fatal(err)
			}
		}
		if err := waitForQualificationPlanMigrationLock(
			lockCtx, database, writerPID, "qualification_plan_authorities", "ShareRowExclusiveLock", false, writerLocksFinished,
		); err != nil {
			t.Fatal(err)
		}

		downConnection, err := database.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer downConnection.Close()
		downCtx, downCancel := context.WithCancel(ctx)
		defer downCancel()
		var downPID int
		if err := downConnection.QueryRowContext(ctx, `SELECT pg_catalog.pg_backend_pid()`).Scan(&downPID); err != nil {
			t.Fatal(err)
		}
		downFinished := make(chan error, 1)
		go func() {
			_, executeErr := downConnection.ExecContext(downCtx, string(down))
			downFinished <- executeErr
		}()
		if err := waitForQualificationPlanMigrationLock(
			lockCtx, database, downPID, "qualification_evidence_events", "AccessExclusiveLock", false, downFinished,
		); err != nil {
			t.Fatal(err)
		}
		if err := planGate.Rollback(); err != nil {
			t.Fatal(err)
		}
		select {
		case lockErr := <-writerLocksFinished:
			if lockErr != nil {
				t.Fatalf("writer migration-order lock batch: %v", lockErr)
			}
		case <-ctx.Done():
			t.Fatalf("writer lock batch did not finish after Plan gate release: %v", ctx.Err())
		}
		if err := freezeQualificationPlanMigrationFixture(ctx, writerTransaction, fixture); err != nil {
			t.Fatalf("freeze after migration-order lock batch: %v", err)
		}
		if err := writerTransaction.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case downErr := <-downFinished:
			if downErr == nil || !strings.Contains(downErr.Error(), "immutable authority state is nonempty") {
				t.Fatalf("rollback after freeze commit error = %v, want nonempty refusal", downErr)
			}
		case <-ctx.Done():
			t.Fatalf("rollback did not finish after freeze commit: %v", ctx.Err())
		}
		var authorities int
		var freezeStillExists bool
		if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM qualification_plan_authorities),
  pg_catalog.to_regprocedure(
    'freeze_qualification_plan_authority(uuid,uuid,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb)'
  ) IS NOT NULL
`).Scan(&authorities, &freezeStillExists); err != nil {
			t.Fatal(err)
		}
		if authorities != 1 || !freezeStillExists {
			t.Fatalf("failed rollback state authorities=%d freezeExists=%t, want 1/true", authorities, freezeStillExists)
		}
	})

	t.Run("staged rollback blocks entry read and rollback lets freeze succeed", func(t *testing.T) {
		database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_plan_down_first_")
		applyQualificationPlanMigrations(t, ctx, database)
		fixture := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{})

		downConnection, err := database.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer downConnection.Close()
		downTransaction, err := downConnection.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer downTransaction.Rollback()
		if _, err := downTransaction.ExecContext(ctx, string(down)); err != nil {
			t.Fatalf("stage complete rollback in open transaction: %v", err)
		}

		writerConnection, err := database.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer writerConnection.Close()
		writerCtx, writerCancel := context.WithCancel(ctx)
		defer writerCancel()
		var writerPID int
		if err := writerConnection.QueryRowContext(ctx, `SELECT pg_catalog.pg_backend_pid()`).Scan(&writerPID); err != nil {
			t.Fatal(err)
		}
		writerFinished := make(chan error, 1)
		go func() {
			writerFinished <- freezeQualificationPlanMigrationFixture(writerCtx, writerConnection, fixture)
		}()
		lockCtx, lockCancel := context.WithTimeout(ctx, 5*time.Second)
		defer lockCancel()
		if err := waitForQualificationPlanMigrationLock(
			lockCtx, database, writerPID, "qualification_plan_authorities", "AccessShareLock", false, writerFinished,
		); err != nil {
			t.Fatal(err)
		}
		if err := downTransaction.Rollback(); err != nil {
			t.Fatal(err)
		}
		select {
		case writerErr := <-writerFinished:
			if writerErr != nil {
				t.Fatalf("freeze after staged rollback was released: %v", writerErr)
			}
		case <-ctx.Done():
			t.Fatalf("freeze did not finish after staged rollback was aborted: %v", ctx.Err())
		}

		var authorities, identities int
		if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM qualification_plan_authorities WHERE authority_id = $1),
  (SELECT count(*) FROM qualification_plan_identity_reservations WHERE authority_id = $1)
`, fixture.authorityID).Scan(&authorities, &identities); err != nil {
			t.Fatal(err)
		}
		if authorities != 1 || identities != 17 {
			t.Fatalf("released staged rollback freeze state authorities=%d identities=%d, want 1/17",
				authorities, identities)
		}
	})
}

type qualificationPlanMigrationQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func waitForQualificationPlanMigrationLock(
	ctx context.Context,
	database *sql.DB,
	backendPID int,
	relationName string,
	mode string,
	granted bool,
	finished <-chan error,
) error {
	for {
		var blocked bool
		if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_catalog.pg_locks
  WHERE pid = $1 AND relation = pg_catalog.to_regclass($2)
    AND mode = $3 AND granted = $4
)
`, backendPID, relationName, mode, granted).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return nil
		}
		select {
		case operationErr := <-finished:
			return fmt.Errorf("operation finished before %s granted=%t was observable: %v", mode, granted, operationErr)
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s granted=%t on %s: %w", mode, granted, relationName, ctx.Err())
		default:
			runtime.Gosched()
		}
	}
}

type qualificationPlanMigrationFixtureOptions struct {
	inputAuthorityID  uuid.UUID
	fixtureID         uuid.UUID
	artifactMismatch  bool
	invalidEncryption bool
	emptyProjection   bool
}

type qualificationPlanMigrationMaterial struct {
	hash     string
	bytes    []byte
	document string
}

type qualificationPlanMigrationFixture struct {
	operationID         uuid.UUID
	authorityID         uuid.UUID
	inputAuthorityID    uuid.UUID
	orchestrationID     uuid.UUID
	reserveOperationID  uuid.UUID
	trustBindingsDigest string
	request             qualificationPlanMigrationMaterial
	input               qualificationPlanMigrationMaterial
	projection          qualificationPlanMigrationMaterial
	evidencePlan        qualificationPlanMigrationMaterial
	evidenceDocument    map[string]any
	trust               qualificationPlanMigrationMaterial
	target              qualificationPlanMigrationMaterial
	envelope            qualificationPlanMigrationMaterial
}

func newQualificationPlanMigrationFixture(t *testing.T, options qualificationPlanMigrationFixtureOptions) qualificationPlanMigrationFixture {
	t.Helper()
	authorityID := uuid.New()
	operationID := uuid.New()
	inputAuthorityID := options.inputAuthorityID
	if inputAuthorityID == uuid.Nil {
		inputAuthorityID = uuid.New()
	}
	fixtureID := options.fixtureID
	if fixtureID == uuid.Nil {
		fixtureID = uuid.New()
	}
	orchestrationID, runID, credentialSetID := uuid.New(), uuid.New(), uuid.New()
	operations := map[string]any{
		"reserve": uuid.New().String(), "credentialIssue": uuid.New().String(),
		"runClosure": uuid.New().String(), "kmsAttestation": uuid.New().String(),
		"credentialRevocation": uuid.New().String(), "artifactIndex": uuid.New().String(),
		"receiptSign": uuid.New().String(), "snapshotSeal": uuid.New().String(),
	}
	trustBindings := map[string]any{
		"captureAuthorityId":    "spiffe://qualification.example/capture-authority",
		"credentialAuthorityId": "spiffe://qualification.example/credential-authority",
		"encryptionAuthorityId": "spiffe://qualification.example/encryption-authority",
		"indexerAuthorityId":    "spiffe://qualification.example/index-authority",
		"kmsAuthorityId":        "spiffe://qualification.example/kms-authority",
		"receiptAuthorityId":    "spiffe://qualification.example/receipt-authority",
		"sealerAuthorityId":     "spiffe://qualification.example/seal-authority",
		"verifierAuthorityId":   "spiffe://qualification.example/verify-authority",
	}
	credential := map[string]any{
		"setId": credentialSetID.String(), "issuer": trustBindings["credentialAuthorityId"],
		"audience": "urn:worksflow:golden-stack", "setHandleHash": qualificationPlanMigrationDigest("set-handle"),
		"memberBindingsDigest": qualificationPlanMigrationDigest("member-bindings"), "memberCount": 2,
		"issuanceArtifactId": "credential-set-issuance", "revocationArtifactId": "credential-set-revocation",
	}
	inputArtifacts := []any{
		map[string]any{"classification": "restricted", "id": "browser-video", "kind": "video"},
		map[string]any{"classification": "restricted", "id": "credential-safe-trace", "kind": "trace"},
		map[string]any{"classification": "distributable", "id": "golden-authority", "kind": "golden-document"},
		map[string]any{"classification": "distributable", "id": "playwright-results", "kind": "run-result"},
	}
	evidenceArtifacts := []any{
		map[string]any{"classification": "restricted", "encryptionOperationId": uuid.New().String(), "id": "browser-video", "kind": "video"},
		map[string]any{"classification": "restricted", "encryptionOperationId": uuid.New().String(), "id": "credential-safe-trace", "kind": "trace"},
		map[string]any{"classification": "distributable", "encryptionOperationId": "", "id": "golden-authority", "kind": "golden-document"},
		map[string]any{"classification": "distributable", "encryptionOperationId": "", "id": "playwright-results", "kind": "run-result"},
	}
	if options.artifactMismatch {
		inputArtifacts[0].(map[string]any)["id"] = "browser-video-other"
	}
	if options.invalidEncryption {
		evidenceArtifacts[0].(map[string]any)["encryptionOperationId"] = ""
	}
	subject := "application/project"
	sourceTreeDigest := qualificationPlanMigrationDigest("source-tree")
	projectionDocument := map[string]any{
		"manifestSchemaVersion": "worksflow-qualification-manifest/v1",
		"policy":                map[string]any{"stageExitRequiresExternalQualification": true},
		"schemaVersion":         "worksflow-qualification-plan/v1",
		"sourceDocuments":       []any{map[string]any{"path": "docs/architecture.md", "sha256": qualificationPlanMigrationDigest("architecture")}},
		"subject":               subject,
		"suites":                []any{map[string]any{"id": "external-browser", "mode": "external"}},
		"supportFiles":          []any{map[string]any{"path": "qualification/test-inventory.json", "sha256": qualificationPlanMigrationDigest("inventory")}},
	}
	if options.emptyProjection {
		projectionDocument["sourceDocuments"] = []any{}
		projectionDocument["suites"] = []any{}
		projectionDocument["supportFiles"] = []any{}
	}
	projection := qualificationPlanMigrationCanonical(t, projectionDocument)
	templateRelease := map[string]any{
		"id": uuid.New().String(), "contentHash": qualificationPlanMigrationDigest("template-content"),
		"approvalReceiptDigest": qualificationPlanMigrationDigest("template-approval"),
	}
	promotionTarget := map[string]any{
		"projectId": uuid.New().String(), "workflowRunId": uuid.New().String(), "nodeKey": "qualification.external",
		"targetRevision": map[string]any{"id": uuid.New().String(), "contentHash": qualificationPlanMigrationDigest("revision")},
		"subject":        subject, "stageGate": "external-qualification",
	}
	outputs := map[string]any{
		"kmsAttestationArtifactId": "kms-encryption-attestation", "artifactIndexId": "qualification-artifact-index",
		"receiptId": "qualification-receipt", "snapshotId": "qualification-evidence-snapshot",
	}
	recipient := map[string]any{"keyResourceId": "qualification-kms-key", "keyVersion": "version-one"}
	inputDocument := map[string]any{
		"artifactPolicy": map[string]any{"maximumArtifacts": 512, "requireRestrictedEncryption": true, "requireTrace": true, "requireVideo": true},
		"artifacts":      inputArtifacts,
		"buildContract":  map[string]any{"contentHash": qualificationPlanMigrationDigest("build-contract"), "id": "build-contract"},
		"buildManifest":  map[string]any{"contentHash": qualificationPlanMigrationDigest("build-manifest"), "id": "build-manifest"},
		"credential":     credential,
		"goldenRuntime": map[string]any{
			"authorityDocumentArtifactId": "golden-authority", "authorityDocumentDigest": qualificationPlanMigrationDigest("golden-authority"),
			"faultOperationSetDigest": qualificationPlanMigrationDigest("fault-operation-set"), "fixtureDocumentArtifactId": "golden-fixture",
			"fixtureDocumentDigest": qualificationPlanMigrationDigest("golden-fixture"), "fixtureId": fixtureID.String(),
		},
		"outputs": outputs,
		"outputPolicy": map[string]any{
			"credentialRevocation": "exact-issued-set-before-index/v1",
			"plaintextDisposition": "restricted-plaintext-disposed-before-kms/v1",
			"snapshotMode":         "immutable-filesystem",
		},
		"promotionTarget": promotionTarget,
		"qualificationManifest": map[string]any{
			"artifactId": "qualification-manifest", "contentHash": qualificationPlanMigrationDigest("manifest"),
			"revisionId": uuid.New().String(),
		},
		"qualificationPlanDigest": projection.hash,
		"recipient":               recipient,
		"schemaVersion":           "worksflow-qualification-plan-input/v1",
		"source": map[string]any{
			"commit": strings.Repeat("a", 40), "treeDigestSchema": "worksflow-source-content-tree/v1",
			"treeDigest": sourceTreeDigest, "dirty": false,
		},
		"templateRelease":   templateRelease,
		"trustBindings":     trustBindings,
		"trustPolicyDigest": qualificationPlanMigrationDigest("trust-policy"),
	}
	input := qualificationPlanMigrationCanonical(t, inputDocument)
	planArtifactID := "qualification-plan-" + authorityID.String()
	evidenceDocument := map[string]any{
		"schemaVersion":   "worksflow-qualification-evidence-plan/v1",
		"orchestrationId": orchestrationID.String(), "runId": runID.String(), "fixtureId": fixtureID.String(),
		"qualificationPlanArtifactId": planArtifactID, "planDigest": projection.hash,
		"sourceTreeDigest":      sourceTreeDigest,
		"templateReleaseDigest": qualificationPlanMigrationCanonical(t, templateRelease).hash,
		"operations":            operations, "credentialSet": credential, "artifacts": evidenceArtifacts,
		"recipient": recipient, "outputs": outputs,
	}
	evidencePlan := qualificationPlanMigrationCanonical(t, evidenceDocument)
	trustDocument := map[string]any{
		"schemaVersion": "worksflow-qualification-plan-trust/v1", "trustBindings": trustBindings,
		"trustPolicyDigest": inputDocument["trustPolicyDigest"],
	}
	trust := qualificationPlanMigrationCanonical(t, trustDocument)
	target := qualificationPlanMigrationCanonical(t, map[string]any{
		"promotionTarget": promotionTarget, "schemaVersion": "worksflow-qualification-plan-target/v1",
	})
	trustBindingsDigest := qualificationPlanMigrationCanonical(t, trustBindings).hash
	envelope := qualificationPlanMigrationCanonical(t, map[string]any{
		"artifactId": planArtifactID, "authorityId": authorityID.String(), "evidencePlanHash": evidencePlan.hash,
		"inputAuthorityId": inputAuthorityID.String(), "inputHash": input.hash,
		"manifestPlanDigest": projection.hash, "operationId": operationID.String(), "projectionHash": projection.hash,
		"schemaVersion": "worksflow-qualification-plan-authority/v1", "targetHash": target.hash,
		"trustBindingsDigest": trustBindingsDigest, "trustHash": trust.hash,
	})
	request := qualificationPlanMigrationCanonical(t, map[string]any{
		"authorityId": authorityID.String(), "inputAuthorityId": inputAuthorityID.String(),
		"operationId": operationID.String(), "schemaVersion": "worksflow-qualification-plan-freeze-request/v1",
	})
	reserveOperationID, err := uuid.Parse(operations["reserve"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return qualificationPlanMigrationFixture{
		operationID: operationID, authorityID: authorityID, inputAuthorityID: inputAuthorityID,
		orchestrationID: orchestrationID, reserveOperationID: reserveOperationID,
		trustBindingsDigest: trustBindingsDigest, request: request, input: input, projection: projection,
		evidencePlan: evidencePlan, evidenceDocument: evidenceDocument, trust: trust, target: target, envelope: envelope,
	}
}

func qualificationPlanMigrationCanonical(t *testing.T, value any) qualificationPlanMigrationMaterial {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return qualificationPlanMigrationMaterial{
		hash: qualificationPlanMigrationDigestBytes(encoded), bytes: encoded, document: string(encoded),
	}
}

func qualificationPlanMigrationDigest(seed string) string {
	return qualificationPlanMigrationDigestBytes([]byte(seed))
}

func qualificationPlanMigrationDigestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func freezeQualificationPlanMigrationFixture(
	ctx context.Context,
	database qualificationPlanMigrationQueryRower,
	fixture qualificationPlanMigrationFixture,
) error {
	arguments := []any{fixture.operationID, fixture.authorityID}
	for _, material := range []qualificationPlanMigrationMaterial{
		fixture.request, fixture.input, fixture.projection, fixture.evidencePlan,
		fixture.trust, fixture.target, fixture.envelope,
	} {
		arguments = append(arguments, material.hash, material.bytes, material.document)
	}
	var count int
	err := database.QueryRowContext(ctx, `
SELECT count(*) FROM freeze_qualification_plan_authority(
  $1,$2,$3,$4,$5::jsonb,$6,$7,$8::jsonb,$9,$10,$11::jsonb,
  $12,$13,$14::jsonb,$15,$16,$17::jsonb,$18,$19,$20::jsonb,$21,$22,$23::jsonb
)`, arguments...).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("Qualification Plan freeze returned %d rows, want 1", count)
	}
	return nil
}

func insertQualificationPlanMigrationEvidenceEvent(
	ctx context.Context,
	database *sql.DB,
	kind string,
	fixture qualificationPlanMigrationFixture,
	includePlan bool,
	eventIDs ...uuid.UUID,
) error {
	eventDocument := map[string]any{}
	orchestrationID, operationID := uuid.New(), uuid.New()
	if includePlan {
		eventDocument = map[string]any{
			"commandHash": fixture.evidencePlan.hash, "plan": fixture.evidenceDocument,
			"trustBindingsDigest": fixture.trustBindingsDigest,
		}
		orchestrationID, operationID = fixture.orchestrationID, fixture.reserveOperationID
	}
	event := qualificationPlanMigrationCanonicalForHelper(eventDocument)
	empty := qualificationPlanMigrationCanonicalForHelper(map[string]any{})
	eventID := uuid.New()
	if len(eventIDs) > 0 {
		eventID = eventIDs[0]
	}
	_, err := database.ExecContext(ctx, `
INSERT INTO qualification_evidence_events (
  event_id, orchestration_id, version, expected_version, event_kind, operation_id,
  active_artifact_id, event_at, requested_at,
  request_hash, request_bytes, request_document,
  event_hash, event_bytes, event_document
) VALUES (
  $1,$2,1,0,$3,$4,'',date_trunc('milliseconds',clock_timestamp()),date_trunc('milliseconds',clock_timestamp()),
  $5,$6,$7::jsonb,$8,$9,$10::jsonb
)`, eventID, orchestrationID, kind, operationID,
		empty.hash, empty.bytes, empty.document, event.hash, event.bytes, event.document)
	return err
}

func qualificationPlanMigrationCanonicalForHelper(value any) qualificationPlanMigrationMaterial {
	encoded, _ := json.Marshal(value)
	return qualificationPlanMigrationMaterial{
		hash: qualificationPlanMigrationDigestBytes(encoded), bytes: encoded, document: string(encoded),
	}
}

func qualificationPlanMigrationDatabase(
	t *testing.T,
	ctx context.Context,
	base *sql.DB,
	dsn string,
	prefix string,
) *sql.DB {
	t.Helper()
	schema := prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func applyQualificationPlanMigrations(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	for _, name := range []string{
		"000073_qualification_evidence_event_store.up.sql",
		"000074_qualification_plan_authority.up.sql",
	} {
		migration, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
}
