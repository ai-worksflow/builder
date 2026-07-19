package qualificationplanauthority

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
	qe "github.com/worksflow/builder/backend/internal/qualificationevidence"
)

func TestClassifyPostgresQualificationPlanFreezeError(t *testing.T) {
	for name, test := range map[string]struct {
		err  error
		want error
	}{
		"authority conflict": {&pgconn.PgError{Code: "WQP01"}, ErrConflict},
		"serialization":      {&pgconn.PgError{Code: "40001"}, ErrConflict},
		"authority invalid":  {&pgconn.PgError{Code: "WQP03"}, ErrInvalid},
		"constraint invalid": {&pgconn.PgError{Code: "23514"}, ErrInvalid},
		"transport unknown":  {errors.New("lost connection"), ErrStoreOutcomeUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			if got := classifyPostgresQualificationPlanFreezeError(test.err); !errors.Is(got, test.want) {
				t.Fatalf("classification = %v, want %v", got, test.want)
			}
		})
	}
	unknownState := classifyPostgresQualificationPlanFreezeError(&pgconn.PgError{Code: "42501"})
	if errors.Is(unknownState, ErrConflict) || errors.Is(unknownState, ErrInvalid) || errors.Is(unknownState, ErrStoreOutcomeUnknown) {
		t.Fatalf("permission failure was misclassified as a domain outcome: %v", unknownState)
	}
}

func TestStoredRecordRejectsDeterministicIdentityRewrite(t *testing.T) {
	command := validFreezeCommand()
	base, err := compileRecord(command, validResolvedInputs(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	base.FrozenAt = time.Date(2026, 7, 19, 15, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*Record){
		"orchestration":   func(record *Record) { record.EvidencePlan.OrchestrationID = uuid.NewString() },
		"fixed operation": func(record *Record) { record.EvidencePlan.Operations.ReceiptSign = uuid.NewString() },
		"encryption operation": func(record *Record) {
			for index := range record.EvidencePlan.Artifacts {
				if record.EvidencePlan.Artifacts[index].Classification == qe.ClassificationRestricted {
					record.EvidencePlan.Artifacts[index].EncryptionOperationID = uuid.NewString()
					return
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			drifted := cloneRecord(base)
			mutate(&drifted)
			drifted.EvidencePlanBytes, drifted.EvidencePlanHash, err = canonicalMaterial(drifted.EvidencePlan)
			if err != nil {
				t.Fatal(err)
			}
			drifted.Envelope.EvidencePlanHash = drifted.EvidencePlanHash
			drifted.EnvelopeBytes, drifted.EnvelopeHash, err = canonicalMaterial(drifted.Envelope)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateStoredRecord(drifted); !errors.Is(err, ErrConflict) {
				t.Fatalf("deterministic identity rewrite error = %v", err)
			}
		})
	}
}

func TestPostgresQualificationPlanAuthorityMigrationLockOrder(t *testing.T) {
	downSQL, err := os.ReadFile("../../migrations/000074_qualification_plan_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("down first", func(t *testing.T) {
		database, _, schema, cleanup := openPostgresQualificationPlanStore(t)
		defer cleanup()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		downDatabase := openPostgresQualificationPlanDedicatedDatabase(t, ctx, schema)
		downTransaction, err := downDatabase.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer downTransaction.Rollback()
		if _, err := downTransaction.ExecContext(ctx, string(downSQL)); err != nil {
			t.Fatalf("stage real migration down: %v", err)
		}

		freezeDatabase := openPostgresQualificationPlanDedicatedDatabase(t, ctx, schema)
		freezeStore, err := NewPostgresStore(freezeDatabase)
		if err != nil {
			t.Fatal(err)
		}
		freezePID := postgresQualificationPlanBackendPID(t, ctx, freezeDatabase)
		candidate, err := compileRecord(validFreezeCommand(), validResolvedInputs(t, 4))
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan postgresQualificationPlanFreezeResult, 1)
		go func() {
			record, freezeErr := freezeStore.Freeze(ctx, candidate)
			result <- postgresQualificationPlanFreezeResult{record: record, err: freezeErr}
		}()
		waitForPostgresQualificationPlanRelationLock(
			t, ctx, database, freezePID,
			"qualification_plan_authorities", "AccessShareLock", false,
		)
		if err := downTransaction.Rollback(); err != nil {
			t.Fatal(err)
		}
		outcome := receivePostgresQualificationPlanResult(t, ctx, result)
		if outcome.err != nil || outcome.record.AuthorityID != candidate.AuthorityID {
			t.Fatalf("down-first freeze = record:%+v error:%v", outcome.record, outcome.err)
		}
	})

	t.Run("store first", func(t *testing.T) {
		database, _, schema, cleanup := openPostgresQualificationPlanStore(t)
		defer cleanup()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		candidate, err := compileRecord(validFreezeCommand(), validResolvedInputs(t, 4))
		if err != nil {
			t.Fatal(err)
		}
		advisoryGate, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer advisoryGate.Rollback()
		if _, err := advisoryGate.ExecContext(ctx, `
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended($1::text, 740074)
)`, candidate.OperationID); err != nil {
			t.Fatal(err)
		}

		freezeDatabase := openPostgresQualificationPlanDedicatedDatabase(t, ctx, schema)
		freezeStore, err := NewPostgresStore(freezeDatabase)
		if err != nil {
			t.Fatal(err)
		}
		freezePID := postgresQualificationPlanBackendPID(t, ctx, freezeDatabase)
		result := make(chan postgresQualificationPlanFreezeResult, 1)
		go func() {
			record, freezeErr := freezeStore.Freeze(ctx, candidate)
			result <- postgresQualificationPlanFreezeResult{record: record, err: freezeErr}
		}()
		waitForPostgresQualificationPlanAdvisoryLock(t, ctx, database, freezePID, false)

		planGate, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer planGate.Rollback()
		if _, err := planGate.ExecContext(ctx,
			`LOCK TABLE qualification_plan_authorities IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		if err := advisoryGate.Rollback(); err != nil {
			t.Fatal(err)
		}
		for _, relation := range []string{
			"qualification_evidence_events",
			"qualification_evidence_operations",
			"qualification_evidence_heads",
		} {
			waitForPostgresQualificationPlanRelationLock(
				t, ctx, database, freezePID, relation, "ShareRowExclusiveLock", true,
			)
		}
		waitForPostgresQualificationPlanRelationLock(
			t, ctx, database, freezePID,
			"qualification_plan_authorities", "ShareRowExclusiveLock", false,
		)
		if err := planGate.Rollback(); err != nil {
			t.Fatal(err)
		}
		outcome := receivePostgresQualificationPlanResult(t, ctx, result)
		if outcome.err != nil || outcome.record.AuthorityID != candidate.AuthorityID {
			t.Fatalf("store-first freeze = record:%+v error:%v", outcome.record, outcome.err)
		}
	})
}

func TestPostgresQualificationPlanAuthorityClosure(t *testing.T) {
	database, store, schema, cleanup := openPostgresQualificationPlanStore(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	t.Run("freeze exact replay restart raw closure and DB time", func(t *testing.T) {
		command := validFreezeCommand()
		resolved := validResolvedInputs(t, 4)
		authority := &fakeInputAuthority{values: map[uuid.UUID]ResolvedInputs{command.InputAuthorityID: resolved}}
		service, err := NewService(authority, store)
		if err != nil {
			t.Fatal(err)
		}
		before := postgresQualificationPlanNow(t, ctx, database)
		first, err := service.Freeze(ctx, command)
		if err != nil {
			postgresQualificationPlanFatal(t, "Freeze", err)
		}
		after := postgresQualificationPlanNow(t, ctx, database)
		if first.Idempotent || first.FrozenAt.Before(before) || first.FrozenAt.After(after) || first.FrozenAt.Nanosecond()%int(time.Millisecond) != 0 {
			t.Fatalf("database frozenAt is not authoritative: before=%s frozen=%s after=%s idempotent=%v", before, first.FrozenAt, after, first.Idempotent)
		}
		if authority.calls != 1 {
			t.Fatalf("new freeze resolver calls = %d", authority.calls)
		}
		var authorities, reservations, materialDrift int
		if err := database.QueryRowContext(ctx, `SELECT count(*) FROM qualification_plan_authorities WHERE authority_id=$1`, command.AuthorityID).Scan(&authorities); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRowContext(ctx, `SELECT count(*) FROM qualification_plan_identity_reservations WHERE authority_id=$1`, command.AuthorityID).Scan(&reservations); err != nil {
			t.Fatal(err)
		}
		if authorities != 1 || reservations != 17 {
			t.Fatalf("authority/reservation closure = %d/%d, want 1/17", authorities, reservations)
		}
		if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM qualification_plan_authorities
WHERE qualification_plan_sha256(request_bytes) <> request_hash
   OR qualification_plan_sha256(input_bytes) <> input_hash
   OR qualification_plan_sha256(projection_bytes) <> projection_hash
   OR qualification_plan_sha256(evidence_plan_bytes) <> evidence_plan_hash
   OR qualification_plan_sha256(trust_bytes) <> trust_hash
   OR qualification_plan_sha256(target_bytes) <> target_hash
   OR qualification_plan_sha256(envelope_bytes) <> envelope_hash
   OR convert_from(request_bytes,'UTF8')::jsonb <> request_document
   OR convert_from(input_bytes,'UTF8')::jsonb <> input_document
   OR convert_from(projection_bytes,'UTF8')::jsonb <> projection_document
   OR convert_from(evidence_plan_bytes,'UTF8')::jsonb <> evidence_plan_document
   OR convert_from(trust_bytes,'UTF8')::jsonb <> trust_document
   OR convert_from(target_bytes,'UTF8')::jsonb <> target_document
   OR convert_from(envelope_bytes,'UTF8')::jsonb <> envelope_document`).Scan(&materialDrift); err != nil {
			t.Fatal(err)
		}
		if materialDrift != 0 {
			t.Fatalf("raw/hash/document drift rows = %d", materialDrift)
		}

		authority.mu.Lock()
		authority.err = errors.New("input authority retired")
		authority.mu.Unlock()
		restartedStore, err := NewPostgresStore(database)
		if err != nil {
			t.Fatal(err)
		}
		restarted, err := NewService(authority, restartedStore)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := restarted.Freeze(ctx, command)
		if err != nil || !replayed.Idempotent || !sameImmutableRecord(first, replayed) {
			t.Fatalf("restart exact replay = record:%+v error:%v", replayed, err)
		}
		if authority.calls != 1 {
			t.Fatalf("restart replay re-resolved input: %d calls", authority.calls)
		}
		resolution, err := restarted.Resolve(ctx, command.AuthorityID.String())
		if err != nil || resolution.AuthorityHash != first.EnvelopeHash || resolution.EvidencePlanHash != first.EvidencePlanHash {
			t.Fatalf("restart Resolve() = resolution:%+v error:%v", resolution, err)
		}
	})

	t.Run("operation drift global collision and shared Golden fixture", func(t *testing.T) {
		firstCommand := validFreezeCommand()
		firstResolved := validResolvedInputs(t, 4)
		firstCandidate, err := compileRecord(firstCommand, firstResolved)
		if err != nil {
			t.Fatal(err)
		}
		first, err := store.Freeze(ctx, firstCandidate)
		if err != nil {
			t.Fatal(err)
		}
		driftCommand := validFreezeCommand()
		driftCommand.OperationID = firstCommand.OperationID
		driftCandidate, err := compileRecord(driftCommand, validResolvedInputs(t, 4))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Freeze(ctx, driftCandidate); !errors.Is(err, ErrConflict) {
			t.Fatalf("same operation/different exact bytes error = %v", err)
		}

		collisionCommand := validFreezeCommand()
		collisionInput := validResolvedInputs(t, 4)
		collisionInput.Input.Credential.SetID = first.EvidencePlan.Operations.ArtifactIndex
		refreshResolvedInput(t, &collisionInput)
		collisionCandidate, err := compileRecord(collisionCommand, collisionInput)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Freeze(ctx, collisionCandidate); !errors.Is(err, ErrConflict) {
			t.Fatalf("cross-kind global identity collision error = %v", err)
		}

		sharedCommand := validFreezeCommand()
		sharedInput := validResolvedInputs(t, 4)
		sharedInput.Input.GoldenRuntime.FixtureID = first.EvidencePlan.FixtureID
		refreshResolvedInput(t, &sharedInput)
		sharedCandidate, err := compileRecord(sharedCommand, sharedInput)
		if err != nil {
			t.Fatal(err)
		}
		shared, err := store.Freeze(ctx, sharedCandidate)
		if err != nil || shared.EvidencePlan.FixtureID != first.EvidencePlan.FixtureID {
			t.Fatalf("shared Golden fixture freeze = record:%+v error:%v", shared, err)
		}
	})

	t.Run("concurrent exact and conflicting operations", func(t *testing.T) {
		exactCommand := validFreezeCommand()
		exactCandidate, err := compileRecord(exactCommand, validResolvedInputs(t, 4))
		if err != nil {
			t.Fatal(err)
		}
		exactResults := concurrentlyFreezePostgresQualificationPlans(ctx, store, exactCandidate, exactCandidate)
		nonIdempotent := 0
		for _, result := range exactResults {
			if result.err != nil {
				t.Fatalf("concurrent exact Freeze error = %v", result.err)
			}
			if !result.record.Idempotent {
				nonIdempotent++
			}
			if result.record.EnvelopeHash != exactCandidate.EnvelopeHash {
				t.Fatal("concurrent exact Freeze returned different authority bytes")
			}
		}
		if nonIdempotent != 1 {
			t.Fatalf("concurrent exact non-idempotent writers = %d", nonIdempotent)
		}

		leftCommand := validFreezeCommand()
		leftCandidate, err := compileRecord(leftCommand, validResolvedInputs(t, 4))
		if err != nil {
			t.Fatal(err)
		}
		rightCommand := validFreezeCommand()
		rightCommand.OperationID = leftCommand.OperationID
		rightCandidate, err := compileRecord(rightCommand, validResolvedInputs(t, 4))
		if err != nil {
			t.Fatal(err)
		}
		conflictResults := concurrentlyFreezePostgresQualificationPlans(ctx, store, leftCandidate, rightCandidate)
		succeeded, conflicted := 0, 0
		for _, result := range conflictResults {
			switch {
			case result.err == nil:
				succeeded++
			case errors.Is(result.err, ErrConflict):
				conflicted++
			default:
				t.Fatalf("concurrent drift unexpected error = %v", result.err)
			}
		}
		if succeeded != 1 || conflicted != 1 {
			t.Fatalf("concurrent drift outcomes succeeded=%d conflicted=%d", succeeded, conflicted)
		}
	})

	t.Run("commit unknown is reconciled and restart is inspect-only", func(t *testing.T) {
		command := validFreezeCommand()
		resolved := validResolvedInputs(t, 4)
		authority := &fakeInputAuthority{values: map[uuid.UUID]ResolvedInputs{command.InputAuthorityID: resolved}}
		service, err := NewService(authority, store)
		if err != nil {
			t.Fatal(err)
		}
		store.commit = func(transaction *sql.Tx) error {
			if err := transaction.Commit(); err != nil {
				return err
			}
			return errors.New("simulated lost commit acknowledgement")
		}
		reconciled, err := service.Freeze(ctx, command)
		store.commit = func(transaction *sql.Tx) error { return transaction.Commit() }
		if err != nil || !reconciled.Idempotent || reconciled.OperationID != command.OperationID {
			t.Fatalf("commit unknown reconciliation = record:%+v error:%v", reconciled, err)
		}
		if authority.calls != 1 {
			t.Fatalf("commit unknown resolver calls = %d", authority.calls)
		}
		authority.mu.Lock()
		authority.err = errors.New("must remain inspect-only")
		authority.mu.Unlock()
		restartedStore, _ := NewPostgresStore(database)
		restartedService, _ := NewService(authority, restartedStore)
		replayed, err := restartedService.Freeze(ctx, command)
		if err != nil || !replayed.Idempotent || replayed.EnvelopeHash != reconciled.EnvelopeHash || authority.calls != 1 {
			t.Fatalf("post-unknown restart = record:%+v calls:%d error:%v", replayed, authority.calls, err)
		}
	})

	t.Run("database time is sampled after the freeze lock", func(t *testing.T) {
		blocker, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		if _, err := blocker.ExecContext(ctx, `LOCK TABLE qualification_plan_authorities IN ACCESS EXCLUSIVE MODE`); err != nil {
			_ = blocker.Rollback()
			t.Fatal(err)
		}
		dedicated := openPostgresQualificationPlanDedicatedDatabase(t, ctx, schema)
		var pid int
		if err := dedicated.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			_ = blocker.Rollback()
			t.Fatal(err)
		}
		dedicatedStore, err := NewPostgresStore(dedicated)
		if err != nil {
			_ = blocker.Rollback()
			t.Fatal(err)
		}
		command := validFreezeCommand()
		candidate, err := compileRecord(command, validResolvedInputs(t, 4))
		if err != nil {
			_ = blocker.Rollback()
			t.Fatal(err)
		}
		result := make(chan postgresQualificationPlanFreezeResult, 1)
		go func() {
			record, freezeErr := dedicatedStore.Freeze(ctx, candidate)
			result <- postgresQualificationPlanFreezeResult{record: record, err: freezeErr}
		}()
		waitForPostgresQualificationPlanLock(t, ctx, database, pid)
		releaseTime := postgresQualificationPlanNow(t, ctx, database)
		if err := blocker.Commit(); err != nil {
			t.Fatal(err)
		}
		outcome := receivePostgresQualificationPlanResult(t, ctx, result)
		if outcome.err != nil || outcome.record.FrozenAt.Before(releaseTime) {
			t.Fatalf("post-lock database time = frozen:%s release:%s error:%v", outcome.record.FrozenAt, releaseTime, outcome.err)
		}
	})

	t.Run("migration 73 reservation requires and consumes exact authority", func(t *testing.T) {
		command := validFreezeCommand()
		resolved := validResolvedInputs(t, 4)
		authority := &fakeInputAuthority{values: map[uuid.UUID]ResolvedInputs{command.InputAuthorityID: resolved}}
		service, _ := NewService(authority, store)
		record, err := service.Freeze(ctx, command)
		if err != nil {
			t.Fatal(err)
		}
		evidenceStore, err := qe.NewPostgresStore(database)
		if err != nil {
			t.Fatal(err)
		}
		requestedAt, err := evidenceStore.TrustedTime(ctx)
		if err != nil {
			t.Fatal(err)
		}
		plan := record.EvidencePlan
		event := qe.Event{
			At: requestedAt.Format("2006-01-02T15:04:05.000Z"), EventID: uuid.NewString(), Kind: qe.EventReserved,
			OperationID: plan.Operations.Reserve, CommandHash: record.EvidencePlanHash,
			TrustBindingsDigest: record.Envelope.TrustBindingsDigest, Plan: &plan,
		}
		snapshot, created, err := evidenceStore.Create(ctx, plan.OrchestrationID, event)
		if err != nil || !created || snapshot.Version != 1 || snapshot.CommandHash != record.EvidencePlanHash {
			postgresQualificationPlanFatal(t, "authorized migration73 reservation", err)
		}

		unauthorizedCommand := validFreezeCommand()
		unauthorizedRecord, err := compileRecord(unauthorizedCommand, validResolvedInputs(t, 4))
		if err != nil {
			t.Fatal(err)
		}
		unauthorizedPlan := unauthorizedRecord.EvidencePlan
		unauthorizedEvent := qe.Event{
			At: requestedAt.Format("2006-01-02T15:04:05.000Z"), EventID: uuid.NewString(), Kind: qe.EventReserved,
			OperationID: unauthorizedPlan.Operations.Reserve, CommandHash: unauthorizedRecord.EvidencePlanHash,
			TrustBindingsDigest: unauthorizedRecord.Envelope.TrustBindingsDigest, Plan: &unauthorizedPlan,
		}
		_, _, err = evidenceStore.Create(ctx, unauthorizedPlan.OrchestrationID, unauthorizedEvent)
		var postgresError *pgconn.PgError
		if err == nil || !errors.As(err, &postgresError) || postgresError.Code != "WQP03" {
			t.Fatalf("unauthorized migration73 reservation error = %v", err)
		}

		collisionCommand := validFreezeCommand()
		collisionInput := validResolvedInputs(t, 4)
		collisionInput.Input.Credential.SetID = event.EventID
		refreshResolvedInput(t, &collisionInput)
		collisionCandidate, err := compileRecord(collisionCommand, collisionInput)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Freeze(ctx, collisionCandidate); !errors.Is(err, ErrConflict) {
			t.Fatalf("migration73 EventID reverse collision error = %v", err)
		}
	})
}

type postgresQualificationPlanFreezeResult struct {
	record Record
	err    error
}

func concurrentlyFreezePostgresQualificationPlans(ctx context.Context, store *PostgresStore, candidates ...Record) []postgresQualificationPlanFreezeResult {
	start := make(chan struct{})
	ready := sync.WaitGroup{}
	ready.Add(len(candidates))
	results := make(chan postgresQualificationPlanFreezeResult, len(candidates))
	for _, candidate := range candidates {
		go func(value Record) {
			ready.Done()
			<-start
			record, err := store.Freeze(ctx, value)
			results <- postgresQualificationPlanFreezeResult{record: record, err: err}
		}(candidate)
	}
	ready.Wait()
	close(start)
	output := make([]postgresQualificationPlanFreezeResult, 0, len(candidates))
	for range candidates {
		output = append(output, <-results)
	}
	return output
}

func openPostgresQualificationPlanStore(t *testing.T) (*sql.DB, *PostgresStore, string, func()) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.PingContext(ctx); err != nil {
		_ = base.Close()
		t.Fatal(err)
	}
	if _, err := base.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		_ = base.Close()
		t.Fatal(err)
	}
	schema := "qualification_plan_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`" AUTHORIZATION worksflow_migration_owner`); err != nil {
		_ = base.Close()
		t.Fatal(err)
	}
	database, err := sql.Open("pgx", postgresQualificationPlanDSN(t, dsn, schema))
	if err != nil {
		_ = base.Close()
		t.Fatal(err)
	}
	database.SetMaxOpenConns(32)
	for _, migration := range []string{
		"../../migrations/000073_qualification_evidence_event_store.up.sql",
		"../../migrations/000074_qualification_plan_authority.up.sql",
	} {
		up, err := os.ReadFile(migration)
		if err != nil {
			_ = database.Close()
			_ = base.Close()
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, string(up)); err != nil {
			var postgresError *pgconn.PgError
			if errors.As(err, &postgresError) {
				t.Fatalf("apply %s: sqlstate=%s position=%d message=%s detail=%s", migration, postgresError.Code, postgresError.Position, postgresError.Message, postgresError.Detail)
			}
			t.Fatalf("apply %s: %v", migration, err)
		}
	}
	store, err := NewPostgresStore(database)
	if err != nil {
		_ = database.Close()
		_ = base.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		_ = database.Close()
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
		_ = base.Close()
	}
	return database, store, schema, cleanup
}

func postgresQualificationPlanDSN(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema+",public")
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema + ",public"
}

func openPostgresQualificationPlanDedicatedDatabase(t *testing.T, ctx context.Context, schema string) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	database, err := sql.Open("pgx", postgresQualificationPlanDSN(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func postgresQualificationPlanBackendPID(t *testing.T, ctx context.Context, database *sql.DB) int {
	t.Helper()
	var pid int
	if err := database.QueryRowContext(ctx, `SELECT pg_catalog.pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return pid
}

func waitForPostgresQualificationPlanAdvisoryLock(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	pid int,
	granted bool,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var found bool
		if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_catalog.pg_locks
  WHERE pid=$1 AND locktype='advisory' AND granted=$2
)`, pid, granted).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("backend %d did not expose advisory granted=%t", pid, granted)
		}
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
	}
}

func waitForPostgresQualificationPlanLock(t *testing.T, ctx context.Context, database *sql.DB, pid int) {
	t.Helper()
	waitForPostgresQualificationPlanRelationLock(
		t, ctx, database, pid,
		"qualification_plan_authorities", "", false,
	)
}

func waitForPostgresQualificationPlanRelationLock(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	pid int,
	relation string,
	mode string,
	granted bool,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var found bool
		if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_catalog.pg_locks
  WHERE pid=$1
    AND relation=pg_catalog.to_regclass($2)::oid
    AND ($3 = '' OR mode=$3)
    AND granted=$4
)`, pid, relation, mode, granted).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"backend %d did not expose %s granted=%t on %s",
				pid, mode, granted, relation,
			)
		}
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
	}
}

func receivePostgresQualificationPlanResult(t *testing.T, ctx context.Context, result <-chan postgresQualificationPlanFreezeResult) postgresQualificationPlanFreezeResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return postgresQualificationPlanFreezeResult{err: ctx.Err()}
	}
}

func postgresQualificationPlanNow(t *testing.T, ctx context.Context, database *sql.DB) time.Time {
	t.Helper()
	var now time.Time
	if err := database.QueryRowContext(ctx, `SELECT date_trunc('milliseconds',clock_timestamp())`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now.UTC()
}

func postgresQualificationPlanFatal(t *testing.T, operation string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s returned an invalid empty result without error", operation)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		t.Fatalf("%s: %v sqlstate=%s detail=%s where=%s", operation, err, postgresError.Code, postgresError.Detail, postgresError.Where)
	}
	t.Fatalf("%s: %v", operation, err)
}
