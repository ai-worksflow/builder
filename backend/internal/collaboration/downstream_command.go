package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type downstreamCommandClaim struct {
	Command  storage.DocumentGenerationCommandModel
	Acquired bool
}

type persistedArtifactProposal struct {
	Proposal domain.OutputProposal
	Provider string
	Model    string
}

func downstreamRequestHash(projectID, actorID string, input GenerateDownstreamDocumentInput) (string, error) {
	hash, err := domain.CanonicalHash(struct {
		ProjectID      string          `json:"projectId"`
		ActorID        string          `json:"actorId"`
		SourceRevision core.VersionRef `json:"sourceRevision"`
		TargetKind     string          `json:"targetKind"`
		TargetTitle    string          `json:"targetTitle"`
		TargetKey      string          `json:"targetKey,omitempty"`
		Instruction    string          `json:"instruction"`
		Model          string          `json:"model,omitempty"`
	}{
		ProjectID: projectID, ActorID: actorID, SourceRevision: input.SourceRevision,
		TargetKind: input.TargetKind, TargetTitle: input.TargetTitle, TargetKey: input.TargetKey,
		Instruction: input.Instruction, Model: strings.TrimSpace(input.Model),
	})
	if err != nil {
		return "", err
	}
	return "sha256:" + hash, nil
}

func (s *Service) claimDownstreamCommand(
	ctx context.Context,
	projectID, actorID, commandKey, requestHash, sourceBindingsETag string,
	resolvedOwnerIDs []string,
) (downstreamCommandClaim, error) {
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return downstreamCommandClaim{}, fmt.Errorf("%w: project id", core.ErrInvalidInput)
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return downstreamCommandClaim{}, fmt.Errorf("%w: actor id", core.ErrInvalidInput)
	}
	now := s.now().UTC()
	lockedUntil := now.Add(s.commandLease)
	encodedOwners, err := json.Marshal(resolvedOwnerIDs)
	if err != nil {
		return downstreamCommandClaim{}, err
	}
	candidate := storage.DocumentGenerationCommandModel{
		ID: uuid.New(), ProjectID: projectUUID, ActorID: actorUUID,
		CommandKey: commandKey, RequestHash: requestHash, Status: "processing",
		SourceBindingsETag: sourceBindingsETag, ResolvedOwnerIDs: encodedOwners,
		Provider: "", Model: "", AttemptCount: 1,
		LockedUntil: &lockedUntil, CreatedAt: now, UpdatedAt: now,
	}
	claim := downstreamCommandClaim{}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		lockScope := projectUUID.String() + ":" + actorUUID.String() + ":" + commandKey
		if err := transaction.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", lockScope).Error; err != nil {
			return err
		}
		var current storage.DocumentGenerationCommandModel
		loadErr := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"project_id = ? AND actor_id = ? AND command_key = ?", projectUUID, actorUUID, commandKey,
		).Take(&current).Error
		if errors.Is(loadErr, gorm.ErrRecordNotFound) {
			var memberCount int64
			if err := transaction.Model(&storage.ProjectMemberModel{}).
				Where("project_id = ? AND user_id IN ?", projectUUID, resolvedOwnerIDs).Count(&memberCount).Error; err != nil {
				return err
			}
			if memberCount != int64(len(resolvedOwnerIDs)) {
				return fmt.Errorf("%w: downstream owners must be project members", core.ErrInvalidInput)
			}
			if err := transaction.Create(&candidate).Error; err != nil {
				return err
			}
			claim = downstreamCommandClaim{Command: candidate, Acquired: true}
			return nil
		}
		if loadErr != nil {
			return loadErr
		}
		if current.RequestHash != requestHash {
			return fmt.Errorf("%w: generation command key was used for a different request", core.ErrConflict)
		}
		if current.Status == "completed" {
			claim = downstreamCommandClaim{Command: current}
			return nil
		}
		if current.Status != "processing" {
			return core.ErrConflict
		}
		if current.LockedUntil != nil && current.LockedUntil.After(now) {
			return fmt.Errorf("%w: generation command is already processing", core.ErrConflict)
		}
		update := transaction.Model(&storage.DocumentGenerationCommandModel{}).
			Where("id = ? AND request_hash = ? AND status = 'processing'", current.ID, requestHash).
			Updates(map[string]any{
				"attempt_count": gorm.Expr("attempt_count + 1"),
				"locked_until":  lockedUntil,
				"updated_at":    now,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return core.ErrConflict
		}
		current.LockedUntil = &lockedUntil
		current.AttemptCount++
		current.UpdatedAt = now
		claim = downstreamCommandClaim{Command: current, Acquired: true}
		return nil
	})
	return claim, err
}

func resolvedOwnersFromCommand(command storage.DocumentGenerationCommandModel) ([]string, error) {
	var values []string
	if json.Unmarshal(command.ResolvedOwnerIDs, &values) != nil || len(values) == 0 {
		return nil, core.ErrConflict
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		parsed, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil {
			return nil, core.ErrConflict
		}
		values[index] = parsed.String()
		if _, duplicate := seen[values[index]]; duplicate {
			return nil, core.ErrConflict
		}
		seen[values[index]] = struct{}{}
	}
	sort.Strings(values)
	return values, nil
}

func (s *Service) checkpointDownstreamCommand(
	ctx context.Context,
	command storage.DocumentGenerationCommandModel,
	values map[string]any,
) (storage.DocumentGenerationCommandModel, error) {
	now := s.now().UTC()
	lockedUntil := now.Add(s.commandLease)
	values["locked_until"] = lockedUntil
	values["updated_at"] = now
	update := s.database.WithContext(ctx).Model(&storage.DocumentGenerationCommandModel{}).
		Where(
			"id = ? AND request_hash = ? AND status = 'processing' AND attempt_count = ?",
			command.ID, command.RequestHash, command.AttemptCount,
		).
		Updates(values)
	if update.Error != nil {
		return storage.DocumentGenerationCommandModel{}, update.Error
	}
	if update.RowsAffected != 1 {
		return storage.DocumentGenerationCommandModel{}, core.ErrConflict
	}
	if err := s.database.WithContext(ctx).Where("id = ?", command.ID).Take(&command).Error; err != nil {
		return storage.DocumentGenerationCommandModel{}, err
	}
	return command, nil
}

func (s *Service) completeDownstreamCommand(
	ctx context.Context,
	command storage.DocumentGenerationCommandModel,
	sourceRevision, targetRevision core.VersionRef,
	manifestID, proposalID uuid.UUID,
	provider, model string,
	resolvedOwnerIDs []string,
) (storage.DocumentGenerationCommandModel, error) {
	artifactID, err := uuid.Parse(targetRevision.ArtifactID)
	if err != nil {
		return storage.DocumentGenerationCommandModel{}, core.ErrConflict
	}
	revisionID, err := uuid.Parse(targetRevision.RevisionID)
	if err != nil {
		return storage.DocumentGenerationCommandModel{}, core.ErrConflict
	}
	now := s.now().UTC()
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	payload := map[string]any{
		"projectId": command.ProjectID.String(), "artifactId": artifactID.String(),
		"commandId": command.ID.String(), "attempt": command.AttemptCount,
		"sourceRevision": sourceRevision, "targetRevision": targetRevision,
		"inputManifestId": manifestID.String(), "outputProposalId": proposalID.String(),
		"resolvedOwnerIds": resolvedOwnerIDs, "provider": provider, "model": model,
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		update := transaction.Model(&storage.DocumentGenerationCommandModel{}).
			Where(
				"id = ? AND request_hash = ? AND status = 'processing' AND attempt_count = ?",
				command.ID, command.RequestHash, command.AttemptCount,
			).
			Updates(map[string]any{
				"status": "completed", "target_artifact_id": artifactID, "base_revision_id": revisionID,
				"input_manifest_id": manifestID, "output_proposal_id": proposalID,
				"provider": provider, "model": model, "locked_until": nil, "updated_at": now,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return core.ErrConflict
		}
		if err := collaborationAudit(
			transaction, command.ProjectID, command.ActorID, "document.downstream_generated",
			"document_generation_command", command.ID.String(), payload,
		); err != nil {
			return err
		}
		return collaborationOutbox(
			transaction, "document_generation_command", command.ID.String(),
			"document.downstream_generated", "worksflow.document.downstream.generated", payload,
		)
	})
	if err != nil {
		return storage.DocumentGenerationCommandModel{}, err
	}
	if err := s.database.WithContext(ctx).Where("id = ?", command.ID).Take(&command).Error; err != nil {
		return storage.DocumentGenerationCommandModel{}, err
	}
	return command, nil
}

func (s *Service) failDownstreamCommand(
	ctx context.Context,
	command storage.DocumentGenerationCommandModel,
	sourceRevision core.VersionRef,
	cause error,
) error {
	now := s.now().UTC()
	failureClass := downstreamFailureClass(cause)
	payload := map[string]any{
		"projectId": command.ProjectID.String(), "commandId": command.ID.String(),
		"attempt": command.AttemptCount, "failureClass": failureClass,
		"sourceRevision": sourceRevision,
	}
	if command.TargetArtifactID != nil {
		payload["artifactId"] = command.TargetArtifactID.String()
		payload["targetArtifactId"] = command.TargetArtifactID.String()
	}
	if command.BaseRevisionID != nil {
		payload["baseRevisionId"] = command.BaseRevisionID.String()
	}
	if command.InputManifestID != nil {
		payload["inputManifestId"] = command.InputManifestID.String()
	}
	return s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		update := transaction.Model(&storage.DocumentGenerationCommandModel{}).
			Where(
				"id = ? AND request_hash = ? AND status = 'processing' AND attempt_count = ?",
				command.ID, command.RequestHash, command.AttemptCount,
			).
			Updates(map[string]any{
				"last_failure": failureClass, "last_failed_at": now,
				"locked_until": nil, "updated_at": now,
			})
		if update.Error != nil {
			return update.Error
		}
		// A zero-row update means a newer recovery attempt already owns the
		// command. The stale worker must not publish a misleading failure.
		if update.RowsAffected == 0 {
			return nil
		}
		if err := collaborationAudit(
			transaction, command.ProjectID, command.ActorID, "document.downstream_generation_failed",
			"document_generation_command", command.ID.String(), payload,
		); err != nil {
			return err
		}
		return collaborationOutbox(
			transaction, "document_generation_command", command.ID.String(),
			"document.downstream_generation_failed", "worksflow.document.downstream.generation_failed", payload,
		)
	})
}

func downstreamFailureClass(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, core.ErrNotFound):
		return "not_found"
	case errors.Is(err, core.ErrForbidden):
		return "forbidden"
	case errors.Is(err, core.ErrConflict):
		return "conflict"
	case errors.Is(err, core.ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, core.ErrProposalStale):
		return "proposal_stale"
	case errors.Is(err, core.ErrBlockingGate):
		return "blocking_gate"
	case errors.Is(err, core.ErrContentNotReady):
		return "content_not_ready"
	default:
		return "internal"
	}
}

func (s *Service) loadCompletedDownstreamGeneration(
	ctx context.Context,
	command storage.DocumentGenerationCommandModel,
	actorID string,
) (DownstreamDocumentGeneration, error) {
	if command.Status != "completed" || command.TargetArtifactID == nil || command.BaseRevisionID == nil ||
		command.InputManifestID == nil || command.OutputProposalID == nil {
		return DownstreamDocumentGeneration{}, core.ErrConflict
	}
	resolvedOwnerIDs, err := resolvedOwnersFromCommand(command)
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	document, err := s.artifacts.Get(ctx, command.TargetArtifactID.String(), actorID, true)
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	baseRevision, err := s.artifacts.GetRevision(ctx, command.BaseRevisionID.String(), actorID)
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	manifest, err := s.proposals.GetManifest(ctx, command.InputManifestID.String(), actorID)
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	proposal, err := s.proposals.GetProposal(ctx, command.OutputProposalID.String(), actorID)
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	if baseRevision.ArtifactID != document.Artifact.ID || manifest.BaseRevision == nil ||
		manifest.BaseRevision.ArtifactID != baseRevision.ArtifactID ||
		manifest.BaseRevision.RevisionID != baseRevision.ID ||
		manifest.BaseRevision.ContentHash != baseRevision.ContentHash ||
		manifest.ID != proposal.Manifest.ID || proposal.ArtifactID != document.Artifact.ID ||
		proposal.BaseRevision.ArtifactID != baseRevision.ArtifactID ||
		proposal.BaseRevision.RevisionID != baseRevision.ID ||
		proposal.BaseRevision.ContentHash != baseRevision.ContentHash {
		return DownstreamDocumentGeneration{}, core.ErrConflict
	}
	// Rebuild from the command's immutable base rather than assuming it remains
	// the artifact's latest revision after the proposal has been reviewed or
	// later edits have advanced the document.
	document.Draft = nil
	document.LatestRevision = &baseRevision
	document.ApprovedRevision = nil
	return DownstreamDocumentGeneration{
		Document: document, Manifest: manifest, Proposal: proposal,
		Provider: command.Provider, Model: command.Model, CommandID: command.ID.String(), Replayed: true,
		ResolvedOwnerIDs: resolvedOwnerIDs,
	}, nil
}

func (s *Service) findProposalForManifest(
	ctx context.Context,
	manifestID uuid.UUID,
	actorID string,
) (*persistedArtifactProposal, error) {
	var models []storage.OutputProposalModel
	if err := s.database.WithContext(ctx).Where("input_manifest_id = ?", manifestID).
		Order("created_at ASC, id ASC").Limit(2).Find(&models).Error; err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, nil
	}
	if len(models) != 1 {
		return nil, core.ErrConflict
	}
	if models[0].AIProvider == nil || strings.TrimSpace(*models[0].AIProvider) == "" ||
		models[0].AIModel == nil || strings.TrimSpace(*models[0].AIModel) == "" {
		return nil, fmt.Errorf("%w: generated proposal is missing persisted provider identity", core.ErrConflict)
	}
	proposal, err := s.proposals.GetProposal(ctx, models[0].ID.String(), actorID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, core.ErrNotFound
		}
		return nil, err
	}
	return &persistedArtifactProposal{
		Proposal: proposal, Provider: strings.TrimSpace(*models[0].AIProvider), Model: strings.TrimSpace(*models[0].AIModel),
	}, nil
}
