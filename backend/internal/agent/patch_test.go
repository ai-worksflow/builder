package agent

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

func TestParsePlatformPatchRejectsOperationCountBeyondQualifiedSchema(t *testing.T) {
	operations := make([]repository.FileOperation, MaxPlatformPatchOperations+1)
	for index := range operations {
		operations[index] = repository.FileOperation{
			ID:           fmt.Sprintf("operation-%04d", index),
			Kind:         repository.OperationDelete,
			Path:         fmt.Sprintf("apps/web/file-%04d.ts", index),
			ExpectedHash: testHash("a"),
		}
	}
	patch := PlatformPatch{
		SchemaVersion: PlatformPatchSchemaVersion,
		AttemptID:     uuid.NewString(), ProjectID: uuid.NewString(), CandidateID: uuid.NewString(),
		TaskCapsule:       repository.ExactReference{ID: uuid.NewString(), ContentHash: testHash("b")},
		ConfigurationHash: testHash("c"), BaseTreeHash: testHash("d"), ProposedTreeHash: testHash("e"),
		Operations: operations, ContentHash: testHash("f"),
	}
	if _, err := ParsePlatformPatch(patch); !errors.Is(err, ErrExecutionDrift) {
		t.Fatalf("oversize platform patch error = %v", err)
	}
}
