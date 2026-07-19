package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

type patchFileSourceFake struct {
	capsule TaskCapsule
	calls   int
}

func (source *patchFileSourceFake) GetTaskCapsule(
	context.Context, string, string,
) (TaskCapsule, error) {
	source.calls++
	return source.capsule, nil
}

type patchFileTreeFake struct {
	tree  repository.TreeManifest
	calls int
}

func (trees *patchFileTreeFake) ResolveExactTree(
	context.Context, TaskCapsule,
) (repository.TreeManifest, error) {
	trees.calls++
	return trees.tree, nil
}

type patchFileBlobFake struct {
	values        map[string][]byte
	calls         int
	tamperPointer bool
	tamperValue   bool
}

func (files *patchFileBlobFake) Resolve(
	_ context.Context,
	projectID, contentHash string,
	byteSize int64,
) (repository.FileBlobPointer, []byte, error) {
	files.calls++
	value, found := files.values[contentHash]
	if !found {
		return repository.FileBlobPointer{}, nil, repository.ErrFileBlobNotFound
	}
	pointer := repository.FileBlobPointer{
		Store: repository.FileContentStore, Ref: "patch-file-" + contentHash[7:19],
		OwnerID: uuid.NewString(), ContentHash: contentHash, ByteSize: byteSize,
		ContentObjectHash: testHash("f"),
	}
	if files.tamperPointer {
		pointer.ContentHash = testHash("e")
	}
	if files.tamperValue {
		value = []byte("tampered")
	}
	_ = projectID
	return pointer, append([]byte(nil), value...), nil
}

type patchFileReviewFixture struct {
	service *PatchFileReviewService
	attempt AgentAttempt
	actorID string
	access  *agentAccessFake
	source  *patchFileSourceFake
	trees   *patchFileTreeFake
	files   *patchFileBlobFake
	paths   struct {
		deleted string
		edited  string
		created string
	}
}

func TestPatchFileReviewReadsOnlyAuthorizedDeclaredBaseAndProposedBytes(t *testing.T) {
	fixture := newPatchFileReviewFixture(t)

	base, err := fixture.service.ReadPatchFile(
		context.Background(), fixture.attempt.ID, fixture.actorID,
		fixture.paths.edited, PatchFileBase,
	)
	if err != nil || !base.Exists || string(base.Value) != "before\n" ||
		base.ContentHash != rawPatchFileHash([]byte("before\n")) ||
		base.Mode != "100644" || base.ByteSize != 7 ||
		!sha256Pattern.MatchString(base.RepresentationHash) {
		t.Fatalf("base result=%#v err=%v", base, err)
	}
	proposed, err := fixture.service.ReadPatchFile(
		context.Background(), fixture.attempt.ID, fixture.actorID,
		fixture.paths.edited, PatchFileProposed,
	)
	if err != nil || !proposed.Exists || string(proposed.Value) != "after\n" ||
		proposed.ContentHash != rawPatchFileHash([]byte("after\n")) ||
		proposed.RepresentationHash == base.RepresentationHash {
		t.Fatalf("proposed result=%#v err=%v", proposed, err)
	}
	if fixture.access.viewCalls != 2 || fixture.source.calls != 2 ||
		fixture.trees.calls != 2 || fixture.files.calls != 2 {
		t.Fatalf("access/source/tree/file calls=%d/%d/%d/%d", fixture.access.viewCalls,
			fixture.source.calls, fixture.trees.calls, fixture.files.calls)
	}
}

func TestPatchFileReviewRepresentsDeclaredAbsenceWithoutBlobLookup(t *testing.T) {
	fixture := newPatchFileReviewFixture(t)
	createdBase, err := fixture.service.ReadPatchFile(
		context.Background(), fixture.attempt.ID, fixture.actorID,
		fixture.paths.created, PatchFileBase,
	)
	if err != nil || createdBase.Exists || createdBase.Value != nil ||
		createdBase.ContentHash != "" || createdBase.ByteSize != 0 {
		t.Fatalf("created base=%#v err=%v", createdBase, err)
	}
	deletedProposed, err := fixture.service.ReadPatchFile(
		context.Background(), fixture.attempt.ID, fixture.actorID,
		fixture.paths.deleted, PatchFileProposed,
	)
	if err != nil || deletedProposed.Exists || deletedProposed.Value != nil ||
		deletedProposed.ContentHash != "" || deletedProposed.ByteSize != 0 {
		t.Fatalf("deleted proposed=%#v err=%v", deletedProposed, err)
	}
	if fixture.files.calls != 0 {
		t.Fatalf("absent sides performed %d blob lookups", fixture.files.calls)
	}
}

func TestPatchFileReviewDoesNotBecomeAnArbitraryBlobOracle(t *testing.T) {
	fixture := newPatchFileReviewFixture(t)
	if _, err := fixture.service.ReadPatchFile(
		context.Background(), fixture.attempt.ID, fixture.actorID,
		"apps/web/features/conversation/not-in-patch.ts", PatchFileBase,
	); !errors.Is(err, ErrPatchFileUnavailable) {
		t.Fatalf("unknown patch path error=%v", err)
	}
	if fixture.files.calls != 0 {
		t.Fatalf("unknown path performed %d blob lookups", fixture.files.calls)
	}

	forbidden := errors.New("forbidden")
	fixture.access.err = forbidden
	if _, err := fixture.service.ReadPatchFile(
		context.Background(), fixture.attempt.ID, fixture.actorID,
		fixture.paths.edited, PatchFileSide("invalid"),
	); !errors.Is(err, forbidden) {
		t.Fatalf("authorization did not precede request details: %v", err)
	}
	if fixture.source.calls != 0 || fixture.trees.calls != 0 || fixture.files.calls != 0 {
		t.Fatalf("unauthorized request reached source/tree/files: %d/%d/%d",
			fixture.source.calls, fixture.trees.calls, fixture.files.calls)
	}
}

func TestPatchFileReviewFailsClosedOnBlobOrExactCapsuleDrift(t *testing.T) {
	fixture := newPatchFileReviewFixture(t)
	fixture.files.tamperPointer = true
	if _, err := fixture.service.ReadPatchFile(
		context.Background(), fixture.attempt.ID, fixture.actorID,
		fixture.paths.edited, PatchFileBase,
	); !errors.Is(err, ErrEvidenceIntegrity) {
		t.Fatalf("tampered pointer error=%v", err)
	}

	fixture = newPatchFileReviewFixture(t)
	fixture.source.capsule.ContentHash = testHash("d")
	if _, err := fixture.service.ReadPatchFile(
		context.Background(), fixture.attempt.ID, fixture.actorID,
		fixture.paths.edited, PatchFileBase,
	); !errors.Is(err, ErrEvidenceIntegrity) {
		t.Fatalf("drifted capsule error=%v", err)
	}
	if fixture.trees.calls != 0 || fixture.files.calls != 0 {
		t.Fatalf("drifted capsule reached tree/files: %d/%d", fixture.trees.calls, fixture.files.calls)
	}
}

func newPatchFileReviewFixture(t *testing.T) patchFileReviewFixture {
	t.Helper()
	fixture := newAgentFixture(t)
	paths := struct {
		deleted string
		edited  string
		created string
	}{
		deleted: "apps/web/features/conversation/delete.ts",
		edited:  "apps/web/features/conversation/edit.ts",
		created: "apps/web/features/conversation/new.ts",
	}
	baseValues := map[string][]byte{
		paths.deleted: []byte("remove\n"),
		paths.edited:  []byte("before\n"),
	}
	proposedValues := map[string][]byte{
		paths.edited:  []byte("after\n"),
		paths.created: []byte("created\n"),
	}
	base, err := repository.NewTree([]repository.TreeFile{
		{Path: paths.deleted, Mode: "100644", ContentHash: rawPatchFileHash(baseValues[paths.deleted]), ByteSize: int64(len(baseValues[paths.deleted]))},
		{Path: paths.edited, Mode: "100644", ContentHash: rawPatchFileHash(baseValues[paths.edited]), ByteSize: int64(len(baseValues[paths.edited]))},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.contextInput.BaseCandidateTreeHash = base.TreeHash
	fixture.taskInput.BaseCandidateTreeHash = base.TreeHash
	pack, err := NewContextPack(fixture.contextInput, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := NewTaskCapsule(fixture.taskInput, pack, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := NewAttempt(NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: fixture.actorID, Executor: testExecutor(),
	}, capsule, pack, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	operations := []repository.FileOperation{
		{ID: "delete-file", Kind: repository.OperationDelete, Path: paths.deleted, ExpectedHash: rawPatchFileHash(baseValues[paths.deleted])},
		{ID: "edit-file", Kind: repository.OperationUpsert, Path: paths.edited, ExpectedHash: rawPatchFileHash(baseValues[paths.edited]), ContentHash: rawPatchFileHash(proposedValues[paths.edited]), ByteSize: int64(len(proposedValues[paths.edited])), Mode: "100644"},
		{ID: "create-file", Kind: repository.OperationUpsert, Path: paths.created, ContentHash: rawPatchFileHash(proposedValues[paths.created]), ByteSize: int64(len(proposedValues[paths.created])), Mode: "100644"},
	}
	proposed := base
	var changedBytes int64
	for _, operation := range operations {
		proposed, err = repository.ApplyOperation(proposed, operation)
		if err != nil {
			t.Fatal(err)
		}
		if operation.Kind == repository.OperationUpsert {
			changedBytes += operation.ByteSize
		}
	}
	patch := PlatformPatch{
		SchemaVersion: PlatformPatchSchemaVersion, AttemptID: attempt.ID,
		ProjectID: attempt.ProjectID, CandidateID: attempt.CandidateID,
		TaskCapsule: attempt.TaskCapsule, ConfigurationHash: attempt.ConfigurationHash,
		BaseTreeHash: base.TreeHash, ProposedTreeHash: proposed.TreeHash,
		Operations: operations, ChangedBytes: changedBytes,
	}
	hash, err := domain.CanonicalHash(platformPatchPayload(patch))
	if err != nil {
		t.Fatal(err)
	}
	patch.ContentHash = "sha256:" + hash
	raw, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	reference := BlobReference{
		Store: AgentEvidenceStore, OwnerID: attempt.ID, Ref: uuid.NewString(),
		ContentHash: testHash("c"), ByteSize: int64(len(raw)),
	}
	attempt.Evidence.Patch = &reference
	access := &agentAccessFake{}
	review, err := NewReviewService(
		reviewStoreFake{attempt: attempt},
		&finalizedEvidenceReaderFake{value: raw},
		access,
	)
	if err != nil {
		t.Fatal(err)
	}
	source := &patchFileSourceFake{capsule: capsule}
	trees := &patchFileTreeFake{tree: base}
	values := make(map[string][]byte)
	for _, collection := range []map[string][]byte{baseValues, proposedValues} {
		for _, value := range collection {
			values[rawPatchFileHash(value)] = append([]byte(nil), value...)
		}
	}
	files := &patchFileBlobFake{values: values}
	service, err := NewPatchFileReviewService(review, source, trees, files)
	if err != nil {
		t.Fatal(err)
	}
	return patchFileReviewFixture{
		service: service, attempt: attempt, actorID: fixture.actorID,
		access: access, source: source, trees: trees, files: files, paths: paths,
	}
}
