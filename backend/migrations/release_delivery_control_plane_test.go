package migrations

import (
	"strings"
	"testing"
)

func TestReleaseDeliveryControlPlaneDeclaresImmutableExactAuthority(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000046_release_delivery_control_plane.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000046_release_delivery_control_plane.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TABLE release_preview_runs",
		"CREATE TABLE release_preview_receipts",
		"CREATE TABLE release_promotion_approvals",
		"CREATE TABLE release_deployment_runs",
		"CREATE TABLE release_production_receipts",
		"CREATE TABLE release_deployment_revisions",
		"PreviewReceipt requires the exact verifying Run and identical ReleaseBundle artifacts",
		"PromotionApproval requires one exact passed PreviewReceipt for the same ReleaseBundle",
		"ProductionReceipt requires the exact verifying Run, passed PreviewReceipt, Approval, Bundle, and rollback source",
		"DeploymentRevision requires the exact passed ProductionReceipt for the verifying Run",
		"FOREIGN KEY (production_receipt_id, production_receipt_hash)",
		"FOREIGN KEY (source_revision_id, source_revision_hash)",
		"Release delivery receipts, approvals, and revisions are immutable",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("release delivery migration is missing %q", expected)
		}
	}
	for _, forbidden := range []string{"candidate_verification_receipts", "REFERENCES quality_runs"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("release delivery authority trusts forbidden source %q", forbidden)
		}
	}
	for _, expected := range []string{
		"DROP TABLE IF EXISTS release_deployment_revisions",
		"DROP TABLE IF EXISTS release_production_receipts",
		"DROP TABLE IF EXISTS release_deployment_runs",
		"DROP TABLE IF EXISTS release_promotion_approvals",
		"DROP TABLE IF EXISTS release_preview_receipts",
		"DROP TABLE IF EXISTS release_preview_runs",
	} {
		if !strings.Contains(string(down), expected) {
			t.Fatalf("release delivery rollback is missing %q", expected)
		}
	}
}
