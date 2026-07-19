package repository

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (service *ExactTreeLiteralIndexService) buildWithDurableClaim(
	ctx context.Context,
	projectID, actorID string,
	canonical TreeManifest,
) (ExactTreeLiteralIndexManifest, error) {
	if service.newClaimOwner == nil || service.claimLease < 50*time.Millisecond ||
		service.claimLease > 5*time.Minute || service.claimHeartbeat <= 0 ||
		service.claimHeartbeat >= service.claimLease || service.claimPoll <= 0 ||
		service.releaseTimeout <= 0 {
		return ExactTreeLiteralIndexManifest{}, ErrInvalidExactTreeLiteralIndex
	}
	ownerToken := service.newClaimOwner()
	if !validUUID(ownerToken) {
		return ExactTreeLiteralIndexManifest{}, ErrInvalidExactTreeLiteralIndex
	}
	request := ExactTreeLiteralIndexBuildClaimRequest{
		ProjectID: projectID, TreeHash: canonical.TreeHash, OwnerToken: ownerToken,
		SourceBytes: treeByteSize(canonical), Lease: service.claimLease,
		MaxProjectTrees:        service.projectQuota.MaxTrees,
		MaxProjectSourceBytes:  service.projectQuota.MaxSourceBytes,
		MaxProjectActiveBuilds: service.projectQuota.MaxActiveBuilds,
	}
	for {
		if err := ctx.Err(); err != nil {
			return ExactTreeLiteralIndexManifest{}, err
		}
		result, err := service.store.AcquireExactTreeLiteralIndexBuildClaim(ctx, request)
		if err != nil {
			return ExactTreeLiteralIndexManifest{}, fmt.Errorf(
				"acquire exact-tree literal index build claim: %w", err,
			)
		}
		switch result.Disposition {
		case ExactTreeLiteralBuildClaimReady:
			if err := validateReadyExactTreeLiteralIndexForTree(result.Manifest, projectID, canonical); err != nil {
				return ExactTreeLiteralIndexManifest{}, err
			}
			result.Manifest.Reused = true
			return result.Manifest, nil
		case ExactTreeLiteralBuildClaimAcquired:
			if err := validateExactTreeLiteralIndexBuildClaim(result.Claim, request); err != nil {
				return ExactTreeLiteralIndexManifest{}, err
			}
			if service.firstBuilderAdmission != nil {
				admissionErr := service.firstBuilderAdmission.Admit(
					ctx,
					ExactTreeSearchAdmissionRequest{
						ProjectID: projectID,
						ActorID:   actorID,
						Operation: ExactTreeSearchAdmissionFirstBuilder,
					},
				)
				if admissionErr != nil {
					admissionErr = normalizeExactTreeLiteralFirstBuilderAdmissionError(admissionErr)
					releaseErr := service.releaseExactTreeLiteralIndexBuildClaim(ctx, result.Claim)
					if releaseErr != nil {
						return ExactTreeLiteralIndexManifest{}, errors.Join(admissionErr, releaseErr)
					}
					return ExactTreeLiteralIndexManifest{}, admissionErr
				}
			}
			return service.buildAsClaimOwner(ctx, canonical, result.Claim)
		case ExactTreeLiteralBuildClaimWaiting:
			// The acquisition call has already returned its connection. Waiting is
			// an ordinary cancellable process timer, never a sleeping SQL session.
			timer := time.NewTimer(service.claimPoll)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return ExactTreeLiteralIndexManifest{}, ctx.Err()
			case <-timer.C:
			}
		default:
			return ExactTreeLiteralIndexManifest{}, exactTreeLiteralIndexContract(
				"build coordinator returned an unknown disposition", nil,
			)
		}
	}
}

func (service *ExactTreeLiteralIndexService) buildAsClaimOwner(
	ctx context.Context,
	canonical TreeManifest,
	claim ExactTreeLiteralIndexBuildClaim,
) (ExactTreeLiteralIndexManifest, error) {
	buildCtx, cancelBuild := context.WithCancelCause(ctx)
	heartbeatCtx, stopHeartbeat := context.WithCancel(buildCtx)
	heartbeatDone := make(chan error, 1)
	go service.runExactTreeLiteralIndexClaimHeartbeat(
		heartbeatCtx, buildCtx, cancelBuild, claim, heartbeatDone,
	)

	build, operationErr := service.resolveClaimedBuild(
		buildCtx, claim.ProjectID, canonical, claim,
	)
	var manifest ExactTreeLiteralIndexManifest
	if operationErr == nil {
		if cause := context.Cause(buildCtx); cause != nil {
			operationErr = cause
		} else {
			manifest, operationErr = service.store.PublishExactTreeLiteralIndex(buildCtx, build)
			if operationErr == nil {
				operationErr = validateExactTreeLiteralIndexManifest(manifest, build)
			}
		}
	}
	stopHeartbeat()
	heartbeatErr := <-heartbeatDone
	cancelBuild(nil)

	releaseErr := service.releaseExactTreeLiteralIndexBuildClaim(ctx, claim)
	if heartbeatErr != nil && !errors.Is(heartbeatErr, context.Canceled) {
		operationErr = errors.Join(operationErr, heartbeatErr)
	}
	if operationErr != nil || releaseErr != nil {
		return ExactTreeLiteralIndexManifest{}, errors.Join(operationErr, releaseErr)
	}
	return manifest, nil
}

func (service *ExactTreeLiteralIndexService) releaseExactTreeLiteralIndexBuildClaim(
	ctx context.Context,
	claim ExactTreeLiteralIndexBuildClaim,
) error {
	releaseCtx, cancelRelease := context.WithTimeout(
		context.WithoutCancel(ctx), service.releaseTimeout,
	)
	defer cancelRelease()
	if err := service.store.ReleaseExactTreeLiteralIndexBuildClaim(releaseCtx, claim); err != nil {
		return errors.Join(
			ErrExactTreeLiteralClaimRelease,
			fmt.Errorf("release exact-tree literal index build claim: %w", err),
		)
	}
	return nil
}

func normalizeExactTreeLiteralFirstBuilderAdmissionError(err error) error {
	if err == nil {
		return nil
	}
	denied := errors.Is(err, ErrExactTreeSearchAdmissionDenied)
	unavailable := errors.Is(err, ErrExactTreeSearchAdmissionUnavailable)
	if denied {
		var typed *ExactTreeSearchAdmissionDeniedError
		if unavailable || !errors.As(err, &typed) || typed == nil ||
			typed.Operation != ExactTreeSearchAdmissionFirstBuilder ||
			typed.RetryAfter <= 0 || typed.RetryAfter > time.Hour {
			// Do not retain a malformed denial as a wrapped cause: callers must not
			// classify a bare or invalid Denied sentinel as an ordinary throttle.
			return ErrExactTreeSearchAdmissionUnavailable
		}
		return err
	}
	if unavailable {
		return err
	}
	return errors.Join(ErrExactTreeSearchAdmissionUnavailable, err)
}

func (service *ExactTreeLiteralIndexService) runExactTreeLiteralIndexClaimHeartbeat(
	heartbeatCtx context.Context,
	buildCtx context.Context,
	cancelBuild context.CancelCauseFunc,
	claim ExactTreeLiteralIndexBuildClaim,
	done chan<- error,
) {
	ticker := time.NewTicker(service.claimHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-heartbeatCtx.Done():
			if cause := context.Cause(buildCtx); cause != nil {
				done <- cause
			} else {
				done <- nil
			}
			return
		case <-ticker.C:
			renewed, err := service.store.RenewExactTreeLiteralIndexBuildClaim(
				heartbeatCtx, claim, service.claimLease,
			)
			if err == nil {
				err = validateRenewedExactTreeLiteralIndexBuildClaim(renewed, claim)
				if err == nil {
					claim = renewed
				}
			}
			if err != nil {
				if heartbeatCtx.Err() != nil && buildCtx.Err() == nil {
					done <- nil
					return
				}
				lost := errors.Join(
					ErrExactTreeLiteralBuildClaimLost,
					fmt.Errorf("renew exact-tree literal index build claim: %w", err),
				)
				cancelBuild(lost)
				done <- lost
				return
			}
		}
	}
}

func validateExactTreeLiteralIndexBuildClaim(
	claim ExactTreeLiteralIndexBuildClaim,
	request ExactTreeLiteralIndexBuildClaimRequest,
) error {
	if claim.ProjectID != request.ProjectID || claim.TreeHash != request.TreeHash ||
		claim.OwnerToken != request.OwnerToken || !validUUID(claim.OwnerToken) ||
		claim.Attempt <= 0 || claim.ReservedSourceBytes != request.SourceBytes ||
		claim.LeaseExpiresAt.IsZero() {
		return exactTreeLiteralIndexContract("acquired build claim has invalid identity or lease facts", nil)
	}
	return nil
}

func validateRenewedExactTreeLiteralIndexBuildClaim(
	renewed, expected ExactTreeLiteralIndexBuildClaim,
) error {
	if renewed.ProjectID != expected.ProjectID || renewed.TreeHash != expected.TreeHash ||
		renewed.OwnerToken != expected.OwnerToken || renewed.Attempt != expected.Attempt ||
		renewed.ReservedSourceBytes != expected.ReservedSourceBytes ||
		renewed.LeaseExpiresAt.IsZero() ||
		!renewed.LeaseExpiresAt.After(expected.LeaseExpiresAt) {
		return ErrExactTreeLiteralBuildClaimLost
	}
	return nil
}

func validateReadyExactTreeLiteralIndexForTree(
	manifest ExactTreeLiteralIndexManifest,
	projectID string,
	canonical TreeManifest,
) error {
	if manifest.SchemaVersion != ExactTreeLiteralIndexSchemaVersion ||
		manifest.ProjectID != projectID || manifest.TreeHash != canonical.TreeHash ||
		manifest.FileCount != len(canonical.Files) ||
		manifest.TextFileCount < 0 || manifest.SkippedFileCount < 0 ||
		manifest.TextFileCount+manifest.SkippedFileCount != manifest.FileCount ||
		manifest.TotalBytes != treeByteSize(canonical) ||
		!isCanonicalSHA256(manifest.TreeCommitment) ||
		!isCanonicalSHA256(manifest.IndexCommitment) || manifest.ReadyAt.IsZero() {
		return exactTreeLiteralIndexContract("ready build claim result differs from the requested exact tree", nil)
	}
	files := make([]ExactTreeLiteralIndexBuildFile, len(canonical.Files))
	for index, file := range canonical.Files {
		files[index] = ExactTreeLiteralIndexBuildFile{
			Path: file.Path, Mode: file.Mode,
			ContentHash: file.ContentHash, ByteSize: file.ByteSize,
		}
	}
	treeCommitment, _, err := exactTreeLiteralIndexCommitments(ExactTreeLiteralIndexBuild{
		SchemaVersion: ExactTreeLiteralIndexSchemaVersion,
		ProjectID:     projectID, TreeHash: canonical.TreeHash,
		FileCount: len(files), Files: files,
	})
	if err != nil || treeCommitment != manifest.TreeCommitment {
		return exactTreeLiteralIndexContract("ready manifest tree commitment differs from canonical input", err)
	}
	return nil
}
