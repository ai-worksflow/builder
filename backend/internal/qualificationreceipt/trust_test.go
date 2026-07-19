package qualificationreceipt

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTrustPolicyBuildsPinnedRoleAndIssuerAuthorities(t *testing.T) {
	document := validTrustPolicyDocument(t)
	path := filepath.Join(t.TempDir(), "trust.json")
	encoded := mustJSON(t, document)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadTrustPolicy(path)
	if err != nil {
		t.Fatalf("load trust policy: %v", err)
	}
	if policy.Digest != testDigestFromBytes(encoded) || policy.MinimumSignatures != 2 || len(policy.Signers) != 2 ||
		len(policy.CredentialIssuers) != 1 || len(policy.FaultAuthority.Keys) != 1 || len(policy.FaultLedgerAttestor.Keys) != 1 {
		t.Fatalf("unexpected loaded policy: %+v", policy)
	}
	if _, err := NewVerifier(policy); err != nil {
		t.Fatalf("configure verifier from policy: %v", err)
	}
}

func TestLoadTrustPolicyRejectsNonCanonicalAndUnsafeKeys(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "signers out of order", mutate: func(document map[string]any) {
			signers := document["signers"].([]any)
			signers[0], signers[1] = signers[1], signers[0]
		}},
		{name: "unsupported algorithm", mutate: func(document map[string]any) {
			document["signers"].([]any)[0].(map[string]any)["algorithm"] = "rsa-sha256"
		}},
		{name: "oversized future skew", mutate: func(document map[string]any) {
			document["maxFutureSkewSeconds"] = 301
		}},
		{name: "omitted zero future skew", mutate: func(document map[string]any) {
			delete(document, "maxFutureSkewSeconds")
		}},
		{name: "null future skew", mutate: func(document map[string]any) {
			document["maxFutureSkewSeconds"] = nil
		}},
		{name: "duplicate issuer identity", mutate: func(document map[string]any) {
			document["credentialIssuers"].([]any)[0].(map[string]any)["allowedIdentities"] = []string{"issuer@example", "issuer@example"}
		}},
		{name: "fault authority reuses runner identity", mutate: func(document map[string]any) {
			document["faultAuthority"].(map[string]any)["allowedIdentities"] = []string{"runner@example"}
			document["faultAuthority"].(map[string]any)["keys"].([]any)[0].(map[string]any)["identity"] = "runner@example"
		}},
		{name: "attestor reuses fault public key", mutate: func(document map[string]any) {
			faultPEM := document["faultAuthority"].(map[string]any)["keys"].([]any)[0].(map[string]any)["publicKeyPem"]
			document["faultLedgerAttestor"].(map[string]any)["keys"].([]any)[0].(map[string]any)["publicKeyPem"] = faultPEM
		}},
		{name: "fault authority reuses credential key id", mutate: func(document map[string]any) {
			document["faultAuthority"].(map[string]any)["keys"].([]any)[0].(map[string]any)["keyId"] = "issuer-key"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validTrustPolicyDocument(t)
			test.mutate(document)
			path := filepath.Join(t.TempDir(), "trust.json")
			if err := os.WriteFile(path, mustJSON(t, document), 0o600); err != nil {
				t.Fatal(err)
			}
			policy, err := LoadTrustPolicy(path)
			if err == nil {
				_, err = NewVerifier(policy)
			}
			if err == nil {
				t.Fatal("expected trust policy to fail closed")
			}
		})
	}
}

func validTrustPolicyDocument(t *testing.T) map[string]any {
	t.Helper()
	approverPEM := testPublicKeyPEM(t)
	runnerPEM := testPublicKeyPEM(t)
	issuerPEM := testPublicKeyPEM(t)
	encryptionPEM := testPublicKeyPEM(t)
	faultPEM := testPublicKeyPEM(t)
	attestorPEM := testPublicKeyPEM(t)
	return map[string]any{
		"schemaVersion": TrustPolicySchemaV2, "minimumSignatures": 2,
		"maxReceiptAgeSeconds": 86400, "maxFutureSkewSeconds": 60,
		"signers": []any{
			map[string]any{"keyId": "approver-key", "algorithm": "ed25519", "identity": "approver@example", "role": SignerRoleApprover, "notBefore": "2026-01-01T00:00:00.000Z", "notAfter": "2027-01-01T00:00:00.000Z", "publicKeyPem": approverPEM},
			map[string]any{"keyId": "runner-key", "algorithm": "ed25519", "identity": "runner@example", "role": SignerRoleRunner, "notBefore": "2026-01-01T00:00:00.000Z", "notAfter": "2027-01-01T00:00:00.000Z", "publicKeyPem": runnerPEM},
		},
		"credentialIssuers": []any{map[string]any{
			"issuer": "golden-issuer", "minimumSignatures": 1, "allowedIdentities": []string{"issuer@example"},
			"keys": []any{map[string]any{"keyId": "issuer-key", "algorithm": "ed25519", "identity": "issuer@example", "notBefore": "2026-01-01T00:00:00.000Z", "notAfter": "2027-01-01T00:00:00.000Z", "publicKeyPem": issuerPEM}},
		}},
		"encryptionRecipients": []any{map[string]any{"keyResource": "kms://qualification/evidence", "keyVersion": "version-7"}},
		"encryptionAuthority": map[string]any{
			"minimumSignatures": 1, "allowedIdentities": []string{"kms@qualification.example"},
			"keys": []any{map[string]any{
				"keyId": "kms-key", "algorithm": "ed25519", "identity": "kms@qualification.example", "notBefore": "2026-01-01T00:00:00.000Z", "notAfter": "2027-01-01T00:00:00.000Z", "publicKeyPem": encryptionPEM,
			}},
		},
		"faultAuthority": map[string]any{
			"minimumSignatures": 1, "allowedIdentities": []string{"fault-operator@qualification.example"},
			"keys": []any{map[string]any{
				"keyId": "fault-key", "algorithm": "ed25519", "identity": "fault-operator@qualification.example", "notBefore": "2026-01-01T00:00:00.000Z", "notAfter": "2027-01-01T00:00:00.000Z", "publicKeyPem": faultPEM,
			}},
		},
		"faultLedgerAttestor": map[string]any{
			"minimumSignatures": 1, "allowedIdentities": []string{"fault-ledger-attestor@qualification.example"},
			"keys": []any{map[string]any{
				"keyId": "fault-ledger-key", "algorithm": "ed25519", "identity": "fault-ledger-attestor@qualification.example", "notBefore": "2026-01-01T00:00:00.000Z", "notAfter": "2027-01-01T00:00:00.000Z", "publicKeyPem": attestorPEM,
			}},
		},
	}
}

func testPublicKeyPEM(t *testing.T) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: encoded}))
}
