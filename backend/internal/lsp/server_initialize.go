package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/templates"
)

const (
	ServerInitializeRequestID        = uint64(1)
	InitializationOptionsSchema      = "worksflow-lsp-initialization-options/v1"
	serverInitializeMaximumBytes     = 512 << 10
	serverInitializeMaximumDepth     = 14
	serverInitializeClientName       = "worksflow-lsp-gateway"
	serverInitializeClientVersion    = "1"
	serverInitializeLocale           = "en-US"
	serverInitializePositionEncoding = "utf-16"
)

var (
	ErrInitializeRequestInvalid  = errors.New("invalid LSP initialize request authority")
	ErrInitializeResponseInvalid = errors.New("invalid LSP initialize response")
	ErrServerIdentityMismatch    = errors.New("LSP server identity does not match the approved profile")
	ErrServerCapabilityViolation = errors.New("LSP server capability violates the approved read-only profile")
)

// ServerInitializeInput contains only authority already admitted by the
// ticket/binding layer. WorkspaceRootPath is a canonical repository path; the
// server receives only its fixed container-local /workspace file URI, never a
// host path or the browser's Candidate capability URI.
type ServerInitializeInput struct {
	Head              SandboxHeadFence
	Profile           ProfileIdentity
	WorkspaceRootPath string
}

type serverInitializeRequest struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      uint64                 `json:"id"`
	Method  string                 `json:"method"`
	Params  serverInitializeParams `json:"params"`
}

type serverInitializeParams struct {
	ProcessID             *int                               `json:"processId"`
	ClientInfo            serverInitializeClientInfo         `json:"clientInfo"`
	Locale                string                             `json:"locale"`
	RootURI               string                             `json:"rootUri"`
	Capabilities          serverInitializeClientCapabilities `json:"capabilities"`
	InitializationOptions serverInitializationOptions        `json:"initializationOptions"`
	WorkspaceFolders      []serverInitializeWorkspaceFolder  `json:"workspaceFolders"`
	Trace                 string                             `json:"trace"`
}

type serverInitializeClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type serverInitializeWorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

type serverInitializationOptions struct {
	SchemaVersion                string               `json:"schemaVersion"`
	InitializationParametersHash string               `json:"initializationParametersHash"`
	WorkspaceConfigurationHash   string               `json:"workspaceConfigurationHash"`
	Head                         SandboxHeadFence     `json:"sandboxHeadFence"`
	TemplateRelease              ExactTemplateRelease `json:"templateRelease"`
	Profile                      ProfileIdentity      `json:"languageServerProfile"`
}

// The client advertises a fixed, deliberately narrow read-only surface. In
// particular it never advertises dynamic registration, workspace edits,
// configuration callbacks, command execution, snippets, or resolve methods.
type serverInitializeClientCapabilities struct {
	Workspace    serverInitializeWorkspaceCapabilities    `json:"workspace"`
	TextDocument serverInitializeTextDocumentCapabilities `json:"textDocument"`
	Window       serverInitializeWindowCapabilities       `json:"window"`
	General      serverInitializeGeneralCapabilities      `json:"general"`
}

type serverInitializeWorkspaceCapabilities struct {
	ApplyEdit        bool `json:"applyEdit"`
	WorkspaceFolders bool `json:"workspaceFolders"`
	Configuration    bool `json:"configuration"`
}

type serverInitializeDynamicCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration"`
}

type serverInitializeSynchronizationCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration"`
	WillSave            bool `json:"willSave"`
	WillSaveWaitUntil   bool `json:"willSaveWaitUntil"`
	DidSave             bool `json:"didSave"`
}

type serverInitializeCompletionItemCapability struct {
	SnippetSupport       bool `json:"snippetSupport"`
	CommitCharacters     bool `json:"commitCharactersSupport"`
	DeprecatedSupport    bool `json:"deprecatedSupport"`
	PreselectSupport     bool `json:"preselectSupport"`
	InsertReplaceSupport bool `json:"insertReplaceSupport"`
}

type serverInitializeCompletionCapability struct {
	DynamicRegistration bool                                     `json:"dynamicRegistration"`
	CompletionItem      serverInitializeCompletionItemCapability `json:"completionItem"`
}

type serverInitializeHoverCapability struct {
	DynamicRegistration bool     `json:"dynamicRegistration"`
	ContentFormat       []string `json:"contentFormat"`
}

type serverInitializeSemanticRequests struct {
	Range bool `json:"range"`
	Full  bool `json:"full"`
}

type serverInitializeSemanticCapability struct {
	DynamicRegistration     bool                             `json:"dynamicRegistration"`
	Requests                serverInitializeSemanticRequests `json:"requests"`
	TokenTypes              []string                         `json:"tokenTypes"`
	TokenModifiers          []string                         `json:"tokenModifiers"`
	Formats                 []string                         `json:"formats"`
	OverlappingTokenSupport bool                             `json:"overlappingTokenSupport"`
	MultilineTokenSupport   bool                             `json:"multilineTokenSupport"`
	ServerCancelSupport     bool                             `json:"serverCancelSupport"`
	AugmentsSyntaxTokens    bool                             `json:"augmentsSyntaxTokens"`
}

type serverInitializeTextDocumentCapabilities struct {
	Synchronization    serverInitializeSynchronizationCapability    `json:"synchronization"`
	Completion         serverInitializeCompletionCapability         `json:"completion"`
	Hover              serverInitializeHoverCapability              `json:"hover"`
	SignatureHelp      serverInitializeDynamicCapability            `json:"signatureHelp"`
	Declaration        serverInitializeDynamicCapability            `json:"declaration"`
	Definition         serverInitializeDynamicCapability            `json:"definition"`
	TypeDefinition     serverInitializeDynamicCapability            `json:"typeDefinition"`
	Implementation     serverInitializeDynamicCapability            `json:"implementation"`
	References         serverInitializeDynamicCapability            `json:"references"`
	DocumentHighlight  serverInitializeDynamicCapability            `json:"documentHighlight"`
	DocumentSymbol     serverInitializeDynamicCapability            `json:"documentSymbol"`
	PublishDiagnostics serverInitializePublishDiagnosticsCapability `json:"publishDiagnostics"`
	SemanticTokens     serverInitializeSemanticCapability           `json:"semanticTokens"`
	InlayHint          serverInitializeDynamicCapability            `json:"inlayHint"`
	Diagnostic         serverInitializeDiagnosticCapability         `json:"diagnostic"`
}

type serverInitializePublishDiagnosticsCapability struct {
	RelatedInformation bool `json:"relatedInformation"`
	VersionSupport     bool `json:"versionSupport"`
	CodeDescription    bool `json:"codeDescriptionSupport"`
	DataSupport        bool `json:"dataSupport"`
}

type serverInitializeDiagnosticCapability struct {
	DynamicRegistration    bool `json:"dynamicRegistration"`
	RelatedDocumentSupport bool `json:"relatedDocumentSupport"`
}

type serverInitializeWindowCapabilities struct {
	WorkDoneProgress bool `json:"workDoneProgress"`
}

type serverInitializeGeneralCapabilities struct {
	PositionEncodings []string `json:"positionEncodings"`
}

// InitializedServer is the exact, sanitized initialize identity. Methods are
// canonical and CapabilityHash is recomputed from those actual declarations;
// neither value is copied from an untrusted server field.
type InitializedServer struct {
	ServerInfo     templates.LanguageServerInfo `json:"serverInfo"`
	Methods        []string                     `json:"methods"`
	CapabilityHash string                       `json:"capabilityHash"`
}

// BuildServerInitializeRequest emits one deterministic JSON-RPC initialize
// request. The fixed request ID is scoped to a newly started stdio process and
// must be answered before any other request is sent.
func BuildServerInitializeRequest(input ServerInitializeInput) ([]byte, error) {
	if input.Head.Validate() != nil || input.Profile.Validate() != nil ||
		input.Profile.TemplateRelease.Validate() != nil ||
		ValidateProductionV1CapabilityCommitment(
			input.Profile.Methods, input.Profile.CapabilityHash,
		) != nil {
		return nil, ErrInitializeRequestInvalid
	}
	workspaceURI, err := ServerWorkspaceURI(input.WorkspaceRootPath)
	if err != nil {
		return nil, fmt.Errorf("%w: workspace root", ErrInitializeRequestInvalid)
	}
	request := serverInitializeRequest{
		JSONRPC: "2.0",
		ID:      ServerInitializeRequestID,
		Method:  "initialize",
		Params: serverInitializeParams{
			ProcessID: nil,
			ClientInfo: serverInitializeClientInfo{
				Name: serverInitializeClientName, Version: serverInitializeClientVersion,
			},
			Locale:       serverInitializeLocale,
			RootURI:      workspaceURI,
			Capabilities: fixedReadonlyClientCapabilities(),
			InitializationOptions: serverInitializationOptions{
				SchemaVersion:                InitializationOptionsSchema,
				InitializationParametersHash: input.Profile.InitializationParametersHash,
				WorkspaceConfigurationHash:   input.Profile.WorkspaceConfigurationHash,
				Head:                         input.Head,
				TemplateRelease:              input.Profile.TemplateRelease,
				Profile:                      input.Profile,
			},
			WorkspaceFolders: []serverInitializeWorkspaceFolder{{
				URI: workspaceURI, Name: input.Profile.ServiceID,
			}},
			Trace: "off",
		},
	}
	value, err := domain.CanonicalJSON(request)
	if err != nil || len(value) == 0 || len(value) > serverInitializeMaximumBytes {
		return nil, ErrInitializeRequestInvalid
	}
	return value, nil
}

func fixedReadonlyClientCapabilities() serverInitializeClientCapabilities {
	dynamic := serverInitializeDynamicCapability{DynamicRegistration: false}
	return serverInitializeClientCapabilities{
		Workspace: serverInitializeWorkspaceCapabilities{
			ApplyEdit: false, WorkspaceFolders: false, Configuration: false,
		},
		TextDocument: serverInitializeTextDocumentCapabilities{
			Synchronization: serverInitializeSynchronizationCapability{},
			Completion: serverInitializeCompletionCapability{
				DynamicRegistration: false,
				CompletionItem:      serverInitializeCompletionItemCapability{},
			},
			Hover: serverInitializeHoverCapability{
				DynamicRegistration: false, ContentFormat: []string{"plaintext"},
			},
			SignatureHelp: dynamic, Declaration: dynamic, Definition: dynamic,
			TypeDefinition: dynamic, Implementation: dynamic, References: dynamic,
			DocumentHighlight: dynamic, DocumentSymbol: dynamic,
			PublishDiagnostics: serverInitializePublishDiagnosticsCapability{
				VersionSupport: true,
			},
			SemanticTokens: serverInitializeSemanticCapability{
				DynamicRegistration: false,
				Requests:            serverInitializeSemanticRequests{Range: true, Full: true},
				TokenTypes: []string{
					"namespace", "type", "class", "enum", "interface", "struct",
					"typeParameter", "parameter", "variable", "property", "enumMember",
					"event", "function", "method", "macro", "keyword", "modifier",
					"comment", "string", "number", "regexp", "operator", "decorator",
				},
				TokenModifiers: []string{
					"declaration", "definition", "readonly", "static", "deprecated",
					"abstract", "async", "modification", "documentation", "defaultLibrary",
				},
				Formats: []string{"relative"},
			},
			InlayHint: dynamic,
			Diagnostic: serverInitializeDiagnosticCapability{
				DynamicRegistration: false, RelatedDocumentSupport: false,
			},
		},
		Window:  serverInitializeWindowCapabilities{WorkDoneProgress: false},
		General: serverInitializeGeneralCapabilities{PositionEncodings: []string{serverInitializePositionEncoding}},
	}
}

// DecodeServerInitializeResponse applies the production-v1 capability
// boundary to the first response from a newly started server. Unknown fields
// are not forwarded or ignored: an image upgrade that changes its advertised
// surface must be admitted through a new exact TemplateRelease.
func DecodeServerInitializeResponse(value []byte, profile ProfileIdentity) (InitializedServer, error) {
	if profile.Validate() != nil || ValidateProductionV1CapabilityCommitment(
		profile.Methods, profile.CapabilityHash,
	) != nil {
		return InitializedServer{}, ErrInitializeResponseInvalid
	}
	maximumBytes := int64(serverInitializeMaximumBytes)
	if profile.EffectiveLimits.MaxFrameBytes < maximumBytes {
		maximumBytes = profile.EffectiveLimits.MaxFrameBytes
	}
	if int64(len(value)) == 0 || int64(len(value)) > maximumBytes ||
		validateStrictJSONDocument(value, serverInitializeMaximumDepth) != nil {
		return InitializedServer{}, ErrInitializeResponseInvalid
	}
	fields, err := decodeStrictInitializeObject(
		value,
		[]string{"jsonrpc", "id", "result"},
		[]string{"jsonrpc", "id", "result"},
	)
	if err != nil {
		return InitializedServer{}, fmt.Errorf("%w: %v", ErrInitializeResponseInvalid, err)
	}
	var jsonRPC string
	var id uint64
	if decodeString(fields["jsonrpc"], &jsonRPC) != nil || jsonRPC != "2.0" ||
		decodeUint(fields["id"], &id) != nil || id != ServerInitializeRequestID {
		return InitializedServer{}, ErrInitializeResponseInvalid
	}
	return decodeServerInitializeResult(fields["result"], profile)
}

func decodeServerInitializeResult(value []byte, profile ProfileIdentity) (InitializedServer, error) {
	fields, err := decodeStrictInitializeObject(
		value,
		[]string{"capabilities", "serverInfo"},
		[]string{"capabilities", "serverInfo"},
	)
	if err != nil {
		return InitializedServer{}, fmt.Errorf("%w: result: %v", ErrInitializeResponseInvalid, err)
	}
	serverInfo, err := decodeExactServerInfo(fields["serverInfo"])
	if err != nil {
		return InitializedServer{}, err
	}
	if serverInfo != profile.ServerInfo {
		return InitializedServer{}, ErrServerIdentityMismatch
	}
	methods, err := decodeReadonlyServerCapabilities(fields["capabilities"], profile)
	if err != nil {
		return InitializedServer{}, err
	}
	capabilityHash, err := ComputeProductionV1CapabilityHash(methods)
	if err != nil || ValidateProductionV1CapabilityCommitment(methods, capabilityHash) != nil {
		return InitializedServer{}, ErrServerCapabilityViolation
	}
	return InitializedServer{
		ServerInfo: serverInfo, Methods: slices.Clone(methods), CapabilityHash: capabilityHash,
	}, nil
}

func decodeExactServerInfo(value []byte) (templates.LanguageServerInfo, error) {
	fields, err := decodeStrictInitializeObject(
		value, []string{"name", "version"}, []string{"name", "version"},
	)
	if err != nil {
		return templates.LanguageServerInfo{}, fmt.Errorf("%w: serverInfo", ErrInitializeResponseInvalid)
	}
	var result templates.LanguageServerInfo
	if decodeString(fields["name"], &result.Name) != nil ||
		decodeString(fields["version"], &result.Version) != nil ||
		!boundedInitializeString(result.Name, 160) || !boundedInitializeString(result.Version, 120) {
		return templates.LanguageServerInfo{}, fmt.Errorf("%w: serverInfo", ErrInitializeResponseInvalid)
	}
	return result, nil
}

func decodeReadonlyServerCapabilities(value []byte, profile ProfileIdentity) ([]string, error) {
	allowed := []string{
		"completionProvider", "declarationProvider", "definitionProvider",
		"diagnosticProvider", "documentHighlightProvider", "documentSymbolProvider",
		"hoverProvider", "implementationProvider", "inlayHintProvider",
		"positionEncoding", "referencesProvider", "semanticTokensProvider",
		"signatureHelpProvider", "textDocumentSync", "typeDefinitionProvider",
	}
	fields, err := decodeStrictInitializeObject(value, allowed, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerCapabilityViolation, err)
	}

	if raw, exists := fields["positionEncoding"]; exists {
		var encoding string
		if decodeString(raw, &encoding) != nil || encoding != serverInitializePositionEncoding {
			return nil, fmt.Errorf("%w: positionEncoding", ErrServerCapabilityViolation)
		}
	}

	syncEnabled := false
	if raw, exists := fields["textDocumentSync"]; exists {
		syncEnabled, err = decodeTextDocumentSync(raw)
		if err != nil {
			return nil, err
		}
	}

	methods := make([]string, 0, len(fields))
	simple := []struct {
		field  string
		method string
		decode func([]byte) (bool, error)
	}{
		{"hoverProvider", "textDocument/hover", decodeBoolOrWorkDoneProvider},
		{"declarationProvider", "textDocument/declaration", decodeBoolOrWorkDoneProvider},
		{"definitionProvider", "textDocument/definition", decodeBoolOrWorkDoneProvider},
		{"typeDefinitionProvider", "textDocument/typeDefinition", decodeBoolOrWorkDoneProvider},
		{"implementationProvider", "textDocument/implementation", decodeBoolOrWorkDoneProvider},
		{"referencesProvider", "textDocument/references", decodeBoolOrWorkDoneProvider},
		{"documentHighlightProvider", "textDocument/documentHighlight", decodeBoolOrWorkDoneProvider},
		{"documentSymbolProvider", "textDocument/documentSymbol", decodeDocumentSymbolProvider},
		{"inlayHintProvider", "textDocument/inlayHint", decodeInlayHintProvider},
	}
	for _, capability := range simple {
		raw, exists := fields[capability.field]
		if !exists {
			continue
		}
		enabled, decodeErr := capability.decode(raw)
		if decodeErr != nil {
			return nil, fmt.Errorf("%w: %s", ErrServerCapabilityViolation, capability.field)
		}
		if enabled {
			methods = append(methods, capability.method)
		}
	}
	if raw, exists := fields["completionProvider"]; exists {
		enabled, decodeErr := decodeCompletionProvider(raw)
		if decodeErr != nil {
			return nil, fmt.Errorf("%w: completionProvider", ErrServerCapabilityViolation)
		}
		if enabled {
			methods = append(methods, "textDocument/completion")
		}
	}
	if raw, exists := fields["signatureHelpProvider"]; exists {
		enabled, decodeErr := decodeSignatureHelpProvider(raw)
		if decodeErr != nil {
			return nil, fmt.Errorf("%w: signatureHelpProvider", ErrServerCapabilityViolation)
		}
		if enabled {
			methods = append(methods, "textDocument/signatureHelp")
		}
	}
	if raw, exists := fields["diagnosticProvider"]; exists {
		enabled, decodeErr := decodeDiagnosticProvider(raw)
		if decodeErr != nil {
			return nil, fmt.Errorf("%w: diagnosticProvider", ErrServerCapabilityViolation)
		}
		if enabled {
			methods = append(methods, "textDocument/diagnostic")
		}
	}
	if raw, exists := fields["semanticTokensProvider"]; exists {
		semanticMethods, decodeErr := decodeSemanticTokensProvider(raw)
		if decodeErr != nil {
			return nil, fmt.Errorf("%w: semanticTokensProvider", ErrServerCapabilityViolation)
		}
		methods = append(methods, semanticMethods...)
	}
	if syncEnabled && slices.Contains(profile.Methods, "textDocument/publishDiagnostics") {
		// publishDiagnostics has no initialize-result provider field in LSP 3.17;
		// controlled document synchronization is its positive runtime signal.
		methods = append(methods, "textDocument/publishDiagnostics")
	}

	sort.Strings(methods)
	if len(methods) == 0 {
		return nil, ErrServerCapabilityViolation
	}
	for index, method := range methods {
		if index > 0 && methods[index-1] == method {
			return nil, ErrServerCapabilityViolation
		}
		if _, found := slices.BinarySearch(profile.Methods, method); !found {
			return nil, fmt.Errorf("%w: unapproved %s", ErrServerCapabilityViolation, method)
		}
	}
	return methods, nil
}

func decodeTextDocumentSync(value []byte) (bool, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false, ErrServerCapabilityViolation
	}
	if trimmed[0] != '{' {
		var kind uint64
		if decodeUint(trimmed, &kind) != nil || kind > 2 {
			return false, fmt.Errorf("%w: textDocumentSync", ErrServerCapabilityViolation)
		}
		return kind > 0, nil
	}
	fields, err := decodeStrictInitializeObject(
		trimmed,
		[]string{"openClose", "change", "willSave", "willSaveWaitUntil", "save"},
		nil,
	)
	if err != nil {
		return false, fmt.Errorf("%w: textDocumentSync", ErrServerCapabilityViolation)
	}
	openClose, change := false, uint64(0)
	if raw, exists := fields["openClose"]; exists && decodeJSONBool(raw, &openClose) != nil {
		return false, fmt.Errorf("%w: textDocumentSync.openClose", ErrServerCapabilityViolation)
	}
	if raw, exists := fields["change"]; exists {
		if decodeUint(raw, &change) != nil || change > 2 {
			return false, fmt.Errorf("%w: textDocumentSync.change", ErrServerCapabilityViolation)
		}
	}
	for _, field := range []string{"willSave", "willSaveWaitUntil"} {
		if raw, exists := fields[field]; exists {
			var enabled bool
			if decodeJSONBool(raw, &enabled) != nil || enabled {
				return false, fmt.Errorf("%w: textDocumentSync.%s", ErrServerCapabilityViolation, field)
			}
		}
	}
	if raw, exists := fields["save"]; exists {
		var enabled bool
		if decodeJSONBool(raw, &enabled) != nil || enabled {
			// SaveOptions/includeText and true both widen the server's sync
			// surface. Production v1 rebinds only authoritative Candidate CAS.
			return false, fmt.Errorf("%w: textDocumentSync.save", ErrServerCapabilityViolation)
		}
	}
	return openClose || change > 0, nil
}

func decodeBoolOrWorkDoneProvider(value []byte) (bool, error) {
	if enabled, matched := decodeOptionalProviderBool(value); matched {
		return enabled, nil
	}
	fields, err := decodeStrictInitializeObject(value, []string{"workDoneProgress"}, nil)
	if err != nil {
		return false, err
	}
	if raw, exists := fields["workDoneProgress"]; exists {
		var enabled bool
		if decodeJSONBool(raw, &enabled) != nil || enabled {
			return false, ErrServerCapabilityViolation
		}
	}
	return true, nil
}

func decodeDocumentSymbolProvider(value []byte) (bool, error) {
	if enabled, matched := decodeOptionalProviderBool(value); matched {
		return enabled, nil
	}
	fields, err := decodeStrictInitializeObject(value, []string{"workDoneProgress", "label"}, nil)
	if err != nil {
		return false, err
	}
	if err := requireDisabledBoolean(fields, "workDoneProgress"); err != nil {
		return false, err
	}
	if raw, exists := fields["label"]; exists {
		var label string
		if decodeString(raw, &label) != nil || !boundedInitializeString(label, 120) {
			return false, ErrServerCapabilityViolation
		}
	}
	return true, nil
}

func decodeInlayHintProvider(value []byte) (bool, error) {
	if enabled, matched := decodeOptionalProviderBool(value); matched {
		return enabled, nil
	}
	fields, err := decodeStrictInitializeObject(
		value, []string{"workDoneProgress", "resolveProvider"}, nil,
	)
	if err != nil || requireDisabledBoolean(fields, "workDoneProgress") != nil ||
		requireDisabledBoolean(fields, "resolveProvider") != nil {
		return false, ErrServerCapabilityViolation
	}
	return true, nil
}

func decodeCompletionProvider(value []byte) (bool, error) {
	fields, err := decodeStrictInitializeObject(
		value,
		[]string{"triggerCharacters", "allCommitCharacters", "resolveProvider", "workDoneProgress", "completionItem"},
		nil,
	)
	if err != nil || requireDisabledBoolean(fields, "resolveProvider") != nil ||
		requireDisabledBoolean(fields, "workDoneProgress") != nil {
		return false, ErrServerCapabilityViolation
	}
	for _, field := range []string{"triggerCharacters", "allCommitCharacters"} {
		if raw, exists := fields[field]; exists {
			if _, err := decodeBoundedStringArray(raw, 64, 16, true); err != nil {
				return false, err
			}
		}
	}
	if raw, exists := fields["completionItem"]; exists {
		itemFields, itemErr := decodeStrictInitializeObject(raw, []string{"labelDetailsSupport"}, nil)
		if itemErr != nil {
			return false, itemErr
		}
		if enabled, present := itemFields["labelDetailsSupport"]; present {
			var supported bool
			if decodeJSONBool(enabled, &supported) != nil {
				return false, ErrServerCapabilityViolation
			}
		}
	}
	return true, nil
}

func decodeSignatureHelpProvider(value []byte) (bool, error) {
	fields, err := decodeStrictInitializeObject(
		value, []string{"triggerCharacters", "retriggerCharacters", "workDoneProgress"}, nil,
	)
	if err != nil || requireDisabledBoolean(fields, "workDoneProgress") != nil {
		return false, ErrServerCapabilityViolation
	}
	for _, field := range []string{"triggerCharacters", "retriggerCharacters"} {
		if raw, exists := fields[field]; exists {
			if _, err := decodeBoundedStringArray(raw, 64, 16, true); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

func decodeDiagnosticProvider(value []byte) (bool, error) {
	fields, err := decodeStrictInitializeObject(
		value,
		[]string{"identifier", "interFileDependencies", "workspaceDiagnostics", "workDoneProgress"},
		[]string{"interFileDependencies", "workspaceDiagnostics"},
	)
	if err != nil || requireDisabledBoolean(fields, "workspaceDiagnostics") != nil ||
		requireDisabledBoolean(fields, "workDoneProgress") != nil {
		return false, ErrServerCapabilityViolation
	}
	var dependencies bool
	if decodeJSONBool(fields["interFileDependencies"], &dependencies) != nil {
		return false, ErrServerCapabilityViolation
	}
	if raw, exists := fields["identifier"]; exists {
		var identifier string
		if decodeString(raw, &identifier) != nil || !boundedInitializeString(identifier, 120) {
			return false, ErrServerCapabilityViolation
		}
	}
	return true, nil
}

func decodeSemanticTokensProvider(value []byte) ([]string, error) {
	fields, err := decodeStrictInitializeObject(
		value,
		[]string{"legend", "range", "full", "workDoneProgress"},
		[]string{"legend"},
	)
	if err != nil || requireDisabledBoolean(fields, "workDoneProgress") != nil {
		return nil, ErrServerCapabilityViolation
	}
	legend, err := decodeStrictInitializeObject(
		fields["legend"], []string{"tokenTypes", "tokenModifiers"}, []string{"tokenTypes", "tokenModifiers"},
	)
	if err != nil {
		return nil, ErrServerCapabilityViolation
	}
	if values, decodeErr := decodeBoundedStringArray(legend["tokenTypes"], 128, 120, true); decodeErr != nil || len(values) == 0 {
		return nil, ErrServerCapabilityViolation
	}
	if _, decodeErr := decodeBoundedStringArray(legend["tokenModifiers"], 128, 120, true); decodeErr != nil {
		return nil, ErrServerCapabilityViolation
	}
	methods := make([]string, 0, 2)
	if raw, exists := fields["range"]; exists {
		enabled, decodeErr := decodeBoolOrWorkDoneProvider(raw)
		if decodeErr != nil {
			return nil, ErrServerCapabilityViolation
		}
		if enabled {
			methods = append(methods, "textDocument/semanticTokens/range")
		}
	}
	if raw, exists := fields["full"]; exists {
		if enabled, matched := decodeOptionalProviderBool(raw); matched {
			if enabled {
				methods = append(methods, "textDocument/semanticTokens/full")
			}
		} else {
			full, decodeErr := decodeStrictInitializeObject(raw, []string{"delta"}, nil)
			if decodeErr != nil || requireDisabledBoolean(full, "delta") != nil {
				return nil, ErrServerCapabilityViolation
			}
			methods = append(methods, "textDocument/semanticTokens/full")
		}
	}
	if len(methods) == 0 {
		return nil, ErrServerCapabilityViolation
	}
	return methods, nil
}

func decodeOptionalProviderBool(value []byte) (bool, bool) {
	trimmed := bytes.TrimSpace(value)
	if bytes.Equal(trimmed, []byte("true")) {
		return true, true
	}
	if bytes.Equal(trimmed, []byte("false")) {
		return false, true
	}
	return false, false
}

func requireDisabledBoolean(fields map[string]json.RawMessage, field string) error {
	raw, exists := fields[field]
	if !exists {
		return nil
	}
	var enabled bool
	if decodeJSONBool(raw, &enabled) != nil || enabled {
		return ErrServerCapabilityViolation
	}
	return nil
}

func decodeJSONBool(value []byte, target *bool) error {
	trimmed := bytes.TrimSpace(value)
	if target == nil || (!bytes.Equal(trimmed, []byte("true")) && !bytes.Equal(trimmed, []byte("false"))) {
		return ErrInitializeResponseInvalid
	}
	*target = bytes.Equal(trimmed, []byte("true"))
	return nil
}

func decodeBoundedStringArray(
	value []byte,
	maximumItems int,
	maximumStringBytes int,
	requireNonEmptyStrings bool,
) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	var result []string
	if err := decoder.Decode(&result); err != nil || result == nil || len(result) > maximumItems {
		return nil, ErrServerCapabilityViolation
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrServerCapabilityViolation
	}
	seen := make(map[string]bool, len(result))
	for _, item := range result {
		if !boundedInitializeString(item, maximumStringBytes) ||
			(requireNonEmptyStrings && item == "") || seen[item] {
			return nil, ErrServerCapabilityViolation
		}
		seen[item] = true
	}
	return result, nil
}

func boundedInitializeString(value string, maximumBytes int) bool {
	if !utf8.ValidString(value) || len(value) > maximumBytes || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func decodeStrictInitializeObject(
	value []byte,
	allowed []string,
	required []string,
) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("object is required")
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = true
	}
	result := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		name, ok := nameToken.(string)
		if tokenErr != nil || !ok || !allowedSet[name] {
			return nil, errors.New("unknown or invalid object field")
		}
		if _, duplicate := result[name]; duplicate {
			return nil, errors.New("duplicate object field")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, errors.New("null or malformed object field")
		}
		result[name] = append(json.RawMessage(nil), raw...)
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, errors.New("unterminated object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple JSON values are forbidden")
	}
	for _, field := range required {
		if _, exists := result[field]; !exists {
			return nil, errors.New("required object field is missing")
		}
	}
	return result, nil
}
