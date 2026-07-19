package qualificationreceipt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadPromotionAuthorityPinsRootOwnedExpectedBindings(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root ownership semantics require a uid-0 test process")
	}
	directory := rootOwnedTestDirectory(t)
	trustPath := filepath.Join(directory, "trust.json")
	trustBytes := mustJSON(t, validTrustPolicyDocument(t))
	if err := os.WriteFile(trustPath, trustBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	authorityPath := filepath.Join(directory, "authority.json")
	document := validPromotionAuthorityDocument(trustPath, testDigestFromBytes(trustBytes))
	if err := os.WriteFile(authorityPath, mustJSON(t, document), 0o600); err != nil {
		t.Fatal(err)
	}
	authority, err := LoadPromotionAuthority(authorityPath)
	if err != nil {
		t.Fatalf("load promotion authority: %v", err)
	}
	policy, err := LoadAuthorityTrustPolicy(authority)
	if err != nil {
		t.Fatalf("load authority trust policy: %v", err)
	}
	if policy.Digest != authority.TrustPolicyDigest || authority.RunID != document["runId"] {
		t.Fatalf("unexpected pinned authority: %+v", authority)
	}
	if !validDigest(authority.Digest) || ValidatePromotionAuthorityAt(authority, time.Date(2026, 7, 18, 12, 8, 0, 0, time.UTC)) != nil {
		t.Fatalf("authority digest or short-lived interval was not bound: %+v", authority)
	}
}

func TestLoadPromotionAuthorityRejectsMissingBindingAndWritableAuthority(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root ownership semantics require a uid-0 test process")
	}
	directory := rootOwnedTestDirectory(t)
	path := filepath.Join(directory, "authority.json")
	document := validPromotionAuthorityDocument(filepath.Join(directory, "trust.json"), testDigest("trust"))
	delete(document["source"].(map[string]any), "dirty")
	if err := os.WriteFile(path, mustJSON(t, document), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPromotionAuthority(path); err == nil {
		t.Fatal("promotion authority with an omitted zero-valued binding was accepted")
	}

	document = validPromotionAuthorityDocument(filepath.Join(directory, "trust.json"), testDigest("trust"))
	delete(document["source"].(map[string]any), "treeDigestSchema")
	if err := os.WriteFile(path, mustJSON(t, document), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPromotionAuthority(path); err == nil {
		t.Fatal("promotion authority using the legacy three-field source binding was accepted")
	}

	document = validPromotionAuthorityDocument(filepath.Join(directory, "trust.json"), testDigest("trust"))
	document["schemaVersion"] = "worksflow-qualification-promotion-authority/v1"
	if err := os.WriteFile(path, mustJSON(t, document), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPromotionAuthority(path); err == nil {
		t.Fatal("legacy v1 promotion authority was accepted by the current verifier")
	}

	document = validPromotionAuthorityDocument(filepath.Join(directory, "trust.json"), testDigest("trust"))
	if err := os.WriteFile(path, mustJSON(t, document), 0o622); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o622); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPromotionAuthority(path); err == nil {
		t.Fatal("group/other-writable promotion authority was accepted")
	}
}

func TestExecutableAndSourcePolicyAuthoritiesAreFailClosed(t *testing.T) {
	digest, err := runningExecutableDigest()
	if err != nil {
		t.Fatal(err)
	}
	authority := PromotionAuthority{VerifierExecutableDigest: digest}
	if err := VerifyCurrentExecutable(authority); err != nil {
		t.Fatalf("verify current executable: %v", err)
	}
	authority.VerifierExecutableDigest = testDigest("other-executable")
	if err := VerifyCurrentExecutable(authority); err == nil {
		t.Fatal("untrusted verifier executable digest was accepted")
	}

	root := t.TempDir()
	toolPath := filepath.Join(root, "frontend", "scripts", "qualification-source-policy.mjs")
	if err := os.MkdirAll(filepath.Dir(toolPath), 0o700); err != nil {
		t.Fatal(err)
	}
	tool := []byte("export const sourcePolicy = 'reviewed'\n")
	if err := os.WriteFile(toolPath, tool, 0o600); err != nil {
		t.Fatal(err)
	}
	authority = PromotionAuthority{
		RepositoryRoot: root, SourcePolicyStatus: "passed", SourcePolicyToolDigest: testDigestFromBytes(tool),
		SourcePolicyVerifiedAt: "2026-07-18T12:00:00.000Z", TrustedReceiptIssuedAt: "2026-07-18T12:01:00.000Z",
		PlanDigest: testDigest("plan"), Source: SourceBinding{
			Commit: strings.Repeat("a", 40), TreeDigestSchema: SourceContentTreeCommitmentSchemaV1, TreeDigest: testDigest("tree"),
		},
	}
	statement := map[string]any{
		"schemaVersion": "worksflow-qualification-source-policy-attestation/v1", "status": authority.SourcePolicyStatus,
		"planDigest": authority.PlanDigest, "source": authority.Source, "toolDigest": authority.SourcePolicyToolDigest,
		"verifiedAt": authority.SourcePolicyVerifiedAt,
	}
	canonical, _ := canonicalJSONBytes(statement)
	authority.SourcePolicyAttestationDigest = testDigestFromBytes(canonical)
	if err := VerifySourcePolicyAuthority(authority); err != nil {
		t.Fatalf("verify source-policy authority: %v", err)
	}
	authority.SourcePolicyToolDigest = testDigest("tampered-tool")
	if err := VerifySourcePolicyAuthority(authority); err == nil {
		t.Fatal("tampered source-policy tool authority was accepted")
	}
}

func TestPromotionAuthorityTrustedTimeRejectsExpiryAndLongTTL(t *testing.T) {
	authority := PromotionAuthority{
		AuthorityIssuedAt:  "2026-07-18T12:00:00.000Z",
		AuthorityExpiresAt: "2026-07-18T12:10:00.000Z",
	}
	if err := ValidatePromotionAuthorityAt(authority, time.Date(2026, 7, 18, 12, 10, 0, 0, time.UTC)); err == nil {
		t.Fatal("authority was accepted at its exclusive expiry boundary")
	}
	authority.AuthorityExpiresAt = "2026-07-18T12:15:00.001Z"
	if err := ValidatePromotionAuthorityAt(authority, time.Date(2026, 7, 18, 12, 1, 0, 0, time.UTC)); err == nil {
		t.Fatal("authority with a validity interval over 15 minutes was accepted")
	}
}

func rootOwnedTestDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/", ".qualification-authority-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func validPromotionAuthorityDocument(trustPath, trustDigest string) map[string]any {
	evidenceRoot := filepath.Join(filepath.Dir(trustPath), "sealed-evidence")
	artifactRoot := filepath.Join(evidenceRoot, "artifacts")
	return map[string]any{
		"schemaVersion": PromotionAuthoritySchemaV2,
		"promotionTarget": map[string]any{
			"projectId": "4a4554f6-f270-4ce7-9323-78510af0f91a", "workflowRunId": "d0118815-af3f-491f-9e26-bf43684a5263",
			"nodeKey": "external-qualification", "targetRevision": map[string]any{
				"id": "f87f0f42-3a01-4c60-bcb9-781da473b945", "contentHash": testDigest("target-revision"),
			}, "subject": "ai-constructor", "stageGate": ExternalQualificationGate,
		},
		"authorityNonce":             "88172ca8-8507-4a7a-bf86-f89f58ac859d",
		"authorityIssuedAt":          "2026-07-18T12:00:00.000Z",
		"authorityExpiresAt":         "2026-07-18T12:10:00.000Z",
		"runId":                      "d5d9a265-d09d-40e7-b643-f2a48698dc9b",
		"planDigest":                 testDigest("plan"),
		"prePromotionManifestDigest": testDigest("manifest"),
		"source": map[string]any{
			"commit": strings.Repeat("a", 40), "treeDigestSchema": SourceContentTreeCommitmentSchemaV1,
			"treeDigest": testDigest("tree"), "dirty": false,
		},
		"templateRelease": map[string]any{
			"id": "173fdbba-3898-43ba-9201-5fbd574948f0", "contentHash": testDigest("template"),
			"approvalReceiptDigest": testDigest("approval"),
		},
		"goldenRuntime": map[string]any{
			"authorityDocumentArtifactId": "golden-authority-document", "authorityDocumentDigest": testDigest("golden-authority-document"),
			"faultOperationSetDigest":   GoldenFaultOperationSetDigestV1,
			"fixtureDocumentArtifactId": "golden-fixture-document", "fixtureDocumentDigest": testDigest("golden-fixture-document"),
			"fixtureId": "3a2c0dcf-f663-48fb-8fb3-88d310a3ff81",
		},
		"buildContractHash":             testDigest("contract"),
		"writerDrainEvidenceArtifactId": "v7-writer-drain-proof",
		"credentialSet": map[string]any{
			"issuer": "golden-issuer", "audience": "worksflow-golden", "setHandleHash": testDigest("credential-set"),
			"memberBindingsDigest": testDigest("credential-members"), "memberCount": 2,
		},
		"trustPolicyPath":               trustPath,
		"trustPolicyDigest":             trustDigest,
		"artifactRoot":                  artifactRoot,
		"evidenceSnapshotRoot":          evidenceRoot,
		"receiptPath":                   filepath.Join(evidenceRoot, "qualification.dsse.json"),
		"artifactIndexPath":             filepath.Join(evidenceRoot, "artifact-index.json"),
		"artifactIndexDigest":           testDigest("artifact-index"),
		"receiptBundleDigest":           testDigest("receipt-bundle"),
		"trustedReceiptIssuedAt":        "2026-07-18T12:07:00.000Z",
		"artifactSnapshotId":            "snapshot-001",
		"artifactSnapshotMode":          ImmutableSnapshotMode,
		"repositoryRoot":                filepath.Join(filepath.Dir(trustPath), "repository-snapshot"),
		"qualificationManifestPath":     "qualification/manifest.json",
		"repositorySnapshotId":          "repository-snapshot-001",
		"repositorySnapshotMode":        ImmutableSnapshotMode,
		"sourcePolicyStatus":            "passed",
		"sourcePolicyToolDigest":        testDigest("source-policy-tool"),
		"sourcePolicyVerifiedAt":        "2026-07-18T12:00:00.000Z",
		"sourcePolicyAttestationDigest": testDigest("source-policy-attestation"),
		"verifierExecutableDigest":      testDigest("verifier-executable"),
		"gitExecutableDigest":           testDigest("git-executable"),
	}
}
