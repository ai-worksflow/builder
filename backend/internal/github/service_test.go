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
