package workflowinputauthority

import "bytes"

func cloneCandidateDocument(document FreezeCandidateDocument) FreezeCandidateDocument {
	clone := document
	clone.InputManifests = cloneSlice(document.InputManifests)
	clone.ReviewRequirements = cloneSlice(document.ReviewRequirements)
	clone.Revisions = cloneSlice(document.Revisions)
	return clone
}

func cloneInput(input WorkflowInputDocument) WorkflowInputDocument {
	clone := input
	clone.InputManifests = cloneSlice(input.InputManifests)
	clone.ReviewReceipts = cloneSlice(input.ReviewReceipts)
	clone.Revisions = cloneSlice(input.Revisions)
	for index := range clone.Revisions {
		clone.Revisions[index].SourceManifestID = cloneStringPointer(input.Revisions[index].SourceManifestID)
		clone.Revisions[index].ProposalID = cloneStringPointer(input.Revisions[index].ProposalID)
		clone.Revisions[index].ImplementationProposalID = cloneStringPointer(input.Revisions[index].ImplementationProposalID)
	}
	clone.Predecessors = make([]PredecessorBinding, len(input.Predecessors))
	for index, predecessor := range input.Predecessors {
		copyPredecessor := predecessor
		copyPredecessor.InputManifest = cloneManifestReference(predecessor.InputManifest)
		copyPredecessor.OutputProposal = cloneProposalReference(predecessor.OutputProposal)
		copyPredecessor.ArtifactRevisions = cloneSlice(predecessor.ArtifactRevisions)
		copyPredecessor.MaterializedArtifactRevisions = cloneSlice(predecessor.MaterializedArtifactRevisions)
		copyPredecessor.ProposalPins = cloneSlice(predecessor.ProposalPins)
		copyPredecessor.DeliverySliceRefs = cloneSlice(predecessor.DeliverySliceRefs)
		for sliceIndex, reference := range predecessor.DeliverySliceRefs {
			copyReference := reference
			copyReference.Blueprint = cloneArtifactReference(reference.Blueprint)
			copyReference.PageSpec = cloneArtifactReference(reference.PageSpec)
			copyReference.Prototype = cloneArtifactReference(reference.Prototype)
			copyPredecessor.DeliverySliceRefs[sliceIndex] = copyReference
		}
		clone.Predecessors[index] = copyPredecessor
	}
	return clone
}

func cloneSlice[T any](source []T) []T {
	if source == nil {
		return nil
	}
	clone := make([]T, len(source))
	copy(clone, source)
	return clone
}

func cloneArtifactReference(reference *ArtifactRevisionReference) *ArtifactRevisionReference {
	if reference == nil {
		return nil
	}
	clone := *reference
	return &clone
}

func cloneManifestReference(reference *ManifestReference) *ManifestReference {
	if reference == nil {
		return nil
	}
	clone := *reference
	return &clone
}

func cloneProposalReference(reference *ProposalReference) *ProposalReference {
	if reference == nil {
		return nil
	}
	clone := *reference
	return &clone
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneMaterials(materials RetainedMaterials) RetainedMaterials {
	clone := RetainedMaterials{
		BuildContract: append([]byte(nil), materials.BuildContract...),
		BuildManifest: append([]byte(nil), materials.BuildManifest...),
		Definition:    append([]byte(nil), materials.Definition...),
		NodeInput:     append([]byte(nil), materials.NodeInput...),
		RunScope:      append([]byte(nil), materials.RunScope...),
	}
	clone.InputManifests = make([]InputManifestMaterial, len(materials.InputManifests))
	for index, material := range materials.InputManifests {
		clone.InputManifests[index] = material
		clone.InputManifests[index].Bytes = append([]byte(nil), material.Bytes...)
	}
	clone.Revisions = make([]RevisionMaterial, len(materials.Revisions))
	for index, material := range materials.Revisions {
		clone.Revisions[index] = material
		clone.Revisions[index].Bytes = append([]byte(nil), material.Bytes...)
	}
	clone.ReviewReceipts = make([]ReviewReceiptMaterial, len(materials.ReviewReceipts))
	for index, material := range materials.ReviewReceipts {
		clone.ReviewReceipts[index] = material
		clone.ReviewReceipts[index].Bytes = append([]byte(nil), material.Bytes...)
	}
	return clone
}

func cloneRecord(record Record) Record {
	clone := record
	clone.RequestBytes = append([]byte(nil), record.RequestBytes...)
	clone.TargetBytes = append([]byte(nil), record.TargetBytes...)
	clone.InputBytes = append([]byte(nil), record.InputBytes...)
	clone.EnvelopeBytes = append([]byte(nil), record.EnvelopeBytes...)
	clone.Input = cloneInput(record.Input)
	clone.Materials = cloneMaterials(record.Materials)
	return clone
}

func sameImmutableRecord(left, right Record) bool {
	return left.OperationID == right.OperationID && left.AuthorityID == right.AuthorityID &&
		left.WorkflowRunID == right.WorkflowRunID && left.NodeRunID == right.NodeRunID &&
		left.RequestHash == right.RequestHash && left.TargetHash == right.TargetHash &&
		left.InputHash == right.InputHash && left.AuthorityHash == right.AuthorityHash &&
		bytes.Equal(left.RequestBytes, right.RequestBytes) && bytes.Equal(left.TargetBytes, right.TargetBytes) &&
		bytes.Equal(left.InputBytes, right.InputBytes) && bytes.Equal(left.EnvelopeBytes, right.EnvelopeBytes) &&
		equalMaterials(left.Materials, right.Materials)
}

func equalMaterials(left, right RetainedMaterials) bool {
	if !bytes.Equal(left.Definition, right.Definition) || !bytes.Equal(left.RunScope, right.RunScope) ||
		!bytes.Equal(left.NodeInput, right.NodeInput) || !bytes.Equal(left.BuildManifest, right.BuildManifest) ||
		!bytes.Equal(left.BuildContract, right.BuildContract) || len(left.InputManifests) != len(right.InputManifests) ||
		len(left.Revisions) != len(right.Revisions) || len(left.ReviewReceipts) != len(right.ReviewReceipts) {
		return false
	}
	for index := range left.InputManifests {
		if left.InputManifests[index].ManifestID != right.InputManifests[index].ManifestID ||
			left.InputManifests[index].Role != right.InputManifests[index].Role ||
			!bytes.Equal(left.InputManifests[index].Bytes, right.InputManifests[index].Bytes) {
			return false
		}
	}
	for index := range left.Revisions {
		if left.Revisions[index].RevisionID != right.Revisions[index].RevisionID ||
			left.Revisions[index].Purpose != right.Revisions[index].Purpose ||
			!bytes.Equal(left.Revisions[index].Bytes, right.Revisions[index].Bytes) {
			return false
		}
	}
	for index := range left.ReviewReceipts {
		if left.ReviewReceipts[index].ReviewRequestID != right.ReviewReceipts[index].ReviewRequestID ||
			!bytes.Equal(left.ReviewReceipts[index].Bytes, right.ReviewReceipts[index].Bytes) {
			return false
		}
	}
	return true
}
