package repository

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/worksflow/builder/backend/internal/templates"
)

// ExactTemplatePathRegistry is the smallest immutable Template Registry view
// required to derive repository path policy. Callers must resolve by exact
// release IDs and content hashes; names, tags, and "latest" are deliberately
// absent from this boundary.
type ExactTemplatePathRegistry interface {
	GetFullStackTemplateExact(context.Context, templates.ExactFullStackTemplateRef) (templates.FullStackTemplateRegistration, error)
	GetTemplateReleaseExact(context.Context, templates.TemplateReleaseRef) (templates.TemplateReleaseRegistration, error)
}

type mountedTemplatePathPolicy struct {
	MountPath      string
	ExtensionPaths []string
	ProtectedPaths []string
}

type exactTemplatePathSource interface {
	ResolveExactTemplatePaths(context.Context, ExactReference) ([]mountedTemplatePathPolicy, error)
}

// RegistryPathPolicyResolver derives effective repository-relative paths from
// the exact FullStackTemplate and its immutable component TemplateReleases.
// ReleasePolicy state is intentionally not copied into the result: revocation
// controls whether a release may be selected for a new build, while the
// immutable manifest remains the authority for an already-created Candidate.
type RegistryPathPolicyResolver struct {
	source exactTemplatePathSource
}

func NewRegistryPathPolicyResolver(registry ExactTemplatePathRegistry) (*RegistryPathPolicyResolver, error) {
	if registry == nil {
		return nil, errors.New("exact template path registry is required")
	}
	return newRegistryPathPolicyResolver(templateRegistryPathSource{registry: registry})
}

func newRegistryPathPolicyResolver(source exactTemplatePathSource) (*RegistryPathPolicyResolver, error) {
	if source == nil {
		return nil, errors.New("exact template path source is required")
	}
	return &RegistryPathPolicyResolver{source: source}, nil
}

func (resolver *RegistryPathPolicyResolver) ResolvePathPolicy(
	ctx context.Context,
	subject PathPolicySubject,
) (PathPolicy, error) {
	if resolver == nil || resolver.source == nil {
		return PathPolicy{}, errors.New("exact template path source is unavailable")
	}
	if !validUUID(subject.ProjectID) || !validUUID(subject.RepositorySnapshotID) {
		return PathPolicy{}, fmt.Errorf("%w: invalid path policy subject identity", ErrPathPolicyDrift)
	}
	for _, ref := range []ExactReference{subject.BuildManifest, subject.BuildContract, subject.FullStackTemplate} {
		if err := validateExact(ref); err != nil {
			return PathPolicy{}, fmt.Errorf("%w: invalid exact path policy subject: %v", ErrPathPolicyDrift, err)
		}
	}

	components, err := resolver.source.ResolveExactTemplatePaths(ctx, subject.FullStackTemplate)
	if err != nil {
		return PathPolicy{}, fmt.Errorf("resolve exact template path manifests: %w", err)
	}
	if len(components) == 0 || len(components) > 8 {
		return PathPolicy{}, fmt.Errorf("%w: exact full-stack template has no bounded components", ErrPathPolicyDrift)
	}

	policy := PathPolicy{Subject: subject}
	for _, component := range components {
		mount, err := NormalizePath(component.MountPath)
		if err != nil {
			return PathPolicy{}, fmt.Errorf("%w: component mount: %v", ErrPathPolicyDrift, err)
		}
		if len(component.ExtensionPaths) == 0 || len(component.ProtectedPaths) == 0 {
			return PathPolicy{}, fmt.Errorf("%w: component path policy is incomplete", ErrPathPolicyDrift)
		}
		for _, componentPath := range component.ExtensionPaths {
			effective, err := effectiveTemplatePath(mount, componentPath)
			if err != nil {
				return PathPolicy{}, fmt.Errorf("%w: extension path: %v", ErrPathPolicyDrift, err)
			}
			policy.ExtensionPaths = append(policy.ExtensionPaths, effective)
		}
		for _, componentPath := range component.ProtectedPaths {
			effective, err := effectiveTemplatePath(mount, componentPath)
			if err != nil {
				return PathPolicy{}, fmt.Errorf("%w: protected path: %v", ErrPathPolicyDrift, err)
			}
			policy.ProtectedPaths = append(policy.ProtectedPaths, effective)
		}
	}
	return normalizePathPolicy(policy, subject)
}

func effectiveTemplatePath(mount, componentPath string) (string, error) {
	if componentPath == "" || componentPath != strings.TrimSpace(componentPath) {
		return "", ErrInvalidTree
	}
	normalizedMount, err := NormalizePath(mount)
	if err != nil {
		return "", err
	}
	normalizedComponent, err := NormalizePath(componentPath)
	if err != nil {
		return "", err
	}
	effective := path.Join(normalizedMount, normalizedComponent)
	// Validate each operand before path.Join so a component path cannot escape
	// its mount through a syntactically-clean "../" prefix. NormalizePath on the
	// result remains a final traversal/control check.
	return NormalizePath(effective)
}

type templateRegistryPathSource struct {
	registry ExactTemplatePathRegistry
}

func (source templateRegistryPathSource) ResolveExactTemplatePaths(
	ctx context.Context,
	ref ExactReference,
) ([]mountedTemplatePathPolicy, error) {
	registration, err := source.registry.GetFullStackTemplateExact(ctx, templates.ExactFullStackTemplateRef{
		ID: ref.ID, ContentHash: ref.ContentHash,
	})
	if err != nil {
		return nil, err
	}
	fullStack := registration.Template.Snapshot()
	if fullStack.ID != ref.ID || fullStack.ContentHash != ref.ContentHash ||
		len(fullStack.Components) == 0 || len(fullStack.Components) != len(registration.Components) {
		return nil, fmt.Errorf("%w: exact full-stack template response drifted", ErrPathPolicyDrift)
	}

	result := make([]mountedTemplatePathPolicy, 0, len(fullStack.Components))
	for index, component := range fullStack.Components {
		registered := registration.Components[index]
		if registered != component {
			return nil, fmt.Errorf("%w: full-stack component relation drifted", ErrPathPolicyDrift)
		}
		release, err := source.registry.GetTemplateReleaseExact(ctx, component.Release)
		if err != nil {
			return nil, err
		}
		view := release.Release.Snapshot()
		if view.ID != component.Release.ID || view.ContentHash != component.Release.ContentHash ||
			view.SubjectHash != component.Release.SubjectHash ||
			release.Policy.TemplateReleaseID != component.Release.ID ||
			release.Policy.ReleaseContentHash != component.Release.ContentHash {
			return nil, fmt.Errorf("%w: exact component release response drifted", ErrPathPolicyDrift)
		}
		result = append(result, mountedTemplatePathPolicy{
			MountPath:      component.MountPath,
			ExtensionPaths: append([]string(nil), view.Manifest.ExtensionPaths...),
			ProtectedPaths: append([]string(nil), view.Manifest.ProtectedPaths...),
		})
	}
	return result, nil
}
