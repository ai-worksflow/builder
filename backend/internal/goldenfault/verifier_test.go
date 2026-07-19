package goldenfault

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/templateauthority"
)

type faultTestKey struct {
	keyID    string
	identity string
	public   ed25519.PublicKey
	private  ed25519.PrivateKey
}

type signedFaultFixture struct {
	now       time.Time
	predicate AuthorityPredicate
	expected  ExpectedBinding
	envelope  []byte
	key       faultTestKey
}

func TestVerifierAcceptsExactCanonicalOneShotAuthority(t *testing.T) {
	fixture := newSignedFaultFixture(t)
	verifier := testFaultVerifier(t, fixture.key)

	verified, err := verifier.VerifyAt(fixture.envelope, fixture.expected, fixture.now)
	if err != nil {
		t.Fatalf("VerifyAt() error = %v", err)
	}
	if verified.Predicate != fixture.predicate || verified.EnvelopeDigest != fixture.expected.EnvelopeDigest ||
		verified.PayloadDigest != fixture.expected.PayloadDigest || len(verified.SignerIdentities) != 1 ||
		verified.SignerIdentities[0] != fixture.key.identity {
		t.Fatalf("verified authority = %+v", verified)
	}
}

func TestVerifierRejectsNonCanonicalUnknownNullDuplicateTrailingAndBOM(t *testing.T) {
	fixture := newSignedFaultFixture(t)
	verifier := testFaultVerifier(t, fixture.key)
	canonicalPayload, err := canonicalJSON(fixture.predicate)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		payload  []byte
		envelope func([]byte) []byte
	}{
		{name: "payload unknown", payload: append(canonicalPayload[:len(canonicalPayload)-1], []byte(`,"signal":"KILL"}`)...)},
		{name: "payload null", payload: []byte(fmt.Sprintf(
			`{"authorityId":null,"expectedFenceDigest":%q,"expiresAt":%q,"fixtureId":%q,"issuedAt":%q,"maxUses":1,"operationKind":%q,"resourceSelector":%q,"runId":%q,"schemaVersion":%q}`,
			fixture.predicate.ExpectedFenceDigest, fixture.predicate.ExpiresAt, fixture.predicate.FixtureID,
			fixture.predicate.IssuedAt, fixture.predicate.OperationKind, fixture.predicate.ResourceSelector,
			fixture.predicate.RunID, fixture.predicate.SchemaVersion,
		))},
		{name: "payload duplicate", payload: append([]byte(`{"authorityId":"`+fixture.predicate.AuthorityID+`","authorityId":"`+fixture.predicate.AuthorityID+`",`), canonicalPayload[len(`{"authorityId":"`+fixture.predicate.AuthorityID+`",`):]...)},
		{name: "payload trailing", payload: append(append([]byte(nil), canonicalPayload...), []byte(` {}`)...)},
		{name: "payload BOM", payload: append([]byte{0xef, 0xbb, 0xbf}, canonicalPayload...)},
		{name: "payload noncanonical", payload: append([]byte(" "), canonicalPayload...)},
		{name: "envelope unknown", payload: canonicalPayload, envelope: func(valid []byte) []byte {
			return append(valid[:len(valid)-1], []byte(`,"url":"https://forbidden.example"}`)...)
		}},
		{name: "envelope trailing", payload: canonicalPayload, envelope: func(valid []byte) []byte {
			return append(append([]byte(nil), valid...), '\n')
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := test.payload
			envelope := signFaultPayload(t, fixture.key, payload)
			if test.envelope != nil {
				envelope = test.envelope(envelope)
			}
			expected := fixture.expected
			expected.PayloadDigest = sha256Digest(payload)
			expected.EnvelopeDigest = sha256Digest(envelope)
			if _, err := verifier.VerifyAt(envelope, expected, fixture.now); err == nil {
				t.Fatal("VerifyAt() accepted malformed input")
			}
		})
	}
}

func TestVerifierRejectsSelectorFenceTimeAndFixtureSubstitution(t *testing.T) {
	fixture := newSignedFaultFixture(t)
	verifier := testFaultVerifier(t, fixture.key)

	tests := []struct {
		name   string
		mutate func(*ExpectedBinding)
		now    time.Time
	}{
		{name: "selector", mutate: func(value *ExpectedBinding) { value.ResourceSelector = "release.controller" }},
		{name: "fence", mutate: func(value *ExpectedBinding) { value.ExpectedFenceDigest = testFaultDigest("other-fence") }},
		{name: "fixture", mutate: func(value *ExpectedBinding) { value.FixtureID = uuid.New() }},
		{name: "expired", now: fixture.now.Add(11 * time.Minute)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expected := fixture.expected
			if test.mutate != nil {
				test.mutate(&expected)
			}
			now := test.now
			if now.IsZero() {
				now = fixture.now
			}
			if _, err := verifier.VerifyAt(fixture.envelope, expected, now); err == nil {
				t.Fatal("VerifyAt() accepted substituted or expired authority")
			}
		})
	}
}

func TestVerifierTrustRequiresDistinctFaultOperatorIdentities(t *testing.T) {
	first := newFaultTestKey(t, "fault-key-a", "fault-a@golden.example")
	second := newFaultTestKey(t, "fault-key-b", "fault-a@golden.example")
	now := testFaultNow()
	_, err := NewVerifier(TrustPolicy{
		MinimumSignatures: 2,
		Signers: map[string]SignerTrust{
			first.keyID:  testSignerTrust(first, now),
			second.keyID: testSignerTrust(second, now),
		},
	})
	if err == nil {
		t.Fatal("NewVerifier() accepted two keys for one signer identity")
	}

	wrongRole := testSignerTrust(first, now)
	wrongRole.Role = "qualification-runner"
	if _, err := NewVerifier(TrustPolicy{
		MinimumSignatures: 1, Signers: map[string]SignerTrust{first.keyID: wrongRole},
	}); err == nil {
		t.Fatal("NewVerifier() accepted a non-fault-operator signer")
	}
}

func newSignedFaultFixture(t *testing.T) signedFaultFixture {
	t.Helper()
	now := testFaultNow()
	key := newFaultTestKey(t, "fault-key-a", "fault-a@golden.example")
	predicate := AuthorityPredicate{
		AuthorityID: uuid.NewString(), ExpectedFenceDigest: testFaultDigest("expected-fence"),
		ExpiresAt: formatCanonicalTime(now.Add(10 * time.Minute)), FixtureID: uuid.NewString(),
		IssuedAt: formatCanonicalTime(now.Add(-time.Minute)), MaxUses: 1,
		OperationKind: OperationAgentRunnerCrash, ResourceSelector: "agent.runner",
		RunID: uuid.NewString(), SchemaVersion: AuthoritySchemaV1,
	}
	payload, err := canonicalJSON(predicate)
	if err != nil {
		t.Fatal(err)
	}
	envelope := signFaultPayload(t, key, payload)
	return signedFaultFixture{
		now: now, predicate: predicate, envelope: envelope, key: key,
		expected: ExpectedBinding{
			AuthorityID: uuid.MustParse(predicate.AuthorityID), FixtureID: uuid.MustParse(predicate.FixtureID),
			RunID: uuid.MustParse(predicate.RunID), OperationKind: predicate.OperationKind,
			ResourceSelector: predicate.ResourceSelector, ExpectedFenceDigest: predicate.ExpectedFenceDigest,
			EnvelopeDigest: sha256Digest(envelope), PayloadDigest: sha256Digest(payload),
		},
	}
}

func signFaultPayload(t *testing.T, key faultTestKey, payload []byte) []byte {
	t.Helper()
	message := templateauthority.DSSEPAE(PayloadTypeV1, payload)
	signature := ed25519.Sign(key.private, message)
	encoded, err := encodeEnvelopeForTest(payload, []dsseSignature{{
		KeyID: key.keyID, Sig: base64.StdEncoding.EncodeToString(signature),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func testFaultVerifier(t *testing.T, key faultTestKey) *Verifier {
	t.Helper()
	verifier, err := NewVerifier(TrustPolicy{
		MinimumSignatures: 1,
		Signers:           map[string]SignerTrust{key.keyID: testSignerTrust(key, testFaultNow())},
	})
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func testSignerTrust(key faultTestKey, now time.Time) SignerTrust {
	return SignerTrust{
		Algorithm: templateauthority.AlgorithmEd25519, PublicKey: key.public, Identity: key.identity,
		Role: FaultOperatorRole, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
	}
}

func newFaultTestKey(t *testing.T, keyID, identity string) faultTestKey {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return faultTestKey{keyID: keyID, identity: identity, public: publicKey, private: privateKey}
}

func testFaultNow() time.Time {
	return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
}

func testFaultDigest(seed string) string { return sha256Digest([]byte(seed)) }
