package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

type verificationFileResolverFake struct {
	values map[string][]byte
}

type candidateExecutionEnvironmentCleanupFake struct {
	cleanups []VerificationEnvironmentCleanup
	err      error
}

func (*candidateExecutionEnvironmentCleanupFake) Prepare(context.Context, CandidateExecutionSpec) error {
	return nil
}

func (environment *candidateExecutionEnvironmentCleanupFake) CleanupVerificationEnvironment(
	_ context.Context,
	input VerificationEnvironmentCleanup,
) error {
	environment.cleanups = append(environment.cleanups, input)
	return environment.err
}

func (resolver verificationFileResolverFake) Resolve(
	_ context.Context,
	_ string,
	contentHash string,
	byteSize int64,
) (repository.FileBlobPointer, []byte, error) {
	value, found := resolver.values[contentHash]
	if !found || int64(len(value)) != byteSize {
		return repository.FileBlobPointer{}, nil, errors.New("file blob unavailable")
	}
	return repository.FileBlobPointer{
		Store: repository.FileContentStore, Ref: uuid.NewString(), OwnerID: uuid.NewString(),
		ContentHash: contentHash, ByteSize: byteSize,
		ContentObjectHash: verificationTestHash("object:" + contentHash),
	}, append([]byte(nil), value...), nil
}

func TestCandidateWorkspaceMaterializerUsesExactTreeAndFenceDirectory(t *testing.T) {
	web := []byte("export const ready = true\n")
	api := []byte("#!/usr/bin/env python3\nprint('ready')\n")
	tree, err := repository.NewTree([]repository.TreeFile{
		{Path: "apps/web/app.ts", Mode: "100644", ContentHash: verificationBytesHash(web), ByteSize: int64(len(web))},
		{Path: "services/api/main.py", Mode: "100755", ContentHash: verificationBytesHash(api), ByteSize: int64(len(api))},
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := verificationMaterializerSpec(t, tree.TreeHash, 3)
	root := t.TempDir()
	materializer := &CandidateWorkspaceMaterializer{
		root: root,
		files: verificationFileResolverFake{values: map[string][]byte{
			verificationBytesHash(web): web, verificationBytesHash(api): api,
		}},
		resolveSnapshot: func(context.Context, CandidateExecutionSpec) (repository.TreeManifest, error) {
			return tree, nil
		},
	}
	if err := materializer.Materialize(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if err := materializer.Prepare(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, spec.AttemptID, "3", "workspace")
	actual, err := os.ReadFile(filepath.Join(workspace, "apps", "web", "app.ts"))
	if err != nil || string(actual) != string(web) {
		t.Fatalf("materialized exact web bytes = %q, %v", actual, err)
	}
	mode, err := os.Stat(filepath.Join(workspace, "services", "api", "main.py"))
	if err != nil || mode.Mode().Perm() != 0o500 {
		t.Fatalf("materialized executable mode = %v, %v", mode, err)
	}

	next := spec
	next.AttemptFenceEpoch = 4
	if err := materializer.Materialize(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("takeover retained stale older-fence workspace: %v", err)
	}
	if err := materializer.Collect(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, spec.AttemptID, "4")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("collect retained current fence workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, spec.AttemptID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("collect retained an empty Attempt workspace root: %v", err)
	}
}

func TestCandidateWorkspaceMaterializerFailsClosedOnMissingFileBlob(t *testing.T) {
	value := []byte("exact\n")
	tree, err := repository.NewTree([]repository.TreeFile{{
		Path: "apps/web/app.ts", Mode: "100644",
		ContentHash: verificationBytesHash(value), ByteSize: int64(len(value)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	spec := verificationMaterializerSpec(t, tree.TreeHash, 1)
	root := t.TempDir()
	materializer := &CandidateWorkspaceMaterializer{
		root: root, files: verificationFileResolverFake{values: map[string][]byte{}},
		resolveSnapshot: func(context.Context, CandidateExecutionSpec) (repository.TreeManifest, error) {
			return tree, nil
		},
	}
	if err := materializer.Materialize(context.Background(), spec); !errors.Is(err, ErrCandidateMaterialization) {
		t.Fatalf("missing exact file blob = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, spec.AttemptID, "1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed materialization exposed partial workspace: %v", err)
	}
}

func TestCandidateWorkspaceTakeoverCleansOlderFenceWithoutGrantingStaleSharedCleanup(t *testing.T) {
	value := []byte("exact\n")
	tree, err := repository.NewTree([]repository.TreeFile{{
		Path: "apps/web/app.ts", Mode: "100644",
		ContentHash: verificationBytesHash(value), ByteSize: int64(len(value)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	environment := &candidateExecutionEnvironmentCleanupFake{}
	spec := verificationMaterializerSpec(t, tree.TreeHash, 3)
	materializer := &CandidateWorkspaceMaterializer{
		root: root, environment: environment,
		files: verificationFileResolverFake{values: map[string][]byte{verificationBytesHash(value): value}},
		resolveSnapshot: func(context.Context, CandidateExecutionSpec) (repository.TreeManifest, error) {
			return tree, nil
		},
	}
	if err := materializer.Materialize(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if err := materializer.Prepare(context.Background(), spec); err != nil {
		t.Fatal(err)
	}

	newer := spec
	newer.AttemptFenceEpoch = 4
	environment.err = errors.New("verification daemon unavailable")
	if err := materializer.Materialize(context.Background(), newer); !errors.Is(err, ErrCandidateMaterialization) {
		t.Fatalf("takeover cleanup failure = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, spec.AttemptID, "3", "workspace")); err != nil {
		t.Fatalf("failed takeover removed retryable Candidate workspace: %v", err)
	}
	if marker, err := readVerificationRuntimeFence(filepath.Join(root, spec.AttemptID)); err != nil || marker != 3 {
		t.Fatalf("failed takeover removed Candidate runtime marker: marker=%d err=%v", marker, err)
	}
	environment.err = nil
	if err := materializer.Materialize(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	if len(environment.cleanups) != 2 || environment.cleanups[1].Fence.AttemptFenceEpoch != 3 ||
		!environment.cleanups[1].OwnsSharedRuntime {
		t.Fatalf("takeover cleanup = %#v", environment.cleanups)
	}
	if err := materializer.Prepare(context.Background(), newer); err != nil {
		t.Fatal(err)
	}

	if err := materializer.CleanupCandidate(context.Background(), candidateSpecFence(spec)); err != nil {
		t.Fatal(err)
	}
	staleCleanup := environment.cleanups[len(environment.cleanups)-1]
	if staleCleanup.Fence.AttemptFenceEpoch != 3 || staleCleanup.OwnsSharedRuntime {
		t.Fatalf("stale cleanup gained shared-runtime authority: %#v", staleCleanup)
	}
	newerWorkspace := filepath.Join(root, newer.AttemptID, "4", "workspace")
	if _, err := os.Stat(newerWorkspace); err != nil {
		t.Fatalf("stale cleanup removed newer workspace: %v", err)
	}
	marker, err := readVerificationRuntimeFence(filepath.Join(root, newer.AttemptID))
	if err != nil || marker != 4 {
		t.Fatalf("stale cleanup changed newer runtime marker: marker=%d err=%v", marker, err)
	}

	if err := materializer.CleanupCandidate(context.Background(), candidateSpecFence(newer)); err != nil {
		t.Fatal(err)
	}
	currentCleanup := environment.cleanups[len(environment.cleanups)-1]
	if currentCleanup.Fence.AttemptFenceEpoch != 4 || !currentCleanup.OwnsSharedRuntime {
		t.Fatalf("current cleanup did not own shared runtime: %#v", currentCleanup)
	}
}

func TestCandidateWorkspaceCleanupRetainsFenceUntilEnvironmentCleanupSucceeds(t *testing.T) {
	value := []byte("exact\n")
	tree, err := repository.NewTree([]repository.TreeFile{{
		Path: "apps/web/app.ts", Mode: "100644",
		ContentHash: verificationBytesHash(value), ByteSize: int64(len(value)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	environment := &candidateExecutionEnvironmentCleanupFake{}
	spec := verificationMaterializerSpec(t, tree.TreeHash, 7)
	materializer := &CandidateWorkspaceMaterializer{
		root: root, environment: environment,
		files: verificationFileResolverFake{values: map[string][]byte{verificationBytesHash(value): value}},
		resolveSnapshot: func(context.Context, CandidateExecutionSpec) (repository.TreeManifest, error) {
			return tree, nil
		},
	}
	if err := materializer.Materialize(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if err := materializer.Prepare(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	environment.err = errors.New("network removal failed")
	if err := materializer.CleanupCandidate(context.Background(), candidateSpecFence(spec)); !errors.Is(err, ErrCandidateMaterialization) {
		t.Fatalf("Candidate cleanup failure = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, spec.AttemptID, "7", "workspace")); err != nil {
		t.Fatalf("failed Candidate cleanup removed workspace evidence: %v", err)
	}
	if marker, err := readVerificationRuntimeFence(filepath.Join(root, spec.AttemptID)); err != nil || marker != 7 {
		t.Fatalf("failed Candidate cleanup removed runtime marker: marker=%d err=%v", marker, err)
	}
	environment.err = nil
	if err := materializer.CleanupCandidate(context.Background(), candidateSpecFence(spec)); err != nil {
		t.Fatalf("Candidate cleanup retry = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, spec.AttemptID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Candidate cleanup retry retained Attempt root: %v", err)
	}
}

func verificationMaterializerSpec(t *testing.T, treeHash string, fence uint64) CandidateExecutionSpec {
	t.Helper()
	compiled, err := (PlanCompiler{}).Compile(validCandidatePlanInput())
	if err != nil {
		t.Fatal(err)
	}
	compiled.Content.Subject.TreeHash = treeHash
	return CandidateExecutionSpec{
		RunID: uuid.NewString(), AttemptID: uuid.NewString(), AttemptFenceEpoch: fence,
		PlanID: uuid.NewString(), PlanHash: verificationTestHash("materializer-plan"),
		Content: compiled.Content,
	}
}

func verificationBytesHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func verificationTestHash(value string) string { return verificationBytesHash([]byte(value)) }
