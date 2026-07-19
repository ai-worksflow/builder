package transport

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/agent"
	"github.com/worksflow/builder/backend/internal/auth"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
)

const (
	agentTransportActor      = "31000000-0000-4000-8000-000000000001"
	agentTransportProject    = "31000000-0000-4000-8000-000000000002"
	agentTransportSession    = "31000000-0000-4000-8000-000000000003"
	agentTransportAttempt    = "31000000-0000-4000-8000-000000000004"
	agentTransportNewAttempt = "31000000-0000-4000-8000-000000000005"
	agentTransportUndo       = "31000000-0000-4000-8000-000000000006"
)

type agentSessionResolverFake struct {
	sessionID string
	actorID   string
	projectID string
	err       error
}

func (resolver *agentSessionResolverFake) ResolveProject(
	_ context.Context,
	sessionID, actorID string,
) (string, error) {
	resolver.sessionID, resolver.actorID = sessionID, actorID
	return resolver.projectID, resolver.err
}

type agentControlAPIFake struct {
	createInput agent.CreateTaskAttemptInput
	create      agent.TaskAttemptResult
	createErr   error

	retryInput agent.RetryAttemptInput
	retry      agent.TaskAttemptResult
	retryErr   error

	getAttemptID string
	getActorID   string
	get          agent.TaskAttemptResult
	getErr       error

	listProjectID string
	listSessionID string
	listActorID   string
	listLimit     int
	list          []agent.AgentAttempt
	listErr       error

	eventAttemptID string
	eventActorID   string
	eventAfter     uint64
	eventLimit     int
	events         []agent.AttemptEvent
	eventsErr      error

	evidenceAttemptID string
	evidenceActorID   string
	evidenceKind      agent.EvidenceKind
	evidence          agent.EvidenceReadResult
	evidenceErr       error

	patchFileAttemptID string
	patchFileActorID   string
	patchFilePath      string
	patchFileSide      agent.PatchFileSide
	patchFile          agent.PatchFileReviewResult
	patchFileErr       error

	mergeInput agent.MergePatchInput
	merge      agent.MergePatchResult
	mergeErr   error

	undoInput agent.UndoPatchInput
	undo      agent.UndoPatchResult
	undoErr   error

	historyAttemptID string
	historyActorID   string
	historyLimit     int
	history          []agent.PatchMergeHistoryItem
	historyErr       error

	cancelAttemptID string
	cancelActorID   string
	cancelVersion   uint64
	cancelReason    string
	cancel          agent.AgentAttempt
	cancelErr       error
}

func (api *agentControlAPIFake) CreateTaskAttempt(
	_ context.Context,
	input agent.CreateTaskAttemptInput,
) (agent.TaskAttemptResult, error) {
	api.createInput = input
	return api.create, api.createErr
}

func (api *agentControlAPIFake) RetryAttempt(
	_ context.Context,
	input agent.RetryAttemptInput,
) (agent.TaskAttemptResult, error) {
	api.retryInput = input
	return api.retry, api.retryErr
}

func (api *agentControlAPIFake) GetAttempt(
	_ context.Context,
	attemptID, actorID string,
) (agent.TaskAttemptResult, error) {
	api.getAttemptID, api.getActorID = attemptID, actorID
	return api.get, api.getErr
}

func (api *agentControlAPIFake) ListAttempts(
	_ context.Context,
	projectID, sessionID, actorID string,
	limit int,
) ([]agent.AgentAttempt, error) {
	api.listProjectID, api.listSessionID, api.listActorID, api.listLimit = projectID, sessionID, actorID, limit
	return api.list, api.listErr
}

func (api *agentControlAPIFake) ListEvents(
	_ context.Context,
	attemptID, actorID string,
	afterSequence uint64,
	limit int,
) ([]agent.AttemptEvent, error) {
	api.eventAttemptID, api.eventActorID = attemptID, actorID
	api.eventAfter, api.eventLimit = afterSequence, limit
	return api.events, api.eventsErr
}

func (api *agentControlAPIFake) CancelAttempt(
	_ context.Context,
	attemptID, actorID string,
	expectedVersion uint64,
	reason string,
) (agent.AgentAttempt, error) {
	api.cancelAttemptID, api.cancelActorID = attemptID, actorID
	api.cancelVersion, api.cancelReason = expectedVersion, reason
	return api.cancel, api.cancelErr
}

func (api *agentControlAPIFake) ReadEvidence(
	_ context.Context,
	attemptID, actorID string,
	kind agent.EvidenceKind,
) (agent.EvidenceReadResult, error) {
	api.evidenceAttemptID, api.evidenceActorID, api.evidenceKind = attemptID, actorID, kind
	return api.evidence, api.evidenceErr
}

func (api *agentControlAPIFake) ReadPatchFile(
	_ context.Context,
	attemptID, actorID, path string,
	side agent.PatchFileSide,
) (agent.PatchFileReviewResult, error) {
	api.patchFileAttemptID, api.patchFileActorID = attemptID, actorID
	api.patchFilePath, api.patchFileSide = path, side
	return api.patchFile, api.patchFileErr
}

func (api *agentControlAPIFake) MergePatch(
	_ context.Context,
	input agent.MergePatchInput,
) (agent.MergePatchResult, error) {
	api.mergeInput = input
	return api.merge, api.mergeErr
}

func (api *agentControlAPIFake) UndoPatch(
	_ context.Context,
	input agent.UndoPatchInput,
) (agent.UndoPatchResult, error) {
	api.undoInput = input
	return api.undo, api.undoErr
}

func (api *agentControlAPIFake) ListAttemptMerges(
	_ context.Context,
	attemptID, actorID string,
	limit int,
) ([]agent.PatchMergeHistoryItem, error) {
	api.historyAttemptID, api.historyActorID, api.historyLimit = attemptID, actorID, limit
	return api.history, api.historyErr
}

func TestAgentCreateRequiresIdempotencyAndDerivesProjectFromSession(t *testing.T) {
	api := &agentControlAPIFake{create: agent.TaskAttemptResult{
		Attempt: agentTransportAttemptView(agentTransportAttempt, 3, agent.AttemptQueued),
	}}
	resolver := &agentSessionResolverFake{projectID: agentTransportProject}
	router := agentTransportRouter(t, api, resolver)

	withoutKey := httptest.NewRequest(
		http.MethodPost,
		"/sandbox-sessions/"+agentTransportSession+"/agent-attempts",
		bytes.NewBufferString(`{"taskKey":"task.one","instruction":"Implement it.","executorProfile":"codex-default"}`),
	)
	withoutKey.Header.Set("Content-Type", "application/json")
	withoutKeyResponse := httptest.NewRecorder()
	router.ServeHTTP(withoutKeyResponse, withoutKey)
	if withoutKeyResponse.Code != http.StatusBadRequest || api.createInput.OperationID != "" {
		t.Fatalf("missing key status=%d body=%s input=%#v", withoutKeyResponse.Code, withoutKeyResponse.Body.String(), api.createInput)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/sandbox-sessions/"+agentTransportSession+"/agent-attempts",
		bytes.NewBufferString(`{"taskKey":"task.one","instruction":"Implement it.","executorProfile":"codex-default"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "agent-create-one")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated ||
		response.Header().Get("Location") != agentAttemptLocation(agentTransportAttempt) ||
		response.Header().Get("ETag") != `"agent-attempt:`+agentTransportAttempt+`:3"` {
		t.Fatalf("create status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if resolver.sessionID != agentTransportSession || resolver.actorID != agentTransportActor ||
		api.createInput.ProjectID != agentTransportProject ||
		api.createInput.SandboxSessionID != agentTransportSession ||
		api.createInput.ActorID != agentTransportActor ||
		api.createInput.OperationID != "agent-create-one" ||
		api.createInput.TaskKey != "task.one" || api.createInput.Instruction != "Implement it." ||
		api.createInput.ExecutorProfile != "codex-default" {
		t.Fatalf("resolver=%#v input=%#v", resolver, api.createInput)
	}
}

func TestAgentRetryRequiresExactETagAndForwardsImmutableParent(t *testing.T) {
	current := agentTransportAttemptView(agentTransportAttempt, 7, agent.AttemptFailed)
	api := &agentControlAPIFake{
		get: agent.TaskAttemptResult{Attempt: current},
		retry: agent.TaskAttemptResult{
			Attempt: agentTransportAttemptView(agentTransportNewAttempt, 3, agent.AttemptQueued),
		},
	}
	router := agentTransportRouter(t, api, &agentSessionResolverFake{projectID: agentTransportProject})

	stale := httptest.NewRequest(
		http.MethodPost,
		"/agent-attempts/"+agentTransportAttempt+":retry",
		bytes.NewBufferString(`{"reason":"Retry exact task."}`),
	)
	stale.Header.Set("Content-Type", "application/json")
	stale.Header.Set("Idempotency-Key", "agent-retry-one")
	stale.Header.Set("If-Match", `"agent-attempt:`+agentTransportAttempt+`:6"`)
	staleResponse := httptest.NewRecorder()
	router.ServeHTTP(staleResponse, stale)
	if staleResponse.Code != http.StatusPreconditionFailed || api.retryInput.OperationID != "" {
		t.Fatalf("stale retry status=%d body=%s input=%#v", staleResponse.Code, staleResponse.Body.String(), api.retryInput)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/agent-attempts/"+agentTransportAttempt+":retry",
		bytes.NewBufferString(`{"reason":"Retry exact task."}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "agent-retry-two")
	request.Header.Set("If-Match", `"agent-attempt:`+agentTransportAttempt+`:7"`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("Location") != agentAttemptLocation(agentTransportNewAttempt) {
		t.Fatalf("retry status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if api.retryInput.ProjectID != agentTransportProject || api.retryInput.ParentAttemptID != agentTransportAttempt ||
		api.retryInput.ActorID != agentTransportActor || api.retryInput.OperationID != "agent-retry-two" ||
		api.retryInput.Reason != "Retry exact task." {
		t.Fatalf("retry input=%#v", api.retryInput)
	}
}

func TestAgentMergeHistoryIsAttemptScopedAndNormalizesEmptyList(t *testing.T) {
	api := &agentControlAPIFake{}
	router := agentTransportRouter(t, api, &agentSessionResolverFake{projectID: agentTransportProject})
	request := httptest.NewRequest(
		http.MethodGet,
		"/agent-attempts/"+agentTransportAttempt+"/merges?limit=17",
		nil,
	)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != `{"merges":[]}` ||
		api.historyAttemptID != agentTransportAttempt || api.historyActorID != agentTransportActor ||
		api.historyLimit != 17 {
		t.Fatalf("history status=%d body=%s api=%#v", response.Code, response.Body.String(), api)
	}
}

func TestAgentListsAttemptAndContiguousEventPages(t *testing.T) {
	evidenceHash := "sha256:" + strings.Repeat("a", 64)
	api := &agentControlAPIFake{
		list: []agent.AgentAttempt{agentTransportAttemptView(agentTransportAttempt, 4, agent.AttemptClaimed)},
		events: []agent.AttemptEvent{
			{SchemaVersion: agent.AttemptEventSchema, AttemptID: agentTransportAttempt, Sequence: 10},
			{SchemaVersion: agent.AttemptEventSchema, AttemptID: agentTransportAttempt, Sequence: 11},
		},
		evidence: agent.EvidenceReadResult{
			Attempt: agentTransportAttemptView(agentTransportAttempt, 5, agent.AttemptReviewReady),
			Kind:    agent.EvidencePatch,
			Reference: agent.BlobReference{
				Store: agent.AgentEvidenceStore, OwnerID: agentTransportAttempt,
				Ref: agentTransportNewAttempt, ContentHash: evidenceHash, ByteSize: 128,
			},
			MediaType: "application/json", RawHash: evidenceHash,
			Value: []byte(`{"schemaVersion":"agent-platform-patch/v1"}`),
		},
	}
	resolver := &agentSessionResolverFake{projectID: agentTransportProject}
	router := agentTransportRouter(t, api, resolver)

	listRequest := httptest.NewRequest(
		http.MethodGet,
		"/sandbox-sessions/"+agentTransportSession+"/agent-attempts?limit=25",
		nil,
	)
	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || api.listProjectID != agentTransportProject ||
		api.listSessionID != agentTransportSession || api.listActorID != agentTransportActor || api.listLimit != 25 {
		t.Fatalf("list status=%d body=%s api=%#v", listResponse.Code, listResponse.Body.String(), api)
	}

	eventRequest := httptest.NewRequest(
		http.MethodGet,
		"/agent-attempts/"+agentTransportAttempt+"/events?afterSequence=9&limit=2",
		nil,
	)
	eventResponse := httptest.NewRecorder()
	router.ServeHTTP(eventResponse, eventRequest)
	if eventResponse.Code != http.StatusOK || api.eventAttemptID != agentTransportAttempt ||
		api.eventActorID != agentTransportActor || api.eventAfter != 9 || api.eventLimit != 2 ||
		!bytes.Contains(eventResponse.Body.Bytes(), []byte(`"lastSequence":11`)) {
		t.Fatalf("events status=%d body=%s api=%#v", eventResponse.Code, eventResponse.Body.String(), api)
	}

	evidenceRequest := httptest.NewRequest(
		http.MethodGet,
		"/agent-attempts/"+agentTransportAttempt+"/evidence/patch",
		nil,
	)
	evidenceResponse := httptest.NewRecorder()
	router.ServeHTTP(evidenceResponse, evidenceRequest)
	if evidenceResponse.Code != http.StatusOK || api.evidenceAttemptID != agentTransportAttempt ||
		api.evidenceActorID != agentTransportActor || api.evidenceKind != agent.EvidencePatch ||
		evidenceResponse.Header().Get("X-Content-Hash") != evidenceHash ||
		evidenceResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("evidence status=%d headers=%v body=%s api=%#v", evidenceResponse.Code, evidenceResponse.Header(), evidenceResponse.Body.String(), api)
	}
}

func TestAgentPatchFileReturnsRawBytesAndExactAbsenceMetadata(t *testing.T) {
	contentHash := "sha256:" + strings.Repeat("d", 64)
	representationHash := "sha256:" + strings.Repeat("e", 64)
	path := "apps/web/features/conversation/page.tsx"
	attempt := agentTransportAttemptView(agentTransportAttempt, 6, agent.AttemptReviewReady)
	api := &agentControlAPIFake{patchFile: agent.PatchFileReviewResult{
		Attempt: attempt, PatchContentHash: "sha256:" + strings.Repeat("c", 64),
		RepresentationHash: representationHash,
		Path:               path, Side: agent.PatchFileProposed, Exists: true,
		Mode: "100644", ContentHash: contentHash, ByteSize: 5,
		Value: []byte{0, 1, 2, 3, 4},
	}}
	router := agentTransportRouter(t, api, &agentSessionResolverFake{projectID: agentTransportProject})
	request := httptest.NewRequest(
		http.MethodGet,
		"/agent-attempts/"+agentTransportAttempt+"/patch-file?path="+path+"&side=proposed",
		nil,
	)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), []byte{0, 1, 2, 3, 4}) ||
		response.Header().Get("Content-Type") != "application/octet-stream" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" ||
		response.Header().Get("X-File-Exists") != "true" ||
		response.Header().Get("X-Content-Hash") != contentHash ||
		response.Header().Get("X-File-Mode") != "100644" ||
		response.Header().Get("X-Byte-Size") != "5" ||
		response.Header().Get("ETag") != `"agent-patch-file:`+agentTransportAttempt+`:proposed:`+representationHash+`"` ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("patch file status=%d headers=%v body=%v", response.Code, response.Header(), response.Body.Bytes())
	}
	if api.patchFileAttemptID != agentTransportAttempt || api.patchFileActorID != agentTransportActor ||
		api.patchFilePath != path || api.patchFileSide != agent.PatchFileProposed {
		t.Fatalf("patch file request=%#v", api)
	}

	api.patchFile = agent.PatchFileReviewResult{
		Attempt: attempt, PatchContentHash: "sha256:" + strings.Repeat("c", 64),
		RepresentationHash: "sha256:" + strings.Repeat("f", 64),
		Path:               path, Side: agent.PatchFileBase,
	}
	absentRequest := httptest.NewRequest(
		http.MethodGet,
		"/agent-attempts/"+agentTransportAttempt+"/patch-file?path="+path+"&side=base",
		nil,
	)
	absentResponse := httptest.NewRecorder()
	router.ServeHTTP(absentResponse, absentRequest)
	if absentResponse.Code != http.StatusNoContent || absentResponse.Body.Len() != 0 ||
		absentResponse.Header().Get("X-File-Exists") != "false" ||
		absentResponse.Header().Get("X-Byte-Size") != "0" ||
		absentResponse.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("absent patch file status=%d headers=%v body=%v", absentResponse.Code,
			absentResponse.Header(), absentResponse.Body.Bytes())
	}
}

func TestAgentMergeRequiresExactFencesAndReturnsStructuredConflict(t *testing.T) {
	mergeID := agentTransportNewAttempt
	contentHash := "sha256:" + strings.Repeat("b", 64)
	api := &agentControlAPIFake{merge: agent.MergePatchResult{Plan: agent.PatchMergePlanRecord{
		ID: mergeID, ContentHash: contentHash, Disposition: agent.PatchMergePlanned,
	}}}
	router := agentTransportRouter(t, api, &agentSessionResolverFake{projectID: agentTransportProject})
	body := `{"expectedSessionVersion":9,"expectedSessionEpoch":2,"expectedCandidateVersion":7,"expectedWriterLeaseEpoch":4}`
	request := httptest.NewRequest(
		http.MethodPost, "/agent-attempts/"+agentTransportAttempt+"/merge", bytes.NewBufferString(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "agent-merge-one")
	request.Header.Set("If-Match", `"agent-attempt:`+agentTransportAttempt+`:8"`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated ||
		response.Header().Get("Location") != "/v1/agent-merges/"+mergeID ||
		response.Header().Get("ETag") != `"agent-merge:`+mergeID+`:`+contentHash+`"` ||
		response.Header().Get("X-Agent-Merge-Disposition") != string(agent.PatchMergePlanned) {
		t.Fatalf("merge status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if api.mergeInput.AttemptID != agentTransportAttempt || api.mergeInput.ActorID != agentTransportActor ||
		api.mergeInput.OperationID != "agent-merge-one" || api.mergeInput.ExpectedAttemptVersion != 8 ||
		api.mergeInput.ExpectedSessionVersion != 9 || api.mergeInput.ExpectedSessionEpoch != 2 ||
		api.mergeInput.ExpectedCandidateVersion != 7 || api.mergeInput.ExpectedWriterLeaseEpoch != 4 {
		t.Fatalf("merge input=%#v", api.mergeInput)
	}

	api.merge = agent.MergePatchResult{Plan: agent.PatchMergePlanRecord{
		ID: mergeID, ContentHash: contentHash, Disposition: agent.PatchMergeConflicted,
		Conflicts: []agent.PatchMergeConflict{{Path: "apps/web/page.tsx"}},
	}}
	conflict := httptest.NewRequest(
		http.MethodPost, "/agent-attempts/"+agentTransportAttempt+"/merge", bytes.NewBufferString(body),
	)
	conflict.Header.Set("Content-Type", "application/json")
	conflict.Header.Set("Idempotency-Key", "agent-merge-two")
	conflict.Header.Set("If-Match", `"agent-attempt:`+agentTransportAttempt+`:8"`)
	conflictResponse := httptest.NewRecorder()
	router.ServeHTTP(conflictResponse, conflict)
	if conflictResponse.Code != http.StatusConflict ||
		!bytes.Contains(conflictResponse.Body.Bytes(), []byte(`"disposition":"conflicted"`)) ||
		!bytes.Contains(conflictResponse.Body.Bytes(), []byte(`"path":"apps/web/page.tsx"`)) {
		t.Fatalf("conflict status=%d body=%s", conflictResponse.Code, conflictResponse.Body.String())
	}
}

func TestAgentUndoRequiresImmutableMergeETagAndReturnsStructuredConflict(t *testing.T) {
	mergeID := agentTransportNewAttempt
	mergeHash := "sha256:" + strings.Repeat("b", 64)
	undoHash := "sha256:" + strings.Repeat("c", 64)
	api := &agentControlAPIFake{undo: agent.UndoPatchResult{Plan: agent.PatchUndoPlanRecord{
		ID: agentTransportUndo, ContentHash: undoHash, Disposition: agent.PatchMergePlanned,
	}}}
	router := agentTransportRouter(t, api, &agentSessionResolverFake{projectID: agentTransportProject})
	body := `{"expectedSessionVersion":10,"expectedSessionEpoch":2,"expectedCandidateVersion":8,"expectedWriterLeaseEpoch":4}`

	invalid := httptest.NewRequest(
		http.MethodPost, "/agent-merges/"+mergeID+"/undo", bytes.NewBufferString(body),
	)
	invalid.Header.Set("Content-Type", "application/json")
	invalid.Header.Set("Idempotency-Key", "agent-undo-invalid")
	invalid.Header.Set("If-Match", `"agent-merge:`+mergeID+`:sha256:not-a-digest"`)
	invalidResponse := httptest.NewRecorder()
	router.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest || api.undoInput.OperationID != "" {
		t.Fatalf("invalid ETag status=%d body=%s input=%#v", invalidResponse.Code, invalidResponse.Body.String(), api.undoInput)
	}

	request := httptest.NewRequest(
		http.MethodPost, "/agent-merges/"+mergeID+"/undo", bytes.NewBufferString(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "agent-undo-one")
	request.Header.Set("If-Match", `"agent-merge:`+mergeID+`:`+mergeHash+`"`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated ||
		response.Header().Get("Location") != "/v1/agent-undos/"+agentTransportUndo ||
		response.Header().Get("ETag") != `"agent-undo:`+agentTransportUndo+`:`+undoHash+`"` ||
		response.Header().Get("X-Agent-Undo-Disposition") != string(agent.PatchMergePlanned) {
		t.Fatalf("undo status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if api.undoInput.MergeID != mergeID || api.undoInput.ExpectedMergeContentHash != mergeHash ||
		api.undoInput.ActorID != agentTransportActor || api.undoInput.OperationID != "agent-undo-one" ||
		api.undoInput.ExpectedSessionVersion != 10 || api.undoInput.ExpectedSessionEpoch != 2 ||
		api.undoInput.ExpectedCandidateVersion != 8 || api.undoInput.ExpectedWriterLeaseEpoch != 4 {
		t.Fatalf("undo input=%#v", api.undoInput)
	}

	api.undo = agent.UndoPatchResult{Plan: agent.PatchUndoPlanRecord{
		ID: agentTransportUndo, ContentHash: undoHash, Disposition: agent.PatchMergeConflicted,
		Conflicts: []agent.PatchMergeConflict{{Path: "apps/web/page.tsx"}},
	}}
	conflict := httptest.NewRequest(
		http.MethodPost, "/agent-merges/"+mergeID+"/undo", bytes.NewBufferString(body),
	)
	conflict.Header.Set("Content-Type", "application/json")
	conflict.Header.Set("Idempotency-Key", "agent-undo-two")
	conflict.Header.Set("If-Match", `"agent-merge:`+mergeID+`:`+mergeHash+`"`)
	conflictResponse := httptest.NewRecorder()
	router.ServeHTTP(conflictResponse, conflict)
	if conflictResponse.Code != http.StatusConflict ||
		!bytes.Contains(conflictResponse.Body.Bytes(), []byte(`"disposition":"conflicted"`)) ||
		!bytes.Contains(conflictResponse.Body.Bytes(), []byte(`"path":"apps/web/page.tsx"`)) {
		t.Fatalf("conflict status=%d body=%s", conflictResponse.Code, conflictResponse.Body.String())
	}
}

func agentTransportRouter(
	t *testing.T,
	api *agentControlAPIFake,
	resolver AgentSessionProjectResolver,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("platform_identity", worksmiddleware.Identity{
			Session: auth.Session{User: auth.User{ID: agentTransportActor}}, Transport: "bearer",
		})
		c.Next()
	})
	handler, err := NewAgentHandler(AgentDependencies{
		Service: api, Review: api, PatchFiles: api,
		Merge: api, Undo: api, History: api, Sessions: resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterAgentRoutes(router, handler, worksmiddleware.CaptureIdempotencyKey(true)); err != nil {
		t.Fatal(err)
	}
	return router
}

func agentTransportAttemptView(id string, version uint64, state agent.AttemptState) agent.AgentAttempt {
	return agent.AgentAttempt{
		SchemaVersion: agent.AttemptSchemaVersion,
		ID:            id, ProjectID: agentTransportProject, SandboxSessionID: agentTransportSession,
		State: state, Version: version,
	}
}

var _ AgentControlAPI = (*agentControlAPIFake)(nil)
var _ AgentReviewAPI = (*agentControlAPIFake)(nil)
var _ AgentPatchFileReviewAPI = (*agentControlAPIFake)(nil)
var _ AgentMergeAPI = (*agentControlAPIFake)(nil)
var _ AgentUndoAPI = (*agentControlAPIFake)(nil)
var _ AgentHistoryAPI = (*agentControlAPIFake)(nil)
var _ AgentSessionProjectResolver = (*agentSessionResolverFake)(nil)
