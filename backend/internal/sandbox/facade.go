package sandbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

var (
	ErrSessionProjectionStale = errors.New("sandbox session Candidate projection is stale")
	ErrSessionReconciliation  = errors.New("sandbox Candidate mutation committed but session reconciliation is pending")
	ErrFileNotInTree          = errors.New("repository file is not present in the Candidate tree")
	ErrFileHeadChanged        = errors.New("sandbox file read Candidate head changed")
)

type ProjectAuthorizer interface {
	RequireProjectView(context.Context, string, string) error
	RequireProjectEdit(context.Context, string, string) error
	RequireSandboxControl(context.Context, string, string) error
}

type CandidateControls interface {
	Get(context.Context, string, string) (repository.CandidateMutationRecord, error)
	AcquireLease(context.Context, string, string, uint64, string, time.Duration) (repository.CandidateMutationRecord, error)
	RotateSession(context.Context, string, string, uint64, uint64, string) (repository.CandidateMutationRecord, error)
	CreateCheckpoint(context.Context, repository.CreateCheckpointInput) (repository.CandidateSnapshot, error)
	Freeze(context.Context, string, string, uint64, uint64, uint64, string, string, string) (repository.CandidateMutationRecord, error)
	Abandon(context.Context, string, string, uint64, uint64, uint64, string, string, string) (repository.CandidateMutationRecord, error)
}

type CandidateMutationService interface {
	Apply(context.Context, repository.MutationPrincipal, repository.ApplyMutationInput) (repository.MutationResult, error)
}

type CandidateBatchMutationService interface {
	ApplyBatch(
		context.Context,
		repository.MutationPrincipal,
		repository.ApplyBatchMutationInput,
	) (repository.BatchMutationResult, error)
}

type RepositoryFileBlobs interface {
	Put(context.Context, string, string, []byte) (repository.FileBlobWriteResult, error)
	Resolve(context.Context, string, string, int64) (repository.FileBlobPointer, []byte, error)
}

type SessionStore interface {
	ResolveProject(context.Context, string) (string, error)
	Get(context.Context, string, string) (SandboxSession, error)
	SyncCandidate(context.Context, string, string, uint64, uint64, string) (SandboxSession, error)
	AttachCheckpoint(context.Context, string, string, uint64, uint64, string, string) (SandboxSession, error)
}

func (facade *Facade) ResolveProject(
	ctx context.Context,
	sessionID, actorID string,
) (string, error) {
	projectID, err := facade.sessions.ResolveProject(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if err := facade.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return "", fmt.Errorf("authorize sandbox project: %w", err)
	}
	return projectID, nil
}

type Facade struct {
	sessions       SessionStore
	candidates     CandidateControls
	mutations      CandidateMutationService
	batchMutations CandidateBatchMutationService
	files          RepositoryFileBlobs
	access         ProjectAuthorizer
	workspace      WorkspaceMutationSynchronizer
	batchWorkspace WorkspaceBatchMutationSynchronizer
}

func NewFacade(
	sessions SessionStore,
	candidates CandidateControls,
	mutations CandidateMutationService,
	files RepositoryFileBlobs,
	access ProjectAuthorizer,
	workspace ...WorkspaceMutationSynchronizer,
) (*Facade, error) {
	if sessions == nil || candidates == nil || mutations == nil || files == nil || access == nil {
		return nil, errors.New("sandbox session, Candidate, mutation, file, and access services are required")
	}
	if len(workspace) > 1 || (len(workspace) == 1 && workspace[0] == nil) {
		return nil, errors.New("at most one non-nil sandbox workspace synchronizer is allowed")
	}
	var synchronizer WorkspaceMutationSynchronizer
	if len(workspace) == 1 {
		synchronizer = workspace[0]
	}
	batchMutations, _ := mutations.(CandidateBatchMutationService)
	batchWorkspace, _ := synchronizer.(WorkspaceBatchMutationSynchronizer)
	return &Facade{
		sessions: sessions, candidates: candidates, mutations: mutations,
		batchMutations: batchMutations, files: files, access: access,
		workspace: synchronizer, batchWorkspace: batchWorkspace,
	}, nil
}

func (facade *Facade) Get(
	ctx context.Context,
	projectID, sessionID, actorID string,
) (SessionView, error) {
	if err := facade.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return SessionView{}, fmt.Errorf("authorize sandbox view: %w", err)
	}
	session, err := facade.sessions.Get(ctx, projectID, sessionID)
	if err != nil {
		return SessionView{}, err
	}
	return session.Snapshot(), nil
}

type RepositoryView struct {
	Session   SessionView                   `json:"session"`
	Candidate repository.CandidateWorkspace `json:"candidate"`
	Tree      repository.TreeManifest       `json:"tree"`
}

func (facade *Facade) Tree(
	ctx context.Context,
	projectID, sessionID, actorID string,
) (RepositoryView, error) {
	session, record, err := facade.authoritativeRepository(ctx, projectID, sessionID, actorID, false)
	if err != nil {
		return RepositoryView{}, err
	}
	return RepositoryView{
		Session: session.Snapshot(), Candidate: record.Candidate, Tree: record.Candidate.CurrentTree,
	}, nil
}

type FileView struct {
	Session     SessionView                   `json:"session"`
	Candidate   repository.CandidateWorkspace `json:"candidate"`
	File        repository.TreeFile           `json:"file"`
	ContentType string                        `json:"contentType"`
	Value       []byte                        `json:"-"`
}

// ReadFileInput binds an ordinary file read to the same exact Candidate head
// that selected the tree entry in the browser. Every field is required. The
// service checks the head both before and after resolving the immutable blob so
// a concurrent Candidate mutation cannot turn a successful response into an
// implicit adoption of stale bytes.
type ReadFileInput struct {
	ProjectID                string
	SessionID                string
	ActorID                  string
	Path                     string
	ExpectedSessionEpoch     uint64
	ExpectedCandidateID      string
	ExpectedCandidateVersion uint64
	ExpectedJournalSequence  uint64
	ExpectedWriterLeaseEpoch uint64
	ExpectedTreeHash         string
	ExpectedFileHash         string
}

func (facade *Facade) ReadFile(
	ctx context.Context,
	input ReadFileInput,
) (FileView, error) {
	if ctx == nil || !validUUID(input.ProjectID) || !validUUID(input.SessionID) ||
		!validUUID(input.ActorID) || !validUUID(input.ExpectedCandidateID) ||
		input.ExpectedSessionEpoch == 0 || input.ExpectedCandidateVersion == 0 ||
		!validDigest(input.ExpectedTreeHash) || !validDigest(input.ExpectedFileHash) {
		return FileView{}, ErrInvalidSession
	}
	normalizedPath, err := repository.NormalizePath(input.Path)
	if err != nil {
		return FileView{}, err
	}
	session, record, err := facade.authoritativeRepository(
		ctx, input.ProjectID, input.SessionID, input.ActorID, false,
	)
	if err != nil {
		return FileView{}, err
	}
	if !fileReadHeadMatches(session.Snapshot(), record.Candidate, input) {
		return FileView{}, ErrFileHeadChanged
	}
	var file repository.TreeFile
	found := false
	for _, candidate := range record.Candidate.CurrentTree.Files {
		if candidate.Path == normalizedPath {
			file = candidate
			found = true
			break
		}
	}
	if !found {
		return FileView{}, ErrFileNotInTree
	}
	if file.ContentHash != input.ExpectedFileHash {
		return FileView{}, ErrFileHeadChanged
	}
	_, value, err := facade.files.Resolve(ctx, input.ProjectID, file.ContentHash, file.ByteSize)
	if err != nil {
		return FileView{}, fmt.Errorf("resolve Candidate file bytes: %w", err)
	}
	closingSession, closingRecord, err := facade.authoritativeRepository(
		ctx, input.ProjectID, input.SessionID, input.ActorID, false,
	)
	if err != nil {
		if errors.Is(err, ErrSessionProjectionStale) {
			return FileView{}, errors.Join(ErrFileHeadChanged, err)
		}
		return FileView{}, err
	}
	closingView := closingSession.Snapshot()
	if !fileReadHeadMatches(closingView, closingRecord.Candidate, input) ||
		!treeContainsExactFile(closingRecord.Candidate.CurrentTree.Files, file) {
		return FileView{}, ErrFileHeadChanged
	}
	return FileView{
		Session: closingView, Candidate: closingRecord.Candidate, File: file,
		ContentType: "application/octet-stream", Value: value,
	}, nil
}

func fileReadHeadMatches(
	session SessionView,
	candidate repository.CandidateWorkspace,
	input ReadFileInput,
) bool {
	return session.ProjectID == input.ProjectID && session.ID == input.SessionID &&
		session.SessionEpoch == input.ExpectedSessionEpoch &&
		session.Candidate.ID == input.ExpectedCandidateID &&
		session.Candidate.Version == input.ExpectedCandidateVersion &&
		session.Candidate.JournalSequence == input.ExpectedJournalSequence &&
		session.Candidate.WriterLeaseEpoch == input.ExpectedWriterLeaseEpoch &&
		session.Candidate.TreeHash == input.ExpectedTreeHash &&
		candidate.ProjectID == input.ProjectID && candidate.ID == input.ExpectedCandidateID &&
		candidate.Version == input.ExpectedCandidateVersion &&
		candidate.JournalSequence == input.ExpectedJournalSequence &&
		candidate.SessionEpoch == input.ExpectedSessionEpoch &&
		candidate.WriterLeaseEpoch == input.ExpectedWriterLeaseEpoch &&
		candidate.CurrentTree.TreeHash == input.ExpectedTreeHash
}

func treeContainsExactFile(values []repository.TreeFile, expected repository.TreeFile) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type AcquireWriterLeaseInput struct {
	ProjectID                string
	SessionID                string
	ActorID                  string
	ExpectedSessionVersion   uint64
	ExpectedSessionEpoch     uint64
	ExpectedCandidateVersion uint64
	TTL                      time.Duration
}

type CandidateSessionResult struct {
	Session   SessionView                   `json:"session"`
	Candidate repository.CandidateWorkspace `json:"candidate"`
}

func (facade *Facade) AcquireWriterLease(
	ctx context.Context,
	input AcquireWriterLeaseInput,
) (CandidateSessionResult, error) {
	if err := facade.access.RequireProjectEdit(ctx, input.ProjectID, input.ActorID); err != nil {
		return CandidateSessionResult{}, fmt.Errorf("authorize Candidate lease: %w", err)
	}
	session, err := facade.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return CandidateSessionResult{}, err
	}
	if err := session.Authorize(ActionEdit, input.ExpectedSessionVersion, input.ExpectedSessionEpoch); err != nil {
		return CandidateSessionResult{}, err
	}
	view := session.Snapshot()
	if view.Candidate.Version != input.ExpectedCandidateVersion {
		return CandidateSessionResult{}, ErrCandidateVersionConflict
	}
	live, err := facade.candidates.Get(ctx, input.ProjectID, view.Candidate.ID)
	if err != nil {
		return CandidateSessionResult{}, err
	}
	if !candidateProjectionMatches(view.Candidate, live.Candidate) {
		if !isRecoveredLease(view.Candidate, live.Candidate, input.ActorID) {
			return CandidateSessionResult{}, ErrSessionProjectionStale
		}
		synced, syncErr := facade.sessions.SyncCandidate(
			ctx, input.ProjectID, input.SessionID, view.Version, view.SessionEpoch, input.ActorID,
		)
		if syncErr != nil {
			return CandidateSessionResult{Candidate: live.Candidate}, errors.Join(ErrSessionReconciliation, syncErr)
		}
		return CandidateSessionResult{Session: synced.Snapshot(), Candidate: live.Candidate}, nil
	}
	record, err := facade.candidates.AcquireLease(
		ctx, input.ProjectID, view.Candidate.ID, input.ExpectedCandidateVersion, input.ActorID, input.TTL,
	)
	if err != nil {
		return CandidateSessionResult{}, err
	}
	synced, err := facade.sessions.SyncCandidate(
		ctx, input.ProjectID, input.SessionID, view.Version, view.SessionEpoch, input.ActorID,
	)
	if err != nil {
		return CandidateSessionResult{Candidate: record.Candidate}, errors.Join(ErrSessionReconciliation, err)
	}
	return CandidateSessionResult{Session: synced.Snapshot(), Candidate: record.Candidate}, nil
}

type FileMutationInput struct {
	ProjectID                string
	SessionID                string
	ActorID                  string
	ExpectedSessionVersion   uint64
	ExpectedSessionEpoch     uint64
	ExpectedCandidateVersion uint64
	ExpectedWriterLeaseEpoch uint64
	OperationID              string
	Kind                     repository.OperationKind
	Path                     string
	FromPath                 string
	ExpectedFileHash         string
	Mode                     string
	Value                    []byte
}

type FileMutationResult struct {
	Session  SessionView                 `json:"session"`
	Mutation repository.MutationResult   `json:"mutation"`
	FileBlob *repository.FileBlobPointer `json:"fileBlob,omitempty"`
}

func (facade *Facade) MutateFile(
	ctx context.Context,
	input FileMutationInput,
) (FileMutationResult, error) {
	if err := facade.access.RequireProjectEdit(ctx, input.ProjectID, input.ActorID); err != nil {
		return FileMutationResult{}, fmt.Errorf("authorize Candidate file mutation: %w", err)
	}
	session, err := facade.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return FileMutationResult{}, err
	}
	view := session.Snapshot()
	if input.ExpectedSessionEpoch != view.SessionEpoch {
		return FileMutationResult{}, ErrEpochFenced
	}
	// Check the current authoritative action set even for an idempotent replay.
	// A stale request version may continue only through MutationService's exact
	// operation-ID replay path; it can never append a new operation.
	if err := session.Authorize(ActionEdit, view.Version, view.SessionEpoch); err != nil {
		return FileMutationResult{}, err
	}
	if view.Version < input.ExpectedSessionVersion {
		return FileMutationResult{}, ErrVersionConflict
	}
	if view.Version > input.ExpectedSessionVersion && view.Candidate.Version <= input.ExpectedCandidateVersion {
		// The session changed without this Candidate operation (for example a
		// checkpoint was attached). A stale session ETag may recover only when
		// the Candidate already advanced, which makes a new append impossible.
		return FileMutationResult{}, ErrVersionConflict
	}
	if view.Version == input.ExpectedSessionVersion && view.Candidate.Version != input.ExpectedCandidateVersion {
		return FileMutationResult{}, ErrCandidateVersionConflict
	}

	operation := repository.FileOperation{
		ID: input.OperationID, Kind: input.Kind, Path: input.Path, FromPath: input.FromPath,
		ExpectedHash: input.ExpectedFileHash, Mode: input.Mode,
	}
	var filePointer *repository.FileBlobPointer
	if input.Kind == repository.OperationUpsert {
		if input.Value == nil {
			return FileMutationResult{}, fmt.Errorf("%w: upsert file bytes are required", repository.ErrInvalidMutation)
		}
		blob, putErr := facade.files.Put(ctx, input.ProjectID, input.ActorID, input.Value)
		if putErr != nil {
			return FileMutationResult{}, fmt.Errorf("store Candidate file bytes: %w", putErr)
		}
		operation.ContentHash = blob.Pointer.ContentHash
		operation.ByteSize = blob.Pointer.ByteSize
		pointer := blob.Pointer
		filePointer = &pointer
	} else if input.Value != nil {
		return FileMutationResult{}, fmt.Errorf("%w: non-upsert operation cannot carry file bytes", repository.ErrInvalidMutation)
	}
	operation, err = repository.NormalizeOperation(operation)
	if err != nil {
		return FileMutationResult{}, err
	}

	mutation, err := facade.mutations.Apply(ctx, repository.MutationPrincipal{
		ActorID: input.ActorID, Attribution: "user",
	}, repository.ApplyMutationInput{
		ProjectID: input.ProjectID, CandidateID: view.Candidate.ID,
		ExpectedCandidateVersion: input.ExpectedCandidateVersion,
		ExpectedSessionEpoch:     input.ExpectedSessionEpoch,
		ExpectedWriterLeaseEpoch: input.ExpectedWriterLeaseEpoch,
		Operation:                operation,
	})
	if err != nil {
		return FileMutationResult{Mutation: mutation, FileBlob: filePointer}, err
	}
	if facade.workspace != nil {
		live, liveErr := facade.candidates.Get(ctx, input.ProjectID, view.Candidate.ID)
		if liveErr != nil {
			return FileMutationResult{Mutation: mutation, FileBlob: filePointer}, errors.Join(
				ErrSessionReconciliation, ErrWorkspaceReconciliation,
				fmt.Errorf("load committed Candidate for workspace synchronization: %w", liveErr),
			)
		}
		if syncErr := facade.workspace.SynchronizeMutation(ctx, view, live.Candidate, mutation, input.Value); syncErr != nil {
			return FileMutationResult{Mutation: mutation, FileBlob: filePointer}, errors.Join(
				ErrSessionReconciliation, ErrWorkspaceReconciliation, syncErr,
			)
		}
	}

	// If a previous attempt already reconciled the session, return its current
	// projection. Otherwise advance it exactly once from the request version.
	if view.Candidate.Version == mutation.Entry.CandidateTo &&
		view.Candidate.TreeHash == mutation.AfterTree.TreeHash {
		return FileMutationResult{Session: view, Mutation: mutation, FileBlob: filePointer}, nil
	}
	if view.Version != input.ExpectedSessionVersion ||
		view.Candidate.Version != mutation.Entry.CandidateFrom ||
		view.Candidate.TreeHash != mutation.BeforeTree.TreeHash {
		return FileMutationResult{Mutation: mutation, FileBlob: filePointer}, ErrSessionProjectionStale
	}
	synced, syncErr := facade.sessions.SyncCandidate(
		ctx, input.ProjectID, input.SessionID, view.Version, view.SessionEpoch, input.ActorID,
	)
	if syncErr != nil {
		return FileMutationResult{Mutation: mutation, FileBlob: filePointer},
			errors.Join(ErrSessionReconciliation, syncErr)
	}
	return FileMutationResult{Session: synced.Snapshot(), Mutation: mutation, FileBlob: filePointer}, nil
}

// FileBatchMutationInput is the internal, server-authored path for applying an
// already reviewed Agent patch. Browser transports must not decode
// Attribution or Operations directly into this type; the Agent merge service
// derives both from immutable evidence and uses the exact session fences.
type FileBatchMutationInput struct {
	ProjectID                string
	SessionID                string
	CandidateID              string
	ActorID                  string
	ExpectedSessionVersion   uint64
	ExpectedSessionEpoch     uint64
	ExpectedCandidateVersion uint64
	ExpectedWriterLeaseEpoch uint64
	Operations               []repository.FileOperation
}

type FileBatchMutationResult struct {
	Session  SessionView                    `json:"session"`
	Mutation repository.BatchMutationResult `json:"mutation"`
}

// MutateAgentFiles applies an Agent merge as one SQL journal transaction,
// reconciles the materialized workspace, and advances the SandboxSession
// projection once. Exact operation IDs make every step replayable after a
// crash; a partial journal append is never observable as success.
func (facade *Facade) MutateAgentFiles(
	ctx context.Context,
	input FileBatchMutationInput,
) (FileBatchMutationResult, error) {
	return facade.mutateFiles(ctx, input, "agent", ActionAgent)
}

// MutateRestoreFiles is the only batch entry used by an explicit Agent merge
// undo. Keeping restore attribution server-selected prevents browser input
// from impersonating either an Agent result or a recovery operation.
func (facade *Facade) MutateRestoreFiles(
	ctx context.Context,
	input FileBatchMutationInput,
) (FileBatchMutationResult, error) {
	return facade.mutateFiles(ctx, input, "restore", ActionEdit)
}

func (facade *Facade) mutateFiles(
	ctx context.Context,
	input FileBatchMutationInput,
	attribution string,
	action Action,
) (FileBatchMutationResult, error) {
	if facade == nil || facade.batchMutations == nil {
		return FileBatchMutationResult{}, errors.New("sandbox atomic Candidate mutation service is unavailable")
	}
	if err := facade.access.RequireProjectEdit(ctx, input.ProjectID, input.ActorID); err != nil {
		return FileBatchMutationResult{}, fmt.Errorf("authorize Agent Candidate merge: %w", err)
	}
	session, err := facade.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return FileBatchMutationResult{}, err
	}
	view := session.Snapshot()
	if view.Candidate.ID != input.CandidateID {
		return FileBatchMutationResult{}, ErrCandidateMismatch
	}
	if input.ExpectedSessionEpoch != view.SessionEpoch {
		return FileBatchMutationResult{}, ErrEpochFenced
	}
	// Authorize against the current projection even on an idempotent replay.
	if err := session.Authorize(action, view.Version, view.SessionEpoch); err != nil {
		return FileBatchMutationResult{}, err
	}
	if view.Version < input.ExpectedSessionVersion {
		return FileBatchMutationResult{}, ErrVersionConflict
	}
	if view.Version > input.ExpectedSessionVersion && view.Candidate.Version <= input.ExpectedCandidateVersion {
		return FileBatchMutationResult{}, ErrVersionConflict
	}
	if view.Version == input.ExpectedSessionVersion && view.Candidate.Version != input.ExpectedCandidateVersion {
		return FileBatchMutationResult{}, ErrCandidateVersionConflict
	}

	mutation, err := facade.batchMutations.ApplyBatch(ctx, repository.MutationPrincipal{
		ActorID: input.ActorID, Attribution: attribution,
	}, repository.ApplyBatchMutationInput{
		ProjectID: input.ProjectID, CandidateID: input.CandidateID,
		ExpectedCandidateVersion: input.ExpectedCandidateVersion,
		ExpectedSessionEpoch:     input.ExpectedSessionEpoch,
		ExpectedWriterLeaseEpoch: input.ExpectedWriterLeaseEpoch,
		Operations:               input.Operations,
	})
	if err != nil {
		return FileBatchMutationResult{Mutation: mutation}, err
	}
	if len(mutation.Entries) == 0 {
		return FileBatchMutationResult{Mutation: mutation}, errors.Join(
			ErrSessionReconciliation, repository.ErrMutationStoreContract,
		)
	}
	live, err := facade.candidates.Get(ctx, input.ProjectID, input.CandidateID)
	if err != nil {
		return FileBatchMutationResult{Mutation: mutation}, errors.Join(
			ErrSessionReconciliation,
			fmt.Errorf("load committed Agent merge Candidate: %w", err),
		)
	}
	if live.Candidate.Version != mutation.FinalCandidateVersion ||
		live.Candidate.CurrentTree.TreeHash != mutation.AfterTree.TreeHash ||
		live.Candidate.JournalSequence != mutation.Entries[len(mutation.Entries)-1].Sequence {
		return FileBatchMutationResult{Mutation: mutation}, errors.Join(
			ErrSessionReconciliation, repository.ErrMutationReconciliation,
		)
	}
	if facade.workspace != nil {
		if facade.batchWorkspace == nil {
			return FileBatchMutationResult{Mutation: mutation}, errors.Join(
				ErrSessionReconciliation, ErrWorkspaceReconciliation,
				errors.New("workspace does not support atomic batch reconciliation"),
			)
		}
		if syncErr := facade.batchWorkspace.SynchronizeBatch(ctx, view, live.Candidate, mutation); syncErr != nil {
			return FileBatchMutationResult{Mutation: mutation}, errors.Join(
				ErrSessionReconciliation, ErrWorkspaceReconciliation, syncErr,
			)
		}
	}

	if view.Candidate.Version == mutation.FinalCandidateVersion &&
		view.Candidate.JournalSequence == mutation.Entries[len(mutation.Entries)-1].Sequence &&
		view.Candidate.TreeHash == mutation.AfterTree.TreeHash {
		return FileBatchMutationResult{Session: view, Mutation: mutation}, nil
	}
	if view.Version != input.ExpectedSessionVersion ||
		view.Candidate.Version != mutation.Entries[0].CandidateFrom ||
		view.Candidate.JournalSequence+uint64(len(mutation.Entries)) !=
			mutation.Entries[len(mutation.Entries)-1].Sequence ||
		view.Candidate.TreeHash != mutation.BeforeTree.TreeHash {
		return FileBatchMutationResult{Mutation: mutation}, ErrSessionProjectionStale
	}
	synced, syncErr := facade.sessions.SyncCandidate(
		ctx, input.ProjectID, input.SessionID, view.Version, view.SessionEpoch, input.ActorID,
	)
	if syncErr != nil {
		return FileBatchMutationResult{Mutation: mutation}, errors.Join(ErrSessionReconciliation, syncErr)
	}
	return FileBatchMutationResult{Session: synced.Snapshot(), Mutation: mutation}, nil
}

type CheckpointInput struct {
	ProjectID                string
	SessionID                string
	ActorID                  string
	CheckpointID             string
	Reason                   string
	ExpectedSessionVersion   uint64
	ExpectedSessionEpoch     uint64
	ExpectedCandidateVersion uint64
	ExpectedWriterLeaseEpoch uint64
}

type CheckpointResult struct {
	Session    SessionView                  `json:"session"`
	Checkpoint repository.CandidateSnapshot `json:"checkpoint"`
}

func (facade *Facade) Checkpoint(
	ctx context.Context,
	input CheckpointInput,
) (CheckpointResult, error) {
	if err := facade.access.RequireProjectEdit(ctx, input.ProjectID, input.ActorID); err != nil {
		return CheckpointResult{}, fmt.Errorf("authorize Candidate checkpoint: %w", err)
	}
	session, err := facade.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return CheckpointResult{}, err
	}
	view := session.Snapshot()
	if input.ExpectedSessionEpoch != view.SessionEpoch {
		return CheckpointResult{}, ErrEpochFenced
	}
	if err := session.Authorize(ActionCheckpoint, view.Version, view.SessionEpoch); err != nil {
		return CheckpointResult{}, err
	}
	if view.Version < input.ExpectedSessionVersion {
		return CheckpointResult{}, ErrVersionConflict
	}

	checkpoint, err := facade.candidates.CreateCheckpoint(ctx, repository.CreateCheckpointInput{
		ID: input.CheckpointID, ProjectID: input.ProjectID, CandidateID: view.Candidate.ID,
		ExpectedCandidateVersion: input.ExpectedCandidateVersion,
		ExpectedSessionEpoch:     input.ExpectedSessionEpoch,
		ExpectedWriterLeaseEpoch: input.ExpectedWriterLeaseEpoch,
		ActorID:                  input.ActorID, Reason: input.Reason,
	})
	if err != nil {
		return CheckpointResult{}, err
	}
	if view.LatestCheckpoint != nil && view.LatestCheckpoint.ID == checkpoint.ID {
		return CheckpointResult{Session: view, Checkpoint: checkpoint}, nil
	}
	if view.Version != input.ExpectedSessionVersion || view.Candidate.Version != checkpoint.CandidateVersion ||
		view.Candidate.TreeHash != checkpoint.Tree.TreeHash {
		return CheckpointResult{Checkpoint: checkpoint}, ErrSessionProjectionStale
	}
	attached, err := facade.sessions.AttachCheckpoint(
		ctx, input.ProjectID, input.SessionID, view.Version, view.SessionEpoch, input.ActorID, checkpoint.ID,
	)
	if err != nil {
		return CheckpointResult{Checkpoint: checkpoint}, errors.Join(ErrSessionReconciliation, err)
	}
	return CheckpointResult{Session: attached.Snapshot(), Checkpoint: checkpoint}, nil
}

func (facade *Facade) authoritativeRepository(
	ctx context.Context,
	projectID, sessionID, actorID string,
	edit bool,
) (SandboxSession, repository.CandidateMutationRecord, error) {
	var authorizeErr error
	if edit {
		authorizeErr = facade.access.RequireProjectEdit(ctx, projectID, actorID)
	} else {
		authorizeErr = facade.access.RequireProjectView(ctx, projectID, actorID)
	}
	if authorizeErr != nil {
		return SandboxSession{}, repository.CandidateMutationRecord{}, authorizeErr
	}
	session, err := facade.sessions.Get(ctx, projectID, sessionID)
	if err != nil {
		return SandboxSession{}, repository.CandidateMutationRecord{}, err
	}
	view := session.Snapshot()
	record, err := facade.candidates.Get(ctx, projectID, view.Candidate.ID)
	if err != nil {
		return SandboxSession{}, repository.CandidateMutationRecord{}, err
	}
	if !candidateProjectionMatches(view.Candidate, record.Candidate) {
		return SandboxSession{}, repository.CandidateMutationRecord{}, ErrSessionProjectionStale
	}
	return session, record, nil
}

func candidateProjectionMatches(projected CandidateState, candidate repository.CandidateWorkspace) bool {
	return projected.ID == candidate.ID && projected.RepositorySnapshotID == candidate.RepositorySnapshotID &&
		projected.Status == candidate.Status &&
		projected.BaseTreeHash == candidate.BaseTreeHash && projected.TreeHash == candidate.CurrentTree.TreeHash &&
		projected.Version == candidate.Version && projected.JournalSequence == candidate.JournalSequence &&
		projected.SessionEpoch == candidate.SessionEpoch && projected.WriterLeaseEpoch == candidate.WriterLeaseEpoch &&
		projected.Dirty == candidate.Dirty && projected.Conflicted == candidate.Conflicted &&
		projected.Stale == candidate.Stale && projected.RebaseRequired == candidate.RebaseRequired &&
		// UpdatedAt records when the session projection was persisted, while the
		// Candidate row records when the source aggregate advanced. Exactness is
		// carried by the version/journal/epoch/tree tuple above, not timestamp
		// equality across two separately committed aggregates.
		!projected.UpdatedAt.IsZero() && !candidate.UpdatedAt.IsZero()
}

func isRecoveredLease(projected CandidateState, candidate repository.CandidateWorkspace, actorID string) bool {
	return candidate.Status == repository.CandidateActive && candidate.Lease != nil &&
		projected.Status == repository.CandidateActive &&
		candidate.Lease.OwnerID == actorID && candidate.ID == projected.ID &&
		candidate.RepositorySnapshotID == projected.RepositorySnapshotID &&
		candidate.BaseTreeHash == projected.BaseTreeHash && candidate.CurrentTree.TreeHash == projected.TreeHash &&
		candidate.Version == projected.Version+1 && candidate.JournalSequence == projected.JournalSequence &&
		candidate.SessionEpoch == projected.SessionEpoch && candidate.WriterLeaseEpoch == projected.WriterLeaseEpoch+1 &&
		candidate.Dirty == projected.Dirty && candidate.Conflicted == projected.Conflicted &&
		candidate.Stale == projected.Stale && candidate.RebaseRequired == projected.RebaseRequired
}

var _ SessionStore = (*Store)(nil)
var _ CandidateControls = (*repository.CandidateControlStore)(nil)
var _ CandidateMutationService = (*repository.MutationService)(nil)
var _ CandidateBatchMutationService = (*repository.MutationService)(nil)
var _ RepositoryFileBlobs = (*repository.FileBlobService)(nil)
