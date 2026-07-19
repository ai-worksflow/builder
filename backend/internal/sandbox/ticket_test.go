package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type connectionTicketStoreFake struct {
	secret     string
	grant      ConnectionTicketGrant
	ttl        time.Duration
	puts       int
	consumed   bool
	consumeErr error
}

func (store *connectionTicketStoreFake) Put(
	_ context.Context,
	secret string,
	grant ConnectionTicketGrant,
	ttl time.Duration,
) error {
	store.puts++
	store.secret = secret
	store.grant = grant
	store.ttl = ttl
	return nil
}

func (store *connectionTicketStoreFake) Consume(_ context.Context, secret string) (ConnectionTicketGrant, error) {
	if store.consumeErr != nil {
		return ConnectionTicketGrant{}, store.consumeErr
	}
	if store.consumed || secret != store.secret {
		return ConnectionTicketGrant{}, ErrConnectionTicketConsumed
	}
	store.consumed = true
	return store.grant, nil
}

func TestConnectionTicketConsumeBurnsAndRevalidatesOriginAndEpoch(t *testing.T) {
	session := readyTestSession(t, cleanCandidate(t), sandboxBaseTime)
	store := &connectionTicketStoreFake{}
	now := sandboxBaseTime.Add(3 * time.Second)
	secret := strings.Repeat("C", 43)
	service, err := newConnectionTicketService(
		store, &connectionTicketSessionsFake{session: session}, &facadeAccessFake{},
		30*time.Second, func() time.Time { return now },
		func() (string, string, error) { return testCheckpoint, secret, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Issue(context.Background(), IssueConnectionTicketInput{
		ProjectID: testProjectID, SessionID: testSessionID, ActorID: testActorID,
		Origin: "https://builder.example", Channels: []StreamChannel{ChannelControl, ChannelPTY},
	}); err != nil {
		t.Fatal(err)
	}
	grant, err := service.Consume(context.Background(), secret, "https://builder.example/")
	if err != nil || grant.SessionEpoch != session.Snapshot().SessionEpoch {
		t.Fatalf("consume exact grant = %#v, %v", grant, err)
	}
	if _, err := service.Consume(context.Background(), secret, "https://builder.example"); !errors.Is(err, ErrConnectionTicketConsumed) {
		t.Fatalf("replayed ticket error = %v", err)
	}

	store.consumed = false
	if _, err := service.Consume(context.Background(), secret, "https://attacker.example"); !errors.Is(err, ErrConnectionTicketInvalid) || !store.consumed {
		t.Fatalf("cross-origin ticket was not burned: consumed=%v err=%v", store.consumed, err)
	}
}

type connectionTicketSessionsFake struct {
	session SandboxSession
}

func (sessions *connectionTicketSessionsFake) Get(context.Context, string, string) (SandboxSession, error) {
	return sessions.session.Clone(), nil
}

func TestConnectionTicketBindsExactEpochOriginChannelsAndCursors(t *testing.T) {
	session := readyTestSession(t, cleanCandidate(t), sandboxBaseTime)
	store := &connectionTicketStoreFake{}
	now := sandboxBaseTime.Add(3 * time.Second)
	secret := strings.Repeat("A", 43)
	service, err := newConnectionTicketService(
		store,
		&connectionTicketSessionsFake{session: session},
		&facadeAccessFake{},
		30*time.Second,
		func() time.Time { return now },
		func() (string, string, error) { return testCheckpoint, secret, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.Issue(context.Background(), IssueConnectionTicketInput{
		ProjectID: testProjectID, SessionID: testSessionID, ActorID: testActorID,
		Origin:   "HTTPS://Builder.Example/",
		Channels: []StreamChannel{ChannelPTY, ChannelControl, ChannelFilesystem},
		Cursors: []ConnectionCursor{
			{Channel: ChannelPTY, LastAckedSeq: 8},
			{Channel: ChannelControl, LastAckedSeq: 3},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedChannels := []StreamChannel{ChannelControl, ChannelFilesystem, ChannelPTY}
	if store.puts != 1 || store.secret != secret || store.ttl != 30*time.Second ||
		view.Ticket != secret || view.SessionEpoch != session.Snapshot().SessionEpoch ||
		!equalStreamChannels(view.Channels, expectedChannels) ||
		store.grant.Origin != "https://builder.example" || store.grant.ActorID != testActorID ||
		store.grant.SessionEpoch != session.Snapshot().SessionEpoch || !store.grant.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("ticket did not seal exact stream scope: view=%#v grant=%#v", view, store.grant)
	}
	if len(view.Cursors) != 3 || view.Cursors[0] != (ConnectionCursor{Channel: ChannelControl, LastAckedSeq: 3}) ||
		view.Cursors[1] != (ConnectionCursor{Channel: ChannelFilesystem}) ||
		view.Cursors[2] != (ConnectionCursor{Channel: ChannelPTY, LastAckedSeq: 8}) {
		t.Fatalf("ticket cursors were not normalized: %#v", view.Cursors)
	}
}

func TestConnectionTicketRejectsUnauthorizedOrUnavailableChannelBeforeStorage(t *testing.T) {
	for _, test := range []struct {
		name     string
		session  SandboxSession
		access   *facadeAccessFake
		channels []StreamChannel
		want     error
	}{
		{
			name: "PTY before ready", session: newTestSession(t, cleanCandidate(t), sandboxBaseTime),
			access: &facadeAccessFake{}, channels: []StreamChannel{ChannelPTY}, want: ErrActionBlocked,
		},
		{
			name: "control role denied", session: readyTestSession(t, cleanCandidate(t), sandboxBaseTime),
			access: &facadeAccessFake{controlErr: errors.New("denied")}, channels: []StreamChannel{ChannelFilesystem},
			want: errors.New("denied"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &connectionTicketStoreFake{}
			service, err := newConnectionTicketService(
				store, &connectionTicketSessionsFake{session: test.session}, test.access,
				30*time.Second, func() time.Time { return sandboxBaseTime.Add(3 * time.Second) },
				func() (string, string, error) { return testCheckpoint, strings.Repeat("A", 43), nil },
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Issue(context.Background(), IssueConnectionTicketInput{
				ProjectID: testProjectID, SessionID: testSessionID, ActorID: testActorID,
				Origin: "https://builder.example", Channels: test.channels,
			})
			if err == nil || (errors.Is(test.want, ErrActionBlocked) && !errors.Is(err, test.want)) ||
				(!errors.Is(test.want, ErrActionBlocked) && !strings.Contains(err.Error(), test.want.Error())) || store.puts != 0 {
				t.Fatalf("unsafe ticket issued: puts=%d err=%v", store.puts, err)
			}
		})
	}
}

func TestConnectionTicketRejectsUnknownDuplicateAndUnrequestedCursor(t *testing.T) {
	for _, input := range []IssueConnectionTicketInput{
		{Channels: []StreamChannel{"unknown"}},
		{Channels: []StreamChannel{ChannelControl, ChannelControl}},
		{Channels: []StreamChannel{ChannelControl}, Cursors: []ConnectionCursor{{Channel: ChannelPTY, LastAckedSeq: 1}}},
	} {
		if _, _, _, err := normalizeConnectionScope(input.Channels, input.Cursors); !errors.Is(err, ErrConnectionTicketInvalid) {
			t.Fatalf("invalid scope accepted: %#v err=%v", input, err)
		}
	}
}
