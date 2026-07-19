package qualificationreceiptv3

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationevidence"
)

const (
	canonicalTimeLayout      = "2006-01-02T15:04:05.000Z"
	maximumSafeInteger       = int64(9007199254740991)
	maximumPayloadBytes      = 8 << 20
	maximumEvidenceArtifacts = MaximumArtifacts + 3
)

var (
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	commitPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	stableIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$`)
	identityPattern = regexp.MustCompile(`^(?:spiffe://[a-z0-9.-]+/[a-z0-9._/-]+|[a-z0-9]+(?:[._:-][a-z0-9]+)*)$`)
	audiencePattern = regexp.MustCompile(`^(?:urn:[a-z0-9][a-z0-9:._-]+|[a-z0-9]+(?:[._:-][a-z0-9]+)*)$`)
)

type authorityEnvelope struct {
	ArtifactID          string `json:"artifactId"`
	AuthorityID         string `json:"authorityId"`
	EvidencePlanHash    string `json:"evidencePlanHash"`
	InputAuthorityID    string `json:"inputAuthorityId"`
	InputHash           string `json:"inputHash"`
	ManifestPlanDigest  string `json:"manifestPlanDigest"`
	OperationID         string `json:"operationId"`
	ProjectionHash      string `json:"projectionHash"`
	SchemaVersion       string `json:"schemaVersion"`
	TargetHash          string `json:"targetHash"`
	TrustBindingsDigest string `json:"trustBindingsDigest"`
	TrustHash           string `json:"trustHash"`
}

// CanonicalJSON is the hash authority for Receipt v3: BOM-free UTF-8 JSON,
// lexicographically sorted object names, integer-only cross-language numbers,
// and no insignificant whitespace.
func CanonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, invalid("canonicalJSON", "encode JSON: %v", err)
	}
	if !utf8.Valid(encoded) {
		return nil, invalid("canonicalJSON", "JSON is not UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, invalid("canonicalJSON", "decode JSON: %v", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, invalid("canonicalJSON", "%v", err)
	}
	if err := validateCanonicalValue(generic); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(generic)
	if err != nil {
		return nil, invalid("canonicalJSON", "encode canonical JSON: %v", err)
	}
	return canonical, nil
}

func CanonicalDigest(value any) (string, error) {
	encoded, err := CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	return SHA256Digest(encoded), nil
}

// SHA256Digest returns the only digest representation admitted by Receipt v3.
func SHA256Digest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// Compile validates a complete Receipt and emits the exact canonical in-toto
// payload that two independent authorities must sign. The subject is the
// already sealed pre-Receipt snapshot, never the Receipt itself.
func Compile(receipt Receipt) (CompiledPayload, error) {
	if err := validateReceipt(receipt); err != nil {
		return CompiledPayload{}, err
	}
	subjectDigest := strings.TrimPrefix(receipt.Snapshot.SnapshotDigest, "sha256:")
	statement := InTotoStatement{
		Type:          InTotoStatementV1,
		Predicate:     receipt,
		PredicateType: ReceiptPredicateTypeV3,
		Subject: []InTotoSubject{{
			Digest: map[string]string{"sha256": subjectDigest},
			Name:   receipt.Snapshot.SnapshotID,
		}},
	}
	payload, err := CanonicalJSON(statement)
	if err != nil {
		return CompiledPayload{}, err
	}
	decoded, err := DecodePayload(payload)
	if err != nil {
		return CompiledPayload{}, invalid("payload", "compiled payload failed independent decode: %v", err)
	}
	return CompiledPayload{
		Payload:       bytes.Clone(payload),
		PayloadDigest: SHA256Digest(payload),
		Receipt:       decoded,
		SubjectDigest: receipt.Snapshot.SnapshotDigest,
		SubjectName:   receipt.Snapshot.SnapshotID,
	}, nil
}

// DecodePayload strictly decodes an exact canonical Receipt v3 in-toto
// payload. Unknown or duplicate fields, missing zero-valued fields, null/empty
// arrays, non-canonical JSON, and trailing bytes are rejected.
func DecodePayload(payload []byte) (Receipt, error) {
	if len(payload) == 0 || len(payload) > maximumPayloadBytes {
		return Receipt{}, invalid("payload", "size must be between 1 and %d bytes", maximumPayloadBytes)
	}
	var statement InTotoStatement
	if err := decodeStrictJSON(payload, &statement); err != nil {
		return Receipt{}, invalid("payload", "%v", err)
	}
	canonical, err := CanonicalJSON(statement)
	if err != nil {
		return Receipt{}, err
	}
	if !bytes.Equal(canonical, payload) {
		return Receipt{}, invalid("payload", "payload is not the exact canonical JSON representation")
	}
	if statement.Type != InTotoStatementV1 || statement.PredicateType != ReceiptPredicateTypeV3 {
		return Receipt{}, invalid("payload", "statement or predicate type is invalid")
	}
	if len(statement.Subject) != 1 || len(statement.Subject[0].Digest) != 1 {
		return Receipt{}, invalid("payload.subject", "exactly one SHA-256 subject is required")
	}
	if err := validateReceipt(statement.Predicate); err != nil {
		return Receipt{}, err
	}
	expectedDigest := strings.TrimPrefix(statement.Predicate.Snapshot.SnapshotDigest, "sha256:")
	if statement.Subject[0].Name != statement.Predicate.Snapshot.SnapshotID ||
		statement.Subject[0].Digest["sha256"] != expectedDigest {
		return Receipt{}, invalid("payload.subject", "subject does not bind the exact pre-Receipt snapshot")
	}
	return statement.Predicate, nil
}

func validateReceipt(receipt Receipt) error {
	if receipt.SchemaVersion != ReceiptSchemaV3 || !validStableID(receipt.ReceiptID) || !validUUIDv4(receipt.OperationID) ||
		receipt.Decision != DecisionQualified {
		return invalid("receipt", "schema, identity, operation, or decision is invalid")
	}
	if err := validatePlanAuthority(receipt); err != nil {
		return err
	}
	if err := validateTarget(receipt.Target, receipt.PlanAuthority.TargetHash); err != nil {
		return err
	}
	if err := validateTrust(receipt.Trust, receipt.PlanAuthority); err != nil {
		return err
	}
	if err := validateSource(receipt.Source, receipt.EvidencePlan); err != nil {
		return err
	}
	if err := validateTemplate(receipt.TemplateRelease, receipt.EvidencePlan); err != nil {
		return err
	}
	if err := validateBuild(receipt.Build); err != nil {
		return err
	}
	if err := validateQualificationManifest(receipt.QualificationManifest, receipt.PlanAuthority); err != nil {
		return err
	}
	if err := validateGolden(receipt.GoldenRuntime, receipt.EvidencePlan); err != nil {
		return err
	}
	if err := validateCredential(receipt.CredentialSet, receipt.EvidencePlan, receipt.Trust.TrustBindings); err != nil {
		return err
	}
	if err := validateEvidence(receipt.Evidence, receipt.EvidencePlan); err != nil {
		return err
	}
	if err := validateArtifactIndex(receipt.ArtifactIndex, receipt.Evidence, receipt.EvidencePlan, receipt.GoldenRuntime, receipt.CredentialSet, receipt.Trust.TrustBindings); err != nil {
		return err
	}
	if err := validateSnapshot(receipt.Snapshot, receipt.ArtifactIndex, receipt.Evidence, receipt.EvidencePlan, receipt.Trust.TrustBindings); err != nil {
		return err
	}
	if err := validateSnapshotVerification(receipt.SnapshotVerification, receipt.Snapshot, receipt.Trust.TrustBindings); err != nil {
		return err
	}
	if err := validateSigners(receipt.Signers); err != nil {
		return err
	}
	allRoleIdentities := []string{
		receipt.Trust.TrustBindings.CaptureAuthorityID,
		receipt.Trust.TrustBindings.CredentialAuthorityID,
		receipt.Trust.TrustBindings.EncryptionAuthorityID,
		receipt.Trust.TrustBindings.IndexerAuthorityID,
		receipt.Trust.TrustBindings.KMSAuthorityID,
		receipt.Trust.TrustBindings.ReceiptAuthorityID,
		receipt.Trust.TrustBindings.SealerAuthorityID,
		receipt.Trust.TrustBindings.VerifierAuthorityID,
		receipt.Signers.Runner.Identity,
		receipt.Signers.Approver.Identity,
	}
	if !uniqueStrings(allRoleIdentities) {
		return invalid("signers", "runner, approver, and operational authorities must use globally distinct identities")
	}
	if receipt.OperationID != receipt.EvidencePlan.Operations.ReceiptSign || receipt.ReceiptID != receipt.EvidencePlan.Outputs.ReceiptID {
		return invalid("receipt", "receipt operation or output identity drifted from the frozen evidence Plan")
	}
	started, err := parseCanonicalTime(receipt.QualificationStartedAt, "qualificationStartedAt")
	if err != nil {
		return err
	}
	completed, err := parseCanonicalTime(receipt.CompletedAt, "completedAt")
	if err != nil {
		return err
	}
	issued, err := parseCanonicalTime(receipt.IssuedAt, "issuedAt")
	if err != nil {
		return err
	}
	sealed, _ := parseCanonicalTime(receipt.Snapshot.SealedAt, "snapshot.sealedAt")
	verified, _ := parseCanonicalTime(receipt.SnapshotVerification.VerifiedAt, "snapshotVerification.verifiedAt")
	indexCommitted, _ := parseCanonicalTime(receipt.ArtifactIndex.CommittedAt, "artifactIndex.committedAt")
	credentialIssued, _ := parseCanonicalTime(receipt.CredentialSet.IssuedAt, "credentialSet.issuedAt")
	credentialRevoked, _ := parseCanonicalTime(receipt.CredentialSet.RevokedAt, "credentialSet.revokedAt")
	if !credentialIssued.Before(started) || !started.Before(credentialRevoked) || !credentialRevoked.Before(indexCommitted) ||
		indexCommitted.After(sealed) || verified.Before(sealed) || completed.Before(verified) || issued.Before(completed) {
		return invalid("receipt.time", "require credential issued < qualification started < revoked < index committed <= sealed <= independently verified <= completed <= Receipt issued")
	}
	return nil
}

func validatePlanAuthority(receipt Receipt) error {
	authority := receipt.PlanAuthority
	for field, value := range map[string]string{
		"authorityHash": authority.AuthorityHash, "evidencePlanHash": authority.EvidencePlanHash,
		"inputHash": authority.InputHash, "planDigest": authority.PlanDigest, "projectionHash": authority.ProjectionHash,
		"targetHash": authority.TargetHash, "trustBindingsDigest": authority.TrustBindingsDigest, "trustHash": authority.TrustHash,
	} {
		if !validDigest(value) {
			return invalid("planAuthority."+field, "digest is invalid")
		}
	}
	if !validUUIDv4(authority.AuthorityID) || !validUUIDv4(authority.FreezeOperationID) || !validUUIDv4(authority.InputAuthorityID) ||
		authority.ArtifactID != PlanArtifactPrefix+authority.AuthorityID || authority.PlanDigest != authority.ProjectionHash {
		return invalid("planAuthority", "identity, artifact, or projection/PlanDigest binding is invalid")
	}
	identities := []string{authority.AuthorityID, authority.FreezeOperationID, authority.InputAuthorityID}
	if !uniqueStrings(identities) {
		return invalid("planAuthority", "authority identities must be distinct")
	}
	digestDomains := []string{
		authority.AuthorityHash, authority.EvidencePlanHash, authority.InputHash, authority.PlanDigest,
		authority.TargetHash, authority.TrustBindingsDigest, authority.TrustHash,
	}
	if !uniqueStrings(digestDomains) {
		return invalid("planAuthority", "independent digest domains are aliased")
	}
	if err := qualificationevidence.ValidatePlan(receipt.EvidencePlan); err != nil {
		return invalid("evidencePlan", "%v", err)
	}
	planBytes, err := qualificationevidence.CanonicalJSON(receipt.EvidencePlan)
	if err != nil || SHA256Digest(planBytes) != authority.EvidencePlanHash {
		return invalid("planAuthority.evidencePlanHash", "does not hash the exact canonical evidence Plan")
	}
	if receipt.EvidencePlan.QualificationPlanArtifactID != authority.ArtifactID || receipt.EvidencePlan.PlanDigest != authority.PlanDigest {
		return invalid("evidencePlan", "qualification artifact or manifest PlanDigest drifted from Plan Authority")
	}
	envelope := authorityEnvelope{
		ArtifactID: authority.ArtifactID, AuthorityID: authority.AuthorityID,
		EvidencePlanHash: authority.EvidencePlanHash, InputAuthorityID: authority.InputAuthorityID,
		InputHash: authority.InputHash, ManifestPlanDigest: authority.PlanDigest,
		OperationID: authority.FreezeOperationID, ProjectionHash: authority.ProjectionHash,
		SchemaVersion: PlanAuthoritySchemaV1, TargetHash: authority.TargetHash,
		TrustBindingsDigest: authority.TrustBindingsDigest, TrustHash: authority.TrustHash,
	}
	envelopeDigest, err := CanonicalDigest(envelope)
	if err != nil || envelopeDigest != authority.AuthorityHash {
		return invalid("planAuthority.authorityHash", "does not hash the exact canonical authority envelope")
	}
	return nil
}

func validateTarget(target TargetBinding, expectedHash string) error {
	value := target.PromotionTarget
	if target.SchemaVersion != PlanTargetSchemaV1 || !validUUIDv4(value.ProjectID) || !validUUIDv4(value.WorkflowRunID) ||
		!validStableID(value.NodeKey) || !validUUIDv4(value.TargetRevision.ID) || !validDigest(value.TargetRevision.ContentHash) ||
		!validCanonicalString(value.Subject, 256) || value.StageGate != ExternalStageGate {
		return invalid("target", "target document is invalid")
	}
	digest, err := CanonicalDigest(target)
	if err != nil || digest != expectedHash {
		return invalid("planAuthority.targetHash", "does not hash the exact canonical target document")
	}
	return nil
}

func validateTrust(trust TrustBinding, authority PlanAuthorityBinding) error {
	if trust.SchemaVersion != PlanTrustSchemaV1 || !validDigest(trust.TrustPolicyDigest) {
		return invalid("trust", "schema or trust policy digest is invalid")
	}
	values := []string{
		trust.TrustBindings.CaptureAuthorityID, trust.TrustBindings.CredentialAuthorityID,
		trust.TrustBindings.EncryptionAuthorityID, trust.TrustBindings.IndexerAuthorityID,
		trust.TrustBindings.KMSAuthorityID, trust.TrustBindings.ReceiptAuthorityID,
		trust.TrustBindings.SealerAuthorityID, trust.TrustBindings.VerifierAuthorityID,
	}
	for _, value := range values {
		if !validIdentity(value) {
			return invalid("trust.trustBindings", "authority identity is invalid")
		}
	}
	if !uniqueStrings(values) {
		return invalid("trust.trustBindings", "authority identities must be distinct")
	}
	bindingDigest, err := CanonicalDigest(trust.TrustBindings)
	if err != nil || bindingDigest != authority.TrustBindingsDigest {
		return invalid("planAuthority.trustBindingsDigest", "does not hash exact canonical trust bindings")
	}
	trustDigest, err := CanonicalDigest(trust)
	if err != nil || trustDigest != authority.TrustHash {
		return invalid("planAuthority.trustHash", "does not hash exact canonical trust document")
	}
	return nil
}

func validateSource(source SourceBinding, plan qualificationevidence.Plan) error {
	if !commitPattern.MatchString(source.Commit) || source.Dirty || source.TreeDigestSchema != SourceTreeDigestSchemaV1 ||
		!validDigest(source.TreeDigest) || source.TreeDigest != plan.SourceTreeDigest {
		return invalid("source", "source must bind the frozen clean actual-byte tree")
	}
	return nil
}

func validateTemplate(template TemplateReleaseBinding, plan qualificationevidence.Plan) error {
	if !validUUIDv4(template.ID) || !validDigest(template.ContentHash) || !validDigest(template.ApprovalReceiptDigest) ||
		template.ContentHash == template.ApprovalReceiptDigest {
		return invalid("templateRelease", "template release identity or digests are invalid")
	}
	digest, err := CanonicalDigest(template)
	if err != nil || digest != plan.TemplateReleaseDigest {
		return invalid("templateRelease", "template release does not match the frozen evidence Plan")
	}
	return nil
}

func validateBuild(build BuildBinding) error {
	if !validStableID(build.Contract.ID) || !validDigest(build.Contract.ContentHash) ||
		!validStableID(build.Manifest.ID) || !validDigest(build.Manifest.ContentHash) ||
		build.Contract == build.Manifest || build.Contract.ID == build.Manifest.ID || build.Contract.ContentHash == build.Manifest.ContentHash {
		return invalid("build", "manifest and contract bindings must be valid and independent")
	}
	return nil
}

func validateQualificationManifest(manifest ArtifactRevisionBinding, authority PlanAuthorityBinding) error {
	if !validStableID(manifest.ArtifactID) || !validUUIDv4(manifest.RevisionID) || !validDigest(manifest.ContentHash) ||
		manifest.ContentHash == authority.PlanDigest || manifest.ContentHash == authority.InputHash {
		return invalid("qualificationManifest", "artifact, immutable revision, or content-hash binding is invalid")
	}
	return nil
}

func validateGolden(golden GoldenRuntimeBinding, plan qualificationevidence.Plan) error {
	if !validStableID(golden.AuthorityDocumentArtifactID) || !validDigest(golden.AuthorityDocumentDigest) ||
		!validStableID(golden.FixtureDocumentArtifactID) || !validDigest(golden.FixtureDocumentDigest) ||
		golden.AuthorityDocumentArtifactID == golden.FixtureDocumentArtifactID ||
		golden.AuthorityDocumentDigest == golden.FixtureDocumentDigest || !validUUIDv4(golden.FixtureID) ||
		golden.FixtureID != plan.FixtureID || golden.FaultOperationSetDigest != GoldenFaultOperationSetDigestV1 {
		return invalid("goldenRuntime", "Golden runtime binding is invalid or drifted from the evidence Plan")
	}
	foundAuthority, foundFixture := false, false
	for _, artifact := range plan.Artifacts {
		if artifact.ID != golden.AuthorityDocumentArtifactID && artifact.ID != golden.FixtureDocumentArtifactID {
			continue
		}
		if artifact.Kind != qualificationevidence.ArtifactKindGolden || artifact.Classification != qualificationevidence.ClassificationDistributable {
			return invalid("goldenRuntime", "Golden document is not a distributable Golden artifact in the evidence Plan")
		}
		foundAuthority = foundAuthority || artifact.ID == golden.AuthorityDocumentArtifactID
		foundFixture = foundFixture || artifact.ID == golden.FixtureDocumentArtifactID
	}
	if !foundAuthority || !foundFixture {
		return invalid("goldenRuntime", "Golden authority and fixture documents must both exist in the evidence Plan")
	}
	return nil
}

func validateCredential(credential CredentialSetBinding, plan qualificationevidence.Plan, trust TrustBindings) error {
	expected := plan.CredentialSet
	if !validUUIDv4(credential.SetID) || !validIdentity(credential.Issuer) || !validAudience(credential.Audience) ||
		!validDigest(credential.SetHandleHash) || !validDigest(credential.MemberBindingsDigest) ||
		credential.SetHandleHash == credential.MemberBindingsDigest || credential.MemberCount < 1 || credential.MemberCount > MaximumMembers {
		return invalid("credentialSet", "credential-set identity or commitments are invalid")
	}
	if credential.SetID != expected.SetID || credential.Issuer != expected.Issuer || credential.Issuer != trust.CredentialAuthorityID ||
		credential.Audience != expected.Audience || credential.SetHandleHash != expected.SetHandleHash ||
		credential.MemberBindingsDigest != expected.MemberBindingsDigest || credential.MemberCount != expected.MemberCount {
		return invalid("credentialSet", "credential set drifted from the frozen Plan or trust authority")
	}
	if err := validateSignedArtifact(credential.Issuance, expected.IssuanceArtifactID, "credentialSet.issuance"); err != nil {
		return err
	}
	if err := validateSignedArtifact(credential.Revocation, expected.RevocationArtifactID, "credentialSet.revocation"); err != nil {
		return err
	}
	if credential.Issuance.ContentDigest == credential.Revocation.ContentDigest ||
		credential.Issuance.PayloadDigest == credential.Revocation.PayloadDigest {
		return invalid("credentialSet", "issuance and revocation digest domains are aliased")
	}
	issued, err := parseCanonicalTime(credential.IssuedAt, "credentialSet.issuedAt")
	if err != nil {
		return err
	}
	revoked, err := parseCanonicalTime(credential.RevokedAt, "credentialSet.revokedAt")
	if err != nil {
		return err
	}
	expires, err := parseCanonicalTime(credential.ExpiresAt, "credentialSet.expiresAt")
	if err != nil {
		return err
	}
	if !issued.Before(revoked) || !revoked.Before(expires) {
		return invalid("credentialSet.time", "require issued < revoked < expires")
	}
	return nil
}

func validateSignedArtifact(value SignedArtifactBinding, expectedID, field string) error {
	if value.ArtifactID != expectedID || !validStableID(value.ArtifactID) || !validDigest(value.ContentDigest) ||
		!validDigest(value.PayloadDigest) || !validDigest(value.SignerSetDigest) ||
		!uniqueStrings([]string{value.ContentDigest, value.PayloadDigest, value.SignerSetDigest}) {
		return invalid(field, "signed artifact identity or digest closure is invalid")
	}
	return nil
}

func validateEvidence(evidence EvidenceClosureBinding, plan qualificationevidence.Plan) error {
	if evidence.SchemaVersion != EvidenceClosureSchemaV3 || evidence.OrchestrationID != plan.OrchestrationID || evidence.RunID != plan.RunID ||
		!validUUIDv4(evidence.OrchestrationID) || !validUUIDv4(evidence.RunID) {
		return invalid("evidence", "schema or run identity drifted from the evidence Plan")
	}
	digests := []string{
		evidence.ArtifactSetDigest, evidence.CaptureDigest, evidence.ClosureDigest,
		evidence.EncryptionManifestDigest, evidence.KMSAttestationDigest, evidence.ResultDigest,
	}
	for _, digest := range digests {
		if !validDigest(digest) {
			return invalid("evidence", "closure digest is invalid")
		}
	}
	if !uniqueStrings(digests) {
		return invalid("evidence", "independent evidence digest domains are aliased")
	}
	expectedArtifacts, expectedRestricted := expectedEvidenceArtifactIDs(plan)
	if !equalStrings(evidence.ArtifactIDs, expectedArtifacts) || !equalStrings(evidence.RestrictedArtifactIDs, expectedRestricted) ||
		!sortedUniqueStableIDs(evidence.ArtifactIDs, 1, maximumEvidenceArtifacts) ||
		!sortedUniqueStableIDs(evidence.RestrictedArtifactIDs, 1, MaximumArtifacts) {
		return invalid("evidence.artifactIds", "artifact and restricted subsets are not the exact frozen closure")
	}
	return nil
}

func validateArtifactIndex(index ArtifactIndexBinding, evidence EvidenceClosureBinding, plan qualificationevidence.Plan, golden GoldenRuntimeBinding, credential CredentialSetBinding, trust TrustBindings) error {
	if index.SchemaVersion != ArtifactIndexSchemaV3 || index.Stage != AuthorityStageCommitted ||
		!validUUIDv4(index.OperationID) || index.OperationID != plan.Operations.ArtifactIndex ||
		index.AuthorityID != trust.IndexerAuthorityID || !validIdentity(index.AuthorityID) ||
		!validStableID(index.IndexID) || index.IndexID != plan.Outputs.ArtifactIndexID ||
		!validDigest(index.RequestDigest) || !validDigest(index.ContentDigest) ||
		index.EvidenceClosureDigest != evidence.ClosureDigest || index.ArtifactSetDigest != evidence.ArtifactSetDigest ||
		index.ContentDigest == evidence.ClosureDigest || index.ContentDigest == evidence.ArtifactSetDigest {
		return invalid("artifactIndex", "index commitment is invalid or drifted from evidence/trust/Plan")
	}
	if !equalStrings(index.ArtifactIDs, evidence.ArtifactIDs) || !equalStrings(index.RestrictedArtifactIDs, evidence.RestrictedArtifactIDs) ||
		index.ArtifactCount != len(index.ArtifactIDs) || index.ArtifactCount != len(index.Artifacts) ||
		index.RestrictedArtifactCount != len(index.RestrictedArtifactIDs) {
		return invalid("artifactIndex", "index identities or repeated counts do not match evidence closure")
	}
	if _, err := parseCanonicalTime(index.CommittedAt, "artifactIndex.committedAt"); err != nil {
		return err
	}
	descriptorIDs := make([]string, 0, len(index.Artifacts))
	seenDigests := make(map[string]struct{}, len(index.Artifacts))
	contentByID := make(map[string]string, len(index.Artifacts))
	for position, artifact := range index.Artifacts {
		if !validStableID(artifact.ID) || !validDigest(artifact.ContentDigest) ||
			(position > 0 && index.Artifacts[position-1].ID >= artifact.ID) {
			return invalid("artifactIndex.artifacts", "descriptors must be canonical and strictly sorted")
		}
		if _, duplicate := seenDigests[artifact.ContentDigest]; duplicate {
			return invalid("artifactIndex.artifacts", "artifact content digests must be unique")
		}
		seenDigests[artifact.ContentDigest] = struct{}{}
		descriptorIDs = append(descriptorIDs, artifact.ID)
		contentByID[artifact.ID] = artifact.ContentDigest
	}
	if !equalStrings(descriptorIDs, index.ArtifactIDs) ||
		contentByID[golden.AuthorityDocumentArtifactID] != golden.AuthorityDocumentDigest ||
		contentByID[golden.FixtureDocumentArtifactID] != golden.FixtureDocumentDigest ||
		contentByID[credential.Issuance.ArtifactID] != credential.Issuance.ContentDigest ||
		contentByID[credential.Revocation.ArtifactID] != credential.Revocation.ContentDigest ||
		contentByID[plan.Outputs.KMSAttestationArtifactID] != evidence.KMSAttestationDigest {
		return invalid("artifactIndex.artifacts", "descriptor closure or Golden/credential/KMS content binding is not exact")
	}
	return nil
}

func validateSnapshot(snapshot PreReceiptSnapshotBinding, index ArtifactIndexBinding, evidence EvidenceClosureBinding, plan qualificationevidence.Plan, trust TrustBindings) error {
	if snapshot.SchemaVersion != PreReceiptSnapshotSchemaV3 || snapshot.Stage != AuthorityStageCommitted ||
		!validUUIDv4(snapshot.OperationID) || snapshot.OperationID != plan.Operations.SnapshotSeal ||
		snapshot.AuthorityID != trust.SealerAuthorityID || !validIdentity(snapshot.AuthorityID) ||
		!validStableID(snapshot.SnapshotID) || snapshot.SnapshotID != plan.Outputs.SnapshotID ||
		!validDigest(snapshot.RequestDigest) || !validDigest(snapshot.SnapshotDigest) ||
		snapshot.EvidenceClosureDigest != evidence.ClosureDigest || snapshot.ArtifactIndexDigest != index.ContentDigest ||
		snapshot.SnapshotDigest == evidence.ClosureDigest || snapshot.SnapshotDigest == index.ContentDigest ||
		snapshot.Mode != ImmutableSnapshotMode {
		return invalid("snapshot", "pre-Receipt snapshot is invalid or drifted from evidence/index/trust/Plan")
	}
	if _, err := parseCanonicalTime(snapshot.SealedAt, "snapshot.sealedAt"); err != nil {
		return err
	}
	return nil
}

func validateSnapshotVerification(verification SnapshotVerificationBinding, snapshot PreReceiptSnapshotBinding, trust TrustBindings) error {
	if verification.SchemaVersion != SnapshotVerificationSchemaV3 || verification.Result != VerificationPassed ||
		verification.AuthorityID != trust.VerifierAuthorityID || !validIdentity(verification.AuthorityID) ||
		verification.AuthorityID == snapshot.AuthorityID || verification.SnapshotID != snapshot.SnapshotID ||
		verification.SnapshotDigest != snapshot.SnapshotDigest || verification.EvidenceClosureDigest != snapshot.EvidenceClosureDigest ||
		verification.ArtifactIndexDigest != snapshot.ArtifactIndexDigest {
		return invalid("snapshotVerification", "independent verification does not bind the exact sealed snapshot")
	}
	verified, err := parseCanonicalTime(verification.VerifiedAt, "snapshotVerification.verifiedAt")
	if err != nil {
		return err
	}
	sealed, _ := parseCanonicalTime(snapshot.SealedAt, "snapshot.sealedAt")
	if verified.Before(sealed) {
		return invalid("snapshotVerification.verifiedAt", "verification predates sealing")
	}
	return nil
}

func validateSigners(signers ReceiptSignerBinding) error {
	if signers.Runner.Role != SignerRoleRunner || signers.Approver.Role != SignerRoleApprover ||
		!validStableID(signers.Runner.KeyID) || !validStableID(signers.Approver.KeyID) ||
		!validIdentity(signers.Runner.Identity) || !validIdentity(signers.Approver.Identity) ||
		signers.Runner.KeyID == signers.Approver.KeyID || signers.Runner.Identity == signers.Approver.Identity {
		return invalid("signers", "runner and approver must use distinct canonical keys and identities")
	}
	return nil
}

func expectedEvidenceArtifactIDs(plan qualificationevidence.Plan) ([]string, []string) {
	artifacts := make([]string, 0, len(plan.Artifacts)+3)
	restricted := make([]string, 0, len(plan.Artifacts))
	for _, artifact := range plan.Artifacts {
		artifacts = append(artifacts, artifact.ID)
		if artifact.Classification == qualificationevidence.ClassificationRestricted {
			restricted = append(restricted, artifact.ID)
		}
	}
	artifacts = append(artifacts,
		plan.CredentialSet.IssuanceArtifactID,
		plan.CredentialSet.RevocationArtifactID,
		plan.Outputs.KMSAttestationArtifactID,
	)
	sort.Strings(artifacts)
	sort.Strings(restricted)
	return artifacts, restricted
}

func parseCanonicalTime(value, field string) (time.Time, error) {
	parsed, err := time.Parse(canonicalTimeLayout, value)
	if err != nil || parsed.Format(canonicalTimeLayout) != value {
		return time.Time{}, invalid(field, "time must be canonical UTC milliseconds")
	}
	return parsed, nil
}

func validUUIDv4(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.Version() == 4 && parsed.String() == value
}

func validDigest(value string) bool { return digestPattern.MatchString(value) }

func validStableID(value string) bool {
	return len(value) > 0 && len(value) <= 256 && stableIDPattern.MatchString(value)
}

func validIdentity(value string) bool {
	return len(value) > 0 && len(value) <= 512 && identityPattern.MatchString(value)
}

func validAudience(value string) bool {
	return len(value) > 0 && len(value) <= 512 && audiencePattern.MatchString(value)
}

func validCanonicalString(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n\t")
}

func sortedUniqueStableIDs(values []string, minimum, maximum int) bool {
	if len(values) < minimum || len(values) > maximum {
		return false
	}
	for index, value := range values {
		if !validStableID(value) || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
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

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func invalid(field, format string, args ...any) error {
	detail := fmt.Sprintf(format, args...)
	if field == "" {
		return fmt.Errorf("%w: %s", ErrInvalid, detail)
	}
	return fmt.Errorf("%w: %s: %s", ErrInvalid, field, detail)
}

func validateCanonicalValue(value any) error {
	switch typed := value.(type) {
	case nil, bool:
		return nil
	case string:
		if !utf8.ValidString(typed) || strings.ContainsRune(typed, '\x00') {
			return invalid("canonicalJSON", "string is invalid")
		}
		return nil
	case json.Number:
		text := typed.String()
		if strings.ContainsAny(text, ".eE+") || text == "-0" {
			return invalid("canonicalJSON", "floats and non-canonical numbers are forbidden")
		}
		integer, err := strconv.ParseInt(text, 10, 64)
		if err != nil || integer < -maximumSafeInteger || integer > maximumSafeInteger {
			return invalid("canonicalJSON", "integer is outside the cross-language safe range")
		}
		return nil
	case []any:
		for _, element := range typed {
			if err := validateCanonicalValue(element); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for name, element := range typed {
			if name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') {
				return invalid("canonicalJSON", "object name is invalid")
			}
			if err := validateCanonicalValue(element); err != nil {
				return err
			}
		}
		return nil
	default:
		return invalid("canonicalJSON", "unsupported JSON value")
	}
}

func decodeStrictJSON(input []byte, target any) error {
	if !utf8.Valid(input) || bytes.HasPrefix(input, []byte{0xef, 0xbb, 0xbf}) || bytes.ContainsRune(input, utf8.RuneError) {
		return errors.New("JSON must be valid BOM-free UTF-8 without replacement characters")
	}
	if err := rejectDuplicateJSONNames(input); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func rejectDuplicateJSONNames(input []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("JSON object name is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate JSON object name %q", name)
			}
			seen[name] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("JSON has trailing content")
	}
	return err
}
