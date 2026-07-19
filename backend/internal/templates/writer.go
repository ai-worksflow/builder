package templates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"gorm.io/gorm"
)

// Writer is an operator-only persistence boundary. Evidence is obtained only
// from its configured ArtifactAuthority; Admit callers supply references and
// signed bytes to verify, never evidence, signatures, trust roots, or policy.
type Writer struct {
	database  *gorm.DB
	authority ArtifactAuthority
	now       func() time.Time
}

type AdmitInput struct {
	AttemptID   string
	ReleaseID   string
	Candidate   AdmissionCandidate
	Bundle      ArtifactAdmissionBundle
	RequestedBy string
	EvaluatedBy string
}

type AdmissionRegistration struct {
	Attempt          AdmissionAttemptView          `json:"attempt"`
	AuthorityReceipt *ArtifactAuthorityReceiptView `json:"authorityReceipt,omitempty"`
	Release          *TemplateReleaseRegistration  `json:"release,omitempty"`
}

type FullStackComponentSelection struct {
	Role      string             `json:"role"`
	MountPath string             `json:"mountPath"`
	Release   TemplateReleaseRef `json:"release"`
}

type RegisterFullStackInput struct {
	ID         string
	TemplateID string
	Version    string
	Components []FullStackComponentSelection
	Layout     FullStackLayout
	CreatedBy  string
	CreatedAt  time.Time
}

func NewWriter(database *gorm.DB, authority ArtifactAuthority) (*Writer, error) {
	if database == nil {
		return nil, &RegistryError{Kind: ErrRegistryUnavailable, Operation: "create writer", Detail: "database is required"}
	}
	if authority == nil {
		return nil, &RegistryError{Kind: ErrRegistryUnavailable, Operation: "create writer", Detail: "Artifact Authority is required"}
	}
	return &Writer{database: database, authority: authority, now: time.Now}, nil
}

// Admit derives every status and commitment through the domain state machine,
// then persists candidate -> validating -> approved/rejected in one SQL
// transaction. No caller-provided status or hash is accepted.
func (w *Writer) Admit(ctx context.Context, input AdmitInput) (AdmissionRegistration, error) {
	if w == nil || w.database == nil || w.authority == nil || w.now == nil {
		return AdmissionRegistration{}, &RegistryError{Kind: ErrRegistryUnavailable, Operation: "admit", Detail: "writer is not configured"}
	}
	if err := w.authority.Readiness(ctx); err != nil {
		return AdmissionRegistration{}, &RegistryError{
			Kind: ErrRegistryUnavailable, Operation: "admit", Resource: "Template Artifact Authority",
			Detail: "authority is not ready", Cause: err,
		}
	}
	if err := validateUUID(input.RequestedBy, "requestedBy"); err != nil {
		return AdmissionRegistration{}, err
	}
	if err := validateUUID(input.EvaluatedBy, "evaluatedBy"); err != nil {
		return AdmissionRegistration{}, err
	}
	requesterUUID, _ := uuid.Parse(strings.TrimSpace(input.RequestedBy))
	evaluatorUUID, _ := uuid.Parse(strings.TrimSpace(input.EvaluatedBy))
	if requesterUUID == evaluatorUUID {
		return AdmissionRegistration{}, invalid("independent_review_required", "evaluatedBy", "requester and evaluator must be different users")
	}

	candidateAt := w.now().UTC()
	if candidateAt.IsZero() {
		return AdmissionRegistration{}, invalid("invalid_time", "createdAt", "trusted writer clock returned zero")
	}
	candidate, err := NewAuthorityAdmissionAttempt(input.AttemptID, input.RequestedBy, input.Candidate, candidateAt)
	if err != nil {
		return AdmissionRegistration{}, err
	}
	validatingAt := w.now().UTC()
	validating, err := candidate.BeginValidation(validatingAt)
	if err != nil {
		return AdmissionRegistration{}, err
	}
	candidateView := validating.Snapshot()
	receipt, err := w.authority.Verify(ctx, ArtifactAuthorityVerifyRequest{
		Candidate: candidateView.Candidate, SubjectHash: candidateView.SubjectHash,
		Bundle: input.Bundle, RecordedBy: strings.TrimSpace(input.EvaluatedBy),
	})
	if err != nil {
		return AdmissionRegistration{}, err
	}
	evaluatedAt := w.now().UTC()
	completed, release, err := validating.CompleteWithAuthority(input.ReleaseID, receipt, input.EvaluatedBy, evaluatedAt)
	if err != nil {
		return AdmissionRegistration{}, err
	}

	candidateModel, err := admissionModel(candidate)
	if err != nil {
		return AdmissionRegistration{}, err
	}
	validatingModel, err := admissionModel(validating)
	if err != nil {
		return AdmissionRegistration{}, err
	}
	completedModel, err := admissionModel(completed)
	if err != nil {
		return AdmissionRegistration{}, err
	}
	receiptModel, hydratedReceipt, err := authorityReceiptPersistenceModel(receipt)
	if err != nil {
		return AdmissionRegistration{}, err
	}

	var registration AdmissionRegistration
	err = w.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Create(&receiptModel).Error; err != nil {
			return err
		}
		if err := transaction.Create(&candidateModel).Error; err != nil {
			return err
		}
		if err := transitionAdmissionModel(transaction, candidateModel, validatingModel); err != nil {
			return err
		}
		if err := transitionAdmissionModel(transaction, validatingModel, completedModel); err != nil {
			return err
		}

		receiptView := hydratedReceipt.Snapshot()
		registration = AdmissionRegistration{Attempt: completed.Snapshot(), AuthorityReceipt: &receiptView}
		if release == nil {
			return nil
		}
		policy, err := NewReleasePolicy(*release, input.EvaluatedBy, evaluatedAt)
		if err != nil {
			return err
		}
		releaseModel, hydratedRelease, err := releasePersistenceModel(*release, evaluatedAt)
		if err != nil {
			return err
		}
		policyModel, hydratedPolicy, err := policyPersistenceModel(policy)
		if err != nil {
			return err
		}
		if err := transaction.Create(&releaseModel).Error; err != nil {
			return err
		}
		if err := transaction.Create(&policyModel).Error; err != nil {
			return err
		}
		registration.Release = &TemplateReleaseRegistration{Release: hydratedRelease, Policy: hydratedPolicy}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return AdmissionRegistration{}, writerOperationError("admit", "template admission", strings.TrimSpace(input.AttemptID), err)
	}
	return registration, nil
}

// RegisterFullStack loads every component through the exact Registry reader,
// requires an approved current policy, constructs the immutable document via
// NewFullStackTemplate, and persists the document and all projections atomically.
func (w *Writer) RegisterFullStack(ctx context.Context, input RegisterFullStackInput) (FullStackTemplateRegistration, error) {
	if w == nil || w.database == nil {
		return FullStackTemplateRegistration{}, &RegistryError{Kind: ErrRegistryUnavailable, Operation: "register", Detail: "writer is not configured"}
	}
	var result FullStackTemplateRegistration
	err := w.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		registry := &Registry{database: transaction}
		components := make([]FullStackComponentInput, 0, len(input.Components))
		for _, selection := range input.Components {
			registration, err := registry.GetTemplateReleaseExact(ctx, selection.Release)
			if err != nil {
				return err
			}
			if !authorityBoundSelectableRegistration(registration) {
				return &RegistryError{
					Kind: ErrReleaseNotSelectable, Operation: "register", Resource: "template release", ID: selection.Release.ID,
					Detail: "exact component release policy is not approved",
				}
			}
			components = append(components, FullStackComponentInput{
				Role: selection.Role, MountPath: selection.MountPath, Release: registration.Release,
			})
		}
		template, err := NewFullStackTemplate(
			input.ID, input.TemplateID, input.Version, components, input.Layout, input.CreatedBy, input.CreatedAt,
		)
		if err != nil {
			return err
		}
		model, hydrated, err := fullStackPersistenceModel(template)
		if err != nil {
			return err
		}
		if err := transaction.Create(&model).Error; err != nil {
			return err
		}
		view := hydrated.Snapshot()
		for _, component := range view.Components {
			componentModel := fullStackTemplateComponentModel{
				FullStackTemplateID: uuid.MustParse(view.ID), FullStackContentHash: view.ContentHash,
				Role: component.Role, MountPath: component.MountPath,
				TemplateReleaseID:          uuid.MustParse(component.Release.ID),
				TemplateReleaseContentHash: component.Release.ContentHash,
			}
			if err := transaction.Create(&componentModel).Error; err != nil {
				return err
			}
		}
		result = FullStackTemplateRegistration{
			Template: hydrated, Components: append([]FullStackComponent(nil), view.Components...),
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return FullStackTemplateRegistration{}, writerOperationError("register", "full-stack template", strings.TrimSpace(input.ID), err)
	}
	return result, nil
}

func admissionModel(attempt AdmissionAttempt) (admissionAttemptModel, error) {
	view := attempt.Snapshot()
	id, err := uuid.Parse(view.ID)
	if err != nil {
		return admissionAttemptModel{}, err
	}
	requestedBy, err := uuid.Parse(view.RequestedBy)
	if err != nil {
		return admissionAttemptModel{}, err
	}
	source, err := canonicalDocument(view.Candidate.Source)
	if err != nil {
		return admissionAttemptModel{}, err
	}
	manifest, err := canonicalDocument(view.Candidate.Manifest)
	if err != nil {
		return admissionAttemptModel{}, err
	}
	evidence, err := canonicalDocument(view.Evidence)
	if err != nil {
		return admissionAttemptModel{}, err
	}
	findings, err := canonicalDocument(view.Findings)
	if err != nil {
		return admissionAttemptModel{}, err
	}
	model := admissionAttemptModel{
		ID: id, SchemaVersion: view.SchemaVersion, Status: view.Status.String(), Version: view.Version,
		Source: source, Manifest: manifest, SBOMDigest: view.Candidate.SBOMDigest,
		LicenseExpression: view.Candidate.LicenseExpression, LicenseDigest: view.Candidate.LicenseDigest,
		SubjectHash: view.SubjectHash, Evidence: evidence, Findings: findings,
		RequestedBy: requestedBy, CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt,
	}
	if view.Signature != nil {
		signature, err := canonicalDocument(*view.Signature)
		if err != nil {
			return admissionAttemptModel{}, err
		}
		model.Signature = &signature
	}
	if view.ApprovedReleaseID != "" {
		value, err := uuid.Parse(view.ApprovedReleaseID)
		if err != nil {
			return admissionAttemptModel{}, err
		}
		model.ApprovedReleaseID = &value
	}
	if view.EvaluatedBy != "" {
		value, err := uuid.Parse(view.EvaluatedBy)
		if err != nil {
			return admissionAttemptModel{}, err
		}
		model.EvaluatedBy = &value
	}
	if view.EvaluatedAt != nil {
		value := view.EvaluatedAt.UTC()
		model.EvaluatedAt = &value
	}
	if view.AuthorityReceipt != nil {
		id := uuid.MustParse(view.AuthorityReceipt.ID)
		contentHash := view.AuthorityReceipt.ContentHash
		policyHash := view.AuthorityReceipt.PolicyHash
		model.AuthorityReceiptID = &id
		model.AuthorityReceiptContentHash = &contentHash
		model.AuthorityPolicyHash = &policyHash
	}
	return model, nil
}

func transitionAdmissionModel(transaction *gorm.DB, from, to admissionAttemptModel) error {
	updates := map[string]any{
		"status": to.Status, "version": to.Version, "updated_at": to.UpdatedAt,
	}
	if to.Status == AttemptApproved.String() || to.Status == AttemptRejected.String() {
		if to.Signature == nil {
			return fmt.Errorf("completed template admission has no signature")
		}
		updates["evidence"] = gorm.Expr("?::jsonb", string(to.Evidence))
		updates["signature"] = gorm.Expr("?::jsonb", string(*to.Signature))
		updates["findings"] = gorm.Expr("?::jsonb", string(to.Findings))
		updates["approved_release_id"] = to.ApprovedReleaseID
		updates["evaluated_by"] = to.EvaluatedBy
		updates["evaluated_at"] = to.EvaluatedAt
		updates["authority_receipt_id"] = to.AuthorityReceiptID
		updates["authority_receipt_content_hash"] = to.AuthorityReceiptContentHash
		updates["authority_policy_hash"] = to.AuthorityPolicyHash
	}
	result := transaction.Model(&admissionAttemptModel{}).
		Where("id = ? AND status = ? AND version = ?", from.ID, from.Status, from.Version).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("template admission transition lost exact state")
	}
	return nil
}

func releasePersistenceModel(release TemplateRelease, createdAt time.Time) (templateReleaseModel, TemplateRelease, error) {
	canonical, err := release.CanonicalJSON()
	if err != nil {
		return templateReleaseModel{}, TemplateRelease{}, err
	}
	hydrated, err := ParseTemplateRelease(canonical)
	if err != nil {
		return templateReleaseModel{}, TemplateRelease{}, err
	}
	view := hydrated.Snapshot()
	manifest, err := canonicalDocument(view.Manifest)
	if err != nil {
		return templateReleaseModel{}, TemplateRelease{}, err
	}
	evidence, err := canonicalDocument(view.EvidenceRefs)
	if err != nil {
		return templateReleaseModel{}, TemplateRelease{}, err
	}
	signature, err := canonicalDocument(view.Signature)
	if err != nil {
		return templateReleaseModel{}, TemplateRelease{}, err
	}
	model := templateReleaseModel{
		ID: uuid.MustParse(view.ID), SchemaVersion: view.SchemaVersion,
		AdmissionAttemptID: uuid.MustParse(view.AdmissionAttemptID),
		TemplateID:         view.Manifest.TemplateID, ReleaseVersion: view.Manifest.Version,
		SourceRepository: view.Source.Repository, SourceBranch: view.Source.Branch,
		SourceCommit: view.Source.Commit, TreeHash: view.Source.TreeHash,
		Manifest: manifest, SBOMDigest: view.SBOMDigest,
		LicenseExpression: view.LicenseExpression, LicenseDigest: view.LicenseDigest,
		EvidenceRefs: evidence, Signature: signature, SubjectHash: view.SubjectHash,
		ContentHash: view.ContentHash, ApprovedBy: uuid.MustParse(view.ApprovedBy),
		ApprovedAt: view.ApprovedAt, CreatedAt: createdAt.UTC(),
	}
	if view.AuthorityReceipt != nil {
		id := uuid.MustParse(view.AuthorityReceipt.ID)
		contentHash := view.AuthorityReceipt.ContentHash
		policyHash := view.AuthorityReceipt.PolicyHash
		model.AuthorityReceiptID = &id
		model.AuthorityReceiptContentHash = &contentHash
		model.AuthorityPolicyHash = &policyHash
	}
	return model, hydrated, nil
}

func policyPersistenceModel(policy ReleasePolicy) (templateReleasePolicyModel, ReleasePolicy, error) {
	canonical, err := domain.CanonicalJSON(policy)
	if err != nil {
		return templateReleasePolicyModel{}, ReleasePolicy{}, err
	}
	hydrated, err := ParseReleasePolicy(canonical)
	if err != nil {
		return templateReleasePolicyModel{}, ReleasePolicy{}, err
	}
	model := templateReleasePolicyModel{
		SchemaVersion:     hydrated.SchemaVersion,
		TemplateReleaseID: uuid.MustParse(hydrated.TemplateReleaseID), ReleaseContentHash: hydrated.ReleaseContentHash,
		State: hydrated.State.String(), Version: hydrated.Version, Reason: hydrated.Reason,
		UpdatedBy: uuid.MustParse(hydrated.UpdatedBy), CreatedAt: hydrated.CreatedAt, UpdatedAt: hydrated.UpdatedAt,
	}
	if hydrated.AuthorityReceipt != nil {
		id := uuid.MustParse(hydrated.AuthorityReceipt.ID)
		contentHash := hydrated.AuthorityReceipt.ContentHash
		policyHash := hydrated.AuthorityReceipt.PolicyHash
		model.AuthorityReceiptID = &id
		model.AuthorityReceiptContentHash = &contentHash
		model.AuthorityPolicyHash = &policyHash
	}
	return model, hydrated, nil
}

func authorityReceiptPersistenceModel(receipt ArtifactAuthorityReceipt) (templateArtifactAuthorityReceiptModel, ArtifactAuthorityReceipt, error) {
	canonical, err := receipt.CanonicalJSON()
	if err != nil {
		return templateArtifactAuthorityReceiptModel{}, ArtifactAuthorityReceipt{}, err
	}
	hydrated, err := ParseArtifactAuthorityReceipt(canonical)
	if err != nil {
		return templateArtifactAuthorityReceiptModel{}, ArtifactAuthorityReceipt{}, err
	}
	view := hydrated.Snapshot()
	return templateArtifactAuthorityReceiptModel{
		ID: uuid.MustParse(view.ID), SchemaVersion: view.SchemaVersion, Decision: view.Decision,
		SubjectHash: view.SubjectHash, SourceTreeHash: view.SourceTreeHash,
		ArtifactDigest: view.ArtifactDigest, SBOMDigest: view.SBOMDigest,
		SignatureBundleDigest: view.SignatureBundleDigest, PolicyHash: view.PolicyHash,
		ContentHash: view.ContentHash, AuthorityID: view.Authority.ID, AuthorityVersion: view.Authority.Version,
		VerifierImageDigest: view.VerifierImageDigest, TrustRootDigest: view.TrustRootDigest,
		TransparencyLogID: view.TransparencyLog.ID, TransparencyEntryUUID: view.TransparencyLog.EntryUUID,
		TransparencyLogIndex:     view.TransparencyLog.LogIndex,
		TransparencyBundleDigest: view.Proof.TransparencyBundleDigest,
		TransparencyTreeSize:     int64(view.Proof.TreeSize), TransparencyRootHash: view.Proof.RootHash,
		IntegratedAt:          view.TransparencyLog.IntegratedAt,
		VerificationReference: view.VerificationReference, VerifiedAt: view.VerifiedAt,
		RecordedBy: uuid.MustParse(view.RecordedBy), CreatedAt: view.CreatedAt, Document: canonical,
	}, hydrated, nil
}

func fullStackPersistenceModel(template FullStackTemplate) (fullStackTemplateReleaseModel, FullStackTemplate, error) {
	canonical, err := template.CanonicalJSON()
	if err != nil {
		return fullStackTemplateReleaseModel{}, FullStackTemplate{}, err
	}
	hydrated, err := ParseFullStackTemplate(canonical)
	if err != nil {
		return fullStackTemplateReleaseModel{}, FullStackTemplate{}, err
	}
	view := hydrated.Snapshot()
	return fullStackTemplateReleaseModel{
		ID: uuid.MustParse(view.ID), SchemaVersion: view.SchemaVersion,
		TemplateID: view.TemplateID, ReleaseVersion: view.Version,
		Document: canonical, ContentHash: view.ContentHash,
		CreatedBy: uuid.MustParse(view.CreatedBy), CreatedAt: view.CreatedAt,
	}, hydrated, nil
}

func canonicalDocument(value any) (json.RawMessage, error) {
	encoded, err := domain.CanonicalJSON(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize template registry document: %w", err)
	}
	return append(json.RawMessage(nil), encoded...), nil
}

func writerOperationError(operation, resource, id string, err error) error {
	var registryError *RegistryError
	if errors.As(err, &registryError) {
		return err
	}
	var domainError *Error
	if errors.As(err, &domainError) {
		return err
	}
	return registryDatabaseError(operation, resource, id, err)
}
