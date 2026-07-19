package repository

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

const (
	mutationProjectID   = "10000000-0000-4000-8000-000000000001"
	mutationSnapshotID  = "10000000-0000-4000-8000-000000000002"
	mutationCandidateID = "10000000-0000-4000-8000-000000000003"
	mutationActorID     = "10000000-0000-4000-8000-000000000004"
	mutationManifestID  = "10000000-0000-4000-8000-000000000005"
	mutationContractID  = "10000000-0000-4000-8000-000000000006"
	mutationTemplateID  = "10000000-0000-4000-8000-000000000007"
	mutationHashA       = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mutationHashB       = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	mutationHashC       = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	mutationHashD       = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

type fakeCandidateMutationStore struct {
	record           CandidateMutationRecord
	committed        map[string]CommittedMutation
	loadCalls        int
	findCalls        int
	appendCalls      int
	batchAppendCalls int
	appendErr        error
	lastCommand      AppendOperationCommand
	lastBatch        []AppendOperationCommand
	maliciousAfter   *TreeBlobPointer
}

func (store *fakeCandidateMutationStore) AppendOperations(
	_ context.Context,
	commands []AppendOperationCommand,
) ([]CommittedMutation, error) {
	store.batchAppendCalls++
	store.lastBatch = append([]AppendOperationCommand(nil), commands...)
	if store.appendErr != nil {
		return nil, store.appendErr
	}
	if err := validateAppendOperationCommands(commands); err != nil {
		return nil, err
	}
	candidate := store.record.Candidate
	before := store.record.CurrentTreePointer
	committed := make([]CommittedMutation, len(commands))
	for index, command := range commands {
		if command.ProjectID != candidate.ProjectID || command.Entry.CandidateID != candidate.ID ||
			command.Entry.CandidateFrom != candidate.Version || command.BeforeTree != before {
			return nil, ErrCandidateState
		}
		value := CommittedMutation{
			ProjectID: command.ProjectID, Entry: command.Entry,
			BeforeTree: command.BeforeTree, AfterTree: command.AfterTree,
		}
		committed[index] = value
		candidate = command.CandidateAfter
		before = command.AfterTree
	}
	for index, command := range commands {
		store.committed[command.Entry.Operation.ID] = committed[index]
	}
	store.record = CandidateMutationRecord{Candidate: candidate, CurrentTreePointer: before}
	return committed, nil
}

func (store *fakeCandidateMutationStore) LoadMutationCandidate(
	_ context.Context,
	projectID, candidateID string,
) (CandidateMutationRecord, error) {
	store.loadCalls++
	if projectID != store.record.Candidate.ProjectID || candidateID != store.record.Candidate.ID {
		return CandidateMutationRecord{}, errors.New("candidate not found")
	}
	return store.record, nil
}

func (store *fakeCandidateMutationStore) FindCommittedOperation(
	_ context.Context,
	projectID, candidateID, operationID string,
) (CommittedMutation, bool, error) {
	store.findCalls++
	committed, found := store.committed[operationID]
	if found && (committed.ProjectID != projectID || committed.Entry.CandidateID != candidateID) {
		return CommittedMutation{}, false, errors.New("committed identity mismatch")
	}
	return committed, found, nil
}

func (store *fakeCandidateMutationStore) AppendOperation(
	_ context.Context,
	command AppendOperationCommand,
) (CommittedMutation, error) {
	store.appendCalls++
	store.lastCommand = command
	if store.appendErr != nil {
		return CommittedMutation{}, store.appendErr
	}
	candidate := store.record.Candidate
	if command.ProjectID != candidate.ProjectID || command.Entry.CandidateID != candidate.ID ||
		command.Entry.CandidateFrom != candidate.Version || command.Entry.SessionEpoch != candidate.SessionEpoch ||
		candidate.Lease == nil || command.Entry.LeaseEpoch != candidate.WriterLeaseEpoch ||
		command.Entry.ActorID != candidate.Lease.OwnerID || command.BeforeTree != store.record.CurrentTreePointer {
		return CommittedMutation{}, ErrCandidateState
	}
	committed := CommittedMutation{
		ProjectID: command.ProjectID, Entry: command.Entry,
		BeforeTree: command.BeforeTree, AfterTree: command.AfterTree,
	}
	if store.maliciousAfter != nil {
		committed.AfterTree = *store.maliciousAfter
	}
	store.committed[command.Entry.Operation.ID] = committed
	store.record = CandidateMutationRecord{Candidate: command.CandidateAfter, CurrentTreePointer: command.AfterTree}
	return committed, nil
}

type fakeMutationTreeStore struct {
	manifests      map[string]TreeManifest
	getCalls       int
	putCalls       int
	finalizeCalls  int
	abortCalls     int
	finalizeErrors int
	getOverride    *TreeManifest
	lastPutOwner   string
	lastPutTree    TreeManifest
	lastAborted    TreeBlobPointer
}

func (store *fakeMutationTreeStore) Get(
	_ context.Context,
	_, _ string,
	pointer TreeBlobPointer,
) (TreeManifest, error) {
	store.getCalls++
	if store.getOverride != nil {
		return cloneTree(*store.getOverride), nil
	}
	manifest, found := store.manifests[pointer.Ref]
	if !found {
		return TreeManifest{}, errors.New("tree not found")
	}
	return cloneTree(manifest), nil
}

type fakeMutationAuthorizer struct {
	calls     int
	projectID string
	actorID   string
	err       error
}

type fakeMutationFileResolver struct {
	calls       int
	projectID   string
	contentHash string
	byteSize    int64
	err         error
}

func (resolver *fakeMutationFileResolver) VerifyFileBlob(
	_ context.Context,
	projectID, contentHash string,
	byteSize int64,
) error {
	resolver.calls++
	resolver.projectID = projectID
	resolver.contentHash = contentHash
	resolver.byteSize = byteSize
	return resolver.err
}

func (authorizer *fakeMutationAuthorizer) RequireProjectEdit(
	_ context.Context,
	projectID, actorID string,
) error {
	authorizer.calls++
	authorizer.projectID = projectID
	authorizer.actorID = actorID
	return authorizer.err
}

func (store *fakeMutationTreeStore) PutPending(
	_ context.Context,
	_ string,
	ownerID string,
	manifest TreeManifest,
) (TreeBlobPointer, error) {
	store.putCalls++
	store.lastPutOwner = ownerID
	store.lastPutTree = cloneTree(manifest)
	pointer := TreeBlobPointer{
		Store: TreeContentStore, Ref: "pending-tree-" + manifest.TreeHash[7:19], OwnerID: ownerID,
		TreeHash: manifest.TreeHash, FileCount: len(manifest.Files), ByteSize: treeByteSize(manifest),
		ContentObjectHash: mutationHashD,
	}
	store.manifests[pointer.Ref] = cloneTree(manifest)
	return pointer, nil
}

func (store *fakeMutationTreeStore) Finalize(
	_ context.Context,
	_, _ string,
	_ TreeBlobPointer,
) error {
	store.finalizeCalls++
	if store.finalizeErrors > 0 {
		store.finalizeErrors--
		return errors.New("injected finalize crash")
	}
	return nil
}

func (store *fakeMutationTreeStore) Abort(
	_ context.Context,
	_, _ string,
	pointer TreeBlobPointer,
) error {
	store.abortCalls++
	store.lastAborted = pointer
	return nil
}

type fakePathPolicyResolver struct {
	policy  PathPolicy
	err     error
	subject PathPolicySubject
	calls   int
}

func (resolver *fakePathPolicyResolver) ResolvePathPolicy(
	_ context.Context,
	subject PathPolicySubject,
) (PathPolicy, error) {
	resolver.calls++
	resolver.subject = subject
	if resolver.err != nil {
		return PathPolicy{}, resolver.err
	}
	policy := resolver.policy
	if policy.Subject == (PathPolicySubject{}) {
		policy.Subject = subject
	}
	return policy, nil
}

type mutationServiceFixture struct {
	service    *MutationService
	candidates *fakeCandidateMutationStore
	trees      *fakeMutationTreeStore
	files      *fakeMutationFileResolver
	policies   *fakePathPolicyResolver
	access     *fakeMutationAuthorizer
	principal  MutationPrincipal
	input      ApplyMutationInput
	baseTree   TreeManifest
	now        time.Time
}

func newMutationServiceFixture(t *testing.T) mutationServiceFixture {
	t.Helper()
	baseTree, err := NewTree([]TreeFile{
		{Path: "README.md", ContentHash: mutationHashA, ByteSize: 8, Mode: "100644"},
		{Path: "frontend/src/existing.ts", ContentHash: mutationHashA, ByteSize: 12, Mode: "100644"},
	})
	if err != nil {
		t.Fatalf("create base tree: %v", err)
	}
	baseTime := time.Date(2026, 7, 16, 5, 0, 0, 0, time.UTC)
	snapshot := RepositorySnapshot{
		ID: mutationSnapshotID, ProjectID: mutationProjectID,
		BuildManifest:     ExactReference{ID: mutationManifestID, ContentHash: mutationHashA},
		BuildContract:     ExactReference{ID: mutationContractID, ContentHash: mutationHashB},
		FullStackTemplate: ExactReference{ID: mutationTemplateID, ContentHash: mutationHashC},
		Tree:              baseTree, CreatedBy: mutationActorID, CreatedAt: baseTime,
	}
	candidate, err := NewCandidate(mutationCandidateID, snapshot, mutationActorID, baseTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("create candidate: %v", err)
	}
	candidate, lease, err := candidate.AcquireLease(
		candidate.Version,
		mutationActorID,
		10*time.Minute,
		baseTime.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	basePointer := TreeBlobPointer{
		Store: TreeContentStore, Ref: "snapshot-base-tree", OwnerID: mutationSnapshotID,
		TreeHash: baseTree.TreeHash, FileCount: len(baseTree.Files), ByteSize: treeByteSize(baseTree),
		ContentObjectHash: mutationHashC,
	}
	candidates := &fakeCandidateMutationStore{
		record:    CandidateMutationRecord{Candidate: candidate, CurrentTreePointer: basePointer},
		committed: map[string]CommittedMutation{},
	}
	trees := &fakeMutationTreeStore{manifests: map[string]TreeManifest{basePointer.Ref: baseTree}}
	files := &fakeMutationFileResolver{}
	policies := &fakePathPolicyResolver{policy: PathPolicy{
		ExtensionPaths: []string{"frontend/src"}, ProtectedPaths: []string{"frontend/config"},
	}}
	access := &fakeMutationAuthorizer{}
	now := baseTime.Add(3 * time.Minute)
	service, err := NewMutationService(candidates, trees, files, policies, access, func() time.Time { return now })
	if err != nil {
		t.Fatalf("create mutation service: %v", err)
	}
	return mutationServiceFixture{
		service: service, candidates: candidates, trees: trees, files: files, policies: policies, access: access,
		principal: MutationPrincipal{ActorID: mutationActorID, Attribution: "agent"}, baseTree: baseTree, now: now,
		input: ApplyMutationInput{
			ProjectID: mutationProjectID, CandidateID: mutationCandidateID,
			ExpectedCandidateVersion: candidate.Version, ExpectedSessionEpoch: candidate.SessionEpoch,
			ExpectedWriterLeaseEpoch: lease.Epoch,
			Operation: FileOperation{
				ID: "op-1", Kind: OperationUpsert, Path: "frontend/src/new.ts",
				ContentHash: mutationHashB, ByteSize: 21, Mode: "100644",
			},
		},
	}
}

func TestMutationServiceDerivesAfterTreeAndExposesNoAfterPointerInput(t *testing.T) {
	fixture := newMutationServiceFixture(t)

	inputType := reflect.TypeOf(ApplyMutationInput{})
	pointerType := reflect.TypeOf(TreeBlobPointer{})
	manifestType := reflect.TypeOf(TreeManifest{})
	for index := 0; index < inputType.NumField(); index++ {
		field := inputType.Field(index)
		if field.Type == pointerType || field.Type == manifestType {
			t.Fatalf("client mutation input unexpectedly exposes server tree field %s", field.Name)
		}
		if field.Name == "ActorID" || field.Name == "Attribution" {
			t.Fatalf("client mutation input unexpectedly exposes server principal field %s", field.Name)
		}
	}

	result, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
	if err != nil {
		t.Fatalf("apply mutation: %v", err)
	}
	expected, err := ApplyOperation(fixture.baseTree, fixture.input.Operation)
	if err != nil {
		t.Fatalf("derive expected tree: %v", err)
	}
	if !equalTrees(fixture.trees.lastPutTree, expected) || result.AfterTree.TreeHash != expected.TreeHash {
		t.Fatalf("service did not derive the exact after tree: %+v", result.AfterTree)
	}
	if fixture.trees.lastPutOwner != mutationCandidateID || fixture.candidates.lastCommand.AfterTree != result.AfterTree {
		t.Fatalf("pending tree was not candidate-owned and atomically passed to the journal")
	}
	if fixture.candidates.lastCommand.BeforeTree.OwnerID != mutationSnapshotID {
		t.Fatalf("initial snapshot-owned before pointer was not preserved in CAS")
	}
	if fixture.trees.putCalls != 1 || fixture.candidates.appendCalls != 1 || fixture.trees.finalizeCalls != 1 || fixture.trees.abortCalls != 0 {
		t.Fatalf("unexpected write ordering: put=%d append=%d finalize=%d abort=%d",
			fixture.trees.putCalls, fixture.candidates.appendCalls, fixture.trees.finalizeCalls, fixture.trees.abortCalls)
	}
	if fixture.files.calls != 1 || fixture.files.projectID != mutationProjectID ||
		fixture.files.contentHash != mutationHashB || fixture.files.byteSize != 21 {
		t.Fatalf("upsert content was not verified through the tenant file catalog: %+v", fixture.files)
	}
	if result.Recovered || result.FinalizationPending {
		t.Fatalf("successful first apply has unexpected recovery flags: %+v", result)
	}
	if fixture.access.calls != 1 || fixture.access.projectID != mutationProjectID || fixture.access.actorID != mutationActorID {
		t.Fatalf("project edit authorization was not checked first: %+v", fixture.access)
	}
}

func TestMutationServiceAppliesOrRejectsWholeOrderedBatch(t *testing.T) {
	fixture := newMutationServiceFixture(t)
	input := ApplyBatchMutationInput{
		ProjectID: fixture.input.ProjectID, CandidateID: fixture.input.CandidateID,
		ExpectedCandidateVersion: fixture.input.ExpectedCandidateVersion,
		ExpectedSessionEpoch:     fixture.input.ExpectedSessionEpoch,
		ExpectedWriterLeaseEpoch: fixture.input.ExpectedWriterLeaseEpoch,
		Operations: []FileOperation{
			{
				ID: "batch-op-1", Kind: OperationUpsert, Path: "frontend/src/a.ts",
				ContentHash: mutationHashB, ByteSize: 12, Mode: "100644",
			},
			{
				ID: "batch-op-2", Kind: OperationUpsert, Path: "frontend/src/b.ts",
				ContentHash: mutationHashC, ByteSize: 13, Mode: "100644",
			},
		},
	}
	result, err := fixture.service.ApplyBatch(context.Background(), fixture.principal, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 || fixture.candidates.batchAppendCalls != 1 ||
		fixture.candidates.appendCalls != 0 || fixture.trees.putCalls != 2 ||
		fixture.trees.finalizeCalls != 2 || fixture.trees.abortCalls != 0 ||
		result.FinalCandidateVersion != input.ExpectedCandidateVersion+2 ||
		fixture.candidates.record.Candidate.Version != input.ExpectedCandidateVersion+2 {
		t.Fatalf("batch result=%#v candidates=%#v trees=%#v", result, fixture.candidates, fixture.trees)
	}
	if result.BeforeTree.TreeHash != fixture.baseTree.TreeHash ||
		result.AfterTree.TreeHash != fixture.candidates.record.Candidate.CurrentTree.TreeHash ||
		result.Entries[1].BeforeTree != result.Entries[0].AfterTree {
		t.Fatalf("batch journal is not one exact tree chain: %#v", result)
	}

	failed := newMutationServiceFixture(t)
	failed.candidates.appendErr = ErrCandidateState
	beforeVersion := failed.candidates.record.Candidate.Version
	if _, err := failed.service.ApplyBatch(context.Background(), failed.principal, input); !errors.Is(err, ErrCandidateState) {
		t.Fatalf("rejected batch error = %v", err)
	}
	if failed.candidates.record.Candidate.Version != beforeVersion ||
		len(failed.candidates.committed) != 0 || failed.trees.abortCalls != 2 {
		t.Fatalf("rejected batch left a partial commit: candidate=%#v committed=%#v aborts=%d",
			failed.candidates.record.Candidate, failed.candidates.committed, failed.trees.abortCalls)
	}
}

func TestMutationServiceAuthorizesBeforeAnyStoreLookupOrBlobSideEffect(t *testing.T) {
	fixture := newMutationServiceFixture(t)
	errForbidden := errors.New("project edit forbidden")
	fixture.access.err = errForbidden

	_, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
	if !errors.Is(err, errForbidden) {
		t.Fatalf("expected authorization error, got %v", err)
	}
	if fixture.access.calls != 1 || fixture.candidates.findCalls != 0 || fixture.candidates.loadCalls != 0 ||
		fixture.policies.calls != 0 || fixture.files.calls != 0 || fixture.trees.getCalls != 0 || fixture.trees.putCalls != 0 ||
		fixture.trees.finalizeCalls != 0 || fixture.trees.abortCalls != 0 {
		t.Fatalf("unauthorized request crossed a store boundary: access=%d find=%d load=%d policy=%d get=%d put=%d finalize=%d abort=%d",
			fixture.access.calls, fixture.candidates.findCalls, fixture.candidates.loadCalls, fixture.policies.calls,
			fixture.trees.getCalls, fixture.trees.putCalls, fixture.trees.finalizeCalls, fixture.trees.abortCalls)
	}
}

func TestMutationServiceRejectsUnregisteredUpsertContentBeforeTreeWrite(t *testing.T) {
	fixture := newMutationServiceFixture(t)
	missing := errors.New("file content is not registered")
	fixture.files.err = missing

	_, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
	if !errors.Is(err, missing) {
		t.Fatalf("expected file catalog error, got %v", err)
	}
	if fixture.files.calls != 1 || fixture.trees.putCalls != 0 || fixture.candidates.appendCalls != 0 ||
		fixture.trees.finalizeCalls != 0 || fixture.trees.abortCalls != 0 {
		t.Fatalf("unregistered bytes reached tree persistence: files=%d put=%d append=%d finalize=%d abort=%d",
			fixture.files.calls, fixture.trees.putCalls, fixture.candidates.appendCalls,
			fixture.trees.finalizeCalls, fixture.trees.abortCalls)
	}
}

func TestMutationServiceDeleteDoesNotRequireNewFileBlob(t *testing.T) {
	fixture := newMutationServiceFixture(t)
	fixture.input.Operation = FileOperation{
		ID: "op-delete", Kind: OperationDelete, Path: "frontend/src/existing.ts", ExpectedHash: mutationHashA,
	}
	if _, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input); err != nil {
		t.Fatalf("delete mutation: %v", err)
	}
	if fixture.files.calls != 0 {
		t.Fatalf("delete unexpectedly required a new file blob %d times", fixture.files.calls)
	}
}

func TestMutationServiceClientCannotSpoofAgentAttribution(t *testing.T) {
	fixture := newMutationServiceFixture(t)
	encoded := []byte(`{
		"projectId":"` + mutationProjectID + `",
		"candidateId":"` + mutationCandidateID + `",
		"expectedCandidateVersion":2,
		"expectedSessionEpoch":1,
		"expectedWriterLeaseEpoch":1,
		"actorId":"10000000-0000-4000-8000-000000000099",
		"attribution":"user",
		"operation":{"id":"spoof-op","kind":"file.upsert","path":"docs/spoof.md","contentHash":"` + mutationHashB + `","byteSize":7,"mode":"100644"}
	}`)
	var input ApplyMutationInput
	if err := json.Unmarshal(encoded, &input); err != nil {
		t.Fatalf("decode transport-shaped input: %v", err)
	}
	principalJSON, err := json.Marshal(fixture.principal)
	if err != nil || string(principalJSON) != "{}" {
		t.Fatalf("server principal must not be JSON-controlled: payload=%s err=%v", principalJSON, err)
	}

	_, err = fixture.service.Apply(context.Background(), fixture.principal, input)
	if !errors.Is(err, ErrPathPolicyDenied) {
		t.Fatalf("server agent attribution must win over spoofed JSON: %v", err)
	}
	if fixture.trees.putCalls != 0 || fixture.candidates.appendCalls != 0 {
		t.Fatalf("spoofed agent mutation wrote state")
	}
}

func TestMutationServiceRejectsStaleVersionAndEpochFencesBeforeWriting(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ApplyMutationInput)
		want   error
	}{
		{name: "candidate version", mutate: func(input *ApplyMutationInput) { input.ExpectedCandidateVersion-- }, want: ErrCandidateState},
		{name: "session epoch", mutate: func(input *ApplyMutationInput) { input.ExpectedSessionEpoch++ }, want: ErrLeaseFenced},
		{name: "writer lease epoch", mutate: func(input *ApplyMutationInput) { input.ExpectedWriterLeaseEpoch++ }, want: ErrLeaseFenced},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMutationServiceFixture(t)
			input := fixture.input
			test.mutate(&input)
			_, err := fixture.service.Apply(context.Background(), fixture.principal, input)
			if !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
			if fixture.trees.putCalls != 0 || fixture.candidates.appendCalls != 0 {
				t.Fatalf("stale fence wrote state: put=%d append=%d", fixture.trees.putCalls, fixture.candidates.appendCalls)
			}
		})
	}
}

func TestMutationServiceRejectsHydratedTreeBlobDrift(t *testing.T) {
	fixture := newMutationServiceFixture(t)
	drifted, err := NewTree([]TreeFile{{Path: "README.md", ContentHash: mutationHashB, ByteSize: 9, Mode: "100644"}})
	if err != nil {
		t.Fatalf("create drifted tree: %v", err)
	}
	fixture.trees.getOverride = &drifted

	_, err = fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
	if !errors.Is(err, ErrCandidateTreeDrift) {
		t.Fatalf("expected blob drift rejection, got %v", err)
	}
	if fixture.trees.putCalls != 0 || fixture.candidates.appendCalls != 0 {
		t.Fatalf("tree drift wrote state")
	}
}

func TestMutationServiceEnforcesExactAttributionPathPolicy(t *testing.T) {
	for _, attribution := range []string{"user", "agent", "merge", "restore"} {
		t.Run("protected-"+attribution, func(t *testing.T) {
			fixture := newMutationServiceFixture(t)
			fixture.principal.Attribution = attribution
			fixture.input.Operation.Path = "frontend/config/runtime.ts"
			_, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
			if !errors.Is(err, ErrPathPolicyDenied) {
				t.Fatalf("protected path must reject %s attribution: %v", attribution, err)
			}
		})
	}

	t.Run("agent outside extension", func(t *testing.T) {
		fixture := newMutationServiceFixture(t)
		fixture.input.Operation.Path = "docs/generated.md"
		_, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
		if !errors.Is(err, ErrPathPolicyDenied) {
			t.Fatalf("expected extension path denial, got %v", err)
		}
	})

	t.Run("agent rename source outside extension", func(t *testing.T) {
		fixture := newMutationServiceFixture(t)
		fixture.input.Operation = FileOperation{
			ID: "op-rename", Kind: OperationRename, FromPath: "README.md", Path: "frontend/src/README.md",
			ExpectedHash: mutationHashA,
		}
		_, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
		if !errors.Is(err, ErrPathPolicyDenied) {
			t.Fatalf("rename must authorize source and target, got %v", err)
		}
	})

	t.Run("user outside extension", func(t *testing.T) {
		fixture := newMutationServiceFixture(t)
		fixture.principal.Attribution = "user"
		fixture.input.Operation.Path = "docs/generated.md"
		if _, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input); err != nil {
			t.Fatalf("user should be allowed outside extension paths: %v", err)
		}
	})

	t.Run("resolver exact subject drift", func(t *testing.T) {
		fixture := newMutationServiceFixture(t)
		fixture.policies.policy.Subject = pathPolicySubject(fixture.candidates.record.Candidate)
		fixture.policies.policy.Subject.FullStackTemplate.ContentHash = mutationHashA
		_, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
		if !errors.Is(err, ErrPathPolicyDrift) {
			t.Fatalf("expected fail-closed exact policy drift, got %v", err)
		}
	})
}

func TestMutationServiceAbortsPendingTreeWhenAtomicAppendLosesCAS(t *testing.T) {
	fixture := newMutationServiceFixture(t)
	fixture.candidates.appendErr = ErrCandidateState

	_, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
	if !errors.Is(err, ErrCandidateState) {
		t.Fatalf("expected append CAS error, got %v", err)
	}
	if fixture.trees.putCalls != 1 || fixture.candidates.appendCalls != 1 || fixture.trees.abortCalls != 1 || fixture.trees.finalizeCalls != 0 {
		t.Fatalf("append failure ordering is wrong: put=%d append=%d abort=%d finalize=%d",
			fixture.trees.putCalls, fixture.candidates.appendCalls, fixture.trees.abortCalls, fixture.trees.finalizeCalls)
	}
	if fixture.trees.lastAborted.OwnerID != mutationCandidateID {
		t.Fatalf("service aborted the wrong pending tree: %+v", fixture.trees.lastAborted)
	}
}

func TestMutationServiceNeverAbortsTreeWhenAppendOutcomeIsUnknown(t *testing.T) {
	fixture := newMutationServiceFixture(t)
	fixture.candidates.appendErr = errors.Join(ErrAppendOutcomeUnknown, errors.New("commit acknowledgement lost"))

	result, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
	if !errors.Is(err, ErrMutationReconciliation) || !errors.Is(err, ErrAppendOutcomeUnknown) ||
		!result.FinalizationPending {
		t.Fatalf("unknown append result=%+v error=%v", result, err)
	}
	if fixture.trees.putCalls != 1 || fixture.candidates.appendCalls != 1 ||
		fixture.trees.abortCalls != 0 || fixture.trees.finalizeCalls != 0 {
		t.Fatalf("unknown append outcome was destructively handled: put=%d append=%d abort=%d finalize=%d",
			fixture.trees.putCalls, fixture.candidates.appendCalls,
			fixture.trees.abortCalls, fixture.trees.finalizeCalls)
	}
}

func TestMutationServiceRetryFinalizesCommittedTreeAfterCrash(t *testing.T) {
	fixture := newMutationServiceFixture(t)
	fixture.trees.finalizeErrors = 1

	first, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
	if !errors.Is(err, ErrTreeFinalizationPending) || !first.FinalizationPending {
		t.Fatalf("expected committed-but-pending result, result=%+v err=%v", first, err)
	}
	if fixture.trees.abortCalls != 0 {
		t.Fatalf("committed tree must never be aborted after finalize failure")
	}

	second, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
	if err != nil {
		t.Fatalf("recover finalize on exact retry: %v", err)
	}
	if !second.Recovered || second.FinalizationPending || second.AfterTree != first.AfterTree {
		t.Fatalf("unexpected recovery result: %+v", second)
	}
	if fixture.trees.putCalls != 1 || fixture.candidates.appendCalls != 1 || fixture.trees.finalizeCalls != 2 {
		t.Fatalf("recovery duplicated a mutation: put=%d append=%d finalize=%d",
			fixture.trees.putCalls, fixture.candidates.appendCalls, fixture.trees.finalizeCalls)
	}
	if fixture.files.calls != 2 {
		t.Fatalf("first apply and recovery must both verify file bytes, got %d calls", fixture.files.calls)
	}
}

func TestMutationServiceRecoveryFailsClosedWhenCommittedFileBlobDisappears(t *testing.T) {
	fixture := newMutationServiceFixture(t)
	fixture.trees.finalizeErrors = 1
	first, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
	if !errors.Is(err, ErrTreeFinalizationPending) {
		t.Fatalf("commit operation before injected crash: result=%+v err=%v", first, err)
	}
	fixture.files.err = ErrFileBlobNotFound

	_, err = fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
	if !errors.Is(err, ErrMutationReconciliation) || !errors.Is(err, ErrFileBlobNotFound) {
		t.Fatalf("missing committed bytes did not block recovery: %v", err)
	}
	if fixture.trees.finalizeCalls != 1 || fixture.trees.putCalls != 1 || fixture.candidates.appendCalls != 1 {
		t.Fatalf("missing bytes recovery performed a side effect: finalize=%d put=%d append=%d",
			fixture.trees.finalizeCalls, fixture.trees.putCalls, fixture.candidates.appendCalls)
	}
}

func TestMutationServiceRecoveryRejectsHiddenDeltaInCommittedAfterBlob(t *testing.T) {
	fixture := newMutationServiceFixture(t)
	fixture.trees.finalizeErrors = 1

	first, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
	if !errors.Is(err, ErrTreeFinalizationPending) {
		t.Fatalf("commit operation before injected crash: result=%+v err=%v", first, err)
	}
	hidden, err := NewTree([]TreeFile{
		{Path: "README.md", ContentHash: mutationHashA, ByteSize: 8, Mode: "100644"},
		{Path: "frontend/src/existing.ts", ContentHash: mutationHashA, ByteSize: 12, Mode: "100644"},
		{Path: "frontend/src/hidden.ts", ContentHash: mutationHashC, ByteSize: 99, Mode: "100644"},
		{Path: "frontend/src/new.ts", ContentHash: mutationHashB, ByteSize: 21, Mode: "100644"},
	})
	if err != nil {
		t.Fatalf("create corrupted committed after tree: %v", err)
	}
	fixture.trees.manifests[first.AfterTree.Ref] = hidden

	_, err = fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
	if !errors.Is(err, ErrMutationReconciliation) {
		t.Fatalf("expected hidden delta recovery rejection, got %v", err)
	}
	if fixture.trees.finalizeCalls != 1 {
		t.Fatalf("corrupted committed after blob was finalized")
	}
	if fixture.trees.putCalls != 1 || fixture.candidates.appendCalls != 1 {
		t.Fatalf("corruption recovery duplicated the mutation")
	}
}

func TestMutationServiceRejectsOperationIDReplayWithDifferentPayload(t *testing.T) {
	fixture := newMutationServiceFixture(t)
	if _, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input); err != nil {
		t.Fatalf("commit initial operation: %v", err)
	}
	replay := fixture.input
	replay.Operation.ContentHash = mutationHashC

	_, err := fixture.service.Apply(context.Background(), fixture.principal, replay)
	if !errors.Is(err, ErrOperationReplay) {
		t.Fatalf("expected operation replay rejection, got %v", err)
	}
	if fixture.trees.putCalls != 1 || fixture.candidates.appendCalls != 1 || fixture.trees.finalizeCalls != 1 {
		t.Fatalf("mismatched replay performed side effects")
	}
}

func TestMutationServiceRejectsMaliciousAfterPointerReturnedByStore(t *testing.T) {
	fixture := newMutationServiceFixture(t)
	fixture.candidates.maliciousAfter = &TreeBlobPointer{
		Store: TreeContentStore, Ref: "attacker-tree", OwnerID: mutationCandidateID,
		TreeHash: mutationHashA, FileCount: 0, ByteSize: 0, ContentObjectHash: mutationHashB,
	}

	result, err := fixture.service.Apply(context.Background(), fixture.principal, fixture.input)
	if !errors.Is(err, ErrMutationStoreContract) {
		t.Fatalf("expected malicious committed pointer rejection, got %v", err)
	}
	if !errors.Is(err, ErrMutationReconciliation) || !result.FinalizationPending {
		t.Fatalf("malicious committed result must require reconciliation: result=%+v err=%v", result, err)
	}
	if fixture.trees.finalizeCalls != 0 || fixture.trees.abortCalls != 0 {
		t.Fatalf("reported commit must be left reachable for reconciliation")
	}
}
