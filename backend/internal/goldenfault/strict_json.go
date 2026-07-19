package goldenfault

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	maximumEnvelopeBytes = 1 << 20
	maximumPayloadBytes  = 64 << 10
	maximumSignatures    = 8
	maximumJSONDepth     = 16
	maximumJSONNodes     = 256
	canonicalTimeLayout  = "2006-01-02T15:04:05.000Z"
)

var (
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	resourcePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,511}$`)
	identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/+~-]{0,511}$`)
)

func decodeStrictJSON(input []byte, target any) error {
	if len(input) == 0 {
		return errors.New("JSON document is empty")
	}
	if !utf8.Valid(input) || bytes.HasPrefix(input, []byte{0xef, 0xbb, 0xbf}) || bytes.ContainsRune(input, utf8.RuneError) {
		return errors.New("JSON must use valid BOM-free UTF-8 without replacement characters")
	}
	if err := rejectDuplicateJSONNames(input); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func decodeStrictValue(input []byte) (any, error) {
	if len(input) == 0 || !utf8.Valid(input) || bytes.HasPrefix(input, []byte{0xef, 0xbb, 0xbf}) || bytes.ContainsRune(input, utf8.RuneError) {
		return nil, errors.New("JSON must be non-empty valid BOM-free UTF-8 without replacement characters")
	}
	if err := rejectDuplicateJSONNames(input); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	return value, nil
}

func rejectDuplicateJSONNames(input []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	nodes := 0
	if err := scanJSONValue(decoder, 0, &nodes); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder, depth int, nodes *int) error {
	if depth > maximumJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", maximumJSONDepth)
	}
	*nodes++
	if *nodes > maximumJSONNodes {
		return fmt.Errorf("JSON exceeds %d values", maximumJSONNodes)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("JSON object name is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate JSON object name %q", name)
			}
			seen[name] = struct{}{}
			if err := scanJSONValue(decoder, depth+1, nodes); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1, nodes); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("JSON document has trailing content")
	}
	return err
}

func requireExactObject(input []byte, fields map[string]valueKind) error {
	value, err := decodeStrictValue(input)
	if err != nil {
		return err
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != len(fields) {
		return errors.New("JSON object does not have the exact required fields")
	}
	for field, kind := range fields {
		value, exists := object[field]
		if !exists || value == nil || !kind.matches(value) {
			return fmt.Errorf("JSON field %q is missing, null, or has the wrong type", field)
		}
	}
	return nil
}

type valueKind uint8

const (
	valueString valueKind = iota + 1
	valueInteger
	valueArray
)

func (kind valueKind) matches(value any) bool {
	switch kind {
	case valueString:
		_, ok := value.(string)
		return ok
	case valueInteger:
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	case valueArray:
		_, ok := value.([]any)
		return ok
	default:
		return false
	}
}

func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func sha256Digest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validDigest(value string) bool { return digestPattern.MatchString(value) }

func validUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.Version() == 4 && parsed.String() == value
}

func parseCanonicalTime(value, field string) (time.Time, error) {
	parsed, err := time.Parse(canonicalTimeLayout, value)
	if err != nil || parsed.Format(canonicalTimeLayout) != value {
		return time.Time{}, fmt.Errorf("%s must be exact UTC ISO-8601 milliseconds", field)
	}
	return parsed, nil
}

func formatCanonicalTime(value time.Time) string { return value.UTC().Format(canonicalTimeLayout) }

func decodeCanonicalBase64(value string, maximum int) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) == 0 || len(decoded) > maximum || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("value is not canonical non-empty standard base64")
	}
	return decoded, nil
}

func validIdentity(value string) bool {
	return identityPattern.MatchString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func validResourceID(value string) bool {
	return resourcePattern.MatchString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\x00") && !strings.Contains(value, "://")
}

func sortedUnique(values []string) bool {
	if len(values) == 0 || len(values) > maximumSignatures {
		return false
	}
	for index, value := range values {
		if !validIdentity(value) || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}
