package content

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var coreReferenceTableDefinitions = []string{
	"CREATE TABLE artifact_drafts (content_ref text)",
	"CREATE TABLE artifact_revisions (content_ref text)",
	"CREATE TABLE input_manifests (content_ref text)",
	"CREATE TABLE output_proposals (content_ref text)",
	"CREATE TABLE application_build_manifests (content_ref text)",
	"CREATE TABLE application_build_contracts (content_ref text)",
	"CREATE TABLE implementation_proposals (content_ref text)",
}

var repositoryReferenceTableDefinitions = []string{
	"CREATE TABLE repository_snapshots (tree_ref text)",
	"CREATE TABLE candidate_workspaces (base_tree_ref text, current_tree_ref text)",
	"CREATE TABLE candidate_workspace_journal (before_tree_ref text, after_tree_ref text)",
	"CREATE TABLE candidate_snapshots (tree_ref text)",
	"CREATE TABLE repository_file_blobs (content_ref text)",
}

var verificationReferenceTableDefinitions = []string{
	"CREATE TABLE candidate_verification_plans (content_ref text)",
	"CREATE TABLE candidate_verification_receipts (content_ref text)",
	"CREATE TABLE canonical_verification_plans (content_ref text)",
	"CREATE TABLE canonical_verification_receipts (content_ref text)",
	"CREATE TABLE release_bundles (content_ref text)",
	"CREATE TABLE release_preview_receipts (content_ref text)",
	"CREATE TABLE release_promotion_approvals (content_ref text)",
	"CREATE TABLE release_production_receipts (content_ref text)",
	"CREATE TABLE release_deployment_revisions (content_ref text)",
}

type fakeReconcileStore struct {
	pending   []pendingContent
	finalized []string
	aborted   []string
}

func (s *fakeReconcileStore) listPending(
	ctx context.Context,
	createdBefore time.Time,
	limit int64,
) ([]pendingContent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([]pendingContent, 0, len(s.pending))
	for _, item := range s.pending {
		if item.CreatedAt.After(createdBefore) {
			continue
		}
		result = append(result, item)
		if int64(len(result)) == limit {
			break
		}
	}
	return result, nil
}

func (s *fakeReconcileStore) Finalize(_ context.Context, contentID string) error {
	s.finalized = append(s.finalized, contentID)
	return nil
}

func (s *fakeReconcileStore) Abort(_ context.Context, contentID string) error {
	s.aborted = append(s.aborted, contentID)
	return nil
}

func TestRepositoryReferenceQueryCoversEveryTreeReference(t *testing.T) {
	t.Parallel()

	for _, fragment := range []string{
		"repository_snapshots WHERE tree_ref",
		"candidate_workspaces WHERE base_tree_ref",
		"candidate_workspaces WHERE current_tree_ref",
		"candidate_workspace_journal WHERE before_tree_ref",
		"candidate_workspace_journal WHERE after_tree_ref",
		"candidate_snapshots WHERE tree_ref",
	} {
		if !strings.Contains(repositoryReferenceQuery, fragment) {
			t.Errorf("repository reference query does not contain %q", fragment)
		}
	}
	if !strings.Contains(repositoryFileReferenceQuery, "repository_file_blobs WHERE content_ref") {
		t.Error("repository file reference query does not cover repository_file_blobs.content_ref")
	}
	for _, key := range []string{"patch", "structuredResult", "stdout", "stderr", "validation"} {
		if !strings.Contains(agentEvidenceReferenceQuery, "'"+key+"'") {
			t.Errorf("Agent evidence reference query does not cover %q", key)
		}
	}
}

func TestReconcilerPostgresAgentEvidenceReachabilityCanary(t *testing.T) {
	definitions := append(append([]string{}, coreReferenceTableDefinitions...), repositoryReferenceTableDefinitions...)
	definitions = append(definitions, "CREATE TABLE agent_attempts (evidence jsonb NOT NULL)")
	database := openReconcilerPostgresSchema(t, definitions)

	references := []string{"patch", "structuredResult", "stdout", "stderr", "validation"}
	for _, key := range references {
		contentID := "agent-evidence-" + key
		if err := database.Exec(
			"INSERT INTO agent_attempts (evidence) VALUES (jsonb_build_object(CAST(? AS text), jsonb_build_object('ref', CAST(? AS text))))",
			key,
			contentID,
		).Error; err != nil {
			t.Fatalf("insert Agent %s evidence reference: %v", key, err)
		}
	}
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	store := &fakeReconcileStore{}
	for _, key := range references {
		store.pending = append(store.pending, pendingContent{
			ID: "agent-evidence-" + key, CreatedAt: now.Add(-2 * time.Hour),
		})
	}
	store.pending = append(store.pending, pendingContent{ID: "agent-evidence-orphan", CreatedAt: now.Add(-2 * time.Hour)})
	reconciler, err := newReconciler(database, store, ReconcileConfig{
		GracePeriod: time.Minute, OrphanTTL: time.Hour, BatchSize: 100,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats != (ReconcileStats{Examined: 6, Finalized: 5, Aborted: 1}) {
		t.Fatalf("Agent evidence reconcile stats = %+v", stats)
	}
}

func TestReconcilerPostgresRepositoryTreeReachabilityCanary(t *testing.T) {
	database := openReconcilerPostgresSchema(t, append(
		append([]string{}, coreReferenceTableDefinitions...),
		repositoryReferenceTableDefinitions...,
	))

	references := []struct {
		table  string
		column string
		id     string
	}{
		{table: "repository_snapshots", column: "tree_ref", id: "repository-snapshot-tree"},
		{table: "candidate_workspaces", column: "base_tree_ref", id: "candidate-base-tree"},
		{table: "candidate_workspaces", column: "current_tree_ref", id: "candidate-current-tree"},
		{table: "candidate_workspace_journal", column: "before_tree_ref", id: "journal-before-tree"},
		{table: "candidate_workspace_journal", column: "after_tree_ref", id: "journal-after-tree"},
		{table: "candidate_snapshots", column: "tree_ref", id: "candidate-snapshot-tree"},
		{table: "repository_file_blobs", column: "content_ref", id: "repository-file-content"},
	}
	for _, reference := range references {
		query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (?)", reference.table, reference.column)
		if err := database.Exec(query, reference.id).Error; err != nil {
			t.Fatalf("insert committed %s.%s reference: %v", reference.table, reference.column, err)
		}
	}

	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	store := &fakeReconcileStore{}
	for _, reference := range references {
		store.pending = append(store.pending, pendingContent{
			ID: reference.id, CreatedAt: now.Add(-2 * time.Hour),
		})
	}
	const orphanID = "unreferenced-tree"
	store.pending = append(store.pending, pendingContent{ID: orphanID, CreatedAt: now.Add(-2 * time.Hour)})
	reconciler, err := newReconciler(database, store, ReconcileConfig{
		GracePeriod: time.Minute,
		OrphanTTL:   time.Hour,
		BatchSize:   100,
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	stats, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("reconcile committed repository tree references: %v", err)
	}
	if stats != (ReconcileStats{Examined: 8, Finalized: 7, Aborted: 1}) {
		t.Fatalf("reconcile stats = %+v, want 8 examined, 7 finalized, 1 aborted", stats)
	}
	for _, reference := range references {
		if !containsString(store.finalized, reference.id) {
			t.Errorf("committed reference %q was not finalized", reference.id)
		}
		if containsString(store.aborted, reference.id) {
			t.Errorf("committed reference %q was aborted", reference.id)
		}
	}
	if !containsString(store.aborted, orphanID) {
		t.Errorf("orphan %q was not aborted", orphanID)
	}
}

func TestReconcilerPostgresVerificationAndReleaseReachabilityCanary(t *testing.T) {
	definitions := append(append([]string{}, coreReferenceTableDefinitions...), verificationReferenceTableDefinitions...)
	database := openReconcilerPostgresSchema(t, definitions)
	references := []struct {
		table string
		id    string
	}{
		{"candidate_verification_plans", "candidate-plan-content"},
		{"candidate_verification_receipts", "candidate-receipt-content"},
		{"canonical_verification_plans", "canonical-plan-content"},
		{"canonical_verification_receipts", "canonical-receipt-content"},
		{"release_bundles", "release-bundle-content"},
		{"release_preview_receipts", "preview-receipt-content"},
		{"release_promotion_approvals", "promotion-approval-content"},
		{"release_production_receipts", "production-receipt-content"},
		{"release_deployment_revisions", "deployment-revision-content"},
	}
	for _, reference := range references {
		if err := database.Exec("INSERT INTO "+reference.table+" (content_ref) VALUES (?)", reference.id).Error; err != nil {
			t.Fatalf("insert %s content reference: %v", reference.table, err)
		}
	}
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	store := &fakeReconcileStore{}
	for _, reference := range references {
		store.pending = append(store.pending, pendingContent{ID: reference.id, CreatedAt: now.Add(-2 * time.Hour)})
	}
	store.pending = append(store.pending, pendingContent{ID: "verification-orphan", CreatedAt: now.Add(-2 * time.Hour)})
	reconciler, err := newReconciler(database, store, ReconcileConfig{
		GracePeriod: time.Minute, OrphanTTL: time.Hour, BatchSize: 100,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats != (ReconcileStats{Examined: 10, Finalized: 9, Aborted: 1}) {
		t.Fatalf("verification/release reconcile stats = %+v", stats)
	}
}

func TestReconcilerPostgresReleaseDeliverySchemaFailsClosed(t *testing.T) {
	definitions := append(append([]string{}, coreReferenceTableDefinitions...), verificationReferenceTableDefinitions[:5]...)
	definitions = append(definitions, "CREATE TABLE release_preview_receipts (content_ref text)")
	database := openReconcilerPostgresSchema(t, definitions)
	store, reconciler := oldPendingReconciler(t, database, "partial-release-delivery-content")

	if _, err := reconciler.RunOnce(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "release delivery reachability tables are partially migrated") {
		t.Fatalf("partial release delivery schema error = %v", err)
	}
	if len(store.aborted) != 0 || len(store.finalized) != 0 {
		t.Fatalf("partial release delivery schema changed content: finalized=%v aborted=%v", store.finalized, store.aborted)
	}
}

func TestReconcilerPostgresRepositorySchemaCompatibility(t *testing.T) {
	t.Run("pre-repository schema", func(t *testing.T) {
		database := openReconcilerPostgresSchema(t, coreReferenceTableDefinitions)
		now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
		store := &fakeReconcileStore{pending: []pendingContent{{
			ID: "old-schema-orphan", CreatedAt: now.Add(-2 * time.Hour),
		}}}
		reconciler, err := newReconciler(database, store, ReconcileConfig{
			GracePeriod: time.Minute,
			OrphanTTL:   time.Hour,
			Now:         func() time.Time { return now },
		})
		if err != nil {
			t.Fatal(err)
		}

		stats, err := reconciler.RunOnce(context.Background())
		if err != nil {
			t.Fatalf("pre-repository schema should remain compatible: %v", err)
		}
		if stats.Aborted != 1 || !containsString(store.aborted, "old-schema-orphan") {
			t.Fatalf("old-schema orphan was not cleaned: stats=%+v aborted=%v", stats, store.aborted)
		}
	})

	t.Run("partially migrated schema fails closed", func(t *testing.T) {
		definitions := append([]string{}, coreReferenceTableDefinitions...)
		definitions = append(definitions, "CREATE TABLE repository_snapshots (tree_ref text)")
		database := openReconcilerPostgresSchema(t, definitions)
		store, reconciler := oldPendingReconciler(t, database, "partial-schema-content")

		if _, err := reconciler.RunOnce(context.Background()); err == nil ||
			!strings.Contains(err.Error(), "partially migrated") {
			t.Fatalf("partial schema error = %v, want partially migrated failure", err)
		}
		if len(store.aborted) != 0 || len(store.finalized) != 0 {
			t.Fatalf("partial schema changed pending content: finalized=%v aborted=%v", store.finalized, store.aborted)
		}
	})

	t.Run("query fault fails closed", func(t *testing.T) {
		definitions := append([]string{}, coreReferenceTableDefinitions...)
		definitions = append(definitions,
			"CREATE TABLE repository_snapshots (tree_ref text)",
			"CREATE TABLE candidate_workspaces (base_tree_ref text, current_tree_ref text)",
			"CREATE TABLE candidate_workspace_journal (before_tree_ref text, after_tree_ref text)",
			"CREATE TABLE candidate_snapshots (unexpected_ref text)",
		)
		database := openReconcilerPostgresSchema(t, definitions)
		store, reconciler := oldPendingReconciler(t, database, "query-fault-content")

		if _, err := reconciler.RunOnce(context.Background()); err == nil ||
			!strings.Contains(err.Error(), "check repository content references") {
			t.Fatalf("query fault error = %v, want repository reference failure", err)
		}
		if len(store.aborted) != 0 || len(store.finalized) != 0 {
			t.Fatalf("query fault changed pending content: finalized=%v aborted=%v", store.finalized, store.aborted)
		}
	})

	t.Run("file catalog query fault fails closed", func(t *testing.T) {
		definitions := append([]string{}, coreReferenceTableDefinitions...)
		definitions = append(definitions,
			"CREATE TABLE repository_snapshots (tree_ref text)",
			"CREATE TABLE candidate_workspaces (base_tree_ref text, current_tree_ref text)",
			"CREATE TABLE candidate_workspace_journal (before_tree_ref text, after_tree_ref text)",
			"CREATE TABLE candidate_snapshots (tree_ref text)",
			"CREATE TABLE repository_file_blobs (unexpected_ref text)",
		)
		database := openReconcilerPostgresSchema(t, definitions)
		store, reconciler := oldPendingReconciler(t, database, "file-query-fault-content")

		if _, err := reconciler.RunOnce(context.Background()); err == nil ||
			!strings.Contains(err.Error(), "check repository file content references") {
			t.Fatalf("file query fault error = %v, want repository file reference failure", err)
		}
		if len(store.aborted) != 0 || len(store.finalized) != 0 {
			t.Fatalf("file query fault changed pending content: finalized=%v aborted=%v", store.finalized, store.aborted)
		}
	})
}

func oldPendingReconciler(
	t *testing.T,
	database *gorm.DB,
	contentID string,
) (*fakeReconcileStore, *Reconciler) {
	t.Helper()
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	store := &fakeReconcileStore{pending: []pendingContent{{
		ID: contentID, CreatedAt: now.Add(-2 * time.Hour),
	}}}
	reconciler, err := newReconciler(database, store, ReconcileConfig{
		GracePeriod: time.Minute,
		OrphanTTL:   time.Hour,
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, reconciler
}

func openReconcilerPostgresSchema(t *testing.T, definitions []string) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	// Keep SET search_path and every committed canary statement on one isolated
	// connection. Each INSERT below commits before the reconciler observes it.
	sqlDatabase.SetMaxOpenConns(1)
	sqlDatabase.SetMaxIdleConns(1)
	schema := "content_reconciler_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := database.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		sqlDatabase.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Exec("SET search_path TO public")
		database.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
		sqlDatabase.Close()
	})
	if err := database.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if err := database.Exec(definition).Error; err != nil {
			t.Fatalf("create reconciler canary table: %v", err)
		}
	}
	return database
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
