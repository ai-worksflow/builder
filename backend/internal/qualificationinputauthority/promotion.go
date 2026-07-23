package qualificationinputauthority

// PromotionBindingFromRecord returns the fixed, closed projection that a
// later Promotion canonical closure must include. Looking up this record from
// a junction table or checking it only in a trigger is not equivalent: these
// exact members must be inside the closure bytes and closure hash.
func PromotionBindingFromRecord(record Record) (PromotionBinding, error) {
	if err := ValidateRecord(record); err != nil {
		return PromotionBinding{}, err
	}
	binding := PromotionBinding{
		AuthorityHash:                    record.AuthorityHash,
		AuthorityID:                      record.Document.AuthorityID,
		CredentialAdmissionHash:          record.Document.CredentialProof.AdmissionHash,
		CredentialReceiptHash:            record.Document.CredentialProof.ReceiptHash,
		CredentialRequestHash:            record.CredentialRequestHash,
		Kind:                             PromotionBindingKindV1,
		QualificationPlanAuthorityHash:   record.Document.Plan.AuthorityHash,
		QualificationPlanAuthorityID:     record.Document.Plan.AuthorityID,
		QualificationPolicyAuthorityHash: record.Document.Policy.AuthorityHash,
		QualificationPolicyAuthorityID:   record.Document.Policy.AuthorityID,
		SourceAdmissionHash:              record.Document.SourceProof.AdmissionHash,
		SourceReceiptHash:                record.Document.SourceProof.ReceiptHash,
		SourceRequestHash:                record.SourceRequestHash,
		WorkflowInputAuthorityHash:       record.Document.WorkflowInput.AuthorityHash,
		WorkflowInputAuthorityID:         record.Document.WorkflowInput.AuthorityID,
	}
	if err := ValidatePromotionBinding(binding); err != nil {
		return PromotionBinding{}, err
	}
	return binding, nil
}

func ValidatePromotionBinding(binding PromotionBinding) error {
	if binding.Kind != PromotionBindingKindV1 || !validUUIDv4(binding.AuthorityID) || !validDigest(binding.AuthorityHash) ||
		!validUUIDv4(binding.WorkflowInputAuthorityID) || !validDigest(binding.WorkflowInputAuthorityHash) ||
		!validUUIDv4(binding.QualificationPolicyAuthorityID) || !validDigest(binding.QualificationPolicyAuthorityHash) ||
		!validUUIDv4(binding.QualificationPlanAuthorityID) || !validDigest(binding.QualificationPlanAuthorityHash) ||
		!validDigest(binding.SourceRequestHash) || !validDigest(binding.SourceReceiptHash) ||
		!validDigest(binding.SourceAdmissionHash) || !validDigest(binding.CredentialRequestHash) ||
		!validDigest(binding.CredentialReceiptHash) || !validDigest(binding.CredentialAdmissionHash) {
		return invalid("promotionBinding", "kind, authority identities, or hashes are invalid")
	}
	if !uniqueStrings([]string{
		binding.AuthorityID,
		binding.WorkflowInputAuthorityID,
		binding.QualificationPolicyAuthorityID,
		binding.QualificationPlanAuthorityID,
	}) {
		return invalid("promotionBinding", "authority identities must be pairwise distinct")
	}
	if !uniqueStrings([]string{
		binding.AuthorityHash,
		binding.WorkflowInputAuthorityHash,
		binding.QualificationPolicyAuthorityHash,
		binding.QualificationPlanAuthorityHash,
	}) {
		return invalid("promotionBinding", "authority hash domains must be pairwise distinct")
	}
	if !uniqueStrings([]string{
		binding.SourceRequestHash,
		binding.SourceReceiptHash,
		binding.SourceAdmissionHash,
		binding.CredentialRequestHash,
		binding.CredentialReceiptHash,
		binding.CredentialAdmissionHash,
	}) {
		return invalid("promotionBinding", "source and credential proof domains must not alias")
	}
	return nil
}
