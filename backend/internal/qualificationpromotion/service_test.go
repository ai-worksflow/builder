package qualificationpromotion

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
)

type fakeVerifier struct {
	mu       sync.Mutex
	verified qualificationreceipt.VerifiedPromotion
	err      error
	calls    int
}

func (verifier *fakeVerifier) Verify(_, _, _ string, _ qualificationreceipt.ExpectedPromotion) (qualificationreceipt.VerifiedPromotion, error) {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	verifier.calls++
	return verifier.verified, verifier.err
}

type fakeExpectationAuthority struct {
	mu         sync.Mutex
	resolution AuthorityResolution
	err        error
	calls      int
}

func (authority *fakeExpectationAuthority) Resolve(_ context.Context, _ uuid.UUID) (AuthorityResolution, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.calls++
	return authority.resolution, authority.err
}

func TestConsumeIsAtomicIdempotentAndReplaySurvivesExpiry(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	verified := validVerifiedPromotion(now)
	service, memory, verifier, authority := newTestService(t, &now, verified)
	command := validConsumeCommand()

	first, err := service.Consume(context.Background(), command)
	if err != nil || first.Idempotent || first.Handoff.State != HandoffStatePending || first.ConsumedAt != now {
		t.Fatalf("first Consume() = record:%+v error:%v", first, err)
	}
	if len(memory.byOperation) != 1 || len(memory.byNonce) != 1 || first.Handoff.CreatedAt != first.ConsumedAt {
		t.Fatalf("ledger/handoff were not one atomic fact: operations=%d nonces=%d record=%+v", len(memory.byOperation), len(memory.byNonce), first)
	}
	now = now.Add(20 * time.Minute)
	verifier.err = errors.New("sealed snapshot retired")
	authority.err = errors.New("authority retired")
	replayed, err := service.Consume(context.Background(), command)
	if err != nil || !replayed.Idempotent || !sameImmutableRecord(replayed, first) {
		t.Fatalf("post-expiry exact replay = record:%+v error:%v", replayed, err)
	}
	if verifier.calls != 1 || authority.calls != 1 {
		t.Fatalf("exact replay re-entered authority/verifier: resolver=%d verifier=%d", authority.calls, verifier.calls)
	}
}

func TestNewConsumptionRejectsExpiredAuthority(t *testing.T) {
	issuedAt := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	now := issuedAt.Add(20 * time.Minute)
	service, memory, _, _ := newTestService(t, &now, validVerifiedPromotion(issuedAt))
	_, err := service.Consume(context.Background(), validConsumeCommand())
	if !errors.Is(err, ErrAuthorityExpired) {
		t.Fatalf("expired new Consume() error = %v", err)
	}
	if len(memory.byOperation) != 0 || len(memory.byNonce) != 0 {
		t.Fatal("expired authority wrote partial ledger state")
	}
}

func TestOperationAndNonceConflictsPreserveExactBytes(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	verified := validVerifiedPromotion(now)
	service, memory, verifier, authority := newTestService(t, &now, verified)
	command := validConsumeCommand()
	first, err := service.Consume(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}

	differentOperationBytes := command
	differentOperationBytes.OutputRevisionID = uuid.New()
	if _, err := service.Consume(context.Background(), differentOperationBytes); !errors.Is(err, ErrConflict) {
		t.Fatalf("same operation/different output revision error = %v", err)
	}

	for name, mutate := range map[string]func(*qualificationreceipt.VerifiedPromotion){
		"target": func(value *qualificationreceipt.VerifiedPromotion) { value.PromotionTarget.NodeKey = "different-node" },
		"authority digest": func(value *qualificationreceipt.VerifiedPromotion) {
			value.PromotionAuthorityDigest = testDigest("different-authority")
		},
		"Golden root": func(value *qualificationreceipt.VerifiedPromotion) {
			value.GoldenRuntime.FixtureDocumentDigest = testDigest("different-fixture")
		},
		"evidence": func(value *qualificationreceipt.VerifiedPromotion) {
			value.ArtifactIndexDigest = testDigest("different-index")
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := verified
			mutate(&changed)
			verifier.verified = changed
			authority.resolution.Target = changed.PromotionTarget
			authority.resolution.Verification.Expected.PromotionTarget = changed.PromotionTarget
			other := command
			other.OperationID = uuid.New()
			other.HandoffID = uuid.New()
			other.OutputRevisionID = uuid.New()
			other.QualificationAuthorityID = uuid.New()
			if _, err := service.Consume(context.Background(), other); !errors.Is(err, ErrConflict) {
				t.Fatalf("nonce reuse with %s drift error = %v", name, err)
			}
		})
	}
	if len(memory.byOperation) != 1 || memory.byOperation[first.OperationID].RequestHash != first.RequestHash {
		t.Fatal("conflicting replays changed the immutable consumption")
	}
}

func TestDistinctNonceIsDistinctAuthorizedConsumption(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	verified := validVerifiedPromotion(now)
	service, memory, verifier, _ := newTestService(t, &now, verified)
	firstCommand := validConsumeCommand()
	first, err := service.Consume(context.Background(), firstCommand)
	if err != nil {
		t.Fatal(err)
	}
	secondVerified := verified
	secondVerified.AuthorityNonce = uuid.NewString()
	verifier.verified = secondVerified
	second := firstCommand
	second.OperationID = uuid.New()
	second.QualificationAuthorityID = uuid.New()
	second.HandoffID = uuid.New()
	second.OutputRevisionID = uuid.New()
	secondRecord, err := service.Consume(context.Background(), second)
	if err != nil || secondRecord.OperationID == first.OperationID || secondRecord.Idempotent {
		t.Fatalf("distinct nonce Consume() = record:%+v error:%v", secondRecord, err)
	}
	if len(memory.byOperation) != 2 || len(memory.byNonce) != 2 {
		t.Fatalf("distinct nonce was silently aliased: operations=%d nonces=%d", len(memory.byOperation), len(memory.byNonce))
	}
}

func TestInvalidVerifierAndTargetMismatchFailBeforeAppend(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	verified := validVerifiedPromotion(now)
	service, memory, verifier, authority := newTestService(t, &now, verified)
	command := validConsumeCommand()

	verifier.err = errors.New("signature threshold failed")
	if _, err := service.Consume(context.Background(), command); err == nil {
		t.Fatal("unverified evidence was accepted")
	}
	verifier.err = nil
	verifier.verified.Decision = ""
	if _, err := service.Consume(context.Background(), command); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid verifier projection error = %v", err)
	}
	verifier.verified = verified
	authority.resolution.Target.NodeKey = "stale-node"
	if _, err := service.Consume(context.Background(), command); !errors.Is(err, ErrInvalid) {
		t.Fatalf("authority expectation mismatch error = %v", err)
	}
	authority.resolution.Target = verified.PromotionTarget
	authority.resolution.Verification.Expected.PromotionTarget = verified.PromotionTarget
	verifier.verified.PromotionTarget.NodeKey = "other-node"
	if _, err := service.Consume(context.Background(), command); !errors.Is(err, ErrInvalid) {
		t.Fatalf("verifier target mismatch error = %v", err)
	}
	if len(memory.byOperation) != 0 {
		t.Fatal("invalid/unverified consumption created ledger state")
	}
}

func TestConcurrentWritersCreateOneLedgerAndHandoff(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	service, memory, _, _ := newTestService(t, &now, validVerifiedPromotion(now))
	command := validConsumeCommand()
	const writers = 16
	results := make(chan ConsumptionRecord, writers)
	errorsFound := make(chan error, writers)
	var group sync.WaitGroup
	for index := 0; index < writers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			record, err := service.Consume(context.Background(), command)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- record
		}()
	}
	group.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("concurrent Consume() error = %v", err)
	}
	firstWrites := 0
	var requestHash string
	for record := range results {
		if !record.Idempotent {
			firstWrites++
		}
		if requestHash == "" {
			requestHash = record.RequestHash
		} else if requestHash != record.RequestHash {
			t.Fatal("concurrent writers observed different immutable results")
		}
	}
	if firstWrites != 1 || len(memory.byOperation) != 1 || len(memory.byNonce) != 1 {
		t.Fatalf("concurrent first writes=%d operations=%d nonces=%d", firstWrites, len(memory.byOperation), len(memory.byNonce))
	}
}

func TestCommitUnknownIsReconstructedBySameOperation(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	service, memory, _, _ := newTestService(t, &now, validVerifiedPromotion(now))
	memory.unknownAfterCommitOnce = true
	command := validConsumeCommand()
	record, err := service.Consume(context.Background(), command)
	if err != nil || !record.Idempotent || record.OperationID != command.OperationID {
		t.Fatalf("unknown commit reconstruction = record:%+v error:%v", record, err)
	}
	inspected, err := service.InspectOperation(context.Background(), command.OperationID)
	if err != nil || !sameImmutableRecord(inspected, record) {
		t.Fatalf("InspectOperation() = record:%+v error:%v", inspected, err)
	}
	byKey, err := service.InspectKey(context.Background(), ConsumptionKey{
		Target:                   record.VerifiedPromotion.PromotionTarget,
		AuthorityNonce:           record.VerifiedPromotion.AuthorityNonce,
		PromotionAuthorityDigest: record.VerifiedPromotion.PromotionAuthorityDigest,
	})
	if err != nil || byKey.OperationID != command.OperationID {
		t.Fatalf("InspectKey() = record:%+v error:%v", byKey, err)
	}
}

func TestConsumeCommandHasNoClientAuthorityProjectionSurface(t *testing.T) {
	commandType := reflect.TypeOf(ConsumeCommand{})
	if commandType.NumField() != 4 {
		t.Fatalf("ConsumeCommand field count = %d, want four opaque/preallocated IDs", commandType.NumField())
	}
	for index := 0; index < commandType.NumField(); index++ {
		field := commandType.Field(index)
		if field.Type != reflect.TypeOf(uuid.UUID{}) {
			t.Fatalf("ConsumeCommand.%s exposes non-opaque request projection type %s", field.Name, field.Type)
		}
	}
	for _, forbidden := range []reflect.Type{
		reflect.TypeOf(qualificationreceipt.VerifiedPromotion{}),
		reflect.TypeOf(qualificationreceipt.ExpectedPromotion{}),
		reflect.TypeOf(qualificationreceipt.PromotionTarget{}),
		reflect.TypeOf(VerificationInput{}),
	} {
		for index := 0; index < commandType.NumField(); index++ {
			if commandType.Field(index).Type == forbidden {
				t.Fatalf("raw client authority type %s entered ConsumeCommand", forbidden)
			}
		}
	}
}

func newTestService(t *testing.T, now *time.Time, verified qualificationreceipt.VerifiedPromotion) (*Service, *MemoryStore, *fakeVerifier, *fakeExpectationAuthority) {
	t.Helper()
	memory, err := NewMemoryStore(func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	verifier := &fakeVerifier{verified: verified}
	authority := &fakeExpectationAuthority{resolution: AuthorityResolution{
		Target: verified.PromotionTarget,
		Verification: VerificationInput{
			ReceiptPath:  "/sealed/receipt.dsse.json",
			IndexPath:    "/sealed/index.json",
			ArtifactRoot: "/sealed/artifacts",
			Expected:     qualificationreceipt.ExpectedPromotion{PromotionTarget: verified.PromotionTarget},
		},
	}}
	service, err := NewService(verifier, authority, memory)
	if err != nil {
		t.Fatal(err)
	}
	return service, memory, verifier, authority
}

func validConsumeCommand() ConsumeCommand {
	return ConsumeCommand{
		OperationID: uuid.New(), QualificationAuthorityID: uuid.New(),
		HandoffID: uuid.New(), OutputRevisionID: uuid.New(),
	}
}

func validVerifiedPromotion(now time.Time) qualificationreceipt.VerifiedPromotion {
	return qualificationreceipt.VerifiedPromotion{
		Scope: qualificationreceipt.ExternalQualificationScope,
		PromotionTarget: qualificationreceipt.PromotionTarget{
			ProjectID: uuid.NewString(), WorkflowRunID: uuid.NewString(), NodeKey: "external-qualification",
			TargetRevision: qualificationreceipt.PromotionTargetRevision{ID: uuid.NewString(), ContentHash: testDigest("target-revision")},
			Subject:        "ai-constructor", StageGate: qualificationreceipt.ExternalQualificationGate,
		},
		AuthorityNonce: uuid.NewString(), AuthorityExpiresAt: now.Add(15 * time.Minute).Format(canonicalTimeLayout),
		PromotionAuthorityDigest: testDigest("authority"),
		GoldenRuntime: qualificationreceipt.GoldenRuntimeBinding{
			AuthorityDocumentArtifactID: "golden-authority", AuthorityDocumentDigest: testDigest("golden-authority"),
			FaultOperationSetDigest:   qualificationreceipt.GoldenFaultOperationSetDigestV1,
			FixtureDocumentArtifactID: "golden-fixture", FixtureDocumentDigest: testDigest("golden-fixture"), FixtureID: uuid.NewString(),
		},
		CredentialSet: qualificationreceipt.VerifiedCredentialSetBinding{
			Issuer: "spiffe://qualification/credential-issuer", Audience: "https://golden.example.test",
			SetHandleHash: testDigest("credential-handle"), MemberBindingsDigest: testDigest("credential-members"), MemberCount: 11,
			IssuanceArtifactID: "credential-issuance", IssuancePayloadDigest: testDigest("issuance"),
			RevocationArtifactID: "credential-revocation", RevocationPayloadDigest: testDigest("revocation"),
		},
		SingleUseConsumption: qualificationreceipt.SingleUseConsumptionPolicy,
		RunID:                uuid.NewString(), PlanDigest: testDigest("plan"), ArtifactIndexDigest: testDigest("artifact-index"),
		ReceiptPayloadDigest: testDigest("receipt-payload"), ReceiptBundleDigest: testDigest("receipt-bundle"),
		SignerIdentities:                     []string{"approver@golden.example", "runner@golden.example"},
		CredentialIssuanceSignerIdentities:   []string{"credential-issuer@golden.example"},
		CredentialRevocationSignerIdentities: []string{"credential-revoker@golden.example"},
		EncryptionSignerIdentities:           []string{"kms@golden.example"},
		FaultAuthoritySignerIdentities:       []string{"fault-operator@golden.example"},
		FaultLedgerAttestationDigest:         testDigest("fault-ledger"),
		FaultLedgerAttestorSignerIdentities:  []string{"fault-attestor@golden.example"},
		IssuedAt:                             now.Add(-time.Minute).Format(canonicalTimeLayout), Decision: "qualified",
	}
}

func testDigest(label string) string {
	return sha256Digest([]byte(fmt.Sprintf("qualification-promotion-test:%s", label)))
}
