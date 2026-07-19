package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/agent"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/sandbox"
	postgresdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAgentPostgresStoreClosesImmutableFencedAttemptLoop(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "agent_store_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	scopedDSN := postgresDSNWithSearchPath(t, dsn, schema)
	database, err := sql.Open("pgx", scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := Up(ctx, database); err != nil {
		t.Fatalf("migrations.Up failed: %v", err)
	}

	seed := seedRepositoryCandidateCanary(t, ctx, database)
	if err := database.QueryRowContext(ctx, `
SELECT contract_hash FROM application_build_contracts WHERE id = $1
`, seed.contractID).Scan(&seed.contractHash); err != nil {
		t.Fatal(err)
	}
	candidate := createSandboxCandidateCanary(
		t, ctx, database, seed, "agent-store", repository.TreeContentStore,
	)
	candidate = acquireSandboxCandidateLeaseCanary(t, ctx, database, seed.actorID, candidate)
	sessionID := uuid.New()
	insertSandboxSessionCanary(t, ctx, database, seed, candidate.id, sessionID, uuid.Nil, true)
	assertSandboxTransition(t, ctx, database, sessionID, 1, 1, "starting", seed.actorID, "runner allocated", uuid.Nil, 2, 1, candidate.version)
	assertSandboxTransition(t, ctx, database, sessionID, 2, 1, "ready", seed.actorID, "runner ready", uuid.Nil, 3, 1, candidate.version)

	gormDatabase, err := gorm.Open(postgresdriver.New(postgresdriver.Config{Conn: database}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := agent.NewPostgresStore(gormDatabase)
	if err != nil {
		t.Fatal(err)
	}

	var templatePayload []byte
	if err := database.QueryRowContext(ctx, `
SELECT jsonb_agg(jsonb_build_object(
  'id', template_release_id::text,
  'contentHash', template_release_content_hash
) ORDER BY template_release_id::text)
FROM sandbox_session_template_releases
WHERE session_id = $1
`, sessionID).Scan(&templatePayload); err != nil {
		t.Fatal(err)
	}
	var templateReleases []repository.ExactReference
	if err := json.Unmarshal(templatePayload, &templateReleases); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	contract := repository.ExactReference{ID: seed.contractID.String(), ContentHash: seed.contractHash}
	pack, err := agent.NewContextPack(agent.NewContextPackInput{
		ID: uuid.NewString(), ProjectID: seed.projectID.String(), CandidateID: candidate.id.String(),
		BaseCandidateTreeHash: candidate.treeHash, BuildContract: contract,
		Items: []agent.ContextItem{{
			Key: "build-contract", Kind: agent.ContextBuildContract, Source: &contract, Required: true,
			Content: agentStoreBlob(seed.contractID, "context", 512),
		}},
		CreatedBy: seed.actorID.String(),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := agent.NewTaskCapsule(agent.NewTaskCapsuleInput{
		ID: uuid.NewString(), TaskKey: "vertical-conversation", ProjectID: seed.projectID.String(),
		SandboxSessionID: sessionID.String(), CandidateID: candidate.id.String(),
		CandidateVersion: uint64(candidate.version), CandidateSessionEpoch: uint64(candidate.sessionEpoch),
		CandidateWriterLeaseEpoch: uint64(candidate.writerLeaseEpoch),
		BaseCandidateTreeHash:     candidate.treeHash, BuildContract: contract,
		TemplateReleases: templateReleases,
		Objective:        "Implement one exact vertical conversation slice.",
		ObligationIDs:    []string{"OBL-REPOSITORY"}, AcceptanceCriterionIDs: []string{"AC-REPOSITORY"},
		ReadSet: []string{"apps"}, WriteSet: []string{"apps/web/src/features/conversation"},
		ProtectedPaths:         []string{".github", "apps/web/protected"},
		Preconditions:          []string{"The exact BuildContract and Candidate are ready."},
		Postconditions:         []string{"The contract-bound verification command passes."},
		VerificationCommandIDs: []string{"test-contract"},
		AllowedTools:           []string{"file.read", "file.write", "file.search", "shell.exec"},
		NetworkPolicy:          agent.NetworkPolicy{Mode: "none"},
		Budgets: agent.TaskBudgets{
			WallTimeSeconds: 900, MaxInputTokens: 200000, MaxOutputTokens: 50000,
			MaxCommands: 100, MaxLogBytes: 4 << 20, MaxPatchBytes: 16 << 20,
		},
		OutputSchemaHash: applicationBuildContractCanaryDigest("agent-store-output-schema"),
		CreatedBy:        seed.actorID.String(),
	}, pack, now.Add(time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}

	plan, err := store.SavePlan(ctx, pack, capsule)
	if err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if plan.Replayed || plan.ContextPack.ContentHash != pack.ContentHash ||
		plan.TaskCapsule.ContentHash != capsule.ContentHash {
		t.Fatalf("unexpected saved plan: %#v", plan)
	}
	replayedPlan, err := store.SavePlan(ctx, pack, capsule)
	if err != nil || !replayedPlan.Replayed {
		t.Fatalf("exact plan replay: plan=%#v err=%v", replayedPlan, err)
	}

	executor := agent.ExecutorIdentity{
		Adapter: "codex-cli", Provider: "openai", Model: "qualified-model",
		RunnerImageDigest: applicationBuildContractCanaryDigest("agent-store-runner"),
		ModelPolicyHash:   applicationBuildContractCanaryDigest("agent-store-policy"),
		ParametersHash:    applicationBuildContractCanaryDigest("agent-store-parameters"),
		PromptHash:        applicationBuildContractCanaryDigest("agent-store-prompt"),
		OutputSchemaHash:  capsule.OutputSchemaHash,
		ToolchainHash:     applicationBuildContractCanaryDigest("agent-store-toolchain"),
	}
	initial, err := agent.NewAttempt(agent.NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: seed.actorID.String(), Executor: executor,
	}, capsule, pack, now.Add(2*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	created, replayed, err := store.CreateAttempt(ctx, "agent-store-create-1", initial)
	if err != nil || replayed || created.State != agent.AttemptPending {
		t.Fatalf("CreateAttempt: attempt=%#v replayed=%t err=%v", created, replayed, err)
	}

	created, err = store.Advance(ctx, created.ID, agent.AdvanceAttemptInput{
		ExpectedVersion: created.Version, ExpectedFenceEpoch: created.FenceEpoch,
		ActorID: seed.actorID.String(), Target: agent.AttemptReady, Reason: "exact plan admitted",
	})
	if err != nil {
		t.Fatalf("pending -> ready: %v", err)
	}
	created, err = store.Advance(ctx, created.ID, agent.AdvanceAttemptInput{
		ExpectedVersion: created.Version, ExpectedFenceEpoch: created.FenceEpoch,
		ActorID: seed.actorID.String(), Target: agent.AttemptQueued, Reason: "queued for a qualified runner",
	})
	if err != nil {
		t.Fatalf("ready -> queued: %v", err)
	}
	created, err = store.Claim(ctx, created.ID, created.Version, seed.actorID.String(), "runner-a", time.Minute)
	if err != nil || created.State != agent.AttemptClaimed || created.FenceEpoch != 1 {
		t.Fatalf("claim: attempt=%#v err=%v", created, err)
	}
	created, err = store.Renew(
		ctx, created.ID, created.Version, created.FenceEpoch,
		seed.actorID.String(), "runner-a", 2*time.Minute,
	)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	created, err = store.Advance(ctx, created.ID, agent.AdvanceAttemptInput{
		ExpectedVersion: created.Version, ExpectedFenceEpoch: created.FenceEpoch,
		ActorID: seed.actorID.String(), WorkerID: "runner-a", Target: agent.AttemptRunning,
		Reason: "digest-pinned Runner started",
	})
	if err != nil {
		t.Fatalf("claimed -> running: %v", err)
	}
	patch := agentStoreBlob(uuid.MustParse(created.ID), "patch", 1024)
	structured := agentStoreBlob(uuid.MustParse(created.ID), "structured", 512)
	stdout := agentStoreBlob(uuid.MustParse(created.ID), "stdout", 256)
	created, err = store.Advance(ctx, created.ID, agent.AdvanceAttemptInput{
		ExpectedVersion: created.Version, ExpectedFenceEpoch: created.FenceEpoch,
		ActorID: seed.actorID.String(), WorkerID: "runner-a", Target: agent.AttemptPatchReady,
		Reason: "platform captured the worktree diff", Evidence: agent.AttemptEvidence{
			Patch: &patch, StructuredResult: &structured, Stdout: &stdout,
		},
	})
	if err != nil {
		t.Fatalf("running -> patch_ready: %v", err)
	}
	created, err = store.Advance(ctx, created.ID, agent.AdvanceAttemptInput{
		ExpectedVersion: created.Version, ExpectedFenceEpoch: created.FenceEpoch,
		ActorID: seed.actorID.String(), WorkerID: "runner-a", Target: agent.AttemptValidating,
		Reason: "platform verifier started",
	})
	if err != nil {
		t.Fatalf("patch_ready -> validating: %v", err)
	}
	validation := agentStoreBlob(uuid.MustParse(created.ID), "validation", 512)
	created, err = store.Advance(ctx, created.ID, agent.AdvanceAttemptInput{
		ExpectedVersion: created.Version, ExpectedFenceEpoch: created.FenceEpoch,
		ActorID: seed.actorID.String(), WorkerID: "runner-a", Target: agent.AttemptReviewReady,
		Reason: "independent verification passed", Evidence: agent.AttemptEvidence{Validation: &validation},
	})
	if err != nil || created.State != agent.AttemptReviewReady || created.Lease != nil ||
		created.FinishedAt == nil {
		t.Fatalf("validating -> review_ready: attempt=%#v err=%v", created, err)
	}

	mergeOperation := repository.FileOperation{
		ID: "agent-merge-operation-1", Kind: repository.OperationUpsert,
		Path:         "apps/web/src/features/conversation/page.tsx",
		ExpectedHash: applicationBuildContractCanaryDigest("agent-store-old-file"),
		ContentHash:  applicationBuildContractCanaryDigest("agent-store-new-file"),
		ByteSize:     128, Mode: "100644",
	}
	mergedTreeHash := applicationBuildContractCanaryDigest("agent-store-planned-tree")
	mergePlan, err := agent.NewPatchMergePlanRecord(agent.NewPatchMergePlanInput{
		ID: uuid.NewString(), OperationID: "agent-store-merge-1",
		ProjectID: seed.projectID.String(), SandboxSessionID: sessionID.String(),
		CandidateID: candidate.id.String(), AttemptID: created.ID, AttemptVersion: created.Version,
		PatchReference:         patch,
		PatchRawHash:           applicationBuildContractCanaryDigest("agent-store-patch-raw"),
		PatchContentHash:       applicationBuildContractCanaryDigest("agent-store-platform-patch"),
		ExpectedSessionVersion: 3, ExpectedSessionEpoch: uint64(candidate.sessionEpoch),
		ExpectedCandidateVersion:         uint64(candidate.version),
		ExpectedCandidateJournalSequence: 0,
		ExpectedWriterLeaseEpoch:         uint64(candidate.writerLeaseEpoch),
		CreatedBy:                        seed.actorID.String(),
	}, agent.PlatformPatchMergePlan{
		BaseTreeHash: candidate.treeHash, CurrentTreeHash: candidate.treeHash,
		ProposedTreeHash: applicationBuildContractCanaryDigest("agent-store-proposed-tree"),
		PlannedTreeHash:  mergedTreeHash,
		Operations:       []repository.FileOperation{mergeOperation},
		Conflicts:        []agent.PatchMergeConflict{},
	}, now.Add(3*time.Microsecond))
	if err != nil {
		t.Fatalf("NewPatchMergePlanRecord: %v", err)
	}
	persistedMerge, mergeReplayed, err := store.SavePatchMergePlan(ctx, mergePlan)
	if err != nil || mergeReplayed || persistedMerge.ContentHash != mergePlan.ContentHash {
		t.Fatalf("SavePatchMergePlan: plan=%#v replayed=%t err=%v", persistedMerge, mergeReplayed, err)
	}
	persistedMerge, mergeReplayed, err = store.SavePatchMergePlan(ctx, mergePlan)
	if err != nil || !mergeReplayed || persistedMerge.ContentHash != mergePlan.ContentHash {
		t.Fatalf("replay PatchMergePlan: plan=%#v replayed=%t err=%v", persistedMerge, mergeReplayed, err)
	}
	foundMerge, found, err := store.FindPatchMergePlanByOperation(
		ctx, seed.projectID.String(), seed.actorID.String(), "agent-store-merge-1",
	)
	if err != nil || !found || foundMerge.ID != mergePlan.ID {
		t.Fatalf("FindPatchMergePlanByOperation: plan=%#v found=%t err=%v", foundMerge, found, err)
	}
	resolvedProject, err := store.ResolvePatchMergeProject(ctx, mergePlan.ID)
	if err != nil || resolvedProject != seed.projectID.String() {
		t.Fatalf("ResolvePatchMergeProject: project=%q err=%v", resolvedProject, err)
	}

	mergeCandidateBefore := candidate
	mergeBeforePointer := repository.TreeBlobPointer{
		Store: candidate.treeStore, OwnerID: candidate.treeOwnerID.String(), Ref: candidate.treeRef,
		ContentObjectHash: candidate.treeContentHash, TreeHash: candidate.treeHash,
		FileCount: candidate.fileCount, ByteSize: candidate.byteSize,
	}
	mergeAfterPointer := repository.TreeBlobPointer{
		Store: repository.TreeContentStore, OwnerID: candidate.id.String(),
		Ref:               "agent-store-merge-tree-" + mergePlan.ID,
		ContentObjectHash: applicationBuildContractCanaryDigest("agent-store-merge-tree-content"),
		TreeHash:          mergedTreeHash, FileCount: 1, ByteSize: 128,
	}
	candidate = appendAgentPatchJournalCanary(
		t, ctx, database, candidate, seed.actorID, "agent", mergeOperation,
		mergeBeforePointer, mergeAfterPointer,
	)
	mergeApplication, err := agent.NewPatchMergeApplication(
		mergePlan,
		repository.BatchMutationResult{
			Entries: []repository.JournalEntry{{
				CandidateID: candidate.id.String(), Sequence: uint64(candidate.journalSequence),
				CandidateFrom: uint64(mergeCandidateBefore.version), CandidateTo: uint64(candidate.version),
				SessionEpoch: uint64(candidate.sessionEpoch), LeaseEpoch: uint64(candidate.writerLeaseEpoch),
				ActorID: seed.actorID.String(), Attribution: "agent", Operation: mergeOperation,
				BeforeTree: mergeBeforePointer.TreeHash, AfterTree: mergeAfterPointer.TreeHash,
				CreatedAt: now.Add(4 * time.Microsecond),
			}},
			BeforeTree: mergeBeforePointer, AfterTree: mergeAfterPointer,
			FinalCandidateVersion: uint64(candidate.version),
		},
		seed.actorID.String(), now.Add(5*time.Microsecond),
	)
	if err != nil {
		t.Fatalf("NewPatchMergeApplication: %v", err)
	}
	persistedMergeApplication, applicationReplayed, err := store.SavePatchMergeApplication(ctx, mergeApplication)
	if err != nil || applicationReplayed || persistedMergeApplication.ContentHash != mergeApplication.ContentHash {
		t.Fatalf("SavePatchMergeApplication: application=%#v replayed=%t err=%v", persistedMergeApplication, applicationReplayed, err)
	}

	sessionStore, err := sandbox.NewStore(gormDatabase)
	if err != nil {
		t.Fatal(err)
	}
	mergedSession, err := sessionStore.SyncCandidate(
		ctx, seed.projectID.String(), sessionID.String(), 3,
		uint64(candidate.sessionEpoch), seed.actorID.String(),
	)
	if err != nil {
		t.Fatalf("sync merged Candidate into SandboxSession: %v", err)
	}
	mergedSessionView := mergedSession.Snapshot()
	if mergedSessionView.Candidate.Version != uint64(candidate.version) ||
		mergedSessionView.Candidate.TreeHash != mergedTreeHash {
		t.Fatalf("merged Session projection drifted: %#v", mergedSessionView.Candidate)
	}

	undoOperation := repository.FileOperation{
		ID: "agent-undo-operation-1", Kind: repository.OperationUpsert,
		Path: mergeOperation.Path, ExpectedHash: mergeOperation.ContentHash,
		ContentHash: mergeOperation.ExpectedHash, ByteSize: 128, Mode: "100644",
	}
	undoPlan, err := agent.NewPatchUndoPlanRecord(agent.NewPatchUndoPlanInput{
		ID: uuid.NewString(), OperationID: "agent-store-undo-1",
		ProjectID: seed.projectID.String(), SandboxSessionID: sessionID.String(),
		CandidateID: candidate.id.String(), MergeID: mergePlan.ID,
		MergePlanContentHash:             mergePlan.ContentHash,
		MergeApplicationContentHash:      mergeApplication.ContentHash,
		ExpectedSessionVersion:           mergedSessionView.Version,
		ExpectedSessionEpoch:             uint64(candidate.sessionEpoch),
		ExpectedCandidateVersion:         uint64(candidate.version),
		ExpectedCandidateJournalSequence: uint64(candidate.journalSequence),
		ExpectedWriterLeaseEpoch:         uint64(candidate.writerLeaseEpoch),
		CreatedBy:                        seed.actorID.String(),
	}, agent.PlatformPatchUndoPlan{
		MergeID: mergePlan.ID, MergeBeforeTreeHash: mergeBeforePointer.TreeHash,
		MergedTreeHash: mergeAfterPointer.TreeHash, CurrentTreeHash: mergeAfterPointer.TreeHash,
		PlannedTreeHash: mergeBeforePointer.TreeHash,
		Operations:      []repository.FileOperation{undoOperation},
		Conflicts:       []agent.PatchMergeConflict{},
	}, now.Add(6*time.Microsecond))
	if err != nil {
		t.Fatalf("NewPatchUndoPlanRecord: %v", err)
	}
	persistedUndo, undoReplayed, err := store.SavePatchUndoPlan(ctx, undoPlan)
	if err != nil || undoReplayed || persistedUndo.ContentHash != undoPlan.ContentHash {
		t.Fatalf("SavePatchUndoPlan: plan=%#v replayed=%t err=%v", persistedUndo, undoReplayed, err)
	}
	persistedUndo, undoReplayed, err = store.SavePatchUndoPlan(ctx, undoPlan)
	if err != nil || !undoReplayed || persistedUndo.ContentHash != undoPlan.ContentHash {
		t.Fatalf("replay PatchUndoPlan: plan=%#v replayed=%t err=%v", persistedUndo, undoReplayed, err)
	}

	undoCandidateBefore := candidate
	undoAfterPointer := repository.TreeBlobPointer{
		Store: repository.TreeContentStore, OwnerID: candidate.id.String(),
		Ref:               "agent-store-undo-tree-" + undoPlan.ID,
		ContentObjectHash: mergeBeforePointer.ContentObjectHash,
		TreeHash:          mergeBeforePointer.TreeHash,
		FileCount:         mergeBeforePointer.FileCount, ByteSize: mergeBeforePointer.ByteSize,
	}
	candidate = appendAgentPatchJournalCanary(
		t, ctx, database, candidate, seed.actorID, "restore", undoOperation,
		mergeAfterPointer, undoAfterPointer,
	)
	undoApplication, err := agent.NewPatchUndoApplication(
		undoPlan,
		repository.BatchMutationResult{
			Entries: []repository.JournalEntry{{
				CandidateID: candidate.id.String(), Sequence: uint64(candidate.journalSequence),
				CandidateFrom: uint64(undoCandidateBefore.version), CandidateTo: uint64(candidate.version),
				SessionEpoch: uint64(candidate.sessionEpoch), LeaseEpoch: uint64(candidate.writerLeaseEpoch),
				ActorID: seed.actorID.String(), Attribution: "restore", Operation: undoOperation,
				BeforeTree: mergeAfterPointer.TreeHash, AfterTree: undoAfterPointer.TreeHash,
				CreatedAt: now.Add(7 * time.Microsecond),
			}},
			BeforeTree: mergeAfterPointer, AfterTree: undoAfterPointer,
			FinalCandidateVersion: uint64(candidate.version),
		},
		seed.actorID.String(), now.Add(8*time.Microsecond),
	)
	if err != nil {
		t.Fatalf("NewPatchUndoApplication: %v", err)
	}
	persistedUndoApplication, undoApplicationReplayed, err := store.SavePatchUndoApplication(ctx, undoApplication)
	if err != nil || undoApplicationReplayed || persistedUndoApplication.ContentHash != undoApplication.ContentHash {
		t.Fatalf("SavePatchUndoApplication: application=%#v replayed=%t err=%v", persistedUndoApplication, undoApplicationReplayed, err)
	}
	persistedUndoApplication, undoApplicationReplayed, err = store.SavePatchUndoApplication(ctx, undoApplication)
	if err != nil || !undoApplicationReplayed || persistedUndoApplication.ContentHash != undoApplication.ContentHash {
		t.Fatalf("replay PatchUndoApplication: application=%#v replayed=%t err=%v", persistedUndoApplication, undoApplicationReplayed, err)
	}
	loadedUndo, found, err := store.GetPatchUndoApplication(ctx, seed.projectID.String(), undoPlan.ID)
	if err != nil || !found || loadedUndo.ContentHash != undoApplication.ContentHash {
		t.Fatalf("GetPatchUndoApplication: application=%#v found=%t err=%v", loadedUndo, found, err)
	}
	mergeHistory, err := store.ListPatchMergePlans(ctx, seed.projectID.String(), created.ID, 50)
	if err != nil || len(mergeHistory) != 1 || mergeHistory[0].ID != mergePlan.ID {
		t.Fatalf("ListPatchMergePlans: plans=%#v err=%v", mergeHistory, err)
	}
	appliedUndo, found, err := store.FindAppliedPatchUndoPlan(
		ctx, seed.projectID.String(), mergePlan.ID,
	)
	if err != nil || !found || appliedUndo.ID != undoPlan.ID ||
		appliedUndo.ContentHash != undoPlan.ContentHash {
		t.Fatalf("FindAppliedPatchUndoPlan: plan=%#v found=%t err=%v", appliedUndo, found, err)
	}
	restoredSession, err := sessionStore.SyncCandidate(
		ctx, seed.projectID.String(), sessionID.String(), mergedSessionView.Version,
		uint64(candidate.sessionEpoch), seed.actorID.String(),
	)
	if err != nil {
		t.Fatalf("sync restored Candidate into SandboxSession: %v", err)
	}
	restoredSessionView := restoredSession.Snapshot()
	if candidate.treeHash != mergeBeforePointer.TreeHash ||
		restoredSessionView.Candidate.TreeHash != mergeBeforePointer.TreeHash ||
		restoredSessionView.Candidate.Version != uint64(candidate.version) {
		t.Fatalf("restored Candidate/Session drifted: candidate=%#v session=%#v", candidate, restoredSessionView.Candidate)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE agent_patch_undo_plans SET operation_id = operation_id || '-changed' WHERE id = $1
`, undoPlan.ID); err == nil || !strings.Contains(strings.ToLower(err.Error()), "immutable") {
		t.Fatalf("Undo plan mutation was not blocked as immutable: %v", err)
	}

	events, err := store.ListEvents(ctx, seed.projectID.String(), created.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 8 || events[0].StateFrom != agent.AttemptPending ||
		events[len(events)-1].StateTo != agent.AttemptReviewReady {
		t.Fatalf("unexpected event chain: %#v", events)
	}
	stream := &agentStreamEventStoreFake{}
	relay, err := agent.NewStreamRelay(
		gormDatabase,
		stream,
		agent.StreamRelayConfig{
			BatchSize: 20, PollInterval: time.Second, ClaimTTL: 30 * time.Second,
			MaxAttempts: 5, PublishTimeout: 5 * time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := relay.DeliverBatch(ctx)
	if err != nil || delivered != len(events) || len(stream.published) != len(events) {
		t.Fatalf("deliver Agent stream outbox: delivered=%d published=%d err=%v", delivered, len(stream.published), err)
	}
	for index, published := range stream.published {
		var event agent.AttemptEvent
		if err := json.Unmarshal(published.Payload, &event); err != nil {
			t.Fatalf("decode published event %d: %v", index, err)
		}
		if published.SessionID != sessionID.String() || published.SessionEpoch != uint64(candidate.sessionEpoch) ||
			published.Channel != sandbox.ChannelAgent || event.AttemptID != created.ID ||
			event.Sequence != uint64(index+1) || published.AggregateVersion != event.VersionTo {
			t.Fatalf("published event %d drifted: input=%#v event=%#v", index, published, event)
		}
	}
	if err := relay.Readiness(ctx); err != nil {
		t.Fatalf("Agent stream relay readiness: %v", err)
	}
	if delivered, err := relay.DeliverBatch(ctx); err != nil || delivered != 0 {
		t.Fatalf("redelivered acknowledged Agent stream events: delivered=%d err=%v", delivered, err)
	}
	var deliveredRows int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM agent_stream_outbox WHERE delivered_at IS NOT NULL AND stream_sequence IS NOT NULL
`).Scan(&deliveredRows); err != nil || deliveredRows != len(events) {
		t.Fatalf("delivered Agent stream outbox rows=%d err=%v", deliveredRows, err)
	}
	listed, err := store.ListAttempts(ctx, seed.projectID.String(), sessionID.String(), 20)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("ListAttempts: values=%#v err=%v", listed, err)
	}

	replayedAttempt, replayed, err := store.CreateAttempt(ctx, "agent-store-create-1", initial)
	if err != nil || !replayed || replayedAttempt.ID != created.ID ||
		replayedAttempt.State != agent.AttemptReviewReady {
		t.Fatalf("exact Attempt replay: attempt=%#v replayed=%t err=%v", replayedAttempt, replayed, err)
	}
	different, err := agent.NewAttempt(agent.NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: seed.actorID.String(), Executor: executor,
	}, capsule, pack, now.Add(3*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateAttempt(ctx, "agent-store-create-1", different); !errors.Is(err, agent.ErrAgentOperationReplay) {
		t.Fatalf("different idempotency replay error = %v", err)
	}
}

type agentStreamEventStoreFake struct {
	published []sandbox.StreamEventInput
}

func (store *agentStreamEventStoreFake) Publish(
	_ context.Context,
	input sandbox.StreamEventInput,
) (sandbox.StreamEnvelope, error) {
	input.Payload = append(json.RawMessage(nil), input.Payload...)
	store.published = append(store.published, input)
	return sandbox.StreamEnvelope{
		SchemaVersion: sandbox.SandboxStreamSchemaVersion,
		SessionID:     input.SessionID, SessionEpoch: input.SessionEpoch, Channel: input.Channel,
		EventType: input.EventType, Sequence: uint64(len(store.published)),
		AggregateVersion: input.AggregateVersion, CorrelationID: input.CorrelationID,
		Timestamp: time.Now().UTC(), Payload: append(json.RawMessage(nil), input.Payload...),
	}, nil
}

func (*agentStreamEventStoreFake) Replay(
	context.Context,
	string,
	uint64,
	sandbox.StreamChannel,
	uint64,
	int,
) ([]sandbox.StreamEnvelope, uint64, error) {
	return nil, 0, nil
}

func (*agentStreamEventStoreFake) ReadAfter(
	context.Context,
	string,
	uint64,
	sandbox.StreamChannel,
	uint64,
	int,
	time.Duration,
) ([]sandbox.StreamEnvelope, error) {
	return nil, nil
}

func agentStoreBlob(ownerID uuid.UUID, key string, size int64) agent.BlobReference {
	return agent.BlobReference{
		Store: "content", OwnerID: ownerID.String(), Ref: "agent-store-" + key,
		ContentHash: applicationBuildContractCanaryDigest("agent-store-blob-" + key), ByteSize: size,
	}
}

func appendAgentPatchJournalCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	candidate sandboxCandidateCanary,
	actorID uuid.UUID,
	attribution string,
	operation repository.FileOperation,
	before, after repository.TreeBlobPointer,
) sandboxCandidateCanary {
	t.Helper()
	if operation.Kind != repository.OperationUpsert {
		t.Fatalf("Agent patch journal canary only supports upserts: %#v", operation)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO candidate_workspace_journal (
  candidate_id, sequence, candidate_version_from, candidate_version_to,
  session_epoch, writer_lease_epoch, actor_id, attribution,
  operation_id, operation_kind, path, expected_content_hash,
  content_hash, byte_size, file_mode,
  before_tree_store, before_tree_owner_id, before_tree_ref, before_tree_content_hash, before_tree_hash,
  after_tree_store, after_tree_owner_id, after_tree_ref, after_tree_content_hash, after_tree_hash,
  after_tree_file_count, after_tree_byte_size
) VALUES (
  $1, $2::bigint, $3::bigint, $3::bigint + 1, $4::bigint, $5::bigint, $6, $7,
  $8, $9, $10, NULLIF($11, ''), $12, $13, $14,
  $15, $16, $17, $18, $19,
  $20, $21, $22, $23, $24, $25, $26
)
`, candidate.id, candidate.journalSequence+1, candidate.version,
		candidate.sessionEpoch, candidate.writerLeaseEpoch, actorID, attribution,
		operation.ID, operation.Kind, operation.Path, operation.ExpectedHash,
		operation.ContentHash, operation.ByteSize, operation.Mode,
		before.Store, before.OwnerID, before.Ref, before.ContentObjectHash, before.TreeHash,
		after.Store, after.OwnerID, after.Ref, after.ContentObjectHash, after.TreeHash,
		after.FileCount, after.ByteSize); err != nil {
		t.Fatalf("append %s Agent patch journal: %v", attribution, err)
	}
	return readSandboxCandidateCanary(t, ctx, database, candidate.id)
}
