package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/workflow"
)

const workflowExecutionProfileV3Migration = "000079_workflow_execution_profile_v3_external_qualification_gate.up.sql"

func TestWorkflowExecutionProfileV3MigrationDeclaresClosedNonWaivableGate(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(workflowExecutionProfileV3Migration)
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000079_workflow_execution_profile_v3_external_qualification_gate.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upText := string(up)
	for _, expected := range []string{
		"LOCK TABLE projects IN ACCESS EXCLUSIVE MODE",
		"worksflow:workflow-input-authority-migration:v1",
		"LOCK TABLE workflow_definition_versions IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE workflow_runs IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE workflow_node_runs IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE workflow_input_authorities IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_policy_authorities IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_policy_review_defaults IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_policy_exact_approved_sources IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_policy_identity_reservations IN ACCESS EXCLUSIVE MODE",
		"CREATE FUNCTION workflow_execution_profile_v3_definition_is_database_admissible(p_document jsonb)",
		"workflow_input_canonical_jsonb_bytes(p_document - 'hash')",
		"WITH RECURSIVE document_values(value)",
		"NEW.content_hash IS DISTINCT FROM NEW.content->>'hash'",
		"workflow-engine/v3",
		"854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104",
		"'external_qualification_gate'",
		"v_external_id <> 'external-qualification'",
		"NEW.node_key IS DISTINCT FROM 'external-qualification'",
		"'blocking', true",
		"'gateName', 'external-qualification'",
		"'inputAuthoritySchema', 'worksflow-workflow-input-authority/v1'",
		"'promotionProtocol', 'worksflow-qualification-promotion-consume/v2'",
		"'receiptSchema', 'worksflow-qualification-receipt/v3'",
		"'waiverPolicy', 'never'",
		"v_config->>'gateName' <> 'release'",
		"v_config->'blocking' IS DISTINCT FROM 'true'::jsonb",
		"v_config->>'environment' <> 'production'",
		"OR v_edge ? 'mapping'",
		"jsonb_array_length(v_edges) <> jsonb_array_length(v_nodes) - 1",
		"ai_transform:refine_project_brief>human_edit:project_brief>review_gate",
		"artifact_input>fan_out:blueprint_selection_page>transform:selection_passthrough",
		"'artifact_input','ai_transform','human_edit','review_gate','fan_out','merge'",
		"'manifest_compiler','workbench_build','quality_gate'",
		"'external_qualification_gate','publish','transform'",
		"CREATE TRIGGER workflow_execution_profile_v3_definition_guard",
		"CREATE TRIGGER workflow_execution_profile_v3_run_guard",
		"workflow-engine/v3 cannot complete before the private qualification handoff",
		"CREATE TRIGGER external_qualification_gate_node_v3_guard",
		"DO $workflow_execution_profile_v3_existing_guard$",
		"pre-existing workflow-engine/v3 definition is not exact",
		"version.execution_profile_version IS DISTINCT FROM 'workflow-engine/v3'",
		"pre-existing workflow run does not bind the exact workflow-engine/v3 definition",
		"pre-existing workflow-engine/v3 node lacks exact stable definition identity",
		"pre-existing external qualification gate used a generic workflow transition",
		"pre-existing workflow-engine/v3 run lacks its exact external qualification gate closure",
		"NEW.slice_kind IS DISTINCT FROM 'root' OR NEW.slice_id IS NOT NULL",
		"NEW.attempt <> 0 OR NEW.lease_owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL",
		"NEW.started_at IS NOT NULL OR NEW.completed_at IS NOT NULL OR NEW.failure IS NOT NULL",
		"NEW.input_manifest_id IS NOT NULL",
		"NEW.output_proposal_id IS NOT NULL",
		"NEW.status NOT IN ('pending','waiting_qualification','cancelled','stale')",
		"OLD.status IN ('cancelled','stale') AND NEW.status <> OLD.status",
		"NEW.status = 'pending' AND NEW.input_authority_id IS NOT NULL",
		"NEW.status = 'waiting_qualification'",
		"authority.workflow_run_id = NEW.run_id",
		"authority.node_run_id = NEW.id",
		"NEW.output_revision_id IS NOT NULL",
		"dedicated external qualification gate cannot use a generic workflow transition",
		"CREATE CONSTRAINT TRIGGER workflow_execution_profile_v3_run_exact_closure",
		"CREATE CONSTRAINT TRIGGER workflow_execution_profile_v3_node_exact_closure",
		"DEFERRABLE INITIALLY DEFERRED",
		"IF TG_OP = 'DELETE' THEN",
		"workflow-engine/v3 run lacks its exact external qualification gate closure",
		"v_run.status <> 'waiting_qualification' AND EXISTS",
		"'waiting_qualification'",
		"REVOKE ALL ON FUNCTION %I.%s FROM PUBLIC",
		"SET search_path TO pg_catalog, %I",
	} {
		if !strings.Contains(upText, expected) {
			t.Fatalf("workflow profile v3 migration is missing %q", expected)
		}
	}
	if strings.Count(upText, "CREATE FUNCTION ") != 5 {
		t.Fatalf("workflow profile v3 function count = %d, want 5", strings.Count(upText, "CREATE FUNCTION "))
	}
	if strings.Count(upText, "SECURITY DEFINER") != 4 {
		t.Fatalf("workflow profile v3 security-definer trigger function count = %d, want 4", strings.Count(upText, "SECURITY DEFINER"))
	}
	if strings.Count(upText, "CREATE TRIGGER ") != 3 || strings.Count(upText, "CREATE CONSTRAINT TRIGGER ") != 2 {
		t.Fatalf(
			"workflow profile v3 trigger counts = ordinary %d constraint %d, want 3 and 2",
			strings.Count(upText, "CREATE TRIGGER "), strings.Count(upText, "CREATE CONSTRAINT TRIGGER "),
		)
	}
	for _, forbidden := range []string{
		"aca0fbcc902ad0b51da4beb7df9c5f4ab58036540aa4046a3f62e848728b37ef",
		"GRANT EXECUTE",
		"GRANT SELECT",
		"GRANT INSERT",
		"GRANT UPDATE",
		"manualApproval",
		"retryPolicy",
		"qualificationRunner",
	} {
		if strings.Contains(strings.ToLower(upText), strings.ToLower(forbidden)) {
			t.Fatalf("workflow profile v3 migration contains forbidden authority surface %q", forbidden)
		}
	}
	assertWorkflowExecutionProfileV3LockOrder(t, upText)

	downText := string(down)
	for _, expected := range []string{
		"LOCK TABLE projects IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE workflow_definition_versions IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE workflow_runs IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE workflow_node_runs IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE workflow_input_authorities IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_policy_authorities IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_policy_review_defaults IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_policy_exact_approved_sources IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_policy_identity_reservations IN ACCESS EXCLUSIVE MODE",
		"OR EXISTS (SELECT 1 FROM workflow_input_authorities)",
		"cannot roll back workflow-engine/v3 after a definition, run, gate, or Workflow Input Authority exists",
		"DROP TRIGGER IF EXISTS workflow_execution_profile_v3_node_exact_closure",
		"DROP TRIGGER IF EXISTS external_qualification_gate_node_v3_guard",
		"DROP FUNCTION IF EXISTS workflow_execution_profile_v3_definition_is_database_admissible(jsonb)",
		"status IN ('pending','ready','running','waiting_input','waiting_review','completed','failed','cancelled','stale')",
		"status IN ('pending','running','waiting_input','waiting_review','completed','failed','cancelled','stale')",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("workflow profile v3 rollback is missing %q", expected)
		}
	}
	assertWorkflowExecutionProfileV3LockOrder(t, downText)
}

func assertWorkflowExecutionProfileV3LockOrder(t *testing.T, text string) {
	t.Helper()
	previous := -1
	for _, lock := range []string{
		"LOCK TABLE projects IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE workflow_runs IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE workflow_node_runs IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE workflow_definition_versions IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_policy_authorities IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_policy_review_defaults IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_policy_exact_approved_sources IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE qualification_policy_identity_reservations IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE workflow_input_authorities IN ACCESS EXCLUSIVE MODE",
	} {
		position := strings.Index(text, lock)
		if position < 0 || position <= previous {
			t.Fatalf("workflow profile v3 lock %q is absent or out of order", lock)
		}
		previous = position
	}
}

func TestWorkflowExecutionProfileV3MigrationPostgresCanary(t *testing.T) {
	ctx, base, dsn := qualificationReceiptV3Postgres(t)
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "workflow_profile_v3_")
	applyPostgresMigrationsForCanary(t, database)

	definition, actorID := workflowExecutionProfileV3MigrationDefinition(t)
	content, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	var exact bool
	if err := database.QueryRowContext(
		ctx, `SELECT workflow_execution_profile_v3_definition_is_database_admissible($1::jsonb)`, content,
	).Scan(&exact); err != nil {
		t.Fatal(err)
	}
	if !exact {
		t.Fatal("database rejected the exact Go-frozen workflow-engine/v3 definition")
	}
	projectID, versionID, runID, externalNodeID := seedWorkflowExecutionProfileV3Run(
		t, ctx, database, definition, actorID,
	)
	selectionDefinition := workflowExecutionProfileV3BlueprintSelectionDefinition(t, actorID)
	selectionContent, err := json.Marshal(selectionDefinition)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(
		ctx, `SELECT workflow_execution_profile_v3_definition_is_database_admissible($1::jsonb)`, selectionContent,
	).Scan(&exact); err != nil {
		t.Fatal(err)
	}
	if !exact {
		t.Fatal("database rejected the exact Go-frozen Blueprint-selection workflow-engine/v3 definition")
	}
	if err := workflow.ValidateDefinitionForExecutionProfile(
		selectionDefinition, workflow.WorkflowExecutionProfileV3Ref(),
	); err != nil {
		t.Fatalf("Go frozen validator rejected Blueprint-selection v3 fixture: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "null edge", mutate: func(document map[string]any) {
			document["edges"] = append(document["edges"].([]any), nil)
		}},
		{name: "empty edge", mutate: func(document map[string]any) {
			document["edges"] = append(document["edges"].([]any), map[string]any{})
		}},
		{name: "unknown node type", mutate: func(document map[string]any) {
			document["nodes"].([]any)[1].(map[string]any)["type"] = "future_runner"
		}},
		{name: "missing top-level identity", mutate: func(document map[string]any) {
			delete(document, "id")
		}},
		{name: "invalid endpoint", mutate: func(document map[string]any) {
			document["edges"].([]any)[0].(map[string]any)["from"] = "missing-node"
		}},
		{name: "unknown config member", mutate: func(document map[string]any) {
			for _, raw := range document["nodes"].([]any) {
				node := raw.(map[string]any)
				if node["type"] == "workbench_build" {
					node["workbenchBuild"].(map[string]any)["retryPolicy"] = "widened"
				}
			}
		}},
		{name: "missing required config member", mutate: func(document map[string]any) {
			for _, raw := range document["nodes"].([]any) {
				node := raw.(map[string]any)
				if node["type"] == "review_gate" {
					delete(node["reviewGate"].(map[string]any), "requiredRole")
					break
				}
			}
		}},
		{name: "runtime-invalid semantic order", mutate: func(document map[string]any) {
			for _, raw := range document["nodes"].([]any) {
				node := raw.(map[string]any)
				config, ok := node["aiTransform"].(map[string]any)
				if ok && config["jobType"] == "refine_project_brief" {
					config["jobType"] = "generate_prototype"
					config["outputSchemaVersion"] = "prototype-proposal/v1"
					break
				}
			}
		}},
		{name: "noncanonical timestamp", mutate: func(document map[string]any) {
			document["createdAt"] = "2026-01-02T03:04:05.000Z"
		}},
		{name: "runtime-invalid timestamp", mutate: func(document map[string]any) {
			document["createdAt"] = "2026-01-02T24:00:00Z"
		}},
		{name: "multiple typed configs", mutate: func(document map[string]any) {
			for _, raw := range document["nodes"].([]any) {
				node := raw.(map[string]any)
				if node["type"] == "quality_gate" {
					node["publish"] = map[string]any{"environment": "production", "requiredRole": "owner", "allowRollback": false}
				}
			}
		}},
		{name: "nonempty edge mapping", mutate: func(document map[string]any) {
			for _, raw := range document["edges"].([]any) {
				edge := raw.(map[string]any)
				if edge["from"] == "quality" && edge["to"] == "external-qualification" {
					edge["mapping"] = map[string]any{"passed": "status"}
				}
			}
		}},
	} {
		t.Run("definition "+test.name, func(t *testing.T) {
			mutated := workflowExecutionProfileV3MutatedDocument(t, content, test.mutate, true)
			if err := database.QueryRowContext(
				ctx, `SELECT workflow_execution_profile_v3_definition_is_database_admissible($1::jsonb)`, mutated,
			).Scan(&exact); err != nil {
				t.Fatal(err)
			}
			if exact {
				t.Fatalf("database accepted profile-v3 %s", test.name)
			}
			var rejected domain.WorkflowDefinition
			if err := json.Unmarshal(mutated, &rejected); err == nil {
				if err := workflow.ValidateDefinitionForExecutionProfile(
					rejected, workflow.WorkflowExecutionProfileV3Ref(),
				); err == nil {
					t.Fatalf("Go frozen validator unexpectedly accepted profile-v3 %s", test.name)
				}
			}
		})
	}
	invalidEmbeddedHash := workflowExecutionProfileV3MutatedDocument(t, content, func(document map[string]any) {
		document["hash"] = strings.Repeat("0", 64)
	}, false)
	if err := database.QueryRowContext(
		ctx, `SELECT workflow_execution_profile_v3_definition_is_database_admissible($1::jsonb)`, invalidEmbeddedHash,
	).Scan(&exact); err != nil {
		t.Fatal(err)
	}
	if exact {
		t.Fatal("database accepted a profile-v3 document with an invalid embedded content hash")
	}
	htmlDocument := workflowExecutionProfileV3MutatedDocument(t, content, func(document map[string]any) {
		document["name"] = "Migration <workflow-engine/v3>"
	}, false)
	var postgresRehashed string
	if err := database.QueryRowContext(ctx, `
WITH payload AS (
  SELECT $1::jsonb - 'hash' AS value
), document AS (
  SELECT value || jsonb_build_object(
    'hash', encode(sha256(workflow_input_canonical_jsonb_bytes(value)), 'hex')
  ) AS value
  FROM payload
)
SELECT value::text, workflow_execution_profile_v3_definition_is_database_admissible(value)
FROM document
`, htmlDocument).Scan(&postgresRehashed, &exact); err != nil {
		t.Fatal(err)
	}
	if exact {
		t.Fatal("database accepted a PostgreSQL-hashed definition that changes under Go JSON escaping")
	}
	var postgresOnlyHash domain.WorkflowDefinition
	if err := json.Unmarshal([]byte(postgresRehashed), &postgresOnlyHash); err != nil {
		t.Fatal(err)
	}
	if err := workflow.ValidateDefinitionForExecutionProfile(
		postgresOnlyHash, workflow.WorkflowExecutionProfileV3Ref(),
	); err == nil {
		t.Fatal("Go frozen validator unexpectedly accepted the PostgreSQL-only content hash")
	}
	mismatchedHashDefinitionID := uuid.New()
	if _, err := database.ExecContext(ctx,
		`INSERT INTO workflow_definitions (id,project_id,workflow_key,title,created_by) VALUES ($1,$2,'hash-mismatch-v3','Hash mismatch v3',$3)`,
		mismatchedHashDefinitionID, projectID, actorID,
	); err != nil {
		t.Fatal(err)
	}
	_, err = database.ExecContext(ctx, `
INSERT INTO workflow_definition_versions (
  id,definition_id,version,schema_version,content,content_hash,
  execution_profile_version,execution_profile_hash,validation_report,published,created_by,created_at
) VALUES ($1,$2,1,6,$3,$4,'workflow-engine/v3',$5,'{"valid":true}',true,$6,$7)
`, uuid.New(), mismatchedHashDefinitionID, content, strings.Repeat("f", 64),
		workflow.WorkflowExecutionProfileV3Ref().Hash, actorID, time.Now().UTC())
	assertPostgresCode(t, err, "23514", "row content hash mismatch")
	_, err = database.ExecContext(ctx, `
INSERT INTO workflow_definition_versions (
  id,definition_id,version,schema_version,content,content_hash,
  execution_profile_version,execution_profile_hash,validation_report,published,created_by,created_at
) VALUES ($1,$2,1,6,$3,$4,'workflow-engine/v2',$5,'{"valid":true}',true,$6,$7)
`, uuid.New(), mismatchedHashDefinitionID, content, definition.Hash,
		workflow.CurrentWorkflowExecutionProfileRef().Hash, actorID, time.Now().UTC())
	assertPostgresCode(t, err, "23514", "row execution profile mismatch")
	_, err = database.ExecContext(ctx, `
INSERT INTO workflow_definition_versions (
  id,definition_id,version,schema_version,content,content_hash,
  execution_profile_version,execution_profile_hash,validation_report,published,created_by,created_at
) VALUES ($1,$2,1,6,$3,$4,'workflow-engine/v3',$5,'{"valid":true}',true,$6,$7)
`, uuid.New(), mismatchedHashDefinitionID, content, definition.Hash,
		"aca0fbcc902ad0b51da4beb7df9c5f4ab58036540aa4046a3f62e848728b37ef", actorID, time.Now().UTC())
	assertPostgresCode(t, err, "23514", "pre-activation draft profile hash")

	for _, test := range []struct {
		name  string
		query string
		args  []any
	}{
		{name: "runner ready", query: `UPDATE workflow_node_runs SET status='ready' WHERE id=$1`, args: []any{externalNodeID}},
		{name: "runner lease", query: `UPDATE workflow_node_runs SET status='running', attempt=1, lease_owner='worker', lease_expires_at=clock_timestamp() + interval '1 minute' WHERE id=$1`, args: []any{externalNodeID}},
		{name: "generic failure", query: `UPDATE workflow_node_runs SET status='failed' WHERE id=$1`, args: []any{externalNodeID}},
		{name: "manual attempt", query: `UPDATE workflow_node_runs SET attempt=1 WHERE id=$1`, args: []any{externalNodeID}},
		{name: "generic started timestamp", query: `UPDATE workflow_node_runs SET started_at=clock_timestamp() WHERE id=$1`, args: []any{externalNodeID}},
		{name: "generic completed timestamp", query: `UPDATE workflow_node_runs SET completed_at=clock_timestamp() WHERE id=$1`, args: []any{externalNodeID}},
		{name: "generic failure payload", query: `UPDATE workflow_node_runs SET failure='{"code":"generic"}' WHERE id=$1`, args: []any{externalNodeID}},
		{name: "generic input manifest", query: `UPDATE workflow_node_runs SET input_manifest_id=$2 WHERE id=$1`, args: []any{externalNodeID, uuid.New()}},
		{name: "proposal path", query: `UPDATE workflow_node_runs SET output_proposal_id=$2 WHERE id=$1`, args: []any{externalNodeID, uuid.New()}},
		{name: "revision path", query: `UPDATE workflow_node_runs SET output_revision_id=$2 WHERE id=$1`, args: []any{externalNodeID, uuid.New()}},
		{name: "unissued authority", query: `UPDATE workflow_node_runs SET input_authority_id=$2 WHERE id=$1`, args: []any{externalNodeID, uuid.New()}},
		{name: "waiting without authority", query: `UPDATE workflow_node_runs SET status='waiting_qualification' WHERE id=$1`, args: []any{externalNodeID}},
		{name: "run waiting without authority", query: `UPDATE workflow_runs SET status='waiting_qualification' WHERE id=$1`, args: []any{runID}},
		{name: "run completed before handoff", query: `UPDATE workflow_runs SET status='completed' WHERE id=$1`, args: []any{runID}},
		{name: "delete dedicated gate", query: `DELETE FROM workflow_node_runs WHERE id=$1`, args: []any{externalNodeID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := database.ExecContext(ctx, test.query, test.args...)
			assertPostgresCode(t, err, "23514", test.name)
		})
	}

	malformed := map[string]any{}
	if err := json.Unmarshal(content, &malformed); err != nil {
		t.Fatal(err)
	}
	nodes := malformed["nodes"].([]any)
	for _, raw := range nodes {
		node := raw.(map[string]any)
		if node["type"] != "external_qualification_gate" {
			continue
		}
		config := node["externalQualificationGate"].(map[string]any)
		config["waiverPolicy"] = "manual"
	}
	malformedHash := workflowExecutionProfileV3RehashDocument(t, malformed)
	malformedBytes, err := json.Marshal(malformed)
	if err != nil {
		t.Fatal(err)
	}
	malformedDefinitionID := uuid.New()
	if _, err := database.ExecContext(ctx,
		`INSERT INTO workflow_definitions (id,project_id,workflow_key,title,created_by) VALUES ($1,$2,'malformed-v3','Malformed v3',$3)`,
		malformedDefinitionID, projectID, actorID,
	); err != nil {
		t.Fatal(err)
	}
	_, err = database.ExecContext(ctx, `
INSERT INTO workflow_definition_versions (
  id,definition_id,version,schema_version,content,content_hash,
  execution_profile_version,execution_profile_hash,validation_report,published,created_by,created_at
) VALUES ($1,$2,1,6,$3,$4,'workflow-engine/v3',$5,'{"valid":true}',true,$6,$7)
`, uuid.New(), malformedDefinitionID, malformedBytes, malformedHash, workflow.WorkflowExecutionProfileV3Ref().Hash, actorID, time.Now().UTC())
	assertPostgresCode(t, err, "23514", "malformed non-waivable config")

	var renamed map[string]any
	if err := json.Unmarshal(content, &renamed); err != nil {
		t.Fatal(err)
	}
	for _, raw := range renamed["nodes"].([]any) {
		node := raw.(map[string]any)
		if node["type"] == "external_qualification_gate" {
			node["id"] = "qualification-alias"
		}
	}
	for _, raw := range renamed["edges"].([]any) {
		edge := raw.(map[string]any)
		if edge["from"] == "external-qualification" {
			edge["from"] = "qualification-alias"
		}
		if edge["to"] == "external-qualification" {
			edge["to"] = "qualification-alias"
		}
	}
	renamedHash := workflowExecutionProfileV3RehashDocument(t, renamed)
	renamedBytes, err := json.Marshal(renamed)
	if err != nil {
		t.Fatal(err)
	}
	renamedDefinitionID := uuid.New()
	if _, err := database.ExecContext(ctx,
		`INSERT INTO workflow_definitions (id,project_id,workflow_key,title,created_by) VALUES ($1,$2,'renamed-v3','Renamed v3',$3)`,
		renamedDefinitionID, projectID, actorID,
	); err != nil {
		t.Fatal(err)
	}
	_, err = database.ExecContext(ctx, `
INSERT INTO workflow_definition_versions (
  id,definition_id,version,schema_version,content,content_hash,
  execution_profile_version,execution_profile_hash,validation_report,published,created_by,created_at
) VALUES ($1,$2,1,6,$3,$4,'workflow-engine/v3',$5,'{"valid":true}',true,$6,$7)
`, uuid.New(), renamedDefinitionID, renamedBytes, renamedHash, workflow.WorkflowExecutionProfileV3Ref().Hash, actorID, time.Now().UTC())
	assertPostgresCode(t, err, "23514", "renamed dedicated gate")

	if _, err := database.ExecContext(ctx, `UPDATE workflow_node_runs SET status='cancelled' WHERE id=$1`, externalNodeID); err != nil {
		t.Fatalf("cancel dedicated gate: %v", err)
	}
	_, err = database.ExecContext(ctx, `UPDATE workflow_node_runs SET status='pending' WHERE id=$1`, externalNodeID)
	assertPostgresCode(t, err, "23514", "terminal dedicated gate resurrection")

	missingGateRunID := uuid.New()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO workflow_runs (
  id,project_id,definition_version_id,execution_profile_version,execution_profile_hash,
  status,governance_mode,scope,context,event_cursor,started_by,started_at,created_at,updated_at
) VALUES ($1,$2,$3,'workflow-engine/v3',$4,'running','solo','{}','{}',0,$5,$6,$6,$6)
`, missingGateRunID, projectID, versionID, workflow.WorkflowExecutionProfileV3Ref().Hash, actorID, time.Now().UTC()); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	assertPostgresCode(t, transaction.Commit(), "23514", "v3 run without dedicated gate")

	down, err := files.ReadFile("000079_workflow_execution_profile_v3_external_qualification_gate.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.ExecContext(ctx, string(down))
	if err == nil || !strings.Contains(err.Error(), "cannot roll back workflow-engine/v3") {
		t.Fatalf("profile-v3 down fence error = %v", err)
	}
}

func workflowExecutionProfileV3MigrationDefinition(t *testing.T) (domain.WorkflowDefinition, uuid.UUID) {
	t.Helper()
	actorID, now := uuid.New(), time.Now().UTC()
	seeded, err := workflow.MinimumLoopDefinition(uuid.NewString(), actorID.String(), now)
	if err != nil {
		t.Fatal(err)
	}
	nodes := append([]domain.NodeDefinition(nil), seeded.Nodes...)
	var quality domain.NodeDefinition
	for _, node := range seeded.Nodes {
		if node.ID == "quality" {
			quality = node
			break
		}
	}
	config := domain.ExactExternalQualificationGateConfig()
	nodes = append(nodes, domain.NodeDefinition{
		ID: "external-qualification", Name: "External qualification", Type: domain.NodeExternalQualificationGate,
		InputSchema: quality.OutputSchema, OutputSchema: quality.OutputSchema, ExternalQualificationGate: &config,
	})
	edges := make([]domain.WorkflowEdge, 0, len(seeded.Edges)+1)
	for _, edge := range seeded.Edges {
		if edge.From == "quality" && edge.To == "publish" {
			edges = append(edges,
				domain.WorkflowEdge{ID: edge.ID + "-qualification", From: "quality", To: "external-qualification"},
				domain.WorkflowEdge{ID: edge.ID + "-publish", From: "external-qualification", To: "publish"},
			)
			continue
		}
		edges = append(edges, edge)
	}
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		seeded.ID, 1, "Migration workflow-engine/v3", "6", nodes, edges,
		workflow.ProjectBriefInputContract(), workflow.ApplicationOutputContract(),
		workflow.WorkflowExecutionProfileV3Ref(), actorID.String(), now,
	)
	if err != nil {
		t.Fatal(err)
	}
	return definition, actorID
}

func workflowExecutionProfileV3BlueprintSelectionDefinition(t *testing.T, actorID uuid.UUID) domain.WorkflowDefinition {
	t.Helper()
	now := time.Now().UTC()
	seeded, err := workflow.BlueprintSelectionFlowDefinition(uuid.NewString(), actorID.String(), now)
	if err != nil {
		t.Fatal(err)
	}
	nodes := append([]domain.NodeDefinition(nil), seeded.Nodes...)
	var quality domain.NodeDefinition
	for _, node := range seeded.Nodes {
		if node.ID == "quality" {
			quality = node
			break
		}
	}
	config := domain.ExactExternalQualificationGateConfig()
	nodes = append(nodes, domain.NodeDefinition{
		ID: "external-qualification", Name: "External qualification", Type: domain.NodeExternalQualificationGate,
		InputSchema: quality.OutputSchema, OutputSchema: quality.OutputSchema, ExternalQualificationGate: &config,
	})
	edges := make([]domain.WorkflowEdge, 0, len(seeded.Edges)+1)
	for _, edge := range seeded.Edges {
		if edge.From == "quality" && edge.To == "publish" {
			edges = append(edges,
				domain.WorkflowEdge{ID: edge.ID + "-qualification", From: "quality", To: "external-qualification"},
				domain.WorkflowEdge{ID: edge.ID + "-publish", From: "external-qualification", To: "publish"},
			)
			continue
		}
		edges = append(edges, edge)
	}
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		seeded.ID, seeded.Version, "Migration Blueprint-selection workflow-engine/v3", seeded.SchemaVersion,
		nodes, edges, workflow.BlueprintSelectionInputContract(), workflow.ApplicationOutputContract(),
		workflow.WorkflowExecutionProfileV3Ref(), actorID.String(), now,
	)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func workflowExecutionProfileV3MutatedDocument(
	t *testing.T,
	content []byte,
	mutate func(map[string]any),
	rehash bool,
) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	mutate(document)
	if rehash {
		workflowExecutionProfileV3RehashDocument(t, document)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func workflowExecutionProfileV3RehashDocument(t *testing.T, document map[string]any) string {
	t.Helper()
	delete(document, "hash")
	hash, err := domain.CanonicalHash(document)
	if err != nil {
		t.Fatal(err)
	}
	document["hash"] = hash
	return hash
}

func seedWorkflowExecutionProfileV3Run(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	definition domain.WorkflowDefinition,
	actorID uuid.UUID,
) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	projectID, definitionID, versionID, runID := uuid.New(), uuid.MustParse(definition.ID), uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := database.ExecContext(ctx, `INSERT INTO users (id,email,display_name,password_hash) VALUES ($1,$2,'Profile owner','unused')`, actorID, actorID.String()+"@profile-v3.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO projects (id,name,created_by,governance_mode) VALUES ($1,'Profile v3',$2,'solo')`, projectID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO workflow_definitions (id,project_id,workflow_key,title,created_by) VALUES ($1,$2,'profile-v3','Profile v3',$3)`, definitionID, projectID, actorID); err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	profile := workflow.WorkflowExecutionProfileV3Ref()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO workflow_definition_versions (
  id,definition_id,version,schema_version,content,content_hash,
  execution_profile_version,execution_profile_hash,validation_report,published,created_by,created_at
) VALUES ($1,$2,1,6,$3,$4,$5,$6,'{"valid":true}',true,$7,$8)
`, versionID, definitionID, content, definition.Hash, profile.Version, profile.Hash, actorID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO workflow_runs (
  id,project_id,definition_version_id,execution_profile_version,execution_profile_hash,
  status,governance_mode,scope,context,event_cursor,started_by,started_at,created_at,updated_at
) VALUES ($1,$2,$3,$4,$5,'running','solo','{}','{}',0,$6,$7,$7,$7)
`, runID, projectID, versionID, profile.Version, profile.Hash, actorID, now); err != nil {
		t.Fatal(err)
	}
	var externalNodeID uuid.UUID
	for _, node := range definition.Nodes {
		nodeID := uuid.New()
		if node.Type == domain.NodeExternalQualificationGate {
			externalNodeID = nodeID
		}
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO workflow_node_runs (
  id,run_id,node_key,node_type,definition_node_id,slice_kind,slice_id,
  status,attempt,available_at,created_at,updated_at
) VALUES ($1,$2,$3,$4,$3,'root',NULL,'pending',0,$5,$5,$5)
`, nodeID, runID, node.ID, string(node.Type), now); err != nil {
			t.Fatalf("insert v3 node %s: %v", node.ID, err)
		}
	}
	if externalNodeID == uuid.Nil {
		t.Fatal("v3 fixture has no external qualification gate")
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit exact workflow-engine/v3 run: %v", err)
	}
	return projectID, versionID, runID, externalNodeID
}
