package productionpostgres

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func secureTestDSN(username, password, host, port, database string) string {
	endpoint := host
	if port != "" {
		endpoint += ":" + port
	}
	parsed := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(username, password),
		Host:   endpoint,
		Path:   "/" + database,
	}
	query := url.Values{
		"sslmode":              {"verify-full"},
		"sslrootcert":          {"/etc/worksflow/postgres-ca.pem"},
		"target_session_attrs": {"read-write"},
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func TestValidateConfigRequiresNineDistinctCredentialsForOneTarget(t *testing.T) {
	valid := Config{
		ApplicationDSN:                    secureTestDSN("app_login", "app-secret", "db.internal", "5432", "worksflow"),
		MigratorDSN:                       secureTestDSN("migrator_login", "migrator-secret", "db.internal", "5432", "worksflow"),
		QualificationDSN:                  secureTestDSN("qualification_login", "qualification-secret", "db.internal", "5432", "worksflow"),
		PromotionDSN:                      secureTestDSN("promotion_login", "promotion-secret", "db.internal", "5432", "worksflow"),
		PromotionSessionAffinity:          PromotionSessionAffinityDirect,
		PromotionRuntimeGate:              PromotionRuntimeGateDisabledPendingInputPrecommitAuthorityCanary,
		PolicyDSN:                         secureTestDSN("policy_login", "policy-secret", "db.internal", "5432", "worksflow"),
		InputPrecommitDSN:                 secureTestDSN("input_precommit_login", "input-precommit-secret", "db.internal", "5432", "worksflow"),
		InputPrecommitSessionAffinity:     PromotionSessionAffinityDirect,
		SourceVerifierDSN:                 secureTestDSN("source_verifier_login", "source-verifier-secret", "db.internal", "5432", "worksflow"),
		SourceVerifierSessionAffinity:     PromotionSessionAffinityDirect,
		CredentialResolverDSN:             secureTestDSN("credential_resolver_login", "credential-resolver-secret", "db.internal", "5432", "worksflow"),
		CredentialResolverSessionAffinity: PromotionSessionAffinityDirect,
		HandoffDSN:                        secureTestDSN("handoff_login", "handoff-secret", "db.internal", "5432", "worksflow"),
		HandoffSessionAffinity:            PromotionSessionAffinityDirect,
		Schema:                            "worksflow",
	}
	validated, err := validateConfig(valid)
	if err != nil {
		t.Fatalf("validateConfig(valid): %v", err)
	}
	if validated.application.username != "app_login" ||
		validated.migrator.username != "migrator_login" ||
		validated.qualification.username != "qualification_login" ||
		validated.promotion.username != "promotion_login" ||
		validated.promotionSessionAffinity != PromotionSessionAffinityDirect ||
		validated.policy.username != "policy_login" ||
		validated.inputPrecommit.username != "input_precommit_login" ||
		validated.sourceVerifier.username != "source_verifier_login" ||
		validated.credentialResolver.username != "credential_resolver_login" ||
		validated.handoff.username != "handoff_login" {
		t.Fatalf("unexpected validated identities: %#v", validated)
	}
	for _, scoped := range []string{
		validated.application.scoped,
		validated.migrator.scoped,
		validated.qualification.scoped,
		validated.promotion.scoped,
		validated.policy.scoped,
		validated.inputPrecommit.scoped,
		validated.sourceVerifier.scoped,
		validated.credentialResolver.scoped,
		validated.handoff.scoped,
	} {
		if !strings.Contains(scoped, "search_path=worksflow") {
			t.Fatalf("scoped DSN does not bind the trusted schema")
		}
	}
	sessionPool := valid
	sessionPool.PromotionSessionAffinity = PromotionSessionAffinitySessionPool
	if _, err := validateConfig(sessionPool); err != nil {
		t.Fatalf("session-affine pool posture rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"invalid schema", func(config *Config) { config.Schema = "public;drop" }},
		{"missing promotion", func(config *Config) { config.PromotionDSN = "" }},
		{"missing promotion session affinity", func(config *Config) { config.PromotionSessionAffinity = "" }},
		{"transaction pooling", func(config *Config) { config.PromotionSessionAffinity = "transaction-pool" }},
		{"promotion runtime enabled", func(config *Config) { config.PromotionRuntimeGate = "enabled" }},
		{"missing promotion runtime gate", func(config *Config) { config.PromotionRuntimeGate = "" }},
		{"missing policy", func(config *Config) { config.PolicyDSN = "" }},
		{"missing input precommit", func(config *Config) { config.InputPrecommitDSN = "" }},
		{"missing input precommit affinity", func(config *Config) { config.InputPrecommitSessionAffinity = "" }},
		{"input precommit transaction pooling", func(config *Config) { config.InputPrecommitSessionAffinity = "transaction-pool" }},
		{"missing source verifier", func(config *Config) { config.SourceVerifierDSN = "" }},
		{"missing source verifier affinity", func(config *Config) { config.SourceVerifierSessionAffinity = "" }},
		{"source verifier transaction pooling", func(config *Config) { config.SourceVerifierSessionAffinity = "transaction-pool" }},
		{"missing credential resolver", func(config *Config) { config.CredentialResolverDSN = "" }},
		{"missing credential resolver affinity", func(config *Config) { config.CredentialResolverSessionAffinity = "" }},
		{"credential resolver transaction pooling", func(config *Config) { config.CredentialResolverSessionAffinity = "transaction-pool" }},
		{"missing handoff", func(config *Config) { config.HandoffDSN = "" }},
		{"missing handoff affinity", func(config *Config) { config.HandoffSessionAffinity = "" }},
		{"handoff transaction pooling", func(config *Config) { config.HandoffSessionAffinity = "transaction-pool" }},
		{"handoff same login", func(config *Config) {
			config.HandoffDSN = secureTestDSN("app_login", "handoff-secret", "db.internal", "5432", "worksflow")
		}},
		{"handoff same secret", func(config *Config) {
			config.HandoffDSN = secureTestDSN("handoff_login", "app-secret", "db.internal", "5432", "worksflow")
		}},
		{"input precommit same login", func(config *Config) {
			config.InputPrecommitDSN = secureTestDSN("app_login", "input-precommit-secret", "db.internal", "5432", "worksflow")
		}},
		{"source verifier same secret", func(config *Config) {
			config.SourceVerifierDSN = secureTestDSN("source_verifier_login", "app-secret", "db.internal", "5432", "worksflow")
		}},
		{"credential resolver other database", func(config *Config) {
			config.CredentialResolverDSN = secureTestDSN("credential_resolver_login", "credential-resolver-secret", "db.internal", "5432", "other")
		}},
		{"promotion same login", func(config *Config) {
			config.PromotionDSN = secureTestDSN("app_login", "promotion-secret", "db.internal", "5432", "worksflow")
		}},
		{"promotion same secret", func(config *Config) {
			config.PromotionDSN = secureTestDSN("promotion_login", "app-secret", "db.internal", "5432", "worksflow")
		}},
		{"policy same login", func(config *Config) {
			config.PolicyDSN = secureTestDSN("app_login", "policy-secret", "db.internal", "5432", "worksflow")
		}},
		{"policy same secret", func(config *Config) {
			config.PolicyDSN = secureTestDSN("policy_login", "app-secret", "db.internal", "5432", "worksflow")
		}},
		{"same login", func(config *Config) {
			config.MigratorDSN = secureTestDSN("app_login", "other-secret", "db.internal", "5432", "worksflow")
		}},
		{"same secret", func(config *Config) {
			config.MigratorDSN = secureTestDSN("migrator_login", "app-secret", "db.internal", "5432", "worksflow")
		}},
		{"other host", func(config *Config) {
			config.QualificationDSN = secureTestDSN("qualification_login", "qualification-secret", "other.internal", "5432", "worksflow")
		}},
		{"other port", func(config *Config) {
			config.QualificationDSN = secureTestDSN("qualification_login", "qualification-secret", "db.internal", "5433", "worksflow")
		}},
		{"other database", func(config *Config) {
			config.QualificationDSN = secureTestDSN("qualification_login", "qualification-secret", "db.internal", "5432", "other")
		}},
		{"missing password", func(config *Config) {
			config.ApplicationDSN = "postgres://app_login@db.internal:5432/worksflow?sslmode=require"
		}},
		{"identity override", func(config *Config) {
			config.ApplicationDSN = "postgres://app_login:app-secret@db.internal:5432/worksflow?role=postgres"
		}},
		{"search path override", func(config *Config) {
			config.ApplicationDSN = "postgres://app_login:app-secret@db.internal:5432/worksflow?search_path=public"
		}},
		{"options override", func(config *Config) {
			config.ApplicationDSN = "postgres://app_login:app-secret@db.internal:5432/worksflow?options=-c%20role%3Dpostgres"
		}},
		{"noncanonical query", func(config *Config) {
			config.ApplicationDSN = "postgres://app_login:app-secret@db.internal:5432/worksflow?sslmode=require&application_name=app"
		}},
		{"uppercase host", func(config *Config) {
			config.ApplicationDSN = secureTestDSN("app_login", "app-secret", "DB.internal", "5432", "worksflow")
		}},
		{"TLS disabled", func(config *Config) {
			config.ApplicationDSN = strings.Replace(config.ApplicationDSN, "sslmode=verify-full", "sslmode=disable", 1)
		}},
		{"CA absent", func(config *Config) {
			parsed, _ := url.Parse(config.ApplicationDSN)
			query := parsed.Query()
			query.Del("sslrootcert")
			parsed.RawQuery = query.Encode()
			config.ApplicationDSN = parsed.String()
		}},
		{"standby allowed", func(config *Config) {
			config.ApplicationDSN = strings.Replace(config.ApplicationDSN, "target_session_attrs=read-write", "target_session_attrs=any", 1)
		}},
		{"client key", func(config *Config) {
			parsed, _ := url.Parse(config.ApplicationDSN)
			query := parsed.Query()
			query.Set("sslcert", "/run/secrets/client.crt")
			query.Set("sslkey", "/run/secrets/client.key")
			parsed.RawQuery = query.Encode()
			config.ApplicationDSN = parsed.String()
		}},
		{"different CA", func(config *Config) {
			parsed, _ := url.Parse(config.QualificationDSN)
			query := parsed.Query()
			query.Set("sslrootcert", "/etc/worksflow/other-ca.pem")
			parsed.RawQuery = query.Encode()
			config.QualificationDSN = parsed.String()
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			_, err := validateConfig(candidate)
			if err == nil {
				t.Fatal("unsafe configuration was accepted")
			}
			for _, secret := range []string{"app-secret", "migrator-secret", "qualification-secret", "promotion-secret", "policy-secret", "input-precommit-secret", "source-verifier-secret", "credential-resolver-secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("configuration error exposed secret %q: %v", secret, err)
				}
			}
		})
	}
}

func TestReadCredentialFileFailsClosed(t *testing.T) {
	directory := t.TempDir()
	validPath := filepath.Join(directory, "application.dsn")
	value := secureTestDSN("app_login", "do-not-expose", "db.internal", "", "worksflow") + "\n"
	if err := os.WriteFile(validPath, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := ReadCredentialFile(validPath)
	if err != nil || read != strings.TrimSuffix(value, "\n") {
		t.Fatalf("ReadCredentialFile(valid) = %q, %v", read, err)
	}

	t.Run("relative", func(t *testing.T) {
		if _, err := ReadCredentialFile("application.dsn"); err == nil {
			t.Fatal("relative credential path was accepted")
		}
	})
	t.Run("mode", func(t *testing.T) {
		path := filepath.Join(directory, "wide.dsn")
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadCredentialFile(path); err == nil {
			t.Fatal("group/world-readable credential was accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		path := filepath.Join(directory, "link.dsn")
		if err := os.Symlink(validPath, path); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadCredentialFile(path); err == nil {
			t.Fatal("symlinked credential was accepted")
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		target := filepath.Join(directory, "hard-target.dsn")
		link := filepath.Join(directory, "hard-link.dsn")
		if err := os.WriteFile(target, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadCredentialFile(target); err == nil {
			t.Fatal("multiply-linked credential was accepted")
		}
	})
	t.Run("multiple lines", func(t *testing.T) {
		path := filepath.Join(directory, "multiline.dsn")
		if err := os.WriteFile(path, []byte(value+"second\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadCredentialFile(path); err == nil || strings.Contains(err.Error(), "do-not-expose") {
			t.Fatalf("multiline credential did not fail safely: %v", err)
		}
	})
}

func TestVerifyTrustAnchorFileFailsClosed(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Worksflow test root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, privateKey.Public(), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(home, ".postgres-trust-anchor-test-")
	if err != nil {
		t.Fatal(err)
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	valid := filepath.Join(directory, "root.pem")
	if err := os.WriteFile(valid, certificate, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTrustAnchorFile(valid); err != nil {
		t.Fatalf("VerifyTrustAnchorFile(valid): %v", err)
	}

	t.Run("writable by group", func(t *testing.T) {
		path := filepath.Join(directory, "writable.pem")
		if err := os.WriteFile(path, certificate, 0o664); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o664); err != nil {
			t.Fatal(err)
		}
		if err := VerifyTrustAnchorFile(path); err == nil {
			t.Fatal("group-writable trust anchor was accepted")
		}
	})
	t.Run("writable parent", func(t *testing.T) {
		parent := filepath.Join(directory, "writable-parent")
		if err := os.Mkdir(parent, 0o770); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0o770); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(parent, "root.pem")
		if err := os.WriteFile(path, certificate, 0o444); err != nil {
			t.Fatal(err)
		}
		if err := VerifyTrustAnchorFile(path); err == nil {
			t.Fatal("trust anchor below a group-writable parent was accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		path := filepath.Join(directory, "root-link.pem")
		if err := os.Symlink(valid, path); err != nil {
			t.Fatal(err)
		}
		if err := VerifyTrustAnchorFile(path); err == nil {
			t.Fatal("symlinked trust anchor was accepted")
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		target := filepath.Join(directory, "linked-root.pem")
		link := filepath.Join(directory, "linked-root-copy.pem")
		if err := os.WriteFile(target, certificate, 0o444); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(target, link); err != nil {
			t.Fatal(err)
		}
		if err := VerifyTrustAnchorFile(target); err == nil {
			t.Fatal("multiply-linked trust anchor was accepted")
		}
	})
	t.Run("not a certificate", func(t *testing.T) {
		path := filepath.Join(directory, "not-a-root.pem")
		if err := os.WriteFile(path, []byte("not a certificate"), 0o444); err != nil {
			t.Fatal(err)
		}
		if err := VerifyTrustAnchorFile(path); err == nil || strings.Contains(err.Error(), "not a certificate") {
			t.Fatalf("invalid trust anchor did not fail safely: %v", err)
		}
	})
}
