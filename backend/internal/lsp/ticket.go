package lsp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/templates"
)

const (
	TicketSchemaVersion = "sandbox-lsp-ticket/v1"
	TicketWebSocketPath = "/v1/sandbox-lsp"
	TicketSubprotocol   = "worksflow.sandbox-lsp.v1"
)

var (
	ErrTicketInvalid      = errors.New("invalid LSP connection ticket")
	ErrTicketUnavailable  = errors.New("LSP connection ticket store is unavailable")
	ErrTicketConsumed     = errors.New("LSP connection ticket is expired, consumed, or unknown")
	ErrForbidden          = errors.New("LSP access is forbidden")
	ErrOriginForbidden    = errors.New("LSP Origin is forbidden")
	ErrSessionNotReady    = errors.New("LSP sandbox session is not ready")
	ErrHeadStale          = errors.New("LSP SandboxHeadFence is stale")
	ErrProfileNotDeclared = errors.New("LSP language-server profile is not declared")
	profileIDPattern      = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
)

type TicketMode string

const (
	TicketModeSnapshot TicketMode = "snapshot"
	TicketModeEditor   TicketMode = "editor"
)

type ExactTemplateRelease struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

func (release ExactTemplateRelease) Validate() error {
	if !canonicalUUID(release.ID) || !digestPattern.MatchString(release.ContentHash) {
		return ErrTicketInvalid
	}
	return nil
}

type EffectiveLimits = templates.LanguageServerLimits

// ProfileIdentity contains only immutable, non-secret facts required to prove
// which admitted language server a ticket may start. Source text and runtime
// tokens never belong in this object.
type ProfileIdentity struct {
	templates.LanguageServerProfile
	TemplateRelease ExactTemplateRelease `json:"templateRelease"`
	EffectiveLimits EffectiveLimits      `json:"effectiveLimits"`
}

func (identity ProfileIdentity) Validate() error {
	if identity.TemplateRelease.Validate() != nil ||
		templates.ValidateLanguageServerProfile(identity.LanguageServerProfile) != nil ||
		!reflect.DeepEqual(identity.EffectiveLimits, identity.Limits) {
		return ErrTicketInvalid
	}
	return nil
}

type AuthorityView struct {
	Head               SandboxHeadFence
	TemplateRelease    ExactTemplateRelease
	State              string
	WriterLeaseOwnerID string
	SessionExpiresAt   time.Time
	Profiles           []ProfileIdentity
}

type TicketAuthority interface {
	GetLSPAuthority(context.Context, string, string, ExactTemplateRelease) (AuthorityView, error)
}

type TicketAuthorizer interface {
	RequireProjectView(context.Context, string, string) error
	RequireProjectEdit(context.Context, string, string) error
}

type TicketGrantStore interface {
	Put(context.Context, string, TicketGrant, time.Duration) error
	Consume(context.Context, string) (TicketGrant, error)
}

type IssueTicketInput struct {
	ProjectID       string
	SessionID       string
	ActorID         string
	Origin          string
	Mode            TicketMode
	Head            SandboxHeadFence
	TemplateRelease ExactTemplateRelease
	ProfileIDs      []string
}

type TicketGrant struct {
	SchemaVersion   string               `json:"schemaVersion"`
	ID              string               `json:"id"`
	ProjectID       string               `json:"projectId"`
	SessionID       string               `json:"sessionId"`
	ActorID         string               `json:"actorId"`
	Origin          string               `json:"origin"`
	Mode            TicketMode           `json:"mode"`
	Head            SandboxHeadFence     `json:"sandboxHeadFence"`
	TemplateRelease ExactTemplateRelease `json:"templateRelease"`
	Profiles        []ProfileIdentity    `json:"profiles"`
	IssuedAt        time.Time            `json:"issuedAt"`
	ExpiresAt       time.Time            `json:"expiresAt"`
}

type TicketView struct {
	SchemaVersion   string               `json:"schemaVersion"`
	ID              string               `json:"id"`
	Ticket          string               `json:"ticket"`
	WebSocketPath   string               `json:"webSocketPath"`
	Subprotocol     string               `json:"subprotocol"`
	Mode            TicketMode           `json:"mode"`
	Head            SandboxHeadFence     `json:"sandboxHeadFence"`
	TemplateRelease ExactTemplateRelease `json:"templateRelease"`
	Profiles        []ProfileIdentity    `json:"profiles"`
	ExpiresAt       time.Time            `json:"expiresAt"`
}

type TicketService struct {
	store       TicketGrantStore
	authority   TicketAuthority
	access      TicketAuthorizer
	ttl         time.Duration
	now         func() time.Time
	tokenSource func() (string, string, error)
}

func NewTicketService(
	store TicketGrantStore,
	authority TicketAuthority,
	access TicketAuthorizer,
	ttl time.Duration,
	now func() time.Time,
) (*TicketService, error) {
	return newTicketService(store, authority, access, ttl, now, newTicketSecret)
}

func newTicketService(
	store TicketGrantStore,
	authority TicketAuthority,
	access TicketAuthorizer,
	ttl time.Duration,
	now func() time.Time,
	tokenSource func() (string, string, error),
) (*TicketService, error) {
	if store == nil || authority == nil || access == nil || now == nil || tokenSource == nil {
		return nil, ErrTicketUnavailable
	}
	if ttl < 5*time.Second || ttl > 30*time.Second {
		return nil, ErrTicketInvalid
	}
	return &TicketService{
		store: store, authority: authority, access: access, ttl: ttl, now: now, tokenSource: tokenSource,
	}, nil
}

func (service *TicketService) Issue(ctx context.Context, input IssueTicketInput) (TicketView, error) {
	if service == nil || ctx == nil || !canonicalUUID(input.ProjectID) ||
		!canonicalUUID(input.SessionID) || !canonicalUUID(input.ActorID) ||
		input.Head.Validate() != nil || input.Head.ProjectID != input.ProjectID ||
		input.Head.SessionID != input.SessionID ||
		input.TemplateRelease.Validate() != nil ||
		(input.Mode != TicketModeSnapshot && input.Mode != TicketModeEditor) {
		return TicketView{}, ErrTicketInvalid
	}
	origin, err := normalizeTicketOrigin(input.Origin)
	if err != nil {
		return TicketView{}, err
	}
	profileIDs, err := validateRequestedProfiles(input.ProfileIDs)
	if err != nil {
		return TicketView{}, err
	}
	if err := service.authorize(ctx, input.ProjectID, input.ActorID, input.Mode); err != nil {
		return TicketView{}, err
	}
	authority, err := service.authority.GetLSPAuthority(
		ctx, input.ProjectID, input.SessionID, input.TemplateRelease,
	)
	if err != nil {
		return TicketView{}, err
	}
	profiles, err := validateAuthority(
		authority, input.Head, input.TemplateRelease, input.ActorID, input.Mode, profileIDs,
	)
	if err != nil {
		return TicketView{}, err
	}
	now := service.now().UTC()
	if now.IsZero() || !authority.SessionExpiresAt.After(now) {
		return TicketView{}, ErrSessionNotReady
	}
	ttl := service.ttl
	if remaining := authority.SessionExpiresAt.Sub(now); remaining < ttl {
		ttl = remaining
	}
	if ttl <= 0 {
		return TicketView{}, ErrSessionNotReady
	}
	id, secret, err := service.tokenSource()
	if err != nil || !canonicalUUID(id) || !validTicketSecret(secret) {
		return TicketView{}, fmt.Errorf("%w: generate ticket", ErrTicketUnavailable)
	}
	grant := TicketGrant{
		SchemaVersion: TicketSchemaVersion, ID: id, ProjectID: input.ProjectID,
		SessionID: input.SessionID, ActorID: input.ActorID, Origin: origin, Mode: input.Mode,
		Head: input.Head, TemplateRelease: input.TemplateRelease,
		Profiles: cloneProfiles(profiles), IssuedAt: now, ExpiresAt: now.Add(ttl),
	}
	if err := validateTicketGrant(grant, now); err != nil {
		return TicketView{}, err
	}
	if err := service.store.Put(ctx, secret, grant, ttl); err != nil {
		return TicketView{}, err
	}
	return TicketView{
		SchemaVersion: TicketSchemaVersion, ID: id, Ticket: secret,
		WebSocketPath: TicketWebSocketPath, Subprotocol: TicketSubprotocol,
		Mode: input.Mode, Head: input.Head, TemplateRelease: input.TemplateRelease,
		Profiles: cloneProfiles(profiles), ExpiresAt: grant.ExpiresAt,
	}, nil
}

// Consume burns the bearer secret before any reauthorization or authority
// lookup. A failed Upgrade can never leave a replayable LSP capability.
func (service *TicketService) Consume(ctx context.Context, secret, origin string) (TicketGrant, error) {
	if service == nil || ctx == nil || !validTicketSecret(secret) {
		return TicketGrant{}, ErrTicketInvalid
	}
	normalizedOrigin, err := normalizeTicketOrigin(origin)
	if err != nil {
		return TicketGrant{}, err
	}
	grant, err := service.store.Consume(ctx, secret)
	if err != nil {
		return TicketGrant{}, err
	}
	now := service.now().UTC()
	if validateTicketGrant(grant, now) != nil || grant.Origin != normalizedOrigin {
		return TicketGrant{}, ErrTicketInvalid
	}
	if err := service.authorize(ctx, grant.ProjectID, grant.ActorID, grant.Mode); err != nil {
		return TicketGrant{}, err
	}
	authority, err := service.authority.GetLSPAuthority(
		ctx, grant.ProjectID, grant.SessionID, grant.TemplateRelease,
	)
	if err != nil {
		return TicketGrant{}, err
	}
	requested := make([]string, len(grant.Profiles))
	for index, profile := range grant.Profiles {
		requested[index] = profile.ID
	}
	profiles, err := validateAuthority(
		authority, grant.Head, grant.TemplateRelease, grant.ActorID, grant.Mode, requested,
	)
	if err != nil {
		return TicketGrant{}, err
	}
	if !equalProfiles(profiles, grant.Profiles) {
		return TicketGrant{}, ErrProfileNotDeclared
	}
	result := grant
	result.Profiles = cloneProfiles(grant.Profiles)
	return result, nil
}

func (service *TicketService) authorize(
	ctx context.Context,
	projectID, actorID string,
	mode TicketMode,
) error {
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	if mode == TicketModeEditor {
		if err := service.access.RequireProjectEdit(ctx, projectID, actorID); err != nil {
			return fmt.Errorf("%w: %v", ErrForbidden, err)
		}
	}
	return nil
}

func validateAuthority(
	authority AuthorityView,
	expected SandboxHeadFence,
	expectedRelease ExactTemplateRelease,
	actorID string,
	mode TicketMode,
	profileIDs []string,
) ([]ProfileIdentity, error) {
	if authority.State != "ready" {
		return nil, ErrSessionNotReady
	}
	if authority.Head.Validate() != nil || !authority.Head.Equal(expected) {
		return nil, ErrHeadStale
	}
	if expectedRelease.Validate() != nil || authority.TemplateRelease != expectedRelease {
		return nil, ErrProfileNotDeclared
	}
	if mode == TicketModeEditor && (expected.WriterLeaseEpoch == 0 || authority.WriterLeaseOwnerID != actorID) {
		return nil, ErrForbidden
	}
	byID := make(map[string]ProfileIdentity, len(authority.Profiles))
	for _, profile := range authority.Profiles {
		if profile.Validate() != nil || profile.TemplateRelease != expectedRelease || byID[profile.ID].ID != "" {
			return nil, ErrProfileNotDeclared
		}
		byID[profile.ID] = profile
	}
	result := make([]ProfileIdentity, len(profileIDs))
	for index, id := range profileIDs {
		profile := byID[id]
		if profile.ID == "" {
			return nil, ErrProfileNotDeclared
		}
		result[index] = profile
	}
	return result, nil
}

func validateRequestedProfiles(values []string) ([]string, error) {
	// Production v1 binds exactly one language-server process per one-time
	// ticket/WebSocket. Multi-language projects issue independent tickets so a
	// single burned capability can never ambiguously authorize multiplexing.
	if len(values) != 1 {
		return nil, ErrProfileNotDeclared
	}
	result := append([]string(nil), values...)
	for index, value := range result {
		if !profileIDPattern.MatchString(value) || len(value) > 80 ||
			(index > 0 && result[index-1] >= value) {
			return nil, ErrProfileNotDeclared
		}
	}
	return result, nil
}

func validateTicketGrant(grant TicketGrant, observedAt time.Time) error {
	if grant.SchemaVersion != TicketSchemaVersion || !canonicalUUID(grant.ID) ||
		!canonicalUUID(grant.ProjectID) || !canonicalUUID(grant.SessionID) ||
		!canonicalUUID(grant.ActorID) || grant.Head.Validate() != nil ||
		grant.Head.ProjectID != grant.ProjectID || grant.Head.SessionID != grant.SessionID ||
		grant.TemplateRelease.Validate() != nil ||
		(grant.Mode != TicketModeSnapshot && grant.Mode != TicketModeEditor) ||
		grant.IssuedAt.IsZero() || !grant.ExpiresAt.After(grant.IssuedAt) ||
		grant.ExpiresAt.Sub(grant.IssuedAt) > 30*time.Second ||
		(!observedAt.IsZero() && !grant.ExpiresAt.After(observedAt)) {
		return ErrTicketInvalid
	}
	normalizedOrigin, err := normalizeTicketOrigin(grant.Origin)
	if err != nil || normalizedOrigin != grant.Origin {
		return ErrTicketInvalid
	}
	ids := make([]string, len(grant.Profiles))
	for index, profile := range grant.Profiles {
		if profile.Validate() != nil || profile.TemplateRelease != grant.TemplateRelease {
			return ErrTicketInvalid
		}
		ids[index] = profile.ID
	}
	if _, err := validateRequestedProfiles(ids); err != nil {
		return ErrTicketInvalid
	}
	return nil
}

func normalizeTicketOrigin(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Opaque != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrOriginForbidden
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "" || strings.ContainsAny(parsed.Host, "\r\n\x00") {
		return "", ErrOriginForbidden
	}
	if parsed.Scheme == "http" && !localDevelopmentHost(hostname) {
		return "", ErrOriginForbidden
	}
	host := hostname
	if parsed.Port() != "" {
		host = net.JoinHostPort(hostname, parsed.Port())
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	return parsed.Scheme + "://" + host, nil
}

func localDevelopmentHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func newTicketSecret() (string, string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", "", err
	}
	return uuid.NewString(), base64.RawURLEncoding.EncodeToString(value), nil
}

func validTicketSecret(value string) bool {
	if len(value) != 43 || value != strings.TrimSpace(value) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func cloneProfiles(values []ProfileIdentity) []ProfileIdentity {
	result := append([]ProfileIdentity(nil), values...)
	for index := range result {
		result[index].LanguageIDs = append([]string(nil), values[index].LanguageIDs...)
		result[index].FileGlobs = append([]string(nil), values[index].FileGlobs...)
		result[index].Methods = append([]string(nil), values[index].Methods...)
		result[index].Runtime.Argv = append([]string(nil), values[index].Runtime.Argv...)
	}
	return result
}

func equalProfiles(left, right []ProfileIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !reflect.DeepEqual(left[index], right[index]) {
			return false
		}
	}
	return true
}

func sortProfileIdentities(values []ProfileIdentity) {
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
}
