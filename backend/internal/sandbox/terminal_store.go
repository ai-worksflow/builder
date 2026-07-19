package sandbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const defaultTerminalListLimit = 100

type PostgresTerminalStore struct {
	database *gorm.DB
}

func NewPostgresTerminalStore(database *gorm.DB) (*PostgresTerminalStore, error) {
	if database == nil {
		return nil, ErrTerminalStoreUnavailable
	}
	return &PostgresTerminalStore{database: database}, nil
}

type sandboxTerminalRow struct {
	ID                       string         `gorm:"column:id"`
	SchemaVersion            string         `gorm:"column:schema_version"`
	ProjectID                string         `gorm:"column:project_id"`
	SessionID                string         `gorm:"column:session_id"`
	SessionEpoch             int64          `gorm:"column:session_epoch"`
	SessionVersionAtCreation int64          `gorm:"column:session_version_at_creation"`
	ActorID                  string         `gorm:"column:actor_id"`
	WorkingDirectory         string         `gorm:"column:working_directory"`
	ShellPath                string         `gorm:"column:shell_path"`
	Rows                     int            `gorm:"column:rows"`
	Columns                  int            `gorm:"column:columns"`
	OutputLimitBytes         int64          `gorm:"column:output_limit_bytes"`
	State                    string         `gorm:"column:state"`
	Version                  int64          `gorm:"column:version"`
	ExitCode                 sql.NullInt64  `gorm:"column:exit_code"`
	Failure                  sql.NullString `gorm:"column:failure"`
	OutputBytes              int64          `gorm:"column:output_bytes"`
	OutputTruncated          bool           `gorm:"column:output_truncated"`
	RuntimeStartedAt         sql.NullTime   `gorm:"column:runtime_started_at"`
	FinishedAt               sql.NullTime   `gorm:"column:finished_at"`
	CreatedAt                time.Time      `gorm:"column:created_at"`
	UpdatedAt                time.Time      `gorm:"column:updated_at"`
}

func (sandboxTerminalRow) TableName() string { return "sandbox_terminals" }

func (store *PostgresTerminalStore) Create(
	ctx context.Context,
	input TerminalRecordInput,
) (TerminalView, error) {
	if err := validateTerminalRecordInput(ctx, input); err != nil {
		return TerminalView{}, err
	}
	var result TerminalView
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		insert := transaction.Exec(`
INSERT INTO sandbox_terminals (
  id, schema_version, project_id, session_id, session_epoch,
  session_version_at_creation, actor_id, working_directory, shell_path,
  rows, columns, output_limit_bytes, state, version, output_bytes, output_truncated
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '/bin/bash', ?, ?, ?, 'opening', 1, 0, false)
`, input.ID, TerminalSchemaVersion, input.ProjectID, input.SessionID, int64(input.SessionEpoch),
			int64(input.SessionVersionAtCreation), input.ActorID, input.WorkingDirectory,
			int(input.Rows), int(input.Columns), input.OutputLimitBytes)
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected != 1 {
			return terminalIntegrityError("create", fmt.Errorf("insert affected %d rows", insert.RowsAffected))
		}
		row, loadErr := loadSandboxTerminalRow(transaction, input.ProjectID, input.SessionID, input.ID, false)
		if loadErr != nil {
			return loadErr
		}
		result, loadErr = hydrateSandboxTerminal(row)
		return loadErr
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return TerminalView{}, mapTerminalStoreError("create", err)
	}
	return result, nil
}

func (store *PostgresTerminalStore) Get(
	ctx context.Context,
	projectID, sessionID, terminalID string,
) (TerminalView, error) {
	if err := validateTerminalIdentity(ctx, projectID, sessionID, terminalID); err != nil {
		return TerminalView{}, err
	}
	row, err := loadSandboxTerminalRow(store.database.WithContext(ctx), projectID, sessionID, terminalID, false)
	if err != nil {
		return TerminalView{}, mapTerminalStoreError("get", err)
	}
	result, err := hydrateSandboxTerminal(row)
	if err != nil {
		return TerminalView{}, terminalIntegrityError("get", err)
	}
	return result, nil
}

func (store *PostgresTerminalStore) List(
	ctx context.Context,
	projectID, sessionID string,
	limit int,
) ([]TerminalView, error) {
	if ctx == nil || !validUUID(projectID) || !validUUID(sessionID) || limit < 0 || limit > 100 {
		return nil, ErrTerminalInvalid
	}
	if limit == 0 {
		limit = defaultTerminalListLimit
	}
	var rows []sandboxTerminalRow
	err := store.database.WithContext(ctx).
		Where("project_id = ? AND session_id = ?", projectID, sessionID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, mapTerminalStoreError("list", err)
	}
	result := make([]TerminalView, 0, len(rows))
	for _, row := range rows {
		terminal, hydrateErr := hydrateSandboxTerminal(row)
		if hydrateErr != nil {
			return nil, terminalIntegrityError("list", hydrateErr)
		}
		result = append(result, terminal)
	}
	return result, nil
}

func (store *PostgresTerminalStore) Transition(
	ctx context.Context,
	input TerminalTransitionInput,
) (TerminalView, error) {
	if err := validateTerminalTransitionInput(ctx, input); err != nil {
		return TerminalView{}, err
	}
	var result TerminalView
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		row, err := loadSandboxTerminalRow(transaction, input.ProjectID, input.SessionID, input.TerminalID, true)
		if err != nil {
			return err
		}
		current, err := hydrateSandboxTerminal(row)
		if err != nil {
			return terminalIntegrityError("transition", err)
		}
		if current.SessionEpoch != input.SessionEpoch {
			return ErrEpochFenced
		}
		if current.Version != input.ExpectedVersion {
			return ErrTerminalVersionConflict
		}
		if err := insertTerminalEvent(transaction, current, input); err != nil {
			return err
		}
		advanced, err := loadSandboxTerminalRow(transaction, input.ProjectID, input.SessionID, input.TerminalID, false)
		if err != nil {
			return err
		}
		result, err = hydrateSandboxTerminal(advanced)
		if err != nil || result.Version != current.Version+1 || result.State != input.State {
			return terminalIntegrityError("transition", errors.New("event did not advance exact terminal projection"))
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return TerminalView{}, mapTerminalStoreError("transition", err)
	}
	return result, nil
}

func (store *PostgresTerminalStore) FenceEpoch(
	ctx context.Context,
	projectID, sessionID string,
	sessionEpoch uint64,
	actorID, reason string,
) ([]TerminalView, error) {
	reason = strings.TrimSpace(reason)
	if ctx == nil || !validUUID(projectID) || !validUUID(sessionID) || sessionEpoch == 0 ||
		!validUUID(actorID) || reason == "" || len(reason) > 1000 {
		return nil, ErrTerminalInvalid
	}
	var result []TerminalView
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var rows []sandboxTerminalRow
		err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND session_id = ? AND session_epoch = ? AND state IN ?",
				projectID, sessionID, int64(sessionEpoch), []string{string(TerminalOpening), string(TerminalRunning)}).
			Order("created_at, id").Find(&rows).Error
		if err != nil {
			return err
		}
		result = make([]TerminalView, 0, len(rows))
		for _, row := range rows {
			current, hydrateErr := hydrateSandboxTerminal(row)
			if hydrateErr != nil {
				return terminalIntegrityError("fence", hydrateErr)
			}
			finished := time.Now().UTC()
			started := current.RuntimeStartedAt
			if started == nil {
				value := current.CreatedAt
				started = &value
			}
			input := terminalTransition(
				current, actorID, "", "epoch.fenced", reason, TerminalOrphaned,
				current.Rows, current.Columns, nil, reason,
				current.OutputBytes, current.OutputTruncated, started, &finished,
			)
			if err := insertTerminalEvent(transaction, current, input); err != nil {
				return err
			}
			advanced, loadErr := loadSandboxTerminalRow(transaction, projectID, sessionID, current.ID, false)
			if loadErr != nil {
				return loadErr
			}
			value, hydrateErr := hydrateSandboxTerminal(advanced)
			if hydrateErr != nil {
				return terminalIntegrityError("fence", hydrateErr)
			}
			result = append(result, value)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, mapTerminalStoreError("fence", err)
	}
	return result, nil
}

func insertTerminalEvent(transaction *gorm.DB, current TerminalView, input TerminalTransitionInput) error {
	insert := transaction.Exec(`
INSERT INTO sandbox_terminal_events (
  terminal_id, sequence, terminal_version_from, terminal_version_to,
  session_epoch, event_kind, state_from, state_to, actor_id, request_id,
  signal, reason, rows_to, columns_to, exit_code_to, failure_to,
  output_bytes_to, output_truncated_to, runtime_started_at_to, finished_at_to
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, '')::uuid, NULLIF(?, ''), ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?)
`, current.ID, int64(current.Version), int64(current.Version), int64(current.Version+1),
		int64(current.SessionEpoch), input.EventKind, string(current.State), string(input.State), input.ActorID,
		input.RequestID, input.Signal, input.Reason, int(input.Rows), int(input.Columns), nullableInt(input.ExitCode),
		input.Failure, input.OutputBytes, input.OutputTruncated, nullableTime(input.RuntimeStartedAt), nullableTime(input.FinishedAt))
	if insert.Error != nil {
		return insert.Error
	}
	if insert.RowsAffected != 1 {
		return terminalIntegrityError("transition", fmt.Errorf("event insert affected %d rows", insert.RowsAffected))
	}
	return nil
}

func loadSandboxTerminalRow(
	database *gorm.DB,
	projectID, sessionID, terminalID string,
	lock bool,
) (sandboxTerminalRow, error) {
	query := database.Where("project_id = ? AND session_id = ? AND id = ?", projectID, sessionID, terminalID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row sandboxTerminalRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return sandboxTerminalRow{}, ErrTerminalNotFound
		}
		return sandboxTerminalRow{}, err
	}
	return row, nil
}

func hydrateSandboxTerminal(row sandboxTerminalRow) (TerminalView, error) {
	if row.SchemaVersion != TerminalSchemaVersion || !validUUID(row.ID) || !validUUID(row.ProjectID) ||
		!validUUID(row.SessionID) || !validUUID(row.ActorID) || row.SessionEpoch < 1 ||
		row.SessionVersionAtCreation < 1 || row.Version < 1 || row.ShellPath != "/bin/bash" ||
		row.Rows < 2 || row.Rows > 500 || row.Columns < 2 || row.Columns > 500 ||
		row.OutputLimitBytes < 1024 || row.OutputLimitBytes > 64<<20 || row.OutputBytes < 0 ||
		row.OutputBytes > row.OutputLimitBytes || row.CreatedAt.IsZero() || row.UpdatedAt.Before(row.CreatedAt) {
		return TerminalView{}, ErrTerminalStoreIntegrity
	}
	state := TerminalState(row.State)
	if state != TerminalOpening && state != TerminalRunning && state != TerminalExited &&
		state != TerminalFailed && state != TerminalOrphaned {
		return TerminalView{}, ErrTerminalStoreIntegrity
	}
	result := TerminalView{
		SchemaVersion: row.SchemaVersion, ID: row.ID, ProjectID: row.ProjectID, SessionID: row.SessionID,
		SessionEpoch: uint64(row.SessionEpoch), SessionVersionAtCreation: uint64(row.SessionVersionAtCreation),
		ActorID: row.ActorID, WorkingDirectory: row.WorkingDirectory, ShellPath: row.ShellPath,
		Rows: uint16(row.Rows), Columns: uint16(row.Columns), OutputLimitBytes: row.OutputLimitBytes,
		State: state, Version: uint64(row.Version), Failure: row.Failure.String,
		OutputBytes: row.OutputBytes, OutputTruncated: row.OutputTruncated,
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
	if row.ExitCode.Valid {
		value := int(row.ExitCode.Int64)
		result.ExitCode = &value
	}
	if row.RuntimeStartedAt.Valid {
		value := row.RuntimeStartedAt.Time.UTC()
		result.RuntimeStartedAt = &value
	}
	if row.FinishedAt.Valid {
		value := row.FinishedAt.Time.UTC()
		result.FinishedAt = &value
	}
	return result, nil
}

func validateTerminalRecordInput(ctx context.Context, input TerminalRecordInput) error {
	if ctx == nil || !validUUID(input.ID) || !validUUID(input.ProjectID) || !validUUID(input.SessionID) ||
		!validUUID(input.ActorID) || input.SessionEpoch == 0 || input.SessionEpoch > math.MaxInt64 ||
		input.SessionVersionAtCreation == 0 || input.SessionVersionAtCreation > math.MaxInt64 ||
		!validTerminalSize(input.Rows, input.Columns) || input.OutputLimitBytes < 1024 || input.OutputLimitBytes > 64<<20 {
		return ErrTerminalInvalid
	}
	directory := strings.TrimSpace(input.WorkingDirectory)
	if directory == "" || len(directory) > 512 || path.IsAbs(directory) || path.Clean(directory) != directory ||
		directory == ".." || strings.HasPrefix(directory, "../") || strings.ContainsAny(directory, "\\\x00\r\n") {
		return ErrTerminalInvalid
	}
	return nil
}

func validateTerminalIdentity(ctx context.Context, projectID, sessionID, terminalID string) error {
	if ctx == nil || !validUUID(projectID) || !validUUID(sessionID) || !validUUID(terminalID) {
		return ErrTerminalInvalid
	}
	return nil
}

func validateTerminalTransitionInput(ctx context.Context, input TerminalTransitionInput) error {
	input.EventKind = strings.TrimSpace(input.EventKind)
	input.Reason = strings.TrimSpace(input.Reason)
	input.Signal = strings.ToUpper(strings.TrimSpace(input.Signal))
	if err := validateTerminalIdentity(ctx, input.ProjectID, input.SessionID, input.TerminalID); err != nil ||
		input.SessionEpoch == 0 || input.SessionEpoch > math.MaxInt64 || input.ExpectedVersion == 0 ||
		input.ExpectedVersion > math.MaxInt64 || !validUUID(input.ActorID) ||
		(input.RequestID != "" && !validUUID(input.RequestID)) || input.Reason == "" || len(input.Reason) > 1000 ||
		!validTerminalSize(input.Rows, input.Columns) || input.OutputBytes < 0 || input.OutputBytes > 64<<20 ||
		len(input.Failure) > 1000 {
		return ErrTerminalInvalid
	}
	if input.EventKind != "runtime.opened" && input.EventKind != "runtime.failed" &&
		input.EventKind != "runtime.exited" && input.EventKind != "attached" &&
		input.EventKind != "detached" && input.EventKind != "resized" &&
		input.EventKind != "signal.sent" && input.EventKind != "close.sent" && input.EventKind != "epoch.fenced" {
		return ErrTerminalInvalid
	}
	if input.EventKind == "signal.sent" {
		if !validTerminalSignal(input.Signal) {
			return ErrTerminalInvalid
		}
	} else if input.Signal != "" {
		return ErrTerminalInvalid
	}
	return nil
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

type terminalStoreError struct {
	operation string
	kind      error
	cause     error
}

func (err *terminalStoreError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("sandbox terminal persistence %s: %v: %v", err.operation, err.kind, err.cause)
}

func (err *terminalStoreError) Unwrap() []error {
	if err == nil {
		return nil
	}
	return []error{err.kind, err.cause}
}

func terminalIntegrityError(operation string, cause error) error {
	return &terminalStoreError{operation: operation, kind: ErrTerminalStoreIntegrity, cause: cause}
}

func mapTerminalStoreError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var persisted *terminalStoreError
	if errors.As(err, &persisted) {
		return err
	}
	for _, known := range []error{
		ErrTerminalInvalid, ErrTerminalNotFound, ErrTerminalExists, ErrTerminalVersionConflict,
		ErrTerminalInvalidTransition, ErrEpochFenced,
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
			return &terminalStoreError{operation: operation, kind: ErrTerminalExists, cause: err}
		case strings.Contains(message, "version, epoch") || strings.Contains(message, "exact ready session version and epoch") || postgres.Code == "40001":
			return &terminalStoreError{operation: operation, kind: ErrTerminalVersionConflict, cause: err}
		case strings.Contains(message, "invalid sandbox terminal state transition"):
			return &terminalStoreError{operation: operation, kind: ErrTerminalInvalidTransition, cause: err}
		case strings.HasPrefix(postgres.Code, "08") || postgres.Code == "57P01" ||
			postgres.Code == "57P02" || postgres.Code == "57P03":
			return &terminalStoreError{operation: operation, kind: ErrTerminalStoreUnavailable, cause: err}
		default:
			return &terminalStoreError{operation: operation, kind: ErrTerminalStoreIntegrity, cause: err}
		}
	}
	return &terminalStoreError{operation: operation, kind: ErrTerminalStoreUnavailable, cause: err}
}

var _ SandboxTerminalStore = (*PostgresTerminalStore)(nil)
