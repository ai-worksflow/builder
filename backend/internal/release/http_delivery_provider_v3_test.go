package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
)

const deliveryProviderTestToken = "0123456789abcdef0123456789abcdef"

func newDeliveryTLSTestProvider(
	t *testing.T,
	handler func(http.ResponseWriter, *http.Request, DeliveryControllerIdentity),
) (*HTTPDeliveryOperationProvider, DeliveryControllerIdentity) {
	t.Helper()
	var identity DeliveryControllerIdentity
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler(writer, request, identity)
	}))
	t.Cleanup(server.Close)
	identity = deliveryIdentityForTLSServer(t, server)
	return newDeliveryProviderForTLSServer(t, server, identity), identity
}

func newDeliveryProviderForTLSServer(
	t *testing.T,
	server *httptest.Server,
	identity DeliveryControllerIdentity,
) *HTTPDeliveryOperationProvider {
	t.Helper()
	provider, err := NewHTTPDeliveryOperationProvider(HTTPDeliveryOperationProviderConfig{
		BaseURL:          server.URL,
		BearerToken:      deliveryProviderTestToken,
		RequestTimeout:   time.Minute,
		MaxResponseBytes: 1 << 20,
		ExpectedIdentity: identity,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func acceptedDeliveryObservation(
	request DeliveryOperationRequest,
	identity DeliveryControllerIdentity,
	sequence uint64,
) DeliveryOperationObservation {
	return DeliveryOperationObservation{
		SchemaVersion: DeliveryOperationObservationSchema,
		Controller:    identity,
		OperationID:   request.OperationID,
		RequestHash:   request.RequestHash,
		State:         DeliveryRemoteAccepted,
		Sequence:      sequence,
		ObservedAt:    time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC),
	}
}

func encodedDeliveryObservation(t *testing.T, observation DeliveryOperationObservation) []byte {
	t.Helper()
	encoded, err := domain.CanonicalJSON(observation)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestHTTPDeliveryOperationProviderSubmitAndReconcileUseOneStableOperation(t *testing.T) {
	operation := deliveryOperationRequestFixture(t, DeliveryOperationPreview)
	wantSubmitBody, err := domain.CanonicalJSON(operation)
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	provider, _ := newDeliveryTLSTestProvider(t, func(writer http.ResponseWriter, request *http.Request, identity DeliveryControllerIdentity) {
		calls++
		if request.URL.Path != "/v3/delivery-operations/"+operation.OperationID {
			t.Errorf("unexpected operation path %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+deliveryProviderTestToken ||
			request.Header.Get("Accept") != "application/json" ||
			request.Header.Get("X-Worksflow-Request-Hash") != operation.RequestHash {
			t.Errorf("request authority headers were incomplete: %v", request.Header)
		}
		var body []byte
		if request.Body != nil {
			var readErr error
			body, readErr = io.ReadAll(request.Body)
			if readErr != nil {
				t.Error(readErr)
			}
		}
		if calls == 1 {
			if request.Method != http.MethodPut || request.Header.Get("Content-Type") != "application/json" ||
				request.Header.Get("Idempotency-Key") != operation.OperationID || !bytes.Equal(body, wantSubmitBody) {
				t.Errorf("submit did not use the stable exact request: method=%s headers=%v body=%s", request.Method, request.Header, body)
			}
		} else if request.Method != http.MethodGet || request.Header.Get("Idempotency-Key") != "" || len(body) != 0 {
			t.Errorf("reconcile request was not an exact GET: method=%s headers=%v body=%s", request.Method, request.Header, body)
		}
		observation := acceptedDeliveryObservation(operation, identity, uint64(calls))
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(encodedDeliveryObservation(t, observation))
	})

	submitted, err := provider.Submit(context.Background(), operation)
	if err != nil || submitted.OperationID != operation.OperationID || submitted.RequestHash != operation.RequestHash {
		t.Fatalf("submit observation=%+v err=%v", submitted, err)
	}
	reconciled, err := provider.Reconcile(context.Background(), operation)
	if err != nil || reconciled.OperationID != operation.OperationID || reconciled.Sequence != 2 {
		t.Fatalf("reconcile observation=%+v err=%v", reconciled, err)
	}
	if calls != 2 {
		t.Fatalf("unexpected operation request count %d", calls)
	}
}

func TestHTTPDeliveryOperationProviderReadinessRequiresExactPinnedIdentity(t *testing.T) {
	for name, mutate := range map[string]func(*DeliveryControllerIdentity){
		"valid":    func(*DeliveryControllerIdentity) {},
		"id":       func(value *DeliveryControllerIdentity) { value.ID = "other-controller" },
		"version":  func(value *DeliveryControllerIdentity) { value.Version = "other-version" },
		"protocol": func(value *DeliveryControllerIdentity) { value.Protocol = "worksflow.release-delivery/v2" },
		"key":      func(value *DeliveryControllerIdentity) { value.TrustKeyDigest = deliveryHashA },
	} {
		t.Run(name, func(t *testing.T) {
			provider, _ := newDeliveryTLSTestProvider(t, func(writer http.ResponseWriter, request *http.Request, identity DeliveryControllerIdentity) {
				if request.Method != http.MethodGet || request.URL.Path != "/v3/identity" ||
					request.Header.Get("Authorization") != "Bearer "+deliveryProviderTestToken {
					t.Errorf("unexpected readiness request: %s %s %v", request.Method, request.URL, request.Header)
				}
				servedIdentity := identity
				mutate(&servedIdentity)
				body, _ := json.Marshal(servedIdentity)
				_, _ = writer.Write(body)
			})
			err := provider.Readiness(context.Background())
			if name == "valid" && err != nil {
				t.Fatalf("exact readiness failed: %v", err)
			}
			if name != "valid" && err == nil {
				t.Fatalf("readiness accepted %s drift", name)
			}
		})
	}

	var handlerHits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		handlerHits.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	wrongPin := deliveryIdentityForTLSServer(t, server)
	wrongPin.TrustKeyDigest = deliveryHashB
	provider := newDeliveryProviderForTLSServer(t, server, wrongPin)
	if err := provider.Readiness(context.Background()); !errors.Is(err, ErrDeliveryControllerTrust) {
		t.Fatalf("wrong TLS SPKI pin error=%v, want ErrDeliveryControllerTrust", err)
	}
	if hits := handlerHits.Load(); hits != 0 {
		t.Fatalf("wrong TLS pin was checked after sending HTTP authority; handler hits=%d", hits)
	}
}

func TestHTTPDeliveryOperationProviderReadinessRejectsUnsafeJSON(t *testing.T) {
	for _, name := range []string{"truncated", "unknown", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			provider, _ := newDeliveryTLSTestProvider(t, func(writer http.ResponseWriter, _ *http.Request, identity DeliveryControllerIdentity) {
				var body string
				switch name {
				case "truncated":
					body = `{"schemaVersion":`
				case "unknown":
					body = `{"schemaVersion":"` + DeliveryControllerIdentitySchemaVersion + `","id":"delivery-controller","version":"2026.07.18+build.42","protocol":"` + DeliveryControllerProtocolV3 + `","trustKeyDigest":"` + identity.TrustKeyDigest + `","unexpected":true}`
				case "duplicate":
					body = `{"schemaVersion":"` + DeliveryControllerIdentitySchemaVersion + `","id":"delivery-controller","id":"other","version":"2026.07.18+build.42","protocol":"` + DeliveryControllerProtocolV3 + `","trustKeyDigest":"` + identity.TrustKeyDigest + `"}`
				}
				_, _ = writer.Write([]byte(body))
			})
			if err := provider.Readiness(context.Background()); err == nil {
				t.Fatalf("readiness accepted unsafe %s JSON", name)
			}
		})
	}
}

func TestHTTPDeliveryOperationProviderClassifiesTransportAndUncertainHTTPOutcomesAsUnknown(t *testing.T) {
	operation := deliveryOperationRequestFixture(t, DeliveryOperationPreview)
	for _, name := range []string{
		"timeout", "response EOF", "server error",
	} {
		t.Run(name, func(t *testing.T) {
			provider, _ := newDeliveryTLSTestProvider(t, func(writer http.ResponseWriter, _ *http.Request, identity DeliveryControllerIdentity) {
				switch name {
				case "timeout":
					time.Sleep(100 * time.Millisecond)
					_, _ = writer.Write([]byte(`{}`))
				case "response EOF":
					connection, buffered, err := writer.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_, _ = buffered.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\nContent-Type: application/json\r\n\r\n{")
					_ = buffered.Flush()
					_ = connection.Close()
				case "2xx truncated":
					_, _ = writer.Write([]byte(`{"schemaVersion":`))
				case "2xx unknown field":
					_, _ = writer.Write([]byte(`{"unexpected":true}`))
				case "2xx duplicate field":
					_, _ = writer.Write([]byte(`{"schemaVersion":"a","schemaVersion":"b"}`))
				case "2xx oversized":
					_, _ = writer.Write(bytes.Repeat([]byte("x"), (1<<20)+1))
				case "unknown remote state":
					observation := acceptedDeliveryObservation(operation, identity, 1)
					observation.State = DeliveryRemoteState("unknown")
					_, _ = writer.Write(encodedDeliveryObservation(t, observation))
				case "server error":
					writer.WriteHeader(http.StatusServiceUnavailable)
					_, _ = writer.Write([]byte(`{"error":"committed but reply unavailable"}`))
				}
			})
			ctx := context.Background()
			if name == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			if _, err := provider.Submit(ctx, operation); !errors.Is(err, ErrDeliveryOutcomeUnknown) {
				t.Fatalf("ambiguous outcome %q error=%v, want ErrDeliveryOutcomeUnknown", name, err)
			}
		})
	}
}

func TestHTTPDeliveryOperationProviderClassifiesCompleteInvalid2xxAsProtocolConflict(t *testing.T) {
	operation := deliveryOperationRequestFixture(t, DeliveryOperationPreview)
	for _, name := range []string{
		"2xx truncated", "2xx unknown field", "2xx duplicate field", "2xx oversized", "unknown remote state",
	} {
		t.Run(name, func(t *testing.T) {
			provider, _ := newDeliveryTLSTestProvider(t, func(writer http.ResponseWriter, _ *http.Request, identity DeliveryControllerIdentity) {
				switch name {
				case "2xx truncated":
					_, _ = writer.Write([]byte(`{"schemaVersion":`))
				case "2xx unknown field":
					_, _ = writer.Write([]byte(`{"unexpected":true}`))
				case "2xx duplicate field":
					_, _ = writer.Write([]byte(`{"schemaVersion":"a","schemaVersion":"b"}`))
				case "2xx oversized":
					_, _ = writer.Write(bytes.Repeat([]byte("x"), (1<<20)+1))
				case "unknown remote state":
					observation := acceptedDeliveryObservation(operation, identity, 1)
					observation.State = DeliveryRemoteState("unknown")
					_, _ = writer.Write(encodedDeliveryObservation(t, observation))
				}
			})
			if _, err := provider.Submit(context.Background(), operation); !errors.Is(err, ErrDeliveryControllerProtocol) {
				t.Fatalf("invalid authoritative outcome %q error=%v, want ErrDeliveryControllerProtocol", name, err)
			}
		})
	}
}

func TestHTTPDeliveryOperationProviderDistinguishesNotFoundAndConflict(t *testing.T) {
	operation := deliveryOperationRequestFixture(t, DeliveryOperationPreview)
	providerForStatus := func(status int) *HTTPDeliveryOperationProvider {
		provider, _ := newDeliveryTLSTestProvider(t, func(writer http.ResponseWriter, _ *http.Request, _ DeliveryControllerIdentity) {
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(`{}`))
		})
		return provider
	}

	if _, err := providerForStatus(http.StatusNotFound).Reconcile(context.Background(), operation); !errors.Is(err, ErrDeliveryOperationNotFound) {
		t.Fatalf("reconcile 404 error=%v, want ErrDeliveryOperationNotFound", err)
	}
	if _, err := providerForStatus(http.StatusNotFound).Submit(context.Background(), operation); !errors.Is(err, ErrDeliveryOutcomeUnknown) {
		t.Fatalf("submit 404 error=%v, want ErrDeliveryOutcomeUnknown", err)
	}
	if _, err := providerForStatus(http.StatusConflict).Submit(context.Background(), operation); !errors.Is(err, ErrDeliveryOperationConflict) {
		t.Fatalf("submit 409 error=%v, want ErrDeliveryOperationConflict", err)
	}
	if _, err := providerForStatus(http.StatusConflict).Reconcile(context.Background(), operation); !errors.Is(err, ErrDeliveryOperationConflict) {
		t.Fatalf("reconcile 409 error=%v, want ErrDeliveryOperationConflict", err)
	}
}

func TestHTTPDeliveryOperationProviderDoesNotFollowRedirectOrLeakCredentials(t *testing.T) {
	var targetHits atomic.Int32
	var targetAuthorization atomic.Value
	var source *httptest.Server
	source = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/credential-sink" {
			targetHits.Add(1)
			targetAuthorization.Store(request.Header.Get("Authorization"))
			writer.WriteHeader(http.StatusOK)
			return
		}
		writer.Header().Set("Location", source.URL+"/credential-sink")
		writer.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	identity := deliveryIdentityForTLSServer(t, source)
	provider := newDeliveryProviderForTLSServer(t, source, identity)
	operation := deliveryOperationRequestFixture(t, DeliveryOperationPreview)
	if _, err := provider.Submit(context.Background(), operation); !errors.Is(err, ErrDeliveryOutcomeUnknown) {
		t.Fatalf("redirect error=%v, want ErrDeliveryOutcomeUnknown", err)
	}
	if hits := targetHits.Load(); hits != 0 {
		t.Fatalf("redirect was followed %d times; leaked authorization=%q", hits, targetAuthorization.Load())
	}
}

func TestHTTPDeliveryOperationProviderReturnsExactTerminalProductionResult(t *testing.T) {
	operation := deliveryOperationRequestFixture(t, DeliveryOperationProduction)
	var result DeliveryOperationResult
	provider, _ := newDeliveryTLSTestProvider(t, func(writer http.ResponseWriter, _ *http.Request, identity DeliveryControllerIdentity) {
		result = completedDeliveryOperationResultFixture(t, operation)
		result.Controller = identity
		result = hashDeliveryOperationResult(t, result)
		observation := acceptedDeliveryObservation(operation, identity, 2)
		observation.State = DeliveryRemoteCompleted
		observation.Result = &result
		_, _ = writer.Write(encodedDeliveryObservation(t, observation))
	})
	parsed, err := provider.Reconcile(context.Background(), operation)
	if err != nil || parsed.Result == nil || parsed.Result.PreviousHead == nil ||
		*parsed.Result.PreviousHead != *result.PreviousHead ||
		parsed.Result.ResultHash != result.ResultHash {
		t.Fatalf("terminal production result=%+v err=%v", parsed, err)
	}

	provider, _ = newDeliveryTLSTestProvider(t, func(writer http.ResponseWriter, _ *http.Request, identity DeliveryControllerIdentity) {
		tamperedResult := completedDeliveryOperationResultFixture(t, operation)
		tamperedResult.Controller = identity
		tamperedResult = hashDeliveryOperationResult(t, tamperedResult)
		tamperedResult.PreviousHead.ContentHash = deliveryHashB
		tampered := acceptedDeliveryObservation(operation, identity, 2)
		tampered.State = DeliveryRemoteCompleted
		tampered.Result = &tamperedResult
		_, _ = writer.Write(encodedDeliveryObservation(t, tampered))
	})
	if _, err := provider.Reconcile(context.Background(), operation); !errors.Is(err, ErrDeliveryControllerProtocol) {
		t.Fatalf("tampered terminal result error=%v, want ErrDeliveryControllerProtocol", err)
	}
}

func deliveryIdentityForTLSServer(t *testing.T, server *httptest.Server) DeliveryControllerIdentity {
	t.Helper()
	if server == nil || server.Certificate() == nil {
		t.Fatal("TLS server certificate is unavailable")
	}
	digest := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	identity := deliveryControllerIdentityFixture()
	identity.TrustKeyDigest = "sha256:" + hex.EncodeToString(digest[:])
	return identity
}
