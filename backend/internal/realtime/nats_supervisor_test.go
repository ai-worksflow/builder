package realtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fanoutStarterStub struct {
	mu       sync.Mutex
	starts   int
	channels []chan error
	fail     bool
}

func (s *fanoutStarterStub) Start(context.Context) (<-chan error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts++
	if s.fail {
		return nil, errors.New("subscribe failed")
	}
	channel := make(chan error, 1)
	s.channels = append(s.channels, channel)
	return channel, nil
}

func (s *fanoutStarterStub) snapshot() (int, []chan error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts, append([]chan error(nil), s.channels...)
}

func TestFanoutSupervisorMarksDeathUnhealthyAndRecovers(t *testing.T) {
	worker := &fanoutStarterStub{}
	supervisor, err := NewFanoutSupervisor(worker, testLogger(), FanoutSupervisorConfig{
		InitialBackoff: 150 * time.Millisecond, MaxBackoff: 300 * time.Millisecond, StableAfter: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		supervisor.Run(ctx)
		close(done)
	}()
	waitForFanoutState(t, supervisor, true, time.Second)
	_, channels := worker.snapshot()
	if len(channels) != 1 {
		t.Fatalf("initial subscriptions=%d", len(channels))
	}
	channels[0] <- errors.New("subscription died")
	waitForFanoutState(t, supervisor, false, 100*time.Millisecond)
	waitForFanoutState(t, supervisor, true, time.Second)
	starts, channels := worker.snapshot()
	if starts < 2 || len(channels) < 2 {
		t.Fatalf("fanout was not resubscribed: starts=%d channels=%d", starts, len(channels))
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fanout supervisor did not stop on context cancellation")
	}
}

func TestFanoutSupervisorBackoffPreventsBusyLoop(t *testing.T) {
	worker := &fanoutStarterStub{fail: true}
	supervisor, err := NewFanoutSupervisor(worker, testLogger(), FanoutSupervisorConfig{
		InitialBackoff: 20 * time.Millisecond, MaxBackoff: 40 * time.Millisecond, StableAfter: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 95*time.Millisecond)
	defer cancel()
	supervisor.Run(ctx)
	starts, _ := worker.snapshot()
	if starts < 2 || starts > 4 {
		t.Fatalf("unexpected retry count without bounded exponential backoff: %d", starts)
	}
	if err := supervisor.Readiness(context.Background()); err == nil {
		t.Fatal("failed fanout reported healthy")
	}
}

func waitForFanoutState(t *testing.T, supervisor *FanoutSupervisor, healthy bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := supervisor.Readiness(context.Background())
		if (err == nil) == healthy {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("fanout healthy=%v was not observed", healthy)
}
