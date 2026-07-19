package migrations

import (
	"strings"
	"testing"
)

func TestSandboxLifecycleDeadlineMigrationContract(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000047_sandbox_lifecycle_deadlines.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000047_sandbox_lifecycle_deadlines.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upSQL := string(up)
	for _, fragment := range []string{
		"sandbox-lifecycle-worker@system.worksflow",
		"CREATE TABLE sandbox_session_activity",
		"sync_sandbox_session_activity_from_projection",
		"sandbox_session_activity_deadline_idx",
		"CREATE TABLE sandbox_lifecycle_deadline_leases",
		"session_id uuid PRIMARY KEY REFERENCES sandbox_sessions(id)",
		"action IN ('suspend', 'terminate')",
		"lease_epoch bigint NOT NULL CHECK (lease_epoch > 0)",
		"sandbox_lifecycle_deadline_retry_idx",
	} {
		if !strings.Contains(upSQL, fragment) {
			t.Fatalf("sandbox deadline migration is missing %q", fragment)
		}
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS sandbox_lifecycle_deadline_leases") {
		t.Fatal("sandbox deadline down migration does not remove its operational lease table")
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS sandbox_session_activity") {
		t.Fatal("sandbox deadline down migration does not remove its activity projection")
	}
}
