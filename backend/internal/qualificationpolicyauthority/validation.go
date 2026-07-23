package qualificationpolicyauthority

import (
	"bytes"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationevidence"
	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
)

var (
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	stableIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$`)
	identityPattern = regexp.MustCompile(`^(?:spiffe://[a-z0-9.-]+/[a-z0-9._/-]+|[a-z0-9]+(?:[._:-][a-z0-9]+)*)$`)
	audiencePattern = regexp.MustCompile(`^(?:urn:[a-z0-9][a-z0-9:._-]+|[a-z0-9]+(?:[._:-][a-z0-9]+)*)$`)
)

func ValidateRevisionPolicy(policy RevisionPolicy) error {
	if policy.SchemaVersion != RevisionPolicySchemaV1 || policy.SourceCurrencyPolicy != CurrencyLatestApproved ||
		policy.WorkspaceTarget.CurrencyPolicy != CurrencyLatestApproved || policy.WorkspaceTarget.CanonicalReviewRequired {
		return invalid("revisionPolicy", "schema, default currency, or fixed workspace policy is invalid")
	}
	if policy.ReviewByChangeSource == nil || len(policy.ReviewByChangeSource) != 6 {
		return invalid("revisionPolicy.reviewByChangeSource", "must contain exactly the six closed change-source rules")
	}
	expectedSources := [...]string{
		ChangeSourceAIProposal,
		ChangeSourceHuman,
		ChangeSourceImport,
		ChangeSourceMerge,
		ChangeSourceRollback,
		ChangeSourceSystem,
	}
	for index, expected := range expectedSources {
		rule := policy.ReviewByChangeSource[index]
		if rule.ChangeSource != expected {
			return invalid("revisionPolicy.reviewByChangeSource", "must be complete, unique, and strictly ordered")
		}
		if rule.ChangeSource == ChangeSourceHuman && !rule.CanonicalReviewRequired {
			return invalid("revisionPolicy.reviewByChangeSource", "human changes must require canonical review")
		}
	}
	if policy.ExactApprovedSources == nil || len(policy.ExactApprovedSources) > MaximumExactApprovedSources {
		return invalid("revisionPolicy.exactApprovedSources", "must be a present bounded array")
	}
	for index, source := range policy.ExactApprovedSources {
		if !validStableID(source.SourceKind, 128) || !validStableID(source.Purpose, 256) ||
			!validUUIDv4String(source.ArtifactID) || !validUUIDv4String(source.RevisionID) || !validDigest(source.ContentHash) {
			return invalid("revisionPolicy.exactApprovedSources", "entry %d is not a complete immutable tuple", index)
		}
		if source.SourceKind == WorkspaceSourceKind || source.Purpose == WorkspaceRevisionPurpose {
			return invalid("revisionPolicy.exactApprovedSources", "workspace currency is fixed and cannot be overridden")
		}
		if index > 0 && !exactApprovedSourceLess(policy.ExactApprovedSources[index-1], source) {
			return invalid("revisionPolicy.exactApprovedSources", "must be strictly sorted and unique")
		}
	}
	encoded, err := CanonicalJSON(policy)
	if err != nil {
		return err
	}
	return validateSecretFree("revisionPolicy", encoded)
}

func exactApprovedSourceLess(left, right ExactApprovedSource) bool {
	leftValues := [...]string{left.SourceKind, left.Purpose, left.ArtifactID, left.RevisionID, left.ContentHash}
	rightValues := [...]string{right.SourceKind, right.Purpose, right.ArtifactID, right.RevisionID, right.ContentHash}
	for index := range leftValues {
		comparison := bytes.Compare([]byte(leftValues[index]), []byte(rightValues[index]))
		if comparison != 0 {
			return comparison < 0
		}
	}
	return false
}

func ValidatePlanInputProfile(profile PlanInputProfile) error {
	if profile.SchemaVersion != PlanInputProfileSchemaV1 ||
		profile.ArtifactPolicy.MaximumArtifacts != qualificationevidence.MaximumArtifacts ||
		!profile.ArtifactPolicy.RequireRestrictedEncryption || !profile.ArtifactPolicy.RequireTrace || !profile.ArtifactPolicy.RequireVideo ||
		profile.OutputPolicy.CredentialRevocation != CredentialRevocationPolicyV1 ||
		profile.OutputPolicy.PlaintextDisposition != PlaintextDispositionPolicyV1 ||
		profile.OutputPolicy.SnapshotMode != qualificationevidence.ImmutableSnapshotMode {
		return invalid("planInputProfile", "schema or fail-closed artifact/output policy is invalid")
	}
	if !validDigest(profile.SourcePolicyDigest) || !validDigest(profile.TrustPolicyDigest) ||
		profile.SourcePolicyDigest == profile.TrustPolicyDigest {
		return invalid("planInputProfile", "source and trust policy digests must be valid and domain-distinct")
	}
	manifest := profile.QualificationManifest
	if !validStableID(manifest.ArtifactID, 256) || !validUUIDv4String(manifest.RevisionID) ||
		!validDigest(manifest.ContentHash) || !validDigest(manifest.PlanDigest) || manifest.ContentHash == manifest.PlanDigest {
		return invalid("planInputProfile.qualificationManifest", "immutable manifest binding is invalid")
	}
	if err := validateTemplateRelease(profile.TemplateRelease); err != nil {
		return err
	}
	if err := validateGoldenRuntime(profile.GoldenRuntime); err != nil {
		return err
	}
	if err := validateTrustBindings(profile.TrustBindings); err != nil {
		return err
	}
	credential := profile.CredentialProfile
	if !validIdentity(credential.AuthorityID) || credential.AuthorityID != profile.TrustBindings.CredentialAuthorityID ||
		!validAudience(credential.Audience) || !validStableID(credential.IssuanceArtifactID, 256) ||
		!validStableID(credential.RevocationArtifactID, 256) || credential.IssuanceArtifactID == credential.RevocationArtifactID ||
		!validDigest(credential.MemberRequestSetDigest) {
		return invalid("planInputProfile.credentialProfile", "trusted credential precommit profile is invalid")
	}
	if !validStableID(profile.Recipient.KeyResourceID, 256) || !validStableID(profile.Recipient.KeyVersion, 256) {
		return invalid("planInputProfile.recipient", "KMS recipient is invalid")
	}
	outputs := []string{
		profile.Outputs.KMSAttestationArtifactID,
		profile.Outputs.ArtifactIndexID,
		profile.Outputs.ReceiptID,
		profile.Outputs.SnapshotID,
		credential.IssuanceArtifactID,
		credential.RevocationArtifactID,
	}
	seenOutputs := make(map[string]struct{}, len(outputs))
	for _, output := range outputs {
		if !validStableID(output, 256) {
			return invalid("planInputProfile.outputs", "output artifact identity is invalid")
		}
		if _, duplicate := seenOutputs[output]; duplicate {
			return invalid("planInputProfile.outputs", "output artifact identities must be unique")
		}
		seenOutputs[output] = struct{}{}
	}
	if profile.Artifacts == nil || len(profile.Artifacts) < 1 || len(profile.Artifacts) > qualificationevidence.MaximumArtifacts {
		return invalid("planInputProfile.artifacts", "must contain a bounded non-empty expectation array")
	}
	restricted, traces, videos := 0, 0, 0
	for index, artifact := range profile.Artifacts {
		if !validStableID(artifact.ID, 256) || !validArtifactKind(artifact.Kind) ||
			!validClassificationForKind(artifact.Kind, artifact.Classification) {
			return invalid("planInputProfile.artifacts", "entry %d is invalid", index)
		}
		if index > 0 && bytes.Compare([]byte(profile.Artifacts[index-1].ID), []byte(artifact.ID)) >= 0 {
			return invalid("planInputProfile.artifacts", "must have strictly sorted unique IDs")
		}
		if _, collision := seenOutputs[artifact.ID]; collision {
			return invalid("planInputProfile.artifacts", "captured and generated artifact identities collide")
		}
		if artifact.Classification == qualificationevidence.ClassificationRestricted {
			restricted++
		}
		if artifact.Kind == qualificationevidence.ArtifactKindTrace {
			traces++
		}
		if artifact.Kind == qualificationevidence.ArtifactKindVideo {
			videos++
		}
	}
	if restricted == 0 || traces == 0 || videos == 0 {
		return invalid("planInputProfile.artifacts", "must include restricted trace and video evidence")
	}
	encoded, err := CanonicalJSON(profile)
	if err != nil {
		return err
	}
	return validateSecretFree("planInputProfile", encoded)
}

func ValidatePromotionPolicy(policy PromotionPolicy) error {
	if policy.SchemaVersion != PromotionPolicySchemaV1 ||
		policy.PlanAuthoritySchema != QualificationPlanAuthoritySchemaV1 ||
		policy.ReceiptSchema != QualificationReceiptSchemaV3 ||
		policy.SingleUseProtocol != QualificationPromotionProtocolV2 {
		return invalid("promotionPolicy", "schema or qualification protocol binding is invalid")
	}
	if policy.IndependentRequirements == nil || len(policy.IndependentRequirements) > MaximumIndependentRequirements {
		return invalid("promotionPolicy.independentRequirements", "must be a present bounded array")
	}
	seenIDs := make(map[string]struct{}, len(policy.IndependentRequirements))
	seenHashes := make(map[string]struct{}, len(policy.IndependentRequirements))
	for index, requirement := range policy.IndependentRequirements {
		if requirement.Kind != IndependentModelProfileActivation && requirement.Kind != IndependentProductionPostgres {
			return invalid("promotionPolicy.independentRequirements", "entry %d has an unknown authority kind", index)
		}
		if !validOpaqueAuthorityID(requirement.AuthorityID) || !validDigest(requirement.AuthorityHash) {
			return invalid("promotionPolicy.independentRequirements", "entry %d has an invalid opaque ID/hash binding", index)
		}
		if index > 0 && bytes.Compare([]byte(policy.IndependentRequirements[index-1].Kind), []byte(requirement.Kind)) >= 0 {
			return invalid("promotionPolicy.independentRequirements", "must be strictly sorted and unique by kind")
		}
		if _, duplicate := seenIDs[requirement.AuthorityID]; duplicate {
			return invalid("promotionPolicy.independentRequirements", "one authority ID cannot satisfy multiple independent roles")
		}
		if _, duplicate := seenHashes[requirement.AuthorityHash]; duplicate {
			return invalid("promotionPolicy.independentRequirements", "one authority hash cannot satisfy multiple independent roles")
		}
		seenIDs[requirement.AuthorityID] = struct{}{}
		seenHashes[requirement.AuthorityHash] = struct{}{}
	}
	encoded, err := CanonicalJSON(policy)
	if err != nil {
		return err
	}
	return validateSecretFree("promotionPolicy", encoded)
}

func ValidateResolvedPolicy(resolved ResolvedPolicy) error {
	if resolved.ProjectID.Version() != 4 || resolved.ExecutionProfile.Version != ExecutionProfileV3 ||
		!validDigest(resolved.ExecutionProfile.Hash) || resolved.ExternalGatePolicy != ExternalGatePolicyV1 ||
		resolved.SupersessionPolicy != SupersessionPolicyV1 ||
		(resolved.Status != AuthorityStatusActive && resolved.Status != AuthorityStatusSuspended) {
		return invalid("resolvedPolicy", "project, profile, gate, status, or supersession binding is invalid")
	}
	if err := ValidateRevisionPolicy(resolved.RevisionPolicy); err != nil {
		return err
	}
	if err := ValidatePlanInputProfile(resolved.PlanInputProfile); err != nil {
		return err
	}
	return ValidatePromotionPolicy(resolved.PromotionPolicy)
}

func ValidateAuthorityDocument(document AuthorityDocument) error {
	if document.SchemaVersion != AuthoritySchemaV1 || !validUUIDv4String(document.AuthorityID) ||
		!validUUIDv4String(document.OperationID) || !validUUIDv4String(document.ProjectID) ||
		document.AuthorityID == document.OperationID || document.AuthorityID == document.ProjectID || document.OperationID == document.ProjectID ||
		!validOpaqueSourceID(document.PolicySourceID) || forbiddenSecretString(document.PolicySourceID) ||
		document.ExecutionProfile.Version != ExecutionProfileV3 || !validDigest(document.ExecutionProfile.Hash) ||
		document.ExternalGatePolicy != ExternalGatePolicyV1 || document.SupersessionPolicy != SupersessionPolicyV1 ||
		(document.Status != AuthorityStatusActive && document.Status != AuthorityStatusSuspended) ||
		document.Generation < 1 || document.Generation > MaximumJavaScriptSafeInteger {
		return invalid("authority", "root identity, profile, lifecycle, or generation is invalid")
	}
	if _, err := parseCanonicalTime(document.IssuedAt); err != nil {
		return err
	}
	if document.Generation == 1 {
		if document.PreviousAuthorityHash != nil {
			return invalid("authority.previousAuthorityHash", "must be null for generation one")
		}
	} else if document.PreviousAuthorityHash == nil || !validDigest(*document.PreviousAuthorityHash) {
		return invalid("authority.previousAuthorityHash", "must bind generation minus one")
	}
	if err := ValidateRevisionPolicy(document.RevisionPolicy); err != nil {
		return err
	}
	if err := ValidatePlanInputProfile(document.PlanInputProfile); err != nil {
		return err
	}
	if err := ValidatePromotionPolicy(document.PromotionPolicy); err != nil {
		return err
	}
	revisionBytes, err := CanonicalJSON(document.RevisionPolicy)
	if err != nil {
		return err
	}
	planBytes, err := CanonicalJSON(document.PlanInputProfile)
	if err != nil {
		return err
	}
	promotionBytes, err := CanonicalJSON(document.PromotionPolicy)
	if err != nil {
		return err
	}
	want := ComponentDigests{
		RevisionPolicy:   DomainHash(RevisionPolicyHashDomainV1, revisionBytes),
		PlanInputProfile: DomainHash(PlanInputProfileHashDomainV1, planBytes),
		PromotionPolicy:  DomainHash(PromotionPolicyHashDomainV1, promotionBytes),
	}
	if document.ComponentDigests != want || want.RevisionPolicy == want.PlanInputProfile ||
		want.RevisionPolicy == want.PromotionPolicy || want.PlanInputProfile == want.PromotionPolicy {
		return invalid("authority.componentDigests", "do not exactly authenticate the embedded closed components")
	}
	if document.PreviousAuthorityHash != nil &&
		(*document.PreviousAuthorityHash == want.RevisionPolicy || *document.PreviousAuthorityHash == want.PlanInputProfile ||
			*document.PreviousAuthorityHash == want.PromotionPolicy) {
		return invalid("authority.previousAuthorityHash", "is confused with a component hash domain")
	}
	if localIdentityCollidesWithEmbeddedReference(document) {
		return invalid("authority", "operation or authority identity collides with an embedded immutable reference")
	}
	encoded, err := canonicalJSONWithLimit(document, MaximumAuthorityBytes)
	if err != nil {
		return err
	}
	return validateSecretFree("authority", encoded)
}

// ValidateRecord independently decodes every retained byte sequence, recomputes
// all four domain hashes, and verifies command/document/store projections.
func ValidateRecord(record Record) error {
	if err := validateIssueCommand(record.Command); err != nil {
		return err
	}
	revision, err := DecodeRevisionPolicy(record.RevisionPolicyBytes, record.RevisionPolicyHash)
	if err != nil {
		return invalid("record.revisionPolicy", "%v", err)
	}
	plan, err := DecodePlanInputProfile(record.PlanInputProfileBytes, record.PlanInputProfileHash)
	if err != nil {
		return invalid("record.planInputProfile", "%v", err)
	}
	promotion, err := DecodePromotionPolicy(record.PromotionPolicyBytes, record.PromotionPolicyHash)
	if err != nil {
		return invalid("record.promotionPolicy", "%v", err)
	}
	document, err := DecodeAuthorityDocument(record.DocumentBytes, record.AuthorityHash)
	if err != nil {
		return invalid("record.authority", "%v", err)
	}
	if !reflect.DeepEqual(record.RevisionPolicy, revision) || !reflect.DeepEqual(record.PlanInputProfile, plan) ||
		!reflect.DeepEqual(record.PromotionPolicy, promotion) || !reflect.DeepEqual(record.Document, document) ||
		!reflect.DeepEqual(document.RevisionPolicy, revision) || !reflect.DeepEqual(document.PlanInputProfile, plan) ||
		!reflect.DeepEqual(document.PromotionPolicy, promotion) {
		return invalid("record", "typed projections differ from exact canonical bytes")
	}
	if document.OperationID != record.Command.OperationID.String() || document.AuthorityID != record.Command.AuthorityID.String() ||
		document.PolicySourceID != record.Command.PolicySourceID ||
		document.ComponentDigests.RevisionPolicy != record.RevisionPolicyHash ||
		document.ComponentDigests.PlanInputProfile != record.PlanInputProfileHash ||
		document.ComponentDigests.PromotionPolicy != record.PromotionPolicyHash {
		return invalid("record", "command, root, and component bindings drifted")
	}
	issuedAt, err := parseCanonicalTime(document.IssuedAt)
	if err != nil || record.IssuedAt.Location() != time.UTC || !record.IssuedAt.Equal(issuedAt) ||
		record.IssuedAt.Nanosecond()%int(time.Millisecond) != 0 {
		return invalid("record.issuedAt", "does not equal the database authority time")
	}
	if document.Generation == 1 {
		if record.Command.ExpectedPreviousAuthorityHash != "" || document.PreviousAuthorityHash != nil {
			return invalid("record.previousAuthorityHash", "first generation must use an empty compare-and-swap cursor")
		}
	} else if !validDigest(record.Command.ExpectedPreviousAuthorityHash) || document.PreviousAuthorityHash == nil ||
		*document.PreviousAuthorityHash != record.Command.ExpectedPreviousAuthorityHash {
		return invalid("record.previousAuthorityHash", "does not equal the command compare-and-swap cursor")
	}
	hashes := []string{record.RevisionPolicyHash, record.PlanInputProfileHash, record.PromotionPolicyHash, record.AuthorityHash}
	seen := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		if _, duplicate := seen[hash]; duplicate {
			return invalid("record", "authority hash domains are aliased")
		}
		seen[hash] = struct{}{}
	}
	return nil
}

func validateIssueCommand(command IssueCommand) error {
	if command.OperationID.Version() != 4 || command.AuthorityID.Version() != 4 || command.OperationID == command.AuthorityID ||
		!validOpaqueSourceID(command.PolicySourceID) ||
		(command.ExpectedPreviousAuthorityHash != "" && !validDigest(command.ExpectedPreviousAuthorityHash)) {
		return invalid("issueCommand", "requires distinct UUIDv4 identities, an opaque source ID, and an optional exact previous hash")
	}
	if forbiddenSecretString(command.PolicySourceID) {
		return invalid("issueCommand.policySourceId", "contains forbidden secret or location material")
	}
	return nil
}

func validateTemplateRelease(binding qualificationreceipt.TemplateReleaseBinding) error {
	if !validUUIDv4String(binding.ID) || !validDigest(binding.ContentHash) || !validDigest(binding.ApprovalReceiptDigest) ||
		binding.ContentHash == binding.ApprovalReceiptDigest {
		return invalid("planInputProfile.templateRelease", "immutable template binding is invalid")
	}
	return nil
}

func validateGoldenRuntime(binding qualificationreceipt.GoldenRuntimeBinding) error {
	if !validStableID(binding.AuthorityDocumentArtifactID, 256) || !validDigest(binding.AuthorityDocumentDigest) ||
		!validStableID(binding.FixtureDocumentArtifactID, 256) || !validDigest(binding.FixtureDocumentDigest) ||
		binding.AuthorityDocumentArtifactID == binding.FixtureDocumentArtifactID ||
		binding.AuthorityDocumentDigest == binding.FixtureDocumentDigest || !validUUIDv4String(binding.FixtureID) ||
		binding.FaultOperationSetDigest != qualificationreceipt.GoldenFaultOperationSetDigestV1 {
		return invalid("planInputProfile.goldenRuntime", "immutable Golden runtime binding is invalid")
	}
	return nil
}

func validateTrustBindings(trust qualificationevidence.TrustBindings) error {
	identities := []string{
		trust.CaptureAuthorityID,
		trust.CredentialAuthorityID,
		trust.EncryptionAuthorityID,
		trust.IndexerAuthorityID,
		trust.KMSAuthorityID,
		trust.ReceiptAuthorityID,
		trust.SealerAuthorityID,
		trust.VerifierAuthorityID,
	}
	seen := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		if !validIdentity(identity) {
			return invalid("planInputProfile.trustBindings", "authority identity is invalid")
		}
		if _, duplicate := seen[identity]; duplicate {
			return invalid("planInputProfile.trustBindings", "all eight authority roles must be distinct")
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func validArtifactKind(kind qualificationevidence.ArtifactKind) bool {
	switch kind {
	case qualificationevidence.ArtifactKindRunResult,
		qualificationevidence.ArtifactKindTrace,
		qualificationevidence.ArtifactKindVideo,
		qualificationevidence.ArtifactKindLog,
		qualificationevidence.ArtifactKindGolden,
		qualificationevidence.ArtifactKindFault,
		qualificationevidence.ArtifactKindWriterDrain,
		qualificationevidence.ArtifactKindRuntimeProof:
		return true
	default:
		return false
	}
}

func validClassificationForKind(kind qualificationevidence.ArtifactKind, classification qualificationevidence.Classification) bool {
	switch kind {
	case qualificationevidence.ArtifactKindTrace, qualificationevidence.ArtifactKindVideo, qualificationevidence.ArtifactKindLog:
		return classification == qualificationevidence.ClassificationRestricted
	default:
		return classification == qualificationevidence.ClassificationDistributable
	}
}

func validUUIDv4String(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.Version() == 4 && parsed.String() == value
}

func validDigest(value string) bool {
	return digestPattern.MatchString(value)
}

func validStableID(value string, maximum int) bool {
	return len(value) > 0 && len(value) <= maximum && stableIDPattern.MatchString(value)
}

func validIdentity(value string) bool {
	return len(value) > 0 && len(value) <= 512 && identityPattern.MatchString(value)
}

func validAudience(value string) bool {
	return len(value) > 0 && len(value) <= 512 && audiencePattern.MatchString(value)
}

func validOpaqueAuthorityID(value string) bool {
	return validCanonicalString(value, 512) && stableIDPattern.MatchString(value)
}

func validOpaqueSourceID(value string) bool {
	return validCanonicalString(value, 256) && stableIDPattern.MatchString(value)
}

func validCanonicalString(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n\t")
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

func sameIssueCommand(left, right IssueCommand) bool {
	return left.OperationID == right.OperationID && left.AuthorityID == right.AuthorityID &&
		left.PolicySourceID == right.PolicySourceID && left.ExpectedPreviousAuthorityHash == right.ExpectedPreviousAuthorityHash
}

func sameImmutableRecord(left, right Record) bool {
	return sameIssueCommand(left.Command, right.Command) && left.AuthorityHash == right.AuthorityHash &&
		bytes.Equal(left.DocumentBytes, right.DocumentBytes) && left.RevisionPolicyHash == right.RevisionPolicyHash &&
		bytes.Equal(left.RevisionPolicyBytes, right.RevisionPolicyBytes) && left.PlanInputProfileHash == right.PlanInputProfileHash &&
		bytes.Equal(left.PlanInputProfileBytes, right.PlanInputProfileBytes) && left.PromotionPolicyHash == right.PromotionPolicyHash &&
		bytes.Equal(left.PromotionPolicyBytes, right.PromotionPolicyBytes) && left.IssuedAt.Equal(right.IssuedAt)
}

func headMatchesRecord(projectID uuid.UUID, profile ExecutionProfileBinding, record Record) bool {
	return record.Document.ProjectID == projectID.String() && record.Document.ExecutionProfile == profile
}

type embeddedUUIDReference struct {
	id   uuid.UUID
	role string
}

func embeddedUUIDReferences(document AuthorityDocument) []embeddedUUIDReference {
	references := make([]embeddedUUIDReference, 0, 32+len(document.RevisionPolicy.ExactApprovedSources)*2)
	appendReference := func(role, value string) {
		parsed, err := uuid.Parse(value)
		if err == nil && parsed.Version() == 4 && parsed.String() == value {
			references = append(references, embeddedUUIDReference{id: parsed, role: role})
		}
	}
	appendReference("project", document.ProjectID)
	for _, source := range document.RevisionPolicy.ExactApprovedSources {
		appendReference("exact-source-artifact", source.ArtifactID)
		appendReference("exact-source-revision", source.RevisionID)
	}
	profile := document.PlanInputProfile
	appendReference("qualification-manifest-artifact", profile.QualificationManifest.ArtifactID)
	appendReference("qualification-manifest-revision", profile.QualificationManifest.RevisionID)
	appendReference("template-release", profile.TemplateRelease.ID)
	appendReference("golden-fixture", profile.GoldenRuntime.FixtureID)
	appendReference("credential-authority", profile.CredentialProfile.AuthorityID)
	appendReference("credential-issuance-artifact", profile.CredentialProfile.IssuanceArtifactID)
	appendReference("credential-revocation-artifact", profile.CredentialProfile.RevocationArtifactID)
	appendReference("kms-recipient-resource", profile.Recipient.KeyResourceID)
	appendReference("kms-recipient-version", profile.Recipient.KeyVersion)
	appendReference("kms-attestation-output", profile.Outputs.KMSAttestationArtifactID)
	appendReference("artifact-index-output", profile.Outputs.ArtifactIndexID)
	appendReference("receipt-output", profile.Outputs.ReceiptID)
	appendReference("snapshot-output", profile.Outputs.SnapshotID)
	for _, artifact := range profile.Artifacts {
		appendReference("evidence-artifact", artifact.ID)
	}
	trust := profile.TrustBindings
	appendReference("capture-authority", trust.CaptureAuthorityID)
	appendReference("credential-trust-authority", trust.CredentialAuthorityID)
	appendReference("encryption-authority", trust.EncryptionAuthorityID)
	appendReference("indexer-authority", trust.IndexerAuthorityID)
	appendReference("kms-authority", trust.KMSAuthorityID)
	appendReference("receipt-authority", trust.ReceiptAuthorityID)
	appendReference("sealer-authority", trust.SealerAuthorityID)
	appendReference("verifier-authority", trust.VerifierAuthorityID)
	for _, requirement := range document.PromotionPolicy.IndependentRequirements {
		appendReference("independent-"+requirement.Kind, requirement.AuthorityID)
	}
	return references
}

func localIdentityCollidesWithEmbeddedReference(document AuthorityDocument) bool {
	operationID := uuid.MustParse(document.OperationID)
	authorityID := uuid.MustParse(document.AuthorityID)
	for _, reference := range embeddedUUIDReferences(document) {
		if reference.id == operationID || reference.id == authorityID {
			return true
		}
	}
	return false
}

func wrapStoredConflict(field string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: stored %s failed independent validation: %v", ErrConflict, field, err)
}
