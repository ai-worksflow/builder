package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/auth"
	"github.com/worksflow/builder/backend/internal/core"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/sandbox"
)

const (
	sandboxTransportActor      = "10000000-0000-4000-8000-000000000001"
	sandboxTransportProject    = "10000000-0000-4000-8000-000000000002"
	sandboxTransportSession    = "10000000-0000-4000-8000-000000000003"
	sandboxTransportCandidate  = "10000000-0000-4000-8000-000000000004"
	sandboxTransportProcess    = "10000000-0000-4000-8000-000000000005"
	sandboxTransportTerminal   = "10000000-0000-4000-8000-000000000006"
	sandboxTransportCheckpoint = "10000000-0000-4000-8000-000000000007"
	sandboxTransportProposal   = "10000000-0000-4000-8000-000000000008"
)

type sandboxAPIFake struct {
	resolvedSession string
	createInput     sandbox.CreateSessionInput
	createCalls     int
	createError     error
	ticketInput     sandbox.IssueConnectionTicketInput
	ticketCalls     int
	readFileInput   sandbox.ReadFileInput
	readFileCalls   int
	readFileError   error
	mutationInput   sandbox.FileMutationInput
	mutationCalls   int
	controlAction   string
	controlInput    sandbox.SessionControlInput
	terminateInput  sandbox.TerminateSessionInput
	startProcess    sandbox.StartProcessInput
	signalProcess   sandbox.SignalProcessInput
	createTerminal  sandbox.CreateTerminalInput
	previewInput    sandbox.IssuePreviewInput
	freezeInput     sandbox.CandidateFreezeInput
	freezeReplayed  bool
	abandonInput    sandbox.CandidateAbandonInput
	abandonError    error
}

func (api *sandboxAPIFake) CreateConnectionTicket(_ context.Context, input sandbox.IssueConnectionTicketInput) (sandbox.ConnectionTicketView, error) {
	api.ticketCalls++
	api.ticketInput = input
	return sandbox.ConnectionTicketView{
		SchemaVersion: sandbox.ConnectionTicketSchemaVersion,
		ID:            sandboxTransportCandidate, Ticket: "ticket-secret", SessionID: sandboxTransportSession,
		SessionEpoch: 1, Channels: input.Channels, Cursors: input.Cursors,
		WebSocketPath: "/v1/sandbox-stream",
	}, nil
}

func (api *sandboxAPIFake) CreateSession(_ context.Context, input sandbox.CreateSessionInput) (sandbox.SessionView, error) {
	api.createCalls++
	api.createInput = input
	return sandboxTransportView(1, 2), api.createError
}

func (api *sandboxAPIFake) SuspendSession(_ context.Context, input sandbox.SessionControlInput) (sandbox.SessionView, error) {
	api.controlAction, api.controlInput = "suspend", input
	view := sandboxTransportView(input.ExpectedSessionVersion+2, 2)
	view.State = sandbox.StateSuspended
	return view, nil
}

func (api *sandboxAPIFake) ResumeSession(_ context.Context, input sandbox.SessionControlInput) (sandbox.SessionView, error) {
	api.controlAction, api.controlInput = "resume", input
	view := sandboxTransportView(input.ExpectedSessionVersion+3, 3)
	view.SessionEpoch = input.ExpectedSessionEpoch + 1
	view.Candidate.SessionEpoch = view.SessionEpoch
	view.State = sandbox.StateReady
	return view, nil
}

func (api *sandboxAPIFake) TerminateSession(_ context.Context, input sandbox.TerminateSessionInput) (sandbox.SessionView, error) {
	api.controlAction, api.controlInput, api.terminateInput = "terminate", input.SessionControlInput, input
	view := sandboxTransportView(input.ExpectedSessionVersion+2, 2)
	view.State = sandbox.StateTerminated
	return view, nil
}

func (api *sandboxAPIFake) StartProcess(_ context.Context, input sandbox.StartProcessInput) (sandbox.ProcessResult, error) {
	api.startProcess = input
	return sandbox.ProcessResult{
		Session: sandboxTransportView(3, 2), Process: sandboxTransportProcessView(1, sandbox.ProcessRunning),
	}, nil
}

func (api *sandboxAPIFake) GetProcess(context.Context, string, string, string, string) (sandbox.ProcessResult, error) {
	return sandbox.ProcessResult{
		Session: sandboxTransportView(3, 2), Process: sandboxTransportProcessView(2, sandbox.ProcessRunning),
	}, nil
}

func (api *sandboxAPIFake) ListProcesses(context.Context, string, string, string, int) (sandbox.ProcessList, error) {
	return sandbox.ProcessList{
		Session:   sandboxTransportView(3, 2),
		Processes: []sandbox.ProcessView{sandboxTransportProcessView(2, sandbox.ProcessRunning)},
	}, nil
}

func (api *sandboxAPIFake) SignalProcess(_ context.Context, input sandbox.SignalProcessInput) (sandbox.ProcessResult, error) {
	api.signalProcess = input
	return sandbox.ProcessResult{
		Session: sandboxTransportView(3, 2), Process: sandboxTransportProcessView(input.ExpectedProcessVersion+1, sandbox.ProcessRunning),
	}, nil
}

func (api *sandboxAPIFake) ProcessLogs(context.Context, string, string, string, string, int64, int64) (sandbox.ProcessLogResult, error) {
	return sandbox.ProcessLogResult{
		Session: sandboxTransportView(3, 2), Process: sandboxTransportProcessView(2, sandbox.ProcessRunning),
		Log: sandbox.RuntimeProcessLog{
			SchemaVersion: sandbox.RuntimeProcessSchemaVersion, ID: sandboxTransportProcess,
			Offset: 0, NextOffset: 4, Value: []byte("test"),
		},
	}, nil
}

func (api *sandboxAPIFake) CreateTerminal(_ context.Context, input sandbox.CreateTerminalInput) (sandbox.TerminalResult, error) {
	api.createTerminal = input
	return sandbox.TerminalResult{
		Session: sandboxTransportView(3, 2), Terminal: sandboxTransportTerminalView(2, sandbox.TerminalRunning),
	}, nil
}

func (api *sandboxAPIFake) GetTerminal(context.Context, string, string, string, string) (sandbox.TerminalResult, error) {
	return sandbox.TerminalResult{
		Session: sandboxTransportView(3, 2), Terminal: sandboxTransportTerminalView(2, sandbox.TerminalRunning),
	}, nil
}

func (api *sandboxAPIFake) ListTerminals(context.Context, string, string, string, int) (sandbox.TerminalList, error) {
	return sandbox.TerminalList{
		Session:   sandboxTransportView(3, 2),
		Terminals: []sandbox.TerminalView{sandboxTransportTerminalView(2, sandbox.TerminalRunning)},
	}, nil
}

func (api *sandboxAPIFake) ListPorts(context.Context, string, string, string) (sandbox.PortList, error) {
	return sandbox.PortList{
		Session: sandboxTransportView(3, 2),
		Ports: []sandbox.PortView{{
			SchemaVersion: sandbox.PortSchemaVersion, Name: "web-http", ServiceID: "web-ui",
			Number: 3000, Protocol: "http", State: sandbox.PortListening, Healthy: true, Previewable: true,
		}},
	}, nil
}

func (api *sandboxAPIFake) CreatePreviewLink(_ context.Context, input sandbox.IssuePreviewInput) (sandbox.PreviewLink, error) {
	api.previewInput = input
	return sandbox.PreviewLink{
		SchemaVersion: sandbox.PreviewGrantSchemaVersion, ID: sandboxTransportCandidate,
		SessionID: sandboxTransportSession, SessionEpoch: 1,
		Port: sandbox.PortView{
			SchemaVersion: sandbox.PortSchemaVersion, Name: "web-http", ServiceID: "web-ui",
			Number: 3000, Protocol: "http", State: sandbox.PortListening, Healthy: true, Previewable: true,
		},
		URL: "https://" + strings.Repeat("a", 48) + ".preview.example/",
	}, nil
}

func (api *sandboxAPIFake) ResolveProject(_ context.Context, sessionID, actorID string) (string, error) {
	api.resolvedSession = sessionID + ":" + actorID
	return sandboxTransportProject, nil
}
func (api *sandboxAPIFake) Get(context.Context, string, string, string) (sandbox.SessionView, error) {
	return sandboxTransportView(3, 2), nil
}
func (api *sandboxAPIFake) Tree(context.Context, string, string, string) (sandbox.RepositoryView, error) {
	return sandbox.RepositoryView{}, nil
}
func (api *sandboxAPIFake) ReadFile(_ context.Context, input sandbox.ReadFileInput) (sandbox.FileView, error) {
	api.readFileCalls++
	api.readFileInput = input
	if api.readFileError != nil {
		return sandbox.FileView{}, api.readFileError
	}
	return sandbox.FileView{
		Session: sandboxTransportView(3, 2),
		Candidate: repository.CandidateWorkspace{
			ID: sandboxTransportCandidate, ProjectID: sandboxTransportProject,
			Version: 2, JournalSequence: 0, SessionEpoch: 1, WriterLeaseEpoch: 1,
			CurrentTree: repository.TreeManifest{TreeHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		File: repository.TreeFile{
			Path: "src/app.ts", Mode: "100644",
			ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		Value: []byte("value"), ContentType: "application/octet-stream",
	}, nil
}
func (api *sandboxAPIFake) AcquireWriterLease(context.Context, sandbox.AcquireWriterLeaseInput) (sandbox.CandidateSessionResult, error) {
	return sandbox.CandidateSessionResult{Session: sandboxTransportView(4, 3)}, nil
}
func (api *sandboxAPIFake) MutateFile(_ context.Context, input sandbox.FileMutationInput) (sandbox.FileMutationResult, error) {
	api.mutationCalls++
	api.mutationInput = input
	return sandbox.FileMutationResult{Session: sandboxTransportView(4, 3)}, nil
}
func (api *sandboxAPIFake) Checkpoint(context.Context, sandbox.CheckpointInput) (sandbox.CheckpointResult, error) {
	return sandbox.CheckpointResult{}, nil
}

func (api *sandboxAPIFake) FreezeCandidate(
	_ context.Context,
	input sandbox.CandidateFreezeInput,
) (sandbox.CandidateFreezeResult, error) {
	api.freezeInput = input
	view := sandboxTransportView(input.ExpectedSessionVersion+1, input.ExpectedCandidateVersion+1)
	view.Candidate.Status = repository.CandidateFrozen
	return sandbox.CandidateFreezeResult{
		Session:  view,
		Proposal: core.ImplementationProposal{ID: sandboxTransportProposal},
		Replayed: api.freezeReplayed,
	}, nil
}

func (api *sandboxAPIFake) AbandonCandidate(
	_ context.Context,
	input sandbox.CandidateAbandonInput,
) (sandbox.CandidateSessionResult, error) {
	api.abandonInput = input
	if api.abandonError != nil {
		return sandbox.CandidateSessionResult{}, api.abandonError
	}
	view := sandboxTransportView(input.ExpectedSessionVersion+2, input.ExpectedCandidateVersion+1)
	view.State = sandbox.StateTerminated
	view.Candidate.ID = input.CandidateID
	view.Candidate.Status = repository.CandidateAbandoned
	view.Candidate.WriterLeaseEpoch = input.ExpectedWriterLeaseEpoch + 1
	return sandbox.CandidateSessionResult{
		Session: view,
		Candidate: repository.CandidateWorkspace{
			ID: input.CandidateID, Status: repository.CandidateAbandoned,
		},
	}, nil
}

func TestSandboxTransportCreatesProjectScopedSessionWithoutEntityPrecondition(t *testing.T) {
	api := &sandboxAPIFake{}
	router := sandboxTransportRouter(t, api)
	request := httptest.NewRequest(
		http.MethodPost,
		"/projects/"+sandboxTransportProject+"/sandbox-sessions",
		bytes.NewBufferString(`{"candidateId":"`+sandboxTransportCandidate+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "create-sandbox-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("POST status=%d body=%s", response.Code, response.Body.String())
	}
	if api.createCalls != 1 || api.createInput.ProjectID != sandboxTransportProject ||
		api.createInput.CandidateID != sandboxTransportCandidate || api.createInput.ActorID != sandboxTransportActor {
		t.Fatalf("create did not preserve authenticated project input: %#v", api.createInput)
	}
	if response.Header().Get("Location") != "/v1/sandbox-sessions/"+sandboxTransportSession ||
		response.Header().Get("X-Sandbox-Session-ETag") != `"sandbox:`+sandboxTransportSession+`:1"` {
		t.Fatalf("missing creation location or session fence: %#v", response.Header())
	}
}

func TestSandboxTransportReturnsCreatedFailedResourceAfterBootstrapFailure(t *testing.T) {
	view := sandboxTransportView(2, 2)
	view.State = sandbox.StateFailed
	view.FailureReason = "runtime bootstrap failed"
	api := &sandboxAPIFake{createError: &sandbox.RuntimeBootstrapError{
		Session: view, Cause: errors.New("runtime unavailable"),
	}}
	router := sandboxTransportRouter(t, api)
	request := httptest.NewRequest(
		http.MethodPost,
		"/projects/"+sandboxTransportProject+"/sandbox-sessions",
		bytes.NewBufferString(`{"candidateId":"`+sandboxTransportCandidate+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "create-sandbox-bootstrap-failure")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || response.Header().Get("Location") != "/v1/sandbox-sessions/"+sandboxTransportSession {
		t.Fatalf("committed failed session became unreachable: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var created sandbox.SessionView
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.State != sandbox.StateFailed ||
		created.FailureReason != "runtime bootstrap failed" {
		t.Fatalf("bootstrap failure resource body = %#v, %v", created, err)
	}
}

func TestSandboxTransportIssuesOriginBoundConnectionTicketWithoutEntityPrecondition(t *testing.T) {
	api := &sandboxAPIFake{}
	router := sandboxTransportRouter(t, api)
	request := httptest.NewRequest(
		http.MethodPost,
		"/sandbox-sessions/"+sandboxTransportSession+"/connection-tickets",
		bytes.NewBufferString(`{"channels":["control","pty"],"cursors":[{"channel":"pty","lastAckedSeq":4}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://builder.example")
	request.Header.Set("Idempotency-Key", "sandbox-ticket-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("POST ticket status=%d body=%s", response.Code, response.Body.String())
	}
	if api.ticketCalls != 1 || api.ticketInput.ProjectID != sandboxTransportProject ||
		api.ticketInput.SessionID != sandboxTransportSession || api.ticketInput.ActorID != sandboxTransportActor ||
		api.ticketInput.Origin != "https://builder.example" || len(api.ticketInput.Channels) != 2 ||
		len(api.ticketInput.Cursors) != 1 || api.ticketInput.Cursors[0].LastAckedSeq != 4 {
		t.Fatalf("ticket did not preserve authoritative request scope: %#v", api.ticketInput)
	}
	if response.Header().Get("Location") != "/v1/sandbox-connection-tickets/"+sandboxTransportCandidate {
		t.Fatalf("missing ticket receipt location: %#v", response.Header())
	}
}

func TestSandboxTransportResolvesAuthoritativeProjectAndReturnsFences(t *testing.T) {
	api := &sandboxAPIFake{}
	router := sandboxTransportRouter(t, api)
	request := httptest.NewRequest(http.MethodGet, "/sandbox-sessions/"+sandboxTransportSession, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", response.Code, response.Body.String())
	}
	if api.resolvedSession != sandboxTransportSession+":"+sandboxTransportActor {
		t.Fatalf("session project was not resolved with authenticated actor: %q", api.resolvedSession)
	}
	if response.Header().Get("ETag") != `"sandbox:`+sandboxTransportSession+`:3"` ||
		response.Header().Get("X-Sandbox-Session-ETag") != `"sandbox:`+sandboxTransportSession+`:3"` ||
		response.Header().Get("X-Sandbox-Session-Epoch") != "1" ||
		response.Header().Get("X-Candidate-Version") != "2" ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing authoritative response fences: %#v", response.Header())
	}
}

func TestSandboxTransportFileResponseKeepsSessionAndFileETagsSeparate(t *testing.T) {
	api := &sandboxAPIFake{}
	router := sandboxTransportRouter(t, api)
	request := httptest.NewRequest(http.MethodGet, "/sandbox-sessions/"+sandboxTransportSession+"/files/src/app.ts", nil)
	request.Header.Set("X-Sandbox-Session-Epoch", "1")
	request.Header.Set("X-Expected-Candidate-ID", sandboxTransportCandidate)
	request.Header.Set("X-Candidate-Version", "2")
	request.Header.Set("X-Candidate-Journal-Sequence", "0")
	request.Header.Set("X-Writer-Lease-Epoch", "1")
	request.Header.Set("X-Candidate-Tree-Hash", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	request.Header.Set("X-Expected-File-Hash", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET file status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Sandbox-Session-ETag") != `"sandbox:`+sandboxTransportSession+`:3"` ||
		response.Header().Get("ETag") == response.Header().Get("X-Sandbox-Session-ETag") {
		t.Fatalf("file and session ETags were not kept separate: %#v", response.Header())
	}
	if api.readFileCalls != 1 || api.readFileInput.ProjectID != sandboxTransportProject ||
		api.readFileInput.SessionID != sandboxTransportSession || api.readFileInput.ActorID != sandboxTransportActor ||
		api.readFileInput.Path != "src/app.ts" || api.readFileInput.ExpectedSessionEpoch != 1 ||
		api.readFileInput.ExpectedCandidateID != sandboxTransportCandidate ||
		api.readFileInput.ExpectedCandidateVersion != 2 || api.readFileInput.ExpectedJournalSequence != 0 ||
		api.readFileInput.ExpectedWriterLeaseEpoch != 1 ||
		api.readFileInput.ExpectedTreeHash != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		api.readFileInput.ExpectedFileHash != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("GET file did not preserve exact head fences: %#v", api.readFileInput)
	}
	if response.Header().Get("X-Candidate-ID") != sandboxTransportCandidate ||
		response.Header().Get("X-Candidate-Journal-Sequence") != "0" {
		t.Fatalf("GET file response omitted exact Candidate identity: %#v", response.Header())
	}
}

func TestSandboxTransportRejectsUnfencedFileReadBeforeService(t *testing.T) {
	api := &sandboxAPIFake{}
	router := sandboxTransportRouter(t, api)
	request := httptest.NewRequest(http.MethodGet, "/sandbox-sessions/"+sandboxTransportSession+"/files/src/app.ts", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || api.readFileCalls != 0 {
		t.Fatalf("unfenced GET file status=%d calls=%d body=%s", response.Code, api.readFileCalls, response.Body.String())
	}
}

func TestSandboxTransportReturnsStableConflictWhenFileHeadChanges(t *testing.T) {
	api := &sandboxAPIFake{readFileError: sandbox.ErrFileHeadChanged}
	router := sandboxTransportRouter(t, api)
	request := httptest.NewRequest(http.MethodGet, "/sandbox-sessions/"+sandboxTransportSession+"/files/src/app.ts", nil)
	request.Header.Set("X-Sandbox-Session-Epoch", "1")
	request.Header.Set("X-Expected-Candidate-ID", sandboxTransportCandidate)
	request.Header.Set("X-Candidate-Version", "2")
	request.Header.Set("X-Candidate-Journal-Sequence", "0")
	request.Header.Set("X-Writer-Lease-Epoch", "1")
	request.Header.Set("X-Candidate-Tree-Hash", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	request.Header.Set("X-Expected-File-Hash", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || api.readFileCalls != 1 ||
		!strings.Contains(response.Body.String(), `"code":"sandbox_file_head_changed"`) {
		t.Fatalf("changed-head GET status=%d calls=%d body=%s", response.Code, api.readFileCalls, response.Body.String())
	}
}

func TestSandboxTransportPUTCarriesRawBytesAndExactFences(t *testing.T) {
	api := &sandboxAPIFake{}
	router := sandboxTransportRouter(t, api)
	request := httptest.NewRequest(
		http.MethodPut,
		"/sandbox-sessions/"+sandboxTransportSession+"/files/src/app.ts",
		bytes.NewReader([]byte("export const value = 1\n")),
	)
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("If-Match", `"sandbox:`+sandboxTransportSession+`:3"`)
	request.Header.Set("Idempotency-Key", "save-src-app-1")
	request.Header.Set("X-Sandbox-Session-Epoch", "1")
	request.Header.Set("X-Candidate-Version", "2")
	request.Header.Set("X-Writer-Lease-Epoch", "1")
	request.Header.Set("X-Expected-File-Hash", "absent")
	request.Header.Set("X-File-Mode", "100644")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", response.Code, response.Body.String())
	}
	input := api.mutationInput
	if api.mutationCalls != 1 || input.ProjectID != sandboxTransportProject ||
		input.SessionID != sandboxTransportSession || input.ActorID != sandboxTransportActor ||
		input.ExpectedSessionVersion != 3 || input.ExpectedSessionEpoch != 1 ||
		input.ExpectedCandidateVersion != 2 || input.ExpectedWriterLeaseEpoch != 1 ||
		input.OperationID != "save-src-app-1" || input.Kind != repository.OperationUpsert ||
		input.Path != "src/app.ts" || input.ExpectedFileHash != "" || input.Mode != "100644" ||
		string(input.Value) != "export const value = 1\n" {
		t.Fatalf("PUT did not preserve exact server input: %#v", input)
	}
}

func TestSandboxTransportRejectsMissingFilePreconditionAndEncodedSeparator(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		want int
	}{
		{name: "missing file precondition", path: "/files/src/app.ts", want: http.StatusPreconditionRequired},
		{name: "encoded separator", path: "/files/src%2Fapp.ts", want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := &sandboxAPIFake{}
			router := sandboxTransportRouter(t, api)
			request := httptest.NewRequest(http.MethodPut, "/sandbox-sessions/"+sandboxTransportSession+test.path, bytes.NewReader(nil))
			request.Header.Set("Content-Type", "application/octet-stream")
			request.Header.Set("If-Match", `"sandbox:`+sandboxTransportSession+`:3"`)
			request.Header.Set("Idempotency-Key", "unsafe-save")
			request.Header.Set("X-Sandbox-Session-Epoch", "1")
			request.Header.Set("X-Candidate-Version", "2")
			request.Header.Set("X-Writer-Lease-Epoch", "1")
			if test.name == "encoded separator" {
				request.Header.Set("X-Expected-File-Hash", "absent")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want || api.mutationCalls != 0 {
				t.Fatalf("status=%d body=%s mutationCalls=%d", response.Code, response.Body.String(), api.mutationCalls)
			}
			var details map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &details); err != nil || details["code"] == "" {
				t.Fatalf("response is not a machine-readable problem: %s (%v)", response.Body.String(), err)
			}
		})
	}
}

func TestSandboxTransportLifecycleActionUsesColonRouteAndSessionFences(t *testing.T) {
	for _, test := range []struct {
		action string
		body   string
	}{
		{action: "suspend", body: ""},
		{action: "resume", body: ""},
		{action: "terminate", body: `{"reason":"user closed the sandbox"}`},
	} {
		t.Run(test.action, func(t *testing.T) {
			api := &sandboxAPIFake{}
			router := sandboxTransportRouter(t, api)
			request := httptest.NewRequest(
				http.MethodPost,
				"/sandbox-sessions/"+sandboxTransportSession+":"+test.action,
				bytes.NewBufferString(test.body),
			)
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			request.Header.Set("If-Match", `"sandbox:`+sandboxTransportSession+`:3"`)
			request.Header.Set("X-Sandbox-Session-Epoch", "1")
			request.Header.Set("Idempotency-Key", "sandbox-control-"+test.action)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("POST %s status=%d body=%s", test.action, response.Code, response.Body.String())
			}
			if api.controlAction != test.action || api.controlInput.ProjectID != sandboxTransportProject ||
				api.controlInput.SessionID != sandboxTransportSession || api.controlInput.ActorID != sandboxTransportActor ||
				api.controlInput.ExpectedSessionVersion != 3 || api.controlInput.ExpectedSessionEpoch != 1 {
				t.Fatalf("control input = %#v action=%q", api.controlInput, api.controlAction)
			}
			if test.action == "terminate" && api.terminateInput.Reason != "user closed the sandbox" {
				t.Fatalf("terminate reason = %q", api.terminateInput.Reason)
			}
		})
	}
}

func TestSandboxTransportFreezesExactCandidateIntoProposal(t *testing.T) {
	api := &sandboxAPIFake{}
	router := sandboxTransportRouter(t, api)
	request := httptest.NewRequest(
		http.MethodPost,
		"/sandbox-sessions/"+sandboxTransportSession+":freeze",
		bytes.NewBufferString(`{"checkpointId":"`+sandboxTransportCheckpoint+`","reason":"submit exact candidate"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"sandbox:`+sandboxTransportSession+`:3"`)
	request.Header.Set("X-Sandbox-Session-Epoch", "1")
	request.Header.Set("X-Candidate-Version", "2")
	request.Header.Set("X-Writer-Lease-Epoch", "1")
	request.Header.Set("Idempotency-Key", "freeze-candidate-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated ||
		response.Header().Get("Location") != "/v1/implementation-proposals/"+sandboxTransportProposal {
		t.Fatalf("freeze response status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	input := api.freezeInput
	if input.ProjectID != sandboxTransportProject || input.SessionID != sandboxTransportSession ||
		input.ActorID != sandboxTransportActor || input.RequestKey != "freeze-candidate-1" ||
		input.CheckpointID != sandboxTransportCheckpoint || input.Reason != "submit exact candidate" ||
		input.ExpectedSessionVersion != 3 || input.ExpectedSessionEpoch != 1 ||
		input.ExpectedCandidateVersion != 2 || input.ExpectedWriterLeaseEpoch != 1 {
		t.Fatalf("freeze input = %#v", input)
	}
}

func TestSandboxTransportAbandonsExactCandidateWithFullFences(t *testing.T) {
	api := &sandboxAPIFake{}
	router := sandboxTransportRouter(t, api)
	request := httptest.NewRequest(
		http.MethodPost,
		"/sandbox-sessions/"+sandboxTransportSession+":abandon",
		bytes.NewBufferString(`{"candidateId":"`+sandboxTransportCandidate+`","checkpointId":"`+sandboxTransportCheckpoint+`","reason":"discard superseded experiment"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"sandbox:`+sandboxTransportSession+`:3"`)
	request.Header.Set("X-Sandbox-Session-Epoch", "1")
	request.Header.Set("X-Candidate-Version", "2")
	request.Header.Set("X-Writer-Lease-Epoch", "1")
	request.Header.Set("Idempotency-Key", "abandon-candidate-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("abandon response status=%d body=%s", response.Code, response.Body.String())
	}
	input := api.abandonInput
	if input.ProjectID != sandboxTransportProject || input.SessionID != sandboxTransportSession ||
		input.CandidateID != sandboxTransportCandidate || input.ActorID != sandboxTransportActor ||
		input.CheckpointID != sandboxTransportCheckpoint || input.Reason != "discard superseded experiment" ||
		input.ExpectedSessionVersion != 3 || input.ExpectedSessionEpoch != 1 ||
		input.ExpectedCandidateVersion != 2 || input.ExpectedWriterLeaseEpoch != 1 {
		t.Fatalf("abandon input = %#v", input)
	}
	var result sandbox.CandidateSessionResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil ||
		result.Session.State != sandbox.StateTerminated ||
		result.Session.Candidate.Status != repository.CandidateAbandoned ||
		result.Candidate.Status != repository.CandidateAbandoned {
		t.Fatalf("abandon response is not exact terminal projection: %#v err=%v", result, err)
	}
}

func TestSandboxTransportExposesDurableAbandonCleanupRetry(t *testing.T) {
	pending := sandboxTransportView(4, 3)
	pending.State = sandbox.StateTerminating
	pending.Candidate.Status = repository.CandidateAbandoned
	api := &sandboxAPIFake{abandonError: &sandbox.LifecycleControlError{
		Session: pending,
		Cause:   errors.Join(sandbox.ErrSessionReconciliation, errors.New("runtime cleanup unavailable")),
	}}
	router := sandboxTransportRouter(t, api)
	request := httptest.NewRequest(
		http.MethodPost,
		"/sandbox-sessions/"+sandboxTransportSession+":abandon",
		bytes.NewBufferString(`{"candidateId":"`+sandboxTransportCandidate+`","reason":"discard superseded experiment"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"sandbox:`+sandboxTransportSession+`:3"`)
	request.Header.Set("X-Sandbox-Session-Epoch", "1")
	request.Header.Set("X-Candidate-Version", "2")
	request.Header.Set("X-Writer-Lease-Epoch", "1")
	request.Header.Set("Idempotency-Key", "abandon-candidate-retry")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable ||
		response.Header().Get("X-Sandbox-Session-ETag") != `"sandbox:`+sandboxTransportSession+`:4"` {
		t.Fatalf("pending abandon did not expose durable Session fence: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var details map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &details); err != nil ||
		details["code"] != "sandbox_reconciliation_pending" {
		t.Fatalf("pending abandon problem is not retryable: %#v err=%v", details, err)
	}
}

func TestSandboxTransportStartsOnlyNamedTemplateProcess(t *testing.T) {
	api := &sandboxAPIFake{}
	router := sandboxTransportRouter(t, api)
	request := httptest.NewRequest(
		http.MethodPost,
		"/sandbox-sessions/"+sandboxTransportSession+"/processes",
		bytes.NewBufferString(`{"serviceId":"web-ui","commandId":"dev"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"sandbox:`+sandboxTransportSession+`:3"`)
	request.Header.Set("X-Sandbox-Session-Epoch", "1")
	request.Header.Set("Idempotency-Key", "start-template-process")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("POST process status=%d body=%s", response.Code, response.Body.String())
	}
	if api.startProcess.ProjectID != sandboxTransportProject || api.startProcess.SessionID != sandboxTransportSession ||
		api.startProcess.ActorID != sandboxTransportActor || api.startProcess.ExpectedSessionVersion != 3 ||
		api.startProcess.ExpectedSessionEpoch != 1 || api.startProcess.ServiceID != "web-ui" ||
		api.startProcess.CommandID != "dev" {
		t.Fatalf("process start input = %#v", api.startProcess)
	}
	if response.Header().Get("ETag") != `"sandbox-process:`+sandboxTransportProcess+`:1"` ||
		response.Header().Get("X-Sandbox-Session-ETag") != `"sandbox:`+sandboxTransportSession+`:3"` ||
		response.Header().Get("Location") != "/v1/sandbox-sessions/"+sandboxTransportSession+"/processes/"+sandboxTransportProcess {
		t.Fatalf("process response fences = %#v", response.Header())
	}
}

func TestSandboxTransportSignalsProcessWithBothEntityFences(t *testing.T) {
	api := &sandboxAPIFake{}
	router := sandboxTransportRouter(t, api)
	request := httptest.NewRequest(
		http.MethodPost,
		"/sandbox-sessions/"+sandboxTransportSession+"/processes/"+sandboxTransportProcess+":signal",
		bytes.NewBufferString(`{"signal":"TERM"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"sandbox-process:`+sandboxTransportProcess+`:2"`)
	request.Header.Set("X-Sandbox-Session-ETag", `"sandbox:`+sandboxTransportSession+`:3"`)
	request.Header.Set("X-Sandbox-Session-Epoch", "1")
	request.Header.Set("Idempotency-Key", "signal-template-process")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("POST signal status=%d body=%s", response.Code, response.Body.String())
	}
	if api.signalProcess.ProjectID != sandboxTransportProject || api.signalProcess.SessionID != sandboxTransportSession ||
		api.signalProcess.ProcessID != sandboxTransportProcess || api.signalProcess.ActorID != sandboxTransportActor ||
		api.signalProcess.ExpectedSessionVersion != 3 || api.signalProcess.ExpectedSessionEpoch != 1 ||
		api.signalProcess.ExpectedProcessVersion != 2 || api.signalProcess.Signal != "TERM" {
		t.Fatalf("process signal input = %#v", api.signalProcess)
	}
}

func TestSandboxTransportOpensOnlyFixedTerminalShape(t *testing.T) {
	api := &sandboxAPIFake{}
	router := sandboxTransportRouter(t, api)
	request := httptest.NewRequest(
		http.MethodPost,
		"/sandbox-sessions/"+sandboxTransportSession+"/ptys",
		bytes.NewBufferString(`{"workingDirectory":".","rows":24,"columns":80}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"sandbox:`+sandboxTransportSession+`:3"`)
	request.Header.Set("X-Sandbox-Session-Epoch", "1")
	request.Header.Set("Idempotency-Key", "open-terminal")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("POST terminal status=%d body=%s", response.Code, response.Body.String())
	}
	if api.createTerminal.ProjectID != sandboxTransportProject || api.createTerminal.SessionID != sandboxTransportSession ||
		api.createTerminal.ActorID != sandboxTransportActor || api.createTerminal.ExpectedSessionVersion != 3 ||
		api.createTerminal.ExpectedSessionEpoch != 1 || api.createTerminal.WorkingDirectory != "." ||
		api.createTerminal.Rows != 24 || api.createTerminal.Columns != 80 {
		t.Fatalf("terminal create input = %#v", api.createTerminal)
	}
	if response.Header().Get("ETag") != `"sandbox-terminal:`+sandboxTransportTerminal+`:2"` ||
		response.Header().Get("X-Sandbox-Session-ETag") != `"sandbox:`+sandboxTransportSession+`:3"` ||
		response.Header().Get("Location") != "/v1/sandbox-sessions/"+sandboxTransportSession+"/ptys/"+sandboxTransportTerminal {
		t.Fatalf("terminal response fences = %#v", response.Header())
	}
}

func TestSandboxTransportListsPortsAndIssuesExactPreviewLink(t *testing.T) {
	api := &sandboxAPIFake{}
	router := sandboxTransportRouter(t, api)

	listRequest := httptest.NewRequest(http.MethodGet, "/sandbox-sessions/"+sandboxTransportSession+"/ports", nil)
	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), `"state":"listening"`) {
		t.Fatalf("GET ports status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}

	issueRequest := httptest.NewRequest(
		http.MethodPost,
		"/sandbox-sessions/"+sandboxTransportSession+"/ports/web-http/preview-links",
		bytes.NewBufferString(`{}`),
	)
	issueRequest.Header.Set("Content-Type", "application/json")
	issueRequest.Header.Set("If-Match", `"sandbox:`+sandboxTransportSession+`:3"`)
	issueRequest.Header.Set("X-Sandbox-Session-Epoch", "1")
	issueRequest.Header.Set("Idempotency-Key", "issue-preview-link")
	issueResponse := httptest.NewRecorder()
	router.ServeHTTP(issueResponse, issueRequest)
	if issueResponse.Code != http.StatusCreated ||
		issueResponse.Header().Get("Location") != "https://"+strings.Repeat("a", 48)+".preview.example/" {
		t.Fatalf("POST preview status=%d headers=%v body=%s", issueResponse.Code, issueResponse.Header(), issueResponse.Body.String())
	}
	if api.previewInput.ProjectID != sandboxTransportProject || api.previewInput.SessionID != sandboxTransportSession ||
		api.previewInput.PortName != "web-http" || api.previewInput.ActorID != sandboxTransportActor ||
		api.previewInput.ExpectedSessionVersion != 3 || api.previewInput.ExpectedSessionEpoch != 1 {
		t.Fatalf("preview input = %#v", api.previewInput)
	}
}

func sandboxTransportRouter(t *testing.T, api SandboxAPI) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("platform_identity", worksmiddleware.Identity{
			Session: auth.Session{User: auth.User{ID: sandboxTransportActor}}, Transport: "bearer",
		})
		c.Next()
	})
	handler, err := NewSandboxHandler(SandboxDependencies{Service: api})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterSandboxRoutes(router, handler, worksmiddleware.CaptureIdempotencyKey(true)); err != nil {
		t.Fatal(err)
	}
	return router
}

func sandboxTransportView(version, candidateVersion uint64) sandbox.SessionView {
	return sandbox.SessionView{
		ID: sandboxTransportSession, ProjectID: sandboxTransportProject,
		Version: version, SessionEpoch: 1,
		Candidate: sandbox.CandidateState{
			ID: sandboxTransportCandidate, Version: candidateVersion,
			WriterLeaseEpoch: 1, TreeHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
}

func sandboxTransportProcessView(version uint64, state sandbox.ProcessState) sandbox.ProcessView {
	return sandbox.ProcessView{
		SchemaVersion: sandbox.RuntimeProcessSchemaVersion,
		ID:            sandboxTransportProcess, ProjectID: sandboxTransportProject, SessionID: sandboxTransportSession,
		SessionEpoch: 1, SessionVersionAtCreation: 3, ActorID: sandboxTransportActor,
		ServiceID: "web-ui", CommandID: "dev", State: state, Version: version,
	}
}

func sandboxTransportTerminalView(version uint64, state sandbox.TerminalState) sandbox.TerminalView {
	return sandbox.TerminalView{
		SchemaVersion: sandbox.TerminalSchemaVersion,
		ID:            sandboxTransportTerminal, ProjectID: sandboxTransportProject, SessionID: sandboxTransportSession,
		SessionEpoch: 1, SessionVersionAtCreation: 3, ActorID: sandboxTransportActor,
		WorkingDirectory: ".", ShellPath: "/bin/bash", Rows: 24, Columns: 80,
		OutputLimitBytes: 1 << 20, State: state, Version: version,
	}
}
