package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/productionpostgres"
)

// This canary is opt-in and produces no qualification or promotion receipt.
// It uses the same nine permission-checked credential files as the binary.
func TestProductionPostgresPostureExternal(t *testing.T) {
	for _, key := range []string{
		applicationDSNFileEnvironment,
		migratorDSNFileEnvironment,
		qualificationDSNFileEnvironment,
		promotionDSNFileEnvironment,
		promotionSessionAffinityEnvironment,
		promotionRuntimeGateEnvironment,
		policyDSNFileEnvironment,
		inputPrecommitDSNFileEnvironment,
		inputPrecommitSessionAffinityEnvironment,
		sourceVerifierDSNFileEnvironment,
		sourceVerifierSessionAffinityEnvironment,
		credentialResolverDSNFileEnvironment,
		credentialResolverSessionAffinityEnvironment,
		handoffDSNFileEnvironment,
		handoffSessionAffinityEnvironment,
		schemaEnvironment,
	} {
		if _, present := os.LookupEnv(key); !present {
			t.Skip("production PostgreSQL posture credential files are not configured")
		}
	}
	loaded, err := loadSettings(os.LookupEnv)
	if err != nil {
		t.Fatalf("load external posture settings: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), minDuration(loaded.timeout, 2*time.Minute))
	defer cancel()
	result, err := productionpostgres.Verify(ctx, loaded.config)
	if err != nil {
		code, role := productionpostgres.FailureDetails(err)
		t.Fatalf("external posture failed safely: code=%s role=%s", code, role)
	}
	if result.Status != productionpostgres.StatusPassed {
		t.Fatalf("external posture returned status %q", result.Status)
	}
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
