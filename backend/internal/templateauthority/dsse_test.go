package templateauthority

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

const (
	testPayloadType   = "application/vnd.in-toto+json"
	testPredicateType = "https://worksflow.example/template-release/v1"
)

type dsseTestKey struct {
	algorithm SignatureAlgorithm
	private   any
	public    any
	identity  string
}

func TestDSSEVerifierAcceptsEd25519AndECDSAThreshold(t *testing.T) {
	ed := newDSSETestKey(t, AlgorithmEd25519, "builder@example.test")
	ecdsaKey := newDSSETestKey(t, AlgorithmECDSASHA256, "release@example.test")
	artifact := []byte("exact-template-artifact")
	expected := ExpectedSubject{Name: "registry.example.test/templates/app", SHA256Digest: SHA256Digest(artifact)}
	payload := testStatement(t, expected, testPredicateType)
	envelope := testDSSEEnvelope(t, testPayloadType, payload, map[string]dsseTestKey{
		"release-ed25519": ed,
		"release-ecdsa":   ecdsaKey,
	}, nil)
	verifier := testDSSEVerifier(t, map[string]dsseTestKey{
		"release-ed25519": ed,
		"release-ecdsa":   ecdsaKey,
	}, 2)

	verified, err := verifier.Verify(envelope, expected)
	if err != nil {
		t.Fatalf("verify DSSE: %v", err)
	}
	if verified.PayloadDigest != SHA256Digest(payload) {
		t.Fatalf("payload digest = %q", verified.PayloadDigest)
	}
	if verified.BundleDigest != SHA256Digest(verified.CanonicalEnvelope) {
		t.Fatalf("bundle digest = %q", verified.BundleDigest)
	}
	if !slices.Equal(verified.SignerIdentities, []string{"builder@example.test", "release@example.test"}) {
		t.Fatalf("signer identities = %#v", verified.SignerIdentities)
	}
	if len(verified.Signers) != 2 || verified.Signers[0].KeyID != "release-ecdsa" || verified.Signers[1].KeyID != "release-ed25519" {
		t.Fatalf("verified signers = %#v", verified.Signers)
	}
	if verified.Subject.SHA256Digest != expected.SHA256Digest[len("sha256:"):] {
		t.Fatalf("normalized subject digest = %q", verified.Subject.SHA256Digest)
	}

	// Reordering the same signatures does not change the canonical bundle.
	var document dsseEnvelope
	if err := json.Unmarshal(envelope, &document); err != nil {
		t.Fatal(err)
	}
	document.Signatures[0], document.Signatures[1] = document.Signatures[1], document.Signatures[0]
	reordered, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	second, err := verifier.Verify(reordered, expected)
	if err != nil {
		t.Fatalf("verify reordered DSSE: %v", err)
	}
	if second.BundleDigest != verified.BundleDigest {
		t.Fatalf("canonical bundle changed after signature reorder: %q != %q", second.BundleDigest, verified.BundleDigest)
	}
}

func TestDSSEVerifierRejectsTamperingAndPolicyViolations(t *testing.T) {
	key := newDSSETestKey(t, AlgorithmEd25519, "builder@example.test")
	artifact := []byte("exact-template-artifact")
	expected := ExpectedSubject{Name: "registry.example.test/templates/app", SHA256Digest: SHA256Digest(artifact)}
	validPayload := testStatement(t, expected, testPredicateType)
	validEnvelope := testDSSEEnvelope(t, testPayloadType, validPayload, map[string]dsseTestKey{"release": key}, nil)
	verifier := testDSSEVerifier(t, map[string]dsseTestKey{"release": key}, 1)

	tests := []struct {
		name string
		code string
		edit func(t *testing.T, envelope dsseEnvelope) dsseEnvelope
	}{
		{
			name: "payload bytes", code: "signature_verification_failed",
			edit: func(t *testing.T, envelope dsseEnvelope) dsseEnvelope {
				payload := append([]byte(nil), validPayload...)
				payload[len(payload)-2] ^= 1
				envelope.Payload = base64.StdEncoding.EncodeToString(payload)
				return envelope
			},
		},
		{
			name: "signature", code: "signature_verification_failed",
			edit: func(t *testing.T, envelope dsseEnvelope) dsseEnvelope {
				signature, err := base64.StdEncoding.DecodeString(envelope.Signatures[0].Sig)
				if err != nil {
					t.Fatal(err)
				}
				signature[0] ^= 1
				envelope.Signatures[0].Sig = base64.StdEncoding.EncodeToString(signature)
				return envelope
			},
		},
		{
			name: "key ID", code: "untrusted_signature_key",
			edit: func(t *testing.T, envelope dsseEnvelope) dsseEnvelope {
				envelope.Signatures[0].KeyID = "unknown"
				return envelope
			},
		},
		{
			name: "payload type", code: "payload_type_not_allowed",
			edit: func(t *testing.T, envelope dsseEnvelope) dsseEnvelope {
				envelope.PayloadType = "application/unknown"
				return envelope
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var envelope dsseEnvelope
			if err := json.Unmarshal(validEnvelope, &envelope); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(test.edit(t, envelope))
			if err != nil {
				t.Fatal(err)
			}
			_, err = verifier.Verify(encoded, expected)
			assertVerificationCode(t, err, test.code)
		})
	}

	t.Run("subject signed but wrong", func(t *testing.T) {
		wrong := expected
		wrong.SHA256Digest = SHA256Digest([]byte("different artifact"))
		envelope := testDSSEEnvelope(t, testPayloadType, testStatement(t, wrong, testPredicateType), map[string]dsseTestKey{"release": key}, nil)
		_, err := verifier.Verify(envelope, expected)
		assertVerificationCode(t, err, "subject_mismatch")
	})

	t.Run("predicate type signed but denied", func(t *testing.T) {
		envelope := testDSSEEnvelope(t, testPayloadType, testStatement(t, expected, "https://untrusted.example/predicate"), map[string]dsseTestKey{"release": key}, nil)
		_, err := verifier.Verify(envelope, expected)
		assertVerificationCode(t, err, "predicate_type_not_allowed")
	})

	t.Run("predicate is not an object", func(t *testing.T) {
		statement := inTotoStatement{
			Type:          InTotoStatementV1,
			Subject:       []inTotoSubject{{Name: expected.Name, Digest: map[string]string{"sha256": expected.SHA256Digest[len("sha256:"):]}}},
			PredicateType: testPredicateType,
			Predicate:     json.RawMessage(`[]`),
		}
		payload, err := json.Marshal(statement)
		if err != nil {
			t.Fatal(err)
		}
		envelope := testDSSEEnvelope(t, testPayloadType, payload, map[string]dsseTestKey{"release": key}, nil)
		_, err = verifier.Verify(envelope, expected)
		assertVerificationCode(t, err, "invalid_predicate")
	})

	t.Run("duplicate key IDs", func(t *testing.T) {
		var envelope dsseEnvelope
		if err := json.Unmarshal(validEnvelope, &envelope); err != nil {
			t.Fatal(err)
		}
		envelope.Signatures = append(envelope.Signatures, envelope.Signatures[0])
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		_, err = verifier.Verify(encoded, expected)
		assertVerificationCode(t, err, "duplicate_signature_key")
	})
}

func TestDSSEVerifierStrictParsingAndTrustPolicy(t *testing.T) {
	key := newDSSETestKey(t, AlgorithmEd25519, "builder@example.test")
	artifactDigest := SHA256Digest([]byte("artifact"))
	expected := ExpectedSubject{Name: "artifact", SHA256Digest: artifactDigest}
	verifier := testDSSEVerifier(t, map[string]dsseTestKey{"release": key}, 1)
	payload := testStatement(t, expected, testPredicateType)
	envelope := testDSSEEnvelope(t, testPayloadType, payload, map[string]dsseTestKey{"release": key}, nil)

	t.Run("duplicate JSON field", func(t *testing.T) {
		duplicate := []byte(`{"payloadType":"` + testPayloadType + `","payloadType":"` + testPayloadType + `","payload":"AA==","signatures":[]}`)
		_, err := verifier.Verify(duplicate, expected)
		assertVerificationCode(t, err, "invalid_envelope")
	})
	t.Run("unknown JSON field", func(t *testing.T) {
		var object map[string]any
		if err := json.Unmarshal(envelope, &object); err != nil {
			t.Fatal(err)
		}
		object["claimedSigner"] = "attacker"
		encoded, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		_, err = verifier.Verify(encoded, expected)
		assertVerificationCode(t, err, "invalid_envelope")
	})
	t.Run("malformed base64", func(t *testing.T) {
		var document dsseEnvelope
		if err := json.Unmarshal(envelope, &document); err != nil {
			t.Fatal(err)
		}
		document.Payload += "\n"
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		_, err = verifier.Verify(encoded, expected)
		assertVerificationCode(t, err, "invalid_payload_encoding")
	})
	t.Run("policy is copied", func(t *testing.T) {
		public := append(ed25519.PublicKey(nil), key.public.(ed25519.PublicKey)...)
		policy := DSSETrustPolicy{
			Keys:                map[string]TrustedSigner{"release": {Algorithm: AlgorithmEd25519, PublicKey: public, Identity: key.identity}},
			AllowedPayloadTypes: []string{testPayloadType}, AllowedPredicateTypes: []string{testPredicateType},
		}
		copied, err := NewDSSEVerifier(policy)
		if err != nil {
			t.Fatal(err)
		}
		for index := range public {
			public[index] = 0
		}
		delete(policy.Keys, "release")
		if _, err := copied.Verify(envelope, expected); err != nil {
			t.Fatalf("caller mutated copied trust policy: %v", err)
		}
	})
	if _, err := NewDSSEVerifier(DSSETrustPolicy{}); err == nil {
		t.Fatal("empty trust policy was accepted")
	}
	if got := string(DSSEPAE("type", []byte("body"))); got != "DSSEv1 4 type 4 body" {
		t.Fatalf("PAE = %q", got)
	}
}

func newDSSETestKey(t *testing.T, algorithm SignatureAlgorithm, identity string) dsseTestKey {
	t.Helper()
	switch algorithm {
	case AlgorithmEd25519:
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return dsseTestKey{algorithm: algorithm, private: private, public: public, identity: identity}
	case AlgorithmECDSASHA256:
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return dsseTestKey{algorithm: algorithm, private: private, public: &private.PublicKey, identity: identity}
	default:
		t.Fatalf("unsupported test algorithm %q", algorithm)
		return dsseTestKey{}
	}
}

func (key dsseTestKey) sign(t *testing.T, message []byte) []byte {
	t.Helper()
	switch key.algorithm {
	case AlgorithmEd25519:
		return ed25519.Sign(key.private.(ed25519.PrivateKey), message)
	case AlgorithmECDSASHA256:
		digest := sha256.Sum256(message)
		signature, err := ecdsa.SignASN1(rand.Reader, key.private.(*ecdsa.PrivateKey), digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return signature
	default:
		t.Fatalf("unsupported test algorithm %q", key.algorithm)
		return nil
	}
}

func testDSSEVerifier(t *testing.T, keys map[string]dsseTestKey, minimum int) *DSSEVerifier {
	t.Helper()
	configured := make(map[string]TrustedSigner, len(keys))
	for keyID, key := range keys {
		configured[keyID] = TrustedSigner{Algorithm: key.algorithm, PublicKey: key.public, Identity: key.identity}
	}
	verifier, err := NewDSSEVerifier(DSSETrustPolicy{
		Keys: configured, AllowedPayloadTypes: []string{testPayloadType},
		AllowedPredicateTypes: []string{testPredicateType}, MinSignatures: minimum,
	})
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func testStatement(t *testing.T, expected ExpectedSubject, predicateType string) []byte {
	t.Helper()
	digest, err := normalizeSHA256Hex(expected.SHA256Digest)
	if err != nil {
		t.Fatal(err)
	}
	statement := inTotoStatement{
		Type:          InTotoStatementV1,
		Subject:       []inTotoSubject{{Name: expected.Name, Digest: map[string]string{"sha256": digest}}},
		PredicateType: predicateType,
		Predicate:     json.RawMessage(`{"builder":{"id":"worksflow-test"}}`),
	}
	payload, err := json.Marshal(statement)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func testDSSEEnvelope(t *testing.T, payloadType string, payload []byte, keys map[string]dsseTestKey, keyOrder []string) []byte {
	t.Helper()
	if keyOrder == nil {
		for keyID := range keys {
			keyOrder = append(keyOrder, keyID)
		}
		slices.Sort(keyOrder)
	}
	pae := DSSEPAE(payloadType, payload)
	envelope := dsseEnvelope{PayloadType: payloadType, Payload: base64.StdEncoding.EncodeToString(payload)}
	for _, keyID := range keyOrder {
		envelope.Signatures = append(envelope.Signatures, dsseSignature{
			KeyID: keyID, Sig: base64.StdEncoding.EncodeToString(keys[keyID].sign(t, pae)),
		})
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertVerificationCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected verification error %q", code)
	}
	var verification *VerificationError
	if !errors.As(err, &verification) {
		t.Fatalf("error %T is not VerificationError: %v", err, err)
	}
	if string(verification.Code) != code {
		t.Fatalf("verification code = %q, want %q: %v", verification.Code, code, err)
	}
}
