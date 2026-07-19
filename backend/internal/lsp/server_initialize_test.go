package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/worksflow/builder/backend/internal/templates"
)

func initializeTestProfile(methods []string) ProfileIdentity {
	identity := lspTestProfile("typescript")
	identity.Methods = slices.Clone(methods)
	identity.CapabilityHash, _ = templates.ComputeLanguageServerCapabilityHash(identity.Methods)
	identity.ContentHash, _ = templates.ComputeLanguageServerProfileContentHash(identity.LanguageServerProfile)
	identity.EffectiveLimits = identity.Limits
	return identity
}

func initializeTestResponse(capabilities, name, version string) []byte {
	return []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"result":{"capabilities":%s,"serverInfo":{"name":%q,"version":%q}}}`,
		capabilities, name, version,
	))
}

func TestBuildServerInitializeRequestBindsExactImmutableAuthority(t *testing.T) {
	profile := initializeTestProfile([]string{"textDocument/hover"})
	input := ServerInitializeInput{
		Head: validHead(), Profile: profile, WorkspaceRootPath: "apps/web",
	}
	first, err := BuildServerInitializeRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildServerInitializeRequest(input)
	if err != nil || !slices.Equal(first, second) {
		t.Fatalf("initialize request is not deterministic: err=%v", err)
	}
	if err := validateStrictJSONDocument(first, serverInitializeMaximumDepth); err != nil {
		t.Fatalf("generated request is not strict JSON: %v", err)
	}
	var request serverInitializeRequest
	if err := json.Unmarshal(first, &request); err != nil {
		t.Fatal(err)
	}
	wantURI, err := ServerWorkspaceURI(input.WorkspaceRootPath)
	if err != nil {
		t.Fatal(err)
	}
	if request.JSONRPC != "2.0" || request.ID != ServerInitializeRequestID ||
		request.Method != "initialize" || request.Params.ProcessID != nil ||
		request.Params.ClientInfo != (serverInitializeClientInfo{Name: serverInitializeClientName, Version: serverInitializeClientVersion}) ||
		request.Params.Locale != serverInitializeLocale || request.Params.Trace != "off" ||
		request.Params.RootURI != wantURI || len(request.Params.WorkspaceFolders) != 1 ||
		request.Params.WorkspaceFolders[0] != (serverInitializeWorkspaceFolder{URI: wantURI, Name: profile.ServiceID}) {
		t.Fatalf("unexpected fixed initialize parameters: %#v", request.Params)
	}
	options := request.Params.InitializationOptions
	if options.SchemaVersion != InitializationOptionsSchema ||
		options.InitializationParametersHash != profile.InitializationParametersHash ||
		options.WorkspaceConfigurationHash != profile.WorkspaceConfigurationHash ||
		!options.Head.Equal(input.Head) || options.TemplateRelease != profile.TemplateRelease ||
		!reflect.DeepEqual(options.Profile, profile) {
		t.Fatalf("initialize options lost exact authority: %#v", options)
	}
	capabilities := request.Params.Capabilities
	if capabilities.Workspace.ApplyEdit || capabilities.Workspace.Configuration ||
		capabilities.Workspace.WorkspaceFolders || capabilities.Window.WorkDoneProgress ||
		capabilities.TextDocument.Synchronization.DynamicRegistration ||
		capabilities.TextDocument.Synchronization.WillSave ||
		capabilities.TextDocument.Synchronization.WillSaveWaitUntil ||
		capabilities.TextDocument.Synchronization.DidSave ||
		capabilities.TextDocument.Completion.DynamicRegistration ||
		capabilities.TextDocument.Completion.CompletionItem.SnippetSupport ||
		!reflect.DeepEqual(capabilities.General.PositionEncodings, []string{"utf-16"}) {
		t.Fatalf("client widened read-only capabilities: %#v", capabilities)
	}
	if !strings.Contains(string(first), `"rootUri":"file:///workspace/apps/web"`) ||
		strings.Contains(string(first), "worksflow-candidate://") ||
		strings.Contains(string(first), "workspace/applyEdit") || strings.Contains(string(first), "executeCommand") {
		t.Fatalf("initialize request contains Candidate/host/write authority: %s", first)
	}
}

func TestBuildServerInitializeRequestSupportsTemplateServiceAtCandidateRoot(t *testing.T) {
	profile := initializeTestProfile([]string{"textDocument/hover"})
	value, err := BuildServerInitializeRequest(ServerInitializeInput{
		Head: validHead(), Profile: profile, WorkspaceRootPath: ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	var request serverInitializeRequest
	if err := json.Unmarshal(value, &request); err != nil {
		t.Fatal(err)
	}
	want := "file:///workspace"
	if request.Params.RootURI != want || len(request.Params.WorkspaceFolders) != 1 ||
		request.Params.WorkspaceFolders[0].URI != want {
		t.Fatalf("root service initialize URI = %#v", request.Params)
	}
}

func TestBuildServerInitializeRequestRejectsInvalidAuthority(t *testing.T) {
	valid := ServerInitializeInput{
		Head: validHead(), Profile: initializeTestProfile([]string{"textDocument/hover"}),
		WorkspaceRootPath: "apps/web",
	}
	tests := []struct {
		name   string
		mutate func(*ServerInitializeInput)
	}{
		{"noncanonical workspace", func(input *ServerInitializeInput) { input.WorkspaceRootPath = "apps/../web" }},
		{"host workspace", func(input *ServerInitializeInput) { input.WorkspaceRootPath = "/tmp/workspace" }},
		{"invalid head", func(input *ServerInitializeInput) { input.Head.Version = 0 }},
		{"profile hash drift", func(input *ServerInitializeInput) { input.Profile.ContentHash = lspDigest("f") }},
		{"capability hash drift", func(input *ServerInitializeInput) { input.Profile.CapabilityHash = lspDigest("f") }},
		{"invalid release", func(input *ServerInitializeInput) { input.Profile.TemplateRelease.ContentHash = "latest" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if _, err := BuildServerInitializeRequest(input); !errors.Is(err, ErrInitializeRequestInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDecodeServerInitializeResponseAcceptsExactCapabilitySubset(t *testing.T) {
	profile := initializeTestProfile([]string{"textDocument/definition", "textDocument/hover"})
	response := initializeTestResponse(
		`{"positionEncoding":"utf-16","textDocumentSync":{"openClose":true,"change":2,"willSave":false,"willSaveWaitUntil":false,"save":false},"hoverProvider":{"workDoneProgress":false},"definitionProvider":false}`,
		profile.ServerInfo.Name,
		profile.ServerInfo.Version,
	)
	result, err := DecodeServerInitializeResponse(response, profile)
	if err != nil {
		t.Fatal(err)
	}
	wantMethods := []string{"textDocument/hover"}
	wantHash, err := ComputeProductionV1CapabilityHash(wantMethods)
	if err != nil {
		t.Fatal(err)
	}
	if result.ServerInfo != profile.ServerInfo || !reflect.DeepEqual(result.Methods, wantMethods) ||
		result.CapabilityHash != wantHash || result.CapabilityHash == profile.CapabilityHash {
		t.Fatalf("actual subset was not independently committed: %#v", result)
	}
	if err := ValidateProductionV1CapabilityCommitment(result.Methods, result.CapabilityHash); err != nil {
		t.Fatalf("actual capability commitment is inconsistent: %v", err)
	}
}

func TestDecodeServerInitializeResponseMapsCompleteReadonlySurface(t *testing.T) {
	profile := initializeTestProfile(ProductionV1MethodBaseline())
	capabilities := `{
		"positionEncoding":"utf-16",
		"textDocumentSync":2,
		"completionProvider":{"triggerCharacters":["."],"allCommitCharacters":[";"],"resolveProvider":false,"workDoneProgress":false,"completionItem":{"labelDetailsSupport":true}},
		"declarationProvider":true,
		"definitionProvider":{"workDoneProgress":false},
		"diagnosticProvider":{"identifier":"document","interFileDependencies":true,"workspaceDiagnostics":false,"workDoneProgress":false},
		"documentHighlightProvider":true,
		"documentSymbolProvider":{"workDoneProgress":false,"label":"symbols"},
		"hoverProvider":true,
		"implementationProvider":true,
		"inlayHintProvider":{"resolveProvider":false,"workDoneProgress":false},
		"referencesProvider":true,
		"semanticTokensProvider":{"legend":{"tokenTypes":["class"],"tokenModifiers":[]},"range":true,"full":{"delta":false},"workDoneProgress":false},
		"signatureHelpProvider":{"triggerCharacters":["("],"retriggerCharacters":[","],"workDoneProgress":false},
		"typeDefinitionProvider":true
	}`
	response := initializeTestResponse(capabilities, profile.ServerInfo.Name, profile.ServerInfo.Version)
	result, err := DecodeServerInitializeResponse(response, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Methods, ProductionV1MethodBaseline()) ||
		result.CapabilityHash != profile.CapabilityHash {
		t.Fatalf("read-only capability mapping drift: %#v", result)
	}
}

func TestDecodeServerInitializeResponseRejectsIdentityDrift(t *testing.T) {
	profile := initializeTestProfile([]string{"textDocument/hover"})
	for _, test := range []struct {
		name    string
		server  string
		version string
	}{
		{"name", "other-server", profile.ServerInfo.Version},
		{"version", profile.ServerInfo.Name, "4.3.4"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := initializeTestResponse(`{"hoverProvider":true}`, test.server, test.version)
			if _, err := DecodeServerInitializeResponse(response, profile); !errors.Is(err, ErrServerIdentityMismatch) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDecodeServerInitializeResponseRejectsForbiddenAndWidenedCapabilities(t *testing.T) {
	profile := initializeTestProfile([]string{"textDocument/hover"})
	tests := []struct {
		name         string
		capabilities string
	}{
		{"workspace edits", `{"hoverProvider":true,"workspace":{"workspaceFolders":{"changeNotifications":true}}}`},
		{"execute command", `{"hoverProvider":true,"executeCommandProvider":{"commands":["build"]}}`},
		{"rename", `{"hoverProvider":true,"renameProvider":true}`},
		{"formatting", `{"hoverProvider":true,"documentFormattingProvider":true}`},
		{"code action", `{"hoverProvider":true,"codeActionProvider":true}`},
		{"workspace symbols", `{"hoverProvider":true,"workspaceSymbolProvider":true}`},
		{"dynamic registration", `{"hoverProvider":true,"experimental":{"dynamicRegistration":true}}`},
		{"unapproved readonly method", `{"hoverProvider":true,"definitionProvider":true}`},
		{"position encoding drift", `{"hoverProvider":true,"positionEncoding":"utf-8"}`},
		{"completion schema widening", `{"hoverProvider":true,"completionProvider":true}`},
		{"completion resolve", `{"hoverProvider":true,"completionProvider":{"resolveProvider":true}}`},
		{"signature schema widening", `{"hoverProvider":true,"signatureHelpProvider":true}`},
		{"inlay resolve", `{"hoverProvider":true,"inlayHintProvider":{"resolveProvider":true}}`},
		{"semantic delta", `{"hoverProvider":true,"semanticTokensProvider":{"legend":{"tokenTypes":["class"],"tokenModifiers":[]},"full":{"delta":true}}}`},
		{"workspace diagnostics", `{"hoverProvider":true,"diagnosticProvider":{"interFileDependencies":false,"workspaceDiagnostics":true}}`},
		{"will save wait", `{"hoverProvider":true,"textDocumentSync":{"willSaveWaitUntil":true}}`},
		{"save object", `{"hoverProvider":true,"textDocumentSync":{"save":{"includeText":true}}}`},
		{"provider null", `{"hoverProvider":null}`},
		{"no admitted capability", `{"hoverProvider":false}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := initializeTestResponse(test.capabilities, profile.ServerInfo.Name, profile.ServerInfo.Version)
			if _, err := DecodeServerInitializeResponse(response, profile); !errors.Is(err, ErrServerCapabilityViolation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDecodeServerInitializeResponseStrictMalformedBoundary(t *testing.T) {
	profile := initializeTestProfile([]string{"textDocument/hover"})
	validResult := fmt.Sprintf(
		`{"capabilities":{"hoverProvider":true},"serverInfo":{"name":%q,"version":%q}}`,
		profile.ServerInfo.Name, profile.ServerInfo.Version,
	)
	tests := []struct {
		name  string
		value string
	}{
		{"empty", ``},
		{"top null", `null`},
		{"duplicate top", `{"jsonrpc":"2.0","jsonrpc":"2.0","id":1,"result":` + validResult + `}`},
		{"unknown top", `{"jsonrpc":"2.0","id":1,"result":` + validResult + `,"error":{}}`},
		{"missing result", `{"jsonrpc":"2.0","id":1}`},
		{"null result", `{"jsonrpc":"2.0","id":1,"result":null}`},
		{"float id", `{"jsonrpc":"2.0","id":1.0,"result":` + validResult + `}`},
		{"wrong id", `{"jsonrpc":"2.0","id":2,"result":` + validResult + `}`},
		{"unknown result", `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"hoverProvider":true},"serverInfo":{"name":"typescript-language-server","version":"4.3.3"},"extra":false}}`},
		{"duplicate nested", `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"hoverProvider":{"workDoneProgress":false,"workDoneProgress":false}},"serverInfo":{"name":"typescript-language-server","version":"4.3.3"}}}`},
		{"float boolean", `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"hoverProvider":{"workDoneProgress":1.0}},"serverInfo":{"name":"typescript-language-server","version":"4.3.3"}}}`},
		{"deep nesting", `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"hoverProvider":{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{"i":{"j":{"k":{"l":{"m":true}}}}}}}}}}}}}},"serverInfo":{"name":"typescript-language-server","version":"4.3.3"}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeServerInitializeResponse([]byte(test.value), profile); err == nil ||
				(!errors.Is(err, ErrInitializeResponseInvalid) && !errors.Is(err, ErrServerCapabilityViolation)) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	profile.Limits.MaxFrameBytes = 128
	profile.ContentHash, _ = templates.ComputeLanguageServerProfileContentHash(profile.LanguageServerProfile)
	profile.EffectiveLimits = profile.Limits
	oversized := initializeTestResponse(`{"hoverProvider":true}`, profile.ServerInfo.Name, profile.ServerInfo.Version)
	if int64(len(oversized)) <= profile.EffectiveLimits.MaxFrameBytes {
		t.Fatal("test response did not exceed the effective frame cap")
	}
	if _, err := DecodeServerInitializeResponse(oversized, profile); !errors.Is(err, ErrInitializeResponseInvalid) {
		t.Fatalf("effective frame limit error = %v", err)
	}

	canonicalProfile := initializeTestProfile([]string{"textDocument/hover"})
	large := append(initializeTestResponse(`{"hoverProvider":true}`, canonicalProfile.ServerInfo.Name, canonicalProfile.ServerInfo.Version),
		make([]byte, canonicalProfile.EffectiveLimits.MaxFrameBytes)...)
	if _, err := DecodeServerInitializeResponse(large, canonicalProfile); !errors.Is(err, ErrInitializeResponseInvalid) {
		t.Fatalf("oversized response error = %v", err)
	}
}
