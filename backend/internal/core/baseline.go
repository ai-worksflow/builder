package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type RequirementBaseline struct {
	SchemaVersion             int               `json:"schemaVersion"`
	SourceVersions            []VersionRef      `json:"sourceVersions"`
	Actors                    []json.RawMessage `json:"actors"`
	Journeys                  []json.RawMessage `json:"journeys"`
	Requirements              []json.RawMessage `json:"requirements"`
	BusinessRules             []json.RawMessage `json:"businessRules"`
	NonFunctionalRequirements []json.RawMessage `json:"nonFunctionalRequirements"`
	Constraints               []json.RawMessage `json:"constraints"`
	Decisions                 []json.RawMessage `json:"decisions"`
	References                []json.RawMessage `json:"references"`
	NonBlockingOpenQuestions  []json.RawMessage `json:"nonBlockingOpenQuestions"`
	BaselineHash              string            `json:"baselineHash"`
}

type BaselineService struct {
	database *gorm.DB
	contents content.Store
	access   *AccessControl
	trace    *TraceService
	now      func() time.Time
}

func NewBaselineService(database *gorm.DB, contents content.Store, access *AccessControl) (*BaselineService, error) {
	if database == nil || contents == nil || access == nil {
		return nil, errors.New("baseline database, content store and access control are required")
	}
	trace, _ := NewTraceService(database, access)
	return &BaselineService{database: database, contents: contents, access: access, trace: trace, now: time.Now}, nil
}

func (s *BaselineService) Compile(ctx context.Context, projectID, actorID string, sources []VersionRef) (ArtifactRevision, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionEdit); err != nil {
		return ArtifactRevision{}, err
	}
	projectUUID, actorUUID, err := parseProjectUser(projectID, actorID)
	if err != nil {
		return ArtifactRevision{}, err
	}
	if len(sources) == 0 {
		return ArtifactRevision{}, fmt.Errorf("%w: baseline sources", ErrInvalidInput)
	}
	baseline := RequirementBaseline{
		SchemaVersion: 1, SourceVersions: []VersionRef{}, Actors: []json.RawMessage{},
		Journeys: []json.RawMessage{}, Requirements: []json.RawMessage{}, BusinessRules: []json.RawMessage{},
		NonFunctionalRequirements: []json.RawMessage{}, Constraints: []json.RawMessage{},
		Decisions: []json.RawMessage{}, References: []json.RawMessage{}, NonBlockingOpenQuestions: []json.RawMessage{},
	}
	sourceModels := make([]storage.ArtifactRevisionModel, 0, len(sources))
	anchorsBySource := map[uuid.UUID][]string{}
	for _, source := range sources {
		artifactID, revisionID, err := s.trace.validateRef(ctx, projectUUID, source)
		if err != nil {
			return ArtifactRevision{}, err
		}
		var artifact storage.ArtifactModel
		if err := s.database.WithContext(ctx).Where("id = ?", artifactID).Take(&artifact).Error; err != nil {
			return ArtifactRevision{}, err
		}
		if artifact.Kind != "project_brief" && artifact.Kind != "product_requirements" && artifact.Kind != "decision_record" && artifact.Kind != "glossary_policy" && artifact.Kind != "requirement_baseline" {
			return ArtifactRevision{}, fmt.Errorf("%w: unsupported baseline source %s", ErrInvalidInput, artifact.Kind)
		}
		var revision storage.ArtifactRevisionModel
		if err := s.database.WithContext(ctx).Where("id = ? AND workflow_status = 'approved'", revisionID).Take(&revision).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ArtifactRevision{}, fmt.Errorf("%w: baseline sources must be approved", ErrBlockingGate)
			}
			return ArtifactRevision{}, err
		}
		stored, err := s.contents.Get(ctx, revision.ContentRef, revision.ContentHash)
		if err != nil {
			return ArtifactRevision{}, err
		}
		if artifact.Kind == "requirement_baseline" {
			if len(sources) != 1 {
				return ArtifactRevision{}, fmt.Errorf("%w: an existing Requirement Baseline cannot be mixed with document sources", ErrInvalidInput)
			}
			if report := ValidateArtifactContent(artifact.Kind, stored.Payload); !report.Valid {
				return ArtifactRevision{}, fmt.Errorf("%w: existing Requirement Baseline is invalid", ErrBlockingGate)
			}
			sourceModels, err := (&ArtifactService{database: s.database}).loadRevisionSourceModels(ctx, []uuid.UUID{revision.ID})
			if err != nil {
				return ArtifactRevision{}, err
			}
			return revisionFromModel(revision, stored.Payload, revisionSourcesFromModels(sourceModels[revision.ID])), nil
		}
		if report := ValidateArtifactContent(artifact.Kind, stored.Payload); !report.Valid && (artifact.Kind == "project_brief" || artifact.Kind == "product_requirements") {
			return ArtifactRevision{}, fmt.Errorf("%w: source validation failed", ErrBlockingGate)
		}
		var content map[string]any
		if err := json.Unmarshal(stored.Payload, &content); err != nil {
			return ArtifactRevision{}, err
		}
		anchorsBySource[revision.ID] = append(anchorsBySource[revision.ID], appendBaselineContent(&baseline, content)...)
		baseline.SourceVersions = appendUniqueRef(baseline.SourceVersions, source)
		sourceModels = append(sourceModels, revision)
	}
	sortVersionRefs(baseline.SourceVersions)
	payload, err := finalizeRequirementBaselinePayload(baseline)
	if err != nil {
		return ArtifactRevision{}, err
	}
	revisionID := uuid.New()
	contentRef, err := s.contents.PutPending(ctx, projectID, "requirement_baseline_revision", revisionID.String(), 1, payload)
	if err != nil {
		return ArtifactRevision{}, err
	}
	abortPending := true
	defer func() {
		if abortPending {
			_ = s.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	now := s.now().UTC()
	var revision storage.ArtifactRevisionModel
	var frozenSources []ArtifactSource
	reusedRevision := false
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var artifact storage.ArtifactModel
		err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND artifact_key = 'REQUIREMENT-BASELINE'", projectUUID).Take(&artifact).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			artifact = storage.ArtifactModel{
				ID: uuid.New(), ProjectID: projectUUID, Kind: "requirement_baseline",
				ArtifactKey: "REQUIREMENT-BASELINE", Title: "Requirement Baseline",
				Lifecycle: "active", Version: 1, CreatedBy: actorUUID, CreatedAt: now, UpdatedAt: now,
			}
			if err := transaction.Create(&artifact).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if artifact.Kind != "requirement_baseline" {
			return fmt.Errorf("%w: REQUIREMENT-BASELINE key belongs to a non-baseline artifact", ErrConflict)
		}
		if err := ensureArtifactHealthRow(transaction, artifact.ID, now); err != nil {
			return err
		}
		var existing storage.ArtifactRevisionModel
		err = transaction.Where(
			"artifact_id = ? AND content_hash = ? AND workflow_status = 'approved'",
			artifact.ID, contentRef.ContentHash,
		).Order("revision_number DESC").Take(&existing).Error
		if err == nil {
			revision = existing
			reusedRevision = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var latest uint64
		if err := transaction.Model(&storage.ArtifactRevisionModel{}).Where("artifact_id = ?", artifact.ID).
			Select("COALESCE(MAX(revision_number), 0)").Scan(&latest).Error; err != nil {
			return err
		}
		revision = storage.ArtifactRevisionModel{
			ID: revisionID, ArtifactID: artifact.ID, RevisionNumber: latest + 1,
			ParentRevisionID: artifact.LatestApprovedRevisionID, SchemaVersion: 1,
			ContentStore: "mongo", ContentRef: contentRef.ID, ContentHash: contentRef.ContentHash,
			ByteSize: contentRef.ByteSize, WorkflowStatus: "approved", ChangeSource: "system",
			ChangeSummary: "Compile approved requirement baseline", CreatedBy: actorUUID,
			CreatedAt: now, ApprovedAt: &now,
		}
		if artifact.LatestApprovedRevisionID != nil {
			if err := transaction.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", *artifact.LatestApprovedRevisionID).
				Updates(map[string]any{"workflow_status": "superseded", "superseded_at": now}).Error; err != nil {
				return err
			}
		}
		if err := transaction.Create(&revision).Error; err != nil {
			return err
		}
		draftID := uuid.New()
		draft := storage.ArtifactDraftModel{
			ID: draftID, ArtifactID: artifact.ID, BaseRevisionID: &revision.ID, Sequence: 1,
			ETag: draftETag(draftID, 1, revision.ContentHash), SchemaVersion: 1,
			ContentStore: "mongo", ContentRef: revision.ContentRef, ContentHash: revision.ContentHash,
			ByteSize: revision.ByteSize, Status: "draft", CreatedBy: actorUUID, UpdatedBy: actorUUID,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := transaction.Create(&draft).Error; err != nil {
			return err
		}
		lineageSources := make([]SystemRevisionSource, 0, len(baseline.SourceVersions))
		for _, source := range baseline.SourceVersions {
			lineageSources = append(lineageSources, SystemRevisionSource{
				Ref: source, Purpose: "baseline_input", Required: true, Relation: "compiled_into",
			})
		}
		frozenSources, err = PersistSystemRevisionLineage(
			transaction, projectUUID, artifact.ID, revision.ID, draft.ID,
			actorUUID, now, lineageSources,
		)
		if err != nil {
			return err
		}
		if err := transaction.Model(&storage.ArtifactModel{}).Where("id = ?", artifact.ID).
			Updates(map[string]any{
				"latest_revision_id": revision.ID, "latest_approved_revision_id": revision.ID,
				"latest_draft_id": draft.ID, "version": gorm.Expr("version + 1"), "updated_at": now,
			}).Error; err != nil {
			return err
		}
		for _, source := range sourceModels {
			for _, anchor := range anchorsBySource[source.ID] {
				anchorValue := anchor
				link := storage.TraceLinkModel{
					ID: uuid.New(), ProjectID: projectUUID, SourceArtifactID: source.ArtifactID,
					SourceRevisionID: source.ID, SourceAnchorID: &anchorValue,
					TargetArtifactID: artifact.ID, TargetRevisionID: &revision.ID,
					TargetAnchorID: &anchorValue, Relation: "compiled_into", Metadata: json.RawMessage(`{}`),
					CreatedBy: actorUUID, CreatedAt: now,
				}
				if err := transaction.Create(&link).Error; err != nil {
					return err
				}
			}
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "requirements.baseline_compiled", "artifact_revision", revision.ID.String(), map[string]any{"sourceCount": len(sourceModels)}); err != nil {
			return err
		}
		return enqueue(transaction, "artifact", artifact.ID.String(), "requirements.baseline_compiled", "worksflow.requirements.baseline.compiled", map[string]any{
			"projectId": projectID, "artifactId": artifact.ID.String(), "revisionId": revision.ID.String(),
		})
	})
	if err != nil {
		return ArtifactRevision{}, err
	}
	if reusedRevision {
		sourceModels, err := (&ArtifactService{database: s.database}).loadRevisionSourceModels(ctx, []uuid.UUID{revision.ID})
		if err != nil {
			return ArtifactRevision{}, err
		}
		return revisionFromModel(revision, payload, revisionSourcesFromModels(sourceModels[revision.ID])), nil
	}
	abortPending = false
	if err := s.contents.Finalize(ctx, contentRef.ID); err != nil {
		return ArtifactRevision{}, fmt.Errorf("%w: %v", ErrContentNotReady, err)
	}
	return revisionFromModel(revision, payload, frozenSources), nil
}

func finalizeRequirementBaselinePayload(baseline RequirementBaseline) (json.RawMessage, error) {
	hashPayload := baseline
	hashPayload.BaselineHash = ""
	baselineHash, err := domain.CanonicalHash(hashPayload)
	if err != nil {
		return nil, err
	}
	baseline.BaselineHash = baselineHash
	payload, err := json.Marshal(baseline)
	if err != nil {
		return nil, err
	}
	report := ValidateArtifactContent("requirement_baseline", payload)
	if !report.Valid {
		message := "compiled Requirement Baseline is invalid"
		if len(report.Findings) > 0 {
			message = report.Findings[0].Code + ": " + report.Findings[0].Message
		}
		return nil, fmt.Errorf("%w: %s", ErrBlockingGate, message)
	}
	return payload, nil
}

func appendBaselineContent(baseline *RequirementBaseline, content map[string]any) []string {
	anchors := make([]string, 0)
	seenAnchors := map[string]struct{}{}
	appendAnchor := func(anchor string) bool {
		if anchor == "" {
			return true
		}
		if _, duplicate := seenAnchors[anchor]; duplicate {
			return false
		}
		seenAnchors[anchor] = struct{}{}
		anchors = append(anchors, anchor)
		return true
	}
	for _, block := range objectSlice(content["blocks"]) {
		encoded, _ := json.Marshal(block)
		blockType := firstString(block, "type")
		anchor := firstString(block, "requirementId", "acceptanceCriterionId", "key", "id")
		switch blockType {
		case "actor":
			baseline.Actors = append(baseline.Actors, encoded)
		case "userJourney":
			baseline.Journeys = append(baseline.Journeys, encoded)
		case "requirement", "acceptanceCriterion":
			if appendAnchor(anchor) {
				baseline.Requirements = append(baseline.Requirements, encoded)
			}
			continue
		case "businessRule":
			baseline.BusinessRules = append(baseline.BusinessRules, encoded)
		case "nonFunctionalRequirement":
			baseline.NonFunctionalRequirements = append(baseline.NonFunctionalRequirements, encoded)
		case "constraint":
			baseline.Constraints = append(baseline.Constraints, encoded)
		case "decision":
			baseline.Decisions = append(baseline.Decisions, encoded)
		case "sourceReference":
			baseline.References = append(baseline.References, encoded)
		case "openQuestion":
			if !boolean(block["blocking"]) {
				baseline.NonBlockingOpenQuestions = append(baseline.NonBlockingOpenQuestions, encoded)
			}
		}
		appendAnchor(anchor)
	}
	for _, requirement := range objectSlice(content["requirements"]) {
		anchor := firstString(requirement, "requirementId", "key", "id")
		if !appendAnchor(anchor) {
			continue
		}
		baseline.Requirements = append(baseline.Requirements, canonicalBaselineRequirement(requirement, "requirement", "requirementId", anchor))
	}
	for _, criterion := range objectSlice(content["acceptanceCriteria"]) {
		anchor := firstString(criterion, "acceptanceCriterionId", "key", "id")
		if !appendAnchor(anchor) {
			continue
		}
		baseline.Requirements = append(baseline.Requirements, canonicalBaselineRequirement(criterion, "acceptanceCriterion", "acceptanceCriterionId", anchor))
	}
	return anchors
}

func canonicalBaselineRequirement(value map[string]any, blockType, key, anchor string) json.RawMessage {
	canonical := make(map[string]any, len(value)+2)
	for field, fieldValue := range value {
		canonical[field] = fieldValue
	}
	canonical["type"] = blockType
	canonical[key] = anchor
	encoded, _ := json.Marshal(canonical)
	return encoded
}
