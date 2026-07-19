package lsp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/sandbox"
	"github.com/worksflow/builder/backend/internal/templates"
)

var (
	ErrRuntimeBindingUnavailable = errors.New("LSP runtime binding authority is unavailable")
	ErrRuntimeBindingStale       = errors.New("LSP runtime binding is stale")
	ErrRuntimeDocumentInvalid    = errors.New("LSP runtime document is invalid")
)

type RuntimeBindingWorkspace interface {
	Materialize(context.Context, sandbox.SessionView, repository.CandidateWorkspace) (sandbox.WorkspaceMount, error)
}

type RuntimeBindingFiles interface {
	Resolve(context.Context, string, string, int64) (repository.FileBlobPointer, []byte, error)
}

type RuntimeServiceRootSource interface {
	ResolveServiceRoot(
		context.Context,
		repository.ExactReference,
		ExactTemplateRelease,
		ProfileIdentity,
	) (string, error)
}

type RuntimeDocument struct {
	Fence DocumentFence
	Path  string
	Mode  string
	Text  []byte
}

// RuntimeFileFence is the exact immutable Candidate tree catalog admitted to
// one server process. It lets result sanitization prove that navigation never
// invents a URI outside the bound tree without exposing file bytes.
type RuntimeFileFence struct {
	Path        string
	Mode        string
	ContentHash string
	ByteSize    int64
}

type RuntimeBindingProjection struct {
	Head          SandboxHeadFence
	Profile       ProfileIdentity
	WorkspaceRoot string
	ServiceRoot   string
	ServicePath   string
	Files         []RuntimeFileFence
	Documents     []RuntimeDocument
}

type RuntimeBindingSource struct {
	authority  TicketAuthority
	sessions   AuthoritySessions
	candidates AuthorityCandidates
	workspaces RuntimeBindingWorkspace
	files      RuntimeBindingFiles
	templates  RuntimeServiceRootSource
	now        func() time.Time
}

func NewRuntimeBindingSource(
	authority TicketAuthority,
	sessions AuthoritySessions,
	candidates AuthorityCandidates,
	workspaces RuntimeBindingWorkspace,
	files RuntimeBindingFiles,
	templates RuntimeServiceRootSource,
	now func() time.Time,
) (*RuntimeBindingSource, error) {
	if authority == nil || sessions == nil || candidates == nil || workspaces == nil ||
		files == nil || templates == nil || now == nil {
		return nil, ErrRuntimeBindingUnavailable
	}
	return &RuntimeBindingSource{
		authority: authority, sessions: sessions, candidates: candidates,
		workspaces: workspaces, files: files, templates: templates, now: now,
	}, nil
}

// Resolve proves the bind against opening and closing Repository/Sandbox
// authority. It independently resolves every saved document from immutable
// blob storage; a browser-provided DocumentFence is never enough to open LSP.
func (source *RuntimeBindingSource) Resolve(
	ctx context.Context,
	grant TicketGrant,
	bind ClientBind,
) (RuntimeBindingProjection, error) {
	if source == nil || ctx == nil {
		return RuntimeBindingProjection{}, ErrRuntimeBindingUnavailable
	}
	now := source.now().UTC()
	if now.IsZero() || validateTicketGrant(grant, now) != nil || validateRuntimeClientBind(grant, bind) != nil {
		return RuntimeBindingProjection{}, ErrRuntimeBindingStale
	}
	if err := source.revalidate(ctx, grant, bind); err != nil {
		return RuntimeBindingProjection{}, err
	}
	session, err := source.sessions.Get(ctx, grant.ProjectID, grant.SessionID)
	if err != nil || session.Validate() != nil {
		return RuntimeBindingProjection{}, ErrRuntimeBindingUnavailable
	}
	sessionView := session.Snapshot()
	if sessionView.State != sandbox.StateReady || !sessionView.TTL.ExpiresAt.After(now) ||
		sessionView.ProjectID != grant.ProjectID || sessionView.ID != grant.SessionID ||
		sessionView.Candidate.ID != grant.Head.CandidateID {
		return RuntimeBindingProjection{}, ErrRuntimeBindingStale
	}
	record, err := source.candidates.Get(ctx, grant.ProjectID, grant.Head.CandidateID)
	if err != nil || record.Candidate.Validate() != nil {
		return RuntimeBindingProjection{}, ErrRuntimeBindingUnavailable
	}
	head, _, err := exactAuthorityHead(sessionView, record, now)
	if err != nil || !head.Equal(grant.Head) {
		return RuntimeBindingProjection{}, ErrRuntimeBindingStale
	}
	documents, err := source.resolveDocuments(ctx, record.Candidate, bind)
	if err != nil {
		return RuntimeBindingProjection{}, err
	}
	mount, err := source.workspaces.Materialize(ctx, sessionView, record.Candidate)
	if err != nil {
		return RuntimeBindingProjection{}, fmt.Errorf("%w: materialize exact workspace", ErrRuntimeBindingUnavailable)
	}
	serviceRoot, err := source.templates.ResolveServiceRoot(
		ctx, sessionView.FullStackTemplate, grant.TemplateRelease, bind.Profile,
	)
	if err != nil {
		return RuntimeBindingProjection{}, err
	}
	if !runtimeDocumentsWithinServiceRoot(documents, serviceRoot) {
		return RuntimeBindingProjection{}, ErrRuntimeDocumentInvalid
	}
	workspaceRoot, servicePath, err := validateRuntimeWorkspacePaths(mount.Workspace, serviceRoot)
	if err != nil {
		return RuntimeBindingProjection{}, err
	}
	if err := source.revalidate(ctx, grant, bind); err != nil {
		return RuntimeBindingProjection{}, err
	}
	return RuntimeBindingProjection{
		Head: grant.Head, Profile: bind.Profile, WorkspaceRoot: workspaceRoot,
		ServiceRoot: serviceRoot, ServicePath: servicePath,
		Files: runtimeFileFences(record.Candidate.CurrentTree), Documents: cloneRuntimeDocuments(documents),
	}, nil
}

func runtimeFileFences(tree repository.TreeManifest) []RuntimeFileFence {
	result := make([]RuntimeFileFence, len(tree.Files))
	for index, file := range tree.Files {
		result[index] = RuntimeFileFence{
			Path: file.Path, Mode: file.Mode, ContentHash: file.ContentHash, ByteSize: file.ByteSize,
		}
	}
	return result
}

func runtimeDocumentsWithinServiceRoot(documents []RuntimeDocument, serviceRoot string) bool {
	if serviceRoot == "." {
		return true
	}
	normalized, err := repository.NormalizePath(serviceRoot)
	if err != nil || normalized != serviceRoot {
		return false
	}
	prefix := serviceRoot + "/"
	for _, document := range documents {
		if !strings.HasPrefix(document.Path, prefix) {
			return false
		}
	}
	return true
}

func (source *RuntimeBindingSource) revalidate(ctx context.Context, grant TicketGrant, bind ClientBind) error {
	authority, err := source.authority.GetLSPAuthority(
		ctx, grant.ProjectID, grant.SessionID, grant.TemplateRelease,
	)
	if err != nil {
		return err
	}
	profiles, err := validateAuthority(
		authority, grant.Head, grant.TemplateRelease, grant.ActorID, grant.Mode, []string{bind.Profile.ID},
	)
	if err != nil || len(profiles) != 1 || !equalProfiles(profiles, []ProfileIdentity{bind.Profile}) {
		return ErrRuntimeBindingStale
	}
	return nil
}

// RevalidateGatewayFence is the steady-state authority check used around each
// admitted browser transition and each server result. Unlike the one-time
// ticket expiry, the Sandbox/Repository/Profile authority remains live for the
// entire connection. A head newer than the ticket is accepted only when it is
// a structurally monotonic successor and exactly equals current server state.
func (source *RuntimeBindingSource) RevalidateGatewayFence(
	ctx context.Context,
	grant TicketGrant,
	head SandboxHeadFence,
	profile ProfileIdentity,
	documents []DocumentFence,
) error {
	if source == nil || ctx == nil || validateTicketGrant(grant, time.Time{}) != nil ||
		head.Validate() != nil || profile.Validate() != nil ||
		profile.TemplateRelease != grant.TemplateRelease || !profileInGrant(profile, grant.Profiles) ||
		(!head.Equal(grant.Head) && head.MonotonicSuccessorOf(grant.Head) != nil) ||
		len(documents) > profile.EffectiveLimits.MaxOpenDocuments ||
		validateCurrentDocuments(head, documents) != nil {
		return ErrRuntimeBindingStale
	}
	authority, err := source.authority.GetLSPAuthority(
		ctx, grant.ProjectID, grant.SessionID, grant.TemplateRelease,
	)
	if err != nil {
		return err
	}
	profiles, err := validateAuthority(
		authority, head, grant.TemplateRelease, grant.ActorID, grant.Mode, []string{profile.ID},
	)
	if err != nil || len(profiles) != 1 || !equalProfiles(profiles, []ProfileIdentity{profile}) {
		return ErrRuntimeBindingStale
	}
	now := source.now().UTC()
	if now.IsZero() {
		return ErrRuntimeBindingUnavailable
	}
	session, err := source.sessions.Get(ctx, grant.ProjectID, grant.SessionID)
	if err != nil || session.Validate() != nil {
		return ErrRuntimeBindingUnavailable
	}
	view := session.Snapshot()
	if view.State != sandbox.StateReady || !view.TTL.ExpiresAt.After(now) {
		return ErrRuntimeBindingStale
	}
	record, err := source.candidates.Get(ctx, grant.ProjectID, head.CandidateID)
	if err != nil || record.Candidate.Validate() != nil {
		return ErrRuntimeBindingUnavailable
	}
	exactHead, _, err := exactAuthorityHead(view, record, now)
	if err != nil || !exactHead.Equal(head) {
		return ErrRuntimeBindingStale
	}
	return validateRuntimeDocumentFences(record.Candidate, head, profile, documents)
}

func validateRuntimeDocumentFences(
	candidate repository.CandidateWorkspace,
	head SandboxHeadFence,
	profile ProfileIdentity,
	documents []DocumentFence,
) error {
	byPath := make(map[string]repository.TreeFile, len(candidate.CurrentTree.Files))
	for _, file := range candidate.CurrentTree.Files {
		byPath[file.Path] = file
	}
	var total int64
	for _, document := range documents {
		identity, err := ParseCandidateModelURI(document.ModelURI)
		if err != nil || identity.ProjectID != candidate.ProjectID || identity.CandidateID != candidate.ID ||
			document.ValidateAgainstHead(head) != nil || !profileSupportsRepositoryPath(profile, identity.Path) {
			return ErrRuntimeBindingStale
		}
		file, exists := byPath[identity.Path]
		if !exists || file.ContentHash != document.SavedContentHash || file.ByteSize < 0 ||
			file.ByteSize > profile.EffectiveLimits.MaxDocumentBytes {
			return ErrRuntimeBindingStale
		}
		total += file.ByteSize
		if total > profile.EffectiveLimits.MaxTotalSyncBytes {
			return ErrRuntimeBindingStale
		}
	}
	return nil
}

func validateRuntimeClientBind(grant TicketGrant, bind ClientBind) error {
	if bind.SchemaVersion != BindingSchemaVersion || bind.Kind != "client.bind" ||
		!canonicalUUID(bind.ConnectionID) || bind.BindingID != nil || bind.Sequence != 1 ||
		!bind.Head.Equal(grant.Head) || bind.Profile.TemplateRelease != grant.TemplateRelease ||
		!profileInGrant(bind.Profile, grant.Profiles) || len(bind.Documents) == 0 ||
		len(bind.Documents) > bind.Profile.EffectiveLimits.MaxOpenDocuments {
		return ErrRuntimeBindingStale
	}
	return validateCurrentDocuments(bind.Head, bind.Documents)
}

func (source *RuntimeBindingSource) resolveDocuments(
	ctx context.Context,
	candidate repository.CandidateWorkspace,
	bind ClientBind,
) ([]RuntimeDocument, error) {
	byPath := make(map[string]repository.TreeFile, len(candidate.CurrentTree.Files))
	for _, file := range candidate.CurrentTree.Files {
		byPath[file.Path] = file
	}
	result := make([]RuntimeDocument, len(bind.Documents))
	var total int64
	for index, fence := range bind.Documents {
		identity, err := ParseCandidateModelURI(fence.ModelURI)
		if err != nil || identity.ProjectID != candidate.ProjectID || identity.CandidateID != candidate.ID ||
			!profileSupportsRepositoryPath(bind.Profile, identity.Path) {
			return nil, ErrRuntimeDocumentInvalid
		}
		file, exists := byPath[identity.Path]
		if !exists || file.ContentHash != fence.SavedContentHash || file.ByteSize < 0 ||
			file.ByteSize > bind.Profile.EffectiveLimits.MaxDocumentBytes {
			return nil, ErrRuntimeDocumentInvalid
		}
		pointer, value, err := source.files.Resolve(ctx, candidate.ProjectID, file.ContentHash, file.ByteSize)
		if err != nil || pointer.ContentHash != file.ContentHash || pointer.ByteSize != file.ByteSize ||
			int64(len(value)) != file.ByteSize || !utf8.Valid(value) || bytes.IndexByte(value, 0) >= 0 {
			return nil, ErrRuntimeDocumentInvalid
		}
		digest := sha256.Sum256(value)
		if fmt.Sprintf("sha256:%x", digest[:]) != file.ContentHash {
			return nil, ErrRuntimeDocumentInvalid
		}
		total += file.ByteSize
		if total > bind.Profile.EffectiveLimits.MaxTotalSyncBytes {
			return nil, ErrRuntimeDocumentInvalid
		}
		result[index] = RuntimeDocument{
			Fence: fence, Path: identity.Path, Mode: file.Mode, Text: append([]byte(nil), value...),
		}
	}
	return result, nil
}

func validateRuntimeWorkspacePaths(workspaceRoot, serviceRoot string) (string, string, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" || !filepath.IsAbs(workspaceRoot) || filepath.Clean(workspaceRoot) != workspaceRoot {
		return "", "", ErrRuntimeBindingUnavailable
	}
	if serviceRoot != "." {
		normalized, err := repository.NormalizePath(serviceRoot)
		if err != nil || normalized != serviceRoot {
			return "", "", ErrRuntimeBindingUnavailable
		}
	}
	servicePath := workspaceRoot
	if serviceRoot != "." {
		servicePath = filepath.Join(workspaceRoot, filepath.FromSlash(serviceRoot))
	}
	for _, directory := range []string{workspaceRoot, servicePath} {
		info, err := os.Lstat(directory)
		resolved, resolveErr := filepath.EvalSymlinks(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || resolveErr != nil || resolved != directory {
			return "", "", ErrRuntimeBindingUnavailable
		}
	}
	relative, err := filepath.Rel(workspaceRoot, servicePath)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", "", ErrRuntimeBindingUnavailable
	}
	return workspaceRoot, servicePath, nil
}

func profileSupportsRepositoryPath(profile ProfileIdentity, repositoryPath string) bool {
	normalized, err := repository.NormalizePath(repositoryPath)
	if err != nil || normalized != repositoryPath {
		return false
	}
	for _, pattern := range profile.FileGlobs {
		if matchLanguageServerGlob(strings.Split(pattern, "/"), strings.Split(repositoryPath, "/")) {
			return true
		}
	}
	return false
}

func matchLanguageServerGlob(pattern, value []string) bool {
	// A profile may contain several ** segments. Memoizing the two sequence
	// offsets keeps matching O(pattern*path) instead of permitting exponential
	// backtracking during a connection bind.
	type state struct{ pattern, value int }
	visited := make(map[state]bool, (len(pattern)+1)*(len(value)+1))
	matches := make(map[state]bool, (len(pattern)+1)*(len(value)+1))
	var visit func(int, int) bool
	visit = func(patternIndex, valueIndex int) bool {
		key := state{pattern: patternIndex, value: valueIndex}
		if visited[key] {
			return matches[key]
		}
		visited[key] = true
		matched := false
		switch {
		case patternIndex == len(pattern):
			matched = valueIndex == len(value)
		case pattern[patternIndex] == "**":
			matched = visit(patternIndex+1, valueIndex) ||
				(valueIndex < len(value) && visit(patternIndex, valueIndex+1))
		case valueIndex < len(value):
			segmentMatched, matchErr := path.Match(pattern[patternIndex], value[valueIndex])
			matched = matchErr == nil && segmentMatched && visit(patternIndex+1, valueIndex+1)
		}
		matches[key] = matched
		return matched
	}
	return visit(0, 0)
}

func cloneRuntimeDocuments(values []RuntimeDocument) []RuntimeDocument {
	result := append([]RuntimeDocument(nil), values...)
	for index := range result {
		result[index].Text = append([]byte(nil), values[index].Text...)
	}
	return result
}

type RegistryRuntimeServiceRootSource struct {
	registry RegistryProfileReader
}

func NewRegistryRuntimeServiceRootSource(registry RegistryProfileReader) (*RegistryRuntimeServiceRootSource, error) {
	if registry == nil {
		return nil, ErrRuntimeBindingUnavailable
	}
	return &RegistryRuntimeServiceRootSource{registry: registry}, nil
}

func (source *RegistryRuntimeServiceRootSource) ResolveServiceRoot(
	ctx context.Context,
	fullStack repository.ExactReference,
	release ExactTemplateRelease,
	profile ProfileIdentity,
) (string, error) {
	if source == nil || ctx == nil || !canonicalUUID(fullStack.ID) ||
		!digestPattern.MatchString(fullStack.ContentHash) || release.Validate() != nil ||
		profile.Validate() != nil || profile.TemplateRelease != release {
		return "", ErrProfileNotDeclared
	}
	resolved, err := source.registry.ResolveForNewBuild(ctx, templates.ExactFullStackTemplateRef{
		ID: fullStack.ID, ContentHash: fullStack.ContentHash,
	})
	if err != nil {
		return "", fmt.Errorf("%w: resolve exact FullStackTemplate", ErrRuntimeBindingUnavailable)
	}
	view := resolved.Template.Snapshot()
	if view.ID != fullStack.ID || view.ContentHash != fullStack.ContentHash {
		return "", ErrProfileNotDeclared
	}
	for _, component := range resolved.Components {
		if component.Release.ID() != release.ID || component.Release.ContentHash() != release.ContentHash {
			continue
		}
		releaseView := component.Release.Snapshot()
		profileFound := false
		for _, declared := range releaseView.Manifest.LanguageServers {
			if declared.ID == profile.ID && reflect.DeepEqual(declared, profile.LanguageServerProfile) {
				profileFound = true
				break
			}
		}
		if !profileFound {
			return "", ErrProfileNotDeclared
		}
		for _, service := range releaseView.Manifest.Services {
			if service.ID != profile.ServiceID {
				continue
			}
			root, rootErr := effectiveTemplateServiceRoot(component.MountPath, service.RootPath)
			if rootErr != nil {
				return "", ErrProfileNotDeclared
			}
			return root, nil
		}
		return "", ErrProfileNotDeclared
	}
	return "", ErrProfileNotDeclared
}

func effectiveTemplateServiceRoot(mountPath, serviceRoot string) (string, error) {
	values := []string{mountPath, serviceRoot}
	for _, value := range values {
		if value == "." {
			continue
		}
		if normalized, err := repository.NormalizePath(value); err != nil || normalized != value {
			return "", ErrProfileNotDeclared
		}
	}
	root := path.Join(mountPath, serviceRoot)
	if root == "." {
		return root, nil
	}
	normalized, err := repository.NormalizePath(root)
	if err != nil || normalized != root {
		return "", ErrProfileNotDeclared
	}
	return root, nil
}

var _ RuntimeServiceRootSource = (*RegistryRuntimeServiceRootSource)(nil)
var _ GatewayAuthorityFence = (*RuntimeBindingSource)(nil)
