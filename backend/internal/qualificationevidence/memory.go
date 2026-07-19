package qualificationevidence

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// MemoryStore is a thread-safe append-only reference implementation for tests
// and local composition. It is not a durable production qualification ledger.
type MemoryStore struct {
	mu     sync.RWMutex
	events map[string][]Event
	heads  map[string]Snapshot
	clock  Clock
}

func NewMemoryStore(clocks ...Clock) *MemoryStore {
	clock := Clock(systemClock{})
	if len(clocks) == 1 && !isNilInterface(clocks[0]) {
		clock = clocks[0]
	}
	return &MemoryStore{events: make(map[string][]Event), heads: make(map[string]Snapshot), clock: clock}
}

func (store *MemoryStore) TrustedTime(ctx context.Context) (time.Time, error) {
	if store == nil || isNilInterface(store.clock) || isNilInterface(ctx) {
		return time.Time{}, fmt.Errorf("%w: memory store clock or context is nil", ErrInvalid)
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

func (store *MemoryStore) Create(ctx context.Context, orchestrationID string, event Event) (Snapshot, bool, error) {
	if store == nil || isNilInterface(ctx) || !validUUIDv4(orchestrationID) || event.Kind != EventReserved {
		return Snapshot{}, false, fmt.Errorf("%w: reservation is invalid", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing := store.events[orchestrationID]; len(existing) > 0 {
		snapshot, exists := store.heads[orchestrationID]
		if !exists {
			return Snapshot{}, false, ErrInvalidTransition
		}
		if existing[0].EventID == event.EventID && !canonicalEqual(existing[0], event) {
			return Snapshot{}, false, ErrIdempotencyConflict
		}
		return cloneSnapshot(snapshot), false, nil
	}
	initial := Snapshot{OrchestrationID: orchestrationID}
	updated, err := applyEvent(initial, event)
	if err != nil {
		return Snapshot{}, false, err
	}
	store.events[orchestrationID] = []Event{cloneEvent(event)}
	store.heads[orchestrationID] = cloneSnapshot(updated)
	return cloneSnapshot(updated), true, nil
}

func (store *MemoryStore) Load(ctx context.Context, orchestrationID string) (Snapshot, error) {
	if store == nil || isNilInterface(ctx) || !validUUIDv4(orchestrationID) {
		return Snapshot{}, fmt.Errorf("%w: orchestration identity or context is invalid", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	snapshot, exists := store.heads[orchestrationID]
	if !exists || len(store.events[orchestrationID]) == 0 {
		return Snapshot{}, ErrNotFound
	}
	return cloneSnapshot(snapshot), nil
}

func (store *MemoryStore) Append(ctx context.Context, orchestrationID string, expectedVersion uint64, event Event) (Snapshot, error) {
	if store == nil || isNilInterface(ctx) || !validUUIDv4(orchestrationID) {
		return Snapshot{}, fmt.Errorf("%w: orchestration identity or context is invalid", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	events := store.events[orchestrationID]
	snapshot, exists := store.heads[orchestrationID]
	if len(events) == 0 || !exists {
		return Snapshot{}, ErrNotFound
	}
	for _, existing := range events {
		if existing.EventID != event.EventID {
			continue
		}
		if !canonicalEqual(existing, event) {
			return Snapshot{}, ErrIdempotencyConflict
		}
		return cloneSnapshot(snapshot), nil
	}
	if snapshot.Version != expectedVersion {
		return cloneSnapshot(snapshot), ErrCASConflict
	}
	updated, err := applyEvent(snapshot, event)
	if err != nil {
		return Snapshot{}, err
	}
	store.events[orchestrationID] = append(events, cloneEvent(event))
	store.heads[orchestrationID] = cloneSnapshot(updated)
	return cloneSnapshot(updated), nil
}

func (store *MemoryStore) Events(ctx context.Context, orchestrationID string) ([]Event, error) {
	if store == nil || isNilInterface(ctx) || !validUUIDv4(orchestrationID) {
		return nil, fmt.Errorf("%w: orchestration identity or context is invalid", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	events := store.events[orchestrationID]
	if len(events) == 0 {
		return nil, ErrNotFound
	}
	result := make([]Event, len(events))
	for index, event := range events {
		result[index] = cloneEvent(event)
	}
	return result, nil
}

func reduceEvents(orchestrationID string, events []Event) (Snapshot, error) {
	snapshot := Snapshot{OrchestrationID: orchestrationID}
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		if _, duplicate := seen[event.EventID]; duplicate {
			return Snapshot{}, fmt.Errorf("%w: event ID is duplicated", ErrInvalidTransition)
		}
		seen[event.EventID] = struct{}{}
		updated, err := applyEvent(snapshot, event)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot = updated
	}
	return snapshot, nil
}

func applyEvent(snapshot Snapshot, event Event) (Snapshot, error) {
	if !validUUIDv4(event.EventID) || !validUUIDv4(event.OperationID) {
		return Snapshot{}, fmt.Errorf("%w: event identity is invalid", ErrInvalidTransition)
	}
	at, err := parseCanonicalTime(event.At)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: event time is invalid", ErrInvalidTransition)
	}
	if snapshot.Version > 0 {
		previous, previousErr := parseCanonicalTime(snapshot.LastEventAt)
		if previousErr != nil || at.Before(previous) {
			return Snapshot{}, fmt.Errorf("%w: event time regressed", ErrInvalidTransition)
		}
	}
	updated := cloneSnapshot(snapshot)
	switch event.Kind {
	case EventReserved:
		if snapshot.Version != 0 || snapshot.Phase != "" || event.Plan == nil || ValidatePlan(*event.Plan) != nil ||
			event.Plan.OrchestrationID != snapshot.OrchestrationID || event.OperationID != event.Plan.Operations.Reserve ||
			!validDigest(event.CommandHash) || !validDigest(event.TrustBindingsDigest) || !eventOnly(event, event.Plan, nil) {
			return Snapshot{}, ErrInvalidTransition
		}
		plan := clonePlan(*event.Plan)
		updated.Plan = &plan
		updated.CommandHash = event.CommandHash
		updated.TrustBindingsDigest = event.TrustBindingsDigest
		updated.Phase = PhaseReserved
	case EventCredentialIssueStarted:
		if snapshot.Phase != PhaseReserved || snapshot.Plan == nil || event.OperationID != snapshot.Plan.Operations.CredentialIssue || !eventOnly(event, nil, nil) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseCredentialIssueStarted
		updated.ActiveOperationID = event.OperationID
	case EventCredentialIssued:
		if snapshot.Phase != PhaseCredentialIssueStarted || event.OperationID != snapshot.ActiveOperationID || event.CredentialIssue == nil ||
			!eventOnly(event, nil, event.CredentialIssue) || validateCredentialIssue(*event.CredentialIssue, *snapshot.Plan) != nil {
			return Snapshot{}, ErrInvalidTransition
		}
		issuedAt, _ := parseCanonicalTime(event.CredentialIssue.Binding.IssuedAt)
		expiresAt, _ := parseCanonicalTime(event.CredentialIssue.Binding.ExpiresAt)
		if at.Before(issuedAt) || !at.Before(expiresAt) {
			return Snapshot{}, ErrInvalidTransition
		}
		value := cloneCredentialIssue(*event.CredentialIssue)
		updated.CredentialIssue = &value
		updated.Phase = PhaseCredentialIssued
		updated.ActiveOperationID = ""
	case EventRunClosureStarted:
		if snapshot.Phase != PhaseCredentialIssued || event.OperationID != snapshot.Plan.Operations.RunClosure || !eventOnly(event, nil, nil) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseRunClosureStarted
		updated.ActiveOperationID = event.OperationID
	case EventRunClosureAccepted:
		if snapshot.Phase != PhaseRunClosureStarted || event.OperationID != snapshot.ActiveOperationID || event.RunClosure == nil ||
			!eventOnly(event, nil, event.RunClosure) || validateRunClosureStored(*event.RunClosure, *snapshot.Plan, *snapshot.CredentialIssue) != nil {
			return Snapshot{}, ErrInvalidTransition
		}
		completedAt, _ := parseCanonicalTime(event.RunClosure.CompletedAt)
		if completedAt.After(at) {
			return Snapshot{}, ErrInvalidTransition
		}
		value := cloneRunClosure(*event.RunClosure)
		updated.RunClosure = &value
		updated.Phase = PhaseRunClosureAccepted
		updated.ActiveOperationID = ""
	case EventEncryptionStarted:
		expected, ok := nextRestrictedArtifact(snapshot)
		if !ok || (snapshot.Phase != PhaseRunClosureAccepted && snapshot.Phase != PhaseEncrypting) || snapshot.ActiveOperationID != "" ||
			event.OperationID != expected.EncryptionOperationID || !eventOnly(event, nil, nil) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseEncrypting
		updated.ActiveOperationID = event.OperationID
		updated.ActiveArtifactID = expected.ID
	case EventEncryptionCommitted:
		if snapshot.Phase != PhaseEncrypting || snapshot.ActiveOperationID == "" || event.OperationID != snapshot.ActiveOperationID ||
			event.Encryption == nil || event.Encryption.ArtifactID != snapshot.ActiveArtifactID || !eventOnly(event, nil, event.Encryption) ||
			validateStoredEncryption(snapshot, *event.Encryption) != nil {
			return Snapshot{}, ErrInvalidTransition
		}
		disposedAt, _ := parseCanonicalTime(event.Encryption.PlaintextDispositionAt)
		if disposedAt.After(at) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Encryptions = append(updated.Encryptions, *event.Encryption)
		updated.ActiveOperationID = ""
		updated.ActiveArtifactID = ""
	case EventKMSAttestationStarted:
		if snapshot.Phase != PhaseEncrypting || snapshot.ActiveOperationID != "" || !allRestrictedEncrypted(snapshot) ||
			event.OperationID != snapshot.Plan.Operations.KMSAttestation || !eventOnly(event, nil, nil) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseKMSAttestationStarted
		updated.ActiveOperationID = event.OperationID
	case EventKMSAttested:
		if snapshot.Phase != PhaseKMSAttestationStarted || event.OperationID != snapshot.ActiveOperationID || event.KMSAttestation == nil ||
			!eventOnly(event, nil, event.KMSAttestation) || validateStoredKMS(snapshot, *event.KMSAttestation) != nil {
			return Snapshot{}, ErrInvalidTransition
		}
		kmsIssuedAt, _ := parseCanonicalTime(event.KMSAttestation.Attestation.IssuedAt)
		if kmsIssuedAt.After(at) {
			return Snapshot{}, ErrInvalidTransition
		}
		value := *event.KMSAttestation
		updated.KMSAttestation = &value
		updated.Phase = PhaseKMSAttested
		updated.ActiveOperationID = ""
	case EventCredentialRevocationStarted:
		if snapshot.Phase != PhaseKMSAttested || event.OperationID != snapshot.Plan.Operations.CredentialRevocation || !eventOnly(event, nil, nil) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseCredentialRevocationStarted
		updated.ActiveOperationID = event.OperationID
	case EventCredentialRevoked:
		if snapshot.Phase != PhaseCredentialRevocationStarted || event.OperationID != snapshot.ActiveOperationID || event.CredentialRevocation == nil ||
			!eventOnly(event, nil, event.CredentialRevocation) || validateStoredRevocation(snapshot, *event.CredentialRevocation) != nil {
			return Snapshot{}, ErrInvalidTransition
		}
		revokedAt, _ := parseCanonicalTime(event.CredentialRevocation.RevokedAt)
		if revokedAt.After(at) {
			return Snapshot{}, ErrInvalidTransition
		}
		value := cloneCredentialRevocation(*event.CredentialRevocation)
		updated.CredentialRevocation = &value
		updated.Phase = PhaseCredentialRevoked
		updated.ActiveOperationID = ""
	case EventArtifactIndexStarted:
		if snapshot.Phase != PhaseCredentialRevoked || event.OperationID != snapshot.Plan.Operations.ArtifactIndex || !eventOnly(event, nil, nil) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseArtifactIndexStarted
		updated.ActiveOperationID = event.OperationID
	case EventArtifactIndexed:
		if snapshot.Phase != PhaseArtifactIndexStarted || event.OperationID != snapshot.ActiveOperationID || event.ArtifactIndex == nil ||
			!eventOnly(event, nil, event.ArtifactIndex) || validateStoredIndex(snapshot, *event.ArtifactIndex) != nil {
			return Snapshot{}, ErrInvalidTransition
		}
		value := *event.ArtifactIndex
		updated.ArtifactIndex = &value
		updated.Phase = PhaseArtifactIndexed
		updated.ActiveOperationID = ""
	case EventReceiptSignStarted:
		if snapshot.Phase != PhaseArtifactIndexed || event.OperationID != snapshot.Plan.Operations.ReceiptSign || !eventOnly(event, nil, nil) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseReceiptSignStarted
		updated.ActiveOperationID = event.OperationID
	case EventReceiptSigned:
		if snapshot.Phase != PhaseReceiptSignStarted || event.OperationID != snapshot.ActiveOperationID || event.Receipt == nil ||
			!eventOnly(event, nil, event.Receipt) || validateStoredReceipt(snapshot, *event.Receipt) != nil {
			return Snapshot{}, ErrInvalidTransition
		}
		receiptIssuedAt, _ := parseCanonicalTime(event.Receipt.IssuedAt)
		if receiptIssuedAt.After(at) {
			return Snapshot{}, ErrInvalidTransition
		}
		value := *event.Receipt
		updated.Receipt = &value
		updated.Phase = PhaseReceiptSigned
		updated.ActiveOperationID = ""
	case EventSnapshotSealStarted:
		if snapshot.Phase != PhaseReceiptSigned || event.OperationID != snapshot.Plan.Operations.SnapshotSeal || !eventOnly(event, nil, nil) {
			return Snapshot{}, ErrInvalidTransition
		}
		updated.Phase = PhaseSnapshotSealStarted
		updated.ActiveOperationID = event.OperationID
	case EventSnapshotSealed:
		if snapshot.Phase != PhaseSnapshotSealStarted || event.OperationID != snapshot.ActiveOperationID || event.Snapshot == nil ||
			!eventOnly(event, nil, event.Snapshot) || validateStoredSnapshot(snapshot, *event.Snapshot) != nil {
			return Snapshot{}, ErrInvalidTransition
		}
		sealedAt, _ := parseCanonicalTime(event.Snapshot.SealedAt)
		if sealedAt.After(at) {
			return Snapshot{}, ErrInvalidTransition
		}
		value := *event.Snapshot
		updated.SealedSnapshot = &value
		updated.Phase = PhaseSnapshotSealed
		updated.ActiveOperationID = ""
	case EventSnapshotVerified:
		if snapshot.Phase != PhaseSnapshotSealed || event.OperationID != snapshot.Plan.Operations.SnapshotSeal || event.Verification == nil ||
			!eventOnly(event, nil, event.Verification) || validateStoredVerification(snapshot, *event.Verification) != nil {
			return Snapshot{}, ErrInvalidTransition
		}
		verifiedAt, _ := parseCanonicalTime(event.Verification.VerifiedAt)
		if verifiedAt.After(at) {
			return Snapshot{}, ErrInvalidTransition
		}
		value := *event.Verification
		updated.Verification = &value
		updated.Phase = PhaseComplete
	default:
		return Snapshot{}, ErrInvalidTransition
	}
	updated.Version++
	updated.LastEventID = event.EventID
	updated.LastEventAt = event.At
	return updated, nil
}

// eventOnly ensures each event carries only its one phase-specific payload.
func eventOnly(event Event, plan *Plan, payload any) bool {
	if event.CommandHash != "" && event.Kind != EventReserved {
		return false
	}
	if event.TrustBindingsDigest != "" && event.Kind != EventReserved {
		return false
	}
	pointers := []any{
		event.CredentialIssue, event.RunClosure, event.Encryption, event.KMSAttestation,
		event.CredentialRevocation, event.ArtifactIndex, event.Receipt, event.Snapshot, event.Verification,
	}
	count := 0
	for _, pointer := range pointers {
		if !isNilInterface(pointer) {
			count++
		}
	}
	if payload == nil && count != 0 {
		return false
	}
	if payload != nil && count != 1 {
		return false
	}
	return (plan == nil && event.Plan == nil) || (plan != nil && event.Plan == plan)
}

func validateCredentialIssue(value CredentialIssueObservation, plan Plan) error {
	request := CredentialIssueRequest{
		OperationID: plan.Operations.CredentialIssue, RunID: plan.RunID, FixtureID: plan.FixtureID,
		PlanDigest: plan.PlanDigest, Expected: plan.CredentialSet,
	}
	requestDigest, err := digestRequest(request)
	if err != nil {
		return err
	}
	if value.OperationID != plan.Operations.CredentialIssue || value.Stage != AuthorityCommitted ||
		value.RequestDigest != requestDigest ||
		ValidateCredentialBinding(value.Binding, plan) != nil ||
		validateSignedArtifact(value.Attestation, plan.CredentialSet.IssuanceArtifactID, plan.CredentialSet.Issuer) != nil ||
		value.Attestation.IssuedAt != value.Binding.IssuedAt {
		return ErrCredentialDrift
	}
	return nil
}

func validateRunClosureStored(value RunClosureObservation, plan Plan, issue CredentialIssueObservation) error {
	bindingDigest, err := credentialBindingDigest(issue.Binding)
	if err != nil {
		return err
	}
	expectedDigest, err := expectedArtifactSetDigest(plan)
	if err != nil {
		return err
	}
	request := RunClosureRequest{
		OperationID: value.OperationID, RunID: plan.RunID, PlanDigest: plan.PlanDigest,
		CredentialSetBindingDigest: bindingDigest, ExpectedArtifactSetDigest: expectedDigest,
	}
	if !validIdentity(value.AuthorityID) || validateRunClosure(value, request, plan, TrustBindings{CaptureAuthorityID: value.AuthorityID}) != nil {
		return ErrEvidenceClosure
	}
	completed, _ := parseCanonicalTime(value.CompletedAt)
	issued, _ := parseCanonicalTime(issue.Binding.IssuedAt)
	expires, _ := parseCanonicalTime(issue.Binding.ExpiresAt)
	if !completed.After(issued) || !completed.Before(expires) {
		return ErrEvidenceClosure
	}
	return nil
}

func nextRestrictedArtifact(snapshot Snapshot) (ArtifactExpectation, bool) {
	if snapshot.Plan == nil {
		return ArtifactExpectation{}, false
	}
	completed := len(snapshot.Encryptions)
	seen := 0
	for _, artifact := range snapshot.Plan.Artifacts {
		if artifact.Classification != ClassificationRestricted {
			continue
		}
		if seen == completed {
			return artifact, true
		}
		seen++
	}
	return ArtifactExpectation{}, false
}

func capturedByID(snapshot Snapshot, artifactID string) (CapturedArtifact, bool) {
	if snapshot.RunClosure == nil {
		return CapturedArtifact{}, false
	}
	for _, artifact := range snapshot.RunClosure.Artifacts {
		if artifact.ID == artifactID {
			return artifact, true
		}
	}
	return CapturedArtifact{}, false
}

func validateStoredEncryption(snapshot Snapshot, value EncryptionCommitment) error {
	captured, exists := capturedByID(snapshot, snapshot.ActiveArtifactID)
	if !exists {
		return ErrEvidenceClosure
	}
	aad, err := encryptionAdditionalDataHash(*snapshot.Plan, captured)
	if err != nil {
		return err
	}
	request := EncryptionRequest{
		OperationID: snapshot.ActiveOperationID, RunID: snapshot.Plan.RunID, PlanDigest: snapshot.Plan.PlanDigest,
		Artifact: captured, Recipient: snapshot.Plan.Recipient, AdditionalDataHash: aad,
	}
	if !validIdentity(value.AuthorityID) || validateEncryption(value, request, TrustBindings{EncryptionAuthorityID: value.AuthorityID}) != nil {
		return ErrDigestDrift
	}
	runCompleted, _ := parseCanonicalTime(snapshot.RunClosure.CompletedAt)
	encryptedAt, _ := parseCanonicalTime(value.EncryptedAt)
	if !encryptedAt.After(runCompleted) {
		return ErrPlaintextDisposition
	}
	return nil
}

func allRestrictedEncrypted(snapshot Snapshot) bool {
	_, remaining := nextRestrictedArtifact(snapshot)
	return !remaining && len(snapshot.Encryptions) > 0
}

func validateStoredKMS(snapshot Snapshot, value KMSAttestationObservation) error {
	manifest, err := encryptionManifestDigest(snapshot.Encryptions)
	if err != nil {
		return err
	}
	artifactSet, err := preKMSArtifactSetDigest(snapshot)
	if err != nil {
		return err
	}
	payloadDigest, err := expectedKMSPayloadDigest(snapshot.Plan.RunID, snapshot.Plan.PlanDigest, manifest, artifactSet)
	if err != nil {
		return err
	}
	request := KMSAttestationRequest{
		OperationID: snapshot.Plan.Operations.KMSAttestation, RunID: snapshot.Plan.RunID,
		PlanDigest: snapshot.Plan.PlanDigest, ManifestDigest: manifest, ArtifactSetDigest: artifactSet,
		ArtifactCount: len(snapshot.Encryptions), ExpectedArtifactID: snapshot.Plan.Outputs.KMSAttestationArtifactID,
		ExpectedPayloadDigest: payloadDigest,
	}
	requestDigest, err := digestRequest(request)
	if err != nil {
		return err
	}
	if value.OperationID != snapshot.Plan.Operations.KMSAttestation || !validIdentity(value.AuthorityID) || value.Stage != AuthorityCommitted ||
		value.RequestDigest != requestDigest || value.ManifestDigest != manifest || value.ArtifactSetDigest != artifactSet ||
		value.ManifestDigest == value.ArtifactSetDigest ||
		value.Attestation.PayloadDigest != payloadDigest ||
		validateSignedArtifact(value.Attestation, snapshot.Plan.Outputs.KMSAttestationArtifactID, value.AuthorityID) != nil {
		return ErrDigestDrift
	}
	latestDisposition := time.Time{}
	for _, encryption := range snapshot.Encryptions {
		disposed, _ := parseCanonicalTime(encryption.PlaintextDispositionAt)
		if disposed.After(latestDisposition) {
			latestDisposition = disposed
		}
	}
	issuedAt, _ := parseCanonicalTime(value.Attestation.IssuedAt)
	if !issuedAt.After(latestDisposition) {
		return ErrPlaintextDisposition
	}
	return nil
}

func validateStoredRevocation(snapshot Snapshot, value CredentialRevocationObservation) error {
	request := CredentialRevocationRequest{
		OperationID: snapshot.Plan.Operations.CredentialRevocation, RunID: snapshot.Plan.RunID,
		Binding:              cloneCredentialBinding(snapshot.CredentialIssue.Binding),
		KMSAttestationDigest: snapshot.KMSAttestation.Attestation.PayloadDigest,
	}
	requestDigest, err := digestRequest(request)
	if err != nil {
		return err
	}
	if value.OperationID != snapshot.Plan.Operations.CredentialRevocation || value.Stage != AuthorityCommitted ||
		value.RequestDigest != requestDigest || value.KMSAttestationDigest != request.KMSAttestationDigest ||
		!equalCredentialBinding(value.Binding, snapshot.CredentialIssue.Binding) ||
		validateSignedArtifact(value.Attestation, snapshot.Plan.CredentialSet.RevocationArtifactID, snapshot.Plan.CredentialSet.Issuer) != nil {
		return ErrCredentialDrift
	}
	revokedAt, err := parseCanonicalTime(value.RevokedAt)
	if err != nil || value.Attestation.IssuedAt != value.RevokedAt {
		return ErrCredentialDrift
	}
	kmsAt, _ := parseCanonicalTime(snapshot.KMSAttestation.Attestation.IssuedAt)
	expiresAt, _ := parseCanonicalTime(snapshot.CredentialIssue.Binding.ExpiresAt)
	if !revokedAt.After(kmsAt) || !revokedAt.Before(expiresAt) {
		return ErrCredentialDrift
	}
	return nil
}

func validateStoredIndex(snapshot Snapshot, value ArtifactIndexCommitment) error {
	closure, err := evidenceClosureDigest(snapshot)
	if err != nil {
		return err
	}
	artifactDigest, _ := artifactSetDigest(snapshot)
	restricted := len(snapshot.Encryptions)
	count := len(snapshot.Plan.Artifacts) + 3
	request := ArtifactIndexRequest{
		OperationID: snapshot.Plan.Operations.ArtifactIndex, RunID: snapshot.Plan.RunID, PlanDigest: snapshot.Plan.PlanDigest,
		EvidenceClosureDigest: closure, ArtifactSetDigest: artifactDigest, ArtifactCount: count,
		RestrictedArtifactCount: restricted, ExpectedIndexID: snapshot.Plan.Outputs.ArtifactIndexID,
	}
	requestDigest, err := digestRequest(request)
	if err != nil {
		return err
	}
	if value.OperationID != snapshot.Plan.Operations.ArtifactIndex || !validIdentity(value.AuthorityID) || value.Stage != AuthorityCommitted ||
		value.RequestDigest != requestDigest ||
		value.IndexID != snapshot.Plan.Outputs.ArtifactIndexID || !validDigest(value.ContentDigest) ||
		value.EvidenceClosureDigest != closure || value.ArtifactSetDigest != artifactDigest ||
		value.ArtifactCount != count || value.RestrictedArtifactCount != restricted {
		return ErrDigestDrift
	}
	return nil
}

func validateStoredReceipt(snapshot Snapshot, value QualificationReceiptCommitment) error {
	closure, err := evidenceClosureDigest(snapshot)
	if err != nil {
		return err
	}
	payloadDigest, err := expectedReceiptPayloadDigest(snapshot.Plan.RunID, snapshot.Plan.PlanDigest, closure, *snapshot.ArtifactIndex)
	if err != nil {
		return err
	}
	request := ReceiptSignRequest{
		OperationID: snapshot.Plan.Operations.ReceiptSign, RunID: snapshot.Plan.RunID, PlanDigest: snapshot.Plan.PlanDigest,
		EvidenceClosureDigest: closure, Index: *snapshot.ArtifactIndex, ExpectedReceiptID: snapshot.Plan.Outputs.ReceiptID,
		ExpectedPayloadDigest: payloadDigest,
	}
	requestDigest, err := digestRequest(request)
	if err != nil {
		return err
	}
	if value.OperationID != snapshot.Plan.Operations.ReceiptSign || !validIdentity(value.AuthorityID) || value.Stage != AuthorityCommitted ||
		value.RequestDigest != requestDigest ||
		value.ReceiptID != snapshot.Plan.Outputs.ReceiptID || !validDigest(value.ContentDigest) || !validDigest(value.PayloadDigest) ||
		value.PayloadDigest != payloadDigest ||
		value.SubjectIndexDigest != snapshot.ArtifactIndex.ContentDigest || value.EvidenceClosureDigest != closure ||
		!validDigest(value.SignerSetDigest) || value.SignerCount != 2 {
		return ErrDigestDrift
	}
	issuedAt, issueErr := parseCanonicalTime(value.IssuedAt)
	revokedAt, revokeErr := parseCanonicalTime(snapshot.CredentialRevocation.RevokedAt)
	if issueErr != nil || revokeErr != nil || !issuedAt.After(revokedAt) {
		return ErrDigestDrift
	}
	return nil
}

func validateStoredSnapshot(snapshot Snapshot, value SnapshotCommitment) error {
	closure, err := evidenceClosureDigest(snapshot)
	if err != nil {
		return err
	}
	request := SnapshotSealRequest{
		OperationID: snapshot.Plan.Operations.SnapshotSeal, RunID: snapshot.Plan.RunID,
		EvidenceClosureDigest: closure, Index: *snapshot.ArtifactIndex, Receipt: *snapshot.Receipt,
		ExpectedSnapshotID: snapshot.Plan.Outputs.SnapshotID, Mode: ImmutableSnapshotMode,
	}
	requestDigest, err := digestRequest(request)
	if err != nil {
		return err
	}
	if value.OperationID != snapshot.Plan.Operations.SnapshotSeal || !validIdentity(value.AuthorityID) || value.Stage != AuthorityCommitted ||
		value.RequestDigest != requestDigest ||
		value.SnapshotID != snapshot.Plan.Outputs.SnapshotID || !validDigest(value.SnapshotDigest) ||
		value.EvidenceClosureDigest != closure || value.IndexDigest != snapshot.ArtifactIndex.ContentDigest ||
		value.ReceiptDigest != snapshot.Receipt.ContentDigest || value.Mode != ImmutableSnapshotMode {
		return ErrDigestDrift
	}
	sealedAt, sealErr := parseCanonicalTime(value.SealedAt)
	receiptAt, receiptErr := parseCanonicalTime(snapshot.Receipt.IssuedAt)
	if sealErr != nil || receiptErr != nil || !sealedAt.After(receiptAt) {
		return ErrDigestDrift
	}
	return nil
}

func validateStoredVerification(snapshot Snapshot, value SnapshotVerification) error {
	if !validIdentity(value.AuthorityID) || value.SnapshotID != snapshot.SealedSnapshot.SnapshotID ||
		value.SnapshotDigest != snapshot.SealedSnapshot.SnapshotDigest ||
		value.EvidenceClosureDigest != snapshot.SealedSnapshot.EvidenceClosureDigest ||
		value.IndexDigest != snapshot.SealedSnapshot.IndexDigest || value.ReceiptDigest != snapshot.SealedSnapshot.ReceiptDigest {
		return ErrDigestDrift
	}
	verifiedAt, verifyErr := parseCanonicalTime(value.VerifiedAt)
	sealedAt, sealErr := parseCanonicalTime(snapshot.SealedSnapshot.SealedAt)
	if verifyErr != nil || sealErr != nil || verifiedAt.Before(sealedAt) {
		return ErrDigestDrift
	}
	return nil
}

var _ Store = (*MemoryStore)(nil)

// Keep errors imported when build tags trim external adapters in downstream
// compositions; it also documents errors.Is as the Store contract.
var _ = errors.Is
