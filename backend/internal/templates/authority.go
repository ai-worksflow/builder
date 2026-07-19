package templates

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	ArtifactAuthorityReceiptSchemaVersion = "template-artifact-authority-receipt/v1"
	ArtifactAuthorityDecisionPassed       = "passed"
)

var authorityEntryIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,256}$`)

// ArtifactServiceSBOMReference pins one service image and its SPDX in-toto
// referrer. Both references must be digest-pinned OCI references; the concrete
// authority fetches and verifies their bytes under its server-owned policy.
type ArtifactServiceSBOMReference struct {
	ServiceID         string `json:"serviceId"`
	ImageReference    string `json:"imageReference"`
	ReferrerReference string `json:"referrerReference"`
}

// ArtifactAdmissionBundle contains references and signed bytes to be verified,
// never trusted evidence. Admit callers cannot provide GateEvidence,
// SignatureEnvelope, policy hashes, trust roots, or a verification decision.
type ArtifactAdmissionBundle struct {
	ArtifactReference     string                         `json:"artifactReference"`
	ServiceSBOMs          []ArtifactServiceSBOMReference `json:"serviceSboms"`
	DSSEEnvelope          json.RawMessage                `json:"dsseEnvelope"`
	TransparencyBundle    json.RawMessage                `json:"transparencyBundle"`
	VerificationReference string                         `json:"verificationReference"`
}

type ArtifactAuthorityIdentity struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type ArtifactTransparencyLog struct {
	ID           string    `json:"id"`
	EntryUUID    string    `json:"entryUuid"`
	LogIndex     int64     `json:"logIndex"`
	IntegratedAt time.Time `json:"integratedAt"`
}

type ArtifactAuthorityReceiptRef struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
	PolicyHash  string `json:"policyHash"`
}

type ArtifactBlobDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"sizeBytes"`
}

type ArtifactDescriptor struct {
	Reference  string                   `json:"reference"`
	MediaType  string                   `json:"mediaType"`
	Digest     string                   `json:"digest"`
	SizeBytes  int64                    `json:"sizeBytes"`
	Config     ArtifactBlobDescriptor   `json:"config"`
	Layers     []ArtifactBlobDescriptor `json:"layers"`
	TotalBytes int64                    `json:"totalBytes"`
}

type ArtifactSBOMServiceDescriptor struct {
	ServiceID         string `json:"serviceId"`
	ImageReference    string `json:"imageReference"`
	ImageDigest       string `json:"imageDigest"`
	ReferrerReference string `json:"referrerReference"`
	ReferrerDigest    string `json:"referrerDigest"`
	StatementDigest   string `json:"statementDigest"`
	PredicateDigest   string `json:"predicateDigest"`
	SPDXVersion       string `json:"spdxVersion"`
	DocumentNamespace string `json:"documentNamespace"`
	EvidenceHash      string `json:"evidenceHash"`
}

type ArtifactSBOMDescriptor struct {
	SchemaVersion string                          `json:"schemaVersion"`
	Digest        string                          `json:"digest"`
	ServiceCount  int                             `json:"serviceCount"`
	Services      []ArtifactSBOMServiceDescriptor `json:"services"`
}

type ArtifactAuthorityProof struct {
	PayloadType              string    `json:"payloadType"`
	PredicateType            string    `json:"predicateType"`
	PayloadDigest            string    `json:"payloadDigest"`
	SignatureBundleDigest    string    `json:"signatureBundleDigest"`
	SignerIdentities         []string  `json:"signerIdentities"`
	TransparencyBundleDigest string    `json:"transparencyBundleDigest"`
	LogID                    string    `json:"logId"`
	EntryUUID                string    `json:"entryUuid"`
	LogIndex                 int64     `json:"logIndex"`
	TreeSize                 uint64    `json:"treeSize"`
	RootHash                 string    `json:"rootHash"`
	IntegratedAt             time.Time `json:"integratedAt"`
	CheckpointSignedAt       time.Time `json:"checkpointSignedAt"`
}

type ArtifactAuthorityReceiptView struct {
	ID                    string                    `json:"id"`
	SchemaVersion         string                    `json:"schemaVersion"`
	Decision              string                    `json:"decision"`
	SubjectHash           string                    `json:"subjectHash"`
	SourceTreeHash        string                    `json:"sourceTreeHash"`
	ArtifactDigest        string                    `json:"artifactDigest"`
	SBOMDigest            string                    `json:"sbomDigest"`
	SignatureBundleDigest string                    `json:"signatureBundleDigest"`
	PolicyHash            string                    `json:"policyHash"`
	ContentHash           string                    `json:"contentHash"`
	Authority             ArtifactAuthorityIdentity `json:"authority"`
	VerifierImageDigest   string                    `json:"verifierImageDigest"`
	TrustRootDigest       string                    `json:"trustRootDigest"`
	TransparencyLog       ArtifactTransparencyLog   `json:"transparencyLog"`
	VerificationReference string                    `json:"verificationReference"`
	ArtifactDescriptor    ArtifactDescriptor        `json:"artifactDescriptor"`
	SBOMDescriptor        ArtifactSBOMDescriptor    `json:"sbomDescriptor"`
	Proof                 ArtifactAuthorityProof    `json:"proof"`
	Evidence              []GateEvidence            `json:"evidence"`
	Signature             SignatureEnvelope         `json:"signature"`
	VerifiedAt            time.Time                 `json:"verifiedAt"`
	RecordedBy            string                    `json:"recordedBy"`
	CreatedAt             time.Time                 `json:"createdAt"`
}

type artifactAuthorityReceiptDocument = ArtifactAuthorityReceiptView

// ArtifactAuthorityReceipt is an immutable, canonical passed decision. Its
// content hash commits to the complete verified evidence, not merely database
// projection columns or caller-supplied reference strings.
type ArtifactAuthorityReceipt struct {
	document artifactAuthorityReceiptDocument
}

type NewArtifactAuthorityReceiptInput struct {
	ID                    string
	SubjectHash           string
	SourceTreeHash        string
	ArtifactDigest        string
	SBOMDigest            string
	SignatureBundleDigest string
	PolicyHash            string
	Authority             ArtifactAuthorityIdentity
	VerifierImageDigest   string
	TrustRootDigest       string
	TransparencyLog       ArtifactTransparencyLog
	VerificationReference string
	ArtifactDescriptor    ArtifactDescriptor
	SBOMDescriptor        ArtifactSBOMDescriptor
	Proof                 ArtifactAuthorityProof
	Evidence              []GateEvidence
	Signature             SignatureEnvelope
	VerifiedAt            time.Time
	RecordedBy            string
	CreatedAt             time.Time
}

// ArtifactAuthority is the only evidence-producing dependency accepted by the
// Template Registry writer. Implementations must fetch and verify source/OCI,
// SBOM, DSSE, and transparency evidence under server-owned trust policy.
type ArtifactAuthority interface {
	Verify(context.Context, ArtifactAuthorityVerifyRequest) (ArtifactAuthorityReceipt, error)
	Readiness(context.Context) error
}

type ArtifactAuthorityVerifyRequest struct {
	Candidate   AdmissionCandidate      `json:"candidate"`
	SubjectHash string                  `json:"subjectHash"`
	Bundle      ArtifactAdmissionBundle `json:"bundle"`
	RecordedBy  string                  `json:"recordedBy"`
}

func NewArtifactAuthorityReceipt(input NewArtifactAuthorityReceiptInput) (ArtifactAuthorityReceipt, error) {
	document := artifactAuthorityReceiptDocument{
		ID: strings.TrimSpace(input.ID), SchemaVersion: ArtifactAuthorityReceiptSchemaVersion,
		Decision: ArtifactAuthorityDecisionPassed, SubjectHash: strings.TrimSpace(input.SubjectHash),
		SourceTreeHash: strings.TrimSpace(input.SourceTreeHash), ArtifactDigest: strings.TrimSpace(input.ArtifactDigest),
		SBOMDigest: strings.TrimSpace(input.SBOMDigest), SignatureBundleDigest: strings.TrimSpace(input.SignatureBundleDigest),
		PolicyHash: strings.TrimSpace(input.PolicyHash), Authority: ArtifactAuthorityIdentity{
			ID: strings.TrimSpace(input.Authority.ID), Version: strings.TrimSpace(input.Authority.Version),
		},
		VerifierImageDigest: strings.TrimSpace(input.VerifierImageDigest), TrustRootDigest: strings.TrimSpace(input.TrustRootDigest),
		TransparencyLog: ArtifactTransparencyLog{
			ID: strings.TrimSpace(input.TransparencyLog.ID), EntryUUID: strings.TrimSpace(input.TransparencyLog.EntryUUID),
			LogIndex: input.TransparencyLog.LogIndex, IntegratedAt: input.TransparencyLog.IntegratedAt.UTC(),
		},
		VerificationReference: strings.TrimSpace(input.VerificationReference),
		ArtifactDescriptor:    input.ArtifactDescriptor, SBOMDescriptor: input.SBOMDescriptor, Proof: input.Proof,
		Evidence: append([]GateEvidence(nil), input.Evidence...), Signature: input.Signature,
		VerifiedAt: input.VerifiedAt.UTC(), RecordedBy: strings.TrimSpace(input.RecordedBy), CreatedAt: input.CreatedAt.UTC(),
	}
	if err := normalizeAndValidateAuthorityReceipt(&document, false); err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	hash, err := artifactAuthorityReceiptContentHash(document)
	if err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	document.ContentHash = hash
	if err := validateArtifactAuthorityReceiptDocument(document); err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	return ArtifactAuthorityReceipt{document: document}, nil
}

func ParseArtifactAuthorityReceipt(encoded []byte) (ArtifactAuthorityReceipt, error) {
	var document artifactAuthorityReceiptDocument
	if err := decodeStrictJSON(encoded, &document); err != nil {
		return ArtifactAuthorityReceipt{}, invalid("invalid_authority_receipt_json", "authorityReceipt", err.Error())
	}
	if err := validateArtifactAuthorityReceiptDocument(document); err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	return ArtifactAuthorityReceipt{document: cloneAuthorityReceiptDocument(document)}, nil
}

func (r ArtifactAuthorityReceipt) Snapshot() ArtifactAuthorityReceiptView {
	return cloneAuthorityReceiptDocument(r.document)
}

func (r ArtifactAuthorityReceipt) Ref() ArtifactAuthorityReceiptRef {
	return ArtifactAuthorityReceiptRef{ID: r.document.ID, ContentHash: r.document.ContentHash, PolicyHash: r.document.PolicyHash}
}

func (r ArtifactAuthorityReceipt) CanonicalJSON() ([]byte, error) {
	return domain.CanonicalJSON(r.document)
}

func (r *ArtifactAuthorityReceipt) UnmarshalJSON([]byte) error { return ErrImmutableRelease }

type artifactAuthorityReceiptHashPayload struct {
	ID                    string                    `json:"id"`
	SchemaVersion         string                    `json:"schemaVersion"`
	Decision              string                    `json:"decision"`
	SubjectHash           string                    `json:"subjectHash"`
	SourceTreeHash        string                    `json:"sourceTreeHash"`
	ArtifactDigest        string                    `json:"artifactDigest"`
	SBOMDigest            string                    `json:"sbomDigest"`
	SignatureBundleDigest string                    `json:"signatureBundleDigest"`
	PolicyHash            string                    `json:"policyHash"`
	Authority             ArtifactAuthorityIdentity `json:"authority"`
	VerifierImageDigest   string                    `json:"verifierImageDigest"`
	TrustRootDigest       string                    `json:"trustRootDigest"`
	TransparencyLog       ArtifactTransparencyLog   `json:"transparencyLog"`
	VerificationReference string                    `json:"verificationReference"`
	ArtifactDescriptor    ArtifactDescriptor        `json:"artifactDescriptor"`
	SBOMDescriptor        ArtifactSBOMDescriptor    `json:"sbomDescriptor"`
	Proof                 ArtifactAuthorityProof    `json:"proof"`
	Evidence              []GateEvidence            `json:"evidence"`
	Signature             SignatureEnvelope         `json:"signature"`
	VerifiedAt            time.Time                 `json:"verifiedAt"`
	RecordedBy            string                    `json:"recordedBy"`
	CreatedAt             time.Time                 `json:"createdAt"`
}

func artifactAuthorityReceiptContentHash(document artifactAuthorityReceiptDocument) (string, error) {
	return canonicalHash(artifactAuthorityReceiptHashPayload{
		ID: document.ID, SchemaVersion: document.SchemaVersion, Decision: document.Decision,
		SubjectHash: document.SubjectHash, SourceTreeHash: document.SourceTreeHash,
		ArtifactDigest: document.ArtifactDigest, SBOMDigest: document.SBOMDigest,
		SignatureBundleDigest: document.SignatureBundleDigest, PolicyHash: document.PolicyHash,
		Authority: document.Authority, VerifierImageDigest: document.VerifierImageDigest,
		TrustRootDigest: document.TrustRootDigest, TransparencyLog: document.TransparencyLog,
		VerificationReference: document.VerificationReference, ArtifactDescriptor: document.ArtifactDescriptor,
		SBOMDescriptor: document.SBOMDescriptor, Proof: document.Proof, Evidence: document.Evidence,
		Signature: document.Signature, VerifiedAt: document.VerifiedAt,
		RecordedBy: document.RecordedBy, CreatedAt: document.CreatedAt,
	})
}

func normalizeAndValidateAuthorityReceipt(document *artifactAuthorityReceiptDocument, requireContentHash bool) error {
	if document.SchemaVersion != ArtifactAuthorityReceiptSchemaVersion {
		return &Error{Kind: ErrUnsupportedSchema, Code: "unsupported_authority_receipt_schema", Field: "schemaVersion", Detail: "must be template-artifact-authority-receipt/v1"}
	}
	if document.Decision != ArtifactAuthorityDecisionPassed {
		return invalid("invalid_authority_decision", "decision", "only a passed authority decision can be persisted")
	}
	if err := validateUUID(document.ID, "id"); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"subjectHash": document.SubjectHash, "sourceTreeHash": document.SourceTreeHash,
		"artifactDigest": document.ArtifactDigest, "sbomDigest": document.SBOMDigest,
		"signatureBundleDigest": document.SignatureBundleDigest, "policyHash": document.PolicyHash,
		"verifierImageDigest": document.VerifierImageDigest, "trustRootDigest": document.TrustRootDigest,
	} {
		if err := validateDigest(value, field); err != nil {
			return err
		}
	}
	if requireContentHash {
		if err := validateDigest(document.ContentHash, "contentHash"); err != nil {
			return err
		}
	}
	if !validAuthorityText(document.Authority.ID, 240) || !validAuthorityText(document.Authority.Version, 120) {
		return invalid("invalid_authority_identity", "authority", "id and version must be bounded non-empty values")
	}
	if !validAuthorityText(document.TransparencyLog.ID, 240) || !authorityEntryIDPattern.MatchString(document.TransparencyLog.EntryUUID) || document.TransparencyLog.LogIndex < 0 {
		return invalid("invalid_transparency_identity", "transparencyLog", "log, entry, and non-negative index are required")
	}
	if !validEvidenceReference(document.VerificationReference) {
		return invalid("invalid_verification_reference", "verificationReference", "must be a durable credential-free reference")
	}
	if err := validateUUID(document.RecordedBy, "recordedBy"); err != nil {
		return err
	}
	if document.VerifiedAt.IsZero() || document.CreatedAt.IsZero() || document.TransparencyLog.IntegratedAt.IsZero() ||
		document.CreatedAt.Before(document.VerifiedAt) || document.VerifiedAt.Before(document.TransparencyLog.IntegratedAt) {
		return invalid("invalid_authority_time", "authorityReceipt", "integratedAt <= verifiedAt <= createdAt is required")
	}
	if err := normalizeAndValidateAuthorityDescriptors(document); err != nil {
		return err
	}
	normalizedEvidence, findings := normalizeEvidence(document.Evidence, document.SubjectHash, document.VerifiedAt)
	if len(findings) != 0 || len(normalizedEvidence) != len(requiredAdmissionGates) {
		return &Error{Kind: ErrAdmissionRejected, Code: "authority_evidence_invalid", Field: "evidence", Detail: "receipt must contain one canonical passed record for every required gate"}
	}
	normalizedSignature, signatureFindings := normalizeSignature(document.Signature, document.SubjectHash, document.VerifiedAt)
	if len(signatureFindings) != 0 || normalizedSignature.BundleDigest != document.SignatureBundleDigest {
		return &Error{Kind: ErrAdmissionRejected, Code: "authority_signature_invalid", Field: "signature", Detail: "receipt signature must be canonical and match signatureBundleDigest"}
	}
	document.Evidence = normalizedEvidence
	document.Signature = normalizedSignature
	if strings.Join(document.Proof.SignerIdentities, ",") != document.Signature.Signer {
		return &Error{Kind: ErrAdmissionRejected, Code: "authority_signer_mismatch", Field: "proof.signerIdentities", Detail: "trusted signer identities must exactly match the derived signature identity"}
	}
	return nil
}

func normalizeAndValidateAuthorityDescriptors(document *artifactAuthorityReceiptDocument) error {
	artifact := &document.ArtifactDescriptor
	artifact.Reference = strings.TrimSpace(artifact.Reference)
	artifact.MediaType = strings.TrimSpace(artifact.MediaType)
	artifact.Digest = strings.TrimSpace(artifact.Digest)
	if artifact.Digest != document.ArtifactDigest || !validExactOCIReference(artifact.Reference, artifact.Digest) ||
		!validAuthorityText(artifact.MediaType, 240) || artifact.SizeBytes <= 0 ||
		len(artifact.Layers) == 0 || len(artifact.Layers) > 128 {
		return invalid("invalid_artifact_descriptor", "artifactDescriptor", "exact OCI reference, manifest identity, and ordered layers are required")
	}
	if err := normalizeArtifactBlob(&artifact.Config, "artifactDescriptor.config"); err != nil {
		return err
	}
	if artifact.Config.SizeBytes > math.MaxInt64-artifact.SizeBytes {
		return invalid("artifact_size_overflow", "artifactDescriptor.totalBytes", "descriptor sizes overflow int64")
	}
	total := artifact.SizeBytes + artifact.Config.SizeBytes
	for index := range artifact.Layers {
		if err := normalizeArtifactBlob(&artifact.Layers[index], "artifactDescriptor.layers"); err != nil {
			return err
		}
		if artifact.Layers[index].SizeBytes > math.MaxInt64-total {
			return invalid("artifact_size_overflow", "artifactDescriptor.totalBytes", "descriptor sizes overflow int64")
		}
		total += artifact.Layers[index].SizeBytes
	}
	if artifact.TotalBytes != total {
		return invalid("artifact_total_mismatch", "artifactDescriptor.totalBytes", "must equal manifest, config, and ordered layer bytes")
	}

	sbom := &document.SBOMDescriptor
	sbom.SchemaVersion = strings.TrimSpace(sbom.SchemaVersion)
	sbom.Digest = strings.TrimSpace(sbom.Digest)
	if sbom.SchemaVersion != "worksflow.template-sbom-aggregate/v1" || sbom.Digest != document.SBOMDigest ||
		sbom.ServiceCount != len(sbom.Services) || len(sbom.Services) == 0 || len(sbom.Services) > 16 {
		return invalid("invalid_sbom_descriptor", "sbomDescriptor", "canonical non-empty aggregate must match the receipt SBOM digest")
	}
	for index := range sbom.Services {
		service := &sbom.Services[index]
		service.ServiceID = strings.TrimSpace(service.ServiceID)
		service.ImageReference = strings.TrimSpace(service.ImageReference)
		service.ImageDigest = strings.TrimSpace(service.ImageDigest)
		service.ReferrerReference = strings.TrimSpace(service.ReferrerReference)
		service.ReferrerDigest = strings.TrimSpace(service.ReferrerDigest)
		service.StatementDigest = strings.TrimSpace(service.StatementDigest)
		service.PredicateDigest = strings.TrimSpace(service.PredicateDigest)
		service.SPDXVersion = strings.TrimSpace(service.SPDXVersion)
		service.DocumentNamespace = strings.TrimSpace(service.DocumentNamespace)
		service.EvidenceHash = strings.TrimSpace(service.EvidenceHash)
		if !slugPattern.MatchString(service.ServiceID) || len(service.ServiceID) > 128 ||
			!validExactOCIReference(service.ImageReference, service.ImageDigest) ||
			!validExactOCIReference(service.ReferrerReference, service.ReferrerDigest) ||
			service.SPDXVersion != "SPDX-2.3" || !validAuthorityText(service.DocumentNamespace, 2048) {
			return invalid("invalid_sbom_service", "sbomDescriptor.services", "service identity, exact OCI references, and SPDX 2.3 metadata are required")
		}
		for _, digest := range []string{service.ImageDigest, service.ReferrerDigest, service.StatementDigest, service.PredicateDigest, service.EvidenceHash} {
			if err := validateDigest(digest, "sbomDescriptor.services.digest"); err != nil {
				return err
			}
		}
	}
	sort.Slice(sbom.Services, func(left, right int) bool { return sbom.Services[left].ServiceID < sbom.Services[right].ServiceID })
	for index := 1; index < len(sbom.Services); index++ {
		if sbom.Services[index-1].ServiceID == sbom.Services[index].ServiceID {
			return invalid("duplicate_sbom_service", "sbomDescriptor.services", "service IDs must be unique")
		}
	}

	proof := &document.Proof
	proof.PayloadType = strings.TrimSpace(proof.PayloadType)
	proof.PredicateType = strings.TrimSpace(proof.PredicateType)
	proof.PayloadDigest = strings.TrimSpace(proof.PayloadDigest)
	proof.SignatureBundleDigest = strings.TrimSpace(proof.SignatureBundleDigest)
	proof.TransparencyBundleDigest = strings.TrimSpace(proof.TransparencyBundleDigest)
	proof.LogID = strings.TrimSpace(proof.LogID)
	proof.EntryUUID = strings.TrimSpace(proof.EntryUUID)
	proof.RootHash = strings.TrimSpace(proof.RootHash)
	proof.IntegratedAt = proof.IntegratedAt.UTC()
	proof.CheckpointSignedAt = proof.CheckpointSignedAt.UTC()
	for index := range proof.SignerIdentities {
		proof.SignerIdentities[index] = strings.TrimSpace(proof.SignerIdentities[index])
		if !validAuthorityText(proof.SignerIdentities[index], 500) {
			return invalid("invalid_proof_signer", "proof.signerIdentities", "trusted signer identities must be bounded non-empty values")
		}
	}
	sort.Strings(proof.SignerIdentities)
	if len(proof.SignerIdentities) == 0 || !uniqueStrings(proof.SignerIdentities) ||
		!validAuthorityText(proof.PayloadType, 500) || !validAuthorityText(proof.PredicateType, 500) ||
		proof.SignatureBundleDigest != document.SignatureBundleDigest || proof.LogID != document.TransparencyLog.ID ||
		proof.EntryUUID != document.TransparencyLog.EntryUUID || proof.LogIndex != document.TransparencyLog.LogIndex ||
		!proof.IntegratedAt.Equal(document.TransparencyLog.IntegratedAt) || proof.TreeSize == 0 || proof.TreeSize > math.MaxInt64 ||
		proof.LogIndex < 0 || uint64(proof.LogIndex) >= proof.TreeSize || proof.CheckpointSignedAt.Before(proof.IntegratedAt) ||
		proof.CheckpointSignedAt.After(document.VerifiedAt) {
		return invalid("invalid_authority_proof", "proof", "DSSE and transparency proof must bind the exact receipt coordinates and verification window")
	}
	for field, digest := range map[string]string{
		"proof.payloadDigest": proof.PayloadDigest, "proof.signatureBundleDigest": proof.SignatureBundleDigest,
		"proof.transparencyBundleDigest": proof.TransparencyBundleDigest, "proof.rootHash": proof.RootHash,
	} {
		if err := validateDigest(digest, field); err != nil {
			return err
		}
	}
	return nil
}

func normalizeArtifactBlob(descriptor *ArtifactBlobDescriptor, field string) error {
	descriptor.MediaType = strings.TrimSpace(descriptor.MediaType)
	descriptor.Digest = strings.TrimSpace(descriptor.Digest)
	if !validAuthorityText(descriptor.MediaType, 240) || descriptor.SizeBytes <= 0 {
		return invalid("invalid_artifact_blob", field, "media type, digest, and positive size are required")
	}
	return validateDigest(descriptor.Digest, field+".digest")
}

func validExactOCIReference(reference, digest string) bool {
	if reference != strings.TrimSpace(reference) || reference == "" || containsControl(reference) ||
		strings.Contains(reference, "://") || strings.Count(reference, "@") != 1 ||
		!strings.HasSuffix(reference, "@"+digest) {
		return false
	}
	name := strings.TrimSuffix(reference, "@"+digest)
	return strings.Contains(name, "/") && !strings.HasPrefix(name, "/") && !strings.HasSuffix(name, "/")
}

func uniqueStrings(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] == values[index] {
			return false
		}
	}
	return true
}

func validateArtifactAuthorityReceiptDocument(document artifactAuthorityReceiptDocument) error {
	original := cloneAuthorityReceiptDocument(document)
	if err := normalizeAndValidateAuthorityReceipt(&document, true); err != nil {
		return err
	}
	if !reflect.DeepEqual(original, document) {
		return invalid("noncanonical_authority_receipt", "authorityReceipt", "must use normalized values, ordered evidence, and UTC timestamps")
	}
	expected, err := artifactAuthorityReceiptContentHash(document)
	if err != nil {
		return err
	}
	if document.ContentHash != expected {
		return invalid("authority_receipt_content_mismatch", "contentHash", "does not commit to the exact immutable authority receipt")
	}
	return nil
}

func validAuthorityText(value string, limit int) bool {
	return value == strings.TrimSpace(value) && value != "" && len(value) <= limit && !containsControl(value)
}

func validateArtifactAuthorityReceiptRef(ref ArtifactAuthorityReceiptRef) error {
	if err := validateUUID(ref.ID, "authorityReceipt.id"); err != nil {
		return err
	}
	if err := validateDigest(ref.ContentHash, "authorityReceipt.contentHash"); err != nil {
		return err
	}
	if err := validateDigest(ref.PolicyHash, "authorityReceipt.policyHash"); err != nil {
		return err
	}
	return nil
}

func sameArtifactAuthorityReceiptRef(left, right *ArtifactAuthorityReceiptRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ID == right.ID && left.ContentHash == right.ContentHash && left.PolicyHash == right.PolicyHash
}

func authorityBoundSelectableRegistration(registration TemplateReleaseRegistration) bool {
	return registration.Policy.State == ReleaseApproved && authorityBoundRegistration(registration)
}

func authorityBoundRegistration(registration TemplateReleaseRegistration) bool {
	release := registration.Release.Snapshot()
	policy := registration.Policy
	if release.SchemaVersion != TemplateReleaseSchemaVersionV2 || policy.SchemaVersion != ReleasePolicySchemaVersionV2 ||
		policy.ReleaseContentHash != release.ContentHash ||
		!sameArtifactAuthorityReceiptRef(release.AuthorityReceipt, policy.AuthorityReceipt) ||
		release.AuthorityReceipt == nil || registration.AuthorityReceipt == nil {
		return false
	}
	receipt := registration.AuthorityReceipt.Snapshot()
	return receipt.Decision == ArtifactAuthorityDecisionPassed &&
		receipt.ID == release.AuthorityReceipt.ID && receipt.ContentHash == release.AuthorityReceipt.ContentHash &&
		receipt.PolicyHash == release.AuthorityReceipt.PolicyHash && receipt.SubjectHash == release.SubjectHash &&
		receipt.SourceTreeHash == release.Source.TreeHash && receipt.SBOMDigest == release.SBOMDigest &&
		receipt.SignatureBundleDigest == release.Signature.BundleDigest
}

func cloneAuthorityReceiptDocument(input artifactAuthorityReceiptDocument) artifactAuthorityReceiptDocument {
	var output artifactAuthorityReceiptDocument
	cloneViaJSON(input, &output)
	return output
}
