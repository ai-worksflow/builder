package modelgovernance

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	maximumGovernanceEnvelopeBytes = 2 << 20
	maximumGovernancePayloadBytes  = 1 << 20
	governanceTimeLayout           = "2006-01-02T15:04:05.000Z"
)

type parsedGovernanceEnvelope struct {
	envelope       GovernanceEnvelope
	envelopeJSON   []byte
	envelopeDigest string
	payload        []byte
	payloadDigest  string
}

func parseGovernanceEnvelope(encoded []byte, expectedDigest, expectedPayloadType string) (parsedGovernanceEnvelope, error) {
	if len(encoded) == 0 || len(encoded) > maximumGovernanceEnvelopeBytes {
		return parsedGovernanceEnvelope{}, fmt.Errorf("%w: governance envelope size is outside its bound", ErrGovernanceInvalid)
	}
	if !validDigest(expectedDigest) || sha256Digest(encoded) != expectedDigest {
		return parsedGovernanceEnvelope{}, fmt.Errorf("%w: governance envelope digest drift", ErrGovernanceInvalid)
	}
	var envelope GovernanceEnvelope
	if err := decodeStrictJSON(encoded, &envelope); err != nil {
		return parsedGovernanceEnvelope{}, fmt.Errorf("%w: decode governance envelope: %v", ErrGovernanceInvalid, err)
	}
	if envelope.PayloadType != expectedPayloadType || envelope.Signatures == nil || len(envelope.Signatures) != 1 {
		return parsedGovernanceEnvelope{}, fmt.Errorf("%w: governance envelope payload type or signature cardinality is invalid", ErrGovernanceInvalid)
	}
	signature := envelope.Signatures[0]
	if !validStableID(signature.KeyID) {
		return parsedGovernanceEnvelope{}, fmt.Errorf("%w: governance envelope key id is invalid", ErrGovernanceInvalid)
	}
	payload, err := decodeCanonicalGovernanceBase64(envelope.Payload, 1, maximumGovernancePayloadBytes)
	if err != nil {
		return parsedGovernanceEnvelope{}, fmt.Errorf("%w: governance payload encoding: %v", ErrGovernanceInvalid, err)
	}
	if _, err := decodeCanonicalGovernanceBase64(signature.Sig, 64, 64); err != nil {
		return parsedGovernanceEnvelope{}, fmt.Errorf("%w: governance signature encoding: %v", ErrGovernanceInvalid, err)
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return parsedGovernanceEnvelope{}, fmt.Errorf("%w: governance envelope is not canonical JSON", ErrGovernanceInvalid)
	}
	return parsedGovernanceEnvelope{
		envelope: envelope, envelopeJSON: bytes.Clone(encoded), envelopeDigest: expectedDigest,
		payload: bytes.Clone(payload), payloadDigest: sha256Digest(payload),
	}, nil
}

func decodeCanonicalGovernanceBase64(value string, minimum, maximum int) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) < minimum || len(decoded) > maximum || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("value is not canonical standard base64 in the allowed size range")
	}
	return decoded, nil
}

func parseCanonicalAuthorityDocument[T any](
	encoded []byte,
	expectedHash, label string,
	validate func(T) error,
) (T, error) {
	var zero T
	if len(encoded) == 0 || len(encoded) > maximumGovernancePayloadBytes || !validDigest(expectedHash) || sha256Digest(encoded) != expectedHash {
		return zero, fmt.Errorf("%w: %s bytes or content hash are invalid", ErrGovernanceInvalid, label)
	}
	var value T
	if err := decodeStrictJSON(encoded, &value); err != nil {
		return zero, fmt.Errorf("%w: decode %s: %v", ErrGovernanceInvalid, label, err)
	}
	if err := validate(value); err != nil {
		return zero, fmt.Errorf("%w: validate %s: %v", ErrGovernanceInvalid, label, err)
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return zero, fmt.Errorf("%w: %s is not canonical JSON", ErrGovernanceInvalid, label)
	}
	return value, nil
}

func canonicalAuthorityDocument[T any](value T, label string, validate func(T) error) ([]byte, error) {
	if err := validate(value); err != nil {
		return nil, fmt.Errorf("validate %s: %w", label, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", label, err)
	}
	return encoded, nil
}

func CanonicalConformanceArtifactJSON(value ConformanceArtifact) ([]byte, error) {
	return canonicalAuthorityDocument(value, "ConformanceArtifact", validateConformanceArtifact)
}

func ParseConformanceArtifact(encoded []byte, expectedHash string) (ConformanceArtifact, error) {
	return parseCanonicalAuthorityDocument(encoded, expectedHash, "ConformanceArtifact", validateConformanceArtifact)
}

func CanonicalShadowArtifactJSON(value ShadowArtifact) ([]byte, error) {
	return canonicalAuthorityDocument(value, "ShadowArtifact", validateShadowArtifact)
}

func ParseShadowArtifact(encoded []byte, expectedHash string) (ShadowArtifact, error) {
	return parseCanonicalAuthorityDocument(encoded, expectedHash, "ShadowArtifact", validateShadowArtifact)
}

func CanonicalApprovalArtifactJSON(value ApprovalArtifact) ([]byte, error) {
	return canonicalAuthorityDocument(value, "ApprovalArtifact", validateApprovalArtifact)
}

func ParseApprovalArtifact(encoded []byte, expectedHash string) (ApprovalArtifact, error) {
	return parseCanonicalAuthorityDocument(encoded, expectedHash, "ApprovalArtifact", validateApprovalArtifact)
}

func CanonicalActivationArtifactJSON(value ActivationArtifact) ([]byte, error) {
	return canonicalAuthorityDocument(value, "ActivationArtifact", validateActivationArtifact)
}

func ParseActivationArtifact(encoded []byte, expectedHash string) (ActivationArtifact, error) {
	return parseCanonicalAuthorityDocument(encoded, expectedHash, "ActivationArtifact", validateActivationArtifact)
}

func CanonicalModelGovernanceReceiptJSON(value ModelGovernanceReceipt) ([]byte, error) {
	return canonicalAuthorityDocument(value, "ModelGovernanceReceipt", validateModelGovernanceReceipt)
}

func ParseModelGovernanceReceipt(encoded []byte, expectedHash string) (ModelGovernanceReceipt, error) {
	return parseCanonicalAuthorityDocument(encoded, expectedHash, "ModelGovernanceReceipt", validateModelGovernanceReceipt)
}

func CanonicalGovernanceGenesisArtifactJSON(value GovernanceGenesisArtifact) ([]byte, error) {
	return canonicalAuthorityDocument(value, "GovernanceGenesisArtifact", validateGovernanceGenesisArtifact)
}

func ParseGovernanceGenesisArtifact(encoded []byte, expectedHash string) (GovernanceGenesisArtifact, error) {
	return parseCanonicalAuthorityDocument(encoded, expectedHash, "GovernanceGenesisArtifact", validateGovernanceGenesisArtifact)
}

func CanonicalModelGovernanceGenesisReceiptJSON(value ModelGovernanceGenesisReceipt) ([]byte, error) {
	return canonicalAuthorityDocument(value, "ModelGovernanceGenesisReceipt", validateModelGovernanceGenesisReceipt)
}

func ParseModelGovernanceGenesisReceipt(encoded []byte, expectedHash string) (ModelGovernanceGenesisReceipt, error) {
	return parseCanonicalAuthorityDocument(encoded, expectedHash, "ModelGovernanceGenesisReceipt", validateModelGovernanceGenesisReceipt)
}

func CanonicalProviderRouteAuthorityJSON(value ProviderRouteAuthority) ([]byte, error) {
	return canonicalAuthorityDocument(value, "ProviderRouteAuthority", validateProviderRouteAuthority)
}

func ParseProviderRouteAuthority(encoded []byte, expectedHash string) (ProviderRouteAuthority, error) {
	return parseCanonicalAuthorityDocument(encoded, expectedHash, "ProviderRouteAuthority", validateProviderRouteAuthority)
}

func validateConformanceArtifact(value ConformanceArtifact) error {
	if value.SchemaVersion != ConformanceArtifactSchemaVersion || !validUUIDv4(value.ArtifactID) ||
		value.Result != ConformanceResultPassed || !validDigest(value.ResultHash) {
		return errors.New("schema, identity, result, or result hash is invalid")
	}
	if err := validateGovernanceSubject(value.Subject); err != nil {
		return err
	}
	startedAt, err := parseGovernanceTime(value.StartedAt, "conformance.startedAt")
	if err != nil {
		return err
	}
	completedAt, err := parseGovernanceTime(value.CompletedAt, "conformance.completedAt")
	if err != nil || !completedAt.After(startedAt) {
		return errors.New("conformance completion must be strictly after start")
	}
	issuedAt, _, err := validateGovernanceWindow(value.IssuedAt, value.ExpiresAt, MaximumGovernanceArtifactLifetime, "conformance")
	if err != nil || issuedAt.Before(completedAt) {
		return errors.New("conformance issuance, completion, or expiry is invalid")
	}
	return nil
}

func validateShadowArtifact(value ShadowArtifact) error {
	if value.SchemaVersion != ShadowArtifactSchemaVersion || !validUUIDv4(value.ArtifactID) ||
		value.Result != ShadowResultPassed || !validDigest(value.ComparisonHash) {
		return errors.New("schema, identity, result, or comparison hash is invalid")
	}
	if err := validateGovernanceSubject(value.Subject); err != nil {
		return err
	}
	if err := validateBaseline(value.Baseline); err != nil {
		return err
	}
	startedAt, err := parseGovernanceTime(value.StartedAt, "shadow.startedAt")
	if err != nil {
		return err
	}
	completedAt, err := parseGovernanceTime(value.CompletedAt, "shadow.completedAt")
	if err != nil || !completedAt.After(startedAt) {
		return errors.New("shadow completion must be strictly after start")
	}
	issuedAt, _, err := validateGovernanceWindow(value.IssuedAt, value.ExpiresAt, MaximumGovernanceArtifactLifetime, "shadow")
	if err != nil || issuedAt.Before(completedAt) {
		return errors.New("shadow issuance, completion, or expiry is invalid")
	}
	return nil
}

func validateApprovalArtifact(value ApprovalArtifact) error {
	if value.SchemaVersion != ApprovalArtifactSchemaVersion || !validUUIDv4(value.ArtifactID) ||
		value.Decision != ApprovalDecisionApprove || !validDigest(value.DecisionHash) {
		return errors.New("schema, identity, decision, or decision hash is invalid")
	}
	if err := validateGovernanceSubject(value.Subject); err != nil {
		return err
	}
	if err := validateGovernanceArtifactRef(value.Conformance); err != nil {
		return fmt.Errorf("conformance reference: %w", err)
	}
	_, _, err := validateGovernanceWindow(value.IssuedAt, value.ExpiresAt, MaximumGovernanceArtifactLifetime, "approval")
	return err
}

func validateActivationArtifact(value ActivationArtifact) error {
	if value.SchemaVersion != ActivationArtifactSchemaVersion || !validUUIDv4(value.ArtifactID) ||
		value.Decision != ActivationDecisionApply || !validDigest(value.DecisionHash) || value.Generation == 0 ||
		value.Generation != value.PreviousGeneration+1 || !validDigest(value.Fence) || !validDigest(value.PreviousFence) || value.Fence == value.PreviousFence {
		return errors.New("schema, identity, decision, generation, or fence is invalid")
	}
	if err := validateGovernanceSubject(value.Subject); err != nil {
		return err
	}
	for name, reference := range map[string]GovernanceArtifactRef{
		"approval": value.Approval, "conformance": value.Conformance, "shadow": value.Shadow,
	} {
		if err := validateGovernanceArtifactRef(reference); err != nil {
			return fmt.Errorf("%s reference: %w", name, err)
		}
	}
	_, _, err := validateGovernanceWindow(value.IssuedAt, value.ExpiresAt, MaximumGovernanceArtifactLifetime, "activation")
	return err
}

func validateModelGovernanceReceipt(value ModelGovernanceReceipt) error {
	if value.SchemaVersion != GovernanceReceiptSchemaVersion || !validUUIDv4(value.ArtifactID) || value.Generation == 0 || !validDigest(value.Fence) {
		return errors.New("schema, identity, generation, or fence is invalid")
	}
	if err := validateGovernanceSubject(value.Subject); err != nil {
		return err
	}
	for name, reference := range map[string]GovernanceArtifactRef{
		"activation": value.Activation, "approval": value.Approval, "conformance": value.Conformance, "shadow": value.Shadow,
	} {
		if err := validateGovernanceArtifactRef(reference); err != nil {
			return fmt.Errorf("%s reference: %w", name, err)
		}
	}
	_, _, err := validateGovernanceWindow(value.IssuedAt, value.ExpiresAt, MaximumGovernanceArtifactLifetime, "receipt")
	return err
}

func validateGovernanceGenesisArtifact(value GovernanceGenesisArtifact) error {
	if value.SchemaVersion != GovernanceGenesisSchemaVersion || !validUUIDv4(value.ArtifactID) ||
		value.Decision != GenesisDecisionBootstrap || !validDigest(value.DecisionHash) || value.Generation != 1 ||
		value.PreviousGeneration != 0 || !validDigest(value.PreviousFence) || !validDigest(value.Fence) ||
		value.Fence == value.PreviousFence {
		return errors.New("schema, identity, closed decision, generation, or fence is invalid")
	}
	if err := validateGovernanceSubject(value.Subject); err != nil {
		return err
	}
	if err := validateGovernanceRevocationBinding(value.RevocationAuthority); err != nil {
		return err
	}
	for name, reference := range map[string]GovernanceArtifactRef{
		"approval": value.Approval, "conformance": value.Conformance,
	} {
		if err := validateGovernanceArtifactRef(reference); err != nil {
			return fmt.Errorf("%s reference: %w", name, err)
		}
	}
	_, _, err := validateGovernanceWindow(value.IssuedAt, value.ExpiresAt, MaximumGovernanceArtifactLifetime, "genesis")
	return err
}

func validateModelGovernanceGenesisReceipt(value ModelGovernanceGenesisReceipt) error {
	if value.SchemaVersion != GovernanceGenesisReceiptSchemaVersion || !validUUIDv4(value.ArtifactID) ||
		value.Generation != 1 || !validDigest(value.Fence) {
		return errors.New("schema, identity, generation, or fence is invalid")
	}
	if err := validateGovernanceSubject(value.Subject); err != nil {
		return err
	}
	if err := validateGovernanceRevocationBinding(value.RevocationAuthority); err != nil {
		return err
	}
	for name, reference := range map[string]GovernanceArtifactRef{
		"approval": value.Approval, "conformance": value.Conformance, "genesis": value.Genesis,
	} {
		if err := validateGovernanceArtifactRef(reference); err != nil {
			return fmt.Errorf("%s reference: %w", name, err)
		}
	}
	_, _, err := validateGovernanceWindow(value.IssuedAt, value.ExpiresAt, MaximumGovernanceArtifactLifetime, "genesis receipt")
	return err
}

func validateGovernanceRevocationBinding(value GovernanceRevocationAuthorityBinding) error {
	if value.AuthorityID != GovernanceRevocationAuthorityID || !validDigest(value.AuthorityHash) || value.Epoch == 0 {
		return errors.New("Genesis revocation authority binding is invalid")
	}
	return nil
}

func validateProviderRouteAuthority(value ProviderRouteAuthority) error {
	if value.SchemaVersion != ProviderRouteAuthoritySchemaV1 || !validStableID(value.RouteID) ||
		value.Protocol != ProviderProtocolOpenAIResponsesV1 || !validDigest(value.EndpointDigest) ||
		!validDigest(value.TLSIdentityHash) || !validDigest(value.EgressPolicyHash) {
		return errors.New("route schema, identity, protocol, or authority digests are invalid")
	}
	_, _, err := validateGovernanceWindow(value.IssuedAt, value.ExpiresAt, MaximumRouteAuthorityLifetime, "provider route authority")
	return err
}

func validateGovernanceSubject(subject GovernanceSubjectBinding) error {
	if !validUUIDv4(subject.Profile.ID) || !validDigest(subject.Profile.ContentHash) || !validStableID(subject.Profile.Workload) ||
		!validUUIDv4(subject.Corpus.ID) || !validDigest(subject.Corpus.ContentHash) ||
		!validDigest(subject.ThresholdPolicyHash) || !validDigest(subject.HarnessHash) || !validDigest(subject.VerifierHash) ||
		!validDigest(subject.TrustPolicyHash) || !validStableID(subject.ProviderRoute.RouteID) ||
		!validDigest(subject.ProviderRoute.AuthorityHash) || subject.Runner.Kind != RunnerKindCodexCLI ||
		!validDigest(subject.Runner.ImmutableDigest) || !commitPattern.MatchString(subject.Source.Commit) ||
		subject.Source.Dirty || subject.Source.TreeDigestSchema != SourceTreeDigestSchemaV1 || !validDigest(subject.Source.TreeDigest) {
		return errors.New("governance subject contains an incomplete or non-canonical binding")
	}
	return nil
}

func validateBaseline(value BaselineBinding) error {
	if value.Generation == 0 || !validDigest(value.ActivationFence) || !validDigest(value.MetricsHash) || !validDigest(value.ReceiptDigest) ||
		!validUUIDv4(value.Profile.ID) || !validDigest(value.Profile.ContentHash) || !validStableID(value.Profile.Workload) {
		return errors.New("shadow baseline binding is incomplete or non-canonical")
	}
	return nil
}

func validateGovernanceArtifactRef(value GovernanceArtifactRef) error {
	if !validUUIDv4(value.ArtifactID) || !validDigest(value.EnvelopeDigest) || !validDigest(value.PayloadDigest) || value.EnvelopeDigest == value.PayloadDigest {
		return errors.New("artifact reference is incomplete or non-canonical")
	}
	return nil
}

func validateGovernanceWindow(issuedValue, expiresValue string, maximum time.Duration, label string) (time.Time, time.Time, error) {
	issuedAt, err := parseGovernanceTime(issuedValue, label+".issuedAt")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	expiresAt, err := parseGovernanceTime(expiresValue, label+".expiresAt")
	if err != nil || !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > maximum {
		return time.Time{}, time.Time{}, fmt.Errorf("%s validity window is invalid or unbounded", label)
	}
	return issuedAt, expiresAt, nil
}

func parseGovernanceTime(value, field string) (time.Time, error) {
	parsed, err := time.Parse(governanceTimeLayout, value)
	if err != nil || parsed.Format(governanceTimeLayout) != value {
		return time.Time{}, fmt.Errorf("%s must be exact UTC ISO-8601 milliseconds", field)
	}
	return parsed, nil
}

func formatGovernanceTime(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format(governanceTimeLayout)
}
