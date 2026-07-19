package verification

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestCanonicalPlanCompilerBindsExactWorkspaceAndReleaseChecks(t *testing.T) {
	input := validCanonicalPlanInput()
	compiled, err := (PlanCompiler{}).CompileCanonical(input)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Content.Scope != ScopeCanonical ||
		compiled.Content.Subject.WorkspaceRevisionID != input.Subject.WorkspaceRevisionID ||
		len(compiled.Content.Dependencies) != 2 || len(compiled.Content.Checks) != 6 ||
		!exactSHA256(compiled.PlanHash) {
		t.Fatalf("canonical Plan lost exact facts: %#v", compiled)
	}
	parsed, err := ParseCanonicalPlan(compiled.Content, compiled.PlanHash)
	if err != nil || parsed.PlanHash != compiled.PlanHash {
		t.Fatalf("parse canonical Plan = %#v, %v", parsed, err)
	}

	reordered := input
	reordered.TemplateReleases[0], reordered.TemplateReleases[1] =
		reordered.TemplateReleases[1], reordered.TemplateReleases[0]
	reordered.Profile.BuiltInChecks[0], reordered.Profile.BuiltInChecks[2] =
		reordered.Profile.BuiltInChecks[2], reordered.Profile.BuiltInChecks[0]
	again, err := (PlanCompiler{}).CompileCanonical(reordered)
	if err != nil || again.PlanHash != compiled.PlanHash {
		t.Fatalf("canonical Plan is not deterministic: %s != %s, %v", again.PlanHash, compiled.PlanHash, err)
	}
}

func TestCanonicalPlanCompilerRejectsMissingOrSpoofedReleaseAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CompileCanonicalPlanInput)
	}{
		{name: "missing sbom", mutate: func(input *CompileCanonicalPlanInput) {
			input.Profile.BuiltInChecks = append(input.Profile.BuiltInChecks[:1], input.Profile.BuiltInChecks[2:]...)
		}},
		{name: "optional vulnerability", mutate: func(input *CompileCanonicalPlanInput) {
			input.Profile.BuiltInChecks[2].Required = false
		}},
		{name: "mutable supply chain image", mutate: func(input *CompileCanonicalPlanInput) {
			input.Profile.VerifierImages[0].Image = "quality-node:latest"
		}},
		{name: "workspace hash drift", mutate: func(input *CompileCanonicalPlanInput) {
			input.Subject.WorkspaceContentHash = "not-a-hash"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validCanonicalPlanInput()
			test.mutate(&input)
			if _, err := (PlanCompiler{}).CompileCanonical(input); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("invalid canonical Plan was accepted: %v", err)
			}
		})
	}
}

func validCanonicalPlanInput() CompileCanonicalPlanInput {
	candidate := validCandidatePlanInput()
	candidate.Profile.BuiltInChecks = []ProfileBuiltInCheck{
		{ID: "release-artifacts", Kind: "release-manifest", ImageRole: "node", Argv: []string{"quality", "release-artifacts"}, WorkingDirectory: ".", Required: true, TimeoutSeconds: 600},
		{ID: "release-sbom", Kind: "sbom", ImageRole: "node", Argv: []string{"quality", "sbom"}, WorkingDirectory: ".", Required: true, TimeoutSeconds: 120},
		{ID: "release-vulnerability", Kind: "vulnerability", ImageRole: "node", Argv: []string{"quality", "vulnerability"}, WorkingDirectory: ".", Required: true, TimeoutSeconds: 120},
		{ID: "release-container-policy", Kind: "container-policy", ImageRole: "node", Argv: []string{"quality", "container-policy"}, WorkingDirectory: ".", Required: true, TimeoutSeconds: 120},
	}
	return CompileCanonicalPlanInput{
		ProjectID: candidate.ProjectID,
		Subject: CanonicalPlanSubject{
			WorkspaceArtifactID: uuid.NewString(), WorkspaceRevisionID: uuid.NewString(),
			WorkspaceContentHash: hashFixture("canonical-workspace"),
		},
		BuildManifest: candidate.BuildManifest, BuildContract: candidate.BuildContract,
		FullStackTemplate: candidate.FullStackTemplate, Profile: candidate.Profile,
		TemplateReleases: candidate.TemplateReleases, Oracles: candidate.Oracles,
		Obligations: candidate.Obligations,
	}
}
