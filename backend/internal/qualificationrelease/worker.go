package qualificationrelease

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type CandidateSource interface {
	Next(context.Context) (Target, error)
}

type Publisher interface {
	Publish(context.Context, Target, string) (TerminalRecord, error)
}

type Worker struct {
	source    CandidateSource
	publisher Publisher
	owner     string
}

func NewWorker(source CandidateSource, publisher Publisher, owner string) (*Worker, error) {
	if isNilInterface(source) || isNilInterface(publisher) || !boundedText(owner, 200) {
		return nil, ErrInvalid
	}
	return &Worker{source: source, publisher: publisher, owner: owner}, nil
}

func (worker *Worker) RunOne(ctx context.Context) (bool, error) {
	if worker == nil || isNilInterface(worker.source) || isNilInterface(worker.publisher) ||
		isNilInterface(ctx) {
		return false, ErrInvalid
	}
	target, err := worker.source.Next(ctx)
	if errors.Is(err, ErrNotReady) || errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, sanitizePublisherError(err)
	}
	_, err = worker.publisher.Publish(ctx, target, worker.owner)
	if errors.Is(err, ErrNotReady) || errors.Is(err, ErrLeaseLost) {
		// Another replica won the claim, or this epoch expired while ownership
		// was being reconciled. Neither is a terminal Workflow failure. Report
		// no work so the scheduler observes its configured backoff instead of
		// hot-looping the still-visible ready target.
		return false, nil
	}
	return true, sanitizePublisherError(err)
}

type WorkerService struct {
	workers      []*Worker
	pollInterval time.Duration
	onError      func(error)
}

func NewWorkerService(
	source CandidateSource,
	publisher Publisher,
	workerID string,
	concurrency int,
	pollInterval time.Duration,
) (*WorkerService, error) {
	workerID = strings.TrimSpace(workerID)
	if isNilInterface(source) || isNilInterface(publisher) ||
		!boundedText(workerID, 160) || concurrency < 1 || concurrency > 64 ||
		pollInterval < 100*time.Millisecond || pollInterval > time.Minute {
		return nil, ErrInvalid
	}
	workers := make([]*Worker, 0, concurrency)
	for index := 0; index < concurrency; index++ {
		owner := fmt.Sprintf("%s/%02d", workerID, index+1)
		worker, err := NewWorker(source, publisher, owner)
		if err != nil {
			return nil, err
		}
		workers = append(workers, worker)
	}
	return &WorkerService{workers: workers, pollInterval: pollInterval}, nil
}

func (service *WorkerService) SetErrorHandler(handler func(error)) {
	if service != nil {
		service.onError = handler
	}
}

func (service *WorkerService) Run(ctx context.Context) error {
	if service == nil || len(service.workers) == 0 || isNilInterface(ctx) {
		return ErrInvalid
	}
	var wait sync.WaitGroup
	wait.Add(len(service.workers))
	for _, worker := range service.workers {
		worker := worker
		go func() {
			defer wait.Done()
			service.runWorker(ctx, worker)
		}()
	}
	<-ctx.Done()
	wait.Wait()
	return nil
}

func (service *WorkerService) runWorker(ctx context.Context, worker *Worker) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		worked, err := worker.RunOne(ctx)
		if err != nil && !errors.Is(err, context.Canceled) &&
			!errors.Is(err, context.DeadlineExceeded) && service.onError != nil {
			service.onError(err)
		}
		if ctx.Err() != nil {
			return
		}
		if worked && err == nil {
			timer.Reset(0)
		} else {
			timer.Reset(service.pollInterval)
		}
	}
}

var _ Publisher = (*QualifiedReleaseControllerPublisher)(nil)
