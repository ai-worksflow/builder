package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidMutation         = errors.New("invalid repository mutation")
	ErrCandidateTreeDrift      = errors.New("candidate tree pointer and hydrated tree have drifted")
	ErrPathPolicyDenied        = errors.New("repository path policy denied the mutation")
	ErrPathPolicyDrift         = errors.New("repository path policy does not bind the exact candidate lineage")
	ErrOperationReplay         = errors.New("repository operation id was already committed with different input")
	ErrMutationStoreContract   = errors.New("repository mutation store violated its contract")
	ErrAppendOutcomeUnknown    = errors.New("repository journal append outcome is unknown")
	ErrMutationReconciliation  = errors.New("repository mutation requires reconciliation")
	ErrTreeFinalizationPending = errors.New("repository mutation was committed but tree finalization is pending")
)

// ApplyMutationInput is the complete client-controlled mutation surface. In
// particular, it intentionally contains no TreeManifest or TreeBlobPointer:
// clients can describe a file operation and its fences, but only this service
// may derive and persist the after-tree identity.
type ApplyMutationInput struct {
	ProjectID                string        `json:"projectId"`
	CandidateID              string        `json:"candidateId"`
	ExpectedCandidateVersion uint64        `json:"expectedCandidateVersion"`
	ExpectedSessionEpoch     uint64        `json:"expectedSessionEpoch"`
	ExpectedWriterLeaseEpoch uint64        `json:"expectedWriterLeaseEpoch"`
	Operation                FileOperation `json:"operation"`
}

// MutationPrincipal is injected from the authenticated server session and
// its capability, never decoded from the mutation request body. In particular
// an agent cannot self-report user attribution to bypass extension paths.
type MutationPrincipal struct {
	ActorID     string `json:"-"`
	Attribution string `json:"-"`
}

type MutationAuthorizer interface {
	RequireProjectEdit(context.Context, string, string) error
}

// MutationFileResolver proves that an upsert's server-derived content hash is
// registered for this project and resolves to the exact byte size. This keeps
// a syntactically valid but nonexistent/client-invented hash out of the tree.
type MutationFileResolver interface {
	VerifyFileBlob(context.Context, string, string, int64) error
}

// CandidateMutationRecord is loaded from the authoritative SQL aggregate.
// Candidate.CurrentTree is a hydrated semantic value; CurrentTreePointer is
// the complete pointer persisted by SQL. The service checks both before use.
type CandidateMutationRecord struct {
	Candidate          CandidateWorkspace
	CurrentTreePointer TreeBlobPointer
}

// PathPolicySubject identifies the immutable RepositorySnapshot and exact
// FullStackTemplate release whose component TemplateReleases own the path
// policy. Implementations must not resolve by template name, tag, or "latest".
type PathPolicySubject struct {
	ProjectID            string
	RepositorySnapshotID string
	BuildManifest        ExactReference
	BuildContract        ExactReference
	FullStackTemplate    ExactReference
}

// PathPolicy contains effective repository-relative paths. A resolver is
// responsible for prefixing each component TemplateRelease policy with the
// component mount path from the exact FullStackTemplate.
type PathPolicy struct {
	Subject        PathPolicySubject
	ExtensionPaths []string
	ProtectedPaths []string
}

type PathPolicyResolver interface {
	ResolvePathPolicy(context.Context, PathPolicySubject) (PathPolicy, error)
}

// AppendOperationCommand is an internal, server-derived write command. A SQL
// implementation must append Entry and advance the Candidate with BeforeTree
// and AfterTree in one transaction, using all version/session/lease/tree facts
// as compare-and-swap preconditions.
type AppendOperationCommand struct {
	ProjectID      string
	CandidateAfter CandidateWorkspace
	Entry          JournalEntry
	BeforeTree     TreeBlobPointer
	AfterTree      TreeBlobPointer
}

// CommittedMutation is the exact row loaded from the committed journal. It is
// also used to recover a crash after SQL commit but before blob finalization.
type CommittedMutation struct {
	ProjectID  string
	Entry      JournalEntry
	BeforeTree TreeBlobPointer
	AfterTree  TreeBlobPointer
}

type CandidateStore interface {
	LoadMutationCandidate(context.Context, string, string) (CandidateMutationRecord, error)
	FindCommittedOperation(context.Context, string, string, string) (CommittedMutation, bool, error)
	AppendOperation(context.Context, AppendOperationCommand) (CommittedMutation, error)
}

type BatchCandidateStore interface {
	CandidateStore
	AppendOperations(context.Context, []AppendOperationCommand) ([]CommittedMutation, error)
}

// MutationTreeStore is implemented by TreeStore. Keeping the service on this
// narrow boundary makes SQL/content-store crash ordering independently
// testable and prevents transports from gaining direct tree-pointer writes.
type MutationTreeStore interface {
	Get(context.Context, string, string, TreeBlobPointer) (TreeManifest, error)
	PutPending(context.Context, string, string, TreeManifest) (TreeBlobPointer, error)
	Finalize(context.Context, string, string, TreeBlobPointer) error
	Abort(context.Context, string, string, TreeBlobPointer) error
}

type MutationResult struct {
	Entry               JournalEntry    `json:"entry"`
	BeforeTree          TreeBlobPointer `json:"beforeTree"`
	AfterTree           TreeBlobPointer `json:"afterTree"`
	Recovered           bool            `json:"recovered"`
	FinalizationPending bool            `json:"finalizationPending"`
}

type MutationService struct {
	candidates      CandidateStore
	batchCandidates BatchCandidateStore
	trees           MutationTreeStore
	files           MutationFileResolver
	policies        PathPolicyResolver
	access          MutationAuthorizer
	now             func() time.Time
}

func NewMutationService(
	candidates CandidateStore,
	trees MutationTreeStore,
	files MutationFileResolver,
	policies PathPolicyResolver,
	access MutationAuthorizer,
	now func() time.Time,
) (*MutationService, error) {
	if candidates == nil || trees == nil || files == nil || policies == nil || access == nil || now == nil {
		return nil, errors.New("repository mutation stores, file resolver, path policy resolver, authorizer, and clock are required")
	}
	batchCandidates, _ := candidates.(BatchCandidateStore)
	return &MutationService{
		candidates: candidates, batchCandidates: batchCandidates,
		trees: trees, files: files, policies: policies, access: access, now: now,
	}, nil
}

// Apply is idempotent by (CandidateID, Operation.ID). A retry of the exact
// request finalizes the already-committed after tree instead of writing a
// second pending blob or journal row.
func (service *MutationService) Apply(
	ctx context.Context,
	principal MutationPrincipal,
	input ApplyMutationInput,
) (MutationResult, error) {
	input, err := normalizeMutationInput(input)
	if err != nil {
		return MutationResult{}, err
	}
	principal, err = normalizeMutationPrincipal(principal)
	if err != nil {
		return MutationResult{}, err
	}
	// Authorization precedes even idempotency lookup: committed operation
	// metadata and blob finalization are both tenant-scoped capabilities.
	if err := service.access.RequireProjectEdit(ctx, input.ProjectID, principal.ActorID); err != nil {
		return MutationResult{}, fmt.Errorf("authorize repository mutation: %w", err)
	}

	committed, found, err := service.candidates.FindCommittedOperation(
		ctx,
		input.ProjectID,
		input.CandidateID,
		input.Operation.ID,
	)
	if err != nil {
		return MutationResult{}, fmt.Errorf("find committed repository operation: %w", err)
	}
	if found {
		if err := committed.matchesRetry(principal, input); err != nil {
			return MutationResult{}, err
		}
		if err := service.verifyCommittedTransition(ctx, committed); err != nil {
			return MutationResult{}, err
		}
		return service.finalize(ctx, committed, true)
	}

	record, err := service.candidates.LoadMutationCandidate(ctx, input.ProjectID, input.CandidateID)
	if err != nil {
		return MutationResult{}, fmt.Errorf("load repository candidate: %w", err)
	}
	if err := validateMutationRecord(record, input); err != nil {
		return MutationResult{}, err
	}

	currentTree, err := service.trees.Get(
		ctx,
		input.ProjectID,
		record.CurrentTreePointer.OwnerID,
		record.CurrentTreePointer,
	)
	if err != nil {
		return MutationResult{}, fmt.Errorf("read authoritative candidate tree: %w", err)
	}
	if !equalTrees(currentTree, record.Candidate.CurrentTree) {
		return MutationResult{}, ErrCandidateTreeDrift
	}

	subject := pathPolicySubject(record.Candidate)
	policy, err := service.policies.ResolvePathPolicy(ctx, subject)
	if err != nil {
		return MutationResult{}, fmt.Errorf("resolve exact repository path policy: %w", err)
	}
	policy, err = normalizePathPolicy(policy, subject)
	if err != nil {
		return MutationResult{}, err
	}
	if err := policy.authorize(principal.Attribution, input.Operation); err != nil {
		return MutationResult{}, err
	}
	if input.Operation.Kind == OperationUpsert {
		if err := service.files.VerifyFileBlob(
			ctx, input.ProjectID, input.Operation.ContentHash, input.Operation.ByteSize,
		); err != nil {
			return MutationResult{}, fmt.Errorf("verify repository file blob: %w", err)
		}
	}

	// ApplyOperation is deliberately performed against the tree read through
	// the authoritative SQL pointer, never against client-provided tree state.
	afterTree, err := ApplyOperation(currentTree, input.Operation)
	if err != nil {
		return MutationResult{}, err
	}
	next, entry, err := record.Candidate.Apply(
		input.ExpectedCandidateVersion,
		input.ExpectedSessionEpoch,
		input.ExpectedWriterLeaseEpoch,
		principal.ActorID,
		principal.Attribution,
		input.Operation,
		service.now().UTC(),
	)
	if err != nil {
		return MutationResult{}, err
	}
	if !equalTrees(next.CurrentTree, afterTree) || entry.BeforeTree != currentTree.TreeHash || entry.AfterTree != afterTree.TreeHash {
		return MutationResult{}, fmt.Errorf("%w: domain apply did not reproduce the authoritative tree", ErrMutationStoreContract)
	}

	afterPointer, err := service.trees.PutPending(ctx, input.ProjectID, input.CandidateID, afterTree)
	if err != nil {
		return MutationResult{}, fmt.Errorf("put pending candidate tree: %w", err)
	}
	if err := validateDerivedAfterPointer(afterPointer, input.CandidateID, afterTree); err != nil {
		abortErr := service.trees.Abort(context.Background(), input.ProjectID, input.CandidateID, afterPointer)
		if abortErr != nil {
			return MutationResult{}, errors.Join(err, fmt.Errorf("abort malformed pending candidate tree: %w", abortErr))
		}
		return MutationResult{}, err
	}

	command := AppendOperationCommand{
		ProjectID: input.ProjectID, CandidateAfter: next, Entry: entry,
		BeforeTree: record.CurrentTreePointer, AfterTree: afterPointer,
	}
	committed, err = service.candidates.AppendOperation(ctx, command)
	if err != nil {
		appendErr := fmt.Errorf("append repository candidate operation: %w", err)
		if errors.Is(err, ErrAppendOutcomeUnknown) {
			// The transaction may have committed. Aborting could destroy the
			// reachable after tree; leave it pending for catalog/journal recovery.
			return MutationResult{
				Entry: command.Entry, BeforeTree: command.BeforeTree, AfterTree: command.AfterTree,
				FinalizationPending: true,
			}, errors.Join(ErrMutationReconciliation, appendErr)
		}
		if abortErr := service.trees.Abort(context.Background(), input.ProjectID, input.CandidateID, afterPointer); abortErr != nil {
			return MutationResult{}, errors.Join(appendErr, fmt.Errorf("abort uncommitted candidate tree: %w", abortErr))
		}
		return MutationResult{}, appendErr
	}
	if err := committed.matchesCommand(command); err != nil {
		// AppendOperation reported a committed transaction, so aborting here
		// could destroy a reachable tree. Leave it pending for reconciliation.
		return MutationResult{
			Entry: command.Entry, BeforeTree: command.BeforeTree, AfterTree: command.AfterTree,
			FinalizationPending: true,
		}, errors.Join(ErrMutationReconciliation, err)
	}
	return service.finalize(ctx, committed, false)
}

func (service *MutationService) finalize(
	ctx context.Context,
	committed CommittedMutation,
	recovered bool,
) (MutationResult, error) {
	result := MutationResult{
		Entry: committed.Entry, BeforeTree: committed.BeforeTree, AfterTree: committed.AfterTree,
		Recovered: recovered,
	}
	if err := service.trees.Finalize(ctx, committed.ProjectID, committed.AfterTree.OwnerID, committed.AfterTree); err != nil {
		result.FinalizationPending = true
		return result, errors.Join(ErrTreeFinalizationPending, fmt.Errorf("finalize committed repository tree: %w", err))
	}
	return result, nil
}

func (service *MutationService) verifyCommittedTransition(ctx context.Context, committed CommittedMutation) error {
	if committed.Entry.Operation.Kind == OperationUpsert {
		if err := service.files.VerifyFileBlob(
			ctx, committed.ProjectID, committed.Entry.Operation.ContentHash, committed.Entry.Operation.ByteSize,
		); err != nil {
			return errors.Join(ErrMutationReconciliation, fmt.Errorf("verify committed repository file blob: %w", err))
		}
	}
	before, err := service.trees.Get(
		ctx,
		committed.ProjectID,
		committed.BeforeTree.OwnerID,
		committed.BeforeTree,
	)
	if err != nil {
		return errors.Join(ErrMutationReconciliation, fmt.Errorf("read committed before tree: %w", err))
	}
	after, err := service.trees.Get(
		ctx,
		committed.ProjectID,
		committed.AfterTree.OwnerID,
		committed.AfterTree,
	)
	if err != nil {
		return errors.Join(ErrMutationReconciliation, fmt.Errorf("read committed after tree: %w", err))
	}
	derived, err := ApplyOperation(before, committed.Entry.Operation)
	if err != nil {
		return errors.Join(ErrMutationReconciliation, fmt.Errorf("replay committed operation: %w", err))
	}
	if !equalTrees(derived, after) || derived.TreeHash != committed.AfterTree.TreeHash ||
		len(derived.Files) != committed.AfterTree.FileCount || treeByteSize(derived) != committed.AfterTree.ByteSize {
		return fmt.Errorf("%w: committed after tree is not the deterministic operation result", ErrMutationReconciliation)
	}
	return nil
}

func normalizeMutationInput(input ApplyMutationInput) (ApplyMutationInput, error) {
	if !validUUID(input.ProjectID) || !validUUID(input.CandidateID) ||
		input.ExpectedCandidateVersion == 0 || input.ExpectedSessionEpoch == 0 || input.ExpectedWriterLeaseEpoch == 0 {
		return ApplyMutationInput{}, ErrInvalidMutation
	}
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.CandidateID = strings.TrimSpace(input.CandidateID)
	operation, err := NormalizeOperation(input.Operation)
	if err != nil {
		return ApplyMutationInput{}, err
	}
	input.Operation = operation
	return input, nil
}

func normalizeMutationPrincipal(principal MutationPrincipal) (MutationPrincipal, error) {
	if !validUUID(principal.ActorID) {
		return MutationPrincipal{}, fmt.Errorf("%w: principal actor", ErrInvalidMutation)
	}
	principal.ActorID = strings.TrimSpace(principal.ActorID)
	principal.Attribution = strings.TrimSpace(principal.Attribution)
	if principal.Attribution != "user" && principal.Attribution != "agent" &&
		principal.Attribution != "merge" && principal.Attribution != "restore" {
		return MutationPrincipal{}, fmt.Errorf("%w: principal attribution", ErrInvalidMutation)
	}
	return principal, nil
}

func validateMutationRecord(record CandidateMutationRecord, input ApplyMutationInput) error {
	candidate := record.Candidate
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.ProjectID != input.ProjectID || candidate.ID != input.CandidateID {
		return fmt.Errorf("%w: candidate identity", ErrMutationStoreContract)
	}
	if err := record.CurrentTreePointer.validate(); err != nil {
		return err
	}
	if record.CurrentTreePointer.TreeHash != candidate.CurrentTree.TreeHash ||
		record.CurrentTreePointer.FileCount != len(candidate.CurrentTree.Files) ||
		record.CurrentTreePointer.ByteSize != treeByteSize(candidate.CurrentTree) {
		return ErrCandidateTreeDrift
	}
	return nil
}

func pathPolicySubject(candidate CandidateWorkspace) PathPolicySubject {
	return PathPolicySubject{
		ProjectID: candidate.ProjectID, RepositorySnapshotID: candidate.RepositorySnapshotID,
		BuildManifest: candidate.BuildManifest, BuildContract: candidate.BuildContract,
		FullStackTemplate: candidate.FullStackTemplate,
	}
}

func normalizePathPolicy(policy PathPolicy, expected PathPolicySubject) (PathPolicy, error) {
	if policy.Subject != expected || len(policy.ExtensionPaths) == 0 || len(policy.ProtectedPaths) == 0 {
		return PathPolicy{}, ErrPathPolicyDrift
	}
	var err error
	policy.ExtensionPaths, err = normalizePolicyPaths(policy.ExtensionPaths)
	if err != nil {
		return PathPolicy{}, fmt.Errorf("%w: extension paths: %v", ErrPathPolicyDrift, err)
	}
	policy.ProtectedPaths, err = normalizePolicyPaths(policy.ProtectedPaths)
	if err != nil {
		return PathPolicy{}, fmt.Errorf("%w: protected paths: %v", ErrPathPolicyDrift, err)
	}
	for _, extension := range policy.ExtensionPaths {
		for _, protected := range policy.ProtectedPaths {
			if pathContains(extension, protected) || pathContains(protected, extension) {
				return PathPolicy{}, fmt.Errorf("%w: extension and protected paths overlap", ErrPathPolicyDrift)
			}
		}
	}
	return policy, nil
}

func normalizePolicyPaths(paths []string) ([]string, error) {
	normalized := make([]string, len(paths))
	seen := make(map[string]bool, len(paths))
	for index, value := range paths {
		pathValue, err := NormalizePath(value)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(pathValue)
		if seen[key] {
			return nil, errors.New("duplicate effective path")
		}
		seen[key] = true
		normalized[index] = pathValue
	}
	return normalized, nil
}

func (policy PathPolicy) authorize(attribution string, operation FileOperation) error {
	paths := []string{operation.Path}
	if operation.Kind == OperationRename {
		paths = append(paths, operation.FromPath)
	}
	for _, target := range paths {
		if matchesAnyPolicyPath(target, policy.ProtectedPaths) {
			return fmt.Errorf("%w: protected path %s", ErrPathPolicyDenied, target)
		}
		if attribution == "agent" && !matchesAnyPolicyPath(target, policy.ExtensionPaths) {
			return fmt.Errorf("%w: agent path %s is outside extension paths", ErrPathPolicyDenied, target)
		}
	}
	return nil
}

func matchesAnyPolicyPath(target string, roots []string) bool {
	for _, root := range roots {
		if pathContains(root, target) {
			return true
		}
	}
	return false
}

func pathContains(root, target string) bool {
	root = strings.ToLower(root)
	target = strings.ToLower(target)
	return target == root || strings.HasPrefix(target, root+"/")
}

func validateDerivedAfterPointer(pointer TreeBlobPointer, candidateID string, tree TreeManifest) error {
	if err := pointer.validate(); err != nil {
		return err
	}
	if pointer.OwnerID != candidateID || pointer.TreeHash != tree.TreeHash ||
		pointer.FileCount != len(tree.Files) || pointer.ByteSize != treeByteSize(tree) {
		return fmt.Errorf("%w: pending store returned a non-derived after pointer", ErrMutationStoreContract)
	}
	return nil
}

func (committed CommittedMutation) matchesRetry(principal MutationPrincipal, input ApplyMutationInput) error {
	if committed.ProjectID != input.ProjectID || committed.Entry.CandidateID != input.CandidateID ||
		committed.Entry.CandidateFrom != input.ExpectedCandidateVersion ||
		committed.Entry.SessionEpoch != input.ExpectedSessionEpoch ||
		committed.Entry.LeaseEpoch != input.ExpectedWriterLeaseEpoch ||
		committed.Entry.ActorID != principal.ActorID || committed.Entry.Attribution != principal.Attribution ||
		committed.Entry.Operation != input.Operation {
		return ErrOperationReplay
	}
	return validateCommittedMutation(committed)
}

func (committed CommittedMutation) matchesCommand(command AppendOperationCommand) error {
	if committed.ProjectID != command.ProjectID || committed.Entry.CandidateID != command.Entry.CandidateID ||
		committed.Entry.Sequence != command.Entry.Sequence || committed.Entry.CandidateFrom != command.Entry.CandidateFrom ||
		committed.Entry.CandidateTo != command.Entry.CandidateTo || committed.Entry.SessionEpoch != command.Entry.SessionEpoch ||
		committed.Entry.LeaseEpoch != command.Entry.LeaseEpoch || committed.Entry.ActorID != command.Entry.ActorID ||
		committed.Entry.Attribution != command.Entry.Attribution || committed.Entry.Operation != command.Entry.Operation ||
		committed.Entry.BeforeTree != command.Entry.BeforeTree || committed.Entry.AfterTree != command.Entry.AfterTree ||
		committed.BeforeTree != command.BeforeTree || committed.AfterTree != command.AfterTree {
		return ErrMutationStoreContract
	}
	return validateCommittedMutation(committed)
}

func validateCommittedMutation(committed CommittedMutation) error {
	if !validUUID(committed.ProjectID) || committed.Entry.CreatedAt.IsZero() ||
		committed.Entry.CandidateTo != committed.Entry.CandidateFrom+1 || committed.Entry.Sequence == 0 ||
		committed.Entry.BeforeTree != committed.BeforeTree.TreeHash ||
		committed.Entry.AfterTree != committed.AfterTree.TreeHash ||
		committed.BeforeTree.TreeHash == committed.AfterTree.TreeHash {
		return ErrMutationStoreContract
	}
	if err := committed.BeforeTree.validate(); err != nil {
		return fmt.Errorf("%w: before pointer: %v", ErrMutationStoreContract, err)
	}
	if err := committed.AfterTree.validate(); err != nil {
		return fmt.Errorf("%w: after pointer: %v", ErrMutationStoreContract, err)
	}
	if committed.AfterTree.OwnerID != committed.Entry.CandidateID {
		return ErrMutationStoreContract
	}
	return nil
}

func equalTrees(left, right TreeManifest) bool {
	left, leftErr := ParseTree(left)
	right, rightErr := ParseTree(right)
	if leftErr != nil || rightErr != nil || left.SchemaVersion != right.SchemaVersion ||
		left.TreeHash != right.TreeHash || len(left.Files) != len(right.Files) {
		return false
	}
	for index := range left.Files {
		if left.Files[index] != right.Files[index] {
			return false
		}
	}
	return true
}
