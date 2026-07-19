package credentialset

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type Config struct {
	Audience string
	Broker   AtomicBroker
	// Clock configures local/test stores that explicitly support an injected
	// clock. Durable stores remain the sole time authority and ignore it.
	Clock           Clock
	Issuer          string
	MemberValidator MemberValidator
	MinimumLifetime time.Duration
	Signer          Signer
	Store           Store
}

type Service struct {
	audience        string
	broker          AtomicBroker
	issuer          string
	memberValidator MemberValidator
	minimumLifetime time.Duration
	signer          Signer
	store           Store
}

// NewService creates the reusable 1..64-member atomic set service. Use
// NewGoldenService for the fixed Golden v2 membership and 2-minute minimum.
func NewService(config Config) (*Service, error) {
	if !validIssuer(config.Issuer) || !validAudience(config.Audience) || isNilInterface(config.Broker) ||
		isNilInterface(config.Signer) || isNilInterface(config.Store) || (config.Clock != nil && isNilInterface(config.Clock)) {
		return nil, fmt.Errorf("%w: service trust and storage dependencies are incomplete", ErrInvalid)
	}
	if config.Clock == nil {
		config.Clock = systemClock{}
	}
	if configurable, ok := config.Store.(interface{ useLocalClock(Clock) }); ok {
		configurable.useLocalClock(config.Clock)
	}
	if config.MemberValidator == nil {
		config.MemberValidator = ValidateMembers
	}
	if config.MinimumLifetime < 0 || config.MinimumLifetime > MaximumLifetime {
		return nil, fmt.Errorf("%w: minimum lifetime is invalid", ErrInvalid)
	}
	return &Service{
		audience: config.Audience, broker: config.Broker,
		issuer: config.Issuer, memberValidator: config.MemberValidator,
		minimumLifetime: config.MinimumLifetime, signer: config.Signer, store: config.Store,
	}, nil
}

func NewGoldenService(config Config) (*Service, error) {
	config.MemberValidator = ValidateGoldenMembers
	config.MinimumLifetime = MinimumGoldenLifetime
	return NewService(config)
}

// Issue reserves durable state before each broker/signing side effect. Once a
// side effect is marked started, every subsequent invocation uses Inspect and
// never repeats PrepareSet, ActivateSet, or Sign.
func (service *Service) Issue(ctx context.Context, command IssueCommand) (IssueResult, error) {
	if service == nil {
		return IssueResult{}, fmt.Errorf("%w: service is nil", ErrInvalid)
	}
	if isNilInterface(ctx) {
		return IssueResult{}, fmt.Errorf("%w: context is nil", ErrInvalid)
	}
	now, err := service.trustedNow(ctx)
	if err != nil {
		return IssueResult{}, err
	}
	snapshot, loadErr := service.store.Load(ctx, command.SetID)
	existing := loadErr == nil
	if loadErr != nil && !errors.Is(loadErr, ErrNotFound) {
		return IssueResult{}, loadErr
	}
	enforceCurrentWindow := !existing || !issueReplayTerminal(snapshot.Phase)
	request, commandHash, err := validateIssueCommand(command, now, service.memberValidator, enforceCurrentWindow)
	if err != nil {
		return IssueResult{}, err
	}
	if request.Issuer != service.issuer || request.Audience != service.audience {
		return IssueResult{}, fmt.Errorf("%w: command issuer or audience does not match service authority", ErrInvalid)
	}
	issued, _ := parseCanonicalTime(request.IssuedAt)
	expires, _ := parseCanonicalTime(request.ExpiresAt)
	if expires.Sub(issued) < service.minimumLifetime {
		return IssueResult{}, fmt.Errorf("%w: credential set lifetime is below the configured minimum", ErrInvalid)
	}
	if !existing {
		reservation := Event{
			At: now, EventID: uuid.NewString(), ExpiresAt: request.ExpiresAt, IssueCommandHash: commandHash,
			IssuedAt: request.IssuedAt, Kind: EventIssueReserved,
			OperationID: command.OperationID,
		}
		snapshot, _, err = service.store.CreateIssue(ctx, command.SetID, reservation)
		if errors.Is(err, ErrStoreOutcomeUnknown) {
			current, reconcileErr := service.store.Load(ctx, command.SetID)
			if reconcileErr != nil {
				return IssueResult{}, ErrOutcomeUnknown
			}
			snapshot, err = current, nil
		}
		if err != nil {
			return IssueResult{}, err
		}
	}
	if snapshot.IssueOperationID != command.OperationID || snapshot.IssueCommandHash != commandHash {
		return IssueResult{}, ErrIdempotencyConflict
	}

	var delivery BrokerDeliveryHandle
	for attempts := 0; attempts < 32; attempts++ {
		switch snapshot.Phase {
		case PhaseIssueReserved:
			updated, owner, appendErr := service.start(ctx, snapshot, EventPrepareStarted, snapshot.IssueOperationID)
			if appendErr != nil {
				return IssueResult{}, appendErr
			}
			snapshot = updated
			if !owner {
				continue
			}
			observation, brokerErr := service.broker.PrepareSet(ctx, request)
			if brokerErr != nil {
				return IssueResult{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptPrepared(ctx, snapshot, request, observation)
			if err != nil {
				return IssueResult{}, err
			}
		case PhasePrepareStarted:
			observation, brokerErr := service.broker.InspectIssue(ctx, BrokerOperationRef{OperationID: request.OperationID, SetID: request.SetID})
			if brokerErr != nil {
				return IssueResult{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptPrepared(ctx, snapshot, request, observation)
			if err != nil {
				return IssueResult{}, err
			}
		case PhasePrepared:
			updated, owner, appendErr := service.start(ctx, snapshot, EventActivationStarted, snapshot.IssueOperationID)
			if appendErr != nil {
				return IssueResult{}, appendErr
			}
			snapshot = updated
			if !owner {
				continue
			}
			observation, brokerErr := service.broker.ActivateSet(ctx, BrokerOperationRef{OperationID: request.OperationID, SetID: request.SetID})
			if brokerErr != nil {
				return IssueResult{}, ErrOutcomeUnknown
			}
			snapshot, delivery, err = service.acceptActivated(ctx, snapshot, request, observation)
			if err != nil {
				return IssueResult{}, err
			}
		case PhaseActivationStarted:
			observation, brokerErr := service.broker.InspectIssue(ctx, BrokerOperationRef{OperationID: request.OperationID, SetID: request.SetID})
			if brokerErr != nil {
				return IssueResult{}, ErrOutcomeUnknown
			}
			snapshot, delivery, err = service.acceptActivated(ctx, snapshot, request, observation)
			if err != nil {
				return IssueResult{}, err
			}
		case PhaseActivated:
			operationID := signingOperationID(snapshot.IssueOperationID, "attestation")
			updated, owner, appendErr := service.start(ctx, snapshot, EventIssuanceSignStarted, operationID)
			if appendErr != nil {
				return IssueResult{}, appendErr
			}
			snapshot = updated
			if !owner {
				continue
			}
			payload, payloadErr := CanonicalIssuancePayload(*snapshot.Binding)
			if payloadErr != nil {
				return IssueResult{}, payloadErr
			}
			attestation, signErr := signAttestation(ctx, service.signer, buildSignRequest(snapshot.IssueOperationID, payload), payload, false)
			if signErr != nil {
				return IssueResult{}, signErr
			}
			snapshot, err = service.appendAttestation(ctx, snapshot, EventIssued, operationID, attestation)
			if err != nil {
				return IssueResult{}, err
			}
		case PhaseIssuanceSignStarted:
			payload, payloadErr := CanonicalIssuancePayload(*snapshot.Binding)
			if payloadErr != nil {
				return IssueResult{}, payloadErr
			}
			signRequest := buildSignRequest(snapshot.IssueOperationID, payload)
			attestation, signErr := signAttestation(ctx, service.signer, signRequest, payload, true)
			if signErr != nil {
				return IssueResult{}, signErr
			}
			// The activation capability is never persisted. If signing was
			// ambiguous after activation, recover it only through the broker's
			// read-only inspection of the same operation.
			if delivery == nil {
				delivery, err = service.inspectActiveDelivery(ctx, request, *snapshot.Binding)
				if err != nil {
					return IssueResult{}, err
				}
			}
			snapshot, err = service.appendAttestation(ctx, snapshot, EventIssued, signRequest.OperationID, attestation)
			if err != nil {
				return IssueResult{}, err
			}
		case PhaseIssued, PhaseRevocationReserved, PhaseRevocationStarted, PhaseRevoked, PhaseRevocationSignStarted, PhaseComplete, PhaseRevocationFailed:
			if snapshot.Binding == nil || snapshot.IssueAttestation == nil {
				return IssueResult{}, ErrInvalidTransition
			}
			result := IssueResult{
				Attestation: cloneAttestation(*snapshot.IssueAttestation), Binding: cloneBinding(*snapshot.Binding),
				Delivery: delivery,
			}
			if delivery == nil {
				return result, ErrDeliveryOutcomeUnknown
			}
			return result, nil
		case PhaseIssueFailed:
			return IssueResult{}, ErrBrokerRejected
		default:
			return IssueResult{}, ErrInvalidTransition
		}
	}
	return IssueResult{}, ErrOutcomeUnknown
}

func issueReplayTerminal(phase Phase) bool {
	switch phase {
	case PhaseIssued, PhaseRevocationReserved, PhaseRevocationStarted, PhaseRevoked,
		PhaseRevocationSignStarted, PhaseComplete, PhaseIssueFailed, PhaseRevocationFailed:
		return true
	default:
		return false
	}
}

func (service *Service) acceptPrepared(ctx context.Context, snapshot Snapshot, request BrokerPrepareRequest, observation BrokerIssueObservation) (Snapshot, error) {
	if observation.OperationID != request.OperationID {
		return Snapshot{}, fmt.Errorf("%w: broker preparation operation id drifted", ErrInvalid)
	}
	switch observation.Stage {
	case BrokerIssuePending:
		return Snapshot{}, ErrOutcomeUnknown
	case BrokerIssueFailed:
		return service.appendFailure(ctx, snapshot, EventIssueFailed, request.OperationID, ErrBrokerRejected)
	case BrokerIssuePrepared:
		if observation.Delivery != nil {
			return Snapshot{}, fmt.Errorf("%w: staged credentials must not be delivered before atomic activation", ErrInvalid)
		}
		if err := validateBindingAgainstRequest(observation.Binding, request, service.memberValidator); err != nil {
			return Snapshot{}, err
		}
		binding := cloneBinding(observation.Binding)
		event, err := service.newEvent(ctx, EventPrepared, request.OperationID)
		if err != nil {
			return Snapshot{}, err
		}
		event.Binding = &binding
		return service.appendEvent(ctx, snapshot, event)
	default:
		// PrepareSet/its inspection may not collapse the explicit atomic
		// activation phase into an implicit issue operation.
		return Snapshot{}, fmt.Errorf("%w: broker did not return the reserved prepared-set state", ErrInvalid)
	}
}

func (service *Service) acceptActivated(ctx context.Context, snapshot Snapshot, request BrokerPrepareRequest, observation BrokerIssueObservation) (Snapshot, BrokerDeliveryHandle, error) {
	if observation.OperationID != request.OperationID {
		return Snapshot{}, nil, fmt.Errorf("%w: broker activation operation id drifted", ErrInvalid)
	}
	switch observation.Stage {
	case BrokerIssuePending, BrokerIssuePrepared:
		return Snapshot{}, nil, ErrOutcomeUnknown
	case BrokerIssueFailed:
		updated, appendErr := service.appendFailure(ctx, snapshot, EventIssueFailed, request.OperationID, ErrBrokerRejected)
		return updated, nil, appendErr
	case BrokerIssueActive:
		if snapshot.Binding == nil || !equalBinding(*snapshot.Binding, observation.Binding) {
			return Snapshot{}, nil, fmt.Errorf("%w: activated set differs from the prepared atomic set", ErrInvalid)
		}
		if observation.Delivery == nil || observation.Delivery.CredentialSetHandleHash() != observation.Binding.SetHandleHash {
			return Snapshot{}, nil, fmt.Errorf("%w: opaque delivery handle commitment does not match the set", ErrInvalid)
		}
		binding := cloneBinding(observation.Binding)
		event, timeErr := service.newEvent(ctx, EventActivated, request.OperationID)
		if timeErr != nil {
			return Snapshot{}, nil, timeErr
		}
		event.Binding = &binding
		updated, appendErr := service.appendEvent(ctx, snapshot, event)
		return updated, observation.Delivery, appendErr
	default:
		return Snapshot{}, nil, fmt.Errorf("%w: broker activation status is invalid", ErrInvalid)
	}
}

func (service *Service) inspectActiveDelivery(ctx context.Context, request BrokerPrepareRequest, binding SetBinding) (BrokerDeliveryHandle, error) {
	observation, err := service.broker.InspectIssue(ctx, BrokerOperationRef{OperationID: request.OperationID, SetID: request.SetID})
	if err != nil {
		return nil, ErrOutcomeUnknown
	}
	if observation.OperationID != request.OperationID || observation.Stage != BrokerIssueActive ||
		!equalBinding(binding, observation.Binding) || observation.Delivery == nil ||
		observation.Delivery.CredentialSetHandleHash() != binding.SetHandleHash {
		return nil, fmt.Errorf("%w: broker inspection did not return the exact active set delivery capability", ErrInvalid)
	}
	return observation.Delivery, nil
}

// Revoke atomically revokes the exact set previously issued by this service.
// A durable revocation-start event means every retry uses InspectRevocation.
func (service *Service) Revoke(ctx context.Context, command RevokeCommand) (RevokeResult, error) {
	if service == nil {
		return RevokeResult{}, fmt.Errorf("%w: service is nil", ErrInvalid)
	}
	if isNilInterface(ctx) || !validUUIDv4(command.OperationID) || ValidateBinding(command.Binding) != nil ||
		command.Binding.Issuer != service.issuer || command.Binding.Audience != service.audience {
		return RevokeResult{}, fmt.Errorf("%w: revocation command is invalid", ErrInvalid)
	}
	snapshot, err := service.store.Load(ctx, command.Binding.SetID)
	if err != nil {
		return RevokeResult{}, err
	}
	if snapshot.Binding == nil || !equalBinding(*snapshot.Binding, command.Binding) {
		return RevokeResult{}, ErrIdempotencyConflict
	}
	if command.OperationID == snapshot.IssueOperationID {
		return RevokeResult{}, fmt.Errorf("%w: revocation operation must be distinct from issuance", ErrInvalid)
	}
	commandHash, err := hashCanonical(command)
	if err != nil {
		return RevokeResult{}, err
	}
	if snapshot.Phase == PhaseIssued {
		now, timeErr := service.trustedNow(ctx)
		if timeErr != nil {
			return RevokeResult{}, timeErr
		}
		issued, _ := parseCanonicalTime(snapshot.Binding.IssuedAt)
		expires, _ := parseCanonicalTime(snapshot.Binding.ExpiresAt)
		if !now.After(issued) || !now.Before(expires) {
			return RevokeResult{}, fmt.Errorf("%w: revocation trusted time is outside the set lifetime", ErrInvalid)
		}
		revokedAt := now.Format(canonicalTimeLayout)
		reservation := Event{
			At: now, EventID: uuid.NewString(), Kind: EventRevocationReserved, OperationID: command.OperationID,
			RevocationCommandHash: commandHash, RevokedAt: revokedAt,
		}
		snapshot, err = service.appendEvent(ctx, snapshot, reservation)
		if err != nil {
			return RevokeResult{}, err
		}
	}
	if snapshot.RevocationOperationID != command.OperationID || snapshot.RevocationCommandHash != commandHash {
		return RevokeResult{}, ErrIdempotencyConflict
	}

	for attempts := 0; attempts < 24; attempts++ {
		switch snapshot.Phase {
		case PhaseRevocationReserved:
			updated, owner, appendErr := service.start(ctx, snapshot, EventRevocationStarted, snapshot.RevocationOperationID)
			if appendErr != nil {
				return RevokeResult{}, appendErr
			}
			snapshot = updated
			if !owner {
				continue
			}
			observation, brokerErr := service.broker.RevokeSet(ctx, BrokerRevokeRequest{
				Binding: cloneBinding(*snapshot.Binding), OperationID: snapshot.RevocationOperationID, RevokedAt: snapshot.RevokedAt,
			})
			if brokerErr != nil {
				return RevokeResult{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptRevoked(ctx, snapshot, observation)
			if err != nil {
				return RevokeResult{}, err
			}
		case PhaseRevocationStarted:
			observation, brokerErr := service.broker.InspectRevocation(ctx, BrokerOperationRef{
				OperationID: snapshot.RevocationOperationID, SetID: snapshot.SetID,
			})
			if brokerErr != nil {
				return RevokeResult{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptRevoked(ctx, snapshot, observation)
			if err != nil {
				return RevokeResult{}, err
			}
		case PhaseRevoked:
			operationID := signingOperationID(snapshot.RevocationOperationID, "attestation")
			updated, owner, appendErr := service.start(ctx, snapshot, EventRevocationSignStarted, operationID)
			if appendErr != nil {
				return RevokeResult{}, appendErr
			}
			snapshot = updated
			if !owner {
				continue
			}
			payload, payloadErr := CanonicalRevocationPayload(*snapshot.Binding, snapshot.RevokedAt)
			if payloadErr != nil {
				return RevokeResult{}, payloadErr
			}
			request := buildSignRequest(snapshot.RevocationOperationID, payload)
			attestation, signErr := signAttestation(ctx, service.signer, request, payload, false)
			if signErr != nil {
				return RevokeResult{}, signErr
			}
			snapshot, err = service.appendAttestation(ctx, snapshot, EventRevocationAttested, request.OperationID, attestation)
			if err != nil {
				return RevokeResult{}, err
			}
		case PhaseRevocationSignStarted:
			payload, payloadErr := CanonicalRevocationPayload(*snapshot.Binding, snapshot.RevokedAt)
			if payloadErr != nil {
				return RevokeResult{}, payloadErr
			}
			request := buildSignRequest(snapshot.RevocationOperationID, payload)
			attestation, signErr := signAttestation(ctx, service.signer, request, payload, true)
			if signErr != nil {
				return RevokeResult{}, signErr
			}
			snapshot, err = service.appendAttestation(ctx, snapshot, EventRevocationAttested, request.OperationID, attestation)
			if err != nil {
				return RevokeResult{}, err
			}
		case PhaseComplete:
			if snapshot.Binding == nil || snapshot.RevocationAttestation == nil {
				return RevokeResult{}, ErrInvalidTransition
			}
			return RevokeResult{
				Attestation: cloneAttestation(*snapshot.RevocationAttestation), Binding: cloneBinding(*snapshot.Binding),
				RevokedAt: snapshot.RevokedAt,
			}, nil
		case PhaseRevocationFailed:
			return RevokeResult{}, ErrBrokerRejected
		default:
			return RevokeResult{}, fmt.Errorf("%w: set is not ready for revocation", ErrInvalidTransition)
		}
	}
	return RevokeResult{}, ErrOutcomeUnknown
}

func (service *Service) acceptRevoked(ctx context.Context, snapshot Snapshot, observation BrokerRevokeObservation) (Snapshot, error) {
	if observation.OperationID != snapshot.RevocationOperationID {
		return Snapshot{}, fmt.Errorf("%w: broker revocation operation id drifted", ErrInvalid)
	}
	switch observation.Stage {
	case BrokerRevokePending:
		return Snapshot{}, ErrOutcomeUnknown
	case BrokerRevokeFailed:
		return service.appendFailure(ctx, snapshot, EventRevocationFailed, snapshot.RevocationOperationID, ErrBrokerRejected)
	case BrokerRevokeDone:
		if snapshot.Binding == nil || !equalBinding(*snapshot.Binding, observation.Binding) || observation.RevokedAt != snapshot.RevokedAt {
			return Snapshot{}, fmt.Errorf("%w: broker revocation does not match the exact issued atomic set", ErrInvalid)
		}
		binding := cloneBinding(observation.Binding)
		event, err := service.newEvent(ctx, EventRevoked, snapshot.RevocationOperationID)
		if err != nil {
			return Snapshot{}, err
		}
		event.Binding = &binding
		event.RevokedAt = snapshot.RevokedAt
		return service.appendEvent(ctx, snapshot, event)
	default:
		return Snapshot{}, fmt.Errorf("%w: broker revocation status is invalid", ErrInvalid)
	}
}

func (service *Service) start(ctx context.Context, snapshot Snapshot, kind EventKind, operationID string) (Snapshot, bool, error) {
	event, err := service.newEvent(ctx, kind, operationID)
	if err != nil {
		return Snapshot{}, false, err
	}
	updated, err := service.store.Append(ctx, snapshot.SetID, snapshot.Version, event)
	if errors.Is(err, ErrCASConflict) {
		current, loadErr := service.store.Load(ctx, snapshot.SetID)
		return current, false, loadErr
	}
	if errors.Is(err, ErrStoreOutcomeUnknown) {
		current, loadErr := service.store.Load(ctx, snapshot.SetID)
		if loadErr != nil {
			return Snapshot{}, false, ErrOutcomeUnknown
		}
		if current.LastEventID == event.EventID {
			return current, true, nil
		}
		if current.Version > snapshot.Version {
			return current, false, nil
		}
		return Snapshot{}, false, ErrOutcomeUnknown
	}
	return updated, err == nil, err
}

func (service *Service) appendAttestation(ctx context.Context, snapshot Snapshot, kind EventKind, operationID string, attestation Attestation) (Snapshot, error) {
	copy := cloneAttestation(attestation)
	event, err := service.newEvent(ctx, kind, operationID)
	if err != nil {
		return Snapshot{}, err
	}
	event.Attestation = &copy
	return service.appendEvent(ctx, snapshot, event)
}

func (service *Service) appendFailure(ctx context.Context, snapshot Snapshot, kind EventKind, operationID string, returned error) (Snapshot, error) {
	event, err := service.newEvent(ctx, kind, operationID)
	if err != nil {
		return Snapshot{}, err
	}
	updated, err := service.appendEvent(ctx, snapshot, event)
	if err != nil {
		return Snapshot{}, err
	}
	return updated, returned
}

func (service *Service) newEvent(ctx context.Context, kind EventKind, operationID string) (Event, error) {
	at, err := service.trustedNow(ctx)
	if err != nil {
		return Event{}, err
	}
	return Event{At: at, EventID: uuid.NewString(), Kind: kind, OperationID: operationID}, nil
}

// appendEvent reconciles an ambiguous Store response with a strongly
// consistent Load. The event ID proves this exact append committed. If it
// cannot be observed, the service returns unknown and lets the next invocation
// inspect the already-started external operation; it never repeats a mutation.
func (service *Service) appendEvent(ctx context.Context, snapshot Snapshot, event Event) (Snapshot, error) {
	updated, err := service.store.Append(ctx, snapshot.SetID, snapshot.Version, event)
	if errors.Is(err, ErrCASConflict) {
		return service.store.Load(ctx, snapshot.SetID)
	}
	if !errors.Is(err, ErrStoreOutcomeUnknown) {
		return updated, err
	}
	current, loadErr := service.store.Load(ctx, snapshot.SetID)
	if loadErr != nil || current.LastEventID != event.EventID {
		return Snapshot{}, ErrOutcomeUnknown
	}
	return current, nil
}

func (service *Service) trustedNow(ctx context.Context) (time.Time, error) {
	now, err := service.store.TrustedTime(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if now.IsZero() {
		return time.Time{}, fmt.Errorf("%w: trusted clock returned zero", ErrInvalid)
	}
	return now.UTC().Truncate(time.Millisecond), nil
}
