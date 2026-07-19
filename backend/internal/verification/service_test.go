package verification

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestControlServiceCreatesAndRecoversExactCandidateRunBeforeReloadingSource(t *testing.T) {
	compileInput := validCandidatePlanInput()
	request := candidateServiceRequest(compileInput)
	store := newControlStoreFake()
	source := &candidatePlanSourceFake{input: compileInput}
	access := &verificationAuthorizerFake{}
	service, err := NewControlService(store, source, access)
	if err != nil {
		t.Fatal(err)
	}

	created, err := service.CreateCandidateRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	planID, runID := verificationOperationIDs(request.ProjectID, request.ActorID, request.OperationID)
	if created.Run.ID != runID || created.Run.Plan.ID != planID || created.Run.State != RunQueued ||
		created.Run.Replayed || len(created.AllowedActions) != 1 || created.AllowedActions[0] != RunActionCancel ||
		source.calls != 1 || access.editCalls != 1 {
		t.Fatalf("created Candidate VerificationRun = %#v, source=%d edit=%d", created, source.calls, access.editCalls)
	}
	history, err := service.ListCandidateRunsForSession(
		context.Background(), request.ProjectID, request.SessionID, request.ActorID, 20,
	)
	if err != nil || len(history) != 1 || history[0].Run.ID != created.Run.ID {
		t.Fatalf("Candidate VerificationRun history = %#v, %v", history, err)
	}

	store.fresh = false
	replayed, err := service.CreateCandidateRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Run.ID != created.Run.ID || !replayed.Run.Replayed || !replayed.Stale || source.calls != 1 {
		t.Fatalf("recovered Candidate VerificationRun = %#v, source calls=%d", replayed, source.calls)
	}
	conflict := request
	conflict.Reason = "same key with different semantic request"
	if _, err := service.CreateCandidateRun(context.Background(), conflict); !errors.Is(err, ErrRunIdempotencyConflict) {
		t.Fatalf("conflicting service replay = %v, want ErrRunIdempotencyConflict", err)
	}
	if source.calls != 1 {
		t.Fatalf("conflicting replay reloaded mutable planning source %d times", source.calls)
	}
}

func TestControlServiceRetriesOnlyExplicitRetriableTerminalRun(t *testing.T) {
	compileInput := validCandidatePlanInput()
	request := candidateServiceRequest(compileInput)
	store := newControlStoreFake()
	service, err := NewControlService(store, &candidatePlanSourceFake{input: compileInput}, &verificationAuthorizerFake{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateCandidateRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	parent := store.runs[created.Run.ID]
	parent.State, parent.Version, parent.TerminalReason = RunCancelled, 2, "cancelled by user"
	store.runs[parent.ID] = parent

	retryRequest := RetryCandidateRunRequest{
		ProjectID: request.ProjectID, ParentRunID: parent.ID,
		Reason:  "rerun after fixing the deterministic fixture",
		ActorID: request.ActorID, OperationID: "candidate-verification-retry-1",
	}
	retried, err := service.RetryCandidateRun(context.Background(), retryRequest)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Run.ParentRunID != parent.ID || retried.Run.RetryReason != retryRequest.Reason ||
		retried.Run.Plan != parent.Plan || retried.Run.State != RunQueued {
		t.Fatalf("retried Candidate VerificationRun = %#v", retried)
	}
	replayed, err := service.RetryCandidateRun(context.Background(), retryRequest)
	if err != nil || !replayed.Run.Replayed || replayed.Run.ID != retried.Run.ID {
		t.Fatalf("retried Run replay = %#v, %v", replayed, err)
	}

	passed := parent
	passed.ID, passed.State = uuid.NewString(), RunPassed
	store.runs[passed.ID] = passed
	blocked := retryRequest
	blocked.ParentRunID, blocked.OperationID = passed.ID, "candidate-verification-retry-passed"
	if _, err := service.RetryCandidateRun(context.Background(), blocked); !errors.Is(err, ErrRunTransition) {
		t.Fatalf("passed Run retry = %v, want ErrRunTransition", err)
	}
}

func TestControlServicePagesReceiptChecksThroughAuthorizedRun(t *testing.T) {
	input := validCandidatePlanInput()
	request := candidateServiceRequest(input)
	store := newControlStoreFake()
	access := &verificationAuthorizerFake{}
	service, err := NewControlService(store, &candidatePlanSourceFake{input: input}, access)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateCandidateRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	run := store.runs[created.Run.ID]
	run.State = RunPassed
	store.runs[run.ID] = run
	receiptID := uuid.NewString()
	store.receipts[receiptID] = Receipt{
		ID: receiptID, RunID: run.ID, ProjectID: request.ProjectID,
		PayloadHash: hashFixture("paged-receipt"), Decision: DecisionPassed,
		Checks: []CheckResult{
			{ID: "first", Diagnostics: []Diagnostic{}},
			{ID: "second", Diagnostics: []Diagnostic{}},
		},
	}

	page, err := service.ListChecksForRunByID(
		context.Background(), run.ID, request.ActorID, 1, 1,
	)
	if err != nil || page.RunID != run.ID || page.TotalCount != 2 ||
		page.Offset != 1 || page.Limit != 1 || len(page.Checks) != 1 ||
		page.Checks[0].ID != "second" || page.Receipt.ID != receiptID {
		t.Fatalf("paged Receipt checks = %#v, %v", page, err)
	}
	if access.viewCalls != 1 {
		t.Fatalf("check page project view authorizations = %d, want 1", access.viewCalls)
	}
}

func TestControlServiceAuthorizesBeforeReadingPlanningFacts(t *testing.T) {
	compileInput := validCandidatePlanInput()
	request := candidateServiceRequest(compileInput)
	denied := errors.New("project edit denied")
	source := &candidatePlanSourceFake{input: compileInput}
	service, err := NewControlService(newControlStoreFake(), source, &verificationAuthorizerFake{editErr: denied})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateCandidateRun(context.Background(), request); !errors.Is(err, denied) {
		t.Fatalf("denied Candidate VerificationRun creation = %v", err)
	}
	if source.calls != 0 {
		t.Fatalf("unauthorized request read planning source %d times", source.calls)
	}
}

func TestControlServiceListsProfilesAndReadsReceiptAfterProjectAuthorization(t *testing.T) {
	store := newControlStoreFake()
	input := validCandidatePlanInput()
	profile := ProfileSummary{
		VerificationProfile: ProfileReference{
			ID: input.Profile.ID, Version: input.Profile.Version, ContentHash: input.Profile.ProfileHash,
		},
		SupportedTemplateRoles: []string{"api", "web"},
	}
	store.profiles = []ProfileSummary{profile}
	receiptID := uuid.NewString()
	receipt := Receipt{
		SchemaVersion: ReceiptSchemaVersion,
		ID:            receiptID, ProjectID: input.ProjectID, PayloadHash: hashFixture("receipt-service"),
	}
	store.receipts[receiptID] = receipt
	access := &verificationAuthorizerFake{}
	service, err := NewControlService(store, &candidatePlanSourceFake{input: input}, access)
	if err != nil {
		t.Fatal(err)
	}

	profiles, err := service.ListActiveProfiles(context.Background(), input.ProjectID, input.Subject.SessionID, uuid.NewString())
	if err != nil || len(profiles) != 1 || profiles[0].VerificationProfile != profile.VerificationProfile {
		t.Fatalf("active profiles = %#v, %v", profiles, err)
	}
	actorID := uuid.NewString()
	loaded, err := service.GetReceiptByID(context.Background(), receiptID, actorID)
	if err != nil || loaded.ID != receiptID || loaded.PayloadHash != receipt.PayloadHash {
		t.Fatalf("loaded Receipt = %#v, %v", loaded, err)
	}
	if access.viewCalls != 2 {
		t.Fatalf("project view authorization calls = %d, want 2", access.viewCalls)
	}
}

func candidateServiceRequest(input CompileCandidatePlanInput) CreateCandidateRunRequest {
	return CreateCandidateRunRequest{
		ProjectID: input.ProjectID, SessionID: input.Subject.SessionID,
		CandidateID: input.Subject.CandidateID, CheckpointID: input.Subject.CandidateSnapshotID,
		ExpectedSessionVersion:   input.Subject.SessionVersion,
		ExpectedSessionEpoch:     input.Subject.SessionEpoch,
		ExpectedCandidateVersion: input.Subject.CandidateVersion,
		ExpectedWriterLeaseEpoch: input.Subject.WriterLeaseEpoch,
		VerificationProfile: ProfileReference{
			ID: input.Profile.ID, Version: input.Profile.Version, ContentHash: input.Profile.ProfileHash,
		},
		Reason:  "verify exact Candidate before Proposal review",
		ActorID: uuid.NewString(), OperationID: "candidate-verification-create-1",
	}
}

type candidatePlanSourceFake struct {
	input CompileCandidatePlanInput
	err   error
	calls int
}

func (source *candidatePlanSourceFake) LoadCandidatePlan(
	_ context.Context,
	request CandidatePlanRequest,
) (CompileCandidatePlanInput, error) {
	source.calls++
	if source.err != nil {
		return CompileCandidatePlanInput{}, source.err
	}
	if request.ProjectID != source.input.ProjectID || request.SessionID != source.input.Subject.SessionID ||
		request.CandidateID != source.input.Subject.CandidateID ||
		request.CheckpointID != source.input.Subject.CandidateSnapshotID {
		return CompileCandidatePlanInput{}, ErrCandidatePlanningDrift
	}
	return source.input, nil
}

type verificationAuthorizerFake struct {
	viewErr   error
	editErr   error
	viewCalls int
	editCalls int
}

func (access *verificationAuthorizerFake) RequireProjectView(context.Context, string, string) error {
	access.viewCalls++
	return access.viewErr
}

func (access *verificationAuthorizerFake) RequireProjectEdit(context.Context, string, string) error {
	access.editCalls++
	return access.editErr
}

type controlStoreFake struct {
	profiles    []ProfileSummary
	receipts    map[string]Receipt
	plans       map[string]Plan
	runs        map[string]Run
	runRequests map[string]string
	fresh       bool
	now         time.Time
}

func newControlStoreFake() *controlStoreFake {
	return &controlStoreFake{
		plans: map[string]Plan{}, runs: map[string]Run{}, receipts: map[string]Receipt{}, runRequests: map[string]string{}, fresh: true,
		now: time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC),
	}
}

func (store *controlStoreFake) ListActiveProfiles(context.Context, string, string) ([]ProfileSummary, error) {
	return append([]ProfileSummary(nil), store.profiles...), nil
}

func (store *controlStoreFake) SavePlan(
	_ context.Context,
	planID, createdBy string,
	compiled CompiledPlan,
) (Plan, error) {
	if existing, ok := store.plans[planID]; ok {
		if existing.PlanHash != compiled.PlanHash {
			return Plan{}, ErrPlanConflict
		}
		existing.Replayed = true
		return existing, nil
	}
	plan := Plan{
		ID: planID, Content: compiled.Content, PlanHash: compiled.PlanHash,
		CreatedBy: createdBy, CreatedAt: store.now,
	}
	store.plans[planID] = plan
	return plan, nil
}

func (store *controlStoreFake) GetPlan(_ context.Context, projectID, planID string) (Plan, error) {
	plan, ok := store.plans[planID]
	if !ok || plan.Content.ProjectID != projectID {
		return Plan{}, ErrPlanNotFound
	}
	return plan, nil
}

func (store *controlStoreFake) CreateRun(_ context.Context, input CreateRunInput) (Run, error) {
	if id, ok := store.runRequests[input.ProjectID+"\x00"+input.RequestKey]; ok {
		existing := store.runs[id]
		if existing.RequestHash != input.RequestHash {
			return Run{}, ErrRunIdempotencyConflict
		}
		existing.Replayed = true
		return existing, nil
	}
	run := Run{
		SchemaVersion: RunSchemaVersion, ID: input.ID, ProjectID: input.ProjectID,
		Plan: input.Plan, RequestKey: input.RequestKey, RequestHash: input.RequestHash,
		Reason: input.Reason, ParentRunID: input.ParentRunID, RetryReason: input.RetryReason,
		State: RunQueued, Version: 1, CreatedBy: input.CreatedBy, UpdatedBy: input.CreatedBy,
		CreatedAt: store.now, UpdatedAt: store.now,
	}
	store.runs[run.ID] = run
	store.runRequests[input.ProjectID+"\x00"+input.RequestKey] = run.ID
	return run, nil
}

func (store *controlStoreFake) FindRunByRequest(
	_ context.Context,
	projectID, requestKey string,
) (Run, bool, error) {
	id, ok := store.runRequests[projectID+"\x00"+requestKey]
	if !ok {
		return Run{}, false, nil
	}
	return store.runs[id], true, nil
}

func (store *controlStoreFake) GetRun(_ context.Context, projectID, runID string) (Run, error) {
	run, ok := store.runs[runID]
	if !ok || run.ProjectID != projectID {
		return Run{}, ErrRunNotFound
	}
	return run, nil
}

func (store *controlStoreFake) GetRunView(_ context.Context, projectID, runID string) (RunView, error) {
	run, err := store.GetRun(context.Background(), projectID, runID)
	if err != nil {
		return RunView{}, err
	}
	plan, err := store.GetPlan(context.Background(), projectID, run.Plan.ID)
	if err != nil {
		return RunView{}, err
	}
	var receipt *Receipt
	for _, value := range store.receipts {
		if value.RunID == run.ID {
			copy := value
			receipt = &copy
			break
		}
	}
	return buildRunView(run, plan, nil, 0, receipt, store.fresh), nil
}

func (store *controlStoreFake) ListRunViewsForSession(
	_ context.Context,
	projectID, sessionID string,
	limit int,
) ([]RunView, error) {
	views := make([]RunView, 0, len(store.runs))
	for _, run := range store.runs {
		plan, ok := store.plans[run.Plan.ID]
		if !ok || run.ProjectID != projectID || plan.Content.Subject.SessionID != sessionID {
			continue
		}
		views = append(views, buildRunView(run, plan, nil, 0, nil, store.fresh))
	}
	sort.SliceStable(views, func(left, right int) bool {
		if views[left].Run.CreatedAt.Equal(views[right].Run.CreatedAt) {
			return views[left].Run.ID > views[right].Run.ID
		}
		return views[left].Run.CreatedAt.After(views[right].Run.CreatedAt)
	})
	if len(views) > limit {
		views = views[:limit]
	}
	return views, nil
}

func (store *controlStoreFake) ResolveRunProject(_ context.Context, runID string) (string, error) {
	run, ok := store.runs[runID]
	if !ok {
		return "", ErrRunNotFound
	}
	return run.ProjectID, nil
}

func (store *controlStoreFake) ResolveReceiptProject(_ context.Context, receiptID string) (string, error) {
	receipt, ok := store.receipts[receiptID]
	if !ok {
		return "", ErrReceiptNotFound
	}
	return receipt.ProjectID, nil
}

func (store *controlStoreFake) GetReceipt(_ context.Context, projectID, receiptID string) (Receipt, error) {
	receipt, ok := store.receipts[receiptID]
	if !ok || receipt.ProjectID != projectID {
		return Receipt{}, ErrReceiptNotFound
	}
	return receipt, nil
}

func (store *controlStoreFake) CancelRun(_ context.Context, input CancelRunInput) (RunView, error) {
	run, err := store.GetRun(context.Background(), input.ProjectID, input.RunID)
	if err != nil {
		return RunView{}, err
	}
	if run.Version != input.ExpectedVersion || run.FenceEpoch != input.ExpectedFenceEpoch {
		return RunView{}, ErrRunVersionConflict
	}
	run.State, run.Version, run.TerminalReason = RunCancelled, run.Version+1, input.Reason
	store.runs[run.ID] = run
	return store.GetRunView(context.Background(), input.ProjectID, input.RunID)
}

var _ ControlStore = (*controlStoreFake)(nil)
