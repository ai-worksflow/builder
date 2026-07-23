package qualificationpromotionv2

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationevidence"
	"github.com/worksflow/builder/backend/internal/qualificationinputauthority"
)

func testUUID(value string) uuid.UUID {
	return uuid.MustParse(value)
}

func testCommand() ConsumeCommand {
	return ConsumeCommand{
		OperationID:              testUUID("10000000-0000-4000-8000-000000000001"),
		WorkflowInputAuthorityID: testUUID("10000000-0000-4000-8000-000000000002"),
		PlanAuthorityID:          testUUID("10000000-0000-4000-8000-000000000003"),
		HandoffID:                testUUID("10000000-0000-4000-8000-000000000004"),
		OutputRevisionID:         testUUID("10000000-0000-4000-8000-000000000005"),
	}
}

func testDigest(label string) string {
	digest := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func testCanonicalDigest(value any) string {
	digest, err := qualificationevidence.CanonicalDigest(value)
	if err != nil {
		panic(err)
	}
	return digest
}

func testTarget() PromotionTargetV2 {
	return PromotionTargetV2{
		TargetArtifactID:          "20000000-0000-4000-8000-000000000001",
		NodeKey:                   ExternalQualificationGate,
		NodeRunID:                 "20000000-0000-4000-8000-000000000002",
		ProjectID:                 "20000000-0000-4000-8000-000000000003",
		TargetRevisionContentHash: testDigest("revision-content"),
		TargetRevisionID:          "20000000-0000-4000-8000-000000000004",
		StageGate:                 ExternalQualificationGate,
		Subject:                   "workspace",
		WorkflowRunID:             "20000000-0000-4000-8000-000000000005",
	}
}

func testPrepared() PreparedAuthority {
	command := testCommand()
	target := testTarget()
	events := []EvidenceEvent{
		{EventHash: testDigest("event-1"), EventID: "30000000-0000-4000-8000-000000000001", Version: 1},
		{EventHash: testDigest("event-2"), EventID: "30000000-0000-4000-8000-000000000002", Version: 2},
		{EventHash: testDigest("event-3"), EventID: "30000000-0000-4000-8000-000000000003", Version: 3},
	}
	eventSet := EvidenceEventSet{
		Events: events, HeadVersion: int64(len(events)), OrchestrationID: "30000000-0000-4000-8000-000000000005",
		SchemaVersion: EvidenceEventSetSchemaV2,
	}
	eventSetBytes, _ := CanonicalJSON(eventSet)
	eventSetHash := DomainHash(EvidenceEventSetHashDomainV2, eventSetBytes)
	policyAuthority := QualificationPolicyAuthorityBinding{
		AuthorityHash: testDigest("policy-authority"), AuthorityID: "30000000-0000-4000-8000-000000000004",
	}
	workflowInput := WorkflowInputProjection{
		AuthorityHash: testDigest("workflow-authority"), AuthorityID: command.WorkflowInputAuthorityID.String(),
		InputHash: testDigest("workflow-input"), QualificationPolicyAuthorityHash: policyAuthority.AuthorityHash,
		QualificationPolicyAuthorityID: policyAuthority.AuthorityID, TargetHash: testDigest("upstream-target"),
	}
	build := BuildLineageBinding{
		Contract: ImmutableContentBinding{ID: "30000000-0000-4000-8000-000000000007", ContentHash: testDigest("build-contract")},
		Manifest: ImmutableContentBinding{ID: "30000000-0000-4000-8000-000000000008", ContentHash: testDigest("build-manifest")},
	}
	templateRelease := TemplateReleaseLineageBinding{
		ApprovalReceiptDigest: testDigest("template-approval"), ContentHash: testDigest("template-content"),
		ID: "30000000-0000-4000-8000-00000000000b",
	}
	goldenRuntime := GoldenRuntimeLineageBinding{
		AuthorityDocumentArtifactID: "golden-authority-document", AuthorityDocumentDigest: testDigest("golden-authority-document"),
		FaultOperationSetDigest: testDigest("golden-fault-operation-set"), FixtureDocumentArtifactID: "golden-fixture-document",
		FixtureDocumentDigest: testDigest("golden-fixture-document"), FixtureID: "30000000-0000-4000-8000-00000000000c",
	}
	qualificationManifest := QualificationManifestLineageBinding{
		ArtifactID: "qualification-manifest", ContentHash: testDigest("qualification-manifest"),
		RevisionID: "30000000-0000-4000-8000-000000000009",
	}
	credentialPlan := qualificationevidence.CredentialExpectation{
		SetID: "30000000-0000-4000-8000-00000000000d", Issuer: "credential-authority",
		Audience: "urn:worksflow:golden-stack", SetHandleHash: testDigest("credential-set-handle"),
		MemberBindingsDigest: testDigest("credential-member-bindings"), MemberCount: 2,
		IssuanceArtifactID: "credential-set-issuance", RevocationArtifactID: "credential-set-revocation",
	}
	targetDocument := PlanReceiptTargetDocument{
		PromotionTarget: PlanReceiptPromotionTarget{
			NodeKey: target.NodeKey, ProjectID: target.ProjectID, StageGate: target.StageGate, Subject: target.Subject,
			TargetRevision: PlanReceiptTargetRevision{ContentHash: target.TargetRevisionContentHash, ID: target.TargetRevisionID},
			WorkflowRunID:  target.WorkflowRunID,
		},
		SchemaVersion: PlanTargetSchemaV1,
	}
	trustDocument := PlanReceiptTrustDocument{
		SchemaVersion: PlanTrustSchemaV1,
		TrustBindings: qualificationevidence.TrustBindings{
			CaptureAuthorityID: "capture-authority", CredentialAuthorityID: credentialPlan.Issuer,
			EncryptionAuthorityID: "encryption-authority", IndexerAuthorityID: "indexer-authority",
			KMSAuthorityID: "kms-authority", ReceiptAuthorityID: "receipt-authority",
			SealerAuthorityID: "sealer-authority", VerifierAuthorityID: "verifier-authority",
		},
		TrustPolicyDigest: testDigest("trust-policy"),
	}
	templateReleaseDigest := testCanonicalDigest(templateRelease)
	trustBindingsDigest := testCanonicalDigest(trustDocument.TrustBindings)
	trustHash := testCanonicalDigest(trustDocument)
	targetHash := testCanonicalDigest(targetDocument)
	projectionHash := testDigest("projection")
	evidencePlan := qualificationevidence.Plan{
		SchemaVersion: qualificationevidence.PlanSchemaV1, OrchestrationID: eventSet.OrchestrationID,
		RunID: "30000000-0000-4000-8000-000000000006", FixtureID: goldenRuntime.FixtureID,
		QualificationPlanArtifactID: QualificationPlanArtifactPrefix + command.PlanAuthorityID.String(),
		PlanDigest:                  projectionHash, SourceTreeDigest: testDigest("source-tree"), TemplateReleaseDigest: templateReleaseDigest,
		Operations: qualificationevidence.OperationIDs{
			Reserve: "50000000-0000-4000-8000-000000000001", CredentialIssue: "50000000-0000-4000-8000-000000000002",
			RunClosure: "50000000-0000-4000-8000-000000000003", KMSAttestation: "50000000-0000-4000-8000-000000000004",
			CredentialRevocation: "50000000-0000-4000-8000-000000000005", ArtifactIndex: "50000000-0000-4000-8000-000000000006",
			ReceiptSign: "50000000-0000-4000-8000-000000000007", SnapshotSeal: "50000000-0000-4000-8000-000000000008",
		},
		CredentialSet: credentialPlan,
		Artifacts: []qualificationevidence.ArtifactExpectation{
			{ID: goldenRuntime.AuthorityDocumentArtifactID, Kind: qualificationevidence.ArtifactKindGolden, Classification: qualificationevidence.ClassificationDistributable},
			{ID: goldenRuntime.FixtureDocumentArtifactID, Kind: qualificationevidence.ArtifactKindGolden, Classification: qualificationevidence.ClassificationDistributable},
			{ID: "qualification-trace", Kind: qualificationevidence.ArtifactKindTrace, Classification: qualificationevidence.ClassificationRestricted, EncryptionOperationID: "50000000-0000-4000-8000-000000000009"},
			{ID: "qualification-video", Kind: qualificationevidence.ArtifactKindVideo, Classification: qualificationevidence.ClassificationRestricted, EncryptionOperationID: "50000000-0000-4000-8000-00000000000a"},
		},
		Recipient: qualificationevidence.EncryptionRecipient{KeyResourceID: "kms-resource", KeyVersion: "kms-version"},
		Outputs: qualificationevidence.OutputExpectation{
			KMSAttestationArtifactID: "kms-attestation", ArtifactIndexID: "artifact-index",
			ReceiptID: "qualification-receipt", SnapshotID: "qualification-snapshot",
		},
	}
	evidencePlanHash := testCanonicalDigest(evidencePlan)
	plan := PlanProjection{
		AuthorityHash: testDigest("plan-authority"), AuthorityID: command.PlanAuthorityID.String(),
		EvidencePlanHash: evidencePlanHash, InputAuthorityID: workflowInput.AuthorityID,
		InputHash: testDigest("plan-input"), OrchestrationID: "30000000-0000-4000-8000-000000000005",
		ProjectionHash: projectionHash, QualificationRunID: evidencePlan.RunID,
		TargetHash: targetHash, TrustHash: trustHash,
	}
	inputPrecommit := InputPrecommitProjection{
		AuthorityHash:                    testDigest("input-precommit-authority"),
		AuthorityID:                      "30000000-0000-4000-8000-00000000000e",
		CredentialAdmissionHash:          testDigest("input-precommit-credential-admission"),
		CredentialReceiptHash:            testDigest("input-precommit-credential-receipt"),
		CredentialRequestHash:            testDigest("input-precommit-credential-request"),
		Kind:                             qualificationinputauthority.PromotionBindingKindV1,
		QualificationPlanAuthorityHash:   plan.AuthorityHash,
		QualificationPlanAuthorityID:     plan.AuthorityID,
		QualificationPolicyAuthorityHash: policyAuthority.AuthorityHash,
		QualificationPolicyAuthorityID:   policyAuthority.AuthorityID,
		SourceAdmissionHash:              testDigest("input-precommit-source-admission"),
		SourceReceiptHash:                testDigest("input-precommit-source-receipt"),
		SourceRequestHash:                testDigest("input-precommit-source-request"),
		WorkflowInputAuthorityHash:       workflowInput.AuthorityHash,
		WorkflowInputAuthorityID:         workflowInput.AuthorityID,
	}
	receipt := ReceiptProjection{
		ApproverObservationHash: testDigest("approver-observation"), ApproverRequestHash: testDigest("approver-request"),
		CompletionHash: testDigest("completion"), EnvelopeHash: testDigest("envelope"), PAEHash: testDigest("pae"),
		PayloadHash: testDigest("payload"), ReceiptID: "qualification-receipt",
		RunnerObservationHash: testDigest("runner-observation"), RunnerRequestHash: testDigest("runner-request"),
		SnapshotObservationHash: testDigest("snapshot-observation"), SnapshotRequestHash: testDigest("snapshot-request"),
		VerificationObservationHash: testDigest("verification-observation"), VerificationRequestHash: testDigest("verification-request"),
	}
	planControls := PlanControlBindings{
		TrustBindingsDigest: trustBindingsDigest, TrustPolicyDigest: trustDocument.TrustPolicyDigest,
	}
	terminal := TerminalEvidenceEvent{
		ArtifactIndexDigest: testDigest("artifact-index"), EvidenceClosureDigest: testDigest("evidence-closure"),
		EventHash: events[len(events)-1].EventHash, EventID: events[len(events)-1].EventID,
		EventKind: EvidenceArtifactIndexed, Stage: EvidenceIndexCommitted,
	}
	documentBinding := func(label string) ExactDocumentDigestBinding {
		digest := testDigest(label)
		return ExactDocumentDigestBinding{PlanDigest: digest, PolicyDigest: digest}
	}
	policyPlanInputs := PolicyPlanInputBindings{
		ArtifactPolicy: documentBinding("artifact-policy"), Artifacts: documentBinding("artifacts"),
		CredentialProfile: CredentialProfilePlanBindings{
			PlanAudience: credentialPlan.Audience, PlanIssuanceArtifactID: credentialPlan.IssuanceArtifactID,
			PlanIssuer: credentialPlan.Issuer, PlanRevocationArtifactID: credentialPlan.RevocationArtifactID,
			PolicyAudience: credentialPlan.Audience, PolicyAuthorityID: credentialPlan.Issuer,
			PolicyIssuanceArtifactID:     credentialPlan.IssuanceArtifactID,
			PolicyMemberRequestSetDigest: testDigest("credential-member-request-set"),
			PolicyRevocationArtifactID:   credentialPlan.RevocationArtifactID,
		},
		GoldenRuntime: ExactDocumentDigestBinding{PlanDigest: testCanonicalDigest(goldenRuntime), PolicyDigest: testCanonicalDigest(goldenRuntime)},
		OutputPolicy:  documentBinding("output-policy"),
		Outputs:       documentBinding("outputs"),
		QualificationManifest: QualificationManifestPlanBindings{
			PlanArtifactID: qualificationManifest.ArtifactID, PlanContentHash: qualificationManifest.ContentHash,
			PlanQualificationPlanDigest: plan.ProjectionHash, PlanRevisionID: qualificationManifest.RevisionID,
			PolicyArtifactID: qualificationManifest.ArtifactID, PolicyContentHash: qualificationManifest.ContentHash,
			PolicyPlanDigest: plan.ProjectionHash, PolicyRevisionID: qualificationManifest.RevisionID,
		},
		Recipient:       documentBinding("recipient"),
		TemplateRelease: ExactDocumentDigestBinding{PlanDigest: templateReleaseDigest, PolicyDigest: templateReleaseDigest},
		TrustBindings:   ExactDocumentDigestBinding{PlanDigest: planControls.TrustBindingsDigest, PolicyDigest: planControls.TrustBindingsDigest},
		TrustPolicy:     ExactDocumentDigestBinding{PlanDigest: planControls.TrustPolicyDigest, PolicyDigest: planControls.TrustPolicyDigest},
		PolicyAuthority: policyAuthority,
	}
	requestBinding := func(hash, kind, role string) ReceiptRequestBindings {
		return ReceiptRequestBindings{
			ArtifactIndexDigest: terminal.ArtifactIndexDigest, EvidenceClosureDigest: terminal.EvidenceClosureDigest,
			EvidenceCommandDigest: plan.EvidencePlanHash, EvidenceHeadVersion: int64(len(events)),
			EvidenceLastEventHash: terminal.EventHash, EvidenceLastEventID: terminal.EventID,
			EvidencePlanHash: plan.EvidencePlanHash, EvidenceTrustDigest: planControls.TrustBindingsDigest,
			InputHash: plan.InputHash, Kind: kind, OrchestrationID: plan.OrchestrationID,
			PlanAuthorityHash: plan.AuthorityHash, PlanAuthorityID: plan.AuthorityID, ProjectionHash: plan.ProjectionHash,
			RequestHash: hash, Role: role, TargetHash: plan.TargetHash, TrustBindingsDigest: planControls.TrustBindingsDigest,
			TrustHash: plan.TrustHash, TrustPolicyDigest: planControls.TrustPolicyDigest,
		}
	}
	snapshotRequest := requestBinding(receipt.SnapshotRequestHash, ReceiptRequestSnapshotSeal, ReceiptRoleSealer)
	verificationRequest := requestBinding(receipt.VerificationRequestHash, ReceiptRequestSnapshotVerify, ReceiptRoleVerifier)
	runnerRequest := requestBinding(receipt.RunnerRequestHash, ReceiptRequestSign, ReceiptRoleQualificationRunner)
	approverRequest := requestBinding(receipt.ApproverRequestHash, ReceiptRequestSign, ReceiptRoleReleaseApprover)
	observation := func(requestHash, observationHash string) ReceiptObservationBindings {
		return ReceiptObservationBindings{
			LatestSequence: 1, ObservationHash: observationHash, RecordedAt: testTime().Add(-time.Minute),
			RequestHash: requestHash, Sequence: 1, Status: ReceiptObservationCommitted,
		}
	}
	planAuthorityLineage := PlanAuthorityLineageBinding{
		ArtifactID: evidencePlan.QualificationPlanArtifactID, AuthorityHash: plan.AuthorityHash,
		AuthorityID: plan.AuthorityID, EvidencePlanHash: plan.EvidencePlanHash,
		FreezeOperationID: "30000000-0000-4000-8000-00000000000a", InputAuthorityID: plan.InputAuthorityID,
		InputHash: plan.InputHash, PlanDigest: plan.ProjectionHash, ProjectionHash: plan.ProjectionHash,
		TargetHash: plan.TargetHash, TrustBindingsDigest: planControls.TrustBindingsDigest, TrustHash: plan.TrustHash,
	}
	return PreparedAuthority{
		Evidence: EvidenceProjection{
			ArtifactIndexDigest: terminal.ArtifactIndexDigest, CommandHash: plan.EvidencePlanHash,
			EventSetDigest: eventSetHash, EvidenceClosureDigest: testDigest("evidence-closure"), HeadVersion: int64(len(events)),
			LastEventHash: events[len(events)-1].EventHash, LastEventID: events[len(events)-1].EventID,
			Phase: EvidenceArtifactIndexed, TrustBindingsDigest: planControls.TrustBindingsDigest,
		},
		EvidenceEventSet: eventSet, EvidenceTerminalEvent: terminal,
		IndependentRequirements: []IndependentAuthorityRequirement{},
		InputPrecommit:          inputPrecommit,
		Plan:                    plan, PlanControls: planControls,
		PlanReceiptLineage: PlanReceiptLineageBindings{
			Authority: PlanReceiptAuthorityBindings{Plan: planAuthorityLineage, Receipt: planAuthorityLineage},
			Build:     PlanReceiptBuildBindings{Plan: build, Receipt: build},
			CredentialSet: PlanReceiptCredentialSetBindings{
				Plan: credentialPlan,
				Receipt: TerminalCredentialSetBinding{
					Audience: credentialPlan.Audience, ExpiresAt: testTime().Add(20 * time.Minute).Format(canonicalTimeLayout),
					Issuance: TerminalCredentialArtifactBinding{
						ArtifactID: credentialPlan.IssuanceArtifactID, ContentDigest: testDigest("credential-issuance-content"),
						PayloadDigest: testDigest("credential-issuance-payload"), SignerSetDigest: testDigest("credential-issuance-signers"),
					},
					IssuedAt: testTime().Add(-5 * time.Minute).Format(canonicalTimeLayout), Issuer: credentialPlan.Issuer,
					MemberBindingsDigest: credentialPlan.MemberBindingsDigest, MemberCount: credentialPlan.MemberCount,
					Revocation: TerminalCredentialArtifactBinding{
						ArtifactID: credentialPlan.RevocationArtifactID, ContentDigest: testDigest("credential-revocation-content"),
						PayloadDigest: testDigest("credential-revocation-payload"), SignerSetDigest: testDigest("credential-revocation-signers"),
					},
					RevokedAt:     testTime().Add(-3 * time.Minute).Format(canonicalTimeLayout),
					SetHandleHash: credentialPlan.SetHandleHash, SetID: credentialPlan.SetID,
				},
			},
			EvidencePlan:          PlanReceiptEvidencePlanBindings{Plan: evidencePlan, Receipt: evidencePlan},
			GoldenRuntime:         PlanReceiptGoldenRuntimeBindings{Plan: goldenRuntime, Receipt: goldenRuntime},
			QualificationManifest: PlanReceiptQualificationManifestBindings{Plan: qualificationManifest, Receipt: qualificationManifest},
			Target:                PlanReceiptTargetBindings{Plan: targetDocument, Receipt: targetDocument},
			TemplateRelease:       PlanReceiptTemplateReleaseBindings{Plan: templateRelease, Receipt: templateRelease},
			Trust:                 PlanReceiptTrustBindings{Plan: trustDocument, Receipt: trustDocument},
		},
		PlanTarget: target, PolicyAuthority: policyAuthority, PolicyPlanInputs: policyPlanInputs, Receipt: receipt,
		ReceiptControls: TerminalReceiptControls{
			ArtifactIndexDigest: terminal.ArtifactIndexDigest, CompletedAt: testTime().Add(-30 * time.Second),
			EvidenceClosureDigest: terminal.EvidenceClosureDigest,
			OrchestrationID:       plan.OrchestrationID, PlanAuthorityHash: plan.AuthorityHash, PlanAuthorityID: plan.AuthorityID,
			Requests: ReceiptRequestSet{
				ApproverSign: approverRequest, RunnerSign: runnerRequest,
				SnapshotSeal: snapshotRequest, SnapshotVerify: verificationRequest,
			},
			Observations: ReceiptObservationSet{
				ApproverSign:   observation(approverRequest.RequestHash, receipt.ApproverObservationHash),
				RunnerSign:     observation(runnerRequest.RequestHash, receipt.RunnerObservationHash),
				SnapshotSeal:   observation(snapshotRequest.RequestHash, receipt.SnapshotObservationHash),
				SnapshotVerify: observation(verificationRequest.RequestHash, receipt.VerificationObservationHash),
			},
		},
		ReceiptTarget: target,
		SourceBindings: PlanReceiptSourceBindings{
			PlanSource: SourceBinding{
				Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Dirty: false,
				TreeDigest: testDigest("source-tree"), TreeDigestSchema: SourceTreeDigestSchemaV1,
			},
			ReceiptSource: SourceBinding{
				Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Dirty: false,
				TreeDigest: testDigest("source-tree"), TreeDigestSchema: SourceTreeDigestSchemaV1,
			},
		},
		Target: target, TargetRevisionArtifactID: target.TargetArtifactID, WorkflowInput: workflowInput,
		WorkflowPlanBuild: WorkflowPlanBuildBindings{
			PlanBuildContract: build.Contract, PlanBuildManifest: build.Manifest,
			WorkflowInputBuildContract: build.Contract, WorkflowInputBuildManifest: build.Manifest,
		},
		PolicyCurrent: true, TargetCurrent: true, WorkflowInputCurrent: true,
	}
}

func compileTestRecord(t *testing.T) Record {
	t.Helper()
	record, err := Compile(testCommand(), testPrepared(), testTime())
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	return record
}

func testTime() time.Time {
	return time.Date(2026, time.July, 19, 12, 34, 56, 789000000, time.UTC)
}

func mustMemoryService(t *testing.T, prepared PreparedAuthority) (*Service, *MemoryStore) {
	t.Helper()
	store, err := NewMemoryStore(testTime)
	if err != nil {
		t.Fatalf("NewMemoryStore() error = %v", err)
	}
	if err := store.InstallAuthority(prepared); err != nil {
		t.Fatalf("InstallAuthority() error = %v", err)
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, store
}
