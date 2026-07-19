package sandbox

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	testSessionID   = "11111111-1111-4111-8111-111111111111"
	testActorID     = "22222222-2222-4222-8222-222222222222"
	testProjectID   = "33333333-3333-4333-8333-333333333333"
	testSnapshotID  = "44444444-4444-4444-8444-444444444444"
	testCandidateID = "55555555-5555-4555-8555-555555555555"
	testArtifactID  = "66666666-6666-4666-8666-666666666666"
	testRevisionID  = "77777777-7777-4777-8777-777777777777"
	testManifestID  = "88888888-8888-4888-8888-888888888888"
	testContractID  = "99999999-9999-4999-8999-999999999999"
	testStackID     = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	testAPIRelease  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	testWebRelease  = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	testCheckpoint  = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
)

var sandboxBaseTime = time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)

func TestSandboxSessionLifecycleRequiresIntermediateStates(t *testing.T) {
	session := newTestSession(t, cleanCandidate(t), sandboxBaseTime)
	initial := session.Snapshot()
	if initial.State != StateProvisioning || initial.Version != 1 || initial.SessionEpoch != 1 {
		t.Fatalf("unexpected initial session: %#v", initial)
	}
	if _, err := session.MarkReady(initial.Version, initial.SessionEpoch, sandboxBaseTime.Add(time.Second)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("provisioning skipped starting: %v", err)
	}

	starting := mustBeginStart(t, session, sandboxBaseTime.Add(time.Second))
	if _, err := starting.MarkSuspended(starting.Snapshot().Version, starting.Snapshot().SessionEpoch, sandboxBaseTime.Add(2*time.Second)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("starting skipped ready/suspending: %v", err)
	}
	ready := mustMarkReady(t, starting, sandboxBaseTime.Add(2*time.Second))
	if _, err := ready.MarkSuspended(ready.Snapshot().Version, ready.Snapshot().SessionEpoch, sandboxBaseTime.Add(3*time.Second)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ready skipped suspending: %v", err)
	}
	suspending, err := ready.BeginSuspend(ready.Snapshot().Version, ready.Snapshot().SessionEpoch, sandboxBaseTime.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	suspended, err := suspending.MarkSuspended(suspending.Snapshot().Version, suspending.Snapshot().SessionEpoch, sandboxBaseTime.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := suspended.MarkReady(suspended.Snapshot().Version, suspended.Snapshot().SessionEpoch, sandboxBaseTime.Add(5*time.Second)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("suspended skipped resuming: %v", err)
	}
	resuming, err := suspended.BeginResume(suspended.Snapshot().Version, suspended.Snapshot().SessionEpoch, sandboxBaseTime.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if resuming.Snapshot().SessionEpoch != suspended.Snapshot().SessionEpoch+1 {
		t.Fatalf("resume did not rotate the epoch: before=%d after=%d", suspended.Snapshot().SessionEpoch, resuming.Snapshot().SessionEpoch)
	}
	if _, err := resuming.MarkReady(resuming.Snapshot().Version, suspended.Snapshot().SessionEpoch, sandboxBaseTime.Add(6*time.Second)); !errors.Is(err, ErrEpochFenced) {
		t.Fatalf("old resume epoch was not fenced: %v", err)
	}
	if _, err := resuming.MarkReady(suspended.Snapshot().Version, resuming.Snapshot().SessionEpoch, sandboxBaseTime.Add(6*time.Second)); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("old aggregate CAS was not fenced: %v", err)
	}
	if _, err := resuming.MarkReady(
		resuming.Snapshot().Version, resuming.Snapshot().SessionEpoch, sandboxBaseTime.Add(6*time.Second),
	); !errors.Is(err, ErrCandidateMismatch) {
		t.Fatalf("resumed runtime became ready before Candidate fence rotation: %v", err)
	}
	rotatedCandidate, _, err := cleanCandidate(t).RotateSession(
		1, 1, testActorID, "sandbox resume", sandboxBaseTime.Add(5*time.Second+250*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	resumingView := resuming.Snapshot()
	bound, err := resuming.BindResumedCandidate(
		resumingView.Version, resumingView.SessionEpoch, resumingView.Candidate.Version,
		rotatedCandidate, nil, sandboxBaseTime.Add(5*time.Second+500*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	readyAgain := mustMarkReady(t, bound, sandboxBaseTime.Add(6*time.Second))
	terminating, err := readyAgain.BeginTerminate(
		readyAgain.Snapshot().Version, readyAgain.Snapshot().SessionEpoch, "user requested cleanup", sandboxBaseTime.Add(7*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := terminating.MarkReady(terminating.Snapshot().Version, terminating.Snapshot().SessionEpoch, sandboxBaseTime.Add(8*time.Second)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminating returned to ready: %v", err)
	}
	terminated, err := terminating.MarkTerminated(
		terminating.Snapshot().Version, terminating.Snapshot().SessionEpoch, sandboxBaseTime.Add(8*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := terminated.BeginStart(terminated.Snapshot().Version, terminated.Snapshot().SessionEpoch, sandboxBaseTime.Add(9*time.Second)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminated session restarted: %v", err)
	}
}

func TestSandboxCancelAndFailedCleanupExceptionsAreExplicit(t *testing.T) {
	t.Run("cancel provisioning", func(t *testing.T) {
		session := newTestSession(t, cleanCandidate(t), sandboxBaseTime)
		cancelled, err := session.Cancel(1, 1, sandboxBaseTime.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		view := cancelled.Snapshot()
		if view.State != StateTerminating || view.LastTransition.From != StateProvisioning || view.LastTransition.Reason != "cancel" {
			t.Fatalf("cancel was not an explicit terminating transition: %#v", view.LastTransition)
		}
	})

	t.Run("cancel starting", func(t *testing.T) {
		session := mustBeginStart(t, newTestSession(t, cleanCandidate(t), sandboxBaseTime), sandboxBaseTime.Add(time.Second))
		cancelled, err := session.Cancel(session.Snapshot().Version, session.Snapshot().SessionEpoch, sandboxBaseTime.Add(2*time.Second))
		if err != nil || cancelled.Snapshot().LastTransition.Reason != "cancel" {
			t.Fatalf("starting cancel failed: session=%#v err=%v", cancelled.Snapshot(), err)
		}
	})

	t.Run("cancel resuming", func(t *testing.T) {
		session := readyTestSession(t, cleanCandidate(t), sandboxBaseTime)
		suspending, err := session.BeginSuspend(session.Snapshot().Version, session.Snapshot().SessionEpoch, sandboxBaseTime.Add(3*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		suspended, err := suspending.MarkSuspended(suspending.Snapshot().Version, suspending.Snapshot().SessionEpoch, sandboxBaseTime.Add(4*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		resuming, err := suspended.BeginResume(suspended.Snapshot().Version, suspended.Snapshot().SessionEpoch, sandboxBaseTime.Add(5*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		cancelled, err := resuming.Cancel(resuming.Snapshot().Version, resuming.Snapshot().SessionEpoch, sandboxBaseTime.Add(6*time.Second))
		if err != nil || cancelled.Snapshot().LastTransition.Reason != "cancel" {
			t.Fatalf("resuming cancel failed: session=%#v err=%v", cancelled.Snapshot(), err)
		}
	})

	t.Run("failed cleanup", func(t *testing.T) {
		starting := mustBeginStart(t, newTestSession(t, cleanCandidate(t), sandboxBaseTime), sandboxBaseTime.Add(time.Second))
		failed, err := starting.Fail(starting.Snapshot().Version, starting.Snapshot().SessionEpoch, "runtime boot failed", sandboxBaseTime.Add(2*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if !hasAction(failed.Snapshot().AllowedActions, ActionTerminate) {
			t.Fatalf("failed session did not expose cleanup: %#v", failed.Snapshot().AllowedActions)
		}
		terminating, err := failed.BeginTerminate(failed.Snapshot().Version, failed.Snapshot().SessionEpoch, "release failed runtime", sandboxBaseTime.Add(3*time.Second))
		if err != nil || terminating.Snapshot().State != StateTerminating {
			t.Fatalf("failed session could not enter cleanup: %#v %v", terminating.Snapshot(), err)
		}
	})

	t.Run("terminating failure can retry cleanup", func(t *testing.T) {
		ready := readyTestSession(t, cleanCandidate(t), sandboxBaseTime)
		terminating, err := ready.BeginTerminate(ready.Snapshot().Version, ready.Snapshot().SessionEpoch, "cleanup", sandboxBaseTime.Add(3*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		failed, err := terminating.Fail(terminating.Snapshot().Version, terminating.Snapshot().SessionEpoch, "runtime cleanup failed", sandboxBaseTime.Add(4*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		retrying, err := failed.BeginTerminate(failed.Snapshot().Version, failed.Snapshot().SessionEpoch, "retry cleanup", sandboxBaseTime.Add(5*time.Second))
		if err != nil || retrying.Snapshot().State != StateTerminating {
			t.Fatalf("failed cleanup was not retryable: %#v %v", retrying.Snapshot(), err)
		}
	})
}

func TestFailOnlyAcceptsRunningLifecycleStates(t *testing.T) {
	builders := []struct {
		name  string
		build func(*testing.T) SandboxSession
	}{
		{"provisioning", func(t *testing.T) SandboxSession { return newTestSession(t, cleanCandidate(t), sandboxBaseTime) }},
		{"starting", func(t *testing.T) SandboxSession {
			return mustBeginStart(t, newTestSession(t, cleanCandidate(t), sandboxBaseTime), sandboxBaseTime.Add(time.Second))
		}},
		{"ready", func(t *testing.T) SandboxSession { return readyTestSession(t, cleanCandidate(t), sandboxBaseTime) }},
		{"suspending", func(t *testing.T) SandboxSession {
			ready := readyTestSession(t, cleanCandidate(t), sandboxBaseTime)
			result, err := ready.BeginSuspend(ready.Snapshot().Version, ready.Snapshot().SessionEpoch, sandboxBaseTime.Add(3*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			return result
		}},
		{"resuming", func(t *testing.T) SandboxSession {
			ready := readyTestSession(t, cleanCandidate(t), sandboxBaseTime)
			suspending, _ := ready.BeginSuspend(ready.Snapshot().Version, ready.Snapshot().SessionEpoch, sandboxBaseTime.Add(3*time.Second))
			suspended, _ := suspending.MarkSuspended(suspending.Snapshot().Version, suspending.Snapshot().SessionEpoch, sandboxBaseTime.Add(4*time.Second))
			result, err := suspended.BeginResume(suspended.Snapshot().Version, suspended.Snapshot().SessionEpoch, sandboxBaseTime.Add(5*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			return result
		}},
		{"terminating", func(t *testing.T) SandboxSession {
			ready := readyTestSession(t, cleanCandidate(t), sandboxBaseTime)
			result, err := ready.BeginTerminate(ready.Snapshot().Version, ready.Snapshot().SessionEpoch, "cleanup", sandboxBaseTime.Add(3*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			return result
		}},
	}
	for _, test := range builders {
		t.Run(test.name, func(t *testing.T) {
			session := test.build(t)
			view := session.Snapshot()
			failed, err := session.Fail(view.Version, view.SessionEpoch, "injected runtime failure", view.UpdatedAt.Add(time.Second))
			if err != nil || failed.Snapshot().State != StateFailed {
				t.Fatalf("running state did not fail closed: state=%s result=%#v err=%v", view.State, failed.Snapshot(), err)
			}
		})
	}

	ready := readyTestSession(t, cleanCandidate(t), sandboxBaseTime)
	suspending, _ := ready.BeginSuspend(ready.Snapshot().Version, ready.Snapshot().SessionEpoch, sandboxBaseTime.Add(3*time.Second))
	suspended, _ := suspending.MarkSuspended(suspending.Snapshot().Version, suspending.Snapshot().SessionEpoch, sandboxBaseTime.Add(4*time.Second))
	if _, err := suspended.Fail(suspended.Snapshot().Version, suspended.Snapshot().SessionEpoch, "not running", sandboxBaseTime.Add(5*time.Second)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("suspended session was treated as running: %v", err)
	}
}

func TestReadyOnlyRuntimeActionsAndBrowserDisconnect(t *testing.T) {
	session := newTestSession(t, cleanCandidate(t), sandboxBaseTime)
	view := session.Snapshot()
	if !hasAction(view.AllowedActions, ActionView) || !hasAction(view.AllowedActions, ActionCancel) || hasAction(view.AllowedActions, ActionEdit) {
		t.Fatalf("provisioning actions were not server-derived: %#v", view.AllowedActions)
	}
	for _, action := range []Action{ActionEdit, ActionPTY, ActionProcess, ActionAgent} {
		if err := session.Authorize(action, view.Version, view.SessionEpoch); !errors.Is(err, ErrActionBlocked) {
			t.Fatalf("%s was available before ready: %v", action, err)
		}
	}

	ready := readyTestSession(t, cleanCandidate(t), sandboxBaseTime)
	readyView := ready.Snapshot()
	for _, action := range []Action{ActionEdit, ActionPTY, ActionProcess, ActionAgent, ActionCheckpoint, ActionSuspend} {
		if err := ready.Authorize(action, readyView.Version, readyView.SessionEpoch); err != nil {
			t.Fatalf("ready action %s was blocked: %v", action, err)
		}
	}
	for _, action := range []Action{ActionVerify, ActionFreeze} {
		if err := ready.Authorize(action, readyView.Version, readyView.SessionEpoch); !errors.Is(err, ErrActionBlocked) {
			t.Fatalf("ready action %s ignored the exact checkpoint gate: %v", action, err)
		}
	}

	if err := ready.Authorize(ActionEdit, readyView.Version, readyView.SessionEpoch+1); !errors.Is(err, ErrEpochFenced) {
		t.Fatalf("future/foreign epoch was not fenced: %v", err)
	}

	disconnected, err := ready.BrowserDisconnected()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ready.Snapshot(), disconnected.Snapshot()) {
		t.Fatal("browser disconnect changed authoritative session state")
	}
}

func TestAllowedActionsAreDerivedForEverySessionState(t *testing.T) {
	provisioning := newTestSession(t, cleanCandidate(t), sandboxBaseTime)
	starting := mustBeginStart(t, provisioning, sandboxBaseTime.Add(time.Second))
	ready := mustMarkReady(t, starting, sandboxBaseTime.Add(2*time.Second))
	suspending, err := ready.BeginSuspend(ready.Snapshot().Version, ready.Snapshot().SessionEpoch, sandboxBaseTime.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	suspended, err := suspending.MarkSuspended(suspending.Snapshot().Version, suspending.Snapshot().SessionEpoch, sandboxBaseTime.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	resuming, err := suspended.BeginResume(suspended.Snapshot().Version, suspended.Snapshot().SessionEpoch, sandboxBaseTime.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	terminating, err := ready.BeginTerminate(ready.Snapshot().Version, ready.Snapshot().SessionEpoch, "cleanup", sandboxBaseTime.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	terminated, err := terminating.MarkTerminated(terminating.Snapshot().Version, terminating.Snapshot().SessionEpoch, sandboxBaseTime.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	failed, err := starting.Fail(starting.Snapshot().Version, starting.Snapshot().SessionEpoch, "boot failure", sandboxBaseTime.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		session SandboxSession
		actions []Action
	}{
		{"provisioning", provisioning, []Action{ActionView, ActionCancel}},
		{"starting", starting, []Action{ActionView, ActionCancel}},
		{"ready", ready, []Action{ActionView, ActionEdit, ActionPTY, ActionProcess, ActionAgent, ActionCheckpoint, ActionAbandon, ActionSuspend, ActionTerminate}},
		{"suspending", suspending, []Action{ActionView}},
		{"suspended", suspended, []Action{ActionView, ActionResume, ActionTerminate}},
		{"resuming", resuming, []Action{ActionView, ActionCancel}},
		{"terminating", terminating, []Action{ActionView}},
		{"terminated", terminated, []Action{ActionView}},
		{"failed", failed, []Action{ActionView, ActionTerminate}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.session.Snapshot().AllowedActions; !reflect.DeepEqual(got, test.actions) {
				t.Fatalf("server actions = %#v, want %#v", got, test.actions)
			}
		})
	}
}

func TestCandidateFlagsBlockFreezeAndAgentOnly(t *testing.T) {
	candidate := cleanCandidate(t)
	candidate.Conflicted = true
	candidate.Stale = true
	candidate.RebaseRequired = true
	ready := readyTestSession(t, candidate, sandboxBaseTime)
	view := ready.Snapshot()
	if hasAction(view.AllowedActions, ActionAgent) || hasAction(view.AllowedActions, ActionVerify) || hasAction(view.AllowedActions, ActionFreeze) {
		t.Fatalf("conflicted/stale candidate exposed Agent or freeze: %#v", view.AllowedActions)
	}
	for _, action := range []Action{ActionEdit, ActionPTY, ActionProcess, ActionCheckpoint, ActionSuspend} {
		if !hasAction(view.AllowedActions, action) {
			t.Fatalf("candidate flag over-blocked %s: %#v", action, view.AllowedActions)
		}
	}
	for _, code := range []BlockingCode{BlockingCandidateConflicted, BlockingCandidateStale, BlockingCandidateRebase} {
		if !hasBlockingCode(view.BlockingReasons, code) {
			t.Fatalf("missing authoritative blocking reason %s: %#v", code, view.BlockingReasons)
		}
	}
	for _, action := range []Action{ActionAgent, ActionVerify, ActionFreeze} {
		err := ready.Authorize(action, view.Version, view.SessionEpoch)
		var actionErr *ActionError
		if !errors.Is(err, ErrActionBlocked) || !errors.As(err, &actionErr) || len(actionErr.Reasons) != map[Action]int{ActionAgent: 3, ActionVerify: 4, ActionFreeze: 4}[action] {
			t.Fatalf("%s did not return exact server reasons: %#v %v", action, actionErr, err)
		}
	}
}

func TestDirtyCandidateRequiresCurrentExactCheckpoint(t *testing.T) {
	candidate, _ := dirtyCandidate(t, cleanCandidate(t), sandboxBaseTime.Add(time.Second))
	ready := readyTestSession(t, candidate, sandboxBaseTime.Add(3*time.Second))
	view := ready.Snapshot()
	if hasAction(view.AllowedActions, ActionSuspend) || hasAction(view.AllowedActions, ActionTerminate) ||
		hasAction(view.AllowedActions, ActionAbandon) ||
		!hasBlockingCode(view.BlockingReasons, BlockingExactCheckpointNeeded) {
		t.Fatalf("dirty candidate checkpoint gate is not authoritative: %#v", view)
	}
	if _, err := ready.BeginSuspend(view.Version, view.SessionEpoch, view.UpdatedAt.Add(time.Second)); !errors.Is(err, ErrCheckpointRequired) {
		t.Fatalf("dirty candidate suspended without checkpoint: %v", err)
	}

	checkpointAt := view.UpdatedAt.Add(2 * time.Second)
	snapshot, err := candidate.Checkpoint(
		candidate.Version, candidate.SessionEpoch, candidate.WriterLeaseEpoch,
		testCheckpoint, testActorID, "before hibernate", checkpointAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	wrong := snapshot
	wrong.CandidateVersion--
	if _, err := ready.RecordCheckpoint(view.Version, view.SessionEpoch, wrong, checkpointAt); !errors.Is(err, ErrCheckpointMismatch) {
		t.Fatalf("non-exact checkpoint was accepted: %v", err)
	}
	checkpointed, err := ready.RecordCheckpoint(view.Version, view.SessionEpoch, snapshot, checkpointAt)
	if err != nil {
		t.Fatal(err)
	}
	checkpointedView := checkpointed.Snapshot()
	if checkpointedView.LatestCheckpoint == nil || !hasAction(checkpointedView.AllowedActions, ActionSuspend) ||
		!hasAction(checkpointedView.AllowedActions, ActionTerminate) ||
		!hasAction(checkpointedView.AllowedActions, ActionVerify) ||
		!hasAction(checkpointedView.AllowedActions, ActionFreeze) ||
		!hasAction(checkpointedView.AllowedActions, ActionAbandon) {
		t.Fatalf("exact checkpoint did not unblock lifecycle actions: %#v", checkpointedView)
	}
	suspending, err := checkpointed.BeginSuspend(
		checkpointedView.Version, checkpointedView.SessionEpoch, checkpointedView.UpdatedAt.Add(time.Second),
	)
	if err != nil || suspending.Snapshot().State != StateSuspending {
		t.Fatalf("checkpointed candidate did not suspend: %#v %v", suspending.Snapshot(), err)
	}
}

func TestInitialExactCheckpointMakesDirtySessionSafelyCancellable(t *testing.T) {
	candidate, _ := dirtyCandidate(t, cleanCandidate(t), sandboxBaseTime.Add(time.Second))
	checkpointAt := candidate.UpdatedAt.Add(time.Second)
	checkpoint, err := candidate.Checkpoint(
		candidate.Version, candidate.SessionEpoch, candidate.WriterLeaseEpoch,
		testCheckpoint, testActorID, "persist before provisioning", checkpointAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	input := testSessionInput(candidate)
	input.LatestCheckpoint = &checkpoint
	session, err := NewSession(input, checkpointAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	view := session.Snapshot()
	if view.LatestCheckpoint == nil || !hasAction(view.AllowedActions, ActionCancel) {
		t.Fatalf("initial exact checkpoint did not make dirty provisioning cancellable: %#v", view)
	}

	wrong := checkpoint
	wrong.Tree.TreeHash = sandboxDigest("9")
	input.LatestCheckpoint = &wrong
	if _, err := NewSession(input, checkpointAt.Add(time.Second)); !errors.Is(err, ErrCheckpointMismatch) {
		t.Fatalf("forged initial checkpoint was accepted: %v", err)
	}
}

func TestDirtyResumeRequiresRepositoryFenceAndFreshExactCheckpoint(t *testing.T) {
	candidate, _ := dirtyCandidate(t, cleanCandidate(t), sandboxBaseTime.Add(time.Second))
	checkpointAt := sandboxBaseTime.Add(3 * time.Second)
	checkpoint, err := candidate.Checkpoint(
		candidate.Version, candidate.SessionEpoch, candidate.WriterLeaseEpoch,
		testCheckpoint, testActorID, "before suspend", checkpointAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	input := testSessionInput(candidate)
	input.LatestCheckpoint = &checkpoint
	session, err := NewSession(input, sandboxBaseTime.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	starting := mustBeginStart(t, session, sandboxBaseTime.Add(5*time.Second))
	ready := mustMarkReady(t, starting, sandboxBaseTime.Add(6*time.Second))
	suspending, err := ready.BeginSuspend(ready.Snapshot().Version, ready.Snapshot().SessionEpoch, sandboxBaseTime.Add(7*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	suspended, err := suspending.MarkSuspended(
		suspending.Snapshot().Version, suspending.Snapshot().SessionEpoch, sandboxBaseTime.Add(8*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	resuming, err := suspended.BeginResume(
		suspended.Snapshot().Version, suspended.Snapshot().SessionEpoch, sandboxBaseTime.Add(9*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}

	rotated, _, err := candidate.RotateSession(
		candidate.Version, candidate.SessionEpoch, testActorID, "resume Candidate", sandboxBaseTime.Add(9*time.Second+100*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	rotated, _, err = rotated.AcquireLease(
		rotated.Version, testActorID, 20*time.Minute, sandboxBaseTime.Add(9*time.Second+200*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	view := resuming.Snapshot()
	if _, err := resuming.BindResumedCandidate(
		view.Version, view.SessionEpoch, view.Candidate.Version, rotated, nil,
		sandboxBaseTime.Add(9*time.Second+300*time.Millisecond),
	); !errors.Is(err, ErrCheckpointRequired) {
		t.Fatalf("dirty resumed Candidate bound without a fresh checkpoint: %v", err)
	}
	if _, err := resuming.BindResumedCandidate(
		view.Version, view.SessionEpoch, view.Candidate.Version, rotated, &checkpoint,
		sandboxBaseTime.Add(9*time.Second+300*time.Millisecond),
	); !errors.Is(err, ErrCheckpointMismatch) {
		t.Fatalf("pre-rotation checkpoint survived a new fence: %v", err)
	}
	fresh, err := rotated.Checkpoint(
		rotated.Version, rotated.SessionEpoch, rotated.WriterLeaseEpoch,
		"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", testActorID, "after resume fence",
		sandboxBaseTime.Add(9*time.Second+300*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := resuming.BindResumedCandidate(
		view.Version, view.SessionEpoch, view.Candidate.Version, rotated, &fresh,
		sandboxBaseTime.Add(9*time.Second+400*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bound.MarkReady(
		bound.Snapshot().Version, bound.Snapshot().SessionEpoch, sandboxBaseTime.Add(10*time.Second),
	); err != nil {
		t.Fatalf("fresh exact checkpoint did not unblock resumed runtime: %v", err)
	}
}

func TestCandidateCASAndNewEditInvalidatePriorCheckpoint(t *testing.T) {
	candidate := cleanCandidate(t)
	ready := readyTestSession(t, candidate, sandboxBaseTime)
	firstUpdate, lease := dirtyCandidate(t, candidate, sandboxBaseTime.Add(3*time.Second))
	view := ready.Snapshot()
	if _, err := ready.UpdateCandidate(view.Version, view.SessionEpoch, view.Candidate.Version+1, firstUpdate, sandboxBaseTime.Add(5*time.Second)); !errors.Is(err, ErrCandidateVersionConflict) {
		t.Fatalf("stale candidate CAS was accepted: %v", err)
	}
	updated, err := ready.UpdateCandidate(view.Version, view.SessionEpoch, view.Candidate.Version, firstUpdate, sandboxBaseTime.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	updatedView := updated.Snapshot()
	snapshot, err := firstUpdate.Checkpoint(
		firstUpdate.Version, firstUpdate.SessionEpoch, firstUpdate.WriterLeaseEpoch,
		testCheckpoint, testActorID, "exact current tree", sandboxBaseTime.Add(6*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	checkpointed, err := updated.RecordCheckpoint(updatedView.Version, updatedView.SessionEpoch, snapshot, sandboxBaseTime.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}

	secondOperation := repository.FileOperation{
		ID: "second-edit", Kind: repository.OperationUpsert, Path: "README.md",
		ExpectedHash: sandboxDigest("1"), ContentHash: sandboxDigest("4"), ByteSize: 12, Mode: "100644",
	}
	secondUpdate, _, err := firstUpdate.Apply(
		firstUpdate.Version, firstUpdate.SessionEpoch, lease.Epoch, testActorID, "user", secondOperation, sandboxBaseTime.Add(7*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	checkpointedView := checkpointed.Snapshot()
	if _, err := checkpointed.UpdateCandidate(
		checkpointedView.Version, checkpointedView.SessionEpoch, checkpointedView.Candidate.Version-1,
		secondUpdate, sandboxBaseTime.Add(8*time.Second),
	); !errors.Is(err, ErrCandidateVersionConflict) {
		t.Fatalf("old candidate version was not fenced: %v", err)
	}
	edited, err := checkpointed.UpdateCandidate(
		checkpointedView.Version, checkpointedView.SessionEpoch, checkpointedView.Candidate.Version,
		secondUpdate, sandboxBaseTime.Add(8*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	editedView := edited.Snapshot()
	if hasAction(editedView.AllowedActions, ActionTerminate) || !hasBlockingCode(editedView.BlockingReasons, BlockingExactCheckpointNeeded) {
		t.Fatalf("new candidate version did not stale the prior checkpoint: %#v", editedView)
	}
	if _, err := edited.BeginTerminate(editedView.Version, editedView.SessionEpoch, "cleanup", sandboxBaseTime.Add(9*time.Second)); !errors.Is(err, ErrCheckpointRequired) {
		t.Fatalf("stale checkpoint allowed termination: %v", err)
	}
}

func TestQuotaExactReferencesAndAllowedPortsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NewSessionInput)
	}{
		{"zero cpu", func(input *NewSessionInput) { input.Quota.CPUMillis = 0 }},
		{"unbounded ttl", func(input *NewSessionInput) { input.TTL.MaxRuntime = 8 * 24 * time.Hour }},
		{"idle exceeds runtime", func(input *NewSessionInput) { input.TTL.IdleHibernateAfter = 9 * time.Hour }},
		{"ports exceed quota", func(input *NewSessionInput) { input.Quota.PreviewPortLimit = 1 }},
		{"unknown port service", func(input *NewSessionInput) { input.Ports[0].ServiceID = "missing" }},
		{"duplicate port number", func(input *NewSessionInput) { input.Ports[1].Number = input.Ports[0].Number }},
		{"unsafe port", func(input *NewSessionInput) { input.Ports[0].Number = 80 }},
		{"invalid protocol", func(input *NewSessionInput) { input.Ports[0].Protocol = "udp" }},
		{"unpinned runner", func(input *NewSessionInput) { input.RunnerImageDigest = "runner:latest" }},
		{"forged release hash", func(input *NewSessionInput) { input.Services[0].TemplateRelease.ContentHash = "main" }},
		{"conflicting release identity", func(input *NewSessionInput) {
			input.Services[1].TemplateRelease.ID = input.Services[0].TemplateRelease.ID
		}},
		{"duplicate service profile", func(input *NewSessionInput) { input.Services[0].Profiles = []string{"dev", "dev"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := testSessionInput(cleanCandidate(t))
			test.mutate(&input)
			if _, err := NewSession(input, sandboxBaseTime); !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("invalid session policy was accepted: %v", err)
			}
		})
	}

	session := newTestSession(t, cleanCandidate(t), sandboxBaseTime)
	view := session.Snapshot()
	if view.AllowedServices[0].ID != "api" || !reflect.DeepEqual(view.AllowedServices[0].Profiles, []string{"dev", "test"}) ||
		view.AllowedPorts[0].Name != "api-http" {
		t.Fatalf("allowed services/ports were not canonicalized: services=%#v ports=%#v", view.AllowedServices, view.AllowedPorts)
	}
	if len(view.TemplateReleases) != 2 || view.TemplateReleases[0].ID != testAPIRelease || view.TemplateReleases[1].ID != testWebRelease {
		t.Fatalf("top-level exact template lineage was not derived canonically: %#v", view.TemplateReleases)
	}
	if !view.TTL.ExpiresAt.Equal(sandboxBaseTime.Add(8*time.Hour)) ||
		!view.TTL.IdleDeadline.Equal(sandboxBaseTime.Add(30*time.Minute)) {
		t.Fatalf("TTL deadlines were not derived by the server: %#v", view.TTL)
	}
}

func TestTemplateOnlyCandidateSessionKeepsExactRepositorySnapshotBaseline(t *testing.T) {
	candidate := cleanCandidate(t)
	candidate.BaseWorkspaceRevision = nil
	if err := candidate.Validate(); err != nil {
		t.Fatalf("template-only Candidate is invalid: %v", err)
	}

	session := newTestSession(t, candidate, sandboxBaseTime)
	view := session.Snapshot()
	if view.BaseWorkspaceRevision != nil ||
		view.Candidate.RepositorySnapshotID != candidate.RepositorySnapshotID ||
		view.Candidate.BaseTreeHash != candidate.BaseTreeHash {
		t.Fatalf("session invented or lost the template RepositorySnapshot baseline: %#v", view)
	}

	ready := mustMarkReady(t, mustBeginStart(t, session, sandboxBaseTime.Add(time.Second)), sandboxBaseTime.Add(2*time.Second))
	dirty, _ := dirtyCandidate(t, candidate, sandboxBaseTime.Add(3*time.Second))
	readyView := ready.Snapshot()
	updated, err := ready.UpdateCandidate(
		readyView.Version,
		readyView.SessionEpoch,
		readyView.Candidate.Version,
		dirty,
		sandboxBaseTime.Add(5*time.Second),
	)
	if err != nil {
		t.Fatalf("template-only Candidate update was rejected: %v", err)
	}
	if updated.Snapshot().BaseWorkspaceRevision != nil {
		t.Fatal("template-only Candidate update invented a WorkspaceRevision")
	}

	forged := dirty
	base := repository.ExactRevisionReference{
		ArtifactID: testArtifactID, RevisionID: testRevisionID, ContentHash: sandboxDigest("2"),
	}
	forged.BaseWorkspaceRevision = &base
	if _, err := ready.UpdateCandidate(
		readyView.Version,
		readyView.SessionEpoch,
		readyView.Candidate.Version,
		forged,
		sandboxBaseTime.Add(5*time.Second),
	); !errors.Is(err, ErrCandidateMismatch) {
		t.Fatalf("template RepositorySnapshot baseline was replaced by a WorkspaceRevision: %v", err)
	}
}

func TestSandboxSessionSnapshotsAndClonesAreImmutable(t *testing.T) {
	candidate := cleanCandidate(t)
	input := testSessionInput(candidate)
	session, err := NewSession(input, sandboxBaseTime)
	if err != nil {
		t.Fatal(err)
	}
	input.Services[0].Profiles[0] = "mutated-input"
	input.Ports[0].Name = "mutated-input"
	input.Candidate.BuildManifest.ContentHash = sandboxDigest("f")

	view := session.Snapshot()
	view.BaseWorkspaceRevision.ContentHash = sandboxDigest("9")
	view.AllowedServices[0].Profiles[0] = "mutated-view"
	view.AllowedPorts[0].Name = "mutated-view"
	view.AllowedActions[0] = ActionAgent
	view.BlockingReasons[0].Actions[0] = ActionAgent
	view.TemplateReleases[0].ContentHash = sandboxDigest("0")
	second := session.Snapshot()
	if second.AllowedServices[0].Profiles[0] == "mutated-view" || second.AllowedPorts[0].Name == "mutated-view" ||
		second.BuildManifest.ContentHash != sandboxDigest("a") ||
		second.BaseWorkspaceRevision.ContentHash != sandboxDigest("2") || second.AllowedActions[0] != ActionView ||
		second.TemplateReleases[0].ContentHash == sandboxDigest("0") {
		t.Fatalf("snapshot or constructor input mutated the aggregate: %#v", second)
	}

	clone := session.Clone()
	advanced := mustBeginStart(t, clone, sandboxBaseTime.Add(time.Second))
	if session.Snapshot().State != StateProvisioning || advanced.Snapshot().State != StateStarting {
		t.Fatal("transitioning a clone mutated the original session")
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var forged SandboxSession
	if err := json.Unmarshal(encoded, &forged); !errors.Is(err, ErrImmutableSession) {
		t.Fatalf("public JSON hydration could forge a session: %v", err)
	}
	if err := (SandboxSession{}).Validate(); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("zero session passed validation: %v", err)
	}
}

func TestSandboxSessionWithoutPreviewPortsRemainsValidAcrossCloneAndStart(t *testing.T) {
	input := testSessionInput(cleanCandidate(t))
	input.Ports = nil
	session, err := NewSession(input, sandboxBaseTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Clone().Validate(); err != nil {
		t.Fatalf("zero-port session clone lost its canonical empty collection: %v", err)
	}
	starting, err := session.BeginStart(1, 1, sandboxBaseTime.Add(time.Second))
	if err != nil {
		t.Fatalf("zero-port session could not start: %v", err)
	}
	if starting.Snapshot().AllowedPorts == nil || len(starting.Snapshot().AllowedPorts) != 0 {
		t.Fatalf("zero-port session did not preserve an explicit empty collection: %#v", starting.Snapshot().AllowedPorts)
	}
}

func cleanCandidate(t *testing.T) repository.CandidateWorkspace {
	t.Helper()
	tree, err := repository.NewTree([]repository.TreeFile{
		{Path: "README.md", Mode: "100644", ContentHash: sandboxDigest("1"), ByteSize: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	base := repository.ExactRevisionReference{ArtifactID: testArtifactID, RevisionID: testRevisionID, ContentHash: sandboxDigest("2")}
	snapshot := repository.RepositorySnapshot{
		ID: testSnapshotID, ProjectID: testProjectID,
		BuildManifest:         repository.ExactReference{ID: testManifestID, ContentHash: sandboxDigest("a")},
		BuildContract:         repository.ExactReference{ID: testContractID, ContentHash: sandboxDigest("b")},
		FullStackTemplate:     repository.ExactReference{ID: testStackID, ContentHash: sandboxDigest("c")},
		BaseWorkspaceRevision: &base, Tree: tree, CreatedBy: testActorID, CreatedAt: sandboxBaseTime.Add(-2 * time.Minute),
	}
	candidate, err := repository.NewCandidate(testCandidateID, snapshot, testActorID, sandboxBaseTime.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func dirtyCandidate(t *testing.T, candidate repository.CandidateWorkspace, at time.Time) (repository.CandidateWorkspace, repository.WriterLease) {
	t.Helper()
	leased, lease, err := candidate.AcquireLease(candidate.Version, testActorID, 20*time.Minute, at)
	if err != nil {
		t.Fatal(err)
	}
	operation := repository.FileOperation{
		ID: "first-edit", Kind: repository.OperationUpsert, Path: "src/main.go",
		ContentHash: sandboxDigest("3"), ByteSize: 20, Mode: "100644",
	}
	dirty, _, err := leased.Apply(leased.Version, leased.SessionEpoch, lease.Epoch, testActorID, "user", operation, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return dirty, lease
}

func testSessionInput(candidate repository.CandidateWorkspace) NewSessionInput {
	return NewSessionInput{
		ID: testSessionID, ActorID: testActorID, Candidate: candidate,
		RunnerImageDigest: sandboxDigest("d"),
		Quota: Quota{
			CPUMillis: 2_000, MemoryBytes: 4 << 30, WorkspaceBytes: 10 << 30, PIDLimit: 256, PreviewPortLimit: 3,
		},
		TTL: TTLPolicy{IdleHibernateAfter: 30 * time.Minute, MaxRuntime: 8 * time.Hour},
		Services: []AllowedService{
			{ID: "web", Kind: "web", Profiles: []string{"test", "dev"}, TemplateRelease: repository.ExactReference{ID: testWebRelease, ContentHash: sandboxDigest("e")}},
			{ID: "api", Kind: "api", Profiles: []string{"test", "dev"}, TemplateRelease: repository.ExactReference{ID: testAPIRelease, ContentHash: sandboxDigest("f")}},
		},
		Ports: []AllowedPort{
			{Name: "web-http", ServiceID: "web", Number: 3000, Protocol: "http"},
			{Name: "api-http", ServiceID: "api", Number: 8080, Protocol: "http"},
		},
	}
}

func newTestSession(t *testing.T, candidate repository.CandidateWorkspace, now time.Time) SandboxSession {
	t.Helper()
	session, err := NewSession(testSessionInput(candidate), now)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func readyTestSession(t *testing.T, candidate repository.CandidateWorkspace, now time.Time) SandboxSession {
	t.Helper()
	session := newTestSession(t, candidate, now)
	session = mustBeginStart(t, session, now.Add(time.Second))
	return mustMarkReady(t, session, now.Add(2*time.Second))
}

func mustBeginStart(t *testing.T, session SandboxSession, now time.Time) SandboxSession {
	t.Helper()
	view := session.Snapshot()
	next, err := session.BeginStart(view.Version, view.SessionEpoch, now)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func mustMarkReady(t *testing.T, session SandboxSession, now time.Time) SandboxSession {
	t.Helper()
	view := session.Snapshot()
	next, err := session.MarkReady(view.Version, view.SessionEpoch, now)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func hasAction(actions []Action, expected Action) bool {
	for _, action := range actions {
		if action == expected {
			return true
		}
	}
	return false
}

func hasBlockingCode(reasons []BlockingReason, expected BlockingCode) bool {
	for _, reason := range reasons {
		if reason.Code == expected {
			return true
		}
	}
	return false
}

func sandboxDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
