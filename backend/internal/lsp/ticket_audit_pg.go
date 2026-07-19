package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

var (
	ticketAuditCodePattern  = regexp.MustCompile(`^(?:ok|lsp_[a-z0-9_]{1,80})$`)
	ticketAuditImagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,900}@sha256:[0-9a-f]{64}$`)
)

type PostgresTicketAuditSink struct {
	database *gorm.DB
}

func NewPostgresTicketAuditSink(database *gorm.DB) (*PostgresTicketAuditSink, error) {
	if database == nil {
		return nil, ErrAuditUnavailable
	}
	return &PostgresTicketAuditSink{database: database}, nil
}

func (sink *PostgresTicketAuditSink) Append(ctx context.Context, event TicketAuditEvent) error {
	if sink == nil || sink.database == nil || ctx == nil || validateTicketAuditEvent(event) != nil {
		return ErrAuditUnavailable
	}
	var projectID, actorID *uuid.UUID
	if parsed, err := uuid.Parse(event.ProjectID); err == nil && parsed.String() == event.ProjectID {
		projectID = &parsed
	}
	if parsed, err := uuid.Parse(event.ActorID); err == nil && parsed.String() == event.ActorID {
		actorID = &parsed
	}
	targetID := "unresolved"
	if canonicalUUID(event.TicketID) {
		targetID = event.TicketID
	} else if canonicalUUID(event.SessionID) {
		targetID = event.SessionID
	}
	metadata, err := encodeTicketAuditMetadata(event)
	if err != nil {
		return ErrAuditUnavailable
	}
	model := storage.AuditEventModel{
		ID: uuid.New(), ProjectID: projectID, ActorID: actorID,
		Action: "lsp." + event.Action, TargetType: "lsp_connection_ticket", TargetID: targetID,
		Metadata: metadata, CreatedAt: event.OccurredAt.UTC(),
	}
	if err := sink.database.WithContext(ctx).Create(&model).Error; err != nil {
		return errors.Join(ErrAuditUnavailable, err)
	}
	return nil
}

type ticketAuditMetadata struct {
	SchemaVersion   string                `json:"schemaVersion"`
	Outcome         string                `json:"outcome"`
	Code            string                `json:"code"`
	SessionID       string                `json:"sessionId,omitempty"`
	Mode            TicketMode            `json:"mode,omitempty"`
	OriginHash      string                `json:"originHash"`
	Head            *SandboxHeadFence     `json:"sandboxHeadFence,omitempty"`
	TemplateRelease *ExactTemplateRelease `json:"templateRelease,omitempty"`
	Profiles        []TicketAuditProfile  `json:"profiles"`
}

func encodeTicketAuditMetadata(event TicketAuditEvent) ([]byte, error) {
	metadata := ticketAuditMetadata{
		SchemaVersion: "sandbox-lsp-ticket-audit/v1", Outcome: event.Outcome, Code: event.Code,
		OriginHash: event.OriginHash, Profiles: append([]TicketAuditProfile(nil), event.Profiles...),
	}
	if canonicalUUID(event.SessionID) {
		metadata.SessionID = event.SessionID
	}
	if event.Mode == TicketModeSnapshot || event.Mode == TicketModeEditor {
		metadata.Mode = event.Mode
	}
	if event.Head.Validate() == nil {
		head := event.Head
		metadata.Head = &head
	}
	if event.TemplateRelease.Validate() == nil {
		release := event.TemplateRelease
		metadata.TemplateRelease = &release
	}
	return json.Marshal(metadata)
}

func validateTicketAuditEvent(event TicketAuditEvent) error {
	if (event.Action != TicketAuditIssue && event.Action != TicketAuditConsume) ||
		(event.Outcome != "issued" && event.Outcome != "consumed" &&
			event.Outcome != "rejected" && event.Outcome != "rate_limited") ||
		!ticketAuditCodePattern.MatchString(event.Code) || event.OccurredAt.IsZero() ||
		!digestPattern.MatchString(event.OriginHash) || len(event.Profiles) > 1 {
		return ErrAuditUnavailable
	}
	profiles := append([]TicketAuditProfile(nil), event.Profiles...)
	sort.Slice(profiles, func(left, right int) bool { return profiles[left].ID < profiles[right].ID })
	for index, profile := range profiles {
		if !profileIDPattern.MatchString(profile.ID) || len(profile.ID) > 80 ||
			(index > 0 && profiles[index-1].ID == profile.ID) ||
			!digestPattern.MatchString(profile.ContentHash) ||
			!ticketAuditImagePattern.MatchString(profile.Image) || strings.Count(profile.Image, "@sha256:") != 1 ||
			!digestPattern.MatchString(profile.ExecutableDigest) ||
			!digestPattern.MatchString(profile.CapabilityHash) {
			return ErrAuditUnavailable
		}
	}
	return nil
}

var _ TicketAuditSink = (*PostgresTicketAuditSink)(nil)
