package migrations

import (
	"strings"
	"testing"
)

func TestReleaseDeliveryNestedAuthorityRecomputesCompleteEmbeddedFacts(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000060_release_delivery_nested_authority.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000060_release_delivery_nested_authority.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"LOCK TABLE release_delivery_operations IN SHARE ROW EXCLUSIVE MODE",
		"CREATE OR REPLACE FUNCTION release_delivery_embedded_hash_is_exact",
		"release_delivery_canonical_json(",
		"jsonb_set(document, ARRAY[hash_field], '\"\"'::jsonb, false)",
		"payload->'releaseBundle', 'bundleHash'",
		"payload->'previewReceipt', 'payloadHash'",
		"payload->'promotionApproval', 'payloadHash'",
		"source_revision, 'payloadHash'",
		"jsonb_typeof(document) IS DISTINCT FROM 'object'",
		"source_revision IS DISTINCT FROM 'null'::jsonb",
		"release-preview-receipt/v2",
		"release-deployment-revision/v2",
		"cannot establish nested release authority",
		"CREATE TRIGGER release_delivery_operation_nested_authority_guard",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("nested delivery authority migration is missing %q", expected)
		}
	}
	if strings.Contains(text, "IMMUTABLE\nSTRICT") {
		t.Fatal("nested hash helper must return false, not SQL NULL, for missing documents")
	}
	for _, forbidden := range []string{
		"UPDATE release_delivery_operations",
		"DELETE FROM release_delivery_operations",
		"SET request_document",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("nested delivery authority migration mutates authority via %q", forbidden)
		}
	}
	if !strings.Contains(string(down), "cannot downgrade nested release authority while delivery Operations exist") {
		t.Fatal("nested delivery authority downgrade is not fail closed")
	}
}
