package qualificationrelease

import "testing"

func TestControllerObserverRejectsCorruptRunOperationPairs(t *testing.T) {
	for name, outcome := range map[string]ControllerOutcome{
		"healthy but running":       {Kind: OutcomeHealthy, RunState: "healthy", RemoteState: "running", RunVersion: 1},
		"failed but rejected":       {Kind: OutcomeFailed, RunState: "failed", RemoteState: "rejected", RunVersion: 1},
		"error but completed":       {Kind: OutcomeFailed, RunState: "error", RemoteState: "completed", RunVersion: 1},
		"cancelled after submit":    {Kind: OutcomeFailed, RunState: "cancelled", RemoteState: "submit_unknown", RunVersion: 1},
		"queued terminal operation": {Kind: OutcomeActive, RunState: "queued", RemoteState: "completed", RunVersion: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if outcome.Validate() == nil && validControllerStatePair(outcome) {
				t.Fatalf("corrupt pair was accepted: %#v", outcome)
			}
		})
	}
}

func TestControllerObserverAcceptsOnlyDurableContractPairs(t *testing.T) {
	for _, outcome := range []ControllerOutcome{
		{Kind: OutcomeActive, RunState: "queued", RemoteState: "prepared", RunVersion: 1},
		{Kind: OutcomeActive, RunState: "reconcile_wait", RemoteState: "submit_unknown", RunVersion: 2},
		{Kind: OutcomeActive, RunState: "reconcile_blocked", RemoteState: "running", RunVersion: 3},
		{Kind: OutcomeActive, RunState: "verifying", RemoteState: "completed", RunVersion: 4},
		{Kind: OutcomeHealthy, RunState: "healthy", RemoteState: "completed", RunVersion: 4},
		{Kind: OutcomeFailed, RunState: "failed", RemoteState: "completed", RunVersion: 5},
		{Kind: OutcomeFailed, RunState: "error", RemoteState: "rejected", RunVersion: 5},
		{Kind: OutcomeFailed, RunState: "cancelled", RemoteState: "prepared", RunVersion: 2},
	} {
		if outcome.Validate() != nil || !validControllerStatePair(outcome) {
			t.Fatalf("valid pair was rejected: %#v", outcome)
		}
	}
}
