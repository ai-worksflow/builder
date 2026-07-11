package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/conversation"
	"github.com/worksflow/builder/backend/internal/core"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
)

type fakeConversationAPI struct {
	ConversationAPI
	actorID       string
	createCalls   int
	decisionCalls int
	generateCalls int
	generateInput conversation.GenerateIntentProposalInput
	generateErr   error
	executeInput  conversation.ExecuteCommandInput
}

type fakeSummaryCheckpointAPI struct {
	ConversationAPI
	checkpoint conversation.ConversationSummaryCheckpoint
	page       conversation.SummaryCheckpointPage
	sourcePage conversation.MessagePage

	createErr   error
	getErr      error
	listErr     error
	sourceErr   error
	decisionErr error

	createCalls int
	createArgs  struct {
		projectID, conversationID, actorID, etag string
		input                                    conversation.CreateSummaryCheckpointInput
	}
	getArgs struct {
		projectID, conversationID, checkpointID, actorID string
	}
	listArgs struct {
		projectID, conversationID, actorID string
		options                            conversation.ListOptions
	}
	sourceArgs struct {
		projectID, conversationID, checkpointID, actorID string
		options                                          conversation.ListOptions
	}
	decisionCalls int
	decisionArgs  struct {
		projectID, conversationID, checkpointID, actorID, etag string
		input                                                  conversation.DecideSummaryCheckpointInput
	}
}

func (f *fakeSummaryCheckpointAPI) CreateSummaryCheckpoint(_ context.Context, projectID, conversationID, actorID, etag string, input conversation.CreateSummaryCheckpointInput) (conversation.ConversationSummaryCheckpoint, error) {
	f.createCalls++
	f.createArgs.projectID = projectID
	f.createArgs.conversationID = conversationID
	f.createArgs.actorID = actorID
	f.createArgs.etag = etag
	f.createArgs.input = input
	return f.checkpoint, f.createErr
}

func (f *fakeSummaryCheckpointAPI) GetSummaryCheckpoint(_ context.Context, projectID, conversationID, checkpointID, actorID string) (conversation.ConversationSummaryCheckpoint, error) {
	f.getArgs.projectID = projectID
	f.getArgs.conversationID = conversationID
	f.getArgs.checkpointID = checkpointID
	f.getArgs.actorID = actorID
	return f.checkpoint, f.getErr
}

func (f *fakeSummaryCheckpointAPI) ListSummaryCheckpoints(_ context.Context, projectID, conversationID, actorID string, options conversation.ListOptions) (conversation.SummaryCheckpointPage, error) {
	f.listArgs.projectID = projectID
	f.listArgs.conversationID = conversationID
	f.listArgs.actorID = actorID
	f.listArgs.options = options
	return f.page, f.listErr
}

func (f *fakeSummaryCheckpointAPI) ListSummaryCheckpointSourceMessages(_ context.Context, projectID, conversationID, checkpointID, actorID string, options conversation.ListOptions) (conversation.MessagePage, error) {
	f.sourceArgs.projectID = projectID
	f.sourceArgs.conversationID = conversationID
	f.sourceArgs.checkpointID = checkpointID
	f.sourceArgs.actorID = actorID
	f.sourceArgs.options = options
	return f.sourcePage, f.sourceErr
}

func (f *fakeSummaryCheckpointAPI) DecideSummaryCheckpoint(_ context.Context, projectID, conversationID, checkpointID, actorID, etag string, input conversation.DecideSummaryCheckpointInput) (conversation.ConversationSummaryCheckpoint, error) {
	f.decisionCalls++
	f.decisionArgs.projectID = projectID
	f.decisionArgs.conversationID = conversationID
	f.decisionArgs.checkpointID = checkpointID
	f.decisionArgs.actorID = actorID
	f.decisionArgs.etag = etag
	f.decisionArgs.input = input
	return f.checkpoint, f.decisionErr
}

func (f *fakeConversationAPI) Create(_ context.Context, projectID, actorID string, input conversation.CreateConversationInput) (conversation.Conversation, error) {
	f.actorID = actorID
	f.createCalls++
	id := uuid.NewString()
	return conversation.Conversation{
		ID: id, ProjectID: projectID, Title: input.Title, Status: conversation.ConversationActive,
		Version: 1, ETag: conversation.ConversationETag(id, 1), CreatedBy: actorID,
	}, nil
}

func (f *fakeConversationAPI) GenerateIntentProposal(_ context.Context, _, _, _ string, input conversation.GenerateIntentProposalInput) (conversation.GeneratedIntentProposal, error) {
	f.generateCalls++
	f.generateInput = input
	return conversation.GeneratedIntentProposal{}, f.generateErr
}

type memoryIdempotencyRecord struct {
	hash     string
	response *worksmiddleware.StoredResponse
}

type memoryIdempotencyStore struct {
	mu      sync.Mutex
	records map[string]memoryIdempotencyRecord
}

func (s *memoryIdempotencyStore) Claim(_ context.Context, scope, key, hash string) (worksmiddleware.ClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = map[string]memoryIdempotencyRecord{}
	}
	recordKey := scope + "\x00" + key
	record, exists := s.records[recordKey]
	if !exists {
		s.records[recordKey] = memoryIdempotencyRecord{hash: hash}
		return worksmiddleware.ClaimResult{Acquired: true}, nil
	}
	if record.hash != hash {
		return worksmiddleware.ClaimResult{}, worksmiddleware.ErrIdempotencyConflict
	}
	if record.response == nil {
		return worksmiddleware.ClaimResult{}, worksmiddleware.ErrIdempotencyInProgress
	}
	replay := *record.response
	replay.Body = append([]byte(nil), replay.Body...)
	replay.Headers = replay.Headers.Clone()
	return worksmiddleware.ClaimResult{Replay: &replay}, nil
}

func (s *memoryIdempotencyStore) Complete(_ context.Context, scope, key, hash string, response worksmiddleware.StoredResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	recordKey := scope + "\x00" + key
	record := s.records[recordKey]
	if record.hash != hash {
		return worksmiddleware.ErrIdempotencyConflict
	}
	response.Body = append([]byte(nil), response.Body...)
	response.Headers = response.Headers.Clone()
	record.response = &response
	s.records[recordKey] = record
	return nil
}

func (s *memoryIdempotencyStore) Release(_ context.Context, scope, key, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	recordKey := scope + "\x00" + key
	if record := s.records[recordKey]; record.hash == hash {
		delete(s.records, recordKey)
	}
	return nil
}

func (s *memoryIdempotencyStore) Seal(context.Context, string, string, string) error { return nil }
func (s *memoryIdempotencyStore) MaxRequestBytes() int64                             { return 1 << 20 }
func (s *memoryIdempotencyStore) MaxResponseBytes() int                              { return 1 << 20 }

func (f *fakeConversationAPI) DecideProposal(_ context.Context, projectID, conversationID, proposalID, actorID, _ string, _ conversation.DecideProposalInput) (conversation.WorkflowIntentProposal, *conversation.ConversationCommand, error) {
	f.decisionCalls++
	proposal := conversation.WorkflowIntentProposal{
		ID: proposalID, ProjectID: projectID, ConversationID: conversationID,
		Status: conversation.ProposalAccepted, Version: 2, ETag: conversation.ProposalETag(proposalID, 2),
	}
	commandID := uuid.NewString()
	command := conversation.ConversationCommand{
		ID: commandID, ProjectID: projectID, ConversationID: conversationID, ProposalID: proposalID,
		Status: conversation.CommandPending, Version: 1, ETag: conversation.CommandETag(commandID, 1), AcceptedBy: actorID,
	}
	return proposal, &command, nil
}

func (f *fakeConversationAPI) ExecuteCommand(_ context.Context, projectID, conversationID, commandID, actorID, _ string, input conversation.ExecuteCommandInput) (conversation.ConversationCommand, error) {
	f.executeInput = input
	return conversation.ConversationCommand{
		ID: commandID, ProjectID: projectID, ConversationID: conversationID,
		Status: conversation.CommandExecuted, Version: 3, ETag: conversation.CommandETag(commandID, 3), ExecutedBy: &actorID,
	}, nil
}

func TestConversationSummaryCheckpointRoutesAreRegistered(t *testing.T) {
	router := conversationRouterForTest(t, &fakeSummaryCheckpointAPI{}, uuid.NewString())
	want := map[string]bool{
		http.MethodGet + " /v1/projects/:projectId/conversations/:conversationId/summary-checkpoints":                               false,
		http.MethodPost + " /v1/projects/:projectId/conversations/:conversationId/summary-checkpoints":                              false,
		http.MethodGet + " /v1/projects/:projectId/conversations/:conversationId/summary-checkpoints/:checkpointId":                 false,
		http.MethodGet + " /v1/projects/:projectId/conversations/:conversationId/summary-checkpoints/:checkpointId/source-messages": false,
		http.MethodPost + " /v1/projects/:projectId/conversations/:conversationId/summary-checkpoints/:checkpointId/decision":       false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for route, registered := range want {
		if !registered {
			t.Errorf("summary checkpoint route is not registered: %s", route)
		}
	}
}

func TestConversationCreateSummaryCheckpointRequiresIfMatchAndReturnsResourceHeaders(t *testing.T) {
	projectID, conversationID, checkpointID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	throughMessageID, userID := uuid.NewString(), uuid.NewString()
	checkpoint := conversation.ConversationSummaryCheckpoint{
		ID: checkpointID, ProjectID: projectID, ConversationID: conversationID,
		ThroughMessageID: throughMessageID, ThroughSequence: 12, Summary: "Reviewed prefix",
		Status: conversation.SummaryCheckpointPendingReview, Version: 1,
		ETag: conversation.SummaryCheckpointETag(checkpointID, 1), CreatedBy: userID,
	}
	api := &fakeSummaryCheckpointAPI{checkpoint: checkpoint}
	router := conversationRouterForTest(t, api, userID)
	path := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/summary-checkpoints"
	body := `{"throughMessageId":"` + throughMessageID + `","summary":"Reviewed prefix"}`

	missing := conversationRequest(router, http.MethodPost, path, body, "", "checkpoint-create-missing-etag")
	if missing.Code != http.StatusPreconditionRequired || api.createCalls != 0 {
		t.Fatalf("missing If-Match status=%d calls=%d body=%s", missing.Code, api.createCalls, missing.Body.String())
	}

	conversationETag := conversation.ConversationETag(conversationID, 4)
	created := conversationRequest(router, http.MethodPost, path, body, conversationETag, "checkpoint-create-1")
	wantLocation := path + "/" + checkpointID
	if created.Code != http.StatusCreated || created.Header().Get("Location") != wantLocation ||
		created.Header().Get("ETag") != checkpoint.ETag || created.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create status=%d headers=%v body=%s", created.Code, created.Header(), created.Body.String())
	}
	if api.createCalls != 1 || api.createArgs.projectID != projectID || api.createArgs.conversationID != conversationID ||
		api.createArgs.actorID != userID || api.createArgs.etag != conversationETag ||
		api.createArgs.input.ThroughMessageID != throughMessageID || api.createArgs.input.Summary != "Reviewed prefix" {
		t.Fatalf("create args calls=%d args=%+v", api.createCalls, api.createArgs)
	}
	var response conversation.ConversationSummaryCheckpoint
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil || response.ID != checkpointID || response.ETag != checkpoint.ETag {
		t.Fatalf("create response=%+v err=%v body=%s", response, err, created.Body.String())
	}
}

func TestConversationGetsAndPaginatesSummaryCheckpointResources(t *testing.T) {
	projectID, conversationID, checkpointID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	throughMessageID, sourceMessageID, userID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	checkpoint := conversation.ConversationSummaryCheckpoint{
		ID: checkpointID, ProjectID: projectID, ConversationID: conversationID,
		ThroughMessageID: throughMessageID, ThroughSequence: 9, Summary: "Immutable prefix",
		Status: conversation.SummaryCheckpointApproved, Version: 2,
		ETag: conversation.SummaryCheckpointETag(checkpointID, 2), CreatedBy: uuid.NewString(),
	}
	source := conversation.Message{
		ID: sourceMessageID, ConversationID: conversationID, Sequence: 9,
		Role: conversation.MessageUser, Content: "source", CreatedBy: userID,
	}
	api := &fakeSummaryCheckpointAPI{
		checkpoint: checkpoint,
		page:       conversation.SummaryCheckpointPage{Items: []conversation.ConversationSummaryCheckpoint{checkpoint}, NextCursor: "checkpoint-next"},
		sourcePage: conversation.MessagePage{Items: []conversation.Message{source}, NextCursor: "source-next"},
	}
	router := conversationRouterForTest(t, api, userID)
	basePath := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/summary-checkpoints"

	got := conversationRequest(router, http.MethodGet, basePath+"/"+checkpointID, "", "", "")
	if got.Code != http.StatusOK || got.Header().Get("ETag") != checkpoint.ETag || got.Header().Get("Cache-Control") != "no-store" ||
		api.getArgs.projectID != projectID || api.getArgs.conversationID != conversationID || api.getArgs.checkpointID != checkpointID || api.getArgs.actorID != userID {
		t.Fatalf("get status=%d headers=%v args=%+v body=%s", got.Code, got.Header(), api.getArgs, got.Body.String())
	}

	listed := conversationRequest(router, http.MethodGet, basePath+"?limit=7&cursor=checkpoint-cursor", "", "", "")
	var checkpointPage conversation.SummaryCheckpointPage
	if err := json.Unmarshal(listed.Body.Bytes(), &checkpointPage); listed.Code != http.StatusOK || err != nil ||
		checkpointPage.NextCursor != "checkpoint-next" || len(checkpointPage.Items) != 1 ||
		api.listArgs.projectID != projectID || api.listArgs.conversationID != conversationID || api.listArgs.actorID != userID ||
		api.listArgs.options.Limit != 7 || api.listArgs.options.Cursor != "checkpoint-cursor" {
		t.Fatalf("list status=%d args=%+v page=%+v err=%v body=%s", listed.Code, api.listArgs, checkpointPage, err, listed.Body.String())
	}

	sources := conversationRequest(router, http.MethodGet, basePath+"/"+checkpointID+"/source-messages?limit=3&cursor=source-cursor", "", "", "")
	var sourcePage conversation.MessagePage
	if err := json.Unmarshal(sources.Body.Bytes(), &sourcePage); sources.Code != http.StatusOK || err != nil ||
		sourcePage.NextCursor != "source-next" || len(sourcePage.Items) != 1 || sourcePage.Items[0].ID != sourceMessageID ||
		api.sourceArgs.projectID != projectID || api.sourceArgs.conversationID != conversationID ||
		api.sourceArgs.checkpointID != checkpointID || api.sourceArgs.actorID != userID ||
		api.sourceArgs.options.Limit != 3 || api.sourceArgs.options.Cursor != "source-cursor" {
		t.Fatalf("sources status=%d args=%+v page=%+v err=%v body=%s", sources.Code, api.sourceArgs, sourcePage, err, sources.Body.String())
	}
}

func TestConversationSummaryCheckpointDecisionRequiresIfMatchAndReturnsETag(t *testing.T) {
	projectID, conversationID, checkpointID, userID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	approved := conversation.ConversationSummaryCheckpoint{
		ID: checkpointID, ProjectID: projectID, ConversationID: conversationID,
		Status: conversation.SummaryCheckpointApproved, Version: 2,
		ETag: conversation.SummaryCheckpointETag(checkpointID, 2),
	}
	api := &fakeSummaryCheckpointAPI{checkpoint: approved}
	router := conversationRouterForTest(t, api, userID)
	path := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/summary-checkpoints/" + checkpointID + "/decision"
	body := `{"decision":"approve","reason":"prefix verified"}`

	missing := conversationRequest(router, http.MethodPost, path, body, "", "checkpoint-decision-missing-etag")
	if missing.Code != http.StatusPreconditionRequired || api.decisionCalls != 0 {
		t.Fatalf("missing If-Match status=%d calls=%d body=%s", missing.Code, api.decisionCalls, missing.Body.String())
	}

	pendingETag := conversation.SummaryCheckpointETag(checkpointID, 1)
	decided := conversationRequest(router, http.MethodPost, path, body, pendingETag, "checkpoint-decision-1")
	if decided.Code != http.StatusOK || decided.Header().Get("ETag") != approved.ETag || decided.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("decision status=%d headers=%v body=%s", decided.Code, decided.Header(), decided.Body.String())
	}
	if api.decisionCalls != 1 || api.decisionArgs.projectID != projectID || api.decisionArgs.conversationID != conversationID ||
		api.decisionArgs.checkpointID != checkpointID || api.decisionArgs.actorID != userID || api.decisionArgs.etag != pendingETag ||
		api.decisionArgs.input.Decision != conversation.SummaryCheckpointApprove || api.decisionArgs.input.Reason != "prefix verified" {
		t.Fatalf("decision args calls=%d args=%+v", api.decisionCalls, api.decisionArgs)
	}
}

func TestConversationSummaryCheckpointDecisionMapsReviewConflicts(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
		key  string
	}{
		{name: "self approval", err: core.ErrSelfApproval, code: "self_approval", key: "checkpoint-conflict-self-approval"},
		{name: "stale chain", err: conversation.ErrSummaryCheckpointChainStale, code: "conversation_summary_checkpoint_chain_stale", key: "checkpoint-conflict-stale-chain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectID, conversationID, checkpointID, userID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
			api := &fakeSummaryCheckpointAPI{decisionErr: tt.err}
			router := conversationRouterForTest(t, api, userID)
			path := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/summary-checkpoints/" + checkpointID + "/decision"
			response := conversationRequest(router, http.MethodPost, path, `{"decision":"approve"}`, conversation.SummaryCheckpointETag(checkpointID, 1), tt.key)
			var details struct {
				Code string `json:"code"`
			}
			err := json.Unmarshal(response.Body.Bytes(), &details)
			if response.Code != http.StatusConflict || err != nil || details.Code != tt.code || api.decisionCalls != 1 {
				t.Fatalf("status=%d code=%q calls=%d err=%v body=%s", response.Code, details.Code, api.decisionCalls, err, response.Body.String())
			}
		})
	}
}

func TestConversationWorkbenchExecutionRejectsBrowserSuppliedResult(t *testing.T) {
	projectID, conversationID, commandID, userID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	runID, bundleID, implementationProposalID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	api := &fakeConversationAPI{}
	router := conversationRouterForTest(t, api, userID)
	path := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/commands/" + commandID + "/execute"
	body := `{"workbenchResult":{"runId":"` + runID + `","bundleId":"` + bundleID + `","implementationProposalId":"` + implementationProposalID + `"}}`
	response := conversationRequest(router, http.MethodPost, path, body, conversation.CommandETag(commandID, 1), "workbench-result-1")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("browser-supplied result status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConversationWorkbenchExecutionAcceptsOnlyEmptyCommandBody(t *testing.T) {
	projectID, conversationID, commandID, userID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	api := &fakeConversationAPI{}
	router := conversationRouterForTest(t, api, userID)
	path := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/commands/" + commandID + "/execute"
	response := conversationRequest(router, http.MethodPost, path, `{}`, conversation.CommandETag(commandID, 1), "workbench-server-execute-1")
	if response.Code != http.StatusOK {
		t.Fatalf("empty execute status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConversationGenerateIntentReturnsInvalidInputForIncompatibleStartCandidate(t *testing.T) {
	projectID, conversationID, userID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	triggerID := uuid.NewString()
	api := &fakeConversationAPI{generateErr: core.ErrInvalidInput}
	router := conversationRouterForTest(t, api, userID)
	path := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/intent-proposals/generate"
	body := `{"triggerMessageId":"` + triggerID + `","desiredOutputCapability":"application","sourceRefs":[],"manifestIntent":{}}`
	response := conversationRequest(router, http.MethodPost, path, body, "", "generate-incompatible-1")
	if response.Code != http.StatusUnprocessableEntity || api.generateCalls != 1 {
		t.Fatalf("generate status=%d calls=%d body=%s", response.Code, api.generateCalls, response.Body.String())
	}
	if api.generateInput.DesiredOutputCapability != "application" {
		t.Fatalf("transport lost desired output capability: %+v", api.generateInput)
	}
}

func TestConversationGenerateIntentAcceptsExactWorkbenchTargetHint(t *testing.T) {
	projectID, conversationID, userID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	triggerID, runID, rootBundleID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	api := &fakeConversationAPI{generateErr: core.ErrInvalidInput}
	router := conversationRouterForTest(t, api, userID)
	path := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/intent-proposals/generate"
	body := `{"triggerMessageId":"` + triggerID + `","desiredOutputCapability":"application","sourceRefs":[],"manifestIntent":{},"workbenchTargetHint":{"runId":"` + runID + `","rootBundleId":"` + rootBundleID + `"}}`
	response := conversationRequest(router, http.MethodPost, path, body, "", "generate-target-hint-1")
	if response.Code != http.StatusUnprocessableEntity || api.generateCalls != 1 || api.generateInput.WorkbenchTargetHint == nil ||
		api.generateInput.WorkbenchTargetHint.RunID != runID || api.generateInput.WorkbenchTargetHint.RootBundleID != rootBundleID {
		t.Fatalf("target hint status=%d calls=%d input=%+v body=%s", response.Code, api.generateCalls, api.generateInput, response.Body.String())
	}
}

func TestConversationGenerateIntentWritesSummaryCheckpointRFCConflict(t *testing.T) {
	projectID, conversationID, userID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	triggerID := uuid.NewString()
	api := &fakeConversationAPI{generateErr: conversation.ErrIntentSummaryCheckpointRequired}
	router := conversationRouterForTest(t, api, userID)
	path := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/intent-proposals/generate"
	body := `{"triggerMessageId":"` + triggerID + `","desiredOutputCapability":"application","sourceRefs":[],"manifestIntent":{}}`
	response := conversationRequest(router, http.MethodPost, path, body, "", "generate-summary-conflict-1")
	if response.Code != http.StatusConflict ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"code":"conversation_summary_checkpoint_required"`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte("no messages were silently omitted")) {
		t.Fatalf("summary checkpoint status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConversationGenerateIntentWritesTypedSummaryCheckpointExtensions(t *testing.T) {
	projectID, conversationID, userID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	triggerID, currentCheckpointID, recommendedThroughID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	api := &fakeConversationAPI{generateErr: &conversation.IntentSummaryCheckpointRequiredError{
		TriggerMessageID:            triggerID,
		TriggerSequence:             240,
		MessageCount:                210,
		MessageContentBytes:         131000,
		ContextBytes:                135168,
		CurrentApprovedCheckpointID: currentCheckpointID,
		CurrentThroughSequence:      80,
		RecommendedThroughMessageID: recommendedThroughID,
		RecommendedThroughSequence:  160,
	}}
	router := conversationRouterForTest(t, api, userID)
	path := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/intent-proposals/generate"
	body := `{"triggerMessageId":"` + triggerID + `","desiredOutputCapability":"application","sourceRefs":[],"manifestIntent":{}}`
	response := conversationRequest(router, http.MethodPost, path, body, "", "generate-summary-typed-1")
	var details struct {
		Code       string `json:"code"`
		Extensions struct {
			TriggerMessageID            string `json:"triggerMessageId"`
			ContextBytes                uint64 `json:"contextBytes"`
			CurrentApprovedCheckpointID string `json:"currentApprovedCheckpointId"`
			RecommendedThroughMessageID string `json:"recommendedThroughMessageId"`
			RecommendedThroughSequence  uint64 `json:"recommendedThroughSequence"`
			CreateHref                  string `json:"createHref"`
		} `json:"extensions"`
	}
	err := json.Unmarshal(response.Body.Bytes(), &details)
	wantCreateHref := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/summary-checkpoints"
	if response.Code != http.StatusConflict || err != nil || details.Code != "conversation_summary_checkpoint_required" ||
		details.Extensions.TriggerMessageID != triggerID || details.Extensions.ContextBytes != 135168 ||
		details.Extensions.CurrentApprovedCheckpointID != currentCheckpointID ||
		details.Extensions.RecommendedThroughMessageID != recommendedThroughID || details.Extensions.RecommendedThroughSequence != 160 ||
		details.Extensions.CreateHref != wantCreateHref {
		t.Fatalf("typed summary checkpoint status=%d details=%+v err=%v body=%s", response.Code, details, err, response.Body.String())
	}
}

func TestConversationGenerateIntentRejectsClientSuppliedWorkflowCandidates(t *testing.T) {
	projectID, conversationID, userID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	triggerID, candidateID := uuid.NewString(), uuid.NewString()
	api := &fakeConversationAPI{}
	router := conversationRouterForTest(t, api, userID)
	path := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/intent-proposals/generate"
	body := `{"triggerMessageId":"` + triggerID + `","desiredOutputCapability":"application","candidateDefinitionVersionIds":["` + candidateID + `"],"sourceRefs":[],"manifestIntent":{}}`
	response := conversationRequest(router, http.MethodPost, path, body, "", "generate-forged-candidate-1")
	if response.Code != http.StatusBadRequest || api.generateCalls != 0 {
		t.Fatalf("forged candidates status=%d calls=%d body=%s", response.Code, api.generateCalls, response.Body.String())
	}
}

func conversationRouterForTest(t *testing.T, api ConversationAPI, userID string) *gin.Engine {
	t.Helper()
	router := gin.New()
	security := config.SecurityConfig{Session: config.SessionSecurityConfig{CookieName: "session"}}
	group := router.Group("/v1", worksmiddleware.RequireAuthentication(workflowAuthenticator{userID: userID}, security))
	handler, err := NewConversationHandler(ConversationDependencies{Service: api, MaxJSONBodyBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterConversationRoutes(group, handler, worksmiddleware.CaptureIdempotencyKey(true)); err != nil {
		t.Fatal(err)
	}
	return router
}

func conversationRequest(router http.Handler, method, path, body, etag, idempotencyKey string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer test-token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if etag != "" {
		request.Header.Set("If-Match", etag)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestConversationMutationsRequireIdempotencyAndUseSessionActor(t *testing.T) {
	projectID, userID := uuid.NewString(), uuid.NewString()
	api := &fakeConversationAPI{}
	router := conversationRouterForTest(t, api, userID)
	path := "/v1/projects/" + projectID + "/conversations"
	missing := conversationRequest(router, http.MethodPost, path, `{"title":"Launch"}`, "", "")
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency status=%d body=%s", missing.Code, missing.Body.String())
	}
	created := conversationRequest(router, http.MethodPost, path, `{"title":"Launch"}`, "", "conversation-create-1")
	if created.Code != http.StatusCreated || created.Header().Get("ETag") == "" || created.Header().Get("Cache-Control") != "no-store" || api.actorID != userID {
		t.Fatalf("created status=%d actor=%q headers=%v body=%s", created.Code, api.actorID, created.Header(), created.Body.String())
	}
}

func TestConversationCreateIdempotencyReplaysWithoutSecondMutation(t *testing.T) {
	projectID, userID := uuid.NewString(), uuid.NewString()
	api := &fakeConversationAPI{}
	store := &memoryIdempotencyStore{}
	router := gin.New()
	security := config.SecurityConfig{Session: config.SessionSecurityConfig{CookieName: "session"}}
	group := router.Group("/v1", worksmiddleware.RequireAuthentication(workflowAuthenticator{userID: userID}, security))
	handler, err := NewConversationHandler(ConversationDependencies{Service: api, MaxJSONBodyBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterConversationRoutes(group, handler, worksmiddleware.CaptureIdempotencyKey(true), worksmiddleware.PersistIdempotency(store)); err != nil {
		t.Fatal(err)
	}
	path := "/v1/projects/" + projectID + "/conversations"
	first := conversationRequest(router, http.MethodPost, path, `{"title":"Idempotent launch"}`, "", "conversation-replay-1")
	second := conversationRequest(router, http.MethodPost, path, `{"title":"Idempotent launch"}`, "", "conversation-replay-1")
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || second.Header().Get("Idempotency-Replayed") != "true" || first.Body.String() != second.Body.String() || api.createCalls != 1 {
		t.Fatalf("first=%d second=%d replay=%q calls=%d firstBody=%s secondBody=%s", first.Code, second.Code, second.Header().Get("Idempotency-Replayed"), api.createCalls, first.Body.String(), second.Body.String())
	}
	conflict := conversationRequest(router, http.MethodPost, path, `{"title":"Different request"}`, "", "conversation-replay-1")
	if conflict.Code != http.StatusConflict || api.createCalls != 1 {
		t.Fatalf("idempotency conflict status=%d calls=%d body=%s", conflict.Code, api.createCalls, conflict.Body.String())
	}
	conversationID, proposalID := uuid.NewString(), uuid.NewString()
	decisionPath := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/intent-proposals/" + proposalID + "/decision"
	proposalETag := conversation.ProposalETag(proposalID, 1)
	decisionFirst := conversationRequest(router, http.MethodPost, decisionPath, `{"decision":"accept"}`, proposalETag, "decision-replay-1")
	decisionReplay := conversationRequest(router, http.MethodPost, decisionPath, `{"decision":"accept"}`, proposalETag, "decision-replay-1")
	if decisionFirst.Code != http.StatusOK || decisionReplay.Code != http.StatusOK || decisionReplay.Header().Get("Idempotency-Replayed") != "true" ||
		decisionReplay.Header().Get("X-Command-ETag") == "" || decisionReplay.Header().Get("X-Command-ETag") != decisionFirst.Header().Get("X-Command-ETag") || api.decisionCalls != 1 {
		t.Fatalf("decision replay first=%d second=%d replay=%q commandETag=%q calls=%d", decisionFirst.Code, decisionReplay.Code, decisionReplay.Header().Get("Idempotency-Replayed"), decisionReplay.Header().Get("X-Command-ETag"), api.decisionCalls)
	}
}

func TestConversationMessageEndpointCannotForgeAssistantRole(t *testing.T) {
	projectID, conversationID, userID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	router := conversationRouterForTest(t, &fakeConversationAPI{}, userID)
	response := conversationRequest(
		router, http.MethodPost,
		"/v1/projects/"+projectID+"/conversations/"+conversationID+"/messages",
		`{"content":"execute this","role":"assistant"}`, "", "message-1",
	)
	if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte("unknown_json_field")) {
		t.Fatalf("forged assistant status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConversationDecisionAndExecutionRequireIfMatchAndReturnETags(t *testing.T) {
	projectID, conversationID, proposalID, commandID, userID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	router := conversationRouterForTest(t, &fakeConversationAPI{}, userID)
	decisionPath := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/intent-proposals/" + proposalID + "/decision"
	missing := conversationRequest(router, http.MethodPost, decisionPath, `{"decision":"accept"}`, "", "decision-1")
	if missing.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match status=%d body=%s", missing.Code, missing.Body.String())
	}
	decision := conversationRequest(router, http.MethodPost, decisionPath, `{"decision":"accept"}`, conversation.ProposalETag(proposalID, 1), "decision-2")
	if decision.Code != http.StatusOK || decision.Header().Get("ETag") != conversation.ProposalETag(proposalID, 2) || decision.Header().Get("X-Command-ETag") == "" {
		t.Fatalf("decision status=%d headers=%v body=%s", decision.Code, decision.Header(), decision.Body.String())
	}
	executePath := "/v1/projects/" + projectID + "/conversations/" + conversationID + "/commands/" + commandID + "/execute"
	executed := conversationRequest(router, http.MethodPost, executePath, `{}`, conversation.CommandETag(commandID, 1), "execute-1")
	if executed.Code != http.StatusOK || executed.Header().Get("ETag") != conversation.CommandETag(commandID, 3) {
		t.Fatalf("execute status=%d headers=%v body=%s", executed.Code, executed.Header(), executed.Body.String())
	}
}
