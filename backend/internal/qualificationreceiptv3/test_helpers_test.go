package qualificationreceiptv3

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/worksflow/builder/backend/internal/qualificationevidence"
	"github.com/worksflow/builder/backend/internal/templateauthority"
)

const (
	testAuthorityID      = "10000000-0000-4000-8000-000000000001"
	testFreezeOperation  = "10000000-0000-4000-8000-000000000002"
	testInputAuthorityID = "10000000-0000-4000-8000-000000000003"
	testProjectID        = "10000000-0000-4000-8000-000000000004"
	testWorkflowRunID    = "10000000-0000-4000-8000-000000000005"
	testTargetRevisionID = "10000000-0000-4000-8000-000000000006"
	testOrchestrationID  = "10000000-0000-4000-8000-000000000007"
	testRunID            = "10000000-0000-4000-8000-000000000008"
	testFixtureID        = "10000000-0000-4000-8000-000000000009"
	testCredentialSetID  = "10000000-0000-4000-8000-00000000000a"
	testTemplateID       = "10000000-0000-4000-8000-00000000000b"
)

type testSigningKey struct {
	identity string
	keyID    string
	private  ed25519.PrivateKey
	public   ed25519.PublicKey
}

type testEnvelope struct {
	PayloadType string          `json:"payloadType"`
	Payload     string          `json:"payload"`
	Signatures  []testSignature `json:"signatures"`
}

type testSignature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

type fakeExpectedResolver struct {
	mu              sync.Mutex
	calls           int
	err             error
	lastAuthorityID string
	lastReceiptID   string
	resolution      ExpectedResolution
}

func (resolver *fakeExpectedResolver) ResolveExpected(_ context.Context, authorityID, receiptID string) (ExpectedResolution, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.calls++
	resolver.lastAuthorityID = authorityID
	resolver.lastReceiptID = receiptID
	if resolver.err != nil {
		return ExpectedResolution{}, resolver.err
	}
	result := resolver.resolution
	result.Payload = append([]byte(nil), result.Payload...)
	return result, nil
}

func (resolver *fakeExpectedResolver) callCount() int {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return resolver.calls
}

func testKeys() (testSigningKey, testSigningKey) {
	runnerSeed := sha256.Sum256([]byte("qualification-receipt-v3-runner-fixed-seed"))
	approverSeed := sha256.Sum256([]byte("qualification-receipt-v3-approver-fixed-seed"))
	runnerPrivate := ed25519.NewKeyFromSeed(runnerSeed[:])
	approverPrivate := ed25519.NewKeyFromSeed(approverSeed[:])
	return testSigningKey{
			identity: "spiffe://worksflow.dev/qualification/runner",
			keyID:    "runner-key-v1",
			private:  runnerPrivate,
			public:   runnerPrivate.Public().(ed25519.PublicKey),
		}, testSigningKey{
			identity: "spiffe://worksflow.dev/release/approver",
			keyID:    "approver-key-v1",
			private:  approverPrivate,
			public:   approverPrivate.Public().(ed25519.PublicKey),
		}
}

func testPolicy() TrustPolicy {
	runner, approver := testKeys()
	policy := TrustPolicy{
		SchemaVersion: ReceiptTrustPolicySchemaV3,
		Runner:        SignerTrust{KeyID: runner.keyID, Identity: runner.identity, PublicKey: runner.public},
		Approver:      SignerTrust{KeyID: approver.keyID, Identity: approver.identity, PublicKey: approver.public},
	}
	digest, err := CanonicalTrustPolicyDigest(policy)
	if err != nil {
		panic(err)
	}
	policy.Digest = digest
	return policy
}

func testDigest(label string) string { return SHA256Digest([]byte(label)) }

func validReceipt(t *testing.T) Receipt {
	t.Helper()
	runner, approver := testKeys()
	template := TemplateReleaseBinding{
		ApprovalReceiptDigest: testDigest("template-approval"),
		ContentHash:           testDigest("template-content"),
		ID:                    testTemplateID,
	}
	templateDigest, err := CanonicalDigest(template)
	if err != nil {
		t.Fatal(err)
	}
	plan := qualificationevidence.Plan{
		SchemaVersion:               qualificationevidence.PlanSchemaV1,
		OrchestrationID:             testOrchestrationID,
		RunID:                       testRunID,
		FixtureID:                   testFixtureID,
		QualificationPlanArtifactID: PlanArtifactPrefix + testAuthorityID,
		PlanDigest:                  testDigest("manifest-plan-projection"),
		SourceTreeDigest:            testDigest("source-tree"),
		TemplateReleaseDigest:       templateDigest,
		Operations: qualificationevidence.OperationIDs{
			Reserve:              "10000000-0000-4000-8000-000000000010",
			CredentialIssue:      "10000000-0000-4000-8000-000000000011",
			RunClosure:           "10000000-0000-4000-8000-000000000012",
			KMSAttestation:       "10000000-0000-4000-8000-000000000013",
			CredentialRevocation: "10000000-0000-4000-8000-000000000014",
			ArtifactIndex:        "10000000-0000-4000-8000-000000000015",
			ReceiptSign:          "10000000-0000-4000-8000-000000000016",
			SnapshotSeal:         "10000000-0000-4000-8000-000000000017",
		},
		CredentialSet: qualificationevidence.CredentialExpectation{
			SetID:                testCredentialSetID,
			Issuer:               "credential-authority",
			Audience:             "worksflow-golden",
			SetHandleHash:        testDigest("credential-set-handle"),
			MemberBindingsDigest: testDigest("credential-member-bindings"),
			MemberCount:          2,
			IssuanceArtifactID:   "credential-set-issuance",
			RevocationArtifactID: "credential-set-revocation",
		},
		Artifacts: []qualificationevidence.ArtifactExpectation{
			{ID: "browser-video", Kind: qualificationevidence.ArtifactKindVideo, Classification: qualificationevidence.ClassificationRestricted, EncryptionOperationID: "10000000-0000-4000-8000-000000000018"},
			{ID: "credential-safe-trace", Kind: qualificationevidence.ArtifactKindTrace, Classification: qualificationevidence.ClassificationRestricted, EncryptionOperationID: "10000000-0000-4000-8000-000000000019"},
			{ID: "golden-authority-document", Kind: qualificationevidence.ArtifactKindGolden, Classification: qualificationevidence.ClassificationDistributable},
			{ID: "golden-fixture-document", Kind: qualificationevidence.ArtifactKindGolden, Classification: qualificationevidence.ClassificationDistributable},
			{ID: "run-result", Kind: qualificationevidence.ArtifactKindRunResult, Classification: qualificationevidence.ClassificationDistributable},
		},
		Recipient: qualificationevidence.EncryptionRecipient{
			KeyResourceID: "qualification-evidence-key",
			KeyVersion:    "version-1",
		},
		Outputs: qualificationevidence.OutputExpectation{
			KMSAttestationArtifactID: "kms-attestation",
			ArtifactIndexID:          "artifact-index",
			ReceiptID:                "qualification-receipt",
			SnapshotID:               "pre-receipt-snapshot",
		},
	}
	trustBindings := TrustBindings{
		CaptureAuthorityID:    "capture-authority",
		CredentialAuthorityID: "credential-authority",
		EncryptionAuthorityID: "encryption-authority",
		IndexerAuthorityID:    "indexer-authority",
		KMSAuthorityID:        "kms-authority",
		ReceiptAuthorityID:    "receipt-authority",
		SealerAuthorityID:     "sealer-authority",
		VerifierAuthorityID:   "verifier-authority",
	}
	trust := TrustBinding{
		SchemaVersion:     PlanTrustSchemaV1,
		TrustBindings:     trustBindings,
		TrustPolicyDigest: testPolicy().Digest,
	}
	target := TargetBinding{
		SchemaVersion: PlanTargetSchemaV1,
		PromotionTarget: PromotionTarget{
			NodeKey:   "external-qualification",
			ProjectID: testProjectID,
			StageGate: ExternalStageGate,
			Subject:   "ai-constructor",
			TargetRevision: PromotionTargetRevision{
				ContentHash: testDigest("target-revision"),
				ID:          testTargetRevisionID,
			},
			WorkflowRunID: testWorkflowRunID,
		},
	}
	artifactIDs, restrictedIDs := expectedEvidenceArtifactIDs(plan)
	artifactDescriptors := make([]ArtifactDescriptorBinding, 0, len(artifactIDs))
	for _, artifactID := range artifactIDs {
		contentDigest := testDigest("artifact-content:" + artifactID)
		switch artifactID {
		case "golden-authority-document":
			contentDigest = testDigest("golden-authority-document")
		case "golden-fixture-document":
			contentDigest = testDigest("golden-fixture-document")
		case plan.CredentialSet.IssuanceArtifactID:
			contentDigest = testDigest("credential-issuance-envelope")
		case plan.CredentialSet.RevocationArtifactID:
			contentDigest = testDigest("credential-revocation-envelope")
		case plan.Outputs.KMSAttestationArtifactID:
			contentDigest = testDigest("kms-attestation")
		}
		artifactDescriptors = append(artifactDescriptors, ArtifactDescriptorBinding{ID: artifactID, ContentDigest: contentDigest})
	}
	receipt := Receipt{
		ArtifactIndex: ArtifactIndexBinding{
			ArtifactCount:           len(artifactIDs),
			ArtifactIDs:             artifactIDs,
			Artifacts:               artifactDescriptors,
			ArtifactSetDigest:       testDigest("artifact-set"),
			AuthorityID:             trustBindings.IndexerAuthorityID,
			CommittedAt:             "2026-07-19T12:01:30.000Z",
			ContentDigest:           testDigest("artifact-index-content"),
			EvidenceClosureDigest:   testDigest("evidence-closure"),
			IndexID:                 plan.Outputs.ArtifactIndexID,
			OperationID:             plan.Operations.ArtifactIndex,
			RequestDigest:           testDigest("artifact-index-request"),
			RestrictedArtifactCount: len(restrictedIDs),
			RestrictedArtifactIDs:   restrictedIDs,
			SchemaVersion:           ArtifactIndexSchemaV3,
			Stage:                   AuthorityStageCommitted,
		},
		Build: BuildBinding{
			Contract: ImmutableContentBinding{ContentHash: testDigest("build-contract"), ID: "build-contract-v1"},
			Manifest: ImmutableContentBinding{ContentHash: testDigest("build-manifest"), ID: "build-manifest-v1"},
		},
		CompletedAt: "2026-07-19T12:04:00.000Z",
		CredentialSet: CredentialSetBinding{
			Audience:  plan.CredentialSet.Audience,
			ExpiresAt: "2026-07-19T12:20:00.000Z",
			Issuance: SignedArtifactBinding{
				ArtifactID: plan.CredentialSet.IssuanceArtifactID, ContentDigest: testDigest("credential-issuance-envelope"),
				PayloadDigest: testDigest("credential-issuance-payload"), SignerSetDigest: testDigest("credential-signer-set"),
			},
			IssuedAt:             "2026-07-19T11:50:00.000Z",
			Issuer:               plan.CredentialSet.Issuer,
			MemberBindingsDigest: plan.CredentialSet.MemberBindingsDigest,
			MemberCount:          plan.CredentialSet.MemberCount,
			Revocation: SignedArtifactBinding{
				ArtifactID: plan.CredentialSet.RevocationArtifactID, ContentDigest: testDigest("credential-revocation-envelope"),
				PayloadDigest: testDigest("credential-revocation-payload"), SignerSetDigest: testDigest("credential-signer-set"),
			},
			RevokedAt:     "2026-07-19T12:01:00.000Z",
			SetHandleHash: plan.CredentialSet.SetHandleHash,
			SetID:         plan.CredentialSet.SetID,
		},
		Decision: DecisionQualified,
		Evidence: EvidenceClosureBinding{
			ArtifactIDs:              append([]string(nil), artifactIDs...),
			ArtifactSetDigest:        testDigest("artifact-set"),
			CaptureDigest:            testDigest("capture"),
			ClosureDigest:            testDigest("evidence-closure"),
			EncryptionManifestDigest: testDigest("encryption-manifest"),
			KMSAttestationDigest:     testDigest("kms-attestation"),
			OrchestrationID:          plan.OrchestrationID,
			RestrictedArtifactIDs:    append([]string(nil), restrictedIDs...),
			ResultDigest:             testDigest("run-result"),
			RunID:                    plan.RunID,
			SchemaVersion:            EvidenceClosureSchemaV3,
		},
		EvidencePlan: plan,
		GoldenRuntime: GoldenRuntimeBinding{
			AuthorityDocumentArtifactID: "golden-authority-document",
			AuthorityDocumentDigest:     testDigest("golden-authority-document"),
			FaultOperationSetDigest:     GoldenFaultOperationSetDigestV1,
			FixtureDocumentArtifactID:   "golden-fixture-document",
			FixtureDocumentDigest:       testDigest("golden-fixture-document"),
			FixtureID:                   plan.FixtureID,
		},
		IssuedAt:    "2026-07-19T12:04:00.000Z",
		OperationID: plan.Operations.ReceiptSign,
		PlanAuthority: PlanAuthorityBinding{
			ArtifactID:        plan.QualificationPlanArtifactID,
			AuthorityID:       testAuthorityID,
			FreezeOperationID: testFreezeOperation,
			InputAuthorityID:  testInputAuthorityID,
			InputHash:         testDigest("resolved-plan-input"),
			PlanDigest:        plan.PlanDigest,
			ProjectionHash:    plan.PlanDigest,
		},
		QualificationManifest: ArtifactRevisionBinding{
			ArtifactID:  "qualification-manifest",
			ContentHash: testDigest("qualification-manifest-content"),
			RevisionID:  "10000000-0000-4000-8000-00000000000c",
		},
		QualificationStartedAt: "2026-07-19T12:00:00.000Z",
		ReceiptID:              plan.Outputs.ReceiptID,
		SchemaVersion:          ReceiptSchemaV3,
		Signers: ReceiptSignerBinding{
			Approver: SignerIdentityBinding{Identity: approver.identity, KeyID: approver.keyID, Role: SignerRoleApprover},
			Runner:   SignerIdentityBinding{Identity: runner.identity, KeyID: runner.keyID, Role: SignerRoleRunner},
		},
		Snapshot: PreReceiptSnapshotBinding{
			ArtifactIndexDigest:   testDigest("artifact-index-content"),
			AuthorityID:           trustBindings.SealerAuthorityID,
			EvidenceClosureDigest: testDigest("evidence-closure"),
			Mode:                  ImmutableSnapshotMode,
			OperationID:           plan.Operations.SnapshotSeal,
			RequestDigest:         testDigest("pre-receipt-snapshot-request"),
			SchemaVersion:         PreReceiptSnapshotSchemaV3,
			SealedAt:              "2026-07-19T12:02:00.000Z",
			SnapshotDigest:        testDigest("pre-receipt-snapshot-content"),
			SnapshotID:            plan.Outputs.SnapshotID,
			Stage:                 AuthorityStageCommitted,
		},
		SnapshotVerification: SnapshotVerificationBinding{
			ArtifactIndexDigest:   testDigest("artifact-index-content"),
			AuthorityID:           trustBindings.VerifierAuthorityID,
			EvidenceClosureDigest: testDigest("evidence-closure"),
			Result:                VerificationPassed,
			SchemaVersion:         SnapshotVerificationSchemaV3,
			SnapshotDigest:        testDigest("pre-receipt-snapshot-content"),
			SnapshotID:            plan.Outputs.SnapshotID,
			VerifiedAt:            "2026-07-19T12:03:00.000Z",
		},
		Source: SourceBinding{
			Commit: strings.Repeat("a", 40), Dirty: false, TreeDigest: plan.SourceTreeDigest,
			TreeDigestSchema: SourceTreeDigestSchemaV1,
		},
		Target:          target,
		TemplateRelease: template,
		Trust:           trust,
	}
	refreshAuthority(t, &receipt)
	if _, err := Compile(receipt); err != nil {
		t.Fatalf("valid fixture is invalid: %v", err)
	}
	return receipt
}

func refreshAuthority(t *testing.T, receipt *Receipt) {
	t.Helper()
	planBytes, err := qualificationevidence.CanonicalJSON(receipt.EvidencePlan)
	if err != nil {
		t.Fatal(err)
	}
	receipt.PlanAuthority.EvidencePlanHash = SHA256Digest(planBytes)
	targetHash, err := CanonicalDigest(receipt.Target)
	if err != nil {
		t.Fatal(err)
	}
	receipt.PlanAuthority.TargetHash = targetHash
	trustBindingsDigest, err := CanonicalDigest(receipt.Trust.TrustBindings)
	if err != nil {
		t.Fatal(err)
	}
	receipt.PlanAuthority.TrustBindingsDigest = trustBindingsDigest
	trustHash, err := CanonicalDigest(receipt.Trust)
	if err != nil {
		t.Fatal(err)
	}
	receipt.PlanAuthority.TrustHash = trustHash
	authority := receipt.PlanAuthority
	envelope := authorityEnvelope{
		ArtifactID: authority.ArtifactID, AuthorityID: authority.AuthorityID,
		EvidencePlanHash: authority.EvidencePlanHash, InputAuthorityID: authority.InputAuthorityID,
		InputHash: authority.InputHash, ManifestPlanDigest: authority.PlanDigest,
		OperationID: authority.FreezeOperationID, ProjectionHash: authority.ProjectionHash,
		SchemaVersion: PlanAuthoritySchemaV1, TargetHash: authority.TargetHash,
		TrustBindingsDigest: authority.TrustBindingsDigest, TrustHash: authority.TrustHash,
	}
	receipt.PlanAuthority.AuthorityHash, err = CanonicalDigest(envelope)
	if err != nil {
		t.Fatal(err)
	}
}

func resolverForReceipt(t *testing.T, receipt Receipt) *fakeExpectedResolver {
	t.Helper()
	compiled, err := Compile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeExpectedResolver{resolution: ExpectedResolution{
		AuthorityID:   receipt.PlanAuthority.AuthorityID,
		Payload:       append([]byte(nil), compiled.Payload...),
		PayloadDigest: compiled.PayloadDigest,
		ReceiptID:     receipt.ReceiptID,
	}}
}

func verifierForReceipt(t *testing.T, receipt Receipt) (*Verifier, *fakeExpectedResolver) {
	t.Helper()
	resolver := resolverForReceipt(t, receipt)
	verifier, err := NewVerifier(testPolicy(), resolver)
	if err != nil {
		t.Fatal(err)
	}
	return verifier, resolver
}

func cloneReceipt(t *testing.T, receipt Receipt) Receipt {
	t.Helper()
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var clone Receipt
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func signCompiled(t *testing.T, compiled CompiledPayload) []byte {
	t.Helper()
	runner, approver := testKeys()
	return signPayload(t, compiled.Payload, runner, approver)
}

func signPayload(t *testing.T, payload []byte, keys ...testSigningKey) []byte {
	t.Helper()
	pae := templateauthority.DSSEPAE(InTotoPayloadType, payload)
	signatures := make([]testSignature, 0, len(keys))
	for _, key := range keys {
		signatures = append(signatures, testSignature{
			KeyID: key.keyID,
			Sig:   base64.StdEncoding.EncodeToString(ed25519.Sign(key.private, pae)),
		})
	}
	sort.Slice(signatures, func(i, j int) bool { return signatures[i].KeyID < signatures[j].KeyID })
	encoded, err := json.Marshal(testEnvelope{
		PayloadType: InTotoPayloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  signatures,
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func payloadMap(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func canonicalMap(t *testing.T, value map[string]any) []byte {
	t.Helper()
	encoded, err := CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func nestedMap(t *testing.T, root map[string]any, keys ...string) map[string]any {
	t.Helper()
	current := root
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("%s is %T, want map", key, current[key])
		}
		current = next
	}
	return current
}
