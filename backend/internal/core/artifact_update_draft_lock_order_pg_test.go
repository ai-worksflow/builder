package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type applyAutosaveArtifactLockContextKey struct{}

type applyAutosaveLineageLockContextKey struct{}

type synchronizedContentStore struct {
	mu       sync.Mutex
	delegate content.Store
}

func (s *synchronizedContentStore) PutPending(
	ctx context.Context,
	projectID string,
	aggregateType string,
	aggregateID string,
	schemaVersion int,
	payload json.RawMessage,
) (content.Reference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delegate.PutPending(ctx, projectID, aggregateType, aggregateID, schemaVersion, payload)
}

func (s *synchronizedContentStore) Finalize(ctx context.Context, contentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delegate.Finalize(ctx, contentID)
}

func (s *synchronizedContentStore) Abort(ctx context.Context, contentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delegate.Abort(ctx, contentID)
}

func (s *synchronizedContentStore) Get(
	ctx context.Context,
	contentID string,
	expectedHash string,
) (content.StoredContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delegate.Get(ctx, contentID, expectedHash)
}

func TestProposalApplyAndAutosaveUseArtifactThenDraftLockOrderPostgres(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	rawStore, _, projectID, ownerID := newArtifactLineageFixture(t, database)
	store := &synchronizedContentStore{delegate: rawStore}
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := NewArtifactService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := NewProposalService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}

	initialPayload := json.RawMessage(`{
		"summary":"Before",
		"blocks":[{
			"id":"requirement-1","type":"requirement","requirementId":"REQ-1","priority":"must",
			"text":"Autosave and Proposal Apply share one lock order.",
			"acceptanceCriteria":[{"id":"AC-1","statement":"Concurrent draft mutation never deadlocks."}]
		}]
	}`)
	created, err := artifacts.Create(ctx, projectID.String(), ownerID.String(), CreateArtifactInput{
		Kind: "product_requirements", Title: "Apply autosave lock order", Content: initialPayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	base, err := artifacts.CreateRevision(
		ctx, created.Artifact.ID, ownerID.String(), created.Draft.ETag,
		CreateRevisionInput{ChangeSummary: "Initialize Apply/autosave race"},
	)
	if err != nil {
		t.Fatal(err)
	}
	baseRef := VersionRef{ArtifactID: base.ArtifactID, RevisionID: base.ID, ContentHash: base.ContentHash}
	manifest, err := proposals.CreateManifest(ctx, projectID.String(), ownerID.String(), CreateManifestInput{
		JobType: "derive_requirements", BaseRevision: &baseRef,
		Constraints: json.RawMessage(`{}`), OutputSchemaVersion: "requirements-proposal/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := proposals.CreateProposal(ctx, projectID.String(), ownerID.String(), CreateProposalInput{
		ManifestID: manifest.ID, ArtifactID: created.Artifact.ID,
		Operations: []domain.ProposalOperation{{
			ID: "proposal-summary", Kind: domain.OperationReplace, Path: "/summary",
			Value: json.RawMessage(`"Applied Proposal"`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err = proposals.Decide(ctx, proposal.ID, ownerID.String(), DecideProposalInput{
		OperationID: "proposal-summary", Decision: domain.DecisionAccepted, Version: proposal.Version,
	})
	if err != nil {
		t.Fatal(err)
	}

	applyMarker := &struct{}{}
	applyHasLineageLocks := make(chan struct{})
	releaseApply := make(chan struct{})
	var pauseApplyOnce sync.Once
	applyCallbackName := "test:pause_apply_with_artifact_lock_" + uuid.NewString()
	if err := database.Callback().Query().After("gorm:query").Register(applyCallbackName, func(query *gorm.DB) {
		if query.Statement.Context.Value(applyAutosaveLineageLockContextKey{}) != applyMarker ||
			query.Statement.Table != (storage.ArtifactHealthModel{}).TableName() || query.Error != nil {
			return
		}
		if _, locked := query.Statement.Clauses["FOR"]; !locked {
			return
		}
		pauseApplyOnce.Do(func() {
			close(applyHasLineageLocks)
			<-releaseApply
		})
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = database.Callback().Query().Remove(applyCallbackName)
	}()

	autosaveMarker := &struct{}{}
	autosaveAttemptedArtifactLock := make(chan struct{})
	var signalAutosaveOnce sync.Once
	autosaveCallbackName := "test:autosave_attempts_artifact_first_" + uuid.NewString()
	if err := database.Callback().Query().Before("gorm:query").Register(autosaveCallbackName, func(query *gorm.DB) {
		if query.Statement.Context.Value(applyAutosaveArtifactLockContextKey{}) != autosaveMarker ||
			query.Statement.Table != (storage.ArtifactModel{}).TableName() || query.Error != nil {
			return
		}
		if _, locked := query.Statement.Clauses["FOR"]; !locked {
			return
		}
		signalAutosaveOnce.Do(func() {
			close(autosaveAttemptedArtifactLock)
		})
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = database.Callback().Query().Remove(autosaveCallbackName)
	}()

	released := false
	defer func() {
		if !released {
			close(releaseApply)
		}
	}()
	runContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	applyResult := make(chan error, 1)
	applyContext := context.WithValue(runContext, applyAutosaveLineageLockContextKey{}, applyMarker)
	go func() {
		_, applyErr := proposals.Apply(
			applyContext, proposal.ID, ownerID.String(), ApplyProposalInput{Version: proposal.Version},
		)
		applyResult <- applyErr
	}()
	select {
	case <-applyHasLineageLocks:
	case <-runContext.Done():
		t.Fatalf("Proposal Apply did not acquire its lineage locks: %v", runContext.Err())
	}

	autosavePayload := json.RawMessage(`{
		"summary":"Autosaved draft",
		"blocks":[{
			"id":"requirement-1","type":"requirement","requirementId":"REQ-1","priority":"must",
			"text":"Autosave and Proposal Apply share one lock order.",
			"acceptanceCriteria":[{"id":"AC-1","statement":"Concurrent draft mutation never deadlocks."}]
		}]
	}`)
	autosaveResult := make(chan error, 1)
	autosaveContext := context.WithValue(runContext, applyAutosaveArtifactLockContextKey{}, autosaveMarker)
	go func() {
		_, autosaveErr := artifacts.UpdateDraft(
			autosaveContext, created.Draft.ID, ownerID.String(), created.Draft.ETag,
			UpdateDraftInput{Content: autosavePayload},
		)
		autosaveResult <- autosaveErr
	}()
	select {
	case <-autosaveAttemptedArtifactLock:
	case <-runContext.Done():
		t.Fatalf("autosave did not attempt the artifact lock before its draft mutation: %v", runContext.Err())
	}

	close(releaseApply)
	released = true
	var applyErr, autosaveErr error
	select {
	case applyErr = <-applyResult:
	case <-runContext.Done():
		t.Fatalf("Proposal Apply did not finish: %v", runContext.Err())
	}
	select {
	case autosaveErr = <-autosaveResult:
	case <-runContext.Done():
		t.Fatalf("autosave did not finish: %v", runContext.Err())
	}

	for name, outcomeErr := range map[string]error{"Proposal Apply": applyErr, "autosave": autosaveErr} {
		if outcomeErr == nil {
			continue
		}
		message := strings.ToLower(outcomeErr.Error())
		if strings.Contains(message, "40p01") || strings.Contains(message, "deadlock detected") {
			t.Fatalf("%s hit a PostgreSQL deadlock: %v", name, outcomeErr)
		}
		if !errors.Is(outcomeErr, ErrConflict) && !errors.Is(outcomeErr, ErrProposalStale) {
			t.Fatalf("%s returned a non-canonical concurrency error: %v", name, outcomeErr)
		}
	}
	successes := 0
	if applyErr == nil {
		successes++
	}
	if autosaveErr == nil {
		successes++
	}
	if successes != 1 {
		t.Fatalf("Apply/autosave race produced %d successes; apply=%v autosave=%v", successes, applyErr, autosaveErr)
	}
}
