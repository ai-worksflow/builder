package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/templates"
	"gorm.io/gorm"
)

const TemplateSourceLockSchemaVersion = "template-source-lock/v1"

var (
	ErrTemplateSourceInvalid     = errors.New("invalid exact TemplateRelease source")
	ErrTemplateSourceUnavailable = errors.New("exact TemplateRelease source is unavailable")
	ErrTemplateSourceDrift       = errors.New("TemplateRelease source tree differs from admission")
	ErrTemplateSourceLimit       = errors.New("TemplateRelease source exceeds repository limits")

	gitObjectPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

type TemplateSourceFile struct {
	Path    string
	Mode    string
	Content []byte
}

type TemplateSourceComponent struct {
	Role                        string `json:"role" gorm:"column:role"`
	MountPath                   string `json:"mountPath" gorm:"column:mount_path"`
	ReleaseID                   string `json:"releaseId" gorm:"column:release_id"`
	ReleaseContentHash          string `json:"releaseContentHash" gorm:"column:release_content_hash"`
	ReleaseSubjectHash          string `json:"releaseSubjectHash" gorm:"column:release_subject_hash"`
	Repository                  string `json:"repository" gorm:"column:source_repository"`
	Branch                      string `json:"branch" gorm:"column:source_branch"`
	Commit                      string `json:"commit" gorm:"column:source_commit"`
	TreeHash                    string `json:"treeHash" gorm:"column:tree_hash"`
	SBOMDigest                  string `json:"sbomDigest" gorm:"column:sbom_digest"`
	SignatureBundleDigest       string `json:"signatureBundleDigest" gorm:"column:signature_bundle_digest"`
	AuthorityReceiptID          string `json:"authorityReceiptId" gorm:"column:authority_receipt_id"`
	AuthorityReceiptContentHash string `json:"authorityReceiptContentHash" gorm:"column:authority_receipt_content_hash"`
	AuthorityPolicyHash         string `json:"authorityPolicyHash" gorm:"column:authority_policy_hash"`
}

type TemplateSourceRequest struct {
	FullStackTemplate ExactReference
	BuildContract     ExactReference
	Components        []TemplateSourceComponent
}

type TemplateSourceMaterializer interface {
	Materialize(context.Context, TemplateSourceRequest) ([]TemplateSourceFile, error)
}

type GitTemplateSourceOptions struct {
	GitBinary    string
	CacheRoot    string
	AllowedHosts []string
	FetchTimeout time.Duration
}

type templateGitRunner interface {
	Run(context.Context, int64, ...string) ([]byte, error)
}

type GitTemplateSourceMaterializer struct {
	git          templateGitRunner
	cacheRoot    string
	allowedHosts map[string]struct{}
	fetchTimeout time.Duration
	mu           sync.Mutex
}

var _ templates.ArtifactSourceVerifier = (*GitTemplateSourceMaterializer)(nil)

// Readiness verifies the exact-source boundary still has a real cache root and
// an executable Git client. It performs no mutation and is used by Template
// Artifact Authority readiness before admissions are accepted.
func (materializer *GitTemplateSourceMaterializer) Readiness(ctx context.Context) error {
	if materializer == nil || materializer.git == nil || ctx == nil {
		return fmt.Errorf("%w: Git source verifier is not configured", ErrTemplateSourceUnavailable)
	}
	info, err := os.Lstat(materializer.cacheRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: source cache is not a real directory", ErrTemplateSourceUnavailable)
	}
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := materializer.git.Run(probe, 64<<10, "--version"); err != nil {
		return fmt.Errorf("%w: Git readiness probe: %v", ErrTemplateSourceUnavailable, err)
	}
	return nil
}

// VerifySource independently fetches the exact candidate commit, recomputes
// its raw `git ls-tree -r --full-tree -z` SHA-256, and reads every admitted
// regular blob through the same size/path/mode limits used by bootstrap.
func (materializer *GitTemplateSourceMaterializer) VerifySource(ctx context.Context, source templates.TemplateSource) error {
	if materializer == nil || materializer.git == nil || ctx == nil {
		return fmt.Errorf("%w: Git source verifier is not configured", ErrTemplateSourceUnavailable)
	}
	repositoryURL := strings.TrimSpace(source.Repository)
	parsed, err := url.Parse(repositoryURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Port() != "" || !strings.HasSuffix(parsed.Path, ".git") ||
		parsed.String() != repositoryURL {
		return fmt.Errorf("%w: source repository is not a canonical credential-free HTTPS Git URL", ErrTemplateSourceInvalid)
	}
	if _, allowed := materializer.allowedHosts[strings.ToLower(parsed.Hostname())]; !allowed {
		return fmt.Errorf("%w: source repository host is not allowed", ErrTemplateSourceInvalid)
	}
	if !gitObjectPattern.MatchString(source.Commit) || !isCanonicalSHA256(source.TreeHash) ||
		strings.TrimSpace(source.Branch) == "" || strings.TrimSpace(source.Branch) != source.Branch ||
		strings.ContainsAny(source.Branch, "\r\n\x00") {
		return fmt.Errorf("%w: source commit, branch, or tree identity is invalid", ErrTemplateSourceInvalid)
	}
	identity := sha256.Sum256([]byte(repositoryURL))
	component := TemplateSourceComponent{
		ReleaseID: hex.EncodeToString(identity[:16]), Repository: repositoryURL,
		Branch: source.Branch, Commit: source.Commit, TreeHash: source.TreeHash,
	}
	verifyContext, cancel := context.WithTimeout(ctx, materializer.fetchTimeout)
	defer cancel()
	repositoryPath, err := materializer.ensureRepository(verifyContext, component)
	if err != nil {
		return err
	}
	_, err = materializer.readExactTree(verifyContext, repositoryPath, component)
	return err
}

func NewGitTemplateSourceMaterializer(options GitTemplateSourceOptions) (*GitTemplateSourceMaterializer, error) {
	binary := strings.TrimSpace(options.GitBinary)
	if binary == "" {
		binary = "git"
	}
	resolvedBinary, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("%w: git executable: %v", ErrTemplateSourceUnavailable, err)
	}
	return newGitTemplateSourceMaterializer(execTemplateGitRunner{binary: resolvedBinary}, options)
}

func newGitTemplateSourceMaterializer(
	runner templateGitRunner,
	options GitTemplateSourceOptions,
) (*GitTemplateSourceMaterializer, error) {
	if runner == nil {
		return nil, fmt.Errorf("%w: git command runner is required", ErrTemplateSourceInvalid)
	}
	root := strings.TrimSpace(options.CacheRoot)
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsAny(root, "\r\n\x00") {
		return nil, fmt.Errorf("%w: cache root must be an absolute normalized path", ErrTemplateSourceInvalid)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create source cache: %v", ErrTemplateSourceUnavailable, err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: source cache is not a real directory", ErrTemplateSourceUnavailable)
	}
	hosts := make(map[string]struct{}, len(options.AllowedHosts))
	for _, raw := range options.AllowedHosts {
		host := strings.ToLower(strings.TrimSpace(raw))
		if host == "" || strings.ContainsAny(host, "/:@\r\n\x00") {
			return nil, fmt.Errorf("%w: invalid allowed source host", ErrTemplateSourceInvalid)
		}
		hosts[host] = struct{}{}
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("%w: at least one exact source host is required", ErrTemplateSourceInvalid)
	}
	timeout := options.FetchTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if timeout > 10*time.Minute {
		return nil, fmt.Errorf("%w: fetch timeout exceeds ten minutes", ErrTemplateSourceInvalid)
	}
	return &GitTemplateSourceMaterializer{
		git: runner, cacheRoot: root, allowedHosts: hosts, fetchTimeout: timeout,
	}, nil
}

func (materializer *GitTemplateSourceMaterializer) Materialize(
	ctx context.Context,
	request TemplateSourceRequest,
) ([]TemplateSourceFile, error) {
	request, err := normalizeTemplateSourceRequest(request, materializer.allowedHosts)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, materializer.fetchTimeout)
	defer cancel()

	files := make([]TemplateSourceFile, 0)
	for _, component := range request.Components {
		repositoryPath, err := materializer.ensureRepository(ctx, component)
		if err != nil {
			return nil, err
		}
		componentFiles, err := materializer.readExactTree(ctx, repositoryPath, component)
		if err != nil {
			return nil, err
		}
		for _, file := range componentFiles {
			file.Path = path.Join(component.MountPath, file.Path)
			files = append(files, file)
		}
	}
	lock, err := templateSourceLock(request)
	if err != nil {
		return nil, err
	}
	files = append(files, TemplateSourceFile{
		Path: "templates.lock.json", Mode: "100644", Content: lock,
	})
	return validateTemplateSourceFiles(files)
}

func (materializer *GitTemplateSourceMaterializer) ensureRepository(
	ctx context.Context,
	component TemplateSourceComponent,
) (string, error) {
	materializer.mu.Lock()
	defer materializer.mu.Unlock()

	cachePath := filepath.Join(materializer.cacheRoot, component.ReleaseID+"-"+component.Commit)
	if info, err := os.Lstat(cachePath); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: cached source path is not a directory", ErrTemplateSourceDrift)
		}
		if err := materializer.verifyCommit(ctx, cachePath, component.Commit); err != nil {
			return "", err
		}
		return cachePath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: inspect source cache: %v", ErrTemplateSourceUnavailable, err)
	}

	temporary, err := os.MkdirTemp(materializer.cacheRoot, ".fetch-")
	if err != nil {
		return "", fmt.Errorf("%w: create source fetch directory: %v", ErrTemplateSourceUnavailable, err)
	}
	defer os.RemoveAll(temporary)
	if _, err := materializer.git.Run(ctx, 64<<10, "init", "--bare", "--quiet", temporary); err != nil {
		return "", fmt.Errorf("%w: initialize source cache: %v", ErrTemplateSourceUnavailable, err)
	}
	if _, err := materializer.git.Run(ctx, 64<<10, "--git-dir", temporary, "remote", "add", "origin", component.Repository); err != nil {
		return "", fmt.Errorf("%w: bind admitted repository: %v", ErrTemplateSourceUnavailable, err)
	}
	if _, err := materializer.git.Run(
		ctx, 1<<20, "--git-dir", temporary, "fetch", "--quiet", "--depth=1", "--no-tags", "origin", component.Commit,
	); err != nil {
		return "", fmt.Errorf("%w: fetch exact admitted commit: %v", ErrTemplateSourceUnavailable, err)
	}
	if err := materializer.verifyCommit(ctx, temporary, component.Commit); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, cachePath); err != nil {
		if info, statErr := os.Lstat(cachePath); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: publish source cache: %v", ErrTemplateSourceUnavailable, err)
		}
	}
	return cachePath, nil
}

func (materializer *GitTemplateSourceMaterializer) verifyCommit(
	ctx context.Context,
	repositoryPath, expected string,
) error {
	resolved, err := materializer.git.Run(
		ctx, 4<<10, "--git-dir", repositoryPath, "rev-parse", "--verify", expected+"^{commit}",
	)
	if err != nil {
		return fmt.Errorf("%w: verify exact admitted commit: %v", ErrTemplateSourceUnavailable, err)
	}
	if strings.TrimSpace(string(resolved)) != expected {
		return fmt.Errorf("%w: fetched commit identity differs", ErrTemplateSourceDrift)
	}
	return nil
}

func (materializer *GitTemplateSourceMaterializer) readExactTree(
	ctx context.Context,
	repositoryPath string,
	component TemplateSourceComponent,
) ([]TemplateSourceFile, error) {
	raw, err := materializer.git.Run(
		ctx, 32<<20, "--git-dir", repositoryPath, "ls-tree", "-r", "--full-tree", "-z", component.Commit,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: read admitted Git tree: %v", ErrTemplateSourceUnavailable, err)
	}
	digest := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(digest[:]) != component.TreeHash {
		return nil, fmt.Errorf("%w: exact ls-tree digest does not match the TemplateRelease", ErrTemplateSourceDrift)
	}
	entries, err := parseGitTreeEntries(raw)
	if err != nil {
		return nil, err
	}
	files := make([]TemplateSourceFile, 0, len(entries))
	for _, entry := range entries {
		value, err := materializer.git.Run(
			ctx, MaxFileBytes+1, "--git-dir", repositoryPath, "cat-file", "blob", entry.objectID,
		)
		if err != nil {
			return nil, fmt.Errorf("%w: read admitted blob for %s: %v", ErrTemplateSourceUnavailable, entry.path, err)
		}
		if int64(len(value)) > MaxFileBytes {
			return nil, fmt.Errorf("%w: %s exceeds the file limit", ErrTemplateSourceLimit, entry.path)
		}
		files = append(files, TemplateSourceFile{
			Path: entry.path, Mode: entry.mode, Content: value,
		})
	}
	return validateTemplateSourceFiles(files)
}

type gitTreeEntry struct {
	mode     string
	objectID string
	path     string
}

func parseGitTreeEntries(raw []byte) ([]gitTreeEntry, error) {
	if len(raw) == 0 || raw[len(raw)-1] != 0 {
		return nil, fmt.Errorf("%w: admitted Git tree is empty or not NUL framed", ErrTemplateSourceInvalid)
	}
	records := bytes.Split(raw[:len(raw)-1], []byte{0})
	if len(records) > MaxTreeFiles {
		return nil, fmt.Errorf("%w: admitted Git tree contains too many files", ErrTemplateSourceLimit)
	}
	result := make([]gitTreeEntry, 0, len(records))
	for _, record := range records {
		separator := bytes.IndexByte(record, '\t')
		if separator <= 0 || separator == len(record)-1 || !utf8.Valid(record[separator+1:]) {
			return nil, fmt.Errorf("%w: malformed ls-tree record", ErrTemplateSourceInvalid)
		}
		metadata := strings.Fields(string(record[:separator]))
		if len(metadata) != 3 || metadata[1] != "blob" ||
			(metadata[0] != "100644" && metadata[0] != "100755") ||
			!gitObjectPattern.MatchString(metadata[2]) {
			return nil, fmt.Errorf("%w: only regular executable/non-executable blobs are supported", ErrTemplateSourceInvalid)
		}
		filePath := string(record[separator+1:])
		if normalized, err := NormalizePath(filePath); err != nil || normalized != filePath {
			return nil, fmt.Errorf("%w: admitted Git path %q is unsafe", ErrTemplateSourceInvalid, filePath)
		}
		result = append(result, gitTreeEntry{mode: metadata[0], objectID: metadata[2], path: filePath})
	}
	return result, nil
}

func normalizeTemplateSourceRequest(
	request TemplateSourceRequest,
	allowedHosts map[string]struct{},
) (TemplateSourceRequest, error) {
	if err := validateExact(request.FullStackTemplate); err != nil {
		return TemplateSourceRequest{}, err
	}
	if err := validateExact(request.BuildContract); err != nil {
		return TemplateSourceRequest{}, err
	}
	if len(request.Components) < 2 || len(request.Components) > 8 {
		return TemplateSourceRequest{}, fmt.Errorf("%w: exact web/api components are required", ErrTemplateSourceInvalid)
	}
	request.Components = append([]TemplateSourceComponent(nil), request.Components...)
	roles := make(map[string]bool, len(request.Components))
	for index := range request.Components {
		component := &request.Components[index]
		component.Role = strings.TrimSpace(component.Role)
		component.MountPath = strings.TrimSpace(component.MountPath)
		component.Repository = strings.TrimSpace(component.Repository)
		component.Branch = strings.TrimSpace(component.Branch)
		component.Commit = strings.TrimSpace(component.Commit)
		if (component.Role != "web" && component.Role != "api" && component.Role != "worker") || roles[component.Role] {
			return TemplateSourceRequest{}, fmt.Errorf("%w: component roles are invalid or duplicated", ErrTemplateSourceInvalid)
		}
		roles[component.Role] = true
		if mount, err := NormalizePath(component.MountPath); err != nil || mount != component.MountPath {
			return TemplateSourceRequest{}, fmt.Errorf("%w: component mount path is unsafe", ErrTemplateSourceInvalid)
		}
		if !validUUID(component.ReleaseID) ||
			!isCanonicalSHA256(component.ReleaseContentHash) ||
			!isCanonicalSHA256(component.ReleaseSubjectHash) ||
			!gitObjectPattern.MatchString(component.Commit) ||
			!isCanonicalSHA256(component.TreeHash) || component.Branch == "" ||
			!isCanonicalSHA256(component.SBOMDigest) ||
			!isCanonicalSHA256(component.SignatureBundleDigest) ||
			!validUUID(component.AuthorityReceiptID) ||
			!isCanonicalSHA256(component.AuthorityReceiptContentHash) ||
			!isCanonicalSHA256(component.AuthorityPolicyHash) {
			return TemplateSourceRequest{}, fmt.Errorf("%w: component release identity is incomplete", ErrTemplateSourceInvalid)
		}
		parsed, err := url.Parse(component.Repository)
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" ||
			parsed.Fragment != "" || parsed.Port() != "" || !strings.HasSuffix(parsed.Path, ".git") {
			return TemplateSourceRequest{}, fmt.Errorf("%w: component repository must be a credentialless canonical HTTPS Git URL", ErrTemplateSourceInvalid)
		}
		if _, allowed := allowedHosts[strings.ToLower(parsed.Hostname())]; !allowed {
			return TemplateSourceRequest{}, fmt.Errorf("%w: component repository host is not allowed", ErrTemplateSourceInvalid)
		}
	}
	if !roles["web"] || !roles["api"] {
		return TemplateSourceRequest{}, fmt.Errorf("%w: exact web and api releases are required", ErrTemplateSourceInvalid)
	}
	sort.Slice(request.Components, func(i, j int) bool {
		return request.Components[i].Role < request.Components[j].Role
	})
	return request, nil
}

func validateTemplateSourceFiles(files []TemplateSourceFile) ([]TemplateSourceFile, error) {
	if len(files) == 0 || len(files) > MaxTreeFiles {
		return nil, fmt.Errorf("%w: source tree file count", ErrTemplateSourceLimit)
	}
	result := make([]TemplateSourceFile, len(files))
	seen := make(map[string]bool, len(files))
	total := int64(0)
	for index, file := range files {
		filePath, err := NormalizePath(file.Path)
		if err != nil || filePath != file.Path || (file.Mode != "100644" && file.Mode != "100755") {
			return nil, fmt.Errorf("%w: invalid source file at index %d", ErrTemplateSourceInvalid, index)
		}
		folded := strings.ToLower(filePath)
		if seen[folded] {
			return nil, fmt.Errorf("%w: case-insensitive path collision for %s", ErrTemplateSourceInvalid, filePath)
		}
		seen[folded] = true
		if int64(len(file.Content)) > MaxFileBytes {
			return nil, fmt.Errorf("%w: %s exceeds the file limit", ErrTemplateSourceLimit, filePath)
		}
		total += int64(len(file.Content))
		if total > MaxTreeBytes {
			return nil, fmt.Errorf("%w: source tree exceeds the byte limit", ErrTemplateSourceLimit)
		}
		result[index] = TemplateSourceFile{
			Path: filePath, Mode: file.Mode, Content: append([]byte(nil), file.Content...),
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func templateSourceLock(request TemplateSourceRequest) ([]byte, error) {
	payload := struct {
		SchemaVersion     string                    `json:"schemaVersion"`
		FullStackTemplate ExactReference            `json:"fullStackTemplate"`
		BuildContract     ExactReference            `json:"buildContract"`
		Components        []TemplateSourceComponent `json:"components"`
	}{
		SchemaVersion:     TemplateSourceLockSchemaVersion,
		FullStackTemplate: request.FullStackTemplate,
		BuildContract:     request.BuildContract,
		Components:        request.Components,
	}
	value, err := domain.CanonicalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: encode templates.lock.json: %v", ErrTemplateSourceInvalid, err)
	}
	return value, nil
}

func (service *CandidateBootstrapService) materializeBootstrapFiles(
	ctx context.Context,
	source bootstrapSource,
) ([]TemplateSourceFile, []TemplateSourceComponent, error) {
	components, err := service.loadTemplateSourceComponents(ctx, service.database, source)
	if err != nil {
		return nil, nil, err
	}
	fingerprint, err := templateSourceComponentHash(components)
	if err != nil || fingerprint != source.TemplateComponentHash {
		return nil, nil, errors.Join(ErrBootstrapSourceDrift, err)
	}
	if source.WorkspaceRevisionID != "" {
		stored, err := service.contents.Get(ctx, source.WorkspaceContentRef, source.WorkspaceContentHash)
		if err != nil {
			return nil, nil, fmt.Errorf("read exact bootstrap WorkspaceRevision: %w", err)
		}
		if err := validateBootstrapStoredContent(stored, source); err != nil {
			return nil, nil, err
		}
		files, err := decodeBootstrapWorkspace(stored.Payload)
		return files, components, err
	}
	if service.templates == nil {
		return nil, nil, fmt.Errorf("%w: exact TemplateRelease source materializer is not configured", ErrBootstrapNotReady)
	}
	files, err := service.templates.Materialize(ctx, TemplateSourceRequest{
		FullStackTemplate: ExactReference{ID: source.FullStackTemplateID, ContentHash: source.FullStackTemplateHash},
		BuildContract:     ExactReference{ID: source.BuildContractID, ContentHash: source.BuildContractHash},
		Components:        components,
	})
	return files, components, err
}

func (service *CandidateBootstrapService) loadTemplateSourceComponents(
	ctx context.Context,
	database *gorm.DB,
	source bootstrapSource,
) ([]TemplateSourceComponent, error) {
	var components []TemplateSourceComponent
	result := database.WithContext(ctx).Raw(`
SELECT component.role,
       component.mount_path,
       release.id::text AS release_id,
       release.content_hash AS release_content_hash,
       release.subject_hash AS release_subject_hash,
       release.source_repository,
       release.source_branch,
       release.source_commit,
       release.tree_hash,
       receipt.sbom_digest,
       receipt.signature_bundle_digest,
       receipt.id::text AS authority_receipt_id,
       receipt.content_hash AS authority_receipt_content_hash,
       receipt.policy_hash AS authority_policy_hash
FROM application_build_contract_template_releases AS selected
JOIN full_stack_template_components AS component
  ON component.full_stack_template_id = ?
 AND component.full_stack_content_hash = ?
 AND component.role = selected.role
 AND component.template_release_id = selected.template_release_id
 AND component.template_release_content_hash = selected.template_release_content_hash
JOIN template_releases AS release
  ON release.id = component.template_release_id
 AND release.content_hash = component.template_release_content_hash
JOIN template_release_policies AS policy
  ON policy.template_release_id = release.id
 AND policy.release_content_hash = release.content_hash
 AND policy.state = 'approved'
 AND policy.authority_receipt_id = release.authority_receipt_id
 AND policy.authority_receipt_content_hash = release.authority_receipt_content_hash
 AND policy.authority_policy_hash = release.authority_policy_hash
JOIN template_artifact_authority_receipts AS receipt
  ON receipt.id = release.authority_receipt_id
 AND receipt.content_hash = release.authority_receipt_content_hash
 AND receipt.policy_hash = release.authority_policy_hash
 AND receipt.subject_hash = release.subject_hash
 AND receipt.source_tree_hash = release.tree_hash
 AND receipt.sbom_digest = release.sbom_digest
 AND receipt.signature_bundle_digest = release.signature ->> 'bundleDigest'
WHERE selected.contract_id = ?
ORDER BY component.role
`, source.FullStackTemplateID, source.FullStackTemplateHash, source.BuildContractID).Scan(&components)
	if result.Error != nil {
		return nil, fmt.Errorf("load exact TemplateRelease sources: %w", result.Error)
	}
	if len(components) < 2 || len(components) > 8 {
		return nil, fmt.Errorf("%w: exact selectable web/api TemplateReleases are unavailable", ErrBootstrapNotReady)
	}
	roles := make(map[string]bool, len(components))
	for _, component := range components {
		if roles[component.Role] || (component.Role != "web" && component.Role != "api" && component.Role != "worker") ||
			!validUUID(component.ReleaseID) || !isCanonicalSHA256(component.ReleaseContentHash) ||
			!isCanonicalSHA256(component.ReleaseSubjectHash) || !isCanonicalSHA256(component.TreeHash) ||
			!isCanonicalSHA256(component.SBOMDigest) || !isCanonicalSHA256(component.SignatureBundleDigest) ||
			!validUUID(component.AuthorityReceiptID) || !isCanonicalSHA256(component.AuthorityReceiptContentHash) ||
			!isCanonicalSHA256(component.AuthorityPolicyHash) {
			return nil, fmt.Errorf("%w: exact TemplateRelease source projection is invalid", ErrBootstrapSourceDrift)
		}
		roles[component.Role] = true
	}
	if !roles["web"] || !roles["api"] {
		return nil, fmt.Errorf("%w: exact web/api TemplateRelease sources are required", ErrBootstrapNotReady)
	}
	return components, nil
}

func templateSourceComponentHash(components []TemplateSourceComponent) (string, error) {
	hash, err := domain.CanonicalHash(components)
	if err != nil {
		return "", fmt.Errorf("hash exact TemplateRelease sources: %w", err)
	}
	return "sha256:" + hash, nil
}

type execTemplateGitRunner struct{ binary string }

func (runner execTemplateGitRunner) Run(
	ctx context.Context,
	limit int64,
	arguments ...string,
) ([]byte, error) {
	if limit <= 0 {
		return nil, ErrTemplateSourceLimit
	}
	base := []string{
		"-c", "credential.helper=",
		"-c", "core.hooksPath=/dev/null",
		"-c", "protocol.file.allow=never",
		"-c", "protocol.ext.allow=never",
		"-c", "http.followRedirects=false",
	}
	command := exec.CommandContext(ctx, runner.binary, append(base, arguments...)...)
	command.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_LFS_SKIP_SMUDGE=1",
	)
	stdout := &boundedCommandBuffer{limit: limit}
	stderr := &boundedCommandBuffer{limit: 64 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(string(stderr.value))
		if detail == "" {
			detail = err.Error()
		}
		return nil, errors.New(detail)
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, ErrTemplateSourceLimit
	}
	return append([]byte(nil), stdout.value...), nil
}

type boundedCommandBuffer struct {
	value    []byte
	limit    int64
	exceeded bool
}

func (buffer *boundedCommandBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - int64(len(buffer.value))
	if remaining > 0 {
		count := int64(len(value))
		if count > remaining {
			count = remaining
		}
		buffer.value = append(buffer.value, value[:count]...)
	}
	if int64(len(value)) > remaining {
		buffer.exceeded = true
	}
	return len(value), nil
}
