package core

import (
	"testing"

	"github.com/google/uuid"
)

func TestStableUniqueApprovalUUIDsUsesCanonicalOrder(t *testing.T) {
	t.Parallel()

	lowest := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	middle := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	highest := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")

	got := stableUniqueApprovalUUIDs([]uuid.UUID{highest, middle, lowest, highest, lowest})
	want := []uuid.UUID{lowest, middle, highest}
	if len(got) != len(want) {
		t.Fatalf("stable UUID set length = %d, want %d: %#v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("stable UUID set[%d] = %s, want %s", index, got[index], want[index])
		}
	}
}
