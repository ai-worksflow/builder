package qualificationpromotionv2

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationinputauthority"
)

func TestInputPrecommitProjectionExactlyMatchesPromotionBindingContract(t *testing.T) {
	projection := reflect.TypeOf(InputPrecommitProjection{})
	binding := reflect.TypeOf(qualificationinputauthority.PromotionBinding{})
	if projection.NumField() != binding.NumField() {
		t.Fatalf("InputPrecommitProjection fields = %d, PromotionBinding fields = %d", projection.NumField(), binding.NumField())
	}
	for index := 0; index < binding.NumField(); index++ {
		want := binding.Field(index)
		got, found := projection.FieldByName(want.Name)
		if !found || got.Type != want.Type || got.Tag.Get("json") != want.Tag.Get("json") {
			t.Fatalf("field %s parity = found:%v type:%v tag:%q; want type:%v tag:%q", want.Name, found, got.Type, got.Tag.Get("json"), want.Type, want.Tag.Get("json"))
		}
	}
}

func TestValidateCommandRequiresFivePairwiseUUIDv4Identities(t *testing.T) {
	valid := testCommand()
	cases := map[string]func(*ConsumeCommand){
		"nil": func(command *ConsumeCommand) { command.OperationID = uuid.Nil },
		"non-v4": func(command *ConsumeCommand) {
			command.HandoffID = uuid.MustParse("10000000-0000-1000-8000-000000000004")
		},
		"non-RFC4122 variant": func(command *ConsumeCommand) {
			command.HandoffID = uuid.MustParse("10000000-0000-4000-c000-000000000004")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := ValidateCommand(candidate); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateCommand() error = %v", err)
			}
		})
	}
	identities := []uuid.UUID{valid.OperationID, valid.WorkflowInputAuthorityID, valid.PlanAuthorityID, valid.HandoffID, valid.OutputRevisionID}
	for left := range identities {
		for right := left + 1; right < len(identities); right++ {
			candidateIDs := append([]uuid.UUID(nil), identities...)
			candidateIDs[right] = candidateIDs[left]
			candidate := ConsumeCommand{
				OperationID: candidateIDs[0], WorkflowInputAuthorityID: candidateIDs[1], PlanAuthorityID: candidateIDs[2],
				HandoffID: candidateIDs[3], OutputRevisionID: candidateIDs[4],
			}
			if err := ValidateCommand(candidate); !errors.Is(err, ErrInvalid) {
				t.Errorf("ValidateCommand() alias pair %d/%d error = %v", left, right, err)
			}
		}
	}
}

func TestCanonicalDocumentsRepeatPairwiseIdentityChecks(t *testing.T) {
	record := compileTestRecord(t)
	request := record.Request
	request.HandoffID = request.OperationID
	if _, _, err := EncodeRequest(request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("aliased request error = %v", err)
	}
	handoff := record.Handoff
	handoff.OutputRevisionID = handoff.PlanAuthorityID
	if _, _, err := EncodeHandoff(handoff); !errors.Is(err, ErrInvalid) {
		t.Fatalf("aliased handoff error = %v", err)
	}
	intent := record.RevisionIntent
	intent.OutputRevisionID = intent.WorkflowInput.AuthorityID
	if _, _, err := EncodeRevisionIntent(intent); !errors.Is(err, ErrInvalid) {
		t.Fatalf("aliased revision intent error = %v", err)
	}
}

func TestCompileRejectsEveryPreparedCrossBindingDrift(t *testing.T) {
	cases := map[string]func(*PreparedAuthority){
		"workflow command": func(value *PreparedAuthority) {
			value.WorkflowInput.AuthorityID = "40000000-0000-4000-8000-000000000001"
		},
		"plan command":         func(value *PreparedAuthority) { value.Plan.AuthorityID = "40000000-0000-4000-8000-000000000002" },
		"plan input authority": func(value *PreparedAuthority) { value.Plan.InputAuthorityID = "40000000-0000-4000-8000-000000000003" },
		"plan input hash":      func(value *PreparedAuthority) { value.Plan.InputHash = testDigest("other-input") },
		"plan target hash":     func(value *PreparedAuthority) { value.Plan.TargetHash = testDigest("other-target") },
		"plan target":          func(value *PreparedAuthority) { value.PlanTarget.Subject = "other-workspace" },
		"receipt target":       func(value *PreparedAuthority) { value.ReceiptTarget.Subject = "other-workspace" },
		"input precommit kind": func(value *PreparedAuthority) { value.InputPrecommit.Kind = "opaque-authority" },
		"input precommit authority hash": func(value *PreparedAuthority) {
			value.InputPrecommit.AuthorityHash = ""
		},
		"input precommit workflow id": func(value *PreparedAuthority) {
			value.InputPrecommit.WorkflowInputAuthorityID = "40000000-0000-4000-8000-00000000000f"
		},
		"input precommit workflow hash": func(value *PreparedAuthority) {
			value.InputPrecommit.WorkflowInputAuthorityHash = testDigest("other-precommit-workflow")
		},
		"input precommit policy id": func(value *PreparedAuthority) {
			value.InputPrecommit.QualificationPolicyAuthorityID = "40000000-0000-4000-8000-000000000010"
		},
		"input precommit policy hash": func(value *PreparedAuthority) {
			value.InputPrecommit.QualificationPolicyAuthorityHash = testDigest("other-precommit-policy")
		},
		"input precommit plan id": func(value *PreparedAuthority) {
			value.InputPrecommit.QualificationPlanAuthorityID = "40000000-0000-4000-8000-000000000011"
		},
		"input precommit plan hash": func(value *PreparedAuthority) {
			value.InputPrecommit.QualificationPlanAuthorityHash = testDigest("other-precommit-plan")
		},
		"input precommit admission alias": func(value *PreparedAuthority) {
			value.InputPrecommit.CredentialAdmissionHash = value.InputPrecommit.SourceAdmissionHash
		},
		"locked policy id": func(value *PreparedAuthority) {
			value.PolicyAuthority.AuthorityID = "40000000-0000-4000-8000-00000000000d"
		},
		"locked policy hash": func(value *PreparedAuthority) {
			value.PolicyAuthority.AuthorityHash = testDigest("other-locked-policy")
		},
		"profile policy id": func(value *PreparedAuthority) {
			value.PolicyPlanInputs.PolicyAuthority.AuthorityID = "40000000-0000-4000-8000-00000000000e"
		},
		"profile policy hash": func(value *PreparedAuthority) {
			value.PolicyPlanInputs.PolicyAuthority.AuthorityHash = testDigest("other-profile-policy")
		},
		"revision artifact": func(value *PreparedAuthority) {
			value.TargetRevisionArtifactID = "40000000-0000-4000-8000-000000000008"
		},
		"event orchestration": func(value *PreparedAuthority) {
			value.EvidenceEventSet.OrchestrationID = "40000000-0000-4000-8000-000000000004"
		},
		"event head":    func(value *PreparedAuthority) { value.Evidence.HeadVersion = 2 },
		"event last id": func(value *PreparedAuthority) { value.Evidence.LastEventID = value.EvidenceEventSet.Events[0].EventID },
		"event last hash": func(value *PreparedAuthority) {
			value.Evidence.LastEventHash = value.EvidenceEventSet.Events[0].EventHash
		},
		"event set digest": func(value *PreparedAuthority) { value.Evidence.EventSetDigest = testDigest("other-event-set") },
		"evidence command": func(value *PreparedAuthority) { value.Evidence.CommandHash = testDigest("other-evidence-plan") },
		"evidence trust bindings": func(value *PreparedAuthority) {
			value.Evidence.TrustBindingsDigest = testDigest("other-evidence-trust")
		},
		"terminal event kind":  func(value *PreparedAuthority) { value.EvidenceTerminalEvent.EventKind = "run-closed" },
		"terminal event stage": func(value *PreparedAuthority) { value.EvidenceTerminalEvent.Stage = "pending" },
		"terminal event id": func(value *PreparedAuthority) {
			value.EvidenceTerminalEvent.EventID = value.EvidenceEventSet.Events[0].EventID
		},
		"terminal event hash": func(value *PreparedAuthority) {
			value.EvidenceTerminalEvent.EventHash = value.EvidenceEventSet.Events[0].EventHash
		},
		"terminal evidence closure": func(value *PreparedAuthority) {
			value.EvidenceTerminalEvent.EvidenceClosureDigest = testDigest("other-evidence-closure")
		},
		"terminal artifact index": func(value *PreparedAuthority) {
			value.EvidenceTerminalEvent.ArtifactIndexDigest = testDigest("other-artifact-index")
		},
		"plan trust bindings": func(value *PreparedAuthority) {
			value.PlanControls.TrustBindingsDigest = testDigest("other-plan-trust-bindings")
		},
		"plan trust policy": func(value *PreparedAuthority) {
			value.PlanControls.TrustPolicyDigest = testDigest("other-plan-trust-policy")
		},
		"build manifest id": func(value *PreparedAuthority) {
			value.WorkflowPlanBuild.PlanBuildManifest.ID = "40000000-0000-4000-8000-000000000005"
		},
		"build manifest content": func(value *PreparedAuthority) {
			value.WorkflowPlanBuild.PlanBuildManifest.ContentHash = testDigest("other-build-manifest")
		},
		"build contract id": func(value *PreparedAuthority) {
			value.WorkflowPlanBuild.PlanBuildContract.ID = "40000000-0000-4000-8000-000000000006"
		},
		"build contract content": func(value *PreparedAuthority) {
			value.WorkflowPlanBuild.PlanBuildContract.ContentHash = testDigest("other-build-contract")
		},
		"source commit": func(value *PreparedAuthority) {
			value.SourceBindings.ReceiptSource.Commit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		"source tree": func(value *PreparedAuthority) {
			value.SourceBindings.ReceiptSource.TreeDigest = testDigest("other-source-tree")
		},
		"source dirty": func(value *PreparedAuthority) {
			value.SourceBindings.ReceiptSource.Dirty = true
		},
		"source schema": func(value *PreparedAuthority) {
			value.SourceBindings.ReceiptSource.TreeDigestSchema = "worksflow-source-content-tree/v2"
		},
		"receipt row plan": func(value *PreparedAuthority) {
			value.ReceiptControls.PlanAuthorityHash = testDigest("other-receipt-plan")
		},
		"receipt row plan id": func(value *PreparedAuthority) {
			value.ReceiptControls.PlanAuthorityID = "40000000-0000-4000-8000-00000000000b"
		},
		"receipt row orchestration": func(value *PreparedAuthority) {
			value.ReceiptControls.OrchestrationID = "40000000-0000-4000-8000-000000000007"
		},
		"receipt row closure": func(value *PreparedAuthority) {
			value.ReceiptControls.EvidenceClosureDigest = testDigest("other-receipt-closure")
		},
		"receipt row index": func(value *PreparedAuthority) {
			value.ReceiptControls.ArtifactIndexDigest = testDigest("other-receipt-index")
		},
		"receipt completion time": func(value *PreparedAuthority) {
			value.ReceiptControls.CompletedAt = value.ReceiptControls.Observations.ApproverSign.RecordedAt
		},
		"snapshot request kind": func(value *PreparedAuthority) {
			value.ReceiptControls.Requests.SnapshotSeal.Kind = ReceiptRequestSnapshotVerify
		},
		"verification request role": func(value *PreparedAuthority) {
			value.ReceiptControls.Requests.SnapshotVerify.Role = ReceiptRoleSealer
		},
		"runner request hash": func(value *PreparedAuthority) {
			value.ReceiptControls.Requests.RunnerSign.RequestHash = testDigest("other-runner-request")
		},
		"approver request plan": func(value *PreparedAuthority) {
			value.ReceiptControls.Requests.ApproverSign.PlanAuthorityHash = testDigest("other-plan")
		},
		"request evidence event": func(value *PreparedAuthority) {
			value.ReceiptControls.Requests.SnapshotSeal.EvidenceLastEventHash = testDigest("other-last-event")
		},
		"request evidence closure": func(value *PreparedAuthority) {
			value.ReceiptControls.Requests.SnapshotSeal.EvidenceClosureDigest = testDigest("other-evidence-closure")
		},
		"request trust policy": func(value *PreparedAuthority) {
			value.ReceiptControls.Requests.SnapshotSeal.TrustPolicyDigest = testDigest("other-trust-policy")
		},
		"observation request": func(value *PreparedAuthority) {
			value.ReceiptControls.Observations.RunnerSign.RequestHash = testDigest("other-runner-request")
		},
		"observation hash": func(value *PreparedAuthority) {
			value.ReceiptControls.Observations.ApproverSign.ObservationHash = testDigest("other-approver-observation")
		},
		"observation not latest": func(value *PreparedAuthority) {
			value.ReceiptControls.Observations.SnapshotSeal.LatestSequence++
		},
		"observation not committed": func(value *PreparedAuthority) {
			value.ReceiptControls.Observations.SnapshotVerify.Status = "pending"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			prepared := testPrepared()
			mutate(&prepared)
			if _, err := Compile(testCommand(), prepared, testTime()); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Compile() error = %v", err)
			}
		})
	}
	policyPairs := map[string]func(*PolicyPlanInputBindings) *ExactDocumentDigestBinding{
		"artifact policy":  func(value *PolicyPlanInputBindings) *ExactDocumentDigestBinding { return &value.ArtifactPolicy },
		"artifacts":        func(value *PolicyPlanInputBindings) *ExactDocumentDigestBinding { return &value.Artifacts },
		"golden runtime":   func(value *PolicyPlanInputBindings) *ExactDocumentDigestBinding { return &value.GoldenRuntime },
		"output policy":    func(value *PolicyPlanInputBindings) *ExactDocumentDigestBinding { return &value.OutputPolicy },
		"outputs":          func(value *PolicyPlanInputBindings) *ExactDocumentDigestBinding { return &value.Outputs },
		"recipient":        func(value *PolicyPlanInputBindings) *ExactDocumentDigestBinding { return &value.Recipient },
		"template release": func(value *PolicyPlanInputBindings) *ExactDocumentDigestBinding { return &value.TemplateRelease },
		"trust bindings":   func(value *PolicyPlanInputBindings) *ExactDocumentDigestBinding { return &value.TrustBindings },
		"trust policy":     func(value *PolicyPlanInputBindings) *ExactDocumentDigestBinding { return &value.TrustPolicy },
	}
	for name, selectPair := range policyPairs {
		t.Run("policy plan "+name, func(t *testing.T) {
			prepared := testPrepared()
			selectPair(&prepared.PolicyPlanInputs).PlanDigest = testDigest("other-" + name)
			if _, err := Compile(testCommand(), prepared, testTime()); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Compile() error = %v", err)
			}
		})
	}
	manifestCases := map[string]func(*QualificationManifestPlanBindings){
		"artifact": func(value *QualificationManifestPlanBindings) { value.PlanArtifactID = "other-manifest" },
		"content": func(value *QualificationManifestPlanBindings) {
			value.PlanContentHash = testDigest("other-manifest-content")
		},
		"revision": func(value *QualificationManifestPlanBindings) {
			value.PlanRevisionID = "40000000-0000-4000-8000-00000000000c"
		},
		"plan digest": func(value *QualificationManifestPlanBindings) {
			value.PlanQualificationPlanDigest = testDigest("other-qualification-plan")
		},
	}
	for name, mutate := range manifestCases {
		t.Run("qualification manifest "+name, func(t *testing.T) {
			prepared := testPrepared()
			mutate(&prepared.PolicyPlanInputs.QualificationManifest)
			if _, err := Compile(testCommand(), prepared, testTime()); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Compile() error = %v", err)
			}
		})
	}
	credentialCases := map[string]func(*CredentialProfilePlanBindings){
		"audience":   func(value *CredentialProfilePlanBindings) { value.PlanAudience = "urn:other" },
		"issuer":     func(value *CredentialProfilePlanBindings) { value.PlanIssuer = "spiffe://qualification.example/other" },
		"issuance":   func(value *CredentialProfilePlanBindings) { value.PlanIssuanceArtifactID = "other-issuance" },
		"revocation": func(value *CredentialProfilePlanBindings) { value.PlanRevocationArtifactID = "other-revocation" },
		"member request digest": func(value *CredentialProfilePlanBindings) {
			value.PolicyMemberRequestSetDigest = "invalid"
		},
	}
	for name, mutate := range credentialCases {
		t.Run("credential "+name, func(t *testing.T) {
			prepared := testPrepared()
			mutate(&prepared.PolicyPlanInputs.CredentialProfile)
			if _, err := Compile(testCommand(), prepared, testTime()); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Compile() error = %v", err)
			}
		})
	}
	requestBindingCases := map[string]func(*ReceiptRequestBindings){
		"artifact index": func(value *ReceiptRequestBindings) { value.ArtifactIndexDigest = testDigest("request-other-index") },
		"evidence closure": func(value *ReceiptRequestBindings) {
			value.EvidenceClosureDigest = testDigest("request-other-closure")
		},
		"evidence command": func(value *ReceiptRequestBindings) {
			value.EvidenceCommandDigest = testDigest("request-other-command")
		},
		"evidence head": func(value *ReceiptRequestBindings) { value.EvidenceHeadVersion-- },
		"evidence last hash": func(value *ReceiptRequestBindings) {
			value.EvidenceLastEventHash = testDigest("request-other-last-event")
		},
		"evidence last id": func(value *ReceiptRequestBindings) {
			value.EvidenceLastEventID = "40000000-0000-4000-8000-000000000008"
		},
		"evidence plan": func(value *ReceiptRequestBindings) { value.EvidencePlanHash = testDigest("request-other-plan") },
		"evidence trust": func(value *ReceiptRequestBindings) {
			value.EvidenceTrustDigest = testDigest("request-other-evidence-trust")
		},
		"input": func(value *ReceiptRequestBindings) { value.InputHash = testDigest("request-other-input") },
		"orchestration": func(value *ReceiptRequestBindings) {
			value.OrchestrationID = "40000000-0000-4000-8000-000000000009"
		},
		"plan authority hash": func(value *ReceiptRequestBindings) {
			value.PlanAuthorityHash = testDigest("request-other-plan-authority")
		},
		"plan authority id": func(value *ReceiptRequestBindings) {
			value.PlanAuthorityID = "40000000-0000-4000-8000-00000000000a"
		},
		"projection": func(value *ReceiptRequestBindings) { value.ProjectionHash = testDigest("request-other-projection") },
		"target":     func(value *ReceiptRequestBindings) { value.TargetHash = testDigest("request-other-target") },
		"trust bindings": func(value *ReceiptRequestBindings) {
			value.TrustBindingsDigest = testDigest("request-other-trust-bindings")
		},
		"trust hash":   func(value *ReceiptRequestBindings) { value.TrustHash = testDigest("request-other-trust") },
		"trust policy": func(value *ReceiptRequestBindings) { value.TrustPolicyDigest = testDigest("request-other-policy") },
	}
	for name, mutate := range requestBindingCases {
		t.Run("receipt request "+name, func(t *testing.T) {
			prepared := testPrepared()
			mutate(&prepared.ReceiptControls.Requests.SnapshotSeal)
			if _, err := Compile(testCommand(), prepared, testTime()); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Compile() error = %v", err)
			}
		})
	}
}

func TestCompileRejectsCommandAliasOfResolvedInputPrecommit(t *testing.T) {
	prepared := testPrepared()
	command := testCommand()
	command.HandoffID = uuid.MustParse(prepared.InputPrecommit.AuthorityID)
	if _, err := Compile(command, prepared, testTime()); !errors.Is(err, ErrConflict) {
		t.Fatalf("Compile() alias error = %v", err)
	}
}

func TestCompileRejectsEveryPlanReceiptLineageEdgeMutation(t *testing.T) {
	cases := map[string]func(*PreparedAuthority){
		"authority artifact": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Authority.Receipt.ArtifactID = "qualification-plan-40000000-0000-4000-8000-000000000001"
		},
		"authority hash": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Authority.Receipt.AuthorityHash = testDigest("receipt-other-authority")
		},
		"authority id": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Authority.Receipt.AuthorityID = "40000000-0000-4000-8000-000000000001"
		},
		"authority evidence plan": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Authority.Receipt.EvidencePlanHash = testDigest("receipt-other-evidence-plan")
		},
		"authority freeze operation": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Authority.Receipt.FreezeOperationID = "40000000-0000-4000-8000-000000000002"
		},
		"authority input id": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Authority.Receipt.InputAuthorityID = "40000000-0000-4000-8000-000000000003"
		},
		"authority input hash": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Authority.Receipt.InputHash = testDigest("receipt-other-input")
		},
		"authority plan digest": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Authority.Receipt.PlanDigest = testDigest("receipt-other-plan")
		},
		"authority projection": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Authority.Receipt.ProjectionHash = testDigest("receipt-other-projection")
		},
		"authority target": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Authority.Receipt.TargetHash = testDigest("receipt-other-target")
		},
		"authority trust bindings": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Authority.Receipt.TrustBindingsDigest = testDigest("receipt-other-trust-bindings")
		},
		"authority trust": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Authority.Receipt.TrustHash = testDigest("receipt-other-trust")
		},
		"evidence plan root": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.EvidencePlan.Receipt.Operations.Reserve = "40000000-0000-4000-8000-000000000004"
		},
		"evidence plan credential": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.EvidencePlan.Receipt.CredentialSet.SetHandleHash = testDigest("receipt-other-set-handle")
		},
		"evidence plan artifact": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.EvidencePlan.Receipt.Artifacts[0].ID = "golden-authority-document-other"
		},
		"evidence plan recipient": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.EvidencePlan.Receipt.Recipient.KeyVersion = "other-kms-version"
		},
		"evidence plan output": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.EvidencePlan.Receipt.Outputs.SnapshotID = "other-snapshot"
		},
		"target schema": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Target.Receipt.SchemaVersion = "worksflow-qualification-plan-target/v2"
		},
		"target project": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Target.Receipt.PromotionTarget.ProjectID = "40000000-0000-4000-8000-000000000005"
		},
		"target workflow": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Target.Receipt.PromotionTarget.WorkflowRunID = "40000000-0000-4000-8000-000000000006"
		},
		"target node": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Target.Receipt.PromotionTarget.NodeKey = "other-gate"
		},
		"target subject": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Target.Receipt.PromotionTarget.Subject = "other-subject"
		},
		"target stage": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Target.Receipt.PromotionTarget.StageGate = "other-gate"
		},
		"target revision id": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Target.Receipt.PromotionTarget.TargetRevision.ID = "40000000-0000-4000-8000-000000000007"
		},
		"target revision content": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Target.Receipt.PromotionTarget.TargetRevision.ContentHash = testDigest("receipt-other-revision")
		},
		"trust schema": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Trust.Receipt.SchemaVersion = "worksflow-qualification-plan-trust/v2"
		},
		"trust policy": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Trust.Receipt.TrustPolicyDigest = testDigest("receipt-other-trust-policy")
		},
		"trust capture authority": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Trust.Receipt.TrustBindings.CaptureAuthorityID = "other-capture-authority"
		},
		"build contract id": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Build.Receipt.Contract.ID = "40000000-0000-4000-8000-000000000008"
		},
		"build contract content": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Build.Receipt.Contract.ContentHash = testDigest("receipt-other-contract")
		},
		"build manifest id": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Build.Receipt.Manifest.ID = "40000000-0000-4000-8000-000000000009"
		},
		"build manifest content": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.Build.Receipt.Manifest.ContentHash = testDigest("receipt-other-manifest")
		},
		"template id": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.TemplateRelease.Receipt.ID = "40000000-0000-4000-8000-00000000000a"
		},
		"template content": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.TemplateRelease.Receipt.ContentHash = testDigest("receipt-other-template")
		},
		"template approval": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.TemplateRelease.Receipt.ApprovalReceiptDigest = testDigest("receipt-other-template-approval")
		},
		"golden authority artifact": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.GoldenRuntime.Receipt.AuthorityDocumentArtifactID = "other-golden-authority"
		},
		"golden authority digest": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.GoldenRuntime.Receipt.AuthorityDocumentDigest = testDigest("receipt-other-golden-authority")
		},
		"golden fault set": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.GoldenRuntime.Receipt.FaultOperationSetDigest = testDigest("receipt-other-golden-faults")
		},
		"golden fixture artifact": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.GoldenRuntime.Receipt.FixtureDocumentArtifactID = "other-golden-fixture"
		},
		"golden fixture digest": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.GoldenRuntime.Receipt.FixtureDocumentDigest = testDigest("receipt-other-golden-fixture")
		},
		"golden fixture id": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.GoldenRuntime.Receipt.FixtureID = "40000000-0000-4000-8000-00000000000b"
		},
		"qualification manifest artifact": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.QualificationManifest.Receipt.ArtifactID = "other-qualification-manifest"
		},
		"qualification manifest revision": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.QualificationManifest.Receipt.RevisionID = "40000000-0000-4000-8000-00000000000c"
		},
		"qualification manifest content": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.QualificationManifest.Receipt.ContentHash = testDigest("receipt-other-qualification-manifest")
		},
		"credential set id": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.CredentialSet.Receipt.SetID = "40000000-0000-4000-8000-00000000000d"
		},
		"credential issuer": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.CredentialSet.Receipt.Issuer = "other-credential-authority"
		},
		"credential audience": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.CredentialSet.Receipt.Audience = "urn:worksflow:other"
		},
		"credential set handle": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.CredentialSet.Receipt.SetHandleHash = testDigest("receipt-other-credential-handle")
		},
		"credential member bindings": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.CredentialSet.Receipt.MemberBindingsDigest = testDigest("receipt-other-credential-members")
		},
		"credential member count": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.CredentialSet.Receipt.MemberCount++
		},
		"credential issuance artifact": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.CredentialSet.Receipt.Issuance.ArtifactID = "other-credential-issuance"
		},
		"credential revocation artifact": func(value *PreparedAuthority) {
			value.PlanReceiptLineage.CredentialSet.Receipt.Revocation.ArtifactID = "other-credential-revocation"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			prepared := testPrepared()
			mutate(&prepared)
			if _, err := Compile(testCommand(), prepared, testTime()); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Compile() error = %v", err)
			}
		})
	}
}

func TestCompileFailureClassesCurrentnessTerminalAndIndependentPolicy(t *testing.T) {
	for name, mutate := range map[string]func(*PreparedAuthority){
		"policy stale":   func(value *PreparedAuthority) { value.PolicyCurrent = false },
		"workflow stale": func(value *PreparedAuthority) { value.WorkflowInputCurrent = false },
		"target stale":   func(value *PreparedAuthority) { value.TargetCurrent = false },
	} {
		t.Run(name, func(t *testing.T) {
			prepared := testPrepared()
			mutate(&prepared)
			if _, err := Compile(testCommand(), prepared, testTime()); !errors.Is(err, ErrStale) {
				t.Fatalf("Compile() error = %v", err)
			}
		})
	}
	prepared := testPrepared()
	prepared.ReceiptControls = TerminalReceiptControls{}
	if _, err := Compile(testCommand(), prepared, testTime()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("missing terminal Receipt controls error = %v", err)
	}
	prepared = testPrepared()
	prepared.IndependentRequirements = nil
	if _, err := Compile(testCommand(), prepared, testTime()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("missing independent list error = %v", err)
	}
	prepared = testPrepared()
	prepared.IndependentRequirements = []IndependentAuthorityRequirement{{
		Kind: "malformed-before-lookup", AuthorityID: "postgres://admin:password@db/app", AuthorityHash: "not-a-hash",
	}}
	if _, err := Compile(testCommand(), prepared, testTime()); !errors.Is(err, ErrNotReady) || errors.Is(err, ErrInvalid) {
		t.Fatalf("non-empty independent list must fail closed before lookup/normalization, error = %v", err)
	}
}

func TestTargetSubjectRejectsControlAndInvalidUTF8(t *testing.T) {
	for name, subject := range map[string]string{
		"newline":         "workspace\nsubject",
		"tab":             "workspace\tsubject",
		"delete":          "workspace\x7fsubject",
		"unicode control": "workspace\u0085subject",
		"invalid utf8":    string([]byte{'w', 0xff}),
	} {
		t.Run(name, func(t *testing.T) {
			prepared := testPrepared()
			prepared.Target.Subject = subject
			prepared.PlanTarget.Subject = subject
			prepared.ReceiptTarget.Subject = subject
			if _, err := Compile(testCommand(), prepared, testTime()); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Compile() error = %v", err)
			}
		})
	}
}

func TestTargetNormalizationEquatesFlatWorkflowAndNestedPlanReceiptShapes(t *testing.T) {
	want := testTarget()
	workflow, err := NormalizeWorkflowInputTarget(WorkflowInputTargetSource{
		ManifestSubject: want.Subject, NodeKey: want.NodeKey, ProjectID: want.ProjectID, StageGate: want.StageGate,
		TargetRevisionContentHash: want.TargetRevisionContentHash, TargetRevisionID: want.TargetRevisionID,
		WorkflowRunID: want.WorkflowRunID,
	}, want.NodeRunID, want.TargetArtifactID)
	if err != nil {
		t.Fatalf("NormalizeWorkflowInputTarget() error = %v", err)
	}
	planReceipt, err := NormalizePlanReceiptTarget(PlanReceiptTargetSource{
		NodeKey: want.NodeKey, ProjectID: want.ProjectID, StageGate: want.StageGate, Subject: want.Subject,
		TargetRevision: PlanReceiptTargetRevisionSource{ContentHash: want.TargetRevisionContentHash, ID: want.TargetRevisionID},
		WorkflowRunID:  want.WorkflowRunID,
	}, want.NodeRunID, want.TargetArtifactID)
	if err != nil {
		t.Fatalf("NormalizePlanReceiptTarget() error = %v", err)
	}
	if workflow != want || planReceipt != want {
		t.Fatalf("normalized targets = %#v / %#v, want %#v", workflow, planReceipt, want)
	}
	if _, err := NormalizeWorkflowInputTarget(WorkflowInputTargetSource{}, want.NodeRunID, want.TargetArtifactID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid flat target error = %v", err)
	}
}

func TestValidateRecordRejectsEachHashGraphEdgeMutation(t *testing.T) {
	cases := map[string]func(*Record){
		"request command": func(record *Record) {
			record.Request.HandoffID = "50000000-0000-4000-8000-000000000001"
			record.RequestBytes, record.RequestHash, _ = EncodeRequest(record.Request)
		},
		"event set closure": func(record *Record) {
			record.EvidenceEventSet.Events[0].EventHash = testDigest("mutated-event")
			record.EvidenceEventSetBytes, record.EvidenceEventSetHash, _ = EncodeEvidenceEventSet(record.EvidenceEventSet)
		},
		"closure intent": func(record *Record) {
			record.Closure.Target.Subject = "mutated-closure-target"
			record.ClosureBytes, record.ClosureHash, _ = EncodeClosure(record.Closure)
		},
		"intent consumption": func(record *Record) {
			record.RevisionIntent.Target.Subject = "mutated-intent-target"
			record.RevisionIntentBytes, record.RevisionIntentHash, _ = EncodeRevisionIntent(record.RevisionIntent)
		},
		"consumption handoff": func(record *Record) {
			record.Consumption.ConsumedAt = "2026-07-19T12:34:57.789Z"
			record.ConsumptionBytes, record.ConsumptionHash, _ = EncodeConsumption(record.Consumption)
		},
		"handoff target": func(record *Record) {
			record.Handoff.Target.Subject = "mutated-handoff-target"
			record.HandoffBytes, record.HandoffHash, _ = EncodeHandoff(record.Handoff)
		},
		"scalar receipt":       func(record *Record) { record.ReceiptID = "other-receipt" },
		"scalar consumed time": func(record *Record) { record.ConsumedAt = record.ConsumedAt.Add(time.Millisecond) },
		"scalar created time":  func(record *Record) { record.CreatedAt = record.CreatedAt.Add(time.Millisecond) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			record := compileTestRecord(t)
			mutate(&record)
			if err := ValidateRecord(record); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateRecord() error = %v", err)
			}
		})
	}
}

func TestCloneAndImmutableEqualityExcludeOnlyIdempotency(t *testing.T) {
	record := compileTestRecord(t)
	clone := CloneRecord(record)
	clone.Idempotent = true
	if !SameImmutableRecord(record, clone) {
		t.Fatal("idempotency metadata changed immutable equality")
	}
	clone.RequestBytes[0] ^= 1
	clone.EvidenceEventSet.Events[0].EventHash = testDigest("clone-only")
	clone.Closure.IndependentAuthorities = append(clone.Closure.IndependentAuthorities, IndependentAuthorityProjection{})
	if record.RequestBytes[0] == clone.RequestBytes[0] || record.EvidenceEventSet.Events[0].EventHash == clone.EvidenceEventSet.Events[0].EventHash ||
		len(record.Closure.IndependentAuthorities) != 0 {
		t.Fatal("CloneRecord shared mutable storage")
	}
	if SameImmutableRecord(record, clone) {
		t.Fatal("byte mutation did not change immutable equality")
	}
}
