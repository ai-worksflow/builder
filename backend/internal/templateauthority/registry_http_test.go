package templateauthority

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	httpTestOrigin = "registry.example.com"
	httpTestCDN    = "cdn.example.com"
	httpTestAssets = "assets.example.com"
	httpTestRepo   = "worksflow/templates"
)

var (
	httpTestManifestDigest = "sha256:" + strings.Repeat("a", 64)
	httpTestBlobDigest     = "sha256:" + strings.Repeat("b", 64)
)

type registryRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip registryRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type staticRegistryResolver struct {
	mu        sync.Mutex
	addresses map[string][]net.IPAddr
	errors    map[string]error
	calls     []string
}

func newStaticRegistryResolver(hosts ...string) *staticRegistryResolver {
	resolver := &staticRegistryResolver{
		addresses: map[string][]net.IPAddr{},
		errors:    map[string]error{},
	}
	for _, host := range hosts {
		resolver.addresses[host] = []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}
	}
	return resolver
}

func (resolver *staticRegistryResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.calls = append(resolver.calls, host)
	if err := resolver.errors[host]; err != nil {
		return nil, err
	}
	addresses := resolver.addresses[host]
	return append([]net.IPAddr(nil), addresses...), nil
}

func (resolver *staticRegistryResolver) callSnapshot() []string {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return append([]string(nil), resolver.calls...)
}

type blockingRegistryResolver struct {
	started chan struct{}
	once    sync.Once
}

func (resolver *blockingRegistryResolver) LookupIPAddr(ctx context.Context, _ string) ([]net.IPAddr, error) {
	resolver.once.Do(func() { close(resolver.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

type observedBody struct {
	reader *bytes.Reader
	reads  atomic.Int32
	closed atomic.Bool
}

func newObservedBody(content string) *observedBody {
	return &observedBody{reader: bytes.NewReader([]byte(content))}
}

func (body *observedBody) Read(buffer []byte) (int, error) {
	body.reads.Add(1)
	return body.reader.Read(buffer)
}

func (body *observedBody) Close() error {
	body.closed.Store(true)
	return nil
}

func newRegistryHTTPTestClient(t *testing.T, resolver RegistryHTTPResolver, origins ...RegistryHTTPOrigin) *HTTPSRegistryClient {
	t.Helper()
	client, err := NewHTTPSRegistryClient(HTTPSRegistryClientConfig{
		Origins:  origins,
		Resolver: resolver,
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func httpTestReference(host string) ExactReference {
	return ExactReference{Host: host, Repository: httpTestRepo, Digest: httpTestManifestDigest}
}

func httpOK(body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}
}

func httpRedirect(status int, location string, body io.ReadCloser) *http.Response {
	header := make(http.Header)
	header.Set("Location", location)
	return &http.Response{StatusCode: status, Header: header, Body: body}
}

func httpUnauthorized(challenge string, body io.ReadCloser) *http.Response {
	header := make(http.Header)
	header.Set("WWW-Authenticate", challenge)
	return &http.Response{StatusCode: http.StatusUnauthorized, Header: header, Body: body}
}

func TestHTTPSRegistryClientBuildsDigestPinnedRequestsAndStreamsBodies(t *testing.T) {
	resolver := newStaticRegistryResolver(httpTestOrigin)
	client := newRegistryHTTPTestClient(t, resolver, RegistryHTTPOrigin{
		Host:          httpTestOrigin,
		Authorization: "Bearer server-owned-secret",
	})
	body := newObservedBody("manifest bytes are not buffered by the HTTP client")
	var requests []*http.Request
	client.httpClient.Transport = registryRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Clone(context.Background()))
		return httpOK(body), nil
	})

	read, err := client.FetchManifest(context.Background(), httpTestReference(httpTestOrigin))
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("got %d requests, want one", len(requests))
	}
	request := requests[0]
	if request.Method != http.MethodGet {
		t.Fatalf("method = %q, want GET", request.Method)
	}
	wantURL := "https://" + httpTestOrigin + "/v2/" + httpTestRepo + "/manifests/" + httpTestManifestDigest
	if request.URL.String() != wantURL {
		t.Fatalf("URL = %q, want %q", request.URL, wantURL)
	}
	if request.Header.Get("Accept") != MediaTypeOCIImageManifest {
		t.Fatalf("Accept = %q", request.Header.Get("Accept"))
	}
	if request.Header.Get("Authorization") != "Bearer server-owned-secret" {
		t.Fatal("server-owned Authorization header was not attached")
	}
	if read.ServingHost != httpTestOrigin || len(read.RedirectHosts) != 0 {
		t.Fatalf("unexpected registry read identity: %#v", read)
	}
	if body.reads.Load() != 0 || body.closed.Load() {
		t.Fatal("client consumed or closed a successful response body")
	}
	if err := read.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if !body.closed.Load() {
		t.Fatal("caller did not own the returned body")
	}
}

func TestHTTPSRegistryClientExchangesBasicCredentialForExactBearerScope(t *testing.T) {
	resolver := newStaticRegistryResolver(httpTestOrigin)
	client := newRegistryHTTPTestClient(t, resolver, RegistryHTTPOrigin{
		Host:          httpTestOrigin,
		Authorization: "Basic server-owned-secret",
	})
	manifestURL := "https://" + httpTestOrigin + "/v2/" + httpTestRepo + "/manifests/" + httpTestManifestDigest
	challenge := `Bearer realm="https://` + httpTestOrigin + `/token",service="` + httpTestOrigin + `",scope="repository:` + httpTestRepo + `:pull"`
	var calls int
	client.httpClient.Transport = registryRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if request.URL.String() != manifestURL || request.Header.Get("Authorization") != "" {
				t.Fatalf("unexpected initial registry request: %s authorization=%q", request.URL, request.Header.Get("Authorization"))
			}
			return httpUnauthorized(challenge, io.NopCloser(strings.NewReader("challenge"))), nil
		case 2:
			if request.URL.Scheme != "https" || request.URL.Host != httpTestOrigin || request.URL.Path != "/token" {
				t.Fatalf("unexpected token endpoint: %s", request.URL)
			}
			if request.URL.Query().Get("service") != httpTestOrigin || request.URL.Query().Get("scope") != "repository:"+httpTestRepo+":pull" {
				t.Fatalf("unexpected token query: %s", request.URL.RawQuery)
			}
			if request.Header.Get("Authorization") != "Basic server-owned-secret" || request.Header.Get("Accept") != "application/json" {
				t.Fatal("token request did not carry only the server-owned Basic credential and JSON accept header")
			}
			return httpOK(io.NopCloser(strings.NewReader(`{"token":"registry-access-token","expires_in":300,"issued_at":"2026-07-20T00:00:00Z"}`))), nil
		case 3:
			if request.URL.String() != manifestURL || request.Header.Get("Authorization") != "Bearer registry-access-token" {
				t.Fatalf("unexpected authenticated registry retry: %s authorization=%q", request.URL, request.Header.Get("Authorization"))
			}
			return httpOK(io.NopCloser(strings.NewReader("manifest"))), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	read, err := client.FetchManifest(context.Background(), httpTestReference(httpTestOrigin))
	if err != nil {
		t.Fatal(err)
	}
	defer read.Body.Close()
	if calls != 3 || read.ServingHost != httpTestOrigin {
		t.Fatalf("calls/read = %d/%#v", calls, read)
	}
}

func TestHTTPSRegistryClientRejectsBearerChallengePolicyEscapes(t *testing.T) {
	tests := []struct {
		name      string
		challenge string
	}{
		{
			name:      "cross-origin realm",
			challenge: `Bearer realm="https://auth.example.com/token",service="registry.example.com",scope="repository:worksflow/templates:pull"`,
		},
		{
			name:      "wrong repository scope",
			challenge: `Bearer realm="https://registry.example.com/token",service="registry.example.com",scope="repository:other/templates:pull"`,
		},
		{
			name:      "realm query injection",
			challenge: `Bearer realm="https://registry.example.com/token?account=attacker",service="registry.example.com"`,
		},
		{
			name:      "missing service",
			challenge: `Bearer realm="https://registry.example.com/token"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := newStaticRegistryResolver(httpTestOrigin, "auth.example.com")
			client := newRegistryHTTPTestClient(t, resolver, RegistryHTTPOrigin{
				Host: httpTestOrigin, Authorization: "Basic confidential-value",
			})
			var calls atomic.Int32
			client.httpClient.Transport = registryRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				if calls.Add(1) != 1 {
					t.Fatal("client sent a token request after challenge escaped policy")
				}
				return httpUnauthorized(test.challenge, io.NopCloser(strings.NewReader("challenge"))), nil
			})

			_, err := client.FetchManifest(context.Background(), httpTestReference(httpTestOrigin))
			if err == nil {
				t.Fatal("unsafe challenge was accepted")
			}
			if calls.Load() != 1 || strings.Contains(err.Error(), "confidential-value") {
				t.Fatalf("unsafe challenge calls/error = %d/%v", calls.Load(), err)
			}
		})
	}
}

func TestHTTPSRegistryClientRejectsInvalidBearerTokenResponse(t *testing.T) {
	resolver := newStaticRegistryResolver(httpTestOrigin)
	client := newRegistryHTTPTestClient(t, resolver, RegistryHTTPOrigin{
		Host: httpTestOrigin, Authorization: "Basic confidential-value",
	})
	challenge := `Bearer realm="https://registry.example.com/token",service="registry.example.com"`
	var calls atomic.Int32
	client.httpClient.Transport = registryRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		switch calls.Add(1) {
		case 1:
			return httpUnauthorized(challenge, io.NopCloser(strings.NewReader("challenge"))), nil
		case 2:
			return httpOK(io.NopCloser(strings.NewReader(`{"token":"first","access_token":"different"}`))), nil
		default:
			t.Fatal("invalid token response caused a registry retry")
			return nil, nil
		}
	})

	_, err := client.FetchManifest(context.Background(), httpTestReference(httpTestOrigin))
	requireCode(t, err, CodeRegistryFetchFailed)
	if calls.Load() != 2 || strings.Contains(err.Error(), "confidential-value") || strings.Contains(err.Error(), "different") {
		t.Fatalf("token response calls/error = %d/%v", calls.Load(), err)
	}
}

func TestHTTPSRegistryClientBuildsExactBlobGET(t *testing.T) {
	resolver := newStaticRegistryResolver(httpTestOrigin)
	client := newRegistryHTTPTestClient(t, resolver, RegistryHTTPOrigin{Host: httpTestOrigin})
	client.httpClient.Transport = registryRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		wantURL := "https://" + httpTestOrigin + "/v2/" + httpTestRepo + "/blobs/" + httpTestBlobDigest
		if request.Method != http.MethodGet || request.URL.String() != wantURL {
			t.Fatalf("unexpected blob request: %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Accept") != "application/octet-stream" {
			t.Fatalf("Accept = %q", request.Header.Get("Accept"))
		}
		return httpOK(io.NopCloser(strings.NewReader("blob"))), nil
	})

	read, err := client.FetchBlob(context.Background(), httpTestReference(httpTestOrigin), Descriptor{Digest: httpTestBlobDigest})
	if err != nil {
		t.Fatal(err)
	}
	defer read.Body.Close()
}

func TestHTTPSRegistryClientReadinessChecksEveryUniqueHostWithoutGET(t *testing.T) {
	resolver := newStaticRegistryResolver(httpTestOrigin, httpTestCDN, httpTestAssets)
	client := newRegistryHTTPTestClient(t, resolver,
		RegistryHTTPOrigin{
			Host:          httpTestOrigin,
			Authorization: "Bearer never-materialized",
			RedirectHosts: []string{httpTestCDN, httpTestAssets},
		},
		RegistryHTTPOrigin{Host: httpTestCDN, RedirectHosts: []string{httpTestAssets}},
	)
	client.httpClient.Transport = registryRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		t.Fatal("readiness must not send a business GET")
		return nil, nil
	})

	if err := client.Readiness(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{httpTestAssets, httpTestCDN, httpTestOrigin}
	if calls := resolver.callSnapshot(); !reflect.DeepEqual(calls, want) {
		t.Fatalf("resolved hosts = %#v, want %#v", calls, want)
	}
}

func TestHTTPSRegistryClientReadinessFailsOnDNSAndPrivateAddresses(t *testing.T) {
	t.Run("DNS failure", func(t *testing.T) {
		resolver := newStaticRegistryResolver(httpTestOrigin)
		resolver.errors[httpTestOrigin] = errors.New("resolver unavailable")
		client := newRegistryHTTPTestClient(t, resolver, RegistryHTTPOrigin{Host: httpTestOrigin})
		requireCode(t, client.Readiness(context.Background()), CodeRegistryFetchFailed)
	})
	t.Run("private redirect", func(t *testing.T) {
		resolver := newStaticRegistryResolver(httpTestOrigin, httpTestCDN)
		resolver.addresses[httpTestCDN] = []net.IPAddr{{IP: net.ParseIP("192.168.1.20")}}
		client := newRegistryHTTPTestClient(t, resolver, RegistryHTTPOrigin{Host: httpTestOrigin, RedirectHosts: []string{httpTestCDN}})
		requireCode(t, client.Readiness(context.Background()), CodePolicyDenied)
	})
}

func TestHTTPSRegistryClientReadinessHonorsCancellation(t *testing.T) {
	resolver := &blockingRegistryResolver{started: make(chan struct{})}
	client := newRegistryHTTPTestClient(t, resolver, RegistryHTTPOrigin{Host: httpTestOrigin})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- client.Readiness(ctx) }()
	<-resolver.started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled readiness check did not return")
	}
}

func TestHTTPSRegistryClientUsesHTTPSWithRealTLSRoundTrip(t *testing.T) {
	var sawTLS atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.TLS == nil || request.Host != "example.com" {
			t.Errorf("request was not an exact TLS origin request: TLS=%v Host=%q", request.TLS != nil, request.Host)
		}
		sawTLS.Store(true)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("manifest"))
	}))
	defer server.Close()

	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	resolver := newStaticRegistryResolver("example.com")
	client, err := NewHTTPSRegistryClient(HTTPSRegistryClientConfig{
		Origins:  []RegistryHTTPOrigin{{Host: "example.com"}},
		Resolver: resolver,
		RootCAs:  pool,
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := client.httpClient.Transport.(*http.Transport)
	serverAddress := server.Listener.Addr().String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, serverAddress)
	}

	read, err := client.FetchManifest(context.Background(), httpTestReference("example.com"))
	if err != nil {
		t.Fatal(err)
	}
	defer read.Body.Close()
	if !sawTLS.Load() {
		t.Fatal("TLS handler was not reached")
	}
}

func TestHTTPSRegistryClientReportsRedirectChainWithoutCredentialLeakage(t *testing.T) {
	resolver := newStaticRegistryResolver(httpTestOrigin, httpTestCDN, httpTestAssets)
	client := newRegistryHTTPTestClient(t, resolver,
		RegistryHTTPOrigin{
			Host:          httpTestOrigin,
			Authorization: "Bearer origin-secret",
			RedirectHosts: []string{httpTestCDN, httpTestAssets},
		},
		RegistryHTTPOrigin{Host: httpTestCDN, Authorization: "Bearer unrelated-cdn-secret"},
	)
	var hosts []string
	client.httpClient.Transport = registryRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		hosts = append(hosts, request.URL.Host)
		switch request.URL.Host {
		case httpTestOrigin:
			if request.Header.Get("Authorization") != "Bearer origin-secret" {
				t.Fatal("origin credential is missing")
			}
			return httpRedirect(http.StatusTemporaryRedirect, "https://"+httpTestCDN+"/objects/manifest", io.NopCloser(strings.NewReader("redirect"))), nil
		case httpTestCDN:
			if request.Header.Get("Authorization") != "" {
				t.Fatalf("credential leaked to first redirect: %q", request.Header.Get("Authorization"))
			}
			return httpRedirect(http.StatusPermanentRedirect, "https://"+httpTestAssets+"/objects/manifest", io.NopCloser(strings.NewReader("redirect"))), nil
		case httpTestAssets:
			if request.Header.Get("Authorization") != "" {
				t.Fatalf("credential leaked to final redirect: %q", request.Header.Get("Authorization"))
			}
			return httpOK(io.NopCloser(strings.NewReader("manifest"))), nil
		default:
			t.Fatalf("unexpected host %q", request.URL.Host)
			return nil, nil
		}
	})

	read, err := client.FetchManifest(context.Background(), httpTestReference(httpTestOrigin))
	if err != nil {
		t.Fatal(err)
	}
	defer read.Body.Close()
	if read.ServingHost != httpTestAssets {
		t.Fatalf("ServingHost = %q", read.ServingHost)
	}
	wantRedirects := []string{httpTestCDN, httpTestAssets}
	if !reflect.DeepEqual(read.RedirectHosts, wantRedirects) {
		t.Fatalf("RedirectHosts = %#v, want %#v", read.RedirectHosts, wantRedirects)
	}
	wantHosts := []string{httpTestOrigin, httpTestCDN, httpTestAssets}
	if !reflect.DeepEqual(hosts, wantHosts) {
		t.Fatalf("request hosts = %#v, want %#v", hosts, wantHosts)
	}
}

func TestHTTPSRegistryClientRejectsRedirectEscapesBeforeSendingNextHop(t *testing.T) {
	tests := []struct {
		name     string
		location string
		prepare  func(*staticRegistryResolver)
	}{
		{name: "plain HTTP", location: "http://" + httpTestCDN + "/object"},
		{name: "userinfo", location: "https://user@" + httpTestCDN + "/object"},
		{name: "same-origin query", location: "https://" + httpTestOrigin + "/object?token=attacker"},
		{name: "fragment", location: "https://" + httpTestCDN + "/object#part"},
		{name: "port", location: "https://" + httpTestCDN + ":444/object"},
		{name: "unallowlisted host", location: "https://escape.example.com/object"},
		{
			name:     "private DNS result",
			location: "https://" + httpTestCDN + "/object",
			prepare: func(resolver *staticRegistryResolver) {
				resolver.addresses[httpTestCDN] = []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := newStaticRegistryResolver(httpTestOrigin, httpTestCDN, "escape.example.com")
			if test.prepare != nil {
				test.prepare(resolver)
			}
			client := newRegistryHTTPTestClient(t, resolver, RegistryHTTPOrigin{
				Host:          httpTestOrigin,
				Authorization: "Bearer must-not-leak",
				RedirectHosts: []string{httpTestCDN},
			})
			var calls atomic.Int32
			client.httpClient.Transport = registryRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				if calls.Add(1) != 1 {
					t.Fatal("client sent a request after the redirect escaped policy")
				}
				return httpRedirect(http.StatusFound, test.location, io.NopCloser(strings.NewReader("redirect"))), nil
			})

			_, err := client.FetchManifest(context.Background(), httpTestReference(httpTestOrigin))
			requireCode(t, err, CodePolicyDenied)
			if calls.Load() != 1 {
				t.Fatalf("sent %d requests, want one", calls.Load())
			}
			if strings.Contains(err.Error(), "must-not-leak") {
				t.Fatal("credential appeared in an error")
			}
		})
	}
}

func TestHTTPSRegistryClientAllowsSignedQueryOnlyOnAllowlistedCredentialFreeContentRedirect(t *testing.T) {
	resolver := newStaticRegistryResolver(httpTestOrigin, httpTestCDN)
	client := newRegistryHTTPTestClient(t, resolver, RegistryHTTPOrigin{
		Host: httpTestOrigin, Authorization: "Bearer must-not-leak",
		RedirectHosts: []string{httpTestCDN},
	})
	var calls atomic.Int32
	client.httpClient.Transport = registryRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch calls.Add(1) {
		case 1:
			if request.URL.Host != httpTestOrigin || request.Header.Get("Authorization") != "Bearer must-not-leak" {
				t.Fatal("origin request did not carry the configured credential")
			}
			return httpRedirect(
				http.StatusTemporaryRedirect,
				"https://"+httpTestCDN+"/objects/manifest?signature=registry-issued&expires=1784544000",
				io.NopCloser(strings.NewReader("redirect")),
			), nil
		case 2:
			if request.URL.Host != httpTestCDN || request.URL.Query().Get("signature") != "registry-issued" ||
				request.URL.Query().Get("expires") != "1784544000" {
				t.Fatalf("signed content redirect was not preserved: %s", request.URL)
			}
			if request.Header.Get("Authorization") != "" {
				t.Fatal("origin credential leaked to signed content redirect")
			}
			return httpOK(io.NopCloser(strings.NewReader("manifest"))), nil
		default:
			t.Fatalf("unexpected request %d", calls.Load())
			return nil, nil
		}
	})

	read, err := client.FetchManifest(context.Background(), httpTestReference(httpTestOrigin))
	if err != nil {
		t.Fatal(err)
	}
	defer read.Body.Close()
	if read.ServingHost != httpTestCDN || calls.Load() != 2 {
		t.Fatalf("signed redirect result = %#v, calls=%d", read, calls.Load())
	}
}

func TestHTTPSRegistryClientRejectsPrivateOriginBeforeTransport(t *testing.T) {
	resolver := newStaticRegistryResolver(httpTestOrigin)
	resolver.addresses[httpTestOrigin] = []net.IPAddr{{IP: net.ParseIP("10.0.0.8")}}
	client := newRegistryHTTPTestClient(t, resolver, RegistryHTTPOrigin{Host: httpTestOrigin})
	var called atomic.Bool
	client.httpClient.Transport = registryRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		called.Store(true)
		return nil, errors.New("must not be called")
	})

	_, err := client.FetchManifest(context.Background(), httpTestReference(httpTestOrigin))
	requireCode(t, err, CodePolicyDenied)
	if called.Load() {
		t.Fatal("transport was invoked for a private resolution")
	}
}

func TestHTTPSRegistryClientClosesNonSuccessResponseAndSanitizesError(t *testing.T) {
	resolver := newStaticRegistryResolver(httpTestOrigin)
	client := newRegistryHTTPTestClient(t, resolver, RegistryHTTPOrigin{
		Host:          httpTestOrigin,
		Authorization: "Bearer confidential-value",
	})
	body := newObservedBody("server error")
	client.httpClient.Transport = registryRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header), Body: body}, nil
	})

	_, err := client.FetchManifest(context.Background(), httpTestReference(httpTestOrigin))
	requireCode(t, err, CodeRegistryFetchFailed)
	if !body.closed.Load() {
		t.Fatal("non-success response body was not closed")
	}
	if body.reads.Load() != 0 {
		t.Fatal("client should not consume an untrusted status body")
	}
	if strings.Contains(err.Error(), "confidential-value") {
		t.Fatal("credential appeared in an error")
	}
}

func TestHTTPSRegistryClientHonorsCallerCancellation(t *testing.T) {
	resolver := newStaticRegistryResolver(httpTestOrigin)
	client := newRegistryHTTPTestClient(t, resolver, RegistryHTTPOrigin{Host: httpTestOrigin})
	started := make(chan struct{})
	client.httpClient.Transport = registryRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.FetchManifest(ctx, httpTestReference(httpTestOrigin))
		result <- err
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled fetch did not return")
	}
}

func TestHTTPSRegistryClientEnforcesConfiguredTimeout(t *testing.T) {
	resolver := newStaticRegistryResolver(httpTestOrigin)
	client, err := NewHTTPSRegistryClient(HTTPSRegistryClientConfig{
		Origins:  []RegistryHTTPOrigin{{Host: httpTestOrigin}},
		Resolver: resolver,
		Timeout:  20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient.Transport = registryRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})

	_, err = client.FetchManifest(context.Background(), httpTestReference(httpTestOrigin))
	requireCode(t, err, CodeTimeout)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestHTTPSRegistryClientEnforcesRedirectLimit(t *testing.T) {
	resolver := newStaticRegistryResolver(httpTestOrigin)
	client, err := NewHTTPSRegistryClient(HTTPSRegistryClientConfig{
		Origins:      []RegistryHTTPOrigin{{Host: httpTestOrigin}},
		Resolver:     resolver,
		Timeout:      time.Second,
		MaxRedirects: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	client.httpClient.Transport = registryRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return httpRedirect(http.StatusTemporaryRedirect, "/another", io.NopCloser(strings.NewReader("redirect"))), nil
	})

	_, err = client.FetchManifest(context.Background(), httpTestReference(httpTestOrigin))
	requireCode(t, err, CodeLimitExceeded)
	if calls.Load() != 2 {
		t.Fatalf("sent %d requests, want two", calls.Load())
	}
}

func TestHTTPSRegistryClientConfigurationIsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		config HTTPSRegistryClientConfig
	}{
		{name: "no origins", config: HTTPSRegistryClientConfig{}},
		{name: "literal origin", config: HTTPSRegistryClientConfig{Origins: []RegistryHTTPOrigin{{Host: "127.0.0.1"}}}},
		{name: "local origin", config: HTTPSRegistryClientConfig{Origins: []RegistryHTTPOrigin{{Host: "registry.local"}}}},
		{name: "origin port", config: HTTPSRegistryClientConfig{Origins: []RegistryHTTPOrigin{{Host: "registry.example.com:443"}}}},
		{name: "invalid credential", config: HTTPSRegistryClientConfig{Origins: []RegistryHTTPOrigin{{Host: httpTestOrigin, Authorization: "Bearer secret\r\nInjected: yes"}}}},
		{name: "duplicate redirect", config: HTTPSRegistryClientConfig{Origins: []RegistryHTTPOrigin{{Host: httpTestOrigin, RedirectHosts: []string{httpTestCDN, httpTestCDN}}}}},
		{name: "too many redirects", config: HTTPSRegistryClientConfig{Origins: []RegistryHTTPOrigin{{Host: httpTestOrigin}}, MaxRedirects: 11}},
		{name: "negative timeout", config: HTTPSRegistryClientConfig{Origins: []RegistryHTTPOrigin{{Host: httpTestOrigin}}, Timeout: -time.Second}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewHTTPSRegistryClient(test.config)
			requireCode(t, err, CodeInvalidConfiguration)
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("invalid credential appeared in configuration error")
			}
		})
	}
}

func TestHTTPSRegistryClientRejectsInvalidDirectReferences(t *testing.T) {
	resolver := newStaticRegistryResolver(httpTestOrigin)
	client := newRegistryHTTPTestClient(t, resolver, RegistryHTTPOrigin{Host: httpTestOrigin})
	invalid := []ExactReference{
		{Host: httpTestOrigin, Repository: httpTestRepo, Digest: "sha256:short"},
		{Host: httpTestOrigin, Repository: "../escape", Digest: httpTestManifestDigest},
		{Host: "127.0.0.1", Repository: httpTestRepo, Digest: httpTestManifestDigest},
	}
	for _, reference := range invalid {
		_, err := client.FetchManifest(context.Background(), reference)
		requireCode(t, err, CodeInvalidReference)
	}
	_, err := client.FetchBlob(context.Background(), httpTestReference(httpTestOrigin), Descriptor{Digest: "sha256:short"})
	requireCode(t, err, CodeInvalidReference)
}

func TestHTTPSRegistryTransportRequiresPublicPort443Address(t *testing.T) {
	resolver := newStaticRegistryResolver(httpTestOrigin)
	resolver.addresses[httpTestOrigin] = []net.IPAddr{{IP: net.ParseIP("100.64.0.1")}}
	dial := safeRegistryDialContext(resolver, &net.Dialer{Timeout: time.Millisecond})
	_, err := dial(context.Background(), "tcp", httpTestOrigin+":443")
	requireCode(t, err, CodePolicyDenied)

	resolver.addresses[httpTestOrigin] = []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}
	_, err = dial(context.Background(), "tcp", httpTestOrigin+":80")
	requireCode(t, err, CodePolicyDenied)
}
