package migrations

import (
	"strings"
	"testing"
)

func TestRepositorySnapshotReceiptMigrationDeclaresAppendOnlyCompletionMarker(t *testing.T) {
	up, err := files.ReadFile("000067_repository_snapshot_receipts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000067_repository_snapshot_receipts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upText := string(up)
	for _, expected := range []string{
		"CREATE TABLE repository_snapshot_receipts",
		"repository-snapshot-receipt/v1",
		"repository-snapshot-receipt-subject/v1",
		"repository-snapshot-tree-commitment/v1",
		"FOREIGN KEY (snapshot_id, project_id)",
		"UNIQUE (snapshot_id, project_id, content_hash)",
		"REVOKE ALL ON TABLE repository_snapshot_receipts FROM PUBLIC",
		"GRANT SELECT, INSERT ON TABLE",
		"OWNER TO worksflow_migration_owner",
	} {
		if !strings.Contains(upText, expected) {
			t.Fatalf("RepositorySnapshot receipt migration is missing %q", expected)
		}
	}
	for _, forbidden := range []string{
		"GRANT UPDATE", "GRANT DELETE", "ON DELETE CASCADE",
	} {
		if strings.Contains(upText, forbidden) {
			t.Fatalf("append-only RepositorySnapshot receipts contain forbidden SQL %q", forbidden)
		}
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS repository_snapshot_receipts") {
		t.Fatal("RepositorySnapshot receipt rollback does not drop its table")
	}
}
