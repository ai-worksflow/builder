package verification

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/templates"
)

func TestPlanCompilerProducesDeterministicExactRequiredChecks(t *testing.T) {
	input := validCandidatePlanInput()
	compiled, err := (PlanCompiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if !exactSHA256(compiled.PlanHash) || len(compiled.Content.Checks) != 2 ||
		compiled.Content.Checks[0].ID != "oracle-contract" || !compiled.Content.Checks[0].Required ||
		compiled.Content.Checks[0].WorkingDirectory != "apps/api" ||
		compiled.Content.Checks[0].VerifierImageDigest != input.Profile.VerifierImages[1].Image ||
		compiled.Content.Checks[1].ID != "oracle-ui" || compiled.Content.Checks[1].Required ||
		compiled.Content.Checks[1].WorkingDirectory != "apps/web/src" {
		t.Fatalf("compiled Plan lost exact check facts: %#v", compiled)
	}
	if len(compiled.Content.TemplateReleases) != 2 ||
		compiled.Content.TemplateReleases[0].Role != "api" ||
		compiled.Content.TemplateReleases[1].Role != "web" ||
		len(compiled.Content.Dependencies) != 2 ||
		compiled.Content.Dependencies[0].ID != "dependency-api" ||
		compiled.Content.Dependencies[0].Ecosystem != "python" ||
		compiled.Content.Dependencies[1].ID != "dependency-web" ||
		compiled.Content.Dependencies[1].Ecosystem != "node" ||
		!exactSHA256(compiled.Content.Dependencies[0].CacheKey) ||
		len(compiled.Content.Obligations) != 2 ||
		compiled.Content.Obligations[0].ID != "OBL-contract" {
		t.Fatalf("compiled Plan projections are not stable: %#v", compiled.Content)
	}
	parsed, err := ParsePlan(compiled.Content, compiled.PlanHash)
	if err != nil || parsed.PlanHash != compiled.PlanHash {
		t.Fatalf("parse exact Plan = %#v, %v", parsed, err)
	}

	reordered := input
	reordered.TemplateReleases[0], reordered.TemplateReleases[1] =
		reordered.TemplateReleases[1], reordered.TemplateReleases[0]
	reordered.Oracles[0], reordered.Oracles[1] = reordered.Oracles[1], reordered.Oracles[0]
	reordered.Obligations[0], reordered.Obligations[1] =
		reordered.Obligations[1], reordered.Obligations[0]
	reordered.Profile.VerifierImages[0], reordered.Profile.VerifierImages[1] =
		reordered.Profile.VerifierImages[1], reordered.Profile.VerifierImages[0]
	reorderedCompiled, err := (PlanCompiler{}).Compile(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if reorderedCompiled.PlanHash != compiled.PlanHash {
		t.Fatalf("semantic input order changed Plan hash: %s != %s", reorderedCompiled.PlanHash, compiled.PlanHash)
	}
}

func TestPlanCompilerSupportsGoAPIReleaseDependencies(t *testing.T) {
	input := validCandidatePlanInput()
	input.Profile.VerifierImages[1] = VerifierImage{
		Role: "go", Image: "registry.example/quality-go@sha256:" + strings.Repeat("b", 64),
	}
	input.Profile.CommandImageRoles["api"] = "go"
	input.TemplateReleases[0].Manifest.Toolchains = []templates.Toolchain{{
		Name: "go", Version: "1.25.12",
		Image: "registry.example/go@sha256:" + strings.Repeat("c", 64),
	}}
	input.TemplateReleases[0].Manifest.Lockfiles = []templates.Lockfile{{
		Path: "go.sum", Digest: hashFixture("plan-api-go-sum"), Registry: "https://proxy.golang.org",
	}}
	input.TemplateReleases[0].Manifest.Commands["test-contract"] = templates.Command{
		WorkingDirectory: ".", Argv: []string{"go", "test", "./..."},
	}

	compiled, err := (PlanCompiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	dependency := compiled.Content.Dependencies[0]
	if dependency.ServiceID != "api" || dependency.Ecosystem != "go" ||
		!equalStrings(dependency.ManifestPaths, []string{"apps/api/go.mod"}) ||
		!equalStrings(dependency.ResolverArgv, []string{"go", "mod", "download"}) {
		t.Fatalf("compiled Go dependency = %#v", dependency)
	}
	if _, err := ParsePlan(compiled.Content, compiled.PlanHash); err != nil {
		t.Fatalf("parse Go dependency plan: %v", err)
	}
}

func TestPlanCompilerFailsClosedForAmbiguousOrUnqualifiedCommands(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CompileCandidatePlanInput)
	}{
		{name: "duplicate command", mutate: func(input *CompileCandidatePlanInput) {
			input.TemplateReleases[1].Manifest.Commands["test-contract"] = templates.Command{
				WorkingDirectory: ".", Argv: []string{"pnpm", "test"},
			}
		}},
		{name: "mutable image", mutate: func(input *CompileCandidatePlanInput) {
			input.Profile.VerifierImages[0].Image = "registry.example/quality-node:latest"
		}},
		{name: "mutable dependency toolchain", mutate: func(input *CompileCandidatePlanInput) {
			input.TemplateReleases[0].Manifest.Toolchains[0].Image = "python:latest"
		}},
		{name: "unqualified dependency registry", mutate: func(input *CompileCandidatePlanInput) {
			input.TemplateReleases[1].Manifest.Lockfiles[0].Registry = "https://user:secret@registry.npmjs.org"
		}},
		{name: "missing resolver network", mutate: func(input *CompileCandidatePlanInput) {
			delete(input.Profile.NetworkPolicy, "dependencyResolver")
		}},
		{name: "mutable postgres service image", mutate: func(input *CompileCandidatePlanInput) {
			input.Profile.NetworkPolicy = map[string]any{"postgres": map[string]any{
				"image": "postgres:latest", "database": "verification",
				"user": "verification", "runtimeUser": "999:999",
			}}
		}},
		{name: "runtime service without isolated database network", mutate: func(input *CompileCandidatePlanInput) {
			input.Profile.NetworkPolicy = map[string]any{"services": []any{map[string]any{
				"id": "api", "image": "python@sha256:" + strings.Repeat("a", 64),
				"workingDirectory": ".", "argv": []any{"python", "-m", "api"},
				"healthArgv": []any{"python", "-m", "api.health"},
			}}}
		}},
		{name: "blocked Must", mutate: func(input *CompileCandidatePlanInput) {
			input.Obligations[0].Status = "blocked"
		}},
		{name: "unknown Oracle", mutate: func(input *CompileCandidatePlanInput) {
			input.Obligations[0].OracleIDs = []string{"oracle-unknown"}
		}},
		{name: "unresolved Must", mutate: func(input *CompileCandidatePlanInput) {
			input.Oracles[0].CommandID = ""
		}},
		{name: "missing image route", mutate: func(input *CompileCandidatePlanInput) {
			delete(input.Profile.CommandImageRoles, "api")
		}},
		{name: "built-in unknown Oracle", mutate: func(input *CompileCandidatePlanInput) {
			input.Oracles[0].CommandID = ""
			input.Profile.BuiltInChecks = []ProfileBuiltInCheck{{
				ID: "hidden-contract", Kind: "hidden", ImageRole: "python",
				Argv: []string{"quality-verifier", "hidden-contract"}, WorkingDirectory: ".",
				OracleIDs: []string{"oracle-unknown"}, TimeoutSeconds: 120,
			}}
		}},
		{name: "built-in spoofed coverage", mutate: func(input *CompileCandidatePlanInput) {
			input.Oracles[0].CommandID = ""
			input.Profile.BuiltInChecks = []ProfileBuiltInCheck{{
				ID: "hidden-contract", Kind: "hidden", ImageRole: "python",
				Argv: []string{"quality-verifier", "hidden-contract"}, WorkingDirectory: ".",
				OracleIDs: []string{"oracle-contract"}, ObligationIDs: []string{"OBL-ui"},
				AcceptanceCriterionIDs: []string{"AC-ui"}, TimeoutSeconds: 120,
			}}
		}},
		{name: "built-in service image role mismatch", mutate: func(input *CompileCandidatePlanInput) {
			input.Profile.BuiltInChecks = []ProfileBuiltInCheck{{
				ID: "api-security", Kind: "security", ImageRole: "node", ServiceID: "api",
				Argv: []string{"npm", "audit"}, WorkingDirectory: "apps/api", TimeoutSeconds: 120,
			}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validCandidatePlanInput()
			test.mutate(&input)
			if _, err := (PlanCompiler{}).Compile(input); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("invalid Plan input was accepted: %v", err)
			}
		})
	}
}

func TestParsePlanRejectsHashValidSemanticSpoof(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PlanContent)
	}{
		{name: "required Must coverage removed", mutate: func(content *PlanContent) {
			content.Checks[0].Required = false
		}},
		{name: "unknown Oracle claimed", mutate: func(content *PlanContent) {
			content.Checks[0].OracleIDs = []string{"oracle-unknown"}
		}},
		{name: "wrong obligation coverage claimed", mutate: func(content *PlanContent) {
			content.Checks[0].ObligationIDs = []string{"OBL-ui"}
		}},
		{name: "canonical check ordering changed", mutate: func(content *PlanContent) {
			content.Checks[0], content.Checks[1] = content.Checks[1], content.Checks[0]
		}},
		{name: "runtime object made unstable", mutate: func(content *PlanContent) {
			content.RuntimePolicy.NetworkPolicy = nil
		}},
		{name: "dependency cache identity changed", mutate: func(content *PlanContent) {
			content.Dependencies[0].CacheKey = hashFixture("spoofed-dependency-cache")
		}},
		{name: "dependency resolver command changed", mutate: func(content *PlanContent) {
			content.Dependencies[0].ResolverArgv = []string{"pip", "install", "unlocked"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compiled, err := (PlanCompiler{}).Compile(validCandidatePlanInput())
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&compiled.Content)
			hash, err := domain.CanonicalHash(compiled.Content)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParsePlan(compiled.Content, "sha256:"+hash); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("hash-valid semantic spoof was accepted: %v", err)
			}
		})
	}
}

func TestPlanCompilerAllowsProfileOwnedBuiltInOracleWithoutClientCommand(t *testing.T) {
	input := validCandidatePlanInput()
	input.Oracles[0].CommandID = ""
	input.Profile.BuiltInChecks = []ProfileBuiltInCheck{{
		ID: "hidden-contract", Kind: "hidden", ImageRole: "python", ServiceID: "api",
		Argv: []string{"quality-verifier", "hidden-contract"}, WorkingDirectory: ".",
		OracleIDs: []string{"oracle-contract"}, ObligationIDs: []string{"OBL-contract"},
		AcceptanceCriterionIDs: []string{"AC-contract"}, TimeoutSeconds: 120,
	}}
	compiled, err := (PlanCompiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Content.Checks) != 2 || compiled.Content.Checks[0].ID != "hidden-contract" ||
		!compiled.Content.Checks[0].Required || compiled.Content.Checks[0].WorkingDirectory != "." ||
		compiled.Content.Checks[0].ServiceID != "api" {
		t.Fatalf("built-in Must Oracle check = %#v", compiled.Content.Checks)
	}
}

func TestPlanCompilerAllowsProfileOwnedBuiltInOracleWithUnmatchedStableCommandID(t *testing.T) {
	input := validCandidatePlanInput()
	input.Oracles[0].CommandID = "test-production-contract"
	input.Profile.BuiltInChecks = []ProfileBuiltInCheck{{
		ID: "hidden-contract", Kind: "hidden", ImageRole: "python",
		Argv: []string{"quality-verifier", "hidden-contract"}, WorkingDirectory: ".",
		OracleIDs: []string{"oracle-contract"}, ObligationIDs: []string{"OBL-contract"},
		AcceptanceCriterionIDs: []string{"AC-contract"}, TimeoutSeconds: 120,
	}}
	compiled, err := (PlanCompiler{}).Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Content.Checks) != 2 || compiled.Content.Checks[0].ID != "hidden-contract" ||
		!compiled.Content.Checks[0].Required {
		t.Fatalf("built-in unmatched-command coverage = %#v", compiled.Content.Checks)
	}
}

func TestPlanCompilerRejectsCyclicCheckDAG(t *testing.T) {
	input := validCandidatePlanInput()
	input.Profile.BuiltInChecks = []ProfileBuiltInCheck{
		{ID: "security-a", Kind: "security", ImageRole: "node", Argv: []string{"scan", "a"}, WorkingDirectory: ".", Required: true, DependsOn: []string{"security-b"}, TimeoutSeconds: 60},
		{ID: "security-b", Kind: "security", ImageRole: "node", Argv: []string{"scan", "b"}, WorkingDirectory: ".", Required: true, DependsOn: []string{"security-a"}, TimeoutSeconds: 60},
	}
	if _, err := (PlanCompiler{}).Compile(input); !errors.Is(err, ErrInvalidPlan) ||
		!strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("cyclic check DAG was not rejected: %v", err)
	}
}

func validCandidatePlanInput() CompileCandidatePlanInput {
	return CompileCandidatePlanInput{
		ProjectID: uuid.NewString(),
		Subject: CandidatePlanSubject{
			SessionID: uuid.NewString(), SessionVersion: 4,
			CandidateID: uuid.NewString(), CandidateSnapshotID: uuid.NewString(),
			CandidateVersion: 3, JournalSequence: 1, SessionEpoch: 1, WriterLeaseEpoch: 1,
			TreeStore: "blob", TreeOwnerID: uuid.NewString(), TreeRef: "blob://candidate/tree",
			TreeContentHash: hashFixture("plan-tree-content"), TreeHash: hashFixture("plan-tree"),
		},
		BuildManifest:     repository.ExactReference{ID: uuid.NewString(), ContentHash: hashFixture("plan-manifest")},
		BuildContract:     repository.ExactReference{ID: uuid.NewString(), ContentHash: hashFixture("plan-contract")},
		FullStackTemplate: repository.ExactReference{ID: uuid.NewString(), ContentHash: hashFixture("plan-full-stack")},
		Profile: VerificationProfileDocument{
			SchemaVersion: "verification-profile/v1", ID: "react-fastapi-postgres-v1",
			Version: 1, ProfileHash: hashFixture("plan-profile"), State: "active",
			SupportedTemplateRoles: []string{"web", "api"},
			VerifierImages: []VerifierImage{
				{Role: "node", Image: "registry.example/quality-node@sha256:" + strings.Repeat("a", 64)},
				{Role: "python", Image: "registry.example/quality-python@sha256:" + strings.Repeat("b", 64)},
			},
			CommandImageRoles: map[string]string{"web": "node", "api": "python"},
			BuiltInChecks:     []ProfileBuiltInCheck{},
			Limits:            map[string]any{"checkTimeoutSeconds": float64(300)},
			NetworkPolicy: map[string]any{
				"mode": "none", "dependencyResolver": map[string]any{"network": "bridge"},
			},
		},
		TemplateReleases: []ResolvedTemplateRelease{
			{
				Role: "api", MountPath: "apps/api",
				Release:     repository.ExactReference{ID: uuid.NewString(), ContentHash: hashFixture("plan-api-release")},
				SubjectHash: hashFixture("plan-api-subject"),
				Manifest: templates.TemplateManifest{
					SchemaVersion: templates.TemplateManifestSchemaVersion,
					Toolchains: []templates.Toolchain{{
						Name: "python", Version: "3.12.4",
						Image: "registry.example/python@sha256:" + strings.Repeat("c", 64),
					}},
					Lockfiles: []templates.Lockfile{{
						Path: "requirements.lock", Digest: hashFixture("plan-api-lock"),
						Registry: "https://pypi.org/simple",
					}},
					Commands: map[string]templates.Command{
						"test-contract": {WorkingDirectory: ".", Argv: []string{"pytest", "tests/contract"}},
					},
				},
			},
			{
				Role: "web", MountPath: "apps/web",
				Release:     repository.ExactReference{ID: uuid.NewString(), ContentHash: hashFixture("plan-web-release")},
				SubjectHash: hashFixture("plan-web-subject"),
				Manifest: templates.TemplateManifest{
					SchemaVersion: templates.TemplateManifestSchemaVersion,
					Toolchains: []templates.Toolchain{{
						Name: "node", Version: "22.4.1",
						Image: "registry.example/node@sha256:" + strings.Repeat("d", 64),
					}},
					Lockfiles: []templates.Lockfile{{
						Path: "package-lock.json", Digest: hashFixture("plan-web-lock"),
						Registry: "https://registry.npmjs.org",
					}},
					Commands: map[string]templates.Command{
						"test-ui": {WorkingDirectory: "src", Argv: []string{"pnpm", "test"}},
					},
				},
			},
		},
		Oracles: []PlanOracle{
			{ID: "oracle-contract", Kind: "contract", Target: "GET /messages", CommandID: "test-contract", AcceptanceCriterionIDs: []string{"AC-contract"}},
			{ID: "oracle-ui", Kind: "ui", Target: "/messages", CommandID: "test-ui", AcceptanceCriterionIDs: []string{"AC-ui"}},
		},
		Obligations: []PlanObligation{
			{ID: "OBL-contract", Level: "must", Status: "ready", OracleIDs: []string{"oracle-contract"}},
			{ID: "OBL-ui", Level: "should", Status: "ready", OracleIDs: []string{"oracle-ui"}},
		},
	}
}
