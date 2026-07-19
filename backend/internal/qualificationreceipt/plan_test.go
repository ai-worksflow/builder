package qualificationreceipt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputePlanDigestStablePromotionProjectionAndFileClosure(t *testing.T) {
	repository, manifest := writePlanFixture(t)
	before, err := ComputePlanDigest(repository, manifest)
	if err != nil {
		t.Fatalf("compute initial plan: %v", err)
	}
	if before.Digest != "sha256:2b5f7f06d6b32b2e5fe008f8b4b65f3c513fe7fc14713f8f90281dd7dec95f42" {
		t.Fatalf("plan projection digest drifted from the cross-language canonical vector: %s", before.Digest)
	}
	if before.Subject != "worksflow-project-level-ai-constructor" {
		t.Fatalf("plan did not export the canonical manifest subject: %q", before.Subject)
	}

	manifestPath := filepath.Join(repository, filepath.FromSlash(manifest))
	var document map[string]any
	encoded, _ := os.ReadFile(manifestPath)
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	suite := document["suites"].([]any)[0].(map[string]any)
	suite["coverage"] = "partial"
	suite["blockers"] = []any{"historical only"}
	suite["receiptPath"] = "qualification/receipts/run.dsse.json"
	suite["trustPolicyDigest"] = testDigest("ignored-suite-trust")
	document["trustPolicyDigest"] = testDigest("ignored-top-level-trust")
	writeJSONFile(t, manifestPath, document)
	afterPromotion, err := ComputePlanDigest(repository, manifest)
	if err != nil {
		t.Fatalf("compute promoted plan: %v", err)
	}
	if before.Digest != afterPromotion.Digest {
		t.Fatalf("promotion-only fields changed plan digest: %s != %s", before.Digest, afterPromotion.Digest)
	}
	if before.ManifestDigest == afterPromotion.ManifestDigest {
		t.Fatal("exact manifest audit digest did not change")
	}

	supportPath := filepath.Join(repository, "frontend", "playwright.golden.config.ts")
	if err := os.WriteFile(supportPath, []byte("export default { retries: 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	afterSupportDrift, err := ComputePlanDigest(repository, manifest)
	if err != nil {
		t.Fatalf("compute drifted plan: %v", err)
	}
	if afterSupportDrift.Digest == afterPromotion.Digest {
		t.Fatal("qualification support-file drift did not change plan digest")
	}
}

func TestCrossLanguagePlanVector(t *testing.T) {
	root, err := filepath.Abs("testdata/plan-vector")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ComputePlanDigest(root, "qualification/manifest.json")
	if err != nil {
		t.Fatalf("compute shared plan vector: %v", err)
	}
	const expected = "sha256:d754b92262a101668872685952bd7dd12c0a6a0216d777a722f6ea1c46a72aea"
	if plan.Digest != expected {
		t.Fatalf("shared Go/Node plan vector drifted: got %s", plan.Digest)
	}
}

func TestComputePlanDigestRejectsInventoryCoverageAndPathDrift(t *testing.T) {
	repository, manifest := writePlanFixture(t)
	inventoryPath := filepath.Join(repository, "qualification", "test-inventory.json")
	writeJSONFile(t, inventoryPath, map[string]any{
		"schemaVersion":    TestInventorySchemaV2,
		"criterionSources": planFixtureCriterionSources(),
		"cases": []any{map[string]any{
			"caseId": "QG-GOLDEN-001", "suiteId": "golden-external", "requirementIds": []string{"AIC-E2E-003"},
			"contractCriterionIds": []string{"AC-GOLDEN-001"},
			"file":                 "frontend/tests/not-in-suite.spec.ts", "title": "QG-GOLDEN-001 Golden", "mode": "qualification",
		}},
	})
	if _, err := ComputePlanDigest(repository, manifest); err == nil {
		t.Fatal("expected test inventory path mismatch to fail")
	}

	writeJSONFile(t, inventoryPath, map[string]any{
		"schemaVersion":    TestInventorySchemaV2,
		"criterionSources": planFixtureCriterionSources(),
		"cases": []any{map[string]any{
			"caseId": "QG-GOLDEN-001", "suiteId": "golden-external", "requirementIds": []string{"AIC-E2E-003"},
			"contractCriterionIds": []string{"AC-GOLDEN-001"},
			"file":                 "frontend/tests/golden-reference.spec.ts", "title": "QG-GOLDEN-001 Golden A", "mode": "qualification",
		}},
	})
	if _, err := ComputePlanDigest(repository, manifest); err == nil {
		t.Fatal("expected uncovered AIC-E2E-004 to fail")
	}
}

func TestComputePlanDigestRejectsDeclaredTestPathWithoutQualificationCase(t *testing.T) {
	repository, manifest := writePlanFixture(t)
	secondPath := filepath.Join(repository, "frontend", "tests", "golden-second.spec.ts")
	if err := os.WriteFile(secondPath, []byte("QG-GOLDEN-003 Golden C\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(repository, filepath.FromSlash(manifest))
	var document map[string]any
	encoded, err := os.ReadFile(manifestPath)
	if err != nil || json.Unmarshal(encoded, &document) != nil {
		t.Fatal("read plan fixture manifest")
	}
	document["suites"].([]any)[0].(map[string]any)["testPaths"] = []string{
		"frontend/tests/golden-reference.spec.ts", "frontend/tests/golden-second.spec.ts",
	}
	writeJSONFile(t, manifestPath, document)
	if _, err := ComputePlanDigest(repository, manifest); err == nil {
		t.Fatal("declared suite test path with no qualification inventory case was accepted")
	}
}

func TestComputePlanDigestBindsPostRunVerifierWithoutInventingPlaywrightCase(t *testing.T) {
	repository, manifest := writePlanFixture(t)
	verificationContract := "qualification/artifact-hygiene.json"
	verificationAbsolute := filepath.Join(repository, filepath.FromSlash(verificationContract))
	writeJSONFile(t, verificationAbsolute, map[string]any{
		"schemaVersion": "worksflow-post-run-verification-contract/v1",
		"suiteId":       "artifact-hygiene",
	})
	architecturePath := filepath.Join(repository, "docs", "architecture.md")
	if err := os.WriteFile(architecturePath, []byte("AIC-E2E-003 AIC-E2E-004 FQP-E2E-016\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(repository, filepath.FromSlash(manifest))
	var document map[string]any
	encoded, err := os.ReadFile(manifestPath)
	if err != nil || json.Unmarshal(encoded, &document) != nil {
		t.Fatal("read plan fixture manifest")
	}
	document["qualificationSupportPaths"] = append(
		document["qualificationSupportPaths"].([]any), verificationContract,
	)
	document["suites"] = append(document["suites"].([]any), map[string]any{
		"id": "artifact-hygiene", "mode": "external-qualification", "executionKind": "post-run-verifier",
		"status": "not-qualified", "coverage": "external-complete", "qualificationGroup": "golden",
		"qualificationCommand":     "pnpm qualification:golden",
		"verificationContractPath": verificationContract,
		"requirementIds":           []string{"FQP-E2E-016"},
		"requiredArtifacts":        []string{"credential-set-revocation-receipt"},
		"blockers":                 []string{"not run"},
	})
	writeJSONFile(t, manifestPath, document)

	before, err := ComputePlanDigest(repository, manifest)
	if err != nil {
		t.Fatalf("compute plan with post-run verifier: %v", err)
	}
	if len(before.ExternalSuites) != 2 || len(before.TestCases) != 2 {
		t.Fatalf("post-run verifier should be receipt-bound without inventing an inventory case: suites=%d cases=%d", len(before.ExternalSuites), len(before.TestCases))
	}
	writeJSONFile(t, verificationAbsolute, map[string]any{
		"schemaVersion": "worksflow-post-run-verification-contract/v1",
		"suiteId":       "artifact-hygiene",
		"changed":       true,
	})
	afterContractDrift, err := ComputePlanDigest(repository, manifest)
	if err != nil {
		t.Fatalf("compute plan after verifier contract drift: %v", err)
	}
	if before.Digest == afterContractDrift.Digest {
		t.Fatal("post-run verifier contract drift did not change the plan digest")
	}

	inventoryPath := filepath.Join(repository, "qualification", "test-inventory.json")
	var inventory map[string]any
	encoded, err = os.ReadFile(inventoryPath)
	if err != nil || json.Unmarshal(encoded, &inventory) != nil {
		t.Fatal("read plan fixture inventory")
	}
	inventory["cases"].([]any)[0].(map[string]any)["suiteId"] = "artifact-hygiene"
	inventory["cases"].([]any)[0].(map[string]any)["contractCriterionIds"] = []any{}
	writeJSONFile(t, inventoryPath, inventory)
	if _, err := ComputePlanDigest(repository, manifest); err == nil || !strings.Contains(err.Error(), "non-Playwright") {
		t.Fatalf("inventory case for a post-run verifier was not rejected: %v", err)
	}
}

func TestComputePlanDigestRejectsContractCriterionDriftAndIncompleteClosure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "foreign criterion",
			mutate: func(inventory map[string]any) {
				inventory["cases"].([]any)[0].(map[string]any)["contractCriterionIds"] = []any{"AC-GOLDEN-999"}
			},
		},
		{
			name: "uncovered criterion",
			mutate: func(inventory map[string]any) {
				inventory["cases"].([]any)[1].(map[string]any)["contractCriterionIds"] = []any{}
			},
		},
		{
			name: "criterion source identity drift",
			mutate: func(inventory map[string]any) {
				inventory["criterionSources"].([]any)[0].(map[string]any)["applicationId"] = "other-application-v1"
			},
		},
		{
			name: "manifest-bound source removed with all case mappings",
			mutate: func(inventory map[string]any) {
				inventory["criterionSources"] = []any{}
				for _, entry := range inventory["cases"].([]any) {
					entry.(map[string]any)["contractCriterionIds"] = []any{}
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, manifest := writePlanFixture(t)
			inventoryPath := filepath.Join(repository, "qualification", "test-inventory.json")
			var inventory map[string]any
			encoded, err := os.ReadFile(inventoryPath)
			if err != nil || json.Unmarshal(encoded, &inventory) != nil {
				t.Fatal("read plan fixture inventory")
			}
			test.mutate(inventory)
			writeJSONFile(t, inventoryPath, inventory)
			if _, err := ComputePlanDigest(repository, manifest); err == nil {
				t.Fatal("contract criterion drift was accepted")
			}
		})
	}
}

func TestComputePlanDigestRejectsNullFailClosedPolicyField(t *testing.T) {
	repository, manifest := writePlanFixture(t)
	manifestPath := filepath.Join(repository, filepath.FromSlash(manifest))
	var document map[string]any
	encoded, err := os.ReadFile(manifestPath)
	if err != nil || json.Unmarshal(encoded, &document) != nil {
		t.Fatal("read plan fixture manifest")
	}
	document["policy"].(map[string]any)["allowMocks"] = nil
	writeJSONFile(t, manifestPath, document)
	if _, err := ComputePlanDigest(repository, manifest); err == nil {
		t.Fatal("null fail-closed policy field was accepted as false")
	}
}

func TestComputePlanDigestRejectsUnknownSuiteFieldsAndOversizedInventory(t *testing.T) {
	t.Run("unknown suite field", func(t *testing.T) {
		repository, manifest := writePlanFixture(t)
		manifestPath := filepath.Join(repository, filepath.FromSlash(manifest))
		var document map[string]any
		encoded, _ := os.ReadFile(manifestPath)
		if err := json.Unmarshal(encoded, &document); err != nil {
			t.Fatal(err)
		}
		document["suites"].([]any)[0].(map[string]any)["runtimeImage"] = "attacker:latest"
		writeJSONFile(t, manifestPath, document)
		if _, err := ComputePlanDigest(repository, manifest); err == nil {
			t.Fatal("unknown execution-affecting suite field was ignored")
		}
	})
	t.Run("absolute suite test path", func(t *testing.T) {
		repository, manifest := writePlanFixture(t)
		manifestPath := filepath.Join(repository, filepath.FromSlash(manifest))
		var document map[string]any
		encoded, _ := os.ReadFile(manifestPath)
		if err := json.Unmarshal(encoded, &document); err != nil {
			t.Fatal(err)
		}
		document["suites"].([]any)[0].(map[string]any)["testPaths"] = []string{"/tmp/attacker.spec.ts"}
		writeJSONFile(t, manifestPath, document)
		if _, err := ComputePlanDigest(repository, manifest); err == nil {
			t.Fatal("absolute suite test path was accepted")
		}
	})
	t.Run("external-complete test path outside support closure", func(t *testing.T) {
		repository, manifest := writePlanFixture(t)
		manifestPath := filepath.Join(repository, filepath.FromSlash(manifest))
		var document map[string]any
		encoded, _ := os.ReadFile(manifestPath)
		if err := json.Unmarshal(encoded, &document); err != nil {
			t.Fatal(err)
		}
		filtered := make([]any, 0)
		for _, raw := range document["qualificationSupportPaths"].([]any) {
			if raw.(string) != "frontend/tests/golden-reference.spec.ts" {
				filtered = append(filtered, raw)
			}
		}
		document["qualificationSupportPaths"] = filtered
		writeJSONFile(t, manifestPath, document)
		if _, err := ComputePlanDigest(repository, manifest); err == nil {
			t.Fatal("external-complete executable outside the support-file hash closure was accepted")
		}
	})
	t.Run("more than 512 cases", func(t *testing.T) {
		repository, manifest := writePlanFixture(t)
		cases := make([]any, maxTestInventoryCases+1)
		for index := range cases {
			cases[index] = map[string]any{
				"caseId": "QG-GOLDEN-001", "suiteId": "golden-external", "requirementIds": []string{"AIC-E2E-003"},
				"contractCriterionIds": []string{"AC-GOLDEN-001"},
				"file":                 "frontend/tests/golden-reference.spec.ts", "title": "QG-GOLDEN-001 Golden A", "mode": "qualification",
			}
		}
		writeJSONFile(t, filepath.Join(repository, "qualification", "test-inventory.json"), map[string]any{
			"schemaVersion": TestInventorySchemaV2, "criterionSources": planFixtureCriterionSources(), "cases": cases,
		})
		if _, err := ComputePlanDigest(repository, manifest); err == nil {
			t.Fatal("oversized qualification inventory was accepted")
		}
	})
}

func writePlanFixture(t *testing.T) (string, string) {
	t.Helper()
	repository := t.TempDir()
	files := map[string]string{
		"docs/architecture.md":                          "AIC-E2E-003 AIC-E2E-004\n",
		"contracts/reference-acceptance-criteria.json":  `{"schemaVersion":"reference-acceptance-criteria/v1","applicationId":"plan-vector-v1","criteria":[{"id":"AC-GOLDEN-001","requirementIds":["REQ-GOLDEN-A"],"statement":"Golden A is externally verified."},{"id":"AC-GOLDEN-002","requirementIds":["REQ-GOLDEN-B"],"statement":"Golden B is externally verified."}]}`,
		"frontend/tests/golden-reference.spec.ts":       "QG-GOLDEN-001 Golden A\nQG-GOLDEN-002 Golden B\n",
		"frontend/tests/qualification-fixture.ts":       "controlled fixture\n",
		"frontend/playwright.golden.config.ts":          "export default { retries: 0 }\n",
		"frontend/scripts/check-golden-environment.mjs": "strict preflight\n",
	}
	for name, contents := range files {
		absolute := filepath.Join(repository, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inventoryPath := filepath.Join(repository, "qualification", "test-inventory.json")
	if err := os.MkdirAll(filepath.Dir(inventoryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, inventoryPath, map[string]any{
		"schemaVersion":    TestInventorySchemaV2,
		"criterionSources": planFixtureCriterionSources(),
		"cases": []any{
			map[string]any{"caseId": "QG-GOLDEN-001", "suiteId": "golden-external", "requirementIds": []string{"AIC-E2E-003"}, "contractCriterionIds": []string{"AC-GOLDEN-001"}, "file": "frontend/tests/golden-reference.spec.ts", "title": "QG-GOLDEN-001 Golden A", "mode": "qualification"},
			map[string]any{"caseId": "QG-GOLDEN-002", "suiteId": "golden-external", "requirementIds": []string{"AIC-E2E-004"}, "contractCriterionIds": []string{"AC-GOLDEN-002"}, "file": "frontend/tests/golden-reference.spec.ts", "title": "QG-GOLDEN-002 Golden B", "mode": "qualification"},
		},
	})
	manifestPath := filepath.Join(repository, "qualification", "manifest.json")
	writeJSONFile(t, manifestPath, map[string]any{
		"schemaVersion":   QualificationManifestSchemaV1,
		"subject":         "worksflow-project-level-ai-constructor",
		"sourceDocuments": []string{"docs/architecture.md"},
		"qualificationSupportPaths": []string{
			"contracts/reference-acceptance-criteria.json",
			"frontend/playwright.golden.config.ts",
			"frontend/scripts/check-golden-environment.mjs",
			"frontend/tests/golden-reference.spec.ts",
			"frontend/tests/qualification-fixture.ts",
			"qualification/test-inventory.json",
		},
		"policy": map[string]any{
			"stageExitRequiresExternalQualification": true,
			"allowSkippedTests":                      false, "allowMocks": false, "allowMutableRuntimeImages": false,
			"credentialBearingArtifacts":                "restricted-encrypted-until-revocation",
			"passingInternalSuitesAreStageExitEvidence": false,
		},
		"suites": []any{map[string]any{
			"id": "golden-external", "mode": "external-qualification", "executionKind": "playwright",
			"status": "not-qualified", "coverage": "external-complete",
			"qualificationGroup":   "golden",
			"requirementIds":       []string{"AIC-E2E-003", "AIC-E2E-004"},
			"qualificationCommand": "pnpm qualification:golden",
			"criterionSource": map[string]any{
				"path": "contracts/reference-acceptance-criteria.json", "schemaVersion": ReferenceCriteriaSchemaV1,
				"applicationId": "plan-vector-v1",
			},
			"testPaths":         []string{"frontend/tests/golden-reference.spec.ts"},
			"requiredArtifacts": []string{"browser-video", "credential-safe-trace"},
			"blockers":          []string{"not run"},
		}},
	})
	return repository, "qualification/manifest.json"
}

func planFixtureCriterionSources() []any {
	return []any{map[string]any{
		"suiteId": "golden-external", "path": "contracts/reference-acceptance-criteria.json",
		"schemaVersion": ReferenceCriteriaSchemaV1, "applicationId": "plan-vector-v1",
	}}
}

func writeJSONFile(t *testing.T, filePath string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testDigest(label string) string { return sha256Digest([]byte(label)) }
