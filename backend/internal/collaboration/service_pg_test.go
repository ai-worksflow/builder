package collaboration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/generation"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"github.com/worksflow/builder/backend/internal/testsupport"
	"github.com/worksflow/builder/backend/migrations"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type collaborationMemoryContentStore struct {
	mu       sync.Mutex
	sequence uint64
	items    map[string]content.StoredContent
}

func newCollaborationMemoryContentStore() *collaborationMemoryContentStore {
	return &collaborationMemoryContentStore{items: make(map[string]content.StoredContent)}
}

func (s *collaborationMemoryContentStore) PutPending(
	_ context.Context,
	projectID, aggregateType, aggregateID string,
	schemaVersion int,
	payload json.RawMessage,
) (content.Reference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	id := fmt.Sprintf("collaboration-content-%d", s.sequence)
	reference := collaborationContentReference(id, schemaVersion, payload)
	s.items[id] = content.StoredContent{
		Reference: reference, ProjectID: projectID, AggregateType: aggregateType, AggregateID: aggregateID,
		State: content.StatePending, Payload: append(json.RawMessage(nil), payload...), CreatedAt: time.Now().UTC(),
	}
	return reference, nil
}

func (s *collaborationMemoryContentStore) Finalize(_ context.Context, contentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.items[contentID]
	if !exists {
		return content.ErrContentNotFound
	}
	now := time.Now().UTC()
	stored.State, stored.FinalizedAt = content.StateFinalized, &now
	s.items[contentID] = stored
	return nil
}

func (s *collaborationMemoryContentStore) Abort(_ context.Context, contentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.items[contentID]
	if !exists {
		return content.ErrContentNotFound
	}
	stored.State = content.StateAborted
	s.items[contentID] = stored
	return nil
}

func (s *collaborationMemoryContentStore) Get(
	_ context.Context,
	contentID, expectedHash string,
) (content.StoredContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.items[contentID]
	if !exists || stored.State == content.StateAborted {
		return content.StoredContent{}, content.ErrContentNotFound
	}
	if stored.ContentHash != expectedHash {
		return content.StoredContent{}, content.ErrHashMismatch
	}
	stored.Payload = append(json.RawMessage(nil), stored.Payload...)
	return stored, nil
}

func (s *collaborationMemoryContentStore) addFinalized(
	projectID, aggregateType, aggregateID string,
	payload json.RawMessage,
) content.Reference {
	reference, _ := s.PutPending(context.Background(), projectID, aggregateType, aggregateID, 1, payload)
	_ = s.Finalize(context.Background(), reference.ID)
	return reference
}

func collaborationContentReference(id string, schemaVersion int, payload json.RawMessage) content.Reference {
	digest := sha256.Sum256(payload)
	return content.Reference{
		ID: id, ContentHash: "sha256:" + hex.EncodeToString(digest[:]),
		ByteSize: int64(len(payload)), SchemaVersion: schemaVersion,
	}
}

type collaborationTestGenerator struct {
	proposals *core.ProposalService
	calls     atomic.Int32
	failure   error
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
}

func (g *collaborationTestGenerator) GenerateArtifactProposal(
	ctx context.Context,
	manifestID, actorID, requestedModel string,
) (generation.ArtifactGenerationResult, error) {
	g.calls.Add(1)
	if g.started != nil {
		g.startOnce.Do(func() { close(g.started) })
		select {
		case <-g.release:
		case <-ctx.Done():
			return generation.ArtifactGenerationResult{}, ctx.Err()
		}
	}
	if g.failure != nil {
		return generation.ArtifactGenerationResult{}, g.failure
	}
	manifest, err := g.proposals.GetManifest(ctx, manifestID, actorID)
	if err != nil {
		return generation.ArtifactGenerationResult{}, err
	}
	operation := domain.ProposalOperation{
		ID: "generated-summary", Kind: domain.OperationAdd, Path: "/implementationSummary",
		Value: json.RawMessage(`{"state":"generated"}`), Rationale: "PG collaboration fixture",
	}
	if manifest.JobType == downstreamDocumentJobType {
		var constraints struct {
			DownstreamDocument struct {
				TargetKind string `json:"targetKind"`
			} `json:"downstreamDocument"`
		}
		if err := json.Unmarshal(manifest.Constraints, &constraints); err != nil {
			return generation.ArtifactGenerationResult{}, err
		}
		operation.Kind, operation.Path = domain.OperationReplace, ""
		switch constraints.DownstreamDocument.TargetKind {
		case "api_contract":
			operation.Value = json.RawMessage(`{
				"schemaVersion":"api-contract/v1","openapi":"3.1.0",
				"info":{"title":"Generated API contract","version":"1.0.0","description":"Generated downstream summary"},
				"paths":{"/health":{"get":{"operationId":"getHealth","responses":{"200":{"description":"Healthy"}}}}}
			}`)
		case "data_contract":
			operation.Value = json.RawMessage(`{
				"schemaVersion":"data-contract/v1",
				"entities":[{"id":"Order","tableName":"orders","fields":[{"id":"id","name":"id","type":"uuid","nullable":false}],"primaryKey":["id"],"indexes":[],"tenantScope":{"mode":"global"}}],
				"migrationPolicy":{"tool":"sql","directory":"migrations","applyCommandId":"migrate","rollbackPolicy":"required"}
			}`)
		case "permission_contract":
			operation.Value = json.RawMessage(`{
				"schemaVersion":"permission-contract/v1",
				"identity":{"subjectClaim":"sub","authentication":"session"},
				"tenant":{"mode":"project","claim":"project_id"},
				"roles":[{"id":"Owner"}],
				"policies":[{"id":"ManageOrders","roles":["Owner"],"resource":"orders","actions":["read","write"],"tenantScoped":true,"effect":"allow"}]
			}`)
		default:
			operation.Path, operation.Value = "/summary", json.RawMessage(`"Generated downstream summary"`)
		}
	}
	model := strings.TrimSpace(requestedModel)
	if model == "" {
		model = "test-model"
	}
	proposal, err := g.proposals.CreateProposal(ctx, manifest.ProjectID, actorID, core.CreateProposalInput{
		ManifestID: manifest.ID, ArtifactID: manifest.BaseRevision.ArtifactID,
		Operations: []domain.ProposalOperation{operation}, AIProvider: "test-provider", AIModel: model,
	})
	if err != nil {
		return generation.ArtifactGenerationResult{}, err
	}
	return generation.ArtifactGenerationResult{Proposal: proposal, Provider: "test-provider", Model: model}, nil
}

type collaborationPostgresFixture struct {
	database   *gorm.DB
	contents   *collaborationMemoryContentStore
	service    *Service
	proposals  *core.ProposalService
	artifacts  *core.ArtifactService
	generator  *collaborationTestGenerator
	projectID  uuid.UUID
	ownerID    uuid.UUID
	editorID   uuid.UUID
	viewerID   uuid.UUID
	outsiderID uuid.UUID
	source     core.VersionRef
}

func TestDocumentBindingsAndDownstreamGenerationAreGovernedAndIdempotentPostgres(t *testing.T) {
	database, cleanup := collaborationPostgresDatabase(t)
	defer cleanup()
	fixture := seedCollaborationFixture(t, database, true)
	ctx := context.Background()

	initial, err := fixture.service.GetMemberBindings(ctx, fixture.source.ArtifactID, fixture.ownerID.String())
	if err != nil || initial.Version != 0 || len(initial.Items) != 1 ||
		initial.Items[0].Role != DocumentOwner || initial.Items[0].UserID != fixture.ownerID.String() {
		t.Fatalf("initial bindings = %+v, err=%v", initial, err)
	}
	if err := database.Create(&storage.ArtifactResponsibilityModel{
		ArtifactID: uuid.MustParse(fixture.source.ArtifactID), UserID: fixture.editorID,
		Responsibility: "reviewer", Reason: "Active review request", AssignedBy: fixture.ownerID, AssignedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	bindings, err := fixture.service.ReplaceMemberBindings(ctx, fixture.source.ArtifactID, fixture.ownerID.String(), initial.ETag, []DocumentMemberBindingInput{
		{UserID: fixture.ownerID.String(), Role: DocumentOwner},
		{UserID: fixture.ownerID.String(), Role: DocumentAssignee},
		{UserID: fixture.editorID.String(), Role: DocumentDownstreamOwner},
		{UserID: fixture.editorID.String(), Role: DocumentReviewer},
		{UserID: fixture.viewerID.String(), Role: DocumentWatcher},
	})
	if err != nil || bindings.Version != 1 || len(bindings.Items) != 5 {
		t.Fatalf("replace bindings = %+v, err=%v", bindings, err)
	}
	var preservedReviewers int64
	if err := database.Model(&storage.ArtifactResponsibilityModel{}).Where(
		"artifact_id = ? AND user_id = ? AND responsibility = 'reviewer'", fixture.source.ArtifactID, fixture.editorID,
	).Count(&preservedReviewers).Error; err != nil || preservedReviewers != 1 {
		t.Fatalf("active review responsibility was overwritten: count=%d err=%v", preservedReviewers, err)
	}
	if _, err := fixture.service.ReplaceMemberBindings(ctx, fixture.source.ArtifactID, fixture.ownerID.String(), initial.ETag, []DocumentMemberBindingInput{{
		UserID: fixture.ownerID.String(), Role: DocumentOwner,
	}}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("stale bindings ETag error = %v", err)
	}
	if _, err := fixture.service.ReplaceMemberBindings(ctx, fixture.source.ArtifactID, fixture.ownerID.String(), bindings.ETag, []DocumentMemberBindingInput{
		{UserID: fixture.ownerID.String(), Role: DocumentOwner},
		{UserID: fixture.outsiderID.String(), Role: DocumentReviewer},
	}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("non-member binding error = %v", err)
	}
	if _, err := fixture.service.ReplaceMemberBindings(ctx, fixture.source.ArtifactID, fixture.viewerID.String(), bindings.ETag, []DocumentMemberBindingInput{{
		UserID: fixture.ownerID.String(), Role: DocumentOwner,
	}}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("viewer binding mutation error = %v", err)
	}
	blueprint := seedApprovedCollaborationArtifact(
		t, database, fixture.contents, fixture.projectID, fixture.ownerID, "blueprint", "NON-DOCUMENT-SOURCE",
		"Blueprint is not a document source", json.RawMessage(`{"nodes":[]}`), nil,
	)
	if _, err := fixture.service.GenerateDownstreamDocument(ctx, fixture.projectID.String(), fixture.ownerID.String(), GenerateDownstreamDocumentInput{
		SourceRevision: blueprint, TargetKind: "api_contract", TargetTitle: "Rejected source",
		Instruction: "This must not run.", CommandKey: "non-document-source-0001",
	}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("Blueprint was accepted by document-only generation: %v", err)
	}
	if _, err := fixture.service.GenerateDownstreamDocument(ctx, fixture.projectID.String(), fixture.ownerID.String(), GenerateDownstreamDocumentInput{
		SourceRevision: fixture.source, TargetKind: "prototype_flow", TargetTitle: "Rejected target",
		Instruction: "This must not run.", CommandKey: "non-document-target-0001",
	}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("non-document target kind was accepted: %v", err)
	}

	input := GenerateDownstreamDocumentInput{
		SourceRevision: fixture.source, TargetKind: "api_contract", TargetTitle: "Generated API contract",
		Instruction: "Derive an exact reviewed API contract.", Model: "test-model", CommandKey: "downstream-command-0001",
	}
	firstResult := make(chan DownstreamDocumentGeneration, 1)
	firstError := make(chan error, 1)
	go func() {
		result, generateErr := fixture.service.GenerateDownstreamDocument(ctx, fixture.projectID.String(), fixture.ownerID.String(), input)
		firstResult <- result
		firstError <- generateErr
	}()
	select {
	case <-fixture.generator.started:
	case generateErr := <-firstError:
		t.Fatalf("first generation failed before provider: %v", generateErr)
	case <-time.After(5 * time.Second):
		t.Fatal("first generation did not reach the provider")
	}
	if _, err := fixture.service.GenerateDownstreamDocument(ctx, fixture.projectID.String(), fixture.ownerID.String(), input); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("concurrent command error = %v", err)
	}
	close(fixture.generator.release)
	created, err := <-firstResult, <-firstError
	if err != nil {
		t.Fatal(err)
	}
	if created.Proposal.Status != domain.ProposalOpen || created.Manifest.JobType != downstreamDocumentJobType ||
		created.CommandID == "" || created.Provider != "test-provider" || created.Model != "test-model" ||
		len(created.ResolvedOwnerIDs) != 1 || created.ResolvedOwnerIDs[0] != fixture.editorID.String() {
		t.Fatalf("downstream result = %+v", created)
	}
	if created.Document.LatestRevision == nil || !canonicalGeneratedDocumentContent(created.Document.LatestRevision.Content, "apiContract") {
		t.Fatalf("generated document is not editor-safe canonical content: %s", created.Document.LatestRevision.Content)
	}
	targetBindings, err := fixture.service.GetMemberBindings(ctx, created.Document.Artifact.ID, fixture.ownerID.String())
	if err != nil || !bindingSetHasOwners(targetBindings, []string{fixture.editorID.String()}) ||
		!bindingSetHasRole(targetBindings, fixture.ownerID.String(), DocumentAssignee) {
		t.Fatalf("resolved target bindings = %+v, err=%v", targetBindings, err)
	}
	if fixture.generator.calls.Load() != 1 {
		t.Fatalf("provider calls = %d", fixture.generator.calls.Load())
	}
	var generatedAuditCount, generatedOutboxCount int64
	if err := database.Model(&storage.AuditEventModel{}).Where(
		"project_id = ? AND action = ? AND target_id = ?",
		fixture.projectID, "document.downstream_generated", created.CommandID,
	).Count(&generatedAuditCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.OutboxEventModel{}).Where(
		"event_type = ? AND aggregate_id = ?", "document.downstream_generated", created.CommandID,
	).Count(&generatedOutboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if generatedAuditCount != 1 || generatedOutboxCount != 1 {
		t.Fatalf("downstream success audit/outbox counts = %d/%d", generatedAuditCount, generatedOutboxCount)
	}
	changedBindings, err := fixture.service.ReplaceMemberBindings(ctx, fixture.source.ArtifactID, fixture.ownerID.String(), bindings.ETag, []DocumentMemberBindingInput{
		{UserID: fixture.ownerID.String(), Role: DocumentOwner},
		{UserID: fixture.viewerID.String(), Role: DocumentDownstreamOwner},
	})
	if err != nil || changedBindings.ETag == bindings.ETag {
		t.Fatalf("change upstream downstreamOwner = %+v, err=%v", changedBindings, err)
	}
	replayed, err := fixture.service.GenerateDownstreamDocument(ctx, fixture.projectID.String(), fixture.ownerID.String(), input)
	if err != nil || !replayed.Replayed || replayed.CommandID != created.CommandID ||
		replayed.Document.Artifact.ID != created.Document.Artifact.ID || replayed.Proposal.ID != created.Proposal.ID ||
		len(replayed.ResolvedOwnerIDs) != 1 || replayed.ResolvedOwnerIDs[0] != fixture.editorID.String() {
		t.Fatalf("completed command replay = %+v, err=%v", replayed, err)
	}
	if fixture.generator.calls.Load() != 1 {
		t.Fatalf("replay called provider: %d", fixture.generator.calls.Load())
	}

	decided, err := fixture.proposals.Decide(ctx, created.Proposal.ID, fixture.ownerID.String(), core.DecideProposalInput{
		OperationID: created.Proposal.Operations[0].ID, Decision: domain.DecisionAccepted, Version: created.Proposal.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := fixture.proposals.Apply(ctx, created.Proposal.ID, fixture.ownerID.String(), core.ApplyProposalInput{Version: decided.Version})
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := fixture.artifacts.CreateRevision(ctx, created.Document.Artifact.ID, fixture.ownerID.String(), draft.ETag, core.CreateRevisionInput{
		ChangeSummary: "Advance generated document", ChangeSource: "human",
	})
	if err != nil {
		t.Fatal(err)
	}
	if advanced.ID == created.Proposal.BaseRevision.RevisionID {
		t.Fatal("fixture did not advance the generated document")
	}
	replayedAfterAdvance, err := fixture.service.GenerateDownstreamDocument(ctx, fixture.projectID.String(), fixture.ownerID.String(), input)
	if err != nil || replayedAfterAdvance.Document.LatestRevision == nil ||
		replayedAfterAdvance.Document.LatestRevision.ID != created.Proposal.BaseRevision.RevisionID {
		t.Fatalf("replay after document advance = %+v, err=%v", replayedAfterAdvance, err)
	}

	graph, err := fixture.service.GetDocumentGraph(ctx, fixture.projectID.String(), fixture.viewerID.String())
	if err != nil {
		t.Fatal(err)
	}
	if !graphHasNode(graph, "inputManifest", created.Manifest.ID) || !graphHasNode(graph, "outputProposal", created.Proposal.ID) ||
		!graphHasEdge(graph, "generated_output", "input_manifest:"+created.Manifest.ID, "output_proposal:"+created.Proposal.ID) ||
		!graphHasEdge(graph, "proposes_patch", "output_proposal:"+created.Proposal.ID, "artifact:"+created.Document.Artifact.ID) {
		t.Fatalf("AI input/output graph projection is incomplete: nodes=%+v edges=%+v", graph.Nodes, graph.Edges)
	}
	var artifactCount, proposalCount int64
	if err := database.Model(&storage.ArtifactModel{}).Where("project_id = ? AND title = ?", fixture.projectID, input.TargetTitle).Count(&artifactCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.OutputProposalModel{}).Where("input_manifest_id = ?", created.Manifest.ID).Count(&proposalCount).Error; err != nil {
		t.Fatal(err)
	}
	if artifactCount != 1 || proposalCount != 1 {
		t.Fatalf("idempotent counts: artifacts=%d proposals=%d", artifactCount, proposalCount)
	}
}

func TestDocumentDownstreamFailureIsObservableAndRetryIsFencedPostgres(t *testing.T) {
	database, cleanup := collaborationPostgresDatabase(t)
	defer cleanup()
	fixture := seedCollaborationFixture(t, database, false)
	ctx := context.Background()
	input := GenerateDownstreamDocumentInput{
		SourceRevision: fixture.source, TargetKind: "data_contract", TargetTitle: "Generated data contract",
		Instruction: "Derive the reviewed data contract.", Model: "test-model",
		CommandKey: "downstream-failure-recovery-0001",
	}
	fixture.generator.failure = context.DeadlineExceeded
	if _, err := fixture.service.GenerateDownstreamDocument(
		ctx, fixture.projectID.String(), fixture.ownerID.String(), input,
	); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first generation error = %v", err)
	}
	var command storage.DocumentGenerationCommandModel
	if err := database.Where(
		"project_id = ? AND actor_id = ? AND command_key = ?",
		fixture.projectID, fixture.ownerID, input.CommandKey,
	).Take(&command).Error; err != nil {
		t.Fatal(err)
	}
	if command.Status != "processing" || command.AttemptCount != 1 || command.LockedUntil != nil ||
		command.LastFailure == nil || *command.LastFailure != "deadline_exceeded" || command.LastFailedAt == nil {
		t.Fatalf("failed command state = %+v", command)
	}
	var failureAudit storage.AuditEventModel
	if err := database.Where(
		"project_id = ? AND action = ? AND target_id = ?",
		fixture.projectID, "document.downstream_generation_failed", command.ID.String(),
	).Take(&failureAudit).Error; err != nil {
		t.Fatal(err)
	}
	var failureOutbox storage.OutboxEventModel
	if err := database.Where(
		"event_type = ? AND aggregate_id = ?", "document.downstream_generation_failed", command.ID.String(),
	).Take(&failureOutbox).Error; err != nil {
		t.Fatal(err)
	}
	var failurePayload map[string]any
	if err := json.Unmarshal(failureOutbox.Payload, &failurePayload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(failureAudit.Metadata), context.DeadlineExceeded.Error()) ||
		strings.Contains(string(failureOutbox.Payload), context.DeadlineExceeded.Error()) ||
		failurePayload["failureClass"] != "deadline_exceeded" || failurePayload["attempt"] != float64(1) {
		t.Fatalf("unsafe or incomplete failure payload: audit=%s outbox=%s", failureAudit.Metadata, failureOutbox.Payload)
	}

	fixture.generator.failure = nil
	generated, err := fixture.service.GenerateDownstreamDocument(
		ctx, fixture.projectID.String(), fixture.ownerID.String(), input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if generated.Replayed || generated.CommandID != command.ID.String() || fixture.generator.calls.Load() != 2 {
		t.Fatalf("recovered generation = %+v calls=%d", generated, fixture.generator.calls.Load())
	}
	if err := database.Where("id = ?", command.ID).Take(&command).Error; err != nil {
		t.Fatal(err)
	}
	if command.Status != "completed" || command.AttemptCount != 2 || command.OutputProposalID == nil ||
		command.LastFailure == nil || *command.LastFailure != "deadline_exceeded" {
		t.Fatalf("recovered command state = %+v", command)
	}
	var successOutbox storage.OutboxEventModel
	if err := database.Where(
		"event_type = ? AND aggregate_id = ?", "document.downstream_generated", command.ID.String(),
	).Take(&successOutbox).Error; err != nil {
		t.Fatal(err)
	}
	var successPayload map[string]any
	if err := json.Unmarshal(successOutbox.Payload, &successPayload); err != nil {
		t.Fatal(err)
	}
	if successPayload["attempt"] != float64(2) || successPayload["sourceRevision"] == nil ||
		successPayload["targetRevision"] == nil || successPayload["inputManifestId"] == nil ||
		successPayload["outputProposalId"] == nil || successPayload["resolvedOwnerIds"] == nil ||
		successPayload["provider"] != "test-provider" || successPayload["model"] != "test-model" {
		t.Fatalf("incomplete success event: %s", successOutbox.Payload)
	}
}

func TestDocumentSyncBackOnlyAcceptsAppliedCurrentImplementationFactsPostgres(t *testing.T) {
	database, cleanup := collaborationPostgresDatabase(t)
	defer cleanup()
	fixture := seedCollaborationFixture(t, database, false)
	ctx := context.Background()
	target := seedApprovedCollaborationArtifact(
		t, database, fixture.contents, fixture.projectID, fixture.ownerID,
		"product_requirements", "SYNC-TARGET", "Reviewed requirements", json.RawMessage(`{"summary":"reviewed","blocks":[]}`), nil,
	)
	manualWorkspace := seedApprovedCollaborationArtifact(
		t, database, fixture.contents, fixture.projectID, fixture.ownerID, "workspace", "WORKSPACE-MANUAL",
		"Manual workspace", json.RawMessage(`{"files":{}}`), nil,
	)
	if _, err := fixture.service.CreateSyncBackProposal(ctx, fixture.projectID.String(), fixture.ownerID.String(), CreateSyncBackProposalInput{
		TargetRevision: target,
		Provenance:     ProvenanceRef{Kind: ProvenanceWorkspaceRevision, ID: manualWorkspace.RevisionID},
		Instruction:    "A manual workspace must not be represented as applied implementation fact.",
	}); !errors.Is(err, core.ErrProposalStale) {
		t.Fatalf("manual workspace sync-back error = %v", err)
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", manualWorkspace.ArtifactID).
		Updates(map[string]any{"lifecycle": "archived", "archived_at": time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	buildID, implementationID, workspace := seedImplementationFactFixture(t, fixture)

	before, err := fixture.artifacts.Get(ctx, target.ArtifactID, fixture.ownerID.String(), true)
	if err != nil || before.Draft == nil {
		t.Fatalf("load target before sync-back: %+v err=%v", before, err)
	}
	request := CreateSyncBackProposalInput{
		TargetRevision: target,
		Provenance:     ProvenanceRef{Kind: ProvenanceImplementationProposal, ID: implementationID.String()},
		Instruction:    "Write the implemented behavior and exact trace into the reviewed requirements.",
		Model:          "test-model",
	}
	if _, err := fixture.service.CreateSyncBackProposal(ctx, fixture.projectID.String(), fixture.ownerID.String(), request); !errors.Is(err, core.ErrProposalStale) {
		t.Fatalf("unapplied implementation sync-back error = %v", err)
	}
	now := time.Now().UTC()
	if err := database.Model(&storage.ImplementationProposalModel{}).Where("id = ?", implementationID).Updates(map[string]any{
		"status": "applied", "applied_by": fixture.ownerID, "applied_at": now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ApplicationBuildManifestModel{}).Where("id = ?", buildID).Update("status", "consumed").Error; err != nil {
		t.Fatal(err)
	}
	deploymentID, deploymentVersionID := uuid.New(), uuid.New()
	if err := database.Exec(`INSERT INTO deployments (id,project_id,environment,provider,status,created_by) VALUES (?,?,?,?,?,?)`,
		deploymentID, fixture.projectID, "preview", "test", "deploying", fixture.ownerID).Error; err != nil {
		t.Fatal(err)
	}
	manualDeploymentRequest := request
	manualDeploymentRequest.Provenance = ProvenanceRef{Kind: ProvenanceDeployment, ID: deploymentID.String()}
	if _, err := fixture.service.CreateSyncBackProposal(ctx, fixture.projectID.String(), fixture.ownerID.String(), manualDeploymentRequest); !errors.Is(err, core.ErrProposalStale) {
		t.Fatalf("deployment without an active immutable version error = %v", err)
	}
	release := seedCollaborationReleaseAuthority(t, fixture, buildID, workspace)
	if err := database.Exec(`
INSERT INTO deployment_versions (
  id, deployment_id, number, action,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  build_manifest_id, quality_run_id,
  build_artifact_id, build_content_ref, build_content_hash, build_hash,
  build_entry_path, build_file_count, build_total_bytes,
  entry_path, checksum, file_count, total_bytes, environment_ref, status, created_by,
  canonical_receipt_id, canonical_receipt_hash, release_bundle_id, release_bundle_hash
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`, deploymentVersionID, deploymentID, 1, "publish",
		workspace.ArtifactID, workspace.RevisionID, workspace.ContentHash,
		buildID, release.qualityRunID,
		release.buildArtifactID, release.buildContentRef, release.buildContentHash, release.buildHash,
		"index.html", 1, 1024,
		"/", collaborationHash("canonical-deployment"), 1, 1024, "default", "deploying", fixture.ownerID,
		release.receiptID, release.receiptHash, release.bundleID, release.bundleHash,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Table("deployment_versions").Where("id = ?", deploymentVersionID).
		Update("status", "ready").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Table("deployments").Where("id = ?", deploymentID).Updates(map[string]any{
		"active_version_id": deploymentVersionID,
		"status":            "ready",
	}).Error; err != nil {
		t.Fatal(err)
	}
	deploymentGenerated, err := fixture.service.CreateSyncBackProposal(
		ctx, fixture.projectID.String(), fixture.ownerID.String(), manualDeploymentRequest,
	)
	if err != nil || deploymentGenerated.WorkspaceSource == nil || *deploymentGenerated.WorkspaceSource != workspace {
		t.Fatalf("canonical deployment sync-back = %+v, err=%v", deploymentGenerated, err)
	}
	generated, err := fixture.service.CreateSyncBackProposal(ctx, fixture.projectID.String(), fixture.ownerID.String(), request)
	if err != nil {
		t.Fatal(err)
	}
	directRequest := request
	directRequest.Provenance = ProvenanceRef{Kind: ProvenanceWorkspaceRevision, ID: workspace.RevisionID}
	directGenerated, err := fixture.service.CreateSyncBackProposal(ctx, fixture.projectID.String(), fixture.ownerID.String(), directRequest)
	if err != nil || directGenerated.WorkspaceSource == nil || *directGenerated.WorkspaceSource != workspace {
		t.Fatalf("applied workspace sync-back = %+v, err=%v", directGenerated, err)
	}
	if generated.Proposal.Status != domain.ProposalOpen || generated.WorkspaceSource == nil || *generated.WorkspaceSource != workspace ||
		len(generated.Manifest.Sources) != 1 || generated.Manifest.Sources[0].Ref.ArtifactID != workspace.ArtifactID ||
		generated.Manifest.JobType != core.DocumentSyncBackJobType {
		t.Fatalf("sync-back result = %+v", generated)
	}
	after, err := fixture.artifacts.Get(ctx, target.ArtifactID, fixture.ownerID.String(), true)
	if err != nil || after.Draft == nil || after.Draft.ContentHash != before.Draft.ContentHash || after.Draft.ETag != before.Draft.ETag {
		t.Fatalf("sync-back mutated document before review: before=%+v after=%+v err=%v", before.Draft, after.Draft, err)
	}

	buildRequest := request
	buildRequest.Provenance = ProvenanceRef{Kind: ProvenanceBuildManifest, ID: buildID.String()}
	buildGenerated, err := fixture.service.CreateSyncBackProposal(ctx, fixture.projectID.String(), fixture.ownerID.String(), buildRequest)
	if err != nil || buildGenerated.WorkspaceSource == nil || *buildGenerated.WorkspaceSource != workspace {
		t.Fatalf("consumed build sync-back = %+v, err=%v", buildGenerated, err)
	}
	var auditCount, outboxCount int64
	if err := database.Model(&storage.AuditEventModel{}).Where("project_id = ? AND action = ?", fixture.projectID, "document.sync_back_proposed").Count(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.OutboxEventModel{}).Where("event_type = ?", "document.sync_back_proposed").Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if auditCount != 4 || outboxCount != 4 {
		t.Fatalf("sync-back audit/outbox counts = %d/%d", auditCount, outboxCount)
	}
}

func seedCollaborationFixture(t *testing.T, database *gorm.DB, blockedGenerator bool) collaborationPostgresFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	userIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	for index, userID := range userIDs {
		if err := database.Create(&storage.UserModel{
			ID: userID, Email: fmt.Sprintf("collaboration-%d-%s@example.com", index, uuid.NewString()),
			DisplayName: fmt.Sprintf("Collaboration user %d", index), PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	projectID := uuid.New()
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Document collaboration PG", Description: "", Lifecycle: "active", Version: 1,
		CreatedBy: userIDs[0], CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for index, role := range []string{"owner", "editor", "viewer"} {
		if err := database.Create(&storage.ProjectMemberModel{
			ProjectID: projectID, UserID: userIDs[index], Role: role, InvitedBy: &userIDs[0], JoinedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	contents := newCollaborationMemoryContentStore()
	access, err := core.NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := core.NewArtifactService(database, contents, access)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := core.NewProposalService(database, contents, access)
	if err != nil {
		t.Fatal(err)
	}
	generator := &collaborationTestGenerator{proposals: proposals}
	if blockedGenerator {
		generator.started, generator.release = make(chan struct{}), make(chan struct{})
	}
	service, err := NewService(database, contents, access, artifacts, proposals, generator, WithDownstreamCommandLease(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	source := seedApprovedCollaborationArtifact(
		t, database, contents, projectID, userIDs[0], "product_requirements", "SOURCE-REQUIREMENTS",
		"Reviewed source requirements", json.RawMessage(`{"summary":"reviewed source","blocks":[]}`), nil,
	)
	return collaborationPostgresFixture{
		database: database, contents: contents, service: service, proposals: proposals, artifacts: artifacts,
		generator: generator, projectID: projectID, ownerID: userIDs[0], editorID: userIDs[1],
		viewerID: userIDs[2], outsiderID: userIDs[3], source: source,
	}
}

func seedApprovedCollaborationArtifact(
	t *testing.T,
	database *gorm.DB,
	contents *collaborationMemoryContentStore,
	projectID, actorID uuid.UUID,
	kind, artifactKey, title string,
	payload json.RawMessage,
	implementationProposalID *uuid.UUID,
) core.VersionRef {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	artifactID, revisionID, draftID := uuid.New(), uuid.New(), uuid.New()
	reference := contents.addFinalized(projectID.String(), "artifact_revision", revisionID.String(), payload)
	if err := database.Create(&storage.ArtifactModel{
		ID: artifactID, ProjectID: projectID, Kind: kind, ArtifactKey: artifactKey, Title: title,
		Lifecycle: "active", Version: 1, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ArtifactRevisionModel{
		ID: revisionID, ArtifactID: artifactID, RevisionNumber: 1, SchemaVersion: 1,
		ContentStore: "mongo", ContentRef: reference.ID, ContentHash: reference.ContentHash, ByteSize: reference.ByteSize,
		WorkflowStatus: "approved", ChangeSource: "human", ChangeSummary: "Reviewed fixture",
		ImplementationProposalID: implementationProposalID, CreatedBy: actorID, CreatedAt: now, ApprovedAt: &now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if kind != "workspace" {
		if err := database.Create(&storage.ArtifactDraftModel{
			ID: draftID, ArtifactID: artifactID, BaseRevisionID: &revisionID, Sequence: 1,
			ETag: `W/"fixture-` + draftID.String() + `"`, SchemaVersion: 1,
			ContentStore: "mongo", ContentRef: reference.ID, ContentHash: reference.ContentHash, ByteSize: reference.ByteSize,
			Status: "draft", CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	updates := map[string]any{"latest_revision_id": revisionID, "latest_approved_revision_id": revisionID}
	if kind != "workspace" {
		updates["latest_draft_id"] = draftID
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).Updates(updates).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ArtifactHealthModel{
		ArtifactID: artifactID, SyncStatus: "current", DeliveryStatus: "incomplete", Report: json.RawMessage(`{}`), ComputedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return core.VersionRef{ArtifactID: artifactID.String(), RevisionID: revisionID.String(), ContentHash: reference.ContentHash}
}

func seedImplementationFactFixture(t *testing.T, fixture collaborationPostgresFixture) (uuid.UUID, uuid.UUID, core.VersionRef) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	buildID, implementationID := uuid.New(), uuid.New()
	buildPayload := json.RawMessage(`{"routes":["/orders"],"tests":["orders.e2e"]}`)
	buildContent := fixture.contents.addFinalized(fixture.projectID.String(), "application_build_manifest", buildID.String(), buildPayload)
	if err := fixture.database.Create(&storage.ApplicationBuildManifestModel{
		ID: buildID, ProjectID: fixture.projectID, RootManifestID: buildID, SchemaVersion: 1,
		ContentStore: "mongo", ContentRef: buildContent.ID, ContentHash: buildContent.ContentHash,
		ManifestHash: collaborationHash("build-manifest:" + buildID.String()), Status: "frozen",
		CreatedBy: fixture.ownerID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	implementationPayload := json.RawMessage(`{"operations":[{"kind":"upsert","path":"app/orders.tsx"}],"tests":["orders.e2e"]}`)
	implementationContent := fixture.contents.addFinalized(fixture.projectID.String(), "implementation_proposal", implementationID.String(), implementationPayload)
	completionCount := 0
	proposal := &storage.ImplementationProposalModel{
		ID: implementationID, ProjectID: fixture.projectID, BuildManifestID: buildID, Status: "open", Version: 1,
		ContentStore: "mongo", ContentRef: implementationContent.ID, ContentHash: implementationContent.ContentHash,
		PayloadHash: collaborationHash("implementation:" + implementationID.String()), OperationCount: 1,
		UnimplementedCount: &completionCount, BlockingDiagnosticCount: &completionCount,
		CreatedBy: fixture.ownerID, CreatedAt: now,
	}
	testsupport.CreateBoundImplementationProposal(t, fixture.database, proposal)
	workspace := seedApprovedCollaborationArtifact(
		t, fixture.database, fixture.contents, fixture.projectID, fixture.ownerID, "workspace", "WORKSPACE-MAIN",
		"Application workspace", json.RawMessage(`{"files":{"app/orders.tsx":{"content":"implemented"}}}`), &implementationID,
	)
	return buildID, implementationID, workspace
}

type collaborationReleaseAuthority struct {
	receiptID        uuid.UUID
	receiptHash      string
	bundleID         uuid.UUID
	bundleHash       string
	qualityRunID     uuid.UUID
	buildArtifactID  uuid.UUID
	buildContentRef  string
	buildContentHash string
	buildHash        string
}

func seedCollaborationReleaseAuthority(
	t *testing.T,
	fixture collaborationPostgresFixture,
	buildID uuid.UUID,
	workspace core.VersionRef,
) collaborationReleaseAuthority {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	type exactBuildLineage struct {
		ManifestHash          string
		BuildContractID       uuid.UUID
		BuildContractHash     string
		FullStackTemplateID   uuid.UUID
		FullStackTemplateHash string
	}
	var lineage exactBuildLineage
	if err := fixture.database.Table("implementation_proposals AS proposal").
		Select(`manifest.manifest_hash, proposal.application_build_contract_id AS build_contract_id,
contract.contract_hash AS build_contract_hash, contract.full_stack_template_id, contract.full_stack_template_hash`).
		Joins("JOIN application_build_manifests AS manifest ON manifest.id = proposal.build_manifest_id").
		Joins("JOIN application_build_contracts AS contract ON contract.id = proposal.application_build_contract_id").
		Where("proposal.build_manifest_id = ? AND proposal.project_id = ?", buildID, fixture.projectID).
		Take(&lineage).Error; err != nil {
		t.Fatalf("load exact build lineage for canonical release: %v", err)
	}

	profileID := "collaboration-canonical-v1"
	profileHash := collaborationHash("collaboration-canonical-profile")
	profileDocument, err := json.Marshal(map[string]any{
		"schemaVersion":          "verification-profile/v1",
		"id":                     profileID,
		"version":                1,
		"profileHash":            profileHash,
		"supportedTemplateRoles": []string{"web", "api"},
		"verifierImages": []map[string]any{{
			"role":  "node",
			"image": "registry.example/quality-node@" + collaborationHash("collaboration-quality-image"),
		}},
		"builtInChecks": []any{},
		"limits":        map[string]any{},
		"networkPolicy": map[string]any{},
		"state":         "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.Exec(`
INSERT INTO verification_profile_versions (
  profile_id, version, schema_version, document, content_hash, created_by
) VALUES (?, 1, 'verification-profile/v1', ?::jsonb, ?, ?)
`, profileID, string(profileDocument), profileHash, fixture.ownerID).Error; err != nil {
		t.Fatalf("insert canonical collaboration VerificationProfile: %v", err)
	}
	if err := fixture.database.Exec(`
INSERT INTO verification_profile_policies (
  profile_id, profile_version, profile_hash, state, policy_version, reason, updated_by
) VALUES (?, 1, ?, 'active', 1, 'canonical collaboration release fixture', ?)
`, profileID, profileHash, fixture.ownerID).Error; err != nil {
		t.Fatalf("activate canonical collaboration VerificationProfile: %v", err)
	}

	templateReleases, err := json.Marshal([]map[string]any{
		{"role": "api", "id": uuid.NewString(), "contentHash": collaborationHash("collaboration-api-template")},
		{"role": "web", "id": uuid.NewString(), "contentHash": collaborationHash("collaboration-web-template")},
	})
	if err != nil {
		t.Fatal(err)
	}
	obligations := json.RawMessage(`[{"id":"OBL-COLLABORATION","level":"must","status":"ready","oracleIds":["oracle-collaboration"]}]`)
	checksJSON := json.RawMessage(`[
  "oracle-collaboration",
  "release-artifacts",
  "release-container-policy",
  "release-sbom",
  "release-vulnerability"
]`)
	planID := uuid.New()
	planHash := collaborationHash("collaboration-canonical-plan:" + workspace.RevisionID)
	runtimePolicyHash := collaborationHash("collaboration-runtime-policy")
	// ReadyApplicationBuildContract creates the compact BuildContract fixture used by
	// this test package without child projections. Seed only the frozen Plan row with
	// trigger replication disabled; the Run, Attempt, Receipt, Bundle,
	// QualityRun and DeploymentVersion below all pass their production gates.
	if err := fixture.database.Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Exec("SET LOCAL session_replication_role = replica").Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO canonical_verification_plans (
  id, schema_version, scope, project_id,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  verification_profile_id, verification_profile_version, verification_profile_hash,
  template_releases, obligations, check_ids, required_check_ids,
  check_count, obligation_count, runtime_policy_hash,
  content_store, content_ref, content_hash, plan_hash, created_by
) VALUES (
  ?, 'canonical-verification-plan/v1', 'canonical', ?,
  ?, ?, ?, ?, ?, ?, ?, ?, ?,
  ?, 1, ?, ?::jsonb, ?::jsonb, ?::jsonb, ?::jsonb,
  5, 1, ?, 'blob', ?, ?, ?, ?
)
`, planID, fixture.projectID,
			workspace.ArtifactID, workspace.RevisionID, workspace.ContentHash,
			buildID, lineage.ManifestHash, lineage.BuildContractID, lineage.BuildContractHash,
			lineage.FullStackTemplateID, lineage.FullStackTemplateHash,
			profileID, profileHash, string(templateReleases), string(obligations), string(checksJSON), string(checksJSON),
			runtimePolicyHash, "blob://collaboration-canonical-plans/"+planID.String(), planHash, planHash,
			fixture.ownerID,
		).Error; err != nil {
			return err
		}
		return transaction.Exec("SET LOCAL session_replication_role = origin").Error
	}); err != nil {
		t.Fatalf("insert exact canonical collaboration VerificationPlan: %v", err)
	}

	runID := uuid.New()
	if err := fixture.database.Exec(`
INSERT INTO canonical_verification_runs (
  id, schema_version, project_id, plan_id, plan_hash,
  request_key, request_hash, reason, state, version, fence_epoch,
  created_by, updated_by
) VALUES (
  ?, 'canonical-verification-run/v1', ?, ?, ?,
  ?, ?, 'canonical collaboration release', 'queued', 1, 0,
  ?, ?
)
`, runID, fixture.projectID, planID, planHash,
		"collaboration-canonical-"+runID.String(), collaborationHash("collaboration-run:"+runID.String()),
		fixture.ownerID, fixture.ownerID,
	).Error; err != nil {
		t.Fatalf("queue canonical collaboration VerificationRun: %v", err)
	}
	workerID := "collaboration-canonical-worker"
	leaseUntil := now.Add(15 * time.Minute)
	attemptID := uuid.New()
	// Claim authority is committed only with its exact Attempt generation and
	// durable cleanup obligation, matching the production worker transaction.
	if err := fixture.database.Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Exec(`
UPDATE canonical_verification_runs
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = ?, lease_epoch = 1, lease_expires_at = ?,
    started_at = ?, updated_by = ?
WHERE id = ?
`, workerID, leaseUntil, now, fixture.ownerID, runID).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
INSERT INTO canonical_verification_attempts (
  id, schema_version, run_id, project_id, plan_id, plan_hash,
  ordinal, state, version, fence_epoch, created_by, updated_by
) VALUES (
  ?, 'canonical-verification-attempt/v1', ?, ?, ?, ?,
  1, 'queued', 1, 0, ?, ?
)
`, attemptID, runID, fixture.projectID, planID, planHash, fixture.ownerID, fixture.ownerID).Error; err != nil {
			return err
		}
		if err := transaction.Exec(`
UPDATE canonical_verification_attempts
SET state = 'claimed', version = 2, fence_epoch = 1,
    lease_worker_id = ?, lease_epoch = 1, lease_expires_at = ?,
    started_at = ?, updated_by = ?
WHERE id = ?
`, workerID, leaseUntil, now, fixture.ownerID, attemptID).Error; err != nil {
			return err
		}
		return transaction.Exec(`
INSERT INTO verification_execution_cleanups (
  scope, project_id, run_id, attempt_id, attempt_fence_epoch,
  state, created_by, updated_by
) VALUES ('canonical', ?, ?, ?, 1, 'registered', ?, ?)
`, fixture.projectID, runID, attemptID, fixture.ownerID, fixture.ownerID).Error
	}); err != nil {
		t.Fatalf("claim canonical collaboration verification execution: %v", err)
	}
	for index, state := range []string{"materializing", "preparing", "running", "collecting"} {
		if err := fixture.database.Exec(
			"UPDATE canonical_verification_attempts SET state = ?, version = ?, updated_by = ? WHERE id = ?",
			state, index+3, fixture.ownerID, attemptID,
		).Error; err != nil {
			t.Fatalf("advance canonical collaboration VerificationAttempt to %s: %v", state, err)
		}
	}
	if err := fixture.database.Exec(`
UPDATE verification_execution_cleanups
SET state = 'completed', version = 2,
    completed_at = statement_timestamp(), updated_by = ?
WHERE scope = 'canonical' AND attempt_id = ? AND attempt_fence_epoch = 1
`, fixture.ownerID, attemptID).Error; err != nil {
		t.Fatalf("complete canonical collaboration verification cleanup: %v", err)
	}
	if err := fixture.database.Exec(`
UPDATE canonical_verification_attempts
SET state = 'passed', version = 7,
    lease_worker_id = NULL, lease_epoch = NULL, lease_expires_at = NULL,
    finished_at = ?, updated_by = ?
WHERE id = ?
`, now, fixture.ownerID, attemptID).Error; err != nil {
		t.Fatalf("pass canonical collaboration VerificationAttempt: %v", err)
	}
	for index, state := range []string{"materializing", "preparing", "running", "collecting"} {
		if err := fixture.database.Exec(
			"UPDATE canonical_verification_runs SET state = ?, version = ?, updated_by = ? WHERE id = ?",
			state, index+3, fixture.ownerID, runID,
		).Error; err != nil {
			t.Fatalf("advance canonical collaboration VerificationRun to %s: %v", state, err)
		}
	}
	if err := fixture.database.Exec(`
UPDATE canonical_verification_runs
SET state = 'passed', version = 7,
    lease_worker_id = NULL, lease_epoch = NULL, lease_expires_at = NULL,
    finished_at = ?, updated_by = ?
WHERE id = ?
`, now, fixture.ownerID, runID).Error; err != nil {
		t.Fatalf("pass canonical collaboration VerificationRun: %v", err)
	}

	buildHash := collaborationHash("collaboration-web-static:" + workspace.RevisionID)
	releaseArtifacts, err := json.Marshal([]map[string]any{
		{
			"id": "application-image", "kind": "oci-image", "store": "oci",
			"ref":         "registry.example/collaboration@" + collaborationHash("collaboration-image"),
			"contentHash": collaborationHash("collaboration-image"),
			"mediaType":   "application/vnd.oci.image.manifest.v1+json", "byteSize": 4096,
		},
		{
			"id": "web-static", "kind": "web-static", "store": "content",
			"ref": "content://collaboration-web-static", "contentHash": buildHash,
			"mediaType": "application/vnd.worksflow.static-tree", "byteSize": 1024,
		},
		collaborationReleaseMetadataArtifact("health-contract", "health-readiness-contract", "application/schema+json"),
		collaborationReleaseMetadataArtifact("migration", "migration", "application/vnd.worksflow.migration"),
		collaborationReleaseMetadataArtifact("provenance", "provenance", "application/vnd.in-toto+json"),
		collaborationReleaseMetadataArtifact("runtime-config", "runtime-config-schema", "application/schema+json"),
		collaborationReleaseMetadataArtifact("sbom", "sbom", "application/spdx+json"),
		collaborationReleaseMetadataArtifact("signature", "signature", "application/vnd.dev.cosign.simplesigning.v1+json"),
		collaborationReleaseMetadataArtifact("vulnerability", "vulnerability-report", "application/vnd.worksflow.vulnerability+json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	receiptID := uuid.New()
	receiptHash := collaborationHash("collaboration-canonical-receipt:" + workspace.RevisionID)
	if err := fixture.database.Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Exec(`
INSERT INTO canonical_verification_receipts (
  id, schema_version, scope, run_id, project_id, plan_id, plan_hash,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  verification_profile_id, verification_profile_version, verification_profile_hash,
  attempt_ids, release_artifacts, check_count, coverage_count,
  must_count, must_passed_count, blocker_count, warning_count, decision,
  content_store, content_ref, content_hash, payload_hash, created_by
) VALUES (
  ?, 'canonical-verification-receipt/v1', 'canonical', ?, ?, ?, ?,
  ?, ?, ?, ?, ?, ?, ?, ?, ?,
  ?, 1, ?, ?::jsonb, ?::jsonb, 5, 1,
  1, 1, 0, 0, 'passed', 'blob', ?, ?, ?, ?
)
`, receiptID, runID, fixture.projectID, planID, planHash,
			workspace.ArtifactID, workspace.RevisionID, workspace.ContentHash,
			buildID, lineage.ManifestHash, lineage.BuildContractID, lineage.BuildContractHash,
			lineage.FullStackTemplateID, lineage.FullStackTemplateHash,
			profileID, profileHash, `["`+attemptID.String()+`"]`, string(releaseArtifacts),
			"blob://collaboration-canonical-receipts/"+receiptID.String(), collaborationHash("collaboration-receipt-content"),
			receiptHash, fixture.ownerID,
		).Error; err != nil {
			return err
		}
		checks := []struct {
			id   string
			kind string
		}{
			{id: "oracle-collaboration", kind: "test"},
			{id: "release-artifacts", kind: "release-manifest"},
			{id: "release-container-policy", kind: "container-policy"},
			{id: "release-sbom", kind: "sbom"},
			{id: "release-vulnerability", kind: "vulnerability"},
		}
		for ordinal, check := range checks {
			if err := transaction.Exec(`
INSERT INTO canonical_verification_checks (
  receipt_id, ordinal, check_id, kind, required, status, attempt_id, truncated
) VALUES (?, ?, ?, ?, true, 'passed', ?, false)
`, receiptID, ordinal, check.id, check.kind, attemptID).Error; err != nil {
				return err
			}
		}
		return transaction.Exec(`
INSERT INTO canonical_verification_obligation_coverage (
  receipt_id, ordinal, obligation_id, level, check_ids, status
) VALUES (?, 0, 'OBL-COLLABORATION', 'must', ?::jsonb, 'passed')
`, receiptID, string(checksJSON)).Error
	}); err != nil {
		t.Fatalf("insert exact passed canonical collaboration VerificationReceipt: %v", err)
	}

	bundleID := uuid.New()
	bundleHash := collaborationHash("collaboration-release-bundle:" + workspace.RevisionID)
	if err := fixture.database.Exec(`
INSERT INTO release_bundles (
  id, schema_version, project_id, workspace_artifact_id, workspace_revision_id,
  workspace_content_hash, canonical_receipt_id, canonical_receipt_hash,
  release_artifacts, content_store, content_ref, content_hash, bundle_hash, created_by
) VALUES (
  ?, 'release-bundle/v1', ?, ?, ?, ?, ?, ?, ?::jsonb,
  'blob', ?, ?, ?, ?
)
`, bundleID, fixture.projectID, workspace.ArtifactID, workspace.RevisionID, workspace.ContentHash,
		receiptID, receiptHash, string(releaseArtifacts),
		"blob://collaboration-release-bundles/"+bundleID.String(), collaborationHash("collaboration-bundle-content"),
		bundleHash, fixture.ownerID,
	).Error; err != nil {
		t.Fatalf("insert exact collaboration ReleaseBundle: %v", err)
	}

	qualityRunID, buildArtifactID := uuid.New(), uuid.New()
	buildContentRef := "content://collaboration-web-static/" + buildArtifactID.String()
	buildContentHash := collaborationHash("collaboration-web-static-content:" + buildArtifactID.String())
	if err := fixture.database.Exec(`
INSERT INTO quality_runs (
  id, project_id, workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  status, score, runner_version, sandbox_kind, version, created_by, started_at, completed_at,
  build_artifact_id, build_content_ref, build_content_hash, build_hash,
  build_entry_path, build_file_count, build_total_bytes
) VALUES (
  ?, ?, ?, ?, ?, 'passed', 100, 'collaboration-canonical/v1', 'test', 1, ?, ?, ?,
  ?, ?, ?, ?, 'index.html', 1, 1024
)
`, qualityRunID, fixture.projectID, workspace.ArtifactID, workspace.RevisionID, workspace.ContentHash,
		fixture.ownerID, now, now, buildArtifactID, buildContentRef, buildContentHash, buildHash,
	).Error; err != nil {
		t.Fatalf("insert exact collaboration QualityRun build artifact: %v", err)
	}

	return collaborationReleaseAuthority{
		receiptID: receiptID, receiptHash: receiptHash,
		bundleID: bundleID, bundleHash: bundleHash,
		qualityRunID: qualityRunID, buildArtifactID: buildArtifactID,
		buildContentRef: buildContentRef, buildContentHash: buildContentHash, buildHash: buildHash,
	}
}

func collaborationReleaseMetadataArtifact(id, kind, mediaType string) map[string]any {
	return map[string]any{
		"id": id, "kind": kind, "store": "content", "ref": "content://collaboration-" + id,
		"contentHash": collaborationHash("collaboration-" + id), "mediaType": mediaType, "byteSize": 512,
	}
}

func graphHasNode(graph DocumentGraph, entityType, entityID string) bool {
	for _, node := range graph.Nodes {
		if node.EntityType == entityType && node.EntityID == entityID {
			return true
		}
	}
	return false
}

func bindingSetHasRole(bindings DocumentMemberBindingSet, userID string, role DocumentMemberRole) bool {
	for _, binding := range bindings.Items {
		if binding.UserID == userID && binding.Role == role {
			return true
		}
	}
	return false
}

func canonicalGeneratedDocumentContent(payload json.RawMessage, expectedKind string) bool {
	if expectedKind == "apiContract" {
		var contract struct {
			SchemaVersion string                     `json:"schemaVersion"`
			OpenAPI       string                     `json:"openapi"`
			Info          map[string]any             `json:"info"`
			Paths         map[string]json.RawMessage `json:"paths"`
		}
		return json.Unmarshal(payload, &contract) == nil && contract.SchemaVersion == "api-contract/v1" &&
			contract.OpenAPI == "3.1.0" && contract.Info != nil && len(contract.Paths) > 0
	}
	var value struct {
		Kind               string `json:"kind"`
		Blocks             []any  `json:"blocks"`
		AcceptanceCriteria []any  `json:"acceptanceCriteria"`
		Requirements       []any  `json:"requirements"`
		OpenQuestions      []any  `json:"openQuestions"`
		Assumptions        []any  `json:"assumptions"`
	}
	return json.Unmarshal(payload, &value) == nil && value.Kind == expectedKind && value.Blocks != nil &&
		value.AcceptanceCriteria != nil && value.Requirements != nil && value.OpenQuestions != nil && value.Assumptions != nil
}

func graphHasEdge(graph DocumentGraph, relation, sourceID, targetID string) bool {
	for _, edge := range graph.Edges {
		if edge.Relation == relation && edge.SourceID == sourceID && edge.TargetID == targetID {
			return true
		}
	}
	return false
}

func collaborationHash(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func collaborationPostgresDatabase(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	base, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := "document_collaboration_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	database, err := gorm.Open(
		gormpostgres.Open(collaborationPostgresDSNWithSearchPath(t, dsn, schema)),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		_ = base.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error
		t.Fatal(err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(context.Background(), sqlDatabase); err != nil {
		_ = sqlDatabase.Close()
		_ = base.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error
		t.Fatal(err)
	}
	cleanup := func() {
		_ = sqlDatabase.Close()
		_ = base.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error
		if baseSQL, sqlErr := base.DB(); sqlErr == nil {
			_ = baseSQL.Close()
		}
	}
	return database, cleanup
}

func collaborationPostgresDSNWithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}
