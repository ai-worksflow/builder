package application

import (
	"context"
	"errors"

	"github.com/worksflow/builder/backend/internal/domain"
)

type WorkflowService struct {
	Definitions WorkflowDefinitionRepository
	Runs        WorkflowRunRepository
	Manifests   ManifestRepository
	Proposals   ProposalRepository
	Slices      DeliverySliceRepository
	Clock       Clock
	IDs         IDGenerator
}

type RegisterWorkflowCommand struct {
	ID               string
	Version          int
	Name             string
	SchemaVersion    string
	Nodes            []domain.NodeDefinition
	Edges            []domain.WorkflowEdge
	ExecutionProfile domain.WorkflowExecutionProfileRef
	CreatedBy        string
}

func (s WorkflowService) RegisterDefinition(ctx context.Context, command RegisterWorkflowCommand) (domain.WorkflowDefinition, error) {
	if s.Definitions == nil {
		return domain.WorkflowDefinition{}, domain.ErrInvalidArgument
	}
	latest, err := s.Definitions.LatestVersion(ctx, command.ID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.WorkflowDefinition{}, err
	}
	expectedVersion := latest + 1
	if latest == 0 {
		expectedVersion = 1
	}
	if command.Version != expectedVersion {
		return domain.WorkflowDefinition{}, &domain.DomainError{Kind: domain.ErrConflict, Field: "workflow.version", Message: "workflow versions must be contiguous and immutable"}
	}
	definition, err := domain.NewWorkflowDefinition(
		command.ID, command.Version, command.Name, command.SchemaVersion,
		command.Nodes, command.Edges, command.CreatedBy, serviceNow(s.Clock),
	)
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	definition, err = definition.WithExecutionProfile(command.ExecutionProfile)
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	if err := s.Definitions.Create(ctx, definition); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	return definition, nil
}

type StartWorkflowCommand struct {
	RunID      string
	ProjectID  string
	CreatedBy  string
	Definition domain.WorkflowDefinitionRef
	Manifest   domain.ManifestRef
}

func (s WorkflowService) StartWorkflow(ctx context.Context, command StartWorkflowCommand) (*domain.WorkflowRun, error) {
	if s.Definitions == nil || s.Runs == nil || s.Manifests == nil {
		return nil, domain.ErrInvalidArgument
	}
	if err := command.Definition.Validate(); err != nil {
		return nil, err
	}
	definition, err := s.Definitions.Get(ctx, command.Definition.ID, command.Definition.Version)
	if err != nil {
		return nil, err
	}
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	if definition.Ref() != command.Definition {
		return nil, &domain.DomainError{Kind: domain.ErrConflict, Field: "workflow.definition", Message: "stored definition does not match the pinned hash"}
	}
	manifest, err := s.Manifests.Get(ctx, command.Manifest.ID)
	if err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	if manifest.Ref() != command.Manifest || manifest.ProjectID != command.ProjectID {
		return nil, &domain.DomainError{Kind: domain.ErrManifestUnpinned, Field: "workflow.manifest", Message: "stored manifest does not match the run pin or project"}
	}
	runID := command.RunID
	if runID == "" && s.IDs != nil {
		runID = s.IDs.NewID("workflow-run")
	}
	run, err := domain.NewWorkflowRun(runID, command.ProjectID, command.CreatedBy, definition, command.Manifest, serviceNow(s.Clock))
	if err != nil {
		return nil, err
	}
	if err := run.Start(run.Version, serviceNow(s.Clock)); err != nil {
		return nil, err
	}
	if err := s.Runs.Create(ctx, run); err != nil {
		return nil, err
	}
	return run, nil
}

type StartNodeCommand struct {
	RunID           string
	NodeID          string
	Manifest        domain.ManifestRef
	ExpectedVersion uint64
}

func (s WorkflowService) StartNode(ctx context.Context, command StartNodeCommand) (*domain.WorkflowRun, error) {
	run, definition, err := s.loadRun(ctx, command.RunID)
	if err != nil {
		return nil, err
	}
	manifest, err := s.Manifests.Get(ctx, command.Manifest.ID)
	if err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	if manifest.Ref() != command.Manifest || manifest.ProjectID != run.ProjectID {
		return nil, &domain.DomainError{Kind: domain.ErrManifestUnpinned, Field: "nodeRun.inputManifest", Message: "stored manifest does not match the node pin or project"}
	}
	if err := run.StartNode(definition, command.NodeID, command.Manifest, command.ExpectedVersion, serviceNow(s.Clock)); err != nil {
		return nil, err
	}
	if err := s.Runs.Save(ctx, run, command.ExpectedVersion); err != nil {
		return nil, err
	}
	return run, nil
}

type CompleteNodeCommand struct {
	RunID           string
	NodeID          string
	ProposalID      string
	ExpectedVersion uint64
}

func (s WorkflowService) CompleteNode(ctx context.Context, command CompleteNodeCommand) (*domain.WorkflowRun, error) {
	run, definition, err := s.loadRun(ctx, command.RunID)
	if err != nil {
		return nil, err
	}
	var output *domain.ProposalRef
	if command.ProposalID != "" {
		if s.Proposals == nil {
			return nil, domain.ErrInvalidArgument
		}
		proposal, err := s.Proposals.Get(ctx, command.ProposalID)
		if err != nil {
			return nil, err
		}
		if err := proposal.ValidatePayloadHash(); err != nil {
			return nil, err
		}
		output = &domain.ProposalRef{ID: proposal.ID, PayloadHash: proposal.PayloadHash}
	}
	if err := run.CompleteNode(definition, command.NodeID, output, command.ExpectedVersion, serviceNow(s.Clock)); err != nil {
		return nil, err
	}
	if err := s.Runs.Save(ctx, run, command.ExpectedVersion); err != nil {
		return nil, err
	}
	return run, nil
}

func (s WorkflowService) WaitForReview(ctx context.Context, runID, nodeID string, expectedVersion uint64) (*domain.WorkflowRun, error) {
	run, definition, err := s.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := run.WaitForReview(definition, nodeID, expectedVersion); err != nil {
		return nil, err
	}
	if err := s.Runs.Save(ctx, run, expectedVersion); err != nil {
		return nil, err
	}
	return run, nil
}

func (s WorkflowService) ResumeNode(ctx context.Context, runID, nodeID string, expectedVersion uint64) (*domain.WorkflowRun, error) {
	run, definition, err := s.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := run.ResumeNode(definition, nodeID, expectedVersion); err != nil {
		return nil, err
	}
	if err := s.Runs.Save(ctx, run, expectedVersion); err != nil {
		return nil, err
	}
	return run, nil
}

func (s WorkflowService) FailNode(ctx context.Context, runID, nodeID, message string, expectedVersion uint64) (*domain.WorkflowRun, error) {
	run, definition, err := s.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := run.FailNode(definition, nodeID, message, expectedVersion, serviceNow(s.Clock)); err != nil {
		return nil, err
	}
	if err := s.Runs.Save(ctx, run, expectedVersion); err != nil {
		return nil, err
	}
	return run, nil
}

func (s WorkflowService) SkipNode(ctx context.Context, runID, nodeID, reason string, expectedVersion uint64) (*domain.WorkflowRun, error) {
	run, definition, err := s.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := run.SkipNode(definition, nodeID, reason, expectedVersion, serviceNow(s.Clock)); err != nil {
		return nil, err
	}
	if err := s.Runs.Save(ctx, run, expectedVersion); err != nil {
		return nil, err
	}
	return run, nil
}

func (s WorkflowService) CancelRun(ctx context.Context, runID string, expectedVersion uint64) (*domain.WorkflowRun, error) {
	run, _, err := s.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := run.Cancel(expectedVersion, serviceNow(s.Clock)); err != nil {
		return nil, err
	}
	if err := s.Runs.Save(ctx, run, expectedVersion); err != nil {
		return nil, err
	}
	return run, nil
}

func (s WorkflowService) loadRun(ctx context.Context, runID string) (*domain.WorkflowRun, domain.WorkflowDefinition, error) {
	if s.Runs == nil || s.Definitions == nil {
		return nil, domain.WorkflowDefinition{}, domain.ErrInvalidArgument
	}
	run, err := s.Runs.Get(ctx, runID)
	if err != nil {
		return nil, domain.WorkflowDefinition{}, err
	}
	definition, err := s.Definitions.Get(ctx, run.Definition.ID, run.Definition.Version)
	if err != nil {
		return nil, domain.WorkflowDefinition{}, err
	}
	if definition.Ref() != run.Definition {
		return nil, domain.WorkflowDefinition{}, &domain.DomainError{Kind: domain.ErrConflict, Field: "workflowRun.definition", Message: "run definition hash no longer matches storage"}
	}
	return run, definition, nil
}

type CreateDeliverySliceCommand struct {
	ID                string
	ProjectID         string
	Key               string
	Title             string
	Blueprint         domain.ArtifactRef
	Prototype         *domain.ArtifactRef
	Sources           []domain.ArtifactRef
	NodeKeys          []string
	RequiresPrototype bool
}

func (s WorkflowService) CreateDeliverySlice(ctx context.Context, command CreateDeliverySliceCommand) (*domain.DeliverySlice, error) {
	if s.Slices == nil {
		return nil, domain.ErrInvalidArgument
	}
	id := command.ID
	if id == "" && s.IDs != nil {
		id = s.IDs.NewID("delivery-slice")
	}
	slice, err := domain.NewDeliverySlice(id, command.ProjectID, command.Key, command.Title, command.Blueprint, command.Prototype, command.Sources, command.NodeKeys, command.RequiresPrototype, serviceNow(s.Clock))
	if err != nil {
		return nil, err
	}
	if err := s.Slices.Create(ctx, slice); err != nil {
		return nil, err
	}
	return slice, nil
}
