package lsp

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	CandidateModelScheme = "worksflow-candidate"
	maxSafeWireInteger   = uint64(9_007_199_254_740_991)
)

var (
	ErrInvalidSandboxHead = errors.New("invalid LSP SandboxHeadFence")
	ErrInvalidDocument    = errors.New("invalid LSP DocumentFence")
	ErrInvalidModelURI    = errors.New("invalid LSP Candidate model URI")
	ErrHeadRebind         = errors.New("invalid LSP Candidate head rebind")
	digestPattern         = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// SandboxHeadFence is the only source-head identity accepted by the LSP
// control plane. Version is the Candidate aggregate version, not a
// SandboxSession version and not a browser-local generation.
type SandboxHeadFence struct {
	ProjectID        string `json:"projectId"`
	SessionID        string `json:"sessionId"`
	SessionEpoch     uint64 `json:"sessionEpoch"`
	CandidateID      string `json:"candidateId"`
	Version          uint64 `json:"version"`
	JournalSequence  uint64 `json:"journalSequence"`
	WriterLeaseEpoch uint64 `json:"writerLeaseEpoch"`
	TreeHash         string `json:"treeHash"`
}

func (fence SandboxHeadFence) Validate() error {
	if !canonicalUUID(fence.ProjectID) || !canonicalUUID(fence.SessionID) ||
		!canonicalUUID(fence.CandidateID) || fence.SessionEpoch == 0 ||
		fence.Version == 0 || fence.SessionEpoch > maxSafeWireInteger ||
		fence.Version > maxSafeWireInteger || fence.JournalSequence > maxSafeWireInteger ||
		fence.WriterLeaseEpoch > maxSafeWireInteger || !digestPattern.MatchString(fence.TreeHash) {
		return ErrInvalidSandboxHead
	}
	return nil
}

func (fence SandboxHeadFence) Equal(other SandboxHeadFence) bool {
	return fence == other
}

// MonotonicSuccessorOf only classifies a structurally possible Candidate CAS
// successor. A Gateway must still read Repository/Sandbox authority and prove
// that next is the exact current head before accepting client.headRebind.
func (fence SandboxHeadFence) MonotonicSuccessorOf(previous SandboxHeadFence) error {
	if fence.Validate() != nil || previous.Validate() != nil ||
		fence.ProjectID != previous.ProjectID || fence.SessionID != previous.SessionID ||
		fence.SessionEpoch != previous.SessionEpoch || fence.CandidateID != previous.CandidateID ||
		fence.WriterLeaseEpoch != previous.WriterLeaseEpoch ||
		fence.Version <= previous.Version || fence.JournalSequence <= previous.JournalSequence ||
		fence.Version-previous.Version != fence.JournalSequence-previous.JournalSequence ||
		fence.TreeHash == previous.TreeHash {
		return ErrHeadRebind
	}
	return nil
}

type DocumentFence struct {
	ModelURI         string `json:"modelUri"`
	OpenID           string `json:"openId"`
	ModelVersion     uint64 `json:"modelVersion"`
	SavedContentHash string `json:"savedContentHash"`
}

func (fence DocumentFence) Validate() error {
	if _, err := ParseCandidateModelURI(fence.ModelURI); err != nil ||
		!canonicalUUID(fence.OpenID) || fence.ModelVersion == 0 ||
		fence.ModelVersion > maxSafeWireInteger || !digestPattern.MatchString(fence.SavedContentHash) {
		return ErrInvalidDocument
	}
	return nil
}

func (fence DocumentFence) ValidateAgainstHead(head SandboxHeadFence) error {
	if fence.Validate() != nil || head.Validate() != nil {
		return ErrInvalidDocument
	}
	identity, err := ParseCandidateModelURI(fence.ModelURI)
	if err != nil || identity.ProjectID != head.ProjectID || identity.CandidateID != head.CandidateID {
		return ErrInvalidDocument
	}
	return nil
}

func (fence DocumentFence) Equal(other DocumentFence) bool {
	return fence == other
}

type CandidateModelIdentity struct {
	ProjectID   string
	CandidateID string
	Path        string
}

// CandidateWorkspaceURI names either the Candidate root (rootPath == ".") or
// a canonical service root. Document URIs remain file-only and continue to use
// CandidateModelURI, so a workspace root cannot become an opened document.
func CandidateWorkspaceURI(projectID, candidateID, rootPath string) (string, error) {
	if !canonicalUUID(projectID) || !canonicalUUID(candidateID) {
		return "", ErrInvalidModelURI
	}
	if rootPath == "." {
		return CandidateModelScheme + "://" + projectID + "/" + candidateID, nil
	}
	return CandidateModelURI(projectID, candidateID, rootPath)
}

func CandidateModelURI(projectID, candidateID, repositoryPath string) (string, error) {
	if !canonicalUUID(projectID) || !canonicalUUID(candidateID) {
		return "", ErrInvalidModelURI
	}
	normalized, err := repository.NormalizePath(repositoryPath)
	if err != nil || normalized != repositoryPath {
		return "", ErrInvalidModelURI
	}
	segments := strings.Split(normalized, "/")
	encoded := make([]string, 0, len(segments)+1)
	encoded = append(encoded, candidateID)
	for _, segment := range segments {
		encoded = append(encoded, url.PathEscape(segment))
	}
	return CandidateModelScheme + "://" + projectID + "/" + strings.Join(encoded, "/"), nil
}

func ParseCandidateModelURI(value string) (CandidateModelIdentity, error) {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 1_024 ||
		strings.ContainsAny(value, "\r\n\x00") {
		return CandidateModelIdentity{}, ErrInvalidModelURI
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != CandidateModelScheme || parsed.Opaque != "" || parsed.User != nil ||
		parsed.Host == "" || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		!canonicalUUID(parsed.Host) {
		return CandidateModelIdentity{}, ErrInvalidModelURI
	}
	escaped := parsed.EscapedPath()
	if !strings.HasPrefix(escaped, "/") || strings.HasSuffix(escaped, "/") {
		return CandidateModelIdentity{}, ErrInvalidModelURI
	}
	rawSegments := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
	if len(rawSegments) < 2 || rawSegments[0] == "" || !canonicalUUID(rawSegments[0]) {
		return CandidateModelIdentity{}, ErrInvalidModelURI
	}
	decoded := make([]string, 0, len(rawSegments)-1)
	for _, raw := range rawSegments[1:] {
		if raw == "" {
			return CandidateModelIdentity{}, ErrInvalidModelURI
		}
		segment, decodeErr := url.PathUnescape(raw)
		if decodeErr != nil || segment == "" || url.PathEscape(segment) != raw ||
			strings.ContainsAny(segment, "/\\\x00") {
			return CandidateModelIdentity{}, ErrInvalidModelURI
		}
		decoded = append(decoded, segment)
	}
	repositoryPath := strings.Join(decoded, "/")
	normalized, err := repository.NormalizePath(repositoryPath)
	if err != nil || normalized != repositoryPath {
		return CandidateModelIdentity{}, ErrInvalidModelURI
	}
	canonical, err := CandidateModelURI(parsed.Host, rawSegments[0], repositoryPath)
	if err != nil || canonical != value {
		return CandidateModelIdentity{}, ErrInvalidModelURI
	}
	return CandidateModelIdentity{
		ProjectID: parsed.Host, CandidateID: rawSegments[0], Path: repositoryPath,
	}, nil
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func invalidField(kind error, field string) error {
	return fmt.Errorf("%w: %s", kind, field)
}
