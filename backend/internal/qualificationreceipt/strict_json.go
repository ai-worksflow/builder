package qualificationreceipt

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
	maxIndexBytes         = 16 << 20
	maxReceiptBytes       = 8 << 20
	maxArtifactCount      = 4096
	maxArtifactBytes      = int64(8 << 30)
	maxTestInventoryCases = 512
	maxCriterionSources   = 32
	maxContractCriteria   = 512
	canonicalTimeLayout   = "2006-01-02T15:04:05.000Z"
)

var (
	digestPattern                = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	stableIDPattern              = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	requirementPattern           = regexp.MustCompile(`^(?:AIC-(?:E2E|FAIL)-[0-9]{3}|FQP-E2E-[0-9]{3}|LSP-QA-[0-9]{3})$`)
	documentedRequirementPattern = regexp.MustCompile(`\b(?:AIC-(?:E2E|FAIL)-[0-9]{3}|FQP-E2E-[0-9]{3}|LSP-QA-[0-9]{3})\b`)
	commitPattern                = regexp.MustCompile(`^[0-9a-f]{40}(?:[0-9a-f]{24})?$`)
	testCaseIDPattern            = regexp.MustCompile(`^QG-[A-Z][A-Z0-9]*-[0-9]{3}$`)
	contractCriterionIDPattern   = regexp.MustCompile(`^AC-[A-Z][A-Z0-9]*-[0-9]{3}$`)
	contractRequirementIDPattern = regexp.MustCompile(`^REQ-[A-Z0-9]+(?:-[A-Z0-9]+)*$`)
	goldenTestPathPattern        = regexp.MustCompile(`^frontend/tests/golden-[a-z0-9-]+\.spec\.ts$`)
	canonicalPathPattern         = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

func decodeStrictJSON(input []byte, target any) error {
	if len(input) == 0 {
		return errors.New("JSON document is empty")
	}
	if err := validateJSONUnicode(input); err != nil {
		return err
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

func decodeJSONValue(input []byte) (any, error) {
	if err := validateJSONUnicode(input); err != nil {
		return nil, err
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

func validateJSONUnicode(input []byte) error {
	if !utf8.Valid(input) || bytes.HasPrefix(input, []byte{0xef, 0xbb, 0xbf}) || bytes.ContainsRune(input, utf8.RuneError) {
		return errors.New("JSON must use valid BOM-free UTF-8 without replacement characters")
	}
	for index := 0; index < len(input); index++ {
		if input[index] != '\\' || index+1 >= len(input) {
			continue
		}
		if input[index+1] != 'u' {
			index++
			continue
		}
		code, ok := parseHexQuad(input[index+2:])
		if !ok || code == 0xfffd {
			return errors.New("JSON contains an invalid Unicode escape")
		}
		if code >= 0xd800 && code <= 0xdbff {
			if index+12 > len(input) || input[index+6] != '\\' || input[index+7] != 'u' {
				return errors.New("JSON contains an unpaired high-surrogate escape")
			}
			low, ok := parseHexQuad(input[index+8:])
			if !ok || low < 0xdc00 || low > 0xdfff {
				return errors.New("JSON contains an invalid surrogate pair")
			}
			index += 11
			continue
		}
		if code >= 0xdc00 && code <= 0xdfff {
			return errors.New("JSON contains an unpaired low-surrogate escape")
		}
		index += 5
	}
	return nil
}

func parseHexQuad(value []byte) (uint16, bool) {
	if len(value) < 4 {
		return 0, false
	}
	var result uint16
	for _, character := range value[:4] {
		result <<= 4
		switch {
		case character >= '0' && character <= '9':
			result += uint16(character - '0')
		case character >= 'a' && character <= 'f':
			result += uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			result += uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return result, true
}

func rejectDuplicateJSONNames(input []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
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
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
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

func sha256Digest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validDigest(value string) bool { return digestPattern.MatchString(value) }

func validCanonicalString(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\x00\u2028\u2029")
}

func validStableID(value string) bool {
	return len(value) <= 128 && stableIDPattern.MatchString(value)
}

func validUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.Version() == 4 && parsed.String() == value
}

func parseCanonicalTime(value, field string) (time.Time, error) {
	parsed, err := time.Parse(canonicalTimeLayout, value)
	if err != nil || parsed.Format(canonicalTimeLayout) != value {
		return time.Time{}, fmt.Errorf("%s must be canonical UTC ISO-8601 milliseconds", field)
	}
	return parsed, nil
}

func decodeCanonicalBase64(value string, minimum, maximum int) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) < minimum || len(decoded) > maximum || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("value is not canonical standard base64 in the allowed size range")
	}
	return decoded, nil
}

func sortedUniqueStrings(values []string, validate func(string) bool) bool {
	if len(values) == 0 {
		return false
	}
	for index, value := range values {
		if !validate(value) || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
