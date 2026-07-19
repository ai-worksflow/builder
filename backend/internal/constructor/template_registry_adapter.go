package constructor

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/templates"
)

// TemplateRegistryResolver adapts the independently versioned Template
// Registry domain to the narrow trusted input required by the deterministic
// BuildContract compiler. The Registry performs all exact identity and policy
// checks before any compiler-facing value is constructed.
type TemplateRegistryResolver struct {
	registry templates.RegistryReader
}

func NewTemplateRegistryResolver(registry templates.RegistryReader) (*TemplateRegistryResolver, error) {
	if registry == nil {
		return nil, errors.New("template registry reader is required")
	}
	return &TemplateRegistryResolver{registry: registry}, nil
}

func (r *TemplateRegistryResolver) ResolveFullStack(
	ctx context.Context,
	selection FullStackTemplateSelection,
) (ResolvedFullStackTemplate, error) {
	resolved, err := r.registry.ResolveForNewBuild(ctx, templates.ExactFullStackTemplateRef{
		ID: selection.ID, ContentHash: selection.ContentHash,
	})
	if err != nil {
		return ResolvedFullStackTemplate{}, normalizeTemplateRegistryError(err)
	}
	stack := resolved.Template.Snapshot()
	releases := make([]TemplateReleaseRef, 0, len(resolved.Components))
	projectionInputs := make([]templateRuntimeProjectionInput, 0, len(resolved.Components))
	for _, component := range resolved.Components {
		release := component.Release.Snapshot()
		if component.Policy.State != templates.ReleaseApproved || component.Policy.ReleaseContentHash != release.ContentHash {
			return ResolvedFullStackTemplate{}, core.ErrConflict
		}
		releases = append(releases, TemplateReleaseRef{
			ID: release.ID, ReleaseHash: release.ContentHash, Role: component.Role,
			Certification: "approved", PolicyStatus: "active",
		})
		projectionInputs = append(projectionInputs, templateRuntimeProjectionInput{
			Role: component.Role, MountPath: component.MountPath, Release: release,
		})
	}
	return ResolvedFullStackTemplate{
		Template: FullStackTemplateRef{
			ID: stack.ID, ContentHash: stack.ContentHash,
			Certification: "approved", PolicyStatus: "active",
		},
		Releases: releases,
		Runtime:  projectTemplateRuntimeFacts(stack, projectionInputs),
	}, nil
}

type templateRuntimeProjectionInput struct {
	Role      string
	MountPath string
	Release   templates.TemplateReleaseView
}

func projectTemplateRuntimeFacts(stack templates.FullStackTemplateView, inputs []templateRuntimeProjectionInput) *TemplateRuntimeFacts {
	facts := &TemplateRuntimeFacts{
		FullStackTemplateID: stack.ID, FullStackTemplateHash: stack.ContentHash,
		Layout: TemplateRuntimeLayout{
			ContractTruthSource: stack.Layout.ContractTruthSource,
			OpenAPIPath:         stack.Layout.OpenAPIPath, GeneratedClientPath: stack.Layout.GeneratedClientPath,
			DeploymentPath: stack.Layout.DeploymentPath, TestPath: stack.Layout.TestPath,
			DatabaseEngine: stack.Layout.DatabaseEngine,
		},
		Components: make([]TemplateRuntimeComponent, 0, len(inputs)),
	}
	for _, input := range inputs {
		manifest := input.Release.Manifest
		component := TemplateRuntimeComponent{
			Role: input.Role, MountPath: input.MountPath,
			ReleaseID: input.Release.ID, ReleaseHash: input.Release.ContentHash,
			ManifestSchemaVersion: manifest.SchemaVersion,
			Services:              make([]TemplateRuntimeService, 0, len(manifest.Services)),
			Commands:              make([]string, 0, len(manifest.Commands)),
			Ports:                 make([]TemplateRuntimePort, 0, len(manifest.Ports)),
			HealthChecks:          make([]TemplateRuntimeHealthCheck, 0, len(manifest.HealthChecks)),
			BuildOutputs:          make([]TemplateRuntimeBuildOutput, 0, len(manifest.BuildOutputs)),
			EnvironmentVariables:  make([]TemplateRuntimeEnvironmentVariable, 0, len(manifest.EnvironmentSchema)),
		}
		for _, service := range manifest.Services {
			component.Services = append(component.Services, TemplateRuntimeService{ID: service.ID, Role: service.Kind, RootPath: service.RootPath})
		}
		for name := range manifest.Commands {
			component.Commands = append(component.Commands, name)
		}
		sort.Strings(component.Commands)
		for _, port := range manifest.Ports {
			component.Ports = append(component.Ports, TemplateRuntimePort{Name: port.Name, ServiceID: port.ServiceID, Number: port.Number, Protocol: port.Protocol})
		}
		for _, health := range manifest.HealthChecks {
			component.HealthChecks = append(component.HealthChecks, TemplateRuntimeHealthCheck{ID: health.ID, ServiceID: health.ServiceID, PortName: health.PortName, Path: health.Path})
		}
		if manifest.Migration != nil {
			component.Migration = &TemplateRuntimeMigration{ServiceID: manifest.Migration.ServiceID, CommandName: manifest.Migration.CommandName}
		}
		for _, output := range manifest.BuildOutputs {
			component.BuildOutputs = append(component.BuildOutputs, TemplateRuntimeBuildOutput{ServiceID: output.ServiceID, Path: output.Path})
		}
		for _, environment := range manifest.EnvironmentSchema {
			component.EnvironmentVariables = append(component.EnvironmentVariables, TemplateRuntimeEnvironmentVariable{
				Name: environment.Name, Required: environment.Required, Secret: environment.Secret,
				Scope: templateEnvironmentScope(input.Role),
			})
		}
		facts.Components = append(facts.Components, component)
	}
	sort.Slice(facts.Components, func(i, j int) bool {
		return facts.Components[i].Role+"\x00"+facts.Components[i].ReleaseID < facts.Components[j].Role+"\x00"+facts.Components[j].ReleaseID
	})
	return facts
}

func templateEnvironmentScope(role string) string {
	switch role {
	case "web":
		return "web-build"
	case "api":
		return "api-runtime"
	case "worker":
		return "worker-runtime"
	default:
		return ""
	}
}

func normalizeTemplateRegistryError(err error) error {
	switch {
	case errors.Is(err, templates.ErrInvalidTemplate), errors.Is(err, templates.ErrUnsupportedSchema):
		return fmt.Errorf("%w: %v", core.ErrInvalidInput, err)
	case errors.Is(err, templates.ErrRegistryNotFound):
		return fmt.Errorf("%w: %v", core.ErrNotFound, err)
	case errors.Is(err, templates.ErrReleaseNotSelectable):
		return fmt.Errorf("%w: %v", core.ErrBlockingGate, err)
	case errors.Is(err, templates.ErrRegistryIntegrity):
		return fmt.Errorf("%w: %v", core.ErrConflict, err)
	default:
		return err
	}
}

var _ TemplateResolver = (*TemplateRegistryResolver)(nil)
