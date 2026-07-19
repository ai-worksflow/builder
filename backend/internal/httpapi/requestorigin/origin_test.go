package requestorigin

import (
	"net/http/httptest"
	"testing"
)

func TestSameUsesEffectivePublicSchemeAndHost(t *testing.T) {
	request := httptest.NewRequest("POST", "http://api:8080/v1/projects/p/presence/heartbeat", nil)
	request.Host = "43.216.1.58:10000"
	request.Header.Set("X-Forwarded-Proto", "http")
	if !Same(request, "http://43.216.1.58:10000") {
		t.Fatal("same-origin public IP was rejected")
	}
	request.Header.Set("X-Forwarded-Proto", "https")
	if !Same(request, "https://43.216.1.58:10000") {
		t.Fatal("same-origin TLS proxy request was rejected")
	}
	if Same(request, "https://evil.example.com") || Same(request, "https://43.216.1.58:10000/path") {
		t.Fatal("cross-origin or malformed Origin was accepted")
	}
}
