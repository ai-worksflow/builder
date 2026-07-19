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
	applicationDSNFileEnvironment   = "WORKSFLOW_PRODUCTION_POSTGRES_APP_DSN_FILE"
	migratorDSNFileEnvironment      = "WORKSFLOW_PRODUCTION_POSTGRES_MIGRATOR_DSN_FILE"
	qualificationDSNFileEnvironment = "WORKSFLOW_PRODUCTION_POSTGRES_QUALIFICATION_DSN_FILE"
	promotionDSNFileEnvironment     = "WORKSFLOW_PRODUCTION_POSTGRES_PROMOTION_DSN_FILE"
	schemaEnvironment               = "WORKSFLOW_PRODUCTION_POSTGRES_SCHEMA"
	timeoutEnvironment              = "WORKSFLOW_PRODUCTION_POSTGRES_POSTURE_TIMEOUT"

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
	paths := []string{applicationPath, migratorPath, qualificationPath, promotionPath}
	seenPaths := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if _, exists := seenPaths[path]; exists {
			return settings{}, errors.New("four separate credential files are required")
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
	schema, present := lookup(schemaEnvironment)
	if !present || schema == "" || schema != strings.TrimSpace(schema) || strings.ContainsAny(schema, "\r\n\x00") {
		return settings{}, fmt.Errorf("%s is required", schemaEnvironment)
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
			ApplicationDSN:   application,
			MigratorDSN:      migrator,
			QualificationDSN: qualification,
			PromotionDSN:     promotion,
			Schema:           schema,
		},
		timeout: timeout,
	}, nil
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
			{Kind: productionpostgres.RolePromotion, Responsibility: "dedicated qualification-promotion consume and pending-handoff reader", Status: productionpostgres.StatusNotChecked},
		},
		ExcludedClaims: []string{
			"external-qualification-receipt",
			"gc-scheduler-qualification",
			"promotion-authority",
		},
		Failure: &productionpostgres.Failure{Code: code, Role: role},
	}
}
