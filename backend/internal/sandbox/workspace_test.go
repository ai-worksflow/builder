package sandbox

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/worksflow/builder/backend/internal/repository"
)

type workspaceResolverFake struct {
	values       map[string][]byte
	pointerDrift bool
	calls        int
}

func (resolver *workspaceResolverFake) Resolve(
	_ context.Context,
	_ string,
	contentHash string,
	byteSize int64,
) (repository.FileBlobPointer, []byte, error) {
	resolver.calls++
	value, ok := resolver.values[contentHash]
	if !ok {
		return repository.FileBlobPointer{}, nil, repository.ErrFileBlobNotFound
	}
	pointerHash := contentHash
	if resolver.pointerDrift {
		pointerHash = sandboxDigest("0")
	}
	return repository.FileBlobPointer{ContentHash: pointerHash, ByteSize: byteSize}, append([]byte(nil), value...), nil
}

func TestWorkspaceMaterializerPublishesExactCandidateAtomically(t *testing.T) {
	root := t.TempDir()
	values := map[string][]byte{
		"README.md":      []byte("hello sandbox\n"),
		"scripts/dev.sh": []byte("#!/bin/sh\nexec true\n"),
	}
	candidate, byHash := workspaceCandidate(t, values)
	resolver := &workspaceResolverFake{values: byHash}
	materializer, err := NewWorkspaceMaterializer(root, resolver)
	if err != nil {
		t.Fatal(err)
	}
	view := newTestSession(t, candidate, sandboxBaseTime).Snapshot()

	mount, err := materializer.Materialize(context.Background(), view, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if mount.SessionRoot != filepath.Join(root, testSessionID) ||
		mount.Workspace != filepath.Join(root, testSessionID, "workspace") ||
		mount.CodexHome != filepath.Join(root, testSessionID, "runtime", "codex") {
		t.Fatalf("unexpected workspace mount: %#v", mount)
	}
	for path, expected := range values {
		actual, readErr := os.ReadFile(filepath.Join(mount.Workspace, filepath.FromSlash(path)))
		if readErr != nil || string(actual) != string(expected) {
			t.Fatalf("projected %s = %q, %v", path, actual, readErr)
		}
	}
	readme, err := os.Stat(filepath.Join(mount.Workspace, "README.md"))
	if err != nil || readme.Mode().Perm() != 0o600 {
		t.Fatalf("README permissions = %v, %v", readme, err)
	}
	script, err := os.Stat(filepath.Join(mount.Workspace, "scripts", "dev.sh"))
	if err != nil || script.Mode().Perm() != 0o700 {
		t.Fatalf("script permissions = %v, %v", script, err)
	}
	if _, err := os.Stat(filepath.Join(mount.Workspace, "projection.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("projection marker leaked into editable workspace: %v", err)
	}

	firstCalls := resolver.calls
	resolver.values = nil
	second, err := materializer.Materialize(context.Background(), view, candidate)
	if err != nil || second != mount || resolver.calls != firstCalls {
		t.Fatalf("exact projection was not idempotent: mount=%#v calls=%d err=%v", second, resolver.calls, err)
	}
}

func TestWorkspaceMaterializerRejectsProjectionAndBlobDrift(t *testing.T) {
	root := t.TempDir()
	candidate, byHash := workspaceCandidate(t, map[string][]byte{"README.md": []byte("first\n")})
	resolver := &workspaceResolverFake{values: byHash}
	materializer, err := NewWorkspaceMaterializer(root, resolver)
	if err != nil {
		t.Fatal(err)
	}
	view := newTestSession(t, candidate, sandboxBaseTime).Snapshot()
	if _, err := materializer.Materialize(context.Background(), view, candidate); err != nil {
		t.Fatal(err)
	}

	different, differentBlobs := workspaceCandidate(t, map[string][]byte{"README.md": []byte("second\n")})
	differentView := newTestSession(t, different, sandboxBaseTime).Snapshot()
	materializer.files = &workspaceResolverFake{values: differentBlobs}
	if _, err := materializer.Materialize(context.Background(), differentView, different); !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("different Candidate reused an existing projection: %v", err)
	}

	driftRoot := t.TempDir()
	driftResolver := &workspaceResolverFake{values: byHash, pointerDrift: true}
	driftMaterializer, err := NewWorkspaceMaterializer(driftRoot, driftResolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driftMaterializer.Materialize(context.Background(), view, candidate); !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("catalog pointer drift was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(driftRoot, testSessionID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed projection was published: %v", err)
	}
}

func TestWorkspaceMaterializerEnforcesQuotaBeforeResolvingBlobs(t *testing.T) {
	root := t.TempDir()
	large := make([]byte, (1<<20)+1)
	candidate, byHash := workspaceCandidate(t, map[string][]byte{"large.bin": large})
	input := testSessionInput(candidate)
	input.Quota.WorkspaceBytes = 1 << 20
	session, err := NewSession(input, sandboxBaseTime)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &workspaceResolverFake{values: byHash}
	materializer, err := NewWorkspaceMaterializer(root, resolver)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := materializer.Materialize(context.Background(), session.Snapshot(), candidate); !errors.Is(err, ErrWorkspaceInvalid) {
		t.Fatalf("oversized Candidate was projected: %v", err)
	}
	if resolver.calls != 0 {
		t.Fatalf("blob was resolved before quota rejection: %d calls", resolver.calls)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("quota failure left workspace state behind: entries=%v err=%v", entries, err)
	}
}

func workspaceCandidate(
	t *testing.T,
	values map[string][]byte,
) (repository.CandidateWorkspace, map[string][]byte) {
	t.Helper()
	files := make([]repository.TreeFile, 0, len(values))
	byHash := make(map[string][]byte, len(values))
	for path, value := range values {
		digest := sha256.Sum256(value)
		contentHash := "sha256:" + fmtHex(digest[:])
		mode := "100644"
		if path == "scripts/dev.sh" {
			mode = "100755"
		}
		files = append(files, repository.TreeFile{
			Path: path, Mode: mode, ContentHash: contentHash, ByteSize: int64(len(value)),
		})
		byHash[contentHash] = append([]byte(nil), value...)
	}
	tree, err := repository.NewTree(files)
	if err != nil {
		t.Fatal(err)
	}
	candidate := cleanCandidate(t)
	candidate.BaseTreeHash = tree.TreeHash
	candidate.CurrentTree = tree
	if err := candidate.Validate(); err != nil {
		t.Fatal(err)
	}
	return candidate, byHash
}

func fmtHex(value []byte) string {
	const alphabet = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, item := range value {
		result[index*2] = alphabet[item>>4]
		result[index*2+1] = alphabet[item&0x0f]
	}
	return string(result)
}
