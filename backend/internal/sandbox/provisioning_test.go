package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

type provisioningSessionsFake struct {
	input NewSessionInput
	now   time.Time
	calls int
}

func (sessions *provisioningSessionsFake) Create(
	_ context.Context,
	input NewSessionInput,
	now time.Time,
) (SandboxSession, error) {
	sessions.calls++
	sessions.input = input
	sessions.now = now
	return NewSession(input, now)
}

type provisioningResolverFake struct {
	configuration SessionConfiguration
	candidate     repository.CandidateWorkspace
	calls         int
}

type provisioningBootstrapFake struct {
	initial   SessionView
	candidate repository.CandidateWorkspace
	calls     int
}

func (bootstrap *provisioningBootstrapFake) Start(
	_ context.Context,
	initial SessionView,
	candidate repository.CandidateWorkspace,
) (SessionView, error) {
	bootstrap.calls++
	bootstrap.initial = initial
	bootstrap.candidate = candidate
	return initial, nil
}

func (resolver *provisioningResolverFake) ResolveSessionConfiguration(
	_ context.Context,
	candidate repository.CandidateWorkspace,
) (SessionConfiguration, error) {
	resolver.calls++
	resolver.candidate = candidate
	return resolver.configuration, nil
}

func TestProvisioningServiceCreatesExactProjectCandidateSession(t *testing.T) {
	candidate := cleanCandidate(t)
	sessions := &provisioningSessionsFake{}
	candidates := &facadeCandidatesFake{record: repository.CandidateMutationRecord{Candidate: candidate}}
	input := testSessionInput(candidate)
	resolver := &provisioningResolverFake{configuration: SessionConfiguration{
		Services: input.Services,
		Ports:    input.Ports,
	}}
	access := &facadeAccessFake{}
	now := sandboxBaseTime.Add(time.Second)
	service, err := newProvisioningService(
		sessions, candidates, resolver, access,
		ProvisioningPolicy{RunnerImageDigest: input.RunnerImageDigest, Quota: input.Quota, TTL: input.TTL},
		func() time.Time { return now },
		func() string { return testSessionID },
	)
	if err != nil {
		t.Fatal(err)
	}

	view, err := service.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: testProjectID, CandidateID: testCandidateID, ActorID: testActorID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sessions.calls != 1 || resolver.calls != 1 || view.ID != testSessionID ||
		view.ProjectID != testProjectID || view.Candidate.ID != testCandidateID ||
		view.State != StateProvisioning || sessions.input.Candidate.CurrentTree.TreeHash != candidate.CurrentTree.TreeHash ||
		resolver.candidate.BuildContract != candidate.BuildContract || !sessions.now.Equal(now) {
		t.Fatalf("provisioning did not preserve exact Candidate lineage: session=%#v input=%#v", view, sessions.input)
	}
}

func TestProvisioningServiceFailsClosedBeforeCreatingStaleCandidate(t *testing.T) {
	candidate := cleanCandidate(t)
	stale, _, err := candidate.UpdateFlags(
		candidate.Version, candidate.SessionEpoch, testActorID,
		"upstream advanced",
		repository.CandidateFlags{Stale: true, RebaseRequired: true},
		sandboxBaseTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	sessions := &provisioningSessionsFake{}
	input := testSessionInput(candidate)
	service, err := newProvisioningService(
		sessions,
		&facadeCandidatesFake{record: repository.CandidateMutationRecord{Candidate: stale}},
		&provisioningResolverFake{configuration: SessionConfiguration{Services: input.Services, Ports: input.Ports}},
		&facadeAccessFake{},
		ProvisioningPolicy{RunnerImageDigest: input.RunnerImageDigest, Quota: input.Quota, TTL: input.TTL},
		func() time.Time { return sandboxBaseTime.Add(time.Second) },
		func() string { return testSessionID },
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: testProjectID, CandidateID: testCandidateID, ActorID: testActorID,
	})
	if !errors.Is(err, ErrActionBlocked) || sessions.calls != 0 {
		t.Fatalf("stale Candidate was provisioned: calls=%d err=%v", sessions.calls, err)
	}
}

func TestProvisioningServiceBootstrapsOnlyAfterExactSessionCommit(t *testing.T) {
	candidate := cleanCandidate(t)
	input := testSessionInput(candidate)
	bootstrap := &provisioningBootstrapFake{}
	service, err := newProvisioningService(
		&provisioningSessionsFake{},
		&facadeCandidatesFake{record: repository.CandidateMutationRecord{Candidate: candidate}},
		&provisioningResolverFake{configuration: SessionConfiguration{Services: input.Services, Ports: input.Ports}},
		&facadeAccessFake{},
		ProvisioningPolicy{RunnerImageDigest: input.RunnerImageDigest, Quota: input.Quota, TTL: input.TTL},
		func() time.Time { return sandboxBaseTime.Add(time.Second) },
		func() string { return testSessionID },
		bootstrap,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: testProjectID, CandidateID: testCandidateID, ActorID: testActorID,
	}); err != nil {
		t.Fatal(err)
	}
	if bootstrap.calls != 1 || bootstrap.initial.State != StateProvisioning ||
		bootstrap.initial.ID != testSessionID || bootstrap.candidate.CurrentTree.TreeHash != candidate.CurrentTree.TreeHash {
		t.Fatalf("runtime bootstrap did not receive the committed exact session: %#v / %#v", bootstrap.initial, bootstrap.candidate)
	}
}

func TestPlatformServiceReportsDisabledProvisioning(t *testing.T) {
	platform := &PlatformService{}
	if _, err := platform.CreateSession(context.Background(), CreateSessionInput{}); !errors.Is(err, ErrProvisioningUnavailable) {
		t.Fatalf("disabled provisioning error = %v", err)
	}
}
