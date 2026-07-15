package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AnchorChange struct {
	AnchorID string `json:"anchorId"`
	Change   string `json:"change"`
}

type ImpactPathStep struct {
	ArtifactID string  `json:"artifactId"`
	RevisionID *string `json:"revisionId,omitempty"`
	AnchorID   *string `json:"anchorId,omitempty"`
	Relation   string  `json:"relation"`
}

type ImpactPath struct {
	Severity string           `json:"severity"`
	Reason   string           `json:"reason"`
	Steps    []ImpactPathStep `json:"steps"`
}

type ImpactReport struct {
	ID               string         `json:"id"`
	ProjectID        string         `json:"projectId"`
	SourceArtifactID string         `json:"sourceArtifactId"`
	FromRevisionID   string         `json:"fromRevisionId"`
	ToRevisionID     string         `json:"toRevisionId"`
	Status           string         `json:"status"`
	Changes          []AnchorChange `json:"changes"`
	Paths            []ImpactPath   `json:"paths"`
	CreatedBy        string         `json:"createdBy"`
	CreatedAt        time.Time      `json:"createdAt"`
}

type AnalyzeImpactInput struct {
	From VersionRef `json:"from"`
	To   VersionRef `json:"to"`
}

type ImpactService struct {
	database *gorm.DB
	contents content.Store
	access   *AccessControl
	trace    *TraceService
	now      func() time.Time
}

func NewImpactService(database *gorm.DB, contents content.Store, access *AccessControl) (*ImpactService, error) {
	if database == nil || contents == nil || access == nil {
		return nil, errors.New("impact database, content store and access control are required")
	}
	trace, _ := NewTraceService(database, access, contents)
	return &ImpactService{database: database, contents: contents, access: access, trace: trace, now: time.Now}, nil
}

func (s *ImpactService) Analyze(ctx context.Context, projectID, actorID string, input AnalyzeImpactInput) (ImpactReport, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionEdit); err != nil {
		return ImpactReport{}, err
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return ImpactReport{}, err
	}
	fromArtifact, fromRevisionID, err := s.trace.validateRef(ctx, projectUUID, input.From)
	if err != nil {
		return ImpactReport{}, err
	}
	toArtifact, toRevisionID, err := s.trace.validateRef(ctx, projectUUID, input.To)
	if err != nil {
		return ImpactReport{}, err
	}
	if fromArtifact != toArtifact || fromRevisionID == toRevisionID {
		return ImpactReport{}, fmt.Errorf("%w: impact revisions must be different versions of one artifact", ErrInvalidInput)
	}
	fromPayload, err := s.revisionPayload(ctx, fromRevisionID, input.From.ContentHash)
	if err != nil {
		return ImpactReport{}, err
	}
	toPayload, err := s.revisionPayload(ctx, toRevisionID, input.To.ContentHash)
	if err != nil {
		return ImpactReport{}, err
	}
	changes := diffAnchors(fromPayload, toPayload)
	paths, affected, err := s.impactPaths(ctx, projectUUID, fromArtifact, fromRevisionID, changes)
	if err != nil {
		return ImpactReport{}, err
	}
	now := s.now().UTC()
	report := ImpactReport{
		ID: uuid.NewString(), ProjectID: projectID, SourceArtifactID: fromArtifact.String(),
		FromRevisionID: fromRevisionID.String(), ToRevisionID: toRevisionID.String(),
		Status: "open", Changes: changes, Paths: paths, CreatedBy: actorID, CreatedAt: now,
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return ImpactReport{}, err
	}
	reportID := uuid.MustParse(report.ID)
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := storage.LockDeliverySliceProjects(transaction, projectUUID); err != nil {
			return err
		}
		model := storage.ImpactReportModel{
			ID: reportID, ProjectID: projectUUID, SourceArtifactID: fromArtifact,
			FromRevisionID: fromRevisionID, ToRevisionID: toRevisionID, Status: "open",
			Report: payload, CreatedBy: actorUUID, CreatedAt: now,
		}
		if err := transaction.Create(&model).Error; err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return err
		}
		affectedArtifactIDs := orderedImpactArtifactIDs(affected)
		// Acquire every health row before touching any delivery slice. Keeping
		// delivery updates inside this loop would allow a partial-overlap cycle:
		// one transaction can hold delivery(A) while waiting for health(B), as a
		// second transaction holds health(B) while waiting for that same delivery
		// row. The two phases preserve one global health-before-delivery order.
		for _, artifactID := range affectedArtifactIDs {
			severity := affected[artifactID]
			health := storage.ArtifactHealthModel{
				ArtifactID: artifactID, SyncStatus: severity, DeliveryStatus: "incomplete",
				FindingCount: 1, BlockingCount: boolInt(severity == "blocked"),
				Report: payload, ComputedAt: now,
			}
			if err := transaction.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "artifact_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"sync_status", "delivery_status", "finding_count", "blocking_count", "report", "computed_at"}),
			}).Create(&health).Error; err != nil {
				return err
			}
		}
		deliverySliceIDs, err := lockAffectedDeliverySliceIDs(
			transaction, projectUUID, affectedArtifactIDs,
		)
		if err != nil {
			return err
		}
		// Apply the weaker delivery status first so a slice that references more
		// than one affected artifact deterministically retains the worst status.
		// Every target row is already locked, so this severity ordering cannot
		// reintroduce a row-lock inversion.
		for _, severity := range []string{"needs_sync", "blocked"} {
			for _, artifactID := range affectedArtifactIDs {
				if len(deliverySliceIDs) == 0 || affected[artifactID] != severity {
					continue
				}
				if err := transaction.Model(&storage.DeliverySliceModel{}).
					Where("id IN ? AND project_id = ? AND (blueprint_revision_id IN (SELECT id FROM artifact_revisions WHERE artifact_id = ?) OR page_spec_revision_id IN (SELECT id FROM artifact_revisions WHERE artifact_id = ?) OR prototype_revision_id IN (SELECT id FROM artifact_revisions WHERE artifact_id = ?))", deliverySliceIDs, projectUUID, artifactID, artifactID, artifactID).
					Updates(map[string]any{"sync_status": severity, "blocker_reason": "Upstream artifact changed; see impact report " + report.ID, "updated_at": now}).Error; err != nil {
					return err
				}
			}
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "impact.analyzed", "impact_report", report.ID, map[string]any{"affectedArtifacts": len(affected)}); err != nil {
			return err
		}
		return enqueue(transaction, "impact_report", report.ID, "impact.analyzed", "worksflow.impact.analyzed", map[string]any{
			"projectId": projectID, "impactReportId": report.ID, "affectedArtifacts": len(affected),
		})
	})
	if err != nil {
		return ImpactReport{}, err
	}
	return report, nil
}

func (s *ImpactService) Get(ctx context.Context, reportID, actorID string) (ImpactReport, error) {
	id, err := uuid.Parse(reportID)
	if err != nil {
		return ImpactReport{}, fmt.Errorf("%w: impact report id", ErrInvalidInput)
	}
	var model storage.ImpactReportModel
	if err := s.database.WithContext(ctx).Where("id = ?", id).Take(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ImpactReport{}, ErrNotFound
		}
		return ImpactReport{}, err
	}
	if _, err := s.access.Authorize(ctx, model.ProjectID.String(), actorID, ActionView); err != nil {
		return ImpactReport{}, err
	}
	var report ImpactReport
	if err := json.Unmarshal(model.Report, &report); err != nil {
		return ImpactReport{}, err
	}
	return report, nil
}

func (s *ImpactService) revisionPayload(ctx context.Context, revisionID uuid.UUID, expectedHash string) (json.RawMessage, error) {
	var revision storage.ArtifactRevisionModel
	if err := s.database.WithContext(ctx).Where("id = ?", revisionID).Take(&revision).Error; err != nil {
		return nil, err
	}
	stored, err := s.contents.Get(ctx, revision.ContentRef, expectedHash)
	if err != nil {
		return nil, err
	}
	return stored.Payload, nil
}

func (s *ImpactService) impactPaths(ctx context.Context, projectID, sourceArtifact, fromRevision uuid.UUID, changes []AnchorChange) ([]ImpactPath, map[uuid.UUID]string, error) {
	changeByAnchor := map[string]string{}
	for _, change := range changes {
		changeByAnchor[change.AnchorID] = change.Change
	}
	var links []storage.TraceLinkModel
	query := s.database.WithContext(ctx).Where("project_id = ? AND source_artifact_id = ? AND source_revision_id = ?", projectID, sourceArtifact, fromRevision)
	if err := query.Find(&links).Error; err != nil {
		return nil, nil, err
	}
	var dependencies []storage.ArtifactDependencyModel
	if err := s.database.WithContext(ctx).
		Where("project_id = ? AND source_artifact_id = ? AND source_revision_id = ?", projectID, sourceArtifact, fromRevision).
		Find(&dependencies).Error; err != nil {
		return nil, nil, err
	}
	paths := make([]ImpactPath, 0)
	affected := map[uuid.UUID]string{}
	queue := make([]ImpactPath, 0)
	for _, link := range links {
		anchor := ""
		if link.SourceAnchorID != nil {
			anchor = *link.SourceAnchorID
		}
		change, changed := changeByAnchor[anchor]
		if !changed && anchor != "" {
			continue
		}
		severity := impactSeverity(change, link.Relation)
		path := ImpactPath{Severity: severity, Reason: impactReason(change, link.Relation), Steps: []ImpactPathStep{
			{ArtifactID: sourceArtifact.String(), RevisionID: stringUUID(fromRevision), AnchorID: link.SourceAnchorID, Relation: "changed"},
			{ArtifactID: link.TargetArtifactID.String(), RevisionID: uuidStringPointer(link.TargetRevisionID), AnchorID: link.TargetAnchorID, Relation: link.Relation},
		}}
		paths = append(paths, path)
		queue = append(queue, path)
		mergeSeverity(affected, link.TargetArtifactID, severity)
	}
	for _, dependency := range dependencies {
		severity := "needs_sync"
		if dependency.Required {
			severity = "blocked"
		}
		path := ImpactPath{Severity: severity, Reason: "The target consumed an older source revision.", Steps: []ImpactPathStep{
			{ArtifactID: sourceArtifact.String(), RevisionID: stringUUID(fromRevision), Relation: "changed"},
			{ArtifactID: dependency.TargetArtifactID.String(), RevisionID: uuidStringPointer(dependency.TargetRevisionID), Relation: dependency.Relation},
		}}
		paths = append(paths, path)
		queue = append(queue, path)
		mergeSeverity(affected, dependency.TargetArtifactID, severity)
	}

	seenPaths := map[string]bool{}
	for _, path := range queue {
		seenPaths[impactPathKey(path)] = true
	}
	for len(queue) > 0 && len(paths) < 10_000 {
		path := queue[0]
		queue = queue[1:]
		last := path.Steps[len(path.Steps)-1]
		artifactID, err := uuid.Parse(last.ArtifactID)
		if err != nil || last.RevisionID == nil {
			continue
		}
		revisionID, err := uuid.Parse(*last.RevisionID)
		if err != nil {
			continue
		}
		var downstream []storage.TraceLinkModel
		if err := s.database.WithContext(ctx).
			Where("project_id = ? AND source_artifact_id = ? AND source_revision_id = ?", projectID, artifactID, revisionID).
			Find(&downstream).Error; err != nil {
			return nil, nil, err
		}
		for _, link := range downstream {
			if impactPathContainsArtifact(path, link.TargetArtifactID) {
				continue
			}
			severity := path.Severity
			if impactSeverity("modified", link.Relation) == "blocked" {
				severity = "blocked"
			}
			next := ImpactPath{Severity: severity, Reason: "Impact propagated through an existing trace link.", Steps: append(cloneImpactSteps(path.Steps), ImpactPathStep{
				ArtifactID: link.TargetArtifactID.String(), RevisionID: uuidStringPointer(link.TargetRevisionID), AnchorID: link.TargetAnchorID, Relation: link.Relation,
			})}
			key := impactPathKey(next)
			if seenPaths[key] {
				continue
			}
			seenPaths[key] = true
			paths = append(paths, next)
			queue = append(queue, next)
			mergeSeverity(affected, link.TargetArtifactID, severity)
		}
		var downstreamDependencies []storage.ArtifactDependencyModel
		if err := s.database.WithContext(ctx).
			Where("project_id = ? AND source_artifact_id = ? AND source_revision_id = ?", projectID, artifactID, revisionID).
			Find(&downstreamDependencies).Error; err != nil {
			return nil, nil, err
		}
		for _, dependency := range downstreamDependencies {
			if impactPathContainsArtifact(path, dependency.TargetArtifactID) {
				continue
			}
			severity := path.Severity
			if dependency.Required {
				severity = "blocked"
			}
			next := ImpactPath{Severity: severity, Reason: "Impact propagated through an exact revision dependency.", Steps: append(cloneImpactSteps(path.Steps), ImpactPathStep{
				ArtifactID: dependency.TargetArtifactID.String(), RevisionID: uuidStringPointer(dependency.TargetRevisionID), Relation: dependency.Relation,
			})}
			key := impactPathKey(next)
			if seenPaths[key] {
				continue
			}
			seenPaths[key] = true
			paths = append(paths, next)
			queue = append(queue, next)
			mergeSeverity(affected, dependency.TargetArtifactID, severity)
		}
	}
	return paths, affected, nil
}

func diffAnchors(from, to json.RawMessage) []AnchorChange {
	fromAnchors := extractAnchors(from)
	toAnchors := extractAnchors(to)
	keys := make([]string, 0, len(fromAnchors)+len(toAnchors))
	seen := map[string]bool{}
	for key := range fromAnchors {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range toAnchors {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	changes := make([]AnchorChange, 0)
	for _, key := range keys {
		before, beforeExists := fromAnchors[key]
		after, afterExists := toAnchors[key]
		switch {
		case beforeExists && !afterExists:
			changes = append(changes, AnchorChange{AnchorID: key, Change: "removed"})
		case !beforeExists && afterExists:
			changes = append(changes, AnchorChange{AnchorID: key, Change: "added"})
		case before != after:
			changes = append(changes, AnchorChange{AnchorID: key, Change: "modified"})
		}
	}
	if len(changes) == 0 && hashJSON(from) != hashJSON(to) {
		changes = append(changes, AnchorChange{AnchorID: "", Change: "modified"})
	}
	return changes
}

func extractAnchors(payload json.RawMessage) map[string]string {
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return nil
	}
	result := map[string]string{}
	for _, collection := range []string{"blocks", "nodes", "states", "layers", "requirements", "acceptanceCriteria"} {
		for _, item := range objectSlice(value[collection]) {
			id := firstString(item, "requirementId", "acceptanceCriterionId", "key", "businessKey", "id", "layerId")
			if id == "" {
				continue
			}
			encoded, _ := json.Marshal(item)
			result[id] = hashJSON(encoded)
		}
	}
	return result
}

func hashJSON(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func impactSeverity(change, relation string) string {
	if change == "removed" || relation == "requires" || relation == "writes" {
		return "blocked"
	}
	return "needs_sync"
}

func impactReason(change, relation string) string {
	if change == "removed" {
		return "A source anchor consumed downstream was removed."
	}
	if relation == "requires" {
		return "A permission or policy dependency changed and requires explicit review."
	}
	return "A source anchor consumed by this artifact changed."
}

func mergeSeverity(values map[uuid.UUID]string, id uuid.UUID, severity string) {
	if current := values[id]; current == "blocked" || current == severity {
		return
	}
	values[id] = severity
}

// orderedImpactArtifactIDs keeps health and delivery mutations on the same
// UUID lock order used by approval and Proposal Apply source-closure locks.
// Iterating the affected map directly can otherwise produce an A/B versus B/A
// row-lock cycle when those transactions overlap.
func orderedImpactArtifactIDs(affected map[uuid.UUID]string) []uuid.UUID {
	artifactIDs := make([]uuid.UUID, 0, len(affected))
	for artifactID := range affected {
		artifactIDs = append(artifactIDs, artifactID)
	}
	sort.Slice(artifactIDs, func(left, right int) bool {
		return artifactIDs[left].String() < artifactIDs[right].String()
	})
	return artifactIDs
}

// lockAffectedDeliverySliceIDs freezes the complete delivery-row set in one
// stable order after every health row has been acquired. The subsequent
// per-artifact updates are restricted to this snapshot, so neither query-plan
// row order nor a concurrently-created slice can reintroduce a delivery-row
// lock inversion between overlapping Impact transactions.
func lockAffectedDeliverySliceIDs(
	transaction *gorm.DB,
	projectID uuid.UUID,
	artifactIDs []uuid.UUID,
) ([]uuid.UUID, error) {
	if len(artifactIDs) == 0 {
		return nil, nil
	}
	var slices []storage.DeliverySliceModel
	if err := transaction.Model(&storage.DeliverySliceModel{}).
		Select("id").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("project_id = ? AND (blueprint_revision_id IN (SELECT id FROM artifact_revisions WHERE artifact_id IN ?) OR page_spec_revision_id IN (SELECT id FROM artifact_revisions WHERE artifact_id IN ?) OR prototype_revision_id IN (SELECT id FROM artifact_revisions WHERE artifact_id IN ?))", projectID, artifactIDs, artifactIDs, artifactIDs).
		Order("id ASC").
		Find(&slices).Error; err != nil {
		return nil, err
	}
	result := make([]uuid.UUID, 0, len(slices))
	for _, slice := range slices {
		result = append(result, slice.ID)
	}
	return result, nil
}

func cloneImpactSteps(values []ImpactPathStep) []ImpactPathStep {
	result := make([]ImpactPathStep, len(values))
	copy(result, values)
	return result
}

func impactPathContainsArtifact(path ImpactPath, artifactID uuid.UUID) bool {
	for _, step := range path.Steps {
		if step.ArtifactID == artifactID.String() {
			return true
		}
	}
	return false
}

func impactPathKey(path ImpactPath) string {
	encoded, _ := json.Marshal(path.Steps)
	return hashJSON(encoded)
}

func stringUUID(value uuid.UUID) *string {
	encoded := value.String()
	return &encoded
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
