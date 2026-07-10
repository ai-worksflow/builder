package core

import (
	"encoding/json"
	"testing"
)

func TestDiffAnchorsTracksStableIDs(t *testing.T) {
	t.Parallel()
	before := json.RawMessage(`{"blocks":[{"id":"b1","type":"requirement","requirementId":"REQ-001","description":"old"},{"id":"b2","type":"requirement","requirementId":"REQ-002"}]}`)
	after := json.RawMessage(`{"blocks":[{"id":"b1","type":"requirement","requirementId":"REQ-001","description":"new"},{"id":"b3","type":"requirement","requirementId":"REQ-003"}]}`)
	changes := diffAnchors(before, after)
	changeMap := map[string]string{}
	for _, change := range changes {
		changeMap[change.AnchorID] = change.Change
	}
	if changeMap["REQ-001"] != "modified" || changeMap["REQ-002"] != "removed" || changeMap["REQ-003"] != "added" {
		t.Fatalf("unexpected changes: %#v", changes)
	}
}

func TestRemovedAnchorsBlockDownstream(t *testing.T) {
	t.Parallel()
	if got := impactSeverity("removed", "drives"); got != "blocked" {
		t.Fatalf("severity=%s, want blocked", got)
	}
	if got := impactSeverity("modified", "drives"); got != "needs_sync" {
		t.Fatalf("severity=%s, want needs_sync", got)
	}
}
