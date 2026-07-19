package verification

import (
	"context"
	"encoding/json"
)

type ProfileSummary struct {
	VerificationProfile    ProfileReference `json:"verificationProfile"`
	SupportedTemplateRoles []string         `json:"supportedTemplateRoles"`
}

func (store *PostgresStore) ListActiveCanonicalProfiles(
	ctx context.Context,
	projectID string,
	subject CanonicalPlanSubject,
) ([]ProfileSummary, error) {
	if !validUUIDs(projectID) {
		return nil, runInvalid("Canonical VerificationProfile catalog project")
	}
	if normalized, err := normalizeCanonicalPlanSubject(subject); err != nil || normalized != subject {
		return nil, runInvalid("Canonical VerificationProfile WorkspaceRevision")
	}
	var rows []activeProfileRow
	err := store.database.WithContext(ctx).
		Table("verification_profile_versions AS versions").
		Select("versions.profile_id, versions.version, versions.content_hash, versions.document").
		Joins("JOIN verification_profile_policies AS policies ON policies.profile_id = versions.profile_id AND policies.profile_version = versions.version AND policies.profile_hash = versions.content_hash AND policies.state = 'active'").
		Joins("JOIN artifact_revisions AS revision ON revision.id = ? AND revision.artifact_id = ? AND revision.content_hash = ? AND revision.workflow_status = 'approved'", subject.WorkspaceRevisionID, subject.WorkspaceArtifactID, subject.WorkspaceContentHash).
		Joins("JOIN artifacts AS artifact ON artifact.id = revision.artifact_id AND artifact.project_id = ? AND artifact.kind = 'workspace'", projectID).
		Joins("JOIN implementation_proposals AS proposal ON proposal.id = revision.implementation_proposal_id AND proposal.project_id = artifact.project_id AND proposal.status IN ('applied','partially_applied') AND proposal.applied_at IS NOT NULL").
		Joins("JOIN application_build_contracts AS contract ON contract.id = proposal.application_build_contract_id AND contract.project_id = artifact.project_id AND contract.status = 'ready' AND contract.must_ready_count = contract.must_count AND contract.blocking_count = 0 AND contract.conflict_count = 0").
		Where(`versions.document->'builtInChecks' @> '[{"id":"release-artifacts","kind":"release-manifest","required":true}]'::jsonb`).
		Where(`versions.document->'builtInChecks' @> '[{"id":"release-sbom","kind":"sbom","required":true}]'::jsonb`).
		Where(`versions.document->'builtInChecks' @> '[{"id":"release-vulnerability","kind":"vulnerability","required":true}]'::jsonb`).
		Where(`versions.document->'builtInChecks' @> '[{"id":"release-container-policy","kind":"container-policy","required":true}]'::jsonb`).
		Where(`NOT EXISTS (
  SELECT 1
  FROM application_build_contract_template_releases AS releases
	  WHERE releases.contract_id = contract.id
    AND NOT jsonb_exists(versions.document->'supportedTemplateRoles', releases.role)
)`).
		Order("versions.profile_id ASC, versions.version DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, mapRunStoreError("list active Canonical VerificationProfiles", err)
	}
	return decodeActiveProfileRows(rows)
}

type activeProfileRow struct {
	ID          string          `gorm:"column:profile_id"`
	Version     int64           `gorm:"column:version"`
	ContentHash string          `gorm:"column:content_hash"`
	Document    json.RawMessage `gorm:"column:document"`
}

func (store *PostgresStore) ListActiveProfiles(
	ctx context.Context,
	projectID, sessionID string,
) ([]ProfileSummary, error) {
	if !validUUIDs(projectID, sessionID) {
		return nil, runInvalid("VerificationProfile catalog project or SandboxSession")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var rows []activeProfileRow
	err := store.database.WithContext(ctx).
		Table("verification_profile_versions AS versions").
		Select("versions.profile_id, versions.version, versions.content_hash, versions.document").
		Joins("JOIN sandbox_sessions AS sessions ON sessions.id = ? AND sessions.project_id = ?", sessionID, projectID).
		Joins("JOIN verification_profile_policies AS policies ON policies.profile_id = versions.profile_id AND policies.profile_version = versions.version AND policies.profile_hash = versions.content_hash").
		Where("policies.state = ?", "active").
		Where(`NOT EXISTS (
  SELECT 1
  FROM application_build_contract_template_releases AS releases
  WHERE releases.contract_id = sessions.build_contract_id
    AND NOT jsonb_exists(versions.document->'supportedTemplateRoles', releases.role)
)`).
		Order("versions.profile_id ASC, versions.version DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, mapRunStoreError("list active VerificationProfiles", err)
	}

	return decodeActiveProfileRows(rows)
}

func decodeActiveProfileRows(rows []activeProfileRow) ([]ProfileSummary, error) {
	profiles := make([]ProfileSummary, 0, len(rows))
	for _, row := range rows {
		var document VerificationProfileDocument
		if err := json.Unmarshal(row.Document, &document); err != nil {
			return nil, runIntegrity("decode active VerificationProfile", err)
		}
		normalized, _, err := normalizeVerificationProfile(document)
		if err != nil || normalized.ID != row.ID || normalized.Version != uint64(row.Version) ||
			normalized.ProfileHash != row.ContentHash {
			return nil, runIntegrity("active VerificationProfile projection", err)
		}
		profiles = append(profiles, ProfileSummary{
			VerificationProfile: ProfileReference{
				ID: normalized.ID, Version: normalized.Version, ContentHash: normalized.ProfileHash,
			},
			SupportedTemplateRoles: append([]string(nil), normalized.SupportedTemplateRoles...),
		})
	}
	return profiles, nil
}
