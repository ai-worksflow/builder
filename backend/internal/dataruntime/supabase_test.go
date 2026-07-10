package dataruntime

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type staticResolver struct {
	addresses []netip.Addr
	err       error
}

func (r staticResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addresses, r.err
}

func TestSupabaseEndpointAndDNSBlockPrivateTargets(t *testing.T) {
	t.Parallel()

	unsafe := []string{
		"http://example.com", "https://localhost", "https://127.0.0.1",
		"https://169.254.169.254", "https://example.com/other",
		"https://user:pass@example.com", "https://example.com?next=x",
	}
	for _, value := range unsafe {
		if _, err := NormalizeSupabaseEndpoint(value); err == nil {
			t.Errorf("expected %q to be unsafe", value)
		}
	}
	endpoint, err := NormalizeSupabaseEndpoint("https://Demo.Supabase.co/rest/v1")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.String() != "https://demo.supabase.co/rest/v1/" {
		t.Fatalf("unexpected normalized endpoint %q", endpoint.String())
	}

	prober, err := NewSupabaseProber(SupabaseProberOptions{
		Resolver: staticResolver{addresses: []netip.Addr{netip.MustParseAddr("10.0.0.8")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = prober.Probe(context.Background(), SupabaseConnectionInput{Endpoint: endpoint.String(), Key: "test-key"})
	if runtimeErr, ok := AsRuntimeError(err); !ok || runtimeErr.Code != CodeUnsafeEndpoint {
		t.Fatalf("private DNS result was not blocked: %v", err)
	}
}

func TestUnsafeNetworkAddressCoversMappedAndDocumentationRanges(t *testing.T) {
	t.Parallel()

	unsafe := []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "192.0.2.1", "::1", "::ffff:10.0.0.1", "2001:db8::1"}
	for _, value := range unsafe {
		if !IsUnsafeNetworkAddress(netip.MustParseAddr(value)) {
			t.Errorf("expected %s to be unsafe", value)
		}
	}
	if IsUnsafeNetworkAddress(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("public address was rejected")
	}
}

func TestReadOpenAPITableNamesIsBoundedAndSorted(t *testing.T) {
	t.Parallel()

	document := `{"openapi":"3.0.0","paths":{"/zebra":{},"/users":{},"/../unsafe":{}},"components":{"schemas":{"accounts":{},"Bad-Name":{}}}}`
	names := ReadOpenAPITableNames(strings.NewReader(document), int64(len(document)))
	if strings.Join(names, ",") != "accounts,users,zebra" {
		t.Fatalf("unexpected names: %v", names)
	}
	tooLarge := ReadOpenAPITableNames(strings.NewReader(document), maxOpenAPISchemaBytes+1)
	if len(tooLarge) != 0 {
		t.Fatal("declared oversized schema must not be read")
	}
}

func TestSupabaseProbeNeverForwardsKeyAcrossRedirect(t *testing.T) {
	t.Parallel()

	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Header.Get("apikey") != "server-key" {
			t.Fatalf("probe omitted target credential header")
		}
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Header:     http.Header{"Location": []string{"https://attacker.example/steal"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	})}
	prober, err := NewSupabaseProber(SupabaseProberOptions{
		Resolver:   staticResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}},
		HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := prober.Probe(context.Background(), SupabaseConnectionInput{
		Endpoint: "https://project.supabase.co", Key: "server-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.OK || result.Status != http.StatusTemporaryRedirect {
		t.Fatalf("redirect was followed: calls=%d result=%+v", calls, result)
	}
}
