package migrations

import (
	"strings"
	"testing"
)

func TestTemplateArtifactAuthorityReceiptMigrationDeclaresExactV2Lineage(t *testing.T) {
	t.Parallel()

	up, err := files.ReadFile("000055_template_artifact_authority_receipts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000055_template_artifact_authority_receipts.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	upText := string(up)
	for _, required := range []string{
		"CREATE TABLE template_artifact_authority_receipts",
		"template-artifact-authority-receipt/v1",
		"template-admission-attempt/v2",
		"template-release/v2",
		"template-release-policy/v2",
		"decision text NOT NULL CHECK (decision = 'passed')",
		"document jsonb NOT NULL",
		"template_artifact_authority_receipt_document_exact",
		"template_artifact_authority_receipt_time_order",
		"template_artifact_authority_receipt_exact",
		"validate_template_artifact_authority_receipt_insert",
		"document->'evidence'",
		"document->'signature'",
		"document->'artifactDescriptor'",
		"document->'sbomDescriptor'",
		"document->'proof'",
		"worksflow.template-sbom-aggregate/v1",
		"strictly ordered by serviceId",
		"artifact totalBytes is not exact",
		"source_tree_hash",
		"artifact_digest",
		"signature_bundle_digest",
		"authority_id",
		"authority_version",
		"verifier_image_digest",
		"trust_root_digest",
		"transparency_log_id",
		"transparency_entry_uuid",
		"transparency_log_index",
		"transparency_bundle_digest",
		"transparency_tree_size",
		"transparency_root_hash",
		"integrated_at",
		"verification_reference",
		"template_artifact_authority_receipt_exact_identity_unique",
		"UNIQUE (id, content_hash, subject_hash, policy_hash)",
		"template_artifact_authority_receipt_immutable",
		"authority_receipt_id",
		"authority_receipt_content_hash",
		"authority_policy_hash",
		"template_admission_attempts_authority_receipt_exact_fk",
		"template_releases_authority_receipt_exact_fk",
		"template_release_policies_exact_authority_release_fk",
		"receipt.source_tree_hash = NEW.source->>'treeHash'",
		"receipt.source_tree_hash = NEW.tree_hash",
		"receipt.sbom_digest = NEW.sbom_digest",
		"receipt.signature_bundle_digest = NEW.signature->>'bundleDigest'",
		"new v1 template admission approval is disabled",
		"new v1 TemplateRelease creation is disabled",
		"historical v1 template release policies are read-only",
		"LOCK TABLE template_release_policies IN ACCESS EXCLUSIVE MODE",
		"SET state = 'revoked'",
		"PostgreSQL validates document projection and exact lineage only",
	} {
		if !strings.Contains(upText, required) {
			t.Fatalf("Template Artifact Authority migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"receipt.artifact_digest = NEW.source->>'treeHash'",
		"receipt.artifact_digest = NEW.tree_hash",
		"http_get(",
		"curl ",
		"cosign verify",
	} {
		if strings.Contains(upText, forbidden) {
			t.Fatalf("Template Artifact Authority migration contains invalid authority behavior %q", forbidden)
		}
	}

	downText := string(down)
	for _, required := range []string{
		"rollback requires explicit handling of all v2 Template Artifact Authority lineage and receipts",
		"EXISTS (SELECT 1 FROM template_artifact_authority_receipts)",
		"schema_version = 'template-admission-attempt/v2'",
		"schema_version = 'template-release/v2'",
		"schema_version = 'template-release-policy/v2'",
		"Revoked v1 policies",
		"DROP TABLE template_artifact_authority_receipts",
		"schema_version = 'template-admission-attempt/v1'",
		"schema_version = 'template-release/v1'",
	} {
		if !strings.Contains(downText, required) {
			t.Fatalf("Template Artifact Authority rollback is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"SET state = 'approved'",
		"UPDATE template_release_policies",
	} {
		if strings.Contains(downText, forbidden) {
			t.Fatalf("Template Artifact Authority rollback silently restores legacy policy via %q", forbidden)
		}
	}
}
