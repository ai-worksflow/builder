package delivery

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
)

func providerTestRef() core.VersionRef {
	return core.VersionRef{
		ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(),
		ContentHash: "sha256:" + strings.Repeat("d", 64),
	}
}

func providerTestArtifact(t *testing.T) BuildArtifact {
	t.Helper()
	files := []BuildArtifactFile{{
		Path: "index.html", ContentBase64: base64.StdEncoding.EncodeToString([]byte("<html><head></head><body>ready</body></html>")),
	}}
	hash, err := hashBuildFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	return BuildArtifact{
		ID: uuid.NewString(), WorkspaceRevision: providerTestRef(), BuildHash: hash,
		EntryPath: "index.html", Files: files, FileCount: 1, TotalBytes: int64(len("<html><head></head><body>ready</body></html>")),
	}
}

func TestPublishedCSPUsesValidatedMinimalConnectOrigins(t *testing.T) {
	t.Parallel()
	provider, err := NewLocalStaticProvider(t.TempDir(), "/published")
	if err != nil {
		t.Fatal(err)
	}
	deploymentID, versionID := uuid.NewString(), uuid.NewString()
	_, err = provider.Deploy(context.Background(), ProviderRequest{
		DeploymentID: deploymentID, VersionID: versionID, Environment: EnvironmentPreview,
		BuildArtifact: providerTestArtifact(t), PublicEnvironment: map[string]string{
			"PUBLIC_API":  "https://api.example.test/v1",
			"VITE_SOCKET": "wss://socket.example.test/events",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	provider.ServeAsset(response, httptest.NewRequest(http.MethodGet, "/published", nil), deploymentID, versionID, "index.html")
	csp := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self' https://api.example.test wss://socket.example.test;") ||
		strings.Contains(csp, "connect-src 'none'") || strings.Contains(csp, "/v1") || strings.Contains(csp, "/events") {
		t.Fatalf("CSP connect policy is not the minimal validated origin set: %s", csp)
	}
}

func TestPublishProviderRejectsCredentialedAndNonHTTPPublicURLs(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"https://user:password@example.test/v1",
		"ftp://files.example.test/archive",
		"javascript:alert(1)",
		"//api.example.test/v1",
	} {
		provider, err := NewLocalStaticProvider(t.TempDir(), "/published")
		if err != nil {
			t.Fatal(err)
		}
		_, err = provider.Deploy(context.Background(), ProviderRequest{
			DeploymentID: uuid.NewString(), VersionID: uuid.NewString(), Environment: EnvironmentPreview,
			BuildArtifact: providerTestArtifact(t), PublicEnvironment: map[string]string{"PUBLIC_URL": value},
		})
		if err == nil {
			t.Fatalf("unsafe public URL was accepted: %s", value)
		}
	}
}

func TestLocalStaticProviderDeclaresExactPublicDataOrigin(t *testing.T) {
	t.Parallel()
	provider, err := NewLocalStaticProvider(t.TempDir(), "https://Apps.Example.test/published")
	if err != nil {
		t.Fatal(err)
	}
	origins, err := provider.PublicDeploymentOrigins(ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(origins) != 1 || origins[0] != "https://apps.example.test" {
		t.Fatalf("public deployment origins = %#v", origins)
	}

	relative, err := NewLocalStaticProvider(t.TempDir(), "/published")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relative.PublicDeploymentOrigins(ProviderRequest{}); err == nil {
		t.Fatal("relative publish URL cannot safely declare a browser Origin")
	}
}
