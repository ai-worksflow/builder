package lsp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	ProductionV1CapabilitySchemaVersion = "language-server-capabilities/v1"
	maxBrowserRequestPayloadBytes       = 32 << 10
	maxLSPPositionValue                 = uint64(2_147_483_647)
)

var (
	ErrInvalidMethodAllowlist          = errors.New("invalid LSP production-v1 method allowlist")
	ErrNonCanonicalMethodAllowlist     = errors.New("non-canonical LSP production-v1 method allowlist")
	ErrPermanentlyForbiddenMethod      = errors.New("permanently forbidden LSP method")
	ErrUnsupportedProductionV1Method   = errors.New("unsupported LSP production-v1 method")
	ErrCapabilityHashMismatch          = errors.New("LSP capability allowlist hash mismatch")
	ErrBrowserRequestMethodNotAdmitted = errors.New("LSP browser request method is not admitted")
	ErrInvalidBrowserRequestPayload    = errors.New("invalid LSP browser request payload")

	// productionV1MethodBaseline is the immutable platform ceiling documented
	// for production v1. Template profiles and runtime capabilities may only
	// narrow this set. Keep it sorted so a copied value is already canonical.
	productionV1MethodBaseline = []string{
		"textDocument/completion",
		"textDocument/declaration",
		"textDocument/definition",
		"textDocument/diagnostic",
		"textDocument/documentHighlight",
		"textDocument/documentSymbol",
		"textDocument/hover",
		"textDocument/implementation",
		"textDocument/inlayHint",
		"textDocument/publishDiagnostics",
		"textDocument/references",
		"textDocument/semanticTokens/full",
		"textDocument/semanticTokens/range",
		"textDocument/signatureHelp",
		"textDocument/typeDefinition",
	}
	productionV1MethodSet = stringSet(productionV1MethodBaseline)

	// productionV1BrowserRequestMethods is deliberately narrower than the
	// profile baseline. A method is not browser-request capable until its
	// method-specific strict decoder is implemented here.
	productionV1BrowserRequestMethods = []string{
		"textDocument/completion",
		"textDocument/declaration",
		"textDocument/definition",
		"textDocument/documentHighlight",
		"textDocument/documentSymbol",
		"textDocument/hover",
		"textDocument/implementation",
		"textDocument/references",
		"textDocument/signatureHelp",
		"textDocument/typeDefinition",
	}
	productionV1BrowserRequestMethodSet = stringSet(productionV1BrowserRequestMethods)

	permanentlyForbiddenMethods = []string{
		"client/registerCapability",
		"client/unregisterCapability",
		"codeAction/resolve",
		"completionItem/resolve",
		"textDocument/codeAction",
		"textDocument/didChange",
		"textDocument/didSave",
		"textDocument/formatting",
		"textDocument/onTypeFormatting",
		"textDocument/prepareRename",
		"textDocument/rangeFormatting",
		"textDocument/rename",
		"textDocument/willSaveWaitUntil",
		"workspace/applyEdit",
		"workspace/configuration",
		"workspace/didCreateFiles",
		"workspace/didDeleteFiles",
		"workspace/didRenameFiles",
		"workspace/executeCommand",
		"workspace/willCreateFiles",
		"workspace/willDeleteFiles",
		"workspace/willRenameFiles",
	}
	permanentlyForbiddenMethodSet = stringSet(permanentlyForbiddenMethods)
)

// ProductionV1MethodBaseline returns a defensive copy of the immutable
// production-v1 platform ceiling. It includes server-to-browser methods such
// as publishDiagnostics; use ProductionV1BrowserRequestMethods for requests.
func ProductionV1MethodBaseline() []string {
	return slices.Clone(productionV1MethodBaseline)
}

// ProductionV1BrowserRequestMethods returns only methods with a strict
// browser request decoder in this package.
func ProductionV1BrowserRequestMethods() []string {
	return slices.Clone(productionV1BrowserRequestMethods)
}

// PermanentlyForbiddenMethods returns the named write/command/dynamic
// registration methods that production v1 can never admit. The predicate
// below additionally rejects every workspace/client method and dangerous
// method family, including future spelling variants.
func PermanentlyForbiddenMethods() []string {
	return slices.Clone(permanentlyForbiddenMethods)
}

// IsPermanentlyForbiddenMethod is evaluated before baseline membership. A
// TemplateRelease cannot use an unknown/custom method name to re-enable a
// write, command, rename, formatting, code-action, or registration path.
func IsPermanentlyForbiddenMethod(method string) bool {
	if permanentlyForbiddenMethodSet[method] || strings.HasPrefix(method, "workspace/") ||
		strings.HasPrefix(method, "client/") {
		return true
	}
	lower := strings.ToLower(method)
	return strings.Contains(lower, "applyedit") ||
		strings.Contains(lower, "executecommand") ||
		strings.Contains(lower, "registercapability") ||
		strings.Contains(lower, "unregistercapability") ||
		strings.Contains(lower, "codeaction") ||
		strings.Contains(lower, "formatting") ||
		strings.Contains(lower, "preparerename") ||
		strings.HasSuffix(lower, "/rename") ||
		strings.Contains(lower, "renamefiles") ||
		strings.Contains(lower, "createfiles") ||
		strings.Contains(lower, "deletefiles") ||
		strings.Contains(lower, "willsavewaituntil")
}

// CanonicalizeProductionV1MethodAllowlist validates and sorts an exact method
// subset. It never trims, de-duplicates, or fills defaults: such normalization
// would let a malformed profile acquire authority accidentally.
func CanonicalizeProductionV1MethodAllowlist(methods []string) ([]string, error) {
	if len(methods) == 0 || len(methods) > 32 {
		return nil, ErrInvalidMethodAllowlist
	}
	result := slices.Clone(methods)
	seen := make(map[string]bool, len(result))
	for index, method := range result {
		if method == "" || method != strings.TrimSpace(method) || len(method) > 128 ||
			strings.ContainsAny(method, "\r\n\x00") {
			return nil, fmt.Errorf("%w: methods[%d]", ErrInvalidMethodAllowlist, index)
		}
		if IsPermanentlyForbiddenMethod(method) {
			return nil, fmt.Errorf("%w: %s", ErrPermanentlyForbiddenMethod, method)
		}
		if !productionV1MethodSet[method] {
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedProductionV1Method, method)
		}
		if seen[method] {
			return nil, fmt.Errorf("%w: duplicate %s", ErrInvalidMethodAllowlist, method)
		}
		seen[method] = true
	}
	sort.Strings(result)
	return result, nil
}

// ValidateCanonicalProductionV1MethodAllowlist proves that a runtime or
// binding supplied the exact canonical method sequence, not merely an
// equivalent set.
func ValidateCanonicalProductionV1MethodAllowlist(methods []string) error {
	canonical, err := CanonicalizeProductionV1MethodAllowlist(methods)
	if err != nil {
		return err
	}
	if !slices.Equal(methods, canonical) {
		return ErrNonCanonicalMethodAllowlist
	}
	return nil
}

type productionV1CapabilityHashPayload struct {
	SchemaVersion string   `json:"schemaVersion"`
	Methods       []string `json:"methods"`
}

// CanonicalProductionV1CapabilityHashInput returns the stable JSON bytes that
// the TemplateRelease profile and runtime binding commit to. Callers receive a
// new byte slice and cannot mutate package authority.
func CanonicalProductionV1CapabilityHashInput(methods []string) ([]byte, error) {
	canonical, err := CanonicalizeProductionV1MethodAllowlist(methods)
	if err != nil {
		return nil, err
	}
	value, err := domain.CanonicalJSON(productionV1CapabilityHashPayload{
		SchemaVersion: ProductionV1CapabilitySchemaVersion,
		Methods:       canonical,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: canonical hash input", ErrInvalidMethodAllowlist)
	}
	return value, nil
}

func ComputeProductionV1CapabilityHash(methods []string) (string, error) {
	value, err := CanonicalProductionV1CapabilityHashInput(methods)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ValidateProductionV1CapabilityCommitment verifies both the canonical wire
// sequence and its supplied immutable hash. An equivalent but unsorted list is
// rejected even when sorting it would produce the same hash.
func ValidateProductionV1CapabilityCommitment(methods []string, capabilityHash string) error {
	if err := ValidateCanonicalProductionV1MethodAllowlist(methods); err != nil {
		return err
	}
	if !digestPattern.MatchString(capabilityHash) {
		return ErrCapabilityHashMismatch
	}
	expected, err := ComputeProductionV1CapabilityHash(methods)
	if err != nil {
		return err
	}
	if expected != capabilityHash {
		return ErrCapabilityHashMismatch
	}
	return nil
}

// AdmitBrowserRequestMethod binds direction, platform support, and the exact
// canonical TemplateRelease allowlist in one fail-closed decision.
func AdmitBrowserRequestMethod(method string, canonicalAllowlist []string) error {
	if IsPermanentlyForbiddenMethod(method) {
		return fmt.Errorf("%w: %s", ErrPermanentlyForbiddenMethod, method)
	}
	if !productionV1MethodSet[method] {
		return fmt.Errorf("%w: %s", ErrUnsupportedProductionV1Method, method)
	}
	if !productionV1BrowserRequestMethodSet[method] {
		return fmt.Errorf("%w: no strict browser request decoder for %s", ErrBrowserRequestMethodNotAdmitted, method)
	}
	if err := ValidateCanonicalProductionV1MethodAllowlist(canonicalAllowlist); err != nil {
		return err
	}
	index, found := slices.BinarySearch(canonicalAllowlist, method)
	if !found || index < 0 {
		return fmt.Errorf("%w: profile excludes %s", ErrBrowserRequestMethodNotAdmitted, method)
	}
	return nil
}

type BrowserRequestPayload interface {
	isBrowserRequestPayload()
}

type BrowserTextDocumentIdentifier struct {
	URI string `json:"uri"`
}

type BrowserPosition struct {
	Line      uint64 `json:"line"`
	Character uint64 `json:"character"`
}

type TextDocumentPositionPayload struct {
	TextDocument BrowserTextDocumentIdentifier `json:"textDocument"`
	Position     BrowserPosition               `json:"position"`
}

func (TextDocumentPositionPayload) isBrowserRequestPayload() {}

type DocumentSymbolPayload struct {
	TextDocument BrowserTextDocumentIdentifier `json:"textDocument"`
}

func (DocumentSymbolPayload) isBrowserRequestPayload() {}

type ReferencesPayload struct {
	TextDocument BrowserTextDocumentIdentifier `json:"textDocument"`
	Position     BrowserPosition               `json:"position"`
	Context      ReferencesContext             `json:"context"`
}

type ReferencesContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

func (ReferencesPayload) isBrowserRequestPayload() {}

type CompletionTriggerKind uint8

const (
	CompletionInvoked                  CompletionTriggerKind = 1
	CompletionTriggerCharacter         CompletionTriggerKind = 2
	CompletionForIncompleteCompletions CompletionTriggerKind = 3
)

type CompletionContext struct {
	TriggerKind      CompletionTriggerKind `json:"triggerKind"`
	TriggerCharacter string                `json:"triggerCharacter,omitempty"`
}

type CompletionPayload struct {
	TextDocument BrowserTextDocumentIdentifier `json:"textDocument"`
	Position     BrowserPosition               `json:"position"`
	Context      CompletionContext             `json:"context"`
}

func (CompletionPayload) isBrowserRequestPayload() {}

type SignatureHelpTriggerKind uint8

const (
	SignatureHelpInvoked          SignatureHelpTriggerKind = 1
	SignatureHelpTriggerCharacter SignatureHelpTriggerKind = 2
	SignatureHelpContentChange    SignatureHelpTriggerKind = 3
)

type SignatureHelpContext struct {
	TriggerKind      SignatureHelpTriggerKind `json:"triggerKind"`
	TriggerCharacter string                   `json:"triggerCharacter,omitempty"`
	IsRetrigger      bool                     `json:"isRetrigger"`
}

type SignatureHelpPayload struct {
	TextDocument BrowserTextDocumentIdentifier `json:"textDocument"`
	Position     BrowserPosition               `json:"position"`
	Context      SignatureHelpContext          `json:"context"`
}

func (SignatureHelpPayload) isBrowserRequestPayload() {}

// DecodeBrowserRequestPayload admits the method against an exact profile
// allowlist and recursively decodes its browser DTO. All initial request
// methods are document-scoped, so the payload URI must equal a canonical
// DocumentFence that itself belongs to the exact SandboxHeadFence.
func DecodeBrowserRequestPayload(
	method string,
	canonicalAllowlist []string,
	payload []byte,
	head SandboxHeadFence,
	document DocumentFence,
) (BrowserRequestPayload, error) {
	if err := AdmitBrowserRequestMethod(method, canonicalAllowlist); err != nil {
		return nil, err
	}
	if len(payload) == 0 || len(payload) > maxBrowserRequestPayloadBytes {
		return nil, ErrInvalidBrowserRequestPayload
	}
	if err := validateBrowserJSONDepth(payload, 4); err != nil {
		return nil, invalidBrowserPayload(err)
	}
	if err := document.ValidateAgainstHead(head); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidBrowserRequestPayload, err)
	}

	switch method {
	case "textDocument/documentSymbol":
		return decodeDocumentSymbolPayload(payload, document)
	case "textDocument/references":
		return decodeReferencesPayload(payload, document)
	case "textDocument/completion":
		return decodeCompletionPayload(payload, document)
	case "textDocument/signatureHelp":
		return decodeSignatureHelpPayload(payload, document)
	case "textDocument/hover", "textDocument/declaration", "textDocument/definition",
		"textDocument/documentHighlight", "textDocument/implementation", "textDocument/typeDefinition":
		return decodeTextDocumentPositionPayload(payload, document)
	default:
		return nil, fmt.Errorf("%w: %s", ErrBrowserRequestMethodNotAdmitted, method)
	}
}

func decodeTextDocumentPositionPayload(value []byte, document DocumentFence) (TextDocumentPositionPayload, error) {
	fields, err := decodeMethodObject(value, []string{"textDocument", "position"}, nil)
	if err != nil {
		return TextDocumentPositionPayload{}, invalidBrowserPayload(err)
	}
	textDocument, err := decodeBrowserTextDocument(fields["textDocument"], document)
	if err != nil {
		return TextDocumentPositionPayload{}, err
	}
	position, err := decodeBrowserPosition(fields["position"])
	if err != nil {
		return TextDocumentPositionPayload{}, err
	}
	return TextDocumentPositionPayload{TextDocument: textDocument, Position: position}, nil
}

func decodeDocumentSymbolPayload(value []byte, document DocumentFence) (DocumentSymbolPayload, error) {
	fields, err := decodeMethodObject(value, []string{"textDocument"}, nil)
	if err != nil {
		return DocumentSymbolPayload{}, invalidBrowserPayload(err)
	}
	textDocument, err := decodeBrowserTextDocument(fields["textDocument"], document)
	if err != nil {
		return DocumentSymbolPayload{}, err
	}
	return DocumentSymbolPayload{TextDocument: textDocument}, nil
}

func decodeReferencesPayload(value []byte, document DocumentFence) (ReferencesPayload, error) {
	fields, err := decodeMethodObject(value, []string{"textDocument", "position", "context"}, nil)
	if err != nil {
		return ReferencesPayload{}, invalidBrowserPayload(err)
	}
	textDocument, err := decodeBrowserTextDocument(fields["textDocument"], document)
	if err != nil {
		return ReferencesPayload{}, err
	}
	position, err := decodeBrowserPosition(fields["position"])
	if err != nil {
		return ReferencesPayload{}, err
	}
	context, err := decodeMethodObject(fields["context"], []string{"includeDeclaration"}, nil)
	if err != nil {
		return ReferencesPayload{}, invalidBrowserPayload(err)
	}
	var includeDeclaration bool
	if err := decodeBool(context["includeDeclaration"], &includeDeclaration); err != nil {
		return ReferencesPayload{}, invalidBrowserPayload(err)
	}
	return ReferencesPayload{
		TextDocument: textDocument, Position: position,
		Context: ReferencesContext{IncludeDeclaration: includeDeclaration},
	}, nil
}

func decodeCompletionPayload(value []byte, document DocumentFence) (CompletionPayload, error) {
	fields, err := decodeMethodObject(value, []string{"textDocument", "position", "context"}, nil)
	if err != nil {
		return CompletionPayload{}, invalidBrowserPayload(err)
	}
	textDocument, err := decodeBrowserTextDocument(fields["textDocument"], document)
	if err != nil {
		return CompletionPayload{}, err
	}
	position, err := decodeBrowserPosition(fields["position"])
	if err != nil {
		return CompletionPayload{}, err
	}
	contextFields, err := decodeMethodObject(fields["context"], []string{"triggerKind"}, []string{"triggerCharacter"})
	if err != nil {
		return CompletionPayload{}, invalidBrowserPayload(err)
	}
	trigger, err := decodeTriggerKind(contextFields["triggerKind"])
	if err != nil || trigger < uint64(CompletionInvoked) || trigger > uint64(CompletionForIncompleteCompletions) {
		return CompletionPayload{}, invalidBrowserPayload(errors.New("invalid completion triggerKind"))
	}
	triggerCharacter, err := decodeConditionalTriggerCharacter(contextFields, trigger == uint64(CompletionTriggerCharacter))
	if err != nil {
		return CompletionPayload{}, err
	}
	return CompletionPayload{
		TextDocument: textDocument,
		Position:     position,
		Context: CompletionContext{
			TriggerKind: CompletionTriggerKind(trigger), TriggerCharacter: triggerCharacter,
		},
	}, nil
}

func decodeSignatureHelpPayload(value []byte, document DocumentFence) (SignatureHelpPayload, error) {
	fields, err := decodeMethodObject(value, []string{"textDocument", "position", "context"}, nil)
	if err != nil {
		return SignatureHelpPayload{}, invalidBrowserPayload(err)
	}
	textDocument, err := decodeBrowserTextDocument(fields["textDocument"], document)
	if err != nil {
		return SignatureHelpPayload{}, err
	}
	position, err := decodeBrowserPosition(fields["position"])
	if err != nil {
		return SignatureHelpPayload{}, err
	}
	contextFields, err := decodeMethodObject(
		fields["context"], []string{"triggerKind", "isRetrigger"}, []string{"triggerCharacter"},
	)
	if err != nil {
		return SignatureHelpPayload{}, invalidBrowserPayload(err)
	}
	trigger, err := decodeTriggerKind(contextFields["triggerKind"])
	if err != nil || trigger < uint64(SignatureHelpInvoked) || trigger > uint64(SignatureHelpContentChange) {
		return SignatureHelpPayload{}, invalidBrowserPayload(errors.New("invalid signature triggerKind"))
	}
	triggerCharacter, err := decodeConditionalTriggerCharacter(
		contextFields, trigger == uint64(SignatureHelpTriggerCharacter),
	)
	if err != nil {
		return SignatureHelpPayload{}, err
	}
	var isRetrigger bool
	if err := decodeBool(contextFields["isRetrigger"], &isRetrigger); err != nil {
		return SignatureHelpPayload{}, invalidBrowserPayload(err)
	}
	return SignatureHelpPayload{
		TextDocument: textDocument,
		Position:     position,
		Context: SignatureHelpContext{
			TriggerKind: SignatureHelpTriggerKind(trigger), TriggerCharacter: triggerCharacter,
			IsRetrigger: isRetrigger,
		},
	}, nil
}

func decodeBrowserTextDocument(value json.RawMessage, document DocumentFence) (BrowserTextDocumentIdentifier, error) {
	fields, err := decodeMethodObject(value, []string{"uri"}, nil)
	if err != nil {
		return BrowserTextDocumentIdentifier{}, invalidBrowserPayload(err)
	}
	var uri string
	if err := decodeString(fields["uri"], &uri); err != nil || len(uri) > 1_024 {
		return BrowserTextDocumentIdentifier{}, invalidBrowserPayload(errors.New("invalid text document URI"))
	}
	if _, err := ParseCandidateModelURI(uri); err != nil || uri != document.ModelURI {
		return BrowserTextDocumentIdentifier{}, invalidBrowserPayload(errors.New("text document URI does not match DocumentFence"))
	}
	return BrowserTextDocumentIdentifier{URI: uri}, nil
}

func decodeBrowserPosition(value json.RawMessage) (BrowserPosition, error) {
	fields, err := decodeMethodObject(value, []string{"line", "character"}, nil)
	if err != nil {
		return BrowserPosition{}, invalidBrowserPayload(err)
	}
	var line, character uint64
	if err := decodeMethodUint(fields["line"], &line); err != nil || line > maxLSPPositionValue {
		return BrowserPosition{}, invalidBrowserPayload(errors.New("invalid position line"))
	}
	if err := decodeMethodUint(fields["character"], &character); err != nil || character > maxLSPPositionValue {
		return BrowserPosition{}, invalidBrowserPayload(errors.New("invalid position character"))
	}
	return BrowserPosition{Line: line, Character: character}, nil
}

func decodeConditionalTriggerCharacter(fields map[string]json.RawMessage, required bool) (string, error) {
	value, exists := fields["triggerCharacter"]
	if exists != required {
		return "", invalidBrowserPayload(errors.New("triggerCharacter presence does not match triggerKind"))
	}
	if !exists {
		return "", nil
	}
	var character string
	if err := decodeString(value, &character); err != nil || character == "" ||
		len(character) > 64 || !utf8.ValidString(character) || utf8.RuneCountInString(character) > 16 ||
		strings.ContainsAny(character, "\r\n\x00") {
		return "", invalidBrowserPayload(errors.New("invalid triggerCharacter"))
	}
	return character, nil
}

func decodeTriggerKind(value json.RawMessage) (uint64, error) {
	var result uint64
	if err := decodeMethodUint(value, &result); err != nil {
		return 0, err
	}
	return result, nil
}

func decodeMethodUint(value json.RawMessage, target *uint64) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || target == nil {
		return errors.New("unsigned decimal integer is required")
	}
	for _, character := range trimmed {
		if character < '0' || character > '9' {
			return errors.New("unsigned decimal integer is required")
		}
	}
	return decodeUint(trimmed, target)
}

func decodeBool(value json.RawMessage, target *bool) error {
	if len(value) == 0 || target == nil || string(value) == "null" {
		return errors.New("boolean is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("invalid boolean")
	}
	return nil
}

func validateBrowserJSONDepth(value []byte, maximum int) error {
	if maximum < 1 {
		return errors.New("invalid JSON depth limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	depth := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			continue
		}
		switch delimiter {
		case '{', '[':
			depth++
			if depth > maximum {
				return errors.New("JSON nesting depth exceeds limit")
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return errors.New("invalid JSON nesting")
			}
		}
	}
	if depth != 0 {
		return errors.New("invalid JSON nesting")
	}
	return nil
}

func decodeMethodObject(value []byte, required, optional []string) (map[string]json.RawMessage, error) {
	if len(value) == 0 || string(value) == "null" {
		return nil, errors.New("object is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("object is required")
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = true
	}
	for _, name := range optional {
		allowed[name] = true
	}
	result := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok || !allowed[name] {
			return nil, fmt.Errorf("unknown field %q", name)
		}
		if _, duplicate := result[name]; duplicate {
			return nil, fmt.Errorf("duplicate field %q", name)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		result[name] = slices.Clone(raw)
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("unterminated object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple values are forbidden")
		}
		return nil, err
	}
	for _, name := range required {
		if _, exists := result[name]; !exists {
			return nil, fmt.Errorf("missing field %q", name)
		}
	}
	return result, nil
}

func invalidBrowserPayload(err error) error {
	return fmt.Errorf("%w: %v", ErrInvalidBrowserRequestPayload, err)
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
