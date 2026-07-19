package release

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/verification"
)

func passingCanonicalReceipt(t *testing.T) verification.CanonicalReceipt {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	attemptID := uuid.NewString()
	exitCode := 0
	checks := []verification.CheckResult{}
	for _, releaseCheck := range []struct{ id, kind string }{
		{id: "release-contract", kind: "contract"},
		{id: "release-artifacts", kind: "release-manifest"},
		{id: "release-sbom", kind: "sbom"},
		{id: "release-vulnerability", kind: "vulnerability"},
		{id: "release-container-policy", kind: "container-policy"},
	} {
		checks = append(checks, verification.CheckResult{
			ID: releaseCheck.id, Kind: releaseCheck.kind, Required: true, Status: verification.CheckPassed,
			AttemptID: attemptID, VerifierImageDigest: "registry.example/verifier@sha256:" + strings.Repeat("a", 64),
			Argv: []string{"verify", releaseCheck.kind}, WorkingDirectory: ".", ExitCode: &exitCode, StartedAt: now,
			CompletedAt: now.Add(time.Second), DurationMS: 1000, AttemptCount: 1,
			OracleIDs: []string{"oracle-release"}, AcceptanceCriterionIDs: []string{"AC-RELEASE"},
			ObligationIDs: []string{"OBL-RELEASE"}, Diagnostics: []verification.Diagnostic{},
		})
	}
	receipt, err := verification.NewCanonicalReceipt(verification.NewCanonicalReceiptInput{
		ID: uuid.NewString(), RunID: uuid.NewString(), ProjectID: uuid.NewString(),
		Subject: verification.CanonicalPlanSubject{
			WorkspaceArtifactID: uuid.NewString(), WorkspaceRevisionID: uuid.NewString(),
			WorkspaceContentHash: "sha256:" + strings.Repeat("b", 64),
		},
		BuildManifest:     repository.ExactReference{ID: uuid.NewString(), ContentHash: "sha256:" + strings.Repeat("c", 64)},
		BuildContract:     repository.ExactReference{ID: uuid.NewString(), ContentHash: "sha256:" + strings.Repeat("d", 64)},
		FullStackTemplate: repository.ExactReference{ID: uuid.NewString(), ContentHash: "sha256:" + strings.Repeat("e", 64)},
		Profile:           verification.ProfileReference{ID: "release-v1", Version: 1, ContentHash: "sha256:" + strings.Repeat("f", 64)},
		Plan:              verification.PlanReference{ID: uuid.NewString(), ContentHash: "sha256:" + strings.Repeat("1", 64)},
		AttemptIDs:        []string{attemptID}, Checks: checks,
		Obligations:      []verification.ObligationRequirement{{ID: "OBL-RELEASE", Level: "must", OracleIDs: []string{"oracle-release"}}},
		ReleaseArtifacts: completeReleaseArtifacts(),
		CreatedBy:        uuid.NewString(), CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Decision != verification.DecisionPassed {
		t.Fatalf("fixture is not passing: %+v", receipt)
	}
	return receipt
}

func completeReleaseArtifacts() []verification.CanonicalReleaseArtifact {
	kinds := []struct {
		id, kind, store, mediaType string
	}{
		{"api-image", "oci-image", "oci", "application/vnd.oci.image.manifest.v1+json"},
		{"health-contract", "health-readiness-contract", "content", "application/schema+json"},
		{"migration", "migration", "content", "application/vnd.worksflow.migration"},
		{"provenance", "provenance", "content", "application/vnd.in-toto+json"},
		{"runtime-config", "runtime-config-schema", "content", "application/schema+json"},
		{"sbom", "sbom", "content", "application/spdx+json"},
		{"signature", "signature", "content", "application/vnd.dev.cosign.simplesigning.v1+json"},
		{"vulnerability", "vulnerability-report", "content", "application/vnd.worksflow.vulnerability+json"},
	}
	artifacts := make([]verification.CanonicalReleaseArtifact, 0, len(kinds))
	for index, value := range kinds {
		digit := string("23456789"[index])
		digest := "sha256:" + strings.Repeat(digit, 64)
		ref := "content://" + value.id
		if value.store == "oci" {
			ref = "registry.example/api@" + digest
		}
		artifacts = append(artifacts, verification.CanonicalReleaseArtifact{
			ID: value.id, Kind: value.kind, Store: value.store, Ref: ref,
			ContentHash: digest, MediaType: value.mediaType, ByteSize: 1024,
		})
	}
	return artifacts
}

func TestBundleRequiresPassingCanonicalReceiptAndIsHashImmutable(t *testing.T) {
	receipt := passingCanonicalReceipt(t)
	bundle, err := NewBundle(NewBundleInput{
		ID: uuid.NewString(), Receipt: receipt, CreatedBy: uuid.NewString(), CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseBundle(bundle)
	if err != nil || parsed.BundleHash != bundle.BundleHash || parsed.CanonicalReceipt.ContentHash != receipt.PayloadHash {
		t.Fatalf("Bundle did not round trip: parsed=%+v err=%v", parsed, err)
	}
	storedHash, err := domain.CanonicalHash(bundle)
	if err != nil || "sha256:"+storedHash == bundle.BundleHash {
		t.Fatalf("semantic Bundle hash was conflated with full content hash: %s, %v", storedHash, err)
	}
	tampered := bundle
	tampered.ReleaseArtifacts = append([]verification.CanonicalReleaseArtifact(nil), bundle.ReleaseArtifacts...)
	tampered.ReleaseArtifacts[0].ByteSize++
	if _, err := ParseBundle(tampered); err == nil {
		t.Fatal("tampered Bundle was accepted")
	}
	receipt.Decision = verification.DecisionFailed
	if _, err := NewBundle(NewBundleInput{ID: uuid.NewString(), Receipt: receipt, CreatedBy: uuid.NewString(), CreatedAt: time.Now().UTC()}); err == nil {
		t.Fatal("non-passing Canonical Receipt was accepted")
	}
	incomplete := passingCanonicalReceipt(t)
	incomplete.ReleaseArtifacts = incomplete.ReleaseArtifacts[:1]
	if _, err := NewBundle(NewBundleInput{
		ID: uuid.NewString(), Receipt: incomplete, CreatedBy: uuid.NewString(), CreatedAt: time.Now().UTC(),
	}); err == nil {
		t.Fatal("incomplete release artifact set created a ReleaseBundle")
	}
}
