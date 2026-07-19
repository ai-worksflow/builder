package templates

import (
	"sort"
	"strings"
	"time"
)

func NewAdmissionAttempt(id, requestedBy string, candidate AdmissionCandidate, now time.Time) (AdmissionAttempt, error) {
	return newAdmissionAttempt(id, requestedBy, candidate, AdmissionAttemptSchemaVersion, now)
}

// NewAuthorityAdmissionAttempt starts the only admission lineage that may
// become selectable after the Artifact Authority gate is installed.
func NewAuthorityAdmissionAttempt(id, requestedBy string, candidate AdmissionCandidate, now time.Time) (AdmissionAttempt, error) {
	return newAdmissionAttempt(id, requestedBy, candidate, AdmissionAttemptSchemaVersionV2, now)
}

func newAdmissionAttempt(id, requestedBy string, candidate AdmissionCandidate, schemaVersion string, now time.Time) (AdmissionAttempt, error) {
	if err := validateUUID(id, "id"); err != nil {
		return AdmissionAttempt{}, err
	}
	if err := validateUUID(requestedBy, "requestedBy"); err != nil {
		return AdmissionAttempt{}, err
	}
	if now.IsZero() {
		return AdmissionAttempt{}, invalid("invalid_time", "createdAt", "must not be zero")
	}
	normalized, err := normalizeCandidate(candidate)
	if err != nil {
		return AdmissionAttempt{}, err
	}
	hash, err := subjectHash(normalized)
	if err != nil {
		return AdmissionAttempt{}, err
	}
	now = now.UTC()
	return AdmissionAttempt{document: admissionAttemptDocument{
		ID:            strings.TrimSpace(id),
		SchemaVersion: schemaVersion,
		Status:        AttemptCandidate,
		Version:       1,
		Candidate:     normalized,
		SubjectHash:   hash,
		Evidence:      []GateEvidence{},
		Findings:      []AdmissionFinding{},
		RequestedBy:   strings.TrimSpace(requestedBy),
		CreatedAt:     now,
		UpdatedAt:     now,
	}}, nil
}

func (a AdmissionAttempt) BeginValidation(now time.Time) (AdmissionAttempt, error) {
	if a.document.Status != AttemptCandidate {
		return AdmissionAttempt{}, transition("admissionAttempt", a.document.Status, AttemptValidating)
	}
	if now.IsZero() || now.Before(a.document.UpdatedAt) {
		return AdmissionAttempt{}, invalid("invalid_time", "updatedAt", "must not be zero or move backwards")
	}
	next := cloneAttemptDocument(a.document)
	next.Status = AttemptValidating
	next.Version++
	next.UpdatedAt = now.UTC()
	return AdmissionAttempt{document: next}, nil
}

// Complete evaluates pre-produced evidence. It deliberately performs no
// network access and starts no containers. Any missing, malformed, mismatched,
// or failed required gate produces a rejected attempt and no release.
func (a AdmissionAttempt) Complete(
	releaseID string,
	evidence []GateEvidence,
	signature SignatureEnvelope,
	evaluatedBy string,
	now time.Time,
) (AdmissionAttempt, *TemplateRelease, error) {
	if a.document.SchemaVersion != AdmissionAttemptSchemaVersion {
		return AdmissionAttempt{}, nil, &Error{
			Kind: ErrAdmissionRejected, Code: "authority_receipt_required", Field: "admissionAttempt",
			Detail: "template-admission-attempt/v2 can only be completed with an exact Artifact Authority receipt",
		}
	}
	return a.complete(releaseID, evidence, signature, nil, evaluatedBy, now)
}

// CompleteWithAuthority consumes evidence only from the immutable authority
// receipt and rejects any mismatch with the exact candidate lineage.
func (a AdmissionAttempt) CompleteWithAuthority(
	releaseID string,
	receipt ArtifactAuthorityReceipt,
	evaluatedBy string,
	now time.Time,
) (AdmissionAttempt, *TemplateRelease, error) {
	if a.document.SchemaVersion != AdmissionAttemptSchemaVersionV2 {
		return AdmissionAttempt{}, nil, &Error{
			Kind: ErrAdmissionRejected, Code: "legacy_admission_not_authority_bound", Field: "admissionAttempt",
			Detail: "only template-admission-attempt/v2 accepts an Artifact Authority receipt",
		}
	}
	if err := validateArtifactAuthorityReceiptDocument(receipt.document); err != nil {
		return AdmissionAttempt{}, nil, err
	}
	view := receipt.Snapshot()
	if view.SubjectHash != a.document.SubjectHash || view.SourceTreeHash != a.document.Candidate.Source.TreeHash ||
		view.SBOMDigest != a.document.Candidate.SBOMDigest || view.RecordedBy != strings.TrimSpace(evaluatedBy) ||
		view.VerifiedAt.After(now.UTC()) {
		return AdmissionAttempt{}, nil, &Error{
			Kind: ErrAdmissionRejected, Code: "authority_receipt_lineage_mismatch", Field: "authorityReceipt",
			Detail: "receipt must bind the exact subject, source tree, SBOM, evaluator, and evaluation window",
		}
	}
	ref := receipt.Ref()
	return a.complete(releaseID, view.Evidence, view.Signature, &ref, evaluatedBy, now)
}

func (a AdmissionAttempt) complete(
	releaseID string,
	evidence []GateEvidence,
	signature SignatureEnvelope,
	authorityReceipt *ArtifactAuthorityReceiptRef,
	evaluatedBy string,
	now time.Time,
) (AdmissionAttempt, *TemplateRelease, error) {
	if a.document.Status != AttemptValidating {
		return AdmissionAttempt{}, nil, transition("admissionAttempt", a.document.Status, AttemptApproved)
	}
	if err := validateUUID(releaseID, "releaseId"); err != nil {
		return AdmissionAttempt{}, nil, err
	}
	if err := validateUUID(evaluatedBy, "evaluatedBy"); err != nil {
		return AdmissionAttempt{}, nil, err
	}
	if now.IsZero() || now.Before(a.document.UpdatedAt) {
		return AdmissionAttempt{}, nil, invalid("invalid_time", "evaluatedAt", "must not be zero or move backwards")
	}
	now = now.UTC()
	normalizedEvidence, findings := normalizeEvidence(evidence, a.document.SubjectHash, now)
	normalizedSignature, signatureFindings := normalizeSignature(signature, a.document.SubjectHash, now)
	findings = append(findings, signatureFindings...)
	sort.SliceStable(findings, func(i, j int) bool {
		left, right := findings[i], findings[j]
		return string(left.Gate)+":"+left.Code+":"+left.Field < string(right.Gate)+":"+right.Code+":"+right.Field
	})

	next := cloneAttemptDocument(a.document)
	next.Version++
	next.Evidence = append([]GateEvidence(nil), normalizedEvidence...)
	next.Signature = &normalizedSignature
	next.Findings = make([]AdmissionFinding, len(findings))
	copy(next.Findings, findings)
	next.EvaluatedBy = strings.TrimSpace(evaluatedBy)
	next.UpdatedAt = now
	next.EvaluatedAt = &now
	if authorityReceipt != nil {
		value := *authorityReceipt
		next.AuthorityReceipt = &value
	}
	if len(findings) != 0 {
		next.Status = AttemptRejected
		return AdmissionAttempt{document: next}, nil, nil
	}

	next.Status = AttemptApproved
	next.ApprovedReleaseID = strings.TrimSpace(releaseID)
	release, err := newTemplateRelease(next, normalizedSignature)
	if err != nil {
		return AdmissionAttempt{}, nil, err
	}
	return AdmissionAttempt{document: next}, &release, nil
}
