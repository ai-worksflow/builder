package sandbox

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSandboxDeadlineStorePostgresCanary(t *testing.T) {
	fixture := openSandboxStorePostgresFixture(t)
	sessionID := uuid.New()
	input := fixture.sessionInput(sessionID)
	input.TTL = TTLPolicy{IdleHibernateAfter: time.Second, MaxRuntime: 3 * time.Second}
	created, err := fixture.store.Create(fixture.context, input, time.Now().UTC())
	if err != nil {
		t.Fatalf("create deadline SandboxSession: %v", err)
	}
	view := created.Snapshot()
	view = transitionSandboxStoreSession(
		t, fixture, sessionID, view.Version, view.SessionEpoch,
		StateStarting, "runner allocated", 2, 1,
	)
	view = transitionSandboxStoreSession(
		t, fixture, sessionID, view.Version, view.SessionEpoch,
		StateReady, "runtime healthy", 3, 1,
	)

	waitUntilSandboxDeadline(t, view.TTL.IdleDeadline)
	first, err := fixture.store.ClaimDueDeadline(fixture.context, "deadline-worker-a", 5*time.Second)
	if err != nil {
		t.Fatalf("claim idle deadline: %v", err)
	}
	if first == nil || first.SessionID != sessionID.String() || first.ProjectID != fixture.projectID.String() ||
		first.Action != DeadlineSuspend || first.Owner != "deadline-worker-a" || first.LeaseEpoch != 1 ||
		first.ObservedAt.Before(view.TTL.IdleDeadline) {
		t.Fatalf("idle deadline claim lost exact identity: %#v", first)
	}
	if err := fixture.store.TouchSandboxActivity(
		fixture.context, sessionID.String(), view.SessionEpoch,
	); !errors.Is(err, ErrEpochFenced) {
		t.Fatalf("live lifecycle claim did not fence heartbeat activity: %v", err)
	}
	if duplicate, err := fixture.store.ClaimDueDeadline(
		fixture.context, "deadline-worker-b", 5*time.Second,
	); err != nil || duplicate != nil {
		t.Fatalf("concurrent deadline claim was not excluded: lease=%#v err=%v", duplicate, err)
	}
	if err := fixture.store.RetryDeadline(
		fixture.context, *first, time.Second, "runtime temporarily unavailable",
	); err != nil {
		t.Fatalf("schedule deadline retry: %v", err)
	}
	if early, err := fixture.store.ClaimDueDeadline(
		fixture.context, "deadline-worker-b", 5*time.Second,
	); err != nil || early != nil {
		t.Fatalf("deadline retry backoff was ignored: lease=%#v err=%v", early, err)
	}
	time.Sleep(1100 * time.Millisecond)
	second, err := fixture.store.ClaimDueDeadline(fixture.context, "deadline-worker-b", 5*time.Second)
	if err != nil {
		t.Fatalf("reclaim elapsed deadline lease: %v", err)
	}
	if second == nil || second.Action != DeadlineSuspend || second.Owner != "deadline-worker-b" ||
		second.LeaseEpoch != first.LeaseEpoch+1 {
		t.Fatalf("deadline takeover did not advance its fence: first=%#v second=%#v", first, second)
	}
	if err := fixture.store.CompleteDeadline(fixture.context, *first); !errors.Is(err, ErrDeadlineLeaseLost) {
		t.Fatalf("stale deadline worker completed a newer lease: %v", err)
	}
	if err := fixture.store.CompleteDeadline(fixture.context, *second); err != nil {
		t.Fatalf("complete current deadline lease: %v", err)
	}

	if err := fixture.store.TouchSandboxActivity(
		fixture.context, sessionID.String(), view.SessionEpoch,
	); err != nil {
		t.Fatalf("touch activity after lease completion: %v", err)
	}
	active, err := fixture.store.Get(fixture.context, fixture.projectID.String(), sessionID.String())
	if err != nil {
		t.Fatalf("load activity-projected SandboxSession: %v", err)
	}
	activeView := active.Snapshot()
	if !activeView.TTL.IdleDeadline.After(second.ObservedAt) ||
		activeView.TTL.IdleDeadline.After(activeView.TTL.ExpiresAt) {
		t.Fatalf("activity did not extend idle deadline within absolute TTL: %#v", activeView.TTL)
	}

	waitUntilSandboxDeadline(t, activeView.TTL.ExpiresAt)
	expired, err := fixture.store.ClaimDueDeadline(fixture.context, "deadline-worker-c", 5*time.Second)
	if err != nil {
		t.Fatalf("claim absolute deadline: %v", err)
	}
	if expired == nil || expired.SessionID != sessionID.String() || expired.Action != DeadlineTerminate {
		t.Fatalf("absolute TTL did not override idle hibernate: %#v", expired)
	}
	if err := fixture.store.CompleteDeadline(fixture.context, *expired); err != nil {
		t.Fatalf("complete absolute deadline lease: %v", err)
	}
}

func waitUntilSandboxDeadline(t *testing.T, deadline time.Time) {
	t.Helper()
	wait := time.Until(deadline.Add(100 * time.Millisecond))
	if wait > 0 {
		time.Sleep(wait)
	}
}
