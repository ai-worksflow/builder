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
		operation.Kind, operation.Path, operation.Value = domain.OperationReplace, "/summary", json.RawMessage(`"Generated downstream summary"`)
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
		deploymentID, fixture.projectID, "preview", "test", "ready", fixture.ownerID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO deployment_versions (id,deployment_id,number,action,workspace_artifact_id,workspace_revision_id,workspace_content_hash,entry_path,checksum,file_count,total_bytes,environment_ref,status,created_by) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		deploymentVersionID, deploymentID, 1, "publish", workspace.ArtifactID, workspace.RevisionID, workspace.ContentHash,
		"/", collaborationHash("manual-deployment"), 1, 1, "default", "ready", fixture.ownerID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Table("deployments").Where("id = ?", deploymentID).Update("active_version_id", deploymentVersionID).Error; err != nil {
		t.Fatal(err)
	}
	manualDeploymentRequest := request
	manualDeploymentRequest.Provenance = ProvenanceRef{Kind: ProvenanceDeployment, ID: deploymentID.String()}
	if _, err := fixture.service.CreateSyncBackProposal(ctx, fixture.projectID.String(), fixture.ownerID.String(), manualDeploymentRequest); !errors.Is(err, core.ErrProposalStale) {
		t.Fatalf("deployment without consumed build producer error = %v", err)
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
	if auditCount != 3 || outboxCount != 3 {
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
	if err := fixture.database.Create(&storage.ImplementationProposalModel{
		ID: implementationID, ProjectID: fixture.projectID, BuildManifestID: buildID, Status: "open", Version: 1,
		ContentStore: "mongo", ContentRef: implementationContent.ID, ContentHash: implementationContent.ContentHash,
		PayloadHash: collaborationHash("implementation:" + implementationID.String()), OperationCount: 1,
		CreatedBy: fixture.ownerID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	workspace := seedApprovedCollaborationArtifact(
		t, fixture.database, fixture.contents, fixture.projectID, fixture.ownerID, "workspace", "WORKSPACE-MAIN",
		"Application workspace", json.RawMessage(`{"files":{"app/orders.tsx":{"content":"implemented"}}}`), &implementationID,
	)
	return buildID, implementationID, workspace
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
