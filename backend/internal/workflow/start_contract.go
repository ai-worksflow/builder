package workflow

import (
	"errors"
	"fmt"
	"strings"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

var ErrStartManifestIncompatible = errors.New("workflow definition is incompatible with the requested input/output contract")

// StartManifestDescriptor is deliberately storage-neutral so conversation
// discovery can use the same matcher after resolving exact kinds, while the
// workflow engine can use it before any run is created.
type StartManifestDescriptor struct {
	JobType              string
	OutputSchemaVersion  string
	SourcePurposes       []string
	ArtifactKinds        []string
	ArtifactCount        int
	AllArtifactsApproved bool
}

func DescribeStartManifest(manifest domain.InputManifest, metadata StartArtifactMetadata) StartManifestDescriptor {
	purposes := make([]string, 0, len(manifest.Sources))
	for _, source := range manifest.Sources {
		purposes = append(purposes, source.Purpose)
	}
	return StartManifestDescriptor{
		JobType: manifest.JobType, OutputSchemaVersion: manifest.OutputSchemaVersion,
		SourcePurposes: purposes, ArtifactKinds: append([]string(nil), metadata.Kinds...),
		ArtifactCount: metadata.Count, AllArtifactsApproved: metadata.AllApproved,
	}
}

// CompatibleStart is the shared deterministic candidate matcher. It checks
// both sides of the contract; desiredOutputCapability is optional only for the
// engine, which has already selected an exact immutable definition version.
func CompatibleStart(
	definition domain.WorkflowDefinition,
	manifest StartManifestDescriptor,
	desiredOutputCapability string,
) error {
	if definition.InputContract == nil || definition.OutputContract == nil {
		return validateLegacyStartCompatibility(definition, manifest.JobType, desiredOutputCapability)
	}
	jobType := strings.TrimSpace(manifest.JobType)
	expectedSchema, allowed := definition.InputContract.ManifestSchemaContracts[jobType]
	if !allowed || strings.TrimSpace(manifest.OutputSchemaVersion) != expectedSchema {
		return fmt.Errorf("%w: manifest job/schema pair is not allowed", ErrStartManifestIncompatible)
	}
	availablePurposes := make(map[string]struct{}, len(manifest.SourcePurposes))
	for _, purpose := range manifest.SourcePurposes {
		availablePurposes[strings.TrimSpace(purpose)] = struct{}{}
	}
	for _, purpose := range definition.InputContract.RequiredSourcePurposes {
		if _, present := availablePurposes[purpose]; !present {
			return fmt.Errorf("%w: required source purpose %q is missing", ErrStartManifestIncompatible, purpose)
		}
	}
	if !sameStrings(manifest.ArtifactKinds, definition.InputContract.ArtifactKinds) {
		return fmt.Errorf("%w: exact artifact kinds do not match", ErrStartManifestIncompatible)
	}
	if manifest.ArtifactCount < definition.InputContract.MinimumArtifacts {
		return fmt.Errorf("%w: artifact count is below the entry minimum", ErrStartManifestIncompatible)
	}
	if definition.InputContract.MaximumArtifacts > 0 && manifest.ArtifactCount > definition.InputContract.MaximumArtifacts {
		return fmt.Errorf("%w: artifact count exceeds the entry maximum", ErrStartManifestIncompatible)
	}
	if definition.InputContract.RequireApproved && !manifest.AllArtifactsApproved {
		return fmt.Errorf("%w: entry policy requires approved artifact revisions", ErrStartManifestIncompatible)
	}
	desiredOutputCapability = strings.TrimSpace(desiredOutputCapability)
	if desiredOutputCapability != "" && desiredOutputCapability != definition.OutputContract.Capability {
		return fmt.Errorf("%w: desired output capability %q is not produced", ErrStartManifestIncompatible, desiredOutputCapability)
	}
	return nil
}

// ValidateStartManifestJobType is retained as a cheap discovery prefilter for
// callers that have not loaded the full immutable manifest. Authoritative
// start decisions must call CompatibleStart with a complete descriptor.
func ValidateStartManifestJobType(definition domain.WorkflowDefinition, manifestJobType string) error {
	manifestJobType = strings.TrimSpace(manifestJobType)
	if definition.InputContract != nil {
		if _, allowed := definition.InputContract.ManifestSchemaContracts[manifestJobType]; !allowed {
			return ErrStartManifestIncompatible
		}
		return nil
	}
	return validateLegacyStartCompatibility(definition, manifestJobType, "")
}

func validateLegacyStartCompatibility(definition domain.WorkflowDefinition, manifestJobType, desiredOutputCapability string) error {
	requiresBlueprintSelection := false
	for _, node := range definition.Nodes {
		if node.FanOut != nil && strings.TrimSpace(node.FanOut.ItemKind) == "blueprint_selection_page" {
			requiresBlueprintSelection = true
			break
		}
	}
	if strings.TrimSpace(manifestJobType) == "" || requiresBlueprintSelection != (strings.TrimSpace(manifestJobType) == core.BlueprintSelectionJobType) {
		return ErrStartManifestIncompatible
	}
	if strings.TrimSpace(desiredOutputCapability) != "" && strings.TrimSpace(desiredOutputCapability) != domain.WorkflowOutputApplication {
		return ErrStartManifestIncompatible
	}
	return nil
}
