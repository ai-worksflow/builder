package qualificationevidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeLayout = "2006-01-02T15:04:05.000Z"

type fixedClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (clock *fixedClock) Now() time.Time {
	clock.mu.RLock()
	defer clock.mu.RUnlock()
	return clock.now
}

func (clock *fixedClock) Set(value time.Time) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = value
}

func testUUID(index int) string {
	return fmt.Sprintf("10000000-0000-4000-8000-%012d", index)
}

func testDigest(label string) string { return sha256Digest([]byte(label)) }

func testTime(hour, minute, second int) string {
	return time.Date(2026, 7, 19, hour, minute, second, 0, time.UTC).Format(testTimeLayout)
}

func testTrust() TrustBindings {
	return TrustBindings{
		CaptureAuthorityID:    "spiffe://qualification.example/capture-authority",
		CredentialAuthorityID: "spiffe://qualification.example/credential-authority",
		EncryptionAuthorityID: "spiffe://qualification.example/encryption-authority",
		IndexerAuthorityID:    "spiffe://qualification.example/index-authority",
		KMSAuthorityID:        "spiffe://qualification.example/kms-authority",
		ReceiptAuthorityID:    "spiffe://qualification.example/receipt-authority",
		SealerAuthorityID:     "spiffe://qualification.example/seal-authority",
		VerifierAuthorityID:   "spiffe://qualification.example/verify-authority",
	}
}

func testPlan(t *testing.T) Plan {
	t.Helper()
	members := []CredentialMember{
		{Slot: "browser-a", ActorID: testUUID(90), Kind: "storage-state", CredentialHandleHash: testDigest("browser-a-handle")},
		{Slot: "platform-api-a", ActorID: testUUID(91), Kind: "token", CredentialHandleHash: testDigest("api-a-handle")},
	}
	memberDigest, err := credentialMemberDigest(members)
	if err != nil {
		t.Fatal(err)
	}
	return Plan{
		SchemaVersion: PlanSchemaV1, OrchestrationID: testUUID(1), RunID: testUUID(2), FixtureID: testUUID(3),
		QualificationPlanArtifactID: "qualification-plan-" + testUUID(80), PlanDigest: testDigest("qualification-plan"),
		SourceTreeDigest: testDigest("source-tree"), TemplateReleaseDigest: testDigest("template-release"),
		Operations: OperationIDs{
			Reserve: testUUID(10), CredentialIssue: testUUID(11), RunClosure: testUUID(12),
			KMSAttestation: testUUID(13), CredentialRevocation: testUUID(14), ArtifactIndex: testUUID(15),
			ReceiptSign: testUUID(16), SnapshotSeal: testUUID(17),
		},
		CredentialSet: CredentialExpectation{
			SetID: testUUID(4), Issuer: testTrust().CredentialAuthorityID, Audience: "urn:worksflow:golden-stack",
			SetHandleHash: testDigest("set-handle"), MemberBindingsDigest: memberDigest, MemberCount: len(members),
			IssuanceArtifactID: "credential-set-issuance", RevocationArtifactID: "credential-set-revocation",
		},
		Artifacts: []ArtifactExpectation{
			{ID: "browser-video", Kind: ArtifactKindVideo, Classification: ClassificationRestricted, EncryptionOperationID: testUUID(20)},
			{ID: "credential-safe-trace", Kind: ArtifactKindTrace, Classification: ClassificationRestricted, EncryptionOperationID: testUUID(21)},
			{ID: "golden-authority", Kind: ArtifactKindGolden, Classification: ClassificationDistributable},
			{ID: "playwright-results", Kind: ArtifactKindRunResult, Classification: ClassificationDistributable},
		},
		Recipient: EncryptionRecipient{KeyResourceID: "qualification-kms-key", KeyVersion: "version-one"},
		Outputs: OutputExpectation{
			KMSAttestationArtifactID: "kms-encryption-attestation", ArtifactIndexID: "qualification-artifact-index",
			ReceiptID: "qualification-receipt", SnapshotID: "qualification-evidence-snapshot",
		},
	}
}

type fakePlanAuthority struct {
	mu         sync.Mutex
	resolution PlanAuthorityResolution
	err        error
	calls      int
	requested  []string
}

func newFakePlanAuthority(t *testing.T, plan Plan, trust TrustBindings) *fakePlanAuthority {
	t.Helper()
	planBytes, err := CanonicalJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	trustDigest, err := CanonicalDigest(trust)
	if err != nil {
		t.Fatal(err)
	}
	return &fakePlanAuthority{resolution: PlanAuthorityResolution{
		AuthorityID: testUUID(80), AuthorityHash: testDigest("qualification-plan-authority"),
		ArtifactID: plan.QualificationPlanArtifactID, EvidencePlanHash: sha256Digest(planBytes),
		EvidencePlanBytes: planBytes, TrustBindingsDigest: trustDigest, Plan: clonePlan(plan),
	}}
}

func (authority *fakePlanAuthority) Resolve(ctx context.Context, authorityID string) (PlanAuthorityResolution, error) {
	if err := ctx.Err(); err != nil {
		return PlanAuthorityResolution{}, err
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.calls++
	authority.requested = append(authority.requested, authorityID)
	if authority.err != nil {
		return PlanAuthorityResolution{}, authority.err
	}
	resolution := authority.resolution
	resolution.EvidencePlanBytes = append([]byte(nil), resolution.EvidencePlanBytes...)
	resolution.Plan = clonePlan(resolution.Plan)
	return resolution, nil
}

func (authority *fakePlanAuthority) mutate(mutator func(*PlanAuthorityResolution)) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	mutator(&authority.resolution)
}

func (authority *fakePlanAuthority) callCount() int {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return authority.calls
}

type authorityCalls struct {
	issue, inspectIssue     int
	capture, inspectCapture int
	encrypt, inspectEncrypt int
	kms, inspectKMS         int
	revoke, inspectRevoke   int
	index, inspectIndex     int
	receipt, inspectReceipt int
	seal, inspectSeal       int
	verify                  int
}

type fakeAuthorities struct {
	mu    sync.Mutex
	plan  Plan
	trust TrustBindings

	calls         authorityCalls
	unknownKind   string
	unknownUsed   bool
	verifyUnknown bool
	preMutation   func(string, string) error

	issueObservation   *CredentialIssueObservation
	captureObservation *RunClosureObservation
	encryptions        map[string]EncryptionCommitment
	kmsObservation     *KMSAttestationObservation
	revokeObservation  *CredentialRevocationObservation
	indexObservation   *ArtifactIndexCommitment
	receiptObservation *QualificationReceiptCommitment
	sealObservation    *SnapshotCommitment

	mutateIssue      func(*CredentialIssueObservation)
	mutateCapture    func(*RunClosureObservation)
	mutateEncryption func(*EncryptionCommitment)
	mutateKMS        func(*KMSAttestationObservation)
	mutateRevoke     func(*CredentialRevocationObservation)
	mutateIndex      func(*ArtifactIndexCommitment)
	mutateReceipt    func(*QualificationReceiptCommitment)
	mutateSeal       func(*SnapshotCommitment)
	mutateVerify     func(*SnapshotVerification)
}

func newFakeAuthorities(plan Plan, trust TrustBindings) *fakeAuthorities {
	return &fakeAuthorities{plan: clonePlan(plan), trust: trust, encryptions: make(map[string]EncryptionCommitment)}
}

func (fake *fakeAuthorities) mutation(kind, operationID string) error {
	if fake.preMutation != nil {
		if err := fake.preMutation(kind, operationID); err != nil {
			return err
		}
	}
	return nil
}

func (fake *fakeAuthorities) lose(kind string) bool {
	if fake.unknownKind == kind && !fake.unknownUsed {
		fake.unknownUsed = true
		return true
	}
	return false
}

func (fake *fakeAuthorities) IssueAtomic(_ context.Context, request CredentialIssueRequest) (CredentialIssueObservation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.issue++
	if err := fake.mutation("issue", request.OperationID); err != nil {
		return CredentialIssueObservation{}, err
	}
	members := []CredentialMember{
		{Slot: "browser-a", ActorID: testUUID(90), Kind: "storage-state", CredentialHandleHash: testDigest("browser-a-handle")},
		{Slot: "platform-api-a", ActorID: testUUID(91), Kind: "token", CredentialHandleHash: testDigest("api-a-handle")},
	}
	observation := CredentialIssueObservation{
		OperationID: request.OperationID, RequestDigest: mustRequestDigest(request), Stage: AuthorityCommitted,
		Binding: CredentialSetBinding{
			SetID: request.Expected.SetID, RunID: request.RunID, FixtureID: request.FixtureID,
			Issuer: request.Expected.Issuer, Audience: request.Expected.Audience,
			SetHandleHash: request.Expected.SetHandleHash, MemberBindingsDigest: request.Expected.MemberBindingsDigest,
			MemberCount: len(members), Members: members, IssuedAt: testTime(12, 0, 0), ExpiresAt: testTime(12, 20, 0),
		},
		Attestation: signedArtifact(request.Expected.IssuanceArtifactID, request.Expected.Issuer, testTime(12, 0, 0), "issuance"),
	}
	if fake.mutateIssue != nil {
		fake.mutateIssue(&observation)
	}
	fake.issueObservation = pointerCredentialIssue(observation)
	if fake.lose("issue") {
		return CredentialIssueObservation{}, errors.New("bearer token /tmp/credential.json")
	}
	return cloneCredentialIssue(observation), nil
}

func (fake *fakeAuthorities) InspectIssue(_ context.Context, ref OperationRef) (CredentialIssueObservation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.inspectIssue++
	if fake.issueObservation == nil {
		return CredentialIssueObservation{OperationID: ref.OperationID, Stage: AuthorityPending}, nil
	}
	return cloneCredentialIssue(*fake.issueObservation), nil
}

func (fake *fakeAuthorities) CloseRun(_ context.Context, request RunClosureRequest) (RunClosureObservation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.capture++
	if err := fake.mutation("capture", request.OperationID); err != nil {
		return RunClosureObservation{}, err
	}
	artifacts := make([]CapturedArtifact, len(fake.plan.Artifacts))
	for index, expected := range fake.plan.Artifacts {
		artifacts[index] = CapturedArtifact{
			ID: expected.ID, Kind: expected.Kind, Classification: expected.Classification,
			CaptureRef: "capture-" + expected.ID, ContentDigest: testDigest("plaintext/" + expected.ID), SizeBytes: int64(100 + index),
		}
	}
	observation := RunClosureObservation{
		OperationID: request.OperationID, RequestDigest: mustRequestDigest(request),
		AuthorityID: fake.trust.CaptureAuthorityID, Stage: AuthorityCommitted,
		ResultDigest: testDigest("normalized-run-result"), CompletedAt: testTime(12, 1, 0), Artifacts: artifacts,
	}
	if fake.mutateCapture != nil {
		fake.mutateCapture(&observation)
	}
	observation.CaptureDigest, _ = capturedArtifactDigest(observation.Artifacts)
	fake.captureObservation = pointerRunClosure(observation)
	if fake.lose("capture") {
		return RunClosureObservation{}, errors.New("cookie Authorization secret")
	}
	return cloneRunClosure(observation), nil
}

func (fake *fakeAuthorities) InspectRunClosure(_ context.Context, ref OperationRef) (RunClosureObservation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.inspectCapture++
	if fake.captureObservation == nil {
		return RunClosureObservation{OperationID: ref.OperationID, Stage: AuthorityPending}, nil
	}
	return cloneRunClosure(*fake.captureObservation), nil
}

func (fake *fakeAuthorities) Encrypt(_ context.Context, request EncryptionRequest) (EncryptionCommitment, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.encrypt++
	if err := fake.mutation("encrypt", request.OperationID); err != nil {
		return EncryptionCommitment{}, err
	}
	encryptedAt := testTime(12, 2, 0)
	disposition, disposedAt := PlaintextNeverPersisted, encryptedAt
	if request.Artifact.ID == "credential-safe-trace" {
		encryptedAt = testTime(12, 3, 0)
		disposition, disposedAt = PlaintextDeleted, testTime(12, 3, 1)
	}
	observation := EncryptionCommitment{
		OperationID: request.OperationID, RequestDigest: mustRequestDigest(request),
		AuthorityID: fake.trust.EncryptionAuthorityID, Stage: AuthorityCommitted,
		ArtifactID: request.Artifact.ID, PlaintextDigest: request.Artifact.ContentDigest,
		CiphertextRef: "ciphertext-" + request.Artifact.ID, CiphertextDigest: testDigest("ciphertext/" + request.Artifact.ID),
		SizeBytes: request.Artifact.SizeBytes + 32, EncryptionDescriptorDigest: testDigest("descriptor/" + request.Artifact.ID),
		WrappedKeyDigest: testDigest("wrapped-key/" + request.Artifact.ID), AdditionalDataHash: request.AdditionalDataHash,
		Recipient: request.Recipient, EncryptedAt: encryptedAt,
		PlaintextDisposition: disposition, PlaintextDispositionAt: disposedAt,
	}
	if fake.mutateEncryption != nil {
		fake.mutateEncryption(&observation)
	}
	fake.encryptions[request.OperationID] = observation
	if fake.lose("encrypt") {
		return EncryptionCommitment{}, errors.New("raw token in encryptor error")
	}
	return observation, nil
}

func (fake *fakeAuthorities) InspectEncryption(_ context.Context, ref OperationRef) (EncryptionCommitment, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.inspectEncrypt++
	value, exists := fake.encryptions[ref.OperationID]
	if !exists {
		return EncryptionCommitment{OperationID: ref.OperationID, Stage: AuthorityPending}, nil
	}
	return value, nil
}

func (fake *fakeAuthorities) Attest(_ context.Context, request KMSAttestationRequest) (KMSAttestationObservation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.kms++
	if err := fake.mutation("kms", request.OperationID); err != nil {
		return KMSAttestationObservation{}, err
	}
	observation := KMSAttestationObservation{
		OperationID: request.OperationID, RequestDigest: mustRequestDigest(request),
		AuthorityID: fake.trust.KMSAuthorityID, Stage: AuthorityCommitted,
		ManifestDigest: request.ManifestDigest, ArtifactSetDigest: request.ArtifactSetDigest,
		Attestation: signedArtifact(request.ExpectedArtifactID, fake.trust.KMSAuthorityID, testTime(12, 4, 0), "kms"),
	}
	observation.Attestation.PayloadDigest = request.ExpectedPayloadDigest
	if fake.mutateKMS != nil {
		fake.mutateKMS(&observation)
	}
	fake.kmsObservation = pointerKMS(observation)
	if fake.lose("kms") {
		return KMSAttestationObservation{}, errors.New("KMS secret diagnostic")
	}
	return observation, nil
}

func (fake *fakeAuthorities) InspectAttestation(_ context.Context, ref OperationRef) (KMSAttestationObservation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.inspectKMS++
	if fake.kmsObservation == nil {
		return KMSAttestationObservation{OperationID: ref.OperationID, Stage: AuthorityPending}, nil
	}
	return *fake.kmsObservation, nil
}

func (fake *fakeAuthorities) RevokeExact(_ context.Context, request CredentialRevocationRequest) (CredentialRevocationObservation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.revoke++
	if err := fake.mutation("revoke", request.OperationID); err != nil {
		return CredentialRevocationObservation{}, err
	}
	observation := CredentialRevocationObservation{
		OperationID: request.OperationID, RequestDigest: mustRequestDigest(request),
		KMSAttestationDigest: request.KMSAttestationDigest, Stage: AuthorityCommitted,
		Binding:     cloneCredentialBinding(request.Binding),
		RevokedAt:   testTime(12, 5, 0),
		Attestation: signedArtifact(fake.plan.CredentialSet.RevocationArtifactID, fake.trust.CredentialAuthorityID, testTime(12, 5, 0), "revocation"),
	}
	if fake.mutateRevoke != nil {
		fake.mutateRevoke(&observation)
	}
	fake.revokeObservation = pointerRevocation(observation)
	if fake.lose("revoke") {
		return CredentialRevocationObservation{}, errors.New("credential cookie response")
	}
	return cloneCredentialRevocation(observation), nil
}

func (fake *fakeAuthorities) InspectRevocation(_ context.Context, ref OperationRef) (CredentialRevocationObservation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.inspectRevoke++
	if fake.revokeObservation == nil {
		return CredentialRevocationObservation{OperationID: ref.OperationID, Stage: AuthorityPending}, nil
	}
	return cloneCredentialRevocation(*fake.revokeObservation), nil
}

func (fake *fakeAuthorities) BuildIndex(_ context.Context, request ArtifactIndexRequest) (ArtifactIndexCommitment, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.index++
	if err := fake.mutation("index", request.OperationID); err != nil {
		return ArtifactIndexCommitment{}, err
	}
	observation := ArtifactIndexCommitment{
		OperationID: request.OperationID, RequestDigest: mustRequestDigest(request),
		AuthorityID: fake.trust.IndexerAuthorityID, Stage: AuthorityCommitted,
		IndexID: request.ExpectedIndexID, ContentDigest: testDigest("artifact-index"),
		EvidenceClosureDigest: request.EvidenceClosureDigest, ArtifactSetDigest: request.ArtifactSetDigest,
		ArtifactCount: request.ArtifactCount, RestrictedArtifactCount: request.RestrictedArtifactCount,
	}
	if fake.mutateIndex != nil {
		fake.mutateIndex(&observation)
	}
	fake.indexObservation = pointerIndex(observation)
	if fake.lose("index") {
		return ArtifactIndexCommitment{}, errors.New("index Authorization response")
	}
	return observation, nil
}

func (fake *fakeAuthorities) InspectIndex(_ context.Context, ref OperationRef) (ArtifactIndexCommitment, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.inspectIndex++
	if fake.indexObservation == nil {
		return ArtifactIndexCommitment{OperationID: ref.OperationID, Stage: AuthorityPending}, nil
	}
	return *fake.indexObservation, nil
}

func (fake *fakeAuthorities) SignReceipt(_ context.Context, request ReceiptSignRequest) (QualificationReceiptCommitment, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.receipt++
	if err := fake.mutation("receipt", request.OperationID); err != nil {
		return QualificationReceiptCommitment{}, err
	}
	observation := QualificationReceiptCommitment{
		OperationID: request.OperationID, RequestDigest: mustRequestDigest(request),
		AuthorityID: fake.trust.ReceiptAuthorityID, Stage: AuthorityCommitted,
		ReceiptID: request.ExpectedReceiptID, ContentDigest: testDigest("receipt-envelope"), PayloadDigest: request.ExpectedPayloadDigest,
		SubjectIndexDigest: request.Index.ContentDigest, EvidenceClosureDigest: request.EvidenceClosureDigest,
		SignerSetDigest: testDigest("runner-and-approver"), SignerCount: 2, IssuedAt: testTime(12, 6, 0),
	}
	if fake.mutateReceipt != nil {
		fake.mutateReceipt(&observation)
	}
	fake.receiptObservation = pointerReceipt(observation)
	if fake.lose("receipt") {
		return QualificationReceiptCommitment{}, errors.New("signer private key diagnostic")
	}
	return observation, nil
}

func (fake *fakeAuthorities) InspectReceipt(_ context.Context, ref OperationRef) (QualificationReceiptCommitment, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.inspectReceipt++
	if fake.receiptObservation == nil {
		return QualificationReceiptCommitment{OperationID: ref.OperationID, Stage: AuthorityPending}, nil
	}
	return *fake.receiptObservation, nil
}

func (fake *fakeAuthorities) Seal(_ context.Context, request SnapshotSealRequest) (SnapshotCommitment, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.seal++
	if err := fake.mutation("seal", request.OperationID); err != nil {
		return SnapshotCommitment{}, err
	}
	observation := SnapshotCommitment{
		OperationID: request.OperationID, RequestDigest: mustRequestDigest(request),
		AuthorityID: fake.trust.SealerAuthorityID, Stage: AuthorityCommitted,
		SnapshotID: request.ExpectedSnapshotID, SnapshotDigest: testDigest("immutable-snapshot"),
		EvidenceClosureDigest: request.EvidenceClosureDigest, IndexDigest: request.Index.ContentDigest,
		ReceiptDigest: request.Receipt.ContentDigest, Mode: request.Mode, SealedAt: testTime(12, 7, 0),
	}
	if fake.mutateSeal != nil {
		fake.mutateSeal(&observation)
	}
	fake.sealObservation = pointerSnapshot(observation)
	if fake.lose("seal") {
		return SnapshotCommitment{}, errors.New("snapshot path /secret/evidence")
	}
	return observation, nil
}

func (fake *fakeAuthorities) InspectSeal(_ context.Context, ref OperationRef) (SnapshotCommitment, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.inspectSeal++
	if fake.sealObservation == nil {
		return SnapshotCommitment{OperationID: ref.OperationID, Stage: AuthorityPending}, nil
	}
	return *fake.sealObservation, nil
}

func (fake *fakeAuthorities) Verify(_ context.Context, request SnapshotVerificationRequest) (SnapshotVerification, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls.verify++
	if fake.verifyUnknown {
		fake.verifyUnknown = false
		return SnapshotVerification{}, errors.New("read-only verifier temporarily unavailable")
	}
	observation := SnapshotVerification{
		AuthorityID: fake.trust.VerifierAuthorityID, SnapshotID: request.Snapshot.SnapshotID,
		SnapshotDigest: request.Snapshot.SnapshotDigest, EvidenceClosureDigest: request.EvidenceClosureDigest,
		IndexDigest: request.Snapshot.IndexDigest, ReceiptDigest: request.Snapshot.ReceiptDigest, VerifiedAt: testTime(12, 8, 0),
	}
	if fake.mutateVerify != nil {
		fake.mutateVerify(&observation)
	}
	return observation, nil
}

func signedArtifact(id, authority, issuedAt, label string) SignedArtifact {
	return SignedArtifact{
		ID: id, ContentDigest: testDigest(label + "/content"), PayloadDigest: testDigest(label + "/payload"),
		SignerSetDigest: testDigest(label + "/signers"), SignerCount: 1, AuthorityIdentity: authority, IssuedAt: issuedAt,
	}
}

func mustRequestDigest(value any) string {
	digest, err := digestRequest(value)
	if err != nil {
		panic(err)
	}
	return digest
}

func pointerCredentialIssue(value CredentialIssueObservation) *CredentialIssueObservation {
	copy := cloneCredentialIssue(value)
	return &copy
}
func pointerRunClosure(value RunClosureObservation) *RunClosureObservation {
	copy := cloneRunClosure(value)
	return &copy
}
func pointerKMS(value KMSAttestationObservation) *KMSAttestationObservation {
	copy := value
	return &copy
}
func pointerRevocation(value CredentialRevocationObservation) *CredentialRevocationObservation {
	copy := cloneCredentialRevocation(value)
	return &copy
}
func pointerIndex(value ArtifactIndexCommitment) *ArtifactIndexCommitment {
	copy := value
	return &copy
}
func pointerReceipt(value QualificationReceiptCommitment) *QualificationReceiptCommitment {
	copy := value
	return &copy
}
func pointerSnapshot(value SnapshotCommitment) *SnapshotCommitment { copy := value; return &copy }

type testRig struct {
	plan        Plan
	trust       TrustBindings
	plans       *fakePlanAuthority
	authorityID string
	clock       *fixedClock
	store       *MemoryStore
	authorities *fakeAuthorities
	service     *Service
}

func newTestRig(t *testing.T) *testRig {
	t.Helper()
	plan, trust := testPlan(t), testTrust()
	clock := &fixedClock{now: time.Date(2026, 7, 19, 12, 10, 0, 0, time.UTC)}
	store := NewMemoryStore(clock)
	plans := newFakePlanAuthority(t, plan, trust)
	authorities := newFakeAuthorities(plan, trust)
	service, err := NewService(Config{
		Store: store, Plans: plans, Credentials: authorities, Capture: authorities, Encryptor: authorities,
		KMS: authorities, Indexer: authorities, Receipt: authorities, Sealer: authorities,
		Verifier: authorities, TrustBindings: trust,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &testRig{
		plan: plan, trust: trust, plans: plans, authorityID: plans.resolution.AuthorityID,
		clock: clock, store: store, authorities: authorities, service: service,
	}
}

func TestExecuteAcceptsOnlyOpaquePlanAuthorityAndResolvesEveryReplay(t *testing.T) {
	trig := newTestRig(t)
	var execute func(context.Context, string) (Result, error) = trig.service.Execute

	if _, err := execute(context.Background(), "qualification-plan"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-UUID authority ID error = %v", err)
	}
	if calls := trig.plans.callCount(); calls != 0 {
		t.Fatalf("invalid opaque ID reached resolver %d times", calls)
	}

	result, err := execute(context.Background(), trig.authorityID)
	if err != nil {
		t.Fatal(err)
	}
	before := trig.authorities.calls
	replayed, err := execute(context.Background(), trig.authorityID)
	if err != nil || !canonicalEqual(result, replayed) {
		t.Fatalf("opaque immutable replay = %#v, %v", replayed, err)
	}
	if calls := trig.plans.callCount(); calls != 2 {
		t.Fatalf("plan authority resolution calls = %d, want 2", calls)
	}
	if trig.authorities.calls != before {
		t.Fatalf("resolved replay repeated external adapters: before=%#v after=%#v", before, trig.authorities.calls)
	}
}

func TestExecuteRejectsPlanAuthorityResolutionDriftBeforeExternalAdapters(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PlanAuthorityResolution)
	}{
		{name: "authority identity alias", mutate: func(value *PlanAuthorityResolution) {
			value.AuthorityID = testUUID(81)
		}},
		{name: "authority hash", mutate: func(value *PlanAuthorityResolution) {
			value.AuthorityHash = "sha256:not-a-digest"
		}},
		{name: "artifact alias", mutate: func(value *PlanAuthorityResolution) {
			value.ArtifactID = "qualification-plan-" + testUUID(81)
		}},
		{name: "evidence plan hash", mutate: func(value *PlanAuthorityResolution) {
			value.EvidencePlanHash = testDigest("drifted-evidence-plan")
		}},
		{name: "noncanonical evidence plan bytes", mutate: func(value *PlanAuthorityResolution) {
			value.EvidencePlanBytes = append(value.EvidencePlanBytes, ' ')
		}},
		{name: "trust bindings", mutate: func(value *PlanAuthorityResolution) {
			value.TrustBindingsDigest = testDigest("foreign-trust-bindings")
		}},
		{name: "plan artifact projection", mutate: func(value *PlanAuthorityResolution) {
			value.Plan.QualificationPlanArtifactID = "qualification-plan-" + testUUID(81)
		}},
		{name: "invalid plan projection", mutate: func(value *PlanAuthorityResolution) {
			value.Plan.SchemaVersion = "worksflow-qualification-evidence-plan/v2"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trig := newTestRig(t)
			trig.plans.mutate(test.mutate)
			if _, err := trig.service.Execute(context.Background(), trig.authorityID); !errors.Is(err, ErrInvalid) {
				t.Fatalf("resolution drift error = %v", err)
			}
			if calls := trig.plans.callCount(); calls != 1 {
				t.Fatalf("resolver calls = %d, want 1", calls)
			}
			if trig.authorities.calls != (authorityCalls{}) {
				t.Fatalf("resolution drift reached an external adapter: %#v", trig.authorities.calls)
			}
			if _, err := trig.store.Load(context.Background(), trig.plan.OrchestrationID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("resolution drift reached durable reservation: %v", err)
			}
		})
	}
}

func TestExecuteFailsClosedOnResolverAndTargetRejection(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "resolver failure", err: errors.New("plan authority storage unavailable")},
		{name: "wrong target envelope", err: fmt.Errorf("%w: authority does not bind the requested workflow target", ErrInvalid)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trig := newTestRig(t)
			trig.plans.err = test.err
			if _, err := trig.service.Execute(context.Background(), trig.authorityID); !errors.Is(err, test.err) {
				t.Fatalf("resolver error = %v, want wrapping %v", err, test.err)
			}
			if trig.authorities.calls != (authorityCalls{}) {
				t.Fatalf("resolver rejection reached an external adapter: %#v", trig.authorities.calls)
			}
		})
	}
}

func TestExecuteReplayRejectsChangedImmutableResolvedPlan(t *testing.T) {
	trig := newTestRig(t)
	if _, err := trig.service.Execute(context.Background(), trig.authorityID); err != nil {
		t.Fatal(err)
	}
	before := trig.authorities.calls
	trig.plans.mutate(func(value *PlanAuthorityResolution) {
		value.Plan.SourceTreeDigest = testDigest("drifted-source-tree")
		planBytes, err := CanonicalJSON(value.Plan)
		if err != nil {
			t.Fatal(err)
		}
		value.EvidencePlanBytes = planBytes
		value.EvidencePlanHash = sha256Digest(planBytes)
		value.AuthorityHash = testDigest("drifted-plan-authority-envelope")
	})
	if _, err := trig.service.Execute(context.Background(), trig.authorityID); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("immutable resolved plan replay drift error = %v", err)
	}
	if calls := trig.plans.callCount(); calls != 2 {
		t.Fatalf("replay did not resolve authority again: calls=%d", calls)
	}
	if trig.authorities.calls != before {
		t.Fatalf("replay drift repeated external adapters: before=%#v after=%#v", before, trig.authorities.calls)
	}
}

func TestExecuteClosesExactLifecycleAndReplayWithoutSecretPersistence(t *testing.T) {
	trig := newTestRig(t)
	trig.authorities.preMutation = func(_ string, operationID string) error {
		snapshot, err := trig.store.Load(context.Background(), trig.plan.OrchestrationID)
		if err != nil || snapshot.ActiveOperationID != operationID || !strings.HasSuffix(string(snapshot.Phase), "started") && snapshot.Phase != PhaseEncrypting {
			return fmt.Errorf("side effect %s did not have a durable started event: %#v, %v", operationID, snapshot, err)
		}
		return nil
	}
	result, err := trig.service.executePlan(context.Background(), trig.plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.SnapshotID != trig.plan.Outputs.SnapshotID || result.Verification.SnapshotDigest != result.Snapshot.SnapshotDigest ||
		result.ArtifactIndex.ArtifactCount != len(trig.plan.Artifacts)+3 || result.ArtifactIndex.RestrictedArtifactCount != 2 {
		t.Fatalf("closed result = %#v", result)
	}
	before := trig.authorities.calls
	replayed, err := trig.service.executePlan(context.Background(), trig.plan)
	if err != nil || !canonicalEqual(result, replayed) || trig.authorities.calls != before {
		t.Fatalf("immutable replay = %#v, %v; calls %#v -> %#v", replayed, err, before, trig.authorities.calls)
	}
	if before.issue != 1 || before.capture != 1 || before.encrypt != 2 || before.kms != 1 || before.revoke != 1 ||
		before.index != 1 || before.receipt != 1 || before.seal != 1 || before.verify != 1 {
		t.Fatalf("mutating/read-only calls = %#v", before)
	}
	for _, operationID := range []string{trig.plan.Artifacts[0].EncryptionOperationID, trig.plan.Artifacts[1].EncryptionOperationID} {
		if _, exists := trig.authorities.encryptions[operationID]; !exists {
			t.Fatalf("restricted operation %s was not encrypted", operationID)
		}
	}
	if len(trig.authorities.encryptions) != 2 {
		t.Fatal("a distributable artifact was sent to the encryptor")
	}
	events, err := trig.store.Events(context.Background(), trig.plan.OrchestrationID)
	if err != nil || len(events) != 20 {
		t.Fatalf("event ledger length = %d, %v", len(events), err)
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"bearer token", "cookie Authorization", "/tmp/credential", "private key", "/secret/evidence"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("append-only non-secret ledger contains %q", forbidden)
		}
	}
	// Returned snapshots and events are deep clones.
	snapshot, _ := trig.store.Load(context.Background(), trig.plan.OrchestrationID)
	snapshot.Plan.Artifacts[0].ID = "mutated"
	snapshot.CredentialIssue.Binding.Members[0].Slot = "mutated"
	events[0].Plan.Artifacts[0].ID = "mutated-again"
	reloaded, _ := trig.store.Load(context.Background(), trig.plan.OrchestrationID)
	if reloaded.Plan.Artifacts[0].ID != "browser-video" || reloaded.CredentialIssue.Binding.Members[0].Slot != "browser-a" {
		t.Fatal("caller mutation escaped into the Store")
	}
}

func TestExternalUnknownRecoversOnlyByInspectAndNeverRepeatsMutation(t *testing.T) {
	tests := []struct {
		kind  string
		calls func(authorityCalls) (int, int)
	}{
		{"issue", func(value authorityCalls) (int, int) { return value.issue, value.inspectIssue }},
		{"capture", func(value authorityCalls) (int, int) { return value.capture, value.inspectCapture }},
		{"encrypt", func(value authorityCalls) (int, int) { return value.encrypt, value.inspectEncrypt }},
		{"kms", func(value authorityCalls) (int, int) { return value.kms, value.inspectKMS }},
		{"revoke", func(value authorityCalls) (int, int) { return value.revoke, value.inspectRevoke }},
		{"index", func(value authorityCalls) (int, int) { return value.index, value.inspectIndex }},
		{"receipt", func(value authorityCalls) (int, int) { return value.receipt, value.inspectReceipt }},
		{"seal", func(value authorityCalls) (int, int) { return value.seal, value.inspectSeal }},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			trig := newTestRig(t)
			trig.authorities.unknownKind = test.kind
			if _, err := trig.service.executePlan(context.Background(), trig.plan); !errors.Is(err, ErrOutcomeUnknown) || strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "cookie") {
				t.Fatalf("first outcome = %v", err)
			}
			if _, err := trig.service.executePlan(context.Background(), trig.plan); err != nil {
				t.Fatal(err)
			}
			mutations, inspections := test.calls(trig.authorities.calls)
			expectedMutations := 1
			if test.kind == "encrypt" {
				expectedMutations = 2 // one call for each distinct restricted artifact
			}
			if mutations != expectedMutations || inspections < 1 {
				t.Fatalf("%s recovery calls mutations=%d inspections=%d", test.kind, mutations, inspections)
			}
		})
	}
}

func TestStartedBeforeExternalCallIsInspectOnlyAndCanRemainPermanentlyFailClosed(t *testing.T) {
	trig := newTestRig(t)
	commandHash, _ := CanonicalDigest(trig.plan)
	trustDigest, _ := CanonicalDigest(trig.trust)
	at := testTime(12, 0, 30)
	plan := clonePlan(trig.plan)
	snapshot, _, err := trig.store.Create(context.Background(), trig.plan.OrchestrationID, Event{
		At: at, EventID: testUUID(70), Kind: EventReserved, OperationID: trig.plan.Operations.Reserve,
		CommandHash: commandHash, TrustBindingsDigest: trustDigest, Plan: &plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = trig.store.Append(context.Background(), trig.plan.OrchestrationID, snapshot.Version, Event{
		At: at, EventID: testUUID(71), Kind: EventCredentialIssueStarted, OperationID: trig.plan.Operations.CredentialIssue,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := trig.service.executePlan(context.Background(), trig.plan); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("inspect-only recovery error = %v", err)
		}
	}
	if trig.authorities.calls.issue != 0 || trig.authorities.calls.inspectIssue != 2 {
		t.Fatalf("started-before-call recovery repeated mutation: %#v", trig.authorities.calls)
	}
}

func TestConcurrentExecutionHasOneMutationOwnerPerOperation(t *testing.T) {
	trig := newTestRig(t)
	var wait sync.WaitGroup
	var unexpected atomic.Int64
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := trig.service.executePlan(context.Background(), trig.plan)
			if err != nil && !errors.Is(err, ErrOutcomeUnknown) && !errors.Is(err, ErrCASConflict) {
				unexpected.Add(1)
			}
		}()
	}
	wait.Wait()
	if unexpected.Load() != 0 {
		t.Fatalf("concurrent executions had %d unexpected errors", unexpected.Load())
	}
	if _, err := trig.service.executePlan(context.Background(), trig.plan); err != nil {
		t.Fatal(err)
	}
	calls := trig.authorities.calls
	if calls.issue != 1 || calls.capture != 1 || calls.encrypt != 2 || calls.kms != 1 || calls.revoke != 1 ||
		calls.index != 1 || calls.receipt != 1 || calls.seal != 1 || calls.verify < 1 {
		t.Fatalf("concurrent mutation owners = %#v", calls)
	}
}

func TestPlanAndTrustFailClosedBeforeReservation(t *testing.T) {
	t.Run("zero restricted artifacts", func(t *testing.T) {
		plan := testPlan(t)
		plan.Artifacts = []ArtifactExpectation{{ID: "playwright-results", Kind: ArtifactKindRunResult, Classification: ClassificationDistributable}}
		if err := ValidatePlan(plan); !errors.Is(err, ErrInvalid) {
			t.Fatalf("zero-restricted plan error = %v", err)
		}
	})
	t.Run("eight operation IDs are unique", func(t *testing.T) {
		plan := testPlan(t)
		plan.Operations.SnapshotSeal = plan.Operations.ReceiptSign
		if err := ValidatePlan(plan); !errors.Is(err, ErrInvalid) {
			t.Fatalf("duplicate operation error = %v", err)
		}
	})
	t.Run("operation cannot collide with root or set identity", func(t *testing.T) {
		for _, collision := range []func(*Plan){
			func(plan *Plan) { plan.Operations.Reserve = plan.OrchestrationID },
			func(plan *Plan) { plan.Operations.RunClosure = plan.RunID },
			func(plan *Plan) { plan.Operations.ReceiptSign = plan.FixtureID },
			func(plan *Plan) { plan.Artifacts[0].EncryptionOperationID = plan.CredentialSet.SetID },
		} {
			plan := testPlan(t)
			collision(&plan)
			if err := ValidatePlan(plan); !errors.Is(err, ErrInvalid) {
				t.Fatalf("cross-domain UUID collision error = %v", err)
			}
		}
	})
	t.Run("credential issuer is server owned", func(t *testing.T) {
		trig := newTestRig(t)
		trig.plan.CredentialSet.Issuer = "spiffe://qualification.example/untrusted-issuer"
		if _, err := trig.service.executePlan(context.Background(), trig.plan); !errors.Is(err, ErrInvalid) {
			t.Fatalf("untrusted issuer error = %v", err)
		}
		if _, err := trig.store.Load(context.Background(), trig.plan.OrchestrationID); !errors.Is(err, ErrNotFound) {
			t.Fatal("untrusted plan reached durable reservation")
		}
	})
	t.Run("qualification plan artifact identity is required", func(t *testing.T) {
		plan := testPlan(t)
		plan.QualificationPlanArtifactID = ""
		if err := ValidatePlan(plan); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unbound external plan digest error = %v", err)
		}
	})
	t.Run("plan authority dependency is required", func(t *testing.T) {
		trig := newTestRig(t)
		if _, err := NewService(Config{
			Store: trig.store, Credentials: trig.authorities, Capture: trig.authorities, Encryptor: trig.authorities,
			KMS: trig.authorities, Indexer: trig.authorities, Receipt: trig.authorities,
			Sealer: trig.authorities, Verifier: trig.authorities, TrustBindings: trig.trust,
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("missing plan authority dependency error = %v", err)
		}
	})
	t.Run("authority roles are independent", func(t *testing.T) {
		trig := newTestRig(t)
		trust := trig.trust
		trust.KMSAuthorityID = trust.EncryptionAuthorityID
		if _, err := NewService(Config{
			Store: trig.store, Plans: trig.plans, Credentials: trig.authorities, Capture: trig.authorities, Encryptor: trig.authorities,
			KMS: trig.authorities, Indexer: trig.authorities, Receipt: trig.authorities,
			Sealer: trig.authorities, Verifier: trig.authorities, TrustBindings: trust,
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("reused authority identity error = %v", err)
		}
	})
}

func TestRunCaptureClosureRejectsMissingExtraAndDuplicateArtifacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RunClosureObservation)
	}{
		{name: "missing", mutate: func(value *RunClosureObservation) { value.Artifacts = value.Artifacts[:len(value.Artifacts)-1] }},
		{name: "extra", mutate: func(value *RunClosureObservation) {
			value.Artifacts = append(value.Artifacts, CapturedArtifact{
				ID: "foreign-artifact", Kind: ArtifactKindRuntimeProof, Classification: ClassificationDistributable,
				CaptureRef: "capture-foreign", ContentDigest: testDigest("foreign"), SizeBytes: 12,
			})
		}},
		{name: "duplicate", mutate: func(value *RunClosureObservation) {
			value.Artifacts[2] = value.Artifacts[1]
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trig := newTestRig(t)
			trig.authorities.mutateCapture = test.mutate
			if _, err := trig.service.executePlan(context.Background(), trig.plan); !errors.Is(err, ErrEvidenceClosure) {
				t.Fatalf("artifact closure error = %v", err)
			}
			snapshot, _ := trig.store.Load(context.Background(), trig.plan.OrchestrationID)
			if snapshot.Phase != PhaseRunClosureStarted || trig.authorities.calls.encrypt != 0 {
				t.Fatalf("invalid capture advanced state: %#v calls=%#v", snapshot, trig.authorities.calls)
			}
		})
	}
}

func TestPartialCredentialRevocationIsRejected(t *testing.T) {
	trig := newTestRig(t)
	trig.authorities.mutateRevoke = func(value *CredentialRevocationObservation) {
		value.Binding.Members = value.Binding.Members[:1]
		value.Binding.MemberCount = 1
		value.Binding.MemberBindingsDigest, _ = credentialMemberDigest(value.Binding.Members)
	}
	if _, err := trig.service.executePlan(context.Background(), trig.plan); !errors.Is(err, ErrCredentialDrift) {
		t.Fatalf("partial revocation error = %v", err)
	}
	snapshot, _ := trig.store.Load(context.Background(), trig.plan.OrchestrationID)
	if snapshot.Phase != PhaseCredentialRevocationStarted || snapshot.CredentialRevocation != nil || trig.authorities.calls.index != 0 {
		t.Fatalf("partial revocation advanced state: %#v calls=%#v", snapshot, trig.authorities.calls)
	}
}

func TestEncryptionRequiresExactAADAndPlaintextDispositionChronology(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*EncryptionCommitment)
		expected error
	}{
		{name: "AAD drift", mutate: func(value *EncryptionCommitment) { value.AdditionalDataHash = testDigest("foreign-aad") }, expected: ErrDigestDrift},
		{name: "missing disposition", mutate: func(value *EncryptionCommitment) { value.PlaintextDisposition = "" }, expected: ErrPlaintextDisposition},
		{name: "deleted at encryption time", mutate: func(value *EncryptionCommitment) {
			value.PlaintextDisposition = PlaintextDeleted
			value.PlaintextDispositionAt = value.EncryptedAt
		}, expected: ErrPlaintextDisposition},
		{name: "disposition before encryption", mutate: func(value *EncryptionCommitment) {
			value.PlaintextDisposition = PlaintextDeleted
			value.EncryptedAt = testTime(12, 3, 0)
			value.PlaintextDispositionAt = testTime(12, 2, 59)
		}, expected: ErrPlaintextDisposition},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trig := newTestRig(t)
			trig.authorities.mutateEncryption = test.mutate
			if _, err := trig.service.executePlan(context.Background(), trig.plan); !errors.Is(err, test.expected) {
				t.Fatalf("encryption error = %v", err)
			}
			if trig.authorities.calls.kms != 0 {
				t.Fatal("invalid encryption reached KMS")
			}
		})
	}
}

func TestKMSReceiptIndexAndSnapshotDigestDriftFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeAuthorities)
	}{
		{name: "run closure request drift", mutate: func(fake *fakeAuthorities) {
			fake.mutateCapture = func(value *RunClosureObservation) { value.RequestDigest = testDigest("foreign-run-request") }
		}},
		{name: "encryption request drift", mutate: func(fake *fakeAuthorities) {
			fake.mutateEncryption = func(value *EncryptionCommitment) { value.RequestDigest = testDigest("foreign-encryption-request") }
		}},
		{name: "KMS swapped projections", mutate: func(fake *fakeAuthorities) {
			fake.mutateKMS = func(value *KMSAttestationObservation) {
				value.ManifestDigest, value.ArtifactSetDigest = value.ArtifactSetDigest, value.ManifestDigest
			}
		}},
		{name: "KMS same projection", mutate: func(fake *fakeAuthorities) {
			fake.mutateKMS = func(value *KMSAttestationObservation) { value.ArtifactSetDigest = value.ManifestDigest }
		}},
		{name: "KMS payload drift", mutate: func(fake *fakeAuthorities) {
			fake.mutateKMS = func(value *KMSAttestationObservation) { value.Attestation.PayloadDigest = testDigest("drift") }
		}},
		{name: "revocation KMS request drift", mutate: func(fake *fakeAuthorities) {
			fake.mutateRevoke = func(value *CredentialRevocationObservation) { value.KMSAttestationDigest = testDigest("foreign-kms") }
		}},
		{name: "index closure drift", mutate: func(fake *fakeAuthorities) {
			fake.mutateIndex = func(value *ArtifactIndexCommitment) { value.EvidenceClosureDigest = testDigest("foreign-closure") }
		}},
		{name: "receipt subject drift", mutate: func(fake *fakeAuthorities) {
			fake.mutateReceipt = func(value *QualificationReceiptCommitment) { value.SubjectIndexDigest = testDigest("foreign-index") }
		}},
		{name: "receipt loses independent signer", mutate: func(fake *fakeAuthorities) {
			fake.mutateReceipt = func(value *QualificationReceiptCommitment) { value.SignerCount = 1 }
		}},
		{name: "seal receipt drift", mutate: func(fake *fakeAuthorities) {
			fake.mutateSeal = func(value *SnapshotCommitment) { value.ReceiptDigest = testDigest("foreign-receipt") }
		}},
		{name: "verification snapshot drift", mutate: func(fake *fakeAuthorities) {
			fake.mutateVerify = func(value *SnapshotVerification) { value.SnapshotDigest = testDigest("foreign-snapshot") }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trig := newTestRig(t)
			test.mutate(trig.authorities)
			if _, err := trig.service.executePlan(context.Background(), trig.plan); !errors.Is(err, ErrDigestDrift) && !errors.Is(err, ErrCredentialDrift) {
				t.Fatalf("digest drift error = %v", err)
			}
			snapshot, _ := trig.store.Load(context.Background(), trig.plan.OrchestrationID)
			if snapshot.Phase == PhaseComplete {
				t.Fatal("digest drift produced a complete snapshot")
			}
		})
	}
}

func TestTrustedChronologyAndObservationUpperBoundFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeAuthorities)
	}{
		{name: "credential issued in future", mutate: func(fake *fakeAuthorities) {
			fake.mutateIssue = func(value *CredentialIssueObservation) {
				value.Binding.IssuedAt = testTime(12, 11, 0)
				value.Attestation.IssuedAt = value.Binding.IssuedAt
			}
		}},
		{name: "credential already expired", mutate: func(fake *fakeAuthorities) {
			fake.mutateIssue = func(value *CredentialIssueObservation) {
				value.Binding.IssuedAt, value.Binding.ExpiresAt = testTime(11, 50, 0), testTime(12, 5, 0)
				value.Attestation.IssuedAt = value.Binding.IssuedAt
			}
		}},
		{name: "run completed at issuance", mutate: func(fake *fakeAuthorities) {
			fake.mutateCapture = func(value *RunClosureObservation) { value.CompletedAt = testTime(12, 0, 0) }
		}},
		{name: "run completed before issuance", mutate: func(fake *fakeAuthorities) {
			fake.mutateCapture = func(value *RunClosureObservation) { value.CompletedAt = testTime(11, 59, 59) }
		}},
		{name: "run completion in future", mutate: func(fake *fakeAuthorities) {
			fake.mutateCapture = func(value *RunClosureObservation) { value.CompletedAt = testTime(12, 11, 0) }
		}},
		{name: "plaintext disposition in future", mutate: func(fake *fakeAuthorities) {
			fake.mutateEncryption = func(value *EncryptionCommitment) {
				value.PlaintextDisposition = PlaintextDeleted
				value.PlaintextDispositionAt = testTime(12, 11, 0)
			}
		}},
		{name: "KMS attestation in future", mutate: func(fake *fakeAuthorities) {
			fake.mutateKMS = func(value *KMSAttestationObservation) { value.Attestation.IssuedAt = testTime(12, 11, 0) }
		}},
		{name: "KMS before plaintext disposition", mutate: func(fake *fakeAuthorities) {
			fake.mutateKMS = func(value *KMSAttestationObservation) { value.Attestation.IssuedAt = testTime(12, 3, 0) }
		}},
		{name: "revocation in future", mutate: func(fake *fakeAuthorities) {
			fake.mutateRevoke = func(value *CredentialRevocationObservation) {
				value.RevokedAt, value.Attestation.IssuedAt = testTime(12, 11, 0), testTime(12, 11, 0)
			}
		}},
		{name: "receipt in future", mutate: func(fake *fakeAuthorities) {
			fake.mutateReceipt = func(value *QualificationReceiptCommitment) { value.IssuedAt = testTime(12, 11, 0) }
		}},
		{name: "seal in future", mutate: func(fake *fakeAuthorities) {
			fake.mutateSeal = func(value *SnapshotCommitment) { value.SealedAt = testTime(12, 11, 0) }
		}},
		{name: "verification in future", mutate: func(fake *fakeAuthorities) {
			fake.mutateVerify = func(value *SnapshotVerification) { value.VerifiedAt = testTime(12, 11, 0) }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trig := newTestRig(t)
			test.mutate(trig.authorities)
			if _, err := trig.service.executePlan(context.Background(), trig.plan); err == nil {
				t.Fatal("invalid or future chronology was accepted")
			}
			snapshot, _ := trig.store.Load(context.Background(), trig.plan.OrchestrationID)
			if snapshot.Phase == PhaseComplete {
				t.Fatal("invalid chronology produced a complete snapshot")
			}
		})
	}
}

func TestPostIssuanceRejectionNeverSignsOrSeals(t *testing.T) {
	trig := newTestRig(t)
	trig.authorities.mutateCapture = func(value *RunClosureObservation) { value.Stage = AuthorityRejected }
	if _, err := trig.service.executePlan(context.Background(), trig.plan); !errors.Is(err, ErrExternalRejected) {
		t.Fatalf("capture rejection error = %v", err)
	}
	snapshot, _ := trig.store.Load(context.Background(), trig.plan.OrchestrationID)
	if snapshot.Phase != PhaseRunClosureStarted || trig.authorities.calls.receipt != 0 || trig.authorities.calls.seal != 0 || snapshot.Phase == PhaseComplete {
		t.Fatalf("post-issuance rejection advanced toward approval: %#v calls=%#v", snapshot, trig.authorities.calls)
	}
	// This internal contract currently relies on the short credential TTL or an
	// external emergency revoker after such a rejection; it does not claim a
	// durable abort-and-revoke liveness path.
}

type ambiguousStore struct {
	delegate Store
	kind     EventKind
	commit   bool
	once     atomic.Bool
}

func (store *ambiguousStore) TrustedTime(ctx context.Context) (time.Time, error) {
	return store.delegate.TrustedTime(ctx)
}
func (store *ambiguousStore) Create(ctx context.Context, id string, event Event) (Snapshot, bool, error) {
	return store.delegate.Create(ctx, id, event)
}
func (store *ambiguousStore) Load(ctx context.Context, id string) (Snapshot, error) {
	return store.delegate.Load(ctx, id)
}
func (store *ambiguousStore) Events(ctx context.Context, id string) ([]Event, error) {
	return store.delegate.Events(ctx, id)
}
func (store *ambiguousStore) Append(ctx context.Context, id string, version uint64, event Event) (Snapshot, error) {
	if event.Kind != store.kind || !store.once.CompareAndSwap(false, true) {
		return store.delegate.Append(ctx, id, version, event)
	}
	if store.commit {
		if _, err := store.delegate.Append(ctx, id, version, event); err != nil {
			return Snapshot{}, err
		}
	}
	return Snapshot{}, ErrStoreOutcomeUnknown
}

func serviceWithStore(t *testing.T, trig *testRig, store Store) *Service {
	t.Helper()
	service, err := NewService(Config{
		Store: store, Plans: trig.plans, Credentials: trig.authorities, Capture: trig.authorities, Encryptor: trig.authorities,
		KMS: trig.authorities, Indexer: trig.authorities, Receipt: trig.authorities,
		Sealer: trig.authorities, Verifier: trig.authorities, TrustBindings: trig.trust,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestStoreUnknownAfterExternalCommitReconcilesWithoutRepeatingSideEffect(t *testing.T) {
	for _, commit := range []bool{true, false} {
		t.Run(fmt.Sprintf("commit-%t", commit), func(t *testing.T) {
			trig := newTestRig(t)
			wrapper := &ambiguousStore{delegate: trig.store, kind: EventEncryptionCommitted, commit: commit}
			trig.service = serviceWithStore(t, trig, wrapper)
			_, firstErr := trig.service.executePlan(context.Background(), trig.plan)
			if commit {
				if firstErr != nil {
					t.Fatal(firstErr)
				}
			} else {
				if !errors.Is(firstErr, ErrOutcomeUnknown) {
					t.Fatalf("uncommitted Store unknown error = %v", firstErr)
				}
				if _, err := trig.service.executePlan(context.Background(), trig.plan); err != nil {
					t.Fatal(err)
				}
			}
			if trig.authorities.calls.encrypt != 2 {
				t.Fatalf("Store response loss repeated encryption: %#v", trig.authorities.calls)
			}
			if !commit && trig.authorities.calls.inspectEncrypt < 1 {
				t.Fatal("uncommitted completion did not recover through Inspect")
			}
		})
	}
}

func TestSnapshotVerifierIsExplicitlyReadOnlyAndSafelyRetryable(t *testing.T) {
	trig := newTestRig(t)
	trig.authorities.verifyUnknown = true
	if _, err := trig.service.executePlan(context.Background(), trig.plan); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("first read-only verify error = %v", err)
	}
	snapshot, _ := trig.store.Load(context.Background(), trig.plan.OrchestrationID)
	if snapshot.Phase != PhaseSnapshotSealed || trig.authorities.calls.seal != 1 {
		t.Fatalf("verifier failure changed sealed state: %#v calls=%#v", snapshot, trig.authorities.calls)
	}
	if _, err := trig.service.executePlan(context.Background(), trig.plan); err != nil {
		t.Fatal(err)
	}
	if trig.authorities.calls.verify != 2 || trig.authorities.calls.seal != 1 {
		t.Fatalf("read-only retry repeated a mutation: %#v", trig.authorities.calls)
	}
}

func TestMaximumRestrictedArtifactPlanCanReachComplete(t *testing.T) {
	trig := newTestRig(t)
	artifacts := make([]ArtifactExpectation, MaximumArtifacts)
	for index := range artifacts {
		kind := ArtifactKindLog
		if index == 0 {
			kind = ArtifactKindTrace
		} else if index == 1 {
			kind = ArtifactKindVideo
		}
		artifacts[index] = ArtifactExpectation{
			ID: fmt.Sprintf("artifact-%03d", index), Kind: kind, Classification: ClassificationRestricted,
			EncryptionOperationID: fmt.Sprintf("20000000-0000-4000-8000-%012d", index+1),
		}
	}
	trig.plan.Artifacts = artifacts
	trig.authorities.plan = clonePlan(trig.plan)
	result, err := trig.service.executePlan(context.Background(), trig.plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.ArtifactIndex.RestrictedArtifactCount != MaximumArtifacts || trig.authorities.calls.encrypt != MaximumArtifacts {
		t.Fatalf("maximum plan closure count=%d encryptCalls=%d", result.ArtifactIndex.RestrictedArtifactCount, trig.authorities.calls.encrypt)
	}
}
