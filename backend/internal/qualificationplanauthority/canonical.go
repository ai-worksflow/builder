package qualificationplanauthority

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const maximumSafeInteger = int64(9007199254740991)

var (
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	stableIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$`)
	identityPattern = regexp.MustCompile(`^(?:spiffe://[a-z0-9.-]+/[a-z0-9._/-]+|[a-z0-9]+(?:[._:-][a-z0-9]+)*)$`)
	commitPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: encode canonical JSON: %v", ErrInvalid, err)
	}
	return canonicalRawJSON(encoded)
}

func canonicalRawJSON(encoded []byte) ([]byte, error) {
	if len(encoded) == 0 || len(encoded) > 16<<20 || !utf8.Valid(encoded) {
		return nil, fmt.Errorf("%w: canonical JSON size or UTF-8 is invalid", ErrInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, fmt.Errorf("%w: decode canonical JSON: %v", ErrInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("%w: canonical JSON has trailing data", ErrInvalid)
	}
	if err := validateCanonicalValue(generic); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(generic)
	if err != nil {
		return nil, fmt.Errorf("%w: encode canonical JSON projection: %v", ErrInvalid, err)
	}
	return canonical, nil
}

func validateCanonicalValue(value any) error {
	switch typed := value.(type) {
	case nil, bool:
		return nil
	case string:
		if !utf8.ValidString(typed) || strings.ContainsRune(typed, '\x00') {
			return fmt.Errorf("%w: JSON string is not canonical", ErrInvalid)
		}
		return nil
	case json.Number:
		text := typed.String()
		if strings.ContainsAny(text, ".eE+") || text == "-0" {
			return fmt.Errorf("%w: floats and non-canonical numbers are forbidden", ErrInvalid)
		}
		integer, err := strconv.ParseInt(text, 10, 64)
		if err != nil || integer < -maximumSafeInteger || integer > maximumSafeInteger {
			return fmt.Errorf("%w: integer is outside the canonical safe range", ErrInvalid)
		}
		return nil
	case []any:
		for _, element := range typed {
			if err := validateCanonicalValue(element); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for name, element := range typed {
			if name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') {
				return fmt.Errorf("%w: JSON object name is not canonical", ErrInvalid)
			}
			if err := validateCanonicalValue(element); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported canonical JSON value", ErrInvalid)
	}
}

func sha256Digest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validDigest(value string) bool { return digestPattern.MatchString(value) }

func validUUIDv4(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.Version() == 4 && parsed.String() == value
}

func validStableID(value string) bool {
	return len(value) > 0 && len(value) <= 256 && stableIDPattern.MatchString(value)
}

func validIdentity(value string) bool {
	return len(value) > 0 && len(value) <= 512 && identityPattern.MatchString(value)
}

func validCanonicalString(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n\t")
}
