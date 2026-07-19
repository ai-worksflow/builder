package modelgovernance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"
)

const (
	ActivationAuthorityKind = "activation"
	GenesisAuthorityKind    = "genesis"
)

// ActivationRequest is an idempotent request to move one workload head. The
// operation ID may be retried only with the exact same request hash.
type ActivationRequest struct {
	OperationID        string
	ReceiptDigest      string
	ExpectedGeneration uint64
	ExpectedFence      string
}

// ActivationRecord is the immutable registry projection of one fully verified
// ModelGovernanceReceipt. ReceiptDigest addresses the DSSE envelope, not only
// its payload.
type ActivationRecord struct {
	AuthorityKind                   string
	OperationID                     string
	RequestHash                     string
	Workload                        string
	ProfileID                       string
	ProfileContentHash              string
	ReceiptDigest                   string
	ReceiptPayloadDigest            string
	ActivationEnvelopeDigest        string
	ActivationPayloadDigest         string
	PreviousGeneration              uint64
	Generation                      uint64
	PreviousFence                   string
	Fence                           string
	CorpusContentHash               string
	ProviderRouteAuthorityHash      string
	RunnerImmutableDigest           string
	SourceTreeDigest                string
	TrustPolicyHash                 string
	GenesisEnvelopeDigest           string
	GenesisPayloadDigest            string
	InitialRevocationAuthorityID    string
	InitialRevocationAuthorityHash  string
	InitialRevocationAuthorityEpoch uint64
	ActivatedAt                     time.Time
}

// ActivationAppend is deliberately accepted only by an ActivationStore. The
// store enforces structural CAS and idempotency; ActivationService is the
// authority boundary that verifies signatures and all referenced materials.
type ActivationAppend struct {
	ExpectedGeneration uint64
	ExpectedFence      string
	Record             ActivationRecord
}

// ActivationStore is append-only. Implementations must atomically persist the
// operation outcome, immutable history row, exact-profile index, and workload
// head. They must never overwrite an existing operation or history row, and an
// exact-profile index entry is insert-only: a second receipt for the same
// (workload, profile ID, content hash) must fail closed. Empty-head creation is
// available only through the separate GenesisBootstrapStore authority.
// Observation methods durably bind cumulative revocation and current trust to
// one exact epoch/hash, rejecting rollback and same-epoch equivocation.
type ActivationStore interface {
	TrustedTime(context.Context) (time.Time, error)
	AppendActivation(context.Context, ActivationAppend) (ActivationRecord, error)
	GetActivationOperation(context.Context, string) (ActivationRecord, error)
	GetActiveActivation(context.Context, string) (ActivationRecord, error)
	GetActivationGeneration(context.Context, string, uint64) (ActivationRecord, error)
	GetActivatedProfile(context.Context, CorpusProfileBinding) (ActivationRecord, error)
	ObserveGovernanceRevocationAuthority(context.Context, GovernanceRevocationAuthority) error
	ObserveGovernanceTrustPolicy(context.Context, GovernanceTrustPolicyObservation) error
}

type GovernanceTrustPolicyObservation struct {
	PolicyHash              string
	RevocationAuthorityHash string
	RevocationEpoch         uint64
}

// RuntimeDisableQuery binds a disable lookup to one exact activated receipt.
// A state for a different generation, fence, or receipt is never reusable.
type RuntimeDisableQuery struct {
	Workload           string
	ProfileID          string
	ProfileContentHash string
	ReceiptDigest      string
	Generation         uint64
	Fence              string
}

// ProfileDisableState is a short-lived, authoritative answer. An empty but
// non-null ActiveConditions array means no declared disable condition is
// currently active. A missing, stale, or mismatched answer fails closed.
type ProfileDisableState struct {
	Query            RuntimeDisableQuery
	ActiveConditions []string
	CheckedAt        time.Time
	ExpiresAt        time.Time
}

// GovernanceAuthority supplies exact immutable bytes, a receipt-addressed
// signer-policy registry, and current revocation/route/disable authorities. No
// method is allowed to synthesize missing materials or discard historical
// signer policies still referenced by an activation record.
type GovernanceAuthority interface {
	LoadGovernanceMaterials(context.Context, string) (GovernanceMaterials, error)
	LoadGenesisGovernanceMaterials(context.Context, string) (GenesisGovernanceMaterials, error)
	LoadGovernanceTrustPolicy(context.Context, string) (GovernanceTrustPolicy, error)
	CurrentProviderRouteAuthority(context.Context, string) ([]byte, error)
	CurrentGovernanceTrustPolicy(context.Context) (GovernanceTrustPolicy, error)
	CurrentGovernanceRevocationAuthority(context.Context) (GovernanceRevocationAuthority, error)
	CurrentProfileDisableState(context.Context, RuntimeDisableQuery) (ProfileDisableState, error)
}

type ResolvedGovernanceNode struct {
	Record   ActivationRecord
	Verified VerifiedGovernance
}

// ResolvedActive is safe to hand to a runtime only for the current call. It is
// not a cacheable authorization token: callers must invoke ResolveActive again
// before every provider execution. The authorities do not yet share one atomic
// epoch, so the data plane must also recheck/consume the exact observations at
// execution time; this value alone cannot close a post-return authority race.
type ResolvedActive struct {
	Primary                   ActivationRecord
	Graph                     []ResolvedGovernanceNode
	AuthorityObservedAt       time.Time
	GovernanceRevocationEpoch uint64
	GovernanceRevocationHash  string
}

type ActivationService struct {
	store     ActivationStore
	authority GovernanceAuthority
	verifier  *GovernanceVerifier
}

func NewActivationService(store ActivationStore, authority GovernanceAuthority, verifier *GovernanceVerifier) (*ActivationService, error) {
	if nilGovernanceDependency(store) || nilGovernanceDependency(authority) || verifier == nil {
		return nil, fmt.Errorf("%w: activation store, authority, and verifier are required", ErrGovernanceInvalid)
	}
	return &ActivationService{store: store, authority: authority, verifier: verifier}, nil
}

// Activate verifies the candidate, the exact current shadow baseline, and the
// complete active fallback graph before one CAS append. This ordinary path
// never creates an empty head; generation one must already have been committed
// through the distinct signed GenesisBootstrapService authority.
func (service *ActivationService) Activate(ctx context.Context, request ActivationRequest) (ActivationRecord, error) {
	if service == nil || nilGovernanceDependency(service.store) || nilGovernanceDependency(service.authority) || service.verifier == nil || nilGovernanceDependency(ctx) {
		return ActivationRecord{}, fmt.Errorf("%w: activation service and non-nil context are required", ErrGovernanceInvalid)
	}
	requestHash, err := activationRequestHash(request)
	if err != nil {
		return ActivationRecord{}, err
	}
	var priorOperation *ActivationRecord
	if existing, inspectErr := service.store.GetActivationOperation(ctx, request.OperationID); inspectErr == nil {
		if existing.RequestHash != requestHash {
			return ActivationRecord{}, fmt.Errorf("%w: operation id was already used for different bytes", ErrActivationConflict)
		}
		priorOperation = &existing
	} else if !errors.Is(inspectErr, ErrActivationNotFound) {
		return ActivationRecord{}, inspectErr
	}

	if priorOperation != nil {
		candidate, verifyErr := service.verifyHistoricalRecord(ctx, *priorOperation)
		if verifyErr != nil {
			return ActivationRecord{}, verifyErr
		}
		if priorOperation.OperationID != request.OperationID || !recordMatchesVerified(*priorOperation, candidate) ||
			validateActivationAppend(ActivationAppend{ExpectedGeneration: priorOperation.PreviousGeneration, ExpectedFence: priorOperation.PreviousFence, Record: *priorOperation}) != nil {
			return ActivationRecord{}, fmt.Errorf("%w: stored operation outcome differs from the exact verified receipt", ErrActivationConflict)
		}
		return service.confirmActivationOutcome(ctx, *priorOperation, priorOperation)
	}

	now, policy, revocations, candidate, err := service.verifyReceipt(ctx, request.ReceiptDigest)
	if err != nil {
		return ActivationRecord{}, err
	}
	if candidate.Activation.PreviousGeneration != request.ExpectedGeneration || candidate.Activation.PreviousFence != request.ExpectedFence {
		return ActivationRecord{}, fmt.Errorf("%w: signed activation predecessor differs from the CAS request", ErrActivationConflict)
	}

	baselineRecord, err := service.store.GetActiveActivation(ctx, candidate.Profile.Workload)
	if errors.Is(err, ErrActivationNotFound) {
		return ActivationRecord{}, fmt.Errorf("%w: ordinary activation requires a predecessor committed through signed Genesis bootstrap", ErrRuntimeAuthority)
	}
	if err != nil {
		return ActivationRecord{}, err
	}
	if !recordIsExactPredecessor(baselineRecord, request, candidate.Shadow.Baseline) {
		return ActivationRecord{}, fmt.Errorf("%w: shadow baseline is not the exact current activated predecessor", ErrActivationConflict)
	}
	historicalBaseline, err := service.store.GetActivationGeneration(ctx, baselineRecord.Workload, baselineRecord.Generation)
	if err != nil || !sameActivationRecord(historicalBaseline, baselineRecord) {
		return ActivationRecord{}, fmt.Errorf("%w: shadow baseline has no exact immutable activation history", ErrRuntimeAuthority)
	}
	baselineGraph, err := service.resolveGraph(ctx, baselineRecord, nil, revocations, now, false)
	if err != nil {
		return ActivationRecord{}, fmt.Errorf("%w: current shadow baseline cannot be revalidated: %v", ErrRuntimeAuthority, err)
	}
	if err := service.requireGraphDependenciesStable(ctx, baselineGraph, "", now, false); err != nil {
		return ActivationRecord{}, err
	}

	record := activationRecordFromVerified(request, requestHash, candidate, now)
	if err := service.requireProfileEnabled(ctx, record, candidate.Profile, now); err != nil {
		return ActivationRecord{}, err
	}
	candidateGraph, err := service.resolveGraph(ctx, record, &candidate, revocations, now, true)
	if err != nil {
		return ActivationRecord{}, err
	}
	if err := service.requireGraphDependenciesStable(ctx, candidateGraph, record.ReceiptDigest, now, true); err != nil {
		return ActivationRecord{}, err
	}
	if err := service.requireAuthorityStable(ctx, policy.PolicyHash, revocations, now); err != nil {
		return ActivationRecord{}, err
	}

	stored, err := service.store.AppendActivation(ctx, ActivationAppend{
		ExpectedGeneration: request.ExpectedGeneration,
		ExpectedFence:      request.ExpectedFence,
		Record:             record,
	})
	if err == nil {
		if !sameActivationRecord(stored, record) {
			return ActivationRecord{}, fmt.Errorf("%w: activation store returned bytes different from the requested append", ErrActivationConflict)
		}
		return service.confirmActivationOutcome(ctx, record, &stored)
	}
	if !errors.Is(err, ErrActivationOutcomeUnknown) {
		return ActivationRecord{}, err
	}
	inspected, inspectErr := service.store.GetActivationOperation(ctx, request.OperationID)
	if inspectErr == nil {
		if inspected.RequestHash != requestHash || inspected.OperationID != request.OperationID || !recordMatchesVerified(inspected, candidate) ||
			validateActivationAppend(ActivationAppend{ExpectedGeneration: inspected.PreviousGeneration, ExpectedFence: inspected.PreviousFence, Record: inspected}) != nil {
			return ActivationRecord{}, fmt.Errorf("%w: unknown operation resolved to different bytes", ErrActivationConflict)
		}
		if !sameActivationRecord(inspected, record) {
			return ActivationRecord{}, fmt.Errorf("%w: unknown operation did not persist the exact requested activation bytes", ErrActivationConflict)
		}
		return service.confirmActivationOutcome(ctx, record, &inspected)
	}
	if errors.Is(inspectErr, ErrActivationNotFound) {
		return ActivationRecord{}, ErrActivationOutcomeUnknown
	}
	return ActivationRecord{}, fmt.Errorf("%w: inspect operation: %v", ErrActivationOutcomeUnknown, inspectErr)
}

// ResolveActive performs a fresh full signature/material/trust/revocation/time,
// disable-state, provider-route, and fallback-graph verification on every call.
// It double-reads the workload head and other dependencies to detect observed
// drift. Those authorities do not yet share an atomic epoch, so this method does
// not claim to eliminate changes after the closing reads.
func (service *ActivationService) ResolveActive(ctx context.Context, workload string) (ResolvedActive, error) {
	if service == nil || nilGovernanceDependency(service.store) || nilGovernanceDependency(service.authority) || service.verifier == nil || nilGovernanceDependency(ctx) || !validStableID(workload) {
		return ResolvedActive{}, fmt.Errorf("%w: workload is invalid", ErrRuntimeAuthority)
	}
	opening, err := service.store.GetActiveActivation(ctx, workload)
	if err != nil {
		return ResolvedActive{}, fmt.Errorf("%w: load active head: %v", ErrRuntimeAuthority, err)
	}
	now, err := service.store.TrustedTime(ctx)
	if err != nil {
		return ResolvedActive{}, fmt.Errorf("%w: trusted time is unavailable", ErrRuntimeAuthority)
	}
	now, err = normalizeGovernanceTrustedTime(now)
	if err != nil {
		return ResolvedActive{}, fmt.Errorf("%w: trusted time is unavailable", ErrRuntimeAuthority)
	}
	revocations, err := service.loadCurrentRevocationAuthority(ctx, now)
	if err != nil {
		return ResolvedActive{}, err
	}
	currentPolicy, err := service.authority.CurrentGovernanceTrustPolicy(ctx)
	if err != nil || ValidateGovernanceTrustPolicy(currentPolicy) != nil {
		return ResolvedActive{}, fmt.Errorf("%w: current trust policy is unavailable", ErrRuntimeAuthority)
	}
	if err := service.observeCurrentTrustPolicy(ctx, currentPolicy, revocations); err != nil {
		return ResolvedActive{}, err
	}
	graph, err := service.resolveGraph(ctx, opening, nil, revocations, now, true)
	if err != nil {
		return ResolvedActive{}, fmt.Errorf("%w: %w", ErrRuntimeAuthority, err)
	}
	if err := service.requireGraphDependenciesStable(ctx, graph, "", now, true); err != nil {
		return ResolvedActive{}, fmt.Errorf("%w: %w", ErrRuntimeAuthority, err)
	}
	if err := service.requireAuthorityStable(ctx, currentPolicy.PolicyHash, revocations, now); err != nil {
		return ResolvedActive{}, fmt.Errorf("%w: %w", ErrRuntimeAuthority, err)
	}
	closing, err := service.store.GetActiveActivation(ctx, workload)
	if err != nil || !sameActivationRecord(opening, closing) {
		return ResolvedActive{}, fmt.Errorf("%w: active head changed during resolution", ErrRuntimeAuthority)
	}
	return ResolvedActive{
		Primary: opening, Graph: graph, AuthorityObservedAt: now,
		GovernanceRevocationEpoch: revocations.Epoch, GovernanceRevocationHash: revocations.AuthorityHash,
	}, nil
}

func (service *ActivationService) verifyReceipt(ctx context.Context, receiptDigest string) (time.Time, GovernanceTrustPolicy, GovernanceRevocationAuthority, VerifiedGovernance, error) {
	if service == nil || !validDigest(receiptDigest) {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, fmt.Errorf("%w: receipt digest is invalid", ErrGovernanceInvalid)
	}
	now, err := service.store.TrustedTime(ctx)
	if err != nil {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, fmt.Errorf("%w: trusted time is unavailable", ErrRuntimeAuthority)
	}
	now, err = normalizeGovernanceTrustedTime(now)
	if err != nil {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, fmt.Errorf("%w: trusted time is unavailable", ErrRuntimeAuthority)
	}
	policy, err := service.authority.CurrentGovernanceTrustPolicy(ctx)
	if err != nil || ValidateGovernanceTrustPolicy(policy) != nil {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, fmt.Errorf("%w: current trust policy is unavailable or invalid", ErrRuntimeAuthority)
	}
	revocations, err := service.loadCurrentRevocationAuthority(ctx, now)
	if err != nil {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, err
	}
	if err := service.observeCurrentTrustPolicy(ctx, policy, revocations); err != nil {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, err
	}
	materials, err := service.authority.LoadGovernanceMaterials(ctx, receiptDigest)
	if err != nil {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, fmt.Errorf("%w: exact governance materials: %v", ErrRuntimeAuthority, err)
	}
	verified, err := service.verifier.Verify(materials, receiptDigest, policy, revocations, now)
	if err != nil {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, err
	}
	return now, policy, revocations, verified, nil
}

func (service *ActivationService) verifyHistoricalRecord(ctx context.Context, record ActivationRecord) (VerifiedGovernance, error) {
	now, err := service.store.TrustedTime(ctx)
	if err != nil {
		return VerifiedGovernance{}, fmt.Errorf("%w: trusted time is unavailable", ErrRuntimeAuthority)
	}
	now, err = normalizeGovernanceTrustedTime(now)
	if err != nil {
		return VerifiedGovernance{}, fmt.Errorf("%w: trusted time is unavailable", ErrRuntimeAuthority)
	}
	revocations, err := service.loadCurrentRevocationAuthority(ctx, now)
	if err != nil {
		return VerifiedGovernance{}, err
	}
	policyHash, err := service.trustPolicyHashForRecord(ctx, record)
	if err != nil {
		return VerifiedGovernance{}, err
	}
	policy, err := service.authority.LoadGovernanceTrustPolicy(ctx, policyHash)
	if err != nil || ValidateGovernanceTrustPolicy(policy) != nil || policy.PolicyHash != policyHash {
		return VerifiedGovernance{}, fmt.Errorf("%w: receipt-bound historical signer policy is unavailable", ErrRuntimeAuthority)
	}
	return service.verifyRecordWithPolicy(ctx, record, policy, revocations, now, false)
}

const maximumActivationProjectionWalk = 4096

// confirmActivationOutcome reconstructs the store's atomic result from all
// four projections. A head may have advanced after this operation only through
// a complete immutable generation/fence chain rooted at the exact record.
func (service *ActivationService) confirmActivationOutcome(ctx context.Context, expected ActivationRecord, returned *ActivationRecord) (ActivationRecord, error) {
	if returned != nil && !sameActivationRecord(*returned, expected) {
		return ActivationRecord{}, fmt.Errorf("%w: activation return projection differs from requested bytes", ErrActivationConflict)
	}
	if err := validateRegistryRecord(expected); err != nil {
		return ActivationRecord{}, err
	}
	operation, err := service.store.GetActivationOperation(ctx, expected.OperationID)
	if err != nil {
		return ActivationRecord{}, activationProjectionReadError("operation", err)
	}
	if !sameActivationRecord(operation, expected) {
		return ActivationRecord{}, fmt.Errorf("%w: operation projection differs from requested bytes", ErrActivationConflict)
	}
	history, err := service.store.GetActivationGeneration(ctx, expected.Workload, expected.Generation)
	if err != nil {
		return ActivationRecord{}, activationProjectionReadError("history", err)
	}
	if !sameActivationRecord(history, expected) {
		return ActivationRecord{}, fmt.Errorf("%w: history projection differs from operation projection", ErrActivationConflict)
	}
	indexed, err := service.store.GetActivatedProfile(ctx, CorpusProfileBinding{
		ID: expected.ProfileID, ContentHash: expected.ProfileContentHash, Workload: expected.Workload,
	})
	if err != nil {
		return ActivationRecord{}, activationProjectionReadError("exact-profile index", err)
	}
	if !sameActivationRecord(indexed, expected) {
		return ActivationRecord{}, fmt.Errorf("%w: exact-profile projection differs from operation projection", ErrActivationConflict)
	}
	openingHead, err := service.store.GetActiveActivation(ctx, expected.Workload)
	if err != nil {
		return ActivationRecord{}, activationProjectionReadError("workload head", err)
	}
	if err := service.confirmActivationHeadDescends(ctx, expected, openingHead); err != nil {
		return ActivationRecord{}, err
	}
	closingHead, err := service.store.GetActiveActivation(ctx, expected.Workload)
	if err != nil {
		return ActivationRecord{}, activationProjectionReadError("closing workload head", err)
	}
	if !sameActivationRecord(openingHead, closingHead) {
		return ActivationRecord{}, fmt.Errorf("%w: workload head changed while reconstructing activation outcome", ErrActivationOutcomeUnknown)
	}
	return operation, nil
}

func (service *ActivationService) confirmActivationHeadDescends(ctx context.Context, expected, head ActivationRecord) error {
	if sameActivationRecord(expected, head) {
		return nil
	}
	if head.Workload != expected.Workload || head.Generation <= expected.Generation || head.Generation-expected.Generation > maximumActivationProjectionWalk {
		return fmt.Errorf("%w: workload head is neither the operation nor a bounded immutable descendant", ErrActivationConflict)
	}
	prior := expected
	for generation := expected.Generation + 1; ; generation++ {
		descendant, err := service.store.GetActivationGeneration(ctx, expected.Workload, generation)
		if err != nil {
			return activationProjectionReadError("descendant history", err)
		}
		if err := validateRegistryRecord(descendant); err != nil || descendant.AuthorityKind != ActivationAuthorityKind ||
			descendant.Workload != expected.Workload || descendant.PreviousGeneration != prior.Generation || descendant.PreviousFence != prior.Fence {
			return fmt.Errorf("%w: workload head descendant chain is incomplete or inconsistent", ErrActivationConflict)
		}
		operation, err := service.store.GetActivationOperation(ctx, descendant.OperationID)
		if err != nil {
			return activationProjectionReadError("descendant operation", err)
		}
		if !sameActivationRecord(operation, descendant) {
			return fmt.Errorf("%w: descendant operation differs from immutable history", ErrActivationConflict)
		}
		indexed, err := service.store.GetActivatedProfile(ctx, CorpusProfileBinding{
			ID: descendant.ProfileID, ContentHash: descendant.ProfileContentHash, Workload: descendant.Workload,
		})
		if err != nil {
			return activationProjectionReadError("descendant exact-profile index", err)
		}
		if !sameActivationRecord(indexed, descendant) {
			return fmt.Errorf("%w: descendant exact-profile index differs from immutable history", ErrActivationConflict)
		}
		prior = descendant
		if generation == head.Generation {
			break
		}
	}
	if !sameActivationRecord(prior, head) {
		return fmt.Errorf("%w: workload head differs from its immutable history projection", ErrActivationConflict)
	}
	return nil
}

func activationProjectionReadError(projection string, err error) error {
	if errors.Is(err, ErrActivationNotFound) {
		return fmt.Errorf("%w: %s projection is missing after activation commit", ErrActivationConflict, projection)
	}
	return fmt.Errorf("%w: inspect %s projection: %v", ErrActivationOutcomeUnknown, projection, err)
}

// resolveGraph verifies primary (or uses an already verified candidate), then
// recursively resolves every fallback through the append-only activation
// registry. A digest in a profile is never treated as evidence of activation.
func (service *ActivationService) resolveGraph(
	ctx context.Context,
	primary ActivationRecord,
	preverified *VerifiedGovernance,
	revocations GovernanceRevocationAuthority,
	now time.Time,
	runtimeAuthority bool,
) ([]ResolvedGovernanceNode, error) {
	nodes := make(map[string]ResolvedGovernanceNode)
	visiting := make(map[string]bool)
	var visit func(ActivationRecord, *VerifiedGovernance) error
	visit = func(record ActivationRecord, supplied *VerifiedGovernance) error {
		if err := validateRegistryRecord(record); err != nil {
			return fmt.Errorf("%w: activation registry projection is invalid: %v", ErrRuntimeAuthority, err)
		}
		key := record.ProfileID + "\x00" + record.ProfileContentHash
		if visiting[key] {
			return fmt.Errorf("%w: active fallback graph contains a cycle", ErrRuntimeAuthority)
		}
		if existing, ok := nodes[key]; ok {
			if existing.Record.ReceiptDigest != record.ReceiptDigest {
				return fmt.Errorf("%w: one exact profile resolves to multiple receipts", ErrRuntimeAuthority)
			}
			return nil
		}
		if len(nodes) >= 1024 {
			return fmt.Errorf("%w: active fallback graph exceeds 1024 exact profiles", ErrRuntimeAuthority)
		}
		visiting[key] = true
		defer delete(visiting, key)

		verified := VerifiedGovernance{}
		if supplied != nil {
			verified = *supplied
		} else {
			policyHash, err := service.trustPolicyHashForRecord(ctx, record)
			if err != nil {
				return err
			}
			policy, err := service.authority.LoadGovernanceTrustPolicy(ctx, policyHash)
			if err != nil || ValidateGovernanceTrustPolicy(policy) != nil || policy.PolicyHash != policyHash {
				return fmt.Errorf("%w: receipt-bound historical signer policy is unavailable", ErrRuntimeAuthority)
			}
			verified, err = service.verifyRecordWithPolicy(ctx, record, policy, revocations, now, runtimeAuthority)
			if err != nil {
				return err
			}
		}
		if !recordMatchesVerified(record, verified) {
			return fmt.Errorf("%w: activation registry record differs from the exact verified receipt", ErrRuntimeAuthority)
		}
		if err := service.requireExactSignedPredecessor(ctx, record, verified); err != nil {
			return err
		}
		if runtimeAuthority {
			if err := service.requireCurrentProviderRoute(ctx, verified, now); err != nil {
				return err
			}
			if err := service.requireProfileEnabled(ctx, record, verified.Profile, now); err != nil {
				return err
			}
		}
		nodes[key] = ResolvedGovernanceNode{Record: record, Verified: verified}

		for _, fallback := range verified.Profile.Fallback.Profiles {
			fallbackRecord, err := service.store.GetActivatedProfile(ctx, CorpusProfileBinding{
				ID: fallback.ID, ContentHash: fallback.ContentHash, Workload: fallback.Workload,
			})
			if err != nil {
				return fmt.Errorf("%w: fallback %s is not in the exact activation registry: %v", ErrRuntimeAuthority, fallback.ID, err)
			}
			if err := visit(fallbackRecord, nil); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(primary, preverified); err != nil {
		return nil, err
	}

	bindings := make([]ProfileAuthorityBinding, 0, len(nodes))
	result := make([]ResolvedGovernanceNode, 0, len(nodes))
	for _, node := range nodes {
		bindings = append(bindings, ProfileAuthorityBinding{
			Profile: node.Verified.Profile, ContentHash: node.Record.ProfileContentHash,
			ApprovalReceiptDigest: node.Record.ReceiptDigest,
		})
		result = append(result, node)
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Profile.ID < bindings[j].Profile.ID })
	sort.Slice(result, func(i, j int) bool { return result[i].Record.ProfileID < result[j].Record.ProfileID })
	if err := ValidateModelProfileGraph(bindings); err != nil {
		return nil, fmt.Errorf("%w: active fallback graph: %v", ErrRuntimeAuthority, err)
	}
	return result, nil
}

func (service *ActivationService) requireGraphDependenciesStable(ctx context.Context, graph []ResolvedGovernanceNode, skipReceipt string, now time.Time, runtimeAuthority bool) error {
	for _, node := range graph {
		if node.Record.ReceiptDigest != skipReceipt {
			history, err := service.store.GetActivationGeneration(ctx, node.Record.Workload, node.Record.Generation)
			if err != nil || !sameActivationRecord(history, node.Record) {
				return fmt.Errorf("%w: activation history changed during graph resolution", ErrRuntimeAuthority)
			}
			indexed, err := service.store.GetActivatedProfile(ctx, CorpusProfileBinding{
				ID: node.Record.ProfileID, ContentHash: node.Record.ProfileContentHash, Workload: node.Record.Workload,
			})
			if err != nil || !sameActivationRecord(indexed, node.Record) {
				return fmt.Errorf("%w: exact-profile activation changed during graph resolution", ErrRuntimeAuthority)
			}
		}
		policy, err := service.authority.LoadGovernanceTrustPolicy(ctx, node.Verified.Subject.TrustPolicyHash)
		if err != nil || ValidateGovernanceTrustPolicy(policy) != nil || policy.PolicyHash != node.Verified.Subject.TrustPolicyHash {
			return fmt.Errorf("%w: receipt-bound signer policy changed during graph resolution", ErrRuntimeAuthority)
		}
		if err := service.requireExactSignedPredecessor(ctx, node.Record, node.Verified); err != nil {
			return err
		}
		if runtimeAuthority {
			if err := service.requireCurrentProviderRoute(ctx, node.Verified, now); err != nil {
				return err
			}
			if err := service.requireProfileEnabled(ctx, node.Record, node.Verified.Profile, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (service *ActivationService) requireExactSignedPredecessor(ctx context.Context, record ActivationRecord, verified VerifiedGovernance) error {
	if record.PreviousGeneration == 0 {
		if record.AuthorityKind != GenesisAuthorityKind || verified.AuthorityKind != GenesisAuthorityKind || record.Generation != 1 {
			return fmt.Errorf("%w: generation one is not backed by the signed Genesis authority", ErrRuntimeAuthority)
		}
		return nil
	}
	predecessor, err := service.store.GetActivationGeneration(ctx, record.Workload, record.PreviousGeneration)
	if err != nil || !recordIsExactPredecessor(predecessor, ActivationRequest{
		ExpectedGeneration: record.PreviousGeneration, ExpectedFence: record.PreviousFence,
	}, verified.Shadow.Baseline) {
		return fmt.Errorf("%w: signed shadow baseline is not the exact immutable predecessor generation", ErrRuntimeAuthority)
	}
	return nil
}

func (service *ActivationService) requireAuthorityStable(ctx context.Context, expectedCurrentPolicyHash string, openingRevocations GovernanceRevocationAuthority, now time.Time) error {
	if expectedCurrentPolicyHash != "" {
		closing, err := service.authority.CurrentGovernanceTrustPolicy(ctx)
		if err != nil || ValidateGovernanceTrustPolicy(closing) != nil || closing.PolicyHash != expectedCurrentPolicyHash {
			return fmt.Errorf("%w: current trust policy changed during authority resolution", ErrRuntimeAuthority)
		}
	}
	closingRevocations, err := service.loadCurrentRevocationAuthority(ctx, now)
	if err != nil ||
		closingRevocations.Epoch != openingRevocations.Epoch || closingRevocations.AuthorityHash != openingRevocations.AuthorityHash {
		return fmt.Errorf("%w: current revocation authority changed during authority resolution", ErrRuntimeAuthority)
	}
	return nil
}

func (service *ActivationService) loadCurrentRevocationAuthority(ctx context.Context, now time.Time) (GovernanceRevocationAuthority, error) {
	authority, err := service.authority.CurrentGovernanceRevocationAuthority(ctx)
	if err != nil || ValidateGovernanceRevocationAuthority(authority, now) != nil {
		return GovernanceRevocationAuthority{}, fmt.Errorf("%w: current revocation authority is unavailable or invalid", ErrRuntimeAuthority)
	}
	if err := service.store.ObserveGovernanceRevocationAuthority(ctx, authority); err != nil {
		return GovernanceRevocationAuthority{}, fmt.Errorf("%w: revocation rollback/equivocation fence: %v", ErrRuntimeAuthority, err)
	}
	return authority, nil
}

func (service *ActivationService) observeCurrentTrustPolicy(ctx context.Context, policy GovernanceTrustPolicy, revocations GovernanceRevocationAuthority) error {
	if err := service.store.ObserveGovernanceTrustPolicy(ctx, GovernanceTrustPolicyObservation{
		PolicyHash: policy.PolicyHash, RevocationAuthorityHash: revocations.AuthorityHash, RevocationEpoch: revocations.Epoch,
	}); err != nil {
		return fmt.Errorf("%w: current trust-policy epoch fence: %v", ErrRuntimeAuthority, err)
	}
	return nil
}

func (service *ActivationService) requireCurrentProviderRoute(ctx context.Context, verified VerifiedGovernance, now time.Time) error {
	encoded, err := service.authority.CurrentProviderRouteAuthority(ctx, verified.ProviderRoute.RouteID)
	if err != nil || sha256Digest(encoded) != verified.Subject.ProviderRoute.AuthorityHash {
		return fmt.Errorf("%w: exact provider route is not the current route authority", ErrRuntimeAuthority)
	}
	current, err := ParseProviderRouteAuthority(encoded, verified.Subject.ProviderRoute.AuthorityHash)
	if err != nil || current != verified.ProviderRoute {
		return fmt.Errorf("%w: current provider route authority drifted", ErrRuntimeAuthority)
	}
	if err := requireGovernanceWindowCurrent(current.IssuedAt, current.ExpiresAt, now, "current provider route authority"); err != nil {
		return fmt.Errorf("%w: %v", ErrRuntimeAuthority, err)
	}
	return nil
}

func (service *ActivationService) requireProfileEnabled(ctx context.Context, record ActivationRecord, profile ModelProfile, now time.Time) error {
	query := disableQueryForRecord(record)
	state, err := service.authority.CurrentProfileDisableState(ctx, query)
	if err != nil {
		return fmt.Errorf("%w: exact disable state is unavailable: %v", ErrProfileDisabled, err)
	}
	if state.Query != query || state.ActiveConditions == nil || state.CheckedAt.IsZero() || state.ExpiresAt.IsZero() ||
		state.CheckedAt.UTC().After(now.Add(MaximumGovernanceClockSkew)) || !now.Before(state.ExpiresAt.UTC()) || !state.ExpiresAt.After(state.CheckedAt) {
		return fmt.Errorf("%w: disable state is mismatched, stale, or invalid", ErrProfileDisabled)
	}
	if !canonicalGovernanceTime(state.CheckedAt) || !canonicalGovernanceTime(state.ExpiresAt) || state.ExpiresAt.Sub(state.CheckedAt) > MaximumDisableStateLifetime {
		return fmt.Errorf("%w: disable-state validity window is non-canonical or too long", ErrProfileDisabled)
	}
	if !sort.StringsAreSorted(state.ActiveConditions) {
		return fmt.Errorf("%w: disable conditions are not canonical", ErrProfileDisabled)
	}
	allowed := make(map[string]struct{}, len(profile.DisableConditions))
	for _, condition := range profile.DisableConditions {
		allowed[condition] = struct{}{}
	}
	previous := ""
	for _, condition := range state.ActiveConditions {
		if _, ok := allowed[condition]; !ok || condition == previous {
			return fmt.Errorf("%w: disable state contains an undeclared or duplicate condition", ErrProfileDisabled)
		}
		previous = condition
	}
	if len(state.ActiveConditions) != 0 {
		return fmt.Errorf("%w: %v", ErrProfileDisabled, state.ActiveConditions)
	}
	return nil
}

func activationRequestHash(request ActivationRequest) (string, error) {
	if !validUUIDv4(request.OperationID) || !validDigest(request.ReceiptDigest) || !validDigest(request.ExpectedFence) {
		return "", fmt.Errorf("%w: activation operation, receipt, or predecessor fence is invalid", ErrGovernanceInvalid)
	}
	encoded, err := json.Marshal(struct {
		ExpectedFence      string `json:"expectedFence"`
		ExpectedGeneration uint64 `json:"expectedGeneration"`
		OperationID        string `json:"operationId"`
		ReceiptDigest      string `json:"receiptDigest"`
	}{request.ExpectedFence, request.ExpectedGeneration, request.OperationID, request.ReceiptDigest})
	if err != nil {
		return "", fmt.Errorf("%w: encode activation request: %v", ErrGovernanceInvalid, err)
	}
	return sha256Digest(encoded), nil
}

func activationRecordFromVerified(request ActivationRequest, requestHash string, value VerifiedGovernance, now time.Time) ActivationRecord {
	return ActivationRecord{
		AuthorityKind: ActivationAuthorityKind,
		OperationID:   request.OperationID, RequestHash: requestHash, Workload: value.Profile.Workload,
		ProfileID: value.Profile.ID, ProfileContentHash: value.Subject.Profile.ContentHash,
		ReceiptDigest: value.ReceiptEnvelopeDigest, ReceiptPayloadDigest: value.ReceiptPayloadDigest,
		ActivationEnvelopeDigest: value.ActivationRef.EnvelopeDigest, ActivationPayloadDigest: value.ActivationRef.PayloadDigest,
		PreviousGeneration: value.Activation.PreviousGeneration, Generation: value.Activation.Generation,
		PreviousFence: value.Activation.PreviousFence, Fence: value.Activation.Fence,
		CorpusContentHash: value.Subject.Corpus.ContentHash, ProviderRouteAuthorityHash: value.Subject.ProviderRoute.AuthorityHash,
		RunnerImmutableDigest: value.Subject.Runner.ImmutableDigest, SourceTreeDigest: value.Subject.Source.TreeDigest,
		TrustPolicyHash: value.Subject.TrustPolicyHash, ActivatedAt: now.UTC(),
	}
}

func recordMatchesVerified(record ActivationRecord, value VerifiedGovernance) bool {
	if record.AuthorityKind == GenesisAuthorityKind {
		return recordMatchesVerifiedGenesis(record, value)
	}
	if record.AuthorityKind != ActivationAuthorityKind || value.AuthorityKind != ActivationAuthorityKind {
		return false
	}
	return record.Workload == value.Profile.Workload && record.ProfileID == value.Profile.ID &&
		record.ProfileContentHash == value.Subject.Profile.ContentHash && record.ReceiptDigest == value.ReceiptEnvelopeDigest &&
		record.ReceiptPayloadDigest == value.ReceiptPayloadDigest && record.ActivationEnvelopeDigest == value.ActivationRef.EnvelopeDigest &&
		record.ActivationPayloadDigest == value.ActivationRef.PayloadDigest && record.PreviousGeneration == value.Activation.PreviousGeneration &&
		record.Generation == value.Activation.Generation && record.PreviousFence == value.Activation.PreviousFence && record.Fence == value.Activation.Fence &&
		record.CorpusContentHash == value.Subject.Corpus.ContentHash && record.ProviderRouteAuthorityHash == value.Subject.ProviderRoute.AuthorityHash &&
		record.RunnerImmutableDigest == value.Subject.Runner.ImmutableDigest && record.SourceTreeDigest == value.Subject.Source.TreeDigest &&
		record.TrustPolicyHash == value.Subject.TrustPolicyHash && record.GenesisEnvelopeDigest == "" &&
		record.GenesisPayloadDigest == "" && record.InitialRevocationAuthorityID == "" &&
		record.InitialRevocationAuthorityHash == "" && record.InitialRevocationAuthorityEpoch == 0
}

func recordIsExactPredecessor(record ActivationRecord, request ActivationRequest, baseline BaselineBinding) bool {
	return record.Workload == baseline.Profile.Workload && record.ProfileID == baseline.Profile.ID &&
		record.ProfileContentHash == baseline.Profile.ContentHash && record.ReceiptDigest == baseline.ReceiptDigest &&
		record.Generation == baseline.Generation && record.Fence == baseline.ActivationFence &&
		record.Generation == request.ExpectedGeneration && record.Fence == request.ExpectedFence
}

func disableQueryForRecord(record ActivationRecord) RuntimeDisableQuery {
	return RuntimeDisableQuery{
		Workload: record.Workload, ProfileID: record.ProfileID, ProfileContentHash: record.ProfileContentHash,
		ReceiptDigest: record.ReceiptDigest, Generation: record.Generation, Fence: record.Fence,
	}
}

func sameActivationRecord(left, right ActivationRecord) bool {
	return left == right
}

func governanceTrustPolicyHashFromMaterials(materials GovernanceMaterials, receiptDigest string) (string, error) {
	envelope, err := parseGovernanceEnvelope(materials.ReceiptEnvelope, receiptDigest, GovernanceEnvelopePayloadTypeReceipt)
	if err != nil {
		return "", err
	}
	receipt, err := ParseModelGovernanceReceipt(envelope.payload, envelope.payloadDigest)
	if err != nil || !validDigest(receipt.Subject.TrustPolicyHash) {
		return "", fmt.Errorf("%w: receipt-bound signer policy selector is invalid", ErrRuntimeAuthority)
	}
	return receipt.Subject.TrustPolicyHash, nil
}

func governanceTrustPolicyHashFromGenesisMaterials(materials GenesisGovernanceMaterials, receiptDigest string) (string, error) {
	envelope, err := parseGovernanceEnvelope(materials.ReceiptEnvelope, receiptDigest, GovernanceEnvelopePayloadTypeGenesisReceipt)
	if err != nil {
		return "", err
	}
	receipt, err := ParseModelGovernanceGenesisReceipt(envelope.payload, envelope.payloadDigest)
	if err != nil || !validDigest(receipt.Subject.TrustPolicyHash) {
		return "", fmt.Errorf("%w: Genesis receipt-bound signer policy selector is invalid", ErrRuntimeAuthority)
	}
	return receipt.Subject.TrustPolicyHash, nil
}

func (service *ActivationService) trustPolicyHashForRecord(ctx context.Context, record ActivationRecord) (string, error) {
	switch record.AuthorityKind {
	case ActivationAuthorityKind:
		materials, err := service.authority.LoadGovernanceMaterials(ctx, record.ReceiptDigest)
		if err != nil {
			return "", fmt.Errorf("%w: exact activation governance materials: %v", ErrRuntimeAuthority, err)
		}
		return governanceTrustPolicyHashFromMaterials(materials, record.ReceiptDigest)
	case GenesisAuthorityKind:
		materials, err := service.authority.LoadGenesisGovernanceMaterials(ctx, record.ReceiptDigest)
		if err != nil {
			return "", fmt.Errorf("%w: exact Genesis governance materials: %v", ErrRuntimeAuthority, err)
		}
		return governanceTrustPolicyHashFromGenesisMaterials(materials, record.ReceiptDigest)
	default:
		return "", fmt.Errorf("%w: registry authority kind is not closed", ErrRuntimeAuthority)
	}
}

func (service *ActivationService) verifyRecordWithPolicy(
	ctx context.Context,
	record ActivationRecord,
	policy GovernanceTrustPolicy,
	revocations GovernanceRevocationAuthority,
	now time.Time,
	runtimeAuthority bool,
) (VerifiedGovernance, error) {
	switch record.AuthorityKind {
	case ActivationAuthorityKind:
		materials, err := service.authority.LoadGovernanceMaterials(ctx, record.ReceiptDigest)
		if err != nil {
			return VerifiedGovernance{}, fmt.Errorf("%w: exact activation governance materials: %v", ErrRuntimeAuthority, err)
		}
		if runtimeAuthority {
			return service.verifier.Verify(materials, record.ReceiptDigest, policy, revocations, now)
		}
		return service.verifier.VerifyHistorical(materials, record.ReceiptDigest, policy, revocations, now, record.ActivatedAt)
	case GenesisAuthorityKind:
		materials, err := service.authority.LoadGenesisGovernanceMaterials(ctx, record.ReceiptDigest)
		if err != nil {
			return VerifiedGovernance{}, fmt.Errorf("%w: exact Genesis governance materials: %v", ErrRuntimeAuthority, err)
		}
		if runtimeAuthority {
			return service.verifier.VerifyGenesis(materials, record.ReceiptDigest, policy, revocations, now)
		}
		return service.verifier.VerifyGenesisHistorical(materials, record.ReceiptDigest, policy, revocations, now, record.ActivatedAt)
	default:
		return VerifiedGovernance{}, fmt.Errorf("%w: registry authority kind is not closed", ErrRuntimeAuthority)
	}
}

func nilGovernanceDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
