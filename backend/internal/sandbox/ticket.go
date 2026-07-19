package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const ConnectionTicketSchemaVersion = "sandbox-connection-ticket/v1"

var (
	ErrConnectionTicketInvalid     = errors.New("invalid sandbox connection ticket")
	ErrConnectionTicketUnavailable = errors.New("sandbox connection ticket store is unavailable")
	ErrConnectionTicketConsumed    = errors.New("sandbox connection ticket is expired, consumed, or unknown")
)

type StreamChannel string

const (
	ChannelControl    StreamChannel = "control"
	ChannelFilesystem StreamChannel = "fs"
	ChannelPTY        StreamChannel = "pty"
	ChannelProcess    StreamChannel = "process"
	ChannelPort       StreamChannel = "port"
	ChannelPreviewLog StreamChannel = "preview-log"
	ChannelAgent      StreamChannel = "agent"
	ChannelResource   StreamChannel = "resource"
)

var streamChannelOrder = []StreamChannel{
	ChannelControl, ChannelFilesystem, ChannelPTY, ChannelProcess,
	ChannelPort, ChannelPreviewLog, ChannelAgent, ChannelResource,
}

type ConnectionCursor struct {
	Channel      StreamChannel `json:"channel"`
	LastAckedSeq uint64        `json:"lastAckedSeq"`
}

type ConnectionTicketGrant struct {
	SchemaVersion string             `json:"schemaVersion"`
	ID            string             `json:"id"`
	ProjectID     string             `json:"projectId"`
	SessionID     string             `json:"sessionId"`
	ActorID       string             `json:"actorId"`
	SessionEpoch  uint64             `json:"sessionEpoch"`
	Origin        string             `json:"origin"`
	Channels      []StreamChannel    `json:"channels"`
	Cursors       []ConnectionCursor `json:"cursors"`
	IssuedAt      time.Time          `json:"issuedAt"`
	ExpiresAt     time.Time          `json:"expiresAt"`
}

type ConnectionTicketView struct {
	SchemaVersion string             `json:"schemaVersion"`
	ID            string             `json:"id"`
	Ticket        string             `json:"ticket"`
	SessionID     string             `json:"sessionId"`
	SessionEpoch  uint64             `json:"sessionEpoch"`
	Channels      []StreamChannel    `json:"channels"`
	Cursors       []ConnectionCursor `json:"cursors"`
	WebSocketPath string             `json:"webSocketPath"`
	ExpiresAt     time.Time          `json:"expiresAt"`
}

type IssueConnectionTicketInput struct {
	ProjectID string
	SessionID string
	ActorID   string
	Origin    string
	Channels  []StreamChannel
	Cursors   []ConnectionCursor
}

type ConnectionTicketStore interface {
	Put(context.Context, string, ConnectionTicketGrant, time.Duration) error
	Consume(context.Context, string) (ConnectionTicketGrant, error)
}

type ConnectionTicketSessions interface {
	Get(context.Context, string, string) (SandboxSession, error)
}

type ConnectionTicketService struct {
	store       ConnectionTicketStore
	sessions    ConnectionTicketSessions
	access      ProjectAuthorizer
	ttl         time.Duration
	now         func() time.Time
	tokenSource func() (string, string, error)
}

func NewConnectionTicketService(
	store ConnectionTicketStore,
	sessions ConnectionTicketSessions,
	access ProjectAuthorizer,
	ttl time.Duration,
	now func() time.Time,
) (*ConnectionTicketService, error) {
	return newConnectionTicketService(store, sessions, access, ttl, now, newConnectionTicketSecret)
}

func newConnectionTicketService(
	store ConnectionTicketStore,
	sessions ConnectionTicketSessions,
	access ProjectAuthorizer,
	ttl time.Duration,
	now func() time.Time,
	tokenSource func() (string, string, error),
) (*ConnectionTicketService, error) {
	if store == nil || sessions == nil || access == nil || now == nil || tokenSource == nil {
		return nil, fmt.Errorf("%w: store, session source, access, clock, and token source are required", ErrConnectionTicketUnavailable)
	}
	if ttl < 5*time.Second || ttl > 2*time.Minute {
		return nil, fmt.Errorf("%w: TTL must be between 5 seconds and 2 minutes", ErrConnectionTicketInvalid)
	}
	return &ConnectionTicketService{
		store: store, sessions: sessions, access: access, ttl: ttl, now: now, tokenSource: tokenSource,
	}, nil
}

func (service *ConnectionTicketService) Issue(
	ctx context.Context,
	input IssueConnectionTicketInput,
) (ConnectionTicketView, error) {
	if service == nil || ctx == nil || !validUUID(input.ProjectID) || !validUUID(input.SessionID) || !validUUID(input.ActorID) {
		return ConnectionTicketView{}, ErrConnectionTicketInvalid
	}
	origin, err := normalizeConnectionOrigin(input.Origin)
	if err != nil {
		return ConnectionTicketView{}, err
	}
	channels, cursors, controlRequired, err := normalizeConnectionScope(input.Channels, input.Cursors)
	if err != nil {
		return ConnectionTicketView{}, err
	}
	if err := service.access.RequireProjectView(ctx, input.ProjectID, input.ActorID); err != nil {
		return ConnectionTicketView{}, fmt.Errorf("authorize sandbox stream view: %w", err)
	}
	if controlRequired {
		if err := service.access.RequireSandboxControl(ctx, input.ProjectID, input.ActorID); err != nil {
			return ConnectionTicketView{}, fmt.Errorf("authorize sandbox stream control: %w", err)
		}
	}
	session, err := service.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return ConnectionTicketView{}, err
	}
	view := session.Snapshot()
	for _, channel := range channels {
		if action, required := streamChannelAction(channel); required {
			if err := session.Authorize(action, view.Version, view.SessionEpoch); err != nil {
				return ConnectionTicketView{}, err
			}
		}
	}
	now := service.now().UTC()
	if now.IsZero() || now.Before(view.UpdatedAt) || !now.Before(view.TTL.ExpiresAt) {
		return ConnectionTicketView{}, ErrConnectionTicketInvalid
	}
	ttl := service.ttl
	if remaining := view.TTL.ExpiresAt.Sub(now); remaining < ttl {
		ttl = remaining
	}
	if ttl <= 0 {
		return ConnectionTicketView{}, ErrConnectionTicketInvalid
	}
	id, secret, err := service.tokenSource()
	if err != nil || !validUUID(id) || !validConnectionTicketSecret(secret) {
		return ConnectionTicketView{}, fmt.Errorf("%w: generate secret", ErrConnectionTicketUnavailable)
	}
	grant := ConnectionTicketGrant{
		SchemaVersion: ConnectionTicketSchemaVersion,
		ID:            id, ProjectID: view.ProjectID, SessionID: view.ID, ActorID: input.ActorID,
		SessionEpoch: view.SessionEpoch, Origin: origin, Channels: channels, Cursors: cursors,
		IssuedAt: now, ExpiresAt: now.Add(ttl),
	}
	if err := validateConnectionTicketGrant(grant, now); err != nil {
		return ConnectionTicketView{}, err
	}
	if err := service.store.Put(ctx, secret, grant, ttl); err != nil {
		return ConnectionTicketView{}, err
	}
	return ConnectionTicketView{
		SchemaVersion: ConnectionTicketSchemaVersion,
		ID:            id, Ticket: secret, SessionID: view.ID, SessionEpoch: view.SessionEpoch,
		Channels:      append([]StreamChannel(nil), channels...),
		Cursors:       append([]ConnectionCursor(nil), cursors...),
		WebSocketPath: "/v1/sandbox-stream", ExpiresAt: grant.ExpiresAt,
	}, nil
}

// Consume burns the secret before repeating authorization and epoch checks.
// A failed upgrade/revalidation therefore never leaves a replayable bearer
// capability behind; the client must request a fresh short-lived ticket.
func (service *ConnectionTicketService) Consume(
	ctx context.Context,
	secret, origin string,
) (ConnectionTicketGrant, error) {
	if service == nil || ctx == nil {
		return ConnectionTicketGrant{}, ErrConnectionTicketInvalid
	}
	normalizedOrigin, err := normalizeConnectionOrigin(origin)
	if err != nil {
		return ConnectionTicketGrant{}, err
	}
	grant, err := service.store.Consume(ctx, secret)
	if err != nil {
		return ConnectionTicketGrant{}, err
	}
	now := service.now().UTC()
	if err := validateConnectionTicketGrant(grant, now); err != nil || grant.Origin != normalizedOrigin {
		return ConnectionTicketGrant{}, ErrConnectionTicketInvalid
	}
	channels, _, controlRequired, err := normalizeConnectionScope(grant.Channels, grant.Cursors)
	if err != nil {
		return ConnectionTicketGrant{}, err
	}
	if err := service.access.RequireProjectView(ctx, grant.ProjectID, grant.ActorID); err != nil {
		return ConnectionTicketGrant{}, fmt.Errorf("re-authorize sandbox stream view: %w", err)
	}
	if controlRequired {
		if err := service.access.RequireSandboxControl(ctx, grant.ProjectID, grant.ActorID); err != nil {
			return ConnectionTicketGrant{}, fmt.Errorf("re-authorize sandbox stream control: %w", err)
		}
	}
	session, err := service.sessions.Get(ctx, grant.ProjectID, grant.SessionID)
	if err != nil {
		return ConnectionTicketGrant{}, err
	}
	view := session.Snapshot()
	if view.ProjectID != grant.ProjectID || view.ID != grant.SessionID || view.SessionEpoch != grant.SessionEpoch {
		return ConnectionTicketGrant{}, ErrEpochFenced
	}
	for _, channel := range channels {
		if action, required := streamChannelAction(channel); required {
			if err := session.Authorize(action, view.Version, view.SessionEpoch); err != nil {
				return ConnectionTicketGrant{}, err
			}
		}
	}
	result := grant
	result.Channels = append([]StreamChannel(nil), grant.Channels...)
	result.Cursors = append([]ConnectionCursor(nil), grant.Cursors...)
	return result, nil
}

func normalizeConnectionScope(
	channels []StreamChannel,
	cursors []ConnectionCursor,
) ([]StreamChannel, []ConnectionCursor, bool, error) {
	requested := make(map[StreamChannel]bool, len(channels))
	for _, channel := range channels {
		if !knownStreamChannel(channel) || requested[channel] {
			return nil, nil, false, ErrConnectionTicketInvalid
		}
		requested[channel] = true
	}
	if len(requested) == 0 || len(requested) > len(streamChannelOrder) {
		return nil, nil, false, ErrConnectionTicketInvalid
	}
	normalizedChannels := make([]StreamChannel, 0, len(requested))
	controlRequired := false
	for _, channel := range streamChannelOrder {
		if requested[channel] {
			normalizedChannels = append(normalizedChannels, channel)
			_, requiresAction := streamChannelAction(channel)
			controlRequired = controlRequired || requiresAction
		}
	}
	cursorByChannel := make(map[StreamChannel]uint64, len(cursors))
	for _, cursor := range cursors {
		if !requested[cursor.Channel] {
			return nil, nil, false, ErrConnectionTicketInvalid
		}
		if _, duplicate := cursorByChannel[cursor.Channel]; duplicate {
			return nil, nil, false, ErrConnectionTicketInvalid
		}
		cursorByChannel[cursor.Channel] = cursor.LastAckedSeq
	}
	normalizedCursors := make([]ConnectionCursor, 0, len(requested))
	for _, channel := range normalizedChannels {
		normalizedCursors = append(normalizedCursors, ConnectionCursor{
			Channel: channel, LastAckedSeq: cursorByChannel[channel],
		})
	}
	return normalizedChannels, normalizedCursors, controlRequired, nil
}

func streamChannelAction(channel StreamChannel) (Action, bool) {
	switch channel {
	case ChannelFilesystem:
		return ActionEdit, true
	case ChannelPTY:
		return ActionPTY, true
	case ChannelProcess:
		return ActionProcess, true
	case ChannelAgent:
		return ActionAgent, true
	case ChannelControl, ChannelPort, ChannelPreviewLog, ChannelResource:
		return ActionView, false
	default:
		return "", false
	}
}

func knownStreamChannel(channel StreamChannel) bool {
	_, known := streamChannelAction(channel)
	if known {
		return true
	}
	return channel == ChannelControl || channel == ChannelPort || channel == ChannelPreviewLog || channel == ChannelResource
}

func validateConnectionTicketGrant(grant ConnectionTicketGrant, observedAt time.Time) error {
	if grant.SchemaVersion != ConnectionTicketSchemaVersion || !validUUID(grant.ID) ||
		!validUUID(grant.ProjectID) || !validUUID(grant.SessionID) || !validUUID(grant.ActorID) ||
		grant.SessionEpoch == 0 || grant.IssuedAt.IsZero() || !grant.ExpiresAt.After(grant.IssuedAt) ||
		grant.ExpiresAt.Sub(grant.IssuedAt) > 2*time.Minute || (!observedAt.IsZero() && !grant.ExpiresAt.After(observedAt)) {
		return ErrConnectionTicketInvalid
	}
	if _, err := normalizeConnectionOrigin(grant.Origin); err != nil {
		return err
	}
	channels, cursors, _, err := normalizeConnectionScope(grant.Channels, grant.Cursors)
	if err != nil || !equalStreamChannels(channels, grant.Channels) || !equalConnectionCursors(cursors, grant.Cursors) {
		return ErrConnectionTicketInvalid
	}
	return nil
}

func normalizeConnectionOrigin(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrConnectionTicketInvalid
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), nil
}

func newConnectionTicketSecret() (string, string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", "", err
	}
	return uuid.NewString(), base64.RawURLEncoding.EncodeToString(value), nil
}

func validConnectionTicketSecret(value string) bool {
	if len(value) != 43 || value != strings.TrimSpace(value) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func equalStreamChannels(left, right []StreamChannel) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalConnectionCursors(left, right []ConnectionCursor) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortConnectionCursors(values []ConnectionCursor) {
	sort.Slice(values, func(left, right int) bool { return values[left].Channel < values[right].Channel })
}
