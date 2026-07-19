package lsp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/sandbox"
)

const (
	snapshotProjectID   = "21000000-0000-4000-8000-000000000001"
	snapshotSessionID   = "21000000-0000-4000-8000-000000000002"
	snapshotCandidateID = "21000000-0000-4000-8000-000000000003"
	snapshotRepoID      = "21000000-0000-4000-8000-000000000004"
	snapshotManifestID  = "21000000-0000-4000-8000-000000000005"
	snapshotContractID  = "21000000-0000-4000-8000-000000000006"
	snapshotTemplateID  = "21000000-0000-4000-8000-000000000007"
	snapshotReleaseID   = "21000000-0000-4000-8000-000000000008"
	snapshotActorID     = "21000000-0000-4000-8000-000000000009"
	snapshotBlobOwnerID = "21000000-0000-4000-8000-000000000010"
)

type runtimeSnapshotBlob struct {
	pointer repository.FileBlobPointer
	value   []byte
}

type runtimeSnapshotFilesFake struct {
	mu sync.Mutex

	blobs            map[string]runtimeSnapshotBlob
	calls            int
	barrierTarget    int
	barrierArrivals  int
	barrier          chan struct{}
	blockUntilCancel bool
	entered          chan struct{}
	err              error
}

func (fake *runtimeSnapshotFilesFake) Resolve(
	ctx context.Context,
	projectID, contentHash string,
	byteSize int64,
) (repository.FileBlobPointer, []byte, error) {
	fake.mu.Lock()
	fake.calls++
	blob, found := fake.blobs[contentHash]
	if fake.entered != nil {
		select {
		case fake.entered <- struct{}{}:
		default:
		}
	}
	barrier := fake.barrier
	if barrier != nil {
		fake.barrierArrivals++
		if fake.barrierArrivals == fake.barrierTarget {
			close(barrier)
		}
	}
	block, resolveErr := fake.blockUntilCancel, fake.err
	fake.mu.Unlock()
	if block {
		<-ctx.Done()
		return repository.FileBlobPointer{}, nil, ctx.Err()
	}
	if barrier != nil {
		select {
		case <-barrier:
		case <-ctx.Done():
			return repository.FileBlobPointer{}, nil, ctx.Err()
		}
	}
	if resolveErr != nil {
		return repository.FileBlobPointer{}, nil, resolveErr
	}
	if projectID != snapshotProjectID || !found || byteSize < 0 {
		return repository.FileBlobPointer{}, nil, errors.New("unexpected exact blob request")
	}
	return blob.pointer, append([]byte(nil), blob.value...), nil
}

func (fake *runtimeSnapshotFilesFake) callCount() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.calls
}

type runtimeSnapshotFixture struct {
	base         string
	sessionRoot  string
	live         string
	candidate    repository.CandidateWorkspace
	session      sandbox.SessionView
	files        *runtimeSnapshotFilesFake
	materializer *RuntimeWorkspaceSnapshotMaterializer
}

func newRuntimeSnapshotFixture(
	t *testing.T,
	values map[string]struct {
		mode  string
		value []byte
	},
	quota int64,
) runtimeSnapshotFixture {
	t.Helper()
	if quota == 0 {
		quota = 8 << 20
	}
	var treeFiles []repository.TreeFile
	resolver := &runtimeSnapshotFilesFake{blobs: make(map[string]runtimeSnapshotBlob, len(values))}
	for path, value := range values {
		digest := runtimeSnapshotDigest(value.value)
		treeFiles = append(treeFiles, repository.TreeFile{
			Path: path, Mode: value.mode, ContentHash: digest, ByteSize: int64(len(value.value)),
		})
		resolver.blobs[digest] = runtimeSnapshotBlob{
			pointer: repository.FileBlobPointer{
				Store: repository.FileContentStore,
				Ref:   "blob-" + strings.TrimPrefix(digest, "sha256:"), OwnerID: snapshotBlobOwnerID,
				ContentHash: digest, ByteSize: int64(len(value.value)),
				ContentObjectHash: runtimeSnapshotDigest(append([]byte("object:"), value.value...)),
			},
			value: append([]byte(nil), value.value...),
		}
	}
	tree, err := repository.NewTree(treeFiles)
	if err != nil {
		t.Fatal(err)
	}
	baseTime := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	candidate, err := repository.NewCandidate(snapshotCandidateID, repository.RepositorySnapshot{
		ID: snapshotRepoID, ProjectID: snapshotProjectID,
		BuildManifest:     repository.ExactReference{ID: snapshotManifestID, ContentHash: runtimeSnapshotDigest([]byte("manifest"))},
		BuildContract:     repository.ExactReference{ID: snapshotContractID, ContentHash: runtimeSnapshotDigest([]byte("contract"))},
		FullStackTemplate: repository.ExactReference{ID: snapshotTemplateID, ContentHash: runtimeSnapshotDigest([]byte("template"))},
		Tree:              tree, CreatedBy: snapshotActorID, CreatedAt: baseTime,
	}, snapshotActorID, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	view := runtimeSnapshotReadySession(t, candidate, quota, baseTime.Add(time.Second))
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeRuntimeSnapshotTreeWritable(base) })
	sessionRoot := filepath.Join(base, snapshotSessionID)
	live := filepath.Join(sessionRoot, "workspace")
	if err := os.MkdirAll(live, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sessionRoot, "runtime", "codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	materializer, err := NewRuntimeWorkspaceSnapshotMaterializer(base, resolver)
	if err != nil {
		t.Fatal(err)
	}
	return runtimeSnapshotFixture{
		base: base, sessionRoot: sessionRoot, live: live,
		candidate: candidate, session: view, files: resolver, materializer: materializer,
	}
}

func runtimeSnapshotReadySession(
	t *testing.T,
	candidate repository.CandidateWorkspace,
	quota int64,
	at time.Time,
) sandbox.SessionView {
	t.Helper()
	session, err := sandbox.NewSession(sandbox.NewSessionInput{
		ID: snapshotSessionID, ActorID: snapshotActorID, Candidate: candidate,
		RunnerImageDigest: runtimeSnapshotDigest([]byte("runner")),
		Quota: sandbox.Quota{
			CPUMillis: 1_000, MemoryBytes: 512 << 20, WorkspaceBytes: quota,
			PIDLimit: 64, PreviewPortLimit: 1,
		},
		TTL: sandbox.TTLPolicy{IdleHibernateAfter: time.Hour, MaxRuntime: 4 * time.Hour},
		Services: []sandbox.AllowedService{{
			ID: "web", Kind: "web", Profiles: []string{"dev"},
			TemplateRelease: repository.ExactReference{
				ID: snapshotReleaseID, ContentHash: runtimeSnapshotDigest([]byte("release")),
			},
		}},
		Ports: []sandbox.AllowedPort{{
			Name: "web-http", ServiceID: "web", Number: 3000, Protocol: "http",
		}},
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.BeginStart(1, 1, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.MarkReady(2, 1, at.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return session.Snapshot()
}

func TestRuntimeWorkspaceSnapshotMaterializesExactReadonlyTreeAwayFromLiveWorkspace(t *testing.T) {
	fixture := newRuntimeSnapshotFixture(t, map[string]struct {
		mode  string
		value []byte
	}{
		"apps/web/main.ts": {mode: "100644", value: []byte("export const exact = 1\n")},
		"bin/tool":         {mode: "100755", value: []byte{0xff, 0x00, 0x01, 0x02}},
	}, 0)
	if err := os.MkdirAll(filepath.Join(fixture.live, "apps", "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.live, "apps", "web", "main.ts"), []byte("terminal bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mount, err := fixture.materializer.Materialize(context.Background(), fixture.session, fixture.candidate)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(
		fixture.sessionRoot, "runtime", "lsp-snapshots", fixture.candidate.CurrentTree.TreeHash,
	)
	if mount.SessionRoot != wantRoot || mount.Workspace != filepath.Join(wantRoot, "workspace") ||
		mount.CodexHome != filepath.Join(wantRoot, "runtime", "codex") || mount.Workspace == fixture.live {
		t.Fatalf("snapshot mount = %#v", mount)
	}
	runtimeSnapshotRequireMode(t, mount.SessionRoot, 0o555)
	runtimeSnapshotRequireMode(t, mount.Workspace, 0o555)
	runtimeSnapshotRequireMode(t, filepath.Join(mount.Workspace, "apps", "web"), 0o555)
	runtimeSnapshotRequireMode(t, filepath.Join(mount.Workspace, "apps", "web", "main.ts"), 0o444)
	runtimeSnapshotRequireMode(t, filepath.Join(mount.Workspace, "bin", "tool"), 0o555)
	tool, err := os.ReadFile(filepath.Join(mount.Workspace, "bin", "tool"))
	if err != nil || !bytes.Equal(tool, []byte{0xff, 0x00, 0x01, 0x02}) {
		t.Fatalf("binary repository file = %x, %v", tool, err)
	}
	if err := os.WriteFile(filepath.Join(fixture.live, "apps", "web", "main.ts"), []byte("mutated live bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exact, err := os.ReadFile(filepath.Join(mount.Workspace, "apps", "web", "main.ts"))
	if err != nil || string(exact) != "export const exact = 1\n" {
		t.Fatalf("live mutation crossed immutable snapshot: %q, %v", exact, err)
	}
	firstCalls := fixture.files.callCount()
	second, err := fixture.materializer.Materialize(context.Background(), fixture.session, fixture.candidate)
	if err != nil || second != mount || fixture.files.callCount() != firstCalls {
		t.Fatalf("idempotent reverify = %#v, %v, resolver calls=%d", second, err, fixture.files.callCount())
	}
}

func TestRuntimeWorkspaceSnapshotConcurrentPublishIsNoReplaceAndIdempotent(t *testing.T) {
	fixture := newRuntimeSnapshotFixture(t, map[string]struct {
		mode  string
		value []byte
	}{"src/index.ts": {mode: "100644", value: []byte("export {}\n")}}, 0)
	fixture.files.barrierTarget = 2
	fixture.files.barrier = make(chan struct{})
	secondMaterializer, err := NewRuntimeWorkspaceSnapshotMaterializer(fixture.base, fixture.files)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		mount sandbox.WorkspaceMount
		err   error
	}
	results := make(chan result, 2)
	for _, materializer := range []*RuntimeWorkspaceSnapshotMaterializer{fixture.materializer, secondMaterializer} {
		go func(materializer *RuntimeWorkspaceSnapshotMaterializer) {
			mount, materializeErr := materializer.Materialize(context.Background(), fixture.session, fixture.candidate)
			results <- result{mount: mount, err: materializeErr}
		}(materializer)
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.mount != second.mount {
		t.Fatalf("concurrent results = %#v / %#v", first, second)
	}
	if calls := fixture.files.callCount(); calls != 2 {
		t.Fatalf("concurrent builders resolved %d blobs, want 2", calls)
	}
	if _, err := fixture.materializer.Materialize(context.Background(), fixture.session, fixture.candidate); err != nil {
		t.Fatal(err)
	}
	if calls := fixture.files.callCount(); calls != 2 {
		t.Fatalf("published snapshot was rebuilt: resolver calls=%d", calls)
	}
}

func TestRuntimeWorkspaceSnapshotPreservesOldTreeAcrossCandidateMutation(t *testing.T) {
	fixture := newRuntimeSnapshotFixture(t, map[string]struct {
		mode  string
		value []byte
	}{"src/value.ts": {mode: "100644", value: []byte("old\n")}}, 0)
	oldMount, err := fixture.materializer.Materialize(context.Background(), fixture.session, fixture.candidate)
	if err != nil {
		t.Fatal(err)
	}
	newValue := []byte("new\n")
	newDigest := runtimeSnapshotDigest(newValue)
	newTree, err := repository.NewTree([]repository.TreeFile{{
		Path: "src/value.ts", Mode: "100644", ContentHash: newDigest, ByteSize: int64(len(newValue)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	next := fixture.candidate
	next.CurrentTree = newTree
	next.Version++
	next.JournalSequence++
	next.Dirty = true
	next.UpdatedAt = next.UpdatedAt.Add(time.Second)
	if err := next.Validate(); err != nil {
		t.Fatal(err)
	}
	fixture.files.mu.Lock()
	fixture.files.blobs[newDigest] = runtimeSnapshotBlob{
		pointer: repository.FileBlobPointer{
			Store: repository.FileContentStore, Ref: "blob-" + strings.TrimPrefix(newDigest, "sha256:"),
			OwnerID: snapshotBlobOwnerID, ContentHash: newDigest, ByteSize: int64(len(newValue)),
			ContentObjectHash: runtimeSnapshotDigest([]byte("new-object")),
		},
		value: append([]byte(nil), newValue...),
	}
	fixture.files.mu.Unlock()
	nextSession := runtimeSnapshotReadySession(
		t, next, fixture.session.Quota.WorkspaceBytes, next.UpdatedAt.Add(time.Second),
	)
	newMount, err := fixture.materializer.Materialize(context.Background(), nextSession, next)
	if err != nil {
		t.Fatal(err)
	}
	if newMount == oldMount || newMount.Workspace == oldMount.Workspace {
		t.Fatalf("new exact tree reused old mount: %#v", newMount)
	}
	oldBytes, oldErr := os.ReadFile(filepath.Join(oldMount.Workspace, "src", "value.ts"))
	newBytes, newErr := os.ReadFile(filepath.Join(newMount.Workspace, "src", "value.ts"))
	if oldErr != nil || newErr != nil || string(oldBytes) != "old\n" || string(newBytes) != "new\n" {
		t.Fatalf("snapshot isolation = old:%q/%v new:%q/%v", oldBytes, oldErr, newBytes, newErr)
	}
}

func TestRuntimeWorkspaceSnapshotReusesExactTreeAcrossNewHeadWithoutOverwritingBytes(t *testing.T) {
	fixture := newRuntimeSnapshotFixture(t, map[string]struct {
		mode  string
		value []byte
	}{"src/value.ts": {mode: "100644", value: []byte("same tree\n")}}, 0)
	mount, err := fixture.materializer.Materialize(context.Background(), fixture.session, fixture.candidate)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := fixture.candidate.AcquireLease(
		fixture.candidate.Version, snapshotActorID, time.Minute, fixture.candidate.UpdatedAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	nextSession := runtimeSnapshotReadySession(
		t, next, fixture.session.Quota.WorkspaceBytes, next.UpdatedAt.Add(time.Second),
	)
	resolverCalls := fixture.files.callCount()
	got, err := fixture.materializer.Materialize(context.Background(), nextSession, next)
	if err != nil || got != mount || fixture.files.callCount() != resolverCalls {
		t.Fatalf("same exact tree was not safely reused: %#v, %v, calls=%d", got, err, fixture.files.callCount())
	}
	value, err := os.ReadFile(filepath.Join(mount.Workspace, "src", "value.ts"))
	if err != nil || string(value) != "same tree\n" {
		t.Fatalf("old snapshot changed: %q, %v", value, err)
	}
}

func TestRuntimeWorkspaceSnapshotExistingTreeTamperFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(*testing.T, runtimeSnapshotFixture, sandbox.WorkspaceMount)
	}{
		{name: "bytes", tamper: func(t *testing.T, _ runtimeSnapshotFixture, mount sandbox.WorkspaceMount) {
			path := filepath.Join(mount.Workspace, "src", "index.ts")
			runtimeSnapshotRewrite(t, path, []byte("tampered!\n"), 0o444)
		}},
		{name: "file mode", tamper: func(t *testing.T, _ runtimeSnapshotFixture, mount sandbox.WorkspaceMount) {
			if err := os.Chmod(filepath.Join(mount.Workspace, "src", "index.ts"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", tamper: func(t *testing.T, fixture runtimeSnapshotFixture, mount sandbox.WorkspaceMount) {
			path := filepath.Join(mount.Workspace, "src", "index.ts")
			parent := filepath.Dir(path)
			if err := os.Chmod(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(fixture.base, "outside")
			if err := os.WriteFile(outside, []byte("export {}\n"), 0o444); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
			_ = os.Chmod(parent, 0o555)
		}},
		{name: "hardlink", tamper: func(t *testing.T, fixture runtimeSnapshotFixture, mount sandbox.WorkspaceMount) {
			path := filepath.Join(mount.Workspace, "src", "index.ts")
			parent := filepath.Dir(path)
			outside := filepath.Join(fixture.base, "outside-hardlink")
			if err := os.WriteFile(outside, []byte("export {}\n"), 0o444); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(outside, path); err != nil {
				t.Fatal(err)
			}
			_ = os.Chmod(parent, 0o555)
		}},
		{name: "extra file", tamper: func(t *testing.T, _ runtimeSnapshotFixture, mount sandbox.WorkspaceMount) {
			if err := os.Chmod(mount.Workspace, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(mount.Workspace, "extra"), []byte("extra"), 0o444); err != nil {
				t.Fatal(err)
			}
			_ = os.Chmod(mount.Workspace, 0o555)
		}},
		{name: "projection", tamper: func(t *testing.T, _ runtimeSnapshotFixture, mount sandbox.WorkspaceMount) {
			projection := filepath.Join(mount.SessionRoot, "projection.json")
			runtimeSnapshotRewrite(t, projection, []byte(`{"schemaVersion":"lsp-runtime-workspace-snapshot/v1","extra":true}`), 0o444)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeSnapshotFixture(t, map[string]struct {
				mode  string
				value []byte
			}{"src/index.ts": {mode: "100644", value: []byte("export {}\n")}}, 0)
			mount, err := fixture.materializer.Materialize(context.Background(), fixture.session, fixture.candidate)
			if err != nil {
				t.Fatal(err)
			}
			test.tamper(t, fixture, mount)
			if got, err := fixture.materializer.Materialize(context.Background(), fixture.session, fixture.candidate); got != (sandbox.WorkspaceMount{}) || !errors.Is(err, ErrRuntimeWorkspaceSnapshotConflict) {
				t.Fatalf("tampered snapshot accepted: %#v, %v", got, err)
			}
		})
	}
}

func TestRuntimeWorkspaceSnapshotRejectsBlobPointerAndByteDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*runtimeSnapshotBlob)
	}{
		{name: "store", mutate: func(blob *runtimeSnapshotBlob) { blob.pointer.Store = "other" }},
		{name: "ref", mutate: func(blob *runtimeSnapshotBlob) { blob.pointer.Ref = "" }},
		{name: "owner", mutate: func(blob *runtimeSnapshotBlob) { blob.pointer.OwnerID = "not-a-uuid" }},
		{name: "pointer hash", mutate: func(blob *runtimeSnapshotBlob) { blob.pointer.ContentHash = runtimeSnapshotDigest([]byte("other")) }},
		{name: "pointer size", mutate: func(blob *runtimeSnapshotBlob) { blob.pointer.ByteSize++ }},
		{name: "object hash", mutate: func(blob *runtimeSnapshotBlob) { blob.pointer.ContentObjectHash = "latest" }},
		{name: "bytes hash", mutate: func(blob *runtimeSnapshotBlob) { blob.value[0] ^= 0xff }},
		{name: "bytes size", mutate: func(blob *runtimeSnapshotBlob) { blob.value = blob.value[:len(blob.value)-1] }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeSnapshotFixture(t, map[string]struct {
				mode  string
				value []byte
			}{"src/index.ts": {mode: "100644", value: []byte("export {}\n")}}, 0)
			hash := fixture.candidate.CurrentTree.Files[0].ContentHash
			fixture.files.mu.Lock()
			blob := fixture.files.blobs[hash]
			test.mutate(&blob)
			fixture.files.blobs[hash] = blob
			fixture.files.mu.Unlock()
			if got, err := fixture.materializer.Materialize(context.Background(), fixture.session, fixture.candidate); got != (sandbox.WorkspaceMount{}) || !errors.Is(err, ErrRuntimeWorkspaceSnapshotConflict) {
				t.Fatalf("blob drift accepted: %#v, %v", got, err)
			}
			runtimeSnapshotRequireNoPublishedOrStaging(t, fixture)
		})
	}
}

func TestRuntimeWorkspaceSnapshotEnforcesQuotaBeforeBlobResolution(t *testing.T) {
	fixture := newRuntimeSnapshotFixture(t, map[string]struct {
		mode  string
		value []byte
	}{
		"a.bin": {mode: "100644", value: bytes.Repeat([]byte("a"), 600<<10)},
		"b.bin": {mode: "100644", value: bytes.Repeat([]byte("b"), 600<<10)},
	}, 1<<20)
	if got, err := fixture.materializer.Materialize(context.Background(), fixture.session, fixture.candidate); got != (sandbox.WorkspaceMount{}) || !errors.Is(err, ErrRuntimeWorkspaceSnapshotConflict) {
		t.Fatalf("over-quota tree accepted: %#v, %v", got, err)
	}
	if calls := fixture.files.callCount(); calls != 0 {
		t.Fatalf("over-quota tree reached blob resolver %d times", calls)
	}
}

func TestRuntimeWorkspaceSnapshotRejectsPathEscapeBeforeFilesystemMutation(t *testing.T) {
	fixture := newRuntimeSnapshotFixture(t, map[string]struct {
		mode  string
		value []byte
	}{"src/index.ts": {mode: "100644", value: []byte("export {}\n")}}, 0)
	escaped := fixture.candidate
	escaped.CurrentTree.Files = append([]repository.TreeFile(nil), escaped.CurrentTree.Files...)
	escaped.CurrentTree.Files[0].Path = "../escaped"
	if got, err := fixture.materializer.Materialize(context.Background(), fixture.session, escaped); got != (sandbox.WorkspaceMount{}) || !errors.Is(err, ErrRuntimeWorkspaceSnapshotConflict) {
		t.Fatalf("path escape accepted: %#v, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.base, "escaped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path escape mutated host filesystem: %v", err)
	}
	if fixture.files.callCount() != 0 {
		t.Fatal("path escape reached blob resolver")
	}
}

func TestRuntimeWorkspaceSnapshotCancellationCleansStaging(t *testing.T) {
	t.Run("before materialization", func(t *testing.T) {
		fixture := newRuntimeSnapshotFixture(t, map[string]struct {
			mode  string
			value []byte
		}{"src/index.ts": {mode: "100644", value: []byte("export {}\n")}}, 0)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := fixture.materializer.Materialize(ctx, fixture.session, fixture.candidate); !errors.Is(err, context.Canceled) {
			t.Fatalf("Materialize = %v", err)
		}
		if fixture.files.callCount() != 0 {
			t.Fatal("canceled materialization reached blob resolver")
		}
	})

	t.Run("during blob resolution", func(t *testing.T) {
		fixture := newRuntimeSnapshotFixture(t, map[string]struct {
			mode  string
			value []byte
		}{"src/index.ts": {mode: "100644", value: []byte("export {}\n")}}, 0)
		fixture.files.blockUntilCancel = true
		fixture.files.entered = make(chan struct{}, 1)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := fixture.materializer.Materialize(ctx, fixture.session, fixture.candidate)
			result <- err
		}()
		select {
		case <-fixture.files.entered:
		case <-time.After(time.Second):
			t.Fatal("blob resolution did not start")
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("Materialize = %v", err)
		}
		runtimeSnapshotRequireNoPublishedOrStaging(t, fixture)
	})
}

func TestRuntimeWorkspaceSnapshotRejectsSymlinkedAuthorityPaths(t *testing.T) {
	t.Run("base root", func(t *testing.T) {
		realRoot := t.TempDir()
		link := filepath.Join(t.TempDir(), "root")
		if err := os.Symlink(realRoot, link); err != nil {
			t.Fatal(err)
		}
		if got, err := NewRuntimeWorkspaceSnapshotMaterializer(link, &runtimeSnapshotFilesFake{}); got != nil ||
			!errors.Is(err, ErrRuntimeWorkspaceSnapshotInvalid) {
			t.Fatalf("symlinked base accepted: %#v, %v", got, err)
		}
	})

	t.Run("base root authority drift", func(t *testing.T) {
		fixture := newRuntimeSnapshotFixture(t, map[string]struct {
			mode  string
			value []byte
		}{"src/index.ts": {mode: "100644", value: []byte("export {}\n")}}, 0)
		if err := os.Chmod(fixture.base, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.materializer.Materialize(context.Background(), fixture.session, fixture.candidate); !errors.Is(err, ErrRuntimeWorkspaceSnapshotConflict) {
			t.Fatalf("mutated base authority accepted: %v", err)
		}
	})

	t.Run("world writable runtime root", func(t *testing.T) {
		fixture := newRuntimeSnapshotFixture(t, map[string]struct {
			mode  string
			value []byte
		}{"src/index.ts": {mode: "100644", value: []byte("export {}\n")}}, 0)
		runtimeRoot := filepath.Join(fixture.sessionRoot, "runtime")
		if err := os.Chmod(runtimeRoot, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.materializer.Materialize(context.Background(), fixture.session, fixture.candidate); !errors.Is(err, ErrRuntimeWorkspaceSnapshotConflict) {
			t.Fatalf("world-writable runtime root accepted: %v", err)
		}
	})

	t.Run("runtime root", func(t *testing.T) {
		fixture := newRuntimeSnapshotFixture(t, map[string]struct {
			mode  string
			value []byte
		}{"src/index.ts": {mode: "100644", value: []byte("export {}\n")}}, 0)
		runtimeRoot := filepath.Join(fixture.sessionRoot, "runtime")
		if err := os.RemoveAll(runtimeRoot); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), runtimeRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.materializer.Materialize(context.Background(), fixture.session, fixture.candidate); !errors.Is(err, ErrRuntimeWorkspaceSnapshotConflict) {
			t.Fatalf("symlinked runtime root accepted: %v", err)
		}
	})

	t.Run("tree hash destination", func(t *testing.T) {
		fixture := newRuntimeSnapshotFixture(t, map[string]struct {
			mode  string
			value []byte
		}{"src/index.ts": {mode: "100644", value: []byte("export {}\n")}}, 0)
		snapshots := filepath.Join(fixture.sessionRoot, "runtime", "lsp-snapshots")
		if err := os.Mkdir(snapshots, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), filepath.Join(snapshots, fixture.candidate.CurrentTree.TreeHash)); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.materializer.Materialize(context.Background(), fixture.session, fixture.candidate); !errors.Is(err, ErrRuntimeWorkspaceSnapshotConflict) {
			t.Fatalf("symlinked snapshot destination accepted: %v", err)
		}
	})
}

func runtimeSnapshotRequireNoPublishedOrStaging(t *testing.T, fixture runtimeSnapshotFixture) {
	t.Helper()
	snapshots := filepath.Join(fixture.sessionRoot, "runtime", "lsp-snapshots")
	entries, err := os.ReadDir(snapshots)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed materialization left snapshot artifacts: %#v", entries)
	}
}

func runtimeSnapshotRewrite(t *testing.T, path string, value []byte, finalMode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, value, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, finalMode); err != nil {
		t.Fatal(err)
	}
}

func runtimeSnapshotRequireMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatalf("mode %s = %v, %v; want %v", path, info, err, mode)
	}
	if info.Mode().IsRegular() && (info.Mode().Perm()&0o004 == 0 || info.Mode().Perm()&0o002 != 0) {
		t.Fatalf("fixed non-root user cannot read or can write %s: %v", path, info.Mode())
	}
	if info.IsDir() && (info.Mode().Perm()&0o005 != 0o005 || info.Mode().Perm()&0o002 != 0) {
		t.Fatalf("fixed non-root user cannot traverse or can write %s: %v", path, info.Mode())
	}
}

func runtimeSnapshotDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return fmt.Sprintf("sha256:%x", digest[:])
}

func makeRuntimeSnapshotTreeWritable(root string) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
}
