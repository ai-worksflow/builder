package templateoperator

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/templateauthority"
	"github.com/worksflow/builder/backend/internal/templates"
)

func TestDecodeEvidencePreparationRequestIsStrict(t *testing.T) {
	valid := []byte(`{"schemaVersion":"template-artifact-authority-evidence-preparation/v1"}`)
	if _, err := DecodeEvidencePreparationRequest(valid); err != nil {
		t.Fatalf("decode minimal preparation request: %v", err)
	}

	tests := []struct {
		input string
		want  string
	}{
		{
			input: `{"schemaVersion":"template-artifact-authority-evidence-preparation/v1","signer":{"keyId":"a","keyId":"b"}}`,
			want:  `duplicate JSON object key "keyId"`,
		},
		{
			input: `{"schemaVersion":"template-artifact-authority-evidence-preparation/v1","dsseEnvelope":{}}`,
			want:  `unknown field "dsseEnvelope"`,
		},
		{
			input: `{"schemaVersion":"template-artifact-authority-admission/v1"}`,
			want:  `schemaVersion must equal "template-artifact-authority-evidence-preparation/v1"`,
		},
	}
	for _, test := range tests {
		_, err := DecodeEvidencePreparationRequest([]byte(test.input))
		testRequireErrorContains(t, err, test.want)
	}
}

func TestLoadMatchingPrivateKeyRequiresReviewedPublicKeyAndPrivatePermissions(t *testing.T) {
	directory := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	path := filepath.Join(directory, "signer.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	configured := templateauthority.TrustedSigner{
		Algorithm: templateauthority.AlgorithmEd25519,
		PublicKey: publicKey,
		Identity:  "signer@example.test",
	}
	loaded, err := loadMatchingPrivateKey(path, configured, "test")
	if err != nil {
		t.Fatalf("load matching private key: %v", err)
	}
	signature, err := loaded.sign([]byte("message"))
	if err != nil || !ed25519.Verify(publicKey, []byte("message"), signature) {
		t.Fatalf("loaded key did not create a valid signature: %v", err)
	}

	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate replacement key: %v", err)
	}
	configured.PublicKey = otherPublic
	_, err = loadMatchingPrivateKey(path, configured, "test")
	testRequireErrorContains(t, err, "does not match the reviewed public key")

	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("relax private key permissions: %v", err)
	}
	configured.PublicKey = publicKey
	_, err = loadMatchingPrivateKey(path, configured, "test")
	testRequireErrorContains(t, err, "readable only by its owner")
}

func TestBindGateEvidenceDerivesAuthorityControlledBindings(t *testing.T) {
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	digest := func(character string) string { return "sha256:" + strings.Repeat(character, 64) }
	candidate := templates.AdmissionCandidate{
		Source:        templates.TemplateSource{TreeHash: digest("1")},
		LicenseDigest: digest("2"),
	}
	input := make([]templates.GateEvidence, 0, 16)
	for _, gate := range templates.RequiredAdmissionGates() {
		if gate == templates.GateSignatureAttestation {
			continue
		}
		input = append(input, templates.GateEvidence{
			Gate: gate, Outcome: templates.EvidencePassed, Digest: digest("3"),
			Reference: "urn:test:" + string(gate), Producer: "ci",
			InvocationID: "run-1", ObservedAt: now.Add(-time.Minute),
		})
	}
	commitments := Commitments{PolicyHash: digest("4"), TrustRootDigest: digest("5")}
	got, err := bindGateEvidence(
		input, candidate, digest("6"), digest("7"), digest("8"), commitments,
		"signer-key", "signer@example.test", "attempt-id", now,
	)
	if err != nil {
		t.Fatalf("bind gate evidence: %v", err)
	}
	if len(got) != len(templates.RequiredAdmissionGates()) {
		t.Fatalf("bound evidence count = %d, want %d", len(got), len(templates.RequiredAdmissionGates()))
	}
	byGate := make(map[templates.AdmissionGate]templates.GateEvidence, len(got))
	for index, item := range got {
		if index > 0 && got[index-1].Gate >= item.Gate {
			t.Fatalf("evidence is not sorted at %q", item.Gate)
		}
		byGate[item.Gate] = item
		if item.SubjectHash != digest("6") {
			t.Fatalf("gate %q subject hash = %q", item.Gate, item.SubjectHash)
		}
	}
	bindings := map[templates.AdmissionGate]string{
		templates.GateSourceIdentity:       digest("1"),
		templates.GateLicenseSPDX:          digest("2"),
		templates.GateRegistryPolicy:       digest("4"),
		templates.GateContainerBuild:       digest("7"),
		templates.GateSBOM:                 digest("8"),
		templates.GateSignatureAttestation: digest("5"),
	}
	for gate, want := range bindings {
		if byGate[gate].Digest != want {
			t.Fatalf("gate %q digest = %q, want %q", gate, byGate[gate].Digest, want)
		}
	}
}
