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
	}
	producedArtifactIDs := map[string]bool{}
	if refs, err := artifactRefsFromNodeOutput(output); err == nil {
		for _, ref := range refs {
			reference.ArtifactRevisions = appendUniqueArtifactRef(reference.ArtifactRevisions, ref)
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
		for _, ref := range storedInputs.ArtifactRefs() {
			if producedArtifactIDs[ref.ArtifactID] {
				continue
			}
			reference.ArtifactRevisions = appendUniqueArtifactRef(reference.ArtifactRevisions, ref)
		}
		for _, ref := range storedInputs.SliceRefs() {
			reference.DeliverySliceRefs = appendUniqueSliceRef(reference.DeliverySliceRefs, ref)
		}
	}
	sliceID := source.SliceID
	if definition.Type == domain.NodeFanOut && targetSliceID != "" {
		sliceID = targetSliceID
		reference.SliceID = targetSliceID
	}
	if slice, exists := run.Context.Slices[sliceID]; exists {
		reference.DeliverySliceRefs = appendUniqueSliceRef(reference.DeliverySliceRefs, workflowSliceRef(slice))
		for _, ref := range sliceArtifactRefs(slice) {
			if producedArtifactIDs[ref.ArtifactID] {
				continue
			}
			reference.ArtifactRevisions = appendUniqueArtifactRef(reference.ArtifactRevisions, ref)
		}
	}
	return reference
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
