package qualificationpolicyauthority

func cloneRevisionPolicy(policy RevisionPolicy) RevisionPolicy {
	clone := policy
	clone.ExactApprovedSources = append([]ExactApprovedSource(nil), policy.ExactApprovedSources...)
	if policy.ExactApprovedSources != nil && clone.ExactApprovedSources == nil {
		clone.ExactApprovedSources = make([]ExactApprovedSource, 0)
	}
	clone.ReviewByChangeSource = append([]ChangeSourceReviewRule(nil), policy.ReviewByChangeSource...)
	if policy.ReviewByChangeSource != nil && clone.ReviewByChangeSource == nil {
		clone.ReviewByChangeSource = make([]ChangeSourceReviewRule, 0)
	}
	return clone
}

func clonePlanInputProfile(profile PlanInputProfile) PlanInputProfile {
	clone := profile
	clone.Artifacts = append([]ArtifactExpectation(nil), profile.Artifacts...)
	if profile.Artifacts != nil && clone.Artifacts == nil {
		clone.Artifacts = make([]ArtifactExpectation, 0)
	}
	return clone
}

func clonePromotionPolicy(policy PromotionPolicy) PromotionPolicy {
	clone := policy
	clone.IndependentRequirements = append([]IndependentAuthorityBinding(nil), policy.IndependentRequirements...)
	if policy.IndependentRequirements != nil && clone.IndependentRequirements == nil {
		clone.IndependentRequirements = make([]IndependentAuthorityBinding, 0)
	}
	return clone
}

func cloneResolvedPolicy(policy ResolvedPolicy) ResolvedPolicy {
	clone := policy
	clone.RevisionPolicy = cloneRevisionPolicy(policy.RevisionPolicy)
	clone.PlanInputProfile = clonePlanInputProfile(policy.PlanInputProfile)
	clone.PromotionPolicy = clonePromotionPolicy(policy.PromotionPolicy)
	return clone
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneAuthorityDocument(document AuthorityDocument) AuthorityDocument {
	clone := document
	clone.PreviousAuthorityHash = cloneStringPointer(document.PreviousAuthorityHash)
	clone.RevisionPolicy = cloneRevisionPolicy(document.RevisionPolicy)
	clone.PlanInputProfile = clonePlanInputProfile(document.PlanInputProfile)
	clone.PromotionPolicy = clonePromotionPolicy(document.PromotionPolicy)
	return clone
}

func cloneRecord(record Record) Record {
	clone := record
	clone.RevisionPolicy = cloneRevisionPolicy(record.RevisionPolicy)
	clone.RevisionPolicyBytes = append([]byte(nil), record.RevisionPolicyBytes...)
	clone.PlanInputProfile = clonePlanInputProfile(record.PlanInputProfile)
	clone.PlanInputProfileBytes = append([]byte(nil), record.PlanInputProfileBytes...)
	clone.PromotionPolicy = clonePromotionPolicy(record.PromotionPolicy)
	clone.PromotionPolicyBytes = append([]byte(nil), record.PromotionPolicyBytes...)
	clone.Document = cloneAuthorityDocument(record.Document)
	clone.DocumentBytes = append([]byte(nil), record.DocumentBytes...)
	return clone
}

func idempotentClone(record Record) Record {
	clone := cloneRecord(record)
	clone.Idempotent = true
	return clone
}
