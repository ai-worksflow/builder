package conversation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

func TestGORMStoreSummaryCheckpointPrefixReviewChainAndConcurrentSiblingApprovalPostgres(t *testing.T) {
	database, cleanup := conversationPostgresDatabase(t)
	defer cleanup()
	store, err := NewGORMStore(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	creatorID, reviewerID, alternateReviewerID := uuid.New(), uuid.New(), uuid.New()
	projectID := uuid.New()
	for index, userID := range []uuid.UUID{creatorID, reviewerID, alternateReviewerID} {
		if err := database.Create(&storage.UserModel{
			ID: userID, Email: "summary-checkpoint-" + uuid.NewString() + "@example.com",
			DisplayName: "Summary reviewer " + string(rune('A'+index)), PasswordHash: "not-used",
			CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Summary checkpoint chain", Description: "", Lifecycle: "active", Version: 1,
		CreatedBy: creatorID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectMemberModel{
		ProjectID: projectID, UserID: creatorID, Role: string(core.RoleOwner),
		JoinedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	conversation, err := store.CreateConversation(ctx, projectID, creatorID, "Checkpoint chain")
	if err != nil {
		t.Fatal(err)
	}
	appendMessages := func(contents ...string) []Message {
		t.Helper()
		messages := make([]Message, 0, len(contents))
		for _, content := range contents {
			message, appendErr := store.AppendUserMessage(ctx, projectID, uuid.MustParse(conversation.ID), creatorID, content)
			if appendErr != nil {
				t.Fatal(appendErr)
			}
			messages = append(messages, message)
		}
		return messages
	}
	firstMessages := appendMessages("first requirement", "second requirement", "third requirement")
	conversationID := uuid.MustParse(conversation.ID)

	if _, err := store.CreateSummaryCheckpoint(ctx, projectID, conversationID, creatorID,
		ConversationETag(conversation.ID, conversation.Version+1), CreateSummaryCheckpointInput{
			ThroughMessageID: firstMessages[2].ID, Summary: "stale conversation precondition",
		}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("stale conversation ETag error=%v", err)
	}
	first, err := store.CreateSummaryCheckpoint(ctx, projectID, conversationID, creatorID,
		conversation.ETag, CreateSummaryCheckpointInput{
			ThroughMessageID: firstMessages[2].ID, Summary: "The first three requirements are reviewed together.",
		})
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := uint64(0)
	wantPrefix := conversationPrefixGenesis(conversationID)
	for _, message := range firstMessages {
		wantBytes += uint64(len(message.Content))
		wantPrefix, err = advanceConversationPrefixHash(wantPrefix, message)
		if err != nil {
			t.Fatal(err)
		}
	}
	if first.PreviousCheckpointID != nil || first.ThroughMessageID != firstMessages[2].ID ||
		first.ThroughSequence != 3 || first.MessageCount != 3 || first.ContentBytes != wantBytes ||
		first.PrefixHash != sha256Ref(wantPrefix) || first.HashAlgorithm != ConversationPrefixHashAlgorithm ||
		first.SummaryHash != sha256Ref(sha256Bytes([]byte(first.Summary))) ||
		first.Status != SummaryCheckpointPendingReview || first.Version != 1 ||
		first.ETag != SummaryCheckpointETag(first.ID, 1) {
		t.Fatalf("first checkpoint did not bind the complete prefix: %+v", first)
	}
	firstSources, err := store.ListSummaryCheckpointSourceMessages(ctx, projectID, conversationID,
		uuid.MustParse(first.ID), ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstSources.Items) != 3 || firstSources.Items[0].Sequence != 1 || firstSources.Items[2].Sequence != 3 {
		t.Fatalf("first checkpoint source prefix=%+v", firstSources.Items)
	}

	if _, err := store.DecideSummaryCheckpoint(ctx, projectID, conversationID, uuid.MustParse(first.ID), creatorID,
		first.ETag, DecideSummaryCheckpointInput{Decision: SummaryCheckpointApprove}); !errors.Is(err, core.ErrSelfApproval) {
		t.Fatalf("creator self-approval error=%v", err)
	}
	if _, err := store.DecideSummaryCheckpoint(ctx, projectID, conversationID, uuid.MustParse(first.ID), reviewerID,
		SummaryCheckpointETag(first.ID, first.Version+1), DecideSummaryCheckpointInput{Decision: SummaryCheckpointApprove}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("stale checkpoint ETag error=%v", err)
	}
	first, err = store.DecideSummaryCheckpoint(ctx, projectID, conversationID, uuid.MustParse(first.ID), reviewerID,
		first.ETag, DecideSummaryCheckpointInput{Decision: SummaryCheckpointApprove, Reason: "complete prefix verified"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != SummaryCheckpointApproved || first.Version != 2 || first.ReviewedBy == nil ||
		*first.ReviewedBy != reviewerID.String() || first.ETag != SummaryCheckpointETag(first.ID, 2) {
		t.Fatalf("first approval did not advance checkpoint CAS state: %+v", first)
	}
	afterFirst, err := store.GetConversation(ctx, projectID, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFirst.SummaryCheckpointHeadID == nil || *afterFirst.SummaryCheckpointHeadID != first.ID || afterFirst.Version != 2 {
		t.Fatalf("first approval did not advance conversation head: %+v", afterFirst)
	}

	secondMessages := appendMessages("fourth requirement", "fifth requirement")
	second, err := store.CreateSummaryCheckpoint(ctx, projectID, conversationID, creatorID,
		afterFirst.ETag, CreateSummaryCheckpointInput{
			ThroughMessageID: secondMessages[1].ID, Summary: "The next two requirements extend the approved prefix.",
		})
	if err != nil {
		t.Fatal(err)
	}
	if second.PreviousCheckpointID == nil || *second.PreviousCheckpointID != first.ID ||
		second.ThroughSequence != 5 || second.MessageCount != 5 {
		t.Fatalf("second checkpoint did not extend the approved head: %+v", second)
	}
	secondSources, err := store.ListSummaryCheckpointSourceMessages(ctx, projectID, conversationID,
		uuid.MustParse(second.ID), ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondSources.Items) != 2 || secondSources.Items[0].Sequence != 4 || secondSources.Items[1].Sequence != 5 {
		t.Fatalf("second checkpoint exposed the wrong incremental source: %+v", secondSources.Items)
	}
	second, err = store.DecideSummaryCheckpoint(ctx, projectID, conversationID, uuid.MustParse(second.ID), alternateReviewerID,
		second.ETag, DecideSummaryCheckpointInput{Decision: SummaryCheckpointApprove})
	if err != nil {
		t.Fatal(err)
	}
	afterSecond, err := store.GetConversation(ctx, projectID, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if afterSecond.SummaryCheckpointHeadID == nil || *afterSecond.SummaryCheckpointHeadID != second.ID ||
		afterSecond.Version != 3 {
		t.Fatalf("second chain approval did not advance head: %+v", afterSecond)
	}

	providerTail := appendMessages("provider-visible continuous tail")
	var providerTrigger storage.ConversationMessageModel
	if err := database.Where("id = ?", providerTail[0].ID).Take(&providerTrigger).Error; err != nil {
		t.Fatal(err)
	}
	providerContext, contextProvenance, err := store.loadIntentConversationContext(ctx, conversationID, providerTrigger)
	if err != nil {
		t.Fatal(err)
	}
	if providerContext.ApprovedCheckpoint == nil || providerContext.ApprovedCheckpoint.ID != second.ID ||
		len(providerContext.TailMessages) != 1 || providerContext.TailMessages[0].ID != providerTail[0].ID ||
		contextProvenance.Mode != "checkpoint_tail" || contextProvenance.Tail.FromSequence != 6 ||
		contextProvenance.Tail.ToSequence != 6 {
		t.Fatalf("approved checkpoint plus continuous tail context=%+v provenance=%+v", providerContext, contextProvenance)
	}
	contextProvenance.ProviderInputHash = sha256Ref(sha256Bytes([]byte("exact provider input")))
	trustedProvenance := ProposalProvenance{
		Origin: ProposalOriginAI, AI: &AIProvenance{Provider: "test", Model: "test"},
		providerInputHash: contextProvenance.ProviderInputHash,
	}
	if err := database.Transaction(func(transaction *gorm.DB) error {
		_, checkpointID, _, validateErr := validateAndEncodeProposalConversationContext(
			transaction, projectID, conversationID, providerTrigger, &contextProvenance,
			trustedProvenance,
		)
		if validateErr != nil {
			return validateErr
		}
		if checkpointID == nil || checkpointID.String() != second.ID {
			return errors.New("validated AI provenance lost its approved checkpoint identity")
		}
		return nil
	}); err != nil {
		t.Fatalf("valid checkpoint-tail proposal provenance was rejected: %v", err)
	}
	tampered := contextProvenance
	tampered.Tail.Hash = sha256Ref(sha256Bytes([]byte("tampered tail")))
	if err := database.Transaction(func(transaction *gorm.DB) error {
		_, _, _, validateErr := validateAndEncodeProposalConversationContext(
			transaction, projectID, conversationID, providerTrigger, &tampered,
			trustedProvenance,
		)
		return validateErr
	}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("tampered checkpoint-tail provenance was not rejected: %v", err)
	}
	tampered = contextProvenance
	tampered.ContextHash = sha256Ref(sha256Bytes([]byte("tampered context")))
	if err := database.Transaction(func(transaction *gorm.DB) error {
		_, _, _, validateErr := validateAndEncodeProposalConversationContext(
			transaction, projectID, conversationID, providerTrigger, &tampered,
			trustedProvenance,
		)
		return validateErr
	}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("tampered checkpoint-tail context hash was not rejected: %v", err)
	}
	tampered = contextProvenance
	tampered.ProviderInputHash = sha256Ref(sha256Bytes([]byte("tampered provider input")))
	if err := database.Transaction(func(transaction *gorm.DB) error {
		_, _, _, validateErr := validateAndEncodeProposalConversationContext(
			transaction, projectID, conversationID, providerTrigger, &tampered,
			trustedProvenance,
		)
		return validateErr
	}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("tampered provider input hash was not rejected: %v", err)
	}

	siblingMessages := appendMessages("sixth requirement", "seventh requirement")
	siblingOne, err := store.CreateSummaryCheckpoint(ctx, projectID, conversationID, creatorID,
		afterSecond.ETag, CreateSummaryCheckpointInput{
			ThroughMessageID: siblingMessages[0].ID, Summary: "Candidate branch ending at requirement six.",
		})
	if err != nil {
		t.Fatal(err)
	}
	siblingTwo, err := store.CreateSummaryCheckpoint(ctx, projectID, conversationID, creatorID,
		afterSecond.ETag, CreateSummaryCheckpointInput{
			ThroughMessageID: siblingMessages[1].ID, Summary: "Candidate branch ending at requirement seven.",
		})
	if err != nil {
		t.Fatal(err)
	}
	if siblingOne.PreviousCheckpointID == nil || siblingTwo.PreviousCheckpointID == nil ||
		*siblingOne.PreviousCheckpointID != second.ID || *siblingTwo.PreviousCheckpointID != second.ID {
		t.Fatalf("sibling fixtures do not share the approved parent: one=%+v two=%+v", siblingOne, siblingTwo)
	}

	type approvalResult struct {
		checkpoint ConversationSummaryCheckpoint
		err        error
	}
	start := make(chan struct{})
	results := make(chan approvalResult, 2)
	var wait sync.WaitGroup
	for _, sibling := range []ConversationSummaryCheckpoint{siblingOne, siblingTwo} {
		sibling := sibling
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			approved, approveErr := store.DecideSummaryCheckpoint(ctx, projectID, conversationID,
				uuid.MustParse(sibling.ID), reviewerID, sibling.ETag,
				DecideSummaryCheckpointInput{Decision: SummaryCheckpointApprove})
			results <- approvalResult{checkpoint: approved, err: approveErr}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	successes := 0
	winnerID := ""
	for result := range results {
		if result.err == nil {
			successes++
			winnerID = result.checkpoint.ID
			continue
		}
		if !errors.Is(result.err, core.ErrConflict) && !errors.Is(result.err, ErrSummaryCheckpointChainStale) {
			t.Fatalf("unexpected concurrent sibling approval error: %v", result.err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent sibling approvals succeeded %d times, want exactly one", successes)
	}
	oneAfter, err := store.GetSummaryCheckpoint(ctx, projectID, conversationID, uuid.MustParse(siblingOne.ID))
	if err != nil {
		t.Fatal(err)
	}
	twoAfter, err := store.GetSummaryCheckpoint(ctx, projectID, conversationID, uuid.MustParse(siblingTwo.ID))
	if err != nil {
		t.Fatal(err)
	}
	approved, superseded := 0, 0
	for _, checkpoint := range []ConversationSummaryCheckpoint{oneAfter, twoAfter} {
		switch checkpoint.Status {
		case SummaryCheckpointApproved:
			approved++
			if checkpoint.ID != winnerID {
				t.Fatalf("returned approval winner %s but persisted winner is %s", winnerID, checkpoint.ID)
			}
		case SummaryCheckpointSuperseded:
			superseded++
		default:
			t.Fatalf("sibling remained in unexpected status: %+v", checkpoint)
		}
	}
	if approved != 1 || superseded != 1 {
		t.Fatalf("sibling terminal states approved=%d superseded=%d", approved, superseded)
	}
	afterSibling, err := store.GetConversation(ctx, projectID, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if afterSibling.SummaryCheckpointHeadID == nil || *afterSibling.SummaryCheckpointHeadID != winnerID ||
		afterSibling.Version != 4 {
		t.Fatalf("concurrent approval head=%+v winner=%s", afterSibling, winnerID)
	}
}
