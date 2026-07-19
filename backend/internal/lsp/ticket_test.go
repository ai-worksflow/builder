package lsp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/templates"
)

const (
	testActor   = "10000000-0000-4000-8000-000000000005"
	testRelease = "10000000-0000-4000-8000-000000000006"
	testTicket  = "10000000-0000-4000-8000-000000000007"
)

type ticketStoreFake struct {
	secret     string
	grant      TicketGrant
	ttl        time.Duration
	puts       int
	consumed   bool
	consumeErr error
}

func (store *ticketStoreFake) Put(
	_ context.Context,
	secret string,
	grant TicketGrant,
	ttl time.Duration,
) error {
	store.puts++
	store.secret, store.grant, store.ttl = secret, grant, ttl
	return nil
}

func (store *ticketStoreFake) Consume(_ context.Context, secret string) (TicketGrant, error) {
	if store.consumeErr != nil {
		return TicketGrant{}, store.consumeErr
	}
	if store.consumed || secret != store.secret {
		return TicketGrant{}, ErrTicketConsumed
	}
	store.consumed = true
	return store.grant, nil
}

type ticketAuthorityFake struct {
	view AuthorityView
	err  error
	gets int
}

func (authority *ticketAuthorityFake) GetLSPAuthority(
	context.Context,
	string,
	string,
	ExactTemplateRelease,
) (AuthorityView, error) {
	authority.gets++
	return authority.view, authority.err
}

type ticketAccessFake struct {
	viewErr error
	editErr error
	views   int
	edits   int
}

func (access *ticketAccessFake) RequireProjectView(context.Context, string, string) error {
	access.views++
	return access.viewErr
}

func (access *ticketAccessFake) RequireProjectEdit(context.Context, string, string) error {
	access.edits++
	return access.editErr
}

func lspTestLimits() EffectiveLimits {
	return EffectiveLimits{
		StartupTimeoutMillis: 10_000, RequestTimeoutMillis: 5_000, ShutdownTimeoutMillis: 2_000,
		CPUMillis: 1_000, MemoryBytes: 512 << 20, PIDLimit: 64, TempBytes: 256 << 20,
		CacheBytes: 256 << 20, MaxOpenDocuments: 4, MaxDocumentBytes: 1 << 20,
		MaxTotalSyncBytes: 8 << 20, MaxFrameBytes: 512 << 10, MaxResultBytes: 1 << 20,
		MaxConcurrentRequests: 8, RequestsPerSecond: 10, RequestBurst: 20,
		MaxDiagnosticsPerDocument: 1_000, MaxCompletionItems: 200, MaxNavigationLocations: 2_000,
	}
}

func lspTestProfile(id string) ProfileIdentity {
	profile := templates.LanguageServerProfile{
		SchemaVersion: templates.LanguageServerProfileSchemaVersion,
		ID:            id, ServiceID: "web", LanguageIDs: []string{"typescript"}, FileGlobs: []string{"**/*.ts"},
		ProtocolVersion: "3.17",
		Runtime: templates.LanguageServerRuntime{
			Image:          "ghcr.io/worksflow/lsp@" + lspDigest("3"),
			ExecutablePath: "/opt/lsp/typescript-language-server", ExecutableDigest: lspDigest("4"),
			Argv:                   []string{"/opt/lsp/typescript-language-server", "--stdio"},
			WorkingDirectoryPolicy: "service-root",
		},
		ServerInfo:                   templates.LanguageServerInfo{Name: "typescript-language-server", Version: "4.3.3"},
		InitializationParametersHash: templates.ProductionV1InitializationParametersHash(),
		WorkspaceConfigurationHash:   templates.ProductionV1WorkspaceConfigurationHash(),
		RequireVersionedDiagnostics:  true, Methods: []string{"textDocument/hover"}, Limits: lspTestLimits(),
		Isolation: templates.LanguageServerIsolation{
			NetworkPolicy: "none", WorkspaceMountPolicy: "read-only",
			TempPolicy: "isolated-bounded", CachePolicy: "isolated-bounded",
			WorkspacePluginPolicy: "forbidden", DynamicSDKPolicy: "forbidden",
			DynamicRegistrationPolicy: "forbidden", ConfigurationCommandPolicy: "forbidden",
			PackageManagerHookPolicy: "forbidden",
		},
	}
	profile.CapabilityHash, _ = templates.ComputeLanguageServerCapabilityHash(profile.Methods)
	profile.ContentHash, _ = templates.ComputeLanguageServerProfileContentHash(profile)
	return ProfileIdentity{
		LanguageServerProfile: profile,
		TemplateRelease:       ExactTemplateRelease{ID: testRelease, ContentHash: lspDigest("2")},
		EffectiveLimits:       profile.Limits,
	}
}

func lspTicketFixture(t *testing.T, mode TicketMode) (
	*TicketService,
	*ticketStoreFake,
	*ticketAuthorityFake,
	*ticketAccessFake,
	IssueTicketInput,
	*time.Time,
) {
	t.Helper()
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	head := validHead()
	authority := &ticketAuthorityFake{view: AuthorityView{
		Head: head, TemplateRelease: ExactTemplateRelease{ID: testRelease, ContentHash: lspDigest("2")},
		State: "ready", WriterLeaseOwnerID: testActor,
		SessionExpiresAt: now.Add(time.Hour), Profiles: []ProfileIdentity{lspTestProfile("typescript")},
	}}
	store := &ticketStoreFake{}
	access := &ticketAccessFake{}
	secret := strings.Repeat("A", 43)
	service, err := newTicketService(
		store, authority, access, 30*time.Second, func() time.Time { return now },
		func() (string, string, error) { return testTicket, secret, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	input := IssueTicketInput{
		ProjectID: testProject, SessionID: testSession, ActorID: testActor,
		Origin: "https://Builder.Example/", Mode: mode, Head: head,
		TemplateRelease: ExactTemplateRelease{ID: testRelease, ContentHash: lspDigest("2")},
		ProfileIDs:      []string{"typescript"},
	}
	return service, store, authority, access, input, &now
}

func TestTicketIssueAndConsumeBindExactAuthorityAndBurnSecret(t *testing.T) {
	service, store, authority, access, input, _ := lspTicketFixture(t, TicketModeEditor)
	view, err := service.Issue(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if store.puts != 1 || store.ttl != 30*time.Second || access.views != 1 || access.edits != 1 ||
		authority.gets != 1 || view.SchemaVersion != TicketSchemaVersion ||
		view.WebSocketPath != TicketWebSocketPath || view.Subprotocol != TicketSubprotocol ||
		view.Mode != TicketModeEditor || !view.Head.Equal(input.Head) || len(view.Profiles) != 1 ||
		view.TemplateRelease != input.TemplateRelease ||
		store.grant.Origin != "https://builder.example" || store.grant.ActorID != testActor {
		t.Fatalf("ticket did not bind exact authority: view=%#v grant=%#v", view, store.grant)
	}
	grant, err := service.Consume(context.Background(), store.secret, "https://builder.example/")
	if err != nil || grant.ID != testTicket || !grant.Head.Equal(input.Head) ||
		access.views != 2 || access.edits != 2 || authority.gets != 2 || !store.consumed {
		t.Fatalf("consume did not burn and revalidate exact grant: grant=%#v err=%v", grant, err)
	}
	if _, err := service.Consume(context.Background(), store.secret, "https://builder.example"); !errors.Is(err, ErrTicketConsumed) {
		t.Fatalf("ticket replay = %v", err)
	}
}

func TestTicketIssueFailsClosedForHeadProfileStateAndEditorLease(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*IssueTicketInput, *ticketAuthorityFake, *ticketAccessFake)
		want   error
	}{
		{
			name: "head stale", want: ErrHeadStale,
			mutate: func(_ *IssueTicketInput, authority *ticketAuthorityFake, _ *ticketAccessFake) {
				authority.view.Head.Version++
			},
		},
		{
			name: "session suspended", want: ErrSessionNotReady,
			mutate: func(_ *IssueTicketInput, authority *ticketAuthorityFake, _ *ticketAccessFake) {
				authority.view.State = "suspended"
			},
		},
		{
			name: "profile absent", want: ErrProfileNotDeclared,
			mutate: func(input *IssueTicketInput, _ *ticketAuthorityFake, _ *ticketAccessFake) {
				input.ProfileIDs = []string{"go"}
			},
		},
		{
			name: "editor lease owner differs", want: ErrForbidden,
			mutate: func(_ *IssueTicketInput, authority *ticketAuthorityFake, _ *ticketAccessFake) {
				authority.view.WriterLeaseOwnerID = testOpen
			},
		},
		{
			name: "edit permission denied", want: ErrForbidden,
			mutate: func(_ *IssueTicketInput, _ *ticketAuthorityFake, access *ticketAccessFake) {
				access.editErr = errors.New("denied")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, store, authority, access, input, _ := lspTicketFixture(t, TicketModeEditor)
			test.mutate(&input, authority, access)
			if _, err := service.Issue(context.Background(), input); !errors.Is(err, test.want) || store.puts != 0 {
				t.Fatalf("unsafe ticket issue: err=%v puts=%d", err, store.puts)
			}
		})
	}
}

func TestSnapshotTicketNeedsViewButNotWriterLease(t *testing.T) {
	service, store, authority, access, input, _ := lspTicketFixture(t, TicketModeSnapshot)
	authority.view.WriterLeaseOwnerID = ""
	authority.view.Head.WriterLeaseEpoch = 0
	input.Head.WriterLeaseEpoch = 0
	if _, err := service.Issue(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if store.puts != 1 || access.views != 1 || access.edits != 0 {
		t.Fatalf("snapshot authorization = views:%d edits:%d puts:%d", access.views, access.edits, store.puts)
	}
}

func TestTicketConsumeBurnsBeforeOriginOrAuthorityFailure(t *testing.T) {
	service, store, authority, _, input, _ := lspTicketFixture(t, TicketModeSnapshot)
	if _, err := service.Issue(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Consume(context.Background(), store.secret, "https://attacker.example"); !errors.Is(err, ErrTicketInvalid) || !store.consumed {
		t.Fatalf("origin drift did not burn ticket: consumed=%v err=%v", store.consumed, err)
	}

	store.consumed = false
	authority.view.Head.Version++
	if _, err := service.Consume(context.Background(), store.secret, input.Origin); !errors.Is(err, ErrHeadStale) || !store.consumed {
		t.Fatalf("head drift did not burn ticket: consumed=%v err=%v", store.consumed, err)
	}
}

func TestTicketLimitsOriginAndProfileOrderAreBounded(t *testing.T) {
	profile := lspTestProfile("typescript")
	profile.EffectiveLimits.MaxOpenDocuments = 33
	if !errors.Is(profile.Validate(), ErrTicketInvalid) {
		t.Fatal("over-limit profile was accepted")
	}
	for _, origin := range []string{
		"http://builder.example", "file:///tmp", "https://user@example.test", "https://example.test/path",
	} {
		if _, err := normalizeTicketOrigin(origin); !errors.Is(err, ErrOriginForbidden) {
			t.Fatalf("unsafe origin accepted: %q (%v)", origin, err)
		}
	}
	for _, origin := range []string{"http://localhost:3000", "http://127.0.0.1:3000", "https://builder.example"} {
		if _, err := normalizeTicketOrigin(origin); err != nil {
			t.Fatalf("safe origin rejected: %q (%v)", origin, err)
		}
	}
	if _, err := validateRequestedProfiles([]string{"typescript", "go"}); !errors.Is(err, ErrProfileNotDeclared) {
		t.Fatalf("noncanonical profile order was accepted: %v", err)
	}
	if _, err := validateRequestedProfiles([]string{"go", "typescript"}); !errors.Is(err, ErrProfileNotDeclared) {
		t.Fatalf("multi-profile ticket was accepted without multiplexing: %v", err)
	}
}
