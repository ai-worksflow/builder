package modelgovernance

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryActivationStore is a strict in-process reference implementation of
// ActivationStore. It exists for deterministic service and race tests; it is
// not a durable production registry.
type MemoryActivationStore struct {
	mu                sync.RWMutex
	clock             func() time.Time
	operations        map[string]ActivationRecord
	heads             map[string]ActivationRecord
	history           map[string]ActivationRecord
	activatedByExact  map[string]ActivationRecord
	revocationAnchor  *GovernanceRevocationAuthority
	trustPolicyAnchor *GovernanceTrustPolicyObservation
}

func NewMemoryActivationStore(clock func() time.Time) (*MemoryActivationStore, error) {
	if clock == nil {
		return nil, fmt.Errorf("%w: trusted clock is required", ErrGovernanceInvalid)
	}
	return &MemoryActivationStore{
		clock: clock, operations: map[string]ActivationRecord{}, heads: map[string]ActivationRecord{},
		history: map[string]ActivationRecord{}, activatedByExact: map[string]ActivationRecord{},
	}, nil
}

func (store *MemoryActivationStore) TrustedTime(_ context.Context) (time.Time, error) {
	if store == nil || store.clock == nil {
		return time.Time{}, fmt.Errorf("%w: trusted clock is unavailable", ErrRuntimeAuthority)
	}
	now := store.clock()
	normalized, err := normalizeGovernanceTrustedTime(now)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: trusted clock returned zero", ErrRuntimeAuthority)
	}
	return normalized, nil
}

func (store *MemoryActivationStore) AppendActivation(_ context.Context, command ActivationAppend) (ActivationRecord, error) {
	if store == nil {
		return ActivationRecord{}, fmt.Errorf("%w: activation store is nil", ErrActivationConflict)
	}
	if err := validateActivationAppend(command); err != nil {
		return ActivationRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	if existing, ok := store.operations[command.Record.OperationID]; ok {
		if !sameActivationRecord(existing, command.Record) {
			return ActivationRecord{}, fmt.Errorf("%w: operation id is bound to another request", ErrActivationConflict)
		}
		return existing, nil
	}
	current, exists := store.heads[command.Record.Workload]
	if exists {
		if current.Generation != command.ExpectedGeneration || current.Fence != command.ExpectedFence {
			return ActivationRecord{}, fmt.Errorf("%w: workload head does not match expected generation and fence", ErrActivationConflict)
		}
	} else {
		return ActivationRecord{}, fmt.Errorf("%w: ordinary activation cannot create an empty workload head; signed Genesis bootstrap is required", ErrActivationConflict)
	}
	if err := store.requireCurrentAuthorityAnchorsLocked(command.Record); err != nil {
		return ActivationRecord{}, err
	}
	historyKey := activationGenerationKey(command.Record.Workload, command.Record.Generation)
	if _, duplicate := store.history[historyKey]; duplicate {
		return ActivationRecord{}, fmt.Errorf("%w: generation already exists", ErrActivationConflict)
	}
	exactKey := activationProfileKey(CorpusProfileBinding{
		ID: command.Record.ProfileID, ContentHash: command.Record.ProfileContentHash, Workload: command.Record.Workload,
	})
	if existing, duplicate := store.activatedByExact[exactKey]; duplicate {
		return ActivationRecord{}, fmt.Errorf("%w: exact profile is already bound to activation receipt %s", ErrActivationConflict, existing.ReceiptDigest)
	}
	record := command.Record
	record.ActivatedAt = record.ActivatedAt.UTC()
	store.operations[record.OperationID] = record
	store.history[historyKey] = record
	store.heads[record.Workload] = record
	store.activatedByExact[exactKey] = record
	return record, nil
}

func (store *MemoryActivationStore) AppendGenesis(_ context.Context, command GenesisAppend) (ActivationRecord, error) {
	if store == nil {
		return ActivationRecord{}, fmt.Errorf("%w: Genesis store is nil", ErrActivationConflict)
	}
	if err := validateGenesisAppend(command); err != nil {
		return ActivationRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record := command.Record
	record.ActivatedAt = record.ActivatedAt.UTC()
	if existing, ok := store.operations[record.OperationID]; ok {
		if !sameActivationRecord(existing, record) {
			return ActivationRecord{}, fmt.Errorf("%w: Genesis operation id is bound to different bytes", ErrActivationConflict)
		}
		return existing, nil
	}
	if store.revocationAnchor == nil || store.revocationAnchor.AuthorityHash != command.CurrentRevocationAuthorityHash ||
		store.revocationAnchor.Epoch != command.CurrentRevocationAuthorityEpoch || store.trustPolicyAnchor == nil ||
		store.trustPolicyAnchor.PolicyHash != command.CurrentTrustPolicyHash ||
		store.trustPolicyAnchor.RevocationAuthorityHash != command.CurrentRevocationAuthorityHash ||
		store.trustPolicyAnchor.RevocationEpoch != command.CurrentRevocationAuthorityEpoch {
		return ActivationRecord{}, fmt.Errorf("%w: Genesis trust or revocation authority drifted", ErrActivationConflict)
	}
	if err := store.requireCurrentAuthorityAnchorsLocked(record); err != nil {
		return ActivationRecord{}, err
	}
	if _, exists := store.heads[record.Workload]; exists {
		return ActivationRecord{}, fmt.Errorf("%w: Genesis requires an empty workload head", ErrActivationConflict)
	}
	historyKey := activationGenerationKey(record.Workload, record.Generation)
	if _, duplicate := store.history[historyKey]; duplicate {
		return ActivationRecord{}, fmt.Errorf("%w: Genesis generation already exists", ErrActivationConflict)
	}
	exactKey := activationProfileKey(CorpusProfileBinding{ID: record.ProfileID, ContentHash: record.ProfileContentHash, Workload: record.Workload})
	if _, duplicate := store.activatedByExact[exactKey]; duplicate {
		return ActivationRecord{}, fmt.Errorf("%w: Genesis exact profile already exists", ErrActivationConflict)
	}
	store.operations[record.OperationID] = record
	store.history[historyKey] = record
	store.heads[record.Workload] = record
	store.activatedByExact[exactKey] = record
	return record, nil
}

func (store *MemoryActivationStore) GetActivationOperation(_ context.Context, operationID string) (ActivationRecord, error) {
	if store == nil || !validUUIDv4(operationID) {
		return ActivationRecord{}, ErrActivationNotFound
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, ok := store.operations[operationID]
	if !ok {
		return ActivationRecord{}, ErrActivationNotFound
	}
	return record, nil
}

func (store *MemoryActivationStore) GetActiveActivation(_ context.Context, workload string) (ActivationRecord, error) {
	if store == nil || !validStableID(workload) {
		return ActivationRecord{}, ErrActivationNotFound
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, ok := store.heads[workload]
	if !ok {
		return ActivationRecord{}, ErrActivationNotFound
	}
	return record, nil
}

func (store *MemoryActivationStore) GetActivationGeneration(_ context.Context, workload string, generation uint64) (ActivationRecord, error) {
	if store == nil || !validStableID(workload) || generation == 0 {
		return ActivationRecord{}, ErrActivationNotFound
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, ok := store.history[activationGenerationKey(workload, generation)]
	if !ok {
		return ActivationRecord{}, ErrActivationNotFound
	}
	return record, nil
}

func (store *MemoryActivationStore) GetActivatedProfile(_ context.Context, binding CorpusProfileBinding) (ActivationRecord, error) {
	if store == nil || !validUUIDv4(binding.ID) || !validDigest(binding.ContentHash) || !validStableID(binding.Workload) {
		return ActivationRecord{}, ErrActivationNotFound
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, ok := store.activatedByExact[activationProfileKey(binding)]
	if !ok {
		return ActivationRecord{}, ErrActivationNotFound
	}
	return record, nil
}

func (store *MemoryActivationStore) ObserveGovernanceRevocationAuthority(_ context.Context, next GovernanceRevocationAuthority) error {
	actualHash, hashErr := GovernanceRevocationAuthorityHash(next)
	if store == nil || !validDigest(next.AuthorityHash) || hashErr != nil || actualHash != next.AuthorityHash {
		return fmt.Errorf("%w: revocation authority observation is invalid", ErrGovernanceUntrusted)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.revocationAnchor == nil {
		cloned := cloneGovernanceRevocationAuthority(next)
		store.revocationAnchor = &cloned
		return nil
	}
	current := *store.revocationAnchor
	if next.Epoch < current.Epoch || (next.Epoch == current.Epoch && next.AuthorityHash != current.AuthorityHash) ||
		(next.Epoch > current.Epoch && (!revocationsContainAll(current.DigestRevocations, next.DigestRevocations) ||
			!signerRevocationsContainAll(current.SignerRevocations, next.SignerRevocations))) {
		return fmt.Errorf("%w: revocation authority rollback, equivocation, or deletion", ErrGovernanceUntrusted)
	}
	if next.Epoch == current.Epoch {
		return nil
	}
	cloned := cloneGovernanceRevocationAuthority(next)
	store.revocationAnchor = &cloned
	return nil
}

func (store *MemoryActivationStore) ObserveGovernanceTrustPolicy(_ context.Context, next GovernanceTrustPolicyObservation) error {
	if store == nil || !validDigest(next.PolicyHash) || !validDigest(next.RevocationAuthorityHash) || next.RevocationEpoch == 0 {
		return fmt.Errorf("%w: trust-policy observation is invalid", ErrGovernanceUntrusted)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.revocationAnchor == nil || store.revocationAnchor.Epoch != next.RevocationEpoch ||
		store.revocationAnchor.AuthorityHash != next.RevocationAuthorityHash {
		return fmt.Errorf("%w: trust-policy observation is not bound to the exact durable revocation authority", ErrGovernanceUntrusted)
	}
	if store.trustPolicyAnchor == nil {
		cloned := next
		store.trustPolicyAnchor = &cloned
		return nil
	}
	current := *store.trustPolicyAnchor
	if next.RevocationEpoch < current.RevocationEpoch ||
		(next.RevocationEpoch == current.RevocationEpoch && next != current) {
		return fmt.Errorf("%w: trust-policy rollback or same-epoch equivocation", ErrGovernanceUntrusted)
	}
	if next.RevocationEpoch == current.RevocationEpoch {
		return nil
	}
	cloned := next
	store.trustPolicyAnchor = &cloned
	return nil
}

func (store *MemoryActivationStore) requireCurrentAuthorityAnchorsLocked(record ActivationRecord) error {
	if store.revocationAnchor == nil || store.trustPolicyAnchor == nil ||
		store.trustPolicyAnchor.PolicyHash != record.TrustPolicyHash ||
		store.trustPolicyAnchor.RevocationAuthorityHash != store.revocationAnchor.AuthorityHash ||
		store.trustPolicyAnchor.RevocationEpoch != store.revocationAnchor.Epoch {
		return fmt.Errorf("%w: current trust/revocation anchor is missing or drifted", ErrActivationConflict)
	}
	now, err := normalizeGovernanceTrustedTime(store.clock())
	if err != nil || record.ActivatedAt.Before(now.Add(-MaximumGovernanceClockSkew)) || record.ActivatedAt.After(now.Add(MaximumGovernanceClockSkew)) ||
		now.Before(store.revocationAnchor.IssuedAt.Add(-MaximumGovernanceClockSkew)) || !now.Before(store.revocationAnchor.ExpiresAt) {
		return fmt.Errorf("%w: activation time or current revocation anchor is outside the trusted-time fence", ErrActivationConflict)
	}
	return nil
}

func validateActivationAppend(command ActivationAppend) error {
	record := command.Record
	if record.AuthorityKind != ActivationAuthorityKind || record.GenesisEnvelopeDigest != "" || record.GenesisPayloadDigest != "" ||
		record.InitialRevocationAuthorityID != "" || record.InitialRevocationAuthorityHash != "" || record.InitialRevocationAuthorityEpoch != 0 ||
		!validUUIDv4(record.OperationID) || !validDigest(record.RequestHash) || !validStableID(record.Workload) ||
		!validUUIDv4(record.ProfileID) || !validDigest(record.ProfileContentHash) || !validDigest(record.ReceiptDigest) ||
		!validDigest(record.ReceiptPayloadDigest) || !validDigest(record.ActivationEnvelopeDigest) || !validDigest(record.ActivationPayloadDigest) ||
		record.Generation == 0 || record.Generation != record.PreviousGeneration+1 || record.PreviousGeneration != command.ExpectedGeneration ||
		!validDigest(record.PreviousFence) || !validDigest(record.Fence) || record.PreviousFence == record.Fence || record.PreviousFence != command.ExpectedFence ||
		!validDigest(record.CorpusContentHash) || !validDigest(record.ProviderRouteAuthorityHash) || !validDigest(record.RunnerImmutableDigest) ||
		!validDigest(record.SourceTreeDigest) || !validDigest(record.TrustPolicyHash) || record.ActivatedAt.IsZero() {
		return fmt.Errorf("%w: activation append is incomplete or structurally inconsistent", ErrActivationConflict)
	}
	expectedRequestHash, err := activationRequestHash(ActivationRequest{
		OperationID: record.OperationID, ReceiptDigest: record.ReceiptDigest,
		ExpectedGeneration: record.PreviousGeneration, ExpectedFence: record.PreviousFence,
	})
	if err != nil || expectedRequestHash != record.RequestHash || !canonicalGovernanceTime(record.ActivatedAt) {
		return fmt.Errorf("%w: activation request hash or activation time is not canonical", ErrActivationConflict)
	}
	return nil
}

func validateGenesisAppend(command GenesisAppend) error {
	record := command.Record
	if record.AuthorityKind != GenesisAuthorityKind || record.Generation != 1 || record.PreviousGeneration != 0 ||
		command.ExpectedGeneration != 0 || record.PreviousFence != command.ExpectedFence ||
		!validDigest(record.GenesisEnvelopeDigest) || !validDigest(record.GenesisPayloadDigest) ||
		record.GenesisEnvelopeDigest == record.GenesisPayloadDigest ||
		record.ActivationEnvelopeDigest != record.GenesisEnvelopeDigest ||
		record.ActivationPayloadDigest != record.GenesisPayloadDigest ||
		record.InitialRevocationAuthorityID != GovernanceRevocationAuthorityID ||
		!validDigest(record.InitialRevocationAuthorityHash) || record.InitialRevocationAuthorityEpoch == 0 ||
		command.CurrentTrustPolicyHash != record.TrustPolicyHash ||
		command.CurrentRevocationAuthorityHash != record.InitialRevocationAuthorityHash ||
		command.CurrentRevocationAuthorityEpoch != record.InitialRevocationAuthorityEpoch ||
		!validUUIDv4(record.OperationID) || !validDigest(record.RequestHash) || !validStableID(record.Workload) ||
		!validUUIDv4(record.ProfileID) || !validDigest(record.ProfileContentHash) || !validDigest(record.ReceiptDigest) ||
		!validDigest(record.ReceiptPayloadDigest) || !validDigest(record.PreviousFence) || !validDigest(record.Fence) ||
		record.PreviousFence == record.Fence || !validDigest(record.CorpusContentHash) ||
		!validDigest(record.ProviderRouteAuthorityHash) || !validDigest(record.RunnerImmutableDigest) ||
		!validDigest(record.SourceTreeDigest) || !validDigest(record.TrustPolicyHash) || record.ActivatedAt.IsZero() ||
		!canonicalGovernanceTime(record.ActivatedAt) {
		return fmt.Errorf("%w: Genesis append is incomplete or structurally inconsistent", ErrActivationConflict)
	}
	expectedRequestHash, err := genesisBootstrapRequestHash(GenesisBootstrapRequest{
		OperationID: record.OperationID, ReceiptDigest: record.ReceiptDigest, ExpectedEmptyFence: record.PreviousFence,
	})
	if err != nil || expectedRequestHash != record.RequestHash {
		return fmt.Errorf("%w: Genesis request hash is not canonical", ErrActivationConflict)
	}
	return nil
}

func validateRegistryRecord(record ActivationRecord) error {
	if record.AuthorityKind == GenesisAuthorityKind {
		return validateGenesisAppend(GenesisAppend{
			ExpectedGeneration: 0, ExpectedFence: record.PreviousFence,
			CurrentTrustPolicyHash:          record.TrustPolicyHash,
			CurrentRevocationAuthorityHash:  record.InitialRevocationAuthorityHash,
			CurrentRevocationAuthorityEpoch: record.InitialRevocationAuthorityEpoch,
			Record:                          record,
		})
	}
	return validateActivationAppend(ActivationAppend{
		ExpectedGeneration: record.PreviousGeneration, ExpectedFence: record.PreviousFence, Record: record,
	})
}

func activationGenerationKey(workload string, generation uint64) string {
	return fmt.Sprintf("%s\x00%020d", workload, generation)
}

func activationProfileKey(binding CorpusProfileBinding) string {
	return binding.Workload + "\x00" + binding.ID + "\x00" + binding.ContentHash
}

// MemoryGovernanceAuthority is a copy-on-read exact-material/trust/disable
// authority for deterministic service tests. Mutators validate bindings and
// never derive a missing entry from another receipt.
type MemoryGovernanceAuthority struct {
	mu               sync.RWMutex
	materials        map[string]GovernanceMaterials
	genesisMaterials map[string]GenesisGovernanceMaterials
	routes           map[string][]byte
	policy           GovernanceTrustPolicy
	policies         map[string]GovernanceTrustPolicy
	revocations      GovernanceRevocationAuthority
	disables         map[string]ProfileDisableState
}

func NewMemoryGovernanceAuthority(policy GovernanceTrustPolicy, revocations GovernanceRevocationAuthority) (*MemoryGovernanceAuthority, error) {
	if err := ValidateGovernanceTrustPolicy(policy); err != nil {
		return nil, err
	}
	if err := ValidateGovernanceRevocationAuthority(revocations, revocations.IssuedAt); err != nil {
		return nil, err
	}
	return &MemoryGovernanceAuthority{
		materials: map[string]GovernanceMaterials{}, genesisMaterials: map[string]GenesisGovernanceMaterials{}, routes: map[string][]byte{}, policy: cloneGovernanceTrustPolicy(policy),
		policies:    map[string]GovernanceTrustPolicy{policy.PolicyHash: cloneGovernanceTrustPolicy(policy)},
		revocations: cloneGovernanceRevocationAuthority(revocations), disables: map[string]ProfileDisableState{},
	}, nil
}

func (authority *MemoryGovernanceAuthority) SetCurrentProviderRouteAuthority(encoded []byte) error {
	if authority == nil || len(encoded) == 0 {
		return fmt.Errorf("%w: provider route authority is empty", ErrGovernanceInvalid)
	}
	digest := sha256Digest(encoded)
	route, err := ParseProviderRouteAuthority(encoded, digest)
	if err != nil {
		return err
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.routes[route.RouteID] = bytes.Clone(encoded)
	return nil
}

func (authority *MemoryGovernanceAuthority) PutGovernanceMaterials(receiptDigest string, materials GovernanceMaterials) error {
	if authority == nil || !validDigest(receiptDigest) || len(materials.ReceiptEnvelope) == 0 || sha256Digest(materials.ReceiptEnvelope) != receiptDigest {
		return fmt.Errorf("%w: exact receipt envelope digest is invalid", ErrGovernanceInvalid)
	}
	cloned := cloneGovernanceMaterials(materials)
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if _, collision := authority.genesisMaterials[receiptDigest]; collision {
		return fmt.Errorf("%w: receipt digest is already bound to Genesis materials", ErrGovernanceInvalid)
	}
	if existing, ok := authority.materials[receiptDigest]; ok && !sameGovernanceMaterials(existing, cloned) {
		return fmt.Errorf("%w: immutable materials already exist for receipt", ErrGovernanceInvalid)
	}
	authority.materials[receiptDigest] = cloned
	return nil
}

func (authority *MemoryGovernanceAuthority) PutGenesisGovernanceMaterials(receiptDigest string, materials GenesisGovernanceMaterials) error {
	if authority == nil || !validDigest(receiptDigest) || len(materials.ReceiptEnvelope) == 0 || sha256Digest(materials.ReceiptEnvelope) != receiptDigest {
		return fmt.Errorf("%w: exact Genesis receipt envelope digest is invalid", ErrGovernanceInvalid)
	}
	cloned := cloneGenesisGovernanceMaterials(materials)
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if existing, ok := authority.genesisMaterials[receiptDigest]; ok && !sameGenesisGovernanceMaterials(existing, cloned) {
		return fmt.Errorf("%w: immutable Genesis materials already exist for receipt", ErrGovernanceInvalid)
	}
	if _, collision := authority.materials[receiptDigest]; collision {
		return fmt.Errorf("%w: receipt digest is already bound to ordinary activation materials", ErrGovernanceInvalid)
	}
	authority.genesisMaterials[receiptDigest] = cloned
	return nil
}

func (authority *MemoryGovernanceAuthority) SetTrustPolicy(policy GovernanceTrustPolicy) error {
	if authority == nil {
		return fmt.Errorf("%w: authority is nil", ErrGovernanceUntrusted)
	}
	if err := ValidateGovernanceTrustPolicy(policy); err != nil {
		return err
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if existing, ok := authority.policies[policy.PolicyHash]; ok && !sameGovernanceTrustPolicy(existing, policy) {
		return fmt.Errorf("%w: immutable trust policy hash is already bound to different bytes", ErrGovernanceUntrusted)
	}
	authority.policies[policy.PolicyHash] = cloneGovernanceTrustPolicy(policy)
	authority.policy = cloneGovernanceTrustPolicy(policy)
	return nil
}

func (authority *MemoryGovernanceAuthority) SetRevocationAuthority(next GovernanceRevocationAuthority) error {
	if authority == nil {
		return fmt.Errorf("%w: authority is nil", ErrGovernanceUntrusted)
	}
	if err := ValidateGovernanceRevocationAuthority(next, next.IssuedAt); err != nil {
		return err
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	current := authority.revocations
	if next.Epoch == current.Epoch && next.AuthorityHash == current.AuthorityHash {
		return nil
	}
	if next.Epoch != current.Epoch+1 || !next.IssuedAt.After(current.IssuedAt) ||
		!revocationsContainAll(current.DigestRevocations, next.DigestRevocations) ||
		!signerRevocationsContainAll(current.SignerRevocations, next.SignerRevocations) {
		return fmt.Errorf("%w: revocation authority update is not a monotonic cumulative epoch", ErrGovernanceUntrusted)
	}
	authority.revocations = cloneGovernanceRevocationAuthority(next)
	return nil
}

func (authority *MemoryGovernanceAuthority) SetProfileDisableState(state ProfileDisableState) error {
	if authority == nil || !validDisableQuery(state.Query) || state.ActiveConditions == nil || state.CheckedAt.IsZero() ||
		state.ExpiresAt.IsZero() || !state.ExpiresAt.After(state.CheckedAt) || !canonicalGovernanceTime(state.CheckedAt) ||
		!canonicalGovernanceTime(state.ExpiresAt) || state.ExpiresAt.Sub(state.CheckedAt) > MaximumDisableStateLifetime {
		return fmt.Errorf("%w: disable state is incomplete", ErrGovernanceInvalid)
	}
	cloned := cloneDisableState(state)
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.disables[disableQueryKey(state.Query)] = cloned
	return nil
}

func (authority *MemoryGovernanceAuthority) LoadGovernanceMaterials(_ context.Context, receiptDigest string) (GovernanceMaterials, error) {
	if authority == nil || !validDigest(receiptDigest) {
		return GovernanceMaterials{}, ErrActivationNotFound
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	materials, ok := authority.materials[receiptDigest]
	if !ok {
		return GovernanceMaterials{}, ErrActivationNotFound
	}
	return cloneGovernanceMaterials(materials), nil
}

func (authority *MemoryGovernanceAuthority) LoadGenesisGovernanceMaterials(_ context.Context, receiptDigest string) (GenesisGovernanceMaterials, error) {
	if authority == nil || !validDigest(receiptDigest) {
		return GenesisGovernanceMaterials{}, ErrActivationNotFound
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	materials, ok := authority.genesisMaterials[receiptDigest]
	if !ok {
		return GenesisGovernanceMaterials{}, ErrActivationNotFound
	}
	return cloneGenesisGovernanceMaterials(materials), nil
}

func (authority *MemoryGovernanceAuthority) LoadGovernanceTrustPolicy(_ context.Context, policyHash string) (GovernanceTrustPolicy, error) {
	if authority == nil || !validDigest(policyHash) {
		return GovernanceTrustPolicy{}, ErrGovernanceUntrusted
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	policy, ok := authority.policies[policyHash]
	if !ok {
		return GovernanceTrustPolicy{}, ErrGovernanceUntrusted
	}
	return cloneGovernanceTrustPolicy(policy), nil
}

func (authority *MemoryGovernanceAuthority) CurrentGovernanceTrustPolicy(_ context.Context) (GovernanceTrustPolicy, error) {
	if authority == nil {
		return GovernanceTrustPolicy{}, ErrGovernanceUntrusted
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	return cloneGovernanceTrustPolicy(authority.policy), nil
}

func (authority *MemoryGovernanceAuthority) CurrentGovernanceRevocationAuthority(_ context.Context) (GovernanceRevocationAuthority, error) {
	if authority == nil {
		return GovernanceRevocationAuthority{}, ErrGovernanceUntrusted
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	return cloneGovernanceRevocationAuthority(authority.revocations), nil
}

func (authority *MemoryGovernanceAuthority) CurrentProviderRouteAuthority(_ context.Context, routeID string) ([]byte, error) {
	if authority == nil || !validStableID(routeID) {
		return nil, ErrRuntimeAuthority
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	encoded, ok := authority.routes[routeID]
	if !ok {
		return nil, ErrRuntimeAuthority
	}
	return bytes.Clone(encoded), nil
}

func (authority *MemoryGovernanceAuthority) CurrentProfileDisableState(_ context.Context, query RuntimeDisableQuery) (ProfileDisableState, error) {
	if authority == nil || !validDisableQuery(query) {
		return ProfileDisableState{}, ErrProfileDisabled
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	state, ok := authority.disables[disableQueryKey(query)]
	if !ok {
		return ProfileDisableState{}, ErrProfileDisabled
	}
	return cloneDisableState(state), nil
}

func validDisableQuery(query RuntimeDisableQuery) bool {
	return validStableID(query.Workload) && validUUIDv4(query.ProfileID) && validDigest(query.ProfileContentHash) &&
		validDigest(query.ReceiptDigest) && query.Generation > 0 && validDigest(query.Fence)
}

func disableQueryKey(query RuntimeDisableQuery) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%020d\x00%s", query.Workload, query.ProfileID, query.ProfileContentHash, query.ReceiptDigest, query.Generation, query.Fence)
}

func cloneDisableState(state ProfileDisableState) ProfileDisableState {
	cloned := make([]string, len(state.ActiveConditions))
	copy(cloned, state.ActiveConditions)
	state.ActiveConditions = cloned
	return state
}

func cloneGovernanceMaterials(materials GovernanceMaterials) GovernanceMaterials {
	return GovernanceMaterials{
		ModelProfileJSON: bytes.Clone(materials.ModelProfileJSON), FrozenCorpusJSON: bytes.Clone(materials.FrozenCorpusJSON),
		ProviderRouteAuthorityJSON: bytes.Clone(materials.ProviderRouteAuthorityJSON), ConformanceEnvelope: bytes.Clone(materials.ConformanceEnvelope),
		ShadowEnvelope: bytes.Clone(materials.ShadowEnvelope), ApprovalEnvelope: bytes.Clone(materials.ApprovalEnvelope),
		ActivationEnvelope: bytes.Clone(materials.ActivationEnvelope), ReceiptEnvelope: bytes.Clone(materials.ReceiptEnvelope),
	}
}

func sameGovernanceMaterials(left, right GovernanceMaterials) bool {
	return bytes.Equal(left.ModelProfileJSON, right.ModelProfileJSON) && bytes.Equal(left.FrozenCorpusJSON, right.FrozenCorpusJSON) &&
		bytes.Equal(left.ProviderRouteAuthorityJSON, right.ProviderRouteAuthorityJSON) && bytes.Equal(left.ConformanceEnvelope, right.ConformanceEnvelope) &&
		bytes.Equal(left.ShadowEnvelope, right.ShadowEnvelope) && bytes.Equal(left.ApprovalEnvelope, right.ApprovalEnvelope) &&
		bytes.Equal(left.ActivationEnvelope, right.ActivationEnvelope) && bytes.Equal(left.ReceiptEnvelope, right.ReceiptEnvelope)
}

func cloneGenesisGovernanceMaterials(materials GenesisGovernanceMaterials) GenesisGovernanceMaterials {
	return GenesisGovernanceMaterials{
		ModelProfileJSON: bytes.Clone(materials.ModelProfileJSON), FrozenCorpusJSON: bytes.Clone(materials.FrozenCorpusJSON),
		ProviderRouteAuthorityJSON: bytes.Clone(materials.ProviderRouteAuthorityJSON), ConformanceEnvelope: bytes.Clone(materials.ConformanceEnvelope),
		ApprovalEnvelope: bytes.Clone(materials.ApprovalEnvelope), GenesisEnvelope: bytes.Clone(materials.GenesisEnvelope),
		ReceiptEnvelope: bytes.Clone(materials.ReceiptEnvelope),
	}
}

func sameGenesisGovernanceMaterials(left, right GenesisGovernanceMaterials) bool {
	return bytes.Equal(left.ModelProfileJSON, right.ModelProfileJSON) && bytes.Equal(left.FrozenCorpusJSON, right.FrozenCorpusJSON) &&
		bytes.Equal(left.ProviderRouteAuthorityJSON, right.ProviderRouteAuthorityJSON) && bytes.Equal(left.ConformanceEnvelope, right.ConformanceEnvelope) &&
		bytes.Equal(left.ApprovalEnvelope, right.ApprovalEnvelope) && bytes.Equal(left.GenesisEnvelope, right.GenesisEnvelope) &&
		bytes.Equal(left.ReceiptEnvelope, right.ReceiptEnvelope)
}

func revocationsContainAll(current, next []GovernanceRevocation) bool {
	byDigest := make(map[string]GovernanceRevocation, len(next))
	for _, revocation := range next {
		byDigest[revocation.Digest] = revocation
	}
	for _, revocation := range current {
		if byDigest[revocation.Digest] != revocation {
			return false
		}
	}
	return true
}

func signerRevocationsContainAll(current, next []GovernanceSignerRevocation) bool {
	bySelector := make(map[string]GovernanceSignerRevocation, len(next))
	for _, revocation := range next {
		bySelector[revocation.PolicyHash+"\x00"+revocation.KeyID] = revocation
	}
	for _, revocation := range current {
		if bySelector[revocation.PolicyHash+"\x00"+revocation.KeyID] != revocation {
			return false
		}
	}
	return true
}

func sameGovernanceTrustPolicy(left, right GovernanceTrustPolicy) bool {
	leftJSON, leftErr := CanonicalGovernanceTrustPolicyJSON(left)
	rightJSON, rightErr := CanonicalGovernanceTrustPolicyJSON(right)
	return leftErr == nil && rightErr == nil && left.PolicyHash == right.PolicyHash && bytes.Equal(leftJSON, rightJSON)
}
