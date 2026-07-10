package delivery

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// StaticAssetServer exposes only deployment versions whose durable metadata is
// ready. This prevents a provider directory from becoming public in the small
// window between an atomic filesystem rename and the SQL completion commit.
type StaticAssetServer interface {
	ServeAsset(http.ResponseWriter, *http.Request, string, string, string)
}

type LocalStaticAssetService struct {
	database *gorm.DB
	provider *LocalStaticProvider
}

func NewLocalStaticAssetService(database *gorm.DB, provider *LocalStaticProvider) (*LocalStaticAssetService, error) {
	if database == nil || provider == nil {
		return nil, errors.New("local static asset database and provider are required")
	}
	return &LocalStaticAssetService{database: database, provider: provider}, nil
}

func (s *LocalStaticAssetService) ServeAsset(response http.ResponseWriter, request *http.Request, deploymentID, versionID, asset string) {
	deploymentUUID, deploymentErr := uuid.Parse(deploymentID)
	versionUUID, versionErr := uuid.Parse(versionID)
	if deploymentErr != nil || versionErr != nil {
		http.NotFound(response, request)
		return
	}
	reference := deploymentID + "/versions/" + versionID
	var count int64
	err := s.database.WithContext(request.Context()).Model(&deploymentVersionModel{}).
		Where("id = ? AND deployment_id = ? AND status = 'ready' AND provider_ref = ?", versionUUID, deploymentUUID, reference).
		Count(&count).Error
	if err != nil || count != 1 {
		http.NotFound(response, request)
		return
	}
	s.provider.ServeAsset(response, request, deploymentID, versionID, asset)
}
