package workflowinputauthority

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

const hashPrefixV1 = "worksflow-workflow-input-authority-hash/v1"

// CanonicalJSON is the frozen v1 JSON writer shared with the PostgreSQL
// authority: object names are ordered by UTF-8 bytes, numbers are bounded
// integers, and strings escape JSON syntax/control bytes but not valid UTF-8.
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

// DomainHash applies the v1 authority framing. It is intentionally distinct
// from RawSHA256, which identifies exact retained legacy/external bytes.
func DomainHash(domain string, canonicalBytes []byte) string {
	material := make([]byte, 0, len(hashPrefixV1)+len(domain)+len(canonicalBytes)+2)
	material = append(material, hashPrefixV1...)
	material = append(material, 0)
	material = append(material, domain...)
	material = append(material, 0)
	material = append(material, canonicalBytes...)
	digest := sha256.Sum256(material)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// RawSHA256 identifies an exact byte sequence without a document domain.
func RawSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// Compile canonicalizes and cross-validates the complete server-resolved
// candidate. Schema/media values and every derived hash are authored here.
func Compile(candidate Candidate) (Record, error) {
	request := candidate.Request
	request.SchemaVersion = FreezeRequestSchemaV1
	request.MediaType = FreezeRequestMediaTypeV1

	input := cloneInput(candidate.Input)
	input.SchemaVersion = InputSchemaV1
	input.MediaType = InputMediaTypeV1

	targetBytes, err := CanonicalJSON(input.Target)
	if err != nil {
		return Record{}, err
	}
	targetHash := DomainHash(TargetHashDomainV1, targetBytes)
	input.TargetHash = targetHash

	materials := cloneMaterials(candidate.Materials)
	sort.Slice(materials.InputManifests, func(i, j int) bool {
		return materials.InputManifests[i].Role+"\x00"+materials.InputManifests[i].ManifestID <
			materials.InputManifests[j].Role+"\x00"+materials.InputManifests[j].ManifestID
	})
	sort.Slice(materials.Revisions, func(i, j int) bool {
		return materials.Revisions[i].Purpose+"\x00"+materials.Revisions[i].RevisionID <
			materials.Revisions[j].Purpose+"\x00"+materials.Revisions[j].RevisionID
	})
	sort.Slice(materials.ReviewReceipts, func(i, j int) bool {
		return materials.ReviewReceipts[i].ReviewRequestID < materials.ReviewReceipts[j].ReviewRequestID
	})
	normalized := Candidate{
		Document: cloneCandidateDocument(candidate.Document), Input: input,
		Materials: materials, Request: request,
	}
	if err := validateCandidate(normalized); err != nil {
		return Record{}, err
	}
	if _, err := EncodeFreezeCandidate(normalized.Document); err != nil {
		return Record{}, err
	}

	requestBytes, err := CanonicalJSON(request)
	if err != nil {
		return Record{}, err
	}
	requestHash := DomainHash(FreezeRequestHashDomainV1, requestBytes)
	inputBytes, err := CanonicalJSON(input)
	if err != nil {
		return Record{}, err
	}
	inputHash := DomainHash(InputHashDomainV1, inputBytes)
	envelope := AuthorityEnvelope{
		AuthorityID: request.AuthorityID, InputHash: inputHash, MediaType: AuthorityMediaTypeV1,
		NodeRunID: request.NodeRunID, OperationID: request.OperationID, ProjectID: request.ProjectID,
		RequestHash: requestHash, SchemaVersion: AuthoritySchemaV1, TargetHash: targetHash,
		WorkflowRunID: request.WorkflowRunID,
	}
	if err := validateEnvelope(envelope); err != nil {
		return Record{}, err
	}
	envelopeBytes, err := CanonicalJSON(envelope)
	if err != nil {
		return Record{}, err
	}
	authorityHash := DomainHash(AuthorityHashDomainV1, envelopeBytes)

	record := Record{
		OperationID: mustUUID(request.OperationID), AuthorityID: mustUUID(request.AuthorityID),
		WorkflowRunID: mustUUID(request.WorkflowRunID), NodeRunID: mustUUID(request.NodeRunID),
		Request: request, RequestBytes: requestBytes, RequestHash: requestHash,
		Target: input.Target, TargetBytes: targetBytes, TargetHash: targetHash,
		Input: input, InputBytes: inputBytes, InputHash: inputHash,
		Envelope: envelope, EnvelopeBytes: envelopeBytes, AuthorityHash: authorityHash,
		Materials: materials,
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, err
	}
	return cloneRecord(record), nil
}

// EncodeFreezeCandidate emits the exact private six-field issuer document.
// It is not an authority document and receives no public domain hash.
func EncodeFreezeCandidate(document FreezeCandidateDocument) ([]byte, error) {
	if err := validateCandidateDocument(document); err != nil {
		return nil, err
	}
	return canonicalJSONWithLimit(document, MaximumCandidateBytes)
}

// DecodeFreezeCandidate rejects widened, duplicate, non-canonical, or
// semantically invalid private issuer input.
func DecodeFreezeCandidate(encoded []byte) (FreezeCandidateDocument, error) {
	var document FreezeCandidateDocument
	if err := decodeExactLimit(encoded, &document, MaximumCandidateBytes); err != nil {
		return FreezeCandidateDocument{}, err
	}
	if err := validateCandidateDocument(document); err != nil {
		return FreezeCandidateDocument{}, err
	}
	return document, nil
}

func DecodeFreezeRequest(encoded []byte, expectedHash string) (FreezeRequest, error) {
	var request FreezeRequest
	if err := decodeExactLimit(encoded, &request, MaximumRequestBytes); err != nil {
		return FreezeRequest{}, err
	}
	if err := validateRequest(request); err != nil {
		return FreezeRequest{}, err
	}
	if err := requireExpectedHash("request", FreezeRequestHashDomainV1, encoded, expectedHash); err != nil {
		return FreezeRequest{}, err
	}
	return request, nil
}

func DecodeTarget(encoded []byte, expectedHash string) (TargetDocument, error) {
	var target TargetDocument
	if err := decodeExactLimit(encoded, &target, MaximumTargetBytes); err != nil {
		return TargetDocument{}, err
	}
	if err := validateTarget(target); err != nil {
		return TargetDocument{}, err
	}
	if err := requireExpectedHash("target", TargetHashDomainV1, encoded, expectedHash); err != nil {
		return TargetDocument{}, err
	}
	return target, nil
}

func DecodeInput(encoded []byte, expectedHash string) (WorkflowInputDocument, error) {
	var input WorkflowInputDocument
	if err := decodeExactLimit(encoded, &input, MaximumInputBytes); err != nil {
		return WorkflowInputDocument{}, err
	}
	if err := validateInput(input); err != nil {
		return WorkflowInputDocument{}, err
	}
	if err := requireExpectedHash("input", InputHashDomainV1, encoded, expectedHash); err != nil {
		return WorkflowInputDocument{}, err
	}
	return input, nil
}

func DecodeAuthority(encoded []byte, expectedHash string) (AuthorityEnvelope, error) {
	var envelope AuthorityEnvelope
	if err := decodeExactLimit(encoded, &envelope, MaximumAuthorityBytes); err != nil {
		return AuthorityEnvelope{}, err
	}
	if err := validateEnvelope(envelope); err != nil {
		return AuthorityEnvelope{}, err
	}
	if err := requireExpectedHash("authority", AuthorityHashDomainV1, encoded, expectedHash); err != nil {
		return AuthorityEnvelope{}, err
	}
	return envelope, nil
}

// ValidateRecord independently decodes all canonical bytes, recomputes every
// hash, cross-checks scalar projections, and verifies retained raw material.
func ValidateRecord(record Record) error {
	request, err := DecodeFreezeRequest(record.RequestBytes, record.RequestHash)
	if err != nil {
		return invalid("record.request", "%v", err)
	}
	target, err := DecodeTarget(record.TargetBytes, record.TargetHash)
	if err != nil {
		return invalid("record.target", "%v", err)
	}
	input, err := DecodeInput(record.InputBytes, record.InputHash)
	if err != nil {
		return invalid("record.input", "%v", err)
	}
	envelope, err := DecodeAuthority(record.EnvelopeBytes, record.AuthorityHash)
	if err != nil {
		return invalid("record.authority", "%v", err)
	}
	if record.OperationID.String() != request.OperationID || record.AuthorityID.String() != request.AuthorityID ||
		record.WorkflowRunID.String() != request.WorkflowRunID || record.NodeRunID.String() != request.NodeRunID ||
		record.Request != request || record.Target != target || !equalInput(record.Input, input) || record.Envelope != envelope {
		return invalid("record", "typed scalar projections differ from canonical bytes")
	}
	if err := validateCrossBindings(request, input, target, envelope, record.RequestHash, record.InputHash, record.TargetHash); err != nil {
		return err
	}
	document, err := candidateDocumentFromRecord(input, record.Materials)
	if err != nil {
		return err
	}
	if err := validateCandidate(Candidate{Document: document, Input: input, Materials: record.Materials, Request: request}); err != nil {
		return err
	}
	if !record.FrozenAt.IsZero() && (record.FrozenAt.Location() != time.UTC || !record.FrozenAt.Equal(record.FrozenAt.UTC().Truncate(time.Millisecond)) ||
		record.FrozenAt.Year() < 1678 || record.FrozenAt.Year() >= 2262) {
		return invalid("record.frozenAt", "must be a millisecond UTC authority timestamp")
	}
	return nil
}

func decodeExact(encoded []byte, destination any) error {
	return decodeExactLimit(encoded, destination, MaximumCanonicalBytes)
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

// encoding/json replaces malformed Go strings with U+FFFD. Reject them before
// json.Marshal so the public canonicalizer cannot silently normalize a value
// that PostgreSQL would never receive as valid UTF-8.
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

func requireExpectedHash(field, domain string, encoded []byte, expected string) error {
	if expected == "" || !validDigest(expected) || DomainHash(domain, encoded) != expected {
		return invalid(field+"Hash", "does not match exact canonical bytes")
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
		if err != nil || integer < -MaximumJavaScriptSafeInt || integer > MaximumJavaScriptSafeInt {
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
