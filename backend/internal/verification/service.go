package verification

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

var (
	ErrCandidatePlanningBlocked = errors.New("candidate verification planning is blocked")
	ErrCandidatePlanningDrift   = errors.New("candidate verification planning source changed")
)

type ProjectAuthorizer interface {
	RequireProjectView(context.Context, string, string) error
	RequireProjectEdit(context.Context, string, string) error
}

type CandidatePlanRequest struct {
	ProjectID                string
	SessionID                string
	CandidateID              string
	CheckpointID             string
	ExpectedSessionVersion   uint64
	ExpectedSessionEpoch     uint64
	ExpectedCandidateVersion uint64
	ExpectedWriterLeaseEpoch uint64
	Profile                  ProfileReference
	ActorID                  string
}

type CandidatePlanSource interface {
	LoadCandidatePlan(context.Context, CandidatePlanRequest) (CompileCandidatePlanInput, error)
}

type ControlStore interface {
	ListActiveProfiles(context.Context, string, string) ([]ProfileSummary, error)
	SavePlan(context.Context, string, string, CompiledPlan) (Plan, error)
	GetPlan(context.Context, string, string) (Plan, error)
	CreateRun(context.Context, CreateRunInput) (Run, error)
	FindRunByRequest(context.Context, string, string) (Run, bool, error)
	GetRun(context.Context, string, string) (Run, error)
	GetRunView(context.Context, string, string) (RunView, error)
	ListRunViewsForSession(context.Context, string, string, int) ([]RunView, error)
	ResolveRunProject(context.Context, string) (string, error)
	ResolveReceiptProject(context.Context, string) (string, error)
	GetReceipt(context.Context, string, string) (Receipt, error)
	CancelRun(context.Context, CancelRunInput) (RunView, error)
}

type CreateCandidateRunRequest struct {
	ProjectID                string           `json:"projectId"`
	SessionID                string           `json:"sessionId"`
	CandidateID              string           `json:"candidateId"`
	CheckpointID             string           `json:"checkpointId"`
	ExpectedSessionVersion   uint64           `json:"expectedSessionVersion"`
	ExpectedSessionEpoch     uint64           `json:"expectedSessionEpoch"`
	ExpectedCandidateVersion uint64           `json:"expectedCandidateVersion"`
	ExpectedWriterLeaseEpoch uint64           `json:"expectedWriterLeaseEpoch"`
	VerificationProfile      ProfileReference `json:"verificationProfile"`
	Reason                   string           `json:"reason"`
	ActorID                  string           `json:"-"`
	OperationID              string           `json:"-"`
}

type RetryCandidateRunRequest struct {
	ProjectID   string `json:"projectId"`
	ParentRunID string `json:"parentRunId"`
	Reason      string `json:"reason"`
	ActorID     string `json:"-"`
	OperationID string `json:"-"`
}

type CancelCandidateRunRequest struct {
	ProjectID          string `json:"projectId"`
	RunID              string `json:"runId"`
	ExpectedVersion    uint64 `json:"expectedVersion"`
	ExpectedFenceEpoch uint64 `json:"expectedFenceEpoch"`
	Reason             string `json:"reason"`
	ActorID            string `json:"-"`
}

type ControlService struct {
	store    ControlStore
	source   CandidatePlanSource
	compiler PlanCompiler
	access   ProjectAuthorizer
}

func NewControlService(
	store ControlStore,
	source CandidatePlanSource,
	access ProjectAuthorizer,
) (*ControlService, error) {
	if store == nil || source == nil || access == nil {
		return nil, errors.New("verification store, Candidate Plan source, and authorizer are required")
	}
	return &ControlService{store: store, source: source, compiler: PlanCompiler{}, access: access}, nil
}

func (service *ControlService) CreateCandidateRun(
	ctx context.Context,
	request CreateCandidateRunRequest,
) (RunView, error) {
	request, err := normalizeCreateCandidateRunRequest(request)
	if err != nil {
		return RunView{}, err
	}
	if err := service.access.RequireProjectEdit(ctx, request.ProjectID, request.ActorID); err != nil {
		return RunView{}, fmt.Errorf("authorize Candidate VerificationRun creation: %w", err)
	}
	if existing, found, findErr := service.store.FindRunByRequest(
		ctx, request.ProjectID, request.OperationID,
	); findErr != nil {
		return RunView{}, findErr
	} else if found {
		return service.recoverCandidateCreate(ctx, request, existing)
	}

	compileInput, err := service.source.LoadCandidatePlan(ctx, candidatePlanRequest(request))
	if err != nil {
		return RunView{}, err
	}
	compiled, err := service.compiler.Compile(compileInput)
	if err != nil {
		return RunView{}, fmt.Errorf("%w: compile exact VerificationPlan: %v", ErrCandidatePlanningBlocked, err)
	}
	planID, runID := verificationOperationIDs(request.ProjectID, request.ActorID, request.OperationID)
	plan, err := service.store.SavePlan(ctx, planID, request.ActorID, compiled)
	if err != nil {
		if errors.Is(err, ErrPlanConflict) {
			return RunView{}, ErrRunIdempotencyConflict
		}
		return RunView{}, err
	}
	create, err := PrepareCreateRunInput(CreateRunInput{
		ID: runID, ProjectID: request.ProjectID,
		Plan:       PlanReference{ID: plan.ID, ContentHash: plan.PlanHash},
		RequestKey: request.OperationID, Reason: request.Reason, CreatedBy: request.ActorID,
	})
	if err != nil {
		return RunView{}, err
	}
	run, err := service.store.CreateRun(ctx, create)
	if err != nil {
		return RunView{}, err
	}
	view, err := service.store.GetRunView(ctx, request.ProjectID, run.ID)
	if err == nil {
		view.Run.Replayed = plan.Replayed || run.Replayed
	}
	return view, err
}

func (service *ControlService) ListActiveProfiles(
	ctx context.Context,
	projectID, sessionID, actorID string,
) ([]ProfileSummary, error) {
	if !validUUIDs(projectID, sessionID, actorID) {
		return nil, runInvalid("project or actor identity")
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return nil, fmt.Errorf("authorize VerificationProfile list: %w", err)
	}
	profiles, err := service.store.ListActiveProfiles(ctx, projectID, sessionID)
	if err != nil {
		return nil, err
	}
	if profiles == nil {
		profiles = []ProfileSummary{}
	}
	return profiles, nil
}

func (service *ControlService) GetReceiptByID(
	ctx context.Context,
	receiptID, actorID string,
) (Receipt, error) {
	if !validUUIDs(receiptID, actorID) {
		return Receipt{}, fmt.Errorf("%w: Receipt or actor identity", ErrInvalidReceipt)
	}
	projectID, err := service.store.ResolveReceiptProject(ctx, receiptID)
	if err != nil {
		return Receipt{}, err
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return Receipt{}, fmt.Errorf("authorize Candidate VerificationReceipt view: %w", err)
	}
	receipt, err := service.store.GetReceipt(ctx, projectID, receiptID)
	if err != nil {
		return Receipt{}, err
	}
	if receipt.ID != receiptID || receipt.ProjectID != projectID {
		return Receipt{}, receiptIntegrity("Receipt service projection", nil)
	}
	return receipt, nil
}

func (service *ControlService) ListCandidateRunsForSession(
	ctx context.Context,
	projectID, sessionID, actorID string,
	limit int,
) ([]RunView, error) {
	if !validUUIDs(projectID, sessionID, actorID) || limit < 1 || limit > 100 {
		return nil, runInvalid("Run history project, SandboxSession, actor, or limit")
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return nil, fmt.Errorf("authorize Candidate VerificationRun history: %w", err)
	}
	runs, err := service.store.ListRunViewsForSession(ctx, projectID, sessionID, limit)
	if err != nil {
		return nil, err
	}
	if runs == nil {
		runs = []RunView{}
	}
	return runs, nil
}

func (service *ControlService) ListChecksForRunByID(
	ctx context.Context,
	runID, actorID string,
	offset, limit int,
) (CheckPage, error) {
	if !validUUIDs(runID, actorID) || offset < 0 || limit < 1 || limit > 100 {
		return CheckPage{}, runInvalid("Run check page identity, offset, or limit")
	}
	view, err := service.GetRunByID(ctx, runID, actorID)
	if err != nil {
		return CheckPage{}, err
	}
	if view.Receipt == nil {
		return CheckPage{}, ErrReceiptNotFound
	}
	receipt, err := service.store.GetReceipt(ctx, view.Run.ProjectID, view.Receipt.ID)
	if err != nil {
		return CheckPage{}, err
	}
	if receipt.RunID != view.Run.ID || receipt.PayloadHash != view.Receipt.ContentHash {
		return CheckPage{}, receiptIntegrity("Run check page Receipt identity", nil)
	}
	total := len(receipt.Checks)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	checks := append([]CheckResult(nil), receipt.Checks[start:end]...)
	if checks == nil {
		checks = []CheckResult{}
	}
	return CheckPage{
		RunID: view.Run.ID, Receipt: *view.Receipt,
		Offset: offset, Limit: limit, TotalCount: total, Checks: checks,
	}, nil
}

func (service *ControlService) GetRun(
	ctx context.Context,
	projectID, runID, actorID string,
) (RunView, error) {
	if !validUUIDs(projectID, runID, actorID) {
		return RunView{}, runInvalid("project, Run, or actor identity")
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return RunView{}, fmt.Errorf("authorize Candidate VerificationRun view: %w", err)
	}
	return service.store.GetRunView(ctx, projectID, runID)
}

func (service *ControlService) GetRunByID(
	ctx context.Context,
	runID, actorID string,
) (RunView, error) {
	if !validUUIDs(runID, actorID) {
		return RunView{}, runInvalid("Run or actor identity")
	}
	projectID, err := service.store.ResolveRunProject(ctx, runID)
	if err != nil {
		return RunView{}, err
	}
	return service.GetRun(ctx, projectID, runID, actorID)
}

func (service *ControlService) CancelCandidateRun(
	ctx context.Context,
	request CancelCandidateRunRequest,
) (RunView, error) {
	request.Reason = strings.TrimSpace(request.Reason)
	if !validUUIDs(request.ProjectID, request.RunID, request.ActorID) || request.ExpectedVersion == 0 ||
		request.Reason == "" || len(request.Reason) > 2000 || strings.ContainsRune(request.Reason, '\x00') {
		return RunView{}, runInvalid("cancel request")
	}
	if err := service.access.RequireProjectEdit(ctx, request.ProjectID, request.ActorID); err != nil {
		return RunView{}, fmt.Errorf("authorize Candidate VerificationRun cancellation: %w", err)
	}
	return service.store.CancelRun(ctx, CancelRunInput{
		ProjectID: request.ProjectID, RunID: request.RunID,
		ExpectedVersion: request.ExpectedVersion, ExpectedFenceEpoch: request.ExpectedFenceEpoch,
		ActorID: request.ActorID, Reason: request.Reason,
	})
}

func (service *ControlService) RetryCandidateRun(
	ctx context.Context,
	request RetryCandidateRunRequest,
) (RunView, error) {
	request, err := normalizeRetryCandidateRunRequest(request)
	if err != nil {
		return RunView{}, err
	}
	if err := service.access.RequireProjectEdit(ctx, request.ProjectID, request.ActorID); err != nil {
		return RunView{}, fmt.Errorf("authorize Candidate VerificationRun retry: %w", err)
	}
	if existing, found, findErr := service.store.FindRunByRequest(
		ctx, request.ProjectID, request.OperationID,
	); findErr != nil {
		return RunView{}, findErr
	} else if found {
		if existing.ParentRunID != request.ParentRunID || existing.RetryReason != request.Reason ||
			existing.CreatedBy != request.ActorID {
			return RunView{}, ErrRunIdempotencyConflict
		}
		view, err := service.store.GetRunView(ctx, request.ProjectID, existing.ID)
		if err == nil {
			view.Run.Replayed = true
		}
		return view, err
	}
	parent, err := service.store.GetRun(ctx, request.ProjectID, request.ParentRunID)
	if err != nil {
		return RunView{}, err
	}
	if parent.State == RunPassed || !runStateIsTerminal(parent.State) {
		return RunView{}, ErrRunTransition
	}
	_, runID := verificationOperationIDs(request.ProjectID, request.ActorID, request.OperationID)
	create, err := PrepareCreateRunInput(CreateRunInput{
		ID: runID, ProjectID: request.ProjectID, Plan: parent.Plan,
		RequestKey: request.OperationID, Reason: parent.Reason,
		ParentRunID: parent.ID, RetryReason: request.Reason, CreatedBy: request.ActorID,
	})
	if err != nil {
		return RunView{}, err
	}
	run, err := service.store.CreateRun(ctx, create)
	if err != nil {
		return RunView{}, err
	}
	view, err := service.store.GetRunView(ctx, request.ProjectID, run.ID)
	if err == nil {
		view.Run.Replayed = run.Replayed
	}
	return view, err
}

func (service *ControlService) recoverCandidateCreate(
	ctx context.Context,
	request CreateCandidateRunRequest,
	existing Run,
) (RunView, error) {
	plan, err := service.store.GetPlan(ctx, request.ProjectID, existing.Plan.ID)
	if err != nil {
		return RunView{}, err
	}
	subject := plan.Content.Subject
	if existing.Plan.ContentHash != plan.PlanHash || existing.CreatedBy != request.ActorID ||
		existing.Reason != request.Reason || plan.Content.ProjectID != request.ProjectID ||
		subject.SessionID != request.SessionID || subject.CandidateID != request.CandidateID ||
		subject.CandidateSnapshotID != request.CheckpointID ||
		subject.SessionVersion != request.ExpectedSessionVersion ||
		subject.SessionEpoch != request.ExpectedSessionEpoch ||
		subject.CandidateVersion != request.ExpectedCandidateVersion ||
		subject.WriterLeaseEpoch != request.ExpectedWriterLeaseEpoch ||
		plan.Content.Profile != request.VerificationProfile {
		return RunView{}, ErrRunIdempotencyConflict
	}
	view, err := service.store.GetRunView(ctx, request.ProjectID, existing.ID)
	if err == nil {
		view.Run.Replayed = true
	}
	return view, err
}

func normalizeCreateCandidateRunRequest(request CreateCandidateRunRequest) (CreateCandidateRunRequest, error) {
	request.Reason = strings.TrimSpace(request.Reason)
	request.OperationID = strings.TrimSpace(request.OperationID)
	profile, err := normalizeProfile(request.VerificationProfile)
	if err != nil || profile != request.VerificationProfile ||
		!validUUIDs(request.ProjectID, request.SessionID, request.CandidateID, request.CheckpointID, request.ActorID) ||
		request.ExpectedSessionVersion == 0 || request.ExpectedSessionEpoch == 0 ||
		request.ExpectedCandidateVersion == 0 || request.ExpectedWriterLeaseEpoch == 0 ||
		request.Reason == "" || len(request.Reason) > 1000 || strings.ContainsRune(request.Reason, '\x00') ||
		request.OperationID == "" || len(request.OperationID) > 128 || strings.ContainsRune(request.OperationID, '\x00') {
		return CreateCandidateRunRequest{}, runInvalid("Candidate VerificationRun request")
	}
	return request, nil
}

func normalizeRetryCandidateRunRequest(request RetryCandidateRunRequest) (RetryCandidateRunRequest, error) {
	request.Reason = strings.TrimSpace(request.Reason)
	request.OperationID = strings.TrimSpace(request.OperationID)
	if !validUUIDs(request.ProjectID, request.ParentRunID, request.ActorID) ||
		request.Reason == "" || len(request.Reason) > 1000 || strings.ContainsRune(request.Reason, '\x00') ||
		request.OperationID == "" || len(request.OperationID) > 128 || strings.ContainsRune(request.OperationID, '\x00') {
		return RetryCandidateRunRequest{}, runInvalid("Candidate VerificationRun retry request")
	}
	return request, nil
}

func candidatePlanRequest(request CreateCandidateRunRequest) CandidatePlanRequest {
	return CandidatePlanRequest{
		ProjectID: request.ProjectID, SessionID: request.SessionID,
		CandidateID: request.CandidateID, CheckpointID: request.CheckpointID,
		ExpectedSessionVersion:   request.ExpectedSessionVersion,
		ExpectedSessionEpoch:     request.ExpectedSessionEpoch,
		ExpectedCandidateVersion: request.ExpectedCandidateVersion,
		ExpectedWriterLeaseEpoch: request.ExpectedWriterLeaseEpoch,
		Profile:                  request.VerificationProfile, ActorID: request.ActorID,
	}
}

func verificationOperationIDs(projectID, actorID, operationID string) (string, string) {
	identity := projectID + "\x00" + actorID + "\x00" + operationID
	planID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("candidate-verification-plan\x00"+identity))
	runID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("candidate-verification-run\x00"+identity))
	return planID.String(), runID.String()
}

var _ ControlStore = (*PostgresStore)(nil)
