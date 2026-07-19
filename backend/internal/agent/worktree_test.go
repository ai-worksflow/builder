package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

type worktreeFileResolverFake struct {
	values map[string][]byte
}

func (resolver *worktreeFileResolverFake) Resolve(
	_ context.Context,
	projectID, contentHash string,
	byteSize int64,
) (repository.FileBlobPointer, []byte, error) {
	value, found := resolver.values[contentHash]
	if !found || int64(len(value)) != byteSize {
		return repository.FileBlobPointer{}, nil, repository.ErrFileBlobNotFound
	}
	return repository.FileBlobPointer{
		Store: repository.FileContentStore, Ref: uuid.NewString(), OwnerID: uuid.NewString(),
		ContentHash: contentHash, ByteSize: byteSize, ContentObjectHash: testHash("a"),
	}, append([]byte(nil), value...), nil
}

func TestWorktreeMaterializesExactTreeAndCapturesCanonicalPlatformPatch(t *testing.T) {
	fixture := agentWorktreeFixture(t)
	manager, err := NewWorktreeManager(t.TempDir(), fixture.files)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := manager.Prepare(
		context.Background(), fixture.capsule.ProjectID, fixture.attempt.ID, 1, fixture.tree,
	)
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(lease.Workspace, "apps/web/features/conversation/message.ts")
	if err := os.WriteFile(allowed, []byte("export const message = 'after'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(lease.Workspace, "apps/web/features/conversation/view.tsx")
	if err := os.WriteFile(created, []byte("export const View = () => null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	captured, err := CaptureWorktreePatch(
		lease.Workspace, fixture.tree, fixture.capsule.WriteSet,
		fixture.capsule.ProtectedPaths, fixture.capsule.Budgets.MaxPatchBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.Changes) != 2 || captured.Changes[0].Operation.Path != "apps/web/features/conversation/message.ts" ||
		captured.Changes[1].Operation.Path != "apps/web/features/conversation/view.tsx" ||
		captured.ProposedTree.TreeHash == captured.BaseTree.TreeHash {
		t.Fatalf("captured patch = %#v", captured)
	}
	patch, err := NewPlatformPatch(fixture.attempt, fixture.capsule, captured)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePlatformPatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := ApplyPlatformPatch(fixture.tree, parsed)
	if err != nil || projected.TreeHash != captured.ProposedTree.TreeHash {
		t.Fatalf("apply platform patch: tree=%#v err=%v", projected, err)
	}

	tampered := patch
	tampered.ChangedBytes++
	if _, err := ParsePlatformPatch(tampered); !errors.Is(err, ErrExecutionDrift) {
		t.Fatalf("tampered patch error = %v", err)
	}
	if err := manager.Cleanup(lease); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreePatchRejectsProtectedOutsideAndSymlinkChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, workspace string)
	}{
		{
			name: "protected",
			mutate: func(t *testing.T, workspace string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(workspace, ".github/workflows/ci.yml"), []byte("changed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "outside write set",
			mutate: func(t *testing.T, workspace string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("changed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			mutate: func(t *testing.T, workspace string) {
				t.Helper()
				target := filepath.Join(workspace, "apps/web/features/conversation/link.ts")
				if err := os.Symlink("message.ts", target); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := agentWorktreeFixture(t)
			manager, err := NewWorktreeManager(t.TempDir(), fixture.files)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := manager.Prepare(
				context.Background(), fixture.capsule.ProjectID, fixture.attempt.ID, 1, fixture.tree,
			)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, lease.Workspace)
			if _, err := CaptureWorktreePatch(
				lease.Workspace, fixture.tree, fixture.capsule.WriteSet,
				fixture.capsule.ProtectedPaths, fixture.capsule.Budgets.MaxPatchBytes,
			); !errors.Is(err, ErrPatchPolicy) {
				t.Fatalf("capture error = %v", err)
			}
		})
	}
}

type agentWorktreeTestFixture struct {
	tree    repository.TreeManifest
	files   *worktreeFileResolverFake
	capsule TaskCapsule
	attempt AgentAttempt
}

func agentWorktreeFixture(t *testing.T) agentWorktreeTestFixture {
	t.Helper()
	values := map[string][]byte{}
	files := []struct {
		path  string
		mode  string
		value string
	}{
		{".github/workflows/ci.yml", "100644", "name: CI\n"},
		{"README.md", "100644", "base\n"},
		{"apps/web/features/conversation/message.ts", "100644", "export const message = 'before'\n"},
	}
	treeFiles := make([]repository.TreeFile, 0, len(files))
	for _, file := range files {
		value := []byte(file.value)
		hash := rawWorktreeHash(value)
		values[hash] = value
		treeFiles = append(treeFiles, repository.TreeFile{
			Path: file.path, Mode: file.mode, ContentHash: hash, ByteSize: int64(len(value)),
		})
	}
	tree, err := repository.NewTree(treeFiles)
	if err != nil {
		t.Fatal(err)
	}
	base := newAgentFixture(t)
	base.contextInput.BaseCandidateTreeHash = tree.TreeHash
	base.taskInput.BaseCandidateTreeHash = tree.TreeHash
	pack, err := NewContextPack(base.contextInput, base.now)
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := NewTaskCapsule(base.taskInput, pack, base.now.Add(time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := NewAttempt(NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: base.actorID, Executor: testExecutor(),
	}, capsule, pack, base.now.Add(2*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	return agentWorktreeTestFixture{
		tree: tree, files: &worktreeFileResolverFake{values: values}, capsule: capsule, attempt: attempt,
	}
}
