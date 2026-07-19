package templates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	defaultRegistryListLimit = 50
	maxRegistryListLimit     = 100
)

var (
	ErrRegistryNotFound    = errors.New("template registry record not found")
	ErrRegistryIntegrity   = errors.New("template registry integrity violation")
	ErrRegistryUnavailable = errors.New("template registry unavailable")
)

type RegistryError struct {
	Kind      error
	Operation string
	Resource  string
	ID        string
	Detail    string
	Cause     error
}

func (e *RegistryError) Error() string {
	if e == nil {
		return ""
	}
	identity := e.Resource
	if e.ID != "" {
		identity += " " + e.ID
	}
	if identity == "" {
		identity = "template registry"
	}
	return fmt.Sprintf("%s %s: %s", e.Operation, identity, e.Detail)
}

func (e *RegistryError) Unwrap() []error {
	result := []error{e.Kind}
	if e.Cause != nil {
		result = append(result, e.Cause)
	}
	return result
}

type TemplateReleaseListOptions struct {
	TemplateID string               `json:"templateId,omitempty"`
	States     []ReleasePolicyState `json:"states,omitempty"`
	Limit      int                  `json:"limit,omitempty"`
}

type FullStackTemplateListOptions struct {
	TemplateID string `json:"templateId,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type ExactFullStackTemplateRef struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

type TemplateReleaseRegistration struct {
	Release          TemplateRelease           `json:"release"`
	Policy           ReleasePolicy             `json:"policy"`
	AuthorityReceipt *ArtifactAuthorityReceipt `json:"authorityReceipt,omitempty"`
}

type FullStackTemplateRegistration struct {
	Template   FullStackTemplate    `json:"template"`
	Components []FullStackComponent `json:"components"`
}

type ResolvedFullStackComponent struct {
	Role      string          `json:"role"`
	MountPath string          `json:"mountPath"`
	Release   TemplateRelease `json:"release"`
	Policy    ReleasePolicy   `json:"policy"`
}

type ResolvedFullStackTemplate struct {
	Template   FullStackTemplate            `json:"template"`
	Components []ResolvedFullStackComponent `json:"components"`
}

// RegistryReader is the stable read-only boundary used by future app and HTTP
// adapters. Admission and policy mutations intentionally live elsewhere.
type RegistryReader interface {
	ListTemplateReleases(context.Context, TemplateReleaseListOptions) ([]TemplateReleaseRegistration, error)
	GetTemplateRelease(context.Context, string) (TemplateReleaseRegistration, error)
	GetTemplateReleaseExact(context.Context, TemplateReleaseRef) (TemplateReleaseRegistration, error)
	ListFullStackTemplates(context.Context, FullStackTemplateListOptions) ([]FullStackTemplateRegistration, error)
	GetFullStackTemplate(context.Context, string) (FullStackTemplateRegistration, error)
	GetFullStackTemplateExact(context.Context, ExactFullStackTemplateRef) (FullStackTemplateRegistration, error)
	ResolveForNewBuild(context.Context, ExactFullStackTemplateRef) (ResolvedFullStackTemplate, error)
}

type Registry struct {
	database *gorm.DB
}

func NewRegistry(database *gorm.DB) (*Registry, error) {
	if database == nil {
		return nil, &RegistryError{Kind: ErrRegistryUnavailable, Operation: "create", Detail: "database is required"}
	}
	return &Registry{database: database}, nil
}

func (r *Registry) ListTemplateReleases(ctx context.Context, options TemplateReleaseListOptions) ([]TemplateReleaseRegistration, error) {
	limit, err := normalizeRegistryLimit(options.Limit)
	if err != nil {
		return nil, err
	}
	templateID, err := normalizeRegistryTemplateID(options.TemplateID)
	if err != nil {
		return nil, err
	}
	states, err := normalizeRegistryStates(options.States)
	if err != nil {
		return nil, err
	}
	query := r.database.WithContext(ctx).Model(&templateReleaseModel{})
	if templateID != "" {
		query = query.Where("template_releases.template_id = ?", templateID)
	}
	if len(states) != 0 {
		query = query.Joins(`
JOIN template_release_policies AS policy_filter
  ON policy_filter.template_release_id = template_releases.id
 AND policy_filter.release_content_hash = template_releases.content_hash
`).Where("policy_filter.state IN ?", states)
	}
	var releases []templateReleaseModel
	if err := query.Order("template_releases.approved_at DESC, template_releases.id DESC").Limit(limit).Find(&releases).Error; err != nil {
		return nil, registryDatabaseError("list", "template releases", "", err)
	}
	if len(releases) == 0 {
		return []TemplateReleaseRegistration{}, nil
	}
	ids := make([]uuid.UUID, len(releases))
	for index, release := range releases {
		ids[index] = release.ID
	}
	var policies []templateReleasePolicyModel
	if err := r.database.WithContext(ctx).Where("template_release_id IN ?", ids).Find(&policies).Error; err != nil {
		return nil, registryDatabaseError("list", "template release policies", "", err)
	}
	policyByRelease := make(map[uuid.UUID]templateReleasePolicyModel, len(policies))
	for _, policy := range policies {
		if _, duplicate := policyByRelease[policy.TemplateReleaseID]; duplicate {
			return nil, registryIntegrityError("list", "template release policy", policy.TemplateReleaseID.String(), "duplicate policy rows", nil)
		}
		policyByRelease[policy.TemplateReleaseID] = policy
	}
	receiptIDs := make([]uuid.UUID, 0, len(releases))
	seenReceiptIDs := make(map[uuid.UUID]bool, len(releases))
	for _, release := range releases {
		if release.AuthorityReceiptID != nil && !seenReceiptIDs[*release.AuthorityReceiptID] {
			seenReceiptIDs[*release.AuthorityReceiptID] = true
			receiptIDs = append(receiptIDs, *release.AuthorityReceiptID)
		}
	}
	receiptByID := make(map[uuid.UUID]templateArtifactAuthorityReceiptModel, len(receiptIDs))
	if len(receiptIDs) != 0 {
		var receipts []templateArtifactAuthorityReceiptModel
		if err := r.database.WithContext(ctx).Where("id IN ?", receiptIDs).Find(&receipts).Error; err != nil {
			return nil, registryDatabaseError("list", "template authority receipts", "", err)
		}
		for _, receipt := range receipts {
			receiptByID[receipt.ID] = receipt
		}
	}
	result := make([]TemplateReleaseRegistration, 0, len(releases))
	for _, model := range releases {
		policy, ok := policyByRelease[model.ID]
		if !ok {
			return nil, registryIntegrityError("list", "template release", model.ID.String(), "release has no selection policy", nil)
		}
		var receiptModels []templateArtifactAuthorityReceiptModel
		if model.AuthorityReceiptID != nil {
			receipt, ok := receiptByID[*model.AuthorityReceiptID]
			if !ok {
				return nil, registryIntegrityError("list", "template release", model.ID.String(), "authority receipt is missing", nil)
			}
			receiptModels = append(receiptModels, receipt)
		}
		registration, err := hydrateTemplateReleaseRegistration(model, policy, receiptModels...)
		if err != nil {
			return nil, registryIntegrityError("hydrate", "template release", model.ID.String(), "stored release or policy is not canonical", err)
		}
		result = append(result, registration)
	}
	return result, nil
}

func (r *Registry) GetTemplateRelease(ctx context.Context, id string) (TemplateReleaseRegistration, error) {
	releaseID, err := parseRegistryUUID(id, "templateReleaseId")
	if err != nil {
		return TemplateReleaseRegistration{}, err
	}
	return r.getTemplateRelease(ctx, releaseID, "")
}

func (r *Registry) GetTemplateReleaseExact(ctx context.Context, ref TemplateReleaseRef) (TemplateReleaseRegistration, error) {
	releaseID, err := parseRegistryUUID(ref.ID, "templateRelease.id")
	if err != nil {
		return TemplateReleaseRegistration{}, err
	}
	if err := validateDigest(ref.ContentHash, "templateRelease.contentHash"); err != nil {
		return TemplateReleaseRegistration{}, err
	}
	if err := validateDigest(ref.SubjectHash, "templateRelease.subjectHash"); err != nil {
		return TemplateReleaseRegistration{}, err
	}
	registration, err := r.getTemplateRelease(ctx, releaseID, ref.ContentHash)
	if err != nil {
		return TemplateReleaseRegistration{}, err
	}
	if registration.Release.SubjectHash() != ref.SubjectHash {
		return TemplateReleaseRegistration{}, registryIntegrityError(
			"get exact", "template release", ref.ID,
			"subject hash differs from the exact reference", nil,
		)
	}
	return registration, nil
}

func (r *Registry) getTemplateRelease(ctx context.Context, id uuid.UUID, contentHash string) (TemplateReleaseRegistration, error) {
	query := r.database.WithContext(ctx).Where("id = ?", id)
	if contentHash != "" {
		query = query.Where("content_hash = ?", contentHash)
	}
	var release templateReleaseModel
	if err := query.First(&release).Error; err != nil {
		return TemplateReleaseRegistration{}, registryDatabaseError("get", "template release", id.String(), err)
	}
	var policy templateReleasePolicyModel
	if err := r.database.WithContext(ctx).Where("template_release_id = ?", id).First(&policy).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TemplateReleaseRegistration{}, registryIntegrityError("get", "template release", id.String(), "release has no selection policy", err)
		}
		return TemplateReleaseRegistration{}, registryDatabaseError("get", "template release policy", id.String(), err)
	}
	var receiptModels []templateArtifactAuthorityReceiptModel
	if release.AuthorityReceiptID != nil {
		var receipt templateArtifactAuthorityReceiptModel
		if err := r.database.WithContext(ctx).Where("id = ?", *release.AuthorityReceiptID).First(&receipt).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return TemplateReleaseRegistration{}, registryIntegrityError("get", "template release", id.String(), "authority receipt is missing", err)
			}
			return TemplateReleaseRegistration{}, registryDatabaseError("get", "template authority receipt", release.AuthorityReceiptID.String(), err)
		}
		receiptModels = append(receiptModels, receipt)
	}
	registration, err := hydrateTemplateReleaseRegistration(release, policy, receiptModels...)
	if err != nil {
		return TemplateReleaseRegistration{}, registryIntegrityError("hydrate", "template release", id.String(), "stored release or policy is not canonical", err)
	}
	return registration, nil
}

func (r *Registry) ListFullStackTemplates(ctx context.Context, options FullStackTemplateListOptions) ([]FullStackTemplateRegistration, error) {
	limit, err := normalizeRegistryLimit(options.Limit)
	if err != nil {
		return nil, err
	}
	templateID, err := normalizeRegistryTemplateID(options.TemplateID)
	if err != nil {
		return nil, err
	}
	query := r.database.WithContext(ctx).Model(&fullStackTemplateReleaseModel{})
	if templateID != "" {
		query = query.Where("template_id = ?", templateID)
	}
	var models []fullStackTemplateReleaseModel
	if err := query.Order("created_at DESC, id DESC").Limit(limit).Find(&models).Error; err != nil {
		return nil, registryDatabaseError("list", "full-stack templates", "", err)
	}
	result := make([]FullStackTemplateRegistration, 0, len(models))
	for _, model := range models {
		registration, err := r.hydrateFullStackRegistration(ctx, model)
		if err != nil {
			return nil, err
		}
		result = append(result, registration)
	}
	return result, nil
}

func (r *Registry) GetFullStackTemplate(ctx context.Context, id string) (FullStackTemplateRegistration, error) {
	fullStackID, err := parseRegistryUUID(id, "fullStackTemplateId")
	if err != nil {
		return FullStackTemplateRegistration{}, err
	}
	return r.getFullStackTemplate(ctx, fullStackID, "")
}

func (r *Registry) GetFullStackTemplateExact(ctx context.Context, ref ExactFullStackTemplateRef) (FullStackTemplateRegistration, error) {
	fullStackID, err := parseRegistryUUID(ref.ID, "fullStackTemplate.id")
	if err != nil {
		return FullStackTemplateRegistration{}, err
	}
	if err := validateDigest(ref.ContentHash, "fullStackTemplate.contentHash"); err != nil {
		return FullStackTemplateRegistration{}, err
	}
	return r.getFullStackTemplate(ctx, fullStackID, ref.ContentHash)
}

func (r *Registry) getFullStackTemplate(ctx context.Context, id uuid.UUID, contentHash string) (FullStackTemplateRegistration, error) {
	query := r.database.WithContext(ctx).Where("id = ?", id)
	if contentHash != "" {
		query = query.Where("content_hash = ?", contentHash)
	}
	var model fullStackTemplateReleaseModel
	if err := query.First(&model).Error; err != nil {
		return FullStackTemplateRegistration{}, registryDatabaseError("get", "full-stack template", id.String(), err)
	}
	return r.hydrateFullStackRegistration(ctx, model)
}

func (r *Registry) hydrateFullStackRegistration(ctx context.Context, model fullStackTemplateReleaseModel) (FullStackTemplateRegistration, error) {
	template, err := hydrateFullStackTemplate(model)
	if err != nil {
		return FullStackTemplateRegistration{}, registryIntegrityError("hydrate", "full-stack template", model.ID.String(), "stored document is not canonical", err)
	}
	var components []fullStackTemplateComponentModel
	if err := r.database.WithContext(ctx).
		Where("full_stack_template_id = ? AND full_stack_content_hash = ?", model.ID, model.ContentHash).
		Order("role ASC, mount_path ASC").
		Find(&components).Error; err != nil {
		return FullStackTemplateRegistration{}, registryDatabaseError("get", "full-stack template components", model.ID.String(), err)
	}
	resolvedComponents, err := validateFullStackComponentRows(template, components)
	if err != nil {
		return FullStackTemplateRegistration{}, registryIntegrityError("hydrate", "full-stack template", model.ID.String(), "component rows drift from the canonical document", err)
	}
	return FullStackTemplateRegistration{Template: template, Components: resolvedComponents}, nil
}

// ResolveForNewBuild runs inside a repeatable-read, read-only transaction. A
// FullStack reference is buildable only while every exact component release
// still has an approved policy and its role/subject/content commitments match.
func (r *Registry) ResolveForNewBuild(ctx context.Context, ref ExactFullStackTemplateRef) (ResolvedFullStackTemplate, error) {
	fullStackID, err := parseRegistryUUID(ref.ID, "fullStackTemplate.id")
	if err != nil {
		return ResolvedFullStackTemplate{}, err
	}
	if err := validateDigest(ref.ContentHash, "fullStackTemplate.contentHash"); err != nil {
		return ResolvedFullStackTemplate{}, err
	}
	var result ResolvedFullStackTemplate
	err = r.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		scoped := &Registry{database: transaction}
		registration, err := scoped.getFullStackTemplate(ctx, fullStackID, ref.ContentHash)
		if err != nil {
			return err
		}
		resolved := make([]ResolvedFullStackComponent, 0, len(registration.Components))
		for _, component := range registration.Components {
			release, err := scoped.GetTemplateReleaseExact(ctx, component.Release)
			if err != nil {
				return err
			}
			if !authorityBoundSelectableRegistration(release) {
				return &RegistryError{
					Kind: ErrReleaseNotSelectable, Operation: "resolve", Resource: "template release", ID: component.Release.ID,
					Detail: "exact component is not backed by a passed Artifact Authority receipt and approved v2 policy",
				}
			}
			if release.Policy.ReleaseContentHash != component.Release.ContentHash {
				return registryIntegrityError("resolve", "template release", component.Release.ID, "policy does not bind the component content hash", nil)
			}
			if !releaseContainsServiceKind(release.Release, component.Role) {
				return registryIntegrityError("resolve", "template release", component.Release.ID, "release no longer satisfies component role "+component.Role, nil)
			}
			resolved = append(resolved, ResolvedFullStackComponent{
				Role: component.Role, MountPath: component.MountPath,
				Release: release.Release, Policy: release.Policy,
			})
		}
		result = ResolvedFullStackTemplate{Template: registration.Template, Components: resolved}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ResolvedFullStackTemplate{}, err
	}
	return result, nil
}

type persistedReleaseDocument struct {
	ID                 string                       `json:"id"`
	SchemaVersion      string                       `json:"schemaVersion"`
	AdmissionAttemptID string                       `json:"admissionAttemptId"`
	Source             TemplateSource               `json:"source"`
	Manifest           json.RawMessage              `json:"manifest"`
	SBOMDigest         string                       `json:"sbomDigest"`
	LicenseExpression  string                       `json:"licenseExpression"`
	LicenseDigest      string                       `json:"licenseDigest"`
	EvidenceRefs       json.RawMessage              `json:"evidenceRefs"`
	Signature          json.RawMessage              `json:"signature"`
	SubjectHash        string                       `json:"subjectHash"`
	ContentHash        string                       `json:"contentHash"`
	ApprovedBy         string                       `json:"approvedBy"`
	ApprovedAt         time.Time                    `json:"approvedAt"`
	AuthorityReceipt   *ArtifactAuthorityReceiptRef `json:"authorityReceipt,omitempty"`
}

func hydrateTemplateReleaseRegistration(
	releaseModel templateReleaseModel,
	policyModel templateReleasePolicyModel,
	receiptModels ...templateArtifactAuthorityReceiptModel,
) (TemplateReleaseRegistration, error) {
	release, err := hydrateTemplateRelease(releaseModel)
	if err != nil {
		return TemplateReleaseRegistration{}, err
	}
	policyDocument := ReleasePolicy{
		SchemaVersion:     policyModel.SchemaVersion,
		TemplateReleaseID: policyModel.TemplateReleaseID.String(), ReleaseContentHash: policyModel.ReleaseContentHash,
		State: ReleasePolicyState(policyModel.State), Version: policyModel.Version, Reason: policyModel.Reason,
		UpdatedBy: policyModel.UpdatedBy.String(), CreatedAt: policyModel.CreatedAt.UTC(), UpdatedAt: policyModel.UpdatedAt.UTC(),
	}
	policyDocument.AuthorityReceipt, err = authorityReceiptRefFromModel(
		policyModel.AuthorityReceiptID, policyModel.AuthorityReceiptContentHash, policyModel.AuthorityPolicyHash,
	)
	if err != nil {
		return TemplateReleaseRegistration{}, err
	}
	encodedPolicy, err := json.Marshal(policyDocument)
	if err != nil {
		return TemplateReleaseRegistration{}, err
	}
	policy, err := ParseReleasePolicy(encodedPolicy)
	if err != nil {
		return TemplateReleaseRegistration{}, err
	}
	if policy.TemplateReleaseID != release.ID() || policy.ReleaseContentHash != release.ContentHash() {
		return TemplateReleaseRegistration{}, fmt.Errorf("policy exact release identity does not match hydrated release")
	}
	registration := TemplateReleaseRegistration{Release: release, Policy: policy}
	view := release.Snapshot()
	if view.SchemaVersion == TemplateReleaseSchemaVersion {
		if len(receiptModels) != 0 {
			return TemplateReleaseRegistration{}, fmt.Errorf("legacy release unexpectedly has an authority receipt row")
		}
		return registration, nil
	}
	if len(receiptModels) != 1 {
		return TemplateReleaseRegistration{}, fmt.Errorf("v2 release requires exactly one authority receipt row")
	}
	receipt, err := hydrateArtifactAuthorityReceipt(receiptModels[0])
	if err != nil {
		return TemplateReleaseRegistration{}, err
	}
	registration.AuthorityReceipt = &receipt
	if !authorityBoundRegistration(registration) {
		return TemplateReleaseRegistration{}, fmt.Errorf("v2 release, policy, and authority receipt lineage do not match")
	}
	return registration, nil
}

func hydrateTemplateRelease(model templateReleaseModel) (TemplateRelease, error) {
	authorityReceipt, err := authorityReceiptRefFromModel(
		model.AuthorityReceiptID, model.AuthorityReceiptContentHash, model.AuthorityPolicyHash,
	)
	if err != nil {
		return TemplateRelease{}, err
	}
	document := persistedReleaseDocument{
		ID: model.ID.String(), SchemaVersion: model.SchemaVersion, AdmissionAttemptID: model.AdmissionAttemptID.String(),
		Source: TemplateSource{
			Repository: model.SourceRepository, Branch: model.SourceBranch,
			Commit: model.SourceCommit, TreeHash: model.TreeHash,
		},
		Manifest: model.Manifest, SBOMDigest: model.SBOMDigest,
		LicenseExpression: model.LicenseExpression, LicenseDigest: model.LicenseDigest,
		EvidenceRefs: model.EvidenceRefs, Signature: model.Signature,
		SubjectHash: model.SubjectHash, ContentHash: model.ContentHash,
		ApprovedBy: model.ApprovedBy.String(), ApprovedAt: model.ApprovedAt.UTC(),
		AuthorityReceipt: authorityReceipt,
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return TemplateRelease{}, err
	}
	release, err := ParseTemplateRelease(encoded)
	if err != nil {
		return TemplateRelease{}, err
	}
	view := release.Snapshot()
	if view.Manifest.TemplateID != model.TemplateID || view.Manifest.Version != model.ReleaseVersion {
		return TemplateRelease{}, fmt.Errorf("release index columns differ from canonical manifest")
	}
	if model.CreatedAt.IsZero() || model.CreatedAt.Before(view.ApprovedAt) {
		return TemplateRelease{}, fmt.Errorf("release registry creation time predates approval")
	}
	return release, nil
}

func hydrateArtifactAuthorityReceipt(model templateArtifactAuthorityReceiptModel) (ArtifactAuthorityReceipt, error) {
	receipt, err := ParseArtifactAuthorityReceipt(model.Document)
	if err != nil {
		return ArtifactAuthorityReceipt{}, err
	}
	view := receipt.Snapshot()
	if view.ID != model.ID.String() || view.SchemaVersion != model.SchemaVersion || view.Decision != model.Decision ||
		view.SubjectHash != model.SubjectHash || view.SourceTreeHash != model.SourceTreeHash ||
		view.ArtifactDigest != model.ArtifactDigest || view.SBOMDigest != model.SBOMDigest ||
		view.SignatureBundleDigest != model.SignatureBundleDigest || view.PolicyHash != model.PolicyHash ||
		view.ContentHash != model.ContentHash || view.Authority.ID != model.AuthorityID ||
		view.Authority.Version != model.AuthorityVersion || view.VerifierImageDigest != model.VerifierImageDigest ||
		view.TrustRootDigest != model.TrustRootDigest || view.TransparencyLog.ID != model.TransparencyLogID ||
		view.TransparencyLog.EntryUUID != model.TransparencyEntryUUID ||
		view.TransparencyLog.LogIndex != model.TransparencyLogIndex ||
		view.Proof.TransparencyBundleDigest != model.TransparencyBundleDigest ||
		view.Proof.TreeSize != uint64(model.TransparencyTreeSize) || view.Proof.RootHash != model.TransparencyRootHash ||
		!view.TransparencyLog.IntegratedAt.Equal(model.IntegratedAt) ||
		view.VerificationReference != model.VerificationReference || !view.VerifiedAt.Equal(model.VerifiedAt) ||
		view.RecordedBy != model.RecordedBy.String() || !view.CreatedAt.Equal(model.CreatedAt) {
		return ArtifactAuthorityReceipt{}, fmt.Errorf("authority receipt index columns differ from canonical document")
	}
	return receipt, nil
}

func authorityReceiptRefFromModel(id *uuid.UUID, contentHash, policyHash *string) (*ArtifactAuthorityReceiptRef, error) {
	if id == nil && contentHash == nil && policyHash == nil {
		return nil, nil
	}
	if id == nil || contentHash == nil || policyHash == nil {
		return nil, fmt.Errorf("partial authority receipt identity")
	}
	ref := &ArtifactAuthorityReceiptRef{ID: id.String(), ContentHash: *contentHash, PolicyHash: *policyHash}
	if err := validateArtifactAuthorityReceiptRef(*ref); err != nil {
		return nil, err
	}
	return ref, nil
}

func hydrateFullStackTemplate(model fullStackTemplateReleaseModel) (FullStackTemplate, error) {
	template, err := ParseFullStackTemplate(model.Document)
	if err != nil {
		return FullStackTemplate{}, err
	}
	view := template.Snapshot()
	if view.ID != model.ID.String() || view.SchemaVersion != model.SchemaVersion ||
		view.TemplateID != model.TemplateID || view.Version != model.ReleaseVersion ||
		view.ContentHash != model.ContentHash || view.CreatedBy != model.CreatedBy.String() ||
		!view.CreatedAt.Equal(model.CreatedAt) {
		return FullStackTemplate{}, fmt.Errorf("full-stack index columns differ from canonical document")
	}
	return template, nil
}

func validateFullStackComponentRows(template FullStackTemplate, rows []fullStackTemplateComponentModel) ([]FullStackComponent, error) {
	view := template.Snapshot()
	if len(rows) != len(view.Components) {
		return nil, fmt.Errorf("expected %d component rows, found %d", len(view.Components), len(rows))
	}
	expected := make(map[string]FullStackComponent, len(view.Components))
	for _, component := range view.Components {
		if _, duplicate := expected[component.Role]; duplicate {
			return nil, fmt.Errorf("canonical document repeats role %s", component.Role)
		}
		expected[component.Role] = component
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if row.FullStackTemplateID.String() != view.ID || row.FullStackContentHash != view.ContentHash {
			return nil, fmt.Errorf("component row points at a different full-stack identity")
		}
		component, ok := expected[row.Role]
		if !ok || seen[row.Role] {
			return nil, fmt.Errorf("component row has unknown or duplicate role %s", row.Role)
		}
		seen[row.Role] = true
		if row.MountPath != component.MountPath || row.TemplateReleaseID.String() != component.Release.ID ||
			row.TemplateReleaseContentHash != component.Release.ContentHash {
			return nil, fmt.Errorf("component role %s differs from its canonical exact reference", row.Role)
		}
	}
	result := append([]FullStackComponent(nil), view.Components...)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Role+":"+result[i].MountPath < result[j].Role+":"+result[j].MountPath
	})
	return result, nil
}

func normalizeRegistryLimit(limit int) (int, error) {
	if limit == 0 {
		return defaultRegistryListLimit, nil
	}
	if limit < 1 || limit > maxRegistryListLimit {
		return 0, invalid("invalid_registry_limit", "limit", "must be between 1 and 100")
	}
	return limit, nil
}

func normalizeRegistryTemplateID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" && (!slugPattern.MatchString(value) || len(value) > 120) {
		return "", invalid("invalid_template_id", "templateId", "must be a lowercase kebab-case identifier")
	}
	return value, nil
}

func normalizeRegistryStates(values []ReleasePolicyState) ([]string, error) {
	seen := map[ReleasePolicyState]bool{}
	result := make([]string, 0, len(values))
	for _, state := range values {
		if state != ReleaseApproved && state != ReleaseDeprecated && state != ReleaseRevoked {
			return nil, invalid("invalid_release_policy_state", "states", "must contain only approved, deprecated, or revoked")
		}
		if !seen[state] {
			seen[state] = true
			result = append(result, state.String())
		}
	}
	sort.Strings(result)
	return result, nil
}

func parseRegistryUUID(value, field string) (uuid.UUID, error) {
	if err := validateUUID(value, field); err != nil {
		return uuid.Nil, err
	}
	return uuid.Parse(strings.TrimSpace(value))
}

func registryDatabaseError(operation, resource, id string, cause error) error {
	if errors.Is(cause, gorm.ErrRecordNotFound) {
		return &RegistryError{Kind: ErrRegistryNotFound, Operation: operation, Resource: resource, ID: id, Detail: "record does not exist", Cause: cause}
	}
	return &RegistryError{Kind: ErrRegistryUnavailable, Operation: operation, Resource: resource, ID: id, Detail: "database operation failed", Cause: cause}
}

func registryIntegrityError(operation, resource, id, detail string, cause error) error {
	return &RegistryError{Kind: ErrRegistryIntegrity, Operation: operation, Resource: resource, ID: id, Detail: detail, Cause: cause}
}

var _ RegistryReader = (*Registry)(nil)
