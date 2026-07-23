package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/worksflow/builder/backend/internal/constructor"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"gorm.io/gorm"
)

type PlanningContentReader interface {
	Get(context.Context, string, string) (content.StoredContent, error)
}

type PlanningCandidateReader interface {
	LoadMutationCandidate(context.Context, string, string) (repository.CandidateMutationRecord, error)
}

type PlanningFileResolver interface {
	Resolve(context.Context, string, string, int64) (repository.FileBlobPointer, []byte, error)
}

type PlanningPathPolicyResolver interface {
	ResolvePathPolicy(context.Context, repository.PathPolicySubject) (repository.PathPolicy, error)
}

type PostgresPlanningSourceConfig struct {
	OutputSchemaHash string
	AllowedTools     []string
	Budgets          TaskBudgets
	MaxContextFiles  int
}

type PostgresPlanningSource struct {
	database   *gorm.DB
	contents   PlanningContentReader
	candidates PlanningCandidateReader
	files      PlanningFileResolver
	policies   PlanningPathPolicyResolver
	config     PostgresPlanningSourceConfig
}

func NewPostgresPlanningSource(
	database *gorm.DB,
	contents PlanningContentReader,
	candidates PlanningCandidateReader,
	files PlanningFileResolver,
	policies PlanningPathPolicyResolver,
	config PostgresPlanningSourceConfig,
) (*PostgresPlanningSource, error) {
	if database == nil || contents == nil || candidates == nil || files == nil || policies == nil {
		return nil, errors.New("agent planning database, content, Candidate, file, and path-policy sources are required")
	}
	if config.AllowedTools == nil {
		config.AllowedTools = []string{"file.read", "file.write", "file.search", "shell.exec", "diagnostic.read"}
	}
	tools, err := normalizeTools(config.AllowedTools)
	if err != nil {
		return nil, err
	}
	config.AllowedTools = tools
	if err := config.Budgets.validate(); err != nil {
		return nil, err
	}
	if !sha256Pattern.MatchString(config.OutputSchemaHash) {
		return nil, fmt.Errorf("%w: planning output schema hash", ErrInvalidTaskCapsule)
	}
	if config.MaxContextFiles <= 0 {
		config.MaxContextFiles = 256
	}
	if config.MaxContextFiles > 480 {
		return nil, fmt.Errorf("%w: planning context file limit", ErrInvalidTaskCapsule)
	}
	return &PostgresPlanningSource{
		database: database, contents: contents, candidates: candidates,
		files: files, policies: policies, config: config,
	}, nil
}

type planningSessionRow struct {
	ProjectID                 string `gorm:"column:project_id"`
	SessionID                 string `gorm:"column:session_id"`
	SessionState              string `gorm:"column:session_state"`
	CandidateID               string `gorm:"column:candidate_id"`
	CandidateVersion          int64  `gorm:"column:candidate_version"`
	CandidateSessionEpoch     int64  `gorm:"column:candidate_session_epoch"`
	CandidateWriterLeaseEpoch int64  `gorm:"column:candidate_writer_lease_epoch"`
	CandidateTreeHash         string `gorm:"column:candidate_tree_hash"`
	BuildManifestID           string `gorm:"column:build_manifest_id"`
	BuildManifestHash         string `gorm:"column:build_manifest_hash"`
	BuildContractID           string `gorm:"column:build_contract_id"`
	BuildContractHash         string `gorm:"column:build_contract_hash"`
	FullStackTemplateID       string `gorm:"column:full_stack_template_id"`
	FullStackTemplateHash     string `gorm:"column:full_stack_template_hash"`
	ContractContentRef        string `gorm:"column:contract_content_ref"`
	ContractContentHash       string `gorm:"column:contract_content_hash"`
	ContractStatus            string `gorm:"column:contract_status"`
	MustCount                 int    `gorm:"column:must_count"`
	MustReadyCount            int    `gorm:"column:must_ready_count"`
	ObligationCount           int    `gorm:"column:obligation_count"`
	SourceCount               int    `gorm:"column:source_count"`
	TemplateReleaseCount      int    `gorm:"column:template_release_count"`
	BlockingCount             int    `gorm:"column:blocking_count"`
	ConflictCount             int    `gorm:"column:conflict_count"`
}

type planningSourceRevisionRow struct {
	Ordinal       int    `gorm:"column:ordinal"`
	SourceKind    string `gorm:"column:source_kind"`
	Purpose       string `gorm:"column:purpose"`
	Required      bool   `gorm:"column:required"`
	ArtifactID    string `gorm:"column:artifact_id"`
	RevisionID    string `gorm:"column:revision_id"`
	ContentRef    string `gorm:"column:content_ref"`
	ContentHash   string `gorm:"column:content_hash"`
	WorkflowState string `gorm:"column:workflow_status"`
}

type planningTemplateRow struct {
	Ordinal     int             `gorm:"column:ordinal"`
	Role        string          `gorm:"column:role"`
	ReleaseID   string          `gorm:"column:template_release_id"`
	ContentHash string          `gorm:"column:template_release_content_hash"`
	Manifest    json.RawMessage `gorm:"column:manifest"`
}

type planningObligationRow struct {
	ObligationID      string          `gorm:"column:obligation_id"`
	Level             string          `gorm:"column:level"`
	Kind              string          `gorm:"column:kind"`
	SourceArtifactID  string          `gorm:"column:source_artifact_id"`
	SourceRevisionID  string          `gorm:"column:source_revision_id"`
	SourceContentHash string          `gorm:"column:source_content_hash"`
	SourceAnchorID    string          `gorm:"column:source_anchor_id"`
	OracleIDs         json.RawMessage `gorm:"column:oracle_ids"`
	DependsOn         json.RawMessage `gorm:"column:depends_on"`
	Waivable          bool            `gorm:"column:waivable"`
	Status            string          `gorm:"column:status"`
	BlockingReasonID  *string         `gorm:"column:blocking_reason_id"`
}

func (source *PostgresPlanningSource) LoadPlanningFacts(
	ctx context.Context,
	projectID, sessionID, taskKey string,
) (PlanningFacts, error) {
	if ctx == nil || !validUUIDs(projectID, sessionID) {
		return PlanningFacts{}, fmt.Errorf("%w: project or SandboxSession identity", ErrInvalidTaskCapsule)
	}
	if normalized, err := normalizeStableValue(taskKey, 160); err != nil || normalized != taskKey {
		return PlanningFacts{}, fmt.Errorf("%w: task key", ErrInvalidTaskCapsule)
	}
	row, err := source.loadExactSession(ctx, projectID, sessionID)
	if err != nil {
		return PlanningFacts{}, err
	}
	record, err := source.candidates.LoadMutationCandidate(ctx, projectID, row.CandidateID)
	if err != nil {
		return PlanningFacts{}, fmt.Errorf("%w: load exact Candidate tree: %v", ErrPlanningBlocked, err)
	}
	if err := matchPlanningCandidate(row, record.Candidate); err != nil {
		return PlanningFacts{}, err
	}
	subject := repository.PathPolicySubject{
		ProjectID: projectID, RepositorySnapshotID: record.Candidate.RepositorySnapshotID,
		BuildManifest: record.Candidate.BuildManifest, BuildContract: record.Candidate.BuildContract,
		FullStackTemplate: record.Candidate.FullStackTemplate,
	}
	policy, err := source.policies.ResolvePathPolicy(ctx, subject)
	if err != nil {
		return PlanningFacts{}, fmt.Errorf("%w: resolve exact TemplateRelease path policy: %v", ErrPlanningBlocked, err)
	}
	if policy.Subject != subject || len(policy.ExtensionPaths) == 0 || len(policy.ProtectedPaths) == 0 {
		return PlanningFacts{}, fmt.Errorf("%w: path policy does not match the exact Candidate", ErrPlanningDrift)
	}

	contract, contractItem, err := source.loadBuildContract(ctx, row)
	if err != nil {
		return PlanningFacts{}, err
	}
	if err := source.validateObligationProjections(ctx, row, contract); err != nil {
		return PlanningFacts{}, err
	}
	obligationIDs, acceptanceIDs, commandIDs, dependencies, err := planningTask(contract, taskKey)
	if err != nil {
		return PlanningFacts{}, err
	}
	sourceItems, err := source.loadSourceItems(ctx, row, contract)
	if err != nil {
		return PlanningFacts{}, err
	}
	templateRefs, templateItems, err := source.loadTemplateItems(ctx, row, contract)
	if err != nil {
		return PlanningFacts{}, err
	}
	fileItems, err := source.loadRepositoryItems(ctx, row, record.Candidate.CurrentTree, policy)
	if err != nil {
		return PlanningFacts{}, err
	}
	items := make([]ContextItem, 0, 1+len(sourceItems)+len(templateItems)+len(fileItems))
	items = append(items, contractItem)
	items = append(items, sourceItems...)
	items = append(items, templateItems...)
	items = append(items, fileItems...)

	readSet := uniqueSorted(append(
		append([]string{}, policy.ExtensionPaths...), policy.ProtectedPaths...,
	))
	objective := fmt.Sprintf(
		"Implement task %s as one complete vertical slice for BuildContract %s. Close all %d bound Must obligations and all %d acceptance criteria using only the declared write-set and verification commands.",
		taskKey, row.BuildContractID, len(obligationIDs), len(acceptanceIDs),
	)
	preconditions := []string{
		"The exact SandboxSession is ready and matches the active Candidate tree.",
		"The exact BuildContract is ready with every Must obligation backed by an Oracle.",
		"The exact TemplateRelease path policy is unchanged.",
	}
	if len(dependencies) > 0 {
		preconditions = append(preconditions,
			"Every dependency task has an applied, non-undone Agent merge: "+strings.Join(dependencies, ", "),
		)
	}
	return PlanningFacts{
		ProjectID: row.ProjectID, SandboxSessionID: row.SessionID, CandidateID: row.CandidateID,
		CandidateVersion:          uint64(row.CandidateVersion),
		CandidateSessionEpoch:     uint64(row.CandidateSessionEpoch),
		CandidateWriterLeaseEpoch: uint64(row.CandidateWriterLeaseEpoch),
		BaseCandidateTreeHash:     row.CandidateTreeHash,
		BuildContract:             repository.ExactReference{ID: row.BuildContractID, ContentHash: row.BuildContractHash},
		TemplateReleases:          templateRefs, TaskKey: taskKey, Objective: objective,
		ObligationIDs: obligationIDs, AcceptanceCriterionIDs: acceptanceIDs,
		ReadSet: readSet, WriteSet: append([]string{}, policy.ExtensionPaths...),
		ProtectedPaths: append([]string{}, policy.ProtectedPaths...), ContextItems: items,
		Preconditions: preconditions,
		Postconditions: []string{
			"Every bound Must obligation has platform-collected verification evidence.",
			"The platform-captured Patch changes only declared write-set paths and no protected path.",
		},
		VerificationCommandIDs: commandIDs, AllowedTools: append([]string{}, source.config.AllowedTools...),
		NetworkPolicy: NetworkPolicy{Mode: "none", AllowedHosts: []string{}},
		Budgets:       source.config.Budgets, OutputSchemaHash: source.config.OutputSchemaHash,
	}, nil
}

func (source *PostgresPlanningSource) LoadTaskGraph(
	ctx context.Context,
	projectID, sessionID string,
) (TaskGraph, error) {
	if ctx == nil || !validUUIDs(projectID, sessionID) {
		return TaskGraph{}, fmt.Errorf("%w: project or SandboxSession identity", ErrTaskGraphBlocked)
	}
	row, err := source.loadExactSession(ctx, projectID, sessionID)
	if err != nil {
		return TaskGraph{}, err
	}
	contract, _, err := source.loadBuildContract(ctx, row)
	if err != nil {
		return TaskGraph{}, err
	}
	if err := source.validateObligationProjections(ctx, row, contract); err != nil {
		return TaskGraph{}, err
	}
	return buildTaskGraph(
		projectID,
		sessionID,
		repository.ExactReference{ID: row.BuildContractID, ContentHash: row.BuildContractHash},
		contract,
	)
}

func (source *PostgresPlanningSource) loadExactSession(
	ctx context.Context,
	projectID, sessionID string,
) (planningSessionRow, error) {
	var row planningSessionRow
	result := source.database.WithContext(ctx).Raw(`
SELECT
  session.project_id::text AS project_id,
  session.id::text AS session_id,
  session.state AS session_state,
  session.candidate_id::text AS candidate_id,
  session.candidate_version,
  session.candidate_session_epoch,
  session.candidate_writer_lease_epoch,
  session.candidate_tree_hash,
  session.build_manifest_id::text AS build_manifest_id,
  session.build_manifest_hash,
  session.build_contract_id::text AS build_contract_id,
  session.build_contract_hash,
  session.full_stack_template_id::text AS full_stack_template_id,
  session.full_stack_template_hash,
  contract.content_ref AS contract_content_ref,
  contract.content_hash AS contract_content_hash,
  contract.status AS contract_status,
  contract.must_count,
  contract.must_ready_count,
  contract.obligation_count,
  contract.source_count,
  contract.template_release_count,
  contract.blocking_count,
  contract.conflict_count
FROM sandbox_sessions AS session
JOIN candidate_workspaces AS candidate
  ON candidate.id = session.candidate_id
 AND candidate.project_id = session.project_id
JOIN application_build_contracts AS contract
  ON contract.id = session.build_contract_id
 AND contract.contract_hash = session.build_contract_hash
 AND contract.project_id = session.project_id
 AND contract.build_manifest_id = session.build_manifest_id
WHERE session.project_id = ? AND session.id = ?
  AND session.state = 'ready'
  AND candidate.status = 'active'
  AND NOT candidate.conflicted
  AND NOT candidate.stale
  AND NOT candidate.rebase_required
  AND candidate.version = session.candidate_version
  AND candidate.session_epoch = session.candidate_session_epoch
  AND candidate.writer_lease_epoch = session.candidate_writer_lease_epoch
  AND candidate.current_tree_hash = session.candidate_tree_hash
`, projectID, sessionID).Scan(&row)
	if result.Error != nil {
		return planningSessionRow{}, fmt.Errorf("%w: load exact ready SandboxSession: %v", ErrPlanningBlocked, result.Error)
	}
	if result.RowsAffected != 1 || row.SessionState != "ready" || row.CandidateVersion <= 0 ||
		row.CandidateSessionEpoch <= 0 || row.CandidateWriterLeaseEpoch < 0 ||
		row.ContractStatus != constructor.StatusReady || row.MustCount <= 0 ||
		row.MustReadyCount != row.MustCount || row.ObligationCount < row.MustCount ||
		row.SourceCount <= 0 || row.TemplateReleaseCount <= 0 ||
		row.BlockingCount != 0 || row.ConflictCount != 0 {
		return planningSessionRow{}, fmt.Errorf("%w: exact ready Session/Candidate/BuildContract tuple is unavailable", ErrPlanningBlocked)
	}
	return row, nil
}

func (source *PostgresPlanningSource) validateObligationProjections(
	ctx context.Context,
	row planningSessionRow,
	contract constructor.ContractContent,
) error {
	var rows []planningObligationRow
	result := source.database.WithContext(ctx).Raw(`
SELECT obligation_id, level, kind,
       source_artifact_id::text AS source_artifact_id,
       source_revision_id::text AS source_revision_id,
       source_content_hash, source_anchor_id, oracle_ids, depends_on,
       waivable, status, blocking_reason_id
FROM application_build_contract_obligations
WHERE contract_id = ?
ORDER BY obligation_id
`, row.BuildContractID).Scan(&rows)
	if result.Error != nil {
		return fmt.Errorf("%w: load BuildContract obligation projections: %v", ErrPlanningBlocked, result.Error)
	}
	expected := append([]constructor.Obligation(nil), contract.Obligations...)
	sort.Slice(expected, func(left, right int) bool { return expected[left].ID < expected[right].ID })
	if len(rows) != len(expected) || len(rows) != row.ObligationCount {
		return fmt.Errorf("%w: BuildContract obligation projection count drifted", ErrPlanningDrift)
	}
	for index, projection := range rows {
		var oracleIDs, dependsOn []string
		if err := decodeJSONColumn(projection.OracleIDs, &oracleIDs); err != nil {
			return fmt.Errorf("%w: decode obligation %s Oracle IDs", ErrPlanningDrift, projection.ObligationID)
		}
		if err := decodeJSONColumn(projection.DependsOn, &dependsOn); err != nil {
			return fmt.Errorf("%w: decode obligation %s dependencies", ErrPlanningDrift, projection.ObligationID)
		}
		obligation := expected[index]
		blockingReason := ""
		if projection.BlockingReasonID != nil {
			blockingReason = *projection.BlockingReasonID
		}
		if projection.ObligationID != obligation.ID || projection.Level != obligation.Level ||
			projection.Kind != obligation.Kind ||
			projection.SourceArtifactID != obligation.SourceRevision.ArtifactID ||
			projection.SourceRevisionID != obligation.SourceRevision.RevisionID ||
			projection.SourceContentHash != obligation.SourceRevision.ContentHash ||
			projection.SourceAnchorID != obligation.SourceAnchorID ||
			!equalJSON(oracleIDs, obligation.OracleIDs) || !equalJSON(dependsOn, obligation.DependsOn) ||
			projection.Waivable != obligation.Waivable || projection.Status != obligation.Status ||
			blockingReason != obligation.BlockingReasonID {
			return fmt.Errorf("%w: BuildContract obligation %s projection drifted", ErrPlanningDrift, obligation.ID)
		}
	}
	return nil
}

func (source *PostgresPlanningSource) loadBuildContract(
	ctx context.Context,
	row planningSessionRow,
) (constructor.ContractContent, ContextItem, error) {
	stored, err := source.contents.Get(ctx, row.ContractContentRef, row.ContractContentHash)
	if err != nil {
		return constructor.ContractContent{}, ContextItem{}, fmt.Errorf("%w: read exact BuildContract content: %v", ErrPlanningBlocked, err)
	}
	if stored.ProjectID != row.ProjectID || stored.AggregateType != "application_build_contract" ||
		stored.AggregateID != row.BuildContractID || stored.State != content.StateFinalized ||
		stored.ContentHash != row.ContractContentHash || stored.ByteSize != int64(len(stored.Payload)) ||
		stored.ByteSize < 1 || stored.ByteSize > 4<<20 {
		return constructor.ContractContent{}, ContextItem{}, fmt.Errorf("%w: BuildContract content identity, state, or size drifted", ErrPlanningDrift)
	}
	var contract constructor.ContractContent
	if err := decodeJSONColumn(stored.Payload, &contract); err != nil {
		return constructor.ContractContent{}, ContextItem{}, fmt.Errorf("%w: decode BuildContract: %v", ErrPlanningDrift, err)
	}
	hash, err := domain.CanonicalHash(contract)
	if err != nil || hash != strings.TrimPrefix(row.BuildContractHash, "sha256:") ||
		contract.Status != constructor.StatusReady || contract.ProjectID != row.ProjectID ||
		contract.BuildManifest.ID != row.BuildManifestID ||
		contract.BuildManifest.ContentHash != row.BuildManifestHash ||
		contract.FullStackTemplate.ID != row.FullStackTemplateID ||
		contract.FullStackTemplate.ContentHash != row.FullStackTemplateHash {
		return constructor.ContractContent{}, ContextItem{}, fmt.Errorf("%w: BuildContract semantic hash or exact lineage drifted", ErrPlanningDrift)
	}
	exact := repository.ExactReference{ID: row.BuildContractID, ContentHash: row.BuildContractHash}
	return contract, ContextItem{
		Key: "build-contract", Kind: ContextBuildContract, Source: &exact, Required: true,
		Content: BlobReference{
			Store: "content", OwnerID: stored.AggregateID, Ref: stored.ID,
			ContentHash: stored.ContentHash, ByteSize: stored.ByteSize,
		},
	}, nil
}

func (source *PostgresPlanningSource) loadSourceItems(
	ctx context.Context,
	row planningSessionRow,
	contract constructor.ContractContent,
) ([]ContextItem, error) {
	var rows []planningSourceRevisionRow
	result := source.database.WithContext(ctx).Raw(`
SELECT source.ordinal, source.source_kind, source.purpose, source.required,
       source.artifact_id::text AS artifact_id,
       source.revision_id::text AS revision_id,
       revision.content_ref, source.content_hash, revision.workflow_status
FROM application_build_contract_sources AS source
JOIN artifact_revisions AS revision
  ON revision.id = source.revision_id
 AND revision.artifact_id = source.artifact_id
 AND revision.content_hash = source.content_hash
WHERE source.contract_id = ?
ORDER BY source.ordinal
`, row.BuildContractID).Scan(&rows)
	if result.Error != nil {
		return nil, fmt.Errorf("%w: load BuildContract source projections: %v", ErrPlanningBlocked, result.Error)
	}
	if len(rows) != len(contract.SourceRevisions) || len(rows) == 0 {
		return nil, fmt.Errorf("%w: BuildContract source projection count drifted", ErrPlanningDrift)
	}
	items := make([]ContextItem, 0, len(rows))
	for index, sourceRow := range rows {
		expected := contract.SourceRevisions[index]
		if sourceRow.Ordinal != index || sourceRow.WorkflowState != "approved" ||
			expected.Kind != sourceRow.SourceKind || expected.Purpose != sourceRow.Purpose ||
			expected.Required != sourceRow.Required || expected.ArtifactID != sourceRow.ArtifactID ||
			expected.RevisionID != sourceRow.RevisionID || expected.ContentHash != sourceRow.ContentHash {
			return nil, fmt.Errorf("%w: BuildContract source projection %d drifted", ErrPlanningDrift, index)
		}
		stored, err := source.contents.Get(ctx, sourceRow.ContentRef, sourceRow.ContentHash)
		if err != nil {
			return nil, fmt.Errorf("%w: read source revision %s: %v", ErrPlanningBlocked, sourceRow.RevisionID, err)
		}
		if stored.ProjectID != row.ProjectID || stored.State != content.StateFinalized ||
			stored.ContentHash != sourceRow.ContentHash || stored.ByteSize != int64(len(stored.Payload)) ||
			stored.ByteSize < 1 || stored.ByteSize > 4<<20 || !validUUIDs(stored.AggregateID) {
			return nil, fmt.Errorf("%w: source revision %s content drifted", ErrPlanningDrift, sourceRow.RevisionID)
		}
		exact := repository.ExactReference{ID: sourceRow.RevisionID, ContentHash: sourceRow.ContentHash}
		items = append(items, ContextItem{
			Key:  "source:" + sourceRow.SourceKind + ":" + sourceRow.RevisionID,
			Kind: ContextSourceRevision, Source: &exact, Required: sourceRow.Required,
			Content: BlobReference{
				Store: "content", OwnerID: stored.AggregateID, Ref: stored.ID,
				ContentHash: stored.ContentHash, ByteSize: stored.ByteSize,
			},
		})
	}
	return items, nil
}

func (source *PostgresPlanningSource) loadTemplateItems(
	ctx context.Context,
	row planningSessionRow,
	contract constructor.ContractContent,
) ([]repository.ExactReference, []ContextItem, error) {
	var rows []planningTemplateRow
	result := source.database.WithContext(ctx).Raw(`
SELECT selected.ordinal, selected.role,
       selected.template_release_id::text AS template_release_id,
       selected.template_release_content_hash,
       release.manifest
FROM sandbox_session_template_releases AS selected
JOIN template_releases AS release
  ON release.id = selected.template_release_id
 AND release.content_hash = selected.template_release_content_hash
WHERE selected.session_id = ?
ORDER BY selected.ordinal
`, row.SessionID).Scan(&rows)
	if result.Error != nil {
		return nil, nil, fmt.Errorf("%w: load exact TemplateRelease set: %v", ErrPlanningBlocked, result.Error)
	}
	if len(rows) == 0 || len(rows) != len(contract.TemplateReleaseRefs) {
		return nil, nil, fmt.Errorf("%w: TemplateRelease projection count drifted", ErrPlanningDrift)
	}
	contractReleases := make(map[string]constructor.TemplateReleaseRef, len(contract.TemplateReleaseRefs))
	for _, ref := range contract.TemplateReleaseRefs {
		contractReleases[ref.ID] = ref
	}
	refs := make([]repository.ExactReference, 0, len(rows))
	items := make([]ContextItem, 0, len(rows))
	for _, template := range rows {
		expected, exists := contractReleases[template.ReleaseID]
		if !exists || expected.ReleaseHash != template.ContentHash || expected.Role != template.Role {
			return nil, nil, fmt.Errorf("%w: TemplateRelease %s drifted", ErrPlanningDrift, template.ReleaseID)
		}
		canonical, err := domain.CanonicalJSON(template.Manifest)
		if err != nil || len(canonical) == 0 || len(canonical) > 4<<20 {
			return nil, nil, fmt.Errorf("%w: TemplateRelease %s manifest is unavailable or too large", ErrPlanningBlocked, template.ReleaseID)
		}
		exact := repository.ExactReference{ID: template.ReleaseID, ContentHash: template.ContentHash}
		refs = append(refs, exact)
		items = append(items, ContextItem{
			Key:  "template:" + template.Role + ":" + template.ReleaseID,
			Kind: ContextTemplateRule, Source: &exact, Required: true,
			Content: BlobReference{
				Store: "template_registry", OwnerID: template.ReleaseID,
				Ref:         "template-release:" + template.ReleaseID,
				ContentHash: template.ContentHash, ByteSize: int64(len(canonical)),
			},
		})
	}
	sort.Slice(refs, func(left, right int) bool { return refs[left].ID < refs[right].ID })
	return refs, items, nil
}

func (source *PostgresPlanningSource) loadRepositoryItems(
	ctx context.Context,
	row planningSessionRow,
	tree repository.TreeManifest,
	policy repository.PathPolicy,
) ([]ContextItem, error) {
	items := []ContextItem{}
	for _, file := range tree.Files {
		if !pathInAnySet(file.Path, policy.ExtensionPaths) && !pathInAnySet(file.Path, policy.ProtectedPaths) {
			continue
		}
		if len(items) >= source.config.MaxContextFiles {
			return nil, fmt.Errorf("%w: relevant repository files exceed the bounded ContextPack limit", ErrPlanningBlocked)
		}
		pointer, value, err := source.files.Resolve(ctx, row.ProjectID, file.ContentHash, file.ByteSize)
		if err != nil {
			return nil, fmt.Errorf("%w: resolve repository file %s: %v", ErrPlanningBlocked, file.Path, err)
		}
		if pointer.Store != repository.FileContentStore || pointer.ContentHash != file.ContentHash ||
			pointer.ByteSize != file.ByteSize || int64(len(value)) != file.ByteSize ||
			pointer.Ref == "" || !validUUIDs(pointer.OwnerID) {
			return nil, fmt.Errorf("%w: repository file %s content drifted", ErrPlanningDrift, file.Path)
		}
		pathKey, err := semanticHash(file.Path)
		if err != nil {
			return nil, fmt.Errorf("%w: hash repository path %s: %v", ErrPlanningBlocked, file.Path, err)
		}
		items = append(items, ContextItem{
			Key: "repository-file:" + pathKey, Kind: ContextRepositoryFile,
			Path: file.Path, Required: true,
			Content: BlobReference{
				Store: "repository_file", OwnerID: pointer.OwnerID, Ref: pointer.Ref,
				ContentHash: pointer.ContentHash, ByteSize: pointer.ByteSize,
			},
		})
	}
	return items, nil
}

func planningObligations(
	contract constructor.ContractContent,
) ([]string, []string, []string, error) {
	criteria := make(map[string]constructor.AcceptanceCriterion, len(contract.AcceptanceCriteria))
	for _, criterion := range contract.AcceptanceCriteria {
		criteria[criterion.ID] = criterion
	}
	oracles := make(map[string]constructor.Oracle, len(contract.Oracles))
	for _, oracle := range contract.Oracles {
		oracles[oracle.ID] = oracle
	}
	obligationIDs := []string{}
	acceptanceSet := map[string]bool{}
	commandSet := map[string]bool{}
	for _, obligation := range contract.Obligations {
		if obligation.Level != "must" {
			continue
		}
		if obligation.Status != constructor.StatusReady || len(obligation.OracleIDs) == 0 {
			return nil, nil, nil, fmt.Errorf("%w: Must obligation %s is not ready", ErrPlanningBlocked, obligation.ID)
		}
		obligationIDs = append(obligationIDs, obligation.ID)
		for _, oracleID := range obligation.OracleIDs {
			oracle, exists := oracles[oracleID]
			if !exists || len(oracle.AcceptanceCriterionIDs) == 0 {
				return nil, nil, nil, fmt.Errorf("%w: Oracle %s is missing", ErrPlanningDrift, oracleID)
			}
			commandID := oracle.CommandID
			if commandID == "" {
				commandID = "oracle:" + oracle.ID
			}
			commandSet[commandID] = true
			for _, acceptanceID := range oracle.AcceptanceCriterionIDs {
				if _, exists := criteria[acceptanceID]; !exists {
					return nil, nil, nil, fmt.Errorf("%w: acceptance criterion %s is missing", ErrPlanningDrift, acceptanceID)
				}
				acceptanceSet[acceptanceID] = true
			}
		}
	}
	if len(obligationIDs) == 0 || len(acceptanceSet) == 0 || len(commandSet) == 0 {
		return nil, nil, nil, fmt.Errorf("%w: no executable Must obligation graph", ErrPlanningBlocked)
	}
	sort.Strings(obligationIDs)
	return obligationIDs, sortedSet(acceptanceSet), sortedSet(commandSet), nil
}

func planningTask(
	contract constructor.ContractContent,
	taskKey string,
) ([]string, []string, []string, []string, error) {
	if !strings.HasPrefix(taskKey, TaskKeyPrefix) {
		obligations, criteria, commands, err := planningObligations(contract)
		return obligations, criteria, commands, []string{}, err
	}
	tasks, err := buildTaskGraphTasks(contract)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for _, task := range tasks {
		if task.Key == taskKey {
			return append([]string(nil), task.ObligationIDs...),
				append([]string(nil), task.AcceptanceCriterionIDs...),
				append([]string(nil), task.VerificationCommandIDs...),
				append([]string(nil), task.DependsOn...), nil
		}
	}
	return nil, nil, nil, nil, fmt.Errorf("%w: task key %s is outside the exact BuildContract graph", ErrTaskGraphBlocked, taskKey)
}

func matchPlanningCandidate(row planningSessionRow, candidate repository.CandidateWorkspace) error {
	if candidate.ProjectID != row.ProjectID || candidate.ID != row.CandidateID ||
		candidate.Status != repository.CandidateActive || candidate.Conflicted || candidate.Stale || candidate.RebaseRequired ||
		candidate.Version != uint64(row.CandidateVersion) ||
		candidate.SessionEpoch != uint64(row.CandidateSessionEpoch) ||
		candidate.WriterLeaseEpoch != uint64(row.CandidateWriterLeaseEpoch) ||
		candidate.CurrentTree.TreeHash != row.CandidateTreeHash ||
		candidate.BuildManifest.ID != row.BuildManifestID || candidate.BuildManifest.ContentHash != row.BuildManifestHash ||
		candidate.BuildContract.ID != row.BuildContractID || candidate.BuildContract.ContentHash != row.BuildContractHash ||
		candidate.FullStackTemplate.ID != row.FullStackTemplateID ||
		candidate.FullStackTemplate.ContentHash != row.FullStackTemplateHash {
		return fmt.Errorf("%w: hydrated Candidate differs from the ready Session projection", ErrPlanningDrift)
	}
	return nil
}

func pathInAnySet(value string, roots []string) bool {
	for _, root := range roots {
		if pathContains(root, value) {
			return true
		}
	}
	return false
}

func uniqueSorted(values []string) []string {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return sortedSet(set)
}

func sortedSet(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

var _ PlanningSource = (*PostgresPlanningSource)(nil)
var _ TaskGraphPlanningSource = (*PostgresPlanningSource)(nil)
