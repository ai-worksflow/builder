package lsp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	ErrRateLimited      = errors.New("LSP admission is rate limited")
	ErrAuditUnavailable = errors.New("LSP security audit is unavailable")
)

const (
	TicketAuditIssue   = "ticket.issue"
	TicketAuditConsume = "ticket.consume"
)

// TicketRateLimitRequest contains authority identifiers only. Implementations
// must hash identifiers before using them in infrastructure keys.
type TicketRateLimitRequest struct {
	TenantID   string
	ProjectID  string
	ActorID    string
	SessionID  string
	ProfileIDs []string
	Method     string
}

type TicketRateLimitDecision struct {
	Allowed    bool
	RetryAfter time.Duration
}

type TicketRateLimiter interface {
	Allow(context.Context, TicketRateLimitRequest) (TicketRateLimitDecision, error)
}

type TicketAuditProfile struct {
	ID               string `json:"id"`
	ContentHash      string `json:"contentHash"`
	Image            string `json:"image"`
	ExecutableDigest string `json:"executableDigest"`
	CapabilityHash   string `json:"capabilityHash"`
}

// TicketAuditEvent deliberately has no field capable of carrying a bearer
// ticket, source text, unsaved document text, diagnostics, or LSP result body.
type TicketAuditEvent struct {
	Action          string
	Outcome         string
	Code            string
	TicketID        string
	ProjectID       string
	ActorID         string
	SessionID       string
	Mode            TicketMode
	OriginHash      string
	Head            SandboxHeadFence
	TemplateRelease ExactTemplateRelease
	Profiles        []TicketAuditProfile
	OccurredAt      time.Time
}

type TicketAuditSink interface {
	Append(context.Context, TicketAuditEvent) error
}

// SecuredTicketService is the production-facing ticket boundary. The wrapped
// TicketService owns capability semantics; this layer makes Redis rate limits
// and durable privacy-preserving audit mandatory and fail closed.
type SecuredTicketService struct {
	tickets *TicketService
	limiter TicketRateLimiter
	audit   TicketAuditSink
	now     func() time.Time
}

func NewSecuredTicketService(
	tickets *TicketService,
	limiter TicketRateLimiter,
	audit TicketAuditSink,
	now func() time.Time,
) (*SecuredTicketService, error) {
	if tickets == nil || limiter == nil || audit == nil || now == nil {
		return nil, ErrTicketUnavailable
	}
	return &SecuredTicketService{tickets: tickets, limiter: limiter, audit: audit, now: now}, nil
}

func (service *SecuredTicketService) Issue(
	ctx context.Context,
	input IssueTicketInput,
) (TicketView, error) {
	event := service.issueEvent(input)
	if service == nil || ctx == nil {
		return TicketView{}, ErrTicketUnavailable
	}
	if err := validateTicketRateScope(input.ProjectID, input.ActorID, input.SessionID, input.ProfileIDs); err != nil {
		return TicketView{}, service.finish(ctx, event, "rejected", "lsp_message_malformed", ErrTicketInvalid)
	}
	decision, err := service.limiter.Allow(ctx, TicketRateLimitRequest{
		TenantID: input.ProjectID, ProjectID: input.ProjectID, ActorID: input.ActorID,
		SessionID: input.SessionID, ProfileIDs: append([]string(nil), input.ProfileIDs...),
		Method: TicketAuditIssue,
	})
	if err != nil {
		return TicketView{}, service.finish(ctx, event, "rejected", "lsp_ticket_store_unavailable", ErrTicketUnavailable)
	}
	if !decision.Allowed {
		return TicketView{}, service.finish(ctx, event, "rate_limited", "lsp_rate_limited", ErrRateLimited)
	}
	view, issueErr := service.tickets.Issue(ctx, input)
	if issueErr != nil {
		return TicketView{}, service.finish(ctx, event, "rejected", ticketErrorCode(issueErr), issueErr)
	}
	event.TicketID = view.ID
	event.Profiles = auditProfiles(view.Profiles)
	if err := service.finish(ctx, event, "issued", "ok", nil); err != nil {
		// The secret has not been returned, but burn the Redis grant as well so
		// an audit outage does not leave even an undisclosed live capability.
		burnContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_, _ = service.tickets.store.Consume(burnContext, view.Ticket)
		cancel()
		return TicketView{}, err
	}
	return view, nil
}

func (service *SecuredTicketService) Consume(
	ctx context.Context,
	secret, origin string,
) (TicketGrant, error) {
	if service == nil || ctx == nil {
		return TicketGrant{}, ErrTicketUnavailable
	}
	event := TicketAuditEvent{
		Action: TicketAuditConsume, OriginHash: hashAuditOrigin(origin), OccurredAt: service.now().UTC(),
	}
	grant, consumeErr := service.tickets.Consume(ctx, secret, origin)
	if consumeErr != nil {
		return TicketGrant{}, service.finish(ctx, event, "rejected", ticketErrorCode(consumeErr), consumeErr)
	}
	event.TicketID = grant.ID
	event.ProjectID, event.ActorID, event.SessionID = grant.ProjectID, grant.ActorID, grant.SessionID
	event.Mode, event.Head, event.TemplateRelease = grant.Mode, grant.Head, grant.TemplateRelease
	event.Profiles = auditProfiles(grant.Profiles)
	profileIDs := make([]string, len(grant.Profiles))
	for index, profile := range grant.Profiles {
		profileIDs[index] = profile.ID
	}
	decision, err := service.limiter.Allow(ctx, TicketRateLimitRequest{
		TenantID: grant.ProjectID, ProjectID: grant.ProjectID, ActorID: grant.ActorID,
		SessionID: grant.SessionID, ProfileIDs: profileIDs, Method: TicketAuditConsume,
	})
	if err != nil {
		return TicketGrant{}, service.finish(ctx, event, "rejected", "lsp_ticket_store_unavailable", ErrTicketUnavailable)
	}
	if !decision.Allowed {
		return TicketGrant{}, service.finish(ctx, event, "rate_limited", "lsp_rate_limited", ErrRateLimited)
	}
	if err := service.finish(ctx, event, "consumed", "ok", nil); err != nil {
		return TicketGrant{}, err
	}
	return grant, nil
}

func (service *SecuredTicketService) issueEvent(input IssueTicketInput) TicketAuditEvent {
	event := TicketAuditEvent{
		Action: TicketAuditIssue, ProjectID: input.ProjectID, ActorID: input.ActorID,
		SessionID: input.SessionID, Mode: input.Mode, OriginHash: hashAuditOrigin(input.Origin),
		Head: input.Head, TemplateRelease: input.TemplateRelease,
	}
	if service != nil && service.now != nil {
		event.OccurredAt = service.now().UTC()
	}
	return event
}

func (service *SecuredTicketService) finish(
	ctx context.Context,
	event TicketAuditEvent,
	outcome, code string,
	result error,
) error {
	event.Outcome, event.Code = outcome, code
	if event.OccurredAt.IsZero() {
		event.OccurredAt = service.now().UTC()
	}
	if err := service.audit.Append(ctx, event); err != nil {
		return fmt.Errorf("%w: append ticket event", ErrAuditUnavailable)
	}
	return result
}

func validateTicketRateScope(projectID, actorID, sessionID string, profileIDs []string) error {
	if !canonicalUUID(projectID) || !canonicalUUID(actorID) || !canonicalUUID(sessionID) {
		return ErrTicketInvalid
	}
	_, err := validateRequestedProfiles(profileIDs)
	return err
}

func auditProfiles(profiles []ProfileIdentity) []TicketAuditProfile {
	result := make([]TicketAuditProfile, len(profiles))
	for index, profile := range profiles {
		result[index] = TicketAuditProfile{
			ID: profile.ID, ContentHash: profile.ContentHash, Image: profile.Runtime.Image,
			ExecutableDigest: profile.Runtime.ExecutableDigest, CapabilityHash: profile.CapabilityHash,
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

func hashAuditOrigin(origin string) string {
	value := strings.TrimSpace(origin)
	if normalized, err := normalizeTicketOrigin(value); err == nil {
		value = normalized
	}
	if len(value) > 2_048 {
		value = value[:2_048]
	}
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", digest[:])
}

func ticketErrorCode(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrRateLimited):
		return "lsp_rate_limited"
	case errors.Is(err, ErrOriginForbidden):
		return "lsp_origin_forbidden"
	case errors.Is(err, ErrForbidden):
		return "lsp_forbidden"
	case errors.Is(err, ErrSessionNotReady):
		return "lsp_session_not_ready"
	case errors.Is(err, ErrHeadStale):
		return "lsp_head_stale"
	case errors.Is(err, ErrProfileNotDeclared):
		return "lsp_profile_not_declared"
	case errors.Is(err, ErrTicketConsumed):
		return "lsp_ticket_rejected"
	case errors.Is(err, ErrTicketInvalid):
		return "lsp_message_malformed"
	default:
		return "lsp_ticket_store_unavailable"
	}
}
