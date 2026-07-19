package transport

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/goldenfault"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

const maximumGoldenFaultConsumeRequestBytes int64 = 4 << 10

// GoldenFaultCredentialAuthenticator verifies the platform-issued,
// audience-bound fault-operator Bearer credential. Implementations must not
// accept an ordinary browser session, owner/admin credential, or Reference
// application credential at this boundary.
type GoldenFaultCredentialAuthenticator interface {
	AuthenticateGoldenFaultCredential(context.Context, string) (goldenfault.RunPrincipal, error)
}

type GoldenFaultConsumeAPI interface {
	Consume(context.Context, goldenfault.RunPrincipal, goldenfault.ConsumeCommand) (goldenfault.ConsumeRecord, error)
}

type GoldenFaultDependencies struct {
	Authenticator GoldenFaultCredentialAuthenticator
	Consumer      GoldenFaultConsumeAPI
}

type GoldenFaultHandler struct {
	authenticator GoldenFaultCredentialAuthenticator
	consumer      GoldenFaultConsumeAPI
}

func NewGoldenFaultHandler(dependencies GoldenFaultDependencies) (*GoldenFaultHandler, error) {
	if nilInterface(dependencies.Authenticator) || nilInterface(dependencies.Consumer) {
		return nil, errors.New("Golden fault credential authenticator and consumer are required")
	}
	return &GoldenFaultHandler{
		authenticator: dependencies.Authenticator,
		consumer:      dependencies.Consumer,
	}, nil
}

// RegisterGoldenFaultRoutes is intentionally independent from the ordinary
// session-authenticated API group. The handler accepts only a dedicated
// run-scoped Bearer credential and is not registered at all when the complete
// qualification-only dependency set is absent.
func RegisterGoldenFaultRoutes(routes gin.IRoutes, handler *GoldenFaultHandler) error {
	if routes == nil || handler == nil || nilInterface(handler.authenticator) || nilInterface(handler.consumer) {
		return errors.New("Golden fault routes and configured handler are required")
	}
	routes.POST(
		"/qualification/golden-fault-authorities/:authorityId/consume",
		handler.consume,
	)
	return nil
}

func (handler *GoldenFaultHandler) consume(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	requestContext := c.Request.Context()
	if requestContext == nil || nilInterface(requestContext) {
		writeGoldenFaultProblem(c, http.StatusServiceUnavailable, "golden_fault_service_unavailable", "Golden fault service unavailable",
			"The trusted request context is not available.")
		return
	}
	if c.Request.URL.RawQuery != "" || c.GetHeader("Cookie") != "" || c.GetHeader("Origin") != "" ||
		c.GetHeader("Sec-Fetch-Site") != "" || c.GetHeader("Sec-Fetch-Mode") != "" ||
		c.GetHeader("Sec-Fetch-Dest") != "" || c.GetHeader("X-HTTP-Method-Override") != "" {
		writeGoldenFaultProblem(c, http.StatusBadRequest, "golden_fault_transport_invalid", "Invalid Golden fault request",
			"This endpoint accepts only a direct Bearer request without browser, cookie, query, or method-override metadata.")
		return
	}
	token, ok := goldenFaultBearer(c.Request)
	if !ok {
		writeGoldenFaultProblem(c, http.StatusUnauthorized, "golden_fault_authentication_required", "Authentication required",
			"A valid run-scoped fault-operator credential is required.")
		return
	}
	principal, err := handler.authenticator.AuthenticateGoldenFaultCredential(requestContext, token)
	if err != nil {
		writeGoldenFaultProblem(c, http.StatusUnauthorized, "golden_fault_authentication_required", "Authentication required",
			"A valid run-scoped fault-operator credential is required.")
		return
	}
	body, err := readGoldenFaultRequest(c)
	if err != nil {
		WriteJSONError(c, err)
		return
	}
	command, err := goldenfault.DecodeConsumeRequest(body, c.Param("authorityId"))
	if err != nil {
		writeGoldenFaultProblem(c, http.StatusBadRequest, "golden_fault_request_invalid", "Invalid Golden fault request",
			"The request must exactly match worksflow-golden-fault-consume-request/v1.")
		return
	}
	record, err := handler.consumer.Consume(requestContext, principal, command)
	if err != nil {
		writeGoldenFaultConsumeError(c, err)
		return
	}
	receipt, err := goldenfault.CanonicalConsumeReceipt(record)
	if err != nil {
		writeGoldenFaultProblem(c, http.StatusServiceUnavailable, "golden_fault_receipt_unavailable", "Golden fault receipt unavailable",
			"The immutable terminal receipt could not be verified.")
		return
	}
	if record.Idempotent {
		c.Header("X-Idempotent-Replay", "true")
	}
	c.Data(http.StatusOK, "application/json", receipt)
}

func readGoldenFaultRequest(c *gin.Context) ([]byte, error) {
	contentType := c.GetHeader("Content-Type")
	mediaType, parameters, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" || len(parameters) != 0 {
		return nil, errUnsupportedJSONType
	}
	if c.GetHeader("Content-Encoding") != "" {
		return nil, errUnsupportedJSONType
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maximumGoldenFaultConsumeRequestBytes)
	value, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	if len(value) == 0 {
		return nil, errEmptyJSONBody
	}
	return value, nil
}

func goldenFaultBearer(request *http.Request) (string, bool) {
	values := request.Header.Values("Authorization")
	if len(values) != 1 || len(values[0]) < len("Bearer ")+1 || len(values[0]) > 8192 ||
		!strings.EqualFold(values[0][:len("Bearer ")], "Bearer ") {
		return "", false
	}
	token := values[0][len("Bearer "):]
	if token == "" || strings.TrimSpace(token) != token {
		return "", false
	}
	for _, value := range token {
		if unicode.IsSpace(value) || unicode.IsControl(value) {
			return "", false
		}
	}
	return token, true
}

func writeGoldenFaultConsumeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, goldenfault.ErrFaultCredentialForbidden):
		writeGoldenFaultProblem(c, http.StatusForbidden, "golden_fault_forbidden", "Golden fault operation forbidden",
			"The credential does not bind the exact actor, tenant, project, run, fixture, audience, and fault-operator role.")
	case errors.Is(err, goldenfault.ErrTrustedAuthorityNotFound):
		writeGoldenFaultProblem(c, http.StatusNotFound, "golden_fault_authority_not_found", "Golden fault authority not found",
			"The requested immutable authority is unavailable.")
	case errors.Is(err, goldenfault.ErrOutcomeUnknown):
		writeGoldenFaultProblem(c, http.StatusConflict, "golden_fault_outcome_unknown", "Golden fault outcome is unknown",
			"Retry this same authority, run, and fixture to inspect its durable result; do not create another authority.")
	case errors.Is(err, goldenfault.ErrConflict), errors.Is(err, goldenfault.ErrInvalidAuthority),
		errors.Is(err, goldenfault.ErrUntrustedSigner):
		writeGoldenFaultProblem(c, http.StatusConflict, "golden_fault_authority_rejected", "Golden fault authority rejected",
			"The immutable authority or its one-shot ledger binding cannot be consumed.")
	case errors.Is(err, goldenfault.ErrTrustedAuthorityUnavailable), errors.Is(err, goldenfault.ErrAdapterMissing):
		writeGoldenFaultProblem(c, http.StatusServiceUnavailable, "golden_fault_service_unavailable", "Golden fault service unavailable",
			"The trusted authority, ledger, or typed fault adapter is not available.")
	default:
		writeGoldenFaultProblem(c, http.StatusServiceUnavailable, "golden_fault_service_unavailable", "Golden fault service unavailable",
			"The trusted authority, ledger, or typed fault adapter is not available.")
	}
}

func writeGoldenFaultProblem(c *gin.Context, status int, code, title, detail string) {
	problem.Write(c, problem.New(status, code, title, detail))
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ GoldenFaultConsumeAPI = (*goldenfault.Consumer)(nil)
