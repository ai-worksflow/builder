package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

var ErrPatchFileUnavailable = errors.New("Agent patch file is unavailable")

type PatchFileSide string

const (
	PatchFileBase     PatchFileSide = "base"
	PatchFileProposed PatchFileSide = "proposed"
)

type PatchFileReviewSource interface {
	GetTaskCapsule(context.Context, string, string) (TaskCapsule, error)
}

type PatchFileReviewEvidence interface {
	ReadEvidence(context.Context, string, string, EvidenceKind) (EvidenceReadResult, error)
}

type PatchFileReviewTreeResolver interface {
	ResolveExactTree(context.Context, TaskCapsule) (repository.TreeManifest, error)
}

type PatchFileReviewBlobResolver interface {
	Resolve(context.Context, string, string, int64) (repository.FileBlobPointer, []byte, error)
}

type PatchFileReviewResult struct {
	Attempt            AgentAttempt
	PatchContentHash   string
	RepresentationHash string
	Operation          repository.FileOperation
	Path               string
	Side               PatchFileSide
	Exists             bool
	Mode               string
	ContentHash        string
	ByteSize           int64
	Value              []byte
}

// PatchFileReviewService exposes only file bodies named by one authorized,
// finalized PlatformPatch. It deliberately accepts no blob pointer or content
// hash from the caller, so it cannot be used as a tenant-scoped blob oracle.
type PatchFileReviewService struct {
	review PatchFileReviewEvidence
	source PatchFileReviewSource
	trees  PatchFileReviewTreeResolver
	files  PatchFileReviewBlobResolver
}

func NewPatchFileReviewService(
	review PatchFileReviewEvidence,
	source PatchFileReviewSource,
	trees PatchFileReviewTreeResolver,
	files PatchFileReviewBlobResolver,
) (*PatchFileReviewService, error) {
	if review == nil || source == nil || trees == nil || files == nil {
		return nil, errors.New("Agent patch file review, source, tree, and file resolvers are required")
	}
	return &PatchFileReviewService{review: review, source: source, trees: trees, files: files}, nil
}

func (service *PatchFileReviewService) ReadPatchFile(
	ctx context.Context,
	attemptID, actorID, requestedPath string,
	side PatchFileSide,
) (PatchFileReviewResult, error) {
	if service == nil || ctx == nil {
		return PatchFileReviewResult{}, fmt.Errorf("%w: patch file request", ErrEvidenceInvalid)
	}

	// ReviewService is intentionally the first authoritative lookup. It derives
	// project scope, authorizes the actor, and reads only finalized evidence.
	evidence, err := service.review.ReadEvidence(ctx, attemptID, actorID, EvidencePatch)
	if err != nil {
		return PatchFileReviewResult{}, err
	}
	path, err := repository.NormalizePath(requestedPath)
	if err != nil || path != requestedPath || (side != PatchFileBase && side != PatchFileProposed) {
		return PatchFileReviewResult{}, fmt.Errorf("%w: patch file path or side", ErrEvidenceInvalid)
	}
	if evidence.Kind != EvidencePatch || evidence.Attempt.ID != attemptID ||
		evidence.Attempt.Evidence.Patch == nil || evidence.Reference != *evidence.Attempt.Evidence.Patch {
		return PatchFileReviewResult{}, fmt.Errorf("%w: patch review result binding", ErrEvidenceIntegrity)
	}
	if evidence.RawHash != rawEvidenceHash(evidence.Value) {
		return PatchFileReviewResult{}, fmt.Errorf("%w: patch review raw hash", ErrEvidenceIntegrity)
	}

	var patch PlatformPatch
	if err := decodeStrictJSON(evidence.Value, &patch); err != nil {
		return PatchFileReviewResult{}, err
	}
	patch, err = ParsePlatformPatch(patch)
	if err != nil || patch.AttemptID != evidence.Attempt.ID ||
		patch.ProjectID != evidence.Attempt.ProjectID ||
		patch.CandidateID != evidence.Attempt.CandidateID ||
		patch.TaskCapsule != evidence.Attempt.TaskCapsule ||
		patch.ConfigurationHash != evidence.Attempt.ConfigurationHash ||
		patch.BaseTreeHash != evidence.Attempt.BaseCandidateTreeHash {
		return PatchFileReviewResult{}, fmt.Errorf("%w: exact patch file binding", ErrEvidenceIntegrity)
	}

	var operation repository.FileOperation
	found := false
	for _, candidate := range patch.Operations {
		if candidate.Path == path {
			operation, found = candidate, true
			break
		}
	}
	if !found {
		return PatchFileReviewResult{}, ErrPatchFileUnavailable
	}

	capsule, err := service.source.GetTaskCapsule(ctx, patch.ProjectID, patch.TaskCapsule.ID)
	if err != nil {
		return PatchFileReviewResult{}, err
	}
	if capsule.ExactReference() != patch.TaskCapsule || capsule.ProjectID != patch.ProjectID ||
		capsule.CandidateID != patch.CandidateID ||
		capsule.SandboxSessionID != evidence.Attempt.SandboxSessionID ||
		capsule.BaseCandidateTreeHash != patch.BaseTreeHash {
		return PatchFileReviewResult{}, fmt.Errorf("%w: patch file TaskCapsule binding", ErrEvidenceIntegrity)
	}
	base, err := service.trees.ResolveExactTree(ctx, capsule)
	if err != nil {
		return PatchFileReviewResult{}, err
	}
	base, err = repository.ParseTree(base)
	if err != nil || base.TreeHash != patch.BaseTreeHash {
		return PatchFileReviewResult{}, fmt.Errorf("%w: patch file base tree", ErrEvidenceIntegrity)
	}
	// Validate all operations and the proposed tree before disclosing even one
	// file. A partially valid or drifted patch is never reviewable.
	if _, err := ApplyPlatformPatch(base, patch); err != nil {
		return PatchFileReviewResult{}, fmt.Errorf("%w: patch file proposed tree: %v", ErrEvidenceIntegrity, err)
	}

	result := PatchFileReviewResult{
		Attempt: evidence.Attempt, PatchContentHash: patch.ContentHash,
		Operation: operation, Path: path, Side: side,
	}
	var file repository.TreeFile
	if side == PatchFileProposed {
		if operation.Kind == repository.OperationUpsert {
			file = repository.TreeFile{
				Path: operation.Path, Mode: operation.Mode,
				ContentHash: operation.ContentHash, ByteSize: operation.ByteSize,
			}
			result.Exists = true
		}
	} else {
		for _, candidate := range base.Files {
			if candidate.Path == path {
				file, result.Exists = candidate, true
				break
			}
		}
	}
	if result.Exists {
		result.Mode, result.ContentHash, result.ByteSize = file.Mode, file.ContentHash, file.ByteSize
		pointer, value, resolveErr := service.files.Resolve(
			ctx, patch.ProjectID, file.ContentHash, file.ByteSize,
		)
		if resolveErr != nil {
			return PatchFileReviewResult{}, fmt.Errorf("%w: resolve declared patch file: %v", ErrEvidenceIntegrity, resolveErr)
		}
		if !validPatchFilePointer(pointer, patch.ProjectID, file.ContentHash, file.ByteSize) ||
			int64(len(value)) != file.ByteSize || rawPatchFileHash(value) != file.ContentHash {
			return PatchFileReviewResult{}, fmt.Errorf("%w: declared patch file blob drifted", ErrEvidenceIntegrity)
		}
		result.Value = append([]byte(nil), value...)
	}
	representationHash, err := domain.CanonicalHash(struct {
		AttemptID        string                   `json:"attemptId"`
		PatchContentHash string                   `json:"patchContentHash"`
		Operation        repository.FileOperation `json:"operation"`
		Path             string                   `json:"path"`
		Side             PatchFileSide            `json:"side"`
		Exists           bool                     `json:"exists"`
		Mode             string                   `json:"mode"`
		ContentHash      string                   `json:"contentHash"`
		ByteSize         int64                    `json:"byteSize"`
	}{
		AttemptID: result.Attempt.ID, PatchContentHash: result.PatchContentHash,
		Operation: result.Operation, Path: result.Path, Side: result.Side,
		Exists: result.Exists, Mode: result.Mode,
		ContentHash: result.ContentHash, ByteSize: result.ByteSize,
	})
	if err != nil {
		return PatchFileReviewResult{}, fmt.Errorf("%w: hash patch file representation", ErrEvidenceIntegrity)
	}
	result.RepresentationHash = "sha256:" + representationHash
	return result, nil
}

func validPatchFilePointer(
	pointer repository.FileBlobPointer,
	projectID, contentHash string,
	byteSize int64,
) bool {
	return validUUIDs(projectID, pointer.OwnerID) && pointer.Store == repository.FileContentStore &&
		pointer.Ref != "" && pointer.Ref == strings.TrimSpace(pointer.Ref) && len(pointer.Ref) <= 512 &&
		pointer.ContentHash == contentHash && pointer.ByteSize == byteSize &&
		sha256Pattern.MatchString(pointer.ContentObjectHash)
}

func rawPatchFileHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
