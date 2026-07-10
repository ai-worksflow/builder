package conversation

import (
	"testing"

	"github.com/google/uuid"
)

func TestDefaultProjectConversationIDIsDeterministicAndProjectScoped(t *testing.T) {
	projectA, projectB := uuid.New(), uuid.New()
	first := DefaultProjectConversationID(projectA)
	if first == uuid.Nil || first != DefaultProjectConversationID(projectA) || first == DefaultProjectConversationID(projectB) {
		t.Fatalf("default conversation identity is not deterministic/project scoped: %s", first)
	}
}
