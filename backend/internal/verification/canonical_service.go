package verification

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

type CanonicalControlStore interface {
	ListActiveCanonicalProfiles(context.Context, string, CanonicalPlanSubject) ([]ProfileSummary, error)
	SaveCanonicalPlan(context.Context, string, string, CompiledCanonicalPlan) (CanonicalPlan, error)
	GetCanonicalPlan(context.Context, string, string) (CanonicalPlan, error)
	CreateCanonicalRun(context.Context, CreateCanonicalRunInput) (CanonicalRun, error)
	FindCanonicalRunByRequest(context.Context, string, string) (CanonicalRun, bool, error)
	GetCanonicalRun(context.Context, string, string) (CanonicalRun, error)
	ListCanonicalRuns(context.Context, string, CanonicalPlanSubject, int) ([]CanonicalRun, error)
	GetCanonicalReceipt(context.Context, string, string, string) (CanonicalReceipt, error)
	GetCanonicalReceiptByRun(context.Context, string, string) (CanonicalReceipt, error)
}

type CreateCanonicalRunRequest struct {
	ProjectID         string               `json:"projectId"`
	WorkspaceRevision CanonicalPlanSubject `json:"workspaceRevision"`
	Profile           ProfileReference     `json:"verificationProfile"`
	Reason            string               `json:"reason"`
	ActorID           string               `json:"-"`
	OperationID       string               `json:"-"`
}

type CanonicalRunView struct {
	Run               CanonicalRun               `json:"run"`
	Subject           CanonicalPlanSubject       `json:"subject"`
	BuildManifest     repository.ExactReference  `json:"buildManifest"`
	BuildContract     repository.ExactReference  `json:"buildContract"`
	FullStackTemplate repository.ExactReference  `json:"fullStackTemplate"`
	Profile           ProfileReference           `json:"verificationProfile"`
	Receipt           *repository.ExactReference `json:"receipt"`
	AllowedActions    []RunAction                `json:"allowedActions"`
	BlockingReasons   []RunBlockingReason        `json:"blockingReasons"`
}

type CanonicalControlService struct {
	store    CanonicalControlStore
	source   CanonicalPlanSource
	compiler PlanCompiler
	access   ProjectAuthorizer
}

func NewCanonicalControlService(
	store CanonicalControlStore,
	source CanonicalPlanSource,
	access ProjectAuthorizer,
) (*CanonicalControlService, error) {
	if store == nil || source == nil || access == nil {
		return nil, errors.New("canonical verification store, Plan source, and authorizer are required")
	}
	return &CanonicalControlService{store: store, source: source, compiler: PlanCompiler{}, access: access}, nil
}

func (service *CanonicalControlService) ListCanonicalProfiles(
	ctx context.Context,
	projectID string,
	subject CanonicalPlanSubject,
	actorID string,
) ([]ProfileSummary, error) {
	if !validUUIDs(projectID, actorID) {
		return nil, runInvalid("Canonical VerificationProfile project or actor")
	}
	if normalized, err := normalizeCanonicalPlanSubject(subject); err != nil || normalized != subject {
		return nil, runInvalid("Canonical VerificationProfile WorkspaceRevision")
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return nil, fmt.Errorf("authorize Canonical VerificationProfile catalog: %w", err)
	}
	profiles, err := service.store.ListActiveCanonicalProfiles(ctx, projectID, subject)
	if profiles == nil && err == nil {
		profiles = []ProfileSummary{}
	}
	return profiles, err
}

func (service *CanonicalControlService) CreateCanonicalRun(
	ctx context.Context,
	request CreateCanonicalRunRequest,
) (CanonicalRunView, error) {
	request, err := normalizeCreateCanonicalRunRequest(request)
	if err != nil {
		return CanonicalRunView{}, err
	}
	if err := service.access.RequireProjectEdit(ctx, request.ProjectID, request.ActorID); err != nil {
		return CanonicalRunView{}, fmt.Errorf("authorize Canonical VerificationRun creation: %w", err)
	}
	if existing, found, findErr := service.store.FindCanonicalRunByRequest(ctx, request.ProjectID, request.OperationID); findErr != nil {
		return CanonicalRunView{}, findErr
	} else if found {
		return service.recoverCanonicalCreate(ctx, request, existing)
	}
	compileInput, err := service.source.LoadCanonicalPlan(ctx, CanonicalPlanRequest{
		ProjectID: request.ProjectID, WorkspaceRevision: request.WorkspaceRevision,
		Profile: request.Profile, ActorID: request.ActorID,
	})
	if err != nil {
		return CanonicalRunView{}, err
	}
	compiled, err := service.compiler.CompileCanonical(compileInput)
	if err != nil {
		return CanonicalRunView{}, fmt.Errorf("%w: compile exact Canonical VerificationPlan: %v", ErrCanonicalPlanningBlocked, err)
	}
	planID, runID := canonicalVerificationOperationIDs(request.ProjectID, request.ActorID, request.OperationID)
	plan, err := service.store.SaveCanonicalPlan(ctx, planID, request.ActorID, compiled)
	if err != nil {
		if errors.Is(err, ErrCanonicalPlanConflict) {
			return CanonicalRunView{}, ErrRunIdempotencyConflict
		}
		return CanonicalRunView{}, err
	}
	create, err := PrepareCreateCanonicalRunInput(CreateCanonicalRunInput{
		ID: runID, ProjectID: request.ProjectID,
		Plan:       PlanReference{ID: plan.ID, ContentHash: plan.PlanHash},
		RequestKey: request.OperationID, Reason: request.Reason, CreatedBy: request.ActorID,
	})
	if err != nil {
		return CanonicalRunView{}, err
	}
	run, err := service.store.CreateCanonicalRun(ctx, create)
	if err != nil {
		return CanonicalRunView{}, err
	}
	view, err := service.canonicalRunView(ctx, run, plan)
	if err == nil {
		view.Run.Replayed = plan.Replayed || run.Replayed
	}
	return view, err
}

func (service *CanonicalControlService) GetCanonicalRun(
	ctx context.Context,
	projectID, runID, actorID string,
) (CanonicalRunView, error) {
	if !validUUIDs(projectID, runID, actorID) {
		return CanonicalRunView{}, runInvalid("Canonical Run project, identity, or actor")
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return CanonicalRunView{}, fmt.Errorf("authorize Canonical VerificationRun view: %w", err)
	}
	run, err := service.store.GetCanonicalRun(ctx, projectID, runID)
	if err != nil {
		return CanonicalRunView{}, err
	}
	plan, err := service.store.GetCanonicalPlan(ctx, projectID, run.Plan.ID)
	if err != nil || plan.PlanHash != run.Plan.ContentHash {
		return CanonicalRunView{}, runIntegrity("Canonical Run Plan", err)
	}
	return service.canonicalRunView(ctx, run, plan)
}

func (service *CanonicalControlService) ListCanonicalRuns(
	ctx context.Context,
	projectID string,
	subject CanonicalPlanSubject,
	actorID string,
	limit int,
) ([]CanonicalRunView, error) {
	if !validUUIDs(projectID, actorID) || limit < 1 || limit > 100 {
		return nil, runInvalid("Canonical VerificationRun list request")
	}
	if normalized, err := normalizeCanonicalPlanSubject(subject); err != nil || normalized != subject {
		return nil, runInvalid("Canonical VerificationRun list WorkspaceRevision")
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return nil, fmt.Errorf("authorize Canonical VerificationRun list: %w", err)
	}
	runs, err := service.store.ListCanonicalRuns(ctx, projectID, subject, limit)
	if err != nil {
		return nil, err
	}
	views := make([]CanonicalRunView, 0, len(runs))
	for _, run := range runs {
		plan, err := service.store.GetCanonicalPlan(ctx, projectID, run.Plan.ID)
		if err != nil || plan.PlanHash != run.Plan.ContentHash || plan.Content.Subject != subject {
			return nil, runIntegrity("Canonical Run list Plan", err)
		}
		view, err := service.canonicalRunView(ctx, run, plan)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (service *CanonicalControlService) recoverCanonicalCreate(
	ctx context.Context,
	request CreateCanonicalRunRequest,
	run CanonicalRun,
) (CanonicalRunView, error) {
	plan, err := service.store.GetCanonicalPlan(ctx, request.ProjectID, run.Plan.ID)
	if err != nil {
		return CanonicalRunView{}, err
	}
	if run.Plan.ContentHash != plan.PlanHash || run.CreatedBy != request.ActorID || run.Reason != request.Reason ||
		plan.Content.ProjectID != request.ProjectID || plan.Content.Subject != request.WorkspaceRevision ||
		plan.Content.Profile != request.Profile {
		return CanonicalRunView{}, ErrRunIdempotencyConflict
	}
	view, err := service.canonicalRunView(ctx, run, plan)
	if err == nil {
		view.Run.Replayed = true
	}
	return view, err
}

func (service *CanonicalControlService) canonicalRunView(
	ctx context.Context,
	run CanonicalRun,
	plan CanonicalPlan,
) (CanonicalRunView, error) {
	view := CanonicalRunView{
		Run: run, Subject: plan.Content.Subject,
		BuildManifest: plan.Content.BuildManifest, BuildContract: plan.Content.BuildContract,
		FullStackTemplate: plan.Content.FullStackTemplate, Profile: plan.Content.Profile,
		AllowedActions: []RunAction{}, BlockingReasons: []RunBlockingReason{},
	}
	if runStateIsTerminal(run.State) {
		receipt, err := service.store.GetCanonicalReceiptByRun(ctx, run.ProjectID, run.ID)
		if err == nil && RunState(receipt.Decision) == run.State {
			view.Receipt = &repository.ExactReference{ID: receipt.ID, ContentHash: receipt.PayloadHash}
			view.AllowedActions = append(view.AllowedActions, RunActionViewReceipt)
			if receipt.Decision == DecisionPassed {
				view.AllowedActions = append(view.AllowedActions, RunActionCreateReleaseBundle)
			}
		} else if err != nil && !errors.Is(err, ErrCanonicalReceiptNotFound) {
			return CanonicalRunView{}, err
		} else {
			view.BlockingReasons = append(view.BlockingReasons, RunBlockingReason{
				Code: RunBlockingReceiptMissing, Detail: "The Canonical Run has not exposed an exact finalized Receipt.",
			})
		}
		if run.State != RunPassed {
			view.BlockingReasons = append(view.BlockingReasons, RunBlockingReason{
				Code: RunBlockingFailed, Detail: "Canonical verification did not produce a passing release authority.",
			})
		}
	}
	return view, nil
}

func normalizeCreateCanonicalRunRequest(request CreateCanonicalRunRequest) (CreateCanonicalRunRequest, error) {
	request.Reason = strings.TrimSpace(request.Reason)
	request.OperationID = strings.TrimSpace(request.OperationID)
	subject, subjectErr := normalizeCanonicalPlanSubject(request.WorkspaceRevision)
	profile, profileErr := normalizeProfile(request.Profile)
	if subjectErr != nil || subject != request.WorkspaceRevision || profileErr != nil || profile != request.Profile ||
		!validUUIDs(request.ProjectID, request.ActorID) || request.Reason == "" || len(request.Reason) > 1000 ||
		strings.ContainsRune(request.Reason, '\x00') || request.OperationID == "" || len(request.OperationID) > 128 ||
		strings.ContainsRune(request.OperationID, '\x00') {
		return CreateCanonicalRunRequest{}, runInvalid("Canonical VerificationRun request")
	}
	return request, nil
}

func canonicalVerificationOperationIDs(projectID, actorID, operationID string) (string, string) {
	identity := projectID + "\x00" + actorID + "\x00" + operationID
	planID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("canonical-verification-plan\x00"+identity))
	runID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("canonical-verification-run\x00"+identity))
	return planID.String(), runID.String()
}

var _ CanonicalControlStore = (*PostgresStore)(nil)
