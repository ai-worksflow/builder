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
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type PlatformCredentialProvider interface {
	Organization() string
	Token(context.Context) (string, time.Time, error)
}

type InstallationTokenProvider struct {
	api            *APIClient
	appID          int64
	installationID int64
	organization   string
	privateKey     *rsa.PrivateKey
	now            func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func NewInstallationTokenProvider(
	api *APIClient,
	appID string,
	installationID string,
	organization string,
	privateKeyPEM []byte,
) (*InstallationTokenProvider, error) {
	parsedAppID, appErr := strconv.ParseInt(strings.TrimSpace(appID), 10, 64)
	parsedInstallationID, installationErr := strconv.ParseInt(strings.TrimSpace(installationID), 10, 64)
	organization, organizationErr := validateRepositoryPart(organization, "organization")
	privateKey, keyErr := parseGitHubAppPrivateKey(privateKeyPEM)
	if api == nil || appErr != nil || parsedAppID < 1 || installationErr != nil ||
		parsedInstallationID < 1 || organizationErr != nil || keyErr != nil {
		return nil, errors.New("GitHub App ID, installation ID, organization and RSA private key are required")
	}
	return &InstallationTokenProvider{
		api: api, appID: parsedAppID, installationID: parsedInstallationID,
		organization: organization, privateKey: privateKey, now: time.Now,
	}, nil
}

func (provider *InstallationTokenProvider) Organization() string {
	if provider == nil {
		return ""
	}
	return provider.organization
}

func (provider *InstallationTokenProvider) Token(ctx context.Context) (string, time.Time, error) {
	if provider == nil || ctx == nil {
		return "", time.Time{}, errors.New("GitHub App installation is unavailable")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	now := provider.now().UTC()
	if provider.token != "" && provider.expiresAt.After(now.Add(2*time.Minute)) {
		return provider.token, provider.expiresAt, nil
	}
	jwt, err := provider.signedJWT(now)
	if err != nil {
		return "", time.Time{}, err
	}
	var response struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	endpoint := fmt.Sprintf("/app/installations/%d/access_tokens", provider.installationID)
	if err := provider.api.request(ctx, jwt, http.MethodPost, endpoint, map[string]any{}, &response); err != nil {
		return "", time.Time{}, err
	}
	response.Token = strings.TrimSpace(response.Token)
	if response.Token == "" || !response.ExpiresAt.After(now.Add(time.Minute)) {
		return "", time.Time{}, upstream("upstream_error", http.StatusBadGateway, "GitHub returned an invalid installation token", nil)
	}
	provider.token, provider.expiresAt = response.Token, response.ExpiresAt.UTC()
	return provider.token, provider.expiresAt, nil
}

func (provider *InstallationTokenProvider) signedJWT(now time.Time) (string, error) {
	header, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": provider.appID,
	})
	encode := base64.RawURLEncoding.EncodeToString
	unsigned := encode(header) + "." + encode(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, provider.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	return unsigned + "." + encode(signature), nil
}

func parseGitHubAppPrivateKey(value []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(value)
	if block == nil {
		return nil, errors.New("GitHub App private key is not PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GitHub App private key is not RSA")
	}
	return key, nil
}
