package canonicalreviewreceipt

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalJSONUsesPostgresCompatibleUTF8AndEscapes(t *testing.T) {
	value := map[string]any{
		"中文":    "中文 <>& \" \\ \b\f\n\r\t\x01 \u2028\u2029",
		"alpha": json.Number("42"),
	}
	encoded, err := CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("{\"alpha\":42,\"中文\":\"中文 <>& \\\" \\\\ \\b\\f\\n\\r\\t\\u0001 \u2028\u2029\"}")
	if !bytes.Equal(encoded, want) {
		t.Fatalf("canonical JSON mismatch\n got: %q\nwant: %q", encoded, want)
	}
	if bytes.Contains(encoded, []byte(`\u003c`)) || bytes.Contains(encoded, []byte(`\u2028`)) {
		t.Fatalf("valid UTF-8 was unnecessarily escaped: %s", encoded)
	}
}

func TestCanonicalReviewReceiptGoldenVectorAndStrictDecode(t *testing.T) {
	receipt := goldenReceipt()
	compiled, err := Compile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Hash != "sha256:7bcb8a58bb3bc33575ae26bfb51e49d18f1433732cde78ab664b1b10165780f1" {
		t.Fatalf("golden hash drifted: %s\n%s", compiled.Hash, compiled.Bytes)
	}
	decoded, err := Decode(compiled.Bytes, compiled.Hash)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compile(decoded)
	if err != nil || !bytes.Equal(compiled.Bytes, second.Bytes) || compiled.Hash != second.Hash {
		t.Fatalf("round trip drifted: %v", err)
	}

	unknown := bytes.Replace(compiled.Bytes, []byte(`"mediaType":`), []byte(`"future":true,"mediaType":`), 1)
	if _, err := Decode(unknown, ""); err == nil {
		t.Fatal("unknown field was accepted")
	}
	duplicate := bytes.Replace(compiled.Bytes, []byte(`"mediaType":`), []byte(`"mediaType":"x","mediaType":`), 1)
	if _, err := Decode(duplicate, ""); err == nil || !strings.Contains(err.Error(), "duplicate field") {
		t.Fatalf("duplicate field error = %v", err)
	}
	nonCanonical := append([]byte(" "), compiled.Bytes...)
	if _, err := Decode(nonCanonical, ""); err == nil {
		t.Fatal("non-canonical whitespace was accepted")
	}
}

func TestCanonicalReviewReceiptRejectsSemanticDrift(t *testing.T) {
	tests := map[string]func(*Receipt){
		"duplicate policy reviewer": func(receipt *Receipt) {
			receipt.Policy.Value.ReviewerIDs = append(receipt.Policy.Value.ReviewerIDs, receipt.Policy.Value.ReviewerIDs[0])
		},
		"null policy reviewer set": func(receipt *Receipt) {
			receipt.Policy.Value.ReviewerIDs = nil
		},
		"empty precondition": func(receipt *Receipt) {
			receipt.Decisions.Decisions[0].AuthorityFacts.PreconditionETag = ""
		},
		"forged precondition": func(receipt *Receipt) {
			receipt.Decisions.Decisions[0].AuthorityFacts.PreconditionETag = `"review:forged:open:0:0"`
		},
		"leading tab summary": func(receipt *Receipt) {
			receipt.Decisions.Decisions[0].Summary = "\tapproved"
		},
		"leading non-breaking space summary": func(receipt *Receipt) {
			receipt.Decisions.Decisions[0].Summary = "\u00a0approved"
		},
		"trailing ideographic space summary": func(receipt *Receipt) {
			receipt.Decisions.Decisions[0].Summary = "approved\u3000"
		},
		"zero UUID": func(receipt *Receipt) {
			receipt.ReviewRequest.RequestedBy = "00000000-0000-0000-0000-000000000000"
		},
		"timestamp outside Unix nanosecond authority domain": func(receipt *Receipt) {
			receipt.ReviewRequest.RequestedAt = "1000-01-01T00:00:00.000000Z"
			receipt.Revision.CreatedAt = "0999-01-01T00:00:00.000000Z"
		},
		"normalized 24-hour timestamp": func(receipt *Receipt) {
			receipt.ReviewRequest.RequestedAt = "2026-07-18T24:00:00.000000Z"
		},
		"normalized leap-second timestamp": func(receipt *Receipt) {
			receipt.ReviewRequest.RequestedAt = "2026-07-18T23:59:60.000000Z"
		},
		"unassigned optional Solo Owner": func(receipt *Receipt) {
			receipt.Policy.Value.ReviewerIDs = []string{}
		},
		"unused Solo Owner does not author the revision": func(receipt *Receipt) {
			author := "99999999-9999-4999-8999-999999999999"
			receipt.Revision.CreatedBy = author
			receipt.Approval.SubjectAuthorID = author
			receipt.Decisions.Decisions[0].SoloSelfReview = false
			receipt.Decisions.Decisions[0].AuthorityFacts.ExplicitConfirmation = false
			receipt.Approval.SoloSelfReview = false
		},
		"governance drift": func(receipt *Receipt) {
			receipt.Decisions.Decisions[0].AuthorityFacts.GovernanceMode = "team"
		},
		"superseded approval": func(receipt *Receipt) {
			value := receipt.Revision.ApprovedAt
			receipt.Revision.SupersededAt = &value
		},
		"byte size above JavaScript-safe integer": func(receipt *Receipt) {
			receipt.Revision.ByteSize = maximumSafeInt + 1
		},
		"revision number above JavaScript-safe integer": func(receipt *Receipt) {
			receipt.Revision.RevisionNumber = maximumSafeInt + 1
		},
		"artifact version above JavaScript-safe integer": func(receipt *Receipt) {
			receipt.Approval.ArtifactVersion = maximumSafeInt + 1
		},
		"closing decision is not last": func(receipt *Receipt) {
			second := receipt.Decisions.Decisions[0]
			second.ID = "77777777-7777-4777-8777-777777777777"
			second.ReviewerID = "88888888-8888-4888-8888-888888888888"
			second.SoloSelfReview = false
			second.AuthorityFacts.ExplicitConfirmation = false
			second.AuthorityFacts.ReviewerRole = "editor"
			second.CreatedAt = receipt.ReviewRequest.ClosedAt
			receipt.Policy.Value.ReviewerIDs = append(receipt.Policy.Value.ReviewerIDs, second.ReviewerID)
			receipt.Policy.Value.MinimumApprovals = 2
			receipt.Decisions.Decisions = append(receipt.Decisions.Decisions, second)
			receipt.Approval.MinimumApprovals = 2
			receipt.Approval.ApprovalCount = 2
			receipt.Approval.ApprovalDecisionIDs = append(receipt.Approval.ApprovalDecisionIDs, second.ID)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			receipt := goldenReceipt()
			mutate(&receipt)
			if _, err := Compile(receipt); err == nil {
				t.Fatal("semantic drift was accepted")
			}
		})
	}
}

func TestCanonicalReviewReceiptAcceptsJavaScriptSafeIntegerBoundary(t *testing.T) {
	t.Parallel()
	receipt := goldenReceipt()
	receipt.Revision.ByteSize = maximumSafeInt
	receipt.Revision.RevisionNumber = maximumSafeInt
	receipt.Approval.ArtifactVersion = maximumSafeInt
	if _, err := Compile(receipt); err != nil {
		t.Fatalf("JavaScript-safe integer boundary was rejected: %v", err)
	}
}

func TestCanonicalReviewReceiptAcceptsEveryPlatformContractArtifactKind(t *testing.T) {
	t.Parallel()
	for _, artifactKind := range []string{"ai_runtime_contract", "deployment_contract", "verification_contract"} {
		artifactKind := artifactKind
		t.Run(artifactKind, func(t *testing.T) {
			t.Parallel()
			receipt := goldenReceipt()
			receipt.Approval.ArtifactKind = artifactKind
			if _, err := Compile(receipt); err != nil {
				t.Fatalf("valid platform artifact kind %q was rejected: %v", artifactKind, err)
			}
		})
	}
}

func goldenReceipt() Receipt {
	owner := "11111111-1111-4111-8111-111111111111"
	project := "22222222-2222-4222-8222-222222222222"
	artifact := "33333333-3333-4333-8333-333333333333"
	revision := "44444444-4444-4444-8444-444444444444"
	request := "55555555-5555-4555-8555-555555555555"
	decision := "66666666-6666-4666-8666-666666666666"
	contentHash := "sha256:" + strings.Repeat("a", 64)
	timeValue := "2026-07-19T01:02:03.456000Z"
	return Receipt{
		IssuedAt: timeValue,
		ReviewRequest: ReviewRequestSnapshot{ArtifactID: artifact, ClosedAt: timeValue, ClosedByDecisionID: decision,
			ContentHash: contentHash, ID: request, ProjectID: project, RequestedAt: "2026-07-19T00:00:00.000000Z",
			RequestedBy: owner, ReviewAuthorityVersion: 1, RevisionID: revision, Status: "approved"},
		Revision: RevisionSnapshot{ApprovedAt: timeValue, ArtifactID: artifact, ArtifactSchemaVersion: 1, ByteSize: 12,
			ChangeSource: "human", ChangeSummary: "Initial", ContentHash: contentHash, ContentRef: "objects/revision",
			ContentStore: "repository", CreatedAt: "2026-07-18T00:00:00.000000Z", CreatedBy: owner, ID: revision,
			RevisionNumber: 1, WorkflowStatus: "approved"},
		Policy: PolicySnapshot{Value: PolicyValue{GovernanceMode: "solo", MinimumApprovals: 1, ProhibitSelfReview: true,
			ReviewerIDs: []string{owner}, SoloSelfReviewOwnerID: &owner}},
		Decisions: DecisionsSnapshot{Decisions: []DecisionSnapshot{{ID: decision, ReviewerID: owner, Decision: "approve",
			Summary: "Reviewed scope and accepted risk.", SoloSelfReview: true, CreatedAt: timeValue,
			AuthorityFacts: DecisionAuthorityFacts{Version: 1, ReviewerRole: "owner", GovernanceMode: "solo", OwnerCount: 1,
				SoleOwnerID: &owner, ExplicitConfirmation: true,
				PreconditionETag: `"review:55555555-5555-4555-8555-555555555555:open:0:0"`}}}},
		Governance: GovernanceSnapshot{Mode: "solo", OwnerCount: 1, SoleOwnerID: &owner},
		Approval: ApprovalSnapshot{ApprovalCount: 1, ApprovalDecisionIDs: []string{decision}, ApprovedAt: timeValue,
			ArtifactID: artifact, ArtifactKind: "product_requirements", ArtifactLatestApprovedID: revision,
			ArtifactLatestRevisionID: revision, ArtifactLifecycle: "active", ArtifactVersion: 2, ClosedByDecisionID: decision,
			MinimumApprovals: 1, ProjectID: project, RevisionContentHash: contentHash, RevisionID: revision,
			SoloSelfReview: true, SubjectAuthorID: owner},
	}
}
