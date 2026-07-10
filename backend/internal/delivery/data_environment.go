package delivery

import (
	"context"
	"errors"
	"strings"

	"github.com/worksflow/builder/backend/internal/dataruntime"
)

// DataRuntimeEnvironmentSource intentionally exposes only the data runtime's
// public/plain capability. Its type cannot retrieve or decrypt secret values.
type DataRuntimeEnvironmentSource interface {
	PublicEnvironment(context.Context, string, string, dataruntime.EnvironmentScope) (map[string]string, error)
}

type DataRuntimeEnvironmentResolver struct {
	Source DataRuntimeEnvironmentSource
}

func (r DataRuntimeEnvironmentResolver) Resolve(
	ctx context.Context,
	projectID string,
	environment Environment,
	reference string,
	actorID string,
) (ResolvedEnvironment, error) {
	if r.Source == nil {
		return ResolvedEnvironment{}, errors.New("data runtime public environment source is required")
	}
	var scope dataruntime.EnvironmentScope
	switch environment {
	case EnvironmentPreview:
		scope = dataruntime.ScopePreview
	case EnvironmentProduction:
		scope = dataruntime.ScopeProduction
	default:
		return ResolvedEnvironment{}, Invalid("environment", "environment must be preview or production")
	}
	values, err := r.Source.PublicEnvironment(ctx, projectID, actorID, scope)
	if err != nil {
		return ResolvedEnvironment{}, err
	}
	reference = strings.TrimSpace(reference)
	if reference == "" {
		reference = "data-runtime:" + string(scope)
	}
	return ResolvedEnvironment{Reference: reference, Public: cloneStringMap(values)}, nil
}
