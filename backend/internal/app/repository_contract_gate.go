package app

import (
	"context"
	"fmt"

	"github.com/worksflow/builder/backend/internal/constructor"
	"github.com/worksflow/builder/backend/internal/repository"
)

type repositoryBuildContractGate struct {
	constructor *constructor.Service
}

func (gate repositoryBuildContractGate) RequireReadyForBootstrap(
	ctx context.Context,
	projectID, buildManifestID, actorID string,
	selection repository.BootstrapBuildContractSelection,
) error {
	if gate.constructor == nil {
		return fmt.Errorf("Constructor readiness authority is unavailable")
	}
	binding, err := gate.constructor.RequireReady(
		ctx, projectID, buildManifestID, actorID,
		constructor.ExactBuildContractSelection{
			ID: selection.ID, ContractHash: selection.ContractHash,
		},
	)
	if err != nil {
		return err
	}
	if binding.ID != selection.ID || binding.ContractHash != selection.ContractHash ||
		binding.BuildManifestID != buildManifestID {
		return fmt.Errorf("Constructor returned a different exact BuildContract binding")
	}
	return nil
}

var _ repository.BootstrapBuildContractGate = repositoryBuildContractGate{}
