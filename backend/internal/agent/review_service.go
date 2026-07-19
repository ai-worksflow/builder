package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrEvidenceUnavailable = errors.New("AgentAttempt evidence is unavailable")

type ReviewStore interface {
	ResolveAttemptProject(context.Context, string) (string, error)
	GetAttempt(context.Context, string, string) (AgentAttempt, error)
}

type FinalizedEvidenceReader interface {
	GetFinalized(context.Context, AgentAttempt, EvidenceKind, BlobReference) ([]byte, error)
}

type EvidenceReadResult struct {
	Attempt   AgentAttempt
	Kind      EvidenceKind
	Reference BlobReference
	MediaType string
	RawHash   string
	Value     []byte
}

// ReviewService is the only browser-facing bridge to Agent evidence. It
// resolves project scope from the Attempt, requires project view permission,
// and returns only finalized content whose immutable reference is already
// committed to the Attempt projection.
type ReviewService struct {
	store    ReviewStore
	evidence FinalizedEvidenceReader
	access   ProjectAuthorizer
}

func NewReviewService(
	store ReviewStore,
	evidence FinalizedEvidenceReader,
	access ProjectAuthorizer,
) (*ReviewService, error) {
	if store == nil || evidence == nil || access == nil {
		return nil, errors.New("Agent review store, finalized evidence reader, and authorizer are required")
	}
	return &ReviewService{store: store, evidence: evidence, access: access}, nil
}

func (service *ReviewService) ReadEvidence(
	ctx context.Context,
	attemptID, actorID string,
	kind EvidenceKind,
) (EvidenceReadResult, error) {
	if service == nil || ctx == nil || !validUUIDs(attemptID, actorID) || !knownEvidenceKind(kind) {
		return EvidenceReadResult{}, fmt.Errorf("%w: evidence request", ErrEvidenceInvalid)
	}
	projectID, err := service.store.ResolveAttemptProject(ctx, attemptID)
	if err != nil {
		return EvidenceReadResult{}, err
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return EvidenceReadResult{}, fmt.Errorf("authorize AgentAttempt evidence view: %w", err)
	}
	attempt, err := service.store.GetAttempt(ctx, projectID, attemptID)
	if err != nil {
		return EvidenceReadResult{}, err
	}
	reference, mediaType, found := attemptEvidenceReference(attempt.Evidence, kind)
	if !found {
		return EvidenceReadResult{}, ErrEvidenceUnavailable
	}
	value, err := service.evidence.GetFinalized(ctx, attempt, kind, reference)
	if err != nil {
		return EvidenceReadResult{}, err
	}
	if err := validateReviewEvidence(attempt, kind, value); err != nil {
		return EvidenceReadResult{}, err
	}
	return EvidenceReadResult{
		Attempt: attempt, Kind: kind, Reference: reference,
		MediaType: mediaType, RawHash: rawEvidenceHash(value), Value: append([]byte(nil), value...),
	}, nil
}

func attemptEvidenceReference(
	evidence AttemptEvidence,
	kind EvidenceKind,
) (BlobReference, string, bool) {
	var reference *BlobReference
	mediaType := "application/octet-stream"
	switch kind {
	case EvidencePatch:
		reference, mediaType = evidence.Patch, "application/json"
	case EvidenceStructuredResult:
		reference, mediaType = evidence.StructuredResult, "application/json"
	case EvidenceStdout:
		reference, mediaType = evidence.Stdout, "application/x-ndjson"
	case EvidenceStderr:
		reference, mediaType = evidence.Stderr, "text/plain; charset=utf-8"
	case EvidenceValidation:
		reference, mediaType = evidence.Validation, "application/json"
	}
	if reference == nil {
		return BlobReference{}, "", false
	}
	return *reference, mediaType, true
}

func validateReviewEvidence(attempt AgentAttempt, kind EvidenceKind, value []byte) error {
	switch kind {
	case EvidencePatch:
		var patch PlatformPatch
		if err := decodeStrictJSON(value, &patch); err != nil {
			return err
		}
		parsed, err := ParsePlatformPatch(patch)
		if err != nil || parsed.AttemptID != attempt.ID || parsed.ProjectID != attempt.ProjectID ||
			parsed.CandidateID != attempt.CandidateID || parsed.TaskCapsule != attempt.TaskCapsule ||
			parsed.ConfigurationHash != attempt.ConfigurationHash {
			return fmt.Errorf("%w: review Patch does not bind the Attempt", ErrEvidenceIntegrity)
		}
	case EvidenceValidation:
		var receipt PatchValidationReceipt
		if err := decodeStrictJSON(value, &receipt); err != nil {
			return err
		}
		parsed, err := ParsePatchValidationReceipt(receipt)
		if err != nil || parsed.AttemptID != attempt.ID || parsed.ProjectID != attempt.ProjectID ||
			parsed.TaskCapsule != attempt.TaskCapsule || parsed.BaseTreeHash != attempt.BaseCandidateTreeHash ||
			attempt.Evidence.Patch == nil || parsed.Patch != *attempt.Evidence.Patch {
			return fmt.Errorf("%w: review validation does not bind the Attempt", ErrEvidenceIntegrity)
		}
	case EvidenceStructuredResult:
		if !json.Valid(value) {
			return fmt.Errorf("%w: structured result is not JSON", ErrEvidenceIntegrity)
		}
	case EvidenceStdout, EvidenceStderr:
		// Logs are opaque bounded evidence. Their envelope hash and byte size are
		// verified by EvidenceStore before they cross the review boundary.
	default:
		return ErrEvidenceInvalid
	}
	return nil
}
