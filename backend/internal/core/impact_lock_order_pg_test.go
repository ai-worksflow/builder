package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type impactHealthWriteContextKey struct{}
type overlappingImpactFirstContextKey struct{}
type overlappingImpactSecondContextKey struct{}

func TestOrderedImpactArtifactIDsMatchesApprovalLockOrder(t *testing.T) {
	t.Parallel()
	lowest := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	middle := uuid.MustParse("00000000-0000-0000-0000-000000000010")
	highest := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")

	ordered := orderedImpactArtifactIDs(map[uuid.UUID]string{
		highest: "blocked",
		lowest:  "needs_sync",
		middle:  "blocked",
	})
	if len(ordered) != 3 || ordered[0] != lowest || ordered[1] != middle || ordered[2] != highest {
		t.Fatalf("impact health order = %v, want stable UUID order", ordered)
	}
}

func TestImpactHealthWritesDoNotDeadlockApprovalClosurePostgres(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, _, projectID, ownerID := newArtifactLineageFixture(t, database)
	first := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "reference_source", "approved", "current",
		json.RawMessage(`{"title":"first affected artifact"}`),
	)
	second := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "reference_source", "approved", "current",
		json.RawMessage(`{"title":"second affected artifact"}`),
	)
	ordered := []VersionRef{first, second}
	if ordered[1].ArtifactID < ordered[0].ArtifactID {
		ordered[0], ordered[1] = ordered[1], ordered[0]
	}
	low, high := ordered[0], ordered[1]
	seedArtifactLineageRevisionSource(t, database, low, high, "upstream", true, nil, ownerID)

	from := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "reference_source", "approved", "current",
		json.RawMessage(`{"blocks":[{"id":"source","value":"before"}]}`),
	)
	fromRevisionID := uuid.MustParse(from.RevisionID)
	toRevisionID := uuid.New()
	toPayload := json.RawMessage(`{"blocks":[{"id":"source","value":"after"}]}`)
	toContent := store.addFinalized("impact-lock-order-to-"+toRevisionID.String(), toPayload)
	now := time.Now().UTC()
	if err := database.Create(&storage.ArtifactRevisionModel{
		ID: toRevisionID, ArtifactID: uuid.MustParse(from.ArtifactID), RevisionNumber: 2,
		ParentRevisionID: &fromRevisionID, SchemaVersion: 1, ContentStore: "mongo",
		ContentRef: toContent.ID, ContentHash: toContent.ContentHash, ByteSize: toContent.ByteSize,
		WorkflowStatus: "draft", ChangeSource: "human", ChangeSummary: "Impact lock order",
		CreatedBy: ownerID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ArtifactModel{}).
		Where("id = ?", uuid.MustParse(from.ArtifactID)).
		Updates(map[string]any{"latest_revision_id": toRevisionID, "updated_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	for _, target := range []VersionRef{low, high} {
		targetRevisionID := uuid.MustParse(target.RevisionID)
		if err := database.Create(&storage.TraceLinkModel{
			ID: uuid.New(), ProjectID: projectID,
			SourceArtifactID: uuid.MustParse(from.ArtifactID), SourceRevisionID: fromRevisionID,
			TargetArtifactID: uuid.MustParse(target.ArtifactID), TargetRevisionID: &targetRevisionID,
			Relation: "reads", Metadata: json.RawMessage(`{}`), CreatedBy: ownerID, CreatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	impacts, err := NewImpactService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}

	marker := &struct{}{}
	firstHealthWritten := make(chan struct{})
	releaseImpact := make(chan struct{})
	var pauseOnce sync.Once
	callbackName := "test:pause_after_first_ordered_impact_health_" + uuid.NewString()
	if err := database.Callback().Create().After("gorm:create").Register(callbackName, func(query *gorm.DB) {
		if query.Statement.Context.Value(impactHealthWriteContextKey{}) != marker || query.Error != nil {
			return
		}
		table := query.Statement.Table
		if table == "" && query.Statement.Schema != nil {
			table = query.Statement.Schema.Table
		}
		if table != (storage.ArtifactHealthModel{}).TableName() {
			return
		}
		pauseOnce.Do(func() {
			close(firstHealthWritten)
			<-releaseImpact
		})
	}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Callback().Create().Remove(callbackName) }()
	released := false
	defer func() {
		if !released {
			close(releaseImpact)
		}
	}()

	impactResult := make(chan error, 1)
	impactContext := context.WithValue(ctx, impactHealthWriteContextKey{}, marker)
	go func() {
		_, analyzeErr := impacts.Analyze(impactContext, projectID.String(), ownerID.String(), AnalyzeImpactInput{
			From: from,
			To: VersionRef{
				ArtifactID: from.ArtifactID, RevisionID: toRevisionID.String(), ContentHash: toContent.ContentHash,
			},
		})
		impactResult <- analyzeErr
	}()

	select {
	case <-firstHealthWritten:
	case <-time.After(5 * time.Second):
		t.Fatal("Impact Analyze did not pause after its first ordered health write")
	}

	closurePID := make(chan int, 1)
	closureResult := make(chan error, 1)
	go func() {
		closureResult <- database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
			if err := transaction.Exec(`SET LOCAL statement_timeout = '5s'`).Error; err != nil {
				return err
			}
			var backendPID int
			if err := transaction.Raw(`SELECT pg_backend_pid()`).Scan(&backendPID).Error; err != nil {
				return err
			}
			closurePID <- backendPID
			_, err := lockArtifactApprovalSourceClosure(
				ctx, transaction, projectID, uuid.MustParse(low.ArtifactID), uuid.MustParse(low.RevisionID),
			)
			return err
		})
	}()

	var backendPID int
	select {
	case backendPID = <-closurePID:
	case <-time.After(5 * time.Second):
		t.Fatal("approval closure transaction did not start")
	}
	waitForPostgresLockWait(t, database, backendPID)
	close(releaseImpact)
	released = true

	for label, result := range map[string]<-chan error{
		"Impact Analyze":   impactResult,
		"approval closure": closureResult,
	} {
		select {
		case outcome := <-result:
			if outcome != nil {
				if strings.Contains(strings.ToLower(outcome.Error()), "40p01") ||
					strings.Contains(strings.ToLower(outcome.Error()), "deadlock detected") {
					t.Fatalf("%s hit a PostgreSQL deadlock despite the shared UUID lock order: %v", label, outcome)
				}
				t.Fatalf("%s failed: %v", label, outcome)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not complete after releasing the intersecting health lock", label)
		}
	}
}

func TestOverlappingImpactWritesLockHealthThenSharedDeliverySlicesPostgres(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, _, projectID, ownerID := newArtifactLineageFixture(t, database)
	firstTarget := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "reference_source", "approved", "current",
		json.RawMessage(`{"title":"first overlapping target"}`),
	)
	secondTarget := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "reference_source", "approved", "current",
		json.RawMessage(`{"title":"second overlapping target"}`),
	)
	targets := []VersionRef{firstTarget, secondTarget}
	if targets[1].ArtifactID < targets[0].ArtifactID {
		targets[0], targets[1] = targets[1], targets[0]
	}
	low, high := targets[0], targets[1]

	firstFrom, firstTo := seedImpactChangeRevisionPair(
		t, database, store, projectID, ownerID, "first-overlap",
	)
	seedImpactTraceTargets(t, database, projectID, ownerID, firstFrom, low, high)
	if err := database.Model(&storage.TraceLinkModel{}).
		Where("source_revision_id = ? AND target_artifact_id = ?", uuid.MustParse(firstFrom.RevisionID), uuid.MustParse(low.ArtifactID)).
		Update("relation", "requires").Error; err != nil {
		t.Fatal(err)
	}
	secondFrom, secondTo := seedImpactChangeRevisionPair(
		t, database, store, projectID, ownerID, "second-overlap",
	)
	seedImpactTraceTargets(t, database, projectID, ownerID, secondFrom, high)

	now := time.Now().UTC()
	lowRevisionID := uuid.MustParse(low.RevisionID)
	highRevisionID := uuid.MustParse(high.RevisionID)
	deliverySlices := []storage.DeliverySliceModel{
		{
			ID: uuid.MustParse("00000000-0000-0000-0000-000000000101"), ProjectID: projectID,
			SliceKey: "overlap-first", Title: "Overlap first",
			BlueprintRevisionID: lowRevisionID, PageSpecRevisionID: &highRevisionID,
			SyncStatus: "current", WorkflowStatus: "pending", UpdatedAt: now,
		},
		{
			ID: uuid.MustParse("00000000-0000-0000-0000-000000000102"), ProjectID: projectID,
			SliceKey: "overlap-second", Title: "Overlap second",
			BlueprintRevisionID: highRevisionID, PageSpecRevisionID: &lowRevisionID,
			SyncStatus: "current", WorkflowStatus: "pending", UpdatedAt: now,
		},
	}
	if err := database.Create(&deliverySlices).Error; err != nil {
		t.Fatal(err)
	}

	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	impacts, err := NewImpactService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}

	firstMarker := &struct{}{}
	secondMarker := &struct{}{}
	firstHealthWritten := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondAttemptedDeliveryLock := make(chan struct{})
	releaseSecond := make(chan struct{})
	var pauseFirstOnce sync.Once
	var pauseSecondOnce sync.Once

	firstAfterName := "test:pause_first_overlap_health_" + uuid.NewString()
	if err := database.Callback().Create().After("gorm:create").Register(firstAfterName, func(query *gorm.DB) {
		if query.Statement.Context.Value(overlappingImpactFirstContextKey{}) != firstMarker ||
			impactCallbackTable(query) != (storage.ArtifactHealthModel{}).TableName() || query.Error != nil {
			return
		}
		pauseFirstOnce.Do(func() {
			close(firstHealthWritten)
			<-releaseFirst
		})
	}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Callback().Create().Remove(firstAfterName) }()

	secondDeliveryName := "test:pause_second_overlap_delivery_lock_" + uuid.NewString()
	if err := database.Callback().Query().Before("gorm:query").Register(secondDeliveryName, func(query *gorm.DB) {
		if query.Statement.Context.Value(overlappingImpactSecondContextKey{}) != secondMarker ||
			impactCallbackTable(query) != (storage.DeliverySliceModel{}).TableName() || query.Error != nil {
			return
		}
		if _, locked := query.Statement.Clauses["FOR"]; !locked {
			return
		}
		pauseSecondOnce.Do(func() {
			close(secondAttemptedDeliveryLock)
			<-releaseSecond
		})
	}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Callback().Query().Remove(secondDeliveryName) }()

	firstReleased := false
	secondReleased := false
	defer func() {
		if !firstReleased {
			close(releaseFirst)
		}
		if !secondReleased {
			close(releaseSecond)
		}
	}()
	runContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	firstResult := make(chan error, 1)
	go func() {
		_, analyzeErr := impacts.Analyze(
			context.WithValue(runContext, overlappingImpactFirstContextKey{}, firstMarker),
			projectID.String(), ownerID.String(), AnalyzeImpactInput{From: firstFrom, To: firstTo},
		)
		firstResult <- analyzeErr
	}()
	select {
	case <-firstHealthWritten:
	case <-runContext.Done():
		t.Fatalf("first Impact did not pause after its low health write: %v", runContext.Err())
	}
	var advisoryAvailable bool
	if err := database.Transaction(func(transaction *gorm.DB) error {
		return transaction.Raw(
			"SELECT pg_try_advisory_xact_lock(hashtextextended(?, 0))",
			"delivery_slices:"+projectID.String(),
		).Scan(&advisoryAvailable).Error
	}); err != nil {
		t.Fatal(err)
	}
	if advisoryAvailable {
		t.Fatal("another transaction acquired the project DeliverySlice range while Impact held it")
	}

	secondResult := make(chan error, 1)
	go func() {
		_, analyzeErr := impacts.Analyze(
			context.WithValue(runContext, overlappingImpactSecondContextKey{}, secondMarker),
			projectID.String(), ownerID.String(), AnalyzeImpactInput{From: secondFrom, To: secondTo},
		)
		secondResult <- analyzeErr
	}()
	select {
	case <-secondAttemptedDeliveryLock:
		t.Fatal("second Impact crossed the project advisory range while the first Impact was open")
	case <-time.After(150 * time.Millisecond):
	}

	close(releaseFirst)
	firstReleased = true
	select {
	case <-secondAttemptedDeliveryLock:
	case <-runContext.Done():
		t.Fatalf("second Impact did not advance after the project advisory range was released: %v", runContext.Err())
	}
	select {
	case outcome := <-firstResult:
		if outcome != nil {
			t.Fatalf("first Impact failed before releasing its project range: %v", outcome)
		}
	case <-runContext.Done():
		t.Fatalf("first Impact did not commit after its health pause was released: %v", runContext.Err())
	}
	var firstImpactSlices []storage.DeliverySliceModel
	if err := database.Where("id IN ?", []uuid.UUID{deliverySlices[0].ID, deliverySlices[1].ID}).
		Order("id ASC").Find(&firstImpactSlices).Error; err != nil {
		t.Fatal(err)
	}
	if len(firstImpactSlices) != 2 || firstImpactSlices[0].SyncStatus != "blocked" || firstImpactSlices[1].SyncStatus != "blocked" {
		t.Fatalf("shared DeliverySlices did not retain the first Impact's worst severity: %+v", firstImpactSlices)
	}

	close(releaseSecond)
	secondReleased = true
	select {
	case outcome := <-secondResult:
		if outcome != nil {
			message := strings.ToLower(outcome.Error())
			if strings.Contains(message, "40p01") || strings.Contains(message, "deadlock detected") {
				t.Fatalf("second Impact hit a PostgreSQL deadlock after advisory serialization: %v", outcome)
			}
			t.Fatalf("second Impact failed: %v", outcome)
		}
	case <-runContext.Done():
		t.Fatalf("second Impact did not finish after its DeliverySlice lock was released: %v", runContext.Err())
	}
}

func seedImpactChangeRevisionPair(
	t *testing.T,
	database *gorm.DB,
	store *baselineContentStoreSpy,
	projectID uuid.UUID,
	ownerID uuid.UUID,
	label string,
) (VersionRef, VersionRef) {
	t.Helper()
	from := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "reference_source", "approved", "current",
		json.RawMessage(`{"blocks":[{"id":"source","value":"before"}]}`),
	)
	fromRevisionID := uuid.MustParse(from.RevisionID)
	toRevisionID := uuid.New()
	toPayload := json.RawMessage(`{"blocks":[{"id":"source","value":"after"}]}`)
	toContent := store.addFinalized("impact-"+label+"-to-"+toRevisionID.String(), toPayload)
	now := time.Now().UTC()
	if err := database.Create(&storage.ArtifactRevisionModel{
		ID: toRevisionID, ArtifactID: uuid.MustParse(from.ArtifactID), RevisionNumber: 2,
		ParentRevisionID: &fromRevisionID, SchemaVersion: 1, ContentStore: "mongo",
		ContentRef: toContent.ID, ContentHash: toContent.ContentHash, ByteSize: toContent.ByteSize,
		WorkflowStatus: "draft", ChangeSource: "human", ChangeSummary: "Impact " + label,
		CreatedBy: ownerID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ArtifactModel{}).
		Where("id = ?", uuid.MustParse(from.ArtifactID)).
		Updates(map[string]any{"latest_revision_id": toRevisionID, "updated_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	return from, VersionRef{
		ArtifactID: from.ArtifactID, RevisionID: toRevisionID.String(), ContentHash: toContent.ContentHash,
	}
}

func seedImpactTraceTargets(
	t *testing.T,
	database *gorm.DB,
	projectID uuid.UUID,
	ownerID uuid.UUID,
	from VersionRef,
	targets ...VersionRef,
) {
	t.Helper()
	for _, target := range targets {
		targetRevisionID := uuid.MustParse(target.RevisionID)
		if err := database.Create(&storage.TraceLinkModel{
			ID: uuid.New(), ProjectID: projectID,
			SourceArtifactID: uuid.MustParse(from.ArtifactID), SourceRevisionID: uuid.MustParse(from.RevisionID),
			TargetArtifactID: uuid.MustParse(target.ArtifactID), TargetRevisionID: &targetRevisionID,
			Relation: "reads", Metadata: json.RawMessage(`{}`), CreatedBy: ownerID, CreatedAt: time.Now().UTC(),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func impactCallbackTable(query *gorm.DB) string {
	table := query.Statement.Table
	if table == "" && query.Statement.Schema != nil {
		table = query.Statement.Schema.Table
	}
	return table
}

func waitForPostgresLockWait(t *testing.T, database *gorm.DB, backendPID int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waitEventType string
		if err := database.Raw(
			`SELECT COALESCE(wait_event_type, '') FROM pg_stat_activity WHERE pid = ?`, backendPID,
		).Scan(&waitEventType).Error; err != nil {
			t.Fatal(err)
		}
		if strings.EqualFold(waitEventType, "Lock") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("approval closure did not wait on the health row held by Impact Analyze")
}
