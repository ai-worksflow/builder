package github

import (
	"errors"
	"net/http"
	"time"
)

const (
	MaxRequestBytes      = 9_000_000
	MaxFileCount         = 250
	MaxFileBytes         = 1_500_000
	MaxTotalFileBytes    = 8_000_000
	MaxRemoteTreeEntries = 10_000
	MaxRemoteBlobBytes   = 2_000_000
)

type Error struct {
	Code       string
	Status     int
	Detail     string
	RetryAfter int
	Cause      error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Detail
}
func (e *Error) Unwrap() error { return e.Cause }
func AsError(err error) (*Error, bool) {
	var target *Error
	return target, errors.As(err, &target)
}
func invalid(detail string) *Error {
	return &Error{Code: "invalid_request", Status: http.StatusBadRequest, Detail: detail}
}
func upstream(code string, status int, detail string, cause error) *Error {
	return &Error{Code: code, Status: status, Detail: detail, Cause: cause}
}

type User struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name,omitempty"`
	AvatarURL string `json:"avatarUrl"`
	HTMLURL   string `json:"htmlUrl"`
}
type ConnectionStatus struct {
	Connected bool       `json:"connected"`
	Source    string     `json:"source,omitempty"`
	User      *User      `json:"user,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}
type RepositoryPermissions struct {
	Pull  bool `json:"pull"`
	Push  bool `json:"push"`
	Admin bool `json:"admin"`
}
type Repository struct {
	ID            int64                 `json:"id"`
	Name          string                `json:"name"`
	FullName      string                `json:"fullName"`
	Owner         string                `json:"owner"`
	Private       bool                  `json:"private"`
	Archived      bool                  `json:"archived"`
	HTMLURL       string                `json:"htmlUrl"`
	DefaultBranch string                `json:"defaultBranch"`
	UpdatedAt     string                `json:"updatedAt,omitempty"`
	Permissions   RepositoryPermissions `json:"permissions"`
}
type Branch struct {
	Name      string `json:"name"`
	CommitSHA string `json:"commitSha"`
	Protected bool   `json:"protected"`
}
type WorkspaceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type PreviewInput struct {
	Owner  string          `json:"owner"`
	Repo   string          `json:"repo"`
	Branch string          `json:"branch"`
	Files  []WorkspaceFile `json:"files"`
}
type LineChanges struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
}
type Change struct {
	Path        string       `json:"path"`
	Status      string       `json:"status"`
	BeforeSHA   string       `json:"beforeSha,omitempty"`
	AfterSHA    string       `json:"afterSha,omitempty"`
	BeforeBytes int          `json:"beforeBytes"`
	AfterBytes  int          `json:"afterBytes"`
	Lines       *LineChanges `json:"lines,omitempty"`
}
type ChangeSummary struct {
	Added     int `json:"added"`
	Modified  int `json:"modified"`
	Deleted   int `json:"deleted"`
	Unchanged int `json:"unchanged"`
	Changed   int `json:"changed"`
}
type ChangesPreview struct {
	Repository    string        `json:"repository"`
	Branch        string        `json:"branch"`
	BaseCommitSHA string        `json:"baseCommitSha"`
	BaseTreeSHA   string        `json:"baseTreeSha"`
	Changes       []Change      `json:"changes"`
	Summary       ChangeSummary `json:"summary"`
}
type PushInput struct {
	PreviewInput
	Message      string `json:"message"`
	Confirm      bool   `json:"confirm"`
	CreateBranch bool   `json:"createBranch,omitempty"`
	BaseBranch   string `json:"baseBranch,omitempty"`
}
type PushResult struct {
	Repository    string         `json:"repository"`
	Branch        string         `json:"branch"`
	CreatedBranch bool           `json:"createdBranch"`
	NoOp          bool           `json:"noOp"`
	CommitSHA     string         `json:"commitSha"`
	CommitURL     string         `json:"commitUrl"`
	Preview       ChangesPreview `json:"preview"`
}
type PullRequestInput struct {
	Owner               string `json:"owner"`
	Repo                string `json:"repo"`
	Head                string `json:"head"`
	Base                string `json:"base"`
	Title               string `json:"title"`
	Body                string `json:"body,omitempty"`
	Draft               bool   `json:"draft,omitempty"`
	MaintainerCanModify *bool  `json:"maintainerCanModify,omitempty"`
	Confirm             bool   `json:"confirm"`
}
type PullRequestResult struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Draft      bool   `json:"draft"`
	Head       string `json:"head"`
	Base       string `json:"base"`
	URL        string `json:"url"`
}
