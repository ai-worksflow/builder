package modelgovernance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresActivationStoreRealPostgresAuthorityClosure(t *testing.T) {
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
	var applicationRoleExisted bool
	if err := roleLock.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_application')`).Scan(&applicationRoleExisted); err != nil {
		t.Fatal(err)
	}
	if !applicationRoleExisted {
		if _, err := roleLock.ExecContext(ctx, `
CREATE ROLE worksflow_application NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION`); err != nil {
			t.Fatalf("create application group for ACL canary: %v", err)
		}
		defer func() {
			_, _ = roleLock.ExecContext(context.Background(), `DROP ROLE IF EXISTS worksflow_application`)
		}()
	}
	var migrationOwnerRoleExisted bool
	if err := roleLock.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_migration_owner')`).Scan(&migrationOwnerRoleExisted); err != nil {
		t.Fatal(err)
	}
	if !migrationOwnerRoleExisted {
		if _, err := roleLock.ExecContext(ctx, `
CREATE ROLE worksflow_migration_owner NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION`); err != nil {
			t.Fatalf("create migration-owner group for ownership canary: %v", err)
		}
		defer func() {
			_, _ = roleLock.ExecContext(context.Background(), `DROP ROLE IF EXISTS worksflow_migration_owner`)
		}()
	}

	if _, err := base.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		t.Fatal(err)
	}
	schema := "model_governance_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	if _, err := base.ExecContext(ctx, `ALTER SCHEMA "`+schema+`" OWNER TO worksflow_migration_owner`); err != nil {
		t.Fatal(err)
	}
	// This defer runs before the earlier role cleanup defers, ensuring the
	// migration-owned objects are gone before a test-created stable role drops.
	defer func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	}()
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	database, err := sql.Open("pgx", modelGovernancePostgresSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	up, err := os.ReadFile("../../migrations/000069_model_governance_activation_store.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) {
			t.Fatalf("apply 000069: sqlstate=%s position=%d message=%s", postgresError.Code, postgresError.Position, postgresError.Message)
		}
		t.Fatalf("apply 000069: %v", err)
	}
	genesisUp, err := os.ReadFile("../../migrations/000070_model_governance_signed_genesis.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(genesisUp)); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) {
			t.Fatalf("apply 000070: sqlstate=%s position=%d message=%s", postgresError.Code, postgresError.Position, postgresError.Message)
		}
		t.Fatalf("apply 000070: %v", err)
	}
	store, err := NewPostgresActivationStore(database)
	if err != nil {
		t.Fatal(err)
	}

	now, err := store.TrustedTime(ctx)
	if err != nil || !canonicalGovernanceTime(now) {
		t.Fatalf("TrustedTime() = %s, %v", now, err)
	}
	empty := postgresActivationTestRecordAt(t, 1, "empty", now)
	if _, err := store.AppendActivation(ctx, activationAppendForRecord(empty)); !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("empty-head append error = %v", err)
	}
	var emptyCount int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM model_governance_activation_records`).Scan(&emptyCount); err != nil || emptyCount != 0 {
		t.Fatalf("empty-head record count = %d, %v", emptyCount, err)
	}
	func() {
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer transaction.Rollback()
		down, err := os.ReadFile("../../migrations/000070_model_governance_signed_genesis.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, string(down)); err != nil {
			t.Fatalf("empty v70 rollback is not reversible: %v", err)
		}
	}()
	assertPostgresRevocationAnchor(t, ctx, database, store)
	var currentEpoch int64
	var currentRevocationHash string
	if err := database.QueryRowContext(ctx, `
SELECT epoch, authority_hash FROM model_governance_revocation_anchor WHERE singleton`).Scan(&currentEpoch, &currentRevocationHash); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveGovernanceTrustPolicy(ctx, GovernanceTrustPolicyObservation{
		PolicyHash: testDigest("postgres-governance-policy"), RevocationAuthorityHash: currentRevocationHash, RevocationEpoch: uint64(currentEpoch),
	}); err != nil {
		t.Fatal(err)
	}

	baseline := postgresActivationTestRecordAt(t, 1, "baseline", now)
	seedPostgresActivationBaseline(t, ctx, database, baseline)
	candidateNow, err := store.TrustedTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate := postgresActivationTestRecordAt(t, 2, "candidate", candidateNow)
	candidate.Workload = baseline.Workload
	candidate.PreviousFence = baseline.Fence
	rehashPostgresActivationRecord(t, &candidate)
	inserted, err := store.AppendActivation(ctx, activationAppendForRecord(candidate))
	if err != nil || !sameActivationRecord(inserted, candidate) {
		t.Fatalf("append candidate = %+v, %v", inserted, err)
	}
	for projection, load := range map[string]func() (ActivationRecord, error){
		"operation": func() (ActivationRecord, error) { return store.GetActivationOperation(ctx, candidate.OperationID) },
		"head":      func() (ActivationRecord, error) { return store.GetActiveActivation(ctx, candidate.Workload) },
		"history": func() (ActivationRecord, error) {
			return store.GetActivationGeneration(ctx, candidate.Workload, candidate.Generation)
		},
		"exact-profile": func() (ActivationRecord, error) {
			return store.GetActivatedProfile(ctx, CorpusProfileBinding{
				ID: candidate.ProfileID, ContentHash: candidate.ProfileContentHash, Workload: candidate.Workload,
			})
		},
	} {
		got, err := load()
		if err != nil || !sameActivationRecord(got, candidate) {
			t.Fatalf("%s projection = %+v, %v", projection, got, err)
		}
	}
	replayed, err := store.AppendActivation(ctx, activationAppendForRecord(candidate))
	if err != nil || !sameActivationRecord(replayed, candidate) {
		t.Fatalf("exact operation retry = %+v, %v", replayed, err)
	}
	altered := candidate
	altered.RunnerImmutableDigest = testDigest("same-operation-altered-runner")
	if _, err := store.AppendActivation(ctx, activationAppendForRecord(altered)); !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("same operation with altered immutable field error = %v", err)
	}
	authorityDriftNow, _ := store.TrustedTime(ctx)
	authorityDrift := postgresActivationTestRecordAt(t, 3, "ordinary-authority-drift", authorityDriftNow)
	authorityDrift.Workload = candidate.Workload
	authorityDrift.PreviousFence = candidate.Fence
	authorityDrift.TrustPolicyHash = testDigest("unobserved-ordinary-policy")
	rehashPostgresActivationRecord(t, &authorityDrift)
	if _, err := store.AppendActivation(ctx, activationAppendForRecord(authorityDrift)); !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("ordinary activation bypassed atomic trust-policy anchor: %v", err)
	}

	concurrentNow, err := store.TrustedTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	left := postgresActivationTestRecordAt(t, 3, "concurrent-left", concurrentNow)
	right := postgresActivationTestRecordAt(t, 3, "concurrent-right", concurrentNow)
	for _, record := range []*ActivationRecord{&left, &right} {
		record.Workload = candidate.Workload
		record.PreviousFence = candidate.Fence
		rehashPostgresActivationRecord(t, record)
	}
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, record := range []ActivationRecord{left, right} {
		wait.Add(1)
		go func(value ActivationRecord) {
			defer wait.Done()
			_, appendErr := store.AppendActivation(ctx, activationAppendForRecord(value))
			results <- appendErr
		}(record)
	}
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for result := range results {
		switch {
		case result == nil:
			successes++
		case errors.Is(result, ErrActivationConflict):
			conflicts++
		default:
			t.Fatalf("concurrent CAS error = %v", result)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent CAS successes=%d conflicts=%d", successes, conflicts)
	}
	head, err := store.GetActiveActivation(ctx, candidate.Workload)
	if err != nil || head.Generation != 3 || (head.OperationID != left.OperationID && head.OperationID != right.OperationID) {
		t.Fatalf("concurrent head = %+v, %v", head, err)
	}
	var historyCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM model_governance_activation_records WHERE workload = $1`, candidate.Workload).Scan(&historyCount); err != nil || historyCount != 3 {
		t.Fatalf("immutable history count = %d, %v", historyCount, err)
	}

	duplicateProfileNow, _ := store.TrustedTime(ctx)
	duplicateProfile := postgresActivationTestRecordAt(t, 4, "duplicate-profile", duplicateProfileNow)
	duplicateProfile.Workload = head.Workload
	duplicateProfile.PreviousFence = head.Fence
	duplicateProfile.ProfileID = baseline.ProfileID
	duplicateProfile.ProfileContentHash = baseline.ProfileContentHash
	rehashPostgresActivationRecord(t, &duplicateProfile)
	if _, err := store.AppendActivation(ctx, activationAppendForRecord(duplicateProfile)); !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("exact-profile overwrite error = %v", err)
	}
	staleTime := postgresActivationTestRecordAt(t, 4, "stale-time", duplicateProfileNow.Add(-time.Minute))
	staleTime.Workload = head.Workload
	staleTime.PreviousFence = head.Fence
	rehashPostgresActivationRecord(t, &staleTime)
	if _, err := store.AppendActivation(ctx, activationAppendForRecord(staleTime)); !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("backdated activation error = %v", err)
	}

	assertPostgresModelGovernanceACL(t, ctx, database, schema)
	assertPostgresSignedGenesis(t, ctx, database, store)
	for _, rollback := range []struct {
		path    string
		message string
	}{
		{"../../migrations/000070_model_governance_signed_genesis.down.sql", "cannot roll back signed Model Governance Genesis"},
		{"../../migrations/000069_model_governance_activation_store.down.sql", "cannot roll back Model Governance activation store"},
	} {
		down, readErr := os.ReadFile(rollback.path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		assertModelGovernanceRollbackFence(t, ctx, database, string(down), rollback.message)
	}
}

func assertPostgresSignedGenesis(t *testing.T, ctx context.Context, database *sql.DB, store *PostgresActivationStore) {
	t.Helper()
	var epoch int64
	var authorityHash string
	if err := database.QueryRowContext(ctx, `
SELECT epoch, authority_hash FROM model_governance_revocation_anchor WHERE singleton`).Scan(&epoch, &authorityHash); err != nil {
		t.Fatal(err)
	}
	trustHash := testDigest("postgres-governance-policy")
	if err := store.ObserveGovernanceTrustPolicy(ctx, GovernanceTrustPolicyObservation{
		PolicyHash: trustHash, RevocationAuthorityHash: authorityHash, RevocationEpoch: uint64(epoch),
	}); err != nil {
		t.Fatalf("observe PostgreSQL Genesis trust policy: %v", err)
	}
	if err := store.ObserveGovernanceTrustPolicy(ctx, GovernanceTrustPolicyObservation{
		PolicyHash: trustHash, RevocationAuthorityHash: authorityHash, RevocationEpoch: uint64(epoch),
	}); err != nil {
		t.Fatalf("replay PostgreSQL Genesis trust policy: %v", err)
	}
	if err := store.ObserveGovernanceTrustPolicy(ctx, GovernanceTrustPolicyObservation{
		PolicyHash: testDigest("same-epoch-policy-equivocation"), RevocationAuthorityHash: authorityHash, RevocationEpoch: uint64(epoch),
	}); !errors.Is(err, ErrGovernanceUntrusted) {
		t.Fatalf("PostgreSQL same-epoch trust-policy equivocation = %v", err)
	}
	now, err := store.TrustedTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	genesis := postgresGenesisTestRecord(t, "postgres-Genesis", "reference-genesis", uint64(epoch), authorityHash, now)
	genesis.TrustPolicyHash = trustHash
	trustDrift := genesisAppendForRecord(genesis)
	trustDrift.CurrentTrustPolicyHash = testDigest("different-current-trust-policy")
	if _, err := store.AppendGenesis(ctx, trustDrift); !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("PostgreSQL Genesis trust-policy drift = %v", err)
	}
	revocationDrift := postgresGenesisTestRecord(t, "postgres-Genesis-revocation-drift", "reference-genesis-drift", uint64(epoch), testDigest("different-current-revocation"), now)
	revocationDrift.TrustPolicyHash = trustHash
	if _, err := store.AppendGenesis(ctx, genesisAppendForRecord(revocationDrift)); !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("PostgreSQL Genesis revocation-anchor drift = %v", err)
	}
	stale := postgresGenesisTestRecord(t, "postgres-Genesis-stale", "reference-genesis-stale", uint64(epoch), authorityHash, now.Add(-time.Minute))
	stale.TrustPolicyHash = trustHash
	if _, err := store.AppendGenesis(ctx, genesisAppendForRecord(stale)); !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("PostgreSQL stale Genesis time = %v", err)
	}
	command := genesisAppendForRecord(genesis)
	inserted, err := store.AppendGenesis(ctx, command)
	if err != nil || !sameActivationRecord(inserted, genesis) {
		t.Fatalf("append signed Genesis projection = %+v, %v", inserted, err)
	}
	replayed, err := store.AppendGenesis(ctx, command)
	if err != nil || !sameActivationRecord(replayed, genesis) {
		t.Fatalf("replay signed Genesis projection = %+v, %v", replayed, err)
	}
	altered := genesis
	altered.SourceTreeDigest = testDigest("altered-Genesis-source")
	if _, err := store.AppendGenesis(ctx, genesisAppendForRecord(altered)); !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("same Genesis operation with different bytes = %v", err)
	}

	nextNow, _ := store.TrustedTime(ctx)
	next := postgresActivationTestRecordAt(t, 2, "after-Genesis", nextNow)
	next.Workload = genesis.Workload
	next.PreviousFence = genesis.Fence
	rehashPostgresActivationRecord(t, &next)
	if _, err := store.AppendActivation(ctx, activationAppendForRecord(next)); err != nil {
		t.Fatalf("ordinary activation after signed Genesis: %v", err)
	}

	raceNow, _ := store.TrustedTime(ctx)
	left := postgresGenesisTestRecord(t, "Genesis-race-left", "reference-genesis-race", uint64(epoch), authorityHash, raceNow)
	right := postgresGenesisTestRecord(t, "Genesis-race-right", "reference-genesis-race", uint64(epoch), authorityHash, raceNow)
	left.TrustPolicyHash = trustHash
	right.TrustPolicyHash = trustHash
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, value := range []ActivationRecord{left, right} {
		wait.Add(1)
		go func(record ActivationRecord) {
			defer wait.Done()
			_, appendErr := store.AppendGenesis(ctx, genesisAppendForRecord(record))
			results <- appendErr
		}(value)
	}
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for result := range results {
		if result == nil {
			successes++
		} else if errors.Is(result, ErrActivationConflict) {
			conflicts++
		} else {
			t.Fatalf("Genesis concurrency error: %v", result)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("Genesis first-writer race successes=%d conflicts=%d", successes, conflicts)
	}
}

func assertPostgresModelGovernanceACL(t *testing.T, ctx context.Context, database *sql.DB, schema string) {
	t.Helper()
	functionIdentity := schema + ".append_model_governance_activation(bigint,text,uuid,text,text,uuid,text,text,text,text,text,bigint,bigint,text,text,text,text,text,text,text,timestamp with time zone)"
	genesisIdentity := schema + ".append_model_governance_genesis(uuid,text,text,uuid,text,text,text,text,text,bigint,bigint,text,text,text,text,text,text,text,text,text,text,bigint,text,bigint,timestamp with time zone,text)"
	observeIdentity := schema + ".observe_model_governance_revocation_authority(bigint,text,bytea,jsonb)"
	trustIdentity := schema + ".observe_model_governance_trust_policy(text,text,bigint)"
	for _, expectation := range []struct {
		identity string
		setof    bool
	}{{functionIdentity, true}, {genesisIdentity, true}, {observeIdentity, false}, {trustIdentity, false}} {
		identity := expectation.identity
		var securityDefiner, ownerExact, pathExact, resultExact bool
		var publicExecute, applicationExecute, applicationGrantable bool
		var owner, resultContract string
		err := database.QueryRowContext(ctx, `
SELECT
  procedure.prosecdef,
  pg_get_userbyid(procedure.proowner),
  pg_get_function_result(procedure.oid),
  procedure.proowner = (
    SELECT relation.relowner FROM pg_class AS relation
    JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
    WHERE namespace.nspname = $2 AND relation.relname = 'model_governance_activation_records'
  ),
  procedure.proconfig = ARRAY['search_path=pg_catalog, ' || $2 || ', pg_temp'],
  CASE WHEN $3 THEN
    procedure.proretset
    AND procedure.prorettype = to_regtype($2 || '.model_governance_activation_records')
  ELSE
    NOT procedure.proretset AND procedure.prorettype = 'void'::regtype
  END,
  EXISTS (
    SELECT 1 FROM aclexplode(coalesce(procedure.proacl, acldefault('f', procedure.proowner))) AS acl
    WHERE acl.grantee = 0 AND acl.privilege_type = 'EXECUTE'
  ),
  has_function_privilege('worksflow_application', procedure.oid, 'EXECUTE'),
  EXISTS (
    SELECT 1 FROM aclexplode(coalesce(procedure.proacl, acldefault('f', procedure.proowner))) AS acl
    WHERE acl.grantee = 'worksflow_application'::regrole
      AND acl.privilege_type = 'EXECUTE' AND acl.is_grantable
  )
FROM pg_proc AS procedure
WHERE procedure.oid = $1::regprocedure`, identity, schema, expectation.setof).Scan(
			&securityDefiner, &owner, &resultContract, &ownerExact, &pathExact, &resultExact,
			&publicExecute, &applicationExecute, &applicationGrantable,
		)
		if err != nil {
			t.Fatalf("inspect %s: %v", identity, err)
		}
		if !securityDefiner || owner != "worksflow_migration_owner" || !ownerExact || !pathExact || !resultExact ||
			publicExecute || applicationExecute || applicationGrantable || resultContract == "" {
			t.Fatalf("unsafe function ACL/owner/path/result for %s: definer=%t owner=%q ownerExact=%t result=%q resultExact=%t pathExact=%t public=%t app=%t grantable=%t",
				identity, securityDefiner, owner, ownerExact, resultContract, resultExact, pathExact,
				publicExecute, applicationExecute, applicationGrantable)
		}
	}
	anchorIdentity := schema + ".enforce_model_governance_activation_authority_anchor()"
	var anchorClosed bool
	if err := database.QueryRowContext(ctx, `
SELECT
  NOT procedure.prosecdef
  AND procedure.prorettype = 'trigger'::regtype
  AND procedure.proconfig = ARRAY['search_path=pg_catalog, ' || $2 || ', pg_temp']
  AND pg_get_userbyid(procedure.proowner) = 'worksflow_migration_owner'
  AND NOT has_function_privilege('worksflow_application', procedure.oid, 'EXECUTE')
  AND NOT EXISTS (
    SELECT 1 FROM aclexplode(coalesce(procedure.proacl, acldefault('f', procedure.proowner))) AS acl
    WHERE acl.grantee = 0 AND acl.privilege_type = 'EXECUTE'
  )
  AND EXISTS (
    SELECT 1 FROM pg_trigger AS trigger
    JOIN pg_class AS relation ON relation.oid = trigger.tgrelid
    JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
    WHERE trigger.tgfoid = procedure.oid
      AND trigger.tgname = 'model_governance_activation_authority_anchor'
      AND namespace.nspname = $2
      AND relation.relname = 'model_governance_activation_records'
      AND trigger.tgenabled = 'O'
  )
FROM pg_proc AS procedure
WHERE procedure.oid = $1::regprocedure`, anchorIdentity, schema).Scan(&anchorClosed); err != nil || !anchorClosed {
		t.Fatalf("PostgreSQL activation authority anchor trigger is not closed: closed=%t err=%v", anchorClosed, err)
	}
	for _, table := range []string{
		"model_governance_activation_records", "model_governance_activation_heads", "model_governance_revocation_anchor",
	} {
		var selectPrivilege, mutationPrivilege bool
		if err := database.QueryRowContext(ctx, `
SELECT
  has_table_privilege('worksflow_application', $1, 'SELECT'),
  has_table_privilege('worksflow_application', $1, 'INSERT,UPDATE,DELETE,TRUNCATE')`,
			schema+"."+table).Scan(&selectPrivilege, &mutationPrivilege); err != nil {
			t.Fatal(err)
		}
		if selectPrivilege || mutationPrivilege {
			t.Fatalf("worksflow_application has direct %s privilege: select=%t mutation=%t", table, selectPrivilege, mutationPrivilege)
		}
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `SET ROLE worksflow_application`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = connection.ExecContext(context.Background(), `RESET ROLE`) }()
	if _, err := connection.ExecContext(ctx, `INSERT INTO "`+schema+`".model_governance_activation_heads (workload, operation_id) VALUES ('forbidden', gen_random_uuid())`); !postgresPermissionDenied(err) {
		t.Fatalf("application direct head INSERT error = %v", err)
	}
	if _, err := connection.ExecContext(ctx, `SELECT "`+schema+`".observe_model_governance_revocation_authority(NULL, NULL, NULL, NULL)`); !postgresPermissionDenied(err) {
		t.Fatalf("application observe execution error = %v", err)
	}
	if _, err := connection.ExecContext(ctx, `SELECT "`+schema+`".append_model_governance_genesis(NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL)`); !postgresPermissionDenied(err) {
		t.Fatalf("application Genesis execution error = %v", err)
	}
	if _, err := connection.ExecContext(ctx, `SELECT "`+schema+`".observe_model_governance_trust_policy(NULL,NULL,NULL)`); !postgresPermissionDenied(err) {
		t.Fatalf("application trust-policy observation error = %v", err)
	}
	if _, err := connection.ExecContext(ctx, `SELECT "`+schema+`".enforce_model_governance_activation_authority_anchor()`); !postgresPermissionDenied(err) {
		t.Fatalf("application authority-anchor guard execution error = %v", err)
	}
}

func assertPostgresRevocationAnchor(t *testing.T, ctx context.Context, database *sql.DB, store *PostgresActivationStore) {
	t.Helper()
	now, err := store.TrustedTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first := finalizedPostgresRevocationAuthority(t, GovernanceRevocationAuthority{
		Epoch: 1, IssuedAt: now.Add(-time.Second), ExpiresAt: now.Add(4 * time.Minute),
		DigestRevocations: []GovernanceRevocation{}, SignerRevocations: []GovernanceSignerRevocation{},
	})
	if err := store.ObserveGovernanceRevocationAuthority(ctx, first); err != nil {
		t.Fatalf("observe first revocation authority: %v", err)
	}
	if err := store.ObserveGovernanceRevocationAuthority(ctx, first); err != nil {
		t.Fatalf("same epoch/hash/bytes replay: %v", err)
	}
	second := finalizedPostgresRevocationAuthority(t, GovernanceRevocationAuthority{
		Epoch: 2, IssuedAt: now, ExpiresAt: now.Add(4 * time.Minute),
		DigestRevocations: []GovernanceRevocation{{
			Digest: testDigest("postgres-revoked-digest"), ReasonHash: testDigest("postgres-revoked-reason"), RevokedAt: now,
		}},
		SignerRevocations: []GovernanceSignerRevocation{},
	})
	if err := store.ObserveGovernanceRevocationAuthority(ctx, second); err != nil {
		t.Fatalf("observe cumulative second epoch: %v", err)
	}

	// Prove the SQL post-lock time check, rather than only pinning source order.
	lockTime, err := store.TrustedTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expiring := finalizedPostgresRevocationAuthority(t, GovernanceRevocationAuthority{
		Epoch: 3, IssuedAt: lockTime, ExpiresAt: lockTime.Add(time.Second),
		DigestRevocations: append([]GovernanceRevocation(nil), second.DigestRevocations...),
		SignerRevocations: []GovernanceSignerRevocation{},
	})
	expiringBytes, err := CanonicalGovernanceRevocationAuthorityJSON(expiring)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, `LOCK TABLE model_governance_revocation_anchor IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	expiringResult := make(chan error, 1)
	go func() {
		_, observeErr := database.ExecContext(ctx, `
SELECT observe_model_governance_revocation_authority($1, $2, $3, $4::jsonb)`,
			int64(expiring.Epoch), expiring.AuthorityHash, expiringBytes, string(expiringBytes))
		expiringResult <- observeErr
	}()
	select {
	case early := <-expiringResult:
		_ = blocker.Rollback()
		t.Fatalf("revocation observe returned before the singleton lock was released: %v", early)
	case <-time.After(1100 * time.Millisecond):
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-expiringResult; !postgresActivationConflict(err) {
		t.Fatalf("authority that expired while lock-waiting error = %v", err)
	}

	equivocationDraft := second
	equivocationDraft.ExpiresAt = second.ExpiresAt.Add(time.Millisecond)
	equivocationDraft.AuthorityHash = ""
	equivocation := finalizedPostgresRevocationAuthority(t, equivocationDraft)
	if err := store.ObserveGovernanceRevocationAuthority(ctx, equivocation); !errors.Is(err, ErrGovernanceUntrusted) {
		t.Fatalf("same-epoch equivocation error = %v", err)
	}
	removal := finalizedPostgresRevocationAuthority(t, GovernanceRevocationAuthority{
		Epoch: 3, IssuedAt: now, ExpiresAt: now.Add(4 * time.Minute),
		DigestRevocations: []GovernanceRevocation{}, SignerRevocations: []GovernanceSignerRevocation{},
	})
	if err := store.ObserveGovernanceRevocationAuthority(ctx, removal); !errors.Is(err, ErrGovernanceUntrusted) {
		t.Fatalf("higher-epoch deletion error = %v", err)
	}
	if err := store.ObserveGovernanceRevocationAuthority(ctx, first); !errors.Is(err, ErrGovernanceUntrusted) {
		t.Fatalf("lower-epoch rollback error = %v", err)
	}

	unsorted := second
	unsorted.Epoch = 3
	unsorted.DigestRevocations = []GovernanceRevocation{
		{Digest: testDigest("z-revocation"), ReasonHash: testDigest("z-reason"), RevokedAt: now},
		{Digest: testDigest("a-revocation"), ReasonHash: testDigest("a-reason"), RevokedAt: now},
	}
	unsorted.AuthorityHash = testDigest("irrelevant-unsorted-hash")
	if err := store.ObserveGovernanceRevocationAuthority(ctx, unsorted); !errors.Is(err, ErrGovernanceUntrusted) {
		t.Fatalf("Go canonical sorted validator error = %v", err)
	}

	encoded, err := CanonicalGovernanceRevocationAuthorityJSON(second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
SELECT observe_model_governance_revocation_authority($1, $2, $3, $4::jsonb)`,
		second.Epoch, testDigest("wrong-authority-hash"), encoded, string(encoded)); !postgresActivationConflict(err) {
		t.Fatalf("SQL bytes/hash mismatch error = %v", err)
	}
	tamperedDocument := strings.Replace(string(encoded), `"epoch":2`, `"epoch":3`, 1)
	if _, err := database.ExecContext(ctx, `
SELECT observe_model_governance_revocation_authority($1, $2, $3, $4::jsonb)`,
		second.Epoch, second.AuthorityHash, encoded, tamperedDocument); !postgresActivationConflict(err) {
		t.Fatalf("SQL bytes/document mismatch error = %v", err)
	}
	var epoch int64
	var hash string
	var bytes []byte
	var document string
	if err := database.QueryRowContext(ctx, `
SELECT epoch, authority_hash, authority_bytes, authority_document::text
FROM model_governance_revocation_anchor WHERE singleton`).Scan(&epoch, &hash, &bytes, &document); err != nil {
		t.Fatal(err)
	}
	if epoch != 2 || hash != second.AuthorityHash || string(bytes) != string(encoded) || document == "" {
		t.Fatalf("durable revocation anchor drifted: epoch=%d hash=%q bytes=%d document=%q", epoch, hash, len(bytes), document)
	}
}

func assertModelGovernanceRollbackFence(t *testing.T, ctx context.Context, database *sql.DB, down, expected string) {
	t.Helper()
	blocker, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, `LOCK TABLE model_governance_activation_heads IN ACCESS SHARE MODE`); err != nil {
		_ = blocker.Rollback()
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, rollbackErr := database.ExecContext(ctx, down)
		result <- rollbackErr
	}()
	select {
	case early := <-result:
		_ = blocker.Rollback()
		t.Fatalf("rollback bypassed ACCESS EXCLUSIVE governance fence: %v", early)
	case <-time.After(100 * time.Millisecond):
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil || !strings.Contains(err.Error(), expected) {
		t.Fatalf("nonempty governance rollback error = %v", err)
	}
}

func seedPostgresActivationBaseline(t *testing.T, ctx context.Context, database *sql.DB, record ActivationRecord) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO model_governance_activation_records (
  operation_id, request_hash, workload, profile_id, profile_content_hash,
  receipt_digest, receipt_payload_digest, activation_envelope_digest,
  activation_payload_digest, previous_generation, generation, previous_fence,
  fence, corpus_content_hash, provider_route_authority_hash,
  runner_immutable_digest, source_tree_digest, trust_policy_hash, activated_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19
)`, postgresActivationRecordArguments(record)...); err != nil {
		t.Fatalf("seed structural baseline record: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO model_governance_activation_heads (workload, operation_id) VALUES ($1, $2)`,
		record.Workload, record.OperationID); err != nil {
		t.Fatalf("seed structural baseline head: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func postgresActivationRecordArguments(record ActivationRecord) []any {
	return []any{
		record.OperationID, record.RequestHash, record.Workload, record.ProfileID, record.ProfileContentHash,
		record.ReceiptDigest, record.ReceiptPayloadDigest, record.ActivationEnvelopeDigest,
		record.ActivationPayloadDigest, int64(record.PreviousGeneration), int64(record.Generation),
		record.PreviousFence, record.Fence, record.CorpusContentHash, record.ProviderRouteAuthorityHash,
		record.RunnerImmutableDigest, record.SourceTreeDigest, record.TrustPolicyHash, record.ActivatedAt,
	}
}

func postgresActivationTestRecordAt(t *testing.T, generation uint64, seed string, activatedAt time.Time) ActivationRecord {
	t.Helper()
	record := postgresActivationTestRecord(t, generation, seed)
	record.ActivatedAt = activatedAt.UTC().Truncate(time.Millisecond)
	return record
}

func rehashPostgresActivationRecord(t *testing.T, record *ActivationRecord) {
	t.Helper()
	requestHash, err := activationRequestHash(ActivationRequest{
		OperationID: record.OperationID, ReceiptDigest: record.ReceiptDigest,
		ExpectedGeneration: record.PreviousGeneration, ExpectedFence: record.PreviousFence,
	})
	if err != nil {
		t.Fatal(err)
	}
	record.RequestHash = requestHash
}

func activationAppendForRecord(record ActivationRecord) ActivationAppend {
	return ActivationAppend{
		ExpectedGeneration: record.PreviousGeneration,
		ExpectedFence:      record.PreviousFence,
		Record:             record,
	}
}

func postgresGenesisTestRecord(t *testing.T, seed, workload string, epoch uint64, authorityHash string, activatedAt time.Time) ActivationRecord {
	t.Helper()
	record := postgresActivationTestRecordAt(t, 1, seed, activatedAt)
	record.AuthorityKind = GenesisAuthorityKind
	record.Workload = workload
	record.GenesisEnvelopeDigest = record.ActivationEnvelopeDigest
	record.GenesisPayloadDigest = record.ActivationPayloadDigest
	record.InitialRevocationAuthorityID = GovernanceRevocationAuthorityID
	record.InitialRevocationAuthorityHash = authorityHash
	record.InitialRevocationAuthorityEpoch = epoch
	request := GenesisBootstrapRequest{
		OperationID: record.OperationID, ReceiptDigest: record.ReceiptDigest, ExpectedEmptyFence: record.PreviousFence,
	}
	requestHash, err := genesisBootstrapRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	record.RequestHash = requestHash
	return record
}

func genesisAppendForRecord(record ActivationRecord) GenesisAppend {
	return GenesisAppend{
		ExpectedGeneration: 0, ExpectedFence: record.PreviousFence,
		CurrentTrustPolicyHash:          record.TrustPolicyHash,
		CurrentRevocationAuthorityHash:  record.InitialRevocationAuthorityHash,
		CurrentRevocationAuthorityEpoch: record.InitialRevocationAuthorityEpoch,
		Record:                          record,
	}
}

func finalizedPostgresRevocationAuthority(t *testing.T, authority GovernanceRevocationAuthority) GovernanceRevocationAuthority {
	t.Helper()
	digest, err := GovernanceRevocationAuthorityHash(authority)
	if err != nil {
		t.Fatal(err)
	}
	authority.AuthorityHash = digest
	return authority
}

func postgresPermissionDenied(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "42501"
}

func postgresObjectStateError(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "55000"
}

func postgresActivationConflict(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "40001"
}

func modelGovernancePostgresSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema+",public")
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return fmt.Sprintf("%s search_path=%s,public", strings.TrimSpace(dsn), schema)
}
