package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/worksflow/builder/backend/internal/productionpostgres"
)

const (
	applicationDSNFileEnvironment                = "WORKSFLOW_PRODUCTION_POSTGRES_APP_DSN_FILE"
	migratorDSNFileEnvironment                   = "WORKSFLOW_PRODUCTION_POSTGRES_MIGRATOR_DSN_FILE"
	qualificationDSNFileEnvironment              = "WORKSFLOW_PRODUCTION_POSTGRES_QUALIFICATION_DSN_FILE"
	promotionDSNFileEnvironment                  = "WORKSFLOW_PRODUCTION_POSTGRES_PROMOTION_DSN_FILE"
	promotionSessionAffinityEnvironment          = "WORKSFLOW_PRODUCTION_POSTGRES_PROMOTION_SESSION_AFFINITY"
	promotionRuntimeGateEnvironment              = "WORKSFLOW_PRODUCTION_POSTGRES_PROMOTION_RUNTIME_GATE"
	policyDSNFileEnvironment                     = "WORKSFLOW_PRODUCTION_POSTGRES_POLICY_DSN_FILE"
	inputPrecommitDSNFileEnvironment             = "WORKSFLOW_PRODUCTION_POSTGRES_INPUT_PRECOMMIT_DSN_FILE"
	inputPrecommitSessionAffinityEnvironment     = "WORKSFLOW_PRODUCTION_POSTGRES_INPUT_PRECOMMIT_SESSION_AFFINITY"
	sourceVerifierDSNFileEnvironment             = "WORKSFLOW_PRODUCTION_POSTGRES_SOURCE_VERIFIER_DSN_FILE"
	sourceVerifierSessionAffinityEnvironment     = "WORKSFLOW_PRODUCTION_POSTGRES_SOURCE_VERIFIER_SESSION_AFFINITY"
	credentialResolverDSNFileEnvironment         = "WORKSFLOW_PRODUCTION_POSTGRES_CREDENTIAL_RESOLVER_DSN_FILE"
	credentialResolverSessionAffinityEnvironment = "WORKSFLOW_PRODUCTION_POSTGRES_CREDENTIAL_RESOLVER_SESSION_AFFINITY"
	handoffDSNFileEnvironment                    = "WORKSFLOW_PRODUCTION_POSTGRES_HANDOFF_DSN_FILE"
	handoffSessionAffinityEnvironment            = "WORKSFLOW_PRODUCTION_POSTGRES_HANDOFF_SESSION_AFFINITY"
	schemaEnvironment                            = "WORKSFLOW_PRODUCTION_POSTGRES_SCHEMA"
	timeoutEnvironment                           = "WORKSFLOW_PRODUCTION_POSTGRES_POSTURE_TIMEOUT"

	exitPassed               = 0
	exitInvalidConfiguration = 2
	exitUnsafePosture        = 3
	exitOperationalFailure   = 4

	defaultTimeout = 30 * time.Second
	minimumTimeout = time.Second
	maximumTimeout = 5 * time.Minute
)

type settings struct {
	config  productionpostgres.Config
	timeout time.Duration
}

type verifier func(context.Context, productionpostgres.Config) (productionpostgres.Result, error)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.LookupEnv, os.Stdout, productionpostgres.Verify, time.Now))
}

func run(
	parent context.Context,
	lookup func(string) (string, bool),
	output io.Writer,
	verify verifier,
	now func() time.Time,
) int {
	if parent == nil || lookup == nil || output == nil || verify == nil || now == nil {
		writeResult(output, failureResult(now, "", productionpostgres.FailureConfigurationInvalid, ""))
		return exitInvalidConfiguration
	}
	loaded, err := loadSettings(lookup)
	if err != nil {
		writeResult(output, failureResult(now, "", productionpostgres.FailureConfigurationInvalid, ""))
		return exitInvalidConfiguration
	}

	ctx, cancel := context.WithTimeout(parent, loaded.timeout)
	defer cancel()
	result, err := verify(ctx, loaded.config)
	if err == nil {
		if result.Status != productionpostgres.StatusPassed || !writeResult(output, result) {
			return exitOperationalFailure
		}
		return exitPassed
	}
	if result.SchemaVersion == "" {
		code, role := productionpostgres.FailureDetails(err)
		result = failureResult(now, loaded.config.Schema, code, role)
	}
	if !writeResult(output, result) {
		return exitOperationalFailure
	}
	switch {
	case errors.Is(err, productionpostgres.ErrInvalidConfiguration):
		return exitInvalidConfiguration
	case errors.Is(err, productionpostgres.ErrUnsafePosture):
		return exitUnsafePosture
	default:
		return exitOperationalFailure
	}
}

func loadSettings(lookup func(string) (string, bool)) (settings, error) {
	if lookup == nil {
		return settings{}, errors.New("environment lookup is required")
	}
	applicationPath, err := loadCredentialPath(lookup, applicationDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	migratorPath, err := loadCredentialPath(lookup, migratorDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	qualificationPath, err := loadCredentialPath(lookup, qualificationDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	promotionPath, err := loadCredentialPath(lookup, promotionDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	policyPath, err := loadCredentialPath(lookup, policyDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	inputPrecommitPath, err := loadCredentialPath(lookup, inputPrecommitDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	sourceVerifierPath, err := loadCredentialPath(lookup, sourceVerifierDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	credentialResolverPath, err := loadCredentialPath(lookup, credentialResolverDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	handoffPath, err := loadCredentialPath(lookup, handoffDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	paths := []string{
		applicationPath,
		migratorPath,
		qualificationPath,
		promotionPath,
		policyPath,
		inputPrecommitPath,
		sourceVerifierPath,
		credentialResolverPath,
		handoffPath,
	}
	seenPaths := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if _, exists := seenPaths[path]; exists {
			return settings{}, errors.New("nine separate credential files are required")
		}
		seenPaths[path] = struct{}{}
	}
	application, err := loadCredential(applicationPath, applicationDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	migrator, err := loadCredential(migratorPath, migratorDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	qualification, err := loadCredential(qualificationPath, qualificationDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	promotion, err := loadCredential(promotionPath, promotionDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	policy, err := loadCredential(policyPath, policyDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	inputPrecommit, err := loadCredential(inputPrecommitPath, inputPrecommitDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	sourceVerifier, err := loadCredential(sourceVerifierPath, sourceVerifierDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	credentialResolver, err := loadCredential(credentialResolverPath, credentialResolverDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	handoff, err := loadCredential(handoffPath, handoffDSNFileEnvironment)
	if err != nil {
		return settings{}, err
	}
	schema, present := lookup(schemaEnvironment)
	if !present || schema == "" || schema != strings.TrimSpace(schema) || strings.ContainsAny(schema, "\r\n\x00") {
		return settings{}, fmt.Errorf("%s is required", schemaEnvironment)
	}
	promotionSessionAffinity, present := lookup(promotionSessionAffinityEnvironment)
	if !present || promotionSessionAffinity == "" || promotionSessionAffinity != strings.TrimSpace(promotionSessionAffinity) ||
		strings.ContainsAny(promotionSessionAffinity, "\r\n\x00") {
		return settings{}, fmt.Errorf("%s is required", promotionSessionAffinityEnvironment)
	}
	if promotionSessionAffinity != string(productionpostgres.PromotionSessionAffinityDirect) &&
		promotionSessionAffinity != string(productionpostgres.PromotionSessionAffinitySessionPool) {
		return settings{}, fmt.Errorf("%s must be direct or session-pool", promotionSessionAffinityEnvironment)
	}
	inputPrecommitSessionAffinity, err := loadSessionAffinity(lookup, inputPrecommitSessionAffinityEnvironment)
	if err != nil {
		return settings{}, err
	}
	sourceVerifierSessionAffinity, err := loadSessionAffinity(lookup, sourceVerifierSessionAffinityEnvironment)
	if err != nil {
		return settings{}, err
	}
	credentialResolverSessionAffinity, err := loadSessionAffinity(lookup, credentialResolverSessionAffinityEnvironment)
	if err != nil {
		return settings{}, err
	}
	handoffSessionAffinity, err := loadSessionAffinity(lookup, handoffSessionAffinityEnvironment)
	if err != nil {
		return settings{}, err
	}
	promotionRuntimeGate, present := lookup(promotionRuntimeGateEnvironment)
	if !present || promotionRuntimeGate == "" || promotionRuntimeGate != strings.TrimSpace(promotionRuntimeGate) ||
		strings.ContainsAny(promotionRuntimeGate, "\r\n\x00") {
		return settings{}, fmt.Errorf("%s is required", promotionRuntimeGateEnvironment)
	}
	if promotionRuntimeGate != productionpostgres.PromotionRuntimeGateDisabledPendingInputPrecommitAuthorityCanary {
		return settings{}, fmt.Errorf("%s must keep Promotion disabled", promotionRuntimeGateEnvironment)
	}
	timeout := defaultTimeout
	if raw, configured := lookup(timeoutEnvironment); configured {
		if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "\r\n\x00") {
			return settings{}, fmt.Errorf("%s must be a canonical duration", timeoutEnvironment)
		}
		timeout, err = time.ParseDuration(raw)
		if err != nil || timeout < minimumTimeout || timeout > maximumTimeout {
			return settings{}, fmt.Errorf("%s must be between %s and %s", timeoutEnvironment, minimumTimeout, maximumTimeout)
		}
	}
	return settings{
		config: productionpostgres.Config{
			ApplicationDSN:                    application,
			MigratorDSN:                       migrator,
			QualificationDSN:                  qualification,
			PromotionDSN:                      promotion,
			PromotionSessionAffinity:          productionpostgres.PromotionSessionAffinity(promotionSessionAffinity),
			PromotionRuntimeGate:              promotionRuntimeGate,
			PolicyDSN:                         policy,
			InputPrecommitDSN:                 inputPrecommit,
			InputPrecommitSessionAffinity:     inputPrecommitSessionAffinity,
			SourceVerifierDSN:                 sourceVerifier,
			SourceVerifierSessionAffinity:     sourceVerifierSessionAffinity,
			CredentialResolverDSN:             credentialResolver,
			CredentialResolverSessionAffinity: credentialResolverSessionAffinity,
			HandoffDSN:                        handoff,
			HandoffSessionAffinity:            handoffSessionAffinity,
			Schema:                            schema,
		},
		timeout: timeout,
	}, nil
}

func loadSessionAffinity(
	lookup func(string) (string, bool),
	environment string,
) (productionpostgres.PromotionSessionAffinity, error) {
	value, present := lookup(environment)
	if !present || value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("%s is required", environment)
	}
	if value != string(productionpostgres.PromotionSessionAffinityDirect) &&
		value != string(productionpostgres.PromotionSessionAffinitySessionPool) {
		return "", fmt.Errorf("%s must be direct or session-pool", environment)
	}
	return productionpostgres.PromotionSessionAffinity(value), nil
}

func loadCredentialPath(lookup func(string) (string, bool), environment string) (string, error) {
	path, present := lookup(environment)
	if !present || path == "" || path != strings.TrimSpace(path) || strings.ContainsAny(path, "\r\n\x00") {
		return "", fmt.Errorf("%s is required", environment)
	}
	return path, nil
}

func loadCredential(path, environment string) (string, error) {
	dsn, err := productionpostgres.ReadCredentialFile(path)
	if err != nil {
		// Do not wrap the path or the underlying filesystem/DSN error: neither
		// belongs in operator logs or the structured result.
		return "", fmt.Errorf("%s is invalid", environment)
	}
	return dsn, nil
}

func writeResult(output io.Writer, result productionpostgres.Result) bool {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result) == nil
}

func failureResult(
	now func() time.Time,
	schema string,
	code string,
	role productionpostgres.RoleKind,
) productionpostgres.Result {
	checkedAt := time.Now().UTC()
	if now != nil {
		checkedAt = now().UTC()
	}
	return productionpostgres.Result{
		SchemaVersion: productionpostgres.ResultSchemaVersion,
		Status:        productionpostgres.StatusFailed,
		CheckedAt:     checkedAt.Format(time.RFC3339Nano),
		Schema:        schema,
		EvidenceClass: "standalone-point-in-time-posture-check",
		Roles: []productionpostgres.RoleResult{
			{Kind: productionpostgres.RoleApplication, Responsibility: "API runtime least-privilege data plane", Status: productionpostgres.StatusNotChecked},
			{Kind: productionpostgres.RoleMigrator, Responsibility: "one-shot migration-owner schema authority", Status: productionpostgres.StatusNotChecked},
			{Kind: productionpostgres.RoleQualification, Responsibility: "read-only catalog auditor with no trusted-schema data or function access", Status: productionpostgres.StatusNotChecked},
			{Kind: productionpostgres.RolePromotion, Responsibility: "disabled Promotion-v2 consumer with no handoff-resolver or data-plane table authority", Status: productionpostgres.StatusNotChecked},
			{Kind: productionpostgres.RolePolicy, Responsibility: "dedicated qualification-policy issuer and resolver", Status: productionpostgres.StatusNotChecked},
			{Kind: productionpostgres.RoleInputPrecommit, Responsibility: "disabled qualification input-precommit authority issuer and resolver", Status: productionpostgres.StatusNotChecked},
			{Kind: productionpostgres.RoleSourceVerifier, Responsibility: "disabled qualification source-verifier receipt admission", Status: productionpostgres.StatusNotChecked},
			{Kind: productionpostgres.RoleCredentialResolver, Responsibility: "disabled qualification credential-resolver receipt admission", Status: productionpostgres.StatusNotChecked},
			{Kind: productionpostgres.RoleHandoff, Responsibility: "disabled private qualification handoff completion and inspection", Status: productionpostgres.StatusNotChecked},
		},
		ExcludedClaims: []string{
			"external-qualification-receipt",
			"gc-scheduler-qualification",
			"promotion-authority",
			"promotion-runtime-activation",
			"input-precommit-authority-canary",
		},
		Failure: &productionpostgres.Failure{Code: code, Role: role},
	}
}
