package qualificationpromotionv2

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
	"unicode/utf8"
)

// CanonicalJSON emits the frozen v2 cross-language representation: bounded
// BOM-free UTF-8, UTF-8-byte ordered object names, integer-only JavaScript-safe
// numbers, and no insignificant whitespace.
func CanonicalJSON(value any) ([]byte, error) {
	if err := validateGoUTF8(reflect.ValueOf(value), map[visit]bool{}); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, invalid("canonicalJSON", "marshal: %v", err)
	}
	if len(encoded) == 0 || len(encoded) > MaximumCanonicalBytes || !utf8.Valid(encoded) {
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
	if len(canonical) == 0 || len(canonical) > MaximumCanonicalBytes {
		return nil, invalid("canonicalJSON", "canonical size is invalid")
	}
	return canonical, nil
}

// DomainHash applies the sole Promotion-v2 framing. Callers should use one of
// the seven exported domain constants; exact upstream hashes are never
// relabelled through this function.
func DomainHash(domain string, canonicalBytes []byte) string {
	material := make([]byte, 0, len(HashPrefixV2)+len(domain)+len(canonicalBytes)+2)
	material = append(material, HashPrefixV2...)
	material = append(material, 0)
	material = append(material, domain...)
	material = append(material, 0)
	material = append(material, canonicalBytes...)
	digest := sha256.Sum256(material)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func EncodeRequest(document ConsumeRequest) ([]byte, string, error) {
	return encodeExact("request", RequestHashDomainV2, document, validateRequest)
}

func DecodeRequest(encoded []byte, expectedHash string) (ConsumeRequest, error) {
	return decodeExactDocument("request", RequestHashDomainV2, encoded, expectedHash, validateRequest)
}

func EncodeEvidenceEventSet(document EvidenceEventSet) ([]byte, string, error) {
	return encodeExact("evidenceEventSet", EvidenceEventSetHashDomainV2, document, validateEvidenceEventSet)
}

func DecodeEvidenceEventSet(encoded []byte, expectedHash string) (EvidenceEventSet, error) {
	return decodeExactDocument("evidenceEventSet", EvidenceEventSetHashDomainV2, encoded, expectedHash, validateEvidenceEventSet)
}

func EncodeClosure(document PromotionClosure) ([]byte, string, error) {
	return encodeExact("closure", ClosureHashDomainV2, document, validateClosure)
}

func DecodeClosure(encoded []byte, expectedHash string) (PromotionClosure, error) {
	return decodeExactDocument("closure", ClosureHashDomainV2, encoded, expectedHash, validateClosure)
}

func EncodeRevisionIntent(document RevisionIntent) ([]byte, string, error) {
	return encodeExact("revisionIntent", RevisionIntentHashDomainV2, document, validateRevisionIntent)
}

func DecodeRevisionIntent(encoded []byte, expectedHash string) (RevisionIntent, error) {
	return decodeExactDocument("revisionIntent", RevisionIntentHashDomainV2, encoded, expectedHash, validateRevisionIntent)
}

func EncodeConsumption(document Consumption) ([]byte, string, error) {
	return encodeExact("consumption", ConsumptionHashDomainV2, document, validateConsumption)
}

func DecodeConsumption(encoded []byte, expectedHash string) (Consumption, error) {
	return decodeExactDocument("consumption", ConsumptionHashDomainV2, encoded, expectedHash, validateConsumption)
}

func EncodeHandoff(document Handoff) ([]byte, string, error) {
	return encodeExact("handoff", HandoffHashDomainV2, document, validateHandoff)
}

func DecodeHandoff(encoded []byte, expectedHash string) (Handoff, error) {
	return decodeExactDocument("handoff", HandoffHashDomainV2, encoded, expectedHash, validateHandoff)
}

// DecodeStoreBundle accepts ordinary JSON/JSONB whitespace but rejects
// unknown names, duplicates, trailing values, invalid UTF-8, and any inner
// canonical byte/document/hash disagreement.
func DecodeStoreBundle(encoded []byte) (Record, error) {
	var bundle StoreBundle
	if err := decodeStrictTransport(encoded, &bundle); err != nil {
		return Record{}, err
	}
	if err := validateRawStoreBundle(encoded, false); err != nil {
		return Record{}, err
	}
	return RecordFromStoreBundle(bundle)
}

func DecodeConsumeStoreBundle(encoded []byte) (Record, error) {
	var bundle ConsumeStoreBundle
	if err := decodeStrictTransport(encoded, &bundle); err != nil {
		return Record{}, err
	}
	if err := validateRawStoreBundle(encoded, true); err != nil {
		return Record{}, err
	}
	return RecordFromConsumeStoreBundle(bundle)
}

func encodeExact[T any](name, domain string, document T, validate func(T) error) ([]byte, string, error) {
	if err := validate(document); err != nil {
		return nil, "", err
	}
	encoded, err := CanonicalJSON(document)
	if err != nil {
		return nil, "", err
	}
	if err := validateSecretFree(name, encoded); err != nil {
		return nil, "", err
	}
	return encoded, DomainHash(domain, encoded), nil
}

func decodeExactDocument[T any](name, domain string, encoded []byte, expectedHash string, validate func(T) error) (T, error) {
	var document T
	if err := decodeStrictCanonical(encoded, &document); err != nil {
		return document, err
	}
	if err := validate(document); err != nil {
		return document, err
	}
	if err := validateSecretFree(name, encoded); err != nil {
		return document, err
	}
	if !validDigest(expectedHash) || DomainHash(domain, encoded) != expectedHash {
		return document, invalid(name+"Hash", "does not match exact canonical bytes")
	}
	return document, nil
}

func decodeStrictCanonical(encoded []byte, destination any) error {
	if len(encoded) == 0 || len(encoded) > MaximumCanonicalBytes || !utf8.Valid(encoded) ||
		bytes.HasPrefix(encoded, []byte{0xef, 0xbb, 0xbf}) {
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
	canonical, err := CanonicalJSON(destination)
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded, canonical) {
		return invalid("wire", "is not exact canonical JSON")
	}
	return nil
}

func validateRawStoreBundle(encoded []byte, consume bool) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return invalid("storeBundle", "decode shape: %v", err)
	}
	root, ok := generic.(map[string]any)
	if !ok {
		return invalid("storeBundle", "must be an object")
	}
	topNames := []string{
		"closure", "consumption", "evidenceEventSet", "handoff", "operationId", "planAuthorityId",
		"receiptId", "request", "revisionIntent", "schemaVersion", "workflowInputAuthorityId",
	}
	if consume {
		topNames = append(topNames, "idempotent")
		if _, ok := root["idempotent"].(bool); !ok {
			return invalid("storeBundle.idempotent", "must be an explicit boolean")
		}
	}
	if !exactObjectNames(root, topNames) {
		return invalid("storeBundle", "top-level member set is not exact")
	}
	for _, name := range []string{"request", "evidenceEventSet", "closure", "revisionIntent"} {
		if err := validateRawStoreMaterial(root[name], name, []string{"bytesHex", "document", "hash"}); err != nil {
			return err
		}
	}
	if err := validateRawStoreMaterial(root["consumption"], "consumption", []string{"bytesHex", "consumedAt", "document", "hash"}); err != nil {
		return err
	}
	return validateRawStoreMaterial(root["handoff"], "handoff", []string{
		"bytesHex", "createdAt", "document", "handoffId", "hash", "outputRevisionId", "state",
	})
}

func validateRawStoreMaterial(value any, name string, names []string) error {
	material, ok := value.(map[string]any)
	if !ok || !exactObjectNames(material, names) {
		return invalid("storeBundle."+name, "member set is not exact")
	}
	bytesHex, ok := material["bytesHex"].(string)
	if !ok {
		return invalid("storeBundle."+name+".bytesHex", "must be a string")
	}
	exactBytes, err := decodeLowerHex("storeBundle."+name+".bytesHex", bytesHex)
	if err != nil {
		return err
	}
	document := material["document"]
	if err := validateCanonicalValue(document); err != nil {
		return err
	}
	canonicalDocument, err := appendCanonicalJSON(nil, document)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonicalDocument, exactBytes) {
		return invalid("storeBundle."+name+".document", "does not equal the exact retained canonical bytes")
	}
	return nil
}

func exactObjectNames(value map[string]any, names []string) bool {
	if len(value) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := value[name]; !ok {
			return false
		}
	}
	return true
}

func decodeStrictTransport(encoded []byte, destination any) error {
	if len(encoded) == 0 || len(encoded) > MaximumStoreBundleBytes || !utf8.Valid(encoded) ||
		bytes.HasPrefix(encoded, []byte{0xef, 0xbb, 0xbf}) {
		return invalid("storeBundle", "must be bounded BOM-free UTF-8")
	}
	if err := rejectDuplicateNames(encoded); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return invalid("storeBundle", "strict decode: %v", err)
	}
	return requireEOF(decoder)
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
			if value.Type().Field(index).PkgPath == "" {
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
		if err != nil || integer < -MaximumJavaScriptSafeInt64 || integer > MaximumJavaScriptSafeInt64 {
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

func invalid(field, format string, args ...any) error {
	detail := fmt.Sprintf(format, args...)
	if field == "" {
		return fmt.Errorf("%w: %s", ErrInvalid, detail)
	}
	return fmt.Errorf("%w: %s: %s", ErrInvalid, field, detail)
}
