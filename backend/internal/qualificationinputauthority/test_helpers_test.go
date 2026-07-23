package qualificationinputauthority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

var testIssuedAt = time.Date(2026, 7, 19, 12, 34, 56, 789000000, time.UTC)

type authorityResolverFunc func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (ResolvedAuthorities, error)

func (function authorityResolverFunc) Resolve(
	ctx context.Context,
	workflowInputAuthorityID uuid.UUID,
	policyAuthorityID uuid.UUID,
	planAuthorityID uuid.UUID,
) (ResolvedAuthorities, error) {
	return function(ctx, workflowInputAuthorityID, policyAuthorityID, planAuthorityID)
}

type serviceHarness struct {
	command         IssueCommand
	credentialCalls atomic.Int64
	resolved        ResolvedAuthorities
	service         *Service
	sourceCalls     atomic.Int64
	store           *MemoryStore
}

func newServiceHarness(t *testing.T) *serviceHarness {
	t.Helper()
	return newServiceHarnessWithBindings(
		t,
		ExecutableBinding{AuthorityID: "source-verifier-v1", ExecutableDigest: testDigest("source-executable")},
		ExecutableBinding{AuthorityID: "credential-resolver-v1", ExecutableDigest: testDigest("credential-executable")},
		testDigest("source-receipt"),
		testDigest("credential-receipt"),
	)
}

func newServiceHarnessWithBindings(
	t *testing.T,
	sourceBinding ExecutableBinding,
	credentialBinding ExecutableBinding,
	sourceReceiptHash string,
	credentialReceiptHash string,
) *serviceHarness {
	t.Helper()
	harness := &serviceHarness{resolved: testResolvedAuthorities(), store: NewMemoryStore()}
	harness.resolved.SourceVerifier = sourceBinding
	harness.resolved.CredentialResolver = credentialBinding
	if err := harness.store.InstallAuthorities(harness.resolved); err != nil {
		t.Fatal(err)
	}
	harness.command = IssueCommand{
		OperationID:                    uuid.New(),
		AuthorityID:                    uuid.New(),
		WorkflowInputAuthorityID:       uuid.MustParse(harness.resolved.WorkflowInput.AuthorityID),
		QualificationPolicyAuthorityID: uuid.MustParse(harness.resolved.Policy.AuthorityID),
		QualificationPlanAuthorityID:   uuid.MustParse(harness.resolved.Plan.AuthorityID),
	}
	sourceVerifier, err := NewSourceVerifier(sourceBinding, func(
		_ context.Context,
		_ SourceVerificationRequest,
		_ []byte,
		requestHash string,
	) (VerificationObservation, error) {
		harness.sourceCalls.Add(1)
		return VerificationObservation{ReceiptHash: sourceReceiptHash, RequestHash: requestHash}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	credentialResolver, err := NewCredentialResolver(credentialBinding, func(
		_ context.Context,
		_ CredentialResolutionRequest,
		_ []byte,
		requestHash string,
	) (VerificationObservation, error) {
		harness.credentialCalls.Add(1)
		return VerificationObservation{ReceiptHash: credentialReceiptHash, RequestHash: requestHash}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.service, err = NewService(
		harness.store,
		sourceVerifier,
		credentialResolver,
		harness.store,
		DatabaseClockFunc(func(context.Context) (time.Time, error) { return testIssuedAt, nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	return harness
}

func testResolvedAuthorities() ResolvedAuthorities {
	wiaID := uuid.NewString()
	policyID := uuid.NewString()
	planID := uuid.NewString()
	profile := CredentialProfile{
		Audience:               "urn:worksflow:qualification",
		AuthorityID:            "credential-authority-v1",
		IssuanceArtifactID:     "credential-set-issuance",
		MemberRequestSetDigest: testDigest("credential-member-request-set"),
		RevocationArtifactID:   "credential-set-revocation",
	}
	return ResolvedAuthorities{
		CredentialResolver: ExecutableBinding{
			AuthorityID: "credential-resolver-v1", ExecutableDigest: testDigest("credential-executable"),
		},
		WorkflowInput: WorkflowInputBinding{
			AuthorityHash:                    testDigest("wia-authority"),
			AuthorityID:                      wiaID,
			InputHash:                        testDigest("wia-input"),
			QualificationPolicyAuthorityHash: testDigest("policy-authority"),
			QualificationPolicyAuthorityID:   policyID,
		},
		Policy: PolicyBinding{
			AuthorityHash:        testDigest("policy-authority"),
			AuthorityID:          policyID,
			CredentialProfile:    profile,
			PlanInputProfileHash: testDigest("policy-plan-input-profile"),
			SourcePolicyDigest:   testDigest("source-policy"),
		},
		PolicyCurrent: true,
		PolicyStatus:  PolicyStatusActive,
		SourceVerifier: ExecutableBinding{
			AuthorityID: "source-verifier-v1", ExecutableDigest: testDigest("source-executable"),
		},
		Plan: PlanBinding{
			AuthorityHash:    testDigest("plan-authority"),
			AuthorityID:      planID,
			InputAuthorityID: wiaID,
			InputHash:        testDigest("plan-input"),
			Source: SourceProjection{
				Commit:           "0123456789abcdef0123456789abcdef01234567",
				Dirty:            false,
				TreeDigest:       testDigest("source-tree"),
				TreeDigestSchema: SourceTreeDigestSchemaV1,
			},
			CredentialSet: CredentialSetProjection{
				Audience:             profile.Audience,
				IssuanceArtifactID:   profile.IssuanceArtifactID,
				Issuer:               profile.AuthorityID,
				MemberBindingsDigest: testDigest("credential-member-bindings"),
				MemberCount:          3,
				RevocationArtifactID: profile.RevocationArtifactID,
				SetHandleHash:        testDigest("credential-set-handle"),
				SetID:                uuid.NewString(),
			},
		},
	}
}

func testDigest(label string) string {
	digest := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(digest[:])
}
