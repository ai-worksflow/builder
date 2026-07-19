package lsp

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/worksflow/builder/backend/internal/templates"
)

func methodDocumentFixture(t *testing.T) (SandboxHeadFence, DocumentFence, string) {
	t.Helper()
	head := validHead()
	modelURI, err := CandidateModelURI(testProject, testCandidate, "apps/web/page.tsx")
	if err != nil {
		t.Fatal(err)
	}
	document := DocumentFence{
		ModelURI: modelURI, OpenID: testOpen, ModelVersion: 27, SavedContentHash: lspDigest("b"),
	}
	return head, document, modelURI
}

func TestProductionV1MethodBaselineIsCanonicalImmutableAndMatchesTemplateAdmission(t *testing.T) {
	baseline := ProductionV1MethodBaseline()
	if len(baseline) != 15 || !slices.IsSorted(baseline) {
		t.Fatalf("baseline is not the canonical documented set: %#v", baseline)
	}
	if !slices.Equal(baseline, templates.LanguageServerBaselineMethods()) {
		t.Fatalf("gateway/template method authorities drifted:\nLSP: %#v\ntemplates: %#v", baseline, templates.LanguageServerBaselineMethods())
	}
	baseline[0] = "workspace/applyEdit"
	if ProductionV1MethodBaseline()[0] == baseline[0] {
		t.Fatal("caller mutated production method authority")
	}

	requestMethods := ProductionV1BrowserRequestMethods()
	if len(requestMethods) != 10 || !slices.IsSorted(requestMethods) {
		t.Fatalf("browser request methods are not canonical: %#v", requestMethods)
	}
	requestMethods[0] = "textDocument/rename"
	if ProductionV1BrowserRequestMethods()[0] == requestMethods[0] {
		t.Fatal("caller mutated browser request method authority")
	}
}

func TestProductionV1CapabilityHashInputMatchesTemplateCommitment(t *testing.T) {
	methods := []string{"textDocument/references", "textDocument/hover"}
	input, err := CanonicalProductionV1CapabilityHashInput(methods)
	if err != nil {
		t.Fatal(err)
	}
	wantInput := `{"methods":["textDocument/hover","textDocument/references"],"schemaVersion":"language-server-capabilities/v1"}`
	if string(input) != wantInput {
		t.Fatalf("canonical input = %s, want %s", input, wantInput)
	}
	got, err := ComputeProductionV1CapabilityHash(methods)
	if err != nil {
		t.Fatal(err)
	}
	want, err := templates.ComputeLanguageServerCapabilityHash(methods)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || !digestPattern.MatchString(got) {
		t.Fatalf("capability hash = %s, template commitment = %s", got, want)
	}
	canonical := []string{"textDocument/hover", "textDocument/references"}
	if err := ValidateProductionV1CapabilityCommitment(canonical, got); err != nil {
		t.Fatalf("exact capability commitment rejected: %v", err)
	}
	if err := ValidateProductionV1CapabilityCommitment(methods, got); !errors.Is(err, ErrNonCanonicalMethodAllowlist) {
		t.Fatalf("unsorted capability identity = %v", err)
	}
	if err := ValidateProductionV1CapabilityCommitment(canonical, lspDigest("f")); !errors.Is(err, ErrCapabilityHashMismatch) {
		t.Fatalf("drifted capability commitment = %v", err)
	}
	input[0] = 'x'
	fresh, err := CanonicalProductionV1CapabilityHashInput(methods)
	if err != nil || fresh[0] != '{' {
		t.Fatal("caller mutated canonical hash input authority")
	}
}

func TestMethodAllowlistCanonicalizationFailsClosed(t *testing.T) {
	input := []string{"textDocument/references", "textDocument/hover"}
	canonical, err := CanonicalizeProductionV1MethodAllowlist(input)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(canonical, []string{"textDocument/hover", "textDocument/references"}) ||
		input[0] != "textDocument/references" {
		t.Fatalf("canonicalization mutated input or returned wrong order: input=%#v result=%#v", input, canonical)
	}
	if !errors.Is(ValidateCanonicalProductionV1MethodAllowlist(input), ErrNonCanonicalMethodAllowlist) {
		t.Fatal("unsorted allowlist was accepted as exact runtime identity")
	}
	if err := ValidateCanonicalProductionV1MethodAllowlist(canonical); err != nil {
		t.Fatalf("canonical allowlist rejected: %v", err)
	}

	for name, fixture := range map[string][]string{
		"nil":        nil,
		"empty":      {},
		"duplicate":  {"textDocument/hover", "textDocument/hover"},
		"padding":    {" textDocument/hover"},
		"unknown":    {"textDocument/notReal"},
		"forbidden":  {"workspace/applyEdit"},
		"case drift": {"textdocument/hover"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalizeProductionV1MethodAllowlist(fixture); err == nil {
				t.Fatalf("malformed allowlist accepted: %#v", fixture)
			}
		})
	}
}

func TestPermanentForbiddenMethodFamiliesCanNeverBeAdmitted(t *testing.T) {
	forbidden := append(PermanentlyForbiddenMethods(),
		"vendor/applyEdit", "vendor/executeCommand", "textDocument/Rename",
		"vendor/codeAction", "vendor/rangeFormatting", "vendor/registerCapability",
		"workspace/symbol", "workspace/didChangeWatchedFiles",
	)
	allowlist := []string{"textDocument/hover"}
	for _, method := range forbidden {
		t.Run(strings.ReplaceAll(method, "/", "_"), func(t *testing.T) {
			if !IsPermanentlyForbiddenMethod(method) {
				t.Fatalf("dangerous method family was not classified: %s", method)
			}
			if _, err := CanonicalizeProductionV1MethodAllowlist([]string{method}); !errors.Is(err, ErrPermanentlyForbiddenMethod) {
				t.Fatalf("dangerous profile method = %v", err)
			}
			if err := AdmitBrowserRequestMethod(method, allowlist); !errors.Is(err, ErrPermanentlyForbiddenMethod) {
				t.Fatalf("dangerous browser method = %v", err)
			}
		})
	}
	for _, safe := range ProductionV1MethodBaseline() {
		if IsPermanentlyForbiddenMethod(safe) {
			t.Fatalf("read-only baseline method classified as dangerous: %s", safe)
		}
	}
}

func TestBrowserRequestMethodAdmissionRequiresDirectionAndProfileSubset(t *testing.T) {
	allowlist := []string{
		"textDocument/completion", "textDocument/hover", "textDocument/publishDiagnostics",
	}
	if err := AdmitBrowserRequestMethod("textDocument/hover", allowlist); err != nil {
		t.Fatal(err)
	}
	if err := AdmitBrowserRequestMethod("textDocument/definition", allowlist); !errors.Is(err, ErrBrowserRequestMethodNotAdmitted) {
		t.Fatalf("profile-excluded request = %v", err)
	}
	if err := AdmitBrowserRequestMethod("textDocument/publishDiagnostics", allowlist); !errors.Is(err, ErrBrowserRequestMethodNotAdmitted) {
		t.Fatalf("notification admitted as browser request: %v", err)
	}
	if err := AdmitBrowserRequestMethod("textDocument/semanticTokens/full", ProductionV1MethodBaseline()); !errors.Is(err, ErrBrowserRequestMethodNotAdmitted) {
		t.Fatalf("method without strict request decoder was admitted: %v", err)
	}
	if err := AdmitBrowserRequestMethod("textDocument/notReal", allowlist); !errors.Is(err, ErrUnsupportedProductionV1Method) {
		t.Fatalf("unknown method = %v", err)
	}
	if err := AdmitBrowserRequestMethod("textDocument/hover", []string{
		"textDocument/hover", "textDocument/completion",
	}); !errors.Is(err, ErrNonCanonicalMethodAllowlist) {
		t.Fatalf("non-canonical profile identity = %v", err)
	}
}

func TestStrictBrowserRequestDecodersAcceptInitialReadOnlySlice(t *testing.T) {
	head, document, uri := methodDocumentFixture(t)
	position := fmt.Sprintf(`{"textDocument":{"uri":%q},"position":{"line":12,"character":7}}`, uri)
	fixtures := map[string]string{
		"textDocument/hover":             position,
		"textDocument/declaration":       position,
		"textDocument/definition":        position,
		"textDocument/documentHighlight": position,
		"textDocument/implementation":    position,
		"textDocument/typeDefinition":    position,
		"textDocument/documentSymbol": fmt.Sprintf(
			`{"textDocument":{"uri":%q}}`, uri,
		),
		"textDocument/references": fmt.Sprintf(
			`{"textDocument":{"uri":%q},"position":{"line":12,"character":7},"context":{"includeDeclaration":false}}`, uri,
		),
		"textDocument/completion": fmt.Sprintf(
			`{"textDocument":{"uri":%q},"position":{"line":12,"character":7},"context":{"triggerKind":2,"triggerCharacter":"."}}`, uri,
		),
		"textDocument/signatureHelp": fmt.Sprintf(
			`{"textDocument":{"uri":%q},"position":{"line":12,"character":7},"context":{"triggerKind":1,"isRetrigger":false}}`, uri,
		),
	}
	allowlist := ProductionV1MethodBaseline()
	if len(fixtures) != len(ProductionV1BrowserRequestMethods()) {
		t.Fatalf("decoder fixture drift: %d fixtures for %d methods", len(fixtures), len(ProductionV1BrowserRequestMethods()))
	}
	for method, raw := range fixtures {
		t.Run(method, func(t *testing.T) {
			decoded, err := DecodeBrowserRequestPayload(method, allowlist, []byte(raw), head, document)
			if err != nil {
				t.Fatalf("strict valid payload rejected: %v\n%s", err, raw)
			}
			if decoded == nil {
				t.Fatal("decoder returned nil payload")
			}
		})
	}

	completion, err := DecodeBrowserRequestPayload(
		"textDocument/completion", allowlist, []byte(fixtures["textDocument/completion"]), head, document,
	)
	if err != nil {
		t.Fatal(err)
	}
	gotCompletion, ok := completion.(CompletionPayload)
	if !ok || gotCompletion.Context.TriggerKind != CompletionTriggerCharacter ||
		gotCompletion.Context.TriggerCharacter != "." || gotCompletion.Position.Line != 12 {
		t.Fatalf("decoded completion = %#v", completion)
	}

	references, err := DecodeBrowserRequestPayload(
		"textDocument/references", allowlist, []byte(fixtures["textDocument/references"]), head, document,
	)
	if err != nil {
		t.Fatal(err)
	}
	gotReferences, ok := references.(ReferencesPayload)
	if !ok || gotReferences.Context.IncludeDeclaration || gotReferences.TextDocument.URI != uri {
		t.Fatalf("decoded references = %#v", references)
	}
}

func TestBrowserRequestPayloadRejectsRecursiveSchemaDrift(t *testing.T) {
	head, document, uri := methodDocumentFixture(t)
	otherURI, err := CandidateModelURI(testProject, testCandidate, "apps/web/other.tsx")
	if err != nil {
		t.Fatal(err)
	}
	positionPrefix := fmt.Sprintf(`{"textDocument":{"uri":%q},"position":`, uri)
	fixtures := []struct {
		name   string
		method string
		raw    string
	}{
		{"top null", "textDocument/hover", `null`},
		{"top array", "textDocument/hover", `[]`},
		{"top missing", "textDocument/hover", fmt.Sprintf(`{"textDocument":{"uri":%q}}`, uri)},
		{"top unknown", "textDocument/hover", positionPrefix + `{"line":1,"character":2},"command":"run"}`},
		{"top duplicate", "textDocument/hover", fmt.Sprintf(`{"textDocument":{"uri":%q},"textDocument":{"uri":%q},"position":{"line":1,"character":2}}`, uri, uri)},
		{"multiple values", "textDocument/hover", positionPrefix + `{"line":1,"character":2}} {}`},
		{"document null", "textDocument/hover", `{"textDocument":null,"position":{"line":1,"character":2}}`},
		{"document unknown", "textDocument/hover", fmt.Sprintf(`{"textDocument":{"uri":%q,"version":1},"position":{"line":1,"character":2}}`, uri)},
		{"document duplicate", "textDocument/hover", fmt.Sprintf(`{"textDocument":{"uri":%q,"uri":%q},"position":{"line":1,"character":2}}`, uri, uri)},
		{"document file URI", "textDocument/hover", `{"textDocument":{"uri":"file:///tmp/page.tsx"},"position":{"line":1,"character":2}}`},
		{"document other canonical URI", "textDocument/hover", fmt.Sprintf(`{"textDocument":{"uri":%q},"position":{"line":1,"character":2}}`, otherURI)},
		{"position null", "textDocument/hover", fmt.Sprintf(`{"textDocument":{"uri":%q},"position":null}`, uri)},
		{"position unknown", "textDocument/hover", positionPrefix + `{"line":1,"character":2,"offset":3}}`},
		{"position missing", "textDocument/hover", positionPrefix + `{"line":1}}`},
		{"position string", "textDocument/hover", positionPrefix + `{"line":"1","character":2}}`},
		{"position float", "textDocument/hover", positionPrefix + `{"line":1.0,"character":2}}`},
		{"position exponent", "textDocument/hover", positionPrefix + `{"line":1e0,"character":2}}`},
		{"position negative", "textDocument/hover", positionPrefix + `{"line":-1,"character":2}}`},
		{"position overflow", "textDocument/hover", positionPrefix + `{"line":2147483648,"character":2}}`},
		{"excessive nesting", "textDocument/hover", positionPrefix + `[[[[[0]]]]]}`},
		{"references context null", "textDocument/references", fmt.Sprintf(`{"textDocument":{"uri":%q},"position":{"line":1,"character":2},"context":null}`, uri)},
		{"references boolean null", "textDocument/references", fmt.Sprintf(`{"textDocument":{"uri":%q},"position":{"line":1,"character":2},"context":{"includeDeclaration":null}}`, uri)},
		{"references context extra", "textDocument/references", fmt.Sprintf(`{"textDocument":{"uri":%q},"position":{"line":1,"character":2},"context":{"includeDeclaration":true,"unknown":false}}`, uri)},
		{"completion context missing", "textDocument/completion", fmt.Sprintf(`{"textDocument":{"uri":%q},"position":{"line":1,"character":2}}`, uri)},
		{"completion trigger float", "textDocument/completion", fmt.Sprintf(`{"textDocument":{"uri":%q},"position":{"line":1,"character":2},"context":{"triggerKind":1.0}}`, uri)},
		{"completion trigger unknown", "textDocument/completion", fmt.Sprintf(`{"textDocument":{"uri":%q},"position":{"line":1,"character":2},"context":{"triggerKind":4}}`, uri)},
		{"completion trigger char missing", "textDocument/completion", fmt.Sprintf(`{"textDocument":{"uri":%q},"position":{"line":1,"character":2},"context":{"triggerKind":2}}`, uri)},
		{"completion unsolicited trigger char", "textDocument/completion", fmt.Sprintf(`{"textDocument":{"uri":%q},"position":{"line":1,"character":2},"context":{"triggerKind":1,"triggerCharacter":"."}}`, uri)},
		{"completion trigger char null", "textDocument/completion", fmt.Sprintf(`{"textDocument":{"uri":%q},"position":{"line":1,"character":2},"context":{"triggerKind":2,"triggerCharacter":null}}`, uri)},
		{"completion command injection", "textDocument/completion", fmt.Sprintf(`{"textDocument":{"uri":%q},"position":{"line":1,"character":2},"context":{"triggerKind":1},"command":{"title":"run"}}`, uri)},
		{"signature retrigger null", "textDocument/signatureHelp", fmt.Sprintf(`{"textDocument":{"uri":%q},"position":{"line":1,"character":2},"context":{"triggerKind":1,"isRetrigger":null}}`, uri)},
		{"signature active help", "textDocument/signatureHelp", fmt.Sprintf(`{"textDocument":{"uri":%q},"position":{"line":1,"character":2},"context":{"triggerKind":1,"isRetrigger":true,"activeSignatureHelp":{}}}`, uri)},
	}
	allowlist := ProductionV1MethodBaseline()
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			if _, err := DecodeBrowserRequestPayload(
				fixture.method, allowlist, []byte(fixture.raw), head, document,
			); !errors.Is(err, ErrInvalidBrowserRequestPayload) {
				t.Fatalf("schema drift accepted or wrong error: %v\n%s", err, fixture.raw)
			}
		})
	}

	oversized := []byte(`{"textDocument":{"uri":"` + strings.Repeat("a", maxBrowserRequestPayloadBytes) + `"}}`)
	if _, err := DecodeBrowserRequestPayload(
		"textDocument/documentSymbol", allowlist, oversized, head, document,
	); !errors.Is(err, ErrInvalidBrowserRequestPayload) {
		t.Fatalf("oversized payload = %v", err)
	}
}

func TestBrowserRequestPayloadRequiresExactDocumentAndHeadFences(t *testing.T) {
	head, document, uri := methodDocumentFixture(t)
	raw := []byte(fmt.Sprintf(
		`{"textDocument":{"uri":%q},"position":{"line":1,"character":2}}`, uri,
	))
	allowlist := ProductionV1MethodBaseline()

	staleHead := head
	staleHead.CandidateID = "10000000-0000-4000-8000-000000000099"
	if _, err := DecodeBrowserRequestPayload(
		"textDocument/hover", allowlist, raw, staleHead, document,
	); !errors.Is(err, ErrInvalidBrowserRequestPayload) {
		t.Fatalf("cross-Candidate head = %v", err)
	}

	for name, mutate := range map[string]func(*DocumentFence){
		"open":    func(value *DocumentFence) { value.OpenID = "" },
		"version": func(value *DocumentFence) { value.ModelVersion = 0 },
		"hash":    func(value *DocumentFence) { value.SavedContentHash = "" },
		"uri":     func(value *DocumentFence) { value.ModelURI = "file:///tmp/page.tsx" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := document
			mutate(&invalid)
			if _, err := DecodeBrowserRequestPayload(
				"textDocument/hover", allowlist, raw, head, invalid,
			); !errors.Is(err, ErrInvalidBrowserRequestPayload) {
				t.Fatalf("invalid DocumentFence accepted: %v", err)
			}
		})
	}
}
