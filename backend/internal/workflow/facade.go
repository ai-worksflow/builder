package workflow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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
	Key           string                  `json:"key"`
	Title         string                  `json:"title"`
	Description   string                  `json:"description,omitempty"`
	Name          string                  `json:"name,omitempty"`
	SchemaVersion string                  `json:"schemaVersion,omitempty"`
	Nodes         []domain.NodeDefinition `json:"nodes"`
	Edges         []domain.WorkflowEdge   `json:"edges"`
}

type CreateDefinitionVersionInput struct {
	Name          string                  `json:"name,omitempty"`
	SchemaVersion string                  `json:"schemaVersion,omitempty"`
	Nodes         []domain.NodeDefinition `json:"nodes"`
	Edges         []domain.WorkflowEdge   `json:"edges"`
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
	definition, err := domain.NewWorkflowDefinition(uuid.NewString(), 1, input.Name, input.SchemaVersion, input.Nodes, input.Edges, actorID, time.Now().UTC())
	if err != nil {
		return DefinitionRecord{}, err
	}
	if err := validateAuthoredDefinition(definition); err != nil {
		return DefinitionRecord{}, err
	}
	record := DefinitionRecord{
		VersionID: uuid.NewString(), ProjectID: projectID, Key: input.Key,
		Title: input.Title, Description: input.Description, Published: false, Definition: definition,
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
	definition, err := domain.NewWorkflowDefinition(
		latest.Definition.ID, latest.Definition.Version+1, input.Name, input.SchemaVersion,
		input.Nodes, input.Edges, actorID, time.Now().UTC(),
	)
	if err != nil {
		return DefinitionRecord{}, err
	}
	if err := validateAuthoredDefinition(definition); err != nil {
		return DefinitionRecord{}, err
	}
	record := DefinitionRecord{
		VersionID: uuid.NewString(), ProjectID: projectID, Key: latest.Key,
		Title: latest.Title, Description: latest.Description, Published: false, Definition: definition,
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
	if err := validateAuthoredDefinition(record.Definition); err != nil {
		return DefinitionRecord{}, err
	}
	return f.Store.PublishDefinitionVersion(ctx, projectID, definitionID, versionID, actorID)
}

func validateAuthoredDefinition(definition domain.WorkflowDefinition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	if len(definition.Nodes) > 200 || len(definition.Edges) > 1000 {
		return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow", Message: "workflow exceeds 200 nodes or 1000 edges"}
	}
	for _, node := range definition.Nodes {
		switch node.Type {
		case domain.NodeArtifactInput, domain.NodeAITransform, domain.NodeHumanEdit, domain.NodeReviewGate,
			domain.NodeCondition, domain.NodeFanOut, domain.NodeMerge, domain.NodeQualityGate,
			domain.NodeManifestCompiler, domain.NodeWorkbenchBuild, domain.NodePublish:
		default:
			return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.nodes." + node.ID, Message: "legacy node type cannot be used in a new workflow version"}
		}
		for _, requiredRole := range workflowNodeRoles(node) {
			if !validWorkflowRole(requiredRole) {
				return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.nodes." + node.ID, Message: "requiredRole is not a project role"}
			}
		}
		if node.Condition != nil {
			for _, branch := range node.Condition.Branches {
				if branch.Default {
					continue
				}
				if len(branch.Expression) > maxConditionExpressionBytes {
					return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.nodes." + node.ID, Message: "condition expression is too large"}
				}
				if _, err := evaluateConditionRule(json.RawMessage(branch.Expression), map[string]any{}, 0); err != nil {
					return &domain.DomainError{Kind: domain.ErrValidation, Field: "workflow.nodes." + node.ID, Message: err.Error()}
				}
			}
		}
	}
	return nil
}

func workflowNodeRoles(node domain.NodeDefinition) []string {
	roles := make([]string, 0, 1)
	if node.HumanEdit != nil {
		roles = append(roles, node.HumanEdit.RequiredRole)
	}
	if node.ReviewGate != nil {
		roles = append(roles, node.ReviewGate.RequiredRole)
	}
	if node.Publish != nil {
		roles = append(roles, node.Publish.RequiredRole)
	}
	return roles
}

func validWorkflowRole(role string) bool {
	switch core.Role(strings.TrimSpace(role)) {
	case core.RoleOwner, core.RoleAdmin, core.RoleEditor, core.RoleCommenter, core.RoleViewer:
		return true
	default:
		return false
	}
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
	records, err := f.Store.ListDefinitions(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.Key == MinimumLoopKey {
			return records, nil
		}
	}
	role, err := f.Access.Authorize(ctx, projectID, actorID, core.ActionAdmin)
	if err != nil {
		if errors.Is(err, core.ErrForbidden) {
			return records, nil
		}
		return nil, err
	}
	if role != core.RoleOwner {
		// Viewing workflows should not fail merely because a non-owner reached a
		// newly-created project before its owner installed built-ins.
		return records, nil
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, core.ErrInvalidInput
	}
	definitionID := uuid.NewSHA1(projectUUID, []byte("worksflow:minimum-loop:definition")).String()
	versionID := uuid.NewSHA1(projectUUID, []byte("worksflow:minimum-loop:version:1")).String()
	_, seedErr := SeedMinimumLoop(ctx, f.Store, MinimumLoopSeed{
		DefinitionID: definitionID, VersionID: versionID, ProjectID: projectID,
		InstallerUserID: actorID, Published: true,
	}, time.Now().UTC())
	if seedErr != nil {
		// A concurrent owner may have installed the same deterministic version.
		reloaded, reloadErr := f.Store.ListDefinitions(ctx, projectID)
		if reloadErr == nil {
			for _, record := range reloaded {
				if record.Key == MinimumLoopKey {
					return reloaded, nil
				}
			}
		}
		return nil, seedErr
	}
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
	action := core.ActionReview
	if resolution == ReviewApprove || resolution == ReviewWaive {
		action = core.ActionApprove
	}
	if err := f.authorize(ctx, projectID, actorID, action); err != nil {
		return err
	}
	run, err := f.GetRun(ctx, projectID, runID, actorID)
	if err != nil {
		return err
	}
	if err := f.requireNodeRole(ctx, actorID, run, nodeKey); err != nil {
		return err
	}
	return f.Engine.ResolveReview(ctx, runID, nodeKey, actorID, resolution, reason)
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
	if err := f.authorize(ctx, projectID, actorID, core.ActionApprove); err != nil {
		return err
	}
	run, err := f.GetRun(ctx, projectID, runID, actorID)
	if err != nil {
		return err
	}
	if err := f.requireNodeRole(ctx, actorID, run, nodeKey); err != nil {
		return err
	}
	return f.Engine.WaiveNode(ctx, runID, nodeKey, actorID, reason)
}

func (f Facade) requireNodeRole(ctx context.Context, actorID string, run *RunRecord, nodeKey string) error {
	if run == nil {
		return core.ErrNotFound
	}
	node := run.Nodes[nodeKey]
	if node == nil {
		return core.ErrNotFound
	}
	record, err := f.Store.GetDefinitionVersion(ctx, run.DefinitionVersionID)
	if err != nil {
		return err
	}
	definition, exists := record.Definition.FindNode(node.DefinitionNodeID)
	if !exists {
		return core.ErrNotFound
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
