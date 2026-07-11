package workflow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

// Facade is the transport-facing authorization boundary. Workers use Engine
// directly, while every user-driven transition passes through project RBAC.
type Facade struct {
	Engine *Engine
	Store  Store
	Access PublishAuthorizer
}

type CreateDefinitionInput struct {
	Key            string                        `json:"key"`
	Title          string                        `json:"title"`
	Description    string                        `json:"description,omitempty"`
	Name           string                        `json:"name,omitempty"`
	SchemaVersion  string                        `json:"schemaVersion,omitempty"`
	Nodes          []domain.NodeDefinition       `json:"nodes"`
	Edges          []domain.WorkflowEdge         `json:"edges"`
	InputContract  domain.WorkflowInputContract  `json:"inputContract"`
	OutputContract domain.WorkflowOutputContract `json:"outputContract"`
}

type CreateDefinitionVersionInput struct {
	Name           string                        `json:"name,omitempty"`
	SchemaVersion  string                        `json:"schemaVersion,omitempty"`
	Nodes          []domain.NodeDefinition       `json:"nodes"`
	Edges          []domain.WorkflowEdge         `json:"edges"`
	InputContract  domain.WorkflowInputContract  `json:"inputContract"`
	OutputContract domain.WorkflowOutputContract `json:"outputContract"`
}

type DefinitionDiscoveryRequest struct {
	InputManifest           domain.ManifestRef `json:"inputManifest"`
	DesiredOutputCapability string             `json:"desiredOutputCapability"`
}

var workflowKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{2,63}$`)

func (f Facade) validate() error {
	if f.Engine == nil || f.Store == nil || f.Access == nil {
		return fmt.Errorf("workflow engine, store and access control are required")
	}
	return nil
}
func (f Facade) authorize(ctx context.Context, projectID, actorID string, action core.Action) error {
	if err := f.validate(); err != nil {
		return err
	}
	_, err := f.Access.Authorize(ctx, projectID, actorID, action)
	return err
}

func (f Facade) ListDefinitions(ctx context.Context, projectID, actorID string) ([]DefinitionRecord, error) {
	if err := f.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	return f.Store.ListDefinitions(ctx, projectID)
}

func (f Facade) WorkflowCapabilities(ctx context.Context, projectID, actorID string) (WorkflowCapabilities, error) {
	if err := f.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return WorkflowCapabilities{}, err
	}
	return CurrentWorkflowExecutionProfileDescriptor().Capabilities, nil
}

// DiscoverCompatibleDefinitionVersions is the authoritative control-plane
// discovery seam used by conversation intent generation. It loads the pinned
// manifest and trusted artifact metadata itself; client candidate lists are
// neither accepted nor consulted.
func (f Facade) DiscoverCompatibleDefinitionVersions(
	ctx context.Context,
	projectID, actorID string,
	request DefinitionDiscoveryRequest,
) ([]DefinitionRecord, error) {
	if err := f.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	if err := request.InputManifest.Validate(); err != nil || strings.TrimSpace(request.DesiredOutputCapability) == "" {
		return nil, core.ErrInvalidInput
	}
	manifest, err := f.Store.GetManifest(ctx, request.InputManifest.ID)
	if err != nil {
		return nil, err
	}
	if manifest.Ref() != request.InputManifest || manifest.ProjectID != projectID {
		return nil, domain.ErrManifestUnpinned
	}
	profile, err := f.Engine.executionProfile(CurrentWorkflowExecutionProfileRef())
	if err != nil {
		return nil, err
	}
	runtime := profile.executionRuntime(f.Engine)
	if runtime.startArtifactKinds == nil {
		return nil, fmt.Errorf("workflow start artifact-kind resolver is required")
	}
	metadata := StartArtifactMetadata{}
	if resolver, ok := runtime.startArtifactKinds.(StartArtifactMetadataResolver); ok {
		metadata, err = resolver.ResolveStartArtifactMetadata(ctx, manifest)
	} else {
		metadata.Kinds, err = runtime.startArtifactKinds.ResolveStartArtifactKinds(ctx, manifest)
		metadata.Count = len(artifactInputRefs(manifest))
	}
	if err != nil {
		return nil, err
	}
	descriptor := DescribeStartManifest(manifest, metadata)
	definitions, err := f.Store.ListDefinitions(ctx, projectID)
	if err != nil {
		return nil, err
	}
	compatible := make([]DefinitionRecord, 0)
	currentProfile := CurrentWorkflowExecutionProfileRef()
	for _, latest := range definitions {
		versions, err := f.Store.ListDefinitionVersions(ctx, latest.Definition.ID)
		if err != nil {
			return nil, err
		}
		var highest *DefinitionRecord
		for _, version := range versions {
			if !version.Published || (version.ProjectID != "" && version.ProjectID != projectID) {
				continue
			}
			if version.ExecutionProfile != currentProfile || version.Definition.ExecutionProfile != currentProfile {
				continue
			}
			if err := ValidateDefinitionForExecutionProfile(version.Definition, version.ExecutionProfile); err != nil {
				return nil, err
			}
			// Discovery is for new runs, not historical replay. Pre-contract
			// versions remain loadable by already pinned runs but are never offered
			// as candidates, and removed execution capabilities fail closed.
			if version.Definition.InputContract == nil {
				continue
			}
			if CompatibleStart(version.Definition, descriptor, request.DesiredOutputCapability) == nil {
				if highest == nil || version.Definition.Version > highest.Definition.Version {
					candidate := version
					highest = &candidate
				}
			}
		}
		if highest != nil {
			compatible = append(compatible, *highest)
		}
	}
	sort.Slice(compatible, func(i, j int) bool {
		if compatible[i].Definition.ID == compatible[j].Definition.ID {
			return compatible[i].Definition.Version > compatible[j].Definition.Version
		}
		return compatible[i].Definition.ID < compatible[j].Definition.ID
	})
	return compatible, nil
}

func (f Facade) CompatibleDefinitionVersions(
	ctx context.Context,
	projectID, actorID string,
	manifestRef domain.ManifestRef,
	desiredOutputCapability string,
) ([]DefinitionRecord, error) {
	return f.DiscoverCompatibleDefinitionVersions(ctx, projectID, actorID, DefinitionDiscoveryRequest{
		InputManifest: manifestRef, DesiredOutputCapability: desiredOutputCapability,
	})
}

// ValidateCompatibleDefinitionVersion revalidates an exact proposed start at
// both proposal-creation and command-execution time. It is intentionally not a
// membership check against a client candidate list.
func (f Facade) ValidateCompatibleDefinitionVersion(
	ctx context.Context,
	projectID, actorID, versionID string,
	manifestRef domain.ManifestRef,
	desiredOutputCapability string,
) error {
	if err := f.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return err
	}
	record, err := f.Store.GetDefinitionVersion(ctx, versionID)
	if err != nil {
		return err
	}
	if !record.Published || (record.ProjectID != "" && record.ProjectID != projectID) {
		return core.ErrInvalidInput
	}
	if record.ExecutionProfile != CurrentWorkflowExecutionProfileRef() || record.Definition.ExecutionProfile != record.ExecutionProfile {
		return core.ErrInvalidInput
	}
	if err := ValidateDefinitionForExecutionProfile(record.Definition, record.ExecutionProfile); err != nil {
		return err
	}
	if record.Definition.InputContract == nil {
		return core.ErrInvalidInput
	}
	_, descriptor, err := f.resolveStartDescriptor(ctx, projectID, manifestRef)
	if err != nil {
		return err
	}
	if err := CompatibleStart(record.Definition, descriptor, desiredOutputCapability); err != nil {
		return &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "inputManifest", Message: err.Error()}
	}
	return nil
}

func (f Facade) resolveStartDescriptor(
	ctx context.Context,
	projectID string,
	manifestRef domain.ManifestRef,
) (domain.InputManifest, StartManifestDescriptor, error) {
	if err := manifestRef.Validate(); err != nil {
		return domain.InputManifest{}, StartManifestDescriptor{}, core.ErrInvalidInput
	}
	manifest, err := f.Store.GetManifest(ctx, manifestRef.ID)
	if err != nil {
		return domain.InputManifest{}, StartManifestDescriptor{}, err
	}
	if manifest.Ref() != manifestRef || manifest.ProjectID != projectID {
		return domain.InputManifest{}, StartManifestDescriptor{}, domain.ErrManifestUnpinned
	}
	profile, err := f.Engine.executionProfile(CurrentWorkflowExecutionProfileRef())
	if err != nil {
		return domain.InputManifest{}, StartManifestDescriptor{}, err
	}
	runtime := profile.executionRuntime(f.Engine)
	if runtime.startArtifactKinds == nil {
		return domain.InputManifest{}, StartManifestDescriptor{}, fmt.Errorf("workflow start artifact-kind resolver is required")
	}
	metadata := StartArtifactMetadata{}
	if resolver, ok := runtime.startArtifactKinds.(StartArtifactMetadataResolver); ok {
		metadata, err = resolver.ResolveStartArtifactMetadata(ctx, manifest)
	} else {
		metadata.Kinds, err = runtime.startArtifactKinds.ResolveStartArtifactKinds(ctx, manifest)
		metadata.Count = len(artifactInputRefs(manifest))
	}
	if err != nil {
		return domain.InputManifest{}, StartManifestDescriptor{}, err
	}
	return manifest, DescribeStartManifest(manifest, metadata), nil
}
func (f Facade) ListDefinitionVersions(ctx context.Context, projectID, definitionID, actorID string) ([]DefinitionRecord, error) {
	if err := f.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	records, err := f.Store.ListDefinitionVersions(ctx, definitionID)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.ProjectID != "" && record.ProjectID != projectID {
			return nil, core.ErrNotFound
		}
	}
	return records, nil
}

func (f Facade) CreateDefinition(ctx context.Context, projectID, actorID string, input CreateDefinitionInput) (DefinitionRecord, error) {
	if err := f.authorize(ctx, projectID, actorID, core.ActionAdmin); err != nil {
		return DefinitionRecord{}, err
	}
	input.Key = strings.TrimSpace(input.Key)
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	input.Name = strings.TrimSpace(input.Name)
	input.SchemaVersion = strings.TrimSpace(input.SchemaVersion)
	if !workflowKeyPattern.MatchString(input.Key) || input.Key == MinimumLoopKey || input.Title == "" || len(input.Title) > 160 || len(input.Description) > 4000 {
		return DefinitionRecord{}, core.ErrInvalidInput
	}
	if input.Name == "" {
		input.Name = input.Title
	}
	if input.SchemaVersion == "" {
		input.SchemaVersion = "2"
	}
	profile := CurrentWorkflowExecutionProfileRef()
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		uuid.NewString(), 1, input.Name, input.SchemaVersion, input.Nodes, input.Edges,
		input.InputContract, input.OutputContract, profile, actorID, time.Now().UTC(),
	)
	if err != nil {
		return DefinitionRecord{}, err
	}
	if err := validateAuthoredDefinition(definition, f.Engine.Capabilities); err != nil {
		return DefinitionRecord{}, err
	}
	record := DefinitionRecord{
		VersionID: uuid.NewString(), ProjectID: projectID, Key: input.Key,
		Title: input.Title, Description: input.Description, Published: false, ExecutionProfile: profile, Definition: definition,
	}
	if err := f.Store.SaveDefinition(ctx, record); err != nil {
		return DefinitionRecord{}, err
	}
	return record, nil
}

func (f Facade) CreateDefinitionVersion(ctx context.Context, projectID, definitionID, actorID string, input CreateDefinitionVersionInput) (DefinitionRecord, error) {
	if err := f.authorize(ctx, projectID, actorID, core.ActionAdmin); err != nil {
		return DefinitionRecord{}, err
	}
	versions, err := f.Store.ListDefinitionVersions(ctx, definitionID)
	if err != nil {
		return DefinitionRecord{}, err
	}
	latest := versions[len(versions)-1]
	if latest.ProjectID != projectID {
		return DefinitionRecord{}, core.ErrNotFound
	}
	input.Name = strings.TrimSpace(input.Name)
	input.SchemaVersion = strings.TrimSpace(input.SchemaVersion)
	if input.Name == "" {
		input.Name = latest.Definition.Name
	}
	if input.SchemaVersion == "" {
		input.SchemaVersion = latest.Definition.SchemaVersion
	}
	if input.InputContract.Capability == "" && latest.Definition.InputContract != nil {
		input.InputContract = *latest.Definition.InputContract
	}
	if input.OutputContract.Capability == "" && latest.Definition.OutputContract != nil {
		input.OutputContract = *latest.Definition.OutputContract
	}
	profile := CurrentWorkflowExecutionProfileRef()
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		latest.Definition.ID, latest.Definition.Version+1, input.Name, input.SchemaVersion,
		input.Nodes, input.Edges, input.InputContract, input.OutputContract, profile, actorID, time.Now().UTC(),
	)
	if err != nil {
		return DefinitionRecord{}, err
	}
	if err := validateAuthoredDefinition(definition, f.Engine.Capabilities); err != nil {
		return DefinitionRecord{}, err
	}
	record := DefinitionRecord{
		VersionID: uuid.NewString(), ProjectID: projectID, Key: latest.Key,
		Title: latest.Title, Description: latest.Description, Published: false, ExecutionProfile: profile, Definition: definition,
	}
	if err := f.Store.SaveDefinition(ctx, record); err != nil {
		return DefinitionRecord{}, err
	}
	return record, nil
}

func (f Facade) PublishDefinitionVersion(ctx context.Context, projectID, definitionID, versionID, actorID string) (DefinitionRecord, error) {
	if err := f.authorize(ctx, projectID, actorID, core.ActionApprove); err != nil {
		return DefinitionRecord{}, err
	}
	record, err := f.Store.GetDefinitionVersion(ctx, versionID)
	if err != nil {
		return DefinitionRecord{}, err
	}
	if record.ProjectID != projectID || record.Definition.ID != definitionID {
		return DefinitionRecord{}, core.ErrNotFound
	}
	if record.ExecutionProfile != CurrentWorkflowExecutionProfileRef() || record.Definition.ExecutionProfile != record.ExecutionProfile {
		return DefinitionRecord{}, &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "workflow.executionProfile", Message: "only the current exact execution profile can be published"}
	}
	if err := validateAuthoredDefinition(record.Definition, f.Engine.Capabilities); err != nil {
		return DefinitionRecord{}, err
	}
	return f.Store.PublishDefinitionVersion(ctx, projectID, definitionID, versionID, actorID)
}

func validateAuthoredDefinition(definition domain.WorkflowDefinition, registries ...WorkflowCapabilities) error {
	_ = registries // Kept for source compatibility; profile descriptor is authoritative.
	return ValidateDefinitionForExecutionProfile(definition, CurrentWorkflowExecutionProfileRef())
}

func (f Facade) Start(ctx context.Context, projectID, actorID string, request StartRequest) (*RunRecord, error) {
	if err := f.authorize(ctx, projectID, actorID, core.ActionEdit); err != nil {
		return nil, err
	}
	request.ProjectID = projectID
	request.StartedBy = actorID
	if request.DefinitionVersionID == "" {
		definitions, err := f.ensureMinimumLoop(ctx, projectID, actorID)
		if err != nil {
			return nil, err
		}
		minimumDefinitionID := ""
		for _, definition := range definitions {
			if definition.Key == MinimumLoopKey {
				minimumDefinitionID = definition.Definition.ID
				break
			}
		}
		if minimumDefinitionID != "" {
			versions, err := f.Store.ListDefinitionVersions(ctx, minimumDefinitionID)
			if err != nil {
				return nil, err
			}
			for index := len(versions) - 1; index >= 0; index-- {
				if versions[index].Published {
					request.DefinitionVersionID = versions[index].VersionID
					break
				}
			}
		}
		if request.DefinitionVersionID == "" {
			return nil, fmt.Errorf("minimum workflow is not installed")
		}
	}
	return f.Engine.Start(ctx, request)
}

func (f Facade) ensureMinimumLoop(ctx context.Context, projectID, actorID string) ([]DefinitionRecord, error) {
	_ = actorID
	// Project creation and the explicit startup provisioner own built-in
	// installation/upgrades. Read/list and default-start selection never write.
	return f.Store.ListDefinitions(ctx, projectID)
}
func (f Facade) GetRun(ctx context.Context, projectID, runID, actorID string) (*RunRecord, error) {
	if err := f.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return nil, err
	}
	run, err := f.Store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.ProjectID != projectID {
		return nil, core.ErrNotFound
	}
	return run, nil
}

func (f Facade) ListRuns(ctx context.Context, projectID, actorID string, options RunListOptions) (RunPage, error) {
	if err := f.authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return RunPage{}, err
	}
	if options.Status != "" && !validRunStatus(options.Status) {
		return RunPage{}, domain.ErrInvalidArgument
	}
	if options.Limit == 0 {
		options.Limit = 25
	}
	if options.Limit < 1 || options.Limit > 100 {
		return RunPage{}, domain.ErrInvalidArgument
	}
	filter := StoreRunFilter{Status: options.Status, Limit: options.Limit + 1}
	if options.Cursor != "" {
		createdAt, id, err := decodeRunCursor(options.Cursor)
		if err != nil {
			return RunPage{}, domain.ErrInvalidArgument
		}
		filter.BeforeCreatedAt, filter.BeforeID = &createdAt, id
	}
	items, err := f.Store.ListRuns(ctx, projectID, filter)
	if err != nil {
		return RunPage{}, err
	}
	page := RunPage{Items: items}
	if len(items) > options.Limit {
		page.Items = items[:options.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeRunCursor(last.CreatedAt, last.ID)
	}
	if page.Items == nil {
		page.Items = []RunSummary{}
	}
	return page, nil
}

type runCursor struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

func encodeRunCursor(createdAt time.Time, id string) string {
	payload, _ := json.Marshal(runCursor{CreatedAt: createdAt.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeRunCursor(value string) (time.Time, string, error) {
	if len(value) > 1024 {
		return time.Time{}, "", domain.ErrInvalidArgument
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, "", err
	}
	var cursor runCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.CreatedAt.IsZero() {
		return time.Time{}, "", domain.ErrInvalidArgument
	}
	if _, err := uuid.Parse(cursor.ID); err != nil {
		return time.Time{}, "", domain.ErrInvalidArgument
	}
	return cursor.CreatedAt.UTC(), cursor.ID, nil
}

func validRunStatus(status RunStatus) bool {
	switch status {
	case RunPending, RunRunning, RunWaitingInput, RunWaitingReview, RunCompleted, RunFailed, RunCancelled, RunStale:
		return true
	default:
		return false
	}
}
func (f Facade) Events(ctx context.Context, projectID, runID, actorID string, after uint64, limit int) ([]Event, error) {
	if _, err := f.GetRun(ctx, projectID, runID, actorID); err != nil {
		return nil, err
	}
	return f.Store.ListEvents(ctx, runID, after, limit)
}

func (f Facade) Resume(ctx context.Context, projectID, runID, nodeKey, actorID string, output json.RawMessage) error {
	if err := f.authorize(ctx, projectID, actorID, core.ActionEdit); err != nil {
		return err
	}
	run, err := f.GetRun(ctx, projectID, runID, actorID)
	if err != nil {
		return err
	}
	if err := f.requireNodeRole(ctx, actorID, run, nodeKey); err != nil {
		return err
	}
	return f.Engine.SubmitHumanInput(ctx, runID, nodeKey, output, actorID)
}

// AuthorizeExecution is the only transport-facing path that can mint the
// actor provenance consumed by quality and publish workers. Identity comes
// from the authenticated session; the request body contains only a node key.
func (f Facade) AuthorizeExecution(ctx context.Context, projectID, runID, nodeKey, actorID string) error {
	run, err := f.GetRun(ctx, projectID, runID, actorID)
	if err != nil {
		return err
	}
	_, definition, err := f.nodeDefinition(ctx, run, nodeKey)
	if err != nil {
		return err
	}
	action, requiredRole, required := nodeExecutionPolicy(definition)
	if !required {
		return &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "node", Message: "node does not accept privileged execution authorization"}
	}
	role, err := f.Access.Authorize(ctx, projectID, actorID, action)
	if err != nil {
		return err
	}
	if !workflowRoleSatisfies(role, requiredRole) {
		return core.ErrForbidden
	}
	return f.Engine.AuthorizeNodeExecution(ctx, runID, nodeKey, ActorProvenance{
		ActorID: actorID, Role: role, Action: action,
		Source: ActorSourceAuthenticatedCommand, AuthorizedAt: f.Engine.now(),
	})
}

func (f Facade) RecordProposal(ctx context.Context, projectID, runID, nodeKey, actorID string, proposal domain.ProposalRef) error {
	if err := f.authorize(ctx, projectID, actorID, core.ActionEdit); err != nil {
		return err
	}
	if _, err := f.GetRun(ctx, projectID, runID, actorID); err != nil {
		return err
	}
	return f.Engine.RecordProposal(ctx, runID, nodeKey, proposal, actorID)
}
func (f Facade) ResolveReview(ctx context.Context, projectID, runID, nodeKey, actorID string, resolution ReviewResolution, reason string) error {
	if err := f.validate(); err != nil {
		return err
	}
	action := core.ActionReview
	if resolution == ReviewApprove || resolution == ReviewWaive {
		action = core.ActionApprove
	}
	role, err := f.Access.Authorize(ctx, projectID, actorID, action)
	if err != nil {
		return err
	}
	run, err := f.GetRun(ctx, projectID, runID, actorID)
	if err != nil {
		return err
	}
	if err := f.requireNodeRole(ctx, actorID, run, nodeKey); err != nil {
		return err
	}
	now := f.Engine.now()
	decision := ReviewDecision{
		Resolution: resolution, Reason: reason,
		Actor:                   ActorProvenance{ActorID: actorID, Role: role, Action: action, Source: ActorSourceAuthenticatedCommand, AuthorizedAt: now},
		ExecutionAuthorizations: map[string]ActorProvenance{},
	}
	if resolution == ReviewApprove || resolution == ReviewWaive {
		node, definition, loadErr := f.nodeDefinition(ctx, run, nodeKey)
		if loadErr != nil {
			return loadErr
		}
		record, loadErr := f.Store.GetDefinitionVersion(ctx, run.DefinitionVersionID)
		if loadErr != nil {
			return loadErr
		}
		source := ActorSourceReviewApproval
		if resolution == ReviewWaive {
			source = ActorSourceReviewWaiver
		}
		for _, edge := range record.Definition.Outgoing(definition.ID) {
			if run.Context.DisabledEdges[disabledEdgeKey(edge.ID, node.SliceID)] {
				continue
			}
			successorKey := edge.To
			if node.SliceID != "" {
				if _, exists := run.Nodes[instanceKey(edge.To, node.SliceID)]; exists {
					successorKey = instanceKey(edge.To, node.SliceID)
				}
			}
			successor := run.Nodes[successorKey]
			if successor == nil || successor.Status != NodePending {
				continue
			}
			successorDefinition, exists := record.Definition.FindNode(successor.DefinitionNodeID)
			if !exists {
				return core.ErrNotFound
			}
			requiredAction, requiredRole, required := nodeExecutionPolicy(successorDefinition)
			if !required {
				continue
			}
			executionRole, authorizeErr := f.Access.Authorize(ctx, projectID, actorID, requiredAction)
			if errors.Is(authorizeErr, core.ErrForbidden) {
				continue
			}
			if authorizeErr != nil {
				return authorizeErr
			}
			if !workflowRoleSatisfies(executionRole, requiredRole) {
				continue
			}
			decision.ExecutionAuthorizations[successorKey] = ActorProvenance{
				ActorID: actorID, Role: executionRole, Action: requiredAction,
				Source: source, AuthorizedAt: now,
			}
		}
	}
	return f.Engine.ResolveReview(ctx, runID, nodeKey, decision)
}
func (f Facade) Cancel(ctx context.Context, projectID, runID, actorID, reason string) error {
	if err := f.authorize(ctx, projectID, actorID, core.ActionEdit); err != nil {
		return err
	}
	if _, err := f.GetRun(ctx, projectID, runID, actorID); err != nil {
		return err
	}
	return f.Engine.Cancel(ctx, runID, actorID, reason)
}
func (f Facade) Retry(ctx context.Context, projectID, runID, nodeKey, actorID, reason string) error {
	if err := f.authorize(ctx, projectID, actorID, core.ActionEdit); err != nil {
		return err
	}
	if _, err := f.GetRun(ctx, projectID, runID, actorID); err != nil {
		return err
	}
	return f.Engine.RetryNode(ctx, runID, nodeKey, actorID, reason)
}
func (f Facade) Waive(ctx context.Context, projectID, runID, nodeKey, actorID, reason string) error {
	if err := f.validate(); err != nil {
		return err
	}
	role, err := f.Access.Authorize(ctx, projectID, actorID, core.ActionApprove)
	if err != nil {
		return err
	}
	run, err := f.GetRun(ctx, projectID, runID, actorID)
	if err != nil {
		return err
	}
	if err := f.requireNodeRole(ctx, actorID, run, nodeKey); err != nil {
		return err
	}
	return f.Engine.WaiveNode(ctx, runID, nodeKey, ActorProvenance{
		ActorID: actorID, Role: role, Action: core.ActionApprove,
		Source: ActorSourceAuthenticatedCommand, AuthorizedAt: f.Engine.now(),
	}, reason)
}

func (f Facade) nodeDefinition(ctx context.Context, run *RunRecord, nodeKey string) (*NodeRecord, domain.NodeDefinition, error) {
	if run == nil {
		return nil, domain.NodeDefinition{}, core.ErrNotFound
	}
	node := run.Nodes[nodeKey]
	if node == nil {
		return nil, domain.NodeDefinition{}, core.ErrNotFound
	}
	record, err := f.Store.GetDefinitionVersion(ctx, run.DefinitionVersionID)
	if err != nil {
		return nil, domain.NodeDefinition{}, err
	}
	definition, exists := record.Definition.FindNode(node.DefinitionNodeID)
	if !exists {
		return nil, domain.NodeDefinition{}, core.ErrNotFound
	}
	return node, definition, nil
}

func (f Facade) requireNodeRole(ctx context.Context, actorID string, run *RunRecord, nodeKey string) error {
	_, definition, err := f.nodeDefinition(ctx, run, nodeKey)
	if err != nil {
		return err
	}
	required := ""
	if definition.HumanEdit != nil {
		required = definition.HumanEdit.RequiredRole
	} else if definition.HumanTask != nil {
		required = definition.HumanTask.RequiredRole
	} else if definition.ReviewGate != nil {
		required = definition.ReviewGate.RequiredRole
	} else if definition.Approval != nil {
		required = definition.Approval.RequiredRole
	} else if definition.QualityGate != nil {
		required = definition.QualityGate.RequiredRole
		if strings.TrimSpace(required) == "" {
			required = string(core.RoleEditor)
		}
	} else if definition.Publish != nil {
		required = definition.Publish.RequiredRole
	}
	if required == "" {
		return nil
	}
	actual, err := f.Access.Authorize(ctx, run.ProjectID, actorID, core.ActionView)
	if err != nil {
		return err
	}
	if !workflowRoleSatisfies(actual, core.Role(required)) {
		return core.ErrForbidden
	}
	return nil
}

func workflowRoleSatisfies(actual, required core.Role) bool {
	rank := map[core.Role]int{
		core.RoleViewer: 1, core.RoleCommenter: 2, core.RoleEditor: 3,
		core.RoleAdmin: 4, core.RoleOwner: 5,
	}
	actualRank, actualValid := rank[actual]
	requiredRank, requiredValid := rank[required]
	return actualValid && requiredValid && actualRank >= requiredRank
}
