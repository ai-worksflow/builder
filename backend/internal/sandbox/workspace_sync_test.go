package sandbox

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

func TestWorkspaceSynchronizerAppliesAndRecoversExactUpsert(t *testing.T) {
	oldValue := []byte("before\n")
	newValue := []byte("after\n")
	candidate, blobs := workspaceCandidate(t, map[string][]byte{"README.md": oldValue})
	materializer, mount, session := materializedWorkspace(t, candidate, blobs)
	leased, lease, err := candidate.AcquireLease(
		candidate.Version, testActorID, 20*time.Minute, sandboxBaseTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	operation := repository.FileOperation{
		ID: "workspace-upsert", Kind: repository.OperationUpsert, Path: "README.md",
		ExpectedHash: fileDigest(oldValue), ContentHash: fileDigest(newValue),
		ByteSize: int64(len(newValue)), Mode: "100755",
	}
	after, mutation := applyWorkspaceCandidateMutation(t, leased, lease, operation, sandboxBaseTime.Add(time.Second))
	workspace, err := openSafeWorkspace(mount.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	afterFile, _ := treeFile(after.CurrentTree, operation.Path)
	if err := workspace.apply(operation, afterFile, newValue); err != nil {
		t.Fatal(err)
	}
	_ = workspace.Close()

	// Simulate a crash after the atomic file rename but before projection.json
	// advanced. The public synchronizer must detect and finish that exact write.
	if err := materializer.SynchronizeMutation(context.Background(), session, after, mutation, newValue); err != nil {
		t.Fatal(err)
	}
	if err := materializer.SynchronizeMutation(context.Background(), session, after, mutation, newValue); err != nil {
		t.Fatalf("exact workspace retry was not idempotent: %v", err)
	}
	actual, err := os.ReadFile(filepath.Join(mount.Workspace, "README.md"))
	if err != nil || string(actual) != string(newValue) {
		t.Fatalf("workspace upsert = %q, %v", actual, err)
	}
	info, err := os.Stat(filepath.Join(mount.Workspace, "README.md"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("workspace upsert mode = %v, %v", info, err)
	}
	projection, err := loadWorkspaceProjection(mount)
	if err != nil || projection.CandidateVersion != after.Version ||
		projection.CandidateJournalSequence != after.JournalSequence || projection.TreeHash != after.CurrentTree.TreeHash {
		t.Fatalf("workspace projection did not advance exactly: %#v, %v", projection, err)
	}

	if err := os.WriteFile(filepath.Join(mount.Workspace, "README.md"), []byte("agent drift\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := materializer.SynchronizeMutation(context.Background(), session, after, mutation, newValue); !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("post-commit workspace drift was hidden: %v", err)
	}
}

func TestWorkspaceSynchronizerAppliesDeleteAndRename(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation func([]byte) repository.FileOperation
		wantPath  string
		gonePath  string
	}{
		{
			name: "delete",
			operation: func(value []byte) repository.FileOperation {
				return repository.FileOperation{
					ID: "workspace-delete", Kind: repository.OperationDelete,
					Path: "src/app.ts", ExpectedHash: fileDigest(value),
				}
			},
			gonePath: "src/app.ts",
		},
		{
			name: "rename",
			operation: func(value []byte) repository.FileOperation {
				return repository.FileOperation{
					ID: "workspace-rename", Kind: repository.OperationRename,
					FromPath: "src/app.ts", Path: "app/main.ts", ExpectedHash: fileDigest(value),
				}
			},
			wantPath: "app/main.ts", gonePath: "src/app.ts",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := []byte("export const ready = true\n")
			candidate, blobs := workspaceCandidate(t, map[string][]byte{"src/app.ts": value})
			materializer, mount, session := materializedWorkspace(t, candidate, blobs)
			leased, lease, err := candidate.AcquireLease(
				candidate.Version, testActorID, 20*time.Minute, sandboxBaseTime,
			)
			if err != nil {
				t.Fatal(err)
			}
			operation := test.operation(value)
			after, mutation := applyWorkspaceCandidateMutation(t, leased, lease, operation, sandboxBaseTime.Add(time.Second))
			if err := materializer.SynchronizeMutation(context.Background(), session, after, mutation, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(mount.Workspace, filepath.FromSlash(test.gonePath))); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("source path still exists: %v", err)
			}
			if test.wantPath != "" {
				actual, err := os.ReadFile(filepath.Join(mount.Workspace, filepath.FromSlash(test.wantPath)))
				if err != nil || string(actual) != string(value) {
					t.Fatalf("renamed file = %q, %v", actual, err)
				}
			}
		})
	}
}

func TestWorkspaceSynchronizerRejectsSymlinkTraversalAndValueDrift(t *testing.T) {
	candidate, blobs := workspaceCandidate(t, map[string][]byte{"README.md": []byte("base\n")})
	materializer, mount, session := materializedWorkspace(t, candidate, blobs)
	leased, lease, err := candidate.AcquireLease(
		candidate.Version, testActorID, 20*time.Minute, sandboxBaseTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	value := []byte("safe value\n")
	operation := repository.FileOperation{
		ID: "workspace-symlink", Kind: repository.OperationUpsert, Path: "linked/value.txt",
		ContentHash: fileDigest(value), ByteSize: int64(len(value)), Mode: "100644",
	}
	after, mutation := applyWorkspaceCandidateMutation(t, leased, lease, operation, sandboxBaseTime.Add(time.Second))
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(mount.Workspace, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := materializer.SynchronizeMutation(context.Background(), session, after, mutation, value); !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("workspace symlink traversal was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "value.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace write escaped its root: %v", err)
	}

	if err := os.Remove(filepath.Join(mount.Workspace, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := materializer.SynchronizeMutation(context.Background(), session, after, mutation, []byte("wrong bytes\n")); !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("workspace content drift was accepted: %v", err)
	}
	projection, err := loadWorkspaceProjection(mount)
	if err != nil || projection.CandidateJournalSequence != candidate.JournalSequence {
		t.Fatalf("rejected mutation advanced projection: %#v, %v", projection, err)
	}
}

func TestWorkspaceSynchronizerRecoversAtomicBatchBeforeAdvancingProjection(t *testing.T) {
	oldValue := []byte("before\n")
	deleteValue := []byte("remove me\n")
	newValue := []byte("after\n")
	candidate, blobs := workspaceCandidate(t, map[string][]byte{
		"src/a.ts": oldValue, "src/delete.ts": deleteValue,
	})
	materializer, mount, session := materializedWorkspace(t, candidate, blobs)
	blobs[fileDigest(newValue)] = append([]byte(nil), newValue...)
	leased, lease, err := candidate.AcquireLease(
		candidate.Version, testActorID, 20*time.Minute, sandboxBaseTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	operations := []repository.FileOperation{
		{
			ID: "agent-batch-upsert", Kind: repository.OperationUpsert, Path: "src/a.ts",
			ExpectedHash: fileDigest(oldValue), ContentHash: fileDigest(newValue),
			ByteSize: int64(len(newValue)), Mode: "100644",
		},
		{
			ID: "agent-batch-delete", Kind: repository.OperationDelete, Path: "src/delete.ts",
			ExpectedHash: fileDigest(deleteValue),
		},
	}
	after := leased
	entries := make([]repository.JournalEntry, len(operations))
	for index, operation := range operations {
		after, entries[index], err = after.Apply(
			after.Version, after.SessionEpoch, lease.Epoch,
			testActorID, "agent", operation, sandboxBaseTime.Add(time.Second),
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	mutation := repository.BatchMutationResult{
		Entries:               entries,
		BeforeTree:            repository.TreeBlobPointer{TreeHash: entries[0].BeforeTree},
		AfterTree:             repository.TreeBlobPointer{TreeHash: entries[len(entries)-1].AfterTree},
		FinalCandidateVersion: after.Version,
	}

	// Crash after the first file rename while projection.json still identifies
	// the exact before tree. SynchronizeBatch must finish, not overwrite drift.
	workspace, err := openSafeWorkspace(mount.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	afterFile, _ := treeFile(after.CurrentTree, operations[0].Path)
	if err := workspace.apply(operations[0], afterFile, newValue); err != nil {
		t.Fatal(err)
	}
	_ = workspace.Close()
	if err := materializer.SynchronizeBatch(context.Background(), session, after, mutation); err != nil {
		t.Fatal(err)
	}
	if err := materializer.SynchronizeBatch(context.Background(), session, after, mutation); err != nil {
		t.Fatalf("exact batch retry was not idempotent: %v", err)
	}
	actual, err := os.ReadFile(filepath.Join(mount.Workspace, "src", "a.ts"))
	if err != nil || string(actual) != string(newValue) {
		t.Fatalf("batch upsert = %q, %v", actual, err)
	}
	if _, err := os.Stat(filepath.Join(mount.Workspace, "src", "delete.ts")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("batch delete was not projected: %v", err)
	}
	projection, err := loadWorkspaceProjection(mount)
	if err != nil || projection.CandidateVersion != after.Version ||
		projection.CandidateJournalSequence != after.JournalSequence ||
		projection.TreeHash != after.CurrentTree.TreeHash {
		t.Fatalf("batch projection=%#v err=%v", projection, err)
	}
}

func materializedWorkspace(
	t *testing.T,
	candidate repository.CandidateWorkspace,
	blobs map[string][]byte,
) (*WorkspaceMaterializer, WorkspaceMount, SessionView) {
	t.Helper()
	materializer, err := NewWorkspaceMaterializer(t.TempDir(), &workspaceResolverFake{values: blobs})
	if err != nil {
		t.Fatal(err)
	}
	session := newTestSession(t, candidate, sandboxBaseTime).Snapshot()
	mount, err := materializer.Materialize(context.Background(), session, candidate)
	if err != nil {
		t.Fatal(err)
	}
	return materializer, mount, session
}

func applyWorkspaceCandidateMutation(
	t *testing.T,
	candidate repository.CandidateWorkspace,
	lease repository.WriterLease,
	operation repository.FileOperation,
	at time.Time,
) (repository.CandidateWorkspace, repository.MutationResult) {
	t.Helper()
	after, entry, err := candidate.Apply(
		candidate.Version, candidate.SessionEpoch, lease.Epoch,
		testActorID, "user", operation, at,
	)
	if err != nil {
		t.Fatal(err)
	}
	return after, repository.MutationResult{
		Entry:      entry,
		BeforeTree: repository.TreeBlobPointer{TreeHash: entry.BeforeTree},
		AfterTree:  repository.TreeBlobPointer{TreeHash: entry.AfterTree},
	}
}

func fileDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + fmtHex(digest[:])
}
