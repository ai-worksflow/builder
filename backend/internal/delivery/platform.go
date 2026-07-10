package delivery

import (
	"errors"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"gorm.io/gorm"
)

// PlatformDependencies is the single bootstrap seam used by the application
// package. All security-sensitive implementations are explicit; the factory
// never falls back to host command execution or an ephemeral publish store.
type PlatformDependencies struct {
	Database     *gorm.DB
	Contents     content.Store
	Access       AccessControl
	Sandbox      Sandbox
	QualityRoot  string
	Provider     PublishProvider
	Environments EnvironmentResolver
}

type PlatformServices struct {
	Loader            *RevisionLoader
	Quality           *QualityService
	Export            *ExportService
	Publish           *PublishService
	StaticAssets      StaticAssetServer
	WorkflowQuality   WorkflowQualityEvaluator
	WorkflowPublisher WorkflowPublisher
}

func NewPlatformServices(dependencies PlatformDependencies) (*PlatformServices, error) {
	if dependencies.Database == nil || dependencies.Contents == nil {
		return nil, errors.New("delivery database and content store are required")
	}
	access := dependencies.Access
	if access == nil {
		created, err := core.NewAccessControl(dependencies.Database)
		if err != nil {
			return nil, err
		}
		access = created
	}
	if dependencies.Sandbox == nil {
		return nil, errors.New("delivery quality sandbox is required")
	}
	if dependencies.Provider == nil {
		return nil, errors.New("delivery publish provider is required")
	}
	loader, err := NewRevisionLoader(dependencies.Database, dependencies.Contents, access)
	if err != nil {
		return nil, err
	}
	quality, err := NewQualityService(dependencies.Database, dependencies.Contents, access, dependencies.Sandbox, dependencies.QualityRoot)
	if err != nil {
		return nil, err
	}
	exporter, err := NewExportService(dependencies.Database, loader)
	if err != nil {
		return nil, err
	}
	publisher, err := NewPublishService(
		dependencies.Database, access, loader, quality,
		dependencies.Provider, dependencies.Environments,
	)
	if err != nil {
		return nil, err
	}
	services := &PlatformServices{
		Loader: loader, Quality: quality, Export: exporter, Publish: publisher,
		WorkflowQuality:   WorkflowQualityEvaluator{Quality: quality},
		WorkflowPublisher: WorkflowPublisher{Quality: quality, Publisher: publisher},
	}
	if local, ok := dependencies.Provider.(*LocalStaticProvider); ok {
		assets, assetErr := NewLocalStaticAssetService(dependencies.Database, local)
		if assetErr != nil {
			return nil, assetErr
		}
		services.StaticAssets = assets
	}
	return services, nil
}
