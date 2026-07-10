package dataruntime

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"
)

const maxOpenAPISchemaBytes = 1_000_000

type IPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type SupabaseProberOptions struct {
	Resolver   IPResolver
	Timeout    time.Duration
	Dialer     *net.Dialer
	HTTPClient *http.Client // Tests only; production should use the pinned default client.
	Now        func() time.Time
}

type SupabaseProber struct {
	resolver   IPResolver
	timeout    time.Duration
	dialer     *net.Dialer
	httpClient *http.Client
	now        func() time.Time
}

func NewSupabaseProber(options SupabaseProberOptions) (*SupabaseProber, error) {
	if options.Resolver == nil {
		options.Resolver = net.DefaultResolver
	}
	if options.Timeout <= 0 {
		options.Timeout = 5 * time.Second
	}
	if options.Timeout < 500*time.Millisecond || options.Timeout > 10*time.Second {
		return nil, errors.New("Supabase probe timeout must be between 500ms and 10s")
	}
	if options.Dialer == nil {
		options.Dialer = &net.Dialer{Timeout: options.Timeout, KeepAlive: 30 * time.Second}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &SupabaseProber{
		resolver: options.Resolver, timeout: options.Timeout, dialer: options.Dialer,
		httpClient: options.HTTPClient, now: options.Now,
	}, nil
}

func (p *SupabaseProber) Probe(ctx context.Context, input SupabaseConnectionInput) (SupabaseConnectionResult, error) {
	probeCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	endpoint, err := NormalizeSupabaseEndpoint(input.Endpoint)
	if err != nil {
		return SupabaseConnectionResult{}, err
	}
	hostname := strings.ToLower(endpoint.Hostname())
	addresses, err := p.resolvePublicAddresses(probeCtx, hostname)
	if err != nil {
		return SupabaseConnectionResult{}, err
	}
	client := p.httpClient
	if client == nil {
		client = p.pinnedClient(hostname, addresses)
	} else {
		// Even an injected test/client transport may never forward the key to a
		// redirect target. Clone it so caller-owned configuration is untouched.
		copy := *client
		copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		if copy.Timeout <= 0 || copy.Timeout > p.timeout {
			copy.Timeout = p.timeout
		}
		client = &copy
	}
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return SupabaseConnectionResult{}, NewError(CodeConnectionFailed, http.StatusBadGateway, "Supabase connection could not be established")
	}
	request.Header.Set("apikey", input.Key)
	request.Header.Set("Authorization", "Bearer "+input.Key)
	request.Header.Set("Accept", "application/json")
	startedAt := p.now()
	response, err := client.Do(request)
	latency := p.now().Sub(startedAt)
	if latency < 0 {
		latency = 0
	}
	if err != nil {
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			return SupabaseConnectionResult{}, NewError(CodeConnectionFailed, http.StatusBadGateway, "Supabase connection timed out")
		}
		return SupabaseConnectionResult{}, NewError(CodeConnectionFailed, http.StatusBadGateway, "Supabase connection could not be established")
	}
	defer response.Body.Close()
	origin := endpoint.Scheme + "://" + endpoint.Host
	result := SupabaseConnectionResult{
		Endpoint: origin, LatencyMS: latency.Milliseconds(), Status: response.StatusCode,
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		result.Message = "Supabase endpoint attempted an unexpected redirect."
		return result, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			result.Message = "Supabase rejected the supplied key."
		} else {
			result.Message = "Supabase REST endpoint returned an error."
		}
		return result, nil
	}
	result.OK = true
	result.Message = "Supabase REST connection succeeded."
	result.SchemaTables = ReadOpenAPITableNames(response.Body, response.ContentLength)
	return result, nil
}

func NormalizeSupabaseEndpoint(value string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(value))
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, unsafeEndpoint("endpoint must be a valid absolute URL")
	}
	if endpoint.User != nil {
		return nil, unsafeEndpoint("endpoint may not contain credentials")
	}
	if endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, unsafeEndpoint("endpoint may not contain a query string or fragment")
	}
	if endpoint.Scheme != "https" {
		return nil, unsafeEndpoint("endpoint must use HTTPS")
	}
	if endpoint.Port() != "" && endpoint.Port() != "443" {
		return nil, unsafeEndpoint("endpoint may use only the HTTPS default port")
	}
	hostname := strings.ToLower(strings.TrimSuffix(endpoint.Hostname(), "."))
	if hostname == "" || hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || hostname == "0.0.0.0" {
		return nil, unsafeEndpoint("endpoint hostname is not allowed")
	}
	if address, err := netip.ParseAddr(hostname); err == nil && IsUnsafeNetworkAddress(address) {
		return nil, unsafeEndpoint("endpoint may not target a private or reserved network")
	} else if err != nil && !validDNSHostname(hostname) {
		return nil, unsafeEndpoint("endpoint hostname is not valid")
	}
	path := strings.TrimRight(endpoint.EscapedPath(), "/")
	if path != "" && path != "/rest/v1" {
		return nil, unsafeEndpoint("endpoint path must be the project root or /rest/v1")
	}
	endpoint.Scheme = "https"
	if address, err := netip.ParseAddr(hostname); err == nil && address.Is6() {
		endpoint.Host = "[" + hostname + "]"
	} else {
		endpoint.Host = hostname
	}
	endpoint.Path = "/rest/v1/"
	endpoint.RawPath = ""
	return endpoint, nil
}

func validDNSHostname(hostname string) bool {
	if len(hostname) == 0 || len(hostname) > 253 {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func (p *SupabaseProber) resolvePublicAddresses(ctx context.Context, hostname string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(hostname); err == nil {
		address = address.Unmap()
		if IsUnsafeNetworkAddress(address) {
			return nil, unsafeEndpoint("endpoint may not target a private or reserved network")
		}
		return []netip.Addr{address}, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	addresses, err := p.resolver.LookupNetIP(lookupCtx, "ip", hostname)
	if err != nil {
		return nil, NewError(CodeConnectionFailed, http.StatusBadGateway, "Could not resolve the Supabase endpoint")
	}
	if len(addresses) == 0 {
		return nil, NewError(CodeConnectionFailed, http.StatusBadGateway, "Supabase endpoint did not resolve to an address")
	}
	result := make([]netip.Addr, 0, len(addresses))
	seen := map[netip.Addr]struct{}{}
	for _, address := range addresses {
		address = address.Unmap()
		if IsUnsafeNetworkAddress(address) {
			return nil, unsafeEndpoint("endpoint resolves to a private or reserved network address")
		}
		if _, ok := seen[address]; !ok {
			seen[address] = struct{}{}
			result = append(result, address)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Less(result[right]) })
	return result, nil
}

func (p *SupabaseProber) pinnedClient(hostname string, addresses []netip.Addr) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2,
		IdleConnTimeout:       15 * time.Second,
		TLSHandshakeTimeout:   p.timeout,
		ResponseHeaderTimeout: p.timeout,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: hostname,
		},
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialHost, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(strings.TrimSuffix(dialHost, "."), hostname) || port != "443" {
			return nil, errors.New("Supabase dial target changed after validation")
		}
		var dialErrors []error
		for _, ip := range addresses {
			connection, err := p.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return connection, nil
			}
			dialErrors = append(dialErrors, err)
		}
		return nil, errors.Join(dialErrors...)
	}
	return &http.Client{
		Transport: transport,
		Timeout:   p.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func IsUnsafeNetworkAddress(address netip.Addr) bool {
	if !address.IsValid() {
		return true
	}
	address = address.Unmap()
	if address.IsUnspecified() || address.IsLoopback() || address.IsPrivate() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() ||
		address.IsMulticast() || !address.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range unsafeNetworkPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

var unsafeNetworkPrefixes = mustPrefixes(
	"0.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "192.0.0.0/24",
	"192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
	"240.0.0.0/4", "2001:db8::/32", "2001:2::/48", "64:ff9b:1::/48",
)

func mustPrefixes(values ...string) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}

func unsafeEndpoint(message string) *RuntimeError {
	return NewError(CodeUnsafeEndpoint, http.StatusBadRequest, message)
}

func ReadOpenAPITableNames(reader io.Reader, declaredLength int64) []string {
	if reader == nil || declaredLength > maxOpenAPISchemaBytes {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxOpenAPISchemaBytes+1))
	if err != nil || len(data) > maxOpenAPISchemaBytes {
		return nil
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil
	}
	if _, swagger := value["swagger"].(string); !swagger {
		if _, openapi := value["openapi"].(string); !openapi {
			return nil
		}
	}
	names := map[string]struct{}{}
	if paths, ok := value["paths"].(map[string]any); ok {
		for path := range paths {
			name := strings.TrimPrefix(path, "/")
			if path == "/"+name && databaseIdentifierPattern.MatchString(strings.ToLower(name)) {
				names[name] = struct{}{}
			}
		}
	}
	if definitions, ok := value["definitions"].(map[string]any); ok {
		addSchemaNames(names, definitions)
	}
	if components, ok := value["components"].(map[string]any); ok {
		if schemas, ok := components["schemas"].(map[string]any); ok {
			addSchemaNames(names, schemas)
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	if len(result) > 256 {
		result = result[:256]
	}
	return result
}

func addSchemaNames(destination map[string]struct{}, schemas map[string]any) {
	for name, definition := range schemas {
		if _, ok := definition.(map[string]any); ok && databaseIdentifierPattern.MatchString(strings.ToLower(name)) {
			destination[name] = struct{}{}
		}
	}
}
