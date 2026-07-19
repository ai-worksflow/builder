package verification

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

func TestCanonicalReceiptRequiresReleaseEvidenceAndExactWorkspace(t *testing.T) {
	input := validCanonicalReceiptInput()
	receipt, err := NewCanonicalReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Decision != DecisionPassed || receipt.Scope != ScopeCanonical ||
		receipt.Subject.WorkspaceRevisionID != input.Subject.WorkspaceRevisionID {
		t.Fatalf("canonical Receipt = %#v", receipt)
	}
	parsed, err := ParseCanonicalReceipt(receipt)
	if err != nil || parsed.PayloadHash != receipt.PayloadHash {
		t.Fatalf("parse canonical Receipt = %#v, %v", parsed, err)
	}
	if reference, err := receipt.PassedReference(); err != nil || reference.ID != receipt.ID ||
		reference.ContentHash != receipt.PayloadHash {
		t.Fatalf("canonical passed reference = %#v, %v", reference, err)
	}
	storedHash, err := domain.CanonicalHash(receipt)
	if err != nil || "sha256:"+storedHash == receipt.PayloadHash {
		t.Fatalf("semantic Receipt hash was conflated with full content hash: %s, %v", storedHash, err)
	}

	reordered := input
	reordered.Checks[0], reordered.Checks[len(reordered.Checks)-1] =
		reordered.Checks[len(reordered.Checks)-1], reordered.Checks[0]
	again, err := NewCanonicalReceipt(reordered)
	if err != nil || again.PayloadHash != receipt.PayloadHash {
		t.Fatalf("canonical Receipt ordering changed hash: %s != %s, %v", again.PayloadHash, receipt.PayloadHash, err)
	}
}

func TestCanonicalReceiptRejectsCandidateOrMissingSupplyChainAuthority(t *testing.T) {
	input := validCanonicalReceiptInput()
	input.Checks = input.Checks[:len(input.Checks)-1]
	receipt, err := NewCanonicalReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Decision != DecisionFailed || receipt.BlockerCount == 0 {
		t.Fatalf("missing release check did not block canonical Receipt: %#v", receipt)
	}
	if _, err := receipt.PassedReference(); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("failed canonical Receipt produced publish authority: %v", err)
	}

	candidate, err := NewCandidateReceipt(validCandidateReceiptInput())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	var spoof CanonicalReceipt
	if err := json.Unmarshal(payload, &spoof); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCanonicalReceipt(spoof); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("Candidate Receipt was accepted as canonical: %v", err)
	}
}

func TestCanonicalReceiptRejectsPassedTruncatedEvidence(t *testing.T) {
	input := validCanonicalReceiptInput()
	input.Checks[0].Truncated = true
	if _, err := NewCanonicalReceipt(input); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("passed truncated Canonical check was accepted: %v", err)
	}
}

func validCanonicalReceiptInput() NewCanonicalReceiptInput {
	candidate := validCandidateReceiptInput()
	checks := append([]CheckResult(nil), candidate.Checks...)
	for _, release := range []struct {
		id   string
		kind string
	}{
		{id: "release-artifacts", kind: "release-manifest"},
		{id: "release-container-policy", kind: "container-policy"},
		{id: "release-sbom", kind: "sbom"},
		{id: "release-vulnerability", kind: "vulnerability"},
	} {
		check := candidate.Checks[0]
		check.ID, check.Kind, check.CommandID = release.id, release.kind, ""
		check.OracleIDs, check.AcceptanceCriterionIDs, check.ObligationIDs = []string{}, []string{}, []string{}
		checks = append(checks, check)
	}
	return NewCanonicalReceiptInput{
		ID: uuid.NewString(), RunID: uuid.NewString(), ProjectID: candidate.ProjectID,
		Subject: CanonicalPlanSubject{
			WorkspaceArtifactID: uuid.NewString(), WorkspaceRevisionID: uuid.NewString(),
			WorkspaceContentHash: hashFixture("canonical-receipt-workspace"),
		},
		BuildManifest: candidate.BuildManifest, BuildContract: candidate.BuildContract,
		FullStackTemplate: candidate.FullStackTemplate, Profile: candidate.Profile, Plan: candidate.Plan,
		AttemptIDs: candidate.AttemptIDs, Checks: checks, Obligations: candidate.Obligations,
		ReleaseArtifacts: []CanonicalReleaseArtifact{{
			ID: "service-api", Kind: "service-image", Store: "oci", Ref: "registry.example/api@sha256:fixture",
			ContentHash: hashFixture("canonical-service-image"), MediaType: "application/vnd.oci.image.manifest.v1+json",
			ByteSize: 4096,
		}},
		CreatedBy: candidate.CreatedBy, CreatedAt: candidate.CreatedAt,
	}
}
