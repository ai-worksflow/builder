package qualificationrelease

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const commitUnknownInspectionTimeout = 5 * time.Second

type AtomicStore interface {
	Resolve(context.Context, Target) (Authorization, error)
	Claim(context.Context, Authorization, uuid.UUID, string, time.Duration) (Claim, error)
	InspectClaim(context.Context, Authorization, uuid.UUID) (Claim, error)
	Renew(context.Context, Authorization, Claim, time.Time, time.Duration) (Claim, error)
	Start(context.Context, Authorization, Claim) (ControllerBinding, error)
	InspectController(context.Context, Authorization) (ControllerBinding, error)
	RecordHealthy(context.Context, Authorization) (TerminalRecord, error)
	InspectHealthy(context.Context, Authorization) (TerminalRecord, error)
	RecordFailure(context.Context, Authorization) (TerminalRecord, error)
	InspectFailure(context.Context, Authorization) (TerminalRecord, error)
	ApplyHealthy(context.Context, Authorization, Claim) (TerminalRecord, error)
	ApplyFailure(context.Context, Authorization, Claim) (TerminalRecord, error)
}

type ControllerObserver interface {
	Observe(context.Context, ControllerBinding) (ControllerOutcome, error)
}

type PublisherConfig struct {
	LeaseDuration     time.Duration
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	IDs               func() uuid.UUID
}

// QualifiedReleaseControllerPublisher owns the dedicated profile-v3 Publish
// continuation. Its only caller-controlled identity is the Workflow target;
// every release, equivalence, actor, receipt and Controller fact is resolved
// from migration-84 authority.
type QualifiedReleaseControllerPublisher struct {
	store             AtomicStore
	observer          ControllerObserver
	leaseDuration     time.Duration
	pollInterval      time.Duration
	heartbeatInterval time.Duration
	ids               func() uuid.UUID
}

func NewQualifiedReleaseControllerPublisher(
	store AtomicStore,
	observer ControllerObserver,
	config PublisherConfig,
) (*QualifiedReleaseControllerPublisher, error) {
	heartbeatInterval := config.HeartbeatInterval
	if heartbeatInterval == 0 {
		heartbeatInterval = config.LeaseDuration / 3
	}
	if isNilInterface(store) || isNilInterface(observer) ||
		config.LeaseDuration < 30*time.Second || config.LeaseDuration > 4*time.Minute ||
		config.LeaseDuration%time.Millisecond != 0 ||
		config.PollInterval < 100*time.Millisecond ||
		config.PollInterval > config.LeaseDuration/4 ||
		heartbeatInterval < 100*time.Millisecond ||
		heartbeatInterval > config.LeaseDuration/3 {
		return nil, ErrInvalid
	}
	ids := config.IDs
	if ids == nil {
		ids = uuid.New
	}
	return &QualifiedReleaseControllerPublisher{
		store: store, observer: observer, leaseDuration: config.LeaseDuration,
		pollInterval: config.PollInterval, heartbeatInterval: heartbeatInterval, ids: ids,
	}, nil
}

func (publisher *QualifiedReleaseControllerPublisher) Publish(
	ctx context.Context,
	target Target,
	leaseOwner string,
) (TerminalRecord, error) {
	if publisher == nil || isNilInterface(publisher.store) || isNilInterface(publisher.observer) ||
		isNilInterface(ctx) || target.Validate() != nil || !boundedText(leaseOwner, 200) ||
		strings.TrimSpace(leaseOwner) != leaseOwner {
		return TerminalRecord{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return TerminalRecord{}, err
	}
	authorization, err := publisher.store.Resolve(ctx, target)
	if err != nil {
		return TerminalRecord{}, sanitizePublisherError(err)
	}
	if err := authorization.ValidateFor(target); err != nil {
		return TerminalRecord{}, ErrConflict
	}

	// This identity is allocated exactly once for this command. Every unknown
	// outcome below inspects or replays this same UUID before returning.
	claimEventID := publisher.ids()
	if !validUUIDv4(claimEventID) {
		return TerminalRecord{}, ErrConflict
	}
	claim, err := publisher.store.Claim(
		ctx, authorization, claimEventID, leaseOwner, publisher.leaseDuration,
	)
	if errors.Is(err, ErrStoreOutcomeUnknown) {
		claim, err = publisher.reconcileUnknownClaim(
			ctx, authorization, claimEventID, leaseOwner,
		)
	}
	if err != nil {
		return TerminalRecord{}, sanitizePublisherError(err)
	}
	if err := claim.ValidateFor(authorization); err != nil {
		return TerminalRecord{}, ErrConflict
	}
	if !claim.Active {
		return TerminalRecord{}, ErrLeaseLost
	}

	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	heartbeat := newLeaseHeartbeat(publisher, authorization, claim)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		heartbeat.run(heartbeatCtx)
	}()
	defer func() {
		stopHeartbeat()
		<-heartbeatDone
	}()

	binding, err := publisher.store.Start(ctx, authorization, claim)
	if errors.Is(err, ErrStoreOutcomeUnknown) {
		binding, err = publisher.reconcileUnknownController(ctx, authorization, claim)
	}
	if err != nil {
		return TerminalRecord{}, sanitizePublisherError(err)
	}
	if err := binding.ValidateFor(authorization); err != nil {
		return TerminalRecord{}, ErrConflict
	}

	ticker := time.NewTicker(publisher.pollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return TerminalRecord{}, err
		}
		if heartbeatErr := heartbeat.err(); heartbeatErr != nil {
			return TerminalRecord{}, sanitizePublisherError(heartbeatErr)
		}
		outcome, err := publisher.observer.Observe(ctx, binding)
		if err != nil {
			return TerminalRecord{}, sanitizePublisherError(err)
		}
		if err := outcome.Validate(); err != nil {
			return TerminalRecord{}, ErrConflict
		}
		switch outcome.Kind {
		case OutcomeHealthy:
			record, terminalErr := publisher.recordAndApply(
				ctx, authorization, heartbeat.claim(), true,
			)
			if errors.Is(terminalErr, ErrNotReady) {
				break
			}
			return record, terminalErr
		case OutcomeFailed:
			record, terminalErr := publisher.recordAndApply(
				ctx, authorization, heartbeat.claim(), false,
			)
			if errors.Is(terminalErr, ErrNotReady) {
				break
			}
			return record, terminalErr
		case OutcomeActive:
		default:
			return TerminalRecord{}, ErrConflict
		}
		select {
		case <-ctx.Done():
			return TerminalRecord{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (publisher *QualifiedReleaseControllerPublisher) renew(
	ctx context.Context,
	authorization Authorization,
	claim Claim,
) (Claim, error) {
	if !claim.Active {
		return Claim{}, ErrLeaseLost
	}
	renewed, err := publisher.store.Renew(
		ctx, authorization, claim, claim.LeaseExpiresAt, publisher.leaseDuration,
	)
	if errors.Is(err, ErrStoreOutcomeUnknown) {
		inspect, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), commitUnknownInspectionTimeout,
		)
		defer cancel()
		renewed, err = publisher.store.InspectClaim(
			inspect, authorization, claim.ClaimEventID,
		)
		if err == nil && (!renewed.Active ||
			!renewed.LeaseExpiresAt.After(claim.LeaseExpiresAt)) {
			return Claim{}, ErrOutcomeUnknown
		}
	}
	if err != nil {
		return Claim{}, err
	}
	if renewed.Owner != claim.Owner || renewed.Attempt != claim.Attempt ||
		renewed.ClaimEventID != claim.ClaimEventID || !renewed.Active ||
		!renewed.LeaseExpiresAt.After(claim.LeaseExpiresAt) {
		return Claim{}, ErrConflict
	}
	return renewed, nil
}

type leaseHeartbeat struct {
	publisher     *QualifiedReleaseControllerPublisher
	authorization Authorization

	mu      sync.RWMutex
	current Claim
	failure error
}

func newLeaseHeartbeat(
	publisher *QualifiedReleaseControllerPublisher,
	authorization Authorization,
	claim Claim,
) *leaseHeartbeat {
	return &leaseHeartbeat{
		publisher: publisher, authorization: authorization, current: claim,
	}
}

func (heartbeat *leaseHeartbeat) run(ctx context.Context) {
	// The schedule is monotonic elapsed time. The absolute new expiry is
	// generated inside PostgreSQL from clock_timestamp(), so application-host
	// clock skew cannot shorten, skip, or over-extend the Workflow lease.
	ticker := time.NewTicker(heartbeat.publisher.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		current := heartbeat.claim()
		renewed, err := heartbeat.publisher.renew(ctx, heartbeat.authorization, current)
		heartbeat.mu.Lock()
		if err != nil {
			heartbeat.failure = err
			heartbeat.mu.Unlock()
			return
		}
		heartbeat.current = renewed
		heartbeat.mu.Unlock()
	}
}

func (heartbeat *leaseHeartbeat) claim() Claim {
	heartbeat.mu.RLock()
	defer heartbeat.mu.RUnlock()
	return heartbeat.current
}

func (heartbeat *leaseHeartbeat) err() error {
	heartbeat.mu.RLock()
	defer heartbeat.mu.RUnlock()
	return heartbeat.failure
}

func (publisher *QualifiedReleaseControllerPublisher) recordAndApply(
	ctx context.Context,
	authorization Authorization,
	claim Claim,
	healthy bool,
) (TerminalRecord, error) {
	record, err := publisher.record(ctx, authorization, healthy)
	if errors.Is(err, ErrStoreOutcomeUnknown) {
		record, err = publisher.reconcileUnknownTerminal(ctx, authorization, healthy)
	}
	if err != nil {
		return TerminalRecord{}, sanitizePublisherError(err)
	}
	if err := record.ValidateFor(authorization); err != nil {
		return TerminalRecord{}, ErrConflict
	}

	var applied TerminalRecord
	if healthy {
		applied, err = publisher.store.ApplyHealthy(ctx, authorization, claim)
	} else {
		applied, err = publisher.store.ApplyFailure(ctx, authorization, claim)
	}
	if errors.Is(err, ErrStoreOutcomeUnknown) {
		// Migration 84 explicitly defines Apply as a same-authorization,
		// same-lease exact replay after a lost completion response. No new event,
		// operation or result identity is allocated by this replay.
		reconcile, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), commitUnknownInspectionTimeout,
		)
		defer cancel()
		if healthy {
			applied, err = publisher.store.ApplyHealthy(reconcile, authorization, claim)
		} else {
			applied, err = publisher.store.ApplyFailure(reconcile, authorization, claim)
		}
	}
	if err != nil {
		return TerminalRecord{}, sanitizePublisherError(err)
	}
	if err := applied.ValidateFor(authorization); err != nil ||
		applied.ResultHash != record.ResultHash || applied.Outcome != record.Outcome {
		return TerminalRecord{}, ErrConflict
	}
	return applied, nil
}

func (publisher *QualifiedReleaseControllerPublisher) reconcileUnknownClaim(
	ctx context.Context,
	authorization Authorization,
	claimEventID uuid.UUID,
	leaseOwner string,
) (Claim, error) {
	reconcile, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), commitUnknownInspectionTimeout,
	)
	defer cancel()
	claim, err := publisher.store.InspectClaim(reconcile, authorization, claimEventID)
	if err == nil {
		return claim, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Claim{}, ErrOutcomeUnknown
	}
	claim, err = publisher.store.Claim(
		reconcile, authorization, claimEventID, leaseOwner, publisher.leaseDuration,
	)
	if !errors.Is(err, ErrStoreOutcomeUnknown) {
		return claim, err
	}
	claim, err = publisher.store.InspectClaim(reconcile, authorization, claimEventID)
	if err != nil {
		return Claim{}, ErrOutcomeUnknown
	}
	return claim, nil
}

func (publisher *QualifiedReleaseControllerPublisher) reconcileUnknownController(
	ctx context.Context,
	authorization Authorization,
	claim Claim,
) (ControllerBinding, error) {
	reconcile, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), commitUnknownInspectionTimeout,
	)
	defer cancel()
	binding, err := publisher.store.InspectController(reconcile, authorization)
	if err == nil {
		return binding, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return ControllerBinding{}, ErrOutcomeUnknown
	}
	binding, err = publisher.store.Start(reconcile, authorization, claim)
	if !errors.Is(err, ErrStoreOutcomeUnknown) {
		return binding, err
	}
	binding, err = publisher.store.InspectController(reconcile, authorization)
	if err != nil {
		return ControllerBinding{}, ErrOutcomeUnknown
	}
	return binding, nil
}

func (publisher *QualifiedReleaseControllerPublisher) record(
	ctx context.Context,
	authorization Authorization,
	healthy bool,
) (TerminalRecord, error) {
	if healthy {
		return publisher.store.RecordHealthy(ctx, authorization)
	}
	return publisher.store.RecordFailure(ctx, authorization)
}

func (publisher *QualifiedReleaseControllerPublisher) inspectTerminal(
	ctx context.Context,
	authorization Authorization,
	healthy bool,
) (TerminalRecord, error) {
	if healthy {
		return publisher.store.InspectHealthy(ctx, authorization)
	}
	return publisher.store.InspectFailure(ctx, authorization)
}

func (publisher *QualifiedReleaseControllerPublisher) reconcileUnknownTerminal(
	ctx context.Context,
	authorization Authorization,
	healthy bool,
) (TerminalRecord, error) {
	reconcile, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), commitUnknownInspectionTimeout,
	)
	defer cancel()
	record, err := publisher.inspectTerminal(reconcile, authorization, healthy)
	if err == nil {
		return record, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return TerminalRecord{}, ErrOutcomeUnknown
	}
	record, err = publisher.record(reconcile, authorization, healthy)
	if !errors.Is(err, ErrStoreOutcomeUnknown) {
		return record, err
	}
	record, err = publisher.inspectTerminal(reconcile, authorization, healthy)
	if err != nil {
		return TerminalRecord{}, ErrOutcomeUnknown
	}
	return record, nil
}

func sanitizePublisherError(err error) error {
	if err == nil {
		return nil
	}
	for _, class := range []error{
		ErrInvalid, ErrNotFound, ErrNotReady, ErrConflict, ErrRetryable,
		ErrOutcomeUnknown, ErrLeaseLost,
	} {
		if errors.Is(err, class) {
			return class
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w", ErrOutcomeUnknown)
}
