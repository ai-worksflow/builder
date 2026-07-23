package qualificationrelease

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type candidateSourceFunc func(context.Context) (Target, error)

func (function candidateSourceFunc) Next(ctx context.Context) (Target, error) {
	return function(ctx)
}

type publisherFunc func(context.Context, Target, string) (TerminalRecord, error)

func (function publisherFunc) Publish(ctx context.Context, target Target, owner string) (TerminalRecord, error) {
	return function(ctx, target, owner)
}

func TestWorkerBacksOffWhenReadyTargetHasNoClaimableAuthority(t *testing.T) {
	worker, err := NewWorker(
		candidateSourceFunc(func(context.Context) (Target, error) { return testTarget, nil }),
		publisherFunc(func(context.Context, Target, string) (TerminalRecord, error) {
			return TerminalRecord{}, ErrNotReady
		}),
		"qualified-worker/01",
	)
	if err != nil {
		t.Fatal(err)
	}
	worked, err := worker.RunOne(context.Background())
	if err != nil || worked {
		t.Fatalf("RunOne() = %t, %v; wanted configured scheduler backoff", worked, err)
	}
}

func TestWorkerServiceHonorsConcurrencyAndCancellation(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	entered := make(chan struct{}, 8)
	service, err := NewWorkerService(
		candidateSourceFunc(func(ctx context.Context) (Target, error) {
			if err := ctx.Err(); err != nil {
				return Target{}, err
			}
			return testTarget, nil
		}),
		publisherFunc(func(ctx context.Context, _ Target, _ string) (TerminalRecord, error) {
			current := active.Add(1)
			defer active.Add(-1)
			for {
				observed := maximum.Load()
				if current <= observed || maximum.CompareAndSwap(observed, current) {
					break
				}
			}
			entered <- struct{}{}
			<-ctx.Done()
			return TerminalRecord{}, ctx.Err()
		}),
		"qualified-service", 3, 100*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	for index := 0; index < 3; index++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("worker concurrency did not start")
		}
	}
	if maximum.Load() != 3 {
		t.Fatalf("maximum concurrency = %d, want 3", maximum.Load())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker service ignored cancellation")
	}
	if active.Load() != 0 {
		t.Fatalf("active publishers after cancellation = %d", active.Load())
	}
}

func TestWorkerPreservesContextCancellation(t *testing.T) {
	worker, err := NewWorker(
		candidateSourceFunc(func(context.Context) (Target, error) { return testTarget, nil }),
		publisherFunc(func(ctx context.Context, _ Target, _ string) (TerminalRecord, error) {
			<-ctx.Done()
			return TerminalRecord{}, ctx.Err()
		}),
		"qualified-worker/01",
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	worked, err := worker.RunOne(ctx)
	if !worked || !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOne() = %t, %v", worked, err)
	}
}
