package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/goldenfault"
)

func TestGoldenFaultHandlerWritesExactCanonicalReceipt(t *testing.T) {
	principal := testGoldenFaultPrincipal()
	command := goldenfault.ConsumeCommand{AuthorityID: uuid.New(), FixtureID: principal.FixtureID, RunID: principal.RunID}
	record := testGoldenFaultTerminalRecord(t, command, true, goldenfault.OperationAgentSecurityCanary, goldenfault.AdapterOutcomeRefused)
	authenticator := &fakeGoldenFaultAuthenticator{principal: principal}
	consumer := &fakeGoldenFaultConsumer{record: record}
	router := goldenFaultTransportRouter(t, authenticator, consumer)

	response := performGoldenFaultRequest(router, command, `{"fixtureId":"`+command.FixtureID.String()+`","runId":"`+
		command.RunID.String()+`","schemaVersion":"`+goldenfault.ConsumeRequestSchemaV1+`"}`, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Idempotent-Replay") != "true" {
		t.Fatalf("headers = %#v", response.Header())
	}
	if !bytes.Equal(response.Body.Bytes(), record.Terminal.ReceiptJSON) {
		t.Fatalf("response body drifted from canonical receipt\n got: %s\nwant: %s", response.Body.Bytes(), record.Terminal.ReceiptJSON)
	}
	if authenticator.token != "opaque-fault-token" || consumer.calls != 1 || consumer.command != command ||
		consumer.principal != principal {
		t.Fatalf("auth token=%q calls=%d command=%+v principal=%+v", authenticator.token, consumer.calls, consumer.command, consumer.principal)
	}
}

func TestGoldenFaultHandlerRejectsExtraDuplicateNullAndCommandFields(t *testing.T) {
	principal := testGoldenFaultPrincipal()
	command := goldenfault.ConsumeCommand{AuthorityID: uuid.New(), FixtureID: principal.FixtureID, RunID: principal.RunID}
	authenticator := &fakeGoldenFaultAuthenticator{principal: principal}
	consumer := &fakeGoldenFaultConsumer{}
	router := goldenFaultTransportRouter(t, authenticator, consumer)
	valid := `{"fixtureId":"` + command.FixtureID.String() + `","runId":"` + command.RunID.String() + `","schemaVersion":"` + goldenfault.ConsumeRequestSchemaV1 + `"}`
	tests := []string{
		strings.TrimSuffix(valid, "}") + `,"operationKind":"agent-runner-crash"}`,
		strings.TrimSuffix(valid, "}") + `,"expectedFenceDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
		strings.TrimSuffix(valid, "}") + `,"url":"https://forbidden.example"}`,
		strings.Replace(valid, `"runId":"`+command.RunID.String()+`"`, `"runId":"`+command.RunID.String()+`","runId":"`+command.RunID.String()+`"`, 1),
		strings.Replace(valid, `"fixtureId":"`+command.FixtureID.String()+`"`, `"fixtureId":null`, 1),
	}
	for index, body := range tests {
		response := performGoldenFaultRequest(router, command, body, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("case %d status = %d, body = %s", index, response.Code, response.Body.String())
		}
	}
	if consumer.calls != 0 {
		t.Fatalf("malformed requests reached consumer %d times", consumer.calls)
	}
}

func TestGoldenFaultHandlerRequiresDedicatedDirectBearer(t *testing.T) {
	principal := testGoldenFaultPrincipal()
	command := goldenfault.ConsumeCommand{AuthorityID: uuid.New(), FixtureID: principal.FixtureID, RunID: principal.RunID}
	body := `{"fixtureId":"` + command.FixtureID.String() + `","runId":"` + command.RunID.String() + `","schemaVersion":"` + goldenfault.ConsumeRequestSchemaV1 + `"}`
	tests := []struct {
		name    string
		headers map[string]string
		authErr error
	}{
		{name: "missing bearer", headers: map[string]string{"Authorization": ""}},
		{name: "cookie", headers: map[string]string{"Cookie": "session=ordinary-user"}},
		{name: "origin", headers: map[string]string{"Origin": "https://app.example.com"}},
		{name: "browser fetch", headers: map[string]string{"Sec-Fetch-Site": "same-origin"}},
		{name: "query", headers: map[string]string{"X-Test-Query": "true"}},
		{name: "invalid credential", authErr: errors.New("credential contains secret details")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticator := &fakeGoldenFaultAuthenticator{principal: principal, err: test.authErr}
			consumer := &fakeGoldenFaultConsumer{}
			router := goldenFaultTransportRouter(t, authenticator, consumer)
			headers := test.headers
			if headers == nil {
				headers = map[string]string{}
			}
			var response *httptest.ResponseRecorder
			if test.name == "query" {
				request := goldenFaultHTTPRequest(command, body)
				request.URL.RawQuery = "operation=agent-runner-crash"
				response = httptest.NewRecorder()
				router.ServeHTTP(response, request)
			} else {
				response = performGoldenFaultRequest(router, command, body, headers)
			}
			if response.Code != http.StatusBadRequest && response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "secret details") || consumer.calls != 0 {
				t.Fatalf("leaking response=%s consumer calls=%d", response.Body.String(), consumer.calls)
			}
		})
	}
}

func TestGoldenFaultHandlerMapsClosedServiceErrorsWithoutDetails(t *testing.T) {
	principal := testGoldenFaultPrincipal()
	command := goldenfault.ConsumeCommand{AuthorityID: uuid.New(), FixtureID: principal.FixtureID, RunID: principal.RunID}
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "role drift", err: goldenfault.ErrFaultCredentialForbidden, status: http.StatusForbidden, code: "golden_fault_forbidden"},
		{name: "unknown authority", err: goldenfault.ErrTrustedAuthorityNotFound, status: http.StatusNotFound, code: "golden_fault_authority_not_found"},
		{name: "unknown outcome", err: fmtGoldenFaultError(goldenfault.ErrOutcomeUnknown), status: http.StatusConflict, code: "golden_fault_outcome_unknown"},
		{name: "authority conflict", err: fmtGoldenFaultError(goldenfault.ErrConflict), status: http.StatusConflict, code: "golden_fault_authority_rejected"},
		{name: "adapter unavailable", err: fmtGoldenFaultError(goldenfault.ErrAdapterMissing), status: http.StatusServiceUnavailable, code: "golden_fault_service_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			consumer := &fakeGoldenFaultConsumer{err: test.err}
			router := goldenFaultTransportRouter(t, &fakeGoldenFaultAuthenticator{principal: principal}, consumer)
			body := `{"fixtureId":"` + command.FixtureID.String() + `","runId":"` + command.RunID.String() + `","schemaVersion":"` + goldenfault.ConsumeRequestSchemaV1 + `"}`
			response := performGoldenFaultRequest(router, command, body, nil)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) ||
				strings.Contains(response.Body.String(), "sensitive-adapter-detail") {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestNewGoldenFaultHandlerRejectsTypedNilDependencies(t *testing.T) {
	var authenticator *fakeGoldenFaultAuthenticator
	var consumer *fakeGoldenFaultConsumer
	if _, err := NewGoldenFaultHandler(GoldenFaultDependencies{Authenticator: authenticator, Consumer: &fakeGoldenFaultConsumer{}}); err == nil {
		t.Fatal("accepted typed-nil authenticator")
	}
	if _, err := NewGoldenFaultHandler(GoldenFaultDependencies{Authenticator: &fakeGoldenFaultAuthenticator{}, Consumer: consumer}); err == nil {
		t.Fatal("accepted typed-nil consumer")
	}
}

func TestGoldenFaultHandlerRejectsTypedNilRequestContext(t *testing.T) {
	principal := testGoldenFaultPrincipal()
	command := goldenfault.ConsumeCommand{AuthorityID: uuid.New(), FixtureID: principal.FixtureID, RunID: principal.RunID}
	authenticator := &fakeGoldenFaultAuthenticator{principal: principal}
	consumer := &fakeGoldenFaultConsumer{}
	router := goldenFaultTransportRouter(t, authenticator, consumer)
	body := `{"fixtureId":"` + command.FixtureID.String() + `","runId":"` + command.RunID.String() + `","schemaVersion":"` + goldenfault.ConsumeRequestSchemaV1 + `"}`
	request := goldenFaultHTTPRequest(command, body)
	var ctx *typedNilGoldenFaultHTTPContext
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || authenticator.calls != 0 || consumer.calls != 0 {
		t.Fatalf("status=%d auth calls=%d consumer calls=%d body=%s",
			response.Code, authenticator.calls, consumer.calls, response.Body.String())
	}
}

type fakeGoldenFaultAuthenticator struct {
	principal goldenfault.RunPrincipal
	err       error
	token     string
	calls     int
}

func (authenticator *fakeGoldenFaultAuthenticator) AuthenticateGoldenFaultCredential(
	_ context.Context,
	token string,
) (goldenfault.RunPrincipal, error) {
	authenticator.calls++
	authenticator.token = token
	return authenticator.principal, authenticator.err
}

type fakeGoldenFaultConsumer struct {
	record    goldenfault.ConsumeRecord
	err       error
	calls     int
	command   goldenfault.ConsumeCommand
	principal goldenfault.RunPrincipal
}

func (consumer *fakeGoldenFaultConsumer) Consume(
	_ context.Context,
	principal goldenfault.RunPrincipal,
	command goldenfault.ConsumeCommand,
) (goldenfault.ConsumeRecord, error) {
	consumer.calls++
	consumer.principal = principal
	consumer.command = command
	return consumer.record, consumer.err
}

func goldenFaultTransportRouter(
	t *testing.T,
	authenticator GoldenFaultCredentialAuthenticator,
	consumer GoldenFaultConsumeAPI,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler, err := NewGoldenFaultHandler(GoldenFaultDependencies{Authenticator: authenticator, Consumer: consumer})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	if err := RegisterGoldenFaultRoutes(router.Group("/v1"), handler); err != nil {
		t.Fatal(err)
	}
	return router
}

func performGoldenFaultRequest(
	router http.Handler,
	command goldenfault.ConsumeCommand,
	body string,
	headers map[string]string,
) *httptest.ResponseRecorder {
	request := goldenFaultHTTPRequest(command, body)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func goldenFaultHTTPRequest(command goldenfault.ConsumeCommand, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost,
		"/v1/qualification/golden-fault-authorities/"+command.AuthorityID.String()+"/consume", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer opaque-fault-token")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func testGoldenFaultPrincipal() goldenfault.RunPrincipal {
	return goldenfault.RunPrincipal{
		ActorID: uuid.New(), Audience: "urn:worksflow:golden-stack", FixtureID: uuid.New(), ProjectID: uuid.New(),
		Role: goldenfault.FaultOperatorRole, RunID: uuid.New(), TenantID: uuid.New(),
	}
}

func testGoldenFaultTerminalRecord(
	t *testing.T,
	command goldenfault.ConsumeCommand,
	idempotent bool,
	operation goldenfault.OperationKind,
	outcome goldenfault.AdapterOutcome,
) goldenfault.ConsumeRecord {
	t.Helper()
	issuedAt := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	reservedAt, completedAt := issuedAt.Add(time.Minute), issuedAt.Add(2*time.Minute)
	expiresAt := issuedAt.Add(10 * time.Minute)
	invocationID, resultID := uuid.New(), uuid.New()
	expectedFence := testGoldenFaultDigest("expected-fence")
	resolution := goldenfault.ResourceResolution{
		ResourceID: "agent-patch-policy/exact-1", HeadDigest: testGoldenFaultDigest("resolved-head"), FenceDigest: expectedFence,
	}
	resolutionDigest, err := goldenfault.ResourceResolutionDigest(command.AuthorityID, resolution)
	if err != nil {
		t.Fatal(err)
	}
	selector := "agent.patch-policy"
	if operation != goldenfault.OperationAgentSecurityCanary {
		selector = "agent.runner"
	}
	reservation := goldenfault.Reservation{
		AuthorityID: command.AuthorityID, FixtureID: command.FixtureID, RunID: command.RunID,
		OperationKind: operation, ResourceSelector: selector, ExpectedFenceDigest: expectedFence,
		EnvelopeDigest: testGoldenFaultDigest("envelope"), PayloadDigest: testGoldenFaultDigest("payload"),
		PredicateDigest: testGoldenFaultDigest("predicate"), AuthorityIssuedAt: issuedAt,
		AuthorityExpiresAt: expiresAt, SignerIdentities: []string{"fault-operator@golden.example"},
		ResolvedResourceID: resolution.ResourceID, ResolvedHeadDigest: resolution.HeadDigest,
		ResolvedFenceDigest: resolution.FenceDigest, ResolutionDigest: resolutionDigest,
		AdapterInvocationID: invocationID, ReservedAt: reservedAt,
	}
	receipt := goldenfault.ConsumeReceipt{
		AdapterInvocationID: invocationID, AdapterResultDigest: testGoldenFaultDigest("adapter-result"),
		AuthorityID: command.AuthorityID, CompletedAt: completedAt.Format("2006-01-02T15:04:05.000Z"),
		EnvelopeDigest: reservation.EnvelopeDigest, ExpectedFenceDigest: expectedFence,
		FixtureID: command.FixtureID, ObservedFenceDigest: testGoldenFaultDigest("observed-fence"),
		ObservedHeadDigest: testGoldenFaultDigest("observed-head"), OperationKind: operation, Outcome: outcome,
		PayloadDigest: reservation.PayloadDigest, PredicateDigest: reservation.PredicateDigest,
		ReservedAt: reservedAt.Format("2006-01-02T15:04:05.000Z"), ResolutionDigest: resolutionDigest,
		ResolvedFenceDigest: resolution.FenceDigest, ResolvedHeadDigest: resolution.HeadDigest,
		ResolvedResourceID: resolution.ResourceID, ResourceSelector: selector, ResultID: resultID,
		RunID: command.RunID, SchemaVersion: goldenfault.ReceiptSchemaV1,
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	terminal := goldenfault.TerminalResult{
		AuthorityID: command.AuthorityID, ResultID: resultID, Outcome: outcome,
		AdapterResultDigest: receipt.AdapterResultDigest, ObservedHeadDigest: receipt.ObservedHeadDigest,
		ObservedFenceDigest: receipt.ObservedFenceDigest, CompletedAt: completedAt,
		Receipt: receipt, ReceiptJSON: receiptJSON, ReceiptDigest: testGoldenFaultDigestBytes(receiptJSON),
	}
	return goldenfault.ConsumeRecord{
		State: goldenfault.ConsumeStateTerminal, Reservation: reservation, Terminal: &terminal, Idempotent: idempotent,
	}
}

func testGoldenFaultDigest(seed string) string { return testGoldenFaultDigestBytes([]byte(seed)) }

func testGoldenFaultDigestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func fmtGoldenFaultError(sentinel error) error {
	return errors.Join(sentinel, errors.New("sensitive-adapter-detail"))
}

type typedNilGoldenFaultHTTPContext struct{}

func (*typedNilGoldenFaultHTTPContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*typedNilGoldenFaultHTTPContext) Done() <-chan struct{}       { return nil }
func (*typedNilGoldenFaultHTTPContext) Err() error                  { return nil }
func (*typedNilGoldenFaultHTTPContext) Value(any) any               { return nil }

var (
	_ GoldenFaultCredentialAuthenticator = (*fakeGoldenFaultAuthenticator)(nil)
	_ GoldenFaultConsumeAPI              = (*fakeGoldenFaultConsumer)(nil)
	_ context.Context                    = (*typedNilGoldenFaultHTTPContext)(nil)
)
