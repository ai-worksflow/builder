package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/storage/content"
)

type contextContentReaderFake struct {
	stored content.StoredContent
}

func (reader contextContentReaderFake) Get(
	_ context.Context,
	ref, hash string,
) (content.StoredContent, error) {
	if reader.stored.ID != ref || reader.stored.ContentHash != hash {
		return content.StoredContent{}, content.ErrContentNotFound
	}
	return reader.stored, nil
}

type contextFileResolverFake struct {
	pointer repository.FileBlobPointer
	value   []byte
}

func (resolver contextFileResolverFake) Resolve(
	_ context.Context,
	_ string,
	hash string,
	size int64,
) (repository.FileBlobPointer, []byte, error) {
	if hash != resolver.pointer.ContentHash || size != resolver.pointer.ByteSize {
		return repository.FileBlobPointer{}, nil, repository.ErrFileBlobNotFound
	}
	return resolver.pointer, append([]byte(nil), resolver.value...), nil
}

type templateContextReaderFake struct{}

func (templateContextReaderFake) ReadExactTemplateManifest(
	context.Context,
	repository.ExactReference,
) ([]byte, error) {
	return []byte(`{"name":"unused"}`), nil
}

func TestContextMaterializerWritesExactInputIndexAndPrompt(t *testing.T) {
	fixture := newAgentFixture(t)
	contractPayload := []byte(`{"status":"ready"}`)
	contractOwner := uuid.NewString()
	contractRef := uuid.NewString()
	repositoryValue := []byte("export const ready = true\n")
	repositoryPointer := repository.FileBlobPointer{
		Store: repository.FileContentStore, Ref: uuid.NewString(), OwnerID: uuid.NewString(),
		ContentHash: rawWorktreeHash(repositoryValue), ByteSize: int64(len(repositoryValue)),
		ContentObjectHash: testHash("d"),
	}
	fixture.contextInput.Items = []ContextItem{
		{
			Key: "contract:root", Kind: ContextBuildContract,
			Source: &fixture.contextInput.BuildContract, Required: true,
			Content: BlobReference{
				Store: AgentEvidenceStore, OwnerID: contractOwner, Ref: contractRef,
				ContentHash: rawWorktreeHash(contractPayload), ByteSize: int64(len(contractPayload)),
			},
		},
		{
			Key: "repo:web", Kind: ContextRepositoryFile, Path: "apps/web/page.tsx", Required: true,
			Content: BlobReference{
				Store: "repository_file", OwnerID: repositoryPointer.OwnerID, Ref: repositoryPointer.Ref,
				ContentHash: repositoryPointer.ContentHash, ByteSize: repositoryPointer.ByteSize,
			},
		},
	}
	pack, err := NewContextPack(fixture.contextInput, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := NewTaskCapsule(fixture.taskInput, pack, fixture.now.Add(time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	executor := testExecutor()
	_, executor.PromptHash = QualifiedPromptTemplate()
	attempt, err := NewAttempt(NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: fixture.actorID, Executor: executor,
	}, capsule, pack, fixture.now.Add(2*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	attempt = runningAttempt(t, attempt, fixture.actorID, fixture.now)
	root := t.TempDir()
	lease := WorktreeLease{
		AttemptID: attempt.ID, Fence: attempt.FenceEpoch, Root: filepath.Join(root, "fence"),
	}
	lease.Input = filepath.Join(lease.Root, "input")
	lease.Output = filepath.Join(lease.Root, "output")
	lease.Workspace = filepath.Join(lease.Root, "workspace")
	for _, directory := range []string{lease.Input, lease.Output, lease.Workspace} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	materializer, err := NewContextMaterializer(
		contextContentReaderFake{stored: content.StoredContent{
			Reference: content.Reference{
				ID: contractRef, ContentHash: rawWorktreeHash(contractPayload),
				ByteSize: int64(len(contractPayload)), SchemaVersion: 1,
			},
			ProjectID: pack.ProjectID, AggregateType: "application_build_contract",
			AggregateID: contractOwner, State: content.StateFinalized,
			Payload: json.RawMessage(contractPayload),
		}},
		contextFileResolverFake{pointer: repositoryPointer, value: repositoryValue},
		templateContextReaderFake{},
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest, prompt, err := materializer.Materialize(
		context.Background(), attempt, capsule, pack, lease,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseMaterializedContext(manifest); err != nil {
		t.Fatalf("materialized context did not parse: %v", err)
	}
	if len(manifest.Entries) != 2 || manifest.Entries[0].InputPath == "" ||
		manifest.Entries[1].WorkspacePath != "/workspace/apps/web/page.tsx" {
		t.Fatalf("materialized entries = %#v", manifest.Entries)
	}
	if value, err := os.ReadFile(filepath.Join(lease.Input, "context", "items", "000.json")); err != nil ||
		string(value) != string(contractPayload) {
		t.Fatalf("materialized source=%q err=%v", value, err)
	}
	if _, err := os.Stat(filepath.Join(lease.Input, "context", "index.json")); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		capsule.ContentHash,
		pack.ContentHash,
		manifest.ContentHash,
		"Never change protectedPaths",
		"Return only output conforming",
		"Platform-qualified skill: frontend-resource-graph",
		"Never use emoji, emoticons, Unicode dingbats",
	} {
		if !strings.Contains(string(prompt), expected) {
			t.Fatalf("compiled prompt is missing %q", expected)
		}
	}
}

func TestContextMaterializerRejectsUnqualifiedPromptAndPendingFormalContent(t *testing.T) {
	fixture := codexRunnerFixture(t)
	materializer, _ := NewContextMaterializer(
		contextContentReaderFake{}, contextFileResolverFake{}, templateContextReaderFake{},
	)
	if _, _, err := materializer.Materialize(
		context.Background(), fixture.attempt, fixture.capsule, fixture.pack, fixture.lease,
	); err == nil || !strings.Contains(err.Error(), "prompt template") {
		t.Fatalf("unqualified prompt error = %v", err)
	}
}

func runningAttempt(t *testing.T, attempt AgentAttempt, actorID string, now time.Time) AgentAttempt {
	t.Helper()
	var err error
	attempt, _, err = attempt.Advance(AdvanceAttemptInput{
		ExpectedVersion: attempt.Version, ActorID: actorID,
		Target: AttemptReady, Reason: "ready",
	}, now.Add(3*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err = attempt.Advance(AdvanceAttemptInput{
		ExpectedVersion: attempt.Version, ActorID: actorID,
		Target: AttemptQueued, Reason: "queued",
	}, now.Add(4*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err = attempt.Claim(
		attempt.Version, actorID, "runner-a", 10*time.Minute, now.Add(5*time.Microsecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err = attempt.Advance(AdvanceAttemptInput{
		ExpectedVersion: attempt.Version, ExpectedFenceEpoch: attempt.FenceEpoch,
		ActorID: actorID, WorkerID: "runner-a", Target: AttemptRunning, Reason: "running",
	}, now.Add(6*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}
