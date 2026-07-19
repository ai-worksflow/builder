package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
)

func TestParseOptionsRequiresOnlyEvidenceAndRootOwnedAuthorityPaths(t *testing.T) {
	root := t.TempDir()
	arguments := []string{
		"--repository-root", root,
		"--receipt", filepath.Join(root, "receipt.dsse.json"),
		"--artifact-index", filepath.Join(root, "artifact-index.json"),
		"--artifact-root", filepath.Join(root, "artifacts"),
		"--promotion-authority", filepath.Join(root, "promotion-authority.json"),
	}
	options, err := parseOptions(arguments)
	if err != nil {
		t.Fatalf("parse complete options: %v", err)
	}
	if options.manifestPath != "qualification/manifest.json" || options.promotionAuthorityPath != arguments[len(arguments)-1] {
		t.Fatalf("unexpected secure defaults: %+v", options)
	}

	incomplete := append([]string(nil), arguments[:len(arguments)-2]...)
	if _, err := parseOptions(incomplete); err == nil {
		t.Fatal("missing root-owned promotion authority was accepted")
	}
}

type fakePromotionVerifier struct {
	result   qualificationreceipt.VerifiedPromotion
	err      error
	expected *qualificationreceipt.ExpectedPromotion
}

func (verifier fakePromotionVerifier) Verify(_ string, _ string, _ string, expected qualificationreceipt.ExpectedPromotion) (qualificationreceipt.VerifiedPromotion, error) {
	if verifier.expected != nil {
		*verifier.expected = expected
	}
	return verifier.result, verifier.err
}

func TestRunBindsAuthorityAndRejectsMidRunDrift(t *testing.T) {
	root := t.TempDir()
	evidenceRoot := filepath.Join(root, "evidence")
	authority := qualificationreceipt.PromotionAuthority{
		Digest: digest('0'), PromotionTarget: qualificationreceipt.PromotionTarget{
			ProjectID: "4a4554f6-f270-4ce7-9323-78510af0f91a", WorkflowRunID: "d0118815-af3f-491f-9e26-bf43684a5263",
			NodeKey: "external-qualification", TargetRevision: qualificationreceipt.PromotionTargetRevision{
				ID: "f87f0f42-3a01-4c60-bcb9-781da473b945", ContentHash: digest('5'),
			}, Subject: "ai-constructor", StageGate: qualificationreceipt.ExternalQualificationGate,
		},
		AuthorityNonce: "88172ca8-8507-4a7a-bf86-f89f58ac859d", AuthorityIssuedAt: "2026-07-18T12:00:00.000Z", AuthorityExpiresAt: "2026-07-18T12:10:00.000Z",
		RunID: "d5d9a265-d09d-40e7-b643-f2a48698dc9b", PlanDigest: digest('a'),
		PrePromotionManifestDigest: digest('b'), RepositoryRoot: root,
		QualificationManifestPath: "qualification/manifest.json", EvidenceSnapshotRoot: evidenceRoot,
		ArtifactRoot: filepath.Join(evidenceRoot, "artifacts"), ReceiptPath: filepath.Join(evidenceRoot, "receipt"), ArtifactIndexPath: filepath.Join(evidenceRoot, "index"),
		Source: qualificationreceipt.SourceBinding{
			Commit: strings.Repeat("a", 40), TreeDigestSchema: qualificationreceipt.SourceContentTreeCommitmentSchemaV1, TreeDigest: digest('c'),
		},
		TemplateRelease: qualificationreceipt.TemplateReleaseBinding{ID: "173fdbba-3898-43ba-9201-5fbd574948f0", ContentHash: digest('d'), ApprovalReceiptDigest: digest('e')},
		GoldenRuntime: qualificationreceipt.GoldenRuntimeBinding{
			AuthorityDocumentArtifactID: "golden-authority-document", AuthorityDocumentDigest: digest('6'),
			FaultOperationSetDigest:   qualificationreceipt.GoldenFaultOperationSetDigestV1,
			FixtureDocumentArtifactID: "golden-fixture-document", FixtureDocumentDigest: digest('7'),
			FixtureID: "3a2c0dcf-f663-48fb-8fb3-88d310a3ff81",
		},
		BuildContractHash: digest('f'), WriterDrainEvidenceArtifactID: "v7-writer-drain-proof",
		CredentialSet: qualificationreceipt.CredentialSetAuthorityBinding{
			Issuer: "issuer", Audience: "audience", SetHandleHash: digest('8'), MemberBindingsDigest: digest('9'), MemberCount: 2,
		},
		ArtifactIndexDigest: digest('1'), ReceiptBundleDigest: digest('2'),
		TrustedReceiptIssuedAt: "2026-07-18T12:07:00.000Z", ArtifactSnapshotID: "snapshot-001", ArtifactSnapshotMode: qualificationreceipt.ImmutableSnapshotMode,
		RepositorySnapshotID: "repository-001", RepositorySnapshotMode: qualificationreceipt.ImmutableSnapshotMode,
		GitExecutableDigest: digest('4'),
	}
	plan := qualificationreceipt.Plan{
		Digest: authority.PlanDigest, ManifestDigest: authority.PrePromotionManifestDigest, Subject: authority.PromotionTarget.Subject, TestInventoryDigest: digest('3'),
		ExternalSuites: []qualificationreceipt.ExpectedSuite{{ID: "golden", RequirementIDs: []string{"AIC-E2E-001"}, RequiredArtifacts: []string{"browser-video"}}},
		TestCases:      []qualificationreceipt.ExpectedTestCase{{CaseID: "QG-GOLDEN-001", SuiteID: "golden", RequirementIDs: []string{"AIC-E2E-001"}, Mode: "qualification"}},
	}
	arguments := []string{
		"--repository-root", root, "--receipt", authority.ReceiptPath, "--artifact-index", authority.ArtifactIndexPath,
		"--artifact-root", authority.ArtifactRoot, "--promotion-authority", filepath.Join(root, "authority"),
	}
	planCalls := 0
	var capturedExpected qualificationreceipt.ExpectedPromotion
	dependencies := commandDependencies{
		now:                func() time.Time { return time.Date(2026, 7, 18, 12, 8, 0, 0, time.UTC) },
		loadAuthority:      func(string) (qualificationreceipt.PromotionAuthority, error) { return authority, nil },
		verifyRepository:   func(string, qualificationreceipt.SourceBinding, string) error { return nil },
		verifySourcePolicy: func(qualificationreceipt.PromotionAuthority) error { return nil },
		verifyExecutable:   func(qualificationreceipt.PromotionAuthority) error { return nil },
		computePlan: func(string, string) (qualificationreceipt.Plan, error) {
			planCalls++
			return plan, nil
		},
		loadTrustPolicy: func(qualificationreceipt.PromotionAuthority) (qualificationreceipt.TrustPolicy, error) {
			return qualificationreceipt.TrustPolicy{}, nil
		},
		newVerifier: func(qualificationreceipt.TrustPolicy) (promotionVerifier, error) {
			return fakePromotionVerifier{
				result:   qualificationreceipt.VerifiedPromotion{RunID: authority.RunID, Decision: "qualified"},
				expected: &capturedExpected,
			}, nil
		},
	}
	var output bytes.Buffer
	if err := runWithDependencies(arguments, &output, dependencies); err != nil {
		t.Fatalf("run bound promotion: %v", err)
	}
	if planCalls != 2 || !strings.Contains(output.String(), `"decision":"qualified"`) {
		t.Fatalf("run did not verify both plan reads and emit the promotion: calls=%d output=%s", planCalls, output.String())
	}
	if capturedExpected.AuthorityIssuedAt != authority.AuthorityIssuedAt || capturedExpected.GoldenRuntime != authority.GoldenRuntime ||
		capturedExpected.CredentialSet != authority.CredentialSet {
		t.Fatalf("CLI omitted Golden runtime or atomic credential-set authority: %+v", capturedExpected)
	}

	t.Run("mid-run plan drift", func(t *testing.T) {
		calls := 0
		drifted := dependencies
		drifted.computePlan = func(string, string) (qualificationreceipt.Plan, error) {
			calls++
			result := plan
			if calls == 2 {
				result.Digest = digest('9')
			}
			return result, nil
		}
		if err := runWithDependencies(arguments, &bytes.Buffer{}, drifted); err == nil || !strings.Contains(err.Error(), "changed") {
			t.Fatalf("mid-run plan drift was accepted: %v", err)
		}
	})
	t.Run("alternate repository root", func(t *testing.T) {
		alternate := append([]string(nil), arguments...)
		alternate[1] = t.TempDir()
		if err := runWithDependencies(alternate, &bytes.Buffer{}, dependencies); err == nil || !strings.Contains(err.Error(), "snapshot authority") {
			t.Fatalf("alternate repository root was accepted: %v", err)
		}
	})
	t.Run("repository source mismatch", func(t *testing.T) {
		mismatch := dependencies
		mismatch.verifyRepository = func(string, qualificationreceipt.SourceBinding, string) error { return errors.New("source mismatch") }
		if err := runWithDependencies(arguments, &bytes.Buffer{}, mismatch); err == nil || !strings.Contains(err.Error(), "source mismatch") {
			t.Fatalf("repository source mismatch was accepted: %v", err)
		}
	})
	t.Run("source policy attestation mismatch", func(t *testing.T) {
		mismatch := dependencies
		mismatch.verifySourcePolicy = func(qualificationreceipt.PromotionAuthority) error { return errors.New("source policy mismatch") }
		if err := runWithDependencies(arguments, &bytes.Buffer{}, mismatch); err == nil || !strings.Contains(err.Error(), "source policy mismatch") {
			t.Fatalf("source-policy mismatch was accepted: %v", err)
		}
	})
	t.Run("verifier executable mismatch", func(t *testing.T) {
		mismatch := dependencies
		mismatch.verifyExecutable = func(qualificationreceipt.PromotionAuthority) error { return errors.New("executable mismatch") }
		if err := runWithDependencies(arguments, &bytes.Buffer{}, mismatch); err == nil || !strings.Contains(err.Error(), "executable mismatch") {
			t.Fatalf("verifier executable mismatch was accepted: %v", err)
		}
	})
	t.Run("incomplete external suite", func(t *testing.T) {
		incomplete := dependencies
		incomplete.computePlan = func(string, string) (qualificationreceipt.Plan, error) {
			result := plan
			result.IncompleteExternalSuites = []string{"golden"}
			return result, nil
		}
		if err := runWithDependencies(arguments, &bytes.Buffer{}, incomplete); err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("incomplete external suite was accepted: %v", err)
		}
	})
}

func digest(character byte) string { return "sha256:" + strings.Repeat(string(character), 64) }

func TestParseOptionsRejectsRelativeAuthorityPathsManifestEscapesAndPositionals(t *testing.T) {
	if _, err := parseOptions([]string{"--repository-root", "relative"}); err == nil {
		t.Fatal("relative repository root was accepted")
	}
	root := t.TempDir()
	complete := []string{
		"--repository-root", root,
		"--receipt", filepath.Join(root, "receipt.dsse.json"),
		"--artifact-index", filepath.Join(root, "artifact-index.json"),
		"--artifact-root", filepath.Join(root, "artifacts"),
		"--promotion-authority", filepath.Join(root, "authority.json"),
		"--qualification-manifest", "../manifest.json",
	}
	if _, err := parseOptions(complete); err == nil {
		t.Fatal("qualification manifest path escape was accepted")
	}
	if _, err := parseOptions([]string{"unexpected"}); err == nil {
		t.Fatal("positional arguments were accepted")
	}
}
