package templates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/templateauthority"
)

const ArtifactAdmissionAttestationSchemaVersion = "template-artifact-admission-attestation/v1"

// ArtifactSourceVerifier must independently fetch the exact Git commit and
// recompute the platform tree digest. Merely comparing request strings does
// not satisfy this contract.
type ArtifactSourceVerifier interface {
	VerifySource(context.Context, TemplateSource) error
	Readiness(context.Context) error
}

type VerifiedArtifactAuthorityConfig struct {
	AuthorityID         string
	AuthorityVersion    string
	VerifierImageDigest string
	PolicyHash          string
	TrustRootDigest     string
	PredicateType       string
	Source              ArtifactSourceVerifier
	OCI                 *templateauthority.OCIVerifier
	SBOM                *templateauthority.SBOMVerifier
	DSSE                *templateauthority.DSSEVerifier
	Transparency        *templateauthority.TransparencyVerifier
	DependencyReadiness func(context.Context) error
	Clock               func() time.Time
	NewReceiptID        func() string
}

// VerifiedArtifactAuthority composes independently verified Git, OCI, SPDX,
// DSSE, and transparency results into one immutable receipt. Every trust and
// policy value is copied from server configuration, not the Admit request.
type VerifiedArtifactAuthority struct {
	authorityID         string
	authorityVersion    string
	verifierImageDigest string
	policyHash          string
	trustRootDigest     string
	predicateType       string
	source              ArtifactSourceVerifier
	oci                 *templateauthority.OCIVerifier
	sbom                *templateauthority.SBOMVerifier
	dsse                *templateauthority.DSSEVerifier
	transparency        *templateauthority.TransparencyVerifier
	dependencyReadiness func(context.Context) error
	clock               func() time.Time
	newReceiptID        func() string
}

func NewVerifiedArtifactAuthority(config VerifiedArtifactAuthorityConfig) (*VerifiedArtifactAuthority, error) {
	config.AuthorityID = strings.TrimSpace(config.AuthorityID)
	config.AuthorityVersion = strings.TrimSpace(config.AuthorityVersion)
	config.VerifierImageDigest = strings.TrimSpace(config.VerifierImageDigest)
	config.PolicyHash = strings.TrimSpace(config.PolicyHash)
	config.TrustRootDigest = strings.TrimSpace(config.TrustRootDigest)
	config.PredicateType = strings.TrimSpace(config.PredicateType)
	if !validAuthorityText(config.AuthorityID, 240) || !validAuthorityText(config.AuthorityVersion, 120) ||
		!validAuthorityText(config.PredicateType, 500) {
		return nil, invalid("invalid_authority_configuration", "artifactAuthority", "authority identity and predicate type are required")
	}
	for field, digest := range map[string]string{
		"verifierImageDigest": config.VerifierImageDigest,
		"policyHash":          config.PolicyHash,
		"trustRootDigest":     config.TrustRootDigest,
	} {
		if err := validateDigest(digest, field); err != nil {
			return nil, err
		}
	}
	if config.Source == nil || config.OCI == nil || config.SBOM == nil || config.DSSE == nil || config.Transparency == nil {
		return nil, invalid("invalid_authority_configuration", "artifactAuthority", "source, OCI, SBOM, DSSE, and transparency verifiers are required")
	}
	if config.DependencyReadiness == nil {
		return nil, invalid("invalid_authority_configuration", "dependencyReadiness", "registry and trust dependency readiness is required")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.NewReceiptID == nil {
		config.NewReceiptID = uuid.NewString
	}
	return &VerifiedArtifactAuthority{
		authorityID: config.AuthorityID, authorityVersion: config.AuthorityVersion,
		verifierImageDigest: config.VerifierImageDigest, policyHash: config.PolicyHash,
		trustRootDigest: config.TrustRootDigest, predicateType: config.PredicateType,
		source: config.Source, oci: config.OCI, sbom: config.SBOM, dsse: config.DSSE,
		transparency: config.Transparency, dependencyReadiness: config.DependencyReadiness,
		clock: config.Clock, newReceiptID: config.NewReceiptID,
	}, nil
}

func (authority *VerifiedArtifactAuthority) Readiness(ctx context.Context) error {
	if authority == nil || authority.source == nil || authority.oci == nil || authority.sbom == nil ||
		authority.dsse == nil || authority.transparency == nil || authority.dependencyReadiness == nil ||
		authority.clock == nil || authority.newReceiptID == nil {
		return errors.New("Template Artifact Authority is not configured")
	}
	if ctx == nil {
		return errors.New("Template Artifact Authority readiness context is required")
	}
	if err := authority.source.Readiness(ctx); err != nil {
		return fmt.Errorf("Template Artifact Authority Git source readiness: %w", err)
	}
	if err := authority.dependencyReadiness(ctx); err != nil {
		return fmt.Errorf("Template Artifact Authority dependency readiness: %w", err)
	}
	if now := authority.clock().UTC(); now.IsZero() {
		return errors.New("Template Artifact Authority clock returned zero")
	}
	return nil
}

func (authority *VerifiedArtifactAuthority) Verify(
	ctx context.Context,
	request ArtifactAuthorityVerifyRequest,
) (ArtifactAuthorityReceipt, error) {
	if err := authority.Readiness(ctx); err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	if err := validateUUID(request.RecordedBy, "recordedBy"); err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	candidate, err := normalizeCandidate(request.Candidate)
	if err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	if !reflect.DeepEqual(candidate, request.Candidate) {
		return ArtifactAuthorityReceipt{}, invalid("noncanonical_candidate", "candidate", "Artifact Authority requires the exact normalized candidate")
	}
	expectedSubject, err := subjectHash(candidate)
	if err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	if request.SubjectHash != expectedSubject {
		return ArtifactAuthorityReceipt{}, invalid("authority_subject_mismatch", "subjectHash", "does not bind the exact normalized candidate")
	}
	if err := authority.source.VerifySource(ctx, candidate.Source); err != nil {
		return ArtifactAuthorityReceipt{}, fmt.Errorf("verify exact Template source: %w", err)
	}

	artifact, err := authority.oci.VerifyImage(ctx, request.Bundle.ArtifactReference)
	if err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	serviceRequests, err := exactSBOMRequests(candidate, request.Bundle.ServiceSBOMs)
	if err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	sbom, err := authority.sbom.VerifyAggregate(ctx, serviceRequests)
	if err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	if sbom.Digest != candidate.SBOMDigest {
		return ArtifactAuthorityReceipt{}, invalid("sbom_digest_mismatch", "candidate.sbomDigest", "does not equal the byte-verified service SBOM aggregate")
	}

	verifiedDSSE, err := authority.dsse.Verify(request.Bundle.DSSEEnvelope, templateauthority.ExpectedSubject{
		Name: artifact.Reference.String(), SHA256Digest: artifact.Reference.Digest,
	})
	if err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	transparency, err := authority.transparency.Verify(
		request.Bundle.TransparencyBundle,
		templateauthority.TransparencyExpectation{Leaf: verifiedDSSE.CanonicalEnvelope},
	)
	if err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	verifiedAt := authority.clock().UTC()
	if verifiedAt.IsZero() || verifiedAt.Before(transparency.CheckpointSignedAt) {
		return ArtifactAuthorityReceipt{}, invalid("invalid_authority_time", "verifiedAt", "trusted verification time must not predate the transparency checkpoint")
	}
	predicate, err := authority.verifyAttestationPredicate(
		verifiedDSSE.Payload, candidate, expectedSubject, artifact.Reference.Digest,
		sbom.Digest, request.Bundle.VerificationReference, verifiedAt,
	)
	if err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	receiptID := strings.TrimSpace(authority.newReceiptID())
	if err := validateUUID(receiptID, "receiptId"); err != nil {
		return ArtifactAuthorityReceipt{}, err
	}

	signature := SignatureEnvelope{
		Format: "dsse", SubjectHash: expectedSubject, BundleDigest: verifiedDSSE.BundleDigest,
		Signer:             strings.Join(verifiedDSSE.SignerIdentities, ","),
		TransparencyLogRef: "urn:worksflow:transparency:" + transparency.Entry.LogID + ":" + transparency.Entry.LeafHash,
		SignedAt:           transparency.IntegratedAt,
	}
	return NewArtifactAuthorityReceipt(NewArtifactAuthorityReceiptInput{
		ID: receiptID, SubjectHash: expectedSubject, SourceTreeHash: candidate.Source.TreeHash,
		ArtifactDigest: artifact.Reference.Digest, SBOMDigest: sbom.Digest,
		SignatureBundleDigest: verifiedDSSE.BundleDigest, PolicyHash: authority.policyHash,
		Authority:           ArtifactAuthorityIdentity{ID: authority.authorityID, Version: authority.authorityVersion},
		VerifierImageDigest: authority.verifierImageDigest, TrustRootDigest: authority.trustRootDigest,
		TransparencyLog: ArtifactTransparencyLog{
			ID: transparency.Entry.LogID, EntryUUID: transparency.Entry.LeafHash,
			LogIndex: int64(transparency.Entry.LeafIndex), IntegratedAt: transparency.IntegratedAt,
		},
		VerificationReference: strings.TrimSpace(request.Bundle.VerificationReference),
		ArtifactDescriptor:    artifactReceiptDescriptor(artifact), SBOMDescriptor: sbomReceiptDescriptor(sbom),
		Proof: ArtifactAuthorityProof{
			PayloadType: verifiedDSSE.PayloadType, PredicateType: verifiedDSSE.PredicateType,
			PayloadDigest: verifiedDSSE.PayloadDigest, SignatureBundleDigest: verifiedDSSE.BundleDigest,
			SignerIdentities:         append([]string(nil), verifiedDSSE.SignerIdentities...),
			TransparencyBundleDigest: transparency.BundleDigest, LogID: transparency.Entry.LogID,
			EntryUUID: transparency.Entry.LeafHash, LogIndex: int64(transparency.Entry.LeafIndex),
			TreeSize: transparency.Entry.TreeSize, RootHash: transparency.Entry.RootHash,
			IntegratedAt: transparency.IntegratedAt, CheckpointSignedAt: transparency.CheckpointSignedAt,
		},
		Evidence: predicate.Evidence, Signature: signature, VerifiedAt: verifiedAt,
		RecordedBy: strings.TrimSpace(request.RecordedBy), CreatedAt: verifiedAt,
	})
}

type artifactAdmissionStatement struct {
	Type          string                              `json:"_type"`
	Subject       []artifactAdmissionStatementSubject `json:"subject"`
	PredicateType string                              `json:"predicateType"`
	Predicate     json.RawMessage                     `json:"predicate"`
}

type artifactAdmissionStatementSubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type artifactAdmissionPredicate struct {
	SchemaVersion         string         `json:"schemaVersion"`
	SubjectHash           string         `json:"subjectHash"`
	SourceTreeHash        string         `json:"sourceTreeHash"`
	ArtifactDigest        string         `json:"artifactDigest"`
	SBOMDigest            string         `json:"sbomDigest"`
	LicenseDigest         string         `json:"licenseDigest"`
	PolicyHash            string         `json:"policyHash"`
	VerificationReference string         `json:"verificationReference"`
	Evidence              []GateEvidence `json:"evidence"`
}

func (authority *VerifiedArtifactAuthority) verifyAttestationPredicate(
	payload []byte,
	candidate AdmissionCandidate,
	subjectHash, artifactDigest, sbomDigest, verificationReference string,
	verifiedAt time.Time,
) (artifactAdmissionPredicate, error) {
	var statement artifactAdmissionStatement
	if err := decodeStrictJSON(payload, &statement); err != nil {
		return artifactAdmissionPredicate{}, invalid("invalid_authority_statement", "dsse.payload", err.Error())
	}
	if statement.Type != templateauthority.InTotoStatementV1 || statement.PredicateType != authority.predicateType || len(statement.Subject) != 1 {
		return artifactAdmissionPredicate{}, invalid("invalid_authority_statement", "dsse.payload", "must use the configured in-toto predicate and one exact artifact subject")
	}
	var predicate artifactAdmissionPredicate
	if err := decodeStrictJSON(statement.Predicate, &predicate); err != nil {
		return artifactAdmissionPredicate{}, invalid("invalid_authority_predicate", "dsse.predicate", err.Error())
	}
	predicate.SchemaVersion = strings.TrimSpace(predicate.SchemaVersion)
	predicate.SubjectHash = strings.TrimSpace(predicate.SubjectHash)
	predicate.SourceTreeHash = strings.TrimSpace(predicate.SourceTreeHash)
	predicate.ArtifactDigest = strings.TrimSpace(predicate.ArtifactDigest)
	predicate.SBOMDigest = strings.TrimSpace(predicate.SBOMDigest)
	predicate.LicenseDigest = strings.TrimSpace(predicate.LicenseDigest)
	predicate.PolicyHash = strings.TrimSpace(predicate.PolicyHash)
	predicate.VerificationReference = strings.TrimSpace(predicate.VerificationReference)
	if predicate.SchemaVersion != ArtifactAdmissionAttestationSchemaVersion || predicate.SubjectHash != subjectHash ||
		predicate.SourceTreeHash != candidate.Source.TreeHash || predicate.ArtifactDigest != artifactDigest ||
		predicate.SBOMDigest != sbomDigest || predicate.LicenseDigest != candidate.LicenseDigest ||
		predicate.PolicyHash != authority.policyHash || predicate.VerificationReference != strings.TrimSpace(verificationReference) ||
		!validEvidenceReference(predicate.VerificationReference) {
		return artifactAdmissionPredicate{}, invalid("authority_predicate_lineage_mismatch", "dsse.predicate", "signed predicate does not bind the exact candidate, artifact, SBOM, policy, license, and verification reference")
	}
	// DSSE verification already rejected duplicate JSON names. Domain
	// normalization now guarantees one passed, canonical record per gate.
	evidence, findings := normalizeEvidence(predicate.Evidence, subjectHash, verifiedAt)
	if len(findings) != 0 || len(evidence) != len(requiredAdmissionGates) || !reflect.DeepEqual(evidence, predicate.Evidence) {
		return artifactAdmissionPredicate{}, &Error{Kind: ErrAdmissionRejected, Code: "authority_predicate_evidence_invalid", Field: "dsse.predicate.evidence", Detail: "signed evidence must contain one canonical passed record per gate"}
	}
	byGate := make(map[AdmissionGate]GateEvidence, len(evidence))
	for _, item := range evidence {
		byGate[item.Gate] = item
	}
	for gate, digest := range map[AdmissionGate]string{
		GateSourceIdentity: candidate.Source.TreeHash, GateLicenseSPDX: candidate.LicenseDigest,
		GateRegistryPolicy: authority.policyHash, GateContainerBuild: artifactDigest, GateSBOM: sbomDigest,
	} {
		if byGate[gate].Digest != digest {
			return artifactAdmissionPredicate{}, invalid("authority_gate_digest_mismatch", "dsse.predicate.evidence", string(gate)+" does not bind its byte-verified digest")
		}
	}
	predicate.Evidence = evidence
	return predicate, nil
}

func exactSBOMRequests(candidate AdmissionCandidate, references []ArtifactServiceSBOMReference) ([]templateauthority.ServiceSBOMRequest, error) {
	if len(references) != len(candidate.Manifest.Services) {
		return nil, invalid("sbom_service_set_mismatch", "bundle.serviceSboms", "must cover every exact manifest service once")
	}
	expected := make(map[string]bool, len(candidate.Manifest.Services))
	for _, service := range candidate.Manifest.Services {
		expected[service.ID] = true
	}
	result := make([]templateauthority.ServiceSBOMRequest, 0, len(references))
	seen := make(map[string]bool, len(references))
	for _, reference := range references {
		reference.ServiceID = strings.TrimSpace(reference.ServiceID)
		if !expected[reference.ServiceID] || seen[reference.ServiceID] {
			return nil, invalid("sbom_service_set_mismatch", "bundle.serviceSboms", "contains an unknown or duplicate manifest service")
		}
		seen[reference.ServiceID] = true
		result = append(result, templateauthority.ServiceSBOMRequest{
			ServiceID: reference.ServiceID, ImageReference: strings.TrimSpace(reference.ImageReference),
			ReferrerReference: strings.TrimSpace(reference.ReferrerReference),
		})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ServiceID < result[right].ServiceID })
	return result, nil
}

func artifactReceiptDescriptor(image templateauthority.VerifiedImage) ArtifactDescriptor {
	layers := make([]ArtifactBlobDescriptor, 0, len(image.Layers))
	for _, layer := range image.Layers {
		layers = append(layers, artifactBlobDescriptor(layer))
	}
	return ArtifactDescriptor{
		Reference: image.Reference.String(), MediaType: image.Manifest.MediaType,
		Digest: image.Manifest.Digest, SizeBytes: image.Manifest.Size,
		Config: artifactBlobDescriptor(image.Config), Layers: layers, TotalBytes: image.TotalBytes,
	}
}

func artifactBlobDescriptor(descriptor templateauthority.VerifiedDescriptor) ArtifactBlobDescriptor {
	return ArtifactBlobDescriptor{MediaType: descriptor.MediaType, Digest: descriptor.Digest, SizeBytes: descriptor.Size}
}

func sbomReceiptDescriptor(aggregate templateauthority.VerifiedSBOMAggregate) ArtifactSBOMDescriptor {
	services := make([]ArtifactSBOMServiceDescriptor, 0, len(aggregate.Services))
	for _, service := range aggregate.Services {
		services = append(services, ArtifactSBOMServiceDescriptor{
			ServiceID: service.ServiceID, ImageReference: service.ImageReference.String(),
			ImageDigest: service.ImageReference.Digest, ReferrerReference: service.ReferrerReference.String(),
			ReferrerDigest: service.ReferrerReference.Digest, StatementDigest: service.StatementDigest,
			PredicateDigest: service.PredicateDigest, SPDXVersion: service.SPDXVersion,
			DocumentNamespace: service.DocumentNamespace, EvidenceHash: service.CanonicalEvidenceHash,
		})
	}
	return ArtifactSBOMDescriptor{
		SchemaVersion: aggregate.SchemaVersion, Digest: aggregate.Digest,
		ServiceCount: len(services), Services: services,
	}
}

var _ ArtifactAuthority = (*VerifiedArtifactAuthority)(nil)
