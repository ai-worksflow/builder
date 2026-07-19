package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type ticketRateLimiterFake struct {
	decisions []TicketRateLimitDecision
	errors    []error
	requests  []TicketRateLimitRequest
}

func (limiter *ticketRateLimiterFake) Allow(
	_ context.Context,
	request TicketRateLimitRequest,
) (TicketRateLimitDecision, error) {
	limiter.requests = append(limiter.requests, request)
	index := len(limiter.requests) - 1
	if index < len(limiter.errors) && limiter.errors[index] != nil {
		return TicketRateLimitDecision{}, limiter.errors[index]
	}
	if index < len(limiter.decisions) {
		return limiter.decisions[index], nil
	}
	return TicketRateLimitDecision{Allowed: true}, nil
}

type ticketAuditSinkFake struct {
	events []TicketAuditEvent
	err    error
}

func (sink *ticketAuditSinkFake) Append(_ context.Context, event TicketAuditEvent) error {
	sink.events = append(sink.events, event)
	return sink.err
}

func securedTicketFixture(t *testing.T, mode TicketMode) (
	*SecuredTicketService,
	*ticketStoreFake,
	*ticketRateLimiterFake,
	*ticketAuditSinkFake,
	IssueTicketInput,
) {
	t.Helper()
	base, store, _, _, input, now := lspTicketFixture(t, mode)
	limiter := &ticketRateLimiterFake{}
	audit := &ticketAuditSinkFake{}
	service, err := NewSecuredTicketService(base, limiter, audit, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	return service, store, limiter, audit, input
}

func TestSecuredTicketServiceRatesAndAuditsIssueAndConsumeWithoutSecret(t *testing.T) {
	service, store, limiter, audit, input := securedTicketFixture(t, TicketModeEditor)
	view, err := service.Issue(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := service.Consume(context.Background(), view.Ticket, input.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ID != view.ID || len(limiter.requests) != 2 ||
		limiter.requests[0].Method != TicketAuditIssue ||
		limiter.requests[1].Method != TicketAuditConsume ||
		len(limiter.requests[0].ProfileIDs) != 1 || limiter.requests[0].ProfileIDs[0] != "typescript" {
		t.Fatalf("rate scopes were not exact: %#v", limiter.requests)
	}
	if len(audit.events) != 2 || audit.events[0].Outcome != "issued" ||
		audit.events[1].Outcome != "consumed" || audit.events[1].TicketID != view.ID ||
		len(audit.events[1].Profiles) != 1 || audit.events[1].Profiles[0].CapabilityHash == "" {
		t.Fatalf("audit sequence was incomplete: %#v", audit.events)
	}
	encoded, err := json.Marshal(audit.events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), view.Ticket) || strings.Contains(string(encoded), store.secret) ||
		strings.Contains(string(encoded), input.Origin) {
		t.Fatalf("audit exposed ticket or raw Origin: %s", encoded)
	}
}

func TestSecuredTicketServiceFailsClosedBeforeIssueWhenRateLimited(t *testing.T) {
	service, store, limiter, audit, input := securedTicketFixture(t, TicketModeSnapshot)
	limiter.decisions = []TicketRateLimitDecision{{Allowed: false, RetryAfter: time.Second}}
	if _, err := service.Issue(context.Background(), input); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Issue() error = %v", err)
	}
	if store.puts != 0 || len(audit.events) != 1 || audit.events[0].Outcome != "rate_limited" ||
		audit.events[0].Code != "lsp_rate_limited" {
		t.Fatalf("rate rejection was unsafe: puts=%d audit=%#v", store.puts, audit.events)
	}
}

func TestSecuredTicketServiceFailsClosedWhenAuditOrLimiterIsUnavailable(t *testing.T) {
	t.Run("issued ticket is not disclosed without durable audit", func(t *testing.T) {
		service, store, _, audit, input := securedTicketFixture(t, TicketModeSnapshot)
		audit.err = errors.New("database unavailable")
		view, err := service.Issue(context.Background(), input)
		if !errors.Is(err, ErrAuditUnavailable) || view.Ticket != "" || store.puts != 1 || !store.consumed {
			t.Fatalf("Issue() view=%#v puts=%d consumed=%v error=%v", view, store.puts, store.consumed, err)
		}
	})

	t.Run("rate store failure prevents mint", func(t *testing.T) {
		service, store, limiter, audit, input := securedTicketFixture(t, TicketModeSnapshot)
		limiter.errors = []error{errors.New("redis unavailable")}
		if _, err := service.Issue(context.Background(), input); !errors.Is(err, ErrTicketUnavailable) {
			t.Fatalf("Issue() error = %v", err)
		}
		if store.puts != 0 || len(audit.events) != 1 || audit.events[0].Outcome != "rejected" {
			t.Fatalf("rate outage did not fail closed: puts=%d audit=%#v", store.puts, audit.events)
		}
	})
}

func TestSecuredTicketServiceBurnsTicketBeforeConsumeRateFailure(t *testing.T) {
	service, store, limiter, audit, input := securedTicketFixture(t, TicketModeSnapshot)
	view, err := service.Issue(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	limiter.decisions = []TicketRateLimitDecision{{Allowed: true}, {Allowed: false}}
	if _, err := service.Consume(context.Background(), view.Ticket, input.Origin); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Consume() error = %v", err)
	}
	if !store.consumed || len(audit.events) != 2 || audit.events[1].Outcome != "rate_limited" {
		t.Fatalf("consume rejection did not burn and audit: consumed=%v audit=%#v", store.consumed, audit.events)
	}
}

func TestPostgresTicketAuditValidationRejectsSecretShapedOrUnboundedMetadata(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	event := TicketAuditEvent{
		Action: TicketAuditIssue, Outcome: "issued", Code: "ok", TicketID: testTicket,
		ProjectID: testProject, ActorID: testActor, SessionID: testSession,
		Mode: TicketModeSnapshot, OriginHash: hashAuditOrigin("https://builder.example"),
		Head: validHead(), TemplateRelease: ExactTemplateRelease{ID: testRelease, ContentHash: lspDigest("2")},
		Profiles: auditProfiles([]ProfileIdentity{lspTestProfile("typescript")}), OccurredAt: now,
	}
	if err := validateTicketAuditEvent(event); err != nil {
		t.Fatal(err)
	}
	metadata, err := encodeTicketAuditMetadata(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), "builder.example") || strings.Contains(string(metadata), strings.Repeat("A", 43)) {
		t.Fatalf("audit metadata exposed forbidden material: %s", metadata)
	}
	event.Profiles[0].Image = strings.Repeat("x", 1_025) + "@sha256:" + strings.Repeat("a", 64)
	if err := validateTicketAuditEvent(event); !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("unbounded profile audit error = %v", err)
	}
}
