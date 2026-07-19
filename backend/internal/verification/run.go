package verification

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	RunSchemaVersion        = "candidate-verification-run/v1"
	RunRequestSchemaVersion = "candidate-verification-run-request/v1"
)

var (
	ErrInvalidRun             = errors.New("invalid candidate verification run")
	ErrRunNotFound            = errors.New("candidate verification run was not found")
	ErrRunIdempotencyConflict = errors.New("candidate verification run idempotency key conflicts with the committed request")
	ErrRunVersionConflict     = errors.New("candidate verification run version or fence changed")
	ErrRunTransition          = errors.New("invalid candidate verification run transition")
	ErrRunStoreIntegrity      = errors.New("candidate verification run persistence integrity failure")
	ErrRunStoreDown           = errors.New("candidate verification run persistence is unavailable")
)

type RunState string

const (
	RunQueued        RunState = "queued"
	RunClaimed       RunState = "claimed"
	RunMaterializing RunState = "materializing"
	RunPreparing     RunState = "preparing"
	RunRunning       RunState = "running"
	RunCollecting    RunState = "collecting"
	RunPassed        RunState = "passed"
	RunFailed        RunState = "failed"
	RunError         RunState = "error"
	RunCancelled     RunState = "cancelled"
	RunTimedOut      RunState = "timed_out"
)

type RunAction string

const (
	RunActionCancel              RunAction = "cancel"
	RunActionRetry               RunAction = "retry"
	RunActionViewReceipt         RunAction = "view_receipt"
	RunActionFreeze              RunAction = "freeze"
	RunActionCreateReleaseBundle RunAction = "create_release_bundle"
)

type RunBlockingCode string

const (
	RunBlockingInProgress       RunBlockingCode = "verification_in_progress"
	RunBlockingFailed           RunBlockingCode = "verification_failed"
	RunBlockingExecutionError   RunBlockingCode = "verification_execution_error"
	RunBlockingCancelled        RunBlockingCode = "verification_cancelled"
	RunBlockingTimedOut         RunBlockingCode = "verification_timed_out"
	RunBlockingCandidateChanged RunBlockingCode = "candidate_advanced"
	RunBlockingReceiptMissing   RunBlockingCode = "verification_receipt_missing"
)

type RunBlockingReason struct {
	Code      RunBlockingCode            `json:"code"`
	Actions   []RunAction                `json:"actions"`
	Detail    string                     `json:"detail"`
	SourceRef *repository.ExactReference `json:"sourceRef"`
}

type CreateRunInput struct {
	ID          string
	ProjectID   string
	Plan        PlanReference
	RequestKey  string
	RequestHash string
	Reason      string
	ParentRunID string
	RetryReason string
	CreatedBy   string
}

type CancelRunInput struct {
	ProjectID          string
	RunID              string
	ExpectedVersion    uint64
	ExpectedFenceEpoch uint64
	ActorID            string
	Reason             string
}

type runRequestFingerprint struct {
	SchemaVersion string        `json:"schemaVersion"`
	ProjectID     string        `json:"projectId"`
	Plan          PlanReference `json:"plan"`
	Reason        string        `json:"reason"`
	ParentRunID   string        `json:"parentRunId,omitempty"`
	RetryReason   string        `json:"retryReason,omitempty"`
	CreatedBy     string        `json:"createdBy"`
}

type Run struct {
	SchemaVersion  string        `json:"schemaVersion"`
	ID             string        `json:"id"`
	ProjectID      string        `json:"projectId"`
	Plan           PlanReference `json:"plan"`
	RequestKey     string        `json:"requestKey"`
	RequestHash    string        `json:"requestHash"`
	Reason         string        `json:"reason"`
	ParentRunID    string        `json:"parentRunId,omitempty"`
	RetryReason    string        `json:"retryReason,omitempty"`
	State          RunState      `json:"state"`
	Version        uint64        `json:"version"`
	FenceEpoch     uint64        `json:"fenceEpoch"`
	LeaseWorkerID  string        `json:"-"`
	LeaseEpoch     uint64        `json:"-"`
	LeaseExpiresAt *time.Time    `json:"-"`
	TerminalReason string        `json:"terminalReason,omitempty"`
	ExecutionError string        `json:"executionError,omitempty"`
	StartedAt      *time.Time    `json:"startedAt,omitempty"`
	FinishedAt     *time.Time    `json:"finishedAt,omitempty"`
	CreatedBy      string        `json:"createdBy"`
	UpdatedBy      string        `json:"updatedBy"`
	CreatedAt      time.Time     `json:"createdAt"`
	UpdatedAt      time.Time     `json:"updatedAt"`
	Replayed       bool          `json:"replayed"`
}

type AttemptSummary struct {
	ID              string     `json:"id"`
	Ordinal         int        `json:"ordinal"`
	ParentAttemptID string     `json:"parentAttemptId,omitempty"`
	RetryReason     string     `json:"retryReason,omitempty"`
	State           RunState   `json:"state"`
	Version         uint64     `json:"version"`
	FenceEpoch      uint64     `json:"fenceEpoch"`
	TerminalReason  string     `json:"terminalReason,omitempty"`
	ExecutionError  string     `json:"executionError,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type CheckPage struct {
	RunID      string                    `json:"runId"`
	Receipt    repository.ExactReference `json:"receipt"`
	Offset     int                       `json:"offset"`
	Limit      int                       `json:"limit"`
	TotalCount int                       `json:"totalCount"`
	Checks     []CheckResult             `json:"checks"`
}

type RunView struct {
	Run                 Run                        `json:"run"`
	Subject             CandidatePlanSubject       `json:"subject"`
	BuildManifest       repository.ExactReference  `json:"buildManifest"`
	BuildContract       repository.ExactReference  `json:"buildContract"`
	FullStackTemplate   repository.ExactReference  `json:"fullStackTemplate"`
	Profile             ProfileReference           `json:"verificationProfile"`
	Receipt             *repository.ExactReference `json:"receipt"`
	ReceiptDecision     *Decision                  `json:"receiptDecision"`
	CheckCount          int                        `json:"checkCount"`
	RequiredCheckCount  int                        `json:"requiredCheckCount"`
	CompletedCheckCount int                        `json:"completedCheckCount"`
	AttemptCount        int                        `json:"attemptCount"`
	LatestAttempt       *AttemptSummary            `json:"latestAttempt"`
	MustCount           int                        `json:"mustCount"`
	MustPassedCount     int                        `json:"mustPassedCount"`
	BlockerCount        int                        `json:"blockerCount"`
	WarningCount        int                        `json:"warningCount"`
	Stale               bool                       `json:"stale"`
	AllowedActions      []RunAction                `json:"allowedActions"`
	BlockingReasons     []RunBlockingReason        `json:"blockingReasons"`
}

func PrepareCreateRunInput(input CreateRunInput) (CreateRunInput, error) {
	normalized, err := normalizeCreateRunInput(input, false)
	if err != nil {
		return CreateRunInput{}, err
	}
	hash, err := domain.CanonicalHash(runFingerprint(normalized))
	if err != nil {
		return CreateRunInput{}, runInvalid("request fingerprint")
	}
	normalized.RequestHash = "sha256:" + hash
	return normalized, nil
}

func normalizeCreateRunInput(input CreateRunInput, requireHash bool) (CreateRunInput, error) {
	input.RequestKey = strings.TrimSpace(input.RequestKey)
	input.Reason = strings.TrimSpace(input.Reason)
	input.ParentRunID = strings.TrimSpace(input.ParentRunID)
	input.RetryReason = strings.TrimSpace(input.RetryReason)
	if !validUUIDs(input.ID, input.ProjectID, input.Plan.ID, input.CreatedBy) ||
		!exactSHA256(input.Plan.ContentHash) || input.RequestKey == "" || len(input.RequestKey) > 128 ||
		strings.ContainsRune(input.RequestKey, '\x00') || input.Reason == "" || len(input.Reason) > 1000 ||
		strings.ContainsRune(input.Reason, '\x00') {
		return CreateRunInput{}, runInvalid("identity, Plan, request key, actor, or reason")
	}
	if (input.ParentRunID == "") != (input.RetryReason == "") ||
		input.ParentRunID != "" && !validUUIDs(input.ParentRunID) ||
		len(input.RetryReason) > 1000 || strings.ContainsRune(input.RetryReason, '\x00') {
		return CreateRunInput{}, runInvalid("retry parent or reason")
	}
	if requireHash {
		hash, err := domain.CanonicalHash(runFingerprint(input))
		if err != nil || input.RequestHash != "sha256:"+hash {
			return CreateRunInput{}, runInvalid("request hash")
		}
	}
	return input, nil
}

func runFingerprint(input CreateRunInput) runRequestFingerprint {
	return runRequestFingerprint{
		SchemaVersion: RunRequestSchemaVersion, ProjectID: input.ProjectID, Plan: input.Plan,
		Reason: input.Reason, ParentRunID: input.ParentRunID,
		RetryReason: input.RetryReason, CreatedBy: input.CreatedBy,
	}
}

func runStateIsTerminal(state RunState) bool {
	switch state {
	case RunPassed, RunFailed, RunError, RunCancelled, RunTimedOut:
		return true
	default:
		return false
	}
}

func validRunState(state RunState) bool {
	return state == RunQueued || state == RunClaimed || state == RunMaterializing ||
		state == RunPreparing || state == RunRunning || state == RunCollecting || runStateIsTerminal(state)
}

func runInvalid(detail string) error {
	return fmt.Errorf("%w: %s", ErrInvalidRun, detail)
}
