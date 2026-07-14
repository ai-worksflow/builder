package delivery

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestLocalStaticProviderServesSafeDirectoryIndexes(t *testing.T) {
	root := t.TempDir()
	provider, err := NewLocalStaticProvider(root, "/published")
	if err != nil {
		t.Fatal(err)
	}
	deploymentID, versionID := uuid.NewString(), uuid.NewString()
	_, err = provider.Deploy(context.Background(), ProviderRequest{
		DeploymentID: deploymentID, VersionID: versionID, Environment: EnvironmentPreview,
		BuildArtifact: testBuildArtifact(t, []WorkspaceFile{
			{Path: "index.html", Content: "<html><body>root</body></html>"},
			{Path: "closure/lineage/index.html", Content: "<html><body>lineage-index</body></html>"},
			{Path: "closure/no-index/app.js", Content: "window.ready = true"},
			{Path: "closure/file.txt", Content: "plain"},
		}, "index.html"),
		PublicEnvironment: map[string]string{"PUBLIC_API": "https://api.example.test/v1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	serve := func(method, asset string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		provider.ServeAsset(response, httptest.NewRequest(method, "/published", nil), deploymentID, versionID, asset)
		return response
	}

	withoutSlash := serve(http.MethodGet, "closure/lineage")
	withSlash := serve(http.MethodGet, "closure/lineage/")
	for asset, response := range map[string]*httptest.ResponseRecorder{
		"closure/lineage":  withoutSlash,
		"closure/lineage/": withSlash,
	} {
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "lineage-index") {
			t.Fatalf("directory index %q was not served: status=%d body=%q", asset, response.Code, response.Body.String())
		}
		if !strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("directory index %q content type = %q", asset, response.Header().Get("Content-Type"))
		}
		csp := response.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "connect-src 'self' https://api.example.test;") {
			t.Fatalf("directory index %q CSP = %q", asset, csp)
		}
	}
	if withoutSlash.Header().Get("ETag") == "" || withoutSlash.Header().Get("ETag") != withSlash.Header().Get("ETag") {
		t.Fatalf("directory URL variants did not resolve the same immutable asset: %q %q", withoutSlash.Header().Get("ETag"), withSlash.Header().Get("ETag"))
	}

	head := serve(http.MethodHead, "closure/lineage/")
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != withoutSlash.Header().Get("ETag") ||
		head.Header().Get("Content-Length") != withoutSlash.Header().Get("Content-Length") {
		t.Fatalf("directory index HEAD response differs from GET: status=%d headers=%v body=%q", head.Code, head.Header(), head.Body.String())
	}

	for _, asset := range []string{
		"closure/no-index", "closure/no-index/", "closure/file.txt/", ".worksflow/",
		"closure//lineage", "closure/../lineage", "closure/lineage//",
	} {
		if response := serve(http.MethodGet, asset); response.Code != http.StatusNotFound {
			t.Fatalf("unsafe or non-index asset %q returned %d", asset, response.Code)
		}
	}

	versionRoot := filepath.Join(root, deploymentID, "versions", versionID)
	if err := os.Symlink("lineage", filepath.Join(versionRoot, "closure", "linked")); err != nil {
		t.Fatal(err)
	}
	if response := serve(http.MethodGet, "closure/linked"); response.Code != http.StatusNotFound {
		t.Fatalf("symlinked directory index returned %d", response.Code)
	}
	if err := os.Mkdir(filepath.Join(versionRoot, "closure", "linked-index"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../lineage/index.html", filepath.Join(versionRoot, "closure", "linked-index", "index.html")); err != nil {
		t.Fatal(err)
	}
	if response := serve(http.MethodGet, "closure/linked-index"); response.Code != http.StatusNotFound {
		t.Fatalf("symlinked index file returned %d", response.Code)
	}
	if err := os.MkdirAll(filepath.Join(versionRoot, "closure", "non-regular", "index.html"), 0o700); err != nil {
		t.Fatal(err)
	}
	if response := serve(http.MethodGet, "closure/non-regular"); response.Code != http.StatusNotFound {
		t.Fatalf("non-regular directory index returned %d", response.Code)
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
