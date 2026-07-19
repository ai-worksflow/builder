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
)

const downstreamDocumentJobType = "document.downstream.generate"

var downstreamDocumentKinds = map[string]struct{}{
	"project_brief": {}, "product_requirements": {}, "decision_record": {},
	"glossary_policy": {}, "reference_source": {}, "change_request": {},
	"api_contract": {}, "data_contract": {}, "permission_contract": {},
}

type GenerateDownstreamDocumentInput struct {
	SourceRevision core.VersionRef `json:"sourceRevision"`
	TargetKind     string          `json:"targetKind"`
	TargetTitle    string          `json:"targetTitle"`
	TargetKey      string          `json:"targetKey,omitempty"`
	Instruction    string          `json:"instruction"`
	Model          string          `json:"model,omitempty"`
	CommandKey     string          `json:"-"`
}

type DownstreamDocumentGeneration struct {
	Document         core.VersionedArtifact `json:"document"`
	Manifest         domain.InputManifest   `json:"inputManifest"`
	Proposal         domain.OutputProposal  `json:"proposal"`
	Provider         string                 `json:"provider"`
	Model            string                 `json:"model"`
	CommandID        string                 `json:"commandId"`
	Replayed         bool                   `json:"replayed,omitempty"`
	ResolvedOwnerIDs []string               `json:"resolvedOwnerIds"`
}

func (s *Service) GenerateDownstreamDocument(
	ctx context.Context,
	projectID, actorID string,
	input GenerateDownstreamDocumentInput,
) (result DownstreamDocumentGeneration, resultErr error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, core.ActionEdit); err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	input.TargetKind = strings.TrimSpace(input.TargetKind)
	input.TargetTitle = strings.TrimSpace(input.TargetTitle)
	input.TargetKey = strings.TrimSpace(input.TargetKey)
	input.Instruction = strings.TrimSpace(input.Instruction)
	input.Model = strings.TrimSpace(input.Model)
	input.CommandKey = strings.TrimSpace(input.CommandKey)
	if _, allowed := downstreamDocumentKinds[input.TargetKind]; !allowed || input.TargetTitle == "" ||
		len(input.TargetTitle) > 240 || input.Instruction == "" || len(input.Instruction) > 8000 ||
		input.CommandKey == "" || len(input.CommandKey) > 200 || input.SourceRevision.AnchorID != nil {
		return DownstreamDocumentGeneration{}, fmt.Errorf("%w: downstream document request", core.ErrInvalidInput)
	}
	if _, _, err := s.requireApprovedDocumentRevision(ctx, projectID, input.SourceRevision); err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	sourceBindings, err := s.GetMemberBindings(ctx, input.SourceRevision.ArtifactID, actorID)
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	candidateOwnerIDs := resolveDownstreamOwnerIDs(sourceBindings, actorID)
	requestHash, err := downstreamRequestHash(projectID, actorID, input)
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	claim, err := s.claimDownstreamCommand(
		ctx, projectID, actorID, input.CommandKey, requestHash, sourceBindings.ETag, candidateOwnerIDs,
	)
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	if !claim.Acquired {
		return s.loadCompletedDownstreamGeneration(ctx, claim.Command, actorID)
	}
	command := claim.Command
	resolvedOwnerIDs, err := resolvedOwnersFromCommand(command)
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	completed := false
	defer func() {
		if !completed {
			_ = s.failDownstreamCommand(context.Background(), command, input.SourceRevision, resultErr)
		}
	}()
	targetArtifactID := uuid.NewSHA1(command.ID, []byte("downstream-target-artifact"))
	documentContentKind, ok := downstreamDocumentContentKind(input.TargetKind)
	if !ok {
		return DownstreamDocumentGeneration{}, core.ErrInvalidInput
	}
	scaffold, err := downstreamDocumentScaffold(input.TargetKind, documentContentKind, input.SourceRevision)
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	document, err := s.artifacts.Get(ctx, targetArtifactID.String(), actorID, true)
	if errors.Is(err, core.ErrNotFound) {
		document, err = s.artifacts.CreateWithID(ctx, projectID, actorID, targetArtifactID.String(), core.CreateArtifactInput{
			Kind: input.TargetKind, ArtifactKey: input.TargetKey, Title: input.TargetTitle,
			SchemaVersion: 1, Content: scaffold,
			SourceVersions: []core.ArtifactSourceInput{{
				Ref: input.SourceRevision, Purpose: "downstream_source", Required: true,
			}},
		})
	}
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	if document.Artifact.ProjectID != projectID || document.Artifact.CreatedBy != actorID ||
		document.Artifact.Kind != input.TargetKind || document.Artifact.Title != input.TargetTitle {
		return DownstreamDocumentGeneration{}, core.ErrConflict
	}
	var baseRevision core.ArtifactRevision
	if document.LatestRevision != nil {
		baseRevision = *document.LatestRevision
	} else {
		if document.Draft == nil {
			return DownstreamDocumentGeneration{}, core.ErrConflict
		}
		baseRevision, err = s.artifacts.CreateRevision(
			ctx, document.Artifact.ID, actorID, document.Draft.ETag,
			core.CreateRevisionInput{ChangeSummary: "Create downstream document scaffold", ChangeSource: "system"},
		)
		if err != nil {
			return DownstreamDocumentGeneration{}, err
		}
		document, err = s.artifacts.Get(ctx, document.Artifact.ID, actorID, true)
		if err != nil {
			return DownstreamDocumentGeneration{}, err
		}
	}
	if baseRevision.ArtifactID != document.Artifact.ID || baseRevision.RevisionNumber != 1 ||
		!downstreamRevisionPinsSource(baseRevision, input.SourceRevision) {
		return DownstreamDocumentGeneration{}, core.ErrConflict
	}
	if command.TargetArtifactID != nil && *command.TargetArtifactID != targetArtifactID {
		return DownstreamDocumentGeneration{}, core.ErrConflict
	}
	if command.BaseRevisionID != nil && command.BaseRevisionID.String() != baseRevision.ID {
		return DownstreamDocumentGeneration{}, core.ErrConflict
	}
	baseRevisionID := uuid.MustParse(baseRevision.ID)
	command, err = s.checkpointDownstreamCommand(ctx, command, map[string]any{
		"target_artifact_id": targetArtifactID, "base_revision_id": baseRevisionID,
	})
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	initialBindings, err := s.GetMemberBindings(ctx, document.Artifact.ID, actorID)
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	targetBindings := downstreamTargetBindings(resolvedOwnerIDs, actorID)
	if initialBindings.Version == 0 {
		if _, err := s.ReplaceMemberBindings(ctx, document.Artifact.ID, actorID, initialBindings.ETag, targetBindings); err != nil {
			return DownstreamDocumentGeneration{}, err
		}
	} else if !bindingSetHasOwners(initialBindings, resolvedOwnerIDs) {
		return DownstreamDocumentGeneration{}, core.ErrConflict
	}
	constraints, err := json.Marshal(map[string]any{
		"instruction":       input.Instruction,
		"generationCommand": map[string]any{"id": command.ID.String(), "requestHash": requestHash},
		"sourceMemberBindings": map[string]any{
			"etag": command.SourceBindingsETag, "resolvedOwnerIds": resolvedOwnerIDs,
		},
		"downstreamDocument": map[string]any{
			"sourceRevision": input.SourceRevision, "targetKind": input.TargetKind,
			"targetArtifactId": document.Artifact.ID,
		},
	})
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	baseRef := core.VersionRef{ArtifactID: baseRevision.ArtifactID, RevisionID: baseRevision.ID, ContentHash: baseRevision.ContentHash}
	manifestID := uuid.NewSHA1(command.ID, []byte("downstream-input-manifest"))
	if command.InputManifestID != nil && *command.InputManifestID != manifestID {
		return DownstreamDocumentGeneration{}, core.ErrConflict
	}
	manifest, err := s.proposals.GetManifest(ctx, manifestID.String(), actorID)
	if errors.Is(err, core.ErrNotFound) {
		manifest, err = s.proposals.CreateManifestWithID(ctx, projectID, actorID, manifestID.String(), core.CreateManifestInput{
			JobType: downstreamDocumentJobType, BaseRevision: &baseRef,
			Sources:     []core.ManifestSourceInput{{Ref: input.SourceRevision, Purpose: "upstream_document"}},
			Constraints: constraints, OutputSchemaVersion: "document.patch.v1",
		})
	}
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	if manifest.BaseRevision == nil || manifest.BaseRevision.ArtifactID != baseRef.ArtifactID ||
		manifest.BaseRevision.RevisionID != baseRef.RevisionID || manifest.BaseRevision.ContentHash != baseRef.ContentHash {
		return DownstreamDocumentGeneration{}, core.ErrConflict
	}
	command, err = s.checkpointDownstreamCommand(ctx, command, map[string]any{"input_manifest_id": manifestID})
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	persistedProposal, err := s.findProposalForManifest(ctx, manifestID, actorID)
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	if persistedProposal == nil {
		command, err = s.checkpointDownstreamCommand(ctx, command, map[string]any{"model": input.Model})
		if err != nil {
			return DownstreamDocumentGeneration{}, err
		}
		generated, generateErr := s.generator.GenerateArtifactProposal(ctx, manifest.ID, actorID, input.Model)
		if generateErr != nil {
			return DownstreamDocumentGeneration{}, generateErr
		}
		persistedProposal, err = s.findProposalForManifest(ctx, manifestID, actorID)
		if err != nil {
			return DownstreamDocumentGeneration{}, err
		}
		if persistedProposal == nil || persistedProposal.Proposal.ID != generated.Proposal.ID ||
			persistedProposal.Provider != generated.Provider || persistedProposal.Model != generated.Model {
			return DownstreamDocumentGeneration{}, core.ErrConflict
		}
	}
	proposalID := uuid.MustParse(persistedProposal.Proposal.ID)
	targetRef := core.VersionRef{
		ArtifactID: baseRevision.ArtifactID, RevisionID: baseRevision.ID, ContentHash: baseRevision.ContentHash,
	}
	command, err = s.completeDownstreamCommand(
		ctx, command, input.SourceRevision, targetRef, manifestID, proposalID,
		persistedProposal.Provider, persistedProposal.Model, resolvedOwnerIDs,
	)
	if err != nil {
		return DownstreamDocumentGeneration{}, err
	}
	completed = true
	return DownstreamDocumentGeneration{
		Document: document, Manifest: manifest, Proposal: persistedProposal.Proposal,
		Provider: command.Provider, Model: command.Model, CommandID: command.ID.String(),
		ResolvedOwnerIDs: resolvedOwnerIDs,
	}, nil
}

func downstreamDocumentScaffold(
	artifactKind, contentKind string,
	source core.VersionRef,
) (json.RawMessage, error) {
	var value any
	switch artifactKind {
	case "api_contract":
		value = map[string]any{
			"schemaVersion": "api-contract/v1", "openapi": "3.1.0",
			"info": map[string]any{
				"title": "Generated API contract draft", "version": "0.0.0-draft",
				"description": "Structurally valid placeholder; replace through the governed Proposal before review.",
			},
			"paths": map[string]any{
				"/__pending_contract_review": map[string]any{
					"get": map[string]any{
						"operationId": "reviewPendingContract",
						"responses": map[string]any{"501": map[string]any{
							"description": "The generated contract has not been reviewed.",
						}},
					},
				},
			},
		}
	case "data_contract":
		value = map[string]any{
			"schemaVersion": "data-contract/v1",
			"entities": []any{map[string]any{
				"id": "PendingContractReview", "tableName": "pending_contract_review",
				"fields": []any{map[string]any{
					"id": "id", "name": "id", "type": "uuid", "nullable": false,
				}},
				"primaryKey": []string{"id"}, "indexes": []any{},
				"tenantScope": map[string]any{"mode": "global"},
			}},
			"migrationPolicy": map[string]any{
				"tool": "pending", "directory": "migrations",
				"applyCommandId": "migrate", "rollbackPolicy": "required",
			},
		}
	case "permission_contract":
		value = map[string]any{
			"schemaVersion": "permission-contract/v1",
			"identity":      map[string]any{"subjectClaim": "sub", "authentication": "session"},
			"tenant":        map[string]any{"mode": "project", "claim": "project_id"},
			"roles":         []any{map[string]any{"id": "PendingReviewer"}},
			"policies": []any{map[string]any{
				"id": "ReviewPendingContract", "roles": []string{"PendingReviewer"},
				"resource": "pending_contract_review", "actions": []string{"review"},
				"tenantScoped": true, "effect": "allow",
			}},
		}
	default:
		value = map[string]any{
			"schemaVersion":      1,
			"kind":               contentKind,
			"summary":            "",
			"blocks":             []any{},
			"acceptanceCriteria": []any{},
			"requirements":       []any{},
			"openQuestions":      []any{},
			"assumptions":        []any{},
			"generation": map[string]any{
				"state": "awaiting_reviewable_proposal", "sourceRevision": source,
			},
		}
	}
	payload, err := json.Marshal(value)
	return json.RawMessage(payload), err
}

func downstreamDocumentContentKind(artifactKind string) (string, bool) {
	kinds := map[string]string{
		"project_brief": "projectBrief", "product_requirements": "requirement",
		"decision_record": "decisionLog", "glossary_policy": "glossaryPolicy",
		"reference_source": "backendDevelopment", "change_request": "changeRequest",
		"api_contract": "apiContract", "data_contract": "dataContract",
		"permission_contract": "permissionContract",
	}
	value, exists := kinds[artifactKind]
	return value, exists
}

func downstreamRevisionPinsSource(revision core.ArtifactRevision, source core.VersionRef) bool {
	for _, pinned := range revision.SourceVersions {
		if pinned.ArtifactID == source.ArtifactID && pinned.RevisionID == source.RevisionID &&
			pinned.ContentHash == source.ContentHash && pinned.Purpose == "downstream_source" && pinned.Required {
			return true
		}
	}
	return false
}

func resolveDownstreamOwnerIDs(bindings DocumentMemberBindingSet, actorID string) []string {
	owners := make([]string, 0)
	seen := make(map[string]struct{})
	for _, binding := range bindings.Items {
		if binding.Role != DocumentDownstreamOwner {
			continue
		}
		if _, exists := seen[binding.UserID]; exists {
			continue
		}
		seen[binding.UserID] = struct{}{}
		owners = append(owners, binding.UserID)
	}
	if len(owners) == 0 {
		owners = append(owners, actorID)
	}
	sort.Strings(owners)
	return owners
}

func downstreamTargetBindings(ownerIDs []string, actorID string) []DocumentMemberBindingInput {
	items := make([]DocumentMemberBindingInput, 0, len(ownerIDs)+1)
	actorIsOwner := false
	for _, ownerID := range ownerIDs {
		items = append(items, DocumentMemberBindingInput{
			UserID: ownerID, Role: DocumentOwner, Reason: "Resolved from the upstream document downstream owner",
		})
		actorIsOwner = actorIsOwner || ownerID == actorID
	}
	if !actorIsOwner {
		items = append(items, DocumentMemberBindingInput{
			UserID: actorID, Role: DocumentAssignee, Reason: "Initiated downstream document generation",
		})
	}
	return items
}

func bindingSetHasOwners(bindings DocumentMemberBindingSet, ownerIDs []string) bool {
	actual := make(map[string]struct{})
	for _, binding := range bindings.Items {
		if binding.Role == DocumentOwner {
			actual[binding.UserID] = struct{}{}
		}
	}
	for _, ownerID := range ownerIDs {
		if _, exists := actual[ownerID]; !exists {
			return false
		}
	}
	return true
}

func (s *Service) requireApprovedDocumentRevision(
	ctx context.Context,
	projectID string,
	reference core.VersionRef,
) (storage.ArtifactModel, storage.ArtifactRevisionModel, error) {
	projectUUID, err := uuid.Parse(strings.TrimSpace(projectID))
	if err != nil {
		return storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, fmt.Errorf("%w: project id", core.ErrInvalidInput)
	}
	artifactID, err := uuid.Parse(strings.TrimSpace(reference.ArtifactID))
	if err != nil {
		return storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, fmt.Errorf("%w: source artifact id", core.ErrInvalidInput)
	}
	revisionID, err := uuid.Parse(strings.TrimSpace(reference.RevisionID))
	if err != nil || !strings.HasPrefix(reference.ContentHash, "sha256:") {
		return storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, fmt.Errorf("%w: source revision", core.ErrInvalidInput)
	}
	var artifact storage.ArtifactModel
	if err := s.database.WithContext(ctx).Where(
		"id = ? AND project_id = ? AND lifecycle = ?", artifactID, projectUUID, "active",
	).Take(&artifact).Error; err != nil {
		return storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, mapCollaborationNotFound(err)
	}
	if _, documentKind := downstreamDocumentKinds[artifact.Kind]; !documentKind {
		return storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, fmt.Errorf("%w: source must be a document artifact", core.ErrInvalidInput)
	}
	var revision storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Where(
		"id = ? AND artifact_id = ? AND content_hash = ? AND workflow_status = ?",
		revisionID, artifact.ID, reference.ContentHash, "approved",
	).Take(&revision).Error; err != nil {
		return storage.ArtifactModel{}, storage.ArtifactRevisionModel{}, mapCollaborationNotFound(err)
	}
	return artifact, revision, nil
}
