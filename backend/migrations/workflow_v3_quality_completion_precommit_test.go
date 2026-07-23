package migrations

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/workflowinputauthority"
	"github.com/worksflow/builder/backend/internal/workflowqualificationactivation"
)

const workflowV3QualityCompletionPrecommitMigration = "000085_workflow_v3_quality_completion_precommit.up.sql"

func TestWorkflowV3QualityCompletionPrecommitMigrationDeclaresClosedBoundary(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(workflowV3QualityCompletionPrecommitMigration)
	if err != nil {
		t.Fatal(err)
	}
	upText := string(up)

	assertWorkflowV3QualityFunctionABI(t, upText,
		"precommit_workflow_v3_quality_completion_v1",
		[]string{
			"uuid", "uuid", "uuid", "uuid", "uuid", "uuid", "uuid", "bigint",
			"uuid", "text", "integer", "timestamptz", "jsonb", "uuid", "bytea",
		},
		nil,
		"setof workflow_v3_quality_completion_precommits",
	)
	assertWorkflowV3QualityPrecommitTableABI(t, upText)
	assertWorkflowV3QualityFunctionABI(t, upText,
		"inspect_workflow_v3_quality_completion_precommit_v1",
		[]string{"uuid"},
		nil,
		"setof workflow_v3_quality_completion_precommits",
	)
	assertWorkflowV3QualityFunctionABI(t, upText,
		"admit_workflow_v3_quality_completion_materials_v1",
		[]string{"uuid", "uuid", "bytea", "bytea", "bytea", "bytea", "bytea", "jsonb"},
		nil,
		"void",
	)
	assertWorkflowV3QualityFunctionABI(t, upText,
		"resolve_workflow_v3_quality_completion_material_plan_v1",
		[]string{"uuid", "uuid", "bytea"}, nil, "jsonb",
	)
	assertWorkflowV3QualityFunctionABI(t, upText,
		"resolve_workflow_v3_quality_completion_candidate_v1",
		[]string{"uuid"},
		[]workflowV3QualityABIColumn{
			{name: "classification", dataType: "text"},
			{name: "completion_event_id", dataType: "uuid"},
			{name: "precommit_id", dataType: "uuid"},
			{name: "freeze_request_hash", dataType: "text"},
			{name: "freeze_request_bytes", dataType: "bytea"},
			{name: "workflow_input_hash", dataType: "text"},
			{name: "workflow_input_bytes", dataType: "bytea"},
			{name: "freeze_candidate_bytes", dataType: "bytea"},
			{name: "definition_raw_bytes", dataType: "bytea"},
			{name: "run_scope_raw_bytes", dataType: "bytea"},
			{name: "node_input_raw_bytes", dataType: "bytea"},
			{name: "build_manifest_raw_bytes", dataType: "bytea"},
			{name: "build_contract_raw_bytes", dataType: "bytea"},
			{name: "material_bundle", dataType: "jsonb"},
			{name: "snapshot_hash", dataType: "text"},
			{name: "retained_raw_bytes_size", dataType: "bigint"},
		},
		"",
	)
	assertWorkflowV3QualityFunctionABI(t, upText,
		"freeze_workflow_input_authority_from_quality_precommit_v1",
		[]string{
			"uuid", "uuid", "uuid", "uuid", "bigint", "uuid", "bigint",
			"bytea", "bytea", "bytea", "bytea", "bytea", "jsonb",
		},
		nil,
		"setof workflow_input_authorities",
	)

	for _, required := range []string{
		"pg_advisory_xact_lock",
		"workflow_v3_quality_completion_precommits",
		"workflow_v3_quality_completion_materials",
		"workflow_v3_quality_completion_identity_reservations",
		"workflow_v3_quality_completion_material_manifests",
		"workflow_v3_quality_completion_material_revisions",
		"workflow_v3_quality_completion_material_review_receipts",
		"workflow_v3_quality_completion_candidate_snapshots",
		"CREATE FUNCTION workflow_v3_quality_completion_material_bundle_v1(",
		"CREATE FUNCTION workflow_v3_quality_completion_snapshot_is_exact_v1(",
		"resolve_workflow_v3_quality_completion_material_plan_v1",
		"completion_event_id",
		"freeze_request_hash",
		"freeze_request_bytes",
		"workflow_input_hash",
		"workflow_input_bytes",
		"freeze_candidate_bytes",
		"definition_raw_bytes",
		"run_scope_raw_bytes",
		"node_input_raw_bytes",
		"build_manifest_raw_bytes",
		"build_contract_raw_bytes",
		"material_bundle",
		"snapshot_hash",
		"retained_raw_bytes_size",
		"worksflow.workflow-v3-quality-completion.candidate-snapshot/v1",
		"workflow_input_authority_hash(",
		"workflow_input_canonical_jsonb_bytes(",
		"'schemaVersion'",
		"'precommitId'",
		"'completionEventId'",
		"'workflowInputOperationId'",
		"'workflowInputAuthorityId'",
		"'activationEventId'",
		"'freezeRequestHash'",
		"'workflowInputHash'",
		"'freezeCandidateRawHash'",
		"'definitionRawHash'",
		"'runScopeRawHash'",
		"'nodeInputRawHash'",
		"'buildManifestRawHash'",
		"'buildContractRawHash'",
		"'inputManifestCount'",
		"'revisionCount'",
		"'reviewReceiptCount'",
		"'materialBundleHash'",
		"'retainedRawBytesSize'",
		"'inputManifests'",
		"'revisions'",
		"'reviewReceipts'",
		"'rawBytesHex'",
		"'rawBytesHash'",
		"'rawBytesSize'",
		"'target'",
		"'non-target'",
		"WQC01",
		"WQC02",
		"WQC03",
		"WQC04",
		"DEFERRABLE INITIALLY DEFERRED",
		"SECURITY DEFINER",
		"SET search_path TO pg_catalog",
		"REVOKE ALL ON FUNCTION",
		"FROM PUBLIC",
		"worksflow_application",
		"worksflow_workflow_input_authority_operator",
		"000085 granted schema USAGE to worksflow_workflow_input_authority_operator",
	} {
		if !strings.Contains(upText, required) {
			t.Fatalf("Quality completion precommit migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"CREATE ROLE",
		"session_replication_role",
		"ON DELETE CASCADE",
		"GRANT SELECT ON TABLE",
		"GRANT INSERT ON TABLE",
		"GRANT UPDATE ON TABLE",
		"GRANT DELETE ON TABLE",
	} {
		if strings.Contains(upText, forbidden) {
			t.Fatalf("Quality completion precommit migration unexpectedly contains %q", forbidden)
		}
	}

	down, err := files.ReadFile("000085_workflow_v3_quality_completion_precommit.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downText := string(down)
	for _, required := range []string{
		"workflow_v3_quality_completion_precommits",
		"cannot roll back",
		"DROP FUNCTION IF EXISTS freeze_workflow_input_authority_from_quality_precommit_v1",
		"DROP FUNCTION IF EXISTS resolve_workflow_v3_quality_completion_candidate_v1",
		"DROP FUNCTION IF EXISTS resolve_workflow_v3_quality_completion_material_plan_v1",
		"DROP FUNCTION IF EXISTS admit_workflow_v3_quality_completion_materials_v1",
		"DROP FUNCTION IF EXISTS inspect_workflow_v3_quality_completion_precommit_v1",
		"DROP FUNCTION IF EXISTS precommit_workflow_v3_quality_completion_v1",
		"DROP TABLE IF EXISTS workflow_v3_quality_completion_materials",
		"DROP TABLE IF EXISTS workflow_v3_quality_completion_precommits",
		"GRANT EXECUTE ON FUNCTION",
		"freeze_workflow_input_authority_v1",
		"worksflow_application",
		"000085 granted schema USAGE to worksflow_workflow_input_authority_operator",
	} {
		if !strings.Contains(downText, required) {
			t.Fatalf("Quality completion precommit rollback is missing %q", required)
		}
	}
}

func TestWorkflowV3QualityCompletionResolverClassificationAndACLPostgresCanary(t *testing.T) {
	ctx, base, dsn := qualificationReceiptV3Postgres(t)
	ensureQualificationReleaseHardenedRoles(t, ctx, base)
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "quality_completion_precommit_")
	applyPostgresMigrationsForCanary(t, database)

	assertWorkflowV3QualityCompletionACL(t, ctx, database)
	resolverDatabase := workflowV3QualityRoleDatabase(
		t, ctx, base, database, dsn, "worksflow_workflow_input_authority_operator", "quality_resolver_",
	)

	missingEventID := uuid.New()
	if _, err := resolveWorkflowV3QualityCompletionWire(ctx, resolverDatabase, missingEventID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing completion event resolver error = %v, want sql.ErrNoRows", err)
	}

	definition, actorID := workflowExecutionProfileV3MigrationDefinition(t)
	_, _, runID, _ := seedWorkflowExecutionProfileV3Run(t, ctx, database, definition, actorID)
	nonTargetEventID := insertWorkflowV3QualityCompletionEvent(
		t, ctx, database, runID, actorID, 1, "run.started", "", false,
	)
	nonTarget, err := resolveWorkflowV3QualityCompletionWire(ctx, resolverDatabase, nonTargetEventID)
	if err != nil {
		t.Fatalf("resolve committed non-target event: %v", err)
	}
	if nonTarget.classification != "non-target" || nonTarget.completionEventID != nonTargetEventID.String() {
		t.Fatalf("non-target classification/event = %q/%q, want non-target/%s",
			nonTarget.classification, nonTarget.completionEventID, nonTargetEventID)
	}
	if !nonTarget.candidateColumnsAreNull() {
		t.Fatalf("non-target event leaked target authority columns: %+v", nonTarget)
	}

	// A target-looking event that bypasses the same-transaction precommit is an
	// incomplete closure, never a passive non-target or not-found result.
	corruptEventID := insertWorkflowV3QualityCompletionEvent(
		t, ctx, database, runID, actorID, 2, "node.completed", "quality", true,
	)
	if _, err := resolveWorkflowV3QualityCompletionWire(ctx, resolverDatabase, corruptEventID); err == nil {
		t.Fatal("precommitless target Quality completion unexpectedly resolved")
	} else {
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.Code != "WQC04" {
			t.Fatalf("precommitless target error = %v, want WQC04", err)
		}
	}
}

func workflowV3QualityRoleDatabase(
	t *testing.T,
	ctx context.Context,
	base *sql.DB,
	schemaDatabase *sql.DB,
	dsn string,
	groupRole string,
	prefix string,
) *sql.DB {
	t.Helper()
	var schema string
	if err := schemaDatabase.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	login := prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	password := "quality-completion-" + uuid.NewString()
	if _, err := base.ExecContext(ctx, fmt.Sprintf(
		"CREATE ROLE %s LOGIN INHERIT NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '%s'",
		login, password,
	)); err != nil {
		t.Fatalf("create Quality completion role login: %v", err)
	}
	if _, err := base.ExecContext(ctx, fmt.Sprintf(
		"GRANT %s TO %s WITH INHERIT TRUE, SET FALSE, ADMIN FALSE", groupRole, login,
	)); err != nil {
		_, _ = base.ExecContext(context.Background(), "DROP ROLE IF EXISTS "+login)
		t.Fatalf("attach Quality completion capability role: %v", err)
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.User = login
	config.Password = password
	config.RuntimeParams["search_path"] = schema
	registeredDSN := stdlib.RegisterConnConfig(config)
	database, err := sql.Open("pgx", registeredDSN)
	if err != nil {
		stdlib.UnregisterConnConfig(registeredDSN)
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		stdlib.UnregisterConnConfig(registeredDSN)
		_, _ = base.ExecContext(context.Background(), "DROP ROLE IF EXISTS "+login)
		t.Fatalf("connect as Quality completion capability login: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
		stdlib.UnregisterConnConfig(registeredDSN)
		_, _ = base.ExecContext(context.Background(), `
SELECT pg_catalog.pg_terminate_backend(pid)
FROM pg_catalog.pg_stat_activity
WHERE usename=$1 AND pid<>pg_catalog.pg_backend_pid()`, login)
		_, _ = base.ExecContext(context.Background(), "DROP ROLE IF EXISTS "+login)
	})
	return database
}

func TestWorkflowV3QualityCompletionPrecommitEmptyRollbackPostgresCanary(t *testing.T) {
	ctx, base, dsn := qualificationReceiptV3Postgres(t)
	ensureQualificationReleaseHardenedRoles(t, ctx, base)
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "quality_completion_precommit_down_")
	applyPostgresMigrationsForCanary(t, database)

	down, err := files.ReadFile("000085_workflow_v3_quality_completion_precommit.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("empty Quality completion precommit rollback: %v", err)
	}

	var remainingTables, remainingFunctions int
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM pg_catalog.pg_class AS relation
   WHERE relation.relnamespace=current_schema()::regnamespace
     AND relation.relkind='r'
     AND relation.relname LIKE 'workflow_v3_quality_completion_%'),
  (SELECT count(*) FROM pg_catalog.pg_proc AS routine
   WHERE routine.pronamespace=current_schema()::regnamespace
     AND routine.proname IN (
       'precommit_workflow_v3_quality_completion_v1',
       'inspect_workflow_v3_quality_completion_precommit_v1',
       'admit_workflow_v3_quality_completion_materials_v1',
       'resolve_workflow_v3_quality_completion_material_plan_v1',
       'resolve_workflow_v3_quality_completion_candidate_v1',
       'freeze_workflow_input_authority_from_quality_precommit_v1'
     ))
`).Scan(&remainingTables, &remainingFunctions); err != nil {
		t.Fatal(err)
	}
	if remainingTables != 0 || remainingFunctions != 0 {
		t.Fatalf("Quality completion rollback left tables/functions = %d/%d", remainingTables, remainingFunctions)
	}
	assertWorkflowV3QualityFunctionPrivilege(t, ctx, database,
		"worksflow_application", workflowV3QualityLegacyFreezeSignature, true,
	)
	var applicationUsage, resolverUsage bool
	if err := database.QueryRowContext(ctx, `
SELECT
  pg_catalog.has_schema_privilege('worksflow_application',current_schema(),'USAGE'),
  pg_catalog.has_schema_privilege('worksflow_workflow_input_authority_operator',current_schema(),'USAGE')
`).Scan(&applicationUsage, &resolverUsage); err != nil {
		t.Fatal(err)
	}
	if !applicationUsage || resolverUsage {
		t.Fatalf("rollback schema USAGE application/resolver = %t/%t, want true/false",
			applicationUsage, resolverUsage)
	}

	up, err := files.ReadFile(workflowV3QualityCompletionPrecommitMigration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		t.Fatalf("reapply Quality completion precommit after empty rollback: %v", err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM pg_catalog.pg_class AS relation
   WHERE relation.relnamespace=current_schema()::regnamespace
     AND relation.relkind='r'
     AND relation.relname LIKE 'workflow_v3_quality_completion_%'),
  (SELECT count(*) FROM pg_catalog.pg_proc AS routine
   WHERE routine.pronamespace=current_schema()::regnamespace
     AND routine.proname IN (
       'precommit_workflow_v3_quality_completion_v1',
       'inspect_workflow_v3_quality_completion_precommit_v1',
       'admit_workflow_v3_quality_completion_materials_v1',
       'resolve_workflow_v3_quality_completion_material_plan_v1',
       'resolve_workflow_v3_quality_completion_candidate_v1',
       'freeze_workflow_input_authority_from_quality_precommit_v1'
     ))
`).Scan(&remainingTables, &remainingFunctions); err != nil {
		t.Fatal(err)
	}
	if remainingTables != 7 || remainingFunctions != 6 {
		t.Fatalf("Quality completion reapply restored tables/functions = %d/%d, want 7/6",
			remainingTables, remainingFunctions)
	}
	assertWorkflowV3QualityCompletionACL(t, ctx, database)
	if err := VerifyCurrent(ctx, database); err != nil {
		t.Fatalf("migration registry is not current after Quality up/down/up: %v", err)
	}
}

func TestWorkflowV3QualityCompletionAtomicHappyPathPostgresCanary(t *testing.T) {
	ctx, base, dsn := qualificationReceiptV3Postgres(t)
	ensureQualificationReleaseHardenedRoles(t, ctx, base)
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "quality_completion_happy_")
	applyPostgresMigrationsForCanary(t, database)

	fixture := seedWorkflowInputCanary(t, ctx, database)
	manifest, err := domain.NewInputManifest(
		fixture.manifestID.String(),
		fixture.projectID.String(),
		"workflow-input-canary",
		fixture.deliverySliceID.String(),
		&domain.ArtifactRef{
			ArtifactID:  fixture.targetArtifactID.String(),
			RevisionID:  fixture.targetRevisionID.String(),
			ContentHash: workflowInputDigest(fixture.revisionRaw),
		},
		[]domain.ManifestSource{},
		json.RawMessage(`{}`),
		"workspace/v1",
		fixture.userID.String(),
		time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("build strict retained InputManifest fixture: %v", err)
	}
	fixture.manifestRaw = mustWorkflowInputJSON(t, manifest)
	var retainedManifest domain.InputManifest
	if string(fixture.manifestRaw) == "{}" ||
		json.Unmarshal(fixture.manifestRaw, &retainedManifest) != nil ||
		retainedManifest.Validate() != nil {
		t.Fatal("Quality completion canary must not reuse the legacy empty InputManifest fixture")
	}
	definition, _ := workflowExecutionProfileV3MigrationDefinition(t)
	definitionDocument, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	var definitionValue map[string]any
	if err := json.Unmarshal(definitionDocument, &definitionValue); err != nil {
		t.Fatal(err)
	}
	definitionValue["id"] = fixture.definitionID.String()
	definitionValue["createdBy"] = fixture.userID.String()
	workflowExecutionProfileV3RehashDocument(t, definitionValue)
	definitionDocument, err = json.Marshal(definitionValue)
	if err != nil {
		t.Fatal(err)
	}
	fixture.definitionRaw = definitionDocument
	var gateEdgeID string
	for _, rawEdge := range definitionValue["edges"].([]any) {
		edge := rawEdge.(map[string]any)
		if edge["from"] == "quality" && edge["to"] == "external-qualification" {
			gateEdgeID = edge["id"].(string)
			break
		}
	}
	var gateInput domain.NodeInputEnvelope
	if gateEdgeID == "" || json.Unmarshal(fixture.nodeInputRaw, &gateInput) != nil {
		t.Fatal("cannot reconstruct exact v3 Quality gate input fixture")
	}
	bindings := gateInput.Bindings()
	if len(bindings) != 1 {
		t.Fatalf("Quality gate fixture has %d bindings, want 1", len(bindings))
	}
	bindings[0].EdgeID = gateEdgeID
	gateInput, err = domain.NewNodeInputEnvelope(bindings)
	if err != nil {
		t.Fatal(err)
	}
	fixture.nodeInputRaw = gateInput.Canonical()

	completedAt := time.Now().UTC().Truncate(time.Millisecond)
	leaseOwner := "quality-completion-canary"
	fixtureTx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixtureTx.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		_ = fixtureTx.Rollback()
		t.Fatal(err)
	}
	if _, err := fixtureTx.ExecContext(ctx, `
UPDATE workflow_definition_versions
SET content=$2::jsonb,content_hash=$3
WHERE id=$1
`, fixture.definitionVersionID, definitionDocument, definitionValue["hash"]); err != nil {
		_ = fixtureTx.Rollback()
		t.Fatalf("install exact v3 definition fixture: %v", err)
	}
	if _, err := fixtureTx.ExecContext(ctx, `
UPDATE input_manifests
SET content_hash=$2,manifest_hash=$3
WHERE id=$1
`, fixture.manifestID, workflowInputDigest(fixture.manifestRaw), "sha256:"+manifest.Hash); err != nil {
		_ = fixtureTx.Rollback()
		t.Fatalf("install strict retained InputManifest fixture: %v", err)
	}
	for _, rawNode := range definitionValue["nodes"].([]any) {
		node := rawNode.(map[string]any)
		nodeKey := node["id"].(string)
		if nodeKey == "quality" || nodeKey == "external-qualification" {
			continue
		}
		if _, err := fixtureTx.ExecContext(ctx, `
INSERT INTO workflow_node_runs(
  id,run_id,node_key,node_type,status,definition_node_id,slice_kind,
  attempt,available_at,created_at,updated_at
) VALUES($1,$2,$3,$4,'pending',$3,'root',0,$5,$5,$5)
`, uuid.New(), fixture.runID, nodeKey, node["type"].(string), completedAt); err != nil {
			_ = fixtureTx.Rollback()
			t.Fatalf("insert exact v3 node %s: %v", nodeKey, err)
		}
	}
	if _, err := fixtureTx.ExecContext(ctx, `
UPDATE workflow_runs
SET context='{"nodes":{"external-qualification":{}}}'::jsonb,
    event_cursor=5,status='running',updated_at=$2
WHERE id=$1
`, fixture.runID, completedAt); err != nil {
		_ = fixtureTx.Rollback()
		t.Fatalf("reset pre-Quality run fixture: %v", err)
	}
	if _, err := fixtureTx.ExecContext(ctx, `
UPDATE workflow_node_runs
SET status='running',attempt=1,output_revision_id=NULL,
    lease_owner=$2,lease_expires_at=$3,started_at=$4,
    completed_at=NULL,failure=NULL,updated_at=$4
WHERE id=$1
`, fixture.qualityNodeID, leaseOwner, completedAt.Add(time.Minute),
		completedAt.Add(-time.Minute)); err != nil {
		_ = fixtureTx.Rollback()
		t.Fatalf("reset pre-Quality node fixture: %v", err)
	}
	if err := fixtureTx.Commit(); err != nil {
		t.Fatalf("commit pre-Quality fixture: %v", err)
	}

	applicationDatabase := workflowV3QualityRoleDatabase(
		t, ctx, base, database, dsn, "worksflow_application", "quality_happy_application_",
	)
	if _, err := database.ExecContext(ctx, `
GRANT SELECT,UPDATE ON workflow_runs,workflow_node_runs TO worksflow_application;
GRANT SELECT,INSERT ON workflow_run_events,outbox_events TO worksflow_application
`); err != nil {
		t.Fatalf("grant application Workflow activation DML fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `
REVOKE SELECT,UPDATE ON workflow_runs,workflow_node_runs FROM worksflow_application;
REVOKE SELECT,INSERT ON workflow_run_events,outbox_events FROM worksflow_application
`)
	})
	var plan []byte
	if err := applicationDatabase.QueryRowContext(ctx, `
SELECT resolve_workflow_v3_quality_completion_material_plan_v1($1,$2,$3)
`, fixture.runID, fixture.qualityNodeID, fixture.nodeInputRaw).Scan(&plan); err != nil {
		t.Fatalf("resolve pre-BEGIN material plan: %v", err)
	}
	for _, key := range []string{
		`"definitionRawBytesHex"`, `"runScopeRawBytesHex"`,
		`"nodeInputRawBytesHex"`, `"buildManifest"`, `"buildContract"`,
		`"inputManifests"`, `"revisions"`, `"reviewReceipts"`,
	} {
		if !strings.Contains(string(plan), key) {
			t.Fatalf("material plan is missing %s: %s", key, plan)
		}
	}

	precommitID, operationID, authorityID := uuid.New(), uuid.New(), uuid.New()
	activationEventID, completionEventID := uuid.New(), uuid.New()
	materialBundle := mustWorkflowInputJSON(t, map[string]any{
		"inputManifests": []any{map[string]any{
			"manifestId":  fixture.manifestID.String(),
			"rawBytesHex": hex.EncodeToString(fixture.manifestRaw), "role": "run",
		}},
		"revisions": []any{map[string]any{
			"purpose": "workspace-target", "revisionId": fixture.targetRevisionID.String(),
			"rawBytesHex": hex.EncodeToString(fixture.revisionRaw),
		}},
		"reviewReceipts": []any{},
	})
	completionPayload := []byte(`{"attempt":1}`)
	transaction, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
SELECT admit_workflow_v3_quality_completion_materials_v1(
  $1,$2,$3,$4,$5,$6,$7,$8::jsonb
)
`, precommitID, completionEventID, fixture.definitionRaw, fixture.scopeRaw,
		fixture.nodeInputRaw, fixture.buildManifestRaw, fixture.buildContractRaw,
		materialBundle); err != nil {
		t.Fatalf("admit exact Quality material: %v", err)
	}
	var retainedPrecommit string
	if err := transaction.QueryRowContext(ctx, `
SELECT precommit_id::text
FROM precommit_workflow_v3_quality_completion_v1(
  $1,$2,$3,$4,$5,$6,$7,5,$8,$9,1,$10,$11::jsonb,$12,$13
)
`, precommitID, operationID, authorityID, activationEventID, fixture.runID,
		fixture.qualityNodeID, fixture.gateNodeID, completionEventID, leaseOwner,
		completedAt, completionPayload, fixture.userID, fixture.nodeInputRaw,
	).Scan(&retainedPrecommit); err != nil {
		t.Fatalf("precommit exact Quality completion: %v", err)
	}
	if retainedPrecommit != precommitID.String() {
		t.Fatalf("precommit identity = %s, want %s", retainedPrecommit, precommitID)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE workflow_runs
SET context=jsonb_set(
      context,'{nodes,external-qualification,input}',$2::jsonb,true
    ),event_cursor=6,status='running',updated_at=$3
WHERE id=$1 AND event_cursor=5
`, fixture.runID, fixture.nodeInputRaw, completedAt); err != nil {
		t.Fatalf("apply exact Quality run mutation: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE workflow_node_runs
SET status='completed',output_revision_id=$3,lease_owner=NULL,
    lease_expires_at=NULL,completed_at=$4,failure=NULL,updated_at=$4
WHERE id=$1 AND run_id=$2 AND status='running'
  AND lease_owner=$5 AND attempt=1
`, fixture.qualityNodeID, fixture.runID, fixture.targetRevisionID,
		completedAt, leaseOwner); err != nil {
		t.Fatalf("apply exact Quality node mutation: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO workflow_run_events(
  id,run_id,sequence,event_type,node_key,payload,actor_id,created_at
) VALUES($1,$2,6,'node.completed','quality',$3::jsonb,$4,$5)
`, completionEventID, fixture.runID, completionPayload, fixture.userID,
		completedAt); err != nil {
		t.Fatalf("insert exact Quality Workflow event: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO outbox_events(
  id,aggregate_type,aggregate_id,event_type,subject,payload,headers,
  available_at,created_at
) VALUES(
  $1::uuid,'workflow_run',($2::uuid)::text,'node.completed',
  'worksflow.workflow.run.event',
  jsonb_build_object(
    'id',($1::uuid)::text,'projectId',($3::uuid)::text,
    'runId',($2::uuid)::text,'sequence',6,
    'type','node.completed','nodeKey','quality',
    'occurredAt',$4::text,'payload',$5::jsonb,
    'actorId',($6::uuid)::text
  ),'{}'::jsonb,$7,$7
)
`, completionEventID, fixture.runID, fixture.projectID,
		completedAt.Format(time.RFC3339Nano), completionPayload,
		fixture.userID, completedAt); err != nil {
		t.Fatalf("insert exact Quality outbox event: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit exact Quality closure: %v", err)
	}

	resolverDatabase := workflowV3QualityRoleDatabase(
		t, ctx, base, database, dsn, "worksflow_workflow_input_authority_operator", "quality_happy_resolver_",
	)
	resolved, err := resolveWorkflowV3QualityCompletionWire(ctx, resolverDatabase, completionEventID)
	if err != nil {
		t.Fatalf("resolve committed Quality candidate: %v", err)
	}
	if resolved.classification != "target" || !resolved.precommitID.Valid ||
		resolved.precommitID.String != precommitID.String() || len(resolved.freezeCandidateBytes) == 0 {
		t.Fatalf("resolved committed Quality candidate is incomplete: %+v", resolved)
	}
	completionIdentity, err := workflowqualificationactivation.ParseCompletionEventID(completionEventID.String())
	if err != nil {
		t.Fatal(err)
	}
	activationResolver, err := workflowqualificationactivation.NewPostgresResolver(resolverDatabase)
	if err != nil {
		t.Fatal(err)
	}
	activationStore, err := workflowqualificationactivation.NewPostgresStore(
		applicationDatabase,
		workflowqualificationactivation.PostgresStoreConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	activationService, err := workflowqualificationactivation.NewService(
		activationResolver,
		activationStore,
	)
	if err != nil {
		t.Fatal(err)
	}
	strictResolution, err := activationResolver.Resolve(ctx, completionIdentity)
	if err != nil {
		t.Fatalf("decode exact retained Quality candidate: %v", err)
	}
	if strictResolution.Classification != workflowqualificationactivation.ClassificationTarget {
		t.Fatalf("strict Quality candidate classification = %q, want target", strictResolution.Classification)
	}
	freezePreflight, err := applicationDatabase.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	inputAuthorityStore, err := workflowinputauthority.NewPostgresStore(applicationDatabase)
	if err != nil {
		_ = freezePreflight.Rollback()
		t.Fatal(err)
	}
	freezeTransaction, err := workflowinputauthority.NewPostgresTransaction(freezePreflight)
	if err != nil {
		_ = freezePreflight.Rollback()
		t.Fatal(err)
	}
	if _, err := inputAuthorityStore.Freeze(ctx, freezeTransaction, strictResolution.Candidate); err != nil {
		_ = freezePreflight.Rollback()
		t.Fatalf("preflight exact Quality-precommit freeze wrapper: %v", err)
	}
	if err := freezePreflight.Rollback(); err != nil {
		t.Fatalf("roll back Quality-precommit freeze preflight: %v", err)
	}
	activated, err := activationService.Activate(ctx, completionIdentity)
	if err != nil {
		t.Fatalf("activate exact retained Quality candidate: %v", err)
	}
	if activated.Idempotent || activated.OperationID != operationID ||
		activated.AuthorityID != authorityID || activated.ActivationEventID != activationEventID ||
		activated.WorkflowRunID != fixture.runID || activated.NodeRunID != fixture.gateNodeID ||
		activated.ActivationEventSequence != 7 {
		t.Fatalf("activated Quality candidate differs from precommit: %+v", activated)
	}
	replayed, err := activationService.Activate(ctx, completionIdentity)
	if err != nil {
		t.Fatalf("replay exact retained Quality activation: %v", err)
	}
	if !replayed.Idempotent || replayed.OperationID != activated.OperationID ||
		replayed.AuthorityID != activated.AuthorityID || replayed.ActivationEventID != activated.ActivationEventID ||
		replayed.WorkflowRunID != activated.WorkflowRunID || replayed.NodeRunID != activated.NodeRunID ||
		replayed.ActivationEventSequence != activated.ActivationEventSequence ||
		replayed.RequestHash != activated.RequestHash || replayed.TargetHash != activated.TargetHash ||
		replayed.InputHash != activated.InputHash || replayed.AuthorityHash != activated.AuthorityHash {
		t.Fatalf("replayed Quality activation differs from immutable first result: first=%+v replay=%+v",
			activated, replayed)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE outbox_events
SET attempts=attempts+1,available_at=$2,published_at=$2,last_error=NULL
WHERE id=$1
`, completionEventID, completedAt.Add(time.Second)); err != nil {
		t.Fatalf("advance mutable Quality outbox delivery state: %v", err)
	}
	if _, err := resolveWorkflowV3QualityCompletionWire(
		ctx, resolverDatabase, completionEventID,
	); err != nil {
		t.Fatalf("resolve Quality candidate after outbox delivery: %v", err)
	}
	tamper, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tamper.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		_ = tamper.Rollback()
		t.Fatal(err)
	}
	if _, err := tamper.ExecContext(ctx, `
UPDATE outbox_events
SET payload=jsonb_set(payload,'{type}','"tampered"'::jsonb)
WHERE id=$1
`, completionEventID); err != nil {
		_ = tamper.Rollback()
		t.Fatal(err)
	}
	if err := tamper.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWorkflowV3QualityCompletionWire(
		ctx, resolverDatabase, completionEventID,
	); err == nil {
		t.Fatal("corrupt committed Quality closure unexpectedly resolved")
	} else {
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.Code != "WQC02" {
			t.Fatalf("corrupt committed Quality resolver error = %v, want WQC02", err)
		}
	}
}

const workflowV3QualityLegacyFreezeSignature = "freeze_workflow_input_authority_v1(uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb)"

type workflowV3QualityResolverWire struct {
	classification        string
	completionEventID     string
	precommitID           sql.NullString
	freezeRequestHash     sql.NullString
	freezeRequestBytes    []byte
	workflowInputHash     sql.NullString
	workflowInputBytes    []byte
	freezeCandidateBytes  []byte
	definitionRawBytes    []byte
	runScopeRawBytes      []byte
	nodeInputRawBytes     []byte
	buildManifestRawBytes []byte
	buildContractRawBytes []byte
	materialBundle        []byte
	snapshotHash          sql.NullString
	retainedRawBytesSize  sql.NullInt64
}

func (wire workflowV3QualityResolverWire) candidateColumnsAreNull() bool {
	return !wire.precommitID.Valid &&
		!wire.freezeRequestHash.Valid && wire.freezeRequestBytes == nil &&
		!wire.workflowInputHash.Valid && wire.workflowInputBytes == nil &&
		wire.freezeCandidateBytes == nil && wire.definitionRawBytes == nil &&
		wire.runScopeRawBytes == nil && wire.nodeInputRawBytes == nil &&
		wire.buildManifestRawBytes == nil && wire.buildContractRawBytes == nil &&
		wire.materialBundle == nil && !wire.snapshotHash.Valid && !wire.retainedRawBytesSize.Valid
}

func resolveWorkflowV3QualityCompletionWire(
	ctx context.Context,
	database *sql.DB,
	completionEventID uuid.UUID,
) (workflowV3QualityResolverWire, error) {
	var wire workflowV3QualityResolverWire
	err := database.QueryRowContext(ctx, `
SELECT
  classification,
  completion_event_id::text,
  precommit_id::text,
  freeze_request_hash,
  freeze_request_bytes,
  workflow_input_hash,
  workflow_input_bytes,
  freeze_candidate_bytes,
  definition_raw_bytes,
  run_scope_raw_bytes,
  node_input_raw_bytes,
  build_manifest_raw_bytes,
  build_contract_raw_bytes,
  material_bundle,
  snapshot_hash,
  retained_raw_bytes_size
FROM resolve_workflow_v3_quality_completion_candidate_v1($1)
`, completionEventID).Scan(
		&wire.classification,
		&wire.completionEventID,
		&wire.precommitID,
		&wire.freezeRequestHash,
		&wire.freezeRequestBytes,
		&wire.workflowInputHash,
		&wire.workflowInputBytes,
		&wire.freezeCandidateBytes,
		&wire.definitionRawBytes,
		&wire.runScopeRawBytes,
		&wire.nodeInputRawBytes,
		&wire.buildManifestRawBytes,
		&wire.buildContractRawBytes,
		&wire.materialBundle,
		&wire.snapshotHash,
		&wire.retainedRawBytesSize,
	)
	return wire, err
}

func insertWorkflowV3QualityCompletionEvent(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	runID uuid.UUID,
	actorID uuid.UUID,
	sequence int64,
	eventType string,
	nodeKey string,
	bypassClosure bool,
) uuid.UUID {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if bypassClosure {
		if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
			t.Fatal(err)
		}
	}
	eventID := uuid.New()
	var nullableNode any
	if nodeKey != "" {
		nullableNode = nodeKey
	}
	createdAt := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO workflow_run_events(
  id,run_id,sequence,event_type,node_key,payload,actor_id,created_at
) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,$8)
`, eventID, runID, sequence, eventType, nullableNode, `{"attempt":1}`, actorID, createdAt); err != nil {
		t.Fatalf("insert %s event: %v", eventType, err)
	}
	advanced, err := transaction.ExecContext(ctx, `
UPDATE workflow_runs
SET event_cursor=$2,updated_at=$3
WHERE id=$1 AND event_cursor=$2-1
`, runID, sequence, createdAt)
	if err != nil {
		t.Fatalf("advance %s event cursor: %v", eventType, err)
	}
	if rows, err := advanced.RowsAffected(); err != nil || rows != 1 {
		t.Fatalf("advance %s event cursor rows/error = %d/%v, want 1/nil", eventType, rows, err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit %s event: %v", eventType, err)
	}
	return eventID
}

func assertWorkflowV3QualityCompletionACL(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) {
	t.Helper()
	capabilities := []string{
		"resolve_workflow_v3_quality_completion_material_plan_v1(uuid,uuid,bytea)",
		"precommit_workflow_v3_quality_completion_v1(uuid,uuid,uuid,uuid,uuid,uuid,uuid,bigint,uuid,text,integer,timestamptz,jsonb,uuid,bytea)",
		"inspect_workflow_v3_quality_completion_precommit_v1(uuid)",
		"admit_workflow_v3_quality_completion_materials_v1(uuid,uuid,bytea,bytea,bytea,bytea,bytea,jsonb)",
		"freeze_workflow_input_authority_from_quality_precommit_v1(uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb)",
	}
	for _, signature := range capabilities {
		assertWorkflowV3QualityFunctionPrivilege(t, ctx, database, "worksflow_application", signature, true)
		assertWorkflowV3QualityFunctionPrivilege(t, ctx, database, "worksflow_workflow_input_authority_operator", signature, false)
	}
	resolver := "resolve_workflow_v3_quality_completion_candidate_v1(uuid)"
	assertWorkflowV3QualityFunctionPrivilege(t, ctx, database, "worksflow_application", resolver, false)
	assertWorkflowV3QualityFunctionPrivilege(t, ctx, database, "worksflow_workflow_input_authority_operator", resolver, true)
	assertWorkflowV3QualityFunctionPrivilege(t, ctx, database, "worksflow_application", workflowV3QualityLegacyFreezeSignature, false)
	assertWorkflowV3QualityFunctionPrivilege(t, ctx, database, "worksflow_workflow_input_authority_operator", workflowV3QualityLegacyFreezeSignature, false)

	for _, role := range []string{
		"worksflow_auditor",
		"worksflow_schema_migrator",
		"worksflow_qualification_plan_operator",
		"worksflow_qualification_release_operator",
	} {
		for _, signature := range append(append([]string(nil), capabilities...), resolver) {
			assertWorkflowV3QualityFunctionPrivilege(t, ctx, database, role, signature, false)
		}
	}

	var leakedTablePrivileges int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_catalog.pg_class AS relation
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid=relation.relnamespace
CROSS JOIN LATERAL pg_catalog.aclexplode(
  COALESCE(relation.relacl,pg_catalog.acldefault('r',relation.relowner))
) AS acl
LEFT JOIN pg_catalog.pg_roles AS grantee ON grantee.oid=acl.grantee
WHERE namespace.nspname=current_schema()
  AND relation.relkind='r'
  AND relation.relname LIKE 'workflow_v3_quality_completion_%'
  AND (acl.grantee=0 OR grantee.rolname=ANY($1::text[]))
`, []string{
		"worksflow_application",
		"worksflow_auditor",
		"worksflow_schema_migrator",
		"worksflow_workflow_input_authority_operator",
	}).Scan(&leakedTablePrivileges); err != nil {
		t.Fatal(err)
	}
	if leakedTablePrivileges != 0 {
		t.Fatalf("Quality completion private tables expose %d runtime ACL entries", leakedTablePrivileges)
	}
}

func assertWorkflowV3QualityFunctionPrivilege(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	role string,
	signature string,
	want bool,
) {
	t.Helper()
	var exists bool
	var allowed sql.NullBool
	if err := database.QueryRowContext(ctx, `
SELECT
  pg_catalog.to_regprocedure(current_schema() || '.' || $1) IS NOT NULL,
  pg_catalog.has_function_privilege(
    $2,
    pg_catalog.to_regprocedure(current_schema() || '.' || $1),
    'EXECUTE'
  )
`, signature, role).Scan(&exists, &allowed); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("function capability %s is absent", signature)
	}
	if !allowed.Valid || allowed.Bool != want {
		t.Fatalf("role %s EXECUTE %s = %v, want %t", role, signature, allowed, want)
	}
}

type workflowV3QualityABIColumn struct {
	name     string
	dataType string
}

func workflowV3QualityPrecommitResultABI() []workflowV3QualityABIColumn {
	return []workflowV3QualityABIColumn{
		{name: "precommit_id", dataType: "uuid"},
		{name: "workflow_input_operation_id", dataType: "uuid"},
		{name: "workflow_input_authority_id", dataType: "uuid"},
		{name: "activation_event_id", dataType: "uuid"},
		{name: "project_id", dataType: "uuid"},
		{name: "workflow_run_id", dataType: "uuid"},
		{name: "quality_node_run_id", dataType: "uuid"},
		{name: "quality_node_key", dataType: "text"},
		{name: "gate_node_run_id", dataType: "uuid"},
		{name: "gate_node_key", dataType: "text"},
		{name: "expected_run_cursor", dataType: "bigint"},
		{name: "completion_event_sequence", dataType: "bigint"},
		{name: "completion_event_id", dataType: "uuid"},
		{name: "completion_event_payload", dataType: "jsonb"},
		{name: "completion_event_actor_id", dataType: "uuid"},
		{name: "quality_completed_at", dataType: "timestamptz"},
		{name: "quality_lease_owner", dataType: "text"},
		{name: "quality_attempt", dataType: "integer"},
		{name: "workspace_revision_id", dataType: "uuid"},
		{name: "gate_input_raw_bytes", dataType: "bytea"},
		{name: "gate_input_raw_bytes_hash", dataType: "text"},
		{name: "gate_input_raw_bytes_size", dataType: "bigint"},
		{name: "gate_input_semantic_hash", dataType: "text"},
		{name: "gate_input_binding_count", dataType: "integer"},
	}
}

func assertWorkflowV3QualityPrecommitTableABI(t *testing.T, text string) {
	t.Helper()
	const marker = "CREATE TABLE workflow_v3_quality_completion_precommits ("
	start := strings.Index(text, marker)
	if start < 0 {
		t.Fatal("Quality completion migration has no precommit row type")
	}
	declaration, _ := workflowV3QualityParenthesized(t, text, start+len(marker)-1)
	parts := workflowV3QualitySQLList(declaration)
	want := workflowV3QualityPrecommitResultABI()
	if len(parts) < len(want) {
		t.Fatalf("Quality completion precommit row has %d entries, want at least %d columns", len(parts), len(want))
	}
	for index, column := range want {
		fields := strings.Fields(parts[index])
		if len(fields) < 2 || strings.ToLower(fields[0]) != column.name ||
			workflowV3QualityCanonicalSQLType(fields[1]) != column.dataType {
			t.Fatalf("Quality completion precommit row column %d = %q, want %s %s",
				index+1, parts[index], column.name, column.dataType)
		}
	}
}

func assertWorkflowV3QualityFunctionABI(
	t *testing.T,
	text string,
	name string,
	wantArgumentTypes []string,
	wantResultColumns []workflowV3QualityABIColumn,
	wantScalarResult string,
) {
	t.Helper()
	marker := "CREATE FUNCTION " + name + "("
	start := strings.Index(text, marker)
	if start < 0 {
		t.Fatalf("Quality completion migration does not create %s", name)
	}
	arguments, afterArguments := workflowV3QualityParenthesized(t, text, start+len(marker)-1)
	argumentParts := workflowV3QualitySQLList(arguments)
	if len(argumentParts) != len(wantArgumentTypes) {
		t.Fatalf("%s argument count = %d, want %d: %q", name, len(argumentParts), len(wantArgumentTypes), arguments)
	}
	for index, argument := range argumentParts {
		fields := strings.Fields(argument)
		if len(fields) < 2 || workflowV3QualityCanonicalSQLType(strings.Join(fields[1:], " ")) != wantArgumentTypes[index] {
			t.Fatalf("%s argument %d = %q, want one named %s argument", name, index+1, argument, wantArgumentTypes[index])
		}
	}

	remainder := strings.TrimSpace(text[afterArguments:])
	if wantResultColumns != nil {
		const returnsTable = "RETURNS TABLE"
		if !strings.HasPrefix(remainder, returnsTable) {
			t.Fatalf("%s does not return its fixed table ABI: %q", name, workflowV3QualityPrefix(remainder, 120))
		}
		opening := strings.Index(remainder[len(returnsTable):], "(")
		if opening < 0 {
			t.Fatalf("%s RETURNS TABLE has no column list", name)
		}
		opening += len(returnsTable)
		columns, _ := workflowV3QualityParenthesized(t, remainder, opening)
		parts := workflowV3QualitySQLList(columns)
		if len(parts) != len(wantResultColumns) {
			t.Fatalf("%s result column count = %d, want %d: %q", name, len(parts), len(wantResultColumns), columns)
		}
		for index, part := range parts {
			fields := strings.Fields(part)
			want := wantResultColumns[index]
			if len(fields) < 2 || strings.ToLower(fields[0]) != want.name ||
				workflowV3QualityCanonicalSQLType(strings.Join(fields[1:], " ")) != want.dataType {
				t.Fatalf("%s result column %d = %q, want %s %s", name, index+1, part, want.name, want.dataType)
			}
		}
		return
	}
	normalizedRemainder := strings.ToLower(strings.Join(strings.Fields(remainder), " "))
	if !strings.HasPrefix(normalizedRemainder, "returns "+wantScalarResult+" ") {
		t.Fatalf("%s result = %q, want %s", name, workflowV3QualityPrefix(normalizedRemainder, 120), wantScalarResult)
	}
}

func workflowV3QualityParenthesized(t *testing.T, text string, opening int) (string, int) {
	t.Helper()
	if opening < 0 || opening >= len(text) || text[opening] != '(' {
		t.Fatalf("invalid SQL parenthesis offset %d", opening)
	}
	depth := 0
	for index := opening; index < len(text); index++ {
		switch text[index] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return text[opening+1 : index], index + 1
			}
		}
	}
	t.Fatal("unterminated SQL parenthesis")
	return "", 0
}

func workflowV3QualitySQLList(value string) []string {
	result := make([]string, 0)
	start := 0
	depth := 0
	for index := 0; index <= len(value); index++ {
		if index < len(value) {
			switch value[index] {
			case '(':
				depth++
			case ')':
				depth--
			case ',':
				if depth != 0 {
					continue
				}
			default:
				continue
			}
			if value[index] != ',' {
				continue
			}
		}
		part := strings.Join(strings.Fields(value[start:index]), " ")
		if part != "" {
			result = append(result, part)
		}
		start = index + 1
	}
	return result
}

func workflowV3QualityPrefix(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}

func workflowV3QualityCanonicalSQLType(value string) string {
	value = strings.ToLower(strings.Join(strings.Fields(value), " "))
	switch value {
	case "timestamp with time zone":
		return "timestamptz"
	case "int", "int4":
		return "integer"
	case "int8":
		return "bigint"
	default:
		return value
	}
}
