package qualificationinputauthority

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"reflect"
	"sort"
	"strconv"
	"unicode/utf8"
)

// CanonicalJSON emits the frozen cross-language representation: bounded
// BOM-free UTF-8, UTF-8-byte ordered object names, JavaScript-safe integers,
// and no insignificant whitespace.
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

func EncodeIssueRequest(document IssueRequest) ([]byte, string, error) {
	return encodeExact("request", IssueRequestHashDomainV1, document, validateIssueRequest)
}

func DecodeIssueRequest(encoded []byte, expectedHash string) (IssueRequest, error) {
	return decodeExact("request", IssueRequestHashDomainV1, encoded, expectedHash, validateIssueRequest)
}

func EncodeSourceRequest(document SourceVerificationRequest) ([]byte, string, error) {
	return encodeExact("sourceRequest", SourceRequestHashDomainV1, document, validateSourceRequest)
}

func DecodeSourceRequest(encoded []byte, expectedHash string) (SourceVerificationRequest, error) {
	return decodeExact("sourceRequest", SourceRequestHashDomainV1, encoded, expectedHash, validateSourceRequest)
}

func EncodeCredentialRequest(document CredentialResolutionRequest) ([]byte, string, error) {
	return encodeExact("credentialRequest", CredentialRequestHashDomainV1, document, validateCredentialRequest)
}

func DecodeCredentialRequest(encoded []byte, expectedHash string) (CredentialResolutionRequest, error) {
	return decodeExact("credentialRequest", CredentialRequestHashDomainV1, encoded, expectedHash, validateCredentialRequest)
}

func EncodeReceiptAdmission(document ReceiptAdmission) ([]byte, string, error) {
	return encodeExact("receiptAdmission", ReceiptAdmissionHashDomainV1, document, validateReceiptAdmission)
}

func DecodeReceiptAdmission(encoded []byte, expectedHash string) (ReceiptAdmission, error) {
	return decodeExact("receiptAdmission", ReceiptAdmissionHashDomainV1, encoded, expectedHash, validateReceiptAdmission)
}

func EncodeAuthority(document AuthorityDocument) ([]byte, string, error) {
	return encodeExact("authority", AuthorityHashDomainV1, document, validateAuthority)
}

func DecodeAuthority(encoded []byte, expectedHash string) (AuthorityDocument, error) {
	return decodeExact("authority", AuthorityHashDomainV1, encoded, expectedHash, validateAuthority)
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

func decodeExact[T any](name, domain string, encoded []byte, expectedHash string, validate func(T) error) (T, error) {
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
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return invalid("wire", "read object name: %v", err)
			}
			name, ok := nameToken.(string)
			if !ok {
				return invalid("wire", "object name is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return invalid("wire", "duplicate object name %q", name)
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

func validateCanonicalValue(value any) error {
	switch typed := value.(type) {
	case nil, bool:
		return nil
	case string:
		if !utf8.ValidString(typed) {
			return invalid("canonicalJSON", "string is invalid UTF-8")
		}
		return nil
	case json.Number:
		text := typed.String()
		if bytes.ContainsAny([]byte(text), ".eE+") || text == "-0" {
			return invalid("canonicalJSON", "numbers must be canonical integers")
		}
		integer, err := strconv.ParseInt(text, 10, 64)
		if err != nil || integer < -MaximumJavaScriptSafeInteger || integer > MaximumJavaScriptSafeInteger {
			return invalid("canonicalJSON", "number is outside the JavaScript-safe integer range")
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
			if !utf8.ValidString(name) {
				return invalid("canonicalJSON", "object name is invalid UTF-8")
			}
			if err := validateCanonicalValue(item); err != nil {
				return err
			}
		}
		return nil
	default:
		return invalid("canonicalJSON", "unsupported JSON value %T", value)
	}
}

type visit struct {
	pointer uintptr
	typeOf  reflect.Type
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
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		key := visit{pointer: value.Pointer(), typeOf: value.Type()}
		if seen[key] {
			return invalid("canonicalJSON", "cyclic value")
		}
		seen[key] = true
		defer delete(seen, key)
		return validateGoUTF8(value.Elem(), seen)
	}
	switch value.Kind() {
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return invalid("canonicalJSON", "Go string is invalid UTF-8")
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if err := validateGoUTF8(value.Field(index), seen); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateGoUTF8(value.Index(index), seen); err != nil {
				return err
			}
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			if err := validateGoUTF8(iterator.Key(), seen); err != nil {
				return err
			}
			if err := validateGoUTF8(iterator.Value(), seen); err != nil {
				return err
			}
		}
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
		return invalid("wire", "contains a trailing JSON value")
	}
	return invalid("wire", "trailing data: %v", err)
}
