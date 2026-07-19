package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
)

type LocalStaticProvider struct {
	rootDirectory string
	baseURL       string
}

func NewLocalStaticProvider(rootDirectory, baseURL string) (*LocalStaticProvider, error) {
	if strings.TrimSpace(rootDirectory) == "" {
		return nil, errors.New("local publish root directory is required")
	}
	var err error
	baseURL, err = normalizePublishBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(rootDirectory)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create local publish root: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("local publish root must be a real directory, not a symlink")
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("protect local publish root: %w", err)
	}
	return &LocalStaticProvider{rootDirectory: absolute, baseURL: baseURL}, nil
}

func (*LocalStaticProvider) Name() string { return "local-static" }

func (p *LocalStaticProvider) PublicDeploymentOrigins(_ ProviderRequest) ([]string, error) {
	parsed, err := url.Parse(p.baseURL)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return nil, errors.New("DELIVERY_PUBLISH_BASE_URL must be absolute before public application data can be enabled")
	}
	return []string{strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)}, nil
}

func (p *LocalStaticProvider) Readiness(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	info, err := os.Lstat(p.rootDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("publish root is unavailable")
	}
	probe, err := os.CreateTemp(p.rootDirectory, ".readiness-")
	if err != nil {
		return fmt.Errorf("publish root is not writable: %w", err)
	}
	name := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		_ = os.Remove(name)
		return closeErr
	}
	return os.Remove(name)
}

func (p *LocalStaticProvider) Deploy(ctx context.Context, request ProviderRequest) (ProviderResult, error) {
	if _, err := uuid.Parse(request.DeploymentID); err != nil {
		return ProviderResult{}, Invalid("deploymentId", "deploymentId must be a UUID")
	}
	if _, err := uuid.Parse(request.VersionID); err != nil {
		return ProviderResult{}, Invalid("versionId", "versionId must be a UUID")
	}
	if !request.Environment.Valid() {
		return ProviderResult{}, Invalid("environment", "environment must be preview or production")
	}
	if request.Environment != EnvironmentPreview {
		return ProviderResult{}, conflict(legacyProductionControllerConflictDetail)
	}
	environmentRef := strings.TrimSpace(request.EnvironmentRef)
	if environmentRef == "" {
		environmentRef = "default"
	}
	if err := validateResolvedEnvironment(ResolvedEnvironment{Reference: environmentRef, Public: request.PublicEnvironment}); err != nil {
		return ProviderResult{}, err
	}
	if err := validateBuildArtifact(request.BuildArtifact); err != nil {
		return ProviderResult{}, err
	}
	entryPath, err := selectBuildEntry(request.BuildArtifact.Files, request.BuildArtifact.EntryPath)
	if err != nil {
		return ProviderResult{}, err
	}
	connectOrigins, err := publicConnectOrigins(request.PublicEnvironment)
	if err != nil {
		return ProviderResult{}, err
	}
	deploymentDirectory := filepath.Join(p.rootDirectory, request.DeploymentID, "versions")
	if err := os.MkdirAll(deploymentDirectory, 0o700); err != nil {
		return ProviderResult{}, &DeliveryError{Code: CodeProviderFailure, Status: 502, Detail: "local publish provider could not create deployment storage", Cause: err}
	}
	target := filepath.Join(deploymentDirectory, request.VersionID)
	if _, err := os.Stat(target); err == nil {
		return ProviderResult{}, conflict("immutable local deployment version already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return ProviderResult{}, wrapInternal("inspect local deployment version", err)
	}
	temporary, err := os.MkdirTemp(deploymentDirectory, ".stage-")
	if err != nil {
		return ProviderResult{}, &DeliveryError{Code: CodeProviderFailure, Status: 502, Detail: "local publish provider could not create a staging directory", Cause: err}
	}
	deployed := false
	defer func() {
		if !deployed {
			_ = os.RemoveAll(temporary)
		}
	}()
	files := append([]BuildArtifactFile(nil), request.BuildArtifact.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	hash := sha256.New()
	totalBytes := int64(0)
	seen := map[string]bool{}
	for _, file := range files {
		select {
		case <-ctx.Done():
			return ProviderResult{}, ctx.Err()
		default:
		}
		path, err := SanitizePath(file.Path)
		if err != nil {
			return ProviderResult{}, err
		}
		canonical := strings.ToLower(path)
		if seen[canonical] {
			return ProviderResult{}, Invalid("files", "published workspace contains duplicate case-insensitive paths")
		}
		seen[canonical] = true
		if SensitivePath(path) {
			return ProviderResult{}, NewError(CodeSensitiveContent, 409, "secret-bearing files cannot be published")
		}
		content, err := decodeBuildFile(file)
		if err != nil {
			return ProviderResult{}, err
		}
		if strings.HasSuffix(strings.ToLower(path), ".html") || strings.HasSuffix(strings.ToLower(path), ".htm") {
			content = []byte(sanitizePublishedHTML(string(content)))
		}
		if path == entryPath {
			injected, injectErr := injectPublicEnvironment(string(content), request.Environment, request.PublicEnvironment)
			err = injectErr
			if err != nil {
				return ProviderResult{}, err
			}
			content = []byte(injected)
		}
		targetFile := filepath.Join(temporary, filepath.FromSlash(path))
		if err := ensureContained(temporary, targetFile); err != nil {
			return ProviderResult{}, err
		}
		if err := os.MkdirAll(filepath.Dir(targetFile), 0o700); err != nil {
			return ProviderResult{}, wrapInternal("create local publish directory", err)
		}
		if err := os.WriteFile(targetFile, content, 0o600); err != nil {
			return ProviderResult{}, wrapInternal("write local published file", err)
		}
		totalBytes += int64(len(content))
		if totalBytes > MaxWorkspaceBytes {
			return ProviderResult{}, NewError(CodeOutputLimit, 413, "published content exceeds the configured size limit")
		}
		_, _ = io.WriteString(hash, path)
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
	}
	metadataDirectory := filepath.Join(temporary, ".worksflow")
	if err := os.MkdirAll(metadataDirectory, 0o700); err != nil {
		return ProviderResult{}, wrapInternal("create immutable publish metadata", err)
	}
	originPayload, err := json.Marshal(connectOrigins)
	if err != nil {
		return ProviderResult{}, wrapInternal("encode immutable connect policy", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDirectory, "connect-origins.json"), originPayload, 0o600); err != nil {
		return ProviderResult{}, wrapInternal("write immutable connect policy", err)
	}
	if err := os.Rename(temporary, target); err != nil {
		return ProviderResult{}, &DeliveryError{Code: CodeProviderFailure, Status: 502, Detail: "local publish provider could not activate immutable files", Cause: err}
	}
	deployed = true
	reference := request.DeploymentID + "/versions/" + request.VersionID
	publicURL := p.baseURL + "/" + request.DeploymentID + "/" + request.VersionID + "/"
	if entryPath != "index.html" {
		publicURL += escapeAssetPath(entryPath)
	}
	return ProviderResult{
		Reference: reference, PublicURL: publicURL,
		Checksum: "sha256:" + hex.EncodeToString(hash.Sum(nil)), EntryPath: entryPath,
		FileCount: len(files), TotalBytes: totalBytes,
	}, nil
}

func (p *LocalStaticProvider) ServeAsset(response http.ResponseWriter, request *http.Request, deploymentID, versionID, asset string) {
	if _, err := uuid.Parse(deploymentID); err != nil {
		http.NotFound(response, request)
		return
	}
	if _, err := uuid.Parse(versionID); err != nil {
		http.NotFound(response, request)
		return
	}
	asset = strings.TrimPrefix(asset, "/")
	directoryRequest := strings.HasSuffix(asset, "/")
	if directoryRequest {
		asset = strings.TrimSuffix(asset, "/")
	}
	if asset == "" {
		asset = "index.html"
		directoryRequest = false
	}
	path, err := SanitizePath(asset)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	if path == ".worksflow" || strings.HasPrefix(path, ".worksflow/") {
		http.NotFound(response, request)
		return
	}
	root := filepath.Join(p.rootDirectory, deploymentID, "versions", versionID)
	target := filepath.Join(root, filepath.FromSlash(path))
	if ensureContained(root, target) != nil {
		http.NotFound(response, request)
		return
	}
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		http.NotFound(response, request)
		return
	}
	redirectDirectory := !directoryRequest && info.IsDir()
	directoryPath := path
	if directoryRequest || info.IsDir() {
		if !info.IsDir() {
			http.NotFound(response, request)
			return
		}
		path, err = SanitizePath(pathpkg.Join(path, "index.html"))
		if err != nil {
			http.NotFound(response, request)
			return
		}
		target = filepath.Join(root, filepath.FromSlash(path))
		if ensureContained(root, target) != nil {
			http.NotFound(response, request)
			return
		}
		info, err = os.Lstat(target)
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		http.NotFound(response, request)
		return
	}
	if redirectDirectory {
		location, err := url.Parse(p.baseURL + "/" + deploymentID + "/" + versionID + "/" + escapeAssetPath(directoryPath) + "/")
		if err != nil {
			http.NotFound(response, request)
			return
		}
		location.RawQuery = request.URL.RawQuery
		location.ForceQuery = request.URL.ForceQuery
		response.Header().Set("Location", location.String())
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		response.WriteHeader(http.StatusPermanentRedirect)
		return
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	mediaType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	response.Header().Set("Content-Type", mediaType)
	digest := sha256.Sum256(contents)
	etag := `"sha256:` + hex.EncodeToString(digest[:]) + `"`
	response.Header().Set("ETag", etag)
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	connectSources := []string{"'self'"}
	if storedOrigins, err := readConnectOrigins(root); err == nil {
		connectSources = append(connectSources, storedOrigins...)
	}
	response.Header().Set("Content-Security-Policy", "default-src 'self' data: blob:; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src "+strings.Join(connectSources, " ")+"; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; sandbox allow-scripts allow-forms allow-modals allow-popups")
	response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if request.Header.Get("If-None-Match") == etag {
		response.WriteHeader(http.StatusNotModified)
		return
	}
	response.Header().Set("Content-Length", fmt.Sprintf("%d", len(contents)))
	response.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	_, _ = response.Write(contents)
}

func publicConnectOrigins(values map[string]string) ([]string, error) {
	origins := map[string]bool{}
	for name, raw := range values {
		if !publicEnvironmentNamePattern.MatchString(name) {
			return nil, Invalid("environment", "resolved public environment contains an invalid variable name")
		}
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		parsed, err := url.Parse(value)
		if err != nil {
			return nil, Invalid("environment", "public URL value is malformed")
		}
		if parsed.Scheme == "" {
			if strings.HasPrefix(value, "//") {
				return nil, Invalid("environment", "protocol-relative public URLs are forbidden")
			}
			continue
		}
		scheme := strings.ToLower(parsed.Scheme)
		if scheme != "http" && scheme != "https" && scheme != "ws" && scheme != "wss" {
			return nil, Invalid("environment", "public URL values must use http, https, ws, or wss")
		}
		if parsed.Host == "" || parsed.User != nil || strings.ContainsAny(parsed.Host, "\r\n\t '\";") {
			return nil, Invalid("environment", "public URL values must use a credential-free safe origin")
		}
		origin := scheme + "://" + strings.ToLower(parsed.Host)
		origins[origin] = true
	}
	result := make([]string, 0, len(origins))
	for origin := range origins {
		result = append(result, origin)
	}
	sort.Strings(result)
	return result, nil
}

func readConnectOrigins(root string) ([]string, error) {
	payload, err := os.ReadFile(filepath.Join(root, ".worksflow", "connect-origins.json"))
	if err != nil {
		return nil, err
	}
	var origins []string
	if err := json.Unmarshal(payload, &origins); err != nil || len(origins) > 128 {
		return nil, errors.New("stored connect origin policy is invalid")
	}
	for _, origin := range origins {
		validated, err := publicConnectOrigins(map[string]string{"PUBLIC_URL": origin})
		if err != nil || len(validated) != 1 || validated[0] != origin {
			return nil, errors.New("stored connect origin policy is invalid")
		}
	}
	return origins, nil
}

func normalizePublishBaseURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		value = "/published"
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil || parsed.RawPath != "" {
		return "", errors.New("local publish base URL must be an HTTP(S) URL or a clean absolute path without credentials, query, or fragment")
	}
	if parsed.IsAbs() {
		if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return "", errors.New("local publish base URL must use HTTP or HTTPS")
		}
	} else if parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") {
		return "", errors.New("relative local publish base URL must be an absolute-path reference")
	}
	if parsed.Path != "" {
		if cleaned := pathpkg.Clean(parsed.Path); cleaned != parsed.Path || strings.Contains(parsed.Path, "\\") {
			return "", errors.New("local publish base URL path must not contain traversal or normalization segments")
		}
	}
	return value, nil
}

func escapeAssetPath(value string) string {
	segments := strings.Split(value, "/")
	for index := range segments {
		segments[index] = url.PathEscape(segments[index])
	}
	return strings.Join(segments, "/")
}

func validatePublicURL(value string) error {
	if len(value) > 4_096 || strings.ContainsRune(value, '\x00') {
		return Invalid("provider.publicUrl", "provider publicUrl exceeds its safe length")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Fragment != "" {
		return Invalid("provider.publicUrl", "provider publicUrl must be a safe HTTP(S) URL or absolute-path reference")
	}
	if parsed.IsAbs() {
		if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return Invalid("provider.publicUrl", "provider publicUrl must use HTTP or HTTPS")
		}
	} else if parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") {
		return Invalid("provider.publicUrl", "provider publicUrl must be an absolute-path reference")
	}
	return nil
}

func selectStaticEntry(files []WorkspaceFile, requested string) (string, error) {
	byPath := map[string]bool{}
	for _, file := range files {
		byPath[file.Path] = true
	}
	if requested != "" {
		path, err := SanitizePath(requested)
		if err != nil {
			return "", err
		}
		if !byPath[path] || !strings.HasSuffix(strings.ToLower(path), ".html") {
			return "", Invalid("entryPath", "entryPath must identify an HTML file in the frozen workspace")
		}
		return path, nil
	}
	candidates := []string{}
	for path := range byPath {
		if strings.HasSuffix(strings.ToLower(path), ".html") || strings.HasSuffix(strings.ToLower(path), ".htm") {
			candidates = append(candidates, path)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		leftIndex := candidates[i] == "index.html"
		rightIndex := candidates[j] == "index.html"
		if leftIndex != rightIndex {
			return leftIndex
		}
		return candidates[i] < candidates[j]
	})
	if len(candidates) == 0 {
		return "", Invalid("workspaceRevision", "local static publishing requires an HTML entry file")
	}
	return candidates[0], nil
}

func ensureContained(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return NewError(CodeUnsafePath, 422, "published path escapes its immutable version directory")
	}
	return nil
}

var (
	unsafeElementPattern = regexp.MustCompile(`(?is)<(?:iframe|frame|frameset|object|embed|applet|portal)\b[^>]*>(?:.*?)</(?:iframe|frame|frameset|object|embed|applet|portal)\s*>`)
	unsafeVoidPattern    = regexp.MustCompile(`(?is)<(?:iframe|frame|frameset|object|embed|applet|portal)\b[^>]*/?\s*>`)
	basePattern          = regexp.MustCompile(`(?is)<base\b[^>]*>`)
	metaRefreshPattern   = regexp.MustCompile(`(?is)<meta\b[^>]*http-equiv\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)[^>]*>`)
	unsafeURLPattern     = regexp.MustCompile(`(?i)\s(?:href|src|action|formaction|poster)\s*=\s*(?:"\s*(?:javascript|vbscript|file|data\s*:\s*text/html)[^"]*"|'\s*(?:javascript|vbscript|file|data\s*:\s*text/html)[^']*')`)
)

func sanitizePublishedHTML(value string) string {
	value = strings.ReplaceAll(value, "\x00", "")
	value = unsafeElementPattern.ReplaceAllString(value, "")
	value = unsafeVoidPattern.ReplaceAllString(value, "")
	value = basePattern.ReplaceAllString(value, "")
	value = metaRefreshPattern.ReplaceAllString(value, "")
	value = unsafeURLPattern.ReplaceAllString(value, "")
	if !strings.Contains(strings.ToLower(value), "<!doctype") {
		value = "<!doctype html>\n" + value
	}
	return value
}

func injectPublicEnvironment(document string, environment Environment, values map[string]string) (string, error) {
	if len(values) == 0 {
		return document, nil
	}
	for name := range values {
		if !publicEnvironmentNamePattern.MatchString(name) {
			return "", Invalid("environment", "resolved public environment contains an invalid variable name")
		}
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return "", wrapInternal("encode public environment", err)
	}
	encoded := strings.NewReplacer("<", `\u003c`, ">", `\u003e`, "&", `\u0026`).Replace(string(payload))
	script := `<script data-worksflow-environment="` + html.EscapeString(string(environment)) + `">window.__WORKSFLOW_ENV__=Object.freeze(` + encoded + `);</script>`
	lower := strings.ToLower(document)
	if index := strings.Index(lower, "</head>"); index >= 0 {
		return document[:index] + script + document[index:], nil
	}
	return script + document, nil
}
