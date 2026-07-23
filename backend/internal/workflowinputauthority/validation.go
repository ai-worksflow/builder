package workflowinputauthority

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/canonicalreviewreceipt"
	"github.com/worksflow/builder/backend/internal/constructor"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

const (
	canonicalTimeLayout    = "2006-01-02T15:04:05.000000Z"
	executionProfileHashV3 = "sha256:854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104"
)

var (
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	legacyHash      = regexp.MustCompile(`^(?:sha256:)?[0-9a-f]{64}$`)
	stableIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$`)
	hexPattern      = regexp.MustCompile(`^[0-9a-f]+$`)
)

type nodeInputEnvelopeWire struct {
	Bindings []domain.NodeInputBinding `json:"bindings"`
	Hash     string                    `json:"hash"`
}

// qualityGateResultWire and qualityWorkflowBuildManifestWire freeze the
// production workflow.QualityResult/BuildManifest JSON contract consumed by
// the external-qualification gate. They remain private so v1 cannot widen by
// accidentally embedding a mutable runtime type.
type qualityGateResultWire struct {
	Passed            bool                          `json:"passed"`
	Findings          json.RawMessage               `json:"findings"`
	QualityRunID      string                        `json:"qualityRunId"`
	WorkspaceRevision *domain.ArtifactRef           `json:"workspaceRevision"`
	BuildManifest     *qualityWorkflowBuildManifest `json:"buildManifest"`
}

type qualityWorkflowBuildManifest struct {
	SchemaVersion    int64                `json:"schemaVersion"`
	ProjectID        string               `json:"projectId"`
	RunID            string               `json:"runId"`
	ManifestGroupKey string               `json:"manifestGroupKey"`
	SliceIDs         []string             `json:"sliceIds"`
	BundleIDs        []string             `json:"bundleIds"`
	Sources          []domain.ArtifactRef `json:"sources"`
	Constraints      json.RawMessage      `json:"constraints"`
	CreatedAt        time.Time            `json:"createdAt"`
	Hash             string               `json:"hash"`
}

type qualityFindingsWire struct {
	Checks            json.RawMessage `json:"checks"`
	Diagnostics       json.RawMessage `json:"diagnostics"`
	QualityRunID      string          `json:"qualityRunId"`
	ReportArtifactID  string          `json:"reportArtifactId"`
	ReportRevisionID  string          `json:"reportRevisionId"`
	Score             int64           `json:"score"`
	WorkspaceRevision core.VersionRef `json:"workspaceRevision"`
}

type predecessorSourceFacts struct {
	DefinitionNodeID     string
	InputManifest        *ManifestReference
	NodeKey              string
	NodeType             string
	OutputProposal       *ProposalReference
	OutputRevisionNumber int64
	SliceIdentity        SliceIdentity
	Status               string
}

type proposalPinFacts struct {
	Manifest                 ManifestReference
	ProducerDefinitionNodeID string
	ProducerNodeKey          string
}

type proposalIdentityFacts struct {
	PayloadHash string
	Pin         *proposalPinFacts
}

func validateCandidate(candidate Candidate) error {
	request := candidate.Request
	input := candidate.Input
	if err := validateRequest(request); err != nil {
		return err
	}
	if err := validateInput(input); err != nil {
		return err
	}
	if err := validateCandidateDocument(candidate.Document); err != nil {
		return err
	}
	if err := validateCrossBindings(request, input, input.Target, AuthorityEnvelope{
		AuthorityID: request.AuthorityID, NodeRunID: request.NodeRunID, OperationID: request.OperationID,
		ProjectID: request.ProjectID, TargetHash: input.TargetHash, WorkflowRunID: request.WorkflowRunID,
	}, "", "", input.TargetHash); err != nil {
		return err
	}
	if err := validateCandidateClosure(candidate); err != nil {
		return err
	}
	if err := validateMaterials(candidate); err != nil {
		return err
	}
	return validateGlobalIdentities(candidate)
}

func validateRequest(request FreezeRequest) error {
	if request.SchemaVersion != FreezeRequestSchemaV1 || request.MediaType != FreezeRequestMediaTypeV1 {
		return invalid("request", "schemaVersion or mediaType is invalid")
	}
	if !validUUIDv4(request.AuthorityID) || !validUUIDv4(request.NodeRunID) || !validUUIDv4(request.OperationID) ||
		!validUUIDv4(request.ProjectID) || !validUUIDv4(request.WorkflowRunID) {
		return invalid("request", "all identities must be canonical nonzero UUIDv4 values")
	}
	if request.AuthorityID == request.OperationID {
		return invalid("request", "authorityId and operationId must be distinct")
	}
	if request.ExpectedRunCursor < 0 || request.ExpectedRunCursor >= MaximumJavaScriptSafeInt {
		return invalid("request.expectedRunCursor", "must leave room for one activation event")
	}
	if request.NodeKey != ExternalQualificationGate {
		return invalid("request.nodeKey", "must identify the dedicated external-qualification gate")
	}
	return nil
}

func validateTarget(target TargetDocument) error {
	if !validUUIDv4(target.ProjectID) || !validUUIDv4(target.WorkflowRunID) || !validUUIDv4(target.TargetRevisionID) ||
		!validDigest(target.TargetRevisionContentHash) {
		return invalid("target", "project, run, revision, or content digest is invalid")
	}
	if target.NodeKey != ExternalQualificationGate || target.StageGate != ExternalQualificationGate ||
		!validCanonicalString(target.ManifestSubject, 256, false) {
		return invalid("target", "subject, nodeKey, or stageGate is invalid")
	}
	return nil
}

func validateInput(input WorkflowInputDocument) error {
	if input.SchemaVersion != InputSchemaV1 || input.MediaType != InputMediaTypeV1 {
		return invalid("input", "schemaVersion or mediaType is invalid")
	}
	if !validUUIDv4(input.Project.ID) || (input.Project.GovernanceMode != GovernanceSolo && input.Project.GovernanceMode != GovernanceTeam) {
		return invalid("input.project", "identity or governance mode is invalid")
	}
	if err := validateDefinition(input.Definition); err != nil {
		return err
	}
	if err := validateRun(input.Run); err != nil {
		return err
	}
	if err := validateGate(input.Gate); err != nil {
		return err
	}
	if err := validateTarget(input.Target); err != nil {
		return err
	}
	targetBytes, err := CanonicalJSON(input.Target)
	if err != nil || input.TargetHash != DomainHash(TargetHashDomainV1, targetBytes) {
		return invalid("input.targetHash", "does not bind the exact target document")
	}
	if err := validateNodeInput(input.NodeInput, len(input.Predecessors)); err != nil {
		return err
	}
	if err := validatePredecessors(input.Predecessors); err != nil {
		return err
	}
	if err := validateManifests(input.InputManifests, input.Project.ID, input.Run); err != nil {
		return err
	}
	if err := validateManifestClosure(input.Predecessors, input.InputManifests); err != nil {
		return err
	}
	if err := validateRevisions(input.Revisions, input.InputManifests, input.Target); err != nil {
		return err
	}
	if err := validatePredecessorRevisionClosure(input.Predecessors, input.Revisions); err != nil {
		return err
	}
	if err := validateReviewReceipts(input.ReviewReceipts, input.Revisions, input.Project.ID); err != nil {
		return err
	}
	if err := validateBuild(input.Build); err != nil {
		return err
	}
	if !validUUIDv4(input.QualificationPolicy.AuthorityID) || !validDigest(input.QualificationPolicy.AuthorityHash) ||
		input.QualificationPolicy.ExternalGatePolicy != ExternalQualificationPolicyV1 {
		return invalid("input.qualificationPolicy", "authority or external gate policy is invalid")
	}
	quality := input.QualityResult
	if !quality.Passed || !validUUIDv4(quality.QualityRunID) || !validUUIDv4(quality.BuildManifestID) ||
		!validDigest(quality.BuildManifestHash) || !validUUIDv4(quality.WorkspaceRevisionID) ||
		!validDigest(quality.WorkspaceRevisionContentHash) {
		return invalid("input.qualityResult", "must be one typed passing result with exact build/workspace identities")
	}
	if quality.BuildManifestID != input.Build.BuildManifest.ID || quality.BuildManifestHash != input.Build.BuildManifest.ManifestHash ||
		quality.WorkspaceRevisionID != input.Target.TargetRevisionID ||
		quality.WorkspaceRevisionContentHash != input.Target.TargetRevisionContentHash {
		return invalid("input.qualityResult", "does not bind build manifest and Workspace target")
	}
	qualityPredecessors := 0
	for _, predecessor := range input.Predecessors {
		if predecessor.SourceNodeType == string(domain.NodeQualityGate) {
			qualityPredecessors++
			targetFound := false
			for _, reference := range predecessor.ArtifactRevisions {
				if reference.RevisionID == input.Target.TargetRevisionID && reference.ContentHash == input.Target.TargetRevisionContentHash {
					targetFound = true
					break
				}
			}
			if !targetFound {
				return invalid("input.predecessors", "quality predecessor does not bind the exact Workspace target")
			}
		}
	}
	if qualityPredecessors != 1 {
		return invalid("input.predecessors", "must contain exactly one quality_gate predecessor")
	}
	return nil
}

func validateDefinition(definition DefinitionBinding) error {
	if !validUUIDv4(definition.DefinitionID) || !validUUIDv4(definition.DefinitionVersionID) ||
		!validDigest(definition.DefinitionHash) || !validDigest(definition.ExecutionProfileHash) ||
		!validDigest(definition.RawBytesHash) || definition.DefinitionVersion < 1 ||
		definition.DefinitionVersion > MaximumJavaScriptSafeInt || definition.ExecutionProfileVersion != ExecutionProfileV3 ||
		definition.ExecutionProfileHash != executionProfileHashV3 || !validSizedBytes(definition.RawBytesSize, MaximumDefinitionBytes) {
		return invalid("input.definition", "identity, version, profile, digest, or raw size is invalid")
	}
	return nil
}

func validateRun(run RunBinding) error {
	if !validUUIDv4(run.ID) || !validUUIDv4(run.InputManifestID) || !validUUIDv4(run.StartedBy) ||
		!validDigest(run.InputManifestHash) || !validDigest(run.ScopeRawBytesHash) ||
		!validSizedBytes(run.ScopeRawBytesSize, MaximumRunScopeBytes) || !validCanonicalTime(run.StartedAt) {
		return invalid("input.run", "identity, manifest, raw scope, or start time is invalid")
	}
	return nil
}

func validateGate(gate GateBinding) error {
	if !validUUIDv4(gate.ActivationEventID) || !validUUIDv4(gate.NodeRunID) ||
		gate.ActivationEventSequence < 1 || gate.ActivationEventSequence > MaximumJavaScriptSafeInt ||
		gate.DefinitionNodeID != ExternalQualificationGate || gate.GateName != ExternalQualificationGate ||
		gate.NodeKey != ExternalQualificationGate || gate.NodeType != ExternalQualificationNodeType ||
		gate.StageGate != ExternalQualificationGate || gate.SliceIdentity.Kind != SliceKindRoot || gate.SliceIdentity.ID != "" {
		return invalid("input.gate", "must be the exact root external-qualification gate activation")
	}
	return nil
}

func validateNodeInput(input NodeInputBinding, predecessorCount int) error {
	if input.BindingCount < 1 || input.BindingCount > MaximumPredecessors || int64(predecessorCount) != input.BindingCount ||
		!validDigest(input.RawBytesHash) || !validDigest(input.SemanticHash) ||
		!validSizedBytes(input.RawBytesSize, MaximumNodeInputBytes) {
		return invalid("input.nodeInput", "count, semantic hash, raw hash, or raw size is invalid")
	}
	return nil
}

func validatePredecessors(predecessors []PredecessorBinding) error {
	if predecessors == nil || len(predecessors) < 1 || len(predecessors) > MaximumPredecessors {
		return invalid("input.predecessors", "must contain a bounded non-null set")
	}
	previous := ""
	seenSourceNodes := map[string]struct{}{}
	sourceFactsByID := map[string]predecessorSourceFacts{}
	proposalFactsByID := map[string]proposalIdentityFacts{}
	deliverySliceFactsByID := map[string]DeliverySliceReference{}
	registerProposal := func(reference ProposalReference, pin *ProposalLineagePin) error {
		facts, exists := proposalFactsByID[reference.ID]
		if exists && facts.PayloadHash != reference.PayloadHash {
			return invalid("input.predecessors", "proposalId %s carries conflicting payload hashes", reference.ID)
		}
		if !exists {
			facts.PayloadHash = reference.PayloadHash
		}
		if pin != nil {
			pinFacts := proposalPinFacts{
				Manifest: pin.Manifest, ProducerDefinitionNodeID: pin.ProducerDefinitionNodeID,
				ProducerNodeKey: pin.ProducerNodeKey,
			}
			if facts.Pin != nil && !reflect.DeepEqual(*facts.Pin, pinFacts) {
				return invalid("input.predecessors", "proposalId %s carries conflicting lineage facts", reference.ID)
			}
			facts.Pin = &pinFacts
		}
		proposalFactsByID[reference.ID] = facts
		return nil
	}
	for index, predecessor := range predecessors {
		key := predecessor.EdgeID + "\x00" + predecessor.SourceNodeRunID + "\x00" + predecessor.SourcePort + "\x00" + predecessor.TargetPort
		if index > 0 && previous >= key {
			return invalid("input.predecessors", "must be strictly sorted by edgeId, sourceNodeRunId, sourcePort, targetPort")
		}
		previous = key
		if predecessor.MappingOrdinal != int64(index) || !validStableID(predecessor.EdgeID, 256) ||
			!validStableID(predecessor.SourcePort, 256) || !validStableID(predecessor.TargetPort, 256) ||
			(predecessor.MappingKind != MappingKindIdentity && predecessor.MappingKind != MappingKindObjectMap) ||
			!validUUIDv4(predecessor.SourceNodeRunID) || !validStableID(predecessor.SourceNodeKey, 256) ||
			!validStableID(predecessor.SourceDefinitionNodeID, 256) || !validSourceNodeType(predecessor.SourceNodeType) ||
			predecessor.SourceStatus != CompletedSourceStatus || !validDigest(predecessor.BindingRawBytesHash) ||
			!validDigest(predecessor.MappingHash) || !validDigest(predecessor.OutputHash) || !validDigest(predecessor.ValueHash) ||
			predecessor.OutputRevisionNumber < 1 || predecessor.OutputRevisionNumber > MaximumJavaScriptSafeInt {
			return invalid("input.predecessors", "member %d has invalid identity, order, mapping, status, hash, or revision number", index)
		}
		if err := validateSliceIdentity(predecessor.SourceSliceIdentity); err != nil {
			return invalid("input.predecessors", "member %d source slice: %v", index, err)
		}
		if predecessor.InputManifest != nil && (!validUUIDv4(predecessor.InputManifest.ID) || !validDigest(predecessor.InputManifest.Hash)) {
			return invalid("input.predecessors", "member %d inputManifest is invalid", index)
		}
		if predecessor.OutputProposal != nil && (!validUUIDv4(predecessor.OutputProposal.ID) || !validDigest(predecessor.OutputProposal.PayloadHash)) {
			return invalid("input.predecessors", "member %d outputProposal is invalid", index)
		}
		if err := validateArtifactReferences(predecessor.ArtifactRevisions, "artifactRevisions"); err != nil {
			return invalid("input.predecessors", "member %d: %v", index, err)
		}
		if err := validateArtifactReferences(predecessor.MaterializedArtifactRevisions, "materializedArtifactRevisions"); err != nil {
			return invalid("input.predecessors", "member %d: %v", index, err)
		}
		if err := validateProposalPins(predecessor.ProposalPins); err != nil {
			return invalid("input.predecessors", "member %d: %v", index, err)
		}
		if err := validateDeliverySlices(predecessor.DeliverySliceRefs); err != nil {
			return invalid("input.predecessors", "member %d: %v", index, err)
		}
		sourceFacts := predecessorSourceFacts{
			DefinitionNodeID: predecessor.SourceDefinitionNodeID, InputManifest: predecessor.InputManifest,
			NodeKey: predecessor.SourceNodeKey, NodeType: predecessor.SourceNodeType,
			OutputProposal: predecessor.OutputProposal, OutputRevisionNumber: predecessor.OutputRevisionNumber,
			SliceIdentity: predecessor.SourceSliceIdentity, Status: predecessor.SourceStatus,
		}
		if old, exists := sourceFactsByID[predecessor.SourceNodeRunID]; exists && !reflect.DeepEqual(old, sourceFacts) {
			return invalid("input.predecessors", "sourceNodeRunId %s carries conflicting locked row facts", predecessor.SourceNodeRunID)
		}
		sourceFactsByID[predecessor.SourceNodeRunID] = sourceFacts
		if predecessor.OutputProposal != nil {
			if err := registerProposal(*predecessor.OutputProposal, nil); err != nil {
				return err
			}
		}
		for pinIndex := range predecessor.ProposalPins {
			pin := &predecessor.ProposalPins[pinIndex]
			if err := registerProposal(pin.Proposal, pin); err != nil {
				return err
			}
		}
		for _, reference := range predecessor.DeliverySliceRefs {
			if old, exists := deliverySliceFactsByID[reference.ID]; exists && !reflect.DeepEqual(old, reference) {
				return invalid("input.predecessors", "delivery slice id %s carries conflicting frozen facts", reference.ID)
			}
			deliverySliceFactsByID[reference.ID] = reference
		}
		if _, duplicate := seenSourceNodes[predecessor.SourceNodeRunID+"\x00"+predecessor.EdgeID+"\x00"+predecessor.SourcePort+"\x00"+predecessor.TargetPort]; duplicate {
			return invalid("input.predecessors", "duplicate source binding identity")
		}
		seenSourceNodes[predecessor.SourceNodeRunID+"\x00"+predecessor.EdgeID+"\x00"+predecessor.SourcePort+"\x00"+predecessor.TargetPort] = struct{}{}
	}
	return nil
}

func validateSliceIdentity(identity SliceIdentity) error {
	switch identity.Kind {
	case SliceKindRoot:
		if identity.ID != "" {
			return invalid("sliceIdentity", "root cannot carry id")
		}
	case SliceKindDelivery:
		if !validUUIDv4(identity.ID) {
			return invalid("sliceIdentity", "slice variant requires UUIDv4 id")
		}
	default:
		return invalid("sliceIdentity", "kind is not closed")
	}
	return nil
}

func validateArtifactReferences(references []ArtifactRevisionReference, field string) error {
	if references == nil || len(references) > MaximumRevisions {
		return invalid(field, "must be a bounded non-null array")
	}
	previous := ""
	for _, reference := range references {
		key := reference.ArtifactID + "\x00" + reference.RevisionID + "\x00" + reference.AnchorID
		if !validUUIDv4(reference.ArtifactID) || !validUUIDv4(reference.RevisionID) || !validDigest(reference.ContentHash) ||
			(reference.AnchorID != "" && !validStableID(reference.AnchorID, 256)) || (previous != "" && previous >= key) {
			return invalid(field, "must contain sorted unique exact artifact revisions")
		}
		previous = key
	}
	return nil
}

func validateProposalPins(pins []ProposalLineagePin) error {
	if pins == nil || len(pins) > MaximumManifests {
		return invalid("proposalPins", "must be a bounded non-null array")
	}
	previous := ""
	for _, pin := range pins {
		key := pin.ProducerNodeKey + "\x00" + pin.Proposal.ID + "\x00" + pin.Manifest.ID
		if !validUUIDv4(pin.Manifest.ID) || !validDigest(pin.Manifest.Hash) || !validUUIDv4(pin.Proposal.ID) ||
			!validDigest(pin.Proposal.PayloadHash) || !validStableID(pin.ProducerDefinitionNodeID, 256) ||
			!validStableID(pin.ProducerNodeKey, 256) || (previous != "" && previous >= key) {
			return invalid("proposalPins", "must contain sorted unique closed lineage pins")
		}
		previous = key
	}
	return nil
}

func validateDeliverySlices(references []DeliverySliceReference) error {
	if references == nil || len(references) > MaximumManifests {
		return invalid("deliverySliceRefs", "must be a bounded non-null array")
	}
	previous := ""
	seenIDs := map[string]DeliverySliceReference{}
	for _, reference := range references {
		key := reference.FanOutNodeID + "\x00" + reference.Key + "\x00" + reference.ID
		if !validUUIDv4(reference.ID) || !validStableID(reference.FanOutNodeID, 256) ||
			!validCanonicalString(reference.Key, 256, false) || (previous != "" && previous >= key) {
			return invalid("deliverySliceRefs", "identity or order is invalid")
		}
		previous = key
		if old, duplicate := seenIDs[reference.ID]; duplicate && !reflect.DeepEqual(old, reference) {
			return invalid("deliverySliceRefs", "one slice id carries conflicting facts")
		}
		seenIDs[reference.ID] = reference
		for _, artifact := range []*ArtifactRevisionReference{reference.Blueprint, reference.PageSpec, reference.Prototype} {
			if artifact != nil && (!validUUIDv4(artifact.ArtifactID) || !validUUIDv4(artifact.RevisionID) || !validDigest(artifact.ContentHash) ||
				(artifact.AnchorID != "" && !validStableID(artifact.AnchorID, 256))) {
				return invalid("deliverySliceRefs", "artifact reference is invalid")
			}
		}
	}
	return nil
}

func validateManifests(manifests []InputManifestBinding, projectID string, run RunBinding) error {
	if manifests == nil || len(manifests) < 1 || len(manifests) > MaximumManifests {
		return invalid("input.inputManifests", "must contain a bounded non-null set")
	}
	previous := ""
	runCount := 0
	nodeCount := 0
	qualificationCount := 0
	factsByID := map[string]InputManifestBinding{}
	for index, manifest := range manifests {
		key := manifest.Role + "\x00" + manifest.ID
		if index > 0 && previous >= key {
			return invalid("input.inputManifests", "must be strictly sorted by role, manifestId")
		}
		previous = key
		if !validManifestRole(manifest.Role) || !validUUIDv4(manifest.ID) || manifest.ProjectID != projectID ||
			!validStableID(manifest.Kind, 256) || manifest.SchemaVersion < 1 || manifest.SchemaVersion > MaximumJavaScriptSafeInt ||
			!validDigest(manifest.ManifestHash) || !validDigest(manifest.ContentHash) || !validDigest(manifest.RawBytesHash) ||
			!validCanonicalString(manifest.ContentStore, 128, false) || !validContentRef(manifest.ContentRef) ||
			!validSizedBytes(manifest.RawBytesSize, MaximumManifestBytes) || manifest.ContentHash != manifest.RawBytesHash {
			return invalid("input.inputManifests", "member %d is invalid", index)
		}
		facts := manifest
		facts.Role = ""
		if old, exists := factsByID[manifest.ID]; exists && !reflect.DeepEqual(old, facts) {
			return invalid("input.inputManifests", "manifestId %s carries conflicting immutable facts", manifest.ID)
		}
		factsByID[manifest.ID] = facts
		if manifest.Role == ManifestRoleRun {
			runCount++
			if manifest.ID != run.InputManifestID || manifest.ManifestHash != run.InputManifestHash {
				return invalid("input.inputManifests", "run member does not bind the run InputManifest")
			}
		}
		if manifest.Role == ManifestRoleNode {
			nodeCount++
		}
		if manifest.Role == ManifestRoleQualification {
			qualificationCount++
		}
	}
	if runCount != 1 {
		return invalid("input.inputManifests", "exactly one run InputManifest binding is required")
	}
	if nodeCount != 0 || qualificationCount != 0 {
		return invalid("input.inputManifests", "v1 external qualification has no target-node or qualification manifest source")
	}
	return nil
}

func validateManifestClosure(predecessors []PredecessorBinding, manifests []InputManifestBinding) error {
	predecessorManifests := map[string]InputManifestBinding{}
	for _, manifest := range manifests {
		if manifest.Role == ManifestRolePredecessor {
			predecessorManifests[manifest.ID] = manifest
		}
	}
	required := map[string]string{}
	for _, predecessor := range predecessors {
		if predecessor.InputManifest != nil {
			if old, exists := required[predecessor.InputManifest.ID]; exists && old != predecessor.InputManifest.Hash {
				return invalid("input.inputManifests", "one predecessor manifest id carries conflicting hashes")
			}
			required[predecessor.InputManifest.ID] = predecessor.InputManifest.Hash
		}
		for _, pin := range predecessor.ProposalPins {
			if old, exists := required[pin.Manifest.ID]; exists && old != pin.Manifest.Hash {
				return invalid("input.inputManifests", "one predecessor manifest id carries conflicting hashes")
			}
			required[pin.Manifest.ID] = pin.Manifest.Hash
		}
	}
	if len(required) != len(predecessorManifests) {
		return invalid("input.inputManifests", "predecessor role set is not the exact propagated manifest closure")
	}
	for id, hash := range required {
		manifest, exists := predecessorManifests[id]
		if !exists || manifest.ManifestHash != hash {
			return invalid("input.inputManifests", "required predecessor manifest %s is absent or hash-drifted", id)
		}
	}
	return nil
}

func validateRevisions(revisions []RevisionBinding, manifests []InputManifestBinding, target TargetDocument) error {
	if revisions == nil || len(revisions) < 1 || len(revisions) > MaximumRevisions {
		return invalid("input.revisions", "must contain a bounded non-null set")
	}
	manifestIDs := map[string]struct{}{}
	for _, manifest := range manifests {
		manifestIDs[manifest.ID] = struct{}{}
	}
	previous := ""
	workspaceCount := 0
	factsByRevision := map[string]RevisionBinding{}
	for index, revision := range revisions {
		key := revision.Purpose + "\x00" + revision.ArtifactID + "\x00" + revision.RevisionID
		if index > 0 && previous >= key {
			return invalid("input.revisions", "must be strictly sorted by purpose, artifactId, revisionId")
		}
		previous = key
		if !validStableID(revision.Purpose, 256) || !validArtifactKind(revision.ArtifactKind) ||
			!validUUIDv4(revision.ArtifactID) || !validUUIDv4(revision.RevisionID) || !validDigest(revision.ContentHash) ||
			!validDigest(revision.RawBytesHash) || revision.ContentHash != revision.RawBytesHash ||
			!validCanonicalString(revision.ContentStore, 128, false) || !validContentRef(revision.ContentRef) ||
			revision.SchemaVersion < 1 || revision.SchemaVersion > MaximumJavaScriptSafeInt ||
			!validSizedBytes(revision.ByteSize, MaximumRevisionBytes) || revision.WorkflowStatusAtFreeze != ApprovedRevisionStatus ||
			!validRevisionCurrencyPolicy(revision.CurrencyPolicy) || !validRevisionChangeSource(revision.ChangeSourceAtFreeze) ||
			!validOptionalUUIDv4(revision.SourceManifestID) || !validOptionalUUIDv4(revision.ProposalID) ||
			!validOptionalUUIDv4(revision.ImplementationProposalID) {
			return invalid("input.revisions", "member %d is invalid", index)
		}
		if revision.SourceManifestID != nil {
			if _, exists := manifestIDs[*revision.SourceManifestID]; !exists {
				return invalid("input.revisions", "member %d references an absent InputManifest", index)
			}
		}
		if revision.CurrencyPolicy == CurrencyLatestApprovedRequired &&
			(!revision.IsLatestCurrentAtFreeze || !revision.IsLatestApprovedAtFreeze) {
			return invalid("input.revisions", "member %d latest-approved-required currency is not latest/current approved", index)
		}
		if revision.ChangeSourceAtFreeze == "human" && !revision.CanonicalReviewRequired {
			return invalid("input.revisions", "member %d human change cannot bypass canonical review", index)
		}
		if revision.Purpose != RevisionPurposeWorkspaceTarget && revision.RevisionID == target.TargetRevisionID {
			return invalid("input.revisions", "member %d governed source reuses the Workspace target revision id", index)
		}
		facts := cloneRevision(revision)
		facts.Purpose = ""
		facts.CurrencyPolicy = ""
		facts.CanonicalReviewRequired = false
		facts.SourceRequiredAtFreeze = false
		if old, exists := factsByRevision[revision.RevisionID]; exists && !reflect.DeepEqual(old, facts) {
			return invalid("input.revisions", "revisionId %s carries conflicting immutable facts", revision.RevisionID)
		}
		factsByRevision[revision.RevisionID] = facts
		if revision.Purpose == RevisionPurposeWorkspaceTarget {
			workspaceCount++
			if revision.ArtifactKind != "workspace" || revision.RevisionID != target.TargetRevisionID ||
				revision.ContentHash != target.TargetRevisionContentHash || revision.CurrencyPolicy != CurrencyLatestApprovedRequired ||
				!revision.IsLatestCurrentAtFreeze || !revision.IsLatestApprovedAtFreeze || revision.ImplementationProposalID == nil ||
				revision.CanonicalReviewRequired || revision.SourceRequiredAtFreeze {
				return invalid("input.revisions", "Workspace target does not bind the exact latest-approved target")
			}
		}
	}
	if workspaceCount != 1 {
		return invalid("input.revisions", "exactly one workspace-target revision is required")
	}
	return nil
}

func validateReviewReceipts(receipts []ReviewReceiptBinding, revisions []RevisionBinding, projectID string) error {
	if receipts == nil || len(receipts) > MaximumReviewReceipts {
		return invalid("input.reviewReceipts", "must be a bounded non-null array")
	}
	revisionByKey := map[string]RevisionBinding{}
	for _, revision := range revisions {
		revisionByKey[revision.Purpose+"\x00"+revision.RevisionID] = revision
	}
	previous := ""
	seenRevisions := map[string]struct{}{}
	seenRequests := map[string]struct{}{}
	seenTargets := map[string]struct{}{}
	for index, receipt := range receipts {
		key := receipt.Purpose + "\x00" + receipt.RevisionID
		if index > 0 && previous >= key {
			return invalid("input.reviewReceipts", "must be strictly sorted by purpose, revisionId")
		}
		previous = key
		if !validStableID(receipt.Purpose, 256) || receipt.ProjectID != projectID || !validUUIDv4(receipt.ReviewRequestID) ||
			!validUUIDv4(receipt.ArtifactID) || !validUUIDv4(receipt.RevisionID) || !validDigest(receipt.RevisionContentHash) ||
			!validDigest(receipt.ReceiptHash) || !validDigest(receipt.ReceiptRawBytesHash) ||
			!validSizedBytes(receipt.ReceiptRawBytesSize, MaximumReviewReceiptBytes) || receipt.ReceiptSchemaVersion != CanonicalReviewReceiptV1 {
			return invalid("input.reviewReceipts", "member %d is invalid", index)
		}
		revision, exists := revisionByKey[receipt.Purpose+"\x00"+receipt.RevisionID]
		if !exists || revision.ArtifactID != receipt.ArtifactID || revision.ContentHash != receipt.RevisionContentHash ||
			revision.Purpose == RevisionPurposeWorkspaceTarget || !revision.CanonicalReviewRequired {
			return invalid("input.reviewReceipts", "member %d does not bind one governed source revision", index)
		}
		if _, duplicate := seenRevisions[receipt.RevisionID]; duplicate {
			return invalid("input.reviewReceipts", "one revision cannot bind multiple receipt requests")
		}
		if _, duplicate := seenRequests[receipt.ReviewRequestID]; duplicate {
			return invalid("input.reviewReceipts", "one receipt request cannot bind multiple revisions")
		}
		seenRevisions[receipt.RevisionID] = struct{}{}
		seenRequests[receipt.ReviewRequestID] = struct{}{}
		seenTargets[receipt.Purpose+"\x00"+receipt.RevisionID] = struct{}{}
	}
	expected := 0
	for _, revision := range revisions {
		if !revision.CanonicalReviewRequired {
			continue
		}
		expected++
		if _, exists := seenTargets[revision.Purpose+"\x00"+revision.RevisionID]; !exists {
			return invalid("input.reviewReceipts", "required governed source revision %s/%s has no exact receipt", revision.Purpose, revision.RevisionID)
		}
	}
	if len(receipts) != expected {
		return invalid("input.reviewReceipts", "must equal the policy-derived canonical-review source subset")
	}
	return nil
}

func validatePredecessorRevisionClosure(predecessors []PredecessorBinding, revisions []RevisionBinding) error {
	revisionByID := map[string]RevisionBinding{}
	for _, revision := range revisions {
		revisionByID[revision.RevisionID] = revision
	}
	for predecessorIndex, predecessor := range predecessors {
		outputs := map[string]ArtifactRevisionReference{}
		for _, reference := range predecessor.ArtifactRevisions {
			revision, exists := revisionByID[reference.RevisionID]
			if !exists || revision.ArtifactID != reference.ArtifactID || revision.ContentHash != reference.ContentHash {
				return invalid("input.predecessors", "member %d artifact revision is absent from frozen revision closure", predecessorIndex)
			}
			outputs[reference.RevisionID] = reference
		}
		for _, reference := range predecessor.MaterializedArtifactRevisions {
			revision, exists := revisionByID[reference.RevisionID]
			output, outputExists := outputs[reference.RevisionID]
			if !exists || !outputExists || revision.ArtifactID != reference.ArtifactID || revision.ContentHash != reference.ContentHash ||
				output.ArtifactID != reference.ArtifactID || output.ContentHash != reference.ContentHash {
				return invalid("input.predecessors", "member %d materialized revision does not exactly bind an artifact revision", predecessorIndex)
			}
		}
		for _, slice := range predecessor.DeliverySliceRefs {
			for _, reference := range []*ArtifactRevisionReference{slice.Blueprint, slice.PageSpec, slice.Prototype} {
				if reference == nil {
					continue
				}
				revision, exists := revisionByID[reference.RevisionID]
				if !exists || revision.ArtifactID != reference.ArtifactID || revision.ContentHash != reference.ContentHash {
					return invalid("input.predecessors", "member %d delivery slice revision is absent from frozen closure", predecessorIndex)
				}
			}
		}
	}
	return nil
}

func validateBuild(build BuildBinding) error {
	manifest := build.BuildManifest
	contract := build.BuildContract
	if !validUUIDv4(manifest.ID) || !validDigest(manifest.ContentHash) || !validDigest(manifest.ManifestHash) ||
		!validDigest(manifest.RawBytesHash) || manifest.ContentHash != manifest.RawBytesHash ||
		!validSizedBytes(manifest.RawBytesSize, MaximumBuildManifestBytes) || manifest.StatusAtFreeze != ConsumedBuildManifestStatus {
		return invalid("input.build.buildManifest", "identity, hashes, bytes, or consumed status is invalid")
	}
	if !validUUIDv4(contract.ID) || !validDigest(contract.ContentHash) || !validDigest(contract.ContractHash) ||
		!validDigest(contract.RawBytesHash) || contract.ContentHash != contract.RawBytesHash ||
		!validSizedBytes(contract.RawBytesSize, MaximumBuildContractBytes) || contract.StatusAtFreeze != ReadyBuildContractStatus {
		return invalid("input.build.buildContract", "identity, hashes, bytes, or ready status is invalid")
	}
	if manifest.ID == contract.ID {
		return invalid("input.build", "manifest and contract identities must be distinct")
	}
	return nil
}

func validateEnvelope(envelope AuthorityEnvelope) error {
	if envelope.SchemaVersion != AuthoritySchemaV1 || envelope.MediaType != AuthorityMediaTypeV1 {
		return invalid("authority", "schemaVersion or mediaType is invalid")
	}
	if !validUUIDv4(envelope.AuthorityID) || !validUUIDv4(envelope.NodeRunID) || !validUUIDv4(envelope.OperationID) ||
		!validUUIDv4(envelope.ProjectID) || !validUUIDv4(envelope.WorkflowRunID) || envelope.AuthorityID == envelope.OperationID {
		return invalid("authority", "identity closure is invalid")
	}
	for name, value := range map[string]string{"inputHash": envelope.InputHash, "requestHash": envelope.RequestHash, "targetHash": envelope.TargetHash} {
		if !validDigest(value) {
			return invalid("authority."+name, "digest is invalid")
		}
	}
	return nil
}

func validateCrossBindings(request FreezeRequest, input WorkflowInputDocument, target TargetDocument, envelope AuthorityEnvelope, requestHash, inputHash, targetHash string) error {
	if input.Project.ID != request.ProjectID || input.Run.ID != request.WorkflowRunID || input.Gate.NodeRunID != request.NodeRunID ||
		input.Gate.NodeKey != request.NodeKey || input.Gate.ActivationEventSequence != request.ExpectedRunCursor+1 ||
		target != input.Target || target.ProjectID != request.ProjectID || target.WorkflowRunID != request.WorkflowRunID ||
		target.NodeKey != request.NodeKey || target.StageGate != input.Gate.StageGate || input.TargetHash != targetHash ||
		envelope.AuthorityID != request.AuthorityID || envelope.OperationID != request.OperationID ||
		envelope.ProjectID != request.ProjectID || envelope.WorkflowRunID != request.WorkflowRunID ||
		envelope.NodeRunID != request.NodeRunID {
		return invalid("closure", "request, target, input, gate, run, project, and envelope identities disagree")
	}
	if requestHash != "" && envelope.RequestHash != requestHash || inputHash != "" && envelope.InputHash != inputHash ||
		targetHash != "" && envelope.TargetHash != targetHash {
		return invalid("closure", "envelope hashes disagree with exact documents")
	}
	return nil
}

func validateCandidateDocument(document FreezeCandidateDocument) error {
	if !validCanonicalString(document.ManifestSubject, 256, false) || document.InputManifests == nil ||
		document.Revisions == nil || document.ReviewRequirements == nil || len(document.InputManifests) < 1 ||
		len(document.InputManifests) > MaximumManifests || len(document.Revisions) < 1 ||
		len(document.Revisions) > MaximumRevisions || len(document.ReviewRequirements) > MaximumReviewReceipts {
		return invalid("candidate", "root scalar or collection bounds are invalid")
	}
	if !validUUIDv4(document.QualificationPolicy.AuthorityID) || !validDigest(document.QualificationPolicy.AuthorityHash) ||
		document.QualificationPolicy.ExternalGatePolicy != ExternalQualificationPolicyV1 {
		return invalid("candidate.qualificationPolicy", "is invalid")
	}
	quality := document.QualityResult
	if !quality.Passed || !validUUIDv4(quality.BuildContractID) || !validDigest(quality.BuildContractHash) ||
		!validUUIDv4(quality.BuildManifestID) || !validDigest(quality.BuildManifestHash) || !validUUIDv4(quality.QualityRunID) ||
		!validUUIDv4(quality.WorkspaceRevisionID) || !validDigest(quality.WorkspaceRevisionContentHash) {
		return invalid("candidate.qualityResult", "is invalid")
	}
	previous := ""
	for index, manifest := range document.InputManifests {
		key := manifest.Role + "\x00" + manifest.ManifestID
		if index > 0 && previous >= key || !validManifestRole(manifest.Role) || !validUUIDv4(manifest.ManifestID) ||
			!validRawHex(manifest.RawBytesHex, MaximumManifestBytes) {
			return invalid("candidate.inputManifests", "must be sorted unique closed manifest candidates")
		}
		previous = key
	}
	previous = ""
	for index, revision := range document.Revisions {
		key := revision.Purpose + "\x00" + revision.RevisionID
		if index > 0 && previous >= key || !validStableID(revision.Purpose, 256) || !validUUIDv4(revision.RevisionID) ||
			!validRevisionCurrencyPolicy(revision.CurrencyPolicy) ||
			!validRawHex(revision.RawBytesHex, MaximumRevisionBytes) {
			return invalid("candidate.revisions", "must be sorted unique closed revision candidates")
		}
		previous = key
	}
	previous = ""
	seenReviewRevisions := map[string]struct{}{}
	for index, requirement := range document.ReviewRequirements {
		key := requirement.Purpose + "\x00" + requirement.RevisionID
		if index > 0 && previous >= key || !validStableID(requirement.Purpose, 256) || !validUUIDv4(requirement.RevisionID) {
			return invalid("candidate.reviewRequirements", "must be sorted unique closed review targets")
		}
		if _, duplicate := seenReviewRevisions[requirement.RevisionID]; duplicate {
			return invalid("candidate.reviewRequirements", "one revision cannot require multiple canonical reviews")
		}
		seenReviewRevisions[requirement.RevisionID] = struct{}{}
		previous = key
	}
	encoded, err := canonicalJSONWithLimit(document, MaximumCandidateBytes)
	if err != nil || len(encoded) > MaximumCandidateBytes {
		return invalid("candidate", "canonical private document is oversized or invalid")
	}
	return nil
}

func validateCandidateClosure(candidate Candidate) error {
	document := candidate.Document
	input := candidate.Input
	if document.ManifestSubject != input.Target.ManifestSubject || document.QualificationPolicy != input.QualificationPolicy {
		return invalid("candidate", "subject or qualification policy disagrees with locked input")
	}
	quality := document.QualityResult
	if quality.BuildContractID != input.Build.BuildContract.ID || quality.BuildContractHash != input.Build.BuildContract.ContractHash ||
		quality.BuildManifestID != input.QualityResult.BuildManifestID || quality.BuildManifestHash != input.QualityResult.BuildManifestHash ||
		quality.Passed != input.QualityResult.Passed || quality.QualityRunID != input.QualityResult.QualityRunID ||
		quality.WorkspaceRevisionID != input.QualityResult.WorkspaceRevisionID ||
		quality.WorkspaceRevisionContentHash != input.QualityResult.WorkspaceRevisionContentHash {
		return invalid("candidate.qualityResult", "disagrees with locked build/quality input")
	}
	manifestByKey := map[string]InputManifestBinding{}
	for _, manifest := range input.InputManifests {
		manifestByKey[manifest.Role+"\x00"+manifest.ID] = manifest
	}
	if len(manifestByKey) != len(document.InputManifests) {
		return invalid("candidate.inputManifests", "does not equal the locked output manifest set")
	}
	for _, member := range document.InputManifests {
		if _, exists := manifestByKey[member.Role+"\x00"+member.ManifestID]; !exists {
			return invalid("candidate.inputManifests", "contains a manifest absent from locked output")
		}
	}
	revisionByKey := map[string]RevisionBinding{}
	for _, revision := range input.Revisions {
		revisionByKey[revision.Purpose+"\x00"+revision.RevisionID] = revision
	}
	if len(revisionByKey) != len(document.Revisions) {
		return invalid("candidate.revisions", "does not equal the locked output revision set")
	}
	for _, member := range document.Revisions {
		revision, exists := revisionByKey[member.Purpose+"\x00"+member.RevisionID]
		if !exists || revision.CurrencyPolicy != member.CurrencyPolicy ||
			revision.CanonicalReviewRequired != member.CanonicalReviewRequired {
			return invalid("candidate.revisions", "contains a revision absent from locked output or changes currency")
		}
	}
	// The qualification-policy authority decides the exact Canonical Review
	// subset. The candidate repeats that result only as an expected assertion;
	// it is not an authorization input to the PostgreSQL issuer.
	required := make([]ReviewRequirementCandidate, 0, len(input.Revisions)-1)
	for _, revision := range input.Revisions {
		if revision.CanonicalReviewRequired {
			required = append(required, ReviewRequirementCandidate{Purpose: revision.Purpose, RevisionID: revision.RevisionID})
		}
	}
	sort.Slice(required, func(i, j int) bool {
		return required[i].Purpose+"\x00"+required[i].RevisionID < required[j].Purpose+"\x00"+required[j].RevisionID
	})
	if !reflect.DeepEqual(required, document.ReviewRequirements) || len(input.ReviewReceipts) != len(required) {
		return invalid("candidate.reviewRequirements", "must equal the policy-derived canonical-review source subset")
	}
	for index, requirement := range required {
		receipt := input.ReviewReceipts[index]
		if receipt.Purpose != requirement.Purpose || receipt.RevisionID != requirement.RevisionID {
			return invalid("input.reviewReceipts", "does not equal the required canonical-review target set")
		}
	}
	return nil
}

func candidateDocumentFromRecord(input WorkflowInputDocument, materials RetainedMaterials) (FreezeCandidateDocument, error) {
	manifestRaw := map[string][]byte{}
	for _, material := range materials.InputManifests {
		key := material.Role + "\x00" + material.ManifestID
		if _, duplicate := manifestRaw[key]; duplicate {
			return FreezeCandidateDocument{}, invalid("materials.inputManifests", "duplicate role/manifest identity")
		}
		manifestRaw[key] = material.Bytes
	}
	revisionRaw := map[string][]byte{}
	for _, material := range materials.Revisions {
		key := material.Purpose + "\x00" + material.RevisionID
		if _, duplicate := revisionRaw[key]; duplicate {
			return FreezeCandidateDocument{}, invalid("materials.revisions", "duplicate purpose/revision identity")
		}
		revisionRaw[key] = material.Bytes
	}
	document := FreezeCandidateDocument{
		InputManifests:      make([]ManifestCandidate, 0, len(input.InputManifests)),
		ManifestSubject:     input.Target.ManifestSubject,
		QualificationPolicy: input.QualificationPolicy,
		QualityResult: CandidateQualityResult{
			BuildContractHash: input.Build.BuildContract.ContractHash, BuildContractID: input.Build.BuildContract.ID,
			BuildManifestHash: input.QualityResult.BuildManifestHash, BuildManifestID: input.QualityResult.BuildManifestID,
			Passed: input.QualityResult.Passed, QualityRunID: input.QualityResult.QualityRunID,
			WorkspaceRevisionContentHash: input.QualityResult.WorkspaceRevisionContentHash,
			WorkspaceRevisionID:          input.QualityResult.WorkspaceRevisionID,
		},
		ReviewRequirements: make([]ReviewRequirementCandidate, 0, len(input.ReviewReceipts)),
		Revisions:          make([]RevisionCandidate, 0, len(input.Revisions)),
	}
	for _, manifest := range input.InputManifests {
		raw, exists := manifestRaw[manifest.Role+"\x00"+manifest.ID]
		if !exists {
			return FreezeCandidateDocument{}, invalid("materials.inputManifests", "raw bytes are missing")
		}
		document.InputManifests = append(document.InputManifests, ManifestCandidate{
			ManifestID: manifest.ID, RawBytesHex: hex.EncodeToString(raw), Role: manifest.Role,
		})
	}
	for _, revision := range input.Revisions {
		raw, exists := revisionRaw[revision.Purpose+"\x00"+revision.RevisionID]
		if !exists {
			return FreezeCandidateDocument{}, invalid("materials.revisions", "raw bytes are missing")
		}
		document.Revisions = append(document.Revisions, RevisionCandidate{
			CanonicalReviewRequired: revision.CanonicalReviewRequired,
			CurrencyPolicy:          revision.CurrencyPolicy,
			Purpose:                 revision.Purpose,
			RawBytesHex:             hex.EncodeToString(raw),
			RevisionID:              revision.RevisionID,
		})
	}
	for _, receipt := range input.ReviewReceipts {
		document.ReviewRequirements = append(document.ReviewRequirements, ReviewRequirementCandidate{
			Purpose: receipt.Purpose, RevisionID: receipt.RevisionID,
		})
	}
	sort.Slice(document.InputManifests, func(i, j int) bool {
		return document.InputManifests[i].Role+"\x00"+document.InputManifests[i].ManifestID <
			document.InputManifests[j].Role+"\x00"+document.InputManifests[j].ManifestID
	})
	sort.Slice(document.Revisions, func(i, j int) bool {
		return document.Revisions[i].Purpose+"\x00"+document.Revisions[i].RevisionID <
			document.Revisions[j].Purpose+"\x00"+document.Revisions[j].RevisionID
	})
	sort.Slice(document.ReviewRequirements, func(i, j int) bool {
		return document.ReviewRequirements[i].Purpose+"\x00"+document.ReviewRequirements[i].RevisionID <
			document.ReviewRequirements[j].Purpose+"\x00"+document.ReviewRequirements[j].RevisionID
	})
	return document, nil
}

func validateMaterials(candidate Candidate) error {
	input := candidate.Input
	materials := candidate.Materials
	total := 0
	add := func(name string, value []byte, maximum int) error {
		if len(value) < 1 || len(value) > maximum {
			return invalid("materials."+name, "is absent or oversized")
		}
		if err := validateRetainedMaterialSafety(name, value, maximum); err != nil {
			return err
		}
		total += len(value)
		if total > MaximumRetainedBytes {
			return invalid("materials", "total retained bytes exceed the v1 bound")
		}
		return nil
	}
	if err := add("definition", materials.Definition, MaximumDefinitionBytes); err != nil {
		return err
	}
	if err := add("runScope", materials.RunScope, MaximumRunScopeBytes); err != nil {
		return err
	}
	if err := add("nodeInput", materials.NodeInput, MaximumNodeInputBytes); err != nil {
		return err
	}
	if err := add("buildManifest", materials.BuildManifest, MaximumBuildManifestBytes); err != nil {
		return err
	}
	if err := add("buildContract", materials.BuildContract, MaximumBuildContractBytes); err != nil {
		return err
	}
	if RawSHA256(materials.Definition) != input.Definition.RawBytesHash || int64(len(materials.Definition)) != input.Definition.RawBytesSize ||
		RawSHA256(materials.RunScope) != input.Run.ScopeRawBytesHash || int64(len(materials.RunScope)) != input.Run.ScopeRawBytesSize ||
		RawSHA256(materials.NodeInput) != input.NodeInput.RawBytesHash || int64(len(materials.NodeInput)) != input.NodeInput.RawBytesSize ||
		RawSHA256(materials.BuildManifest) != input.Build.BuildManifest.RawBytesHash || int64(len(materials.BuildManifest)) != input.Build.BuildManifest.RawBytesSize ||
		RawSHA256(materials.BuildContract) != input.Build.BuildContract.RawBytesHash || int64(len(materials.BuildContract)) != input.Build.BuildContract.RawBytesSize {
		return invalid("materials", "top-level raw digest or size disagrees with locked input")
	}
	if err := validateDefinitionMaterial(materials.Definition, input); err != nil {
		return err
	}
	if _, err := decodeRetainedGeneric("runScope", materials.RunScope, MaximumRunScopeBytes); err != nil {
		return err
	}
	nodeBindings, err := validateNodeInputMaterial(materials.NodeInput, input, materials.BuildManifest)
	if err != nil {
		return err
	}
	if err := validateManifestMaterials(candidate, nodeBindings, add); err != nil {
		return err
	}
	if err := validateRevisionMaterials(candidate, add); err != nil {
		return err
	}
	if err := validateBuildMaterials(materials.BuildManifest, materials.BuildContract, input); err != nil {
		return err
	}
	if err := validateReceiptMaterials(materials.ReviewReceipts, input, add); err != nil {
		return err
	}
	return nil
}

func validateDefinitionMaterial(raw []byte, input WorkflowInputDocument) error {
	var definition domain.WorkflowDefinition
	if err := strictRetainedDecode("definition", raw, MaximumDefinitionBytes, &definition); err != nil {
		return err
	}
	if err := definition.Validate(); err != nil {
		return invalid("materials.definition", "established definition validation failed: %v", err)
	}
	binding := input.Definition
	if definition.ID != binding.DefinitionID || int64(definition.Version) != binding.DefinitionVersion ||
		normalizeEstablishedHash(definition.Hash) != binding.DefinitionHash ||
		definition.ExecutionProfile.Version != binding.ExecutionProfileVersion ||
		normalizeEstablishedHash(definition.ExecutionProfile.Hash) != binding.ExecutionProfileHash {
		return invalid("materials.definition", "identity, version, semantic hash, or execution profile disagrees")
	}
	nodes := map[string]domain.NodeDefinition{}
	for _, node := range definition.Nodes {
		nodes[node.ID] = node
	}
	gate, exists := nodes[input.Gate.DefinitionNodeID]
	if !exists || gate.Type != domain.NodeExternalQualificationGate || gate.ExternalQualificationGate == nil ||
		!gate.ExternalQualificationGate.IsExact() {
		return invalid("materials.definition", "does not contain the exact frozen external-qualification gate node")
	}
	if err := validateDefinitionEdgeClosure(definition, input); err != nil {
		return err
	}
	for _, predecessor := range input.Predecessors {
		node, exists := nodes[predecessor.SourceDefinitionNodeID]
		if !exists || string(node.Type) != predecessor.SourceNodeType {
			return invalid("materials.definition", "predecessor definition-node type disagrees with definition")
		}
	}
	return nil
}

func validateDefinitionEdgeClosure(definition domain.WorkflowDefinition, input WorkflowInputDocument) error {
	incoming := map[string]domain.WorkflowEdge{}
	for _, edge := range definition.Edges {
		if edge.To == input.Gate.DefinitionNodeID {
			incoming[edge.ID] = edge
		}
	}
	if len(incoming) != len(input.Predecessors) {
		return invalid("materials.definition", "external gate incoming edge set does not equal the NodeInput predecessor set")
	}
	for index, predecessor := range input.Predecessors {
		edge, exists := incoming[predecessor.EdgeID]
		if !exists || edge.From != predecessor.SourceDefinitionNodeID || edge.To != input.Gate.DefinitionNodeID {
			return invalid("materials.definition", "predecessor %d does not bind one exact incoming definition edge", index)
		}
		fromPort, toPort := edge.FromPort, edge.ToPort
		if fromPort == "" {
			fromPort = "default"
		}
		if toPort == "" {
			toPort = "default"
		}
		mappingKind := MappingKindObjectMap
		mappingBytes, err := CanonicalJSON(edge.Mapping)
		if len(edge.Mapping) == 0 {
			mappingKind = MappingKindIdentity
			mappingBytes = []byte(`{}`)
		}
		if err != nil || fromPort != predecessor.SourcePort || toPort != predecessor.TargetPort ||
			mappingKind != predecessor.MappingKind || RawSHA256(mappingBytes) != predecessor.MappingHash {
			return invalid("materials.definition", "predecessor %d port or mapping differs from its definition edge", index)
		}
	}
	return nil
}

func validateNodeInputMaterial(raw []byte, input WorkflowInputDocument, buildManifestRaw []byte) ([]domain.NodeInputBinding, error) {
	selectedBuild, err := validateBuildManifestMaterial(buildManifestRaw, input)
	if err != nil {
		return nil, err
	}
	var wire nodeInputEnvelopeWire
	if err := strictRetainedDecode("nodeInput", raw, MaximumNodeInputBytes, &wire); err != nil {
		return nil, err
	}
	rebuilt, err := domain.NewNodeInputEnvelope(wire.Bindings)
	if err != nil || normalizeEstablishedHash(wire.Hash) != normalizeEstablishedHash(rebuilt.Hash()) ||
		normalizeEstablishedHash(rebuilt.Hash()) != input.NodeInput.SemanticHash || len(wire.Bindings) != len(input.Predecessors) ||
		!bytes.Equal(raw, rebuilt.Canonical()) {
		return nil, invalid("materials.nodeInput", "established semantic hash or binding count disagrees")
	}
	var generic map[string]any
	if err := decodeRetainedInto(raw, &generic); err != nil {
		return nil, invalid("materials.nodeInput", "%v", err)
	}
	genericBindings, ok := generic["bindings"].([]any)
	if !ok || len(genericBindings) != len(wire.Bindings) {
		return nil, invalid("materials.nodeInput", "binding wire is not an exact array")
	}
	byKey := map[string]int{}
	for index, predecessor := range input.Predecessors {
		key := predecessor.EdgeID + "\x00" + predecessor.SourceNodeKey + "\x00" + predecessor.SourcePort + "\x00" + predecessor.TargetPort
		byKey[key] = index
	}
	seen := make([]bool, len(input.Predecessors))
	outputRevisionBySourceNode := map[string]string{}
	for rawIndex, binding := range wire.Bindings {
		if binding.Source.RunID != input.Run.ID {
			return nil, invalid("materials.nodeInput", "binding %d source run disagrees with workflow run", rawIndex)
		}
		key := binding.EdgeID + "\x00" + binding.Source.NodeKey + "\x00" + binding.FromPort + "\x00" + binding.ToPort
		index, exists := byKey[key]
		if !exists || seen[index] {
			return nil, invalid("materials.nodeInput", "binding %d has no unique locked predecessor", rawIndex)
		}
		if !validUUIDv4(binding.Source.OutputRevisionID) {
			return nil, invalid("materials.nodeInput", "binding %d output revision id is absent or invalid", rawIndex)
		}
		sourceNodeRunID := input.Predecessors[index].SourceNodeRunID
		if old, exists := outputRevisionBySourceNode[sourceNodeRunID]; exists && old != binding.Source.OutputRevisionID {
			return nil, invalid("materials.nodeInput", "sourceNodeRunId %s carries conflicting output revision ids", sourceNodeRunID)
		}
		outputRevisionBySourceNode[sourceNodeRunID] = binding.Source.OutputRevisionID
		outputFound := false
		for _, reference := range binding.Source.ArtifactRevisions {
			if reference.RevisionID == binding.Source.OutputRevisionID {
				outputFound = true
				break
			}
		}
		if !outputFound || input.Predecessors[index].SourceNodeType == string(domain.NodeQualityGate) &&
			binding.Source.OutputRevisionID != input.Target.TargetRevisionID {
			return nil, invalid("materials.nodeInput", "binding %d output revision closure disagrees", rawIndex)
		}
		if input.Predecessors[index].SourceNodeType == string(domain.NodeQualityGate) {
			if input.Predecessors[index].MappingKind != MappingKindIdentity || !bytes.Equal(binding.Output, binding.Value) {
				return nil, invalid("materials.nodeInput", "quality binding %d must carry one identical typed output/value", rawIndex)
			}
			if err := validateQualityGateResult(binding.Output, input, selectedBuild); err != nil {
				return nil, invalid("materials.nodeInput", "quality binding %d: %v", rawIndex, err)
			}
		}
		seen[index] = true
		canonicalBinding, err := CanonicalJSON(genericBindings[rawIndex])
		if err != nil {
			return nil, err
		}
		if err := validateRawPredecessorProjection(binding, input.Predecessors[index], rawIndex, canonicalBinding); err != nil {
			return nil, err
		}
	}
	return wire.Bindings, nil
}

func validateQualityGateResult(raw json.RawMessage, input WorkflowInputDocument, selectedBuild core.WorkbenchBundle) error {
	var result qualityGateResultWire
	if err := strictRetainedDecode("qualityResult", raw, MaximumNodeInputBytes, &result); err != nil {
		return err
	}
	workspaceRevision, exists := workspaceTargetRevision(input.Revisions)
	if !exists || !result.Passed || result.QualityRunID != input.QualityResult.QualityRunID || result.WorkspaceRevision == nil ||
		result.BuildManifest == nil || len(result.Findings) == 0 || result.WorkspaceRevision.AnchorID != "" ||
		result.WorkspaceRevision.ArtifactID != workspaceRevision.ArtifactID ||
		result.WorkspaceRevision.RevisionID != input.Target.TargetRevisionID ||
		normalizeEstablishedHash(result.WorkspaceRevision.ContentHash) != input.Target.TargetRevisionContentHash {
		return invalid("qualityResult", "passed/run/workspace/build result does not equal the frozen quality authority")
	}
	if err := validateQualityFindings(result.Findings, result.QualityRunID, *result.WorkspaceRevision); err != nil {
		return err
	}
	manifest := *result.BuildManifest
	if err := validateQualityWorkflowBuildManifest(manifest, input, selectedBuild); err != nil {
		return err
	}
	return nil
}

func validateQualityFindings(raw json.RawMessage, qualityRunID string, workspace domain.ArtifactRef) error {
	var findings qualityFindingsWire
	if err := strictRetainedDecode("qualityFindings", raw, MaximumNodeInputBytes, &findings); err != nil {
		return err
	}
	checks, err := decodeRetainedGeneric("qualityFindings.checks", findings.Checks, MaximumNodeInputBytes)
	if err != nil {
		return err
	}
	diagnostics, err := decodeRetainedGeneric("qualityFindings.diagnostics", findings.Diagnostics, MaximumNodeInputBytes)
	if err != nil {
		return err
	}
	if _, ok := checks.([]any); !ok {
		return invalid("qualityFindings.checks", "must be an explicit array")
	}
	if _, ok := diagnostics.([]any); !ok {
		return invalid("qualityFindings.diagnostics", "must be an explicit array")
	}
	if findings.QualityRunID != qualityRunID || !validUUIDv4(findings.ReportArtifactID) ||
		!validUUIDv4(findings.ReportRevisionID) || findings.Score < 0 || findings.Score > 100 ||
		findings.WorkspaceRevision.AnchorID != nil || findings.WorkspaceRevision.ArtifactID != workspace.ArtifactID ||
		findings.WorkspaceRevision.RevisionID != workspace.RevisionID ||
		normalizeEstablishedHash(findings.WorkspaceRevision.ContentHash) != normalizeEstablishedHash(workspace.ContentHash) {
		return invalid("qualityFindings", "does not repeat the exact quality run and Workspace result")
	}
	return nil
}

func validateQualityWorkflowBuildManifest(manifest qualityWorkflowBuildManifest, input WorkflowInputDocument, selectedBuild core.WorkbenchBundle) error {
	if manifest.SchemaVersion < 1 || manifest.SchemaVersion > MaximumJavaScriptSafeInt || manifest.ProjectID != input.Project.ID ||
		manifest.RunID != input.Run.ID || !validUUIDv4(manifest.ManifestGroupKey) || manifest.SliceIDs == nil ||
		manifest.BundleIDs == nil || len(manifest.SliceIDs) < 1 || len(manifest.SliceIDs) > MaximumManifests ||
		len(manifest.SliceIDs) != len(manifest.BundleIDs) || manifest.Sources == nil || len(manifest.Sources) < 1 ||
		len(manifest.Sources) > MaximumRevisions || len(manifest.Constraints) == 0 || manifest.CreatedAt.IsZero() ||
		manifest.CreatedAt.Location() != time.UTC ||
		normalizeEstablishedHash(manifest.Hash) == "" || selectedBuild.ID != input.QualityResult.BuildManifestID ||
		selectedBuild.ID != input.Build.BuildManifest.ID || selectedBuild.ManifestGroupKey == nil ||
		manifest.ManifestGroupKey != *selectedBuild.ManifestGroupKey ||
		!validUUIDv4(selectedBuild.RootBuildManifestID) || manifest.BundleIDs[len(manifest.BundleIDs)-1] != selectedBuild.RootBuildManifestID ||
		selectedBuild.DeliverySliceID == nil || manifest.SliceIDs[len(manifest.SliceIDs)-1] != *selectedBuild.DeliverySliceID ||
		normalizeEstablishedHash(selectedBuild.ManifestHash) != input.QualityResult.BuildManifestHash {
		return invalid("qualityResult.buildManifest", "does not select the exact application BuildManifest lineage")
	}
	seenSlices, seenBundles := map[string]struct{}{}, map[string]struct{}{}
	for index := range manifest.SliceIDs {
		if !validUUIDv4(manifest.SliceIDs[index]) || !validUUIDv4(manifest.BundleIDs[index]) {
			return invalid("qualityResult.buildManifest", "slice and bundle identities must be canonical UUIDv4 values")
		}
		if _, duplicate := seenSlices[manifest.SliceIDs[index]]; duplicate {
			return invalid("qualityResult.buildManifest", "slice identities must be unique")
		}
		if _, duplicate := seenBundles[manifest.BundleIDs[index]]; duplicate {
			return invalid("qualityResult.buildManifest", "bundle identities must be unique")
		}
		seenSlices[manifest.SliceIDs[index]] = struct{}{}
		seenBundles[manifest.BundleIDs[index]] = struct{}{}
	}
	for _, source := range manifest.Sources {
		if err := source.Validate(); err != nil {
			return invalid("qualityResult.buildManifest", "source: %v", err)
		}
	}
	if err := validateQualityBuildManifestSourceCoverage(manifest.Sources, selectedBuild); err != nil {
		return err
	}
	if _, err := domain.CanonicalJSON(manifest.Constraints); err != nil {
		return invalid("qualityResult.buildManifest", "constraints: %v", err)
	}
	claimed := normalizeEstablishedHash(manifest.Hash)
	manifest.Hash = ""
	hash, err := domain.CanonicalHash(manifest)
	if err != nil || normalizeEstablishedHash(hash) != claimed {
		return invalid("qualityResult.buildManifest", "semantic hash is invalid")
	}
	return nil
}

func validateQualityBuildManifestSourceCoverage(sources []domain.ArtifactRef, bundle core.WorkbenchBundle) error {
	available := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		key := artifactRefIdentity(source)
		if _, duplicate := available[key]; duplicate {
			return invalid("qualityResult.buildManifest", "sources must contain unique exact artifact revisions")
		}
		available[key] = struct{}{}
	}
	required := []core.VersionRef{bundle.BlueprintRevision, bundle.PageSpecRevision, bundle.PrototypeRevision}
	required = append(required, bundle.RequirementRevisions...)
	required = append(required, bundle.ContractRevisions...)
	required = append(required, bundle.DesignSystemRevisions...)
	for _, contextRevision := range bundle.ContextRevisions {
		required = append(required, contextRevision.Revision)
	}
	if bundle.WorkflowContext != nil {
		if bundle.WorkflowContext.InputManifest.BaseRevision != nil {
			base := bundle.WorkflowContext.InputManifest.BaseRevision
			required = append(required, core.VersionRef{
				ArtifactID: base.ArtifactID, RevisionID: base.RevisionID, ContentHash: base.ContentHash,
				AnchorID: optionalAnchor(base.AnchorID),
			})
		}
		for _, source := range bundle.WorkflowContext.InputManifest.Sources {
			required = append(required, core.VersionRef{
				ArtifactID: source.Ref.ArtifactID, RevisionID: source.Ref.RevisionID, ContentHash: source.Ref.ContentHash,
				AnchorID: optionalAnchor(source.Ref.AnchorID),
			})
		}
	}
	for _, reference := range required {
		converted := artifactRefFromCore(reference)
		if err := converted.Validate(); err != nil {
			return invalid("qualityResult.buildManifest", "selected Workbench source is invalid: %v", err)
		}
		if _, exists := available[artifactRefIdentity(converted)]; !exists {
			return invalid("qualityResult.buildManifest", "sources omit a frozen Workbench bundle revision")
		}
	}
	return nil
}

func artifactRefFromCore(reference core.VersionRef) domain.ArtifactRef {
	anchor := ""
	if reference.AnchorID != nil {
		anchor = *reference.AnchorID
	}
	return domain.ArtifactRef{
		AnchorID: anchor, ArtifactID: reference.ArtifactID, ContentHash: reference.ContentHash, RevisionID: reference.RevisionID,
	}
}

func optionalAnchor(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func artifactRefIdentity(reference domain.ArtifactRef) string {
	return reference.ArtifactID + "\x00" + reference.RevisionID + "\x00" + normalizeEstablishedHash(reference.ContentHash) + "\x00" + reference.AnchorID
}

func workspaceTargetRevision(revisions []RevisionBinding) (RevisionBinding, bool) {
	for _, revision := range revisions {
		if revision.Purpose == RevisionPurposeWorkspaceTarget {
			return revision, true
		}
	}
	return RevisionBinding{}, false
}

func validateRawPredecessorProjection(raw domain.NodeInputBinding, frozen PredecessorBinding, ordinal int, canonicalBinding []byte) error {
	mappingBytes, err := CanonicalJSON(raw.Mapping)
	if err != nil {
		return err
	}
	mappingKind := MappingKindObjectMap
	if len(raw.Mapping) == 0 {
		mappingKind = MappingKindIdentity
		mappingBytes = []byte("{}")
	}
	slice := SliceIdentity{Kind: SliceKindRoot}
	if raw.Source.SliceID != "" {
		slice = SliceIdentity{ID: raw.Source.SliceID, Kind: SliceKindDelivery}
	}
	expectedArtifacts := make([]ArtifactRevisionReference, len(raw.Source.ArtifactRevisions))
	for index, value := range raw.Source.ArtifactRevisions {
		expectedArtifacts[index] = artifactReferenceFromDomain(value)
	}
	expectedMaterialized := make([]ArtifactRevisionReference, len(raw.Source.MaterializedArtifactRevisions))
	for index, value := range raw.Source.MaterializedArtifactRevisions {
		expectedMaterialized[index] = artifactReferenceFromDomain(value)
	}
	expectedSlices := make([]DeliverySliceReference, len(raw.Source.DeliverySliceRefs))
	for index, value := range raw.Source.DeliverySliceRefs {
		expectedSlices[index] = deliverySliceFromDomain(value)
	}
	expectedPins := make([]ProposalLineagePin, len(raw.Source.ProposalPins))
	for index, value := range raw.Source.ProposalPins {
		expectedPins[index] = proposalPinFromDomain(value)
	}
	var manifest *ManifestReference
	if raw.Source.InputManifest != nil {
		manifest = &ManifestReference{Hash: normalizeEstablishedHash(raw.Source.InputManifest.Hash), ID: raw.Source.InputManifest.ID}
	}
	var proposal *ProposalReference
	if raw.Source.OutputProposal != nil {
		proposal = &ProposalReference{ID: raw.Source.OutputProposal.ID, PayloadHash: normalizeEstablishedHash(raw.Source.OutputProposal.PayloadHash)}
	}
	if frozen.ArtifactRevisions == nil || frozen.MaterializedArtifactRevisions == nil || frozen.DeliverySliceRefs == nil || frozen.ProposalPins == nil ||
		!reflect.DeepEqual(frozen.ArtifactRevisions, expectedArtifacts) ||
		!reflect.DeepEqual(frozen.MaterializedArtifactRevisions, expectedMaterialized) ||
		!reflect.DeepEqual(frozen.DeliverySliceRefs, expectedSlices) || !reflect.DeepEqual(frozen.ProposalPins, expectedPins) ||
		!reflect.DeepEqual(frozen.InputManifest, manifest) || !reflect.DeepEqual(frozen.OutputProposal, proposal) ||
		frozen.BindingRawBytesHash != RawSHA256(canonicalBinding) || frozen.MappingHash != RawSHA256(mappingBytes) ||
		frozen.MappingKind != mappingKind || frozen.MappingOrdinal != int64(ordinal) || frozen.EdgeID != raw.EdgeID ||
		frozen.OutputHash != normalizeEstablishedHash(raw.OutputHash) || frozen.ValueHash != normalizeEstablishedHash(raw.ValueHash) ||
		frozen.SourceDefinitionNodeID != raw.Source.DefinitionNodeID || frozen.SourceNodeKey != raw.Source.NodeKey ||
		frozen.SourcePort != raw.FromPort || frozen.TargetPort != raw.ToPort || frozen.SourceSliceIdentity != slice {
		return invalid("materials.nodeInput", "binding %d does not equal frozen predecessor projection", ordinal)
	}
	return nil
}

func validateManifestMaterials(candidate Candidate, nodeBindings []domain.NodeInputBinding, add func(string, []byte, int) error) error {
	if len(candidate.Materials.InputManifests) != len(candidate.Document.InputManifests) ||
		len(candidate.Input.InputManifests) != len(candidate.Document.InputManifests) {
		return invalid("materials.inputManifests", "does not equal candidate/output set")
	}
	requiredPredecessors := map[string]struct{}{}
	for _, binding := range nodeBindings {
		if binding.Source.InputManifest != nil {
			requiredPredecessors[binding.Source.InputManifest.ID] = struct{}{}
		}
		for _, pin := range binding.Source.ProposalPins {
			requiredPredecessors[pin.Manifest.ID] = struct{}{}
		}
	}
	materialByKey := map[string]InputManifestMaterial{}
	for _, material := range candidate.Materials.InputManifests {
		key := material.Role + "\x00" + material.ManifestID
		if _, duplicate := materialByKey[key]; duplicate {
			return invalid("materials.inputManifests", "duplicate role/manifest identity")
		}
		materialByKey[key] = material
	}
	boundByKey := map[string]InputManifestBinding{}
	for _, binding := range candidate.Input.InputManifests {
		boundByKey[binding.Role+"\x00"+binding.ID] = binding
	}
	seenRequired := map[string]struct{}{}
	for _, item := range candidate.Document.InputManifests {
		key := item.Role + "\x00" + item.ManifestID
		material, exists := materialByKey[key]
		binding, bound := boundByKey[key]
		decoded, err := hex.DecodeString(item.RawBytesHex)
		if !exists || !bound || err != nil || !bytes.Equal(decoded, material.Bytes) {
			return invalid("materials.inputManifests", "candidate raw hex/material/output mismatch for %s", key)
		}
		if err := add("inputManifests", material.Bytes, MaximumManifestBytes); err != nil {
			return err
		}
		if RawSHA256(material.Bytes) != binding.RawBytesHash || binding.ContentHash != binding.RawBytesHash ||
			int64(len(material.Bytes)) != binding.RawBytesSize {
			return invalid("materials.inputManifests", "raw content digest/size mismatch for %s", key)
		}
		var manifest domain.InputManifest
		if err := strictRetainedDecode("inputManifest", material.Bytes, MaximumManifestBytes, &manifest); err != nil {
			return err
		}
		if err := manifest.Validate(); err != nil || manifest.ID != binding.ID || manifest.ProjectID != binding.ProjectID ||
			manifest.JobType != binding.Kind || normalizeEstablishedHash(manifest.Hash) != binding.ManifestHash {
			return invalid("materials.inputManifests", "established manifest semantics disagree for %s", key)
		}
		if item.Role == ManifestRolePredecessor {
			if _, required := requiredPredecessors[item.ManifestID]; !required {
				return invalid("materials.inputManifests", "extra predecessor manifest %s", item.ManifestID)
			}
			seenRequired[item.ManifestID] = struct{}{}
		}
	}
	if len(seenRequired) != len(requiredPredecessors) {
		return invalid("materials.inputManifests", "required predecessor manifest closure is incomplete")
	}
	return nil
}

func validateRevisionMaterials(candidate Candidate, add func(string, []byte, int) error) error {
	if len(candidate.Materials.Revisions) != len(candidate.Document.Revisions) || len(candidate.Input.Revisions) != len(candidate.Document.Revisions) {
		return invalid("materials.revisions", "does not equal candidate/output set")
	}
	materialByKey := map[string]RevisionMaterial{}
	for _, material := range candidate.Materials.Revisions {
		key := material.Purpose + "\x00" + material.RevisionID
		if _, duplicate := materialByKey[key]; duplicate {
			return invalid("materials.revisions", "duplicate purpose/revision identity")
		}
		materialByKey[key] = material
	}
	boundByKey := map[string]RevisionBinding{}
	for _, binding := range candidate.Input.Revisions {
		boundByKey[binding.Purpose+"\x00"+binding.RevisionID] = binding
	}
	for _, item := range candidate.Document.Revisions {
		key := item.Purpose + "\x00" + item.RevisionID
		material, exists := materialByKey[key]
		binding, bound := boundByKey[key]
		decoded, err := hex.DecodeString(item.RawBytesHex)
		if !exists || !bound || err != nil || !bytes.Equal(decoded, material.Bytes) || item.CurrencyPolicy != binding.CurrencyPolicy {
			return invalid("materials.revisions", "candidate raw hex/material/output mismatch for %s", key)
		}
		if err := add("revisions", material.Bytes, MaximumRevisionBytes); err != nil {
			return err
		}
		if _, err := decodeRetainedGeneric("revision", material.Bytes, MaximumRevisionBytes); err != nil {
			return err
		}
		if RawSHA256(material.Bytes) != binding.RawBytesHash || binding.ContentHash != binding.RawBytesHash || int64(len(material.Bytes)) != binding.ByteSize {
			return invalid("materials.revisions", "raw content digest/size mismatch for %s", key)
		}
	}
	return nil
}

func validateBuildMaterials(manifestRaw, contractRaw []byte, input WorkflowInputDocument) error {
	if _, err := validateBuildManifestMaterial(manifestRaw, input); err != nil {
		return err
	}
	var contract constructor.ContractContent
	if err := strictRetainedDecode("buildContract", contractRaw, MaximumBuildContractBytes, &contract); err != nil {
		return err
	}
	contractHash, err := domain.CanonicalHash(contract)
	if err != nil || normalizeEstablishedHash(contractHash) != input.Build.BuildContract.ContractHash ||
		contract.SchemaVersion != constructor.BuildContractSchemaVersion || contract.Compiler.Version != constructor.CompilerVersion ||
		normalizeEstablishedHash(contract.Compiler.Hash) == "" ||
		contract.ProjectID != input.Project.ID || contract.Status != ReadyBuildContractStatus ||
		contract.BuildManifest.ID != input.Build.BuildManifest.ID ||
		normalizeEstablishedHash(contract.BuildManifest.ContentHash) != input.Build.BuildManifest.ManifestHash {
		return invalid("materials.buildContract", "established contract semantics or build closure disagree")
	}
	if contract.SourceRevisions == nil || contract.TemplateReleaseRefs == nil || contract.Routes == nil || contract.States == nil ||
		contract.ContractBindings == nil || contract.AcceptanceCriteria == nil || contract.Oracles == nil || contract.Obligations == nil ||
		contract.Waivers == nil || contract.Gaps == nil || contract.Conflicts == nil || contract.ForbiddenClaims == nil ||
		len(contract.Obligations) < 1 || len(contract.Gaps) != 0 || len(contract.Conflicts) != 0 {
		return invalid("materials.buildContract", "ready contract collection/obligation closure is invalid")
	}
	for _, obligation := range contract.Obligations {
		if obligation.Level != "must" || obligation.Status != "ready" || len(obligation.OracleIDs) == 0 {
			return invalid("materials.buildContract", "ready contract contains an unready must obligation")
		}
	}
	sources := map[string]constructor.ExactRevisionRef{}
	for _, source := range contract.SourceRevisions {
		key := source.Purpose + "\x00" + source.RevisionID
		if _, duplicate := sources[key]; duplicate {
			return invalid("materials.buildContract", "duplicate source revision")
		}
		sources[key] = source
	}
	expected := 0
	for _, revision := range input.Revisions {
		if revision.Purpose == RevisionPurposeWorkspaceTarget {
			continue
		}
		expected++
		source, exists := sources[revision.Purpose+"\x00"+revision.RevisionID]
		if !exists || source.ArtifactID != revision.ArtifactID || source.Kind != revision.ArtifactKind ||
			normalizeEstablishedHash(source.ContentHash) != revision.ContentHash || source.ApprovalStatus != ApprovedRevisionStatus ||
			source.Required != revision.SourceRequiredAtFreeze {
			return invalid("materials.buildContract", "source revision closure disagrees with frozen revision")
		}
	}
	if len(sources) != expected {
		return invalid("materials.buildContract", "source revision set is not exact")
	}
	return nil
}

func validateBuildManifestMaterial(manifestRaw []byte, input WorkflowInputDocument) (core.WorkbenchBundle, error) {
	var bundle core.WorkbenchBundle
	if err := strictRetainedDecode("buildManifest", manifestRaw, MaximumBuildManifestBytes, &bundle); err != nil {
		return core.WorkbenchBundle{}, err
	}
	claimed := normalizeEstablishedHash(bundle.ManifestHash)
	semantic := bundle
	semantic.ManifestHash = ""
	hash, err := domain.CanonicalHash(semantic)
	if err != nil || claimed == "" || normalizeEstablishedHash(hash) != claimed || claimed != input.Build.BuildManifest.ManifestHash ||
		bundle.ID != input.Build.BuildManifest.ID || bundle.ProjectID != input.Project.ID ||
		bundle.WorkflowRunID == nil || *bundle.WorkflowRunID != input.Run.ID {
		return core.WorkbenchBundle{}, invalid("materials.buildManifest", "established manifest identity or semantics disagree")
	}
	if bundle.RequirementRevisions == nil || bundle.ContractRevisions == nil || bundle.DesignSystemRevisions == nil ||
		bundle.ContextRevisions == nil || bundle.RenderedFrames == nil || bundle.Assumptions == nil || bundle.Waivers == nil ||
		bundle.RootBuildManifestID == "" {
		return core.WorkbenchBundle{}, invalid("materials.buildManifest", "required manifest collections or root identity are absent")
	}
	if bundle.CurrentWorkspaceRevision != nil && (!validUUIDv4(bundle.CurrentWorkspaceRevision.ArtifactID) ||
		!validUUIDv4(bundle.CurrentWorkspaceRevision.RevisionID) ||
		normalizeEstablishedHash(bundle.CurrentWorkspaceRevision.ContentHash) == "" || bundle.CurrentWorkspaceRevision.AnchorID != nil) {
		return core.WorkbenchBundle{}, invalid("materials.buildManifest", "optional base Workspace revision is invalid")
	}
	return bundle, nil
}

func validateReceiptMaterials(materials []ReviewReceiptMaterial, input WorkflowInputDocument, add func(string, []byte, int) error) error {
	if len(materials) != len(input.ReviewReceipts) {
		return invalid("materials.reviewReceipts", "does not equal required receipt set")
	}
	materialByRequest := map[string][]byte{}
	for _, material := range materials {
		if _, duplicate := materialByRequest[material.ReviewRequestID]; duplicate {
			return invalid("materials.reviewReceipts", "duplicate review request identity")
		}
		materialByRequest[material.ReviewRequestID] = material.Bytes
	}
	revisionByKey := map[string]RevisionBinding{}
	for _, revision := range input.Revisions {
		revisionByKey[revision.Purpose+"\x00"+revision.RevisionID] = revision
	}
	for index, binding := range input.ReviewReceipts {
		raw, exists := materialByRequest[binding.ReviewRequestID]
		if !exists || len(raw) < 1 || len(raw) > MaximumReviewReceiptBytes || RawSHA256(raw) != binding.ReceiptRawBytesHash ||
			int64(len(raw)) != binding.ReceiptRawBytesSize {
			return invalid("materials.reviewReceipts", "member %d raw bytes disagree", index)
		}
		if err := add("reviewReceipts", raw, MaximumReviewReceiptBytes); err != nil {
			return err
		}
		decoded, err := canonicalreviewreceipt.Decode(raw, binding.ReceiptHash)
		if err != nil {
			return invalid("materials.reviewReceipts", "member %d: %v", index, err)
		}
		receipt := decoded
		revision, exists := revisionByKey[binding.Purpose+"\x00"+binding.RevisionID]
		if !exists || receipt.ReviewRequest.ID != binding.ReviewRequestID || receipt.ReviewRequest.ProjectID != binding.ProjectID ||
			receipt.ReviewRequest.ArtifactID != binding.ArtifactID || receipt.ReviewRequest.RevisionID != binding.RevisionID ||
			receipt.ReviewRequest.ContentHash != binding.RevisionContentHash || receipt.Revision.ID != revision.RevisionID ||
			receipt.Revision.ArtifactID != revision.ArtifactID || receipt.Revision.ContentHash != revision.ContentHash ||
			int64(receipt.Revision.ArtifactSchemaVersion) != revision.SchemaVersion || receipt.Revision.ByteSize != revision.ByteSize ||
			receipt.Revision.ContentStore != revision.ContentStore || receipt.Revision.ContentRef != revision.ContentRef ||
			!equalOptional(receipt.Revision.SourceManifestID, revision.SourceManifestID) ||
			!equalOptional(receipt.Revision.ProposalID, revision.ProposalID) ||
			!equalOptional(receipt.Revision.ImplementationProposalID, revision.ImplementationProposalID) ||
			receipt.Revision.WorkflowStatus != revision.WorkflowStatusAtFreeze || receipt.Approval.ArtifactKind != revision.ArtifactKind ||
			receipt.Approval.ArtifactID != revision.ArtifactID || receipt.Approval.RevisionID != revision.RevisionID ||
			receipt.Approval.RevisionContentHash != revision.ContentHash || receipt.Approval.ProjectID != input.Project.ID ||
			receipt.Governance.Mode != input.Project.GovernanceMode ||
			receipt.Policy.Value.GovernanceMode != input.Project.GovernanceMode {
			return invalid("materials.reviewReceipts", "member %d immutable receipt facts disagree with frozen revision/project", index)
		}
	}
	return nil
}

func validateGlobalIdentities(candidate Candidate) error {
	roles := map[string]string{}
	register := func(value, role string) error {
		if value == "" {
			return nil
		}
		if old, exists := roles[value]; exists && old != role {
			return invalid("identities", "%s is reused as both %s and %s", value, old, role)
		}
		roles[value] = role
		return nil
	}
	input := candidate.Input
	values := []struct{ value, role string }{
		{candidate.Request.AuthorityID, "authority"}, {candidate.Request.OperationID, "freeze-operation"},
		{input.Gate.ActivationEventID, "activation-event"}, {input.Project.ID, "project"},
		{input.Run.ID, "workflow-run"}, {input.Gate.NodeRunID, "node-run"}, {input.Run.StartedBy, "user"},
		{input.Definition.DefinitionID, "workflow-definition"}, {input.Definition.DefinitionVersionID, "workflow-definition-version"},
		{input.Build.BuildManifest.ID, "build-manifest"}, {input.Build.BuildContract.ID, "build-contract"},
		{input.QualificationPolicy.AuthorityID, "qualification-policy-authority"},
	}
	for _, value := range values {
		if err := register(value.value, value.role); err != nil {
			return err
		}
	}
	if err := register(input.QualityResult.QualityRunID, "quality-run"); err != nil {
		return err
	}
	for _, predecessor := range input.Predecessors {
		if err := register(predecessor.SourceNodeRunID, "node-run"); err != nil {
			return err
		}
		if predecessor.InputManifest != nil {
			if err := register(predecessor.InputManifest.ID, "input-manifest"); err != nil {
				return err
			}
		}
		if predecessor.OutputProposal != nil {
			if err := register(predecessor.OutputProposal.ID, "output-proposal"); err != nil {
				return err
			}
		}
		if predecessor.SourceSliceIdentity.Kind == SliceKindDelivery {
			if err := register(predecessor.SourceSliceIdentity.ID, "delivery-slice"); err != nil {
				return err
			}
		}
		for _, slice := range predecessor.DeliverySliceRefs {
			if err := register(slice.ID, "delivery-slice"); err != nil {
				return err
			}
		}
		for _, pin := range predecessor.ProposalPins {
			if err := register(pin.Manifest.ID, "input-manifest"); err != nil {
				return err
			}
			if err := register(pin.Proposal.ID, "output-proposal"); err != nil {
				return err
			}
		}
	}
	for _, manifest := range input.InputManifests {
		if err := register(manifest.ID, "input-manifest"); err != nil {
			return err
		}
	}
	for _, revision := range input.Revisions {
		if err := register(revision.ArtifactID, "artifact"); err != nil {
			return err
		}
		if err := register(revision.RevisionID, "artifact-revision"); err != nil {
			return err
		}
		for _, optional := range []*string{revision.SourceManifestID, revision.ProposalID, revision.ImplementationProposalID} {
			if optional != nil {
				role := "output-proposal"
				if optional == revision.SourceManifestID {
					role = "input-manifest"
				} else if optional == revision.ImplementationProposalID {
					role = "implementation-proposal"
				}
				if err := register(*optional, role); err != nil {
					return err
				}
			}
		}
	}
	for _, receipt := range input.ReviewReceipts {
		if err := register(receipt.ReviewRequestID, "review-request"); err != nil {
			return err
		}
	}
	return nil
}

func artifactReferenceFromDomain(value domain.ArtifactRef) ArtifactRevisionReference {
	return ArtifactRevisionReference{AnchorID: value.AnchorID, ArtifactID: value.ArtifactID, ContentHash: normalizeEstablishedHash(value.ContentHash), RevisionID: value.RevisionID}
}

func deliverySliceFromDomain(value domain.WorkflowSliceRef) DeliverySliceReference {
	result := DeliverySliceReference{FanOutNodeID: value.FanOutNodeID, ID: value.ID, Key: value.Key}
	if value.Blueprint != nil {
		converted := artifactReferenceFromDomain(*value.Blueprint)
		result.Blueprint = &converted
	}
	if value.PageSpec != nil {
		converted := artifactReferenceFromDomain(*value.PageSpec)
		result.PageSpec = &converted
	}
	if value.Prototype != nil {
		converted := artifactReferenceFromDomain(*value.Prototype)
		result.Prototype = &converted
	}
	return result
}

func proposalPinFromDomain(value domain.ProposalLineagePin) ProposalLineagePin {
	return ProposalLineagePin{
		Manifest:                 ManifestReference{Hash: normalizeEstablishedHash(value.Manifest.Hash), ID: value.Manifest.ID},
		ProducerDefinitionNodeID: value.ProducerDefinitionNodeID, ProducerNodeKey: value.ProducerNodeKey,
		Proposal: ProposalReference{ID: value.Proposal.ID, PayloadHash: normalizeEstablishedHash(value.Proposal.PayloadHash)},
	}
}

func strictRetainedDecode(name string, raw []byte, maximum int, destination any) error {
	if len(raw) < 1 || len(raw) > maximum || !utf8.Valid(raw) || bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		return invalid("materials."+name, "must be bounded BOM-free UTF-8 JSON")
	}
	if err := rejectDuplicateNames(raw); err != nil {
		return invalid("materials."+name, "%v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return invalid("materials."+name, "strict decode: %v", err)
	}
	if err := requireEOF(decoder); err != nil {
		return invalid("materials."+name, "%v", err)
	}
	return nil
}

func decodeRetainedGeneric(name string, raw []byte, maximum int) (any, error) {
	if len(raw) < 1 || len(raw) > maximum || !utf8.Valid(raw) || bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		return nil, invalid("materials."+name, "must be bounded BOM-free UTF-8 JSON")
	}
	if err := rejectDuplicateNames(raw); err != nil {
		return nil, invalid("materials."+name, "%v", err)
	}
	var value any
	if err := decodeRetainedInto(raw, &value); err != nil {
		return nil, invalid("materials."+name, "%v", err)
	}
	return value, nil
}

func decodeRetainedInto(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	return nil
}

func normalizeEstablishedHash(value string) string {
	if !legacyHash.MatchString(value) {
		return ""
	}
	return "sha256:" + strings.TrimPrefix(value, "sha256:")
}

func validManifestRole(value string) bool {
	switch value {
	case ManifestRoleRun, ManifestRolePredecessor, ManifestRoleNode, ManifestRoleQualification:
		return true
	default:
		return false
	}
}

func validSourceNodeType(value string) bool {
	switch domain.WorkflowNodeType(value) {
	case domain.NodeArtifactInput, domain.NodeAITransform, domain.NodeHumanEdit, domain.NodeReviewGate,
		domain.NodeCondition, domain.NodeFanOut, domain.NodeMerge, domain.NodeQualityGate,
		domain.NodeManifestCompiler, domain.NodeWorkbenchBuild:
		return true
	default:
		return false
	}
}

func validArtifactKind(value string) bool {
	switch value {
	case "project_brief", "product_requirements", "decision_record", "glossary_policy", "reference_source",
		"change_request", "requirement_baseline", "blueprint", "page_spec", "prototype", "prototype_flow",
		"fixture_bundle", "design_system", "token_set", "component_registry", "api_contract", "data_contract",
		"permission_contract", "ai_runtime_contract", "deployment_contract", "verification_contract", "workspace",
		"test_report", "quality_report":
		return true
	default:
		return false
	}
}

func validRevisionCurrencyPolicy(value string) bool {
	return value == CurrencyExactApproved || value == CurrencyLatestApprovedRequired
}

func validRevisionChangeSource(value string) bool {
	switch value {
	case "ai_proposal", "human", "import", "merge", "rollback", "system":
		return true
	default:
		return false
	}
}

func validRawHex(value string, maximumBytes int) bool {
	return len(value) >= 2 && len(value) <= maximumBytes*2 && len(value)%2 == 0 && hexPattern.MatchString(value)
}

func validDigest(value string) bool { return digestPattern.MatchString(value) }

func validUUIDv4(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.Version() == 4 && parsed.Variant() == uuid.RFC4122 && parsed.String() == value
}

func validOptionalUUIDv4(value *string) bool { return value == nil || validUUIDv4(*value) }

func validStableID(value string, maximum int) bool {
	return len(value) <= maximum && stableIDPattern.MatchString(value)
}

func validCanonicalString(value string, maximum int, allowEmpty bool) bool {
	if !allowEmpty && value == "" {
		return false
	}
	return len(value) <= maximum && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n\t")
}

func validContentRef(value string) bool {
	if !validCanonicalString(value, 65536, false) {
		return false
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) || strings.HasPrefix(lower, "file://") {
		return false
	}
	return len(value) < 3 || !((value[0] >= 'a' && value[0] <= 'z' || value[0] >= 'A' && value[0] <= 'Z') &&
		value[1] == ':' && (value[2] == '/' || value[2] == '\\'))
}

func validSizedBytes(value int64, maximum int) bool { return value >= 1 && value <= int64(maximum) }

func validCanonicalTime(value string) bool {
	parsed, err := time.Parse(canonicalTimeLayout, value)
	return err == nil && len(value) == len(canonicalTimeLayout) && strings.HasSuffix(value, "000Z") &&
		parsed.Year() >= 1678 && parsed.Year() < 2262 && parsed.UTC().Format(canonicalTimeLayout) == value
}

func equalOptional(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func cloneRevision(value RevisionBinding) RevisionBinding {
	clone := value
	clone.SourceManifestID = cloneStringPointer(value.SourceManifestID)
	clone.ProposalID = cloneStringPointer(value.ProposalID)
	clone.ImplementationProposalID = cloneStringPointer(value.ImplementationProposalID)
	return clone
}

func mustUUID(value string) uuid.UUID { return uuid.MustParse(value) }

func equalInput(left, right WorkflowInputDocument) bool { return reflect.DeepEqual(left, right) }
