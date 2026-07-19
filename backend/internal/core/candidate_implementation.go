package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

const candidateImplementationContractVersion = "candidate-implementation-freeze/v2"
const candidateVerificationBindingContractVersion = "candidate-verification-binding/v1"

type CandidateFileResolver interface {
	Resolve(context.Context, string, string, int64) (repository.FileBlobPointer, []byte, error)
}

type CandidateImplementationRequest struct {
	RequestKey               string
	SessionID                string
	CandidateID              string
	CheckpointID             string
	VerificationReceipt      repository.ExactReference
	Reason                   string
	ExpectedSessionVersion   uint64
	ExpectedSessionEpoch     uint64
	ExpectedCandidateVersion uint64
	ExpectedWriterLeaseEpoch uint64
}

type CandidateImplementationIdentity struct {
	FreezeReceiptID          string
	ProposalID               string
	RequestKey               string
	RequestHash              string
	SessionID                string
	CandidateID              string
	RepositorySnapshotID     string
	CheckpointID             string
	Reason                   string
	VerificationReceipt      repository.ExactReference
	ExpectedSessionVersion   uint64
	ExpectedSessionEpoch     uint64
	ExpectedCandidateVersion uint64
	JournalSequence          uint64
	ExpectedWriterLeaseEpoch uint64
	BaseTreeHash             string
	TreePointer              repository.TreeBlobPointer
	BuildManifest            repository.ExactReference
	BuildContract            repository.ExactReference
	FullStackTemplate        repository.ExactReference
	BaseWorkspaceRevision    *repository.ExactRevisionReference
}

type CandidateImplementationFreezeReceipt struct {
	ID                       string                             `json:"id"`
	ProjectID                string                             `json:"projectId"`
	SessionID                string                             `json:"sessionId"`
	CandidateID              string                             `json:"candidateId"`
	CandidateSnapshotID      string                             `json:"candidateSnapshotId"`
	VerificationReceipt      repository.ExactReference          `json:"verificationReceipt"`
	ImplementationProposalID string                             `json:"implementationProposalId"`
	RequestKey               string                             `json:"requestKey"`
	RequestHash              string                             `json:"requestHash"`
	SessionVersion           uint64                             `json:"sessionVersion"`
	CandidateVersion         uint64                             `json:"candidateVersion"`
	JournalSequence          uint64                             `json:"journalSequence"`
	SessionEpoch             uint64                             `json:"sessionEpoch"`
	WriterLeaseEpoch         uint64                             `json:"writerLeaseEpoch"`
	BaseTreeHash             string                             `json:"baseTreeHash"`
	CandidateTreeHash        string                             `json:"candidateTreeHash"`
	BuildManifest            repository.ExactReference          `json:"buildManifest"`
	BuildContract            repository.ExactReference          `json:"buildContract"`
	FullStackTemplate        repository.ExactReference          `json:"fullStackTemplate"`
	BaseWorkspaceRevision    *repository.ExactRevisionReference `json:"baseWorkspaceRevision,omitempty"`
	ProposalPayloadHash      string                             `json:"proposalPayloadHash"`
	OperationCount           int                                `json:"operationCount"`
	Reason                   string                             `json:"reason"`
	CreatedBy                string                             `json:"createdBy"`
	CreatedAt                time.Time                          `json:"createdAt"`
}

type CandidateImplementationResult struct {
	Proposal ImplementationProposal               `json:"proposal"`
	Receipt  CandidateImplementationFreezeReceipt `json:"receipt"`
	Replayed bool                                 `json:"replayed"`
}

func (s *ImplementationService) FindCandidateImplementation(
	ctx context.Context,
	projectID, actorID string,
	request CandidateImplementationRequest,
) (CandidateImplementationResult, bool, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionEdit); err != nil {
		return CandidateImplementationResult{}, false, err
	}
	request, requestHash, err := normalizeCandidateImplementationRequest(projectID, request)
	if err != nil {
		return CandidateImplementationResult{}, false, err
	}
	var model storage.CandidateImplementationFreezeModel
	err = s.database.WithContext(ctx).Where(
		"project_id = ? AND request_key = ?", projectID, request.RequestKey,
	).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CandidateImplementationResult{}, false, nil
	}
	if err != nil {
		return CandidateImplementationResult{}, false, err
	}
	if err := exactCandidateImplementationReplay(model, request, requestHash); err != nil {
		return CandidateImplementationResult{}, true, err
	}
	proposal, proposalModel, err := s.load(ctx, model.ImplementationProposalID.String())
	if err != nil {
		return CandidateImplementationResult{}, true, err
	}
	if proposalModel.ProjectID != model.ProjectID ||
		proposal.PayloadHash != model.ProposalPayloadHash ||
		proposal.ID != model.ImplementationProposalID.String() {
		return CandidateImplementationResult{}, true, ErrConflict
	}
	if err := s.contents.Finalize(ctx, proposalModel.ContentRef); err != nil {
		return CandidateImplementationResult{}, true, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return CandidateImplementationResult{
		Proposal: proposal, Receipt: candidateImplementationReceipt(model), Replayed: true,
	}, true, nil
}

func (s *ImplementationService) CreateCandidateImplementation(
	ctx context.Context,
	projectID, actorID string,
	request CandidateImplementationRequest,
	record repository.CandidateMutationRecord,
	files CandidateFileResolver,
) (CandidateImplementationResult, error) {
	request, requestHash, err := normalizeCandidateImplementationRequest(projectID, request)
	if err != nil {
		return CandidateImplementationResult{}, err
	}
	if replay, found, findErr := s.FindCandidateImplementation(
		ctx, projectID, actorID, request,
	); found || findErr != nil {
		return replay, findErr
	}
	if files == nil {
		return CandidateImplementationResult{}, fmt.Errorf("%w: Candidate file resolver", ErrInvalidInput)
	}
	candidate := record.Candidate
	if err := candidate.Validate(); err != nil || candidate.Status != repository.CandidateActive ||
		candidate.ProjectID != projectID || candidate.ID != request.CandidateID ||
		candidate.Version != request.ExpectedCandidateVersion ||
		candidate.SessionEpoch != request.ExpectedSessionEpoch ||
		candidate.WriterLeaseEpoch != request.ExpectedWriterLeaseEpoch ||
		candidate.Conflicted || candidate.Stale || candidate.RebaseRequired {
		return CandidateImplementationResult{}, fmt.Errorf("%w: Candidate freeze source is not exact and active", ErrConflict)
	}
	if candidate.Lease == nil || candidate.Lease.OwnerID != actorID ||
		candidate.Lease.Epoch != candidate.WriterLeaseEpoch ||
		!s.now().UTC().Before(candidate.Lease.ExpiresAt) {
		return CandidateImplementationResult{}, fmt.Errorf("%w: Candidate writer lease", ErrConflict)
	}
	if record.CurrentTreePointer.OwnerID != candidate.ID ||
		record.CurrentTreePointer.TreeHash != candidate.CurrentTree.TreeHash {
		return CandidateImplementationResult{}, fmt.Errorf("%w: Candidate tree pointer", ErrConflict)
	}
	workspaceRef := candidateImplementationVersionRef(candidate.BaseWorkspaceRevision)
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return CandidateImplementationResult{}, ErrInvalidInput
	}
	workspace, _, _, err := s.loadWorkspace(ctx, projectUUID, workspaceRef)
	if err != nil {
		return CandidateImplementationResult{}, err
	}
	operations, err := candidateImplementationOperations(
		ctx, projectID, request.CheckpointID, candidate.CurrentTree, workspace, files,
	)
	if err != nil {
		return CandidateImplementationResult{}, err
	}
	if len(operations) == 0 {
		return CandidateImplementationResult{}, fmt.Errorf("%w: Candidate contains no changes to freeze", ErrBlockingGate)
	}
	identity := CandidateImplementationIdentity{
		FreezeReceiptID: candidateImplementationUUID(projectID, request.RequestKey, "receipt"),
		ProposalID:      candidateImplementationUUID(projectID, request.RequestKey, "proposal"),
		RequestKey:      request.RequestKey, RequestHash: requestHash,
		SessionID: request.SessionID, CandidateID: candidate.ID,
		RepositorySnapshotID: candidate.RepositorySnapshotID,
		CheckpointID:         request.CheckpointID, Reason: request.Reason,
		VerificationReceipt:      request.VerificationReceipt,
		ExpectedSessionVersion:   request.ExpectedSessionVersion,
		ExpectedSessionEpoch:     request.ExpectedSessionEpoch,
		ExpectedCandidateVersion: request.ExpectedCandidateVersion,
		JournalSequence:          candidate.JournalSequence,
		ExpectedWriterLeaseEpoch: request.ExpectedWriterLeaseEpoch,
		BaseTreeHash:             candidate.BaseTreeHash, TreePointer: record.CurrentTreePointer,
		BuildManifest: candidate.BuildManifest, BuildContract: candidate.BuildContract,
		FullStackTemplate:     candidate.FullStackTemplate,
		BaseWorkspaceRevision: cloneCandidateImplementationRevision(candidate.BaseWorkspaceRevision),
	}
	proposalInput := CreateImplementationProposalInput{
		BuildManifestID: candidate.BuildManifest.ID,
		ApplicationBuildContract: ApplicationBuildContractRef{
			ID: candidate.BuildContract.ID, ContractHash: candidate.BuildContract.ContentHash,
		},
		Operations: operations,
		TraceLinks: candidateImplementationTraceLinks(identity),
	}
	proposal, createErr := s.create(ctx, projectID, actorID, proposalInput, nil, &identity)
	if createErr != nil {
		reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if replay, found, findErr := s.FindCandidateImplementation(
			reconcileCtx, projectID, actorID, request,
		); found {
			if findErr != nil {
				return CandidateImplementationResult{}, errors.Join(createErr, findErr)
			}
			return replay, nil
		}
		return CandidateImplementationResult{}, createErr
	}
	var receiptModel storage.CandidateImplementationFreezeModel
	if err := s.database.WithContext(ctx).Where(
		"id = ? AND project_id = ? AND implementation_proposal_id = ?",
		identity.FreezeReceiptID, projectID, proposal.ID,
	).Take(&receiptModel).Error; err != nil {
		return CandidateImplementationResult{}, fmt.Errorf("%w: load committed Candidate freeze receipt: %v", ErrConflict, err)
	}
	return CandidateImplementationResult{
		Proposal: proposal, Receipt: candidateImplementationReceipt(receiptModel),
	}, nil
}

func normalizeCandidateImplementationRequest(
	projectID string,
	request CandidateImplementationRequest,
) (CandidateImplementationRequest, string, error) {
	request.RequestKey = strings.TrimSpace(request.RequestKey)
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.CandidateID = strings.TrimSpace(request.CandidateID)
	request.CheckpointID = strings.TrimSpace(request.CheckpointID)
	request.VerificationReceipt.ID = strings.TrimSpace(request.VerificationReceipt.ID)
	request.VerificationReceipt.ContentHash = strings.TrimSpace(request.VerificationReceipt.ContentHash)
	request.Reason = strings.TrimSpace(request.Reason)
	if _, err := uuid.Parse(projectID); err != nil {
		return CandidateImplementationRequest{}, "", ErrInvalidInput
	}
	for _, value := range []string{request.SessionID, request.CandidateID, request.CheckpointID, request.VerificationReceipt.ID} {
		if _, err := uuid.Parse(value); err != nil {
			return CandidateImplementationRequest{}, "", fmt.Errorf("%w: Candidate freeze identity", ErrInvalidInput)
		}
	}
	if request.RequestKey == "" || len(request.RequestKey) > 128 ||
		request.Reason == "" || len(request.Reason) > 1000 ||
		request.ExpectedSessionVersion == 0 || request.ExpectedSessionEpoch == 0 ||
		request.ExpectedCandidateVersion == 0 || request.ExpectedWriterLeaseEpoch == 0 ||
		!domain.IsCanonicalHash(request.VerificationReceipt.ContentHash) {
		return CandidateImplementationRequest{}, "", fmt.Errorf("%w: Candidate freeze request", ErrInvalidInput)
	}
	hash, err := domain.CanonicalHash(struct {
		SchemaVersion            string                    `json:"schemaVersion"`
		ProjectID                string                    `json:"projectId"`
		RequestKey               string                    `json:"requestKey"`
		SessionID                string                    `json:"sessionId"`
		CandidateID              string                    `json:"candidateId"`
		CheckpointID             string                    `json:"checkpointId"`
		VerificationReceipt      repository.ExactReference `json:"verificationReceipt"`
		Reason                   string                    `json:"reason"`
		ExpectedSessionVersion   uint64                    `json:"expectedSessionVersion"`
		ExpectedSessionEpoch     uint64                    `json:"expectedSessionEpoch"`
		ExpectedCandidateVersion uint64                    `json:"expectedCandidateVersion"`
		ExpectedWriterLeaseEpoch uint64                    `json:"expectedWriterLeaseEpoch"`
	}{
		candidateImplementationContractVersion, projectID, request.RequestKey,
		request.SessionID, request.CandidateID, request.CheckpointID, request.VerificationReceipt, request.Reason,
		request.ExpectedSessionVersion, request.ExpectedSessionEpoch,
		request.ExpectedCandidateVersion, request.ExpectedWriterLeaseEpoch,
	})
	if err != nil {
		return CandidateImplementationRequest{}, "", err
	}
	return request, "sha256:" + hash, nil
}

func candidateImplementationUUID(projectID, requestKey, purpose string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		candidateImplementationContractVersion, projectID, requestKey, purpose,
	}, "\x00")))
	value, _ := uuid.FromBytes(digest[:16])
	value[6] = (value[6] & 0x0f) | 0x50
	value[8] = (value[8] & 0x3f) | 0x80
	return value.String()
}

func candidateImplementationOperations(
	ctx context.Context,
	projectID, checkpointID string,
	tree repository.TreeManifest,
	workspace map[string]any,
	files CandidateFileResolver,
) ([]FileOperation, error) {
	tree, err := repository.ParseTree(tree)
	if err != nil {
		return nil, err
	}
	base := make(map[string]map[string]any)
	for _, file := range objectSlice(workspace["files"]) {
		filePath := firstString(file, "path")
		if filePath == "" {
			continue
		}
		if err := validateWorkspacePath(filePath); err != nil {
			return nil, err
		}
		base[filePath] = file
	}
	candidatePaths := make(map[string]bool, len(tree.Files))
	operations := make([]FileOperation, 0, len(tree.Files)+len(base))
	ordinal := 0
	for _, file := range tree.Files {
		candidatePaths[file.Path] = true
		_, value, err := files.Resolve(ctx, projectID, file.ContentHash, file.ByteSize)
		if err != nil {
			return nil, fmt.Errorf("%w: resolve Candidate file %s: %v", ErrContentNotReady, file.Path, err)
		}
		if int64(len(value)) != file.ByteSize || hashBytes(value) != file.ContentHash {
			return nil, fmt.Errorf("%w: Candidate file %s differs from its tree pointer", ErrConflict, file.Path)
		}
		if !utf8.Valid(value) || bytes.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("%w: Candidate file %s is binary and cannot enter a text implementation Proposal", ErrBlockingGate, file.Path)
		}
		existing := base[file.Path]
		existingContent := workspaceFileContent(existing)
		existingMode := workspaceFileMode(existing)
		if existing != nil && hashText(existingContent) == file.ContentHash && existingMode == file.Mode {
			continue
		}
		ordinal++
		content := string(value)
		expectedHash := ""
		if existing != nil {
			expectedHash = hashText(existingContent)
		}
		operations = append(operations, FileOperation{
			ID:   candidateOperationID(ordinal, file.Path, "upsert"),
			Kind: "file.upsert", Path: file.Path, Content: &content,
			Language: implementationLanguage(file.Path), Mode: file.Mode,
			ExpectedHash: expectedHash,
			Rationale:    "Freeze exact CandidateSnapshot " + checkpointID,
			TraceSource:  []string{"candidate-snapshot:" + checkpointID},
		})
	}
	basePaths := make([]string, 0, len(base))
	for filePath := range base {
		if !candidatePaths[filePath] {
			basePaths = append(basePaths, filePath)
		}
	}
	sort.Strings(basePaths)
	for _, filePath := range basePaths {
		ordinal++
		operations = append(operations, FileOperation{
			ID:   candidateOperationID(ordinal, filePath, "delete"),
			Kind: "file.delete", Path: filePath,
			ExpectedHash: hashText(workspaceFileContent(base[filePath])),
			Rationale:    "Freeze exact CandidateSnapshot " + checkpointID,
			TraceSource:  []string{"candidate-snapshot:" + checkpointID},
		})
	}
	if len(operations) > 40_000 {
		return nil, fmt.Errorf("%w: Candidate freeze exceeds 40000 file operations", ErrInvalidInput)
	}
	return operations, nil
}

func candidateOperationID(ordinal int, filePath, kind string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + filePath))
	return fmt.Sprintf("candidate-%05d-%s", ordinal, hex.EncodeToString(digest[:6]))
}

func implementationLanguage(filePath string) string {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".json":
		return "json"
	case ".css":
		return "css"
	case ".html":
		return "html"
	case ".md":
		return "markdown"
	case ".sql":
		return "sql"
	case ".yaml", ".yml":
		return "yaml"
	default:
		return ""
	}
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func workspaceFileMode(file map[string]any) string {
	if file == nil {
		return ""
	}
	mode, _ := file["mode"].(string)
	if mode == "" {
		return "100644"
	}
	return mode
}

func validateCandidateImplementationCreate(
	bundle WorkbenchBundle,
	contract ApplicationBuildContractRef,
	identity CandidateImplementationIdentity,
) error {
	if identity.BuildManifest.ID != bundle.ID ||
		identity.BuildManifest.ContentHash != bundle.ManifestHash ||
		identity.BuildContract.ID != contract.ID ||
		identity.BuildContract.ContentHash != contract.ContractHash ||
		!optionalVersionRefsEqual(
			bundle.CurrentWorkspaceRevision,
			candidateImplementationVersionRef(identity.BaseWorkspaceRevision),
		) {
		return fmt.Errorf("%w: Candidate lineage differs from the active Workbench leaf", ErrProposalStale)
	}
	if identity.BaseWorkspaceRevision != nil {
		base := identity.BaseWorkspaceRevision
		if artifactID, err := uuid.Parse(base.ArtifactID); err != nil || artifactID == uuid.Nil {
			return fmt.Errorf("%w: Candidate base workspace artifact", ErrInvalidInput)
		}
		if revisionID, err := uuid.Parse(base.RevisionID); err != nil || revisionID == uuid.Nil ||
			!domain.IsCanonicalHash(base.ContentHash) {
			return fmt.Errorf("%w: Candidate base workspace revision", ErrInvalidInput)
		}
	}
	if identity.TreePointer.OwnerID != identity.CandidateID ||
		identity.TreePointer.TreeHash == "" ||
		identity.BaseTreeHash == "" || identity.VerificationReceipt.ID == "" ||
		!domain.IsCanonicalHash(identity.VerificationReceipt.ContentHash) {
		return fmt.Errorf("%w: Candidate tree identity", ErrInvalidInput)
	}
	return nil
}

func (s *ImplementationService) validateCandidateImplementationSource(
	ctx context.Context,
	proposal ImplementationProposal,
	model storage.ImplementationProposalModel,
) error {
	if proposal.ExecutionSource != ImplementationSourceCandidateFreeze {
		if proposal.CandidateSource != nil || model.CandidateSnapshotID != nil ||
			model.CandidateBaseTreeHash != nil || model.CandidateTreeHash != nil ||
			model.CandidateVerificationBindingVersion != nil || model.CandidateVerificationReceiptID != nil ||
			model.CandidateVerificationReceiptHash != nil {
			return fmt.Errorf("%w: unexpected Candidate implementation source", ErrConflict)
		}
		return nil
	}
	source := proposal.CandidateSource
	if source == nil || proposal.ApplicationBuildContract == nil ||
		model.CandidateSnapshotID == nil || model.CandidateBaseTreeHash == nil || model.CandidateTreeHash == nil ||
		model.CandidateVerificationBindingVersion == nil || *model.CandidateVerificationBindingVersion != candidateVerificationBindingContractVersion ||
		model.CandidateVerificationReceiptID == nil || model.CandidateVerificationReceiptHash == nil {
		return fmt.Errorf("%w: incomplete Candidate implementation source", ErrConflict)
	}
	identities := []string{
		source.FreezeReceiptID, source.RepositorySnapshotID, source.SessionID,
		source.CandidateID, source.CandidateSnapshotID, source.FullStackTemplate.ID, source.VerificationReceipt.ID,
		proposal.ApplicationBuildContract.ID,
	}
	if proposal.BaseWorkspaceRevision != nil {
		identities = append(
			identities,
			proposal.BaseWorkspaceRevision.ArtifactID,
			proposal.BaseWorkspaceRevision.RevisionID,
		)
	}
	for _, value := range identities {
		if parsed, err := uuid.Parse(value); err != nil || parsed == uuid.Nil {
			return fmt.Errorf("%w: Candidate implementation UUID", ErrConflict)
		}
	}
	if source.CandidateVersion == 0 || source.SessionEpoch == 0 || source.WriterLeaseEpoch == 0 ||
		!exactPrefixedSHA256(source.BaseTreeHash) || !exactPrefixedSHA256(source.TreeHash) ||
		!domain.IsCanonicalHash(source.FullStackTemplate.ContentHash) ||
		!domain.IsCanonicalHash(source.VerificationReceipt.ContentHash) ||
		!domain.IsCanonicalHash(proposal.ApplicationBuildContract.ContractHash) ||
		(proposal.BaseWorkspaceRevision != nil &&
			!domain.IsCanonicalHash(proposal.BaseWorkspaceRevision.ContentHash)) {
		return fmt.Errorf("%w: Candidate implementation exact references", ErrConflict)
	}
	if model.CandidateSnapshotID.String() != source.CandidateSnapshotID ||
		*model.CandidateBaseTreeHash != source.BaseTreeHash || *model.CandidateTreeHash != source.TreeHash ||
		model.CandidateVerificationReceiptID.String() != source.VerificationReceipt.ID ||
		*model.CandidateVerificationReceiptHash != source.VerificationReceipt.ContentHash {
		return fmt.Errorf("%w: Candidate implementation source differs from proposal row", ErrConflict)
	}
	var receipt storage.CandidateImplementationFreezeModel
	if err := s.database.WithContext(ctx).Where(
		"implementation_proposal_id = ? AND project_id = ?", model.ID, model.ProjectID,
	).Take(&receipt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: Candidate implementation freeze receipt", ErrConflict)
		}
		return err
	}
	if receipt.ID.String() != source.FreezeReceiptID ||
		receipt.VerificationBindingVersion != candidateVerificationBindingContractVersion ||
		receipt.VerificationReceiptID == nil || receipt.VerificationReceiptID.String() != source.VerificationReceipt.ID ||
		receipt.VerificationReceiptHash == nil || *receipt.VerificationReceiptHash != source.VerificationReceipt.ContentHash ||
		receipt.ProjectID != model.ProjectID || receipt.ImplementationProposalID != model.ID ||
		receipt.SessionID.String() != source.SessionID ||
		receipt.CandidateID.String() != source.CandidateID ||
		receipt.CandidateSnapshotID.String() != source.CandidateSnapshotID ||
		receipt.CandidateVersion != source.CandidateVersion ||
		receipt.JournalSequence != source.JournalSequence ||
		receipt.SessionEpoch != source.SessionEpoch ||
		receipt.WriterLeaseEpoch != source.WriterLeaseEpoch ||
		receipt.BaseTreeHash != source.BaseTreeHash || receipt.CandidateTreeHash != source.TreeHash ||
		receipt.CandidateTreeOwnerID.String() != source.CandidateID ||
		receipt.BuildManifestID != model.BuildManifestID ||
		receipt.BuildContractID.String() != proposal.ApplicationBuildContract.ID ||
		receipt.BuildContractHash != proposal.ApplicationBuildContract.ContractHash ||
		receipt.FullStackTemplateID.String() != source.FullStackTemplate.ID ||
		receipt.FullStackTemplateHash != source.FullStackTemplate.ContentHash ||
		!candidateImplementationReceiptBaseMatches(receipt, proposal.BaseWorkspaceRevision) ||
		receipt.ProposalPayloadHash != proposal.PayloadHash ||
		receipt.OperationCount != len(proposal.Operations) ||
		receipt.CreatedBy.String() != proposal.CreatedBy {
		return fmt.Errorf("%w: Candidate implementation differs from its freeze receipt", ErrConflict)
	}
	var eventCount int64
	if err := s.database.WithContext(ctx).Table("candidate_workspace_control_events").Where(
		"candidate_id = ? AND event_kind = 'candidate.frozen' AND target_status = 'frozen' "+
			"AND candidate_snapshot_id = ? AND candidate_version_from = ? AND candidate_version_to = ? "+
			"AND session_epoch_from = ? AND session_epoch_to = ? AND actor_id = ? AND reason = ?",
		receipt.CandidateID, receipt.CandidateSnapshotID, receipt.CandidateVersion,
		receipt.CandidateVersion+1, receipt.SessionEpoch, receipt.SessionEpoch,
		receipt.CreatedBy, receipt.Reason,
	).Count(&eventCount).Error; err != nil {
		return err
	}
	if eventCount != 1 {
		return fmt.Errorf("%w: Candidate implementation freeze event", ErrConflict)
	}
	return nil
}

func exactPrefixedSHA256(value string) bool {
	return strings.HasPrefix(value, "sha256:") && value == strings.ToLower(value) &&
		domain.IsCanonicalHash(value)
}

func candidateWorkspaceTree(workspace map[string]any) (repository.TreeManifest, error) {
	values := objectSlice(workspace["files"])
	files := make([]repository.TreeFile, 0, len(values))
	for _, file := range values {
		filePath := firstString(file, "path")
		content, contentOK := file["content"].(string)
		mode := workspaceFileMode(file)
		if err := validateWorkspacePath(filePath); err != nil || !contentOK ||
			(mode != "100644" && mode != "100755") {
			return repository.TreeManifest{}, fmt.Errorf("%w: workspace file cannot form an exact tree", ErrConflict)
		}
		files = append(files, repository.TreeFile{
			Path: filePath, Mode: mode, ContentHash: hashBytes([]byte(content)), ByteSize: int64(len([]byte(content))),
		})
	}
	tree, err := repository.NewTree(files)
	if err != nil {
		return repository.TreeManifest{}, fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return tree, nil
}

func persistCandidateImplementationFreeze(
	transaction *gorm.DB,
	proposalModel storage.ImplementationProposalModel,
	proposal ImplementationProposal,
	identity CandidateImplementationIdentity,
	actorID uuid.UUID,
	now time.Time,
) error {
	if proposal.CandidateSource == nil || !candidateImplementationRevisionMatchesVersionRef(
		identity.BaseWorkspaceRevision,
		proposal.BaseWorkspaceRevision,
	) {
		return ErrConflict
	}
	parse := func(value string) (uuid.UUID, error) {
		parsed, err := uuid.Parse(value)
		if err != nil {
			return uuid.Nil, fmt.Errorf("%w: Candidate freeze UUID", ErrInvalidInput)
		}
		return parsed, nil
	}
	receiptID, err := parse(identity.FreezeReceiptID)
	if err != nil {
		return err
	}
	sessionID, err := parse(identity.SessionID)
	if err != nil {
		return err
	}
	candidateID, err := parse(identity.CandidateID)
	if err != nil {
		return err
	}
	checkpointID, err := parse(identity.CheckpointID)
	if err != nil {
		return err
	}
	buildContractID, err := parse(identity.BuildContract.ID)
	if err != nil {
		return err
	}
	fullStackTemplateID, err := parse(identity.FullStackTemplate.ID)
	if err != nil {
		return err
	}
	var baseArtifactID, baseRevisionID *uuid.UUID
	var baseContentHash *string
	if identity.BaseWorkspaceRevision != nil {
		artifactID, err := parse(identity.BaseWorkspaceRevision.ArtifactID)
		if err != nil {
			return err
		}
		revisionID, err := parse(identity.BaseWorkspaceRevision.RevisionID)
		if err != nil {
			return err
		}
		baseArtifactID = &artifactID
		baseRevisionID = &revisionID
		baseContentHash = nonEmptyStringPointer(identity.BaseWorkspaceRevision.ContentHash)
	}
	treeOwnerID, err := parse(identity.TreePointer.OwnerID)
	if err != nil {
		return err
	}
	verificationReceiptID, err := parse(identity.VerificationReceipt.ID)
	if err != nil {
		return err
	}
	receipt := storage.CandidateImplementationFreezeModel{
		ID: receiptID, ProjectID: proposalModel.ProjectID,
		SessionID: sessionID, CandidateID: candidateID,
		CandidateSnapshotID: checkpointID, ImplementationProposalID: proposalModel.ID,
		RequestKey: identity.RequestKey, RequestHash: identity.RequestHash,
		SessionVersion:             identity.ExpectedSessionVersion,
		CandidateVersion:           identity.ExpectedCandidateVersion,
		JournalSequence:            identity.JournalSequence,
		SessionEpoch:               identity.ExpectedSessionEpoch,
		WriterLeaseEpoch:           identity.ExpectedWriterLeaseEpoch,
		BaseTreeHash:               identity.BaseTreeHash,
		CandidateTreeStore:         identity.TreePointer.Store,
		CandidateTreeOwnerID:       treeOwnerID,
		CandidateTreeRef:           identity.TreePointer.Ref,
		CandidateTreeContentHash:   identity.TreePointer.ContentObjectHash,
		CandidateTreeHash:          identity.TreePointer.TreeHash,
		VerificationBindingVersion: candidateVerificationBindingContractVersion,
		VerificationReceiptID:      &verificationReceiptID,
		VerificationReceiptHash:    nonEmptyStringPointer(identity.VerificationReceipt.ContentHash),
		BuildManifestID:            proposalModel.BuildManifestID,
		BuildManifestHash:          identity.BuildManifest.ContentHash,
		BuildContractID:            buildContractID,
		BuildContractHash:          identity.BuildContract.ContentHash,
		FullStackTemplateID:        fullStackTemplateID,
		FullStackTemplateHash:      identity.FullStackTemplate.ContentHash,
		BaseWorkspaceArtifactID:    baseArtifactID,
		BaseWorkspaceRevisionID:    baseRevisionID,
		BaseWorkspaceContentHash:   baseContentHash,
		ProposalPayloadHash:        proposal.PayloadHash,
		OperationCount:             len(proposal.Operations),
		Reason:                     identity.Reason, CreatedBy: actorID, CreatedAt: now,
	}
	if err := transaction.Create(&receipt).Error; err != nil {
		return err
	}
	var frozenVersion int64
	result := transaction.Raw(
		"SELECT freeze_candidate_workspace(?, ?, ?, ?, ?, ?, ?) AS frozen_version",
		candidateID, int64(identity.ExpectedCandidateVersion),
		int64(identity.ExpectedSessionEpoch), int64(identity.ExpectedWriterLeaseEpoch),
		actorID, checkpointID, identity.Reason,
	).Scan(&frozenVersion)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 || frozenVersion != int64(identity.ExpectedCandidateVersion)+1 {
		return ErrConflict
	}
	var synced struct {
		SessionVersion    int64  `gorm:"column:session_version"`
		SessionEpoch      int64  `gorm:"column:session_epoch"`
		CandidateVersion  int64  `gorm:"column:candidate_version"`
		CandidateTreeHash string `gorm:"column:candidate_tree_hash"`
	}
	result = transaction.Raw(
		"SELECT session_version, session_epoch, candidate_version, candidate_tree_hash "+
			"FROM sync_sandbox_session_candidate(?, ?, ?, ?)",
		sessionID, int64(identity.ExpectedSessionVersion), int64(identity.ExpectedSessionEpoch), actorID,
	).Scan(&synced)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 ||
		synced.SessionVersion != int64(identity.ExpectedSessionVersion)+1 ||
		synced.SessionEpoch != int64(identity.ExpectedSessionEpoch) ||
		synced.CandidateVersion != frozenVersion ||
		synced.CandidateTreeHash != identity.TreePointer.TreeHash {
		return ErrConflict
	}
	if err := insertAudit(
		transaction, proposalModel.ProjectID, actorID, "candidate.implementation_frozen",
		"candidate_implementation_freeze", receiptID.String(), map[string]any{
			"sessionId": sessionID.String(), "candidateId": candidateID.String(),
			"candidateSnapshotId": checkpointID.String(), "proposalId": proposal.ID,
			"candidateTreeHash": identity.TreePointer.TreeHash,
		},
	); err != nil {
		return err
	}
	return enqueue(
		transaction, "candidate_implementation_freeze", receiptID.String(),
		"candidate.implementation_frozen", "worksflow.candidate.implementation.frozen",
		map[string]any{
			"projectId": proposal.ProjectID, "sessionId": identity.SessionID,
			"candidateId": identity.CandidateID, "candidateSnapshotId": identity.CheckpointID,
			"proposalId": proposal.ID, "candidateTreeHash": identity.TreePointer.TreeHash,
		},
	)
}

func exactCandidateImplementationReplay(
	model storage.CandidateImplementationFreezeModel,
	request CandidateImplementationRequest,
	requestHash string,
) error {
	if model.ID == uuid.Nil || model.ProjectID == uuid.Nil ||
		model.SessionID.String() != request.SessionID ||
		model.CandidateID.String() != request.CandidateID ||
		model.CandidateSnapshotID.String() != request.CheckpointID ||
		model.RequestKey != request.RequestKey || model.RequestHash != requestHash ||
		model.SessionVersion != request.ExpectedSessionVersion ||
		model.CandidateVersion != request.ExpectedCandidateVersion ||
		model.SessionEpoch != request.ExpectedSessionEpoch ||
		model.WriterLeaseEpoch != request.ExpectedWriterLeaseEpoch ||
		model.Reason != request.Reason || model.VerificationBindingVersion != candidateVerificationBindingContractVersion ||
		model.VerificationReceiptID == nil || model.VerificationReceiptID.String() != request.VerificationReceipt.ID ||
		model.VerificationReceiptHash == nil || *model.VerificationReceiptHash != request.VerificationReceipt.ContentHash {
		return fmt.Errorf("%w: Idempotency-Key is bound to a different Candidate freeze payload", ErrConflict)
	}
	return nil
}

func candidateImplementationReceipt(
	model storage.CandidateImplementationFreezeModel,
) CandidateImplementationFreezeReceipt {
	return CandidateImplementationFreezeReceipt{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(),
		SessionID: model.SessionID.String(), CandidateID: model.CandidateID.String(),
		CandidateSnapshotID:      model.CandidateSnapshotID.String(),
		VerificationReceipt:      candidateVerificationReceiptReference(model),
		ImplementationProposalID: model.ImplementationProposalID.String(),
		RequestKey:               model.RequestKey, RequestHash: model.RequestHash,
		SessionVersion: model.SessionVersion, CandidateVersion: model.CandidateVersion,
		JournalSequence: model.JournalSequence, SessionEpoch: model.SessionEpoch,
		WriterLeaseEpoch: model.WriterLeaseEpoch,
		BaseTreeHash:     model.BaseTreeHash, CandidateTreeHash: model.CandidateTreeHash,
		BuildManifest: repository.ExactReference{
			ID: model.BuildManifestID.String(), ContentHash: model.BuildManifestHash,
		},
		BuildContract: repository.ExactReference{
			ID: model.BuildContractID.String(), ContentHash: model.BuildContractHash,
		},
		FullStackTemplate: repository.ExactReference{
			ID: model.FullStackTemplateID.String(), ContentHash: model.FullStackTemplateHash,
		},
		BaseWorkspaceRevision: candidateImplementationReceiptBase(model),
		ProposalPayloadHash:   model.ProposalPayloadHash,
		OperationCount:        model.OperationCount, Reason: model.Reason,
		CreatedBy: model.CreatedBy.String(), CreatedAt: model.CreatedAt.UTC(),
	}
}

func candidateImplementationTraceLinks(identity CandidateImplementationIdentity) []json.RawMessage {
	candidate, _ := json.Marshal(map[string]any{
		"kind":                "candidate_snapshot",
		"candidateId":         identity.CandidateID,
		"candidateSnapshotId": identity.CheckpointID,
		"baseTreeHash":        identity.BaseTreeHash,
		"treeHash":            identity.TreePointer.TreeHash,
	})
	verification, _ := json.Marshal(map[string]any{
		"kind":        "candidate_verification_receipt",
		"id":          identity.VerificationReceipt.ID,
		"contentHash": identity.VerificationReceipt.ContentHash,
	})
	return []json.RawMessage{candidate, verification}
}

func cloneCandidateImplementationSource(
	identity *CandidateImplementationIdentity,
) *CandidateImplementationSource {
	if identity == nil {
		return nil
	}
	return &CandidateImplementationSource{
		FreezeReceiptID:      identity.FreezeReceiptID,
		RepositorySnapshotID: identity.RepositorySnapshotID,
		SessionID:            identity.SessionID,
		CandidateID:          identity.CandidateID, CandidateSnapshotID: identity.CheckpointID,
		CandidateVersion: identity.ExpectedCandidateVersion,
		JournalSequence:  identity.JournalSequence,
		SessionEpoch:     identity.ExpectedSessionEpoch,
		WriterLeaseEpoch: identity.ExpectedWriterLeaseEpoch,
		BaseTreeHash:     identity.BaseTreeHash, TreeHash: identity.TreePointer.TreeHash,
		FullStackTemplate: ExactContentReference{
			ID:          identity.FullStackTemplate.ID,
			ContentHash: identity.FullStackTemplate.ContentHash,
		},
		VerificationReceipt: ExactContentReference{
			ID: identity.VerificationReceipt.ID, ContentHash: identity.VerificationReceipt.ContentHash,
		},
	}
}

func candidateSnapshotUUID(identity *CandidateImplementationIdentity) *uuid.UUID {
	if identity == nil {
		return nil
	}
	value := uuid.MustParse(identity.CheckpointID)
	return &value
}

func candidateBaseTreeHash(identity *CandidateImplementationIdentity) *string {
	if identity == nil {
		return nil
	}
	value := identity.BaseTreeHash
	return &value
}

func candidateTreeHash(identity *CandidateImplementationIdentity) *string {
	if identity == nil {
		return nil
	}
	value := identity.TreePointer.TreeHash
	return &value
}

func candidateVerificationBindingVersion(identity *CandidateImplementationIdentity) *string {
	if identity == nil {
		return nil
	}
	value := candidateVerificationBindingContractVersion
	return &value
}

func candidateVerificationReceiptUUID(identity *CandidateImplementationIdentity) *uuid.UUID {
	if identity == nil {
		return nil
	}
	value := uuid.MustParse(identity.VerificationReceipt.ID)
	return &value
}

func candidateVerificationReceiptHash(identity *CandidateImplementationIdentity) *string {
	if identity == nil {
		return nil
	}
	value := identity.VerificationReceipt.ContentHash
	return &value
}

func candidateVerificationReceiptReference(model storage.CandidateImplementationFreezeModel) repository.ExactReference {
	if model.VerificationReceiptID == nil || model.VerificationReceiptHash == nil {
		return repository.ExactReference{}
	}
	return repository.ExactReference{
		ID: model.VerificationReceiptID.String(), ContentHash: *model.VerificationReceiptHash,
	}
}

func candidateImplementationVersionRef(
	reference *repository.ExactRevisionReference,
) *VersionRef {
	if reference == nil {
		return nil
	}
	return &VersionRef{
		ArtifactID: reference.ArtifactID, RevisionID: reference.RevisionID,
		ContentHash: reference.ContentHash,
	}
}

func cloneCandidateImplementationRevision(
	reference *repository.ExactRevisionReference,
) *repository.ExactRevisionReference {
	if reference == nil {
		return nil
	}
	cloned := *reference
	return &cloned
}

func candidateImplementationRevisionMatchesVersionRef(
	reference *repository.ExactRevisionReference,
	version *VersionRef,
) bool {
	return optionalVersionRefsEqual(candidateImplementationVersionRef(reference), version)
}

func candidateImplementationReceiptBase(
	model storage.CandidateImplementationFreezeModel,
) *repository.ExactRevisionReference {
	if model.BaseWorkspaceArtifactID == nil || model.BaseWorkspaceRevisionID == nil ||
		model.BaseWorkspaceContentHash == nil {
		return nil
	}
	return &repository.ExactRevisionReference{
		ArtifactID: model.BaseWorkspaceArtifactID.String(), RevisionID: model.BaseWorkspaceRevisionID.String(),
		ContentHash: *model.BaseWorkspaceContentHash,
	}
}

func candidateImplementationReceiptBaseMatches(
	model storage.CandidateImplementationFreezeModel,
	reference *VersionRef,
) bool {
	allAbsent := model.BaseWorkspaceArtifactID == nil && model.BaseWorkspaceRevisionID == nil &&
		model.BaseWorkspaceContentHash == nil
	allPresent := model.BaseWorkspaceArtifactID != nil && model.BaseWorkspaceRevisionID != nil &&
		model.BaseWorkspaceContentHash != nil
	if !allAbsent && !allPresent {
		return false
	}
	return candidateImplementationRevisionMatchesVersionRef(
		candidateImplementationReceiptBase(model),
		reference,
	)
}
