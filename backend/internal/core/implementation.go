package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ImplementationDecision string

const (
	ImplementationPending  ImplementationDecision = "pending"
	ImplementationAccepted ImplementationDecision = "accepted"
	ImplementationRejected ImplementationDecision = "rejected"
	ImplementationApplied  ImplementationDecision = "applied"
)

type ImplementationExecutionSource string

const (
	ImplementationSourceManualSubmission    ImplementationExecutionSource = "manual_submission"
	ImplementationSourceManualGeneration    ImplementationExecutionSource = "manual_generation"
	ImplementationSourceWorkflowRunner      ImplementationExecutionSource = "workflow_runner"
	ImplementationSourceConversationCommand ImplementationExecutionSource = "conversation_command"
	ImplementationSourceCandidateFreeze     ImplementationExecutionSource = "candidate_freeze"
)

type FileOperation struct {
	ID           string                 `json:"id"`
	Kind         string                 `json:"kind"`
	Path         string                 `json:"path"`
	FromPath     string                 `json:"fromPath,omitempty"`
	Content      *string                `json:"content,omitempty"`
	Language     string                 `json:"language,omitempty"`
	Mode         string                 `json:"mode,omitempty"`
	ExpectedHash string                 `json:"expectedHash,omitempty"`
	DependsOn    []string               `json:"dependsOn,omitempty"`
	Rationale    string                 `json:"rationale,omitempty"`
	TraceSource  []string               `json:"traceSource,omitempty"`
	Decision     ImplementationDecision `json:"decision"`
	DecidedBy    string                 `json:"decidedBy,omitempty"`
	Reason       string                 `json:"reason,omitempty"`
}

type CandidateImplementationSource struct {
	FreezeReceiptID      string                `json:"freezeReceiptId"`
	RepositorySnapshotID string                `json:"repositorySnapshotId"`
	SessionID            string                `json:"sessionId"`
	CandidateID          string                `json:"candidateId"`
	CandidateSnapshotID  string                `json:"candidateSnapshotId"`
	CandidateVersion     uint64                `json:"candidateVersion"`
	JournalSequence      uint64                `json:"journalSequence"`
	SessionEpoch         uint64                `json:"sessionEpoch"`
	WriterLeaseEpoch     uint64                `json:"writerLeaseEpoch"`
	BaseTreeHash         string                `json:"baseTreeHash"`
	TreeHash             string                `json:"treeHash"`
	FullStackTemplate    ExactContentReference `json:"fullStackTemplate"`
	VerificationReceipt  ExactContentReference `json:"verificationReceipt"`
}

// ApplicationBuildContractRef is the exact semantic identity that authorizes
// implementation work. ID alone is deliberately insufficient because a
// caller must pin the canonical contract payload it reviewed.
type ApplicationBuildContractRef struct {
	ID           string `json:"id"`
	ContractHash string `json:"contractHash"`
}

type ExactContentReference struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

// ApplicationBuildContractVerifier is implemented by the constructor
// boundary. Core depends on this small interface so proposal creation and
// apply can fail closed without importing the constructor package.
type ApplicationBuildContractVerifier interface {
	RequireReadyForImplementation(
		context.Context,
		string,
		string,
		string,
		ApplicationBuildContractRef,
	) (ApplicationBuildContractRef, error)
}

type ImplementationProposal struct {
	ID                       string                         `json:"id"`
	ProjectID                string                         `json:"projectId"`
	BuildManifestID          string                         `json:"buildManifestId"`
	ApplicationBuildContract *ApplicationBuildContractRef   `json:"applicationBuildContract,omitempty"`
	BaseWorkspaceRevision    *VersionRef                    `json:"baseWorkspaceRevision,omitempty"`
	ExecutionSource          ImplementationExecutionSource  `json:"executionSource"`
	ConversationCommandID    *string                        `json:"conversationCommandId,omitempty"`
	SupersedesProposalID     *string                        `json:"supersedesProposalId,omitempty"`
	InstructionHash          string                         `json:"instructionHash,omitempty"`
	AIProvider               string                         `json:"aiProvider,omitempty"`
	AIModel                  string                         `json:"aiModel,omitempty"`
	CandidateSource          *CandidateImplementationSource `json:"candidateSource,omitempty"`
	Operations               []FileOperation                `json:"operations"`
	Routes                   []json.RawMessage              `json:"routes"`
	APIs                     []json.RawMessage              `json:"apis"`
	Migrations               []json.RawMessage              `json:"migrations"`
	Tests                    []json.RawMessage              `json:"tests"`
	Previews                 []json.RawMessage              `json:"previews"`
	TraceLinks               []json.RawMessage              `json:"traceLinks"`
	Diagnostics              []ValidationFinding            `json:"diagnostics"`
	Assumptions              []string                       `json:"assumptions"`
	UnimplementedItems       []string                       `json:"unimplementedItems"`
	Status                   string                         `json:"status"`
	Version                  uint64                         `json:"version"`
	PayloadHash              string                         `json:"payloadHash"`
	CreatedBy                string                         `json:"createdBy"`
	CreatedAt                time.Time                      `json:"createdAt"`
	AppliedAt                *time.Time                     `json:"appliedAt,omitempty"`
}

type CreateImplementationProposalInput struct {
	BuildManifestID          string                      `json:"buildManifestId"`
	ApplicationBuildContract ApplicationBuildContractRef `json:"applicationBuildContract"`
	Operations               []FileOperation             `json:"operations"`
	Routes                   []json.RawMessage           `json:"routes,omitempty"`
	APIs                     []json.RawMessage           `json:"apis,omitempty"`
	Migrations               []json.RawMessage           `json:"migrations,omitempty"`
	Tests                    []json.RawMessage           `json:"tests,omitempty"`
	Previews                 []json.RawMessage           `json:"previews,omitempty"`
	TraceLinks               []json.RawMessage           `json:"traceLinks,omitempty"`
	Diagnostics              []ValidationFinding         `json:"diagnostics,omitempty"`
	Assumptions              []string                    `json:"assumptions,omitempty"`
	UnimplementedItems       []string                    `json:"unimplementedItems,omitempty"`
}

// GeneratedImplementationIdentity is server-only execution provenance. It is
// never decoded from the public CreateImplementationProposalInput contract.
// ClaimToken fences the AI worker at the PostgreSQL checkpoint after the
// potentially long provider call.
type GeneratedImplementationIdentity struct {
	ProposalID                    string
	RequestKey                    string
	ExecutionSource               ImplementationExecutionSource
	ConversationCommandID         *string
	ExpectedActiveProposalID      *string
	ExpectedActiveProposalVersion uint64
	InstructionHash               string
	AIProvider                    string
	AIModel                       string
	ClaimToken                    string
	ApplicationBuildContract      ApplicationBuildContractRef
}

type DecideImplementationInput struct {
	OperationID string                 `json:"operationId"`
	Decision    ImplementationDecision `json:"decision"`
	Reason      string                 `json:"reason,omitempty"`
	Version     uint64                 `json:"version"`
}

type ApplyImplementationInput struct {
	Version uint64 `json:"version"`
}

type QuarantineImplementationInput struct {
	Version uint64 `json:"version"`
	Reason  string `json:"reason"`
}

type ImplementationService struct {
	database       *gorm.DB
	contents       content.Store
	access         *AccessControl
	workbench      *WorkbenchService
	buildContracts ApplicationBuildContractVerifier
	now            func() time.Time
}

func NewImplementationService(
	database *gorm.DB,
	contents content.Store,
	access *AccessControl,
	buildContracts ApplicationBuildContractVerifier,
) (*ImplementationService, error) {
	if buildContracts == nil {
		return nil, errors.New("Application Build Contract verifier is required")
	}
	workbench, err := NewWorkbenchService(database, contents, access)
	if err != nil {
		return nil, err
	}
	return &ImplementationService{
		database: database, contents: contents, access: access, workbench: workbench,
		buildContracts: buildContracts, now: time.Now,
	}, nil
}

func (s *ImplementationService) requireApplicationBuildContract(
	ctx context.Context,
	projectID, buildManifestID, actorID string,
	selection ApplicationBuildContractRef,
) (ApplicationBuildContractRef, error) {
	if s == nil || s.buildContracts == nil {
		return ApplicationBuildContractRef{}, fmt.Errorf("%w: Application Build Contract verifier is unavailable", ErrBlockingGate)
	}
	normalized, err := normalizeApplicationBuildContractRef(selection)
	if err != nil {
		return ApplicationBuildContractRef{}, err
	}
	verified, err := s.buildContracts.RequireReadyForImplementation(
		ctx, projectID, buildManifestID, actorID, normalized,
	)
	if err != nil {
		return ApplicationBuildContractRef{}, err
	}
	verified, err = normalizeApplicationBuildContractRef(verified)
	if err != nil || !sameApplicationBuildContractRef(normalized, verified) {
		return ApplicationBuildContractRef{}, fmt.Errorf("%w: Application Build Contract verifier returned a different identity", ErrConflict)
	}
	return verified, nil
}

func normalizeApplicationBuildContractRef(value ApplicationBuildContractRef) (ApplicationBuildContractRef, error) {
	if value.ID == "" || value.ID != strings.TrimSpace(value.ID) ||
		value.ContractHash == "" || value.ContractHash != strings.TrimSpace(value.ContractHash) ||
		!domain.IsCanonicalHash(value.ContractHash) {
		return ApplicationBuildContractRef{}, fmt.Errorf("%w: exact Application Build Contract reference", ErrInvalidInput)
	}
	id, err := uuid.Parse(value.ID)
	if err != nil {
		return ApplicationBuildContractRef{}, fmt.Errorf("%w: Application Build Contract id", ErrInvalidInput)
	}
	digest := strings.TrimPrefix(value.ContractHash, "sha256:")
	if digest != strings.ToLower(digest) {
		return ApplicationBuildContractRef{}, fmt.Errorf("%w: Application Build Contract hash", ErrInvalidInput)
	}
	return ApplicationBuildContractRef{ID: id.String(), ContractHash: value.ContractHash}, nil
}

func sameApplicationBuildContractRef(left, right ApplicationBuildContractRef) bool {
	return left.ID == right.ID && left.ContractHash == right.ContractHash
}

func cloneApplicationBuildContractRef(value *ApplicationBuildContractRef) *ApplicationBuildContractRef {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func exactApplicationBuildContractFromProposal(
	proposal ImplementationProposal,
	model storage.ImplementationProposalModel,
) (ApplicationBuildContractRef, error) {
	if proposal.ApplicationBuildContract == nil || model.ApplicationBuildContractID == nil || model.ApplicationBuildContractHash == nil {
		return ApplicationBuildContractRef{}, fmt.Errorf("%w: implementation proposal predates the exact Build Contract gate", ErrBlockingGate)
	}
	ref, err := normalizeApplicationBuildContractRef(*proposal.ApplicationBuildContract)
	if err != nil || model.ApplicationBuildContractID.String() != ref.ID || *model.ApplicationBuildContractHash != ref.ContractHash {
		return ApplicationBuildContractRef{}, fmt.Errorf("%w: implementation proposal Build Contract binding drift", ErrConflict)
	}
	return ref, nil
}

func optionalApplicationBuildContractMatchesModel(
	value *ApplicationBuildContractRef,
	model storage.ImplementationProposalModel,
) bool {
	if value == nil || model.ApplicationBuildContractID == nil || model.ApplicationBuildContractHash == nil {
		return value == nil && model.ApplicationBuildContractID == nil && model.ApplicationBuildContractHash == nil
	}
	normalized, err := normalizeApplicationBuildContractRef(*value)
	return err == nil && normalized.ID == model.ApplicationBuildContractID.String() &&
		normalized.ContractHash == *model.ApplicationBuildContractHash
}

func nonNilUUIDPointer(value uuid.UUID) *uuid.UUID {
	copy := value
	return &copy
}

func (s *ImplementationService) Create(ctx context.Context, projectID, actorID string, input CreateImplementationProposalInput) (ImplementationProposal, error) {
	return s.create(ctx, projectID, actorID, input, nil, nil)
}

func (s *ImplementationService) CreateGenerated(
	ctx context.Context,
	projectID, actorID string,
	input CreateImplementationProposalInput,
	identity GeneratedImplementationIdentity,
) (ImplementationProposal, error) {
	return s.create(ctx, projectID, actorID, input, &identity, nil)
}

func (s *ImplementationService) create(
	ctx context.Context,
	projectID, actorID string,
	input CreateImplementationProposalInput,
	identity *GeneratedImplementationIdentity,
	candidateIdentity *CandidateImplementationIdentity,
) (ImplementationProposal, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionEdit); err != nil {
		return ImplementationProposal{}, err
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return ImplementationProposal{}, err
	}
	verifiedContract, err := s.requireApplicationBuildContract(
		ctx, projectID, input.BuildManifestID, actorID, input.ApplicationBuildContract,
	)
	if err != nil {
		return ImplementationProposal{}, err
	}
	input.ApplicationBuildContract = verifiedContract
	bundle, err := s.workbench.GetBundleForGeneration(ctx, input.BuildManifestID, actorID)
	if err != nil {
		return ImplementationProposal{}, err
	}
	if bundle.ProjectID != projectID {
		return ImplementationProposal{}, ErrNotFound
	}
	if candidateIdentity != nil {
		if err := validateCandidateImplementationCreate(bundle, verifiedContract, *candidateIdentity); err != nil {
			return ImplementationProposal{}, err
		}
	}
	if err := validateFileOperations(input.Operations); err != nil {
		return ImplementationProposal{}, err
	}
	proposalID := uuid.New()
	executionSource := ImplementationSourceManualSubmission
	var conversationCommandID, supersedesProposalID *uuid.UUID
	var instructionHash, aiProvider, aiModel string
	var requestKey, claimToken uuid.UUID
	if identity != nil {
		proposalID, requestKey, claimToken, conversationCommandID, supersedesProposalID, err = validateGeneratedImplementationIdentity(*identity)
		if err != nil {
			return ImplementationProposal{}, err
		}
		executionSource = identity.ExecutionSource
		if !sameApplicationBuildContractRef(identity.ApplicationBuildContract, verifiedContract) {
			return ImplementationProposal{}, fmt.Errorf("%w: generated proposal Build Contract binding", ErrConflict)
		}
		instructionHash = identity.InstructionHash
		aiProvider = strings.TrimSpace(identity.AIProvider)
		aiModel = strings.TrimSpace(identity.AIModel)
	} else if candidateIdentity != nil {
		proposalID, err = uuid.Parse(candidateIdentity.ProposalID)
		if err != nil {
			return ImplementationProposal{}, fmt.Errorf("%w: reserved Candidate implementation proposal id", ErrInvalidInput)
		}
		executionSource = ImplementationSourceCandidateFreeze
	}
	now := s.now().UTC()
	var supersededProposal ImplementationProposal
	var supersededModel storage.ImplementationProposalModel
	var supersededContentRef content.Reference
	if supersedesProposalID != nil {
		supersededProposal, supersededModel, err = s.load(ctx, supersedesProposalID.String())
		if err != nil {
			return ImplementationProposal{}, err
		}
		if identity == nil || identity.ExpectedActiveProposalVersion == 0 ||
			supersededModel.ProjectID != projectUUID || supersededModel.BuildManifestID.String() != input.BuildManifestID ||
			supersededProposal.Version != identity.ExpectedActiveProposalVersion || supersededProposal.Status != "open" ||
			supersededModel.AcceptedCount != 0 || supersededModel.RejectedCount != 0 || supersededModel.AppliedAt != nil {
			return ImplementationProposal{}, ErrConflict
		}
		for _, operation := range supersededProposal.Operations {
			if operation.Decision != ImplementationPending {
				return ImplementationProposal{}, ErrConflict
			}
		}
		supersededProposal.Status = "stale"
		supersededProposal.Version++
		stalePayload, marshalErr := json.Marshal(supersededProposal)
		if marshalErr != nil {
			return ImplementationProposal{}, marshalErr
		}
		supersededContentRef, err = s.contents.PutPending(
			ctx, projectID, "implementation_proposal", supersededProposal.ID, 1, stalePayload,
		)
		if err != nil {
			return ImplementationProposal{}, err
		}
	}
	proposal := ImplementationProposal{
		ID: proposalID.String(), ProjectID: projectID, BuildManifestID: input.BuildManifestID,
		ApplicationBuildContract: cloneApplicationBuildContractRef(&verifiedContract),
		BaseWorkspaceRevision:    cloneVersionRef(bundle.CurrentWorkspaceRevision),
		ExecutionSource:          executionSource, ConversationCommandID: uuidStringPointer(conversationCommandID),
		SupersedesProposalID: uuidStringPointer(supersedesProposalID),
		InstructionHash:      instructionHash, AIProvider: aiProvider, AIModel: aiModel,
		CandidateSource: cloneCandidateImplementationSource(candidateIdentity),
		Operations:      cloneFileOperations(input.Operations), Routes: cloneRawMessages(input.Routes),
		APIs: cloneRawMessages(input.APIs), Migrations: cloneRawMessages(input.Migrations),
		Tests: cloneRawMessages(input.Tests), Previews: cloneRawMessages(input.Previews),
		TraceLinks: cloneRawMessages(input.TraceLinks), Diagnostics: append([]ValidationFinding(nil), input.Diagnostics...),
		Assumptions:        append([]string(nil), input.Assumptions...),
		UnimplementedItems: append([]string(nil), input.UnimplementedItems...),
		Status:             "open", Version: 1, CreatedBy: actorID, CreatedAt: now,
	}
	unimplementedCount, blockingDiagnosticCount := implementationIncompleteCounts(proposal)
	payloadHash, err := implementationPayloadHash(proposal)
	if err != nil {
		return ImplementationProposal{}, err
	}
	proposal.PayloadHash = payloadHash
	payload, err := json.Marshal(proposal)
	if err != nil {
		return ImplementationProposal{}, err
	}
	contentRef, err := s.contents.PutPending(ctx, projectID, "implementation_proposal", proposal.ID, 1, payload)
	if err != nil {
		return ImplementationProposal{}, err
	}
	abortPending := true
	defer func() {
		if abortPending {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
			if supersededContentRef.ID != "" {
				_ = s.contents.Abort(context.Background(), supersededContentRef.ID)
			}
		}
	}()
	buildManifestUUID := uuid.MustParse(input.BuildManifestID)
	var baseRevisionID *uuid.UUID
	if proposal.BaseWorkspaceRevision != nil {
		parsed := uuid.MustParse(proposal.BaseWorkspaceRevision.RevisionID)
		baseRevisionID = &parsed
	}
	model := storage.ImplementationProposalModel{
		ID: proposalID, ProjectID: projectUUID, BuildManifestID: buildManifestUUID,
		ApplicationBuildContractID:   nonNilUUIDPointer(uuid.MustParse(verifiedContract.ID)),
		ApplicationBuildContractHash: nonEmptyStringPointer(verifiedContract.ContractHash),
		BaseWorkspaceRevisionID:      baseRevisionID, Status: proposal.Status, Version: proposal.Version,
		ExecutionSource: string(executionSource), ConversationCommandID: conversationCommandID,
		SupersedesProposalID: supersedesProposalID,
		InstructionHash:      nonEmptyStringPointer(instructionHash), AIProvider: nonEmptyStringPointer(aiProvider), AIModel: nonEmptyStringPointer(aiModel),
		CandidateSnapshotID:                 candidateSnapshotUUID(candidateIdentity),
		CandidateBaseTreeHash:               candidateBaseTreeHash(candidateIdentity),
		CandidateTreeHash:                   candidateTreeHash(candidateIdentity),
		CandidateVerificationBindingVersion: candidateVerificationBindingVersion(candidateIdentity),
		CandidateVerificationReceiptID:      candidateVerificationReceiptUUID(candidateIdentity),
		CandidateVerificationReceiptHash:    candidateVerificationReceiptHash(candidateIdentity),
		ContentStore:                        "mongo", ContentRef: contentRef.ID, ContentHash: contentRef.ContentHash,
		PayloadHash: proposal.PayloadHash, OperationCount: len(proposal.Operations),
		UnimplementedCount: &unimplementedCount, BlockingDiagnosticCount: &blockingDiagnosticCount,
		CreatedBy: actorUUID, CreatedAt: now,
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := ensureManifestRootHasNoAppliedProposal(transaction, model); err != nil {
			return err
		}
		manifest, err := lockFrozenManifestLeaf(transaction, model.BuildManifestID, model.ProjectID)
		if err != nil {
			return err
		}
		if err := ensureWorkflowManifestOrdinalReady(ctx, transaction, manifest); err != nil {
			return err
		}
		if identity != nil {
			if err := validateImplementationGenerationClaim(
				transaction, model, requestKey, claimToken, executionSource, conversationCommandID,
				supersedesProposalID, identity.ExpectedActiveProposalVersion, instructionHash,
				verifiedContract, actorUUID, now,
			); err != nil {
				return err
			}
		}
		if supersedesProposalID != nil {
			result := transaction.Model(&storage.ImplementationProposalModel{}).Where(
				"id = ? AND project_id = ? AND build_manifest_id = ? AND version = ? AND status = 'open' AND accepted_count = 0 AND rejected_count = 0 AND applied_at IS NULL",
				*supersedesProposalID, projectUUID, buildManifestUUID, identity.ExpectedActiveProposalVersion,
			).Updates(map[string]any{
				"status": "stale", "version": identity.ExpectedActiveProposalVersion + 1,
				"content_ref": supersededContentRef.ID, "content_hash": supersededContentRef.ContentHash,
			})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrConflict
			}
			if err := insertAudit(transaction, projectUUID, actorUUID, "implementation.proposal_superseded", "implementation_proposal", supersedesProposalID.String(), map[string]any{
				"replacementProposalId": proposal.ID, "buildManifestId": input.BuildManifestID,
				"expectedVersion": identity.ExpectedActiveProposalVersion,
			}); err != nil {
				return err
			}
			if err := enqueue(transaction, "implementation_proposal", supersedesProposalID.String(), "implementation.proposal_superseded", "worksflow.implementation.proposal.superseded", map[string]any{
				"projectId": projectID, "proposalId": supersedesProposalID.String(), "replacementProposalId": proposal.ID,
			}); err != nil {
				return err
			}
		}
		var activeProposalCount int64
		if err := transaction.Model(&storage.ImplementationProposalModel{}).Where(
			"build_manifest_id = ? AND status IN ?", buildManifestUUID, []string{"open", "reviewing", "ready"},
		).Count(&activeProposalCount).Error; err != nil {
			return err
		}
		if activeProposalCount != 0 {
			return ErrConflict
		}
		if err := transaction.Create(&model).Error; err != nil {
			return err
		}
		if candidateIdentity != nil {
			if err := persistCandidateImplementationFreeze(
				transaction, model, proposal, *candidateIdentity, actorUUID, now,
			); err != nil {
				return err
			}
		}
		if identity != nil {
			result := transaction.Model(&storage.ImplementationGenerationClaimModel{}).Where(
				"request_key = ? AND build_manifest_id = ? AND claim_token = ? AND status = 'processing' AND claim_expires_at >= ?",
				requestKey, buildManifestUUID, claimToken, now,
			).Updates(map[string]any{
				"status": "completed", "completed_proposal_id": proposalID,
				"claim_token": nil, "claim_expires_at": nil,
				"last_failure": nil, "last_failed_at": nil, "updated_at": now,
			})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrConflict
			}
		}
		auditMetadata := map[string]any{
			"buildManifestId": input.BuildManifestID, "executionSource": executionSource,
			"applicationBuildContractId":   verifiedContract.ID,
			"applicationBuildContractHash": verifiedContract.ContractHash,
			"instructionHash":              instructionHash, "conversationCommandId": uuidStringPointer(conversationCommandID),
			"supersedesProposalId": uuidStringPointer(supersedesProposalID),
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "implementation.proposal_created", "implementation_proposal", proposal.ID, auditMetadata); err != nil {
			return err
		}
		return enqueue(transaction, "implementation_proposal", proposal.ID, "implementation.proposal_created", "worksflow.implementation.proposal.created", map[string]any{
			"projectId": projectID, "proposalId": proposal.ID, "buildManifestId": input.BuildManifestID,
			"executionSource": executionSource, "instructionHash": instructionHash,
			"applicationBuildContractId":   verifiedContract.ID,
			"applicationBuildContractHash": verifiedContract.ContractHash,
		})
	})
	if err != nil {
		return ImplementationProposal{}, err
	}
	abortPending = false
	finalizeIDs := []string{contentRef.ID}
	if supersededContentRef.ID != "" {
		finalizeIDs = append(finalizeIDs, supersededContentRef.ID)
	}
	var finalizeErrors []error
	for _, contentID := range finalizeIDs {
		if err := s.contents.Finalize(ctx, contentID); err != nil {
			finalizeErrors = append(finalizeErrors, err)
		}
	}
	if err := errors.Join(finalizeErrors...); err != nil {
		return ImplementationProposal{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return proposal, nil
}

func (s *ImplementationService) Get(ctx context.Context, proposalID, actorID string) (ImplementationProposal, error) {
	proposal, model, err := s.loadEnvelope(ctx, proposalID)
	if err != nil {
		return ImplementationProposal{}, err
	}
	if !historicalUnverifiedCandidateImplementation(model) {
		if err := s.validateCandidateImplementationSource(ctx, proposal, model); err != nil {
			return ImplementationProposal{}, err
		}
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, ActionView); err != nil {
		return ImplementationProposal{}, err
	}
	return proposal, nil
}

// Quarantine releases an active Workbench leaf that cannot enter the
// governed review gate: a retired direct-model Proposal, a manual Proposal, or
// pre-verification Candidate history. The old content and decisions remain
// immutable history, while an exact version fence and required audit reason
// prevent terminalization from being mistaken for Candidate verification.
func (s *ImplementationService) Quarantine(
	ctx context.Context,
	proposalID, actorID string,
	input QuarantineImplementationInput,
) (ImplementationProposal, error) {
	proposal, model, err := s.loadEnvelope(ctx, proposalID)
	if err != nil {
		return ImplementationProposal{}, err
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, ActionEdit); err != nil {
		return ImplementationProposal{}, err
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" || len(reason) > 1000 {
		return ImplementationProposal{}, fmt.Errorf("%w: quarantine reason is required and bounded", ErrInvalidInput)
	}
	if input.Version == 0 {
		input.Version = proposal.Version
	}
	if proposal.Version != input.Version {
		return ImplementationProposal{}, ErrConflict
	}
	if !quarantinableImplementationProposal(proposal, model) {
		return ImplementationProposal{}, fmt.Errorf("%w: only an unreviewable implementation Proposal can be quarantined", ErrInvalidInput)
	}
	if proposal.Status == "stale" {
		return proposal, nil
	}
	if proposal.Status != "open" && proposal.Status != "reviewing" && proposal.Status != "ready" {
		return ImplementationProposal{}, ErrConflict
	}

	proposal.Status = "stale"
	proposal.Version++
	proposal.AppliedAt = nil
	payload, err := json.Marshal(proposal)
	if err != nil {
		return ImplementationProposal{}, err
	}
	contentRef, err := s.contents.PutPending(
		ctx, model.ProjectID.String(), "implementation_proposal", proposal.ID, 1, payload,
	)
	if err != nil {
		return ImplementationProposal{}, err
	}
	abortPending := true
	defer func() {
		if abortPending {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	actorUUID := uuid.MustParse(actorID)
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		updated := transaction.Model(&storage.ImplementationProposalModel{}).
			Where("id = ? AND version = ? AND status IN ?", model.ID, input.Version, []string{"open", "reviewing", "ready"}).
			Updates(map[string]any{
				"status": proposal.Status, "version": proposal.Version,
				"content_ref": contentRef.ID, "content_hash": contentRef.ContentHash,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrConflict
		}
		metadata := map[string]any{
			"buildManifestId": model.BuildManifestID.String(),
			"executionSource": proposal.ExecutionSource,
			"reason":          reason,
			"priorVersion":    input.Version,
		}
		if err := insertAudit(
			transaction, model.ProjectID, actorUUID, "implementation.proposal_quarantined",
			"implementation_proposal", proposal.ID, metadata,
		); err != nil {
			return err
		}
		return enqueue(
			transaction, "implementation_proposal", proposal.ID,
			"implementation.proposal_quarantined", "worksflow.implementation.proposal.quarantined",
			map[string]any{
				"projectId": model.ProjectID.String(), "proposalId": proposal.ID,
				"buildManifestId": model.BuildManifestID.String(), "reason": reason,
			},
		)
	})
	if err != nil {
		return ImplementationProposal{}, err
	}
	abortPending = false
	if err := s.contents.Finalize(ctx, contentRef.ID); err != nil {
		return ImplementationProposal{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return proposal, nil
}

func (s *ImplementationService) Decide(ctx context.Context, proposalID, actorID string, input DecideImplementationInput) (ImplementationProposal, error) {
	proposal, model, err := s.load(ctx, proposalID)
	if err != nil {
		return ImplementationProposal{}, err
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, ActionEdit); err != nil {
		return ImplementationProposal{}, err
	}
	if input.Version == 0 {
		input.Version = proposal.Version
	}
	if proposal.Version != input.Version || proposal.Status == "applied" || proposal.Status == "partially_applied" || proposal.Status == "stale" {
		return ImplementationProposal{}, ErrConflict
	}
	if legacyAIImplementationSource(proposal.ExecutionSource) {
		return ImplementationProposal{}, fmt.Errorf(
			"%w: AI implementation must be produced by an exact verified Candidate freeze",
			ErrBlockingGate,
		)
	}
	if err := requireGovernedImplementationReview(proposal); err != nil {
		return ImplementationProposal{}, err
	}
	if err := ensureConversationProposalCommandExecuted(s.database.WithContext(ctx), model, false); err != nil {
		return ImplementationProposal{}, err
	}
	if input.Decision != ImplementationAccepted && input.Decision != ImplementationRejected {
		return ImplementationProposal{}, fmt.Errorf("%w: implementation decision", ErrInvalidInput)
	}
	binding, bindingErr := exactApplicationBuildContractFromProposal(proposal, model)
	if bindingErr != nil {
		if errors.Is(bindingErr, ErrBlockingGate) || errors.Is(bindingErr, ErrConflict) {
			return ImplementationProposal{}, s.persistProposalStale(ctx, proposal, model, actorID)
		}
		return ImplementationProposal{}, bindingErr
	}
	if _, bindingErr = s.requireApplicationBuildContract(
		ctx, model.ProjectID.String(), proposal.BuildManifestID, actorID, binding,
	); bindingErr != nil {
		if errors.Is(bindingErr, ErrBlockingGate) || errors.Is(bindingErr, ErrConflict) || errors.Is(bindingErr, ErrProposalStale) {
			return ImplementationProposal{}, s.persistProposalStale(ctx, proposal, model, actorID)
		}
		return ImplementationProposal{}, bindingErr
	}
	// Lineage staleness is authoritative for every interaction with an otherwise
	// mutable proposal. Persist it before validating the requested operation so a
	// client cannot keep an obsolete ready/partially-decided proposal visible by
	// submitting an already-decided or otherwise invalid operation.
	if _, err := s.workbench.GetBundleForGeneration(ctx, proposal.BuildManifestID, actorID); err != nil {
		if errors.Is(err, ErrBlockingGate) || errors.Is(err, ErrConflict) || errors.Is(err, ErrProposalStale) {
			return ImplementationProposal{}, s.persistProposalStale(ctx, proposal, model, actorID)
		}
		return ImplementationProposal{}, err
	}
	operationIndex := -1
	for index := range proposal.Operations {
		if proposal.Operations[index].ID == input.OperationID {
			operationIndex = index
			break
		}
	}
	if operationIndex < 0 {
		return ImplementationProposal{}, ErrNotFound
	}
	if proposal.Operations[operationIndex].Decision != ImplementationPending {
		return ImplementationProposal{}, ErrConflict
	}
	if input.Decision == ImplementationRejected && strings.TrimSpace(input.Reason) == "" {
		return ImplementationProposal{}, fmt.Errorf("%w: rejection reason", ErrInvalidInput)
	}
	staleProposal := proposal
	staleProposal.Operations = append([]FileOperation(nil), proposal.Operations...)
	proposal.Operations[operationIndex].Decision = input.Decision
	proposal.Operations[operationIndex].DecidedBy = actorID
	proposal.Operations[operationIndex].Reason = strings.TrimSpace(input.Reason)
	proposal.Version++
	proposal.Status = implementationStatus(proposal.Operations)
	payload, err := json.Marshal(proposal)
	if err != nil {
		return ImplementationProposal{}, err
	}
	contentRef, err := s.contents.PutPending(ctx, model.ProjectID.String(), "implementation_proposal", proposalID, 1, payload)
	if err != nil {
		return ImplementationProposal{}, err
	}
	abortPending := true
	defer func() {
		if abortPending {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	accepted, rejected := implementationDecisionCounts(proposal.Operations)
	unimplementedCount, blockingDiagnosticCount := implementationIncompleteCounts(proposal)
	actorUUID := uuid.MustParse(actorID)
	now := s.now().UTC()
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := ensureConversationProposalCommandExecuted(transaction, model, true); err != nil {
			return err
		}
		if err := ensureManifestRootHasNoAppliedProposal(transaction, model); err != nil {
			if errors.Is(err, ErrBlockingGate) || errors.Is(err, ErrConflict) {
				return ErrProposalStale
			}
			return err
		}
		manifest, err := lockFrozenManifestLeaf(transaction, model.BuildManifestID, model.ProjectID)
		if err != nil {
			if errors.Is(err, ErrBlockingGate) || errors.Is(err, ErrConflict) {
				return ErrProposalStale
			}
			return err
		}
		if err := ensureWorkflowManifestOrdinalReady(ctx, transaction, manifest); err != nil {
			if errors.Is(err, ErrBlockingGate) || errors.Is(err, ErrConflict) {
				return ErrProposalStale
			}
			return err
		}
		result := transaction.Model(&storage.ImplementationProposalModel{}).
			Where("id = ? AND version = ?", model.ID, input.Version).
			Updates(map[string]any{
				"status": proposal.Status, "version": proposal.Version,
				"content_ref": contentRef.ID, "content_hash": contentRef.ContentHash,
				"accepted_count": accepted, "rejected_count": rejected,
				"unimplemented_count": unimplementedCount, "blocking_diagnostic_count": blockingDiagnosticCount,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		decision := storage.ImplementationOperationDecisionModel{
			ProposalID: model.ID, OperationID: input.OperationID, Decision: string(input.Decision),
			Reason: strings.TrimSpace(input.Reason), DecidedBy: actorUUID, DecidedAt: now,
		}
		if err := transaction.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "proposal_id"}, {Name: "operation_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"decision", "reason", "decided_by", "decided_at"}),
		}).Create(&decision).Error; err != nil {
			return err
		}
		return enqueue(transaction, "implementation_proposal", proposalID, "implementation.operation_decided", "worksflow.implementation.operation.decided", map[string]any{
			"projectId": model.ProjectID.String(), "proposalId": proposalID, "operationId": input.OperationID,
		})
	})
	if err != nil {
		if errors.Is(err, ErrProposalStale) {
			return ImplementationProposal{}, s.persistProposalStale(ctx, staleProposal, model, actorID)
		}
		return ImplementationProposal{}, err
	}
	abortPending = false
	if err := s.contents.Finalize(ctx, contentRef.ID); err != nil {
		return ImplementationProposal{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return proposal, nil
}

func (s *ImplementationService) Apply(ctx context.Context, proposalID, actorID string, input ApplyImplementationInput) (ArtifactRevision, error) {
	proposal, proposalModel, err := s.load(ctx, proposalID)
	if err != nil {
		return ArtifactRevision{}, err
	}
	if _, err := s.access.Authorize(ctx, proposalModel.ProjectID.String(), actorID, ActionEdit); err != nil {
		return ArtifactRevision{}, err
	}
	if input.Version == 0 {
		input.Version = proposal.Version
	}
	if proposal.Version != input.Version || proposal.Status != "ready" {
		return ArtifactRevision{}, ErrConflict
	}
	if err := requireGovernedImplementationReview(proposal); err != nil {
		return ArtifactRevision{}, err
	}
	unimplementedCount, blockingDiagnosticCount := implementationIncompleteCounts(proposal)
	if err := ensureConversationProposalCommandExecuted(s.database.WithContext(ctx), proposalModel, false); err != nil {
		return ArtifactRevision{}, err
	}
	staleProposal := proposal
	staleProposal.Operations = append([]FileOperation(nil), proposal.Operations...)
	binding, err := exactApplicationBuildContractFromProposal(proposal, proposalModel)
	if err != nil {
		return ArtifactRevision{}, err
	}
	if _, err = s.requireApplicationBuildContract(
		ctx, proposalModel.ProjectID.String(), proposal.BuildManifestID, actorID, binding,
	); err != nil {
		if errors.Is(err, ErrBlockingGate) || errors.Is(err, ErrConflict) || errors.Is(err, ErrProposalStale) {
			return ArtifactRevision{}, s.persistProposalStale(ctx, staleProposal, proposalModel, actorID)
		}
		return ArtifactRevision{}, err
	}
	bundle, err := s.workbench.GetBundleForGeneration(ctx, proposal.BuildManifestID, actorID)
	if err != nil {
		if errors.Is(err, ErrBlockingGate) || errors.Is(err, ErrConflict) || errors.Is(err, ErrProposalStale) {
			return ArtifactRevision{}, s.persistProposalStale(ctx, staleProposal, proposalModel, actorID)
		}
		return ArtifactRevision{}, err
	}
	if bundle.ProjectID != proposalModel.ProjectID.String() {
		return ArtifactRevision{}, ErrConflict
	}
	if !optionalVersionRefsEqual(bundle.CurrentWorkspaceRevision, proposal.BaseWorkspaceRevision) {
		return ArtifactRevision{}, ErrConflict
	}
	accepted, err := acceptedImplementationOperations(proposal.Operations)
	if err != nil {
		return ArtifactRevision{}, err
	}
	if proposal.CandidateSource != nil && len(accepted) != len(proposal.Operations) {
		return ArtifactRevision{}, fmt.Errorf(
			"%w: an exact Candidate implementation must be accepted in full",
			ErrBlockingGate,
		)
	}
	workspace, workspaceArtifact, baseRevision, err := s.loadWorkspace(ctx, proposalModel.ProjectID, proposal.BaseWorkspaceRevision)
	if err != nil {
		if errors.Is(err, ErrProposalStale) {
			return ArtifactRevision{}, s.persistProposalStale(ctx, staleProposal, proposalModel, actorID)
		}
		return ArtifactRevision{}, err
	}
	if proposal.CandidateSource != nil && proposal.BaseWorkspaceRevision != nil {
		baseTree, treeErr := candidateWorkspaceTree(workspace)
		if treeErr != nil || baseTree.TreeHash != proposal.CandidateSource.BaseTreeHash {
			return ArtifactRevision{}, fmt.Errorf("%w: Candidate implementation base tree", ErrConflict)
		}
	}
	workspace, err = applyFileOperations(workspace, accepted)
	if err != nil {
		if errors.Is(err, ErrProposalStale) {
			return ArtifactRevision{}, s.persistProposalStale(ctx, staleProposal, proposalModel, actorID)
		}
		return ArtifactRevision{}, err
	}
	if proposal.CandidateSource != nil {
		resultTree, treeErr := candidateWorkspaceTree(workspace)
		if treeErr != nil || resultTree.TreeHash != proposal.CandidateSource.TreeHash {
			return ArtifactRevision{}, fmt.Errorf("%w: Candidate implementation result tree", ErrConflict)
		}
	}
	now := s.now().UTC()
	workspace["updatedAt"] = now.Format(time.RFC3339Nano)
	workspacePayload, err := json.Marshal(workspace)
	if err != nil {
		return ArtifactRevision{}, err
	}
	workspaceRevisionID := uuid.New()
	workspaceContentRef, err := s.contents.PutPending(ctx, proposalModel.ProjectID.String(), "workspace_revision", workspaceRevisionID.String(), 1, workspacePayload)
	if err != nil {
		return ArtifactRevision{}, err
	}
	for index := range proposal.Operations {
		if proposal.Operations[index].Decision == ImplementationAccepted {
			proposal.Operations[index].Decision = ImplementationApplied
		}
	}
	_, rejected := implementationDecisionCounts(proposal.Operations)
	if rejected > 0 {
		proposal.Status = "partially_applied"
	} else {
		proposal.Status = "applied"
	}
	proposal.Version++
	proposal.AppliedAt = &now
	proposalPayload, err := json.Marshal(proposal)
	if err != nil {
		_ = s.contents.Abort(context.Background(), workspaceContentRef.ID)
		return ArtifactRevision{}, err
	}
	proposalContentRef, err := s.contents.PutPending(ctx, proposalModel.ProjectID.String(), "implementation_proposal", proposalID, 1, proposalPayload)
	if err != nil {
		_ = s.contents.Abort(context.Background(), workspaceContentRef.ID)
		return ArtifactRevision{}, err
	}
	pending := []string{workspaceContentRef.ID, proposalContentRef.ID}
	defer func() {
		for _, contentID := range pending {
			_ = s.contents.Abort(context.Background(), contentID)
		}
	}()
	actorUUID := uuid.MustParse(actorID)
	var revision storage.ArtifactRevisionModel
	var frozenSources []ArtifactSource
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := ensureConversationProposalCommandExecuted(transaction, proposalModel, true); err != nil {
			return err
		}
		if workspaceArtifact.ID == uuid.Nil {
			var project storage.ProjectModel
			if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
				Select("id").Where("id = ?", proposalModel.ProjectID).Take(&project).Error; err != nil {
				return err
			}
			var existing storage.ArtifactModel
			err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("project_id = ? AND kind = 'workspace' AND lifecycle = 'active'", proposalModel.ProjectID).
				Take(&existing).Error
			if err == nil {
				return ErrProposalStale
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			workspaceArtifact = storage.ArtifactModel{
				ID: uuid.New(), ProjectID: proposalModel.ProjectID, Kind: "workspace", ArtifactKey: "WORKSPACE-MAIN",
				Title: "Application Workspace", Lifecycle: "active", Version: 1,
				CreatedBy: actorUUID, CreatedAt: now, UpdatedAt: now,
			}
			if err := transaction.Create(&workspaceArtifact).Error; err != nil {
				return err
			}
		} else {
			var locked storage.ArtifactModel
			if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", workspaceArtifact.ID).Take(&locked).Error; err != nil {
				return err
			}
			if baseRevision.ID == uuid.Nil || locked.LatestApprovedRevisionID == nil || *locked.LatestApprovedRevisionID != baseRevision.ID {
				return ErrProposalStale
			}
			workspaceArtifact = locked
		}
		if err := ensureArtifactHealthRow(transaction, workspaceArtifact.ID, now); err != nil {
			return err
		}
		if err := ensureManifestRootHasNoAppliedProposal(transaction, proposalModel); err != nil {
			if errors.Is(err, ErrBlockingGate) || errors.Is(err, ErrConflict) {
				return ErrProposalStale
			}
			return err
		}
		manifest, err := lockFrozenManifestLeaf(
			transaction, proposalModel.BuildManifestID, proposalModel.ProjectID,
		)
		if err != nil {
			if errors.Is(err, ErrBlockingGate) || errors.Is(err, ErrConflict) {
				return ErrProposalStale
			}
			return err
		}
		if err := ensureWorkflowManifestOrdinalReady(ctx, transaction, manifest); err != nil {
			if errors.Is(err, ErrBlockingGate) || errors.Is(err, ErrConflict) {
				return ErrProposalStale
			}
			return err
		}
		var latest uint64
		if err := transaction.Model(&storage.ArtifactRevisionModel{}).Where("artifact_id = ?", workspaceArtifact.ID).
			Select("COALESCE(MAX(revision_number), 0)").Scan(&latest).Error; err != nil {
			return err
		}
		revisionID := workspaceRevisionID
		revision = storage.ArtifactRevisionModel{
			ID: revisionID, ArtifactID: workspaceArtifact.ID, RevisionNumber: latest + 1,
			SchemaVersion: 1, ContentStore: "mongo", ContentRef: workspaceContentRef.ID,
			ContentHash: workspaceContentRef.ContentHash, ByteSize: workspaceContentRef.ByteSize,
			WorkflowStatus: "approved", ChangeSource: "ai_proposal",
			ChangeSummary:            "Apply implementation proposal " + proposalID,
			ImplementationProposalID: &proposalModel.ID, CreatedBy: actorUUID, CreatedAt: now, ApprovedAt: &now,
		}
		if baseRevision.ID != uuid.Nil {
			revision.ParentRevisionID = &baseRevision.ID
			if err := transaction.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", baseRevision.ID).
				Updates(map[string]any{"workflow_status": "superseded", "superseded_at": now}).Error; err != nil {
				return err
			}
		}
		if err := transaction.Create(&revision).Error; err != nil {
			return err
		}
		draftID := uuid.New()
		draft := storage.ArtifactDraftModel{
			ID: draftID, ArtifactID: workspaceArtifact.ID, BaseRevisionID: &revision.ID,
			Sequence: 1, ETag: draftETag(draftID, 1, revision.ContentHash), SchemaVersion: 1,
			ContentStore: "mongo", ContentRef: revision.ContentRef, ContentHash: revision.ContentHash,
			ByteSize: revision.ByteSize, Status: "draft", CreatedBy: actorUUID, UpdatedBy: actorUUID,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := transaction.Create(&draft).Error; err != nil {
			return err
		}
		frozenSources, err = PersistSystemRevisionLineage(
			transaction, proposalModel.ProjectID, workspaceArtifact.ID, revision.ID, draft.ID,
			actorUUID, now, implementationRevisionLineageSources(bundle),
		)
		if err != nil {
			return err
		}
		if err := transaction.Model(&storage.ArtifactModel{}).Where("id = ?", workspaceArtifact.ID).
			Updates(map[string]any{
				"latest_revision_id": revision.ID, "latest_approved_revision_id": revision.ID,
				"latest_draft_id": draft.ID, "version": gorm.Expr("version + 1"), "updated_at": now,
			}).Error; err != nil {
			return err
		}
		result := transaction.Model(&storage.ImplementationProposalModel{}).
			Where("id = ? AND version = ?", proposalModel.ID, input.Version).
			Updates(map[string]any{
				"status": proposal.Status, "version": proposal.Version,
				"content_ref": proposalContentRef.ID, "content_hash": proposalContentRef.ContentHash,
				"applied_by": actorUUID, "applied_at": now,
				"unimplemented_count": unimplementedCount, "blocking_diagnostic_count": blockingDiagnosticCount,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		consumed := transaction.Model(&storage.ApplicationBuildManifestModel{}).
			Where("id = ? AND status = 'frozen'", proposalModel.BuildManifestID).
			Update("status", "consumed")
		if consumed.Error != nil {
			return consumed.Error
		}
		if consumed.RowsAffected != 1 {
			return ErrConflict
		}
		applyMetadata := map[string]any{"proposalId": proposalID, "appliedBaseRevisionId": nullableUUIDString(baseRevision.ID)}
		if proposal.BaseWorkspaceRevision != nil {
			applyMetadata["proposalBaseRevisionId"] = proposal.BaseWorkspaceRevision.RevisionID
		}
		if err := insertAudit(transaction, proposalModel.ProjectID, actorUUID, "implementation.applied", "artifact_revision", revision.ID.String(), applyMetadata); err != nil {
			return err
		}
		return enqueue(transaction, "workspace", workspaceArtifact.ID.String(), "implementation.applied", "worksflow.implementation.applied", map[string]any{
			"projectId": proposalModel.ProjectID.String(), "proposalId": proposalID,
			"workspaceArtifactId": workspaceArtifact.ID.String(), "workspaceRevisionId": revision.ID.String(),
			"appliedBaseRevisionId": nullableUUIDString(baseRevision.ID),
		})
	})
	if err != nil {
		if errors.Is(err, ErrProposalStale) {
			return ArtifactRevision{}, s.persistProposalStale(ctx, staleProposal, proposalModel, actorID)
		}
		return ArtifactRevision{}, err
	}
	pending = nil
	var finalizeErrors []error
	for _, contentID := range []string{workspaceContentRef.ID, proposalContentRef.ID} {
		if err := s.contents.Finalize(ctx, contentID); err != nil {
			finalizeErrors = append(finalizeErrors, err)
		}
	}
	if err := errors.Join(finalizeErrors...); err != nil {
		return ArtifactRevision{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return revisionFromModel(revision, workspacePayload, frozenSources), nil
}

// A conversation-owned proposal becomes reviewable only after the command
// receipt has been committed. This closes the crash window where generation
// succeeded but the accepted command was still pending and could otherwise be
// reviewed/applied (or even rejected) as if it had never produced a result.
func ensureConversationProposalCommandExecuted(
	database *gorm.DB,
	proposal storage.ImplementationProposalModel,
	lock bool,
) error {
	if proposal.ExecutionSource != string(ImplementationSourceConversationCommand) {
		return nil
	}
	if proposal.ConversationCommandID == nil || proposal.ID != *proposal.ConversationCommandID {
		return ErrConflict
	}
	query := database
	if lock {
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	var command storage.ConversationCommandModel
	if err := query.Select("id", "project_id", "kind", "status").Where(
		"id = ? AND project_id = ? AND kind = ? AND status = ?",
		*proposal.ConversationCommandID,
		proposal.ProjectID,
		"workbench_instruction",
		"executed",
	).Take(&command).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func ensureManifestRootHasNoAppliedProposal(
	transaction *gorm.DB,
	proposal storage.ImplementationProposalModel,
) error {
	var manifest storage.ApplicationBuildManifestModel
	if err := transaction.Where("id = ? AND project_id = ?", proposal.BuildManifestID, proposal.ProjectID).
		Take(&manifest).Error; err != nil {
		return err
	}
	rootID := manifest.RootManifestID
	if rootID == uuid.Nil {
		rootID = manifest.ID
	}
	var root storage.ApplicationBuildManifestModel
	if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND project_id = ?", rootID, proposal.ProjectID).Take(&root).Error; err != nil {
		return err
	}
	if root.RootManifestID != uuid.Nil && root.RootManifestID != root.ID {
		return ErrConflict
	}
	var applied int64
	if err := transaction.Table("implementation_proposals AS proposals").
		Joins("JOIN application_build_manifests AS manifests ON manifests.id = proposals.build_manifest_id").
		Where(
			"proposals.project_id = ? AND manifests.root_manifest_id = ? AND proposals.id <> ? AND proposals.status IN ?",
			proposal.ProjectID, rootID, proposal.ID, []string{"applied", "partially_applied"},
		).Count(&applied).Error; err != nil {
		return err
	}
	if applied != 0 {
		return fmt.Errorf("%w: build manifest root already has an applied proposal", ErrBlockingGate)
	}
	return nil
}

func validateGeneratedImplementationIdentity(
	identity GeneratedImplementationIdentity,
) (uuid.UUID, uuid.UUID, uuid.UUID, *uuid.UUID, *uuid.UUID, error) {
	proposalID, err := uuid.Parse(strings.TrimSpace(identity.ProposalID))
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, nil, nil, fmt.Errorf("%w: reserved implementation proposal id", ErrInvalidInput)
	}
	requestKey, err := uuid.Parse(strings.TrimSpace(identity.RequestKey))
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, nil, nil, fmt.Errorf("%w: implementation generation request key", ErrInvalidInput)
	}
	claimToken, err := uuid.Parse(strings.TrimSpace(identity.ClaimToken))
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, nil, nil, fmt.Errorf("%w: implementation generation claim token", ErrInvalidInput)
	}
	if !validSHA256(identity.InstructionHash) || strings.TrimSpace(identity.AIProvider) == "" || strings.TrimSpace(identity.AIModel) == "" {
		return uuid.Nil, uuid.Nil, uuid.Nil, nil, nil, fmt.Errorf("%w: implementation generation provenance", ErrInvalidInput)
	}
	var conversationCommandID *uuid.UUID
	if identity.ConversationCommandID != nil {
		parsed, parseErr := uuid.Parse(strings.TrimSpace(*identity.ConversationCommandID))
		if parseErr != nil {
			return uuid.Nil, uuid.Nil, uuid.Nil, nil, nil, fmt.Errorf("%w: conversation command id", ErrInvalidInput)
		}
		conversationCommandID = &parsed
	}
	var supersedesProposalID *uuid.UUID
	if identity.ExpectedActiveProposalID != nil {
		parsed, parseErr := uuid.Parse(strings.TrimSpace(*identity.ExpectedActiveProposalID))
		if parseErr != nil || identity.ExpectedActiveProposalVersion == 0 {
			return uuid.Nil, uuid.Nil, uuid.Nil, nil, nil, fmt.Errorf("%w: expected active implementation proposal", ErrInvalidInput)
		}
		supersedesProposalID = &parsed
	} else if identity.ExpectedActiveProposalVersion != 0 {
		return uuid.Nil, uuid.Nil, uuid.Nil, nil, nil, fmt.Errorf("%w: expected active implementation proposal version", ErrInvalidInput)
	}
	switch identity.ExecutionSource {
	case ImplementationSourceManualGeneration, ImplementationSourceWorkflowRunner:
		if conversationCommandID != nil {
			return uuid.Nil, uuid.Nil, uuid.Nil, nil, nil, fmt.Errorf("%w: non-conversation generation command", ErrInvalidInput)
		}
	case ImplementationSourceConversationCommand:
		if conversationCommandID == nil || requestKey != *conversationCommandID || proposalID != *conversationCommandID {
			return uuid.Nil, uuid.Nil, uuid.Nil, nil, nil, fmt.Errorf("%w: conversation generation identity", ErrInvalidInput)
		}
	default:
		return uuid.Nil, uuid.Nil, uuid.Nil, nil, nil, fmt.Errorf("%w: implementation execution source", ErrInvalidInput)
	}
	return proposalID, requestKey, claimToken, conversationCommandID, supersedesProposalID, nil
}

func validateImplementationGenerationClaim(
	transaction *gorm.DB,
	proposal storage.ImplementationProposalModel,
	requestKey, claimToken uuid.UUID,
	executionSource ImplementationExecutionSource,
	conversationCommandID *uuid.UUID,
	expectedActiveProposalID *uuid.UUID,
	expectedActiveProposalVersion uint64,
	instructionHash string,
	applicationBuildContract ApplicationBuildContractRef,
	actorID uuid.UUID,
	now time.Time,
) error {
	var claim storage.ImplementationGenerationClaimModel
	if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"request_key = ? AND build_manifest_id = ? AND project_id = ?", requestKey, proposal.BuildManifestID, proposal.ProjectID,
	).Take(&claim).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrConflict
		}
		return err
	}
	if claim.Status != "processing" || claim.ClaimToken == nil || *claim.ClaimToken != claimToken ||
		claim.ClaimExpiresAt == nil || claim.ClaimExpiresAt.Before(now) || claim.RequestKey != requestKey ||
		claim.ReservedProposalID != proposal.ID || claim.ExecutionSource != string(executionSource) ||
		claim.InstructionHash != instructionHash || claim.ActorID != actorID ||
		claim.ApplicationBuildContractID == nil || *claim.ApplicationBuildContractID != uuid.MustParse(applicationBuildContract.ID) ||
		stringValue(claim.ApplicationBuildContractHash) != applicationBuildContract.ContractHash ||
		!optionalUUIDsEqual(claim.ConversationCommandID, conversationCommandID) ||
		!optionalUUIDsEqual(claim.ExpectedActiveProposalID, expectedActiveProposalID) ||
		optionalUint64Value(claim.ExpectedActiveProposalVersion) != expectedActiveProposalVersion {
		return ErrConflict
	}
	return nil
}

func optionalUint64Value(value *uint64) uint64 {
	if value == nil {
		return 0
	}
	return *value
}

func validSHA256(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil
}

func nonEmptyStringPointer(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	copy := value
	return &copy
}

func lockFrozenManifestLeaf(
	transaction *gorm.DB,
	manifestID uuid.UUID,
	projectID uuid.UUID,
) (storage.ApplicationBuildManifestModel, error) {
	var manifest storage.ApplicationBuildManifestModel
	if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND project_id = ?", manifestID, projectID).Take(&manifest).Error; err != nil {
		return manifest, err
	}
	if manifest.Status != "frozen" {
		return manifest, fmt.Errorf("%w: build manifest is not frozen", ErrBlockingGate)
	}
	var childCount int64
	if err := transaction.Model(&storage.ApplicationBuildManifestModel{}).
		Where("derived_from_id = ?", manifest.ID).Count(&childCount).Error; err != nil {
		return manifest, err
	}
	if childCount != 0 {
		return manifest, fmt.Errorf("%w: build manifest is not the active lineage leaf", ErrBlockingGate)
	}
	return manifest, nil
}

func (s *ImplementationService) load(ctx context.Context, proposalID string) (ImplementationProposal, storage.ImplementationProposalModel, error) {
	proposal, model, err := s.loadEnvelope(ctx, proposalID)
	if err != nil {
		return ImplementationProposal{}, model, err
	}
	if err := s.validateCandidateImplementationSource(ctx, proposal, model); err != nil {
		return ImplementationProposal{}, model, err
	}
	return proposal, model, nil
}

// loadEnvelope validates the immutable Proposal payload and all SQL
// projections that existed when it was written. Candidate VerificationReceipt
// authority is deliberately checked by load, except for the read/quarantine
// compatibility path for pre-verification Candidate history.
func (s *ImplementationService) loadEnvelope(ctx context.Context, proposalID string) (ImplementationProposal, storage.ImplementationProposalModel, error) {
	id, err := uuid.Parse(proposalID)
	if err != nil {
		return ImplementationProposal{}, storage.ImplementationProposalModel{}, fmt.Errorf("%w: implementation proposal id", ErrInvalidInput)
	}
	var model storage.ImplementationProposalModel
	if err := s.database.WithContext(ctx).Where("id = ?", id).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ImplementationProposal{}, model, ErrNotFound
		}
		return ImplementationProposal{}, model, err
	}
	stored, err := s.contents.Get(ctx, model.ContentRef, model.ContentHash)
	if err != nil {
		return ImplementationProposal{}, model, err
	}
	var proposal ImplementationProposal
	if err := json.Unmarshal(stored.Payload, &proposal); err != nil {
		return ImplementationProposal{}, model, err
	}
	normalizeLegacyImplementationExecution(&proposal, model)
	hash, err := storedImplementationPayloadHash(proposal, model)
	if err != nil || hash != proposal.PayloadHash || hash != model.PayloadHash || proposal.Version != model.Version || proposal.Status != model.Status ||
		proposal.ID != model.ID.String() || proposal.ProjectID != model.ProjectID.String() || proposal.BuildManifestID != model.BuildManifestID.String() ||
		!optionalApplicationBuildContractMatchesModel(proposal.ApplicationBuildContract, model) ||
		string(proposal.ExecutionSource) != model.ExecutionSource || !optionalStringMatchesUUID(proposal.ConversationCommandID, model.ConversationCommandID) ||
		!optionalStringMatchesUUID(proposal.SupersedesProposalID, model.SupersedesProposalID) ||
		proposal.InstructionHash != stringValue(model.InstructionHash) || proposal.AIProvider != stringValue(model.AIProvider) || proposal.AIModel != stringValue(model.AIModel) {
		return ImplementationProposal{}, model, ErrConflict
	}
	unimplementedCount, blockingDiagnosticCount := implementationIncompleteCounts(proposal)
	if (model.UnimplementedCount == nil) != (model.BlockingDiagnosticCount == nil) ||
		(model.UnimplementedCount != nil &&
			(*model.UnimplementedCount != unimplementedCount || *model.BlockingDiagnosticCount != blockingDiagnosticCount)) {
		return ImplementationProposal{}, model, ErrConflict
	}
	return proposal, model, nil
}

func normalizeLegacyImplementationExecution(proposal *ImplementationProposal, model storage.ImplementationProposalModel) {
	// Rows created before migration 015 have immutable content without the new
	// presentation fields. The migration classifies only those rows as manual
	// submissions, so no generated provenance can be hidden by this bridge.
	if proposal.ExecutionSource == "" && model.ExecutionSource == string(ImplementationSourceManualSubmission) &&
		model.ConversationCommandID == nil && model.SupersedesProposalID == nil && model.InstructionHash == nil && model.AIProvider == nil && model.AIModel == nil {
		proposal.ExecutionSource = ImplementationSourceManualSubmission
	}
}

func optionalStringMatchesUUID(value *string, expected *uuid.UUID) bool {
	if value == nil || expected == nil {
		return value == nil && expected == nil
	}
	parsed, err := uuid.Parse(*value)
	return err == nil && parsed == *expected
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *ImplementationService) loadWorkspace(ctx context.Context, projectID uuid.UUID, expected *VersionRef) (map[string]any, storage.ArtifactModel, storage.ArtifactRevisionModel, error) {
	var artifact storage.ArtifactModel
	err := s.database.WithContext(ctx).
		Where("project_id = ? AND kind = 'workspace' AND lifecycle = 'active'", projectID).
		Take(&artifact).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if expected != nil {
			return nil, storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, ErrProposalStale
		}
		return emptyWorkspace(projectID), storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, nil
	}
	if err != nil {
		return nil, storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, err
	}
	if artifact.LatestApprovedRevisionID == nil {
		return nil, artifact, storage.ArtifactRevisionModel{}, ErrProposalStale
	}
	if expected == nil {
		return nil, artifact, storage.ArtifactRevisionModel{}, ErrProposalStale
	}
	expectedArtifactID, expectedRevisionID, err := (&TraceService{database: s.database}).validateRef(ctx, projectID, *expected)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidInput) {
			return nil, artifact, storage.ArtifactRevisionModel{}, ErrProposalStale
		}
		return nil, artifact, storage.ArtifactRevisionModel{}, err
	}
	if expectedArtifactID != artifact.ID || *artifact.LatestApprovedRevisionID != expectedRevisionID {
		return nil, artifact, storage.ArtifactRevisionModel{}, ErrProposalStale
	}
	var revision storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Where("id = ? AND artifact_id = ?", *artifact.LatestApprovedRevisionID, artifact.ID).Take(&revision).Error; err != nil {
		return nil, artifact, revision, err
	}
	stored, err := s.contents.Get(ctx, revision.ContentRef, revision.ContentHash)
	if err != nil {
		return nil, artifact, revision, err
	}
	var workspace map[string]any
	if err := json.Unmarshal(stored.Payload, &workspace); err != nil {
		return nil, artifact, revision, err
	}
	return workspace, artifact, revision, nil
}

func (s *ImplementationService) persistProposalStale(
	ctx context.Context,
	proposal ImplementationProposal,
	model storage.ImplementationProposalModel,
	actorID string,
) error {
	if proposal.Status == "stale" || model.Status == "stale" {
		return ErrProposalStale
	}
	if model.Status == "applied" || model.Status == "partially_applied" {
		return ErrConflict
	}
	proposal.Status = "stale"
	proposal.Version = model.Version + 1
	proposal.AppliedAt = nil
	payload, err := json.Marshal(proposal)
	if err != nil {
		return fmt.Errorf("%w: encode stale proposal: %v", ErrProposalStale, err)
	}
	contentRef, err := s.contents.PutPending(
		ctx, model.ProjectID.String(), "implementation_proposal", model.ID.String(), 1, payload,
	)
	if err != nil {
		return fmt.Errorf("%w: store stale proposal: %v", ErrProposalStale, err)
	}
	abortPending := true
	defer func() {
		if abortPending {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	alreadyStale := false
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		updated := transaction.Model(&storage.ImplementationProposalModel{}).
			Where("id = ? AND version = ? AND status = ?", model.ID, model.Version, model.Status).
			Updates(map[string]any{
				"status": proposal.Status, "version": proposal.Version,
				"content_ref": contentRef.ID, "content_hash": contentRef.ContentHash,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			var current storage.ImplementationProposalModel
			if err := transaction.Select("status").Where("id = ?", model.ID).Take(&current).Error; err != nil {
				return err
			}
			if current.Status == "stale" {
				alreadyStale = true
				return nil
			}
			return ErrConflict
		}
		metadata := map[string]any{
			"buildManifestId":         model.BuildManifestID.String(),
			"baseWorkspaceRevisionId": nullableUUIDPointerString(model.BaseWorkspaceRevisionID),
		}
		if err := insertAudit(
			transaction, model.ProjectID, actorUUID, "implementation.proposal_stale",
			"implementation_proposal", model.ID.String(), metadata,
		); err != nil {
			return err
		}
		return enqueue(
			transaction, "implementation_proposal", model.ID.String(), "implementation.proposal_stale",
			"worksflow.implementation.proposal.stale", map[string]any{
				"projectId": model.ProjectID.String(), "proposalId": model.ID.String(),
				"buildManifestId": model.BuildManifestID.String(),
			},
		)
	})
	if err != nil {
		return fmt.Errorf("%w: persist stale proposal: %v", ErrProposalStale, err)
	}
	if alreadyStale {
		return ErrProposalStale
	}
	abortPending = false
	if err := s.contents.Finalize(ctx, contentRef.ID); err != nil {
		return fmt.Errorf("%w: finalize stale proposal: %v", ErrProposalStale, err)
	}
	return ErrProposalStale
}

func optionalVersionRefsEqual(left, right *VersionRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ArtifactID == right.ArtifactID &&
		left.RevisionID == right.RevisionID &&
		left.ContentHash == right.ContentHash &&
		stringPointerEqual(left.AnchorID, right.AnchorID)
}

func nullableUUIDPointerString(value *uuid.UUID) any {
	if value == nil || *value == uuid.Nil {
		return nil
	}
	return value.String()
}

func nullableUUIDString(value uuid.UUID) any {
	if value == uuid.Nil {
		return nil
	}
	return value.String()
}

func validateFileOperations(operations []FileOperation) error {
	if len(operations) == 0 || len(operations) > 40_000 {
		return fmt.Errorf("%w: implementation operations", ErrInvalidInput)
	}
	byID := map[string]FileOperation{}
	for index := range operations {
		operation := &operations[index]
		operation.ID = strings.TrimSpace(operation.ID)
		if operation.ID == "" || byID[operation.ID].ID != "" {
			return fmt.Errorf("%w: operation id", ErrInvalidInput)
		}
		if err := validateWorkspacePath(operation.Path); err != nil {
			return err
		}
		switch operation.Kind {
		case "file.upsert":
			if operation.Content == nil || len(*operation.Content) > 4<<20 {
				return fmt.Errorf("%w: file content at operation %d", ErrInvalidInput, index)
			}
			if operation.Mode != "" && operation.Mode != "100644" && operation.Mode != "100755" {
				return fmt.Errorf("%w: file mode at operation %d", ErrInvalidInput, index)
			}
		case "file.delete":
			if operation.Content != nil || operation.Mode != "" {
				return fmt.Errorf("%w: delete operation content", ErrInvalidInput)
			}
		case "file.rename":
			if err := validateWorkspacePath(operation.FromPath); err != nil ||
				operation.FromPath == operation.Path || operation.Mode != "" {
				return fmt.Errorf("%w: rename paths", ErrInvalidInput)
			}
		default:
			return fmt.Errorf("%w: file operation kind", ErrInvalidInput)
		}
		operation.Decision = ImplementationPending
		operation.DecidedBy = ""
		operation.Reason = ""
		byID[operation.ID] = *operation
	}
	for _, operation := range operations {
		for _, dependency := range operation.DependsOn {
			if dependency == operation.ID || byID[dependency].ID == "" {
				return fmt.Errorf("%w: operation dependency", ErrInvalidInput)
			}
		}
	}
	if _, err := topologicalFileOperations(operations, false); err != nil {
		return err
	}
	return nil
}

func acceptedImplementationOperations(operations []FileOperation) ([]FileOperation, error) {
	for _, operation := range operations {
		if operation.Decision == ImplementationPending {
			return nil, ErrConflict
		}
		if operation.Decision == ImplementationAccepted {
			for _, dependency := range operation.DependsOn {
				dependencyOperation := findFileOperation(operations, dependency)
				if dependencyOperation == nil || dependencyOperation.Decision != ImplementationAccepted {
					return nil, fmt.Errorf("%w: accepted operation dependency", ErrBlockingGate)
				}
			}
		}
	}
	result, err := topologicalFileOperations(operations, true)
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: no operations were accepted", ErrBlockingGate)
	}
	return result, nil
}

func topologicalFileOperations(operations []FileOperation, acceptedOnly bool) ([]FileOperation, error) {
	selected := map[string]FileOperation{}
	for _, operation := range operations {
		if !acceptedOnly || operation.Decision == ImplementationAccepted {
			selected[operation.ID] = operation
		}
	}
	indegree := map[string]int{}
	dependents := map[string][]string{}
	for id := range selected {
		indegree[id] = 0
	}
	for id, operation := range selected {
		for _, dependency := range operation.DependsOn {
			if _, included := selected[dependency]; !included {
				if acceptedOnly {
					continue
				}
				return nil, fmt.Errorf("%w: operation dependency", ErrInvalidInput)
			}
			indegree[id]++
			dependents[dependency] = append(dependents[dependency], id)
		}
	}
	queue := []string{}
	for id, count := range indegree {
		if count == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)
	result := make([]FileOperation, 0, len(selected))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		result = append(result, selected[id])
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
				sort.Strings(queue)
			}
		}
	}
	if len(result) != len(selected) {
		return nil, fmt.Errorf("%w: operation dependency cycle", ErrInvalidInput)
	}
	return result, nil
}

func applyFileOperations(workspace map[string]any, operations []FileOperation) (map[string]any, error) {
	files := map[string]map[string]any{}
	for _, file := range objectSlice(workspace["files"]) {
		filePath := firstString(file, "path")
		if filePath != "" {
			files[filePath] = file
		}
	}
	for _, operation := range operations {
		switch operation.Kind {
		case "file.upsert":
			existing := files[operation.Path]
			if existing != nil {
				if operation.ExpectedHash == "" || operation.ExpectedHash != hashText(workspaceFileContent(existing)) {
					return nil, fmt.Errorf("%w: file %s changed", ErrProposalStale, operation.Path)
				}
			} else if operation.ExpectedHash != "" {
				return nil, fmt.Errorf("%w: file %s no longer exists", ErrProposalStale, operation.Path)
			}
			revision := 1
			if value, ok := existing["revision"].(float64); ok {
				revision = int(value) + 1
			}
			mode := operation.Mode
			if mode == "" {
				mode = workspaceFileMode(existing)
				if mode == "" {
					mode = "100644"
				}
			}
			files[operation.Path] = map[string]any{
				"path": operation.Path, "content": dereferenceString(operation.Content),
				"language": operation.Language, "mode": mode,
				"revision": revision, "dirty": false,
			}
		case "file.delete":
			existing := files[operation.Path]
			if existing == nil || operation.ExpectedHash == "" || operation.ExpectedHash != hashText(workspaceFileContent(existing)) {
				return nil, fmt.Errorf("%w: file %s changed", ErrProposalStale, operation.Path)
			}
			delete(files, operation.Path)
		case "file.rename":
			existing := files[operation.FromPath]
			if existing == nil || files[operation.Path] != nil || operation.ExpectedHash == "" || operation.ExpectedHash != hashText(workspaceFileContent(existing)) {
				return nil, fmt.Errorf("%w: rename source %s changed", ErrProposalStale, operation.FromPath)
			}
			delete(files, operation.FromPath)
			existing["path"] = operation.Path
			files[operation.Path] = existing
		}
	}
	paths := make([]string, 0, len(files))
	for filePath := range files {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	fileList := make([]any, 0, len(paths))
	for _, filePath := range paths {
		fileList = append(fileList, files[filePath])
	}
	workspace["files"] = fileList
	if revision, ok := workspace["revision"].(float64); ok {
		workspace["revision"] = int(revision) + 1
	} else {
		workspace["revision"] = 1
	}
	return workspace, nil
}

func workspaceFileContent(file map[string]any) string {
	content, _ := file["content"].(string)
	return content
}

func validateWorkspacePath(value string) error {
	if value == "" || len(value) > 512 || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return fmt.Errorf("%w: workspace path", ErrInvalidInput)
	}
	cleaned := path.Clean(value)
	if cleaned != value || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("%w: workspace path", ErrInvalidInput)
	}
	first := strings.Split(cleaned, "/")[0]
	if first == ".git" || first == ".ssh" || cleaned == ".env" || strings.HasPrefix(cleaned, ".env.") {
		return fmt.Errorf("%w: protected workspace path", ErrForbidden)
	}
	return nil
}

func implementationPayloadHash(proposal ImplementationProposal) (string, error) {
	var candidateSource any
	if proposal.CandidateSource != nil {
		candidateSource = proposal.CandidateSource
	}
	return implementationPayloadHashWithCandidateSource(proposal, candidateSource)
}

func storedImplementationPayloadHash(
	proposal ImplementationProposal,
	model storage.ImplementationProposalModel,
) (string, error) {
	hash, err := implementationPayloadHash(proposal)
	if err != nil || (hash == proposal.PayloadHash && hash == model.PayloadHash) {
		return hash, err
	}
	if !historicalUnverifiedCandidateImplementation(model) || proposal.CandidateSource == nil {
		return hash, nil
	}
	legacy := struct {
		FreezeReceiptID      string                `json:"freezeReceiptId"`
		RepositorySnapshotID string                `json:"repositorySnapshotId"`
		SessionID            string                `json:"sessionId"`
		CandidateID          string                `json:"candidateId"`
		CandidateSnapshotID  string                `json:"candidateSnapshotId"`
		CandidateVersion     uint64                `json:"candidateVersion"`
		JournalSequence      uint64                `json:"journalSequence"`
		SessionEpoch         uint64                `json:"sessionEpoch"`
		WriterLeaseEpoch     uint64                `json:"writerLeaseEpoch"`
		BaseTreeHash         string                `json:"baseTreeHash"`
		TreeHash             string                `json:"treeHash"`
		FullStackTemplate    ExactContentReference `json:"fullStackTemplate"`
	}{
		FreezeReceiptID:      proposal.CandidateSource.FreezeReceiptID,
		RepositorySnapshotID: proposal.CandidateSource.RepositorySnapshotID,
		SessionID:            proposal.CandidateSource.SessionID,
		CandidateID:          proposal.CandidateSource.CandidateID,
		CandidateSnapshotID:  proposal.CandidateSource.CandidateSnapshotID,
		CandidateVersion:     proposal.CandidateSource.CandidateVersion,
		JournalSequence:      proposal.CandidateSource.JournalSequence,
		SessionEpoch:         proposal.CandidateSource.SessionEpoch,
		WriterLeaseEpoch:     proposal.CandidateSource.WriterLeaseEpoch,
		BaseTreeHash:         proposal.CandidateSource.BaseTreeHash,
		TreeHash:             proposal.CandidateSource.TreeHash,
		FullStackTemplate:    proposal.CandidateSource.FullStackTemplate,
	}
	return implementationPayloadHashWithCandidateSource(proposal, legacy)
}

func implementationPayloadHashWithCandidateSource(
	proposal ImplementationProposal,
	candidateSource any,
) (string, error) {
	type immutableOperation struct {
		ID           string   `json:"id"`
		Kind         string   `json:"kind"`
		Path         string   `json:"path"`
		FromPath     string   `json:"fromPath,omitempty"`
		Content      *string  `json:"content,omitempty"`
		Language     string   `json:"language,omitempty"`
		Mode         string   `json:"mode,omitempty"`
		ExpectedHash string   `json:"expectedHash,omitempty"`
		DependsOn    []string `json:"dependsOn,omitempty"`
		Rationale    string   `json:"rationale,omitempty"`
		TraceSource  []string `json:"traceSource,omitempty"`
	}
	operations := make([]immutableOperation, len(proposal.Operations))
	for index, operation := range proposal.Operations {
		operations[index] = immutableOperation{
			ID: operation.ID, Kind: operation.Kind, Path: operation.Path,
			FromPath: operation.FromPath, Content: operation.Content,
			Language: operation.Language, Mode: operation.Mode,
			ExpectedHash: operation.ExpectedHash, DependsOn: operation.DependsOn,
			Rationale: operation.Rationale, TraceSource: operation.TraceSource,
		}
	}
	payload := struct {
		ID                       string                       `json:"id"`
		ProjectID                string                       `json:"projectId"`
		BuildManifestID          string                       `json:"buildManifestId"`
		ApplicationBuildContract *ApplicationBuildContractRef `json:"applicationBuildContract,omitempty"`
		BaseWorkspaceRevision    *VersionRef                  `json:"baseWorkspaceRevision,omitempty"`
		CandidateSource          any                          `json:"candidateSource,omitempty"`
		Operations               []immutableOperation         `json:"operations"`
		Routes                   []json.RawMessage            `json:"routes"`
		APIs                     []json.RawMessage            `json:"apis"`
		Migrations               []json.RawMessage            `json:"migrations"`
		Tests                    []json.RawMessage            `json:"tests"`
		Previews                 []json.RawMessage            `json:"previews"`
		TraceLinks               []json.RawMessage            `json:"traceLinks"`
		Diagnostics              []ValidationFinding          `json:"diagnostics"`
		Assumptions              []string                     `json:"assumptions"`
		UnimplementedItems       []string                     `json:"unimplementedItems"`
		CreatedBy                string                       `json:"createdBy"`
		CreatedAt                time.Time                    `json:"createdAt"`
	}{
		ID: proposal.ID, ProjectID: proposal.ProjectID,
		BuildManifestID:          proposal.BuildManifestID,
		ApplicationBuildContract: proposal.ApplicationBuildContract,
		BaseWorkspaceRevision:    proposal.BaseWorkspaceRevision,
		CandidateSource:          candidateSource,
		Operations:               operations, Routes: proposal.Routes, APIs: proposal.APIs,
		Migrations: proposal.Migrations, Tests: proposal.Tests, Previews: proposal.Previews,
		TraceLinks: proposal.TraceLinks, Diagnostics: proposal.Diagnostics,
		Assumptions: proposal.Assumptions, UnimplementedItems: proposal.UnimplementedItems,
		CreatedBy: proposal.CreatedBy, CreatedAt: proposal.CreatedAt,
	}
	return domain.CanonicalHash(payload)
}

func implementationStatus(operations []FileOperation) string {
	pending, accepted, rejected := 0, 0, 0
	for _, operation := range operations {
		switch operation.Decision {
		case ImplementationPending:
			pending++
		case ImplementationAccepted:
			accepted++
		case ImplementationRejected:
			rejected++
		}
	}
	switch {
	case pending > 0 && (accepted > 0 || rejected > 0):
		return "reviewing"
	case pending > 0:
		return "open"
	case accepted > 0:
		return "ready"
	default:
		return "rejected"
	}
}

func implementationIncompleteCounts(proposal ImplementationProposal) (int, int) {
	unimplemented := len(proposal.UnimplementedItems)
	blockingDiagnostics := 0
	for _, finding := range proposal.Diagnostics {
		if strings.EqualFold(strings.TrimSpace(finding.Severity), "blocker") {
			blockingDiagnostics++
		}
	}
	return unimplemented, blockingDiagnostics
}

func legacyAIImplementationSource(source ImplementationExecutionSource) bool {
	switch source {
	case ImplementationSourceManualGeneration,
		ImplementationSourceWorkflowRunner,
		ImplementationSourceConversationCommand:
		return true
	default:
		return false
	}
}

func historicalUnverifiedCandidateImplementation(model storage.ImplementationProposalModel) bool {
	return model.ExecutionSource == string(ImplementationSourceCandidateFreeze) &&
		model.CandidateVerificationBindingVersion == nil &&
		model.CandidateVerificationReceiptID == nil &&
		model.CandidateVerificationReceiptHash == nil
}

func quarantinableImplementationProposal(
	proposal ImplementationProposal,
	model storage.ImplementationProposalModel,
) bool {
	if legacyAIImplementationSource(proposal.ExecutionSource) ||
		proposal.ExecutionSource == ImplementationSourceManualSubmission {
		return true
	}
	return proposal.ExecutionSource == ImplementationSourceCandidateFreeze &&
		historicalUnverifiedCandidateImplementation(model)
}

func requireGovernedImplementationReview(proposal ImplementationProposal) error {
	if legacyAIImplementationSource(proposal.ExecutionSource) {
		return fmt.Errorf(
			"%w: AI implementation must be produced by an exact verified Candidate freeze",
			ErrBlockingGate,
		)
	}
	unimplemented, blockers := implementationIncompleteCounts(proposal)
	if unimplemented != 0 || blockers != 0 {
		return fmt.Errorf(
			"%w: implementation has %d unimplemented item(s) and %d blocking diagnostic(s)",
			ErrBlockingGate, unimplemented, blockers,
		)
	}
	return nil
}

func implementationDecisionCounts(operations []FileOperation) (int, int) {
	accepted, rejected := 0, 0
	for _, operation := range operations {
		if operation.Decision == ImplementationAccepted || operation.Decision == ImplementationApplied {
			accepted++
		} else if operation.Decision == ImplementationRejected {
			rejected++
		}
	}
	return accepted, rejected
}

func emptyWorkspace(projectID uuid.UUID) map[string]any {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return map[string]any{
		"schemaVersion": 1, "id": "workspace-" + projectID.String(), "name": "Application Workspace",
		"revision": 0, "createdAt": now, "updatedAt": now, "files": []any{},
		"checkpoints": []any{}, "branches": []any{}, "activeBranchId": "main", "diagnostics": []any{},
	}
}

func buildManifestSources(bundle WorkbenchBundle) []VersionRef {
	values := []VersionRef{bundle.BlueprintRevision, bundle.PageSpecRevision, bundle.PrototypeRevision}
	for _, collection := range [][]VersionRef{bundle.RequirementRevisions, bundle.ContractRevisions, bundle.DesignSystemRevisions} {
		for _, reference := range collection {
			values = appendUniqueRef(values, reference)
		}
	}
	for _, contextRevision := range bundle.ContextRevisions {
		values = appendUniqueRef(values, contextRevision.Revision)
	}
	if bundle.WorkflowContext != nil {
		if bundle.WorkflowContext.InputManifest.BaseRevision != nil {
			values = appendUniqueRef(values, versionRefFromArtifactReference(*bundle.WorkflowContext.InputManifest.BaseRevision))
		}
		for _, source := range bundle.WorkflowContext.InputManifest.Sources {
			values = appendUniqueRef(values, versionRefFromArtifactReference(source.Ref))
		}
	}
	return values
}

func implementationRevisionLineageSources(bundle WorkbenchBundle) []SystemRevisionSource {
	result := []SystemRevisionSource{
		{Ref: bundle.BlueprintRevision, Purpose: "blueprint", Required: true, Relation: "implemented_by"},
		{Ref: bundle.PageSpecRevision, Purpose: "page_spec", Required: true, Relation: "implemented_by"},
		{Ref: bundle.PrototypeRevision, Purpose: "prototype", Required: true, Relation: "implemented_by"},
	}
	for _, source := range bundle.RequirementRevisions {
		result = append(result, SystemRevisionSource{Ref: source, Purpose: "requirement", Required: true, Relation: "implemented_by"})
	}
	for _, source := range bundle.ContractRevisions {
		result = append(result, SystemRevisionSource{Ref: source, Purpose: "contract", Required: true, Relation: "implemented_by"})
	}
	for _, source := range bundle.DesignSystemRevisions {
		result = append(result, SystemRevisionSource{Ref: source, Purpose: "design_system", Required: true, Relation: "implemented_by"})
	}
	for _, source := range bundle.ContextRevisions {
		result = append(result, SystemRevisionSource{
			Ref: source.Revision, Purpose: "context_" + source.Kind, Required: true, Relation: "implemented_by",
		})
	}
	if bundle.WorkflowContext != nil {
		if bundle.WorkflowContext.InputManifest.BaseRevision != nil {
			result = append(result, SystemRevisionSource{
				Ref:     versionRefFromArtifactReference(*bundle.WorkflowContext.InputManifest.BaseRevision),
				Purpose: "workflow_input_base", Required: true, Relation: "implemented_by",
			})
		}
		for _, source := range bundle.WorkflowContext.InputManifest.Sources {
			result = append(result, SystemRevisionSource{
				Ref: versionRefFromArtifactReference(source.Ref), Purpose: "workflow_input:" + source.Purpose,
				Required: true, Relation: "implemented_by",
			})
		}
	}
	if bundle.CurrentWorkspaceRevision != nil {
		result = append(result, SystemRevisionSource{
			Ref: *bundle.CurrentWorkspaceRevision, Purpose: "workspace_base", Required: true, Relation: "derives_from",
		})
	}
	return result
}

func versionRefFromArtifactReference(reference domain.ArtifactRef) VersionRef {
	var anchorID *string
	if strings.TrimSpace(reference.AnchorID) != "" {
		value := reference.AnchorID
		anchorID = &value
	}
	return VersionRef{
		ArtifactID: reference.ArtifactID, RevisionID: reference.RevisionID,
		ContentHash: reference.ContentHash, AnchorID: anchorID,
	}
}

func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func findFileOperation(values []FileOperation, id string) *FileOperation {
	for index := range values {
		if values[index].ID == id {
			return &values[index]
		}
	}
	return nil
}

func cloneFileOperations(values []FileOperation) []FileOperation {
	result := make([]FileOperation, len(values))
	for index, value := range values {
		value.DependsOn = append([]string(nil), value.DependsOn...)
		value.TraceSource = append([]string(nil), value.TraceSource...)
		if value.Content != nil {
			content := *value.Content
			value.Content = &content
		}
		value.Decision = ImplementationPending
		value.DecidedBy = ""
		value.Reason = ""
		result[index] = value
	}
	return result
}

func cloneRawMessages(values []json.RawMessage) []json.RawMessage {
	result := make([]json.RawMessage, len(values))
	for index, value := range values {
		result[index] = cloneJSON(value)
	}
	return result
}

func cloneVersionRef(value *VersionRef) *VersionRef {
	if value == nil {
		return nil
	}
	clone := *value
	if value.AnchorID != nil {
		anchor := *value.AnchorID
		clone.AnchorID = &anchor
	}
	return &clone
}
