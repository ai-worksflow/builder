package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/worksflow/builder/backend/internal/domain"
)

// BlueprintSemanticNode is the canonical read model shared by approval,
// selection compilation, and workflow fan-out. Page delivery fields may live
// at the node root or under spec; every consumer resolves them identically.
type BlueprintSemanticNode struct {
	ID             string
	Key            string
	Kind           string
	Title          string
	Description    string
	Route          string
	UserGoal       string
	RequirementIDs []string
	Roles          []string
	Method         string
	Path           string
}

type BlueprintSemanticEdge struct {
	ID       string
	SourceID string
	TargetID string
	Kind     string
	Required bool
}

type blueprintSemanticNodeWire struct {
	ID             string   `json:"id"`
	Key            string   `json:"key"`
	BusinessKey    string   `json:"businessKey"`
	Kind           string   `json:"kind"`
	Type           string   `json:"type"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Route          string   `json:"route"`
	Goal           string   `json:"goal"`
	UserGoal       string   `json:"userGoal"`
	RequirementIDs []string `json:"requirementIds"`
	Roles          []string `json:"roles"`
	RequiredRoles  []string `json:"requiredRoles"`
	Role           string   `json:"role"`
	Method         string   `json:"method"`
	Path           string   `json:"path"`
	Spec           *struct {
		Title         string   `json:"title"`
		Description   string   `json:"description"`
		Route         string   `json:"route"`
		Goal          string   `json:"goal"`
		UserGoal      string   `json:"userGoal"`
		Roles         []string `json:"roles"`
		RequiredRoles []string `json:"requiredRoles"`
	} `json:"spec"`
}

type blueprintSemanticEdgeWire struct {
	ID           string `json:"id"`
	From         string `json:"from"`
	To           string `json:"to"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	SourceNodeID string `json:"sourceNodeId"`
	TargetNodeID string `json:"targetNodeId"`
	Kind         string `json:"kind"`
	Type         string `json:"type"`
	Relation     string `json:"relation"`
	Required     bool   `json:"required"`
	IsRequired   bool   `json:"isRequired"`
}

// DecodeBlueprintSemanticGraph returns the canonical semantic graph and
// enforces the Page fields required by runtime fan-out. A Blueprint with no
// valid Page cannot be approved for application generation.
func DecodeBlueprintSemanticGraph(payload json.RawMessage) ([]BlueprintSemanticNode, []BlueprintSemanticEdge, error) {
	var wire struct {
		Nodes    []blueprintSemanticNodeWire `json:"nodes"`
		Edges    []blueprintSemanticEdgeWire `json:"edges"`
		Semantic *struct {
			Nodes []blueprintSemanticNodeWire `json:"nodes"`
			Edges []blueprintSemanticEdgeWire `json:"edges"`
		} `json:"semantic"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return nil, nil, fmt.Errorf("decode Blueprint semantic graph: %w", err)
	}
	if wire.Semantic != nil && (wire.Nodes != nil || wire.Edges != nil) {
		rootPayload, err := json.Marshal(struct {
			Nodes []blueprintSemanticNodeWire `json:"nodes"`
			Edges []blueprintSemanticEdgeWire `json:"edges"`
		}{Nodes: wire.Nodes, Edges: wire.Edges})
		if err != nil {
			return nil, nil, err
		}
		semanticPayload, err := json.Marshal(struct {
			Nodes []blueprintSemanticNodeWire `json:"nodes"`
			Edges []blueprintSemanticEdgeWire `json:"edges"`
		}{Nodes: wire.Semantic.Nodes, Edges: wire.Semantic.Edges})
		if err != nil {
			return nil, nil, err
		}
		rootNodes, rootEdges, err := DecodeBlueprintSemanticGraph(rootPayload)
		if err != nil {
			return nil, nil, fmt.Errorf("Blueprint root graph is invalid: %w", err)
		}
		semanticNodes, semanticEdges, err := DecodeBlueprintSemanticGraph(semanticPayload)
		if err != nil {
			return nil, nil, fmt.Errorf("Blueprint semantic graph is invalid: %w", err)
		}
		if !sameCanonicalBlueprintGraph(rootNodes, rootEdges, semanticNodes, semanticEdges) {
			return nil, nil, fmt.Errorf("Blueprint root and semantic graph representations conflict")
		}
	}
	nodeWires, edgeWires := wire.Nodes, wire.Edges
	if wire.Semantic != nil {
		nodeWires, edgeWires = wire.Semantic.Nodes, wire.Semantic.Edges
	}
	nodes := make([]BlueprintSemanticNode, 0, len(nodeWires))
	seenIDs := map[string]bool{}
	pageCount := 0
	for _, candidate := range nodeWires {
		key, err := canonicalBlueprintAlias("node key", false, candidate.Key, candidate.BusinessKey)
		if err != nil {
			return nil, nil, err
		}
		kind, err := canonicalBlueprintAlias("node kind", true, candidate.Kind, candidate.Type)
		if err != nil {
			return nil, nil, err
		}
		titleValues, descriptionValues := []string{candidate.Title}, []string{candidate.Description}
		routeValues := []string{candidate.Route}
		goalValues := []string{candidate.UserGoal, candidate.Goal}
		if candidate.Spec != nil {
			titleValues = append(titleValues, candidate.Spec.Title)
			descriptionValues = append(descriptionValues, candidate.Spec.Description)
			routeValues = append(routeValues, candidate.Spec.Route)
			goalValues = append(goalValues, candidate.Spec.UserGoal, candidate.Spec.Goal)
		}
		title, err := canonicalBlueprintAlias("Page title", false, titleValues...)
		if err != nil {
			return nil, nil, err
		}
		description, err := canonicalBlueprintAlias("Page description", false, descriptionValues...)
		if err != nil {
			return nil, nil, err
		}
		route, err := canonicalBlueprintAlias("Page route", false, routeValues...)
		if err != nil {
			return nil, nil, err
		}
		userGoal, err := canonicalBlueprintAlias("Page user goal", false, goalValues...)
		if err != nil {
			return nil, nil, err
		}
		roleValues := append(append([]string(nil), candidate.Roles...), candidate.RequiredRoles...)
		if strings.TrimSpace(candidate.Role) != "" {
			roleValues = append(roleValues, candidate.Role)
		}
		if candidate.Spec != nil {
			roleValues = append(roleValues, candidate.Spec.Roles...)
			roleValues = append(roleValues, candidate.Spec.RequiredRoles...)
		}
		node := BlueprintSemanticNode{
			ID: strings.TrimSpace(candidate.ID), Key: key, Kind: strings.ToLower(kind),
			Title: title, Description: description, Route: route, UserGoal: userGoal,
			RequirementIDs: normalizedBlueprintStrings(candidate.RequirementIDs), Roles: normalizedBlueprintStrings(roleValues),
		}
		if node.Kind == "apioperation" || node.Kind == "api" {
			node.Method = strings.ToUpper(strings.TrimSpace(candidate.Method))
			node.Path, err = canonicalBlueprintAlias("API path", false, candidate.Path, candidate.Route)
			if err != nil {
				return nil, nil, err
			}
		}
		if node.ID == "" || node.Key == "" || node.Kind == "" || seenIDs[node.ID] {
			return nil, nil, fmt.Errorf("Blueprint semantic nodes require unique id, key, and kind")
		}
		seenIDs[node.ID] = true
		if strings.EqualFold(node.Kind, "page") {
			pageCount++
			if pageCount > domain.MaximumWorkflowFanOutItems {
				return nil, nil, fmt.Errorf("Blueprint contains more than %d semantic Page nodes", domain.MaximumWorkflowFanOutItems)
			}
			if node.Title == "" || !strings.HasPrefix(node.Route, "/") || node.UserGoal == "" || len(node.RequirementIDs) == 0 {
				return nil, nil, fmt.Errorf("every Blueprint Page requires title, absolute route, user goal, and requirementIds")
			}
		}
		nodes = append(nodes, node)
	}
	if pageCount == 0 {
		return nil, nil, fmt.Errorf("Blueprint contains no semantic Page nodes")
	}
	edges := make([]BlueprintSemanticEdge, 0, len(edgeWires))
	for _, candidate := range edgeWires {
		sourceID, err := canonicalBlueprintAlias("edge source", false, candidate.SourceNodeID, candidate.From, candidate.Source)
		if err != nil {
			return nil, nil, err
		}
		targetID, err := canonicalBlueprintAlias("edge target", false, candidate.TargetNodeID, candidate.To, candidate.Target)
		if err != nil {
			return nil, nil, err
		}
		kind, err := canonicalBlueprintAlias("edge kind", true, candidate.Kind, candidate.Type, candidate.Relation)
		if err != nil {
			return nil, nil, err
		}
		edges = append(edges, BlueprintSemanticEdge{
			ID: strings.TrimSpace(candidate.ID), SourceID: sourceID, TargetID: targetID,
			Kind: strings.ToLower(kind), Required: candidate.Required || candidate.IsRequired,
		})
	}
	return nodes, edges, nil
}

func sameCanonicalBlueprintGraph(
	leftNodes []BlueprintSemanticNode,
	leftEdges []BlueprintSemanticEdge,
	rightNodes []BlueprintSemanticNode,
	rightEdges []BlueprintSemanticEdge,
) bool {
	normalizeNodes := func(values []BlueprintSemanticNode) []BlueprintSemanticNode {
		result := append([]BlueprintSemanticNode(nil), values...)
		for index := range result {
			result[index].RequirementIDs = append([]string(nil), result[index].RequirementIDs...)
			sort.Strings(result[index].RequirementIDs)
			result[index].Roles = append([]string(nil), result[index].Roles...)
			sort.Strings(result[index].Roles)
		}
		sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
		return result
	}
	normalizeEdges := func(values []BlueprintSemanticEdge) []BlueprintSemanticEdge {
		result := append([]BlueprintSemanticEdge(nil), values...)
		sort.Slice(result, func(left, right int) bool {
			leftKey := result[left].ID + "\x00" + result[left].SourceID + "\x00" + result[left].TargetID + "\x00" + result[left].Kind
			rightKey := result[right].ID + "\x00" + result[right].SourceID + "\x00" + result[right].TargetID + "\x00" + result[right].Kind
			return leftKey < rightKey
		})
		return result
	}
	left, _ := json.Marshal(struct {
		Nodes []BlueprintSemanticNode `json:"nodes"`
		Edges []BlueprintSemanticEdge `json:"edges"`
	}{normalizeNodes(leftNodes), normalizeEdges(leftEdges)})
	right, _ := json.Marshal(struct {
		Nodes []BlueprintSemanticNode `json:"nodes"`
		Edges []BlueprintSemanticEdge `json:"edges"`
	}{normalizeNodes(rightNodes), normalizeEdges(rightEdges)})
	return string(left) == string(right)
}

func DecodeBlueprintPages(payload json.RawMessage) ([]BlueprintSemanticNode, error) {
	nodes, _, err := DecodeBlueprintSemanticGraph(payload)
	if err != nil {
		return nil, err
	}
	pages := make([]BlueprintSemanticNode, 0)
	for _, node := range nodes {
		if strings.EqualFold(node.Kind, "page") {
			pages = append(pages, node)
		}
	}
	return pages, nil
}

func canonicalBlueprintAlias(label string, caseInsensitive bool, values ...string) (string, error) {
	canonical := ""
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			if canonical == "" {
				canonical = value
				continue
			}
			equal := canonical == value
			if caseInsensitive {
				equal = strings.EqualFold(canonical, value)
			}
			if !equal {
				return "", fmt.Errorf("Blueprint %s aliases conflict", label)
			}
		}
	}
	return canonical, nil
}

func normalizedBlueprintStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}
