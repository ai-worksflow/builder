package lsp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/sandbox"
	"github.com/worksflow/builder/backend/internal/templates"
)

var ErrAuthorityUnavailable = errors.New("LSP source authority is unavailable")

type AuthoritySessions interface {
	Get(context.Context, string, string) (sandbox.SandboxSession, error)
}

type AuthorityCandidates interface {
	Get(context.Context, string, string) (repository.CandidateMutationRecord, error)
}

// AuthorityProfileSource resolves profiles only after proving that release is
// an approved exact component of the session's exact FullStackTemplate.
type AuthorityProfileSource interface {
	ResolveApprovedProfiles(
		context.Context,
		repository.ExactReference,
		ExactTemplateRelease,
	) ([]templates.LanguageServerProfile, error)
}

type AuthoritySource struct {
	sessions   AuthoritySessions
	candidates AuthorityCandidates
	profiles   AuthorityProfileSource
	now        func() time.Time
}

func NewAuthoritySource(
	sessions AuthoritySessions,
	candidates AuthorityCandidates,
	profiles AuthorityProfileSource,
	now func() time.Time,
) (*AuthoritySource, error) {
	if sessions == nil || candidates == nil || profiles == nil || now == nil {
		return nil, ErrAuthorityUnavailable
	}
	return &AuthoritySource{sessions: sessions, candidates: candidates, profiles: profiles, now: now}, nil
}

func (source *AuthoritySource) GetLSPAuthority(
	ctx context.Context,
	projectID, sessionID string,
	templateRelease ExactTemplateRelease,
) (AuthorityView, error) {
	if source == nil || ctx == nil || !canonicalUUID(projectID) || !canonicalUUID(sessionID) ||
		templateRelease.Validate() != nil {
		return AuthorityView{}, ErrTicketInvalid
	}
	session, err := source.sessions.Get(ctx, projectID, sessionID)
	if err != nil {
		return AuthorityView{}, fmt.Errorf("%w: read SandboxSession: %v", ErrAuthorityUnavailable, err)
	}
	if err := session.Validate(); err != nil {
		return AuthorityView{}, fmt.Errorf("%w: invalid SandboxSession projection", ErrAuthorityUnavailable)
	}
	view := session.Snapshot()
	if view.ProjectID != projectID || view.ID != sessionID {
		return AuthorityView{}, ErrHeadStale
	}
	now := source.now().UTC()
	if now.IsZero() || !view.TTL.ExpiresAt.After(now) {
		return AuthorityView{State: view.State.String(), SessionExpiresAt: view.TTL.ExpiresAt}, ErrSessionNotReady
	}
	if view.State != sandbox.StateReady {
		return AuthorityView{State: view.State.String(), SessionExpiresAt: view.TTL.ExpiresAt}, nil
	}

	record, err := source.candidates.Get(ctx, projectID, view.Candidate.ID)
	if err != nil {
		return AuthorityView{}, fmt.Errorf("%w: read Candidate: %v", ErrAuthorityUnavailable, err)
	}
	candidate := record.Candidate
	if err := candidate.Validate(); err != nil {
		return AuthorityView{}, fmt.Errorf("%w: invalid Candidate projection", ErrAuthorityUnavailable)
	}
	head, leaseOwner, err := exactAuthorityHead(view, record, now)
	if err != nil {
		return AuthorityView{}, err
	}
	if !containsExactRelease(view.TemplateReleases, templateRelease) {
		return AuthorityView{}, ErrProfileNotDeclared
	}
	profiles, err := source.profiles.ResolveApprovedProfiles(
		ctx, view.FullStackTemplate, templateRelease,
	)
	if err != nil {
		return AuthorityView{}, fmt.Errorf("%w: %v", ErrProfileNotDeclared, err)
	}
	allowedServices := make(map[string]bool, len(view.AllowedServices))
	for _, service := range view.AllowedServices {
		if service.TemplateRelease.ID == templateRelease.ID &&
			service.TemplateRelease.ContentHash == templateRelease.ContentHash {
			allowedServices[service.ID] = true
		}
	}
	identities := make([]ProfileIdentity, 0, len(profiles))
	seen := make(map[string]bool, len(profiles))
	for _, profile := range profiles {
		if templates.ValidateLanguageServerProfile(profile) != nil ||
			!allowedServices[profile.ServiceID] || seen[profile.ID] {
			return AuthorityView{}, ErrProfileNotDeclared
		}
		seen[profile.ID] = true
		identities = append(identities, ProfileIdentity{
			LanguageServerProfile: cloneTemplateProfile(profile),
			TemplateRelease:       templateRelease,
			EffectiveLimits:       profile.Limits,
		})
	}
	sortProfileIdentities(identities)
	return AuthorityView{
		Head: head, TemplateRelease: templateRelease, State: sandbox.StateReady.String(),
		WriterLeaseOwnerID: leaseOwner, SessionExpiresAt: view.TTL.ExpiresAt,
		Profiles: cloneProfiles(identities),
	}, nil
}

func exactAuthorityHead(
	view sandbox.SessionView,
	record repository.CandidateMutationRecord,
	now time.Time,
) (SandboxHeadFence, string, error) {
	candidate := record.Candidate
	projectMatches := candidate.ProjectID == view.ProjectID && candidate.ID == view.Candidate.ID
	lineageMatches := candidate.BuildManifest == view.BuildManifest &&
		candidate.BuildContract == view.BuildContract && candidate.FullStackTemplate == view.FullStackTemplate
	headMatches := candidate.SessionEpoch == view.SessionEpoch &&
		candidate.SessionEpoch == view.Candidate.SessionEpoch && candidate.Version == view.Candidate.Version &&
		candidate.JournalSequence == view.Candidate.JournalSequence &&
		candidate.WriterLeaseEpoch == view.Candidate.WriterLeaseEpoch &&
		candidate.CurrentTree.TreeHash == view.Candidate.TreeHash &&
		record.CurrentTreePointer.OwnerID == candidate.ID &&
		record.CurrentTreePointer.TreeHash == candidate.CurrentTree.TreeHash
	if !projectMatches || !lineageMatches || !headMatches || candidate.Status != repository.CandidateActive {
		return SandboxHeadFence{}, "", ErrHeadStale
	}
	head := SandboxHeadFence{
		ProjectID: candidate.ProjectID, SessionID: view.ID, SessionEpoch: candidate.SessionEpoch,
		CandidateID: candidate.ID, Version: candidate.Version,
		JournalSequence: candidate.JournalSequence, WriterLeaseEpoch: candidate.WriterLeaseEpoch,
		TreeHash: candidate.CurrentTree.TreeHash,
	}
	if head.Validate() != nil {
		return SandboxHeadFence{}, "", ErrHeadStale
	}
	leaseOwner := ""
	if candidate.Lease != nil && candidate.Lease.Epoch == candidate.WriterLeaseEpoch &&
		candidate.Lease.ExpiresAt.After(now) {
		leaseOwner = candidate.Lease.OwnerID
	}
	return head, leaseOwner, nil
}

func containsExactRelease(values []repository.ExactReference, expected ExactTemplateRelease) bool {
	for _, value := range values {
		if value.ID == expected.ID && value.ContentHash == expected.ContentHash {
			return true
		}
	}
	return false
}

func cloneTemplateProfile(profile templates.LanguageServerProfile) templates.LanguageServerProfile {
	profile.LanguageIDs = append([]string(nil), profile.LanguageIDs...)
	profile.FileGlobs = append([]string(nil), profile.FileGlobs...)
	profile.Methods = append([]string(nil), profile.Methods...)
	profile.Runtime.Argv = append([]string(nil), profile.Runtime.Argv...)
	return profile
}

// RegistryProfileSource resolves through the FullStackTemplate selector, so a
// release which is deprecated, revoked, hash-drifted, or not an exact
// component never reaches the LSP authority layer.
type RegistryProfileReader interface {
	ResolveForNewBuild(
		context.Context,
		templates.ExactFullStackTemplateRef,
	) (templates.ResolvedFullStackTemplate, error)
}

type RegistryProfileSource struct {
	registry RegistryProfileReader
}

func NewRegistryProfileSource(registry RegistryProfileReader) (*RegistryProfileSource, error) {
	if registry == nil {
		return nil, ErrAuthorityUnavailable
	}
	return &RegistryProfileSource{registry: registry}, nil
}

func (source *RegistryProfileSource) ResolveApprovedProfiles(
	ctx context.Context,
	fullStack repository.ExactReference,
	release ExactTemplateRelease,
) ([]templates.LanguageServerProfile, error) {
	if source == nil || ctx == nil || !canonicalUUID(fullStack.ID) ||
		!digestPattern.MatchString(fullStack.ContentHash) || release.Validate() != nil {
		return nil, ErrProfileNotDeclared
	}
	resolved, err := source.registry.ResolveForNewBuild(ctx, templates.ExactFullStackTemplateRef{
		ID: fullStack.ID, ContentHash: fullStack.ContentHash,
	})
	if err != nil {
		return nil, err
	}
	view := resolved.Template.Snapshot()
	if view.ID != fullStack.ID || view.ContentHash != fullStack.ContentHash {
		return nil, ErrProfileNotDeclared
	}
	for _, component := range resolved.Components {
		if component.Release.ID() != release.ID || component.Release.ContentHash() != release.ContentHash {
			continue
		}
		releaseView := component.Release.Snapshot()
		profiles := make([]templates.LanguageServerProfile, len(releaseView.Manifest.LanguageServers))
		for index, profile := range releaseView.Manifest.LanguageServers {
			if templates.ValidateLanguageServerProfile(profile) != nil {
				return nil, ErrProfileNotDeclared
			}
			profiles[index] = cloneTemplateProfile(profile)
		}
		sort.Slice(profiles, func(left, right int) bool { return profiles[left].ID < profiles[right].ID })
		return profiles, nil
	}
	return nil, ErrProfileNotDeclared
}

var _ TicketAuthority = (*AuthoritySource)(nil)
var _ AuthorityProfileSource = (*RegistryProfileSource)(nil)
