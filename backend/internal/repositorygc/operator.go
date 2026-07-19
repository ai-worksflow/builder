package repositorygc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const ResultSchemaVersion = "repository-exact-tree-literal-index-gc-result/v1"

var (
	ErrNotConfigured       = errors.New("repository exact-tree index GC operator is not configured")
	ErrAuthorityContract   = errors.New("repository exact-tree index GC database authority contract violation")
	ErrExecutionUnresolved = errors.New("repository exact-tree index GC execution outcome is unresolved")
)

type RunState string

const (
	RunStatePlanned           RunState = "planned"
	RunStatePartiallyExecuted RunState = "partially_executed"
	RunStateCompleted         RunState = "completed"
)

// Result is the canonical, non-secret one-shot command result. Counts and byte
// totals come only from PostgreSQL; the Go process never infers them from a
// working-tree or object-store snapshot.
type Result struct {
	SchemaVersion        string    `json:"schemaVersion"`
	RunID                uuid.UUID `json:"runId"`
	Planned              int64     `json:"planned"`
	Deleted              int64     `json:"deleted"`
	Protected            int64     `json:"protected"`
	Stale                int64     `json:"stale"`
	Expired              int64     `json:"expired"`
	LogicalBytesReleased int64     `json:"logicalBytesReleased"`
	BlobBytesFreed       int64     `json:"blobBytesFreed"`
}

type PlanInput struct {
	RunID          uuid.UUID
	Retention      time.Duration
	KeepPerProject int
	BatchSize      int
	CapabilityTTL  time.Duration
}

// Capability is an opaque, database-issued permission to collect one exact
// immutable tree. Project IDs, tree hashes, and commitments deliberately stay
// inside PostgreSQL and are not needed by this operator.
type Capability struct {
	RunID        uuid.UUID
	CapabilityID uuid.UUID
	ExpiresAt    time.Time
}

type Receipt struct {
	ReceiptID    uuid.UUID
	RunID        uuid.UUID
	CapabilityID uuid.UUID
	Outcome      Outcome
	Idempotent   bool
}

type Outcome string

const (
	OutcomeDeleted   Outcome = "deleted"
	OutcomeProtected Outcome = "protected"
	OutcomeStale     Outcome = "stale"
	OutcomeExpired   Outcome = "expired"
)

type Inspection struct {
	RunID  uuid.UUID
	State  RunState
	Result Result
}

type Authority interface {
	Readiness(context.Context) error
	Plan(context.Context, PlanInput) ([]Capability, error)
	Execute(context.Context, uuid.UUID) (Receipt, error)
	Inspect(context.Context, uuid.UUID) (Inspection, error)
}

type Operator struct {
	authority Authority
}

func New(authority Authority) (*Operator, error) {
	if authority == nil {
		return nil, ErrNotConfigured
	}
	return &Operator{authority: authority}, nil
}

// Run plans and executes one bounded collection batch. Every retry preserves
// the original run and capability identities. No replacement run is planned to
// conceal an uncertain database outcome.
func (operator *Operator) Run(ctx context.Context, runID uuid.UUID, policy Policy) (Result, error) {
	if operator == nil || operator.authority == nil {
		return Result{}, ErrNotConfigured
	}
	if ctx == nil {
		return Result{}, fmt.Errorf("%w: context is required", ErrNotConfigured)
	}
	if err := policy.Validate(); err != nil {
		return Result{}, err
	}
	if runID == uuid.Nil {
		return Result{}, fmt.Errorf("%w: stable run ID is required", ErrAuthorityContract)
	}
	if err := operator.authority.Readiness(ctx); err != nil {
		return Result{}, fmt.Errorf("repository exact-tree index GC readiness: %w", err)
	}

	input := PlanInput{
		RunID: runID, Retention: policy.Retention,
		KeepPerProject: policy.KeepPerProject, BatchSize: policy.BatchSize,
		CapabilityTTL: policy.CapabilityTTL,
	}
	capabilities, err := operator.authority.Plan(ctx, input)
	if err != nil {
		// The plan function is idempotent for one run ID plus exact parameters.
		// Retrying that exact tuple reconciles a response lost after commit.
		capabilities, err = operator.authority.Plan(ctx, input)
		if err != nil {
			return Result{}, fmt.Errorf("%w: plan retry with the same run ID failed: %v", ErrExecutionUnresolved, err)
		}
	}
	if err := validateCapabilities(capabilities, runID, policy.BatchSize); err != nil {
		return Result{}, err
	}

	for _, capability := range capabilities {
		receipt, executeErr := operator.authority.Execute(ctx, capability.CapabilityID)
		if executeErr != nil {
			// The same capability is deliberately retried before aggregate
			// inspection: a committed first call must replay its durable receipt.
			receipt, executeErr = operator.authority.Execute(ctx, capability.CapabilityID)
		}
		if executeErr != nil {
			inspection, inspectErr := operator.authority.Inspect(ctx, runID)
			if inspectErr == nil {
				if result, terminal, validationErr := terminalResult(inspection, runID, policy.BatchSize); validationErr != nil {
					return Result{}, validationErr
				} else if terminal {
					return result, nil
				}
			}
			return Result{}, fmt.Errorf("%w: a planned capability did not return a receipt after an exact retry", ErrExecutionUnresolved)
		}
		if err := validateReceipt(receipt, runID, capability.CapabilityID); err != nil {
			return Result{}, err
		}
	}

	inspection, err := operator.authority.Inspect(ctx, runID)
	if err != nil {
		return Result{}, fmt.Errorf("inspect repository exact-tree index GC run: %w", err)
	}
	result, terminal, err := terminalResult(inspection, runID, policy.BatchSize)
	if err != nil {
		return Result{}, err
	}
	if !terminal {
		return Result{}, fmt.Errorf("%w: run ended in state %q", ErrExecutionUnresolved, inspection.State)
	}
	return result, nil
}

func validateCapabilities(capabilities []Capability, runID uuid.UUID, batchSize int) error {
	if len(capabilities) > batchSize {
		return fmt.Errorf("%w: plan returned %d capabilities for batch size %d", ErrAuthorityContract, len(capabilities), batchSize)
	}
	seen := make(map[uuid.UUID]struct{}, len(capabilities))
	for _, capability := range capabilities {
		if capability.RunID != runID || capability.CapabilityID == uuid.Nil || capability.ExpiresAt.IsZero() {
			return fmt.Errorf("%w: planned capability has invalid identity or expiry", ErrAuthorityContract)
		}
		if _, duplicated := seen[capability.CapabilityID]; duplicated {
			return fmt.Errorf("%w: plan returned a duplicate capability", ErrAuthorityContract)
		}
		seen[capability.CapabilityID] = struct{}{}
	}
	return nil
}

func validateReceipt(receipt Receipt, runID, capabilityID uuid.UUID) error {
	if receipt.ReceiptID == uuid.Nil || receipt.RunID != runID || receipt.CapabilityID != capabilityID {
		return fmt.Errorf("%w: execution receipt identity mismatch", ErrAuthorityContract)
	}
	switch receipt.Outcome {
	case OutcomeDeleted, OutcomeProtected, OutcomeStale, OutcomeExpired:
		// Exact terminal outcomes are issued by PostgreSQL.
	default:
		return fmt.Errorf("%w: execution receipt outcome is invalid", ErrAuthorityContract)
	}
	return nil
}

func terminalResult(inspection Inspection, runID uuid.UUID, batchSize int) (Result, bool, error) {
	if inspection.RunID != runID || inspection.Result.RunID != runID ||
		inspection.Result.SchemaVersion != ResultSchemaVersion {
		return Result{}, false, fmt.Errorf("%w: inspection result identity or schema mismatch", ErrAuthorityContract)
	}
	result := inspection.Result
	values := []int64{
		result.Planned, result.Deleted, result.Protected, result.Stale,
		result.Expired, result.LogicalBytesReleased, result.BlobBytesFreed,
	}
	for _, value := range values {
		if value < 0 {
			return Result{}, false, fmt.Errorf("%w: inspection contains a negative count or byte total", ErrAuthorityContract)
		}
	}
	if result.Planned > int64(batchSize) || result.Deleted+result.Protected+result.Stale+result.Expired > result.Planned {
		return Result{}, false, fmt.Errorf("%w: inspection terminal counts are inconsistent", ErrAuthorityContract)
	}
	switch inspection.State {
	case RunStateCompleted:
		if result.Deleted+result.Protected+result.Stale+result.Expired != result.Planned {
			return Result{}, false, fmt.Errorf("%w: terminal run has pending capabilities", ErrAuthorityContract)
		}
		return result, true, nil
	case RunStatePlanned, RunStatePartiallyExecuted:
		return Result{}, false, nil
	default:
		return Result{}, false, fmt.Errorf("%w: unsupported run state %q", ErrAuthorityContract, inspection.State)
	}
}
