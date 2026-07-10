package delivery

import "context"

type ProviderRequest struct {
	DeploymentID      string
	VersionID         string
	Environment       Environment
	EnvironmentRef    string
	BuildArtifact     BuildArtifact
	PublicEnvironment map[string]string
}

type ProviderResult struct {
	Reference  string
	PublicURL  string
	Checksum   string
	EntryPath  string
	FileCount  int
	TotalBytes int64
}

type PublishProvider interface {
	Name() string
	Deploy(context.Context, ProviderRequest) (ProviderResult, error)
}

type EnvironmentResolver interface {
	Resolve(context.Context, string, Environment, string, string) (ResolvedEnvironment, error)
}

type EmptyEnvironmentResolver struct{}

func (EmptyEnvironmentResolver) Resolve(_ context.Context, _ string, _ Environment, reference, _ string) (ResolvedEnvironment, error) {
	if reference == "" {
		reference = "default"
	}
	return ResolvedEnvironment{Reference: reference, Public: map[string]string{}}, nil
}
