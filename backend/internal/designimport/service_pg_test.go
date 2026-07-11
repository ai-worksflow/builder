package designimport

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"github.com/worksflow/builder/backend/migrations"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type pgCanaryContentStore struct {
	mu    sync.Mutex
	items map[string]content.StoredContent
}

func newPGCanaryContentStore() *pgCanaryContentStore {
	return &pgCanaryContentStore{items: map[string]content.StoredContent{}}
}

func (s *pgCanaryContentStore) PutPending(_ context.Context, projectID, aggregateType, aggregateID string, schemaVersion int, payload json.RawMessage) (content.Reference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copyPayload := append(json.RawMessage(nil), payload...)
	digest := sha256.Sum256(copyPayload)
	reference := content.Reference{
		ID: uuid.NewString(), ContentHash: "sha256:" + hex.EncodeToString(digest[:]),
		ByteSize: int64(len(copyPayload)), SchemaVersion: schemaVersion,
	}
	s.items[reference.ID] = content.StoredContent{
		Reference: reference, ProjectID: projectID, AggregateType: aggregateType,
		AggregateID: aggregateID, State: content.StatePending, Payload: copyPayload, CreatedAt: time.Now().UTC(),
	}
	return reference, nil
}

func (s *pgCanaryContentStore) Finalize(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return content.ErrContentNotFound
	}
	now := time.Now().UTC()
	item.State = content.StateFinalized
	item.FinalizedAt = &now
	s.items[id] = item
	return nil
}

func (s *pgCanaryContentStore) Abort(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return content.ErrContentNotFound
	}
	item.State = content.StateAborted
	s.items[id] = item
	return nil
}

func (s *pgCanaryContentStore) Get(_ context.Context, id, expectedHash string) (content.StoredContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok || item.State == content.StateAborted {
		return content.StoredContent{}, content.ErrContentNotFound
	}
	if expectedHash != "" && item.ContentHash != expectedHash {
		return content.StoredContent{}, content.ErrHashMismatch
	}
	item.Payload = append(json.RawMessage(nil), item.Payload...)
	return item, nil
}

func TestDesignImportPostgresApprovalCanary(t *testing.T) {
	database := designImportPGDatabase(t)
	store := newPGCanaryContentStore()
	ctx := context.Background()
	userID := uuid.New()
	reviewerID := uuid.New()
	projectID := uuid.New()
	now := time.Now().UTC()
	if err := database.Create(&storage.UserModel{
		ID: userID, Email: "design-import-" + userID.String() + "@example.com", DisplayName: "Design Import Owner",
		PasswordHash: "test-only", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.UserModel{
		ID: reviewerID, Email: "design-import-reviewer-" + reviewerID.String() + "@example.com", DisplayName: "Independent Reviewer",
		PasswordHash: "test-only", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Design Import", Description: "PG canary", Lifecycle: "active",
		Version: 1, CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectMemberModel{
		ProjectID: projectID, UserID: userID, Role: string(core.RoleOwner), JoinedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectMemberModel{
		ProjectID: projectID, UserID: reviewerID, Role: string(core.RoleEditor), JoinedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	pageArtifactID := uuid.New()
	pageRevisionID := uuid.New()
	pagePayload := json.RawMessage(`{
  "title":"Home","route":"/","goal":"Open home","blueprintPageNodeId":"page-home",
  "states":[
    {"id":"ready","key":"ready","title":"Ready","required":true,"fixtureIds":[]},
    {"id":"loading","key":"loading","title":"Loading","required":true,"fixtureIds":[]},
    {"id":"empty","key":"empty","title":"Empty","required":true,"fixtureIds":[]},
    {"id":"error","key":"error","title":"Error","required":true,"fixtureIds":[]}
  ],
  "acceptanceCriterionIds":["ac-home"],"dataBindings":[],"interactions":[]
}`)
	pageContent, err := store.PutPending(ctx, projectID.String(), "artifact_revision", pageRevisionID.String(), 1, pagePayload)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(ctx, pageContent.ID); err != nil {
		t.Fatal(err)
	}
	approvedAt := now
	pageArtifact := storage.ArtifactModel{
		ID: pageArtifactID, ProjectID: projectID, Kind: "page_spec", ArtifactKey: "PAGE-HOME",
		Title: "Home PageSpec", Lifecycle: "active", Version: 1,
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&pageArtifact).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ArtifactRevisionModel{
		ID: pageRevisionID, ArtifactID: pageArtifactID, RevisionNumber: 1, SchemaVersion: 1,
		ContentStore: "mongo", ContentRef: pageContent.ID, ContentHash: pageContent.ContentHash, ByteSize: pageContent.ByteSize,
		WorkflowStatus: "approved", ChangeSource: "system", ChangeSummary: "PG canary PageSpec",
		CreatedBy: userID, CreatedAt: now, ApprovedAt: &approvedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", pageArtifactID).
		Updates(map[string]any{"latest_revision_id": pageRevisionID, "latest_approved_revision_id": pageRevisionID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ArtifactHealthModel{
		ArtifactID: pageArtifactID, SyncStatus: "current", DeliveryStatus: "incomplete",
		Report: json.RawMessage(`{}`), ComputedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	seedDesignImportPageSpecAuthority(
		t, ctx, database, store, projectID, userID, now, pageRevisionID,
	)

	access, err := core.NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := core.NewArtifactService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := core.NewProposalService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(database, store, access, artifacts, proposals)
	if err != nil {
		t.Fatal(err)
	}
	export := []byte(`{"pages":[{"id":"home-frame","name":"Home frame"}],"components":[{"id":"task-card","name":"Task card"}]}`)
	created, err := service.Create(ctx, projectID.String(), userID.String(), "pg-canary-create", CreateInput{
		SourceKind: SourceUpload, Mode: "upload", Title: "Imported home",
		File:             &UploadFile{Name: "home.json", MediaType: "application/json", ContentBase64: base64.StdEncoding.EncodeToString(export)},
		SelectedFrameIDs: []string{"home-frame", "task-card"},
		PageSpecRevision: core.VersionRef{ArtifactID: pageArtifactID.String(), RevisionID: pageRevisionID.String(), ContentHash: pageContent.ContentHash},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != "open" || created.Proposal == nil || created.Manifest == nil || created.PrototypeArtifactID == "" {
		t.Fatalf("unexpected open import: %#v", created)
	}
	if _, err := proposals.Decide(ctx, created.OutputProposalID, userID.String(), core.DecideProposalInput{
		OperationID: created.OperationID, Decision: domain.DecisionAccepted, Version: created.Proposal.Version,
	}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("generic proposal decision bypass error = %v, want forbidden", err)
	}
	if _, err := proposals.Apply(ctx, created.OutputProposalID, userID.String(), core.ApplyProposalInput{Version: created.Proposal.Version}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("generic proposal apply bypass error = %v, want forbidden", err)
	}
	for _, decision := range []string{"approve", "reject"} {
		if _, selfReviewErr := service.Decide(ctx, created.ID, userID.String(), created.ETag, DecisionInput{
			Decision: decision, Reason: "creator must not review", Version: created.Version,
		}); !errors.Is(selfReviewErr, core.ErrForbidden) {
			t.Fatalf("creator %s review error = %v, want forbidden", decision, selfReviewErr)
		}
	}
	if err := database.Model(&importModel{}).Where("id = ?", created.ID).Updates(map[string]any{
		"status": "rejected", "decided_by": userID, "decided_at": now,
		"version": gorm.Expr("version + 1"), "updated_at": now,
	}).Error; err == nil {
		t.Fatal("database invariant accepted creator self-review")
	}
	if err := database.Create(&storage.ProposalOperationDecisionModel{
		ProposalID: uuid.MustParse(created.OutputProposalID), OperationID: created.OperationID,
		Decision: string(domain.DecisionAccepted), DecidedBy: userID, DecidedAt: now,
	}).Error; err == nil {
		t.Fatal("database invariant accepted creator proposal decision")
	}
	if err := database.Model(&storage.OutputProposalModel{}).Where("id = ?", created.OutputProposalID).Updates(map[string]any{
		"status": "applied", "applied_by": userID, "applied_at": now,
	}).Error; err == nil {
		t.Fatal("database invariant accepted creator proposal apply")
	}
	applied, err := service.Decide(ctx, created.ID, reviewerID.String(), created.ETag, DecisionInput{
		Decision: "approve", Reason: "mapping reviewed", Version: created.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != "applied" || applied.AppliedRevisionID == "" {
		t.Fatalf("unexpected applied import: %#v", applied)
	}
	if err := database.Model(&storage.OutputProposalModel{}).Where("id = ?", applied.OutputProposalID).
		Update("applied_by", userID).Error; err == nil {
		t.Fatal("database invariant accepted replacing an independent proposal applier with the creator")
	}
	prototype, err := artifacts.Get(ctx, applied.PrototypeArtifactID, userID.String(), true)
	if err != nil {
		t.Fatal(err)
	}
	if prototype.LatestRevision == nil || prototype.LatestRevision.ID != applied.AppliedRevisionID || prototype.LatestRevision.ProposalID == nil || *prototype.LatestRevision.ProposalID != applied.OutputProposalID || prototype.LatestRevision.SourceManifestID == nil || *prototype.LatestRevision.SourceManifestID != applied.InputManifestID {
		t.Fatalf("prototype did not freeze proposal lineage: %#v", prototype.LatestRevision)
	}
	if report := core.ValidateArtifactContent("prototype", prototype.LatestRevision.Content); !report.Valid {
		t.Fatalf("applied Prototype failed backend review schema: %#v", report.Findings)
	}
	gate, err := artifacts.ReviewGate(ctx, prototype.Artifact.ID, userID.String())
	if err != nil {
		t.Fatal(err)
	}
	contentValid := false
	for _, check := range gate.Checks {
		if check.Code == "artifact_content_valid" && check.Severity == "info" {
			contentValid = true
		}
	}
	if !contentValid {
		t.Fatalf("Prototype review gate could not read canonical content: %#v", gate.Checks)
	}
	var frozenSources int64
	if err := database.Model(&storage.ArtifactRevisionSourceModel{}).
		Where("revision_id = ? AND source_artifact_id = ? AND source_revision_id = ? AND source_content_hash = ? AND purpose = 'page_spec' AND required = true", applied.AppliedRevisionID, pageArtifactID, pageRevisionID, pageContent.ContentHash).
		Count(&frozenSources).Error; err != nil {
		t.Fatal(err)
	}
	if frozenSources != 1 {
		t.Fatalf("expected one exact PageSpec source, got %d", frozenSources)
	}
	updateCreated, err := service.Create(ctx, projectID.String(), userID.String(), "pg-canary-update", CreateInput{
		SourceKind: SourceUpload, Mode: "upload", Title: "Updated imported home",
		File: &UploadFile{
			Name: "home-update.json", MediaType: "application/json",
			ContentBase64: base64.StdEncoding.EncodeToString([]byte(`{"pages":[{"id":"home-frame-v2","name":"Home frame v2"}]}`)),
		},
		PageSpecRevision:          core.VersionRef{ArtifactID: pageArtifactID.String(), RevisionID: pageRevisionID.String(), ContentHash: pageContent.ContentHash},
		TargetPrototypeArtifactID: prototype.Artifact.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updateCreated.CreatesPrototype || updateCreated.PrototypeArtifactID != prototype.Artifact.ID {
		t.Fatalf("existing Prototype update was not preserved: %#v", updateCreated)
	}
	updateApplied, err := service.Decide(ctx, updateCreated.ID, reviewerID.String(), updateCreated.ETag, DecisionInput{
		Decision: "approve", Reason: "updated mapping reviewed", Version: updateCreated.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updateApplied.AppliedRevisionID == applied.AppliedRevisionID {
		t.Fatal("existing Prototype update did not create a new immutable revision")
	}
	if err := database.Model(&storage.ArtifactRevisionSourceModel{}).
		Where("revision_id = ? AND source_artifact_id = ? AND source_revision_id = ? AND source_content_hash = ? AND purpose = 'page_spec' AND required = true", updateApplied.AppliedRevisionID, pageArtifactID, pageRevisionID, pageContent.ContentHash).
		Count(&frozenSources).Error; err != nil {
		t.Fatal(err)
	}
	if frozenSources != 1 {
		t.Fatalf("updated Prototype lost its exact PageSpec source, got %d", frozenSources)
	}
	var auditCount, outboxCount int64
	if err := database.Model(&storage.AuditEventModel{}).Where("target_type = 'design_import' AND target_id = ?", created.ID).Count(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.OutboxEventModel{}).Where("aggregate_type = 'design_import' AND aggregate_id = ?", created.ID).Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if auditCount < 3 || outboxCount < 3 {
		t.Fatalf("expected durable design import audit/outbox events, audit=%d outbox=%d", auditCount, outboxCount)
	}

	crashInput := func(name string) CreateInput {
		return CreateInput{
			SourceKind: SourceUpload, Mode: "upload", Title: name,
			File: &UploadFile{
				Name:          strings.ToLower(strings.ReplaceAll(name, " ", "-")) + ".json",
				MediaType:     "application/json",
				ContentBase64: base64.StdEncoding.EncodeToString([]byte(`{"pages":[{"id":"home-frame","name":"Home frame"}]}`)),
			},
			PageSpecRevision: core.VersionRef{
				ArtifactID: pageArtifactID.String(), RevisionID: pageRevisionID.String(), ContentHash: pageContent.ContentHash,
			},
		}
	}
	for _, crashStage := range []string{"prototype_artifact", "base_revision", "input_manifest", "output_proposal"} {
		key := "pg-crash-" + crashStage
		injected := false
		service.creationHook = func(stage string) error {
			if stage == crashStage && !injected {
				injected = true
				return errors.New("injected crash after " + stage)
			}
			return nil
		}
		input := crashInput("Crash " + crashStage)
		if _, createErr := service.Create(ctx, projectID.String(), userID.String(), key, input); createErr == nil {
			t.Fatalf("%s crash was not injected", crashStage)
		}
		service.creationHook = nil
		recovered, recoverErr := service.Create(ctx, projectID.String(), userID.String(), key, input)
		if recoverErr != nil || recovered.Status != "open" || recovered.PipelineStage != stageProposalReady {
			t.Fatalf("recover %s: item=%#v err=%v", crashStage, recovered, recoverErr)
		}
		assertDesignImportStableResources(t, database, projectID, key, recovered)
		var recoveryEvents int64
		if err := database.Model(&storage.AuditEventModel{}).
			Where("target_type = 'design_import' AND target_id = ? AND action = 'design_import.creation_recovered'", recovered.ID).
			Count(&recoveryEvents).Error; err != nil {
			t.Fatal(err)
		}
		if recoveryEvents != 1 {
			t.Fatalf("%s recovery event count = %d", crashStage, recoveryEvents)
		}
	}

	concurrentKey := "pg-concurrent-create"
	concurrentInput := crashInput("Concurrent create")
	hookEntered := make(chan struct{})
	releaseHook := make(chan struct{})
	service.creationHook = func(stage string) error {
		if stage == "prototype_artifact" {
			close(hookEntered)
			<-releaseHook
		}
		return nil
	}
	type createResult struct {
		item Import
		err  error
	}
	firstResult := make(chan createResult, 1)
	go func() {
		item, createErr := service.Create(ctx, projectID.String(), userID.String(), concurrentKey, concurrentInput)
		firstResult <- createResult{item: item, err: createErr}
	}()
	<-hookEntered
	if _, concurrentErr := service.Create(ctx, projectID.String(), userID.String(), concurrentKey, concurrentInput); !errors.Is(concurrentErr, ErrProcessing) {
		t.Fatalf("concurrent create error = %v, want processing", concurrentErr)
	}
	close(releaseHook)
	first := <-firstResult
	service.creationHook = nil
	if first.err != nil || first.item.Status != "open" {
		t.Fatalf("claimed create item=%#v err=%v", first.item, first.err)
	}
	assertDesignImportStableResources(t, database, projectID, concurrentKey, first.item)

	staleKey := "pg-stale-lease-create"
	staleInput := crashInput("Stale lease create")
	staleEntered := make(chan struct{})
	releaseStale := make(chan struct{})
	var staleHookCalls atomic.Int32
	service.createLease = 500 * time.Millisecond
	service.creationHook = func(stage string) error {
		if stage == "prototype_artifact" && staleHookCalls.Add(1) == 1 {
			close(staleEntered)
			<-releaseStale
		}
		return nil
	}
	staleResult := make(chan createResult, 1)
	go func() {
		item, createErr := service.Create(ctx, projectID.String(), userID.String(), staleKey, staleInput)
		staleResult <- createResult{item: item, err: createErr}
	}()
	<-staleEntered
	time.Sleep(650 * time.Millisecond)
	recoveredStale, recoverStaleErr := service.Create(ctx, projectID.String(), userID.String(), staleKey, staleInput)
	if recoverStaleErr != nil || recoveredStale.Status != "open" {
		t.Fatalf("expired lease recovery item=%#v err=%v", recoveredStale, recoverStaleErr)
	}
	close(releaseStale)
	stale := <-staleResult
	service.creationHook = nil
	service.createLease = defaultCreateLease
	if !errors.Is(stale.err, ErrProcessing) || stale.item.ID != "" {
		t.Fatalf("stale worker result item=%#v err=%v", stale.item, stale.err)
	}
	assertDesignImportStableResources(t, database, projectID, staleKey, recoveredStale)
	var staleRecoveryEvents int64
	if err := database.Model(&storage.AuditEventModel{}).
		Where("target_type = 'design_import' AND target_id = ? AND action = 'design_import.creation_recovered'", recoveredStale.ID).
		Count(&staleRecoveryEvents).Error; err != nil {
		t.Fatal(err)
	}
	if staleRecoveryEvents != 1 {
		t.Fatalf("expired lease recovery audit count = %d", staleRecoveryEvents)
	}

	foreignProjectID := uuid.New()
	if err := database.Create(&storage.ProjectModel{
		ID: foreignProjectID, Name: "Foreign tenant", Description: "isolation canary", Lifecycle: "active",
		Version: 1, CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	foreignManifestID := uuid.New()
	if err := database.Create(&storage.InputManifestModel{
		ID: foreignManifestID, ProjectID: foreignProjectID, Kind: "design_import_to_prototype", SchemaVersion: 1,
		ContentStore: "mongo", ContentRef: uuid.NewString(), ContentHash: "sha256:foreign-manifest-content",
		ManifestHash: "sha256:foreign-manifest", CreatedBy: userID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&importModel{}).Where("id = ?", created.ID).
		Update("input_manifest_id", foreignManifestID).Error; err == nil {
		t.Fatal("tenant invariant accepted a foreign design import manifest")
	}
}

func seedDesignImportPageSpecAuthority(
	t *testing.T,
	ctx context.Context,
	database *gorm.DB,
	store *pgCanaryContentStore,
	projectID uuid.UUID,
	userID uuid.UUID,
	now time.Time,
	pageSpecRevisionID uuid.UUID,
) {
	t.Helper()
	type approvedSource struct {
		artifactID uuid.UUID
		revisionID uuid.UUID
		content    content.Reference
	}
	seed := func(kind, key string, payload json.RawMessage) approvedSource {
		artifactID, revisionID := uuid.New(), uuid.New()
		contentRef, err := store.PutPending(ctx, projectID.String(), "artifact_revision", revisionID.String(), 1, payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Finalize(ctx, contentRef.ID); err != nil {
			t.Fatal(err)
		}
		if err := database.Create(&storage.ArtifactModel{
			ID: artifactID, ProjectID: projectID, Kind: kind, ArtifactKey: key, Title: key,
			Lifecycle: "active", Version: 1, CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		approvedAt := now
		if err := database.Create(&storage.ArtifactRevisionModel{
			ID: revisionID, ArtifactID: artifactID, RevisionNumber: 1, SchemaVersion: 1,
			ContentStore: "mongo", ContentRef: contentRef.ID, ContentHash: contentRef.ContentHash, ByteSize: contentRef.ByteSize,
			WorkflowStatus: "approved", ChangeSource: "system", ChangeSummary: "PG canary semantic authority",
			CreatedBy: userID, CreatedAt: now, ApprovedAt: &approvedAt,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).Updates(map[string]any{
			"latest_revision_id": revisionID, "latest_approved_revision_id": revisionID,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Create(&storage.ArtifactHealthModel{
			ArtifactID: artifactID, SyncStatus: "current", DeliveryStatus: "incomplete",
			Report: json.RawMessage(`{}`), ComputedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		return approvedSource{artifactID: artifactID, revisionID: revisionID, content: contentRef}
	}

	baseline := seed("requirement_baseline", "BASELINE-HOME", json.RawMessage(`{
  "requirements":[
    {"type":"requirement","requirementId":"req-home","priority":"must","acceptanceCriterionIds":["ac-home"]},
    {"type":"acceptanceCriterion","acceptanceCriterionId":"ac-home"}
  ]
}`))
	blueprint := seed("blueprint", "BLUEPRINT-HOME", json.RawMessage(`{
  "nodes":[
    {"id":"feature-home","key":"FEATURE-HOME","kind":"feature","title":"Home","requirementIds":["req-home"]},
    {"id":"page-home","key":"PAGE-HOME","kind":"page","title":"Home","route":"/","userGoal":"Open home","requirementIds":["req-home"]}
  ],
  "edges":[{"id":"contains-home","sourceNodeId":"feature-home","targetNodeId":"page-home","kind":"contains","required":true}]
}`))
	if err := database.Create(&storage.ArtifactRevisionSourceModel{
		RevisionID: blueprint.revisionID, Ordinal: 0,
		SourceArtifactID: baseline.artifactID, SourceRevisionID: baseline.revisionID,
		SourceContentHash: baseline.content.ContentHash, Purpose: "requirement_baseline", Required: true,
		AddedBy: userID, AddedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	pageAnchor := "page-home"
	if err := database.Create(&storage.ArtifactRevisionSourceModel{
		RevisionID: pageSpecRevisionID, Ordinal: 0,
		SourceArtifactID: blueprint.artifactID, SourceRevisionID: blueprint.revisionID,
		SourceContentHash: blueprint.content.ContentHash, SourceAnchorID: &pageAnchor,
		Purpose: "blueprint", Required: true, AddedBy: userID, AddedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func assertDesignImportStableResources(t *testing.T, database *gorm.DB, projectID uuid.UUID, commandKey string, item Import) {
	t.Helper()
	digest := sha256.Sum256([]byte(commandKey))
	requestKeyHash := hex.EncodeToString(digest[:])
	importID := designImportID(projectID, requestKeyHash)
	if item.ID != importID.String() {
		t.Fatalf("import id = %s, want stable %s", item.ID, importID)
	}
	expectedArtifactID := designImportResourceID(importID, "prototype-artifact")
	expectedBaseID := designImportResourceID(importID, "prototype-base-revision")
	expectedManifestID := designImportResourceID(importID, "input-manifest")
	expectedProposalID := designImportResourceID(importID, "output-proposal")
	if item.PrototypeArtifactID != expectedArtifactID.String() || item.BaseRevisionID != expectedBaseID.String() ||
		item.InputManifestID != expectedManifestID.String() || item.OutputProposalID != expectedProposalID.String() {
		t.Fatalf("stable identities were not preserved: %#v", item)
	}
	for label, query := range map[string]*gorm.DB{
		"artifact":      database.Model(&storage.ArtifactModel{}).Where("id = ? AND project_id = ?", expectedArtifactID, projectID),
		"base revision": database.Model(&storage.ArtifactRevisionModel{}).Where("id = ? AND artifact_id = ?", expectedBaseID, expectedArtifactID),
		"manifest":      database.Model(&storage.InputManifestModel{}).Where("id = ? AND project_id = ?", expectedManifestID, projectID),
		"proposal":      database.Model(&storage.OutputProposalModel{}).Where("id = ? AND project_id = ?", expectedProposalID, projectID),
	} {
		var count int64
		if err := query.Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s stable resource count = %d", label, count)
		}
	}
}

func designImportPGDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	schema := "design_import_service_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = base.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`).Error
	})
	scopedDSN := designImportSearchPathDSN(t, dsn, schema)
	database, err := gorm.Open(postgres.Open(scopedDSN), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(context.Background(), sqlDatabase); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDatabase.Close() })
	return database
}

func designImportSearchPathDSN(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}

var _ content.Store = (*pgCanaryContentStore)(nil)
