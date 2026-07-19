package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type DeadlineWorkerService struct {
	worker       *DeadlineWorker
	pollInterval time.Duration
	logger       *slog.Logger
	mu           sync.RWMutex
	running      bool
	lastSuccess  time.Time
	lastError    error
}

func NewDeadlineWorkerService(
	worker *DeadlineWorker,
	pollInterval time.Duration,
	logger *slog.Logger,
) (*DeadlineWorkerService, error) {
	if worker == nil || logger == nil || pollInterval < time.Second || pollInterval > time.Minute {
		return nil, ErrDeadlineWorkerInvalid
	}
	return &DeadlineWorkerService{worker: worker, pollInterval: pollInterval, logger: logger}, nil
}

func (service *DeadlineWorkerService) Run(ctx context.Context) error {
	if service == nil || ctx == nil {
		return ErrDeadlineWorkerInvalid
	}
	service.mu.Lock()
	if service.running {
		service.mu.Unlock()
		return fmt.Errorf("%w: worker service is already running", ErrDeadlineWorkerInvalid)
	}
	service.running = true
	service.mu.Unlock()
	defer func() {
		service.mu.Lock()
		service.running = false
		service.mu.Unlock()
	}()

	for {
		processed, err := service.worker.RunOnce(ctx)
		if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && ctx.Err() != nil {
			return ctx.Err()
		}
		service.mu.Lock()
		if err == nil {
			service.lastSuccess, service.lastError = time.Now().UTC(), nil
		} else {
			service.lastError = err
		}
		service.mu.Unlock()
		if err != nil {
			service.logger.Error("SandboxSession lifecycle deadline enforcement failed", "error", err)
		}
		if processed && err == nil {
			continue
		}
		timer := time.NewTimer(service.pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (service *DeadlineWorkerService) Readiness(context.Context) error {
	if service == nil || service.worker == nil {
		return ErrDeadlineWorkerInvalid
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	if !service.running {
		return errors.New("SandboxSession lifecycle deadline worker is not running")
	}
	return nil
}
