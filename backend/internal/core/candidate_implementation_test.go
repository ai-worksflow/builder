package core

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
)

type candidateImplementationFilesFake struct {
	values map[string][]byte
	err    error
}

func (files candidateImplementationFilesFake) Resolve(
	_ context.Context,
	_, contentHash string,
	byteSize int64,
) (repository.FileBlobPointer, []byte, error) {
	if files.err != nil {
		return repository.FileBlobPointer{}, nil, files.err
	}
	value := append([]byte(nil), files.values[contentHash]...)
	return repository.FileBlobPointer{
		ContentHash: contentHash,
		ByteSize:    byteSize,
	}, value, nil
}

func TestCandidateImplementationOperationsProduceCompleteExactDiff(t *testing.T) {
	keep := []byte("keep")
	script := []byte("#!/bin/sh\necho exact\n")
	created := []byte("export const exact = true\n")
	tree, err := repository.NewTree([]repository.TreeFile{
		candidateTreeFile("keep.txt", "100644", keep),
		candidateTreeFile("src/new.ts", "100644", created),
		candidateTreeFile("tools/run.sh", "100755", script),
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace := map[string]any{
		"files": []any{
			map[string]any{"path": "keep.txt", "content": string(keep)},
			map[string]any{"path": "obsolete.md", "content": "remove me", "mode": "100644"},
			map[string]any{"path": "tools/run.sh", "content": string(script), "mode": "100644"},
		},
	}
	values := map[string][]byte{
		hashBytes(keep):    keep,
		hashBytes(created): created,
		hashBytes(script):  script,
	}

	operations, err := candidateImplementationOperations(
		context.Background(),
		"project-1",
		"checkpoint-1",
		tree,
		workspace,
		candidateImplementationFilesFake{values: values},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 3 {
		t.Fatalf("complete Candidate diff has %d operations: %#v", len(operations), operations)
	}
	createdOperation := operations[0]
	if createdOperation.Kind != "file.upsert" ||
		createdOperation.Path != "src/new.ts" ||
		createdOperation.Content == nil ||
		*createdOperation.Content != string(created) ||
		createdOperation.Mode != "100644" ||
		createdOperation.ExpectedHash != "" ||
		createdOperation.Language != "typescript" {
		t.Fatalf("new file operation lost exact content or mode: %#v", createdOperation)
	}
	modeOperation := operations[1]
	if modeOperation.Kind != "file.upsert" ||
		modeOperation.Path != "tools/run.sh" ||
		modeOperation.Content == nil ||
		*modeOperation.Content != string(script) ||
		modeOperation.Mode != "100755" ||
		modeOperation.ExpectedHash != hashBytes(script) {
		t.Fatalf("mode-only change was not preserved exactly: %#v", modeOperation)
	}
	deleteOperation := operations[2]
	if deleteOperation.Kind != "file.delete" ||
		deleteOperation.Path != "obsolete.md" ||
		deleteOperation.ExpectedHash != hashBytes([]byte("remove me")) {
		t.Fatalf("deleted base file was omitted or unguarded: %#v", deleteOperation)
	}
	for index, operation := range operations {
		if len(operation.TraceSource) != 1 ||
			operation.TraceSource[0] != "candidate-snapshot:checkpoint-1" ||
			operation.Rationale != "Freeze exact CandidateSnapshot checkpoint-1" {
			t.Fatalf("operation %d lost CandidateSnapshot traceability: %#v", index, operation)
		}
	}
}

func TestCandidateImplementationOperationsRejectTamperedAndBinaryBlobs(t *testing.T) {
	t.Run("tree pointer mismatch", func(t *testing.T) {
		expected := []byte("expected")
		tree, err := repository.NewTree([]repository.TreeFile{
			candidateTreeFile("README.md", "100644", expected),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = candidateImplementationOperations(
			context.Background(),
			"project-1",
			"checkpoint-1",
			tree,
			map[string]any{"files": []any{}},
			candidateImplementationFilesFake{
				values: map[string][]byte{hashBytes(expected): []byte("tampered")},
			},
		)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("tampered file bytes were accepted: %v", err)
		}
	})

	t.Run("binary file", func(t *testing.T) {
		value := []byte{0, 1, 2}
		tree, err := repository.NewTree([]repository.TreeFile{
			candidateTreeFile("asset.bin", "100644", value),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = candidateImplementationOperations(
			context.Background(),
			"project-1",
			"checkpoint-1",
			tree,
			map[string]any{"files": []any{}},
			candidateImplementationFilesFake{
				values: map[string][]byte{hashBytes(value): value},
			},
		)
		if !errors.Is(err, ErrBlockingGate) {
			t.Fatalf("binary file entered a text Proposal: %v", err)
		}
	})
}

func TestTemplateOnlyCandidateImplementationBuildsCompleteFirstRevision(t *testing.T) {
	packageFile := []byte(`{"name":"template-app","private":true}`)
	sourceFile := []byte("export const ready = true\n")
	tree, err := repository.NewTree([]repository.TreeFile{
		candidateTreeFile("package.json", "100644", packageFile),
		candidateTreeFile("src/main.ts", "100644", sourceFile),
	})
	if err != nil {
		t.Fatal(err)
	}

	operations, err := candidateImplementationOperations(
		context.Background(),
		"project-1",
		"checkpoint-1",
		tree,
		map[string]any{"files": []any{}},
		candidateImplementationFilesFake{values: map[string][]byte{
			hashBytes(packageFile): packageFile,
			hashBytes(sourceFile):  sourceFile,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != len(tree.Files) {
		t.Fatalf("first revision has %d operations, want one per exact template file: %#v", len(operations), operations)
	}
	for _, operation := range operations {
		if operation.Kind != "file.upsert" || operation.ExpectedHash != "" || operation.Content == nil {
			t.Fatalf("first revision operation is not an unguarded create: %#v", operation)
		}
	}
	applied, err := applyFileOperations(map[string]any{"files": []any{}}, operations)
	if err != nil {
		t.Fatal(err)
	}
	appliedTree, err := candidateWorkspaceTree(applied)
	if err != nil {
		t.Fatal(err)
	}
	if appliedTree.TreeHash != tree.TreeHash {
		t.Fatalf("first revision tree = %s, exact Candidate tree = %s", appliedTree.TreeHash, tree.TreeHash)
	}
}

func TestCandidateImplementationTemplateBaselineRequiresMatchingEmptyWorkbenchLeaf(t *testing.T) {
	manifestID := uuid.New().String()
	candidateID := uuid.New().String()
	contractID := uuid.New().String()
	verificationID := uuid.New().String()
	manifestHash := hashBytes([]byte("manifest"))
	contractHash := hashBytes([]byte("contract"))
	identity := CandidateImplementationIdentity{
		CandidateID: candidateID,
		BuildManifest: repository.ExactReference{
			ID: manifestID, ContentHash: manifestHash,
		},
		BuildContract: repository.ExactReference{
			ID: contractID, ContentHash: contractHash,
		},
		TreePointer: repository.TreeBlobPointer{
			OwnerID: candidateID, TreeHash: hashBytes([]byte("candidate-tree")),
		},
		BaseTreeHash: hashBytes([]byte("template-tree")),
		VerificationReceipt: repository.ExactReference{
			ID: verificationID, ContentHash: hashBytes([]byte("verification")),
		},
	}
	bundle := WorkbenchBundle{ID: manifestID, ManifestHash: manifestHash}
	contract := ApplicationBuildContractRef{ID: contractID, ContractHash: contractHash}
	if err := validateCandidateImplementationCreate(bundle, contract, identity); err != nil {
		t.Fatalf("exact template RepositorySnapshot baseline was rejected: %v", err)
	}

	workspace := VersionRef{
		ArtifactID: uuid.New().String(), RevisionID: uuid.New().String(),
		ContentHash: hashBytes([]byte("workspace")),
	}
	bundle.CurrentWorkspaceRevision = &workspace
	if err := validateCandidateImplementationCreate(bundle, contract, identity); !errors.Is(err, ErrProposalStale) {
		t.Fatalf("template baseline was rebound to an existing WorkspaceRevision: %v", err)
	}
}

func TestCandidateImplementationReceiptBaseUsesAllOrNoneShape(t *testing.T) {
	if !candidateImplementationReceiptBaseMatches(storage.CandidateImplementationFreezeModel{}, nil) {
		t.Fatal("nil WorkspaceRevision receipt did not match the template RepositorySnapshot baseline")
	}

	artifactID, revisionID := uuid.New(), uuid.New()
	contentHash := hashBytes([]byte("workspace"))
	reference := &VersionRef{
		ArtifactID: artifactID.String(), RevisionID: revisionID.String(), ContentHash: contentHash,
	}
	complete := storage.CandidateImplementationFreezeModel{
		BaseWorkspaceArtifactID:  &artifactID,
		BaseWorkspaceRevisionID:  &revisionID,
		BaseWorkspaceContentHash: &contentHash,
	}
	if !candidateImplementationReceiptBaseMatches(complete, reference) {
		t.Fatal("complete exact WorkspaceRevision receipt was rejected")
	}
	partial := complete
	partial.BaseWorkspaceRevisionID = nil
	if candidateImplementationReceiptBaseMatches(partial, nil) ||
		candidateImplementationReceiptBaseMatches(partial, reference) {
		t.Fatal("partial WorkspaceRevision receipt shape was accepted")
	}
}

func TestCandidateWorkspaceTreeUsesContentAndExecutableMode(t *testing.T) {
	workspace := map[string]any{
		"files": []any{
			map[string]any{"path": "plain.txt", "content": "plain"},
			map[string]any{"path": "tools/run.sh", "content": "run", "mode": "100755"},
		},
	}
	tree, err := candidateWorkspaceTree(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Files) != 2 ||
		tree.Files[0].Mode != "100644" ||
		tree.Files[1].Mode != "100755" ||
		tree.Files[1].ContentHash != hashBytes([]byte("run")) {
		t.Fatalf("workspace did not form the exact semantic tree: %#v", tree)
	}
}

func TestCandidateWorkspaceTreeAfterApplyingFileOperations(t *testing.T) {
	updated := "updated\n"
	created := "created\n"
	workspace := map[string]any{
		"files": []any{
			map[string]any{"path": "existing.txt", "content": "existing\n", "revision": float64(1)},
		},
	}
	applied, err := applyFileOperations(workspace, []FileOperation{
		{
			ID: "update", Kind: "file.upsert", Path: "existing.txt", Content: &updated,
			Mode: "100755", ExpectedHash: hashText("existing\n"),
		},
		{
			ID: "create", Kind: "file.upsert", Path: "created.txt", Content: &created,
			Mode: "100644",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := candidateWorkspaceTree(applied)
	if err != nil {
		t.Fatal(err)
	}
	want, err := repository.NewTree([]repository.TreeFile{
		candidateTreeFile("created.txt", "100644", []byte(created)),
		candidateTreeFile("existing.txt", "100755", []byte(updated)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if tree.TreeHash != want.TreeHash {
		t.Fatalf("applied workspace tree = %#v, want %#v", tree, want)
	}
}

func candidateTreeFile(path, mode string, value []byte) repository.TreeFile {
	return repository.TreeFile{
		Path:        path,
		Mode:        mode,
		ContentHash: hashBytes(value),
		ByteSize:    int64(len(value)),
	}
}
