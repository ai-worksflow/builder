package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	qualificationinputauthority "github.com/worksflow/builder/backend/internal/qualificationinputauthority"
)

const qualificationInputPrecommitMigration = "000080_qualification_input_precommit_authority.up.sql"

type qualificationInputPrecommitMaterial struct {
	hash     string
	bytes    []byte
	document string
}

type qualificationInputPrecommitFixture struct {
	operationID uuid.UUID
	authorityID uuid.UUID
	authority   qualificationInputPrecommitMaterial
	request     qualificationInputPrecommitMaterial
	source      qualificationInputPrecommitMaterial
	credential  qualificationInputPrecommitMaterial
	wiaID       uuid.UUID
	policyID    uuid.UUID
	planID      uuid.UUID
}

func TestQualificationInputPrecommitMigrationDeclaresClosedBoundary(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(qualificationInputPrecommitMigration)
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000080_qualification_input_precommit_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	fence := strings.Index(text, "SELECT pg_catalog.pg_advisory_xact_lock(")
	firstDDL := strings.Index(text, "CREATE FUNCTION qualification_input_precommit_hash_v1")
	if fence < 0 || firstDDL < 0 || fence > firstDDL {
		t.Fatal("input-precommit migration must acquire the rollout fence before DDL")
	}
	for _, required := range []string{
		"CREATE TABLE qualification_input_precommit_executable_binding_generations",
		"CREATE TABLE qualification_input_precommit_executable_binding_heads",
		"CREATE TABLE qualification_input_source_receipt_admissions",
		"CREATE TABLE qualification_input_credential_receipt_admissions",
		"CREATE TABLE qualification_input_precommit_authorities",
		"CREATE TABLE qualification_input_precommit_identity_reservations",
		"CREATE TABLE qualification_input_precommit_wia_reservations",
		"CREATE TABLE qualification_input_precommit_plan_reservations",
		"CREATE FUNCTION review_qualification_input_precommit_executable_binding_v1(",
		"CREATE FUNCTION admit_qualification_input_source_receipt_v1(",
		"CREATE FUNCTION admit_qualification_input_credential_receipt_v1(",
		"CREATE FUNCTION issue_qualification_input_precommit_v1(",
		"CREATE FUNCTION resolve_qualification_input_precommit_for_promotion_v1(",
		"CREATE FUNCTION qualification_input_precommit_authority_record_is_exact_v1(",
		"CREATE FUNCTION qualification_input_precommit_string_is_secret_free_v1(",
		"ON CONFLICT (request_hash) DO NOTHING",
		"FOR SHARE OF binding_head, binding_history",
		"qualification_input_precommit_binding_heads_no_removal",
		"FOR SHARE",
		"pg_catalog.current_setting('role') = 'none'",
		"login.rolname = session_user::text",
		"operator.rolname = p_expected_login",
		"membership.inherit_option",
		"NOT membership.set_option",
		"NOT membership.admin_option",
		"WHERE inbound.roleid = operator.oid",
		"WHERE owned_database.datdba = login.oid",
		"pg_catalog.pg_get_userbyid(database.datdba) = session_user::text",
		"worksflow.qualification-input-precommit.source-request/v1",
		"worksflow.qualification-input-precommit.credential-request/v1",
		"worksflow.qualification-input-precommit.receipt-admission/v1",
		"worksflow.qualification-input-precommit.authority/v1",
		"GRANT EXECUTE ON FUNCTION %I.resolve_qualification_input_precommit_for_promotion_v1(uuid,uuid)",
		"DEFERRABLE INITIALLY DEFERRED",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("input-precommit migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"session_replication_role", "CREATE ROLE", "ON DELETE CASCADE",
		"GRANT SELECT ON TABLE", "GRANT INSERT ON TABLE", "GRANT UPDATE ON TABLE",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("input-precommit migration unexpectedly contains %q", forbidden)
		}
	}
	downText := string(down)
	for _, required := range []string{
		"qualification_input_precommit_down_guard",
		"while Promotion v2 or its handoff successor is installed",
		"relname LIKE 'qualification_promotion_v2_%'",
		"LOCK TABLE qualification_input_precommit_authorities",
		"LOCK TABLE qualification_input_precommit_executable_binding_heads",
		"cannot roll back Qualification Input Precommit while immutable authority history exists",
		"DROP TABLE IF EXISTS qualification_input_precommit_authorities",
	} {
		if !strings.Contains(downText, required) {
			t.Fatalf("input-precommit rollback is missing %q", required)
		}
	}
}

func TestQualificationInputPrecommitRollbackRejectsInstalledPromotionPostgresCanary(t *testing.T) {
	ctx, base, dsn := qualificationReceiptV3Postgres(t)
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_input_precommit_forward_")
	applyQualificationInputPrecommitPrefix(t, ctx, database, true)
	promotion, err := files.ReadFile(qualificationPromotionV2Migration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(promotion)); err != nil {
		t.Fatalf("install Promotion v2 above input-precommit: %v", err)
	}
	down, err := files.ReadFile("000080_qualification_input_precommit_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err == nil ||
		!strings.Contains(err.Error(), "Promotion v2 or its handoff successor is installed") {
		t.Fatalf("input-precommit rollback below Promotion v2 error=%v, want WIP02 forward guard", err)
	}
	var inputTable, promotionTable bool
	if err := database.QueryRowContext(ctx, `
SELECT to_regclass(current_schema() || '.qualification_input_precommit_authorities') IS NOT NULL,
       to_regclass(current_schema() || '.qualification_promotion_v2_consumptions') IS NOT NULL`,
	).Scan(&inputTable, &promotionTable); err != nil {
		t.Fatal(err)
	}
	if !inputTable || !promotionTable {
		t.Fatalf("forward rollback guard partially removed schema input=%t promotion=%t", inputTable, promotionTable)
	}
}

func TestQualificationInputPrecommitEmptyRollbackPostgresCanary(t *testing.T) {
	ctx, base, dsn := qualificationReceiptV3Postgres(t)
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_input_precommit_empty_")
	applyQualificationInputPrecommitPrefix(t, ctx, database, true)

	var tables, functions, triggers, deferredTriggers int
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM pg_catalog.pg_class
   WHERE relnamespace=current_schema()::regnamespace AND relkind='r'
     AND relname LIKE 'qualification_input_%'),
  (SELECT count(*) FROM pg_catalog.pg_proc
   WHERE pronamespace=current_schema()::regnamespace
     AND (proname LIKE 'qualification_input_%'
       OR proname LIKE 'admit_qualification_input_%'
       OR proname LIKE 'inspect_qualification_input_%'
       OR proname LIKE 'resolve_qualification_input_%'
       OR proname LIKE 'review_qualification_input_%'
       OR proname LIKE 'reject_qualification_input_%'
       OR proname LIKE 'enforce_qualification_input_%'
       OR proname LIKE 'issue_qualification_input_%')),
  (SELECT count(*) FROM pg_catalog.pg_trigger AS trigger
   JOIN pg_catalog.pg_class AS relation ON relation.oid=trigger.tgrelid
   WHERE relation.relnamespace=current_schema()::regnamespace
     AND relation.relname LIKE 'qualification_input_%' AND NOT trigger.tgisinternal),
  (SELECT count(*) FROM pg_catalog.pg_trigger AS trigger
   JOIN pg_catalog.pg_class AS relation ON relation.oid=trigger.tgrelid
   WHERE relation.relnamespace=current_schema()::regnamespace
     AND relation.relname LIKE 'qualification_input_%' AND NOT trigger.tgisinternal
     AND trigger.tgconstraint <> 0 AND trigger.tgdeferrable AND trigger.tginitdeferred)
`).Scan(&tables, &functions, &triggers, &deferredTriggers); err != nil {
		t.Fatal(err)
	}
	if tables != 8 || functions != 24 || triggers != 11 || deferredTriggers != 3 {
		t.Fatalf("input-precommit catalog tables/functions/triggers/deferred=%d/%d/%d/%d, want 8/24/11/3",
			tables, functions, triggers, deferredTriggers)
	}
	down, err := files.ReadFile("000080_qualification_input_precommit_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("empty input-precommit rollback: %v", err)
	}
}

func TestQualificationInputPrecommitAdmissionParityAndRotationPostgresCanary(t *testing.T) {
	ctx, base, dsn := qualificationReceiptV3Postgres(t)
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_input_precommit_parity_")
	database.SetMaxOpenConns(8)
	applyQualificationInputPrecommitPrefix(t, ctx, database, false)

	sourceBinding := qualificationinputauthority.ExecutableBinding{
		AuthorityID:      "source-verifier-v1",
		ExecutableDigest: qualificationPlanMigrationDigest("input-parity-source-executable-v1"),
	}
	credentialBinding := qualificationinputauthority.ExecutableBinding{
		AuthorityID:      "credential-resolver-v1",
		ExecutableDigest: qualificationPlanMigrationDigest("input-parity-credential-executable-v1"),
	}
	for _, binding := range []struct {
		role  string
		value qualificationinputauthority.ExecutableBinding
	}{
		{"source-verification", sourceBinding},
		{"credential-resolution", credentialBinding},
	} {
		var generation int64
		if err := database.QueryRowContext(ctx, `
SELECT generation FROM review_qualification_input_precommit_executable_binding_v1(
  $1,1,$2,$3,NULL
)`, binding.role, binding.value.AuthorityID, binding.value.ExecutableDigest).Scan(&generation); err != nil {
			t.Fatalf("review parity %s binding: %v", binding.role, err)
		}
	}
	if _, err := database.ExecContext(ctx, `
SELECT * FROM review_qualification_input_precommit_executable_binding_v1(
  'source-verification',2,'sk-abcdefghijklmnop',$1,$2
)`, qualificationPlanMigrationDigest("forbidden-provider-executable"), sourceBinding.ExecutableDigest); err == nil ||
		!strings.Contains(err.Error(), "WIP01") {
		t.Fatalf("provider-token executable authority review error=%v, want WIP01", err)
	}
	for _, test := range []struct {
		value      string
		secretFree bool
	}{
		{value: "ésk-abcdefghijklmnop", secretFree: false},
		{value: "http://u\u2003:p@", secretFree: false},
		{value: "Authorization:\u2003x", secretFree: false},
		{value: "sk-abcdefghijklmno-", secretFree: true},
		{value: "sK-abcdefghijklmnop", secretFree: true},
		{value: "sk-abcdefſhijklmnop", secretFree: true},
		{value: "password\u2003=abcdefgh", secretFree: true},
		{value: "x\u2003/root/private", secretFree: true},
	} {
		var secretFree bool
		if err := database.QueryRowContext(ctx, `
SELECT qualification_input_precommit_string_is_secret_free_v1($1)`,
			test.value).Scan(&secretFree); err != nil {
			t.Fatal(err)
		}
		if secretFree != test.secretFree {
			t.Fatalf("SQL secret-free predicate for %q=%t, want %t",
				test.value, secretFree, test.secretFree)
		}
	}

	references := []qualificationinputauthority.AuthorityReference{
		{AuthorityID: uuid.NewString(), AuthorityHash: qualificationPlanMigrationDigest("input-parity-wia")},
		{AuthorityID: uuid.NewString(), AuthorityHash: qualificationPlanMigrationDigest("input-parity-policy")},
		{AuthorityID: uuid.NewString(), AuthorityHash: qualificationPlanMigrationDigest("input-parity-plan")},
	}
	profile := qualificationinputauthority.CredentialProfile{
		Audience:               "urn:worksflow:input-parity",
		AuthorityID:            "credential-authority-v1",
		IssuanceArtifactID:     "credential-issuance-v1",
		MemberRequestSetDigest: qualificationPlanMigrationDigest("input-parity-member-requests"),
		RevocationArtifactID:   "credential-revocation-v1",
	}
	credentialRequest := qualificationinputauthority.CredentialResolutionRequest{
		CredentialProfile: profile,
		CredentialSet: qualificationinputauthority.CredentialSetProjection{
			Audience: profile.Audience, IssuanceArtifactID: profile.IssuanceArtifactID,
			Issuer:               profile.AuthorityID,
			MemberBindingsDigest: qualificationPlanMigrationDigest("input-parity-member-bindings"),
			MemberCount:          2,
			RevocationArtifactID: profile.RevocationArtifactID,
			SetHandleHash:        qualificationPlanMigrationDigest("input-parity-set-handle"),
			SetID:                uuid.NewString(),
		},
		Plan: references[2], Policy: references[1], Resolver: credentialBinding,
		SchemaVersion: qualificationinputauthority.CredentialRequestSchemaV1,
		WorkflowInput: references[0],
	}
	sourceRequest := qualificationinputauthority.SourceVerificationRequest{
		Plan: references[2], Policy: references[1],
		SchemaVersion: qualificationinputauthority.SourceRequestSchemaV1,
		Source: qualificationinputauthority.SourceProjection{
			Commit: strings.Repeat("a", 40), Dirty: false,
			TreeDigest:       qualificationPlanMigrationDigest("input-parity-source-tree"),
			TreeDigestSchema: qualificationinputauthority.SourceTreeDigestSchemaV1,
		},
		SourcePolicyDigest: qualificationPlanMigrationDigest("input-parity-source-policy"),
		Verifier:           sourceBinding,
		WorkflowInput:      references[0],
	}

	type queryRower interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}
	admit := func(
		query queryRower,
		kind string,
		binding qualificationinputauthority.ExecutableBinding,
		requestBytes []byte,
		requestHash string,
		label string,
	) error {
		t.Helper()
		receiptHash := qualificationPlanMigrationDigest("input-parity-receipt-" + label)
		document := qualificationinputauthority.ReceiptAdmission{
			AuthorityID: binding.AuthorityID, ExecutableDigest: binding.ExecutableDigest,
			Kind: kind, ReceiptHash: receiptHash, RequestHash: requestHash,
			SchemaVersion: qualificationinputauthority.ReceiptAdmissionSchemaV1,
		}
		admissionBytes, admissionHash, err := qualificationinputauthority.EncodeReceiptAdmission(document)
		if err != nil {
			t.Fatal(err)
		}
		function := "admit_qualification_input_source_receipt_v1"
		if kind == qualificationinputauthority.ReceiptKindCredential {
			function = "admit_qualification_input_credential_receipt_v1"
		}
		var storedHash string
		return query.QueryRowContext(ctx, `SELECT admission_hash FROM `+function+`(
  $1,$2,$3,$4,$5,$6
)`, requestHash, requestBytes, string(requestBytes), admissionHash,
			admissionBytes, string(admissionBytes)).Scan(&storedHash)
	}
	toMap := func(value any) map[string]any {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(encoded, &document); err != nil {
			t.Fatal(err)
		}
		return document
	}
	assertCredentialRejected := func(label string, mutate func(map[string]any)) {
		t.Helper()
		document := toMap(credentialRequest)
		mutate(document)
		requestBytes, err := qualificationinputauthority.CanonicalJSON(document)
		if err != nil {
			t.Fatal(err)
		}
		requestHash := qualificationinputauthority.DomainHash(
			qualificationinputauthority.CredentialRequestHashDomainV1, requestBytes,
		)
		if err := admit(
			database, qualificationinputauthority.ReceiptKindCredential,
			credentialBinding, requestBytes, requestHash, label,
		); err == nil || (!strings.Contains(err.Error(), "WIP01") && !strings.Contains(err.Error(), "WIP02")) {
			t.Fatalf("malformed credential %s admission error=%v, want fail-closed", label, err)
		}
	}
	assertCredentialRejected("renamed-key", func(document map[string]any) {
		set := document["credentialSet"].(map[string]any)
		set["renamedDigest"] = set["memberBindingsDigest"]
		delete(set, "memberBindingsDigest")
	})
	assertCredentialRejected("nbsp-audience", func(document map[string]any) {
		document["credentialProfile"].(map[string]any)["audience"] = "\u00a0audience"
		document["credentialSet"].(map[string]any)["audience"] = "\u00a0audience"
	})
	assertCredentialRejected("narrow-nbsp-audience", func(document map[string]any) {
		document["credentialProfile"].(map[string]any)["audience"] = "audience\u202f"
		document["credentialSet"].(map[string]any)["audience"] = "audience\u202f"
	})
	assertCredentialRejected("bearer-audience", func(document map[string]any) {
		document["credentialProfile"].(map[string]any)["audience"] = "Bearer abcdefghijklmnop"
		document["credentialSet"].(map[string]any)["audience"] = "Bearer abcdefghijklmnop"
	})
	assertCredentialRejected("provider-artifact", func(document map[string]any) {
		document["credentialProfile"].(map[string]any)["issuanceArtifactId"] = "sk-abcdefghijklmnop"
		document["credentialSet"].(map[string]any)["issuanceArtifactId"] = "sk-abcdefghijklmnop"
	})

	malformedSource := toMap(sourceRequest)
	source := malformedSource["source"].(map[string]any)
	source["renamedCommit"] = source["commit"]
	delete(source, "commit")
	malformedSourceBytes, err := qualificationinputauthority.CanonicalJSON(malformedSource)
	if err != nil {
		t.Fatal(err)
	}
	malformedSourceHash := qualificationinputauthority.DomainHash(
		qualificationinputauthority.SourceRequestHashDomainV1, malformedSourceBytes,
	)
	if err := admit(
		database, qualificationinputauthority.ReceiptKindSource, sourceBinding,
		malformedSourceBytes, malformedSourceHash, "renamed-source-key",
	); err == nil || (!strings.Contains(err.Error(), "WIP01") && !strings.Contains(err.Error(), "WIP02")) {
		t.Fatalf("malformed source admission error=%v, want fail-closed", err)
	}

	credentialBytes, credentialHash, err := qualificationinputauthority.EncodeCredentialRequest(credentialRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := admit(
		database, qualificationinputauthority.ReceiptKindCredential, credentialBinding,
		credentialBytes, credentialHash, "valid-credential",
	); err != nil {
		t.Fatalf("valid credential admission: %v", err)
	}
	var storedCredentialBytes []byte
	if err := database.QueryRowContext(ctx, `
SELECT request_bytes FROM qualification_input_credential_receipt_admissions
WHERE request_hash=$1`, credentialHash).Scan(&storedCredentialBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := qualificationinputauthority.DecodeCredentialRequest(
		storedCredentialBytes, credentialHash,
	); err != nil {
		t.Fatalf("SQL-admitted credential bytes fail Go decode: %v", err)
	}

	sourceBytes, sourceHash, err := qualificationinputauthority.EncodeSourceRequest(sourceRequest)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	var observedGeneration int64
	if err := transaction.QueryRowContext(ctx, `
SELECT generation FROM qualification_input_precommit_executable_binding_heads
WHERE binding_role='source-verification'`).Scan(&observedGeneration); err != nil || observedGeneration != 1 {
		t.Fatalf("observe source binding head generation=%d error=%v", observedGeneration, err)
	}
	sourceBindingV2 := qualificationinputauthority.ExecutableBinding{
		AuthorityID:      "source-verifier-v2",
		ExecutableDigest: qualificationPlanMigrationDigest("input-parity-source-executable-v2"),
	}
	var rotatedGeneration int64
	if err := database.QueryRowContext(ctx, `
SELECT generation FROM review_qualification_input_precommit_executable_binding_v1(
  'source-verification',2,$1,$2,$3
)`, sourceBindingV2.AuthorityID, sourceBindingV2.ExecutableDigest,
		sourceBinding.ExecutableDigest).Scan(&rotatedGeneration); err != nil || rotatedGeneration != 2 {
		t.Fatalf("rotate source binding generation=%d error=%v", rotatedGeneration, err)
	}
	rotationErr := admit(
		transaction, qualificationinputauthority.ReceiptKindSource, sourceBinding,
		sourceBytes, sourceHash, "old-snapshot-source",
	)
	var postgresError *pgconn.PgError
	if rotationErr == nil || (!errors.As(rotationErr, &postgresError) ||
		(postgresError.Code != "40001" && postgresError.Code != "WIP03")) {
		t.Fatalf("old-snapshot source admission error=%v code=%q, want 40001 or WIP03",
			rotationErr, func() string {
				if postgresError == nil {
					return ""
				}
				return postgresError.Code
			}())
	}
	_ = transaction.Rollback()
	var staleRows int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM qualification_input_source_receipt_admissions
WHERE request_hash=$1`, sourceHash).Scan(&staleRows); err != nil || staleRows != 0 {
		t.Fatalf("retired source admission rows=%d error=%v, want none", staleRows, err)
	}
	if err := admit(
		database, qualificationinputauthority.ReceiptKindSource, sourceBinding,
		sourceBytes, sourceHash, "retired-source",
	); err == nil || !strings.Contains(err.Error(), "WIP03") {
		t.Fatalf("retired source binding admission error=%v, want WIP03", err)
	}

	sourceRequest.Verifier = sourceBindingV2
	sourceV2Bytes, sourceV2Hash, err := qualificationinputauthority.EncodeSourceRequest(sourceRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := admit(
		database, qualificationinputauthority.ReceiptKindSource, sourceBindingV2,
		sourceV2Bytes, sourceV2Hash, "current-source-v2",
	); err != nil {
		t.Fatalf("current source binding admission: %v", err)
	}
	var storedSourceBytes []byte
	if err := database.QueryRowContext(ctx, `
SELECT request_bytes FROM qualification_input_source_receipt_admissions
WHERE request_hash=$1`, sourceV2Hash).Scan(&storedSourceBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := qualificationinputauthority.DecodeSourceRequest(storedSourceBytes, sourceV2Hash); err != nil {
		t.Fatalf("SQL-admitted source bytes fail Go decode: %v", err)
	}

	lockedRequest := sourceRequest
	lockedRequest.Source.Commit = strings.Repeat("b", 40)
	lockedRequest.Source.TreeDigest = qualificationPlanMigrationDigest("input-parity-locked-source-tree")
	lockedBytes, lockedHash, err := qualificationinputauthority.EncodeSourceRequest(lockedRequest)
	if err != nil {
		t.Fatal(err)
	}
	lockingTransaction, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockingTransaction.Rollback() }()
	if err := admit(
		lockingTransaction, qualificationinputauthority.ReceiptKindSource,
		sourceBindingV2, lockedBytes, lockedHash, "lock-held-source-v2",
	); err != nil {
		t.Fatalf("admit source while holding generation 2 head: %v", err)
	}
	rotationConnection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rotationConnection.Close()
	var rotationPID int
	if err := rotationConnection.QueryRowContext(ctx, `SELECT pg_catalog.pg_backend_pid()`).Scan(&rotationPID); err != nil {
		t.Fatal(err)
	}
	sourceBindingV3 := qualificationinputauthority.ExecutableBinding{
		AuthorityID:      "source-verifier-v3",
		ExecutableDigest: qualificationPlanMigrationDigest("input-parity-source-executable-v3"),
	}
	rotationResult := make(chan error, 1)
	go func() {
		var generation int64
		err := rotationConnection.QueryRowContext(ctx, `
SELECT generation FROM review_qualification_input_precommit_executable_binding_v1(
  'source-verification',3,$1,$2,$3
)`, sourceBindingV3.AuthorityID, sourceBindingV3.ExecutableDigest,
			sourceBindingV2.ExecutableDigest).Scan(&generation)
		if err == nil && generation != 3 {
			err = errors.New("review returned another generation")
		}
		rotationResult <- err
	}()
	waitDeadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err := base.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM pg_catalog.pg_locks
  WHERE pid=$1 AND NOT granted
)`, rotationPID).Scan(&waiting); err != nil {
			_ = lockingTransaction.Rollback()
			<-rotationResult
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case rotationErr := <-rotationResult:
			_ = lockingTransaction.Rollback()
			t.Fatalf("generation 3 rotation did not wait for the shared head lock: %v", rotationErr)
		default:
		}
		if time.Now().After(waitDeadline) {
			_ = lockingTransaction.Rollback()
			rotationErr := <-rotationResult
			if rotationErr != nil {
				t.Fatalf("generation 3 rotation failed instead of waiting: %v", rotationErr)
			}
			t.Fatal("generation 3 rotation never reached a lock wait")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := lockingTransaction.Commit(); err != nil {
		t.Fatalf("commit generation 2 admission before rotation: %v", err)
	}
	select {
	case rotationErr := <-rotationResult:
		if rotationErr != nil {
			t.Fatalf("generation 3 rotation after lock release: %v", rotationErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("generation 3 rotation remained blocked after admission commit")
	}
	var currentGeneration int64
	if err := database.QueryRowContext(ctx, `
SELECT generation FROM qualification_input_precommit_executable_binding_heads
WHERE binding_role='source-verification'`).Scan(&currentGeneration); err != nil || currentGeneration != 3 {
		t.Fatalf("linearized source head generation=%d error=%v, want 3", currentGeneration, err)
	}
	var lockedAdmissionRows int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM qualification_input_source_receipt_admissions
WHERE request_hash=$1`, lockedHash).Scan(&lockedAdmissionRows); err != nil || lockedAdmissionRows != 1 {
		t.Fatalf("pre-rotation admission rows=%d error=%v, want one", lockedAdmissionRows, err)
	}
	if _, err := database.ExecContext(ctx, `
DELETE FROM qualification_input_precommit_executable_binding_heads
WHERE binding_role='source-verification'`); err == nil || !strings.Contains(err.Error(), "WIP02") {
		t.Fatalf("binding-head delete error=%v, want WIP02", err)
	}
}

func TestQualificationInputPrecommitFreshReplayAndNoBypassPostgresCanary(t *testing.T) {
	ctx, base, dsn := qualificationReceiptV3Postgres(t)
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_input_precommit_fresh_")
	applyQualificationInputPrecommitPrefix(t, ctx, database, false)
	if _, err := database.ExecContext(ctx, `
ALTER TABLE workflow_runs DROP CONSTRAINT workflow_runs_status_check;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_status_check CHECK (
  status IN ('pending','ready','running','waiting_input','waiting_review',
             'waiting_qualification','completed','failed','cancelled','stale')
);
ALTER TABLE workflow_node_runs DROP CONSTRAINT workflow_node_runs_status_check;
ALTER TABLE workflow_node_runs ADD CONSTRAINT workflow_node_runs_status_check CHECK (
  status IN ('pending','ready','running','waiting_input','waiting_review',
             'waiting_qualification','completed','failed','cancelled','stale')
)`); err != nil {
		t.Fatalf("install input-precommit fixture status vocabulary: %v", err)
	}

	wia := seedWorkflowInputCanary(t, ctx, database)
	plan := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{
		inputAuthorityID: wia.authorityID,
	})
	bindQualificationPlanToWorkflowInput(t, &plan, wia)
	bindWorkflowInputToEmptyPromotionPolicy(t, ctx, database, &wia, &plan)
	activateWorkflowInputForInputPrecommit(t, ctx, database, wia)
	if err := freezeQualificationPlanMigrationFixture(ctx, database, plan); err != nil {
		t.Fatalf("freeze input-precommit Plan: %v", err)
	}
	fixture := issueQualificationInputPrecommitForPromotion(t, ctx, database, wia, plan)
	assertQualificationInputAdmissionFirstCommit(t, ctx, database, qualificationinputauthority.ReceiptKindSource)
	assertQualificationInputAdmissionFirstCommit(t, ctx, database, qualificationinputauthority.ReceiptKindCredential)

	var exact bool
	if err := database.QueryRowContext(ctx, `
SELECT qualification_input_precommit_authority_record_is_exact_v1($1)`,
		fixture.authorityID).Scan(&exact); err != nil || !exact {
		t.Fatalf("input-precommit exact=%t error=%v", exact, err)
	}
	issueQualificationInputPrecommitFixture(t, ctx, database, fixture)
	singleUse := fixture
	singleUse.operationID, singleUse.authorityID = uuid.New(), uuid.New()
	requestDocument, err := qualificationinputauthority.DecodeIssueRequest(
		fixture.request.bytes, fixture.request.hash,
	)
	if err != nil {
		t.Fatal(err)
	}
	requestDocument.OperationID = singleUse.operationID.String()
	requestDocument.AuthorityID = singleUse.authorityID.String()
	singleUse.request.bytes, singleUse.request.hash, err =
		qualificationinputauthority.EncodeIssueRequest(requestDocument)
	if err != nil {
		t.Fatal(err)
	}
	singleUse.request.document = string(singleUse.request.bytes)
	authorityDocument, err := qualificationinputauthority.DecodeAuthority(
		fixture.authority.bytes, fixture.authority.hash,
	)
	if err != nil {
		t.Fatal(err)
	}
	authorityDocument.OperationID = singleUse.operationID.String()
	authorityDocument.AuthorityID = singleUse.authorityID.String()
	authorityDocument.RequestHash = singleUse.request.hash
	singleUse.authority.bytes, singleUse.authority.hash, err =
		qualificationinputauthority.EncodeAuthority(authorityDocument)
	if err != nil {
		t.Fatal(err)
	}
	singleUse.authority.document = string(singleUse.authority.bytes)
	singleUseTx, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	_, singleUseErr := singleUseTx.ExecContext(ctx, `
SELECT * FROM issue_qualification_input_precommit_v1(
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17
)`, singleUse.operationID, singleUse.authorityID, singleUse.wiaID, singleUse.policyID, singleUse.planID,
		singleUse.request.hash, singleUse.request.bytes, singleUse.request.document,
		singleUse.source.hash, singleUse.source.bytes, singleUse.source.document,
		singleUse.credential.hash, singleUse.credential.bytes, singleUse.credential.document,
		singleUse.authority.hash, singleUse.authority.bytes, singleUse.authority.document)
	_ = singleUseTx.Rollback()
	if singleUseErr == nil || !strings.Contains(singleUseErr.Error(), "WIP02") {
		t.Fatalf("WIA/Plan single-use error=%v, want WIP02", singleUseErr)
	}

	transaction, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	changedAuthorityID := uuid.New()
	_, changedErr := transaction.ExecContext(ctx, `
SELECT * FROM issue_qualification_input_precommit_v1(
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17
)`, fixture.operationID, changedAuthorityID, fixture.wiaID, fixture.policyID, fixture.planID,
		fixture.request.hash, fixture.request.bytes, fixture.request.document,
		fixture.source.hash, fixture.source.bytes, fixture.source.document,
		fixture.credential.hash, fixture.credential.bytes, fixture.credential.document,
		fixture.authority.hash, fixture.authority.bytes, fixture.authority.document)
	_ = transaction.Rollback()
	if changedErr == nil || !strings.Contains(changedErr.Error(), "WIP02") {
		t.Fatalf("changed input-precommit replay error=%v, want WIP02", changedErr)
	}

	invalidTx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	invalidRequestHash := qualificationPlanMigrationDigest("invalid-direct-source-request")
	_, invalidErr := invalidTx.ExecContext(ctx, `
INSERT INTO qualification_input_source_receipt_admissions(
  request_hash,request_bytes,request_document,
  admission_hash,admission_bytes,admission_document,
  authority_id,executable_digest,receipt_hash,admitted_at
) VALUES($1,convert_to('{}','UTF8'),'{}',$2,convert_to('{}','UTF8'),'{}',
  'source-verifier-v1',$3,$4,date_trunc('milliseconds',clock_timestamp()))`,
		invalidRequestHash, qualificationPlanMigrationDigest("invalid-direct-source-admission"),
		qualificationPlanMigrationDigest("input-source-verifier-executable"),
		qualificationPlanMigrationDigest("invalid-direct-source-receipt"))
	if invalidErr != nil {
		_ = invalidTx.Rollback()
		t.Fatalf("stage direct-DML deferred closure probe: %v", invalidErr)
	}
	if err := invalidTx.Commit(); err == nil || !strings.Contains(err.Error(), "WIP02") {
		t.Fatalf("direct-DML closure commit error=%v, want WIP02", err)
	}

	for _, table := range []string{
		"qualification_input_precommit_executable_binding_generations",
		"qualification_input_precommit_executable_binding_heads",
		"qualification_input_precommit_authorities",
		"qualification_input_source_receipt_admissions",
		"qualification_input_credential_receipt_admissions",
	} {
		var publicPrivileges int
		if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_catalog.pg_class AS relation
CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(
  relation.relacl, pg_catalog.acldefault('r',relation.relowner)
)) AS privilege
WHERE relation.relnamespace=current_schema()::regnamespace
  AND relation.relname=$1 AND privilege.grantee=0`, table).Scan(&publicPrivileges); err != nil {
			t.Fatal(err)
		}
		if publicPrivileges != 0 {
			t.Fatalf("PUBLIC unexpectedly has a direct privilege on %s", table)
		}
	}

	down, err := files.ReadFile("000080_qualification_input_precommit_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err == nil ||
		!strings.Contains(err.Error(), "immutable authority history exists") {
		t.Fatalf("nonempty input-precommit rollback error=%v", err)
	}
}

func activateWorkflowInputForInputPrecommit(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture workflowInputCanary,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := freezeWorkflowInputCanary(ctx, transaction, fixture); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("freeze Workflow Input for input-precommit: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE workflow_node_runs
SET status='waiting_qualification',input_authority_id=$2 WHERE id=$1`,
		fixture.gateNodeID, fixture.authorityID); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("attach input-precommit WIA: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO workflow_run_events(
  id,run_id,sequence,event_type,node_key,payload
) VALUES($1,$2,6,'external_qualification_activated','external-qualification',$3)`,
		fixture.eventID, fixture.runID,
		`{"inputAuthorityId":"`+fixture.authorityID.String()+`","nodeRunId":"`+fixture.gateNodeID.String()+`"}`); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("append input-precommit WIA activation: %v", err)
	}
	if err := insertWorkflowInputActivationOutbox(ctx, transaction, fixture); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("append input-precommit WIA outbox: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE workflow_runs
SET status='waiting_qualification',event_cursor=6 WHERE id=$1`, fixture.runID); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("advance input-precommit WIA run: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit input-precommit WIA activation: %v", err)
	}
}

func applyQualificationInputPrecommitPrefix(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	includeProfileV3 bool,
) {
	t.Helper()
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `CREATE TABLE schema_migrations (
  version text PRIMARY KEY, checksum text NOT NULL, down_checksum text,
  applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		t.Fatal(err)
	}
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name > qualificationInputPrecommitMigration {
			break
		}
		if !includeProfileV3 && name == workflowExecutionProfileV3Migration {
			continue
		}
		if err := applyFile(ctx, connection, name); err != nil {
			var postgresError *pgconn.PgError
			if errors.As(err, &postgresError) {
				t.Fatalf("apply input-precommit prerequisite %s at position %d: %v", name, postgresError.Position, err)
			}
			t.Fatalf("apply input-precommit prerequisite %s: %v", name, err)
		}
	}
}

func issueQualificationInputPrecommitForPromotion(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	wia workflowInputCanary,
	plan qualificationPlanMigrationFixture,
) qualificationInputPrecommitFixture {
	t.Helper()
	sourceBinding := qualificationinputauthority.ExecutableBinding{
		AuthorityID:      "source-verifier-v1",
		ExecutableDigest: qualificationPlanMigrationDigest("input-source-verifier-executable"),
	}
	credentialBinding := qualificationinputauthority.ExecutableBinding{
		AuthorityID:      "credential-resolver-v1",
		ExecutableDigest: qualificationPlanMigrationDigest("input-credential-resolver-executable"),
	}
	for _, binding := range []struct {
		role  string
		value qualificationinputauthority.ExecutableBinding
	}{
		{"source-verification", sourceBinding},
		{"credential-resolution", credentialBinding},
	} {
		var generation int64
		if err := database.QueryRowContext(ctx, `
SELECT generation FROM review_qualification_input_precommit_executable_binding_v1(
  $1,1,$2,$3,NULL
)`, binding.role, binding.value.AuthorityID, binding.value.ExecutableDigest).Scan(&generation); err != nil {
			t.Fatalf("review input-precommit %s binding: %v", binding.role, err)
		}
		if generation != 1 {
			t.Fatalf("reviewed input-precommit %s generation=%d, want 1", binding.role, generation)
		}
	}

	var wiaHash, wiaInputHash, policyHash, policyPlanInputHash, sourcePolicyDigest string
	var planHash, planInputHash string
	var planInputAuthorityID uuid.UUID
	var credentialProfileRaw, sourceRaw, credentialSetRaw []byte
	if err := database.QueryRowContext(ctx, `
SELECT authority_hash,input_hash
FROM workflow_input_authorities WHERE authority_id=$1`, wia.authorityID,
	).Scan(&wiaHash, &wiaInputHash); err != nil {
		t.Fatalf("resolve input-precommit WIA: %v", err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT authority_hash,plan_input_profile_hash,
       plan_input_profile_document->>'sourcePolicyDigest',
       plan_input_profile_document->'credentialProfile'
FROM qualification_policy_authorities WHERE authority_id=$1`, wia.policyID,
	).Scan(&policyHash, &policyPlanInputHash, &sourcePolicyDigest, &credentialProfileRaw); err != nil {
		t.Fatalf("resolve input-precommit Policy: %v", err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT envelope_hash,input_hash,input_authority_id,
       input_document->'source',input_document->'credential'
FROM qualification_plan_authorities WHERE authority_id=$1`, plan.authorityID,
	).Scan(&planHash, &planInputHash, &planInputAuthorityID, &sourceRaw, &credentialSetRaw); err != nil {
		t.Fatalf("resolve input-precommit Plan: %v", err)
	}
	var profile qualificationinputauthority.CredentialProfile
	var source qualificationinputauthority.SourceProjection
	var credentialSet qualificationinputauthority.CredentialSetProjection
	for name, decode := range map[string]struct {
		raw    []byte
		target any
	}{
		"credential profile": {credentialProfileRaw, &profile},
		"source":             {sourceRaw, &source},
		"credential set":     {credentialSetRaw, &credentialSet},
	} {
		if err := json.Unmarshal(decode.raw, decode.target); err != nil {
			t.Fatalf("decode input-precommit %s: %v", name, err)
		}
	}
	resolved := qualificationinputauthority.ResolvedAuthorities{
		WorkflowInput: qualificationinputauthority.WorkflowInputBinding{
			AuthorityHash:                    wiaHash,
			AuthorityID:                      wia.authorityID.String(),
			InputHash:                        wiaInputHash,
			QualificationPolicyAuthorityHash: policyHash,
			QualificationPolicyAuthorityID:   wia.policyID.String(),
		},
		Policy: qualificationinputauthority.PolicyBinding{
			AuthorityHash:        policyHash,
			AuthorityID:          wia.policyID.String(),
			CredentialProfile:    profile,
			PlanInputProfileHash: policyPlanInputHash,
			SourcePolicyDigest:   sourcePolicyDigest,
		},
		PolicyCurrent:      true,
		PolicyStatus:       qualificationinputauthority.PolicyStatusActive,
		SourceVerifier:     sourceBinding,
		CredentialResolver: credentialBinding,
		Plan: qualificationinputauthority.PlanBinding{
			AuthorityHash:    planHash,
			AuthorityID:      plan.authorityID.String(),
			CredentialSet:    credentialSet,
			InputAuthorityID: planInputAuthorityID.String(),
			InputHash:        planInputHash,
			Source:           source,
		},
	}
	if err := qualificationinputauthority.ValidateResolvedAuthorities(resolved); err != nil {
		t.Fatalf("resolved input-precommit fixture is invalid: %v", err)
	}

	sourceRequest := qualificationinputauthority.SourceVerificationRequest{
		Plan: qualificationinputauthority.AuthorityReference{
			AuthorityHash: resolved.Plan.AuthorityHash, AuthorityID: resolved.Plan.AuthorityID,
		},
		Policy: qualificationinputauthority.AuthorityReference{
			AuthorityHash: resolved.Policy.AuthorityHash, AuthorityID: resolved.Policy.AuthorityID,
		},
		SchemaVersion:      qualificationinputauthority.SourceRequestSchemaV1,
		Source:             resolved.Plan.Source,
		SourcePolicyDigest: resolved.Policy.SourcePolicyDigest,
		Verifier:           sourceBinding,
		WorkflowInput: qualificationinputauthority.AuthorityReference{
			AuthorityHash: resolved.WorkflowInput.AuthorityHash,
			AuthorityID:   resolved.WorkflowInput.AuthorityID,
		},
	}
	sourceBytes, sourceHash, err := qualificationinputauthority.EncodeSourceRequest(sourceRequest)
	if err != nil {
		t.Fatal(err)
	}
	credentialRequest := qualificationinputauthority.CredentialResolutionRequest{
		CredentialProfile: resolved.Policy.CredentialProfile,
		CredentialSet:     resolved.Plan.CredentialSet,
		Plan: qualificationinputauthority.AuthorityReference{
			AuthorityHash: resolved.Plan.AuthorityHash, AuthorityID: resolved.Plan.AuthorityID,
		},
		Policy: qualificationinputauthority.AuthorityReference{
			AuthorityHash: resolved.Policy.AuthorityHash, AuthorityID: resolved.Policy.AuthorityID,
		},
		Resolver:      credentialBinding,
		SchemaVersion: qualificationinputauthority.CredentialRequestSchemaV1,
		WorkflowInput: qualificationinputauthority.AuthorityReference{
			AuthorityHash: resolved.WorkflowInput.AuthorityHash,
			AuthorityID:   resolved.WorkflowInput.AuthorityID,
		},
	}
	credentialBytes, credentialHash, err := qualificationinputauthority.EncodeCredentialRequest(credentialRequest)
	if err != nil {
		t.Fatal(err)
	}
	type admittedProof struct {
		admissionHash    string
		authorityID      string
		executableDigest string
		receiptHash      string
		requestHash      string
	}
	admit := func(
		kind string,
		binding qualificationinputauthority.ExecutableBinding,
		requestHash string,
		requestBytes []byte,
		receiptHash string,
	) admittedProof {
		t.Helper()
		document := qualificationinputauthority.ReceiptAdmission{
			AuthorityID:      binding.AuthorityID,
			ExecutableDigest: binding.ExecutableDigest,
			Kind:             kind,
			ReceiptHash:      receiptHash,
			RequestHash:      requestHash,
			SchemaVersion:    qualificationinputauthority.ReceiptAdmissionSchemaV1,
		}
		admissionBytes, admissionHash, encodeErr := qualificationinputauthority.EncodeReceiptAdmission(document)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		function := "admit_qualification_input_source_receipt_v1"
		if kind == qualificationinputauthority.ReceiptKindCredential {
			function = "admit_qualification_input_credential_receipt_v1"
		}
		var proof admittedProof
		query := `SELECT admission_hash,authority_id,executable_digest,receipt_hash,request_hash
FROM ` + function + `($1,$2,$3,$4,$5,$6)`
		if queryErr := database.QueryRowContext(
			ctx, query, requestHash, requestBytes, string(requestBytes),
			admissionHash, admissionBytes, string(admissionBytes),
		).Scan(
			&proof.admissionHash, &proof.authorityID, &proof.executableDigest,
			&proof.receiptHash, &proof.requestHash,
		); queryErr != nil {
			var postgresError *pgconn.PgError
			if errors.As(queryErr, &postgresError) {
				t.Fatalf(
					"admit input-precommit %s receipt: %v detail=%q where=%q",
					kind, queryErr, postgresError.Detail, postgresError.Where,
				)
			}
			t.Fatalf("admit input-precommit %s receipt: %v", kind, queryErr)
		}
		return proof
	}
	sourceProof := admit(
		qualificationinputauthority.ReceiptKindSource, sourceBinding,
		sourceHash, sourceBytes, qualificationPlanMigrationDigest("input-source-receipt-"+plan.authorityID.String()),
	)
	credentialProof := admit(
		qualificationinputauthority.ReceiptKindCredential, credentialBinding,
		credentialHash, credentialBytes,
		qualificationPlanMigrationDigest("input-credential-receipt-"+plan.authorityID.String()),
	)

	operationID, authorityID := uuid.New(), uuid.New()
	requestDocument := qualificationinputauthority.IssueRequest{
		AuthorityID:                    authorityID.String(),
		OperationID:                    operationID.String(),
		QualificationPlanAuthorityID:   plan.authorityID.String(),
		QualificationPolicyAuthorityID: wia.policyID.String(),
		SchemaVersion:                  qualificationinputauthority.IssueRequestSchemaV1,
		WorkflowInputAuthorityID:       wia.authorityID.String(),
	}
	requestBytes, requestHash, err := qualificationinputauthority.EncodeIssueRequest(requestDocument)
	if err != nil {
		t.Fatal(err)
	}
	var issuedAt time.Time
	if err := database.QueryRowContext(
		ctx, `SELECT date_trunc('milliseconds',clock_timestamp())`,
	).Scan(&issuedAt); err != nil {
		t.Fatal(err)
	}
	authorityDocument := qualificationinputauthority.AuthorityDocument{
		AuthorityID: authorityID.String(),
		CredentialProof: qualificationinputauthority.VerificationProof{
			AdmissionHash: credentialProof.admissionHash, AuthorityID: credentialProof.authorityID,
			ExecutableDigest: credentialProof.executableDigest, ReceiptHash: credentialProof.receiptHash,
			RequestHash: credentialProof.requestHash,
		},
		CredentialRequestHash: credentialHash,
		IssuedAt:              issuedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		OperationID:           operationID.String(),
		Plan:                  resolved.Plan,
		Policy:                resolved.Policy,
		RequestHash:           requestHash,
		SchemaVersion:         qualificationinputauthority.AuthoritySchemaV1,
		SourceProof: qualificationinputauthority.VerificationProof{
			AdmissionHash: sourceProof.admissionHash, AuthorityID: sourceProof.authorityID,
			ExecutableDigest: sourceProof.executableDigest, ReceiptHash: sourceProof.receiptHash,
			RequestHash: sourceProof.requestHash,
		},
		SourceRequestHash: sourceHash,
		WorkflowInput:     resolved.WorkflowInput,
	}
	authorityBytes, authorityHash, err := qualificationinputauthority.EncodeAuthority(authorityDocument)
	if err != nil {
		t.Fatal(err)
	}
	fixture := qualificationInputPrecommitFixture{
		operationID: operationID,
		authorityID: authorityID,
		authority: qualificationInputPrecommitMaterial{
			hash: authorityHash, bytes: authorityBytes, document: string(authorityBytes),
		},
		request: qualificationInputPrecommitMaterial{
			hash: requestHash, bytes: requestBytes, document: string(requestBytes),
		},
		source: qualificationInputPrecommitMaterial{
			hash: sourceHash, bytes: sourceBytes, document: string(sourceBytes),
		},
		credential: qualificationInputPrecommitMaterial{
			hash: credentialHash, bytes: credentialBytes, document: string(credentialBytes),
		},
		wiaID: wia.authorityID, policyID: wia.policyID, planID: plan.authorityID,
	}
	issueQualificationInputPrecommitFixture(t, ctx, database, fixture)
	return fixture
}

func issueQualificationInputPrecommitFixture(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture qualificationInputPrecommitFixture,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	var authorityHash string
	if err := transaction.QueryRowContext(ctx, `
SELECT authority_hash FROM issue_qualification_input_precommit_v1(
  $1,$2,$3,$4,$5,
  $6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17
)`,
		fixture.operationID, fixture.authorityID, fixture.wiaID, fixture.policyID, fixture.planID,
		fixture.request.hash, fixture.request.bytes, fixture.request.document,
		fixture.source.hash, fixture.source.bytes, fixture.source.document,
		fixture.credential.hash, fixture.credential.bytes, fixture.credential.document,
		fixture.authority.hash, fixture.authority.bytes, fixture.authority.document,
	).Scan(&authorityHash); err != nil {
		t.Fatalf("issue Qualification Input Precommit: %v", err)
	}
	if authorityHash != fixture.authority.hash {
		t.Fatalf("issued input-precommit hash=%q, want %q", authorityHash, fixture.authority.hash)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit Qualification Input Precommit: %v", err)
	}
}

func assertQualificationInputAdmissionFirstCommit(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	kind string,
) {
	t.Helper()
	sourceBinding := qualificationinputauthority.ExecutableBinding{
		AuthorityID:      "source-verifier-v1",
		ExecutableDigest: qualificationPlanMigrationDigest("input-source-verifier-executable"),
	}
	credentialBinding := qualificationinputauthority.ExecutableBinding{
		AuthorityID:      "credential-resolver-v1",
		ExecutableDigest: qualificationPlanMigrationDigest("input-credential-resolver-executable"),
	}
	references := []qualificationinputauthority.AuthorityReference{
		{AuthorityID: uuid.NewString(), AuthorityHash: qualificationPlanMigrationDigest(kind + "-race-wia")},
		{AuthorityID: uuid.NewString(), AuthorityHash: qualificationPlanMigrationDigest(kind + "-race-policy")},
		{AuthorityID: uuid.NewString(), AuthorityHash: qualificationPlanMigrationDigest(kind + "-race-plan")},
	}
	var requestBytes []byte
	var requestHash string
	var binding qualificationinputauthority.ExecutableBinding
	var function string
	var err error
	if kind == qualificationinputauthority.ReceiptKindSource {
		binding = sourceBinding
		function = "admit_qualification_input_source_receipt_v1"
		requestBytes, requestHash, err = qualificationinputauthority.EncodeSourceRequest(
			qualificationinputauthority.SourceVerificationRequest{
				Plan: references[2], Policy: references[1],
				SchemaVersion: qualificationinputauthority.SourceRequestSchemaV1,
				Source: qualificationinputauthority.SourceProjection{
					Commit: strings.Repeat("a", 40), Dirty: false,
					TreeDigest:       qualificationPlanMigrationDigest(kind + "-race-tree"),
					TreeDigestSchema: qualificationinputauthority.SourceTreeDigestSchemaV1,
				},
				SourcePolicyDigest: qualificationPlanMigrationDigest(kind + "-race-policy-digest"),
				Verifier:           binding,
				WorkflowInput:      references[0],
			},
		)
	} else {
		binding = credentialBinding
		function = "admit_qualification_input_credential_receipt_v1"
		profile := qualificationinputauthority.CredentialProfile{
			Audience: "urn:worksflow:input-race", AuthorityID: "credential-authority-v1",
			IssuanceArtifactID:     "credential-issuance",
			MemberRequestSetDigest: qualificationPlanMigrationDigest(kind + "-race-member-requests"),
			RevocationArtifactID:   "credential-revocation",
		}
		requestBytes, requestHash, err = qualificationinputauthority.EncodeCredentialRequest(
			qualificationinputauthority.CredentialResolutionRequest{
				CredentialProfile: profile,
				CredentialSet: qualificationinputauthority.CredentialSetProjection{
					Audience: profile.Audience, IssuanceArtifactID: profile.IssuanceArtifactID,
					Issuer:               profile.AuthorityID,
					MemberBindingsDigest: qualificationPlanMigrationDigest(kind + "-race-member-bindings"),
					MemberCount:          2, RevocationArtifactID: profile.RevocationArtifactID,
					SetHandleHash: qualificationPlanMigrationDigest(kind + "-race-set-handle"),
					SetID:         uuid.NewString(),
				},
				Plan: references[2], Policy: references[1], Resolver: binding,
				SchemaVersion: qualificationinputauthority.CredentialRequestSchemaV1,
				WorkflowInput: references[0],
			},
		)
	}
	if err != nil {
		t.Fatal(err)
	}
	type observation struct {
		admissionHash string
		receiptHash   string
		err           error
	}
	results := make(chan observation, 2)
	candidates := make([]observation, 0, 2)
	for index := range 2 {
		receiptHash := qualificationPlanMigrationDigest(kind + "-race-receipt-" + string(rune('a'+index)))
		document := qualificationinputauthority.ReceiptAdmission{
			AuthorityID: binding.AuthorityID, ExecutableDigest: binding.ExecutableDigest,
			Kind: kind, ReceiptHash: receiptHash, RequestHash: requestHash,
			SchemaVersion: qualificationinputauthority.ReceiptAdmissionSchemaV1,
		}
		admissionBytes, admissionHash, encodeErr := qualificationinputauthority.EncodeReceiptAdmission(document)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		candidates = append(candidates, observation{admissionHash: admissionHash, receiptHash: receiptHash})
		go func() {
			var result observation
			result.err = database.QueryRowContext(ctx, `SELECT admission_hash,receipt_hash FROM `+function+`(
  $1,$2,$3,$4,$5,$6
)`, requestHash, requestBytes, string(requestBytes), admissionHash, admissionBytes, string(admissionBytes)).Scan(
				&result.admissionHash, &result.receiptHash,
			)
			results <- result
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("%s concurrent admission errors first=%v second=%v", kind, first.err, second.err)
	}
	if first != second {
		t.Fatalf("%s concurrent admission did not converge first=%+v second=%+v", kind, first, second)
	}
	if first != candidates[0] && first != candidates[1] {
		t.Fatalf("%s admission winner was not either committed observation: %+v", kind, first)
	}
	resolver := "resolve_qualification_input_source_receipt_admission_v1"
	if kind == qualificationinputauthority.ReceiptKindCredential {
		resolver = "resolve_qualification_input_credential_receipt_admission_v1"
	}
	var recoveredRequestHash string
	if err := database.QueryRowContext(ctx,
		`SELECT request_hash FROM `+resolver+`($1)`, first.admissionHash,
	).Scan(&recoveredRequestHash); err != nil || recoveredRequestHash != requestHash {
		t.Fatalf("%s admission-hash recovery request=%q error=%v", kind, recoveredRequestHash, err)
	}
}
