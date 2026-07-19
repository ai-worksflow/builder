package repository

import (
	"fmt"
	"strings"
	"time"
)

type CandidateControlKind string

const (
	ControlLeaseAcquired  CandidateControlKind = "lease.acquired"
	ControlSessionRotated CandidateControlKind = "session.rotated"
	ControlFlagsUpdated   CandidateControlKind = "candidate.flags_updated"
	ControlFrozen         CandidateControlKind = "candidate.frozen"
	ControlAbandoned      CandidateControlKind = "candidate.abandoned"
)

type CandidateFlags struct {
	Conflicted     bool `json:"conflicted"`
	Stale          bool `json:"stale"`
	RebaseRequired bool `json:"rebaseRequired"`
}

type CandidateCheckpointIdentity struct {
	ID               string `json:"id"`
	CandidateID      string `json:"candidateId"`
	CandidateVersion uint64 `json:"candidateVersion"`
	JournalSequence  uint64 `json:"journalSequence"`
	SessionEpoch     uint64 `json:"sessionEpoch"`
	WriterLeaseEpoch uint64 `json:"writerLeaseEpoch"`
	TreeHash         string `json:"treeHash"`
}

type CandidateControlEvent struct {
	CandidateID      string                       `json:"candidateId"`
	Kind             CandidateControlKind         `json:"kind"`
	CandidateFrom    uint64                       `json:"candidateVersionFrom"`
	CandidateTo      uint64                       `json:"candidateVersionTo"`
	SessionEpochFrom uint64                       `json:"sessionEpochFrom"`
	SessionEpochTo   uint64                       `json:"sessionEpochTo"`
	WriterLeaseFrom  uint64                       `json:"writerLeaseEpochFrom"`
	WriterLeaseTo    uint64                       `json:"writerLeaseEpochTo"`
	StatusFrom       CandidateStatus              `json:"statusFrom"`
	StatusTo         CandidateStatus              `json:"statusTo"`
	FlagsFrom        CandidateFlags               `json:"flagsFrom"`
	FlagsTo          CandidateFlags               `json:"flagsTo"`
	WriterLease      *WriterLease                 `json:"writerLease,omitempty"`
	Checkpoint       *CandidateCheckpointIdentity `json:"checkpoint,omitempty"`
	ActorID          string                       `json:"actorId"`
	Reason           string                       `json:"reason"`
	CreatedAt        time.Time                    `json:"createdAt"`
}

func (candidate CandidateWorkspace) RotateSession(
	expectedVersion, expectedSessionEpoch uint64,
	actorID, reason string,
	now time.Time,
) (CandidateWorkspace, CandidateControlEvent, error) {
	if err := candidate.controlGuard(expectedVersion, expectedSessionEpoch, actorID, reason, now); err != nil {
		return CandidateWorkspace{}, CandidateControlEvent{}, err
	}
	next := cloneCandidate(candidate)
	next.SessionEpoch++
	next.WriterLeaseEpoch++
	next.Lease = nil
	next.Version++
	next.UpdatedAt = now.UTC()
	event := candidateControlEvent(candidate, next, ControlSessionRotated, actorID, reason, nil, now)
	return next, event, nil
}

func (candidate CandidateWorkspace) UpdateFlags(
	expectedVersion, expectedSessionEpoch uint64,
	actorID, reason string,
	flags CandidateFlags,
	now time.Time,
) (CandidateWorkspace, CandidateControlEvent, error) {
	if err := candidate.controlGuard(expectedVersion, expectedSessionEpoch, actorID, reason, now); err != nil {
		return CandidateWorkspace{}, CandidateControlEvent{}, err
	}
	current := candidate.flags()
	if current == flags {
		return CandidateWorkspace{}, CandidateControlEvent{}, fmt.Errorf("%w: flags are unchanged", ErrCandidateState)
	}
	if current.Stale && !flags.Stale || current.RebaseRequired && !flags.RebaseRequired || flags.Stale != flags.RebaseRequired {
		return CandidateWorkspace{}, CandidateControlEvent{}, fmt.Errorf("%w: stale/rebase flags require a replacement Candidate", ErrCandidateState)
	}
	next := cloneCandidate(candidate)
	next.Conflicted = flags.Conflicted
	next.Stale = flags.Stale
	next.RebaseRequired = flags.RebaseRequired
	next.WriterLeaseEpoch++
	next.Lease = nil
	next.Version++
	next.UpdatedAt = now.UTC()
	event := candidateControlEvent(candidate, next, ControlFlagsUpdated, actorID, reason, nil, now)
	return next, event, nil
}

func (candidate CandidateWorkspace) Freeze(
	expectedVersion, expectedSessionEpoch uint64,
	actorID, reason string,
	checkpoint CandidateSnapshot,
	now time.Time,
) (CandidateWorkspace, CandidateControlEvent, error) {
	if err := candidate.controlGuard(expectedVersion, expectedSessionEpoch, actorID, reason, now); err != nil {
		return CandidateWorkspace{}, CandidateControlEvent{}, err
	}
	if candidate.Conflicted || candidate.Stale || candidate.RebaseRequired {
		return CandidateWorkspace{}, CandidateControlEvent{}, fmt.Errorf("%w: conflicted or stale Candidate cannot freeze", ErrCandidateState)
	}
	identity, err := candidate.exactCheckpoint(checkpoint, now)
	if err != nil {
		return CandidateWorkspace{}, CandidateControlEvent{}, err
	}
	next := candidate.terminal(CandidateFrozen, now)
	event := candidateControlEvent(candidate, next, ControlFrozen, actorID, reason, &identity, now)
	return next, event, nil
}

func (candidate CandidateWorkspace) Abandon(
	expectedVersion, expectedSessionEpoch uint64,
	actorID, reason string,
	checkpoint *CandidateSnapshot,
	now time.Time,
) (CandidateWorkspace, CandidateControlEvent, error) {
	if err := candidate.controlGuard(expectedVersion, expectedSessionEpoch, actorID, reason, now); err != nil {
		return CandidateWorkspace{}, CandidateControlEvent{}, err
	}
	var identity *CandidateCheckpointIdentity
	if checkpoint != nil {
		value, err := candidate.exactCheckpoint(*checkpoint, now)
		if err != nil {
			return CandidateWorkspace{}, CandidateControlEvent{}, err
		}
		identity = &value
	} else if candidate.Dirty {
		return CandidateWorkspace{}, CandidateControlEvent{}, fmt.Errorf("%w: dirty Candidate must be checkpointed before abandon", ErrCandidateState)
	}
	next := candidate.terminal(CandidateAbandoned, now)
	event := candidateControlEvent(candidate, next, ControlAbandoned, actorID, reason, identity, now)
	return next, event, nil
}

func (candidate CandidateWorkspace) controlGuard(
	expectedVersion, expectedSessionEpoch uint64,
	actorID, reason string,
	now time.Time,
) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.Status != CandidateActive || candidate.Version != expectedVersion {
		return ErrCandidateState
	}
	if candidate.SessionEpoch != expectedSessionEpoch {
		return ErrLeaseFenced
	}
	reason = strings.TrimSpace(reason)
	if !validUUID(actorID) || reason == "" || len(reason) > 1000 || now.IsZero() || now.Before(candidate.UpdatedAt) {
		return fmt.Errorf("%w: invalid Candidate control transition", ErrInvalidCandidate)
	}
	return nil
}

func (candidate CandidateWorkspace) terminal(status CandidateStatus, now time.Time) CandidateWorkspace {
	next := cloneCandidate(candidate)
	next.Status = status
	next.WriterLeaseEpoch++
	next.Lease = nil
	next.Version++
	next.UpdatedAt = now.UTC()
	return next
}

func (candidate CandidateWorkspace) exactCheckpoint(snapshot CandidateSnapshot, now time.Time) (CandidateCheckpointIdentity, error) {
	if snapshot.SchemaVersion != CandidateSnapshotSchemaVersion || !validUUID(snapshot.ID) || snapshot.ProjectID != candidate.ProjectID ||
		snapshot.CandidateID != candidate.ID || snapshot.CandidateVersion != candidate.Version ||
		snapshot.JournalSequence != candidate.JournalSequence || snapshot.Tree.TreeHash != candidate.CurrentTree.TreeHash ||
		snapshot.SessionEpoch != candidate.SessionEpoch || snapshot.WriterLeaseEpoch != candidate.WriterLeaseEpoch ||
		!validUUID(snapshot.CreatedBy) || snapshot.CreatedAt.IsZero() || snapshot.CreatedAt.Before(candidate.UpdatedAt) ||
		snapshot.CreatedAt.After(now) {
		return CandidateCheckpointIdentity{}, fmt.Errorf("%w: checkpoint does not bind the exact Candidate", ErrCandidateState)
	}
	if _, err := ParseTree(snapshot.Tree); err != nil {
		return CandidateCheckpointIdentity{}, fmt.Errorf("%w: checkpoint tree: %v", ErrInvalidCandidate, err)
	}
	return CandidateCheckpointIdentity{
		ID: snapshot.ID, CandidateID: snapshot.CandidateID, CandidateVersion: snapshot.CandidateVersion,
		JournalSequence: snapshot.JournalSequence, SessionEpoch: snapshot.SessionEpoch,
		WriterLeaseEpoch: snapshot.WriterLeaseEpoch, TreeHash: snapshot.Tree.TreeHash,
	}, nil
}

func (candidate CandidateWorkspace) flags() CandidateFlags {
	return CandidateFlags{
		Conflicted: candidate.Conflicted, Stale: candidate.Stale, RebaseRequired: candidate.RebaseRequired,
	}
}

func candidateControlEvent(
	before, after CandidateWorkspace,
	kind CandidateControlKind,
	actorID, reason string,
	checkpoint *CandidateCheckpointIdentity,
	now time.Time,
) CandidateControlEvent {
	event := CandidateControlEvent{
		CandidateID: before.ID, Kind: kind, CandidateFrom: before.Version, CandidateTo: after.Version,
		SessionEpochFrom: before.SessionEpoch, SessionEpochTo: after.SessionEpoch,
		WriterLeaseFrom: before.WriterLeaseEpoch, WriterLeaseTo: after.WriterLeaseEpoch,
		StatusFrom: before.Status, StatusTo: after.Status, FlagsFrom: before.flags(), FlagsTo: after.flags(),
		Checkpoint: checkpoint, ActorID: strings.TrimSpace(actorID), Reason: strings.TrimSpace(reason), CreatedAt: now.UTC(),
	}
	if after.Lease != nil {
		lease := *after.Lease
		event.WriterLease = &lease
	}
	return event
}
