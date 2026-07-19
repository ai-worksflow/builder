package qualificationplanauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationevidence"
	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
)

type Service struct {
	inputs InputAuthority
	store  Store
}

func NewService(inputs InputAuthority, store Store) (*Service, error) {
	if isNilInterface(inputs) || isNilInterface(store) {
		return nil, fmt.Errorf("%w: input authority and immutable store are required", ErrInvalid)
	}
	return &Service{inputs: inputs, store: store}, nil
}

// Freeze resolves a complete immutable server input and compiles the only Plan
// accepted by qualificationevidence. Operation inspection deliberately occurs
// before input resolution so a replay survives input retirement or expiry.
func (service *Service) Freeze(ctx context.Context, command FreezeCommand) (Record, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.inputs) || isNilInterface(service.store) {
		return Record{}, fmt.Errorf("%w: service or dependencies are incomplete", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := validateCommand(command); err != nil {
		return Record{}, err
	}

	existing, inspectErr := service.store.InspectOperation(ctx, command.OperationID)
	if inspectErr == nil {
		return replayRecord(existing, command)
	}
	if !errors.Is(inspectErr, ErrNotFound) {
		return Record{}, fmt.Errorf("inspect qualification plan freeze operation: %w", inspectErr)
	}

	resolved, err := service.inputs.Resolve(ctx, command.InputAuthorityID)
	if err != nil {
		return Record{}, fmt.Errorf("resolve immutable qualification plan inputs: %w", err)
	}
	if err := validateResolvedInputs(resolved); err != nil {
		return Record{}, err
	}
	record, err := compileRecord(command, resolved)
	if err != nil {
		return Record{}, err
	}
	stored, err := service.store.Freeze(ctx, record)
	if errors.Is(err, ErrStoreOutcomeUnknown) {
		stored, err = service.store.InspectOperation(ctx, command.OperationID)
		if err != nil {
			return Record{}, ErrOutcomeUnknown
		}
		if !sameImmutableRecord(stored, record) {
			return Record{}, fmt.Errorf("%w: uncertain freeze resolved to different canonical bytes", ErrConflict)
		}
		stored.Idempotent = true
		return cloneRecord(stored), nil
	}
	if err != nil {
		// A concurrent exact writer may have committed between the initial
		// inspection and Freeze. Reconcile only the same operation; never turn a
		// cross-identity reservation conflict into success.
		if errors.Is(err, ErrConflict) {
			current, reconcileErr := service.store.InspectOperation(ctx, command.OperationID)
			if reconcileErr == nil && sameImmutableRecord(current, record) {
				current.Idempotent = true
				return cloneRecord(current), nil
			}
		}
		return Record{}, err
	}
	if !sameImmutableRecord(stored, record) {
		return Record{}, fmt.Errorf("%w: store returned different immutable qualification plan bytes", ErrConflict)
	}
	return cloneRecord(stored), nil
}

func (service *Service) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.store) || operationID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	record, err := service.store.InspectOperation(ctx, operationID)
	if err != nil {
		return Record{}, err
	}
	if err := validateStoredRecord(record); err != nil {
		return Record{}, err
	}
	return cloneRecord(record), nil
}

// Resolve implements qualificationevidence.PlanAuthority. The opaque value is
// the canonical server-issued authority UUID, not a browser-supplied Plan.
func (service *Service) Resolve(ctx context.Context, opaqueAuthorityID string) (qualificationevidence.PlanAuthorityResolution, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.store) {
		return qualificationevidence.PlanAuthorityResolution{}, ErrNotFound
	}
	authorityID, err := uuid.Parse(opaqueAuthorityID)
	if err != nil || authorityID.Version() != 4 || authorityID.String() != opaqueAuthorityID {
		return qualificationevidence.PlanAuthorityResolution{}, ErrNotFound
	}
	record, err := service.store.ResolveAuthority(ctx, authorityID)
	if err != nil {
		return qualificationevidence.PlanAuthorityResolution{}, err
	}
	if err := validateStoredRecord(record); err != nil {
		return qualificationevidence.PlanAuthorityResolution{}, err
	}
	wantArtifactID := QualificationPlanArtifactPrefix + opaqueAuthorityID
	if record.Envelope.ArtifactID != wantArtifactID || record.AuthorityID != authorityID {
		return qualificationevidence.PlanAuthorityResolution{}, fmt.Errorf("%w: opaque identity resolved to a different authority", ErrConflict)
	}
	return qualificationevidence.PlanAuthorityResolution{
		AuthorityID:         record.AuthorityID.String(),
		AuthorityHash:       record.EnvelopeHash,
		ArtifactID:          record.Envelope.ArtifactID,
		EvidencePlanHash:    record.EvidencePlanHash,
		EvidencePlanBytes:   append([]byte(nil), record.EvidencePlanBytes...),
		TrustBindingsDigest: record.Envelope.TrustBindingsDigest,
		Plan:                clonePlan(record.EvidencePlan),
	}, nil
}

func compileRecord(command FreezeCommand, resolved ResolvedInputs) (Record, error) {
	input := cloneInput(resolved.Input)
	planArtifactID := QualificationPlanArtifactPrefix + command.AuthorityID.String()
	templateDigest, err := digestCanonical(input.TemplateRelease)
	if err != nil {
		return Record{}, err
	}

	plan := qualificationevidence.Plan{
		SchemaVersion:               qualificationevidence.PlanSchemaV1,
		OrchestrationID:             deriveUUID(command.AuthorityID, resolved.InputHash, "orchestration").String(),
		RunID:                       deriveUUID(command.AuthorityID, resolved.InputHash, "run").String(),
		FixtureID:                   input.GoldenRuntime.FixtureID,
		QualificationPlanArtifactID: planArtifactID,
		PlanDigest:                  input.QualificationPlanDigest,
		SourceTreeDigest:            input.Source.TreeDigest,
		TemplateReleaseDigest:       templateDigest,
		Operations: qualificationevidence.OperationIDs{
			Reserve:              deriveUUID(command.AuthorityID, resolved.InputHash, "operation/reserve").String(),
			CredentialIssue:      deriveUUID(command.AuthorityID, resolved.InputHash, "operation/credential-issue").String(),
			RunClosure:           deriveUUID(command.AuthorityID, resolved.InputHash, "operation/run-closure").String(),
			KMSAttestation:       deriveUUID(command.AuthorityID, resolved.InputHash, "operation/kms-attestation").String(),
			CredentialRevocation: deriveUUID(command.AuthorityID, resolved.InputHash, "operation/credential-revocation").String(),
			ArtifactIndex:        deriveUUID(command.AuthorityID, resolved.InputHash, "operation/artifact-index").String(),
			ReceiptSign:          deriveUUID(command.AuthorityID, resolved.InputHash, "operation/receipt-sign").String(),
			SnapshotSeal:         deriveUUID(command.AuthorityID, resolved.InputHash, "operation/snapshot-seal").String(),
		},
		CredentialSet: input.Credential,
		Recipient:     input.Recipient,
		Outputs:       input.Outputs,
		Artifacts:     make([]qualificationevidence.ArtifactExpectation, len(input.Artifacts)),
	}
	for index, expected := range input.Artifacts {
		plan.Artifacts[index] = qualificationevidence.ArtifactExpectation{
			ID: expected.ID, Kind: expected.Kind, Classification: expected.Classification,
		}
		if expected.Classification == qualificationevidence.ClassificationRestricted {
			plan.Artifacts[index].EncryptionOperationID = deriveUUID(
				command.AuthorityID, resolved.InputHash, "operation/encryption/"+expected.ID,
			).String()
		}
	}
	if err := qualificationevidence.ValidatePlan(plan); err != nil {
		return Record{}, fmt.Errorf("%w: compiled evidence plan: %v", ErrInvalid, err)
	}

	request := FreezeRequest{
		AuthorityID: command.AuthorityID.String(), InputAuthorityID: command.InputAuthorityID.String(),
		OperationID: command.OperationID.String(), SchemaVersion: FreezeRequestSchemaV1,
	}
	requestBytes, requestHash, err := canonicalMaterial(request)
	if err != nil {
		return Record{}, err
	}
	evidencePlanBytes, evidencePlanHash, err := canonicalMaterial(plan)
	if err != nil {
		return Record{}, err
	}
	trust := TrustDocument{SchemaVersion: TrustSchemaV1, TrustBindings: input.TrustBindings, TrustPolicyDigest: input.TrustPolicyDigest}
	trustBytes, trustHash, err := canonicalMaterial(trust)
	if err != nil {
		return Record{}, err
	}
	trustBindingsBytes, err := canonicalJSON(input.TrustBindings)
	if err != nil {
		return Record{}, err
	}
	trustBindingsDigest := sha256Digest(trustBindingsBytes)
	target := TargetDocument{PromotionTarget: input.PromotionTarget, SchemaVersion: TargetSchemaV1}
	targetBytes, targetHash, err := canonicalMaterial(target)
	if err != nil {
		return Record{}, err
	}
	envelope := AuthorityEnvelope{
		ArtifactID: planArtifactID, AuthorityID: command.AuthorityID.String(), EvidencePlanHash: evidencePlanHash,
		InputAuthorityID: command.InputAuthorityID.String(), InputHash: resolved.InputHash,
		ManifestPlanDigest: input.QualificationPlanDigest, OperationID: command.OperationID.String(),
		ProjectionHash: input.QualificationPlanDigest, SchemaVersion: AuthoritySchemaV1,
		TargetHash: targetHash, TrustBindingsDigest: trustBindingsDigest, TrustHash: trustHash,
	}
	envelopeBytes, envelopeHash, err := canonicalMaterial(envelope)
	if err != nil {
		return Record{}, err
	}
	if evidencePlanHash == input.QualificationPlanDigest || envelopeHash == evidencePlanHash || envelopeHash == input.QualificationPlanDigest {
		return Record{}, fmt.Errorf("%w: manifest, evidence, and authority digest domains are confused", ErrInvalid)
	}

	record := Record{
		OperationID: command.OperationID, AuthorityID: command.AuthorityID, InputAuthorityID: command.InputAuthorityID,
		RequestHash: requestHash, RequestBytes: requestBytes, Request: request,
		InputHash: resolved.InputHash, InputBytes: append([]byte(nil), resolved.InputBytes...), Input: input,
		ProjectionHash: input.QualificationPlanDigest, ProjectionBytes: append([]byte(nil), resolved.QualificationPlanBytes...),
		ProjectionDocument: append(json.RawMessage(nil), resolved.QualificationPlanBytes...),
		EvidencePlanHash:   evidencePlanHash, EvidencePlanBytes: evidencePlanBytes, EvidencePlan: plan,
		TrustHash: trustHash, TrustBytes: trustBytes, Trust: trust,
		TargetHash: targetHash, TargetBytes: targetBytes, Target: target,
		EnvelopeHash: envelopeHash, EnvelopeBytes: envelopeBytes, Envelope: envelope,
	}
	if err := validateRecordMaterials(record, false); err != nil {
		return Record{}, err
	}
	return record, nil
}

func replayRecord(record Record, command FreezeCommand) (Record, error) {
	if err := validateStoredRecord(record); err != nil {
		return Record{}, err
	}
	if record.OperationID != command.OperationID || record.AuthorityID != command.AuthorityID ||
		record.InputAuthorityID != command.InputAuthorityID {
		return Record{}, fmt.Errorf("%w: operation is bound to different freeze command bytes", ErrConflict)
	}
	record.Idempotent = true
	return cloneRecord(record), nil
}

func validateCommand(command FreezeCommand) error {
	if command.OperationID.Version() != 4 || command.AuthorityID.Version() != 4 || command.InputAuthorityID.Version() != 4 ||
		command.OperationID == command.AuthorityID || command.OperationID == command.InputAuthorityID || command.AuthorityID == command.InputAuthorityID {
		return fmt.Errorf("%w: operation, authority, and input authority must be distinct UUIDv4 identities", ErrInvalid)
	}
	return nil
}

func validateResolvedInputs(resolved ResolvedInputs) error {
	input := resolved.Input
	canonicalInput, err := canonicalJSON(input)
	if err != nil || !bytes.Equal(canonicalInput, resolved.InputBytes) || sha256Digest(resolved.InputBytes) != resolved.InputHash {
		return fmt.Errorf("%w: input raw bytes/hash do not exactly encode the resolved input", ErrInvalid)
	}
	canonicalProjection, err := canonicalRawJSON(resolved.QualificationPlanBytes)
	if err != nil || !bytes.Equal(canonicalProjection, resolved.QualificationPlanBytes) ||
		sha256Digest(resolved.QualificationPlanBytes) != input.QualificationPlanDigest {
		return fmt.Errorf("%w: qualification plan projection bytes do not match manifest PlanDigest", ErrInvalid)
	}
	var projection map[string]any
	if err := json.Unmarshal(resolved.QualificationPlanBytes, &projection); err != nil ||
		validateQualificationProjectionRoot(projection, input.PromotionTarget.Subject) != nil {
		return fmt.Errorf("%w: qualification plan projection schema or subject is inconsistent", ErrInvalid)
	}
	if input.SchemaVersion != InputSchemaV1 || !validDigest(resolved.InputHash) || !validDigest(input.QualificationPlanDigest) ||
		!validDigest(input.TrustPolicyDigest) {
		return fmt.Errorf("%w: input root schema or digest is invalid", ErrInvalid)
	}
	if err := validateTarget(input.PromotionTarget); err != nil || errValidateSource(input.Source) != nil ||
		errValidateTemplate(input.TemplateRelease) != nil || errValidateGolden(input.GoldenRuntime) != nil {
		return fmt.Errorf("%w: target, source, template release, or Golden binding is invalid", ErrInvalid)
	}
	if err := validateArtifactRevision(input.QualificationManifest); err != nil ||
		input.QualificationManifest.ContentHash == input.QualificationPlanDigest ||
		validateContentBinding(input.BuildManifest) != nil || validateContentBinding(input.BuildContract) != nil ||
		input.BuildManifest == input.BuildContract {
		return fmt.Errorf("%w: immutable manifest/build bindings are invalid or aliased", ErrInvalid)
	}
	if input.ArtifactPolicy.MaximumArtifacts != qualificationevidence.MaximumArtifacts ||
		!input.ArtifactPolicy.RequireRestrictedEncryption || !input.ArtifactPolicy.RequireTrace || !input.ArtifactPolicy.RequireVideo ||
		input.OutputPolicy.SnapshotMode != qualificationevidence.ImmutableSnapshotMode ||
		input.OutputPolicy.CredentialRevocation != CredentialRevocationPolicyV1 ||
		input.OutputPolicy.PlaintextDisposition != PlaintextDispositionPolicyV1 {
		return fmt.Errorf("%w: evidence artifact/output policy is not fail closed", ErrInvalid)
	}
	if len(input.Artifacts) < 1 || len(input.Artifacts) > qualificationevidence.MaximumArtifacts {
		return fmt.Errorf("%w: artifact expectation count is invalid", ErrInvalid)
	}
	for index, artifact := range input.Artifacts {
		if !validStableID(artifact.ID) || (index > 0 && input.Artifacts[index-1].ID >= artifact.ID) {
			return fmt.Errorf("%w: artifact expectations are not strictly sorted unique IDs", ErrInvalid)
		}
	}
	if err := validateTrust(input.TrustBindings); err != nil || input.Credential.Issuer != input.TrustBindings.CredentialAuthorityID {
		return fmt.Errorf("%w: trust roles or credential issuer are inconsistent", ErrInvalid)
	}
	if !validUUIDv4(input.GoldenRuntime.FixtureID) || !validUUIDv4(input.Credential.SetID) ||
		input.GoldenRuntime.FixtureID == input.Credential.SetID {
		return fmt.Errorf("%w: fixture and credential-set identities form a cycle", ErrInvalid)
	}
	return nil
}

func validateQualificationProjectionRoot(projection map[string]any, expectedSubject string) error {
	exactKeys := []string{
		"manifestSchemaVersion", "policy", "schemaVersion", "sourceDocuments", "subject", "suites", "supportFiles",
	}
	if len(projection) != len(exactKeys) {
		return ErrInvalid
	}
	for _, key := range exactKeys {
		if _, exists := projection[key]; !exists {
			return ErrInvalid
		}
	}
	if projection["schemaVersion"] != "worksflow-qualification-plan/v1" ||
		projection["manifestSchemaVersion"] != qualificationreceipt.QualificationManifestSchemaV1 ||
		projection["subject"] != expectedSubject {
		return ErrInvalid
	}
	if _, ok := projection["policy"].(map[string]any); !ok {
		return ErrInvalid
	}
	for _, key := range []string{"sourceDocuments", "suites", "supportFiles"} {
		values, ok := projection[key].([]any)
		if !ok || len(values) == 0 {
			return ErrInvalid
		}
	}
	return nil
}

func validateTarget(target qualificationreceipt.PromotionTarget) error {
	if !validUUIDv4(target.ProjectID) || !validUUIDv4(target.WorkflowRunID) || !validStableID(target.NodeKey) ||
		!validUUIDv4(target.TargetRevision.ID) || !validDigest(target.TargetRevision.ContentHash) ||
		!validCanonicalString(target.Subject, 256) || target.StageGate != qualificationreceipt.ExternalQualificationGate {
		return ErrInvalid
	}
	return nil
}

func errValidateSource(source qualificationreceipt.SourceBinding) error {
	if !commitPattern.MatchString(source.Commit) || source.TreeDigestSchema != qualificationreceipt.SourceContentTreeCommitmentSchemaV1 ||
		!validDigest(source.TreeDigest) || source.Dirty {
		return ErrInvalid
	}
	return nil
}

func errValidateTemplate(binding qualificationreceipt.TemplateReleaseBinding) error {
	if !validUUIDv4(binding.ID) || !validDigest(binding.ContentHash) || !validDigest(binding.ApprovalReceiptDigest) ||
		binding.ContentHash == binding.ApprovalReceiptDigest {
		return ErrInvalid
	}
	return nil
}

func errValidateGolden(binding qualificationreceipt.GoldenRuntimeBinding) error {
	if !validStableID(binding.AuthorityDocumentArtifactID) || !validDigest(binding.AuthorityDocumentDigest) ||
		!validStableID(binding.FixtureDocumentArtifactID) || !validDigest(binding.FixtureDocumentDigest) ||
		binding.AuthorityDocumentArtifactID == binding.FixtureDocumentArtifactID ||
		binding.AuthorityDocumentDigest == binding.FixtureDocumentDigest || !validUUIDv4(binding.FixtureID) ||
		binding.FaultOperationSetDigest != qualificationreceipt.GoldenFaultOperationSetDigestV1 {
		return ErrInvalid
	}
	return nil
}

func validateArtifactRevision(binding ArtifactRevisionBinding) error {
	if !validStableID(binding.ArtifactID) || !validUUIDv4(binding.RevisionID) || !validDigest(binding.ContentHash) {
		return ErrInvalid
	}
	return nil
}

func validateContentBinding(binding ImmutableContentBinding) error {
	if !validStableID(binding.ID) || !validDigest(binding.ContentHash) {
		return ErrInvalid
	}
	return nil
}

func validateTrust(trust qualificationevidence.TrustBindings) error {
	identities := []string{
		trust.CaptureAuthorityID, trust.CredentialAuthorityID, trust.EncryptionAuthorityID, trust.IndexerAuthorityID,
		trust.KMSAuthorityID, trust.ReceiptAuthorityID, trust.SealerAuthorityID, trust.VerifierAuthorityID,
	}
	seen := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		if !validIdentity(identity) {
			return ErrInvalid
		}
		if _, duplicate := seen[identity]; duplicate {
			return ErrInvalid
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func validateStoredRecord(record Record) error {
	return validateRecordMaterials(record, true)
}

func validateRecordMaterials(record Record, requireFrozen bool) error {
	if validateCommand(FreezeCommand{OperationID: record.OperationID, AuthorityID: record.AuthorityID, InputAuthorityID: record.InputAuthorityID}) != nil {
		return fmt.Errorf("%w: stored root identities are invalid", ErrConflict)
	}
	if requireFrozen && (record.FrozenAt.IsZero() || record.FrozenAt.Location() != timeUTC || record.FrozenAt.Nanosecond()%1_000_000 != 0) {
		return fmt.Errorf("%w: store frozenAt is not canonical UTC milliseconds", ErrConflict)
	}
	if !requireFrozen && !record.FrozenAt.IsZero() {
		return fmt.Errorf("%w: service attempted to assign store frozenAt", ErrInvalid)
	}
	if err := validateUniqueRecordUUIDs(record); err != nil {
		return fmt.Errorf("%w: qualification plan UUID closure is not unique", ErrConflict)
	}
	checks := []struct {
		value any
		bytes []byte
		hash  string
	}{
		{record.Request, record.RequestBytes, record.RequestHash},
		{record.Input, record.InputBytes, record.InputHash},
		{record.EvidencePlan, record.EvidencePlanBytes, record.EvidencePlanHash},
		{record.Trust, record.TrustBytes, record.TrustHash},
		{record.Target, record.TargetBytes, record.TargetHash},
		{record.Envelope, record.EnvelopeBytes, record.EnvelopeHash},
	}
	for _, check := range checks {
		canonical, err := canonicalJSON(check.value)
		if err != nil || !bytes.Equal(canonical, check.bytes) || sha256Digest(check.bytes) != check.hash {
			return fmt.Errorf("%w: stored canonical bytes/hash drifted", ErrConflict)
		}
	}
	projection, err := canonicalRawJSON(record.ProjectionBytes)
	if err != nil || !bytes.Equal(projection, record.ProjectionBytes) || !bytes.Equal(record.ProjectionDocument, record.ProjectionBytes) ||
		sha256Digest(record.ProjectionBytes) != record.ProjectionHash {
		return fmt.Errorf("%w: stored manifest plan projection drifted", ErrConflict)
	}
	if qualificationevidence.ValidatePlan(record.EvidencePlan) != nil ||
		record.Request.SchemaVersion != FreezeRequestSchemaV1 || record.Input.SchemaVersion != InputSchemaV1 ||
		record.Trust.SchemaVersion != TrustSchemaV1 || record.Target.SchemaVersion != TargetSchemaV1 ||
		record.Envelope.SchemaVersion != AuthoritySchemaV1 ||
		record.Request.OperationID != record.OperationID.String() || record.Request.AuthorityID != record.AuthorityID.String() ||
		record.Request.InputAuthorityID != record.InputAuthorityID.String() ||
		record.Envelope.OperationID != record.OperationID.String() || record.Envelope.AuthorityID != record.AuthorityID.String() ||
		record.Envelope.InputAuthorityID != record.InputAuthorityID.String() ||
		record.Envelope.ArtifactID != QualificationPlanArtifactPrefix+record.AuthorityID.String() ||
		record.EvidencePlan.QualificationPlanArtifactID != record.Envelope.ArtifactID ||
		record.Envelope.EvidencePlanHash != record.EvidencePlanHash || record.Envelope.InputHash != record.InputHash ||
		record.Envelope.ManifestPlanDigest != record.Input.QualificationPlanDigest ||
		record.Envelope.ProjectionHash != record.ProjectionHash || record.ProjectionHash != record.Input.QualificationPlanDigest ||
		record.Envelope.TargetHash != record.TargetHash || record.Envelope.TrustHash != record.TrustHash ||
		record.Target.PromotionTarget != record.Input.PromotionTarget || record.Trust.TrustBindings != record.Input.TrustBindings ||
		record.Trust.TrustPolicyDigest != record.Input.TrustPolicyDigest {
		return fmt.Errorf("%w: stored qualification plan cross-binding drifted", ErrConflict)
	}
	resolved := ResolvedInputs{
		Input: record.Input, InputHash: record.InputHash, InputBytes: record.InputBytes,
		QualificationPlanBytes: record.ProjectionBytes,
	}
	if validateResolvedInputs(resolved) != nil || !inputMatchesEvidencePlan(record.Input, record.EvidencePlan) {
		return fmt.Errorf("%w: stored input/evidence plan projection is inconsistent", ErrConflict)
	}
	if !hasDeterministicPlanIdentities(record) {
		return fmt.Errorf("%w: stored evidence plan deterministic identity closure drifted", ErrConflict)
	}
	trustBytes, _ := canonicalJSON(record.Input.TrustBindings)
	if record.Envelope.TrustBindingsDigest != sha256Digest(trustBytes) ||
		record.EvidencePlanHash == record.ProjectionHash || record.EnvelopeHash == record.EvidencePlanHash || record.EnvelopeHash == record.ProjectionHash {
		return fmt.Errorf("%w: stored authority digest domains are invalid", ErrConflict)
	}
	return nil
}

func hasDeterministicPlanIdentities(record Record) bool {
	plan := record.EvidencePlan
	authorityID := record.AuthorityID
	inputHash := record.InputHash
	if plan.OrchestrationID != deriveUUID(authorityID, inputHash, "orchestration").String() ||
		plan.RunID != deriveUUID(authorityID, inputHash, "run").String() {
		return false
	}
	expectedOperations := qualificationevidence.OperationIDs{
		Reserve:              deriveUUID(authorityID, inputHash, "operation/reserve").String(),
		CredentialIssue:      deriveUUID(authorityID, inputHash, "operation/credential-issue").String(),
		RunClosure:           deriveUUID(authorityID, inputHash, "operation/run-closure").String(),
		KMSAttestation:       deriveUUID(authorityID, inputHash, "operation/kms-attestation").String(),
		CredentialRevocation: deriveUUID(authorityID, inputHash, "operation/credential-revocation").String(),
		ArtifactIndex:        deriveUUID(authorityID, inputHash, "operation/artifact-index").String(),
		ReceiptSign:          deriveUUID(authorityID, inputHash, "operation/receipt-sign").String(),
		SnapshotSeal:         deriveUUID(authorityID, inputHash, "operation/snapshot-seal").String(),
	}
	if plan.Operations != expectedOperations {
		return false
	}
	for _, artifact := range plan.Artifacts {
		expected := ""
		if artifact.Classification == qualificationevidence.ClassificationRestricted {
			expected = deriveUUID(authorityID, inputHash, "operation/encryption/"+artifact.ID).String()
		}
		if artifact.EncryptionOperationID != expected {
			return false
		}
	}
	return true
}

func inputMatchesEvidencePlan(input ResolvedInputDocument, plan qualificationevidence.Plan) bool {
	if plan.FixtureID != input.GoldenRuntime.FixtureID || plan.PlanDigest != input.QualificationPlanDigest ||
		plan.SourceTreeDigest != input.Source.TreeDigest || plan.CredentialSet != input.Credential ||
		plan.Recipient != input.Recipient || plan.Outputs != input.Outputs || len(plan.Artifacts) != len(input.Artifacts) {
		return false
	}
	templateDigest, err := digestCanonical(input.TemplateRelease)
	if err != nil || plan.TemplateReleaseDigest != templateDigest {
		return false
	}
	for index, artifact := range input.Artifacts {
		compiled := plan.Artifacts[index]
		if compiled.ID != artifact.ID || compiled.Kind != artifact.Kind || compiled.Classification != artifact.Classification {
			return false
		}
		if artifact.Classification == qualificationevidence.ClassificationRestricted {
			if !validUUIDv4(compiled.EncryptionOperationID) {
				return false
			}
		} else if compiled.EncryptionOperationID != "" {
			return false
		}
	}
	return true
}

func canonicalMaterial(value any) ([]byte, string, error) {
	encoded, err := canonicalJSON(value)
	if err != nil {
		return nil, "", err
	}
	return encoded, sha256Digest(encoded), nil
}

func digestCanonical(value any) (string, error) {
	encoded, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	return sha256Digest(encoded), nil
}

func deriveUUID(authorityID uuid.UUID, inputHash, label string) uuid.UUID {
	digest := sha256.Sum256([]byte("worksflow-qualification-plan-authority-id/v1\x00" + authorityID.String() + "\x00" + inputHash + "\x00" + label))
	var value uuid.UUID
	copy(value[:], digest[:16])
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return value
}

func recordUUIDs(record Record) []uuid.UUID {
	values := []uuid.UUID{record.OperationID, record.AuthorityID, record.InputAuthorityID}
	plan := record.EvidencePlan
	for _, text := range []string{
		plan.OrchestrationID, plan.RunID, plan.FixtureID, plan.CredentialSet.SetID,
		plan.Operations.Reserve, plan.Operations.CredentialIssue, plan.Operations.RunClosure,
		plan.Operations.KMSAttestation, plan.Operations.CredentialRevocation,
		plan.Operations.ArtifactIndex, plan.Operations.ReceiptSign, plan.Operations.SnapshotSeal,
	} {
		parsed, _ := uuid.Parse(text)
		values = append(values, parsed)
	}
	for _, artifact := range plan.Artifacts {
		if artifact.EncryptionOperationID != "" {
			parsed, _ := uuid.Parse(artifact.EncryptionOperationID)
			values = append(values, parsed)
		}
	}
	return values
}

// reservedRecordUUIDs excludes upstream stable references. In particular a
// Golden FixtureID can intentionally be reused by independent qualification
// runs even though it must remain distinct from identities inside each Plan.
func reservedRecordUUIDs(record Record) []uuid.UUID {
	all := recordUUIDs(record)
	fixtureID, _ := uuid.Parse(record.EvidencePlan.FixtureID)
	owned := make([]uuid.UUID, 0, len(all)-1)
	for _, identity := range all {
		if identity != fixtureID {
			owned = append(owned, identity)
		}
	}
	return owned
}

func validateUniqueRecordUUIDs(record Record) error {
	seen := make(map[uuid.UUID]struct{})
	for _, value := range recordUUIDs(record) {
		if value == uuid.Nil || value.Version() != 4 {
			return ErrInvalid
		}
		if _, duplicate := seen[value]; duplicate {
			return ErrConflict
		}
		seen[value] = struct{}{}
	}
	return nil
}

func sameImmutableRecord(left, right Record) bool {
	left.FrozenAt = right.FrozenAt
	left.Idempotent, right.Idempotent = false, false
	return reflect.DeepEqual(left, right)
}

func cloneInput(input ResolvedInputDocument) ResolvedInputDocument {
	cloned := input
	cloned.Artifacts = append([]ArtifactExpectation(nil), input.Artifacts...)
	return cloned
}

func clonePlan(plan qualificationevidence.Plan) qualificationevidence.Plan {
	cloned := plan
	cloned.Artifacts = append([]qualificationevidence.ArtifactExpectation(nil), plan.Artifacts...)
	return cloned
}

func cloneRecord(record Record) Record {
	cloned := record
	cloned.RequestBytes = append([]byte(nil), record.RequestBytes...)
	cloned.InputBytes = append([]byte(nil), record.InputBytes...)
	cloned.Input = cloneInput(record.Input)
	cloned.ProjectionBytes = append([]byte(nil), record.ProjectionBytes...)
	cloned.ProjectionDocument = append(json.RawMessage(nil), record.ProjectionDocument...)
	cloned.EvidencePlanBytes = append([]byte(nil), record.EvidencePlanBytes...)
	cloned.EvidencePlan = clonePlan(record.EvidencePlan)
	cloned.TrustBytes = append([]byte(nil), record.TrustBytes...)
	cloned.TargetBytes = append([]byte(nil), record.TargetBytes...)
	cloned.EnvelopeBytes = append([]byte(nil), record.EnvelopeBytes...)
	return cloned
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var timeUTC = time.UTC

var _ qualificationevidence.PlanAuthority = (*Service)(nil)
