package templateoperator

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/worksflow/builder/backend/internal/templateauthority"
)

func TestDecodeConfigRejectsNonStrictJSON(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{
			name:  "duplicate top-level name",
			input: []byte(`{"schemaVersion":"first","schemaVersion":"second"}`),
			want:  `duplicate JSON object key "schemaVersion"`,
		},
		{
			name:  "duplicate nested name",
			input: []byte(`{"schemaVersion":"template-artifact-authority-config/v1","authority":{"id":"first","id":"second"}}`),
			want:  `duplicate JSON object key "id"`,
		},
		{
			name:  "unknown name",
			input: []byte(`{"schemaVersion":"template-artifact-authority-config/v1","unexpected":true}`),
			want:  `unknown field "unexpected"`,
		},
		{
			name:  "trailing value",
			input: []byte(`{"schemaVersion":"template-artifact-authority-config/v1"} {}`),
			want:  "trailing JSON value",
		},
		{
			name:  "oversized",
			input: bytes.Repeat([]byte{' '}, maxAuthorityConfigBytes+1),
			want:  "must be between 1 and",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeConfig(test.input)
			testRequireErrorContains(t, err, test.want)
		})
	}
}

func TestLoadConfigRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "authority.json")
	if err := os.WriteFile(target, []byte(`{"schemaVersion":"template-artifact-authority-config/v1"}`), 0o600); err != nil {
		t.Fatalf("write config target: %v", err)
	}
	link := filepath.Join(directory, "authority-link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create config symlink: %v", err)
	}

	_, err := LoadConfig(link)
	testRequireErrorContains(t, err, "regular non-symlink file")
}

func TestDeriveCommitmentsRejectsSymlinkedPublicKey(t *testing.T) {
	config := testValidOperatorConfig(t)
	target := config.DSSE.Keys[0].PublicKeyFile
	link := filepath.Join(filepath.Dir(target), "dsse-link.pem")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create public-key symlink: %v", err)
	}
	config.DSSE.Keys[0].PublicKeyFile = link

	_, err := DeriveCommitments(config)
	testRequireErrorContains(t, err, "regular non-symlink file")
}

func TestDeriveCommitmentsRejectsPublicKeyAlgorithmMismatch(t *testing.T) {
	config := testValidOperatorConfig(t)
	config.DSSE.Keys[0].Algorithm = string(templateauthority.AlgorithmECDSASHA256)

	_, err := DeriveCommitments(config)
	testRequireErrorContains(t, err, "public key is not a valid ECDSA key")
}

func TestDeriveCommitmentsIsStableAcrossSetOrdering(t *testing.T) {
	config := testValidOperatorConfig(t)
	want, err := DeriveCommitments(config)
	if err != nil {
		t.Fatalf("derive baseline commitments: %v", err)
	}
	compiled, err := compileConfig(config)
	if err != nil {
		t.Fatalf("compile baseline config: %v", err)
	}
	wantRedirectHosts := []string{
		"redirect-a.example.test",
		"redirect-b.example.test",
		"shared-cdn.example.test",
	}
	if !slices.Equal(compiled.redirectHosts, wantRedirectHosts) {
		t.Fatalf("compiled redirect union = %#v, want %#v", compiled.redirectHosts, wantRedirectHosts)
	}

	reordered := testCloneConfig(t, config)
	testReverse(reordered.Source.AllowedHosts)
	testReverse(reordered.Registry.Origins)
	for index := range reordered.Registry.Origins {
		testReverse(reordered.Registry.Origins[index].RedirectHosts)
	}
	testReverse(reordered.Registry.Repositories)
	testReverse(reordered.DSSE.Keys)
	testReverse(reordered.DSSE.AllowedPayloadTypes)
	testReverse(reordered.DSSE.AllowedPredicateTypes)
	testReverse(reordered.Transparency.Logs)
	for index := range reordered.Transparency.Logs {
		testReverse(reordered.Transparency.Logs[index].Keys)
	}

	got, err := DeriveCommitments(reordered)
	if err != nil {
		t.Fatalf("derive reordered commitments: %v", err)
	}
	if got != want {
		t.Fatalf("reordered commitments = %#v, want %#v", got, want)
	}
	reorderedCompiled, err := compileConfig(reordered)
	if err != nil {
		t.Fatalf("compile reordered config: %v", err)
	}
	if !slices.Equal(reorderedCompiled.redirectHosts, wantRedirectHosts) {
		t.Fatalf("reordered redirect union = %#v, want %#v", reorderedCompiled.redirectHosts, wantRedirectHosts)
	}
}

func TestDeriveCommitmentsDetectsTrustKeyDrift(t *testing.T) {
	config := testValidOperatorConfig(t)
	baseline, err := DeriveCommitments(config)
	if err != nil {
		t.Fatalf("derive baseline commitments: %v", err)
	}

	drifted := testCloneConfig(t, config)
	drifted.DSSE.Keys[0].PublicKeyFile = testWriteEd25519PublicKey(t, filepath.Dir(config.DSSE.Keys[0].PublicKeyFile), "replacement.pem")
	got, err := DeriveCommitments(drifted)
	if err != nil {
		t.Fatalf("derive key-drifted commitments: %v", err)
	}
	if got.TrustRootDigest == baseline.TrustRootDigest {
		t.Fatal("trust-root digest did not change after trusted public-key bytes changed")
	}
	if got.PolicyHash == baseline.PolicyHash {
		t.Fatal("policy hash did not change after its trust-root dependency changed")
	}
}

func TestDeriveCommitmentsDetectsPolicyDrift(t *testing.T) {
	config := testValidOperatorConfig(t)
	baseline, err := DeriveCommitments(config)
	if err != nil {
		t.Fatalf("derive baseline commitments: %v", err)
	}

	drifted := testCloneConfig(t, config)
	drifted.Registry.MaxBlobs++
	got, err := DeriveCommitments(drifted)
	if err != nil {
		t.Fatalf("derive policy-drifted commitments: %v", err)
	}
	if got.PolicyHash == baseline.PolicyHash {
		t.Fatal("policy hash did not change after a registry policy limit changed")
	}
	if got.TrustRootDigest != baseline.TrustRootDigest {
		t.Fatalf("policy-only drift changed trust root: got %q, want %q", got.TrustRootDigest, baseline.TrustRootDigest)
	}
}

func testValidOperatorConfig(t *testing.T) Config {
	t.Helper()
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	key := func(name string) string {
		return testWriteEd25519PublicKey(t, directory, name)
	}

	return Config{
		SchemaVersion: ConfigSchemaVersion,
		Authority: AuthorityConfig{
			ID:                  "template-authority",
			Version:             "v1",
			VerifierImageDigest: "sha256:" + strings.Repeat("a", 64),
			PredicateType:       "https://worksflow.example.test/predicate/template/v1",
		},
		Source: SourceConfig{
			GitBinary:    filepath.Clean(executable),
			CacheRoot:    filepath.Join(directory, "cache"),
			AllowedHosts: []string{"git-b.example.test", "git-a.example.test"},
			FetchTimeout: "2m0s",
		},
		Registry: RegistryConfig{
			Origins: []RegistryOriginConfig{
				{
					Host:          "registry-b.example.test",
					RedirectHosts: []string{"shared-cdn.example.test", "redirect-b.example.test"},
				},
				{
					Host:             "registry-a.example.test",
					AuthorizationEnv: "REGISTRY_A_TOKEN",
					RedirectHosts:    []string{"shared-cdn.example.test", "redirect-a.example.test"},
				},
			},
			Repositories: []RegistryRepositoryConfig{
				{Host: "registry-b.example.test", Repository: "worksflow/runtime"},
				{Host: "registry-a.example.test", Repository: "worksflow/builder"},
			},
			MaxManifestBytes: 1 << 20,
			MaxBlobBytes:     2 << 20,
			MaxTotalBytes:    8 << 20,
			MaxBlobs:         8,
			MaxRedirects:     3,
			Timeout:          "30s",
		},
		DSSE: DSSEConfig{
			Keys: []TrustedKeyConfig{
				{KeyID: "dsse-b", Algorithm: "ed25519", Identity: "builder-b@example.test", PublicKeyFile: key("dsse-b.pem")},
				{KeyID: "dsse-a", Algorithm: "ed25519", Identity: "builder-a@example.test", PublicKeyFile: key("dsse-a.pem")},
			},
			AllowedPayloadTypes: []string{
				"application/vnd.worksflow.template+json",
				"application/vnd.in-toto+json",
			},
			AllowedPredicateTypes: []string{
				"https://worksflow.example.test/predicate/secondary/v1",
				"https://worksflow.example.test/predicate/template/v1",
			},
			MinSignatures: 2,
		},
		Transparency: TransparencyConfig{
			Logs: []TransparencyLogConfig{
				{
					ID: "log-b",
					Keys: []TrustedKeyConfig{
						{KeyID: "log-b-key-b", Algorithm: "ed25519", Identity: "log-b-b@example.test", PublicKeyFile: key("log-b-key-b.pem")},
						{KeyID: "log-b-key-a", Algorithm: "ed25519", Identity: "log-b-a@example.test", PublicKeyFile: key("log-b-key-a.pem")},
					},
				},
				{
					ID: "log-a",
					Keys: []TrustedKeyConfig{
						{KeyID: "log-a-key-b", Algorithm: "ed25519", Identity: "log-a-b@example.test", PublicKeyFile: key("log-a-key-b.pem")},
						{KeyID: "log-a-key-a", Algorithm: "ed25519", Identity: "log-a-a@example.test", PublicKeyFile: key("log-a-key-a.pem")},
					},
				},
			},
			MaxEntryAge:   "24h0m0s",
			MaxFutureSkew: "5m0s",
		},
	}
}

func testConfigWithExpectedCommitments(t *testing.T) Config {
	t.Helper()
	config := testValidOperatorConfig(t)
	commitments, err := DeriveCommitments(config)
	if err != nil {
		t.Fatalf("derive expected commitments: %v", err)
	}
	config.Authority.ExpectedPolicyHash = commitments.PolicyHash
	config.Authority.ExpectedTrustRootDigest = commitments.TrustRootDigest
	return config
}

func testWriteEd25519PublicKey(t *testing.T, directory, name string) string {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate Ed25519 key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatalf("marshal Ed25519 public key: %v", err)
	}
	path := filepath.Join(directory, name)
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write Ed25519 public key: %v", err)
	}
	return path
}

func testCloneConfig(t *testing.T, config Config) Config {
	t.Helper()
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config clone: %v", err)
	}
	var clone Config
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatalf("unmarshal config clone: %v", err)
	}
	return clone
}

func testReverse[T any](values []T) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func testRequireErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want substring %q", err, want)
	}
}
