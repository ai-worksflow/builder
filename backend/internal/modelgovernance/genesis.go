package modelgovernance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// GenesisBootstrapRequest is the one idempotent request allowed to create a
// workload head. ExpectedEmptyFence is an explicit non-zero signed fence; it
// is not an implicit zero value or a fabricated predecessor receipt.
type GenesisBootstrapRequest struct {
	OperationID        string
	ReceiptDigest      string
	ExpectedEmptyFence string
}

type GenesisAppend struct {
	ExpectedGeneration              uint64
	ExpectedFence                   string
	CurrentTrustPolicyHash          string
	CurrentRevocationAuthorityHash  string
	CurrentRevocationAuthorityEpoch uint64
	Record                          ActivationRecord
}

// GenesisBootstrapStore is deliberately distinct from ActivationStore. An
// ordinary activation caller cannot use AppendActivation to create a head.
type GenesisBootstrapStore interface {
	ActivationStore
	AppendGenesis(context.Context, GenesisAppend) (ActivationRecord, error)
}

type GenesisBootstrapService struct {
	store      GenesisBootstrapStore
	authority  GovernanceAuthority
	verifier   *GovernanceVerifier
	activation *ActivationService
}

func NewGenesisBootstrapService(store GenesisBootstrapStore, authority GovernanceAuthority, verifier *GovernanceVerifier) (*GenesisBootstrapService, error) {
	if nilGovernanceDependency(store) || nilGovernanceDependency(authority) || verifier == nil {
		return nil, fmt.Errorf("%w: Genesis store, authority, and verifier are required", ErrGovernanceInvalid)
	}
	activation, err := NewActivationService(store, authority, verifier)
	if err != nil {
		return nil, err
	}
	return &GenesisBootstrapService{store: store, authority: authority, verifier: verifier, activation: activation}, nil
}

func (service *GenesisBootstrapService) Bootstrap(ctx context.Context, request GenesisBootstrapRequest) (ActivationRecord, error) {
	if service == nil || nilGovernanceDependency(service.store) || nilGovernanceDependency(service.authority) ||
		service.verifier == nil || service.activation == nil || nilGovernanceDependency(ctx) {
		return ActivationRecord{}, fmt.Errorf("%w: Genesis bootstrap service and non-nil context are required", ErrGovernanceInvalid)
	}
	requestHash, err := genesisBootstrapRequestHash(request)
	if err != nil {
		return ActivationRecord{}, err
	}
	if existing, inspectErr := service.store.GetActivationOperation(ctx, request.OperationID); inspectErr == nil {
		if existing.RequestHash != requestHash || existing.AuthorityKind != GenesisAuthorityKind {
			return ActivationRecord{}, fmt.Errorf("%w: Genesis operation id was used for different bytes", ErrActivationConflict)
		}
		verified, verifyErr := service.activation.verifyHistoricalRecord(ctx, existing)
		if verifyErr != nil || !recordMatchesVerifiedGenesis(existing, verified) || validateRegistryRecord(existing) != nil {
			return ActivationRecord{}, fmt.Errorf("%w: stored Genesis outcome cannot be exactly revalidated", ErrActivationConflict)
		}
		return service.activation.confirmActivationOutcome(ctx, existing, &existing)
	} else if !errors.Is(inspectErr, ErrActivationNotFound) {
		return ActivationRecord{}, inspectErr
	}

	now, policy, revocations, verified, err := service.verifyGenesisReceipt(ctx, request.ReceiptDigest)
	if err != nil {
		return ActivationRecord{}, err
	}
	if verified.Genesis.PreviousGeneration != 0 || verified.Genesis.Generation != 1 ||
		verified.Genesis.PreviousFence != request.ExpectedEmptyFence {
		return ActivationRecord{}, fmt.Errorf("%w: signed Genesis does not match the explicit empty-head fence", ErrActivationConflict)
	}
	if _, err := service.store.GetActiveActivation(ctx, verified.Profile.Workload); err == nil {
		return ActivationRecord{}, fmt.Errorf("%w: Genesis requires an empty workload head", ErrActivationConflict)
	} else if !errors.Is(err, ErrActivationNotFound) {
		return ActivationRecord{}, err
	}
	record := genesisRecordFromVerified(request, requestHash, verified, now)
	if err := service.activation.requireCurrentProviderRoute(ctx, verified, now); err != nil {
		return ActivationRecord{}, err
	}
	if err := service.activation.requireProfileEnabled(ctx, record, verified.Profile, now); err != nil {
		return ActivationRecord{}, err
	}
	graph, err := service.activation.resolveGraph(ctx, record, &verified, revocations, now, true)
	if err != nil {
		return ActivationRecord{}, err
	}
	if err := service.activation.requireGraphDependenciesStable(ctx, graph, record.ReceiptDigest, now, true); err != nil {
		return ActivationRecord{}, err
	}
	if err := service.activation.requireAuthorityStable(ctx, policy.PolicyHash, revocations, now); err != nil {
		return ActivationRecord{}, err
	}

	command := GenesisAppend{
		ExpectedGeneration: 0, ExpectedFence: request.ExpectedEmptyFence,
		CurrentTrustPolicyHash:          policy.PolicyHash,
		CurrentRevocationAuthorityHash:  revocations.AuthorityHash,
		CurrentRevocationAuthorityEpoch: revocations.Epoch,
		Record:                          record,
	}
	stored, err := service.store.AppendGenesis(ctx, command)
	if err == nil {
		if !sameActivationRecord(stored, record) {
			return ActivationRecord{}, fmt.Errorf("%w: Genesis store returned different immutable bytes", ErrActivationConflict)
		}
		return service.activation.confirmActivationOutcome(ctx, record, &stored)
	}
	if !errors.Is(err, ErrActivationOutcomeUnknown) {
		return ActivationRecord{}, err
	}
	inspected, inspectErr := service.store.GetActivationOperation(ctx, request.OperationID)
	if inspectErr == nil {
		if !sameActivationRecord(inspected, record) || inspected.RequestHash != requestHash ||
			!recordMatchesVerifiedGenesis(inspected, verified) || validateRegistryRecord(inspected) != nil {
			return ActivationRecord{}, fmt.Errorf("%w: unknown Genesis outcome resolved to different bytes", ErrActivationConflict)
		}
		return service.activation.confirmActivationOutcome(ctx, record, &inspected)
	}
	if errors.Is(inspectErr, ErrActivationNotFound) {
		return ActivationRecord{}, ErrActivationOutcomeUnknown
	}
	return ActivationRecord{}, fmt.Errorf("%w: inspect Genesis operation: %v", ErrActivationOutcomeUnknown, inspectErr)
}

func (service *GenesisBootstrapService) verifyGenesisReceipt(ctx context.Context, receiptDigest string) (
	time.Time, GovernanceTrustPolicy, GovernanceRevocationAuthority, VerifiedGovernance, error,
) {
	if !validDigest(receiptDigest) {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, fmt.Errorf("%w: Genesis receipt digest is invalid", ErrGovernanceInvalid)
	}
	now, err := service.store.TrustedTime(ctx)
	if err != nil {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, fmt.Errorf("%w: trusted time is unavailable", ErrRuntimeAuthority)
	}
	now, err = normalizeGovernanceTrustedTime(now)
	if err != nil {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, err
	}
	policy, err := service.authority.CurrentGovernanceTrustPolicy(ctx)
	if err != nil || ValidateGovernanceTrustPolicy(policy) != nil {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, fmt.Errorf("%w: current Genesis trust policy is unavailable", ErrRuntimeAuthority)
	}
	revocations, err := service.activation.loadCurrentRevocationAuthority(ctx, now)
	if err != nil {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, err
	}
	if err := service.activation.observeCurrentTrustPolicy(ctx, policy, revocations); err != nil {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, err
	}
	materials, err := service.authority.LoadGenesisGovernanceMaterials(ctx, receiptDigest)
	if err != nil {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, fmt.Errorf("%w: exact Genesis materials: %v", ErrRuntimeAuthority, err)
	}
	verified, err := service.verifier.VerifyGenesis(materials, receiptDigest, policy, revocations, now)
	if err != nil {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, err
	}
	if verified.Genesis.RevocationAuthority.AuthorityHash != revocations.AuthorityHash ||
		verified.Genesis.RevocationAuthority.Epoch != revocations.Epoch {
		return time.Time{}, GovernanceTrustPolicy{}, GovernanceRevocationAuthority{}, VerifiedGovernance{}, fmt.Errorf("%w: Genesis does not bind the exact current revocation authority", ErrGovernanceUntrusted)
	}
	return now, policy, revocations, verified, nil
}

func genesisBootstrapRequestHash(request GenesisBootstrapRequest) (string, error) {
	if !validUUIDv4(request.OperationID) || !validDigest(request.ReceiptDigest) || !validDigest(request.ExpectedEmptyFence) {
		return "", fmt.Errorf("%w: Genesis operation, receipt, or empty-head fence is invalid", ErrGovernanceInvalid)
	}
	encoded, err := json.Marshal(struct {
		ExpectedEmptyFence string `json:"expectedEmptyFence"`
		OperationID        string `json:"operationId"`
		ReceiptDigest      string `json:"receiptDigest"`
	}{request.ExpectedEmptyFence, request.OperationID, request.ReceiptDigest})
	if err != nil {
		return "", fmt.Errorf("%w: encode Genesis request: %v", ErrGovernanceInvalid, err)
	}
	return sha256Digest(encoded), nil
}

func genesisRecordFromVerified(request GenesisBootstrapRequest, requestHash string, value VerifiedGovernance, now time.Time) ActivationRecord {
	return ActivationRecord{
		AuthorityKind: GenesisAuthorityKind,
		OperationID:   request.OperationID, RequestHash: requestHash, Workload: value.Profile.Workload,
		ProfileID: value.Profile.ID, ProfileContentHash: value.Subject.Profile.ContentHash,
		ReceiptDigest: value.ReceiptEnvelopeDigest, ReceiptPayloadDigest: value.ReceiptPayloadDigest,
		ActivationEnvelopeDigest: value.GenesisRef.EnvelopeDigest, ActivationPayloadDigest: value.GenesisRef.PayloadDigest,
		PreviousGeneration: value.Genesis.PreviousGeneration, Generation: value.Genesis.Generation,
		PreviousFence: value.Genesis.PreviousFence, Fence: value.Genesis.Fence,
		CorpusContentHash: value.Subject.Corpus.ContentHash, ProviderRouteAuthorityHash: value.Subject.ProviderRoute.AuthorityHash,
		RunnerImmutableDigest: value.Subject.Runner.ImmutableDigest, SourceTreeDigest: value.Subject.Source.TreeDigest,
		TrustPolicyHash:       value.Subject.TrustPolicyHash,
		GenesisEnvelopeDigest: value.GenesisRef.EnvelopeDigest, GenesisPayloadDigest: value.GenesisRef.PayloadDigest,
		InitialRevocationAuthorityID:    value.Genesis.RevocationAuthority.AuthorityID,
		InitialRevocationAuthorityHash:  value.Genesis.RevocationAuthority.AuthorityHash,
		InitialRevocationAuthorityEpoch: value.Genesis.RevocationAuthority.Epoch,
		ActivatedAt:                     now.UTC(),
	}
}

func recordMatchesVerifiedGenesis(record ActivationRecord, value VerifiedGovernance) bool {
	return record.AuthorityKind == GenesisAuthorityKind && value.AuthorityKind == GenesisAuthorityKind &&
		record.Workload == value.Profile.Workload && record.ProfileID == value.Profile.ID &&
		record.ProfileContentHash == value.Subject.Profile.ContentHash && record.ReceiptDigest == value.ReceiptEnvelopeDigest &&
		record.ReceiptPayloadDigest == value.ReceiptPayloadDigest && record.ActivationEnvelopeDigest == value.GenesisRef.EnvelopeDigest &&
		record.ActivationPayloadDigest == value.GenesisRef.PayloadDigest && record.GenesisEnvelopeDigest == value.GenesisRef.EnvelopeDigest &&
		record.GenesisPayloadDigest == value.GenesisRef.PayloadDigest && record.PreviousGeneration == value.Genesis.PreviousGeneration &&
		record.Generation == value.Genesis.Generation && record.PreviousFence == value.Genesis.PreviousFence && record.Fence == value.Genesis.Fence &&
		record.CorpusContentHash == value.Subject.Corpus.ContentHash && record.ProviderRouteAuthorityHash == value.Subject.ProviderRoute.AuthorityHash &&
		record.RunnerImmutableDigest == value.Subject.Runner.ImmutableDigest && record.SourceTreeDigest == value.Subject.Source.TreeDigest &&
		record.TrustPolicyHash == value.Subject.TrustPolicyHash &&
		record.InitialRevocationAuthorityID == value.Genesis.RevocationAuthority.AuthorityID &&
		record.InitialRevocationAuthorityHash == value.Genesis.RevocationAuthority.AuthorityHash &&
		record.InitialRevocationAuthorityEpoch == value.Genesis.RevocationAuthority.Epoch
}

var _ GenesisBootstrapStore = (*MemoryActivationStore)(nil)
