package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// CanonicalJSON produces deterministic JSON with recursively sorted object keys.
// It intentionally preserves json.Number values instead of converting them to floats.
func CanonicalJSON(value any) ([]byte, error) {
	var encoded []byte
	var err error
	switch typed := value.(type) {
	case json.RawMessage:
		encoded = append([]byte(nil), typed...)
	case []byte:
		encoded = append([]byte(nil), typed...)
	default:
		encoded, err = json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("marshal canonical JSON input: %w", err)
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, invalid("json", err.Error())
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}

	var output bytes.Buffer
	if err := writeCanonicalJSON(&output, decoded); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func CanonicalHash(value any) (string, error) {
	canonical, err := CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func IsCanonicalHash(value string) bool {
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return invalid("json", err.Error())
	}
	return invalid("json", "multiple JSON values are not allowed")
}

func writeCanonicalJSON(output *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		if typed {
			output.WriteString("true")
		} else {
			output.WriteString("false")
		}
	case string:
		encoded, _ := json.Marshal(typed)
		output.Write(encoded)
	case json.Number:
		if _, err := typed.Int64(); err != nil {
			if _, floatErr := typed.Float64(); floatErr != nil {
				return invalid("json.number", typed.String())
			}
		}
		output.WriteString(typed.String())
	case []any:
		output.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeCanonicalJSON(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			encoded, _ := json.Marshal(key)
			output.Write(encoded)
			output.WriteByte(':')
			if err := writeCanonicalJSON(output, typed[key]); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return invalid("json", fmt.Sprintf("unsupported decoded type %T", typed))
	}
	return nil
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}
