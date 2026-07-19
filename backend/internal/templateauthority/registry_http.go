package templateauthority

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultRegistryHTTPTimeout      = 2 * time.Minute
	defaultRegistryHTTPMaxRedirects = 4
	maxRegistryHTTPRedirects        = 10
	maxRegistryHTTPHeaderBytes      = 1 << 20
)

var nonPublicRegistryPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),   // shared address space
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("192.88.99.0/24"),  // deprecated 6to4 relay anycast
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use translation
	netip.MustParsePrefix("100::/64"),        // discard-only
	netip.MustParsePrefix("2001:2::/48"),     // benchmarking
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("fec0::/10"),       // deprecated site-local
}

// RegistryHTTPResolver is the DNS boundary used by HTTPSRegistryClient. The
// production default is net.DefaultResolver; the interface exists so callers
// can use their platform resolver without weakening the address checks.
type RegistryHTTPResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// RegistryHTTPOrigin is one server-owned registry origin. Authorization is
// never copied to a redirect host. RedirectHosts is an exact per-origin
// allowlist; wildcards and suffix matching are intentionally unsupported.
type RegistryHTTPOrigin struct {
	Host          string
	Authorization string
	RedirectHosts []string
}

// HTTPSRegistryClientConfig configures the production OCI registry reader.
// RootCAs may add a private registry CA. Insecure TLS modes are not exposed.
type HTTPSRegistryClientConfig struct {
	Origins      []RegistryHTTPOrigin
	Resolver     RegistryHTTPResolver
	RootCAs      *x509.CertPool
	Timeout      time.Duration
	MaxRedirects int
}

type registryHTTPOrigin struct {
	authorization string
	redirectHosts map[string]struct{}
}

// HTTPSRegistryClient is a fail-closed RegistryClient. It constructs only
// digest-pinned HTTPS requests, follows redirects itself, and reports every
// redirect hop to OCIVerifier.
type HTTPSRegistryClient struct {
	httpClient   *http.Client
	resolver     RegistryHTTPResolver
	origins      map[string]registryHTTPOrigin
	maxRedirects int
	timeout      time.Duration
}

// NewHTTPSRegistryClient creates a production-safe OCI registry client.
func NewHTTPSRegistryClient(config HTTPSRegistryClientConfig) (*HTTPSRegistryClient, error) {
	resolver := config.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultRegistryHTTPTimeout
	}
	if timeout < time.Millisecond {
		return nil, registryHTTPFailure(CodeInvalidConfiguration, "configure registry client", "timeout", "must be at least one millisecond", nil)
	}
	maxRedirects := config.MaxRedirects
	if maxRedirects == 0 {
		maxRedirects = defaultRegistryHTTPMaxRedirects
	}
	if maxRedirects < 1 || maxRedirects > maxRegistryHTTPRedirects {
		return nil, registryHTTPFailure(CodeInvalidConfiguration, "configure registry client", "maxRedirects", "must be between 1 and 10", nil)
	}

	origins, err := normalizeRegistryHTTPOrigins(config.Origins)
	if err != nil {
		return nil, err
	}
	transport := newRegistryHTTPTransport(resolver, config.RootCAs, timeout)
	client := &HTTPSRegistryClient{
		resolver:     resolver,
		origins:      origins,
		maxRedirects: maxRedirects,
		timeout:      timeout,
	}
	client.httpClient = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return client, nil
}

// FetchManifest performs GET against the exact OCI manifest digest and sends
// the OCI manifest Accept header. The returned body belongs to the caller.
func (client *HTTPSRegistryClient) FetchManifest(ctx context.Context, reference ExactReference) (RegistryRead, error) {
	target, err := registryManifestURL(reference)
	if err != nil {
		return RegistryRead{}, err
	}
	return client.fetch(ctx, target, reference.Host, MediaTypeOCIImageManifest)
}

// FetchBlob performs GET against the descriptor's exact digest. The returned
// body is streamed; OCIVerifier remains responsible for size and digest bounds.
func (client *HTTPSRegistryClient) FetchBlob(ctx context.Context, repository ExactReference, descriptor Descriptor) (RegistryRead, error) {
	target, err := registryBlobURL(repository, descriptor)
	if err != nil {
		return RegistryRead{}, err
	}
	return client.fetch(ctx, target, repository.Host, "application/octet-stream")
}

// Readiness proves that every configured origin and redirect host currently
// resolves only to public addresses. It sends no registry object request and
// never materializes an Authorization header.
func (client *HTTPSRegistryClient) Readiness(ctx context.Context) error {
	if client == nil || client.httpClient == nil || client.httpClient.Transport == nil || client.resolver == nil || len(client.origins) == 0 || client.timeout < time.Millisecond {
		return registryHTTPFailure(CodeInvalidConfiguration, "check registry readiness", "client", "client configuration is incomplete", nil)
	}
	if ctx == nil {
		return registryHTTPFailure(CodeInvalidConfiguration, "check registry readiness", "context", "context is required", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()

	hostSet := make(map[string]struct{}, len(client.origins))
	for originHost, origin := range client.origins {
		hostSet[originHost] = struct{}{}
		for redirectHost := range origin.redirectHosts {
			hostSet[redirectHost] = struct{}{}
		}
	}
	hosts := make([]string, 0, len(hostSet))
	for host := range hostSet {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		if err := requirePublicRegistryHost(ctx, client.resolver, host); err != nil {
			return err
		}
	}
	return nil
}

func (client *HTTPSRegistryClient) fetch(ctx context.Context, target *url.URL, originHost, accept string) (RegistryRead, error) {
	if client == nil || client.httpClient == nil || client.httpClient.Transport == nil || client.resolver == nil || client.timeout < time.Millisecond {
		return RegistryRead{}, registryHTTPFailure(CodeInvalidConfiguration, "fetch registry object", "client", "client is required", nil)
	}
	if ctx == nil {
		return RegistryRead{}, registryHTTPFailure(CodeInvalidConfiguration, "fetch registry object", "context", "context is required", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, client.timeout)
	cancelOnReturn := true
	defer func() {
		if cancelOnReturn {
			cancel()
		}
	}()
	origin, ok := client.origins[originHost]
	if !ok {
		return RegistryRead{}, registryHTTPFailure(CodePolicyDenied, "fetch registry object", "origin", "registry origin is not allowlisted", nil)
	}

	current := cloneURL(target)
	redirectHosts := make([]string, 0, client.maxRedirects)
	credentialEligible := true
	for {
		if err := validateRegistryOutboundURL(current); err != nil {
			return RegistryRead{}, err
		}
		currentHost := current.Hostname()
		if currentHost != originHost {
			if _, allowed := origin.redirectHosts[currentHost]; !allowed {
				return RegistryRead{}, registryHTTPFailure(CodePolicyDenied, "follow registry redirect", "host", "redirect host is not allowlisted for this origin", nil)
			}
		}
		if err := requirePublicRegistryHost(ctx, client.resolver, currentHost); err != nil {
			return RegistryRead{}, err
		}

		request, err := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if err != nil {
			return RegistryRead{}, registryHTTPFailure(CodeInvalidReference, "construct registry request", "url", "request URL is invalid", err)
		}
		request.Header.Set("Accept", accept)
		if credentialEligible && currentHost == originHost && origin.authorization != "" {
			request.Header.Set("Authorization", origin.authorization)
		}

		response, err := client.httpClient.Do(request)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RegistryRead{}, registryHTTPContextFailure("send registry request", ctxErr)
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return RegistryRead{}, registryHTTPContextFailure("send registry request", err)
			}
			var timeoutError interface{ Timeout() bool }
			if errors.As(err, &timeoutError) && timeoutError.Timeout() {
				return RegistryRead{}, registryHTTPContextFailure("send registry request", errors.Join(context.DeadlineExceeded, err))
			}
			return RegistryRead{}, registryHTTPFailure(CodeRegistryFetchFailed, "send registry request", "response", "registry request failed", err)
		}
		if response == nil || response.Body == nil {
			if response != nil {
				closeRegistryResponse(response)
			}
			return RegistryRead{}, registryHTTPFailure(CodeRegistryFetchFailed, "read registry response", "body", "registry returned no response body", nil)
		}

		if isRegistryRedirect(response.StatusCode) {
			if len(redirectHosts) >= client.maxRedirects {
				closeRegistryResponse(response)
				return RegistryRead{}, registryHTTPFailure(CodeLimitExceeded, "follow registry redirect", "redirects", "redirect limit exceeded", nil)
			}
			next, redirectErr := registryRedirectURL(current, response)
			closeRegistryResponse(response)
			if redirectErr != nil {
				return RegistryRead{}, redirectErr
			}
			nextHost := next.Hostname()
			if nextHost != originHost {
				credentialEligible = false
				if _, allowed := origin.redirectHosts[nextHost]; !allowed {
					return RegistryRead{}, registryHTTPFailure(CodePolicyDenied, "follow registry redirect", "host", "redirect host is not allowlisted for this origin", nil)
				}
			}
			if err := requirePublicRegistryHost(ctx, client.resolver, nextHost); err != nil {
				return RegistryRead{}, err
			}
			redirectHosts = append(redirectHosts, nextHost)
			current = next
			continue
		}

		if response.StatusCode != http.StatusOK {
			status := response.StatusCode
			closeRegistryResponse(response)
			return RegistryRead{}, registryHTTPFailure(CodeRegistryFetchFailed, "read registry response", "status", fmt.Sprintf("registry returned HTTP status %d", status), nil)
		}
		cancelOnReturn = false
		return RegistryRead{
			Body:          &registryHTTPResponseBody{body: response.Body, cancel: cancel},
			ServingHost:   currentHost,
			RedirectHosts: append([]string(nil), redirectHosts...),
		}, nil
	}
}

type registryHTTPResponseBody struct {
	body   io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (body *registryHTTPResponseBody) Read(buffer []byte) (int, error) {
	return body.body.Read(buffer)
}

func (body *registryHTTPResponseBody) Close() error {
	var closeErr error
	body.once.Do(func() {
		closeErr = body.body.Close()
		body.cancel()
	})
	return closeErr
}

func normalizeRegistryHTTPOrigins(configured []RegistryHTTPOrigin) (map[string]registryHTTPOrigin, error) {
	if len(configured) == 0 {
		return nil, registryHTTPFailure(CodeInvalidConfiguration, "configure registry client", "origins", "at least one exact origin is required", nil)
	}
	origins := make(map[string]registryHTTPOrigin, len(configured))
	for index, configuredOrigin := range configured {
		host := configuredOrigin.Host
		if normalizeHost(host) != host || isLocalRegistryName(host) {
			return nil, registryHTTPFailure(CodeInvalidConfiguration, "configure registry client", fmt.Sprintf("origins[%d].host", index), "must be a canonical non-local lowercase DNS host", nil)
		}
		if _, duplicate := origins[host]; duplicate {
			return nil, registryHTTPFailure(CodeInvalidConfiguration, "configure registry client", fmt.Sprintf("origins[%d].host", index), "duplicate origin host", nil)
		}
		if !validAuthorizationValue(configuredOrigin.Authorization) {
			return nil, registryHTTPFailure(CodeInvalidConfiguration, "configure registry client", fmt.Sprintf("origins[%d].authorization", index), "contains an invalid header value", nil)
		}
		redirects := make(map[string]struct{}, len(configuredOrigin.RedirectHosts))
		for redirectIndex, redirectHost := range configuredOrigin.RedirectHosts {
			if normalizeHost(redirectHost) != redirectHost || isLocalRegistryName(redirectHost) {
				return nil, registryHTTPFailure(CodeInvalidConfiguration, "configure registry client", fmt.Sprintf("origins[%d].redirectHosts[%d]", index, redirectIndex), "must be a canonical non-local lowercase DNS host", nil)
			}
			if _, duplicate := redirects[redirectHost]; duplicate {
				return nil, registryHTTPFailure(CodeInvalidConfiguration, "configure registry client", fmt.Sprintf("origins[%d].redirectHosts[%d]", index, redirectIndex), "duplicate redirect host", nil)
			}
			redirects[redirectHost] = struct{}{}
		}
		origins[host] = registryHTTPOrigin{
			authorization: configuredOrigin.Authorization,
			redirectHosts: redirects,
		}
	}
	return origins, nil
}

func newRegistryHTTPTransport(resolver RegistryHTTPResolver, roots *x509.CertPool, timeout time.Duration) *http.Transport {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                  nil,
		DialContext:            safeRegistryDialContext(resolver, dialer),
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           32,
		MaxIdleConnsPerHost:    4,
		IdleConnTimeout:        90 * time.Second,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: maxRegistryHTTPHeaderBytes,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		},
	}
}

func safeRegistryDialContext(resolver RegistryHTTPResolver, dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" || normalizeHost(host) != host {
			return nil, registryHTTPFailure(CodePolicyDenied, "dial registry", "address", "only canonical DNS hosts on HTTPS port 443 are allowed", err)
		}
		addresses, err := lookupPublicRegistryAddresses(ctx, resolver, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, resolved := range addresses {
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			lastErr = dialErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, registryHTTPContextFailure("dial registry", ctxErr)
		}
		return nil, registryHTTPFailure(CodeRegistryFetchFailed, "dial registry", "address", "could not connect to an approved registry address", lastErr)
	}
}

func registryManifestURL(reference ExactReference) (*url.URL, error) {
	if err := validateRegistryReference(reference); err != nil {
		return nil, err
	}
	return &url.URL{
		Scheme: "https",
		Host:   reference.Host,
		Path:   "/v2/" + reference.Repository + "/manifests/" + reference.Digest,
	}, nil
}

func registryBlobURL(repository ExactReference, descriptor Descriptor) (*url.URL, error) {
	if err := validateRegistryReference(repository); err != nil {
		return nil, err
	}
	if !digestPattern.MatchString(descriptor.Digest) {
		return nil, registryHTTPFailure(CodeInvalidReference, "construct blob URL", "descriptor.digest", "must be an exact lowercase sha256 digest", nil)
	}
	return &url.URL{
		Scheme: "https",
		Host:   repository.Host,
		Path:   "/v2/" + repository.Repository + "/blobs/" + descriptor.Digest,
	}, nil
}

func validateRegistryReference(reference ExactReference) error {
	if normalizeHost(reference.Host) != reference.Host || isLocalRegistryName(reference.Host) {
		return registryHTTPFailure(CodeInvalidReference, "construct registry URL", "host", "must be a canonical non-local lowercase DNS host", nil)
	}
	if !repositoryPattern.MatchString(reference.Repository) {
		return registryHTTPFailure(CodeInvalidReference, "construct registry URL", "repository", "must be an exact canonical lowercase repository", nil)
	}
	if !digestPattern.MatchString(reference.Digest) {
		return registryHTTPFailure(CodeInvalidReference, "construct registry URL", "digest", "must be an exact lowercase sha256 digest", nil)
	}
	return nil
}

func registryRedirectURL(current *url.URL, response *http.Response) (*url.URL, error) {
	locations := response.Header.Values("Location")
	if len(locations) != 1 || strings.TrimSpace(locations[0]) != locations[0] || locations[0] == "" {
		return nil, registryHTTPFailure(CodePolicyDenied, "follow registry redirect", "location", "redirect must contain one canonical Location", nil)
	}
	raw := locations[0]
	if strings.ContainsAny(raw, "?#") {
		return nil, registryHTTPFailure(CodePolicyDenied, "follow registry redirect", "location", "redirect query and fragment are forbidden", nil)
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return nil, registryHTTPFailure(CodePolicyDenied, "follow registry redirect", "location", "redirect URL is invalid", err)
	}
	target := current.ResolveReference(reference)
	if err := validateRegistryOutboundURL(target); err != nil {
		return nil, err
	}
	return target, nil
}

func validateRegistryOutboundURL(target *url.URL) error {
	if target == nil || target.Scheme != "https" || target.Opaque != "" {
		return registryHTTPFailure(CodePolicyDenied, "validate registry URL", "scheme", "only hierarchical HTTPS URLs are allowed", nil)
	}
	if target.User != nil {
		return registryHTTPFailure(CodePolicyDenied, "validate registry URL", "userinfo", "URL userinfo is forbidden", nil)
	}
	if target.RawQuery != "" || target.ForceQuery || target.Fragment != "" || target.RawFragment != "" {
		return registryHTTPFailure(CodePolicyDenied, "validate registry URL", "url", "URL query and fragment are forbidden", nil)
	}
	if target.RawPath != "" || target.Path == "" || !strings.HasPrefix(target.Path, "/") || strings.Contains(target.Path, "\\") {
		return registryHTTPFailure(CodePolicyDenied, "validate registry URL", "path", "URL path is not canonical", nil)
	}
	host := target.Hostname()
	if target.Host != host || normalizeHost(host) != host || isLocalRegistryName(host) {
		return registryHTTPFailure(CodePolicyDenied, "validate registry URL", "host", "host must be a canonical non-local DNS name without a port", nil)
	}
	return nil
}

func requirePublicRegistryHost(ctx context.Context, resolver RegistryHTTPResolver, host string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return registryHTTPContextFailure("resolve registry host", ctxErr)
	}
	if normalizeHost(host) != host || isLocalRegistryName(host) {
		return registryHTTPFailure(CodePolicyDenied, "resolve registry host", "host", "literal and local hosts are forbidden", nil)
	}
	_, err := lookupPublicRegistryAddresses(ctx, resolver, host)
	return err
}

func lookupPublicRegistryAddresses(ctx context.Context, resolver RegistryHTTPResolver, host string) ([]net.IPAddr, error) {
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, registryHTTPContextFailure("resolve registry host", ctxErr)
		}
		return nil, registryHTTPFailure(CodeRegistryFetchFailed, "resolve registry host", "host", "DNS resolution failed", err)
	}
	if len(addresses) == 0 {
		return nil, registryHTTPFailure(CodePolicyDenied, "resolve registry host", "addresses", "host resolved to no addresses", nil)
	}
	for _, address := range addresses {
		if address.Zone != "" || !isPublicRegistryIP(address.IP) {
			return nil, registryHTTPFailure(CodePolicyDenied, "resolve registry host", "addresses", "host resolved to a non-public address", nil)
		}
	}
	return addresses, nil
}

func isPublicRegistryIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsUnspecified() || address.IsMulticast() {
		return false
	}
	for _, prefix := range nonPublicRegistryPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func isLocalRegistryName(host string) bool {
	return host == "localhost" ||
		strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".localdomain") ||
		strings.HasSuffix(host, ".internal") ||
		strings.HasSuffix(host, ".lan") ||
		host == "home.arpa" || strings.HasSuffix(host, ".home.arpa")
}

func validAuthorizationValue(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 8192 || strings.TrimSpace(value) != value {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func isRegistryRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func closeRegistryResponse(response *http.Response) {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
}

func cloneURL(source *url.URL) *url.URL {
	copy := *source
	return &copy
}

func registryHTTPContextFailure(operation string, cause error) error {
	code := CodeRegistryFetchFailed
	detail := "registry operation was canceled"
	if errors.Is(cause, context.DeadlineExceeded) {
		code = CodeTimeout
		detail = "registry operation timed out"
	}
	return registryHTTPFailure(code, operation, "context", detail, cause)
}

func registryHTTPFailure(code ErrorCode, operation, field, detail string, cause error) error {
	return verificationFailure(code, operation, field, detail, cause)
}

var _ RegistryClient = (*HTTPSRegistryClient)(nil)
