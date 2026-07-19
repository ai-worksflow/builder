package verification

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"gorm.io/gorm"
)

var (
	ErrReceiptNotFound       = errors.New("verification receipt was not found")
	ErrReceiptConflict       = errors.New("verification receipt conflicts with committed evidence")
	ErrReceiptRunConflict    = errors.New("verification run version or worker fence changed")
	ErrReceiptStoreIntegrity = errors.New("verification receipt persistence integrity failure")
	ErrReceiptStoreDown      = errors.New("verification receipt persistence is unavailable")
)

const receiptAggregateType = "candidate_verification_receipt"

type ReceiptContentStore interface {
	PutPending(context.Context, string, string, string, int, json.RawMessage) (content.Reference, error)
	Finalize(context.Context, string) error
	Abort(context.Context, string) error
	Get(context.Context, string, string) (content.StoredContent, error)
}

type PersistReceiptInput struct {
	Receipt                Receipt
	ExpectedRunVersion     uint64
	ExpectedRunFenceEpoch  uint64
	ExpectedRunLeaseWorker string
	ExpectedAttemptID      string
	ExpectedAttemptVersion uint64
	ExpectedAttemptFence   uint64
	TerminalReason         string
}

type PostgresStore struct {
	database *gorm.DB
	contents ReceiptContentStore
}

func NewPostgresStore(database *gorm.DB, contents ReceiptContentStore) (*PostgresStore, error) {
	if database == nil || contents == nil {
		return nil, fmt.Errorf("%w: database and content store are required", ErrReceiptStoreDown)
	}
	return &PostgresStore{database: database, contents: contents}, nil
}

type receiptRow struct {
	ID                         string          `gorm:"column:id"`
	SchemaVersion              string          `gorm:"column:schema_version"`
	Scope                      string          `gorm:"column:scope"`
	RunID                      string          `gorm:"column:run_id"`
	ProjectID                  string          `gorm:"column:project_id"`
	PlanID                     string          `gorm:"column:plan_id"`
	PlanHash                   string          `gorm:"column:plan_hash"`
	SandboxSessionID           string          `gorm:"column:sandbox_session_id"`
	CandidateID                string          `gorm:"column:candidate_id"`
	CandidateSnapshotID        string          `gorm:"column:candidate_snapshot_id"`
	CandidateVersion           int64           `gorm:"column:candidate_version"`
	JournalSequence            int64           `gorm:"column:journal_sequence"`
	SessionEpoch               int64           `gorm:"column:session_epoch"`
	WriterLeaseEpoch           int64           `gorm:"column:writer_lease_epoch"`
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
	AttemptIDs                 json.RawMessage `gorm:"column:attempt_ids"`
	CheckCount                 int             `gorm:"column:check_count"`
	CoverageCount              int             `gorm:"column:coverage_count"`
	MustCount                  int             `gorm:"column:must_count"`
	MustPassedCount            int             `gorm:"column:must_passed_count"`
	BlockerCount               int             `gorm:"column:blocker_count"`
	WarningCount               int             `gorm:"column:warning_count"`
	Decision                   string          `gorm:"column:decision"`
	ExecutionError             sql.NullString  `gorm:"column:execution_error"`
	ContentStore               string          `gorm:"column:content_store"`
	ContentRef                 string          `gorm:"column:content_ref"`
	ContentHash                string          `gorm:"column:content_hash"`
	PayloadHash                string          `gorm:"column:payload_hash"`
	CreatedBy                  string          `gorm:"column:created_by"`
	CreatedAt                  time.Time       `gorm:"column:created_at"`
}

func (receiptRow) TableName() string { return "candidate_verification_receipts" }

type checkRow struct {
	ReceiptID              string          `gorm:"column:receipt_id"`
	RunID                  string          `gorm:"column:run_id"`
	Ordinal                int             `gorm:"column:ordinal"`
	CheckID                string          `gorm:"column:check_id"`
	Kind                   string          `gorm:"column:kind"`
	ServiceID              sql.NullString  `gorm:"column:service_id"`
	CommandID              sql.NullString  `gorm:"column:command_id"`
	Required               bool            `gorm:"column:required"`
	Status                 string          `gorm:"column:status"`
	AttemptID              string          `gorm:"column:attempt_id"`
	VerifierImageDigest    string          `gorm:"column:verifier_image_digest"`
	Argv                   json.RawMessage `gorm:"column:argv"`
	WorkingDirectory       string          `gorm:"column:working_directory"`
	ExitCode               sql.NullInt64   `gorm:"column:exit_code"`
	StartedAt              time.Time       `gorm:"column:started_at"`
	CompletedAt            time.Time       `gorm:"column:completed_at"`
	DurationMS             int64           `gorm:"column:duration_ms"`
	AttemptCount           int64           `gorm:"column:attempt_count"`
	Stdout                 json.RawMessage `gorm:"column:stdout"`
	Stderr                 json.RawMessage `gorm:"column:stderr"`
	Truncated              bool            `gorm:"column:truncated"`
	RedactionCount         int             `gorm:"column:redaction_count"`
	OracleIDs              json.RawMessage `gorm:"column:oracle_ids"`
	AcceptanceCriterionIDs json.RawMessage `gorm:"column:acceptance_criterion_ids"`
	ObligationIDs          json.RawMessage `gorm:"column:obligation_ids"`
	Diagnostics            json.RawMessage `gorm:"column:diagnostics"`
}

func (checkRow) TableName() string { return "candidate_verification_checks" }

type coverageRow struct {
	ReceiptID       string          `gorm:"column:receipt_id"`
	Ordinal         int             `gorm:"column:ordinal"`
	BuildContractID string          `gorm:"column:build_contract_id"`
	ObligationID    string          `gorm:"column:obligation_id"`
	Level           string          `gorm:"column:level"`
	OracleIDs       json.RawMessage `gorm:"column:oracle_ids"`
	CheckIDs        json.RawMessage `gorm:"column:check_ids"`
	Status          string          `gorm:"column:status"`
}

func (coverageRow) TableName() string { return "candidate_verification_obligation_coverage" }

func (store *PostgresStore) PersistReceipt(
	ctx context.Context,
	input PersistReceiptInput,
) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	receipt, err := ParseReceipt(input.Receipt)
	if err != nil {
		return Receipt{}, err
	}
	input.ExpectedRunLeaseWorker = strings.TrimSpace(input.ExpectedRunLeaseWorker)
	input.ExpectedAttemptID = strings.TrimSpace(input.ExpectedAttemptID)
	input.TerminalReason = strings.TrimSpace(input.TerminalReason)
	attemptPrecondition := input.ExpectedAttemptID != "" || input.ExpectedAttemptVersion != 0 ||
		input.ExpectedAttemptFence != 0
	attemptReferenced := false
	for _, attemptID := range receipt.AttemptIDs {
		if attemptID == input.ExpectedAttemptID {
			attemptReferenced = true
			break
		}
	}
	if input.ExpectedRunVersion == 0 || input.ExpectedRunFenceEpoch == 0 ||
		input.ExpectedRunLeaseWorker == "" || len(input.ExpectedRunLeaseWorker) > 160 ||
		(attemptPrecondition && (!validUUIDs(input.ExpectedAttemptID) ||
			input.ExpectedAttemptVersion == 0 || input.ExpectedAttemptFence == 0 || !attemptReferenced)) ||
		(receipt.Decision == DecisionPassed && input.TerminalReason != "") ||
		(receipt.Decision != DecisionPassed && (input.TerminalReason == "" || len(input.TerminalReason) > 2000)) {
		return Receipt{}, fmt.Errorf("%w: invalid terminal Run precondition", ErrInvalidReceipt)
	}
	if existing, existingErr := store.GetReceiptByRun(ctx, receipt.RunID); existingErr == nil {
		if existing.PayloadHash != receipt.PayloadHash {
			return Receipt{}, ErrReceiptConflict
		}
		return existing, nil
	} else if !errors.Is(existingErr, ErrReceiptNotFound) {
		return Receipt{}, existingErr
	}

	payload, err := json.Marshal(receipt)
	if err != nil {
		return Receipt{}, receiptIntegrity("encode Receipt", err)
	}
	contentRef, err := store.contents.PutPending(
		ctx, receipt.ProjectID, receiptAggregateType, receipt.ID, 1, payload,
	)
	if err != nil {
		return Receipt{}, fmt.Errorf("put pending VerificationReceipt content: %w", err)
	}
	abort := true
	defer func() {
		if abort {
			_ = store.contents.Abort(context.Background(), contentRef.ID)
		}
	}()

	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if attemptPrecondition {
			result := transaction.Exec(`
UPDATE candidate_verification_attempts
SET state = ?, version = version + 1, lease_expires_at = NULL,
    terminal_reason = ?, execution_error = ?, finished_at = statement_timestamp(),
    updated_by = ?
WHERE id = ? AND run_id = ? AND project_id = ? AND plan_id = ? AND plan_hash = ?
  AND state = 'collecting' AND version = ? AND fence_epoch = ?
  AND lease_worker_id = ? AND lease_epoch = ?
`, string(receipt.Decision), nullableString(input.TerminalReason), nullableString(receipt.ExecutionError),
				receipt.CreatedBy, input.ExpectedAttemptID, receipt.RunID, receipt.ProjectID,
				receipt.Plan.ID, receipt.Plan.ContentHash, int64(input.ExpectedAttemptVersion),
				int64(input.ExpectedAttemptFence), input.ExpectedRunLeaseWorker,
				int64(input.ExpectedAttemptFence))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrReceiptRunConflict
			}
		}
		attemptIDs, marshalErr := json.Marshal(receipt.AttemptIDs)
		if marshalErr != nil {
			return receiptIntegrity("encode Receipt Attempt IDs", marshalErr)
		}
		result := transaction.Exec(`
INSERT INTO candidate_verification_receipts (
  id, schema_version, scope, run_id, project_id, plan_id, plan_hash,
  sandbox_session_id, candidate_id, candidate_snapshot_id,
  candidate_version, journal_sequence, session_epoch, writer_lease_epoch, tree_hash,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  verification_profile_id, verification_profile_version, verification_profile_hash,
  attempt_ids, check_count, coverage_count, must_count, must_passed_count,
  blocker_count, warning_count, decision, execution_error,
  content_store, content_ref, content_hash, payload_hash, created_by, created_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
  ?::jsonb, ?, ?, ?, ?, ?, ?, ?, ?, 'mongo', ?, ?, ?, ?, ?
)
`, receipt.ID, receipt.SchemaVersion, string(receipt.Scope), receipt.RunID, receipt.ProjectID,
			receipt.Plan.ID, receipt.Plan.ContentHash, receipt.Subject.SessionID,
			receipt.Subject.CandidateID, receipt.Subject.CandidateSnapshotID,
			int64(receipt.Subject.CandidateVersion), int64(receipt.Subject.JournalSequence),
			int64(receipt.Subject.SessionEpoch), int64(receipt.Subject.WriterLeaseEpoch),
			receipt.Subject.TreeHash, receipt.BuildManifest.ID, receipt.BuildManifest.ContentHash,
			receipt.BuildContract.ID, receipt.BuildContract.ContentHash,
			receipt.FullStackTemplate.ID, receipt.FullStackTemplate.ContentHash,
			receipt.Profile.ID, int64(receipt.Profile.Version), receipt.Profile.ContentHash,
			string(attemptIDs), len(receipt.Checks), len(receipt.ObligationCoverage),
			receipt.MustCount, receipt.MustPassedCount, receipt.BlockerCount,
			receipt.WarningCount, string(receipt.Decision), nullableString(receipt.ExecutionError),
			contentRef.ID, contentRef.ContentHash, receipt.PayloadHash, receipt.CreatedBy, receipt.CreatedAt,
		)
		if result.Error != nil {
			return result.Error
		}
		for ordinal := range receipt.Checks {
			if err := insertReceiptCheck(transaction, receipt, receipt.Checks[ordinal], ordinal); err != nil {
				return err
			}
		}
		for ordinal := range receipt.ObligationCoverage {
			if err := insertReceiptCoverage(
				transaction, receipt, receipt.ObligationCoverage[ordinal], ordinal,
			); err != nil {
				return err
			}
		}
		result = transaction.Exec(`
UPDATE candidate_verification_runs
SET state = ?, version = version + 1, lease_expires_at = NULL,
    terminal_reason = ?, execution_error = ?, finished_at = statement_timestamp(),
    updated_by = ?
WHERE id = ? AND project_id = ? AND plan_id = ? AND plan_hash = ?
  AND state = 'collecting' AND version = ? AND fence_epoch = ?
  AND lease_worker_id = ? AND lease_epoch = ?
`, string(receipt.Decision), nullableString(input.TerminalReason), nullableString(receipt.ExecutionError),
			receipt.CreatedBy, receipt.RunID, receipt.ProjectID, receipt.Plan.ID,
			receipt.Plan.ContentHash, int64(input.ExpectedRunVersion),
			int64(input.ExpectedRunFenceEpoch), input.ExpectedRunLeaseWorker,
			int64(input.ExpectedRunFenceEpoch))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrReceiptRunConflict
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		mapped := mapReceiptStoreError("persist Receipt", err)
		if isReceiptUniqueViolation(err) {
			if concurrent, loadErr := store.GetReceiptByRun(ctx, receipt.RunID); loadErr == nil {
				if concurrent.PayloadHash == receipt.PayloadHash {
					return concurrent, nil
				}
				return Receipt{}, ErrReceiptConflict
			}
		}
		return Receipt{}, mapped
	}
	abort = false
	if err := store.contents.Finalize(ctx, contentRef.ID); err != nil {
		return Receipt{}, fmt.Errorf("%w: finalize VerificationReceipt content: %v", core.ErrContentNotReady, err)
	}
	return store.GetReceipt(ctx, receipt.ProjectID, receipt.ID)
}

func insertReceiptCheck(
	transaction *gorm.DB,
	receipt Receipt,
	check CheckResult,
	ordinal int,
) error {
	argv, err := json.Marshal(check.Argv)
	if err != nil {
		return receiptIntegrity("encode check argv", err)
	}
	oracleIDs, err := json.Marshal(check.OracleIDs)
	if err != nil {
		return receiptIntegrity("encode check Oracle IDs", err)
	}
	acceptanceIDs, err := json.Marshal(check.AcceptanceCriterionIDs)
	if err != nil {
		return receiptIntegrity("encode check acceptance IDs", err)
	}
	obligationIDs, err := json.Marshal(check.ObligationIDs)
	if err != nil {
		return receiptIntegrity("encode check obligation IDs", err)
	}
	diagnostics, err := json.Marshal(check.Diagnostics)
	if err != nil {
		return receiptIntegrity("encode check diagnostics", err)
	}
	stdout, err := marshalOptionalBlob(check.Stdout)
	if err != nil {
		return err
	}
	stderr, err := marshalOptionalBlob(check.Stderr)
	if err != nil {
		return err
	}
	result := transaction.Exec(`
INSERT INTO candidate_verification_checks (
  receipt_id, run_id, ordinal, check_id, kind, service_id, command_id,
  required, status, attempt_id, verifier_image_digest, argv, working_directory,
  exit_code, started_at, completed_at, duration_ms, attempt_count,
  stdout, stderr, truncated, redaction_count,
  oracle_ids, acceptance_criterion_ids, obligation_ids, diagnostics
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, ?,
  ?::jsonb, ?::jsonb, ?, ?, ?::jsonb, ?::jsonb, ?::jsonb, ?::jsonb
)
`, receipt.ID, receipt.RunID, ordinal, check.ID, check.Kind,
		nullableString(check.ServiceID), nullableString(check.CommandID), check.Required,
		string(check.Status), check.AttemptID, check.VerifierImageDigest, string(argv),
		check.WorkingDirectory, nullableInt(check.ExitCode), check.StartedAt, check.CompletedAt,
		check.DurationMS, int64(check.AttemptCount), stdout, stderr, check.Truncated,
		check.RedactionCount, string(oracleIDs), string(acceptanceIDs), string(obligationIDs),
		string(diagnostics))
	return result.Error
}

func insertReceiptCoverage(
	transaction *gorm.DB,
	receipt Receipt,
	coverage ObligationCoverage,
	ordinal int,
) error {
	oracleIDs, err := json.Marshal(coverage.OracleIDs)
	if err != nil {
		return receiptIntegrity("encode coverage Oracle IDs", err)
	}
	checkIDs, err := json.Marshal(coverage.CheckIDs)
	if err != nil {
		return receiptIntegrity("encode coverage check IDs", err)
	}
	return transaction.Exec(`
INSERT INTO candidate_verification_obligation_coverage (
  receipt_id, ordinal, build_contract_id, obligation_id,
  level, oracle_ids, check_ids, status
) VALUES (?, ?, ?, ?, ?, ?::jsonb, ?::jsonb, ?)
`, receipt.ID, ordinal, receipt.BuildContract.ID, coverage.ObligationID,
		coverage.Level, string(oracleIDs), string(checkIDs), coverage.Status).Error
}

func (store *PostgresStore) ResolveReceiptProject(
	ctx context.Context,
	receiptID string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var row struct {
		ProjectID string `gorm:"column:project_id"`
	}
	err := store.database.WithContext(ctx).
		Model(&receiptRow{}).
		Select("project_id").
		Where("id = ?", receiptID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrReceiptNotFound
	}
	if err != nil {
		return "", mapReceiptStoreError("resolve Receipt project", err)
	}
	if !validUUIDs(row.ProjectID) {
		return "", receiptIntegrity("Receipt project projection", nil)
	}
	return row.ProjectID, nil
}

func (store *PostgresStore) GetReceipt(
	ctx context.Context,
	projectID string,
	receiptID string,
) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	var row receiptRow
	err := store.database.WithContext(ctx).
		Where("id = ? AND project_id = ?", receiptID, projectID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Receipt{}, ErrReceiptNotFound
	}
	if err != nil {
		return Receipt{}, mapReceiptStoreError("load Receipt", err)
	}
	return store.hydrateReceipt(ctx, row)
}

func (store *PostgresStore) GetReceiptByRun(
	ctx context.Context,
	runID string,
) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	var row receiptRow
	err := store.database.WithContext(ctx).Where("run_id = ?", runID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Receipt{}, ErrReceiptNotFound
	}
	if err != nil {
		return Receipt{}, mapReceiptStoreError("load Receipt by Run", err)
	}
	return store.hydrateReceipt(ctx, row)
}

func (store *PostgresStore) hydrateReceipt(
	ctx context.Context,
	row receiptRow,
) (Receipt, error) {
	stored, err := store.contents.Get(ctx, row.ContentRef, row.ContentHash)
	if err != nil {
		return Receipt{}, receiptIntegrity("load Receipt content", err)
	}
	if stored.ProjectID != row.ProjectID || stored.AggregateType != receiptAggregateType ||
		stored.AggregateID != row.ID || stored.SchemaVersion != 1 {
		return Receipt{}, receiptIntegrity("Receipt content identity", nil)
	}
	var receipt Receipt
	if err := json.Unmarshal(stored.Payload, &receipt); err != nil {
		return Receipt{}, receiptIntegrity("decode Receipt content", err)
	}
	receipt, err = ParseReceipt(receipt)
	if err != nil {
		return Receipt{}, receiptIntegrity("parse Receipt content", err)
	}
	if !receiptMatchesRow(receipt, row) {
		return Receipt{}, receiptIntegrity("Receipt parent projection mismatch", nil)
	}
	checks, err := loadReceiptChecks(store.database.WithContext(ctx), row.ID)
	if err != nil {
		return Receipt{}, err
	}
	coverage, err := loadReceiptCoverage(store.database.WithContext(ctx), row.ID)
	if err != nil {
		return Receipt{}, err
	}
	if !equalChecks(receipt.Checks, checks) || !equalCoverage(receipt.ObligationCoverage, coverage) {
		return Receipt{}, receiptIntegrity("Receipt child projection mismatch", nil)
	}
	if stored.State != content.StateFinalized {
		return Receipt{}, core.ErrContentNotReady
	}
	return receipt, nil
}

func loadReceiptChecks(database *gorm.DB, receiptID string) ([]CheckResult, error) {
	var rows []checkRow
	if err := database.Where("receipt_id = ?", receiptID).Order("ordinal ASC").Find(&rows).Error; err != nil {
		return nil, mapReceiptStoreError("load Receipt checks", err)
	}
	result := make([]CheckResult, 0, len(rows))
	for index := range rows {
		row := rows[index]
		if row.Ordinal != index || row.AttemptCount <= 0 {
			return nil, receiptIntegrity("invalid check ordinal or attempt count", nil)
		}
		var argv, oracleIDs, acceptanceIDs, obligationIDs []string
		var diagnostics []Diagnostic
		if err := decodeJSON(row.Argv, &argv); err != nil {
			return nil, err
		}
		if err := decodeJSON(row.OracleIDs, &oracleIDs); err != nil {
			return nil, err
		}
		if err := decodeJSON(row.AcceptanceCriterionIDs, &acceptanceIDs); err != nil {
			return nil, err
		}
		if err := decodeJSON(row.ObligationIDs, &obligationIDs); err != nil {
			return nil, err
		}
		if err := decodeJSON(row.Diagnostics, &diagnostics); err != nil {
			return nil, err
		}
		stdout, err := decodeOptionalBlob(row.Stdout)
		if err != nil {
			return nil, err
		}
		stderr, err := decodeOptionalBlob(row.Stderr)
		if err != nil {
			return nil, err
		}
		result = append(result, CheckResult{
			ID: row.CheckID, Kind: row.Kind, ServiceID: nullString(row.ServiceID),
			CommandID: nullString(row.CommandID), Required: row.Required,
			Status: CheckStatus(row.Status), AttemptID: row.AttemptID,
			VerifierImageDigest: row.VerifierImageDigest, Argv: argv,
			WorkingDirectory: row.WorkingDirectory, ExitCode: nullInt(row.ExitCode),
			StartedAt: row.StartedAt, CompletedAt: row.CompletedAt,
			DurationMS: row.DurationMS, AttemptCount: uint64(row.AttemptCount),
			Stdout: stdout, Stderr: stderr, Truncated: row.Truncated,
			RedactionCount: row.RedactionCount, OracleIDs: oracleIDs,
			AcceptanceCriterionIDs: acceptanceIDs, ObligationIDs: obligationIDs,
			Diagnostics: diagnostics,
		})
	}
	return result, nil
}

func loadReceiptCoverage(database *gorm.DB, receiptID string) ([]ObligationCoverage, error) {
	var rows []coverageRow
	if err := database.Where("receipt_id = ?", receiptID).Order("ordinal ASC").Find(&rows).Error; err != nil {
		return nil, mapReceiptStoreError("load Receipt coverage", err)
	}
	result := make([]ObligationCoverage, 0, len(rows))
	for index := range rows {
		row := rows[index]
		if row.Ordinal != index {
			return nil, receiptIntegrity("invalid coverage ordinal", nil)
		}
		var oracleIDs, checkIDs []string
		if err := decodeJSON(row.OracleIDs, &oracleIDs); err != nil {
			return nil, err
		}
		if err := decodeJSON(row.CheckIDs, &checkIDs); err != nil {
			return nil, err
		}
		result = append(result, ObligationCoverage{
			ObligationID: row.ObligationID, Level: row.Level,
			OracleIDs: oracleIDs, CheckIDs: checkIDs, Status: row.Status,
		})
	}
	return result, nil
}

func receiptMatchesRow(receipt Receipt, row receiptRow) bool {
	var attemptIDs []string
	if err := json.Unmarshal(row.AttemptIDs, &attemptIDs); err != nil {
		return false
	}
	return receipt.SchemaVersion == row.SchemaVersion && string(receipt.Scope) == row.Scope &&
		receipt.ID == row.ID && receipt.RunID == row.RunID && receipt.ProjectID == row.ProjectID &&
		receipt.Plan.ID == row.PlanID && receipt.Plan.ContentHash == row.PlanHash &&
		receipt.Subject.SessionID == row.SandboxSessionID && receipt.Subject.CandidateID == row.CandidateID &&
		receipt.Subject.CandidateSnapshotID == row.CandidateSnapshotID &&
		int64(receipt.Subject.CandidateVersion) == row.CandidateVersion &&
		int64(receipt.Subject.JournalSequence) == row.JournalSequence &&
		int64(receipt.Subject.SessionEpoch) == row.SessionEpoch &&
		int64(receipt.Subject.WriterLeaseEpoch) == row.WriterLeaseEpoch &&
		receipt.Subject.TreeHash == row.TreeHash &&
		receipt.BuildManifest.ID == row.BuildManifestID && receipt.BuildManifest.ContentHash == row.BuildManifestHash &&
		receipt.BuildContract.ID == row.BuildContractID && receipt.BuildContract.ContentHash == row.BuildContractHash &&
		receipt.FullStackTemplate.ID == row.FullStackTemplateID && receipt.FullStackTemplate.ContentHash == row.FullStackTemplateHash &&
		receipt.Profile.ID == row.VerificationProfileID && int64(receipt.Profile.Version) == row.VerificationProfileVersion &&
		receipt.Profile.ContentHash == row.VerificationProfileHash && equalStrings(receipt.AttemptIDs, attemptIDs) &&
		len(receipt.Checks) == row.CheckCount && len(receipt.ObligationCoverage) == row.CoverageCount &&
		receipt.MustCount == row.MustCount && receipt.MustPassedCount == row.MustPassedCount &&
		receipt.BlockerCount == row.BlockerCount && receipt.WarningCount == row.WarningCount &&
		string(receipt.Decision) == row.Decision && receipt.ExecutionError == nullString(row.ExecutionError) &&
		receipt.PayloadHash == row.PayloadHash && receipt.CreatedBy == row.CreatedBy && row.ContentStore == "mongo"
}

func marshalOptionalBlob(value *BlobReference) (any, error) {
	if value == nil {
		return nil, nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, receiptIntegrity("encode check log reference", err)
	}
	return string(payload), nil
}

func decodeOptionalBlob(value json.RawMessage) (*BlobReference, error) {
	if len(value) == 0 || string(value) == "null" {
		return nil, nil
	}
	var result BlobReference
	if err := json.Unmarshal(value, &result); err != nil {
		return nil, receiptIntegrity("decode check log reference", err)
	}
	return &result, nil
}

func decodeJSON(value json.RawMessage, target any) error {
	if len(value) == 0 || string(value) == "null" {
		return receiptIntegrity("missing Receipt JSON projection", nil)
	}
	if err := json.Unmarshal(value, target); err != nil {
		return receiptIntegrity("decode Receipt JSON projection", err)
	}
	return nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func nullInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int64)
	return &result
}

type receiptStoreError struct {
	operation string
	kind      error
	cause     error
}

func (err *receiptStoreError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("verification receipt persistence %s: %v: %v", err.operation, err.kind, err.cause)
}

func (err *receiptStoreError) Unwrap() []error {
	if err == nil {
		return nil
	}
	return []error{err.kind, err.cause}
}

func receiptIntegrity(message string, cause error) error {
	if cause == nil {
		cause = errors.New(message)
	} else {
		cause = fmt.Errorf("%s: %w", message, cause)
	}
	return &receiptStoreError{operation: message, kind: ErrReceiptStoreIntegrity, cause: cause}
}

func mapReceiptStoreError(operation string, err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var persisted *receiptStoreError
	if errors.As(err, &persisted) {
		return err
	}
	for _, known := range []error{
		ErrInvalidReceipt, ErrReceiptNotFound, ErrReceiptConflict,
		ErrReceiptRunConflict, ErrPlanNotFound, ErrPlanConflict, core.ErrContentNotReady,
	} {
		if errors.Is(err, known) {
			return err
		}
	}
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		message := strings.ToLower(postgres.Message)
		switch {
		case postgres.Code == "23505":
			return &receiptStoreError{operation: operation, kind: ErrReceiptConflict, cause: err}
		case postgres.Code == "40001" || postgres.Code == "40P01" || strings.Contains(message, "worker fence"):
			return &receiptStoreError{operation: operation, kind: ErrReceiptRunConflict, cause: err}
		case strings.HasPrefix(postgres.Code, "08") || postgres.Code == "57P01" ||
			postgres.Code == "57P02" || postgres.Code == "57P03":
			return &receiptStoreError{operation: operation, kind: ErrReceiptStoreDown, cause: err}
		default:
			return &receiptStoreError{operation: operation, kind: ErrReceiptStoreIntegrity, cause: err}
		}
	}
	return &receiptStoreError{operation: operation, kind: ErrReceiptStoreDown, cause: err}
}

func isReceiptUniqueViolation(err error) bool {
	var postgres *pgconn.PgError
	return errors.As(err, &postgres) && postgres.Code == "23505"
}
