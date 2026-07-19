package goldenfault

import (
	"bytes"
	"fmt"

	"github.com/google/uuid"
)

const (
	ReservationEvidenceSchemaV1 = "worksflow-golden-fault-reservation-evidence/v1"
	TerminalEvidenceSchemaV1    = "worksflow-golden-fault-terminal-evidence/v1"
)

// ReservationEvidenceDigest commits the complete durable reservation fact in
// a stable JSON projection suitable for an independently signed ledger
// attestation. It never exposes a command or adapter-controlled payload.
func ReservationEvidenceDigest(reservation Reservation) (string, error) {
	if err := validateReservation(reservation); err != nil {
		return "", err
	}
	document := reservationEvidenceDocument{
		AdapterInvocationID: reservation.AdapterInvocationID,
		AuthorityExpiresAt:  formatCanonicalTime(reservation.AuthorityExpiresAt),
		AuthorityID:         reservation.AuthorityID,
		AuthorityIssuedAt:   formatCanonicalTime(reservation.AuthorityIssuedAt),
		EnvelopeDigest:      reservation.EnvelopeDigest,
		ExpectedFenceDigest: reservation.ExpectedFenceDigest,
		FixtureID:           reservation.FixtureID,
		OperationKind:       reservation.OperationKind,
		PayloadDigest:       reservation.PayloadDigest,
		PredicateDigest:     reservation.PredicateDigest,
		ReservedAt:          formatCanonicalTime(reservation.ReservedAt),
		ResolutionDigest:    reservation.ResolutionDigest,
		ResolvedFenceDigest: reservation.ResolvedFenceDigest,
		ResolvedHeadDigest:  reservation.ResolvedHeadDigest,
		ResolvedResourceID:  reservation.ResolvedResourceID,
		ResourceSelector:    reservation.ResourceSelector,
		RunID:               reservation.RunID,
		SchemaVersion:       ReservationEvidenceSchemaV1,
		SignerIdentities:    append([]string(nil), reservation.SignerIdentities...),
	}
	encoded, err := canonicalJSON(document)
	if err != nil {
		return "", fmt.Errorf("canonicalize Golden fault reservation evidence: %w", err)
	}
	return sha256Digest(encoded), nil
}

// TerminalEvidenceDigest commits the terminal result and the exact canonical
// consume-receipt digest. The reservation remains a separate commitment so an
// attestor cannot silently replace either half of the append-only ledger.
func TerminalEvidenceDigest(terminal TerminalResult) (string, error) {
	if err := validateTerminalResult(terminal); err != nil {
		return "", err
	}
	document := terminalEvidenceDocument{
		AdapterResultDigest: terminal.AdapterResultDigest,
		AuthorityID:         terminal.AuthorityID,
		CompletedAt:         formatCanonicalTime(terminal.CompletedAt),
		ObservedFenceDigest: terminal.ObservedFenceDigest,
		ObservedHeadDigest:  terminal.ObservedHeadDigest,
		Outcome:             terminal.Outcome,
		ReceiptDigest:       terminal.ReceiptDigest,
		ResultID:            terminal.ResultID,
		SchemaVersion:       TerminalEvidenceSchemaV1,
	}
	encoded, err := canonicalJSON(document)
	if err != nil {
		return "", fmt.Errorf("canonicalize Golden fault terminal evidence: %w", err)
	}
	return sha256Digest(encoded), nil
}

// ValidateConsumeReceiptEvidence strictly parses a distributable plain
// receipt and reconstructs both immutable ledger facts from a cryptographically
// verified authority. Its historical validity decision is based on reservedAt,
// never on the verifier's current wall clock.
func ValidateConsumeReceiptEvidence(
	authority VerifiedAuthority,
	receiptJSON []byte,
) (Reservation, TerminalResult, error) {
	if err := requireExactObject(receiptJSON, consumeReceiptKinds()); err != nil {
		return Reservation{}, TerminalResult{}, fmt.Errorf("%w: consume receipt shape: %v", ErrConflict, err)
	}
	var receipt ConsumeReceipt
	if err := decodeStrictJSON(receiptJSON, &receipt); err != nil {
		return Reservation{}, TerminalResult{}, fmt.Errorf("%w: decode consume receipt: %v", ErrConflict, err)
	}
	canonical, err := canonicalJSON(receipt)
	if err != nil || !bytes.Equal(canonical, receiptJSON) {
		return Reservation{}, TerminalResult{}, fmt.Errorf("%w: consume receipt is not canonical JSON", ErrConflict)
	}
	reservedAt, err := parseCanonicalTime(receipt.ReservedAt, "consumeReceipt.reservedAt")
	if err != nil {
		return Reservation{}, TerminalResult{}, fmt.Errorf("%w: %v", ErrConflict, err)
	}
	completedAt, err := parseCanonicalTime(receipt.CompletedAt, "consumeReceipt.completedAt")
	if err != nil {
		return Reservation{}, TerminalResult{}, fmt.Errorf("%w: %v", ErrConflict, err)
	}
	canonicalPredicate, err := canonicalJSON(authority.Predicate)
	if err != nil {
		return Reservation{}, TerminalResult{}, fmt.Errorf("%w: canonicalize authority predicate: %v", ErrConflict, err)
	}
	reservation := Reservation{
		AuthorityID:         receipt.AuthorityID,
		FixtureID:           receipt.FixtureID,
		RunID:               receipt.RunID,
		OperationKind:       receipt.OperationKind,
		ResourceSelector:    receipt.ResourceSelector,
		ExpectedFenceDigest: receipt.ExpectedFenceDigest,
		EnvelopeDigest:      receipt.EnvelopeDigest,
		PayloadDigest:       receipt.PayloadDigest,
		PredicateDigest:     receipt.PredicateDigest,
		AuthorityIssuedAt:   authority.IssuedAt,
		AuthorityExpiresAt:  authority.ExpiresAt,
		SignerIdentities:    append([]string(nil), authority.SignerIdentities...),
		ResolvedResourceID:  receipt.ResolvedResourceID,
		ResolvedHeadDigest:  receipt.ResolvedHeadDigest,
		ResolvedFenceDigest: receipt.ResolvedFenceDigest,
		ResolutionDigest:    receipt.ResolutionDigest,
		AdapterInvocationID: receipt.AdapterInvocationID,
		ReservedAt:          reservedAt,
	}
	if authority.Predicate.AuthorityID != reservation.AuthorityID.String() ||
		authority.Predicate.FixtureID != reservation.FixtureID.String() ||
		authority.Predicate.RunID != reservation.RunID.String() ||
		authority.Predicate.OperationKind != reservation.OperationKind ||
		authority.Predicate.ResourceSelector != reservation.ResourceSelector ||
		authority.Predicate.ExpectedFenceDigest != reservation.ExpectedFenceDigest ||
		authority.EnvelopeDigest != reservation.EnvelopeDigest ||
		authority.PayloadDigest != reservation.PayloadDigest ||
		sha256Digest(canonicalPredicate) != reservation.PredicateDigest {
		return Reservation{}, TerminalResult{}, fmt.Errorf("%w: receipt does not bind the verified authority", ErrConflict)
	}
	terminal := TerminalResult{
		AuthorityID:         receipt.AuthorityID,
		ResultID:            receipt.ResultID,
		Outcome:             receipt.Outcome,
		AdapterResultDigest: receipt.AdapterResultDigest,
		ObservedHeadDigest:  receipt.ObservedHeadDigest,
		ObservedFenceDigest: receipt.ObservedFenceDigest,
		CompletedAt:         completedAt,
		Receipt:             receipt,
		ReceiptJSON:         bytes.Clone(receiptJSON),
		ReceiptDigest:       sha256Digest(receiptJSON),
	}
	if err := validateReservation(reservation); err != nil {
		return Reservation{}, TerminalResult{}, err
	}
	if err := validateTerminalClosure(terminal, reservation); err != nil {
		return Reservation{}, TerminalResult{}, err
	}
	return reservation, terminal, nil
}

// ResourceResolutionDigest exposes the same typed resolution commitment used
// by the service so evidence producers do not reimplement its canonical form.
func ResourceResolutionDigest(authorityID uuid.UUID, resolution ResourceResolution) (string, error) {
	return hashResolution(authorityID, resolution)
}

type reservationEvidenceDocument struct {
	AdapterInvocationID uuid.UUID     `json:"adapterInvocationId"`
	AuthorityExpiresAt  string        `json:"authorityExpiresAt"`
	AuthorityID         uuid.UUID     `json:"authorityId"`
	AuthorityIssuedAt   string        `json:"authorityIssuedAt"`
	EnvelopeDigest      string        `json:"envelopeDigest"`
	ExpectedFenceDigest string        `json:"expectedFenceDigest"`
	FixtureID           uuid.UUID     `json:"fixtureId"`
	OperationKind       OperationKind `json:"operationKind"`
	PayloadDigest       string        `json:"payloadDigest"`
	PredicateDigest     string        `json:"predicateDigest"`
	ReservedAt          string        `json:"reservedAt"`
	ResolutionDigest    string        `json:"resolutionDigest"`
	ResolvedFenceDigest string        `json:"resolvedFenceDigest"`
	ResolvedHeadDigest  string        `json:"resolvedHeadDigest"`
	ResolvedResourceID  string        `json:"resolvedResourceId"`
	ResourceSelector    string        `json:"resourceSelector"`
	RunID               uuid.UUID     `json:"runId"`
	SchemaVersion       string        `json:"schemaVersion"`
	SignerIdentities    []string      `json:"signerIdentities"`
}

type terminalEvidenceDocument struct {
	AdapterResultDigest string         `json:"adapterResultDigest"`
	AuthorityID         uuid.UUID      `json:"authorityId"`
	CompletedAt         string         `json:"completedAt"`
	ObservedFenceDigest string         `json:"observedFenceDigest"`
	ObservedHeadDigest  string         `json:"observedHeadDigest"`
	Outcome             AdapterOutcome `json:"outcome"`
	ReceiptDigest       string         `json:"receiptDigest"`
	ResultID            uuid.UUID      `json:"resultId"`
	SchemaVersion       string         `json:"schemaVersion"`
}

func consumeReceiptKinds() map[string]valueKind {
	return map[string]valueKind{
		"adapterInvocationId": valueString, "adapterResultDigest": valueString,
		"authorityId": valueString, "completedAt": valueString, "envelopeDigest": valueString,
		"expectedFenceDigest": valueString, "fixtureId": valueString,
		"observedFenceDigest": valueString, "observedHeadDigest": valueString,
		"operationKind": valueString, "outcome": valueString, "payloadDigest": valueString,
		"predicateDigest": valueString, "reservedAt": valueString, "resolutionDigest": valueString,
		"resolvedFenceDigest": valueString, "resolvedHeadDigest": valueString,
		"resolvedResourceId": valueString, "resourceSelector": valueString, "resultId": valueString,
		"runId": valueString, "schemaVersion": valueString,
	}
}
