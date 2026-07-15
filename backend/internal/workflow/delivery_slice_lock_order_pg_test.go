package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type deliverySliceMutationLockContextKey struct{}

func TestWorkflowDeliverySliceMutationLocksActualTargetsInUUIDOrderPostgres(t *testing.T) {
	database, cleanup := multiBundleCompletionPostgresDatabase(t)
	defer cleanup()

	now := time.Now().UTC()
	userID := uuid.New()
	projectID := uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: userID, Email: "slice-lock-" + uuid.NewString() + "@example.com",
		DisplayName: "Slice lock owner", PasswordHash: "unused", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Delivery slice lock order", Lifecycle: "active", Version: 1,
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	type revisionTarget struct {
		artifactID uuid.UUID
		revisionID uuid.UUID
	}
	newRevisionTarget := func(label string) revisionTarget {
		t.Helper()
		artifactID := uuid.New()
		revisionID := uuid.New()
		if err := database.Create(&storage.ArtifactModel{
			ID: artifactID, ProjectID: projectID, Kind: "reference_source",
			ArtifactKey: "SLICE-" + strings.ToUpper(label) + "-" + artifactID.String()[:8],
			Title:       label, Lifecycle: "active", Version: 1,
			CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Create(&storage.ArtifactRevisionModel{
			ID: revisionID, ArtifactID: artifactID, RevisionNumber: 1, SchemaVersion: 1,
			ContentStore: "mongo", ContentRef: "slice-lock-" + label,
			ContentHash: "slice-lock-hash-" + label, ByteSize: 1,
			WorkflowStatus: "approved", ChangeSource: "human", ChangeSummary: label,
			CreatedBy: userID, CreatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		return revisionTarget{artifactID: artifactID, revisionID: revisionID}
	}
	lowTarget := newRevisionTarget("low")
	highTarget := newRevisionTarget("high")
	lowSliceID := uuid.MustParse("00000000-0000-0000-0000-000000000201")
	highSliceID := uuid.MustParse("00000000-0000-0000-0000-000000000202")
	storedSlices := []storage.DeliverySliceModel{
		{
			ID: lowSliceID, ProjectID: projectID, SliceKey: "slice-low", Title: "Low",
			BlueprintRevisionID: lowTarget.revisionID, SyncStatus: "current",
			WorkflowStatus: "pending", UpdatedAt: now,
		},
		{
			ID: highSliceID, ProjectID: projectID, SliceKey: "slice-high", Title: "High",
			BlueprintRevisionID: highTarget.revisionID, SyncStatus: "current",
			WorkflowStatus: "pending", UpdatedAt: now,
		},
	}
	if err := database.Create(&storedSlices).Error; err != nil {
		t.Fatal(err)
	}

	// Deliberately reverse caller order and use different candidate IDs. The
	// lock helper must resolve the existing UPSERT targets and lock their actual
	// UUIDs, rather than trusting either caller order or candidate primary keys.
	mutationRows := []sliceRow{
		{
			ID: uuid.New(), ProjectID: projectID, SliceKey: "slice-high",
			BlueprintRevisionID: highTarget.revisionID,
		},
		{
			ID: uuid.New(), ProjectID: projectID, SliceKey: "slice-low",
			BlueprintRevisionID: lowTarget.revisionID,
		},
	}
	marker := &struct{}{}
	var lockSQL string
	callbackName := "test:capture_delivery_slice_mutation_lock_" + uuid.NewString()
	if err := database.Callback().Query().After("gorm:query").Register(callbackName, func(query *gorm.DB) {
		if query.Statement.Context.Value(deliverySliceMutationLockContextKey{}) != marker || query.Error != nil {
			return
		}
		if query.Statement.Table != (sliceRow{}).TableName() {
			return
		}
		lockSQL = query.Statement.SQL.String()
	}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Callback().Query().Remove(callbackName) }()

	locked := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	lockContext := context.WithValue(context.Background(), deliverySliceMutationLockContextKey{}, marker)
	go func() {
		result <- database.WithContext(lockContext).Transaction(func(transaction *gorm.DB) error {
			if err := storage.LockDeliverySliceProjects(transaction, projectID); err != nil {
				return err
			}
			if err := lockDeliverySliceMutationTargets(transaction, mutationRows); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("workflow mutation did not acquire its DeliverySlice target locks")
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
		t.Fatal("workflow mutation did not hold its project DeliverySlice range lock")
	}

	normalizedSQL := strings.ToLower(lockSQL)
	if !strings.Contains(normalizedSQL, "order by id asc") || !strings.Contains(normalizedSQL, "for update") {
		t.Fatalf("DeliverySlice target lock is not ordered by actual row UUID: %s", lockSQL)
	}
	for _, sliceID := range []uuid.UUID{lowSliceID, highSliceID} {
		err := database.Transaction(func(transaction *gorm.DB) error {
			if err := transaction.Exec(`SET LOCAL lock_timeout = '250ms'`).Error; err != nil {
				return err
			}
			return transaction.Model(&storage.DeliverySliceModel{}).
				Where("id = ?", sliceID).Update("title", gorm.Expr("title")).Error
		})
		if err == nil {
			t.Fatalf("DeliverySlice %s remained writable during workflow target lock", sliceID)
		}
		message := strings.ToLower(err.Error())
		if !strings.Contains(message, "55p03") && !strings.Contains(message, "lock timeout") {
			t.Fatalf("DeliverySlice %s lock probe failed unexpectedly: %v", sliceID, err)
		}
	}

	close(release)
	released = true
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("workflow DeliverySlice target lock transaction failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workflow DeliverySlice target lock transaction did not finish")
	}
}
