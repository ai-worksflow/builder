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

	serve := func(method, asset, rawQuery string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		target := "/published/" + deploymentID + "/" + versionID + "/" + asset
		if rawQuery != "" {
			target += "?" + rawQuery
		}
		request := httptest.NewRequest(method, target, nil)
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		provider.ServeAsset(response, request, deploymentID, versionID, asset)
		return response
	}

	canonicalLocation := "/published/" + deploymentID + "/" + versionID + "/closure/lineage/"
	withoutSlash := serve(http.MethodGet, "closure/lineage", "", nil)
	if withoutSlash.Code != http.StatusPermanentRedirect || withoutSlash.Header().Get("Location") != canonicalLocation || withoutSlash.Body.Len() != 0 {
		t.Fatalf("directory URL was not canonicalized: status=%d location=%q body=%q", withoutSlash.Code, withoutSlash.Header().Get("Location"), withoutSlash.Body.String())
	}
	if withoutSlash.Header().Get("ETag") != "" || withoutSlash.Header().Get("Content-Security-Policy") != "" {
		t.Fatalf("directory redirect exposed index response headers: %v", withoutSlash.Header())
	}

	withSlash := serve(http.MethodGet, "closure/lineage/", "", nil)
	if withSlash.Code != http.StatusOK || !strings.Contains(withSlash.Body.String(), "lineage-index") {
		t.Fatalf("directory index was not served: status=%d body=%q", withSlash.Code, withSlash.Body.String())
	}
	if !strings.HasPrefix(withSlash.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("directory index content type = %q", withSlash.Header().Get("Content-Type"))
	}
	csp := withSlash.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "connect-src 'self' https://api.example.test;") {
		t.Fatalf("directory index CSP = %q", csp)
	}
	if withSlash.Header().Get("ETag") == "" {
		t.Fatal("directory index did not include an immutable ETag")
	}

	redirectHead := serve(http.MethodHead, "closure/lineage", "", nil)
	if redirectHead.Code != http.StatusPermanentRedirect || redirectHead.Header().Get("Location") != canonicalLocation || redirectHead.Body.Len() != 0 {
		t.Fatalf("directory HEAD was not canonicalized: status=%d headers=%v body=%q", redirectHead.Code, redirectHead.Header(), redirectHead.Body.String())
	}
	queryRedirect := serve(http.MethodGet, "closure/lineage", "state=error&from=review", map[string]string{"If-None-Match": withSlash.Header().Get("ETag")})
	if queryRedirect.Code != http.StatusPermanentRedirect || queryRedirect.Header().Get("Location") != canonicalLocation+"?state=error&from=review" {
		t.Fatalf("directory redirect did not preserve query or beat conditional handling: status=%d location=%q", queryRedirect.Code, queryRedirect.Header().Get("Location"))
	}

	head := serve(http.MethodHead, "closure/lineage/", "", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != withSlash.Header().Get("ETag") ||
		head.Header().Get("Content-Length") != withSlash.Header().Get("Content-Length") {
		t.Fatalf("directory index HEAD response differs from GET: status=%d headers=%v body=%q", head.Code, head.Header(), head.Body.String())
	}

	for _, asset := range []string{
		"closure/no-index", "closure/no-index/", "closure/file.txt/", ".worksflow/",
		"closure//lineage", "closure/../lineage", "closure/lineage//",
	} {
		response := serve(http.MethodGet, asset, "", nil)
		if response.Code != http.StatusNotFound || response.Header().Get("Location") != "" {
			t.Fatalf("unsafe or non-index asset %q returned status=%d location=%q", asset, response.Code, response.Header().Get("Location"))
		}
	}

	versionRoot := filepath.Join(root, deploymentID, "versions", versionID)
	if err := os.Symlink("lineage", filepath.Join(versionRoot, "closure", "linked")); err != nil {
		t.Fatal(err)
	}
	if response := serve(http.MethodGet, "closure/linked", "", nil); response.Code != http.StatusNotFound || response.Header().Get("Location") != "" {
		t.Fatalf("symlinked directory index returned %d", response.Code)
	}
	if err := os.Mkdir(filepath.Join(versionRoot, "closure", "linked-index"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../lineage/index.html", filepath.Join(versionRoot, "closure", "linked-index", "index.html")); err != nil {
		t.Fatal(err)
	}
	if response := serve(http.MethodGet, "closure/linked-index", "", nil); response.Code != http.StatusNotFound || response.Header().Get("Location") != "" {
		t.Fatalf("symlinked index file returned %d", response.Code)
	}
	if err := os.MkdirAll(filepath.Join(versionRoot, "closure", "non-regular", "index.html"), 0o700); err != nil {
		t.Fatal(err)
	}
	if response := serve(http.MethodGet, "closure/non-regular", "", nil); response.Code != http.StatusNotFound || response.Header().Get("Location") != "" {
		t.Fatalf("non-regular directory index returned %d", response.Code)
	}

	absolute, err := NewLocalStaticProvider(root, "https://apps.example.test/published")
	if err != nil {
		t.Fatal(err)
	}
	absoluteResponse := httptest.NewRecorder()
	absoluteRequest := httptest.NewRequest(http.MethodGet, "https://evil.example/published/"+deploymentID+"/"+versionID+"/closure/lineage?state=error", nil)
	absolute.ServeAsset(absoluteResponse, absoluteRequest, deploymentID, versionID, "closure/lineage")
	expectedAbsolute := "https://apps.example.test/published/" + deploymentID + "/" + versionID + "/closure/lineage/?state=error"
	if absoluteResponse.Code != http.StatusPermanentRedirect || absoluteResponse.Header().Get("Location") != expectedAbsolute {
		t.Fatalf("absolute redirect trusted request host: status=%d location=%q", absoluteResponse.Code, absoluteResponse.Header().Get("Location"))
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
