package sandbox

import (
	"sort"

	"github.com/worksflow/builder/backend/internal/repository"
)

var actionOrder = []Action{
	ActionView,
	ActionCancel,
	ActionEdit,
	ActionPTY,
	ActionProcess,
	ActionAgent,
	ActionCheckpoint,
	ActionVerify,
	ActionFreeze,
	ActionAbandon,
	ActionSuspend,
	ActionResume,
	ActionTerminate,
	ActionViewLogs,
	ActionRestoreCheckpoint,
	ActionNewSession,
	ActionViewAudit,
	ActionViewSnapshots,
}

func deriveAllowedActions(document sandboxSessionDocument) []Action {
	allowed := map[Action]bool{ActionView: true}
	switch document.State {
	case StateProvisioning, StateStarting, StateResuming:
		allowed[ActionCancel] = true
	case StateReady:
		allowed[ActionEdit] = true
		allowed[ActionPTY] = true
		allowed[ActionProcess] = true
		allowed[ActionAgent] = true
		allowed[ActionCheckpoint] = true
		allowed[ActionVerify] = true
		allowed[ActionFreeze] = true
		allowed[ActionAbandon] = true
		allowed[ActionSuspend] = true
		allowed[ActionTerminate] = true
	case StateSuspended:
		allowed[ActionResume] = true
		allowed[ActionTerminate] = true
	case StateFailed:
		allowed[ActionTerminate] = true
	}

	if document.Candidate.Conflicted || document.Candidate.Stale || document.Candidate.RebaseRequired {
		delete(allowed, ActionAgent)
		delete(allowed, ActionVerify)
		delete(allowed, ActionFreeze)
	}
	if document.Candidate.Status != repository.CandidateActive {
		for _, action := range []Action{
			ActionCancel, ActionEdit, ActionPTY, ActionProcess, ActionAgent,
			ActionCheckpoint, ActionVerify, ActionFreeze, ActionAbandon, ActionSuspend, ActionResume,
		} {
			delete(allowed, action)
		}
	}
	if document.Candidate.Status == repository.CandidateActive && !hasExactCheckpoint(document) {
		delete(allowed, ActionVerify)
		delete(allowed, ActionFreeze)
		if document.Candidate.Dirty {
			delete(allowed, ActionAbandon)
		}
	}
	if document.Candidate.Status == repository.CandidateActive &&
		document.Candidate.Dirty && !hasExactCheckpoint(document) {
		delete(allowed, ActionCancel)
		delete(allowed, ActionSuspend)
		delete(allowed, ActionTerminate)
	}

	result := make([]Action, 0, len(allowed))
	for _, action := range actionOrder {
		if allowed[action] {
			result = append(result, action)
		}
	}
	return result
}

func deriveBlockingReasons(document sandboxSessionDocument) []BlockingReason {
	reasons := make([]BlockingReason, 0, 6)
	readyOnly := []Action{
		ActionEdit, ActionPTY, ActionProcess, ActionAgent, ActionCheckpoint,
		ActionVerify, ActionFreeze, ActionAbandon, ActionSuspend,
	}
	switch document.State {
	case StateProvisioning, StateStarting, StateSuspending, StateResuming, StateTerminating:
		reasons = append(reasons, BlockingReason{
			Code: BlockingSessionTransitioning, Actions: readyOnly,
			Detail: "the sandbox lifecycle transition must complete before runtime or candidate actions are available",
		})
	case StateSuspended:
		reasons = append(reasons, BlockingReason{
			Code: BlockingSessionSuspended, Actions: readyOnly,
			Detail: "the sandbox must resume into a new session epoch before runtime or candidate actions are available",
		})
	case StateFailed:
		reasons = append(reasons, BlockingReason{
			Code: BlockingSessionFailed, Actions: readyOnly,
			Detail: "a failed sandbox cannot continue using its failed runtime",
		})
	case StateTerminated:
		reasons = append(reasons, BlockingReason{
			Code: BlockingSessionTerminated,
			Actions: append(
				append([]Action(nil), readyOnly...),
				ActionResume, ActionTerminate, ActionCancel,
			),
			Detail: "a terminated sandbox is read-only and exposes only its current persisted view",
		})
	}

	if document.Candidate.Conflicted {
		reasons = append(reasons, BlockingReason{
			Code:    BlockingCandidateConflicted,
			Actions: []Action{ActionAgent, ActionVerify, ActionFreeze},
			Detail:  "candidate conflicts require explicit resolution before Agent, verification, or freeze",
		})
	}
	if document.Candidate.Stale {
		reasons = append(reasons, BlockingReason{
			Code:    BlockingCandidateStale,
			Actions: []Action{ActionAgent, ActionVerify, ActionFreeze},
			Detail:  "the candidate is stale against the canonical upstream revision",
		})
	}
	if document.Candidate.RebaseRequired {
		reasons = append(reasons, BlockingReason{
			Code:    BlockingCandidateRebase,
			Actions: []Action{ActionAgent, ActionVerify, ActionFreeze},
			Detail:  "the candidate requires an explicit rebase before Agent, verification, or freeze",
		})
	}
	if document.Candidate.Status == repository.CandidateFrozen {
		reasons = append(reasons, BlockingReason{
			Code: BlockingCandidateFrozen,
			Actions: []Action{
				ActionCancel, ActionEdit, ActionPTY, ActionProcess, ActionAgent,
				ActionCheckpoint, ActionVerify, ActionFreeze, ActionSuspend, ActionResume,
			},
			Detail: "the Candidate is frozen into an immutable implementation Proposal and is now read-only",
		})
	}
	if document.Candidate.Status == repository.CandidateAbandoned {
		reasons = append(reasons, BlockingReason{
			Code: BlockingCandidateAbandoned,
			Actions: []Action{
				ActionCancel, ActionEdit, ActionPTY, ActionProcess, ActionAgent,
				ActionCheckpoint, ActionVerify, ActionFreeze, ActionAbandon,
				ActionSuspend, ActionResume, ActionTerminate,
			},
			Detail: "the Candidate was explicitly abandoned and its SandboxSession is terminal",
		})
	}
	if document.Candidate.Status == repository.CandidateActive && !hasExactCheckpoint(document) {
		actions := []Action{ActionVerify, ActionFreeze}
		if document.Candidate.Dirty {
			actions = append(actions, ActionAbandon)
			switch document.State {
			case StateProvisioning, StateStarting, StateResuming:
				actions = append(actions, ActionCancel)
			case StateReady:
				actions = append(actions, ActionSuspend, ActionTerminate)
			case StateSuspended, StateFailed:
				actions = append(actions, ActionTerminate)
			}
		}
		reasons = append(reasons, BlockingReason{
			Code: BlockingExactCheckpointNeeded, Actions: actions,
			Detail: "an exact current Candidate checkpoint is required before verification, freeze, or a protected lifecycle transition",
		})
	}

	sort.SliceStable(reasons, func(left, right int) bool {
		return reasons[left].Code < reasons[right].Code
	})
	for index := range reasons {
		sort.SliceStable(reasons[index].Actions, func(left, right int) bool {
			return reasons[index].Actions[left] < reasons[index].Actions[right]
		})
	}
	return reasons
}

func hasExactCheckpoint(document sandboxSessionDocument) bool {
	checkpoint := document.LatestCheckpoint
	return checkpoint != nil && checkpoint.CandidateID == document.Candidate.ID &&
		checkpoint.CandidateVersion == document.Candidate.Version &&
		checkpoint.JournalSequence == document.Candidate.JournalSequence &&
		checkpoint.SessionEpoch == document.Candidate.SessionEpoch &&
		checkpoint.WriterLeaseEpoch == document.Candidate.WriterLeaseEpoch &&
		checkpoint.TreeHash == document.Candidate.TreeHash
}

func knownAction(action Action) bool {
	for _, candidate := range actionOrder {
		if candidate == action {
			return true
		}
	}
	return false
}

func actionAllowed(actions []Action, target Action) bool {
	for _, action := range actions {
		if action == target {
			return true
		}
	}
	return false
}
