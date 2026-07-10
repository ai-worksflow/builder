package events

import (
	"testing"
	"time"
)

func TestDefaultOutboxConfigIsValid(t *testing.T) {
	t.Parallel()
	config := DefaultOutboxConfig()
	if config.BatchSize < 1 || config.PollInterval <= 0 || config.ClaimTTL <= time.Second ||
		config.MaxAttempts < 1 || config.PublishWait <= 0 {
		t.Fatalf("invalid default outbox configuration: %#v", config)
	}
}
