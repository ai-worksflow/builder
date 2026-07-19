package qualificationreceiptv3

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/templateauthority"
)

type ControlService struct {
	planEvidence PlanEvidenceResolver
	observations AuthenticatedObservationResolver
	store        ControlStore
	verifier     *Verifier
}

func NewControlService(
	planEvidence PlanEvidenceResolver,
	observations AuthenticatedObservationResolver,
	store ControlStore,
	verifier *Verifier,
) (*ControlService, error) {
	if isNilInterface(planEvidence) || isNilInterface(observations) || isNilInterface(store) || verifier == nil {
		return nil, fmt.Errorf("%w: Plan/evidence resolver, authenticated observation resolver, store, and v3 verifier are required", ErrControlInvalid)
	}
	return &ControlService{planEvidence: planEvidence, observations: observations, store: store, verifier: verifier}, nil
}

func (service *ControlService) StartSnapshotSeal(ctx context.Context, command StartCommand) (StartOutcome, error) {
	return service.start(ctx, command, RequestKindSnapshotSeal)
}

func (service *ControlService) StartSnapshotVerification(ctx context.Context, command StartCommand) (StartOutcome, error) {
	return service.start(ctx, command, RequestKindSnapshotVerify)
}

// StartSigning atomically freezes the runner and release-approver rows. Only a
// fresh, definitely committed batch receives call ownership. Commit-unknown,
// exact replay, and a concurrent exact loser never do.
func (service *ControlService) StartSigning(ctx context.Context, command StartCommand) (StartOutcome, error) {
	return service.start(ctx, command, RequestKindReceiptSign)
}

func (service *ControlService) start(ctx context.Context, command StartCommand, kind RequestKind) (StartOutcome, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.planEvidence) || isNilInterface(service.store) {
		return StartOutcome{}, fmt.Errorf("%w: control service or dependencies are incomplete", ErrControlInvalid)
	}
	if err := ctx.Err(); err != nil {
		return StartOutcome{}, err
	}
	lookup := ControlLookup{AuthorityID: command.AuthorityID, OperationID: command.OperationID, Kind: kind}
	if err := validateControlLookup(lookup); err != nil {
		return StartOutcome{}, err
	}
	if existing, err := service.store.InspectAttempt(ctx, lookup); err == nil {
		if err := validateAttemptRecords(existing, lookup, true); err != nil {
			return StartOutcome{}, err
		}
		markRequestReplay(existing)
		return StartOutcome{Requests: cloneRequestRecords(existing), CallOwnership: false}, nil
	} else if !errors.Is(err, ErrControlNotFound) {
		return StartOutcome{}, fmt.Errorf("inspect control attempt: %w", err)
	}

	resolution, err := service.planEvidence.ResolveControl(ctx, lookup)
	if err != nil {
		return StartOutcome{}, fmt.Errorf("resolve server-built control request: %w", err)
	}
	candidates, err := recordsFromResolution(lookup, resolution)
	if err != nil {
		return StartOutcome{}, err
	}
	if err := service.validateStartPrerequisites(ctx, lookup, candidates); err != nil {
		return StartOutcome{}, err
	}
	stored, err := service.store.StartBatch(ctx, candidates)
	if errors.Is(err, ErrControlStoreOutcomeUnknown) {
		reconciled, inspectErr := service.store.InspectAttempt(ctx, lookup)
		if inspectErr != nil || !sameRequestBatch(reconciled, candidates, false) {
			return StartOutcome{}, ErrControlOutcomeUnknown
		}
		markRequestReplay(reconciled)
		return StartOutcome{Requests: cloneRequestRecords(reconciled), CallOwnership: false}, nil
	}
	if err != nil {
		if errors.Is(err, ErrControlConflict) {
			reconciled, inspectErr := service.store.InspectAttempt(ctx, lookup)
			if inspectErr == nil && sameRequestBatch(reconciled, candidates, false) {
				markRequestReplay(reconciled)
				return StartOutcome{Requests: cloneRequestRecords(reconciled), CallOwnership: false}, nil
			}
		}
		return StartOutcome{}, err
	}
	if !sameRequestBatch(stored.Requests, candidates, false) || validateAttemptRecords(stored.Requests, lookup, true) != nil {
		return StartOutcome{}, fmt.Errorf("%w: store returned different request bytes", ErrControlConflict)
	}
	return StartOutcome{Requests: cloneRequestRecords(stored.Requests), CallOwnership: stored.Created}, nil
}

func (service *ControlService) validateStartPrerequisites(ctx context.Context, lookup ControlLookup, candidates []RequestRecord) error {
	switch lookup.Kind {
	case RequestKindSnapshotSeal:
		return nil
	case RequestKindSnapshotVerify:
		seal, err := service.store.InspectAttempt(ctx, ControlLookup{AuthorityID: lookup.AuthorityID, OperationID: lookup.OperationID, Kind: RequestKindSnapshotSeal})
		if err != nil || len(seal) != 1 {
			return fmt.Errorf("%w: snapshot verification cannot start before snapshot seal is committed", ErrControlNotReady)
		}
		terminal, err := service.store.InspectTerminalObservation(ctx, seal[0].RequestHash)
		if err != nil || terminal.Status != ObservationCommitted || validateObservationRecord(terminal, seal[0], true) != nil {
			return fmt.Errorf("%w: exact snapshot-seal result is absent", ErrControlNotReady)
		}
		snapshot, err := decodeSnapshotResult(terminal)
		if err != nil || !sameBaseAnchors(seal[0].Request, candidates[0].Request) || candidates[0].Request.SnapshotDigest != snapshot.SnapshotDigest {
			return fmt.Errorf("%w: snapshot-verification request does not bind the committed seal result", ErrControlConflict)
		}
		return nil
	case RequestKindReceiptSign:
		receipt, err := DecodePayload(candidates[0].Payload)
		if err != nil || receipt.SchemaVersion != ReceiptSchemaV3 {
			return fmt.Errorf("%w: signing resolver did not return exact Receipt v3", ErrControlInvalid)
		}
		snapshotOperation, err := uuid.Parse(receipt.Snapshot.OperationID)
		if err != nil || snapshotOperation.Version() != 4 {
			return fmt.Errorf("%w: frozen Receipt snapshot operation is invalid", ErrControlInvalid)
		}
		seal, err := service.store.InspectAttempt(ctx, ControlLookup{AuthorityID: lookup.AuthorityID, OperationID: snapshotOperation, Kind: RequestKindSnapshotSeal})
		if err != nil || len(seal) != 1 {
			return fmt.Errorf("%w: signing cannot start before snapshot seal", ErrControlNotReady)
		}
		verification, err := service.store.InspectAttempt(ctx, ControlLookup{AuthorityID: lookup.AuthorityID, OperationID: snapshotOperation, Kind: RequestKindSnapshotVerify})
		if err != nil || len(verification) != 1 {
			return fmt.Errorf("%w: signing cannot start before read-only snapshot verification", ErrControlNotReady)
		}
		sealObservation, sealErr := service.store.InspectTerminalObservation(ctx, seal[0].RequestHash)
		verifyObservation, verifyErr := service.store.InspectTerminalObservation(ctx, verification[0].RequestHash)
		if sealErr != nil || verifyErr != nil || sealObservation.Status != ObservationCommitted || verifyObservation.Status != ObservationCommitted {
			return fmt.Errorf("%w: snapshot seal and independent verification must both be committed", ErrControlNotReady)
		}
		if validateObservationRecord(sealObservation, seal[0], true) != nil || validateObservationRecord(verifyObservation, verification[0], true) != nil {
			return fmt.Errorf("%w: snapshot observations lack authenticated exact closure", ErrControlConflict)
		}
		if !sameBaseAnchors(seal[0].Request, candidates[0].Request) || !sameBaseAnchors(verification[0].Request, candidates[0].Request) ||
			!operationalAuthoritiesMatchReceipt(seal[0], verification[0], candidates, receipt) ||
			resultsMatchReceipt(sealObservation, verifyObservation, receipt) != nil {
			return fmt.Errorf("%w: signing payload differs from committed snapshot evidence", ErrControlConflict)
		}
		return nil
	default:
		return ErrControlInvalid
	}
}

// Observe appends an authenticated pending or terminal observation. A pending
// observation after a not-invoked terminal is intentionally rejected here;
// only AcquireRetry may restore call ownership for the next generation.
func (service *ControlService) Observe(ctx context.Context, command ObservationCommand) (ObservationRecord, error) {
	request, candidate, err := service.resolveObservation(ctx, command)
	if err != nil {
		return ObservationRecord{}, err
	}
	if candidate.Status == ObservationPending {
		if terminal, inspectErr := service.store.InspectTerminalObservation(ctx, request.RequestHash); inspectErr == nil && terminal.Status == ObservationNotInvoked {
			return ObservationRecord{}, fmt.Errorf("%w: retry pending requires AcquireRetry", ErrControlConflict)
		}
	}
	stored, _, err := service.appendObservation(ctx, request, candidate)
	return stored, err
}

// AcquireRetry is the only transition that can return call ownership after an
// invocation was released. The previous generation must end in an exact,
// authenticated claim/ACK not-invoked observation. The next pending claim is
// appended first; only a definitely fresh commit returns ownership.
func (service *ControlService) AcquireRetry(ctx context.Context, command ObservationCommand) (RetryOutcome, error) {
	request, candidate, err := service.resolveObservation(ctx, command)
	if err != nil {
		return RetryOutcome{}, err
	}
	if candidate.Status != ObservationPending {
		return RetryOutcome{}, fmt.Errorf("%w: retry acquisition requires an authenticated pending claim", ErrControlInvalid)
	}
	if existing, inspectErr := service.store.InspectObservation(ctx, request.RequestHash, candidate.Sequence); inspectErr == nil {
		if !sameObservation(existing, candidate, false) || validateObservationRecord(existing, request, true) != nil {
			return RetryOutcome{}, fmt.Errorf("%w: retry sequence is bound to different bytes", ErrControlConflict)
		}
		existing.Idempotent = true
		return RetryOutcome{Observation: cloneObservation(existing), CallOwnership: false}, nil
	} else if !errors.Is(inspectErr, ErrControlNotFound) {
		return RetryOutcome{}, inspectErr
	}
	previous, err := service.store.InspectTerminalObservation(ctx, request.RequestHash)
	if err != nil || previous.Status != ObservationNotInvoked || validateObservationRecord(previous, request, true) != nil ||
		candidate.Generation != previous.Generation+1 || candidate.Sequence != previous.Sequence+1 {
		return RetryOutcome{}, fmt.Errorf("%w: prior generation lacks authenticated claim/ACK not-invoked closure", ErrControlConflict)
	}
	stored, created, err := service.appendObservation(ctx, request, candidate)
	if err != nil {
		return RetryOutcome{}, err
	}
	return RetryOutcome{Observation: stored, CallOwnership: created}, nil
}

func (service *ControlService) resolveObservation(ctx context.Context, command ObservationCommand) (RequestRecord, ObservationRecord, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.observations) || isNilInterface(service.store) {
		return RequestRecord{}, ObservationRecord{}, fmt.Errorf("%w: control service or dependencies are incomplete", ErrControlInvalid)
	}
	if err := ctx.Err(); err != nil {
		return RequestRecord{}, ObservationRecord{}, err
	}
	if err := validateRequestKey(command.Request); err != nil || command.ObservationAuthorityID.Version() != 4 {
		return RequestRecord{}, ObservationRecord{}, fmt.Errorf("%w: exact request key and opaque observation lookup are required", ErrControlInvalid)
	}
	request, err := service.store.InspectRequest(ctx, command.Request)
	if err != nil {
		return RequestRecord{}, ObservationRecord{}, fmt.Errorf("inspect request before observation: %w", err)
	}
	resolved, err := service.observations.ResolveObservation(ctx, ObservationLookup{
		ObservationAuthorityID: command.ObservationAuthorityID,
		RequestHash:            request.RequestHash,
	})
	if err != nil {
		return RequestRecord{}, ObservationRecord{}, fmt.Errorf("resolve authenticated observation: %w", err)
	}
	candidate := observationFromResolution(request, resolved)
	if err := validateObservationRecord(candidate, request, false); err != nil {
		return RequestRecord{}, ObservationRecord{}, err
	}
	return request, candidate, nil
}

func (service *ControlService) appendObservation(ctx context.Context, request RequestRecord, candidate ObservationRecord) (ObservationRecord, bool, error) {
	stored, err := service.store.AppendObservation(ctx, candidate)
	if errors.Is(err, ErrControlStoreOutcomeUnknown) {
		reconciled, inspectErr := service.store.InspectObservation(ctx, candidate.RequestHash, candidate.Sequence)
		if inspectErr != nil || !sameObservation(reconciled, candidate, false) || validateObservationRecord(reconciled, request, true) != nil {
			return ObservationRecord{}, false, ErrControlOutcomeUnknown
		}
		reconciled.Idempotent = true
		return cloneObservation(reconciled), false, nil
	}
	if err != nil {
		if errors.Is(err, ErrControlConflict) {
			reconciled, inspectErr := service.store.InspectObservation(ctx, candidate.RequestHash, candidate.Sequence)
			if inspectErr == nil && sameObservation(reconciled, candidate, false) && validateObservationRecord(reconciled, request, true) == nil {
				reconciled.Idempotent = true
				return cloneObservation(reconciled), false, nil
			}
		}
		return ObservationRecord{}, false, err
	}
	if !sameObservation(stored, candidate, false) || validateObservationRecord(stored, request, true) != nil {
		return ObservationRecord{}, false, fmt.Errorf("%w: store returned different observation bytes", ErrControlConflict)
	}
	return cloneObservation(stored), !stored.Idempotent, nil
}

// Complete reconstructs a canonical two-signature DSSE envelope from the
// committed raw signatures, verifies it with the existing keyful v3 Verifier
// and its independent ExpectedResolver, and then appends one terminal record.
func (service *ControlService) Complete(ctx context.Context, command CompletionCommand) (CompletionRecord, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.store) || service.verifier == nil {
		return CompletionRecord{}, fmt.Errorf("%w: control service or dependencies are incomplete", ErrControlInvalid)
	}
	if err := validateCompletionCommand(command); err != nil {
		return CompletionRecord{}, err
	}
	if existing, err := service.store.InspectCompletion(ctx, command.AuthorityID); err == nil {
		if validateStoredCompletion(existing) != nil || !completionMatchesCommand(existing, command) {
			return CompletionRecord{}, fmt.Errorf("%w: completion replay differs from immutable operations", ErrControlConflict)
		}
		existing.Idempotent = true
		return cloneCompletion(existing), nil
	} else if !errors.Is(err, ErrControlNotFound) {
		return CompletionRecord{}, err
	}

	seal, err := service.store.InspectAttempt(ctx, ControlLookup{command.AuthorityID, command.SnapshotOperationID, RequestKindSnapshotSeal})
	if err != nil || len(seal) != 1 {
		return CompletionRecord{}, fmt.Errorf("%w: exact snapshot-seal request is absent", ErrControlNotReady)
	}
	verification, err := service.store.InspectAttempt(ctx, ControlLookup{command.AuthorityID, command.SnapshotOperationID, RequestKindSnapshotVerify})
	if err != nil || len(verification) != 1 {
		return CompletionRecord{}, fmt.Errorf("%w: exact snapshot-verification request is absent", ErrControlNotReady)
	}
	signing, err := service.store.InspectAttempt(ctx, ControlLookup{command.AuthorityID, command.ReceiptSignOperationID, RequestKindReceiptSign})
	if err != nil || len(signing) != 2 || !samePayloadClosure(signing) {
		return CompletionRecord{}, fmt.Errorf("%w: two exact signing requests are absent", ErrControlNotReady)
	}
	all := []RequestRecord{seal[0], verification[0], signing[0], signing[1]}
	if !sameAnchorClosure(all) {
		return CompletionRecord{}, fmt.Errorf("%w: four requests do not share one Plan/evidence/snapshot authority", ErrControlConflict)
	}
	byRole := make(map[ControlRole]RequestRecord, 4)
	terminal := make(map[ControlRole]ObservationRecord, 4)
	for _, request := range all {
		byRole[request.Key.Role] = request
		observation, inspectErr := service.store.InspectTerminalObservation(ctx, request.RequestHash)
		if inspectErr != nil || observation.Status != ObservationCommitted || validateObservationRecord(observation, request, true) != nil {
			return CompletionRecord{}, fmt.Errorf("%w: %s/%s is not committed", ErrControlNotReady, request.Key.Kind, request.Key.Role)
		}
		terminal[request.Key.Role] = observation
	}
	receipt, err := DecodePayload(byRole[ControlRoleRunner].Payload)
	if err != nil || receipt.SchemaVersion != ReceiptSchemaV3 || !operationalAuthoritiesMatchReceipt(seal[0], verification[0], signing, receipt) || resultsMatchReceipt(
		terminal[ControlRoleSealer], terminal[ControlRoleVerifier], receipt,
	) != nil {
		return CompletionRecord{}, fmt.Errorf("%w: actual snapshot results differ from frozen v3 Receipt", ErrControlConflict)
	}
	envelope, err := buildControlDSSEEnvelope(
		byRole[ControlRoleRunner].Payload,
		terminal[ControlRoleRunner], terminal[ControlRoleReleaseApprover],
	)
	if err != nil {
		return CompletionRecord{}, err
	}
	verified, err := service.verifier.Verify(ctx, envelope, command.AuthorityID.String(), receipt.ReceiptID)
	if err != nil {
		return CompletionRecord{}, fmt.Errorf("verify reconstructed exact v3 DSSE envelope: %w", err)
	}
	if !bytes.Equal(verified.Payload, byRole[ControlRoleRunner].Payload) || !bytes.Equal(verified.CanonicalEnvelope, envelope) {
		return CompletionRecord{}, fmt.Errorf("%w: verifier output differs from frozen exact bytes", ErrControlConflict)
	}
	candidate := buildCompletionCandidate(command, byRole, terminal, envelope, receipt)
	candidate.verificationEnvelopeHash = verified.EnvelopeDigest
	stored, err := service.store.Complete(ctx, candidate)
	if errors.Is(err, ErrControlStoreOutcomeUnknown) {
		reconciled, inspectErr := service.store.InspectCompletion(ctx, command.AuthorityID)
		if inspectErr != nil || !sameCompletion(reconciled, candidate, false) || validateStoredCompletion(reconciled) != nil {
			return CompletionRecord{}, ErrControlOutcomeUnknown
		}
		reconciled.Idempotent = true
		return cloneCompletion(reconciled), nil
	}
	if err != nil {
		return CompletionRecord{}, err
	}
	if !sameCompletion(stored, candidate, false) || validateStoredCompletion(stored) != nil {
		return CompletionRecord{}, fmt.Errorf("%w: store returned different completion bytes", ErrControlConflict)
	}
	return cloneCompletion(stored), nil
}

func recordsFromResolution(lookup ControlLookup, resolution ControlResolution) ([]RequestRecord, error) {
	records := make([]RequestRecord, len(resolution.Requests))
	for index, resolved := range resolution.Requests {
		records[index] = RequestRecord{
			Key:     RequestKey{AuthorityID: lookup.AuthorityID, OperationID: lookup.OperationID, Kind: lookup.Kind, Role: resolved.Request.Role},
			Request: resolved.Request, RequestBytes: bytes.Clone(resolved.RequestBytes), RequestHash: resolved.RequestHash,
			Payload: bytes.Clone(resolved.Payload), PayloadHash: resolved.PayloadHash,
			PAE: bytes.Clone(resolved.PAE), PAEHash: resolved.PAEHash,
		}
	}
	if err := validateAttemptRecords(records, lookup, false); err != nil {
		return nil, err
	}
	return records, nil
}

func validateAttemptRecords(records []RequestRecord, lookup ControlLookup, stored bool) error {
	wantRoles := expectedControlRoles(lookup.Kind)
	if len(records) != len(wantRoles) {
		return fmt.Errorf("%w: %s requires exactly %d request rows", ErrControlInvalid, lookup.Kind, len(wantRoles))
	}
	seen := make(map[ControlRole]bool, len(records))
	for _, record := range records {
		if record.Key.AuthorityID != lookup.AuthorityID || record.Key.OperationID != lookup.OperationID || record.Key.Kind != lookup.Kind {
			return fmt.Errorf("%w: request lookup identity drift", ErrControlConflict)
		}
		if err := validateRequestRecord(record, stored); err != nil {
			return err
		}
		if seen[record.Key.Role] {
			return fmt.Errorf("%w: duplicate request role", ErrControlInvalid)
		}
		seen[record.Key.Role] = true
	}
	for _, role := range wantRoles {
		if !seen[role] {
			return fmt.Errorf("%w: required %s role is absent", ErrControlInvalid, role)
		}
	}
	if lookup.Kind == RequestKindReceiptSign && !samePayloadClosure(records) {
		return fmt.Errorf("%w: signing requests do not freeze byte-identical payload/PAE", ErrControlInvalid)
	}
	return nil
}

func validateRequestRecord(record RequestRecord, stored bool) error {
	if err := validateRequestKey(record.Key); err != nil {
		return err
	}
	request := record.Request
	if request.SchemaVersion != ControlRequestSchemaV1 || request.PlanAuthorityID != record.Key.AuthorityID.String() ||
		request.OperationID != record.Key.OperationID.String() || request.Kind != record.Key.Kind || request.Role != record.Key.Role ||
		!validStableID(request.OperationalAuthorityID) || !validStableID(request.AuthenticationKeyID) ||
		!validDigest(request.PlanAuthorityHash) || !validDigest(request.InputHash) || !validDigest(request.ProjectionHash) ||
		!validDigest(request.EvidencePlanHash) || !validDigest(request.TargetHash) || !validDigest(request.TrustHash) ||
		!validDigest(request.TrustBindingsDigest) || !validDigest(request.TrustPolicyDigest) ||
		request.EvidenceHeadVersion == 0 || request.EvidenceHeadVersion > uint64(maximumSafeInteger) ||
		!validUUIDv4(request.EvidenceLastEventID) || !validDigest(request.EvidenceLastEventHash) ||
		!validDigest(request.EvidenceCommandDigest) || !validDigest(request.EvidenceTrustDigest) ||
		!validDigest(request.EvidenceClosureDigest) || !validDigest(request.ArtifactIndexDigest) || !validStableID(request.SnapshotID) ||
		!validUUIDv4(request.OrchestrationID) {
		return fmt.Errorf("%w: request identity or Plan/evidence anchors are invalid", ErrControlInvalid)
	}
	requestBytes, err := CanonicalJSON(request)
	if err != nil || !bytes.Equal(requestBytes, record.RequestBytes) || record.RequestHash != SHA256Digest(record.RequestBytes) {
		return fmt.Errorf("%w: request raw bytes/hash closure is invalid", ErrControlInvalid)
	}
	switch record.Key.Kind {
	case RequestKindSnapshotSeal:
		if record.Key.Role != ControlRoleSealer || request.SnapshotDigest != "" || request.ReceiptID != "" ||
			request.PayloadDigest != "" || request.PAEDigest != "" || len(record.Payload) != 0 || record.PayloadHash != "" ||
			len(record.PAE) != 0 || record.PAEHash != "" || request.SignerIdentity != "" || request.SignerKeyID != "" {
			return fmt.Errorf("%w: snapshot-seal request is not role-closed", ErrControlInvalid)
		}
	case RequestKindSnapshotVerify:
		if record.Key.Role != ControlRoleVerifier || !validDigest(request.SnapshotDigest) || request.ReceiptID != "" ||
			request.PayloadDigest != "" || request.PAEDigest != "" || len(record.Payload) != 0 || record.PayloadHash != "" ||
			len(record.PAE) != 0 || record.PAEHash != "" || request.SignerIdentity != "" || request.SignerKeyID != "" {
			return fmt.Errorf("%w: snapshot-verification request is not role-closed", ErrControlInvalid)
		}
	case RequestKindReceiptSign:
		if !validDigest(request.SnapshotDigest) || !validStableID(request.ReceiptID) || !validDigest(request.PayloadDigest) ||
			!validDigest(request.PAEDigest) || !validIdentity(request.SignerIdentity) || !validStableID(request.SignerKeyID) ||
			request.AuthenticationKeyID != request.SignerKeyID {
			return fmt.Errorf("%w: receipt-sign request is not role-closed", ErrControlInvalid)
		}
		if err := validateSigningPayload(record); err != nil {
			return err
		}
	default:
		return ErrControlInvalid
	}
	if stored {
		if !validControlTime(record.StartedAt) {
			return fmt.Errorf("%w: StartedAt is not trusted UTC millisecond time", ErrControlConflict)
		}
	} else if !record.StartedAt.IsZero() {
		return fmt.Errorf("%w: resolver cannot assign StartedAt", ErrControlInvalid)
	}
	return nil
}

func validateSigningPayload(record RequestRecord) error {
	request := record.Request
	if record.PayloadHash != SHA256Digest(record.Payload) || request.PayloadDigest != record.PayloadHash {
		return fmt.Errorf("%w: expected payload raw bytes/hash closure is invalid", ErrControlInvalid)
	}
	receipt, err := DecodePayload(record.Payload)
	if err != nil || receipt.SchemaVersion != ReceiptSchemaV3 {
		return fmt.Errorf("%w: expected payload is not exact canonical Receipt v3: %v", ErrControlInvalid, err)
	}
	compiled, err := Compile(receipt)
	if err != nil || !bytes.Equal(compiled.Payload, record.Payload) || compiled.PayloadDigest != record.PayloadHash {
		return fmt.Errorf("%w: expected payload is not its exact compiled representation", ErrControlInvalid)
	}
	pae := templateauthority.DSSEPAE(InTotoPayloadType, record.Payload)
	if !bytes.Equal(pae, record.PAE) || record.PAEHash != SHA256Digest(record.PAE) || request.PAEDigest != record.PAEHash {
		return fmt.Errorf("%w: PAE raw bytes/hash closure is invalid", ErrControlInvalid)
	}
	authority := receipt.PlanAuthority
	if request.PlanAuthorityID != authority.AuthorityID || request.PlanAuthorityHash != authority.AuthorityHash ||
		request.InputHash != authority.InputHash || request.ProjectionHash != authority.ProjectionHash ||
		request.EvidencePlanHash != authority.EvidencePlanHash || request.TargetHash != authority.TargetHash ||
		request.TrustHash != authority.TrustHash || request.TrustBindingsDigest != authority.TrustBindingsDigest ||
		request.TrustPolicyDigest != receipt.Trust.TrustPolicyDigest || request.EvidenceClosureDigest != receipt.Evidence.ClosureDigest ||
		request.OrchestrationID != receipt.Evidence.OrchestrationID ||
		request.ArtifactIndexDigest != receipt.ArtifactIndex.ContentDigest || request.SnapshotID != receipt.Snapshot.SnapshotID ||
		request.SnapshotDigest != receipt.Snapshot.SnapshotDigest || request.ReceiptID != receipt.ReceiptID ||
		request.OperationID != receipt.OperationID || request.OperationalAuthorityID != receipt.Trust.TrustBindings.ReceiptAuthorityID {
		return fmt.Errorf("%w: signing request differs from v3 Receipt authority/evidence/snapshot anchors", ErrControlInvalid)
	}
	var signer SignerIdentityBinding
	if record.Key.Role == ControlRoleRunner {
		signer = receipt.Signers.Runner
	} else if record.Key.Role == ControlRoleReleaseApprover {
		signer = receipt.Signers.Approver
	} else {
		return ErrControlInvalid
	}
	if request.SignerIdentity != signer.Identity || request.SignerKeyID != signer.KeyID || string(record.Key.Role) != signer.Role {
		return fmt.Errorf("%w: signer identity/key/role differs from frozen Receipt", ErrControlInvalid)
	}
	return nil
}

func validateControlLookup(lookup ControlLookup) error {
	if lookup.AuthorityID.Version() != 4 || lookup.OperationID.Version() != 4 || len(expectedControlRoles(lookup.Kind)) == 0 {
		return fmt.Errorf("%w: opaque Plan Authority, operation, or request kind is invalid", ErrControlInvalid)
	}
	return nil
}

func validateRequestKey(key RequestKey) error {
	if key.AuthorityID.Version() != 4 || key.OperationID.Version() != 4 || !roleAllowedForKind(key.Kind, key.Role) {
		return fmt.Errorf("%w: request key is invalid", ErrControlInvalid)
	}
	return nil
}

func expectedControlRoles(kind RequestKind) []ControlRole {
	switch kind {
	case RequestKindSnapshotSeal:
		return []ControlRole{ControlRoleSealer}
	case RequestKindSnapshotVerify:
		return []ControlRole{ControlRoleVerifier}
	case RequestKindReceiptSign:
		return []ControlRole{ControlRoleRunner, ControlRoleReleaseApprover}
	default:
		return nil
	}
}

func roleAllowedForKind(kind RequestKind, role ControlRole) bool {
	for _, allowed := range expectedControlRoles(kind) {
		if role == allowed {
			return true
		}
	}
	return false
}

func sameBaseAnchors(left, right ControlRequest) bool {
	return left.PlanAuthorityID == right.PlanAuthorityID && left.PlanAuthorityHash == right.PlanAuthorityHash &&
		left.InputHash == right.InputHash && left.ProjectionHash == right.ProjectionHash && left.EvidencePlanHash == right.EvidencePlanHash &&
		left.TargetHash == right.TargetHash && left.TrustHash == right.TrustHash && left.TrustBindingsDigest == right.TrustBindingsDigest &&
		left.TrustPolicyDigest == right.TrustPolicyDigest && left.EvidenceHeadVersion == right.EvidenceHeadVersion &&
		left.EvidenceLastEventID == right.EvidenceLastEventID && left.EvidenceLastEventHash == right.EvidenceLastEventHash &&
		left.EvidenceCommandDigest == right.EvidenceCommandDigest && left.EvidenceTrustDigest == right.EvidenceTrustDigest &&
		left.EvidenceClosureDigest == right.EvidenceClosureDigest && left.ArtifactIndexDigest == right.ArtifactIndexDigest &&
		left.OrchestrationID == right.OrchestrationID && left.SnapshotID == right.SnapshotID
}

func sameAnchorClosure(records []RequestRecord) bool {
	if len(records) == 0 {
		return false
	}
	for _, record := range records[1:] {
		if !sameBaseAnchors(records[0].Request, record.Request) {
			return false
		}
	}
	return true
}

func samePayloadClosure(records []RequestRecord) bool {
	if len(records) != 2 || !sameBaseAnchors(records[0].Request, records[1].Request) {
		return false
	}
	return records[0].Request.SnapshotDigest == records[1].Request.SnapshotDigest &&
		records[0].Request.ReceiptID == records[1].Request.ReceiptID && records[0].PayloadHash == records[1].PayloadHash &&
		records[0].PAEHash == records[1].PAEHash && bytes.Equal(records[0].Payload, records[1].Payload) && bytes.Equal(records[0].PAE, records[1].PAE)
}

func observationFromResolution(request RequestRecord, resolved ResolvedObservation) ObservationRecord {
	record := ObservationRecord{
		RequestKey: request.Key, RequestHash: request.RequestHash,
		Generation: resolved.Generation, Sequence: resolved.Sequence, Status: resolved.Status, ObservedAt: resolved.ObservedAt,
		AuthenticationKeyID: resolved.AuthenticationKeyID, AuthenticationPayload: resolved.AuthenticationPayload,
		AuthenticationPayloadBytes: bytes.Clone(resolved.AuthenticationPayloadBytes), AuthenticationPayloadHash: resolved.AuthenticationPayloadHash,
		AuthenticationEnvelope: resolved.AuthenticationEnvelope,
		AuthenticationBytes:    bytes.Clone(resolved.AuthenticationBytes), AuthenticationEnvelopeHash: resolved.AuthenticationEnvelopeHash,
		Result: append(json.RawMessage(nil), resolved.Result...), ResultBytes: bytes.Clone(resolved.ResultBytes), ResultHash: resolved.ResultHash,
		Signature: bytes.Clone(resolved.Signature), SignatureHash: resolved.SignatureHash,
		Claim: resolved.Claim, ClaimBytes: bytes.Clone(resolved.ClaimBytes), ClaimTokenHash: resolved.ClaimTokenHash,
		Acknowledgement: resolved.Acknowledgement, AckBytes: bytes.Clone(resolved.AckBytes), AckTokenHash: resolved.AckTokenHash,
	}
	return record
}

func validateObservationRecord(record ObservationRecord, request RequestRecord, stored bool) error {
	if err := validateRequestRecord(request, true); err != nil {
		return err
	}
	if record.RequestKey != request.Key || record.RequestHash != request.RequestHash || record.Generation == 0 ||
		record.Generation > uint64(maximumSafeInteger) || record.Sequence == 0 || record.Sequence > uint64(maximumSafeInteger) ||
		!validControlTime(record.ObservedAt) ||
		record.AuthenticationKeyID != request.Request.AuthenticationKeyID {
		return fmt.Errorf("%w: observation identity, generation, sequence, time, or auth key is invalid", ErrControlInvalid)
	}
	payload := record.AuthenticationPayload
	if payload.SchemaVersion != ControlObservationPayloadSchemaV1 || payload.PlanAuthorityID != request.Key.AuthorityID.String() ||
		payload.OperationalAuthorityID != request.Request.OperationalAuthorityID || payload.OperationID != request.Key.OperationID.String() ||
		payload.Kind != request.Key.Kind || payload.Role != request.Key.Role || payload.Generation != record.Generation ||
		payload.RequestHash != request.RequestHash || payload.Sequence != record.Sequence || payload.Status != record.Status ||
		payload.ObservedAt != record.ObservedAt.UTC().Format(canonicalTimeLayout) || payload.AuthenticationKeyID != record.AuthenticationKeyID {
		return fmt.Errorf("%w: authenticated observation envelope binding drift", ErrControlInvalid)
	}
	payloadBytes, err := CanonicalJSON(payload)
	if err != nil || !bytes.Equal(payloadBytes, record.AuthenticationPayloadBytes) ||
		record.AuthenticationPayloadHash != SHA256Digest(record.AuthenticationPayloadBytes) {
		return fmt.Errorf("%w: authentication payload bytes/hash closure is invalid", ErrControlInvalid)
	}
	envelope := record.AuthenticationEnvelope
	if envelope.SchemaVersion != ControlObservationProofSchemaV1 || envelope.Algorithm != ControlAuthenticationEd25519 ||
		envelope.OperationalAuthorityID != request.Request.OperationalAuthorityID || envelope.KeyID != record.AuthenticationKeyID ||
		envelope.PayloadType != ControlObservationPayloadType {
		return fmt.Errorf("%w: authentication proof authority, algorithm, key, or payload type drift", ErrControlInvalid)
	}
	decodedPayload, decodeErr := base64.StdEncoding.Strict().DecodeString(envelope.Payload)
	decodedSignature, signatureErr := base64.StdEncoding.Strict().DecodeString(envelope.Signature)
	if decodeErr != nil || signatureErr != nil || base64.StdEncoding.EncodeToString(decodedPayload) != envelope.Payload ||
		base64.StdEncoding.EncodeToString(decodedSignature) != envelope.Signature || !bytes.Equal(decodedPayload, record.AuthenticationPayloadBytes) ||
		len(decodedSignature) != ed25519.SignatureSize {
		return fmt.Errorf("%w: authentication proof payload/signature encoding is invalid", ErrControlInvalid)
	}
	envelopeBytes, err := CanonicalJSON(envelope)
	if err != nil || !bytes.Equal(envelopeBytes, record.AuthenticationBytes) || record.AuthenticationEnvelopeHash != SHA256Digest(record.AuthenticationBytes) {
		return fmt.Errorf("%w: authenticated observation envelope bytes/hash closure is invalid", ErrControlInvalid)
	}
	switch record.Status {
	case ObservationPending:
		if hasTerminalMaterial(record) || payload.ResultHash != "" || payload.SignatureHash != "" ||
			payload.ClaimTokenHash != "" || payload.AcknowledgementTokenHash != "" {
			return fmt.Errorf("%w: pending observation carries terminal material", ErrControlInvalid)
		}
	case ObservationCommitted:
		if len(record.ClaimBytes) != 0 || len(record.AckBytes) != 0 || record.ClaimTokenHash != "" || record.AckTokenHash != "" ||
			record.Claim != (ClaimToken{}) || record.Acknowledgement != (AcknowledgementToken{}) ||
			payload.ClaimTokenHash != "" || payload.AcknowledgementTokenHash != "" {
			return fmt.Errorf("%w: committed observation carries recovery material", ErrControlInvalid)
		}
		if request.Key.Kind == RequestKindReceiptSign {
			if len(record.Signature) != ed25519.SignatureSize || record.SignatureHash != SHA256Digest(record.Signature) ||
				payload.SignatureHash != record.SignatureHash || len(record.Result) != 0 || len(record.ResultBytes) != 0 || record.ResultHash != "" || payload.ResultHash != "" {
				return fmt.Errorf("%w: signing commit lacks exact raw signature closure", ErrControlInvalid)
			}
		} else if len(record.Signature) != 0 || record.SignatureHash != "" || payload.SignatureHash != "" || validateCommittedResult(record, request, stored) != nil {
			return fmt.Errorf("%w: sealer/verifier commit lacks exact result closure", ErrControlInvalid)
		}
	case ObservationRejected:
		if hasTerminalMaterial(record) || payload.ResultHash != "" || payload.SignatureHash != "" ||
			payload.ClaimTokenHash != "" || payload.AcknowledgementTokenHash != "" {
			return fmt.Errorf("%w: rejected observation carries result or recovery material", ErrControlInvalid)
		}
	case ObservationNotInvoked:
		if len(record.Result) != 0 || len(record.ResultBytes) != 0 || record.ResultHash != "" || len(record.Signature) != 0 || record.SignatureHash != "" ||
			payload.ResultHash != "" || payload.SignatureHash != "" || validateNotInvokedClosure(record, request) != nil {
			return fmt.Errorf("%w: not-invoked lacks authenticated claim/ACK closure", ErrControlInvalid)
		}
	default:
		return fmt.Errorf("%w: observation status is invalid", ErrControlInvalid)
	}
	if record.RecordHash != controlObservationHash(record) {
		if stored || record.RecordHash != "" {
			return fmt.Errorf("%w: observation record hash closure is invalid", ErrControlInvalid)
		}
	}
	if stored {
		if !validControlTime(record.RecordedAt) || record.RecordedAt.Before(request.StartedAt) || record.RecordHash == "" {
			return fmt.Errorf("%w: observation ledger RecordedAt is invalid", ErrControlConflict)
		}
	} else if !record.RecordedAt.IsZero() || record.RecordHash != "" {
		return fmt.Errorf("%w: resolver cannot assign ledger RecordedAt or RecordHash", ErrControlInvalid)
	}
	return nil
}

func validateCommittedResult(record ObservationRecord, request RequestRecord, stored bool) error {
	if len(record.Result) == 0 || len(record.ResultBytes) == 0 || !bytes.Equal(record.Result, record.ResultBytes) ||
		record.ResultHash != SHA256Digest(record.ResultBytes) || record.AuthenticationPayload.ResultHash != record.ResultHash {
		return ErrControlInvalid
	}
	var value any
	if request.Key.Kind == RequestKindSnapshotSeal {
		value = new(PreReceiptSnapshotBinding)
	} else if request.Key.Kind == RequestKindSnapshotVerify {
		value = new(SnapshotVerificationBinding)
	} else {
		return ErrControlInvalid
	}
	if err := decodeStrictJSON(record.ResultBytes, value); err != nil {
		return err
	}
	canonical, err := CanonicalJSON(value)
	if err != nil || !bytes.Equal(canonical, record.ResultBytes) {
		return ErrControlInvalid
	}
	switch result := value.(type) {
	case *PreReceiptSnapshotBinding:
		sealedAt, timeErr := parseCanonicalTime(result.SealedAt, "sealedAt")
		if result.SchemaVersion != PreReceiptSnapshotSchemaV3 || result.SnapshotID != request.Request.SnapshotID ||
			result.OperationID != request.Key.OperationID.String() || result.AuthorityID != request.Request.OperationalAuthorityID ||
			result.EvidenceClosureDigest != request.Request.EvidenceClosureDigest || result.ArtifactIndexDigest != request.Request.ArtifactIndexDigest ||
			!validDigest(result.SnapshotDigest) || result.RequestDigest != request.RequestHash || result.Mode != ImmutableSnapshotMode ||
			result.Stage != AuthorityStageCommitted || timeErr != nil || sealedAt.Before(request.StartedAt) || sealedAt.After(record.ObservedAt) ||
			(stored && sealedAt.After(record.RecordedAt)) {
			return ErrControlInvalid
		}
	case *SnapshotVerificationBinding:
		verifiedAt, timeErr := parseCanonicalTime(result.VerifiedAt, "verifiedAt")
		if result.SchemaVersion != SnapshotVerificationSchemaV3 || result.SnapshotID != request.Request.SnapshotID ||
			result.SnapshotDigest != request.Request.SnapshotDigest || result.AuthorityID != request.Request.OperationalAuthorityID ||
			result.EvidenceClosureDigest != request.Request.EvidenceClosureDigest || result.ArtifactIndexDigest != request.Request.ArtifactIndexDigest ||
			result.Result != VerificationPassed || timeErr != nil || verifiedAt.Before(request.StartedAt) || verifiedAt.After(record.ObservedAt) ||
			(stored && verifiedAt.After(record.RecordedAt)) {
			return ErrControlInvalid
		}
	}
	return nil
}

func validateNotInvokedClosure(record ObservationRecord, request RequestRecord) error {
	claimBytes, err := CanonicalJSON(record.Claim)
	if err != nil || !bytes.Equal(claimBytes, record.ClaimBytes) || record.ClaimTokenHash != SHA256Digest(record.ClaimBytes) {
		return ErrControlInvalid
	}
	claim := record.Claim
	if claim.SchemaVersion != ControlClaimTokenSchemaV1 || claim.PlanAuthorityID != request.Key.AuthorityID.String() ||
		claim.OperationalAuthorityID != request.Request.OperationalAuthorityID || claim.OperationID != request.Key.OperationID.String() ||
		claim.Kind != request.Key.Kind || claim.Role != request.Key.Role || claim.Generation != record.Generation ||
		claim.RequestHash != request.RequestHash || !validUUIDv4(claim.ClaimID) || !validDigest(claim.PendingEnvelopeHash) {
		return ErrControlInvalid
	}
	ackBytes, err := CanonicalJSON(record.Acknowledgement)
	if err != nil || !bytes.Equal(ackBytes, record.AckBytes) || record.AckTokenHash != SHA256Digest(record.AckBytes) {
		return ErrControlInvalid
	}
	ack := record.Acknowledgement
	if ack.SchemaVersion != ControlAcknowledgementSchemaV1 || !validUUIDv4(ack.AcknowledgementID) || ack.RequestHash != request.RequestHash ||
		ack.AcknowledgementID == claim.ClaimID || ack.ClaimTokenHash != record.ClaimTokenHash || ack.Status != ObservationNotInvoked ||
		record.AuthenticationPayload.ClaimTokenHash != record.ClaimTokenHash ||
		record.AuthenticationPayload.AcknowledgementTokenHash != record.AckTokenHash {
		return ErrControlInvalid
	}
	return nil
}

func hasTerminalMaterial(record ObservationRecord) bool {
	return len(record.Result) != 0 || len(record.ResultBytes) != 0 || record.ResultHash != "" || len(record.Signature) != 0 || record.SignatureHash != "" ||
		record.Claim != (ClaimToken{}) || len(record.ClaimBytes) != 0 || record.ClaimTokenHash != "" ||
		record.Acknowledgement != (AcknowledgementToken{}) || len(record.AckBytes) != 0 || record.AckTokenHash != ""
}

type controlObservationProjection struct {
	AcknowledgementTokenHash   string            `json:"acknowledgementTokenHash"`
	AuthenticationEnvelopeHash string            `json:"authenticationEnvelopeHash"`
	AuthenticationKeyID        string            `json:"authenticationKeyId"`
	AuthenticationPayloadHash  string            `json:"authenticationPayloadHash"`
	Generation                 uint64            `json:"generation"`
	ObservedAt                 string            `json:"observedAt"`
	RecordedAt                 string            `json:"recordedAt"`
	RequestHash                string            `json:"requestHash"`
	ResultHash                 string            `json:"resultHash"`
	Sequence                   uint64            `json:"sequence"`
	SignatureHash              string            `json:"signatureHash"`
	ClaimTokenHash             string            `json:"claimTokenHash"`
	Status                     ObservationStatus `json:"status"`
}

func controlObservationHash(record ObservationRecord) string {
	encoded, err := CanonicalJSON(controlObservationProjection{
		AcknowledgementTokenHash: record.AckTokenHash, AuthenticationEnvelopeHash: record.AuthenticationEnvelopeHash,
		AuthenticationKeyID: record.AuthenticationKeyID, AuthenticationPayloadHash: record.AuthenticationPayloadHash, Generation: record.Generation,
		ObservedAt: record.ObservedAt.UTC().Format(canonicalTimeLayout), RequestHash: record.RequestHash,
		RecordedAt: record.RecordedAt.UTC().Format(canonicalTimeLayout),
		ResultHash: record.ResultHash, Sequence: record.Sequence, SignatureHash: record.SignatureHash,
		ClaimTokenHash: record.ClaimTokenHash, Status: record.Status,
	})
	if err != nil {
		return ""
	}
	return SHA256Digest(encoded)
}

func decodeSnapshotResult(observation ObservationRecord) (PreReceiptSnapshotBinding, error) {
	var result PreReceiptSnapshotBinding
	if err := decodeStrictJSON(observation.ResultBytes, &result); err != nil {
		return result, err
	}
	return result, nil
}

func resultsMatchReceipt(seal, verification ObservationRecord, receipt Receipt) error {
	var snapshot PreReceiptSnapshotBinding
	var verified SnapshotVerificationBinding
	if decodeStrictJSON(seal.ResultBytes, &snapshot) != nil || decodeStrictJSON(verification.ResultBytes, &verified) != nil {
		return ErrControlInvalid
	}
	snapshotBytes, _ := CanonicalJSON(receipt.Snapshot)
	verificationBytes, _ := CanonicalJSON(receipt.SnapshotVerification)
	if !bytes.Equal(snapshotBytes, seal.ResultBytes) || !bytes.Equal(verificationBytes, verification.ResultBytes) ||
		snapshot.SnapshotDigest != verified.SnapshotDigest || snapshot.SnapshotID != verified.SnapshotID {
		return ErrControlConflict
	}
	return nil
}

func operationalAuthoritiesMatchReceipt(seal, verification RequestRecord, signing []RequestRecord, receipt Receipt) bool {
	if len(signing) != 2 || seal.Request.OperationalAuthorityID != receipt.Trust.TrustBindings.SealerAuthorityID ||
		verification.Request.OperationalAuthorityID != receipt.Trust.TrustBindings.VerifierAuthorityID {
		return false
	}
	for _, request := range signing {
		if request.Request.OperationalAuthorityID != receipt.Trust.TrustBindings.ReceiptAuthorityID {
			return false
		}
	}
	return true
}

func buildControlDSSEEnvelope(payload []byte, observations ...ObservationRecord) ([]byte, error) {
	type signature struct {
		KeyID string `json:"keyid"`
		Sig   string `json:"sig"`
	}
	type envelope struct {
		PayloadType string      `json:"payloadType"`
		Payload     string      `json:"payload"`
		Signatures  []signature `json:"signatures"`
	}
	if len(observations) != 2 {
		return nil, ErrControlNotReady
	}
	signatures := make([]signature, 0, 2)
	for _, observation := range observations {
		if observation.Status != ObservationCommitted || len(observation.Signature) != ed25519.SignatureSize {
			return nil, ErrControlNotReady
		}
		signatures = append(signatures, signature{KeyID: observation.AuthenticationKeyID, Sig: base64.StdEncoding.EncodeToString(observation.Signature)})
	}
	sort.Slice(signatures, func(i, j int) bool { return signatures[i].KeyID < signatures[j].KeyID })
	if signatures[0].KeyID == signatures[1].KeyID {
		return nil, fmt.Errorf("%w: signer keys are not independent", ErrControlInvalid)
	}
	return json.Marshal(envelope{PayloadType: InTotoPayloadType, Payload: base64.StdEncoding.EncodeToString(payload), Signatures: signatures})
}

func buildCompletionCandidate(command CompletionCommand, requests map[ControlRole]RequestRecord, observations map[ControlRole]ObservationRecord, envelope []byte, receipt Receipt) CompletionRecord {
	runner := requests[ControlRoleRunner]
	return CompletionRecord{
		AuthorityID: command.AuthorityID, ReceiptID: receipt.ReceiptID,
		PlanAuthorityHash: runner.Request.PlanAuthorityHash, EvidenceClosureDigest: runner.Request.EvidenceClosureDigest,
		SnapshotID: runner.Request.SnapshotID, SnapshotDigest: runner.Request.SnapshotDigest,
		Operations: CompletionOperations{ReceiptSign: command.ReceiptSignOperationID.String(), Snapshot: command.SnapshotOperationID.String()},
		RequestHashes: CompletionRequestHashes{
			SnapshotSeal: requests[ControlRoleSealer].RequestHash, SnapshotVerify: requests[ControlRoleVerifier].RequestHash,
			RunnerSign: requests[ControlRoleRunner].RequestHash, ApproverSign: requests[ControlRoleReleaseApprover].RequestHash,
		},
		ObservationHashes: CompletionObservationHashes{
			SnapshotSeal: observations[ControlRoleSealer].RecordHash, SnapshotVerify: observations[ControlRoleVerifier].RecordHash,
			RunnerSign: observations[ControlRoleRunner].RecordHash, ApproverSign: observations[ControlRoleReleaseApprover].RecordHash,
		},
		Payload: bytes.Clone(runner.Payload), PayloadDigest: runner.PayloadHash,
		PAE: bytes.Clone(runner.PAE), PAEDigest: runner.PAEHash,
		Envelope: bytes.Clone(envelope), EnvelopeDigest: SHA256Digest(envelope),
	}
}

func validateCompletionCommand(command CompletionCommand) error {
	if command.AuthorityID.Version() != 4 || command.SnapshotOperationID.Version() != 4 || command.ReceiptSignOperationID.Version() != 4 ||
		command.SnapshotOperationID == command.ReceiptSignOperationID {
		return fmt.Errorf("%w: Plan Authority and its distinct snapshot/Receipt-sign operations are required", ErrControlInvalid)
	}
	return nil
}

func completionMatchesCommand(record CompletionRecord, command CompletionCommand) bool {
	return record.AuthorityID == command.AuthorityID && record.Operations == (CompletionOperations{
		ReceiptSign: command.ReceiptSignOperationID.String(), Snapshot: command.SnapshotOperationID.String(),
	})
}

func validateCompletionCandidate(record CompletionRecord) error {
	if record.AuthorityID.Version() != 4 || !validStableID(record.ReceiptID) || !validDigest(record.PlanAuthorityHash) ||
		!validDigest(record.EvidenceClosureDigest) || !validStableID(record.SnapshotID) || !validDigest(record.SnapshotDigest) ||
		record.PayloadDigest != SHA256Digest(record.Payload) || record.PAEDigest != SHA256Digest(record.PAE) ||
		record.EnvelopeDigest != SHA256Digest(record.Envelope) || record.verificationEnvelopeHash != record.EnvelopeDigest ||
		!bytes.Equal(record.PAE, templateauthority.DSSEPAE(InTotoPayloadType, record.Payload)) {
		return fmt.Errorf("%w: completion byte/hash or anchor closure is invalid", ErrControlInvalid)
	}
	receipt, err := DecodePayload(record.Payload)
	if err != nil || receipt.SchemaVersion != ReceiptSchemaV3 || receipt.PlanAuthority.AuthorityID != record.AuthorityID.String() ||
		receipt.PlanAuthority.AuthorityHash != record.PlanAuthorityHash || receipt.ReceiptID != record.ReceiptID ||
		receipt.Evidence.ClosureDigest != record.EvidenceClosureDigest || receipt.Snapshot.SnapshotID != record.SnapshotID ||
		receipt.Snapshot.SnapshotDigest != record.SnapshotDigest {
		return fmt.Errorf("%w: completion is not exact Receipt v3", ErrControlInvalid)
	}
	for _, digest := range []string{
		record.RequestHashes.SnapshotSeal, record.RequestHashes.SnapshotVerify, record.RequestHashes.RunnerSign, record.RequestHashes.ApproverSign,
		record.ObservationHashes.SnapshotSeal, record.ObservationHashes.SnapshotVerify, record.ObservationHashes.RunnerSign, record.ObservationHashes.ApproverSign,
	} {
		if !validDigest(digest) {
			return fmt.Errorf("%w: completion source closure is incomplete", ErrControlInvalid)
		}
	}
	if !validUUIDv4(record.Operations.Snapshot) || !validUUIDv4(record.Operations.ReceiptSign) || record.Operations.Snapshot == record.Operations.ReceiptSign {
		return fmt.Errorf("%w: completion operation closure is invalid", ErrControlInvalid)
	}
	if !record.CompletedAt.IsZero() || len(record.DocumentBytes) != 0 || record.DocumentHash != "" {
		return fmt.Errorf("%w: caller cannot assign completion time/document", ErrControlInvalid)
	}
	return nil
}

func validateStoredCompletion(record CompletionRecord) error {
	core := cloneCompletion(record)
	core.CompletedAt = time.Time{}
	core.Document = CompletionDocument{}
	core.DocumentBytes = nil
	core.DocumentHash = ""
	core.Idempotent = false
	if err := validateCompletionCandidate(core); err != nil {
		return err
	}
	if !validControlTime(record.CompletedAt) {
		return fmt.Errorf("%w: completion time is invalid", ErrControlConflict)
	}
	want := completionDocument(record, record.CompletedAt)
	encoded, err := CanonicalJSON(want)
	if err != nil || record.Document != want || !bytes.Equal(encoded, record.DocumentBytes) || record.DocumentHash != SHA256Digest(encoded) {
		return fmt.Errorf("%w: completion document closure is invalid", ErrControlConflict)
	}
	return nil
}

func completionDocument(record CompletionRecord, completedAt time.Time) CompletionDocument {
	return CompletionDocument{
		PlanAuthorityID: record.AuthorityID.String(), PlanAuthorityHash: record.PlanAuthorityHash,
		CompletedAt: completedAt.UTC().Format(canonicalTimeLayout), EnvelopeHash: record.EnvelopeDigest,
		EvidenceClosureDigest: record.EvidenceClosureDigest, ObservationHashes: record.ObservationHashes,
		Operations: record.Operations, PAEDigest: record.PAEDigest, PayloadDigest: record.PayloadDigest,
		ReceiptID: record.ReceiptID, RequestHashes: record.RequestHashes, SchemaVersion: ControlCompletionSchemaV1,
		SnapshotDigest: record.SnapshotDigest, SnapshotID: record.SnapshotID,
	}
}

func validControlTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Nanosecond()%int(time.Millisecond) == 0
}
