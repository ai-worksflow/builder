package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/internal/constructor"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	qualificationpolicy "github.com/worksflow/builder/backend/internal/qualificationpolicyauthority"
)

const workflowInputAuthorityMigration = "000078_workflow_input_authority.up.sql"

func TestWorkflowInputAuthorityDeclaresImmutableAtomicBoundary(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(workflowInputAuthorityMigration)
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000078_workflow_input_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	fence := strings.Index(text, "SELECT pg_catalog.pg_advisory_xact_lock(")
	firstDDL := strings.Index(text, "DO $workflow_input_hash_functions$")
	if fence < 0 || firstDDL < 0 || fence > firstDDL {
		t.Fatal("Workflow Input migration must acquire its exclusive rolling fence before relation access")
	}
	for _, required := range []string{
		"CREATE TABLE workflow_input_authorities",
		"CREATE TABLE workflow_input_authority_identity_reservations",
		"CREATE TABLE workflow_input_authority_predecessors",
		"CREATE TABLE workflow_input_authority_manifests",
		"CREATE TABLE workflow_input_authority_revisions",
		"CREATE TABLE workflow_input_authority_review_receipts",
		"worksflow-workflow-input-freeze-request/v1",
		"worksflow-workflow-input/v1",
		"worksflow-workflow-input-authority/v1",
		"worksflow-workflow-input-authority-hash/v1",
		"CREATE FUNCTION freeze_workflow_input_authority_v1(",
		"worksflow:workflow-input-authority-migration:v1",
		"pg_catalog.pg_advisory_xact_lock_shared(",
		"CREATE FUNCTION workflow_input_authority_bundle_v1",
		"CREATE FUNCTION inspect_workflow_input_authority_operation_v1",
		"CREATE FUNCTION resolve_workflow_input_authority_v1",
		"CREATE FUNCTION resolve_workflow_input_authority_for_node_v1",
		"CREATE FUNCTION assert_current_workflow_input_authority_v1",
		"CREATE CONSTRAINT TRIGGER workflow_input_authorities_exact_closure",
		"CREATE TRIGGER workflow_input_authority_event_identity_guard",
		"BEFORE INSERT OR UPDATE OR DELETE ON workflow_run_events",
		"DEFERRABLE INITIALLY DEFERRED",
		"Workflow Input Authority records are immutable",
		"canonical_review_receipts_workflow_input_exact_unique",
		"resolve_canonical_review_approval_receipt(",
		"canonical_review_approval_receipt_record_is_exact",
		"FROM projects WHERE id = v_project_id FOR UPDATE",
		"pin->'manifest'->>'id'",
		"quality predecessor does not produce the exact target revision",
		"USING ERRCODE = 'WIA04'",
		"must enqueue its exact activation outbox event",
		"WHERE authority_id = p_authority_id AND canonical_review_required",
		"must equal the policy-derived source subset",
		"qualification_policy_authority_record_is_exact_v1(",
		"Workflow Input aggregate retained bytes exceed the v1 bound",
		"Workflow Input activation event identity is immutable",
		"Workflow Input activation event actor and occurrence time are immutable",
		"workflow_run_id, node_run_id",
		"REVOKE ALL ON TABLE %I.workflow_input_authorities",
		"GRANT EXECUTE ON FUNCTION %I.freeze_workflow_input_authority_v1",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Workflow Input Authority migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"ADD VALUE 'waiting_qualification'",
		"workflow_runs_status_check CHECK (",
		"GRANT SELECT ON TABLE %I.workflow_input_authorities TO worksflow_application",
		"ON DELETE CASCADE",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Workflow Input Authority migration unexpectedly contains %q", forbidden)
		}
	}
	for _, required := range []string{
		"cannot roll back Workflow Input Authority after an authority",
		"workflow_input_authority_down_guard",
		"LOCK TABLE projects IN ACCESS EXCLUSIVE MODE",
		"workflow_input_authority_id','input_authority_id",
		"DROP FUNCTION freeze_workflow_input_authority_v1(",
		"DROP TABLE workflow_input_authorities",
		"DROP COLUMN input_authority_id",
		"DROP CONSTRAINT canonical_review_receipts_workflow_input_exact_unique",
	} {
		if !strings.Contains(string(down), required) {
			t.Fatalf("Workflow Input Authority rollback is missing %q", required)
		}
	}
}

type workflowInputCanary struct {
	userID, projectID, definitionID, definitionVersionID uuid.UUID
	runID, qualityRunID, qualityNodeID, gateNodeID       uuid.UUID
	manifestID, targetArtifactID, targetRevisionID       uuid.UUID
	reportArtifactID, reportRevisionID                   uuid.UUID
	buildManifestID, buildContractID, implementationID   uuid.UUID
	manifestGroupID, deliverySliceID                     uuid.UUID
	fullStackTemplateID                                  uuid.UUID
	operationID, authorityID, eventID, policyID          uuid.UUID
	definitionRaw, scopeRaw, nodeInputRaw                []byte
	manifestRaw, revisionRaw, buildManifestRaw           []byte
	buildContractRaw, candidateRaw                       []byte
	policyRecord                                         qualificationpolicy.Record
}

func TestWorkflowInputAuthorityPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "workflow_input_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	defer database.Close()
	// This canary exercises migration 78's atomic authority boundary. Migration
	// 79 intentionally forbids the generic transition statements used by the
	// negative cycle probes below and has its own production-profile canary.
	applyQualificationPolicyMigrationPrefix(t, ctx, database)
	if _, err := database.ExecContext(ctx, `
ALTER TABLE workflow_runs
  DROP CONSTRAINT workflow_runs_status_check,
  ADD CONSTRAINT workflow_runs_status_check CHECK (
    status IN ('pending','running','waiting_input','waiting_review','waiting_qualification','completed','failed','cancelled','stale')
  );
ALTER TABLE workflow_node_runs
  DROP CONSTRAINT workflow_node_runs_status_check,
  ADD CONSTRAINT workflow_node_runs_status_check CHECK (
    status IN ('pending','ready','running','waiting_input','waiting_review','waiting_qualification','completed','failed','cancelled','stale')
  )`); err != nil {
		t.Fatalf("enable migration-78 waiting_qualification canary state: %v", err)
	}
	assertWorkflowInputCatalog(t, ctx, database)

	fixture := seedWorkflowInputCanary(t, ctx, database)
	assertWorkflowInputProfileHashTamper(t, ctx, database, fixture)
	assertWorkflowInputPolicyBindingGuards(t, ctx, database, fixture)
	assertWorkflowInputReviewRequirementExactSet(t, ctx, database, fixture)
	assertWorkflowInputAtomicCycle(t, ctx, database, fixture)
	assertWorkflowInputCursorAtomicCycle(t, ctx, database, fixture)
	assertWorkflowInputApplicationAtomicCycle(t, ctx, database, fixture)
	assertWorkflowInputOutboxAtomicCycle(t, ctx, database, fixture)
	assertWorkflowInputExactFreeze(t, ctx, database, fixture)
	assertWorkflowInputPolicySupersession(t, ctx, database, fixture)
	assertWorkflowInputTamperDetection(t, ctx, database, fixture)
	assertWorkflowInputDownFence(t, ctx, database)
}

func TestWorkflowInputAuthorityEmptyRollbackPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "workflow_input_down_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	defer database.Close()
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version text PRIMARY KEY, checksum text NOT NULL, down_checksum text, applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		t.Fatal(err)
	}
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name > workflowInputAuthorityMigration {
			break
		}
		if err := applyFile(ctx, connection, name); err != nil {
			t.Fatalf("apply prerequisite %s: %v", name, err)
		}
	}
	down, err := files.ReadFile("000078_workflow_input_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("empty Workflow Input Authority rollback: %v", err)
	}
	var remaining int
	if err := connection.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema=current_schema() AND table_name LIKE 'workflow_input_authorit%'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("Workflow Input Authority tables remaining after down = %d", remaining)
	}
}

func seedWorkflowInputCanary(t *testing.T, ctx context.Context, database *sql.DB) workflowInputCanary {
	t.Helper()
	f := workflowInputCanary{
		userID: uuid.New(), projectID: uuid.New(), definitionID: uuid.New(), definitionVersionID: uuid.New(),
		runID: uuid.New(), qualityRunID: uuid.New(), qualityNodeID: uuid.New(), gateNodeID: uuid.New(), manifestID: uuid.New(),
		targetArtifactID: uuid.New(), targetRevisionID: uuid.New(), buildManifestID: uuid.New(),
		reportArtifactID: uuid.New(), reportRevisionID: uuid.New(), manifestGroupID: uuid.New(), deliverySliceID: uuid.New(),
		fullStackTemplateID: uuid.New(),
		buildContractID:     uuid.New(), implementationID: uuid.New(), operationID: uuid.New(), authorityID: uuid.New(), eventID: uuid.New(),
		policyID: uuid.New(), scopeRaw: []byte(`{}`), manifestRaw: []byte(`{}`),
		revisionRaw: []byte(`{}`), buildManifestRaw: []byte(`{}`), buildContractRaw: []byte(`{}`),
	}
	profileHash := "854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104"
	definition := map[string]any{
		"edges": []any{
			map[string]any{"id": "workbench-quality", "from": "workbench", "to": "quality"},
			map[string]any{"id": "quality-to-external", "from": "quality", "to": "external-qualification"},
			map[string]any{"id": "external-publish", "from": "external-qualification", "to": "publish"},
		},
		"executionProfile": map[string]any{"hash": profileHash, "version": "workflow-engine/v3"},
		"nodes": []any{
			map[string]any{"id": "workbench", "type": "workbench_build"},
			map[string]any{"id": "quality", "type": "quality_gate", "qualityGate": map[string]any{"blocking": true, "gateName": "release"}},
			map[string]any{
				"id": "external-qualification", "type": "external_qualification_gate",
				"externalQualificationGate": map[string]any{
					"blocking": true, "gateName": "external-qualification",
					"inputAuthoritySchema": "worksflow-workflow-input-authority/v1",
					"promotionProtocol":    "worksflow-qualification-promotion-consume/v2",
					"receiptSchema":        "worksflow-qualification-receipt/v3", "waiverPolicy": "never",
				},
			},
			map[string]any{"id": "publish", "type": "publish", "publish": map[string]any{"environment": "production"}},
		},
	}
	f.definitionRaw = mustWorkflowInputJSON(t, definition)
	emptyHash := workflowInputDigest([]byte(`{}`))
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	workspaceVersion := core.VersionRef{
		ArtifactID: f.targetArtifactID.String(), RevisionID: f.targetRevisionID.String(), ContentHash: emptyHash,
	}
	runID, manifestGroupID, deliverySliceID := f.runID.String(), f.manifestGroupID.String(), f.deliverySliceID.String()
	asset := core.AssetRef{
		AssetID: "canary-asset", ContentHash: "sha256:" + strings.Repeat("8", 64),
		MediaType: "application/json", ByteSize: 2,
	}
	selectedBundle := core.WorkbenchBundle{
		ID: f.buildManifestID.String(), ProjectID: f.projectID.String(), RootBuildManifestID: f.buildManifestID.String(),
		WorkflowRunID: &runID, ManifestGroupKey: &manifestGroupID, DeliverySliceID: &deliverySliceID,
		PageSpecRevision: workspaceVersion, PrototypeRevision: workspaceVersion,
		RequirementRevisions: []core.VersionRef{workspaceVersion}, BlueprintRevision: workspaceVersion,
		ContractRevisions: []core.VersionRef{}, DesignSystemRevisions: []core.VersionRef{},
		ContextRevisions: []core.WorkbenchContextRevision{}, SceneGraph: asset,
		RenderedFrames: []core.RenderedFrameRef{}, InteractionManifest: asset, FixtureBundle: asset,
		TokenManifest: asset, ComponentMapping: asset, TraceMatrix: asset, AcceptanceManifest: asset,
		Assumptions: []string{}, Waivers: []string{}, CreatedBy: f.userID.String(), CreatedAt: now,
	}
	selectedBundleHash, err := domain.CanonicalHash(selectedBundle)
	if err != nil {
		t.Fatalf("hash selected Workbench bundle: %v", err)
	}
	selectedBundle.ManifestHash = selectedBundleHash
	f.buildManifestRaw = mustWorkflowInputJSON(t, selectedBundle)
	buildManifestHash := "sha256:" + selectedBundleHash
	buildManifestContentHash := workflowInputDigest(f.buildManifestRaw)

	workspaceSource := constructor.ExactRevisionRef{
		Kind: "workspace", Purpose: "workspace-target", Required: true,
		ArtifactID: f.targetArtifactID.String(), RevisionID: f.targetRevisionID.String(),
		ContentHash: emptyHash, ApprovalStatus: "approved",
	}
	contract := constructor.ContractContent{
		SchemaVersion: constructor.BuildContractSchemaVersion,
		Compiler: constructor.CompilerIdentity{
			Version: constructor.CompilerVersion, Hash: strings.Repeat("9", 64),
		},
		ProjectID: f.projectID.String(), DeliverySliceID: f.deliverySliceID.String(),
		BuildManifest:   constructor.BuildManifestRef{ID: f.buildManifestID.String(), ContentHash: selectedBundleHash},
		SourceRevisions: []constructor.ExactRevisionRef{},
		FullStackTemplate: constructor.FullStackTemplateRef{
			ID: f.fullStackTemplateID.String(), ContentHash: strings.Repeat("1", 64),
			Certification: "approved", PolicyStatus: "active",
		},
		TemplateReleaseRefs: []constructor.TemplateReleaseRef{}, Routes: []constructor.RouteConstraint{},
		States: []constructor.StateConstraint{}, ContractBindings: []constructor.ContractBinding{},
		AcceptanceCriteria: []constructor.AcceptanceCriterion{},
		Oracles: []constructor.Oracle{{
			ID: "oracle", Kind: "unit", Target: "workspace", SourceRevision: workspaceSource,
			AcceptanceCriterionIDs: []string{},
		}},
		Obligations: []constructor.Obligation{{
			ID: "canary", Level: "must", Kind: "workspace", SourceRevision: workspaceSource,
			SourceAnchorID: "root", OracleIDs: []string{"oracle"}, DependsOn: []string{}, Status: "ready",
		}},
		Waivers: []constructor.Waiver{}, Gaps: []constructor.BuildGap{}, Conflicts: []constructor.BuildConflict{},
		ForbiddenClaims: []string{}, Status: constructor.StatusReady,
	}
	contractHash, err := domain.CanonicalHash(contract)
	if err != nil {
		t.Fatalf("hash selected BuildContract: %v", err)
	}
	f.buildContractRaw = mustWorkflowInputJSON(t, contract)
	buildContractHash := "sha256:" + contractHash
	buildContractContentHash := workflowInputDigest(f.buildContractRaw)
	workspaceRevision := map[string]any{
		"artifactId": f.targetArtifactID.String(), "revisionId": f.targetRevisionID.String(),
		"contentHash": emptyHash,
	}
	workflowBuildManifest := map[string]any{
		"schemaVersion": 1,
		"projectId":     f.projectID.String(), "runId": f.runID.String(),
		"manifestGroupKey": f.manifestGroupID.String(),
		"sliceIds":         []any{f.deliverySliceID.String()},
		"bundleIds":        []any{f.buildManifestID.String()},
		"sources":          []any{workspaceRevision},
		"constraints":      map[string]any{},
		"createdAt":        "2026-07-19T00:00:00Z",
		"hash":             "",
	}
	workflowBuildHash, err := domain.CanonicalHash(workflowBuildManifest)
	if err != nil {
		t.Fatalf("hash quality BuildManifest: %v", err)
	}
	workflowBuildManifest["hash"] = workflowBuildHash
	qualityOutput := map[string]any{
		"passed": true, "qualityRunId": f.qualityRunID.String(),
		"workspaceRevision": workspaceRevision,
		"findings": map[string]any{
			"checks": []any{}, "diagnostics": []any{}, "qualityRunId": f.qualityRunID.String(),
			"reportArtifactId": f.reportArtifactID.String(), "reportRevisionId": f.reportRevisionID.String(),
			"score": 100, "workspaceRevision": workspaceRevision,
		},
		"buildManifest": workflowBuildManifest,
	}
	qualityOutputRaw := mustWorkflowInputJSON(t, qualityOutput)
	envelope, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "quality-to-external", FromPort: "default", ToPort: "default",
		Source: domain.NodeOutputReference{
			RunID: f.runID.String(), NodeKey: "quality", DefinitionNodeID: "quality",
			OutputRevisionID: f.targetRevisionID.String(),
			ArtifactRevisions: []domain.ArtifactRef{{
				ArtifactID: f.targetArtifactID.String(), RevisionID: f.targetRevisionID.String(), ContentHash: emptyHash,
			}},
		},
		Output: qualityOutputRaw, Value: qualityOutputRaw,
	}})
	if err != nil {
		t.Fatalf("build canonical quality NodeInput: %v", err)
	}
	f.nodeInputRaw = envelope.Canonical()
	var nodeInput map[string]any
	if err := json.Unmarshal(f.nodeInputRaw, &nodeInput); err != nil {
		t.Fatalf("decode canonical quality NodeInput: %v", err)
	}
	contextDocument := map[string]any{
		"nodes": map[string]any{
			"external-qualification": map[string]any{"input": nodeInput},
		},
	}
	contextRaw := mustWorkflowInputJSON(t, contextDocument)
	candidate := map[string]any{
		"inputManifests": []any{map[string]any{
			"manifestId": f.manifestID.String(), "rawBytesHex": hex.EncodeToString(f.manifestRaw), "role": "run",
		}},
		"manifestSubject": "workflow-input-canary",
		"qualificationPolicy": map[string]any{
			"authorityHash": "sha256:" + strings.Repeat("d", 64), "authorityId": f.policyID.String(),
			"externalGatePolicy": "external-qualification/v1",
		},
		"qualityResult": map[string]any{
			"buildContractHash": buildContractHash, "buildContractId": f.buildContractID.String(),
			"buildManifestHash": buildManifestHash, "buildManifestId": f.buildManifestID.String(),
			"passed": true, "qualityRunId": f.qualityRunID.String(),
			"workspaceRevisionContentHash": emptyHash, "workspaceRevisionId": f.targetRevisionID.String(),
		},
		"reviewRequirements": []any{},
		"revisions": []any{map[string]any{
			"canonicalReviewRequired": false,
			"currencyPolicy":          "latest-approved-required", "purpose": "workspace-target",
			"rawBytesHex": hex.EncodeToString(f.revisionRaw), "revisionId": f.targetRevisionID.String(),
		}},
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	fail := func(message string, err error) {
		_ = tx.Rollback()
		t.Fatalf("%s: %v", message, err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		fail("disable fixture triggers", err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,email,display_name,password_hash) VALUES($1,$2,'Workflow Input Canary','x')`, []any{f.userID, strings.ToLower(f.userID.String()) + "@example.test"}},
		{`INSERT INTO projects(id,name,created_by,governance_mode) VALUES($1,'Workflow Input Canary',$2,'solo')`, []any{f.projectID, f.userID}},
		{`INSERT INTO input_manifests(id,project_id,kind,schema_version,content_store,content_ref,content_hash,manifest_hash,created_by)
		  VALUES($1,$2,'workflow-input-canary',1,'mongo',$3,$4,$5,$6)`, []any{f.manifestID, f.projectID, "manifest/" + f.manifestID.String(), emptyHash, "sha256:" + strings.Repeat("e", 64), f.userID}},
		{`INSERT INTO artifacts(id,project_id,kind,artifact_key,title,created_by)
		  VALUES($1,$2,'workspace',$3,'Workspace',$4)`, []any{f.targetArtifactID, f.projectID, "workspace-" + f.targetArtifactID.String(), f.userID}},
		{`INSERT INTO artifact_revisions(id,artifact_id,revision_number,schema_version,content_store,content_ref,content_hash,byte_size,workflow_status,change_source,created_by,approved_at)
		  VALUES($1,$2,1,1,'mongo',$3,$4,2,'approved','system',$5,date_trunc('milliseconds',now()))`, []any{f.targetRevisionID, f.targetArtifactID, "revision/" + f.targetRevisionID.String(), emptyHash, f.userID}},
		{`UPDATE artifacts SET latest_revision_id=$2,latest_approved_revision_id=$2 WHERE id=$1`, []any{f.targetArtifactID, f.targetRevisionID}},
		{`INSERT INTO artifacts(id,project_id,kind,artifact_key,title,created_by)
		  VALUES($1,$2,'quality_report',$3,'Quality report',$4)`, []any{f.reportArtifactID, f.projectID, "quality-report-" + f.reportArtifactID.String(), f.userID}},
		{`INSERT INTO artifact_revisions(id,artifact_id,revision_number,schema_version,content_store,content_ref,content_hash,byte_size,workflow_status,change_source,created_by,approved_at)
		  VALUES($1,$2,1,1,'mongo',$3,$4,2,'approved','system',$5,date_trunc('milliseconds',now()))`, []any{f.reportRevisionID, f.reportArtifactID, "revision/" + f.reportRevisionID.String(), emptyHash, f.userID}},
		{`UPDATE artifacts SET latest_revision_id=$2,latest_approved_revision_id=$2 WHERE id=$1`, []any{f.reportArtifactID, f.reportRevisionID}},
		{`INSERT INTO workflow_definitions(id,project_id,workflow_key,title,created_by)
		  VALUES($1,$2,'workflow-input-canary','Workflow Input Canary',$3)`, []any{f.definitionID, f.projectID, f.userID}},
		{`INSERT INTO workflow_definition_versions(id,definition_id,version,schema_version,content,content_hash,validation_report,published,created_by,execution_profile_version,execution_profile_hash)
		  VALUES($1,$2,1,1,$3,$4,'{}',true,$5,'workflow-engine/v3',$6)`, []any{f.definitionVersionID, f.definitionID, string(f.definitionRaw), strings.Repeat("f", 64), f.userID, profileHash}},
		{`INSERT INTO workflow_runs(id,project_id,definition_version_id,status,input_manifest_id,scope,context,event_cursor,started_by,started_at,execution_profile_version,execution_profile_hash,governance_mode)
		  VALUES($1,$2,$3,'running',$4,'{}',$5,5,$6,date_trunc('milliseconds',now()),'workflow-engine/v3',$7,'solo')`, []any{f.runID, f.projectID, f.definitionVersionID, f.manifestID, string(contextRaw), f.userID, profileHash}},
		{`INSERT INTO workflow_node_runs(id,run_id,node_key,node_type,status,output_revision_id,definition_node_id,slice_kind)
		  VALUES($1,$2,'quality','quality_gate','completed',$3,'quality','root')`, []any{f.qualityNodeID, f.runID, f.targetRevisionID}},
		{`INSERT INTO workflow_node_runs(id,run_id,node_key,node_type,status,definition_node_id,slice_kind)
		  VALUES($1,$2,'external-qualification','external_qualification_gate','pending','external-qualification','root')`, []any{f.gateNodeID, f.runID}},
		{`INSERT INTO application_build_manifests(id,project_id,workflow_run_id,schema_version,content_store,content_ref,content_hash,manifest_hash,status,created_by,root_manifest_id,workspace_revision_id,root_ordinal,manifest_group_key,delivery_slice_id)
		  VALUES($1,$2,$3,1,'mongo',$4,$5,$6,'consumed',$7,$1,$8,0,$9,$10)`, []any{
			f.buildManifestID, f.projectID, f.runID, "build-manifest/" + f.buildManifestID.String(),
			buildManifestContentHash, buildManifestHash, f.userID, f.targetRevisionID, f.manifestGroupID.String(), f.deliverySliceID.String(),
		}},
		{`INSERT INTO application_build_contracts(id,project_id,build_manifest_id,build_manifest_hash,full_stack_template_id,full_stack_template_hash,schema_version,compiler_version,compiler_hash,content_store,content_ref,content_hash,contract_hash,status,must_count,must_ready_count,obligation_count,source_count,template_release_count,blocking_count,conflict_count,created_by)
		  VALUES($1,$2,$3,$4,$5,$6,'application-build-contract/v2','worksflow-constraint-compiler/v7',$7,'mongo',$8,$9,$10,'ready',1,1,1,0,0,0,0,$11)`, []any{
			f.buildContractID, f.projectID, f.buildManifestID, buildManifestHash,
			f.fullStackTemplateID, strings.Repeat("1", 64), strings.Repeat("9", 64),
			"build-contract/" + f.buildContractID.String(), buildContractContentHash, buildContractHash, f.userID,
		}},
		{`INSERT INTO implementation_proposals(
		  id,project_id,build_manifest_id,status,version,content_store,content_ref,content_hash,payload_hash,
		  operation_count,accepted_count,rejected_count,created_by,applied_by,applied_at,
		  application_build_contract_id,application_build_contract_hash
		) VALUES($1,$2,$3,'applied',1,'mongo',$4,$5,$5,0,0,0,$6,$6,date_trunc('milliseconds',now()),$7,$8)`, []any{
			f.implementationID, f.projectID, f.buildManifestID, "implementation/" + f.implementationID.String(), emptyHash,
			f.userID, f.buildContractID, buildContractHash,
		}},
		{`UPDATE artifact_revisions SET implementation_proposal_id=$2 WHERE id=$1`, []any{f.targetRevisionID, f.implementationID}},
		{`INSERT INTO quality_runs(
		  id,project_id,workflow_run_id,workspace_artifact_id,workspace_revision_id,workspace_content_hash,report_artifact_id,report_revision_id,
		  status,score,runner_version,sandbox_kind,created_by,started_at,completed_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'passed',100,'canary','container',$9,
		  date_trunc('milliseconds',now()),date_trunc('milliseconds',now()))`, []any{
			f.qualityRunID, f.projectID, f.runID, f.targetArtifactID, f.targetRevisionID, emptyHash,
			f.reportArtifactID, f.reportRevisionID, f.userID,
		}},
		{`INSERT INTO application_build_contract_obligations(contract_id,obligation_id,level,kind,source_artifact_id,source_revision_id,source_content_hash,source_anchor_id,oracle_ids,depends_on,waivable,status)
		  VALUES($1,'canary','must','workspace',$2,$3,$4,'root','["oracle"]','[]',false,'ready')`, []any{f.buildContractID, f.targetArtifactID, f.targetRevisionID, emptyHash}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			fail("seed Workflow Input fixture", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit Workflow Input fixture: %v", err)
	}
	policy := seedCurrentQualificationPolicy(
		t, ctx, database, f.projectID, "sha256:"+profileHash,
	)
	f.policyID = policy.AuthorityID
	f.policyRecord = policy.Record
	candidate["qualificationPolicy"] = map[string]any{
		"authorityHash":      policy.AuthorityHash,
		"authorityId":        policy.AuthorityID.String(),
		"externalGatePolicy": "external-qualification/v1",
	}
	f.candidateRaw = mustWorkflowInputJSON(t, candidate)
	return f
}

func assertWorkflowInputProfileHashTamper(t *testing.T, ctx context.Context, database *sql.DB, f workflowInputCanary) {
	t.Helper()
	const exact = "854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104"
	// The pre-activation draft profile hash must be rejected just like any
	// other mismatched identity; immutable facts cannot be silently relabelled.
	tampered := "aca0fbcc902ad0b51da4beb7df9c5f4ab58036540aa4046a3f62e848728b37ef"
	setHash := func(value string) {
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_definition_versions
			SET execution_profile_hash=$2,content=jsonb_set(content,'{executionProfile,hash}',to_jsonb($2::text)) WHERE id=$1`, f.definitionVersionID, value); err != nil {
			_ = tx.Rollback()
			t.Fatalf("set definition profile hash: %v", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET execution_profile_hash=$2 WHERE id=$1`, f.runID, value); err != nil {
			_ = tx.Rollback()
			t.Fatalf("set run profile hash: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit profile hash fixture: %v", err)
		}
	}
	setHash(tampered)
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = freezeWorkflowInputCanary(ctx, tx, f)
	_ = tx.Rollback()
	if err == nil || !strings.Contains(err.Error(), "activatable v3 gate") {
		t.Fatalf("tampered v3 profile hash freeze error = %v", err)
	}
	setHash(exact)
}

func assertWorkflowInputPolicyBindingGuards(t *testing.T, ctx context.Context, database *sql.DB, f workflowInputCanary) {
	t.Helper()
	assertRejected := func(name string, candidateRaw []byte) {
		t.Helper()
		probe := f
		probe.candidateRaw = candidateRaw
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = freezeWorkflowInputCanary(ctx, tx, probe)
		_ = tx.Rollback()
		if err == nil || !strings.Contains(err.Error(), "qualification policy") {
			t.Fatalf("%s policy binding freeze error = %v", name, err)
		}
	}

	assertRejected("hash-mismatched", workflowInputCandidateWithPolicy(
		t, f.candidateRaw, f.policyID, workflowInputDigest([]byte("wrong-policy-hash")),
	))
	assertRejected("caller-invented", workflowInputCandidateWithPolicy(
		t, f.candidateRaw, uuid.New(), workflowInputDigest([]byte("invented-policy")),
	))

	suspended := compileWorkflowInputPolicySuccessor(t, f, qualificationpolicy.AuthorityStatusSuspended)
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var authorityID uuid.UUID
	var authorityHash, status string
	var generation int64
	var inserted bool
	if err := tx.QueryRowContext(
		ctx, qualificationPolicyIssueSQL, qualificationPolicyIssueArguments(suspended)...,
	).Scan(&authorityID, &authorityHash, &generation, &status, &inserted); err != nil {
		_ = tx.Rollback()
		t.Fatalf("issue suspended policy probe: %v", err)
	}
	if !inserted || authorityID != suspended.Command.AuthorityID {
		_ = tx.Rollback()
		t.Fatalf("suspended policy probe projection = %s inserted=%t", authorityID, inserted)
	}
	probe := f
	probe.candidateRaw = workflowInputCandidateWithPolicy(
		t, f.candidateRaw, suspended.Command.AuthorityID, suspended.AuthorityHash,
	)
	_, freezeErr := freezeWorkflowInputCanary(ctx, tx, probe)
	_ = tx.Rollback()
	if freezeErr == nil || !strings.Contains(freezeErr.Error(), "qualification policy") {
		t.Fatalf("suspended current policy freeze error = %v", freezeErr)
	}
}

func assertWorkflowInputPolicySupersession(t *testing.T, ctx context.Context, database *sql.DB, f workflowInputCanary) {
	t.Helper()
	successor := compileWorkflowInputPolicySuccessor(t, f, qualificationpolicy.AuthorityStatusActive)
	issueQualificationPolicyRecord(t, ctx, database, successor)
	var bundle []byte
	err := database.QueryRowContext(ctx, `
SELECT value FROM assert_current_workflow_input_authority_v1($1) AS value`, f.authorityID).Scan(&bundle)
	if err == nil || !strings.Contains(err.Error(), "policy is no longer current") {
		t.Fatalf("superseded policy current-WIA assertion error = %v", err)
	}
	var exact bool
	if err := database.QueryRowContext(ctx, `
SELECT workflow_input_authority_record_is_exact($1)`, f.authorityID).Scan(&exact); err != nil || !exact {
		t.Fatalf("historical WIA after policy supersession exact=%t error=%v", exact, err)
	}
}

func compileWorkflowInputPolicySuccessor(
	t *testing.T,
	f workflowInputCanary,
	status string,
) qualificationpolicy.Record {
	t.Helper()
	compiler := newQualificationPolicyFixtureCompiler(
		t,
		f.projectID,
		f.policyRecord.Document.ExecutionProfile.Hash,
		nil,
	)
	first := compiler.compile(
		t,
		f.policyRecord.Command,
		f.policyRecord.Document.Status,
		f.policyRecord.IssuedAt,
	)
	if first.AuthorityHash != f.policyRecord.AuthorityHash ||
		!strings.EqualFold(first.Command.AuthorityID.String(), f.policyID.String()) {
		t.Fatal("could not reproduce the current policy fixture before compiling its successor")
	}
	return compiler.compile(t, qualificationpolicy.IssueCommand{
		OperationID:                   uuid.New(),
		AuthorityID:                   uuid.New(),
		PolicySourceID:                first.Command.PolicySourceID,
		ExpectedPreviousAuthorityHash: first.AuthorityHash,
	}, status, first.IssuedAt.Add(time.Minute))
}

func workflowInputCandidateWithPolicy(
	t *testing.T,
	encoded []byte,
	authorityID uuid.UUID,
	authorityHash string,
) []byte {
	t.Helper()
	var candidate map[string]any
	if err := json.Unmarshal(encoded, &candidate); err != nil {
		t.Fatal(err)
	}
	candidate["qualificationPolicy"] = map[string]any{
		"authorityHash":      authorityHash,
		"authorityId":        authorityID.String(),
		"externalGatePolicy": "external-qualification/v1",
	}
	return mustWorkflowInputJSON(t, candidate)
}

func assertWorkflowInputReviewRequirementExactSet(t *testing.T, ctx context.Context, database *sql.DB, f workflowInputCanary) {
	t.Helper()
	sourceArtifactID, sourceRevisionID := uuid.New(), uuid.New()
	var candidate map[string]any
	if err := json.Unmarshal(f.candidateRaw, &candidate); err != nil {
		t.Fatal(err)
	}
	workspaceCandidate := candidate["revisions"].([]any)[0]
	candidate["revisions"] = []any{map[string]any{
		"canonicalReviewRequired": true,
		"currencyPolicy":          "latest-approved-required",
		"purpose":                 "governed-source",
		"rawBytesHex":             hex.EncodeToString(f.revisionRaw),
		"revisionId":              sourceRevisionID.String(),
	}, workspaceCandidate}
	f.candidateRaw = mustWorkflowInputJSON(t, candidate)

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(id,project_id,kind,artifact_key,title,created_by)
		VALUES($1,$2,'product_requirements',$3,'Governed source',$4)`, sourceArtifactID, f.projectID,
		"governed-"+sourceArtifactID.String(), f.userID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare governed source artifact: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_revisions(
		id,artifact_id,revision_number,schema_version,content_store,content_ref,content_hash,byte_size,
		workflow_status,change_source,created_by,approved_at
	) VALUES($1,$2,1,1,'mongo',$3,$4,2,'approved','human',$5,date_trunc('milliseconds',now()))`,
		sourceRevisionID, sourceArtifactID, "revision/"+sourceRevisionID.String(), workflowInputDigest(f.revisionRaw), f.userID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare governed source revision: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET latest_revision_id=$2,latest_approved_revision_id=$2 WHERE id=$1`, sourceArtifactID, sourceRevisionID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare governed source pointers: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_build_contracts SET source_count=1 WHERE id=$1`, f.buildContractID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare governed source count: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_build_contract_sources(
		contract_id,ordinal,source_kind,purpose,required,artifact_id,revision_id,content_hash
	) VALUES($1,0,'product_requirements','governed-source',true,$2,$3,$4)`,
		f.buildContractID, sourceArtifactID, sourceRevisionID, workflowInputDigest(f.revisionRaw)); err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare governed source member: %v", err)
	}
	_, err = freezeWorkflowInputCanary(ctx, tx, f)
	_ = tx.Rollback()
	if err == nil || (!strings.Contains(err.Error(), "policy-derived source subset") &&
		!strings.Contains(err.Error(), "BuildContract bytes disagree with exact child projections")) {
		t.Fatalf("omitted governed-source review requirement freeze error = %v", err)
	}
}

func assertWorkflowInputAtomicCycle(t *testing.T, ctx context.Context, database *sql.DB, f workflowInputCanary) {
	t.Helper()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := freezeWorkflowInputCanary(ctx, tx, f); err != nil {
		_ = tx.Rollback()
		t.Fatalf("freeze before atomic-cycle canary: %v", err)
	}
	if err := tx.Commit(); err == nil || (!strings.Contains(err.Error(), "node attachment") &&
		!strings.Contains(err.Error(), "workflow_input_authority_activation_event_fk")) {
		t.Fatalf("authority without node/event cycle commit error = %v", err)
	}
}

func assertWorkflowInputCursorAtomicCycle(t *testing.T, ctx context.Context, database *sql.DB, f workflowInputCanary) {
	t.Helper()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := freezeWorkflowInputCanary(ctx, tx, f); err != nil {
		_ = tx.Rollback()
		t.Fatalf("freeze before cursor canary: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_node_runs
		SET status='waiting_qualification',input_authority_id=$2 WHERE id=$1`, f.gateNodeID, f.authorityID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_run_events(id,run_id,sequence,event_type,node_key,payload)
		VALUES($1,$2,6,'external_qualification_activated','external-qualification',$3)`, f.eventID, f.runID,
		`{"inputAuthorityId":"`+f.authorityID.String()+`","nodeRunId":"`+f.gateNodeID.String()+`"}`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := insertWorkflowInputActivationOutbox(ctx, tx, f); err != nil {
		_ = tx.Rollback()
		t.Fatalf("enqueue cursor-canary activation event: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET status='waiting_qualification' WHERE id=$1`, f.runID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil || !strings.Contains(err.Error(), "node attachment") {
		t.Fatalf("authority cycle without event cursor advancement commit error = %v", err)
	}
}

func assertWorkflowInputApplicationAtomicCycle(t *testing.T, ctx context.Context, database *sql.DB, f workflowInputCanary) {
	t.Helper()
	var canSetRole bool
	if err := database.QueryRowContext(ctx, `SELECT pg_has_role(current_user,'worksflow_application','MEMBER')`).Scan(&canSetRole); err != nil {
		t.Fatal(err)
	}
	if !canSetRole {
		t.Log("current PostgreSQL principal cannot SET ROLE worksflow_application; skipping effective-user canary")
		return
	}
	if _, err := database.ExecContext(ctx, `GRANT SELECT,UPDATE ON workflow_runs,workflow_node_runs TO worksflow_application;
		GRANT SELECT,INSERT ON workflow_run_events,outbox_events TO worksflow_application`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `REVOKE SELECT,UPDATE ON workflow_runs,workflow_node_runs FROM worksflow_application;
			REVOKE SELECT,INSERT ON workflow_run_events,outbox_events FROM worksflow_application`)
	})
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL ROLE worksflow_application`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := freezeWorkflowInputCanary(ctx, tx, f); err != nil {
		_ = tx.Rollback()
		t.Fatalf("application-role freeze: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_node_runs
		SET status='waiting_qualification',input_authority_id=$2 WHERE id=$1`, f.gateNodeID, f.authorityID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("application-role node activation: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_run_events(id,run_id,sequence,event_type,node_key,payload)
		VALUES($1,$2,6,'external_qualification_activated','external-qualification',$3)`, f.eventID, f.runID,
		`{"inputAuthorityId":"`+f.authorityID.String()+`","nodeRunId":"`+f.gateNodeID.String()+`"}`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("application-role activation event: %v", err)
	}
	if err := insertWorkflowInputActivationOutbox(ctx, tx, f); err != nil {
		_ = tx.Rollback()
		t.Fatalf("application-role activation outbox: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET status='waiting_qualification',event_cursor=6 WHERE id=$1`, f.runID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("application-role run activation: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("application-role deferred closure: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func assertWorkflowInputOutboxAtomicCycle(t *testing.T, ctx context.Context, database *sql.DB, f workflowInputCanary) {
	t.Helper()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := freezeWorkflowInputCanary(ctx, tx, f); err != nil {
		_ = tx.Rollback()
		t.Fatalf("freeze before outbox canary: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_node_runs
		SET status='waiting_qualification',input_authority_id=$2 WHERE id=$1`, f.gateNodeID, f.authorityID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_run_events(id,run_id,sequence,event_type,node_key,payload)
		VALUES($1,$2,6,'external_qualification_activated','external-qualification',$3)`, f.eventID, f.runID,
		`{"inputAuthorityId":"`+f.authorityID.String()+`","nodeRunId":"`+f.gateNodeID.String()+`"}`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs
		SET status='waiting_qualification',event_cursor=6 WHERE id=$1`, f.runID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil || !strings.Contains(err.Error(), "exact activation outbox event") {
		t.Fatalf("authority cycle without outbox commit error = %v", err)
	}
}

func assertWorkflowInputExactFreeze(t *testing.T, ctx context.Context, database *sql.DB, f workflowInputCanary) {
	t.Helper()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := freezeWorkflowInputCanary(ctx, tx, f)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("freeze exact Workflow Input Authority: %v", err)
	}
	if !strings.HasPrefix(hash, "sha256:") {
		_ = tx.Rollback()
		t.Fatalf("authority hash = %q", hash)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_node_runs
		SET status='waiting_qualification',input_authority_id=$2 WHERE id=$1`, f.gateNodeID, f.authorityID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("attach authority to gate: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_run_events(id,run_id,sequence,event_type,node_key,payload)
		VALUES($1,$2,6,'external_qualification_activated','external-qualification',$3)`, f.eventID, f.runID,
		`{"inputAuthorityId":"`+f.authorityID.String()+`","nodeRunId":"`+f.gateNodeID.String()+`"}`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("append exact activation event: %v", err)
	}
	if err := insertWorkflowInputActivationOutbox(ctx, tx, f); err != nil {
		_ = tx.Rollback()
		t.Fatalf("append exact activation outbox: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET status='waiting_qualification',event_cursor=6 WHERE id=$1`, f.runID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("advance run cursor: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit exact Workflow Input Authority cycle: %v", err)
	}

	var exact bool
	if err := database.QueryRowContext(ctx, `SELECT workflow_input_authority_record_is_exact($1)`, f.authorityID).Scan(&exact); err != nil || !exact {
		t.Fatalf("exact authority verifier = %t, error %v", exact, err)
	}
	var resolvedHash string
	if err := database.QueryRowContext(ctx, `SELECT value#>>'{authority,authority_hash}'
		FROM resolve_workflow_input_authority_for_node_v1($1,$2) AS value`, f.runID, f.gateNodeID).Scan(&resolvedHash); err != nil || resolvedHash != hash {
		t.Fatalf("resolve node authority hash = %q, error %v", resolvedHash, err)
	}
	var replayHash string
	if err := database.QueryRowContext(ctx, `SELECT authority_hash FROM freeze_workflow_input_authority_v1(
		$1,$2,$3,$4,5,$5,6,$6,$7,$8,$9,$10,$11)`,
		f.operationID, f.authorityID, f.runID, f.gateNodeID, f.eventID,
		f.definitionRaw, f.scopeRaw, f.nodeInputRaw, f.buildManifestRaw, f.buildContractRaw, f.candidateRaw,
	).Scan(&replayHash); err != nil || replayHash != hash {
		t.Fatalf("inspect-only replay hash = %q, error %v", replayHash, err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE workflow_input_authorities SET manifest_subject='tampered' WHERE authority_id=$1`, f.authorityID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("immutable authority update error = %v", err)
	}
	var changed map[string]any
	if err := json.Unmarshal(f.candidateRaw, &changed); err != nil {
		t.Fatal(err)
	}
	changed["manifestSubject"] = "changed-replay"
	changedRaw := mustWorkflowInputJSON(t, changed)
	if _, err := database.ExecContext(ctx, `SELECT authority_hash FROM freeze_workflow_input_authority_v1(
		$1,$2,$3,$4,5,$5,6,$6,$7,$8,$9,$10,$11)`,
		f.operationID, f.authorityID, f.runID, f.gateNodeID, f.eventID,
		f.definitionRaw, f.scopeRaw, f.nodeInputRaw, f.buildManifestRaw, f.buildContractRaw, changedRaw,
	); err == nil || !strings.Contains(err.Error(), "different or corrupt authority bytes") {
		t.Fatalf("changed same-operation replay error = %v", err)
	}
	for _, reserved := range []uuid.UUID{f.authorityID, f.operationID} {
		if _, err := database.ExecContext(ctx, `INSERT INTO workflow_run_events(id,run_id,sequence,event_type,payload)
			VALUES($1,$2,7,'unrelated','{}')`, reserved, f.runID); err == nil || !strings.Contains(err.Error(), "cannot be reused as an event") {
			t.Fatalf("future reserved identity collision %s error = %v", reserved, err)
		}
	}
	if _, err := database.ExecContext(ctx, `UPDATE workflow_run_events
		SET payload=payload || '{"extra":true}'::jsonb WHERE id=$1`, f.eventID); err == nil || !strings.Contains(err.Error(), "one exact closed payload") {
		t.Fatalf("activation event payload widening error = %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE workflow_run_events
		SET actor_id=$2 WHERE id=$1`, f.eventID, f.userID); err == nil || !strings.Contains(err.Error(), "actor and occurrence time are immutable") {
		t.Fatalf("activation event actor mutation error = %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE workflow_run_events
		SET created_at=created_at + interval '1 millisecond' WHERE id=$1`, f.eventID); err == nil || !strings.Contains(err.Error(), "actor and occurrence time are immutable") {
		t.Fatalf("activation event occurrence-time mutation error = %v", err)
	}
	movedEventID := uuid.New()
	tx, err = database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, exploitErr := tx.ExecContext(ctx, `UPDATE workflow_run_events
		SET id=$2,sequence=7 WHERE id=$1`, f.eventID, movedEventID)
	if exploitErr == nil {
		_, exploitErr = tx.ExecContext(ctx, `INSERT INTO workflow_run_events(
			id,run_id,sequence,event_type,node_key,payload,actor_id,created_at
		)
		SELECT $1,run_id,6,event_type,node_key,payload,$3,created_at + interval '1 millisecond'
		FROM workflow_run_events WHERE id=$2`, f.eventID, movedEventID, f.userID)
	}
	if exploitErr == nil {
		exploitErr = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if exploitErr == nil || !strings.Contains(exploitErr.Error(), "activation event identity is immutable") {
		t.Fatalf("activation event move-and-reinsert actor/time bypass error = %v", exploitErr)
	}
	tx, err = database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, exploitErr = tx.ExecContext(ctx, `WITH deleted AS (
		DELETE FROM workflow_run_events WHERE id=$1 RETURNING *
	)
	INSERT INTO workflow_run_events(
		id,run_id,sequence,event_type,node_key,payload,actor_id,created_at
	)
	SELECT id,run_id,sequence,event_type,node_key,payload,$2,created_at + interval '1 millisecond'
	FROM deleted`, f.eventID, f.userID)
	if exploitErr == nil {
		exploitErr = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if exploitErr == nil || !strings.Contains(exploitErr.Error(), "activation event identity is immutable") {
		t.Fatalf("activation event delete-and-reinsert actor/time bypass error = %v", exploitErr)
	}
}

func assertWorkflowInputCatalog(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	var tableCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_catalog.pg_class AS relation
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE namespace.nspname = current_schema()
  AND relation.relkind IN ('r','p')
  AND relation.relname IN (
    'workflow_input_authorities',
    'workflow_input_authority_identity_reservations',
    'workflow_input_authority_predecessors',
    'workflow_input_authority_manifests',
    'workflow_input_authority_revisions',
    'workflow_input_authority_review_receipts'
  )
`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 6 {
		t.Fatalf("Workflow Input Authority table count = %d, want 6", tableCount)
	}

	var functionCount, fixedSearchPathCount, securityDefinerCount int
	if err := database.QueryRowContext(ctx, `
SELECT
  count(*),
  count(*) FILTER (WHERE EXISTS (
    SELECT 1
    FROM pg_catalog.unnest(COALESCE(procedure.proconfig, ARRAY[]::text[])) AS setting
    WHERE setting LIKE 'search_path=%'
  )),
  count(*) FILTER (WHERE procedure.prosecdef)
FROM pg_catalog.pg_proc AS procedure
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
WHERE namespace.nspname = current_schema()
  AND procedure.proname IN (
    'workflow_input_raw_sha256',
    'workflow_input_authority_hash',
    'workflow_input_normalize_sha256',
    'workflow_input_uuid_is_exact',
    'workflow_input_timestamp_is_exact',
    'workflow_input_canonical_jsonb_bytes',
    'reject_workflow_input_authority_mutation',
    'guard_workflow_node_stable_identity_v1',
    'workflow_input_authority_record_is_exact',
    'workflow_input_authority_replay_is_exact_v1',
    'guard_workflow_input_authority_event_identity_v1',
    'validate_workflow_input_authority_closure_v1',
    'freeze_workflow_input_authority_v1',
    'workflow_input_authority_bundle_v1',
    'inspect_workflow_input_authority_operation_v1',
    'resolve_workflow_input_authority_v1',
    'resolve_workflow_input_authority_for_node_v1',
    'assert_current_workflow_input_authority_v1'
  )
`).Scan(&functionCount, &fixedSearchPathCount, &securityDefinerCount); err != nil {
		t.Fatal(err)
	}
	if functionCount != 18 || fixedSearchPathCount != 18 || securityDefinerCount != 7 {
		t.Fatalf(
			"Workflow Input Authority functions=%d fixed_search_path=%d security_definer=%d, want 18/18/7",
			functionCount, fixedSearchPathCount, securityDefinerCount,
		)
	}

	var triggerCount, deferredTriggerCount int
	if err := database.QueryRowContext(ctx, `
SELECT
  count(*),
  count(*) FILTER (WHERE trigger.tgdeferrable AND trigger.tginitdeferred)
FROM pg_catalog.pg_trigger AS trigger
JOIN pg_catalog.pg_class AS relation ON relation.oid = trigger.tgrelid
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE NOT trigger.tgisinternal
  AND namespace.nspname = current_schema()
  AND trigger.tgname IN (
    'workflow_input_authorities_immutable',
    'workflow_input_authority_identity_reservations_immutable',
    'workflow_input_authority_predecessors_immutable',
    'workflow_input_authority_manifests_immutable',
    'workflow_input_authority_revisions_immutable',
    'workflow_input_authority_review_receipts_immutable',
    'workflow_node_stable_identity_v1_immutable',
    'workflow_input_authorities_exact_closure',
    'workflow_input_authority_predecessors_exact_closure',
    'workflow_input_authority_manifests_exact_closure',
    'workflow_input_authority_revisions_exact_closure',
    'workflow_input_authority_review_receipts_exact_closure',
    'workflow_node_input_authority_exact_closure',
    'workflow_input_authority_event_exact_closure',
    'workflow_input_authority_event_identity_guard'
  )
`).Scan(&triggerCount, &deferredTriggerCount); err != nil {
		t.Fatal(err)
	}
	if triggerCount != 15 || deferredTriggerCount != 7 {
		t.Fatalf("Workflow Input Authority triggers=%d deferred=%d, want 15/7", triggerCount, deferredTriggerCount)
	}
	var eventIdentityTriggerType int
	if err := database.QueryRowContext(ctx, `
SELECT trigger.tgtype::integer
FROM pg_catalog.pg_trigger AS trigger
JOIN pg_catalog.pg_class AS relation ON relation.oid = trigger.tgrelid
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE namespace.nspname = current_schema()
  AND relation.relname = 'workflow_run_events'
  AND trigger.tgname = 'workflow_input_authority_event_identity_guard'
  AND NOT trigger.tgisinternal
`).Scan(&eventIdentityTriggerType); err != nil {
		t.Fatal(err)
	}
	if eventIdentityTriggerType != 31 {
		t.Fatalf("Workflow Input event identity trigger type = %d, want row-level BEFORE INSERT/UPDATE/DELETE (31)", eventIdentityTriggerType)
	}

	var directTableGrants int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_catalog.pg_roles AS principal
CROSS JOIN (VALUES
  ('workflow_input_authorities'),
  ('workflow_input_authority_identity_reservations'),
  ('workflow_input_authority_predecessors'),
  ('workflow_input_authority_manifests'),
  ('workflow_input_authority_revisions'),
  ('workflow_input_authority_review_receipts')
) AS protected_table(name)
WHERE principal.rolname IN (
  'worksflow_application',
  'worksflow_qualification_plan_operator',
  'worksflow_qualification_promotion_operator'
)
  AND pg_catalog.has_table_privilege(
    principal.oid,
    pg_catalog.format('%I.%I', current_schema(), protected_table.name),
    'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER'
  )
`).Scan(&directTableGrants); err != nil {
		t.Fatal(err)
	}
	if directTableGrants != 0 {
		t.Fatalf("Workflow Input Authority direct runtime table grants = %d, want 0", directTableGrants)
	}

	var publicFunctionGrants int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_catalog.pg_proc AS procedure
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
CROSS JOIN LATERAL pg_catalog.aclexplode(
  COALESCE(procedure.proacl, pg_catalog.acldefault('f', procedure.proowner))
) AS privilege
WHERE namespace.nspname = current_schema()
  AND procedure.proname IN (
    'workflow_input_raw_sha256',
    'workflow_input_authority_hash',
    'workflow_input_normalize_sha256',
    'workflow_input_uuid_is_exact',
    'workflow_input_timestamp_is_exact',
    'workflow_input_canonical_jsonb_bytes',
    'reject_workflow_input_authority_mutation',
    'guard_workflow_node_stable_identity_v1',
    'workflow_input_authority_record_is_exact',
    'workflow_input_authority_replay_is_exact_v1',
    'guard_workflow_input_authority_event_identity_v1',
    'validate_workflow_input_authority_closure_v1',
    'freeze_workflow_input_authority_v1',
    'workflow_input_authority_bundle_v1',
    'inspect_workflow_input_authority_operation_v1',
    'resolve_workflow_input_authority_v1',
    'resolve_workflow_input_authority_for_node_v1',
    'assert_current_workflow_input_authority_v1'
  )
  AND privilege.grantee = 0
  AND privilege.privilege_type = 'EXECUTE'
`).Scan(&publicFunctionGrants); err != nil {
		t.Fatal(err)
	}
	if publicFunctionGrants != 0 {
		t.Fatalf("Workflow Input Authority PUBLIC function grants = %d, want 0", publicFunctionGrants)
	}

	var missingCapabilityGrants int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM (VALUES
  ('worksflow_application', 'freeze_workflow_input_authority_v1(uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb)'),
  ('worksflow_application', 'inspect_workflow_input_authority_operation_v1(uuid)'),
  ('worksflow_application', 'resolve_workflow_input_authority_for_node_v1(uuid,uuid)'),
  ('worksflow_qualification_plan_operator', 'resolve_workflow_input_authority_v1(uuid)'),
  ('worksflow_qualification_promotion_operator', 'assert_current_workflow_input_authority_v1(uuid)')
) AS expected(principal_name, signature)
JOIN pg_catalog.pg_roles AS principal ON principal.rolname = expected.principal_name
WHERE NOT pg_catalog.has_function_privilege(
  principal.oid,
  pg_catalog.to_regprocedure(pg_catalog.format('%I.%s', current_schema(), expected.signature)),
  'EXECUTE'
)
`).Scan(&missingCapabilityGrants); err != nil {
		t.Fatal(err)
	}
	if missingCapabilityGrants != 0 {
		t.Fatalf("Workflow Input Authority missing capability grants = %d, want 0", missingCapabilityGrants)
	}
}

func assertWorkflowInputTamperDetection(t *testing.T, ctx context.Context, database *sql.DB, f workflowInputCanary) {
	t.Helper()
	assertRejected := func(name, statement string, args ...any) {
		t.Helper()
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply %s tamper: %v", name, err)
		}
		var exact bool
		if err := tx.QueryRowContext(ctx, `SELECT workflow_input_authority_record_is_exact($1)`, f.authorityID).Scan(&exact); err != nil {
			_ = tx.Rollback()
			t.Fatalf("verify %s tamper: %v", name, err)
		}
		if exact {
			_ = tx.Rollback()
			t.Fatalf("Workflow Input Authority verifier accepted %s tamper", name)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("restore %s tamper: %v", name, err)
		}
	}

	assertRejected(
		"identity reservation",
		`DELETE FROM workflow_input_authority_identity_reservations
		 WHERE authority_id=$1 AND identity_kind='activation-event'`,
		f.authorityID,
	)
	assertRejected(
		"parent member count",
		`UPDATE workflow_input_authorities SET manifest_count=manifest_count+1 WHERE authority_id=$1`,
		f.authorityID,
	)
	assertRejected(
		"manifest membership",
		`UPDATE workflow_input_authority_manifests SET role='predecessor'
		 WHERE authority_id=$1 AND role='run'`,
		f.authorityID,
	)
	assertRejected(
		"BuildContract obligation projection",
		`UPDATE application_build_contract_obligations
		 SET source_anchor_id='tampered'
		 WHERE contract_id=$1 AND obligation_id='canary'`,
		f.buildContractID,
	)

	var exact bool
	if err := database.QueryRowContext(ctx, `SELECT workflow_input_authority_record_is_exact($1)`, f.authorityID).Scan(&exact); err != nil || !exact {
		t.Fatalf("Workflow Input Authority did not recover after rolled-back tamper canaries: exact=%t error=%v", exact, err)
	}
}

func freezeWorkflowInputCanary(ctx context.Context, tx *sql.Tx, f workflowInputCanary) (string, error) {
	var hash string
	err := tx.QueryRowContext(ctx, `SELECT authority_hash FROM freeze_workflow_input_authority_v1(
		$1,$2,$3,$4,5,$5,6,$6,$7,$8,$9,$10,$11)`,
		f.operationID, f.authorityID, f.runID, f.gateNodeID, f.eventID,
		f.definitionRaw, f.scopeRaw, f.nodeInputRaw, f.buildManifestRaw, f.buildContractRaw, f.candidateRaw,
	).Scan(&hash)
	return hash, err
}

func insertWorkflowInputActivationOutbox(ctx context.Context, tx *sql.Tx, f workflowInputCanary) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO outbox_events(
  id,aggregate_type,aggregate_id,event_type,subject,payload,headers,
  attempts,available_at,published_at,last_error,created_at
)
SELECT event.id,'workflow_run',event.run_id::text,event.event_type,
       'worksflow.workflow.run.event',
       jsonb_build_object(
         'id',event.id::text,
         'projectId',$2::text,
         'runId',event.run_id::text,
         'sequence',event.sequence,
         'type',event.event_type,
         'occurredAt',to_char(event.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
         'payload',event.payload,
         'nodeKey',event.node_key
       ),
       '{}'::jsonb,0,event.created_at,NULL,NULL,event.created_at
FROM workflow_run_events AS event
WHERE event.id=$1`, f.eventID, f.projectID)
	return err
}

func assertWorkflowInputDownFence(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	down, err := files.ReadFile("000078_workflow_input_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err == nil || !strings.Contains(err.Error(), "cannot roll back Workflow Input Authority") {
		t.Fatalf("populated Workflow Input Authority rollback error = %v", err)
	}
}

func workflowInputDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func mustWorkflowInputJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
