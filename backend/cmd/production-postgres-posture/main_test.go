package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/productionpostgres"
)

func secureCommandDSN(username, password string) string {
	parsed := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(username, password),
		Host:   "db.internal:5432",
		Path:   "/worksflow",
	}
	query := url.Values{
		"sslmode":              {"verify-full"},
		"sslrootcert":          {"/etc/worksflow/postgres-ca.pem"},
		"target_session_attrs": {"read-write"},
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func writeCredential(t *testing.T, name, dsn string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(dsn+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validEnvironment(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		applicationDSNFileEnvironment:                writeCredential(t, "app.dsn", secureCommandDSN("app_login", "app-secret")),
		migratorDSNFileEnvironment:                   writeCredential(t, "migrator.dsn", secureCommandDSN("migrator_login", "migrator-secret")),
		qualificationDSNFileEnvironment:              writeCredential(t, "qualification.dsn", secureCommandDSN("qualification_login", "qualification-secret")),
		promotionDSNFileEnvironment:                  writeCredential(t, "promotion.dsn", secureCommandDSN("promotion_login", "promotion-secret")),
		promotionSessionAffinityEnvironment:          string(productionpostgres.PromotionSessionAffinityDirect),
		promotionRuntimeGateEnvironment:              productionpostgres.PromotionRuntimeGateDisabledPendingInputPrecommitAuthorityCanary,
		policyDSNFileEnvironment:                     writeCredential(t, "policy.dsn", secureCommandDSN("policy_login", "policy-secret")),
		inputPrecommitDSNFileEnvironment:             writeCredential(t, "input-precommit.dsn", secureCommandDSN("input_precommit_login", "input-precommit-secret")),
		inputPrecommitSessionAffinityEnvironment:     string(productionpostgres.PromotionSessionAffinityDirect),
		sourceVerifierDSNFileEnvironment:             writeCredential(t, "source-verifier.dsn", secureCommandDSN("source_verifier_login", "source-verifier-secret")),
		sourceVerifierSessionAffinityEnvironment:     string(productionpostgres.PromotionSessionAffinityDirect),
		credentialResolverDSNFileEnvironment:         writeCredential(t, "credential-resolver.dsn", secureCommandDSN("credential_resolver_login", "credential-resolver-secret")),
		credentialResolverSessionAffinityEnvironment: string(productionpostgres.PromotionSessionAffinityDirect),
		handoffDSNFileEnvironment:                    writeCredential(t, "handoff.dsn", secureCommandDSN("handoff_login", "handoff-secret")),
		handoffSessionAffinityEnvironment:            string(productionpostgres.PromotionSessionAffinityDirect),
		schemaEnvironment:                            "worksflow",
		timeoutEnvironment:                           "45s",
	}
}

func lookupMap(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func passedResult() productionpostgres.Result {
	return productionpostgres.Result{
		SchemaVersion:                     productionpostgres.ResultSchemaVersion,
		Status:                            productionpostgres.StatusPassed,
		CheckedAt:                         "2026-07-19T12:00:00Z",
		Schema:                            "worksflow",
		EvidenceClass:                     "standalone-point-in-time-posture-check",
		PromotionSessionAffinity:          productionpostgres.PromotionSessionAffinityDirect,
		PromotionRuntimeGate:              productionpostgres.PromotionRuntimeGateDisabledPendingInputPrecommitAuthorityCanary,
		InputPrecommitSessionAffinity:     productionpostgres.PromotionSessionAffinityDirect,
		SourceVerifierSessionAffinity:     productionpostgres.PromotionSessionAffinityDirect,
		CredentialResolverSessionAffinity: productionpostgres.PromotionSessionAffinityDirect,
		HandoffSessionAffinity:            productionpostgres.PromotionSessionAffinityDirect,
		Roles: []productionpostgres.RoleResult{
			{Kind: productionpostgres.RoleApplication, Identity: "app_login", Responsibility: "API runtime least-privilege data plane", Status: productionpostgres.StatusPassed},
			{Kind: productionpostgres.RoleMigrator, Identity: "migrator_login", Responsibility: "one-shot migration-owner schema authority", Status: productionpostgres.StatusPassed},
			{Kind: productionpostgres.RoleQualification, Identity: "qualification_login", Responsibility: "read-only catalog auditor with no trusted-schema data or function access", Status: productionpostgres.StatusPassed},
			{Kind: productionpostgres.RolePromotion, Identity: "promotion_login", Responsibility: "disabled Promotion-v2 consumer with no handoff-resolver or data-plane table authority", Status: productionpostgres.StatusPassed},
			{Kind: productionpostgres.RolePolicy, Identity: "policy_login", Responsibility: "dedicated qualification-policy issuer and resolver", Status: productionpostgres.StatusPassed},
			{Kind: productionpostgres.RoleInputPrecommit, Identity: "input_precommit_login", Responsibility: "disabled qualification input-precommit authority issuer and resolver", Status: productionpostgres.StatusPassed},
			{Kind: productionpostgres.RoleSourceVerifier, Identity: "source_verifier_login", Responsibility: "disabled qualification source-verifier receipt admission", Status: productionpostgres.StatusPassed},
			{Kind: productionpostgres.RoleCredentialResolver, Identity: "credential_resolver_login", Responsibility: "disabled qualification credential-resolver receipt admission", Status: productionpostgres.StatusPassed},
			{Kind: productionpostgres.RoleHandoff, Identity: "handoff_login", Responsibility: "disabled private qualification handoff completion and inspection", Status: productionpostgres.StatusPassed},
		},
		ExcludedClaims: []string{"external-qualification-receipt", "gc-scheduler-qualification", "promotion-authority", "promotion-runtime-activation", "input-precommit-authority-canary"},
	}
}

func TestRunWritesOneSafePassedResult(t *testing.T) {
	environment := validEnvironment(t)
	var output strings.Builder
	verified := false
	exitCode := run(
		context.Background(),
		lookupMap(environment),
		&output,
		func(_ context.Context, config productionpostgres.Config) (productionpostgres.Result, error) {
			verified = true
			if config.Schema != "worksflow" || !strings.Contains(config.ApplicationDSN, "app-secret") ||
				!strings.Contains(config.MigratorDSN, "migrator-secret") ||
				!strings.Contains(config.QualificationDSN, "qualification-secret") ||
				!strings.Contains(config.PromotionDSN, "promotion-secret") ||
				config.PromotionSessionAffinity != productionpostgres.PromotionSessionAffinityDirect ||
				config.PromotionRuntimeGate != productionpostgres.PromotionRuntimeGateDisabledPendingInputPrecommitAuthorityCanary ||
				!strings.Contains(config.PolicyDSN, "policy-secret") ||
				!strings.Contains(config.InputPrecommitDSN, "input-precommit-secret") ||
				!strings.Contains(config.SourceVerifierDSN, "source-verifier-secret") ||
				!strings.Contains(config.CredentialResolverDSN, "credential-resolver-secret") ||
				config.InputPrecommitSessionAffinity != productionpostgres.PromotionSessionAffinityDirect ||
				config.SourceVerifierSessionAffinity != productionpostgres.PromotionSessionAffinityDirect ||
				config.CredentialResolverSessionAffinity != productionpostgres.PromotionSessionAffinityDirect ||
				!strings.Contains(config.HandoffDSN, "handoff-secret") ||
				config.HandoffSessionAffinity != productionpostgres.PromotionSessionAffinityDirect {
				t.Fatalf("verifier did not receive the nine isolated credentials")
			}
			return passedResult(), nil
		},
		func() time.Time { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) },
	)
	if exitCode != exitPassed || !verified {
		t.Fatalf("run exit = %d, verified = %v", exitCode, verified)
	}
	var result productionpostgres.Result
	if err := json.Unmarshal([]byte(output.String()), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, output.String())
	}
	if result.Status != productionpostgres.StatusPassed || len(result.Roles) != 9 {
		t.Fatalf("unexpected result: %#v", result)
	}
	for _, secret := range []string{"app-secret", "migrator-secret", "qualification-secret", "promotion-secret", "policy-secret", "input-precommit-secret", "source-verifier-secret", "credential-resolver-secret", "handoff-secret", "postgres://", "db.internal"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("structured output exposed %q: %s", secret, output.String())
		}
	}
}

func TestRunUsesExplicitExitCodesAndNeverPrintsSecrets(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) }
	t.Run("invalid configuration", func(t *testing.T) {
		environment := validEnvironment(t)
		delete(environment, qualificationDSNFileEnvironment)
		var output strings.Builder
		exitCode := run(context.Background(), lookupMap(environment), &output, productionpostgres.Verify, now)
		if exitCode != exitInvalidConfiguration || !strings.Contains(output.String(), productionpostgres.FailureConfigurationInvalid) {
			t.Fatalf("invalid configuration exit/output = %d, %s", exitCode, output.String())
		}
	})
	t.Run("missing policy credential", func(t *testing.T) {
		environment := validEnvironment(t)
		delete(environment, policyDSNFileEnvironment)
		var output strings.Builder
		exitCode := run(context.Background(), lookupMap(environment), &output, productionpostgres.Verify, now)
		if exitCode != exitInvalidConfiguration || !strings.Contains(output.String(), productionpostgres.FailureConfigurationInvalid) {
			t.Fatalf("invalid configuration exit/output = %d, %s", exitCode, output.String())
		}
	})
	t.Run("transaction pooling rejected", func(t *testing.T) {
		environment := validEnvironment(t)
		environment[promotionSessionAffinityEnvironment] = "transaction-pool"
		var output strings.Builder
		exitCode := run(context.Background(), lookupMap(environment), &output, productionpostgres.Verify, now)
		if exitCode != exitInvalidConfiguration || !strings.Contains(output.String(), productionpostgres.FailureConfigurationInvalid) {
			t.Fatalf("transaction-pool posture exit/output = %d, %s", exitCode, output.String())
		}
	})
	t.Run("input authority transaction pooling rejected", func(t *testing.T) {
		environment := validEnvironment(t)
		environment[sourceVerifierSessionAffinityEnvironment] = "transaction-pool"
		var output strings.Builder
		exitCode := run(context.Background(), lookupMap(environment), &output, productionpostgres.Verify, now)
		if exitCode != exitInvalidConfiguration || !strings.Contains(output.String(), productionpostgres.FailureConfigurationInvalid) {
			t.Fatalf("input authority transaction-pool posture exit/output = %d, %s", exitCode, output.String())
		}
	})
	t.Run("missing handoff credential", func(t *testing.T) {
		environment := validEnvironment(t)
		delete(environment, handoffDSNFileEnvironment)
		var output strings.Builder
		exitCode := run(context.Background(), lookupMap(environment), &output, productionpostgres.Verify, now)
		if exitCode != exitInvalidConfiguration || !strings.Contains(output.String(), productionpostgres.FailureConfigurationInvalid) {
			t.Fatalf("missing handoff posture exit/output = %d, %s", exitCode, output.String())
		}
	})
	t.Run("handoff transaction pooling rejected", func(t *testing.T) {
		environment := validEnvironment(t)
		environment[handoffSessionAffinityEnvironment] = "transaction-pool"
		var output strings.Builder
		exitCode := run(context.Background(), lookupMap(environment), &output, productionpostgres.Verify, now)
		if exitCode != exitInvalidConfiguration || !strings.Contains(output.String(), productionpostgres.FailureConfigurationInvalid) {
			t.Fatalf("handoff transaction-pool posture exit/output = %d, %s", exitCode, output.String())
		}
	})
	t.Run("promotion runtime enable rejected", func(t *testing.T) {
		environment := validEnvironment(t)
		environment[promotionRuntimeGateEnvironment] = "enabled"
		var output strings.Builder
		exitCode := run(context.Background(), lookupMap(environment), &output, productionpostgres.Verify, now)
		if exitCode != exitInvalidConfiguration || !strings.Contains(output.String(), productionpostgres.FailureConfigurationInvalid) {
			t.Fatalf("enabled Promotion posture exit/output = %d, %s", exitCode, output.String())
		}
	})
	t.Run("unsafe posture", func(t *testing.T) {
		environment := validEnvironment(t)
		var output strings.Builder
		failure := passedResult()
		failure.Status = productionpostgres.StatusFailed
		failure.Failure = &productionpostgres.Failure{
			Code: productionpostgres.FailureAuditorPostureUnsafe,
			Role: productionpostgres.RoleQualification,
		}
		exitCode := run(
			context.Background(), lookupMap(environment), &output,
			func(context.Context, productionpostgres.Config) (productionpostgres.Result, error) {
				return failure, productionpostgres.ErrUnsafePosture
			}, now,
		)
		if exitCode != exitUnsafePosture || !strings.Contains(output.String(), productionpostgres.FailureAuditorPostureUnsafe) {
			t.Fatalf("unsafe posture exit/output = %d, %s", exitCode, output.String())
		}
	})
	t.Run("operational failure", func(t *testing.T) {
		environment := validEnvironment(t)
		var output strings.Builder
		failure := passedResult()
		failure.Status = productionpostgres.StatusFailed
		failure.Failure = &productionpostgres.Failure{
			Code: productionpostgres.FailureConnectionUnavailable,
			Role: productionpostgres.RoleApplication,
		}
		exitCode := run(
			context.Background(), lookupMap(environment), &output,
			func(context.Context, productionpostgres.Config) (productionpostgres.Result, error) {
				return failure, errors.New("dial postgres://app_login:do-not-print@db.internal/worksflow")
			}, now,
		)
		if exitCode != exitOperationalFailure || strings.Contains(output.String(), "do-not-print") {
			t.Fatalf("operational failure exit/output = %d, %s", exitCode, output.String())
		}
	})
}

func TestLoadSettingsRejectsInsecureFilesAndTimeouts(t *testing.T) {
	environment := validEnvironment(t)
	loaded, err := loadSettings(lookupMap(environment))
	if err != nil || loaded.timeout != 45*time.Second || loaded.config.Schema != "worksflow" {
		t.Fatalf("loadSettings(valid) = %#v, %v", loaded, err)
	}

	for _, timeout := range []string{"", " 30s", "500ms", "6m", "forever"} {
		candidate := validEnvironment(t)
		candidate[timeoutEnvironment] = timeout
		if _, err := loadSettings(lookupMap(candidate)); err == nil {
			t.Fatalf("unsafe timeout %q was accepted", timeout)
		}
	}

	insecure := validEnvironment(t)
	path := insecure[applicationDSNFileEnvironment]
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = loadSettings(lookupMap(insecure))
	if err == nil || strings.Contains(err.Error(), "app-secret") || strings.Contains(err.Error(), path) {
		t.Fatalf("insecure credential failure was not safely redacted: %v", err)
	}

	reused := validEnvironment(t)
	reused[migratorDSNFileEnvironment] = reused[applicationDSNFileEnvironment]
	if _, err := loadSettings(lookupMap(reused)); err == nil {
		t.Fatal("one credential file was accepted for two identities")
	}
}
