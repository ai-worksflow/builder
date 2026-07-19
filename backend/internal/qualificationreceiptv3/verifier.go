package qualificationreceiptv3

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"reflect"
	"sort"

	"github.com/worksflow/builder/backend/internal/templateauthority"
)

type trustedRoleSigner struct {
	identity string
	keyID    string
	role     string
}

// Verifier is immutable and safe for concurrent use. It admits exactly two
// signatures: one runner key and one independent approver key. Neither roles,
// identities, algorithms, nor public keys are learned from the envelope.
type Verifier struct {
	dsse         *templateauthority.DSSEVerifier
	approver     trustedRoleSigner
	policyDigest string
	resolver     ExpectedResolver
	runner       trustedRoleSigner
}

type signerTrustDocument struct {
	Algorithm string `json:"algorithm"`
	Identity  string `json:"identity"`
	KeyID     string `json:"keyId"`
	PublicKey string `json:"publicKey"`
	Role      string `json:"role"`
}

type trustPolicyDocument struct {
	SchemaVersion string                `json:"schemaVersion"`
	Signers       []signerTrustDocument `json:"signers"`
}

// CanonicalTrustPolicyDigest binds the actual key bytes, key IDs, identities,
// algorithms, and roles used by Verifier. Digest itself is excluded from the
// projection so the commitment is acyclic.
func CanonicalTrustPolicyDigest(policy TrustPolicy) (string, error) {
	if policy.SchemaVersion != ReceiptTrustPolicySchemaV3 {
		return "", invalid("trustPolicy.schemaVersion", "schema is invalid")
	}
	if err := validateSignerTrust(policy.Runner, SignerRoleRunner); err != nil {
		return "", err
	}
	if err := validateSignerTrust(policy.Approver, SignerRoleApprover); err != nil {
		return "", err
	}
	if policy.Runner.KeyID == policy.Approver.KeyID || policy.Runner.Identity == policy.Approver.Identity ||
		bytes.Equal(policy.Runner.PublicKey, policy.Approver.PublicKey) {
		return "", invalid("trustPolicy", "runner and approver must use distinct key IDs, identities, and public keys")
	}
	signers := []signerTrustDocument{
		{Algorithm: string(templateauthority.AlgorithmEd25519), Identity: policy.Runner.Identity, KeyID: policy.Runner.KeyID, PublicKey: base64.StdEncoding.EncodeToString(policy.Runner.PublicKey), Role: SignerRoleRunner},
		{Algorithm: string(templateauthority.AlgorithmEd25519), Identity: policy.Approver.Identity, KeyID: policy.Approver.KeyID, PublicKey: base64.StdEncoding.EncodeToString(policy.Approver.PublicKey), Role: SignerRoleApprover},
	}
	sort.Slice(signers, func(i, j int) bool { return signers[i].KeyID < signers[j].KeyID })
	return CanonicalDigest(trustPolicyDocument{SchemaVersion: policy.SchemaVersion, Signers: signers})
}

func NewVerifier(policy TrustPolicy, resolver ExpectedResolver) (*Verifier, error) {
	if isNilInterface(resolver) {
		return nil, invalid("expectedResolver", "trusted resolver is nil")
	}
	policyDigest, err := CanonicalTrustPolicyDigest(policy)
	if err != nil {
		return nil, err
	}
	if !validDigest(policy.Digest) || policy.Digest != policyDigest {
		return nil, invalid("trustPolicy.digest", "does not hash the exact canonical keyful trust policy")
	}
	dsse, err := templateauthority.NewDSSEVerifier(templateauthority.DSSETrustPolicy{
		Keys: map[string]templateauthority.TrustedSigner{
			policy.Runner.KeyID: {
				Algorithm: templateauthority.AlgorithmEd25519,
				PublicKey: ed25519.PublicKey(bytes.Clone(policy.Runner.PublicKey)),
				Identity:  policy.Runner.Identity,
			},
			policy.Approver.KeyID: {
				Algorithm: templateauthority.AlgorithmEd25519,
				PublicKey: ed25519.PublicKey(bytes.Clone(policy.Approver.PublicKey)),
				Identity:  policy.Approver.Identity,
			},
		},
		AllowedPayloadTypes:   []string{InTotoPayloadType},
		AllowedPredicateTypes: []string{ReceiptPredicateTypeV3},
		MinSignatures:         2,
	})
	if err != nil {
		return nil, invalid("trustPolicy", "configure DSSE verification: %v", err)
	}
	return &Verifier{
		dsse:         dsse,
		policyDigest: policyDigest,
		resolver:     resolver,
		runner: trustedRoleSigner{
			identity: policy.Runner.Identity,
			keyID:    policy.Runner.KeyID,
			role:     SignerRoleRunner,
		},
		approver: trustedRoleSigner{
			identity: policy.Approver.Identity,
			keyID:    policy.Approver.KeyID,
			role:     SignerRoleApprover,
		},
	}, nil
}

// Verify resolves exact expected bytes from trusted immutable state using only
// opaque identities, then cryptographically verifies an exact canonical DSSE
// envelope against those bytes. A wire Receipt cannot be passed as expected
// authority at this boundary.
func (verifier *Verifier) Verify(ctx context.Context, envelopeJSON []byte, authorityID, receiptID string) (VerifiedReceipt, error) {
	if verifier == nil || verifier.dsse == nil || isNilInterface(verifier.resolver) {
		return VerifiedReceipt{}, verificationFailure("verifier is nil")
	}
	if isNilInterface(ctx) {
		return VerifiedReceipt{}, verificationFailure("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return VerifiedReceipt{}, verificationFailure("context is not active: %v", err)
	}
	if !validUUIDv4(authorityID) || !validStableID(receiptID) {
		return VerifiedReceipt{}, verificationFailure("opaque authority or Receipt identity is invalid")
	}
	resolution, err := verifier.resolver.ResolveExpected(ctx, authorityID, receiptID)
	if err != nil {
		return VerifiedReceipt{}, verificationFailure("resolve trusted expected Receipt: %v", err)
	}
	if err := ctx.Err(); err != nil {
		return VerifiedReceipt{}, verificationFailure("context ended while resolving expected Receipt: %v", err)
	}
	expectedPayload := bytes.Clone(resolution.Payload)
	if resolution.AuthorityID != authorityID || resolution.ReceiptID != receiptID ||
		!validDigest(resolution.PayloadDigest) || resolution.PayloadDigest != SHA256Digest(expectedPayload) {
		return VerifiedReceipt{}, verificationFailure("expected resolver returned identity or payload digest drift")
	}
	expected, err := DecodePayload(expectedPayload)
	if err != nil {
		return VerifiedReceipt{}, verificationFailure("trusted expected Receipt payload is invalid: %v", err)
	}
	if expected.PlanAuthority.AuthorityID != authorityID || expected.ReceiptID != receiptID {
		return VerifiedReceipt{}, verificationFailure("resolved payload identities do not match opaque lookup identities")
	}
	compiled, err := Compile(expected)
	if err != nil || !bytes.Equal(compiled.Payload, expectedPayload) {
		return VerifiedReceipt{}, verificationFailure("resolved payload is not the exact compiled expected Receipt: %v", err)
	}
	if !verifier.signersMatchReceipt(expected.Signers) {
		return VerifiedReceipt{}, verificationFailure("trusted signing policy does not match the Receipt signer binding")
	}
	if expected.Trust.TrustPolicyDigest != verifier.policyDigest {
		return VerifiedReceipt{}, verificationFailure("keyful trust policy digest drifted from frozen Plan trust")
	}
	verified, err := verifier.dsse.Verify(envelopeJSON, templateauthority.ExpectedSubject{
		Name:         compiled.SubjectName,
		SHA256Digest: compiled.SubjectDigest,
	})
	if err != nil {
		return VerifiedReceipt{}, verificationFailure("verify DSSE: %v", err)
	}
	if !bytes.Equal(envelopeJSON, verified.CanonicalEnvelope) {
		return VerifiedReceipt{}, verificationFailure("DSSE envelope is valid but not in its exact canonical representation")
	}
	actualReceipt, err := DecodePayload(verified.Payload)
	if err != nil {
		return VerifiedReceipt{}, verificationFailure("decode signed Receipt payload: %v", err)
	}
	if !bytes.Equal(verified.Payload, compiled.Payload) {
		return VerifiedReceipt{}, verificationFailure("signed payload does not equal the exact trusted expected Receipt")
	}
	exactPayloadDigest := SHA256Digest(verified.Payload)
	if verified.PayloadDigest != exactPayloadDigest || compiled.PayloadDigest != exactPayloadDigest {
		return VerifiedReceipt{}, verificationFailure("PayloadDigest is not SHA-256 of the decoded exact payload bytes")
	}
	if len(verified.Signers) != 2 {
		return VerifiedReceipt{}, verificationFailure("exactly one runner and one approver signature are required")
	}
	seenRunner, seenApprover := false, false
	for _, signer := range verified.Signers {
		if signer.Algorithm != templateauthority.AlgorithmEd25519 {
			return VerifiedReceipt{}, verificationFailure("signer %q used an unauthorized algorithm", signer.KeyID)
		}
		switch {
		case signer.KeyID == verifier.runner.keyID && signer.Identity == verifier.runner.identity:
			seenRunner = true
		case signer.KeyID == verifier.approver.keyID && signer.Identity == verifier.approver.identity:
			seenApprover = true
		default:
			return VerifiedReceipt{}, verificationFailure("verified signer is not the exact runner or approver authority")
		}
	}
	if !seenRunner || !seenApprover {
		return VerifiedReceipt{}, verificationFailure("both independent signer roles are required")
	}
	return VerifiedReceipt{
		Approver: VerifiedSigner{
			Identity: verifier.approver.identity,
			KeyID:    verifier.approver.keyID,
			Role:     verifier.approver.role,
		},
		CanonicalEnvelope: bytes.Clone(verified.CanonicalEnvelope),
		EnvelopeDigest:    verified.BundleDigest,
		Payload:           bytes.Clone(verified.Payload),
		PayloadDigest:     exactPayloadDigest,
		Receipt:           actualReceipt,
		Runner: VerifiedSigner{
			Identity: verifier.runner.identity,
			KeyID:    verifier.runner.keyID,
			Role:     verifier.runner.role,
		},
		SubjectDigest: compiled.SubjectDigest,
		SubjectName:   compiled.SubjectName,
	}, nil
}

func validateSignerTrust(signer SignerTrust, role string) error {
	if !validStableID(signer.KeyID) || !validIdentity(signer.Identity) || len(signer.PublicKey) != ed25519.PublicKeySize {
		return invalid("trustPolicy."+role, "key ID, identity, or Ed25519 public key is invalid")
	}
	return nil
}

func (verifier *Verifier) signersMatchReceipt(binding ReceiptSignerBinding) bool {
	return binding.Runner.Role == verifier.runner.role && binding.Runner.KeyID == verifier.runner.keyID &&
		binding.Runner.Identity == verifier.runner.identity && binding.Approver.Role == verifier.approver.role &&
		binding.Approver.KeyID == verifier.approver.keyID && binding.Approver.Identity == verifier.approver.identity
}

func verificationFailure(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrVerification, fmt.Sprintf(format, args...))
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
