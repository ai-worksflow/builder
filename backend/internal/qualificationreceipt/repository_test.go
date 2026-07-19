package qualificationreceipt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceContentTreeDigestKnownVectorAndRawPathSorting(t *testing.T) {
	first := []byte("first\n")
	second := []byte("second\n")
	entries := []SourceContentTreeEntry{
		{Path: "z-last.txt", Mode: "100644", SizeBytes: int64(len(second)), SHA256: sha256Digest(second)},
		{Path: "frontend/app/[projectId]/page.tsx", Mode: "100755", SizeBytes: int64(len(first)), SHA256: sha256Digest(first)},
	}
	digest, err := ComputeSourceContentTreeDigest(entries)
	if err != nil {
		t.Fatal(err)
	}
	const expected = "sha256:89d5ad8180f746bf13b572be96a1fe7722ac0af930aaa2cd27dbcf8d4ba3dd28"
	if digest != expected {
		t.Fatalf("source content-tree vector changed: got %s", digest)
	}
	reversed := []SourceContentTreeEntry{entries[1], entries[0]}
	reversedDigest, err := ComputeSourceContentTreeDigest(reversed)
	if err != nil || reversedDigest != digest {
		t.Fatalf("raw path ordering was not canonical: digest=%s err=%v", reversedDigest, err)
	}
}

func TestSourceContentTreeDigestDoesNotTrustGitSHA1ObjectIdentity(t *testing.T) {
	leftBytes := []byte("left-content")
	rightBytes := []byte("right-data!!")
	if len(leftBytes) != len(rightBytes) {
		t.Fatal("test inputs must have equal size")
	}
	base := SourceContentTreeEntry{
		Path: "source.txt", Mode: "100644", SizeBytes: int64(len(leftBytes)), SHA256: sha256Digest(leftBytes),
	}
	left, err := ComputeSourceContentTreeDigest([]SourceContentTreeEntry{base})
	if err != nil {
		t.Fatal(err)
	}
	base.SHA256 = sha256Digest(rightBytes)
	right, err := ComputeSourceContentTreeDigest([]SourceContentTreeEntry{base})
	if err != nil {
		t.Fatal(err)
	}
	if left == right {
		t.Fatal("different actual-byte SHA-256 values produced the same content-tree commitment")
	}
	// A Git SHA-1 object ID is intentionally absent from SourceContentTreeEntry,
	// so even a hypothetical pair sharing one cannot suppress the difference.
}

func TestSourceContentTreeDigestCommitsPathModeSizeAndActualBytes(t *testing.T) {
	base := SourceContentTreeEntry{Path: "a.txt", Mode: "100644", SizeBytes: 1, SHA256: sha256Digest([]byte("a"))}
	want, err := ComputeSourceContentTreeDigest([]SourceContentTreeEntry{base})
	if err != nil {
		t.Fatal(err)
	}
	mutations := []SourceContentTreeEntry{base, base, base, base}
	mutations[0].Path = "b.txt"
	mutations[1].Mode = "100755"
	mutations[2].SizeBytes = 2
	mutations[3].SHA256 = sha256Digest([]byte("b"))
	for index, mutation := range mutations {
		got, err := ComputeSourceContentTreeDigest([]SourceContentTreeEntry{mutation})
		if err != nil {
			t.Fatalf("mutation %d: %v", index, err)
		}
		if got == want {
			t.Fatalf("mutation %d did not change the content-tree commitment", index)
		}
	}
}

func TestHashStableGitBlobReturnsHistoricalObjectAndActualByteSHA256(t *testing.T) {
	content := []byte("hello\n")
	path := filepath.Join(t.TempDir(), "executable.sh")
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
	objectID, contentDigest, executable, size, err := hashStableGitBlob(path, "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if objectID != "ce013625030ba8dba906f756967f9e9ca394464a" || contentDigest != sha256Digest(content) ||
		!executable || size != int64(len(content)) {
		t.Fatalf("unexpected dual Git/content digest: object=%s content=%s executable=%t size=%d", objectID, contentDigest, executable, size)
	}
}

func TestSourceContentTreeDigestRejectsDuplicateAndNonSHA256Entries(t *testing.T) {
	entry := SourceContentTreeEntry{Path: "a.txt", Mode: "100644", SizeBytes: 1, SHA256: sha256Digest([]byte("a"))}
	if _, err := ComputeSourceContentTreeDigest([]SourceContentTreeEntry{entry, entry}); err == nil {
		t.Fatal("duplicate source paths were accepted")
	}
	entry.SHA256 = "sha1:" + strings.Repeat("0", 40)
	if _, err := ComputeSourceContentTreeDigest([]SourceContentTreeEntry{entry}); err == nil {
		t.Fatal("a non-SHA-256 content digest was accepted")
	}
}

func TestTrustedGitExecutableIsDigestBound(t *testing.T) {
	encoded, err := os.ReadFile(trustedGitBinary)
	if err != nil {
		t.Skipf("trusted Git executable is unavailable: %v", err)
	}
	digest := sha256Digest(encoded)
	if err := validateTrustedGitBinary(digest); err != nil {
		t.Fatalf("validate pinned Git executable: %v", err)
	}
	if err := validateTrustedGitBinary("sha256:" + strings.Repeat("0", 64)); err == nil {
		t.Fatal("an unpinned Git executable digest was accepted")
	}
}

func TestGitWorktreePathsRejectEscapesButAllowLiteralBrackets(t *testing.T) {
	if !validGitWorktreePath("frontend/app/[projectId]/page.tsx") {
		t.Fatal("a canonical Git path containing literal route brackets was rejected")
	}
	for _, invalid := range []string{"../escape", "/absolute", "a\\b", "a\nname", "a/../b"} {
		if validGitWorktreePath(invalid) {
			t.Fatalf("unsafe Git worktree path %q was accepted", invalid)
		}
	}
}
