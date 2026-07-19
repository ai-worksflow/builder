package sandbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const defaultProcessListLimit = 100

type PostgresProcessStore struct {
	database *gorm.DB
}

func NewPostgresProcessStore(database *gorm.DB) (*PostgresProcessStore, error) {
	if database == nil {
		return nil, ErrProcessStoreUnavailable
	}
	return &PostgresProcessStore{database: database}, nil
}

type sandboxProcessRow struct {
	ID                       string         `gorm:"column:id"`
	SchemaVersion            string         `gorm:"column:schema_version"`
	ProjectID                string         `gorm:"column:project_id"`
	SessionID                string         `gorm:"column:session_id"`
	SessionEpoch             int64          `gorm:"column:session_epoch"`
	SessionVersionAtCreation int64          `gorm:"column:session_version_at_creation"`
	ActorID                  string         `gorm:"column:actor_id"`
	ServiceID                string         `gorm:"column:service_id"`
	CommandID                string         `gorm:"column:command_id"`
	TemplateReleaseID        string         `gorm:"column:template_release_id"`
	TemplateReleaseHash      string         `gorm:"column:template_release_content_hash"`
	WorkingDirectory         string         `gorm:"column:working_directory"`
	Argv                     []byte         `gorm:"column:argv"`
	LogLimitBytes            int64          `gorm:"column:log_limit_bytes"`
	State                    string         `gorm:"column:state"`
	Version                  int64          `gorm:"column:version"`
	PID                      sql.NullInt64  `gorm:"column:pid"`
	ExitCode                 sql.NullInt64  `gorm:"column:exit_code"`
	Failure                  sql.NullString `gorm:"column:failure"`
	LogBytes                 int64          `gorm:"column:log_bytes"`
	LogTruncated             bool           `gorm:"column:log_truncated"`
	RuntimeStartedAt         sql.NullTime   `gorm:"column:runtime_started_at"`
	FinishedAt               sql.NullTime   `gorm:"column:finished_at"`
	CreatedAt                time.Time      `gorm:"column:created_at"`
	UpdatedAt                time.Time      `gorm:"column:updated_at"`
}

func (sandboxProcessRow) TableName() string { return "sandbox_runtime_processes" }

func (store *PostgresProcessStore) Create(
	ctx context.Context,
	input ProcessRecordInput,
) (ProcessView, error) {
	if err := validateProcessRecordInput(ctx, input); err != nil {
		return ProcessView{}, err
	}
	argv, err := json.Marshal(input.Command.Argv)
	if err != nil {
		return ProcessView{}, ErrProcessInvalid
	}
	var result ProcessView
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		insert := transaction.Exec(`
INSERT INTO sandbox_runtime_processes (
  id, schema_version, project_id, session_id, session_epoch,
  session_version_at_creation, actor_id, service_id, command_id,
  template_release_id, template_release_content_hash,
  working_directory, argv, log_limit_bytes,
  state, version, log_bytes, log_truncated
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, 'starting', 1, 0, false)
`, input.ID, RuntimeProcessSchemaVersion, input.ProjectID, input.SessionID, int64(input.SessionEpoch),
			int64(input.SessionVersionAtCreation), input.ActorID,
			input.Command.ServiceID, input.Command.CommandID,
			input.Command.TemplateRelease.ID, input.Command.TemplateRelease.ContentHash,
			input.Command.WorkingDirectory, string(argv), input.LogLimitBytes)
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected != 1 {
			return processIntegrityError("create", fmt.Errorf("insert affected %d rows", insert.RowsAffected))
		}
		row, loadErr := loadSandboxProcessRow(transaction, input.ProjectID, input.SessionID, input.ID, false)
		if loadErr != nil {
			return loadErr
		}
		result, loadErr = hydrateSandboxProcess(row)
		return loadErr
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ProcessView{}, mapProcessStoreError("create", err)
	}
	return result, nil
}

func (store *PostgresProcessStore) Get(
	ctx context.Context,
	projectID, sessionID, processID string,
) (ProcessView, error) {
	if err := validateProcessIdentity(ctx, projectID, sessionID, processID); err != nil {
		return ProcessView{}, err
	}
	row, err := loadSandboxProcessRow(store.database.WithContext(ctx), projectID, sessionID, processID, false)
	if err != nil {
		return ProcessView{}, mapProcessStoreError("get", err)
	}
	result, err := hydrateSandboxProcess(row)
	if err != nil {
		return ProcessView{}, mapProcessStoreError("get", err)
	}
	return result, nil
}

func (store *PostgresProcessStore) List(
	ctx context.Context,
	projectID, sessionID string,
	limit int,
) ([]ProcessView, error) {
	if ctx == nil || !validUUID(projectID) || !validUUID(sessionID) {
		return nil, ErrProcessInvalid
	}
	if limit == 0 {
		limit = defaultProcessListLimit
	}
	if limit < 1 || limit > defaultProcessListLimit {
		return nil, ErrProcessInvalid
	}
	var rows []sandboxProcessRow
	err := store.database.WithContext(ctx).
		Where("project_id = ? AND session_id = ?", projectID, sessionID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, mapProcessStoreError("list", err)
	}
	result := make([]ProcessView, len(rows))
	for index, row := range rows {
		result[index], err = hydrateSandboxProcess(row)
		if err != nil {
			return nil, mapProcessStoreError("list", err)
		}
	}
	return result, nil
}

func (store *PostgresProcessStore) Observe(
	ctx context.Context,
	input ProcessObservation,
) (ProcessView, error) {
	if err := validateProcessObservation(ctx, input); err != nil {
		return ProcessView{}, err
	}
	var result ProcessView
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		row, loadErr := loadSandboxProcessRow(
			transaction, input.ProjectID, input.SessionID, input.ProcessID, true,
		)
		if loadErr != nil {
			return loadErr
		}
		if row.SessionEpoch != int64(input.ExpectedSessionEpoch) {
			return ErrEpochFenced
		}
		if row.Version != int64(input.ExpectedProcessVersion) {
			return ErrProcessVersionConflict
		}
		current, hydrateErr := hydrateSandboxProcess(row)
		if hydrateErr != nil {
			return hydrateErr
		}
		if !runtimeStatusMatchesExactProcess(input.Status, current) {
			return ErrRuntimeConflict
		}
		if input.Status.State == ProcessStarting.String() {
			if current.State == ProcessStarting {
				result = current
				return nil
			}
			return ErrProcessInvalidTransition
		}
		if !processActive(current.State) {
			if runtimeStatusMatchesProcess(input.Status, current) {
				result = current
				return nil
			}
			return ErrProcessInvalidTransition
		}
		if insertErr := appendSandboxProcessEvent(transaction, row, input); insertErr != nil {
			return insertErr
		}
		updated, loadErr := loadSandboxProcessRow(
			transaction, input.ProjectID, input.SessionID, input.ProcessID, false,
		)
		if loadErr != nil {
			return loadErr
		}
		result, loadErr = hydrateSandboxProcess(updated)
		return loadErr
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ProcessView{}, mapProcessStoreError("observe", err)
	}
	return result, nil
}

func (store *PostgresProcessStore) FenceEpoch(
	ctx context.Context,
	projectID, sessionID string,
	sessionEpoch uint64,
	actorID, reason string,
) ([]ProcessView, error) {
	reason = strings.TrimSpace(reason)
	if ctx == nil || !validUUID(projectID) || !validUUID(sessionID) || !validUUID(actorID) ||
		sessionEpoch == 0 || sessionEpoch > math.MaxInt64 || reason == "" || len(reason) > 1000 {
		return nil, ErrProcessInvalid
	}
	var result []ProcessView
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var rows []sandboxProcessRow
		if queryErr := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND session_id = ? AND session_epoch = ? AND state IN ?",
				projectID, sessionID, int64(sessionEpoch), []string{ProcessStarting.String(), ProcessRunning.String()}).
			Order("created_at, id").Find(&rows).Error; queryErr != nil {
			return queryErr
		}
		for _, row := range rows {
			startedAt := row.CreatedAt
			if row.RuntimeStartedAt.Valid {
				startedAt = row.RuntimeStartedAt.Time
			}
			finishedAt := time.Now().UTC()
			if finishedAt.Before(startedAt) {
				finishedAt = startedAt
			}
			status := RuntimeProcessStatus{
				SchemaVersion: RuntimeProcessSchemaVersion, ID: row.ID,
				State: ProcessOrphaned.String(), PID: int(row.PID.Int64),
				WorkingDirectory: row.WorkingDirectory,
				LogBytes:         row.LogBytes, LogTruncated: row.LogTruncated,
				Failure: reason, StartedAt: startedAt, FinishedAt: finishedAt,
			}
			if decodeErr := json.Unmarshal(row.Argv, &status.Argv); decodeErr != nil {
				return processIntegrityError("fence epoch", decodeErr)
			}
			observation := ProcessObservation{
				ProjectID: projectID, SessionID: sessionID, ProcessID: row.ID,
				ActorID: actorID, ExpectedProcessVersion: uint64(row.Version),
				ExpectedSessionEpoch: sessionEpoch, EventKind: "epoch.fenced",
				Reason: reason, Status: status,
			}
			if eventErr := appendSandboxProcessEvent(transaction, row, observation); eventErr != nil {
				return eventErr
			}
			updated, loadErr := loadSandboxProcessRow(transaction, projectID, sessionID, row.ID, false)
			if loadErr != nil {
				return loadErr
			}
			view, hydrateErr := hydrateSandboxProcess(updated)
			if hydrateErr != nil {
				return hydrateErr
			}
			result = append(result, view)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, mapProcessStoreError("fence epoch", err)
	}
	return result, nil
}

func appendSandboxProcessEvent(
	transaction *gorm.DB,
	row sandboxProcessRow,
	input ProcessObservation,
) error {
	status := input.Status
	var pid any
	if status.PID >= 2 {
		pid = status.PID
	}
	var exitCode any
	if status.ExitCode != nil {
		exitCode = *status.ExitCode
	}
	var failure any
	if status.Failure != "" {
		failure = status.Failure
	}
	var startedAt any
	if !status.StartedAt.IsZero() {
		startedAt = status.StartedAt.UTC()
	}
	var finishedAt any
	if !status.FinishedAt.IsZero() {
		finishedAt = status.FinishedAt.UTC()
	}
	var signal any
	if input.Signal != "" {
		signal = input.Signal
	}
	insert := transaction.Exec(`
INSERT INTO sandbox_runtime_process_events (
  process_id, sequence, process_version_from, process_version_to,
  session_epoch, event_kind, state_from, state_to,
  actor_id, signal, reason,
  pid_to, exit_code_to, failure_to, log_bytes_to, log_truncated_to,
  runtime_started_at_to, finished_at_to
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, row.ID, row.Version, row.Version, row.Version+1,
		row.SessionEpoch, input.EventKind, row.State, status.State,
		input.ActorID, signal, input.Reason,
		pid, exitCode, failure, status.LogBytes, status.LogTruncated, startedAt, finishedAt)
	if insert.Error != nil {
		return insert.Error
	}
	if insert.RowsAffected != 1 {
		return processIntegrityError("append event", fmt.Errorf("insert affected %d rows", insert.RowsAffected))
	}
	return nil
}

func loadSandboxProcessRow(
	database *gorm.DB,
	projectID, sessionID, processID string,
	lock bool,
) (sandboxProcessRow, error) {
	var row sandboxProcessRow
	query := database.Where("project_id = ? AND session_id = ? AND id = ?", projectID, sessionID, processID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sandboxProcessRow{}, ErrProcessNotFound
	}
	if err != nil {
		return sandboxProcessRow{}, err
	}
	return row, nil
}

func hydrateSandboxProcess(row sandboxProcessRow) (ProcessView, error) {
	if row.SessionEpoch <= 0 || row.SessionVersionAtCreation <= 0 || row.Version <= 0 {
		return ProcessView{}, processIntegrityError("hydrate", errors.New("integer projection is outside domain bounds"))
	}
	var argv []string
	if err := json.Unmarshal(row.Argv, &argv); err != nil {
		return ProcessView{}, processIntegrityError("hydrate", err)
	}
	value := ProcessView{
		SchemaVersion: row.SchemaVersion, ID: row.ID, ProjectID: row.ProjectID, SessionID: row.SessionID,
		SessionEpoch: uint64(row.SessionEpoch), SessionVersionAtCreation: uint64(row.SessionVersionAtCreation),
		ActorID: row.ActorID, ServiceID: row.ServiceID, CommandID: row.CommandID,
		TemplateRelease:  repositoryExactReference(row.TemplateReleaseID, row.TemplateReleaseHash),
		WorkingDirectory: row.WorkingDirectory, Argv: argv, LogLimitBytes: row.LogLimitBytes,
		State: ProcessState(row.State), Version: uint64(row.Version),
		PID: int(row.PID.Int64), Failure: row.Failure.String,
		LogBytes: row.LogBytes, LogTruncated: row.LogTruncated,
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
	if row.ExitCode.Valid {
		exitCode := int(row.ExitCode.Int64)
		value.ExitCode = &exitCode
	}
	if row.RuntimeStartedAt.Valid {
		startedAt := row.RuntimeStartedAt.Time.UTC()
		value.RuntimeStartedAt = &startedAt
	}
	if row.FinishedAt.Valid {
		finishedAt := row.FinishedAt.Time.UTC()
		value.FinishedAt = &finishedAt
	}
	if err := validateProcessView(value); err != nil {
		return ProcessView{}, processIntegrityError("hydrate", err)
	}
	return value, nil
}

func repositoryExactReference(id, contentHash string) repository.ExactReference {
	return repository.ExactReference{ID: id, ContentHash: contentHash}
}

func validateProcessRecordInput(ctx context.Context, input ProcessRecordInput) error {
	if ctx == nil || !validUUID(input.ID) || !validUUID(input.ProjectID) || !validUUID(input.SessionID) ||
		!validUUID(input.ActorID) || input.SessionEpoch == 0 || input.SessionEpoch > math.MaxInt64 ||
		input.SessionVersionAtCreation == 0 || input.SessionVersionAtCreation > math.MaxInt64 ||
		!slugPattern.MatchString(input.Command.ServiceID) || !slugPattern.MatchString(input.Command.CommandID) ||
		validateExactReference(input.Command.TemplateRelease) != nil ||
		!validProcessWorkingDirectory(input.Command.WorkingDirectory) ||
		len(input.Command.Argv) == 0 || len(input.Command.Argv) > 64 ||
		input.LogLimitBytes < 1 || input.LogLimitBytes > 64<<20 {
		return ErrProcessInvalid
	}
	for _, argument := range input.Command.Argv {
		if strings.TrimSpace(argument) == "" || len(argument) > 4096 || strings.ContainsAny(argument, "\x00\r\n") {
			return ErrProcessInvalid
		}
	}
	return nil
}

func validateProcessIdentity(ctx context.Context, projectID, sessionID, processID string) error {
	if ctx == nil || !validUUID(projectID) || !validUUID(sessionID) || !validUUID(processID) {
		return ErrProcessInvalid
	}
	return nil
}

func validateProcessObservation(ctx context.Context, input ProcessObservation) error {
	input.EventKind = strings.TrimSpace(input.EventKind)
	input.Signal = strings.ToUpper(strings.TrimSpace(input.Signal))
	input.Reason = strings.TrimSpace(input.Reason)
	if err := validateProcessIdentity(ctx, input.ProjectID, input.SessionID, input.ProcessID); err != nil ||
		!validUUID(input.ActorID) || input.ExpectedProcessVersion == 0 ||
		input.ExpectedProcessVersion > math.MaxInt64 || input.ExpectedSessionEpoch == 0 ||
		input.ExpectedSessionEpoch > math.MaxInt64 || input.Reason == "" || len(input.Reason) > 1000 ||
		(input.EventKind != "runtime.observed" && input.EventKind != "start.failed" &&
			input.EventKind != "signal.sent" && input.EventKind != "epoch.fenced") {
		return ErrProcessInvalid
	}
	if input.EventKind == "signal.sent" {
		if input.Signal != "INT" && input.Signal != "TERM" && input.Signal != "KILL" && input.Signal != "HUP" {
			return ErrProcessInvalid
		}
	} else if input.Signal != "" {
		return ErrProcessInvalid
	}
	if input.EventKind != "epoch.fenced" {
		if err := validateRuntimeProcessStatus(input.Status); err != nil {
			return err
		}
	} else if input.Status.State != ProcessOrphaned.String() || input.Status.FinishedAt.IsZero() ||
		strings.TrimSpace(input.Status.Failure) == "" || input.Status.ExitCode != nil {
		return ErrProcessInvalid
	}
	return nil
}

func runtimeStatusMatchesExactProcess(status RuntimeProcessStatus, process ProcessView) bool {
	if status.ID != process.ID || status.WorkingDirectory != process.WorkingDirectory ||
		len(status.Argv) != len(process.Argv) || status.LogBytes < 0 || status.LogBytes > process.LogLimitBytes {
		return false
	}
	for index := range status.Argv {
		if status.Argv[index] != process.Argv[index] {
			return false
		}
	}
	return true
}

type processStoreError struct {
	operation string
	kind      error
	cause     error
}

func (err *processStoreError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("sandbox process persistence %s: %v: %v", err.operation, err.kind, err.cause)
}

func (err *processStoreError) Unwrap() []error {
	if err == nil {
		return nil
	}
	return []error{err.kind, err.cause}
}

func processIntegrityError(operation string, cause error) error {
	return &processStoreError{operation: operation, kind: ErrProcessStoreIntegrity, cause: cause}
}

func mapProcessStoreError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var persisted *processStoreError
	if errors.As(err, &persisted) {
		return err
	}
	for _, known := range []error{
		ErrProcessInvalid, ErrProcessNotFound, ErrProcessExists, ErrProcessVersionConflict,
		ErrProcessInvalidTransition, ErrEpochFenced, ErrRuntimeConflict,
	} {
		if errors.Is(err, known) {
			return err
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		message := strings.ToLower(postgres.Message)
		switch {
		case postgres.Code == "23505" && operation == "create":
			return &processStoreError{operation: operation, kind: ErrProcessExists, cause: err}
		case strings.Contains(message, "version or epoch fence") ||
			strings.Contains(message, "exact ready session version and epoch") || postgres.Code == "40001":
			return &processStoreError{operation: operation, kind: ErrProcessVersionConflict, cause: err}
		case strings.Contains(message, "invalid sandbox process state transition"):
			return &processStoreError{operation: operation, kind: ErrProcessInvalidTransition, cause: err}
		case strings.HasPrefix(postgres.Code, "08") || postgres.Code == "57P01" ||
			postgres.Code == "57P02" || postgres.Code == "57P03":
			return &processStoreError{operation: operation, kind: ErrProcessStoreUnavailable, cause: err}
		default:
			return &processStoreError{operation: operation, kind: ErrProcessStoreIntegrity, cause: err}
		}
	}
	return &processStoreError{operation: operation, kind: ErrProcessStoreUnavailable, cause: err}
}

var _ SandboxProcessStore = (*PostgresProcessStore)(nil)
