package postgres

import (
	"testing"

	"github.com/google/uuid"
)

func TestDeliverySliceProjectLocksUseStableUniqueUUIDOrder(t *testing.T) {
	t.Parallel()
	low := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	middle := uuid.MustParse("00000000-0000-0000-0000-000000000010")
	high := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	ordered, err := orderedDeliverySliceProjectIDs([]uuid.UUID{high, low, middle, high, low})
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 3 || ordered[0] != low || ordered[1] != middle || ordered[2] != high {
		t.Fatalf("delivery slice project lock order = %v", ordered)
	}
	if _, err := orderedDeliverySliceProjectIDs([]uuid.UUID{low, uuid.Nil}); err == nil {
		t.Fatal("zero project ID was accepted by the DeliverySlice lock protocol")
	}
}
