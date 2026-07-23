package qualificationinputauthority

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const canonicalTimeLayout = "2006-01-02T15:04:05.000Z"

var (
	digestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	gitCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	stableIDPattern  = regexp.MustCompile(`^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$`)
)

func ValidateCommand(command IssueCommand) error {
	identities := []uuid.UUID{
		command.OperationID,
		command.AuthorityID,
		command.WorkflowInputAuthorityID,
		command.QualificationPolicyAuthorityID,
		command.QualificationPlanAuthorityID,
	}
	seen := make(map[uuid.UUID]struct{}, len(identities))
	for _, identity := range identities {
		if !validUUIDv4Value(identity) {
			return invalid("command", "all identities must be nonzero RFC-4122 UUIDv4 values")
		}
		if _, duplicate := seen[identity]; duplicate {
			return invalid("command", "all identities must be pairwise distinct")
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func ValidateResolvedAuthorities(resolved ResolvedAuthorities) error {
	if err := validateExecutableBinding("resolvedAuthorities.sourceVerifier", resolved.SourceVerifier); err != nil {
		return err
	}
	if err := validateExecutableBinding("resolvedAuthorities.credentialResolver", resolved.CredentialResolver); err != nil {
		return err
	}
	if resolved.SourceVerifier.AuthorityID == resolved.CredentialResolver.AuthorityID ||
		resolved.SourceVerifier.ExecutableDigest == resolved.CredentialResolver.ExecutableDigest {
		return invalid("resolvedAuthorities.verifierBindings", "source and credential identities and executable digests must be distinct")
	}
	if err := validateWorkflowInput(resolved.WorkflowInput); err != nil {
		return err
	}
	if err := validatePolicy(resolved.Policy); err != nil {
		return err
	}
	if err := validatePlan(resolved.Plan); err != nil {
		return err
	}
	if !resolved.PolicyCurrent || resolved.PolicyStatus != PolicyStatusActive {
		return fmt.Errorf("%w: qualification Policy must be the current active generation", ErrStale)
	}
	if resolved.WorkflowInput.QualificationPolicyAuthorityID != resolved.Policy.AuthorityID ||
		resolved.WorkflowInput.QualificationPolicyAuthorityHash != resolved.Policy.AuthorityHash {
		return invalid("resolvedAuthorities", "WIA does not bind the exact Policy identity and hash")
	}
	if resolved.Plan.InputAuthorityID != resolved.WorkflowInput.AuthorityID {
		return invalid("resolvedAuthorities", "Plan does not bind the exact WIA identity")
	}
	if !uniqueStrings([]string{
		resolved.WorkflowInput.AuthorityID,
		resolved.Policy.AuthorityID,
		resolved.Plan.AuthorityID,
	}) {
		return invalid("resolvedAuthorities", "WIA, Policy, and Plan identities must be distinct")
	}
	profile := resolved.Policy.CredentialProfile
	credential := resolved.Plan.CredentialSet
	if profile.AuthorityID != credential.Issuer || profile.Audience != credential.Audience ||
		profile.IssuanceArtifactID != credential.IssuanceArtifactID ||
		profile.RevocationArtifactID != credential.RevocationArtifactID {
		return invalid("resolvedAuthorities.credential", "Policy profile does not map exactly to the Plan credential controls")
	}
	if resolved.Policy.SourcePolicyDigest == resolved.Plan.Source.TreeDigest {
		return invalid("resolvedAuthorities.source", "source policy and source tree digests are distinct domains")
	}
	if profile.MemberRequestSetDigest == credential.MemberBindingsDigest ||
		profile.MemberRequestSetDigest == credential.SetHandleHash ||
		credential.MemberBindingsDigest == credential.SetHandleHash {
		return invalid("resolvedAuthorities.credential", "request, member-binding, and handle digests are distinct domains")
	}
	return nil
}

func validateIssueRequest(request IssueRequest) error {
	if request.SchemaVersion != IssueRequestSchemaV1 {
		return invalid("request.schemaVersion", "is invalid")
	}
	identities := []string{
		request.OperationID,
		request.AuthorityID,
		request.WorkflowInputAuthorityID,
		request.QualificationPolicyAuthorityID,
		request.QualificationPlanAuthorityID,
	}
	for _, identity := range identities {
		if !validUUIDv4(identity) {
			return invalid("request", "all identities must be canonical UUIDv4 strings")
		}
	}
	if !uniqueStrings(identities) {
		return invalid("request", "all identities must be pairwise distinct")
	}
	return nil
}

func validateWorkflowInput(binding WorkflowInputBinding) error {
	if !validUUIDv4(binding.AuthorityID) || !validUUIDv4(binding.QualificationPolicyAuthorityID) ||
		!validDigest(binding.AuthorityHash) || !validDigest(binding.InputHash) ||
		!validDigest(binding.QualificationPolicyAuthorityHash) {
		return invalid("workflowInput", "identity or digest is invalid")
	}
	if binding.AuthorityID == binding.QualificationPolicyAuthorityID {
		return invalid("workflowInput", "WIA and Policy identities must differ")
	}
	return nil
}

func validateCredentialProfile(profile CredentialProfile) error {
	if !validAuthorityID(profile.AuthorityID) || !validCanonicalString(profile.Audience, 512) ||
		!validStableID(profile.IssuanceArtifactID, 256) || !validStableID(profile.RevocationArtifactID, 256) ||
		profile.IssuanceArtifactID == profile.RevocationArtifactID || !validDigest(profile.MemberRequestSetDigest) {
		return invalid("policy.credentialProfile", "is invalid")
	}
	return nil
}

func validatePolicy(binding PolicyBinding) error {
	if !validUUIDv4(binding.AuthorityID) || !validDigest(binding.AuthorityHash) ||
		!validDigest(binding.PlanInputProfileHash) || !validDigest(binding.SourcePolicyDigest) {
		return invalid("policy", "identity or digest is invalid")
	}
	if err := validateCredentialProfile(binding.CredentialProfile); err != nil {
		return err
	}
	if !uniqueStrings([]string{
		binding.AuthorityHash,
		binding.PlanInputProfileHash,
		binding.SourcePolicyDigest,
		binding.CredentialProfile.MemberRequestSetDigest,
	}) {
		return invalid("policy", "independent Policy digest domains must not alias")
	}
	return nil
}

func validateSource(source SourceProjection) error {
	if !gitCommitPattern.MatchString(source.Commit) || source.Dirty ||
		source.TreeDigestSchema != SourceTreeDigestSchemaV1 || !validDigest(source.TreeDigest) {
		return invalid("plan.source", "must be a clean immutable source-content-tree projection")
	}
	return nil
}

func validateCredentialSet(credential CredentialSetProjection) error {
	if !validUUIDv4(credential.SetID) || !validAuthorityID(credential.Issuer) ||
		!validCanonicalString(credential.Audience, 512) ||
		!validDigest(credential.SetHandleHash) || !validDigest(credential.MemberBindingsDigest) ||
		credential.MemberCount < 1 || credential.MemberCount > 64 ||
		!validStableID(credential.IssuanceArtifactID, 256) || !validStableID(credential.RevocationArtifactID, 256) ||
		credential.IssuanceArtifactID == credential.RevocationArtifactID ||
		credential.SetHandleHash == credential.MemberBindingsDigest {
		return invalid("plan.credentialSet", "is invalid")
	}
	return nil
}

func validatePlan(binding PlanBinding) error {
	if !validUUIDv4(binding.AuthorityID) || !validUUIDv4(binding.InputAuthorityID) ||
		!validDigest(binding.AuthorityHash) || !validDigest(binding.InputHash) ||
		binding.AuthorityID == binding.InputAuthorityID {
		return invalid("plan", "identity or digest is invalid")
	}
	if err := validateSource(binding.Source); err != nil {
		return err
	}
	return validateCredentialSet(binding.CredentialSet)
}

func validateAuthorityReference(name string, reference AuthorityReference) error {
	if !validUUIDv4(reference.AuthorityID) || !validDigest(reference.AuthorityHash) {
		return invalid(name, "authority identity or hash is invalid")
	}
	return nil
}

func validateSourceRequest(request SourceVerificationRequest) error {
	if request.SchemaVersion != SourceRequestSchemaV1 || !validDigest(request.SourcePolicyDigest) {
		return invalid("sourceRequest", "schema or source policy digest is invalid")
	}
	if err := validateAuthorityReference("sourceRequest.workflowInput", request.WorkflowInput); err != nil {
		return err
	}
	if err := validateAuthorityReference("sourceRequest.policy", request.Policy); err != nil {
		return err
	}
	if err := validateAuthorityReference("sourceRequest.plan", request.Plan); err != nil {
		return err
	}
	if err := validateSource(request.Source); err != nil {
		return err
	}
	if err := validateExecutableBinding("sourceRequest.verifier", request.Verifier); err != nil {
		return err
	}
	if request.SourcePolicyDigest == request.Source.TreeDigest || !uniqueAuthorityReferences(request.WorkflowInput, request.Policy, request.Plan) {
		return invalid("sourceRequest", "authority roles or digest domains alias")
	}
	return nil
}

func validateCredentialRequest(request CredentialResolutionRequest) error {
	if request.SchemaVersion != CredentialRequestSchemaV1 {
		return invalid("credentialRequest.schemaVersion", "is invalid")
	}
	if err := validateAuthorityReference("credentialRequest.workflowInput", request.WorkflowInput); err != nil {
		return err
	}
	if err := validateAuthorityReference("credentialRequest.policy", request.Policy); err != nil {
		return err
	}
	if err := validateAuthorityReference("credentialRequest.plan", request.Plan); err != nil {
		return err
	}
	if err := validateCredentialProfile(request.CredentialProfile); err != nil {
		return err
	}
	if err := validateCredentialSet(request.CredentialSet); err != nil {
		return err
	}
	if err := validateExecutableBinding("credentialRequest.resolver", request.Resolver); err != nil {
		return err
	}
	if !uniqueAuthorityReferences(request.WorkflowInput, request.Policy, request.Plan) {
		return invalid("credentialRequest", "authority roles alias")
	}
	if request.CredentialProfile.AuthorityID != request.CredentialSet.Issuer ||
		request.CredentialProfile.Audience != request.CredentialSet.Audience ||
		request.CredentialProfile.IssuanceArtifactID != request.CredentialSet.IssuanceArtifactID ||
		request.CredentialProfile.RevocationArtifactID != request.CredentialSet.RevocationArtifactID ||
		request.CredentialProfile.MemberRequestSetDigest == request.CredentialSet.MemberBindingsDigest ||
		request.CredentialProfile.MemberRequestSetDigest == request.CredentialSet.SetHandleHash {
		return invalid("credentialRequest", "Policy profile does not authorize the exact non-aliased Plan credential set")
	}
	return nil
}

func validateExecutableBinding(name string, binding ExecutableBinding) error {
	if !validAuthorityID(binding.AuthorityID) || !validDigest(binding.ExecutableDigest) {
		return invalid(name, "authority identity or executable digest is invalid")
	}
	return nil
}

func validateProof(name string, proof VerificationProof) error {
	if err := validateExecutableBinding(name, ExecutableBinding{AuthorityID: proof.AuthorityID, ExecutableDigest: proof.ExecutableDigest}); err != nil {
		return err
	}
	if !validDigest(proof.AdmissionHash) || !validDigest(proof.RequestHash) || !validDigest(proof.ReceiptHash) ||
		proof.RequestHash == proof.ReceiptHash || proof.ExecutableDigest == proof.ReceiptHash ||
		proof.ExecutableDigest == proof.RequestHash || proof.AdmissionHash == proof.RequestHash ||
		proof.AdmissionHash == proof.ReceiptHash || proof.AdmissionHash == proof.ExecutableDigest {
		return invalid(name, "request, receipt, and executable digest domains must be valid and distinct")
	}
	return nil
}

func validateReceiptAdmission(document ReceiptAdmission) error {
	if document.SchemaVersion != ReceiptAdmissionSchemaV1 ||
		(document.Kind != ReceiptKindSource && document.Kind != ReceiptKindCredential) {
		return invalid("receiptAdmission", "schema or closed receipt kind is invalid")
	}
	if err := validateExecutableBinding("receiptAdmission", ExecutableBinding{
		AuthorityID: document.AuthorityID, ExecutableDigest: document.ExecutableDigest,
	}); err != nil {
		return err
	}
	if !validDigest(document.RequestHash) || !validDigest(document.ReceiptHash) ||
		document.RequestHash == document.ReceiptHash || document.ExecutableDigest == document.RequestHash ||
		document.ExecutableDigest == document.ReceiptHash {
		return invalid("receiptAdmission", "request, external receipt, and executable digest domains must be distinct")
	}
	return nil
}

func validateReceiptAdmissionRecord(record ReceiptAdmissionRecord) error {
	document, err := DecodeReceiptAdmission(record.DocumentBytes, record.AdmissionHash)
	if err != nil || document != record.Document {
		return invalid("receiptAdmissionRecord", "typed document, canonical bytes, or hash differ")
	}
	expectedBinding := ExecutableBinding{AuthorityID: document.AuthorityID, ExecutableDigest: document.ExecutableDigest}
	switch document.Kind {
	case ReceiptKindSource:
		request, err := DecodeSourceRequest(record.RequestBytes, document.RequestHash)
		if err != nil || request.Verifier != expectedBinding {
			return invalid("receiptAdmissionRecord", "source request bytes/hash do not bind the admitted verifier")
		}
	case ReceiptKindCredential:
		request, err := DecodeCredentialRequest(record.RequestBytes, document.RequestHash)
		if err != nil || request.Resolver != expectedBinding {
			return invalid("receiptAdmissionRecord", "credential request bytes/hash do not bind the admitted resolver")
		}
	default:
		return invalid("receiptAdmissionRecord", "receipt kind is invalid")
	}
	return nil
}

func validateAuthority(document AuthorityDocument) error {
	if document.SchemaVersion != AuthoritySchemaV1 || !validUUIDv4(document.OperationID) ||
		!validUUIDv4(document.AuthorityID) || document.OperationID == document.AuthorityID ||
		!validCanonicalTime(document.IssuedAt) || !validDigest(document.RequestHash) ||
		!validDigest(document.SourceRequestHash) || !validDigest(document.CredentialRequestHash) {
		return invalid("authority", "schema, identity, time, or component hash is invalid")
	}
	if err := validateWorkflowInput(document.WorkflowInput); err != nil {
		return err
	}
	if err := validatePolicy(document.Policy); err != nil {
		return err
	}
	if err := validatePlan(document.Plan); err != nil {
		return err
	}
	if err := validateProof("authority.sourceProof", document.SourceProof); err != nil {
		return err
	}
	if err := validateProof("authority.credentialProof", document.CredentialProof); err != nil {
		return err
	}
	if document.SourceProof.RequestHash != document.SourceRequestHash ||
		document.CredentialProof.RequestHash != document.CredentialRequestHash {
		return invalid("authority", "proof request hashes do not bind the exact component requests")
	}
	if !uniqueStrings([]string{document.RequestHash, document.SourceRequestHash, document.CredentialRequestHash}) {
		return invalid("authority", "issue, source, and credential request hash domains must not alias")
	}
	if document.SourceProof.AuthorityID == document.CredentialProof.AuthorityID ||
		document.SourceProof.ExecutableDigest == document.CredentialProof.ExecutableDigest ||
		!uniqueStrings([]string{
			document.SourceProof.RequestHash,
			document.SourceProof.ReceiptHash,
			document.SourceProof.AdmissionHash,
			document.CredentialProof.RequestHash,
			document.CredentialProof.ReceiptHash,
			document.CredentialProof.AdmissionHash,
		}) {
		return invalid("authority", "source and credential identities, executables, and all proof hashes must be independent")
	}
	if !uniqueStrings([]string{
		document.OperationID,
		document.AuthorityID,
		document.WorkflowInput.AuthorityID,
		document.Policy.AuthorityID,
		document.Plan.AuthorityID,
	}) {
		return invalid("authority", "operation, precommit, WIA, Policy, and Plan identities must be distinct")
	}
	resolved := ResolvedAuthorities{
		CredentialResolver: ExecutableBinding{
			AuthorityID: document.CredentialProof.AuthorityID, ExecutableDigest: document.CredentialProof.ExecutableDigest,
		},
		Plan:          document.Plan,
		Policy:        document.Policy,
		PolicyCurrent: true,
		PolicyStatus:  PolicyStatusActive,
		SourceVerifier: ExecutableBinding{
			AuthorityID: document.SourceProof.AuthorityID, ExecutableDigest: document.SourceProof.ExecutableDigest,
		},
		WorkflowInput: document.WorkflowInput,
	}
	return ValidateResolvedAuthorities(resolved)
}

func ValidateRecord(record Record) error {
	if err := ValidateCommand(record.Command); err != nil {
		return err
	}
	request, err := DecodeIssueRequest(record.RequestBytes, record.RequestHash)
	if err != nil || request != record.Request {
		return invalid("record.request", "typed document, canonical bytes, or hash differ")
	}
	sourceRequest, err := DecodeSourceRequest(record.SourceRequestBytes, record.SourceRequestHash)
	if err != nil || sourceRequest != record.SourceRequest {
		return invalid("record.sourceRequest", "typed document, canonical bytes, or hash differ")
	}
	credentialRequest, err := DecodeCredentialRequest(record.CredentialRequestBytes, record.CredentialRequestHash)
	if err != nil || credentialRequest != record.CredentialRequest {
		return invalid("record.credentialRequest", "typed document, canonical bytes, or hash differ")
	}
	document, err := DecodeAuthority(record.DocumentBytes, record.AuthorityHash)
	if err != nil || document != record.Document {
		return invalid("record.authority", "typed document, canonical bytes, or hash differ")
	}
	if record.Request != issueRequestFromCommand(record.Command) ||
		record.Document.OperationID != record.Command.OperationID.String() ||
		record.Document.AuthorityID != record.Command.AuthorityID.String() ||
		record.Document.RequestHash != record.RequestHash ||
		record.Document.SourceRequestHash != record.SourceRequestHash ||
		record.Document.CredentialRequestHash != record.CredentialRequestHash ||
		record.Document.WorkflowInput.AuthorityID != record.Command.WorkflowInputAuthorityID.String() ||
		record.Document.Policy.AuthorityID != record.Command.QualificationPolicyAuthorityID.String() ||
		record.Document.Plan.AuthorityID != record.Command.QualificationPlanAuthorityID.String() {
		return invalid("record", "command and authority cross-bindings differ")
	}
	resolved := ResolvedAuthorities{
		CredentialResolver: record.CredentialRequest.Resolver,
		Plan:               record.Document.Plan,
		Policy:             record.Document.Policy,
		PolicyCurrent:      true,
		PolicyStatus:       PolicyStatusActive,
		SourceVerifier:     record.SourceRequest.Verifier,
		WorkflowInput:      record.Document.WorkflowInput,
	}
	if sourceRequestFromAuthoritySet(resolved) != record.SourceRequest ||
		credentialRequestFromAuthoritySet(resolved) != record.CredentialRequest {
		return invalid("record", "component requests differ from the authority projections")
	}
	if record.SourceRequest.Verifier != (ExecutableBinding{
		AuthorityID: record.Document.SourceProof.AuthorityID, ExecutableDigest: record.Document.SourceProof.ExecutableDigest,
	}) || record.CredentialRequest.Resolver != (ExecutableBinding{
		AuthorityID: record.Document.CredentialProof.AuthorityID, ExecutableDigest: record.Document.CredentialProof.ExecutableDigest,
	}) {
		return invalid("record", "component requests do not bind the proof executable identities")
	}
	if record.IssuedAt.Location() != time.UTC || !record.IssuedAt.Equal(record.IssuedAt.UTC().Truncate(time.Millisecond)) ||
		record.IssuedAt.Format(canonicalTimeLayout) != record.Document.IssuedAt {
		return invalid("record.issuedAt", "database time and canonical document time differ")
	}
	return nil
}

func issueRequestFromCommand(command IssueCommand) IssueRequest {
	return IssueRequest{
		AuthorityID:                    command.AuthorityID.String(),
		OperationID:                    command.OperationID.String(),
		QualificationPlanAuthorityID:   command.QualificationPlanAuthorityID.String(),
		QualificationPolicyAuthorityID: command.QualificationPolicyAuthorityID.String(),
		SchemaVersion:                  IssueRequestSchemaV1,
		WorkflowInputAuthorityID:       command.WorkflowInputAuthorityID.String(),
	}
}

func sourceRequestFromAuthoritySet(resolved ResolvedAuthorities) SourceVerificationRequest {
	return SourceVerificationRequest{
		Plan:               AuthorityReference{AuthorityHash: resolved.Plan.AuthorityHash, AuthorityID: resolved.Plan.AuthorityID},
		Policy:             AuthorityReference{AuthorityHash: resolved.Policy.AuthorityHash, AuthorityID: resolved.Policy.AuthorityID},
		SchemaVersion:      SourceRequestSchemaV1,
		Source:             resolved.Plan.Source,
		SourcePolicyDigest: resolved.Policy.SourcePolicyDigest,
		Verifier:           resolved.SourceVerifier,
		WorkflowInput: AuthorityReference{
			AuthorityHash: resolved.WorkflowInput.AuthorityHash, AuthorityID: resolved.WorkflowInput.AuthorityID,
		},
	}
}

func credentialRequestFromAuthoritySet(resolved ResolvedAuthorities) CredentialResolutionRequest {
	return CredentialResolutionRequest{
		CredentialProfile: resolved.Policy.CredentialProfile,
		CredentialSet:     resolved.Plan.CredentialSet,
		Plan:              AuthorityReference{AuthorityHash: resolved.Plan.AuthorityHash, AuthorityID: resolved.Plan.AuthorityID},
		Policy:            AuthorityReference{AuthorityHash: resolved.Policy.AuthorityHash, AuthorityID: resolved.Policy.AuthorityID},
		Resolver:          resolved.CredentialResolver,
		SchemaVersion:     CredentialRequestSchemaV1,
		WorkflowInput: AuthorityReference{
			AuthorityHash: resolved.WorkflowInput.AuthorityHash, AuthorityID: resolved.WorkflowInput.AuthorityID,
		},
	}
}

func uniqueAuthorityReferences(references ...AuthorityReference) bool {
	identities := make([]string, 0, len(references))
	hashes := make([]string, 0, len(references))
	for _, reference := range references {
		identities = append(identities, reference.AuthorityID)
		hashes = append(hashes, reference.AuthorityHash)
	}
	return uniqueStrings(identities) && uniqueStrings(hashes)
}

func uniqueStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validUUIDv4(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value && validUUIDv4Value(parsed)
}

func validUUIDv4Value(value uuid.UUID) bool {
	return value != uuid.Nil && value.Version() == 4 && value.Variant() == uuid.RFC4122
}

func validDigest(value string) bool {
	return digestPattern.MatchString(value)
}

func validAuthorityID(value string) bool {
	return validStableID(value, 256)
}

func validStableID(value string, maximum int) bool {
	return len(value) > 0 && len(value) <= maximum && utf8.ValidString(value) && stableIDPattern.MatchString(value)
}

func validCanonicalString(value string, maximum int) bool {
	return len(value) > 0 && len(value) <= maximum && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func validCanonicalTime(value string) bool {
	parsed, err := time.Parse(canonicalTimeLayout, value)
	return err == nil && parsed.Location() == time.UTC && parsed.Format(canonicalTimeLayout) == value
}

func invalid(field, format string, arguments ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalid, field, fmt.Sprintf(format, arguments...))
}
