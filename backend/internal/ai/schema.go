package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

func validateStructuredOutput(schemaPayload, output json.RawMessage) error {
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external JSON Schema references are disabled")
	}
	if err := compiler.AddResource("memory://output-schema.json", bytes.NewReader(schemaPayload)); err != nil {
		return fmt.Errorf("compile output schema resource: %w", err)
	}
	schema, err := compiler.Compile("memory://output-schema.json")
	if err != nil {
		return fmt.Errorf("compile output schema: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode structured output: %w", err)
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidOutput, err)
	}
	return nil
}
