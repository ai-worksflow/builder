package templates

import (
	"encoding/json"
	"strings"
	"time"
)

func NewReleasePolicy(release TemplateRelease, actorID string, now time.Time) (ReleasePolicy, error) {
	if err := validateReleaseDocument(release.document); err != nil {
		return ReleasePolicy{}, err
	}
	if err := validateUUID(actorID, "updatedBy"); err != nil {
		return ReleasePolicy{}, err
	}
	if now.IsZero() || now.Before(release.document.ApprovedAt) {
		return ReleasePolicy{}, invalid("invalid_time", "createdAt", "must not be zero or predate release approval")
	}
	now = now.UTC()
	schemaVersion := ReleasePolicySchemaVersion
	var authorityReceipt *ArtifactAuthorityReceiptRef
	if release.document.SchemaVersion == TemplateReleaseSchemaVersionV2 {
		schemaVersion = ReleasePolicySchemaVersionV2
		if release.document.AuthorityReceipt == nil {
			return ReleasePolicy{}, invalid("authority_receipt_required", "authorityReceipt", "v2 policy requires the release authority receipt")
		}
		value := *release.document.AuthorityReceipt
		authorityReceipt = &value
	}
	return ReleasePolicy{
		SchemaVersion:      schemaVersion,
		TemplateReleaseID:  release.ID(),
		ReleaseContentHash: release.ContentHash(),
		AuthorityReceipt:   authorityReceipt,
		State:              ReleaseApproved,
		Version:            1,
		Reason:             "all required admission gates passed",
		UpdatedBy:          strings.TrimSpace(actorID),
		CreatedAt:          now,
		UpdatedAt:          now,
	}, nil
}

// Transition keeps registry selection policy independent of immutable release
// content. Revocation is terminal; template-release/v1 does not silently
// reactivate deprecated content.
func (p ReleasePolicy) Transition(expectedVersion uint64, next ReleasePolicyState, reason, actorID string, now time.Time) (ReleasePolicy, error) {
	if expectedVersion != p.Version {
		return ReleasePolicy{}, invalid("policy_version_conflict", "version", "expected version does not match current policy")
	}
	if err := validateUUID(p.TemplateReleaseID, "templateReleaseId"); err != nil {
		return ReleasePolicy{}, err
	}
	if err := validateDigest(p.ReleaseContentHash, "releaseContentHash"); err != nil {
		return ReleasePolicy{}, err
	}
	if err := validateUUID(actorID, "updatedBy"); err != nil {
		return ReleasePolicy{}, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 2000 {
		return ReleasePolicy{}, invalid("policy_reason_required", "reason", "deprecation and revocation require a bounded reason")
	}
	if now.IsZero() || now.Before(p.UpdatedAt) {
		return ReleasePolicy{}, invalid("invalid_time", "updatedAt", "must not be zero or move backwards")
	}
	if p.State != ReleaseApproved || (next != ReleaseDeprecated && next != ReleaseRevoked) {
		return ReleasePolicy{}, transition("releasePolicy", p.State, next)
	}
	nextPolicy := p
	nextPolicy.State = next
	nextPolicy.Version++
	nextPolicy.Reason = reason
	nextPolicy.UpdatedBy = strings.TrimSpace(actorID)
	nextPolicy.UpdatedAt = now.UTC()
	return nextPolicy, nil
}

func (p ReleasePolicy) AllowsNewProjects() bool {
	return p.SchemaVersion == ReleasePolicySchemaVersionV2 && p.AuthorityReceipt != nil && p.State == ReleaseApproved
}

func (p ReleasePolicy) AllowsBuilds() bool {
	return p.SchemaVersion == ReleasePolicySchemaVersionV2 && p.AuthorityReceipt != nil &&
		(p.State == ReleaseApproved || p.State == ReleaseDeprecated)
}

func ParseReleasePolicy(encoded []byte) (ReleasePolicy, error) {
	var policy ReleasePolicy
	if err := decodeStrictJSON(encoded, &policy); err != nil {
		return ReleasePolicy{}, invalid("invalid_release_policy_json", "releasePolicy", err.Error())
	}
	policy.SchemaVersion = strings.TrimSpace(policy.SchemaVersion)
	policy.TemplateReleaseID = strings.TrimSpace(policy.TemplateReleaseID)
	policy.ReleaseContentHash = strings.TrimSpace(policy.ReleaseContentHash)
	policy.Reason = strings.TrimSpace(policy.Reason)
	policy.UpdatedBy = strings.TrimSpace(policy.UpdatedBy)
	if policy.AuthorityReceipt != nil {
		policy.AuthorityReceipt.ID = strings.TrimSpace(policy.AuthorityReceipt.ID)
		policy.AuthorityReceipt.ContentHash = strings.TrimSpace(policy.AuthorityReceipt.ContentHash)
		policy.AuthorityReceipt.PolicyHash = strings.TrimSpace(policy.AuthorityReceipt.PolicyHash)
	}
	policy.CreatedAt = policy.CreatedAt.UTC()
	policy.UpdatedAt = policy.UpdatedAt.UTC()
	if err := policy.Validate(); err != nil {
		return ReleasePolicy{}, err
	}
	canonical, err := json.Marshal(policy)
	if err != nil {
		return ReleasePolicy{}, invalid("invalid_release_policy_json", "releasePolicy", err.Error())
	}
	var original ReleasePolicy
	if err := json.Unmarshal(encoded, &original); err != nil {
		return ReleasePolicy{}, invalid("invalid_release_policy_json", "releasePolicy", err.Error())
	}
	originalCanonical, err := json.Marshal(original)
	if err != nil || string(canonical) != string(originalCanonical) {
		return ReleasePolicy{}, invalid("noncanonical_release_policy", "releasePolicy", "must use normalized values and UTC timestamps")
	}
	return policy, nil
}

func (p ReleasePolicy) Validate() error {
	switch p.SchemaVersion {
	case ReleasePolicySchemaVersion:
		if p.AuthorityReceipt != nil {
			return invalid("unexpected_authority_receipt", "authorityReceipt", "template-release-policy/v1 cannot bind an authority receipt")
		}
	case ReleasePolicySchemaVersionV2:
		if p.AuthorityReceipt == nil {
			return invalid("authority_receipt_required", "authorityReceipt", "template-release-policy/v2 requires an exact authority receipt")
		}
		if err := validateArtifactAuthorityReceiptRef(*p.AuthorityReceipt); err != nil {
			return err
		}
	default:
		return &Error{Kind: ErrUnsupportedSchema, Code: "unsupported_release_policy_schema", Field: "schemaVersion", Detail: "must be template-release-policy/v1 or template-release-policy/v2"}
	}
	if err := validateUUID(p.TemplateReleaseID, "templateReleaseId"); err != nil {
		return err
	}
	if err := validateDigest(p.ReleaseContentHash, "releaseContentHash"); err != nil {
		return err
	}
	if p.State != ReleaseApproved && p.State != ReleaseDeprecated && p.State != ReleaseRevoked {
		return invalid("invalid_release_policy_state", "state", "must be approved, deprecated, or revoked")
	}
	if p.Version == 0 {
		return invalid("invalid_release_policy_version", "version", "must be positive")
	}
	if p.Version > 2 {
		return invalid("invalid_release_policy_version", "version", "template-release/v1 policies permit only the initial state and one terminal transition")
	}
	if p.Version == 1 && p.State != ReleaseApproved {
		return invalid("invalid_release_policy_state", "state", "version 1 must be approved")
	}
	if p.Version > 1 && p.State == ReleaseApproved {
		return invalid("invalid_release_policy_state", "state", "an approved policy cannot have a post-transition version")
	}
	if strings.TrimSpace(p.Reason) == "" || len(p.Reason) > 2000 {
		return invalid("policy_reason_required", "reason", "must be non-empty and at most 2000 bytes")
	}
	if err := validateUUID(p.UpdatedBy, "updatedBy"); err != nil {
		return err
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() || p.UpdatedAt.Before(p.CreatedAt) {
		return invalid("invalid_time", "releasePolicy", "timestamps must be non-zero and cannot move backwards")
	}
	return nil
}
