package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type Authorizer interface {
	Authorize(context.Context, string, string, core.Action) (core.Role, error)
}

type Client interface {
	AuthenticatedUser(context.Context, string) (User, error)
	Repositories(context.Context, string) ([]Repository, error)
	CreateRepository(context.Context, string, RepositoryCreateOptions) (Repository, error)
	Branches(context.Context, string, string, string) ([]Branch, error)
	Reference(context.Context, string, string, string, string) (gitReference, error)
	Commit(context.Context, string, string, string, string) (gitCommit, error)
	Tree(context.Context, string, string, string, string) (gitTree, error)
	Blob(context.Context, string, string, string, string) (gitBlob, error)
	CreateBlob(context.Context, string, string, string, string) (string, error)
	CreateTree(context.Context, string, string, string, string, []map[string]any) (string, error)
	CreateCommit(context.Context, string, string, string, string, string, string) (gitCommit, error)
	UpdateReference(context.Context, string, string, string, string, string) error
	CreateReference(context.Context, string, string, string, string, string) error
	CreatePullRequest(context.Context, string, PullRequestInput) (map[string]any, error)
}

type Service struct {
	api      Client
	store    CredentialStore
	platform PlatformCredentialProvider
	access   Authorizer
	database *gorm.DB
	logger   *slog.Logger
	ttl      time.Duration
	now      func() time.Time
}

func NewService(api Client, store CredentialStore, access Authorizer, database *gorm.DB, logger *slog.Logger, ttl time.Duration, platform ...PlatformCredentialProvider) (*Service, error) {
	if api == nil || store == nil || access == nil || database == nil || logger == nil || ttl <= 0 || ttl > 90*24*time.Hour ||
		len(platform) > 1 || (len(platform) == 1 && platform[0] == nil) {
		return nil, errors.New("GitHub API, credential store, access, database, logger and bounded TTL are required")
	}
	var platformCredential PlatformCredentialProvider
	if len(platform) == 1 {
		platformCredential = platform[0]
	}
	return &Service{api: api, store: store, platform: platformCredential, access: access, database: database, logger: logger, ttl: ttl, now: time.Now}, nil
}

func (s *Service) Connect(ctx context.Context, projectID, actorID, token string) (ConnectionStatus, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return ConnectionStatus{}, err
	}
	validated, err := validateToken(token)
	if err != nil {
		return ConnectionStatus{}, err
	}
	user, err := s.api.AuthenticatedUser(ctx, validated)
	if err != nil {
		return ConnectionStatus{}, err
	}
	expires := s.now().UTC().Add(s.ttl)
	credential := Credential{Token: validated, User: user, ExpiresAt: expires}
	if err := s.store.Set(ctx, actorID, projectID, credential, s.ttl); err != nil {
		return ConnectionStatus{}, err
	}
	if err := s.record(ctx, projectID, actorID, "github.connected", map[string]any{"githubUserId": user.ID, "githubLogin": user.Login}); err != nil {
		_ = s.store.Delete(context.WithoutCancel(ctx), actorID, projectID)
		return ConnectionStatus{}, err
	}
	return ConnectionStatus{Connected: true, Source: "user", User: &user, ExpiresAt: &expires}, nil
}

func (s *Service) Disconnect(ctx context.Context, projectID, actorID string) (ConnectionStatus, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return ConnectionStatus{}, err
	}
	if err := s.store.Delete(ctx, actorID, projectID); err != nil {
		return ConnectionStatus{}, err
	}
	if err := s.record(ctx, projectID, actorID, "github.disconnected", nil); err != nil {
		return ConnectionStatus{}, err
	}
	if s.platform != nil {
		return s.platformStatus(ctx)
	}
	return ConnectionStatus{Connected: false}, nil
}

func (s *Service) Status(ctx context.Context, projectID, actorID string) (ConnectionStatus, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, core.ActionView); err != nil {
		return ConnectionStatus{}, err
	}
	if s.platform != nil {
		return s.platformStatus(ctx)
	}
	credential, err := s.store.Get(ctx, actorID, projectID)
	if errors.Is(err, ErrCredentialNotFound) {
		return ConnectionStatus{Connected: false}, nil
	}
	if err != nil {
		return ConnectionStatus{}, err
	}
	return ConnectionStatus{Connected: true, Source: "user", User: &credential.User, ExpiresAt: &credential.ExpiresAt}, nil
}

func (s *Service) platformStatus(ctx context.Context) (ConnectionStatus, error) {
	_, expiresAt, err := s.platform.Token(ctx)
	if err != nil {
		return ConnectionStatus{}, err
	}
	return ConnectionStatus{
		Connected: true, Source: "platform", Organization: s.platform.Organization(), ExpiresAt: &expiresAt,
	}, nil
}

type resolvedCredential struct {
	Token        string
	Organization string
	UserLogin    string
	Platform     bool
}

func (s *Service) resolveCredential(
	ctx context.Context,
	projectID, actorID string,
	action core.Action,
	owner string,
) (resolvedCredential, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, action); err != nil {
		return resolvedCredential{}, err
	}
	owner = strings.TrimSpace(owner)
	if s.platform != nil && (owner == "" || strings.EqualFold(owner, s.platform.Organization())) {
		token, _, err := s.platform.Token(ctx)
		if err != nil {
			return resolvedCredential{}, err
		}
		return resolvedCredential{Token: token, Organization: s.platform.Organization(), Platform: true}, nil
	}
	credential, err := s.store.Get(ctx, actorID, projectID)
	if errors.Is(err, ErrCredentialNotFound) {
		return resolvedCredential{}, &Error{Code: "authentication_required", Status: 401, Detail: "Connect GitHub before continuing"}
	}
	return resolvedCredential{Token: credential.Token, UserLogin: credential.User.Login}, err
}

func (s *Service) credential(ctx context.Context, projectID, actorID string, action core.Action) (Credential, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, action); err != nil {
		return Credential{}, err
	}
	credential, err := s.store.Get(ctx, actorID, projectID)
	if errors.Is(err, ErrCredentialNotFound) {
		return Credential{}, &Error{Code: "authentication_required", Status: 401, Detail: "Connect GitHub before continuing"}
	}
	return credential, err
}

func (s *Service) Repositories(ctx context.Context, projectID, actorID string) ([]Repository, error) {
	credential, err := s.resolveCredential(ctx, projectID, actorID, core.ActionView, "")
	if err != nil {
		return nil, err
	}
	if credential.Platform {
		if client, ok := s.api.(interface {
			InstallationRepositories(context.Context, string) ([]Repository, error)
		}); ok {
			return client.InstallationRepositories(ctx, credential.Token)
		}
	}
	return s.api.Repositories(ctx, credential.Token)
}

func (s *Service) CreateRepository(ctx context.Context, projectID, actorID string, input CreateRepositoryInput) (CreateRepositoryResult, error) {
	credential, err := s.resolveCredential(ctx, projectID, actorID, core.ActionPublish, input.Owner)
	if err != nil {
		return CreateRepositoryResult{}, err
	}
	if !input.Confirm {
		return CreateRepositoryResult{}, &Error{Code: "confirmation_required", Status: 428, Detail: "confirm must be true before GitHub can create a repository"}
	}
	if input.Name, err = validateRepositoryPart(input.Name, "name"); err != nil {
		return CreateRepositoryResult{}, err
	}
	input.Owner = strings.TrimSpace(input.Owner)
	endpointOwner := input.Owner
	if credential.Platform {
		input.Owner, endpointOwner = credential.Organization, credential.Organization
	} else if input.Owner == "" || strings.EqualFold(input.Owner, credential.UserLogin) {
		input.Owner, endpointOwner = credential.UserLogin, ""
	} else if input.Owner, err = validateRepositoryPart(input.Owner, "owner"); err != nil {
		return CreateRepositoryResult{}, err
	} else {
		endpointOwner = input.Owner
	}
	input.Description = strings.TrimSpace(input.Description)
	if len(input.Description) > 350 || containsCredential(input.Description) {
		return CreateRepositoryResult{}, invalid("description must contain at most 350 safe characters")
	}
	input.CommitMessage = strings.TrimSpace(input.CommitMessage)
	if input.CommitMessage == "" || len(input.CommitMessage) > 500 || containsCredential(input.CommitMessage) {
		return CreateRepositoryResult{}, invalid("commitMessage must contain 1 to 500 safe characters")
	}
	input.Files, err = validateFiles(input.Files)
	if err != nil {
		return CreateRepositoryResult{}, err
	}
	repository, err := s.api.CreateRepository(ctx, credential.Token, RepositoryCreateOptions{
		Owner: endpointOwner, Name: input.Name, Description: input.Description, Private: input.Private,
	})
	if err != nil {
		return CreateRepositoryResult{}, err
	}
	if !strings.EqualFold(repository.Owner, input.Owner) || !strings.EqualFold(repository.Name, input.Name) {
		return CreateRepositoryResult{}, upstream("upstream_error", 502, "GitHub created an unexpected repository", nil)
	}
	if err := s.record(ctx, projectID, actorID, "github.repository_created", map[string]any{
		"repository": repository.FullName, "private": repository.Private, "url": repository.HTMLURL,
	}); err != nil {
		s.logger.Error("GitHub repository creation succeeded but audit persistence failed", "error", err, "project_id", projectID, "repository", repository.FullName)
	}
	push, err := s.Push(ctx, projectID, actorID, PushInput{
		PreviewInput: PreviewInput{
			Owner: repository.Owner, Repo: repository.Name, Branch: repository.DefaultBranch, Files: input.Files,
		},
		Message: input.CommitMessage, Confirm: true,
	})
	if err != nil {
		return CreateRepositoryResult{}, &Error{
			Code: "repository_initialization_failed", Status: 502,
			Detail: "GitHub created " + repository.FullName + " but the generated code could not be initialized; select the repository and retry the push",
			Cause:  err,
		}
	}
	return CreateRepositoryResult{Repository: repository, CommitSHA: push.CommitSHA, CommitURL: push.CommitURL}, nil
}

func (s *Service) Branches(ctx context.Context, projectID, actorID, owner, repo string) ([]Branch, error) {
	credential, err := s.resolveCredential(ctx, projectID, actorID, core.ActionView, owner)
	if err != nil {
		return nil, err
	}
	owner, err = validateRepositoryPart(owner, "owner")
	if err != nil {
		return nil, err
	}
	repo, err = validateRepositoryPart(repo, "repo")
	if err != nil {
		return nil, err
	}
	return s.api.Branches(ctx, credential.Token, owner, repo)
}
func (s *Service) Preview(ctx context.Context, projectID, actorID string, input PreviewInput) (ChangesPreview, error) {
	credential, err := s.resolveCredential(ctx, projectID, actorID, core.ActionView, input.Owner)
	if err != nil {
		return ChangesPreview{}, err
	}
	input, err = validatePreview(input)
	if err != nil {
		return ChangesPreview{}, err
	}
	return s.buildPreview(ctx, credential.Token, input, input.Branch, input.Branch)
}

func (s *Service) buildPreview(ctx context.Context, token string, input PreviewInput, sourceBranch, resultBranch string) (ChangesPreview, error) {
	reference, err := s.api.Reference(ctx, token, input.Owner, input.Repo, sourceBranch)
	if err != nil {
		return ChangesPreview{}, err
	}
	commit, err := s.api.Commit(ctx, token, input.Owner, input.Repo, reference.Object.SHA)
	if err != nil {
		return ChangesPreview{}, err
	}
	tree, err := s.api.Tree(ctx, token, input.Owner, input.Repo, commit.Tree.SHA)
	if err != nil {
		return ChangesPreview{}, err
	}
	if tree.Truncated || len(tree.Tree) > MaxRemoteTreeEntries {
		return ChangesPreview{}, &Error{Code: "remote_too_large", Status: 413, Detail: "GitHub repository tree is too large to preview safely"}
	}
	remote := map[string]gitTreeEntry{}
	for _, entry := range tree.Tree {
		if entry.Type != "blob" {
			continue
		}
		if safePath, pathErr := validateWorkspacePath(entry.Path); pathErr == nil {
			remote[safePath] = entry
		}
	}
	files := append([]WorkspaceFile(nil), input.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	changes := make([]Change, 0, len(files))
	for _, file := range files {
		remoteFile, exists := remote[file.Path]
		afterSHA := gitBlobSHA(file.Content)
		if !exists {
			changes = append(changes, Change{Path: file.Path, Status: "added", AfterSHA: afterSHA, AfterBytes: len([]byte(file.Content))})
			continue
		}
		if remoteFile.SHA == afterSHA {
			changes = append(changes, Change{Path: file.Path, Status: "unchanged", BeforeSHA: remoteFile.SHA, AfterSHA: afterSHA, BeforeBytes: remoteFile.Size, AfterBytes: len([]byte(file.Content))})
			continue
		}
		if remoteFile.Size > MaxRemoteBlobBytes {
			return ChangesPreview{}, &Error{Code: "remote_too_large", Status: 413, Detail: "Remote file " + file.Path + " is too large to preview safely"}
		}
		blob, err := s.api.Blob(ctx, token, input.Owner, input.Repo, remoteFile.SHA)
		if err != nil {
			return ChangesPreview{}, err
		}
		if blob.Size > MaxRemoteBlobBytes || blob.Encoding != "base64" {
			return ChangesPreview{}, &Error{Code: "remote_too_large", Status: 413, Detail: "Remote file " + file.Path + " cannot be decoded safely"}
		}
		before, err := base64.StdEncoding.DecodeString(strings.Map(func(value rune) rune {
			if value == '\r' || value == '\n' || value == ' ' || value == '\t' {
				return -1
			}
			return value
		}, blob.Content))
		if err != nil {
			return ChangesPreview{}, upstream("upstream_error", 502, "GitHub returned an invalid blob", err)
		}
		var lines *LineChanges
		if utf8.Valid(before) {
			calculated := calculateLineChanges(string(before), file.Content)
			lines = &calculated
		}
		changes = append(changes, Change{Path: file.Path, Status: "modified", BeforeSHA: remoteFile.SHA, AfterSHA: afterSHA, BeforeBytes: len(before), AfterBytes: len([]byte(file.Content)), Lines: lines})
	}
	return ChangesPreview{Repository: input.Owner + "/" + input.Repo, Branch: resultBranch, BaseCommitSHA: commit.SHA, BaseTreeSHA: tree.SHA, Changes: changes, Summary: summarize(changes)}, nil
}

func (s *Service) Push(ctx context.Context, projectID, actorID string, input PushInput) (PushResult, error) {
	credential, err := s.resolveCredential(ctx, projectID, actorID, core.ActionPublish, input.Owner)
	if err != nil {
		return PushResult{}, err
	}
	if !input.Confirm {
		return PushResult{}, &Error{Code: "confirmation_required", Status: 428, Detail: "confirm must be true before GitHub can write a commit"}
	}
	previewInput, err := validatePreview(input.PreviewInput)
	if err != nil {
		return PushResult{}, err
	}
	input.PreviewInput = previewInput
	input.Message = strings.TrimSpace(input.Message)
	if input.Message == "" || len(input.Message) > 500 || containsCredential(input.Message) {
		return PushResult{}, invalid("message must contain 1 to 500 safe characters")
	}
	sourceBranch := input.Branch
	if input.CreateBranch {
		input.BaseBranch, err = validateBranch(input.BaseBranch, "baseBranch")
		if err != nil || input.BaseBranch == input.Branch {
			return PushResult{}, invalid("baseBranch must be valid and differ from the new branch")
		}
		sourceBranch = input.BaseBranch
	}
	preview, err := s.buildPreview(ctx, credential.Token, input.PreviewInput, sourceBranch, input.Branch)
	if err != nil {
		return PushResult{}, err
	}
	changed := make([]Change, 0)
	for _, change := range preview.Changes {
		if change.Status != "unchanged" {
			changed = append(changed, change)
		}
	}
	result := PushResult{Repository: preview.Repository, Branch: input.Branch, CreatedBranch: input.CreateBranch, Preview: preview}
	if len(changed) == 0 {
		if input.CreateBranch {
			if err := s.api.CreateReference(ctx, credential.Token, input.Owner, input.Repo, input.Branch, preview.BaseCommitSHA); err != nil {
				return PushResult{}, err
			}
		}
		result.NoOp, result.CommitSHA = true, preview.BaseCommitSHA
		result.CommitURL = fmt.Sprintf("https://github.com/%s/%s/commit/%s", input.Owner, input.Repo, preview.BaseCommitSHA)
		return result, nil
	}
	byPath := map[string]WorkspaceFile{}
	for _, file := range input.Files {
		byPath[file.Path] = file
	}
	entries := make([]map[string]any, 0, len(changed))
	for _, change := range changed {
		file, exists := byPath[change.Path]
		if !exists {
			return PushResult{}, invalid("workspace changed during GitHub push")
		}
		sha, err := s.api.CreateBlob(ctx, credential.Token, input.Owner, input.Repo, file.Content)
		if err != nil {
			return PushResult{}, err
		}
		entries = append(entries, map[string]any{"path": file.Path, "mode": "100644", "type": "blob", "sha": sha})
	}
	treeSHA, err := s.api.CreateTree(ctx, credential.Token, input.Owner, input.Repo, preview.BaseTreeSHA, entries)
	if err != nil {
		return PushResult{}, err
	}
	commit, err := s.api.CreateCommit(ctx, credential.Token, input.Owner, input.Repo, input.Message, treeSHA, preview.BaseCommitSHA)
	if err != nil {
		return PushResult{}, err
	}
	if input.CreateBranch {
		err = s.api.CreateReference(ctx, credential.Token, input.Owner, input.Repo, input.Branch, commit.SHA)
	} else {
		err = s.api.UpdateReference(ctx, credential.Token, input.Owner, input.Repo, input.Branch, commit.SHA)
	}
	if err != nil {
		return PushResult{}, err
	}
	result.CommitSHA, result.CommitURL = commit.SHA, commit.HTMLURL
	if result.CommitURL == "" {
		result.CommitURL = fmt.Sprintf("https://github.com/%s/%s/commit/%s", input.Owner, input.Repo, commit.SHA)
	}
	if err := s.record(ctx, projectID, actorID, "github.workspace_pushed", map[string]any{"repository": result.Repository, "branch": result.Branch, "commitSha": result.CommitSHA}); err != nil {
		s.logger.Error("GitHub push succeeded but audit persistence failed", "error", err, "project_id", projectID, "commit_sha", result.CommitSHA)
	}
	return result, nil
}

func (s *Service) CreatePullRequest(ctx context.Context, projectID, actorID string, input PullRequestInput) (PullRequestResult, error) {
	credential, err := s.resolveCredential(ctx, projectID, actorID, core.ActionPublish, input.Owner)
	if err != nil {
		return PullRequestResult{}, err
	}
	if !input.Confirm {
		return PullRequestResult{}, &Error{Code: "confirmation_required", Status: 428, Detail: "confirm must be true before GitHub can create a pull request"}
	}
	if input.Owner, err = validateRepositoryPart(input.Owner, "owner"); err != nil {
		return PullRequestResult{}, err
	}
	if input.Repo, err = validateRepositoryPart(input.Repo, "repo"); err != nil {
		return PullRequestResult{}, err
	}
	if input.Head, err = validateBranch(input.Head, "head"); err != nil {
		return PullRequestResult{}, err
	}
	if input.Base, err = validateBranch(input.Base, "base"); err != nil {
		return PullRequestResult{}, err
	}
	input.Title, input.Body = strings.TrimSpace(input.Title), strings.TrimSpace(input.Body)
	if input.Head == input.Base || input.Title == "" || len(input.Title) > 256 || len(input.Body) > 65_000 || containsCredential(input.Title) || containsCredential(input.Body) {
		return PullRequestResult{}, invalid("pull request input is invalid or contains sensitive content")
	}
	response, err := s.api.CreatePullRequest(ctx, credential.Token, input)
	if err != nil {
		return PullRequestResult{}, err
	}
	number, _ := response["number"].(float64)
	if number < 1 {
		return PullRequestResult{}, upstream("upstream_error", 502, "GitHub returned an invalid pull request", nil)
	}
	result := PullRequestResult{Repository: input.Owner + "/" + input.Repo, Number: int(number), Title: input.Title, State: "open", Draft: input.Draft, Head: input.Head, Base: input.Base}
	if value, ok := response["title"].(string); ok && value != "" {
		result.Title = value
	}
	if value, ok := response["state"].(string); ok && value != "" {
		result.State = value
	}
	if value, ok := response["draft"].(bool); ok {
		result.Draft = value
	}
	if value, ok := response["html_url"].(string); ok {
		result.URL = value
	}
	if result.URL == "" {
		result.URL = fmt.Sprintf("https://github.com/%s/%s/pull/%d", input.Owner, input.Repo, result.Number)
	}
	if err := s.record(ctx, projectID, actorID, "github.pull_request_created", map[string]any{"repository": result.Repository, "number": result.Number, "url": result.URL}); err != nil {
		s.logger.Error("GitHub pull request succeeded but audit persistence failed", "error", err, "project_id", projectID, "pull_request", result.Number)
	}
	return result, nil
}

func (s *Service) record(ctx context.Context, projectID, actorID, action string, metadata map[string]any) error {
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return err
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return err
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	return s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&storage.AuditEventModel{ID: uuid.New(), ProjectID: &projectUUID, ActorID: &actorUUID, Action: action, TargetType: "github_integration", TargetID: projectID, Metadata: payload, CreatedAt: now}).Error; err != nil {
			return err
		}
		eventPayload, _ := json.Marshal(map[string]any{"projectId": projectID, "actorId": actorID, "action": action, "metadata": metadata})
		return tx.Create(&storage.OutboxEventModel{ID: uuid.New(), AggregateType: "github_integration", AggregateID: projectID, EventType: action, Subject: "worksflow." + strings.ReplaceAll(action, "_", "."), Payload: eventPayload, Headers: json.RawMessage(`{}`), AvailableAt: now, CreatedAt: now}).Error
	})
}

func calculateLineChanges(before, after string) LineChanges {
	beforeLines, afterLines := strings.Split(before, "\n"), strings.Split(after, "\n")
	prefix := 0
	for prefix < len(beforeLines) && prefix < len(afterLines) && beforeLines[prefix] == afterLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(beforeLines)-prefix && suffix < len(afterLines)-prefix && beforeLines[len(beforeLines)-1-suffix] == afterLines[len(afterLines)-1-suffix] {
		suffix++
	}
	return LineChanges{Additions: max(0, len(afterLines)-prefix-suffix), Deletions: max(0, len(beforeLines)-prefix-suffix)}
}
func summarize(changes []Change) ChangeSummary {
	result := ChangeSummary{}
	for _, change := range changes {
		switch change.Status {
		case "added":
			result.Added++
		case "modified":
			result.Modified++
		case "deleted":
			result.Deleted++
		case "unchanged":
			result.Unchanged++
		}
	}
	result.Changed = result.Added + result.Modified + result.Deleted
	return result
}
