package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strconv"
	"strings"

	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	maxConditionExpressionBytes = 8 << 10
	maxConditionDepth           = 16
)

// DeclarativeConditionEvaluator evaluates a deliberately small JSON rule DSL.
// It never executes source code. Expressions are either true/false or objects:
//
//	{"path":"/scope/priority","op":"eq","value":"high"}
//	{"all":[<rule>, ...]}, {"any":[<rule>, ...]}, {"not":<rule>}
//
// Supported leaf operations are exists, truthy, eq, ne, gt, gte, lt and lte.
type DeclarativeConditionEvaluator struct{}

func (DeclarativeConditionEvaluator) Evaluate(_ context.Context, execution Execution, branches []domain.ConditionBranch) (string, error) {
	root, err := conditionContext(execution)
	if err != nil {
		return "", err
	}
	defaultBranch := ""
	for _, branch := range branches {
		if branch.Default {
			defaultBranch = branch.Name
			continue
		}
		if len(branch.Expression) > maxConditionExpressionBytes {
			return "", fmt.Errorf("condition branch %q expression exceeds %d bytes", branch.Name, maxConditionExpressionBytes)
		}
		matched, err := evaluateConditionRule(json.RawMessage(branch.Expression), root, 0)
		if err != nil {
			return "", fmt.Errorf("evaluate condition branch %q: %w", branch.Name, err)
		}
		if matched {
			return branch.Name, nil
		}
	}
	if defaultBranch == "" {
		return "", fmt.Errorf("condition has no matching or default branch")
	}
	return defaultBranch, nil
}

// DeclarativeConditionEvaluatorV1 is the deterministic typed-input evaluator
// for the current execution profile. Unlike the frozen pre-pin evaluator above,
// it cannot inspect whichever unrelated node/value happens to have committed
// first. Its entire decision context is immutable run identity/scope, the
// current NodeInputEnvelope, and the current fan-out slice identity.
type DeclarativeConditionEvaluatorV1 struct{}

func (DeclarativeConditionEvaluatorV1) Evaluate(_ context.Context, execution Execution, branches []domain.ConditionBranch) (string, error) {
	root, err := conditionContextV1(execution)
	if err != nil {
		return "", err
	}
	defaultBranch := ""
	for _, branch := range branches {
		if branch.Default {
			defaultBranch = branch.Name
			continue
		}
		if len(branch.Expression) > maxConditionExpressionBytes {
			return "", fmt.Errorf("condition branch %q expression exceeds %d bytes", branch.Name, maxConditionExpressionBytes)
		}
		raw := json.RawMessage(branch.Expression)
		if err := validateConditionRuleRootsV1(raw, 0); err != nil {
			return "", fmt.Errorf("condition branch %q: %w", branch.Name, err)
		}
		matched, err := evaluateConditionRule(raw, root, 0)
		if err != nil {
			return "", fmt.Errorf("evaluate condition branch %q: %w", branch.Name, err)
		}
		if matched {
			return branch.Name, nil
		}
	}
	if defaultBranch == "" {
		return "", fmt.Errorf("condition has no matching or default branch")
	}
	return defaultBranch, nil
}

func validateCurrentConditionExpressions(definition domain.WorkflowDefinition) error {
	for _, node := range definition.Nodes {
		if node.Condition == nil {
			continue
		}
		for _, branch := range node.Condition.Branches {
			if branch.Default {
				continue
			}
			raw := json.RawMessage(branch.Expression)
			if _, err := evaluateConditionRule(raw, map[string]any{}, 0); err != nil {
				return capabilityError("workflow.nodes."+node.ID+".condition", err.Error())
			}
			if err := validateConditionRuleRootsV1(raw, 0); err != nil {
				return capabilityError("workflow.nodes."+node.ID+".condition", err.Error())
			}
		}
	}
	return nil
}

func conditionContextV1(execution Execution) (map[string]any, error) {
	if err := execution.Inputs.Validate(); err != nil {
		return nil, fmt.Errorf("validate immutable condition inputs: %w", err)
	}
	inputs, err := decodeConditionJSON(execution.Inputs.Canonical())
	if err != nil {
		return nil, fmt.Errorf("decode immutable condition inputs: %w", err)
	}
	inputObject, ok := inputs.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("immutable condition inputs are not an envelope object")
	}
	ports := map[string]any{}
	edges := map[string]any{}
	portValues := map[string][]any{}
	edgeValues := map[string][]any{}
	for _, binding := range execution.Inputs.Bindings() {
		value, err := decodeConditionJSON(binding.Value)
		if err != nil {
			return nil, fmt.Errorf("decode immutable condition edge %s: %w", binding.EdgeID, err)
		}
		portValues[binding.ToPort] = append(portValues[binding.ToPort], value)
		edgeValues[binding.EdgeID] = append(edgeValues[binding.EdgeID], value)
	}
	for port, values := range portValues {
		ports[port] = values
	}
	for edge, values := range edgeValues {
		edges[edge] = values
	}
	// These are deterministic indexes over the hash-bound bindings, not another
	// source of state. They let definitions address named ports/edges without
	// depending on a global node key or scheduler completion order.
	inputObject["ports"] = ports
	inputObject["edges"] = edges
	scope, err := decodeConditionJSON(execution.Run.Scope)
	if err != nil {
		return nil, fmt.Errorf("decode immutable run scope: %w", err)
	}
	definition := map[string]any{
		"id": execution.Run.Definition.ID, "version": execution.Run.Definition.Version,
		"hash": execution.Run.Definition.Hash,
	}
	executionProfile := map[string]any{
		"version": execution.Run.ExecutionProfile.Version, "hash": execution.Run.ExecutionProfile.Hash,
	}
	var inputManifest any
	if execution.Run.InputManifest != nil {
		inputManifest = map[string]any{
			"id": execution.Run.InputManifest.ID, "hash": execution.Run.InputManifest.Hash,
		}
	}
	run := map[string]any{
		"id": execution.Run.ID, "projectId": execution.Run.ProjectID,
		"definitionVersionId": execution.Run.DefinitionVersionID,
		"definition":          definition, "executionProfile": executionProfile,
		"inputManifest": inputManifest,
	}
	var sliceIdentity any
	if execution.Node.SliceID != "" {
		slice, exists := execution.Run.Context.Slices[execution.Node.SliceID]
		if !exists || slice.ID != execution.Node.SliceID || slice.Key == "" || slice.FanOutNodeID == "" {
			return nil, fmt.Errorf("current condition slice identity is missing or stale")
		}
		sliceIdentity = map[string]any{
			"id": slice.ID, "key": slice.Key, "title": slice.Title,
			"fanOutNodeId": slice.FanOutNodeID,
		}
	}
	return map[string]any{
		"run": run, "scope": scope, "inputs": inputObject, "slice": sliceIdentity,
	}, nil
}

func validateConditionRuleRootsV1(raw json.RawMessage, depth int) error {
	if depth > maxConditionDepth {
		return fmt.Errorf("condition nesting exceeds %d", maxConditionDepth)
	}
	decoded, err := decodeConditionJSON(raw)
	if err != nil {
		return err
	}
	if _, literal := decoded.(bool); literal {
		return nil
	}
	if _, ok := decoded.(map[string]any); !ok {
		return fmt.Errorf("expression must be a boolean or rule object")
	}
	var rule declarativeRule
	if err := json.Unmarshal(raw, &rule); err != nil {
		return err
	}
	for _, children := range [][]json.RawMessage{rule.All, rule.Any} {
		for _, child := range children {
			if err := validateConditionRuleRootsV1(child, depth+1); err != nil {
				return err
			}
		}
	}
	if len(rule.Not) > 0 {
		return validateConditionRuleRootsV1(rule.Not, depth+1)
	}
	if rule.Path == "" {
		return nil // structural validation reports a malformed rule separately
	}
	root, err := conditionPathRoot(rule.Path)
	if err != nil {
		return err
	}
	switch root {
	case "run", "scope", "inputs", "slice":
		return nil
	default:
		return fmt.Errorf("condition path root %q is forbidden; use /run, /scope, /inputs, or /slice", root)
	}
}

func conditionPathRoot(pointer string) (string, error) {
	if !strings.HasPrefix(pointer, "/") {
		return "", fmt.Errorf("condition path must be a JSON pointer")
	}
	encoded := strings.SplitN(pointer[1:], "/", 2)[0]
	if encoded == "" {
		return "", fmt.Errorf("condition path must select an allowed root")
	}
	for index := 0; index < len(encoded); index++ {
		if encoded[index] != '~' {
			continue
		}
		if index+1 >= len(encoded) || encoded[index+1] != '0' && encoded[index+1] != '1' {
			return "", fmt.Errorf("condition path contains an invalid JSON pointer escape")
		}
		index++
	}
	return strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~"), nil
}

func conditionContext(execution Execution) (map[string]any, error) {
	root := map[string]any{
		"run": map[string]any{
			"id": execution.Run.ID, "projectId": execution.Run.ProjectID,
			"status": execution.Run.Status, "startedBy": execution.Run.StartedBy,
		},
	}
	scope, err := decodeConditionJSON(execution.Run.Scope)
	if err != nil {
		return nil, fmt.Errorf("decode run scope: %w", err)
	}
	root["scope"] = scope
	values := make(map[string]any, len(execution.Run.Context.Values))
	for key, raw := range execution.Run.Context.Values {
		value, err := decodeConditionJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("decode context value %q: %w", key, err)
		}
		values[key] = value
	}
	root["values"] = values
	nodes := make(map[string]any, len(execution.Run.Context.Nodes))
	for key, metadata := range execution.Run.Context.Nodes {
		if len(metadata.Output) == 0 {
			continue
		}
		value, err := decodeConditionJSON(metadata.Output)
		if err != nil {
			return nil, fmt.Errorf("decode node output %q: %w", key, err)
		}
		nodes[key] = value
	}
	root["nodes"] = nodes
	slices, err := json.Marshal(execution.Run.Context.Slices)
	if err != nil {
		return nil, err
	}
	decodedSlices, err := decodeConditionJSON(slices)
	if err != nil {
		return nil, err
	}
	root["slices"] = decodedSlices
	return root, nil
}

type declarativeRule struct {
	All   []json.RawMessage `json:"all,omitempty"`
	Any   []json.RawMessage `json:"any,omitempty"`
	Not   json.RawMessage   `json:"not,omitempty"`
	Path  string            `json:"path,omitempty"`
	Op    string            `json:"op,omitempty"`
	Value json.RawMessage   `json:"value,omitempty"`
}

func evaluateConditionRule(raw json.RawMessage, root any, depth int) (bool, error) {
	if depth > maxConditionDepth {
		return false, fmt.Errorf("condition nesting exceeds %d", maxConditionDepth)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var literal any
	if err := decoder.Decode(&literal); err != nil {
		return false, fmt.Errorf("expression must be JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return false, fmt.Errorf("expression must contain exactly one JSON value")
	}
	if boolean, ok := literal.(bool); ok {
		return boolean, nil
	}
	object, ok := literal.(map[string]any)
	if !ok {
		return false, fmt.Errorf("expression must be a boolean or rule object")
	}
	allowed := map[string]bool{"all": true, "any": true, "not": true, "path": true, "op": true, "value": true}
	for key := range object {
		if !allowed[key] {
			return false, fmt.Errorf("unknown rule field %q", key)
		}
	}
	var rule declarativeRule
	if err := json.Unmarshal(raw, &rule); err != nil {
		return false, err
	}
	forms := 0
	if rule.All != nil {
		forms++
	}
	if rule.Any != nil {
		forms++
	}
	if len(rule.Not) > 0 {
		forms++
	}
	if rule.Path != "" {
		forms++
	}
	if forms != 1 {
		return false, fmt.Errorf("rule must contain exactly one of all, any, not or path")
	}
	if rule.All != nil {
		if len(rule.All) == 0 {
			return false, fmt.Errorf("all requires at least one child rule")
		}
		for _, child := range rule.All {
			matched, err := evaluateConditionRule(child, root, depth+1)
			if err != nil || !matched {
				return false, err
			}
		}
		return true, nil
	}
	if rule.Any != nil {
		if len(rule.Any) == 0 {
			return false, fmt.Errorf("any requires at least one child rule")
		}
		for _, child := range rule.Any {
			matched, err := evaluateConditionRule(child, root, depth+1)
			if err != nil {
				return false, err
			}
			if matched {
				return true, nil
			}
		}
		return false, nil
	}
	if len(rule.Not) > 0 {
		matched, err := evaluateConditionRule(rule.Not, root, depth+1)
		return !matched, err
	}
	return evaluateLeafRule(rule, root)
}

func evaluateLeafRule(rule declarativeRule, root any) (bool, error) {
	value, exists, err := resolveJSONPointer(root, rule.Path)
	if err != nil {
		return false, err
	}
	op := strings.ToLower(strings.TrimSpace(rule.Op))
	if op == "" {
		op = "truthy"
	}
	switch op {
	case "exists":
		if len(rule.Value) > 0 {
			return false, fmt.Errorf("exists does not accept value")
		}
		return exists, nil
	case "truthy":
		if len(rule.Value) > 0 {
			return false, fmt.Errorf("truthy does not accept value")
		}
		return exists && conditionTruthy(value), nil
	case "eq", "ne", "gt", "gte", "lt", "lte":
		if !exists || len(rule.Value) == 0 {
			return false, nil
		}
		expected, err := decodeConditionJSON(rule.Value)
		if err != nil {
			return false, err
		}
		comparison, comparable := compareConditionValues(value, expected)
		if !comparable {
			if op == "eq" || op == "ne" {
				equal := reflect.DeepEqual(value, expected)
				if op == "ne" {
					return !equal, nil
				}
				return equal, nil
			}
			return false, fmt.Errorf("%s requires two numbers or two strings", op)
		}
		switch op {
		case "eq":
			return comparison == 0, nil
		case "ne":
			return comparison != 0, nil
		case "gt":
			return comparison > 0, nil
		case "gte":
			return comparison >= 0, nil
		case "lt":
			return comparison < 0, nil
		default:
			return comparison <= 0, nil
		}
	default:
		return false, fmt.Errorf("unsupported condition operation %q", rule.Op)
	}
}

func resolveJSONPointer(root any, pointer string) (any, bool, error) {
	if pointer == "" {
		return root, true, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false, fmt.Errorf("condition path must be a JSON pointer")
	}
	current := root
	for _, encoded := range strings.Split(pointer[1:], "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch value := current.(type) {
		case map[string]any:
			next, exists := value[token]
			if !exists {
				return nil, false, nil
			}
			current = next
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(value) {
				return nil, false, nil
			}
			current = value[index]
		default:
			return nil, false, nil
		}
	}
	return current, true, nil
}

func conditionTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		number, ok := new(big.Rat).SetString(typed.String())
		return ok && number.Sign() != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func compareConditionValues(left, right any) (int, bool) {
	leftNumber, leftIsNumber := left.(json.Number)
	rightNumber, rightIsNumber := right.(json.Number)
	if leftIsNumber && rightIsNumber {
		leftRat, leftOK := new(big.Rat).SetString(leftNumber.String())
		rightRat, rightOK := new(big.Rat).SetString(rightNumber.String())
		if !leftOK || !rightOK {
			return 0, false
		}
		return leftRat.Cmp(rightRat), true
	}
	leftString, leftIsString := left.(string)
	rightString, rightIsString := right.(string)
	if leftIsString && rightIsString {
		return strings.Compare(leftString, rightString), true
	}
	if reflect.DeepEqual(left, right) {
		return 0, true
	}
	return 0, false
}

func decodeConditionJSON(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("JSON value has trailing data")
	}
	return value, nil
}
