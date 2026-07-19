package qualificationreceipt

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/goldenfault"
	"github.com/worksflow/builder/backend/internal/templateauthority"
)

const maximumGoldenFaultArtifactBytes = 1 << 20

type verifiedGoldenFaultEvidence struct {
	authoritySignerIdentities []string
	attestationDigest         string
	attestorSignerIdentities  []string
}

func (verifier *Verifier) verifyGoldenFaultEvidence(
	root string,
	receipt QualificationReceipt,
	expected ExpectedPromotion,
	artifacts verifiedArtifactSet,
	fixture parsedGoldenFixture,
) (verifiedGoldenFaultEvidence, error) {
	if verifier.faultAuthority == nil || verifier.faultLedgerAttestor.verifier == nil {
		return verifiedGoldenFaultEvidence{}, errors.New("Golden fault evidence verifiers are not configured")
	}
	if err := validateExpectedGoldenFaultOperationSet(fixture.faults, expected.GoldenRuntime.FaultOperationSetDigest); err != nil {
		return verifiedGoldenFaultEvidence{}, err
	}
	authorityDescriptors := make(map[string]ArtifactDescriptor)
	receiptDescriptors := make(map[string]ArtifactDescriptor)
	var attestationDescriptor ArtifactDescriptor
	attestationCount := 0
	for _, descriptor := range artifacts.byID {
		switch descriptor.Type {
		case ArtifactTypeGoldenFaultAuthority:
			authorityDescriptors[descriptor.ID] = descriptor
		case ArtifactTypeGoldenFaultReceipt:
			receiptDescriptors[descriptor.ID] = descriptor
		case ArtifactTypeGoldenFaultLedger:
			attestationDescriptor = descriptor
			attestationCount++
		}
	}
	if len(fixture.faults) == 0 || len(fixture.faults) > 64 ||
		len(authorityDescriptors) != len(fixture.faults) || len(receiptDescriptors) != len(fixture.faults) ||
		attestationCount != 1 || attestationDescriptor.ID != GoldenFaultLedgerArtifactID ||
		attestationDescriptor.MediaType != DSSEEnvelopeMediaType ||
		attestationDescriptor.Classification != ClassificationDistributable {
		return verifiedGoldenFaultEvidence{}, errors.New("Golden fault artifact cardinality does not exactly match the fixture")
	}
	for _, fault := range fixture.faults {
		descriptor, exists := authorityDescriptors[fault.DSSE.ArtifactID]
		if !exists || descriptor.SHA256 != fault.DSSE.EnvelopeDigest ||
			descriptor.Type != ArtifactTypeGoldenFaultAuthority || descriptor.MediaType != DSSEEnvelopeMediaType ||
			descriptor.Classification != ClassificationDistributable {
			return verifiedGoldenFaultEvidence{}, fmt.Errorf("Golden fault authority %q does not close its exact indexed DSSE artifact", fault.AuthorityID)
		}
	}
	fixtureDescriptor, exists := artifacts.byID[expected.GoldenRuntime.FixtureDocumentArtifactID]
	if !exists {
		return verifiedGoldenFaultEvidence{}, errors.New("Golden fixture artifact is unavailable for ledger attestation subject binding")
	}
	attestationBytes, err := readVerifiedArtifact(root, attestationDescriptor, maximumGoldenFaultArtifactBytes)
	if err != nil {
		return verifiedGoldenFaultEvidence{}, fmt.Errorf("read Golden fault ledger attestation: %w", err)
	}
	verifiedAttestation, err := verifier.faultLedgerAttestor.verifier.Verify(attestationBytes, templateauthority.ExpectedSubject{
		Name:         "worksflow-golden-fixture/" + expected.GoldenRuntime.FixtureID,
		SHA256Digest: fixtureDescriptor.SHA256,
	})
	if err != nil {
		return verifiedGoldenFaultEvidence{}, fmt.Errorf("verify Golden fault ledger attestation DSSE: %w", err)
	}
	if !bytes.Equal(attestationBytes, verifiedAttestation.CanonicalEnvelope) {
		return verifiedGoldenFaultEvidence{}, errors.New("Golden fault ledger attestation must use exact canonical DSSE envelope bytes")
	}
	for _, identity := range verifiedAttestation.SignerIdentities {
		if _, allowed := verifier.faultLedgerAttestor.allowedIdentities[identity]; !allowed {
			return verifiedGoldenFaultEvidence{}, fmt.Errorf("Golden fault ledger attestor identity %q is not allowed", identity)
		}
	}
	if err := requireCanonicalEvidenceJSON(verifiedAttestation.Payload, "Golden fault ledger in-toto payload"); err != nil {
		return verifiedGoldenFaultEvidence{}, err
	}
	statement, err := parseStatement(verifiedAttestation.Payload, GoldenFaultLedgerPredicateTypeV1)
	if err != nil {
		return verifiedGoldenFaultEvidence{}, err
	}
	if err := requireExactShape(statement.Predicate, goldenFaultLedgerAttestationShape()); err != nil {
		return verifiedGoldenFaultEvidence{}, fmt.Errorf("validate Golden fault ledger attestation shape: %w", err)
	}
	if err := requireCanonicalEvidenceJSON(statement.Predicate, "Golden fault ledger predicate"); err != nil {
		return verifiedGoldenFaultEvidence{}, err
	}
	var attestation GoldenFaultLedgerAttestation
	if err := decodeStrictJSON(statement.Predicate, &attestation); err != nil {
		return verifiedGoldenFaultEvidence{}, fmt.Errorf("decode Golden fault ledger attestation: %w", err)
	}
	if attestation.SchemaVersion != GoldenFaultLedgerSchemaV1 || attestation.Status != "terminal" ||
		attestation.FixtureID != expected.GoldenRuntime.FixtureID || attestation.RunID != expected.RunID ||
		len(attestation.Entries) != len(fixture.faults) {
		return verifiedGoldenFaultEvidence{}, errors.New("Golden fault ledger attestation does not bind the exact terminal fixture/run closure")
	}
	attestedAt, err := parseCanonicalTime(attestation.IssuedAt, "goldenFaultLedger.issuedAt")
	if err != nil {
		return verifiedGoldenFaultEvidence{}, err
	}
	runStartedAt, err := parseCanonicalTime(receipt.StartedAt, "receipt.startedAt")
	if err != nil {
		return verifiedGoldenFaultEvidence{}, err
	}
	runCompletedAt, err := parseCanonicalTime(receipt.CompletedAt, "receipt.completedAt")
	if err != nil {
		return verifiedGoldenFaultEvidence{}, err
	}
	receiptIssuedAt, err := parseCanonicalTime(receipt.IssuedAt, "receipt.issuedAt")
	if err != nil {
		return verifiedGoldenFaultEvidence{}, err
	}
	if !attestedAt.After(runCompletedAt) || !attestedAt.Before(receiptIssuedAt) {
		return verifiedGoldenFaultEvidence{}, errors.New("Golden fault ledger attestation must be issued after run completion and before receipt issuance")
	}
	if err := validateAuthoritySignerValidity(verifier.faultLedgerAttestor, verifiedAttestation, attestedAt, "Golden fault ledger attestation"); err != nil {
		return verifiedGoldenFaultEvidence{}, err
	}
	// issuedAt is asserted by the attestor, while the qualification Receipt time
	// is independently fixed by the root promotion authority and signed by the
	// runner/approver domain. Requiring the attestor key to remain valid through
	// that later time prevents a revoked or expired key from minting a newly
	// backdated ledger that independent Receipt signers could otherwise seal.
	if err := validateAuthoritySignerValidity(verifier.faultLedgerAttestor, verifiedAttestation, receiptIssuedAt, "Golden fault ledger attestation at trusted receipt issuance"); err != nil {
		return verifiedGoldenFaultEvidence{}, err
	}
	entryByAuthority := make(map[string]GoldenFaultLedgerEntry, len(attestation.Entries))
	priorAuthorityID := ""
	for index, entry := range attestation.Entries {
		if !validUUID(entry.AuthorityID) || (index > 0 && priorAuthorityID >= entry.AuthorityID) {
			return verifiedGoldenFaultEvidence{}, errors.New("Golden fault ledger entries must be uniquely sorted by authorityId")
		}
		if _, duplicate := entryByAuthority[entry.AuthorityID]; duplicate {
			return verifiedGoldenFaultEvidence{}, errors.New("Golden fault ledger contains a duplicate authority")
		}
		entryByAuthority[entry.AuthorityID] = entry
		priorAuthorityID = entry.AuthorityID
	}
	seenReceipts := make(map[string]struct{}, len(fixture.faults))
	seenAdapterInvocations := make(map[uuid.UUID]struct{}, len(fixture.faults))
	authorityIdentities := make(map[string]struct{})
	for _, fault := range fixture.faults {
		entry, exists := entryByAuthority[fault.AuthorityID]
		if !exists {
			return verifiedGoldenFaultEvidence{}, fmt.Errorf("Golden fault authority %q is missing from the ledger attestation", fault.AuthorityID)
		}
		selector, expectedOutcome, supported := goldenFaultContract(fault.OperationKind)
		if !supported || selector != fault.ResourceSelector || entry.OperationKind != fault.OperationKind ||
			entry.State != "terminal" || entry.Outcome != expectedOutcome || !validUUID(entry.ResultID) ||
			entry.ReceiptArtifactID != entry.ResultID || entry.ResultID == entry.AuthorityID ||
			!validDigest(entry.AuthorityDigest) || !validDigest(entry.EnvelopeDigest) ||
			!validDigest(entry.PayloadDigest) || !validDigest(entry.ReservationDigest) ||
			!validDigest(entry.ResultDigest) || !validDigest(entry.ReceiptDigest) {
			return verifiedGoldenFaultEvidence{}, fmt.Errorf("Golden fault ledger entry %q is not a closed terminal operation", entry.AuthorityID)
		}
		reservedAt, err := parseCanonicalTime(entry.ReservedAt, "goldenFaultLedger.entries.reservedAt")
		if err != nil {
			return verifiedGoldenFaultEvidence{}, err
		}
		completedAt, err := parseCanonicalTime(entry.CompletedAt, "goldenFaultLedger.entries.completedAt")
		if err != nil || reservedAt.Before(runStartedAt) || completedAt.Before(reservedAt) ||
			completedAt.After(runCompletedAt) || attestedAt.Before(completedAt) {
			return verifiedGoldenFaultEvidence{}, errors.New("Golden fault reserved/completed/attested chronology is invalid")
		}
		authorityID, _ := uuid.Parse(fault.AuthorityID)
		fixtureID, _ := uuid.Parse(expected.GoldenRuntime.FixtureID)
		runID, _ := uuid.Parse(expected.RunID)
		authorityDescriptor := authorityDescriptors[fault.DSSE.ArtifactID]
		authorityEnvelope, err := readVerifiedArtifact(root, authorityDescriptor, maximumGoldenFaultArtifactBytes)
		if err != nil {
			return verifiedGoldenFaultEvidence{}, fmt.Errorf("read Golden fault authority %q: %w", fault.AuthorityID, err)
		}
		verifiedAuthority, err := verifier.faultAuthority.VerifyAt(authorityEnvelope, goldenfault.ExpectedBinding{
			AuthorityID: authorityID, FixtureID: fixtureID, RunID: runID,
			OperationKind: goldenfault.OperationKind(fault.OperationKind), ResourceSelector: fault.ResourceSelector,
			ExpectedFenceDigest: fault.ExpectedFenceDigest, EnvelopeDigest: fault.DSSE.EnvelopeDigest,
			PayloadDigest: fault.DSSE.PayloadDigest,
		}, reservedAt)
		if err != nil {
			return verifiedGoldenFaultEvidence{}, fmt.Errorf("verify historical Golden fault authority %q at attested reservedAt: %w", fault.AuthorityID, err)
		}
		for _, identity := range verifiedAuthority.SignerIdentities {
			if _, allowed := verifier.faultAuthorityAllowed[identity]; !allowed {
				return verifiedGoldenFaultEvidence{}, fmt.Errorf("Golden fault operator identity %q is not allowed", identity)
			}
			authorityIdentities[identity] = struct{}{}
		}
		receiptDescriptor, exists := receiptDescriptors[entry.ReceiptArtifactID]
		if !exists || receiptDescriptor.Type != ArtifactTypeGoldenFaultReceipt ||
			receiptDescriptor.MediaType != CanonicalJSONMediaType ||
			receiptDescriptor.Classification != ClassificationDistributable ||
			receiptDescriptor.SHA256 != entry.ReceiptDigest {
			return verifiedGoldenFaultEvidence{}, fmt.Errorf("Golden fault consume receipt %q is missing or drifts from the attestation", entry.ReceiptArtifactID)
		}
		if _, duplicate := seenReceipts[receiptDescriptor.ID]; duplicate {
			return verifiedGoldenFaultEvidence{}, errors.New("Golden fault consume receipt is reused by multiple authorities")
		}
		seenReceipts[receiptDescriptor.ID] = struct{}{}
		receiptBytes, err := readVerifiedArtifact(root, receiptDescriptor, maximumGoldenFaultArtifactBytes)
		if err != nil {
			return verifiedGoldenFaultEvidence{}, fmt.Errorf("read Golden fault consume receipt %q: %w", receiptDescriptor.ID, err)
		}
		reservation, terminal, err := goldenfault.ValidateConsumeReceiptEvidence(verifiedAuthority, receiptBytes)
		if err != nil {
			return verifiedGoldenFaultEvidence{}, fmt.Errorf("validate Golden fault consume receipt %q: %w", receiptDescriptor.ID, err)
		}
		if _, duplicate := seenAdapterInvocations[reservation.AdapterInvocationID]; duplicate {
			return verifiedGoldenFaultEvidence{}, errors.New("Golden fault adapter invocation is reused by multiple authorities")
		}
		seenAdapterInvocations[reservation.AdapterInvocationID] = struct{}{}
		reservationDigest, err := goldenfault.ReservationEvidenceDigest(reservation)
		if err != nil {
			return verifiedGoldenFaultEvidence{}, err
		}
		resultDigest, err := goldenfault.TerminalEvidenceDigest(terminal)
		if err != nil {
			return verifiedGoldenFaultEvidence{}, err
		}
		if terminal.ResultID.String() != entry.ResultID || string(terminal.Outcome) != expectedOutcome ||
			terminal.Receipt.OperationKind != goldenfault.OperationKind(fault.OperationKind) ||
			terminal.Receipt.ReservedAt != entry.ReservedAt || terminal.Receipt.CompletedAt != entry.CompletedAt ||
			reservation.PredicateDigest != entry.AuthorityDigest || reservation.EnvelopeDigest != entry.EnvelopeDigest ||
			reservation.PayloadDigest != entry.PayloadDigest || reservationDigest != entry.ReservationDigest ||
			resultDigest != entry.ResultDigest || terminal.ReceiptDigest != entry.ReceiptDigest ||
			entry.EnvelopeDigest != fault.DSSE.EnvelopeDigest || entry.PayloadDigest != fault.DSSE.PayloadDigest {
			return verifiedGoldenFaultEvidence{}, fmt.Errorf("Golden fault authority/reservation/result/receipt closure drift for %q", fault.AuthorityID)
		}
	}
	if len(seenReceipts) != len(receiptDescriptors) || len(entryByAuthority) != len(fixture.faults) {
		return verifiedGoldenFaultEvidence{}, errors.New("Golden fault ledger contains missing or extra authority/result/receipt evidence")
	}
	authorityIdentityList := make([]string, 0, len(authorityIdentities))
	for identity := range authorityIdentities {
		authorityIdentityList = append(authorityIdentityList, identity)
	}
	sort.Strings(authorityIdentityList)
	return verifiedGoldenFaultEvidence{
		authoritySignerIdentities: authorityIdentityList,
		attestationDigest:         attestationDescriptor.SHA256,
		attestorSignerIdentities:  append([]string(nil), verifiedAttestation.SignerIdentities...),
	}, nil
}

func requireCanonicalEvidenceJSON(encoded []byte, label string) error {
	value, err := decodeJSONValue(encoded)
	if err != nil {
		return fmt.Errorf("decode %s: %w", label, err)
	}
	canonical, err := canonicalJSONBytes(value)
	if err != nil || !bytes.Equal(encoded, canonical) {
		return fmt.Errorf("%s must use exact canonical JSON bytes", label)
	}
	return nil
}
