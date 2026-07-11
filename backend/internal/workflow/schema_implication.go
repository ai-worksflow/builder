package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/worksflow/builder/backend/internal/domain"
)

// validateCurrentDefinitionPortSchemas is the current authoring/profile seam
// for typed edge compatibility. domain.WorkflowDefinition.Validate retains its
// historical shallow check so pre-pin definitions remain replayable, while a
// newly authored governed definition must prove that every mapped output value
// is accepted by its target input schema. An implication that this deliberately
// conservative prover cannot establish fails closed.
func validateCurrentDefinitionPortSchemas(definition domain.WorkflowDefinition) error {
	type compiledPorts struct {
		input  map[string]compiledPortSchema
		output map[string]compiledPortSchema
	}
	compiled := make(map[string]compiledPorts, len(definition.Nodes))
	for _, node := range definition.Nodes {
		inputs, err := node.ResolvedInputPorts()
		if err != nil {
			return capabilityError("workflow.nodes."+node.ID+".inputPorts", err.Error())
		}
		outputs, err := node.ResolvedOutputPorts()
		if err != nil {
			return capabilityError("workflow.nodes."+node.ID+".outputPorts", err.Error())
		}
		compiledInputs, err := compilePortSchemas(node.ID, "input", inputs)
		if err != nil {
			return err
		}
		compiledOutputs, err := compilePortSchemas(node.ID, "output", outputs)
		if err != nil {
			return err
		}
		compiled[node.ID] = compiledPorts{input: compiledInputs, output: compiledOutputs}
	}

	for _, edge := range definition.Edges {
		fromPort, toPort := normalizedPort(edge.FromPort), normalizedPort(edge.ToPort)
		source, sourceOK := compiled[edge.From].output[fromPort]
		target, targetOK := compiled[edge.To].input[toPort]
		if !sourceOK || !targetOK {
			// The domain validator reports missing ports. Avoid replacing that more
			// precise structural error if this helper is called independently.
			continue
		}
		var err error
		if len(edge.Mapping) == 0 {
			if bytes.Equal(source.canonical, target.canonical) {
				continue
			}
			err = proveSchemaImplication(source.schema, target.schema, map[schemaPair]bool{})
		} else {
			err = proveMappedObjectImplication(source.schema, target.schema, edge.Mapping)
		}
		if err != nil {
			return capabilityError(
				"workflow.edges."+edge.ID+".mapping",
				fmt.Sprintf("%s:%s cannot safely feed %s:%s: %v", edge.From, fromPort, edge.To, toPort, err),
			)
		}
	}
	return nil
}

type compiledPortSchema struct {
	canonical json.RawMessage
	schema    *jsonschema.Schema
}

func compilePortSchemas(nodeID, direction string, ports map[string]domain.PortDefinition) (map[string]compiledPortSchema, error) {
	names := make([]string, 0, len(ports))
	for name := range ports {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make(map[string]compiledPortSchema, len(ports))
	for _, name := range names {
		raw := append(json.RawMessage(nil), ports[name].Schema...)
		canonical, err := domain.CanonicalJSON(raw)
		if err != nil {
			return nil, capabilityError("workflow.nodes."+nodeID+"."+direction+"Ports."+name+".schema", err.Error())
		}
		compiler := jsonschema.NewCompiler()
		compiler.Draft = jsonschema.Draft2020
		compiler.LoadURL = func(string) (io.ReadCloser, error) {
			return nil, fmt.Errorf("external JSON Schema references are disabled")
		}
		const resource = "memory://workflow-authoring/port-schema.json"
		if err := compiler.AddResource(resource, bytes.NewReader(raw)); err != nil {
			return nil, capabilityError("workflow.nodes."+nodeID+"."+direction+"Ports."+name+".schema", err.Error())
		}
		compiled, err := compiler.Compile(resource)
		if err != nil {
			return nil, capabilityError("workflow.nodes."+nodeID+"."+direction+"Ports."+name+".schema", err.Error())
		}
		result[name] = compiledPortSchema{canonical: canonical, schema: compiled}
	}
	return result, nil
}

type schemaPair struct {
	source *jsonschema.Schema
	target *jsonschema.Schema
}

// proveSchemaImplication proves source => target for the deterministic subset
// used by workflow ports. Unsupported target assertions are rejected. Source
// assertions may be ignored only when the remaining, broader source shape is
// already sufficient to prove the target, which is conservative.
func proveSchemaImplication(source, target *jsonschema.Schema, visiting map[schemaPair]bool) error {
	if source == nil || target == nil {
		return fmt.Errorf("compiled schema is unavailable")
	}
	if target.Always != nil && *target.Always {
		return nil
	}
	if source.Always != nil && !*source.Always {
		return nil // false implies every schema
	}
	if target.Always != nil && !*target.Always {
		return fmt.Errorf("target schema rejects every value")
	}
	pair := schemaPair{source: source, target: target}
	if visiting[pair] {
		return fmt.Errorf("recursive schema implication cannot be proven")
	}
	visiting[pair] = true
	defer delete(visiting, pair)

	if schemaHasFiniteValues(source) {
		return proveFiniteValues(source, target)
	}
	if target.Ref != nil {
		if err := proveSchemaImplication(source, target.Ref, visiting); err != nil {
			return fmt.Errorf("target $ref: %w", err)
		}
	}
	for index, branch := range target.AllOf {
		if err := proveSchemaImplication(source, branch, visiting); err != nil {
			return fmt.Errorf("target allOf[%d]: %w", index, err)
		}
	}
	if len(target.AnyOf) > 0 {
		matched := false
		for _, branch := range target.AnyOf {
			if proveSchemaImplication(source, branch, visiting) == nil {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("source is not proven to satisfy any target anyOf branch")
		}
	}
	if len(target.OneOf) > 0 || target.Not != nil || target.If != nil || target.Then != nil || target.Else != nil || target.DynamicRef != nil || target.RecursiveRef != nil {
		return fmt.Errorf("target uses a conditional/composite assertion whose implication cannot be proven")
	}

	if err := proveDirectSchemaAssertions(source, target, visiting); err == nil {
		return nil
	} else {
		directErr := err
		// A source reference/allOf member is an additional conjunct. If any one
		// of those narrower schemas alone proves the target, the full source does.
		if source.Ref != nil && proveSchemaImplication(source.Ref, target, visiting) == nil {
			return nil
		}
		for _, branch := range source.AllOf {
			if proveSchemaImplication(branch, target, visiting) == nil {
				return nil
			}
		}
		// For a source union every alternative must imply the target. This proof
		// intentionally ignores sibling constraints and may reject a valid but
		// non-trivial schema rather than accepting an unsafe edge.
		for _, union := range [][]*jsonschema.Schema{source.AnyOf, source.OneOf} {
			if len(union) == 0 {
				continue
			}
			all := true
			for _, branch := range union {
				if proveSchemaImplication(branch, target, visiting) != nil {
					all = false
					break
				}
			}
			if all {
				return nil
			}
		}
		return directErr
	}
}

func proveDirectSchemaAssertions(source, target *jsonschema.Schema, visiting map[schemaPair]bool) error {
	if err := proveTypeSubset(source, target); err != nil {
		return err
	}
	if len(target.Constant) > 0 || len(target.Enum) > 0 {
		return fmt.Errorf("target const/enum is not guaranteed by the source")
	}
	if formatIsAsserted(target) && (source.Format != target.Format || !formatIsAsserted(source)) {
		return fmt.Errorf("target format %q is not guaranteed by an asserted source format", target.Format)
	}

	if hasObjectAssertions(target) && schemaMayProduceType(source, "object") {
		if err := proveObjectImplication(source, target, visiting); err != nil {
			return err
		}
	}
	if hasArrayAssertions(target) && schemaMayProduceType(source, "array") {
		if err := proveArrayImplication(source, target, visiting); err != nil {
			return err
		}
	}
	if hasStringAssertions(target) && schemaMayProduceType(source, "string") {
		if err := proveStringImplication(source, target, visiting); err != nil {
			return err
		}
	}
	if hasNumberAssertions(target) && (schemaMayProduceType(source, "number") || schemaMayProduceType(source, "integer")) {
		if err := proveNumberImplication(source, target); err != nil {
			return err
		}
	}
	return nil
}

func schemaMayProduceType(schema *jsonschema.Schema, targetType string) bool {
	types := effectiveSchemaTypes(schema)
	if len(types) == 0 {
		return true
	}
	for _, sourceType := range types {
		if sourceType == targetType || sourceType == "integer" && targetType == "number" {
			return true
		}
	}
	return false
}

func hasObjectAssertions(schema *jsonschema.Schema) bool {
	return schema.MinProperties >= 0 || schema.MaxProperties >= 0 || len(schema.Required) > 0 ||
		len(schema.Properties) > 0 || schema.PropertyNames != nil || len(schema.PatternProperties) > 0 ||
		schema.AdditionalProperties != nil || len(schema.Dependencies) > 0 || len(schema.DependentRequired) > 0 ||
		len(schema.DependentSchemas) > 0 || schema.UnevaluatedProperties != nil
}

func hasArrayAssertions(schema *jsonschema.Schema) bool {
	return schema.MinItems >= 0 || schema.MaxItems >= 0 || schema.UniqueItems || schema.Items != nil ||
		schema.Items2020 != nil || len(schema.PrefixItems) > 0 || schema.AdditionalItems != nil ||
		schema.Contains != nil || schema.UnevaluatedItems != nil
}

func hasStringAssertions(schema *jsonschema.Schema) bool {
	return schema.MinLength >= 0 || schema.MaxLength >= 0 || schema.Pattern != nil ||
		schema.ContentEncoding != "" || schema.ContentMediaType != "" || schema.ContentSchema != nil
}

func hasNumberAssertions(schema *jsonschema.Schema) bool {
	return schema.Minimum != nil || schema.ExclusiveMinimum != nil || schema.Maximum != nil ||
		schema.ExclusiveMaximum != nil || schema.MultipleOf != nil
}

// The runtime compiler keeps format as an annotation for the built-in
// 2019-09/2020-12 metaschemas, but drafts 4/6/7 assert it. Merely comparing the
// public Format strings across drafts would therefore let an annotation-only
// source feed an asserted legacy target without actually guaranteeing it.
func formatIsAsserted(schema *jsonschema.Schema) bool {
	if schema == nil || schema.Format == "" {
		return false
	}
	return schema.Draft == jsonschema.Draft4 || schema.Draft == jsonschema.Draft6 || schema.Draft == jsonschema.Draft7
}

func proveTypeSubset(source, target *jsonschema.Schema) error {
	targetTypes := effectiveSchemaTypes(target)
	if len(targetTypes) == 0 {
		return nil
	}
	sourceTypes := effectiveSchemaTypes(source)
	if len(sourceTypes) == 0 {
		return fmt.Errorf("source type is unconstrained while target requires %s", strings.Join(targetTypes, "|"))
	}
	for _, sourceType := range sourceTypes {
		matched := false
		for _, targetType := range targetTypes {
			if sourceType == targetType || sourceType == "integer" && targetType == "number" {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("source type %s is not accepted by target type %s", sourceType, strings.Join(targetTypes, "|"))
		}
	}
	return nil
}

func effectiveSchemaTypes(schema *jsonschema.Schema) []string {
	return append([]string(nil), schema.Types...)
}

func schemaHasFiniteValues(schema *jsonschema.Schema) bool {
	return len(schema.Constant) > 0 || len(schema.Enum) > 0
}

func proveFiniteValues(source, target *jsonschema.Schema) error {
	values := source.Enum
	if len(source.Constant) > 0 {
		values = source.Constant
	}
	valid := 0
	for _, value := range values {
		if source.Validate(value) != nil {
			continue
		}
		valid++
		if err := target.Validate(value); err != nil {
			return fmt.Errorf("source finite value is rejected by target: %v", err)
		}
	}
	if valid == 0 {
		return nil
	}
	return nil
}

func proveObjectImplication(source, target *jsonschema.Schema, visiting map[schemaPair]bool) error {
	if len(target.PatternProperties) > 0 || target.PropertyNames != nil || len(target.Dependencies) > 0 ||
		len(target.DependentRequired) > 0 || len(target.DependentSchemas) > 0 || target.UnevaluatedProperties != nil {
		return fmt.Errorf("target object uses pattern/dependent/unevaluated assertions whose implication cannot be proven")
	}
	if len(source.PatternProperties) > 0 && !objectAcceptsEveryPropertyValue(target) {
		// A pattern-matched property is evaluated by patternProperties, so it is
		// not governed by additionalProperties. Treating the latter as its schema
		// can silently skip a real value (for example ^x$:integer against x:string).
		return fmt.Errorf("source patternProperties cannot be proven against a constrained target object")
	}
	sourceRequired := boolStringSet(source.Required)
	targetRequired := boolStringSet(target.Required)
	for name := range targetRequired {
		if !sourceRequired[name] {
			return fmt.Errorf("required target property %q is not guaranteed upstream", name)
		}
	}
	for name, targetProperty := range target.Properties {
		sourceProperty, possible := schemaForPossibleProperty(source, name)
		if !possible {
			continue
		}
		if err := proveSchemaImplication(sourceProperty, targetProperty, visiting); err != nil {
			return fmt.Errorf("property %q: %w", name, err)
		}
	}
	targetAdditional := additionalPropertySchema(target)
	for name, sourceProperty := range source.Properties {
		if _, declared := target.Properties[name]; declared {
			continue
		}
		if err := proveAdditionalProperty(sourceProperty, targetAdditional, visiting); err != nil {
			return fmt.Errorf("source property %q is not accepted by target additionalProperties: %w", name, err)
		}
	}
	if err := proveUnknownAdditionalProperties(source, targetAdditional, visiting); err != nil {
		return err
	}
	if target.MinProperties >= 0 {
		sourceMinimum := len(sourceRequired)
		if source.MinProperties > sourceMinimum {
			sourceMinimum = source.MinProperties
		}
		if sourceMinimum < target.MinProperties {
			return fmt.Errorf("target minProperties=%d is not guaranteed upstream", target.MinProperties)
		}
	}
	if target.MaxProperties >= 0 {
		sourceMaximum, bounded := maximumObjectProperties(source)
		if !bounded || sourceMaximum > target.MaxProperties {
			return fmt.Errorf("target maxProperties=%d may be exceeded upstream", target.MaxProperties)
		}
	}
	return nil
}

func proveMappedObjectImplication(source, target *jsonschema.Schema, mapping map[string]string) error {
	if !schemaAcceptsOnlyObject(source) || !schemaAcceptsOnlyObject(target) {
		return fmt.Errorf("edge mappings require object-only source and target schemas")
	}
	if source.Ref != nil || target.Ref != nil || len(source.AllOf)+len(source.AnyOf)+len(source.OneOf) > 0 ||
		len(target.AllOf)+len(target.AnyOf)+len(target.OneOf) > 0 || source.Not != nil || target.Not != nil {
		return fmt.Errorf("mapped composite schema implication cannot be proven")
	}
	if len(target.PatternProperties) > 0 || target.PropertyNames != nil || len(target.Dependencies) > 0 ||
		len(target.DependentRequired) > 0 || len(target.DependentSchemas) > 0 || target.UnevaluatedProperties != nil ||
		target.MinProperties >= 0 || target.MaxProperties >= 0 {
		return fmt.Errorf("mapped target uses object assertions whose implication cannot be proven")
	}
	if len(source.PatternProperties) > 0 {
		return fmt.Errorf("mapped source patternProperties cannot be proven against target properties")
	}
	sourceRequired := boolStringSet(source.Required)
	for targetName, sourceName := range mapping {
		if _, exists := target.Properties[targetName]; !exists {
			return fmt.Errorf("mapping target %q is not declared by the target schema", targetName)
		}
		if !sourceRequired[sourceName] {
			return fmt.Errorf("mapping source %q is not required upstream and may be absent at runtime", sourceName)
		}
		if _, possible := schemaForPossibleProperty(source, sourceName); !possible {
			return fmt.Errorf("mapping source %q cannot be produced upstream", sourceName)
		}
	}

	transformedRequired := boolStringSet(source.Required)
	for targetName := range mapping {
		transformedRequired[targetName] = true
	}
	for _, name := range target.Required {
		if !transformedRequired[name] {
			return fmt.Errorf("required target property %q is not guaranteed after mapping", name)
		}
	}
	visiting := map[schemaPair]bool{}
	for name, targetProperty := range target.Properties {
		sourceName, mapped := mapping[name]
		var sourceProperty *jsonschema.Schema
		var possible bool
		if mapped {
			sourceProperty, possible = schemaForPossibleProperty(source, sourceName)
		} else {
			sourceProperty, possible = schemaForPossibleProperty(source, name)
		}
		if !possible {
			continue
		}
		if err := proveSchemaImplication(sourceProperty, targetProperty, visiting); err != nil {
			return fmt.Errorf("mapped property %q: %w", name, err)
		}
	}
	targetAdditional := additionalPropertySchema(target)
	for name, sourceProperty := range source.Properties {
		if _, declared := target.Properties[name]; declared {
			continue
		}
		if err := proveAdditionalProperty(sourceProperty, targetAdditional, visiting); err != nil {
			return fmt.Errorf("preserved source property %q is not accepted by target additionalProperties: %w", name, err)
		}
	}
	if err := proveUnknownAdditionalProperties(source, targetAdditional, visiting); err != nil {
		return err
	}
	return nil
}

func schemaAcceptsOnlyObject(schema *jsonschema.Schema) bool {
	types := effectiveSchemaTypes(schema)
	return len(types) == 1 && types[0] == "object"
}

type additionalSchema struct {
	allowed bool
	schema  *jsonschema.Schema
}

func additionalPropertySchema(schema *jsonschema.Schema) additionalSchema {
	switch typed := schema.AdditionalProperties.(type) {
	case bool:
		return additionalSchema{allowed: typed}
	case *jsonschema.Schema:
		return additionalSchema{allowed: true, schema: typed}
	default:
		return additionalSchema{allowed: true}
	}
}

func objectAcceptsEveryPropertyValue(schema *jsonschema.Schema) bool {
	if len(schema.Required) > 0 || len(schema.Properties) > 0 || schema.MinProperties > 0 || schema.MaxProperties >= 0 ||
		schema.PropertyNames != nil || len(schema.PatternProperties) > 0 || len(schema.Dependencies) > 0 ||
		len(schema.DependentRequired) > 0 || len(schema.DependentSchemas) > 0 || schema.UnevaluatedProperties != nil {
		return false
	}
	additional := additionalPropertySchema(schema)
	return additional.allowed && additional.schema == nil
}

func schemaForPossibleProperty(schema *jsonschema.Schema, name string) (*jsonschema.Schema, bool) {
	if property := schema.Properties[name]; property != nil {
		return property, true
	}
	additional := additionalPropertySchema(schema)
	if !additional.allowed {
		return nil, false
	}
	if additional.schema != nil {
		return additional.schema, true
	}
	return unconstrainedCompiledSchema(), true
}

func proveAdditionalProperty(source *jsonschema.Schema, target additionalSchema, visiting map[schemaPair]bool) error {
	if !target.allowed {
		return fmt.Errorf("additional properties are forbidden")
	}
	if target.schema == nil {
		return nil
	}
	return proveSchemaImplication(source, target.schema, visiting)
}

func proveUnknownAdditionalProperties(source *jsonschema.Schema, target additionalSchema, visiting map[schemaPair]bool) error {
	if len(source.PatternProperties) > 0 || source.PropertyNames != nil || source.UnevaluatedProperties != nil {
		if target.allowed && target.schema == nil {
			return nil
		}
		return fmt.Errorf("source pattern/unevaluated properties are not safely accepted by target")
	}
	sourceAdditional := additionalPropertySchema(source)
	if !sourceAdditional.allowed {
		return nil
	}
	if !target.allowed {
		return fmt.Errorf("source permits undeclared properties but target forbids them")
	}
	if target.schema == nil {
		return nil
	}
	if sourceAdditional.schema == nil {
		return fmt.Errorf("source permits unconstrained additional properties")
	}
	return proveSchemaImplication(sourceAdditional.schema, target.schema, visiting)
}

func maximumObjectProperties(schema *jsonschema.Schema) (int, bool) {
	if schema.MaxProperties >= 0 {
		return schema.MaxProperties, true
	}
	additional := additionalPropertySchema(schema)
	if additional.allowed || len(schema.PatternProperties) > 0 {
		return 0, false
	}
	return len(schema.Properties), true
}

func proveArrayImplication(source, target *jsonschema.Schema, visiting map[schemaPair]bool) error {
	if len(target.PrefixItems) > 0 || target.Contains != nil || target.UnevaluatedItems != nil || target.AdditionalItems != nil {
		return fmt.Errorf("target array uses tuple/contains/unevaluated assertions whose implication cannot be proven")
	}
	if target.Items != nil {
		if _, homogeneous := target.Items.(*jsonschema.Schema); !homogeneous {
			return fmt.Errorf("target array uses legacy tuple items whose implication cannot be proven")
		}
	}
	if target.MinItems >= 0 && (source.MinItems < 0 || source.MinItems < target.MinItems) {
		return fmt.Errorf("target minItems=%d is not guaranteed upstream", target.MinItems)
	}
	if target.MaxItems >= 0 && (source.MaxItems < 0 || source.MaxItems > target.MaxItems) {
		return fmt.Errorf("target maxItems=%d may be exceeded upstream", target.MaxItems)
	}
	if target.UniqueItems && !source.UniqueItems {
		return fmt.Errorf("target uniqueItems is not guaranteed upstream")
	}
	targetItems := homogeneousItems(target)
	if targetItems == nil {
		return nil
	}
	if len(source.PrefixItems) > 0 {
		return fmt.Errorf("source prefixItems cannot be ignored when proving homogeneous target items")
	}
	sourceItems := homogeneousItems(source)
	if sourceItems == nil {
		return fmt.Errorf("source array items are unconstrained")
	}
	if err := proveSchemaImplication(sourceItems, targetItems, visiting); err != nil {
		return fmt.Errorf("array items: %w", err)
	}
	return nil
}

func homogeneousItems(schema *jsonschema.Schema) *jsonschema.Schema {
	if schema.Items2020 != nil {
		return schema.Items2020
	}
	if items, ok := schema.Items.(*jsonschema.Schema); ok {
		return items
	}
	return nil
}

func proveStringImplication(source, target *jsonschema.Schema, visiting map[schemaPair]bool) error {
	if target.MinLength >= 0 && (source.MinLength < 0 || source.MinLength < target.MinLength) {
		return fmt.Errorf("target minLength=%d is not guaranteed upstream", target.MinLength)
	}
	if target.MaxLength >= 0 && (source.MaxLength < 0 || source.MaxLength > target.MaxLength) {
		return fmt.Errorf("target maxLength=%d may be exceeded upstream", target.MaxLength)
	}
	if target.Pattern != nil && (source.Pattern == nil || source.Pattern.String() != target.Pattern.String()) {
		return fmt.Errorf("target pattern %q is not guaranteed upstream", target.Pattern.String())
	}
	if contentIsAsserted(target) {
		if !contentIsAsserted(source) {
			return fmt.Errorf("target content assertions are not guaranteed by an annotation-only source")
		}
		if target.ContentEncoding != source.ContentEncoding {
			return fmt.Errorf("target contentEncoding %q is not guaranteed upstream", target.ContentEncoding)
		}
		if target.ContentMediaType != source.ContentMediaType {
			return fmt.Errorf("target contentMediaType %q is not guaranteed upstream", target.ContentMediaType)
		}
		if target.ContentSchema != nil {
			if source.ContentSchema == nil {
				return fmt.Errorf("target contentSchema is not guaranteed upstream")
			}
			if err := proveSchemaImplication(source.ContentSchema, target.ContentSchema, visiting); err != nil {
				return fmt.Errorf("contentSchema: %w", err)
			}
		}
	}
	return nil
}

func contentIsAsserted(schema *jsonschema.Schema) bool {
	if schema == nil || schema.ContentEncoding == "" && schema.ContentMediaType == "" && schema.ContentSchema == nil {
		return false
	}
	// With the compiler options shared by runtime validation, content keywords
	// are assertions in draft-07. In 2019-09/2020-12 they are annotations and
	// the compiler clears ContentSchema unless AssertContent is explicitly set.
	return schema.Draft == jsonschema.Draft7 || schema.ContentSchema != nil
}

func proveNumberImplication(source, target *jsonschema.Schema) error {
	if !lowerBoundImplies(source.Minimum, source.ExclusiveMinimum, target.Minimum, target.ExclusiveMinimum) {
		return fmt.Errorf("target lower bound is not guaranteed upstream")
	}
	if !upperBoundImplies(source.Maximum, source.ExclusiveMaximum, target.Maximum, target.ExclusiveMaximum) {
		return fmt.Errorf("target upper bound may be exceeded upstream")
	}
	if target.MultipleOf != nil {
		if source.MultipleOf == nil {
			return fmt.Errorf("target multipleOf is not guaranteed upstream")
		}
		quotient := new(big.Rat).Quo(source.MultipleOf, target.MultipleOf)
		if !quotient.IsInt() {
			return fmt.Errorf("source multipleOf does not imply target multipleOf")
		}
	}
	return nil
}

func lowerBoundImplies(sourceInclusive, sourceExclusive, targetInclusive, targetExclusive *big.Rat) bool {
	if targetInclusive == nil && targetExclusive == nil {
		return true
	}
	source, sourceIsExclusive, exists := strongestLowerBound(sourceInclusive, sourceExclusive)
	if !exists {
		return false
	}
	target, targetIsExclusive, _ := strongestLowerBound(targetInclusive, targetExclusive)
	comparison := source.Cmp(target)
	return comparison > 0 || comparison == 0 && (!targetIsExclusive || sourceIsExclusive)
}

func upperBoundImplies(sourceInclusive, sourceExclusive, targetInclusive, targetExclusive *big.Rat) bool {
	if targetInclusive == nil && targetExclusive == nil {
		return true
	}
	source, sourceIsExclusive, exists := strongestUpperBound(sourceInclusive, sourceExclusive)
	if !exists {
		return false
	}
	target, targetIsExclusive, _ := strongestUpperBound(targetInclusive, targetExclusive)
	comparison := source.Cmp(target)
	return comparison < 0 || comparison == 0 && (!targetIsExclusive || sourceIsExclusive)
}

func strongestLowerBound(inclusive, exclusive *big.Rat) (*big.Rat, bool, bool) {
	if inclusive == nil && exclusive == nil {
		return nil, false, false
	}
	if inclusive == nil {
		return exclusive, true, true
	}
	if exclusive == nil {
		return inclusive, false, true
	}
	comparison := inclusive.Cmp(exclusive)
	if comparison > 0 {
		return inclusive, false, true
	}
	return exclusive, true, true
}

func strongestUpperBound(inclusive, exclusive *big.Rat) (*big.Rat, bool, bool) {
	if inclusive == nil && exclusive == nil {
		return nil, false, false
	}
	if inclusive == nil {
		return exclusive, true, true
	}
	if exclusive == nil {
		return inclusive, false, true
	}
	comparison := inclusive.Cmp(exclusive)
	if comparison < 0 {
		return inclusive, false, true
	}
	return exclusive, true, true
}

func boolStringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

var (
	openCompiledSchemaOnce sync.Once
	openCompiledSchema     *jsonschema.Schema
)

func unconstrainedCompiledSchema() *jsonschema.Schema {
	openCompiledSchemaOnce.Do(func() {
		compiler := jsonschema.NewCompiler()
		compiler.Draft = jsonschema.Draft2020
		const resource = "memory://workflow-authoring/unconstrained.json"
		_ = compiler.AddResource(resource, strings.NewReader(`{}`))
		openCompiledSchema, _ = compiler.Compile(resource)
	})
	return openCompiledSchema
}
