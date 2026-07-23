package github

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/core"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type memoryCredentialStore struct{ value Credential }

func (s *memoryCredentialStore) Get(context.Context, string, string) (Credential, error) {
	if s.value.Token == "" {
		return Credential{}, ErrCredentialNotFound
	}
	return s.value, nil
}

type repositoryCreationClient struct {
	options       RepositoryCreateOptions
	token         string
	updatedBranch string
	updatedSHA    string
}

func (c *repositoryCreationClient) AuthenticatedUser(context.Context, string) (User, error) {
	return User{}, nil
}
func (c *repositoryCreationClient) Repositories(context.Context, string) ([]Repository, error) {
	return nil, nil
}
func (c *repositoryCreationClient) CreateRepository(_ context.Context, token string, options RepositoryCreateOptions) (Repository, error) {
	c.options = options
	c.token = token
	owner := options.Owner
	if owner == "" {
		owner = "noir"
	}
	return Repository{
		ID: 71, Name: options.Name, FullName: owner + "/" + options.Name, Owner: owner,
		Private: options.Private, HTMLURL: "https://github.com/" + owner + "/" + options.Name,
		DefaultBranch: "main", Permissions: RepositoryPermissions{Pull: true, Push: true, Admin: true},
	}, nil
}

type fixedPlatformCredential struct {
	organization string
	token        string
}

func (c fixedPlatformCredential) Organization() string { return c.organization }
func (c fixedPlatformCredential) Token(context.Context) (string, time.Time, error) {
	return c.token, time.Now().UTC().Add(time.Hour), nil
}
func (c *repositoryCreationClient) Branches(context.Context, string, string, string) ([]Branch, error) {
	return nil, nil
}
func (c *repositoryCreationClient) Reference(context.Context, string, string, string, string) (gitReference, error) {
	value := gitReference{}
	value.Object.SHA = "initial-commit"
	return value, nil
}
func (c *repositoryCreationClient) Commit(context.Context, string, string, string, string) (gitCommit, error) {
	value := gitCommit{SHA: "initial-commit"}
	value.Tree.SHA = "initial-tree"
	return value, nil
}
func (c *repositoryCreationClient) Tree(context.Context, string, string, string, string) (gitTree, error) {
	return gitTree{SHA: "initial-tree"}, nil
}
func (c *repositoryCreationClient) Blob(context.Context, string, string, string, string) (gitBlob, error) {
	return gitBlob{}, nil
}
func (c *repositoryCreationClient) CreateBlob(context.Context, string, string, string, string) (string, error) {
	return "generated-blob", nil
}
func (c *repositoryCreationClient) CreateTree(context.Context, string, string, string, string, []map[string]any) (string, error) {
	return "generated-tree", nil
}
func (c *repositoryCreationClient) CreateCommit(context.Context, string, string, string, string, string, string) (gitCommit, error) {
	return gitCommit{SHA: "generated-commit", HTMLURL: "https://github.com/noir/video-platform/commit/generated-commit"}, nil
}
func (c *repositoryCreationClient) UpdateReference(_ context.Context, _, _, _, branch, sha string) error {
	c.updatedBranch, c.updatedSHA = branch, sha
	return nil
}
func (c *repositoryCreationClient) CreateReference(context.Context, string, string, string, string, string) error {
	return nil
}
func (c *repositoryCreationClient) CreatePullRequest(context.Context, string, PullRequestInput) (map[string]any, error) {
	return nil, nil
}
func (s *memoryCredentialStore) Set(_ context.Context, _, _ string, value Credential, _ time.Duration) error {
	s.value = value
	return nil
}
func (s *memoryCredentialStore) Delete(context.Context, string, string) error {
	s.value = Credential{}
	return nil
}

type allowGitHubAccess struct{}

func (allowGitHubAccess) Authorize(context.Context, string, string, core.Action) (core.Role, error) {
	return core.RoleOwner, nil
}

type previewClient struct{}

func (previewClient) AuthenticatedUser(context.Context, string) (User, error)    { return User{}, nil }
func (previewClient) Repositories(context.Context, string) ([]Repository, error) { return nil, nil }
func (previewClient) CreateRepository(context.Context, string, RepositoryCreateOptions) (Repository, error) {
	return Repository{}, nil
}
func (previewClient) Branches(context.Context, string, string, string) ([]Branch, error) {
	return nil, nil
}
func (previewClient) Reference(context.Context, string, string, string, string) (gitReference, error) {
	value := gitReference{}
	value.Object.SHA = "commit"
	return value, nil
}
func (previewClient) Commit(context.Context, string, string, string, string) (gitCommit, error) {
	value := gitCommit{SHA: "commit"}
	value.Tree.SHA = "tree"
	return value, nil
}
func (previewClient) Tree(context.Context, string, string, string, string) (gitTree, error) {
	return gitTree{SHA: "tree", Tree: []gitTreeEntry{{Path: "same.txt", Type: "blob", SHA: gitBlobSHA("same"), Size: 4}, {Path: "change.txt", Type: "blob", SHA: "old", Size: 3}}}, nil
}
func (previewClient) Blob(context.Context, string, string, string, string) (gitBlob, error) {
	return gitBlob{Size: 3, Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte("old"))}, nil
}
func (previewClient) CreateBlob(context.Context, string, string, string, string) (string, error) {
	return "", nil
}
func (previewClient) CreateTree(context.Context, string, string, string, string, []map[string]any) (string, error) {
	return "", nil
}
func (previewClient) CreateCommit(context.Context, string, string, string, string, string, string) (gitCommit, error) {
	return gitCommit{}, nil
}
func (previewClient) UpdateReference(context.Context, string, string, string, string, string) error {
	return nil
}
func (previewClient) CreateReference(context.Context, string, string, string, string, string) error {
	return nil
}
func (previewClient) CreatePullRequest(context.Context, string, PullRequestInput) (map[string]any, error) {
	return nil, nil
}

func TestPreviewIsAdditiveAndComputesLineChanges(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 user=test dbname=test sslmode=disable", PreferSimpleProtocol: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryCredentialStore{value: Credential{Token: "token", User: User{ID: 1}, ExpiresAt: time.Now().Add(time.Hour)}}
	service, err := NewService(previewClient{}, store, allowGitHubAccess{}, db, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.Preview(context.Background(), "project", "actor", PreviewInput{Owner: "owner", Repo: "repo", Branch: "main", Files: []WorkspaceFile{{Path: "same.txt", Content: "same"}, {Path: "change.txt", Content: "new\nline"}, {Path: "added.txt", Content: "added"}}})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Summary.Added != 1 || preview.Summary.Modified != 1 || preview.Summary.Unchanged != 1 || preview.Summary.Deleted != 0 || preview.Summary.Changed != 2 {
		t.Fatalf("preview summary = %+v", preview.Summary)
	}
}

func TestCreateRepositoryInitializesGeneratedWorkspace(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 user=test dbname=test sslmode=disable", PreferSimpleProtocol: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	client := &repositoryCreationClient{}
	store := &memoryCredentialStore{value: Credential{
		Token: "token", User: User{ID: 7, Login: "noir"}, ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}}
	service, err := NewService(client, store, allowGitHubAccess{}, db, slog.New(slog.NewTextHandler(io.Discard, nil)), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CreateRepository(
		context.Background(),
		"71e3cb41-fc27-45cd-95b6-d6135edf11d3",
		"0f651d07-6d46-49cf-aed1-ef222b6ba2d5",
		CreateRepositoryInput{
			Owner: "noir", Name: "video-platform", Description: "Generated project", Private: true,
			Files:         []WorkspaceFile{{Path: "README.md", Content: "# Video platform\n"}},
			CommitMessage: "Initialize generated project", Confirm: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if client.options.Owner != "" || client.options.Name != "video-platform" || !client.options.Private {
		t.Fatalf("create options = %+v", client.options)
	}
	if client.updatedBranch != "main" || client.updatedSHA != "generated-commit" {
		t.Fatalf("updated reference = %s@%s", client.updatedBranch, client.updatedSHA)
	}
	if result.Repository.FullName != "noir/video-platform" || result.CommitSHA != "generated-commit" {
		t.Fatalf("result = %+v", result)
	}
}

func TestCreateRepositoryDefaultsToPlatformOrganization(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 user=test dbname=test sslmode=disable", PreferSimpleProtocol: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	client := &repositoryCreationClient{}
	service, err := NewService(
		client,
		&memoryCredentialStore{},
		allowGitHubAccess{},
		db,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		time.Hour,
		fixedPlatformCredential{organization: "ai-worksflow", token: "installation-token"},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CreateRepository(
		context.Background(),
		"71e3cb41-fc27-45cd-95b6-d6135edf11d3",
		"0f651d07-6d46-49cf-aed1-ef222b6ba2d5",
		CreateRepositoryInput{
			Name: "video-platform", Private: true,
			Files:         []WorkspaceFile{{Path: "README.md", Content: "# Video platform\n"}},
			CommitMessage: "Initialize generated project", Confirm: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if client.token != "installation-token" || client.options.Owner != "ai-worksflow" {
		t.Fatalf("platform create token/owner = %q/%q", client.token, client.options.Owner)
	}
	if result.Repository.FullName != "ai-worksflow/video-platform" {
		t.Fatalf("result repository = %q", result.Repository.FullName)
	}
}
