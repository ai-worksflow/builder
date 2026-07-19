package lsp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/sandbox"
	"github.com/worksflow/builder/backend/internal/templates"
)

const (
	testSnapshot  = "10000000-0000-4000-8000-000000000008"
	testManifest  = "10000000-0000-4000-8000-000000000009"
	testContract  = "10000000-0000-4000-8000-000000000010"
	testFullStack = "10000000-0000-4000-8000-000000000011"
)

type authoritySessionFake struct {
	session sandbox.SandboxSession
	err     error
	gets    int
}

func (fake *authoritySessionFake) Get(context.Context, string, string) (sandbox.SandboxSession, error) {
	fake.gets++
	return fake.session.Clone(), fake.err
}

type authorityCandidateFake struct {
	record repository.CandidateMutationRecord
	err    error
	gets   int
}

func (fake *authorityCandidateFake) Get(context.Context, string, string) (repository.CandidateMutationRecord, error) {
	fake.gets++
	return fake.record, fake.err
}

type authorityProfileFake struct {
	profiles  []templates.LanguageServerProfile
	err       error
	gets      int
	fullStack repository.ExactReference
	release   ExactTemplateRelease
}

func (fake *authorityProfileFake) ResolveApprovedProfiles(
	_ context.Context,
	fullStack repository.ExactReference,
	release ExactTemplateRelease,
) ([]templates.LanguageServerProfile, error) {
	fake.gets++
	fake.fullStack, fake.release = fullStack, release
	result := make([]templates.LanguageServerProfile, len(fake.profiles))
	for index, profile := range fake.profiles {
		result[index] = cloneTemplateProfile(profile)
	}
	return result, fake.err
}

func authorityFixture(t *testing.T, ready bool) (
	*AuthoritySource,
	*authoritySessionFake,
	*authorityCandidateFake,
	*authorityProfileFake,
	time.Time,
) {
	t.Helper()
	base := time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)
	tree, err := repository.NewTree(nil)
	if err != nil {
		t.Fatalf("create tree: %v", err)
	}
	candidate, err := repository.NewCandidate(testCandidate, repository.RepositorySnapshot{
		ID: testSnapshot, ProjectID: testProject,
		BuildManifest:     repository.ExactReference{ID: testManifest, ContentHash: lspDigest("1")},
		BuildContract:     repository.ExactReference{ID: testContract, ContentHash: lspDigest("2")},
		FullStackTemplate: repository.ExactReference{ID: testFullStack, ContentHash: lspDigest("3")},
		Tree:              tree, CreatedBy: testActor, CreatedAt: base,
	}, testActor, base)
	if err != nil {
		t.Fatalf("create Candidate: %v", err)
	}
	session, err := sandbox.NewSession(sandbox.NewSessionInput{
		ID: testSession, ActorID: testActor, Candidate: candidate,
		RunnerImageDigest: lspDigest("4"),
		Quota: sandbox.Quota{
			CPUMillis: 1_000, MemoryBytes: 512 << 20, WorkspaceBytes: 1 << 30,
			PIDLimit: 64, PreviewPortLimit: 3,
		},
		TTL: sandbox.TTLPolicy{IdleHibernateAfter: time.Hour, MaxRuntime: 4 * time.Hour},
		Services: []sandbox.AllowedService{{
			ID: "web", Kind: "web", Profiles: []string{"dev"},
			TemplateRelease: repository.ExactReference{ID: testRelease, ContentHash: lspDigest("2")},
		}},
		Ports: []sandbox.AllowedPort{{Name: "web-http", ServiceID: "web", Number: 3000, Protocol: "http"}},
	}, base.Add(time.Second))
	if err != nil {
		t.Fatalf("create SandboxSession: %v", err)
	}
	if ready {
		session, err = session.BeginStart(1, 1, base.Add(2*time.Second))
		if err != nil {
			t.Fatalf("begin SandboxSession start: %v", err)
		}
		session, err = session.MarkReady(2, 1, base.Add(3*time.Second))
		if err != nil {
			t.Fatalf("mark SandboxSession ready: %v", err)
		}
	}
	sessions := &authoritySessionFake{session: session}
	candidates := &authorityCandidateFake{record: repository.CandidateMutationRecord{
		Candidate: candidate,
		CurrentTreePointer: repository.TreeBlobPointer{
			OwnerID: candidate.ID, TreeHash: candidate.CurrentTree.TreeHash,
		},
	}}
	profiles := &authorityProfileFake{profiles: []templates.LanguageServerProfile{
		lspTestProfile("typescript").LanguageServerProfile,
	}}
	now := base.Add(4 * time.Second)
	source, err := NewAuthoritySource(sessions, candidates, profiles, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return source, sessions, candidates, profiles, now
}

func TestAuthoritySourceBuildsExactHeadFromRepositoryAndApprovedProfile(t *testing.T) {
	source, sessions, candidates, profiles, _ := authorityFixture(t, true)
	release := ExactTemplateRelease{ID: testRelease, ContentHash: lspDigest("2")}
	view, err := source.GetLSPAuthority(context.Background(), testProject, testSession, release)
	if err != nil {
		t.Fatal(err)
	}
	if sessions.gets != 1 || candidates.gets != 1 || profiles.gets != 1 ||
		profiles.fullStack.ID != testFullStack || profiles.release != release ||
		view.State != sandbox.StateReady.String() || view.TemplateRelease != release ||
		view.Head.ProjectID != testProject || view.Head.SessionID != testSession ||
		view.Head.CandidateID != testCandidate || view.Head.Version != 1 ||
		view.Head.SessionEpoch != 1 || view.Head.JournalSequence != 0 ||
		view.Head.WriterLeaseEpoch != 0 || view.WriterLeaseOwnerID != "" ||
		len(view.Profiles) != 1 || view.Profiles[0].ID != "typescript" || view.Profiles[0].Validate() != nil {
		t.Fatalf("authority projection drifted: %#v", view)
	}
	view.Profiles[0].Runtime.Argv[0] = "/mutated"
	again, err := source.GetLSPAuthority(context.Background(), testProject, testSession, release)
	if err != nil || again.Profiles[0].Runtime.Argv[0] == "/mutated" {
		t.Fatalf("authority profile storage was mutable: %#v, %v", again.Profiles, err)
	}
}

func TestAuthoritySourceFailsClosedForProjectionReleaseAndServiceDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*authorityCandidateFake, *authorityProfileFake)
		want   error
	}{
		{
			name: "Candidate version",
			mutate: func(candidate *authorityCandidateFake, _ *authorityProfileFake) {
				candidate.record.Candidate.Version++
			},
			want: ErrHeadStale,
		},
		{
			name: "tree pointer",
			mutate: func(candidate *authorityCandidateFake, _ *authorityProfileFake) {
				candidate.record.CurrentTreePointer.TreeHash = lspDigest("9")
			},
			want: ErrHeadStale,
		},
		{
			name: "profile service",
			mutate: func(_ *authorityCandidateFake, profiles *authorityProfileFake) {
				profiles.profiles[0].ServiceID = "api"
				profiles.profiles[0].ContentHash, _ = templates.ComputeLanguageServerProfileContentHash(profiles.profiles[0])
			},
			want: ErrProfileNotDeclared,
		},
		{
			name: "registry unavailable",
			mutate: func(_ *authorityCandidateFake, profiles *authorityProfileFake) {
				profiles.err = errors.New("registry offline")
			},
			want: ErrProfileNotDeclared,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, _, candidate, profiles, _ := authorityFixture(t, true)
			test.mutate(candidate, profiles)
			_, err := source.GetLSPAuthority(
				context.Background(), testProject, testSession,
				ExactTemplateRelease{ID: testRelease, ContentHash: lspDigest("2")},
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("drift escaped authority: %v", err)
			}
		})
	}

	source, _, candidate, profiles, _ := authorityFixture(t, true)
	_, err := source.GetLSPAuthority(context.Background(), testProject, testSession, ExactTemplateRelease{
		ID: testRelease, ContentHash: lspDigest("9"),
	})
	if !errors.Is(err, ErrProfileNotDeclared) || candidate.gets != 1 || profiles.gets != 0 {
		t.Fatalf("foreign exact release escaped session projection: err=%v candidate=%d profiles=%d", err, candidate.gets, profiles.gets)
	}
}

func TestAuthoritySourceDoesNotResolveCandidateOrProfilesUntilSessionIsReady(t *testing.T) {
	source, _, candidates, profiles, _ := authorityFixture(t, false)
	view, err := source.GetLSPAuthority(
		context.Background(), testProject, testSession,
		ExactTemplateRelease{ID: testRelease, ContentHash: lspDigest("2")},
	)
	if err != nil || view.State != sandbox.StateProvisioning.String() ||
		candidates.gets != 0 || profiles.gets != 0 {
		t.Fatalf("non-ready session resolved mutable authority: view=%#v err=%v", view, err)
	}
}
