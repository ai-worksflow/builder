package core

import (
	"errors"
	"testing"
)

func TestRequireSoloSelfReview(t *testing.T) {
	valid := ProjectGovernance{Mode: GovernanceModeSolo, OwnerCount: 1}
	if err := RequireSoloSelfReview(valid, RoleOwner, true, "Reviewed risks and accepted them."); err != nil {
		t.Fatalf("valid solo self-review was rejected: %v", err)
	}
	for _, test := range []struct {
		name        string
		governance  ProjectGovernance
		role        Role
		confirmed   bool
		explanation string
		want        error
	}{
		{name: "team mode", governance: ProjectGovernance{Mode: GovernanceModeTeam, OwnerCount: 1}, role: RoleOwner, confirmed: true, explanation: "reviewed", want: ErrSelfApproval},
		{name: "second owner", governance: ProjectGovernance{Mode: GovernanceModeSolo, OwnerCount: 2}, role: RoleOwner, confirmed: true, explanation: "reviewed", want: ErrSelfApproval},
		{name: "non-owner", governance: valid, role: RoleAdmin, confirmed: true, explanation: "reviewed", want: ErrSelfApproval},
		{name: "not confirmed", governance: valid, role: RoleOwner, explanation: "reviewed", want: ErrSoloReviewConfirmation},
		{name: "missing explanation", governance: valid, role: RoleOwner, confirmed: true, want: ErrSoloReviewConfirmation},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := RequireSoloSelfReview(test.governance, test.role, test.confirmed, test.explanation); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}

func TestCanonicalReviewApprovalDecisionUsesImmutablePolicySnapshot(t *testing.T) {
	author, reviewer := "owner-a", "owner-b"
	soloPolicy := ReviewPolicy{
		ReviewerIDs: []string{author}, MinimumApprovals: 1, ProhibitSelfReview: true,
		GovernanceMode: GovernanceModeSolo, SoloSelfReviewOwnerID: author,
	}
	if !CanonicalReviewApprovalDecision(soloPolicy, author, author, true) {
		t.Fatal("explicit solo self-review evidence should remain canonical")
	}
	if CanonicalReviewApprovalDecision(soloPolicy, author, author, false) {
		t.Fatal("unmarked self-review decision became canonical")
	}
	teamPolicy := soloPolicy
	teamPolicy.GovernanceMode = GovernanceModeTeam
	if CanonicalReviewApprovalDecision(teamPolicy, author, author, true) {
		t.Fatal("team policy was reinterpreted as solo self-review")
	}
	independent := ReviewPolicy{ReviewerIDs: []string{reviewer}, MinimumApprovals: 1, ProhibitSelfReview: true, GovernanceMode: GovernanceModeTeam}
	if !CanonicalReviewApprovalDecision(independent, reviewer, author, false) {
		t.Fatal("assigned independent approval was rejected")
	}
	if CanonicalReviewApprovalDecision(independent, "unassigned", author, false) {
		t.Fatal("unassigned decision became canonical")
	}
}
