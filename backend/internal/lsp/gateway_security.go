package lsp

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/templates"
)

var (
	ErrGatewayRequestRateLimited  = errors.New("LSP Gateway request rate is limited")
	ErrGatewaySecurityUnavailable = errors.New("LSP Gateway security boundary is unavailable")
)

const (
	// Production keeps the distributed lease longer than the browser heartbeat
	// deadline. The first post-start renewal happens only after the strict bind
	// has been revalidated and server.bound has been written.
	GatewayEditorLeaseTTL          = 30 * time.Second
	GatewayEditorHeartbeatInterval = 10 * time.Second
)

const (
	GatewayAuditBindingOpen   = "binding.open"
	GatewayAuditBindingRebind = "binding.rebind"
	GatewayAuditBindingClose  = "binding.close"

	GatewayAuditEditorLeaseAcquire  = "editor_lease.acquire"
	GatewayAuditEditorLeaseConflict = "editor_lease.conflict"
	GatewayAuditEditorLeaseLost     = "editor_lease.lost"
	GatewayAuditEditorLeaseRelease  = "editor_lease.release"

	GatewayAuditRequestAdmitted  = "request.admitted"
	GatewayAuditRequestCompleted = "request.completed"
	GatewayAuditRequestCancel    = "request.cancel"
	GatewayAuditRequestTimeout   = "request.timeout"
	GatewayAuditRequestStale     = "request.stale"
	GatewayAuditRequestError     = "request.error"

	GatewayAuditServerViolation = "server.violation"
)

var gatewayAuditCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,80}$`)

// GatewayRequestRateLimitInput is deliberately payload-free. The exact
// profile commitments and method are sufficient to derive every admission
// scope without retaining source text or request parameters.
type GatewayRequestRateLimitInput struct {
	ProjectID          string
	ActorID            string
	SessionID          string
	ProfileID          string
	ProfileContentHash string
	CapabilityHash     string
	Method             string
	RequestsPerSecond  int
	RequestBurst       int
}

type GatewayRequestRateLimitDecision struct {
	Allowed    bool
	RetryAfter time.Duration
}

type GatewayRequestRateLimiter interface {
	AllowGatewayRequest(context.Context, GatewayRequestRateLimitInput) (GatewayRequestRateLimitDecision, error)
}

// GatewayEditorLeaseInput contains only the exact editor authority tuple and
// its unguessable per-connection binding owner. ActorID is deliberately absent:
// actor handoff must not permit overlapping editor owners for one session and
// exact profile.
type GatewayEditorLeaseInput struct {
	ProjectID          string
	SessionID          string
	ProfileID          string
	ProfileContentHash string
	CapabilityHash     string
	OwnerBindingID     string
}

type GatewayEditorLeaseContract struct {
	TTL               time.Duration
	HeartbeatInterval time.Duration
}

// GatewayEditorLeaseStore must implement atomic compare-owner operations. A
// false renew/release is a fencing result, not an invitation to recreate the
// key. Snapshot bindings never invoke this interface.
type GatewayEditorLeaseStore interface {
	Contract() GatewayEditorLeaseContract
	AcquireGatewayEditorLease(context.Context, GatewayEditorLeaseInput) (bool, error)
	RenewGatewayEditorLease(context.Context, GatewayEditorLeaseInput) (bool, error)
	ReleaseGatewayEditorLease(context.Context, GatewayEditorLeaseInput) (bool, error)
}

// GatewayAuditEvent has no generic payload-shaped field by design. In
// particular, it cannot represent a ticket secret, document text, request
// params, diagnostics, or a language-server result body.
type GatewayAuditEvent struct {
	Action          string
	Outcome         string
	Code            string
	TicketID        string
	ProjectID       string
	ActorID         string
	SessionID       string
	ConnectionID    string
	BindingID       string
	Mode            TicketMode
	Head            SandboxHeadFence
	Document        *DocumentFence
	TemplateRelease ExactTemplateRelease
	Profile         TicketAuditProfile
	RequestID       string
	Method          string
	LatencyMillis   int64
	ServerViolation *GatewayServerViolationAudit
	OccurredAt      time.Time
}

// GatewayServerViolationAudit is the only durable projection of a rejected
// server callback. Method is either a fixed allowlisted callback name or one
// of the fixed unknown request/notification classifications; it is never a raw
// unrecognized server string. Ordinal is the binding-wide violation sequence
// and Count is the occurrence count for that exact safe classification.
// Request IDs, params, server text, and source never enter the audit shape.
type GatewayServerViolationAudit struct {
	Method  string `json:"method"`
	Ordinal uint32 `json:"ordinal"`
	Count   uint32 `json:"count"`
}

type GatewayAuditSink interface {
	AppendGatewayAudit(context.Context, GatewayAuditEvent) error
}

// GatewaySecurityBoundary is mandatory in the exported production Gateway
// constructor. Tests use an explicit test-only implementation rather than an
// implicit production fallback.
type GatewaySecurityBoundary interface {
	AllowGatewayRequest(context.Context, GatewayRequestRateLimitInput) (GatewayRequestRateLimitDecision, error)
	AcquireGatewayEditorLease(context.Context, GatewayEditorLeaseInput) (GatewayEditorLeaseContract, bool, error)
	RenewGatewayEditorLease(context.Context, GatewayEditorLeaseInput) (bool, error)
	ReleaseGatewayEditorLease(context.Context, GatewayEditorLeaseInput) (bool, error)
	AppendGatewayAudit(context.Context, GatewayAuditEvent) error
}

type GatewaySecurityService struct {
	limiter GatewayRequestRateLimiter
	audit   GatewayAuditSink
	leases  GatewayEditorLeaseStore
}

func NewGatewaySecurity(
	limiter GatewayRequestRateLimiter,
	audit GatewayAuditSink,
	leases GatewayEditorLeaseStore,
) (*GatewaySecurityService, error) {
	if limiter == nil || audit == nil || leases == nil ||
		validateGatewayEditorLeaseContract(leases.Contract()) != nil {
		return nil, ErrGatewaySecurityUnavailable
	}
	return &GatewaySecurityService{limiter: limiter, audit: audit, leases: leases}, nil
}

func (service *GatewaySecurityService) AllowGatewayRequest(
	ctx context.Context,
	input GatewayRequestRateLimitInput,
) (GatewayRequestRateLimitDecision, error) {
	if service == nil || service.limiter == nil || ctx == nil || validateGatewayRateInput(input) != nil {
		return GatewayRequestRateLimitDecision{}, ErrGatewaySecurityUnavailable
	}
	decision, err := service.limiter.AllowGatewayRequest(ctx, input)
	if err != nil {
		return GatewayRequestRateLimitDecision{}, errors.Join(ErrGatewaySecurityUnavailable, err)
	}
	if decision.Allowed && decision.RetryAfter != 0 || !decision.Allowed && decision.RetryAfter <= 0 {
		return GatewayRequestRateLimitDecision{}, ErrGatewaySecurityUnavailable
	}
	return decision, nil
}

func (service *GatewaySecurityService) AppendGatewayAudit(
	ctx context.Context,
	event GatewayAuditEvent,
) error {
	if service == nil || service.audit == nil || ctx == nil || validateGatewayAuditEvent(event) != nil {
		return ErrGatewaySecurityUnavailable
	}
	if err := service.audit.AppendGatewayAudit(ctx, event); err != nil {
		return errors.Join(ErrGatewaySecurityUnavailable, err)
	}
	return nil
}

func (service *GatewaySecurityService) AcquireGatewayEditorLease(
	ctx context.Context,
	input GatewayEditorLeaseInput,
) (GatewayEditorLeaseContract, bool, error) {
	if service == nil || service.leases == nil || ctx == nil || validateGatewayEditorLeaseInput(input) != nil {
		return GatewayEditorLeaseContract{}, false, ErrGatewaySecurityUnavailable
	}
	contract := service.leases.Contract()
	if validateGatewayEditorLeaseContract(contract) != nil {
		return GatewayEditorLeaseContract{}, false, ErrGatewaySecurityUnavailable
	}
	acquired, err := service.leases.AcquireGatewayEditorLease(ctx, input)
	if err != nil {
		return GatewayEditorLeaseContract{}, false, errors.Join(ErrGatewaySecurityUnavailable, err)
	}
	return contract, acquired, nil
}

func (service *GatewaySecurityService) RenewGatewayEditorLease(
	ctx context.Context,
	input GatewayEditorLeaseInput,
) (bool, error) {
	if service == nil || service.leases == nil || ctx == nil ||
		validateGatewayEditorLeaseInput(input) != nil ||
		validateGatewayEditorLeaseContract(service.leases.Contract()) != nil {
		return false, ErrGatewaySecurityUnavailable
	}
	renewed, err := service.leases.RenewGatewayEditorLease(ctx, input)
	if err != nil {
		return false, errors.Join(ErrGatewaySecurityUnavailable, err)
	}
	return renewed, nil
}

func (service *GatewaySecurityService) ReleaseGatewayEditorLease(
	ctx context.Context,
	input GatewayEditorLeaseInput,
) (bool, error) {
	if service == nil || service.leases == nil || ctx == nil ||
		validateGatewayEditorLeaseInput(input) != nil ||
		validateGatewayEditorLeaseContract(service.leases.Contract()) != nil {
		return false, ErrGatewaySecurityUnavailable
	}
	released, err := service.leases.ReleaseGatewayEditorLease(ctx, input)
	if err != nil {
		return false, errors.Join(ErrGatewaySecurityUnavailable, err)
	}
	return released, nil
}

func validateGatewayRateInput(input GatewayRequestRateLimitInput) error {
	if !canonicalUUID(input.ProjectID) || !canonicalUUID(input.ActorID) ||
		!canonicalUUID(input.SessionID) || !profileIDPattern.MatchString(input.ProfileID) ||
		len(input.ProfileID) > 80 || !digestPattern.MatchString(input.ProfileContentHash) ||
		!digestPattern.MatchString(input.CapabilityHash) ||
		AdmitBrowserRequestMethod(input.Method, ProductionV1MethodBaseline()) != nil ||
		input.RequestsPerSecond < 1 ||
		input.RequestsPerSecond > templates.LanguageServerMaxRequestsPerSecond ||
		input.RequestBurst < input.RequestsPerSecond ||
		input.RequestBurst > templates.LanguageServerMaxRequestBurst {
		return ErrGatewaySecurityUnavailable
	}
	return nil
}

func validateGatewayEditorLeaseInput(input GatewayEditorLeaseInput) error {
	if !canonicalUUID(input.ProjectID) || !canonicalUUID(input.SessionID) ||
		!profileIDPattern.MatchString(input.ProfileID) || len(input.ProfileID) > 80 ||
		!digestPattern.MatchString(input.ProfileContentHash) ||
		!digestPattern.MatchString(input.CapabilityHash) || !canonicalUUID(input.OwnerBindingID) {
		return ErrGatewaySecurityUnavailable
	}
	return nil
}

func validateGatewayEditorLeaseContract(contract GatewayEditorLeaseContract) error {
	// The lower bound supports deterministic short integration contracts while
	// still rejecting zero/busy-loop durations. The exported Redis constructor
	// always supplies the fixed production 30s/10s contract.
	if contract.HeartbeatInterval < 10*time.Millisecond || contract.TTL < 3*contract.HeartbeatInterval ||
		contract.TTL > time.Minute || contract.HeartbeatInterval > 30*time.Second ||
		contract.TTL%time.Millisecond != 0 || contract.HeartbeatInterval%time.Millisecond != 0 {
		return ErrGatewaySecurityUnavailable
	}
	return nil
}

func validateGatewayAuditEvent(event GatewayAuditEvent) error {
	if !canonicalUUID(event.TicketID) || !canonicalUUID(event.ProjectID) ||
		!canonicalUUID(event.ActorID) || !canonicalUUID(event.SessionID) ||
		!canonicalUUID(event.ConnectionID) || !canonicalUUID(event.BindingID) ||
		event.ConnectionID == event.BindingID ||
		(event.Mode != TicketModeSnapshot && event.Mode != TicketModeEditor) ||
		event.Head.Validate() != nil || event.Head.ProjectID != event.ProjectID ||
		event.Head.SessionID != event.SessionID || event.TemplateRelease.Validate() != nil ||
		validateGatewayAuditProfile(event.Profile) != nil ||
		!gatewayAuditCodePattern.MatchString(event.Code) || event.OccurredAt.IsZero() {
		return ErrGatewaySecurityUnavailable
	}
	expectedOutcome, requestEvent := gatewayAuditShape(event.Action)
	if expectedOutcome == "" || event.Outcome != expectedOutcome &&
		!(event.Action == GatewayAuditRequestError && event.Outcome == "rate_limited") {
		return ErrGatewaySecurityUnavailable
	}
	if requestEvent {
		if event.ServerViolation != nil || !canonicalUUID(event.RequestID) || event.Document == nil ||
			event.Document.ValidateAgainstHead(event.Head) != nil ||
			AdmitBrowserRequestMethod(event.Method, ProductionV1MethodBaseline()) != nil ||
			event.LatencyMillis < 0 || event.LatencyMillis > int64(time.Hour/time.Millisecond) {
			return ErrGatewaySecurityUnavailable
		}
		if event.Action == GatewayAuditRequestAdmitted && event.LatencyMillis != 0 {
			return ErrGatewaySecurityUnavailable
		}
		return nil
	}
	if event.Action == GatewayAuditServerViolation {
		violation := event.ServerViolation
		if violation == nil || !auditableServerControlMethod(violation.Method) ||
			len(violation.Method) > 256 || violation.Ordinal == 0 ||
			violation.Ordinal > maximumServerControlAuditOrdinal || violation.Count == 0 ||
			violation.Count > violation.Ordinal || event.RequestID != "" || event.Method != "" ||
			event.Document != nil || event.LatencyMillis != 0 || !gatewayServerViolationCode(event.Code) {
			return ErrGatewaySecurityUnavailable
		}
		return nil
	}
	if event.RequestID != "" || event.Method != "" || event.Document != nil || event.LatencyMillis != 0 {
		return ErrGatewaySecurityUnavailable
	}
	if event.ServerViolation != nil {
		return ErrGatewaySecurityUnavailable
	}
	return nil
}

func gatewayServerViolationCode(code string) bool {
	return code == "server_request_rejected" || code == "server_request_forbidden" ||
		code == "server_notification_forbidden" || code == "server_request_repeat_limit" ||
		code == "server_initialize_rejected" || code == "server_message_malformed"
}

func gatewayAuditShape(action string) (string, bool) {
	switch action {
	case GatewayAuditBindingOpen:
		return "opened", false
	case GatewayAuditBindingRebind:
		return "rebound", false
	case GatewayAuditBindingClose:
		return "closed", false
	case GatewayAuditEditorLeaseAcquire:
		return "acquired", false
	case GatewayAuditEditorLeaseConflict:
		return "conflict", false
	case GatewayAuditEditorLeaseLost:
		return "lost", false
	case GatewayAuditEditorLeaseRelease:
		return "released", false
	case GatewayAuditRequestAdmitted:
		return "admitted", true
	case GatewayAuditRequestCompleted:
		return "completed", true
	case GatewayAuditRequestCancel:
		return "canceled", true
	case GatewayAuditRequestTimeout:
		return "timed_out", true
	case GatewayAuditRequestStale:
		return "stale", true
	case GatewayAuditRequestError:
		return "error", true
	case GatewayAuditServerViolation:
		return "rejected", false
	default:
		return "", false
	}
}

func validateGatewayAuditProfile(profile TicketAuditProfile) error {
	if !profileIDPattern.MatchString(profile.ID) || len(profile.ID) > 80 ||
		!digestPattern.MatchString(profile.ContentHash) ||
		!ticketAuditImagePattern.MatchString(profile.Image) || strings.Count(profile.Image, "@sha256:") != 1 ||
		!digestPattern.MatchString(profile.ExecutableDigest) ||
		!digestPattern.MatchString(profile.CapabilityHash) {
		return ErrGatewaySecurityUnavailable
	}
	return nil
}

var _ GatewaySecurityBoundary = (*GatewaySecurityService)(nil)
