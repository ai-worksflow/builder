package qualificationpromotion

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
)

type Service struct {
	verifier  Verifier
	authority ExpectationAuthority
	store     store
}

func NewService(verifier Verifier, authority ExpectationAuthority, persistence store) (*Service, error) {
	if verifier == nil || authority == nil || persistence == nil {
		return nil, fmt.Errorf("%w: trusted verifier, expectation authority, and atomic persistence are required", ErrInvalid)
	}
	return &Service{verifier: verifier, authority: authority, store: persistence}, nil
}

// Consume invokes the trusted verifier, compares its complete result with the
// current server-owned target, and atomically appends the ledger and pending
// handoff. A commit-unknown result is reconstructed by the same operation ID;
// it is never retried under a new identity.
func (service *Service) Consume(ctx context.Context, command ConsumeCommand) (ConsumptionRecord, error) {
	if service == nil || service.verifier == nil || service.authority == nil || service.store == nil || ctx == nil ||
		command.OperationID.Version() != 4 || command.QualificationAuthorityID.Version() != 4 ||
		command.HandoffID.Version() != 4 || command.OutputRevisionID.Version() != 4 {
		return ConsumptionRecord{}, fmt.Errorf("%w: operation, qualification-authority, handoff, and revision IDs are required", ErrInvalid)
	}
	// Operation identity is sufficient to reconstruct a response after commit.
	// This read occurs before authority resolution or verification so an exact
	// replay remains available after authority expiry or evidence retirement.
	existing, inspectErr := service.store.inspectOperation(ctx, command.OperationID)
	if inspectErr == nil {
		if existing.QualificationAuthorityID != command.QualificationAuthorityID ||
			existing.Handoff.HandoffID != command.HandoffID ||
			existing.Handoff.OutputRevisionID != command.OutputRevisionID {
			return ConsumptionRecord{}, fmt.Errorf("%w: operation ID is bound to different authority or handoff bytes", ErrConflict)
		}
		existing.Idempotent = true
		return existing, nil
	}
	if !errors.Is(inspectErr, ErrNotFound) {
		return ConsumptionRecord{}, fmt.Errorf("inspect qualification promotion operation before append: %w", inspectErr)
	}
	resolution, err := service.authority.Resolve(ctx, command.QualificationAuthorityID)
	if err != nil {
		return ConsumptionRecord{}, fmt.Errorf("resolve server-owned qualification authority: %w", err)
	}
	if validateTarget(resolution.Target) != nil || !sameTarget(resolution.Target, resolution.Verification.Expected.PromotionTarget) {
		return ConsumptionRecord{}, fmt.Errorf("%w: expectation authority returned an inconsistent workflow target", ErrInvalid)
	}
	verified, err := service.verifier.Verify(
		resolution.Verification.ReceiptPath,
		resolution.Verification.IndexPath,
		resolution.Verification.ArtifactRoot,
		resolution.Verification.Expected,
	)
	if err != nil {
		return ConsumptionRecord{}, fmt.Errorf("verify external qualification before consumption: %w", err)
	}
	if !sameTarget(verified.PromotionTarget, resolution.Target) {
		return ConsumptionRecord{}, fmt.Errorf("%w: verifier output targets a different workflow node or immutable revision", ErrInvalid)
	}
	if err := validateVerifiedPromotionShape(verified); err != nil {
		return ConsumptionRecord{}, err
	}
	record, err := buildConsumptionRecord(command, verified)
	if err != nil {
		return ConsumptionRecord{}, err
	}
	stored, err := service.store.append(ctx, appendCommand{record: record})
	if errors.Is(err, ErrOutcomeUnknown) {
		stored, err = service.store.inspectOperation(ctx, command.OperationID)
		if err != nil {
			return ConsumptionRecord{}, ErrOutcomeUnknown
		}
		if !sameImmutableRecord(stored, record) {
			return ConsumptionRecord{}, fmt.Errorf("%w: uncertain operation resolved to different immutable bytes", ErrConflict)
		}
		stored.Idempotent = true
		return stored, nil
	}
	if err != nil {
		return ConsumptionRecord{}, err
	}
	if !sameImmutableRecord(stored, record) {
		return ConsumptionRecord{}, fmt.Errorf("%w: store returned different immutable consumption bytes", ErrConflict)
	}
	return stored, nil
}

func (service *Service) InspectOperation(ctx context.Context, operationID uuid.UUID) (ConsumptionRecord, error) {
	if service == nil || service.store == nil || ctx == nil || operationID.Version() != 4 {
		return ConsumptionRecord{}, ErrNotFound
	}
	return service.store.inspectOperation(ctx, operationID)
}

func (service *Service) InspectKey(ctx context.Context, key ConsumptionKey) (ConsumptionRecord, error) {
	if service == nil || service.store == nil || ctx == nil || validateTarget(key.Target) != nil ||
		!validUUIDv4(key.AuthorityNonce) || !validDigest(key.PromotionAuthorityDigest) {
		return ConsumptionRecord{}, ErrNotFound
	}
	return service.store.inspectKey(ctx, key)
}

func buildConsumptionRecord(command ConsumeCommand, verified qualificationreceipt.VerifiedPromotion) (ConsumptionRecord, error) {
	verifiedBytes, err := canonicalJSON(verified)
	if err != nil {
		return ConsumptionRecord{}, fmt.Errorf("%w: canonicalize verified promotion: %v", ErrInvalid, err)
	}
	verifiedHash := sha256Digest(verifiedBytes)
	targetHash, err := targetDigest(verified.PromotionTarget)
	if err != nil {
		return ConsumptionRecord{}, err
	}
	intent := RevisionIntent{
		AuthorityNonce:           verified.AuthorityNonce,
		HandoffID:                command.HandoffID.String(),
		OutputRevisionID:         command.OutputRevisionID.String(),
		PromotionAuthorityDigest: verified.PromotionAuthorityDigest,
		RevisionKind:             RevisionIntentKindV1,
		SchemaVersion:            RevisionIntentSchemaV1,
		SourceTarget:             verified.PromotionTarget,
		VerifiedPromotionHash:    verifiedHash,
	}
	intentBytes, err := canonicalJSON(intent)
	if err != nil {
		return ConsumptionRecord{}, fmt.Errorf("%w: canonicalize immutable revision intent: %v", ErrInvalid, err)
	}
	intentDigest := sha256Digest(intentBytes)
	request := ConsumeRequest{
		HandoffID:                command.HandoffID.String(),
		OperationID:              command.OperationID.String(),
		OutputRevisionID:         command.OutputRevisionID.String(),
		QualificationAuthorityID: command.QualificationAuthorityID.String(),
		RevisionIntentDigest:     intentDigest,
		SchemaVersion:            RequestSchemaV1,
		TargetDigest:             targetHash,
		VerifiedPromotionHash:    verifiedHash,
	}
	requestBytes, err := canonicalJSON(request)
	if err != nil {
		return ConsumptionRecord{}, fmt.Errorf("%w: canonicalize consume request: %v", ErrInvalid, err)
	}
	return ConsumptionRecord{
		OperationID:              command.OperationID,
		QualificationAuthorityID: command.QualificationAuthorityID,
		RequestHash:              sha256Digest(requestBytes),
		RequestBytes:             requestBytes,
		Request:                  request,
		TargetDigest:             targetHash,
		VerifiedPromotionHash:    verifiedHash,
		VerifiedPromotionBytes:   verifiedBytes,
		VerifiedPromotion:        verified,
		Handoff: HandoffRecord{
			HandoffID:                command.HandoffID,
			OperationID:              command.OperationID,
			State:                    HandoffStatePending,
			Target:                   verified.PromotionTarget,
			OutputRevisionID:         command.OutputRevisionID,
			RevisionKind:             RevisionIntentKindV1,
			RevisionIntentDigest:     intentDigest,
			RevisionIntentBytes:      intentBytes,
			RevisionIntent:           intent,
			AuthorityNonce:           verified.AuthorityNonce,
			PromotionAuthorityDigest: verified.PromotionAuthorityDigest,
			VerifiedPromotionHash:    verifiedHash,
		},
	}, nil
}
