package lsp

import (
	"errors"
	"net/url"
	"strings"

	"github.com/worksflow/builder/backend/internal/repository"
)

const serverWorkspaceRootURI = "file:///workspace"

var ErrServerURIInvalid = errors.New("invalid container-local LSP server URI")

// ServerWorkspaceURI exposes only the fixed read-only mount inside the
// language-server container. Host paths and Candidate capability URIs never
// cross the process boundary.
func ServerWorkspaceURI(repositoryRoot string) (string, error) {
	if repositoryRoot == "." {
		return serverWorkspaceRootURI, nil
	}
	if normalized, err := repository.NormalizePath(repositoryRoot); err != nil || normalized != repositoryRoot {
		return "", ErrServerURIInvalid
	}
	return serverFileURI(repositoryRoot), nil
}

// ServerDocumentURI converts one browser-visible Candidate URI to the exact
// container-local file URI understood by ordinary language servers.
func ServerDocumentURI(modelURI string, head SandboxHeadFence) (string, error) {
	if head.Validate() != nil {
		return "", ErrServerURIInvalid
	}
	identity, err := ParseCandidateModelURI(modelURI)
	if err != nil || identity.ProjectID != head.ProjectID || identity.CandidateID != head.CandidateID {
		return "", ErrServerURIInvalid
	}
	return serverFileURI(identity.Path), nil
}

// CandidateDocumentURI converts a server-returned URI back to the only
// browser-visible identity. The wire spelling must be canonical and remain
// below /workspace; aliases, hosts, traversal and protected paths fail closed.
func CandidateDocumentURI(serverURI string, head SandboxHeadFence) (string, error) {
	if head.Validate() != nil || serverURI == "" || serverURI != strings.TrimSpace(serverURI) ||
		len(serverURI) > 1_024 || strings.ContainsAny(serverURI, "\r\n\x00") {
		return "", ErrServerURIInvalid
	}
	parsed, err := url.Parse(serverURI)
	if err != nil || parsed.Scheme != "file" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrServerURIInvalid
	}
	escaped := parsed.EscapedPath()
	const prefix = "/workspace/"
	if !strings.HasPrefix(escaped, prefix) || strings.HasSuffix(escaped, "/") {
		return "", ErrServerURIInvalid
	}
	rawSegments := strings.Split(strings.TrimPrefix(escaped, prefix), "/")
	decoded := make([]string, 0, len(rawSegments))
	for _, raw := range rawSegments {
		segment, decodeErr := url.PathUnescape(raw)
		if raw == "" || decodeErr != nil || segment == "" || url.PathEscape(segment) != raw ||
			strings.ContainsAny(segment, "/\\\x00") {
			return "", ErrServerURIInvalid
		}
		decoded = append(decoded, segment)
	}
	repositoryPath := strings.Join(decoded, "/")
	if normalized, normalizeErr := repository.NormalizePath(repositoryPath); normalizeErr != nil ||
		normalized != repositoryPath || serverFileURI(repositoryPath) != serverURI {
		return "", ErrServerURIInvalid
	}
	modelURI, err := CandidateModelURI(head.ProjectID, head.CandidateID, repositoryPath)
	if err != nil {
		return "", ErrServerURIInvalid
	}
	return modelURI, nil
}

func serverFileURI(repositoryPath string) string {
	segments := strings.Split(repositoryPath, "/")
	encoded := make([]string, len(segments))
	for index, segment := range segments {
		encoded[index] = url.PathEscape(segment)
	}
	return serverWorkspaceRootURI + "/" + strings.Join(encoded, "/")
}

// serverRequestPayload replaces the sole admitted textDocument URI in a
// freshly decoded browser DTO. It never reflects over arbitrary JSON.
func serverRequestPayload(
	payload BrowserRequestPayload,
	document DocumentFence,
	head SandboxHeadFence,
) (BrowserRequestPayload, error) {
	if payload == nil || document.ValidateAgainstHead(head) != nil {
		return nil, ErrServerURIInvalid
	}
	uri, err := ServerDocumentURI(document.ModelURI, head)
	if err != nil {
		return nil, err
	}
	textDocument := BrowserTextDocumentIdentifier{URI: uri}
	switch value := payload.(type) {
	case TextDocumentPositionPayload:
		if value.TextDocument.URI != document.ModelURI {
			return nil, ErrServerURIInvalid
		}
		value.TextDocument = textDocument
		return value, nil
	case DocumentSymbolPayload:
		if value.TextDocument.URI != document.ModelURI {
			return nil, ErrServerURIInvalid
		}
		value.TextDocument = textDocument
		return value, nil
	case ReferencesPayload:
		if value.TextDocument.URI != document.ModelURI {
			return nil, ErrServerURIInvalid
		}
		value.TextDocument = textDocument
		return value, nil
	case CompletionPayload:
		if value.TextDocument.URI != document.ModelURI {
			return nil, ErrServerURIInvalid
		}
		value.TextDocument = textDocument
		return value, nil
	case SignatureHelpPayload:
		if value.TextDocument.URI != document.ModelURI {
			return nil, ErrServerURIInvalid
		}
		value.TextDocument = textDocument
		return value, nil
	default:
		return nil, ErrServerURIInvalid
	}
}
