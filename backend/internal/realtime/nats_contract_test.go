package realtime

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestWorkflowRunOutboxEventsUseStableRealtimeEnvelopeType(t *testing.T) {
	header := nats.Header{}
	header.Set("Worksflow-Event-Type", "node.review_approved")
	event, err := domainEventFromRaw(
		"worksflow.workflow.run.event",
		header,
		[]byte(`{"projectId":"project-1","runId":"run-1","type":"node.review_approved"}`),
		42,
		time.Date(2026, time.July, 11, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != "run.event" || event.ProjectID != "project-1" || event.RunID != "run-1" {
		t.Fatalf("workflow realtime event = %+v", event)
	}
}
