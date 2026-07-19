package qualificationevidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	canonicalTimeLayout = "2006-01-02T15:04:05.000Z"
	maximumSafeInteger  = int64(9007199254740991)
)

var (
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	stableIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	identityPattern = regexp.MustCompile(`^(?:spiffe://[a-z0-9.-]+/[a-z0-9._/-]+|[a-z0-9]+(?:[._:-][a-z0-9]+)*)$`)
	audiencePattern = regexp.MustCompile(`^(?:urn:[a-z0-9][a-z0-9:._-]+|[a-z0-9]+(?:[._:-][a-z0-9]+)*)$`)
)

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

func validUUIDv4(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.Version() == 4 && parsed.String() == value
}

func validDigest(value string) bool { return digestPattern.MatchString(value) }

func validStableID(value string) bool {
	return len(value) > 0 && len(value) <= 128 && stableIDPattern.MatchString(value)
}

func validIdentity(value string) bool {
	return len(value) > 0 && len(value) <= 512 && identityPattern.MatchString(value)
}

func validAudience(value string) bool {
	return len(value) > 0 && len(value) <= 512 && audiencePattern.MatchString(value)
}

func canonicalTime(value time.Time) (string, error) {
	if value.IsZero() || value.Nanosecond()%int(time.Millisecond) != 0 {
		return "", errors.New("trusted time must have millisecond precision")
	}
	return value.UTC().Format(canonicalTimeLayout), nil
}

func parseCanonicalTime(value string) (time.Time, error) {
	parsed, err := time.Parse(canonicalTimeLayout, value)
	if err != nil || parsed.Format(canonicalTimeLayout) != value {
		return time.Time{}, errors.New("time is not canonical UTC milliseconds")
	}
	return parsed, nil
}

// CanonicalJSON produces the package's hash authority: UTF-8 JSON objects with
// lexicographically sorted keys, no floats, and only cross-language safe
// integers. Domain structs cannot contribute unknown or duplicate fields.
func CanonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: encode JSON: %v", ErrInvalid, err)
	}
	if !utf8.Valid(encoded) {
		return nil, fmt.Errorf("%w: JSON is not UTF-8", ErrInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, fmt.Errorf("%w: decode JSON: %v", ErrInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: JSON has trailing data", ErrInvalid)
	}
	if err := validateCanonicalValue(generic); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(generic)
	if err != nil {
		return nil, fmt.Errorf("%w: canonical JSON encode failed: %v", ErrInvalid, err)
	}
	return canonical, nil
}

func validateCanonicalValue(value any) error {
	switch typed := value.(type) {
	case nil, bool:
		return nil
	case string:
		if !utf8.ValidString(typed) || strings.ContainsRune(typed, '\x00') {
			return fmt.Errorf("%w: JSON string is not canonical", ErrInvalid)
		}
		return nil
	case json.Number:
		text := typed.String()
		if strings.ContainsAny(text, ".eE+") || (strings.HasPrefix(text, "-") && text == "-0") {
			return fmt.Errorf("%w: floats and non-canonical numbers are forbidden", ErrInvalid)
		}
		integer, err := strconv.ParseInt(text, 10, 64)
		if err != nil || integer < -maximumSafeInteger || integer > maximumSafeInteger {
			return fmt.Errorf("%w: integer is outside the safe canonical range", ErrInvalid)
		}
		return nil
	case []any:
		for _, element := range typed {
			if err := validateCanonicalValue(element); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for key, element := range typed {
			if key == "" || !utf8.ValidString(key) || strings.ContainsRune(key, '\x00') {
				return fmt.Errorf("%w: JSON object name is not canonical", ErrInvalid)
			}
			if err := validateCanonicalValue(element); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported JSON value", ErrInvalid)
	}
}

func CanonicalDigest(value any) (string, error) {
	encoded, err := CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	return sha256Digest(encoded), nil
}

func sha256Digest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func ValidatePlan(plan Plan) error {
	if plan.SchemaVersion != PlanSchemaV1 || !validUUIDv4(plan.OrchestrationID) || !validUUIDv4(plan.RunID) ||
		!validUUIDv4(plan.FixtureID) || !validStableID(plan.QualificationPlanArtifactID) ||
		!validDigest(plan.PlanDigest) || !validDigest(plan.SourceTreeDigest) ||
		!validDigest(plan.TemplateReleaseDigest) {
		return fmt.Errorf("%w: plan root identity is invalid", ErrInvalid)
	}
	rootIDs := map[string]struct{}{plan.OrchestrationID: {}, plan.RunID: {}, plan.FixtureID: {}}
	if len(rootIDs) != 3 {
		return fmt.Errorf("%w: orchestration, run, and fixture IDs must be distinct", ErrInvalid)
	}
	if err := validateCredentialExpectation(plan.CredentialSet); err != nil {
		return err
	}
	rootIDs[plan.CredentialSet.SetID] = struct{}{}
	if len(rootIDs) != 4 {
		return fmt.Errorf("%w: orchestration, run, fixture, and credential-set IDs must be distinct", ErrInvalid)
	}
	if !validStableID(plan.Recipient.KeyResourceID) || !validStableID(plan.Recipient.KeyVersion) {
		return fmt.Errorf("%w: KMS recipient is invalid", ErrInvalid)
	}
	outputs := []string{
		plan.Outputs.KMSAttestationArtifactID, plan.Outputs.ArtifactIndexID,
		plan.Outputs.ReceiptID, plan.Outputs.SnapshotID,
		plan.CredentialSet.IssuanceArtifactID, plan.CredentialSet.RevocationArtifactID,
	}
	seenOutput := make(map[string]struct{}, len(outputs))
	for _, output := range outputs {
		if !validStableID(output) {
			return fmt.Errorf("%w: output artifact identity is invalid", ErrInvalid)
		}
		if _, exists := seenOutput[output]; exists {
			return fmt.Errorf("%w: output artifact identities must be unique", ErrInvalid)
		}
		seenOutput[output] = struct{}{}
	}
	operationIDs := []string{
		plan.Operations.Reserve, plan.Operations.CredentialIssue, plan.Operations.RunClosure,
		plan.Operations.KMSAttestation, plan.Operations.CredentialRevocation,
		plan.Operations.ArtifactIndex, plan.Operations.ReceiptSign, plan.Operations.SnapshotSeal,
	}
	seenOperations := make(map[string]struct{}, len(operationIDs)+len(plan.Artifacts)+len(rootIDs))
	for identity := range rootIDs {
		seenOperations[identity] = struct{}{}
	}
	for _, operationID := range operationIDs {
		if !validUUIDv4(operationID) {
			return fmt.Errorf("%w: operation ID is invalid", ErrInvalid)
		}
		if _, duplicate := seenOperations[operationID]; duplicate {
			return fmt.Errorf("%w: operation IDs must be globally unique", ErrInvalid)
		}
		seenOperations[operationID] = struct{}{}
	}
	if len(plan.Artifacts) < 1 || len(plan.Artifacts) > MaximumArtifacts {
		return fmt.Errorf("%w: artifact plan must contain 1..%d entries", ErrInvalid, MaximumArtifacts)
	}
	traceCount, videoCount, restrictedCount := 0, 0, 0
	seenArtifacts := make(map[string]struct{}, len(plan.Artifacts))
	for index, artifact := range plan.Artifacts {
		if !validStableID(artifact.ID) || !validArtifactKind(artifact.Kind) || !validClassificationForKind(artifact.Kind, artifact.Classification) {
			return fmt.Errorf("%w: artifact expectation %d is invalid", ErrInvalid, index)
		}
		if index > 0 && bytes.Compare([]byte(plan.Artifacts[index-1].ID), []byte(artifact.ID)) >= 0 {
			return fmt.Errorf("%w: artifact expectations must be strictly sorted", ErrInvalid)
		}
		if _, duplicate := seenArtifacts[artifact.ID]; duplicate {
			return fmt.Errorf("%w: artifact identity is duplicated", ErrInvalid)
		}
		if _, collision := seenOutput[artifact.ID]; collision {
			return fmt.Errorf("%w: captured and generated artifact identities collide", ErrInvalid)
		}
		seenArtifacts[artifact.ID] = struct{}{}
		if artifact.Classification == ClassificationRestricted {
			restrictedCount++
			if !validUUIDv4(artifact.EncryptionOperationID) {
				return fmt.Errorf("%w: restricted artifact has no encryption operation", ErrInvalid)
			}
			if _, duplicate := seenOperations[artifact.EncryptionOperationID]; duplicate {
				return fmt.Errorf("%w: encryption operation IDs must be globally unique", ErrInvalid)
			}
			seenOperations[artifact.EncryptionOperationID] = struct{}{}
		} else if artifact.EncryptionOperationID != "" {
			return fmt.Errorf("%w: distributable artifact cannot reserve encryption", ErrInvalid)
		}
		if artifact.Kind == ArtifactKindTrace {
			traceCount++
		}
		if artifact.Kind == ArtifactKindVideo {
			videoCount++
		}
	}
	if restrictedCount == 0 || traceCount == 0 || videoCount == 0 {
		return fmt.Errorf("%w: qualification requires restricted evidence including trace and video", ErrInvalid)
	}
	return nil
}

func validateCredentialExpectation(value CredentialExpectation) error {
	if !validUUIDv4(value.SetID) || !validIdentity(value.Issuer) || !validAudience(value.Audience) ||
		!validDigest(value.SetHandleHash) || !validDigest(value.MemberBindingsDigest) ||
		value.SetHandleHash == value.MemberBindingsDigest || value.MemberCount < 1 || value.MemberCount > MaximumMembers ||
		!validStableID(value.IssuanceArtifactID) || !validStableID(value.RevocationArtifactID) ||
		value.IssuanceArtifactID == value.RevocationArtifactID {
		return fmt.Errorf("%w: credential expectation is invalid", ErrInvalid)
	}
	return nil
}

func validArtifactKind(kind ArtifactKind) bool {
	switch kind {
	case ArtifactKindRunResult, ArtifactKindTrace, ArtifactKindVideo, ArtifactKindLog,
		ArtifactKindGolden, ArtifactKindFault, ArtifactKindWriterDrain, ArtifactKindRuntimeProof:
		return true
	default:
		return false
	}
}

func validClassificationForKind(kind ArtifactKind, classification Classification) bool {
	switch kind {
	case ArtifactKindTrace, ArtifactKindVideo, ArtifactKindLog:
		return classification == ClassificationRestricted
	default:
		return classification == ClassificationDistributable
	}
}

func ValidateCredentialBinding(binding CredentialSetBinding, plan Plan) error {
	expected := plan.CredentialSet
	if binding.SetID != expected.SetID || binding.RunID != plan.RunID || binding.FixtureID != plan.FixtureID ||
		binding.Issuer != expected.Issuer || binding.Audience != expected.Audience ||
		binding.SetHandleHash != expected.SetHandleHash || binding.MemberBindingsDigest != expected.MemberBindingsDigest ||
		binding.MemberCount != expected.MemberCount || binding.MemberCount != len(binding.Members) ||
		binding.MemberCount < 1 || binding.MemberCount > MaximumMembers {
		return ErrCredentialDrift
	}
	issuedAt, issueErr := parseCanonicalTime(binding.IssuedAt)
	expiresAt, expiryErr := parseCanonicalTime(binding.ExpiresAt)
	if issueErr != nil || expiryErr != nil || !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > 30*time.Minute {
		return fmt.Errorf("%w: credential lifetime is invalid", ErrCredentialDrift)
	}
	seenSlots := make(map[string]struct{}, len(binding.Members))
	seenHandles := make(map[string]struct{}, len(binding.Members))
	for index, member := range binding.Members {
		if !validStableID(member.Slot) || !validUUIDv4(member.ActorID) ||
			(member.Kind != "token" && member.Kind != "storage-state") || !validDigest(member.CredentialHandleHash) ||
			member.CredentialHandleHash == binding.SetHandleHash {
			return fmt.Errorf("%w: credential member %d is invalid", ErrCredentialDrift, index)
		}
		if _, duplicate := seenSlots[member.Slot]; duplicate {
			return fmt.Errorf("%w: credential member slot is duplicated", ErrCredentialDrift)
		}
		if _, duplicate := seenHandles[member.CredentialHandleHash]; duplicate {
			return fmt.Errorf("%w: credential member handle is duplicated", ErrCredentialDrift)
		}
		seenSlots[member.Slot] = struct{}{}
		seenHandles[member.CredentialHandleHash] = struct{}{}
		if index > 0 && !credentialMemberLess(binding.Members[index-1], member) {
			return fmt.Errorf("%w: credential members are not strictly sorted", ErrCredentialDrift)
		}
	}
	digest, err := credentialMemberDigest(binding.Members)
	if err != nil || digest != binding.MemberBindingsDigest {
		return fmt.Errorf("%w: credential member digest does not match", ErrCredentialDrift)
	}
	return nil
}

func credentialMemberLess(left, right CredentialMember) bool {
	leftValues := [...]string{left.Slot, left.ActorID, left.Kind, left.CredentialHandleHash}
	rightValues := [...]string{right.Slot, right.ActorID, right.Kind, right.CredentialHandleHash}
	for index := range leftValues {
		comparison := bytes.Compare([]byte(leftValues[index]), []byte(rightValues[index]))
		if comparison != 0 {
			return comparison < 0
		}
	}
	return false
}

func credentialMemberDigest(members []CredentialMember) (string, error) {
	projection := struct {
		Members       []CredentialMember `json:"members"`
		SchemaVersion string             `json:"schemaVersion"`
	}{Members: cloneCredentialMembers(members), SchemaVersion: CredentialMemberBindingsSchema}
	return CanonicalDigest(projection)
}

func validateSignedArtifact(value SignedArtifact, expectedID string, expectedAuthority string) error {
	if value.ID != expectedID || !validStableID(value.ID) || !validDigest(value.ContentDigest) || !validDigest(value.PayloadDigest) ||
		!validDigest(value.SignerSetDigest) || value.SignerCount < 1 || value.SignerCount > 16 ||
		value.AuthorityIdentity != expectedAuthority || !validIdentity(value.AuthorityIdentity) {
		return ErrDigestDrift
	}
	if _, err := parseCanonicalTime(value.IssuedAt); err != nil {
		return fmt.Errorf("%w: signed artifact time is invalid", ErrDigestDrift)
	}
	return nil
}

func digestRequest(value any) (string, error) { return CanonicalDigest(value) }

func expectedArtifactSetDigest(plan Plan) (string, error) {
	projection := struct {
		Artifacts     []ArtifactExpectation `json:"artifacts"`
		SchemaVersion string                `json:"schemaVersion"`
	}{Artifacts: cloneArtifactExpectations(plan.Artifacts), SchemaVersion: ArtifactSetSchemaV1}
	return CanonicalDigest(projection)
}

func validateRunClosure(observation RunClosureObservation, request RunClosureRequest, plan Plan, trust TrustBindings) error {
	requestDigest, err := digestRequest(request)
	if err != nil {
		return err
	}
	if observation.OperationID != request.OperationID || observation.AuthorityID != trust.CaptureAuthorityID ||
		observation.RequestDigest != requestDigest || observation.Stage != AuthorityCommitted ||
		!validDigest(observation.ResultDigest) || !validDigest(observation.CaptureDigest) {
		return ErrDigestDrift
	}
	if _, err := parseCanonicalTime(observation.CompletedAt); err != nil {
		return fmt.Errorf("%w: run closure time is invalid", ErrDigestDrift)
	}
	if len(observation.Artifacts) != len(plan.Artifacts) {
		return ErrEvidenceClosure
	}
	for index, captured := range observation.Artifacts {
		expected := plan.Artifacts[index]
		if captured.ID != expected.ID || captured.Kind != expected.Kind || captured.Classification != expected.Classification ||
			!validStableID(captured.CaptureRef) || !validDigest(captured.ContentDigest) || captured.SizeBytes <= 0 ||
			captured.SizeBytes > 1<<40 {
			return fmt.Errorf("%w: captured artifact %d drifted", ErrEvidenceClosure, index)
		}
		if index > 0 && bytes.Compare([]byte(observation.Artifacts[index-1].ID), []byte(captured.ID)) >= 0 {
			return fmt.Errorf("%w: captured artifacts are duplicated or unsorted", ErrEvidenceClosure)
		}
	}
	computed, err := capturedArtifactDigest(observation.Artifacts)
	if err != nil || computed != observation.CaptureDigest {
		return fmt.Errorf("%w: capture digest does not match exact artifacts", ErrDigestDrift)
	}
	return nil
}

func capturedArtifactDigest(artifacts []CapturedArtifact) (string, error) {
	projection := struct {
		Artifacts     []CapturedArtifact `json:"artifacts"`
		SchemaVersion string             `json:"schemaVersion"`
	}{Artifacts: cloneCapturedArtifacts(artifacts), SchemaVersion: ArtifactSetSchemaV1}
	return CanonicalDigest(projection)
}

func credentialBindingDigest(binding CredentialSetBinding) (string, error) {
	return CanonicalDigest(cloneCredentialBinding(binding))
}

func encryptionAdditionalDataHash(plan Plan, captured CapturedArtifact) (string, error) {
	projection := struct {
		SchemaVersion         string              `json:"schemaVersion"`
		RunID                 string              `json:"runId"`
		PlanDigest            string              `json:"planDigest"`
		TemplateReleaseDigest string              `json:"templateReleaseDigest"`
		Artifact              CapturedArtifact    `json:"artifact"`
		Recipient             EncryptionRecipient `json:"recipient"`
	}{
		SchemaVersion: EncryptionManifestSchemaV1, RunID: plan.RunID, PlanDigest: plan.PlanDigest,
		TemplateReleaseDigest: plan.TemplateReleaseDigest, Artifact: captured, Recipient: plan.Recipient,
	}
	return CanonicalDigest(projection)
}

func validateEncryption(value EncryptionCommitment, request EncryptionRequest, trust TrustBindings) error {
	requestDigest, err := digestRequest(request)
	if err != nil {
		return err
	}
	if value.OperationID != request.OperationID || value.AuthorityID != trust.EncryptionAuthorityID ||
		value.RequestDigest != requestDigest || value.Stage != AuthorityCommitted || value.ArtifactID != request.Artifact.ID ||
		value.PlaintextDigest != request.Artifact.ContentDigest || value.Recipient != request.Recipient ||
		!validStableID(value.CiphertextRef) || !validDigest(value.CiphertextDigest) || value.SizeBytes <= 0 || value.SizeBytes > 1<<40 ||
		!validDigest(value.EncryptionDescriptorDigest) || !validDigest(value.WrappedKeyDigest) ||
		value.AdditionalDataHash != request.AdditionalDataHash {
		return ErrDigestDrift
	}
	encryptedAt, encryptErr := parseCanonicalTime(value.EncryptedAt)
	disposedAt, disposeErr := parseCanonicalTime(value.PlaintextDispositionAt)
	if encryptErr != nil || disposeErr != nil || disposedAt.Before(encryptedAt) {
		return ErrPlaintextDisposition
	}
	switch value.PlaintextDisposition {
	case PlaintextNeverPersisted:
		if !disposedAt.Equal(encryptedAt) {
			return ErrPlaintextDisposition
		}
	case PlaintextDeleted:
		if !disposedAt.After(encryptedAt) {
			return ErrPlaintextDisposition
		}
	default:
		return ErrPlaintextDisposition
	}
	return nil
}

func encryptionManifestDigest(values []EncryptionCommitment) (string, error) {
	projection := struct {
		Artifacts     []EncryptionCommitment `json:"artifacts"`
		SchemaVersion string                 `json:"schemaVersion"`
	}{Artifacts: cloneEncryptions(values), SchemaVersion: EncryptionManifestSchemaV1}
	return CanonicalDigest(projection)
}

// preKMSArtifactSetDigest is deliberately distinct from the encryption
// manifest. It closes the issued credential commitment, exact capture, and
// restricted plaintext/ciphertext mapping without depending on the later KMS
// attestation or credential revocation (which would create a hash cycle).
func preKMSArtifactSetDigest(snapshot Snapshot) (string, error) {
	if snapshot.Plan == nil || snapshot.CredentialIssue == nil || snapshot.RunClosure == nil || len(snapshot.Encryptions) == 0 {
		return "", ErrInvalidTransition
	}
	bindingDigest, err := credentialBindingDigest(snapshot.CredentialIssue.Binding)
	if err != nil {
		return "", err
	}
	type restrictedProjection struct {
		AdditionalDataHash         string              `json:"additionalDataHash"`
		ArtifactID                 string              `json:"artifactId"`
		CiphertextDigest           string              `json:"ciphertextDigest"`
		EncryptionDescriptorDigest string              `json:"encryptionDescriptorDigest"`
		PlaintextDigest            string              `json:"plaintextDigest"`
		Recipient                  EncryptionRecipient `json:"recipient"`
		WrappedKeyDigest           string              `json:"wrappedKeyDigest"`
	}
	restricted := make([]restrictedProjection, len(snapshot.Encryptions))
	for index, value := range snapshot.Encryptions {
		restricted[index] = restrictedProjection{
			AdditionalDataHash: value.AdditionalDataHash, ArtifactID: value.ArtifactID,
			CiphertextDigest: value.CiphertextDigest, EncryptionDescriptorDigest: value.EncryptionDescriptorDigest,
			PlaintextDigest: value.PlaintextDigest, Recipient: value.Recipient, WrappedKeyDigest: value.WrappedKeyDigest,
		}
	}
	projection := struct {
		SchemaVersion                   string                 `json:"schemaVersion"`
		RunID                           string                 `json:"runId"`
		PlanDigest                      string                 `json:"planDigest"`
		CredentialSetBindingDigest      string                 `json:"credentialSetBindingDigest"`
		CredentialIssuancePayloadDigest string                 `json:"credentialIssuancePayloadDigest"`
		CaptureDigest                   string                 `json:"captureDigest"`
		Restricted                      []restrictedProjection `json:"restricted"`
	}{
		SchemaVersion: PreKMSArtifactSetSchemaV1, RunID: snapshot.Plan.RunID, PlanDigest: snapshot.Plan.PlanDigest,
		CredentialSetBindingDigest:      bindingDigest,
		CredentialIssuancePayloadDigest: snapshot.CredentialIssue.Attestation.PayloadDigest,
		CaptureDigest:                   snapshot.RunClosure.CaptureDigest, Restricted: restricted,
	}
	return CanonicalDigest(projection)
}

func expectedKMSPayloadDigest(runID, planDigest, manifestDigest, artifactSetDigest string) (string, error) {
	projection := struct {
		SchemaVersion     string `json:"schemaVersion"`
		RunID             string `json:"runId"`
		PlanDigest        string `json:"planDigest"`
		ManifestDigest    string `json:"manifestDigest"`
		ArtifactSetDigest string `json:"artifactSetDigest"`
	}{
		SchemaVersion: "worksflow-evidence-encryption-attestation-commitment/v1", RunID: runID,
		PlanDigest: planDigest, ManifestDigest: manifestDigest, ArtifactSetDigest: artifactSetDigest,
	}
	return CanonicalDigest(projection)
}

func expectedReceiptPayloadDigest(runID, planDigest, closureDigest string, index ArtifactIndexCommitment) (string, error) {
	projection := struct {
		SchemaVersion         string `json:"schemaVersion"`
		RunID                 string `json:"runId"`
		PlanDigest            string `json:"planDigest"`
		EvidenceClosureDigest string `json:"evidenceClosureDigest"`
		IndexID               string `json:"indexId"`
		IndexDigest           string `json:"indexDigest"`
	}{
		SchemaVersion: "worksflow-qualification-receipt-payload-commitment/v1", RunID: runID,
		PlanDigest: planDigest, EvidenceClosureDigest: closureDigest, IndexID: index.IndexID, IndexDigest: index.ContentDigest,
	}
	return CanonicalDigest(projection)
}

type artifactSetProjection struct {
	Captured             []CapturedArtifact     `json:"captured"`
	Encrypted            []EncryptionCommitment `json:"encrypted"`
	CredentialIssuance   SignedArtifact         `json:"credentialIssuance"`
	KMSAttestation       SignedArtifact         `json:"kmsAttestation"`
	CredentialRevocation SignedArtifact         `json:"credentialRevocation"`
	SchemaVersion        string                 `json:"schemaVersion"`
}

func artifactSetDigest(snapshot Snapshot) (string, error) {
	if snapshot.RunClosure == nil || snapshot.CredentialIssue == nil || snapshot.KMSAttestation == nil || snapshot.CredentialRevocation == nil {
		return "", ErrInvalidTransition
	}
	distributable := make([]CapturedArtifact, 0, len(snapshot.RunClosure.Artifacts))
	for _, artifact := range snapshot.RunClosure.Artifacts {
		if artifact.Classification == ClassificationDistributable {
			distributable = append(distributable, artifact)
		}
	}
	projection := artifactSetProjection{
		Captured: cloneCapturedArtifacts(distributable), Encrypted: cloneEncryptions(snapshot.Encryptions),
		CredentialIssuance:   snapshot.CredentialIssue.Attestation,
		KMSAttestation:       snapshot.KMSAttestation.Attestation,
		CredentialRevocation: snapshot.CredentialRevocation.Attestation,
		SchemaVersion:        ArtifactSetSchemaV1,
	}
	return CanonicalDigest(projection)
}

func evidenceClosureDigest(snapshot Snapshot) (string, error) {
	artifactDigest, err := artifactSetDigest(snapshot)
	if err != nil {
		return "", err
	}
	bindingDigest, err := credentialBindingDigest(snapshot.CredentialIssue.Binding)
	if err != nil {
		return "", err
	}
	projection := struct {
		SchemaVersion      string `json:"schemaVersion"`
		RunID              string `json:"runId"`
		PlanDigest         string `json:"planDigest"`
		CredentialBinding  string `json:"credentialBindingDigest"`
		RunResultDigest    string `json:"runResultDigest"`
		CaptureDigest      string `json:"captureDigest"`
		EncryptionManifest string `json:"encryptionManifestDigest"`
		KMSPayloadDigest   string `json:"kmsPayloadDigest"`
		RevocationPayload  string `json:"revocationPayloadDigest"`
		ArtifactSetDigest  string `json:"artifactSetDigest"`
	}{
		SchemaVersion: EvidenceClosureSchemaV1, RunID: snapshot.Plan.RunID, PlanDigest: snapshot.Plan.PlanDigest,
		CredentialBinding: bindingDigest, RunResultDigest: snapshot.RunClosure.ResultDigest,
		CaptureDigest: snapshot.RunClosure.CaptureDigest, KMSPayloadDigest: snapshot.KMSAttestation.Attestation.PayloadDigest,
		RevocationPayload: snapshot.CredentialRevocation.Attestation.PayloadDigest, ArtifactSetDigest: artifactDigest,
	}
	projection.EncryptionManifest, err = encryptionManifestDigest(snapshot.Encryptions)
	if err != nil {
		return "", err
	}
	return CanonicalDigest(projection)
}

func equalCredentialBinding(left, right CredentialSetBinding) bool {
	leftDigest, leftErr := CanonicalDigest(left)
	rightDigest, rightErr := CanonicalDigest(right)
	return leftErr == nil && rightErr == nil && leftDigest == rightDigest
}

func authorityOutcome(stage AuthorityStage) error {
	switch stage {
	case AuthorityPending:
		return ErrOutcomeUnknown
	case AuthorityRejected:
		return ErrExternalRejected
	case AuthorityCommitted:
		return nil
	default:
		return ErrInvalid
	}
}

func cloneCredentialMembers(values []CredentialMember) []CredentialMember {
	return append([]CredentialMember(nil), values...)
}

func cloneArtifactExpectations(values []ArtifactExpectation) []ArtifactExpectation {
	return append([]ArtifactExpectation(nil), values...)
}

func cloneCapturedArtifacts(values []CapturedArtifact) []CapturedArtifact {
	return append([]CapturedArtifact(nil), values...)
}

func cloneCredentialBinding(value CredentialSetBinding) CredentialSetBinding {
	value.Members = cloneCredentialMembers(value.Members)
	return value
}

func clonePlan(value Plan) Plan {
	value.Artifacts = cloneArtifactExpectations(value.Artifacts)
	return value
}

func cloneCredentialIssue(value CredentialIssueObservation) CredentialIssueObservation {
	value.Binding = cloneCredentialBinding(value.Binding)
	return value
}

func cloneRunClosure(value RunClosureObservation) RunClosureObservation {
	value.Artifacts = cloneCapturedArtifacts(value.Artifacts)
	return value
}

func cloneEncryptions(values []EncryptionCommitment) []EncryptionCommitment {
	return append([]EncryptionCommitment(nil), values...)
}

func cloneCredentialRevocation(value CredentialRevocationObservation) CredentialRevocationObservation {
	value.Binding = cloneCredentialBinding(value.Binding)
	return value
}

func cloneEvent(event Event) Event {
	if event.Plan != nil {
		value := clonePlan(*event.Plan)
		event.Plan = &value
	}
	if event.CredentialIssue != nil {
		value := cloneCredentialIssue(*event.CredentialIssue)
		event.CredentialIssue = &value
	}
	if event.RunClosure != nil {
		value := cloneRunClosure(*event.RunClosure)
		event.RunClosure = &value
	}
	if event.Encryption != nil {
		value := *event.Encryption
		event.Encryption = &value
	}
	if event.KMSAttestation != nil {
		value := *event.KMSAttestation
		event.KMSAttestation = &value
	}
	if event.CredentialRevocation != nil {
		value := cloneCredentialRevocation(*event.CredentialRevocation)
		event.CredentialRevocation = &value
	}
	if event.ArtifactIndex != nil {
		value := *event.ArtifactIndex
		event.ArtifactIndex = &value
	}
	if event.Receipt != nil {
		value := *event.Receipt
		event.Receipt = &value
	}
	if event.Snapshot != nil {
		value := *event.Snapshot
		event.Snapshot = &value
	}
	if event.Verification != nil {
		value := *event.Verification
		event.Verification = &value
	}
	return event
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	if snapshot.Plan != nil {
		value := clonePlan(*snapshot.Plan)
		snapshot.Plan = &value
	}
	if snapshot.CredentialIssue != nil {
		value := cloneCredentialIssue(*snapshot.CredentialIssue)
		snapshot.CredentialIssue = &value
	}
	if snapshot.RunClosure != nil {
		value := cloneRunClosure(*snapshot.RunClosure)
		snapshot.RunClosure = &value
	}
	snapshot.Encryptions = cloneEncryptions(snapshot.Encryptions)
	if snapshot.KMSAttestation != nil {
		value := *snapshot.KMSAttestation
		snapshot.KMSAttestation = &value
	}
	if snapshot.CredentialRevocation != nil {
		value := cloneCredentialRevocation(*snapshot.CredentialRevocation)
		snapshot.CredentialRevocation = &value
	}
	if snapshot.ArtifactIndex != nil {
		value := *snapshot.ArtifactIndex
		snapshot.ArtifactIndex = &value
	}
	if snapshot.Receipt != nil {
		value := *snapshot.Receipt
		snapshot.Receipt = &value
	}
	if snapshot.SealedSnapshot != nil {
		value := *snapshot.SealedSnapshot
		snapshot.SealedSnapshot = &value
	}
	if snapshot.Verification != nil {
		value := *snapshot.Verification
		snapshot.Verification = &value
	}
	return snapshot
}

func canonicalEqual(left, right any) bool {
	leftDigest, leftErr := CanonicalDigest(left)
	rightDigest, rightErr := CanonicalDigest(right)
	return leftErr == nil && rightErr == nil && leftDigest == rightDigest
}
