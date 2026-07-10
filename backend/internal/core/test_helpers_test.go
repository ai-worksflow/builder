package core

import (
	"testing"

	"github.com/google/uuid"
)

func mustUUID(t *testing.T, value string) uuid.UUID {
	t.Helper()
	parsed, err := uuid.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
