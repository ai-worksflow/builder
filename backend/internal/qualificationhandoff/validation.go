package qualificationhandoff

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/qualificationpromotionv2"
)

const (
	gateCompletedRole             = "gate-completed"
	publishAuthorizationRole      = "publish-authorization-required"
	gateCompletedEventType        = "node.completed"
	publishAuthorizationEventType = "node.execution_authorization_required"
	externalQualificationKey      = "external-qualification"
)

var (
	digestPattern           = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	normalizedDigestPattern = regexp.MustCompile(`^(?:sha256:)?[0-9a-f]{64}$`)
	stableIDPattern         = regexp.MustCompile(`^[a-zA-Z0-9]+(?:[a-zA-Z0-9._:/@+-]*[a-zA-Z0-9])?$`)
	buildTimePattern        = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?Z$`)
)

func ValidateHandoffID(handoffID uuid.UUID) error {
	if !validUUIDv4Value(handoffID) {
		return invalid("handoffId", "must be a canonical nonzero UUIDv4")
	}
	return nil
}

func ValidateRecord(record Record) error {
	if err := ValidateHandoffID(record.HandoffID); err != nil {
		return err
	}
	if record.Bundle.HandoffID != record.HandoffID.String() {
		return invalid("record", "bundle belongs to another handoff")
	}
	return ValidateBundle(record.Bundle)
}

func ValidateBundle(bundle CompletionBundle) error {
	if bundle.SchemaVersion != BundleSchemaV1 || !validUUIDv4(bundle.HandoffID) {
		return invalid("bundle", "schema or handoff identity is invalid")
	}
	if err := validateCompletionDocument(bundle.Completion.Document); err != nil {
		return err
	}
	if err := validateRevisionAuthorityDocument(bundle.RevisionAuthority.Document); err != nil {
		return err
	}
	if err := validateOutputRevision(bundle.OutputRevision); err != nil {
		return err
	}
	if err := validateWorkflow(bundle.Workflow); err != nil {
		return err
	}
	if err := validateRetainedDocument(
		"bundle.completion", CompletionHashDomainV1,
		bundle.Completion.Hash, bundle.Completion.BytesHex, bundle.Completion.Document,
	); err != nil {
		return err
	}
	if err := validateRetainedDocument(
		"bundle.revisionAuthority", RevisionAuthorityDomainV1,
		bundle.RevisionAuthority.Hash, bundle.RevisionAuthority.BytesHex, bundle.RevisionAuthority.Document,
	); err != nil {
		return err
	}

	completion := bundle.Completion.Document
	authority := bundle.RevisionAuthority.Document
	output := bundle.OutputRevision
	workflow := bundle.Workflow
	target := authority.Target
	if bundle.HandoffID != completion.HandoffID || bundle.HandoffID != authority.HandoffID ||
		bundle.HandoffID != output.PromotionHandoffID ||
		completion.OperationID != authority.OperationID ||
		completion.OutputRevisionID != authority.OutputRevisionID ||
		completion.OutputRevisionID != output.ID ||
		completion.ConsumptionHash != authority.Promotion.ConsumptionHash {
		return invalid("bundle", "handoff, operation, output Revision, or consumption identities disagree")
	}
	if target.ProjectID != completion.ProjectID || target.ProjectID != workflow.ProjectID ||
		target.WorkflowRunID != completion.WorkflowRunID || target.WorkflowRunID != workflow.WorkflowRunID ||
		target.NodeRunID != completion.NodeRunID || target.NodeRunID != workflow.GateNodeRunID ||
		target.NodeKey != completion.NodeKey || target.NodeKey != workflow.GateNodeKey {
		return invalid("bundle", "Promotion target and workflow completion disagree")
	}
	if target.ArtifactID != output.ArtifactID || target.RevisionID != output.ParentRevisionID ||
		target.RevisionContentHash != output.ContentHash ||
		completion.OutputRevisionContentHash != output.ContentHash {
		return invalid("bundle", "same-content output Revision does not match its qualified parent target")
	}
	if completion.PublishNodeRunID != workflow.PublishNodeRunID ||
		completion.CompletedAt != output.StateAtHandoff.ApprovedAt ||
		completion.CompletedAt != output.CreatedAt ||
		completion.CompletedAt != authority.RevisionStateAtHandoff.ApprovedAt ||
		completion.CompletedAt != authority.RevisionStateAtHandoff.ParentSupersededAt {
		return invalid("bundle", "Handoff-time workflow, Revision, and authority projections disagree")
	}
	if workflow.EventCursorBefore != completion.WorkflowEvents[0].EventSequence-1 ||
		workflow.EventCursorAfter != completion.WorkflowEvents[1].EventSequence ||
		workflow.EventCursorAfter != workflow.EventCursorBefore+2 ||
		completion.WorkflowEvents[1].NodeKey != workflow.PublishNodeKey {
		return invalid("bundle.workflow", "event cursor or Publish projection disagrees with completion")
	}
	quality := workflow.QualityResult
	if quality.WorkspaceRevision.ArtifactID != output.ArtifactID ||
		quality.WorkspaceRevision.RevisionID != output.ID ||
		quality.WorkspaceRevision.ContentHash != output.ContentHash ||
		quality.WorkspaceRevision.AnchorID != nil ||
		quality.Findings.WorkspaceRevision.ArtifactID != target.ArtifactID ||
		quality.Findings.WorkspaceRevision.RevisionID != target.RevisionID ||
		quality.Findings.WorkspaceRevision.ContentHash != target.RevisionContentHash ||
		quality.Findings.WorkspaceRevision.AnchorID != nil ||
		quality.BuildManifest.ProjectID != workflow.ProjectID ||
		quality.BuildManifest.RunID != workflow.WorkflowRunID {
		return invalid("bundle.workflow.qualityResult", "does not bind the output, qualified parent, project, and run")
	}
	encoded, err := qualificationpromotionv2.CanonicalJSON(bundle)
	if err != nil {
		return invalid("bundle", "cannot be canonically represented")
	}
	return validateSecretFree("bundle", encoded)
}

func validateCompletionDocument(document CompletionDocument) error {
	if document.SchemaVersion != CompletionSchemaV1 || !validUUIDv4(document.HandoffID) ||
		!validUUIDv4(document.OperationID) || !validDigest(document.ConsumptionHash) ||
		!validUUIDv4(document.OutputRevisionID) || !validDigest(document.OutputRevisionContentHash) ||
		!validUUIDv4(document.ProjectID) || !validUUIDv4(document.WorkflowRunID) ||
		!validUUIDv4(document.NodeRunID) || !validUUIDv4(document.PublishNodeRunID) ||
		!validCanonicalString(document.NodeKey, 256) || !validMillisecondTime(document.CompletedAt) {
		return invalid("bundle.completion.document", "schema, identity, digest, node key, or timestamp is invalid")
	}
	if document.NodeKey != externalQualificationKey || document.NodeRunID == document.PublishNodeRunID ||
		len(document.WorkflowEvents) != 2 || len(document.OutboxEvents) != 2 {
		return invalid("bundle.completion.document", "must contain the exact external gate and two event pairs")
	}
	gate, publish := document.WorkflowEvents[0], document.WorkflowEvents[1]
	if err := validateWorkflowEvent(gate); err != nil {
		return err
	}
	if err := validateWorkflowEvent(publish); err != nil {
		return err
	}
	if gate.Role != gateCompletedRole || gate.EventType != gateCompletedEventType ||
		gate.NodeRunID != document.NodeRunID || gate.NodeKey != document.NodeKey ||
		publish.Role != publishAuthorizationRole || publish.EventType != publishAuthorizationEventType ||
		publish.NodeRunID != document.PublishNodeRunID || publish.EventSequence != gate.EventSequence+1 ||
		gate.EventID == publish.EventID {
		return invalid("bundle.completion.document.workflowEvents", "roles, types, identities, or sequence are not exact")
	}
	for index, outbox := range document.OutboxEvents {
		if err := validateOutboxEvent(outbox); err != nil {
			return err
		}
		workflowEvent := document.WorkflowEvents[index]
		if outbox.Role != workflowEvent.Role || outbox.EventType != workflowEvent.EventType ||
			outbox.OutboxEventID != workflowEvent.EventID || outbox.WorkflowEventID != workflowEvent.EventID {
			return invalid("bundle.completion.document.outboxEvents", "does not bind the exact Workflow event")
		}
	}
	return nil
}

func validateWorkflowEvent(event WorkflowEvent) error {
	if !validCanonicalString(event.Role, 128) || !validUUIDv4(event.EventID) ||
		event.EventSequence < 1 || event.EventSequence > maximumSafeInteger ||
		!validCanonicalString(event.EventType, 128) || !validUUIDv4(event.NodeRunID) ||
		!validCanonicalString(event.NodeKey, 256) {
		return invalid("bundle.completion.document.workflowEvents", "contains an invalid member")
	}
	return nil
}

func validateOutboxEvent(event OutboxEvent) error {
	if !validCanonicalString(event.Role, 128) || !validUUIDv4(event.OutboxEventID) ||
		!validUUIDv4(event.WorkflowEventID) || !validCanonicalString(event.EventType, 128) {
		return invalid("bundle.completion.document.outboxEvents", "contains an invalid member")
	}
	return nil
}

func validateRevisionAuthorityDocument(document RevisionAuthorityDocument) error {
	if document.SchemaVersion != RevisionAuthoritySchemaV1 || !validUUIDv4(document.HandoffID) ||
		!validUUIDv4(document.OperationID) || !validUUIDv4(document.OutputRevisionID) {
		return invalid("bundle.revisionAuthority.document", "schema or identity is invalid")
	}
	if err := validateAuthorityReference("workflowInput", document.WorkflowInput); err != nil {
		return err
	}
	if err := validateAuthorityReference("plan", document.Plan); err != nil {
		return err
	}
	if document.WorkflowInput.AuthorityID == document.Plan.AuthorityID ||
		!validStableID(document.Receipt.ReceiptID, 256) || !validDigest(document.Receipt.EnvelopeHash) {
		return invalid("bundle.revisionAuthority.document.receipt", "identity or envelope hash is invalid")
	}
	for _, digest := range []string{
		document.Promotion.RequestHash, document.Promotion.ClosureHash,
		document.Promotion.RevisionIntentHash, document.Promotion.ConsumptionHash,
	} {
		if !validDigest(digest) {
			return invalid("bundle.revisionAuthority.document.promotion", "contains an invalid digest")
		}
	}
	if err := validatePromotionTarget(document.Target); err != nil {
		return err
	}
	state := document.RevisionStateAtHandoff
	if state.WorkflowStatus != "approved" || state.SupersededAt != nil ||
		state.ParentWorkflowStatus != "superseded" || !validMillisecondTime(state.ApprovedAt) ||
		!validMicrosecondTime(state.ParentApprovedAt) || !validMillisecondTime(state.ParentSupersededAt) {
		return invalid("bundle.revisionAuthority.document.revisionStateAtHandoff", "is not the exact immutable Handoff-time state")
	}
	lineage := document.CopiedLineage
	if lineage.SchemaVersion != CopiedLineageSchemaV1 || !validDigest(lineage.RootHash) ||
		lineage.SourceCount < 0 || lineage.DependencyCount < 0 || lineage.TraceCount < 0 ||
		lineage.SourceCount > maximumSafeInteger || lineage.DependencyCount > maximumSafeInteger ||
		lineage.TraceCount > maximumSafeInteger {
		return invalid("bundle.revisionAuthority.document.copiedLineage", "schema, root, or member counts are invalid")
	}
	return nil
}

func validateAuthorityReference(name string, reference AuthorityReference) error {
	if !validUUIDv4(reference.AuthorityID) || !validDigest(reference.AuthorityHash) {
		return invalid("bundle.revisionAuthority.document."+name, "identity or hash is invalid")
	}
	return nil
}

func validatePromotionTarget(target PromotionTarget) error {
	if !validUUIDv4(target.ArtifactID) || !validUUIDv4(target.NodeRunID) ||
		!validUUIDv4(target.ProjectID) || !validDigest(target.RevisionContentHash) ||
		!validUUIDv4(target.RevisionID) || !validUUIDv4(target.WorkflowRunID) ||
		target.NodeKey != externalQualificationKey || target.StageGate != externalQualificationKey ||
		!validCanonicalString(target.Subject, 256) {
		return invalid("bundle.revisionAuthority.document.target", "is invalid")
	}
	return nil
}

func validateOutputRevision(revision OutputRevision) error {
	if !validUUIDv4(revision.ID) || !validUUIDv4(revision.ArtifactID) ||
		!validUUIDv4(revision.ParentRevisionID) || revision.RevisionNumber < 1 ||
		revision.RevisionNumber > maximumSafeInteger || revision.SchemaVersion < 1 ||
		revision.SchemaVersion > maximumSafeInteger || !validCanonicalString(revision.ContentStore, 128) ||
		!validCanonicalString(revision.ContentRef, 4096) || !validDigest(revision.ContentHash) ||
		revision.ByteSize < 0 || revision.ByteSize > maximumSafeInteger ||
		!validUUIDv4(revision.PromotionHandoffID) || !validMillisecondTime(revision.CreatedAt) {
		return invalid("bundle.outputRevision", "identity, content projection, size, or timestamp is invalid")
	}
	if revision.ID == revision.ParentRevisionID {
		return invalid("bundle.outputRevision.parentRevisionId", "must identify a distinct qualified parent Revision")
	}
	if revision.StateAtHandoff.WorkflowStatus != "approved" ||
		revision.StateAtHandoff.SupersededAt != nil ||
		!validMillisecondTime(revision.StateAtHandoff.ApprovedAt) {
		return invalid("bundle.outputRevision.stateAtHandoff", "must be the immutable approved Handoff-time state")
	}
	return nil
}

func validateWorkflow(workflow WorkflowProjection) error {
	if !validUUIDv4(workflow.ProjectID) || !validUUIDv4(workflow.WorkflowRunID) ||
		!validUUIDv4(workflow.GateNodeRunID) || workflow.GateNodeKey != externalQualificationKey ||
		!validUUIDv4(workflow.PublishNodeRunID) || !validCanonicalString(workflow.PublishNodeKey, 256) ||
		workflow.GateNodeRunID == workflow.PublishNodeRunID || workflow.EventCursorBefore < 0 ||
		workflow.EventCursorAfter != workflow.EventCursorBefore+2 ||
		workflow.EventCursorAfter > maximumSafeInteger || workflow.GateStatusAtHandoff != "completed" ||
		workflow.PublishStatusAtHandoff != "waiting_input" || workflow.RunStatusAtHandoff != "waiting_input" {
		return invalid("bundle.workflow", "identity, cursor, or Handoff-time status is invalid")
	}
	return validateQualityResult(workflow.QualityResult)
}

func validateQualityResult(result QualityResult) error {
	if !result.Passed || !validUUIDv4(result.QualityRunID) ||
		result.Findings.QualityRunID != result.QualityRunID ||
		!validUUIDv4(result.Findings.ReportArtifactID) || !validUUIDv4(result.Findings.ReportRevisionID) ||
		result.Findings.Score < 0 || result.Findings.Score > 100 {
		return invalid("bundle.workflow.qualityResult", "passing result or frozen findings identity is invalid")
	}
	if err := validateArtifactReference("workspaceRevision", result.WorkspaceRevision); err != nil {
		return err
	}
	if err := validateArtifactReference("findings.workspaceRevision", result.Findings.WorkspaceRevision); err != nil {
		return err
	}
	if result.Findings.Checks == nil || result.Findings.Diagnostics == nil ||
		len(result.Findings.Checks) > 2048 || len(result.Findings.Diagnostics) > 2048 {
		return invalid("bundle.workflow.qualityResult.findings", "checks and diagnostics must be explicit bounded arrays")
	}
	for _, collection := range [][]json.RawMessage{result.Findings.Checks, result.Findings.Diagnostics} {
		for _, item := range collection {
			if err := validateRawJSON(item); err != nil {
				return invalid("bundle.workflow.qualityResult.findings", "contains invalid closed JSON")
			}
		}
	}
	manifest := result.BuildManifest
	if manifest.SchemaVersion < 1 || manifest.SchemaVersion > maximumSafeInteger ||
		!validUUIDv4(manifest.ProjectID) || !validUUIDv4(manifest.RunID) ||
		!validUUIDv4(manifest.ManifestGroupKey) || manifest.SliceIDs == nil || manifest.BundleIDs == nil ||
		len(manifest.SliceIDs) < 1 || len(manifest.SliceIDs) > 1024 ||
		len(manifest.BundleIDs) != len(manifest.SliceIDs) || manifest.Sources == nil ||
		len(manifest.Sources) < 1 || len(manifest.Sources) > 2048 ||
		!validBuildTime(manifest.CreatedAt) || !validNormalizedDigest(manifest.Hash) {
		return invalid("bundle.workflow.qualityResult.buildManifest", "schema, identity, cardinality, time, or hash is invalid")
	}
	if !uniqueCanonicalIDs(manifest.SliceIDs) || !uniqueUUIDs(manifest.BundleIDs) {
		return invalid("bundle.workflow.qualityResult.buildManifest", "slice and bundle identities must be valid and unique")
	}
	seenSources := map[string]struct{}{}
	for _, source := range manifest.Sources {
		if err := validateArtifactReference("buildManifest.sources", source); err != nil {
			return err
		}
		anchor := ""
		if source.AnchorID != nil {
			anchor = *source.AnchorID
		}
		key := source.ArtifactID + "\x00" + source.RevisionID + "\x00" + source.ContentHash + "\x00" + anchor
		if _, exists := seenSources[key]; exists {
			return invalid("bundle.workflow.qualityResult.buildManifest.sources", "contains a duplicate reference")
		}
		seenSources[key] = struct{}{}
	}
	if err := validateRawJSON(manifest.Constraints); err != nil {
		return invalid("bundle.workflow.qualityResult.buildManifest.constraints", "is invalid closed JSON")
	}
	expectedManifestHash := manifest.Hash
	manifest.Hash = ""
	computedManifestHash, err := domain.CanonicalHash(manifest)
	if err != nil || computedManifestHash != expectedManifestHash {
		return invalid("bundle.workflow.qualityResult.buildManifest.hash", "does not authenticate the exact frozen BuildManifest")
	}
	return nil
}

func validateArtifactReference(name string, reference ArtifactReference) error {
	if !validUUIDv4(reference.ArtifactID) || !validUUIDv4(reference.RevisionID) ||
		!validDigest(reference.ContentHash) {
		return invalid("bundle.workflow.qualityResult."+name, "identity or content hash is invalid")
	}
	if reference.AnchorID != nil && !validCanonicalString(*reference.AnchorID, 256) {
		return invalid("bundle.workflow.qualityResult."+name+".anchorId", "is invalid")
	}
	return nil
}

func validateRawJSON(raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > MaximumRetainedBytes || !utf8.Valid(raw) ||
		bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		return ErrInvalid
	}
	if err := rejectDuplicateNames(raw); err != nil {
		return err
	}
	_, err := qualificationpromotionv2.CanonicalJSON(raw)
	if err != nil {
		return err
	}
	return validateSecretFree("rawJSON", raw)
}

func validUUIDv4(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value && validUUIDv4Value(parsed)
}

func validUUIDv4Value(value uuid.UUID) bool {
	return value != uuid.Nil && value.Version() == 4 && value.Variant() == uuid.RFC4122
}

func validDigest(value string) bool { return digestPattern.MatchString(value) }

func validNormalizedDigest(value string) bool { return normalizedDigestPattern.MatchString(value) }

func validStableID(value string, maximum int) bool {
	return validCanonicalString(value, maximum) && stableIDPattern.MatchString(value)
}

func validCanonicalString(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00') &&
		len(value) <= maximum && strings.TrimSpace(value) == value
}

func validMillisecondTime(value string) bool {
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", value)
	return err == nil && parsed.Format("2006-01-02T15:04:05.000Z") == value
}

func validMicrosecondTime(value string) bool {
	parsed, err := time.Parse("2006-01-02T15:04:05.000000Z", value)
	return err == nil && parsed.Format("2006-01-02T15:04:05.000000Z") == value
}

func validBuildTime(value string) bool {
	if !buildTimePattern.MatchString(value) {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC
}

func uniqueCanonicalIDs(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validUUIDv4(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func uniqueUUIDs(values []string) bool { return uniqueCanonicalIDs(values) }
