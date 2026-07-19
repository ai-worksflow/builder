package templates

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"

	"github.com/worksflow/builder/backend/internal/domain"
)

type releaseHashPayload struct {
	ID                 string                       `json:"id"`
	SchemaVersion      string                       `json:"schemaVersion"`
	AdmissionAttemptID string                       `json:"admissionAttemptId"`
	Source             TemplateSource               `json:"source"`
	Manifest           TemplateManifest             `json:"manifest"`
	SBOMDigest         string                       `json:"sbomDigest"`
	LicenseExpression  string                       `json:"licenseExpression"`
	LicenseDigest      string                       `json:"licenseDigest"`
	EvidenceRefs       []GateEvidence               `json:"evidenceRefs"`
	Signature          SignatureEnvelope            `json:"signature"`
	SubjectHash        string                       `json:"subjectHash"`
	ApprovedBy         string                       `json:"approvedBy"`
	ApprovedAt         json.RawMessage              `json:"approvedAt"`
	AuthorityReceipt   *ArtifactAuthorityReceiptRef `json:"authorityReceipt,omitempty"`
}

func newTemplateRelease(attempt admissionAttemptDocument, signature SignatureEnvelope) (TemplateRelease, error) {
	if attempt.Status != AttemptApproved || attempt.EvaluatedAt == nil || attempt.ApprovedReleaseID == "" || len(attempt.Findings) != 0 {
		return TemplateRelease{}, invalid("admission_not_approved", "admissionAttempt", "only an approved, finding-free attempt can create a release")
	}
	candidate := attempt.Candidate
	schemaVersion := TemplateReleaseSchemaVersion
	var authorityReceipt *ArtifactAuthorityReceiptRef
	switch attempt.SchemaVersion {
	case AdmissionAttemptSchemaVersion:
		if attempt.AuthorityReceipt != nil {
			return TemplateRelease{}, invalid("unexpected_authority_receipt", "authorityReceipt", "legacy admission cannot bind an authority receipt")
		}
	case AdmissionAttemptSchemaVersionV2:
		if attempt.AuthorityReceipt == nil {
			return TemplateRelease{}, invalid("authority_receipt_required", "authorityReceipt", "v2 release requires an exact authority receipt")
		}
		schemaVersion = TemplateReleaseSchemaVersionV2
		value := *attempt.AuthorityReceipt
		authorityReceipt = &value
	default:
		return TemplateRelease{}, &Error{Kind: ErrUnsupportedSchema, Code: "unsupported_admission_schema", Field: "schemaVersion", Detail: "unsupported admission attempt schema"}
	}
	document := templateReleaseDocument{
		ID:                 attempt.ApprovedReleaseID,
		SchemaVersion:      schemaVersion,
		AdmissionAttemptID: attempt.ID,
		Source:             candidate.Source,
		Manifest:           candidate.Manifest,
		SBOMDigest:         candidate.SBOMDigest,
		LicenseExpression:  candidate.LicenseExpression,
		LicenseDigest:      candidate.LicenseDigest,
		EvidenceRefs:       append([]GateEvidence(nil), attempt.Evidence...),
		Signature:          signature,
		SubjectHash:        attempt.SubjectHash,
		ApprovedBy:         attempt.EvaluatedBy,
		ApprovedAt:         attempt.EvaluatedAt.UTC(),
		AuthorityReceipt:   authorityReceipt,
	}
	hash, err := releaseContentHash(document)
	if err != nil {
		return TemplateRelease{}, err
	}
	document.ContentHash = hash
	if err := validateReleaseDocument(document); err != nil {
		return TemplateRelease{}, err
	}
	return TemplateRelease{document: document}, nil
}

func releaseContentHash(document templateReleaseDocument) (string, error) {
	approvedAt, err := json.Marshal(document.ApprovedAt)
	if err != nil {
		return "", invalid("canonicalization_failed", "approvedAt", err.Error())
	}
	return canonicalHash(releaseHashPayload{
		ID:                 document.ID,
		SchemaVersion:      document.SchemaVersion,
		AdmissionAttemptID: document.AdmissionAttemptID,
		Source:             document.Source,
		Manifest:           document.Manifest,
		SBOMDigest:         document.SBOMDigest,
		LicenseExpression:  document.LicenseExpression,
		LicenseDigest:      document.LicenseDigest,
		EvidenceRefs:       document.EvidenceRefs,
		Signature:          document.Signature,
		SubjectHash:        document.SubjectHash,
		ApprovedBy:         document.ApprovedBy,
		ApprovedAt:         approvedAt,
		AuthorityReceipt:   document.AuthorityReceipt,
	})
}

// ParseTemplateRelease is the only hydration path. It validates every source,
// manifest, evidence, signature, subject, and content commitment before
// returning the immutable value.
func ParseTemplateRelease(encoded []byte) (TemplateRelease, error) {
	var document templateReleaseDocument
	if err := decodeStrictJSON(encoded, &document); err != nil {
		return TemplateRelease{}, invalid("invalid_release_json", "release", err.Error())
	}
	if err := validateReleaseDocument(document); err != nil {
		return TemplateRelease{}, err
	}
	return TemplateRelease{document: cloneReleaseDocument(document)}, nil
}

func validateReleaseDocument(document templateReleaseDocument) error {
	switch document.SchemaVersion {
	case TemplateReleaseSchemaVersion:
		if document.AuthorityReceipt != nil {
			return invalid("unexpected_authority_receipt", "authorityReceipt", "template-release/v1 cannot bind an authority receipt")
		}
	case TemplateReleaseSchemaVersionV2:
		if document.AuthorityReceipt == nil {
			return invalid("authority_receipt_required", "authorityReceipt", "template-release/v2 requires an exact authority receipt")
		}
		if err := validateArtifactAuthorityReceiptRef(*document.AuthorityReceipt); err != nil {
			return err
		}
	default:
		return &Error{Kind: ErrUnsupportedSchema, Code: "unsupported_release_schema", Field: "schemaVersion", Detail: "must be template-release/v1 or template-release/v2"}
	}
	if err := validateUUID(document.ID, "id"); err != nil {
		return err
	}
	if err := validateUUID(document.AdmissionAttemptID, "admissionAttemptId"); err != nil {
		return err
	}
	if err := validateUUID(document.ApprovedBy, "approvedBy"); err != nil {
		return err
	}
	if document.ApprovedAt.IsZero() || document.ApprovedAt.Location() != document.ApprovedAt.UTC().Location() {
		return invalid("invalid_time", "approvedAt", "must be a non-zero UTC timestamp")
	}
	candidate := AdmissionCandidate{
		Source: document.Source, Manifest: document.Manifest, SBOMDigest: document.SBOMDigest,
		LicenseExpression: document.LicenseExpression, LicenseDigest: document.LicenseDigest,
	}
	normalizedCandidate, err := normalizeCandidate(candidate)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(candidate, normalizedCandidate) {
		return invalid("noncanonical_release", "release", "source and manifest fields must use their canonical normalized form")
	}
	expectedSubject, err := subjectHash(normalizedCandidate)
	if err != nil {
		return err
	}
	if document.SubjectHash != expectedSubject {
		return invalid("release_subject_mismatch", "subjectHash", "does not commit to the exact normalized candidate")
	}
	evidence, findings := normalizeEvidence(document.EvidenceRefs, document.SubjectHash, document.ApprovedAt)
	if len(findings) != 0 || !reflect.DeepEqual(evidence, document.EvidenceRefs) {
		return &Error{Kind: ErrAdmissionRejected, Code: "release_evidence_invalid", Field: "evidenceRefs", Detail: "all required evidence must be present, passed, exact-subject, and canonical"}
	}
	signature, signatureFindings := normalizeSignature(document.Signature, document.SubjectHash, document.ApprovedAt)
	if len(signatureFindings) != 0 || !reflect.DeepEqual(signature, document.Signature) {
		return &Error{Kind: ErrAdmissionRejected, Code: "release_signature_invalid", Field: "signature", Detail: "signature must be canonical and bind the exact release subject"}
	}
	if err := validateDigest(document.ContentHash, "contentHash"); err != nil {
		return err
	}
	expectedContent, err := releaseContentHash(document)
	if err != nil {
		return err
	}
	if document.ContentHash != expectedContent {
		return invalid("release_content_mismatch", "contentHash", "does not commit to the exact immutable release payload")
	}
	return nil
}

func (r TemplateRelease) CanonicalJSON() ([]byte, error) {
	return domain.CanonicalJSON(r.document)
}

// UnmarshalJSON prevents accidental in-place replacement of a release. Use
// ParseTemplateRelease to create a separately validated value.
func (r *TemplateRelease) UnmarshalJSON([]byte) error {
	return ErrImmutableRelease
}

func decodeStrictJSON(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func releaseContainsServiceKind(release TemplateRelease, kind string) bool {
	for _, service := range release.document.Manifest.Services {
		if strings.EqualFold(service.Kind, kind) {
			return true
		}
	}
	return false
}
