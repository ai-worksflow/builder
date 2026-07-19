package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRepositorySnapshotReceiptIsCompactDeterministicAndTamperEvident(t *testing.T) {
	projectID, actorID, snapshotID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	tree, err := NewTree([]TreeFile{{
		Path: "apps/web/page.tsx", Mode: "100644",
		ContentHash: digestFixture("snapshot-page"), ByteSize: 17,
	}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := RepositorySnapshot{
		ID: snapshotID, ProjectID: projectID,
		BuildManifest:     ExactReference{ID: uuid.NewString(), ContentHash: strings.Repeat("a", 64)},
		BuildContract:     ExactReference{ID: uuid.NewString(), ContentHash: strings.Repeat("b", 64)},
		FullStackTemplate: ExactReference{ID: uuid.NewString(), ContentHash: digestFixture("snapshot-stack")},
		Tree:              tree, CreatedBy: actorID,
		CreatedAt: time.Date(2026, 7, 18, 21, 0, 0, 123000, time.UTC),
	}
	pointer := TreeBlobPointer{
		Store: TreeContentStore, Ref: "snapshot-tree-object", OwnerID: snapshotID,
		TreeHash: tree.TreeHash, FileCount: len(tree.Files), ByteSize: treeByteSize(tree),
		ContentObjectHash: digestFixture("snapshot-tree-object"),
	}
	components := []TemplateSourceComponent{
		repositorySnapshotComponentFixture("web", "apps/web", "1"),
		repositorySnapshotComponentFixture("api", "services/api", "2"),
	}
	first, err := NewRepositorySnapshotReceipt(snapshot, pointer, components)
	if err != nil {
		t.Fatalf("create RepositorySnapshot receipt: %v", err)
	}
	second, err := NewRepositorySnapshotReceipt(snapshot, pointer, []TemplateSourceComponent{components[1], components[0]})
	if err != nil {
		t.Fatalf("create reordered RepositorySnapshot receipt: %v", err)
	}
	if first.ContentHash != second.ContentHash || first.ContentHash == tree.TreeHash ||
		first.Snapshot.TemplateReleases[0].Role != "api" || first.Snapshot.TemplateReleases[1].Role != "web" {
		t.Fatalf("receipt is not deterministic and canonical: %#v / %#v", first, second)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("validate RepositorySnapshot receipt: %v", err)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"files"`) || !strings.Contains(string(encoded), `"contentObjectHash"`) {
		t.Fatalf("receipt duplicated the full tree or lost its compact commitment: %s", encoded)
	}

	for name, mutate := range map[string]func(*RepositorySnapshotReceipt){
		"receipt hash": func(value *RepositorySnapshotReceipt) { value.ContentHash = digestFixture("tampered") },
		"tree hash":    func(value *RepositorySnapshotReceipt) { value.Snapshot.Tree.TreeHash = digestFixture("tampered-tree") },
		"SBOM digest": func(value *RepositorySnapshotReceipt) {
			value.Snapshot.TemplateReleases[0].SBOMDigest = digestFixture("tampered-sbom")
		},
	} {
		t.Run(name, func(t *testing.T) {
			tampered := first
			tampered.Snapshot.TemplateReleases = append(
				[]RepositorySnapshotTemplateEvidence(nil), first.Snapshot.TemplateReleases...,
			)
			mutate(&tampered)
			if err := tampered.Validate(); !errors.Is(err, ErrRepositorySnapshotIntegrity) {
				t.Fatalf("tampered receipt error = %v", err)
			}
		})
	}
}

func TestRepositorySnapshotReadAuthorizesBeforeLookup(t *testing.T) {
	denied := errors.New("project view denied")
	service := &CandidateBootstrapService{access: bootstrapAccessFake{denied: denied}}
	_, err := service.GetSnapshot(
		context.Background(), uuid.NewString(), uuid.NewString(), digestFixture("snapshot"), uuid.NewString(),
	)
	if !errors.Is(err, denied) {
		t.Fatalf("Snapshot read error = %v, want authorization denial before storage", err)
	}
}

func TestRepositorySnapshotReadRejectsInvalidExactSelection(t *testing.T) {
	service := &CandidateBootstrapService{access: bootstrapAccessFake{}}
	_, err := service.GetSnapshot(
		context.Background(), uuid.NewString(), uuid.NewString(), "", uuid.NewString(),
	)
	if !errors.Is(err, ErrInvalidRepositorySnapshotSelection) || errors.Is(err, ErrBootstrapInvalid) {
		t.Fatalf("invalid Snapshot selection error = %v", err)
	}
}

func TestRepositorySnapshotContentSettlementIsRetryable(t *testing.T) {
	projectID, actorID, snapshotID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	files := &bootstrapFileWriterFake{}
	value := []byte("durable snapshot content")
	written, err := files.Put(context.Background(), projectID, actorID, value)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := NewTree([]TreeFile{{
		Path: "README.md", Mode: "100644",
		ContentHash: written.Pointer.ContentHash, ByteSize: int64(len(value)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	trees, err := NewTreeStore(newFakeTreeContentStore())
	if err != nil {
		t.Fatal(err)
	}
	pointer, err := trees.PutPending(context.Background(), projectID, snapshotID, tree)
	if err != nil {
		t.Fatal(err)
	}
	service := &CandidateBootstrapService{files: files, trees: trees}
	files.settleErr = errors.New("object store finalize unavailable")
	if err := service.settleRepositorySnapshotContent(
		context.Background(), projectID, pointer, tree,
	); err == nil {
		t.Fatal("pending file finalization unexpectedly completed")
	}
	files.settleErr = nil
	if err := service.settleRepositorySnapshotContent(
		context.Background(), projectID, pointer, tree,
	); err != nil {
		t.Fatalf("retry snapshot settlement: %v", err)
	}
	if len(files.settles) != 2 {
		t.Fatalf("file settlement attempts = %d, want 2", len(files.settles))
	}
}

func repositorySnapshotComponentFixture(role, mountPath, commitCharacter string) TemplateSourceComponent {
	return TemplateSourceComponent{
		Role: role, MountPath: mountPath,
		ReleaseID: uuid.NewString(), ReleaseContentHash: digestFixture(role + "-release"),
		ReleaseSubjectHash: digestFixture(role + "-subject"),
		Repository:         "https://github.com/ai-worksflow/templates.git", Branch: role,
		Commit: strings.Repeat(commitCharacter, 40), TreeHash: digestFixture(role + "-tree"),
		SBOMDigest:                  digestFixture(role + "-sbom"),
		SignatureBundleDigest:       digestFixture(role + "-signature"),
		AuthorityReceiptID:          uuid.NewString(),
		AuthorityReceiptContentHash: digestFixture(role + "-authority-receipt"),
		AuthorityPolicyHash:         digestFixture(role + "-authority-policy"),
	}
}
