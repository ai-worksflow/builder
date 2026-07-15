package postgres

import (
	"fmt"
	"sort"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const deliverySliceProjectLockPrefix = "delivery_slices:"

// LockDeliverySliceProjects serializes the health-to-delivery mutation range
// for each project. Row locks alone cannot protect a delivery slice that is
// inserted after a writer discovers its current target set. Every production
// delivery_slices writer acquires these transaction-scoped advisory locks
// before any target row lock or write, then keeps its narrower UUID row locks.
func LockDeliverySliceProjects(transaction *gorm.DB, projectIDs ...uuid.UUID) error {
	if transaction == nil {
		return fmt.Errorf("delivery slice project lock requires a transaction")
	}
	ordered, err := orderedDeliverySliceProjectIDs(projectIDs)
	if err != nil {
		return err
	}
	for _, projectID := range ordered {
		scope := deliverySliceProjectLockPrefix + projectID.String()
		if err := transaction.Exec(
			"SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", scope,
		).Error; err != nil {
			return fmt.Errorf("lock delivery slices for project %s: %w", projectID, err)
		}
	}
	return nil
}

func orderedDeliverySliceProjectIDs(projectIDs []uuid.UUID) ([]uuid.UUID, error) {
	unique := make(map[uuid.UUID]struct{}, len(projectIDs))
	ordered := make([]uuid.UUID, 0, len(projectIDs))
	for _, projectID := range projectIDs {
		if projectID == uuid.Nil {
			return nil, fmt.Errorf("delivery slice project lock requires a non-zero project id")
		}
		if _, exists := unique[projectID]; exists {
			continue
		}
		unique[projectID] = struct{}{}
		ordered = append(ordered, projectID)
	}
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].String() < ordered[right].String()
	})
	return ordered, nil
}
