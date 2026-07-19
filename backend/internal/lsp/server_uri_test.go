package lsp

import (
	"errors"
	"reflect"
	"testing"
)

func TestServerURIMapsOnlyExactCandidateIntoFixedContainerWorkspace(t *testing.T) {
	head := validHead()
	modelURI, err := CandidateModelURI(head.ProjectID, head.CandidateID, "apps/web/页面 one.tsx")
	if err != nil {
		t.Fatal(err)
	}
	serverURI, err := ServerDocumentURI(modelURI, head)
	if err != nil || serverURI != "file:///workspace/apps/web/%E9%A1%B5%E9%9D%A2%20one.tsx" {
		t.Fatalf("server URI = %q, err = %v", serverURI, err)
	}
	roundTrip, err := CandidateDocumentURI(serverURI, head)
	if err != nil || roundTrip != modelURI {
		t.Fatalf("Candidate round trip = %q, err = %v", roundTrip, err)
	}
	serviceURI, err := ServerWorkspaceURI("apps/web")
	if err != nil || serviceURI != "file:///workspace/apps/web" {
		t.Fatalf("service URI = %q, err = %v", serviceURI, err)
	}
	rootURI, err := ServerWorkspaceURI(".")
	if err != nil || rootURI != "file:///workspace" {
		t.Fatalf("root URI = %q, err = %v", rootURI, err)
	}
}

func TestServerURIRejectsAliasesEscapeAndForeignAuthority(t *testing.T) {
	head := validHead()
	other := head
	other.CandidateID = "20000000-0000-4000-8000-000000000099"
	foreign, err := CandidateModelURI(other.ProjectID, other.CandidateID, "apps/web/page.ts")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ServerDocumentURI(foreign, head); !errors.Is(err, ErrServerURIInvalid) {
		t.Fatalf("foreign Candidate error = %v", err)
	}
	for _, invalid := range []string{
		"file:///workspace",
		"file://localhost/workspace/apps/web/page.ts",
		"file:/workspace/apps/web/page.ts",
		"file:///workspace/apps//web/page.ts",
		"file:///workspace/apps/../secret.ts",
		"file:///workspace/apps%2Fweb/page.ts",
		"file:///workspace/apps/web/page%2ets",
		"file:///workspace/.git/config",
		"file:///etc/passwd",
		"file:///workspace/apps/web/page.ts?secret=1",
		"file:///workspace/apps/web/page.ts#fragment",
	} {
		if _, err := CandidateDocumentURI(invalid, head); !errors.Is(err, ErrServerURIInvalid) {
			t.Fatalf("CandidateDocumentURI(%q) error = %v", invalid, err)
		}
	}
	for _, invalid := range []string{"", "./apps/web", "/workspace/apps/web", "apps/../web"} {
		if _, err := ServerWorkspaceURI(invalid); !errors.Is(err, ErrServerURIInvalid) {
			t.Fatalf("ServerWorkspaceURI(%q) error = %v", invalid, err)
		}
	}
}

func TestServerRequestPayloadClonesTypedDTOAndReplacesOnlyURI(t *testing.T) {
	head := validHead()
	modelURI, err := CandidateModelURI(head.ProjectID, head.CandidateID, "apps/web/page.ts")
	if err != nil {
		t.Fatal(err)
	}
	document := DocumentFence{
		ModelURI: modelURI, OpenID: "40000000-0000-4000-8000-000000000001",
		ModelVersion: 2, SavedContentHash: lspDigest("b"),
	}
	original := CompletionPayload{
		TextDocument: BrowserTextDocumentIdentifier{URI: modelURI},
		Position:     BrowserPosition{Line: 3, Character: 4},
		Context:      CompletionContext{TriggerKind: CompletionInvoked},
	}
	translated, err := serverRequestPayload(original, document, head)
	if err != nil {
		t.Fatal(err)
	}
	want := original
	want.TextDocument.URI = "file:///workspace/apps/web/page.ts"
	if !reflect.DeepEqual(translated, want) || original.TextDocument.URI != modelURI {
		t.Fatalf("translated = %#v, original = %#v", translated, original)
	}
	drifted := original
	drifted.TextDocument.URI = "worksflow-candidate://drift"
	if _, err := serverRequestPayload(drifted, document, head); !errors.Is(err, ErrServerURIInvalid) {
		t.Fatalf("drifted request error = %v", err)
	}
}
