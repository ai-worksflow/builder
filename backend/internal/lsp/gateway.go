package lsp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

var (
	ErrGatewayInvalid             = errors.New("invalid LSP bound Gateway configuration or authority")
	ErrGatewayUnavailable         = errors.New("LSP bound Gateway is unavailable")
	ErrGatewayStale               = errors.New("LSP bound Gateway authority is stale")
	ErrGatewayClosed              = errors.New("LSP bound Gateway connection is closed")
	ErrGatewayEditorLeaseConflict = errors.New("LSP editor connection already has an active owner")
	ErrGatewayEditorLeaseLost     = errors.New("LSP editor connection lease ownership is lost")
	ErrGatewayServerViolation     = errors.New("LSP server violated the approved runtime capability boundary")
)

const (
	gatewayDetachedAuditTimeout               = 2 * time.Second
	maximumRecoverableServerControlViolations = uint32(3)
	maximumServerControlAuditOrdinal          = uint32(64)
)

// FrameConnection is the entire browser transport authority used by the
// production Gateway core. Implementations must unblock ReadFrame and
// WriteFrame when their context is canceled or Close is called.
type FrameConnection interface {
	ReadFrame(context.Context) ([]byte, error)
	WriteFrame(context.Context, []byte) error
	Close() error
}

// GatewayRuntimeBindingResolver is implemented by RuntimeBindingSource. The
// interface keeps transport and test fixtures independent from Repository and
// Sandbox storage details while still requiring the exact production Resolve
// operation.
type GatewayRuntimeBindingResolver interface {
	Resolve(context.Context, TicketGrant, ClientBind) (RuntimeBindingProjection, error)
}

// GatewayAuthorityFence must prove the exact current Sandbox/Repository head,
// approved TemplateRelease profile, actor lease, and URI-sorted document
// projection. Gateway invokes it both before and after every browser state
// transition and every server frame admitted for browser output.
type GatewayAuthorityFence interface {
	RevalidateGatewayFence(
		context.Context,
		TicketGrant,
		SandboxHeadFence,
		ProfileIdentity,
		[]DocumentFence,
	) error
}

type Gateway struct {
	bindings  GatewayRuntimeBindingResolver
	runtime   LanguageServerRuntime
	authority GatewayAuthorityFence
	security  GatewaySecurityBoundary
	idSource  func() string
	now       func() time.Time
}

type gatewayEditorLeaseState struct {
	input    GatewayEditorLeaseInput
	contract GatewayEditorLeaseContract
	protocol *EnvelopeProtocol

	renewalMu             sync.Mutex
	lastSuccessfulRenewal time.Time
	auditMu               sync.Mutex
	lostAudited           bool
}

func NewGateway(
	bindings GatewayRuntimeBindingResolver,
	runtime LanguageServerRuntime,
	authority GatewayAuthorityFence,
	security GatewaySecurityBoundary,
) (*Gateway, error) {
	return newGateway(bindings, runtime, authority, security, uuid.NewString, time.Now)
}

func newGateway(
	bindings GatewayRuntimeBindingResolver,
	runtime LanguageServerRuntime,
	authority GatewayAuthorityFence,
	security GatewaySecurityBoundary,
	idSource func() string,
	now func() time.Time,
) (*Gateway, error) {
	if bindings == nil || runtime == nil || authority == nil || security == nil || idSource == nil || now == nil {
		return nil, ErrGatewayInvalid
	}
	return &Gateway{
		bindings: bindings, runtime: runtime, authority: authority,
		security: security, idSource: idSource, now: now,
	}, nil
}

// Serve owns one already ticketed and strictly decoded binding until either
// side closes. It never forwards a raw server DTO to the browser.
func (gateway *Gateway) Serve(
	ctx context.Context,
	connection FrameConnection,
	grant TicketGrant,
	bind ClientBind,
) (serveErr error) {
	if gateway == nil || ctx == nil || connection == nil ||
		validateRuntimeClientBind(grant, bind) != nil {
		return ErrGatewayInvalid
	}
	var closeOnce sync.Once
	closeConnection := func() { closeOnce.Do(func() { _ = connection.Close() }) }
	defer closeConnection()

	startupTimeout := time.Duration(bind.Profile.EffectiveLimits.StartupTimeoutMillis) * time.Millisecond
	if startupTimeout <= 0 {
		return ErrGatewayInvalid
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	projection, err := gateway.bindings.Resolve(startupCtx, grant, bind)
	if err != nil {
		cancelStartup()
		return classifyGatewayError(err)
	}
	repositoryPaths, err := validateGatewayProjection(projection, bind)
	if err != nil {
		cancelStartup()
		return err
	}
	if err := gateway.runtime.Readiness(startupCtx, bind.Profile); err != nil {
		cancelStartup()
		return classifyGatewayError(err)
	}
	bindingID := gateway.idSource()
	if !canonicalUUID(bindingID) || bindingID == bind.ConnectionID {
		cancelStartup()
		return ErrGatewayInvalid
	}
	var editorLease *gatewayEditorLeaseState
	if grant.Mode == TicketModeEditor {
		leaseInput := GatewayEditorLeaseInput{
			ProjectID: grant.ProjectID, SessionID: grant.SessionID,
			ProfileID: bind.Profile.ID, ProfileContentHash: bind.Profile.ContentHash,
			CapabilityHash: bind.Profile.CapabilityHash, OwnerBindingID: bindingID,
		}
		leaseCtx, cancelLease := context.WithTimeout(startupCtx, gatewayDetachedAuditTimeout)
		contract, acquired, leaseErr := gateway.security.AcquireGatewayEditorLease(leaseCtx, leaseInput)
		cancelLease()
		if leaseErr != nil {
			cancelStartup()
			auditErr := gateway.auditEditorLeaseDetached(
				ctx, grant, bind, bindingID, GatewayAuditEditorLeaseLost, "lost", "lease_store_unavailable",
			)
			return errors.Join(ErrGatewaySecurityUnavailable, leaseErr, auditErr)
		}
		if !acquired {
			cancelStartup()
			if auditErr := gateway.auditEditorLeaseDetached(
				ctx, grant, bind, bindingID, GatewayAuditEditorLeaseConflict, "conflict", "active_owner",
			); auditErr != nil {
				return auditErr
			}
			return ErrGatewayEditorLeaseConflict
		}
		editorLease = &gatewayEditorLeaseState{input: leaseInput, contract: contract}
		defer func() {
			releaseErr := gateway.releaseEditorLeaseDetached(ctx, grant, bind, editorLease)
			if releaseErr != nil {
				serveErr = errors.Join(serveErr, releaseErr)
			}
		}()
		if auditErr := gateway.auditEditorLeaseDetached(
			ctx, grant, bind, bindingID, GatewayAuditEditorLeaseAcquire, "acquired", "ok",
		); auditErr != nil {
			cancelStartup()
			return auditErr
		}
	}
	process, err := gateway.runtime.Start(startupCtx, ContainerStartInput{
		Profile: bind.Profile, WorkspaceRoot: projection.WorkspaceRoot,
		ServiceRoot: projection.ServiceRoot, ConnectionID: bind.ConnectionID, BindingID: bindingID,
	})
	if err != nil {
		cancelStartup()
		_ = terminateGatewayProcess(process, bind.Profile.EffectiveLimits)
		return classifyGatewayError(err)
	}
	if process == nil || !equalProfiles(
		[]ProfileIdentity{process.Profile()}, []ProfileIdentity{bind.Profile},
	) {
		cancelStartup()
		_ = terminateGatewayProcess(process, bind.Profile.EffectiveLimits)
		return ErrGatewayInvalid
	}

	serverControl := newServerControlState()
	initialized, err := gateway.initializeGatewayProcess(
		startupCtx, process, projection, grant, bind, bindingID, serverControl,
	)
	cancelStartup()
	if err != nil {
		_ = terminateGatewayProcess(process, bind.Profile.EffectiveLimits)
		return err
	}
	filter, err := NewServerMessageFilter(
		initialized.Methods, bind.Profile.EffectiveLimits, repositoryPaths,
	)
	if err != nil {
		_ = terminateGatewayProcess(process, bind.Profile.EffectiveLimits)
		return classifyGatewayError(err)
	}
	protocol, err := newEnvelopeProtocol(
		bind.ConnectionID, bindingID, bind.Head, bind.Profile, bind.Documents, gateway.idSource,
	)
	if err != nil {
		_ = terminateGatewayProcess(process, bind.Profile.EffectiveLimits)
		return classifyGatewayError(err)
	}
	if editorLease != nil {
		editorLease.protocol = protocol
	}

	serveCtx, cancelServe := context.WithCancel(ctx)
	session := &gatewaySession{
		gateway: gateway, connection: connection, process: process, grant: grant,
		profile: bind.Profile, protocol: protocol, filter: filter,
		initialized: initialized,
		methods:     stringSet(slices.Clone(initialized.Methods)), ctx: serveCtx, cancel: cancelServe,
		authoritative: make(map[string]RuntimeDocument, len(projection.Documents)),
		byBrowser:     make(map[string]*gatewayPending), byServer: make(map[string]*gatewayPending),
		ignoredServer: make(map[string]struct{}), fatal: make(chan error, 1),
		closeConnection: closeConnection,
		editorLease:     editorLease, heartbeatReset: make(chan struct{}, 1),
		serverControl: serverControl,
	}
	for _, document := range projection.Documents {
		copyValue := document
		copyValue.Text = slices.Clone(document.Text)
		session.authoritative[document.Fence.ModelURI] = copyValue
	}

	if err := session.auditBinding(serveCtx, GatewayAuditBindingOpen, "opened", "ok"); err != nil {
		cancelServe()
		_ = terminateGatewayProcess(process, bind.Profile.EffectiveLimits)
		return err
	}
	if err := session.sendInitialBound(serveCtx); err != nil {
		cancelServe()
		_ = terminateGatewayProcess(process, bind.Profile.EffectiveLimits)
		if auditErr := session.auditBindingDetached(
			GatewayAuditBindingClose, "closed", gatewayCloseCode(err),
		); auditErr != nil {
			return auditErr
		}
		return err
	}
	// client.bind was strictly consumed by the transport, validated again by
	// this Gateway, and fenced twice while producing server.bound. Refresh here
	// so a valid near-20s startup still has the full 30s lease before the browser
	// can send its first 10s heartbeat.
	if err := session.renewEditorLease(serveCtx); err != nil {
		cancelServe()
		_ = terminateGatewayProcess(process, bind.Profile.EffectiveLimits)
		if auditErr := session.auditBindingDetached(
			GatewayAuditBindingClose, "closed", gatewayCloseCode(err),
		); auditErr != nil {
			return auditErr
		}
		return err
	}
	return session.run()
}

func (gateway *Gateway) initializeGatewayProcess(
	ctx context.Context,
	process LanguageServerProcess,
	projection RuntimeBindingProjection,
	grant TicketGrant,
	bind ClientBind,
	bindingID string,
	controlState *serverControlState,
) (InitializedServer, error) {
	if gateway == nil || controlState == nil {
		return InitializedServer{}, ErrGatewayInvalid
	}
	request, err := BuildServerInitializeRequest(ServerInitializeInput{
		Head: projection.Head, Profile: projection.Profile, WorkspaceRootPath: projection.ServiceRoot,
	})
	if err != nil {
		return InitializedServer{}, classifyGatewayError(err)
	}
	if err := process.WriteFrame(ctx, request); err != nil {
		return InitializedServer{}, classifyGatewayError(err)
	}
	var initialized InitializedServer
	for {
		response, readErr := process.ReadFrame(ctx)
		if readErr != nil {
			return InitializedServer{}, classifyGatewayError(readErr)
		}
		control, controlErr := DecodeServerControlMessage(
			response, projection.Profile.EffectiveLimits,
		)
		switch {
		case controlErr == nil:
			terminate, handleErr := gateway.handleServerControl(
				ctx, process, grant, bind.ConnectionID, bindingID, bind.Head,
				projection.Profile, controlState, control, nil,
			)
			if handleErr != nil {
				return InitializedServer{}, handleErr
			}
			if terminate {
				return InitializedServer{}, ErrGatewayServerViolation
			}
			continue
		case !errors.Is(controlErr, ErrServerControlNotApplicable):
			_, auditErr := gateway.handleServerControl(
				ctx, process, grant, bind.ConnectionID, bindingID, bind.Head,
				projection.Profile, controlState, malformedServerControlAuditMessage(), nil,
			)
			if auditErr != nil {
				return InitializedServer{}, auditErr
			}
			return InitializedServer{}, errors.Join(ErrGatewayServerViolation, controlErr)
		}
		initialized, err = DecodeServerInitializeResponse(response, projection.Profile)
		if err != nil {
			_, auditErr := gateway.handleServerControl(
				ctx, process, grant, bind.ConnectionID, bindingID, bind.Head,
				projection.Profile, controlState, ServerControlMessage{
					Method: serverControlInitializeAuditMethod, Disposition: ServerControlTerminate,
					AuditCode: "server_initialize_rejected",
				}, nil,
			)
			if auditErr != nil {
				return InitializedServer{}, auditErr
			}
			return InitializedServer{}, errors.Join(ErrGatewayServerViolation, err)
		}
		break
	}
	// No caller-controlled fields exist in this notification.
	initializedNotification := []byte(`{"jsonrpc":"2.0","method":"initialized","params":{}}`)
	if err := process.WriteFrame(ctx, initializedNotification); err != nil {
		return InitializedServer{}, classifyGatewayError(err)
	}
	return initialized, nil
}

func validateGatewayProjection(
	projection RuntimeBindingProjection,
	bind ClientBind,
) ([]string, error) {
	if !projection.Head.Equal(bind.Head) || !equalProfiles(
		[]ProfileIdentity{projection.Profile}, []ProfileIdentity{bind.Profile},
	) || projection.WorkspaceRoot == "" || !filepath.IsAbs(projection.WorkspaceRoot) ||
		filepath.Clean(projection.WorkspaceRoot) != projection.WorkspaceRoot ||
		projection.ServiceRoot == "" || len(projection.Files) == 0 ||
		len(projection.Files) > repository.MaxTreeFiles || len(projection.Documents) != len(bind.Documents) {
		return nil, ErrGatewayStale
	}
	servicePath := projection.WorkspaceRoot
	if projection.ServiceRoot != "." {
		normalized, err := repository.NormalizePath(projection.ServiceRoot)
		if err != nil || normalized != projection.ServiceRoot {
			return nil, ErrGatewayStale
		}
		servicePath = filepath.Join(projection.WorkspaceRoot, filepath.FromSlash(projection.ServiceRoot))
	}
	if projection.ServicePath != servicePath {
		return nil, ErrGatewayStale
	}
	paths := make([]string, len(projection.Files))
	files := make(map[string]RuntimeFileFence, len(projection.Files))
	var treeBytes int64
	for index, file := range projection.Files {
		normalized, err := repository.NormalizePath(file.Path)
		if err != nil || normalized != file.Path || (file.Mode != "100644" && file.Mode != "100755") ||
			!digestPattern.MatchString(file.ContentHash) || file.ByteSize < 0 ||
			file.ByteSize > repository.MaxFileBytes || (index > 0 && projection.Files[index-1].Path >= file.Path) {
			return nil, ErrGatewayStale
		}
		treeBytes += file.ByteSize
		if treeBytes > repository.MaxTreeBytes {
			return nil, ErrGatewayStale
		}
		paths[index] = file.Path
		files[file.Path] = file
	}
	var total int64
	for index, document := range projection.Documents {
		if !document.Fence.Equal(bind.Documents[index]) || document.Fence.ModelVersion > maxLSPPositionValue ||
			document.Fence.ValidateAgainstHead(bind.Head) != nil ||
			!utf8.Valid(document.Text) || bytes.IndexByte(document.Text, 0) >= 0 ||
			int64(len(document.Text)) > bind.Profile.EffectiveLimits.MaxDocumentBytes ||
			!gatewayContentDigest(document.Text, document.Fence.SavedContentHash) {
			return nil, ErrGatewayStale
		}
		identity, err := ParseCandidateModelURI(document.Fence.ModelURI)
		if err != nil || identity.Path != document.Path ||
			!profileSupportsRepositoryPath(bind.Profile, identity.Path) {
			return nil, ErrGatewayStale
		}
		file, exists := files[document.Path]
		if !exists || document.Mode != file.Mode || document.Fence.SavedContentHash != file.ContentHash ||
			int64(len(document.Text)) != file.ByteSize {
			return nil, ErrGatewayStale
		}
		total += int64(len(document.Text))
		if total > bind.Profile.EffectiveLimits.MaxTotalSyncBytes {
			return nil, ErrGatewayStale
		}
	}
	return paths, nil
}

type gatewayPending struct {
	browserID string
	serverID  string
	method    string
	head      SandboxHeadFence
	document  DocumentFence
	startedAt time.Time
	timer     *time.Timer
}

type serverControlState struct {
	ordinal uint32
	counts  map[string]uint32
}

func newServerControlState() *serverControlState {
	return &serverControlState{counts: make(map[string]uint32)}
}

func (state *serverControlState) record(
	message ServerControlMessage,
) (GatewayServerViolationAudit, ServerControlDisposition, string, error) {
	if state == nil || !validAuditedServerControlShape(message) ||
		!auditableServerControlMethod(message.Method) || len(message.Method) > 256 ||
		state.ordinal >= maximumServerControlAuditOrdinal {
		return GatewayServerViolationAudit{}, "", "", ErrGatewayServerViolation
	}
	if state.counts == nil {
		state.counts = make(map[string]uint32)
	}
	state.ordinal++
	state.counts[message.Method]++
	disposition := message.Disposition
	code := message.AuditCode
	if disposition == ServerControlRespondContinue &&
		state.ordinal > maximumRecoverableServerControlViolations {
		disposition = ServerControlRespondTerminate
		code = "server_request_repeat_limit"
	}
	return GatewayServerViolationAudit{
		Method: message.Method, Ordinal: state.ordinal, Count: state.counts[message.Method],
	}, disposition, code, nil
}

func validAuditedServerControlShape(message ServerControlMessage) bool {
	switch message.AuditCode {
	case "server_request_rejected":
		return message.Disposition == ServerControlRespondContinue && len(message.Response) > 0
	case "server_request_forbidden":
		return message.Disposition == ServerControlRespondTerminate && len(message.Response) > 0
	case "server_notification_forbidden", "server_initialize_rejected", "server_message_malformed":
		return message.Disposition == ServerControlTerminate && len(message.Response) == 0
	default:
		return false
	}
}

func malformedServerControlAuditMessage() ServerControlMessage {
	return ServerControlMessage{
		Method: serverControlInvalidMessageAuditMethod, Disposition: ServerControlTerminate,
		AuditCode: "server_message_malformed",
	}
}

type gatewaySession struct {
	gateway     *Gateway
	connection  FrameConnection
	process     LanguageServerProcess
	grant       TicketGrant
	profile     ProfileIdentity
	protocol    *EnvelopeProtocol
	filter      *ServerMessageFilter
	methods     map[string]bool
	initialized InitializedServer

	ctx    context.Context
	cancel context.CancelFunc

	operationMu     sync.Mutex
	requestMu       sync.Mutex
	byBrowser       map[string]*gatewayPending
	byServer        map[string]*gatewayPending
	ignoredServer   map[string]struct{}
	authoritative   map[string]RuntimeDocument
	closeConnection func()
	editorLease     *gatewayEditorLeaseState
	heartbeatReset  chan struct{}
	serverControl   *serverControlState

	failOnce sync.Once
	fatal    chan error
	wait     sync.WaitGroup
}

func (session *gatewaySession) sendInitialBound(ctx context.Context) error {
	session.operationMu.Lock()
	defer session.operationMu.Unlock()
	if err := session.revalidate(ctx); err != nil {
		return err
	}
	bound, err := NewServerBound(ServerBoundExpectation{
		ConnectionID: session.protocol.connectionID, BindingID: session.protocol.bindingID,
		Head: session.protocol.head, Profile: session.profile, Initialized: session.initialized,
		Documents: session.protocol.sortedDocumentsLocked(),
	})
	if err != nil {
		return classifyGatewayError(err)
	}
	if err := session.revalidate(ctx); err != nil {
		return err
	}
	frame, err := json.Marshal(bound)
	if err != nil || int64(len(frame)) > session.profile.EffectiveLimits.MaxFrameBytes {
		return ErrGatewayInvalid
	}
	requestCtx, cancel := context.WithTimeout(
		ctx, time.Duration(session.profile.EffectiveLimits.RequestTimeoutMillis)*time.Millisecond,
	)
	defer cancel()
	if err := session.connection.WriteFrame(requestCtx, frame); err != nil {
		return classifyGatewayError(err)
	}
	return nil
}

func (session *gatewaySession) run() error {
	loops := 2
	if session.editorLease != nil {
		loops++
	}
	session.wait.Add(loops)
	go func() {
		defer session.wait.Done()
		session.fail(session.clientLoop())
	}()
	go func() {
		defer session.wait.Done()
		session.fail(session.serverLoop())
	}()
	if session.editorLease != nil {
		go func() {
			defer session.wait.Done()
			session.fail(session.heartbeatLoop())
		}()
	}

	var result error
	select {
	case result = <-session.fatal:
	case <-session.ctx.Done():
		result = session.ctx.Err()
	}
	session.cancel()
	session.operationMu.Lock()
	pendingAuditErr := session.stopAllRequests(result)
	session.operationMu.Unlock()
	session.closeConnection()
	terminateErr := terminateGatewayProcess(session.process, session.profile.EffectiveLimits)

	completed := make(chan struct{})
	go func() {
		session.wait.Wait()
		close(completed)
	}()
	shutdown := time.Duration(session.profile.EffectiveLimits.ShutdownTimeoutMillis) * time.Millisecond
	if shutdown <= 0 {
		shutdown = time.Second
	}
	select {
	case <-completed:
	case <-time.After(shutdown):
		if result == nil {
			result = ErrGatewayClosed
		}
	}
	if benignGatewayClosure(result) {
		result = nil
	}
	if terminateErr != nil && result == nil && !benignGatewayClosure(terminateErr) {
		result = classifyGatewayError(terminateErr)
	}
	closeAuditErr := session.auditBindingDetached(
		GatewayAuditBindingClose, "closed", gatewayCloseCode(errors.Join(result, pendingAuditErr)),
	)
	if pendingAuditErr != nil || closeAuditErr != nil {
		return errors.Join(pendingAuditErr, closeAuditErr)
	}
	return result
}

func (session *gatewaySession) fail(err error) {
	session.failOnce.Do(func() {
		if err == nil {
			err = io.EOF
		}
		session.fatal <- err
		session.cancel()
	})
}

func (session *gatewaySession) clientLoop() error {
	for {
		frame, err := session.connection.ReadFrame(session.ctx)
		if err != nil {
			return err
		}
		session.operationMu.Lock()
		proposedHead, proposedDocuments, atomicRebind, err :=
			session.protocol.previewClientHeadRebind(frame)
		if err == nil && atomicRebind {
			err = session.revalidateFence(session.ctx, proposedHead, proposedDocuments)
		} else if err == nil {
			err = session.revalidate(session.ctx)
		}
		if err != nil {
			session.operationMu.Unlock()
			return err
		}
		envelope, err := session.protocol.DecodeClientEnvelope(frame)
		if err == nil {
			err = session.revalidate(session.ctx)
		}
		if err == nil {
			// Only a fully decoded strict envelope with its closing authority
			// revalidation may extend editor ownership. Ping is explicit
			// heartbeat; every other legal activity has the same renewal effect.
			err = session.renewEditorLease(session.ctx)
		}
		if err == nil {
			err = session.handleClientEnvelope(session.ctx, envelope)
		}
		session.operationMu.Unlock()
		if err != nil {
			return err
		}
	}
}

func (session *gatewaySession) heartbeatLoop() error {
	if session == nil || session.editorLease == nil || session.heartbeatReset == nil {
		return ErrGatewayInvalid
	}
	for {
		remaining, err := session.editorLease.heartbeatRemaining(session.gateway.now())
		if err != nil {
			return err
		}
		if remaining <= 0 {
			auditErr := session.auditEditorLeaseLost("heartbeat_deadline")
			if auditErr != nil {
				return auditErr
			}
			return ErrGatewayEditorLeaseLost
		}
		timer := time.NewTimer(remaining)
		select {
		case <-session.ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return session.ctx.Err()
		case <-session.heartbeatReset:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		// Always recalculate from the compare-owner renewal timestamp. A
		// delayed/coalesced signal can wake this loop but can never move the
		// deadline beyond Redis' authoritative TTL.
	}
}

func (session *gatewaySession) renewEditorLease(ctx context.Context) error {
	if session.editorLease == nil {
		return nil
	}
	leaseCtx, cancelLease := context.WithTimeout(ctx, gatewayDetachedAuditTimeout)
	renewed, err := session.gateway.security.RenewGatewayEditorLease(leaseCtx, session.editorLease.input)
	cancelLease()
	if err != nil {
		auditErr := session.auditEditorLeaseLost("lease_store_unavailable")
		return errors.Join(ErrGatewaySecurityUnavailable, err, auditErr)
	}
	if !renewed {
		if auditErr := session.auditEditorLeaseLost("owner_fenced"); auditErr != nil {
			return auditErr
		}
		return ErrGatewayEditorLeaseLost
	}
	if err := session.editorLease.recordSuccessfulRenewal(session.gateway.now()); err != nil {
		if auditErr := session.auditEditorLeaseLost("renewal_clock_invalid"); auditErr != nil {
			return auditErr
		}
		return ErrGatewaySecurityUnavailable
	}
	select {
	case session.heartbeatReset <- struct{}{}:
	default:
		// A pending signal already represents a renewal at least as recent as
		// the timer's last observation. Coalescing cannot shorten the deadline.
	}
	return nil
}

func (session *gatewaySession) serverLoop() error {
	for {
		frame, err := session.process.ReadFrame(session.ctx)
		if err != nil {
			return err
		}
		session.operationMu.Lock()
		if err := session.revalidate(session.ctx); err != nil {
			session.operationMu.Unlock()
			return err
		}
		head, documents := session.protocol.CurrentHeadAndDocuments()
		control, controlErr := DecodeServerControlMessage(frame, session.profile.EffectiveLimits)
		if controlErr == nil {
			terminate, handleErr := session.gateway.handleServerControl(
				session.ctx, session.process, session.grant,
				session.protocol.connectionID, session.protocol.bindingID, head,
				session.profile, session.serverControl, control, func() error {
					return session.revalidate(session.ctx)
				},
			)
			session.operationMu.Unlock()
			if handleErr != nil {
				return handleErr
			}
			if terminate {
				return ErrGatewayServerViolation
			}
			continue
		}
		if !errors.Is(controlErr, ErrServerControlNotApplicable) {
			_, auditErr := session.gateway.handleServerControl(
				session.ctx, session.process, session.grant,
				session.protocol.connectionID, session.protocol.bindingID, head,
				session.profile, session.serverControl, malformedServerControlAuditMessage(), nil,
			)
			session.operationMu.Unlock()
			if auditErr != nil {
				return auditErr
			}
			return errors.Join(ErrGatewayServerViolation, controlErr)
		}
		message, err := session.filter.Filter(frame, head, documents)
		if err != nil && errors.Is(err, ErrServerResponseUnknown) && session.consumeIgnoredServerFrame(frame) {
			session.operationMu.Unlock()
			continue
		}
		if err != nil {
			_, auditErr := session.gateway.handleServerControl(
				session.ctx, session.process, session.grant,
				session.protocol.connectionID, session.protocol.bindingID, head,
				session.profile, session.serverControl, malformedServerControlAuditMessage(), nil,
			)
			session.operationMu.Unlock()
			if auditErr != nil {
				return auditErr
			}
			return errors.Join(ErrGatewayServerViolation, err)
		}
		var outgoing *ServerEnvelope
		outgoing, err = session.handleServerMessage(message)
		if err == nil && outgoing != nil {
			err = session.revalidate(session.ctx)
		}
		if err == nil && outgoing != nil {
			err = session.writeBrowser(session.ctx, *outgoing)
		}
		session.operationMu.Unlock()
		if err != nil {
			return err
		}
	}
}

func (gateway *Gateway) handleServerControl(
	ctx context.Context,
	process LanguageServerProcess,
	grant TicketGrant,
	connectionID string,
	bindingID string,
	head SandboxHeadFence,
	profile ProfileIdentity,
	state *serverControlState,
	message ServerControlMessage,
	beforeResponse func() error,
) (bool, error) {
	if gateway == nil || ctx == nil || process == nil || state == nil {
		return false, ErrGatewayInvalid
	}
	if message.Disposition == ServerControlDropNotification && message.AuditCode == "" &&
		len(message.Response) == 0 {
		if _, harmless := ignoredServerNotifications[message.Method]; !harmless {
			return false, ErrGatewayServerViolation
		}
		return false, nil
	}
	violation, disposition, code, err := state.record(message)
	if err != nil {
		return false, err
	}
	if err := gateway.auditServerViolation(
		ctx, grant, connectionID, bindingID, head, profile, violation, code,
	); err != nil {
		return false, err
	}
	if len(message.Response) > 0 {
		if beforeResponse != nil {
			if err := beforeResponse(); err != nil {
				return false, err
			}
		}
		responseCtx, cancelResponse := context.WithTimeout(
			ctx, time.Duration(profile.EffectiveLimits.RequestTimeoutMillis)*time.Millisecond,
		)
		defer cancelResponse()
		if err := process.WriteFrame(responseCtx, message.Response); err != nil {
			return false, classifyGatewayError(err)
		}
	}
	return disposition == ServerControlRespondTerminate || disposition == ServerControlTerminate, nil
}

func (session *gatewaySession) handleClientEnvelope(ctx context.Context, envelope ClientEnvelope) error {
	switch payload := envelope.Payload.(type) {
	case DocumentOpenEnvelopePayload:
		authoritative, exists := session.authoritative[envelope.Document.ModelURI]
		if !exists || !authoritative.Fence.Equal(*envelope.Document) ||
			!bytes.Equal(authoritative.Text, []byte(payload.Text)) {
			return ErrGatewayStale
		}
		serverURI, err := ServerDocumentURI(envelope.Document.ModelURI, envelope.Head)
		if err != nil {
			return ErrGatewayInvalid
		}
		return session.writeProcess(ctx, "textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri": serverURI, "languageId": payload.LanguageID,
				"version": envelope.Document.ModelVersion, "text": payload.Text,
			},
		})
	case DocumentChangeEnvelopePayload:
		serverURI, err := ServerDocumentURI(envelope.Document.ModelURI, envelope.Head)
		if err != nil {
			return ErrGatewayInvalid
		}
		return session.writeProcess(ctx, "textDocument/didChange", map[string]any{
			"textDocument": map[string]any{
				"uri": serverURI, "version": envelope.Document.ModelVersion,
			},
			"contentChanges": []map[string]any{{"text": payload.Text}},
		})
	case EmptyEnvelopePayload:
		if envelope.Kind == ClientEnvelopeDocumentClose {
			serverURI, err := ServerDocumentURI(envelope.Document.ModelURI, envelope.Head)
			if err != nil {
				return ErrGatewayInvalid
			}
			return session.writeProcess(ctx, "textDocument/didClose", map[string]any{
				"textDocument": map[string]any{"uri": serverURI},
			})
		}
		// A Repository save advances head and savedContentHash together.
		// document.save under the old head can never prove that atomic CAS;
		// production uses one exact headRebind instead and never sends didSave.
		return ErrGatewayStale
	case RequestEnvelopePayload:
		return session.startRequest(ctx, envelope, payload)
	case CancelEnvelopePayload:
		return session.cancelBrowserRequest(ctx, payload.ReplyTo)
	case HeadRebindEnvelopePayload:
		if err := session.auditBinding(ctx, GatewayAuditBindingRebind, "rebound", "ok"); err != nil {
			return err
		}
		return session.cancelAllForRebind(ctx)
	case PingEnvelopePayload:
		response, err := session.protocol.BuildPongEnvelope(envelope.MessageID, payload.Nonce)
		if err != nil {
			return classifyGatewayError(err)
		}
		return session.writeBrowser(ctx, response)
	default:
		return ErrGatewayInvalid
	}
}

func (session *gatewaySession) startRequest(
	ctx context.Context,
	envelope ClientEnvelope,
	payload RequestEnvelopePayload,
) error {
	if envelope.Document == nil {
		return ErrGatewayInvalid
	}
	pending := &gatewayPending{
		browserID: envelope.MessageID, method: envelope.Method,
		head: envelope.Head, document: *envelope.Document, startedAt: session.gateway.now().UTC(),
	}
	securityCtx, cancelSecurity := context.WithTimeout(ctx, gatewayDetachedAuditTimeout)
	decision, rateErr := session.gateway.security.AllowGatewayRequest(securityCtx, GatewayRequestRateLimitInput{
		ProjectID: session.grant.ProjectID, ActorID: session.grant.ActorID,
		SessionID: session.grant.SessionID, ProfileID: session.profile.ID,
		ProfileContentHash: session.profile.ContentHash, CapabilityHash: session.profile.CapabilityHash,
		Method: envelope.Method, RequestsPerSecond: session.profile.EffectiveLimits.RequestsPerSecond,
		RequestBurst: session.profile.EffectiveLimits.RequestBurst,
	})
	cancelSecurity()
	if rateErr != nil {
		if auditErr := session.auditRequest(
			ctx, pending, GatewayAuditRequestError, "error", "rate_limiter_unavailable",
		); auditErr != nil {
			return auditErr
		}
		return errors.Join(ErrGatewaySecurityUnavailable, rateErr)
	}
	if !decision.Allowed {
		if auditErr := session.auditRequest(
			ctx, pending, GatewayAuditRequestError, "rate_limited", "rate_limited",
		); auditErr != nil {
			return auditErr
		}
		return ErrGatewayRequestRateLimited
	}
	if err := session.auditRequest(
		ctx, pending, GatewayAuditRequestAdmitted, "admitted", "ok",
	); err != nil {
		return err
	}
	if !session.methods[envelope.Method] {
		return session.finishAdmittedRequest(ctx, pending, "method_unavailable", ErrGatewayInvalid)
	}
	serverParams, err := serverRequestPayload(payload.Params, *envelope.Document, envelope.Head)
	if err != nil {
		return session.finishAdmittedRequest(ctx, pending, "request_invalid", ErrGatewayInvalid)
	}
	serverID := session.gateway.idSource()
	if !canonicalUUID(serverID) || serverID == envelope.MessageID || serverID == session.protocol.connectionID ||
		serverID == session.protocol.bindingID {
		return session.finishAdmittedRequest(ctx, pending, "request_identity_invalid", ErrGatewayInvalid)
	}
	pending.serverID = serverID
	request := PendingServerRequest{
		ID: serverID, Method: envelope.Method, Head: envelope.Head, Document: *envelope.Document,
	}
	if err := session.filter.RegisterPending(request); err != nil {
		return session.finishAdmittedRequest(
			ctx, pending, "request_registration_failed", classifyGatewayError(err),
		)
	}
	session.requestMu.Lock()
	if session.byBrowser[envelope.MessageID] != nil || session.byServer[serverID] != nil {
		session.requestMu.Unlock()
		_ = session.filter.CancelPending(serverID)
		return session.finishAdmittedRequest(ctx, pending, "request_identity_reused", ErrGatewayInvalid)
	}
	session.byBrowser[envelope.MessageID] = pending
	session.byServer[serverID] = pending
	session.requestMu.Unlock()

	frame, err := marshalGatewayJSONRPCRequest(
		serverID, envelope.Method, serverParams, session.profile.EffectiveLimits,
	)
	if err != nil {
		claimed := session.removePending(serverID, true)
		return session.finishAdmittedRequest(ctx, claimed, "request_encoding_failed", err)
	}
	if err := session.writeProcessFrame(ctx, frame); err != nil {
		claimed := session.removePending(serverID, true)
		return session.finishAdmittedRequest(ctx, claimed, "runtime_write_failed", err)
	}
	timeout := time.Duration(session.profile.EffectiveLimits.RequestTimeoutMillis) * time.Millisecond
	pending.timer = time.AfterFunc(timeout, func() { session.expireRequest(serverID) })
	return nil
}

func (session *gatewaySession) cancelBrowserRequest(ctx context.Context, browserID string) error {
	session.requestMu.Lock()
	pending := session.byBrowser[browserID]
	if pending != nil {
		delete(session.byBrowser, browserID)
		delete(session.byServer, pending.serverID)
		session.ignoredServer[pending.serverID] = struct{}{}
	}
	session.requestMu.Unlock()
	if pending == nil {
		return ErrGatewayInvalid
	}
	if pending.timer != nil {
		pending.timer.Stop()
	}
	if err := session.auditRequest(
		ctx, pending, GatewayAuditRequestCancel, "canceled", "client_cancel",
	); err != nil {
		return err
	}
	if err := session.filter.CancelPending(pending.serverID); err != nil {
		return classifyGatewayError(err)
	}
	return session.writeProcess(ctx, "$/cancelRequest", map[string]any{"id": pending.serverID})
}

func (session *gatewaySession) cancelAllForRebind(ctx context.Context) error {
	session.requestMu.Lock()
	values := make([]*gatewayPending, 0, len(session.byServer))
	for _, pending := range session.byServer {
		values = append(values, pending)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].serverID < values[j].serverID })
	for _, pending := range values {
		delete(session.byBrowser, pending.browserID)
		delete(session.byServer, pending.serverID)
		session.ignoredServer[pending.serverID] = struct{}{}
	}
	session.requestMu.Unlock()
	var auditErr error
	for _, pending := range values {
		auditErr = errors.Join(auditErr, session.auditRequest(
			ctx, pending, GatewayAuditRequestStale, "stale", "head_rebound",
		))
	}
	if auditErr != nil {
		return auditErr
	}
	for _, pending := range values {
		if pending.timer != nil {
			pending.timer.Stop()
		}
		_ = session.filter.CancelPending(pending.serverID)
		if err := session.writeProcess(ctx, "$/cancelRequest", map[string]any{"id": pending.serverID}); err != nil {
			return err
		}
		stale, err := session.protocol.BuildStaleEnvelope(pending.browserID, "head-rebound")
		if err != nil {
			return classifyGatewayError(err)
		}
		if err := session.writeBrowser(ctx, stale); err != nil {
			return err
		}
	}
	return nil
}

func (session *gatewaySession) handleServerMessage(message FilteredServerMessage) (*ServerEnvelope, error) {
	if message.Kind == ServerMessageKindNotification {
		if message.Disposition == ServerMessageStaleDropped {
			return nil, nil
		}
		envelope, err := session.protocol.BuildDiagnosticsEnvelope(message.Document, message.Payload)
		return &envelope, classifyGatewayError(err)
	}
	if message.Kind != ServerMessageKindResponse {
		return nil, ErrGatewayInvalid
	}
	pending := session.removePending(message.RequestID, false)
	if pending == nil {
		if session.consumeIgnoredServerID(message.RequestID) {
			return nil, nil
		}
		return nil, ErrGatewayInvalid
	}
	if message.Disposition == ServerMessageStaleDropped {
		envelope, err := session.protocol.BuildStaleEnvelope(pending.browserID, "stale-response")
		if err != nil {
			return nil, session.finishAdmittedRequest(
				session.ctx, pending, "stale_response_encoding_failed", classifyGatewayError(err),
			)
		}
		if err := session.auditRequest(
			session.ctx, pending, GatewayAuditRequestStale, "stale", "stale_response",
		); err != nil {
			return nil, err
		}
		return &envelope, nil
	}
	envelope, err := session.protocol.BuildResponseEnvelope(
		pending.browserID, message.Payload, message.Error,
	)
	if err != nil {
		return nil, session.finishAdmittedRequest(
			session.ctx, pending, "response_invalid", classifyGatewayError(err),
		)
	}
	action, outcome, code := GatewayAuditRequestCompleted, "completed", "ok"
	if message.Error != nil {
		action, outcome, code = GatewayAuditRequestError, "error", "server_error"
	}
	if err := session.auditRequest(session.ctx, pending, action, outcome, code); err != nil {
		return nil, err
	}
	return &envelope, nil
}

func (session *gatewaySession) expireRequest(serverID string) {
	if session.ctx.Err() != nil {
		return
	}
	session.operationMu.Lock()
	defer session.operationMu.Unlock()
	if session.ctx.Err() != nil {
		return
	}
	if err := session.revalidate(session.ctx); err != nil {
		session.fail(err)
		return
	}
	pending := session.removePending(serverID, true)
	if pending == nil {
		return
	}
	stale, err := session.protocol.BuildStaleEnvelope(pending.browserID, "request-timeout")
	if err != nil {
		session.fail(session.finishAdmittedRequest(
			session.ctx, pending, "timeout_encoding_failed", classifyGatewayError(err),
		))
		return
	}
	if err := session.auditRequest(
		session.ctx, pending, GatewayAuditRequestTimeout, "timed_out", "request_timeout",
	); err != nil {
		session.fail(err)
		return
	}
	if err := session.revalidate(session.ctx); err != nil {
		session.fail(err)
		return
	}
	requestCtx, cancel := context.WithTimeout(
		session.ctx, time.Duration(session.profile.EffectiveLimits.RequestTimeoutMillis)*time.Millisecond,
	)
	defer cancel()
	if err := session.writeProcess(requestCtx, "$/cancelRequest", map[string]any{"id": serverID}); err != nil {
		session.fail(err)
		return
	}
	if err := session.writeBrowser(requestCtx, stale); err != nil {
		session.fail(err)
	}
}

func (session *gatewaySession) removePending(serverID string, ignoreLate bool) *gatewayPending {
	session.requestMu.Lock()
	defer session.requestMu.Unlock()
	pending := session.byServer[serverID]
	if pending == nil {
		return nil
	}
	delete(session.byServer, serverID)
	delete(session.byBrowser, pending.browserID)
	if ignoreLate {
		session.ignoredServer[serverID] = struct{}{}
	}
	if pending.timer != nil {
		pending.timer.Stop()
	}
	if ignoreLate {
		_ = session.filter.CancelPending(serverID)
	}
	return pending
}

func (session *gatewaySession) consumeIgnoredServerFrame(frame []byte) bool {
	fields, err := decodeServerTopObject(frame)
	if err != nil {
		return false
	}
	if _, hasMethod := fields["method"]; hasMethod {
		return false
	}
	requestID, err := decodeRequiredServerString(fields["id"], 64)
	return err == nil && session.consumeIgnoredServerID(requestID)
}

func (session *gatewaySession) consumeIgnoredServerID(serverID string) bool {
	session.requestMu.Lock()
	defer session.requestMu.Unlock()
	_, exists := session.ignoredServer[serverID]
	if exists {
		delete(session.ignoredServer, serverID)
	}
	return exists
}

func (session *gatewaySession) stopAllRequests(cause error) error {
	session.requestMu.Lock()
	values := make([]*gatewayPending, 0, len(session.byServer))
	for _, pending := range session.byServer {
		if pending.timer != nil {
			pending.timer.Stop()
		}
		values = append(values, pending)
	}
	session.byBrowser = make(map[string]*gatewayPending)
	session.byServer = make(map[string]*gatewayPending)
	session.requestMu.Unlock()
	sort.Slice(values, func(left, right int) bool { return values[left].serverID < values[right].serverID })
	action, outcome, code := GatewayAuditRequestError, "error", gatewayCloseCode(cause)
	if errors.Is(cause, ErrGatewayStale) {
		action, outcome, code = GatewayAuditRequestStale, "stale", "authority_stale"
	}
	var result error
	for _, pending := range values {
		_ = session.filter.CancelPending(pending.serverID)
		result = errors.Join(result, session.auditRequestDetached(pending, action, outcome, code))
	}
	return result
}

func (session *gatewaySession) auditBinding(
	ctx context.Context,
	action, outcome, code string,
) error {
	head, _ := session.protocol.CurrentHeadAndDocuments()
	event := GatewayAuditEvent{
		Action: action, Outcome: outcome, Code: code,
		TicketID: session.grant.ID, ProjectID: session.grant.ProjectID,
		ActorID: session.grant.ActorID, SessionID: session.grant.SessionID,
		ConnectionID: session.protocol.connectionID, BindingID: session.protocol.bindingID,
		Mode: session.grant.Mode, Head: head, TemplateRelease: session.grant.TemplateRelease,
		Profile: gatewayProfileAuditIdentity(session.profile), OccurredAt: session.gateway.now().UTC(),
	}
	auditCtx, cancelAudit := context.WithTimeout(ctx, gatewayDetachedAuditTimeout)
	defer cancelAudit()
	if err := session.gateway.security.AppendGatewayAudit(auditCtx, event); err != nil {
		return errors.Join(ErrGatewaySecurityUnavailable, err)
	}
	return nil
}

func (session *gatewaySession) auditBindingDetached(
	action, outcome, code string,
) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(session.ctx), gatewayDetachedAuditTimeout)
	defer cancel()
	return session.auditBinding(ctx, action, outcome, code)
}

func (gateway *Gateway) auditServerViolation(
	ctx context.Context,
	grant TicketGrant,
	connectionID string,
	bindingID string,
	head SandboxHeadFence,
	profile ProfileIdentity,
	violation GatewayServerViolationAudit,
	code string,
) error {
	if gateway == nil || ctx == nil {
		return ErrGatewaySecurityUnavailable
	}
	copyViolation := violation
	event := GatewayAuditEvent{
		Action: GatewayAuditServerViolation, Outcome: "rejected", Code: code,
		TicketID: grant.ID, ProjectID: grant.ProjectID, ActorID: grant.ActorID,
		SessionID: grant.SessionID, ConnectionID: connectionID, BindingID: bindingID,
		Mode: grant.Mode, Head: head, TemplateRelease: grant.TemplateRelease,
		Profile: gatewayProfileAuditIdentity(profile), ServerViolation: &copyViolation,
		OccurredAt: gateway.now().UTC(),
	}
	auditCtx, cancelAudit := context.WithTimeout(ctx, gatewayDetachedAuditTimeout)
	defer cancelAudit()
	if err := gateway.security.AppendGatewayAudit(auditCtx, event); err != nil {
		return errors.Join(ErrGatewaySecurityUnavailable, err)
	}
	return nil
}

func (gateway *Gateway) auditEditorLeaseDetached(
	parent context.Context,
	grant TicketGrant,
	bind ClientBind,
	bindingID, action, outcome, code string,
) error {
	return gateway.appendEditorLeaseAuditDetached(
		parent, grant, bind.Profile, bind.ConnectionID, bindingID, bind.Head,
		action, outcome, code,
	)
}

func (gateway *Gateway) appendEditorLeaseAuditDetached(
	parent context.Context,
	grant TicketGrant,
	profile ProfileIdentity,
	connectionID, bindingID string,
	head SandboxHeadFence,
	action, outcome, code string,
) error {
	if gateway == nil || parent == nil {
		return ErrGatewaySecurityUnavailable
	}
	event := GatewayAuditEvent{
		Action: action, Outcome: outcome, Code: code,
		TicketID: grant.ID, ProjectID: grant.ProjectID, ActorID: grant.ActorID,
		SessionID: grant.SessionID, ConnectionID: connectionID, BindingID: bindingID,
		Mode: grant.Mode, Head: head, TemplateRelease: grant.TemplateRelease,
		Profile: gatewayProfileAuditIdentity(profile), OccurredAt: gateway.now().UTC(),
	}
	auditCtx, cancelAudit := context.WithTimeout(context.WithoutCancel(parent), gatewayDetachedAuditTimeout)
	defer cancelAudit()
	if err := gateway.security.AppendGatewayAudit(auditCtx, event); err != nil {
		return errors.Join(ErrGatewaySecurityUnavailable, err)
	}
	return nil
}

func (state *gatewayEditorLeaseState) currentHead(fallback SandboxHeadFence) SandboxHeadFence {
	if state != nil && state.protocol != nil {
		head, _ := state.protocol.CurrentHeadAndDocuments()
		return head
	}
	return fallback
}

func (state *gatewayEditorLeaseState) recordSuccessfulRenewal(now time.Time) error {
	if state == nil || now.IsZero() || validateGatewayEditorLeaseContract(state.contract) != nil {
		return ErrGatewaySecurityUnavailable
	}
	state.renewalMu.Lock()
	defer state.renewalMu.Unlock()
	if !state.lastSuccessfulRenewal.IsZero() && now.Before(state.lastSuccessfulRenewal) {
		return ErrGatewaySecurityUnavailable
	}
	state.lastSuccessfulRenewal = now
	return nil
}

func (state *gatewayEditorLeaseState) heartbeatRemaining(now time.Time) (time.Duration, error) {
	if state == nil || now.IsZero() || validateGatewayEditorLeaseContract(state.contract) != nil {
		return 0, ErrGatewaySecurityUnavailable
	}
	state.renewalMu.Lock()
	defer state.renewalMu.Unlock()
	if state.lastSuccessfulRenewal.IsZero() || now.Before(state.lastSuccessfulRenewal) {
		return 0, ErrGatewaySecurityUnavailable
	}
	deadline := state.contract.TTL - state.contract.HeartbeatInterval
	return deadline - now.Sub(state.lastSuccessfulRenewal), nil
}

func (gateway *Gateway) auditEditorLeaseLostOnce(
	parent context.Context,
	grant TicketGrant,
	profile ProfileIdentity,
	connectionID string,
	head SandboxHeadFence,
	state *gatewayEditorLeaseState,
	code string,
) error {
	if state == nil {
		return ErrGatewaySecurityUnavailable
	}
	state.auditMu.Lock()
	defer state.auditMu.Unlock()
	if state.lostAudited {
		return nil
	}
	if err := gateway.appendEditorLeaseAuditDetached(
		parent, grant, profile, connectionID, state.input.OwnerBindingID, head,
		GatewayAuditEditorLeaseLost, "lost", code,
	); err != nil {
		return err
	}
	state.lostAudited = true
	return nil
}

func (session *gatewaySession) auditEditorLeaseLost(code string) error {
	if session == nil || session.editorLease == nil {
		return ErrGatewaySecurityUnavailable
	}
	head, _ := session.protocol.CurrentHeadAndDocuments()
	return session.gateway.auditEditorLeaseLostOnce(
		session.ctx, session.grant, session.profile, session.protocol.connectionID,
		head, session.editorLease, code,
	)
}

func (gateway *Gateway) releaseEditorLeaseDetached(
	parent context.Context,
	grant TicketGrant,
	bind ClientBind,
	state *gatewayEditorLeaseState,
) error {
	if gateway == nil || parent == nil || state == nil {
		return ErrGatewaySecurityUnavailable
	}
	releaseCtx, cancelRelease := context.WithTimeout(
		context.WithoutCancel(parent), gatewayDetachedAuditTimeout,
	)
	released, err := gateway.security.ReleaseGatewayEditorLease(releaseCtx, state.input)
	cancelRelease()
	head := state.currentHead(bind.Head)
	if err != nil {
		auditErr := gateway.auditEditorLeaseLostOnce(
			parent, grant, bind.Profile, bind.ConnectionID, head, state, "lease_store_unavailable",
		)
		return errors.Join(ErrGatewaySecurityUnavailable, err, auditErr)
	}
	if !released {
		if auditErr := gateway.auditEditorLeaseLostOnce(
			parent, grant, bind.Profile, bind.ConnectionID, head, state, "release_fenced",
		); auditErr != nil {
			return auditErr
		}
		return ErrGatewayEditorLeaseLost
	}
	return gateway.appendEditorLeaseAuditDetached(
		parent, grant, bind.Profile, bind.ConnectionID, state.input.OwnerBindingID, head,
		GatewayAuditEditorLeaseRelease, "released", "ok",
	)
}

func (session *gatewaySession) auditRequest(
	ctx context.Context,
	pending *gatewayPending,
	action, outcome, code string,
) error {
	if pending == nil {
		return ErrGatewaySecurityUnavailable
	}
	now := session.gateway.now().UTC()
	latency := now.Sub(pending.startedAt).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	if action == GatewayAuditRequestAdmitted {
		latency = 0
	}
	document := pending.document
	event := GatewayAuditEvent{
		Action: action, Outcome: outcome, Code: code,
		TicketID: session.grant.ID, ProjectID: session.grant.ProjectID,
		ActorID: session.grant.ActorID, SessionID: session.grant.SessionID,
		ConnectionID: session.protocol.connectionID, BindingID: session.protocol.bindingID,
		Mode: session.grant.Mode, Head: pending.head, Document: &document,
		TemplateRelease: session.grant.TemplateRelease,
		Profile:         gatewayProfileAuditIdentity(session.profile), RequestID: pending.browserID,
		Method: pending.method, LatencyMillis: latency, OccurredAt: now,
	}
	auditCtx, cancelAudit := context.WithTimeout(ctx, gatewayDetachedAuditTimeout)
	defer cancelAudit()
	if err := session.gateway.security.AppendGatewayAudit(auditCtx, event); err != nil {
		return errors.Join(ErrGatewaySecurityUnavailable, err)
	}
	return nil
}

func (session *gatewaySession) auditRequestDetached(
	pending *gatewayPending,
	action, outcome, code string,
) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(session.ctx), gatewayDetachedAuditTimeout)
	defer cancel()
	return session.auditRequest(ctx, pending, action, outcome, code)
}

func (session *gatewaySession) finishAdmittedRequest(
	ctx context.Context,
	pending *gatewayPending,
	code string,
	cause error,
) error {
	if pending == nil {
		return ErrGatewaySecurityUnavailable
	}
	if auditErr := session.auditRequest(
		ctx, pending, GatewayAuditRequestError, "error", code,
	); auditErr != nil {
		return auditErr
	}
	return cause
}

func gatewayProfileAuditIdentity(profile ProfileIdentity) TicketAuditProfile {
	return TicketAuditProfile{
		ID: profile.ID, ContentHash: profile.ContentHash, Image: profile.Runtime.Image,
		ExecutableDigest: profile.Runtime.ExecutableDigest, CapabilityHash: profile.CapabilityHash,
	}
}

func gatewayCloseCode(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrGatewayRequestRateLimited):
		return "rate_limited"
	case errors.Is(err, ErrGatewaySecurityUnavailable):
		return "security_unavailable"
	case errors.Is(err, ErrGatewayEditorLeaseConflict):
		return "editor_lease_conflict"
	case errors.Is(err, ErrGatewayEditorLeaseLost):
		return "editor_lease_lost"
	case errors.Is(err, ErrGatewayServerViolation):
		return "server_capability_violation"
	case errors.Is(err, ErrGatewayStale):
		return "gateway_stale"
	case errors.Is(err, ErrGatewayInvalid):
		return "gateway_invalid"
	case errors.Is(err, ErrGatewayUnavailable):
		return "gateway_unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, io.EOF):
		return "connection_closed"
	default:
		return "gateway_error"
	}
}

func (session *gatewaySession) revalidate(ctx context.Context) error {
	head, documents := session.protocol.CurrentHeadAndDocuments()
	return session.revalidateFence(ctx, head, documents)
}

func (session *gatewaySession) revalidateFence(
	ctx context.Context,
	head SandboxHeadFence,
	documents []DocumentFence,
) error {
	if err := session.gateway.authority.RevalidateGatewayFence(
		ctx, session.grant, head, session.profile, documents,
	); err != nil {
		return classifyGatewayError(err)
	}
	return nil
}

func (session *gatewaySession) writeProcess(ctx context.Context, method string, params any) error {
	frame, err := marshalGatewayJSONRPCNotification(method, params, session.profile.EffectiveLimits)
	if err != nil {
		return err
	}
	return session.writeProcessFrame(ctx, frame)
}

func (session *gatewaySession) writeProcessFrame(ctx context.Context, frame []byte) error {
	requestCtx, cancel := context.WithTimeout(
		ctx, time.Duration(session.profile.EffectiveLimits.RequestTimeoutMillis)*time.Millisecond,
	)
	defer cancel()
	if err := session.process.WriteFrame(requestCtx, frame); err != nil {
		return classifyGatewayError(err)
	}
	return nil
}

func (session *gatewaySession) writeBrowser(ctx context.Context, envelope ServerEnvelope) error {
	frame, err := envelope.MarshalJSONStrict(session.profile.EffectiveLimits.MaxFrameBytes)
	if err != nil {
		return classifyGatewayError(err)
	}
	requestCtx, cancel := context.WithTimeout(
		ctx, time.Duration(session.profile.EffectiveLimits.RequestTimeoutMillis)*time.Millisecond,
	)
	defer cancel()
	if err := session.connection.WriteFrame(requestCtx, frame); err != nil {
		return classifyGatewayError(err)
	}
	return nil
}

func marshalGatewayJSONRPCRequest(
	id string,
	method string,
	params any,
	limits EffectiveLimits,
) ([]byte, error) {
	if !canonicalUUID(id) || params == nil || AdmitBrowserRequestMethod(method, ProductionV1MethodBaseline()) != nil {
		return nil, ErrGatewayInvalid
	}
	return marshalGatewayFrame(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{JSONRPC: "2.0", ID: id, Method: method, Params: params}, limits)
}

func marshalGatewayJSONRPCNotification(
	method string,
	params any,
	limits EffectiveLimits,
) ([]byte, error) {
	allowed := method == "textDocument/didOpen" || method == "textDocument/didChange" ||
		method == "textDocument/didClose" || method == "$/cancelRequest"
	if !allowed || params == nil {
		return nil, ErrGatewayInvalid
	}
	return marshalGatewayFrame(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{JSONRPC: "2.0", Method: method, Params: params}, limits)
}

func marshalGatewayFrame(value any, limits EffectiveLimits) ([]byte, error) {
	frame, err := json.Marshal(value)
	if err != nil || int64(len(frame)) == 0 || int64(len(frame)) > limits.MaxFrameBytes ||
		validateStrictJSONDocument(frame, maximumEnvelopeDepth) != nil {
		return nil, ErrGatewayInvalid
	}
	return frame, nil
}

func terminateGatewayProcess(process LanguageServerProcess, limits EffectiveLimits) error {
	if process == nil {
		return nil
	}
	timeout := time.Duration(limits.ShutdownTimeoutMillis) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return process.Terminate(ctx)
}

func classifyGatewayError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) {
		return err
	}
	if errors.Is(err, ErrGatewaySecurityUnavailable) || errors.Is(err, ErrGatewayRequestRateLimited) ||
		errors.Is(err, ErrGatewayEditorLeaseConflict) || errors.Is(err, ErrGatewayEditorLeaseLost) ||
		errors.Is(err, ErrGatewayServerViolation) {
		return err
	}
	if errors.Is(err, ErrGatewayStale) || errors.Is(err, ErrRuntimeBindingStale) ||
		errors.Is(err, ErrHeadStale) || errors.Is(err, ErrEnvelopeHeadStale) ||
		errors.Is(err, ErrEnvelopeDocumentStale) {
		return ErrGatewayStale
	}
	return fmt.Errorf("%w", ErrGatewayUnavailable)
}

func benignGatewayClosure(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) ||
		errors.Is(err, ErrGatewayClosed)
}

func gatewayContentDigest(value []byte, expected string) bool {
	sum := sha256.Sum256(value)
	return expected == "sha256:"+hex.EncodeToString(sum[:])
}
