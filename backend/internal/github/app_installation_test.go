package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstallationTokenProviderSignsAndCachesToken(t *testing.T) {
	t.Parallel()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/app/installations/456/access_tokens" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		jwt := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(jwt, ".")
		if len(parts) != 3 {
			t.Errorf("authorization is not a JWT")
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		unsigned := parts[0] + "." + parts[1]
		digest := sha256.Sum256([]byte(unsigned))
		signature, decodeErr := base64.RawURLEncoding.DecodeString(parts[2])
		if decodeErr != nil || rsa.VerifyPKCS1v15(&privateKey.PublicKey, crypto.SHA256, digest[:], signature) != nil {
			t.Errorf("JWT signature is invalid")
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		payload, decodeErr := base64.RawURLEncoding.DecodeString(parts[1])
		var claims map[string]any
		if decodeErr != nil || json.Unmarshal(payload, &claims) != nil || claims["iss"] != float64(123) {
			t.Errorf("JWT claims = %#v", claims)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"token":      "installation-token",
			"expires_at": now.Add(time.Hour).Format(time.RFC3339),
		})
	}))
	defer server.Close()
	api, err := NewAPIClient(server.URL, time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	provider, err := NewInstallationTokenProvider(api, "123", "456", "ai-worksflow", keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	provider.now = func() time.Time { return now }
	for attempt := 0; attempt < 2; attempt++ {
		token, expiresAt, tokenErr := provider.Token(context.Background())
		if tokenErr != nil || token != "installation-token" || !expiresAt.Equal(now.Add(time.Hour)) {
			t.Fatalf("token attempt %d = %q %s, error=%v", attempt, token, expiresAt, tokenErr)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("installation token requests = %d, want 1", requests.Load())
	}
}
