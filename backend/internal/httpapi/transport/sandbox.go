package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/core"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/sandbox"
)

const defaultSandboxMaxJSONBodyBytes int64 = 1 << 20

type SandboxAPI interface {
	CreateSession(context.Context, sandbox.CreateSessionInput) (sandbox.SessionView, error)
	SuspendSession(context.Context, sandbox.SessionControlInput) (sandbox.SessionView, error)
	ResumeSession(context.Context, sandbox.SessionControlInput) (sandbox.SessionView, error)
	TerminateSession(context.Context, sandbox.TerminateSessionInput) (sandbox.SessionView, error)
	StartProcess(context.Context, sandbox.StartProcessInput) (sandbox.ProcessResult, error)
	GetProcess(context.Context, string, string, string, string) (sandbox.ProcessResult, error)
	ListProcesses(context.Context, string, string, string, int) (sandbox.ProcessList, error)
	SignalProcess(context.Context, sandbox.SignalProcessInput) (sandbox.ProcessResult, error)
	ProcessLogs(context.Context, string, string, string, string, int64, int64) (sandbox.ProcessLogResult, error)
	CreateTerminal(context.Context, sandbox.CreateTerminalInput) (sandbox.TerminalResult, error)
	GetTerminal(context.Context, string, string, string, string) (sandbox.TerminalResult, error)
	ListTerminals(context.Context, string, string, string, int) (sandbox.TerminalList, error)
	ListPorts(context.Context, string, string, string) (sandbox.PortList, error)
	CreatePreviewLink(context.Context, sandbox.IssuePreviewInput) (sandbox.PreviewLink, error)
	CreateConnectionTicket(context.Context, sandbox.IssueConnectionTicketInput) (sandbox.ConnectionTicketView, error)
	ResolveProject(context.Context, string, string) (string, error)
	Get(context.Context, string, string, string) (sandbox.SessionView, error)
	Tree(context.Context, string, string, string) (sandbox.RepositoryView, error)
	ReadFile(context.Context, sandbox.ReadFileInput) (sandbox.FileView, error)
	AcquireWriterLease(context.Context, sandbox.AcquireWriterLeaseInput) (sandbox.CandidateSessionResult, error)
	MutateFile(context.Context, sandbox.FileMutationInput) (sandbox.FileMutationResult, error)
	Checkpoint(context.Context, sandbox.CheckpointInput) (sandbox.CheckpointResult, error)
	FreezeCandidate(context.Context, sandbox.CandidateFreezeInput) (sandbox.CandidateFreezeResult, error)
	AbandonCandidate(context.Context, sandbox.CandidateAbandonInput) (sandbox.CandidateSessionResult, error)
}

type SandboxDependencies struct {
	Service          SandboxAPI
	MaxJSONBodyBytes int64
}

type SandboxHandler struct {
	service SandboxAPI
	maxJSON int64
}

func NewSandboxHandler(dependencies SandboxDependencies) (*SandboxHandler, error) {
	if dependencies.Service == nil {
		return nil, errors.New("sandbox façade is required")
	}
	maxJSON := dependencies.MaxJSONBodyBytes
	if maxJSON <= 0 {
		maxJSON = defaultSandboxMaxJSONBodyBytes
	}
	return &SandboxHandler{service: dependencies.Service, maxJSON: maxJSON}, nil
}

func RegisterSandboxRoutes(
	routes gin.IRoutes,
	handler *SandboxHandler,
	mutationMiddleware ...gin.HandlerFunc,
) error {
	if routes == nil || handler == nil {
		return errors.New("sandbox routes and handler are required")
	}
	unfencedMutation := func(target gin.HandlerFunc) []gin.HandlerFunc {
		handlers := []gin.HandlerFunc{sandboxNoStore}
		handlers = append(handlers, mutationMiddleware...)
		return append(handlers, target)
	}
	routes.POST("/projects/:projectId/sandbox-sessions", unfencedMutation(handler.createSession)...)
	routes.GET("/sandbox-sessions/:sessionId", sandboxNoStore, handler.get)
	routes.GET("/sandbox-sessions/:sessionId/tree", sandboxNoStore, handler.tree)
	routes.GET("/sandbox-sessions/:sessionId/files/*filePath", sandboxNoStore, handler.readFile)
	routes.POST("/sandbox-sessions/:sessionId/connection-tickets", unfencedMutation(handler.createConnectionTicket)...)
	routes.GET("/sandbox-sessions/:sessionId/processes", sandboxNoStore, handler.listProcesses)
	routes.GET("/sandbox-sessions/:sessionId/processes/:processId", sandboxNoStore, handler.getProcess)
	routes.GET("/sandbox-sessions/:sessionId/processes/:processId/logs", sandboxNoStore, handler.processLogs)
	routes.GET("/sandbox-sessions/:sessionId/ptys", sandboxNoStore, handler.listTerminals)
	routes.GET("/sandbox-sessions/:sessionId/ptys/:terminalId", sandboxNoStore, handler.getTerminal)
	routes.GET("/sandbox-sessions/:sessionId/ports", sandboxNoStore, handler.listPorts)

	mutation := func(target gin.HandlerFunc) []gin.HandlerFunc {
		handlers := []gin.HandlerFunc{sandboxNoStore, worksmiddleware.RequireIfMatch()}
		handlers = append(handlers, mutationMiddleware...)
		return append(handlers, target)
	}
	routes.POST("/sandbox-sessions/:sessionId/writer-lease", mutation(handler.acquireWriterLease)...)
	routes.PUT("/sandbox-sessions/:sessionId/files/*filePath", mutation(handler.putFile)...)
	routes.POST("/sandbox-sessions/:sessionId/file-operations", mutation(handler.fileOperation)...)
	routes.POST("/sandbox-sessions/:sessionId/checkpoints", mutation(handler.checkpoint)...)
	routes.POST("/sandbox-sessions/:sessionId/processes", mutation(handler.startProcess)...)
	routes.POST("/sandbox-sessions/:sessionId/ptys", mutation(handler.createTerminal)...)
	routes.POST("/sandbox-sessions/:sessionId/ports/:portName/preview-links", mutation(handler.createPreviewLink)...)
	// A terminal process parameter preserves the RFC's `{processId}:signal`
	// form without accepting arbitrary action suffixes.
	routes.POST("/sandbox-sessions/:sessionId/processes/:processId", mutation(handler.processControl)...)
	// Gin parameters consume a whole segment, so the RFC's Google-style
	// `{sessionId}:action` endpoints share this terminal route and are split by
	// the handler. Child routes above remain unambiguous.
	routes.POST("/sandbox-sessions/:sessionId", mutation(handler.control)...)
	return nil
}

type createConnectionTicketRequest struct {
	Channels []sandbox.StreamChannel    `json:"channels"`
	Cursors  []sandbox.ConnectionCursor `json:"cursors"`
}

func (handler *SandboxHandler) createConnectionTicket(c *gin.Context) {
	actor, projectID, ok := handler.identity(c)
	if !ok {
		return
	}
	var request createConnectionTicketRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	view, err := handler.service.CreateConnectionTicket(c.Request.Context(), sandbox.IssueConnectionTicketInput{
		ProjectID: projectID, SessionID: c.Param("sessionId"), ActorID: actor,
		Origin: c.GetHeader("Origin"), Channels: request.Channels, Cursors: request.Cursors,
	})
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	c.Header("Location", "/v1/sandbox-connection-tickets/"+view.ID)
	c.JSON(http.StatusCreated, view)
}

type createSandboxSessionRequest struct {
	CandidateID string `json:"candidateId"`
}

func (handler *SandboxHandler) createSession(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	var request createSandboxSessionRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	view, err := handler.service.CreateSession(c.Request.Context(), sandbox.CreateSessionInput{
		ProjectID: c.Param("projectId"), CandidateID: request.CandidateID, ActorID: actor,
	})
	if err != nil {
		var bootstrapError *sandbox.RuntimeBootstrapError
		if !errors.As(err, &bootstrapError) || bootstrapError.Session.ID == "" {
			writeSandboxProblem(c, err)
			return
		}
		view = bootstrapError.Session
	}
	writeSandboxHeaders(c, view)
	c.Header("Location", "/v1/sandbox-sessions/"+view.ID)
	c.JSON(http.StatusCreated, view)
}

func sandboxNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func (handler *SandboxHandler) get(c *gin.Context) {
	actor, projectID, ok := handler.identity(c)
	if !ok {
		return
	}
	view, err := handler.service.Get(c.Request.Context(), projectID, c.Param("sessionId"), actor)
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxHeaders(c, view)
	c.JSON(http.StatusOK, view)
}

func (handler *SandboxHandler) tree(c *gin.Context) {
	actor, projectID, ok := handler.identity(c)
	if !ok {
		return
	}
	view, err := handler.service.Tree(c.Request.Context(), projectID, c.Param("sessionId"), actor)
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxHeaders(c, view.Session)
	c.Header("X-Candidate-Tree-ETag", entityHashETag("candidate-tree", view.Candidate.ID, view.Tree.TreeHash))
	c.JSON(http.StatusOK, view)
}

func (handler *SandboxHandler) readFile(c *gin.Context) {
	actor, projectID, ok := handler.identity(c)
	if !ok {
		return
	}
	filePath, ok := sandboxFilePath(c)
	if !ok {
		return
	}
	epoch, ok := requiredUintHeader(c, "X-Sandbox-Session-Epoch", true)
	if !ok {
		return
	}
	candidateID, ok := requiredSandboxFenceHeader(c, "X-Expected-Candidate-ID")
	if !ok {
		return
	}
	candidateVersion, ok := requiredUintHeader(c, "X-Candidate-Version", true)
	if !ok {
		return
	}
	journalSequence, ok := requiredUintHeader(c, "X-Candidate-Journal-Sequence", false)
	if !ok {
		return
	}
	writerLeaseEpoch, ok := requiredUintHeader(c, "X-Writer-Lease-Epoch", false)
	if !ok {
		return
	}
	treeHash, ok := requiredSandboxFenceHeader(c, "X-Candidate-Tree-Hash")
	if !ok {
		return
	}
	fileHash, ok := expectedFileHash(c)
	if !ok {
		return
	}
	if fileHash == "" {
		problem.Write(c, problem.New(http.StatusUnprocessableEntity, "invalid_expected_file_hash", "Invalid file precondition", "X-Expected-File-Hash must contain the exact file sha256 digest for a read."))
		return
	}
	view, err := handler.service.ReadFile(c.Request.Context(), sandbox.ReadFileInput{
		ProjectID: projectID, SessionID: c.Param("sessionId"), ActorID: actor, Path: filePath,
		ExpectedSessionEpoch: epoch, ExpectedCandidateID: candidateID,
		ExpectedCandidateVersion: candidateVersion, ExpectedJournalSequence: journalSequence,
		ExpectedWriterLeaseEpoch: writerLeaseEpoch, ExpectedTreeHash: treeHash,
		ExpectedFileHash: fileHash,
	})
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxHeaders(c, view.Session)
	c.Header("ETag", entityHashETag("candidate-file", view.File.Path, view.File.ContentHash))
	c.Header("X-Content-Hash", view.File.ContentHash)
	c.Header("X-File-Mode", view.File.Mode)
	c.Data(http.StatusOK, view.ContentType, view.Value)
}

type acquireWriterLeaseRequest struct {
	TTLSeconds int `json:"ttlSeconds"`
}

func (handler *SandboxHandler) acquireWriterLease(c *gin.Context) {
	actor, projectID, fences, ok := handler.mutationIdentity(c)
	if !ok {
		return
	}
	var request acquireWriterLeaseRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	result, err := handler.service.AcquireWriterLease(c.Request.Context(), sandbox.AcquireWriterLeaseInput{
		ProjectID: projectID, SessionID: c.Param("sessionId"), ActorID: actor,
		ExpectedSessionVersion: fences.sessionVersion, ExpectedSessionEpoch: fences.sessionEpoch,
		ExpectedCandidateVersion: fences.candidateVersion,
		TTL:                      time.Duration(request.TTLSeconds) * time.Second,
	})
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxHeaders(c, result.Session)
	c.JSON(http.StatusOK, result)
}

func (handler *SandboxHandler) putFile(c *gin.Context) {
	actor, projectID, fences, ok := handler.mutationIdentity(c)
	if !ok {
		return
	}
	filePath, ok := sandboxFilePath(c)
	if !ok {
		return
	}
	expectedHash, ok := expectedFileHash(c)
	if !ok {
		return
	}
	value, err := readSandboxFileBody(c)
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	result, err := handler.service.MutateFile(c.Request.Context(), sandbox.FileMutationInput{
		ProjectID: projectID, SessionID: c.Param("sessionId"), ActorID: actor,
		ExpectedSessionVersion: fences.sessionVersion, ExpectedSessionEpoch: fences.sessionEpoch,
		ExpectedCandidateVersion: fences.candidateVersion,
		ExpectedWriterLeaseEpoch: fences.writerLeaseEpoch,
		OperationID:              worksmiddleware.IdempotencyKey(c), Kind: repository.OperationUpsert,
		Path: filePath, ExpectedFileHash: expectedHash, Mode: strings.TrimSpace(c.GetHeader("X-File-Mode")),
		Value: value,
	})
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxHeaders(c, result.Session)
	c.JSON(http.StatusOK, result)
}

type fileOperationRequest struct {
	Kind             repository.OperationKind `json:"kind"`
	Path             string                   `json:"path"`
	FromPath         string                   `json:"fromPath,omitempty"`
	ExpectedFileHash string                   `json:"expectedFileHash"`
}

func (handler *SandboxHandler) fileOperation(c *gin.Context) {
	actor, projectID, fences, ok := handler.mutationIdentity(c)
	if !ok {
		return
	}
	var request fileOperationRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	if request.Kind != repository.OperationDelete && request.Kind != repository.OperationRename {
		problem.Write(c, problem.New(http.StatusUnprocessableEntity, "invalid_file_operation", "Invalid file operation", "This endpoint accepts only delete or rename operations."))
		return
	}
	result, err := handler.service.MutateFile(c.Request.Context(), sandbox.FileMutationInput{
		ProjectID: projectID, SessionID: c.Param("sessionId"), ActorID: actor,
		ExpectedSessionVersion: fences.sessionVersion, ExpectedSessionEpoch: fences.sessionEpoch,
		ExpectedCandidateVersion: fences.candidateVersion,
		ExpectedWriterLeaseEpoch: fences.writerLeaseEpoch,
		OperationID:              worksmiddleware.IdempotencyKey(c), Kind: request.Kind,
		Path: request.Path, FromPath: request.FromPath, ExpectedFileHash: request.ExpectedFileHash,
	})
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxHeaders(c, result.Session)
	c.JSON(http.StatusOK, result)
}

type checkpointRequest struct {
	CheckpointID string `json:"checkpointId"`
	Reason       string `json:"reason"`
}

func (handler *SandboxHandler) checkpoint(c *gin.Context) {
	actor, projectID, fences, ok := handler.mutationIdentity(c)
	if !ok {
		return
	}
	var request checkpointRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	result, err := handler.service.Checkpoint(c.Request.Context(), sandbox.CheckpointInput{
		ProjectID: projectID, SessionID: c.Param("sessionId"), ActorID: actor,
		CheckpointID: request.CheckpointID, Reason: request.Reason,
		ExpectedSessionVersion: fences.sessionVersion, ExpectedSessionEpoch: fences.sessionEpoch,
		ExpectedCandidateVersion: fences.candidateVersion,
		ExpectedWriterLeaseEpoch: fences.writerLeaseEpoch,
	})
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxHeaders(c, result.Session)
	c.Header("Location", "/v1/sandbox-sessions/"+c.Param("sessionId")+"/checkpoints/"+result.Checkpoint.ID)
	c.JSON(http.StatusCreated, result)
}

type terminateSandboxSessionRequest struct {
	Reason string `json:"reason"`
}

type freezeSandboxCandidateRequest struct {
	CheckpointID            string `json:"checkpointId"`
	VerificationReceiptID   string `json:"verificationReceiptId"`
	VerificationReceiptHash string `json:"verificationReceiptHash"`
	Reason                  string `json:"reason"`
}

type abandonSandboxCandidateRequest struct {
	CandidateID  string `json:"candidateId"`
	CheckpointID string `json:"checkpointId,omitempty"`
	Reason       string `json:"reason"`
}

type createSandboxTerminalRequest struct {
	WorkingDirectory string `json:"workingDirectory"`
	Rows             uint16 `json:"rows"`
	Columns          uint16 `json:"columns"`
}

func (handler *SandboxHandler) createTerminal(c *gin.Context) {
	actor, projectID, fences, ok := handler.controlIdentity(c, c.Param("sessionId"))
	if !ok {
		return
	}
	var request createSandboxTerminalRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	result, err := handler.service.CreateTerminal(c.Request.Context(), sandbox.CreateTerminalInput{
		ProjectID: projectID, SessionID: c.Param("sessionId"), ActorID: actor,
		ExpectedSessionVersion: fences.sessionVersion, ExpectedSessionEpoch: fences.sessionEpoch,
		WorkingDirectory: request.WorkingDirectory, Rows: request.Rows, Columns: request.Columns,
	})
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxTerminalHeaders(c, result.Session, result.Terminal)
	c.Header("Location", "/v1/sandbox-sessions/"+result.Session.ID+"/ptys/"+result.Terminal.ID)
	c.JSON(http.StatusCreated, result)
}

func (handler *SandboxHandler) listTerminals(c *gin.Context) {
	actor, projectID, ok := handler.identity(c)
	if !ok {
		return
	}
	limit := 0
	if value := strings.TrimSpace(c.Query("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			problem.Write(c, problem.New(http.StatusBadRequest, "invalid_terminal_limit", "Invalid terminal limit", "limit must be between 1 and 100."))
			return
		}
		limit = parsed
	}
	result, err := handler.service.ListTerminals(c.Request.Context(), projectID, c.Param("sessionId"), actor, limit)
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxHeaders(c, result.Session)
	c.JSON(http.StatusOK, result)
}

func (handler *SandboxHandler) getTerminal(c *gin.Context) {
	actor, projectID, ok := handler.identity(c)
	if !ok {
		return
	}
	result, err := handler.service.GetTerminal(
		c.Request.Context(), projectID, c.Param("sessionId"), c.Param("terminalId"), actor,
	)
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxTerminalHeaders(c, result.Session, result.Terminal)
	c.JSON(http.StatusOK, result)
}

func (handler *SandboxHandler) listPorts(c *gin.Context) {
	actor, projectID, ok := handler.identity(c)
	if !ok {
		return
	}
	result, err := handler.service.ListPorts(
		c.Request.Context(), projectID, c.Param("sessionId"), actor,
	)
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxHeaders(c, result.Session)
	c.JSON(http.StatusOK, result)
}

func (handler *SandboxHandler) createPreviewLink(c *gin.Context) {
	actor, projectID, fences, ok := handler.controlIdentity(c, c.Param("sessionId"))
	if !ok {
		return
	}
	result, err := handler.service.CreatePreviewLink(c.Request.Context(), sandbox.IssuePreviewInput{
		ProjectID: projectID, SessionID: c.Param("sessionId"), PortName: c.Param("portName"),
		ActorID: actor, ExpectedSessionVersion: fences.sessionVersion,
		ExpectedSessionEpoch: fences.sessionEpoch,
	})
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	c.Header("Location", result.URL)
	c.Header("X-Sandbox-Session-Epoch", strconv.FormatUint(result.SessionEpoch, 10))
	c.JSON(http.StatusCreated, result)
}

type startSandboxProcessRequest struct {
	ServiceID string `json:"serviceId"`
	CommandID string `json:"commandId"`
}

func (handler *SandboxHandler) startProcess(c *gin.Context) {
	actor, projectID, fences, ok := handler.controlIdentity(c, c.Param("sessionId"))
	if !ok {
		return
	}
	var request startSandboxProcessRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	result, err := handler.service.StartProcess(c.Request.Context(), sandbox.StartProcessInput{
		ProjectID: projectID, SessionID: c.Param("sessionId"), ActorID: actor,
		ExpectedSessionVersion: fences.sessionVersion, ExpectedSessionEpoch: fences.sessionEpoch,
		ServiceID: request.ServiceID, CommandID: request.CommandID,
	})
	if err != nil {
		var controlError *sandbox.ProcessControlError
		if errors.As(err, &controlError) && controlError.Process.ID != "" {
			writeSandboxProcessHeaders(c, controlError.Session, controlError.Process)
		}
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxProcessHeaders(c, result.Session, result.Process)
	c.Header("Location", "/v1/sandbox-sessions/"+result.Session.ID+"/processes/"+result.Process.ID)
	c.JSON(http.StatusCreated, result)
}

func (handler *SandboxHandler) listProcesses(c *gin.Context) {
	actor, projectID, ok := handler.identity(c)
	if !ok {
		return
	}
	limit := 0
	if value := strings.TrimSpace(c.Query("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			problem.Write(c, problem.New(http.StatusBadRequest, "invalid_process_limit", "Invalid process limit", "limit must be between 1 and 100."))
			return
		}
		limit = parsed
	}
	result, err := handler.service.ListProcesses(
		c.Request.Context(), projectID, c.Param("sessionId"), actor, limit,
	)
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxHeaders(c, result.Session)
	c.JSON(http.StatusOK, result)
}

func (handler *SandboxHandler) getProcess(c *gin.Context) {
	actor, projectID, ok := handler.identity(c)
	if !ok {
		return
	}
	result, err := handler.service.GetProcess(
		c.Request.Context(), projectID, c.Param("sessionId"), c.Param("processId"), actor,
	)
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxProcessHeaders(c, result.Session, result.Process)
	c.JSON(http.StatusOK, result)
}

func (handler *SandboxHandler) processLogs(c *gin.Context) {
	actor, projectID, ok := handler.identity(c)
	if !ok {
		return
	}
	offset, ok := boundedInt64Query(c, "offset", 0, 0, 1<<40)
	if !ok {
		return
	}
	limit, ok := boundedInt64Query(c, "limit", 64<<10, 1, 1<<20)
	if !ok {
		return
	}
	result, err := handler.service.ProcessLogs(
		c.Request.Context(), projectID, c.Param("sessionId"), c.Param("processId"), actor,
		offset, limit,
	)
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxProcessHeaders(c, result.Session, result.Process)
	c.JSON(http.StatusOK, result)
}

type sandboxProcessSignalRequest struct {
	Signal string `json:"signal"`
}

func (handler *SandboxHandler) processControl(c *gin.Context) {
	raw := c.Param("processId")
	separator := strings.LastIndexByte(raw, ':')
	if separator <= 0 || raw[separator+1:] != "signal" {
		problem.Write(c, problem.New(http.StatusNotFound, "sandbox_process_control_not_found", "Sandbox process control not found", "The requested process action does not exist."))
		return
	}
	processID := raw[:separator]
	actor, ok := actorID(c)
	if !ok {
		return
	}
	projectID, err := handler.service.ResolveProject(c.Request.Context(), c.Param("sessionId"), actor)
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	processVersion, ok := parseSandboxProcessETag(c, worksmiddleware.IfMatch(c), processID)
	if !ok {
		return
	}
	sessionETag := strings.TrimSpace(c.GetHeader("X-Sandbox-Session-ETag"))
	if sessionETag == "" {
		problem.Write(c, problem.New(http.StatusPreconditionRequired, "sandbox_session_etag_required", "Sandbox session precondition required", "X-Sandbox-Session-ETag must identify the current SandboxSession."))
		return
	}
	sessionVersion, ok := parseSandboxETagForSession(c, sessionETag, c.Param("sessionId"))
	if !ok {
		return
	}
	epoch, ok := requiredUintHeader(c, "X-Sandbox-Session-Epoch", true)
	if !ok {
		return
	}
	var request sandboxProcessSignalRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	result, err := handler.service.SignalProcess(c.Request.Context(), sandbox.SignalProcessInput{
		ProjectID: projectID, SessionID: c.Param("sessionId"), ProcessID: processID, ActorID: actor,
		ExpectedSessionVersion: sessionVersion, ExpectedSessionEpoch: epoch,
		ExpectedProcessVersion: processVersion, Signal: request.Signal,
	})
	if err != nil {
		var controlError *sandbox.ProcessControlError
		if errors.As(err, &controlError) && controlError.Process.ID != "" {
			writeSandboxProcessHeaders(c, controlError.Session, controlError.Process)
		}
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxProcessHeaders(c, result.Session, result.Process)
	c.JSON(http.StatusOK, result)
}

func (handler *SandboxHandler) control(c *gin.Context) {
	raw := c.Param("sessionId")
	separator := strings.LastIndexByte(raw, ':')
	if separator <= 0 || separator == len(raw)-1 {
		problem.Write(c, problem.New(http.StatusNotFound, "sandbox_control_not_found", "Sandbox control not found", "The requested SandboxSession lifecycle action does not exist."))
		return
	}
	sessionID, action := raw[:separator], raw[separator+1:]
	if action == "freeze" {
		handler.freezeCandidate(c, sessionID)
		return
	}
	if action == "abandon" {
		handler.abandonCandidate(c, sessionID)
		return
	}
	actor, projectID, fences, ok := handler.controlIdentity(c, sessionID)
	if !ok {
		return
	}
	input := sandbox.SessionControlInput{
		ProjectID: projectID, SessionID: sessionID, ActorID: actor,
		ExpectedSessionVersion: fences.sessionVersion,
		ExpectedSessionEpoch:   fences.sessionEpoch,
	}
	var view sandbox.SessionView
	var err error
	switch action {
	case "suspend":
		view, err = handler.service.SuspendSession(c.Request.Context(), input)
	case "resume":
		view, err = handler.service.ResumeSession(c.Request.Context(), input)
	case "terminate":
		var request terminateSandboxSessionRequest
		if decodeErr := DecodeJSON(c, &request, handler.maxJSON); decodeErr != nil {
			WriteJSONError(c, decodeErr)
			return
		}
		view, err = handler.service.TerminateSession(c.Request.Context(), sandbox.TerminateSessionInput{
			SessionControlInput: input,
			Reason:              request.Reason,
		})
	default:
		problem.Write(c, problem.New(http.StatusNotFound, "sandbox_control_not_found", "Sandbox control not found", "The requested SandboxSession lifecycle action does not exist."))
		return
	}
	if err != nil {
		var controlError *sandbox.LifecycleControlError
		if errors.As(err, &controlError) && controlError.Session.ID == sessionID {
			writeSandboxHeaders(c, controlError.Session)
		}
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxHeaders(c, view)
	c.JSON(http.StatusOK, view)
}

func (handler *SandboxHandler) abandonCandidate(c *gin.Context, sessionID string) {
	actor, projectID, fences, ok := handler.mutationIdentityForSession(c, sessionID)
	if !ok {
		return
	}
	var request abandonSandboxCandidateRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	result, err := handler.service.AbandonCandidate(c.Request.Context(), sandbox.CandidateAbandonInput{
		ProjectID: projectID, SessionID: sessionID, CandidateID: request.CandidateID,
		ActorID: actor, CheckpointID: request.CheckpointID, Reason: request.Reason,
		ExpectedSessionVersion: fences.sessionVersion, ExpectedSessionEpoch: fences.sessionEpoch,
		ExpectedCandidateVersion: fences.candidateVersion,
		ExpectedWriterLeaseEpoch: fences.writerLeaseEpoch,
	})
	if err != nil {
		var controlError *sandbox.LifecycleControlError
		if errors.As(err, &controlError) && controlError.Session.ID == sessionID {
			writeSandboxHeaders(c, controlError.Session)
		}
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxHeaders(c, result.Session)
	c.JSON(http.StatusOK, result)
}

func (handler *SandboxHandler) freezeCandidate(c *gin.Context, sessionID string) {
	actor, projectID, fences, ok := handler.mutationIdentityForSession(c, sessionID)
	if !ok {
		return
	}
	var request freezeSandboxCandidateRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	result, err := handler.service.FreezeCandidate(c.Request.Context(), sandbox.CandidateFreezeInput{
		ProjectID: projectID, SessionID: sessionID, ActorID: actor,
		RequestKey: worksmiddleware.IdempotencyKey(c), CheckpointID: request.CheckpointID,
		VerificationReceipt: repository.ExactReference{ID: request.VerificationReceiptID, ContentHash: request.VerificationReceiptHash},
		Reason:              request.Reason, ExpectedSessionVersion: fences.sessionVersion,
		ExpectedSessionEpoch: fences.sessionEpoch, ExpectedCandidateVersion: fences.candidateVersion,
		ExpectedWriterLeaseEpoch: fences.writerLeaseEpoch,
	})
	if err != nil {
		writeSandboxProblem(c, err)
		return
	}
	writeSandboxHeaders(c, result.Session)
	c.Header("Location", "/v1/implementation-proposals/"+result.Proposal.ID)
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
		c.Header("X-Idempotent-Replay", "true")
	}
	c.JSON(status, result)
}

func (handler *SandboxHandler) identity(c *gin.Context) (string, string, bool) {
	actor, ok := actorID(c)
	if !ok {
		return "", "", false
	}
	projectID, err := handler.service.ResolveProject(c.Request.Context(), c.Param("sessionId"), actor)
	if err != nil {
		writeSandboxProblem(c, err)
		return "", "", false
	}
	return actor, projectID, true
}

type sandboxFences struct {
	sessionVersion   uint64
	sessionEpoch     uint64
	candidateVersion uint64
	writerLeaseEpoch uint64
}

func (handler *SandboxHandler) mutationIdentity(c *gin.Context) (string, string, sandboxFences, bool) {
	return handler.mutationIdentityForSession(c, c.Param("sessionId"))
}

func (handler *SandboxHandler) mutationIdentityForSession(
	c *gin.Context,
	sessionID string,
) (string, string, sandboxFences, bool) {
	actor, ok := actorID(c)
	if !ok {
		return "", "", sandboxFences{}, false
	}
	projectID, err := handler.service.ResolveProject(c.Request.Context(), sessionID, actor)
	if err != nil {
		writeSandboxProblem(c, err)
		return "", "", sandboxFences{}, false
	}
	version, ok := parseSandboxETagForSession(c, worksmiddleware.IfMatch(c), sessionID)
	if !ok {
		return "", "", sandboxFences{}, false
	}
	epoch, ok := requiredUintHeader(c, "X-Sandbox-Session-Epoch", true)
	if !ok {
		return "", "", sandboxFences{}, false
	}
	candidateVersion, ok := requiredUintHeader(c, "X-Candidate-Version", true)
	if !ok {
		return "", "", sandboxFences{}, false
	}
	writerEpoch, ok := requiredUintHeader(c, "X-Writer-Lease-Epoch", false)
	if !ok {
		return "", "", sandboxFences{}, false
	}
	return actor, projectID, sandboxFences{
		sessionVersion: version, sessionEpoch: epoch,
		candidateVersion: candidateVersion, writerLeaseEpoch: writerEpoch,
	}, true
}

func (handler *SandboxHandler) controlIdentity(c *gin.Context, sessionID string) (string, string, sandboxFences, bool) {
	actor, ok := actorID(c)
	if !ok {
		return "", "", sandboxFences{}, false
	}
	projectID, err := handler.service.ResolveProject(c.Request.Context(), sessionID, actor)
	if err != nil {
		writeSandboxProblem(c, err)
		return "", "", sandboxFences{}, false
	}
	version, ok := parseSandboxETagForSession(c, worksmiddleware.IfMatch(c), sessionID)
	if !ok {
		return "", "", sandboxFences{}, false
	}
	epoch, ok := requiredUintHeader(c, "X-Sandbox-Session-Epoch", true)
	if !ok {
		return "", "", sandboxFences{}, false
	}
	return actor, projectID, sandboxFences{sessionVersion: version, sessionEpoch: epoch}, true
}

func parseSandboxETag(c *gin.Context, value string) (uint64, bool) {
	return parseSandboxETagForSession(c, value, c.Param("sessionId"))
}

func parseSandboxETagForSession(c *gin.Context, value, sessionID string) (uint64, bool) {
	prefix := `"sandbox:` + sessionID + `:`
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		problem.Write(c, problem.New(http.StatusBadRequest, "invalid_sandbox_etag", "Invalid sandbox ETag", "If-Match does not identify this SandboxSession."))
		return 0, false
	}
	version, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`), 10, 64)
	if err != nil || version == 0 {
		problem.Write(c, problem.New(http.StatusBadRequest, "invalid_sandbox_etag", "Invalid sandbox ETag", "If-Match contains an invalid SandboxSession version."))
		return 0, false
	}
	return version, true
}

func parseSandboxProcessETag(c *gin.Context, value, processID string) (uint64, bool) {
	prefix := `"sandbox-process:` + processID + `:`
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		problem.Write(c, problem.New(http.StatusBadRequest, "invalid_sandbox_process_etag", "Invalid sandbox process ETag", "If-Match does not identify this sandbox process."))
		return 0, false
	}
	version, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`), 10, 64)
	if err != nil || version == 0 {
		problem.Write(c, problem.New(http.StatusBadRequest, "invalid_sandbox_process_etag", "Invalid sandbox process ETag", "If-Match contains an invalid sandbox process version."))
		return 0, false
	}
	return version, true
}

func boundedInt64Query(c *gin.Context, name string, defaultValue, minimum, maximum int64) (int64, bool) {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return defaultValue, true
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum {
		problem.Write(c, problem.New(http.StatusBadRequest, "invalid_process_query", "Invalid process query", name+" is outside the supported range."))
		return 0, false
	}
	return parsed, true
}

func requiredUintHeader(c *gin.Context, name string, nonzero bool) (uint64, bool) {
	value := strings.TrimSpace(c.GetHeader(name))
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || (nonzero && parsed == 0) {
		problem.Write(c, problem.New(http.StatusBadRequest, "invalid_sandbox_fence", "Invalid sandbox fence", name+" must be a valid unsigned integer fence."))
		return 0, false
	}
	return parsed, true
}

func requiredSandboxFenceHeader(c *gin.Context, name string) (string, bool) {
	raw := c.GetHeader(name)
	value := strings.TrimSpace(raw)
	if value == "" {
		problem.Write(c, problem.New(http.StatusPreconditionRequired, "sandbox_read_fence_required", "Sandbox read fence required", name+" is required for an exact Candidate file read."))
		return "", false
	}
	if value != raw || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
		problem.Write(c, problem.New(http.StatusBadRequest, "invalid_sandbox_read_fence", "Invalid sandbox read fence", name+" is not canonical."))
		return "", false
	}
	return value, true
}

func sandboxFilePath(c *gin.Context) (string, bool) {
	raw := strings.ToLower(c.Request.URL.EscapedPath())
	if strings.Contains(raw, "%2f") || strings.Contains(raw, "%5c") || strings.Contains(raw, "%00") {
		problem.Write(c, problem.New(http.StatusBadRequest, "invalid_file_path", "Invalid file path", "Encoded path separators and NUL bytes are not allowed."))
		return "", false
	}
	value := strings.TrimPrefix(c.Param("filePath"), "/")
	normalized, err := repository.NormalizePath(value)
	if err != nil {
		problem.Write(c, problem.New(http.StatusUnprocessableEntity, "invalid_file_path", "Invalid file path", "The repository path is unsafe or not canonical."))
		return "", false
	}
	return normalized, true
}

func expectedFileHash(c *gin.Context) (string, bool) {
	value := strings.TrimSpace(c.GetHeader("X-Expected-File-Hash"))
	if value == "absent" {
		return "", true
	}
	if value == "" {
		problem.Write(c, problem.New(http.StatusPreconditionRequired, "expected_file_hash_required", "File precondition required", "X-Expected-File-Hash must contain the current sha256 hash or the literal absent."))
		return "", false
	}
	operation, err := repository.NormalizeOperation(repository.FileOperation{
		ID: "validate-hash", Kind: repository.OperationDelete, Path: "placeholder", ExpectedHash: value,
	})
	if err != nil {
		problem.Write(c, problem.New(http.StatusUnprocessableEntity, "invalid_expected_file_hash", "Invalid file precondition", "X-Expected-File-Hash must contain a canonical sha256 digest or absent."))
		return "", false
	}
	return operation.ExpectedHash, true
}

func readSandboxFileBody(c *gin.Context) ([]byte, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, repository.MaxFileBytes)
	value, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			return nil, fmt.Errorf("%w: file body exceeds %d bytes", repository.ErrTreeLimit, repository.MaxFileBytes)
		}
		return nil, fmt.Errorf("read Candidate file body: %w", err)
	}
	// Preserve a non-nil empty slice: nil means an upsert body was not supplied,
	// while an explicit zero-byte file is valid repository content.
	result := make([]byte, len(value))
	copy(result, value)
	return result, nil
}

func writeSandboxHeaders(c *gin.Context, view sandbox.SessionView) {
	sessionETag := entityVersionETag("sandbox", view.ID, view.Version)
	c.Header("ETag", sessionETag)
	c.Header("X-Sandbox-Session-ETag", sessionETag)
	c.Header("X-Sandbox-Session-Epoch", strconv.FormatUint(view.SessionEpoch, 10))
	c.Header("X-Candidate-Version", strconv.FormatUint(view.Candidate.Version, 10))
	c.Header("X-Candidate-ID", view.Candidate.ID)
	c.Header("X-Candidate-Journal-Sequence", strconv.FormatUint(view.Candidate.JournalSequence, 10))
	c.Header("X-Writer-Lease-Epoch", strconv.FormatUint(view.Candidate.WriterLeaseEpoch, 10))
	c.Header("X-Candidate-Tree-Hash", view.Candidate.TreeHash)
}

func writeSandboxProcessHeaders(c *gin.Context, session sandbox.SessionView, process sandbox.ProcessView) {
	writeSandboxHeaders(c, session)
	processETag := entityVersionETag("sandbox-process", process.ID, process.Version)
	c.Header("ETag", processETag)
	c.Header("X-Sandbox-Process-ETag", processETag)
}

func writeSandboxTerminalHeaders(c *gin.Context, session sandbox.SessionView, terminal sandbox.TerminalView) {
	writeSandboxHeaders(c, session)
	terminalETag := entityVersionETag("sandbox-terminal", terminal.ID, terminal.Version)
	c.Header("ETag", terminalETag)
	c.Header("X-Sandbox-Terminal-ETag", terminalETag)
}

func writeSandboxProblem(c *gin.Context, err error) {
	var actionError *sandbox.ActionError
	switch {
	case errors.Is(err, sandbox.ErrConnectionTicketUnavailable):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "sandbox_connection_ticket_unavailable", "Sandbox connection ticket is unavailable", "A short-lived sandbox stream ticket could not be created. Retry without reusing a prior ticket."))
	case errors.Is(err, sandbox.ErrConnectionTicketInvalid), errors.Is(err, sandbox.ErrConnectionTicketConsumed):
		problem.Write(c, problem.New(http.StatusUnprocessableEntity, "invalid_sandbox_connection_ticket", "Sandbox connection ticket is invalid", "The requested Origin, channel scope, cursor, or ticket lifetime is invalid."))
	case errors.Is(err, sandbox.ErrProvisioningUnavailable):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "sandbox_provisioning_unavailable", "Sandbox provisioning is unavailable", "Interactive sandbox provisioning is disabled or not configured on this deployment."))
	case errors.Is(err, sandbox.ErrControlUnavailable):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "sandbox_control_unavailable", "Sandbox control is unavailable", "Interactive sandbox lifecycle control is disabled or not configured on this deployment."))
	case errors.Is(err, sandbox.ErrFreezeUnavailable):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "sandbox_freeze_unavailable", "Candidate freeze is unavailable", "Candidate freeze-to-Proposal is disabled or not configured on this deployment."))
	case errors.Is(err, sandbox.ErrProcessUnavailable):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "sandbox_process_unavailable", "Sandbox process control is unavailable", "Interactive sandbox process control is disabled or not configured on this deployment."))
	case errors.Is(err, sandbox.ErrProcessStoreUnavailable):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "sandbox_process_persistence_unavailable", "Sandbox process state is unavailable", "The durable process projection could not be read or advanced. Retry with the same idempotency key."))
	case errors.Is(err, sandbox.ErrTerminalUnavailable):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "sandbox_terminal_unavailable", "Sandbox terminal control is unavailable", "Interactive terminal control is disabled or not configured on this deployment."))
	case errors.Is(err, sandbox.ErrTerminalStoreUnavailable):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "sandbox_terminal_persistence_unavailable", "Sandbox terminal state is unavailable", "The durable terminal projection could not be read or advanced."))
	case errors.Is(err, sandbox.ErrPortUnavailable), errors.Is(err, sandbox.ErrPreviewGrantUnavailable):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "sandbox_preview_unavailable", "Sandbox preview is unavailable", "The isolated port gateway or short-lived preview capability store is unavailable."))
	case errors.Is(err, sandbox.ErrPortNotFound), errors.Is(err, sandbox.ErrPreviewGrantExpired):
		problem.Write(c, problem.New(http.StatusNotFound, "sandbox_preview_not_found", "Sandbox preview not found", "The requested exact Session port or preview capability was not found."))
	case errors.Is(err, sandbox.ErrPortNotReady):
		problem.Write(c, problem.New(http.StatusConflict, "sandbox_port_not_ready", "Sandbox port is not ready", "Start the exact Template command and wait for the declared port to accept connections."))
	case errors.Is(err, sandbox.ErrRuntimeUnavailable), errors.Is(err, sandbox.ErrRuntimeNotReady):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "sandbox_runtime_unavailable", "Sandbox runtime is unavailable", "The isolated runtime did not complete the requested lifecycle action. The authoritative session state can be retried."))
	case errors.Is(err, sandbox.ErrSessionNotFound), errors.Is(err, sandbox.ErrProcessNotFound), errors.Is(err, sandbox.ErrTerminalNotFound), errors.Is(err, repository.ErrCandidateNotFound),
		errors.Is(err, sandbox.ErrFileNotInTree), errors.Is(err, repository.ErrFileBlobNotFound):
		problem.Write(c, problem.New(http.StatusNotFound, "sandbox_resource_not_found", "Sandbox resource not found", "The requested SandboxSession or repository resource was not found."))
	case errors.Is(err, sandbox.ErrVersionConflict), errors.Is(err, repository.ErrCandidateState),
		errors.Is(err, sandbox.ErrCandidateVersionConflict), errors.Is(err, sandbox.ErrProcessVersionConflict), errors.Is(err, sandbox.ErrTerminalVersionConflict):
		problem.Write(c, problem.New(http.StatusPreconditionFailed, "sandbox_precondition_failed", "Sandbox precondition failed", "The SandboxSession or Candidate changed since it was loaded."))
	case errors.Is(err, sandbox.ErrFileHeadChanged):
		problem.Write(c, problem.New(http.StatusConflict, "sandbox_file_head_changed", "Sandbox Candidate head changed", "Refresh the exact Sandbox/Candidate head before opening this file again."))
	case errors.Is(err, sandbox.ErrEpochFenced), errors.Is(err, repository.ErrLeaseFenced),
		errors.Is(err, repository.ErrLeaseRequired):
		problem.Write(c, problem.New(http.StatusConflict, "sandbox_writer_fenced", "Sandbox writer is fenced", "Acquire the current writer lease and retry with the latest session epoch."))
	case errors.Is(err, sandbox.ErrCheckpointRequired):
		problem.Write(c, problem.New(http.StatusConflict, "sandbox_checkpoint_required", "Exact Candidate checkpoint required", "Create and attach an exact checkpoint for the current Candidate fences before retrying this operation."))
	case errors.Is(err, sandbox.ErrCheckpointMismatch):
		problem.Write(c, problem.New(http.StatusPreconditionFailed, "sandbox_checkpoint_mismatch", "Candidate checkpoint is stale", "Refresh the SandboxSession and use only its exact current Candidate checkpoint."))
	case errors.As(err, &actionError):
		details := problem.New(http.StatusConflict, "sandbox_action_blocked", "Sandbox action is blocked", actionError.Error())
		details.Extensions = map[string]interface{}{"action": actionError.Action, "blockingReasons": actionError.Reasons}
		problem.Write(c, details)
	case errors.Is(err, sandbox.ErrRuntimeConflict):
		problem.Write(c, problem.New(http.StatusConflict, "sandbox_runtime_stale", "Sandbox runtime is stale", "Open a fresh SandboxSession before retrying this operation."))
	case errors.Is(err, sandbox.ErrSessionProjectionStale), errors.Is(err, repository.ErrCandidateTreeDrift),
		errors.Is(err, repository.ErrPathPolicyDrift), errors.Is(err, sandbox.ErrWorkspaceConflict),
		errors.Is(err, sandbox.ErrProcessStoreIntegrity), errors.Is(err, sandbox.ErrTerminalStoreIntegrity):
		problem.Write(c, problem.New(http.StatusConflict, "sandbox_projection_stale", "Sandbox projection is stale", "Refresh the SandboxSession before retrying this operation."))
	case errors.Is(err, core.ErrBlockingGate):
		problem.Write(c, problem.New(http.StatusConflict, "candidate_freeze_blocked", "Candidate freeze is blocked", err.Error()))
	case errors.Is(err, core.ErrConflict), errors.Is(err, core.ErrProposalStale):
		problem.Write(c, problem.New(http.StatusConflict, "candidate_freeze_conflict", "Candidate freeze conflicts with canonical state", "Refresh the SandboxSession and canonical Workbench lineage before retrying."))
	case errors.Is(err, core.ErrContentNotReady):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "candidate_freeze_reconciliation_pending", "Candidate freeze reconciliation is pending", "The freeze may have committed. Retry with the same Idempotency-Key and exact fences."))
	case errors.Is(err, sandbox.ErrSessionReconciliation), errors.Is(err, repository.ErrMutationReconciliation),
		errors.Is(err, sandbox.ErrWorkspaceReconciliation),
		errors.Is(err, repository.ErrAppendOutcomeUnknown), errors.Is(err, repository.ErrTreeFinalizationPending),
		errors.Is(err, repository.ErrFileBlobReconciliation), errors.Is(err, repository.ErrFileBlobFinalizationPending):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "sandbox_reconciliation_pending", "Sandbox reconciliation is pending", "The operation may have committed. Retry with the same Idempotency-Key; do not create a replacement operation."))
	case errors.Is(err, repository.ErrTreeConflict), errors.Is(err, repository.ErrFilePrecondition),
		errors.Is(err, repository.ErrOperationReplay), errors.Is(err, repository.ErrPathPolicyDenied):
		problem.Write(c, problem.New(http.StatusConflict, "candidate_file_conflict", "Candidate file conflict", "The file changed, the operation was replayed with different input, or the exact template path policy denied it."))
	case errors.Is(err, sandbox.ErrProcessInvalidTransition):
		problem.Write(c, problem.New(http.StatusConflict, "sandbox_process_transition_conflict", "Sandbox process transition conflict", "The process is no longer in a state that accepts this action."))
	case errors.Is(err, sandbox.ErrTerminalInvalidTransition):
		problem.Write(c, problem.New(http.StatusConflict, "sandbox_terminal_transition_conflict", "Sandbox terminal transition conflict", "The terminal is no longer in a state that accepts this action."))
	case errors.Is(err, sandbox.ErrInvalidSession), errors.Is(err, sandbox.ErrProcessInvalid), errors.Is(err, sandbox.ErrTerminalInvalid), errors.Is(err, sandbox.ErrPortInvalid), errors.Is(err, sandbox.ErrPreviewGrantInvalid), errors.Is(err, repository.ErrInvalidCandidate),
		errors.Is(err, repository.ErrInvalidMutation), errors.Is(err, repository.ErrInvalidTree),
		errors.Is(err, repository.ErrInvalidFilePointer), errors.Is(err, core.ErrInvalidInput):
		problem.Write(c, problem.New(http.StatusUnprocessableEntity, "invalid_sandbox_input", "Sandbox input is invalid", "One or more SandboxSession, Candidate, or repository fields are invalid."))
	default:
		problem.WriteError(c, err)
	}
}

var _ SandboxAPI = (*sandbox.PlatformService)(nil)
