package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrPlanNotFound           = errors.New("agent TaskCapsule plan was not found")
	ErrAttemptNotFound        = errors.New("agent Attempt was not found")
	ErrAgentOperationReplay   = errors.New("agent operation id was already committed with different input")
	ErrAttemptVersionConflict = errors.New("agent Attempt version changed")
	ErrAgentStoreIntegrity    = errors.New("agent persistence integrity failure")
	ErrAgentStoreUnavailable  = errors.New("agent persistence is unavailable")
)

var agentOperationPattern = regexp.MustCompile(`^[A-Za-z0-9._:~-]{1,128}$`)

// TaskPlan is the immutable, exact-tree input sealed before an AgentAttempt is
// allowed to enter the queue.
type TaskPlan struct {
	ContextPack ContextPack `json:"contextPack"`
	TaskCapsule TaskCapsule `json:"taskCapsule"`
	Replayed    bool        `json:"replayed"`
}

// PostgresStore is the sole SQL writer for the Stage 2 agent control plane.
// ContextPack and TaskCapsule rows are immutable; Attempt mutations append one
// event and let PostgreSQL project it under an exact version/fence CAS.
type PostgresStore struct {
	database *gorm.DB
}

func NewPostgresStore(database *gorm.DB) (*PostgresStore, error) {
	if database == nil {
		return nil, fmt.Errorf("%w: database is required", ErrAgentStoreUnavailable)
	}
	return &PostgresStore{database: database}, nil
}

type contextPackRow struct {
	ID                    string          `gorm:"column:id"`
	SchemaVersion         string          `gorm:"column:schema_version"`
	ProjectID             string          `gorm:"column:project_id"`
	CandidateID           string          `gorm:"column:candidate_id"`
	BaseCandidateTreeHash string          `gorm:"column:base_candidate_tree_hash"`
	BuildContractID       string          `gorm:"column:build_contract_id"`
	BuildContractHash     string          `gorm:"column:build_contract_hash"`
	Items                 json.RawMessage `gorm:"column:items"`
	ContentHash           string          `gorm:"column:content_hash"`
	CreatedBy             string          `gorm:"column:created_by"`
	CreatedAt             time.Time       `gorm:"column:created_at"`
}

func (contextPackRow) TableName() string { return "agent_context_packs" }

type taskCapsuleRow struct {
	ID                        string          `gorm:"column:id"`
	SchemaVersion             string          `gorm:"column:schema_version"`
	TaskKey                   string          `gorm:"column:task_key"`
	ProjectID                 string          `gorm:"column:project_id"`
	SandboxSessionID          string          `gorm:"column:sandbox_session_id"`
	CandidateID               string          `gorm:"column:candidate_id"`
	CandidateVersion          int64           `gorm:"column:candidate_version"`
	CandidateSessionEpoch     int64           `gorm:"column:candidate_session_epoch"`
	CandidateWriterLeaseEpoch int64           `gorm:"column:candidate_writer_lease_epoch"`
	BaseCandidateTreeHash     string          `gorm:"column:base_candidate_tree_hash"`
	BuildContractID           string          `gorm:"column:build_contract_id"`
	BuildContractHash         string          `gorm:"column:build_contract_hash"`
	TemplateReleases          json.RawMessage `gorm:"column:template_releases"`
	ContextPackID             string          `gorm:"column:context_pack_id"`
	ContextPackHash           string          `gorm:"column:context_pack_hash"`
	Objective                 string          `gorm:"column:objective"`
	ObligationIDs             json.RawMessage `gorm:"column:obligation_ids"`
	AcceptanceCriterionIDs    json.RawMessage `gorm:"column:acceptance_criterion_ids"`
	ReadSet                   json.RawMessage `gorm:"column:read_set"`
	WriteSet                  json.RawMessage `gorm:"column:write_set"`
	ProtectedPaths            json.RawMessage `gorm:"column:protected_paths"`
	Preconditions             json.RawMessage `gorm:"column:preconditions"`
	Postconditions            json.RawMessage `gorm:"column:postconditions"`
	VerificationCommandIDs    json.RawMessage `gorm:"column:verification_command_ids"`
	AllowedTools              json.RawMessage `gorm:"column:allowed_tools"`
	NetworkPolicy             json.RawMessage `gorm:"column:network_policy"`
	Budgets                   json.RawMessage `gorm:"column:budgets"`
	OutputSchemaHash          string          `gorm:"column:output_schema_hash"`
	ContentHash               string          `gorm:"column:content_hash"`
	CreatedBy                 string          `gorm:"column:created_by"`
	CreatedAt                 time.Time       `gorm:"column:created_at"`
}

func (taskCapsuleRow) TableName() string { return "agent_task_capsules" }

type attemptRow struct {
	ID                    string          `gorm:"column:id"`
	SchemaVersion         string          `gorm:"column:schema_version"`
	OperationID           string          `gorm:"column:operation_id"`
	ProjectID             string          `gorm:"column:project_id"`
	SandboxSessionID      string          `gorm:"column:sandbox_session_id"`
	CandidateID           string          `gorm:"column:candidate_id"`
	TaskCapsuleID         string          `gorm:"column:task_capsule_id"`
	TaskCapsuleHash       string          `gorm:"column:task_capsule_hash"`
	ContextPackID         string          `gorm:"column:context_pack_id"`
	ContextPackHash       string          `gorm:"column:context_pack_hash"`
	BaseCandidateTreeHash string          `gorm:"column:base_candidate_tree_hash"`
	BuildContractHash     string          `gorm:"column:build_contract_hash"`
	Executor              json.RawMessage `gorm:"column:executor"`
	RequestKeyHash        string          `gorm:"column:request_key_hash"`
	ConfigurationHash     string          `gorm:"column:configuration_hash"`
	ParentAttemptID       sql.NullString  `gorm:"column:parent_attempt_id"`
	RetryReason           sql.NullString  `gorm:"column:retry_reason"`
	State                 string          `gorm:"column:state"`
	Version               int64           `gorm:"column:version"`
	FenceEpoch            int64           `gorm:"column:fence_epoch"`
	LeaseWorkerID         sql.NullString  `gorm:"column:lease_worker_id"`
	LeaseEpoch            sql.NullInt64   `gorm:"column:lease_epoch"`
	LeaseExpiresAt        sql.NullTime    `gorm:"column:lease_expires_at"`
	Evidence              json.RawMessage `gorm:"column:evidence"`
	ExitReason            sql.NullString  `gorm:"column:exit_reason"`
	CreatedBy             string          `gorm:"column:created_by"`
	CreatedAt             time.Time       `gorm:"column:created_at"`
	StartedAt             sql.NullTime    `gorm:"column:started_at"`
	FinishedAt            sql.NullTime    `gorm:"column:finished_at"`
	UpdatedAt             time.Time       `gorm:"column:updated_at"`
}

func (attemptRow) TableName() string { return "agent_attempts" }

type attemptEventRow struct {
	AttemptID        string          `gorm:"column:attempt_id"`
	Sequence         int64           `gorm:"column:sequence"`
	VersionFrom      int64           `gorm:"column:version_from"`
	VersionTo        int64           `gorm:"column:version_to"`
	StateFrom        string          `gorm:"column:state_from"`
	StateTo          string          `gorm:"column:state_to"`
	FenceFrom        int64           `gorm:"column:fence_epoch_from"`
	FenceTo          int64           `gorm:"column:fence_epoch_to"`
	EventKind        string          `gorm:"column:event_kind"`
	ActorID          string          `gorm:"column:actor_id"`
	WorkerID         sql.NullString  `gorm:"column:worker_id"`
	Reason           string          `gorm:"column:reason"`
	LeaseWorkerIDTo  sql.NullString  `gorm:"column:lease_worker_id_to"`
	LeaseEpochTo     sql.NullInt64   `gorm:"column:lease_epoch_to"`
	LeaseExpiresAtTo sql.NullTime    `gorm:"column:lease_expires_at_to"`
	EvidenceTo       json.RawMessage `gorm:"column:evidence_to"`
	ExitReasonTo     sql.NullString  `gorm:"column:exit_reason_to"`
	CreatedAt        time.Time       `gorm:"column:created_at"`
}

func (attemptEventRow) TableName() string { return "agent_attempt_events" }

func (store *PostgresStore) SavePlan(
	ctx context.Context,
	pack ContextPack,
	capsule TaskCapsule,
) (TaskPlan, error) {
	if err := validateAgentStoreContext(ctx); err != nil {
		return TaskPlan{}, err
	}
	pack, err := ParseContextPack(pack)
	if err != nil {
		return TaskPlan{}, err
	}
	capsule, err = ParseTaskCapsule(capsule, pack)
	if err != nil {
		return TaskPlan{}, err
	}

	var persistedPack ContextPack
	var persistedCapsule TaskCapsule
	replayed := false
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		items, marshalErr := json.Marshal(pack.Items)
		if marshalErr != nil {
			return agentIntegrity("encode ContextPack items", marshalErr)
		}
		result := transaction.Exec(`
INSERT INTO agent_context_packs (
  id, schema_version, project_id, candidate_id, base_candidate_tree_hash,
  build_contract_id, build_contract_hash, items, content_hash, created_by, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?)
ON CONFLICT DO NOTHING
`, pack.ID, pack.SchemaVersion, pack.ProjectID, pack.CandidateID, pack.BaseCandidateTreeHash,
			pack.BuildContract.ID, pack.BuildContract.ContentHash, string(items), pack.ContentHash,
			pack.CreatedBy, pack.CreatedAt)
		if result.Error != nil {
			return result.Error
		}
		replayed = result.RowsAffected == 0
		persistedPack, marshalErr = loadContextPack(transaction, pack.ProjectID, pack.ID, "")
		if marshalErr != nil {
			return marshalErr
		}
		if !sameContextPackInput(persistedPack, pack) {
			return ErrAgentOperationReplay
		}

		payloads, marshalErr := marshalTaskCapsuleColumns(capsule)
		if marshalErr != nil {
			return marshalErr
		}
		result = transaction.Exec(`
INSERT INTO agent_task_capsules (
  id, schema_version, task_key, project_id, sandbox_session_id, candidate_id,
  candidate_version, candidate_session_epoch, candidate_writer_lease_epoch,
  base_candidate_tree_hash, build_contract_id, build_contract_hash,
  template_releases, context_pack_id, context_pack_hash, objective,
  obligation_ids, acceptance_criterion_ids, read_set, write_set, protected_paths,
  preconditions, postconditions, verification_command_ids, allowed_tools,
  network_policy, budgets, output_schema_hash, content_hash, created_by, created_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?,
  ?::jsonb, ?::jsonb, ?::jsonb, ?::jsonb, ?::jsonb,
  ?::jsonb, ?::jsonb, ?::jsonb, ?::jsonb, ?::jsonb, ?::jsonb, ?, ?, ?, ?
)
ON CONFLICT DO NOTHING
`, capsule.ID, capsule.SchemaVersion, capsule.TaskKey, capsule.ProjectID,
			capsule.SandboxSessionID, capsule.CandidateID, int64(capsule.CandidateVersion),
			int64(capsule.CandidateSessionEpoch), int64(capsule.CandidateWriterLeaseEpoch),
			capsule.BaseCandidateTreeHash, capsule.BuildContract.ID, capsule.BuildContract.ContentHash,
			payloads.templateReleases, capsule.ContextPack.ID, capsule.ContextPack.ContentHash,
			capsule.Objective, payloads.obligationIDs, payloads.acceptanceCriterionIDs,
			payloads.readSet, payloads.writeSet, payloads.protectedPaths, payloads.preconditions,
			payloads.postconditions, payloads.verificationCommandIDs, payloads.allowedTools,
			payloads.networkPolicy, payloads.budgets, capsule.OutputSchemaHash, capsule.ContentHash,
			capsule.CreatedBy, capsule.CreatedAt)
		if result.Error != nil {
			return result.Error
		}
		replayed = replayed || result.RowsAffected == 0
		persistedCapsule, marshalErr = loadTaskCapsule(transaction, capsule.ProjectID, capsule.ID, "")
		if marshalErr != nil {
			return marshalErr
		}
		if !sameTaskCapsuleInput(persistedCapsule, capsule) {
			return ErrAgentOperationReplay
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return TaskPlan{}, mapAgentStoreError("save plan", err)
	}
	return TaskPlan{ContextPack: persistedPack, TaskCapsule: persistedCapsule, Replayed: replayed}, nil
}

func (store *PostgresStore) CreateAttempt(
	ctx context.Context,
	operationID string,
	attempt AgentAttempt,
) (AgentAttempt, bool, error) {
	if err := validateAgentStoreContext(ctx); err != nil {
		return AgentAttempt{}, false, err
	}
	if !agentOperationPattern.MatchString(operationID) || operationID != strings.TrimSpace(operationID) {
		return AgentAttempt{}, false, fmt.Errorf("%w: operation ID", ErrInvalidAttempt)
	}
	attempt, err := ParseAttempt(attempt)
	if err != nil {
		return AgentAttempt{}, false, err
	}
	if attempt.State != AttemptPending || attempt.Version != 1 {
		return AgentAttempt{}, false, fmt.Errorf("%w: only an initial pending Attempt can be created", ErrInvalidAttempt)
	}

	var persisted AgentAttempt
	replayed := false
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		existing, existingErr := loadAttempt(transaction, attempt.ProjectID, "", operationLookup{
			actorID: attempt.CreatedBy, operationID: operationID,
		}, false)
		if existingErr == nil {
			if !sameAttemptCreation(existing, attempt) {
				return ErrAgentOperationReplay
			}
			persisted, replayed = existing, true
			return nil
		}
		if !errors.Is(existingErr, ErrAttemptNotFound) {
			return existingErr
		}
		capsule, contextPack, loadErr := loadAttemptInputs(
			transaction, attempt.ProjectID, attempt.TaskCapsule.ID, attempt.TaskCapsule.ContentHash,
			attempt.ContextPack.ID, attempt.ContextPack.ContentHash,
		)
		if loadErr != nil {
			return loadErr
		}
		var parent *AgentAttempt
		if attempt.ParentAttemptID != "" {
			loadedParent, parentErr := loadAttempt(transaction, attempt.ProjectID, attempt.ParentAttemptID, "", false)
			if parentErr != nil {
				return parentErr
			}
			parent = &loadedParent
		}
		expected, createErr := NewAttempt(NewAttemptInput{
			ID: attempt.ID, CreatedBy: attempt.CreatedBy, Executor: attempt.Executor,
			Parent: parent, RetryReason: attempt.RetryReason,
		}, capsule, contextPack, attempt.CreatedAt)
		if createErr != nil || !sameAttemptCreation(expected, attempt) {
			if createErr != nil {
				return createErr
			}
			return fmt.Errorf("%w: Attempt does not bind its exact persisted plan", ErrInvalidAttempt)
		}
		if strings.HasPrefix(capsule.TaskKey, TaskKeyPrefix) {
			var active struct {
				Exists bool `gorm:"column:exists"`
			}
			activeResult := transaction.Raw(`
SELECT EXISTS (
  SELECT 1
  FROM agent_attempts AS other_attempt
  JOIN agent_task_capsules AS other_capsule
    ON other_capsule.id = other_attempt.task_capsule_id
   AND other_capsule.project_id = other_attempt.project_id
  WHERE other_attempt.project_id = ?
    AND other_attempt.sandbox_session_id = ?
    AND other_capsule.task_key = ?
    AND other_attempt.state IN (
      'pending', 'ready', 'queued', 'claimed', 'running',
      'patch_ready', 'validating', 'review_ready'
    )
) AS exists
`, attempt.ProjectID, attempt.SandboxSessionID, capsule.TaskKey).Scan(&active)
			if activeResult.Error != nil {
				return activeResult.Error
			}
			if active.Exists {
				return fmt.Errorf("%w: task %s already has an active or reviewable Attempt", ErrTaskGraphBlocked, capsule.TaskKey)
			}
		}

		executor, marshalErr := json.Marshal(attempt.Executor)
		if marshalErr != nil {
			return agentIntegrity("encode executor identity", marshalErr)
		}
		evidence, marshalErr := json.Marshal(attempt.Evidence)
		if marshalErr != nil {
			return agentIntegrity("encode initial evidence", marshalErr)
		}
		result := transaction.Exec(`
INSERT INTO agent_attempts (
  id, schema_version, operation_id, project_id, sandbox_session_id, candidate_id,
  task_capsule_id, task_capsule_hash, context_pack_id, context_pack_hash,
  base_candidate_tree_hash, build_contract_hash, executor,
  request_key_hash, configuration_hash, parent_attempt_id, retry_reason,
  state, version, fence_epoch, evidence, created_by, created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?
)
ON CONFLICT DO NOTHING
`, attempt.ID, attempt.SchemaVersion, operationID, attempt.ProjectID, attempt.SandboxSessionID,
			attempt.CandidateID, attempt.TaskCapsule.ID, attempt.TaskCapsule.ContentHash,
			attempt.ContextPack.ID, attempt.ContextPack.ContentHash, attempt.BaseCandidateTreeHash,
			attempt.BuildContractHash, string(executor), attempt.RequestKeyHash, attempt.ConfigurationHash,
			nullableString(attempt.ParentAttemptID), nullableString(attempt.RetryReason), attempt.State,
			int64(attempt.Version), int64(attempt.FenceEpoch), string(evidence), attempt.CreatedBy,
			attempt.CreatedAt, attempt.UpdatedAt)
		if result.Error != nil {
			return result.Error
		}
		replayed = result.RowsAffected == 0
		persisted, loadErr = loadAttempt(transaction, attempt.ProjectID, "", operationLookup{
			actorID: attempt.CreatedBy, operationID: operationID,
		}, false)
		if loadErr != nil {
			if replayed && errors.Is(loadErr, ErrAttemptNotFound) {
				return ErrAgentOperationReplay
			}
			return loadErr
		}
		if !sameAttemptCreation(persisted, attempt) {
			return ErrAgentOperationReplay
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return AgentAttempt{}, false, mapAgentStoreError("create Attempt", err)
	}
	return persisted, replayed, nil
}

func (store *PostgresStore) GetContextPack(
	ctx context.Context,
	projectID, contextPackID string,
) (ContextPack, error) {
	if err := validateIDs(ctx, projectID, contextPackID); err != nil {
		return ContextPack{}, err
	}
	value, err := loadContextPack(store.database.WithContext(ctx), projectID, contextPackID, "")
	if err != nil {
		return ContextPack{}, mapAgentStoreError("get ContextPack", err)
	}
	return value, nil
}

func (store *PostgresStore) GetTaskCapsule(
	ctx context.Context,
	projectID, taskCapsuleID string,
) (TaskCapsule, error) {
	if err := validateIDs(ctx, projectID, taskCapsuleID); err != nil {
		return TaskCapsule{}, err
	}
	value, err := loadTaskCapsule(store.database.WithContext(ctx), projectID, taskCapsuleID, "")
	if err != nil {
		return TaskCapsule{}, mapAgentStoreError("get TaskCapsule", err)
	}
	return value, nil
}

func (store *PostgresStore) GetAttempt(
	ctx context.Context,
	projectID, attemptID string,
) (AgentAttempt, error) {
	if err := validateIDs(ctx, projectID, attemptID); err != nil {
		return AgentAttempt{}, err
	}
	value, err := loadAttempt(store.database.WithContext(ctx), projectID, attemptID, "", false)
	if err != nil {
		return AgentAttempt{}, mapAgentStoreError("get Attempt", err)
	}
	return value, nil
}

func (store *PostgresStore) FindAttemptByOperation(
	ctx context.Context,
	projectID, actorID, operationID string,
) (AgentAttempt, bool, error) {
	if err := validateIDs(ctx, projectID, actorID); err != nil {
		return AgentAttempt{}, false, err
	}
	if !agentOperationPattern.MatchString(operationID) || operationID != strings.TrimSpace(operationID) {
		return AgentAttempt{}, false, fmt.Errorf("%w: operation ID", ErrInvalidAttempt)
	}
	value, err := loadAttempt(store.database.WithContext(ctx), projectID, "", operationLookup{
		actorID: actorID, operationID: operationID,
	}, false)
	if errors.Is(err, ErrAttemptNotFound) {
		return AgentAttempt{}, false, nil
	}
	if err != nil {
		return AgentAttempt{}, false, mapAgentStoreError("find Attempt operation", err)
	}
	return value, true, nil
}

func (store *PostgresStore) ResolveAttemptProject(ctx context.Context, attemptID string) (string, error) {
	if err := validateIDs(ctx, attemptID); err != nil {
		return "", err
	}
	var row struct {
		ProjectID string `gorm:"column:project_id"`
	}
	result := store.database.WithContext(ctx).Raw(`
SELECT project_id FROM agent_attempts WHERE id = ?
`, attemptID).Scan(&row)
	if result.Error != nil {
		return "", mapAgentStoreError("resolve Attempt project", result.Error)
	}
	if result.RowsAffected != 1 || !validUUIDs(row.ProjectID) {
		return "", ErrAttemptNotFound
	}
	return row.ProjectID, nil
}

func (store *PostgresStore) ListAttempts(
	ctx context.Context,
	projectID, sessionID string,
	limit int,
) ([]AgentAttempt, error) {
	if err := validateIDs(ctx, projectID, sessionID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%w: list limit", ErrInvalidAttempt)
	}
	var rows []attemptRow
	result := store.database.WithContext(ctx).Where(
		"project_id = ? AND sandbox_session_id = ?", projectID, sessionID,
	).Order("updated_at DESC, id DESC").Limit(limit).Find(&rows)
	if result.Error != nil {
		return nil, mapAgentStoreError("list Attempts", result.Error)
	}
	values := make([]AgentAttempt, 0, len(rows))
	for _, row := range rows {
		value, err := hydrateAttempt(store.database.WithContext(ctx), row)
		if err != nil {
			return nil, mapAgentStoreError("hydrate listed Attempt", err)
		}
		values = append(values, value)
	}
	return values, nil
}

func (store *PostgresStore) ListTaskAttemptProgress(
	ctx context.Context,
	projectID, sessionID string,
) ([]TaskAttemptProgress, error) {
	if err := validateIDs(ctx, projectID, sessionID); err != nil {
		return nil, err
	}
	var rows []struct {
		AttemptID string `gorm:"column:attempt_id"`
		TaskKey   string `gorm:"column:task_key"`
		Applied   bool   `gorm:"column:applied"`
	}
	result := store.database.WithContext(ctx).Raw(`
SELECT
  attempt.id::text AS attempt_id,
  capsule.task_key,
  EXISTS (
    SELECT 1
    FROM agent_patch_merge_plans AS merge_plan
    LEFT JOIN agent_patch_merge_applications AS merge_application
      ON merge_application.merge_id = merge_plan.id
     AND merge_application.project_id = merge_plan.project_id
    WHERE merge_plan.project_id = attempt.project_id
      AND merge_plan.attempt_id = attempt.id
      AND (
        merge_plan.disposition = 'noop'
        OR (merge_plan.disposition = 'planned' AND merge_application.merge_id IS NOT NULL)
      )
      AND NOT EXISTS (
        SELECT 1
        FROM agent_patch_undo_plans AS undo_plan
        JOIN agent_patch_undo_applications AS undo_application
          ON undo_application.undo_id = undo_plan.id
         AND undo_application.project_id = undo_plan.project_id
        WHERE undo_plan.project_id = merge_plan.project_id
          AND undo_plan.merge_id = merge_plan.id
          AND undo_plan.disposition = 'planned'
      )
  ) AS applied
FROM agent_attempts AS attempt
JOIN agent_task_capsules AS capsule
  ON capsule.id = attempt.task_capsule_id
 AND capsule.project_id = attempt.project_id
WHERE attempt.project_id = ?
  AND attempt.sandbox_session_id = ?
ORDER BY attempt.updated_at DESC, attempt.id DESC
LIMIT ?
`, projectID, sessionID, 1001).Scan(&rows)
	if result.Error != nil {
		return nil, mapAgentStoreError("list task graph Attempt progress", result.Error)
	}
	if len(rows) > 1000 {
		return nil, fmt.Errorf("%w: task graph Attempt history exceeds 1000 entries", ErrAgentStoreIntegrity)
	}
	progress := make([]TaskAttemptProgress, 0, len(rows))
	for _, row := range rows {
		attempt, err := store.GetAttempt(ctx, projectID, row.AttemptID)
		if err != nil {
			return nil, mapAgentStoreError("hydrate task graph Attempt progress", err)
		}
		progress = append(progress, TaskAttemptProgress{
			Attempt: attempt,
			TaskKey: row.TaskKey,
			Applied: row.Applied,
		})
	}
	return progress, nil
}

var _ TaskGraphProgressStore = (*PostgresStore)(nil)

// ListClaimable returns immutable projections that are either newly queued or
// have an expired worker lease. Claim remains the authoritative CAS, so two
// workers may observe the same row but only one can advance its fencing epoch.
func (store *PostgresStore) ListClaimable(
	ctx context.Context,
	limit int,
) ([]AgentAttempt, error) {
	if err := validateAgentStoreContext(ctx); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%w: claimable list limit", ErrInvalidAttempt)
	}
	var rows []attemptRow
	result := store.database.WithContext(ctx).Raw(`
SELECT *
FROM agent_attempts
WHERE state = 'queued'
   OR (
     state IN ('claimed', 'running', 'patch_ready', 'validating')
     AND lease_expires_at <= clock_timestamp()
   )
ORDER BY
  CASE WHEN state = 'queued' THEN 0 ELSE 1 END,
  COALESCE(lease_expires_at, created_at),
  created_at,
  id
LIMIT ?
`, limit).Scan(&rows)
	if result.Error != nil {
		return nil, mapAgentStoreError("list claimable Attempts", result.Error)
	}
	values := make([]AgentAttempt, 0, len(rows))
	for _, row := range rows {
		value, err := hydrateAttempt(store.database.WithContext(ctx), row)
		if err != nil {
			return nil, mapAgentStoreError("hydrate claimable Attempt", err)
		}
		values = append(values, value)
	}
	return values, nil
}

func (store *PostgresStore) ListEvents(
	ctx context.Context,
	projectID, attemptID string,
	afterSequence uint64,
	limit int,
) ([]AttemptEvent, error) {
	if err := validateIDs(ctx, projectID, attemptID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("%w: event list limit", ErrInvalidAttempt)
	}
	if _, err := loadAttempt(store.database.WithContext(ctx), projectID, attemptID, "", false); err != nil {
		return nil, mapAgentStoreError("load Attempt for events", err)
	}
	var rows []attemptEventRow
	result := store.database.WithContext(ctx).Where(
		"attempt_id = ? AND sequence > ?", attemptID, int64(afterSequence),
	).Order("sequence ASC").Limit(limit).Find(&rows)
	if result.Error != nil {
		return nil, mapAgentStoreError("list Attempt events", result.Error)
	}
	values := make([]AttemptEvent, 0, len(rows))
	for _, row := range rows {
		value, err := hydrateAttemptEvent(row)
		if err != nil {
			return nil, mapAgentStoreError("hydrate Attempt event", err)
		}
		if len(values) > 0 && value.Sequence != values[len(values)-1].Sequence+1 {
			return nil, agentIntegrity("Attempt event page is not contiguous", nil)
		}
		values = append(values, value)
	}
	return values, nil
}

func (store *PostgresStore) Claim(
	ctx context.Context,
	attemptID string,
	expectedVersion uint64,
	actorID, workerID string,
	ttl time.Duration,
) (AgentAttempt, error) {
	return store.mutate(ctx, "claim Attempt", attemptID, func(current AgentAttempt, now time.Time) (AgentAttempt, AttemptEvent, error) {
		return current.Claim(expectedVersion, actorID, workerID, ttl, now)
	})
}

func (store *PostgresStore) Renew(
	ctx context.Context,
	attemptID string,
	expectedVersion, expectedFenceEpoch uint64,
	actorID, workerID string,
	ttl time.Duration,
) (AgentAttempt, error) {
	return store.mutate(ctx, "renew Attempt lease", attemptID, func(current AgentAttempt, now time.Time) (AgentAttempt, AttemptEvent, error) {
		return current.Renew(expectedVersion, expectedFenceEpoch, actorID, workerID, ttl, now)
	})
}

func (store *PostgresStore) Advance(
	ctx context.Context,
	attemptID string,
	input AdvanceAttemptInput,
) (AgentAttempt, error) {
	return store.mutate(ctx, "advance Attempt", attemptID, func(current AgentAttempt, now time.Time) (AgentAttempt, AttemptEvent, error) {
		return current.Advance(input, now)
	})
}

func (store *PostgresStore) Cancel(
	ctx context.Context,
	attemptID string,
	expectedVersion uint64,
	actorID, reason string,
) (AgentAttempt, error) {
	return store.mutate(ctx, "cancel Attempt", attemptID, func(current AgentAttempt, now time.Time) (AgentAttempt, AttemptEvent, error) {
		return current.Cancel(expectedVersion, actorID, reason, now)
	})
}

func (store *PostgresStore) MarkStale(
	ctx context.Context,
	attemptID string,
	expectedVersion uint64,
	actorID, reason string,
) (AgentAttempt, error) {
	return store.mutate(ctx, "mark Attempt stale", attemptID, func(current AgentAttempt, now time.Time) (AgentAttempt, AttemptEvent, error) {
		return current.MarkStale(expectedVersion, actorID, reason, now)
	})
}

type attemptMutation func(AgentAttempt, time.Time) (AgentAttempt, AttemptEvent, error)

func (store *PostgresStore) mutate(
	ctx context.Context,
	operation, attemptID string,
	mutation attemptMutation,
) (AgentAttempt, error) {
	if err := validateIDs(ctx, attemptID); err != nil {
		return AgentAttempt{}, err
	}
	if mutation == nil {
		return AgentAttempt{}, fmt.Errorf("%w: mutation is required", ErrInvalidAttempt)
	}
	var persisted AgentAttempt
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		current, loadErr := loadAttempt(transaction, "", attemptID, "", true)
		if loadErr != nil {
			return loadErr
		}
		now, clockErr := agentDatabaseClock(transaction)
		if clockErr != nil {
			return clockErr
		}
		next, event, mutationErr := mutation(current, now)
		if mutationErr != nil {
			return mutationErr
		}
		if event.AttemptID != current.ID || event.VersionFrom != current.Version ||
			event.VersionTo != next.Version || event.StateTo != next.State ||
			event.FenceTo != next.FenceEpoch {
			return agentIntegrity("domain mutation produced an inconsistent event", nil)
		}
		if insertErr := insertAttemptEvent(transaction, event, next); insertErr != nil {
			return insertErr
		}
		persisted, loadErr = loadAttempt(transaction, current.ProjectID, current.ID, "", false)
		if loadErr != nil {
			return loadErr
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return AgentAttempt{}, mapAgentStoreError(operation, err)
	}
	return persisted, nil
}

type taskCapsuleJSONColumns struct {
	templateReleases       string
	obligationIDs          string
	acceptanceCriterionIDs string
	readSet                string
	writeSet               string
	protectedPaths         string
	preconditions          string
	postconditions         string
	verificationCommandIDs string
	allowedTools           string
	networkPolicy          string
	budgets                string
}

func marshalTaskCapsuleColumns(value TaskCapsule) (taskCapsuleJSONColumns, error) {
	columns := taskCapsuleJSONColumns{}
	values := []struct {
		value  any
		target *string
	}{
		{value.TemplateReleases, &columns.templateReleases},
		{value.ObligationIDs, &columns.obligationIDs},
		{value.AcceptanceCriterionIDs, &columns.acceptanceCriterionIDs},
		{value.ReadSet, &columns.readSet}, {value.WriteSet, &columns.writeSet},
		{value.ProtectedPaths, &columns.protectedPaths},
		{value.Preconditions, &columns.preconditions}, {value.Postconditions, &columns.postconditions},
		{value.VerificationCommandIDs, &columns.verificationCommandIDs},
		{value.AllowedTools, &columns.allowedTools}, {value.NetworkPolicy, &columns.networkPolicy},
		{value.Budgets, &columns.budgets},
	}
	for _, item := range values {
		payload, err := json.Marshal(item.value)
		if err != nil {
			return taskCapsuleJSONColumns{}, agentIntegrity("encode TaskCapsule column", err)
		}
		*item.target = string(payload)
	}
	return columns, nil
}

func loadContextPack(database *gorm.DB, projectID, id, expectedHash string) (ContextPack, error) {
	query := database.Where("id = ?", id)
	if projectID != "" {
		query = query.Where("project_id = ?", projectID)
	}
	if expectedHash != "" {
		query = query.Where("content_hash = ?", expectedHash)
	}
	var row contextPackRow
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return ContextPack{}, ErrPlanNotFound
	}
	if result.Error != nil {
		return ContextPack{}, result.Error
	}
	var items []ContextItem
	if err := decodeJSONColumn(row.Items, &items); err != nil {
		return ContextPack{}, agentIntegrity("decode ContextPack items", err)
	}
	return ParseContextPack(ContextPack{
		SchemaVersion: row.SchemaVersion, ID: row.ID, ProjectID: row.ProjectID,
		CandidateID: row.CandidateID, BaseCandidateTreeHash: row.BaseCandidateTreeHash,
		BuildContract: repository.ExactReference{ID: row.BuildContractID, ContentHash: row.BuildContractHash},
		Items:         items, ContentHash: row.ContentHash, CreatedBy: row.CreatedBy,
		CreatedAt: canonicalDatabaseTime(row.CreatedAt),
	})
}

func loadTaskCapsule(database *gorm.DB, projectID, id, expectedHash string) (TaskCapsule, error) {
	query := database.Where("id = ?", id)
	if projectID != "" {
		query = query.Where("project_id = ?", projectID)
	}
	if expectedHash != "" {
		query = query.Where("content_hash = ?", expectedHash)
	}
	var row taskCapsuleRow
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return TaskCapsule{}, ErrPlanNotFound
	}
	if result.Error != nil {
		return TaskCapsule{}, result.Error
	}
	contextPack, err := loadContextPack(database, row.ProjectID, row.ContextPackID, row.ContextPackHash)
	if err != nil {
		return TaskCapsule{}, err
	}
	var templateReleases []repository.ExactReference
	var obligationIDs, acceptanceIDs, readSet, writeSet, protectedPaths []string
	var preconditions, postconditions, commandIDs, tools []string
	var network NetworkPolicy
	var budgets TaskBudgets
	for _, item := range []struct {
		payload json.RawMessage
		target  any
	}{
		{row.TemplateReleases, &templateReleases}, {row.ObligationIDs, &obligationIDs},
		{row.AcceptanceCriterionIDs, &acceptanceIDs}, {row.ReadSet, &readSet},
		{row.WriteSet, &writeSet}, {row.ProtectedPaths, &protectedPaths},
		{row.Preconditions, &preconditions}, {row.Postconditions, &postconditions},
		{row.VerificationCommandIDs, &commandIDs}, {row.AllowedTools, &tools},
		{row.NetworkPolicy, &network}, {row.Budgets, &budgets},
	} {
		if err := decodeJSONColumn(item.payload, item.target); err != nil {
			return TaskCapsule{}, agentIntegrity("decode TaskCapsule column", err)
		}
	}
	if row.CandidateVersion <= 0 || row.CandidateSessionEpoch <= 0 || row.CandidateWriterLeaseEpoch < 0 {
		return TaskCapsule{}, agentIntegrity("TaskCapsule numeric projection", nil)
	}
	return ParseTaskCapsule(TaskCapsule{
		SchemaVersion: row.SchemaVersion, ID: row.ID, TaskKey: row.TaskKey,
		ProjectID: row.ProjectID, SandboxSessionID: row.SandboxSessionID, CandidateID: row.CandidateID,
		CandidateVersion: uint64(row.CandidateVersion), CandidateSessionEpoch: uint64(row.CandidateSessionEpoch),
		CandidateWriterLeaseEpoch: uint64(row.CandidateWriterLeaseEpoch),
		BaseCandidateTreeHash:     row.BaseCandidateTreeHash,
		BuildContract:             repository.ExactReference{ID: row.BuildContractID, ContentHash: row.BuildContractHash},
		TemplateReleases:          templateReleases,
		ContextPack:               ContextPackReference{ID: row.ContextPackID, ContentHash: row.ContextPackHash},
		Objective:                 row.Objective, ObligationIDs: obligationIDs, AcceptanceCriterionIDs: acceptanceIDs,
		ReadSet: readSet, WriteSet: writeSet, ProtectedPaths: protectedPaths,
		Preconditions: preconditions, Postconditions: postconditions,
		VerificationCommandIDs: commandIDs, AllowedTools: tools, NetworkPolicy: network, Budgets: budgets,
		OutputSchemaHash: row.OutputSchemaHash, ContentHash: row.ContentHash,
		CreatedBy: row.CreatedBy, CreatedAt: canonicalDatabaseTime(row.CreatedAt),
	}, contextPack)
}

type operationLookup struct {
	actorID     string
	operationID string
}

func loadAttempt(database *gorm.DB, projectID, id string, lookup any, lock bool) (AgentAttempt, error) {
	query := database.Model(&attemptRow{})
	if projectID != "" {
		query = query.Where("project_id = ?", projectID)
	}
	if id != "" {
		query = query.Where("id = ?", id)
	}
	if operation, ok := lookup.(operationLookup); ok {
		query = query.Where("created_by = ? AND operation_id = ?", operation.actorID, operation.operationID)
	}
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row attemptRow
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return AgentAttempt{}, ErrAttemptNotFound
	}
	if result.Error != nil {
		return AgentAttempt{}, result.Error
	}
	return hydrateAttempt(database, row)
}

func hydrateAttempt(database *gorm.DB, row attemptRow) (AgentAttempt, error) {
	capsule, contextPack, err := loadAttemptInputs(
		database, row.ProjectID, row.TaskCapsuleID, row.TaskCapsuleHash,
		row.ContextPackID, row.ContextPackHash,
	)
	if err != nil {
		return AgentAttempt{}, err
	}
	var executor ExecutorIdentity
	var evidence AttemptEvidence
	if err := decodeJSONColumn(row.Executor, &executor); err != nil {
		return AgentAttempt{}, agentIntegrity("decode Attempt executor", err)
	}
	if err := decodeJSONColumn(row.Evidence, &evidence); err != nil {
		return AgentAttempt{}, agentIntegrity("decode Attempt evidence", err)
	}
	if row.Version <= 0 || row.FenceEpoch < 0 {
		return AgentAttempt{}, agentIntegrity("Attempt numeric projection", nil)
	}
	var lease *AttemptLease
	if row.LeaseWorkerID.Valid || row.LeaseEpoch.Valid || row.LeaseExpiresAt.Valid {
		if !row.LeaseWorkerID.Valid || !row.LeaseEpoch.Valid || !row.LeaseExpiresAt.Valid || row.LeaseEpoch.Int64 < 0 {
			return AgentAttempt{}, agentIntegrity("Attempt lease projection", nil)
		}
		lease = &AttemptLease{
			WorkerID: row.LeaseWorkerID.String, Epoch: uint64(row.LeaseEpoch.Int64),
			ExpiresAt: canonicalDatabaseTime(row.LeaseExpiresAt.Time),
		}
	}
	templateHashes := make([]string, len(capsule.TemplateReleases))
	for index, release := range capsule.TemplateReleases {
		templateHashes[index] = release.ContentHash
	}
	attempt := AgentAttempt{
		SchemaVersion: row.SchemaVersion, ID: row.ID, ProjectID: row.ProjectID,
		SandboxSessionID: row.SandboxSessionID, CandidateID: row.CandidateID,
		TaskCapsule:           repository.ExactReference{ID: row.TaskCapsuleID, ContentHash: row.TaskCapsuleHash},
		ContextPack:           ContextPackReference{ID: row.ContextPackID, ContentHash: row.ContextPackHash},
		BaseCandidateTreeHash: row.BaseCandidateTreeHash, BuildContractHash: row.BuildContractHash,
		TemplateReleaseHashes: templateHashes, Executor: executor,
		RequestKeyHash: row.RequestKeyHash, ConfigurationHash: row.ConfigurationHash,
		ParentAttemptID: nullStringValue(row.ParentAttemptID), RetryReason: nullStringValue(row.RetryReason),
		State: AttemptState(row.State), Version: uint64(row.Version), FenceEpoch: uint64(row.FenceEpoch),
		Lease: lease, Evidence: evidence, ExitReason: nullStringValue(row.ExitReason),
		CreatedBy: row.CreatedBy, CreatedAt: canonicalDatabaseTime(row.CreatedAt),
		StartedAt: nullableTime(row.StartedAt), FinishedAt: nullableTime(row.FinishedAt),
		UpdatedAt: canonicalDatabaseTime(row.UpdatedAt),
	}
	parsed, err := ParseAttempt(attempt)
	if err != nil {
		return AgentAttempt{}, agentIntegrity("validate Attempt projection", err)
	}
	if parsed.ContextPack != contextPack.ExactReference() ||
		parsed.TaskCapsule.ID != capsule.ID || parsed.TaskCapsule.ContentHash != capsule.ContentHash ||
		parsed.SandboxSessionID != capsule.SandboxSessionID || parsed.CandidateID != capsule.CandidateID ||
		parsed.BaseCandidateTreeHash != capsule.BaseCandidateTreeHash ||
		parsed.BuildContractHash != capsule.BuildContract.ContentHash ||
		parsed.Executor.OutputSchemaHash != capsule.OutputSchemaHash {
		return AgentAttempt{}, agentIntegrity("Attempt does not match its exact TaskCapsule", nil)
	}
	return parsed, nil
}

func loadAttemptInputs(
	database *gorm.DB,
	projectID, taskID, taskHash, contextID, contextHash string,
) (TaskCapsule, ContextPack, error) {
	capsule, err := loadTaskCapsule(database, projectID, taskID, taskHash)
	if err != nil {
		return TaskCapsule{}, ContextPack{}, err
	}
	contextPack, err := loadContextPack(database, projectID, contextID, contextHash)
	if err != nil {
		return TaskCapsule{}, ContextPack{}, err
	}
	if capsule.ContextPack != contextPack.ExactReference() {
		return TaskCapsule{}, ContextPack{}, agentIntegrity("Attempt plan references diverged", nil)
	}
	return capsule, contextPack, nil
}

func hydrateAttemptEvent(row attemptEventRow) (AttemptEvent, error) {
	if row.Sequence <= 0 || row.VersionFrom <= 0 || row.VersionTo != row.VersionFrom+1 ||
		row.Sequence != row.VersionFrom || row.FenceFrom < 0 || row.FenceTo < row.FenceFrom ||
		!validUUIDs(row.AttemptID, row.ActorID) || !knownAttemptState(AttemptState(row.StateFrom)) ||
		!knownAttemptState(AttemptState(row.StateTo)) || row.Reason != strings.TrimSpace(row.Reason) ||
		row.Reason == "" || len(row.Reason) > 2000 || row.CreatedAt.IsZero() {
		return AttemptEvent{}, agentIntegrity("Attempt event structure", nil)
	}
	kind := AttemptEventKind(row.EventKind)
	if kind != EventLifecycleAdvanced && kind != EventLeaseClaimed && kind != EventLeaseReclaimed &&
		kind != EventLeaseRenewed && kind != EventControlCancelled && kind != EventControlStale {
		return AttemptEvent{}, agentIntegrity("Attempt event kind", nil)
	}
	var evidence AttemptEvidence
	if err := decodeJSONColumn(row.EvidenceTo, &evidence); err != nil {
		return AttemptEvent{}, agentIntegrity("decode Attempt event evidence", err)
	}
	evidence, err := mergeEvidence(AttemptEvidence{}, evidence)
	if err != nil {
		return AttemptEvent{}, agentIntegrity("validate Attempt event evidence", err)
	}
	var lease *AttemptLease
	if row.LeaseWorkerIDTo.Valid || row.LeaseEpochTo.Valid || row.LeaseExpiresAtTo.Valid {
		if !row.LeaseWorkerIDTo.Valid || !row.LeaseEpochTo.Valid || !row.LeaseExpiresAtTo.Valid ||
			row.LeaseEpochTo.Int64 < 0 {
			return AttemptEvent{}, agentIntegrity("Attempt event lease", nil)
		}
		lease = &AttemptLease{
			WorkerID: row.LeaseWorkerIDTo.String, Epoch: uint64(row.LeaseEpochTo.Int64),
			ExpiresAt: canonicalDatabaseTime(row.LeaseExpiresAtTo.Time),
		}
	}
	return AttemptEvent{
		SchemaVersion: AttemptEventSchema, AttemptID: row.AttemptID, Sequence: uint64(row.Sequence),
		VersionFrom: uint64(row.VersionFrom), VersionTo: uint64(row.VersionTo),
		StateFrom: AttemptState(row.StateFrom), StateTo: AttemptState(row.StateTo),
		FenceFrom: uint64(row.FenceFrom), FenceTo: uint64(row.FenceTo), Kind: kind,
		ActorID: row.ActorID, WorkerID: nullStringValue(row.WorkerID), Reason: row.Reason,
		Lease: lease, Evidence: evidence, ExitReason: nullStringValue(row.ExitReasonTo),
		CreatedAt: canonicalDatabaseTime(row.CreatedAt),
	}, nil
}

func insertAttemptEvent(database *gorm.DB, event AttemptEvent, next AgentAttempt) error {
	evidence, err := json.Marshal(event.Evidence)
	if err != nil {
		return agentIntegrity("encode Attempt event evidence", err)
	}
	var leaseWorker, leaseEpoch, leaseExpires any
	if event.Lease != nil {
		leaseWorker = event.Lease.WorkerID
		leaseEpoch = int64(event.Lease.Epoch)
		leaseExpires = event.Lease.ExpiresAt
	}
	result := database.Exec(`
INSERT INTO agent_attempt_events (
  attempt_id, sequence, version_from, version_to, state_from, state_to,
  fence_epoch_from, fence_epoch_to, event_kind, actor_id, worker_id, reason,
  lease_worker_id_to, lease_epoch_to, lease_expires_at_to, evidence_to,
  exit_reason_to, started_at_to, finished_at_to, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?)
`, event.AttemptID, int64(event.Sequence), int64(event.VersionFrom), int64(event.VersionTo),
		event.StateFrom, event.StateTo, int64(event.FenceFrom), int64(event.FenceTo), event.Kind,
		event.ActorID, nullableString(event.WorkerID), event.Reason, leaseWorker, leaseEpoch, leaseExpires,
		string(evidence), nullableString(event.ExitReason), nullableTimeValue(next.StartedAt),
		nullableTimeValue(next.FinishedAt), event.CreatedAt)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return agentIntegrity("append Attempt event returned no row", nil)
	}
	return nil
}

func agentDatabaseClock(database *gorm.DB) (time.Time, error) {
	var now time.Time
	result := database.Raw(`SELECT clock_timestamp()`).Scan(&now)
	if result.Error != nil || result.RowsAffected != 1 || now.IsZero() {
		if result.Error != nil {
			return time.Time{}, result.Error
		}
		return time.Time{}, agentIntegrity("read database clock", nil)
	}
	return canonicalDatabaseTime(now), nil
}

func sameContextPackInput(left, right ContextPack) bool {
	left.CreatedAt = right.CreatedAt
	return equalJSON(left, right)
}

func sameTaskCapsuleInput(left, right TaskCapsule) bool {
	left.CreatedAt = right.CreatedAt
	return equalJSON(left, right)
}

func sameAttemptCreation(left, right AgentAttempt) bool {
	return left.ID == right.ID && left.SchemaVersion == right.SchemaVersion &&
		left.ProjectID == right.ProjectID && left.SandboxSessionID == right.SandboxSessionID &&
		left.CandidateID == right.CandidateID && left.TaskCapsule == right.TaskCapsule &&
		left.ContextPack == right.ContextPack && left.BaseCandidateTreeHash == right.BaseCandidateTreeHash &&
		left.BuildContractHash == right.BuildContractHash &&
		equalJSON(left.TemplateReleaseHashes, right.TemplateReleaseHashes) && left.Executor == right.Executor &&
		left.RequestKeyHash == right.RequestKeyHash && left.ConfigurationHash == right.ConfigurationHash &&
		left.ParentAttemptID == right.ParentAttemptID && left.RetryReason == right.RetryReason &&
		left.CreatedBy == right.CreatedBy
}

func decodeJSONColumn(payload json.RawMessage, target any) error {
	if len(payload) == 0 || target == nil {
		return errors.New("JSON column is empty")
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON column contains multiple values")
		}
		return err
	}
	return nil
}

func validateAgentStoreContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalidAttempt)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func validateIDs(ctx context.Context, values ...string) error {
	if err := validateAgentStoreContext(ctx); err != nil {
		return err
	}
	if !validUUIDs(values...) {
		return fmt.Errorf("%w: exact UUID is required", ErrInvalidAttempt)
	}
	return nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func nullableTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	canonical := canonicalDatabaseTime(value.Time)
	return &canonical
}

func nullableTimeValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

func canonicalDatabaseTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

type agentStoreError struct {
	operation string
	kind      error
	cause     error
}

func (err *agentStoreError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("agent persistence %s: %v: %v", err.operation, err.kind, err.cause)
}

func (err *agentStoreError) Unwrap() []error {
	if err == nil {
		return nil
	}
	return []error{err.kind, err.cause}
}

func agentIntegrity(message string, cause error) error {
	if cause == nil {
		cause = errors.New(message)
	} else {
		cause = fmt.Errorf("%s: %w", message, cause)
	}
	return &agentStoreError{operation: message, kind: ErrAgentStoreIntegrity, cause: cause}
}

func mapAgentStoreError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var persisted *agentStoreError
	if errors.As(err, &persisted) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	for _, known := range []error{
		ErrInvalidContextPack, ErrInvalidTaskCapsule, ErrInvalidAttempt, ErrAttemptState,
		ErrAttemptFenced, ErrAttemptLease, ErrPlanNotFound, ErrAttemptNotFound,
		ErrAgentOperationReplay, ErrAttemptVersionConflict, ErrTaskGraphBlocked,
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
			return &agentStoreError{operation: operation, kind: ErrAgentOperationReplay, cause: err}
		case postgres.Code == "40001" || postgres.Code == "40P01":
			return &agentStoreError{operation: operation, kind: ErrAttemptVersionConflict, cause: err}
		case strings.Contains(message, "worker fence") || strings.Contains(message, "lease"):
			return &agentStoreError{operation: operation, kind: ErrAttemptFenced, cause: err}
		case strings.HasPrefix(postgres.Code, "08") || postgres.Code == "57P01" ||
			postgres.Code == "57P02" || postgres.Code == "57P03":
			return &agentStoreError{operation: operation, kind: ErrAgentStoreUnavailable, cause: err}
		case postgres.Code == "22001" || postgres.Code == "22003" || postgres.Code == "22023" ||
			postgres.Code == "22P02" || postgres.Code == "23502":
			return &agentStoreError{operation: operation, kind: ErrInvalidAttempt, cause: err}
		default:
			return &agentStoreError{operation: operation, kind: ErrAgentStoreIntegrity, cause: err}
		}
	}
	return &agentStoreError{operation: operation, kind: ErrAgentStoreUnavailable, cause: err}
}
