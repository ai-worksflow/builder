package qualificationreceiptv3

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

type controlAttemptKey struct {
	authority uuid.UUID
	operation uuid.UUID
	kind      RequestKind
}

// MemoryControlStore is the concurrent semantic reference implementation. It
// is intentionally not advertised as production persistence.
type MemoryControlStore struct {
	mu    sync.Mutex
	clock func() time.Time

	attempts            map[controlAttemptKey][]RequestRecord
	requests            map[RequestKey]RequestRecord
	requestsByHash      map[string]RequestRecord
	operationOwner      map[uuid.UUID]uuid.UUID
	observations        map[string][]ObservationRecord
	observationsByHash  map[string]ObservationRecord
	claimOwners         map[string]ObservationRecord
	ackOwners           map[string]ObservationRecord
	completions         map[uuid.UUID]CompletionRecord
	completionByReceipt map[string]uuid.UUID

	unknownStartOnce       bool
	unknownObservationOnce bool
	unknownCompletionOnce  bool
}

func NewMemoryControlStore(clocks ...func() time.Time) *MemoryControlStore {
	clock := time.Now
	if len(clocks) > 0 && clocks[0] != nil {
		clock = clocks[0]
	}
	return &MemoryControlStore{
		clock: clock, attempts: make(map[controlAttemptKey][]RequestRecord), requests: make(map[RequestKey]RequestRecord),
		requestsByHash: make(map[string]RequestRecord), operationOwner: make(map[uuid.UUID]uuid.UUID),
		observations: make(map[string][]ObservationRecord), observationsByHash: make(map[string]ObservationRecord),
		claimOwners: make(map[string]ObservationRecord), ackOwners: make(map[string]ObservationRecord),
		completions: make(map[uuid.UUID]CompletionRecord), completionByReceipt: make(map[string]uuid.UUID),
	}
}

func (store *MemoryControlStore) StartBatch(ctx context.Context, candidates []RequestRecord) (StoreStartOutcome, error) {
	if store == nil || isNilInterface(ctx) || len(candidates) == 0 {
		return StoreStartOutcome{}, ErrControlInvalid
	}
	if err := ctx.Err(); err != nil {
		return StoreStartOutcome{}, err
	}
	lookup := ControlLookup{AuthorityID: candidates[0].Key.AuthorityID, OperationID: candidates[0].Key.OperationID, Kind: candidates[0].Key.Kind}
	if err := validateAttemptRecords(candidates, lookup, false); err != nil {
		return StoreStartOutcome{}, err
	}
	key := controlAttemptKey{lookup.AuthorityID, lookup.OperationID, lookup.Kind}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.attempts[key]; ok {
		if !sameRequestBatch(existing, candidates, false) {
			return StoreStartOutcome{}, fmt.Errorf("%w: attempt is bound to different request bytes", ErrControlConflict)
		}
		markRequestReplay(existing)
		return StoreStartOutcome{Requests: cloneRequestRecords(existing), Created: false}, nil
	}
	if owner, ok := store.operationOwner[lookup.OperationID]; ok && owner != lookup.AuthorityID {
		return StoreStartOutcome{}, fmt.Errorf("%w: operation belongs to another Plan Authority", ErrControlConflict)
	}
	for _, candidate := range candidates {
		if _, exists := store.requests[candidate.Key]; exists {
			return StoreStartOutcome{}, fmt.Errorf("%w: request key is already reserved", ErrControlConflict)
		}
		if existing, exists := store.requestsByHash[candidate.RequestHash]; exists && existing.Key != candidate.Key {
			return StoreStartOutcome{}, fmt.Errorf("%w: request hash is already reserved", ErrControlConflict)
		}
	}
	now := store.clock().UTC()
	if !validControlTime(now) {
		return StoreStartOutcome{}, fmt.Errorf("%w: store clock must return UTC millisecond precision", ErrControlInvalid)
	}
	if err := store.validateBatchPrerequisitesLocked(lookup, candidates, now); err != nil {
		return StoreStartOutcome{}, err
	}
	stored := cloneRequestRecords(candidates)
	for index := range stored {
		stored[index].StartedAt = now
		stored[index].Idempotent = false
		store.requests[stored[index].Key] = cloneRequestRecord(stored[index])
		store.requestsByHash[stored[index].RequestHash] = cloneRequestRecord(stored[index])
	}
	store.attempts[key] = cloneRequestRecords(stored)
	store.operationOwner[lookup.OperationID] = lookup.AuthorityID
	if store.unknownStartOnce {
		store.unknownStartOnce = false
		return StoreStartOutcome{}, ErrControlStoreOutcomeUnknown
	}
	return StoreStartOutcome{Requests: cloneRequestRecords(stored), Created: true}, nil
}

func (store *MemoryControlStore) validateBatchPrerequisitesLocked(lookup ControlLookup, candidates []RequestRecord, startedAt time.Time) error {
	if lookup.Kind == RequestKindSnapshotSeal {
		return nil
	}
	if lookup.Kind == RequestKindSnapshotVerify {
		seal := store.attempts[controlAttemptKey{lookup.AuthorityID, lookup.OperationID, RequestKindSnapshotSeal}]
		if len(seal) != 1 || !store.requestCommittedLocked(seal[0]) {
			return fmt.Errorf("%w: snapshot seal is not committed", ErrControlNotReady)
		}
		terminal := store.lastObservationLocked(seal[0].RequestHash)
		if startedAt.Before(terminal.RecordedAt) {
			return fmt.Errorf("%w: verification start time precedes snapshot-seal commit", ErrControlInvalid)
		}
		snapshot, err := decodeSnapshotResult(terminal)
		if err != nil || !sameBaseAnchors(seal[0].Request, candidates[0].Request) || candidates[0].Request.SnapshotDigest != snapshot.SnapshotDigest {
			return fmt.Errorf("%w: verification request does not bind exact seal result", ErrControlConflict)
		}
		return nil
	}
	if lookup.Kind == RequestKindReceiptSign {
		receipt, err := DecodePayload(candidates[0].Payload)
		if err != nil {
			return ErrControlInvalid
		}
		snapshotOperation, err := uuid.Parse(receipt.Snapshot.OperationID)
		if err != nil {
			return ErrControlInvalid
		}
		seal := store.attempts[controlAttemptKey{lookup.AuthorityID, snapshotOperation, RequestKindSnapshotSeal}]
		verification := store.attempts[controlAttemptKey{lookup.AuthorityID, snapshotOperation, RequestKindSnapshotVerify}]
		if len(seal) != 1 || len(verification) != 1 || !store.requestCommittedLocked(seal[0]) || !store.requestCommittedLocked(verification[0]) {
			return fmt.Errorf("%w: snapshot seal/verification are not committed", ErrControlNotReady)
		}
		sealTerminal := store.lastObservationLocked(seal[0].RequestHash)
		verificationTerminal := store.lastObservationLocked(verification[0].RequestHash)
		if startedAt.Before(sealTerminal.RecordedAt) || startedAt.Before(verificationTerminal.RecordedAt) {
			return fmt.Errorf("%w: signing start time precedes a snapshot prerequisite commit", ErrControlInvalid)
		}
		if !sameBaseAnchors(seal[0].Request, candidates[0].Request) || !sameBaseAnchors(verification[0].Request, candidates[0].Request) ||
			!operationalAuthoritiesMatchReceipt(seal[0], verification[0], candidates, receipt) ||
			resultsMatchReceipt(sealTerminal, verificationTerminal, receipt) != nil {
			return fmt.Errorf("%w: signing request does not bind actual snapshot results", ErrControlConflict)
		}
		return nil
	}
	return ErrControlInvalid
}

func (store *MemoryControlStore) InspectAttempt(ctx context.Context, lookup ControlLookup) ([]RequestRecord, error) {
	if store == nil || isNilInterface(ctx) || validateControlLookup(lookup) != nil {
		return nil, ErrControlNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	records, ok := store.attempts[controlAttemptKey{lookup.AuthorityID, lookup.OperationID, lookup.Kind}]
	if !ok {
		return nil, ErrControlNotFound
	}
	return cloneRequestRecords(records), nil
}

func (store *MemoryControlStore) InspectRequest(ctx context.Context, key RequestKey) (RequestRecord, error) {
	if store == nil || isNilInterface(ctx) || validateRequestKey(key) != nil {
		return RequestRecord{}, ErrControlNotFound
	}
	if err := ctx.Err(); err != nil {
		return RequestRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.requests[key]
	if !ok {
		return RequestRecord{}, ErrControlNotFound
	}
	return cloneRequestRecord(record), nil
}

func (store *MemoryControlStore) AppendObservation(ctx context.Context, candidate ObservationRecord) (ObservationRecord, error) {
	if store == nil || isNilInterface(ctx) {
		return ObservationRecord{}, ErrControlInvalid
	}
	if err := ctx.Err(); err != nil {
		return ObservationRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	request, ok := store.requestsByHash[candidate.RequestHash]
	if !ok || request.Key != candidate.RequestKey {
		return ObservationRecord{}, ErrControlNotFound
	}
	if err := validateObservationRecord(candidate, request, false); err != nil {
		return ObservationRecord{}, err
	}
	rows := store.observations[candidate.RequestHash]
	for _, existing := range rows {
		if existing.Sequence == candidate.Sequence {
			if !sameObservation(existing, candidate, false) {
				return ObservationRecord{}, fmt.Errorf("%w: observation sequence has different immutable bytes", ErrControlConflict)
			}
			existing.Idempotent = true
			return cloneObservation(existing), nil
		}
	}
	now := store.clock().UTC()
	if !validControlTime(now) || now.Before(request.StartedAt) || (len(rows) > 0 && now.Before(rows[len(rows)-1].RecordedAt)) {
		return ObservationRecord{}, fmt.Errorf("%w: store observation clock is invalid or regressed", ErrControlInvalid)
	}
	stored := cloneObservation(candidate)
	stored.RecordedAt = now
	stored.RecordHash = controlObservationHash(stored)
	stored.Idempotent = false
	if existing, ok := store.observationsByHash[stored.RecordHash]; ok &&
		(existing.RequestHash != stored.RequestHash || existing.Sequence != stored.Sequence) {
		return ObservationRecord{}, fmt.Errorf("%w: observation hash is already bound to another identity", ErrControlConflict)
	}
	if err := validateObservationRecord(stored, request, true); err != nil {
		return ObservationRecord{}, err
	}
	if err := validateObservationTransition(rows, stored); err != nil {
		return ObservationRecord{}, err
	}
	if stored.Status == ObservationNotInvoked {
		if existing, ok := store.claimOwners[stored.Claim.ClaimID]; ok &&
			(existing.RequestHash != stored.RequestHash || existing.Sequence != stored.Sequence) {
			return ObservationRecord{}, fmt.Errorf("%w: claim identity is globally reserved", ErrControlConflict)
		}
		if existing, ok := store.ackOwners[stored.Acknowledgement.AcknowledgementID]; ok &&
			(existing.RequestHash != stored.RequestHash || existing.Sequence != stored.Sequence) {
			return ObservationRecord{}, fmt.Errorf("%w: ACK identity is globally reserved", ErrControlConflict)
		}
	}
	store.observations[candidate.RequestHash] = append(cloneObservations(rows), cloneObservation(stored))
	store.observationsByHash[stored.RecordHash] = cloneObservation(stored)
	if stored.Status == ObservationNotInvoked {
		store.claimOwners[stored.Claim.ClaimID] = cloneObservation(stored)
		store.ackOwners[stored.Acknowledgement.AcknowledgementID] = cloneObservation(stored)
	}
	if store.unknownObservationOnce {
		store.unknownObservationOnce = false
		return ObservationRecord{}, ErrControlStoreOutcomeUnknown
	}
	return cloneObservation(stored), nil
}

func validateObservationTransition(rows []ObservationRecord, candidate ObservationRecord) error {
	if len(rows) == 0 {
		if candidate.Sequence != 1 || candidate.Generation != 1 || candidate.Status != ObservationPending {
			return fmt.Errorf("%w: first observation must be pending generation 1 sequence 1", ErrControlConflict)
		}
		return nil
	}
	last := rows[len(rows)-1]
	if candidate.Sequence != last.Sequence+1 || candidate.RecordedAt.Before(last.RecordedAt) {
		return fmt.Errorf("%w: observation sequence/time is not append-only", ErrControlConflict)
	}
	switch last.Status {
	case ObservationPending:
		if candidate.Generation != last.Generation || candidate.Status == ObservationPending {
			return fmt.Errorf("%w: pending must transition to one terminal status in the same generation", ErrControlConflict)
		}
		if candidate.Status == ObservationNotInvoked && candidate.Claim.PendingEnvelopeHash != last.AuthenticationEnvelopeHash {
			return fmt.Errorf("%w: not-invoked claim does not acknowledge exact pending envelope", ErrControlConflict)
		}
	case ObservationNotInvoked:
		if candidate.Status != ObservationPending || candidate.Generation != last.Generation+1 {
			return fmt.Errorf("%w: only next-generation pending may follow authenticated not-invoked", ErrControlConflict)
		}
	case ObservationCommitted, ObservationRejected:
		return fmt.Errorf("%w: terminal request permits exact replay only", ErrControlConflict)
	default:
		return ErrControlConflict
	}
	return nil
}

func (store *MemoryControlStore) InspectObservation(ctx context.Context, requestHash string, sequence uint64) (ObservationRecord, error) {
	if store == nil || isNilInterface(ctx) || !validDigest(requestHash) || sequence == 0 {
		return ObservationRecord{}, ErrControlNotFound
	}
	if err := ctx.Err(); err != nil {
		return ObservationRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, record := range store.observations[requestHash] {
		if record.Sequence == sequence {
			return cloneObservation(record), nil
		}
	}
	return ObservationRecord{}, ErrControlNotFound
}

func (store *MemoryControlStore) InspectTerminalObservation(ctx context.Context, requestHash string) (ObservationRecord, error) {
	if store == nil || isNilInterface(ctx) || !validDigest(requestHash) {
		return ObservationRecord{}, ErrControlNotFound
	}
	if err := ctx.Err(); err != nil {
		return ObservationRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	last := store.lastObservationLocked(requestHash)
	if last.Status == ObservationPending || last.Status == "" {
		return ObservationRecord{}, ErrControlNotFound
	}
	return cloneObservation(last), nil
}

func (store *MemoryControlStore) Complete(ctx context.Context, candidate CompletionRecord) (CompletionRecord, error) {
	if store == nil || isNilInterface(ctx) {
		return CompletionRecord{}, ErrControlInvalid
	}
	if err := ctx.Err(); err != nil {
		return CompletionRecord{}, err
	}
	if err := validateCompletionCandidate(candidate); err != nil {
		return CompletionRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.completions[candidate.AuthorityID]; ok {
		if !sameCompletion(existing, candidate, false) {
			return CompletionRecord{}, fmt.Errorf("%w: Plan Authority already has another completion", ErrControlConflict)
		}
		existing.Idempotent = true
		return cloneCompletion(existing), nil
	}
	if owner, ok := store.completionByReceipt[candidate.ReceiptID]; ok && owner != candidate.AuthorityID {
		return CompletionRecord{}, fmt.Errorf("%w: Receipt identity is already bound to another Plan Authority", ErrControlConflict)
	}
	if err := store.validateCompletionSourcesLocked(candidate); err != nil {
		return CompletionRecord{}, err
	}
	now := store.clock().UTC()
	if !validControlTime(now) {
		return CompletionRecord{}, fmt.Errorf("%w: store clock must return UTC millisecond precision", ErrControlInvalid)
	}
	for _, digest := range []string{candidate.ObservationHashes.SnapshotSeal, candidate.ObservationHashes.SnapshotVerify, candidate.ObservationHashes.RunnerSign, candidate.ObservationHashes.ApproverSign} {
		observation := store.observationByHashLocked(digest)
		if !observation.RecordedAt.Before(now) {
			return CompletionRecord{}, fmt.Errorf("%w: completion time is not strictly later than every source observation", ErrControlInvalid)
		}
	}
	stored := cloneCompletion(candidate)
	stored.CompletedAt = now
	stored.Document = completionDocument(stored, now)
	stored.DocumentBytes, _ = CanonicalJSON(stored.Document)
	stored.DocumentHash = SHA256Digest(stored.DocumentBytes)
	stored.Idempotent = false
	store.completions[stored.AuthorityID] = cloneCompletion(stored)
	store.completionByReceipt[stored.ReceiptID] = stored.AuthorityID
	if store.unknownCompletionOnce {
		store.unknownCompletionOnce = false
		return CompletionRecord{}, ErrControlStoreOutcomeUnknown
	}
	return cloneCompletion(stored), nil
}

func (store *MemoryControlStore) validateCompletionSourcesLocked(candidate CompletionRecord) error {
	types := []struct {
		requestHash     string
		observationHash string
		kind            RequestKind
		role            ControlRole
	}{
		{candidate.RequestHashes.SnapshotSeal, candidate.ObservationHashes.SnapshotSeal, RequestKindSnapshotSeal, ControlRoleSealer},
		{candidate.RequestHashes.SnapshotVerify, candidate.ObservationHashes.SnapshotVerify, RequestKindSnapshotVerify, ControlRoleVerifier},
		{candidate.RequestHashes.RunnerSign, candidate.ObservationHashes.RunnerSign, RequestKindReceiptSign, ControlRoleRunner},
		{candidate.RequestHashes.ApproverSign, candidate.ObservationHashes.ApproverSign, RequestKindReceiptSign, ControlRoleReleaseApprover},
	}
	requests := make([]RequestRecord, 0, 4)
	observations := make(map[ControlRole]ObservationRecord, 4)
	for _, source := range types {
		request, ok := store.requestsByHash[source.requestHash]
		if !ok || request.Key.AuthorityID != candidate.AuthorityID || request.Key.Kind != source.kind || request.Key.Role != source.role {
			return fmt.Errorf("%w: completion request source is absent", ErrControlNotReady)
		}
		wantOperation := candidate.Operations.Snapshot
		if source.kind == RequestKindReceiptSign {
			wantOperation = candidate.Operations.ReceiptSign
		}
		if request.Key.OperationID.String() != wantOperation {
			return fmt.Errorf("%w: completion request operation drift", ErrControlConflict)
		}
		terminal := store.lastObservationLocked(source.requestHash)
		if terminal.Status != ObservationCommitted || terminal.RecordHash != source.observationHash {
			return fmt.Errorf("%w: completion observation source is not exact current commit", ErrControlNotReady)
		}
		if validateRequestRecord(request, true) != nil || validateObservationRecord(terminal, request, true) != nil {
			return fmt.Errorf("%w: completion source closure is invalid", ErrControlConflict)
		}
		requests = append(requests, request)
		observations[source.role] = terminal
	}
	if !sameAnchorClosure(requests) {
		return fmt.Errorf("%w: completion source anchors drift", ErrControlConflict)
	}
	var signing []RequestRecord
	for _, request := range requests {
		if request.Key.Kind == RequestKindReceiptSign {
			signing = append(signing, request)
		}
	}
	if !samePayloadClosure(signing) || !bytes.Equal(signing[0].Payload, candidate.Payload) || !bytes.Equal(signing[0].PAE, candidate.PAE) ||
		signing[0].PayloadHash != candidate.PayloadDigest || signing[0].PAEHash != candidate.PAEDigest {
		return fmt.Errorf("%w: completion payload/PAE differs from signing requests", ErrControlConflict)
	}
	receipt, err := DecodePayload(candidate.Payload)
	if err != nil || !operationalAuthoritiesMatchReceipt(requests[0], requests[1], signing, receipt) ||
		resultsMatchReceipt(observations[ControlRoleSealer], observations[ControlRoleVerifier], receipt) != nil {
		return fmt.Errorf("%w: completion payload differs from actual snapshot results", ErrControlConflict)
	}
	if candidate.PlanAuthorityHash != signing[0].Request.PlanAuthorityHash ||
		candidate.EvidenceClosureDigest != signing[0].Request.EvidenceClosureDigest ||
		candidate.SnapshotID != signing[0].Request.SnapshotID || candidate.SnapshotDigest != signing[0].Request.SnapshotDigest {
		return fmt.Errorf("%w: completion top-level anchors drift", ErrControlConflict)
	}
	envelope, err := buildControlDSSEEnvelope(candidate.Payload, observations[ControlRoleRunner], observations[ControlRoleReleaseApprover])
	if err != nil || !bytes.Equal(envelope, candidate.Envelope) || SHA256Digest(envelope) != candidate.EnvelopeDigest {
		return fmt.Errorf("%w: completion envelope differs from committed signatures", ErrControlConflict)
	}
	return nil
}

func (store *MemoryControlStore) InspectCompletion(ctx context.Context, authorityID uuid.UUID) (CompletionRecord, error) {
	if store == nil || isNilInterface(ctx) || authorityID.Version() != 4 {
		return CompletionRecord{}, ErrControlNotFound
	}
	if err := ctx.Err(); err != nil {
		return CompletionRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.completions[authorityID]
	if !ok {
		return CompletionRecord{}, ErrControlNotFound
	}
	return cloneCompletion(record), nil
}

func (store *MemoryControlStore) requestCommittedLocked(request RequestRecord) bool {
	return store.lastObservationLocked(request.RequestHash).Status == ObservationCommitted
}

func (store *MemoryControlStore) lastObservationLocked(requestHash string) ObservationRecord {
	rows := store.observations[requestHash]
	if len(rows) == 0 {
		return ObservationRecord{}
	}
	return rows[len(rows)-1]
}

func (store *MemoryControlStore) observationByHashLocked(hash string) ObservationRecord {
	for _, rows := range store.observations {
		for _, row := range rows {
			if row.RecordHash == hash {
				return row
			}
		}
	}
	return ObservationRecord{}
}

func (store *MemoryControlStore) InjectStartCommitUnknownOnce() {
	if store != nil {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.unknownStartOnce = true
	}
}

func (store *MemoryControlStore) InjectObservationCommitUnknownOnce() {
	if store != nil {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.unknownObservationOnce = true
	}
}

func (store *MemoryControlStore) InjectCompletionCommitUnknownOnce() {
	if store != nil {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.unknownCompletionOnce = true
	}
}

func cloneRequestRecord(record RequestRecord) RequestRecord {
	cloned := record
	cloned.RequestBytes = bytes.Clone(record.RequestBytes)
	cloned.Payload = bytes.Clone(record.Payload)
	cloned.PAE = bytes.Clone(record.PAE)
	return cloned
}

func cloneRequestRecords(records []RequestRecord) []RequestRecord {
	cloned := make([]RequestRecord, len(records))
	for index, record := range records {
		cloned[index] = cloneRequestRecord(record)
	}
	return cloned
}

func markRequestReplay(records []RequestRecord) {
	for index := range records {
		records[index].Idempotent = true
	}
}

func sameRequestBatch(left, right []RequestRecord, includeStored bool) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy, rightCopy := cloneRequestRecords(left), cloneRequestRecords(right)
	sort.Slice(leftCopy, func(i, j int) bool { return leftCopy[i].Key.Role < leftCopy[j].Key.Role })
	sort.Slice(rightCopy, func(i, j int) bool { return rightCopy[i].Key.Role < rightCopy[j].Key.Role })
	for index := range leftCopy {
		if !sameRequestRecord(leftCopy[index], rightCopy[index], includeStored) {
			return false
		}
	}
	return true
}

func sameRequestRecord(left, right RequestRecord, includeStored bool) bool {
	if !includeStored {
		left.StartedAt, right.StartedAt = time.Time{}, time.Time{}
	}
	left.Idempotent, right.Idempotent = false, false
	return left.Key == right.Key && left.Request == right.Request && left.RequestHash == right.RequestHash &&
		left.PayloadHash == right.PayloadHash && left.PAEHash == right.PAEHash && left.StartedAt.Equal(right.StartedAt) &&
		bytes.Equal(left.RequestBytes, right.RequestBytes) && bytes.Equal(left.Payload, right.Payload) && bytes.Equal(left.PAE, right.PAE)
}

func cloneObservation(record ObservationRecord) ObservationRecord {
	cloned := record
	cloned.AuthenticationPayloadBytes = bytes.Clone(record.AuthenticationPayloadBytes)
	cloned.AuthenticationBytes = bytes.Clone(record.AuthenticationBytes)
	cloned.Result = append([]byte(nil), record.Result...)
	cloned.ResultBytes = bytes.Clone(record.ResultBytes)
	cloned.Signature = bytes.Clone(record.Signature)
	cloned.ClaimBytes = bytes.Clone(record.ClaimBytes)
	cloned.AckBytes = bytes.Clone(record.AckBytes)
	return cloned
}

func cloneObservations(records []ObservationRecord) []ObservationRecord {
	cloned := make([]ObservationRecord, len(records))
	for index, record := range records {
		cloned[index] = cloneObservation(record)
	}
	return cloned
}

func sameObservation(left, right ObservationRecord, includeStored bool) bool {
	if !includeStored {
		left.RecordedAt, right.RecordedAt = time.Time{}, time.Time{}
		left.RecordHash, right.RecordHash = "", ""
	}
	left.Idempotent, right.Idempotent = false, false
	return left.RequestKey == right.RequestKey && left.RequestHash == right.RequestHash && left.Generation == right.Generation &&
		left.Sequence == right.Sequence && left.Status == right.Status && left.ObservedAt.Equal(right.ObservedAt) && left.RecordedAt.Equal(right.RecordedAt) &&
		left.AuthenticationKeyID == right.AuthenticationKeyID && left.AuthenticationPayload == right.AuthenticationPayload &&
		left.AuthenticationPayloadHash == right.AuthenticationPayloadHash && left.AuthenticationEnvelope == right.AuthenticationEnvelope &&
		left.AuthenticationEnvelopeHash == right.AuthenticationEnvelopeHash && left.ResultHash == right.ResultHash &&
		left.SignatureHash == right.SignatureHash && left.Claim == right.Claim && left.ClaimTokenHash == right.ClaimTokenHash &&
		left.Acknowledgement == right.Acknowledgement && left.AckTokenHash == right.AckTokenHash && left.RecordHash == right.RecordHash &&
		bytes.Equal(left.AuthenticationPayloadBytes, right.AuthenticationPayloadBytes) && bytes.Equal(left.AuthenticationBytes, right.AuthenticationBytes) && bytes.Equal(left.Result, right.Result) &&
		bytes.Equal(left.ResultBytes, right.ResultBytes) && bytes.Equal(left.Signature, right.Signature) &&
		bytes.Equal(left.ClaimBytes, right.ClaimBytes) && bytes.Equal(left.AckBytes, right.AckBytes)
}

func cloneCompletion(record CompletionRecord) CompletionRecord {
	cloned := record
	cloned.Payload = bytes.Clone(record.Payload)
	cloned.PAE = bytes.Clone(record.PAE)
	cloned.Envelope = bytes.Clone(record.Envelope)
	cloned.DocumentBytes = bytes.Clone(record.DocumentBytes)
	return cloned
}

func sameCompletion(left, right CompletionRecord, includeStored bool) bool {
	if !includeStored {
		left.CompletedAt, right.CompletedAt = time.Time{}, time.Time{}
		left.Document, right.Document = CompletionDocument{}, CompletionDocument{}
		left.DocumentBytes, right.DocumentBytes = nil, nil
		left.DocumentHash, right.DocumentHash = "", ""
	}
	left.Idempotent, right.Idempotent = false, false
	return left.AuthorityID == right.AuthorityID && left.ReceiptID == right.ReceiptID && left.PlanAuthorityHash == right.PlanAuthorityHash &&
		left.EvidenceClosureDigest == right.EvidenceClosureDigest && left.SnapshotID == right.SnapshotID && left.SnapshotDigest == right.SnapshotDigest &&
		left.RequestHashes == right.RequestHashes && left.ObservationHashes == right.ObservationHashes && left.Operations == right.Operations &&
		left.PayloadDigest == right.PayloadDigest && left.PAEDigest == right.PAEDigest && left.EnvelopeDigest == right.EnvelopeDigest &&
		left.verificationEnvelopeHash == right.verificationEnvelopeHash &&
		left.Document == right.Document && left.DocumentHash == right.DocumentHash && left.CompletedAt.Equal(right.CompletedAt) &&
		bytes.Equal(left.Payload, right.Payload) && bytes.Equal(left.PAE, right.PAE) && bytes.Equal(left.Envelope, right.Envelope) &&
		bytes.Equal(left.DocumentBytes, right.DocumentBytes)
}

var _ ControlStore = (*MemoryControlStore)(nil)
