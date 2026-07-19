package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
)

var (
	ErrDeliveryOutcomeUnknown     = errors.New("release delivery controller outcome is unknown")
	ErrDeliveryOperationNotFound  = errors.New("release delivery controller operation was not found")
	ErrDeliveryOperationConflict  = errors.New("release delivery controller operation conflicts with the exact request")
	ErrDeliveryControllerProtocol = errors.New("release delivery controller returned an invalid authoritative response")
	ErrDeliveryControllerTrust    = errors.New("release delivery controller TLS trust key does not match")
	errDeliveryResponseOversized  = errors.New("release delivery controller response exceeded its bound")
)

type DeliveryProviderError struct {
	Kind       error
	Operation  string
	HTTPStatus int
	Cause      error
}

func (err *DeliveryProviderError) Error() string {
	if err == nil {
		return "release delivery provider failed"
	}
	detail := err.Operation
	if err.HTTPStatus != 0 {
		detail += fmt.Sprintf(" (HTTP %d)", err.HTTPStatus)
	}
	if detail == "" {
		detail = "release delivery provider"
	}
	return detail + ": " + err.Kind.Error()
}

func (err *DeliveryProviderError) Unwrap() []error {
	if err == nil {
		return nil
	}
	if err.Cause == nil {
		return []error{err.Kind}
	}
	return []error{err.Kind, err.Cause}
}

type DeliveryOperationProvider interface {
	Submit(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error)
	Reconcile(context.Context, DeliveryOperationRequest) (DeliveryOperationObservation, error)
	Readiness(context.Context) error
}

type HTTPDeliveryOperationProviderConfig struct {
	BaseURL          string
	BearerToken      string
	RequestTimeout   time.Duration
	MaxResponseBytes int64
	ExpectedIdentity DeliveryControllerIdentity
}

// HTTPDeliveryOperationProvider uses client-chosen stable operation IDs. PUT
// may be retried only with the same request hash; GET observes the same remote
// operation. A transport failure is always OutcomeUnknown and can never be
// interpreted as proof that the controller did not mutate state. A complete
// 2xx response that violates the pinned protocol is authoritative evidence of
// controller drift and must be quarantined instead of retried forever.
type HTTPDeliveryOperationProvider struct {
	baseURL          *url.URL
	bearerToken      string
	client           *http.Client
	maxResponse      int64
	expectedIdentity DeliveryControllerIdentity
}

func NewHTTPDeliveryOperationProvider(
	config HTTPDeliveryOperationProviderConfig,
	client *http.Client,
) (*HTTPDeliveryOperationProvider, error) {
	parsed, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("release delivery v3 controller URL must be an absolute HTTPS origin")
	}
	token := strings.TrimSpace(config.BearerToken)
	if len(token) < 32 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("release delivery v3 controller bearer token is invalid")
	}
	identity, err := ParseDeliveryControllerIdentity(config.ExpectedIdentity)
	if err != nil {
		return nil, err
	}
	if config.RequestTimeout < time.Second || config.RequestTimeout > 5*time.Minute ||
		config.MaxResponseBytes < 1024 || config.MaxResponseBytes > 8<<20 {
		return nil, errors.New("release delivery v3 controller timeout or response limit is invalid")
	}
	baseTransport := http.DefaultTransport
	if client != nil && client.Transport != nil {
		baseTransport = client.Transport
	}
	httpTransport, ok := baseTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("release delivery v3 controller requires a dedicated HTTP transport for pre-request TLS pinning")
	}
	transport := httpTransport.Clone()
	tlsConfig := &tls.Config{}
	if transport.TLSClientConfig != nil {
		if transport.TLSClientConfig.InsecureSkipVerify {
			return nil, errors.New("release delivery v3 controller cannot disable TLS certificate verification")
		}
		tlsConfig = transport.TLSClientConfig.Clone()
	}
	if tlsConfig.MinVersion < tls.VersionTLS12 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	previousVerifyConnection := tlsConfig.VerifyConnection
	expectedTrustKeyDigest := identity.TrustKeyDigest
	tlsConfig.VerifyConnection = func(connection tls.ConnectionState) error {
		if previousVerifyConnection != nil {
			if err := previousVerifyConnection(connection); err != nil {
				return err
			}
		}
		return verifyDeliveryControllerConnection(connection, expectedTrustKeyDigest)
	}
	transport.TLSClientConfig = tlsConfig
	if client == nil {
		client = &http.Client{Transport: transport, Timeout: config.RequestTimeout}
	} else {
		copy := *client
		copy.Transport = transport
		if copy.Timeout == 0 || copy.Timeout > config.RequestTimeout {
			copy.Timeout = config.RequestTimeout
		}
		client = &copy
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPDeliveryOperationProvider{
		baseURL: parsed, bearerToken: token, client: client,
		maxResponse: config.MaxResponseBytes, expectedIdentity: identity,
	}, nil
}

func (provider *HTTPDeliveryOperationProvider) Readiness(ctx context.Context) error {
	if provider == nil || provider.baseURL == nil || provider.client == nil || ctx == nil {
		return errors.New("release delivery v3 provider is not configured")
	}
	endpoint := provider.endpoint("/v3/identity")
	request, err := provider.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := provider.client.Do(request)
	if err != nil {
		return fmt.Errorf("query release delivery controller identity: %w", err)
	}
	if err := provider.verifyControllerTLS(response); err != nil {
		closeDeliveryHTTPResponse(response)
		return err
	}
	body, status, err := provider.readResponse(response)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("release delivery controller identity returned HTTP %d", status)
	}
	var identity DeliveryControllerIdentity
	if err := decodeReleaseStrictJSON(body, &identity); err != nil {
		return fmt.Errorf("decode release delivery controller identity: %w", err)
	}
	parsed, err := ParseDeliveryControllerIdentity(identity)
	if err != nil || parsed != provider.expectedIdentity {
		return errors.New("release delivery controller identity does not match the configured id, version, protocol, and trust key")
	}
	return nil
}

func (provider *HTTPDeliveryOperationProvider) Submit(
	ctx context.Context,
	request DeliveryOperationRequest,
) (DeliveryOperationObservation, error) {
	request, err := ParseDeliveryOperationRequest(request)
	if err != nil {
		return DeliveryOperationObservation{}, err
	}
	body, err := domain.CanonicalJSON(request)
	if err != nil {
		return DeliveryOperationObservation{}, err
	}
	endpoint := provider.endpoint("/v3/delivery-operations/" + url.PathEscape(request.OperationID))
	httpRequest, err := provider.newRequest(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return DeliveryOperationObservation{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Idempotency-Key", request.OperationID)
	httpRequest.Header.Set("X-Worksflow-Request-Hash", request.RequestHash)
	return provider.executeOperation(httpRequest, "submit release delivery operation", request, false)
}

func (provider *HTTPDeliveryOperationProvider) Reconcile(
	ctx context.Context,
	request DeliveryOperationRequest,
) (DeliveryOperationObservation, error) {
	request, err := ParseDeliveryOperationRequest(request)
	if err != nil {
		return DeliveryOperationObservation{}, err
	}
	endpoint := provider.endpoint("/v3/delivery-operations/" + url.PathEscape(request.OperationID))
	httpRequest, err := provider.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return DeliveryOperationObservation{}, err
	}
	httpRequest.Header.Set("X-Worksflow-Request-Hash", request.RequestHash)
	return provider.executeOperation(httpRequest, "reconcile release delivery operation", request, true)
}

func (provider *HTTPDeliveryOperationProvider) executeOperation(
	request *http.Request,
	operation string,
	expected DeliveryOperationRequest,
	reconcile bool,
) (DeliveryOperationObservation, error) {
	if provider == nil || provider.client == nil || request == nil {
		return DeliveryOperationObservation{}, errors.New("release delivery v3 provider is not configured")
	}
	response, err := provider.client.Do(request)
	if err != nil {
		return DeliveryOperationObservation{}, &DeliveryProviderError{
			Kind: ErrDeliveryOutcomeUnknown, Operation: operation, Cause: err,
		}
	}
	if err := provider.verifyControllerTLS(response); err != nil {
		closeDeliveryHTTPResponse(response)
		return DeliveryOperationObservation{}, &DeliveryProviderError{
			Kind: ErrDeliveryControllerTrust, Operation: operation, Cause: err,
		}
	}
	body, status, readErr := provider.readResponse(response)
	if readErr != nil {
		kind := ErrDeliveryOutcomeUnknown
		if errors.Is(readErr, errDeliveryResponseOversized) {
			kind = ErrDeliveryControllerProtocol
		}
		return DeliveryOperationObservation{}, &DeliveryProviderError{
			Kind: kind, Operation: operation, HTTPStatus: status, Cause: readErr,
		}
	}
	if reconcile && status == http.StatusNotFound {
		return DeliveryOperationObservation{}, &DeliveryProviderError{
			Kind: ErrDeliveryOperationNotFound, Operation: operation, HTTPStatus: status,
		}
	}
	if status == http.StatusConflict {
		return DeliveryOperationObservation{}, &DeliveryProviderError{
			Kind: ErrDeliveryOperationConflict, Operation: operation, HTTPStatus: status,
		}
	}
	if status < 200 || status >= 300 {
		return DeliveryOperationObservation{}, &DeliveryProviderError{
			Kind: ErrDeliveryOutcomeUnknown, Operation: operation, HTTPStatus: status,
		}
	}
	var observation DeliveryOperationObservation
	if err := decodeReleaseStrictJSON(body, &observation); err != nil {
		return DeliveryOperationObservation{}, &DeliveryProviderError{
			Kind: ErrDeliveryControllerProtocol, Operation: operation, HTTPStatus: status, Cause: err,
		}
	}
	parsed, err := ParseDeliveryOperationObservation(observation, provider.expectedIdentity, expected)
	if err != nil {
		return DeliveryOperationObservation{}, &DeliveryProviderError{
			Kind: ErrDeliveryControllerProtocol, Operation: operation, HTTPStatus: status, Cause: err,
		}
	}
	return parsed, nil
}

func (provider *HTTPDeliveryOperationProvider) newRequest(
	ctx context.Context,
	method string,
	endpoint *url.URL,
	body io.Reader,
) (*http.Request, error) {
	if ctx == nil {
		return nil, errors.New("release delivery controller context is required")
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+provider.bearerToken)
	request.Header.Set("Accept", "application/json")
	return request, nil
}

func (provider *HTTPDeliveryOperationProvider) endpoint(path string) *url.URL {
	endpoint := *provider.baseURL
	endpoint.Path = path
	return &endpoint
}

func (provider *HTTPDeliveryOperationProvider) readResponse(response *http.Response) ([]byte, int, error) {
	if response == nil || response.Body == nil {
		return nil, 0, errors.New("release delivery controller returned no response body")
	}
	status := response.StatusCode
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, provider.maxResponse+1))
	if err != nil {
		return nil, status, err
	}
	if int64(len(body)) > provider.maxResponse {
		return nil, status, errDeliveryResponseOversized
	}
	return body, status, nil
}

// TrustKeyDigest is the SHA-256 digest of the leaf TLS certificate's
// SubjectPublicKeyInfo. Normal PKI verification still runs first; this exact
// pin prevents a controller or proxy from satisfying readiness by merely
// echoing the configured identity fields.
func (provider *HTTPDeliveryOperationProvider) verifyControllerTLS(response *http.Response) error {
	if response == nil || response.Request == nil || response.Request.URL == nil || response.Request.URL.Scheme != "https" ||
		response.TLS == nil || !response.TLS.HandshakeComplete || len(response.TLS.PeerCertificates) == 0 {
		return ErrDeliveryControllerTrust
	}
	return verifyDeliveryControllerConnection(*response.TLS, provider.expectedIdentity.TrustKeyDigest)
}

func verifyDeliveryControllerConnection(connection tls.ConnectionState, expectedDigest string) error {
	// VerifyConnection runs before crypto/tls publishes HandshakeComplete on
	// the eventual response.  At this point the normal PKI verification has
	// already populated VerifiedChains, which is the authority signal needed
	// for the pre-request SPKI check.  The response-side defense in depth still
	// requires HandshakeComplete.
	if len(connection.PeerCertificates) == 0 || len(connection.VerifiedChains) == 0 {
		return ErrDeliveryControllerTrust
	}
	digest := sha256.Sum256(connection.PeerCertificates[0].RawSubjectPublicKeyInfo)
	actual := "sha256:" + hex.EncodeToString(digest[:])
	if len(actual) != len(expectedDigest) || subtle.ConstantTimeCompare([]byte(actual), []byte(expectedDigest)) != 1 {
		return ErrDeliveryControllerTrust
	}
	return nil
}

func closeDeliveryHTTPResponse(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
}

var _ DeliveryOperationProvider = (*HTTPDeliveryOperationProvider)(nil)
