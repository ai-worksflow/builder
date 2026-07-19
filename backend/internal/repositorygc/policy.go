package repositorygc

import (
	"errors"
	"fmt"
	"time"
)

const (
	DefaultRetention       = 30 * 24 * time.Hour
	MinimumRetention       = 7 * 24 * time.Hour
	DefaultKeepPerProject  = 8
	MinimumKeepPerProject  = 8
	DefaultBatchSize       = 25
	MaximumBatchSize       = 100
	DefaultCapabilityTTL   = 10 * time.Minute
	MaximumCapabilityTTL   = 15 * time.Minute
	MinimumCommandTimeout  = time.Second
	MaximumCommandTimeout  = 30 * time.Minute
	DefaultCommandTimeout  = 5 * time.Minute
	maximumPostgresInteger = int(^uint32(0) >> 1)
)

var ErrInvalidPolicy = errors.New("invalid repository exact-tree index GC policy")

// Policy is the complete mutable input accepted by the database planning
// authority. The database remains authoritative for clocks, protection checks,
// candidate selection, byte accounting, and deletion.
type Policy struct {
	Retention      time.Duration
	KeepPerProject int
	BatchSize      int
	CapabilityTTL  time.Duration
}

func DefaultPolicy() Policy {
	return Policy{
		Retention:      DefaultRetention,
		KeepPerProject: DefaultKeepPerProject,
		BatchSize:      DefaultBatchSize,
		CapabilityTTL:  DefaultCapabilityTTL,
	}
}

func (policy Policy) Validate() error {
	violations := make([]string, 0, 4)
	if policy.Retention < MinimumRetention || policy.Retention%time.Millisecond != 0 {
		violations = append(violations, fmt.Sprintf("retention must be at least %s and use whole milliseconds", MinimumRetention))
	}
	if policy.KeepPerProject < MinimumKeepPerProject || policy.KeepPerProject > maximumPostgresInteger {
		violations = append(violations, fmt.Sprintf("keep per project must be between %d and %d", MinimumKeepPerProject, maximumPostgresInteger))
	}
	if policy.BatchSize < 1 || policy.BatchSize > MaximumBatchSize {
		violations = append(violations, fmt.Sprintf("batch size must be between 1 and %d", MaximumBatchSize))
	}
	if policy.CapabilityTTL <= 0 || policy.CapabilityTTL > MaximumCapabilityTTL || policy.CapabilityTTL%time.Millisecond != 0 {
		violations = append(violations, fmt.Sprintf("capability TTL must be positive, no greater than %s, and use whole milliseconds", MaximumCapabilityTTL))
	}
	if len(violations) > 0 {
		return fmt.Errorf("%w: %v", ErrInvalidPolicy, violations)
	}
	return nil
}
