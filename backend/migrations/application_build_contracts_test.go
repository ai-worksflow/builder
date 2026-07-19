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
)

func TestApplicationBuildContractMigrationDeclaresExactImmutableProjections(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000022_application_build_contracts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000022_application_build_contracts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TABLE application_build_contracts",
		"CREATE TABLE application_build_contract_sources",
		"CREATE TABLE application_build_contract_template_releases",
		"CREATE TABLE application_build_contract_obligations",
		"application_build_contract_exact_identity_unique",
		"application_build_contract_compile_unique",
		"application_build_contract_template_fk",
		"application_build_contract_release_fk",
		"application_build_contract_state_shape",
		"source_count integer NOT NULL",
		"template_release_count integer NOT NULL",
		"creation_transaction_id bigint NOT NULL",
		"creation_transaction_id <> txid_current()",
		"manifest.status = 'frozen'",
		"revision.workflow_status = 'approved'",
		"policy.state = 'approved'",
		"application_build_contract_projection_parent_guard",
		"application_build_contract_projection_source_guard",
		"application_build_contract_projection_template_guard",
		"application_build_contract_source_count",
		"application_build_contract_template_release_count",
		"application_build_contract_immutable",
		"Application Build Contract content and projections are immutable",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Application Build Contract migration is missing %q", expected)
		}
	}
	downText := string(down)
	for _, expected := range []string{
		"DROP TABLE IF EXISTS application_build_contract_obligations",
		"DROP TABLE IF EXISTS application_build_contract_template_releases",
		"DROP TABLE IF EXISTS application_build_contract_sources",
		"DROP TABLE IF EXISTS application_build_contracts",
		"DROP FUNCTION IF EXISTS increment_application_build_contract_projection_count",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("Application Build Contract rollback is missing %q", expected)
		}
	}
}

func TestApplicationBuildContractMigrationPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "application_build_contract_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	if _, err := base.ExecContext(setupCtx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		setupCancel()
		t.Fatal(err)
	}
	setupCancel()
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	applyPostgresMigrationsForCanary(t, database)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	for _, table := range []string{
		"application_build_contracts", "application_build_contract_sources",
		"application_build_contract_template_releases", "application_build_contract_obligations",
	} {
		var actual string
		if err := database.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, table).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != table {
			t.Fatalf("expected %s table, got %q", table, actual)
		}
	}
	var triggerCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_trigger AS trigger
JOIN pg_class AS relation ON relation.oid = trigger.tgrelid
JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE NOT trigger.tgisinternal
  AND namespace.nspname = current_schema()
  AND tgname IN (
    'application_build_contract_parent_guard',
    'application_build_contract_source_guard',
    'application_build_contract_template_release_guard',
	    'application_build_contract_obligation_source_guard',
	    'application_build_contract_projection_parent_guard',
	    'application_build_contract_projection_source_guard',
	    'application_build_contract_projection_template_guard',
	    'application_build_contract_projection_obligation_guard',
	    'application_build_contract_source_count',
	    'application_build_contract_template_release_count',
    'application_build_contract_immutable',
    'application_build_contract_source_immutable',
    'application_build_contract_template_immutable',
    'application_build_contract_obligation_immutable'
  )
`).Scan(&triggerCount); err != nil {
		t.Fatal(err)
	}
	if triggerCount != 14 {
		t.Fatalf("expected fourteen BuildContract invariant triggers, got %d", triggerCount)
	}

	assertApplicationBuildContractProjectionGuards(t, ctx, database)
}

type applicationBuildContractCanaryTemplate struct {
	id          uuid.UUID
	contentHash string
	subjectHash string
	role        string
	mountPath   string
}

func assertApplicationBuildContractProjectionGuards(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	requesterID, reviewerID := uuid.New(), uuid.New()
	projectID, manifestID := uuid.New(), uuid.New()
	artifactID, revisionID := uuid.New(), uuid.New()
	lateArtifactID, lateRevisionID := uuid.New(), uuid.New()
	fullStackID, contractID := uuid.New(), uuid.New()
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'BuildContract requester', 'not-used'),
       ($3, $4, 'BuildContract reviewer', 'not-used')
`, requesterID, "build-contract-requester-"+uuid.NewString()+"@example.com", reviewerID, "build-contract-reviewer-"+uuid.NewString()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, 'BuildContract projection canary', $2)
`, projectID, requesterID); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO artifacts (id, project_id, kind, artifact_key, title, created_by)
VALUES ($1, $2, 'api_contract', 'CANARY-SOURCE', 'Canary source', $3),
       ($4, $2, 'api_contract', 'CANARY-LATE-SOURCE', 'Canary late source', $3)
`, artifactID, projectID, requesterID, lateArtifactID); err != nil {
		t.Fatal(err)
	}
	sourceHash := applicationBuildContractCanaryDigest("source-revision")
	lateSourceHash := applicationBuildContractCanaryDigest("late-source-revision")
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO artifact_revisions (
  id, artifact_id, revision_number, schema_version, content_ref, content_hash,
  workflow_status, change_source, change_summary, created_by, approved_at
) VALUES
  ($1, $2, 1, 1, $3, $4, 'approved', 'system', 'canary', $5, $6),
  ($7, $8, 1, 1, $9, $10, 'approved', 'system', 'canary', $5, $6)
`, revisionID, artifactID, "canary-source-"+revisionID.String(), sourceHash, requesterID, createdAt,
		lateRevisionID, lateArtifactID, "canary-source-"+lateRevisionID.String(), lateSourceHash); err != nil {
		t.Fatal(err)
	}
	manifestHash := applicationBuildContractCanaryDigest("build-manifest")
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO application_build_manifests (
  id, project_id, root_manifest_id, schema_version, content_ref, content_hash,
  manifest_hash, status, created_by, created_at
) VALUES ($1, $2, $1, 1, $3, $4, $5, 'frozen', $6, $7)
`, manifestID, projectID, "canary-manifest-"+manifestID.String(),
		applicationBuildContractCanaryDigest("manifest-content"), manifestHash, requesterID, createdAt); err != nil {
		t.Fatal(err)
	}

	web := insertApplicationBuildContractCanaryRelease(t, ctx, transaction, requesterID, reviewerID, "web", createdAt)
	api := insertApplicationBuildContractCanaryRelease(t, ctx, transaction, requesterID, reviewerID, "api", createdAt)
	extra := insertApplicationBuildContractCanaryRelease(t, ctx, transaction, requesterID, reviewerID, "worker", createdAt)
	fullStackHash := applicationBuildContractCanaryDigest("full-stack")
	fullStackDocument := applicationBuildContractCanaryJSON(t, map[string]any{
		"id": fullStackID.String(), "schemaVersion": "full-stack-template/v1",
		"templateId": "canary-full-stack", "version": "1.0.0", "contentHash": fullStackHash,
		"components": []any{
			map[string]any{"role": api.role, "mountPath": api.mountPath, "release": map[string]any{"id": api.id.String(), "contentHash": api.contentHash, "subjectHash": api.subjectHash}},
			map[string]any{"role": web.role, "mountPath": web.mountPath, "release": map[string]any{"id": web.id.String(), "contentHash": web.contentHash, "subjectHash": web.subjectHash}},
		},
		"layout":    map[string]any{"contractTruthSource": "openapi"},
		"createdBy": reviewerID.String(), "createdAt": createdAt.Format(time.RFC3339Nano),
	})
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO full_stack_template_releases (
  id, schema_version, template_id, release_version, document, content_hash, created_by, created_at
) VALUES ($1, 'full-stack-template/v1', 'canary-full-stack', '1.0.0', $2::jsonb, $3, $4, $5)
`, fullStackID, fullStackDocument, fullStackHash, reviewerID, createdAt); err != nil {
		t.Fatal(err)
	}
	for _, component := range []applicationBuildContractCanaryTemplate{api, web} {
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO full_stack_template_components (
  full_stack_template_id, full_stack_content_hash, role, mount_path,
  template_release_id, template_release_content_hash
) VALUES ($1, $2, $3, $4, $5, $6)
`, fullStackID, fullStackHash, component.role, component.mountPath, component.id, component.contentHash); err != nil {
			t.Fatal(err)
		}
	}

	insertApplicationBuildContractCanaryParent(
		t, ctx, transaction, contractID, projectID, manifestID, manifestHash,
		fullStackID, fullStackHash, requesterID, "valid",
	)
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO application_build_contract_sources (
  contract_id, ordinal, source_kind, purpose, required, artifact_id, revision_id, content_hash
) VALUES ($1, 0, 'api_contract', 'contract', true, $2, $3, $4)
`, contractID, artifactID, revisionID, sourceHash); err != nil {
		t.Fatal(err)
	}
	for ordinal, component := range []applicationBuildContractCanaryTemplate{api, web} {
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO application_build_contract_template_releases (
  contract_id, ordinal, role, template_release_id, template_release_content_hash
) VALUES ($1, $2, $3, $4, $5)
`, contractID, ordinal, component.role, component.id, component.contentHash); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO application_build_contract_obligations (
  contract_id, obligation_id, level, kind, source_artifact_id, source_revision_id,
  source_content_hash, source_anchor_id, oracle_ids, depends_on, waivable, status
) VALUES ($1, 'OBL-CANARY', 'must', 'acceptance', $2, $3, $4, 'AC-CANARY',
          '["oracle-canary"]'::jsonb, '[]'::jsonb, false, 'ready')
`, contractID, artifactID, revisionID, sourceHash); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("valid BuildContract projections did not commit: %v", err)
	}

	var sourceCount, templateCount int
	var sealed bool
	if err := database.QueryRowContext(ctx, `
SELECT source_count, template_release_count, creation_transaction_id <> txid_current()
FROM application_build_contracts WHERE id = $1
`, contractID).Scan(&sourceCount, &templateCount, &sealed); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 || templateCount != 2 || !sealed {
		t.Fatalf("sealed projection = sources:%d templates:%d sealed:%v", sourceCount, templateCount, sealed)
	}

	if _, err := database.ExecContext(ctx, `
INSERT INTO application_build_contract_sources (
  contract_id, ordinal, source_kind, purpose, required, artifact_id, revision_id, content_hash
) VALUES ($1, 1, 'api_contract', 'late-append', true, $2, $3, $4)
`, contractID, lateArtifactID, lateRevisionID, lateSourceHash); err == nil || !strings.Contains(strings.ToLower(err.Error()), "sealed") {
		t.Fatalf("late source append was not rejected by the seal: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO application_build_contract_template_releases (
  contract_id, ordinal, role, template_release_id, template_release_content_hash
) VALUES ($1, 2, 'worker', $2, $3)
`, contractID, extra.id, extra.contentHash); err == nil || !strings.Contains(strings.ToLower(err.Error()), "sealed") {
		t.Fatalf("late Template Release append was not rejected by the seal: %v", err)
	}

	wrongComponentTransaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	wrongContractID := uuid.New()
	insertApplicationBuildContractCanaryParent(
		t, ctx, wrongComponentTransaction, wrongContractID, projectID, manifestID, manifestHash,
		fullStackID, fullStackHash, requesterID, "wrong-component",
	)
	_, wrongComponentErr := wrongComponentTransaction.ExecContext(ctx, `
INSERT INTO application_build_contract_template_releases (
  contract_id, ordinal, role, template_release_id, template_release_content_hash
) VALUES ($1, 0, 'web', $2, $3)
`, wrongContractID, extra.id, extra.contentHash)
	_ = wrongComponentTransaction.Rollback()
	if wrongComponentErr == nil || !strings.Contains(strings.ToLower(wrongComponentErr.Error()), "exact component") {
		t.Fatalf("unrelated approved Template Release escaped exact component guard: %v", wrongComponentErr)
	}
}

func insertApplicationBuildContractCanaryParent(
	t *testing.T,
	ctx context.Context,
	transaction *sql.Tx,
	contractID, projectID, manifestID uuid.UUID,
	manifestHash string,
	fullStackID uuid.UUID,
	fullStackHash string,
	createdBy uuid.UUID,
	identity string,
) {
	t.Helper()
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO application_build_contracts (
  id, project_id, build_manifest_id, build_manifest_hash,
  full_stack_template_id, full_stack_template_hash,
  schema_version, compiler_version, compiler_hash,
  content_store, content_ref, content_hash, contract_hash, status,
  must_count, must_ready_count, obligation_count,
  source_count, template_release_count,
  blocking_count, conflict_count, version, created_by
) VALUES (
  $1, $2, $3, $4, $5, $6,
  'application-build-contract/v2', 'canary-compiler/v1', $7,
  'mongo', $8, $9, $10, 'ready',
  1, 1, 1, 999, 999, 0, 0, 1, $11
)
`, contractID, projectID, manifestID, manifestHash, fullStackID, fullStackHash,
		applicationBuildContractCanaryDigest("compiler-"+identity), "canary-contract-"+contractID.String(),
		applicationBuildContractCanaryDigest("content-"+identity), applicationBuildContractCanaryDigest("contract-"+identity), createdBy); err != nil {
		t.Fatal(err)
	}
}

func insertApplicationBuildContractCanaryRelease(
	t *testing.T,
	ctx context.Context,
	transaction *sql.Tx,
	requestedBy, approvedBy uuid.UUID,
	role string,
	approvedAt time.Time,
) applicationBuildContractCanaryTemplate {
	t.Helper()
	attemptID, releaseID := uuid.New(), uuid.New()
	subjectHash := applicationBuildContractCanaryDigest(role + "-subject")
	contentHash := applicationBuildContractCanaryDigest(role + "-release")
	treeHash := applicationBuildContractCanaryDigest(role + "-tree")
	sbomHash := applicationBuildContractCanaryDigest(role + "-sbom")
	licenseHash := applicationBuildContractCanaryDigest(role + "-license")
	repository := "https://example.test/templates.git"
	branch := "canary-" + role
	commit := strings.TrimPrefix(applicationBuildContractCanaryDigest(role+"-commit"), "sha256:")[:40]
	templateID := "canary-" + role
	manifest := applicationBuildContractCanaryJSON(t, map[string]any{
		"schemaVersion": "template-manifest/v1", "templateId": templateID, "version": "1.0.0",
	})
	source := applicationBuildContractCanaryJSON(t, map[string]any{
		"repository": repository, "branch": branch, "commit": commit, "treeHash": treeHash,
	})
	gates := []string{
		"source_identity", "manifest_schema", "license_spdx", "dependency_lock",
		"registry_policy", "install", "lint", "typecheck", "unit_test", "build",
		"start_health", "contract_smoke", "container_build", "secret_scan", "sbom",
		"vulnerability", "signature_attestation",
	}
	evidenceItems := make([]any, 0, len(gates))
	for _, gate := range gates {
		evidenceItems = append(evidenceItems, map[string]any{
			"gate": gate, "outcome": "passed", "subjectHash": subjectHash,
			"digest":    applicationBuildContractCanaryDigest(role + "-evidence-" + gate),
			"reference": "canary://" + role + "/" + gate, "producer": "migration-canary",
			"invocationId": role + "-" + gate, "observedAt": approvedAt.Format(time.RFC3339Nano),
		})
	}
	evidence := applicationBuildContractCanaryJSON(t, evidenceItems)
	signatureBundleHash := applicationBuildContractCanaryDigest(role + "-signature")
	signature := applicationBuildContractCanaryJSON(t, map[string]any{
		"format": "dsse", "subjectHash": subjectHash,
		"bundleDigest": signatureBundleHash,
		"signer":       "migration-canary", "transparencyLogRef": "canary://log/" + role,
		"signedAt": approvedAt.Format(time.RFC3339Nano),
	})
	var authorityTable sql.NullString
	if err := transaction.QueryRowContext(ctx, `
SELECT to_regclass('template_artifact_authority_receipts')::text
`).Scan(&authorityTable); err != nil {
		t.Fatal(err)
	}
	authorityEnabled := authorityTable.Valid
	authorityReceiptID := uuid.Nil
	authorityReceiptHash := ""
	authorityPolicyHash := ""
	if authorityEnabled {
		authorityReceiptID = uuid.New()
		authorityReceiptHash = applicationBuildContractCanaryDigest(role + "-authority-receipt")
		authorityPolicyHash = applicationBuildContractCanaryDigest(role + "-authority-policy")
		artifactDigest := applicationBuildContractCanaryDigest(role + "-admission-bundle")
		verifierImageDigest := applicationBuildContractCanaryDigest(role + "-authority-verifier")
		trustRootDigest := applicationBuildContractCanaryDigest(role + "-authority-trust-root")
		verifiedAt := approvedAt
		integratedAt := approvedAt.Add(-time.Second)
		checkpointSignedAt := integratedAt.Add(500 * time.Millisecond)
		transparencyBundleDigest := applicationBuildContractCanaryDigest(role + "-transparency-bundle")
		transparencyTreeSize := int64(11)
		transparencyRootHash := applicationBuildContractCanaryDigest(role + "-transparency-root")
		verificationReference := "canary://template-authority/receipt/" + authorityReceiptID.String()
		authorityDocument := applicationBuildContractCanaryJSON(t, map[string]any{
			"id": authorityReceiptID.String(), "schemaVersion": "template-artifact-authority-receipt/v1",
			"decision": "passed", "subjectHash": subjectHash, "sourceTreeHash": treeHash,
			"artifactDigest": artifactDigest, "sbomDigest": sbomHash,
			"signatureBundleDigest": signatureBundleHash,
			"policyHash":            authorityPolicyHash,
			"contentHash":           authorityReceiptHash,
			"authority": map[string]any{
				"id": "migration-canary-authority", "version": "v1",
			},
			"verifierImageDigest": verifierImageDigest,
			"trustRootDigest":     trustRootDigest,
			"transparencyLog": map[string]any{
				"id": "migration-canary-log", "entryUuid": "entry:" + strings.ReplaceAll(authorityReceiptID.String(), "-", ""),
				"logIndex": int64(1), "integratedAt": integratedAt.Format(time.RFC3339Nano),
			},
			"verificationReference": verificationReference,
			"evidence":              json.RawMessage(evidence),
			"signature":             json.RawMessage(signature),
			"verifiedAt":            verifiedAt.Format(time.RFC3339Nano),
			"recordedBy":            approvedBy.String(),
			"createdAt":             verifiedAt.Format(time.RFC3339Nano),
			"artifactDescriptor": map[string]any{
				"reference": "registry.example/migration/" + role + "@" + artifactDigest,
				"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": artifactDigest,
				"sizeBytes": int64(100),
				"config": map[string]any{
					"mediaType": "application/vnd.oci.image.config.v1+json",
					"digest":    applicationBuildContractCanaryDigest(role + "-config"), "sizeBytes": int64(50),
				},
				"layers": []any{
					map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": applicationBuildContractCanaryDigest(role + "-layer"), "sizeBytes": int64(75)},
				},
				"totalBytes": int64(225),
			},
			"sbomDescriptor": map[string]any{
				"schemaVersion": "worksflow.template-sbom-aggregate/v1", "digest": sbomHash,
				"serviceCount": 1,
				"services": []any{map[string]any{
					"serviceId": role, "imageReference": "registry.example/migration/" + role + "@" + artifactDigest,
					"imageDigest":       artifactDigest,
					"referrerReference": "registry.example/migration/" + role + "-sbom@" + applicationBuildContractCanaryDigest(role+"-sbom-referrer"),
					"referrerDigest":    applicationBuildContractCanaryDigest(role + "-sbom-referrer"),
					"statementDigest":   applicationBuildContractCanaryDigest(role + "-sbom-statement"),
					"predicateDigest":   applicationBuildContractCanaryDigest(role + "-sbom-predicate"),
					"spdxVersion":       "SPDX-2.3", "documentNamespace": "https://spdx.example/migration/" + role,
					"evidenceHash": applicationBuildContractCanaryDigest(role + "-sbom-evidence"),
				}},
			},
			"proof": map[string]any{
				"payloadType": "application/vnd.in-toto+json", "predicateType": "https://slsa.dev/provenance/v1",
				"payloadDigest":            applicationBuildContractCanaryDigest(role + "-dsse-payload"),
				"signatureBundleDigest":    signatureBundleHash,
				"transparencyBundleDigest": transparencyBundleDigest,
				"logId":                    "migration-canary-log", "entryUuid": "entry:" + strings.ReplaceAll(authorityReceiptID.String(), "-", ""),
				"logIndex": int64(1), "treeSize": transparencyTreeSize,
				"rootHash": transparencyRootHash, "integratedAt": integratedAt.Format(time.RFC3339Nano),
				"signerIdentities":   []string{"migration-canary"},
				"checkpointSignedAt": checkpointSignedAt.Format(time.RFC3339Nano),
			},
		})
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO template_artifact_authority_receipts (
  id, schema_version, decision, subject_hash, source_tree_hash,
  artifact_digest, sbom_digest, signature_bundle_digest, policy_hash, content_hash,
  authority_id, authority_version, verifier_image_digest, trust_root_digest,
  transparency_log_id, transparency_entry_uuid, transparency_log_index,
  transparency_bundle_digest, transparency_tree_size, transparency_root_hash,
  integrated_at, verification_reference, verified_at, recorded_by, created_at, document
) VALUES (
  $1, 'template-artifact-authority-receipt/v1', 'passed', $2, $3,
  $4, $5, $6, $7, $8,
  'migration-canary-authority', 'v1', $9, $10,
  'migration-canary-log', $11, 1,
  $12, $13, $14, $15, $16, $17, $18, $17, $19::jsonb
)
`, authorityReceiptID, subjectHash, treeHash, artifactDigest, sbomHash,
			signatureBundleHash, authorityPolicyHash, authorityReceiptHash,
			verifierImageDigest, trustRootDigest,
			"entry:"+strings.ReplaceAll(authorityReceiptID.String(), "-", ""),
			transparencyBundleDigest, transparencyTreeSize, transparencyRootHash,
			integratedAt, verificationReference, verifiedAt, approvedBy, authorityDocument); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO template_admission_attempts (
  id, schema_version, status, version, source, manifest,
  sbom_digest, license_expression, license_digest, subject_hash,
  requested_by, created_at, updated_at
) VALUES (
  $1, 'template-admission-attempt/v2', 'candidate', 1, $2::jsonb, $3::jsonb,
  $4, 'Apache-2.0', $5, $6, $7, $8, $8
)
`, attemptID, source, manifest, sbomHash, licenseHash, subjectHash,
			requestedBy, approvedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `
UPDATE template_admission_attempts
SET status = 'validating', version = 2, updated_at = $2
WHERE id = $1
`, attemptID, approvedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `
UPDATE template_admission_attempts
SET status = 'approved', version = 3,
    evidence = $2::jsonb, signature = $3::jsonb, findings = '[]'::jsonb,
    approved_release_id = $4, evaluated_by = $5, evaluated_at = $6, updated_at = $6,
    authority_receipt_id = $7, authority_receipt_content_hash = $8,
    authority_policy_hash = $9
WHERE id = $1
`, attemptID, evidence, signature, releaseID, approvedBy, approvedAt,
			authorityReceiptID, authorityReceiptHash, authorityPolicyHash); err != nil {
			t.Fatal(err)
		}
	} else if _, err := transaction.ExecContext(ctx, `
INSERT INTO template_admission_attempts (
  id, schema_version, status, version, source, manifest,
  sbom_digest, license_expression, license_digest, subject_hash,
  evidence, signature, findings, approved_release_id,
  requested_by, evaluated_by, created_at, updated_at, evaluated_at
) VALUES (
  $1, 'template-admission-attempt/v1', 'approved', 1, $2::jsonb, $3::jsonb,
  $4, 'Apache-2.0', $5, $6,
  $7::jsonb, $8::jsonb, '[]'::jsonb, $9,
  $10, $11, $12, $12, $12
)
`, attemptID, source, manifest, sbomHash, licenseHash, subjectHash, evidence, signature,
		releaseID, requestedBy, approvedBy, approvedAt); err != nil {
		t.Fatal(err)
	}
	if authorityEnabled {
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO template_releases (
  id, schema_version, admission_attempt_id, template_id, release_version,
  source_repository, source_branch, source_commit, tree_hash, manifest,
  sbom_digest, license_expression, license_digest, evidence_refs, signature,
  subject_hash, content_hash, approved_by, approved_at,
  authority_receipt_id, authority_receipt_content_hash, authority_policy_hash
) VALUES (
  $1, 'template-release/v2', $2, $3, '1.0.0',
  $4, $5, $6, $7, $8::jsonb,
  $9, 'Apache-2.0', $10, $11::jsonb, $12::jsonb,
  $13, $14, $15, $16, $17, $18, $19
)
`, releaseID, attemptID, templateID, repository, branch, commit, treeHash, manifest,
			sbomHash, licenseHash, evidence, signature, subjectHash, contentHash, approvedBy, approvedAt,
			authorityReceiptID, authorityReceiptHash, authorityPolicyHash); err != nil {
			t.Fatal(err)
		}
	} else if _, err := transaction.ExecContext(ctx, `
INSERT INTO template_releases (
  id, schema_version, admission_attempt_id, template_id, release_version,
  source_repository, source_branch, source_commit, tree_hash, manifest,
  sbom_digest, license_expression, license_digest, evidence_refs, signature,
  subject_hash, content_hash, approved_by, approved_at
) VALUES (
  $1, 'template-release/v1', $2, $3, '1.0.0',
  $4, $5, $6, $7, $8::jsonb,
  $9, 'Apache-2.0', $10, $11::jsonb, $12::jsonb,
  $13, $14, $15, $16
)
`, releaseID, attemptID, templateID, repository, branch, commit, treeHash, manifest,
		sbomHash, licenseHash, evidence, signature, subjectHash, contentHash, approvedBy, approvedAt); err != nil {
		t.Fatal(err)
	}
	if authorityEnabled {
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO template_release_policies (
  template_release_id, release_content_hash, schema_version,
  state, version, reason, updated_by, created_at, updated_at,
  authority_receipt_id, authority_receipt_content_hash, authority_policy_hash
) VALUES (
  $1, $2, 'template-release-policy/v2',
  'approved', 1, 'migration canary authority receipt', $3, $4, $4, $5, $6, $7
)
`, releaseID, contentHash, approvedBy, approvedAt,
			authorityReceiptID, authorityReceiptHash, authorityPolicyHash); err != nil {
			t.Fatal(err)
		}
	} else if _, err := transaction.ExecContext(ctx, `
INSERT INTO template_release_policies (
  template_release_id, release_content_hash, state, version, reason, updated_by, created_at, updated_at
) VALUES ($1, $2, 'approved', 1, 'migration canary', $3, $4, $4)
`, releaseID, contentHash, approvedBy, approvedAt); err != nil {
		t.Fatal(err)
	}
	return applicationBuildContractCanaryTemplate{
		id: releaseID, contentHash: contentHash, subjectHash: subjectHash,
		role: role, mountPath: "apps/" + role,
	}
}

func applicationBuildContractCanaryDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func applicationBuildContractCanaryJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
