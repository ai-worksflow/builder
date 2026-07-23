package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestQualificationReceiptV3StoreMigrationIsSnapshotFirstImmutableAndOwnerOnly(t *testing.T) {
	up, err := files.ReadFile("000075_qualification_receipt_v3_store.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000075_qualification_receipt_v3_store.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upText := string(up)
	for _, required := range []string{
		"CREATE TABLE qualification_receipt_v3_requests",
		"CREATE TABLE qualification_receipt_v3_observations",
		"CREATE TABLE qualification_receipt_v3_receipts",
		"CREATE FUNCTION start_qualification_receipt_v3_requests(",
		"CREATE FUNCTION append_qualification_receipt_v3_observation(",
		"CREATE FUNCTION complete_qualification_receipt_v3(",
		"CREATE FUNCTION reject_qualification_receipt_v3_mutation()",
		"CREATE FUNCTION guard_qualification_evidence_v1_receipt_tail_history_only()",
		"CREATE FUNCTION guard_qualification_promotion_v1_new_consumption_history_only()",
		"worksflow-qualification-receipt/v3",
		"https://worksflow.dev/attestations/qualification-receipt/v3",
		"application/vnd.in-toto+json",
		"worksflow-qualification-receipt-control-request/v1",
		"worksflow-qualification-receipt-control-observation-payload/v1",
		"worksflow-qualification-receipt-control-observation-proof/v1",
		"worksflow-qualification-receipt-control-claim/v1",
		"worksflow-qualification-receipt-control-acknowledgement/v1",
		"worksflow-qualification-receipt-control-completion/v1",
		"pg_catalog.sha256($1)",
		"qualification_receipt_v3_sha256(p_payload_bytes) <> p_payload_hash",
		"qualification_receipt_v3_sha256(p_pae_bytes) <> p_pae_hash",
		`"authenticationEnvelopeHash"`,
		`"observedAt"`,
		`"recordedAt"`,
		"BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_receipt_v3_requests",
		"BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_receipt_v3_observations",
		"BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_receipt_v3_receipts",
		"receipt-sign must atomically",
		"NEW.event_kind IN (",
		"Qualification promotion v1 is history-only after Receipt v3 activation",
		"ALTER TABLE %I.qualification_receipt_v3_requests OWNER TO worksflow_migration_owner",
		"REVOKE ALL ON FUNCTION %I.start_qualification_receipt_v3_requests(",
	} {
		if !strings.Contains(upText, required) {
			t.Fatalf("Qualification Receipt v3 migration is missing %q", required)
		}
	}
	if strings.Count(upText, "CREATE TABLE ") != 3 {
		t.Fatalf("Qualification Receipt v3 table count = %d, want 3", strings.Count(upText, "CREATE TABLE "))
	}
	if strings.Count(upText, "CREATE FUNCTION") != 7 {
		t.Fatalf("Qualification Receipt v3 function count = %d, want 7", strings.Count(upText, "CREATE FUNCTION"))
	}
	if strings.Count(upText, "CREATE TRIGGER") != 3 {
		// The two history-only triggers are created inside the fenced DO block.
		t.Fatalf("Qualification Receipt v3 direct trigger count = %d, want 3", strings.Count(upText, "CREATE TRIGGER"))
	}
	if strings.Count(upText, "\nSECURITY DEFINER\n") != 3 {
		t.Fatalf("Qualification Receipt v3 SECURITY DEFINER count = %d, want 3", strings.Count(upText, "\nSECURITY DEFINER\n"))
	}
	for _, forbidden := range []string{
		"CREATE ROLE", "GRANT EXECUTE", "GRANT SELECT", "GRANT INSERT", "GRANT UPDATE", "GRANT DELETE",
		"worksflow_qualification_receipt_operator", "postgres://", "WORKSFLOW_TEST_POSTGRES_DSN", "ON DELETE CASCADE",
		"metadata json", "metadata jsonb", "request_id uuid", "observation_id uuid", ".digest($1",
	} {
		if strings.Contains(strings.ToLower(upText), strings.ToLower(forbidden)) {
			t.Fatalf("Qualification Receipt v3 migration contains forbidden SQL %q", forbidden)
		}
	}
	assertQualificationReceiptV3LockOrder(t, upText, "SHARE ROW EXCLUSIVE")
	downText := string(down)
	assertQualificationRollbackFencePrecedesRelations(t, downText)
	assertQualificationReceiptV3LockOrder(t, downText, "ACCESS EXCLUSIVE")
	for _, required := range []string{
		"IF EXISTS (SELECT 1 FROM qualification_receipt_v3_requests)",
		"OR EXISTS (SELECT 1 FROM qualification_receipt_v3_observations)",
		"OR EXISTS (SELECT 1 FROM qualification_receipt_v3_receipts)",
		"cannot roll back Qualification Receipt v3 while immutable control state is nonempty",
		"DROP TRIGGER IF EXISTS qualification_promotion_v1_new_consumption_history_only",
		"DROP TRIGGER IF EXISTS qualification_evidence_v1_receipt_tail_history_only",
		"DROP TABLE IF EXISTS qualification_receipt_v3_receipts",
		"DROP TABLE IF EXISTS qualification_receipt_v3_observations",
		"DROP TABLE IF EXISTS qualification_receipt_v3_requests",
	} {
		if !strings.Contains(downText, required) {
			t.Fatalf("Qualification Receipt v3 rollback is missing %q", required)
		}
	}
}

func assertQualificationReceiptV3LockOrder(t *testing.T, text, mode string) {
	t.Helper()
	previous := -1
	for _, relation := range []string{
		"qualification_evidence_events",
		"qualification_evidence_operations",
		"qualification_evidence_heads",
		"qualification_plan_authorities",
		"qualification_plan_identity_reservations",
		"qualification_receipt_v3_requests",
		"qualification_receipt_v3_observations",
		"qualification_receipt_v3_receipts",
	} {
		needle := "LOCK TABLE " + relation + " IN " + mode + " MODE"
		position := strings.Index(text, needle)
		if position < 0 || position <= previous {
			t.Fatalf("Qualification Receipt v3 lock %q is absent or out of order", needle)
		}
		previous = position
	}
}

// waitForQualificationReceiptV3Lock is used by the rollback/writer canary. It
// observes pg_locks rather than sleeping, so a deadlock-order regression is a
// deterministic failure rather than a timing-sensitive test.
func waitForQualificationReceiptV3Lock(
	ctx context.Context,
	database *sql.DB,
	backendPID int,
	relationName string,
	mode string,
	granted bool,
	finished <-chan error,
) error {
	for {
		var found bool
		if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_catalog.pg_locks
  WHERE pid = $1 AND relation = pg_catalog.to_regclass($2)
    AND mode = $3 AND granted = $4
)
`, backendPID, relationName, mode, granted).Scan(&found); err != nil {
			return err
		}
		if found {
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

func qualificationReceiptV3Postgres(t *testing.T) (context.Context, *sql.DB, string) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	if _, err := base.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		t.Fatal(err)
	}
	return ctx, base, dsn
}

func TestQualificationReceiptV3StoreMigrationPostgresCanary(t *testing.T) {
	ctx, base, dsn := qualificationReceiptV3Postgres(t)
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_receipt_v3_")
	applyQualificationReceiptV3Migrations(t, ctx, database, true)

	var tables, indexes, functions, definers, triggers int
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM pg_catalog.pg_class
   WHERE relnamespace = pg_catalog.current_schema()::regnamespace AND relkind = 'r'
     AND relname IN ('qualification_receipt_v3_requests','qualification_receipt_v3_observations',
       'qualification_receipt_v3_receipts')),
  (SELECT count(*) FROM pg_catalog.pg_index AS i
   JOIN pg_catalog.pg_class AS c ON c.oid = i.indrelid
   WHERE c.relnamespace = pg_catalog.current_schema()::regnamespace
     AND c.relname IN ('qualification_receipt_v3_requests','qualification_receipt_v3_observations',
       'qualification_receipt_v3_receipts')),
  (SELECT count(*) FROM pg_catalog.pg_proc
   WHERE pronamespace = pg_catalog.current_schema()::regnamespace
     AND proname IN ('qualification_receipt_v3_sha256','reject_qualification_receipt_v3_mutation',
       'start_qualification_receipt_v3_requests','append_qualification_receipt_v3_observation',
       'complete_qualification_receipt_v3',
       'guard_qualification_evidence_v1_receipt_tail_history_only',
       'guard_qualification_promotion_v1_new_consumption_history_only')),
  (SELECT count(*) FROM pg_catalog.pg_proc
   WHERE pronamespace = pg_catalog.current_schema()::regnamespace AND prosecdef
     AND proname IN ('qualification_receipt_v3_sha256','reject_qualification_receipt_v3_mutation',
       'start_qualification_receipt_v3_requests','append_qualification_receipt_v3_observation',
       'complete_qualification_receipt_v3',
       'guard_qualification_evidence_v1_receipt_tail_history_only',
       'guard_qualification_promotion_v1_new_consumption_history_only')),
  (SELECT count(*) FROM pg_catalog.pg_trigger AS trigger
   JOIN pg_catalog.pg_class AS relation ON relation.oid = trigger.tgrelid
   WHERE relation.relnamespace = pg_catalog.current_schema()::regnamespace
     AND NOT trigger.tgisinternal
     AND trigger.tgname IN ('qualification_receipt_v3_requests_immutable',
       'qualification_receipt_v3_observations_immutable','qualification_receipt_v3_receipts_immutable',
       'qualification_evidence_v1_receipt_tail_history_only',
       'qualification_promotion_v1_new_consumption_history_only'))
`).Scan(&tables, &indexes, &functions, &definers, &triggers); err != nil {
		t.Fatal(err)
	}
	if tables != 3 || indexes != 14 || functions != 7 || definers != 3 || triggers != 5 {
		t.Fatalf("Receipt v3 PG objects tables=%d indexes=%d functions=%d definers=%d triggers=%d, want 3/14/7/3/5",
			tables, indexes, functions, definers, triggers)
	}
	var publicTableACL, publicFunctionACL, fixedPaths bool
	if err := database.QueryRowContext(ctx, `
SELECT
  NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_class
    WHERE relnamespace = pg_catalog.current_schema()::regnamespace
      AND relname IN ('qualification_receipt_v3_requests','qualification_receipt_v3_observations',
        'qualification_receipt_v3_receipts')
      AND pg_catalog.has_table_privilege('public', oid, 'SELECT,INSERT,UPDATE,DELETE,TRUNCATE')
  ),
  NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_proc
    WHERE pronamespace = pg_catalog.current_schema()::regnamespace
      AND proname IN ('start_qualification_receipt_v3_requests',
        'append_qualification_receipt_v3_observation','complete_qualification_receipt_v3')
      AND pg_catalog.has_function_privilege('public', oid, 'EXECUTE')
  ),
  NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_proc
    WHERE pronamespace = pg_catalog.current_schema()::regnamespace
      AND proname IN ('start_qualification_receipt_v3_requests',
        'append_qualification_receipt_v3_observation','complete_qualification_receipt_v3')
      AND proconfig IS DISTINCT FROM
        ARRAY['search_path=pg_catalog, ' || pg_catalog.current_schema() || ', pg_temp']::text[]
  )
`).Scan(&publicTableACL, &publicFunctionACL, &fixedPaths); err != nil {
		t.Fatal(err)
	}
	if !publicTableACL || !publicFunctionACL || !fixedPaths {
		t.Fatalf("Receipt v3 posture publicTablesRevoked=%t publicFunctionsRevoked=%t fixedPaths=%t, want true",
			publicTableACL, publicFunctionACL, fixedPaths)
	}

	fixture := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{})
	if err := freezeQualificationPlanMigrationFixture(ctx, database, fixture); err != nil {
		t.Fatalf("freeze Plan Authority: %v", err)
	}
	indexed := seedQualificationReceiptV3IndexedEvidence(t, ctx, database, fixture)
	seal := qualificationReceiptV3Request(t, fixture, indexed, "snapshot-seal", "sealer", "", "", "", nil, "", nil)
	created, err := startQualificationReceiptV3(ctx, database, seal, qualificationPlanMigrationMaterial{}, nil, nil)
	if err != nil || !created {
		t.Fatalf("start snapshot seal created=%t error=%v", created, err)
	}
	created, err = startQualificationReceiptV3(ctx, database, seal, qualificationPlanMigrationMaterial{}, nil, nil)
	if err != nil || created {
		t.Fatalf("exact snapshot seal replay created=%t error=%v", created, err)
	}

	pending := qualificationReceiptV3Observation(t, seal, 1, 1, "pending", nil, nil, nil, nil)
	pendingRecord := appendQualificationReceiptV3(t, ctx, database, pending)
	if pendingRecord.hash == "" || pendingRecord.recordedAt.IsZero() {
		t.Fatal("pending observation did not receive DB record hash/time")
	}
	snapshotDigest := qualificationPlanMigrationDigest("v3-pre-receipt-snapshot")
	sealResult := qualificationReceiptV3Canonical(t, map[string]any{
		"artifactIndexDigest": indexed.indexDigest, "authorityId": seal.documentValue["operationalAuthorityId"],
		"evidenceClosureDigest": indexed.closureDigest, "mode": "immutable-filesystem",
		"operationId": seal.documentValue["operationId"], "requestDigest": seal.material.hash,
		"schemaVersion": "worksflow-qualification-pre-receipt-snapshot/v3",
		"sealedAt":      qualificationReceiptV3Time(time.Now()), "snapshotDigest": snapshotDigest,
		"snapshotId": seal.documentValue["snapshotId"], "stage": "committed",
	})
	sealCommitted := qualificationReceiptV3Observation(t, seal, 2, 1, "committed", &sealResult, nil, nil, nil)
	sealRecord := appendQualificationReceiptV3(t, ctx, database, sealCommitted)
	if sealRecord.hash == pendingRecord.hash {
		t.Fatal("terminal seal observation reused pending record hash")
	}

	verification := qualificationReceiptV3Request(t, fixture, indexed, "snapshot-verify", "verifier",
		snapshotDigest, "", "", nil, "", nil)
	created, err = startQualificationReceiptV3(ctx, database, verification, qualificationPlanMigrationMaterial{}, nil, nil)
	if err != nil || !created {
		t.Fatalf("start snapshot verification created=%t error=%v", created, err)
	}
	appendQualificationReceiptV3(t, ctx, database,
		qualificationReceiptV3Observation(t, verification, 1, 1, "pending", nil, nil, nil, nil))
	verificationResult := qualificationReceiptV3Canonical(t, map[string]any{
		"artifactIndexDigest":   indexed.indexDigest,
		"authorityId":           verification.documentValue["operationalAuthorityId"],
		"evidenceClosureDigest": indexed.closureDigest, "result": "verified",
		"schemaVersion":  "worksflow-qualification-snapshot-verification/v3",
		"snapshotDigest": snapshotDigest, "snapshotId": verification.documentValue["snapshotId"],
		"verifiedAt": qualificationReceiptV3Time(time.Now()),
	})
	verificationRecord := appendQualificationReceiptV3(t, ctx, database,
		qualificationReceiptV3Observation(t, verification, 2, 1, "committed", &verificationResult, nil, nil, nil))

	payload, pae := qualificationReceiptV3Payload(t, fixture, indexed, sealResult, verificationResult)
	runner := qualificationReceiptV3Request(t, fixture, indexed, "receipt-sign", "qualification-runner",
		snapshotDigest, "qualification-receipt", "runner-key", payload.bytes, pae.hash, pae.bytes)
	approver := qualificationReceiptV3Request(t, fixture, indexed, "receipt-sign", "release-approver",
		snapshotDigest, "qualification-receipt", "approver-key", payload.bytes, pae.hash, pae.bytes)
	badApprover := approver
	badApprover.documentValue = qualificationReceiptV3CloneMap(approver.documentValue)
	badApprover.documentValue["role"] = "qualification-runner"
	badApprover.material = qualificationReceiptV3Canonical(t, badApprover.documentValue)
	if _, err := startQualificationReceiptV3(ctx, database, runner, badApprover.material, payload.bytes, pae.bytes); err == nil {
		t.Fatal("non-independent dual signer batch was accepted")
	}
	var signRows int
	if err := database.QueryRowContext(ctx,
		`SELECT count(*) FROM qualification_receipt_v3_requests WHERE request_kind = 'receipt-sign'`).Scan(&signRows); err != nil {
		t.Fatal(err)
	}
	if signRows != 0 {
		t.Fatalf("failed dual signer batch left %d partial rows", signRows)
	}
	created, err = startQualificationReceiptV3(ctx, database, runner, approver.material, payload.bytes, pae.bytes)
	if err != nil || !created {
		t.Fatalf("atomic dual signer start created=%t error=%v", created, err)
	}
	created, err = startQualificationReceiptV3(ctx, database, runner, approver.material, payload.bytes, pae.bytes)
	if err != nil || created {
		t.Fatalf("dual signer exact replay created=%t error=%v", created, err)
	}

	runnerRecord := qualificationReceiptV3CommitSigner(t, ctx, database, runner, bytes.Repeat([]byte{0x31}, 64))
	approverRecord := qualificationReceiptV3CommitSigner(t, ctx, database, approver, bytes.Repeat([]byte{0x52}, 64))
	envelope := qualificationReceiptV3Envelope(t, payload.bytes, runner, runnerRecord, approver, approverRecord)
	completed := completeQualificationReceiptV3(t, ctx, database, fixture,
		seal, sealRecord, verification, verificationRecord, runner, runnerRecord, approver, approverRecord,
		payload, pae, envelope)
	if completed.receiptID != "qualification-receipt" || completed.completionHash == "" {
		t.Fatalf("completion receipt=%q hash=%q", completed.receiptID, completed.completionHash)
	}
	replayed := completeQualificationReceiptV3(t, ctx, database, fixture,
		seal, sealRecord, verification, verificationRecord, runner, runnerRecord, approver, approverRecord,
		payload, pae, envelope)
	if replayed.completionHash != completed.completionHash || !replayed.completedAt.Equal(completed.completedAt) {
		t.Fatal("exact completion replay changed DB-assigned record hash/time")
	}

	concurrentFixture := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{})
	if err := freezeQualificationPlanMigrationFixture(ctx, database, concurrentFixture); err != nil {
		t.Fatalf("freeze concurrent Plan Authority: %v", err)
	}
	concurrentIndexed := seedQualificationReceiptV3IndexedEvidence(t, ctx, database, concurrentFixture)
	concurrentSeal := qualificationReceiptV3Request(t, concurrentFixture, concurrentIndexed,
		"snapshot-seal", "sealer", "", "", "", nil, "", nil)
	type startResult struct {
		created bool
		err     error
	}
	startResults := make(chan startResult, 2)
	for range 2 {
		go func() {
			created, startErr := startQualificationReceiptV3(
				ctx, database, concurrentSeal, qualificationPlanMigrationMaterial{}, nil, nil,
			)
			startResults <- startResult{created: created, err: startErr}
		}()
	}
	createdCount := 0
	for range 2 {
		result := <-startResults
		if result.err != nil {
			t.Fatalf("concurrent Receipt v3 start: %v", result.err)
		}
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("concurrent Receipt v3 start fresh owners=%d, want 1", createdCount)
	}
	concurrentPending := qualificationReceiptV3Observation(
		t, concurrentSeal, 1, 1, "pending", nil, nil, nil, nil,
	)
	appendResults := make(chan struct {
		record qualificationReceiptV3Record
		err    error
	}, 2)
	for range 2 {
		go func() {
			record, appendErr := appendQualificationReceiptV3Raw(ctx, database, concurrentPending)
			appendResults <- struct {
				record qualificationReceiptV3Record
				err    error
			}{record: record, err: appendErr}
		}()
	}
	var concurrentRecords []qualificationReceiptV3Record
	for range 2 {
		result := <-appendResults
		if result.err != nil {
			t.Fatalf("concurrent Receipt v3 append: %v", result.err)
		}
		concurrentRecords = append(concurrentRecords, result.record)
	}
	if concurrentRecords[0].hash != concurrentRecords[1].hash ||
		!concurrentRecords[0].recordedAt.Equal(concurrentRecords[1].recordedAt) ||
		concurrentRecords[0].idempotent == concurrentRecords[1].idempotent {
		t.Fatal("concurrent observation append did not produce one fresh row and one exact replay")
	}

	claimID, acknowledgementID := uuid.New(), uuid.New()
	claim, acknowledgement := qualificationReceiptV3RecoveryMaterials(
		t, concurrentSeal, concurrentPending, claimID, acknowledgementID,
	)
	appendQualificationReceiptV3(t, ctx, database, qualificationReceiptV3Observation(
		t, concurrentSeal, 2, 1, "not-invoked", nil, nil, &claim, &acknowledgement,
	))
	retryPending := qualificationReceiptV3Observation(t, concurrentSeal, 3, 2, "pending", nil, nil, nil, nil)
	retryFresh := appendQualificationReceiptV3(t, ctx, database, retryPending)
	retryReplay := appendQualificationReceiptV3(t, ctx, database, retryPending)
	if retryFresh.idempotent || !retryReplay.idempotent || retryFresh.hash != retryReplay.hash ||
		!retryFresh.recordedAt.Equal(retryReplay.recordedAt) {
		t.Fatal("authenticated retry exact replay changed ownership or Store closure")
	}
	appendQualificationReceiptV3(t, ctx, database, qualificationReceiptV3Observation(
		t, concurrentSeal, 4, 2, "rejected", nil, nil, nil, nil,
	))
	if _, err := appendQualificationReceiptV3Raw(ctx, database,
		qualificationReceiptV3Observation(t, concurrentSeal, 5, 3, "pending", nil, nil, nil, nil)); err == nil {
		t.Fatal("rejected terminal observation admitted a later retry generation")
	}

	reuseFixture := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{})
	if err := freezeQualificationPlanMigrationFixture(ctx, database, reuseFixture); err != nil {
		t.Fatalf("freeze token-reuse Plan Authority: %v", err)
	}
	reuseIndexed := seedQualificationReceiptV3IndexedEvidence(t, ctx, database, reuseFixture)
	reuseSeal := qualificationReceiptV3Request(t, reuseFixture, reuseIndexed,
		"snapshot-seal", "sealer", "", "", "", nil, "", nil)
	if created, err := startQualificationReceiptV3(
		ctx, database, reuseSeal, qualificationPlanMigrationMaterial{}, nil, nil,
	); err != nil || !created {
		t.Fatalf("start token-reuse request created=%t error=%v", created, err)
	}
	reusePending := qualificationReceiptV3Observation(t, reuseSeal, 1, 1, "pending", nil, nil, nil, nil)
	appendQualificationReceiptV3(t, ctx, database, reusePending)
	reusedClaim, reusedAcknowledgement := qualificationReceiptV3RecoveryMaterials(
		t, reuseSeal, reusePending, claimID, acknowledgementID,
	)
	if _, err := appendQualificationReceiptV3Raw(ctx, database, qualificationReceiptV3Observation(
		t, reuseSeal, 2, 1, "not-invoked", nil, nil, &reusedClaim, &reusedAcknowledgement,
	)); err == nil {
		t.Fatal("globally reserved claim/ACK identities were reused by another request")
	}

	driftFixture := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{})
	if err := freezeQualificationPlanMigrationFixture(ctx, database, driftFixture); err != nil {
		t.Fatalf("freeze Evidence-drift Plan Authority: %v", err)
	}
	driftIndexed := seedQualificationReceiptV3IndexedEvidence(t, ctx, database, driftFixture)
	driftSeal := qualificationReceiptV3Request(t, driftFixture, driftIndexed,
		"snapshot-seal", "sealer", "", "", "", nil, "", nil)
	if created, err := startQualificationReceiptV3(
		ctx, database, driftSeal, qualificationPlanMigrationMaterial{}, nil, nil,
	); err != nil || !created {
		t.Fatalf("start Evidence-drift request created=%t error=%v", created, err)
	}
	appendQualificationReceiptV3(t, ctx, database,
		qualificationReceiptV3Observation(t, driftSeal, 1, 1, "pending", nil, nil, nil, nil))
	driftQualificationReceiptV3EvidenceHead(t, ctx, database, driftFixture, driftIndexed)
	driftResult := qualificationReceiptV3Canonical(t, map[string]any{
		"artifactIndexDigest":   driftIndexed.indexDigest,
		"authorityId":           driftSeal.documentValue["operationalAuthorityId"],
		"evidenceClosureDigest": driftIndexed.closureDigest, "mode": "immutable-filesystem",
		"operationId": driftSeal.documentValue["operationId"], "requestDigest": driftSeal.material.hash,
		"schemaVersion":  "worksflow-qualification-pre-receipt-snapshot/v3",
		"sealedAt":       qualificationReceiptV3Time(time.Now()),
		"snapshotDigest": qualificationPlanMigrationDigest("drift-snapshot"),
		"snapshotId":     driftSeal.documentValue["snapshotId"], "stage": "committed",
	})
	if _, err := appendQualificationReceiptV3Raw(ctx, database, qualificationReceiptV3Observation(
		t, driftSeal, 2, 1, "committed", &driftResult, nil, nil, nil,
	)); err == nil || !strings.Contains(err.Error(), "Evidence drifted") {
		t.Fatalf("Evidence head drift append error = %v, want fail-closed conflict", err)
	}

	completionDriftFixture := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{})
	if err := freezeQualificationPlanMigrationFixture(ctx, database, completionDriftFixture); err != nil {
		t.Fatalf("freeze completion-drift Plan Authority: %v", err)
	}
	completionDriftIndexed := seedQualificationReceiptV3IndexedEvidence(
		t, ctx, database, completionDriftFixture,
	)
	completionDriftSeal := qualificationReceiptV3Request(
		t, completionDriftFixture, completionDriftIndexed,
		"snapshot-seal", "sealer", "", "", "", nil, "", nil,
	)
	if created, err := startQualificationReceiptV3(
		ctx, database, completionDriftSeal, qualificationPlanMigrationMaterial{}, nil, nil,
	); err != nil || !created {
		t.Fatalf("start completion-drift seal created=%t error=%v", created, err)
	}
	appendQualificationReceiptV3(t, ctx, database, qualificationReceiptV3Observation(
		t, completionDriftSeal, 1, 1, "pending", nil, nil, nil, nil,
	))
	completionDriftSnapshotDigest := qualificationPlanMigrationDigest("completion-drift-snapshot")
	completionDriftSealResult := qualificationReceiptV3Canonical(t, map[string]any{
		"artifactIndexDigest":   completionDriftIndexed.indexDigest,
		"authorityId":           completionDriftSeal.documentValue["operationalAuthorityId"],
		"evidenceClosureDigest": completionDriftIndexed.closureDigest,
		"mode":                  "immutable-filesystem",
		"operationId":           completionDriftSeal.documentValue["operationId"],
		"requestDigest":         completionDriftSeal.material.hash,
		"schemaVersion":         "worksflow-qualification-pre-receipt-snapshot/v3",
		"sealedAt":              qualificationReceiptV3Time(time.Now()),
		"snapshotDigest":        completionDriftSnapshotDigest,
		"snapshotId":            completionDriftSeal.documentValue["snapshotId"],
		"stage":                 "committed",
	})
	completionDriftSealRecord := appendQualificationReceiptV3(t, ctx, database,
		qualificationReceiptV3Observation(
			t, completionDriftSeal, 2, 1, "committed", &completionDriftSealResult, nil, nil, nil,
		))
	completionDriftVerification := qualificationReceiptV3Request(
		t, completionDriftFixture, completionDriftIndexed,
		"snapshot-verify", "verifier", completionDriftSnapshotDigest, "", "", nil, "", nil,
	)
	if created, err := startQualificationReceiptV3(
		ctx, database, completionDriftVerification, qualificationPlanMigrationMaterial{}, nil, nil,
	); err != nil || !created {
		t.Fatalf("start completion-drift verification created=%t error=%v", created, err)
	}
	appendQualificationReceiptV3(t, ctx, database, qualificationReceiptV3Observation(
		t, completionDriftVerification, 1, 1, "pending", nil, nil, nil, nil,
	))
	completionDriftVerificationResult := qualificationReceiptV3Canonical(t, map[string]any{
		"artifactIndexDigest":   completionDriftIndexed.indexDigest,
		"authorityId":           completionDriftVerification.documentValue["operationalAuthorityId"],
		"evidenceClosureDigest": completionDriftIndexed.closureDigest,
		"result":                "verified",
		"schemaVersion":         "worksflow-qualification-snapshot-verification/v3",
		"snapshotDigest":        completionDriftSnapshotDigest,
		"snapshotId":            completionDriftVerification.documentValue["snapshotId"],
		"verifiedAt":            qualificationReceiptV3Time(time.Now()),
	})
	completionDriftVerificationRecord := appendQualificationReceiptV3(t, ctx, database,
		qualificationReceiptV3Observation(
			t, completionDriftVerification, 2, 1, "committed",
			&completionDriftVerificationResult, nil, nil, nil,
		))
	completionDriftPayload, completionDriftPAE := qualificationReceiptV3Payload(
		t, completionDriftFixture, completionDriftIndexed,
		completionDriftSealResult, completionDriftVerificationResult,
	)
	completionDriftRunner := qualificationReceiptV3Request(
		t, completionDriftFixture, completionDriftIndexed, "receipt-sign", "qualification-runner",
		completionDriftSnapshotDigest, "qualification-receipt", "runner-key",
		completionDriftPayload.bytes, completionDriftPAE.hash, completionDriftPAE.bytes,
	)
	completionDriftApprover := qualificationReceiptV3Request(
		t, completionDriftFixture, completionDriftIndexed, "receipt-sign", "release-approver",
		completionDriftSnapshotDigest, "qualification-receipt", "approver-key",
		completionDriftPayload.bytes, completionDriftPAE.hash, completionDriftPAE.bytes,
	)
	if created, err := startQualificationReceiptV3(
		ctx, database, completionDriftRunner, completionDriftApprover.material,
		completionDriftPayload.bytes, completionDriftPAE.bytes,
	); err != nil || !created {
		t.Fatalf("start completion-drift signing created=%t error=%v", created, err)
	}
	completionDriftRunnerRecord := qualificationReceiptV3CommitSigner(
		t, ctx, database, completionDriftRunner, bytes.Repeat([]byte{0x63}, 64),
	)
	completionDriftApproverRecord := qualificationReceiptV3CommitSigner(
		t, ctx, database, completionDriftApprover, bytes.Repeat([]byte{0x74}, 64),
	)
	completionDriftEnvelope := qualificationReceiptV3Envelope(
		t, completionDriftPayload.bytes, completionDriftRunner, completionDriftRunnerRecord,
		completionDriftApprover, completionDriftApproverRecord,
	)
	driftQualificationReceiptV3EvidenceHead(
		t, ctx, database, completionDriftFixture, completionDriftIndexed,
	)
	if _, err := completeQualificationReceiptV3Raw(
		ctx, database, completionDriftFixture,
		completionDriftSeal, completionDriftSealRecord,
		completionDriftVerification, completionDriftVerificationRecord,
		completionDriftRunner, completionDriftRunnerRecord,
		completionDriftApprover, completionDriftApproverRecord,
		completionDriftPayload, completionDriftPAE, completionDriftEnvelope,
	); err == nil || !strings.Contains(err.Error(), "current indexed Evidence drifted") {
		t.Fatalf("completion-time Evidence drift error = %v, want fail-closed conflict", err)
	}

	late := qualificationReceiptV3Observation(t, runner, 3, 2, "pending", nil, nil, nil, nil)
	if _, err := appendQualificationReceiptV3Raw(ctx, database, late); err == nil ||
		!strings.Contains(err.Error(), "retry ownership") {
		t.Fatalf("post-terminal signer observation error = %v, want terminal fence", err)
	}

	if _, err := database.ExecContext(ctx,
		`UPDATE qualification_receipt_v3_requests SET signer_role = signer_role`); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("request mutation error = %v, want immutable rejection", err)
	}
	if _, err := database.ExecContext(ctx,
		`INSERT INTO qualification_promotion_consumptions(operation_id) VALUES ($1)`, uuid.New()); err == nil ||
		!strings.Contains(err.Error(), "history-only") {
		t.Fatalf("promotion v1 guard error = %v, want history-only rejection", err)
	}
	down, err := files.ReadFile("000075_qualification_receipt_v3_store.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err == nil ||
		!strings.Contains(err.Error(), "immutable control state is nonempty") {
		t.Fatalf("nonempty Receipt v3 rollback error = %v, want refusal", err)
	}
}

func TestQualificationReceiptV3StoreMigrationRejectsPlanLinkedV1TailUpgrade(t *testing.T) {
	ctx, base, dsn := qualificationReceiptV3Postgres(t)
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_receipt_v3_upgrade_")
	applyQualificationReceiptV3Migrations(t, ctx, database, false)
	fixture := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{})
	if err := freezeQualificationPlanMigrationFixture(ctx, database, fixture); err != nil {
		t.Fatal(err)
	}
	if err := insertQualificationReceiptV3LegacyTail(ctx, database, fixture, "snapshot-sealed"); err != nil {
		t.Fatalf("seed Plan-linked v1 Receipt tail: %v", err)
	}
	up, err := files.ReadFile("000075_qualification_receipt_v3_store.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err == nil ||
		!strings.Contains(err.Error(), "cannot activate Qualification Receipt v3") {
		t.Fatalf("Plan-linked v1 tail activation error = %v, want fail-closed refusal", err)
	}
	var activationRolledBack bool
	if err := database.QueryRowContext(ctx, `
SELECT pg_catalog.to_regclass(
  pg_catalog.current_schema() || '.qualification_receipt_v3_requests'
) IS NULL
`).Scan(&activationRolledBack); err != nil {
		t.Fatal(err)
	}
	if !activationRolledBack {
		t.Fatal("failed Receipt v3 activation left partially installed control tables")
	}
}

func TestQualificationReceiptV3RollbackWriterFencingPostgres(t *testing.T) {
	ctx, base, dsn := qualificationReceiptV3Postgres(t)
	down, err := files.ReadFile("000075_qualification_receipt_v3_store.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("writer commits before waiting rollback observes nonempty state", func(t *testing.T) {
		database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_receipt_v3_writer_first_")
		applyQualificationReceiptV3Migrations(t, ctx, database, true)
		fixture := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{})
		if err := freezeQualificationPlanMigrationFixture(ctx, database, fixture); err != nil {
			t.Fatal(err)
		}
		indexed := seedQualificationReceiptV3IndexedEvidence(t, ctx, database, fixture)
		request := qualificationReceiptV3Request(t, fixture, indexed,
			"snapshot-seal", "sealer", "", "", "", nil, "", nil)

		requestGate, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer requestGate.Rollback()
		if _, err := requestGate.ExecContext(ctx,
			`LOCK TABLE qualification_receipt_v3_requests IN ACCESS EXCLUSIVE MODE`); err != nil {
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
		writerLocksFinished := make(chan error, 1)
		go func() {
			_, lockErr := writerTransaction.ExecContext(ctx, `
LOCK TABLE qualification_evidence_events IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_evidence_operations IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_evidence_heads IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_plan_authorities IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_plan_identity_reservations IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_receipt_v3_requests IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_receipt_v3_observations IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE qualification_receipt_v3_receipts IN SHARE ROW EXCLUSIVE MODE;
`)
			writerLocksFinished <- lockErr
		}()
		lockCtx, lockCancel := context.WithTimeout(ctx, 5*time.Second)
		defer lockCancel()
		for _, relation := range []string{
			"qualification_evidence_events", "qualification_evidence_operations",
			"qualification_evidence_heads", "qualification_plan_authorities",
			"qualification_plan_identity_reservations",
		} {
			if err := waitForQualificationReceiptV3Lock(
				lockCtx, database, writerPID, relation, "ShareRowExclusiveLock", true, writerLocksFinished,
			); err != nil {
				t.Fatal(err)
			}
		}
		if err := waitForQualificationReceiptV3Lock(
			lockCtx, database, writerPID, "qualification_receipt_v3_requests",
			"ShareRowExclusiveLock", false, writerLocksFinished,
		); err != nil {
			t.Fatal(err)
		}

		downConnection, err := database.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer downConnection.Close()
		var downPID int
		if err := downConnection.QueryRowContext(ctx, `SELECT pg_catalog.pg_backend_pid()`).Scan(&downPID); err != nil {
			t.Fatal(err)
		}
		downFinished := make(chan error, 1)
		go func() {
			_, downErr := downConnection.ExecContext(ctx, string(down))
			downFinished <- downErr
		}()
		if err := waitForQualificationReceiptV3Lock(
			lockCtx, database, downPID, "qualification_evidence_events",
			"AccessExclusiveLock", false, downFinished,
		); err != nil {
			t.Fatal(err)
		}
		if err := requestGate.Rollback(); err != nil {
			t.Fatal(err)
		}
		select {
		case lockErr := <-writerLocksFinished:
			if lockErr != nil {
				t.Fatalf("writer ordered lock batch: %v", lockErr)
			}
		case <-ctx.Done():
			t.Fatalf("writer locks did not finish: %v", ctx.Err())
		}
		created, err := startQualificationReceiptV3(
			ctx, writerTransaction, request, qualificationPlanMigrationMaterial{}, nil, nil,
		)
		if err != nil || !created {
			t.Fatalf("writer start after ordered locks created=%t error=%v", created, err)
		}
		if err := writerTransaction.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case downErr := <-downFinished:
			if downErr == nil || !strings.Contains(downErr.Error(), "immutable control state is nonempty") {
				t.Fatalf("rollback after writer commit error = %v, want nonempty refusal", downErr)
			}
		case <-ctx.Done():
			t.Fatalf("rollback did not finish after writer commit: %v", ctx.Err())
		}
		var rows int
		if err := database.QueryRowContext(ctx,
			`SELECT count(*) FROM qualification_receipt_v3_requests`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Fatalf("failed rollback retained request rows=%d, want 1", rows)
		}
	})

	t.Run("staged empty rollback blocks writer and abort releases it", func(t *testing.T) {
		database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_receipt_v3_down_first_")
		applyQualificationReceiptV3Migrations(t, ctx, database, true)
		fixture := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{})
		if err := freezeQualificationPlanMigrationFixture(ctx, database, fixture); err != nil {
			t.Fatal(err)
		}
		indexed := seedQualificationReceiptV3IndexedEvidence(t, ctx, database, fixture)
		request := qualificationReceiptV3Request(t, fixture, indexed,
			"snapshot-seal", "sealer", "", "", "", nil, "", nil)

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
			t.Fatalf("stage empty Receipt v3 rollback: %v", err)
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
		type stagedWriterResult struct {
			created bool
			err     error
		}
		writerFinished := make(chan stagedWriterResult, 1)
		go func() {
			created, writerErr := startQualificationReceiptV3(
				ctx, writerConnection, request, qualificationPlanMigrationMaterial{}, nil, nil,
			)
			writerFinished <- stagedWriterResult{created: created, err: writerErr}
		}()
		lockCtx, lockCancel := context.WithTimeout(ctx, 5*time.Second)
		defer lockCancel()
		writerErrors := make(chan error, 1)
		go func() {
			result := <-writerFinished
			writerErrors <- result.err
			writerFinished <- result
		}()
		if err := waitForQualificationReceiptV3Lock(
			lockCtx, database, writerPID, "qualification_receipt_v3_requests",
			"AccessShareLock", false, writerErrors,
		); err != nil {
			t.Fatal(err)
		}
		if err := downTransaction.Rollback(); err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-writerFinished:
			if result.err != nil || !result.created {
				t.Fatalf("writer after staged rollback created=%t error=%v", result.created, result.err)
			}
		case <-ctx.Done():
			t.Fatalf("writer did not finish after staged rollback abort: %v", ctx.Err())
		}
	})

	t.Run("empty rollback removes only Receipt v3 control objects", func(t *testing.T) {
		database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_receipt_v3_empty_down_")
		applyQualificationReceiptV3Migrations(t, ctx, database, true)
		if _, err := database.ExecContext(ctx, string(down)); err != nil {
			t.Fatalf("empty Receipt v3 rollback: %v", err)
		}
		var removed bool
		if err := database.QueryRowContext(ctx, `
SELECT pg_catalog.to_regclass(
  pg_catalog.current_schema() || '.qualification_receipt_v3_requests'
) IS NULL
AND pg_catalog.to_regclass(
  pg_catalog.current_schema() || '.qualification_receipt_v3_observations'
) IS NULL
AND pg_catalog.to_regclass(
  pg_catalog.current_schema() || '.qualification_receipt_v3_receipts'
) IS NULL
AND pg_catalog.to_regclass(
  pg_catalog.current_schema() || '.qualification_evidence_events'
) IS NOT NULL
`).Scan(&removed); err != nil {
			t.Fatal(err)
		}
		if !removed {
			t.Fatal("empty rollback did not remove exactly the Receipt v3 control objects")
		}
	})
}

type qualificationReceiptV3Indexed struct {
	eventID       uuid.UUID
	eventHash     string
	indexDigest   string
	closureDigest string
}

type qualificationReceiptV3RequestFixture struct {
	material      qualificationPlanMigrationMaterial
	documentValue map[string]any
	payload       []byte
	pae           []byte
}

type qualificationReceiptV3ObservationFixture struct {
	request       qualificationReceiptV3RequestFixture
	authPayload   qualificationPlanMigrationMaterial
	authProof     qualificationPlanMigrationMaterial
	result        *qualificationPlanMigrationMaterial
	signatureHash any
	signature     any
	claim         *qualificationPlanMigrationMaterial
	ack           *qualificationPlanMigrationMaterial
}

type qualificationReceiptV3Record struct {
	hash       string
	recordedAt time.Time
	signature  []byte
	idempotent bool
}

type qualificationReceiptV3Completion struct {
	receiptID      string
	completionHash string
	completedAt    time.Time
	idempotent     bool
}

type qualificationReceiptV3Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func applyQualificationReceiptV3Migrations(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	includeReceiptV3 bool,
) {
	t.Helper()
	names := []string{
		"000071_qualification_promotion_consume.up.sql",
		"000073_qualification_evidence_event_store.up.sql",
		"000074_qualification_plan_authority.up.sql",
	}
	if includeReceiptV3 {
		names = append(names, "000075_qualification_receipt_v3_store.up.sql")
	}
	for _, name := range names {
		migration, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
}

func insertQualificationReceiptV3LegacyTail(
	ctx context.Context,
	database *sql.DB,
	fixture qualificationPlanMigrationFixture,
	kind string,
) error {
	operations, ok := fixture.evidenceDocument["operations"].(map[string]any)
	if !ok {
		return fmt.Errorf("fixture operations are absent")
	}
	operationID, err := uuid.Parse(operations["snapshotSeal"].(string))
	if err != nil {
		return err
	}
	empty := qualificationPlanMigrationCanonicalForHelper(map[string]any{})
	_, err = database.ExecContext(ctx, `
INSERT INTO qualification_evidence_events (
  event_id, orchestration_id, version, expected_version, event_kind, operation_id,
  active_artifact_id, event_at, requested_at,
  request_hash, request_bytes, request_document, event_hash, event_bytes, event_document
) VALUES (
  $1,$2,1,0,$3,$4,'',date_trunc('milliseconds',clock_timestamp()),
  date_trunc('milliseconds',clock_timestamp()),$5,$6,$7::jsonb,$5,$6,$7::jsonb
)
`, uuid.New(), fixture.orchestrationID, kind, operationID,
		empty.hash, empty.bytes, empty.document)
	return err
}

func seedQualificationReceiptV3IndexedEvidence(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture qualificationPlanMigrationFixture,
) qualificationReceiptV3Indexed {
	t.Helper()
	indexed := qualificationReceiptV3Indexed{
		eventID:       uuid.New(),
		indexDigest:   qualificationPlanMigrationDigest("receipt-v3-artifact-index"),
		closureDigest: qualificationPlanMigrationDigest("receipt-v3-evidence-closure"),
	}
	event := qualificationReceiptV3Canonical(t, map[string]any{
		"artifactIndex": map[string]any{
			"contentDigest": indexed.indexDigest, "evidenceClosureDigest": indexed.closureDigest,
			"stage": "committed",
		},
	})
	indexed.eventHash = event.hash
	empty := qualificationReceiptV3Canonical(t, map[string]any{})
	operations := qualificationReceiptV3Map(t, fixture.evidenceDocument, "operations")
	operationID, err := uuid.Parse(operations["artifactIndex"].(string))
	if err != nil {
		t.Fatal(err)
	}

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	var eventAt time.Time
	if err := transaction.QueryRowContext(ctx, `
INSERT INTO qualification_evidence_events (
  event_id, orchestration_id, version, expected_version, event_kind, operation_id,
  active_artifact_id, event_at, requested_at,
  request_hash, request_bytes, request_document, event_hash, event_bytes, event_document
) VALUES (
  $1,$2,1,0,'artifact-indexed',$3,'',
  date_trunc('milliseconds',clock_timestamp()),date_trunc('milliseconds',clock_timestamp()),
  $4,$5,$6::jsonb,$7,$8,$9::jsonb
) RETURNING event_at
`, indexed.eventID, fixture.orchestrationID, operationID,
		empty.hash, empty.bytes, empty.document, event.hash, event.bytes, event.document).Scan(&eventAt); err != nil {
		t.Fatalf("seed indexed Evidence event: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO qualification_evidence_projection_authorizations(transaction_id,backend_pid)
VALUES (txid_current(),pg_backend_pid())
`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO qualification_evidence_heads (
  orchestration_id, version, phase, last_event_id, last_event_at, command_hash,
  trust_bindings_digest, active_operation_id, active_artifact_id, plan_document
) VALUES ($1,1,'artifact-indexed',$2,$3,$4,$5,NULL,'',$6::jsonb)
`, fixture.orchestrationID, indexed.eventID, eventAt, fixture.evidencePlan.hash,
		fixture.trustBindingsDigest, fixture.evidencePlan.document); err != nil {
		t.Fatalf("seed indexed Evidence head: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
DELETE FROM qualification_evidence_projection_authorizations
WHERE transaction_id=txid_current() AND backend_pid=pg_backend_pid()
`); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	return indexed
}

func qualificationReceiptV3Request(
	t *testing.T,
	fixture qualificationPlanMigrationFixture,
	indexed qualificationReceiptV3Indexed,
	kind string,
	role string,
	snapshotDigest string,
	receiptID string,
	signerKeyID string,
	payload []byte,
	paeHash string,
	pae []byte,
) qualificationReceiptV3RequestFixture {
	t.Helper()
	operations := qualificationReceiptV3Map(t, fixture.evidenceDocument, "operations")
	outputs := qualificationReceiptV3Map(t, fixture.evidenceDocument, "outputs")
	trust := qualificationReceiptV3DecodeMap(t, fixture.trust.document)
	bindings := qualificationReceiptV3Map(t, trust, "trustBindings")
	operationID := operations["snapshotSeal"].(string)
	operationalAuthorityID := bindings["sealerAuthorityId"].(string)
	authenticationKeyID := "sealer-auth-key"
	if kind == "snapshot-verify" {
		operationalAuthorityID = bindings["verifierAuthorityId"].(string)
		authenticationKeyID = "verifier-auth-key"
	}
	signerIdentity := ""
	if kind == "receipt-sign" {
		operationID = operations["receiptSign"].(string)
		operationalAuthorityID = bindings["receiptAuthorityId"].(string)
		authenticationKeyID = signerKeyID
		if role == "qualification-runner" {
			signerIdentity = "spiffe://qualification.example/runner"
		} else {
			signerIdentity = "spiffe://qualification.example/approver"
		}
	}
	payloadHash := ""
	if len(payload) > 0 {
		payloadHash = qualificationPlanMigrationDigestBytes(payload)
	}
	document := map[string]any{
		"artifactIndexDigest":    indexed.indexDigest,
		"authenticationKeyId":    authenticationKeyID,
		"evidenceClosureDigest":  indexed.closureDigest,
		"evidenceCommandDigest":  fixture.evidencePlan.hash,
		"evidenceHeadVersion":    1,
		"evidenceLastEventHash":  indexed.eventHash,
		"evidenceLastEventId":    indexed.eventID.String(),
		"evidencePlanHash":       fixture.evidencePlan.hash,
		"evidenceTrustDigest":    fixture.trustBindingsDigest,
		"inputHash":              fixture.input.hash,
		"kind":                   kind,
		"operationId":            operationID,
		"orchestrationId":        fixture.orchestrationID.String(),
		"operationalAuthorityId": operationalAuthorityID,
		"paeDigest":              paeHash,
		"payloadDigest":          payloadHash,
		"planAuthorityHash":      fixture.envelope.hash,
		"planAuthorityId":        fixture.authorityID.String(),
		"projectionHash":         fixture.projection.hash,
		"receiptId":              receiptID,
		"role":                   role,
		"schemaVersion":          "worksflow-qualification-receipt-control-request/v1",
		"signerIdentity":         signerIdentity,
		"signerKeyId":            signerKeyID,
		"snapshotDigest":         snapshotDigest,
		"snapshotId":             outputs["snapshotId"].(string),
		"targetHash":             fixture.target.hash,
		"trustBindingsDigest":    fixture.trustBindingsDigest,
		"trustHash":              fixture.trust.hash,
		"trustPolicyDigest":      trust["trustPolicyDigest"].(string),
	}
	return qualificationReceiptV3RequestFixture{
		material: qualificationReceiptV3Canonical(t, document), documentValue: document,
		payload: bytes.Clone(payload), pae: bytes.Clone(pae),
	}
}

func startQualificationReceiptV3(
	ctx context.Context,
	database qualificationReceiptV3Queryer,
	primary qualificationReceiptV3RequestFixture,
	secondary qualificationPlanMigrationMaterial,
	payload []byte,
	pae []byte,
) (bool, error) {
	var secondaryHash any
	var secondaryBytes any
	var secondaryDocument any
	if secondary.hash != "" {
		secondaryHash, secondaryBytes, secondaryDocument = secondary.hash, secondary.bytes, secondary.document
	}
	var payloadHash any
	var payloadBytes any
	var paeHash any
	var paeBytes any
	if len(payload) > 0 {
		payloadHash, payloadBytes = qualificationPlanMigrationDigestBytes(payload), payload
	}
	if len(pae) > 0 {
		paeHash, paeBytes = qualificationPlanMigrationDigestBytes(pae), pae
	}
	rows, err := database.QueryContext(ctx, `
SELECT (started.request_record).request_hash,
       (started.request_record).started_at,
       started.created
FROM start_qualification_receipt_v3_requests(
  $1,$2,$3::jsonb,$4,$5,$6::jsonb,$7,$8,$9,$10
) AS started
`, primary.material.hash, primary.material.bytes, primary.material.document,
		secondaryHash, secondaryBytes, secondaryDocument, payloadHash, payloadBytes, paeHash, paeBytes)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	count := 0
	created := false
	for rows.Next() {
		var requestHash string
		var startedAt time.Time
		var rowCreated bool
		if err := rows.Scan(&requestHash, &startedAt, &rowCreated); err != nil {
			return false, err
		}
		if requestHash == "" || startedAt.IsZero() || (count > 0 && rowCreated != created) {
			return false, fmt.Errorf("Receipt v3 start returned incomplete or inconsistent persisted rows")
		}
		created = rowCreated
		count++
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	want := 1
	if secondary.hash != "" {
		want = 2
	}
	if count != want {
		return false, fmt.Errorf("Receipt v3 start returned %d rows, want %d", count, want)
	}
	return created, nil
}

func qualificationReceiptV3Observation(
	t *testing.T,
	request qualificationReceiptV3RequestFixture,
	sequence int64,
	generation int64,
	status string,
	result *qualificationPlanMigrationMaterial,
	signature []byte,
	claim *qualificationPlanMigrationMaterial,
	ack *qualificationPlanMigrationMaterial,
) qualificationReceiptV3ObservationFixture {
	t.Helper()
	resultHash := ""
	if result != nil {
		resultHash = result.hash
	}
	signatureHash := ""
	if len(signature) > 0 {
		signatureHash = qualificationPlanMigrationDigestBytes(signature)
	}
	claimHash := ""
	if claim != nil {
		claimHash = claim.hash
	}
	ackHash := ""
	if ack != nil {
		ackHash = ack.hash
	}
	observedAt := qualificationReceiptV3Time(time.Now())
	payloadDocument := map[string]any{
		"acknowledgementTokenHash": ackHash,
		"authenticationKeyId":      request.documentValue["authenticationKeyId"],
		"generation":               generation,
		"kind":                     request.documentValue["kind"],
		"observedAt":               observedAt,
		"operationId":              request.documentValue["operationId"],
		"operationalAuthorityId":   request.documentValue["operationalAuthorityId"],
		"planAuthorityId":          request.documentValue["planAuthorityId"],
		"requestHash":              request.material.hash,
		"resultHash":               resultHash,
		"role":                     request.documentValue["role"],
		"schemaVersion":            "worksflow-qualification-receipt-control-observation-payload/v1",
		"sequence":                 sequence,
		"signatureHash":            signatureHash,
		"status":                   status,
		"claimTokenHash":           claimHash,
	}
	authPayload := qualificationReceiptV3Canonical(t, payloadDocument)
	proofDocument := map[string]any{
		"algorithm":              "ed25519",
		"keyId":                  request.documentValue["authenticationKeyId"],
		"operationalAuthorityId": request.documentValue["operationalAuthorityId"],
		"payload":                base64.StdEncoding.EncodeToString(authPayload.bytes),
		"payloadType":            "application/vnd.worksflow.qualification-receipt-control-observation+json",
		"schemaVersion":          "worksflow-qualification-receipt-control-observation-proof/v1",
		"signature":              base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{byte(sequence + generation)}, 64)),
	}
	var signatureHashValue any
	var signatureValue any
	if len(signature) > 0 {
		signatureHashValue, signatureValue = signatureHash, bytes.Clone(signature)
	}
	return qualificationReceiptV3ObservationFixture{
		request: request, authPayload: authPayload,
		authProof: qualificationReceiptV3Canonical(t, proofDocument), result: result,
		signatureHash: signatureHashValue, signature: signatureValue, claim: claim, ack: ack,
	}
}

func appendQualificationReceiptV3(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	observation qualificationReceiptV3ObservationFixture,
) qualificationReceiptV3Record {
	t.Helper()
	record, err := appendQualificationReceiptV3Raw(ctx, database, observation)
	if err != nil {
		t.Fatalf("append Receipt v3 observation: %v", err)
	}
	return record
}

func appendQualificationReceiptV3Raw(
	ctx context.Context,
	database *sql.DB,
	observation qualificationReceiptV3ObservationFixture,
) (qualificationReceiptV3Record, error) {
	materialArguments := func(material *qualificationPlanMigrationMaterial) (any, any, any) {
		if material == nil {
			return nil, nil, nil
		}
		return material.hash, material.bytes, material.document
	}
	resultHash, resultBytes, resultDocument := materialArguments(observation.result)
	claimHash, claimBytes, claimDocument := materialArguments(observation.claim)
	ackHash, ackBytes, ackDocument := materialArguments(observation.ack)
	var storedBytes []byte
	record := qualificationReceiptV3Record{}
	err := database.QueryRowContext(ctx, `
SELECT (appended.observation_record).record_hash,
       (appended.observation_record).observation_bytes,
       (appended.observation_record).recorded_at,
       (appended.observation_record).signature_bytes,
       appended.idempotent
FROM append_qualification_receipt_v3_observation(
  $1,$2,$3,$4::jsonb,$5,$6,$7::jsonb,$8,$9,$10::jsonb,
  $11,$12,$13,$14,$15::jsonb,$16,$17,$18::jsonb
) AS appended
`, observation.request.material.hash,
		observation.authPayload.hash, observation.authPayload.bytes, observation.authPayload.document,
		observation.authProof.hash, observation.authProof.bytes, observation.authProof.document,
		resultHash, resultBytes, resultDocument, observation.signatureHash, observation.signature,
		claimHash, claimBytes, claimDocument, ackHash, ackBytes, ackDocument,
	).Scan(&record.hash, &storedBytes, &record.recordedAt, &record.signature, &record.idempotent)
	if err != nil {
		return qualificationReceiptV3Record{}, err
	}
	payload := qualificationReceiptV3DecodeMapForHelper(observation.authPayload.document)
	want := qualificationPlanMigrationCanonicalForHelper(map[string]any{
		"acknowledgementTokenHash":   stringValue(ackHash),
		"authenticationEnvelopeHash": observation.authProof.hash,
		"authenticationKeyId":        observation.request.documentValue["authenticationKeyId"],
		"authenticationPayloadHash":  observation.authPayload.hash,
		"claimTokenHash":             stringValue(claimHash),
		"generation":                 payload["generation"],
		"observedAt":                 payload["observedAt"],
		"recordedAt":                 qualificationReceiptV3Time(record.recordedAt),
		"requestHash":                observation.request.material.hash,
		"resultHash":                 stringValue(resultHash),
		"sequence":                   payload["sequence"],
		"signatureHash":              stringValue(observation.signatureHash),
		"status":                     payload["status"],
	})
	if record.hash != want.hash || !bytes.Equal(storedBytes, want.bytes) {
		return qualificationReceiptV3Record{}, fmt.Errorf(
			"Receipt v3 Store record projection drift hash=%s want=%s", record.hash, want.hash,
		)
	}
	return record, nil
}

func qualificationReceiptV3CommitSigner(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	request qualificationReceiptV3RequestFixture,
	signature []byte,
) qualificationReceiptV3Record {
	t.Helper()
	appendQualificationReceiptV3(t, ctx, database,
		qualificationReceiptV3Observation(t, request, 1, 1, "pending", nil, nil, nil, nil))
	return appendQualificationReceiptV3(t, ctx, database,
		qualificationReceiptV3Observation(t, request, 2, 1, "committed", nil, signature, nil, nil))
}

func qualificationReceiptV3RecoveryMaterials(
	t *testing.T,
	request qualificationReceiptV3RequestFixture,
	pending qualificationReceiptV3ObservationFixture,
	claimID uuid.UUID,
	acknowledgementID uuid.UUID,
) (qualificationPlanMigrationMaterial, qualificationPlanMigrationMaterial) {
	t.Helper()
	pendingPayload := qualificationReceiptV3DecodeMap(t, pending.authPayload.document)
	claim := qualificationReceiptV3Canonical(t, map[string]any{
		"claimId":                claimID.String(),
		"generation":             pendingPayload["generation"],
		"kind":                   request.documentValue["kind"],
		"operationId":            request.documentValue["operationId"],
		"operationalAuthorityId": request.documentValue["operationalAuthorityId"],
		"pendingEnvelopeHash":    pending.authProof.hash,
		"planAuthorityId":        request.documentValue["planAuthorityId"],
		"requestHash":            request.material.hash,
		"role":                   request.documentValue["role"],
		"schemaVersion":          "worksflow-qualification-receipt-control-claim/v1",
	})
	acknowledgement := qualificationReceiptV3Canonical(t, map[string]any{
		"acknowledgementId": acknowledgementID.String(),
		"claimTokenHash":    claim.hash,
		"requestHash":       request.material.hash,
		"schemaVersion":     "worksflow-qualification-receipt-control-acknowledgement/v1",
		"status":            "not-invoked",
	})
	return claim, acknowledgement
}

func driftQualificationReceiptV3EvidenceHead(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture qualificationPlanMigrationFixture,
	previous qualificationReceiptV3Indexed,
) {
	t.Helper()
	operations := qualificationReceiptV3Map(t, fixture.evidenceDocument, "operations")
	operationID, err := uuid.Parse(operations["artifactIndex"].(string))
	if err != nil {
		t.Fatal(err)
	}
	eventID := uuid.New()
	event := qualificationReceiptV3Canonical(t, map[string]any{
		"artifactIndex": map[string]any{
			"contentDigest":         qualificationPlanMigrationDigest("drifted-artifact-index"),
			"evidenceClosureDigest": qualificationPlanMigrationDigest("drifted-evidence-closure"),
			"stage":                 "committed",
		},
	})
	empty := qualificationReceiptV3Canonical(t, map[string]any{})
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	var eventAt time.Time
	if err := transaction.QueryRowContext(ctx, `
INSERT INTO qualification_evidence_events (
  event_id, orchestration_id, version, expected_version, event_kind, operation_id,
  active_artifact_id, event_at, requested_at,
  request_hash, request_bytes, request_document, event_hash, event_bytes, event_document
) VALUES (
  $1,$2,2,1,'artifact-indexed',$3,'',
  date_trunc('milliseconds',clock_timestamp()),date_trunc('milliseconds',clock_timestamp()),
  $4,$5,$6::jsonb,$7,$8,$9::jsonb
) RETURNING event_at
`, eventID, fixture.orchestrationID, operationID, empty.hash, empty.bytes, empty.document,
		event.hash, event.bytes, event.document).Scan(&eventAt); err != nil {
		t.Fatalf("append drifted Evidence event after %s: %v", previous.eventID, err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO qualification_evidence_projection_authorizations(transaction_id,backend_pid)
VALUES (txid_current(),pg_backend_pid())
`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE qualification_evidence_heads
SET version=2, last_event_id=$2, last_event_at=$3
WHERE orchestration_id=$1
`, fixture.orchestrationID, eventID, eventAt); err != nil {
		t.Fatalf("move Evidence head for drift canary: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
DELETE FROM qualification_evidence_projection_authorizations
WHERE transaction_id=txid_current() AND backend_pid=pg_backend_pid()
`); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func qualificationReceiptV3Payload(
	t *testing.T,
	fixture qualificationPlanMigrationFixture,
	indexed qualificationReceiptV3Indexed,
	sealResult qualificationPlanMigrationMaterial,
	verificationResult qualificationPlanMigrationMaterial,
) (qualificationPlanMigrationMaterial, qualificationPlanMigrationMaterial) {
	t.Helper()
	target := qualificationReceiptV3DecodeMap(t, fixture.target.document)
	trust := qualificationReceiptV3DecodeMap(t, fixture.trust.document)
	input := qualificationReceiptV3DecodeMap(t, fixture.input.document)
	credential := qualificationReceiptV3Map(t, input, "credential")
	outputs := qualificationReceiptV3Map(t, fixture.evidenceDocument, "outputs")
	operations := qualificationReceiptV3Map(t, fixture.evidenceDocument, "operations")
	snapshot := qualificationReceiptV3DecodeMap(t, sealResult.document)
	verification := qualificationReceiptV3DecodeMap(t, verificationResult.document)
	receipt := map[string]any{
		"artifactIndex": map[string]any{
			"contentDigest":         indexed.indexDigest,
			"evidenceClosureDigest": indexed.closureDigest,
			"stage":                 "committed",
		},
		"build": map[string]any{
			"contract": input["buildContract"],
			"manifest": input["buildManifest"],
		},
		"completedAt": qualificationReceiptV3Time(time.Now()),
		"credentialSet": map[string]any{
			"audience":             credential["audience"],
			"issuance":             map[string]any{"artifactId": credential["issuanceArtifactId"]},
			"issuer":               credential["issuer"],
			"memberBindingsDigest": credential["memberBindingsDigest"],
			"memberCount":          credential["memberCount"],
			"revocation":           map[string]any{"artifactId": credential["revocationArtifactId"]},
			"setHandleHash":        credential["setHandleHash"],
			"setId":                credential["setId"],
		},
		"decision": "qualified",
		"evidence": map[string]any{
			"closureDigest":   indexed.closureDigest,
			"orchestrationId": fixture.orchestrationID.String(),
			"runId":           fixture.evidenceDocument["runId"],
		},
		"evidencePlan":  fixture.evidenceDocument,
		"goldenRuntime": input["goldenRuntime"],
		"issuedAt":      qualificationReceiptV3Time(time.Now()),
		"operationId":   operations["receiptSign"],
		"planAuthority": map[string]any{
			"artifactId":          fixture.evidenceDocument["qualificationPlanArtifactId"],
			"authorityHash":       fixture.envelope.hash,
			"authorityId":         fixture.authorityID.String(),
			"evidencePlanHash":    fixture.evidencePlan.hash,
			"freezeOperationId":   fixture.operationID.String(),
			"inputAuthorityId":    fixture.inputAuthorityID.String(),
			"inputHash":           fixture.input.hash,
			"planDigest":          fixture.projection.hash,
			"projectionHash":      fixture.projection.hash,
			"targetHash":          fixture.target.hash,
			"trustBindingsDigest": fixture.trustBindingsDigest,
			"trustHash":           fixture.trust.hash,
		},
		"qualificationManifest":  input["qualificationManifest"],
		"qualificationStartedAt": qualificationReceiptV3Time(time.Now()),
		"receiptId":              outputs["receiptId"],
		"schemaVersion":          "worksflow-qualification-receipt/v3",
		"signers": map[string]any{
			"approver": map[string]any{
				"identity": "spiffe://qualification.example/approver", "keyId": "approver-key",
				"role": "release-approver",
			},
			"runner": map[string]any{
				"identity": "spiffe://qualification.example/runner", "keyId": "runner-key",
				"role": "qualification-runner",
			},
		},
		"snapshot":             snapshot,
		"snapshotVerification": verification,
		"source":               input["source"],
		"target":               target,
		"templateRelease":      input["templateRelease"],
		"trust":                trust,
	}
	payload := qualificationReceiptV3Canonical(t, map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"predicate":     receipt,
		"predicateType": "https://worksflow.dev/attestations/qualification-receipt/v3",
		"subject": []any{map[string]any{
			"digest": map[string]any{"sha256": strings.TrimPrefix(snapshot["snapshotDigest"].(string), "sha256:")},
			"name":   outputs["snapshotId"],
		}},
	})
	paeBytes := append([]byte(fmt.Sprintf(
		"DSSEv1 28 application/vnd.in-toto+json %d ", len(payload.bytes),
	)), payload.bytes...)
	pae := qualificationPlanMigrationMaterial{
		hash: qualificationPlanMigrationDigestBytes(paeBytes), bytes: paeBytes,
	}
	return payload, pae
}

func qualificationReceiptV3Envelope(
	t *testing.T,
	payload []byte,
	runner qualificationReceiptV3RequestFixture,
	runnerRecord qualificationReceiptV3Record,
	approver qualificationReceiptV3RequestFixture,
	approverRecord qualificationReceiptV3Record,
) qualificationPlanMigrationMaterial {
	t.Helper()
	signatures := []map[string]any{
		{"keyid": runner.documentValue["signerKeyId"], "sig": base64.StdEncoding.EncodeToString(runnerRecord.signature)},
		{"keyid": approver.documentValue["signerKeyId"], "sig": base64.StdEncoding.EncodeToString(approverRecord.signature)},
	}
	sort.Slice(signatures, func(i, j int) bool {
		return signatures[i]["keyid"].(string) < signatures[j]["keyid"].(string)
	})
	return qualificationReceiptV3Canonical(t, map[string]any{
		"payload":     base64.StdEncoding.EncodeToString(payload),
		"payloadType": "application/vnd.in-toto+json",
		"signatures":  signatures,
	})
}

func completeQualificationReceiptV3(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture qualificationPlanMigrationFixture,
	seal qualificationReceiptV3RequestFixture,
	sealRecord qualificationReceiptV3Record,
	verification qualificationReceiptV3RequestFixture,
	verificationRecord qualificationReceiptV3Record,
	runner qualificationReceiptV3RequestFixture,
	runnerRecord qualificationReceiptV3Record,
	approver qualificationReceiptV3RequestFixture,
	approverRecord qualificationReceiptV3Record,
	payload qualificationPlanMigrationMaterial,
	pae qualificationPlanMigrationMaterial,
	envelope qualificationPlanMigrationMaterial,
) qualificationReceiptV3Completion {
	t.Helper()
	completion, err := completeQualificationReceiptV3Raw(
		ctx, database, fixture,
		seal, sealRecord, verification, verificationRecord, runner, runnerRecord, approver, approverRecord,
		payload, pae, envelope,
	)
	if err != nil {
		t.Fatalf("complete Receipt v3: %v", err)
	}
	return completion
}

func completeQualificationReceiptV3Raw(
	ctx context.Context,
	database *sql.DB,
	fixture qualificationPlanMigrationFixture,
	seal qualificationReceiptV3RequestFixture,
	sealRecord qualificationReceiptV3Record,
	verification qualificationReceiptV3RequestFixture,
	verificationRecord qualificationReceiptV3Record,
	runner qualificationReceiptV3RequestFixture,
	runnerRecord qualificationReceiptV3Record,
	approver qualificationReceiptV3RequestFixture,
	approverRecord qualificationReceiptV3Record,
	payload qualificationPlanMigrationMaterial,
	pae qualificationPlanMigrationMaterial,
	envelope qualificationPlanMigrationMaterial,
) (qualificationReceiptV3Completion, error) {
	// Completion time is Store-owned and must strictly follow every source row.
	if _, err := database.ExecContext(ctx, `SELECT pg_catalog.pg_sleep(0.002)`); err != nil {
		return qualificationReceiptV3Completion{}, err
	}
	completion := qualificationReceiptV3Completion{}
	err := database.QueryRowContext(ctx, `
SELECT (completed.receipt_record).receipt_id,
       (completed.receipt_record).completion_hash,
       (completed.receipt_record).completed_at,
       completed.idempotent
FROM complete_qualification_receipt_v3(
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13,$14,$15,$16,$17::jsonb,$18
) AS completed
`, fixture.authorityID, seal.material.hash, sealRecord.hash,
		verification.material.hash, verificationRecord.hash,
		runner.material.hash, runnerRecord.hash, approver.material.hash, approverRecord.hash,
		payload.hash, payload.bytes, payload.document, pae.hash, pae.bytes,
		envelope.hash, envelope.bytes, envelope.document, envelope.hash,
	).Scan(&completion.receiptID, &completion.completionHash, &completion.completedAt, &completion.idempotent)
	return completion, err
}

func qualificationReceiptV3Canonical(t *testing.T, value any) qualificationPlanMigrationMaterial {
	t.Helper()
	return qualificationPlanMigrationCanonical(t, value)
}

func qualificationReceiptV3Time(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func qualificationReceiptV3CloneMap(value map[string]any) map[string]any {
	encoded, _ := json.Marshal(value)
	var cloned map[string]any
	_ = json.Unmarshal(encoded, &cloned)
	return cloned
}

func qualificationReceiptV3DecodeMap(t *testing.T, document string) map[string]any {
	t.Helper()
	value := qualificationReceiptV3DecodeMapForHelper(document)
	if value == nil {
		t.Fatalf("decode Receipt v3 fixture document %q", document)
	}
	return value
}

func qualificationReceiptV3DecodeMapForHelper(document string) map[string]any {
	var value map[string]any
	if json.Unmarshal([]byte(document), &value) != nil {
		return nil
	}
	return value
}

func qualificationReceiptV3Map(t *testing.T, value map[string]any, key string) map[string]any {
	t.Helper()
	child, ok := value[key].(map[string]any)
	if !ok {
		t.Fatalf("Receipt v3 fixture %s is %T, want object", key, value[key])
	}
	return child
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
