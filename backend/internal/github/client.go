package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxUpstreamResponseBytes = 24_000_000

type APIClient struct {
	baseURL *url.URL
	client  *http.Client
	now     func() time.Time
}

func NewAPIClient(baseURL string, timeout time.Duration, client *http.Client) (*APIClient, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("GitHub API base URL must be an absolute HTTPS origin")
	}
	if timeout <= 0 {
		return nil, errors.New("GitHub request timeout must be positive")
	}
	if client == nil {
		client = &http.Client{Timeout: timeout}
	} else if client.Timeout == 0 || client.Timeout > timeout {
		clone := *client
		clone.Timeout = timeout
		client = &clone
	}
	return &APIClient{baseURL: parsed, client: client, now: time.Now}, nil
}

func (c *APIClient) request(ctx context.Context, token, method, endpoint string, body any, output any) error {
	if !strings.HasPrefix(endpoint, "/") || strings.HasPrefix(endpoint, "//") {
		return invalid("GitHub API path is invalid")
	}
	relative, err := url.Parse(endpoint)
	if err != nil || !strings.HasPrefix(relative.Path, "/") {
		return invalid("GitHub API path is invalid")
	}
	resolved := *c.baseURL
	resolved.Path = strings.TrimRight(c.baseURL.Path, "/") + relative.Path
	resolved.RawQuery = relative.RawQuery
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, resolved.String(), reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "worksflow-builder")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return upstream("request_timeout", http.StatusGatewayTimeout, "GitHub request timed out", err)
		}
		return upstream("upstream_error", http.StatusBadGateway, "GitHub could not be reached", err)
	}
	defer response.Body.Close()
	if response.ContentLength > maxUpstreamResponseBytes {
		return upstream("remote_too_large", http.StatusRequestEntityTooLarge, "GitHub response is too large", nil)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxUpstreamResponseBytes+1))
	if err != nil {
		return upstream("upstream_error", http.StatusBadGateway, "GitHub response could not be read", err)
	}
	if len(payload) > maxUpstreamResponseBytes {
		return upstream("remote_too_large", http.StatusRequestEntityTooLarge, "GitHub response is too large", nil)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return c.responseError(response, payload, token)
	}
	if output != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, output); err != nil {
			return upstream("upstream_error", http.StatusBadGateway, "GitHub returned malformed JSON", err)
		}
	}
	return nil
}

func (c *APIClient) responseError(response *http.Response, payload []byte, token string) error {
	var upstreamBody struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(payload, &upstreamBody)
	detail := strings.TrimSpace(upstreamBody.Message)
	if detail != "" {
		detail = strings.ReplaceAll(detail, token, "[REDACTED]")
		detail = credentialPattern.ReplaceAllString(detail, "[REDACTED]")
		detail = strings.ReplaceAll(detail, "\n", " ")
		if len(detail) > 500 {
			detail = detail[:500]
		}
	}
	suffix := ""
	if detail != "" {
		suffix = ": " + detail
	}
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return upstream("invalid_token", http.StatusUnauthorized, "GitHub rejected the credential"+suffix, nil)
	case http.StatusForbidden:
		remaining := response.Header.Get("X-RateLimit-Remaining")
		if remaining == "0" || response.Header.Get("Retry-After") != "" || strings.Contains(strings.ToLower(detail), "rate limit") {
			return c.rateLimitError(response)
		}
		return upstream("forbidden", http.StatusForbidden, "GitHub denied the operation"+suffix, nil)
	case http.StatusNotFound:
		return upstream("not_found", http.StatusNotFound, "GitHub resource was not found or is inaccessible"+suffix, nil)
	case http.StatusConflict:
		return upstream("conflict", http.StatusConflict, "GitHub reported a repository conflict"+suffix, nil)
	case http.StatusUnprocessableEntity:
		return upstream("validation_failed", http.StatusUnprocessableEntity, "GitHub rejected the operation"+suffix, nil)
	case http.StatusTooManyRequests:
		return c.rateLimitError(response)
	default:
		return upstream("upstream_error", http.StatusBadGateway, fmt.Sprintf("GitHub returned HTTP %d%s", response.StatusCode, suffix), nil)
	}
}

func (c *APIClient) rateLimitError(response *http.Response) error {
	retryAfter, _ := strconv.Atoi(response.Header.Get("Retry-After"))
	if retryAfter <= 0 {
		reset, _ := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64)
		if reset > 0 {
			retryAfter = int(time.Until(time.Unix(reset, 0)).Seconds())
		}
	}
	if retryAfter <= 0 {
		retryAfter = 60
	}
	return &Error{Code: "rate_limited", Status: http.StatusTooManyRequests, Detail: "GitHub rate limit reached", RetryAfter: retryAfter}
}

func repoEndpoint(owner, repo string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}
func escapedBranch(branch string) string {
	parts := strings.Split(branch, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}

type apiUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	HTMLURL   string `json:"html_url"`
}
type apiRepository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	UpdatedAt     string `json:"updated_at"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
	Permissions RepositoryPermissions `json:"permissions"`
}
type apiBranch struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
	Commit    struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}
type gitReference struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}
type gitCommit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Tree    struct {
		SHA string `json:"sha"`
	} `json:"tree"`
}
type gitTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int    `json:"size"`
}
type gitTree struct {
	SHA       string         `json:"sha"`
	Truncated bool           `json:"truncated"`
	Tree      []gitTreeEntry `json:"tree"`
}
type gitBlob struct {
	SHA      string `json:"sha"`
	Size     int    `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

func (c *APIClient) AuthenticatedUser(ctx context.Context, token string) (User, error) {
	var result apiUser
	if err := c.request(ctx, token, http.MethodGet, "/user", nil, &result); err != nil {
		return User{}, err
	}
	if result.ID < 1 || result.Login == "" || result.AvatarURL == "" || result.HTMLURL == "" {
		return User{}, upstream("upstream_error", http.StatusBadGateway, "GitHub returned an invalid user", nil)
	}
	return User{ID: result.ID, Login: result.Login, Name: result.Name, AvatarURL: result.AvatarURL, HTMLURL: result.HTMLURL}, nil
}
func (c *APIClient) Repositories(ctx context.Context, token string) ([]Repository, error) {
	result := make([]Repository, 0)
	for page := 1; page <= 5; page++ {
		var values []apiRepository
		endpoint := fmt.Sprintf("/user/repos?affiliation=owner%%2Ccollaborator%%2Corganization_member&sort=updated&per_page=100&page=%d", page)
		if err := c.request(ctx, token, http.MethodGet, endpoint, nil, &values); err != nil {
			return nil, err
		}
		for _, value := range values {
			if value.ID < 1 || value.Name == "" || value.FullName == "" || value.Owner.Login == "" {
				return nil, upstream("upstream_error", http.StatusBadGateway, "GitHub returned an invalid repository", nil)
			}
			result = append(result, Repository{ID: value.ID, Name: value.Name, FullName: value.FullName, Owner: value.Owner.Login, Private: value.Private, Archived: value.Archived, HTMLURL: value.HTMLURL, DefaultBranch: value.DefaultBranch, UpdatedAt: value.UpdatedAt, Permissions: value.Permissions})
		}
		if len(values) < 100 {
			break
		}
	}
	return result, nil
}
func (c *APIClient) Branches(ctx context.Context, token, owner, repo string) ([]Branch, error) {
	result := make([]Branch, 0)
	for page := 1; page <= 5; page++ {
		var values []apiBranch
		if err := c.request(ctx, token, http.MethodGet, fmt.Sprintf("%s/branches?per_page=100&page=%d", repoEndpoint(owner, repo), page), nil, &values); err != nil {
			return nil, err
		}
		for _, value := range values {
			result = append(result, Branch{Name: value.Name, CommitSHA: value.Commit.SHA, Protected: value.Protected})
		}
		if len(values) < 100 {
			break
		}
	}
	return result, nil
}
func (c *APIClient) Reference(ctx context.Context, token, owner, repo, branch string) (gitReference, error) {
	var result gitReference
	err := c.request(ctx, token, http.MethodGet, repoEndpoint(owner, repo)+"/git/ref/heads/"+escapedBranch(branch), nil, &result)
	return result, err
}
func (c *APIClient) Commit(ctx context.Context, token, owner, repo, sha string) (gitCommit, error) {
	var result gitCommit
	err := c.request(ctx, token, http.MethodGet, repoEndpoint(owner, repo)+"/git/commits/"+url.PathEscape(sha), nil, &result)
	return result, err
}
func (c *APIClient) Tree(ctx context.Context, token, owner, repo, sha string) (gitTree, error) {
	var result gitTree
	err := c.request(ctx, token, http.MethodGet, repoEndpoint(owner, repo)+"/git/trees/"+url.PathEscape(sha)+"?recursive=1", nil, &result)
	return result, err
}
func (c *APIClient) Blob(ctx context.Context, token, owner, repo, sha string) (gitBlob, error) {
	var result gitBlob
	err := c.request(ctx, token, http.MethodGet, repoEndpoint(owner, repo)+"/git/blobs/"+url.PathEscape(sha), nil, &result)
	return result, err
}
func (c *APIClient) CreateBlob(ctx context.Context, token, owner, repo, content string) (string, error) {
	var result struct {
		SHA string `json:"sha"`
	}
	err := c.request(ctx, token, http.MethodPost, repoEndpoint(owner, repo)+"/git/blobs", map[string]any{"content": content, "encoding": "utf-8"}, &result)
	return result.SHA, err
}
func (c *APIClient) CreateTree(ctx context.Context, token, owner, repo, base string, entries []map[string]any) (string, error) {
	var result struct {
		SHA string `json:"sha"`
	}
	err := c.request(ctx, token, http.MethodPost, repoEndpoint(owner, repo)+"/git/trees", map[string]any{"base_tree": base, "tree": entries}, &result)
	return result.SHA, err
}
func (c *APIClient) CreateCommit(ctx context.Context, token, owner, repo, message, tree, parent string) (gitCommit, error) {
	var result gitCommit
	err := c.request(ctx, token, http.MethodPost, repoEndpoint(owner, repo)+"/git/commits", map[string]any{"message": message, "tree": tree, "parents": []string{parent}}, &result)
	return result, err
}
func (c *APIClient) UpdateReference(ctx context.Context, token, owner, repo, branch, sha string) error {
	return c.request(ctx, token, http.MethodPatch, repoEndpoint(owner, repo)+"/git/refs/heads/"+escapedBranch(branch), map[string]any{"sha": sha, "force": false}, nil)
}
func (c *APIClient) CreateReference(ctx context.Context, token, owner, repo, branch, sha string) error {
	return c.request(ctx, token, http.MethodPost, repoEndpoint(owner, repo)+"/git/refs", map[string]any{"ref": "refs/heads/" + branch, "sha": sha}, nil)
}
func (c *APIClient) CreatePullRequest(ctx context.Context, token string, input PullRequestInput) (map[string]any, error) {
	maintainer := true
	if input.MaintainerCanModify != nil {
		maintainer = *input.MaintainerCanModify
	}
	body := map[string]any{"title": input.Title, "head": input.Head, "base": input.Base, "body": input.Body, "draft": input.Draft, "maintainer_can_modify": maintainer}
	var result map[string]any
	err := c.request(ctx, token, http.MethodPost, repoEndpoint(input.Owner, input.Repo)+"/pulls", body, &result)
	return result, err
}
