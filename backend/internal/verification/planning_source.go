package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/worksflow/builder/backend/internal/constructor"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"github.com/worksflow/builder/backend/internal/templates"
	"gorm.io/gorm"
)

type PlanningContentReader interface {
	Get(context.Context, string, string) (content.StoredContent, error)
}

type PostgresCandidatePlanSource struct {
	database *gorm.DB
	contents PlanningContentReader
}

func NewPostgresCandidatePlanSource(
	database *gorm.DB,
	contents PlanningContentReader,
) (*PostgresCandidatePlanSource, error) {
	if database == nil || contents == nil {
		return nil, errors.New("verification planning database and content reader are required")
	}
	return &PostgresCandidatePlanSource{database: database, contents: contents}, nil
}

type candidatePlanSourceRow struct {
	ProjectID                  string          `gorm:"column:project_id"`
	SessionID                  string          `gorm:"column:session_id"`
	SessionVersion             int64           `gorm:"column:session_version"`
	SessionEpoch               int64           `gorm:"column:session_epoch"`
	CandidateID                string          `gorm:"column:candidate_id"`
	CandidateSnapshotID        string          `gorm:"column:candidate_snapshot_id"`
	CandidateVersion           int64           `gorm:"column:candidate_version"`
	JournalSequence            int64           `gorm:"column:journal_sequence"`
	WriterLeaseEpoch           int64           `gorm:"column:writer_lease_epoch"`
	TreeStore                  string          `gorm:"column:tree_store"`
	TreeOwnerID                string          `gorm:"column:tree_owner_id"`
	TreeRef                    string          `gorm:"column:tree_ref"`
	TreeContentHash            string          `gorm:"column:tree_content_hash"`
	TreeHash                   string          `gorm:"column:tree_hash"`
	BuildManifestID            string          `gorm:"column:build_manifest_id"`
	BuildManifestHash          string          `gorm:"column:build_manifest_hash"`
	BuildContractID            string          `gorm:"column:build_contract_id"`
	BuildContractHash          string          `gorm:"column:build_contract_hash"`
	ContractContentRef         string          `gorm:"column:contract_content_ref"`
	ContractContentHash        string          `gorm:"column:contract_content_hash"`
	ContractCompilerVersion    string          `gorm:"column:contract_compiler_version"`
	ContractCompilerHash       string          `gorm:"column:contract_compiler_hash"`
	ContractMustCount          int             `gorm:"column:contract_must_count"`
	ContractObligationCount    int             `gorm:"column:contract_obligation_count"`
	ContractTemplateCount      int             `gorm:"column:contract_template_count"`
	FullStackTemplateID        string          `gorm:"column:full_stack_template_id"`
	FullStackTemplateHash      string          `gorm:"column:full_stack_template_hash"`
	VerificationProfileID      string          `gorm:"column:verification_profile_id"`
	VerificationProfileVersion int64           `gorm:"column:verification_profile_version"`
	VerificationProfileHash    string          `gorm:"column:verification_profile_hash"`
	VerificationProfile        json.RawMessage `gorm:"column:verification_profile"`
}

type candidatePlanTemplateRow struct {
	Ordinal     int             `gorm:"column:ordinal"`
	Role        string          `gorm:"column:role"`
	MountPath   string          `gorm:"column:mount_path"`
	ReleaseID   string          `gorm:"column:template_release_id"`
	ContentHash string          `gorm:"column:template_release_content_hash"`
	SubjectHash string          `gorm:"column:subject_hash"`
	Manifest    json.RawMessage `gorm:"column:manifest"`
}

type candidatePlanObligationRow struct {
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

func (source *PostgresCandidatePlanSource) LoadCandidatePlan(
	ctx context.Context,
	request CandidatePlanRequest,
) (CompileCandidatePlanInput, error) {
	if !validCandidatePlanRequest(request) {
		return CompileCandidatePlanInput{}, runInvalid("Candidate VerificationPlan request")
	}
	row, err := source.loadExactCandidateSubject(ctx, request)
	if err != nil {
		return CompileCandidatePlanInput{}, err
	}
	contract, err := source.loadExactBuildContract(ctx, row)
	if err != nil {
		return CompileCandidatePlanInput{}, err
	}
	if err := source.validateObligationProjections(ctx, row, contract); err != nil {
		return CompileCandidatePlanInput{}, err
	}
	releases, err := source.loadExactTemplateReleases(ctx, row, contract)
	if err != nil {
		return CompileCandidatePlanInput{}, err
	}
	profile, err := decodeVerificationProfile(row)
	if err != nil {
		return CompileCandidatePlanInput{}, err
	}
	oracles, obligations, err := verificationRequirements(contract)
	if err != nil {
		return CompileCandidatePlanInput{}, err
	}
	return CompileCandidatePlanInput{
		ProjectID: row.ProjectID,
		Subject: CandidatePlanSubject{
			SessionID: row.SessionID, SessionVersion: uint64(row.SessionVersion),
			CandidateID: row.CandidateID, CandidateSnapshotID: row.CandidateSnapshotID,
			CandidateVersion: uint64(row.CandidateVersion), JournalSequence: uint64(row.JournalSequence),
			SessionEpoch: uint64(row.SessionEpoch), WriterLeaseEpoch: uint64(row.WriterLeaseEpoch),
			TreeStore: row.TreeStore, TreeOwnerID: row.TreeOwnerID, TreeRef: row.TreeRef,
			TreeContentHash: row.TreeContentHash, TreeHash: row.TreeHash,
		},
		BuildManifest: repository.ExactReference{ID: row.BuildManifestID, ContentHash: row.BuildManifestHash},
		BuildContract: repository.ExactReference{ID: row.BuildContractID, ContentHash: row.BuildContractHash},
		FullStackTemplate: repository.ExactReference{
			ID: row.FullStackTemplateID, ContentHash: row.FullStackTemplateHash,
		},
		Profile: profile, TemplateReleases: releases, Oracles: oracles, Obligations: obligations,
	}, nil
}

func (source *PostgresCandidatePlanSource) loadExactCandidateSubject(
	ctx context.Context,
	request CandidatePlanRequest,
) (candidatePlanSourceRow, error) {
	var row candidatePlanSourceRow
	result := source.database.WithContext(ctx).Raw(`
SELECT
  session.project_id::text AS project_id,
  session.id::text AS session_id,
  session.version AS session_version,
  session.session_epoch,
  session.candidate_id::text AS candidate_id,
  session.latest_checkpoint_id::text AS candidate_snapshot_id,
  session.candidate_version,
  session.candidate_journal_sequence AS journal_sequence,
  session.candidate_writer_lease_epoch AS writer_lease_epoch,
  session.candidate_tree_store AS tree_store,
  session.candidate_tree_owner_id::text AS tree_owner_id,
  session.candidate_tree_ref AS tree_ref,
  session.candidate_tree_content_hash AS tree_content_hash,
  session.candidate_tree_hash AS tree_hash,
  session.build_manifest_id::text AS build_manifest_id,
  verification_normalize_sha256(session.build_manifest_hash) AS build_manifest_hash,
  session.build_contract_id::text AS build_contract_id,
  verification_normalize_sha256(session.build_contract_hash) AS build_contract_hash,
  contract.content_ref AS contract_content_ref,
  contract.content_hash AS contract_content_hash,
  contract.compiler_version AS contract_compiler_version,
  verification_normalize_sha256(contract.compiler_hash) AS contract_compiler_hash,
  contract.must_count AS contract_must_count,
  contract.obligation_count AS contract_obligation_count,
  contract.template_release_count AS contract_template_count,
  session.full_stack_template_id::text AS full_stack_template_id,
  session.full_stack_template_hash,
  profile.profile_id AS verification_profile_id,
  profile.version AS verification_profile_version,
  profile.content_hash AS verification_profile_hash,
  profile.document AS verification_profile
FROM sandbox_sessions AS session
JOIN candidate_workspaces AS candidate
  ON candidate.id = session.candidate_id AND candidate.project_id = session.project_id
JOIN candidate_snapshots AS snapshot
  ON snapshot.id = session.latest_checkpoint_id
 AND snapshot.project_id = session.project_id AND snapshot.candidate_id = session.candidate_id
JOIN application_build_manifests AS manifest
  ON manifest.id = session.build_manifest_id AND manifest.project_id = session.project_id
JOIN application_build_contracts AS contract
  ON contract.id = session.build_contract_id AND contract.project_id = session.project_id
JOIN full_stack_template_releases AS full_stack
  ON full_stack.id = session.full_stack_template_id
 AND full_stack.content_hash = session.full_stack_template_hash
JOIN verification_profile_versions AS profile
  ON profile.profile_id = ? AND profile.version = ? AND profile.content_hash = ?
JOIN verification_profile_policies AS profile_policy
  ON profile_policy.profile_id = profile.profile_id
 AND profile_policy.profile_version = profile.version
 AND profile_policy.profile_hash = profile.content_hash
 AND profile_policy.state = 'active'
WHERE session.project_id = ? AND session.id = ? AND session.actor_id = ?
  AND session.candidate_id = ? AND session.latest_checkpoint_id = ?
  AND session.version = ? AND session.session_epoch = ?
  AND session.candidate_version = ? AND session.candidate_writer_lease_epoch = ?
  AND session.candidate_session_epoch = session.session_epoch
  AND session.state = 'ready'
  AND candidate.status = 'active'
  AND NOT candidate.conflicted AND NOT candidate.stale AND NOT candidate.rebase_required
  AND candidate.version = session.candidate_version
  AND candidate.journal_sequence = session.candidate_journal_sequence
  AND candidate.session_epoch = session.session_epoch
  AND candidate.writer_lease_epoch = session.candidate_writer_lease_epoch
  AND candidate.current_tree_store = session.candidate_tree_store
  AND candidate.current_tree_owner_id = session.candidate_tree_owner_id
  AND candidate.current_tree_ref = session.candidate_tree_ref
  AND candidate.current_tree_content_hash = session.candidate_tree_content_hash
  AND candidate.current_tree_hash = session.candidate_tree_hash
  AND candidate.build_manifest_id = session.build_manifest_id
  AND verification_normalize_sha256(candidate.build_manifest_hash) = verification_normalize_sha256(session.build_manifest_hash)
  AND candidate.build_contract_id = session.build_contract_id
  AND verification_normalize_sha256(candidate.build_contract_hash) = verification_normalize_sha256(session.build_contract_hash)
  AND candidate.full_stack_template_id = session.full_stack_template_id
  AND candidate.full_stack_template_hash = session.full_stack_template_hash
  AND snapshot.candidate_version = session.candidate_version
  AND snapshot.journal_sequence = session.candidate_journal_sequence
  AND snapshot.session_epoch = session.session_epoch
  AND snapshot.writer_lease_epoch = session.candidate_writer_lease_epoch
  AND snapshot.tree_store = session.candidate_tree_store
  AND snapshot.tree_owner_id = session.candidate_tree_owner_id
  AND snapshot.tree_ref = session.candidate_tree_ref
  AND snapshot.tree_content_hash = session.candidate_tree_content_hash
  AND snapshot.tree_hash = session.candidate_tree_hash
  AND manifest.status = 'frozen'
  AND verification_normalize_sha256(manifest.manifest_hash) = verification_normalize_sha256(session.build_manifest_hash)
  AND contract.build_manifest_id = session.build_manifest_id
  AND verification_normalize_sha256(contract.build_manifest_hash) = verification_normalize_sha256(session.build_manifest_hash)
  AND verification_normalize_sha256(contract.contract_hash) = verification_normalize_sha256(session.build_contract_hash)
  AND contract.full_stack_template_id = session.full_stack_template_id
  AND contract.full_stack_template_hash = session.full_stack_template_hash
  AND contract.status = 'ready' AND contract.must_count > 0
  AND contract.must_ready_count = contract.must_count
  AND contract.obligation_count >= contract.must_count
  AND contract.source_count > 0 AND contract.template_release_count >= 2
  AND contract.blocking_count = 0 AND contract.conflict_count = 0
`, request.Profile.ID, int64(request.Profile.Version), request.Profile.ContentHash,
		request.ProjectID, request.SessionID, request.ActorID, request.CandidateID, request.CheckpointID,
		int64(request.ExpectedSessionVersion), int64(request.ExpectedSessionEpoch),
		int64(request.ExpectedCandidateVersion), int64(request.ExpectedWriterLeaseEpoch)).Scan(&row)
	if result.Error != nil {
		return candidatePlanSourceRow{}, fmt.Errorf("%w: load exact Candidate checkpoint: %v", ErrCandidatePlanningBlocked, result.Error)
	}
	if result.RowsAffected != 1 || !validCandidatePlanSourceRow(row, request) {
		return candidatePlanSourceRow{}, fmt.Errorf("%w: exact ready Candidate checkpoint, lineage, or active profile is unavailable", ErrCandidatePlanningBlocked)
	}
	return row, nil
}

func (source *PostgresCandidatePlanSource) loadExactBuildContract(
	ctx context.Context,
	row candidatePlanSourceRow,
) (constructor.ContractContent, error) {
	stored, err := source.contents.Get(ctx, row.ContractContentRef, row.ContractContentHash)
	if err != nil {
		return constructor.ContractContent{}, fmt.Errorf("%w: read exact BuildContract content: %v", ErrCandidatePlanningBlocked, err)
	}
	if stored.ProjectID != row.ProjectID || stored.AggregateType != "application_build_contract" ||
		stored.AggregateID != row.BuildContractID || stored.State != content.StateFinalized ||
		stored.ContentHash != row.ContractContentHash || stored.ByteSize != int64(len(stored.Payload)) ||
		stored.ByteSize < 1 || stored.ByteSize > 4<<20 {
		return constructor.ContractContent{}, fmt.Errorf("%w: BuildContract content identity or state drifted", ErrCandidatePlanningDrift)
	}
	var contract constructor.ContractContent
	if err := decodePlanningJSON(stored.Payload, &contract); err != nil {
		return constructor.ContractContent{}, fmt.Errorf("%w: decode BuildContract: %v", ErrCandidatePlanningDrift, err)
	}
	mustCount := 0
	for _, obligation := range contract.Obligations {
		if obligation.Level == "must" {
			mustCount++
		}
	}
	hash, err := domain.CanonicalHash(contract)
	if err != nil || "sha256:"+hash != row.BuildContractHash || contract.ProjectID != row.ProjectID ||
		contract.SchemaVersion != constructor.BuildContractSchemaVersion || contract.DeliverySliceID == "" ||
		contract.Compiler.Version != row.ContractCompilerVersion ||
		normalizePlanningSHA(contract.Compiler.Hash) != row.ContractCompilerHash ||
		contract.Status != constructor.StatusReady || mustCount != row.ContractMustCount ||
		contract.BuildManifest.ID != row.BuildManifestID ||
		normalizePlanningSHA(contract.BuildManifest.ContentHash) != row.BuildManifestHash ||
		contract.FullStackTemplate.ID != row.FullStackTemplateID ||
		contract.FullStackTemplate.ContentHash != row.FullStackTemplateHash ||
		len(contract.Obligations) != row.ContractObligationCount ||
		len(contract.TemplateReleaseRefs) != row.ContractTemplateCount {
		return constructor.ContractContent{}, fmt.Errorf("%w: BuildContract semantic hash, lineage, or count drifted", ErrCandidatePlanningDrift)
	}
	return contract, nil
}

func (source *PostgresCandidatePlanSource) validateObligationProjections(
	ctx context.Context,
	row candidatePlanSourceRow,
	contract constructor.ContractContent,
) error {
	var rows []candidatePlanObligationRow
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
		return fmt.Errorf("%w: load BuildContract obligation projections: %v", ErrCandidatePlanningBlocked, result.Error)
	}
	expected := append([]constructor.Obligation{}, contract.Obligations...)
	sort.Slice(expected, func(left, right int) bool { return expected[left].ID < expected[right].ID })
	if len(rows) != len(expected) || len(rows) != row.ContractObligationCount {
		return fmt.Errorf("%w: BuildContract obligation projection count drifted", ErrCandidatePlanningDrift)
	}
	for index, projection := range rows {
		var oracleIDs, dependsOn []string
		if decodePlanningJSON(projection.OracleIDs, &oracleIDs) != nil ||
			decodePlanningJSON(projection.DependsOn, &dependsOn) != nil {
			return fmt.Errorf("%w: decode BuildContract obligation projection", ErrCandidatePlanningDrift)
		}
		obligation := expected[index]
		blockingReason := ""
		if projection.BlockingReasonID != nil {
			blockingReason = *projection.BlockingReasonID
		}
		if projection.ObligationID != obligation.ID || projection.Level != obligation.Level ||
			projection.Kind != obligation.Kind || projection.SourceArtifactID != obligation.SourceRevision.ArtifactID ||
			projection.SourceRevisionID != obligation.SourceRevision.RevisionID ||
			normalizePlanningSHA(projection.SourceContentHash) != normalizePlanningSHA(obligation.SourceRevision.ContentHash) ||
			projection.SourceAnchorID != obligation.SourceAnchorID || !equalStrings(oracleIDs, obligation.OracleIDs) ||
			!equalStrings(dependsOn, obligation.DependsOn) || projection.Waivable != obligation.Waivable ||
			projection.Status != obligation.Status || blockingReason != obligation.BlockingReasonID {
			return fmt.Errorf("%w: BuildContract obligation %s projection drifted", ErrCandidatePlanningDrift, obligation.ID)
		}
	}
	return nil
}

func (source *PostgresCandidatePlanSource) loadExactTemplateReleases(
	ctx context.Context,
	row candidatePlanSourceRow,
	contract constructor.ContractContent,
) ([]ResolvedTemplateRelease, error) {
	var rows []candidatePlanTemplateRow
	result := source.database.WithContext(ctx).Raw(`
SELECT selected.ordinal, selected.role, component.mount_path,
       selected.template_release_id::text AS template_release_id,
       selected.template_release_content_hash,
       release.subject_hash, release.manifest
FROM sandbox_session_template_releases AS selected
JOIN full_stack_template_components AS component
  ON component.full_stack_template_id = ?
 AND component.full_stack_content_hash = ?
 AND component.role = selected.role
 AND component.template_release_id = selected.template_release_id
 AND component.template_release_content_hash = selected.template_release_content_hash
JOIN template_releases AS release
  ON release.id = selected.template_release_id
 AND release.content_hash = selected.template_release_content_hash
JOIN template_release_policies AS policy
  ON policy.template_release_id = release.id
 AND policy.release_content_hash = release.content_hash
 AND policy.state = 'approved'
WHERE selected.session_id = ?
ORDER BY selected.ordinal
`, row.FullStackTemplateID, row.FullStackTemplateHash, row.SessionID).Scan(&rows)
	if result.Error != nil {
		return nil, fmt.Errorf("%w: load exact approved TemplateReleases: %v", ErrCandidatePlanningBlocked, result.Error)
	}
	if len(rows) != len(contract.TemplateReleaseRefs) || len(rows) != row.ContractTemplateCount {
		return nil, fmt.Errorf("%w: TemplateRelease projection count drifted", ErrCandidatePlanningDrift)
	}
	expected := make(map[string]constructor.TemplateReleaseRef, len(contract.TemplateReleaseRefs))
	for _, release := range contract.TemplateReleaseRefs {
		expected[release.ID] = release
	}
	resolved := make([]ResolvedTemplateRelease, 0, len(rows))
	for _, row := range rows {
		contractRelease, exists := expected[row.ReleaseID]
		if !exists || contractRelease.Role != row.Role ||
			normalizePlanningSHA(contractRelease.ReleaseHash) != row.ContentHash ||
			contractRelease.Certification != "approved" || contractRelease.PolicyStatus != "active" {
			return nil, fmt.Errorf("%w: TemplateRelease %s drifted", ErrCandidatePlanningDrift, row.ReleaseID)
		}
		var manifest templates.TemplateManifest
		if err := decodePlanningJSON(row.Manifest, &manifest); err != nil {
			return nil, fmt.Errorf("%w: decode TemplateRelease %s manifest: %v", ErrCandidatePlanningDrift, row.ReleaseID, err)
		}
		resolved = append(resolved, ResolvedTemplateRelease{
			Role: row.Role, MountPath: row.MountPath,
			Release:     repository.ExactReference{ID: row.ReleaseID, ContentHash: row.ContentHash},
			SubjectHash: row.SubjectHash, Manifest: manifest,
		})
	}
	return resolved, nil
}

func decodeVerificationProfile(row candidatePlanSourceRow) (VerificationProfileDocument, error) {
	var profile VerificationProfileDocument
	if err := decodePlanningJSON(row.VerificationProfile, &profile); err != nil {
		return VerificationProfileDocument{}, fmt.Errorf("%w: decode VerificationProfile: %v", ErrCandidatePlanningDrift, err)
	}
	if profile.ID != row.VerificationProfileID || profile.Version != uint64(row.VerificationProfileVersion) ||
		profile.ProfileHash != row.VerificationProfileHash {
		return VerificationProfileDocument{}, fmt.Errorf("%w: VerificationProfile document projection drifted", ErrCandidatePlanningDrift)
	}
	return profile, nil
}

func verificationRequirements(
	contract constructor.ContractContent,
) ([]PlanOracle, []PlanObligation, error) {
	criteria := make(map[string]bool, len(contract.AcceptanceCriteria))
	for _, criterion := range contract.AcceptanceCriteria {
		criteria[criterion.ID] = true
	}
	oracles := make([]PlanOracle, 0, len(contract.Oracles))
	for _, oracle := range contract.Oracles {
		if len(oracle.AcceptanceCriterionIDs) == 0 {
			return nil, nil, fmt.Errorf("%w: Oracle %s has no acceptance criterion", ErrCandidatePlanningDrift, oracle.ID)
		}
		for _, acceptanceID := range oracle.AcceptanceCriterionIDs {
			if !criteria[acceptanceID] {
				return nil, nil, fmt.Errorf("%w: Oracle %s references unknown acceptance criterion %s", ErrCandidatePlanningDrift, oracle.ID, acceptanceID)
			}
		}
		oracles = append(oracles, PlanOracle{
			ID: oracle.ID, Kind: oracle.Kind, Target: oracle.Target, CommandID: oracle.CommandID,
			AcceptanceCriterionIDs: append([]string{}, oracle.AcceptanceCriterionIDs...),
		})
	}
	obligations := make([]PlanObligation, 0, len(contract.Obligations))
	for _, obligation := range contract.Obligations {
		if obligation.Level == "must" && obligation.Status != constructor.StatusReady {
			return nil, nil, fmt.Errorf("%w: Must obligation %s is not ready", ErrCandidatePlanningBlocked, obligation.ID)
		}
		obligations = append(obligations, PlanObligation{
			ID: obligation.ID, Level: obligation.Level, Status: obligation.Status,
			OracleIDs: append([]string{}, obligation.OracleIDs...),
		})
	}
	return oracles, obligations, nil
}

func validCandidatePlanRequest(request CandidatePlanRequest) bool {
	return validUUIDs(request.ProjectID, request.SessionID, request.CandidateID, request.CheckpointID, request.ActorID) &&
		request.ExpectedSessionVersion > 0 && request.ExpectedSessionEpoch > 0 &&
		request.ExpectedCandidateVersion > 0 && request.ExpectedWriterLeaseEpoch > 0 &&
		stableIDPattern.MatchString(request.Profile.ID) && request.Profile.Version > 0 &&
		exactSHA256(request.Profile.ContentHash)
}

func validCandidatePlanSourceRow(row candidatePlanSourceRow, request CandidatePlanRequest) bool {
	return row.ProjectID == request.ProjectID && row.SessionID == request.SessionID &&
		row.CandidateID == request.CandidateID && row.CandidateSnapshotID == request.CheckpointID &&
		row.SessionVersion == int64(request.ExpectedSessionVersion) &&
		row.SessionEpoch == int64(request.ExpectedSessionEpoch) &&
		row.CandidateVersion == int64(request.ExpectedCandidateVersion) &&
		row.WriterLeaseEpoch == int64(request.ExpectedWriterLeaseEpoch) &&
		row.VerificationProfileID == request.Profile.ID &&
		row.VerificationProfileVersion == int64(request.Profile.Version) &&
		row.VerificationProfileHash == request.Profile.ContentHash &&
		validUUIDs(row.ProjectID, row.SessionID, row.CandidateID, row.CandidateSnapshotID,
			row.TreeOwnerID, row.BuildManifestID, row.BuildContractID, row.FullStackTemplateID) &&
		row.JournalSequence >= 0 && row.ContractMustCount > 0 &&
		row.ContractObligationCount >= row.ContractMustCount && row.ContractTemplateCount >= 2 &&
		exactSHA256(row.TreeContentHash) && exactSHA256(row.TreeHash) &&
		exactSHA256(row.BuildManifestHash) && exactSHA256(row.BuildContractHash) &&
		exactSHA256(row.FullStackTemplateHash) && row.ContractContentRef != "" &&
		row.ContractCompilerVersion != "" && exactSHA256(row.ContractCompilerHash) &&
		exactSHA256(row.ContractContentHash)
}

func normalizePlanningSHA(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 64 {
		value = "sha256:" + value
	}
	return value
}

func decodePlanningJSON(value json.RawMessage, target any) error {
	if len(value) == 0 || string(value) == "null" {
		return errors.New("JSON value is absent")
	}
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON value contains trailing data")
		}
		return err
	}
	return nil
}

var _ CandidatePlanSource = (*PostgresCandidatePlanSource)(nil)
