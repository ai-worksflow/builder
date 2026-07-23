package sandbox

import (
	"testing"
	"time"
)

func TestSandboxActivityProjectionAllowsOnlyExpiryCappedPostTTLReconciliation(t *testing.T) {
	created := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	expires := created.Add(time.Hour)
	row := sandboxSessionRow{
		ID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 3,
		UpdatedAt: expires.Add(time.Minute), ExpiresAt: expires,
	}
	activity := sandboxSessionActivityRow{
		SessionID: row.ID, SessionEpoch: row.SessionEpoch,
		LastActivityAt: expires, IdleDeadline: expires,
	}
	if !sandboxActivityProjectionMatches(row, activity) {
		t.Fatal("exact expiry-capped activity was rejected after absolute TTL")
	}
	activity.LastActivityAt = expires.Add(-time.Second)
	if sandboxActivityProjectionMatches(row, activity) {
		t.Fatal("arbitrarily stale activity was accepted after absolute TTL")
	}
	activity.LastActivityAt = expires
	activity.IdleDeadline = expires.Add(-time.Second)
	if sandboxActivityProjectionMatches(row, activity) {
		t.Fatal("activity beyond the idle deadline was accepted")
	}
}
