package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/worksflow/builder/backend/internal/domain"
)

func validateNodeOutput(definition domain.NodeDefinition, output json.RawMessage) error {
	ports, err := definition.ResolvedOutputPorts()
	if err != nil {
		return err
	}
	port, exists := ports["default"]
	if !exists {
		if len(ports) != 1 {
			return validateNamedPortOutputs(ports, output)
		}
		for _, only := range ports {
			port = only
		}
	}
	return validateAgainstSchema("output", port.Schema, output)
}

func validateNamedPortOutputs(ports map[string]domain.PortDefinition, output json.RawMessage) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(output, &object); err != nil {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "output.ports", Message: "multiple output ports require an object keyed by port name"}
	}
	values := object
	if rawPorts, exists := object["ports"]; exists {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(rawPorts, &nested); err != nil {
			return &domain.DomainError{Kind: domain.ErrValidation, Field: "output.ports", Message: "ports must be an object"}
		}
		values = nested
	}
	validated := 0
	for name, port := range ports {
		value, exists := values[name]
		if !exists {
			continue
		}
		if err := validateAgainstSchema("output."+name, port.Schema, value); err != nil {
			return err
		}
		validated++
	}
	if validated == 0 {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "output.ports", Message: "output does not contain a declared port"}
	}
	return nil
}

func validateNodeInput(definition domain.NodeDefinition, input domain.NodeInputEnvelope, requiredPorts map[string]bool) error {
	if err := input.Validate(); err != nil {
		return err
	}
	ports, err := definition.ResolvedInputPorts()
	if err != nil {
		return err
	}
	seen := make(map[string]int, len(ports))
	for _, binding := range input.Bindings() {
		port, exists := ports[binding.ToPort]
		if !exists {
			return &domain.DomainError{Kind: domain.ErrValidation, Field: "input." + binding.ToPort, Message: "input port is not declared"}
		}
		if err := validateAgainstSchema("input."+binding.ToPort, port.Schema, binding.Value); err != nil {
			return err
		}
		seen[binding.ToPort]++
	}
	for name := range requiredPorts {
		if seen[name] == 0 {
			return &domain.DomainError{Kind: domain.ErrValidation, Field: "input." + name, Message: "enabled incoming edge did not provide this port"}
		}
	}
	return nil
}

func validateAgainstSchema(field string, rawSchema, value json.RawMessage) error {
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external JSON Schema references are disabled")
	}
	if err := compiler.AddResource("memory://workflow-value.json", bytes.NewReader(rawSchema)); err != nil {
		return err
	}
	schema, err := compiler.Compile("memory://workflow-value.json")
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if err := schema.Validate(decoded); err != nil {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: field, Message: err.Error()}
	}
	return nil
}

func matchesSchema(rawSchema, value json.RawMessage) bool {
	return validateAgainstSchema("value", rawSchema, value) == nil
}
