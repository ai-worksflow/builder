package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// DecodeSandboxHeadFence and DecodeDocumentFence enforce exact wire field
// names, reject duplicate/unknown fields, and do not manufacture defaults for
// missing or null values. Method-specific LSP schemas build on this boundary.
func DecodeSandboxHeadFence(value []byte) (SandboxHeadFence, error) {
	fields, err := decodeExactObject(value, []string{
		"projectId", "sessionId", "sessionEpoch", "candidateId", "version",
		"journalSequence", "writerLeaseEpoch", "treeHash",
	})
	if err != nil {
		return SandboxHeadFence{}, fmt.Errorf("%w: %v", ErrInvalidSandboxHead, err)
	}
	var result SandboxHeadFence
	if err := decodeString(fields["projectId"], &result.ProjectID); err != nil {
		return SandboxHeadFence{}, invalidField(ErrInvalidSandboxHead, "projectId")
	}
	if err := decodeString(fields["sessionId"], &result.SessionID); err != nil {
		return SandboxHeadFence{}, invalidField(ErrInvalidSandboxHead, "sessionId")
	}
	if err := decodeUint(fields["sessionEpoch"], &result.SessionEpoch); err != nil {
		return SandboxHeadFence{}, invalidField(ErrInvalidSandboxHead, "sessionEpoch")
	}
	if err := decodeString(fields["candidateId"], &result.CandidateID); err != nil {
		return SandboxHeadFence{}, invalidField(ErrInvalidSandboxHead, "candidateId")
	}
	if err := decodeUint(fields["version"], &result.Version); err != nil {
		return SandboxHeadFence{}, invalidField(ErrInvalidSandboxHead, "version")
	}
	if err := decodeUint(fields["journalSequence"], &result.JournalSequence); err != nil {
		return SandboxHeadFence{}, invalidField(ErrInvalidSandboxHead, "journalSequence")
	}
	if err := decodeUint(fields["writerLeaseEpoch"], &result.WriterLeaseEpoch); err != nil {
		return SandboxHeadFence{}, invalidField(ErrInvalidSandboxHead, "writerLeaseEpoch")
	}
	if err := decodeString(fields["treeHash"], &result.TreeHash); err != nil {
		return SandboxHeadFence{}, invalidField(ErrInvalidSandboxHead, "treeHash")
	}
	if err := result.Validate(); err != nil {
		return SandboxHeadFence{}, err
	}
	return result, nil
}

func DecodeDocumentFence(value []byte) (DocumentFence, error) {
	fields, err := decodeExactObject(value, []string{
		"modelUri", "openId", "modelVersion", "savedContentHash",
	})
	if err != nil {
		return DocumentFence{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	var result DocumentFence
	if err := decodeString(fields["modelUri"], &result.ModelURI); err != nil {
		return DocumentFence{}, invalidField(ErrInvalidDocument, "modelUri")
	}
	if err := decodeString(fields["openId"], &result.OpenID); err != nil {
		return DocumentFence{}, invalidField(ErrInvalidDocument, "openId")
	}
	if err := decodeUint(fields["modelVersion"], &result.ModelVersion); err != nil {
		return DocumentFence{}, invalidField(ErrInvalidDocument, "modelVersion")
	}
	if err := decodeString(fields["savedContentHash"], &result.SavedContentHash); err != nil {
		return DocumentFence{}, invalidField(ErrInvalidDocument, "savedContentHash")
	}
	if err := result.Validate(); err != nil {
		return DocumentFence{}, err
	}
	return result, nil
}

func decodeExactObject(value []byte, required []string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("object is required")
	}
	allowed := make(map[string]bool, len(required))
	for _, name := range required {
		allowed[name] = true
	}
	result := make(map[string]json.RawMessage, len(required))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok || !allowed[name] {
			return nil, fmt.Errorf("unknown field %q", name)
		}
		if _, duplicate := result[name]; duplicate {
			return nil, fmt.Errorf("duplicate field %q", name)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		result[name] = append(json.RawMessage(nil), raw...)
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("unterminated object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple values are forbidden")
		}
		return nil, err
	}
	for _, name := range required {
		if _, exists := result[name]; !exists {
			return nil, fmt.Errorf("missing field %q", name)
		}
	}
	return result, nil
}

func decodeString(value json.RawMessage, target *string) error {
	if len(value) == 0 || target == nil || string(value) == "null" {
		return errors.New("string is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("invalid string")
	}
	return nil
}

func decodeUint(value json.RawMessage, target *uint64) error {
	if len(value) == 0 || target == nil || string(value) == "null" {
		return errors.New("integer is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var number json.Number
	if err := decoder.Decode(&number); err != nil {
		return err
	}
	parsed, err := number.Int64()
	if err != nil || parsed < 0 {
		return errors.New("unsigned decimal integer is required")
	}
	*target = uint64(parsed)
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("invalid integer")
	}
	return nil
}
