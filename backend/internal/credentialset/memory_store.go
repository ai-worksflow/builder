package credentialset

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"
)

// MemoryStore is a thread-safe append-only implementation for unit tests and
// local composition. Production must provide a durable Store; this type is not
// presented as a production credential ledger.
type MemoryStore struct {
	mu     sync.RWMutex
	events map[string][]Event
	clock  Clock
}

func NewMemoryStore(clocks ...Clock) *MemoryStore {
	clock := Clock(systemClock{})
	if len(clocks) == 1 && !isNilInterface(clocks[0]) {
		clock = clocks[0]
	}
	return &MemoryStore{events: make(map[string][]Event), clock: clock}
}

func (store *MemoryStore) useLocalClock(clock Clock) {
	if store == nil || isNilInterface(clock) {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.clock = clock
}

func (store *MemoryStore) TrustedTime(ctx context.Context) (time.Time, error) {
	if isNilInterface(ctx) || store == nil || isNilInterface(store.clock) {
		return time.Time{}, fmt.Errorf("%w: store clock or context is nil", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	now := store.clock.Now()
	if now.IsZero() {
		return time.Time{}, fmt.Errorf("%w: trusted clock returned zero", ErrInvalid)
	}
	return now.UTC().Truncate(time.Millisecond), nil
}

func (store *MemoryStore) CreateIssue(ctx context.Context, setID string, event Event) (Snapshot, bool, error) {
	if isNilInterface(ctx) {
		return Snapshot{}, false, fmt.Errorf("%w: context is nil", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, false, err
	}
	if store == nil || !validUUIDv4(setID) || event.Kind != EventIssueReserved {
		return Snapshot{}, false, fmt.Errorf("%w: issue reservation is invalid", ErrInvalid)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existingSetID, existingEvent, exists := store.eventByID(event.EventID); exists {
		if existingSetID != setID || !reflect.DeepEqual(existingEvent, event) {
			return Snapshot{}, false, ErrIdempotencyConflict
		}
		snapshot, err := reduceEvents(setID, store.events[setID])
		return cloneSnapshot(snapshot), false, err
	}
	if existing := store.events[setID]; len(existing) > 0 {
		snapshot, err := reduceEvents(setID, existing)
		if err != nil {
			return Snapshot{}, false, err
		}
		if snapshot.IssueOperationID != event.OperationID || snapshot.IssueCommandHash != event.IssueCommandHash ||
			snapshot.IssuedAt != event.IssuedAt || snapshot.ExpiresAt != event.ExpiresAt {
			return Snapshot{}, false, ErrIdempotencyConflict
		}
		return cloneSnapshot(snapshot), false, nil
	}
	if store.operationReserved(event.OperationID) {
		return Snapshot{}, false, ErrIdempotencyConflict
	}
	if _, err := applyEvent(Snapshot{SetID: setID}, event); err != nil {
		return Snapshot{}, false, err
	}
	store.events[setID] = []Event{cloneEvent(event)}
	snapshot, err := reduceEvents(setID, store.events[setID])
	return cloneSnapshot(snapshot), true, err
}

func (store *MemoryStore) Load(ctx context.Context, setID string) (Snapshot, error) {
	if isNilInterface(ctx) {
		return Snapshot{}, fmt.Errorf("%w: context is nil", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if store == nil || !validUUIDv4(setID) {
		return Snapshot{}, fmt.Errorf("%w: set id is invalid", ErrInvalid)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	events := store.events[setID]
	if len(events) == 0 {
		return Snapshot{}, ErrNotFound
	}
	snapshot, err := reduceEvents(setID, events)
	return cloneSnapshot(snapshot), err
}

func (store *MemoryStore) Append(ctx context.Context, setID string, expectedVersion uint64, event Event) (Snapshot, error) {
	if isNilInterface(ctx) {
		return Snapshot{}, fmt.Errorf("%w: context is nil", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if store == nil || !validUUIDv4(setID) {
		return Snapshot{}, fmt.Errorf("%w: set id is invalid", ErrInvalid)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existingSetID, existingEvent, exists := store.eventByID(event.EventID); exists {
		if existingSetID != setID || !reflect.DeepEqual(existingEvent, event) {
			return Snapshot{}, ErrIdempotencyConflict
		}
		snapshot, err := reduceEvents(setID, store.events[setID])
		return cloneSnapshot(snapshot), err
	}
	events := store.events[setID]
	if len(events) == 0 {
		return Snapshot{}, ErrNotFound
	}
	snapshot, err := reduceEvents(setID, events)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot.Version != expectedVersion {
		return cloneSnapshot(snapshot), ErrCASConflict
	}
	if event.Kind == EventRevocationReserved && store.operationReserved(event.OperationID) {
		return Snapshot{}, ErrIdempotencyConflict
	}
	if _, err := applyEvent(snapshot, event); err != nil {
		return Snapshot{}, err
	}
	store.events[setID] = append(events, cloneEvent(event))
	updated, err := reduceEvents(setID, store.events[setID])
	return cloneSnapshot(updated), err
}

func (store *MemoryStore) eventByID(eventID string) (string, Event, bool) {
	for setID, events := range store.events {
		for _, event := range events {
			if event.EventID == eventID {
				return setID, event, true
			}
		}
	}
	return "", Event{}, false
}

func (store *MemoryStore) operationReserved(operationID string) bool {
	for _, events := range store.events {
		for _, event := range events {
			if (event.Kind == EventIssueReserved || event.Kind == EventRevocationReserved) &&
				event.OperationID == operationID {
				return true
			}
		}
	}
	return false
}

func (store *MemoryStore) Events(ctx context.Context, setID string) ([]Event, error) {
	if isNilInterface(ctx) {
		return nil, fmt.Errorf("%w: context is nil", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store == nil || !validUUIDv4(setID) {
		return nil, fmt.Errorf("%w: set id is invalid", ErrInvalid)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	events := store.events[setID]
	if len(events) == 0 {
		return nil, ErrNotFound
	}
	result := make([]Event, len(events))
	for index, event := range events {
		result[index] = cloneEvent(event)
	}
	return result, nil
}

func reduceEvents(setID string, events []Event) (Snapshot, error) {
	snapshot := Snapshot{SetID: setID}
	seenEventIDs := make(map[string]struct{}, len(events))
	for _, event := range events {
		if _, duplicate := seenEventIDs[event.EventID]; duplicate {
			return Snapshot{}, fmt.Errorf("%w: event id is duplicated", ErrInvalidTransition)
		}
		seenEventIDs[event.EventID] = struct{}{}
		updated, err := applyEvent(snapshot, event)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot = updated
	}
	return snapshot, nil
}

func applyEvent(snapshot Snapshot, event Event) (Snapshot, error) {
	if !validUUIDv4(event.EventID) || event.At.IsZero() || event.At.Location() != time.UTC ||
		!event.At.Equal(event.At.UTC().Truncate(time.Millisecond)) ||
		(snapshot.Version > 0 && event.At.Before(snapshot.LastEventAt)) {
		return Snapshot{}, fmt.Errorf("%w: event time must be canonical millisecond UTC", ErrInvalidTransition)
	}
	updated := cloneSnapshot(snapshot)
	switch event.Kind {
	case EventIssueReserved:
		if snapshot.Version != 0 || snapshot.Phase != "" || !validUUIDv4(event.OperationID) || !validDigest(event.IssueCommandHash) ||
			event.Binding != nil || event.Attestation != nil || event.RevocationCommandHash != "" || event.RevokedAt != "" {
			return Snapshot{}, ErrInvalidTransition
		}
		issuedAt, issueErr := parseCanonicalTime(event.IssuedAt)
		expiresAt, expiryErr := parseCanonicalTime(event.ExpiresAt)
		if issueErr != nil || expiryErr != nil || !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > MaximumLifetime {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.IssueOperationID = event.OperationID
		updated.IssueCommandHash = event.IssueCommandHash
		updated.IssuedAt = event.IssuedAt
		updated.ExpiresAt = event.ExpiresAt
		updated.Phase = PhaseIssueReserved
	case EventPrepareStarted:
		if snapshot.Phase != PhaseIssueReserved || !eventOperationOnly(event, snapshot.IssueOperationID) || eventHasIssueWindow(event) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhasePrepareStarted
	case EventPrepared:
		if snapshot.Phase != PhasePrepareStarted || !eventHasOperation(event, snapshot.IssueOperationID) || event.Attestation != nil || eventHasIssueWindow(event) ||
			event.RevokedAt != "" || event.Binding == nil || ValidateBinding(*event.Binding) != nil || event.Binding.SetID != snapshot.SetID {
			return Snapshot{}, ErrInvalidTransition
		}
		if event.Binding.IssuedAt != snapshot.IssuedAt || event.Binding.ExpiresAt != snapshot.ExpiresAt {
			return Snapshot{}, ErrInvalidTransition
		}
		binding := cloneBinding(*event.Binding)
		updated.Binding = &binding
		updated.Phase = PhasePrepared
	case EventActivationStarted:
		if snapshot.Phase != PhasePrepared || !eventOperationOnly(event, snapshot.IssueOperationID) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseActivationStarted
	case EventActivated:
		if snapshot.Phase != PhaseActivationStarted || !eventHasOperation(event, snapshot.IssueOperationID) || event.Attestation != nil ||
			event.RevokedAt != "" || event.Binding == nil || snapshot.Binding == nil || !equalBinding(*snapshot.Binding, *event.Binding) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseActivated
	case EventIssuanceSignStarted:
		if snapshot.Phase != PhaseActivated || !eventOperationOnly(event, signingOperationID(snapshot.IssueOperationID, "attestation")) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseIssuanceSignStarted
	case EventIssued:
		if snapshot.Phase != PhaseIssuanceSignStarted || !eventHasOperation(event, signingOperationID(snapshot.IssueOperationID, "attestation")) ||
			event.Binding != nil || event.RevokedAt != "" || event.Attestation == nil || !validAttestation(*event.Attestation) {
			return Snapshot{}, ErrInvalidTransition
		}
		attestation := cloneAttestation(*event.Attestation)
		updated.IssueAttestation = &attestation
		updated.Phase = PhaseIssued
	case EventRevocationReserved:
		if snapshot.Phase != PhaseIssued || !validUUIDv4(event.OperationID) || !validDigest(event.RevocationCommandHash) ||
			event.IssueCommandHash != "" || event.RevokedAt == "" || event.Binding != nil || event.Attestation != nil {
			return Snapshot{}, ErrInvalidTransition
		}
		issued, _ := parseCanonicalTime(snapshot.Binding.IssuedAt)
		expires, _ := parseCanonicalTime(snapshot.Binding.ExpiresAt)
		revoked, err := parseCanonicalTime(event.RevokedAt)
		if err != nil || !revoked.After(issued) || !revoked.Before(expires) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.RevocationOperationID = event.OperationID
		updated.RevocationCommandHash = event.RevocationCommandHash
		updated.RevokedAt = event.RevokedAt
		updated.Phase = PhaseRevocationReserved
	case EventRevocationStarted:
		if snapshot.Phase != PhaseRevocationReserved || !eventOperationOnly(event, snapshot.RevocationOperationID) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseRevocationStarted
	case EventRevoked:
		if snapshot.Phase != PhaseRevocationStarted || !eventHasOperation(event, snapshot.RevocationOperationID) || event.Attestation != nil ||
			event.Binding == nil || snapshot.Binding == nil || !equalBinding(*snapshot.Binding, *event.Binding) || event.RevokedAt != snapshot.RevokedAt {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseRevoked
	case EventRevocationSignStarted:
		if snapshot.Phase != PhaseRevoked || !eventOperationOnly(event, signingOperationID(snapshot.RevocationOperationID, "attestation")) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseRevocationSignStarted
	case EventRevocationAttested:
		if snapshot.Phase != PhaseRevocationSignStarted || !eventHasOperation(event, signingOperationID(snapshot.RevocationOperationID, "attestation")) ||
			event.Binding != nil || event.RevokedAt != "" || event.Attestation == nil || !validAttestation(*event.Attestation) {
			return Snapshot{}, ErrInvalidTransition
		}
		attestation := cloneAttestation(*event.Attestation)
		updated.RevocationAttestation = &attestation
		updated.Phase = PhaseComplete
	case EventIssueFailed:
		if (snapshot.Phase != PhasePrepareStarted && snapshot.Phase != PhaseActivationStarted) ||
			!eventOperationOnly(event, snapshot.IssueOperationID) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseIssueFailed
	case EventRevocationFailed:
		if snapshot.Phase != PhaseRevocationStarted || !eventOperationOnly(event, snapshot.RevocationOperationID) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseRevocationFailed
	default:
		return Snapshot{}, ErrInvalidTransition
	}
	updated.LastEventAt = event.At
	updated.LastEventID = event.EventID
	updated.Version++
	return updated, nil
}

func eventHasOperation(event Event, operationID string) bool {
	return event.OperationID == operationID && event.IssueCommandHash == "" && event.RevocationCommandHash == ""
}

func eventHasIssueWindow(event Event) bool {
	return event.IssuedAt != "" || event.ExpiresAt != ""
}

func eventOperationOnly(event Event, operationID string) bool {
	return eventHasOperation(event, operationID) && event.Binding == nil && event.Attestation == nil &&
		event.RevokedAt == "" && !eventHasIssueWindow(event)
}

func cloneBinding(binding SetBinding) SetBinding {
	binding.Members = cloneMembers(binding.Members)
	return binding
}

func cloneAttestation(attestation Attestation) Attestation {
	attestation.Payload = append([]byte(nil), attestation.Payload...)
	attestation.Envelope = append([]byte(nil), attestation.Envelope...)
	return attestation
}

func cloneEvent(event Event) Event {
	if event.Binding != nil {
		binding := cloneBinding(*event.Binding)
		event.Binding = &binding
	}
	if event.Attestation != nil {
		attestation := cloneAttestation(*event.Attestation)
		event.Attestation = &attestation
	}
	return event
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	if snapshot.Binding != nil {
		binding := cloneBinding(*snapshot.Binding)
		snapshot.Binding = &binding
	}
	if snapshot.IssueAttestation != nil {
		attestation := cloneAttestation(*snapshot.IssueAttestation)
		snapshot.IssueAttestation = &attestation
	}
	if snapshot.RevocationAttestation != nil {
		attestation := cloneAttestation(*snapshot.RevocationAttestation)
		snapshot.RevocationAttestation = &attestation
	}
	return snapshot
}
