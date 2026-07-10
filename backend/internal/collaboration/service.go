package collaboration

import (
	"context"
	"errors"
	"time"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/generation"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"gorm.io/gorm"
)

type Authorizer interface {
	Authorize(context.Context, string, string, core.Action) (core.Role, error)
}

type ArtifactGenerator interface {
	GenerateArtifactProposal(context.Context, string, string, string) (generation.ArtifactGenerationResult, error)
}

type Service struct {
	database     *gorm.DB
	contents     content.Store
	access       Authorizer
	artifacts    *core.ArtifactService
	proposals    *core.ProposalService
	generator    ArtifactGenerator
	now          func() time.Time
	commandLease time.Duration
}

type Option func(*Service)

// WithDownstreamCommandLease must be configured longer than the AI provider's
// maximum request timeout. It prevents an expired recovery claim from running
// a second generation while the original provider call is still active.
func WithDownstreamCommandLease(lease time.Duration) Option {
	return func(service *Service) {
		if lease > 0 {
			service.commandLease = lease
		}
	}
}

func NewService(
	database *gorm.DB,
	contents content.Store,
	access Authorizer,
	artifacts *core.ArtifactService,
	proposals *core.ProposalService,
	generator ArtifactGenerator,
	options ...Option,
) (*Service, error) {
	if database == nil || contents == nil || access == nil || artifacts == nil || proposals == nil || generator == nil {
		return nil, errors.New("document collaboration dependencies are required")
	}
	service := &Service{
		database: database, contents: contents, access: access,
		artifacts: artifacts, proposals: proposals, generator: generator, now: time.Now,
		commandLease: 30 * time.Minute,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service, nil
}
