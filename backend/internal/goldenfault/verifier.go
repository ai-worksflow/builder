package goldenfault

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/worksflow/builder/backend/internal/templateauthority"
)

const FaultOperatorRole = "fault-operator"

type SignerTrust struct {
	Algorithm templateauthority.SignatureAlgorithm
	PublicKey any
	Identity  string
	Role      string
	NotBefore time.Time
	NotAfter  time.Time
	RevokedAt *time.Time
}

type TrustPolicy struct {
	Signers           map[string]SignerTrust
	MinimumSignatures int
}

type trustedSigner struct {
	algorithm templateauthority.SignatureAlgorithm
	publicKey any
	identity  string
	notBefore time.Time
	notAfter  time.Time
	revokedAt *time.Time
}

type Verifier struct {
	signers           map[string]trustedSigner
	minimumSignatures int
}

func NewVerifier(policy TrustPolicy) (*Verifier, error) {
	if len(policy.Signers) == 0 || len(policy.Signers) > maximumSignatures ||
		policy.MinimumSignatures < 1 || policy.MinimumSignatures > len(policy.Signers) {
		return nil, fmt.Errorf("%w: fault-operator trust threshold is invalid", ErrUntrustedSigner)
	}
	verifier := &Verifier{
		signers:           make(map[string]trustedSigner, len(policy.Signers)),
		minimumSignatures: policy.MinimumSignatures,
	}
	seenIdentities := make(map[string]string, len(policy.Signers))
	for keyID, signer := range policy.Signers {
		if !validIdentity(keyID) || !validIdentity(signer.Identity) || signer.Role != FaultOperatorRole ||
			signer.NotBefore.IsZero() || signer.NotAfter.IsZero() || !signer.NotAfter.After(signer.NotBefore) {
			return nil, fmt.Errorf("%w: signer %q has an invalid identity, role, or validity interval", ErrUntrustedSigner, keyID)
		}
		if priorKeyID, duplicate := seenIdentities[signer.Identity]; duplicate {
			return nil, fmt.Errorf(
				"%w: signer identity %q is reused by keys %q and %q",
				ErrUntrustedSigner, signer.Identity, priorKeyID, keyID,
			)
		}
		seenIdentities[signer.Identity] = keyID
		publicKey, err := copyPublicKey(signer.Algorithm, signer.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("%w: signer %q: %v", ErrUntrustedSigner, keyID, err)
		}
		var revokedAt *time.Time
		if signer.RevokedAt != nil {
			value := signer.RevokedAt.UTC()
			if value.Before(signer.NotBefore.UTC()) || !value.Before(signer.NotAfter.UTC()) {
				return nil, fmt.Errorf("%w: signer %q revocation is outside its validity interval", ErrUntrustedSigner, keyID)
			}
			revokedAt = &value
		}
		verifier.signers[keyID] = trustedSigner{
			algorithm: signer.Algorithm, publicKey: publicKey, identity: signer.Identity,
			notBefore: signer.NotBefore.UTC(), notAfter: signer.NotAfter.UTC(), revokedAt: revokedAt,
		}
	}
	return verifier, nil
}

type dsseEnvelope struct {
	Payload     string          `json:"payload"`
	PayloadType string          `json:"payloadType"`
	Signatures  []dsseSignature `json:"signatures"`
}

type dsseSignature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

// VerifyAt verifies exact canonical envelope and predicate bytes, the
// fault-operator trust policy, the fixture-provided digest fence, and trusted
// service time. A valid signature alone is intentionally insufficient.
func (verifier *Verifier) VerifyAt(envelopeJSON []byte, expected ExpectedBinding, now time.Time) (VerifiedAuthority, error) {
	if verifier == nil || len(verifier.signers) == 0 || now.IsZero() {
		return VerifiedAuthority{}, fmt.Errorf("%w: verifier and trusted service time are required", ErrInvalidAuthority)
	}
	if err := validateExpectedBinding(expected); err != nil {
		return VerifiedAuthority{}, err
	}
	if len(envelopeJSON) == 0 || len(envelopeJSON) > maximumEnvelopeBytes {
		return VerifiedAuthority{}, fmt.Errorf("%w: DSSE envelope size is outside 1..%d bytes", ErrInvalidAuthority, maximumEnvelopeBytes)
	}
	if sha256Digest(envelopeJSON) != expected.EnvelopeDigest {
		return VerifiedAuthority{}, fmt.Errorf("%w: DSSE envelope digest does not match the Golden fixture", ErrInvalidAuthority)
	}
	if err := validateEnvelopeShape(envelopeJSON); err != nil {
		return VerifiedAuthority{}, fmt.Errorf("%w: DSSE envelope shape: %v", ErrInvalidAuthority, err)
	}
	var envelope dsseEnvelope
	if err := decodeStrictJSON(envelopeJSON, &envelope); err != nil {
		return VerifiedAuthority{}, fmt.Errorf("%w: decode DSSE envelope: %v", ErrInvalidAuthority, err)
	}
	if envelope.PayloadType != PayloadTypeV1 || len(envelope.Signatures) < verifier.minimumSignatures ||
		len(envelope.Signatures) > maximumSignatures {
		return VerifiedAuthority{}, fmt.Errorf("%w: DSSE payload type or signature threshold is invalid", ErrInvalidAuthority)
	}
	payload, err := decodeCanonicalBase64(envelope.Payload, maximumPayloadBytes)
	if err != nil || sha256Digest(payload) != expected.PayloadDigest {
		return VerifiedAuthority{}, fmt.Errorf("%w: DSSE payload encoding or digest is invalid", ErrInvalidAuthority)
	}
	if err := requireExactObject(payload, map[string]valueKind{
		"authorityId": valueString, "expectedFenceDigest": valueString, "expiresAt": valueString,
		"fixtureId": valueString, "issuedAt": valueString, "maxUses": valueInteger,
		"operationKind": valueString, "resourceSelector": valueString, "runId": valueString,
		"schemaVersion": valueString,
	}); err != nil {
		return VerifiedAuthority{}, fmt.Errorf("%w: authority predicate shape: %v", ErrInvalidAuthority, err)
	}
	var predicate AuthorityPredicate
	if err := decodeStrictJSON(payload, &predicate); err != nil {
		return VerifiedAuthority{}, fmt.Errorf("%w: decode authority predicate: %v", ErrInvalidAuthority, err)
	}
	canonicalPayload, err := canonicalJSON(predicate)
	if err != nil || !bytes.Equal(payload, canonicalPayload) {
		return VerifiedAuthority{}, fmt.Errorf("%w: authority predicate is not canonical JSON", ErrInvalidAuthority)
	}
	issuedAt, expiresAt, err := validatePredicate(predicate, expected, now.UTC())
	if err != nil {
		return VerifiedAuthority{}, err
	}

	pae := templateauthority.DSSEPAE(envelope.PayloadType, payload)
	identities := make([]string, 0, len(envelope.Signatures))
	priorKeyID := ""
	for index, signature := range envelope.Signatures {
		if !validIdentity(signature.KeyID) || (index > 0 && priorKeyID >= signature.KeyID) {
			return VerifiedAuthority{}, fmt.Errorf("%w: DSSE signatures must be uniquely sorted by keyid", ErrInvalidAuthority)
		}
		priorKeyID = signature.KeyID
		trusted, exists := verifier.signers[signature.KeyID]
		if !exists {
			return VerifiedAuthority{}, fmt.Errorf("%w: DSSE key %q is not allowlisted", ErrUntrustedSigner, signature.KeyID)
		}
		if issuedAt.Before(trusted.notBefore) || !issuedAt.Before(trusted.notAfter) ||
			(trusted.revokedAt != nil && !issuedAt.Before(*trusted.revokedAt)) {
			return VerifiedAuthority{}, fmt.Errorf("%w: signer %q was not valid at authority issuance", ErrUntrustedSigner, signature.KeyID)
		}
		rawSignature, err := decodeCanonicalBase64(signature.Sig, 4096)
		if err != nil || !verifySignature(trusted, pae, rawSignature) {
			return VerifiedAuthority{}, fmt.Errorf("%w: signature for key %q is invalid", ErrUntrustedSigner, signature.KeyID)
		}
		identities = append(identities, trusted.identity)
	}
	sort.Strings(identities)
	if !sortedUnique(identities) {
		return VerifiedAuthority{}, fmt.Errorf("%w: signer identity closure is invalid", ErrUntrustedSigner)
	}
	canonicalEnvelope, err := canonicalJSON(envelope)
	if err != nil || !bytes.Equal(envelopeJSON, canonicalEnvelope) {
		return VerifiedAuthority{}, fmt.Errorf("%w: DSSE envelope is not canonical JSON", ErrInvalidAuthority)
	}
	return VerifiedAuthority{
		Predicate: predicate, EnvelopeDigest: expected.EnvelopeDigest, PayloadDigest: expected.PayloadDigest,
		SignerIdentities: append([]string(nil), identities...), IssuedAt: issuedAt, ExpiresAt: expiresAt,
	}, nil
}

func validateExpectedBinding(expected ExpectedBinding) error {
	selector, exists := selectorForOperation(expected.OperationKind)
	if expected.AuthorityID.Version() != 4 || expected.FixtureID.Version() != 4 || expected.RunID.Version() != 4 ||
		!exists || expected.ResourceSelector != selector || !validDigest(expected.ExpectedFenceDigest) ||
		!validDigest(expected.EnvelopeDigest) || !validDigest(expected.PayloadDigest) ||
		expected.EnvelopeDigest == expected.PayloadDigest {
		return fmt.Errorf("%w: Golden fixture fault binding is incomplete or non-canonical", ErrInvalidAuthority)
	}
	return nil
}

func validatePredicate(predicate AuthorityPredicate, expected ExpectedBinding, now time.Time) (time.Time, time.Time, error) {
	selector, exists := selectorForOperation(predicate.OperationKind)
	if predicate.SchemaVersion != AuthoritySchemaV1 || predicate.MaxUses != 1 || !exists ||
		predicate.ResourceSelector != selector || !validUUID(predicate.AuthorityID) ||
		!validUUID(predicate.FixtureID) || !validUUID(predicate.RunID) || !validDigest(predicate.ExpectedFenceDigest) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: authority predicate bindings are invalid", ErrInvalidAuthority)
	}
	if predicate.AuthorityID != expected.AuthorityID.String() || predicate.FixtureID != expected.FixtureID.String() ||
		predicate.RunID != expected.RunID.String() || predicate.OperationKind != expected.OperationKind ||
		predicate.ResourceSelector != expected.ResourceSelector || predicate.ExpectedFenceDigest != expected.ExpectedFenceDigest {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: signed authority does not match the exact Golden fixture binding", ErrInvalidAuthority)
	}
	issuedAt, err := parseCanonicalTime(predicate.IssuedAt, "authority.issuedAt")
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: %v", ErrInvalidAuthority, err)
	}
	expiresAt, err := parseCanonicalTime(predicate.ExpiresAt, "authority.expiresAt")
	if err != nil || !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > MaximumAuthorityLifetime {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: authority lifetime must be positive and no longer than 30 minutes", ErrInvalidAuthority)
	}
	if now.Before(issuedAt.Add(-MaximumClockSkew)) || !now.Before(expiresAt) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: authority is not valid at trusted service time", ErrInvalidAuthority)
	}
	return issuedAt, expiresAt, nil
}

func validateEnvelopeShape(input []byte) error {
	value, err := decodeStrictValue(input)
	if err != nil {
		return err
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 3 {
		return errors.New("DSSE envelope must have exactly payload, payloadType, and signatures")
	}
	if _, ok := object["payload"].(string); !ok {
		return errors.New("DSSE payload must be a non-null string")
	}
	if _, ok := object["payloadType"].(string); !ok {
		return errors.New("DSSE payloadType must be a non-null string")
	}
	rawSignatures, ok := object["signatures"].([]any)
	if !ok || len(rawSignatures) == 0 || len(rawSignatures) > maximumSignatures {
		return errors.New("DSSE signatures must be a bounded non-null array")
	}
	if len(object) != 3 || object["payload"] == nil || object["payloadType"] == nil || object["signatures"] == nil {
		return errors.New("DSSE envelope contains missing or null fields")
	}
	for index, raw := range rawSignatures {
		signature, ok := raw.(map[string]any)
		if !ok || len(signature) != 2 || signature["keyid"] == nil || signature["sig"] == nil {
			return fmt.Errorf("DSSE signature %d must have exactly non-null keyid and sig", index)
		}
		if _, ok := signature["keyid"].(string); !ok {
			return fmt.Errorf("DSSE signature %d keyid must be a string", index)
		}
		if _, ok := signature["sig"].(string); !ok {
			return fmt.Errorf("DSSE signature %d sig must be a string", index)
		}
	}
	return nil
}

func copyPublicKey(algorithm templateauthority.SignatureAlgorithm, input any) (any, error) {
	switch algorithm {
	case templateauthority.AlgorithmEd25519:
		key, ok := input.(ed25519.PublicKey)
		if !ok || len(key) != ed25519.PublicKeySize {
			return nil, errors.New("ed25519 public key has the wrong type or length")
		}
		return ed25519.PublicKey(bytes.Clone(key)), nil
	case templateauthority.AlgorithmECDSASHA256:
		key, ok := input.(*ecdsa.PublicKey)
		if !ok || key == nil || key.Curve == nil || key.X == nil || key.Y == nil || !key.Curve.IsOnCurve(key.X, key.Y) {
			return nil, errors.New("ECDSA public key is missing or invalid")
		}
		return &ecdsa.PublicKey{Curve: key.Curve, X: new(big.Int).Set(key.X), Y: new(big.Int).Set(key.Y)}, nil
	default:
		return nil, fmt.Errorf("unsupported signature algorithm %q", algorithm)
	}
}

func verifySignature(signer trustedSigner, message, signature []byte) bool {
	switch signer.algorithm {
	case templateauthority.AlgorithmEd25519:
		key, ok := signer.publicKey.(ed25519.PublicKey)
		return ok && ed25519.Verify(key, message, signature)
	case templateauthority.AlgorithmECDSASHA256:
		key, ok := signer.publicKey.(*ecdsa.PublicKey)
		if !ok {
			return false
		}
		digest := sha256.Sum256(message)
		return ecdsa.VerifyASN1(key, digest[:], signature)
	default:
		return false
	}
}

func encodeEnvelopeForTest(payload []byte, signatures []dsseSignature) ([]byte, error) {
	return canonicalJSON(dsseEnvelope{
		Payload: base64.StdEncoding.EncodeToString(payload), PayloadType: PayloadTypeV1, Signatures: signatures,
	})
}
