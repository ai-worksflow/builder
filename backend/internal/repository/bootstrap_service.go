package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"gorm.io/gorm"
)

const RepositorySnapshotSchemaVersion = "repository-snapshot/v1"

var (
	ErrBootstrapInvalid             = errors.New("invalid repository Candidate bootstrap")
	ErrBootstrapNotReady            = errors.New("repository Candidate bootstrap source is not ready")
	ErrBootstrapSourceDrift         = errors.New("repository Candidate bootstrap source changed")
	ErrBootstrapFinalizationPending = errors.New("repository Candidate bootstrap finalization is pending")
	ErrBootstrapReconciliation      = errors.New("repository Candidate bootstrap requires reconciliation")
	ErrCandidateHeadLimit           = errors.New("repository Candidate head discovery exceeds its safe limit")

	bootstrapOperationPattern = regexp.MustCompile(`^[A-Za-z0-9._:~-]{1,128}$`)
)

// BootstrapContentReader is the exact immutable-content boundary required to
// turn a canonical WorkspaceRevision into repository files. The SQL row is
// authoritative reachability; both pending and finalized referenced content
// remain readable through content.Store's crash-recovery contract.
type BootstrapContentReader interface {
	Get(context.Context, string, string) (content.StoredContent, error)
}

type BootstrapFileWriter interface {
	Put(context.Context, string, string, []byte) (FileBlobWriteResult, error)
}

type BootstrapFileStore interface {
	BootstrapFileWriter
	CandidateSearchFileReader
	Settle(context.Context, string, string, int64) error
}

type BootstrapTreeStore interface {
	Get(context.Context, string, string, TreeBlobPointer) (TreeManifest, error)
	PutPending(context.Context, string, string, TreeManifest) (TreeBlobPointer, error)
	Finalize(context.Context, string, string, TreeBlobPointer) error
	Abort(context.Context, string, string, TreeBlobPointer) error
}

type BootstrapCandidateReader interface {
	LoadMutationCandidate(context.Context, string, string) (CandidateMutationRecord, error)
}

type BootstrapBuildContractSelection struct {
	ID           string
	ContractHash string
}

// BootstrapBuildContractGate delegates readiness to Constructor authority.
// Repository must not duplicate compiler identities or infer readiness from a
// mutable SQL status column.
type BootstrapBuildContractGate interface {
	RequireReadyForBootstrap(
		context.Context,
		string,
		string,
		string,
		BootstrapBuildContractSelection,
	) error
}

type BootstrapCandidateInput struct {
	ProjectID       string `json:"projectId"`
	BuildManifestID string `json:"buildManifestId"`
	ActorID         string `json:"-"`
	OperationID     string `json:"-"`
}

type BootstrapCandidateResult struct {
	Candidate                 CandidateWorkspace        `json:"candidate"`
	RepositorySnapshotReceipt RepositorySnapshotReceipt `json:"repositorySnapshotReceipt"`
	Created                   bool                      `json:"created"`
	Recovered                 bool                      `json:"recovered"`
	FinalizationPending       bool                      `json:"finalizationPending"`
}

type CandidateHead struct {
	Candidate CandidateWorkspace `json:"candidate"`
	RebaseID  string             `json:"rebaseId,omitempty"`
}

type CandidateHeadList struct {
	SchemaVersion string          `json:"schemaVersion"`
	Candidates    []CandidateHead `json:"candidates"`
}

type CandidateBootstrapService struct {
	database        *gorm.DB
	contents        BootstrapContentReader
	files           BootstrapFileStore
	trees           BootstrapTreeStore
	candidates      BootstrapCandidateReader
	access          CandidateReadAuthorizer
	contracts       BootstrapBuildContractGate
	templates       TemplateSourceMaterializer
	literalIndex    CandidateSearchLiteralIndex
	searchAdmission ExactTreeSearchAdmission
	now             func() time.Time
}

type CandidateBootstrapOption func(*CandidateBootstrapService) error

func WithTemplateSourceMaterializer(materializer TemplateSourceMaterializer) CandidateBootstrapOption {
	return func(service *CandidateBootstrapService) error {
		if materializer == nil {
			return errors.New("template source materializer is required")
		}
		service.templates = materializer
		return nil
	}
}

// CandidateSearchLiteralIndex is an optional derived accelerator. Search
// still treats the exact Candidate aggregate/tree and FileBlobService as
// authority and independently rechecks every returned candidate document.
type CandidateSearchLiteralIndex interface {
	BuildForActor(context.Context, string, string, TreeManifest) (ExactTreeLiteralIndexManifest, error)
	QueryCandidateDocuments(context.Context, ExactTreeLiteralIndexQuery) (ExactTreeLiteralIndexQueryResult, error)
}

func WithCandidateSearchLiteralIndex(index CandidateSearchLiteralIndex) CandidateBootstrapOption {
	return func(service *CandidateBootstrapService) error {
		if index == nil {
			return errors.New("Candidate search literal index is required")
		}
		service.literalIndex = index
		return nil
	}
}

func WithExactTreeSearchAdmission(admission ExactTreeSearchAdmission) CandidateBootstrapOption {
	return func(service *CandidateBootstrapService) error {
		if admission == nil {
			return errors.New("exact-tree Candidate search admission is required")
		}
		service.searchAdmission = admission
		return nil
	}
}

func NewCandidateBootstrapService(
	database *gorm.DB,
	contents BootstrapContentReader,
	files BootstrapFileStore,
	trees BootstrapTreeStore,
	candidates BootstrapCandidateReader,
	access CandidateReadAuthorizer,
	contracts BootstrapBuildContractGate,
	now func() time.Time,
	options ...CandidateBootstrapOption,
) (*CandidateBootstrapService, error) {
	if database == nil || contents == nil || files == nil || trees == nil || candidates == nil ||
		access == nil || contracts == nil || now == nil {
		return nil, errors.New("repository Candidate bootstrap database, stores, authorizer, and clock are required")
	}
	service := &CandidateBootstrapService{
		database: database, contents: contents, files: files, trees: trees,
		candidates: candidates, access: access, contracts: contracts, now: now,
	}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("repository Candidate bootstrap option is required")
		}
		if err := option(service); err != nil {
			return nil, err
		}
	}
	if service.literalIndex != nil && service.searchAdmission == nil {
		return nil, errors.New("Candidate search literal index requires exact-tree search admission")
	}
	return service, nil
}

// Bootstrap is an idempotent get-or-create operation. Stable IDs are derived
// from the authenticated actor, exact BuildManifest, and durable HTTP
// idempotency key so a lost SQL acknowledgement can be recovered without
// creating a second Candidate. Clients never supply a contract, template,
// WorkspaceRevision, tree, file hash, or aggregate ID.
func (service *CandidateBootstrapService) Bootstrap(
	ctx context.Context,
	input BootstrapCandidateInput,
) (BootstrapCandidateResult, error) {
	input, err := normalizeBootstrapInput(input)
	if err != nil {
		return BootstrapCandidateResult{}, err
	}
	if err := service.access.RequireProjectEdit(ctx, input.ProjectID, input.ActorID); err != nil {
		return BootstrapCandidateResult{}, fmt.Errorf("authorize repository Candidate bootstrap: %w", err)
	}

	snapshotID := bootstrapUUID(input, "snapshot")
	candidateID := bootstrapUUID(input, "candidate")
	if existing, found, loadErr := service.loadExisting(ctx, input, snapshotID, candidateID); loadErr != nil {
		return BootstrapCandidateResult{}, loadErr
	} else if found {
		candidate := existing.Record.Candidate
		if gateErr := service.contracts.RequireReadyForBootstrap(
			ctx, input.ProjectID, input.BuildManifestID, input.ActorID,
			BootstrapBuildContractSelection{ID: candidate.BuildContract.ID, ContractHash: candidate.BuildContract.ContentHash},
		); gateErr != nil {
			return BootstrapCandidateResult{}, errors.Join(ErrBootstrapNotReady, gateErr)
		}
		return service.settleExisting(ctx, input.ProjectID, existing, true)
	}

	source, err := service.loadSource(ctx, service.database, input.ProjectID, input.BuildManifestID, false)
	if err != nil {
		return BootstrapCandidateResult{}, err
	}
	if gateErr := service.contracts.RequireReadyForBootstrap(
		ctx, input.ProjectID, input.BuildManifestID, input.ActorID,
		BootstrapBuildContractSelection{ID: source.BuildContractID, ContractHash: source.BuildContractHash},
	); gateErr != nil {
		return BootstrapCandidateResult{}, errors.Join(ErrBootstrapNotReady, gateErr)
	}
	workspaceFiles, templateComponents, err := service.materializeBootstrapFiles(ctx, source)
	if err != nil {
		return BootstrapCandidateResult{}, err
	}

	treeFiles := make([]TreeFile, 0, len(workspaceFiles))
	for _, file := range workspaceFiles {
		written, writeErr := service.files.Put(ctx, input.ProjectID, input.ActorID, file.Content)
		if writeErr != nil && !errors.Is(writeErr, ErrFileBlobFinalizationPending) {
			return BootstrapCandidateResult{}, fmt.Errorf("persist bootstrap file %s: %w", file.Path, writeErr)
		}
		digest := sha256.Sum256(file.Content)
		expectedContentHash := fmt.Sprintf("sha256:%x", digest[:])
		if pointerErr := validateCatalogPointer(
			written.Pointer, input.ProjectID, expectedContentHash, int64(len(file.Content)),
		); pointerErr != nil {
			return BootstrapCandidateResult{}, fmt.Errorf("persist bootstrap file %s: %w", file.Path, pointerErr)
		}
		treeFiles = append(treeFiles, TreeFile{
			Path: file.Path, Mode: file.Mode, ContentHash: written.Pointer.ContentHash,
			ByteSize: written.Pointer.ByteSize,
		})
	}
	tree, err := NewTree(treeFiles)
	if err != nil {
		return BootstrapCandidateResult{}, fmt.Errorf("build canonical repository tree: %w", err)
	}
	pointer, err := service.trees.PutPending(ctx, input.ProjectID, snapshotID, tree)
	if err != nil {
		return BootstrapCandidateResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = service.trees.Abort(context.WithoutCancel(ctx), input.ProjectID, snapshotID, pointer)
		}
	}()

	now := service.now().UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		return BootstrapCandidateResult{}, fmt.Errorf("%w: clock returned a zero timestamp", ErrBootstrapInvalid)
	}
	snapshot := RepositorySnapshot{
		ID: snapshotID, ProjectID: input.ProjectID,
		BuildManifest:     ExactReference{ID: source.BuildManifestID, ContentHash: source.BuildManifestHash},
		BuildContract:     ExactReference{ID: source.BuildContractID, ContentHash: source.BuildContractHash},
		FullStackTemplate: ExactReference{ID: source.FullStackTemplateID, ContentHash: source.FullStackTemplateHash},
		Tree:              tree, CreatedBy: input.ActorID, CreatedAt: now,
	}
	if source.WorkspaceRevisionID != "" {
		snapshot.BaseWorkspaceRevision = &ExactRevisionReference{
			ArtifactID: source.WorkspaceArtifactID, RevisionID: source.WorkspaceRevisionID,
			ContentHash: source.WorkspaceContentHash,
		}
	}
	candidate, err := NewCandidate(candidateID, snapshot, input.ActorID, now)
	if err != nil {
		return BootstrapCandidateResult{}, err
	}
	receipt, err := NewRepositorySnapshotReceipt(snapshot, pointer, templateComponents)
	if err != nil {
		return BootstrapCandidateResult{}, err
	}

	transactionErr := service.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		locked, loadErr := service.loadSource(ctx, transaction, input.ProjectID, input.BuildManifestID, true)
		if loadErr != nil {
			return loadErr
		}
		if locked != source {
			return ErrBootstrapSourceDrift
		}
		return insertBootstrapRows(transaction, snapshot, candidate, pointer)
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if transactionErr != nil {
		result, recovered, recoveryErr := service.recoverCommit(
			ctx, input, snapshotID, candidateID, pointer, transactionErr,
		)
		if recovered {
			committed = true
		}
		if recoveryErr != nil {
			return result, recoveryErr
		}
		return result, nil
	}
	committed = true

	result := BootstrapCandidateResult{
		Candidate: candidate, Created: true,
	}
	if err := service.settleRepositorySnapshotContent(ctx, input.ProjectID, pointer, tree); err != nil {
		result.FinalizationPending = true
		return result, errors.Join(ErrBootstrapFinalizationPending, err)
	}
	if err := service.recordRepositorySnapshotReceipt(ctx, receipt); err != nil {
		result.FinalizationPending = true
		return result, errors.Join(ErrBootstrapReconciliation, err)
	}
	result.RepositorySnapshotReceipt = receipt
	return result, nil
}

// Get returns the current durable Candidate projection. It is intentionally
// project-scoped and authorized before the aggregate reader is invoked so a
// Candidate UUID cannot be used as a cross-tenant existence oracle.
func (service *CandidateBootstrapService) Get(
	ctx context.Context,
	projectID, candidateID, actorID string,
) (CandidateWorkspace, error) {
	projectID = strings.TrimSpace(projectID)
	candidateID = strings.TrimSpace(candidateID)
	actorID = strings.TrimSpace(actorID)
	if !validUUID(projectID) || !validUUID(candidateID) || !validUUID(actorID) {
		return CandidateWorkspace{}, ErrBootstrapInvalid
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return CandidateWorkspace{}, fmt.Errorf("authorize repository Candidate read: %w", err)
	}
	record, err := service.candidates.LoadMutationCandidate(ctx, projectID, candidateID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CandidateWorkspace{}, ErrCandidateNotFound
	}
	if err != nil {
		return CandidateWorkspace{}, fmt.Errorf("load repository Candidate: %w", err)
	}
	return record.Candidate, nil
}

// ListHeads discovers durable project Candidate lineage heads without making
// a browser-local pointer authoritative. A Candidate with an outgoing rebase
// is not a head; its active successor is returned with the incoming rebase ID
// so a different browser can resume an exact conflicted/applying lineage.
func (service *CandidateBootstrapService) ListHeads(
	ctx context.Context,
	projectID, actorID string,
) (CandidateHeadList, error) {
	projectID = strings.TrimSpace(projectID)
	actorID = strings.TrimSpace(actorID)
	if ctx == nil || !validUUID(projectID) || !validUUID(actorID) {
		return CandidateHeadList{}, ErrBootstrapInvalid
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return CandidateHeadList{}, fmt.Errorf("authorize repository Candidate head discovery: %w", err)
	}
	type headRow struct {
		CandidateID string         `gorm:"column:candidate_id"`
		RebaseID    sql.NullString `gorm:"column:rebase_id"`
	}
	rows := make([]headRow, 0)
	result := service.database.WithContext(ctx).Raw(`
SELECT candidate.id::text AS candidate_id, incoming.id::text AS rebase_id
FROM candidate_workspaces AS candidate
LEFT JOIN candidate_rebases AS incoming
  ON incoming.successor_candidate_id = candidate.id
WHERE candidate.project_id = ?
  AND candidate.status = 'active'
  AND NOT EXISTS (
    SELECT 1 FROM candidate_rebases AS outgoing
    WHERE outgoing.predecessor_candidate_id = candidate.id
  )
ORDER BY candidate.updated_at DESC, candidate.id DESC
LIMIT 101
`, projectID).Scan(&rows)
	if result.Error != nil {
		return CandidateHeadList{}, fmt.Errorf("discover repository Candidate heads: %w", result.Error)
	}
	if len(rows) > 100 {
		return CandidateHeadList{}, ErrCandidateHeadLimit
	}
	heads := CandidateHeadList{
		SchemaVersion: "repository-candidate-head-list/v1",
		Candidates:    make([]CandidateHead, 0, len(rows)),
	}
	for _, row := range rows {
		record, err := service.candidates.LoadMutationCandidate(ctx, projectID, row.CandidateID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return CandidateHeadList{}, ErrCandidateNotFound
		}
		if err != nil {
			return CandidateHeadList{}, fmt.Errorf("hydrate repository Candidate head: %w", err)
		}
		if record.Candidate.ProjectID != projectID || record.Candidate.Status != CandidateActive {
			return CandidateHeadList{}, fmt.Errorf("%w: discovered Candidate is not an active same-project head", ErrInvalidCandidate)
		}
		head := CandidateHead{Candidate: record.Candidate}
		if row.RebaseID.Valid {
			head.RebaseID = row.RebaseID.String
		}
		heads.Candidates = append(heads.Candidates, head)
	}
	return heads, nil
}

type bootstrapSource struct {
	BuildManifestID       string `gorm:"column:build_manifest_id"`
	ProjectID             string `gorm:"column:project_id"`
	BuildManifestHash     string `gorm:"column:build_manifest_hash"`
	ManifestStatus        string `gorm:"column:manifest_status"`
	BuildContractID       string `gorm:"column:build_contract_id"`
	BuildContractHash     string `gorm:"column:build_contract_hash"`
	FullStackTemplateID   string `gorm:"column:full_stack_template_id"`
	FullStackTemplateHash string `gorm:"column:full_stack_template_hash"`
	WorkspaceArtifactID   string `gorm:"column:workspace_artifact_id"`
	WorkspaceRevisionID   string `gorm:"column:workspace_revision_id"`
	WorkspaceContentStore string `gorm:"column:workspace_content_store"`
	WorkspaceContentRef   string `gorm:"column:workspace_content_ref"`
	WorkspaceContentHash  string `gorm:"column:workspace_content_hash"`
	WorkspaceByteSize     int64  `gorm:"column:workspace_byte_size"`
	WorkspaceSchema       int    `gorm:"column:workspace_schema"`
	WorkspaceStatus       string `gorm:"column:workspace_status"`
	TemplateComponentHash string `gorm:"-"`
}

const bootstrapTemplateSourceQuery = `
SELECT manifest.id::text AS build_manifest_id,
       manifest.project_id::text AS project_id,
       manifest.manifest_hash AS build_manifest_hash,
       manifest.status AS manifest_status,
       contract.id::text AS build_contract_id,
       contract.contract_hash AS build_contract_hash,
       contract.full_stack_template_id::text AS full_stack_template_id,
       contract.full_stack_template_hash
FROM application_build_manifests AS manifest
JOIN application_build_contracts AS contract
  ON contract.project_id = manifest.project_id
 AND contract.build_manifest_id = manifest.id
 AND contract.build_manifest_hash = manifest.manifest_hash
 AND contract.status = 'ready'
JOIN full_stack_template_releases AS stack
  ON stack.id = contract.full_stack_template_id
 AND stack.content_hash = contract.full_stack_template_hash
WHERE manifest.id = @manifest_id
  AND manifest.project_id = @project_id
  AND manifest.status = 'frozen'
  AND manifest.workspace_revision_id IS NULL
  AND NOT EXISTS (
    SELECT 1
    FROM full_stack_template_components AS component
    LEFT JOIN template_release_policies AS policy
      ON policy.template_release_id = component.template_release_id
     AND policy.release_content_hash = component.template_release_content_hash
    WHERE component.full_stack_template_id = stack.id
      AND component.full_stack_content_hash = stack.content_hash
      AND (policy.template_release_id IS NULL OR policy.state <> 'approved')
  )
  AND NOT EXISTS (
    SELECT 1
    FROM application_build_contract_template_releases AS selected
    LEFT JOIN full_stack_template_components AS component
      ON component.full_stack_template_id = stack.id
     AND component.full_stack_content_hash = stack.content_hash
     AND component.role = selected.role
     AND component.template_release_id = selected.template_release_id
     AND component.template_release_content_hash = selected.template_release_content_hash
    WHERE selected.contract_id = contract.id
      AND component.template_release_id IS NULL
  )
  AND (SELECT count(*) FROM application_build_contract_template_releases AS selected
       WHERE selected.contract_id = contract.id) >= 2
LIMIT 2`

const bootstrapSourceQuery = `
SELECT manifest.id::text AS build_manifest_id,
       manifest.project_id::text AS project_id,
       manifest.manifest_hash AS build_manifest_hash,
       manifest.status AS manifest_status,
       contract.id::text AS build_contract_id,
       contract.contract_hash AS build_contract_hash,
       contract.full_stack_template_id::text AS full_stack_template_id,
       contract.full_stack_template_hash,
       artifact.id::text AS workspace_artifact_id,
       revision.id::text AS workspace_revision_id,
       revision.content_store AS workspace_content_store,
       revision.content_ref AS workspace_content_ref,
       revision.content_hash AS workspace_content_hash,
       revision.byte_size AS workspace_byte_size,
       revision.schema_version AS workspace_schema,
       revision.workflow_status AS workspace_status
FROM application_build_manifests AS manifest
JOIN application_build_contracts AS contract
  ON contract.project_id = manifest.project_id
 AND contract.build_manifest_id = manifest.id
 AND contract.build_manifest_hash = manifest.manifest_hash
 AND contract.status = 'ready'
JOIN full_stack_template_releases AS stack
  ON stack.id = contract.full_stack_template_id
 AND stack.content_hash = contract.full_stack_template_hash
JOIN artifacts AS artifact
  ON artifact.project_id = manifest.project_id
 AND artifact.kind = 'workspace'
JOIN artifact_revisions AS revision
  ON revision.artifact_id = artifact.id
 AND revision.workflow_status IN ('approved', 'superseded')
LEFT JOIN implementation_proposals AS proposal
  ON proposal.id = revision.implementation_proposal_id
WHERE manifest.id = @manifest_id
  AND manifest.project_id = @project_id
  AND (
    (manifest.status = 'frozen'
      AND manifest.workspace_revision_id IS NOT NULL
      AND revision.id = manifest.workspace_revision_id)
    OR
    (manifest.status = 'consumed'
      AND proposal.project_id = manifest.project_id
      AND proposal.build_manifest_id = manifest.id
      AND proposal.status IN ('applied', 'partially_applied'))
  )
  AND EXISTS (
    SELECT 1 FROM full_stack_template_components AS component
    WHERE component.full_stack_template_id = stack.id
      AND component.full_stack_content_hash = stack.content_hash
      AND component.role = 'web'
  )
  AND EXISTS (
    SELECT 1 FROM full_stack_template_components AS component
    WHERE component.full_stack_template_id = stack.id
      AND component.full_stack_content_hash = stack.content_hash
      AND component.role = 'api'
  )
  AND NOT EXISTS (
    SELECT 1
    FROM full_stack_template_components AS component
    LEFT JOIN template_release_policies AS policy
      ON policy.template_release_id = component.template_release_id
     AND policy.release_content_hash = component.template_release_content_hash
    WHERE component.full_stack_template_id = stack.id
      AND component.full_stack_content_hash = stack.content_hash
      AND (policy.template_release_id IS NULL OR policy.state <> 'approved')
  )
  AND NOT EXISTS (
    SELECT 1
    FROM application_build_contract_template_releases AS selected
    LEFT JOIN full_stack_template_components AS component
      ON component.full_stack_template_id = stack.id
     AND component.full_stack_content_hash = stack.content_hash
     AND component.role = selected.role
     AND component.template_release_id = selected.template_release_id
     AND component.template_release_content_hash = selected.template_release_content_hash
    WHERE selected.contract_id = contract.id
      AND component.template_release_id IS NULL
  )
  AND (SELECT count(*) FROM application_build_contract_template_releases AS selected
       WHERE selected.contract_id = contract.id) >= 2
ORDER BY revision.revision_number DESC, contract.created_at DESC, contract.id DESC
LIMIT 2`

func (service *CandidateBootstrapService) loadSource(
	ctx context.Context,
	database *gorm.DB,
	projectID, manifestID string,
	lock bool,
) (bootstrapSource, error) {
	query := bootstrapSourceQuery
	if lock {
		query += "\nFOR SHARE OF manifest, contract, stack, artifact, revision"
	}
	var rows []bootstrapSource
	result := database.WithContext(ctx).Raw(query, map[string]any{
		"project_id": projectID, "manifest_id": manifestID,
	}).Scan(&rows)
	if result.Error != nil {
		return bootstrapSource{}, fmt.Errorf("load repository Candidate bootstrap source: %w", result.Error)
	}
	if len(rows) == 0 {
		fallbackQuery := bootstrapTemplateSourceQuery
		if lock {
			fallbackQuery += "\nFOR SHARE OF manifest, contract, stack"
		}
		result = database.WithContext(ctx).Raw(fallbackQuery, map[string]any{
			"project_id": projectID, "manifest_id": manifestID,
		}).Scan(&rows)
		if result.Error != nil {
			return bootstrapSource{}, fmt.Errorf("load repository template bootstrap source: %w", result.Error)
		}
	}
	if len(rows) != 1 {
		return bootstrapSource{}, fmt.Errorf(
			"%w: expected one exact frozen/consumed manifest, ready contract, approved stack, and canonical WorkspaceRevision",
			ErrBootstrapNotReady,
		)
	}
	if err := validateBootstrapSource(rows[0], projectID, manifestID); err != nil {
		return bootstrapSource{}, err
	}
	components, err := service.loadTemplateSourceComponents(ctx, database, rows[0])
	if err != nil {
		return bootstrapSource{}, err
	}
	rows[0].TemplateComponentHash, err = templateSourceComponentHash(components)
	if err != nil {
		return bootstrapSource{}, err
	}
	return rows[0], nil
}

func validateBootstrapSource(source bootstrapSource, projectID, manifestID string) error {
	if source.ProjectID != projectID || source.BuildManifestID != manifestID ||
		!validUUID(source.ProjectID) || !validUUID(source.BuildManifestID) ||
		!validUUID(source.BuildContractID) || !validUUID(source.FullStackTemplateID) ||
		!isCanonicalExternalHash(source.BuildManifestHash) ||
		!isCanonicalExternalHash(source.BuildContractHash) ||
		!isCanonicalExternalHash(source.FullStackTemplateHash) ||
		(source.ManifestStatus != "frozen" && source.ManifestStatus != "consumed") ||
		!validBootstrapWorkspaceSource(source) {
		return fmt.Errorf("%w: persisted bootstrap source violates its exact projection", ErrBootstrapSourceDrift)
	}
	return nil
}

func validBootstrapWorkspaceSource(source bootstrapSource) bool {
	if source.WorkspaceRevisionID == "" {
		return source.ManifestStatus == "frozen" && source.WorkspaceArtifactID == "" &&
			source.WorkspaceContentStore == "" && source.WorkspaceContentRef == "" &&
			source.WorkspaceContentHash == "" && source.WorkspaceByteSize == 0 &&
			source.WorkspaceSchema == 0 && source.WorkspaceStatus == ""
	}
	return validUUID(source.WorkspaceArtifactID) && validUUID(source.WorkspaceRevisionID) &&
		isCanonicalSHA256(source.WorkspaceContentHash) &&
		(source.WorkspaceStatus == "approved" || source.WorkspaceStatus == "superseded") &&
		source.WorkspaceContentStore != "" && source.WorkspaceContentRef != "" &&
		source.WorkspaceByteSize > 0 && source.WorkspaceSchema > 0
}

func validateBootstrapStoredContent(stored content.StoredContent, source bootstrapSource) error {
	if stored.ID != source.WorkspaceContentRef || stored.ProjectID != source.ProjectID ||
		stored.AggregateType != "artifact_revision" || stored.AggregateID != source.WorkspaceRevisionID ||
		stored.SchemaVersion != source.WorkspaceSchema || stored.ContentHash != source.WorkspaceContentHash ||
		stored.ByteSize != source.WorkspaceByteSize ||
		(stored.State != content.StatePending && stored.State != content.StateFinalized) {
		return fmt.Errorf("%w: WorkspaceRevision content identity or integrity differs from PostgreSQL", ErrBootstrapSourceDrift)
	}
	return nil
}

func decodeBootstrapWorkspace(payload json.RawMessage) ([]TemplateSourceFile, error) {
	var workspace struct {
		Files []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"files"`
	}
	if err := json.Unmarshal(payload, &workspace); err != nil {
		return nil, fmt.Errorf("%w: WorkspaceRevision is not valid JSON: %v", ErrBootstrapSourceDrift, err)
	}
	if len(workspace.Files) == 0 {
		return nil, fmt.Errorf("%w: WorkspaceRevision contains no project files", ErrBootstrapNotReady)
	}
	if len(workspace.Files) > MaxTreeFiles {
		return nil, fmt.Errorf("%w: WorkspaceRevision contains too many files", ErrBootstrapInvalid)
	}
	result := make([]TemplateSourceFile, 0, len(workspace.Files))
	for index, file := range workspace.Files {
		result = append(result, TemplateSourceFile{
			Path: file.Path, Mode: "100644", Content: []byte(file.Content),
		})
		if _, err := NormalizePath(file.Path); err != nil {
			return nil, fmt.Errorf("%w: workspace.files[%d].path is unsafe or non-canonical", ErrBootstrapInvalid, index)
		}
	}
	return validateTemplateSourceFiles(result)
}

func normalizeBootstrapInput(input BootstrapCandidateInput) (BootstrapCandidateInput, error) {
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.BuildManifestID = strings.TrimSpace(input.BuildManifestID)
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	if !validUUID(input.ProjectID) || !validUUID(input.BuildManifestID) || !validUUID(input.ActorID) ||
		!bootstrapOperationPattern.MatchString(input.OperationID) {
		return BootstrapCandidateInput{}, ErrBootstrapInvalid
	}
	return input, nil
}

func bootstrapUUID(input BootstrapCandidateInput, purpose string) string {
	value := strings.Join([]string{
		"repository-candidate-bootstrap/v1", input.ProjectID, input.BuildManifestID,
		input.ActorID, input.OperationID, purpose,
	}, "\x00")
	digest := sha256.Sum256([]byte(value))
	identifier, _ := uuid.FromBytes(digest[:16])
	identifier[6] = (identifier[6] & 0x0f) | 0x50
	identifier[8] = (identifier[8] & 0x3f) | 0x80
	return identifier.String()
}

func insertBootstrapRows(
	transaction *gorm.DB,
	snapshot RepositorySnapshot,
	candidate CandidateWorkspace,
	pointer TreeBlobPointer,
) error {
	var baseArtifactID, baseRevisionID, baseContentHash any
	if base := snapshot.BaseWorkspaceRevision; base != nil {
		baseArtifactID, baseRevisionID, baseContentHash = base.ArtifactID, base.RevisionID, base.ContentHash
	}
	insertSnapshot := transaction.Exec(`
INSERT INTO repository_snapshots (
  id, schema_version, project_id,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
  tree_file_count, tree_byte_size, created_by, created_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
`, snapshot.ID, RepositorySnapshotSchemaVersion, snapshot.ProjectID,
		snapshot.BuildManifest.ID, snapshot.BuildManifest.ContentHash,
		snapshot.BuildContract.ID, snapshot.BuildContract.ContentHash,
		snapshot.FullStackTemplate.ID, snapshot.FullStackTemplate.ContentHash,
		baseArtifactID, baseRevisionID, baseContentHash,
		pointer.Store, pointer.OwnerID, pointer.Ref, pointer.ContentObjectHash, pointer.TreeHash,
		pointer.FileCount, pointer.ByteSize, snapshot.CreatedBy, snapshot.CreatedAt)
	if insertSnapshot.Error != nil {
		return insertSnapshot.Error
	}
	if insertSnapshot.RowsAffected != 1 {
		return ErrBootstrapSourceDrift
	}

	insertCandidate := transaction.Exec(`
INSERT INTO candidate_workspaces (
  id, schema_version, project_id, repository_snapshot_id,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  base_workspace_artifact_id, base_workspace_revision_id, base_workspace_content_hash,
  base_tree_store, base_tree_owner_id, base_tree_ref, base_tree_content_hash, base_tree_hash,
  current_tree_store, current_tree_owner_id, current_tree_ref, current_tree_content_hash, current_tree_hash,
  current_tree_file_count, current_tree_byte_size,
  status, dirty, conflicted, stale, rebase_required,
  session_epoch, version, journal_sequence, writer_lease_epoch,
  created_by, created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
  ?, false, false, false, false, 1, 1, 0, 0, ?, ?, ?
)
`, candidate.ID, candidate.SchemaVersion, candidate.ProjectID, candidate.RepositorySnapshotID,
		candidate.BuildManifest.ID, candidate.BuildManifest.ContentHash,
		candidate.BuildContract.ID, candidate.BuildContract.ContentHash,
		candidate.FullStackTemplate.ID, candidate.FullStackTemplate.ContentHash,
		baseArtifactID, baseRevisionID, baseContentHash,
		pointer.Store, pointer.OwnerID, pointer.Ref, pointer.ContentObjectHash, pointer.TreeHash,
		pointer.Store, pointer.OwnerID, pointer.Ref, pointer.ContentObjectHash, pointer.TreeHash,
		pointer.FileCount, pointer.ByteSize, candidate.Status, candidate.CreatedBy,
		candidate.CreatedAt, candidate.UpdatedAt)
	if insertCandidate.Error != nil {
		return insertCandidate.Error
	}
	if insertCandidate.RowsAffected != 1 {
		return ErrBootstrapSourceDrift
	}
	return nil
}

type bootstrapExisting struct {
	Record  CandidateMutationRecord
	Pointer TreeBlobPointer
}

func (service *CandidateBootstrapService) loadExisting(
	ctx context.Context,
	input BootstrapCandidateInput,
	snapshotID, candidateID string,
) (bootstrapExisting, bool, error) {
	record, err := service.candidates.LoadMutationCandidate(ctx, input.ProjectID, candidateID)
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrCandidateNotFound) {
		return bootstrapExisting{}, false, nil
	}
	if err != nil {
		return bootstrapExisting{}, false, fmt.Errorf("load idempotent repository Candidate bootstrap: %w", err)
	}
	candidate := record.Candidate
	if candidate.ID != candidateID || candidate.ProjectID != input.ProjectID ||
		candidate.RepositorySnapshotID != snapshotID || candidate.BuildManifest.ID != input.BuildManifestID ||
		candidate.CreatedBy != input.ActorID {
		return bootstrapExisting{}, false, ErrBootstrapSourceDrift
	}
	pointer, err := service.loadSnapshotPointer(ctx, input.ProjectID, snapshotID)
	if err != nil {
		return bootstrapExisting{}, false, err
	}
	if pointer.TreeHash != candidate.BaseTreeHash {
		return bootstrapExisting{}, false, ErrBootstrapSourceDrift
	}
	return bootstrapExisting{Record: record, Pointer: pointer}, true, nil
}

func (service *CandidateBootstrapService) loadSnapshotPointer(
	ctx context.Context,
	projectID, snapshotID string,
) (TreeBlobPointer, error) {
	var row struct {
		Store       string `gorm:"column:tree_store"`
		OwnerID     string `gorm:"column:tree_owner_id"`
		Ref         string `gorm:"column:tree_ref"`
		ContentHash string `gorm:"column:tree_content_hash"`
		TreeHash    string `gorm:"column:tree_hash"`
		FileCount   int    `gorm:"column:tree_file_count"`
		ByteSize    int64  `gorm:"column:tree_byte_size"`
	}
	result := service.database.WithContext(ctx).Raw(`
SELECT tree_store, tree_owner_id::text AS tree_owner_id, tree_ref,
       tree_content_hash, tree_hash, tree_file_count, tree_byte_size
FROM repository_snapshots
WHERE project_id = ? AND id = ?
`, projectID, snapshotID).Scan(&row)
	if result.Error != nil {
		return TreeBlobPointer{}, fmt.Errorf("load repository snapshot tree pointer: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return TreeBlobPointer{}, ErrBootstrapSourceDrift
	}
	pointer := TreeBlobPointer{
		Store: row.Store, OwnerID: row.OwnerID, Ref: row.Ref, ContentObjectHash: row.ContentHash,
		TreeHash: row.TreeHash, FileCount: row.FileCount, ByteSize: row.ByteSize,
	}
	if err := pointer.validate(); err != nil || pointer.OwnerID != snapshotID {
		return TreeBlobPointer{}, errors.Join(ErrBootstrapSourceDrift, err)
	}
	return pointer, nil
}

func (service *CandidateBootstrapService) settleExisting(
	ctx context.Context,
	projectID string,
	existing bootstrapExisting,
	recovered bool,
) (BootstrapCandidateResult, error) {
	result := BootstrapCandidateResult{Candidate: existing.Record.Candidate, Recovered: recovered}
	receipt, err := service.loadRepositorySnapshotReceipt(
		ctx, projectID, existing.Record.Candidate.RepositorySnapshotID, &existing.Pointer, false,
	)
	if err != nil {
		return result, errors.Join(ErrBootstrapReconciliation, err)
	}
	if !repositorySnapshotMatchesCandidate(receipt.Snapshot, existing.Record.Candidate) {
		return result, errors.Join(ErrBootstrapReconciliation, ErrRepositorySnapshotIntegrity)
	}
	tree, err := service.trees.Get(ctx, projectID, existing.Pointer.OwnerID, existing.Pointer)
	if err != nil {
		return result, errors.Join(ErrBootstrapReconciliation, err)
	}
	if err := service.settleRepositorySnapshotContent(ctx, projectID, existing.Pointer, tree); err != nil {
		result.FinalizationPending = true
		return result, errors.Join(ErrBootstrapFinalizationPending, err)
	}
	if err := service.recordRepositorySnapshotReceipt(ctx, receipt); err != nil {
		result.FinalizationPending = true
		return result, errors.Join(ErrBootstrapReconciliation, err)
	}
	result.RepositorySnapshotReceipt = receipt
	return result, nil
}

func (service *CandidateBootstrapService) recoverCommit(
	ctx context.Context,
	input BootstrapCandidateInput,
	snapshotID, candidateID string,
	pending TreeBlobPointer,
	transactionErr error,
) (BootstrapCandidateResult, bool, error) {
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	existing, found, loadErr := service.loadExisting(recoveryContext, input, snapshotID, candidateID)
	if loadErr != nil {
		return BootstrapCandidateResult{}, false, errors.Join(
			ErrBootstrapReconciliation,
			fmt.Errorf("commit repository Candidate bootstrap: %w", transactionErr),
			loadErr,
		)
	}
	if !found {
		if abortErr := service.trees.Abort(recoveryContext, input.ProjectID, snapshotID, pending); abortErr != nil {
			return BootstrapCandidateResult{}, false, errors.Join(
				fmt.Errorf("commit repository Candidate bootstrap: %w", transactionErr),
				fmt.Errorf("abort uncommitted bootstrap tree: %w", abortErr),
			)
		}
		return BootstrapCandidateResult{}, true, fmt.Errorf("commit repository Candidate bootstrap: %w", transactionErr)
	}
	if existing.Pointer != pending {
		if abortErr := service.trees.Abort(recoveryContext, input.ProjectID, snapshotID, pending); abortErr != nil {
			return BootstrapCandidateResult{Candidate: existing.Record.Candidate, Recovered: true}, true, errors.Join(
				ErrBootstrapReconciliation,
				fmt.Errorf("abort duplicate bootstrap tree: %w", abortErr),
			)
		}
	}
	result, settleErr := service.settleExisting(recoveryContext, input.ProjectID, existing, true)
	return result, true, settleErr
}
