package qualificationpolicyauthority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationevidence"
	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
)

var fixedDatabaseTime = time.Date(2026, 7, 19, 18, 30, 0, 123_000_000, time.UTC)

type fakePolicySource struct {
	mu     sync.Mutex
	values map[string]ResolvedPolicy
	err    error
	calls  int
	lastID string
}

func (source *fakePolicySource) Resolve(_ context.Context, sourceID string) (ResolvedPolicy, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.calls++
	source.lastID = sourceID
	if source.err != nil {
		return ResolvedPolicy{}, source.err
	}
	value, exists := source.values[sourceID]
	if !exists {
		return ResolvedPolicy{}, ErrNotFound
	}
	return cloneResolvedPolicy(value), nil
}

func (source *fakePolicySource) setError(err error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.err = err
}

func (source *fakePolicySource) callCount() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.calls
}

func validIssueCommand() IssueCommand {
	return IssueCommand{
		OperationID:    uuid.MustParse("10000000-0000-4000-8000-000000000001"),
		AuthorityID:    uuid.MustParse("10000000-0000-4000-8000-000000000002"),
		PolicySourceID: "reviewed-release-2026-07-19",
	}
}

func validResolvedPolicy() ResolvedPolicy {
	return ResolvedPolicy{
		ProjectID: uuid.MustParse("10000000-0000-4000-8000-000000000003"),
		ExecutionProfile: ExecutionProfileBinding{
			Version: ExecutionProfileV3,
			Hash:    testDigest("execution-profile"),
		},
		ExternalGatePolicy: ExternalGatePolicyV1,
		Status:             AuthorityStatusActive,
		SupersessionPolicy: SupersessionPolicyV1,
		RevisionPolicy: RevisionPolicy{
			SchemaVersion:        RevisionPolicySchemaV1,
			SourceCurrencyPolicy: CurrencyLatestApproved,
			WorkspaceTarget: WorkspaceTargetPolicy{
				CurrencyPolicy:          CurrencyLatestApproved,
				CanonicalReviewRequired: false,
			},
			ReviewByChangeSource: []ChangeSourceReviewRule{
				{ChangeSource: ChangeSourceAIProposal, CanonicalReviewRequired: true},
				{ChangeSource: ChangeSourceHuman, CanonicalReviewRequired: true},
				{ChangeSource: ChangeSourceImport, CanonicalReviewRequired: true},
				{ChangeSource: ChangeSourceMerge, CanonicalReviewRequired: true},
				{ChangeSource: ChangeSourceRollback, CanonicalReviewRequired: true},
				{ChangeSource: ChangeSourceSystem, CanonicalReviewRequired: false},
			},
			ExactApprovedSources: []ExactApprovedSource{
				{
					SourceKind:  "blueprint",
					Purpose:     "blueprint-source",
					ArtifactID:  "20000000-0000-4000-8000-000000000001",
					RevisionID:  "20000000-0000-4000-8000-000000000002",
					ContentHash: testDigest("blueprint-revision"),
				},
			},
		},
		PlanInputProfile: PlanInputProfile{
			SchemaVersion: PlanInputProfileSchemaV1,
			ArtifactPolicy: ArtifactPolicy{
				MaximumArtifacts:            qualificationevidence.MaximumArtifacts,
				RequireRestrictedEncryption: true,
				RequireTrace:                true,
				RequireVideo:                true,
			},
			Artifacts: []ArtifactExpectation{
				{
					ID:             "browser-video",
					Kind:           qualificationevidence.ArtifactKindVideo,
					Classification: qualificationevidence.ClassificationRestricted,
				},
				{
					ID:             "credential-safe-trace",
					Kind:           qualificationevidence.ArtifactKindTrace,
					Classification: qualificationevidence.ClassificationRestricted,
				},
				{
					ID:             "run-result",
					Kind:           qualificationevidence.ArtifactKindRunResult,
					Classification: qualificationevidence.ClassificationDistributable,
				},
			},
			CredentialProfile: CredentialProfile{
				Audience:               "urn:worksflow:qualification",
				AuthorityID:            "credential-authority",
				IssuanceArtifactID:     "credential-set-issuance",
				MemberRequestSetDigest: testDigest("credential-member-request-set"),
				RevocationArtifactID:   "credential-set-revocation",
			},
			GoldenRuntime: qualificationreceipt.GoldenRuntimeBinding{
				AuthorityDocumentArtifactID: "golden-authority-document",
				AuthorityDocumentDigest:     testDigest("golden-authority-document"),
				FaultOperationSetDigest:     qualificationreceipt.GoldenFaultOperationSetDigestV1,
				FixtureDocumentArtifactID:   "golden-fixture-document",
				FixtureDocumentDigest:       testDigest("golden-fixture-document"),
				FixtureID:                   "30000000-0000-4000-8000-000000000001",
			},
			OutputPolicy: OutputPolicy{
				CredentialRevocation: CredentialRevocationPolicyV1,
				PlaintextDisposition: PlaintextDispositionPolicyV1,
				SnapshotMode:         qualificationevidence.ImmutableSnapshotMode,
			},
			Outputs: qualificationevidence.OutputExpectation{
				KMSAttestationArtifactID: "kms-encryption-attestation",
				ArtifactIndexID:          "qualification-artifact-index",
				ReceiptID:                "qualification-receipt",
				SnapshotID:               "qualification-snapshot",
			},
			QualificationManifest: QualificationManifestBinding{
				ArtifactID:  "qualification-manifest",
				RevisionID:  "30000000-0000-4000-8000-000000000002",
				ContentHash: testDigest("qualification-manifest"),
				PlanDigest:  testDigest("qualification-plan"),
			},
			Recipient: qualificationevidence.EncryptionRecipient{
				KeyResourceID: "qualification-kms-key",
				KeyVersion:    "version-1",
			},
			SourcePolicyDigest: testDigest("source-policy"),
			TemplateRelease: qualificationreceipt.TemplateReleaseBinding{
				ID:                    "30000000-0000-4000-8000-000000000003",
				ContentHash:           testDigest("template-release"),
				ApprovalReceiptDigest: testDigest("template-release-approval"),
			},
			TrustBindings: qualificationevidence.TrustBindings{
				CaptureAuthorityID:    "capture-authority",
				CredentialAuthorityID: "credential-authority",
				EncryptionAuthorityID: "encryption-authority",
				IndexerAuthorityID:    "indexer-authority",
				KMSAuthorityID:        "kms-authority",
				ReceiptAuthorityID:    "receipt-authority",
				SealerAuthorityID:     "sealer-authority",
				VerifierAuthorityID:   "verifier-authority",
			},
			TrustPolicyDigest: testDigest("trust-policy"),
		},
		PromotionPolicy: PromotionPolicy{
			SchemaVersion:       PromotionPolicySchemaV1,
			PlanAuthoritySchema: QualificationPlanAuthoritySchemaV1,
			ReceiptSchema:       QualificationReceiptSchemaV3,
			SingleUseProtocol:   QualificationPromotionProtocolV2,
			IndependentRequirements: []IndependentAuthorityBinding{
				{
					Kind:          IndependentModelProfileActivation,
					AuthorityID:   "model-profile-activation-2026-07",
					AuthorityHash: testDigest("model-profile-activation"),
				},
				{
					Kind:          IndependentProductionPostgres,
					AuthorityID:   "production-postgresql-posture-2026-07",
					AuthorityHash: testDigest("production-postgresql-posture"),
				},
			},
		},
	}
}

func newTestService(t *testing.T) (*Service, *MemoryStore, *fakePolicySource) {
	t.Helper()
	resolved := validResolvedPolicy()
	source := &fakePolicySource{values: map[string]ResolvedPolicy{
		validIssueCommand().PolicySourceID: resolved,
	}}
	store := NewMemoryStore()
	clock := DatabaseClockFunc(func(context.Context) (time.Time, error) {
		return fixedDatabaseTime, nil
	})
	service, err := NewService(source, store, clock)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, source
}

func testDigest(label string) string {
	digest := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(digest[:])
}
