package qualificationinputauthority

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
)

const reconciliationTimeout = 5 * time.Second

type Service struct {
	clock              DatabaseClock
	credentialResolver CredentialResolver
	resolver           AuthorityResolver
	sourceVerifier     SourceVerifier
	store              Store
}

func NewService(
	resolver AuthorityResolver,
	sourceVerifier SourceVerifier,
	credentialResolver CredentialResolver,
	store Store,
	clock DatabaseClock,
) (*Service, error) {
	if isNilInterface(resolver) || isNilInterface(sourceVerifier) || isNilInterface(credentialResolver) ||
		isNilInterface(store) || isNilInterface(clock) {
		return nil, invalid("service", "trusted authority resolver, two sealed verifiers, immutable Store, and database clock are required")
	}
	sourceBinding := sourceVerifier.sourceBinding()
	credentialBinding := credentialResolver.credentialBinding()
	if err := validateExecutableBinding("service.sourceVerifier", sourceBinding); err != nil {
		return nil, err
	}
	if err := validateExecutableBinding("service.credentialResolver", credentialBinding); err != nil {
		return nil, err
	}
	if sourceBinding.AuthorityID == credentialBinding.AuthorityID ||
		sourceBinding.ExecutableDigest == credentialBinding.ExecutableDigest {
		return nil, invalid("service.verifierBindings", "source and credential identities and executable digests must be distinct")
	}
	return &Service{
		clock: clock, credentialResolver: credentialResolver, resolver: resolver,
		sourceVerifier: sourceVerifier, store: store,
	}, nil
}

// Issue performs external verification before the append transaction, then
// relies on Store.Issue to lock and revalidate every exact upstream fact and
// both locally admitted receipt records atomically with the authority append.
func (service *Service) Issue(ctx context.Context, command IssueCommand) (Record, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.resolver) ||
		isNilInterface(service.sourceVerifier) || isNilInterface(service.credentialResolver) ||
		isNilInterface(service.store) || isNilInterface(service.clock) {
		return Record{}, invalid("service", "service or dependencies are incomplete")
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := ValidateCommand(command); err != nil {
		return Record{}, err
	}

	existing, inspectErr := service.store.InspectOperation(ctx, command.OperationID)
	if inspectErr == nil {
		return replayRecord(existing, command)
	}
	if !errors.Is(inspectErr, ErrNotFound) {
		return Record{}, sanitizeInspectionError(inspectErr)
	}

	resolved, err := service.resolver.Resolve(
		ctx,
		command.WorkflowInputAuthorityID,
		command.QualificationPolicyAuthorityID,
		command.QualificationPlanAuthorityID,
	)
	if err != nil {
		return Record{}, sanitizePreflightError(err)
	}
	if err := ValidateResolvedAuthorities(resolved); err != nil {
		return Record{}, err
	}
	if resolved.WorkflowInput.AuthorityID != command.WorkflowInputAuthorityID.String() ||
		resolved.Policy.AuthorityID != command.QualificationPolicyAuthorityID.String() ||
		resolved.Plan.AuthorityID != command.QualificationPlanAuthorityID.String() {
		return Record{}, fmt.Errorf("%w: resolver returned another upstream authority set", ErrConflict)
	}
	if service.sourceVerifier.sourceBinding() != resolved.SourceVerifier ||
		service.credentialResolver.credentialBinding() != resolved.CredentialResolver {
		return Record{}, fmt.Errorf("%w: installed adapters do not equal the reviewed executable bindings", ErrConflict)
	}

	request := issueRequestFromCommand(command)
	requestBytes, requestHash, err := EncodeIssueRequest(request)
	if err != nil {
		return Record{}, err
	}
	sourceRequest := sourceRequestFromAuthoritySet(resolved)
	sourceBytes, sourceHash, err := EncodeSourceRequest(sourceRequest)
	if err != nil {
		return Record{}, err
	}
	credentialRequest := credentialRequestFromAuthoritySet(resolved)
	credentialBytes, credentialHash, err := EncodeCredentialRequest(credentialRequest)
	if err != nil {
		return Record{}, err
	}
	if sourceHash == credentialHash {
		return Record{}, invalid("verificationRequests", "source and credential request hash domains must be distinct")
	}
	sourceGrant, err := service.resolveOrVerifySource(
		ctx, sourceRequest, sourceBytes, sourceHash, credentialHash,
	)
	if err != nil {
		return Record{}, err
	}
	credentialGrant, err := service.resolveOrVerifyCredential(
		ctx, credentialRequest, credentialBytes, credentialHash, sourceGrant,
	)
	if err != nil {
		return Record{}, err
	}
	if err := validateGrantIndependence(sourceGrant, credentialGrant); err != nil {
		return Record{}, err
	}

	issuedAt, err := service.clock.Now(ctx)
	if err != nil {
		return Record{}, sanitizePreflightError(err)
	}
	if !validDatabaseTime(issuedAt) {
		return Record{}, invalid("issuedAt", "trusted time must be exact UTC millisecond precision")
	}

	document := AuthorityDocument{
		AuthorityID:           command.AuthorityID.String(),
		CredentialProof:       credentialGrant.proof,
		CredentialRequestHash: credentialHash,
		IssuedAt:              issuedAt.Format(canonicalTimeLayout),
		OperationID:           command.OperationID.String(),
		Plan:                  resolved.Plan,
		Policy:                resolved.Policy,
		RequestHash:           requestHash,
		SchemaVersion:         AuthoritySchemaV1,
		SourceProof:           sourceGrant.proof,
		SourceRequestHash:     sourceHash,
		WorkflowInput:         resolved.WorkflowInput,
	}
	documentBytes, authorityHash, err := EncodeAuthority(document)
	if err != nil {
		return Record{}, err
	}
	candidate := Record{
		Command: command,
		Request: request, RequestBytes: requestBytes, RequestHash: requestHash,
		SourceRequest: sourceRequest, SourceRequestBytes: sourceBytes, SourceRequestHash: sourceHash,
		CredentialRequest: credentialRequest, CredentialRequestBytes: credentialBytes, CredentialRequestHash: credentialHash,
		Document: document, DocumentBytes: documentBytes, AuthorityHash: authorityHash,
		IssuedAt: issuedAt,
	}
	if err := ValidateRecord(candidate); err != nil {
		return Record{}, err
	}

	stored, err := service.store.Issue(ctx, candidate)
	if errors.Is(err, ErrStoreOutcomeUnknown) {
		return service.recoverUnknownIssue(ctx, command, candidate)
	}
	if err != nil {
		for _, class := range []error{ErrInvalid, ErrNotReady, ErrStale, ErrConflict, ErrRetryable} {
			if errors.Is(err, class) {
				return Record{}, class
			}
		}
		return Record{}, ErrOutcomeUnknown
	}
	if err := ValidateRecord(stored); err != nil {
		return Record{}, fmt.Errorf("%w: Store returned a corrupt immutable authority", ErrConflict)
	}
	if stored.Idempotent {
		return replayRecord(stored, command)
	}
	if !sameImmutableRecord(stored, candidate) {
		return Record{}, fmt.Errorf("%w: Store returned different immutable authority bytes", ErrConflict)
	}
	return cloneRecord(stored), nil
}

func (service *Service) resolveOrVerifySource(
	ctx context.Context,
	request SourceVerificationRequest,
	requestBytes []byte,
	requestHash string,
	credentialRequestHash string,
) (verifiedSourceGrant, error) {
	existing, err := service.store.resolveReceiptAdmissionForRequest(ctx, ReceiptKindSource, requestHash)
	if err == nil {
		proof, proofErr := proofFromReceiptAdmission(existing, ReceiptKindSource, requestHash, service.sourceVerifier.sourceBinding())
		result := verifiedSourceGrant{proof: proof}
		if proofErr == nil {
			proofErr = validateSourceGrantIndependence(result, credentialRequestHash)
		}
		return result, proofErr
	}
	if !errors.Is(err, ErrNotFound) {
		return verifiedSourceGrant{}, sanitizeInspectionError(err)
	}
	grant, err := service.sourceVerifier.verifySource(ctx, request, requestBytes, requestHash)
	if err != nil {
		return verifiedSourceGrant{}, err
	}
	candidate, err := compileReceiptAdmission(ReceiptKindSource, grant.proof, grant.requestBytes)
	if err != nil {
		return verifiedSourceGrant{}, err
	}
	grant.proof.AdmissionHash = candidate.AdmissionHash
	if err := validateSourceGrantIndependence(grant, credentialRequestHash); err != nil {
		return verifiedSourceGrant{}, err
	}
	admission, err := service.admitSourceReceipt(ctx, grant)
	if err != nil {
		return verifiedSourceGrant{}, err
	}
	proof, err := proofFromReceiptAdmission(admission, ReceiptKindSource, requestHash, service.sourceVerifier.sourceBinding())
	result := verifiedSourceGrant{proof: proof}
	if err == nil {
		err = validateSourceGrantIndependence(result, credentialRequestHash)
	}
	return result, err
}

func (service *Service) resolveOrVerifyCredential(
	ctx context.Context,
	request CredentialResolutionRequest,
	requestBytes []byte,
	requestHash string,
	sourceGrant verifiedSourceGrant,
) (verifiedCredentialGrant, error) {
	existing, err := service.store.resolveReceiptAdmissionForRequest(ctx, ReceiptKindCredential, requestHash)
	if err == nil {
		proof, proofErr := proofFromReceiptAdmission(existing, ReceiptKindCredential, requestHash, service.credentialResolver.credentialBinding())
		result := verifiedCredentialGrant{proof: proof}
		if proofErr == nil {
			proofErr = validateGrantIndependence(sourceGrant, result)
		}
		return result, proofErr
	}
	if !errors.Is(err, ErrNotFound) {
		return verifiedCredentialGrant{}, sanitizeInspectionError(err)
	}
	grant, err := service.credentialResolver.resolveCredential(ctx, request, requestBytes, requestHash)
	if err != nil {
		return verifiedCredentialGrant{}, err
	}
	candidate, err := compileReceiptAdmission(ReceiptKindCredential, grant.proof, grant.requestBytes)
	if err != nil {
		return verifiedCredentialGrant{}, err
	}
	grant.proof.AdmissionHash = candidate.AdmissionHash
	if err := validateGrantIndependence(sourceGrant, grant); err != nil {
		return verifiedCredentialGrant{}, err
	}
	admission, err := service.admitCredentialReceipt(ctx, grant)
	if err != nil {
		return verifiedCredentialGrant{}, err
	}
	proof, err := proofFromReceiptAdmission(admission, ReceiptKindCredential, requestHash, service.credentialResolver.credentialBinding())
	result := verifiedCredentialGrant{proof: proof}
	if err == nil {
		err = validateGrantIndependence(sourceGrant, result)
	}
	return result, err
}

func (service *Service) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.store) || !validUUIDv4Value(operationID) {
		return Record{}, ErrNotFound
	}
	record, err := service.store.InspectOperation(ctx, operationID)
	if err != nil {
		return Record{}, sanitizeInspectionError(err)
	}
	if err := ValidateRecord(record); err != nil || record.Command.OperationID != operationID {
		return Record{}, fmt.Errorf("%w: stored operation is corrupt", ErrConflict)
	}
	return cloneRecord(record), nil
}

func (service *Service) ResolveAuthority(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.store) || !validUUIDv4Value(authorityID) {
		return Record{}, ErrNotFound
	}
	record, err := service.store.ResolveAuthority(ctx, authorityID)
	if err != nil {
		return Record{}, sanitizeInspectionError(err)
	}
	if err := ValidateRecord(record); err != nil || record.Command.AuthorityID != authorityID {
		return Record{}, fmt.Errorf("%w: stored authority is corrupt", ErrConflict)
	}
	return cloneRecord(record), nil
}

func (service *Service) admitSourceReceipt(ctx context.Context, grant verifiedSourceGrant) (ReceiptAdmissionRecord, error) {
	expected, err := compileReceiptAdmission(ReceiptKindSource, grant.proof, grant.requestBytes)
	if err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	stored, err := service.store.admitSourceReceipt(ctx, grant)
	return service.reconcileReceiptAdmission(ctx, expected, stored, err)
}

func (service *Service) admitCredentialReceipt(ctx context.Context, grant verifiedCredentialGrant) (ReceiptAdmissionRecord, error) {
	expected, err := compileReceiptAdmission(ReceiptKindCredential, grant.proof, grant.requestBytes)
	if err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	stored, err := service.store.admitCredentialReceipt(ctx, grant)
	return service.reconcileReceiptAdmission(ctx, expected, stored, err)
}

func (service *Service) reconcileReceiptAdmission(
	ctx context.Context,
	expected ReceiptAdmissionRecord,
	stored ReceiptAdmissionRecord,
	storeErr error,
) (ReceiptAdmissionRecord, error) {
	if errors.Is(storeErr, ErrStoreOutcomeUnknown) {
		reconcileCtx, cancel := reconciliationContext(ctx)
		defer cancel()
		resolved, err := service.store.resolveReceiptAdmission(
			reconcileCtx, expected.Document.Kind, expected.AdmissionHash,
		)
		if err != nil {
			return ReceiptAdmissionRecord{}, ErrOutcomeUnknown
		}
		stored = resolved
		storeErr = nil
	}
	if storeErr != nil {
		if errors.Is(storeErr, ErrRetryable) {
			return ReceiptAdmissionRecord{}, ErrRetryable
		}
		if errors.Is(storeErr, ErrConflict) {
			// Another verifier may have admitted a different observation for the
			// same exact request concurrently. The first immutable admission wins;
			// recover it by (kind, requestHash) rather than selecting a new receipt.
			reconcileCtx, cancel := reconciliationContext(ctx)
			defer cancel()
			resolved, err := service.store.resolveReceiptAdmissionForRequest(
				reconcileCtx, expected.Document.Kind, expected.Document.RequestHash,
			)
			if err != nil {
				return ReceiptAdmissionRecord{}, ErrConflict
			}
			expectedBinding := ExecutableBinding{
				AuthorityID: expected.Document.AuthorityID, ExecutableDigest: expected.Document.ExecutableDigest,
			}
			if _, err := proofFromReceiptAdmission(resolved, expected.Document.Kind, expected.Document.RequestHash, expectedBinding); err != nil {
				return ReceiptAdmissionRecord{}, err
			}
			return cloneReceiptAdmission(resolved), nil
		}
		if errors.Is(storeErr, ErrInvalid) {
			return ReceiptAdmissionRecord{}, ErrInvalid
		}
		return ReceiptAdmissionRecord{}, ErrOutcomeUnknown
	}
	if err := validateReceiptAdmissionRecord(stored); err != nil || !sameReceiptAdmission(stored, expected) {
		return ReceiptAdmissionRecord{}, fmt.Errorf("%w: receipt admission differs from the sealed verification grant", ErrConflict)
	}
	return cloneReceiptAdmission(stored), nil
}

func proofFromReceiptAdmission(
	record ReceiptAdmissionRecord,
	kind string,
	requestHash string,
	expectedBinding ExecutableBinding,
) (VerificationProof, error) {
	if err := validateReceiptAdmissionRecord(record); err != nil || record.Document.Kind != kind ||
		record.Document.RequestHash != requestHash || record.Document.AuthorityID != expectedBinding.AuthorityID ||
		record.Document.ExecutableDigest != expectedBinding.ExecutableDigest {
		return VerificationProof{}, fmt.Errorf("%w: receipt admission does not resolve the exact role and request", ErrConflict)
	}
	return VerificationProof{
		AdmissionHash:    record.AdmissionHash,
		AuthorityID:      record.Document.AuthorityID,
		ExecutableDigest: record.Document.ExecutableDigest,
		ReceiptHash:      record.Document.ReceiptHash,
		RequestHash:      record.Document.RequestHash,
	}, nil
}

func (service *Service) recoverUnknownIssue(ctx context.Context, command IssueCommand, candidate Record) (Record, error) {
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	recovered, err := service.store.InspectOperation(reconcileCtx, command.OperationID)
	if err != nil {
		return Record{}, ErrOutcomeUnknown
	}
	replayed, err := replayRecord(recovered, command)
	if err != nil {
		return Record{}, err
	}
	if !sameImmutableRecord(recovered, candidate) {
		return Record{}, fmt.Errorf("%w: uncertain issue resolved to different canonical bytes", ErrConflict)
	}
	replayed.Idempotent = true
	return replayed, nil
}

func compileReceiptAdmission(kind string, proof VerificationProof, requestBytes []byte) (ReceiptAdmissionRecord, error) {
	document := ReceiptAdmission{
		AuthorityID: proof.AuthorityID, ExecutableDigest: proof.ExecutableDigest, Kind: kind,
		ReceiptHash: proof.ReceiptHash, RequestHash: proof.RequestHash, SchemaVersion: ReceiptAdmissionSchemaV1,
	}
	encoded, hash, err := EncodeReceiptAdmission(document)
	if err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	record := ReceiptAdmissionRecord{
		Document: document, DocumentBytes: encoded, RequestBytes: append([]byte(nil), requestBytes...), AdmissionHash: hash,
	}
	if err := validateReceiptAdmissionRecord(record); err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	return record, nil
}

func validateGrantIndependence(source verifiedSourceGrant, credential verifiedCredentialGrant) error {
	if err := validateProof("sourceVerificationGrant", source.proof); err != nil {
		return err
	}
	if err := validateProof("credentialResolutionGrant", credential.proof); err != nil {
		return err
	}
	if source.proof.AuthorityID == credential.proof.AuthorityID ||
		source.proof.ExecutableDigest == credential.proof.ExecutableDigest ||
		!uniqueStrings([]string{
			source.proof.RequestHash,
			source.proof.ReceiptHash,
			source.proof.AdmissionHash,
			credential.proof.RequestHash,
			credential.proof.ReceiptHash,
			credential.proof.AdmissionHash,
		}) {
		return invalid("verificationGrants", "source and credential identities, executables, and all proof hashes must be pairwise distinct")
	}
	return nil
}

func validateSourceGrantIndependence(source verifiedSourceGrant, credentialRequestHash string) error {
	if err := validateProof("sourceVerificationGrant", source.proof); err != nil {
		return err
	}
	if !validDigest(credentialRequestHash) || !uniqueStrings([]string{
		source.proof.RequestHash,
		source.proof.ReceiptHash,
		source.proof.AdmissionHash,
		credentialRequestHash,
	}) {
		return invalid("verificationGrants", "source proof and credential request hash domains must be pairwise distinct")
	}
	return nil
}

func replayRecord(record Record, command IssueCommand) (Record, error) {
	if err := ValidateRecord(record); err != nil {
		return Record{}, fmt.Errorf("%w: stored operation is corrupt", ErrConflict)
	}
	if record.Command != command {
		return Record{}, fmt.Errorf("%w: operation ID is bound to another immutable command", ErrConflict)
	}
	record.Idempotent = true
	return cloneRecord(record), nil
}

func sameImmutableRecord(left, right Record) bool {
	return left.Command == right.Command && left.Request == right.Request && left.RequestHash == right.RequestHash &&
		left.SourceRequest == right.SourceRequest && left.SourceRequestHash == right.SourceRequestHash &&
		left.CredentialRequest == right.CredentialRequest && left.CredentialRequestHash == right.CredentialRequestHash &&
		left.Document == right.Document && left.AuthorityHash == right.AuthorityHash && left.IssuedAt.Equal(right.IssuedAt) &&
		bytes.Equal(left.RequestBytes, right.RequestBytes) && bytes.Equal(left.SourceRequestBytes, right.SourceRequestBytes) &&
		bytes.Equal(left.CredentialRequestBytes, right.CredentialRequestBytes) && bytes.Equal(left.DocumentBytes, right.DocumentBytes)
}

func sameReceiptAdmission(left, right ReceiptAdmissionRecord) bool {
	return left.Document == right.Document && left.AdmissionHash == right.AdmissionHash &&
		bytes.Equal(left.DocumentBytes, right.DocumentBytes) && bytes.Equal(left.RequestBytes, right.RequestBytes)
}

func validDatabaseTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Equal(value.Truncate(time.Millisecond)) &&
		value.Format(canonicalTimeLayout) != "0001-01-01T00:00:00.000Z"
}

func reconciliationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), reconciliationTimeout)
}

func sanitizePreflightError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	for _, class := range []error{ErrInvalid, ErrNotFound, ErrNotReady, ErrStale, ErrConflict} {
		if errors.Is(err, class) {
			return class
		}
	}
	return ErrNotReady
}

func sanitizeInspectionError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	for _, class := range []error{ErrNotFound, ErrConflict} {
		if errors.Is(err, class) {
			return class
		}
	}
	return ErrOutcomeUnknown
}

func isNilInterface(value any) bool {
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
