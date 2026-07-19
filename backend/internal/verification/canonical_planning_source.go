package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/worksflow/builder/backend/internal/constructor"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/templates"
	"gorm.io/gorm"
)

var (
	ErrCanonicalPlanningBlocked = errors.New("canonical verification planning is blocked")
	ErrCanonicalPlanningDrift   = errors.New("canonical verification planning source changed")
)

type CanonicalPlanRequest struct {
	ProjectID         string
	WorkspaceRevision CanonicalPlanSubject
	Profile           ProfileReference
	ActorID           string
}

type CanonicalPlanSource interface {
	LoadCanonicalPlan(context.Context, CanonicalPlanRequest) (CompileCanonicalPlanInput, error)
}

type PostgresCanonicalPlanSource struct {
	database *gorm.DB
	contents PlanningContentReader
}

func NewPostgresCanonicalPlanSource(
	database *gorm.DB,
	contents PlanningContentReader,
) (*PostgresCanonicalPlanSource, error) {
	if database == nil || contents == nil {
		return nil, errors.New("canonical planning database and content reader are required")
	}
	return &PostgresCanonicalPlanSource{database: database, contents: contents}, nil
}

func (source *PostgresCanonicalPlanSource) LoadCanonicalPlan(
	ctx context.Context,
	request CanonicalPlanRequest,
) (CompileCanonicalPlanInput, error) {
	if !validUUIDs(request.ProjectID, request.ActorID) {
		return CompileCanonicalPlanInput{}, runInvalid("Canonical VerificationPlan request identity")
	}
	if normalized, err := normalizeCanonicalPlanSubject(request.WorkspaceRevision); err != nil || normalized != request.WorkspaceRevision {
		return CompileCanonicalPlanInput{}, runInvalid("Canonical VerificationPlan WorkspaceRevision")
	}
	if profile, err := normalizeProfile(request.Profile); err != nil || profile != request.Profile {
		return CompileCanonicalPlanInput{}, runInvalid("Canonical VerificationPlan profile")
	}
	row, err := source.loadExactCanonicalSubject(ctx, request)
	if err != nil {
		return CompileCanonicalPlanInput{}, err
	}
	candidateSource := &PostgresCandidatePlanSource{database: source.database, contents: source.contents}
	contract, err := candidateSource.loadExactBuildContract(ctx, row)
	if err != nil {
		return CompileCanonicalPlanInput{}, fmt.Errorf("%w: %v", ErrCanonicalPlanningDrift, err)
	}
	if err := candidateSource.validateObligationProjections(ctx, row, contract); err != nil {
		return CompileCanonicalPlanInput{}, fmt.Errorf("%w: %v", ErrCanonicalPlanningDrift, err)
	}
	releases, err := source.loadExactCanonicalTemplateReleases(ctx, row, contract.TemplateReleaseRefs)
	if err != nil {
		return CompileCanonicalPlanInput{}, err
	}
	profile, err := decodeVerificationProfile(row)
	if err != nil {
		return CompileCanonicalPlanInput{}, fmt.Errorf("%w: %v", ErrCanonicalPlanningDrift, err)
	}
	oracles, obligations, err := verificationRequirements(contract)
	if err != nil {
		return CompileCanonicalPlanInput{}, fmt.Errorf("%w: %v", ErrCanonicalPlanningDrift, err)
	}
	return CompileCanonicalPlanInput{
		ProjectID: row.ProjectID,
		Subject: CanonicalPlanSubject{
			WorkspaceArtifactID:  request.WorkspaceRevision.WorkspaceArtifactID,
			WorkspaceRevisionID:  request.WorkspaceRevision.WorkspaceRevisionID,
			WorkspaceContentHash: request.WorkspaceRevision.WorkspaceContentHash,
		},
		BuildManifest: repository.ExactReference{ID: row.BuildManifestID, ContentHash: row.BuildManifestHash},
		BuildContract: repository.ExactReference{ID: row.BuildContractID, ContentHash: row.BuildContractHash},
		FullStackTemplate: repository.ExactReference{
			ID: row.FullStackTemplateID, ContentHash: row.FullStackTemplateHash,
		},
		Profile: profile, TemplateReleases: releases, Oracles: oracles, Obligations: obligations,
	}, nil
}

func (source *PostgresCanonicalPlanSource) loadExactCanonicalSubject(
	ctx context.Context,
	request CanonicalPlanRequest,
) (candidatePlanSourceRow, error) {
	var row candidatePlanSourceRow
	result := source.database.WithContext(ctx).Raw(`
SELECT
  artifact.project_id::text AS project_id,
  manifest.id::text AS build_manifest_id,
  verification_normalize_sha256(manifest.manifest_hash) AS build_manifest_hash,
  contract.id::text AS build_contract_id,
  verification_normalize_sha256(contract.contract_hash) AS build_contract_hash,
  contract.content_ref AS contract_content_ref,
  contract.content_hash AS contract_content_hash,
  contract.compiler_version AS contract_compiler_version,
  verification_normalize_sha256(contract.compiler_hash) AS contract_compiler_hash,
  contract.must_count AS contract_must_count,
  contract.obligation_count AS contract_obligation_count,
  contract.template_release_count AS contract_template_count,
  contract.full_stack_template_id::text AS full_stack_template_id,
  contract.full_stack_template_hash,
  profile.profile_id AS verification_profile_id,
  profile.version AS verification_profile_version,
  profile.content_hash AS verification_profile_hash,
  profile.document AS verification_profile
FROM artifact_revisions AS revision
JOIN artifacts AS artifact
  ON artifact.id = revision.artifact_id
 AND artifact.project_id = ? AND artifact.kind = 'workspace'
JOIN implementation_proposals AS proposal
  ON proposal.id = revision.implementation_proposal_id
 AND proposal.project_id = artifact.project_id
 AND proposal.status IN ('applied', 'partially_applied')
 AND proposal.applied_at IS NOT NULL
JOIN application_build_manifests AS manifest
  ON manifest.id = proposal.build_manifest_id
 AND manifest.project_id = artifact.project_id
 AND manifest.status = 'consumed' AND manifest.invalidated_at IS NULL
JOIN application_build_contracts AS contract
  ON contract.id = proposal.application_build_contract_id
 AND contract.project_id = artifact.project_id
 AND verification_normalize_sha256(contract.contract_hash) = verification_normalize_sha256(proposal.application_build_contract_hash)
JOIN full_stack_template_releases AS full_stack
  ON full_stack.id = contract.full_stack_template_id
 AND full_stack.content_hash = contract.full_stack_template_hash
JOIN verification_profile_versions AS profile
  ON profile.profile_id = ? AND profile.version = ? AND profile.content_hash = ?
JOIN verification_profile_policies AS profile_policy
  ON profile_policy.profile_id = profile.profile_id
 AND profile_policy.profile_version = profile.version
 AND profile_policy.profile_hash = profile.content_hash
 AND profile_policy.state = 'active'
WHERE revision.id = ? AND revision.artifact_id = ? AND revision.content_hash = ?
  AND revision.workflow_status = 'approved'
  AND contract.build_manifest_id = manifest.id
  AND verification_normalize_sha256(contract.build_manifest_hash) = verification_normalize_sha256(manifest.manifest_hash)
  AND contract.status = 'ready' AND contract.must_count > 0
  AND contract.must_ready_count = contract.must_count
  AND contract.obligation_count >= contract.must_count
  AND contract.source_count > 0 AND contract.template_release_count >= 2
  AND contract.blocking_count = 0 AND contract.conflict_count = 0
`, request.ProjectID, request.Profile.ID, int64(request.Profile.Version), request.Profile.ContentHash,
		request.WorkspaceRevision.WorkspaceRevisionID, request.WorkspaceRevision.WorkspaceArtifactID,
		request.WorkspaceRevision.WorkspaceContentHash).Scan(&row)
	if result.Error != nil {
		return candidatePlanSourceRow{}, fmt.Errorf("%w: load exact approved WorkspaceRevision: %v", ErrCanonicalPlanningBlocked, result.Error)
	}
	if result.RowsAffected != 1 || row.ProjectID != request.ProjectID ||
		row.VerificationProfileID != request.Profile.ID ||
		row.VerificationProfileVersion != int64(request.Profile.Version) ||
		row.VerificationProfileHash != request.Profile.ContentHash ||
		!validUUIDs(row.BuildManifestID, row.BuildContractID, row.FullStackTemplateID) ||
		!exactSHA256(row.BuildManifestHash) || !exactSHA256(row.BuildContractHash) ||
		!exactSHA256(row.FullStackTemplateHash) {
		return candidatePlanSourceRow{}, fmt.Errorf("%w: exact approved WorkspaceRevision, applied Proposal lineage, or active profile is unavailable", ErrCanonicalPlanningBlocked)
	}
	return row, nil
}

func (source *PostgresCanonicalPlanSource) loadExactCanonicalTemplateReleases(
	ctx context.Context,
	row candidatePlanSourceRow,
	contractRefs []constructor.TemplateReleaseRef,
) ([]ResolvedTemplateRelease, error) {
	var rows []canonicalPlanTemplateRow
	result := source.database.WithContext(ctx).Raw(`
SELECT selected.ordinal, selected.role, component.mount_path,
       selected.template_release_id::text AS template_release_id,
       verification_normalize_sha256(selected.template_release_content_hash) AS template_release_content_hash,
       release.subject_hash, release.manifest
FROM application_build_contract_template_releases AS selected
JOIN full_stack_template_components AS component
  ON component.full_stack_template_id = ?
 AND component.full_stack_content_hash = ?
 AND component.role = selected.role
 AND component.template_release_id = selected.template_release_id
 AND component.template_release_content_hash = selected.template_release_content_hash
JOIN template_releases AS release
  ON release.id = selected.template_release_id
 AND release.content_hash = selected.template_release_content_hash
JOIN template_release_policies AS policy
  ON policy.template_release_id = release.id
 AND policy.release_content_hash = release.content_hash
 AND policy.state = 'approved'
WHERE selected.contract_id = ?
ORDER BY selected.ordinal
`, row.FullStackTemplateID, row.FullStackTemplateHash, row.BuildContractID).Scan(&rows)
	if result.Error != nil {
		return nil, fmt.Errorf("%w: load exact approved TemplateReleases: %v", ErrCanonicalPlanningBlocked, result.Error)
	}
	if len(rows) != len(contractRefs) || len(rows) != row.ContractTemplateCount {
		return nil, fmt.Errorf("%w: TemplateRelease projection count drifted", ErrCanonicalPlanningDrift)
	}
	expected := make(map[string]constructor.TemplateReleaseRef, len(contractRefs))
	for _, reference := range contractRefs {
		expected[reference.ID] = reference
	}
	resolved := make([]ResolvedTemplateRelease, 0, len(rows))
	for index, projection := range rows {
		reference, exists := expected[projection.ReleaseID]
		if !exists || projection.Ordinal != index || reference.Role != projection.Role ||
			normalizePlanningSHA(reference.ReleaseHash) != projection.ContentHash ||
			reference.Certification != "approved" || reference.PolicyStatus != "active" {
			return nil, fmt.Errorf("%w: TemplateRelease %s drifted", ErrCanonicalPlanningDrift, projection.ReleaseID)
		}
		var manifest templates.TemplateManifest
		if err := decodePlanningJSON(projection.Manifest, &manifest); err != nil {
			return nil, fmt.Errorf("%w: decode TemplateRelease %s manifest: %v", ErrCanonicalPlanningDrift, projection.ReleaseID, err)
		}
		resolved = append(resolved, ResolvedTemplateRelease{
			Role: projection.Role, MountPath: projection.MountPath,
			Release:     repository.ExactReference{ID: projection.ReleaseID, ContentHash: projection.ContentHash},
			SubjectHash: projection.SubjectHash, Manifest: manifest,
		})
	}
	return resolved, nil
}

type canonicalPlanTemplateRow struct {
	Ordinal     int             `gorm:"column:ordinal"`
	Role        string          `gorm:"column:role"`
	MountPath   string          `gorm:"column:mount_path"`
	ReleaseID   string          `gorm:"column:template_release_id"`
	ContentHash string          `gorm:"column:template_release_content_hash"`
	SubjectHash string          `gorm:"column:subject_hash"`
	Manifest    json.RawMessage `gorm:"column:manifest"`
}
