package sandbox

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"

	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/templates"
)

type ExactSessionTemplateRegistry interface {
	GetFullStackTemplateExact(context.Context, templates.ExactFullStackTemplateRef) (templates.FullStackTemplateRegistration, error)
	GetTemplateReleaseExact(context.Context, templates.TemplateReleaseRef) (templates.TemplateReleaseRegistration, error)
}

type TemplateSessionConfigurationResolver struct {
	registry ExactSessionTemplateRegistry
}

// ResolvedProcessCommand is the only executable process description exposed
// by the template boundary. The client selects a service and command ID; the
// server resolves argv and cwd from the exact immutable release already bound
// to the session.
type ResolvedProcessCommand struct {
	ServiceID        string
	CommandID        string
	TemplateRelease  repository.ExactReference
	WorkingDirectory string
	Argv             []string
}

type SessionProcessCommandResolver interface {
	ResolveProcessCommand(context.Context, SessionView, string, string) (ResolvedProcessCommand, error)
}

func NewTemplateSessionConfigurationResolver(
	registry ExactSessionTemplateRegistry,
) (*TemplateSessionConfigurationResolver, error) {
	if registry == nil {
		return nil, errors.New("exact sandbox template registry is required")
	}
	return &TemplateSessionConfigurationResolver{registry: registry}, nil
}

func (resolver *TemplateSessionConfigurationResolver) ResolveSessionConfiguration(
	ctx context.Context,
	candidate repository.CandidateWorkspace,
) (SessionConfiguration, error) {
	if resolver == nil || resolver.registry == nil || candidate.Validate() != nil {
		return SessionConfiguration{}, ErrInvalidSession
	}
	stack, err := resolver.registry.GetFullStackTemplateExact(ctx, templates.ExactFullStackTemplateRef{
		ID: candidate.FullStackTemplate.ID, ContentHash: candidate.FullStackTemplate.ContentHash,
	})
	if err != nil {
		return SessionConfiguration{}, err
	}
	view := stack.Template.Snapshot()
	if view.ID != candidate.FullStackTemplate.ID || view.ContentHash != candidate.FullStackTemplate.ContentHash ||
		len(view.Components) == 0 || len(view.Components) != len(stack.Components) {
		return SessionConfiguration{}, ErrCandidateMismatch
	}

	services := make([]AllowedService, 0, 8)
	ports := make([]AllowedPort, 0, 8)
	serviceIDs := make(map[string]bool)
	portNames := make(map[string]bool)
	portNumbers := make(map[int]bool)
	for index, component := range view.Components {
		if stack.Components[index] != component {
			return SessionConfiguration{}, ErrCandidateMismatch
		}
		release, err := resolver.registry.GetTemplateReleaseExact(ctx, component.Release)
		if err != nil {
			return SessionConfiguration{}, err
		}
		releaseView := release.Release.Snapshot()
		if releaseView.ID != component.Release.ID || releaseView.ContentHash != component.Release.ContentHash ||
			releaseView.SubjectHash != component.Release.SubjectHash {
			return SessionConfiguration{}, ErrCandidateMismatch
		}
		profiles := make([]string, 0, len(releaseView.Manifest.Commands))
		for name := range releaseView.Manifest.Commands {
			profiles = append(profiles, name)
		}
		sort.Strings(profiles)
		selected := make(map[string]bool)
		for _, declared := range releaseView.Manifest.Services {
			if declared.Kind != component.Role {
				continue
			}
			if serviceIDs[declared.ID] {
				return SessionConfiguration{}, fmt.Errorf("%w: duplicate mounted service ID %s", ErrInvalidSession, declared.ID)
			}
			serviceIDs[declared.ID] = true
			selected[declared.ID] = true
			services = append(services, AllowedService{
				ID: declared.ID, Kind: declared.Kind, Profiles: append([]string(nil), profiles...),
				TemplateRelease: repository.ExactReference{
					ID: releaseView.ID, ContentHash: releaseView.ContentHash,
				},
			})
		}
		if len(selected) == 0 {
			return SessionConfiguration{}, fmt.Errorf("%w: release does not declare its selected role", ErrInvalidSession)
		}
		for _, declared := range releaseView.Manifest.Ports {
			if !selected[declared.ServiceID] || declared.Exposure != "preview" {
				continue
			}
			if portNames[declared.Name] || portNumbers[declared.Number] {
				return SessionConfiguration{}, fmt.Errorf("%w: duplicate mounted preview port", ErrInvalidSession)
			}
			portNames[declared.Name] = true
			portNumbers[declared.Number] = true
			ports = append(ports, AllowedPort{
				Name: declared.Name, ServiceID: declared.ServiceID,
				Number: declared.Number, Protocol: declared.Protocol,
			})
		}
	}
	if len(services) == 0 {
		return SessionConfiguration{}, fmt.Errorf("%w: exact stack has no runnable services", ErrInvalidSession)
	}
	return SessionConfiguration{Services: services, Ports: ports}, nil
}

func (resolver *TemplateSessionConfigurationResolver) ResolveProcessCommand(
	ctx context.Context,
	session SessionView,
	serviceID, commandID string,
) (ResolvedProcessCommand, error) {
	if resolver == nil || resolver.registry == nil || ctx == nil ||
		session.SchemaVersion != SessionSchemaVersion || !validUUID(session.ID) ||
		validateExactReference(session.FullStackTemplate) != nil ||
		!slugPattern.MatchString(serviceID) || !slugPattern.MatchString(commandID) {
		return ResolvedProcessCommand{}, ErrProcessInvalid
	}

	allowed, ok := exactAllowedService(session.AllowedServices, serviceID, commandID)
	if !ok {
		return ResolvedProcessCommand{}, ErrProcessInvalid
	}
	stack, err := resolver.registry.GetFullStackTemplateExact(ctx, templates.ExactFullStackTemplateRef{
		ID: session.FullStackTemplate.ID, ContentHash: session.FullStackTemplate.ContentHash,
	})
	if err != nil {
		return ResolvedProcessCommand{}, err
	}
	view := stack.Template.Snapshot()
	if view.ID != session.FullStackTemplate.ID || view.ContentHash != session.FullStackTemplate.ContentHash ||
		len(view.Components) == 0 || len(view.Components) != len(stack.Components) {
		return ResolvedProcessCommand{}, ErrCandidateMismatch
	}

	for index, component := range view.Components {
		if stack.Components[index] != component {
			return ResolvedProcessCommand{}, ErrCandidateMismatch
		}
		if component.Role != allowed.Kind ||
			component.Release.ID != allowed.TemplateRelease.ID ||
			component.Release.ContentHash != allowed.TemplateRelease.ContentHash {
			continue
		}
		release, releaseErr := resolver.registry.GetTemplateReleaseExact(ctx, component.Release)
		if releaseErr != nil {
			return ResolvedProcessCommand{}, releaseErr
		}
		releaseView := release.Release.Snapshot()
		if releaseView.ID != component.Release.ID || releaseView.ContentHash != component.Release.ContentHash ||
			releaseView.SubjectHash != component.Release.SubjectHash {
			return ResolvedProcessCommand{}, ErrCandidateMismatch
		}
		serviceFound := false
		for _, declared := range releaseView.Manifest.Services {
			if declared.ID == serviceID && declared.Kind == component.Role {
				serviceFound = true
				break
			}
		}
		command, commandFound := releaseView.Manifest.Commands[commandID]
		if !serviceFound || !commandFound || len(command.Argv) == 0 {
			return ResolvedProcessCommand{}, ErrProcessInvalid
		}
		workingDirectory := component.MountPath
		if command.WorkingDirectory != "." {
			workingDirectory = path.Join(component.MountPath, command.WorkingDirectory)
		}
		// Both operands came from canonical immutable manifests. Re-check the
		// effective path at this trust boundary so registry drift cannot create
		// an absolute or escaping cwd.
		if path.IsAbs(workingDirectory) || path.Clean(workingDirectory) != workingDirectory ||
			workingDirectory == "." || workingDirectory == ".." {
			return ResolvedProcessCommand{}, ErrCandidateMismatch
		}
		return ResolvedProcessCommand{
			ServiceID: serviceID, CommandID: commandID,
			TemplateRelease:  allowed.TemplateRelease,
			WorkingDirectory: workingDirectory,
			Argv:             append([]string(nil), command.Argv...),
		}, nil
	}
	return ResolvedProcessCommand{}, ErrProcessInvalid
}

func exactAllowedService(values []AllowedService, serviceID, commandID string) (AllowedService, bool) {
	for _, value := range values {
		if value.ID != serviceID {
			continue
		}
		for _, profile := range value.Profiles {
			if profile == commandID {
				return value, true
			}
		}
		return AllowedService{}, false
	}
	return AllowedService{}, false
}

var _ SessionConfigurationResolver = (*TemplateSessionConfigurationResolver)(nil)
var _ SessionProcessCommandResolver = (*TemplateSessionConfigurationResolver)(nil)
