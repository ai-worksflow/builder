package migrations

import (
	"strings"
	"testing"
)

func TestAuthSessionReceiptMigrationForbidsPersistedSecretsAndHasRollback(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000010_auth_session_receipts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000010_auth_session_receipts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upSQL := strings.ToLower(string(up))
	for _, required := range []string{
		"idempotency_auth_receipt_safe_check",
		"auth_session_sign_up_v1",
		"auth_session_sign_in_v1",
		"auth_session_refresh_v1",
		"scope ~ '^auth:(sign-up|sign-in|refresh):[0-9a-f]{64}$'",
		"request_hash ~ '^[0-9a-f]{64}$'",
		"resource_id ~* '^[0-9a-f]{8}",
		"response_body is null",
		"response_headers, '{}'::jsonb",
		"idempotency_auth_refresh_replay_idx",
		"idempotency_key, resource_id",
	} {
		if !strings.Contains(upSQL, required) {
			t.Errorf("auth receipt migration is missing %q", required)
		}
	}
	downSQL := strings.ToLower(string(down))
	if !strings.Contains(downSQL, "drop index if exists idempotency_auth_refresh_replay_idx") ||
		!strings.Contains(downSQL, "drop constraint if exists idempotency_auth_receipt_safe_check") {
		t.Fatal("auth receipt down migration does not fully remove its index and constraint")
	}
	for _, forbidden := range []string{"refresh_token", "session_token", "csrf_token", "set-cookie"} {
		if strings.Contains(upSQL, forbidden) {
			t.Fatalf("auth receipt migration must not add a persisted secret column: %q", forbidden)
		}
	}
}
