package repository

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeExactTemplatePathSource struct {
	components []mountedTemplatePathPolicy
	ref        ExactReference
	err        error
}

func (source *fakeExactTemplatePathSource) ResolveExactTemplatePaths(
	_ context.Context,
	ref ExactReference,
) ([]mountedTemplatePathPolicy, error) {
	source.ref = ref
	if source.err != nil {
		return nil, source.err
	}
	return source.components, nil
}

func TestRegistryPathPolicyResolverMountsExactComponentPolicies(t *testing.T) {
	subject := PathPolicySubject{
		ProjectID:            mutationProjectID,
		RepositorySnapshotID: mutationSnapshotID,
		BuildManifest:        ExactReference{ID: mutationManifestID, ContentHash: mutationHashA},
		BuildContract:        ExactReference{ID: mutationContractID, ContentHash: mutationHashB},
		FullStackTemplate:    ExactReference{ID: mutationTemplateID, ContentHash: mutationHashC},
	}
	source := &fakeExactTemplatePathSource{components: []mountedTemplatePathPolicy{
		{MountPath: "apps/web", ExtensionPaths: []string{"src", "tests"}, ProtectedPaths: []string{"templates.lock.json"}},
		{MountPath: "services/api", ExtensionPaths: []string{"internal"}, ProtectedPaths: []string{"migrations"}},
	}}
	resolver, err := newRegistryPathPolicyResolver(source)
	if err != nil {
		t.Fatal(err)
	}

	policy, err := resolver.ResolvePathPolicy(context.Background(), subject)
	if err != nil {
		t.Fatalf("resolve policy: %v", err)
	}
	if source.ref != subject.FullStackTemplate {
		t.Fatalf("source received %#v, want %#v", source.ref, subject.FullStackTemplate)
	}
	if policy.Subject != subject {
		t.Fatalf("policy subject drifted: %#v", policy.Subject)
	}
	wantExtension := []string{"apps/web/src", "apps/web/tests", "services/api/internal"}
	wantProtected := []string{"apps/web/templates.lock.json", "services/api/migrations"}
	if !reflect.DeepEqual(policy.ExtensionPaths, wantExtension) {
		t.Fatalf("extension paths = %#v, want %#v", policy.ExtensionPaths, wantExtension)
	}
	if !reflect.DeepEqual(policy.ProtectedPaths, wantProtected) {
		t.Fatalf("protected paths = %#v, want %#v", policy.ProtectedPaths, wantProtected)
	}
}

func TestRegistryPathPolicyResolverFailsClosed(t *testing.T) {
	validSubject := PathPolicySubject{
		ProjectID:            mutationProjectID,
		RepositorySnapshotID: mutationSnapshotID,
		BuildManifest:        ExactReference{ID: mutationManifestID, ContentHash: mutationHashA},
		BuildContract:        ExactReference{ID: mutationContractID, ContentHash: mutationHashB},
		FullStackTemplate:    ExactReference{ID: mutationTemplateID, ContentHash: mutationHashC},
	}
	tests := []struct {
		name       string
		subject    PathPolicySubject
		components []mountedTemplatePathPolicy
		sourceErr  error
	}{
		{name: "invalid subject", subject: PathPolicySubject{}},
		{name: "source failure", subject: validSubject, sourceErr: errors.New("registry unavailable")},
		{name: "missing components", subject: validSubject},
		{name: "missing protected policy", subject: validSubject, components: []mountedTemplatePathPolicy{{MountPath: "apps/web", ExtensionPaths: []string{"src"}}}},
		{name: "traversal", subject: validSubject, components: []mountedTemplatePathPolicy{{MountPath: "apps/web", ExtensionPaths: []string{"../escape"}, ProtectedPaths: []string{"lock"}}}},
		{name: "cross policy overlap", subject: validSubject, components: []mountedTemplatePathPolicy{{MountPath: "apps/web", ExtensionPaths: []string{"src"}, ProtectedPaths: []string{"src/generated"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &fakeExactTemplatePathSource{components: test.components, err: test.sourceErr}
			resolver, err := newRegistryPathPolicyResolver(source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := resolver.ResolvePathPolicy(context.Background(), test.subject); err == nil {
				t.Fatal("expected fail-closed path policy error")
			}
		})
	}
}

func TestNewRegistryPathPolicyResolverRequiresRegistry(t *testing.T) {
	if _, err := NewRegistryPathPolicyResolver(nil); err == nil {
		t.Fatal("expected nil registry to be rejected")
	}
}
