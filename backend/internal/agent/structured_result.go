package agent

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

type runnerCoverageResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Note   string `json:"note"`
}

type runnerVerificationResult struct {
	CommandID string `json:"commandId"`
	Status    string `json:"status"`
	Note      string `json:"note"`
}

type runnerResourceNode struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Purpose        string   `json:"purpose"`
	RequirementIDs []string `json:"requirementIds"`
	Consumers      []string `json:"consumers"`
	Source         string   `json:"source"`
	Reference      string   `json:"reference"`
	Accessibility  string   `json:"accessibility"`
	Status         string   `json:"status"`
}

type runnerResourceEdge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
}

type runnerResourceGraph struct {
	Applicable bool                 `json:"applicable"`
	Nodes      []runnerResourceNode `json:"nodes"`
	Edges      []runnerResourceEdge `json:"edges"`
}

type runnerStructuredResult struct {
	Summary            string                     `json:"summary"`
	ChangedPaths       []string                   `json:"changedPaths"`
	Obligations        []runnerCoverageResult     `json:"obligations"`
	AcceptanceCriteria []runnerCoverageResult     `json:"acceptanceCriteria"`
	Verification       []runnerVerificationResult `json:"verification"`
	ResourceGraph      runnerResourceGraph        `json:"resourceGraph"`
	Blockers           []string                   `json:"blockers"`
}

func validateRunnerResultClosure(
	capsule TaskCapsule,
	patch CapturedPatch,
	payload []byte,
) error {
	var result runnerStructuredResult
	if err := decodeStrictJSON(payload, &result); err != nil {
		return fmt.Errorf("%w: decode structured completion result: %v", ErrExecutionDrift, err)
	}
	changedPaths := make([]string, len(patch.Changes))
	for index, change := range patch.Changes {
		changedPaths[index] = change.Operation.Path
	}
	if !slices.Equal(result.ChangedPaths, changedPaths) {
		return fmt.Errorf("%w: structured changedPaths do not match the platform-captured Patch", ErrExecutionDrift)
	}
	if err := validateCoverageResults("obligation", capsule.ObligationIDs, result.Obligations); err != nil {
		return err
	}
	if err := validateCoverageResults(
		"acceptance criterion",
		capsule.AcceptanceCriterionIDs,
		result.AcceptanceCriteria,
	); err != nil {
		return err
	}
	if len(result.Verification) != len(capsule.VerificationCommandIDs) {
		return fmt.Errorf("%w: structured verification does not cover every command", ErrExecutionBlocked)
	}
	for index, expected := range capsule.VerificationCommandIDs {
		actual := result.Verification[index]
		if actual.CommandID != expected {
			return fmt.Errorf("%w: structured verification command %d does not match %s", ErrExecutionDrift, index, expected)
		}
		if actual.Status != "passed" {
			return fmt.Errorf("%w: verification command %s is %s", ErrExecutionBlocked, expected, actual.Status)
		}
	}
	if err := validateRunnerResourceGraph(capsule, patch, result.ResourceGraph); err != nil {
		return err
	}
	if len(result.Blockers) != 0 {
		return fmt.Errorf("%w: Runner reported %d completion blocker(s)", ErrExecutionBlocked, len(result.Blockers))
	}
	return nil
}

func validateRunnerResourceGraph(
	capsule TaskCapsule,
	patch CapturedPatch,
	graph runnerResourceGraph,
) error {
	changedPaths := make(map[string]bool, len(patch.Changes))
	frontendChanged := false
	for _, change := range patch.Changes {
		path := change.Operation.Path
		changedPaths[path] = true
		if !frontendResourceSourcePath(path) {
			continue
		}
		frontendChanged = true
		if change.Operation.Kind == "file.upsert" && utf8.Valid(change.Content) &&
			containsProhibitedEmoji(string(change.Content)) {
			return fmt.Errorf("%w: frontend source %s uses an emoji or Unicode symbol substitute", ErrExecutionBlocked, path)
		}
	}
	if frontendChanged && !graph.Applicable {
		return fmt.Errorf("%w: frontend changes require an applicable resource graph", ErrExecutionBlocked)
	}
	if !graph.Applicable {
		if len(graph.Nodes) != 0 || len(graph.Edges) != 0 {
			return fmt.Errorf("%w: a non-applicable resource graph must be empty", ErrExecutionDrift)
		}
		return nil
	}
	if !sort.SliceIsSorted(graph.Nodes, func(left, right int) bool {
		return graph.Nodes[left].ID < graph.Nodes[right].ID
	}) {
		return fmt.Errorf("%w: resource graph nodes are not in stable ID order", ErrExecutionDrift)
	}
	allowedRequirements := make(map[string]bool, len(capsule.ObligationIDs)+len(capsule.AcceptanceCriterionIDs))
	for _, id := range capsule.ObligationIDs {
		allowedRequirements[id] = true
	}
	for _, id := range capsule.AcceptanceCriterionIDs {
		allowedRequirements[id] = true
	}
	nodes := make(map[string]runnerResourceNode, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if node.ID == "" || nodes[node.ID].ID != "" || node.Status != "resolved" ||
			containsProhibitedEmoji(node.Reference) || containsProhibitedEmoji(node.Purpose) {
			return fmt.Errorf("%w: resource node %s is duplicate, blocked, or uses a prohibited substitute", ErrExecutionBlocked, node.ID)
		}
		if node.Kind == "icon" && node.Source != "authoritative_input" &&
			node.Source != "existing_asset" && node.Source != "icon_library" {
			return fmt.Errorf("%w: interface icon %s does not use an authoritative asset or icon library", ErrExecutionBlocked, node.ID)
		}
		if (node.Source == "generated_svg" || node.Source == "generated_raster") &&
			!changedPaths[node.Reference] {
			return fmt.Errorf("%w: generated resource %s is absent from the captured Patch", ErrExecutionDrift, node.ID)
		}
		if !strictSortedUnique(node.RequirementIDs) || !strictSortedUnique(node.Consumers) {
			return fmt.Errorf("%w: resource node %s bindings are not unique and sorted", ErrExecutionDrift, node.ID)
		}
		for _, requirementID := range node.RequirementIDs {
			if !allowedRequirements[requirementID] {
				return fmt.Errorf("%w: resource node %s invents requirement %s", ErrExecutionDrift, node.ID, requirementID)
			}
		}
		nodes[node.ID] = node
	}
	if !sort.SliceIsSorted(graph.Edges, func(left, right int) bool {
		leftKey := graph.Edges[left].From + "\x00" + graph.Edges[left].To + "\x00" + graph.Edges[left].Relation
		rightKey := graph.Edges[right].From + "\x00" + graph.Edges[right].To + "\x00" + graph.Edges[right].Relation
		return leftKey < rightKey
	}) {
		return fmt.Errorf("%w: resource graph edges are not in stable order", ErrExecutionDrift)
	}
	edges := make(map[string]bool, len(graph.Edges))
	for _, edge := range graph.Edges {
		key := edge.From + "\x00" + edge.To + "\x00" + edge.Relation
		if edges[key] {
			return fmt.Errorf("%w: duplicate resource graph edge", ErrExecutionDrift)
		}
		edges[key] = true
		switch edge.Relation {
		case "supports":
			if !allowedRequirements[edge.From] || nodes[edge.To].ID == "" {
				return fmt.Errorf("%w: invalid resource requirement edge", ErrExecutionDrift)
			}
		case "consumed_by":
			node := nodes[edge.From]
			if node.ID == "" || !slices.Contains(node.Consumers, edge.To) {
				return fmt.Errorf("%w: invalid resource consumer edge", ErrExecutionDrift)
			}
		case "variant_of":
			if nodes[edge.From].ID == "" || nodes[edge.To].ID == "" {
				return fmt.Errorf("%w: invalid resource variant edge", ErrExecutionDrift)
			}
		default:
			return fmt.Errorf("%w: unsupported resource graph relation", ErrExecutionDrift)
		}
	}
	for _, node := range graph.Nodes {
		for _, requirementID := range node.RequirementIDs {
			if !edges[requirementID+"\x00"+node.ID+"\x00supports"] {
				return fmt.Errorf("%w: resource node %s omits a requirement edge", ErrExecutionBlocked, node.ID)
			}
		}
		for _, consumer := range node.Consumers {
			if !edges[node.ID+"\x00"+consumer+"\x00consumed_by"] {
				return fmt.Errorf("%w: resource node %s omits a consumer edge", ErrExecutionBlocked, node.ID)
			}
		}
	}
	return nil
}

func frontendResourceSourcePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tsx", ".jsx", ".vue", ".svelte", ".html", ".css", ".scss", ".sass", ".less":
		return true
	default:
		return false
	}
}

func strictSortedUnique(values []string) bool {
	for index, value := range values {
		if strings.TrimSpace(value) == "" || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}

func containsProhibitedEmoji(value string) bool {
	for _, symbol := range value {
		switch {
		case symbol >= 0x1f000 && symbol <= 0x1faff,
			symbol >= 0x2600 && symbol <= 0x27bf,
			symbol >= 0x2b00 && symbol <= 0x2bff,
			symbol >= 0x1f1e6 && symbol <= 0x1f1ff,
			symbol == 0xfe0f || symbol == 0x20e3:
			return true
		}
	}
	return false
}

func validateCoverageResults(
	kind string,
	expected []string,
	actual []runnerCoverageResult,
) error {
	if len(actual) != len(expected) {
		return fmt.Errorf("%w: structured result does not cover every %s", ErrExecutionBlocked, kind)
	}
	for index, expectedID := range expected {
		item := actual[index]
		if item.ID != expectedID {
			return fmt.Errorf("%w: structured %s %d does not match %s", ErrExecutionDrift, kind, index, expectedID)
		}
		if item.Status != "satisfied" {
			return fmt.Errorf("%w: %s %s is %s", ErrExecutionBlocked, kind, expectedID, item.Status)
		}
	}
	return nil
}
