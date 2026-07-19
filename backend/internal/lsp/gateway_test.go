package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/templates"
)

const (
	gatewayBindingID        = "60000000-0000-4000-8000-000000000001"
	gatewayServerRequestID  = "60000000-0000-4000-8000-000000000002"
	gatewayBrowserRequestID = "60000000-0000-4000-8000-000000000003"
	gatewayServerMessageID  = "60000000-0000-4000-8000-000000000004"
)

type gatewayBindingResolverFake struct {
	projection RuntimeBindingProjection
	err        error
	calls      atomic.Int32
}

func (fake *gatewayBindingResolverFake) Resolve(
	ctx context.Context,
	_ TicketGrant,
	_ ClientBind,
) (RuntimeBindingProjection, error) {
	fake.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return RuntimeBindingProjection{}, err
	}
	if fake.err != nil {
		return RuntimeBindingProjection{}, fake.err
	}
	result := fake.projection
	result.Documents = cloneRuntimeDocuments(fake.projection.Documents)
	return result, nil
}

type gatewayFrameResult struct {
	value []byte
	err   error
}

type gatewayProcessFake struct {
	profile ProfileIdentity
	reads   chan gatewayFrameResult
	writes  chan []byte
	done    chan struct{}

	terminateOnce sync.Once
	terminates    atomic.Int32
	writeErr      error
}

func newGatewayProcessFake(profile ProfileIdentity) *gatewayProcessFake {
	return &gatewayProcessFake{
		profile: profile, reads: make(chan gatewayFrameResult, 32),
		writes: make(chan []byte, 64), done: make(chan struct{}),
	}
}

func (fake *gatewayProcessFake) Name() string { return "gateway-test-process" }

func (fake *gatewayProcessFake) Profile() ProfileIdentity {
	return cloneProfiles([]ProfileIdentity{fake.profile})[0]
}

func (fake *gatewayProcessFake) WriteFrame(ctx context.Context, value []byte) error {
	if fake.writeErr != nil {
		return fake.writeErr
	}
	select {
	case fake.writes <- slices.Clone(value):
		return nil
	case <-fake.done:
		return ErrContainerRuntimeClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (fake *gatewayProcessFake) ReadFrame(ctx context.Context) ([]byte, error) {
	select {
	case result := <-fake.reads:
		return slices.Clone(result.value), result.err
	case <-fake.done:
		return nil, ErrContainerRuntimeClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (fake *gatewayProcessFake) Stderr() []byte { return nil }

func (fake *gatewayProcessFake) Wait(ctx context.Context) (ContainerProcessExit, error) {
	select {
	case <-fake.done:
		return ContainerProcessExit{FinishedAt: time.Now().UTC()}, nil
	case <-ctx.Done():
		return ContainerProcessExit{}, ctx.Err()
	}
}

func (fake *gatewayProcessFake) Terminate(context.Context) error {
	fake.terminateOnce.Do(func() {
		fake.terminates.Add(1)
		close(fake.done)
	})
	return nil
}

type gatewayRuntimeFake struct {
	process  *gatewayProcessFake
	starts   chan ContainerStartInput
	ready    atomic.Int32
	startErr error
}

func (fake *gatewayRuntimeFake) Readiness(ctx context.Context, profiles ...ProfileIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(profiles) != 1 || profiles[0].Validate() != nil {
		return ErrContainerRuntimeInvalid
	}
	fake.ready.Add(1)
	return nil
}

func (fake *gatewayRuntimeFake) Start(
	ctx context.Context,
	input ContainerStartInput,
) (LanguageServerProcess, error) {
	if fake.startErr != nil {
		return nil, fake.startErr
	}
	select {
	case fake.starts <- input:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return fake.process, nil
}

func (*gatewayRuntimeFake) Close() error { return nil }

type gatewayConnectionFake struct {
	reads    chan gatewayFrameResult
	writes   chan []byte
	closed   chan struct{}
	writeErr error

	closeOnce sync.Once
	closes    atomic.Int32
}

func newGatewayConnectionFake() *gatewayConnectionFake {
	return &gatewayConnectionFake{
		reads: make(chan gatewayFrameResult, 64), writes: make(chan []byte, 64),
		closed: make(chan struct{}),
	}
}

func (fake *gatewayConnectionFake) ReadFrame(ctx context.Context) ([]byte, error) {
	select {
	case result := <-fake.reads:
		return slices.Clone(result.value), result.err
	case <-fake.closed:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (fake *gatewayConnectionFake) WriteFrame(ctx context.Context, value []byte) error {
	if fake.writeErr != nil {
		return fake.writeErr
	}
	select {
	case fake.writes <- slices.Clone(value):
		return nil
	case <-fake.closed:
		return io.EOF
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (fake *gatewayConnectionFake) Close() error {
	fake.closeOnce.Do(func() {
		fake.closes.Add(1)
		close(fake.closed)
	})
	return nil
}

type gatewayAuthorityFake struct {
	mu      sync.Mutex
	calls   int
	failAt  int
	failErr error
	heads   []SandboxHeadFence
	docs    [][]DocumentFence
}

// gatewaySecurityNoop is an explicit test-only security boundary. Production
// construction has no implicit bypass and requires Redis admission, an exact
// editor lease, plus a durable audit sink.
type gatewaySecurityNoop struct{}

func (gatewaySecurityNoop) editorLeaseContract() GatewayEditorLeaseContract {
	return GatewayEditorLeaseContract{
		TTL: GatewayEditorLeaseTTL, HeartbeatInterval: GatewayEditorHeartbeatInterval,
	}
}

func (security gatewaySecurityNoop) Contract() GatewayEditorLeaseContract {
	return security.editorLeaseContract()
}

func (gatewaySecurityNoop) AllowGatewayRequest(
	context.Context,
	GatewayRequestRateLimitInput,
) (GatewayRequestRateLimitDecision, error) {
	return GatewayRequestRateLimitDecision{Allowed: true}, nil
}

func (gatewaySecurityNoop) AppendGatewayAudit(context.Context, GatewayAuditEvent) error {
	return nil
}

func (security gatewaySecurityNoop) AcquireGatewayEditorLease(
	context.Context,
	GatewayEditorLeaseInput,
) (GatewayEditorLeaseContract, bool, error) {
	return security.editorLeaseContract(), true, nil
}

func (gatewaySecurityNoop) RenewGatewayEditorLease(
	context.Context,
	GatewayEditorLeaseInput,
) (bool, error) {
	return true, nil
}

func (gatewaySecurityNoop) ReleaseGatewayEditorLease(
	context.Context,
	GatewayEditorLeaseInput,
) (bool, error) {
	return true, nil
}

func (fake *gatewayAuthorityFake) RevalidateGatewayFence(
	ctx context.Context,
	grant TicketGrant,
	head SandboxHeadFence,
	profile ProfileIdentity,
	documents []DocumentFence,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if head.Validate() != nil || profile.Validate() != nil ||
		grant.ProjectID != head.ProjectID || grant.SessionID != head.SessionID ||
		validateCurrentDocuments(head, documents) != nil {
		return ErrGatewayStale
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	fake.heads = append(fake.heads, head)
	fake.docs = append(fake.docs, slices.Clone(documents))
	if fake.failAt > 0 && fake.calls == fake.failAt {
		if fake.failErr != nil {
			return fake.failErr
		}
		return ErrGatewayStale
	}
	return nil
}

func (fake *gatewayAuthorityFake) callCount() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.calls
}

func (fake *gatewayAuthorityFake) snapshots() ([]SandboxHeadFence, [][]DocumentFence) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	heads := slices.Clone(fake.heads)
	documents := make([][]DocumentFence, len(fake.docs))
	for index := range fake.docs {
		documents[index] = slices.Clone(fake.docs[index])
	}
	return heads, documents
}

type gatewayFixture struct {
	head       SandboxHeadFence
	document   DocumentFence
	content    string
	profile    ProfileIdentity
	grant      TicketGrant
	bind       ClientBind
	projection RuntimeBindingProjection
	resolver   *gatewayBindingResolverFake
	process    *gatewayProcessFake
	runtime    *gatewayRuntimeFake
	connection *gatewayConnectionFake
	authority  *gatewayAuthorityFake
	security   GatewaySecurityBoundary
}

func newGatewayFixture(t *testing.T, mutateProfile func(*ProfileIdentity)) *gatewayFixture {
	t.Helper()
	head := validHead()
	content := "const answer = 42\n"
	uri, err := CandidateModelURI(head.ProjectID, head.CandidateID, "apps/web/page.ts")
	if err != nil {
		t.Fatal(err)
	}
	document := DocumentFence{
		ModelURI: uri, OpenID: testOpen, ModelVersion: 1,
		SavedContentHash: envelopeContentDigest(content),
	}
	profile := lspTestProfile("typescript")
	if mutateProfile != nil {
		mutateProfile(&profile)
		profile.EffectiveLimits = profile.Limits
		profile.ContentHash, err = templates.ComputeLanguageServerProfileContentHash(profile.LanguageServerProfile)
		if err != nil || profile.Validate() != nil {
			t.Fatalf("mutated profile invalid: %v", err)
		}
	}
	now := time.Now().UTC()
	grant := TicketGrant{
		SchemaVersion: TicketSchemaVersion, ID: testTicket, ProjectID: head.ProjectID,
		SessionID: head.SessionID, ActorID: testActor, Origin: "https://builder.example",
		Mode: TicketModeEditor, Head: head, TemplateRelease: profile.TemplateRelease,
		Profiles: []ProfileIdentity{profile}, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	bind := ClientBind{
		SchemaVersion: BindingSchemaVersion, Kind: "client.bind", ConnectionID: testConnection,
		BindingID: nil, Sequence: 1, Head: head, Profile: profile,
		Documents: []DocumentFence{document},
	}
	workspace := t.TempDir()
	projection := RuntimeBindingProjection{
		Head: head, Profile: profile, WorkspaceRoot: workspace, ServiceRoot: ".",
		ServicePath: workspace,
		Files: []RuntimeFileFence{{
			Path: "apps/web/page.ts", Mode: "100644",
			ContentHash: document.SavedContentHash, ByteSize: int64(len(content)),
		}},
		Documents: []RuntimeDocument{{
			Fence: document, Path: "apps/web/page.ts", Mode: "100644", Text: []byte(content),
		}},
	}
	process := newGatewayProcessFake(profile)
	return &gatewayFixture{
		head: head, document: document, content: content, profile: profile,
		grant: grant, bind: bind, projection: projection,
		resolver:   &gatewayBindingResolverFake{projection: projection},
		process:    process,
		runtime:    &gatewayRuntimeFake{process: process, starts: make(chan ContainerStartInput, 1)},
		connection: newGatewayConnectionFake(), authority: &gatewayAuthorityFake{},
		security: gatewaySecurityNoop{},
	}
}

func gatewayInitializeResponse(profile ProfileIdentity) []byte {
	return []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"hoverProvider":true},"serverInfo":{"name":%q,"version":%q}}}`,
		profile.ServerInfo.Name, profile.ServerInfo.Version,
	))
}

func gatewayInitializedServer(t *testing.T, profile ProfileIdentity) InitializedServer {
	t.Helper()
	methods := []string{"textDocument/hover"}
	hash, err := ComputeProductionV1CapabilityHash(methods)
	if err != nil {
		t.Fatal(err)
	}
	return InitializedServer{ServerInfo: profile.ServerInfo, Methods: methods, CapabilityHash: hash}
}

func sequenceGatewayIDs(values ...string) func() string {
	var mu sync.Mutex
	index := 0
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		if index >= len(values) {
			return "exhausted"
		}
		value := values[index]
		index++
		return value
	}
}

func startGatewayFixture(
	t *testing.T,
	fixture *gatewayFixture,
	ids ...string,
) (context.CancelFunc, <-chan error) {
	t.Helper()
	fixture.process.reads <- gatewayFrameResult{value: gatewayInitializeResponse(fixture.profile)}
	gateway, err := newGateway(
		fixture.resolver, fixture.runtime, fixture.authority, fixture.security,
		sequenceGatewayIDs(ids...), time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	completed := make(chan error, 1)
	go func() { completed <- gateway.Serve(ctx, fixture.connection, fixture.grant, fixture.bind) }()
	return cancel, completed
}

func gatewayClientEnvelopeJSON(
	t *testing.T,
	kind string,
	sequence uint64,
	head SandboxHeadFence,
	document *DocumentFence,
	payload any,
) []byte {
	t.Helper()
	value := clientEnvelopeJSON(t, kind, sequence, head, document, payload)
	return []byte(strings.Replace(string(value), envelopeBindingID, gatewayBindingID, 1))
}

func gatewayEnvelopeReplyTo(value string) *string {
	return &value
}

func receiveGatewayFrame(t *testing.T, values <-chan []byte, label string) []byte {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}

func requireGatewayMethod(t *testing.T, frame []byte, method string) map[string]json.RawMessage {
	t.Helper()
	fields, err := decodeServerTopObject(frame)
	if err != nil {
		t.Fatalf("%s frame malformed: %v\n%s", method, err, frame)
	}
	var got string
	if decodeString(fields["method"], &got) != nil || got != method {
		t.Fatalf("method = %q, want %q: %s", got, method, frame)
	}
	return fields
}

func waitGatewayDone(t *testing.T, cancel context.CancelFunc, completed <-chan error) error {
	t.Helper()
	cancel()
	select {
	case err := <-completed:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("Gateway did not stop")
		return nil
	}
}

func TestGatewayServesBoundReadOnlyLifecycleWithExactFences(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	cancel, completed := startGatewayFixture(
		t, fixture, gatewayBindingID, gatewayServerRequestID, gatewayServerMessageID,
	)

	start := <-fixture.runtime.starts
	if start.ConnectionID != testConnection || start.BindingID != gatewayBindingID ||
		start.WorkspaceRoot != fixture.projection.WorkspaceRoot || start.ServiceRoot != "." {
		t.Fatalf("runtime start = %#v", start)
	}
	initialize := receiveGatewayFrame(t, fixture.process.writes, "initialize")
	requireGatewayMethod(t, initialize, "initialize")
	initialized := receiveGatewayFrame(t, fixture.process.writes, "initialized")
	requireGatewayMethod(t, initialized, "initialized")
	bound := receiveGatewayFrame(t, fixture.connection.writes, "bound")
	if _, err := DecodeServerBound(bound, ServerBoundExpectation{
		ConnectionID: testConnection, BindingID: gatewayBindingID, Head: fixture.head,
		Profile: fixture.profile, Initialized: gatewayInitializedServer(t, fixture.profile),
		Documents: []DocumentFence{fixture.document},
	}); err != nil {
		t.Fatalf("bound envelope = %v\n%s", err, bound)
	}

	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentOpen, 2, fixture.head, &fixture.document,
		DocumentOpenEnvelopePayload{LanguageID: "typescript", Text: fixture.content},
	)}
	didOpen := receiveGatewayFrame(t, fixture.process.writes, "didOpen")
	openFields := requireGatewayMethod(t, didOpen, "textDocument/didOpen")
	var opened struct {
		TextDocument struct {
			URI  string `json:"uri"`
			Text string `json:"text"`
		} `json:"textDocument"`
	}
	wantServerURI, err := ServerDocumentURI(fixture.document.ModelURI, fixture.head)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(openFields["params"], &opened) != nil ||
		opened.TextDocument.URI != wantServerURI || opened.TextDocument.Text != fixture.content {
		t.Fatalf("didOpen did not use exact authoritative content: %s", didOpen)
	}

	params := map[string]any{
		"textDocument": map[string]any{"uri": fixture.document.ModelURI},
		"position":     map[string]any{"line": 0, "character": 6},
	}
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeRequest, 3, fixture.head, &fixture.document,
		map[string]any{
			"requestId": gatewayBrowserRequestID, "method": "textDocument/hover", "params": params,
		},
	)}
	request := receiveGatewayFrame(t, fixture.process.writes, "hover request")
	requestFields := requireGatewayMethod(t, request, "textDocument/hover")
	var serverID string
	if decodeString(requestFields["id"], &serverID) != nil || serverID != gatewayServerRequestID {
		t.Fatalf("server request ID = %q: %s", serverID, request)
	}
	var serverRequest struct {
		TextDocument BrowserTextDocumentIdentifier `json:"textDocument"`
	}
	if json.Unmarshal(requestFields["params"], &serverRequest) != nil ||
		serverRequest.TextDocument.URI != wantServerURI ||
		bytes.Contains(requestFields["params"], []byte(CandidateModelScheme+":")) {
		t.Fatalf("server request leaked browser Candidate URI: %s", request)
	}
	fixture.process.reads <- gatewayFrameResult{value: []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%q,"result":{"contents":{"kind":"plaintext","value":"safe hover"}}}`,
		gatewayServerRequestID,
	))}
	response := receiveGatewayFrame(t, fixture.connection.writes, "hover response")
	decoded, err := DecodeServerEnvelope(response, ServerEnvelopeExpectation{
		ConnectionID: testConnection, BindingID: gatewayBindingID, ExpectedSequence: 2,
		MessageID: gatewayServerMessageID, ReplyTo: gatewayEnvelopeReplyTo(gatewayBrowserRequestID),
		Head: fixture.head, Document: &fixture.document, Method: "textDocument/hover",
		Limits: fixture.profile.EffectiveLimits,
	})
	if err != nil || bytes.Contains(decoded.Payload, []byte(gatewayServerRequestID)) ||
		!bytes.Contains(decoded.Payload, []byte("safe hover")) {
		t.Fatalf("filtered response = %#v, %v\n%s", decoded, err, response)
	}
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentClose, 4, fixture.head, &fixture.document, EmptyEnvelopePayload{},
	)}
	didClose := receiveGatewayFrame(t, fixture.process.writes, "didClose")
	closeFields := requireGatewayMethod(t, didClose, "textDocument/didClose")
	var closed struct {
		TextDocument BrowserTextDocumentIdentifier `json:"textDocument"`
	}
	if json.Unmarshal(closeFields["params"], &closed) != nil || closed.TextDocument.URI != wantServerURI {
		t.Fatalf("didClose leaked browser Candidate URI: %s", didClose)
	}

	if err := waitGatewayDone(t, cancel, completed); err != nil {
		t.Fatalf("Serve() close = %v", err)
	}
	if fixture.authority.callCount() < 10 || fixture.process.terminates.Load() != 1 ||
		fixture.connection.closes.Load() != 1 || fixture.runtime.ready.Load() != 1 {
		t.Fatalf("lifecycle: authority=%d terminate=%d close=%d ready=%d",
			fixture.authority.callCount(), fixture.process.terminates.Load(),
			fixture.connection.closes.Load(), fixture.runtime.ready.Load())
	}
}

func TestGatewayStaleServerResultNeverCrossesAsResponse(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	cancel, completed := startGatewayFixture(
		t, fixture, gatewayBindingID, gatewayServerRequestID, gatewayServerMessageID,
	)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentOpen, 2, fixture.head, &fixture.document,
		DocumentOpenEnvelopePayload{LanguageID: "typescript", Text: fixture.content},
	)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "didOpen")
	params := map[string]any{
		"textDocument": map[string]any{"uri": fixture.document.ModelURI},
		"position":     map[string]any{"line": 0, "character": 0},
	}
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeRequest, 3, fixture.head, &fixture.document,
		map[string]any{
			"requestId": gatewayBrowserRequestID, "method": "textDocument/hover", "params": params,
		},
	)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "request")
	changed := fixture.document
	changed.ModelVersion++
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentChange, 4, fixture.head, &changed,
		DocumentChangeEnvelopePayload{Text: "const answer = 43\n"},
	)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "didChange")
	fixture.process.reads <- gatewayFrameResult{value: []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%q,"result":{"contents":"must-not-cross"}}`,
		gatewayServerRequestID,
	))}
	stale := receiveGatewayFrame(t, fixture.connection.writes, "stale")
	decoded, err := DecodeServerEnvelope(stale, ServerEnvelopeExpectation{
		ConnectionID: testConnection, BindingID: gatewayBindingID, ExpectedSequence: 2,
		MessageID: gatewayServerMessageID, ReplyTo: gatewayEnvelopeReplyTo(gatewayBrowserRequestID),
		Head: fixture.head, Document: &fixture.document, Method: "textDocument/hover",
		Limits: fixture.profile.EffectiveLimits,
	})
	if err != nil || decoded.Kind != ServerEnvelopeStale || bytes.Contains(stale, []byte("must-not-cross")) {
		t.Fatalf("stale result crossed boundary: %#v, %v\n%s", decoded, err, stale)
	}
	if err := waitGatewayDone(t, cancel, completed); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayCancelCleansPendingAndDropsLateServerResponse(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{}
	fixture.security = recorder
	cancel, completed := startGatewayFixture(t, fixture, gatewayBindingID, gatewayServerRequestID)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentOpen, 2, fixture.head, &fixture.document,
		DocumentOpenEnvelopePayload{LanguageID: "typescript", Text: fixture.content},
	)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "didOpen")
	params := map[string]any{
		"textDocument": map[string]any{"uri": fixture.document.ModelURI},
		"position":     map[string]any{"line": 0, "character": 0},
	}
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeRequest, 3, fixture.head, &fixture.document,
		map[string]any{
			"requestId": gatewayBrowserRequestID, "method": "textDocument/hover", "params": params,
		},
	)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "request")
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeCancel, 4, fixture.head, &fixture.document,
		CancelEnvelopePayload{ReplyTo: gatewayBrowserRequestID},
	)}
	cancelFrame := receiveGatewayFrame(t, fixture.process.writes, "cancel")
	requireGatewayMethod(t, cancelFrame, "$/cancelRequest")
	fixture.process.reads <- gatewayFrameResult{value: []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%q,"result":{"contents":"late"}}`, gatewayServerRequestID,
	))}
	select {
	case value := <-fixture.connection.writes:
		t.Fatalf("late canceled response crossed browser boundary: %s", value)
	case <-time.After(50 * time.Millisecond):
	}
	if err := waitGatewayDone(t, cancel, completed); err != nil {
		t.Fatal(err)
	}
	_, events := recorder.snapshot()
	if violations := gatewayServerViolationEvents(events); len(violations) != 0 {
		t.Fatalf("late canceled response was audited as malformed: %#v", violations)
	}
}

func TestGatewayRequestTimeoutCancelsServerAndEmitsOnlyStale(t *testing.T) {
	fixture := newGatewayFixture(t, func(profile *ProfileIdentity) {
		profile.Limits.RequestTimeoutMillis = 25
	})
	cancel, completed := startGatewayFixture(
		t, fixture, gatewayBindingID, gatewayServerRequestID, gatewayServerMessageID,
	)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentOpen, 2, fixture.head, &fixture.document,
		DocumentOpenEnvelopePayload{LanguageID: "typescript", Text: fixture.content},
	)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "didOpen")
	params := map[string]any{
		"textDocument": map[string]any{"uri": fixture.document.ModelURI},
		"position":     map[string]any{"line": 0, "character": 0},
	}
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeRequest, 3, fixture.head, &fixture.document,
		map[string]any{
			"requestId": gatewayBrowserRequestID, "method": "textDocument/hover", "params": params,
		},
	)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "request")
	cancelFrame := receiveGatewayFrame(t, fixture.process.writes, "timeout cancel")
	requireGatewayMethod(t, cancelFrame, "$/cancelRequest")
	stale := receiveGatewayFrame(t, fixture.connection.writes, "timeout stale")
	decoded, err := DecodeServerEnvelope(stale, ServerEnvelopeExpectation{
		ConnectionID: testConnection, BindingID: gatewayBindingID, ExpectedSequence: 2,
		MessageID: gatewayServerMessageID, ReplyTo: gatewayEnvelopeReplyTo(gatewayBrowserRequestID),
		Head: fixture.head, Document: &fixture.document, Method: "textDocument/hover",
		Limits: fixture.profile.EffectiveLimits,
	})
	if err != nil || decoded.Kind != ServerEnvelopeStale {
		t.Fatalf("timeout stale = %#v, %v\n%s", decoded, err, stale)
	}
	if err := waitGatewayDone(t, cancel, completed); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayAtomicHeadRebindCarriesSavedHashThroughOpeningAndClosingAuthority(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	cancel, completed := startGatewayFixture(t, fixture, gatewayBindingID, gatewayServerMessageID)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentOpen, 2, fixture.head, &fixture.document,
		DocumentOpenEnvelopePayload{LanguageID: "typescript", Text: fixture.content},
	)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "didOpen")
	changedText := "const answer = 43\n"
	changed := fixture.document
	changed.ModelVersion++
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentChange, 3, fixture.head, &changed,
		DocumentChangeEnvelopePayload{Text: changedText},
	)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "didChange")
	nextDocument := changed
	nextDocument.SavedContentHash = envelopeContentDigest(changedText)
	nextHead := fixture.head
	nextHead.Version++
	nextHead.JournalSequence++
	nextHead.TreeHash = lspDigest("9")
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeHeadRebind, 4, nextHead, nil,
		HeadRebindEnvelopePayload{Documents: []DocumentFence{nextDocument}},
	)}
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopePing, 5, nextHead, nil, PingEnvelopePayload{Nonce: "after-save"},
	)}
	pong := receiveGatewayFrame(t, fixture.connection.writes, "post-rebind pong")
	if _, err := DecodeServerEnvelope(pong, ServerEnvelopeExpectation{
		ConnectionID: testConnection, BindingID: gatewayBindingID, ExpectedSequence: 2,
		MessageID: gatewayServerMessageID, ReplyTo: gatewayEnvelopeReplyTo(clientEnvelopeMessageID(5)),
		Method: EnvelopeMethodPong, Head: nextHead, Nonce: "after-save",
		Limits: fixture.profile.EffectiveLimits,
	}); err != nil {
		t.Fatalf("post-rebind pong = %v\n%s", err, pong)
	}
	heads, documents := fixture.authority.snapshots()
	foundPair := false
	for index := 1; index < len(heads); index++ {
		if heads[index-1].Equal(nextHead) && heads[index].Equal(nextHead) &&
			len(documents[index-1]) == 1 && len(documents[index]) == 1 &&
			documents[index-1][0].Equal(nextDocument) && documents[index][0].Equal(nextDocument) {
			foundPair = true
			break
		}
	}
	if !foundPair {
		t.Fatalf("atomic rebind lacked proposed opening/closing authority: heads=%#v docs=%#v", heads, documents)
	}
	select {
	case frame := <-fixture.process.writes:
		t.Fatalf("headRebind emitted forbidden didSave/runtime frame: %s", frame)
	default:
	}
	if err := waitGatewayDone(t, cancel, completed); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRejectsDocumentSaveUnderOldHead(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	_, completed := startGatewayFixture(t, fixture, gatewayBindingID)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentOpen, 2, fixture.head, &fixture.document,
		DocumentOpenEnvelopePayload{LanguageID: "typescript", Text: fixture.content},
	)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "didOpen")
	changedText := "const answer = 43\n"
	changed := fixture.document
	changed.ModelVersion++
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentChange, 3, fixture.head, &changed,
		DocumentChangeEnvelopePayload{Text: changedText},
	)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "didChange")
	savedUnderOldHead := changed
	savedUnderOldHead.SavedContentHash = envelopeContentDigest(changedText)
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentSave, 4, fixture.head, &savedUnderOldHead, EmptyEnvelopePayload{},
	)}
	select {
	case err := <-completed:
		if !errors.Is(err, ErrGatewayStale) {
			t.Fatalf("old-head document.save = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old-head document.save did not fail closed")
	}
	select {
	case frame := <-fixture.process.writes:
		t.Fatalf("old-head document.save reached runtime: %s", frame)
	default:
	}
	if fixture.process.terminates.Load() != 1 || fixture.connection.closes.Load() != 1 {
		t.Fatalf("save rejection terminate=%d close=%d", fixture.process.terminates.Load(), fixture.connection.closes.Load())
	}
}

func TestGatewayClosingAuthorityCheckPrecedesRuntimeSideEffect(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	fixture.authority.failAt = 4 // bound opening/closing, then document.open opening/closing
	_, completed := startGatewayFixture(t, fixture, gatewayBindingID)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentOpen, 2, fixture.head, &fixture.document,
		DocumentOpenEnvelopePayload{LanguageID: "typescript", Text: fixture.content},
	)}
	select {
	case err := <-completed:
		if !errors.Is(err, ErrGatewayStale) {
			t.Fatalf("closing authority error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Gateway did not fail closed")
	}
	select {
	case value := <-fixture.process.writes:
		t.Fatalf("runtime side effect preceded closing authority: %s", value)
	default:
	}
	if fixture.process.terminates.Load() != 1 || fixture.connection.closes.Load() != 1 {
		t.Fatalf("failed close terminate=%d close=%d", fixture.process.terminates.Load(), fixture.connection.closes.Load())
	}
}

func TestGatewayInitializationTimeoutClosesAndTerminates(t *testing.T) {
	fixture := newGatewayFixture(t, func(profile *ProfileIdentity) {
		profile.Limits.StartupTimeoutMillis = 25
	})
	gateway, err := newGateway(
		fixture.resolver, fixture.runtime, fixture.authority, gatewaySecurityNoop{},
		sequenceGatewayIDs(gatewayBindingID), time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		completed <- gateway.Serve(context.Background(), fixture.connection, fixture.grant, fixture.bind)
	}()
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	select {
	case err := <-completed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("initialization timeout = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initialization did not time out")
	}
	if fixture.process.terminates.Load() != 1 || fixture.connection.closes.Load() != 1 {
		t.Fatalf("timeout terminate=%d close=%d", fixture.process.terminates.Load(), fixture.connection.closes.Load())
	}
}
