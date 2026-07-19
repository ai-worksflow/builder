package lsp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/sandbox"
	"github.com/worksflow/builder/backend/internal/templates"
)

type runtimeBindingAuthorityFake struct {
	views []AuthorityView
	err   error
	gets  int
}

func (fake *runtimeBindingAuthorityFake) GetLSPAuthority(
	context.Context,
	string,
	string,
	ExactTemplateRelease,
) (AuthorityView, error) {
	index := fake.gets
	fake.gets++
	if fake.err != nil {
		return AuthorityView{}, fake.err
	}
	if len(fake.views) == 0 {
		return AuthorityView{}, ErrAuthorityUnavailable
	}
	if index >= len(fake.views) {
		index = len(fake.views) - 1
	}
	return fake.views[index], nil
}

type runtimeBindingWorkspaceFake struct {
	mount sandbox.WorkspaceMount
	err   error
	calls int
}

func (fake *runtimeBindingWorkspaceFake) Materialize(
	_ context.Context,
	_ sandbox.SessionView,
	_ repository.CandidateWorkspace,
) (sandbox.WorkspaceMount, error) {
	fake.calls++
	return fake.mount, fake.err
}

type runtimeBindingFilesFake struct {
	pointer repository.FileBlobPointer
	value   []byte
	err     error
	calls   int
}

func (fake *runtimeBindingFilesFake) Resolve(
	_ context.Context,
	_, _ string,
	_ int64,
) (repository.FileBlobPointer, []byte, error) {
	fake.calls++
	return fake.pointer, append([]byte(nil), fake.value...), fake.err
}

type runtimeBindingServiceRootFake struct {
	root  string
	err   error
	calls int
}

func (fake *runtimeBindingServiceRootFake) ResolveServiceRoot(
	context.Context,
	repository.ExactReference,
	ExactTemplateRelease,
	ProfileIdentity,
) (string, error) {
	fake.calls++
	return fake.root, fake.err
}

type runtimeBindingFixture struct {
	source     *RuntimeBindingSource
	authority  *runtimeBindingAuthorityFake
	sessions   *authoritySessionFake
	candidates *authorityCandidateFake
	workspace  *runtimeBindingWorkspaceFake
	files      *runtimeBindingFilesFake
	templates  *runtimeBindingServiceRootFake
	grant      TicketGrant
	bind       ClientBind
	content    []byte
}

func newRuntimeBindingFixture(t *testing.T, repositoryPath string, content []byte) runtimeBindingFixture {
	t.Helper()
	base := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	digest := runtimeBindingDigest(content)
	tree, err := repository.NewTree([]repository.TreeFile{{
		Path: repositoryPath, Mode: "100644", ContentHash: digest, ByteSize: int64(len(content)),
	}})
	if err != nil {
		t.Fatalf("create exact tree: %v", err)
	}
	candidate, err := repository.NewCandidate(testCandidate, repository.RepositorySnapshot{
		ID: testSnapshot, ProjectID: testProject,
		BuildManifest:     repository.ExactReference{ID: testManifest, ContentHash: lspDigest("1")},
		BuildContract:     repository.ExactReference{ID: testContract, ContentHash: lspDigest("2")},
		FullStackTemplate: repository.ExactReference{ID: testFullStack, ContentHash: lspDigest("3")},
		Tree:              tree, CreatedBy: testActor, CreatedAt: base,
	}, testActor, base)
	if err != nil {
		t.Fatalf("create exact Candidate: %v", err)
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
		t.Fatalf("create exact SandboxSession: %v", err)
	}
	session, err = session.BeginStart(1, 1, base.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.MarkReady(2, 1, base.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	record := repository.CandidateMutationRecord{
		Candidate: candidate,
		CurrentTreePointer: repository.TreeBlobPointer{
			OwnerID: candidate.ID, TreeHash: candidate.CurrentTree.TreeHash,
		},
	}
	now := base.Add(4 * time.Second)
	head, _, err := exactAuthorityHead(session.Snapshot(), record, now)
	if err != nil {
		t.Fatalf("derive exact authority head: %v", err)
	}
	profile := lspTestProfile("typescript")
	view := AuthorityView{
		Head: head, TemplateRelease: profile.TemplateRelease, State: sandbox.StateReady.String(),
		SessionExpiresAt: session.Snapshot().TTL.ExpiresAt, Profiles: []ProfileIdentity{profile},
	}
	grant := TicketGrant{
		SchemaVersion: TicketSchemaVersion, ID: testTicket, ProjectID: testProject,
		SessionID: testSession, ActorID: testActor, Origin: "https://builder.example",
		Mode: TicketModeSnapshot, Head: head, TemplateRelease: profile.TemplateRelease,
		Profiles: []ProfileIdentity{profile}, IssuedAt: now.Add(-time.Second), ExpiresAt: now.Add(29 * time.Second),
	}
	modelURI, err := CandidateModelURI(testProject, testCandidate, repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	bind := ClientBind{
		SchemaVersion: BindingSchemaVersion, Kind: "client.bind", ConnectionID: testConnection,
		Sequence: 1, Head: head, Profile: profile,
		Documents: []DocumentFence{{
			ModelURI: modelURI, OpenID: testOpen, ModelVersion: 1, SavedContentHash: digest,
		}},
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	authority := &runtimeBindingAuthorityFake{views: []AuthorityView{view}}
	sessions := &authoritySessionFake{session: session}
	candidates := &authorityCandidateFake{record: record}
	workspace := &runtimeBindingWorkspaceFake{mount: sandbox.WorkspaceMount{Workspace: root}}
	files := &runtimeBindingFilesFake{
		pointer: repository.FileBlobPointer{ContentHash: digest, ByteSize: int64(len(content))},
		value:   append([]byte(nil), content...),
	}
	serviceRoots := &runtimeBindingServiceRootFake{root: "apps/web"}
	source, err := NewRuntimeBindingSource(
		authority, sessions, candidates, workspace, files, serviceRoots, func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	return runtimeBindingFixture{
		source: source, authority: authority, sessions: sessions, candidates: candidates,
		workspace: workspace, files: files, templates: serviceRoots,
		grant: grant, bind: bind, content: append([]byte(nil), content...),
	}
}

func runtimeBindingDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return fmt.Sprintf("sha256:%x", digest[:])
}

func TestRuntimeBindingSourceResolvesExactImmutableCandidateProjection(t *testing.T) {
	fixture := newRuntimeBindingFixture(t, "apps/web/page.ts", []byte("export const value = 1\n"))
	projection, err := fixture.source.Resolve(context.Background(), fixture.grant, fixture.bind)
	if err != nil {
		t.Fatal(err)
	}
	wantServicePath := filepath.Join(fixture.workspace.mount.Workspace, "apps", "web")
	if !projection.Head.Equal(fixture.grant.Head) ||
		!equalProfiles([]ProfileIdentity{projection.Profile}, []ProfileIdentity{fixture.grant.Profiles[0]}) ||
		projection.WorkspaceRoot != fixture.workspace.mount.Workspace || projection.ServiceRoot != "apps/web" ||
		projection.ServicePath != wantServicePath || len(projection.Files) != 1 ||
		projection.Files[0] != (RuntimeFileFence{
			Path: "apps/web/page.ts", Mode: "100644", ContentHash: fixture.bind.Documents[0].SavedContentHash,
			ByteSize: int64(len(fixture.content)),
		}) || len(projection.Documents) != 1 ||
		projection.Documents[0].Path != "apps/web/page.ts" || projection.Documents[0].Mode != "100644" ||
		string(projection.Documents[0].Text) != string(fixture.content) ||
		fixture.authority.gets != 2 || fixture.sessions.gets != 1 || fixture.candidates.gets != 1 ||
		fixture.workspace.calls != 1 || fixture.files.calls != 1 || fixture.templates.calls != 1 {
		t.Fatalf("runtime projection drifted: projection=%#v calls=%d/%d/%d/%d/%d/%d",
			projection, fixture.authority.gets, fixture.sessions.gets, fixture.candidates.gets,
			fixture.workspace.calls, fixture.files.calls, fixture.templates.calls)
	}
	projection.Documents[0].Text[0] = 'X'
	again, err := fixture.source.Resolve(context.Background(), fixture.grant, fixture.bind)
	if err != nil || string(again.Documents[0].Text) != string(fixture.content) {
		t.Fatalf("caller mutated resolved immutable bytes: %q, %v", again.Documents[0].Text, err)
	}
}

func TestRuntimeBindingSourceRejectsClosingAuthorityDrift(t *testing.T) {
	fixture := newRuntimeBindingFixture(t, "apps/web/page.ts", []byte("export const value = 1\n"))
	opening := fixture.authority.views[0]
	closing := opening
	closing.Head.Version++
	fixture.authority.views = []AuthorityView{opening, closing}
	if _, err := fixture.source.Resolve(context.Background(), fixture.grant, fixture.bind); !errors.Is(err, ErrRuntimeBindingStale) {
		t.Fatalf("closing authority drift escaped: %v", err)
	}
	if fixture.authority.gets != 2 || fixture.workspace.calls != 1 {
		t.Fatalf("closing fence was not checked after materialization: authority=%d workspace=%d",
			fixture.authority.gets, fixture.workspace.calls)
	}
}

func TestRuntimeBindingSourceRevalidatesSteadyStateDocumentAuthority(t *testing.T) {
	fixture := newRuntimeBindingFixture(t, "apps/web/page.ts", []byte("export const value = 1\n"))
	if err := fixture.source.RevalidateGatewayFence(
		context.Background(), fixture.grant, fixture.bind.Head,
		fixture.bind.Profile, fixture.bind.Documents,
	); err != nil {
		t.Fatalf("exact steady-state fence was rejected: %v", err)
	}
	stale := append([]DocumentFence(nil), fixture.bind.Documents...)
	stale[0].SavedContentHash = lspDigest("9")
	if err := fixture.source.RevalidateGatewayFence(
		context.Background(), fixture.grant, fixture.bind.Head,
		fixture.bind.Profile, stale,
	); !errors.Is(err, ErrRuntimeBindingStale) {
		t.Fatalf("document not present at the exact Candidate head escaped: %v", err)
	}
	foreignHead := fixture.bind.Head
	foreignHead.SessionEpoch++
	if err := fixture.source.RevalidateGatewayFence(
		context.Background(), fixture.grant, foreignHead,
		fixture.bind.Profile, fixture.bind.Documents,
	); !errors.Is(err, ErrRuntimeBindingStale) {
		t.Fatalf("non-successor gateway head escaped: %v", err)
	}
}

func TestRuntimeBindingSourceRejectsUntrustedDocumentFacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		path   string
		value  []byte
		mutate func(*runtimeBindingFixture)
		want   error
	}{
		{name: "unsupported profile glob", path: "apps/web/page.tsx", value: []byte("export {}\n"), want: ErrRuntimeDocumentInvalid},
		{name: "outside exact template service root", path: "apps/api/page.ts", value: []byte("export {}\n"), want: ErrRuntimeDocumentInvalid},
		{name: "non UTF-8 source", path: "apps/web/page.ts", value: []byte{0xff, 0xfe}, want: ErrRuntimeDocumentInvalid},
		{name: "NUL source", path: "apps/web/page.ts", value: []byte{'a', 0, 'b'}, want: ErrRuntimeDocumentInvalid},
		{
			name: "browser hash disagrees with tree", path: "apps/web/page.ts", value: []byte("export {}\n"),
			mutate: func(fixture *runtimeBindingFixture) { fixture.bind.Documents[0].SavedContentHash = lspDigest("9") },
			want:   ErrRuntimeDocumentInvalid,
		},
		{
			name: "blob pointer disagrees with tree", path: "apps/web/page.ts", value: []byte("export {}\n"),
			mutate: func(fixture *runtimeBindingFixture) { fixture.files.pointer.ContentHash = lspDigest("9") },
			want:   ErrRuntimeDocumentInvalid,
		},
		{
			name: "blob bytes disagree with tree", path: "apps/web/page.ts", value: []byte("export {}\n"),
			mutate: func(fixture *runtimeBindingFixture) { fixture.files.value[0] = 'X' },
			want:   ErrRuntimeDocumentInvalid,
		},
		{
			name: "document exceeds admitted profile limit", path: "apps/web/page.ts", value: []byte("export {}\n"),
			mutate: func(fixture *runtimeBindingFixture) {
				profile := fixture.bind.Profile
				profile.Limits.MaxDocumentBytes = int64(len(fixture.content) - 1)
				profile.EffectiveLimits = profile.Limits
				profile.ContentHash, _ = templates.ComputeLanguageServerProfileContentHash(profile.LanguageServerProfile)
				fixture.bind.Profile = profile
				fixture.grant.Profiles = []ProfileIdentity{profile}
				fixture.authority.views[0].Profiles = []ProfileIdentity{profile}
			},
			want: ErrRuntimeDocumentInvalid,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeBindingFixture(t, test.path, test.value)
			if test.mutate != nil {
				test.mutate(&fixture)
			}
			if _, err := fixture.source.Resolve(context.Background(), fixture.grant, fixture.bind); !errors.Is(err, test.want) {
				t.Fatalf("untrusted document facts escaped: %v", err)
			}
		})
	}
}

func TestRuntimeBindingSourceRejectsSymlinkedServiceRoot(t *testing.T) {
	fixture := newRuntimeBindingFixture(t, "apps/web/page.ts", []byte("export {}\n"))
	if err := os.Remove(filepath.Join(fixture.workspace.mount.Workspace, "apps", "web")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(fixture.workspace.mount.Workspace, "apps", "web")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.source.Resolve(context.Background(), fixture.grant, fixture.bind); !errors.Is(err, ErrRuntimeBindingUnavailable) {
		t.Fatalf("symlinked service root escaped: %v", err)
	}
}

func TestEffectiveTemplateServiceRootIsCanonicalAndContained(t *testing.T) {
	for _, test := range []struct {
		mount, service, want string
		valid                bool
	}{
		{mount: ".", service: ".", want: ".", valid: true},
		{mount: "apps/web", service: ".", want: "apps/web", valid: true},
		{mount: "packages", service: "api", want: "packages/api", valid: true},
		{mount: "apps/../web", service: "."},
		{mount: "apps", service: "../secret"},
		{mount: "/host", service: "."},
		{mount: ".", service: ""},
	} {
		got, err := effectiveTemplateServiceRoot(test.mount, test.service)
		if test.valid && (err != nil || got != test.want) {
			t.Fatalf("root %q + %q = %q, %v", test.mount, test.service, got, err)
		}
		if !test.valid && !errors.Is(err, ErrProfileNotDeclared) {
			t.Fatalf("unsafe root %q + %q escaped: %q, %v", test.mount, test.service, got, err)
		}
	}
}

func TestLanguageServerGlobMatchingIsBoundedAndSegmentScoped(t *testing.T) {
	pattern := make([]string, 0, 66)
	for range 64 {
		pattern = append(pattern, "**")
	}
	pattern = append(pattern, "*.ts")
	value := make([]string, 0, 65)
	for range 64 {
		value = append(value, "nested")
	}
	value = append(value, "page.ts")
	if !matchLanguageServerGlob(pattern, value) ||
		matchLanguageServerGlob([]string{"apps", "*.ts"}, []string{"apps", "nested", "page.ts"}) ||
		matchLanguageServerGlob([]string{"**", "*.ts"}, []string{"apps", "page.tsx"}) {
		t.Fatal("language-server glob matching drifted")
	}
}
