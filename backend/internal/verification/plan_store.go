package verification

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"gorm.io/gorm"
)

var (
	ErrPlanNotFound = errors.New("candidate verification plan was not found")
	ErrPlanConflict = errors.New("candidate verification plan conflicts with committed content")
)

const planAggregateType = "candidate_verification_plan"

type Plan struct {
	ID        string      `json:"id"`
	Content   PlanContent `json:"content"`
	PlanHash  string      `json:"planHash"`
	CreatedBy string      `json:"createdBy"`
	CreatedAt time.Time   `json:"createdAt"`
	Replayed  bool        `json:"replayed"`
}

type planRow struct {
	ID                         string          `gorm:"column:id"`
	SchemaVersion              string          `gorm:"column:schema_version"`
	Scope                      string          `gorm:"column:scope"`
	ProjectID                  string          `gorm:"column:project_id"`
	SandboxSessionID           string          `gorm:"column:sandbox_session_id"`
	SessionVersion             int64           `gorm:"column:session_version"`
	CandidateID                string          `gorm:"column:candidate_id"`
	CandidateSnapshotID        string          `gorm:"column:candidate_snapshot_id"`
	CandidateVersion           int64           `gorm:"column:candidate_version"`
	JournalSequence            int64           `gorm:"column:journal_sequence"`
	SessionEpoch               int64           `gorm:"column:session_epoch"`
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
	FullStackTemplateID        string          `gorm:"column:full_stack_template_id"`
	FullStackTemplateHash      string          `gorm:"column:full_stack_template_hash"`
	VerificationProfileID      string          `gorm:"column:verification_profile_id"`
	VerificationProfileVersion int64           `gorm:"column:verification_profile_version"`
	VerificationProfileHash    string          `gorm:"column:verification_profile_hash"`
	TemplateReleases           json.RawMessage `gorm:"column:template_releases"`
	Obligations                json.RawMessage `gorm:"column:obligations"`
	CheckIDs                   json.RawMessage `gorm:"column:check_ids"`
	RequiredCheckIDs           json.RawMessage `gorm:"column:required_check_ids"`
	CheckCount                 int             `gorm:"column:check_count"`
	ObligationCount            int             `gorm:"column:obligation_count"`
	RuntimePolicyHash          string          `gorm:"column:runtime_policy_hash"`
	ContentStore               string          `gorm:"column:content_store"`
	ContentRef                 string          `gorm:"column:content_ref"`
	ContentHash                string          `gorm:"column:content_hash"`
	PlanHash                   string          `gorm:"column:plan_hash"`
	CreatedBy                  string          `gorm:"column:created_by"`
	CreatedAt                  time.Time       `gorm:"column:created_at"`
}

func (planRow) TableName() string { return "candidate_verification_plans" }

type planTemplateProjection struct {
	Role        string `json:"role"`
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

type planObligationProjection struct {
	ID        string   `json:"id"`
	Level     string   `json:"level"`
	Status    string   `json:"status"`
	OracleIDs []string `json:"oracleIds"`
}

func (store *PostgresStore) SavePlan(
	ctx context.Context,
	planID string,
	createdBy string,
	compiled CompiledPlan,
) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if !validUUIDs(planID, createdBy) {
		return Plan{}, planInvalid("Plan identity or actor")
	}
	parsed, err := ParsePlan(compiled.Content, compiled.PlanHash)
	if err != nil {
		return Plan{}, err
	}
	payload, err := domain.CanonicalJSON(parsed.Content)
	if err != nil {
		return Plan{}, receiptIntegrity("encode VerificationPlan", err)
	}
	contentRef, err := store.contents.PutPending(
		ctx, parsed.Content.ProjectID, planAggregateType, planID, 1, payload,
	)
	if err != nil {
		return Plan{}, fmt.Errorf("put pending VerificationPlan content: %w", err)
	}
	if contentRef.ContentHash != parsed.PlanHash {
		_ = store.contents.Abort(context.Background(), contentRef.ID)
		return Plan{}, receiptIntegrity("VerificationPlan content store hash mismatch", nil)
	}
	abort := true
	defer func() {
		if abort {
			_ = store.contents.Abort(context.Background(), contentRef.ID)
		}
	}()

	projections, err := encodePlanProjections(parsed.Content)
	if err != nil {
		return Plan{}, err
	}
	var persisted planRow
	replayed := false
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Exec(`
INSERT INTO candidate_verification_plans (
  id, schema_version, scope, project_id,
  sandbox_session_id, session_version,
  candidate_id, candidate_snapshot_id, candidate_version, journal_sequence,
  session_epoch, writer_lease_epoch,
  tree_store, tree_owner_id, tree_ref, tree_content_hash, tree_hash,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  verification_profile_id, verification_profile_version, verification_profile_hash,
  template_releases, obligations, check_ids, required_check_ids,
  check_count, obligation_count, runtime_policy_hash,
  content_store, content_ref, content_hash, plan_hash, created_by
) VALUES (
  ?, 'candidate-verification-plan/v1', 'candidate', ?, ?, ?, ?, ?, ?, ?, ?, ?,
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
  ?::jsonb, ?::jsonb, ?::jsonb, ?::jsonb, ?, ?, ?,
  'mongo', ?, ?, ?, ?
)
ON CONFLICT DO NOTHING
`, planID, parsed.Content.ProjectID, parsed.Content.Subject.SessionID,
			int64(parsed.Content.Subject.SessionVersion), parsed.Content.Subject.CandidateID,
			parsed.Content.Subject.CandidateSnapshotID, int64(parsed.Content.Subject.CandidateVersion),
			int64(parsed.Content.Subject.JournalSequence), int64(parsed.Content.Subject.SessionEpoch),
			int64(parsed.Content.Subject.WriterLeaseEpoch), parsed.Content.Subject.TreeStore,
			parsed.Content.Subject.TreeOwnerID, parsed.Content.Subject.TreeRef,
			parsed.Content.Subject.TreeContentHash, parsed.Content.Subject.TreeHash,
			parsed.Content.BuildManifest.ID, parsed.Content.BuildManifest.ContentHash,
			parsed.Content.BuildContract.ID, parsed.Content.BuildContract.ContentHash,
			parsed.Content.FullStackTemplate.ID, parsed.Content.FullStackTemplate.ContentHash,
			parsed.Content.Profile.ID, int64(parsed.Content.Profile.Version), parsed.Content.Profile.ContentHash,
			projections.templateReleases, projections.obligations, projections.checkIDs,
			projections.requiredCheckIDs, len(parsed.Content.Checks), len(parsed.Content.Obligations),
			projections.runtimePolicyHash, contentRef.ID, contentRef.ContentHash, parsed.PlanHash, createdBy)
		if result.Error != nil {
			return result.Error
		}
		replayed = result.RowsAffected == 0
		query := transaction.Where("id = ?", planID)
		if replayed {
			query = transaction.Where(
				"candidate_snapshot_id = ? AND verification_profile_id = ? AND verification_profile_version = ? AND verification_profile_hash = ? AND runtime_policy_hash = ? AND plan_hash = ?",
				parsed.Content.Subject.CandidateSnapshotID, parsed.Content.Profile.ID,
				int64(parsed.Content.Profile.Version), parsed.Content.Profile.ContentHash,
				projections.runtimePolicyHash, parsed.PlanHash,
			)
		}
		if err := query.Take(&persisted).Error; errors.Is(err, gorm.ErrRecordNotFound) && replayed {
			return ErrPlanConflict
		} else if err != nil {
			return err
		}
		if persisted.PlanHash != parsed.PlanHash || persisted.ProjectID != parsed.Content.ProjectID {
			return ErrPlanConflict
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Plan{}, mapReceiptStoreError("save VerificationPlan", err)
	}
	if replayed {
		abort = false
		return store.finishReplayedPlan(ctx, persisted, contentRef.ID)
	}
	abort = false
	if err := store.contents.Finalize(ctx, contentRef.ID); err != nil {
		return Plan{}, fmt.Errorf("%w: finalize VerificationPlan content: %v", core.ErrContentNotReady, err)
	}
	return store.GetPlan(ctx, persisted.ProjectID, persisted.ID)
}

func (store *PostgresStore) finishReplayedPlan(
	ctx context.Context,
	persisted planRow,
	orphanContentID string,
) (Plan, error) {
	_ = store.contents.Abort(context.Background(), orphanContentID)
	plan, err := store.hydratePlan(ctx, persisted)
	if err != nil {
		return Plan{}, err
	}
	plan.Replayed = true
	return plan, nil
}

func (store *PostgresStore) GetPlan(
	ctx context.Context,
	projectID string,
	planID string,
) (Plan, error) {
	var row planRow
	err := store.database.WithContext(ctx).
		Where("id = ? AND project_id = ?", planID, projectID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Plan{}, ErrPlanNotFound
	}
	if err != nil {
		return Plan{}, mapReceiptStoreError("load VerificationPlan", err)
	}
	return store.hydratePlan(ctx, row)
}

func (store *PostgresStore) hydratePlan(ctx context.Context, row planRow) (Plan, error) {
	stored, err := store.contents.Get(ctx, row.ContentRef, row.ContentHash)
	if err != nil {
		return Plan{}, receiptIntegrity("load VerificationPlan content", err)
	}
	if stored.ProjectID != row.ProjectID || stored.AggregateType != planAggregateType ||
		stored.AggregateID != row.ID || stored.SchemaVersion != 1 {
		return Plan{}, receiptIntegrity("VerificationPlan content identity", nil)
	}
	var planContent PlanContent
	if err := json.Unmarshal(stored.Payload, &planContent); err != nil {
		return Plan{}, receiptIntegrity("decode VerificationPlan content", err)
	}
	parsed, err := ParsePlan(planContent, row.PlanHash)
	if err != nil || !planMatchesRow(parsed.Content, row) {
		return Plan{}, receiptIntegrity("VerificationPlan projection mismatch", err)
	}
	if stored.State != content.StateFinalized {
		return Plan{}, core.ErrContentNotReady
	}
	return Plan{
		ID: row.ID, Content: parsed.Content, PlanHash: parsed.PlanHash,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
	}, nil
}

type encodedPlanProjections struct {
	templateReleases  string
	obligations       string
	checkIDs          string
	requiredCheckIDs  string
	runtimePolicyHash string
}

func encodePlanProjections(content PlanContent) (encodedPlanProjections, error) {
	templatesProjection := make([]planTemplateProjection, 0, len(content.TemplateReleases))
	for _, release := range content.TemplateReleases {
		templatesProjection = append(templatesProjection, planTemplateProjection{
			Role: release.Role, ID: release.Release.ID, ContentHash: release.Release.ContentHash,
		})
	}
	obligations := make([]planObligationProjection, 0, len(content.Obligations))
	for _, obligation := range content.Obligations {
		obligations = append(obligations, planObligationProjection{
			ID: obligation.ID, Level: obligation.Level, Status: obligation.Status,
			OracleIDs: append([]string{}, obligation.OracleIDs...),
		})
	}
	checkIDs := make([]string, 0, len(content.Checks))
	requiredCheckIDs := []string{}
	for _, check := range content.Checks {
		checkIDs = append(checkIDs, check.ID)
		if check.Required {
			requiredCheckIDs = append(requiredCheckIDs, check.ID)
		}
	}
	if len(requiredCheckIDs) == 0 {
		return encodedPlanProjections{}, planInvalid("Plan has no required checks")
	}
	templateJSON, err := json.Marshal(templatesProjection)
	if err != nil {
		return encodedPlanProjections{}, receiptIntegrity("encode Plan TemplateReleases", err)
	}
	obligationJSON, err := json.Marshal(obligations)
	if err != nil {
		return encodedPlanProjections{}, receiptIntegrity("encode Plan obligations", err)
	}
	checkJSON, err := json.Marshal(checkIDs)
	if err != nil {
		return encodedPlanProjections{}, receiptIntegrity("encode Plan check IDs", err)
	}
	requiredJSON, err := json.Marshal(requiredCheckIDs)
	if err != nil {
		return encodedPlanProjections{}, receiptIntegrity("encode Plan required check IDs", err)
	}
	runtimeHash, err := domain.CanonicalHash(content.RuntimePolicy)
	if err != nil {
		return encodedPlanProjections{}, receiptIntegrity("hash Plan runtime policy", err)
	}
	return encodedPlanProjections{
		templateReleases: string(templateJSON), obligations: string(obligationJSON),
		checkIDs: string(checkJSON), requiredCheckIDs: string(requiredJSON),
		runtimePolicyHash: "sha256:" + runtimeHash,
	}, nil
}

func planMatchesRow(content PlanContent, row planRow) bool {
	projections, err := encodePlanProjections(content)
	if err != nil {
		return false
	}
	return row.SchemaVersion == "candidate-verification-plan/v1" && row.Scope == string(ScopeCandidate) &&
		row.ProjectID == content.ProjectID && row.SandboxSessionID == content.Subject.SessionID &&
		row.SessionVersion == int64(content.Subject.SessionVersion) && row.CandidateID == content.Subject.CandidateID &&
		row.CandidateSnapshotID == content.Subject.CandidateSnapshotID &&
		row.CandidateVersion == int64(content.Subject.CandidateVersion) &&
		row.JournalSequence == int64(content.Subject.JournalSequence) &&
		row.SessionEpoch == int64(content.Subject.SessionEpoch) &&
		row.WriterLeaseEpoch == int64(content.Subject.WriterLeaseEpoch) &&
		row.TreeStore == content.Subject.TreeStore && row.TreeOwnerID == content.Subject.TreeOwnerID &&
		row.TreeRef == content.Subject.TreeRef && row.TreeContentHash == content.Subject.TreeContentHash &&
		row.TreeHash == content.Subject.TreeHash && row.BuildManifestID == content.BuildManifest.ID &&
		row.BuildManifestHash == content.BuildManifest.ContentHash && row.BuildContractID == content.BuildContract.ID &&
		row.BuildContractHash == content.BuildContract.ContentHash && row.FullStackTemplateID == content.FullStackTemplate.ID &&
		row.FullStackTemplateHash == content.FullStackTemplate.ContentHash &&
		row.VerificationProfileID == content.Profile.ID &&
		row.VerificationProfileVersion == int64(content.Profile.Version) &&
		row.VerificationProfileHash == content.Profile.ContentHash && row.CheckCount == len(content.Checks) &&
		row.ObligationCount == len(content.Obligations) && row.RuntimePolicyHash == projections.runtimePolicyHash &&
		row.ContentHash == row.PlanHash && jsonProjectionEqual(row.TemplateReleases, projections.templateReleases) &&
		jsonProjectionEqual(row.Obligations, projections.obligations) &&
		jsonProjectionEqual(row.CheckIDs, projections.checkIDs) &&
		jsonProjectionEqual(row.RequiredCheckIDs, projections.requiredCheckIDs)
}

func jsonProjectionEqual(value json.RawMessage, expected string) bool {
	left, leftErr := domain.CanonicalJSON(value)
	right, rightErr := domain.CanonicalJSON([]byte(expected))
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}
