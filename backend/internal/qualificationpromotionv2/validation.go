package qualificationpromotionv2

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationevidence"
	"github.com/worksflow/builder/backend/internal/qualificationinputauthority"
)

const canonicalTimeLayout = "2006-01-02T15:04:05.000Z"

var (
	digestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	gitCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	stableIDPattern  = regexp.MustCompile(`^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$`)
)

// ValidateCommand enforces the same five-ID contract expected by the SQL
// entrypoint and the shared identity reservation tables.
func ValidateCommand(command ConsumeCommand) error {
	identities := []uuid.UUID{
		command.OperationID,
		command.WorkflowInputAuthorityID,
		command.PlanAuthorityID,
		command.HandoffID,
		command.OutputRevisionID,
	}
	seen := make(map[uuid.UUID]struct{}, len(identities))
	for _, identity := range identities {
		if !validUUIDv4Value(identity) {
			return invalid("command", "all five identities must be canonical nonzero UUIDv4 values")
		}
		if _, duplicate := seen[identity]; duplicate {
			return invalid("command", "all five identities must be pairwise distinct")
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func validateRequest(request ConsumeRequest) error {
	if request.SchemaVersion != RequestSchemaV2 || !validUUIDv4(request.OperationID) ||
		!validUUIDv4(request.WorkflowInputAuthorityID) || !validUUIDv4(request.PlanAuthorityID) ||
		!validUUIDv4(request.HandoffID) || !validUUIDv4(request.OutputRevisionID) {
		return invalid("request", "schema or identity is invalid")
	}
	identities := []string{request.OperationID, request.WorkflowInputAuthorityID, request.PlanAuthorityID, request.HandoffID, request.OutputRevisionID}
	if !uniqueStrings(identities) {
		return invalid("request", "all five identities must be pairwise distinct")
	}
	return nil
}

func validateTarget(target PromotionTargetV2) error {
	if !validUUIDv4(target.ProjectID) || !validUUIDv4(target.WorkflowRunID) || !validUUIDv4(target.NodeRunID) ||
		!validUUIDv4(target.TargetArtifactID) || !validUUIDv4(target.TargetRevisionID) || !validDigest(target.TargetRevisionContentHash) {
		return invalid("target", "project, workflow, node, artifact, revision, or revision digest is invalid")
	}
	if target.NodeKey != ExternalQualificationGate || target.StageGate != ExternalQualificationGate ||
		!validCanonicalString(target.Subject, 256) {
		return invalid("target", "must identify the exact external-qualification gate and bounded subject")
	}
	return nil
}

func NormalizeWorkflowInputTarget(source WorkflowInputTargetSource, nodeRunID, targetArtifactID string) (PromotionTargetV2, error) {
	target := PromotionTargetV2{
		TargetArtifactID: targetArtifactID, NodeKey: source.NodeKey, NodeRunID: nodeRunID, ProjectID: source.ProjectID,
		TargetRevisionContentHash: source.TargetRevisionContentHash, TargetRevisionID: source.TargetRevisionID,
		StageGate: source.StageGate, Subject: source.ManifestSubject, WorkflowRunID: source.WorkflowRunID,
	}
	if err := validateTarget(target); err != nil {
		return PromotionTargetV2{}, err
	}
	return target, nil
}

func NormalizePlanReceiptTarget(source PlanReceiptTargetSource, nodeRunID, targetArtifactID string) (PromotionTargetV2, error) {
	target := PromotionTargetV2{
		TargetArtifactID: targetArtifactID, NodeKey: source.NodeKey, NodeRunID: nodeRunID, ProjectID: source.ProjectID,
		TargetRevisionContentHash: source.TargetRevision.ContentHash, TargetRevisionID: source.TargetRevision.ID,
		StageGate: source.StageGate, Subject: source.Subject, WorkflowRunID: source.WorkflowRunID,
	}
	if err := validateTarget(target); err != nil {
		return PromotionTargetV2{}, err
	}
	return target, nil
}

func validateWorkflowInput(input WorkflowInputProjection) error {
	if !validUUIDv4(input.AuthorityID) || !validUUIDv4(input.QualificationPolicyAuthorityID) ||
		!validDigest(input.AuthorityHash) || !validDigest(input.InputHash) || !validDigest(input.TargetHash) ||
		!validDigest(input.QualificationPolicyAuthorityHash) {
		return invalid("closure.workflowInput", "identity or digest is invalid")
	}
	return nil
}

func validateInputPrecommit(input InputPrecommitProjection) error {
	if err := qualificationinputauthority.ValidatePromotionBinding(input.promotionBinding()); err != nil {
		return invalid("closure.inputPrecommit", "%v", err)
	}
	return nil
}

func validatePlan(plan PlanProjection) error {
	if !validUUIDv4(plan.AuthorityID) || !validUUIDv4(plan.InputAuthorityID) || !validUUIDv4(plan.OrchestrationID) ||
		!validUUIDv4(plan.QualificationRunID) || !validDigest(plan.AuthorityHash) || !validDigest(plan.InputHash) ||
		!validDigest(plan.ProjectionHash) || !validDigest(plan.EvidencePlanHash) || !validDigest(plan.TargetHash) ||
		!validDigest(plan.TrustHash) {
		return invalid("closure.plan", "identity or digest is invalid")
	}
	return nil
}

func validateEvidence(evidence EvidenceProjection) error {
	if evidence.HeadVersion < 1 || evidence.HeadVersion > MaximumEvidenceEvents || evidence.Phase != EvidenceArtifactIndexed ||
		!validUUIDv4(evidence.LastEventID) || !validDigest(evidence.LastEventHash) || !validDigest(evidence.CommandHash) ||
		!validDigest(evidence.TrustBindingsDigest) || !validDigest(evidence.EvidenceClosureDigest) ||
		!validDigest(evidence.ArtifactIndexDigest) || !validDigest(evidence.EventSetDigest) {
		return invalid("closure.evidence", "terminal artifact-indexed Evidence projection is invalid")
	}
	return nil
}

func validateReceipt(receipt ReceiptProjection) error {
	if !validStableID(receipt.ReceiptID, 256) {
		return invalid("closure.receipt.receiptId", "is invalid")
	}
	for field, digest := range map[string]string{
		"envelopeHash":                receipt.EnvelopeHash,
		"payloadHash":                 receipt.PayloadHash,
		"paeHash":                     receipt.PAEHash,
		"completionHash":              receipt.CompletionHash,
		"snapshotRequestHash":         receipt.SnapshotRequestHash,
		"snapshotObservationHash":     receipt.SnapshotObservationHash,
		"verificationRequestHash":     receipt.VerificationRequestHash,
		"verificationObservationHash": receipt.VerificationObservationHash,
		"runnerRequestHash":           receipt.RunnerRequestHash,
		"runnerObservationHash":       receipt.RunnerObservationHash,
		"approverRequestHash":         receipt.ApproverRequestHash,
		"approverObservationHash":     receipt.ApproverObservationHash,
	} {
		if !validDigest(digest) {
			return invalid("closure.receipt."+field, "is invalid")
		}
	}
	return nil
}

func validateEvidenceEventSet(eventSet EvidenceEventSet) error {
	if eventSet.SchemaVersion != EvidenceEventSetSchemaV2 || !validUUIDv4(eventSet.OrchestrationID) ||
		eventSet.Events == nil || eventSet.HeadVersion < 1 || eventSet.HeadVersion > MaximumEvidenceEvents ||
		int64(len(eventSet.Events)) != eventSet.HeadVersion {
		return invalid("evidenceEventSet", "schema, orchestration, event set, or head version is invalid")
	}
	seen := make(map[string]struct{}, len(eventSet.Events))
	for index, event := range eventSet.Events {
		if event.Version != int64(index+1) || !validUUIDv4(event.EventID) || !validDigest(event.EventHash) {
			return invalid("evidenceEventSet.events", "must be the consecutive exact 1..head event sequence")
		}
		if _, duplicate := seen[event.EventID]; duplicate {
			return invalid("evidenceEventSet.events", "event identities must be unique")
		}
		seen[event.EventID] = struct{}{}
	}
	return nil
}

func validateClosure(closure PromotionClosure) error {
	if closure.SchemaVersion != ClosureSchemaV2 {
		return invalid("closure.schemaVersion", "is invalid")
	}
	if err := validateWorkflowInput(closure.WorkflowInput); err != nil {
		return err
	}
	if err := validateInputPrecommit(closure.InputPrecommit); err != nil {
		return err
	}
	if err := validatePlan(closure.Plan); err != nil {
		return err
	}
	if err := validateEvidence(closure.Evidence); err != nil {
		return err
	}
	if err := validateReceipt(closure.Receipt); err != nil {
		return err
	}
	if err := validateTarget(closure.Target); err != nil {
		return err
	}
	if closure.IndependentAuthorities == nil || len(closure.IndependentAuthorities) != 0 {
		// Migration 81 cannot admit independent receipts. A durable bundle that
		// claims any such receipt is corrupt rather than future-compatible.
		return invalid("closure.independentAuthorities", "must be an explicit empty collection until a reviewed admission migration exists")
	}
	if closure.Plan.InputAuthorityID != closure.WorkflowInput.AuthorityID {
		return invalid("closure", "Plan does not bind the exact Workflow Input authority identity")
	}
	if closure.InputPrecommit.WorkflowInputAuthorityID != closure.WorkflowInput.AuthorityID ||
		closure.InputPrecommit.WorkflowInputAuthorityHash != closure.WorkflowInput.AuthorityHash ||
		closure.InputPrecommit.QualificationPolicyAuthorityID != closure.WorkflowInput.QualificationPolicyAuthorityID ||
		closure.InputPrecommit.QualificationPolicyAuthorityHash != closure.WorkflowInput.QualificationPolicyAuthorityHash ||
		closure.InputPrecommit.QualificationPlanAuthorityID != closure.Plan.AuthorityID ||
		closure.InputPrecommit.QualificationPlanAuthorityHash != closure.Plan.AuthorityHash {
		return invalid("closure.inputPrecommit", "does not bind the exact Workflow Input, current Policy, and Plan authorities")
	}
	if closure.Plan.AuthorityID == closure.WorkflowInput.AuthorityID {
		return invalid("closure", "Workflow Input and Plan authority identities must be distinct")
	}
	return nil
}

func validateAuthorityReference(name string, reference AuthorityReference) error {
	if !validUUIDv4(reference.AuthorityID) || !validDigest(reference.AuthorityHash) {
		return invalid("revisionIntent."+name, "authority identity or hash is invalid")
	}
	return nil
}

func validateRevisionIntent(intent RevisionIntent) error {
	if intent.SchemaVersion != RevisionIntentSchemaV2 || intent.RevisionKind != RevisionKindV2 ||
		!validDigest(intent.RequestHash) || !validDigest(intent.ClosureHash) || !validUUIDv4(intent.OutputRevisionID) {
		return invalid("revisionIntent", "schema, kind, revision identity, or upstream hash is invalid")
	}
	if err := validateTarget(intent.Target); err != nil {
		return err
	}
	if err := validateAuthorityReference("workflowInput", intent.WorkflowInput); err != nil {
		return err
	}
	if err := validateAuthorityReference("plan", intent.Plan); err != nil {
		return err
	}
	if !validStableID(intent.Receipt.ReceiptID, 256) || !validDigest(intent.Receipt.EnvelopeHash) {
		return invalid("revisionIntent.receipt", "identity or envelope hash is invalid")
	}
	if !uniqueStrings([]string{intent.OutputRevisionID, intent.WorkflowInput.AuthorityID, intent.Plan.AuthorityID}) {
		return invalid("revisionIntent", "output revision, Workflow Input, and Plan identities must be distinct")
	}
	return nil
}

func validateConsumption(consumption Consumption) error {
	if consumption.SchemaVersion != ConsumptionSchemaV2 || !validUUIDv4(consumption.OperationID) ||
		!validDigest(consumption.RequestHash) || !validDigest(consumption.ClosureHash) ||
		!validDigest(consumption.RevisionIntentHash) || !validCanonicalTime(consumption.ConsumedAt) {
		return invalid("consumption", "schema, identity, hash, or timestamp is invalid")
	}
	return nil
}

func validateHandoff(handoff Handoff) error {
	if handoff.SchemaVersion != HandoffSchemaV2 || handoff.State != HandoffStatePending ||
		!validUUIDv4(handoff.HandoffID) || !validUUIDv4(handoff.OperationID) ||
		!validUUIDv4(handoff.OutputRevisionID) || !validUUIDv4(handoff.WorkflowInputAuthorityID) ||
		!validUUIDv4(handoff.PlanAuthorityID) || !validStableID(handoff.ReceiptID, 256) ||
		!validDigest(handoff.RevisionIntentHash) || !validDigest(handoff.ConsumptionHash) ||
		!validCanonicalTime(handoff.CreatedAt) {
		return invalid("handoff", "schema, state, identity, hash, or timestamp is invalid")
	}
	if !uniqueStrings([]string{
		handoff.OperationID, handoff.WorkflowInputAuthorityID, handoff.PlanAuthorityID, handoff.HandoffID, handoff.OutputRevisionID,
	}) {
		return invalid("handoff", "all five command identities must be pairwise distinct")
	}
	return validateTarget(handoff.Target)
}

func validateInitialIndependent(requirements []IndependentAuthorityRequirement) error {
	if requirements == nil {
		return fmt.Errorf("%w: current Policy must declare an explicit independent-authority list", ErrNotReady)
	}
	if len(requirements) != 0 {
		// Migration 81 has no registry write path or kind-specific verifier. Do
		// not inspect, normalize, sort, or resolve these values before failing.
		return fmt.Errorf("%w: independent-authority admission is not deployed", ErrNotReady)
	}
	return nil
}

func validateTerminalEvidenceEvent(event TerminalEvidenceEvent) error {
	if event.EventKind != EvidenceArtifactIndexed || event.Stage != EvidenceIndexCommitted ||
		!validUUIDv4(event.EventID) || !validDigest(event.EventHash) ||
		!validDigest(event.EvidenceClosureDigest) || !validDigest(event.ArtifactIndexDigest) {
		return invalid("prepared.evidenceTerminalEvent", "terminal artifact-indexed committed event is invalid")
	}
	return nil
}

func validatePlanControls(controls PlanControlBindings) error {
	if !validDigest(controls.TrustBindingsDigest) || !validDigest(controls.TrustPolicyDigest) {
		return invalid("prepared.planControls", "trust bindings or trust policy digest is invalid")
	}
	return nil
}

func validateImmutableContentBinding(name string, binding ImmutableContentBinding) error {
	if !validUUIDv4(binding.ID) || !validDigest(binding.ContentHash) {
		return invalid("prepared.workflowPlanBuild."+name, "identity or content hash is invalid")
	}
	return nil
}

func validateWorkflowPlanBuild(bindings WorkflowPlanBuildBindings) error {
	for name, binding := range map[string]ImmutableContentBinding{
		"planBuildContract":          bindings.PlanBuildContract,
		"planBuildManifest":          bindings.PlanBuildManifest,
		"workflowInputBuildContract": bindings.WorkflowInputBuildContract,
		"workflowInputBuildManifest": bindings.WorkflowInputBuildManifest,
	} {
		if err := validateImmutableContentBinding(name, binding); err != nil {
			return err
		}
	}
	if bindings.WorkflowInputBuildManifest != bindings.PlanBuildManifest ||
		bindings.WorkflowInputBuildContract != bindings.PlanBuildContract {
		return invalid("prepared.workflowPlanBuild", "Workflow Input and Plan build identities/content hashes differ")
	}
	if bindings.PlanBuildManifest.ID == bindings.PlanBuildContract.ID ||
		bindings.PlanBuildManifest.ContentHash == bindings.PlanBuildContract.ContentHash {
		return invalid("prepared.workflowPlanBuild", "build manifest and contract must remain independent")
	}
	return nil
}

func validateSourceBinding(name string, source SourceBinding) error {
	if !gitCommitPattern.MatchString(source.Commit) || source.Dirty ||
		source.TreeDigestSchema != SourceTreeDigestSchemaV1 || !validDigest(source.TreeDigest) {
		return invalid("prepared.sourceBindings."+name, "clean source-tree binding is invalid")
	}
	return nil
}

func validatePlanReceiptSource(bindings PlanReceiptSourceBindings) error {
	if err := validateSourceBinding("planSource", bindings.PlanSource); err != nil {
		return err
	}
	if err := validateSourceBinding("receiptSource", bindings.ReceiptSource); err != nil {
		return err
	}
	if bindings.PlanSource != bindings.ReceiptSource {
		return invalid("prepared.sourceBindings", "Plan input and terminal Receipt source bindings differ")
	}
	return nil
}

func validateQualificationPolicyBindings(prepared PreparedAuthority) error {
	locked := prepared.PolicyAuthority
	profile := prepared.PolicyPlanInputs.PolicyAuthority
	for name, binding := range map[string]QualificationPolicyAuthorityBinding{
		"locked":           locked,
		"planInputProfile": profile,
	} {
		if !validUUIDv4(binding.AuthorityID) || !validDigest(binding.AuthorityHash) {
			return invalid("prepared.policyAuthority."+name, "identity or authority hash is invalid")
		}
	}
	wia := QualificationPolicyAuthorityBinding{
		AuthorityHash: prepared.WorkflowInput.QualificationPolicyAuthorityHash,
		AuthorityID:   prepared.WorkflowInput.QualificationPolicyAuthorityID,
	}
	if locked != wia || profile != locked {
		return invalid("prepared.policyAuthority", "Workflow Input, locked current Policy, and Policy Plan-input profile do not identify the same exact authority")
	}
	return nil
}

func validatePlanAuthorityLineageBinding(name string, binding PlanAuthorityLineageBinding) error {
	if !validStableID(binding.ArtifactID, 256) || !validUUIDv4(binding.AuthorityID) ||
		!validUUIDv4(binding.FreezeOperationID) || !validUUIDv4(binding.InputAuthorityID) {
		return invalid("prepared.planReceiptLineage.authority."+name, "artifact or authority identity is invalid")
	}
	for field, digest := range map[string]string{
		"authorityHash":       binding.AuthorityHash,
		"evidencePlanHash":    binding.EvidencePlanHash,
		"inputHash":           binding.InputHash,
		"planDigest":          binding.PlanDigest,
		"projectionHash":      binding.ProjectionHash,
		"targetHash":          binding.TargetHash,
		"trustBindingsDigest": binding.TrustBindingsDigest,
		"trustHash":           binding.TrustHash,
	} {
		if !validDigest(digest) {
			return invalid("prepared.planReceiptLineage.authority."+name+"."+field, "is invalid")
		}
	}
	if !uniqueStrings([]string{binding.AuthorityID, binding.FreezeOperationID, binding.InputAuthorityID}) ||
		binding.ArtifactID != QualificationPlanArtifactPrefix+binding.AuthorityID ||
		binding.PlanDigest != binding.ProjectionHash {
		return invalid("prepared.planReceiptLineage.authority."+name, "identity, artifact, or Plan/projection binding is invalid")
	}
	return nil
}

func validatePlanReceiptAuthority(prepared PreparedAuthority) error {
	bindings := prepared.PlanReceiptLineage.Authority
	if err := validatePlanAuthorityLineageBinding("plan", bindings.Plan); err != nil {
		return err
	}
	if err := validatePlanAuthorityLineageBinding("receipt", bindings.Receipt); err != nil {
		return err
	}
	if bindings.Plan != bindings.Receipt {
		return invalid("prepared.planReceiptLineage.authority", "terminal Receipt planAuthority differs from the locked Plan envelope")
	}
	plan := bindings.Plan
	if plan.AuthorityHash != prepared.Plan.AuthorityHash || plan.AuthorityID != prepared.Plan.AuthorityID ||
		plan.EvidencePlanHash != prepared.Plan.EvidencePlanHash || plan.InputAuthorityID != prepared.Plan.InputAuthorityID ||
		plan.InputHash != prepared.Plan.InputHash || plan.PlanDigest != prepared.Plan.ProjectionHash ||
		plan.ProjectionHash != prepared.Plan.ProjectionHash || plan.TargetHash != prepared.Plan.TargetHash ||
		plan.TrustBindingsDigest != prepared.PlanControls.TrustBindingsDigest || plan.TrustHash != prepared.Plan.TrustHash {
		return invalid("prepared.planReceiptLineage.authority.plan", "does not equal the exact locked Plan projection and controls")
	}
	return nil
}

func validatePlanReceiptEvidencePlan(prepared PreparedAuthority) error {
	bindings := prepared.PlanReceiptLineage.EvidencePlan
	if err := qualificationevidence.ValidatePlan(bindings.Plan); err != nil {
		return invalid("prepared.planReceiptLineage.evidencePlan.plan", "%v", err)
	}
	if err := qualificationevidence.ValidatePlan(bindings.Receipt); err != nil {
		return invalid("prepared.planReceiptLineage.evidencePlan.receipt", "%v", err)
	}
	if !reflect.DeepEqual(bindings.Plan, bindings.Receipt) {
		return invalid("prepared.planReceiptLineage.evidencePlan", "terminal Receipt evidencePlan differs from the complete locked Plan document")
	}
	digest, err := qualificationevidence.CanonicalDigest(bindings.Plan)
	if err != nil || digest != prepared.Plan.EvidencePlanHash {
		return invalid("prepared.planReceiptLineage.evidencePlan", "canonical document does not equal the locked evidencePlanHash")
	}
	authority := prepared.PlanReceiptLineage.Authority.Plan
	if bindings.Plan.OrchestrationID != prepared.Plan.OrchestrationID ||
		bindings.Plan.RunID != prepared.Plan.QualificationRunID ||
		bindings.Plan.QualificationPlanArtifactID != authority.ArtifactID ||
		bindings.Plan.PlanDigest != prepared.Plan.ProjectionHash ||
		bindings.Plan.SourceTreeDigest != prepared.SourceBindings.PlanSource.TreeDigest ||
		bindings.Plan.Outputs.ReceiptID != prepared.Receipt.ReceiptID {
		return invalid("prepared.planReceiptLineage.evidencePlan", "root members do not bind the locked Plan projection and source")
	}
	return nil
}

func validatePlanReceiptTarget(prepared PreparedAuthority) error {
	bindings := prepared.PlanReceiptLineage.Target
	for name, document := range map[string]PlanReceiptTargetDocument{"plan": bindings.Plan, "receipt": bindings.Receipt} {
		target := document.PromotionTarget
		if document.SchemaVersion != PlanTargetSchemaV1 || !validUUIDv4(target.ProjectID) ||
			!validUUIDv4(target.WorkflowRunID) || target.NodeKey != ExternalQualificationGate ||
			target.StageGate != ExternalQualificationGate || !validCanonicalString(target.Subject, 256) ||
			!validUUIDv4(target.TargetRevision.ID) || !validDigest(target.TargetRevision.ContentHash) {
			return invalid("prepared.planReceiptLineage.target."+name, "target document is invalid")
		}
	}
	if bindings.Plan != bindings.Receipt {
		return invalid("prepared.planReceiptLineage.target", "terminal Receipt target differs from the locked Plan target document")
	}
	digest, err := qualificationevidence.CanonicalDigest(bindings.Plan)
	if err != nil || digest != prepared.Plan.TargetHash {
		return invalid("prepared.planReceiptLineage.target", "canonical target document does not equal the locked targetHash")
	}
	target := bindings.Plan.PromotionTarget
	expected := prepared.PlanTarget
	if target.ProjectID != expected.ProjectID || target.WorkflowRunID != expected.WorkflowRunID ||
		target.NodeKey != expected.NodeKey || target.Subject != expected.Subject || target.StageGate != expected.StageGate ||
		target.TargetRevision.ID != expected.TargetRevisionID ||
		target.TargetRevision.ContentHash != expected.TargetRevisionContentHash {
		return invalid("prepared.planReceiptLineage.target", "target document does not equal the normalized locked Plan target")
	}
	return nil
}

func validatePlanReceiptTrust(prepared PreparedAuthority) error {
	bindings := prepared.PlanReceiptLineage.Trust
	for name, document := range map[string]PlanReceiptTrustDocument{"plan": bindings.Plan, "receipt": bindings.Receipt} {
		if document.SchemaVersion != PlanTrustSchemaV1 || !validDigest(document.TrustPolicyDigest) {
			return invalid("prepared.planReceiptLineage.trust."+name, "schema or trust policy digest is invalid")
		}
		authorities := []string{
			document.TrustBindings.CaptureAuthorityID,
			document.TrustBindings.CredentialAuthorityID,
			document.TrustBindings.EncryptionAuthorityID,
			document.TrustBindings.IndexerAuthorityID,
			document.TrustBindings.KMSAuthorityID,
			document.TrustBindings.ReceiptAuthorityID,
			document.TrustBindings.SealerAuthorityID,
			document.TrustBindings.VerifierAuthorityID,
		}
		for _, authority := range authorities {
			if !validCanonicalString(authority, 512) {
				return invalid("prepared.planReceiptLineage.trust."+name, "authority identity is invalid")
			}
		}
		if !uniqueStrings(authorities) {
			return invalid("prepared.planReceiptLineage.trust."+name, "authority identities must be pairwise distinct")
		}
	}
	if bindings.Plan != bindings.Receipt {
		return invalid("prepared.planReceiptLineage.trust", "terminal Receipt trust differs from the locked Plan trust document")
	}
	bindingDigest, bindingErr := qualificationevidence.CanonicalDigest(bindings.Plan.TrustBindings)
	trustDigest, trustErr := qualificationevidence.CanonicalDigest(bindings.Plan)
	if bindingErr != nil || trustErr != nil || bindingDigest != prepared.PlanControls.TrustBindingsDigest ||
		trustDigest != prepared.Plan.TrustHash || bindings.Plan.TrustPolicyDigest != prepared.PlanControls.TrustPolicyDigest {
		return invalid("prepared.planReceiptLineage.trust", "canonical trust documents do not equal the locked Plan controls")
	}
	return nil
}

func validatePlanReceiptBuild(prepared PreparedAuthority) error {
	bindings := prepared.PlanReceiptLineage.Build
	for name, build := range map[string]BuildLineageBinding{"plan": bindings.Plan, "receipt": bindings.Receipt} {
		if err := validateImmutableContentBinding(name+"Contract", build.Contract); err != nil {
			return err
		}
		if err := validateImmutableContentBinding(name+"Manifest", build.Manifest); err != nil {
			return err
		}
		if build.Contract == build.Manifest || build.Contract.ID == build.Manifest.ID ||
			build.Contract.ContentHash == build.Manifest.ContentHash {
			return invalid("prepared.planReceiptLineage.build."+name, "build contract and manifest must remain independent")
		}
	}
	if bindings.Plan != bindings.Receipt || bindings.Plan.Contract != prepared.WorkflowPlanBuild.PlanBuildContract ||
		bindings.Plan.Manifest != prepared.WorkflowPlanBuild.PlanBuildManifest {
		return invalid("prepared.planReceiptLineage.build", "terminal Receipt build differs from the exact locked Plan input")
	}
	return nil
}

func validatePlanReceiptTemplateRelease(prepared PreparedAuthority) error {
	bindings := prepared.PlanReceiptLineage.TemplateRelease
	for name, template := range map[string]TemplateReleaseLineageBinding{"plan": bindings.Plan, "receipt": bindings.Receipt} {
		if !validUUIDv4(template.ID) || !validDigest(template.ContentHash) ||
			!validDigest(template.ApprovalReceiptDigest) || template.ContentHash == template.ApprovalReceiptDigest {
			return invalid("prepared.planReceiptLineage.templateRelease."+name, "identity or digest is invalid")
		}
	}
	if bindings.Plan != bindings.Receipt {
		return invalid("prepared.planReceiptLineage.templateRelease", "terminal Receipt TemplateRelease differs from the locked Plan input")
	}
	digest, err := qualificationevidence.CanonicalDigest(bindings.Plan)
	if err != nil || digest != prepared.PolicyPlanInputs.TemplateRelease.PlanDigest ||
		digest != prepared.PlanReceiptLineage.EvidencePlan.Plan.TemplateReleaseDigest {
		return invalid("prepared.planReceiptLineage.templateRelease", "canonical TemplateRelease does not bind the Plan input and evidence Plan")
	}
	return nil
}

func validatePlanReceiptGoldenRuntime(prepared PreparedAuthority) error {
	bindings := prepared.PlanReceiptLineage.GoldenRuntime
	for name, golden := range map[string]GoldenRuntimeLineageBinding{"plan": bindings.Plan, "receipt": bindings.Receipt} {
		if !validStableID(golden.AuthorityDocumentArtifactID, 256) || !validDigest(golden.AuthorityDocumentDigest) ||
			!validStableID(golden.FixtureDocumentArtifactID, 256) || !validDigest(golden.FixtureDocumentDigest) ||
			!validDigest(golden.FaultOperationSetDigest) || !validUUIDv4(golden.FixtureID) ||
			golden.AuthorityDocumentArtifactID == golden.FixtureDocumentArtifactID ||
			golden.AuthorityDocumentDigest == golden.FixtureDocumentDigest {
			return invalid("prepared.planReceiptLineage.goldenRuntime."+name, "Golden runtime binding is invalid")
		}
	}
	if bindings.Plan != bindings.Receipt {
		return invalid("prepared.planReceiptLineage.goldenRuntime", "terminal Receipt Golden runtime differs from the locked Plan input")
	}
	digest, err := qualificationevidence.CanonicalDigest(bindings.Plan)
	if err != nil || digest != prepared.PolicyPlanInputs.GoldenRuntime.PlanDigest ||
		bindings.Plan.FixtureID != prepared.PlanReceiptLineage.EvidencePlan.Plan.FixtureID {
		return invalid("prepared.planReceiptLineage.goldenRuntime", "Golden runtime does not bind the Plan input and evidence Plan fixture")
	}
	foundAuthority, foundFixture := false, false
	for _, artifact := range prepared.PlanReceiptLineage.EvidencePlan.Plan.Artifacts {
		if artifact.ID == bindings.Plan.AuthorityDocumentArtifactID {
			foundAuthority = artifact.Kind == qualificationevidence.ArtifactKindGolden && artifact.Classification == qualificationevidence.ClassificationDistributable
		}
		if artifact.ID == bindings.Plan.FixtureDocumentArtifactID {
			foundFixture = artifact.Kind == qualificationevidence.ArtifactKindGolden && artifact.Classification == qualificationevidence.ClassificationDistributable
		}
	}
	if !foundAuthority || !foundFixture {
		return invalid("prepared.planReceiptLineage.goldenRuntime", "Golden documents are not the exact distributable evidence Plan artifacts")
	}
	return nil
}

func validatePlanReceiptQualificationManifest(prepared PreparedAuthority) error {
	bindings := prepared.PlanReceiptLineage.QualificationManifest
	for name, manifest := range map[string]QualificationManifestLineageBinding{"plan": bindings.Plan, "receipt": bindings.Receipt} {
		if !validStableID(manifest.ArtifactID, 256) || !validUUIDv4(manifest.RevisionID) || !validDigest(manifest.ContentHash) {
			return invalid("prepared.planReceiptLineage.qualificationManifest."+name, "artifact, revision, or content hash is invalid")
		}
	}
	if bindings.Plan != bindings.Receipt {
		return invalid("prepared.planReceiptLineage.qualificationManifest", "terminal Receipt qualification manifest differs from the locked Plan input")
	}
	policyPlan := prepared.PolicyPlanInputs.QualificationManifest
	if bindings.Plan.ArtifactID != policyPlan.PlanArtifactID || bindings.Plan.RevisionID != policyPlan.PlanRevisionID ||
		bindings.Plan.ContentHash != policyPlan.PlanContentHash {
		return invalid("prepared.planReceiptLineage.qualificationManifest", "qualification manifest does not equal the Policy-bound Plan input")
	}
	return nil
}

func validateTerminalCredentialArtifact(name string, artifact TerminalCredentialArtifactBinding) error {
	if !validStableID(artifact.ArtifactID, 256) || !validDigest(artifact.ContentDigest) ||
		!validDigest(artifact.PayloadDigest) || !validDigest(artifact.SignerSetDigest) {
		return invalid("prepared.planReceiptLineage.credentialSet.receipt."+name, "signed artifact commitment is invalid")
	}
	return nil
}

func validatePlanReceiptCredentialSet(prepared PreparedAuthority) error {
	bindings := prepared.PlanReceiptLineage.CredentialSet
	if bindings.Plan != prepared.PlanReceiptLineage.EvidencePlan.Plan.CredentialSet {
		return invalid("prepared.planReceiptLineage.credentialSet.plan", "does not equal the complete evidence Plan credential expectation")
	}
	receipt := bindings.Receipt
	if !validUUIDv4(receipt.SetID) || !validCanonicalString(receipt.Issuer, 512) ||
		!validCanonicalString(receipt.Audience, 512) || !validDigest(receipt.SetHandleHash) ||
		!validDigest(receipt.MemberBindingsDigest) || receipt.SetHandleHash == receipt.MemberBindingsDigest ||
		receipt.MemberCount < 1 || receipt.MemberCount > qualificationevidence.MaximumMembers {
		return invalid("prepared.planReceiptLineage.credentialSet.receipt", "credential-set identity or commitment is invalid")
	}
	if err := validateTerminalCredentialArtifact("issuance", receipt.Issuance); err != nil {
		return err
	}
	if err := validateTerminalCredentialArtifact("revocation", receipt.Revocation); err != nil {
		return err
	}
	if receipt.Issuance.ArtifactID == receipt.Revocation.ArtifactID ||
		receipt.Issuance.ContentDigest == receipt.Revocation.ContentDigest ||
		receipt.Issuance.PayloadDigest == receipt.Revocation.PayloadDigest {
		return invalid("prepared.planReceiptLineage.credentialSet.receipt", "issuance and revocation commitments must remain independent")
	}
	issued, issuedErr := time.Parse(canonicalTimeLayout, receipt.IssuedAt)
	revoked, revokedErr := time.Parse(canonicalTimeLayout, receipt.RevokedAt)
	expires, expiresErr := time.Parse(canonicalTimeLayout, receipt.ExpiresAt)
	if issuedErr != nil || revokedErr != nil || expiresErr != nil ||
		!validCanonicalTime(receipt.IssuedAt) || !validCanonicalTime(receipt.RevokedAt) || !validCanonicalTime(receipt.ExpiresAt) ||
		!issued.Before(revoked) || !revoked.Before(expires) {
		return invalid("prepared.planReceiptLineage.credentialSet.receipt", "credential time ordering must be issued < revoked < expires")
	}
	plan := bindings.Plan
	if receipt.SetID != plan.SetID || receipt.Issuer != plan.Issuer || receipt.Audience != plan.Audience ||
		receipt.SetHandleHash != plan.SetHandleHash || receipt.MemberBindingsDigest != plan.MemberBindingsDigest ||
		receipt.MemberCount != plan.MemberCount || receipt.Issuance.ArtifactID != plan.IssuanceArtifactID ||
		receipt.Revocation.ArtifactID != plan.RevocationArtifactID {
		return invalid("prepared.planReceiptLineage.credentialSet", "terminal Receipt credential set differs from all eight frozen Plan members")
	}
	return nil
}

func validatePlanReceiptLineage(prepared PreparedAuthority) error {
	for _, validate := range []func(PreparedAuthority) error{
		validatePlanReceiptAuthority,
		validatePlanReceiptEvidencePlan,
		validatePlanReceiptTarget,
		validatePlanReceiptTrust,
		validatePlanReceiptBuild,
		validatePlanReceiptTemplateRelease,
		validatePlanReceiptGoldenRuntime,
		validatePlanReceiptQualificationManifest,
		validatePlanReceiptCredentialSet,
	} {
		if err := validate(prepared); err != nil {
			return err
		}
	}
	return nil
}

func validateExactDocumentDigestBinding(name string, binding ExactDocumentDigestBinding) error {
	if !validDigest(binding.PolicyDigest) || !validDigest(binding.PlanDigest) {
		return invalid("prepared.policyPlanInputs."+name, "Policy or Plan canonical digest is invalid")
	}
	if binding.PolicyDigest != binding.PlanDigest {
		return invalid("prepared.policyPlanInputs."+name, "Policy and Plan input members differ")
	}
	return nil
}

func validateCredentialProfilePlan(bindings CredentialProfilePlanBindings) error {
	for name, value := range map[string]string{
		"planAudience":               bindings.PlanAudience,
		"planIssuanceArtifactId":     bindings.PlanIssuanceArtifactID,
		"planIssuer":                 bindings.PlanIssuer,
		"planRevocationArtifactId":   bindings.PlanRevocationArtifactID,
		"policyAudience":             bindings.PolicyAudience,
		"policyAuthorityId":          bindings.PolicyAuthorityID,
		"policyIssuanceArtifactId":   bindings.PolicyIssuanceArtifactID,
		"policyRevocationArtifactId": bindings.PolicyRevocationArtifactID,
	} {
		if !validCanonicalString(value, 512) {
			return invalid("prepared.policyPlanInputs.credentialProfile."+name, "is invalid")
		}
	}
	if !validDigest(bindings.PolicyMemberRequestSetDigest) {
		return invalid("prepared.policyPlanInputs.credentialProfile.policyMemberRequestSetDigest", "is invalid")
	}
	if bindings.PolicyAudience != bindings.PlanAudience || bindings.PolicyAuthorityID != bindings.PlanIssuer ||
		bindings.PolicyIssuanceArtifactID != bindings.PlanIssuanceArtifactID ||
		bindings.PolicyRevocationArtifactID != bindings.PlanRevocationArtifactID {
		return invalid("prepared.policyPlanInputs.credentialProfile", "Policy credential profile does not map exactly to Plan credential controls")
	}
	return nil
}

func validateQualificationManifestPlan(bindings QualificationManifestPlanBindings, projectionHash string) error {
	for name, value := range map[string]string{
		"planArtifactId":   bindings.PlanArtifactID,
		"policyArtifactId": bindings.PolicyArtifactID,
	} {
		if !validStableID(value, 256) {
			return invalid("prepared.policyPlanInputs.qualificationManifest."+name, "is invalid")
		}
	}
	if !validUUIDv4(bindings.PlanRevisionID) || !validUUIDv4(bindings.PolicyRevisionID) ||
		!validDigest(bindings.PlanContentHash) || !validDigest(bindings.PolicyContentHash) ||
		!validDigest(bindings.PlanQualificationPlanDigest) || !validDigest(bindings.PolicyPlanDigest) {
		return invalid("prepared.policyPlanInputs.qualificationManifest", "revision or digest is invalid")
	}
	if bindings.PlanArtifactID != bindings.PolicyArtifactID ||
		bindings.PlanRevisionID != bindings.PolicyRevisionID ||
		bindings.PlanContentHash != bindings.PolicyContentHash ||
		bindings.PolicyPlanDigest != bindings.PlanQualificationPlanDigest ||
		bindings.PlanQualificationPlanDigest != projectionHash {
		return invalid("prepared.policyPlanInputs.qualificationManifest", "Policy profile does not map exactly to Plan manifest and qualificationPlanDigest")
	}
	return nil
}

func validatePolicyPlanInputs(bindings PolicyPlanInputBindings, plan PlanProjection, controls PlanControlBindings) error {
	for name, binding := range map[string]ExactDocumentDigestBinding{
		"artifactPolicy":  bindings.ArtifactPolicy,
		"artifacts":       bindings.Artifacts,
		"goldenRuntime":   bindings.GoldenRuntime,
		"outputPolicy":    bindings.OutputPolicy,
		"outputs":         bindings.Outputs,
		"recipient":       bindings.Recipient,
		"templateRelease": bindings.TemplateRelease,
		"trustBindings":   bindings.TrustBindings,
		"trustPolicy":     bindings.TrustPolicy,
	} {
		if err := validateExactDocumentDigestBinding(name, binding); err != nil {
			return err
		}
	}
	if err := validateCredentialProfilePlan(bindings.CredentialProfile); err != nil {
		return err
	}
	if err := validateQualificationManifestPlan(bindings.QualificationManifest, plan.ProjectionHash); err != nil {
		return err
	}
	if controls.TrustBindingsDigest != bindings.TrustBindings.PlanDigest ||
		controls.TrustPolicyDigest != bindings.TrustPolicy.PlanDigest {
		return invalid("prepared.policyPlanInputs", "Plan trust controls differ from Policy-fixed Plan inputs")
	}
	return nil
}

func validateReceiptRequest(name string, request ReceiptRequestBindings, kind, role string, prepared PreparedAuthority) error {
	if request.Kind != kind || request.Role != role || !validDigest(request.RequestHash) ||
		!validUUIDv4(request.PlanAuthorityID) || !validUUIDv4(request.OrchestrationID) ||
		request.EvidenceHeadVersion < 1 || request.EvidenceHeadVersion > MaximumEvidenceEvents ||
		!validUUIDv4(request.EvidenceLastEventID) {
		return invalid("prepared.receiptControls.requests."+name, "request identity, kind, role, or Evidence head is invalid")
	}
	for field, digest := range map[string]string{
		"artifactIndexDigest":   request.ArtifactIndexDigest,
		"evidenceClosureDigest": request.EvidenceClosureDigest,
		"evidenceCommandDigest": request.EvidenceCommandDigest,
		"evidenceLastEventHash": request.EvidenceLastEventHash,
		"evidencePlanHash":      request.EvidencePlanHash,
		"evidenceTrustDigest":   request.EvidenceTrustDigest,
		"inputHash":             request.InputHash,
		"planAuthorityHash":     request.PlanAuthorityHash,
		"projectionHash":        request.ProjectionHash,
		"targetHash":            request.TargetHash,
		"trustBindingsDigest":   request.TrustBindingsDigest,
		"trustHash":             request.TrustHash,
		"trustPolicyDigest":     request.TrustPolicyDigest,
	} {
		if !validDigest(digest) {
			return invalid("prepared.receiptControls.requests."+name+"."+field, "is invalid")
		}
	}
	terminal := prepared.EvidenceTerminalEvent
	if request.PlanAuthorityID != prepared.Plan.AuthorityID || request.PlanAuthorityHash != prepared.Plan.AuthorityHash ||
		request.InputHash != prepared.Plan.InputHash || request.ProjectionHash != prepared.Plan.ProjectionHash ||
		request.EvidencePlanHash != prepared.Plan.EvidencePlanHash || request.TargetHash != prepared.Plan.TargetHash ||
		request.TrustHash != prepared.Plan.TrustHash || request.TrustBindingsDigest != prepared.PlanControls.TrustBindingsDigest ||
		request.TrustPolicyDigest != prepared.PlanControls.TrustPolicyDigest ||
		request.OrchestrationID != prepared.Plan.OrchestrationID || request.EvidenceHeadVersion != prepared.Evidence.HeadVersion ||
		request.EvidenceLastEventID != terminal.EventID || request.EvidenceLastEventHash != terminal.EventHash ||
		request.EvidenceCommandDigest != prepared.Evidence.CommandHash ||
		request.EvidenceTrustDigest != prepared.Evidence.TrustBindingsDigest ||
		request.EvidenceClosureDigest != terminal.EvidenceClosureDigest ||
		request.ArtifactIndexDigest != terminal.ArtifactIndexDigest {
		return invalid("prepared.receiptControls.requests."+name, "does not bind the exact Plan and terminal Evidence event")
	}
	return nil
}

func validateReceiptObservation(name string, observation ReceiptObservationBindings, requestHash, observationHash string) error {
	if observation.Sequence < 1 || observation.Sequence > MaximumJavaScriptSafeInt64 ||
		observation.LatestSequence != observation.Sequence || observation.Status != ReceiptObservationCommitted ||
		!validAuthorityTime(observation.RecordedAt) ||
		!validDigest(observation.RequestHash) || !validDigest(observation.ObservationHash) ||
		observation.RequestHash != requestHash || observation.ObservationHash != observationHash {
		return invalid("prepared.receiptControls.observations."+name, "is not the exact latest committed observation")
	}
	return nil
}

func validateTerminalReceiptControls(prepared PreparedAuthority) error {
	controls := prepared.ReceiptControls
	if controls == (TerminalReceiptControls{}) {
		return fmt.Errorf("%w: exact terminal Receipt v3 controls are not ready", ErrNotReady)
	}
	if !validUUIDv4(controls.PlanAuthorityID) || !validUUIDv4(controls.OrchestrationID) ||
		!validDigest(controls.PlanAuthorityHash) || !validDigest(controls.EvidenceClosureDigest) ||
		!validDigest(controls.ArtifactIndexDigest) || !validAuthorityTime(controls.CompletedAt) ||
		controls.PlanAuthorityID != prepared.Plan.AuthorityID ||
		controls.PlanAuthorityHash != prepared.Plan.AuthorityHash || controls.OrchestrationID != prepared.Plan.OrchestrationID ||
		controls.EvidenceClosureDigest != prepared.EvidenceTerminalEvent.EvidenceClosureDigest ||
		controls.ArtifactIndexDigest != prepared.EvidenceTerminalEvent.ArtifactIndexDigest {
		return invalid("prepared.receiptControls", "terminal Receipt row does not bind the exact Plan and Evidence authority")
	}
	requests := controls.Requests
	requestCases := []struct {
		name    string
		request ReceiptRequestBindings
		kind    string
		role    string
	}{
		{name: "snapshotSeal", request: requests.SnapshotSeal, kind: ReceiptRequestSnapshotSeal, role: ReceiptRoleSealer},
		{name: "snapshotVerify", request: requests.SnapshotVerify, kind: ReceiptRequestSnapshotVerify, role: ReceiptRoleVerifier},
		{name: "runnerSign", request: requests.RunnerSign, kind: ReceiptRequestSign, role: ReceiptRoleQualificationRunner},
		{name: "approverSign", request: requests.ApproverSign, kind: ReceiptRequestSign, role: ReceiptRoleReleaseApprover},
	}
	for _, requestCase := range requestCases {
		if err := validateReceiptRequest(requestCase.name, requestCase.request, requestCase.kind, requestCase.role, prepared); err != nil {
			return err
		}
	}
	if requests.SnapshotSeal.RequestHash != prepared.Receipt.SnapshotRequestHash ||
		requests.SnapshotVerify.RequestHash != prepared.Receipt.VerificationRequestHash ||
		requests.RunnerSign.RequestHash != prepared.Receipt.RunnerRequestHash ||
		requests.ApproverSign.RequestHash != prepared.Receipt.ApproverRequestHash ||
		!uniqueStrings([]string{
			requests.SnapshotSeal.RequestHash, requests.SnapshotVerify.RequestHash,
			requests.RunnerSign.RequestHash, requests.ApproverSign.RequestHash,
		}) {
		return invalid("prepared.receiptControls.requests", "four exact request hashes do not match the terminal Receipt")
	}
	observations := controls.Observations
	if err := validateReceiptObservation("snapshotSeal", observations.SnapshotSeal, requests.SnapshotSeal.RequestHash, prepared.Receipt.SnapshotObservationHash); err != nil {
		return err
	}
	if err := validateReceiptObservation("snapshotVerify", observations.SnapshotVerify, requests.SnapshotVerify.RequestHash, prepared.Receipt.VerificationObservationHash); err != nil {
		return err
	}
	if err := validateReceiptObservation("runnerSign", observations.RunnerSign, requests.RunnerSign.RequestHash, prepared.Receipt.RunnerObservationHash); err != nil {
		return err
	}
	if err := validateReceiptObservation("approverSign", observations.ApproverSign, requests.ApproverSign.RequestHash, prepared.Receipt.ApproverObservationHash); err != nil {
		return err
	}
	for _, recordedAt := range []time.Time{
		observations.SnapshotSeal.RecordedAt, observations.SnapshotVerify.RecordedAt,
		observations.RunnerSign.RecordedAt, observations.ApproverSign.RecordedAt,
	} {
		if !controls.CompletedAt.After(recordedAt) {
			return invalid("prepared.receiptControls.completedAt", "must be later than every terminal observation")
		}
	}
	return nil
}

func validatePrepared(prepared PreparedAuthority, eventSetHash string) error {
	if err := validateInitialIndependent(prepared.IndependentRequirements); err != nil {
		return err
	}
	if !prepared.PolicyCurrent || !prepared.WorkflowInputCurrent || !prepared.TargetCurrent {
		return fmt.Errorf("%w: Policy, Workflow Input, and target must still be current", ErrStale)
	}
	if err := validateWorkflowInput(prepared.WorkflowInput); err != nil {
		return err
	}
	if err := validateQualificationPolicyBindings(prepared); err != nil {
		return err
	}
	if err := validatePlan(prepared.Plan); err != nil {
		return err
	}
	if err := validateInputPrecommit(prepared.InputPrecommit); err != nil {
		return err
	}
	if prepared.InputPrecommit.WorkflowInputAuthorityID != prepared.WorkflowInput.AuthorityID ||
		prepared.InputPrecommit.WorkflowInputAuthorityHash != prepared.WorkflowInput.AuthorityHash ||
		prepared.InputPrecommit.QualificationPolicyAuthorityID != prepared.PolicyAuthority.AuthorityID ||
		prepared.InputPrecommit.QualificationPolicyAuthorityHash != prepared.PolicyAuthority.AuthorityHash ||
		prepared.InputPrecommit.QualificationPlanAuthorityID != prepared.Plan.AuthorityID ||
		prepared.InputPrecommit.QualificationPlanAuthorityHash != prepared.Plan.AuthorityHash {
		return invalid("prepared.inputPrecommit", "does not equal the locked Workflow Input, current Policy, and Plan authorities")
	}
	if err := validateEvidence(prepared.Evidence); err != nil {
		return err
	}
	if err := validateReceipt(prepared.Receipt); err != nil {
		return err
	}
	if err := validateTerminalEvidenceEvent(prepared.EvidenceTerminalEvent); err != nil {
		return err
	}
	if err := validatePlanControls(prepared.PlanControls); err != nil {
		return err
	}
	if err := validateTarget(prepared.Target); err != nil {
		return err
	}
	if err := validateTarget(prepared.PlanTarget); err != nil {
		return err
	}
	if err := validateTarget(prepared.ReceiptTarget); err != nil {
		return err
	}
	if !sameTarget(prepared.Target, prepared.PlanTarget) || !sameTarget(prepared.Target, prepared.ReceiptTarget) {
		return invalid("prepared.target", "Workflow Input, Plan, and Receipt targets differ")
	}
	if prepared.TargetRevisionArtifactID != prepared.Target.TargetArtifactID {
		return invalid("prepared.target", "locked target revision does not belong to the target artifact")
	}
	lastEvent := prepared.EvidenceEventSet.Events[len(prepared.EvidenceEventSet.Events)-1]
	if prepared.Plan.OrchestrationID != prepared.EvidenceEventSet.OrchestrationID ||
		prepared.Evidence.HeadVersion != prepared.EvidenceEventSet.HeadVersion ||
		prepared.Evidence.LastEventID != lastEvent.EventID || prepared.Evidence.LastEventHash != lastEvent.EventHash ||
		prepared.Evidence.EventSetDigest != eventSetHash {
		return invalid("prepared.evidence", "event set does not bind the exact Plan and locked Evidence head")
	}
	terminal := prepared.EvidenceTerminalEvent
	if terminal.EventID != prepared.Evidence.LastEventID || terminal.EventHash != prepared.Evidence.LastEventHash ||
		terminal.EvidenceClosureDigest != prepared.Evidence.EvidenceClosureDigest ||
		terminal.ArtifactIndexDigest != prepared.Evidence.ArtifactIndexDigest {
		return invalid("prepared.evidenceTerminalEvent", "does not equal the locked Evidence head and projection")
	}
	if prepared.Evidence.CommandHash != prepared.Plan.EvidencePlanHash ||
		prepared.Evidence.TrustBindingsDigest != prepared.PlanControls.TrustBindingsDigest {
		return invalid("prepared.evidence", "Evidence command/trust controls do not equal the exact Plan authority")
	}
	if err := validateWorkflowPlanBuild(prepared.WorkflowPlanBuild); err != nil {
		return err
	}
	if err := validatePlanReceiptSource(prepared.SourceBindings); err != nil {
		return err
	}
	if err := validatePolicyPlanInputs(prepared.PolicyPlanInputs, prepared.Plan, prepared.PlanControls); err != nil {
		return err
	}
	if err := validatePlanReceiptLineage(prepared); err != nil {
		return err
	}
	if err := validateTerminalReceiptControls(prepared); err != nil {
		return err
	}
	return nil
}

// Compile constructs the acyclic request -> closure -> revision intent ->
// consumption -> handoff graph from one already transaction-bound authority.
// It is exported for PostgreSQL bundle cross-validation, not for assembling a
// production authority from independent repository reads.
func Compile(command ConsumeCommand, prepared PreparedAuthority, trustedNow time.Time) (Record, error) {
	if err := ValidateCommand(command); err != nil {
		return Record{}, err
	}
	if err := validateInitialIndependent(prepared.IndependentRequirements); err != nil {
		return Record{}, err
	}
	prepared = clonePrepared(prepared)
	prepared.EvidenceEventSet.SchemaVersion = EvidenceEventSetSchemaV2
	eventSetBytes, eventSetHash, err := EncodeEvidenceEventSet(prepared.EvidenceEventSet)
	if err != nil {
		return Record{}, err
	}
	if prepared.Evidence.EventSetDigest == "" {
		prepared.Evidence.EventSetDigest = eventSetHash
	}
	if err := validatePrepared(prepared, eventSetHash); err != nil {
		return Record{}, err
	}
	inputPrecommitID := uuid.MustParse(prepared.InputPrecommit.AuthorityID)
	if inputPrecommitID == command.OperationID || inputPrecommitID == command.HandoffID ||
		inputPrecommitID == command.OutputRevisionID {
		return Record{}, fmt.Errorf("%w: server command identity aliases the resolved input precommit authority", ErrConflict)
	}
	if command.OutputRevisionID.String() == prepared.Target.TargetRevisionID {
		return Record{}, fmt.Errorf("%w: output revision identity is the already reserved target revision", ErrConflict)
	}
	now := trustedNow.UTC().Truncate(time.Millisecond)
	if !validAuthorityTime(now) {
		return Record{}, invalid("trustedNow", "must be a representable database timestamp")
	}
	request := ConsumeRequest{
		HandoffID: command.HandoffID.String(), OperationID: command.OperationID.String(),
		OutputRevisionID: command.OutputRevisionID.String(), PlanAuthorityID: command.PlanAuthorityID.String(),
		SchemaVersion: RequestSchemaV2, WorkflowInputAuthorityID: command.WorkflowInputAuthorityID.String(),
	}
	requestBytes, requestHash, err := EncodeRequest(request)
	if err != nil {
		return Record{}, err
	}
	closure := PromotionClosure{
		Evidence: prepared.Evidence, IndependentAuthorities: []IndependentAuthorityProjection{},
		InputPrecommit: prepared.InputPrecommit, Plan: prepared.Plan, Receipt: prepared.Receipt, SchemaVersion: ClosureSchemaV2,
		Target: prepared.Target, WorkflowInput: prepared.WorkflowInput,
	}
	closureBytes, closureHash, err := EncodeClosure(closure)
	if err != nil {
		return Record{}, err
	}
	intent := RevisionIntent{
		ClosureHash: closureHash, OutputRevisionID: command.OutputRevisionID.String(),
		Plan:        AuthorityReference{AuthorityHash: prepared.Plan.AuthorityHash, AuthorityID: prepared.Plan.AuthorityID},
		Receipt:     ReceiptReference{EnvelopeHash: prepared.Receipt.EnvelopeHash, ReceiptID: prepared.Receipt.ReceiptID},
		RequestHash: requestHash, RevisionKind: RevisionKindV2, SchemaVersion: RevisionIntentSchemaV2,
		Target:        prepared.Target,
		WorkflowInput: AuthorityReference{AuthorityHash: prepared.WorkflowInput.AuthorityHash, AuthorityID: prepared.WorkflowInput.AuthorityID},
	}
	intentBytes, intentHash, err := EncodeRevisionIntent(intent)
	if err != nil {
		return Record{}, err
	}
	canonicalNow := now.Format(canonicalTimeLayout)
	consumption := Consumption{
		ClosureHash: closureHash, ConsumedAt: canonicalNow, OperationID: command.OperationID.String(),
		RequestHash: requestHash, RevisionIntentHash: intentHash, SchemaVersion: ConsumptionSchemaV2,
	}
	consumptionBytes, consumptionHash, err := EncodeConsumption(consumption)
	if err != nil {
		return Record{}, err
	}
	handoff := Handoff{
		ConsumptionHash: consumptionHash, CreatedAt: canonicalNow, HandoffID: command.HandoffID.String(),
		OperationID: command.OperationID.String(), OutputRevisionID: command.OutputRevisionID.String(),
		PlanAuthorityID: command.PlanAuthorityID.String(), ReceiptID: prepared.Receipt.ReceiptID,
		RevisionIntentHash: intentHash, SchemaVersion: HandoffSchemaV2, State: HandoffStatePending,
		Target: prepared.Target, WorkflowInputAuthorityID: command.WorkflowInputAuthorityID.String(),
	}
	handoffBytes, handoffHash, err := EncodeHandoff(handoff)
	if err != nil {
		return Record{}, err
	}
	record := Record{
		Command: command, ReceiptID: prepared.Receipt.ReceiptID,
		Request: request, RequestBytes: requestBytes, RequestHash: requestHash,
		EvidenceEventSet: prepared.EvidenceEventSet, EvidenceEventSetBytes: eventSetBytes, EvidenceEventSetHash: eventSetHash,
		Closure: closure, ClosureBytes: closureBytes, ClosureHash: closureHash,
		RevisionIntent: intent, RevisionIntentBytes: intentBytes, RevisionIntentHash: intentHash,
		Consumption: consumption, ConsumptionBytes: consumptionBytes, ConsumptionHash: consumptionHash,
		Handoff: handoff, HandoffBytes: handoffBytes, HandoffHash: handoffHash,
		ConsumedAt: now, CreatedAt: now,
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, err
	}
	return cloneRecord(record), nil
}

// ValidateRecord reparses every exact byte sequence, recomputes every domain
// hash, and independently checks the complete acyclic graph and scalar copy.
func ValidateRecord(record Record) error {
	if err := ValidateCommand(record.Command); err != nil {
		return err
	}
	request, err := DecodeRequest(record.RequestBytes, record.RequestHash)
	if err != nil {
		return invalid("record.request", "%v", err)
	}
	eventSet, err := DecodeEvidenceEventSet(record.EvidenceEventSetBytes, record.EvidenceEventSetHash)
	if err != nil {
		return invalid("record.evidenceEventSet", "%v", err)
	}
	closure, err := DecodeClosure(record.ClosureBytes, record.ClosureHash)
	if err != nil {
		return invalid("record.closure", "%v", err)
	}
	intent, err := DecodeRevisionIntent(record.RevisionIntentBytes, record.RevisionIntentHash)
	if err != nil {
		return invalid("record.revisionIntent", "%v", err)
	}
	consumption, err := DecodeConsumption(record.ConsumptionBytes, record.ConsumptionHash)
	if err != nil {
		return invalid("record.consumption", "%v", err)
	}
	handoff, err := DecodeHandoff(record.HandoffBytes, record.HandoffHash)
	if err != nil {
		return invalid("record.handoff", "%v", err)
	}
	if !reflect.DeepEqual(record.Request, request) || !reflect.DeepEqual(record.EvidenceEventSet, eventSet) ||
		!reflect.DeepEqual(record.Closure, closure) || !reflect.DeepEqual(record.RevisionIntent, intent) ||
		!reflect.DeepEqual(record.Consumption, consumption) || !reflect.DeepEqual(record.Handoff, handoff) {
		return invalid("record", "typed projections differ from exact canonical bytes")
	}
	command := record.Command
	if request.OperationID != command.OperationID.String() || request.WorkflowInputAuthorityID != command.WorkflowInputAuthorityID.String() ||
		request.PlanAuthorityID != command.PlanAuthorityID.String() || request.HandoffID != command.HandoffID.String() ||
		request.OutputRevisionID != command.OutputRevisionID.String() {
		return invalid("record.request", "does not bind the exact five-ID command")
	}
	lastEvent := eventSet.Events[len(eventSet.Events)-1]
	if closure.WorkflowInput.AuthorityID != command.WorkflowInputAuthorityID.String() ||
		closure.Plan.AuthorityID != command.PlanAuthorityID.String() || closure.Plan.OrchestrationID != eventSet.OrchestrationID ||
		closure.Evidence.EventSetDigest != record.EvidenceEventSetHash || closure.Evidence.HeadVersion != eventSet.HeadVersion ||
		closure.Evidence.LastEventID != lastEvent.EventID || closure.Evidence.LastEventHash != lastEvent.EventHash ||
		closure.Receipt.ReceiptID != record.ReceiptID {
		return invalid("record.closure", "does not bind the command, event set, or Receipt")
	}
	if closure.InputPrecommit.WorkflowInputAuthorityID != closure.WorkflowInput.AuthorityID ||
		closure.InputPrecommit.WorkflowInputAuthorityHash != closure.WorkflowInput.AuthorityHash ||
		closure.InputPrecommit.QualificationPolicyAuthorityID != closure.WorkflowInput.QualificationPolicyAuthorityID ||
		closure.InputPrecommit.QualificationPolicyAuthorityHash != closure.WorkflowInput.QualificationPolicyAuthorityHash ||
		closure.InputPrecommit.QualificationPlanAuthorityID != closure.Plan.AuthorityID ||
		closure.InputPrecommit.QualificationPlanAuthorityHash != closure.Plan.AuthorityHash {
		return invalid("record.closure.inputPrecommit", "does not bind the exact upstream authority tuple")
	}
	if closure.InputPrecommit.AuthorityID == command.OperationID.String() ||
		closure.InputPrecommit.AuthorityID == command.HandoffID.String() ||
		closure.InputPrecommit.AuthorityID == command.OutputRevisionID.String() {
		return invalid("record.closure.inputPrecommit", "authority identity aliases a server command identity")
	}
	if intent.RequestHash != record.RequestHash || intent.ClosureHash != record.ClosureHash ||
		intent.OutputRevisionID != command.OutputRevisionID.String() || intent.WorkflowInput.AuthorityID != closure.WorkflowInput.AuthorityID ||
		intent.WorkflowInput.AuthorityHash != closure.WorkflowInput.AuthorityHash || intent.Plan.AuthorityID != closure.Plan.AuthorityID ||
		intent.Plan.AuthorityHash != closure.Plan.AuthorityHash || intent.Receipt.ReceiptID != closure.Receipt.ReceiptID ||
		intent.Receipt.EnvelopeHash != closure.Receipt.EnvelopeHash || !sameTarget(intent.Target, closure.Target) {
		return invalid("record.revisionIntent", "does not bind the request and exact closure")
	}
	if consumption.OperationID != command.OperationID.String() || consumption.RequestHash != record.RequestHash ||
		consumption.ClosureHash != record.ClosureHash || consumption.RevisionIntentHash != record.RevisionIntentHash {
		return invalid("record.consumption", "does not bind the request, closure, and revision intent")
	}
	if handoff.HandoffID != command.HandoffID.String() || handoff.OperationID != command.OperationID.String() ||
		handoff.OutputRevisionID != command.OutputRevisionID.String() || handoff.WorkflowInputAuthorityID != command.WorkflowInputAuthorityID.String() ||
		handoff.PlanAuthorityID != command.PlanAuthorityID.String() || handoff.ReceiptID != record.ReceiptID ||
		handoff.RevisionIntentHash != record.RevisionIntentHash || handoff.ConsumptionHash != record.ConsumptionHash ||
		!sameTarget(handoff.Target, closure.Target) {
		return invalid("record.handoff", "does not bind the consumption and exact pending intent")
	}
	consumedAt, _ := time.Parse(canonicalTimeLayout, consumption.ConsumedAt)
	createdAt, _ := time.Parse(canonicalTimeLayout, handoff.CreatedAt)
	if !validAuthorityTime(record.ConsumedAt) || !validAuthorityTime(record.CreatedAt) ||
		!record.ConsumedAt.Equal(consumedAt) || !record.CreatedAt.Equal(createdAt) || !consumedAt.Equal(createdAt) {
		return invalid("record.time", "one exact UTC millisecond timestamp must bind consumption and handoff")
	}
	return nil
}

// StoreBundleFromRecord creates the exact transport projection shared by all
// reviewed PostgreSQL resolvers.
func StoreBundleFromRecord(record Record) (StoreBundle, error) {
	if err := ValidateRecord(record); err != nil {
		return StoreBundle{}, err
	}
	record = cloneRecord(record)
	return StoreBundle{
		Closure: StoreMaterial[PromotionClosure]{
			BytesHex: hex.EncodeToString(record.ClosureBytes), Document: record.Closure, Hash: record.ClosureHash,
		},
		Consumption: StoreConsumptionMaterial{
			BytesHex: hex.EncodeToString(record.ConsumptionBytes), ConsumedAt: record.Consumption.ConsumedAt,
			Document: record.Consumption, Hash: record.ConsumptionHash,
		},
		EvidenceEventSet: StoreMaterial[EvidenceEventSet]{
			BytesHex: hex.EncodeToString(record.EvidenceEventSetBytes), Document: record.EvidenceEventSet, Hash: record.EvidenceEventSetHash,
		},
		Handoff: StoreHandoffMaterial{
			BytesHex: hex.EncodeToString(record.HandoffBytes), CreatedAt: record.Handoff.CreatedAt, Document: record.Handoff,
			HandoffID: record.Handoff.HandoffID, Hash: record.HandoffHash, OutputRevisionID: record.Handoff.OutputRevisionID,
			State: record.Handoff.State,
		},
		OperationID: record.Command.OperationID.String(), PlanAuthorityID: record.Command.PlanAuthorityID.String(),
		ReceiptID: record.ReceiptID,
		Request: StoreMaterial[ConsumeRequest]{
			BytesHex: hex.EncodeToString(record.RequestBytes), Document: record.Request, Hash: record.RequestHash,
		},
		RevisionIntent: StoreMaterial[RevisionIntent]{
			BytesHex: hex.EncodeToString(record.RevisionIntentBytes), Document: record.RevisionIntent, Hash: record.RevisionIntentHash,
		},
		SchemaVersion: StoreBundleSchemaV2, WorkflowInputAuthorityID: record.Command.WorkflowInputAuthorityID.String(),
	}, nil
}

func ConsumeStoreBundleFromRecord(record Record) (ConsumeStoreBundle, error) {
	bundle, err := StoreBundleFromRecord(record)
	if err != nil {
		return ConsumeStoreBundle{}, err
	}
	return ConsumeStoreBundle{
		Closure: bundle.Closure, Consumption: bundle.Consumption, EvidenceEventSet: bundle.EvidenceEventSet,
		Handoff: bundle.Handoff, Idempotent: record.Idempotent, OperationID: bundle.OperationID,
		PlanAuthorityID: bundle.PlanAuthorityID, ReceiptID: bundle.ReceiptID, Request: bundle.Request,
		RevisionIntent: bundle.RevisionIntent, SchemaVersion: bundle.SchemaVersion,
		WorkflowInputAuthorityID: bundle.WorkflowInputAuthorityID,
	}, nil
}

func RecordFromStoreBundle(bundle StoreBundle) (Record, error) {
	if bundle.SchemaVersion != StoreBundleSchemaV2 || !validUUIDv4(bundle.OperationID) ||
		!validUUIDv4(bundle.WorkflowInputAuthorityID) || !validUUIDv4(bundle.PlanAuthorityID) ||
		!validStableID(bundle.ReceiptID, 256) {
		return Record{}, invalid("storeBundle", "schema or top-level identity is invalid")
	}
	requestBytes, err := decodeLowerHex("storeBundle.request.bytesHex", bundle.Request.BytesHex)
	if err != nil {
		return Record{}, err
	}
	eventSetBytes, err := decodeLowerHex("storeBundle.evidenceEventSet.bytesHex", bundle.EvidenceEventSet.BytesHex)
	if err != nil {
		return Record{}, err
	}
	closureBytes, err := decodeLowerHex("storeBundle.closure.bytesHex", bundle.Closure.BytesHex)
	if err != nil {
		return Record{}, err
	}
	intentBytes, err := decodeLowerHex("storeBundle.revisionIntent.bytesHex", bundle.RevisionIntent.BytesHex)
	if err != nil {
		return Record{}, err
	}
	consumptionBytes, err := decodeLowerHex("storeBundle.consumption.bytesHex", bundle.Consumption.BytesHex)
	if err != nil {
		return Record{}, err
	}
	handoffBytes, err := decodeLowerHex("storeBundle.handoff.bytesHex", bundle.Handoff.BytesHex)
	if err != nil {
		return Record{}, err
	}
	operationID := uuid.MustParse(bundle.OperationID)
	workflowInputID := uuid.MustParse(bundle.WorkflowInputAuthorityID)
	planID := uuid.MustParse(bundle.PlanAuthorityID)
	handoffID, handoffErr := uuid.Parse(bundle.Handoff.HandoffID)
	outputRevisionID, outputErr := uuid.Parse(bundle.Handoff.OutputRevisionID)
	consumedAt, consumedErr := time.Parse(canonicalTimeLayout, bundle.Consumption.ConsumedAt)
	createdAt, createdErr := time.Parse(canonicalTimeLayout, bundle.Handoff.CreatedAt)
	if handoffErr != nil || !validUUIDv4Value(handoffID) || outputErr != nil || !validUUIDv4Value(outputRevisionID) ||
		consumedErr != nil || createdErr != nil {
		return Record{}, invalid("storeBundle", "handoff identity or timestamp is invalid")
	}
	record := Record{
		Command: ConsumeCommand{
			OperationID: operationID, WorkflowInputAuthorityID: workflowInputID, PlanAuthorityID: planID,
			HandoffID: handoffID, OutputRevisionID: outputRevisionID,
		},
		ReceiptID: bundle.ReceiptID,
		Request:   bundle.Request.Document, RequestBytes: requestBytes, RequestHash: bundle.Request.Hash,
		EvidenceEventSet: bundle.EvidenceEventSet.Document, EvidenceEventSetBytes: eventSetBytes,
		EvidenceEventSetHash: bundle.EvidenceEventSet.Hash,
		Closure:              bundle.Closure.Document, ClosureBytes: closureBytes, ClosureHash: bundle.Closure.Hash,
		RevisionIntent: bundle.RevisionIntent.Document, RevisionIntentBytes: intentBytes, RevisionIntentHash: bundle.RevisionIntent.Hash,
		Consumption: bundle.Consumption.Document, ConsumptionBytes: consumptionBytes, ConsumptionHash: bundle.Consumption.Hash,
		Handoff: bundle.Handoff.Document, HandoffBytes: handoffBytes, HandoffHash: bundle.Handoff.Hash,
		ConsumedAt: consumedAt, CreatedAt: createdAt,
	}
	if bundle.Consumption.ConsumedAt != bundle.Consumption.Document.ConsumedAt ||
		bundle.Handoff.CreatedAt != bundle.Handoff.Document.CreatedAt || bundle.Handoff.HandoffID != bundle.Handoff.Document.HandoffID ||
		bundle.Handoff.OutputRevisionID != bundle.Handoff.Document.OutputRevisionID || bundle.Handoff.State != bundle.Handoff.Document.State {
		return Record{}, invalid("storeBundle", "wrapper scalars differ from canonical documents")
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, err
	}
	return cloneRecord(record), nil
}

func RecordFromConsumeStoreBundle(bundle ConsumeStoreBundle) (Record, error) {
	record, err := RecordFromStoreBundle(StoreBundle{
		Closure: bundle.Closure, Consumption: bundle.Consumption, EvidenceEventSet: bundle.EvidenceEventSet,
		Handoff: bundle.Handoff, OperationID: bundle.OperationID, PlanAuthorityID: bundle.PlanAuthorityID,
		ReceiptID: bundle.ReceiptID, Request: bundle.Request, RevisionIntent: bundle.RevisionIntent,
		SchemaVersion: bundle.SchemaVersion, WorkflowInputAuthorityID: bundle.WorkflowInputAuthorityID,
	})
	if err != nil {
		return Record{}, err
	}
	record.Idempotent = bundle.Idempotent
	return record, nil
}

func decodeLowerHex(field, value string) ([]byte, error) {
	if value == "" || len(value)%2 != 0 || strings.ToLower(value) != value {
		return nil, invalid(field, "must be non-empty lower-case hex")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) == 0 || len(decoded) > MaximumCanonicalBytes || hex.EncodeToString(decoded) != value {
		return nil, invalid(field, "must identify bounded exact bytes")
	}
	return decoded, nil
}

func sameTarget(left, right PromotionTargetV2) bool {
	return left == right
}

func validUUIDv4(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && validUUIDv4Value(parsed) && parsed.String() == value
}

func validUUIDv4Value(value uuid.UUID) bool {
	return value != uuid.Nil && value.Version() == 4 && value.Variant() == uuid.RFC4122
}

func validDigest(value string) bool {
	return digestPattern.MatchString(value)
}

func validStableID(value string, maximum int) bool {
	return len(value) >= 1 && len(value) <= maximum && value == strings.TrimSpace(value) && stableIDPattern.MatchString(value)
}

func validCanonicalString(value string, maximum int) bool {
	if len(value) < 1 || len(value) > maximum || value != strings.TrimSpace(value) || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validCanonicalTime(value string) bool {
	parsed, err := time.Parse(canonicalTimeLayout, value)
	return err == nil && parsed.Location() == time.UTC && parsed.Format(canonicalTimeLayout) == value && validAuthorityTime(parsed)
}

func validAuthorityTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Equal(value.UTC().Truncate(time.Millisecond)) &&
		value.Year() >= 1678 && value.Year() < 2262
}

func uniqueStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func sameImmutableRecord(left, right Record) bool {
	return left.Command == right.Command && left.ReceiptID == right.ReceiptID &&
		left.RequestHash == right.RequestHash && left.EvidenceEventSetHash == right.EvidenceEventSetHash &&
		left.ClosureHash == right.ClosureHash && left.RevisionIntentHash == right.RevisionIntentHash &&
		left.ConsumptionHash == right.ConsumptionHash && left.HandoffHash == right.HandoffHash &&
		left.ConsumedAt.Equal(right.ConsumedAt) && left.CreatedAt.Equal(right.CreatedAt) &&
		reflect.DeepEqual(left.Request, right.Request) && reflect.DeepEqual(left.EvidenceEventSet, right.EvidenceEventSet) &&
		reflect.DeepEqual(left.Closure, right.Closure) && reflect.DeepEqual(left.RevisionIntent, right.RevisionIntent) &&
		reflect.DeepEqual(left.Consumption, right.Consumption) && reflect.DeepEqual(left.Handoff, right.Handoff) &&
		bytes.Equal(left.RequestBytes, right.RequestBytes) && bytes.Equal(left.EvidenceEventSetBytes, right.EvidenceEventSetBytes) &&
		bytes.Equal(left.ClosureBytes, right.ClosureBytes) && bytes.Equal(left.RevisionIntentBytes, right.RevisionIntentBytes) &&
		bytes.Equal(left.ConsumptionBytes, right.ConsumptionBytes) && bytes.Equal(left.HandoffBytes, right.HandoffBytes)
}
