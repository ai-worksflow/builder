package workflow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
)

type MapRegistry struct {
	mu      sync.RWMutex
	runners map[domain.WorkflowNodeType]WorkerRunner
}

func NewMapRegistry() *MapRegistry {
	return &MapRegistry{runners: map[domain.WorkflowNodeType]WorkerRunner{}}
}

func (r *MapRegistry) Register(nodeType domain.WorkflowNodeType, runner WorkerRunner) error {
	if runner == nil {
		return fmt.Errorf("runner is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.runners[nodeType]; exists {
		return fmt.Errorf("runner already registered for %s", nodeType)
	}
	r.runners[nodeType] = runner
	return nil
}

func (r *MapRegistry) RunnerFor(nodeType domain.WorkflowNodeType) (WorkerRunner, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	runner, exists := r.runners[nodeType]
	return runner, exists
}

type RunnerFunc func(context.Context, Execution) (WorkerResult, error)

func (f RunnerFunc) Run(ctx context.Context, execution Execution) (WorkerResult, error) {
	return f(ctx, execution)
}

type AIProposalRunner struct {
	Freezer    ManifestFreezer
	Dispatcher ProposalDispatcher
}

func (r AIProposalRunner) Run(ctx context.Context, execution Execution) (WorkerResult, error) {
	if r.Freezer == nil || r.Dispatcher == nil {
		return WorkerResult{}, fmt.Errorf("AI proposal runner requires freezer and dispatcher")
	}
	manifest, err := r.Freezer.Freeze(ctx, execution)
	if err != nil {
		return WorkerResult{}, err
	}
	if err := manifest.Validate(); err != nil {
		return WorkerResult{}, err
	}
	proposal, err := r.Dispatcher.Dispatch(ctx, execution, manifest)
	if err != nil {
		return WorkerResult{}, err
	}
	disposition := ResultWaitInput
	if proposal != nil {
		disposition = ResultComplete
	}
	return WorkerResult{Disposition: disposition, Manifest: &manifest, Proposal: proposal}, nil
}

type ManifestCompilerRunner struct{ Hook BuildManifestHook }

func (r ManifestCompilerRunner) Run(ctx context.Context, execution Execution) (WorkerResult, error) {
	if r.Hook == nil {
		return WorkerResult{}, fmt.Errorf("build manifest hook is required")
	}
	manifest, err := r.Hook.Compile(ctx, execution)
	if err != nil {
		return WorkerResult{}, err
	}
	if err := manifest.Freeze(); err != nil {
		return WorkerResult{}, err
	}
	return WorkerResult{Disposition: ResultComplete, BuildManifest: &manifest}, nil
}

type ConditionEvaluatorFunc func(context.Context, Execution, []domain.ConditionBranch) (string, error)

func (f ConditionEvaluatorFunc) Evaluate(ctx context.Context, execution Execution, branches []domain.ConditionBranch) (string, error) {
	return f(ctx, execution, branches)
}

// Worker renews a lease while a runner executes. An expired/lost lease cancels
// the runner context, ensuring a recovered worker is the only committer.
type Worker struct {
	Engine    *Engine
	WorkerID  string
	Heartbeat time.Duration
}

func (w Worker) RunOnce(ctx context.Context) error {
	if w.Engine == nil || w.Engine.Store == nil || w.WorkerID == "" {
		return fmt.Errorf("engine and worker ID are required")
	}
	leaseDuration := w.Engine.leaseDuration()
	heartbeat := w.Heartbeat
	if heartbeat <= 0 || heartbeat >= leaseDuration {
		heartbeat = leaseDuration / 3
	}
	lease, err := w.Engine.Store.ClaimRunnable(ctx, w.WorkerID, w.Engine.now(), leaseDuration)
	if err != nil {
		return err
	}
	executionContext, cancel := context.WithCancel(ctx)
	defer cancel()
	renewErrors := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-executionContext.Done():
				return
			case <-ticker.C:
				if _, err := w.Engine.Store.RenewLease(executionContext, lease, w.Engine.now(), leaseDuration); err != nil {
					select {
					case renewErrors <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()
	executeErr := w.Engine.ExecuteLease(executionContext, lease)
	close(done)
	select {
	case renewErr := <-renewErrors:
		if executeErr == nil || executeErr == context.Canceled {
			return renewErr
		}
	default:
	}
	return executeErr
}
