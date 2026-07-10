package core

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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"github.com/worksflow/builder/backend/migrations"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type baselineContentStoreSpy struct {
	items         map[string]content.StoredContent
	putCalls      int
	finalizeCalls int
	abortCalls    int
}

func newBaselineContentStoreSpy() *baselineContentStoreSpy {
	return &baselineContentStoreSpy{items: map[string]content.StoredContent{}}
}

func (s *baselineContentStoreSpy) addFinalized(id string, payload json.RawMessage) content.Reference {
	reference := baselineContentReference(id, payload)
	now := time.Now().UTC()
	s.items[id] = content.StoredContent{
		Reference: reference, State: content.StateFinalized,
		Payload: append(json.RawMessage(nil), payload...), CreatedAt: now, FinalizedAt: &now,
	}
	return reference
}

func (s *baselineContentStoreSpy) PutPending(
	_ context.Context,
	projectID string,
	aggregateType string,
	aggregateID string,
	schemaVersion int,
	payload json.RawMessage,
) (content.Reference, error) {
	s.putCalls++
	id := fmt.Sprintf("baseline-pending-%d", s.putCalls)
	reference := baselineContentReference(id, payload)
	reference.SchemaVersion = schemaVersion
	s.items[id] = content.StoredContent{
		Reference: reference, ProjectID: projectID, AggregateType: aggregateType,
		AggregateID: aggregateID, State: content.StatePending,
		Payload: append(json.RawMessage(nil), payload...), CreatedAt: time.Now().UTC(),
	}
	return reference, nil
}

func (s *baselineContentStoreSpy) Finalize(_ context.Context, contentID string) error {
	item, exists := s.items[contentID]
	if !exists {
		return content.ErrContentNotFound
	}
	s.finalizeCalls++
	now := time.Now().UTC()
	item.State = content.StateFinalized
	item.FinalizedAt = &now
	s.items[contentID] = item
	return nil
}

func (s *baselineContentStoreSpy) Abort(_ context.Context, contentID string) error {
	s.abortCalls++
	item := s.items[contentID]
	item.State = content.StateAborted
	s.items[contentID] = item
	return nil
}

func (s *baselineContentStoreSpy) Get(_ context.Context, contentID, expectedHash string) (content.StoredContent, error) {
	item, exists := s.items[contentID]
	if !exists {
		return content.StoredContent{}, content.ErrContentNotFound
	}
	if item.ContentHash != expectedHash {
		return content.StoredContent{}, content.ErrHashMismatch
	}
	item.Payload = append(json.RawMessage(nil), item.Payload...)
	return item, nil
}

func baselineContentReference(id string, payload json.RawMessage) content.Reference {
	digest := sha256.Sum256(payload)
	return content.Reference{
		ID: id, ContentHash: "sha256:" + hex.EncodeToString(digest[:]),
		ByteSize: int64(len(payload)), SchemaVersion: 1,
	}
}

func TestBaselineCompilePersistsOnlyCanonicalFinalPayload(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	userID := uuid.New()
	projectID := uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: userID, Email: "baseline-" + uuid.NewString() + "@example.com",
		DisplayName: "Baseline Owner", PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Baseline integration", Lifecycle: "active", Version: 1,
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectMemberModel{
		ProjectID: projectID, UserID: userID, Role: "owner", JoinedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	store := newBaselineContentStoreSpy()
	briefRef := seedApprovedBaselineSource(t, database, store, projectID, userID, "project_brief", json.RawMessage(`{
		"summary":"Define the support application.",
		"blocks":[{"id":"goal-1","type":"goal","text":"Reduce response time."}]
	}`))
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewBaselineService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Compile(ctx, projectID.String(), userID.String(), []VersionRef{briefRef}); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("Project Brief without requirements must be blocked, got %v", err)
	}
	if store.putCalls != 0 || store.finalizeCalls != 0 || store.abortCalls != 0 {
		t.Fatalf("invalid final payload touched pending content: put=%d finalize=%d abort=%d", store.putCalls, store.finalizeCalls, store.abortCalls)
	}
	var invalidBaselineArtifacts int64
	if err := database.Model(&storage.ArtifactModel{}).
		Where("project_id = ? AND kind = 'requirement_baseline'", projectID).
		Count(&invalidBaselineArtifacts).Error; err != nil {
		t.Fatal(err)
	}
	if invalidBaselineArtifacts != 0 {
		t.Fatalf("invalid final payload persisted %d Requirement Baseline artifact(s)", invalidBaselineArtifacts)
	}

	requirementsRef := seedApprovedBaselineSource(t, database, store, projectID, userID, "product_requirements", json.RawMessage(`{
		"summary":"Define order exception handling.",
		"blocks":[{"id":"source-1","type":"paragraph","text":"Approved source context."}],
		"requirements":[{
			"id":"REQ-001","statement":"Agents must resolve order exceptions.","priority":"must",
			"acceptanceCriterionIds":["AC-001"],"sourceBlockIds":["source-1"]
		}],
		"acceptanceCriteria":[{"id":"AC-001","statement":"The exception is marked resolved."}]
	}`))
	revision, err := service.Compile(ctx, projectID.String(), userID.String(), []VersionRef{requirementsRef})
	if err != nil {
		t.Fatalf("valid Product Requirements must compile: %v", err)
	}
	if revision.WorkflowStatus != "approved" || store.putCalls != 1 || store.finalizeCalls != 1 || store.abortCalls != 0 {
		t.Fatalf("unexpected valid compile result: status=%q put=%d finalize=%d abort=%d", revision.WorkflowStatus, store.putCalls, store.finalizeCalls, store.abortCalls)
	}
	if report := ValidateArtifactContent("requirement_baseline", revision.Content); !report.Valid {
		t.Fatalf("persisted baseline failed its own gate: %#v", report.Findings)
	}
	if len(revision.SourceVersions) != 1 || revision.SourceVersions[0].RevisionID != requirementsRef.RevisionID ||
		revision.SourceVersions[0].ContentHash != requirementsRef.ContentHash ||
		revision.SourceVersions[0].Purpose != "baseline_input" || !revision.SourceVersions[0].Required {
		t.Fatalf("baseline response lost its exact frozen source: %#v", revision.SourceVersions)
	}
	assertPersistedBaselineLineage(t, database, revision, requirementsRef)
}

func TestPersistSystemRevisionLineageRejectsNonExactSource(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	now := time.Now().UTC()
	userID := uuid.New()
	projectID := uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: userID, Email: "lineage-" + uuid.NewString() + "@example.com",
		DisplayName: "Lineage Owner", PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Lineage integration", Lifecycle: "active", Version: 1,
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	store := newBaselineContentStoreSpy()
	valid := seedApprovedBaselineSource(
		t, database, store, projectID, userID, "product_requirements",
		json.RawMessage(`{"requirements":[{"id":"REQ-001","statement":"Exact source"}]}`),
	)

	wrongArtifact := valid
	wrongArtifact.ArtifactID = uuid.NewString()
	wrongRevision := valid
	wrongRevision.RevisionID = uuid.NewString()
	wrongHash := valid
	wrongHash.ContentHash = "sha256:" + strings.Repeat("f", 64)
	for _, test := range []struct {
		name      string
		projectID uuid.UUID
		ref       VersionRef
		want      error
	}{
		{name: "project", projectID: uuid.New(), ref: valid, want: ErrNotFound},
		{name: "artifact", projectID: projectID, ref: wrongArtifact, want: ErrNotFound},
		{name: "revision", projectID: projectID, ref: wrongRevision, want: ErrNotFound},
		{name: "content hash", projectID: projectID, ref: wrongHash, want: ErrConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := database.Transaction(func(transaction *gorm.DB) error {
				_, err := PersistSystemRevisionLineage(
					transaction, test.projectID, uuid.New(), uuid.New(), uuid.Nil, userID, now,
					[]SystemRevisionSource{{
						Ref: test.ref, Purpose: "exact_source", Required: true, Relation: "derives_from",
					}},
				)
				return err
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("expected %v for non-exact %s, got %v", test.want, test.name, err)
			}
		})
	}
	var frozenCount int64
	if err := database.Model(&storage.ArtifactRevisionSourceModel{}).Count(&frozenCount).Error; err != nil {
		t.Fatal(err)
	}
	if frozenCount != 0 {
		t.Fatalf("rejected non-exact sources leaked %d frozen lineage rows", frozenCount)
	}
}

func assertPersistedBaselineLineage(
	t *testing.T,
	database *gorm.DB,
	revision ArtifactRevision,
	source VersionRef,
) {
	t.Helper()
	revisionID := uuid.MustParse(revision.ID)
	artifactID := uuid.MustParse(revision.ArtifactID)
	sourceRevisionID := uuid.MustParse(source.RevisionID)

	var artifact storage.ArtifactModel
	if err := database.Where("id = ?", artifactID).Take(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.LatestDraftID == nil {
		t.Fatal("compiled baseline did not retain its generated draft")
	}
	var revisionSources []storage.ArtifactRevisionSourceModel
	if err := database.Where("revision_id = ?", revisionID).Find(&revisionSources).Error; err != nil {
		t.Fatal(err)
	}
	if len(revisionSources) != 1 || revisionSources[0].SourceRevisionID != sourceRevisionID ||
		revisionSources[0].SourceContentHash != source.ContentHash ||
		revisionSources[0].Purpose != "baseline_input" || !revisionSources[0].Required {
		t.Fatalf("unexpected immutable baseline revision sources: %#v", revisionSources)
	}
	var draftSources []storage.ArtifactDraftSourceModel
	if err := database.Where("draft_id = ?", *artifact.LatestDraftID).Find(&draftSources).Error; err != nil {
		t.Fatal(err)
	}
	if len(draftSources) != 1 || draftSources[0].SourceRevisionID != sourceRevisionID ||
		draftSources[0].SourceContentHash != source.ContentHash || draftSources[0].Purpose != "baseline_input" {
		t.Fatalf("unexpected generated baseline draft sources: %#v", draftSources)
	}
	var dependencies []storage.ArtifactDependencyModel
	if err := database.Where("target_revision_id = ?", revisionID).Find(&dependencies).Error; err != nil {
		t.Fatal(err)
	}
	if len(dependencies) != 1 || dependencies[0].SourceRevisionID != sourceRevisionID ||
		dependencies[0].SourceContentHash != source.ContentHash ||
		dependencies[0].Relation != "compiled_into" || !dependencies[0].Required {
		t.Fatalf("unexpected required baseline dependencies: %#v", dependencies)
	}
	var wholeTraceCount int64
	if err := database.Model(&storage.TraceLinkModel{}).Where(
		"source_revision_id = ? AND target_revision_id = ? AND relation = ? AND source_anchor_id IS NULL",
		sourceRevisionID, revisionID, "compiled_into",
	).Count(&wholeTraceCount).Error; err != nil {
		t.Fatal(err)
	}
	if wholeTraceCount != 1 {
		t.Fatalf("required baseline dependency must have one whole-source trace, got %d", wholeTraceCount)
	}
}

func seedApprovedBaselineSource(
	t *testing.T,
	database *gorm.DB,
	store *baselineContentStoreSpy,
	projectID uuid.UUID,
	userID uuid.UUID,
	kind string,
	payload json.RawMessage,
) VersionRef {
	t.Helper()
	now := time.Now().UTC()
	artifactID := uuid.New()
	revisionID := uuid.New()
	contentRef := store.addFinalized("source-"+revisionID.String(), payload)
	artifact := storage.ArtifactModel{
		ID: artifactID, ProjectID: projectID, Kind: kind,
		ArtifactKey: strings.ToUpper(kind) + "-" + strings.ToUpper(artifactID.String()[:8]),
		Title:       kind, Lifecycle: "active", Version: 1, CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	revision := storage.ArtifactRevisionModel{
		ID: revisionID, ArtifactID: artifactID, RevisionNumber: 1, SchemaVersion: 1,
		ContentStore: "mongo", ContentRef: contentRef.ID, ContentHash: contentRef.ContentHash,
		ByteSize: contentRef.ByteSize, WorkflowStatus: "approved", ChangeSource: "human",
		ChangeSummary: "Approved source", CreatedBy: userID, CreatedAt: now, ApprovedAt: &now,
	}
	if err := database.Create(&revision).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).Updates(map[string]any{
		"latest_revision_id": revisionID, "latest_approved_revision_id": revisionID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return VersionRef{
		ArtifactID: artifactID.String(), RevisionID: revisionID.String(), ContentHash: contentRef.ContentHash,
	}
}

func baselinePostgresDatabase(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	base, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := "baseline_compile_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	testDSN := postgresDSNWithSearchPath(t, dsn, schema)
	database, err := gorm.Open(gormpostgres.Open(testDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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

func postgresDSNWithSearchPath(t *testing.T, dsn, schema string) string {
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
