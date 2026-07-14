package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/worksflow/builder/backend/internal/domain"
)

// buildNodeInputEnvelope materializes the exact enabled edge transfers for one
// node attempt. The result is canonical and hash-bound before any runner code
// is invoked.
func buildNodeInputEnvelope(run *RunRecord, definition domain.WorkflowDefinition, target *NodeRecord) (domain.NodeInputEnvelope, error) {
	if run == nil || target == nil {
		return domain.NodeInputEnvelope{}, fmt.Errorf("workflow run and target node are required")
	}
	targetDefinition, exists := definition.FindNode(target.DefinitionNodeID)
	if !exists {
		return domain.NodeInputEnvelope{}, fmt.Errorf("definition node %q missing", target.DefinitionNodeID)
	}
	edges := definition.Incoming(target.DefinitionNodeID)
	sort.Slice(edges, func(left, right int) bool { return edges[left].ID < edges[right].ID })
	bindings := make([]domain.NodeInputBinding, 0, len(edges))
	requiredPorts := map[string]bool{}
	for _, edge := range edges {
		if run.Context.DisabledEdges[disabledEdgeKey(edge.ID, target.SliceID)] {
			continue
		}
		sources := incomingSourcesForEdge(run, targetDefinition, target, edge)
		before := len(bindings)
		for _, source := range sources {
			sliceID := target.SliceID
			if source.SliceID != "" {
				sliceID = source.SliceID
			}
			if run.Context.DisabledEdges[disabledEdgeKey(edge.ID, sliceID)] || source.Status == NodeCancelled {
				continue
			}
			if source.Status != NodeCompleted {
				if targetDefinition.Type == domain.NodeMerge && targetDefinition.Merge != nil && targetDefinition.Merge.Policy != domain.MergeAll {
					continue
				}
				return domain.NodeInputEnvelope{}, &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "input." + edge.ID, Message: fmt.Sprintf("predecessor %q is %s, not completed", source.Key, source.Status)}
			}
			output, reference, err := predecessorOutputSnapshot(run, definition, source, target, edge)
			if err != nil {
				return domain.NodeInputEnvelope{}, fmt.Errorf("edge %s from %s: %w", edge.ID, source.Key, err)
			}
			reference = pruneReferenceForSemanticTarget(run, definition, targetDefinition, reference)
			value, err := applyEdgeMapping(output, edge.Mapping)
			if err != nil {
				return domain.NodeInputEnvelope{}, fmt.Errorf("edge %s mapping: %w", edge.ID, err)
			}
			bindings = append(bindings, domain.NodeInputBinding{
				EdgeID: edge.ID, FromPort: normalizedPort(edge.FromPort), ToPort: normalizedPort(edge.ToPort),
				Mapping: edge.Mapping, Source: reference, Output: output, Value: value,
			})
		}
		if len(bindings) > before {
			requiredPorts[normalizedPort(edge.ToPort)] = true
		}
	}
	if len(edges) > 0 && len(bindings) == 0 {
		return domain.NodeInputEnvelope{}, &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "input", Message: "enabled incoming edges produced no inputs"}
	}
	envelope, err := domain.NewNodeInputEnvelope(bindings)
	if err != nil {
		return domain.NodeInputEnvelope{}, err
	}
	if err := validateNodeInput(targetDefinition, envelope, requiredPorts); err != nil {
		return domain.NodeInputEnvelope{}, err
	}
	return envelope, nil
}

func incomingSourcesForEdge(run *RunRecord, targetDefinition domain.NodeDefinition, target *NodeRecord, edge domain.WorkflowEdge) []*NodeRecord {
	if target.SliceID != "" {
		if dynamic := run.Nodes[instanceKey(edge.From, target.SliceID)]; dynamic != nil {
			return []*NodeRecord{dynamic}
		}
		if static := run.Nodes[edge.From]; static != nil {
			return []*NodeRecord{static}
		}
		return nil
	}
	if static := run.Nodes[edge.From]; static != nil {
		return []*NodeRecord{static}
	}
	if targetDefinition.Type != domain.NodeMerge || targetDefinition.Merge == nil {
		return nil
	}
	sources := make([]*NodeRecord, 0)
	for _, slice := range slicesForFanOut(run.Context, targetDefinition.Merge.FanOutNodeID) {
		if source := run.Nodes[instanceKey(edge.From, slice.ID)]; source != nil {
			sources = append(sources, source)
		}
	}
	sort.Slice(sources, func(left, right int) bool {
		leftSlice, rightSlice := run.Context.Slices[sources[left].SliceID], run.Context.Slices[sources[right].SliceID]
		if leftSlice.Key == rightSlice.Key {
			return sources[left].Key < sources[right].Key
		}
		return leftSlice.Key < rightSlice.Key
	})
	return sources
}

func predecessorOutputSnapshot(run *RunRecord, definition domain.WorkflowDefinition, source, target *NodeRecord, edge domain.WorkflowEdge) (json.RawMessage, domain.NodeOutputReference, error) {
	sourceDefinition, exists := definition.FindNode(source.DefinitionNodeID)
	if !exists {
		return nil, domain.NodeOutputReference{}, fmt.Errorf("source definition %q missing", source.DefinitionNodeID)
	}
	ports, err := sourceDefinition.ResolvedOutputPorts()
	if err != nil {
		return nil, domain.NodeOutputReference{}, err
	}
	fromPort := normalizedPort(edge.FromPort)
	port, exists := ports[fromPort]
	if !exists {
		return nil, domain.NodeOutputReference{}, fmt.Errorf("output port %q is not declared", fromPort)
	}
	metadata := run.Context.Nodes[source.Key]
	storedInputs, hasStoredInputs, err := decodeStoredInputs(metadata.Input)
	if err != nil {
		return nil, domain.NodeOutputReference{}, err
	}
	var output json.RawMessage
	if sourceDefinition.Type == domain.NodeFanOut && target.SliceID != "" {
		item := metadata.FanOutOutputs[target.SliceID]
		if len(item) == 0 {
			return nil, domain.NodeOutputReference{}, fmt.Errorf("fan-out has no output for slice %q", target.SliceID)
		}
		output, err = fanOutPortValue(port.Schema, item)
	} else if len(metadata.Output) > 0 {
		output, err = selectPortOutput(metadata.Output, fromPort, len(ports))
	} else if hasStoredInputs {
		output, err = passthroughPortValue(port.Schema, storedInputs)
	} else {
		output, err = domain.CanonicalJSON(map[string]any{})
	}
	if err != nil {
		return nil, domain.NodeOutputReference{}, err
	}
	if err := validateAgainstSchema("output."+fromPort, port.Schema, output); err != nil {
		return nil, domain.NodeOutputReference{}, err
	}
	reference := nodeOutputReference(run, sourceDefinition, source, output, storedInputs, hasStoredInputs, target.SliceID)
	return output, reference, nil
}

func nodeOutputReference(run *RunRecord, definition domain.NodeDefinition, source *NodeRecord, output json.RawMessage, storedInputs domain.NodeInputEnvelope, hasStoredInputs bool, targetSliceID string) domain.NodeOutputReference {
	reference := domain.NodeOutputReference{
		RunID: run.ID, NodeKey: source.Key, DefinitionNodeID: source.DefinitionNodeID, SliceID: source.SliceID,
		OutputRevisionID: source.OutputRevisionID,
	}
	if source.InputManifest != nil {
		value := *source.InputManifest
		reference.InputManifest = &value
	} else if definition.Type == domain.NodeArtifactInput && run.InputManifest != nil {
		value := *run.InputManifest
		reference.InputManifest = &value
	}
	if source.OutputProposal != nil {
		value := *source.OutputProposal
		reference.OutputProposal = &value
		if reference.InputManifest != nil {
			reference.ProposalPins = appendUniqueProposalPin(reference.ProposalPins, domain.ProposalLineagePin{
				Proposal: value, Manifest: *reference.InputManifest,
				ProducerNodeKey: source.Key, ProducerDefinitionNodeID: source.DefinitionNodeID,
			})
		}
	}
	producedArtifactIDs := map[string]bool{}
	if refs, err := artifactRefsFromNodeOutput(output); err == nil {
		for _, ref := range refs {
			reference.ArtifactRevisions = appendUniqueArtifactRef(reference.ArtifactRevisions, ref)
			if definition.Type == domain.NodeHumanEdit {
				reference.MaterializedArtifactRevisions = appendUniqueArtifactRef(reference.MaterializedArtifactRevisions, ref)
			}
			producedArtifactIDs[ref.ArtifactID] = true
		}
	}
	var sourceEnvelope struct {
		Sources []domain.ArtifactRef `json:"sources"`
	}
	if json.Unmarshal(output, &sourceEnvelope) == nil {
		for _, ref := range sourceEnvelope.Sources {
			if ref.Validate() == nil {
				reference.ArtifactRevisions = appendUniqueArtifactRef(reference.ArtifactRevisions, ref)
				producedArtifactIDs[ref.ArtifactID] = true
			}
		}
	}
	if hasStoredInputs {
		if source.OutputProposal == nil && propagatesProposalLineage(definition.Type) {
			for _, pin := range storedInputs.ProposalPins() {
				reference.ProposalPins = appendUniqueProposalPin(reference.ProposalPins, pin)
			}
		}
		if propagatesMaterializedLineage(definition.Type) {
			for _, ref := range storedInputs.MaterializedArtifactRefs() {
				reference.MaterializedArtifactRevisions = appendUniqueArtifactRef(reference.MaterializedArtifactRevisions, ref)
			}
		}
		for _, ref := range storedInputs.ArtifactRefs() {
			if producedArtifactIDs[ref.ArtifactID] {
				continue
			}
			reference.ArtifactRevisions = appendUniqueArtifactRef(reference.ArtifactRevisions, ref)
		}
		for _, ref := range storedInputs.SliceRefs() {
			// Merge is the fan-out barrier. A historical merge input may have
			// been frozen before a HumanEdit materialized PageSpec or Prototype
			// fields into the same run-scoped slice. Fill only fields that were
			// absent from that immutable input; never overwrite an exact pin that
			// the merge already carried.
			if definition.Type == domain.NodeMerge {
				if current, exists := run.Context.Slices[ref.ID]; exists {
					ref = enrichSliceRef(ref, workflowSliceRef(current))
				}
			}
			reference.DeliverySliceRefs = appendUniqueSliceRef(reference.DeliverySliceRefs, ref)
		}
	}
	if invalidatesPriorDeliverySlices(definition) {
		reference = withoutPriorDeliverySliceEpoch(reference)
	}
	sliceID := source.SliceID
	if definition.Type == domain.NodeFanOut && targetSliceID != "" {
		sliceID = targetSliceID
		reference.SliceID = targetSliceID
	}
	if slice, exists := run.Context.Slices[sliceID]; exists {
		current := workflowSliceRef(slice)
		if definition.Type == domain.NodeHumanEdit {
			// HumanEdit is the operation that advances the exact PageSpec or
			// Prototype pointer. Its post-submit run slice is authoritative for
			// this output and must replace the pre-materialization input snapshot.
			reference.DeliverySliceRefs = replaceSliceRef(reference.DeliverySliceRefs, current)
		} else {
			reference.DeliverySliceRefs = appendUniqueSliceRef(reference.DeliverySliceRefs, current)
		}
		for _, ref := range sliceArtifactRefs(slice) {
			if producedArtifactIDs[ref.ArtifactID] {
				continue
			}
			reference.ArtifactRevisions = appendUniqueArtifactRef(reference.ArtifactRevisions, ref)
		}
	}
	return reference
}

// A new upstream semantic epoch cannot inherit page delivery evidence from an
// earlier epoch. The exact refs embedded by those slices are removed together
// with the slice refs; global refs that are not part of the old slice snapshot
// remain available as generation context.
func withoutPriorDeliverySliceEpoch(reference domain.NodeOutputReference) domain.NodeOutputReference {
	stale := make([]domain.ArtifactRef, 0)
	for _, slice := range reference.DeliverySliceRefs {
		for _, ref := range []*domain.ArtifactRef{slice.Blueprint, slice.PageSpec, slice.Prototype} {
			if ref != nil && ref.Validate() == nil {
				stale = appendUniqueArtifactRef(stale, *ref)
			}
		}
	}
	reference.ArtifactRevisions = filterArtifactRefs(reference.ArtifactRevisions, stale)
	reference.MaterializedArtifactRevisions = filterArtifactRefs(reference.MaterializedArtifactRevisions, stale)
	reference.DeliverySliceRefs = nil
	return reference
}

func filterArtifactRefs(refs, excluded []domain.ArtifactRef) []domain.ArtifactRef {
	filtered := make([]domain.ArtifactRef, 0, len(refs))
	for _, ref := range refs {
		drop := false
		for _, candidate := range excluded {
			if sameArtifactRevision(ref, candidate) {
				drop = true
				break
			}
		}
		if !drop {
			filtered = append(filtered, ref)
		}
	}
	return filtered
}

func sameArtifactRevision(left, right domain.ArtifactRef) bool {
	return left.ArtifactID == right.ArtifactID && left.RevisionID == right.RevisionID && left.ContentHash == right.ContentHash
}

func pruneReferenceForSemanticTarget(run *RunRecord, workflow domain.WorkflowDefinition, target domain.NodeDefinition, reference domain.NodeOutputReference) domain.NodeOutputReference {
	producedKind := semanticProducedArtifactKind(target)
	if invalidatesPriorDeliverySlices(target) {
		reference = withoutPriorDeliverySliceEpoch(reference)
	}
	if producedKind == "" {
		return reference
	}
	obsoleteKinds := map[string]bool{}
	for _, kind := range semanticDownstreamArtifactKinds(producedKind) {
		obsoleteKinds[kind] = true
	}
	kinds := runtimeArtifactKinds(run, workflow)
	filter := func(refs []domain.ArtifactRef) []domain.ArtifactRef {
		filtered := make([]domain.ArtifactRef, 0, len(refs))
		for _, ref := range refs {
			if obsoleteKinds[kinds[ref.ArtifactID]] {
				continue
			}
			filtered = append(filtered, ref)
		}
		return filtered
	}
	reference.ArtifactRevisions = filter(reference.ArtifactRevisions)
	reference.MaterializedArtifactRevisions = filter(reference.MaterializedArtifactRevisions)
	for index := range reference.DeliverySliceRefs {
		slice := &reference.DeliverySliceRefs[index]
		if obsoleteKinds["blueprint"] {
			slice.Blueprint, slice.PageSpec, slice.Prototype = nil, nil, nil
			continue
		}
		if obsoleteKinds["page_spec"] {
			slice.PageSpec, slice.Prototype = nil, nil
			continue
		}
		if obsoleteKinds["prototype"] {
			slice.Prototype = nil
		}
	}
	return reference
}

func semanticProducedArtifactKind(definition domain.NodeDefinition) string {
	if definition.Type == domain.NodeHumanEdit && definition.HumanEdit != nil {
		return definition.HumanEdit.ArtifactKind
	}
	if definition.Type != domain.NodeAITransform || definition.AITransform == nil {
		return ""
	}
	switch definition.AITransform.JobType {
	case "refine_project_brief":
		return "project_brief"
	case "derive_requirements":
		return "product_requirements"
	case "decompose_pages":
		return "blueprint"
	case "generate_page_spec":
		return "page_spec"
	case "generate_prototype":
		return "prototype"
	default:
		return ""
	}
}

func semanticDownstreamArtifactKinds(kind string) []string {
	return map[string][]string{
		"project_brief":        {"product_requirements", "requirement_baseline", "blueprint", "page_spec", "prototype"},
		"product_requirements": {"requirement_baseline", "blueprint", "page_spec", "prototype"},
		"requirement_baseline": {"blueprint", "page_spec", "prototype"},
		"blueprint":            {"page_spec", "prototype"},
		"page_spec":            {"prototype"},
	}[kind]
}

func runtimeArtifactKinds(run *RunRecord, workflow domain.WorkflowDefinition) map[string]string {
	kinds := map[string]string{}
	ambiguous := map[string]bool{}
	record := func(ref domain.ArtifactRef, kind string) {
		if ref.Validate() != nil || strings.TrimSpace(kind) == "" {
			return
		}
		if ambiguous[ref.ArtifactID] {
			return
		}
		if existing, exists := kinds[ref.ArtifactID]; !exists || existing == kind {
			kinds[ref.ArtifactID] = kind
		} else {
			// A stable artifact cannot change semantic kind. Leave an ambiguous
			// history unclassified so pruning never silently deletes evidence.
			delete(kinds, ref.ArtifactID)
			ambiguous[ref.ArtifactID] = true
		}
	}
	for _, node := range run.Nodes {
		definition, exists := workflow.FindNode(node.DefinitionNodeID)
		if !exists {
			continue
		}
		kind := ""
		if definition.HumanEdit != nil {
			kind = definition.HumanEdit.ArtifactKind
		} else if definition.ArtifactInput != nil && len(definition.ArtifactInput.AllowedKinds) == 1 {
			kind = definition.ArtifactInput.AllowedKinds[0]
		}
		if kind == "" {
			continue
		}
		metadata := run.Context.Nodes[node.Key]
		refs, err := artifactRefsFromNodeOutput(metadata.Output)
		if err != nil {
			continue
		}
		for _, ref := range refs {
			record(ref, kind)
		}
	}
	for _, slice := range run.Context.Slices {
		record(slice.Blueprint, "blueprint")
		if slice.PageSpec != nil {
			record(*slice.PageSpec, "page_spec")
		}
		if slice.Prototype != nil {
			record(*slice.Prototype, "prototype")
		}
	}
	return kinds
}

func invalidatesPriorDeliverySlices(definition domain.NodeDefinition) bool {
	if definition.Type == domain.NodeFanOut {
		return true
	}
	if definition.Type == domain.NodeHumanEdit && definition.HumanEdit != nil {
		switch definition.HumanEdit.ArtifactKind {
		case "project_brief", "product_requirements", "requirement_baseline", "blueprint":
			return true
		}
	}
	if definition.Type == domain.NodeAITransform && definition.AITransform != nil {
		switch definition.AITransform.JobType {
		case "refine_project_brief", "derive_requirements", "decompose_pages":
			return true
		}
	}
	return false
}

func propagatesProposalLineage(nodeType domain.WorkflowNodeType) bool {
	switch nodeType {
	case domain.NodeCondition, domain.NodeFanOut, domain.NodeMerge, domain.NodeReviewGate,
		domain.NodeQualityGate, domain.NodeTransform:
		return true
	default:
		return false
	}
}

func propagatesMaterializedLineage(nodeType domain.WorkflowNodeType) bool {
	switch nodeType {
	case domain.NodeCondition, domain.NodeFanOut, domain.NodeMerge, domain.NodeTransform:
		return true
	default:
		return false
	}
}

func appendUniqueProposalPin(pins []domain.ProposalLineagePin, candidate domain.ProposalLineagePin) []domain.ProposalLineagePin {
	for _, pin := range pins {
		if pin.ProducerNodeKey == candidate.ProducerNodeKey &&
			pin.ProducerDefinitionNodeID == candidate.ProducerDefinitionNodeID &&
			pin.Proposal == candidate.Proposal && pin.Manifest == candidate.Manifest {
			return pins
		}
	}
	return append(pins, candidate)
}

func workflowSliceRef(slice SliceContext) domain.WorkflowSliceRef {
	ref := domain.WorkflowSliceRef{ID: slice.ID, Key: slice.Key, FanOutNodeID: slice.FanOutNodeID}
	if slice.Blueprint.Validate() == nil {
		value := slice.Blueprint
		ref.Blueprint = &value
	}
	if slice.PageSpec != nil {
		value := *slice.PageSpec
		ref.PageSpec = &value
	}
	if slice.Prototype != nil {
		value := *slice.Prototype
		ref.Prototype = &value
	}
	return ref
}

func decodeStoredInputs(raw json.RawMessage) (domain.NodeInputEnvelope, bool, error) {
	if len(raw) == 0 {
		return domain.NodeInputEnvelope{}, false, nil
	}
	var envelope domain.NodeInputEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return domain.NodeInputEnvelope{}, false, fmt.Errorf("decode stored node inputs: %w", err)
	}
	if err := envelope.Validate(); err != nil {
		return domain.NodeInputEnvelope{}, false, err
	}
	return envelope, true, nil
}

func selectPortOutput(raw json.RawMessage, port string, portCount int) (json.RawMessage, error) {
	canonical, err := domain.CanonicalJSON(raw)
	if err != nil {
		return nil, err
	}
	if port == "default" || portCount == 1 {
		return canonical, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &object); err != nil {
		return canonical, nil
	}
	if rawPorts, exists := object["ports"]; exists {
		var ports map[string]json.RawMessage
		if json.Unmarshal(rawPorts, &ports) == nil {
			if selected, found := ports[port]; found {
				return domain.CanonicalJSON(selected)
			}
		}
	}
	if selected, exists := object[port]; exists {
		return domain.CanonicalJSON(selected)
	}
	return canonical, nil
}

func applyEdgeMapping(output json.RawMessage, mapping map[string]string) (json.RawMessage, error) {
	if len(mapping) == 0 {
		return domain.CanonicalJSON(output)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var source map[string]any
	if err := decoder.Decode(&source); err != nil {
		return nil, &domain.DomainError{Kind: domain.ErrValidation, Field: "mapping", Message: "source output must be an object"}
	}
	target := make(map[string]any, len(source)+len(mapping))
	for name, value := range source {
		target[name] = value
	}
	for targetName, sourceName := range mapping {
		value, exists := source[sourceName]
		if !exists {
			return nil, &domain.DomainError{Kind: domain.ErrValidation, Field: "mapping." + targetName, Message: fmt.Sprintf("source property %q is missing", sourceName)}
		}
		target[targetName] = value
	}
	return domain.CanonicalJSON(target)
}

func fanOutPortValue(schema, item json.RawMessage) (json.RawMessage, error) {
	canonical, err := domain.CanonicalJSON(item)
	if err != nil {
		return nil, err
	}
	wrapped, err := domain.CanonicalJSON(map[string]any{"payload": json.RawMessage(canonical)})
	if err != nil {
		return nil, err
	}
	for _, candidate := range []json.RawMessage{canonical, wrapped} {
		if matchesSchema(schema, candidate) {
			return candidate, nil
		}
	}
	return canonical, nil
}

func passthroughPortValue(schema json.RawMessage, inputs domain.NodeInputEnvelope) (json.RawMessage, error) {
	values := inputs.Values("default")
	if len(values) == 0 {
		for _, binding := range inputs.Bindings() {
			values = append(values, binding.Value)
		}
	}
	candidates := make([]json.RawMessage, 0, 4)
	if len(values) == 1 {
		canonical, err := domain.CanonicalJSON(values[0])
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, canonical)
	}
	inputsObject, err := domain.CanonicalJSON(map[string]any{"inputs": values})
	if err != nil {
		return nil, err
	}
	payloadObject, err := domain.CanonicalJSON(map[string]any{"payload": map[string]any{"inputs": values}})
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, inputsObject, payloadObject)
	if len(values) > 0 {
		candidates = append(candidates, values[0])
	}
	for _, candidate := range candidates {
		if matchesSchema(schema, candidate) {
			return domain.CanonicalJSON(candidate)
		}
	}
	return domain.CanonicalJSON(inputsObject)
}

func appendUniqueSliceRef(refs []domain.WorkflowSliceRef, candidate domain.WorkflowSliceRef) []domain.WorkflowSliceRef {
	for _, ref := range refs {
		if ref.ID == candidate.ID && ref.FanOutNodeID == candidate.FanOutNodeID {
			return refs
		}
	}
	return append(refs, candidate)
}

func replaceSliceRef(refs []domain.WorkflowSliceRef, candidate domain.WorkflowSliceRef) []domain.WorkflowSliceRef {
	for index, ref := range refs {
		if ref.ID == candidate.ID && ref.FanOutNodeID == candidate.FanOutNodeID {
			refs[index] = candidate
			return refs
		}
	}
	return append(refs, candidate)
}

func enrichSliceRef(ref, current domain.WorkflowSliceRef) domain.WorkflowSliceRef {
	if ref.ID != current.ID || ref.Key != current.Key || ref.FanOutNodeID != current.FanOutNodeID {
		return ref
	}
	if ref.Blueprint == nil && current.Blueprint != nil {
		value := *current.Blueprint
		ref.Blueprint = &value
	}
	if ref.PageSpec == nil && current.PageSpec != nil {
		value := *current.PageSpec
		ref.PageSpec = &value
	}
	if ref.Prototype == nil && current.Prototype != nil {
		value := *current.Prototype
		ref.Prototype = &value
	}
	return ref
}

func sliceArtifactRefs(slice SliceContext) []domain.ArtifactRef {
	refs := []domain.ArtifactRef{slice.Blueprint}
	if slice.PageSpec != nil {
		refs = append(refs, *slice.PageSpec)
	}
	if slice.Prototype != nil {
		refs = append(refs, *slice.Prototype)
	}
	result := make([]domain.ArtifactRef, 0, len(refs))
	for _, ref := range refs {
		if ref.Validate() == nil {
			result = appendUniqueArtifactRef(result, ref)
		}
	}
	return result
}

func inputValuesFromSource(inputs domain.NodeInputEnvelope, definitionNodeID, port string) []json.RawMessage {
	port = normalizedPort(port)
	values := make([]json.RawMessage, 0)
	for _, binding := range inputs.BindingsForPort(port) {
		if strings.TrimSpace(definitionNodeID) == "" || binding.Source.DefinitionNodeID == definitionNodeID {
			values = append(values, append(json.RawMessage(nil), binding.Value...))
		}
	}
	return values
}
