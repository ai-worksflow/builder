package sandbox

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/templates"
)

type exactTemplateRegistryFake struct {
	stack    templates.FullStackTemplateRegistration
	releases map[string]templates.TemplateReleaseRegistration
}

func (registry *exactTemplateRegistryFake) GetFullStackTemplateExact(
	_ context.Context,
	ref templates.ExactFullStackTemplateRef,
) (templates.FullStackTemplateRegistration, error) {
	view := registry.stack.Template.Snapshot()
	if view.ID != ref.ID || view.ContentHash != ref.ContentHash {
		return templates.FullStackTemplateRegistration{}, templates.ErrRegistryNotFound
	}
	return registry.stack, nil
}

func (registry *exactTemplateRegistryFake) GetTemplateReleaseExact(
	_ context.Context,
	ref templates.TemplateReleaseRef,
) (templates.TemplateReleaseRegistration, error) {
	value, ok := registry.releases[ref.ID]
	if !ok {
		return templates.TemplateReleaseRegistration{}, templates.ErrRegistryNotFound
	}
	view := value.Release.Snapshot()
	if view.ContentHash != ref.ContentHash || view.SubjectHash != ref.SubjectHash {
		return templates.TemplateReleaseRegistration{}, templates.ErrRegistryNotFound
	}
	return value, nil
}

func TestTemplateConfigurationResolvesOnlyExactImmutableProcessCommand(t *testing.T) {
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	actorID := uuid.NewString()
	web := admittedSandboxTemplateRelease(t, "web-ui", "web", actorID, now)
	api := admittedSandboxTemplateRelease(t, "api-core", "api", actorID, now)
	stack, err := templates.NewFullStackTemplate(
		uuid.NewString(), "constructor-stack", "1.0.0",
		[]templates.FullStackComponentInput{
			{Role: "web", MountPath: "apps/web", Release: web},
			{Role: "api", MountPath: "services/api", Release: api},
		},
		templates.FullStackLayout{
			ContractTruthSource: "openapi", OpenAPIPath: "contracts/openapi.yaml",
			GeneratedClientPath: "packages/api-client", DeploymentPath: "deployment",
			TestPath: "integration-tests", DatabaseEngine: "postgresql",
		},
		actorID, now.Add(10*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	webPolicy, err := templates.NewReleasePolicy(web, actorID, now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	apiPolicy, err := templates.NewReleasePolicy(api, actorID, now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	stackView := stack.Snapshot()
	registry := &exactTemplateRegistryFake{
		stack: templates.FullStackTemplateRegistration{
			Template: stack, Components: append([]templates.FullStackComponent(nil), stackView.Components...),
		},
		releases: map[string]templates.TemplateReleaseRegistration{
			web.ID(): {Release: web, Policy: webPolicy},
			api.ID(): {Release: api, Policy: apiPolicy},
		},
	}
	resolver, err := NewTemplateSessionConfigurationResolver(registry)
	if err != nil {
		t.Fatal(err)
	}
	session := SessionView{
		SchemaVersion: SessionSchemaVersion, ID: testSessionID,
		FullStackTemplate: repository.ExactReference{ID: stack.ID(), ContentHash: stack.ContentHash()},
		AllowedServices: []AllowedService{
			{ID: "web-ui", Kind: "web", Profiles: []string{"dev"}, TemplateRelease: repository.ExactReference{ID: web.ID(), ContentHash: web.ContentHash()}},
			{ID: "api-core", Kind: "api", Profiles: []string{"dev"}, TemplateRelease: repository.ExactReference{ID: api.ID(), ContentHash: api.ContentHash()}},
		},
	}

	resolved, err := resolver.ResolveProcessCommand(context.Background(), session, "web-ui", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ServiceID != "web-ui" || resolved.CommandID != "dev" ||
		resolved.WorkingDirectory != "apps/web/src" ||
		!reflect.DeepEqual(resolved.Argv, []string{"node", "server.js"}) ||
		resolved.TemplateRelease.ID != web.ID() || resolved.TemplateRelease.ContentHash != web.ContentHash() {
		t.Fatalf("resolved command lost exact template identity: %#v", resolved)
	}
	resolved.Argv[0] = "tampered"
	again, err := resolver.ResolveProcessCommand(context.Background(), session, "web-ui", "dev")
	if err != nil || again.Argv[0] != "node" {
		t.Fatalf("resolved argv aliased immutable release state: %#v, %v", again, err)
	}

	mismatched := session
	mismatched.AllowedServices = append([]AllowedService(nil), session.AllowedServices...)
	mismatched.AllowedServices[0].TemplateRelease.ContentHash = sandboxDigest("1")
	if _, err := resolver.ResolveProcessCommand(context.Background(), mismatched, "web-ui", "dev"); !errors.Is(err, ErrProcessInvalid) {
		t.Fatalf("mismatched session release resolved: %v", err)
	}

	drifted := *registry
	drifted.stack.Components = append([]templates.FullStackComponent(nil), registry.stack.Components...)
	drifted.stack.Components[0], drifted.stack.Components[1] = drifted.stack.Components[1], drifted.stack.Components[0]
	driftResolver, err := NewTemplateSessionConfigurationResolver(&drifted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driftResolver.ResolveProcessCommand(context.Background(), session, "web-ui", "dev"); !errors.Is(err, ErrCandidateMismatch) {
		t.Fatalf("registry relation drift resolved: %v", err)
	}
}

func admittedSandboxTemplateRelease(
	t *testing.T,
	serviceID, kind, actorID string,
	now time.Time,
) templates.TemplateRelease {
	t.Helper()
	digest := func(seed string) string {
		character := seed[:1]
		if character < "0" || character > "f" {
			character = "a"
		}
		return "sha256:" + strings.Repeat(character, 64)
	}
	templateID := "sandbox-" + kind
	candidate := templates.AdmissionCandidate{
		Source: templates.TemplateSource{
			Repository: "https://github.com/ai-worksflow/templates.git", Branch: templateID,
			Commit: strings.Repeat("a", 40), TreeHash: digest("b"),
		},
		Manifest: templates.TemplateManifest{
			SchemaVersion: templates.TemplateManifestSchemaVersion,
			TemplateID:    templateID, DisplayName: templateID, Version: "1.0.0",
			Services: []templates.TemplateService{{ID: serviceID, Kind: kind, RootPath: "."}},
			Toolchains: []templates.Toolchain{{
				Name: "runtime", Version: "22.0.0", Image: "ghcr.io/worksflow/runtime@" + digest("c"),
			}},
			Commands: map[string]templates.Command{
				"dev":       {WorkingDirectory: "src", Argv: []string{"node", "server.js"}},
				"install":   {WorkingDirectory: ".", Argv: []string{"pnpm", "install", "--frozen-lockfile"}},
				"lint":      {WorkingDirectory: ".", Argv: []string{"pnpm", "lint"}},
				"typecheck": {WorkingDirectory: ".", Argv: []string{"pnpm", "typecheck"}},
				"test":      {WorkingDirectory: ".", Argv: []string{"pnpm", "test"}},
				"build":     {WorkingDirectory: ".", Argv: []string{"pnpm", "build"}},
				"start":     {WorkingDirectory: ".", Argv: []string{"pnpm", "start"}},
			},
			Ports: []templates.Port{{
				Name: kind + "-http", ServiceID: serviceID, Number: 3000,
				Protocol: "http", Exposure: "preview",
			}},
			HealthChecks: []templates.HealthCheck{{
				ID: kind + "-health", ServiceID: serviceID, PortName: kind + "-http", Path: "/health",
			}},
			BuildOutputs:   []templates.BuildOutput{{ServiceID: serviceID, Path: "output"}},
			ExtensionPaths: []string{"src"}, ProtectedPaths: []string{"templates.lock.json"},
			EnvironmentSchema: []templates.EnvironmentVariable{{
				Name: "PORT", Required: true, Description: "service port",
			}},
			Lockfiles: []templates.Lockfile{{
				Path: "pnpm-lock.yaml", Digest: digest("d"), Registry: "https://registry.npmjs.org",
			}},
			ProfileDigest: digest("e"),
		},
		SBOMDigest: digest("f"), LicenseExpression: "Apache-2.0", LicenseDigest: digest("1"),
	}
	attempt, err := templates.NewAdmissionAttempt(uuid.NewString(), actorID, candidate, now)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = attempt.BeginValidation(now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	subject := attempt.Snapshot().SubjectHash
	evidence := make([]templates.GateEvidence, 0, len(templates.RequiredAdmissionGates()))
	for _, gate := range templates.RequiredAdmissionGates() {
		evidence = append(evidence, templates.GateEvidence{
			Gate: gate, Outcome: templates.EvidencePassed, SubjectHash: subject,
			Digest: digest("2"), Reference: "urn:evidence:" + string(gate),
			Producer: "template-admission/v1", InvocationID: "invocation-" + string(gate),
			ObservedAt: now.Add(2 * time.Minute),
		})
	}
	_, release, err := attempt.Complete(
		uuid.NewString(), evidence,
		templates.SignatureEnvelope{
			Format: "dsse", SubjectHash: subject, BundleDigest: digest("3"),
			Signer:             "https://github.com/ai-worksflow/templates/.github/workflows/admit.yml@refs/heads/main",
			TransparencyLogRef: "urn:rekor:entry:" + kind, SignedAt: now.Add(2 * time.Minute),
		},
		actorID, now.Add(3*time.Minute),
	)
	if err != nil || release == nil {
		t.Fatalf("admit exact %s release: %v", kind, err)
	}
	return *release
}
