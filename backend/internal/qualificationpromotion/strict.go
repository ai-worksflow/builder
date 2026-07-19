package qualificationpromotion

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
)

const canonicalTimeLayout = "2006-01-02T15:04:05.000Z"

var (
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	stableIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$`)
)

func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(generic); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func decodeExactJSON(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errorsNewTrailingJSON()
		}
		return err
	}
	return nil
}

func errorsNewTrailingJSON() error { return fmt.Errorf("trailing JSON value") }

func sha256Digest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validDigest(value string) bool { return digestPattern.MatchString(value) }

func validUUIDv4(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.Version() == 4 && parsed.String() == value
}

func validCanonicalString(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= maximum && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n\t")
}

func validStableID(value string) bool {
	return len(value) <= 256 && stableIDPattern.MatchString(value)
}

func parseCanonicalTime(value string) (time.Time, error) {
	parsed, err := time.Parse(canonicalTimeLayout, value)
	if err != nil || parsed.Format(canonicalTimeLayout) != value {
		return time.Time{}, fmt.Errorf("%w: timestamp is not canonical UTC milliseconds", ErrInvalid)
	}
	return parsed, nil
}

func validateTarget(target qualificationreceipt.PromotionTarget) error {
	if !validUUIDv4(target.ProjectID) || !validUUIDv4(target.WorkflowRunID) || !validStableID(target.NodeKey) ||
		!validUUIDv4(target.TargetRevision.ID) || !validDigest(target.TargetRevision.ContentHash) ||
		!validCanonicalString(target.Subject, 256) || target.StageGate != qualificationreceipt.ExternalQualificationGate {
		return fmt.Errorf("%w: promotion target is incomplete or non-canonical", ErrInvalid)
	}
	return nil
}

func sameTarget(left, right qualificationreceipt.PromotionTarget) bool {
	return left == right
}

func validIdentityList(values []string) bool {
	if len(values) == 0 || len(values) > 16 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validCanonicalString(value, 512) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validateVerifiedPromotionShape(verified qualificationreceipt.VerifiedPromotion) error {
	if validateTarget(verified.PromotionTarget) != nil ||
		verified.Scope != qualificationreceipt.ExternalQualificationScope ||
		verified.SingleUseConsumption != qualificationreceipt.SingleUseConsumptionPolicy ||
		!validUUIDv4(verified.AuthorityNonce) || !validDigest(verified.PromotionAuthorityDigest) ||
		!validUUIDv4(verified.RunID) || !validDigest(verified.PlanDigest) ||
		!validDigest(verified.ArtifactIndexDigest) || !validDigest(verified.ReceiptPayloadDigest) ||
		!validDigest(verified.ReceiptBundleDigest) || verified.ReceiptPayloadDigest == verified.ReceiptBundleDigest ||
		!validDigest(verified.FaultLedgerAttestationDigest) || verified.Decision != "qualified" {
		return fmt.Errorf("%w: verifier output does not contain the closed qualification authority/evidence binding", ErrInvalid)
	}
	issuedAt, err := parseCanonicalTime(verified.IssuedAt)
	if err != nil {
		return err
	}
	expiresAt, err := parseCanonicalTime(verified.AuthorityExpiresAt)
	if err != nil || !expiresAt.After(issuedAt) {
		return fmt.Errorf("%w: qualification authority interval is invalid", ErrInvalid)
	}
	golden := verified.GoldenRuntime
	if !validStableID(golden.AuthorityDocumentArtifactID) || !validDigest(golden.AuthorityDocumentDigest) ||
		!validStableID(golden.FixtureDocumentArtifactID) || !validDigest(golden.FixtureDocumentDigest) ||
		golden.AuthorityDocumentArtifactID == golden.FixtureDocumentArtifactID ||
		golden.AuthorityDocumentDigest == golden.FixtureDocumentDigest || !validUUIDv4(golden.FixtureID) ||
		golden.FaultOperationSetDigest != qualificationreceipt.GoldenFaultOperationSetDigestV1 {
		return fmt.Errorf("%w: Golden authority/fixture root is invalid", ErrInvalid)
	}
	credential := verified.CredentialSet
	if !validCanonicalString(credential.Issuer, 256) || !validCanonicalString(credential.Audience, 512) ||
		!validDigest(credential.SetHandleHash) || !validDigest(credential.MemberBindingsDigest) ||
		credential.MemberCount < 1 || credential.MemberCount > qualificationreceipt.CredentialSetMaximumMembers ||
		!validStableID(credential.IssuanceArtifactID) || !validDigest(credential.IssuancePayloadDigest) ||
		!validStableID(credential.RevocationArtifactID) || !validDigest(credential.RevocationPayloadDigest) ||
		credential.IssuanceArtifactID == credential.RevocationArtifactID ||
		credential.IssuancePayloadDigest == credential.RevocationPayloadDigest {
		return fmt.Errorf("%w: credential-set evidence binding is invalid", ErrInvalid)
	}
	identitySets := [][]string{
		verified.SignerIdentities,
		verified.CredentialIssuanceSignerIdentities,
		verified.CredentialRevocationSignerIdentities,
		verified.EncryptionSignerIdentities,
		verified.FaultAuthoritySignerIdentities,
		verified.FaultLedgerAttestorSignerIdentities,
	}
	global := map[string]struct{}{}
	for _, identities := range identitySets {
		if !validIdentityList(identities) {
			return fmt.Errorf("%w: signer identity closure is empty, duplicate, or non-canonical", ErrInvalid)
		}
		for _, identity := range identities {
			if _, exists := global[identity]; exists {
				return fmt.Errorf("%w: signer identities are reused across independent roles", ErrInvalid)
			}
			global[identity] = struct{}{}
		}
	}
	return nil
}

func validateVerifiedPromotionAt(verified qualificationreceipt.VerifiedPromotion, now time.Time) error {
	if err := validateVerifiedPromotionShape(verified); err != nil {
		return err
	}
	if now.IsZero() {
		return fmt.Errorf("%w: trusted append time is required", ErrInvalid)
	}
	expiresAt, err := parseCanonicalTime(verified.AuthorityExpiresAt)
	if err != nil {
		return err
	}
	if !now.Before(expiresAt) {
		return ErrAuthorityExpired
	}
	return nil
}

func targetDigest(target qualificationreceipt.PromotionTarget) (string, error) {
	if err := validateTarget(target); err != nil {
		return "", err
	}
	encoded, err := canonicalJSON(target)
	if err != nil {
		return "", err
	}
	return sha256Digest(encoded), nil
}

func sameImmutableRecord(left, right ConsumptionRecord) bool {
	return left.OperationID == right.OperationID && left.RequestHash == right.RequestHash &&
		left.QualificationAuthorityID == right.QualificationAuthorityID &&
		bytes.Equal(left.RequestBytes, right.RequestBytes) && left.Request == right.Request &&
		left.TargetDigest == right.TargetDigest && left.VerifiedPromotionHash == right.VerifiedPromotionHash &&
		bytes.Equal(left.VerifiedPromotionBytes, right.VerifiedPromotionBytes) &&
		reflect.DeepEqual(left.VerifiedPromotion, right.VerifiedPromotion) && sameHandoff(left.Handoff, right.Handoff)
}

func sameHandoff(left, right HandoffRecord) bool {
	return left.HandoffID == right.HandoffID && left.OperationID == right.OperationID && left.State == right.State &&
		sameTarget(left.Target, right.Target) && left.OutputRevisionID == right.OutputRevisionID &&
		left.RevisionKind == right.RevisionKind && left.RevisionIntentDigest == right.RevisionIntentDigest &&
		bytes.Equal(left.RevisionIntentBytes, right.RevisionIntentBytes) && left.RevisionIntent == right.RevisionIntent &&
		left.AuthorityNonce == right.AuthorityNonce && left.PromotionAuthorityDigest == right.PromotionAuthorityDigest &&
		left.VerifiedPromotionHash == right.VerifiedPromotionHash
}

func cloneRecord(record ConsumptionRecord) ConsumptionRecord {
	record.RequestBytes = bytes.Clone(record.RequestBytes)
	record.VerifiedPromotionBytes = bytes.Clone(record.VerifiedPromotionBytes)
	record.Handoff.RevisionIntentBytes = bytes.Clone(record.Handoff.RevisionIntentBytes)
	record.VerifiedPromotion.SignerIdentities = append([]string(nil), record.VerifiedPromotion.SignerIdentities...)
	record.VerifiedPromotion.CredentialIssuanceSignerIdentities = append([]string(nil), record.VerifiedPromotion.CredentialIssuanceSignerIdentities...)
	record.VerifiedPromotion.CredentialRevocationSignerIdentities = append([]string(nil), record.VerifiedPromotion.CredentialRevocationSignerIdentities...)
	record.VerifiedPromotion.EncryptionSignerIdentities = append([]string(nil), record.VerifiedPromotion.EncryptionSignerIdentities...)
	record.VerifiedPromotion.FaultAuthoritySignerIdentities = append([]string(nil), record.VerifiedPromotion.FaultAuthoritySignerIdentities...)
	record.VerifiedPromotion.FaultLedgerAttestorSignerIdentities = append([]string(nil), record.VerifiedPromotion.FaultLedgerAttestorSignerIdentities...)
	return record
}
