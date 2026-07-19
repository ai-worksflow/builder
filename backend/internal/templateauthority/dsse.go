// Package templateauthority contains the cryptographic primitives used to
// admit template artifacts.  It deliberately accepts keyful, server-owned
// trust configuration only: references and caller-provided identity strings
// are never treated as proof of signature validity.
package templateauthority

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"
)

const (
	// InTotoStatementV1 is shared by signed attestation and SBOM validation.
	InTotoStatementV1 = "https://in-toto.io/Statement/v1"

	maxDSSEEnvelopeBytes = 8 << 20
	maxDSSEPayloadBytes  = 4 << 20
	maxDSSESignatures    = 64
)

// SignatureAlgorithm identifies the algorithm associated with a configured
// public key.  DSSE itself does not carry this value; the server-side trust
// policy is authoritative.
type SignatureAlgorithm string

const (
	AlgorithmEd25519     SignatureAlgorithm = "ed25519"
	AlgorithmECDSASHA256 SignatureAlgorithm = "ecdsa-sha256"
)

// TrustedSigner is a keyful trust-policy entry. PublicKey must be an
// ed25519.PublicKey for AlgorithmEd25519 or *ecdsa.PublicKey for
// AlgorithmECDSASHA256.
type TrustedSigner struct {
	Algorithm SignatureAlgorithm
	PublicKey any
	Identity  string
}

// DSSETrustPolicy is immutable after NewDSSEVerifier returns. All signatures
// present in an envelope must use a configured key ID and verify successfully;
// MinSignatures can require a threshold greater than one.
type DSSETrustPolicy struct {
	Keys                  map[string]TrustedSigner
	AllowedPayloadTypes   []string
	AllowedPredicateTypes []string
	MinSignatures         int
}

// ExpectedSubject is the one exact subject an admitted in-toto Statement must
// contain. Only lowercase SHA-256 is accepted so the comparison has one
// canonical representation.
type ExpectedSubject struct {
	Name         string
	SHA256Digest string
}

// VerifiedSigner reports identity derived from server-owned key configuration,
// never from the signed document.
type VerifiedSigner struct {
	KeyID     string
	Identity  string
	Algorithm SignatureAlgorithm
}

// VerifiedDSSE is the cryptographically verified, policy-bound result. The
// canonical envelope is included so a transparency verifier can bind the exact
// normalized bundle rather than trusting an external reference.
type VerifiedDSSE struct {
	PayloadType       string
	PredicateType     string
	Subject           ExpectedSubject
	PayloadDigest     string
	BundleDigest      string
	SignerIdentities  []string
	Signers           []VerifiedSigner
	Payload           []byte
	CanonicalEnvelope []byte
}

// VerificationError carries a stable code while retaining a diagnostic field
// and the underlying cause for server-side logging. It is shared by the OCI,
// SBOM, DSSE, and transparency verification layers.
type VerificationError struct {
	Code      ErrorCode
	Operation string
	Field     string
	Detail    string
	Cause     error
}

func (e *VerificationError) Error() string {
	if e == nil {
		return "template authority verification failed"
	}
	message := string(e.Code)
	if e.Operation != "" {
		message += " (" + e.Operation + ")"
	}
	if e.Field != "" {
		message += ": " + e.Field
	}
	if e.Detail != "" {
		message += ": " + e.Detail
	}
	return message
}

func (e *VerificationError) Unwrap() error { return e.Cause }

func verificationError(code, field, format string, args ...any) error {
	detail := fmt.Sprintf(format, args...)
	return &VerificationError{
		Code: ErrorCode(code), Operation: "verify cryptographic evidence",
		Field: field, Detail: detail, Cause: errors.New(detail),
	}
}

type trustedSigner struct {
	algorithm SignatureAlgorithm
	publicKey any
	identity  string
}

// DSSEVerifier verifies DSSE signatures and the signed in-toto statement under
// a copied server-side trust policy. It is safe for concurrent use.
type DSSEVerifier struct {
	keys              map[string]trustedSigner
	payloadTypes      map[string]struct{}
	predicateTypes    map[string]struct{}
	minimumSignatures int
}

// NewDSSEVerifier validates and copies policy material. An empty allowlist or
// empty key set is rejected instead of becoming an implicit allow-all policy.
func NewDSSEVerifier(policy DSSETrustPolicy) (*DSSEVerifier, error) {
	if len(policy.Keys) == 0 {
		return nil, errors.New("DSSE trust policy must contain at least one key")
	}
	minimum := policy.MinSignatures
	if minimum == 0 {
		minimum = 1
	}
	if minimum < 1 || minimum > len(policy.Keys) {
		return nil, fmt.Errorf("DSSE minimum signatures must be between 1 and %d", len(policy.Keys))
	}
	verifier := &DSSEVerifier{
		keys:              make(map[string]trustedSigner, len(policy.Keys)),
		payloadTypes:      make(map[string]struct{}, len(policy.AllowedPayloadTypes)),
		predicateTypes:    make(map[string]struct{}, len(policy.AllowedPredicateTypes)),
		minimumSignatures: minimum,
	}
	for keyID, configured := range policy.Keys {
		if !validPolicyToken(keyID) {
			return nil, fmt.Errorf("invalid DSSE key ID %q", keyID)
		}
		if !validPolicyToken(configured.Identity) {
			return nil, fmt.Errorf("invalid identity for DSSE key %q", keyID)
		}
		publicKey, err := copyPublicKey(configured.Algorithm, configured.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("invalid DSSE key %q: %w", keyID, err)
		}
		verifier.keys[keyID] = trustedSigner{
			algorithm: configured.Algorithm,
			publicKey: publicKey,
			identity:  configured.Identity,
		}
	}
	for _, value := range policy.AllowedPayloadTypes {
		if !validPolicyToken(value) {
			return nil, fmt.Errorf("invalid allowed DSSE payload type %q", value)
		}
		if _, duplicate := verifier.payloadTypes[value]; duplicate {
			return nil, fmt.Errorf("duplicate allowed DSSE payload type %q", value)
		}
		verifier.payloadTypes[value] = struct{}{}
	}
	if len(verifier.payloadTypes) == 0 {
		return nil, errors.New("DSSE payload type allowlist must not be empty")
	}
	for _, value := range policy.AllowedPredicateTypes {
		if !validPolicyToken(value) {
			return nil, fmt.Errorf("invalid allowed in-toto predicate type %q", value)
		}
		if _, duplicate := verifier.predicateTypes[value]; duplicate {
			return nil, fmt.Errorf("duplicate allowed in-toto predicate type %q", value)
		}
		verifier.predicateTypes[value] = struct{}{}
	}
	if len(verifier.predicateTypes) == 0 {
		return nil, errors.New("in-toto predicate type allowlist must not be empty")
	}
	return verifier, nil
}

type dsseEnvelope struct {
	PayloadType string          `json:"payloadType"`
	Payload     string          `json:"payload"`
	Signatures  []dsseSignature `json:"signatures"`
}

type dsseSignature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

type inTotoStatement struct {
	Type          string          `json:"_type"`
	Subject       []inTotoSubject `json:"subject"`
	PredicateType string          `json:"predicateType"`
	Predicate     json.RawMessage `json:"predicate"`
}

type inTotoSubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type decodedSignature struct {
	keyID string
	sig   []byte
}

// Verify parses an exact DSSE envelope, verifies every signature, then checks
// the signed payload as an in-toto Statement v1 with one exact subject.
func (v *DSSEVerifier) Verify(envelopeJSON []byte, expected ExpectedSubject) (*VerifiedDSSE, error) {
	if v == nil {
		return nil, verificationError("invalid_verifier", "", "DSSE verifier is nil")
	}
	if len(envelopeJSON) == 0 || len(envelopeJSON) > maxDSSEEnvelopeBytes {
		return nil, verificationError("invalid_envelope", "envelope", "size must be between 1 and %d bytes", maxDSSEEnvelopeBytes)
	}
	expectedDigest, err := normalizeSHA256Hex(expected.SHA256Digest)
	if err != nil || !validPolicyToken(expected.Name) {
		return nil, verificationError("invalid_expected_subject", "subject", "name and lowercase SHA-256 digest are required")
	}
	expected.SHA256Digest = expectedDigest

	var envelope dsseEnvelope
	if err := decodeStrictJSON(envelopeJSON, &envelope); err != nil {
		return nil, verificationError("invalid_envelope", "envelope", "%v", err)
	}
	if _, allowed := v.payloadTypes[envelope.PayloadType]; !allowed {
		return nil, verificationError("payload_type_not_allowed", "payloadType", "payload type %q is not allowed", envelope.PayloadType)
	}
	if len(envelope.Signatures) < v.minimumSignatures || len(envelope.Signatures) > maxDSSESignatures {
		return nil, verificationError("invalid_signature_count", "signatures", "got %d signatures; require %d..%d", len(envelope.Signatures), v.minimumSignatures, maxDSSESignatures)
	}
	payload, err := decodeCanonicalBase64(envelope.Payload)
	if err != nil || len(payload) == 0 || len(payload) > maxDSSEPayloadBytes {
		return nil, verificationError("invalid_payload_encoding", "payload", "payload must be strict standard base64 encoding of 1..%d bytes", maxDSSEPayloadBytes)
	}
	pae := DSSEPAE(envelope.PayloadType, payload)
	decoded := make([]decodedSignature, 0, len(envelope.Signatures))
	verifiedSigners := make([]VerifiedSigner, 0, len(envelope.Signatures))
	seenKeyIDs := make(map[string]struct{}, len(envelope.Signatures))
	for index, signature := range envelope.Signatures {
		field := fmt.Sprintf("signatures[%d]", index)
		if _, duplicate := seenKeyIDs[signature.KeyID]; duplicate {
			return nil, verificationError("duplicate_signature_key", field+".keyid", "key ID %q appears more than once", signature.KeyID)
		}
		seenKeyIDs[signature.KeyID] = struct{}{}
		trusted, exists := v.keys[signature.KeyID]
		if !exists {
			return nil, verificationError("untrusted_signature_key", field+".keyid", "key ID %q is not allowlisted", signature.KeyID)
		}
		rawSignature, decodeErr := decodeCanonicalBase64(signature.Sig)
		if decodeErr != nil || len(rawSignature) == 0 {
			return nil, verificationError("invalid_signature_encoding", field+".sig", "signature must be non-empty strict standard base64")
		}
		if !verifyConfiguredSignature(trusted, pae, rawSignature) {
			return nil, verificationError("signature_verification_failed", field+".sig", "signature for key %q is invalid", signature.KeyID)
		}
		decoded = append(decoded, decodedSignature{keyID: signature.KeyID, sig: rawSignature})
		verifiedSigners = append(verifiedSigners, VerifiedSigner{
			KeyID: signature.KeyID, Identity: trusted.identity, Algorithm: trusted.algorithm,
		})
	}

	var statement inTotoStatement
	if err := decodeStrictJSON(payload, &statement); err != nil {
		return nil, verificationError("invalid_in_toto_statement", "payload", "%v", err)
	}
	if statement.Type != InTotoStatementV1 {
		return nil, verificationError("invalid_statement_type", "payload._type", "got %q, require %q", statement.Type, InTotoStatementV1)
	}
	if _, allowed := v.predicateTypes[statement.PredicateType]; !allowed {
		return nil, verificationError("predicate_type_not_allowed", "payload.predicateType", "predicate type %q is not allowed", statement.PredicateType)
	}
	trimmedPredicate := bytes.TrimSpace(statement.Predicate)
	if len(trimmedPredicate) < 2 || trimmedPredicate[0] != '{' || trimmedPredicate[len(trimmedPredicate)-1] != '}' {
		return nil, verificationError("invalid_predicate", "payload.predicate", "predicate must be a non-null JSON object")
	}
	if len(statement.Subject) != 1 {
		return nil, verificationError("subject_mismatch", "payload.subject", "statement must contain exactly one subject")
	}
	subject := statement.Subject[0]
	if subject.Name != expected.Name || len(subject.Digest) != 1 || subject.Digest["sha256"] != expected.SHA256Digest {
		return nil, verificationError("subject_mismatch", "payload.subject[0]", "statement does not bind the exact expected name and SHA-256 digest")
	}

	canonicalEnvelope, err := canonicalizeDSSEEnvelope(envelope.PayloadType, payload, decoded)
	if err != nil {
		return nil, verificationError("canonicalization_failed", "envelope", "%v", err)
	}
	sort.Slice(verifiedSigners, func(i, j int) bool { return verifiedSigners[i].KeyID < verifiedSigners[j].KeyID })
	identities := make([]string, 0, len(verifiedSigners))
	seenIdentities := make(map[string]struct{}, len(verifiedSigners))
	for _, signer := range verifiedSigners {
		if _, exists := seenIdentities[signer.Identity]; exists {
			continue
		}
		seenIdentities[signer.Identity] = struct{}{}
		identities = append(identities, signer.Identity)
	}
	sort.Strings(identities)
	return &VerifiedDSSE{
		PayloadType:       envelope.PayloadType,
		PredicateType:     statement.PredicateType,
		Subject:           expected,
		PayloadDigest:     SHA256Digest(payload),
		BundleDigest:      SHA256Digest(canonicalEnvelope),
		SignerIdentities:  identities,
		Signers:           verifiedSigners,
		Payload:           bytes.Clone(payload),
		CanonicalEnvelope: bytes.Clone(canonicalEnvelope),
	}, nil
}

// DSSEPAE constructs the DSSE v1 pre-authentication encoding without using
// ambiguous delimiters: "DSSEv1 <len(type)> <type> <len(payload)> <payload>".
func DSSEPAE(payloadType string, payload []byte) []byte {
	return []byte(fmt.Sprintf("DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload))
}

// SHA256Digest returns the repository's canonical lowercase digest form.
func SHA256Digest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func canonicalizeDSSEEnvelope(payloadType string, payload []byte, signatures []decodedSignature) ([]byte, error) {
	sorted := append([]decodedSignature(nil), signatures...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].keyID == sorted[j].keyID {
			return bytes.Compare(sorted[i].sig, sorted[j].sig) < 0
		}
		return sorted[i].keyID < sorted[j].keyID
	})
	canonical := dsseEnvelope{
		PayloadType: payloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  make([]dsseSignature, 0, len(sorted)),
	}
	for _, signature := range sorted {
		canonical.Signatures = append(canonical.Signatures, dsseSignature{
			KeyID: signature.keyID,
			Sig:   base64.StdEncoding.EncodeToString(signature.sig),
		})
	}
	return json.Marshal(canonical)
}

func decodeCanonicalBase64(value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil {
		return nil, err
	}
	if base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("base64 value is not in canonical standard encoding")
	}
	return decoded, nil
}

func verifyConfiguredSignature(signer trustedSigner, message, signature []byte) bool {
	switch signer.algorithm {
	case AlgorithmEd25519:
		publicKey, ok := signer.publicKey.(ed25519.PublicKey)
		return ok && ed25519.Verify(publicKey, message, signature)
	case AlgorithmECDSASHA256:
		publicKey, ok := signer.publicKey.(*ecdsa.PublicKey)
		if !ok {
			return false
		}
		digest := sha256.Sum256(message)
		return ecdsa.VerifyASN1(publicKey, digest[:], signature)
	default:
		return false
	}
}

func copyPublicKey(algorithm SignatureAlgorithm, input any) (any, error) {
	switch algorithm {
	case AlgorithmEd25519:
		publicKey, ok := input.(ed25519.PublicKey)
		if !ok || len(publicKey) != ed25519.PublicKeySize {
			return nil, errors.New("ed25519 public key has the wrong type or length")
		}
		return ed25519.PublicKey(bytes.Clone(publicKey)), nil
	case AlgorithmECDSASHA256:
		publicKey, ok := input.(*ecdsa.PublicKey)
		if !ok || publicKey == nil || publicKey.Curve == nil || publicKey.X == nil || publicKey.Y == nil || !publicKey.Curve.IsOnCurve(publicKey.X, publicKey.Y) {
			return nil, errors.New("ECDSA public key is missing or invalid")
		}
		copy := &ecdsa.PublicKey{Curve: publicKey.Curve, X: newBigInt(publicKey.X), Y: newBigInt(publicKey.Y)}
		return copy, nil
	default:
		return nil, fmt.Errorf("unsupported signature algorithm %q", algorithm)
	}
}

// newBigInt is kept as a small indirection so copied trust policy keys cannot
// be mutated through aliases held by configuration callers.
func newBigInt(value interface{ Bytes() []byte }) *big.Int {
	return new(big.Int).SetBytes(value.Bytes())
}

func normalizeSHA256Hex(value string) (string, error) {
	if strings.HasPrefix(value, "sha256:") {
		value = strings.TrimPrefix(value, "sha256:")
	}
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return "", errors.New("SHA-256 digest must be 64 lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", errors.New("SHA-256 digest is not hexadecimal")
	}
	return value, nil
}

func validPolicyToken(value string) bool {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func decodeStrictJSON(input []byte, target any) error {
	if err := rejectDuplicateJSONNames(input); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	return nil
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
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate JSON object key %q", name)
			}
			seen[name] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("JSON object is not terminated")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("JSON array is not terminated")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
