package delivery

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/dataruntime"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func exactRef() core.VersionRef {
	return core.VersionRef{
		ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(),
		ContentHash: "sha256:" + strings.Repeat("a", 64),
	}
}

func testBuildArtifact(t *testing.T, files []WorkspaceFile, entryPath string) BuildArtifact {
	t.Helper()
	artifactFiles := make([]BuildArtifactFile, 0, len(files))
	total := int64(0)
	for _, file := range files {
		artifactFiles = append(artifactFiles, BuildArtifactFile{Path: file.Path, ContentBase64: base64.StdEncoding.EncodeToString([]byte(file.Content))})
		total += int64(len(file.Content))
	}
	hash, err := hashBuildFiles(artifactFiles)
	if err != nil {
		hash = "sha256:" + strings.Repeat("0", 64)
	}
	return BuildArtifact{
		ID: uuid.NewString(), WorkspaceRevision: exactRef(), BuildHash: hash,
		EntryPath: entryPath, Files: artifactFiles, FileCount: len(artifactFiles), TotalBytes: total,
	}
}

func TestValidateVersionRefRequiresCanonicalExactIdentity(t *testing.T) {
	valid := exactRef()
	if err := ValidateVersionRef(valid); err != nil {
		t.Fatal(err)
	}
	invalid := []core.VersionRef{
		{ArtifactID: "artifact", RevisionID: valid.RevisionID, ContentHash: valid.ContentHash},
		{ArtifactID: valid.ArtifactID, RevisionID: "revision", ContentHash: valid.ContentHash},
		{ArtifactID: valid.ArtifactID, RevisionID: valid.RevisionID, ContentHash: "sha256:abc"},
		{ArtifactID: valid.ArtifactID, RevisionID: valid.RevisionID, ContentHash: "sha256:" + strings.Repeat("A", 64)},
	}
	for _, candidate := range invalid {
		if err := ValidateVersionRef(candidate); err == nil {
			t.Fatalf("expected exact reference rejection: %+v", candidate)
		}
	}
	anchor := "block-1"
	anchored := valid
	anchored.AnchorID = &anchor
	if err := ValidateVersionRef(anchored); err != nil {
		t.Fatal(err)
	}
	otherAnchor := "block-1"
	copy := anchored
	copy.AnchorID = &otherAnchor
	if !exactVersionRefEqual(anchored, copy) {
		t.Fatal("equal anchor values must not depend on pointer identity")
	}
}

func TestDecodeWorkspaceRejectsTraversalAndCaseCollisions(t *testing.T) {
	for _, payload := range []string{
		`{"files":[{"path":"../secret","content":"x"}]}`,
		`{"files":[{"path":"index.html","content":"a"},{"path":"INDEX.HTML","content":"b"}]}`,
		`{"files":[{"path":"node_modules/x.js","content":"x"}]}`,
	} {
		if _, _, err := decodeWorkspace(json.RawMessage(payload)); err == nil {
			t.Fatalf("unsafe workspace accepted: %s", payload)
		}
	}
}

func TestArchiveIsDeterministicNamespacedAndRejectsCollisions(t *testing.T) {
	entries := []archiveEntry{
		{Name: "source/index.html", Data: []byte("<h1>safe</h1>")},
		{Name: "worksflow-export.json", Data: []byte(`{"schemaVersion":1}`)},
	}
	first, err := createArchive(entries)
	if err != nil {
		t.Fatal(err)
	}
	second, err := createArchive(entries)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("exact exports must be byte-for-byte deterministic")
	}
	reader, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 2 || reader.File[0].Name != "source/index.html" || reader.File[1].Name != "worksflow-export.json" {
		t.Fatalf("unexpected archive entries: %+v", reader.File)
	}
	if _, err := createArchive([]archiveEntry{{Name: "A.txt", Data: []byte("a")}, {Name: "a.TXT", Data: []byte("b")}}); err == nil {
		t.Fatal("case-insensitive duplicate archive paths must fail closed")
	}
	if _, err := createArchive([]archiveEntry{{Name: "../escape", Data: []byte("x")}}); err == nil {
		t.Fatal("archive traversal must fail closed")
	}
}

func TestRedactJSONRemovesNestedCredentials(t *testing.T) {
	payload := json.RawMessage(`{"api_key":"super-secret-value-123","nested":["sk-abcdefghijklmnopq"]}`)
	redacted, changed, err := redactJSON(payload)
	if err != nil || !changed {
		t.Fatalf("redaction failed: changed=%v err=%v", changed, err)
	}
	value := string(redacted)
	if strings.Contains(value, "super-secret") || strings.Contains(value, "sk-abcdefgh") || !strings.Contains(value, "REDACTED") {
		t.Fatalf("sensitive value survived redaction: %s", value)
	}
}

func TestLocalStaticProviderPublishesImmutableSanitizedVersion(t *testing.T) {
	root := t.TempDir()
	provider, err := NewLocalStaticProvider(root, "https://preview.example.test/apps")
	if err != nil {
		t.Fatal(err)
	}
	deploymentID, versionID := uuid.NewString(), uuid.NewString()
	result, err := provider.Deploy(context.Background(), ProviderRequest{
		DeploymentID: deploymentID, VersionID: versionID, Environment: EnvironmentPreview,
		BuildArtifact: testBuildArtifact(t, []WorkspaceFile{
			{Path: "assets/app.js", Content: "window.ready=true"},
			{Path: "pages/home page.html", Content: `<html><head></head><body><iframe src="https://evil.test"></iframe><h1>ok</h1></body></html>`},
		}, "pages/home page.html"),
		PublicEnvironment: map[string]string{"VITE_PUBLIC_API": "https://api.example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FileCount != 2 || result.EntryPath != "pages/home page.html" || !strings.HasSuffix(result.PublicURL, "/pages/home%20page.html") {
		t.Fatalf("provider metadata does not describe exact deployed files: %+v", result)
	}
	entry, err := os.ReadFile(filepath.Join(root, deploymentID, "versions", versionID, "pages", "home page.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(entry)), "iframe") || !strings.Contains(string(entry), "window.__WORKSFLOW_ENV__") || !strings.Contains(string(entry), "VITE_PUBLIC_API") {
		t.Fatalf("published HTML was not sanitized/injected: %s", entry)
	}
	if _, err := provider.Deploy(context.Background(), ProviderRequest{
		DeploymentID: deploymentID, VersionID: versionID, Environment: EnvironmentPreview,
		BuildArtifact: testBuildArtifact(t, []WorkspaceFile{{Path: "index.html", Content: "new"}}, "index.html"),
	}); err == nil {
		t.Fatal("an immutable deployment version was overwritten")
	}

	request := httptest.NewRequest(http.MethodGet, "/published", nil)
	response := httptest.NewRecorder()
	provider.ServeAsset(response, request, deploymentID, versionID, "pages/home page.html")
	csp := response.Header().Get("Content-Security-Policy")
	if response.Code != http.StatusOK || response.Header().Get("ETag") == "" || !strings.Contains(csp, "sandbox") ||
		!strings.Contains(csp, "connect-src 'self' https://api.example.test") || strings.Contains(csp, "connect-src 'none'") {
		t.Fatalf("static security response is incomplete: status=%d headers=%v", response.Code, response.Header())
	}
	metadata := httptest.NewRecorder()
	provider.ServeAsset(metadata, httptest.NewRequest(http.MethodGet, "/published", nil), deploymentID, versionID, ".worksflow/connect-origins.json")
	if metadata.Code != http.StatusNotFound {
		t.Fatalf("internal CSP metadata became public: %d", metadata.Code)
	}
	conditional := httptest.NewRequest(http.MethodGet, "/published", nil)
	conditional.Header.Set("If-None-Match", response.Header().Get("ETag"))
	conditionalResponse := httptest.NewRecorder()
	provider.ServeAsset(conditionalResponse, conditional, deploymentID, versionID, "pages/home page.html")
	if conditionalResponse.Code != http.StatusNotModified || conditionalResponse.Body.Len() != 0 {
		t.Fatalf("immutable conditional request was not honored: %d", conditionalResponse.Code)
	}
	traversal := httptest.NewRecorder()
	provider.ServeAsset(traversal, httptest.NewRequest(http.MethodGet, "/published", nil), deploymentID, versionID, "../secret")
	if traversal.Code != http.StatusNotFound {
		t.Fatalf("traversal status=%d", traversal.Code)
	}
}

func TestLocalStaticProviderIsPreviewOnly(t *testing.T) {
	provider, err := NewLocalStaticProvider(t.TempDir(), "https://preview.example.test/apps")
	if err != nil {
		t.Fatal(err)
	}
	deploymentID, versionID := uuid.NewString(), uuid.NewString()
	_, err = provider.Deploy(context.Background(), ProviderRequest{
		DeploymentID: deploymentID,
		VersionID:    versionID,
		Environment:  EnvironmentProduction,
		BuildArtifact: testBuildArtifact(
			t, []WorkspaceFile{{Path: "index.html", Content: "production"}}, "index.html",
		),
	})
	typed, ok := AsError(err)
	if !ok || typed.Code != CodeConflict || typed.Status != http.StatusConflict || typed.Detail != legacyProductionControllerConflictDetail {
		t.Fatalf("LocalStaticProvider accepted production authority: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(provider.rootDirectory, deploymentID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("production rejection left provider state behind: %v", statErr)
	}
}

func TestPublicConnectOriginsRejectCredentialsAndNonHTTPProtocols(t *testing.T) {
	t.Parallel()
	origins, err := publicConnectOrigins(map[string]string{
		"PUBLIC_API":   "https://api.example.test/v1?q=1",
		"VITE_SOCKET":  "wss://socket.example.test/events",
		"PUBLIC_LABEL": "not a URL",
	})
	if err != nil || strings.Join(origins, ",") != "https://api.example.test,wss://socket.example.test" {
		t.Fatalf("minimal public origin set is incorrect: %#v %v", origins, err)
	}
	for _, value := range []string{"https://user:password@example.test/v1", "ftp://files.example.test", "javascript:alert(1)", "//example.test/path"} {
		if _, err := publicConnectOrigins(map[string]string{"PUBLIC_URL": value}); err == nil {
			t.Fatalf("unsafe public URL was accepted: %s", value)
		}
	}
}

func TestLocalStaticProviderRejectsUnsafeConfigurationAndInput(t *testing.T) {
	for _, base := range []string{"javascript:alert(1)", "//evil.test/path", "https://user:pass@example.test/path", "/published/../escape"} {
		if _, err := NewLocalStaticProvider(filepath.Join(t.TempDir(), "publish"), base); err == nil {
			t.Fatalf("unsafe base URL accepted: %s", base)
		}
	}
	provider, err := NewLocalStaticProvider(t.TempDir(), "/published")
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Deploy(context.Background(), ProviderRequest{
		DeploymentID: uuid.NewString(), VersionID: uuid.NewString(), Environment: EnvironmentPreview,
		BuildArtifact: testBuildArtifact(t, []WorkspaceFile{{Path: "index.html", Content: "a"}, {Path: "INDEX.HTML", Content: "b"}}, "index.html"),
	})
	if err == nil {
		t.Fatal("case-insensitive duplicate publish paths were accepted")
	}
	_, err = provider.Deploy(context.Background(), ProviderRequest{
		DeploymentID: uuid.NewString(), VersionID: uuid.NewString(), Environment: EnvironmentPreview,
		BuildArtifact:     testBuildArtifact(t, []WorkspaceFile{{Path: "index.html", Content: "ok"}}, "index.html"),
		PublicEnvironment: map[string]string{"SECRET": "not-public"},
	})
	if err == nil {
		t.Fatal("non-public environment variable was injected")
	}
}

type testSandbox struct {
	t              *testing.T
	requests       []SandboxRequest
	forbiddenValue string
}

func (s *testSandbox) Kind() string { return "test-container" }

func (s *testSandbox) PrepareDependencies(_ context.Context, directory string, request DependencyPreparationRequest) (SandboxResult, error) {
	if request.Ecosystem != "node" {
		s.t.Fatalf("unexpected dependency ecosystem: %s", request.Ecosystem)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		s.t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "package.json" && entry.Name() != "package-lock.json" {
			s.t.Fatalf("source reached network dependency phase: %s", entry.Name())
		}
	}
	if err := os.Mkdir(filepath.Join(directory, "node_modules"), 0o700); err != nil {
		s.t.Fatal(err)
	}
	return SandboxResult{ExitCode: 0, Duration: time.Millisecond}, nil
}

func (s *testSandbox) Run(_ context.Context, directory string, request SandboxRequest) (SandboxResult, error) {
	s.requests = append(s.requests, request)
	if _, err := os.Stat(filepath.Join(directory, ".env")); !errors.Is(err, os.ErrNotExist) {
		s.t.Fatalf("secret-bearing file was materialized into quality sandbox: %v", err)
	}
	app, err := os.ReadFile(filepath.Join(directory, "app.ts"))
	if err != nil {
		s.t.Fatal(err)
	}
	if strings.Contains(string(app), s.forbiddenValue) {
		s.t.Fatal("credential-like source reached the sandbox without redaction")
	}
	return SandboxResult{ExitCode: 0, Output: s.forbiddenValue, Duration: time.Millisecond}, nil
}

func TestQualityChecksUseFixedSandboxAndPreserveStaticFindings(t *testing.T) {
	secret := "sk-abcdefghijklmnopq"
	sandbox := &testSandbox{t: t, forbiddenValue: secret}
	service := &QualityService{sandbox: sandbox, tempRoot: t.TempDir()}
	workspace := WorkspaceSnapshot{Files: []WorkspaceFile{
		{Path: "package.json", Content: `{"scripts":{"build":"next build","lint":"eslint .","test":"vitest"},"dependencies":{"demo":"latest"}}`},
		{Path: "package-lock.json", Content: `{"name":"demo","lockfileVersion":3,"packages":{"":{"dependencies":{"demo":"latest"}},"node_modules/demo":{"version":"1.0.0","resolved":"https://registry.npmjs.org/demo/-/demo-1.0.0.tgz","integrity":"sha512-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="}}}`},
		{Path: "tsconfig.json", Content: `{}`},
		{Path: "app.ts", Content: `const token = "` + secret + `"`},
		{Path: ".env", Content: "TOKEN=" + secret},
		{Path: "index.html", Content: `<html><body><img src="x"><button></button></body></html>`},
	}}
	results := service.runChecks(context.Background(), workspace)
	if len(results) != len(RequiredChecks) || len(sandbox.requests) != 4 {
		t.Fatalf("quality profile coverage mismatch: checks=%d sandbox=%d", len(results), len(sandbox.requests))
	}
	byID := map[CheckID]CheckResult{}
	for _, result := range results {
		byID[result.ID] = result
	}
	if byID[CheckAccessibility].Status != CheckFailed || byID[CheckSecret].Status != CheckFailed || byID[CheckDependency].Status != CheckWarning {
		t.Fatalf("static checks did not preserve findings: %+v", byID)
	}
	if strings.Contains(byID[CheckBuild].Output, secret) || !strings.Contains(byID[CheckBuild].Output, "REDACTED") {
		t.Fatalf("sandbox output was not redacted: %q", byID[CheckBuild].Output)
	}
}

type fakeAccess struct{}

func (fakeAccess) Authorize(context.Context, string, string, core.Action) (core.Role, error) {
	return core.RoleOwner, nil
}

type fakeContentStore struct{}

func (fakeContentStore) PutPending(context.Context, string, string, string, int, json.RawMessage) (content.Reference, error) {
	return content.Reference{}, nil
}
func (fakeContentStore) Finalize(context.Context, string) error { return nil }
func (fakeContentStore) Abort(context.Context, string) error    { return nil }
func (fakeContentStore) Get(context.Context, string, string) (content.StoredContent, error) {
	return content.StoredContent{}, nil
}

type fakeProvider struct{}

func (fakeProvider) Name() string { return "fake" }
func (fakeProvider) Deploy(context.Context, ProviderRequest) (ProviderResult, error) {
	return ProviderResult{}, nil
}

func dryRunDeliveryDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 user=worksflow dbname=worksflow sslmode=disable", PreferSimpleProtocol: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func TestPlatformFactoryRequiresExplicitSafeImplementations(t *testing.T) {
	if _, err := NewPlatformServices(PlatformDependencies{}); err == nil {
		t.Fatal("empty platform dependencies were accepted")
	}
	services, err := NewPlatformServices(PlatformDependencies{
		Database: dryRunDeliveryDB(t), Contents: fakeContentStore{}, Access: fakeAccess{},
		Sandbox: &testSandbox{t: t}, Provider: fakeProvider{}, ReleaseBundles: publishReleaseStub{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if services.Quality == nil || services.Export == nil || services.Publish == nil || services.StaticAssets != nil {
		t.Fatalf("unexpected external-provider platform wiring: %+v", services)
	}
	local, err := NewLocalStaticProvider(t.TempDir(), "/published")
	if err != nil {
		t.Fatal(err)
	}
	services, err = NewPlatformServices(PlatformDependencies{
		Database: dryRunDeliveryDB(t), Contents: fakeContentStore{}, Access: fakeAccess{},
		Sandbox: &testSandbox{t: t}, Provider: local, ReleaseBundles: publishReleaseStub{},
	})
	if err != nil || services.StaticAssets == nil {
		t.Fatalf("local static asset gate was not wired: services=%+v err=%v", services, err)
	}
}

type publicEnvironmentSource struct {
	scope dataruntime.EnvironmentScope
	seen  bool
}

func (s *publicEnvironmentSource) PublicEnvironment(_ context.Context, _, _ string, scope dataruntime.EnvironmentScope) (map[string]string, error) {
	s.scope, s.seen = scope, true
	return map[string]string{"VITE_PUBLIC_API": "https://api.example.test"}, nil
}

func TestDataRuntimeEnvironmentResolverUsesPublicCapabilityOnly(t *testing.T) {
	source := &publicEnvironmentSource{}
	resolved, err := (DataRuntimeEnvironmentResolver{Source: source}).Resolve(context.Background(), uuid.NewString(), EnvironmentProduction, "", uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if !source.seen || source.scope != dataruntime.ScopeProduction || resolved.Reference != "data-runtime:production" || resolved.Public["VITE_PUBLIC_API"] == "" {
		t.Fatalf("unexpected public environment resolution: %+v", resolved)
	}
	resolved.Public["VITE_PUBLIC_API"] = "changed"
	if source.scope != dataruntime.ScopeProduction {
		t.Fatal("resolver state changed unexpectedly")
	}
}

func TestValidateProviderResultRejectsUnsafeURLAndMalformedChecksum(t *testing.T) {
	digest := sha256.Sum256([]byte("content"))
	valid := ProviderResult{
		Reference: "deployment/version", PublicURL: "/published/deployment/version/",
		Checksum: "sha256:" + hex.EncodeToString(digest[:]), EntryPath: "index.html",
		FileCount: 1, TotalBytes: 7,
	}
	if err := validateProviderResult(valid); err != nil {
		t.Fatal(err)
	}
	unsafe := valid
	unsafe.PublicURL = "javascript:alert(1)"
	if err := validateProviderResult(unsafe); err == nil {
		t.Fatal("unsafe provider URL was accepted")
	}
	unsafe = valid
	unsafe.Checksum = "sha256:" + strings.Repeat("z", 64)
	if err := validateProviderResult(unsafe); err == nil {
		t.Fatal("non-hex provider checksum was accepted")
	}
}

func readZipEntry(t *testing.T, archive []byte, name string) string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		opened, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer opened.Close()
		payload, err := io.ReadAll(opened)
		if err != nil {
			t.Fatal(err)
		}
		return string(payload)
	}
	t.Fatalf("archive entry %s not found", name)
	return ""
}
