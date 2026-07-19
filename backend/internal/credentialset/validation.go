package credentialset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"time"

	"github.com/google/uuid"
)

const canonicalTimeLayout = "2006-01-02T15:04:05.000Z"

var (
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	stableIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	issuerPattern   = regexp.MustCompile(`^(?:spiffe://[a-z0-9.-]+/[a-z0-9._/-]+|[a-z0-9]+(?:[._/-][a-z0-9]+)*)$`)
	audiencePattern = regexp.MustCompile(`^(?:urn:[a-z0-9][a-z0-9:._/-]+|[a-z0-9]+(?:[._/-][a-z0-9]+)*)$`)
)

var goldenSlots = []struct {
	slot  string
	kind  MemberKind
	group string
}{
	{slot: "platform-admin", kind: MemberKindToken, group: "platform-admin"},
	{slot: "platform-api-a", kind: MemberKindToken, group: "platform-user-a"},
	{slot: "platform-api-b", kind: MemberKindToken, group: "platform-user-b"},
	{slot: "platform-browser-a", kind: MemberKindStorageState, group: "platform-user-a"},
	{slot: "platform-browser-b", kind: MemberKindStorageState, group: "platform-user-b"},
	{slot: "platform-fault-operator", kind: MemberKindToken, group: "fault-operator"},
	{slot: "platform-owner", kind: MemberKindToken, group: "platform-owner"},
	{slot: "reference-api-a", kind: MemberKindStorageState, group: "reference-user-a"},
	{slot: "reference-api-b", kind: MemberKindStorageState, group: "reference-user-b"},
	{slot: "reference-browser-a", kind: MemberKindStorageState, group: "reference-user-a"},
	{slot: "reference-browser-b", kind: MemberKindStorageState, group: "reference-user-b"},
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

func validUUIDv4(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.Version() == 4 && parsed.String() == value
}

func validStableID(value string) bool {
	return len(value) <= 128 && stableIDPattern.MatchString(value)
}

func validDigest(value string) bool { return digestPattern.MatchString(value) }

func validIssuer(value string) bool {
	return len(value) <= 256 && issuerPattern.MatchString(value)
}

func validAudience(value string) bool {
	return len(value) <= 512 && audiencePattern.MatchString(value)
}

func validMemberKind(kind MemberKind) bool {
	return kind == MemberKindToken || kind == MemberKindStorageState
}

func canonicalTime(value time.Time) (string, error) {
	if value.IsZero() || value.Nanosecond()%int(time.Millisecond) != 0 {
		return "", errors.New("time must be non-zero with millisecond precision")
	}
	return value.UTC().Format(canonicalTimeLayout), nil
}

func parseCanonicalTime(value string) (time.Time, error) {
	parsed, err := time.Parse(canonicalTimeLayout, value)
	if err != nil || parsed.Format(canonicalTimeLayout) != value {
		return time.Time{}, errors.New("time must be canonical UTC ISO-8601 milliseconds")
	}
	return parsed, nil
}

// ValidateMembers validates the reusable 1..64-member commitment contract.
// Requests must already be strictly sorted by slot, then actor and kind; a
// caller cannot rely on this control plane silently normalizing ambiguous input.
func ValidateMembers(members []MemberRequest) error {
	if len(members) < 1 || len(members) > MaximumMembers {
		return fmt.Errorf("%w: member count must be between 1 and %d", ErrInvalid, MaximumMembers)
	}
	seenSlots := make(map[string]struct{}, len(members))
	for index, member := range members {
		if !validStableID(member.Slot) || !validUUIDv4(member.ActorID) || !validMemberKind(member.Kind) {
			return fmt.Errorf("%w: member %d is non-canonical", ErrInvalid, index)
		}
		if _, duplicate := seenSlots[member.Slot]; duplicate {
			return fmt.Errorf("%w: member slots must be unique", ErrInvalid)
		}
		seenSlots[member.Slot] = struct{}{}
		if index > 0 && !memberRequestLess(members[index-1], member) {
			return fmt.Errorf("%w: members must be strictly sorted", ErrInvalid)
		}
	}
	return nil
}

func memberRequestLess(left, right MemberRequest) bool {
	leftValues := [...]string{left.Slot, left.ActorID, string(left.Kind)}
	rightValues := [...]string{right.Slot, right.ActorID, string(right.Kind)}
	for index := range leftValues {
		if leftValues[index] == rightValues[index] {
			continue
		}
		return bytes.Compare([]byte(leftValues[index]), []byte(rightValues[index])) < 0
	}
	return false
}

// ValidateGoldenMembers closes the fixed Golden v2 11-slot contract, including
// its actor-sharing relationships and separation between the seven principals.
func ValidateGoldenMembers(members []MemberRequest) error {
	if err := ValidateMembers(members); err != nil {
		return err
	}
	if len(members) != GoldenMemberCount {
		return fmt.Errorf("%w: Golden credential set must contain exactly %d members", ErrInvalid, GoldenMemberCount)
	}
	actorsByGroup := make(map[string]string, 7)
	groupsByActor := make(map[string]string, 7)
	for index, expected := range goldenSlots {
		member := members[index]
		if member.Slot != expected.slot || member.Kind != expected.kind {
			return fmt.Errorf("%w: Golden member %d does not match the fixed slot and kind", ErrInvalid, index)
		}
		if actor, exists := actorsByGroup[expected.group]; exists {
			if actor != member.ActorID {
				return fmt.Errorf("%w: Golden actor group %q drifted", ErrInvalid, expected.group)
			}
			continue
		}
		if previousGroup, reused := groupsByActor[member.ActorID]; reused {
			return fmt.Errorf("%w: Golden actor is shared by distinct groups %q and %q", ErrInvalid, previousGroup, expected.group)
		}
		actorsByGroup[expected.group] = member.ActorID
		groupsByActor[member.ActorID] = expected.group
	}
	return nil
}

// ValidateBinding verifies all non-secret broker commitments and their exact
// canonical member digest. It is also used to reject partial broker results.
func ValidateBinding(binding SetBinding) error {
	if !validUUIDv4(binding.SetID) || !validUUIDv4(binding.RunID) || !validUUIDv4(binding.FixtureID) ||
		!validIssuer(binding.Issuer) || !validAudience(binding.Audience) || !validDigest(binding.SetHandleHash) ||
		!validDigest(binding.MemberBindingsDigest) || binding.MemberBindingsDigest == binding.SetHandleHash ||
		binding.MemberCount != len(binding.Members) || binding.MemberCount < 1 || binding.MemberCount > MaximumMembers {
		return fmt.Errorf("%w: set binding identity or commitment is invalid", ErrInvalid)
	}
	issuedAt, err := parseCanonicalTime(binding.IssuedAt)
	if err != nil {
		return fmt.Errorf("%w: issuedAt: %v", ErrInvalid, err)
	}
	expiresAt, err := parseCanonicalTime(binding.ExpiresAt)
	if err != nil || !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > MaximumLifetime {
		return fmt.Errorf("%w: set lifetime must be positive and no longer than %s", ErrInvalid, MaximumLifetime)
	}
	seenSlots := make(map[string]struct{}, len(binding.Members))
	seenHandles := make(map[string]struct{}, len(binding.Members))
	for index, member := range binding.Members {
		if !validStableID(member.Slot) || !validUUIDv4(member.ActorID) || !validMemberKind(member.Kind) ||
			!validDigest(member.CredentialHandleHash) || member.CredentialHandleHash == binding.SetHandleHash {
			return fmt.Errorf("%w: binding member %d is non-canonical", ErrInvalid, index)
		}
		if _, duplicate := seenSlots[member.Slot]; duplicate {
			return fmt.Errorf("%w: binding member slots are not unique", ErrInvalid)
		}
		if _, duplicate := seenHandles[member.CredentialHandleHash]; duplicate {
			return fmt.Errorf("%w: credential handle commitments are not unique", ErrInvalid)
		}
		seenSlots[member.Slot] = struct{}{}
		seenHandles[member.CredentialHandleHash] = struct{}{}
		if index > 0 && !memberBindingLess(binding.Members[index-1], member) {
			return fmt.Errorf("%w: binding members must be strictly sorted", ErrInvalid)
		}
	}
	digest, err := MemberBindingsDigest(binding.Members)
	if err != nil || digest != binding.MemberBindingsDigest {
		return fmt.Errorf("%w: canonical member bindings digest does not match", ErrInvalid)
	}
	return nil
}

func ValidateGoldenBinding(binding SetBinding) error {
	if err := ValidateBinding(binding); err != nil {
		return err
	}
	requests := make([]MemberRequest, len(binding.Members))
	for index, member := range binding.Members {
		requests[index] = MemberRequest{ActorID: member.ActorID, Kind: member.Kind, Slot: member.Slot}
	}
	return ValidateGoldenMembers(requests)
}

func memberBindingLess(left, right MemberBinding) bool {
	leftValues := [...]string{left.Slot, left.ActorID, string(left.Kind), left.CredentialHandleHash}
	rightValues := [...]string{right.Slot, right.ActorID, string(right.Kind), right.CredentialHandleHash}
	for index := range leftValues {
		if leftValues[index] == rightValues[index] {
			continue
		}
		return bytes.Compare([]byte(leftValues[index]), []byte(rightValues[index])) < 0
	}
	return false
}

func MemberBindingsDigest(members []MemberBinding) (string, error) {
	projected := make([]map[string]string, len(members))
	for index, member := range members {
		projected[index] = map[string]string{
			"actorId":              member.ActorID,
			"credentialHandleHash": member.CredentialHandleHash,
			"kind":                 string(member.Kind),
			"slot":                 member.Slot,
		}
	}
	encoded, err := json.Marshal(map[string]any{
		"members":       projected,
		"schemaVersion": MemberBindingsV1,
	})
	if err != nil {
		return "", err
	}
	return sha256Digest(encoded), nil
}

func sha256Digest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func equalBinding(left, right SetBinding) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func cloneMembers[T MemberRequest | MemberBinding](input []T) []T {
	return append([]T(nil), input...)
}

func validateIssueCommand(command IssueCommand, now time.Time, validator MemberValidator, enforceCurrentWindow bool) (BrokerPrepareRequest, string, error) {
	if !validUUIDv4(command.OperationID) || !validUUIDv4(command.SetID) || !validUUIDv4(command.RunID) ||
		!validUUIDv4(command.FixtureID) || !validIssuer(command.Issuer) || !validAudience(command.Audience) {
		return BrokerPrepareRequest{}, "", fmt.Errorf("%w: issue command identity is invalid", ErrInvalid)
	}
	identities := map[string]struct{}{
		command.OperationID: {}, command.SetID: {}, command.RunID: {}, command.FixtureID: {},
	}
	if len(identities) != 4 {
		return BrokerPrepareRequest{}, "", fmt.Errorf("%w: issue operation, set, run, and fixture identities must be distinct", ErrInvalid)
	}
	if validator == nil {
		validator = ValidateMembers
	}
	if err := validator(command.Members); err != nil {
		return BrokerPrepareRequest{}, "", err
	}
	issuedAt, err := canonicalTime(command.IssuedAt)
	if err != nil {
		return BrokerPrepareRequest{}, "", fmt.Errorf("%w: issuedAt: %v", ErrInvalid, err)
	}
	expiresAt, err := canonicalTime(command.ExpiresAt)
	if err != nil {
		return BrokerPrepareRequest{}, "", fmt.Errorf("%w: expiresAt: %v", ErrInvalid, err)
	}
	issued, _ := parseCanonicalTime(issuedAt)
	expires, _ := parseCanonicalTime(expiresAt)
	now = now.UTC()
	if !expires.After(issued) || expires.Sub(issued) > MaximumLifetime ||
		(enforceCurrentWindow && (!now.Before(expires) || issued.Before(now.Add(-MaximumClockSkew)) || issued.After(now.Add(MaximumClockSkew)))) {
		return BrokerPrepareRequest{}, "", fmt.Errorf("%w: issue lifetime or trusted-time window is invalid", ErrInvalid)
	}
	request := BrokerPrepareRequest{
		Audience: command.Audience, ExpiresAt: expiresAt, FixtureID: command.FixtureID,
		IssuedAt: issuedAt, Issuer: command.Issuer, Members: cloneMembers(command.Members),
		OperationID: command.OperationID, RunID: command.RunID, SetID: command.SetID,
	}
	hash, err := hashCanonical(request)
	if err != nil {
		return BrokerPrepareRequest{}, "", err
	}
	return request, hash, nil
}

func validateBindingAgainstRequest(binding SetBinding, request BrokerPrepareRequest, validator MemberValidator) error {
	if err := ValidateBinding(binding); err != nil {
		return err
	}
	if validator == nil {
		validator = ValidateMembers
	}
	projected := make([]MemberRequest, len(binding.Members))
	for index, member := range binding.Members {
		projected[index] = MemberRequest{ActorID: member.ActorID, Kind: member.Kind, Slot: member.Slot}
	}
	if err := validator(projected); err != nil {
		return err
	}
	if binding.SetID != request.SetID || binding.RunID != request.RunID || binding.FixtureID != request.FixtureID ||
		binding.Issuer != request.Issuer || binding.Audience != request.Audience || binding.IssuedAt != request.IssuedAt ||
		binding.ExpiresAt != request.ExpiresAt || len(projected) != len(request.Members) {
		return fmt.Errorf("%w: broker set binding does not close over the issue command", ErrInvalid)
	}
	for index := range projected {
		if projected[index] != request.Members[index] {
			return fmt.Errorf("%w: broker member binding drifted from requested membership", ErrInvalid)
		}
	}
	return nil
}

func hashCanonical(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: canonical encoding failed: %v", ErrInvalid, err)
	}
	return sha256Digest(encoded), nil
}

func validAttestation(attestation Attestation) bool {
	return len(attestation.Payload) > 0 && len(attestation.Envelope) > 0 && validDigest(attestation.PayloadDigest) &&
		validDigest(attestation.EnvelopeDigest) && sha256Digest(attestation.Payload) == attestation.PayloadDigest &&
		sha256Digest(attestation.Envelope) == attestation.EnvelopeDigest && validStableID(attestation.KeyID)
}
