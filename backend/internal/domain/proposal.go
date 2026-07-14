package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ManifestRef struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}

func (r ManifestRef) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return invalid("manifestRef.id", "is required")
	}
	if !IsCanonicalHash(r.Hash) {
		return invalid("manifestRef.hash", "must be a SHA-256 hash")
	}
	return nil
}

type ManifestSource struct {
	Ref     ArtifactRef `json:"ref"`
	Purpose string      `json:"purpose"`
}

type InputManifest struct {
	ID                  string           `json:"id"`
	ProjectID           string           `json:"projectId"`
	JobType             string           `json:"jobType"`
	DeliverySliceID     string           `json:"deliverySliceId,omitempty"`
	BaseRevision        *ArtifactRef     `json:"baseRevision,omitempty"`
	Sources             []ManifestSource `json:"sources"`
	Constraints         json.RawMessage  `json:"constraints"`
	OutputSchemaVersion string           `json:"outputSchemaVersion"`
	CreatedBy           string           `json:"createdBy"`
	CreatedAt           time.Time        `json:"createdAt"`
	Hash                string           `json:"hash"`
}

func NewInputManifest(id, projectID, jobType, deliverySliceID string, base *ArtifactRef, sources []ManifestSource, constraints json.RawMessage, outputSchemaVersion, createdBy string, now time.Time) (InputManifest, error) {
	manifest := InputManifest{
		ID: strings.TrimSpace(id), ProjectID: strings.TrimSpace(projectID), JobType: strings.TrimSpace(jobType),
		DeliverySliceID: strings.TrimSpace(deliverySliceID), OutputSchemaVersion: strings.TrimSpace(outputSchemaVersion),
		CreatedBy: strings.TrimSpace(createdBy), CreatedAt: now.UTC(),
	}
	if base != nil {
		copyRef := *base
		manifest.BaseRevision = &copyRef
	}
	// Keep the wire shape an array even for a valid base-only transform.
	manifest.Sources = append([]ManifestSource{}, sources...)
	if len(constraints) == 0 {
		constraints = json.RawMessage(`{}`)
	}
	canonical, err := CanonicalJSON(constraints)
	if err != nil {
		return InputManifest{}, err
	}
	manifest.Constraints = canonical
	if err := manifest.validate(false); err != nil {
		return InputManifest{}, err
	}
	hash, err := manifest.calculateHash()
	if err != nil {
		return InputManifest{}, err
	}
	manifest.Hash = hash
	return manifest, nil
}

func (m InputManifest) Ref() ManifestRef { return ManifestRef{ID: m.ID, Hash: m.Hash} }

func (m InputManifest) Validate() error {
	if err := m.validate(true); err != nil {
		return err
	}
	hash, err := m.calculateHash()
	if err != nil {
		return err
	}
	if hash != m.Hash {
		return &DomainError{Kind: ErrConflict, Field: "manifest.hash", Message: "manifest payload no longer matches its pinned hash"}
	}
	return nil
}

func (m InputManifest) validate(requireHash bool) error {
	if m.ID == "" || m.ProjectID == "" || m.JobType == "" || m.OutputSchemaVersion == "" || m.CreatedBy == "" {
		return invalid("manifest", "id, projectId, jobType, outputSchemaVersion and createdBy are required")
	}
	if len(m.Sources) == 0 && m.BaseRevision == nil {
		return &DomainError{Kind: ErrManifestUnpinned, Field: "manifest", Message: "a pinned base revision or source is required"}
	}
	if m.BaseRevision != nil {
		if err := m.BaseRevision.Validate(); err != nil {
			return &DomainError{Kind: ErrManifestUnpinned, Field: "manifest.baseRevision", Message: err.Error()}
		}
	}
	seen := map[string]struct{}{}
	for index, source := range m.Sources {
		if err := source.Ref.Validate(); err != nil {
			return &DomainError{Kind: ErrManifestUnpinned, Field: fmt.Sprintf("manifest.sources[%d]", index), Message: err.Error()}
		}
		if strings.TrimSpace(source.Purpose) == "" {
			return invalid(fmt.Sprintf("manifest.sources[%d].purpose", index), "is required")
		}
		key := source.Ref.ArtifactID + "\x00" + source.Ref.RevisionID + "\x00" + source.Ref.AnchorID
		if _, duplicate := seen[key]; duplicate {
			return invalid("manifest.sources", "duplicate pinned source")
		}
		seen[key] = struct{}{}
	}
	if _, err := CanonicalJSON(m.Constraints); err != nil {
		return err
	}
	if requireHash && !IsCanonicalHash(m.Hash) {
		return invalid("manifest.hash", "must be a SHA-256 hash")
	}
	return nil
}

func (m InputManifest) calculateHash() (string, error) {
	payload := struct {
		ID                  string           `json:"id"`
		ProjectID           string           `json:"projectId"`
		JobType             string           `json:"jobType"`
		DeliverySliceID     string           `json:"deliverySliceId,omitempty"`
		BaseRevision        *ArtifactRef     `json:"baseRevision,omitempty"`
		Sources             []ManifestSource `json:"sources"`
		Constraints         json.RawMessage  `json:"constraints"`
		OutputSchemaVersion string           `json:"outputSchemaVersion"`
		CreatedBy           string           `json:"createdBy"`
		CreatedAt           time.Time        `json:"createdAt"`
	}{
		m.ID, m.ProjectID, m.JobType, m.DeliverySliceID, m.BaseRevision,
		m.Sources, m.Constraints, m.OutputSchemaVersion, m.CreatedBy, m.CreatedAt,
	}
	return CanonicalHash(payload)
}

type ProposalOperationKind string

const (
	OperationAdd     ProposalOperationKind = "add"
	OperationReplace ProposalOperationKind = "replace"
	OperationRemove  ProposalOperationKind = "remove"
)

type ProposalDecision string

const (
	DecisionPending  ProposalDecision = "pending"
	DecisionAccepted ProposalDecision = "accepted"
	DecisionRejected ProposalDecision = "rejected"
	DecisionApplied  ProposalDecision = "applied"
)

type ProposalOperation struct {
	ID        string                `json:"id"`
	Kind      ProposalOperationKind `json:"kind"`
	Path      string                `json:"path"`
	Value     json.RawMessage       `json:"value,omitempty"`
	DependsOn []string              `json:"dependsOn,omitempty"`
	Rationale string                `json:"rationale,omitempty"`
	Decision  ProposalDecision      `json:"decision"`
	DecidedBy string                `json:"decidedBy,omitempty"`
	Reason    string                `json:"reason,omitempty"`
}

func (o ProposalOperation) validate(index int) error {
	field := fmt.Sprintf("proposal.operations[%d]", index)
	if strings.TrimSpace(o.ID) == "" {
		return invalid(field+".id", "is required")
	}
	if o.Path != "" && !strings.HasPrefix(o.Path, "/") {
		return invalid(field+".path", "must be an RFC 6901 JSON pointer")
	}
	switch o.Kind {
	case OperationAdd, OperationReplace:
		if len(o.Value) == 0 {
			return invalid(field+".value", "is required")
		}
		if _, err := CanonicalJSON(o.Value); err != nil {
			return err
		}
	case OperationRemove:
		if o.Path == "" {
			return invalid(field+".path", "root removal is not allowed")
		}
	default:
		return invalid(field+".kind", string(o.Kind))
	}
	if o.Decision == "" {
		o.Decision = DecisionPending
	}
	if o.Decision != DecisionPending {
		return invalid(field+".decision", "new operations must be pending")
	}
	return nil
}

type ProposalStatus string

const (
	ProposalOpen             ProposalStatus = "open"
	ProposalReviewing        ProposalStatus = "reviewing"
	ProposalReady            ProposalStatus = "ready"
	ProposalRejected         ProposalStatus = "rejected"
	ProposalApplied          ProposalStatus = "applied"
	ProposalPartiallyApplied ProposalStatus = "partially_applied"
	ProposalStale            ProposalStatus = "stale"
)

type OutputProposal struct {
	ID           string              `json:"id"`
	ProjectID    string              `json:"projectId"`
	ArtifactID   string              `json:"artifactId"`
	Manifest     ManifestRef         `json:"manifest"`
	BaseRevision ArtifactRef         `json:"baseRevision"`
	Operations   []ProposalOperation `json:"operations"`
	Assumptions  []string            `json:"assumptions,omitempty"`
	Questions    []string            `json:"questions,omitempty"`
	PayloadHash  string              `json:"payloadHash"`
	Status       ProposalStatus      `json:"status"`
	Version      uint64              `json:"version"`
	CreatedBy    string              `json:"createdBy"`
	CreatedAt    time.Time           `json:"createdAt"`
	AppliedAt    *time.Time          `json:"appliedAt,omitempty"`
}

func NewOutputProposal(id, projectID, artifactID string, manifest ManifestRef, base ArtifactRef, operations []ProposalOperation, assumptions, questions []string, createdBy string, now time.Time) (*OutputProposal, error) {
	proposal := &OutputProposal{
		ID: strings.TrimSpace(id), ProjectID: strings.TrimSpace(projectID), ArtifactID: strings.TrimSpace(artifactID),
		Manifest: manifest, BaseRevision: base, Assumptions: append([]string(nil), assumptions...),
		Questions: append([]string(nil), questions...), CreatedBy: strings.TrimSpace(createdBy),
		CreatedAt: now.UTC(), Status: ProposalOpen, Version: 1,
	}
	proposal.Operations = make([]ProposalOperation, len(operations))
	for index, operation := range operations {
		operation.Value = cloneJSON(operation.Value)
		operation.DependsOn = append([]string(nil), operation.DependsOn...)
		if operation.Decision == "" {
			operation.Decision = DecisionPending
		}
		proposal.Operations[index] = operation
	}
	if err := proposal.validateNew(); err != nil {
		return nil, err
	}
	hash, err := proposal.calculatePayloadHash()
	if err != nil {
		return nil, err
	}
	proposal.PayloadHash = hash
	return proposal, nil
}

func (p *OutputProposal) validateNew() error {
	if p.ID == "" || p.ProjectID == "" || p.ArtifactID == "" || p.CreatedBy == "" {
		return invalid("proposal", "id, projectId, artifactId and createdBy are required")
	}
	if err := p.Manifest.Validate(); err != nil {
		return err
	}
	if err := p.BaseRevision.Validate(); err != nil {
		return err
	}
	if p.BaseRevision.ArtifactID != p.ArtifactID {
		return invalid("proposal.baseRevision.artifactId", "does not match proposal artifact")
	}
	if len(p.Operations) == 0 {
		return invalid("proposal.operations", "at least one operation is required")
	}
	byID := map[string]struct{}{}
	for index, operation := range p.Operations {
		if err := operation.validate(index); err != nil {
			return err
		}
		if _, duplicate := byID[operation.ID]; duplicate {
			return invalid("proposal.operations", "operation IDs must be unique")
		}
		byID[operation.ID] = struct{}{}
	}
	for _, operation := range p.Operations {
		for _, dependency := range operation.DependsOn {
			if dependency == operation.ID {
				return invalid("proposal.operations.dependsOn", "operation cannot depend on itself")
			}
			if _, exists := byID[dependency]; !exists {
				return invalid("proposal.operations.dependsOn", "references an unknown operation")
			}
		}
	}
	if _, err := topologicalOperations(p.Operations, false); err != nil {
		return err
	}
	return nil
}

func (p OutputProposal) calculatePayloadHash() (string, error) {
	type immutableOperation struct {
		ID        string                `json:"id"`
		Kind      ProposalOperationKind `json:"kind"`
		Path      string                `json:"path"`
		Value     json.RawMessage       `json:"value,omitempty"`
		DependsOn []string              `json:"dependsOn,omitempty"`
		Rationale string                `json:"rationale,omitempty"`
	}
	operations := make([]immutableOperation, len(p.Operations))
	for index, operation := range p.Operations {
		operations[index] = immutableOperation{operation.ID, operation.Kind, operation.Path, operation.Value, operation.DependsOn, operation.Rationale}
	}
	payload := struct {
		ID           string               `json:"id"`
		ProjectID    string               `json:"projectId"`
		ArtifactID   string               `json:"artifactId"`
		Manifest     ManifestRef          `json:"manifest"`
		BaseRevision ArtifactRef          `json:"baseRevision"`
		Operations   []immutableOperation `json:"operations"`
		Assumptions  []string             `json:"assumptions,omitempty"`
		Questions    []string             `json:"questions,omitempty"`
		CreatedBy    string               `json:"createdBy"`
		CreatedAt    time.Time            `json:"createdAt"`
	}{p.ID, p.ProjectID, p.ArtifactID, p.Manifest, p.BaseRevision, operations, p.Assumptions, p.Questions, p.CreatedBy, p.CreatedAt}
	return CanonicalHash(payload)
}

func (p OutputProposal) ValidatePayloadHash() error {
	hash, err := p.calculatePayloadHash()
	if err != nil {
		return err
	}
	if hash != p.PayloadHash {
		return &DomainError{Kind: ErrConflict, Field: "proposal.payloadHash", Message: "proposal payload was mutated"}
	}
	return nil
}

func (p *OutputProposal) Decide(operationID string, decision ProposalDecision, actorID, reason string, expectedVersion uint64) error {
	if p.Version != expectedVersion {
		return conflict("proposal", expectedVersion, p.Version)
	}
	if p.Status == ProposalApplied || p.Status == ProposalPartiallyApplied || p.Status == ProposalRejected || p.Status == ProposalStale {
		return transition("proposal", string(p.Status), "decide")
	}
	if decision != DecisionAccepted && decision != DecisionRejected {
		return invalid("proposal.decision", "must be accepted or rejected")
	}
	if strings.TrimSpace(actorID) == "" {
		return invalid("proposal.actorId", "is required")
	}
	for index := range p.Operations {
		operation := &p.Operations[index]
		if operation.ID != operationID {
			continue
		}
		if operation.Decision != DecisionPending {
			return transition("proposalOperation", string(operation.Decision), string(decision))
		}
		if decision == DecisionRejected && strings.TrimSpace(reason) == "" {
			return invalid("proposal.reason", "is required when rejecting an operation")
		}
		operation.Decision = decision
		operation.DecidedBy = actorID
		operation.Reason = strings.TrimSpace(reason)
		p.Version++
		p.recalculateStatus()
		return nil
	}
	return &DomainError{Kind: ErrNotFound, Field: "proposal.operationId", Message: operationID}
}

func (p *OutputProposal) recalculateStatus() {
	pending, accepted, rejected := 0, 0, 0
	for _, operation := range p.Operations {
		switch operation.Decision {
		case DecisionPending:
			pending++
		case DecisionAccepted:
			accepted++
		case DecisionRejected:
			rejected++
		}
	}
	switch {
	case pending > 0 && (accepted > 0 || rejected > 0):
		p.Status = ProposalReviewing
	case pending > 0:
		p.Status = ProposalOpen
	case accepted > 0:
		p.Status = ProposalReady
	case rejected == len(p.Operations):
		p.Status = ProposalRejected
	}
}

func (p *OutputProposal) MarkStale(current ArtifactRef, expectedVersion uint64) error {
	if p.Version != expectedVersion {
		return conflict("proposal", expectedVersion, p.Version)
	}
	if p.Status == ProposalApplied || p.Status == ProposalPartiallyApplied || p.Status == ProposalRejected {
		return transition("proposal", string(p.Status), string(ProposalStale))
	}
	if p.BaseRevision.Equal(current) {
		return invalid("proposal.baseRevision", "still matches the current revision")
	}
	p.Status = ProposalStale
	p.Version++
	return nil
}

func (p OutputProposal) AcceptedOperations() ([]ProposalOperation, error) {
	if p.Status != ProposalReady {
		return nil, transition("proposal", string(p.Status), "apply")
	}
	for _, operation := range p.Operations {
		if operation.Decision == DecisionPending {
			return nil, invalid("proposal.operations", "all operations must be decided before apply")
		}
		if operation.Decision == DecisionAccepted {
			for _, dependencyID := range operation.DependsOn {
				dependency := findOperation(p.Operations, dependencyID)
				if dependency == nil || dependency.Decision != DecisionAccepted {
					return nil, invalid("proposal.operations.dependsOn", fmt.Sprintf("accepted operation %s requires accepted operation %s", operation.ID, dependencyID))
				}
			}
		}
	}
	return topologicalOperations(p.Operations, true)
}

func (p *OutputProposal) MarkApplied(expectedVersion uint64, now time.Time) error {
	if p.Version != expectedVersion {
		return conflict("proposal", expectedVersion, p.Version)
	}
	if _, err := p.AcceptedOperations(); err != nil {
		return err
	}
	rejected := 0
	for index := range p.Operations {
		if p.Operations[index].Decision == DecisionAccepted {
			p.Operations[index].Decision = DecisionApplied
		} else if p.Operations[index].Decision == DecisionRejected {
			rejected++
		}
	}
	if rejected > 0 {
		p.Status = ProposalPartiallyApplied
	} else {
		p.Status = ProposalApplied
	}
	appliedAt := now.UTC()
	p.AppliedAt = &appliedAt
	p.Version++
	return nil
}

func findOperation(operations []ProposalOperation, id string) *ProposalOperation {
	for index := range operations {
		if operations[index].ID == id {
			return &operations[index]
		}
	}
	return nil
}

func topologicalOperations(operations []ProposalOperation, acceptedOnly bool) ([]ProposalOperation, error) {
	selected := map[string]ProposalOperation{}
	order := map[string]int{}
	for index, operation := range operations {
		if acceptedOnly && operation.Decision != DecisionAccepted {
			continue
		}
		selected[operation.ID] = operation
		order[operation.ID] = index
	}
	indegree := map[string]int{}
	dependents := map[string][]string{}
	for id := range selected {
		indegree[id] = 0
	}
	for id, operation := range selected {
		for _, dependency := range operation.DependsOn {
			if _, included := selected[dependency]; !included {
				if acceptedOnly {
					continue
				}
				return nil, invalid("proposal.operations.dependsOn", "references an unknown operation")
			}
			indegree[id]++
			dependents[dependency] = append(dependents[dependency], id)
		}
	}
	queue := make([]string, 0)
	for id, count := range indegree {
		if count == 0 {
			queue = append(queue, id)
		}
	}
	sort.Slice(queue, func(i, j int) bool { return order[queue[i]] < order[queue[j]] })
	ordered := make([]ProposalOperation, 0, len(selected))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		ordered = append(ordered, selected[id])
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
				sort.Slice(queue, func(i, j int) bool { return order[queue[i]] < order[queue[j]] })
			}
		}
	}
	if len(ordered) != len(selected) {
		return nil, invalid("proposal.operations.dependsOn", "dependency graph contains a cycle")
	}
	return ordered, nil
}

// ApplyProposalPatch applies accepted operations to a detached JSON value.
// It never mutates the caller's raw message and returns canonical JSON.
func ApplyProposalPatch(base json.RawMessage, operations []ProposalOperation) (json.RawMessage, error) {
	root, err := decodeJSON(base)
	if err != nil {
		return nil, err
	}
	for _, operation := range operations {
		if operation.Decision != DecisionAccepted {
			return nil, invalid("proposal.operation.decision", "only accepted operations may be applied")
		}
		tokens, err := parseJSONPointer(operation.Path)
		if err != nil {
			return nil, err
		}
		var value any
		if operation.Kind != OperationRemove {
			value, err = decodeJSON(operation.Value)
			if err != nil {
				return nil, err
			}
		}
		root, err = patchJSONValue(root, tokens, operation.Kind, value)
		if err != nil {
			return nil, &DomainError{Kind: ErrValidation, Field: "proposal.operation." + operation.ID, Message: err.Error()}
		}
	}
	canonical, err := CanonicalJSON(root)
	return json.RawMessage(canonical), err
}

func decodeJSON(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, invalid("json", err.Error())
	}
	if err := ensureJSONEOF(decoder); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return value, nil
}

func parseJSONPointer(pointer string) ([]string, error) {
	if pointer == "" {
		return nil, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, invalid("jsonPointer", "must start with /")
	}
	parts := strings.Split(pointer[1:], "/")
	for index, part := range parts {
		var output strings.Builder
		for offset := 0; offset < len(part); offset++ {
			if part[offset] != '~' {
				output.WriteByte(part[offset])
				continue
			}
			if offset+1 >= len(part) || (part[offset+1] != '0' && part[offset+1] != '1') {
				return nil, invalid("jsonPointer", "contains an invalid escape")
			}
			offset++
			if part[offset] == '0' {
				output.WriteByte('~')
			} else {
				output.WriteByte('/')
			}
		}
		parts[index] = output.String()
	}
	return parts, nil
}

func patchJSONValue(current any, tokens []string, kind ProposalOperationKind, value any) (any, error) {
	if len(tokens) == 0 {
		if kind == OperationRemove {
			return nil, fmt.Errorf("cannot remove the JSON root")
		}
		return value, nil
	}
	token := tokens[0]
	if object, ok := current.(map[string]any); ok {
		if len(tokens) == 1 {
			_, exists := object[token]
			switch kind {
			case OperationAdd:
				object[token] = value
			case OperationReplace:
				if !exists {
					return nil, fmt.Errorf("replace target %q does not exist", token)
				}
				object[token] = value
			case OperationRemove:
				if !exists {
					return nil, fmt.Errorf("remove target %q does not exist", token)
				}
				delete(object, token)
			}
			return object, nil
		}
		child, exists := object[token]
		if !exists {
			return nil, fmt.Errorf("path segment %q does not exist", token)
		}
		patched, err := patchJSONValue(child, tokens[1:], kind, value)
		if err != nil {
			return nil, err
		}
		object[token] = patched
		return object, nil
	}

	array, ok := current.([]any)
	if !ok {
		return nil, fmt.Errorf("path segment %q traverses a scalar", token)
	}
	if len(tokens) == 1 && kind == OperationAdd && token == "-" {
		return append(array, value), nil
	}
	index, err := strconv.Atoi(token)
	if err != nil || index < 0 {
		return nil, fmt.Errorf("array index %q is invalid", token)
	}
	if len(tokens) == 1 {
		switch kind {
		case OperationAdd:
			if index > len(array) {
				return nil, fmt.Errorf("array add index %d exceeds length %d", index, len(array))
			}
			array = append(array, nil)
			copy(array[index+1:], array[index:])
			array[index] = value
		case OperationReplace:
			if index >= len(array) {
				return nil, fmt.Errorf("array replace index %d is out of range", index)
			}
			array[index] = value
		case OperationRemove:
			if index >= len(array) {
				return nil, fmt.Errorf("array remove index %d is out of range", index)
			}
			array = append(array[:index], array[index+1:]...)
		}
		return array, nil
	}
	if index >= len(array) {
		return nil, fmt.Errorf("array index %d is out of range", index)
	}
	patched, err := patchJSONValue(array[index], tokens[1:], kind, value)
	if err != nil {
		return nil, err
	}
	array[index] = patched
	return array, nil
}
