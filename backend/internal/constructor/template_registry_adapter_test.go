package constructor

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/templates"
)

type registryReaderStub struct {
	resolved templates.ResolvedFullStackTemplate
	err      error
}

func (stub registryReaderStub) ListTemplateReleases(context.Context, templates.TemplateReleaseListOptions) ([]templates.TemplateReleaseRegistration, error) {
	return nil, errors.New("unexpected call")
}

func (stub registryReaderStub) GetTemplateRelease(context.Context, string) (templates.TemplateReleaseRegistration, error) {
	return templates.TemplateReleaseRegistration{}, errors.New("unexpected call")
}

func (stub registryReaderStub) GetTemplateReleaseExact(context.Context, templates.TemplateReleaseRef) (templates.TemplateReleaseRegistration, error) {
	return templates.TemplateReleaseRegistration{}, errors.New("unexpected call")
}

func (stub registryReaderStub) ListFullStackTemplates(context.Context, templates.FullStackTemplateListOptions) ([]templates.FullStackTemplateRegistration, error) {
	return nil, errors.New("unexpected call")
}

func (stub registryReaderStub) GetFullStackTemplate(context.Context, string) (templates.FullStackTemplateRegistration, error) {
	return templates.FullStackTemplateRegistration{}, errors.New("unexpected call")
}

func (stub registryReaderStub) GetFullStackTemplateExact(context.Context, templates.ExactFullStackTemplateRef) (templates.FullStackTemplateRegistration, error) {
	return templates.FullStackTemplateRegistration{}, errors.New("unexpected call")
}

func (stub registryReaderStub) ResolveForNewBuild(context.Context, templates.ExactFullStackTemplateRef) (templates.ResolvedFullStackTemplate, error) {
	return stub.resolved, stub.err
}

func TestTemplateRegistryResolverNormalizesStableRegistryErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		registry error
		want     error
	}{
		{templates.ErrRegistryNotFound, core.ErrNotFound},
		{templates.ErrReleaseNotSelectable, core.ErrBlockingGate},
		{templates.ErrRegistryIntegrity, core.ErrConflict},
		{templates.ErrInvalidTemplate, core.ErrInvalidInput},
	} {
		resolver, err := NewTemplateRegistryResolver(registryReaderStub{err: test.registry})
		if err != nil {
			t.Fatal(err)
		}
		_, err = resolver.ResolveFullStack(context.Background(), FullStackTemplateSelection{})
		if !errors.Is(err, test.want) {
			t.Fatalf("error = %v, want %v", err, test.want)
		}
	}
}

func TestProjectTemplateRuntimeFactsPreservesExactManifestAndLayoutFacts(t *testing.T) {
	t.Parallel()

	stack := templates.FullStackTemplateView{
		ID: "stack-1", ContentHash: "stack-hash",
		Layout: templates.FullStackLayout{
			ContractTruthSource: "openapi", OpenAPIPath: "contracts/openapi.yaml",
			GeneratedClientPath: "generated/client", DeploymentPath: "deploy/stack.yaml",
			TestPath: "tests", DatabaseEngine: "postgresql",
		},
	}
	apiMigration := templates.MigrationCommand{ServiceID: "api", CommandName: "migrate"}
	inputs := []templateRuntimeProjectionInput{
		{
			Role: "web", MountPath: "apps/web",
			Release: templates.TemplateReleaseView{ID: "release-web", ContentHash: "hash-web", Manifest: templates.TemplateManifest{
				SchemaVersion: templates.TemplateManifestSchemaVersion,
				Services:      []templates.TemplateService{{ID: "web", Kind: "web", RootPath: "."}},
				Commands: map[string]templates.Command{
					"z-build": {WorkingDirectory: ".", Argv: []string{"pnpm", "build"}},
					"a-lint":  {WorkingDirectory: ".", Argv: []string{"pnpm", "lint"}},
				},
				Ports:             []templates.Port{{Name: "web", ServiceID: "web", Number: 3000, Protocol: "http", Exposure: "preview"}},
				HealthChecks:      []templates.HealthCheck{{ID: "web-health", ServiceID: "web", PortName: "web", Path: "/healthz"}},
				BuildOutputs:      []templates.BuildOutput{{ServiceID: "web", Path: ".next"}},
				EnvironmentSchema: []templates.EnvironmentVariable{{Name: "PUBLIC_API_ORIGIN", Required: true}},
			}},
		},
		{
			Role: "api", MountPath: "services/api",
			Release: templates.TemplateReleaseView{ID: "release-api", ContentHash: "hash-api", Manifest: templates.TemplateManifest{
				SchemaVersion:     templates.TemplateManifestSchemaVersion,
				Services:          []templates.TemplateService{{ID: "api", Kind: "api", RootPath: "."}},
				Commands:          map[string]templates.Command{"migrate": {WorkingDirectory: ".", Argv: []string{"goose", "up"}}},
				Ports:             []templates.Port{{Name: "api", ServiceID: "api", Number: 8080, Protocol: "http", Exposure: "internal"}},
				HealthChecks:      []templates.HealthCheck{{ID: "api-health", ServiceID: "api", PortName: "api", Path: "/readyz"}},
				Migration:         &apiMigration,
				BuildOutputs:      []templates.BuildOutput{{ServiceID: "api", Path: "bin/server"}},
				EnvironmentSchema: []templates.EnvironmentVariable{{Name: "DATABASE_URL", Required: true, Secret: true}},
			}},
		},
	}
	facts := projectTemplateRuntimeFacts(stack, inputs)
	want := &TemplateRuntimeFacts{
		FullStackTemplateID: "stack-1", FullStackTemplateHash: "stack-hash",
		Layout: TemplateRuntimeLayout{
			ContractTruthSource: "openapi", OpenAPIPath: "contracts/openapi.yaml",
			GeneratedClientPath: "generated/client", DeploymentPath: "deploy/stack.yaml",
			TestPath: "tests", DatabaseEngine: "postgresql",
		},
		Components: []TemplateRuntimeComponent{
			{
				Role: "api", MountPath: "services/api", ReleaseID: "release-api", ReleaseHash: "hash-api",
				ManifestSchemaVersion: templates.TemplateManifestSchemaVersion,
				Services:              []TemplateRuntimeService{{ID: "api", Role: "api", RootPath: "."}}, Commands: []string{"migrate"},
				Ports:                []TemplateRuntimePort{{Name: "api", ServiceID: "api", Number: 8080, Protocol: "http"}},
				HealthChecks:         []TemplateRuntimeHealthCheck{{ID: "api-health", ServiceID: "api", PortName: "api", Path: "/readyz"}},
				Migration:            &TemplateRuntimeMigration{ServiceID: "api", CommandName: "migrate"},
				BuildOutputs:         []TemplateRuntimeBuildOutput{{ServiceID: "api", Path: "bin/server"}},
				EnvironmentVariables: []TemplateRuntimeEnvironmentVariable{{Name: "DATABASE_URL", Required: true, Secret: true, Scope: "api-runtime"}},
			},
			{
				Role: "web", MountPath: "apps/web", ReleaseID: "release-web", ReleaseHash: "hash-web",
				ManifestSchemaVersion: templates.TemplateManifestSchemaVersion,
				Services:              []TemplateRuntimeService{{ID: "web", Role: "web", RootPath: "."}}, Commands: []string{"a-lint", "z-build"},
				Ports:                []TemplateRuntimePort{{Name: "web", ServiceID: "web", Number: 3000, Protocol: "http"}},
				HealthChecks:         []TemplateRuntimeHealthCheck{{ID: "web-health", ServiceID: "web", PortName: "web", Path: "/healthz"}},
				BuildOutputs:         []TemplateRuntimeBuildOutput{{ServiceID: "web", Path: ".next"}},
				EnvironmentVariables: []TemplateRuntimeEnvironmentVariable{{Name: "PUBLIC_API_ORIGIN", Required: true, Scope: "web-build"}},
			},
		},
	}
	if !reflect.DeepEqual(facts, want) {
		t.Fatalf("runtime facts = %#v, want %#v", facts, want)
	}
}
