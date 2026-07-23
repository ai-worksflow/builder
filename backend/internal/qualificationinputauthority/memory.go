package qualificationinputauthority

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// MemoryStore is a deterministic semantic reference. Its mutex models the
// required atomic recheck-and-append boundary, not production PostgreSQL
// session affinity or role posture.
type MemoryStore struct {
	mu sync.Mutex

	resolved map[string]ResolvedAuthorities

	receiptAdmissions map[string]ReceiptAdmissionRecord
	receiptByRequest  map[string]string

	byOperation     map[uuid.UUID]Record
	byAuthority     map[uuid.UUID]uuid.UUID
	byWorkflowInput map[uuid.UUID]uuid.UUID
	byPlan          map[uuid.UUID]uuid.UUID
	localIDs        map[uuid.UUID]uuid.UUID
	upstreamIDs     map[uuid.UUID]struct{}

	unknownAdmissionAfterCommitOnce bool
	unknownIssueAfterCommitOnce     bool
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		resolved:          make(map[string]ResolvedAuthorities),
		receiptAdmissions: make(map[string]ReceiptAdmissionRecord),
		receiptByRequest:  make(map[string]string),
		byOperation:       make(map[uuid.UUID]Record),
		byAuthority:       make(map[uuid.UUID]uuid.UUID),
		byWorkflowInput:   make(map[uuid.UUID]uuid.UUID),
		byPlan:            make(map[uuid.UUID]uuid.UUID),
		localIDs:          make(map[uuid.UUID]uuid.UUID),
		upstreamIDs:       make(map[uuid.UUID]struct{}),
	}
}

// InstallAuthorities installs a trusted server fixture. It is not a public
// production ingestion API.
func (store *MemoryStore) InstallAuthorities(resolved ResolvedAuthorities) error {
	if store == nil {
		return invalid("memoryStore", "store is required")
	}
	if err := ValidateResolvedAuthorities(resolved); err != nil {
		return err
	}
	key := resolvedKey(resolved.WorkflowInput.AuthorityID, resolved.Policy.AuthorityID, resolved.Plan.AuthorityID)
	identities := []string{resolved.WorkflowInput.AuthorityID, resolved.Policy.AuthorityID, resolved.Plan.AuthorityID}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, found := store.resolved[key]; found && existing != resolved {
		return fmt.Errorf("%w: exact upstream key is already bound to different facts", ErrConflict)
	}
	for _, identity := range identities {
		parsed := uuid.MustParse(identity)
		if _, collision := store.localIDs[parsed]; collision {
			return fmt.Errorf("%w: upstream identity collides with a precommit allocation", ErrConflict)
		}
	}
	store.resolved[key] = cloneResolvedAuthorities(resolved)
	for _, identity := range identities {
		store.upstreamIDs[uuid.MustParse(identity)] = struct{}{}
	}
	return nil
}

func (store *MemoryStore) SetPolicyState(policyAuthorityID uuid.UUID, current bool, status string) error {
	if store == nil || !validUUIDv4Value(policyAuthorityID) || (status != PolicyStatusActive && status != "suspended") {
		return invalid("memoryStore.policyState", "canonical Policy identity and closed status are required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	found := false
	for key, resolved := range store.resolved {
		if resolved.Policy.AuthorityID == policyAuthorityID.String() {
			resolved.PolicyCurrent = current
			resolved.PolicyStatus = status
			store.resolved[key] = resolved
			found = true
		}
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

func (store *MemoryStore) Resolve(
	ctx context.Context,
	workflowInputAuthorityID uuid.UUID,
	policyAuthorityID uuid.UUID,
	planAuthorityID uuid.UUID,
) (ResolvedAuthorities, error) {
	if store == nil || ctx == nil || !validUUIDv4Value(workflowInputAuthorityID) ||
		!validUUIDv4Value(policyAuthorityID) || !validUUIDv4Value(planAuthorityID) {
		return ResolvedAuthorities{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return ResolvedAuthorities{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	resolved, found := store.resolved[resolvedKey(workflowInputAuthorityID.String(), policyAuthorityID.String(), planAuthorityID.String())]
	if !found {
		return ResolvedAuthorities{}, ErrNotFound
	}
	return cloneResolvedAuthorities(resolved), nil
}

func (store *MemoryStore) admitSourceReceipt(ctx context.Context, grant verifiedSourceGrant) (ReceiptAdmissionRecord, error) {
	return store.admitReceipt(ctx, ReceiptKindSource, grant.proof, grant.requestBytes)
}

func (store *MemoryStore) admitCredentialReceipt(ctx context.Context, grant verifiedCredentialGrant) (ReceiptAdmissionRecord, error) {
	return store.admitReceipt(ctx, ReceiptKindCredential, grant.proof, grant.requestBytes)
}

func (store *MemoryStore) admitReceipt(ctx context.Context, kind string, proof VerificationProof, requestBytes []byte) (ReceiptAdmissionRecord, error) {
	if store == nil || ctx == nil {
		return ReceiptAdmissionRecord{}, invalid("memoryStore.receiptAdmission", "store and context are required")
	}
	if err := ctx.Err(); err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	candidate, err := compileReceiptAdmission(kind, proof, requestBytes)
	if err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existingHash, found := store.receiptByRequest[receiptRequestKey(kind, proof.RequestHash)]; found {
		existing := store.receiptAdmissions[existingHash]
		if !sameReceiptAdmission(existing, candidate) {
			return ReceiptAdmissionRecord{}, fmt.Errorf("%w: exact verification request is bound to another receipt", ErrConflict)
		}
		return cloneReceiptAdmission(existing), nil
	}
	if existing, found := store.receiptAdmissions[candidate.AdmissionHash]; found {
		if !sameReceiptAdmission(existing, candidate) {
			return ReceiptAdmissionRecord{}, fmt.Errorf("%w: receipt admission hash collision", ErrConflict)
		}
		return cloneReceiptAdmission(existing), nil
	}
	store.receiptAdmissions[candidate.AdmissionHash] = cloneReceiptAdmission(candidate)
	store.receiptByRequest[receiptRequestKey(kind, proof.RequestHash)] = candidate.AdmissionHash
	if store.unknownAdmissionAfterCommitOnce {
		store.unknownAdmissionAfterCommitOnce = false
		return ReceiptAdmissionRecord{}, ErrStoreOutcomeUnknown
	}
	return cloneReceiptAdmission(candidate), nil
}

func (store *MemoryStore) resolveReceiptAdmission(
	ctx context.Context,
	kind string,
	admissionHash string,
) (ReceiptAdmissionRecord, error) {
	if store == nil || ctx == nil || (kind != ReceiptKindSource && kind != ReceiptKindCredential) ||
		!validDigest(admissionHash) {
		return ReceiptAdmissionRecord{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found := store.receiptAdmissions[admissionHash]
	if !found || record.Document.Kind != kind {
		return ReceiptAdmissionRecord{}, ErrNotFound
	}
	if err := validateReceiptAdmissionRecord(record); err != nil {
		return ReceiptAdmissionRecord{}, fmt.Errorf("%w: stored receipt admission is corrupt", ErrConflict)
	}
	return cloneReceiptAdmission(record), nil
}

func (store *MemoryStore) resolveReceiptAdmissionForRequest(
	ctx context.Context,
	kind string,
	requestHash string,
) (ReceiptAdmissionRecord, error) {
	if store == nil || ctx == nil || (kind != ReceiptKindSource && kind != ReceiptKindCredential) || !validDigest(requestHash) {
		return ReceiptAdmissionRecord{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	admissionHash, found := store.receiptByRequest[receiptRequestKey(kind, requestHash)]
	if !found {
		return ReceiptAdmissionRecord{}, ErrNotFound
	}
	record, found := store.receiptAdmissions[admissionHash]
	if !found || record.Document.Kind != kind || record.Document.RequestHash != requestHash {
		return ReceiptAdmissionRecord{}, fmt.Errorf("%w: receipt request index is corrupt", ErrConflict)
	}
	if err := validateReceiptAdmissionRecord(record); err != nil {
		return ReceiptAdmissionRecord{}, fmt.Errorf("%w: stored receipt admission is corrupt", ErrConflict)
	}
	return cloneReceiptAdmission(record), nil
}

func (store *MemoryStore) Issue(ctx context.Context, candidate Record) (Record, error) {
	if store == nil || ctx == nil {
		return Record{}, invalid("memoryStore", "store and context are required")
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := ValidateRecord(candidate); err != nil {
		return Record{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	if existing, found := store.byOperation[candidate.Command.OperationID]; found {
		if existing.Command != candidate.Command {
			return Record{}, fmt.Errorf("%w: operation identity is bound to another command", ErrConflict)
		}
		existing.Idempotent = true
		return cloneRecord(existing), nil
	}

	resolved, found := store.resolved[resolvedKey(
		candidate.Command.WorkflowInputAuthorityID.String(),
		candidate.Command.QualificationPolicyAuthorityID.String(),
		candidate.Command.QualificationPlanAuthorityID.String(),
	)]
	if !found {
		return Record{}, fmt.Errorf("%w: exact WIA/Policy/Plan tuple is unavailable", ErrNotReady)
	}
	if err := ValidateResolvedAuthorities(resolved); err != nil {
		return Record{}, err
	}
	if candidate.Document.WorkflowInput != resolved.WorkflowInput || candidate.Document.Policy != resolved.Policy ||
		candidate.Document.Plan != resolved.Plan {
		return Record{}, fmt.Errorf("%w: candidate does not equal the transaction-current upstream tuple", ErrStale)
	}
	if candidate.Document.SourceProof.AuthorityID != resolved.SourceVerifier.AuthorityID ||
		candidate.Document.SourceProof.ExecutableDigest != resolved.SourceVerifier.ExecutableDigest ||
		candidate.Document.CredentialProof.AuthorityID != resolved.CredentialResolver.AuthorityID ||
		candidate.Document.CredentialProof.ExecutableDigest != resolved.CredentialResolver.ExecutableDigest {
		return Record{}, fmt.Errorf("%w: candidate proof does not equal the reviewed executable bindings", ErrStale)
	}
	if err := store.requireProofAdmissionLocked(ReceiptKindSource, candidate.Document.SourceProof, candidate.SourceRequestBytes); err != nil {
		return Record{}, err
	}
	if err := store.requireProofAdmissionLocked(ReceiptKindCredential, candidate.Document.CredentialProof, candidate.CredentialRequestBytes); err != nil {
		return Record{}, err
	}
	if _, used := store.byWorkflowInput[candidate.Command.WorkflowInputAuthorityID]; used {
		return Record{}, fmt.Errorf("%w: WIA is already bound to another precommit", ErrConflict)
	}
	if _, used := store.byPlan[candidate.Command.QualificationPlanAuthorityID]; used {
		return Record{}, fmt.Errorf("%w: Plan is already bound to another precommit", ErrConflict)
	}
	for _, identity := range []uuid.UUID{candidate.Command.OperationID, candidate.Command.AuthorityID} {
		if _, upstream := store.upstreamIDs[identity]; upstream {
			return Record{}, fmt.Errorf("%w: precommit identity collides with an upstream authority", ErrConflict)
		}
		if _, used := store.localIDs[identity]; used {
			return Record{}, fmt.Errorf("%w: precommit identity is already reserved", ErrConflict)
		}
	}
	if _, used := store.byAuthority[candidate.Command.AuthorityID]; used {
		return Record{}, fmt.Errorf("%w: authority identity is already reserved", ErrConflict)
	}

	stored := cloneRecord(candidate)
	store.byOperation[candidate.Command.OperationID] = stored
	store.byAuthority[candidate.Command.AuthorityID] = candidate.Command.OperationID
	store.byWorkflowInput[candidate.Command.WorkflowInputAuthorityID] = candidate.Command.OperationID
	store.byPlan[candidate.Command.QualificationPlanAuthorityID] = candidate.Command.OperationID
	store.localIDs[candidate.Command.OperationID] = candidate.Command.OperationID
	store.localIDs[candidate.Command.AuthorityID] = candidate.Command.OperationID
	if store.unknownIssueAfterCommitOnce {
		store.unknownIssueAfterCommitOnce = false
		return Record{}, ErrStoreOutcomeUnknown
	}
	return cloneRecord(stored), nil
}

func (store *MemoryStore) requireProofAdmissionLocked(kind string, proof VerificationProof, requestBytes []byte) error {
	record, found := store.receiptAdmissions[proof.AdmissionHash]
	if !found {
		return fmt.Errorf("%w: exact %s receipt admission is unavailable", ErrNotReady, kind)
	}
	expected, err := compileReceiptAdmission(kind, proof, requestBytes)
	if err != nil {
		return err
	}
	if !sameReceiptAdmission(record, expected) {
		return fmt.Errorf("%w: %s receipt admission differs from the canonical proof", ErrConflict, kind)
	}
	return nil
}

func (store *MemoryStore) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	if store == nil || ctx == nil || !validUUIDv4Value(operationID) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found := store.byOperation[operationID]
	if !found {
		return Record{}, ErrNotFound
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, fmt.Errorf("%w: stored operation is corrupt", ErrConflict)
	}
	return cloneRecord(record), nil
}

func (store *MemoryStore) ResolveAuthority(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if store == nil || ctx == nil || !validUUIDv4Value(authorityID) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operationID, found := store.byAuthority[authorityID]
	if !found {
		return Record{}, ErrNotFound
	}
	record := store.byOperation[operationID]
	if err := ValidateRecord(record); err != nil {
		return Record{}, fmt.Errorf("%w: stored authority is corrupt", ErrConflict)
	}
	return cloneRecord(record), nil
}

func (store *MemoryStore) InjectAdmissionCommitUnknownOnce() {
	if store == nil {
		return
	}
	store.mu.Lock()
	store.unknownAdmissionAfterCommitOnce = true
	store.mu.Unlock()
}

func (store *MemoryStore) InjectIssueCommitUnknownOnce() {
	if store == nil {
		return
	}
	store.mu.Lock()
	store.unknownIssueAfterCommitOnce = true
	store.mu.Unlock()
}

func resolvedKey(workflowInputAuthorityID, policyAuthorityID, planAuthorityID string) string {
	return workflowInputAuthorityID + "\x00" + policyAuthorityID + "\x00" + planAuthorityID
}

func receiptRequestKey(kind, requestHash string) string {
	return kind + "\x00" + requestHash
}

var _ AuthorityResolver = (*MemoryStore)(nil)
var _ Store = (*MemoryStore)(nil)
