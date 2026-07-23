package qualificationpromotionv2

func cloneSlice[T any](source []T) []T {
	if source == nil {
		return nil
	}
	clone := make([]T, len(source))
	copy(clone, source)
	return clone
}

func clonePrepared(prepared PreparedAuthority) PreparedAuthority {
	clone := prepared
	clone.EvidenceEventSet.Events = cloneSlice(prepared.EvidenceEventSet.Events)
	clone.IndependentRequirements = cloneSlice(prepared.IndependentRequirements)
	clone.PlanReceiptLineage.EvidencePlan.Plan.Artifacts = cloneSlice(prepared.PlanReceiptLineage.EvidencePlan.Plan.Artifacts)
	clone.PlanReceiptLineage.EvidencePlan.Receipt.Artifacts = cloneSlice(prepared.PlanReceiptLineage.EvidencePlan.Receipt.Artifacts)
	return clone
}

func cloneRecord(record Record) Record {
	clone := record
	clone.RequestBytes = append([]byte(nil), record.RequestBytes...)
	clone.EvidenceEventSetBytes = append([]byte(nil), record.EvidenceEventSetBytes...)
	clone.ClosureBytes = append([]byte(nil), record.ClosureBytes...)
	clone.RevisionIntentBytes = append([]byte(nil), record.RevisionIntentBytes...)
	clone.ConsumptionBytes = append([]byte(nil), record.ConsumptionBytes...)
	clone.HandoffBytes = append([]byte(nil), record.HandoffBytes...)
	clone.EvidenceEventSet.Events = cloneSlice(record.EvidenceEventSet.Events)
	clone.Closure.IndependentAuthorities = cloneSlice(record.Closure.IndependentAuthorities)
	return clone
}

// CloneRecord returns a deep copy suitable for crossing a store boundary.
func CloneRecord(record Record) Record {
	return cloneRecord(record)
}

// SameImmutableRecord compares the complete durable aggregate while ignoring
// response-only idempotency metadata.
func SameImmutableRecord(left, right Record) bool {
	return sameImmutableRecord(left, right)
}
