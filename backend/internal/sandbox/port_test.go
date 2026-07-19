package sandbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

type previewGrantStoreFake struct {
	values map[string]PreviewGrant
	ttls   map[string]time.Duration
}

func (store *previewGrantStoreFake) Put(_ context.Context, secret string, grant PreviewGrant, ttl time.Duration) error {
	if _, exists := store.values[secret]; exists {
		return ErrPreviewGrantUnavailable
	}
	store.values[secret], store.ttls[secret] = grant, ttl
	return nil
}

func (store *previewGrantStoreFake) Get(_ context.Context, secret string) (PreviewGrant, error) {
	grant, ok := store.values[secret]
	if !ok {
		return PreviewGrant{}, ErrPreviewGrantExpired
	}
	return grant, nil
}

type portRuntimeFake struct {
	status      RuntimeStatus
	inspectSpec RuntimeSpec
	inspectErr  error
}

func (runtime *portRuntimeFake) Ensure(context.Context, RuntimeSpec) (RuntimeStatus, error) {
	return RuntimeStatus{}, errors.New("unexpected ensure")
}
func (runtime *portRuntimeFake) Start(context.Context, RuntimeSpec) (RuntimeStatus, error) {
	return RuntimeStatus{}, errors.New("unexpected start")
}
func (runtime *portRuntimeFake) WaitReady(context.Context, RuntimeSpec) (RuntimeStatus, error) {
	return RuntimeStatus{}, errors.New("unexpected wait")
}
func (runtime *portRuntimeFake) Suspend(context.Context, RuntimeSpec) (RuntimeStatus, error) {
	return RuntimeStatus{}, errors.New("unexpected suspend")
}
func (runtime *portRuntimeFake) Resume(context.Context, RuntimeSpec) (RuntimeStatus, error) {
	return RuntimeStatus{}, errors.New("unexpected resume")
}
func (runtime *portRuntimeFake) Terminate(context.Context, RuntimeSpec) error {
	return errors.New("unexpected terminate")
}
func (runtime *portRuntimeFake) Inspect(_ context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	runtime.inspectSpec = spec
	return runtime.status, runtime.inspectErr
}

func TestPortServiceListsOnlyExactRuntimePortsAndIssuesCapabilityHost(t *testing.T) {
	service, sessions, runtime, grants, view := portServiceFixture(t)
	probes := make([]string, 0, 4)
	var probesMu sync.Mutex
	service.probe = func(_ context.Context, host string, port int, protocol string, _ time.Duration) bool {
		probesMu.Lock()
		probes = append(probes, host+":"+strconv.Itoa(port)+":"+protocol)
		probesMu.Unlock()
		return port == 32000
	}

	listed, err := service.List(context.Background(), view.ProjectID, view.ID, testActorID)
	if err != nil {
		t.Fatal(err)
	}
	probesMu.Lock()
	probeCount := len(probes)
	probeSnapshot := append([]string(nil), probes...)
	probesMu.Unlock()
	if len(listed.Ports) != 2 || listed.Ports[0].Name != "api-http" || listed.Ports[0].State != PortStarting ||
		listed.Ports[1].Name != "web-http" || listed.Ports[1].State != PortListening || !listed.Ports[1].Healthy ||
		probeCount != 2 || runtime.inspectSpec.SessionEpoch != view.SessionEpoch {
		t.Fatalf("port projection = %#v probes=%#v spec=%#v", listed, probeSnapshot, runtime.inspectSpec)
	}

	link, err := service.Issue(context.Background(), IssuePreviewInput{
		ProjectID: view.ProjectID, SessionID: view.ID, PortName: "web-http", ActorID: testActorID,
		ExpectedSessionVersion: view.Version, ExpectedSessionEpoch: view.SessionEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("a", 48)
	if link.URL != "http://"+secret+".preview.localhost:8080/" || link.Port.State != PortListening ||
		link.SessionEpoch != view.SessionEpoch || len(grants.values) != 1 || grants.values[secret].PortNumber != 3000 ||
		grants.ttls[secret] != 15*time.Minute {
		t.Fatalf("preview link = %#v grants=%#v ttls=%#v", link, grants.values, grants.ttls)
	}

	target, err := service.Resolve(context.Background(), secret)
	if err != nil || target.Address != "127.0.0.1:32000" || target.Port.Name != "web-http" ||
		target.Grant.SessionID != view.ID {
		t.Fatalf("resolved target = %#v, %v", target, err)
	}

	sessions.session.document.SessionEpoch++
	if _, err := service.Resolve(context.Background(), secret); !errors.Is(err, ErrEpochFenced) {
		t.Fatalf("old capability survived epoch rotation: %v", err)
	}
}

func TestPortServiceRefusesUndeclaredUnreadyAndRawTCPPreview(t *testing.T) {
	service, sessions, runtime, _, view := portServiceFixture(t)
	service.probe = func(context.Context, string, int, string, time.Duration) bool { return false }
	base := IssuePreviewInput{
		ProjectID: view.ProjectID, SessionID: view.ID, PortName: "web-http", ActorID: testActorID,
		ExpectedSessionVersion: view.Version, ExpectedSessionEpoch: view.SessionEpoch,
	}
	if _, err := service.Issue(context.Background(), base); !errors.Is(err, ErrPortNotReady) {
		t.Fatalf("unready port was issued: %v", err)
	}
	base.PortName = "not-declared"
	if _, err := service.Issue(context.Background(), base); !errors.Is(err, ErrPortNotFound) {
		t.Fatalf("undeclared port was issued: %v", err)
	}

	base.PortName = "web-http"
	sessions.session.document.AllowedPorts[1].Protocol = "tcp"
	runtime.status.HostPorts["web-http"] = 32000
	if _, err := service.Issue(context.Background(), base); !errors.Is(err, ErrPortInvalid) {
		t.Fatalf("raw TCP preview was issued: %v", err)
	}
}

func TestProbeSandboxPortRequiresAnUpstreamHTTPResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Probe", request.Host)
		writer.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, rawPort, found := strings.Cut(parsed.Host, ":")
	if !found {
		t.Fatalf("test server has no port: %s", parsed.Host)
	}
	port, _ := strconv.Atoi(rawPort)
	if !probeSandboxPort(context.Background(), host, port, "http", time.Second) {
		t.Fatal("healthy HTTP port was not detected")
	}
	if probeSandboxPort(context.Background(), host, port, "smtp", time.Second) {
		t.Fatal("unsupported protocol was detected as healthy")
	}
}

func portServiceFixture(t *testing.T) (
	*PortService,
	*controlSessionsFake,
	*portRuntimeFake,
	*previewGrantStoreFake,
	SessionView,
) {
	t.Helper()
	candidate, blobs := workspaceCandidate(t, map[string][]byte{"package.json": []byte("{}\n")})
	ready := readyTestSession(t, candidate, sandboxBaseTime)
	view := ready.Snapshot()
	candidates := &controlCandidatesFake{
		record: repository.CandidateMutationRecord{Candidate: candidate}, checkpoints: map[string]repository.CandidateSnapshot{},
	}
	sessions := &controlSessionsFake{session: ready, candidates: candidates}
	workspaces, err := NewWorkspaceMaterializer(t.TempDir(), &workspaceResolverFake{values: blobs})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &portRuntimeFake{status: RuntimeStatus{
		ID: "runtime", SessionID: view.ID, SessionEpoch: view.SessionEpoch,
		State: "running", Healthy: true,
		HostPorts: map[string]int{"web-http": 32000, "api-http": 32001},
	}}
	grants := &previewGrantStoreFake{values: map[string]PreviewGrant{}, ttls: map[string]time.Duration{}}
	service, err := newPortService(
		sessions, candidates, workspaces, runtime, &facadeAccessFake{}, grants,
		"http://preview.localhost:8080", "127.0.0.1", 15*time.Minute, 250*time.Millisecond,
		func() (string, string, error) {
			return "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", strings.Repeat("a", 48), nil
		},
		func() time.Time { return sandboxBaseTime.Add(10 * time.Second) },
		func(context.Context, string, int, string, time.Duration) bool { return true },
	)
	if err != nil {
		t.Fatal(err)
	}
	return service, sessions, runtime, grants, view
}
