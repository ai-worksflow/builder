package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	PortSchemaVersion         = "sandbox-port/v1"
	PreviewGrantSchemaVersion = "sandbox-preview-grant/v1"
)

var (
	ErrPortInvalid             = errors.New("invalid sandbox port request")
	ErrPortNotFound            = errors.New("sandbox port is not declared by the exact session")
	ErrPortNotReady            = errors.New("sandbox port is not ready")
	ErrPortUnavailable         = errors.New("sandbox port service is not configured")
	ErrPreviewGrantInvalid     = errors.New("invalid sandbox preview grant")
	ErrPreviewGrantExpired     = errors.New("sandbox preview grant is expired or unknown")
	ErrPreviewGrantUnavailable = errors.New("sandbox preview grant store is unavailable")
)

type PortState string

const (
	PortUnavailable PortState = "unavailable"
	PortStarting    PortState = "starting"
	PortListening   PortState = "listening"
)

type PortView struct {
	SchemaVersion string    `json:"schemaVersion"`
	Name          string    `json:"name"`
	ServiceID     string    `json:"serviceId"`
	Number        int       `json:"number"`
	Protocol      string    `json:"protocol"`
	State         PortState `json:"state"`
	Healthy       bool      `json:"healthy"`
	Previewable   bool      `json:"previewable"`
}

type PortList struct {
	Session SessionView `json:"session"`
	Ports   []PortView  `json:"ports"`
}

type IssuePreviewInput struct {
	ProjectID              string
	SessionID              string
	PortName               string
	ActorID                string
	ExpectedSessionVersion uint64
	ExpectedSessionEpoch   uint64
}

type PreviewGrant struct {
	SchemaVersion string    `json:"schemaVersion"`
	ID            string    `json:"id"`
	ProjectID     string    `json:"projectId"`
	SessionID     string    `json:"sessionId"`
	SessionEpoch  uint64    `json:"sessionEpoch"`
	ActorID       string    `json:"actorId"`
	PortName      string    `json:"portName"`
	PortNumber    int       `json:"portNumber"`
	Protocol      string    `json:"protocol"`
	IssuedAt      time.Time `json:"issuedAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

type PreviewLink struct {
	SchemaVersion string    `json:"schemaVersion"`
	ID            string    `json:"id"`
	SessionID     string    `json:"sessionId"`
	SessionEpoch  uint64    `json:"sessionEpoch"`
	Port          PortView  `json:"port"`
	URL           string    `json:"url"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

type PreviewTarget struct {
	Grant     PreviewGrant
	Port      PortView
	Address   string
	ExpiresAt time.Time
}

type PreviewGrantStore interface {
	Put(context.Context, string, PreviewGrant, time.Duration) error
	Get(context.Context, string) (PreviewGrant, error)
}

type PortService struct {
	sessions     ControlSessionStore
	candidates   CandidateControls
	workspaces   *WorkspaceMaterializer
	runtime      RuntimeManager
	access       ProjectAuthorizer
	grants       PreviewGrantStore
	publicOrigin *url.URL
	dialHost     string
	ticketTTL    time.Duration
	probeTimeout time.Duration
	newSecret    func() (string, string, error)
	now          func() time.Time
	probe        func(context.Context, string, int, string, time.Duration) bool
}

func NewPortService(
	sessions ControlSessionStore,
	candidates CandidateControls,
	workspaces *WorkspaceMaterializer,
	runtime RuntimeManager,
	access ProjectAuthorizer,
	grants PreviewGrantStore,
	publicOrigin string,
	dialHost string,
	ticketTTL time.Duration,
	probeTimeout time.Duration,
) (*PortService, error) {
	return newPortService(
		sessions, candidates, workspaces, runtime, access, grants, publicOrigin, dialHost,
		ticketTTL, probeTimeout, newPreviewSecret, time.Now, probeSandboxPort,
	)
}

func newPortService(
	sessions ControlSessionStore,
	candidates CandidateControls,
	workspaces *WorkspaceMaterializer,
	runtime RuntimeManager,
	access ProjectAuthorizer,
	grants PreviewGrantStore,
	publicOrigin string,
	dialHost string,
	ticketTTL time.Duration,
	probeTimeout time.Duration,
	newSecret func() (string, string, error),
	now func() time.Time,
	probe func(context.Context, string, int, string, time.Duration) bool,
) (*PortService, error) {
	origin, err := normalizePreviewPublicOrigin(publicOrigin)
	if sessions == nil || candidates == nil || workspaces == nil || runtime == nil || access == nil ||
		grants == nil || err != nil || !validPreviewDialHost(dialHost) || ticketTTL < 30*time.Second ||
		ticketTTL > time.Hour || probeTimeout < 50*time.Millisecond || probeTimeout > 5*time.Second ||
		newSecret == nil || now == nil || probe == nil {
		return nil, ErrPortUnavailable
	}
	return &PortService{
		sessions: sessions, candidates: candidates, workspaces: workspaces, runtime: runtime,
		access: access, grants: grants, publicOrigin: origin, dialHost: strings.TrimSpace(dialHost),
		ticketTTL: ticketTTL, probeTimeout: probeTimeout, newSecret: newSecret, now: now, probe: probe,
	}, nil
}

func (service *PortService) List(
	ctx context.Context,
	projectID, sessionID, actorID string,
) (PortList, error) {
	if service == nil || ctx == nil || !validUUID(projectID) || !validUUID(sessionID) || !validUUID(actorID) {
		return PortList{}, ErrPortInvalid
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return PortList{}, fmt.Errorf("authorize sandbox ports: %w", err)
	}
	session, err := service.sessions.Get(ctx, projectID, sessionID)
	if err != nil {
		return PortList{}, err
	}
	view := session.Snapshot()
	ports := declaredPortViews(view.AllowedPorts)
	if view.State != StateReady || len(ports) == 0 {
		return PortList{Session: view, Ports: ports}, nil
	}
	view, err = service.syncReadyCandidateProjection(ctx, view, actorID)
	if err != nil {
		return PortList{}, err
	}
	status, err := service.runtimeStatus(ctx, view)
	if err != nil {
		// Port discovery is an observational endpoint. A stale workspace or
		// runtime projection must never expose the old runtime, but it also must
		// not turn the frontend's health polling into a stream of 409 responses.
		// Keep the declared ports visible and conservatively mark them
		// unavailable until lifecycle reconciliation catches up.
		if errors.Is(err, ErrSessionProjectionStale) || errors.Is(err, ErrWorkspaceConflict) ||
			errors.Is(err, ErrRuntimeConflict) {
			return PortList{Session: view, Ports: ports}, nil
		}
		return PortList{}, err
	}
	if status.State != "running" || !status.Healthy {
		return PortList{Session: view, Ports: ports}, nil
	}

	var workers sync.WaitGroup
	semaphore := make(chan struct{}, 8)
	for index := range ports {
		hostPort, exists := status.HostPorts[ports[index].Name]
		if !exists {
			continue
		}
		ports[index].State = PortStarting
		workers.Add(1)
		go func(index, hostPort int) {
			defer workers.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			if service.probe(ctx, service.dialHost, hostPort, ports[index].Protocol, service.probeTimeout) {
				ports[index].State = PortListening
				ports[index].Healthy = true
			}
		}(index, hostPort)
	}
	workers.Wait()
	return PortList{Session: view, Ports: ports}, nil
}

func (service *PortService) syncReadyCandidateProjection(
	ctx context.Context,
	view SessionView,
	actorID string,
) (SessionView, error) {
	record, err := service.candidates.Get(ctx, view.ProjectID, view.Candidate.ID)
	if err != nil {
		return SessionView{}, err
	}
	if candidateProjectionMatches(view.Candidate, record.Candidate) {
		return view, nil
	}
	if view.State != StateReady {
		return SessionView{}, ErrSessionProjectionStale
	}
	synced, err := service.sessions.SyncCandidate(ctx, view.ProjectID, view.ID, view.Version, view.SessionEpoch, actorID)
	if err != nil {
		return SessionView{}, err
	}
	return synced.Snapshot(), nil
}

func (service *PortService) Issue(
	ctx context.Context,
	input IssuePreviewInput,
) (PreviewLink, error) {
	if service == nil || ctx == nil || !validUUID(input.ProjectID) || !validUUID(input.SessionID) ||
		!validUUID(input.ActorID) || !slugPattern.MatchString(strings.TrimSpace(input.PortName)) ||
		input.ExpectedSessionVersion == 0 || input.ExpectedSessionEpoch == 0 {
		return PreviewLink{}, ErrPortInvalid
	}
	if err := service.access.RequireProjectView(ctx, input.ProjectID, input.ActorID); err != nil {
		return PreviewLink{}, fmt.Errorf("authorize sandbox preview: %w", err)
	}
	session, err := service.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return PreviewLink{}, err
	}
	if err := session.Authorize(ActionView, input.ExpectedSessionVersion, input.ExpectedSessionEpoch); err != nil {
		return PreviewLink{}, err
	}
	view := session.Snapshot()
	if view.State != StateReady {
		return PreviewLink{}, ErrPortNotReady
	}
	// Writer-lease heartbeats advance Candidate/session fences without changing
	// the source tree. Bring a ready session forward before inspecting its
	// runtime so preview creation does not spuriously fail between port polling
	// and this capability request.
	view, err = service.syncReadyCandidateProjection(ctx, view, input.ActorID)
	if err != nil {
		return PreviewLink{}, err
	}
	port, ok := exactAllowedPort(view.AllowedPorts, strings.TrimSpace(input.PortName))
	if !ok {
		return PreviewLink{}, ErrPortNotFound
	}
	if port.Protocol != "http" && port.Protocol != "https" {
		return PreviewLink{}, ErrPortInvalid
	}
	status, err := service.runtimeStatus(ctx, view)
	if err != nil {
		return PreviewLink{}, err
	}
	hostPort, exposed := status.HostPorts[port.Name]
	if status.State != "running" || !status.Healthy || !exposed ||
		!service.probe(ctx, service.dialHost, hostPort, port.Protocol, service.probeTimeout) {
		return PreviewLink{}, ErrPortNotReady
	}

	now := service.now().UTC()
	if now.IsZero() || now.Before(view.UpdatedAt) || !now.Before(view.TTL.ExpiresAt) {
		return PreviewLink{}, ErrPreviewGrantInvalid
	}
	ttl := service.ticketTTL
	if remaining := view.TTL.ExpiresAt.Sub(now); remaining < ttl {
		ttl = remaining
	}
	if ttl <= 0 {
		return PreviewLink{}, ErrPreviewGrantInvalid
	}
	id, secret, err := service.newSecret()
	if err != nil || !validUUID(id) || !validPreviewSecret(secret) {
		return PreviewLink{}, fmt.Errorf("%w: generate capability", ErrPreviewGrantUnavailable)
	}
	grant := PreviewGrant{
		SchemaVersion: PreviewGrantSchemaVersion, ID: id, ProjectID: view.ProjectID,
		SessionID: view.ID, SessionEpoch: view.SessionEpoch, ActorID: input.ActorID,
		PortName: port.Name, PortNumber: port.Number, Protocol: port.Protocol,
		IssuedAt: now, ExpiresAt: now.Add(ttl),
	}
	if err := validatePreviewGrant(grant, now); err != nil {
		return PreviewLink{}, err
	}
	if err := service.grants.Put(ctx, secret, grant, ttl); err != nil {
		return PreviewLink{}, err
	}
	portView := portView(port)
	portView.State, portView.Healthy = PortListening, true
	return PreviewLink{
		SchemaVersion: PreviewGrantSchemaVersion, ID: id, SessionID: view.ID,
		SessionEpoch: view.SessionEpoch, Port: portView,
		URL: service.previewURL(secret), ExpiresAt: grant.ExpiresAt,
	}, nil
}

func (service *PortService) Resolve(
	ctx context.Context,
	secret string,
) (PreviewTarget, error) {
	if service == nil || ctx == nil || !validPreviewSecret(secret) {
		return PreviewTarget{}, ErrPreviewGrantInvalid
	}
	grant, err := service.grants.Get(ctx, secret)
	if err != nil {
		return PreviewTarget{}, err
	}
	now := service.now().UTC()
	if err := validatePreviewGrant(grant, now); err != nil {
		return PreviewTarget{}, ErrPreviewGrantExpired
	}
	if err := service.access.RequireProjectView(ctx, grant.ProjectID, grant.ActorID); err != nil {
		return PreviewTarget{}, fmt.Errorf("re-authorize sandbox preview: %w", err)
	}
	session, err := service.sessions.Get(ctx, grant.ProjectID, grant.SessionID)
	if err != nil {
		return PreviewTarget{}, err
	}
	view := session.Snapshot()
	if view.SessionEpoch != grant.SessionEpoch {
		return PreviewTarget{}, ErrEpochFenced
	}
	if view.State != StateReady || !now.Before(view.TTL.ExpiresAt) {
		return PreviewTarget{}, ErrPortNotReady
	}
	port, exists := exactAllowedPort(view.AllowedPorts, grant.PortName)
	if !exists || port.Number != grant.PortNumber || port.Protocol != grant.Protocol {
		return PreviewTarget{}, ErrPortNotFound
	}
	status, err := service.runtimeStatus(ctx, view)
	if err != nil {
		return PreviewTarget{}, err
	}
	hostPort, exposed := status.HostPorts[port.Name]
	if status.State != "running" || !status.Healthy || !exposed {
		return PreviewTarget{}, ErrPortNotReady
	}
	viewPort := portView(port)
	viewPort.State, viewPort.Healthy = PortListening, true
	return PreviewTarget{
		Grant: grant, Port: viewPort,
		Address:   net.JoinHostPort(service.dialHost, strconv.Itoa(hostPort)),
		ExpiresAt: grant.ExpiresAt,
	}, nil
}

func (service *PortService) runtimeStatus(ctx context.Context, view SessionView) (RuntimeStatus, error) {
	record, err := service.candidates.Get(ctx, view.ProjectID, view.Candidate.ID)
	if err != nil {
		return RuntimeStatus{}, err
	}
	if !candidateProjectionMatches(view.Candidate, record.Candidate) {
		return RuntimeStatus{}, ErrSessionProjectionStale
	}
	mount, err := service.workspaces.Materialize(ctx, view, record.Candidate)
	if err != nil {
		return RuntimeStatus{}, err
	}
	spec, err := RuntimeSpecForSession(view, mount)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return service.runtime.Inspect(ctx, spec)
}

func declaredPortViews(values []AllowedPort) []PortView {
	result := make([]PortView, len(values))
	for index, value := range values {
		result[index] = portView(value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result
}

func portView(port AllowedPort) PortView {
	return PortView{
		SchemaVersion: PortSchemaVersion, Name: port.Name, ServiceID: port.ServiceID,
		Number: port.Number, Protocol: port.Protocol, State: PortUnavailable,
		Previewable: port.Protocol == "http" || port.Protocol == "https",
	}
}

func exactAllowedPort(values []AllowedPort, name string) (AllowedPort, bool) {
	for _, value := range values {
		if value.Name == name {
			return value, true
		}
	}
	return AllowedPort{}, false
}

func normalizePreviewPublicOrigin(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.User != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || net.ParseIP(parsed.Hostname()) != nil {
		return nil, ErrPortInvalid
	}
	parsed.Path, parsed.RawPath = "", ""
	return parsed, nil
}

func validPreviewDialHost(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 253 || strings.ContainsAny(value, "/?#@\r\n\x00") {
		return false
	}
	if net.ParseIP(value) != nil {
		return true
	}
	return !strings.Contains(value, ":")
}

func (service *PortService) previewURL(secret string) string {
	copy := *service.publicOrigin
	copy.Host = secret + "." + service.publicOrigin.Host
	copy.Path = "/"
	return copy.String()
}

func newPreviewSecret() (string, string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", "", err
	}
	return uuid.NewString(), hex.EncodeToString(value), nil
}

func validPreviewSecret(value string) bool {
	if len(value) != 48 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 24 && value == strings.ToLower(value)
}

func validatePreviewGrant(grant PreviewGrant, now time.Time) error {
	if grant.SchemaVersion != PreviewGrantSchemaVersion || !validUUID(grant.ID) ||
		!validUUID(grant.ProjectID) || !validUUID(grant.SessionID) || !validUUID(grant.ActorID) ||
		grant.SessionEpoch == 0 || !slugPattern.MatchString(grant.PortName) ||
		grant.PortNumber < 1024 || grant.PortNumber > 65535 ||
		(grant.Protocol != "http" && grant.Protocol != "https") || now.IsZero() ||
		grant.IssuedAt.IsZero() || grant.ExpiresAt.IsZero() || grant.ExpiresAt.Before(grant.IssuedAt) ||
		now.Before(grant.IssuedAt) || !now.Before(grant.ExpiresAt) || grant.ExpiresAt.Sub(grant.IssuedAt) > time.Hour {
		return ErrPreviewGrantInvalid
	}
	return nil
}

func probeSandboxPort(
	ctx context.Context,
	host string,
	port int,
	protocol string,
	timeout time.Duration,
) bool {
	if ctx == nil || !validPreviewDialHost(host) || port < 1 || port > 65535 || timeout <= 0 {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	address := net.JoinHostPort(host, strconv.Itoa(port))
	if protocol == "tcp" {
		connection, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", address)
		if err != nil {
			return false
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(timeout))
		if _, err := connection.Write([]byte{0}); err != nil {
			return false
		}
		buffer := make([]byte, 1)
		_, err = connection.Read(buffer)
		if err == nil {
			return true
		}
		var netError net.Error
		return errors.As(err, &netError) && netError.Timeout()
	}
	if protocol != "http" && protocol != "https" {
		return false
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(dialContext context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(dialContext, "tcp", address)
		},
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, // #nosec G402 -- exact isolated sandbox target may use a self-signed development certificate.
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, protocol+"://sandbox-port/", nil)
	if err != nil {
		return false
	}
	request.Host = net.JoinHostPort("localhost", strconv.Itoa(port))
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.CopyN(io.Discard, response.Body, 1)
	return true
}
