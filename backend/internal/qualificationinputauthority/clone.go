package qualificationinputauthority

func cloneResolvedAuthorities(value ResolvedAuthorities) ResolvedAuthorities {
	return value
}

func cloneReceiptAdmission(value ReceiptAdmissionRecord) ReceiptAdmissionRecord {
	cloned := value
	cloned.DocumentBytes = append([]byte(nil), value.DocumentBytes...)
	cloned.RequestBytes = append([]byte(nil), value.RequestBytes...)
	return cloned
}

func cloneRecord(value Record) Record {
	cloned := value
	cloned.RequestBytes = append([]byte(nil), value.RequestBytes...)
	cloned.SourceRequestBytes = append([]byte(nil), value.SourceRequestBytes...)
	cloned.CredentialRequestBytes = append([]byte(nil), value.CredentialRequestBytes...)
	cloned.DocumentBytes = append([]byte(nil), value.DocumentBytes...)
	return cloned
}
