package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

type facadeAccessFake struct {
	viewErr    error
	editErr    error
	controlErr error
}

func (access *facadeAccessFake) RequireProjectView(context.Context, string, string) error {
	return access.viewErr
}
func (access *facadeAccessFake) RequireProjectEdit(context.Context, string, string) error {
	return access.editErr
}
func (access *facadeAccessFake) RequireSandboxControl(context.Context, string, string) error {
	return access.controlErr
}

type facadeCandidatesFake struct {
	record repository.CandidateMutationRecord
	gets   int
}

func (store *facadeCandidatesFake) Get(context.Context, string, string) (repository.CandidateMutationRecord, error) {
	store.gets++
	return store.record, nil
}
func (store *facadeCandidatesFake) AcquireLease(
	context.Context, string, string, uint64, string, time.Duration,
) (repository.CandidateMutationRecord, error) {
	return store.record, nil
}
func (store *facadeCandidatesFake) RotateSession(
	context.Context, string, string, uint64, uint64, string,
) (repository.CandidateMutationRecord, error) {
	return store.record, nil
}
func (store *facadeCandidatesFake) CreateCheckpoint(
	context.Context, repository.CreateCheckpointInput,
) (repository.CandidateSnapshot, error) {
	return repository.CandidateSnapshot{}, errors.New("checkpoint not configured")
}
func (store *facadeCandidatesFake) Freeze(
	context.Context, string, string, uint64, uint64, uint64, string, string, string,
) (repository.CandidateMutationRecord, error) {
	return store.record, nil
}
func (store *facadeCandidatesFake) Abandon(
	context.Context, string, string, uint64, uint64, uint64, string, string, string,
) (repository.CandidateMutationRecord, error) {
	return store.record, nil
}

type facadeSessionStoreFake struct {
	session    SandboxSession
	candidates *facadeCandidatesFake
	now        time.Time
	syncErr    error
	syncCalls  int
}

func (store *facadeSessionStoreFake) ResolveProject(context.Context, string) (string, error) {
	return store.session.Snapshot().ProjectID, nil
}

func (store *facadeSessionStoreFake) Get(context.Context, string, string) (SandboxSession, error) {
	return store.session.Clone(), nil
}
func (store *facadeSessionStoreFake) SyncCandidate(
	_ context.Context,
	_, _ string,
	expectedVersion, expectedSessionEpoch uint64,
	_ string,
) (SandboxSession, error) {
	store.syncCalls++
	if store.syncErr != nil {
		return SandboxSession{}, store.syncErr
	}
	view := store.session.Snapshot()
	next, err := store.session.UpdateCandidate(
		expectedVersion, expectedSessionEpoch, view.Candidate.Version,
		store.candidates.record.Candidate, store.now,
	)
	if err != nil {
		return SandboxSession{}, err
	}
	store.session = next
	store.now = store.now.Add(time.Second)
	return next.Clone(), nil
}
func (store *facadeSessionStoreFake) AttachCheckpoint(
	context.Context, string, string, uint64, uint64, string, string,
) (SandboxSession, error) {
	return SandboxSession{}, errors.New("attach checkpoint not configured")
}

type facadeFilesFake struct {
	pointer   repository.FileBlobPointer
	puts      int
	resolves  int
	onResolve func()
}

func (files *facadeFilesFake) Put(
	_ context.Context,
	_, _ string,
	value []byte,
) (repository.FileBlobWriteResult, error) {
	files.puts++
	pointer := files.pointer
	pointer.ByteSize = int64(len(value))
	return repository.FileBlobWriteResult{Pointer: pointer}, nil
}
func (files *facadeFilesFake) Resolve(
	context.Context, string, string, int64,
) (repository.FileBlobPointer, []byte, error) {
	files.resolves++
	if files.onResolve != nil {
		files.onResolve()
	}
	return files.pointer, []byte("resolved"), nil
}

type facadeMutationFake struct {
	candidates *facadeCandidatesFake
	now        time.Time
	committed  *repository.MutationResult
	batch      *repository.BatchMutationResult
	calls      int
	batchCalls int
}

type facadeWorkspaceFake struct {
	sessions      *facadeSessionStoreFake
	err           error
	calls         int
	syncCallsSeen []int
	candidate     repository.CandidateWorkspace
	mutation      repository.MutationResult
	batch         repository.BatchMutationResult
	value         []byte
	batchCalls    int
}

func (workspace *facadeWorkspaceFake) SynchronizeBatch(
	_ context.Context,
	_ SessionView,
	candidate repository.CandidateWorkspace,
	mutation repository.BatchMutationResult,
) error {
	workspace.batchCalls++
	workspace.syncCallsSeen = append(workspace.syncCallsSeen, workspace.sessions.syncCalls)
	workspace.candidate = candidate
	workspace.batch = mutation
	return workspace.err
}

func (workspace *facadeWorkspaceFake) SynchronizeMutation(
	_ context.Context,
	_ SessionView,
	candidate repository.CandidateWorkspace,
	mutation repository.MutationResult,
	value []byte,
) error {
	workspace.calls++
	workspace.syncCallsSeen = append(workspace.syncCallsSeen, workspace.sessions.syncCalls)
	workspace.candidate = candidate
	workspace.mutation = mutation
	workspace.value = append([]byte(nil), value...)
	return workspace.err
}

func (service *facadeMutationFake) Apply(
	_ context.Context,
	principal repository.MutationPrincipal,
	input repository.ApplyMutationInput,
) (repository.MutationResult, error) {
	service.calls++
	if service.committed != nil {
		replayed := *service.committed
		replayed.Recovered = true
		return replayed, nil
	}
	before := service.candidates.record
	next, entry, err := before.Candidate.Apply(
		input.ExpectedCandidateVersion, input.ExpectedSessionEpoch, input.ExpectedWriterLeaseEpoch,
		principal.ActorID, principal.Attribution, input.Operation, service.now,
	)
	if err != nil {
		return repository.MutationResult{}, err
	}
	after := repository.TreeBlobPointer{
		Store: repository.TreeContentStore, Ref: "facade-after-tree", OwnerID: next.ID,
		TreeHash: next.CurrentTree.TreeHash, FileCount: len(next.CurrentTree.Files),
		ByteSize: 14, ContentObjectHash: sandboxDigest("7"),
	}
	result := repository.MutationResult{Entry: entry, BeforeTree: before.CurrentTreePointer, AfterTree: after}
	service.candidates.record = repository.CandidateMutationRecord{Candidate: next, CurrentTreePointer: after}
	service.committed = &result
	return result, nil
}

func (service *facadeMutationFake) ApplyBatch(
	_ context.Context,
	principal repository.MutationPrincipal,
	input repository.ApplyBatchMutationInput,
) (repository.BatchMutationResult, error) {
	service.batchCalls++
	if service.batch != nil {
		replayed := *service.batch
		replayed.Recovered = true
		return replayed, nil
	}
	before := service.candidates.record
	candidate := before.Candidate
	entries := make([]repository.JournalEntry, len(input.Operations))
	for index, operation := range input.Operations {
		var err error
		candidate, entries[index], err = candidate.Apply(
			candidate.Version, input.ExpectedSessionEpoch, input.ExpectedWriterLeaseEpoch,
			principal.ActorID, principal.Attribution, operation, service.now,
		)
		if err != nil {
			return repository.BatchMutationResult{}, err
		}
	}
	after := repository.TreeBlobPointer{
		Store: repository.TreeContentStore, Ref: "facade-batch-after-tree", OwnerID: candidate.ID,
		TreeHash: candidate.CurrentTree.TreeHash, FileCount: len(candidate.CurrentTree.Files),
		ByteSize: 28, ContentObjectHash: sandboxDigest("8"),
	}
	result := repository.BatchMutationResult{
		Entries: entries, BeforeTree: before.CurrentTreePointer, AfterTree: after,
		FinalCandidateVersion: candidate.Version,
	}
	service.candidates.record = repository.CandidateMutationRecord{Candidate: candidate, CurrentTreePointer: after}
	service.batch = &result
	return result, nil
}

func TestFacadeFileMutationReconcilesSessionAndReplaysExactly(t *testing.T) {
	facade, sessions, candidates, mutations, files, input := newFacadeMutationFixture(t)

	first, err := facade.MutateFile(context.Background(), input)
	if err != nil {
		t.Fatalf("first file mutation: %v", err)
	}
	if first.Session.Version != input.ExpectedSessionVersion+1 ||
		first.Session.Candidate.Version != input.ExpectedCandidateVersion+1 ||
		first.Mutation.Entry.Operation.ContentHash != files.pointer.ContentHash ||
		sessions.syncCalls != 1 || files.puts != 1 {
		t.Fatalf("first mutation did not advance exact projections: result=%#v sync=%d puts=%d",
			first, sessions.syncCalls, files.puts)
	}

	replayed, err := facade.MutateFile(context.Background(), input)
	if err != nil {
		t.Fatalf("replay file mutation: %v", err)
	}
	if !replayed.Mutation.Recovered || replayed.Session.Version != first.Session.Version ||
		sessions.syncCalls != 1 || mutations.calls != 2 {
		t.Fatalf("exact replay appended or resynced: result=%#v sync=%d calls=%d",
			replayed, sessions.syncCalls, mutations.calls)
	}
	// The file CAS object is content-addressed and may be safely deduplicated on
	// a transport retry; the journal and session projections remain exactly-once.
	if files.puts != 2 || candidates.record.Candidate.Version != first.Session.Candidate.Version {
		t.Fatalf("replay changed Candidate state: puts=%d candidate=%#v", files.puts, candidates.record.Candidate)
	}
}

func TestFacadeReadFileRequiresExactHeadAndRechecksAfterBlobResolve(t *testing.T) {
	base := cleanCandidate(t)
	session := readyTestSession(t, base, sandboxBaseTime)
	candidates := &facadeCandidatesFake{record: repository.CandidateMutationRecord{Candidate: base}}
	sessions := &facadeSessionStoreFake{session: session, candidates: candidates, now: sandboxBaseTime.Add(time.Second)}
	files := &facadeFilesFake{pointer: repository.FileBlobPointer{
		Store: repository.FileContentStore, Ref: "read-file", OwnerID: testProjectID,
		ContentHash: sandboxDigest("1"), ContentObjectHash: sandboxDigest("9"), ByteSize: 10,
	}}
	facade, err := NewFacade(sessions, candidates, &facadeMutationFake{candidates: candidates}, files, &facadeAccessFake{})
	if err != nil {
		t.Fatal(err)
	}
	view := session.Snapshot()
	input := ReadFileInput{
		ProjectID: testProjectID, SessionID: testSessionID, ActorID: testActorID,
		Path: "README.md", ExpectedSessionEpoch: view.SessionEpoch,
		ExpectedCandidateID: view.Candidate.ID, ExpectedCandidateVersion: view.Candidate.Version,
		ExpectedJournalSequence:  view.Candidate.JournalSequence,
		ExpectedWriterLeaseEpoch: view.Candidate.WriterLeaseEpoch,
		ExpectedTreeHash:         view.Candidate.TreeHash, ExpectedFileHash: sandboxDigest("1"),
	}

	result, err := facade.ReadFile(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Value) != "resolved" || result.File.Path != "README.md" ||
		result.Candidate.ID != input.ExpectedCandidateID || candidates.gets != 2 || files.resolves != 1 {
		t.Fatalf("exact file read did not close against the same head: result=%#v gets=%d resolves=%d", result, candidates.gets, files.resolves)
	}

	stale := input
	stale.ExpectedCandidateVersion++
	if _, err := facade.ReadFile(context.Background(), stale); !errors.Is(err, ErrFileHeadChanged) || files.resolves != 1 {
		t.Fatalf("stale opening head was not rejected before blob resolve: err=%v resolves=%d", err, files.resolves)
	}

	files.onResolve = func() {
		advanced, _, leaseErr := candidates.record.Candidate.AcquireLease(
			candidates.record.Candidate.Version, testActorID, time.Minute, sandboxBaseTime.Add(2*time.Second),
		)
		if leaseErr != nil {
			t.Fatalf("advance Candidate during read: %v", leaseErr)
		}
		candidates.record.Candidate = advanced
	}
	if _, err := facade.ReadFile(context.Background(), input); !errors.Is(err, ErrFileHeadChanged) {
		t.Fatalf("Candidate mutation during blob resolve was adopted: %v", err)
	}
}

func TestFacadeFileMutationRecoversAfterSessionSyncFailure(t *testing.T) {
	facade, sessions, candidates, _, _, input := newFacadeMutationFixture(t)
	syncFailure := errors.New("session projection database unavailable")
	sessions.syncErr = syncFailure

	committed, err := facade.MutateFile(context.Background(), input)
	if !errors.Is(err, ErrSessionReconciliation) || !errors.Is(err, syncFailure) {
		t.Fatalf("sync failure = %v, want reconciliation plus cause", err)
	}
	if committed.Mutation.Entry.CandidateTo != input.ExpectedCandidateVersion+1 ||
		candidates.record.Candidate.Version != input.ExpectedCandidateVersion+1 || sessions.session.Snapshot().Candidate.Version != input.ExpectedCandidateVersion {
		t.Fatalf("commit/sync ordering lost: result=%#v candidate=%d session=%d", committed,
			candidates.record.Candidate.Version, sessions.session.Snapshot().Candidate.Version)
	}

	sessions.syncErr = nil
	recovered, err := facade.MutateFile(context.Background(), input)
	if err != nil {
		t.Fatalf("recover committed mutation/session projection: %v", err)
	}
	if !recovered.Mutation.Recovered || recovered.Session.Candidate.Version != candidates.record.Candidate.Version ||
		sessions.syncCalls != 2 {
		t.Fatalf("recovery did not converge: result=%#v sync=%d", recovered, sessions.syncCalls)
	}
}

func TestFacadeFileMutationSynchronizesWorkspaceBeforeSessionProjection(t *testing.T) {
	_, sessions, candidates, mutations, files, input := newFacadeMutationFixture(t)
	workspace := &facadeWorkspaceFake{sessions: sessions}
	facade, err := NewFacade(sessions, candidates, mutations, files, &facadeAccessFake{}, workspace)
	if err != nil {
		t.Fatal(err)
	}

	result, err := facade.MutateFile(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.calls != 1 || len(workspace.syncCallsSeen) != 1 || workspace.syncCallsSeen[0] != 0 ||
		sessions.syncCalls != 1 || workspace.candidate.Version != result.Mutation.Entry.CandidateTo ||
		workspace.candidate.CurrentTree.TreeHash != result.Mutation.Entry.AfterTree ||
		string(workspace.value) != string(input.Value) {
		t.Fatalf("workspace/session commit ordering drifted: workspace=%#v result=%#v sync=%d", workspace, result, sessions.syncCalls)
	}
}

func TestFacadeFileMutationRecoversWorkspaceBeforeAdvancingSession(t *testing.T) {
	_, sessions, candidates, mutations, files, input := newFacadeMutationFixture(t)
	workspaceFailure := errors.New("workspace storage unavailable")
	workspace := &facadeWorkspaceFake{sessions: sessions, err: workspaceFailure}
	facade, err := NewFacade(sessions, candidates, mutations, files, &facadeAccessFake{}, workspace)
	if err != nil {
		t.Fatal(err)
	}

	committed, err := facade.MutateFile(context.Background(), input)
	if !errors.Is(err, ErrWorkspaceReconciliation) || !errors.Is(err, ErrSessionReconciliation) ||
		!errors.Is(err, workspaceFailure) {
		t.Fatalf("workspace failure = %v", err)
	}
	if committed.Mutation.Entry.CandidateTo != input.ExpectedCandidateVersion+1 || sessions.syncCalls != 0 ||
		candidates.record.Candidate.Version != input.ExpectedCandidateVersion+1 {
		t.Fatalf("workspace failure violated commit ordering: result=%#v sync=%d", committed, sessions.syncCalls)
	}

	workspace.err = nil
	recovered, err := facade.MutateFile(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Mutation.Recovered || workspace.calls != 2 || sessions.syncCalls != 1 ||
		len(workspace.syncCallsSeen) != 2 || workspace.syncCallsSeen[1] != 0 {
		t.Fatalf("workspace reconciliation did not converge before session sync: result=%#v workspace=%#v", recovered, workspace)
	}
}

func TestFacadeAgentBatchAdvancesCandidateAtomicallyAndSessionOnce(t *testing.T) {
	_, sessions, candidates, mutations, files, _ := newFacadeMutationFixture(t)
	workspace := &facadeWorkspaceFake{sessions: sessions}
	facade, err := NewFacade(sessions, candidates, mutations, files, &facadeAccessFake{}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	view := sessions.session.Snapshot()
	input := FileBatchMutationInput{
		ProjectID: testProjectID, SessionID: testSessionID, CandidateID: view.Candidate.ID,
		ActorID: testActorID, ExpectedSessionVersion: view.Version,
		ExpectedSessionEpoch: view.SessionEpoch, ExpectedCandidateVersion: view.Candidate.Version,
		ExpectedWriterLeaseEpoch: view.Candidate.WriterLeaseEpoch,
		Operations: []repository.FileOperation{
			{ID: "agent-batch-a", Kind: repository.OperationUpsert, Path: "src/a.ts", ContentHash: sandboxDigest("1"), ByteSize: 10, Mode: "100644"},
			{ID: "agent-batch-b", Kind: repository.OperationUpsert, Path: "src/b.ts", ContentHash: sandboxDigest("2"), ByteSize: 12, Mode: "100644"},
		},
	}
	first, err := facade.MutateAgentFiles(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Mutation.Entries) != 2 || first.Session.Version != view.Version+1 ||
		first.Session.Candidate.Version != view.Candidate.Version+2 || sessions.syncCalls != 1 ||
		workspace.batchCalls != 1 || workspace.syncCallsSeen[0] != 0 ||
		first.Mutation.Entries[0].Attribution != "agent" || first.Mutation.Entries[1].Attribution != "agent" {
		t.Fatalf("first Agent batch=%#v workspace=%#v sync=%d", first, workspace, sessions.syncCalls)
	}

	replayed, err := facade.MutateAgentFiles(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Mutation.Recovered || sessions.syncCalls != 1 || workspace.batchCalls != 2 ||
		mutations.batchCalls != 2 || candidates.record.Candidate.Version != first.Session.Candidate.Version {
		t.Fatalf("Agent batch replay=%#v workspace=%#v sync=%d", replayed, workspace, sessions.syncCalls)
	}
}

func newFacadeMutationFixture(t *testing.T) (
	*Facade,
	*facadeSessionStoreFake,
	*facadeCandidatesFake,
	*facadeMutationFake,
	*facadeFilesFake,
	FileMutationInput,
) {
	t.Helper()
	base := cleanCandidate(t)
	leased, _, err := base.AcquireLease(base.Version, testActorID, 20*time.Minute, sandboxBaseTime.Add(-30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	session := readyTestSession(t, leased, sandboxBaseTime)
	basePointer := repository.TreeBlobPointer{
		Store: repository.TreeContentStore, Ref: "facade-base-tree", OwnerID: leased.RepositorySnapshotID,
		TreeHash: leased.CurrentTree.TreeHash, FileCount: len(leased.CurrentTree.Files),
		ByteSize: 10, ContentObjectHash: sandboxDigest("6"),
	}
	candidates := &facadeCandidatesFake{record: repository.CandidateMutationRecord{
		Candidate: leased, CurrentTreePointer: basePointer,
	}}
	sessions := &facadeSessionStoreFake{
		session: session, candidates: candidates, now: sandboxBaseTime.Add(4 * time.Second),
	}
	mutations := &facadeMutationFake{candidates: candidates, now: sandboxBaseTime.Add(3 * time.Second)}
	files := &facadeFilesFake{pointer: repository.FileBlobPointer{
		Store: repository.FileContentStore, Ref: "facade-file", OwnerID: testCheckpoint,
		ContentHash: sandboxDigest("5"), ContentObjectHash: sandboxDigest("4"),
	}}
	facade, err := NewFacade(sessions, candidates, mutations, files, &facadeAccessFake{})
	if err != nil {
		t.Fatal(err)
	}
	view := session.Snapshot()
	input := FileMutationInput{
		ProjectID: testProjectID, SessionID: testSessionID, ActorID: testActorID,
		ExpectedSessionVersion: view.Version, ExpectedSessionEpoch: view.SessionEpoch,
		ExpectedCandidateVersion: view.Candidate.Version,
		ExpectedWriterLeaseEpoch: view.Candidate.WriterLeaseEpoch,
		OperationID:              "facade-create", Kind: repository.OperationUpsert,
		Path: "src/new.ts", Mode: "100644", Value: []byte("new bytes"),
	}
	return facade, sessions, candidates, mutations, files, input
}
