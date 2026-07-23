package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
)

func TestDecodeReviewPolicyRequiresCanonicalCurrentAuthority(t *testing.T) {
	t.Parallel()
	validReviewer := uuid.NewString()
	valid := json.RawMessage(`{"reviewerIds":["` + validReviewer + `"],"minimumApprovals":1,"prohibitSelfReview":true,"governanceMode":"team"}`)
	if policy, err := decodeReviewPolicy(valid); err != nil || len(policy.ReviewerIDs) != 1 {
		t.Fatalf("valid current policy = %#v, %v", policy, err)
	}
	zero := "00000000-0000-0000-0000-000000000000"
	tests := map[string]json.RawMessage{
		"missing governance": json.RawMessage(`{"reviewerIds":["` + validReviewer + `"],"minimumApprovals":1,"prohibitSelfReview":true}`),
		"zero reviewer":      json.RawMessage(`{"reviewerIds":["` + zero + `"],"minimumApprovals":1,"prohibitSelfReview":true,"governanceMode":"team"}`),
		"zero Solo Owner":    json.RawMessage(`{"reviewerIds":["` + zero + `"],"minimumApprovals":1,"prohibitSelfReview":true,"governanceMode":"solo","soloSelfReviewOwnerId":"` + zero + `"}`),
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeReviewPolicy(value); err == nil {
				t.Fatal("noncanonical current policy was accepted")
			}
		})
	}
}

func TestReviewEntityTagUsesTheClosedNonNegativeNanosecondDomain(t *testing.T) {
	t.Parallel()
	request := storage.ReviewRequestModel{ID: uuid.New(), Status: "open"}
	preEpoch := storage.ReviewDecisionModel{CreatedAt: time.Date(1960, 1, 2, 3, 4, 5, 0, time.UTC)}
	outOfRange := storage.ReviewDecisionModel{CreatedAt: time.Date(3000, 1, 2, 3, 4, 5, 0, time.UTC)}
	want := `"review:` + request.ID.String() + `:open:2:0"`
	if got := reviewEntityTag(request, []storage.ReviewDecisionModel{preEpoch, outOfRange}); got != want {
		t.Fatalf("pre-epoch/out-of-range ETag = %q, want %q", got, want)
	}
}

func TestCanonicalReviewCeilingMillisecond(t *testing.T) {
	t.Parallel()
	createdAt := time.Date(2026, 7, 19, 1, 2, 3, 456789000, time.UTC)
	got := canonicalReviewCeilingMillisecond(createdAt)
	want := time.Date(2026, 7, 19, 1, 2, 3, 457000000, time.UTC)
	if !got.Equal(want) || got.Before(createdAt) {
		t.Fatalf("causal millisecond = %s, want %s", got, want)
	}
}

func TestCanonicalReviewDigest(t *testing.T) {
	t.Parallel()
	valid := "sha256:" + strings.Repeat("0a", 32)
	if !canonicalReviewDigest(valid) {
		t.Fatal("canonical lowercase SHA-256 digest was rejected")
	}
	for name, value := range map[string]string{
		"uppercase": "sha256:" + strings.Repeat("0A", 32),
		"short":     "sha256:" + strings.Repeat("0a", 31),
		"prefix":    "SHA256:" + strings.Repeat("0a", 32),
		"non-hex":   "sha256:" + strings.Repeat("0a", 31) + "0g",
	} {
		t.Run(name, func(t *testing.T) {
			if canonicalReviewDigest(value) {
				t.Fatalf("noncanonical digest %q was accepted", value)
			}
		})
	}
}

func TestCanonicalReviewCausalDecisionTimeRejectsUpperBoundaryOverflow(t *testing.T) {
	t.Parallel()
	upperBoundary := time.Date(2261, 12, 31, 23, 59, 59, 999500000, time.UTC)
	if got, ok := canonicalReviewCausalDecisionTime(
		time.Date(2261, 12, 31, 23, 59, 59, 999000000, time.UTC),
		upperBoundary,
		nil,
	); ok || canonicalReviewTimeInDomain(got) {
		t.Fatalf("upper-bound causal time = %s, ok=%t; want an out-of-domain rejection", got, ok)
	}

	requestTime := time.Date(2026, 7, 19, 1, 2, 3, 456789000, time.UTC)
	got, ok := canonicalReviewCausalDecisionTime(requestTime, requestTime, nil)
	want := time.Date(2026, 7, 19, 1, 2, 3, 457000000, time.UTC)
	if !ok || !got.Equal(want) {
		t.Fatalf("ordinary causal time = %s, ok=%t; want %s", got, ok, want)
	}
}

func TestCanonicalReviewApprovalCapacityExcludesTheRevisionAuthorWithoutExactSoloAuthority(t *testing.T) {
	t.Parallel()
	author := uuid.New()
	reviewer := uuid.New()
	tests := []struct {
		name      string
		reviewers []uuid.UUID
		soloOwner string
		want      int
	}{
		{name: "author only", reviewers: []uuid.UUID{author}, want: 0},
		{name: "exact Solo author", reviewers: []uuid.UUID{author}, soloOwner: author.String(), want: 1},
		{name: "independent reviewer", reviewers: []uuid.UUID{author, reviewer}, want: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := canonicalReviewApprovalCapacity(nil, uuid.New(), test.reviewers, author, test.soloOwner)
			if err != nil || got != test.want {
				t.Fatalf("approval capacity = %d, %v; want %d", got, err, test.want)
			}
		})
	}
}
