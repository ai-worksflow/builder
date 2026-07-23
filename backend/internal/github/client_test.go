package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAPIClientPreservesQueryAndAuthorization(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/user/repos" || request.URL.Query().Get("page") != "1" || request.Header.Get("Authorization") != "Bearer secret-token" {
			t.Fatalf("request = %s %s headers=%v", request.Method, request.URL.String(), request.Header)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode([]map[string]any{{
			"id": 1, "name": "repo", "full_name": "owner/repo", "private": true,
			"archived": false, "html_url": "https://github.com/owner/repo", "default_branch": "main",
			"owner": map[string]any{"login": "owner"}, "permissions": map[string]any{"pull": true, "push": true, "admin": false},
		}})
	}))
	defer server.Close()
	client, err := NewAPIClient(server.URL, time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	repositories, err := client.Repositories(context.Background(), "secret-token")
	if err != nil || len(repositories) != 1 || repositories[0].FullName != "owner/repo" {
		t.Fatalf("repositories=%+v error=%v", repositories, err)
	}
}

func TestAPIClientRedactsCredentialFromUpstreamError(t *testing.T) {
	t.Parallel()
	const token = "github_pat_abcdefghijklmnopqrstuvwxyz"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(writer).Encode(map[string]any{"message": "bad token " + token})
	}))
	defer server.Close()
	client, _ := NewAPIClient(server.URL, time.Second, server.Client())
	_, err := client.AuthenticatedUser(context.Background(), token)
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("error was not safely redacted: %v", err)
	}
}

func TestAPIClientCreatesPersonalAndOrganizationRepositories(t *testing.T) {
	t.Parallel()
	requests := make(chan string, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer secret-token" || json.NewDecoder(request.Body).Decode(&body) != nil {
			t.Fatalf("unexpected create request: %s %s", request.Method, request.URL.Path)
		}
		if body["name"] != "video-platform" || body["auto_init"] != true || body["private"] != true {
			t.Fatalf("create body = %#v", body)
		}
		requests <- request.URL.Path
		owner := "noir"
		if strings.HasPrefix(request.URL.Path, "/orgs/") {
			owner = "ai-worksflow"
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"id": 71, "name": "video-platform", "full_name": owner + "/video-platform",
			"private": true, "archived": false, "html_url": "https://github.com/" + owner + "/video-platform",
			"default_branch": "main", "owner": map[string]any{"login": owner},
			"permissions": map[string]any{"pull": true, "push": true, "admin": true},
		})
	}))
	defer server.Close()
	client, err := NewAPIClient(server.URL, time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	personal, err := client.CreateRepository(context.Background(), "secret-token", RepositoryCreateOptions{Name: "video-platform", Private: true})
	if err != nil || personal.FullName != "noir/video-platform" || personal.DefaultBranch != "main" {
		t.Fatalf("personal=%+v error=%v", personal, err)
	}
	organization, err := client.CreateRepository(context.Background(), "secret-token", RepositoryCreateOptions{Owner: "ai-worksflow", Name: "video-platform", Private: true})
	if err != nil || organization.FullName != "ai-worksflow/video-platform" {
		t.Fatalf("organization=%+v error=%v", organization, err)
	}
	if first, second := <-requests, <-requests; first != "/user/repos" || second != "/orgs/ai-worksflow/repos" {
		t.Fatalf("create paths = %q, %q", first, second)
	}
}
