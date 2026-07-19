package qualificationplanauthority

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationevidence"
	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
)

type fakeInputAuthority struct {
	mu     sync.Mutex
	values map[uuid.UUID]ResolvedInputs
	err    error
	calls  int
	lastID uuid.UUID
}

func (authority *fakeInputAuthority) Resolve(_ context.Context, inputID uuid.UUID) (ResolvedInputs, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.calls++
	authority.lastID = inputID
	if authority.err != nil {
		return ResolvedInputs{}, authority.err
	}
	value, exists := authority.values[inputID]
	if !exists {
		return ResolvedInputs{}, ErrNotFound
	}
	return cloneResolvedInputs(value), nil
}

func TestFreezeUsesOpaqueInputAndReturnsExactAuthorityResolution(t *testing.T) {
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	command := validFreezeCommand()
	resolved := validResolvedInputs(t, 3)
	service, store, authority := newTestService(t, now, command.InputAuthorityID, resolved)

	record, err := service.Freeze(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if record.Idempotent || record.FrozenAt != now || authority.calls != 1 || authority.lastID != command.InputAuthorityID {
		t.Fatalf("Freeze() = record:%+v resolver calls:%d last:%s", record, authority.calls, authority.lastID)
	}
	if got := reflect.TypeOf(FreezeCommand{}).NumField(); got != 3 {
		t.Fatalf("FreezeCommand acquired caller-controlled fields: %d", got)
	}
	wantArtifact := QualificationPlanArtifactPrefix + command.AuthorityID.String()
	if record.Envelope.ArtifactID != wantArtifact || record.EvidencePlan.QualificationPlanArtifactID != wantArtifact {
		t.Fatalf("deterministic artifact binding = envelope:%q plan:%q", record.Envelope.ArtifactID, record.EvidencePlan.QualificationPlanArtifactID)
	}
	if record.ProjectionHash != resolved.Input.QualificationPlanDigest || record.EvidencePlanHash == record.ProjectionHash ||
		record.EnvelopeHash == record.EvidencePlanHash || record.EnvelopeHash == record.ProjectionHash {
		t.Fatalf("digest domains were confused: projection=%s evidence=%s authority=%s", record.ProjectionHash, record.EvidencePlanHash, record.EnvelopeHash)
	}
	if err := validateStoredRecord(record); err != nil {
		t.Fatalf("stored record failed independent validation: %v", err)
	}
	resolution, err := service.Resolve(context.Background(), command.AuthorityID.String())
	if err != nil {
		t.Fatal(err)
	}
	if resolution.AuthorityID != command.AuthorityID.String() || resolution.AuthorityHash != record.EnvelopeHash ||
		resolution.ArtifactID != wantArtifact || resolution.EvidencePlanHash != record.EvidencePlanHash ||
		!reflect.DeepEqual(resolution.Plan, record.EvidencePlan) || string(resolution.EvidencePlanBytes) != string(record.EvidencePlanBytes) {
		t.Fatalf("PlanAuthority resolution drifted: %+v", resolution)
	}
	if _, err := store.ResolveAuthority(context.Background(), command.AuthorityID); err != nil {
		t.Fatal(err)
	}
}

func TestReplayInspectsBeforeInputAuthorityAndRejectsCommandDrift(t *testing.T) {
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	command := validFreezeCommand()
	service, _, authority := newTestService(t, now, command.InputAuthorityID, validResolvedInputs(t, 3))
	first, err := service.Freeze(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	authority.mu.Lock()
	authority.err = errors.New("immutable input retired")
	authority.mu.Unlock()
	replayed, err := service.Freeze(context.Background(), command)
	if err != nil || !replayed.Idempotent || !sameImmutableRecord(first, replayed) {
		t.Fatalf("exact replay = record:%+v error:%v", replayed, err)
	}
	if authority.calls != 1 {
		t.Fatalf("exact replay re-resolved immutable input: %d calls", authority.calls)
	}
	for name, mutate := range map[string]func(*FreezeCommand){
		"authority":       func(value *FreezeCommand) { value.AuthorityID = uuid.New() },
		"input authority": func(value *FreezeCommand) { value.InputAuthorityID = uuid.New() },
	} {
		t.Run(name, func(t *testing.T) {
			drifted := command
			mutate(&drifted)
			if _, err := service.Freeze(context.Background(), drifted); !errors.Is(err, ErrConflict) {
				t.Fatalf("drifted replay error = %v", err)
			}
		})
	}
	if authority.calls != 1 {
		t.Fatalf("drifted replay re-resolved immutable input: %d calls", authority.calls)
	}
}

func TestMemoryStoreGloballyReservesAllIdentityKinds(t *testing.T) {
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	firstCommand := validFreezeCommand()
	firstInput := validResolvedInputs(t, 3)
	inputAuthority := &fakeInputAuthority{values: map[uuid.UUID]ResolvedInputs{firstCommand.InputAuthorityID: firstInput}}
	store := NewMemoryStore(func() time.Time { return now })
	service, err := NewService(inputAuthority, store)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Freeze(context.Background(), firstCommand)
	if err != nil {
		t.Fatal(err)
	}
	restrictedOperation := ""
	for _, artifact := range first.EvidencePlan.Artifacts {
		if artifact.EncryptionOperationID != "" {
			restrictedOperation = artifact.EncryptionOperationID
			break
		}
	}
	for name, collision := range map[string]string{
		"orchestration":         first.EvidencePlan.OrchestrationID,
		"run":                   first.EvidencePlan.RunID,
		"fixed operation":       first.EvidencePlan.Operations.ReceiptSign,
		"restricted encryption": restrictedOperation,
	} {
		t.Run(name, func(t *testing.T) {
			command := validFreezeCommand()
			input := validResolvedInputs(t, 3)
			input.Input.Credential.SetID = collision
			refreshResolvedInput(t, &input)
			inputAuthority.mu.Lock()
			inputAuthority.values[command.InputAuthorityID] = input
			inputAuthority.mu.Unlock()
			if _, err := service.Freeze(context.Background(), command); !errors.Is(err, ErrConflict) {
				t.Fatalf("cross-kind UUID reuse error = %v", err)
			}
		})
	}

	command := validFreezeCommand()
	inputAuthority.mu.Lock()
	inputAuthority.values[command.InputAuthorityID] = validResolvedInputs(t, 3)
	inputAuthority.mu.Unlock()
	command.AuthorityID = firstCommand.AuthorityID
	if _, err := service.Freeze(context.Background(), command); !errors.Is(err, ErrConflict) {
		t.Fatalf("authority/artifact reuse error = %v", err)
	}

	command = validFreezeCommand()
	command.InputAuthorityID = firstCommand.InputAuthorityID
	if _, err := service.Freeze(context.Background(), command); !errors.Is(err, ErrConflict) {
		t.Fatalf("input authority reuse error = %v", err)
	}

	// Golden FixtureID is an immutable upstream reference, not an identity
	// allocated by this authority. A later run must be able to reuse it.
	command = validFreezeCommand()
	sharedFixture := validResolvedInputs(t, 3)
	sharedFixture.Input.GoldenRuntime.FixtureID = first.EvidencePlan.FixtureID
	refreshResolvedInput(t, &sharedFixture)
	inputAuthority.mu.Lock()
	inputAuthority.values[command.InputAuthorityID] = sharedFixture
	inputAuthority.mu.Unlock()
	if _, err := service.Freeze(context.Background(), command); err != nil {
		t.Fatalf("stable Golden fixture reuse failed: %v", err)
	}
}

func TestMaximumArtifactPlanCompilesAllRestrictedOperations(t *testing.T) {
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	command := validFreezeCommand()
	resolved := validResolvedInputs(t, qualificationevidence.MaximumArtifacts)
	service, _, _ := newTestService(t, now, command.InputAuthorityID, resolved)
	record, err := service.Freeze(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.EvidencePlan.Artifacts) != qualificationevidence.MaximumArtifacts {
		t.Fatalf("artifact count = %d", len(record.EvidencePlan.Artifacts))
	}
	restricted := 0
	seen := map[string]struct{}{}
	for _, artifact := range record.EvidencePlan.Artifacts {
		if artifact.Classification == qualificationevidence.ClassificationRestricted {
			restricted++
			if !validUUIDv4(artifact.EncryptionOperationID) {
				t.Fatalf("restricted artifact %s has invalid operation", artifact.ID)
			}
			if _, duplicate := seen[artifact.EncryptionOperationID]; duplicate {
				t.Fatalf("duplicate restricted operation %s", artifact.EncryptionOperationID)
			}
			seen[artifact.EncryptionOperationID] = struct{}{}
		}
	}
	if restricted < 2 {
		t.Fatalf("restricted closure count = %d", restricted)
	}
}

func TestInvalidResolvedBindingsFailBeforeFreeze(t *testing.T) {
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*ResolvedInputs){
		"target":              func(value *ResolvedInputs) { value.Input.PromotionTarget.TargetRevision.ContentHash = "latest" },
		"dirty source":        func(value *ResolvedInputs) { value.Input.Source.Dirty = true },
		"unreviewed template": func(value *ResolvedInputs) { value.Input.TemplateRelease.ApprovalReceiptDigest = "" },
		"trust role alias": func(value *ResolvedInputs) {
			value.Input.TrustBindings.VerifierAuthorityID = value.Input.TrustBindings.ReceiptAuthorityID
		},
		"credential issuer drift": func(value *ResolvedInputs) { value.Input.Credential.Issuer = "other-credential-authority" },
		"credential identity cycle": func(value *ResolvedInputs) {
			value.Input.Credential.SetID = value.Input.GoldenRuntime.FixtureID
		},
		"credential hash cycle": func(value *ResolvedInputs) {
			value.Input.Credential.MemberBindingsDigest = value.Input.Credential.SetHandleHash
		},
	} {
		t.Run(name, func(t *testing.T) {
			command := validFreezeCommand()
			resolved := validResolvedInputs(t, 3)
			mutate(&resolved)
			refreshResolvedInput(t, &resolved)
			service, store, _ := newTestService(t, now, command.InputAuthorityID, resolved)
			if _, err := service.Freeze(context.Background(), command); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid binding error = %v", err)
			}
			if _, err := store.InspectOperation(context.Background(), command.OperationID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("invalid input reached store: %v", err)
			}
		})
	}
}

func TestRawInputAndManifestProjectionDriftAreRejected(t *testing.T) {
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*ResolvedInputs){
		"input bytes":       func(value *ResolvedInputs) { value.InputBytes = append(value.InputBytes, ' ') },
		"input hash":        func(value *ResolvedInputs) { value.InputHash = testDigest("wrong-input") },
		"projection bytes":  func(value *ResolvedInputs) { value.QualificationPlanBytes = append(value.QualificationPlanBytes, ' ') },
		"projection digest": func(value *ResolvedInputs) { value.Input.QualificationPlanDigest = testDigest("wrong-plan") },
	} {
		t.Run(name, func(t *testing.T) {
			command := validFreezeCommand()
			resolved := validResolvedInputs(t, 3)
			mutate(&resolved)
			authority := &fakeInputAuthority{values: map[uuid.UUID]ResolvedInputs{command.InputAuthorityID: resolved}}
			store := NewMemoryStore(func() time.Time { return now })
			service, _ := NewService(authority, store)
			if _, err := service.Freeze(context.Background(), command); !errors.Is(err, ErrInvalid) {
				t.Fatalf("raw drift error = %v", err)
			}
		})
	}
}

func TestQualificationProjectionRootIsClosedAndTyped(t *testing.T) {
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(map[string]any){
		"missing":             func(value map[string]any) { delete(value, "supportFiles") },
		"extra":               func(value map[string]any) { value["status"] = "qualified" },
		"wrong type":          func(value map[string]any) { value["sourceDocuments"] = "docs" },
		"empty suite closure": func(value map[string]any) { value["suites"] = []any{} },
	} {
		t.Run(name, func(t *testing.T) {
			command := validFreezeCommand()
			resolved := validResolvedInputs(t, 3)
			var projection map[string]any
			if err := json.Unmarshal(resolved.QualificationPlanBytes, &projection); err != nil {
				t.Fatal(err)
			}
			mutate(projection)
			encoded, err := canonicalJSON(projection)
			if err != nil {
				t.Fatal(err)
			}
			resolved.QualificationPlanBytes = encoded
			resolved.Input.QualificationPlanDigest = sha256Digest(encoded)
			refreshResolvedInput(t, &resolved)
			service, store, _ := newTestService(t, now, command.InputAuthorityID, resolved)
			if _, err := service.Freeze(context.Background(), command); !errors.Is(err, ErrInvalid) {
				t.Fatalf("non-closed projection root error = %v", err)
			}
			if _, err := store.InspectOperation(context.Background(), command.OperationID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("non-closed projection reached Store: %v", err)
			}
		})
	}
}

func TestConcurrentExactFreezeCreatesOneImmutableAuthority(t *testing.T) {
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	command := validFreezeCommand()
	service, store, _ := newTestService(t, now, command.InputAuthorityID, validResolvedInputs(t, 3))
	const writers = 32
	results := make(chan Record, writers)
	errorsFound := make(chan error, writers)
	var group sync.WaitGroup
	for index := 0; index < writers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			record, err := service.Freeze(context.Background(), command)
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
		t.Fatalf("concurrent Freeze() error = %v", err)
	}
	firstWrites := 0
	hash := ""
	for record := range results {
		if !record.Idempotent {
			firstWrites++
		}
		if hash == "" {
			hash = record.EnvelopeHash
		} else if record.EnvelopeHash != hash {
			t.Fatal("concurrent writers returned distinct envelopes")
		}
	}
	if firstWrites != 1 {
		t.Fatalf("non-idempotent first writers = %d", firstWrites)
	}
	stored, err := store.InspectOperation(context.Background(), command.OperationID)
	if err != nil || stored.EnvelopeHash != hash {
		t.Fatalf("stored authority = record:%+v error:%v", stored, err)
	}
}

func TestCommitUnknownReconcilesExactOperation(t *testing.T) {
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	command := validFreezeCommand()
	service, store, authority := newTestService(t, now, command.InputAuthorityID, validResolvedInputs(t, 3))
	store.InjectCommitUnknownOnce()
	reconciled, err := service.Freeze(context.Background(), command)
	if err != nil || !reconciled.Idempotent || reconciled.FrozenAt != now {
		t.Fatalf("commit-unknown reconciliation = record:%+v error:%v", reconciled, err)
	}
	authority.mu.Lock()
	authority.err = errors.New("must not be called")
	authority.mu.Unlock()
	replayed, err := service.Freeze(context.Background(), command)
	if err != nil || !replayed.Idempotent || replayed.EnvelopeHash != reconciled.EnvelopeHash {
		t.Fatalf("post-reconciliation replay = record:%+v error:%v", replayed, err)
	}
	if authority.calls != 1 {
		t.Fatalf("commit-unknown/replay resolved input %d times", authority.calls)
	}
}

func validFreezeCommand() FreezeCommand {
	return FreezeCommand{OperationID: uuid.New(), AuthorityID: uuid.New(), InputAuthorityID: uuid.New()}
}

func newTestService(t *testing.T, now time.Time, inputID uuid.UUID, resolved ResolvedInputs) (*Service, *MemoryStore, *fakeInputAuthority) {
	t.Helper()
	authority := &fakeInputAuthority{values: map[uuid.UUID]ResolvedInputs{inputID: resolved}}
	store := NewMemoryStore(func() time.Time { return now })
	service, err := NewService(authority, store)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, authority
}

func validResolvedInputs(t *testing.T, artifactCount int) ResolvedInputs {
	t.Helper()
	subject := "worksflow-project-level-ai-constructor"
	projection, err := canonicalJSON(map[string]any{
		"manifestSchemaVersion": qualificationreceipt.QualificationManifestSchemaV1,
		"policy":                map[string]any{"stageExitRequiresExternalQualification": true},
		"schemaVersion":         "worksflow-qualification-plan/v1",
		"sourceDocuments":       []any{map[string]any{"path": "docs/spec.md", "sha256": testDigest("spec")}},
		"subject":               subject,
		"suites":                []any{map[string]any{"id": "external-golden"}},
		"supportFiles":          []any{map[string]any{"path": "qualification/test-inventory.json", "sha256": testDigest("inventory")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	artifacts := make([]ArtifactExpectation, 0, artifactCount)
	for index := 0; index < artifactCount; index++ {
		artifacts = append(artifacts, ArtifactExpectation{
			ID: fmt.Sprintf("artifact-%03d", index), Kind: qualificationevidence.ArtifactKindRunResult,
			Classification: qualificationevidence.ClassificationDistributable,
		})
	}
	if artifactCount >= 2 {
		artifacts[0] = ArtifactExpectation{ID: "browser-video", Kind: qualificationevidence.ArtifactKindVideo, Classification: qualificationevidence.ClassificationRestricted}
		artifacts[1] = ArtifactExpectation{ID: "credential-safe-trace", Kind: qualificationevidence.ArtifactKindTrace, Classification: qualificationevidence.ClassificationRestricted}
	}
	sort.Slice(artifacts, func(left, right int) bool { return artifacts[left].ID < artifacts[right].ID })
	input := ResolvedInputDocument{
		ArtifactPolicy: ArtifactPolicy{
			MaximumArtifacts: qualificationevidence.MaximumArtifacts, RequireRestrictedEncryption: true,
			RequireTrace: true, RequireVideo: true,
		},
		Artifacts:     artifacts,
		BuildContract: ImmutableContentBinding{ID: "build-contract-v1", ContentHash: testDigest("build-contract")},
		BuildManifest: ImmutableContentBinding{ID: "build-manifest-v1", ContentHash: testDigest("build-manifest")},
		Credential: qualificationevidence.CredentialExpectation{
			SetID: uuid.NewString(), Issuer: "credential-authority", Audience: "urn:worksflow:qualification",
			SetHandleHash: testDigest("set-handle"), MemberBindingsDigest: testDigest("members"), MemberCount: 3,
			IssuanceArtifactID: "credential-set-issuance", RevocationArtifactID: "credential-set-revocation",
		},
		GoldenRuntime: qualificationreceipt.GoldenRuntimeBinding{
			AuthorityDocumentArtifactID: "golden-authority-document", AuthorityDocumentDigest: testDigest("golden-authority"),
			FaultOperationSetDigest:   qualificationreceipt.GoldenFaultOperationSetDigestV1,
			FixtureDocumentArtifactID: "golden-fixture-document", FixtureDocumentDigest: testDigest("golden-fixture"),
			FixtureID: uuid.NewString(),
		},
		Outputs: qualificationevidence.OutputExpectation{
			KMSAttestationArtifactID: "kms-encryption-attestation", ArtifactIndexID: "qualification-artifact-index",
			ReceiptID: "qualification-receipt", SnapshotID: "qualification-snapshot",
		},
		OutputPolicy: OutputPolicy{
			CredentialRevocation: CredentialRevocationPolicyV1, PlaintextDisposition: PlaintextDispositionPolicyV1,
			SnapshotMode: qualificationevidence.ImmutableSnapshotMode,
		},
		PromotionTarget: qualificationreceipt.PromotionTarget{
			ProjectID: uuid.NewString(), WorkflowRunID: uuid.NewString(), NodeKey: "external-qualification",
			TargetRevision: qualificationreceipt.PromotionTargetRevision{ID: uuid.NewString(), ContentHash: testDigest("target-revision")},
			Subject:        subject, StageGate: qualificationreceipt.ExternalQualificationGate,
		},
		QualificationManifest: ArtifactRevisionBinding{
			ArtifactID: "qualification-manifest", RevisionID: uuid.NewString(), ContentHash: testDigest("qualification-manifest"),
		},
		QualificationPlanDigest: sha256Digest(projection),
		Recipient:               qualificationevidence.EncryptionRecipient{KeyResourceID: "qualification-kms-key", KeyVersion: "version-1"},
		SchemaVersion:           InputSchemaV1,
		Source: qualificationreceipt.SourceBinding{
			Commit: "0123456789abcdef0123456789abcdef01234567", TreeDigestSchema: qualificationreceipt.SourceContentTreeCommitmentSchemaV1,
			TreeDigest: testDigest("source-tree"), Dirty: false,
		},
		TemplateRelease: qualificationreceipt.TemplateReleaseBinding{
			ID: uuid.NewString(), ContentHash: testDigest("template-release"), ApprovalReceiptDigest: testDigest("template-approval"),
		},
		TrustBindings: qualificationevidence.TrustBindings{
			CaptureAuthorityID: "capture-authority", CredentialAuthorityID: "credential-authority",
			EncryptionAuthorityID: "encryption-authority", IndexerAuthorityID: "indexer-authority",
			KMSAuthorityID: "kms-authority", ReceiptAuthorityID: "receipt-authority",
			SealerAuthorityID: "sealer-authority", VerifierAuthorityID: "verifier-authority",
		},
		TrustPolicyDigest: testDigest("trust-policy"),
	}
	resolved := ResolvedInputs{Input: input, QualificationPlanBytes: projection}
	refreshResolvedInput(t, &resolved)
	return resolved
}

func refreshResolvedInput(t *testing.T, resolved *ResolvedInputs) {
	t.Helper()
	encoded, err := canonicalJSON(resolved.Input)
	if err != nil {
		t.Fatal(err)
	}
	resolved.InputBytes = encoded
	resolved.InputHash = sha256Digest(encoded)
}

func cloneResolvedInputs(value ResolvedInputs) ResolvedInputs {
	cloned := value
	cloned.Input = cloneInput(value.Input)
	cloned.InputBytes = append([]byte(nil), value.InputBytes...)
	cloned.QualificationPlanBytes = append([]byte(nil), value.QualificationPlanBytes...)
	return cloned
}

func testDigest(seed string) string { return sha256Digest([]byte(seed)) }
