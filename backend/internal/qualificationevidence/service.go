package qualificationevidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type Config struct {
	Store         Store
	Plans         PlanAuthority
	Credentials   CredentialSetAuthority
	Capture       RunCaptureAuthority
	Encryptor     ArtifactEncryptor
	KMS           KMSAuthority
	Indexer       ArtifactIndexer
	Receipt       ReceiptAuthority
	Sealer        SnapshotSealer
	Verifier      SnapshotVerifier
	TrustBindings TrustBindings
}

type Service struct {
	store       Store
	plans       PlanAuthority
	credentials CredentialSetAuthority
	capture     RunCaptureAuthority
	encryptor   ArtifactEncryptor
	kms         KMSAuthority
	indexer     ArtifactIndexer
	receipt     ReceiptAuthority
	sealer      SnapshotSealer
	verifier    SnapshotVerifier
	trust       TrustBindings
	trustDigest string
}

func NewService(config Config) (*Service, error) {
	if isNilInterface(config.Store) || isNilInterface(config.Plans) || isNilInterface(config.Credentials) || isNilInterface(config.Capture) ||
		isNilInterface(config.Encryptor) || isNilInterface(config.KMS) || isNilInterface(config.Indexer) ||
		isNilInterface(config.Receipt) || isNilInterface(config.Sealer) || isNilInterface(config.Verifier) {
		return nil, fmt.Errorf("%w: trusted orchestration dependencies are incomplete", ErrInvalid)
	}
	identities := []string{
		config.TrustBindings.CaptureAuthorityID, config.TrustBindings.EncryptionAuthorityID,
		config.TrustBindings.CredentialAuthorityID,
		config.TrustBindings.IndexerAuthorityID, config.TrustBindings.KMSAuthorityID,
		config.TrustBindings.ReceiptAuthorityID, config.TrustBindings.SealerAuthorityID,
		config.TrustBindings.VerifierAuthorityID,
	}
	seen := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		if !validIdentity(identity) {
			return nil, fmt.Errorf("%w: trusted authority identity is invalid", ErrInvalid)
		}
		if _, duplicate := seen[identity]; duplicate {
			return nil, fmt.Errorf("%w: trusted orchestration roles must use distinct identities", ErrInvalid)
		}
		seen[identity] = struct{}{}
	}
	trustDigest, err := CanonicalDigest(config.TrustBindings)
	if err != nil {
		return nil, err
	}
	return &Service{
		store: config.Store, plans: config.Plans, credentials: config.Credentials, capture: config.Capture,
		encryptor: config.Encryptor, kms: config.KMS, indexer: config.Indexer,
		receipt: config.Receipt, sealer: config.Sealer, verifier: config.Verifier,
		trust: config.TrustBindings, trustDigest: trustDigest,
	}, nil
}

// Execute resolves an opaque, server-owned plan authority and advances its
// exact evidence plan until an immutable snapshot has been independently
// verified. A caller cannot supply or alter Plan material at this boundary.
//
// The resolved v1 evidence plan authorizes only this internal lifecycle. It is
// not, by itself, authority to promote or submit a workflow target.
func (service *Service) Execute(ctx context.Context, authorityID string) (Result, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.plans) {
		return Result{}, fmt.Errorf("%w: service, context, or plan authority is nil", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !validUUIDv4(authorityID) {
		return Result{}, fmt.Errorf("%w: plan authority ID must be a canonical UUIDv4", ErrInvalid)
	}
	resolution, err := service.plans.Resolve(ctx, authorityID)
	if err != nil {
		return Result{}, fmt.Errorf("resolve qualification plan authority: %w", err)
	}
	plan, err := service.validatePlanAuthorityResolution(authorityID, resolution)
	if err != nil {
		return Result{}, err
	}
	return service.executePlan(ctx, plan)
}

// validatePlanAuthorityResolution independently verifies the evidence-plan
// projection returned by the trusted resolver. The resolver remains
// responsible for validating the canonical authority envelope and its exact
// project, run, node, revision, source, build, and template-release target.
func (service *Service) validatePlanAuthorityResolution(authorityID string, resolution PlanAuthorityResolution) (Plan, error) {
	if !validUUIDv4(resolution.AuthorityID) || resolution.AuthorityID != authorityID {
		return Plan{}, fmt.Errorf("%w: resolved plan authority identity does not match the request", ErrInvalid)
	}
	if !validDigest(resolution.AuthorityHash) {
		return Plan{}, fmt.Errorf("%w: resolved plan authority hash is invalid", ErrInvalid)
	}
	if !validStableID(resolution.ArtifactID) || resolution.ArtifactID != "qualification-plan-"+authorityID {
		return Plan{}, fmt.Errorf("%w: resolved qualification plan artifact identity is invalid", ErrInvalid)
	}
	if !validDigest(resolution.EvidencePlanHash) {
		return Plan{}, fmt.Errorf("%w: resolved evidence plan hash is invalid", ErrInvalid)
	}
	if !validDigest(resolution.TrustBindingsDigest) || resolution.TrustBindingsDigest != service.trustDigest {
		return Plan{}, fmt.Errorf("%w: resolved trust bindings do not match server trust", ErrInvalid)
	}
	if err := ValidatePlan(resolution.Plan); err != nil {
		return Plan{}, err
	}
	if resolution.Plan.QualificationPlanArtifactID != resolution.ArtifactID {
		return Plan{}, fmt.Errorf("%w: resolved plan does not bind the authority artifact", ErrInvalid)
	}
	canonicalPlan, err := CanonicalJSON(resolution.Plan)
	if err != nil {
		return Plan{}, err
	}
	if !bytes.Equal(canonicalPlan, resolution.EvidencePlanBytes) {
		return Plan{}, fmt.Errorf("%w: resolved evidence plan bytes are not the exact canonical plan", ErrInvalid)
	}
	if sha256Digest(resolution.EvidencePlanBytes) != resolution.EvidencePlanHash {
		return Plan{}, fmt.Errorf("%w: resolved evidence plan hash does not match its canonical bytes", ErrInvalid)
	}
	return clonePlan(resolution.Plan), nil
}

// executePlan reserves the complete, authority-resolved plan and advances it.
// Tests of the state machine use this private boundary so production callers
// cannot bypass PlanAuthority resolution.
func (service *Service) executePlan(ctx context.Context, plan Plan) (Result, error) {
	if service == nil || isNilInterface(ctx) {
		return Result{}, fmt.Errorf("%w: service or context is nil", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := ValidatePlan(plan); err != nil {
		return Result{}, err
	}
	if plan.CredentialSet.Issuer != service.trust.CredentialAuthorityID {
		return Result{}, fmt.Errorf("%w: credential issuer does not match server trust", ErrInvalid)
	}
	commandHash, err := CanonicalDigest(plan)
	if err != nil {
		return Result{}, err
	}
	snapshot, loadErr := service.store.Load(ctx, plan.OrchestrationID)
	if loadErr != nil && !errors.Is(loadErr, ErrNotFound) {
		return Result{}, loadErr
	}
	if errors.Is(loadErr, ErrNotFound) {
		at, timeErr := service.trustedTime(ctx)
		if timeErr != nil {
			return Result{}, timeErr
		}
		reservedPlan := clonePlan(plan)
		event := Event{
			At: at, EventID: uuid.NewString(), Kind: EventReserved, OperationID: plan.Operations.Reserve,
			CommandHash: commandHash, TrustBindingsDigest: service.trustDigest, Plan: &reservedPlan,
		}
		var createErr error
		snapshot, _, createErr = service.store.Create(ctx, plan.OrchestrationID, event)
		if errors.Is(createErr, ErrStoreOutcomeUnknown) {
			current, reconcileErr := service.store.Load(ctx, plan.OrchestrationID)
			if reconcileErr != nil || current.LastEventID != event.EventID {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, createErr = current, nil
		}
		if createErr != nil {
			return Result{}, createErr
		}
	}
	if snapshot.CommandHash != commandHash || snapshot.Plan == nil || !canonicalEqual(*snapshot.Plan, plan) {
		return Result{}, ErrIdempotencyConflict
	}
	if snapshot.TrustBindingsDigest != service.trustDigest {
		return Result{}, fmt.Errorf("%w: trusted authority configuration changed", ErrIdempotencyConflict)
	}

	// A restricted artifact can consume a start/ownership pass and an Inspect
	// recovery pass. Keep the progress bound derived from the admitted plan
	// cardinality rather than silently truncating a valid large closure.
	for attempts := 0; attempts < 2*MaximumArtifacts+64; attempts++ {
		switch snapshot.Phase {
		case PhaseReserved:
			updated, owner, startErr := service.start(ctx, snapshot, EventCredentialIssueStarted, plan.Operations.CredentialIssue)
			if startErr != nil {
				return Result{}, startErr
			}
			snapshot = updated
			if !owner {
				continue
			}
			observation, callErr := service.credentials.IssueAtomic(ctx, service.issueRequest(plan))
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptCredentialIssue(ctx, snapshot, observation)
		case PhaseCredentialIssueStarted:
			observation, callErr := service.credentials.InspectIssue(ctx, service.operationRef(plan, plan.Operations.CredentialIssue))
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptCredentialIssue(ctx, snapshot, observation)
		case PhaseCredentialIssued:
			updated, owner, startErr := service.start(ctx, snapshot, EventRunClosureStarted, plan.Operations.RunClosure)
			if startErr != nil {
				return Result{}, startErr
			}
			snapshot = updated
			if !owner {
				continue
			}
			request, requestErr := service.runClosureRequest(snapshot)
			if requestErr != nil {
				return Result{}, requestErr
			}
			observation, callErr := service.capture.CloseRun(ctx, request)
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptRunClosure(ctx, snapshot, observation)
		case PhaseRunClosureStarted:
			observation, callErr := service.capture.InspectRunClosure(ctx, service.operationRef(plan, plan.Operations.RunClosure))
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptRunClosure(ctx, snapshot, observation)
		case PhaseRunClosureAccepted, PhaseEncrypting:
			if snapshot.ActiveOperationID != "" {
				observation, callErr := service.encryptor.InspectEncryption(ctx, service.operationRef(plan, snapshot.ActiveOperationID))
				if callErr != nil {
					return Result{}, ErrOutcomeUnknown
				}
				snapshot, err = service.acceptEncryption(ctx, snapshot, observation)
				break
			}
			expected, remaining := nextRestrictedArtifact(snapshot)
			if !remaining {
				if snapshot.Phase != PhaseEncrypting || !allRestrictedEncrypted(snapshot) {
					return Result{}, ErrInvalidTransition
				}
				updated, owner, startErr := service.start(ctx, snapshot, EventKMSAttestationStarted, plan.Operations.KMSAttestation)
				if startErr != nil {
					return Result{}, startErr
				}
				snapshot = updated
				if !owner {
					continue
				}
				request, requestErr := service.kmsRequest(snapshot)
				if requestErr != nil {
					return Result{}, requestErr
				}
				observation, callErr := service.kms.Attest(ctx, request)
				if callErr != nil {
					return Result{}, ErrOutcomeUnknown
				}
				snapshot, err = service.acceptKMS(ctx, snapshot, observation)
				break
			}
			updated, owner, startErr := service.start(ctx, snapshot, EventEncryptionStarted, expected.EncryptionOperationID)
			if startErr != nil {
				return Result{}, startErr
			}
			snapshot = updated
			if !owner {
				continue
			}
			request, requestErr := service.encryptionRequest(snapshot)
			if requestErr != nil {
				return Result{}, requestErr
			}
			observation, callErr := service.encryptor.Encrypt(ctx, request)
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptEncryption(ctx, snapshot, observation)
		case PhaseKMSAttestationStarted:
			observation, callErr := service.kms.InspectAttestation(ctx, service.operationRef(plan, plan.Operations.KMSAttestation))
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptKMS(ctx, snapshot, observation)
		case PhaseKMSAttested:
			updated, owner, startErr := service.start(ctx, snapshot, EventCredentialRevocationStarted, plan.Operations.CredentialRevocation)
			if startErr != nil {
				return Result{}, startErr
			}
			snapshot = updated
			if !owner {
				continue
			}
			observation, callErr := service.credentials.RevokeExact(ctx, service.revocationRequest(snapshot))
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptRevocation(ctx, snapshot, observation)
		case PhaseCredentialRevocationStarted:
			observation, callErr := service.credentials.InspectRevocation(ctx, service.operationRef(plan, plan.Operations.CredentialRevocation))
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptRevocation(ctx, snapshot, observation)
		case PhaseCredentialRevoked:
			updated, owner, startErr := service.start(ctx, snapshot, EventArtifactIndexStarted, plan.Operations.ArtifactIndex)
			if startErr != nil {
				return Result{}, startErr
			}
			snapshot = updated
			if !owner {
				continue
			}
			request, requestErr := service.indexRequest(snapshot)
			if requestErr != nil {
				return Result{}, requestErr
			}
			observation, callErr := service.indexer.BuildIndex(ctx, request)
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptIndex(ctx, snapshot, observation)
		case PhaseArtifactIndexStarted:
			observation, callErr := service.indexer.InspectIndex(ctx, service.operationRef(plan, plan.Operations.ArtifactIndex))
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptIndex(ctx, snapshot, observation)
		case PhaseArtifactIndexed:
			updated, owner, startErr := service.start(ctx, snapshot, EventReceiptSignStarted, plan.Operations.ReceiptSign)
			if startErr != nil {
				return Result{}, startErr
			}
			snapshot = updated
			if !owner {
				continue
			}
			request, requestErr := service.receiptRequest(snapshot)
			if requestErr != nil {
				return Result{}, requestErr
			}
			observation, callErr := service.receipt.SignReceipt(ctx, request)
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptReceipt(ctx, snapshot, observation)
		case PhaseReceiptSignStarted:
			observation, callErr := service.receipt.InspectReceipt(ctx, service.operationRef(plan, plan.Operations.ReceiptSign))
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptReceipt(ctx, snapshot, observation)
		case PhaseReceiptSigned:
			updated, owner, startErr := service.start(ctx, snapshot, EventSnapshotSealStarted, plan.Operations.SnapshotSeal)
			if startErr != nil {
				return Result{}, startErr
			}
			snapshot = updated
			if !owner {
				continue
			}
			request, requestErr := service.sealRequest(snapshot)
			if requestErr != nil {
				return Result{}, requestErr
			}
			observation, callErr := service.sealer.Seal(ctx, request)
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptSnapshot(ctx, snapshot, observation)
		case PhaseSnapshotSealStarted:
			observation, callErr := service.sealer.InspectSeal(ctx, service.operationRef(plan, plan.Operations.SnapshotSeal))
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			snapshot, err = service.acceptSnapshot(ctx, snapshot, observation)
		case PhaseSnapshotSealed:
			request, requestErr := service.verificationRequest(snapshot)
			if requestErr != nil {
				return Result{}, requestErr
			}
			observation, callErr := service.verifier.Verify(ctx, request)
			if callErr != nil {
				return Result{}, ErrOutcomeUnknown
			}
			if observation.AuthorityID != service.trust.VerifierAuthorityID || validateStoredVerification(snapshot, observation) != nil {
				return Result{}, ErrDigestDrift
			}
			snapshot, err = service.appendPayload(ctx, snapshot, EventSnapshotVerified, plan.Operations.SnapshotSeal, observation)
		case PhaseComplete:
			return resultFromSnapshot(snapshot)
		default:
			return Result{}, ErrInvalidTransition
		}
		if err != nil {
			return Result{}, err
		}
	}
	return Result{}, ErrOutcomeUnknown
}

func (service *Service) issueRequest(plan Plan) CredentialIssueRequest {
	return CredentialIssueRequest{
		OperationID: plan.Operations.CredentialIssue, RunID: plan.RunID, FixtureID: plan.FixtureID,
		PlanDigest: plan.PlanDigest, Expected: plan.CredentialSet,
	}
}

func (service *Service) operationRef(plan Plan, operationID string) OperationRef {
	return OperationRef{OperationID: operationID, OrchestrationID: plan.OrchestrationID, RunID: plan.RunID}
}

func (service *Service) runClosureRequest(snapshot Snapshot) (RunClosureRequest, error) {
	bindingDigest, err := credentialBindingDigest(snapshot.CredentialIssue.Binding)
	if err != nil {
		return RunClosureRequest{}, err
	}
	artifactDigest, err := expectedArtifactSetDigest(*snapshot.Plan)
	if err != nil {
		return RunClosureRequest{}, err
	}
	return RunClosureRequest{
		OperationID: snapshot.Plan.Operations.RunClosure, RunID: snapshot.Plan.RunID, PlanDigest: snapshot.Plan.PlanDigest,
		CredentialSetBindingDigest: bindingDigest, ExpectedArtifactSetDigest: artifactDigest,
	}, nil
}

func (service *Service) encryptionRequest(snapshot Snapshot) (EncryptionRequest, error) {
	captured, exists := capturedByID(snapshot, snapshot.ActiveArtifactID)
	if !exists {
		return EncryptionRequest{}, ErrEvidenceClosure
	}
	aad, err := encryptionAdditionalDataHash(*snapshot.Plan, captured)
	if err != nil {
		return EncryptionRequest{}, err
	}
	return EncryptionRequest{
		OperationID: snapshot.ActiveOperationID, RunID: snapshot.Plan.RunID, PlanDigest: snapshot.Plan.PlanDigest,
		Artifact: captured, Recipient: snapshot.Plan.Recipient, AdditionalDataHash: aad,
	}, nil
}

func (service *Service) kmsRequest(snapshot Snapshot) (KMSAttestationRequest, error) {
	manifest, err := encryptionManifestDigest(snapshot.Encryptions)
	if err != nil {
		return KMSAttestationRequest{}, err
	}
	artifactSet, err := preKMSArtifactSetDigest(snapshot)
	if err != nil {
		return KMSAttestationRequest{}, err
	}
	if artifactSet == manifest {
		return KMSAttestationRequest{}, ErrDigestDrift
	}
	payloadDigest, err := expectedKMSPayloadDigest(snapshot.Plan.RunID, snapshot.Plan.PlanDigest, manifest, artifactSet)
	if err != nil {
		return KMSAttestationRequest{}, err
	}
	return KMSAttestationRequest{
		OperationID: snapshot.Plan.Operations.KMSAttestation, RunID: snapshot.Plan.RunID,
		PlanDigest: snapshot.Plan.PlanDigest, ManifestDigest: manifest, ArtifactSetDigest: artifactSet,
		ArtifactCount: len(snapshot.Encryptions), ExpectedArtifactID: snapshot.Plan.Outputs.KMSAttestationArtifactID,
		ExpectedPayloadDigest: payloadDigest,
	}, nil
}

func (service *Service) revocationRequest(snapshot Snapshot) CredentialRevocationRequest {
	return CredentialRevocationRequest{
		OperationID: snapshot.Plan.Operations.CredentialRevocation, RunID: snapshot.Plan.RunID,
		Binding:              cloneCredentialBinding(snapshot.CredentialIssue.Binding),
		KMSAttestationDigest: snapshot.KMSAttestation.Attestation.PayloadDigest,
	}
}

func (service *Service) indexRequest(snapshot Snapshot) (ArtifactIndexRequest, error) {
	closure, err := evidenceClosureDigest(snapshot)
	if err != nil {
		return ArtifactIndexRequest{}, err
	}
	artifactDigest, err := artifactSetDigest(snapshot)
	if err != nil {
		return ArtifactIndexRequest{}, err
	}
	return ArtifactIndexRequest{
		OperationID: snapshot.Plan.Operations.ArtifactIndex, RunID: snapshot.Plan.RunID, PlanDigest: snapshot.Plan.PlanDigest,
		EvidenceClosureDigest: closure, ArtifactSetDigest: artifactDigest,
		ArtifactCount: len(snapshot.Plan.Artifacts) + 3, RestrictedArtifactCount: len(snapshot.Encryptions),
		ExpectedIndexID: snapshot.Plan.Outputs.ArtifactIndexID,
	}, nil
}

func (service *Service) receiptRequest(snapshot Snapshot) (ReceiptSignRequest, error) {
	closure, err := evidenceClosureDigest(snapshot)
	if err != nil {
		return ReceiptSignRequest{}, err
	}
	payloadDigest, err := expectedReceiptPayloadDigest(snapshot.Plan.RunID, snapshot.Plan.PlanDigest, closure, *snapshot.ArtifactIndex)
	if err != nil {
		return ReceiptSignRequest{}, err
	}
	return ReceiptSignRequest{
		OperationID: snapshot.Plan.Operations.ReceiptSign, RunID: snapshot.Plan.RunID, PlanDigest: snapshot.Plan.PlanDigest,
		EvidenceClosureDigest: closure, Index: *snapshot.ArtifactIndex, ExpectedReceiptID: snapshot.Plan.Outputs.ReceiptID,
		ExpectedPayloadDigest: payloadDigest,
	}, nil
}

func (service *Service) sealRequest(snapshot Snapshot) (SnapshotSealRequest, error) {
	closure, err := evidenceClosureDigest(snapshot)
	if err != nil {
		return SnapshotSealRequest{}, err
	}
	return SnapshotSealRequest{
		OperationID: snapshot.Plan.Operations.SnapshotSeal, RunID: snapshot.Plan.RunID,
		EvidenceClosureDigest: closure, Index: *snapshot.ArtifactIndex, Receipt: *snapshot.Receipt,
		ExpectedSnapshotID: snapshot.Plan.Outputs.SnapshotID, Mode: ImmutableSnapshotMode,
	}, nil
}

func (service *Service) verificationRequest(snapshot Snapshot) (SnapshotVerificationRequest, error) {
	closure, err := evidenceClosureDigest(snapshot)
	if err != nil {
		return SnapshotVerificationRequest{}, err
	}
	return SnapshotVerificationRequest{
		OrchestrationID: snapshot.OrchestrationID, RunID: snapshot.Plan.RunID,
		EvidenceClosureDigest: closure, Snapshot: *snapshot.SealedSnapshot,
	}, nil
}

func (service *Service) acceptCredentialIssue(ctx context.Context, snapshot Snapshot, value CredentialIssueObservation) (Snapshot, error) {
	if err := authorityOutcome(value.Stage); err != nil {
		return Snapshot{}, err
	}
	if err := validateCredentialIssue(value, *snapshot.Plan); err != nil {
		return Snapshot{}, err
	}
	return service.appendPayload(ctx, snapshot, EventCredentialIssued, value.OperationID, value)
}

func (service *Service) acceptRunClosure(ctx context.Context, snapshot Snapshot, value RunClosureObservation) (Snapshot, error) {
	if err := authorityOutcome(value.Stage); err != nil {
		return Snapshot{}, err
	}
	request, err := service.runClosureRequest(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	if err := validateRunClosure(value, request, *snapshot.Plan, service.trust); err != nil {
		return Snapshot{}, err
	}
	return service.appendPayload(ctx, snapshot, EventRunClosureAccepted, value.OperationID, value)
}

func (service *Service) acceptEncryption(ctx context.Context, snapshot Snapshot, value EncryptionCommitment) (Snapshot, error) {
	if err := authorityOutcome(value.Stage); err != nil {
		return Snapshot{}, err
	}
	request, err := service.encryptionRequest(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	if err := validateEncryption(value, request, service.trust); err != nil {
		return Snapshot{}, err
	}
	return service.appendPayload(ctx, snapshot, EventEncryptionCommitted, value.OperationID, value)
}

func (service *Service) acceptKMS(ctx context.Context, snapshot Snapshot, value KMSAttestationObservation) (Snapshot, error) {
	if err := authorityOutcome(value.Stage); err != nil {
		return Snapshot{}, err
	}
	request, err := service.kmsRequest(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	if value.OperationID != request.OperationID || value.AuthorityID != service.trust.KMSAuthorityID ||
		value.ManifestDigest != request.ManifestDigest || value.ArtifactSetDigest != request.ArtifactSetDigest ||
		value.Attestation.PayloadDigest != request.ExpectedPayloadDigest ||
		validateSignedArtifact(value.Attestation, request.ExpectedArtifactID, service.trust.KMSAuthorityID) != nil {
		return Snapshot{}, ErrDigestDrift
	}
	return service.appendPayload(ctx, snapshot, EventKMSAttested, value.OperationID, value)
}

func (service *Service) acceptRevocation(ctx context.Context, snapshot Snapshot, value CredentialRevocationObservation) (Snapshot, error) {
	if err := authorityOutcome(value.Stage); err != nil {
		return Snapshot{}, err
	}
	request := service.revocationRequest(snapshot)
	requestDigest, digestErr := digestRequest(request)
	if digestErr != nil {
		return Snapshot{}, digestErr
	}
	if value.OperationID != request.OperationID || value.RequestDigest != requestDigest ||
		value.KMSAttestationDigest != request.KMSAttestationDigest || !equalCredentialBinding(value.Binding, request.Binding) {
		return Snapshot{}, ErrCredentialDrift
	}
	return service.appendPayload(ctx, snapshot, EventCredentialRevoked, value.OperationID, value)
}

func (service *Service) acceptIndex(ctx context.Context, snapshot Snapshot, value ArtifactIndexCommitment) (Snapshot, error) {
	if err := authorityOutcome(value.Stage); err != nil {
		return Snapshot{}, err
	}
	if value.AuthorityID != service.trust.IndexerAuthorityID || validateStoredIndex(snapshot, value) != nil {
		return Snapshot{}, ErrDigestDrift
	}
	return service.appendPayload(ctx, snapshot, EventArtifactIndexed, value.OperationID, value)
}

func (service *Service) acceptReceipt(ctx context.Context, snapshot Snapshot, value QualificationReceiptCommitment) (Snapshot, error) {
	if err := authorityOutcome(value.Stage); err != nil {
		return Snapshot{}, err
	}
	request, requestErr := service.receiptRequest(snapshot)
	if requestErr != nil {
		return Snapshot{}, requestErr
	}
	if value.AuthorityID != service.trust.ReceiptAuthorityID || value.PayloadDigest != request.ExpectedPayloadDigest ||
		validateStoredReceipt(snapshot, value) != nil {
		return Snapshot{}, ErrDigestDrift
	}
	return service.appendPayload(ctx, snapshot, EventReceiptSigned, value.OperationID, value)
}

func (service *Service) acceptSnapshot(ctx context.Context, snapshot Snapshot, value SnapshotCommitment) (Snapshot, error) {
	if err := authorityOutcome(value.Stage); err != nil {
		return Snapshot{}, err
	}
	if value.AuthorityID != service.trust.SealerAuthorityID || validateStoredSnapshot(snapshot, value) != nil {
		return Snapshot{}, ErrDigestDrift
	}
	return service.appendPayload(ctx, snapshot, EventSnapshotSealed, value.OperationID, value)
}

func (service *Service) start(ctx context.Context, snapshot Snapshot, kind EventKind, operationID string) (Snapshot, bool, error) {
	event, err := service.newEvent(ctx, kind, operationID)
	if err != nil {
		return Snapshot{}, false, err
	}
	updated, err := service.store.Append(ctx, snapshot.OrchestrationID, snapshot.Version, event)
	if errors.Is(err, ErrCASConflict) {
		current, loadErr := service.store.Load(ctx, snapshot.OrchestrationID)
		return current, false, loadErr
	}
	if errors.Is(err, ErrStoreOutcomeUnknown) {
		current, loadErr := service.store.Load(ctx, snapshot.OrchestrationID)
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

func (service *Service) appendPayload(ctx context.Context, snapshot Snapshot, kind EventKind, operationID string, payload any) (Snapshot, error) {
	event, err := service.newEvent(ctx, kind, operationID)
	if err != nil {
		return Snapshot{}, err
	}
	switch value := payload.(type) {
	case CredentialIssueObservation:
		copy := cloneCredentialIssue(value)
		event.CredentialIssue = &copy
	case RunClosureObservation:
		copy := cloneRunClosure(value)
		event.RunClosure = &copy
	case EncryptionCommitment:
		copy := value
		event.Encryption = &copy
	case KMSAttestationObservation:
		copy := value
		event.KMSAttestation = &copy
	case CredentialRevocationObservation:
		copy := cloneCredentialRevocation(value)
		event.CredentialRevocation = &copy
	case ArtifactIndexCommitment:
		copy := value
		event.ArtifactIndex = &copy
	case QualificationReceiptCommitment:
		copy := value
		event.Receipt = &copy
	case SnapshotCommitment:
		copy := value
		event.Snapshot = &copy
	case SnapshotVerification:
		copy := value
		event.Verification = &copy
	default:
		return Snapshot{}, fmt.Errorf("%w: unsupported event payload", ErrInvalid)
	}
	updated, err := service.store.Append(ctx, snapshot.OrchestrationID, snapshot.Version, event)
	if errors.Is(err, ErrCASConflict) {
		return service.store.Load(ctx, snapshot.OrchestrationID)
	}
	if !errors.Is(err, ErrStoreOutcomeUnknown) {
		return updated, err
	}
	current, loadErr := service.store.Load(ctx, snapshot.OrchestrationID)
	if loadErr != nil || current.LastEventID != event.EventID {
		return Snapshot{}, ErrOutcomeUnknown
	}
	return current, nil
}

func (service *Service) newEvent(ctx context.Context, kind EventKind, operationID string) (Event, error) {
	at, err := service.trustedTime(ctx)
	if err != nil {
		return Event{}, err
	}
	return Event{At: at, EventID: uuid.NewString(), Kind: kind, OperationID: operationID}, nil
}

func (service *Service) trustedTime(ctx context.Context) (string, error) {
	now, err := service.store.TrustedTime(ctx)
	if err != nil {
		return "", err
	}
	value, err := canonicalTime(now.UTC().Truncate(1e6))
	if err != nil {
		return "", fmt.Errorf("%w: trusted store time is invalid", ErrInvalid)
	}
	return value, nil
}

func resultFromSnapshot(snapshot Snapshot) (Result, error) {
	if snapshot.Phase != PhaseComplete || snapshot.Plan == nil || snapshot.ArtifactIndex == nil || snapshot.Receipt == nil ||
		snapshot.SealedSnapshot == nil || snapshot.Verification == nil {
		return Result{}, ErrInvalidTransition
	}
	closure, err := evidenceClosureDigest(snapshot)
	if err != nil {
		return Result{}, err
	}
	return Result{
		OrchestrationID: snapshot.OrchestrationID, RunID: snapshot.Plan.RunID, EvidenceClosureDigest: closure,
		ArtifactIndex: *snapshot.ArtifactIndex, Receipt: *snapshot.Receipt,
		Snapshot: *snapshot.SealedSnapshot, Verification: *snapshot.Verification,
	}, nil
}
