package qualificationreceipt

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/goldenfault"
	"github.com/worksflow/builder/backend/internal/templateauthority"
)

type testSigningKey struct {
	keyID     string
	algorithm templateauthority.SignatureAlgorithm
	private   any
	public    any
	identity  string
}

type verificationFixture struct {
	t             *testing.T
	root          string
	indexPath     string
	receiptPath   string
	expected      ExpectedPromotion
	policy        TrustPolicy
	index         ArtifactIndex
	receipt       QualificationReceipt
	runner        testSigningKey
	approver      testSigningKey
	issuer        testSigningKey
	kms           testSigningKey
	faultOperator testSigningKey
	faultAttestor testSigningKey
	members       []CredentialSetMember
}

func TestCredentialSetMemberBindingsDigestCanonicalVector(t *testing.T) {
	members := []CredentialSetMember{
		{Slot: "api-a", ActorID: "2ada99cd-d941-4e4f-96c0-ad21b0ddcb57", Kind: "token", CredentialHandleHash: testDigest("api-a-credential")},
		{Slot: "browser-a", ActorID: "0d87efc5-006e-454c-8d1d-e32d459d0808", Kind: "storage-state", CredentialHandleHash: testDigest("browser-a-credential")},
	}
	digest, err := credentialSetMemberBindingsDigest(members)
	if err != nil {
		t.Fatal(err)
	}
	const expected = "sha256:d9f0a3dbf9240ac7010c65eff8fa43bad8614135ad954a73402121b23a61475f"
	if digest != expected {
		t.Fatalf("credential-set member digest changed: got %s want %s", digest, expected)
	}
}

func TestCredentialSetMemberKindIsClosed(t *testing.T) {
	members := []CredentialSetMember{
		{Slot: "api-a", ActorID: "2ada99cd-d941-4e4f-96c0-ad21b0ddcb57", Kind: "api-key", CredentialHandleHash: testDigest("api-a-credential")},
	}
	digest, err := credentialSetMemberBindingsDigest(members)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCredentialSetMembers(members, digest, len(members), testDigest("set-handle")); err == nil {
		t.Fatal("credential-set member kind outside token/storage-state was accepted")
	}
}

func TestGoldenAuthorityAndFixtureDocumentsFailClosed(t *testing.T) {
	fixture := newVerificationFixture(t)
	authorityDescriptor := fixture.descriptor("golden-authority-document")
	fixtureDescriptor := fixture.descriptor("golden-fixture-document")
	authorityBytes, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(authorityDescriptor.Path)))
	if err != nil {
		t.Fatal(err)
	}
	fixtureBytes, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(fixtureDescriptor.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseGoldenAuthorityDocument(authorityBytes); err != nil {
		t.Fatalf("valid Golden authority was rejected: %v", err)
	}
	if _, err := parseGoldenFixtureDocument(fixtureBytes); err != nil {
		t.Fatalf("valid Golden fixture was rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "legacy v1", mutate: func(document map[string]any) {
			document["schemaVersion"] = "worksflow-golden-fixture/v1"
		}},
		{name: "unknown secret field", mutate: func(document map[string]any) {
			document["secretBrokerResponse"] = "must-not-be-public"
		}},
		{name: "null Agent topology", mutate: func(document map[string]any) {
			document["subject"].(map[string]any)["agent"] = nil
		}},
		{name: "unknown Reference field", mutate: func(document map[string]any) {
			reference := document["subject"].(map[string]any)["reference"].(map[string]any)
			reference["secretURL"] = "https://secret.invalid"
		}},
		{name: "missing Reference commands", mutate: func(document map[string]any) {
			reference := document["subject"].(map[string]any)["reference"].(map[string]any)
			delete(reference, "commands")
		}},
		{name: "null Reference Gateway", mutate: func(document map[string]any) {
			reference := document["subject"].(map[string]any)["reference"].(map[string]any)
			reference["gateway"] = nil
		}},
		{name: "Reference deployment receipt kind drift", mutate: func(document map[string]any) {
			reference := document["subject"].(map[string]any)["reference"].(map[string]any)
			reference["deploymentReceipt"].(map[string]any)["schemaVersion"] = "reference-deployment-runtime-receipt/v2"
		}},
		{name: "Reference operation set member tamper", mutate: func(document map[string]any) {
			reference := document["subject"].(map[string]any)["reference"].(map[string]any)
			operations := reference["qualificationOperationSet"].(map[string]any)["operations"].([]any)
			operations[0] = "arbitrary-operation"
		}},
		{name: "Reference operation set hash tamper", mutate: func(document map[string]any) {
			reference := document["subject"].(map[string]any)["reference"].(map[string]any)
			reference["qualificationOperationSet"].(map[string]any)["contentHash"] = testDigest("foreign-reference-operation-set")
		}},
		{name: "Reference Gateway identity reuse", mutate: func(document map[string]any) {
			subject := document["subject"].(map[string]any)
			referenceGateway := subject["reference"].(map[string]any)["gateway"].(map[string]any)
			referenceGateway["identity"] = subject["agent"].(map[string]any)["modelGateway"].(map[string]any)["identity"]
		}},
		{name: "Reference profile reuse", mutate: func(document map[string]any) {
			subject := document["subject"].(map[string]any)
			referenceProfile := subject["reference"].(map[string]any)["gateway"].(map[string]any)["modelProfile"].(map[string]any)
			referenceProfile["id"] = subject["agent"].(map[string]any)["modelGateway"].(map[string]any)["profileId"]
		}},
		{name: "Reference provider reuse", mutate: func(document map[string]any) {
			subject := document["subject"].(map[string]any)
			referenceProfile := subject["reference"].(map[string]any)["gateway"].(map[string]any)["modelProfile"].(map[string]any)
			referenceProfile["providerId"] = subject["agent"].(map[string]any)["modelGateway"].(map[string]any)["providerId"]
		}},
		{name: "Reference command identity reuse", mutate: func(document map[string]any) {
			commands := document["subject"].(map[string]any)["reference"].(map[string]any)["commands"].(map[string]any)
			commands["web"].(map[string]any)["identity"] = commands["api"].(map[string]any)["identity"]
		}},
		{name: "Reference shell command", mutate: func(document map[string]any) {
			commands := document["subject"].(map[string]any)["reference"].(map[string]any)["commands"].(map[string]any)
			commands["api"].(map[string]any)["argv"] = []any{"/bin/sh", "-c"}
		}},
		{name: "Reference commitment reuse", mutate: func(document map[string]any) {
			gateway := document["subject"].(map[string]any)["reference"].(map[string]any)["gateway"].(map[string]any)
			gateway["capabilityDigest"] = gateway["attestationDigest"]
		}},
		{name: "Reference artifact identity reuse", mutate: func(document map[string]any) {
			reference := document["subject"].(map[string]any)["reference"].(map[string]any)
			gateway := reference["gateway"].(map[string]any)
			gateway["secretInjectionReceipt"].(map[string]any)["id"] = reference["deploymentReceipt"].(map[string]any)["id"]
		}},
		{name: "insecure platform origin", mutate: func(document map[string]any) {
			subject := document["subject"].(map[string]any)
			subject["platform"].(map[string]any)["apiOrigin"] = "http://platform-api.golden.example.test"
			subject["sandbox"].(map[string]any)["apiOrigin"] = "http://platform-api.golden.example.test"
			subject["lsp"].(map[string]any)["gateway"].(map[string]any)["apiOrigin"] = "http://platform-api.golden.example.test"
		}},
		{name: "non-canonical uppercase platform origin", mutate: func(document map[string]any) {
			subject := document["subject"].(map[string]any)
			subject["platform"].(map[string]any)["apiOrigin"] = "https://Platform-api.golden.example.test"
			subject["sandbox"].(map[string]any)["apiOrigin"] = "https://Platform-api.golden.example.test"
			subject["lsp"].(map[string]any)["gateway"].(map[string]any)["apiOrigin"] = "https://Platform-api.golden.example.test"
		}},
		{name: "non-canonical default port", mutate: func(document map[string]any) {
			subject := document["subject"].(map[string]any)
			subject["platform"].(map[string]any)["apiOrigin"] = "https://platform-api.golden.example.test:443"
			subject["sandbox"].(map[string]any)["apiOrigin"] = "https://platform-api.golden.example.test:443"
			subject["lsp"].(map[string]any)["gateway"].(map[string]any)["apiOrigin"] = "https://platform-api.golden.example.test:443"
		}},
		{name: "credential member digest drift", mutate: func(document map[string]any) {
			members := document["subject"].(map[string]any)["credentialSet"].(map[string]any)["memberBindings"].([]any)
			members[0].(map[string]any)["credentialHandleHash"] = testDigest("foreign-credential-handle")
		}},
		{name: "unknown fault operation", mutate: func(document map[string]any) {
			fault := document["subject"].(map[string]any)["faultAuthorities"].([]any)[0].(map[string]any)
			fault["operationKind"] = "arbitrary-exec"
		}},
		{name: "reused fault DSSE artifact", mutate: func(document map[string]any) {
			subject := document["subject"].(map[string]any)
			faults := subject["faultAuthorities"].([]any)
			first := faults[0].(map[string]any)
			duplicate := make(map[string]any, len(first))
			for key, value := range first {
				duplicate[key] = value
			}
			duplicate["authorityId"] = testGoldenUUID(25)
			duplicate["operationKind"] = "agent-runner-timeout"
			duplicate["resourceSelector"] = "agent.runner"
			subject["faultAuthorities"] = append(faults, duplicate)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal(fixtureBytes, &document); err != nil {
				t.Fatal(err)
			}
			test.mutate(document)
			if _, err := parseGoldenFixtureDocument(mustJSON(t, document)); err == nil {
				t.Fatal("malformed Golden fixture was accepted")
			}
		})
	}
	if _, err := parseGoldenFixtureDocument(append([]byte(" "), fixtureBytes...)); err == nil {
		t.Fatal("non-canonical Golden fixture bytes were accepted")
	}
	duplicate := []byte(`{"authorityHash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","authorityHash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","schemaVersion":"worksflow-golden-fixture/v2","subject":{}}`)
	if _, err := parseGoldenFixtureDocument(duplicate); err == nil {
		t.Fatal("duplicate Golden fixture field was accepted")
	}
	var authority map[string]any
	if err := json.Unmarshal(authorityBytes, &authority); err != nil {
		t.Fatal(err)
	}
	authority["schemaVersion"] = "worksflow-golden-authority/v1"
	if _, err := parseGoldenAuthorityDocument(mustJSON(t, authority)); err == nil {
		t.Fatal("legacy Golden authority v1 was accepted")
	}
}

func TestGoldenFaultOperationOutcomeContractIsClosed(t *testing.T) {
	expected := map[string]struct {
		selector string
		outcome  string
	}{
		"agent-runner-crash":        {"agent.runner", "applied"},
		"agent-runner-timeout":      {"agent.runner", "applied"},
		"agent-security-canary":     {"agent.patch-policy", "refused"},
		"controller-conflict":       {"release.controller", "applied"},
		"controller-maintenance":    {"release.controller", "applied"},
		"controller-not-found":      {"release.controller", "applied"},
		"controller-timeout":        {"release.controller", "applied"},
		"lsp-resource-pressure":     {"lsp.runtime", "applied"},
		"lsp-runtime-crash":         {"lsp.runtime", "applied"},
		"lsp-runtime-drift":         {"lsp.runtime", "applied"},
		"reference-gateway-outage":  {"reference.gateway", "applied"},
		"reference-process-restart": {"reference.process", "applied"},
		"sandbox-dependency-crash":  {"sandbox.dependency", "applied"},
	}
	for operation, contract := range expected {
		selector, outcome, ok := goldenFaultContract(operation)
		if !ok || selector != contract.selector || outcome != contract.outcome {
			t.Fatalf("operation %q contract = %q/%q/%t", operation, selector, outcome, ok)
		}
	}
	if _, _, ok := goldenFaultContract("arbitrary-exec"); ok {
		t.Fatal("unknown Golden fault operation was accepted")
	}
}

func TestGoldenFaultOperationSetCommitmentIsRootBoundAndExact(t *testing.T) {
	fixture := newVerificationFixture(t)
	descriptor := fixture.descriptor(fixture.expected.GoldenRuntime.FixtureDocumentArtifactID)
	encoded, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(descriptor.Path)))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseGoldenFixtureDocument(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateExpectedGoldenFaultOperationSet(parsed.faults, GoldenFaultOperationSetDigestV1); err != nil {
		t.Fatalf("exact closed operation set was rejected: %v", err)
	}
	if err := validateExpectedGoldenFaultOperationSet(parsed.faults[:len(parsed.faults)-1], GoldenFaultOperationSetDigestV1); err == nil {
		t.Fatal("Fixture-defined subset was accepted as the root-required fault-operation set")
	}
	if err := validateExpectedGoldenFaultOperationSet(parsed.faults, testDigest("foreign-operation-set")); err == nil {
		t.Fatal("foreign root operation-set digest was accepted")
	}
}

func TestGoldenReferenceOperationSetCommitmentIsCanonicalAndClosed(t *testing.T) {
	operations := goldenReferenceOperationKindsV1()
	digest, err := goldenCanonicalDigest(map[string]any{
		"operations": operations, "schemaVersion": GoldenReferenceOperationSetSchemaV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if digest != GoldenReferenceOperationSetDigestV1 {
		t.Fatalf("Reference qualification operation-set digest changed: got %s want %s", digest, GoldenReferenceOperationSetDigestV1)
	}
	expected := []string{
		"migration-rerun",
		"rate-limit-observation",
		"reference-audit-observation",
		"retention-job",
		"run-execution-observation",
		"timeout-vector",
	}
	if fmt.Sprint(operations) != fmt.Sprint(expected) {
		t.Fatalf("Reference qualification operation set changed: got %v want %v", operations, expected)
	}
}

func TestVerifierAcceptsClosedSignedQualification(t *testing.T) {
	fixture := newVerificationFixture(t)
	verifier, err := newFixtureVerifier(fixture)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected)
	if err != nil {
		t.Fatalf("verify qualification: %v", err)
	}
	if verified.RunID != fixture.expected.RunID || verified.PlanDigest != fixture.expected.PlanDigest ||
		verified.Scope != ExternalQualificationScope || verified.PromotionTarget != fixture.expected.PromotionTarget ||
		verified.AuthorityNonce != fixture.expected.AuthorityNonce || verified.SingleUseConsumption != SingleUseConsumptionPolicy ||
		verified.Decision != "qualified" || verified.GoldenRuntime != fixture.expected.GoldenRuntime ||
		verified.CredentialSet.SetHandleHash != fixture.expected.CredentialSet.SetHandleHash ||
		verified.CredentialSet.MemberBindingsDigest != fixture.expected.CredentialSet.MemberBindingsDigest ||
		verified.CredentialSet.MemberCount != fixture.expected.CredentialSet.MemberCount ||
		len(verified.SignerIdentities) != 2 || len(verified.CredentialIssuanceSignerIdentities) != 1 ||
		len(verified.CredentialRevocationSignerIdentities) != 1 ||
		len(verified.EncryptionSignerIdentities) != 1 ||
		len(verified.FaultAuthoritySignerIdentities) != 1 ||
		verified.FaultLedgerAttestationDigest != fixture.descriptor(GoldenFaultLedgerArtifactID).SHA256 ||
		len(verified.FaultLedgerAttestorSignerIdentities) != 1 {
		t.Fatalf("unexpected verified promotion: %+v", verified)
	}
}

func TestVerifierUsesAttestedHistoricalFaultReservationTime(t *testing.T) {
	fixture := newVerificationFixture(t)
	if !fixture.expected.VerifiedAt.After(mustCanonicalTime(t, "2026-07-18T12:03:30.000Z")) {
		t.Fatal("test fixture must verify after the short-lived fault authority expired")
	}
	verifier, err := newFixtureVerifier(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err != nil {
		t.Fatalf("historically valid attested reservation was rejected after authority expiry: %v", err)
	}
}

func TestVerifierRequiresFaultLedgerAttestorKeyThroughTrustedReceiptIssuance(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AuthorityKeyValidity)
	}{
		{
			name: "revoked after ledger but before receipt",
			mutate: func(validity *AuthorityKeyValidity) {
				revokedAt := mustCanonicalTime(t, "2026-07-18T12:06:00.000Z")
				validity.RevokedAt = &revokedAt
			},
		},
		{
			name: "expires exactly at receipt issuance",
			mutate: func(validity *AuthorityKeyValidity) {
				validity.NotAfter = mustCanonicalTime(t, "2026-07-18T12:07:00.000Z")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVerificationFixture(t)
			validity := fixture.policy.FaultLedgerAttestor.KeyValidity[fixture.faultAttestor.keyID]
			test.mutate(&validity)
			fixture.policy.FaultLedgerAttestor.KeyValidity[fixture.faultAttestor.keyID] = validity
			verifier, err := newFixtureVerifier(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err == nil ||
				!strings.Contains(err.Error(), "at trusted receipt issuance") {
				t.Fatalf("attestor key invalid before trusted Receipt issuance was not rejected at the intended boundary: %v", err)
			}
		})
	}
}

func TestVerifierRejectsGoldenFaultLedgerDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*GoldenFaultLedgerAttestation)
	}{
		{"reserved unknown", func(value *GoldenFaultLedgerAttestation) { value.Entries[0].State = "reserved" }},
		{"wrong refused outcome", func(value *GoldenFaultLedgerAttestation) { value.Entries[0].Outcome = "refused" }},
		{"authority expired at reservation", func(value *GoldenFaultLedgerAttestation) { value.Entries[0].ReservedAt = "2026-07-18T12:03:30.000Z" }},
		{"unknown operation", func(value *GoldenFaultLedgerAttestation) { value.Entries[0].OperationKind = "arbitrary-exec" }},
		{"authority digest drift", func(value *GoldenFaultLedgerAttestation) {
			value.Entries[0].AuthorityDigest = testDigest("drift-authority")
		}},
		{"envelope digest drift", func(value *GoldenFaultLedgerAttestation) {
			value.Entries[0].EnvelopeDigest = testDigest("drift-envelope")
		}},
		{"payload digest drift", func(value *GoldenFaultLedgerAttestation) {
			value.Entries[0].PayloadDigest = testDigest("drift-payload")
		}},
		{"reservation digest drift", func(value *GoldenFaultLedgerAttestation) {
			value.Entries[0].ReservationDigest = testDigest("drift-reservation")
		}},
		{"result digest drift", func(value *GoldenFaultLedgerAttestation) { value.Entries[0].ResultDigest = testDigest("drift-result") }},
		{"receipt digest drift", func(value *GoldenFaultLedgerAttestation) {
			value.Entries[0].ReceiptDigest = testDigest("drift-receipt")
		}},
		{"result receipt identity drift", func(value *GoldenFaultLedgerAttestation) { value.Entries[0].ReceiptArtifactID = testGoldenUUID(24) }},
		{"completed time drift", func(value *GoldenFaultLedgerAttestation) { value.Entries[0].CompletedAt = "2026-07-18T12:02:31.000Z" }},
		{"attested before run completion", func(value *GoldenFaultLedgerAttestation) { value.IssuedAt = "2026-07-18T12:04:59.000Z" }},
		{"attested at run completion", func(value *GoldenFaultLedgerAttestation) { value.IssuedAt = "2026-07-18T12:05:00.000Z" }},
		{"attested at receipt issuance", func(value *GoldenFaultLedgerAttestation) { value.IssuedAt = "2026-07-18T12:07:00.000Z" }},
		{"duplicate authority", func(value *GoldenFaultLedgerAttestation) { value.Entries = append(value.Entries, value.Entries[0]) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVerificationFixture(t)
			ledger := fixture.readFaultLedger()
			test.mutate(&ledger)
			fixture.writeFaultLedger(ledger, fixture.faultAttestor)
			fixture.reseal()
			verifier, err := newFixtureVerifier(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err == nil {
				t.Fatal("drifted Golden fault ledger evidence was accepted")
			}
		})
	}
}

func TestVerifierRejectsGoldenFaultArtifactCardinalityAndSignature(t *testing.T) {
	t.Run("missing authority", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		fixture.removeArtifact(testGoldenUUID(21))
		fixture.reseal()
		assertFixtureVerificationFails(t, fixture)
	})
	t.Run("missing consume receipt", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		fixture.removeArtifact(testGoldenUUID(22))
		fixture.reseal()
		assertFixtureVerificationFails(t, fixture)
	})
	t.Run("extra consume receipt", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		original := fixture.descriptor(testGoldenUUID(22))
		content, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(original.Path)))
		if err != nil {
			t.Fatal(err)
		}
		extraID := testGoldenUUID(24)
		extra := fixture.newArtifact(extraID, "evidence/fault-receipts/"+extraID+".json", ArtifactTypeGoldenFaultReceipt, CanonicalJSONMediaType, content, false)
		fixture.index.Artifacts = append(fixture.index.Artifacts[:2], append([]ArtifactDescriptor{extra}, fixture.index.Artifacts[2:]...)...)
		fixture.writeArtifactBytes(extra, content)
		fixture.reseal()
		assertFixtureVerificationFails(t, fixture)
	})
	t.Run("attestation signed by qualification runner", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		fixture.writeFaultLedger(fixture.readFaultLedger(), fixture.runner)
		fixture.reseal()
		assertFixtureVerificationFails(t, fixture)
	})
	t.Run("non-canonical attestation envelope", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		descriptor := fixture.descriptor(GoldenFaultLedgerArtifactID)
		encoded, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(descriptor.Path)))
		if err != nil {
			t.Fatal(err)
		}
		fixture.writeArtifact(GoldenFaultLedgerArtifactID, append([]byte(" "), encoded...))
		fixture.reseal()
		assertFixtureVerificationFails(t, fixture)
	})
	t.Run("authority media type drift", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		for index := range fixture.index.Artifacts {
			if fixture.index.Artifacts[index].ID == testGoldenUUID(21) {
				fixture.index.Artifacts[index].MediaType = CanonicalJSONMediaType
			}
		}
		fixture.reseal()
		assertFixtureVerificationFails(t, fixture)
	})
	t.Run("plain receipt bytes drift", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		descriptor := fixture.descriptor(testGoldenUUID(22))
		encoded, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(descriptor.Path)))
		if err != nil {
			t.Fatal(err)
		}
		var consume goldenfault.ConsumeReceipt
		if err := json.Unmarshal(encoded, &consume); err != nil {
			t.Fatal(err)
		}
		consume.ObservedHeadDigest = testDigest("receipt-drift")
		fixture.writeArtifact(testGoldenUUID(22), mustJSON(t, consume))
		fixture.reseal()
		assertFixtureVerificationFails(t, fixture)
	})
}

func TestVerifierRejectsGoldenFaultAdapterInvocationReuse(t *testing.T) {
	fixture := newVerificationFixture(t)
	ledger := fixture.readFaultLedger()
	if len(ledger.Entries) != len(goldenFaultOperationKindsV1()) {
		t.Fatalf("fixture has %d fault entries", len(ledger.Entries))
	}
	firstReceipt := fixture.readFaultConsumeReceipt(ledger.Entries[0].ReceiptArtifactID)
	secondEntry := &ledger.Entries[1]
	secondReceipt := fixture.readFaultConsumeReceipt(secondEntry.ReceiptArtifactID)
	secondReceipt.AdapterInvocationID = firstReceipt.AdapterInvocationID
	mutated := mustJSON(t, secondReceipt)
	verifiedAuthority := fixture.verifiedFaultAuthority(secondEntry.AuthorityID)
	reservation, terminal, err := goldenfault.ValidateConsumeReceiptEvidence(verifiedAuthority, mutated)
	if err != nil {
		t.Fatalf("build individually valid reused adapter invocation: %v", err)
	}
	reservationDigest, err := goldenfault.ReservationEvidenceDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	resultDigest, err := goldenfault.TerminalEvidenceDigest(terminal)
	if err != nil {
		t.Fatal(err)
	}
	secondEntry.ReceiptDigest = testDigestFromBytes(mutated)
	secondEntry.ReservationDigest = reservationDigest
	secondEntry.ResultDigest = resultDigest
	fixture.writeArtifact(secondEntry.ReceiptArtifactID, mutated)
	fixture.writeFaultLedger(ledger, fixture.faultAttestor)
	fixture.reseal()
	verifier, err := newFixtureVerifier(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err == nil ||
		!strings.Contains(err.Error(), "adapter invocation is reused") {
		t.Fatalf("run-level adapter invocation reuse was not rejected at the intended boundary: %v", err)
	}
}

func TestVerifierRejectsNonCanonicalOrNonExactGoldenFaultConsumeReceipts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"leading whitespace", func(encoded []byte) []byte { return append([]byte(" "), encoded...) }},
		{"trailing JSON value", func(encoded []byte) []byte { return append(encoded, []byte(`{}`)...) }},
		{"UTF-8 BOM", func(encoded []byte) []byte { return append([]byte{0xef, 0xbb, 0xbf}, encoded...) }},
		{"duplicate null field", func(encoded []byte) []byte {
			return append([]byte(`{"adapterInvocationId":null,`), encoded[1:]...)
		}},
		{"unknown field", func(encoded []byte) []byte {
			return append([]byte(`{"unexpected":"value",`), encoded[1:]...)
		}},
		{"missing field", func(encoded []byte) []byte {
			var document map[string]any
			if err := json.Unmarshal(encoded, &document); err != nil {
				t.Fatal(err)
			}
			delete(document, "observedHeadDigest")
			return mustJSON(t, document)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVerificationFixture(t)
			receiptID := testGoldenUUID(22)
			descriptor := fixture.descriptor(receiptID)
			encoded, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(descriptor.Path)))
			if err != nil {
				t.Fatal(err)
			}
			mutated := test.mutate(encoded)
			fixture.writeArtifact(receiptID, mutated)
			ledger := fixture.readFaultLedger()
			ledger.Entries[0].ReceiptDigest = testDigestFromBytes(mutated)
			fixture.writeFaultLedger(ledger, fixture.faultAttestor)
			fixture.reseal()
			assertFixtureVerificationFails(t, fixture)
		})
	}
}

func TestVerifierAcceptsCredentialSetIssuedBeforePromotionAuthority(t *testing.T) {
	fixture := newVerificationFixture(t)
	fixture.expected.AuthorityIssuedAt = "2026-07-18T12:06:30.000Z"
	verifier, err := newFixtureVerifier(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err != nil {
		t.Fatalf("credential set must be issued during the Golden run before the later promotion authority is formed: %v", err)
	}
}

func TestVerifierCopiesHigherLevelTrustPolicy(t *testing.T) {
	fixture := newVerificationFixture(t)
	verifier, err := newFixtureVerifier(fixture)
	if err != nil {
		t.Fatal(err)
	}
	runner := fixture.policy.Signers[fixture.runner.keyID]
	runner.Role = SignerRoleApprover
	runner.NotAfter = time.Date(2026, 7, 18, 12, 6, 0, 0, time.UTC)
	fixture.policy.Signers[fixture.runner.keyID] = runner
	fixture.policy.EncryptionRecipients[0] = EncryptionRecipient{KeyResource: "kms://attacker", KeyVersion: "changed"}
	if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err != nil {
		t.Fatalf("caller mutation changed configured verifier policy: %v", err)
	}
}

func TestVerifierRequiresActualReadOnlySnapshotMount(t *testing.T) {
	fixture := newVerificationFixture(t)
	verifier, err := NewVerifier(fixture.policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err == nil ||
		!strings.Contains(err.Error(), "snapshot filesystem is writable") {
		t.Fatalf("writable artifact root was not rejected by the production snapshot inspector: %v", err)
	}
}

func TestVerifierRejectsArtifactFilesystemAndHashEscapes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*verificationFixture)
	}{
		{name: "tampered bytes", mutate: func(f *verificationFixture) {
			f.writeArtifact("browser-video", []byte("different ciphertext"))
		}},
		{name: "unlisted file", mutate: func(f *verificationFixture) {
			if err := os.WriteFile(filepath.Join(f.root, "unlisted.txt"), []byte("unexpected"), 0o600); err != nil {
				f.t.Fatal(err)
			}
		}},
		{name: "plaintext sibling outside artifact root", mutate: func(f *verificationFixture) {
			if err := os.WriteFile(filepath.Join(f.expected.EvidenceSnapshotRoot, "plaintext-token.log"), []byte("secret"), 0o600); err != nil {
				f.t.Fatal(err)
			}
		}},
		{name: "symlink sibling outside artifact root", mutate: func(f *verificationFixture) {
			if err := os.Symlink(f.receiptPath, filepath.Join(f.expected.EvidenceSnapshotRoot, "receipt-alias")); err != nil {
				f.t.Fatal(err)
			}
		}},
		{name: "excessive empty directories", mutate: func(f *verificationFixture) {
			for index := 0; index <= maxEvidenceSnapshotDirectories; index++ {
				if err := os.Mkdir(filepath.Join(f.expected.EvidenceSnapshotRoot, fmt.Sprintf("empty-%03d", index)), 0o700); err != nil {
					f.t.Fatal(err)
				}
			}
		}},
		{name: "symlink", mutate: func(f *verificationFixture) {
			if err := os.Symlink(filepath.Join(f.root, f.descriptor("browser-video").Path), filepath.Join(f.root, "linked")); err != nil {
				f.t.Fatal(err)
			}
		}},
		{name: "hardlink", mutate: func(f *verificationFixture) {
			if err := os.Link(filepath.Join(f.root, f.descriptor("browser-video").Path), filepath.Join(f.t.TempDir(), "alias")); err != nil {
				f.t.Fatal(err)
			}
		}},
		{name: "missing listed file", mutate: func(f *verificationFixture) {
			if err := os.Remove(filepath.Join(f.root, f.descriptor("browser-video").Path)); err != nil {
				f.t.Fatal(err)
			}
		}},
		{name: "legacy three-field artifact index source", mutate: func(f *verificationFixture) {
			var document map[string]any
			encoded, err := os.ReadFile(f.indexPath)
			if err != nil || json.Unmarshal(encoded, &document) != nil {
				f.t.Fatal("read artifact index")
			}
			delete(document["source"].(map[string]any), "treeDigestSchema")
			if err := os.WriteFile(f.indexPath, mustJSON(f.t, document), 0o600); err != nil {
				f.t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVerificationFixture(t)
			test.mutate(fixture)
			verifier, _ := newFixtureVerifier(fixture)
			if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err == nil {
				t.Fatal("expected artifact closure verification to fail")
			}
		})
	}
}

func TestVerifierRejectsSymlinkedEvidenceRootComponent(t *testing.T) {
	fixture := newVerificationFixture(t)
	alias := filepath.Join(t.TempDir(), "evidence-alias")
	if err := os.Symlink(fixture.expected.EvidenceSnapshotRoot, alias); err != nil {
		t.Fatal(err)
	}
	fixture.expected.EvidenceSnapshotRoot = alias
	fixture.expected.ArtifactRoot = filepath.Join(alias, "artifacts")
	fixture.root = fixture.expected.ArtifactRoot
	fixture.indexPath = filepath.Join(alias, "artifact-index.json")
	fixture.receiptPath = filepath.Join(alias, "qualification.dsse.json")
	verifier, _ := newFixtureVerifier(fixture)
	if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked evidence root was accepted: %v", err)
	}
}

func TestVerifierRejectsQualificationSemanticTampering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*verificationFixture)
	}{
		{name: "plan mismatch", mutate: func(f *verificationFixture) {
			f.receipt.PlanDigest = testDigest("wrong-plan")
			f.writeReceipt(f.runner, f.approver)
		}},
		{name: "promotion target mismatch", mutate: func(f *verificationFixture) {
			f.receipt.PromotionTarget.Subject = "other-subject"
			f.writeReceipt(f.runner, f.approver)
		}},
		{name: "promotion workflow run mismatch", mutate: func(f *verificationFixture) {
			f.receipt.PromotionTarget.WorkflowRunID = "17516a9d-3cf1-43a9-b777-0422d8fd9e47"
			f.writeReceipt(f.runner, f.approver)
		}},
		{name: "promotion target revision mismatch", mutate: func(f *verificationFixture) {
			f.receipt.PromotionTarget.TargetRevision.ContentHash = testDigest("other-target-revision")
			f.writeReceipt(f.runner, f.approver)
		}},
		{name: "authority nonce mismatch", mutate: func(f *verificationFixture) {
			f.receipt.AuthorityNonce = "37baf81c-c3be-40d8-9af7-4427d8a8167c"
			f.writeReceipt(f.runner, f.approver)
		}},
		{name: "TemplateRelease mismatch", mutate: func(f *verificationFixture) {
			f.receipt.TemplateRelease.ContentHash = testDigest("other-template")
			f.writeReceipt(f.runner, f.approver)
		}},
		{name: "legacy v1 receipt schema", mutate: func(f *verificationFixture) {
			f.receipt.SchemaVersion = "worksflow-qualification-receipt/v1"
			f.writeReceipt(f.runner, f.approver)
		}},
		{name: "Golden fixture id mismatch", mutate: func(f *verificationFixture) {
			f.receipt.GoldenRuntime.FixtureID = "a32e7d0f-4c1e-411a-8524-a3e1ea1bb772"
			f.writeReceipt(f.runner, f.approver)
		}},
		{name: "Golden fixture document bytes mismatch", mutate: func(f *verificationFixture) {
			f.writeArtifact("golden-fixture-document", []byte(`{"schemaVersion":"worksflow-golden-fixture/v2","subject":{}}`))
			f.reseal()
		}},
		{name: "credential set root binding mismatch", mutate: func(f *verificationFixture) {
			f.expected.CredentialSet.SetHandleHash = testDigest("other-set-handle")
		}},
		{name: "skip total", mutate: func(f *verificationFixture) {
			f.receipt.Totals.Skipped = 1
			f.writeReceipt(f.runner, f.approver)
		}},
		{name: "active v6 writer", mutate: func(f *verificationFixture) {
			f.receipt.Constructor.WriterDrain.ActiveWriters = 1
			f.writeReceipt(f.runner, f.approver)
		}},
		{name: "credential set revoked before completion", mutate: func(f *verificationFixture) {
			f.receipt.CredentialSet.RevokedAt = "2026-07-18T12:04:00.000Z"
			f.rewriteCredentialSetEvidence()
			f.reseal()
		}},
		{name: "credential set revoked exactly at completion", mutate: func(f *verificationFixture) {
			f.receipt.CredentialSet.RevokedAt = f.receipt.CompletedAt
			f.rewriteCredentialSetEvidence()
			f.reseal()
		}},
		{name: "credential set revoked before KMS attestation", mutate: func(f *verificationFixture) {
			f.receipt.CredentialSet.RevokedAt = "2026-07-18T12:05:40.000Z"
			f.rewriteCredentialSetEvidence()
			f.reseal()
		}},
		{name: "credential set issued exactly at run start", mutate: func(f *verificationFixture) {
			f.receipt.CredentialSet.IssuedAt = f.receipt.StartedAt
			f.rewriteCredentialSetEvidence()
			f.reseal()
		}},
		{name: "credential set issued before writer drain", mutate: func(f *verificationFixture) {
			f.receipt.CredentialSet.IssuedAt = "2026-07-18T11:58:59.000Z"
			f.rewriteCredentialSetEvidence()
			f.reseal()
		}},
		{name: "credential set expiry does not cover run", mutate: func(f *verificationFixture) {
			f.receipt.CredentialSet.ExpiresAt = f.receipt.CompletedAt
			f.rewriteCredentialSetEvidence()
			f.reseal()
		}},
		{name: "credential set lifetime exceeds thirty minutes", mutate: func(f *verificationFixture) {
			f.receipt.CredentialSet.ExpiresAt = "2026-07-18T12:30:30.001Z"
			f.rewriteCredentialSetEvidence()
			f.reseal()
		}},
		{name: "credential set revoked exactly at receipt issuance", mutate: func(f *verificationFixture) {
			f.receipt.CredentialSet.RevokedAt = f.receipt.IssuedAt
			f.rewriteCredentialSetEvidence()
			f.reseal()
		}},
		{name: "credential set duplicate handle", mutate: func(f *verificationFixture) {
			f.members[1].CredentialHandleHash = f.members[0].CredentialHandleHash
			digest, err := credentialSetMemberBindingsDigest(f.members)
			if err != nil {
				f.t.Fatal(err)
			}
			f.expected.CredentialSet.MemberBindingsDigest = digest
			f.receipt.CredentialSet.MemberBindingsDigest = digest
			f.rewriteCredentialSetEvidence()
			f.reseal()
		}},
		{name: "credential set members not sorted", mutate: func(f *verificationFixture) {
			f.members[0], f.members[1] = f.members[1], f.members[0]
			digest, err := credentialSetMemberBindingsDigest(f.members)
			if err != nil {
				f.t.Fatal(err)
			}
			f.expected.CredentialSet.MemberBindingsDigest = digest
			f.receipt.CredentialSet.MemberBindingsDigest = digest
			f.rewriteCredentialSetEvidence()
			f.reseal()
		}},
		{name: "credential set revocation changes issued member list", mutate: func(f *verificationFixture) {
			changed := append([]CredentialSetMember(nil), f.members...)
			changed[1].ActorID = "656d83bd-75f9-4e9e-8d5d-c4fe6dd4f3f1"
			f.rewriteCredentialSetRevocationMembers(changed)
			f.reseal()
		}},
		{name: "receipt issued exactly at completion", mutate: func(f *verificationFixture) {
			f.receipt.IssuedAt = f.receipt.CompletedAt
			f.expected.TrustedReceiptIssuedAt = f.receipt.CompletedAt
			f.writeReceipt(f.runner, f.approver)
		}},
		{name: "receipt time differs from trusted authority", mutate: func(f *verificationFixture) {
			f.expected.TrustedReceiptIssuedAt = "2026-07-18T12:07:01.000Z"
		}},
		{name: "missing approver signature", mutate: func(f *verificationFixture) {
			f.writeReceipt(f.runner)
		}},
		{name: "unknown receipt field", mutate: func(f *verificationFixture) {
			var predicate map[string]any
			if err := json.Unmarshal(mustJSON(f.t, f.receipt), &predicate); err != nil {
				f.t.Fatal(err)
			}
			predicate["unreviewedAuthority"] = true
			f.writeReceiptValue(predicate, f.runner, f.approver)
		}},
		{name: "legacy three-field signed receipt source", mutate: func(f *verificationFixture) {
			var predicate map[string]any
			if err := json.Unmarshal(mustJSON(f.t, f.receipt), &predicate); err != nil {
				f.t.Fatal(err)
			}
			delete(predicate["source"].(map[string]any), "treeDigestSchema")
			f.writeReceiptValue(predicate, f.runner, f.approver)
		}},
		{name: "Playwright contract criterion mismatch", mutate: func(f *verificationFixture) {
			descriptor := f.descriptor("zero-mock-zero-skip-playwright-json")
			encoded, _ := os.ReadFile(filepath.Join(f.root, descriptor.Path))
			var result PlaywrightQualificationResult
			if err := json.Unmarshal(encoded, &result); err != nil {
				f.t.Fatal(err)
			}
			result.Tests[0].ContractCriterionIDs = []string{"AC-GOLDEN-999"}
			f.writeArtifact(descriptor.ID, mustJSON(f.t, result))
			f.reseal()
		}},
		{name: "omitted Playwright contract criteria field", mutate: func(f *verificationFixture) {
			descriptor := f.descriptor("zero-mock-zero-skip-playwright-json")
			encoded, _ := os.ReadFile(filepath.Join(f.root, descriptor.Path))
			var result map[string]any
			if err := json.Unmarshal(encoded, &result); err != nil {
				f.t.Fatal(err)
			}
			delete(result["tests"].([]any)[0].(map[string]any), "contractCriterionIds")
			f.writeArtifact(descriptor.ID, mustJSON(f.t, result))
			f.reseal()
		}},
		{name: "mocked Playwright case", mutate: func(f *verificationFixture) {
			descriptor := f.descriptor("zero-mock-zero-skip-playwright-json")
			encoded, _ := os.ReadFile(filepath.Join(f.root, descriptor.Path))
			var result PlaywrightQualificationResult
			if err := json.Unmarshal(encoded, &result); err != nil {
				f.t.Fatal(err)
			}
			result.Tests[0].Mocked = true
			result.Totals.Mocked = 1
			f.writeArtifact(descriptor.ID, mustJSON(f.t, result))
			f.reseal()
		}},
		{name: "omitted zero-valued Playwright mock field", mutate: func(f *verificationFixture) {
			descriptor := f.descriptor("zero-mock-zero-skip-playwright-json")
			encoded, _ := os.ReadFile(filepath.Join(f.root, descriptor.Path))
			var result map[string]any
			if err := json.Unmarshal(encoded, &result); err != nil {
				f.t.Fatal(err)
			}
			delete(result["tests"].([]any)[0].(map[string]any), "mocked")
			f.writeArtifact(descriptor.ID, mustJSON(f.t, result))
			f.reseal()
		}},
		{name: "null Playwright mock field", mutate: func(f *verificationFixture) {
			descriptor := f.descriptor("zero-mock-zero-skip-playwright-json")
			encoded, _ := os.ReadFile(filepath.Join(f.root, descriptor.Path))
			var result map[string]any
			if err := json.Unmarshal(encoded, &result); err != nil {
				f.t.Fatal(err)
			}
			result["tests"].([]any)[0].(map[string]any)["mocked"] = nil
			f.writeArtifact(descriptor.ID, mustJSON(f.t, result))
			f.reseal()
		}},
		{name: "plaintext trace", mutate: func(f *verificationFixture) {
			for index := range f.index.Artifacts {
				if f.index.Artifacts[index].ID == "credential-safe-trace" {
					f.index.Artifacts[index].Classification = ClassificationDistributable
					f.index.Artifacts[index].Encryption = nil
				}
			}
			f.reseal()
		}},
		{name: "distributable generic evidence", mutate: func(f *verificationFixture) {
			content := []byte("service log that was not independently proven secret-free")
			f.index.Artifacts = append(f.index.Artifacts, ArtifactDescriptor{})
			copy(f.index.Artifacts[4:], f.index.Artifacts[3:])
			f.index.Artifacts[3] = f.newArtifact("generic-service-log", "evidence/service.log", ArtifactTypeEvidence, "text/plain", content, false)
			f.writeArtifact("generic-service-log", content)
			f.reseal()
		}},
		{name: "untrusted encryption recipient", mutate: func(f *verificationFixture) {
			for index := range f.index.Artifacts {
				if f.index.Artifacts[index].ID == "credential-safe-trace" {
					f.index.Artifacts[index].Encryption.KeyResource = "kms://attacker/evidence"
				}
			}
			f.reseal()
		}},
		{name: "KMS-unattested AES-GCM nonce", mutate: func(f *verificationFixture) {
			for index := range f.index.Artifacts {
				if f.index.Artifacts[index].ID == "credential-safe-trace" {
					f.index.Artifacts[index].Encryption.Nonce = base64.StdEncoding.EncodeToString([]byte("changednonce"))
				}
			}
			f.reseal()
		}},
		{name: "missing required artifact", mutate: func(f *verificationFixture) {
			descriptor := f.descriptor("browser-video")
			if err := os.Remove(filepath.Join(f.root, descriptor.Path)); err != nil {
				f.t.Fatal(err)
			}
			filtered := make([]ArtifactDescriptor, 0, len(f.index.Artifacts)-1)
			for _, artifact := range f.index.Artifacts {
				if artifact.ID != descriptor.ID {
					filtered = append(filtered, artifact)
				}
			}
			f.index.Artifacts = filtered
			f.reseal()
		}},
		{name: "forged credential-set issuance envelope", mutate: func(f *verificationFixture) {
			f.writeArtifact("credential-set-issuance-receipt", []byte(`{"payloadType":"bad"}`))
			f.reseal()
		}},
		{name: "forged credential-set revocation envelope", mutate: func(f *verificationFixture) {
			f.writeArtifact("credential-set-revocation-receipt", []byte(`{"payloadType":"bad"}`))
			f.reseal()
		}},
		{name: "forged KMS encryption attestation", mutate: func(f *verificationFixture) {
			f.writeArtifact(EncryptionAttestationArtifactID, []byte(`{"payloadType":"bad"}`))
			f.reseal()
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVerificationFixture(t)
			test.mutate(fixture)
			verifier, _ := newFixtureVerifier(fixture)
			if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err == nil {
				t.Fatal("expected semantic qualification tampering to fail")
			}
		})
	}
}

func TestVerifierRejectsSignerRoleValidityAndStrictJSONFailures(t *testing.T) {
	t.Run("same identity across roles", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		approver := fixture.policy.Signers[fixture.approver.keyID]
		approver.Identity = fixture.runner.identity
		fixture.policy.Signers[fixture.approver.keyID] = approver
		if _, err := NewVerifier(fixture.policy); err == nil {
			t.Fatal("expected cross-role identity reuse to fail")
		}
	})
	t.Run("same public key across roles", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		approver := fixture.policy.Signers[fixture.approver.keyID]
		approver.PublicKey = fixture.runner.public
		fixture.policy.Signers[fixture.approver.keyID] = approver
		if _, err := NewVerifier(fixture.policy); err == nil {
			t.Fatal("expected cross-role public key reuse to fail")
		}
	})
	t.Run("same public key across receipt and credential authorities", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		issuer := fixture.policy.CredentialIssuers[fixture.expected.CredentialSet.Issuer]
		issuer.Keys[fixture.issuer.keyID] = templateauthority.TrustedSigner{
			Algorithm: fixture.runner.algorithm, PublicKey: fixture.runner.public, Identity: fixture.issuer.identity,
		}
		fixture.policy.CredentialIssuers[fixture.expected.CredentialSet.Issuer] = issuer
		if _, err := NewVerifier(fixture.policy); err == nil {
			t.Fatal("expected receipt and credential authority public key reuse to fail")
		}
	})
	t.Run("expired signer", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		runner := fixture.policy.Signers[fixture.runner.keyID]
		runner.NotAfter = time.Date(2026, 7, 18, 12, 6, 0, 0, time.UTC)
		fixture.policy.Signers[fixture.runner.keyID] = runner
		verifier, err := newFixtureVerifier(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err == nil {
			t.Fatal("expected expired signer to fail")
		}
	})
	t.Run("credential issuer key not valid at issuance", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		issuer := fixture.policy.CredentialIssuers[fixture.expected.CredentialSet.Issuer]
		validity := issuer.KeyValidity[fixture.issuer.keyID]
		validity.NotBefore = time.Date(2026, 7, 18, 12, 0, 31, 0, time.UTC)
		issuer.KeyValidity[fixture.issuer.keyID] = validity
		fixture.policy.CredentialIssuers[fixture.expected.CredentialSet.Issuer] = issuer
		verifier, err := newFixtureVerifier(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err == nil {
			t.Fatal("credential-set issuance signed before issuer-key validity was accepted")
		}
	})
	t.Run("credential issuer key not valid at revocation", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		issuer := fixture.policy.CredentialIssuers[fixture.expected.CredentialSet.Issuer]
		validity := issuer.KeyValidity[fixture.issuer.keyID]
		validity.NotAfter = time.Date(2026, 7, 18, 12, 6, 0, 0, time.UTC)
		issuer.KeyValidity[fixture.issuer.keyID] = validity
		fixture.policy.CredentialIssuers[fixture.expected.CredentialSet.Issuer] = issuer
		verifier, err := newFixtureVerifier(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err == nil {
			t.Fatal("credential-set revocation signed at issuer-key expiry was accepted")
		}
	})
	t.Run("legacy v1 receipt predicate type", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		indexBytes, err := os.ReadFile(fixture.indexPath)
		if err != nil {
			t.Fatal(err)
		}
		payload := inTotoPayload(t, "https://worksflow.dev/attestations/qualification-receipt/v1",
			"worksflow-qualification-artifacts/"+fixture.expected.RunID, testDigestFromBytes(indexBytes), fixture.receipt)
		bundle := signDSSE(t, payload, fixture.runner, fixture.approver)
		if err := os.WriteFile(fixture.receiptPath, bundle, 0o600); err != nil {
			t.Fatal(err)
		}
		fixture.expected.ReceiptBundleDigest = testDigestFromBytes(bundle)
		verifier, _ := newFixtureVerifier(fixture)
		if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err == nil {
			t.Fatal("legacy v1 qualification predicate type was accepted")
		}
	})
	t.Run("duplicate JSON name", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		if err := os.WriteFile(fixture.indexPath, []byte(`{"schemaVersion":"`+ArtifactIndexSchemaV1+`","schemaVersion":"`+ArtifactIndexSchemaV1+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		verifier, _ := newFixtureVerifier(fixture)
		if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err == nil {
			t.Fatal("expected duplicate JSON name to fail")
		}
	})
}

func newVerificationFixture(t *testing.T) *verificationFixture {
	t.Helper()
	runner := newTestSigningKey(t, "runner-key", "runner@qualification.example")
	approver := newTestSigningKey(t, "approver-key", "approver@release.example")
	issuer := newTestSigningKey(t, "issuer-key", "issuer@credentials.example")
	kms := newTestSigningKey(t, "kms-key", "kms@qualification.example")
	faultOperator := newTestSigningKey(t, "fault-key", "fault-operator@qualification.example")
	faultAttestor := newTestSigningKey(t, "fault-ledger-key", "fault-ledger-attestor@qualification.example")
	now := time.Date(2026, 7, 18, 12, 8, 0, 0, time.UTC)
	evidenceRoot := t.TempDir()
	artifactRoot := filepath.Join(evidenceRoot, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	expected := ExpectedPromotion{
		PromotionTarget: PromotionTarget{
			ProjectID: "4a4554f6-f270-4ce7-9323-78510af0f91a", WorkflowRunID: "d0118815-af3f-491f-9e26-bf43684a5263",
			NodeKey: "external-qualification", TargetRevision: PromotionTargetRevision{
				ID: "f87f0f42-3a01-4c60-bcb9-781da473b945", ContentHash: testDigest("target-revision"),
			}, Subject: "ai-constructor", StageGate: ExternalQualificationGate,
		},
		AuthorityNonce: "88172ca8-8507-4a7a-bf86-f89f58ac859d", AuthorityIssuedAt: "2026-07-18T12:00:00.000Z",
		AuthorityExpiresAt:       "2026-07-18T12:10:00.000Z",
		PromotionAuthorityDigest: testDigest("promotion-authority"),
		RunID:                    "d5d9a265-d09d-40e7-b643-f2a48698dc9b", PlanDigest: testDigest("plan"),
		PrePromotionManifestDigest: testDigest("pre-promotion-manifest"),
		Source: SourceBinding{
			Commit: strings.Repeat("a", 40), TreeDigestSchema: SourceContentTreeCommitmentSchemaV1, TreeDigest: testDigest("tree"), Dirty: false,
		},
		TemplateRelease: TemplateReleaseBinding{
			ID: "173fdbba-3898-43ba-9201-5fbd574948f0", ContentHash: testDigest("template"),
			ApprovalReceiptDigest: testDigest("template-approval"),
		},
		GoldenRuntime: GoldenRuntimeBinding{
			AuthorityDocumentArtifactID: "golden-authority-document", AuthorityDocumentDigest: testDigest("golden-authority-document-pending"),
			FaultOperationSetDigest:   GoldenFaultOperationSetDigestV1,
			FixtureDocumentArtifactID: "golden-fixture-document", FixtureDocumentDigest: testDigest("golden-fixture-document-pending"),
			FixtureID: "3a2c0dcf-f663-48fb-8fb3-88d310a3ff81",
		},
		BuildContractHash: testDigest("build-contract"), WriterDrainEvidenceArtifactID: "v7-writer-drain-proof",
		CredentialSet: CredentialSetAuthorityBinding{
			Issuer: "golden-token-issuer", Audience: "worksflow-golden-stack", SetHandleHash: testDigest("opaque-credential-set-handle"),
			MemberBindingsDigest: testDigest("credential-members-pending"), MemberCount: goldenCredentialMemberCount,
		},
		SourcePolicyAttestationDigest: testDigest("source-policy-attestation"),
		Suites: []ExpectedSuite{{
			ID: "golden-external", RequirementIDs: []string{"AIC-E2E-003"},
			RequiredArtifacts: []string{"browser-video", "credential-safe-trace", "credential-set-issuance-receipt", "credential-set-revocation-receipt", "golden-authority-document", GoldenFaultLedgerArtifactID, "golden-fixture-document", EncryptionAttestationArtifactID, "v7-writer-drain-proof", "zero-mock-zero-skip-playwright-json"},
		}},
		TestInventoryDigest: testDigest("test-inventory"),
		TestCases: []ExpectedTestCase{{
			CaseID: "QG-GOLDEN-001", SuiteID: "golden-external", RequirementIDs: []string{"AIC-E2E-003"},
			ContractCriterionIDs: []string{"AC-GOLDEN-001"}, File: "frontend/tests/golden.spec.ts",
			Title: "QG-GOLDEN-001 Golden", Mode: "qualification",
		}},
		VerifiedAt:   now,
		ArtifactRoot: artifactRoot, ArtifactIndexDigest: testDigest("pending-index"),
		EvidenceSnapshotRoot: evidenceRoot,
		ReceiptBundleDigest:  testDigest("pending-receipt"), TrustedReceiptIssuedAt: "2026-07-18T12:07:00.000Z",
		ArtifactSnapshotID: "snapshot-001", ArtifactSnapshotMode: ImmutableSnapshotMode,
	}
	members := testGoldenCredentialMembers()
	membersDigest, err := credentialSetMemberBindingsDigest(members)
	if err != nil {
		t.Fatal(err)
	}
	expected.CredentialSet.MemberBindingsDigest = membersDigest
	policy := TrustPolicy{
		Digest: testDigest("trust-policy"), MinimumSignatures: 2, MaxReceiptAge: 24 * time.Hour, MaxFutureSkew: time.Minute,
		Signers: map[string]SignerTrust{
			runner.keyID:   {Algorithm: runner.algorithm, PublicKey: runner.public, Identity: runner.identity, Role: SignerRoleRunner, NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour)},
			approver.keyID: {Algorithm: approver.algorithm, PublicKey: approver.public, Identity: approver.identity, Role: SignerRoleApprover, NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour)},
		},
		CredentialIssuers: map[string]CredentialIssuerTrust{
			expected.CredentialSet.Issuer: {
				Issuer: expected.CredentialSet.Issuer, MinimumSignatures: 1,
				Keys:              map[string]templateauthority.TrustedSigner{issuer.keyID: {Algorithm: issuer.algorithm, PublicKey: issuer.public, Identity: issuer.identity}},
				AllowedIdentities: []string{issuer.identity},
				KeyValidity:       map[string]AuthorityKeyValidity{issuer.keyID: {NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour)}},
			},
		},
		EncryptionRecipients: []EncryptionRecipient{{KeyResource: "kms://qualification/evidence", KeyVersion: "version-7"}},
		EncryptionAuthority: EncryptionAuthorityTrust{
			MinimumSignatures: 1, AllowedIdentities: []string{kms.identity},
			Keys: map[string]templateauthority.TrustedSigner{
				kms.keyID: {Algorithm: kms.algorithm, PublicKey: kms.public, Identity: kms.identity},
			},
			KeyValidity: map[string]AuthorityKeyValidity{kms.keyID: {NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour)}},
		},
		FaultAuthority: FaultAuthorityTrust{
			MinimumSignatures: 1, AllowedIdentities: []string{faultOperator.identity},
			Keys: map[string]templateauthority.TrustedSigner{
				faultOperator.keyID: {Algorithm: faultOperator.algorithm, PublicKey: faultOperator.public, Identity: faultOperator.identity},
			},
			KeyValidity: map[string]AuthorityKeyValidity{faultOperator.keyID: {NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour)}},
		},
		FaultLedgerAttestor: FaultLedgerAttestorTrust{
			MinimumSignatures: 1, AllowedIdentities: []string{faultAttestor.identity},
			Keys: map[string]templateauthority.TrustedSigner{
				faultAttestor.keyID: {Algorithm: faultAttestor.algorithm, PublicKey: faultAttestor.public, Identity: faultAttestor.identity},
			},
			KeyValidity: map[string]AuthorityKeyValidity{faultAttestor.keyID: {NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour)}},
		},
	}
	fixture := &verificationFixture{
		t: t, root: artifactRoot, indexPath: filepath.Join(evidenceRoot, "artifact-index.json"),
		receiptPath: filepath.Join(evidenceRoot, "qualification.dsse.json"), expected: expected, policy: policy,
		runner: runner, approver: approver, issuer: issuer, kms: kms,
		faultOperator: faultOperator, faultAttestor: faultAttestor, members: members,
	}
	issuance := CredentialSetIssuance{
		SchemaVersion: CredentialSetIssuanceSchemaV1, RunID: expected.RunID, FixtureID: expected.GoldenRuntime.FixtureID,
		Issuer: expected.CredentialSet.Issuer, Audience: expected.CredentialSet.Audience, SetHandleHash: expected.CredentialSet.SetHandleHash,
		MemberBindingsDigest: expected.CredentialSet.MemberBindingsDigest, MemberCount: expected.CredentialSet.MemberCount,
		Members: append([]CredentialSetMember(nil), members...), Status: "issued",
		IssuedAt: "2026-07-18T12:00:30.000Z", ExpiresAt: "2026-07-18T12:09:30.000Z",
	}
	revocation := CredentialSetRevocation{
		SchemaVersion: CredentialSetRevocationSchemaV1, RunID: issuance.RunID, FixtureID: issuance.FixtureID,
		Issuer: issuance.Issuer, Audience: issuance.Audience, SetHandleHash: issuance.SetHandleHash,
		MemberBindingsDigest: issuance.MemberBindingsDigest, MemberCount: issuance.MemberCount,
		Members: append([]CredentialSetMember(nil), members...), Status: "revoked",
		IssuedAt: issuance.IssuedAt, ExpiresAt: issuance.ExpiresAt, RevokedAt: "2026-07-18T12:06:00.000Z",
	}
	setSubject := credentialSetExpectedSubject(expected.CredentialSet.SetHandleHash)
	issuancePayload := inTotoPayload(t, CredentialSetIssuancePredicateTypeV1, setSubject.Name, expected.CredentialSet.SetHandleHash, issuance)
	revocationPayload := inTotoPayload(t, CredentialSetRevocationPredicateTypeV1, setSubject.Name, expected.CredentialSet.SetHandleHash, revocation)
	type generatedFault struct {
		reference           goldenFaultAuthority
		predicate           goldenfault.AuthorityPredicate
		payload             []byte
		envelope            []byte
		receiptArtifactID   string
		adapterInvocationID string
		expectedOutcome     string
	}
	generatedFaults := make([]generatedFault, 0, len(goldenFaultOperationKindsV1()))
	faultReferences := make([]goldenFaultAuthority, 0, len(goldenFaultOperationKindsV1()))
	for index, operation := range goldenFaultOperationKindsV1() {
		selector, expectedOutcome, supported := goldenFaultContract(operation)
		if !supported {
			t.Fatalf("test operation %q is not in the closed contract", operation)
		}
		base := 20
		if index > 0 {
			base = 600 + (index-1)*4
		}
		predicate := goldenfault.AuthorityPredicate{
			AuthorityID: testGoldenUUID(base), ExpectedFenceDigest: testDigest(operation + "-precondition"),
			ExpiresAt: "2026-07-18T12:03:30.000Z", FixtureID: expected.GoldenRuntime.FixtureID,
			IssuedAt: "2026-07-18T12:01:30.000Z", MaxUses: 1,
			OperationKind: goldenfault.OperationKind(operation), ResourceSelector: selector,
			RunID: expected.RunID, SchemaVersion: goldenfault.AuthoritySchemaV1,
		}
		payload := mustJSON(t, predicate)
		envelope := signDirectDSSE(t, GoldenFaultPayloadType, payload, faultOperator)
		reference := goldenFaultAuthority{
			AuthorityID: predicate.AuthorityID,
			DSSE: goldenFaultDSSE{
				ArtifactID: testGoldenUUID(base + 1), EnvelopeDigest: testDigestFromBytes(envelope),
				PayloadDigest: testDigestFromBytes(payload), PayloadType: GoldenFaultPayloadType,
			},
			ExpectedFenceDigest: predicate.ExpectedFenceDigest, MaxUses: 1,
			OperationKind: operation, ResourceSelector: selector,
		}
		faultReferences = append(faultReferences, reference)
		generatedFaults = append(generatedFaults, generatedFault{
			reference: reference, predicate: predicate, payload: payload, envelope: envelope,
			receiptArtifactID: testGoldenUUID(base + 2), adapterInvocationID: testGoldenUUID(base + 3),
			expectedOutcome: expectedOutcome,
		})
	}
	goldenAuthorityDocument, goldenFixtureDocument := testGoldenDocuments(
		t, expected, members, templateauthority.SHA256Digest(issuancePayload), issuance.IssuedAt, issuance.ExpiresAt,
		faultReferences,
	)
	expected.GoldenRuntime.AuthorityDocumentDigest = testDigestFromBytes(goldenAuthorityDocument)
	expected.GoldenRuntime.FixtureDocumentDigest = testDigestFromBytes(goldenFixtureDocument)
	fixture.expected.GoldenRuntime = expected.GoldenRuntime
	issuanceEnvelope := signDSSE(t, issuancePayload, issuer)
	revocationEnvelope := signDSSE(t, revocationPayload, issuer)
	writerProof := WriterDrainProof{
		SchemaVersion: WriterDrainProofSchemaV1, PlanDigest: expected.PlanDigest, TemplateRelease: expected.TemplateRelease,
		FromVersion: CompilerVersionV6, ToVersion: CompilerVersionV7, Status: "drained", ActiveWriters: 0,
		InFlightMutations: 0, CompletedAt: "2026-07-18T11:59:00.000Z",
	}
	playwright := PlaywrightQualificationResult{
		SchemaVersion: PlaywrightResultSchemaV1, RunID: expected.RunID, TestInventoryDigest: expected.TestInventoryDigest,
		Config: PlaywrightResultConfig{ForbidOnly: true, Retries: 0, Workers: 1},
		Tests: []PlaywrightTestResult{{
			CaseID: "QG-GOLDEN-001", SuiteID: "golden-external", RequirementIDs: []string{"AIC-E2E-003"},
			ContractCriterionIDs: []string{"AC-GOLDEN-001"}, Status: "passed",
		}},
		Totals: QualificationTotals{Discovered: 1, Passed: 1},
	}
	contents := map[string][]byte{
		"browser-video":                       []byte("encrypted browser video"),
		"credential-safe-trace":               []byte("encrypted Playwright trace"),
		"credential-set-issuance-receipt":     issuanceEnvelope,
		"credential-set-revocation-receipt":   revocationEnvelope,
		"golden-authority-document":           goldenAuthorityDocument,
		"golden-fixture-document":             goldenFixtureDocument,
		"v7-writer-drain-proof":               mustJSON(t, writerProof),
		"zero-mock-zero-skip-playwright-json": mustJSON(t, playwright),
	}
	faultDescriptors := make([]ArtifactDescriptor, 0, len(generatedFaults)*2)
	faultLedger := GoldenFaultLedgerAttestation{
		Entries:   make([]GoldenFaultLedgerEntry, 0, len(generatedFaults)),
		FixtureID: expected.GoldenRuntime.FixtureID, IssuedAt: "2026-07-18T12:05:15.000Z",
		RunID: expected.RunID, SchemaVersion: GoldenFaultLedgerSchemaV1, Status: "terminal",
	}
	for _, fault := range generatedFaults {
		authorityID := uuid.MustParse(fault.predicate.AuthorityID)
		resolution := goldenfault.ResourceResolution{
			ResourceID:  "golden-" + string(fault.predicate.OperationKind),
			HeadDigest:  testDigest(string(fault.predicate.OperationKind) + "-head"),
			FenceDigest: fault.predicate.ExpectedFenceDigest,
		}
		resolutionDigest, err := goldenfault.ResourceResolutionDigest(authorityID, resolution)
		if err != nil {
			t.Fatal(err)
		}
		resultID := uuid.MustParse(fault.receiptArtifactID)
		consumeReceipt := goldenfault.ConsumeReceipt{
			AdapterInvocationID: uuid.MustParse(fault.adapterInvocationID),
			AdapterResultDigest: testDigest(string(fault.predicate.OperationKind) + "-adapter-result"),
			AuthorityID:         authorityID, CompletedAt: "2026-07-18T12:02:30.000Z",
			EnvelopeDigest: testDigestFromBytes(fault.envelope), ExpectedFenceDigest: fault.predicate.ExpectedFenceDigest,
			FixtureID:           uuid.MustParse(expected.GoldenRuntime.FixtureID),
			ObservedFenceDigest: testDigest(string(fault.predicate.OperationKind) + "-observed-fence"),
			ObservedHeadDigest:  testDigest(string(fault.predicate.OperationKind) + "-observed-head"),
			OperationKind:       fault.predicate.OperationKind, Outcome: goldenfault.AdapterOutcome(fault.expectedOutcome),
			PayloadDigest: testDigestFromBytes(fault.payload), PredicateDigest: testDigestFromBytes(fault.payload),
			ReservedAt: "2026-07-18T12:02:00.000Z", ResolutionDigest: resolutionDigest,
			ResolvedFenceDigest: resolution.FenceDigest, ResolvedHeadDigest: resolution.HeadDigest,
			ResolvedResourceID: resolution.ResourceID, ResourceSelector: fault.predicate.ResourceSelector,
			ResultID: resultID, RunID: uuid.MustParse(expected.RunID), SchemaVersion: goldenfault.ReceiptSchemaV1,
		}
		receiptBytes := mustJSON(t, consumeReceipt)
		verifiedAuthority := goldenfault.VerifiedAuthority{
			Predicate: fault.predicate, EnvelopeDigest: testDigestFromBytes(fault.envelope), PayloadDigest: testDigestFromBytes(fault.payload),
			SignerIdentities: []string{faultOperator.identity}, IssuedAt: mustCanonicalTime(t, fault.predicate.IssuedAt),
			ExpiresAt: mustCanonicalTime(t, fault.predicate.ExpiresAt),
		}
		reservation, terminal, err := goldenfault.ValidateConsumeReceiptEvidence(verifiedAuthority, receiptBytes)
		if err != nil {
			t.Fatalf("build valid Golden fault receipt for %q: %v", fault.predicate.OperationKind, err)
		}
		reservationDigest, err := goldenfault.ReservationEvidenceDigest(reservation)
		if err != nil {
			t.Fatal(err)
		}
		resultDigest, err := goldenfault.TerminalEvidenceDigest(terminal)
		if err != nil {
			t.Fatal(err)
		}
		faultLedger.Entries = append(faultLedger.Entries, GoldenFaultLedgerEntry{
			AuthorityDigest: consumeReceipt.PredicateDigest, AuthorityID: fault.predicate.AuthorityID,
			CompletedAt: consumeReceipt.CompletedAt, EnvelopeDigest: testDigestFromBytes(fault.envelope),
			OperationKind: string(fault.predicate.OperationKind), Outcome: string(consumeReceipt.Outcome),
			PayloadDigest: testDigestFromBytes(fault.payload), ReceiptArtifactID: resultID.String(),
			ReceiptDigest: testDigestFromBytes(receiptBytes), ReservationDigest: reservationDigest,
			ReservedAt: consumeReceipt.ReservedAt, ResultDigest: resultDigest,
			ResultID: resultID.String(), State: "terminal",
		})
		contents[fault.reference.DSSE.ArtifactID] = fault.envelope
		contents[resultID.String()] = receiptBytes
		faultDescriptors = append(faultDescriptors,
			fixture.newArtifact(fault.reference.DSSE.ArtifactID, "evidence/fault-authorities/"+fault.reference.DSSE.ArtifactID+".dsse.json", ArtifactTypeGoldenFaultAuthority, DSSEEnvelopeMediaType, fault.envelope, false),
			fixture.newArtifact(resultID.String(), "evidence/fault-receipts/"+resultID.String()+".json", ArtifactTypeGoldenFaultReceipt, CanonicalJSONMediaType, receiptBytes, false),
		)
	}
	faultLedgerPayload := inTotoPayload(
		t, GoldenFaultLedgerPredicateTypeV1, "worksflow-golden-fixture/"+expected.GoldenRuntime.FixtureID,
		testDigestFromBytes(goldenFixtureDocument), faultLedger,
	)
	faultLedgerEnvelope := signDSSE(t, faultLedgerPayload, faultAttestor)
	contents[GoldenFaultLedgerArtifactID] = faultLedgerEnvelope
	fixture.index = ArtifactIndex{
		SchemaVersion: ArtifactIndexSchemaV1, RunID: expected.RunID, PlanDigest: expected.PlanDigest,
		Source: expected.Source, TemplateRelease: expected.TemplateRelease,
		Artifacts: append(faultDescriptors, []ArtifactDescriptor{
			fixture.newArtifact("browser-video", "evidence/browser-video.enc", ArtifactTypeVideo, "application/octet-stream", contents["browser-video"], true),
			fixture.newArtifact("credential-safe-trace", "evidence/playwright-trace.zip.enc", ArtifactTypeTrace, "application/octet-stream", contents["credential-safe-trace"], true),
			fixture.newArtifact("credential-set-issuance-receipt", "evidence/credential-set-issuance.dsse.json", ArtifactTypeCredentialSetIssuance, "application/json", contents["credential-set-issuance-receipt"], false),
			fixture.newArtifact("credential-set-revocation-receipt", "evidence/credential-set-revocation.dsse.json", ArtifactTypeCredentialSetRevocation, "application/json", contents["credential-set-revocation-receipt"], false),
			fixture.newArtifact("golden-authority-document", "evidence/golden-authority.json", ArtifactTypeGoldenAuthorityDocument, "application/json", contents["golden-authority-document"], false),
			fixture.newArtifact(GoldenFaultLedgerArtifactID, "evidence/golden-fault-ledger-attestation.dsse.json", ArtifactTypeGoldenFaultLedger, DSSEEnvelopeMediaType, contents[GoldenFaultLedgerArtifactID], false),
			fixture.newArtifact("golden-fixture-document", "evidence/golden-fixture.json", ArtifactTypeGoldenFixtureDocument, "application/json", contents["golden-fixture-document"], false),
		}...),
	}
	encryptionManifestDigest, encryptedArtifacts, err := encryptionManifest(fixture.index)
	if err != nil {
		t.Fatal(err)
	}
	encryptionAttestation := EncryptionAttestation{
		SchemaVersion: EncryptionAttestationSchemaV1, RunID: expected.RunID, PlanDigest: expected.PlanDigest,
		TemplateRelease: expected.TemplateRelease, ManifestDigest: encryptionManifestDigest, Artifacts: encryptedArtifacts,
		IssuedAt: "2026-07-18T12:05:45.000Z",
	}
	for index := range encryptionAttestation.Artifacts {
		encryptionAttestation.Artifacts[index].EncryptedAt = "2026-07-18T12:05:30.000Z"
		encryptionAttestation.Artifacts[index].PlaintextDisposition = "never-persisted"
		encryptionAttestation.Artifacts[index].PlaintextDispositionAt = "2026-07-18T12:05:30.000Z"
	}
	encryptionPayload := inTotoPayload(t, EncryptionPredicateTypeV1, encryptionSubjectName(expected.RunID), encryptionManifestDigest, encryptionAttestation)
	contents[EncryptionAttestationArtifactID] = signDSSE(t, encryptionPayload, kms)
	fixture.index.Artifacts = append(fixture.index.Artifacts,
		fixture.newArtifact(EncryptionAttestationArtifactID, "evidence/kms-encryption-attestation.dsse.json", ArtifactTypeEncryptionAttestation, "application/json", contents[EncryptionAttestationArtifactID], false),
		fixture.newArtifact("v7-writer-drain-proof", "evidence/writer-drain.json", ArtifactTypeWriterDrain, "application/json", contents["v7-writer-drain-proof"], false),
		fixture.newArtifact("zero-mock-zero-skip-playwright-json", "evidence/playwright-result.json", ArtifactTypePlaywrightResults, "application/json", contents["zero-mock-zero-skip-playwright-json"], false),
	)
	for id, content := range contents {
		fixture.writeArtifact(id, content)
	}
	fixture.receipt = QualificationReceipt{
		SchemaVersion: ReceiptSchemaV2, Scope: ExternalQualificationScope, PromotionTarget: expected.PromotionTarget,
		AuthorityNonce: expected.AuthorityNonce, AuthorityExpiresAt: expected.AuthorityExpiresAt,
		RunID: expected.RunID, PlanDigest: expected.PlanDigest,
		PrePromotionManifestDigest: expected.PrePromotionManifestDigest, TrustPolicyDigest: policy.Digest,
		SourcePolicyAttestationDigest: expected.SourcePolicyAttestationDigest,
		Source:                        expected.Source, TemplateRelease: expected.TemplateRelease, GoldenRuntime: expected.GoldenRuntime,
		Constructor: ConstructorBinding{
			CompilerVersion: CompilerVersionV7, BuildContractHash: expected.BuildContractHash,
			WriterDrain: WriterDrainBinding{FromVersion: CompilerVersionV6, ToVersion: CompilerVersionV7, Status: "drained", ActiveWriters: 0, InFlightMutations: 0, CompletedAt: writerProof.CompletedAt, EvidenceArtifactID: expected.WriterDrainEvidenceArtifactID},
		},
		Suites: []SuiteResult{{
			ID: "golden-external", Result: "passed", RequirementIDs: []string{"AIC-E2E-003"},
			TestInventoryDigest: expected.TestInventoryDigest,
			ArtifactIDs:         []string{"browser-video", "credential-safe-trace", "credential-set-issuance-receipt", "credential-set-revocation-receipt", "golden-authority-document", GoldenFaultLedgerArtifactID, "golden-fixture-document", EncryptionAttestationArtifactID, "v7-writer-drain-proof", "zero-mock-zero-skip-playwright-json"},
		}},
		Totals: QualificationTotals{Discovered: 1, Passed: 1},
		CredentialSet: CredentialSetBinding{
			Issuer: expected.CredentialSet.Issuer, Audience: expected.CredentialSet.Audience, SetHandleHash: expected.CredentialSet.SetHandleHash,
			MemberBindingsDigest: expected.CredentialSet.MemberBindingsDigest, MemberCount: expected.CredentialSet.MemberCount,
			IssuedAt: issuance.IssuedAt, ExpiresAt: issuance.ExpiresAt, RevokedAt: revocation.RevokedAt,
			Issuance:   CredentialSetArtifactBinding{ArtifactID: "credential-set-issuance-receipt", PayloadDigest: templateauthority.SHA256Digest(issuancePayload)},
			Revocation: CredentialSetArtifactBinding{ArtifactID: "credential-set-revocation-receipt", PayloadDigest: templateauthority.SHA256Digest(revocationPayload)},
		},
		Decision: "qualified", StartedAt: "2026-07-18T12:01:00.000Z", CompletedAt: "2026-07-18T12:05:00.000Z", IssuedAt: "2026-07-18T12:07:00.000Z",
	}
	fixture.reseal()
	return fixture
}

func (fixture *verificationFixture) newArtifact(id, path, artifactType, mediaType string, content []byte, encrypted bool) ArtifactDescriptor {
	descriptor := ArtifactDescriptor{
		ID: id, Path: path, Type: artifactType, MediaType: mediaType, SHA256: templateauthority.SHA256Digest(content),
		SizeBytes: int64(len(content)), Classification: ClassificationDistributable,
		SuiteIDs: []string{"golden-external"}, RequirementIDs: []string{"AIC-E2E-003"},
	}
	if encrypted {
		descriptor.Classification = ClassificationRestrictedEncrypted
		aad := strings.Join([]string{"worksflow-qualification-encryption/v1", fixture.expected.RunID, fixture.expected.PlanDigest, id, path, fixture.expected.TemplateRelease.ContentHash}, "\n")
		descriptor.Encryption = &EncryptionDescriptor{
			Scheme: EncryptionSchemeV1, KeyResource: "kms://qualification/evidence", KeyVersion: "version-7",
			WrappedKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), Nonce: base64.StdEncoding.EncodeToString(make([]byte, 12)),
			Tag: base64.StdEncoding.EncodeToString(make([]byte, 16)), AdditionalData: aad, AdditionalDataHash: testDigestFromBytes([]byte(aad)),
		}
	}
	return descriptor
}

func (fixture *verificationFixture) descriptor(id string) ArtifactDescriptor {
	fixture.t.Helper()
	for _, descriptor := range fixture.index.Artifacts {
		if descriptor.ID == id {
			return descriptor
		}
	}
	fixture.t.Fatalf("missing descriptor %s", id)
	return ArtifactDescriptor{}
}

func (fixture *verificationFixture) readFaultLedger() GoldenFaultLedgerAttestation {
	fixture.t.Helper()
	descriptor := fixture.descriptor(GoldenFaultLedgerArtifactID)
	encoded, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(descriptor.Path)))
	if err != nil {
		fixture.t.Fatal(err)
	}
	var envelope struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		fixture.t.Fatal(err)
	}
	payload, err := base64.StdEncoding.Strict().DecodeString(envelope.Payload)
	if err != nil {
		fixture.t.Fatal(err)
	}
	var statement struct {
		Predicate json.RawMessage `json:"predicate"`
	}
	if err := json.Unmarshal(payload, &statement); err != nil {
		fixture.t.Fatal(err)
	}
	var ledger GoldenFaultLedgerAttestation
	if err := json.Unmarshal(statement.Predicate, &ledger); err != nil {
		fixture.t.Fatal(err)
	}
	return ledger
}

func (fixture *verificationFixture) readFaultConsumeReceipt(artifactID string) goldenfault.ConsumeReceipt {
	fixture.t.Helper()
	descriptor := fixture.descriptor(artifactID)
	encoded, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(descriptor.Path)))
	if err != nil {
		fixture.t.Fatal(err)
	}
	var receipt goldenfault.ConsumeReceipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		fixture.t.Fatal(err)
	}
	return receipt
}

func (fixture *verificationFixture) verifiedFaultAuthority(authorityID string) goldenfault.VerifiedAuthority {
	fixture.t.Helper()
	fixtureDescriptor := fixture.descriptor(fixture.expected.GoldenRuntime.FixtureDocumentArtifactID)
	fixtureBytes, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(fixtureDescriptor.Path)))
	if err != nil {
		fixture.t.Fatal(err)
	}
	parsedFixture, err := parseGoldenFixtureDocument(fixtureBytes)
	if err != nil {
		fixture.t.Fatal(err)
	}
	var reference goldenFaultAuthority
	found := false
	for _, fault := range parsedFixture.faults {
		if fault.AuthorityID == authorityID {
			reference = fault
			found = true
			break
		}
	}
	if !found {
		fixture.t.Fatalf("missing fault authority %s", authorityID)
	}
	descriptor := fixture.descriptor(reference.DSSE.ArtifactID)
	envelopeBytes, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(descriptor.Path)))
	if err != nil {
		fixture.t.Fatal(err)
	}
	var envelope struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(envelopeBytes, &envelope); err != nil {
		fixture.t.Fatal(err)
	}
	payload, err := base64.StdEncoding.Strict().DecodeString(envelope.Payload)
	if err != nil {
		fixture.t.Fatal(err)
	}
	var predicate goldenfault.AuthorityPredicate
	if err := json.Unmarshal(payload, &predicate); err != nil {
		fixture.t.Fatal(err)
	}
	return goldenfault.VerifiedAuthority{
		Predicate: predicate, EnvelopeDigest: testDigestFromBytes(envelopeBytes), PayloadDigest: testDigestFromBytes(payload),
		SignerIdentities: []string{fixture.faultOperator.identity}, IssuedAt: mustCanonicalTime(fixture.t, predicate.IssuedAt),
		ExpiresAt: mustCanonicalTime(fixture.t, predicate.ExpiresAt),
	}
}

func (fixture *verificationFixture) writeFaultLedger(value GoldenFaultLedgerAttestation, signer testSigningKey) {
	fixture.t.Helper()
	fixtureDescriptor := fixture.descriptor(fixture.expected.GoldenRuntime.FixtureDocumentArtifactID)
	payload := inTotoPayload(
		fixture.t, GoldenFaultLedgerPredicateTypeV1,
		"worksflow-golden-fixture/"+fixture.expected.GoldenRuntime.FixtureID,
		fixtureDescriptor.SHA256, value,
	)
	fixture.writeArtifact(GoldenFaultLedgerArtifactID, signDSSE(fixture.t, payload, signer))
}

func (fixture *verificationFixture) removeArtifact(id string) {
	fixture.t.Helper()
	descriptor := fixture.descriptor(id)
	if err := os.Remove(filepath.Join(fixture.root, filepath.FromSlash(descriptor.Path))); err != nil {
		fixture.t.Fatal(err)
	}
	filtered := make([]ArtifactDescriptor, 0, len(fixture.index.Artifacts)-1)
	for _, candidate := range fixture.index.Artifacts {
		if candidate.ID != id {
			filtered = append(filtered, candidate)
		}
	}
	fixture.index.Artifacts = filtered
}

func (fixture *verificationFixture) writeArtifactBytes(descriptor ArtifactDescriptor, content []byte) {
	fixture.t.Helper()
	absolute := filepath.Join(fixture.root, filepath.FromSlash(descriptor.Path))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		fixture.t.Fatal(err)
	}
	if err := os.WriteFile(absolute, content, 0o600); err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture *verificationFixture) writeArtifact(id string, content []byte) {
	fixture.t.Helper()
	descriptor := fixture.descriptor(id)
	absolute := filepath.Join(fixture.root, filepath.FromSlash(descriptor.Path))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		fixture.t.Fatal(err)
	}
	if err := os.WriteFile(absolute, content, 0o600); err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture *verificationFixture) reseal() {
	fixture.t.Helper()
	restricted := 0
	for index := range fixture.index.Artifacts {
		descriptor := &fixture.index.Artifacts[index]
		encoded, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(descriptor.Path)))
		if err != nil {
			fixture.t.Fatal(err)
		}
		descriptor.SHA256 = testDigestFromBytes(encoded)
		descriptor.SizeBytes = int64(len(encoded))
		if descriptor.Classification == ClassificationRestrictedEncrypted {
			restricted++
		}
	}
	indexBytes := mustJSON(fixture.t, fixture.index)
	if err := os.WriteFile(fixture.indexPath, indexBytes, 0o600); err != nil {
		fixture.t.Fatal(err)
	}
	fixture.expected.ArtifactIndexDigest = testDigestFromBytes(indexBytes)
	fixture.receipt.ArtifactIndex = ArtifactIndexBinding{Digest: testDigestFromBytes(indexBytes), Count: len(fixture.index.Artifacts), RestrictedEncryptedCount: restricted}
	fixture.writeReceipt(fixture.runner, fixture.approver)
}

func (fixture *verificationFixture) writeReceipt(signers ...testSigningKey) {
	fixture.t.Helper()
	fixture.writeReceiptValue(fixture.receipt, signers...)
}

func (fixture *verificationFixture) writeReceiptValue(predicate any, signers ...testSigningKey) {
	fixture.t.Helper()
	indexBytes, err := os.ReadFile(fixture.indexPath)
	if err != nil {
		fixture.t.Fatal(err)
	}
	payload := inTotoPayload(fixture.t, QualificationPredicateTypeV2,
		"worksflow-qualification-artifacts/"+fixture.expected.RunID, testDigestFromBytes(indexBytes), predicate)
	bundle := signDSSE(fixture.t, payload, signers...)
	if err := os.WriteFile(fixture.receiptPath, bundle, 0o600); err != nil {
		fixture.t.Fatal(err)
	}
	fixture.expected.ReceiptBundleDigest = testDigestFromBytes(bundle)
}

func newFixtureVerifier(fixture *verificationFixture) (*Verifier, error) {
	verifier, err := NewVerifier(fixture.policy)
	if err == nil {
		verifier.snapshotInspector = func(string) error { return nil }
	}
	return verifier, err
}

func assertFixtureVerificationFails(t *testing.T, fixture *verificationFixture) {
	t.Helper()
	verifier, err := newFixtureVerifier(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(fixture.receiptPath, fixture.indexPath, fixture.root, fixture.expected); err == nil {
		t.Fatal("unsafe Golden fault evidence was accepted")
	}
}

func (fixture *verificationFixture) rewriteCredentialSetEvidence() {
	fixture.t.Helper()
	credentialSet := fixture.receipt.CredentialSet
	issuance := CredentialSetIssuance{
		SchemaVersion: CredentialSetIssuanceSchemaV1, RunID: fixture.expected.RunID, FixtureID: fixture.expected.GoldenRuntime.FixtureID,
		Issuer: credentialSet.Issuer, Audience: credentialSet.Audience, SetHandleHash: credentialSet.SetHandleHash,
		MemberBindingsDigest: credentialSet.MemberBindingsDigest, MemberCount: credentialSet.MemberCount,
		Members: append([]CredentialSetMember(nil), fixture.members...), Status: "issued",
		IssuedAt: credentialSet.IssuedAt, ExpiresAt: credentialSet.ExpiresAt,
	}
	revocation := CredentialSetRevocation{
		SchemaVersion: CredentialSetRevocationSchemaV1, RunID: issuance.RunID, FixtureID: issuance.FixtureID,
		Issuer: issuance.Issuer, Audience: issuance.Audience, SetHandleHash: issuance.SetHandleHash,
		MemberBindingsDigest: issuance.MemberBindingsDigest, MemberCount: issuance.MemberCount,
		Members: append([]CredentialSetMember(nil), fixture.members...), Status: "revoked",
		IssuedAt: issuance.IssuedAt, ExpiresAt: issuance.ExpiresAt, RevokedAt: credentialSet.RevokedAt,
	}
	subject := credentialSetExpectedSubject(credentialSet.SetHandleHash)
	issuancePayload := inTotoPayload(fixture.t, CredentialSetIssuancePredicateTypeV1, subject.Name, credentialSet.SetHandleHash, issuance)
	revocationPayload := inTotoPayload(fixture.t, CredentialSetRevocationPredicateTypeV1, subject.Name, credentialSet.SetHandleHash, revocation)
	fixture.receipt.CredentialSet.Issuance.PayloadDigest = templateauthority.SHA256Digest(issuancePayload)
	fixture.receipt.CredentialSet.Revocation.PayloadDigest = templateauthority.SHA256Digest(revocationPayload)
	fixture.writeArtifact("credential-set-issuance-receipt", signDSSE(fixture.t, issuancePayload, fixture.issuer))
	fixture.writeArtifact("credential-set-revocation-receipt", signDSSE(fixture.t, revocationPayload, fixture.issuer))
}

func (fixture *verificationFixture) rewriteCredentialSetRevocationMembers(members []CredentialSetMember) {
	fixture.t.Helper()
	credentialSet := fixture.receipt.CredentialSet
	revocation := CredentialSetRevocation{
		SchemaVersion: CredentialSetRevocationSchemaV1, RunID: fixture.expected.RunID, FixtureID: fixture.expected.GoldenRuntime.FixtureID,
		Issuer: credentialSet.Issuer, Audience: credentialSet.Audience, SetHandleHash: credentialSet.SetHandleHash,
		MemberBindingsDigest: credentialSet.MemberBindingsDigest, MemberCount: credentialSet.MemberCount,
		Members: append([]CredentialSetMember(nil), members...), Status: "revoked",
		IssuedAt: credentialSet.IssuedAt, ExpiresAt: credentialSet.ExpiresAt, RevokedAt: credentialSet.RevokedAt,
	}
	subject := credentialSetExpectedSubject(credentialSet.SetHandleHash)
	payload := inTotoPayload(fixture.t, CredentialSetRevocationPredicateTypeV1, subject.Name, credentialSet.SetHandleHash, revocation)
	fixture.receipt.CredentialSet.Revocation.PayloadDigest = templateauthority.SHA256Digest(payload)
	fixture.writeArtifact("credential-set-revocation-receipt", signDSSE(fixture.t, payload, fixture.issuer))
}

func testGoldenCredentialMembers() []CredentialSetMember {
	actors := map[string]string{
		"fault-operator":   testGoldenUUID(101),
		"platform-admin":   testGoldenUUID(102),
		"platform-owner":   testGoldenUUID(103),
		"platform-user-a":  testGoldenUUID(104),
		"platform-user-b":  testGoldenUUID(105),
		"reference-user-a": testGoldenUUID(106),
		"reference-user-b": testGoldenUUID(107),
	}
	result := make([]CredentialSetMember, 0, len(goldenCredentialDefinitions))
	for _, definition := range goldenCredentialDefinitions {
		result = append(result, CredentialSetMember{
			Slot: definition.slot, ActorID: actors[definition.principalSlot], Kind: definition.kind,
			CredentialHandleHash: testDigest(definition.slot + "-credential-handle"),
		})
	}
	return result
}

func testGoldenDocuments(
	t *testing.T,
	expected ExpectedPromotion,
	members []CredentialSetMember,
	issuancePayloadDigest string,
	issuedAt string,
	expiresAt string,
	faultAuthorities []goldenFaultAuthority,
) ([]byte, []byte) {
	t.Helper()
	principals := make([]any, 0, len(goldenPrincipalDefinitions))
	for index, definition := range goldenPrincipalDefinitions {
		principals = append(principals, map[string]any{
			"actorId": testGoldenUUID(101 + index), "projectId": testGoldenUUID(201 + index),
			"realm": definition.realm, "role": definition.role, "slot": definition.slot,
			"tenantId": testGoldenUUID(301 + index),
		})
	}
	imageDigests := map[string]string{
		"agent-runner":           testDigest("agent-runner-image"),
		"language-server":        testDigest("language-server-image"),
		"qualification-runner":   testDigest("qualification-runner-image"),
		"qualification-verifier": testDigest("qualification-verifier-image"),
		"release-controller":     testDigest("release-controller-image"),
		"sandbox-runner":         testDigest("sandbox-runner-image"),
	}
	runtimeImages := make([]any, 0, len(goldenRuntimeImageRoles))
	for index, role := range goldenRuntimeImageRoles {
		runtimeImages = append(runtimeImages, map[string]any{
			"imageDigest": imageDigests[role], "role": role,
			"provenance": testGoldenIdentity(500+index*3, role+"-provenance"),
			"sbom":       testGoldenIdentity(501+index*3, role+"-sbom"),
			"signature":  testGoldenIdentity(502+index*3, role+"-signature"),
		})
	}
	referenceContract := testGoldenIdentity(50, "reference-contract")
	memberBindings := make([]any, 0, len(members))
	for _, member := range members {
		memberBindings = append(memberBindings, map[string]any{
			"actorId": member.ActorID, "credentialHandleHash": member.CredentialHandleHash,
			"kind": member.Kind, "slot": member.Slot,
		})
	}
	fixtureSubject := map[string]any{
		"agent": map[string]any{
			"modelGateway": map[string]any{
				"attestationDigest": testDigest("model-gateway-attestation"),
				"identity":          "spiffe://golden.example.test/model-gateway",
				"modelId":           "approved-model",
				"modelRevision":     "model-v1",
				"profileId":         "model-profile-v1",
				"providerId":        "approved-provider",
			},
			"runner": map[string]any{
				"identity": "spiffe://golden.example.test/agent-runner", "imageDigest": imageDigests["agent-runner"],
				"profileId": "agent-runner-v1",
			},
		},
		"credentialSet": map[string]any{
			"audience": expected.CredentialSet.Audience, "credentialSetHandleHash": expected.CredentialSet.SetHandleHash,
			"expiresAt": expiresAt, "issuedAt": issuedAt, "issuer": expected.CredentialSet.Issuer,
			"issuerAttestationDigest": issuancePayloadDigest, "memberBindings": memberBindings,
			"memberBindingsDigest": expected.CredentialSet.MemberBindingsDigest,
			"memberCount":          expected.CredentialSet.MemberCount,
			"setId":                testGoldenUUID(10),
		},
		"expiresAt":        expiresAt,
		"faultAuthorities": faultAuthorities,
		"fixtureId":        expected.GoldenRuntime.FixtureID,
		"issuedAt":         issuedAt,
		"lsp": map[string]any{
			"gateway": map[string]any{
				"apiOrigin": "https://platform-api.golden.example.test", "path": "/v1/sandbox-lsp",
				"ticketProtocolDigest": testDigest("lsp-ticket-protocol"), "wssProtocolDigest": testDigest("lsp-wss-protocol"),
			},
			"runtime": map[string]any{
				"capabilityDigest": testDigest("lsp-capabilities"), "identity": "spiffe://golden.example.test/language-server",
				"imageDigest": imageDigests["language-server"], "languages": []string{"typescript"}, "profileId": "lsp-runtime-v1",
			},
		},
		"planDigest": expected.PlanDigest,
		"platform": map[string]any{
			"apiOrigin": "https://platform-api.golden.example.test", "apiSchemaDigest": testDigest("platform-api-schema"),
			"deploymentReceipt": testGoldenIdentity(30, "platform-deployment"),
			"serverBuild": map[string]any{
				"buildId": "platform-build-v1", "imageDigest": testDigest("platform-server-image"), "version": "1.0.0",
			},
			"webOrigin": "https://platform-web.golden.example.test", "wssProtocolDigest": testDigest("platform-wss-protocol"),
		},
		"principals": principals,
		"reference": map[string]any{
			"apiImageDigest": testDigest("reference-api-image"), "apiOrigin": "https://reference-api.golden.example.test",
			"applicationId": testGoldenUUID(40), "contractBundle": referenceContract,
			"commands": map[string]any{
				"api": map[string]any{
					"argv": []string{"./bin/reference-api", "serve"}, "identity": "reference-api-command-v1", "workingDirectory": "/workspace",
				},
				"migration": map[string]any{
					"argv": []string{"./bin/reference-api", "migrate", "up"}, "identity": "reference-migration-command-v1", "workingDirectory": "/workspace",
				},
				"retention": map[string]any{
					"argv": []string{"./bin/reference-api", "retention", "run"}, "identity": "reference-retention-command-v1", "workingDirectory": "/workspace",
				},
				"web": map[string]any{
					"argv": []string{"node", "frontend/server.js"}, "identity": "reference-web-command-v1", "workingDirectory": "/workspace",
				},
			},
			"deploymentReceipt": map[string]any{
				"contentHash": testDigest("reference-deployment"), "id": testGoldenUUID(41),
				"schemaVersion": GoldenReferenceDeploymentReceiptSchemaV1,
			},
			"gateway": map[string]any{
				"attestationDigest": testDigest("reference-gateway-attestation"),
				"capabilityDigest":  testDigest("reference-gateway-capability"),
				"identity":          "spiffe://golden.example.test/reference-model-gateway",
				"modelProfile": map[string]any{
					"contentHash": testDigest("reference-model-profile"), "id": "reference-model-profile-v1",
					"maxAttempts": 3, "modelId": "reference-model", "modelRevision": "reference-model-v1",
					"providerId": "reference-provider", "timeoutMilliseconds": 120000,
				},
				"providerPolicy": map[string]any{
					"contentHash": testDigest("reference-provider-policy"), "fallbackAllowed": false,
					"id": "reference-project-default", "profilePinned": true,
				},
				"routeId": "reference-generated-app-route-v1", "secretInjectionReceipt": testGoldenIdentity(43, "reference-secret-injection"),
			},
			"migration": map[string]any{"contentHash": testDigest("reference-migration"), "identity": "reference-migration-v1"},
			"qualificationOperationSet": map[string]any{
				"contentHash": GoldenReferenceOperationSetDigestV1, "operations": goldenReferenceOperationKindsV1(),
				"schemaVersion": GoldenReferenceOperationSetSchemaV1,
			},
			"rateLimitPolicy": map[string]any{
				"burst": 10, "contentHash": testDigest("reference-rate-limit-policy"), "id": "reference-rate-limit-v1",
				"requests": 60, "scopes": []string{"project", "tenant-actor"}, "windowSeconds": 60,
			},
			"retentionPolicy": map[string]any{
				"auditDays": 90, "contentHash": testDigest("reference-retention"), "eventDays": 30,
				"id": testGoldenUUID(42), "messageDays": 30, "redactionRequired": true, "runDays": 90,
			},
			"runEventSchemaDigest": testDigest("reference-run-event-schema"),
			"webImageDigest":       testDigest("reference-web-image"), "webOrigin": "https://reference-web.golden.example.test",
		},
		"release": map[string]any{"controller": map[string]any{
			"identity": "spiffe://golden.example.test/release-controller", "imageDigest": imageDigests["release-controller"],
			"profileId": "release-controller-v1", "protocol": "worksflow-release-controller-v1",
			"trustKeyDigest": testDigest("release-controller-trust-key"),
		}},
		"runId": expected.RunID,
		"sandbox": map[string]any{
			"apiOrigin": "https://platform-api.golden.example.test",
			"runner": map[string]any{
				"identity": "spiffe://golden.example.test/sandbox-runner", "imageDigest": imageDigests["sandbox-runner"],
				"profileId": "sandbox-runner-v1",
			},
			"runtimeProfileId": "sandbox-runtime-v1",
			"serviceProfiles": []any{map[string]any{
				"id": "sandbox-api-v1", "imageDigest": testDigest("sandbox-api-image"), "protocol": "http", "service": "sandbox-api",
			}},
		},
		"sharedArtifacts": map[string]any{
			"buildContract":           map[string]any{"contentHash": expected.BuildContractHash, "id": testGoldenUUID(51)},
			"buildManifest":           testGoldenIdentity(52, "build-manifest"),
			"referenceContractBundle": referenceContract,
			"runtimeImages":           runtimeImages,
			"sourceRepository": map[string]any{
				"commitOid": expected.Source.Commit, "contentTreeDigest": expected.Source.TreeDigest,
			},
			"templateRelease": map[string]any{
				"approvalReceiptDigest": expected.TemplateRelease.ApprovalReceiptDigest,
				"contentHash":           expected.TemplateRelease.ContentHash, "id": expected.TemplateRelease.ID,
			},
			"workspaceRevision": map[string]any{
				"canonicalQualityReceiptDigest": testDigest("canonical-quality-receipt"),
				"contentHash":                   expected.PromotionTarget.TargetRevision.ContentHash,
				"id":                            expected.PromotionTarget.TargetRevision.ID,
			},
		},
	}
	fixtureHash, err := goldenCanonicalDigest(fixtureSubject)
	if err != nil {
		t.Fatal(err)
	}
	authoritySubject := map[string]any{
		"authorityId": testGoldenUUID(1), "expiresAt": expiresAt, "fixtureHash": fixtureHash,
		"issuance": "root-issued-hash-bound", "issuedAt": issuedAt, "planDigest": expected.PlanDigest, "runId": expected.RunID,
	}
	authorityHash, err := goldenCanonicalDigest(authoritySubject)
	if err != nil {
		t.Fatal(err)
	}
	authority := mustJSON(t, map[string]any{"schemaVersion": GoldenAuthoritySchemaV2, "subject": authoritySubject})
	fixture := mustJSON(t, map[string]any{
		"authorityHash": authorityHash, "schemaVersion": GoldenFixtureSchemaV2, "subject": fixtureSubject,
	})
	return authority, fixture
}

func testGoldenIdentity(index int, label string) map[string]any {
	return map[string]any{"contentHash": testDigest(label), "id": testGoldenUUID(index)}
}

func testGoldenUUID(index int) string {
	return fmt.Sprintf("10000000-0000-4000-8000-%012d", index)
}

func newTestSigningKey(t *testing.T, keyID, identity string) testSigningKey {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testSigningKey{keyID: keyID, algorithm: templateauthority.AlgorithmEd25519, private: private, public: public, identity: identity}
}

func inTotoPayload(t *testing.T, predicateType, subjectName, subjectDigest string, predicate any) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"_type":         templateauthority.InTotoStatementV1,
		"subject":       []any{map[string]any{"name": subjectName, "digest": map[string]string{"sha256": strings.TrimPrefix(subjectDigest, "sha256:")}}},
		"predicateType": predicateType, "predicate": predicate,
	})
}

func signDSSE(t *testing.T, payload []byte, signers ...testSigningKey) []byte {
	t.Helper()
	raw := signDirectDSSE(t, InTotoPayloadType, payload, signers...)
	var envelope struct {
		PayloadType string `json:"payloadType"`
		Payload     string `json:"payload"`
		Signatures  []struct {
			KeyID string `json:"keyid"`
			Sig   string `json:"sig"`
		} `json:"signatures"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	return mustJSON(t, envelope)
}

func signDirectDSSE(t *testing.T, payloadType string, payload []byte, signers ...testSigningKey) []byte {
	t.Helper()
	pae := templateauthority.DSSEPAE(payloadType, payload)
	signatures := make([]map[string]string, 0, len(signers))
	for _, signer := range signers {
		var signature []byte
		switch signer.algorithm {
		case templateauthority.AlgorithmEd25519:
			signature = ed25519.Sign(signer.private.(ed25519.PrivateKey), pae)
		case templateauthority.AlgorithmECDSASHA256:
			digest := sha256.Sum256(pae)
			var err error
			signature, err = ecdsa.SignASN1(rand.Reader, signer.private.(*ecdsa.PrivateKey), digest[:])
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("unsupported test signing algorithm")
		}
		signatures = append(signatures, map[string]string{"keyid": signer.keyID, "sig": base64.StdEncoding.EncodeToString(signature)})
	}
	return mustJSON(t, map[string]any{"payload": base64.StdEncoding.EncodeToString(payload), "payloadType": payloadType, "signatures": signatures})
}

func mustCanonicalTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(canonicalTimeLayout, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func testDigestFromBytes(value []byte) string { return templateauthority.SHA256Digest(value) }

func unusedECDSATestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
