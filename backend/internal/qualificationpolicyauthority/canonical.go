package qualificationpolicyauthority

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const canonicalTimeLayout = "2006-01-02T15:04:05.000Z"

// CanonicalJSON emits the cross-language v1 representation: object names are
// ordered by UTF-8 bytes, numbers are bounded integers, and malformed Go UTF-8
// is rejected before encoding/json can replace it with U+FFFD.
func CanonicalJSON(value any) ([]byte, error) {
	return canonicalJSONWithLimit(value, MaximumCanonicalBytes)
}

func canonicalJSONWithLimit(value any, maximum int) ([]byte, error) {
	if err := validateGoUTF8(reflect.ValueOf(value), map[visit]bool{}); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, invalid("canonicalJSON", "marshal: %v", err)
	}
	if len(encoded) == 0 || len(encoded) > maximum || !utf8.Valid(encoded) {
		return nil, invalid("canonicalJSON", "size or UTF-8 is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, invalid("canonicalJSON", "decode: %v", err)
	}
	if err := requireEOF(decoder); err != nil {
		return nil, err
	}
	if err := validateCanonicalValue(generic); err != nil {
		return nil, err
	}
	canonical, err := appendCanonicalJSON(make([]byte, 0, len(encoded)), generic)
	if err != nil {
		return nil, err
	}
	if len(canonical) == 0 || len(canonical) > maximum {
		return nil, invalid("canonicalJSON", "canonical size is invalid")
	}
	return canonical, nil
}

func appendCanonicalJSON(destination []byte, value any) ([]byte, error) {
	switch typed := value.(type) {
	case nil:
		return append(destination, "null"...), nil
	case bool:
		return strconv.AppendBool(destination, typed), nil
	case string:
		return appendCanonicalJSONString(destination, typed), nil
	case json.Number:
		return append(destination, typed.String()...), nil
	case []any:
		destination = append(destination, '[')
		for index, item := range typed {
			if index > 0 {
				destination = append(destination, ',')
			}
			var err error
			destination, err = appendCanonicalJSON(destination, item)
			if err != nil {
				return nil, err
			}
		}
		return append(destination, ']'), nil
	case map[string]any:
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		destination = append(destination, '{')
		for index, name := range names {
			if index > 0 {
				destination = append(destination, ',')
			}
			destination = appendCanonicalJSONString(destination, name)
			destination = append(destination, ':')
			var err error
			destination, err = appendCanonicalJSON(destination, typed[name])
			if err != nil {
				return nil, err
			}
		}
		return append(destination, '}'), nil
	default:
		return nil, invalid("canonicalJSON", "unsupported value")
	}
}

func appendCanonicalJSONString(destination []byte, value string) []byte {
	const hexadecimal = "0123456789abcdef"
	destination = append(destination, '"')
	for index := 0; index < len(value); {
		character := value[index]
		if character >= utf8.RuneSelf {
			_, size := utf8.DecodeRuneInString(value[index:])
			destination = append(destination, value[index:index+size]...)
			index += size
			continue
		}
		index++
		switch character {
		case '"', '\\':
			destination = append(destination, '\\', character)
		case '\b':
			destination = append(destination, '\\', 'b')
		case '\f':
			destination = append(destination, '\\', 'f')
		case '\n':
			destination = append(destination, '\\', 'n')
		case '\r':
			destination = append(destination, '\\', 'r')
		case '\t':
			destination = append(destination, '\\', 't')
		default:
			if character < 0x20 {
				destination = append(destination, '\\', 'u', '0', '0', hexadecimal[character>>4], hexadecimal[character&0x0f])
			} else {
				destination = append(destination, character)
			}
		}
	}
	return append(destination, '"')
}

// DomainHash applies the policy-authority framing. It must never be confused
// with a raw SHA-256 content digest used by an upstream artifact.
func DomainHash(domain string, canonicalBytes []byte) string {
	material := make([]byte, 0, len(AuthorityHashPrefixV1)+len(domain)+len(canonicalBytes)+2)
	material = append(material, AuthorityHashPrefixV1...)
	material = append(material, 0)
	material = append(material, domain...)
	material = append(material, 0)
	material = append(material, canonicalBytes...)
	digest := sha256.Sum256(material)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func EncodeRevisionPolicy(policy RevisionPolicy) ([]byte, string, error) {
	if err := ValidateRevisionPolicy(policy); err != nil {
		return nil, "", err
	}
	encoded, err := CanonicalJSON(policy)
	if err != nil {
		return nil, "", err
	}
	if err := validateSecretFree("revisionPolicy", encoded); err != nil {
		return nil, "", err
	}
	return encoded, DomainHash(RevisionPolicyHashDomainV1, encoded), nil
}

func DecodeRevisionPolicy(encoded []byte, expectedHash string) (RevisionPolicy, error) {
	var policy RevisionPolicy
	if err := decodeExactLimit(encoded, &policy, MaximumCanonicalBytes); err != nil {
		return RevisionPolicy{}, err
	}
	if err := ValidateRevisionPolicy(policy); err != nil {
		return RevisionPolicy{}, err
	}
	if err := validateSecretFree("revisionPolicy", encoded); err != nil {
		return RevisionPolicy{}, err
	}
	if !validDigest(expectedHash) || DomainHash(RevisionPolicyHashDomainV1, encoded) != expectedHash {
		return RevisionPolicy{}, invalid("revisionPolicyHash", "does not match exact canonical bytes")
	}
	return cloneRevisionPolicy(policy), nil
}

func EncodePlanInputProfile(profile PlanInputProfile) ([]byte, string, error) {
	if err := ValidatePlanInputProfile(profile); err != nil {
		return nil, "", err
	}
	encoded, err := CanonicalJSON(profile)
	if err != nil {
		return nil, "", err
	}
	if err := validateSecretFree("planInputProfile", encoded); err != nil {
		return nil, "", err
	}
	return encoded, DomainHash(PlanInputProfileHashDomainV1, encoded), nil
}

func DecodePlanInputProfile(encoded []byte, expectedHash string) (PlanInputProfile, error) {
	var profile PlanInputProfile
	if err := decodeExactLimit(encoded, &profile, MaximumCanonicalBytes); err != nil {
		return PlanInputProfile{}, err
	}
	if err := ValidatePlanInputProfile(profile); err != nil {
		return PlanInputProfile{}, err
	}
	if err := validateSecretFree("planInputProfile", encoded); err != nil {
		return PlanInputProfile{}, err
	}
	if !validDigest(expectedHash) || DomainHash(PlanInputProfileHashDomainV1, encoded) != expectedHash {
		return PlanInputProfile{}, invalid("planInputProfileHash", "does not match exact canonical bytes")
	}
	return clonePlanInputProfile(profile), nil
}

func EncodePromotionPolicy(policy PromotionPolicy) ([]byte, string, error) {
	if err := ValidatePromotionPolicy(policy); err != nil {
		return nil, "", err
	}
	encoded, err := CanonicalJSON(policy)
	if err != nil {
		return nil, "", err
	}
	if err := validateSecretFree("promotionPolicy", encoded); err != nil {
		return nil, "", err
	}
	return encoded, DomainHash(PromotionPolicyHashDomainV1, encoded), nil
}

func DecodePromotionPolicy(encoded []byte, expectedHash string) (PromotionPolicy, error) {
	var policy PromotionPolicy
	if err := decodeExactLimit(encoded, &policy, MaximumCanonicalBytes); err != nil {
		return PromotionPolicy{}, err
	}
	if err := ValidatePromotionPolicy(policy); err != nil {
		return PromotionPolicy{}, err
	}
	if err := validateSecretFree("promotionPolicy", encoded); err != nil {
		return PromotionPolicy{}, err
	}
	if !validDigest(expectedHash) || DomainHash(PromotionPolicyHashDomainV1, encoded) != expectedHash {
		return PromotionPolicy{}, invalid("promotionPolicyHash", "does not match exact canonical bytes")
	}
	return clonePromotionPolicy(policy), nil
}

func EncodeAuthorityDocument(document AuthorityDocument) ([]byte, string, error) {
	if err := ValidateAuthorityDocument(document); err != nil {
		return nil, "", err
	}
	encoded, err := canonicalJSONWithLimit(document, MaximumAuthorityBytes)
	if err != nil {
		return nil, "", err
	}
	if err := validateSecretFree("authority", encoded); err != nil {
		return nil, "", err
	}
	return encoded, DomainHash(AuthorityHashDomainV1, encoded), nil
}

func DecodeAuthorityDocument(encoded []byte, expectedHash string) (AuthorityDocument, error) {
	var document AuthorityDocument
	if err := decodeExactLimit(encoded, &document, MaximumAuthorityBytes); err != nil {
		return AuthorityDocument{}, err
	}
	if err := ValidateAuthorityDocument(document); err != nil {
		return AuthorityDocument{}, err
	}
	if err := validateSecretFree("authority", encoded); err != nil {
		return AuthorityDocument{}, err
	}
	if !validDigest(expectedHash) || DomainHash(AuthorityHashDomainV1, encoded) != expectedHash {
		return AuthorityDocument{}, invalid("authorityHash", "does not match exact canonical bytes")
	}
	return cloneAuthorityDocument(document), nil
}

func compileRecord(command IssueCommand, resolved ResolvedPolicy, generation int64, previousHash *string, issuedAt time.Time) (Record, error) {
	if err := validateIssueCommand(command); err != nil {
		return Record{}, err
	}
	if err := ValidateResolvedPolicy(resolved); err != nil {
		return Record{}, err
	}
	canonicalIssuedAt, err := formatCanonicalDatabaseTime(issuedAt)
	if err != nil {
		return Record{}, err
	}
	normalizedIssuedAt, err := parseCanonicalTime(canonicalIssuedAt)
	if err != nil {
		return Record{}, err
	}

	revisionBytes, revisionHash, err := EncodeRevisionPolicy(resolved.RevisionPolicy)
	if err != nil {
		return Record{}, err
	}
	planBytes, planHash, err := EncodePlanInputProfile(resolved.PlanInputProfile)
	if err != nil {
		return Record{}, err
	}
	promotionBytes, promotionHash, err := EncodePromotionPolicy(resolved.PromotionPolicy)
	if err != nil {
		return Record{}, err
	}
	document := AuthorityDocument{
		AuthorityID: command.AuthorityID.String(),
		ComponentDigests: ComponentDigests{
			PlanInputProfile: planHash,
			PromotionPolicy:  promotionHash,
			RevisionPolicy:   revisionHash,
		},
		ExecutionProfile:      resolved.ExecutionProfile,
		ExternalGatePolicy:    resolved.ExternalGatePolicy,
		Generation:            generation,
		IssuedAt:              canonicalIssuedAt,
		OperationID:           command.OperationID.String(),
		PlanInputProfile:      clonePlanInputProfile(resolved.PlanInputProfile),
		PolicySourceID:        command.PolicySourceID,
		PreviousAuthorityHash: cloneStringPointer(previousHash),
		ProjectID:             resolved.ProjectID.String(),
		PromotionPolicy:       clonePromotionPolicy(resolved.PromotionPolicy),
		RevisionPolicy:        cloneRevisionPolicy(resolved.RevisionPolicy),
		SchemaVersion:         AuthoritySchemaV1,
		Status:                resolved.Status,
		SupersessionPolicy:    resolved.SupersessionPolicy,
	}
	documentBytes, authorityHash, err := EncodeAuthorityDocument(document)
	if err != nil {
		return Record{}, err
	}
	record := Record{
		Command:               command,
		RevisionPolicy:        cloneRevisionPolicy(resolved.RevisionPolicy),
		RevisionPolicyBytes:   revisionBytes,
		RevisionPolicyHash:    revisionHash,
		PlanInputProfile:      clonePlanInputProfile(resolved.PlanInputProfile),
		PlanInputProfileBytes: planBytes,
		PlanInputProfileHash:  planHash,
		PromotionPolicy:       clonePromotionPolicy(resolved.PromotionPolicy),
		PromotionPolicyBytes:  promotionBytes,
		PromotionPolicyHash:   promotionHash,
		Document:              document,
		DocumentBytes:         documentBytes,
		AuthorityHash:         authorityHash,
		IssuedAt:              normalizedIssuedAt,
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, err
	}
	return cloneRecord(record), nil
}

func formatCanonicalDatabaseTime(value time.Time) (string, error) {
	if value.IsZero() || value.Nanosecond()%int(time.Millisecond) != 0 {
		return "", invalid("databaseClock", "must return a timestamp in range with millisecond precision")
	}
	value = value.Round(0).UTC()
	if value.Year() < 1678 || value.Year() >= 2262 {
		return "", invalid("databaseClock", "must return a timestamp in range with millisecond precision")
	}
	return value.Format(canonicalTimeLayout), nil
}

func parseCanonicalTime(value string) (time.Time, error) {
	parsed, err := time.Parse(canonicalTimeLayout, value)
	if err != nil || parsed.Format(canonicalTimeLayout) != value || parsed.Year() < 1678 || parsed.Year() >= 2262 {
		return time.Time{}, invalid("issuedAt", "must be canonical UTC milliseconds")
	}
	return parsed, nil
}

func decodeExactLimit(encoded []byte, destination any, maximum int) error {
	if len(encoded) == 0 || len(encoded) > maximum || !utf8.Valid(encoded) || bytes.HasPrefix(encoded, []byte{0xef, 0xbb, 0xbf}) {
		return invalid("wire", "must be bounded BOM-free UTF-8")
	}
	if err := rejectDuplicateNames(encoded); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return invalid("wire", "strict decode: %v", err)
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	canonical, err := canonicalJSONWithLimit(destination, maximum)
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded, canonical) {
		return invalid("wire", "is not exact canonical JSON")
	}
	return nil
}

type visit struct {
	typ reflect.Type
	ptr uintptr
}

func validateGoUTF8(value reflect.Value, seen map[visit]bool) error {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return validateGoUTF8(value.Elem(), seen)
	}
	switch value.Kind() {
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return invalid("canonicalJSON", "Go string contains invalid UTF-8")
		}
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		key := visit{typ: value.Type(), ptr: value.Pointer()}
		if seen[key] {
			return nil
		}
		seen[key] = true
		return validateGoUTF8(value.Elem(), seen)
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		key := visit{typ: value.Type(), ptr: value.Pointer()}
		if seen[key] {
			return nil
		}
		seen[key] = true
		iterator := value.MapRange()
		for iterator.Next() {
			if err := validateGoUTF8(iterator.Key(), seen); err != nil {
				return err
			}
			if err := validateGoUTF8(iterator.Value(), seen); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.IsNil() || value.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		key := visit{typ: value.Type(), ptr: value.Pointer()}
		if seen[key] {
			return nil
		}
		seen[key] = true
		fallthrough
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateGoUTF8(value.Index(index), seen); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if field.PkgPath == "" {
				if err := validateGoUTF8(value.Field(index), seen); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateCanonicalValue(value any) error {
	switch typed := value.(type) {
	case nil, bool:
		return nil
	case string:
		if !utf8.ValidString(typed) || strings.ContainsRune(typed, '\x00') {
			return invalid("canonicalJSON", "string is invalid")
		}
		return nil
	case json.Number:
		text := typed.String()
		if strings.ContainsAny(text, ".eE+") || text == "-0" {
			return invalid("canonicalJSON", "floats, exponents, plus signs, and negative zero are forbidden")
		}
		integer, err := strconv.ParseInt(text, 10, 64)
		if err != nil || integer < -MaximumJavaScriptSafeInteger || integer > MaximumJavaScriptSafeInteger {
			return invalid("canonicalJSON", "integer is outside the JavaScript-safe range")
		}
		return nil
	case []any:
		for _, item := range typed {
			if err := validateCanonicalValue(item); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for name, item := range typed {
			if name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') {
				return invalid("canonicalJSON", "object name is invalid")
			}
			if err := validateCanonicalValue(item); err != nil {
				return err
			}
		}
		return nil
	default:
		return invalid("canonicalJSON", "unsupported value")
	}
}

func rejectDuplicateNames(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	return requireEOF(decoder)
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return invalid("wire", "tokenize: %v", err)
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
				return invalid("wire", "object name: %v", err)
			}
			name, ok := nameToken.(string)
			if !ok {
				return invalid("wire", "object name is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return invalid("wire", "duplicate field %q", name)
			}
			seen[name] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return invalid("wire", "object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return invalid("wire", "array is not closed")
		}
	default:
		return invalid("wire", "unexpected delimiter")
	}
	return nil
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return invalid("wire", "has trailing JSON value")
	}
	return invalid("wire", "has trailing data: %v", err)
}

func invalid(field, format string, arguments ...any) error {
	detail := fmt.Sprintf(format, arguments...)
	if field == "" {
		return fmt.Errorf("%w: %s", ErrInvalid, detail)
	}
	return fmt.Errorf("%w: %s: %s", ErrInvalid, field, detail)
}
