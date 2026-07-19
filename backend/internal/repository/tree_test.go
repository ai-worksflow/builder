package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTreeIsDeterministicAndOperationsAreCASBound(t *testing.T) {
	t.Parallel()

	first, err := NewTree([]TreeFile{
		{Path: "services/api/main.py", ContentHash: testDigest("api"), ByteSize: 3},
		{Path: "apps/web/src/main.tsx", ContentHash: testDigest("web"), ByteSize: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewTree([]TreeFile{first.Files[1], first.Files[0]})
	if err != nil {
		t.Fatal(err)
	}
	if first.TreeHash != second.TreeHash || first.Files[0].Path != "apps/web/src/main.tsx" {
		t.Fatalf("tree is not canonical: %#v %#v", first, second)
	}
	updated, err := ApplyOperation(first, FileOperation{
		ID: "edit-web", Kind: OperationUpsert, Path: "apps/web/src/main.tsx",
		ExpectedHash: testDigest("web"), ContentHash: testDigest("web-v2"), ByteSize: 6,
	})
	if err != nil || updated.TreeHash == first.TreeHash {
		t.Fatalf("upsert = %#v, %v", updated, err)
	}
	if _, err := ApplyOperation(first, FileOperation{
		ID: "stale", Kind: OperationUpsert, Path: "apps/web/src/main.tsx",
		ExpectedHash: testDigest("other"), ContentHash: testDigest("web-v2"), ByteSize: 6,
	}); !errors.Is(err, ErrTreeConflict) {
		t.Fatalf("stale expected hash error = %v", err)
	}
	renamed, err := ApplyOperation(updated, FileOperation{
		ID: "rename", Kind: OperationRename, FromPath: "services/api/main.py", Path: "services/api/app.py",
		ExpectedHash: testDigest("api"),
	})
	if err != nil || renamed.Files[1].Path != "services/api/app.py" {
		t.Fatalf("rename = %#v, %v", renamed, err)
	}
}

func TestTreeRejectsProtectedAndUnboundedFiles(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		".git/config", ".env", "apps/web/node_modules/pkg.js", "../escape", "/absolute",
		"src/line\nbreak.ts", "src/tab\tname.ts", "src/carriage\rreturn.ts",
	} {
		_, err := NewTree([]TreeFile{{Path: target, ContentHash: testDigest(target), ByteSize: 1}})
		if !errors.Is(err, ErrProtectedPath) && !errors.Is(err, ErrInvalidTree) {
			t.Fatalf("path %q error = %v", target, err)
		}
	}
	_, err := NewTree([]TreeFile{{Path: "large.bin", ContentHash: testDigest("large"), ByteSize: MaxFileBytes + 1}})
	if !errors.Is(err, ErrTreeLimit) {
		t.Fatalf("large file error = %v", err)
	}
}

func TestFileOperationsRequireExactExistingFilePreconditions(t *testing.T) {
	t.Parallel()

	tree, err := NewTree([]TreeFile{{
		Path: "src/existing.ts", ContentHash: testDigest("existing"), ByteSize: 8,
	}})
	if err != nil {
		t.Fatal(err)
	}

	for _, operation := range []FileOperation{
		{ID: "blind-update", Kind: OperationUpsert, Path: "src/existing.ts", ContentHash: testDigest("next"), ByteSize: 4},
		{ID: "blind-delete", Kind: OperationDelete, Path: "src/existing.ts"},
		{ID: "blind-rename", Kind: OperationRename, FromPath: "src/existing.ts", Path: "src/renamed.ts"},
	} {
		if _, err := ApplyOperation(tree, operation); !errors.Is(err, ErrFilePrecondition) {
			t.Fatalf("%s without expected hash error = %v", operation.Kind, err)
		}
	}

	created, err := ApplyOperation(tree, FileOperation{
		ID: "create", Kind: OperationUpsert, Path: "src/new.ts", ContentHash: testDigest("new"), ByteSize: 3,
	})
	if err != nil || len(created.Files) != 2 {
		t.Fatalf("create with explicit expected absence = %#v, %v", created, err)
	}
	if _, err := ApplyOperation(tree, FileOperation{
		ID: "stale-create", Kind: OperationUpsert, Path: "src/missing.ts",
		ExpectedHash: testDigest("old"), ContentHash: testDigest("new"), ByteSize: 3,
	}); !errors.Is(err, ErrTreeConflict) {
		t.Fatalf("create carrying an existing-file precondition error = %v", err)
	}
}

func TestCandidateLeaseFencesWritersAndCheckpointPinsTree(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 5, 0, 0, 0, time.UTC)
	projectID, snapshotID := uuid.NewString(), uuid.NewString()
	manifestID, contractID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	tree, err := NewTree([]TreeFile{{Path: "README.md", ContentHash: testDigest("base"), ByteSize: 4}})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := NewCandidate(uuid.NewString(), RepositorySnapshot{
		ID: snapshotID, ProjectID: projectID,
		BuildManifest:     ExactReference{ID: manifestID, ContentHash: testDigest("manifest")},
		BuildContract:     ExactReference{ID: contractID, ContentHash: testDigest("contract")},
		FullStackTemplate: ExactReference{ID: uuid.NewString(), ContentHash: testDigest("stack")},
		Tree:              tree, CreatedBy: actorID, CreatedAt: now.Add(-time.Second),
	}, actorID, now)
	if err != nil {
		t.Fatal(err)
	}
	candidate, lease, err := candidate.AcquireLease(candidate.Version, actorID, 5*time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := candidate.Apply(candidate.Version, candidate.SessionEpoch, lease.Epoch+1, actorID, "user", FileOperation{
		ID: "edit", Kind: OperationUpsert, Path: "README.md", ExpectedHash: testDigest("base"), ContentHash: testDigest("next"), ByteSize: 4,
	}, now.Add(2*time.Second)); !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("stale epoch error = %v", err)
	}
	if _, _, err := candidate.Apply(candidate.Version, candidate.SessionEpoch+1, lease.Epoch, actorID, "user", FileOperation{
		ID: "edit", Kind: OperationUpsert, Path: "README.md", ExpectedHash: testDigest("base"), ContentHash: testDigest("next"), ByteSize: 4,
	}, now.Add(2*time.Second)); !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("stale session epoch error = %v", err)
	}
	next, entry, err := candidate.Apply(candidate.Version, candidate.SessionEpoch, lease.Epoch, actorID, "user", FileOperation{
		ID: "edit", Kind: OperationUpsert, Path: "README.md", ExpectedHash: testDigest("base"), ContentHash: testDigest("next"), ByteSize: 4,
	}, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if entry.BeforeTree != tree.TreeHash || entry.AfterTree != next.CurrentTree.TreeHash || entry.Sequence != 1 ||
		entry.SessionEpoch != candidate.SessionEpoch {
		t.Fatalf("journal entry = %#v", entry)
	}
	if !next.Dirty || next.SessionEpoch != 1 || entry.Operation.Mode != "100644" {
		t.Fatalf("candidate/journal normalization = %#v, %#v", next, entry)
	}
	checkpoint, err := next.Checkpoint(
		next.Version, next.SessionEpoch, next.WriterLeaseEpoch,
		uuid.NewString(), actorID, "autosave", now.Add(3*time.Second),
	)
	if err != nil || checkpoint.Tree.TreeHash != next.CurrentTree.TreeHash || checkpoint.JournalSequence != 1 {
		t.Fatalf("checkpoint = %#v, %v", checkpoint, err)
	}
}

func TestCandidateLeaseEpochRemainsMonotonicWithoutAnActiveLease(t *testing.T) {
	now := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)
	actorID := uuid.NewString()
	tree, err := NewTree([]TreeFile{{Path: "README.md", ContentHash: testDigest("base"), ByteSize: 4}})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := NewCandidate(uuid.NewString(), RepositorySnapshot{
		ID: uuid.NewString(), ProjectID: uuid.NewString(),
		BuildManifest:     ExactReference{ID: uuid.NewString(), ContentHash: testDigest("manifest")},
		BuildContract:     ExactReference{ID: uuid.NewString(), ContentHash: testDigest("contract")},
		FullStackTemplate: ExactReference{ID: uuid.NewString(), ContentHash: testDigest("stack")},
		Tree:              tree, CreatedBy: actorID, CreatedAt: now.Add(-time.Second),
	}, actorID, now)
	if err != nil {
		t.Fatal(err)
	}
	candidate.WriterLeaseEpoch = 7
	candidate.Version = 8
	candidate.Lease = nil
	next, lease, err := candidate.AcquireLease(candidate.Version, actorID, time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if lease.Epoch != 8 || next.WriterLeaseEpoch != 8 || next.Lease == nil || next.Lease.Epoch != 8 {
		t.Fatalf("lease epoch restarted instead of advancing: next=%#v lease=%#v", next, lease)
	}
}

func testDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func TestTreeCanonicalizesHashPrefixAndRejectsPathWhitespace(t *testing.T) {
	t.Parallel()

	digest := testDigest("canonical")
	tree, err := NewTree([]TreeFile{{Path: "src/app.ts", ContentHash: digest[len("sha256:"):], ByteSize: 1}})
	if err != nil || tree.Files[0].ContentHash != digest {
		t.Fatalf("canonical hash = %#v, %v", tree, err)
	}
	if _, err := ParseTree(TreeManifest{SchemaVersion: tree.SchemaVersion, TreeHash: tree.TreeHash, Files: []TreeFile{{
		Path: "src/app.ts", ContentHash: digest[len("sha256:"):], ByteSize: 1, Mode: "100644",
	}}}); !errors.Is(err, ErrInvalidTree) {
		t.Fatalf("non-canonical persisted hash error = %v", err)
	}
	if _, err := NormalizePath(" src/app.ts"); !errors.Is(err, ErrInvalidTree) {
		t.Fatalf("path whitespace error = %v", err)
	}
}
