package lsp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const (
	testProject   = "10000000-0000-4000-8000-000000000001"
	testSession   = "10000000-0000-4000-8000-000000000002"
	testCandidate = "10000000-0000-4000-8000-000000000003"
	testOpen      = "10000000-0000-4000-8000-000000000004"
)

func lspDigest(character string) string { return "sha256:" + strings.Repeat(character, 64) }

func validHead() SandboxHeadFence {
	return SandboxHeadFence{
		ProjectID: testProject, SessionID: testSession, SessionEpoch: 3,
		CandidateID: testCandidate, Version: 18, JournalSequence: 42,
		WriterLeaseEpoch: 7, TreeHash: lspDigest("a"),
	}
}

func TestCandidateModelURIIsCanonicalAndCandidateScoped(t *testing.T) {
	value, err := CandidateModelURI(testProject, testCandidate, "apps/web/页面 one.tsx")
	if err != nil {
		t.Fatal(err)
	}
	want := "worksflow-candidate://" + testProject + "/" + testCandidate + "/apps/web/%E9%A1%B5%E9%9D%A2%20one.tsx"
	if value != want {
		t.Fatalf("model URI = %q, want %q", value, want)
	}
	identity, err := ParseCandidateModelURI(value)
	if err != nil || identity.ProjectID != testProject || identity.CandidateID != testCandidate ||
		identity.Path != "apps/web/页面 one.tsx" {
		t.Fatalf("identity = %#v, %v", identity, err)
	}

	for _, invalid := range []string{
		"file:///apps/web/page.tsx",
		"untitled://" + testProject + "/" + testCandidate + "/page.tsx",
		"worksflow-candidate://" + testProject + "/" + testCandidate + "/../secret.ts",
		"worksflow-candidate://" + testProject + "/" + testCandidate + "/%2E%2E/secret.ts",
		"worksflow-candidate://" + testProject + "/" + testCandidate + "/apps%2Fsecret.ts",
		"worksflow-candidate://" + testProject + "/" + testCandidate + "/apps/%e9%a1%b5.tsx",
		"worksflow-candidate://" + testProject + "/" + testCandidate + "/apps/page.tsx?head=1",
		"worksflow-candidate://" + testProject + "/" + testCandidate + "/apps/page.tsx#fragment",
		"worksflow-candidate://ABCDEFAB-0000-4000-8000-000000000001/" + testCandidate + "/apps/page.tsx",
	} {
		if _, err := ParseCandidateModelURI(invalid); !errors.Is(err, ErrInvalidModelURI) {
			t.Fatalf("invalid URI accepted: %q (%v)", invalid, err)
		}
	}
}

func TestCandidateWorkspaceURISupportsExactCandidateAndServiceRoots(t *testing.T) {
	root, err := CandidateWorkspaceURI(testProject, testCandidate, ".")
	if err != nil || root != "worksflow-candidate://"+testProject+"/"+testCandidate {
		t.Fatalf("Candidate root URI = %q, %v", root, err)
	}
	service, err := CandidateWorkspaceURI(testProject, testCandidate, "apps/web")
	if err != nil {
		t.Fatal(err)
	}
	want, err := CandidateModelURI(testProject, testCandidate, "apps/web")
	if err != nil || service != want {
		t.Fatalf("service root URI = %q, want %q, error %v", service, want, err)
	}
	for _, unsafe := range []string{"", "./", "apps/../web", "/workspace"} {
		if _, err := CandidateWorkspaceURI(testProject, testCandidate, unsafe); !errors.Is(err, ErrInvalidModelURI) {
			t.Fatalf("unsafe root %q error = %v", unsafe, err)
		}
	}
}

func TestStrictSandboxHeadFenceDecoderRejectsSchemaDrift(t *testing.T) {
	head := validHead()
	encoded, err := json.Marshal(head)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := DecodeSandboxHeadFence(encoded)
	if err != nil || !parsed.Equal(head) {
		t.Fatalf("head decode = %#v, %v", parsed, err)
	}

	valid := string(encoded)
	for _, invalid := range []string{
		strings.Replace(valid, `"version":18,`, ``, 1),
		strings.Replace(valid, `"version":18`, `"version":null`, 1),
		strings.Replace(valid, `"version":18`, `"candidateVersion":18`, 1),
		strings.Replace(valid, `"version":18`, `"Version":18`, 1),
		strings.Replace(valid, `"version":18`, `"version":1.8e1`, 1),
		strings.Replace(valid, `"version":18`, `"version":9007199254740992`, 1),
		strings.Replace(valid, `"version":18`, `"version":18,"version":19`, 1),
		strings.Replace(valid, `"version":18`, `"version":18,"unknown":true`, 1),
	} {
		if _, err := DecodeSandboxHeadFence([]byte(invalid)); !errors.Is(err, ErrInvalidSandboxHead) {
			t.Fatalf("schema drift accepted: %s (%v)", invalid, err)
		}
	}
}

func TestDocumentFenceAndMonotonicHeadRebind(t *testing.T) {
	head := validHead()
	modelURI, err := CandidateModelURI(testProject, testCandidate, "apps/web/page.tsx")
	if err != nil {
		t.Fatal(err)
	}
	document := DocumentFence{
		ModelURI: modelURI, OpenID: testOpen, ModelVersion: 27,
		SavedContentHash: lspDigest("b"),
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := DecodeDocumentFence(encoded)
	if err != nil || !parsed.Equal(document) || parsed.ValidateAgainstHead(head) != nil {
		t.Fatalf("document decode/validation = %#v, %v", parsed, err)
	}
	otherHead := head
	otherHead.CandidateID = "10000000-0000-4000-8000-000000000099"
	if !errors.Is(document.ValidateAgainstHead(otherHead), ErrInvalidDocument) {
		t.Fatal("cross-Candidate document fence was accepted")
	}

	next := head
	next.Version += 2
	next.JournalSequence += 2
	next.TreeHash = lspDigest("c")
	if err := next.MonotonicSuccessorOf(head); err != nil {
		t.Fatalf("exact structural successor rejected: %v", err)
	}
	for _, mutate := range []func(*SandboxHeadFence){
		func(value *SandboxHeadFence) { value.SessionEpoch++ },
		func(value *SandboxHeadFence) { value.WriterLeaseEpoch++ },
		func(value *SandboxHeadFence) { value.Version = head.Version },
		func(value *SandboxHeadFence) { value.JournalSequence++ },
		func(value *SandboxHeadFence) { value.TreeHash = head.TreeHash },
	} {
		candidate := next
		mutate(&candidate)
		if !errors.Is(candidate.MonotonicSuccessorOf(head), ErrHeadRebind) {
			t.Fatalf("invalid rebind accepted: %#v", candidate)
		}
	}
}
