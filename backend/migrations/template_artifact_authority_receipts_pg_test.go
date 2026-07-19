package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type templateAuthorityCanaryFixture struct {
	requesterID uuid.UUID
	evaluatorID uuid.UUID
	attemptID   uuid.UUID
	releaseID   uuid.UUID
	subjectHash string
	treeHash    string
	sbomDigest  string
	licenseHash string
	releaseHash string
	manifest    string
	evidence    string
	signature   string
	evaluatedAt time.Time
}

type templateAuthorityReceiptFixture struct {
	id                    uuid.UUID
	contentHash           string
	policyHash            string
	subjectHash           string
	sourceTreeHash        string
	artifactDigest        string
	sbomDigest            string
	signatureBundleDigest string
	verifiedAt            time.Time
}

func TestTemplateArtifactAuthorityReceiptsPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()

	schema := "template_artifact_authority_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	applyFreshCandidateBaselineMigrationsThrough(
		t, ctx, database, "000054_fresh_candidate_sandbox_baseline.up.sql",
	)
	legacy := seedLegacyTemplateAuthorityCanary(t, ctx, database)

	up, err := files.ReadFile("000055_template_artifact_authority_receipts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		t.Fatalf("apply Template Artifact Authority migration: %v", err)
	}

	var legacyPolicySchema, legacyPolicyState, legacyReason string
	var legacyPolicyVersion int64
	if err := database.QueryRowContext(ctx, `
SELECT schema_version, state, version, reason
FROM template_release_policies
WHERE template_release_id = $1
`, legacy.releaseID).Scan(
		&legacyPolicySchema, &legacyPolicyState, &legacyPolicyVersion, &legacyReason,
	); err != nil {
		t.Fatal(err)
	}
	if legacyPolicySchema != "template-release-policy/v1" ||
		legacyPolicyState != "revoked" || legacyPolicyVersion != 2 ||
		!strings.Contains(legacyReason, "exact Template Artifact Authority receipt required") {
		t.Fatalf(
			"legacy approved policy was not atomically revoked: schema=%q state=%q version=%d reason=%q",
			legacyPolicySchema, legacyPolicyState, legacyPolicyVersion, legacyReason,
		)
	}
	var historicalRows int
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM template_admission_attempts WHERE id = $1)
  + (SELECT count(*) FROM template_releases WHERE id = $2)
`, legacy.attemptID, legacy.releaseID).Scan(&historicalRows); err != nil {
		t.Fatal(err)
	}
	if historicalRows != 2 {
		t.Fatalf("v1 admission/release history is not queryable: count=%d", historicalRows)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE template_release_policies
SET version = version + 1, reason = 'must remain read-only', updated_at = statement_timestamp()
WHERE template_release_id = $1
`, legacy.releaseID); err == nil || !strings.Contains(err.Error(), "historical v1 template release policies are read-only") {
		t.Fatalf("historical v1 policy mutation was not rejected: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE template_admission_attempts
SET status = 'rejected', version = version + 1,
    signature = '{"format":"dsse"}'::jsonb,
    findings = '[{"message":"retire"}]'::jsonb,
    evaluated_by = $2, evaluated_at = statement_timestamp()
WHERE id = $1
`, legacy.attemptID, legacy.evaluatorID); err == nil || !strings.Contains(err.Error(), "historical v1 template admission attempts are read-only") {
		t.Fatalf("historical v1 admission mutation was not rejected: %v", err)
	}

	v1CandidateID := insertTemplateAuthorityAttempt(
		t, ctx, database, "template-admission-attempt/v1",
		legacy.requesterID, "new-v1", templateAuthorityCanaryDigest("new-v1-subject"),
		templateAuthorityCanaryDigest("new-v1-tree"), templateAuthorityCanaryDigest("new-v1-sbom"),
	)
	if _, err := database.ExecContext(ctx, `
UPDATE template_admission_attempts
SET status = 'validating', version = 2, updated_at = statement_timestamp()
WHERE id = $1
`, v1CandidateID); err == nil || !strings.Contains(err.Error(), "historical v1 template admission attempts are read-only") {
		t.Fatalf("new v1 admission could still advance toward approval: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO template_releases (
  id, schema_version, admission_attempt_id, template_id, release_version,
  source_repository, source_branch, source_commit, tree_hash, manifest,
  sbom_digest, license_expression, license_digest, evidence_refs, signature,
  subject_hash, content_hash, approved_by, approved_at
) VALUES (
  $1, 'template-release/v1', $2, 'blocked-v1', '1.0.0',
  'https://example.test/templates.git', 'main', $3, $4,
  '{"schemaVersion":"template-manifest/v1","templateId":"blocked-v1","version":"1.0.0"}'::jsonb,
  $5, 'Apache-2.0', $6, '[]'::jsonb, '{}'::jsonb,
  $7, $8, $9, statement_timestamp()
)
`, uuid.New(), legacy.attemptID, strings.Repeat("a", 40),
		templateAuthorityCanaryDigest("blocked-v1-tree"),
		templateAuthorityCanaryDigest("blocked-v1-sbom"),
		templateAuthorityCanaryDigest("blocked-v1-license"),
		templateAuthorityCanaryDigest("blocked-v1-subject"),
		templateAuthorityCanaryDigest("blocked-v1-release"), legacy.evaluatorID,
	); err == nil || !strings.Contains(err.Error(), "new v1 TemplateRelease creation is disabled") {
		t.Fatalf("new v1 TemplateRelease was not rejected: %v", err)
	}

	receipt := insertTemplateAuthorityReceiptCanary(t, ctx, database, legacy.evaluatorID)
	if receipt.artifactDigest == receipt.sourceTreeHash {
		t.Fatal("canary must keep ArtifactDigest independent from Git SourceTreeHash")
	}
	if _, err := database.ExecContext(ctx, `
UPDATE template_artifact_authority_receipts
SET content_hash = $2
WHERE id = $1
`, receipt.id, templateAuthorityCanaryDigest("tampered-receipt-content")); err == nil || !strings.Contains(err.Error(), "receipts are immutable") {
		t.Fatalf("authority receipt content tamper was not rejected: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
DELETE FROM template_artifact_authority_receipts WHERE id = $1
`, receipt.id); err == nil || !strings.Contains(err.Error(), "receipts are immutable") {
		t.Fatalf("authority receipt delete was not rejected: %v", err)
	}
	insertTamperedTemplateAuthorityReceiptCanary(t, ctx, database, legacy.evaluatorID, receipt)

	noReceiptAttempt := createValidatingTemplateAuthorityAttempt(
		t, ctx, database, legacy.requesterID, "no-receipt",
		receipt.subjectHash, receipt.sourceTreeHash, receipt.sbomDigest,
	)
	expectTemplateAuthorityApprovalRejected(
		t, ctx, database, noReceiptAttempt, legacy.evaluatorID,
		uuid.New(), "", "", receipt.subjectHash,
		receipt.signatureBundleDigest, "exact passed Artifact Authority receipt",
	)

	driftAttempt := createValidatingTemplateAuthorityAttempt(
		t, ctx, database, legacy.requesterID, "drift",
		receipt.subjectHash, receipt.sourceTreeHash, receipt.sbomDigest,
	)
	expectTemplateAuthorityApprovalRejected(
		t, ctx, database, driftAttempt, legacy.evaluatorID,
		receipt.id, templateAuthorityCanaryDigest("wrong-receipt-content"), receipt.policyHash,
		receipt.subjectHash, receipt.signatureBundleDigest, "exact passed Artifact Authority receipt",
	)
	expectTemplateAuthorityApprovalRejected(
		t, ctx, database, driftAttempt, legacy.evaluatorID,
		receipt.id, receipt.contentHash, templateAuthorityCanaryDigest("wrong-policy"),
		receipt.subjectHash, receipt.signatureBundleDigest, "exact passed Artifact Authority receipt",
	)

	subjectDriftAttempt := createValidatingTemplateAuthorityAttempt(
		t, ctx, database, legacy.requesterID, "subject-drift",
		templateAuthorityCanaryDigest("wrong-subject"), receipt.sourceTreeHash, receipt.sbomDigest,
	)
	expectTemplateAuthorityApprovalRejected(
		t, ctx, database, subjectDriftAttempt, legacy.evaluatorID,
		receipt.id, receipt.contentHash, receipt.policyHash,
		templateAuthorityCanaryDigest("wrong-subject"), receipt.signatureBundleDigest,
		"exact passed Artifact Authority receipt",
	)

	treeDriftAttempt := createValidatingTemplateAuthorityAttempt(
		t, ctx, database, legacy.requesterID, "tree-drift",
		receipt.subjectHash, templateAuthorityCanaryDigest("wrong-source-tree"), receipt.sbomDigest,
	)
	expectTemplateAuthorityApprovalRejected(
		t, ctx, database, treeDriftAttempt, legacy.evaluatorID,
		receipt.id, receipt.contentHash, receipt.policyHash,
		receipt.subjectHash, receipt.signatureBundleDigest,
		"exact passed Artifact Authority receipt",
	)

	legalAttempt := createValidatingTemplateAuthorityAttempt(
		t, ctx, database, legacy.requesterID, "legal-v2",
		receipt.subjectHash, receipt.sourceTreeHash, receipt.sbomDigest,
	)
	legalRelease := uuid.New()
	legalReleaseHash := templateAuthorityCanaryDigest("legal-v2-release")
	legalEvidence := templateAuthorityEvidence(t, receipt.subjectHash, receipt.verifiedAt.Add(time.Second))
	legalSignature := templateAuthoritySignature(
		t, receipt.subjectHash, receipt.signatureBundleDigest, receipt.verifiedAt.Add(time.Second),
	)
	legalManifest := templateAuthorityManifest(t, "legal-v2")
	evaluatedAt := receipt.verifiedAt.Add(2 * time.Second)
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
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
`, legalAttempt, legalEvidence, legalSignature, legalRelease,
		legacy.evaluatorID, evaluatedAt, receipt.id, receipt.contentHash, receipt.policyHash,
	); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("approve exact v2 Template admission: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO template_releases (
  id, schema_version, admission_attempt_id, template_id, release_version,
  source_repository, source_branch, source_commit, tree_hash, manifest,
  sbom_digest, license_expression, license_digest, evidence_refs, signature,
  subject_hash, content_hash, approved_by, approved_at,
  authority_receipt_id, authority_receipt_content_hash, authority_policy_hash
) VALUES (
  $1, 'template-release/v2', $2, 'legal-v2', '1.0.0',
  'https://example.test/templates.git', 'main', $3, $4, $5::jsonb,
  $6, 'Apache-2.0', $7, $8::jsonb, $9::jsonb,
  $10, $11, $12, $13, $14, $15, $16
)
`, legalRelease, legalAttempt, templateAuthorityCommit("legal-v2"), receipt.sourceTreeHash,
		legalManifest, receipt.sbomDigest, templateAuthorityCanaryDigest("legal-v2-license"),
		legalEvidence, legalSignature, receipt.subjectHash, legalReleaseHash,
		legacy.evaluatorID, evaluatedAt, receipt.id, receipt.contentHash, receipt.policyHash,
	); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("insert exact v2 TemplateRelease: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit exact v2 admission/release: %v", err)
	}

	if _, err := database.ExecContext(ctx, `
INSERT INTO template_release_policies (
  template_release_id, release_content_hash, schema_version,
  state, version, reason, updated_by
) VALUES ($1, $2, 'template-release-policy/v2', 'approved', 1, $3, $4)
`, legalRelease, legalReleaseHash, "missing authority receipt must fail", legacy.evaluatorID,
	); err == nil {
		t.Fatal("v2 TemplateRelease policy without an authority receipt was accepted")
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO template_release_policies (
  template_release_id, release_content_hash, schema_version,
  state, version, reason, updated_by,
  authority_receipt_id, authority_receipt_content_hash, authority_policy_hash
) VALUES (
  $1, $2, 'template-release-policy/v2',
  'approved', 1, 'exact Artifact Authority receipt', $3, $4, $5, $6
)
`, legalRelease, legalReleaseHash, legacy.evaluatorID,
		receipt.id, receipt.contentHash, receipt.policyHash,
	); err != nil {
		t.Fatalf("insert exact v2 TemplateRelease policy: %v", err)
	}
	var legalPolicyState string
	if err := database.QueryRowContext(ctx, `
SELECT state FROM template_release_policies WHERE template_release_id = $1
`, legalRelease).Scan(&legalPolicyState); err != nil {
		t.Fatal(err)
	}
	if legalPolicyState != "approved" {
		t.Fatalf("exact v2 policy state = %q, want approved", legalPolicyState)
	}

	down, err := files.ReadFile("000055_template_artifact_authority_receipts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err == nil ||
		!strings.Contains(err.Error(), "explicit handling of all v2 Template Artifact Authority lineage") {
		t.Fatalf("rollback did not fail closed with v2 authority lineage: %v", err)
	}

	transaction, err = database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM template_release_policies WHERE schema_version = 'template-release-policy/v2'`,
		`DELETE FROM template_releases WHERE schema_version = 'template-release/v2'`,
		`DELETE FROM template_admission_attempts WHERE schema_version = 'template-admission-attempt/v2'`,
		`DELETE FROM template_artifact_authority_receipts`,
	} {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			_ = transaction.Rollback()
			t.Fatal(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("rollback after explicit v2 handling: %v", err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT state, version FROM template_release_policies WHERE template_release_id = $1
`, legacy.releaseID).Scan(&legacyPolicyState, &legacyPolicyVersion); err != nil {
		t.Fatal(err)
	}
	if legacyPolicyState != "revoked" || legacyPolicyVersion != 2 {
		t.Fatalf("rollback silently restored v1 policy: state=%q version=%d", legacyPolicyState, legacyPolicyVersion)
	}
}

func seedLegacyTemplateAuthorityCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) templateAuthorityCanaryFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond).Add(-10 * time.Minute)
	fixture := templateAuthorityCanaryFixture{
		requesterID: uuid.New(), evaluatorID: uuid.New(), attemptID: uuid.New(), releaseID: uuid.New(),
		subjectHash: templateAuthorityCanaryDigest("legacy-subject"),
		treeHash:    templateAuthorityCanaryDigest("legacy-tree"),
		sbomDigest:  templateAuthorityCanaryDigest("legacy-sbom"),
		licenseHash: templateAuthorityCanaryDigest("legacy-license"),
		releaseHash: templateAuthorityCanaryDigest("legacy-release"),
		evaluatedAt: now.Add(2 * time.Minute),
	}
	for _, user := range []struct {
		id    uuid.UUID
		email string
		name  string
	}{
		{fixture.requesterID, "authority-requester-" + uuid.NewString() + "@example.com", "Authority requester"},
		{fixture.evaluatorID, "authority-evaluator-" + uuid.NewString() + "@example.com", "Authority evaluator"},
	} {
		if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash, created_at)
VALUES ($1, $2, $3, 'not-used', $4)
`, user.id, user.email, user.name, now); err != nil {
			t.Fatal(err)
		}
	}
	fixture.manifest = templateAuthorityManifest(t, "legacy-authority")
	fixture.evidence = templateAuthorityEvidence(t, fixture.subjectHash, now.Add(time.Minute))
	fixture.signature = templateAuthoritySignature(
		t, fixture.subjectHash, templateAuthorityCanaryDigest("legacy-signature-bundle"), now.Add(time.Minute),
	)
	if _, err := database.ExecContext(ctx, `
INSERT INTO template_admission_attempts (
  id, schema_version, status, version, source, manifest,
  sbom_digest, license_expression, license_digest, subject_hash,
  requested_by, created_at, updated_at
) VALUES (
  $1, 'template-admission-attempt/v1', 'candidate', 1,
  jsonb_build_object(
    'repository', 'https://example.test/templates.git', 'branch', 'main',
    'commit', $2::text, 'treeHash', $3::text
  ), $4::jsonb, $5, 'Apache-2.0', $6, $7, $8, $9, $9
)
`, fixture.attemptID, templateAuthorityCommit("legacy-authority"), fixture.treeHash,
		fixture.manifest, fixture.sbomDigest, fixture.licenseHash, fixture.subjectHash,
		fixture.requesterID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE template_admission_attempts
SET status = 'validating', version = 2, updated_at = $2
WHERE id = $1
`, fixture.attemptID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE template_admission_attempts
SET status = 'approved', version = 3,
    evidence = $2::jsonb, signature = $3::jsonb, findings = '[]'::jsonb,
    approved_release_id = $4, evaluated_by = $5, evaluated_at = $6, updated_at = $6
WHERE id = $1
`, fixture.attemptID, fixture.evidence, fixture.signature, fixture.releaseID,
		fixture.evaluatorID, fixture.evaluatedAt); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO template_releases (
  id, schema_version, admission_attempt_id, template_id, release_version,
  source_repository, source_branch, source_commit, tree_hash, manifest,
  sbom_digest, license_expression, license_digest, evidence_refs, signature,
  subject_hash, content_hash, approved_by, approved_at, created_at
) VALUES (
  $1, 'template-release/v1', $2, 'legacy-authority', '1.0.0',
  'https://example.test/templates.git', 'main', $3, $4, $5::jsonb,
  $6, 'Apache-2.0', $7, $8::jsonb, $9::jsonb,
  $10, $11, $12, $13, $13
)
`, fixture.releaseID, fixture.attemptID, templateAuthorityCommit("legacy-authority"),
		fixture.treeHash, fixture.manifest, fixture.sbomDigest, fixture.licenseHash,
		fixture.evidence, fixture.signature, fixture.subjectHash, fixture.releaseHash,
		fixture.evaluatorID, fixture.evaluatedAt); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO template_release_policies (
  template_release_id, release_content_hash, state, version, reason, updated_by,
  created_at, updated_at
) VALUES ($1, $2, 'approved', 1, 'legacy shape-only evidence', $3, $4, $4)
`, fixture.releaseID, fixture.releaseHash, fixture.evaluatorID, fixture.evaluatedAt); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func insertTemplateAuthorityReceiptCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	recordedBy uuid.UUID,
) templateAuthorityReceiptFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	receipt := templateAuthorityReceiptFixture{
		id:                    uuid.New(),
		contentHash:           templateAuthorityCanaryDigest("authority-receipt-content"),
		policyHash:            templateAuthorityCanaryDigest("authority-policy"),
		subjectHash:           templateAuthorityCanaryDigest("authority-subject"),
		sourceTreeHash:        templateAuthorityCanaryDigest("authority-source-tree"),
		artifactDigest:        templateAuthorityCanaryDigest("authority-oci-admission-bundle"),
		sbomDigest:            templateAuthorityCanaryDigest("authority-sbom"),
		signatureBundleDigest: templateAuthorityCanaryDigest("authority-signature-bundle"),
		verifiedAt:            now,
	}
	integratedAt := now.Add(-time.Second)
	checkpointSignedAt := integratedAt.Add(500 * time.Millisecond)
	transparencyBundleDigest := templateAuthorityCanaryDigest("authority-transparency-bundle")
	transparencyRootHash := templateAuthorityCanaryDigest("authority-transparency-root")
	transparencyTreeSize := int64(84)
	authorityEvidence := templateAuthorityEvidence(t, receipt.subjectHash, receipt.verifiedAt)
	authoritySignature := templateAuthoritySignature(
		t, receipt.subjectHash, receipt.signatureBundleDigest, receipt.verifiedAt,
	)
	document := templateAuthorityJSON(t, map[string]any{
		"id": receipt.id.String(), "schemaVersion": "template-artifact-authority-receipt/v1",
		"decision": "passed", "subjectHash": receipt.subjectHash,
		"sourceTreeHash": receipt.sourceTreeHash, "artifactDigest": receipt.artifactDigest,
		"sbomDigest": receipt.sbomDigest, "signatureBundleDigest": receipt.signatureBundleDigest,
		"policyHash": receipt.policyHash, "contentHash": receipt.contentHash,
		"authority":           map[string]any{"id": "worksflow-template-authority", "version": "2026.07"},
		"verifierImageDigest": templateAuthorityCanaryDigest("authority-verifier-image"),
		"trustRootDigest":     templateAuthorityCanaryDigest("authority-trust-root"),
		"transparencyLog": map[string]any{
			"id": "rekor-production", "entryUuid": "entry:0123456789abcdef",
			"logIndex": int64(42), "integratedAt": integratedAt.Format(time.RFC3339Nano),
		},
		"verificationReference": "oci://registry.example/authority/receipts@" + receipt.contentHash,
		"evidence":              json.RawMessage(authorityEvidence),
		"signature":             json.RawMessage(authoritySignature),
		"verifiedAt":            receipt.verifiedAt.Format(time.RFC3339Nano),
		"recordedBy":            recordedBy.String(), "createdAt": now.Format(time.RFC3339Nano),
		"artifactDescriptor": map[string]any{
			"reference": "registry.example/templates/legal@" + receipt.artifactDigest,
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest":    receipt.artifactDigest, "sizeBytes": int64(100),
			"config": map[string]any{
				"mediaType": "application/vnd.oci.image.config.v1+json",
				"digest":    templateAuthorityCanaryDigest("authority-config"), "sizeBytes": int64(50),
			},
			"layers": []any{
				map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": templateAuthorityCanaryDigest("authority-layer-1"), "sizeBytes": int64(75)},
				map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": templateAuthorityCanaryDigest("authority-layer-2"), "sizeBytes": int64(80)},
			},
			"totalBytes": int64(305),
		},
		"sbomDescriptor": map[string]any{
			"schemaVersion": "worksflow.template-sbom-aggregate/v1", "digest": receipt.sbomDigest,
			"serviceCount": 1,
			"services": []any{map[string]any{
				"serviceId": "web", "imageReference": "registry.example/templates/web@" + receipt.artifactDigest,
				"imageDigest":       receipt.artifactDigest,
				"referrerReference": "registry.example/templates/web-sbom@" + templateAuthorityCanaryDigest("authority-sbom-referrer"),
				"referrerDigest":    templateAuthorityCanaryDigest("authority-sbom-referrer"),
				"statementDigest":   templateAuthorityCanaryDigest("authority-sbom-statement"),
				"predicateDigest":   templateAuthorityCanaryDigest("authority-sbom-predicate"),
				"spdxVersion":       "SPDX-2.3", "documentNamespace": "https://spdx.example/authority/web",
				"evidenceHash": templateAuthorityCanaryDigest("authority-sbom-evidence"),
			}},
		},
		"proof": map[string]any{
			"payloadType": "application/vnd.in-toto+json", "predicateType": "https://slsa.dev/provenance/v1",
			"payloadDigest":            templateAuthorityCanaryDigest("authority-dsse-payload"),
			"signatureBundleDigest":    receipt.signatureBundleDigest,
			"transparencyBundleDigest": transparencyBundleDigest,
			"logId":                    "rekor-production", "entryUuid": "entry:0123456789abcdef",
			"logIndex": int64(42), "treeSize": transparencyTreeSize,
			"rootHash": transparencyRootHash, "integratedAt": integratedAt.Format(time.RFC3339Nano),
			"signerIdentities":   []string{"template-authority@example.com"},
			"checkpointSignedAt": checkpointSignedAt.Format(time.RFC3339Nano),
		},
	})
	if _, err := database.ExecContext(ctx, `
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
  'worksflow-template-authority', '2026.07', $9, $10,
  'rekor-production', 'entry:0123456789abcdef', 42,
  $11, $12, $13, $14, $15, $16, $17, $16, $18::jsonb
)
`, receipt.id, receipt.subjectHash, receipt.sourceTreeHash, receipt.artifactDigest,
		receipt.sbomDigest, receipt.signatureBundleDigest, receipt.policyHash, receipt.contentHash,
		templateAuthorityCanaryDigest("authority-verifier-image"),
		templateAuthorityCanaryDigest("authority-trust-root"), transparencyBundleDigest,
		transparencyTreeSize, transparencyRootHash, integratedAt,
		"oci://registry.example/authority/receipts@"+receipt.contentHash,
		receipt.verifiedAt, recordedBy, document); err != nil {
		t.Fatalf("insert exact Template Artifact Authority receipt: %v", err)
	}
	return receipt
}

func insertTamperedTemplateAuthorityReceiptCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	recordedBy uuid.UUID,
	source templateAuthorityReceiptFixture,
) {
	t.Helper()
	id := uuid.New()
	now := source.verifiedAt.Add(time.Second)
	integratedAt := now.Add(-time.Second)
	checkpointSignedAt := integratedAt.Add(500 * time.Millisecond)
	transparencyBundleDigest := templateAuthorityCanaryDigest("tampered-transparency-bundle")
	transparencyRootHash := templateAuthorityCanaryDigest("tampered-transparency-root")
	transparencyTreeSize := int64(85)
	contentHash := templateAuthorityCanaryDigest("tampered-document-receipt")
	authorityEvidence := templateAuthorityEvidence(t, source.subjectHash, now)
	authoritySignature := templateAuthoritySignature(
		t, source.subjectHash, source.signatureBundleDigest, now,
	)
	document := templateAuthorityJSON(t, map[string]any{
		"id": id.String(), "schemaVersion": "template-artifact-authority-receipt/v1",
		"decision": "passed", "subjectHash": source.subjectHash,
		"sourceTreeHash": templateAuthorityCanaryDigest("document-tree-does-not-match-column"),
		"artifactDigest": source.artifactDigest, "sbomDigest": source.sbomDigest,
		"signatureBundleDigest": source.signatureBundleDigest,
		"policyHash":            source.policyHash, "contentHash": contentHash,
		"authority":           map[string]any{"id": "worksflow-template-authority", "version": "2026.07"},
		"verifierImageDigest": templateAuthorityCanaryDigest("authority-verifier-image"),
		"trustRootDigest":     templateAuthorityCanaryDigest("authority-trust-root"),
		"transparencyLog": map[string]any{
			"id": "rekor-production", "entryUuid": "entry:fedcba9876543210",
			"logIndex": int64(43), "integratedAt": integratedAt.Format(time.RFC3339Nano),
		},
		"verificationReference": "oci://registry.example/authority/receipts@" + contentHash,
		"evidence":              json.RawMessage(authorityEvidence),
		"signature":             json.RawMessage(authoritySignature),
		"verifiedAt":            now.Format(time.RFC3339Nano), "recordedBy": recordedBy.String(),
		"createdAt": now.Format(time.RFC3339Nano),
		"artifactDescriptor": map[string]any{
			"reference": "registry.example/templates/tampered@" + source.artifactDigest,
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest":    source.artifactDigest, "sizeBytes": int64(100),
			"config": map[string]any{
				"mediaType": "application/vnd.oci.image.config.v1+json",
				"digest":    templateAuthorityCanaryDigest("tampered-config"), "sizeBytes": int64(50),
			},
			"layers": []any{
				map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": templateAuthorityCanaryDigest("tampered-layer"), "sizeBytes": int64(75)},
			},
			"totalBytes": int64(225),
		},
		"sbomDescriptor": map[string]any{
			"schemaVersion": "worksflow.template-sbom-aggregate/v1", "digest": source.sbomDigest,
			"serviceCount": 1,
			"services": []any{map[string]any{
				"serviceId": "web", "imageReference": "registry.example/templates/web@" + source.artifactDigest,
				"imageDigest":       source.artifactDigest,
				"referrerReference": "registry.example/templates/web-sbom@" + templateAuthorityCanaryDigest("tampered-referrer"),
				"referrerDigest":    templateAuthorityCanaryDigest("tampered-referrer"),
				"statementDigest":   templateAuthorityCanaryDigest("tampered-statement"),
				"predicateDigest":   templateAuthorityCanaryDigest("tampered-predicate"),
				"spdxVersion":       "SPDX-2.3", "documentNamespace": "https://spdx.example/tampered/web",
				"evidenceHash": templateAuthorityCanaryDigest("tampered-evidence"),
			}},
		},
		"proof": map[string]any{
			"payloadType": "application/vnd.in-toto+json", "predicateType": "https://slsa.dev/provenance/v1",
			"payloadDigest":            templateAuthorityCanaryDigest("tampered-payload"),
			"signatureBundleDigest":    source.signatureBundleDigest,
			"transparencyBundleDigest": transparencyBundleDigest,
			"logId":                    "rekor-production", "entryUuid": "entry:fedcba9876543210",
			"logIndex": int64(43), "treeSize": transparencyTreeSize,
			"rootHash": transparencyRootHash, "integratedAt": integratedAt.Format(time.RFC3339Nano),
			"signerIdentities":   []string{"template-authority@example.com"},
			"checkpointSignedAt": checkpointSignedAt.Format(time.RFC3339Nano),
		},
	})
	if _, err := database.ExecContext(ctx, `
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
  'worksflow-template-authority', '2026.07', $9, $10,
  'rekor-production', 'entry:fedcba9876543210', 43,
  $11, $12, $13, $14, $15, $16, $17, $16, $18::jsonb
)
`, id, source.subjectHash, source.sourceTreeHash, source.artifactDigest,
		source.sbomDigest, source.signatureBundleDigest, source.policyHash, contentHash,
		templateAuthorityCanaryDigest("authority-verifier-image"),
		templateAuthorityCanaryDigest("authority-trust-root"), transparencyBundleDigest,
		transparencyTreeSize, transparencyRootHash, integratedAt,
		"oci://registry.example/authority/receipts@"+contentHash,
		now, recordedBy, document); err == nil || !strings.Contains(err.Error(), "template_artifact_authority_receipt_document_exact") {
		t.Fatalf("receipt document/index-column drift was not rejected: %v", err)
	}
}

func createValidatingTemplateAuthorityAttempt(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	requesterID uuid.UUID,
	templateID, subjectHash, treeHash, sbomDigest string,
) uuid.UUID {
	t.Helper()
	id := insertTemplateAuthorityAttempt(
		t, ctx, database, "template-admission-attempt/v2",
		requesterID, templateID, subjectHash, treeHash, sbomDigest,
	)
	if _, err := database.ExecContext(ctx, `
UPDATE template_admission_attempts
SET status = 'validating', version = 2, updated_at = statement_timestamp()
WHERE id = $1
`, id); err != nil {
		t.Fatalf("advance v2 admission to validating: %v", err)
	}
	return id
}

func insertTemplateAuthorityAttempt(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	schemaVersion string,
	requesterID uuid.UUID,
	templateID, subjectHash, treeHash, sbomDigest string,
) uuid.UUID {
	t.Helper()
	id := uuid.New()
	manifest := templateAuthorityManifest(t, templateID)
	if _, err := database.ExecContext(ctx, `
INSERT INTO template_admission_attempts (
  id, schema_version, status, version, source, manifest,
  sbom_digest, license_expression, license_digest, subject_hash, requested_by
) VALUES (
  $1, $2, 'candidate', 1,
  jsonb_build_object(
    'repository', 'https://example.test/templates.git', 'branch', 'main',
    'commit', $3::text, 'treeHash', $4::text
  ), $5::jsonb, $6, 'Apache-2.0', $7, $8, $9
)
`, id, schemaVersion, templateAuthorityCommit(templateID), treeHash, manifest,
		sbomDigest, templateAuthorityCanaryDigest(templateID+"-license"), subjectHash, requesterID,
	); err != nil {
		t.Fatalf("insert %s admission: %v", schemaVersion, err)
	}
	return id
}

func expectTemplateAuthorityApprovalRejected(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	attemptID, evaluatorID, receiptID uuid.UUID,
	receiptContentHash, policyHash, subjectHash, signatureBundleDigest, errorFragment string,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	evaluatedAt := time.Now().UTC().Truncate(time.Microsecond)
	evidence := templateAuthorityEvidence(t, subjectHash, evaluatedAt)
	signature := templateAuthoritySignature(t, subjectHash, signatureBundleDigest, evaluatedAt)
	var receiptIDValue any = receiptID
	var receiptHashValue any = receiptContentHash
	var policyHashValue any = policyHash
	if receiptContentHash == "" {
		receiptIDValue = nil
		receiptHashValue = nil
		policyHashValue = nil
	}
	_, err = transaction.ExecContext(ctx, `
UPDATE template_admission_attempts
SET status = 'approved', version = 3,
    evidence = $2::jsonb, signature = $3::jsonb, findings = '[]'::jsonb,
    approved_release_id = $4, evaluated_by = $5, evaluated_at = $6, updated_at = $6,
    authority_receipt_id = $7, authority_receipt_content_hash = $8,
    authority_policy_hash = $9
WHERE id = $1
`, attemptID, evidence, signature, uuid.New(), evaluatorID, evaluatedAt,
		receiptIDValue, receiptHashValue, policyHashValue)
	if err == nil || !strings.Contains(err.Error(), errorFragment) {
		t.Fatalf("authority drift/no-receipt approval was not rejected with %q: %v", errorFragment, err)
	}
}

func templateAuthorityEvidence(t *testing.T, subjectHash string, observedAt time.Time) string {
	t.Helper()
	gates := []string{
		"source_identity", "manifest_schema", "license_spdx", "dependency_lock",
		"registry_policy", "install", "lint", "typecheck", "unit_test", "build",
		"start_health", "contract_smoke", "container_build", "secret_scan", "sbom",
		"vulnerability", "signature_attestation",
	}
	evidence := make([]map[string]any, 0, len(gates))
	for index, gate := range gates {
		evidence = append(evidence, map[string]any{
			"gate": gate, "outcome": "passed", "subjectHash": subjectHash,
			"digest":    templateAuthorityCanaryDigest(fmt.Sprintf("%s-%d", gate, index)),
			"reference": "oci://registry.example/evidence/" + gate,
			"producer":  "worksflow-template-authority", "invocationId": uuid.NewString(),
			"observedAt": observedAt.Format(time.RFC3339Nano),
		})
	}
	return templateAuthorityJSON(t, evidence)
}

func templateAuthoritySignature(
	t *testing.T,
	subjectHash, bundleDigest string,
	signedAt time.Time,
) string {
	t.Helper()
	return templateAuthorityJSON(t, map[string]any{
		"format": "dsse", "subjectHash": subjectHash, "bundleDigest": bundleDigest,
		"signer":             "template-authority@example.com",
		"transparencyLogRef": "rekor://entry/0123456789abcdef",
		"signedAt":           signedAt.Format(time.RFC3339Nano),
	})
}

func templateAuthorityManifest(t *testing.T, templateID string) string {
	t.Helper()
	return templateAuthorityJSON(t, map[string]any{
		"schemaVersion": "template-manifest/v1", "templateId": templateID,
		"version": "1.0.0",
	})
}

func templateAuthorityJSON(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func templateAuthorityCanaryDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func templateAuthorityCommit(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:20])
}
