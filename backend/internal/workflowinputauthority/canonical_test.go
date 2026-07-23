package workflowinputauthority

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/canonicalreviewreceipt"
	"github.com/worksflow/builder/backend/internal/constructor"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

func TestCanonicalJSONUsesPostgresCompatibleUTF8AndIntegerContract(t *testing.T) {
	t.Parallel()
	value := map[string]any{
		"中文":    "中文 <>& \" \\ \b\f\n\r\t\x01 \u2028\u2029",
		"alpha": json.Number("42"),
	}
	encoded, err := CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("{\"alpha\":42,\"中文\":\"中文 <>& \\\" \\\\ \\b\\f\\n\\r\\t\\u0001 \u2028\u2029\"}")
	if !bytes.Equal(encoded, want) {
		t.Fatalf("canonical JSON mismatch\n got: %q\nwant: %q", encoded, want)
	}
	if bytes.Contains(encoded, []byte(`\u003c`)) || bytes.Contains(encoded, []byte(`\u2028`)) {
		t.Fatalf("valid UTF-8 was unnecessarily escaped: %s", encoded)
	}
	for _, number := range []json.Number{"1.0", "1e0", "+1", "-0", json.Number("9007199254740992")} {
		if _, err := CanonicalJSON(map[string]any{"number": number}); err == nil {
			t.Fatalf("non-canonical number %q was accepted", number)
		}
	}
	if _, err := CanonicalJSON(map[string]any{"bad": string([]byte{0xff})}); err == nil {
		t.Fatal("invalid UTF-8 Go string was silently normalized")
	}
	if _, err := CanonicalJSON(map[string]any{string([]byte{0xff}): "bad-key"}); err == nil {
		t.Fatal("invalid UTF-8 Go map key was silently normalized")
	}
}

func TestWorkflowInputAuthorityGoldenVectorsAndStrictRoundTrip(t *testing.T) {
	t.Parallel()
	record := mustCompileGolden(t)

	// These vectors are intentionally independent constants. Any wire change
	// must be treated as a new version/domain, never as a v1 fixture update.
	wants := map[string]string{
		"request":   "sha256:01fbbdde085cd513ac0b2fa5e4a240566a88ecc0a9108f25895f4af864bad679",
		"target":    "sha256:8bd1bd71398a7ddb86d41b6f0c7e74c19776068b365b7e74a9e2dc1f74b7efcf",
		"input":     "sha256:e50e1076ee1902cabf7948e10cd8a22423ba8c45a1df3c6ad6309482de2866f7",
		"authority": "sha256:1b931970149db6d8d27229345503a4a09df8c65c52b402736aca2733b47319ed",
	}
	got := map[string]string{
		"request": record.RequestHash, "target": record.TargetHash,
		"input": record.InputHash, "authority": record.AuthorityHash,
	}
	for name, want := range wants {
		if got[name] != want {
			t.Fatalf("%s golden hash drifted: got %s\nrequest=%s\ntarget=%s\ninput=%s\nauthority=%s", name, got[name], record.RequestBytes, record.TargetBytes, record.InputBytes, record.EnvelopeBytes)
		}
	}

	wantRequest := `{"authorityId":"88888888-8888-4888-8888-888888888888","expectedRunCursor":42,"mediaType":"application/vnd.worksflow.workflow-input-freeze-request+json;version=1","nodeKey":"external-qualification","nodeRunId":"66666666-6666-4666-8666-666666666666","operationId":"77777777-7777-4777-8777-777777777777","projectId":"22222222-2222-4222-8222-222222222222","schemaVersion":"worksflow-workflow-input-freeze-request/v1","workflowRunId":"55555555-5555-4555-8555-555555555555"}`
	if string(record.RequestBytes) != wantRequest {
		t.Fatalf("request wire drifted\n got: %s\nwant: %s", record.RequestBytes, wantRequest)
	}
	wantTarget := `{"manifestSubject":"stable-subject","nodeKey":"external-qualification","projectId":"22222222-2222-4222-8222-222222222222","stageGate":"external-qualification","targetRevisionContentHash":"` + record.Target.TargetRevisionContentHash + `","targetRevisionId":"ffffffff-ffff-4fff-8fff-ffffffffffff","workflowRunId":"55555555-5555-4555-8555-555555555555"}`
	if string(record.TargetBytes) != wantTarget {
		t.Fatalf("target wire drifted\n got: %s\nwant: %s", record.TargetBytes, wantTarget)
	}

	if _, err := DecodeFreezeRequest(record.RequestBytes, record.RequestHash); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTarget(record.TargetBytes, record.TargetHash); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeInput(record.InputBytes, record.InputHash); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeAuthority(record.EnvelopeBytes, record.AuthorityHash); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecord(record); err != nil {
		t.Fatal(err)
	}
}

func TestFrozenSQLWireFieldParityAndPrivateCandidateExactness(t *testing.T) {
	t.Parallel()
	candidate := goldenCandidate(t)
	encoded, err := EncodeFreezeCandidate(candidate.Document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeFreezeCandidate(encoded)
	if err != nil || !reflect.DeepEqual(decoded, candidate.Document) {
		t.Fatalf("candidate round trip = %#v, %v", decoded, err)
	}
	assertJSONKeys(t, candidate.Document, []string{
		"inputManifests", "manifestSubject", "qualificationPolicy", "qualityResult", "reviewRequirements", "revisions",
	})
	assertJSONKeys(t, candidate.Document.InputManifests[0], []string{"manifestId", "rawBytesHex", "role"})
	assertJSONKeys(t, candidate.Document.Revisions[0], []string{
		"canonicalReviewRequired", "currencyPolicy", "purpose", "rawBytesHex", "revisionId",
	})
	assertJSONKeys(t, candidate.Document.ReviewRequirements[0], []string{"purpose", "revisionId"})
	assertJSONKeys(t, candidate.Document.QualityResult, []string{
		"buildContractHash", "buildContractId", "buildManifestHash", "buildManifestId", "passed", "qualityRunId",
		"workspaceRevisionContentHash", "workspaceRevisionId",
	})
	assertJSONKeys(t, candidate.Input.Predecessors[0], []string{
		"artifactRevisions", "bindingRawBytesHash", "deliverySliceRefs", "edgeId", "inputManifest", "mappingHash",
		"mappingKind", "mappingOrdinal", "materializedArtifactRevisions", "outputHash", "outputProposal",
		"outputRevisionNumber", "proposalPins", "sourceDefinitionNodeId", "sourceNodeKey", "sourceNodeRunId",
		"sourceNodeType", "sourcePort", "sourceSliceIdentity", "sourceStatus", "targetPort", "valueHash",
	})
	assertJSONKeys(t, candidate.Input.InputManifests[0], []string{
		"contentHash", "contentRef", "contentStore", "id", "kind", "manifestHash", "projectId", "rawBytesHash",
		"rawBytesSize", "role", "schemaVersion",
	})
	assertJSONKeys(t, candidate.Input.Revisions[0], []string{
		"artifactId", "artifactKind", "byteSize", "canonicalReviewRequired", "changeSourceAtFreeze", "contentHash",
		"contentRef", "contentStore", "currencyPolicy", "implementationProposalId", "isLatestApprovedAtFreeze",
		"isLatestCurrentAtFreeze", "proposalId", "purpose", "rawBytesHash", "revisionId", "schemaVersion",
		"sourceManifestId", "sourceRequiredAtFreeze", "workflowStatusAtFreeze",
	})
	rootSlice, err := CanonicalJSON(SliceIdentity{Kind: SliceKindRoot})
	if err != nil || string(rootSlice) != `{"kind":"root"}` {
		t.Fatalf("root slice wire = %s, %v", rootSlice, err)
	}
	deliverySlice, err := CanonicalJSON(SliceIdentity{ID: "19191919-1919-4919-8919-191919191919", Kind: SliceKindDelivery})
	if err != nil || string(deliverySlice) != `{"id":"19191919-1919-4919-8919-191919191919","kind":"slice"}` {
		t.Fatalf("slice wire = %s, %v", deliverySlice, err)
	}
	widened := bytes.Replace(encoded, []byte(`"manifestSubject":`), []byte(`"future":true,"manifestSubject":`), 1)
	if _, err := DecodeFreezeCandidate(widened); err == nil {
		t.Fatal("widened private candidate was accepted")
	}
}

func TestStrictRequestAndAuthorityDecodeRejectMalformedOrPartialWire(t *testing.T) {
	t.Parallel()
	record := mustCompileGolden(t)
	replace := func(old, replacement string) []byte {
		return bytes.Replace(record.RequestBytes, []byte(old), []byte(replacement), 1)
	}
	tests := map[string][]byte{
		"unknown field":  replace(`"mediaType":`, `"future":true,"mediaType":`),
		"duplicate name": replace(`"mediaType":`, `"mediaType":"x","mediaType":`),
		"trailing token": append(append([]byte(nil), record.RequestBytes...), []byte(` true`)...),
		"BOM":            append([]byte{0xef, 0xbb, 0xbf}, record.RequestBytes...),
		"invalid UTF-8":  append(append([]byte(nil), record.RequestBytes...), 0xff),
		"null integer":   replace(`"expectedRunCursor":42`, `"expectedRunCursor":null`),
		"float":          replace(`"expectedRunCursor":42`, `"expectedRunCursor":42.0`),
		"exponent":       replace(`"expectedRunCursor":42`, `"expectedRunCursor":42e0`),
		"unsafe integer": replace(`"expectedRunCursor":42`, `"expectedRunCursor":9007199254740992`),
		"whitespace":     append([]byte(" "), record.RequestBytes...),
	}
	for name, wire := range tests {
		wire := wire
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := DecodeFreezeRequest(wire, DomainHash(FreezeRequestHashDomainV1, wire)); err == nil {
				t.Fatal("malformed or widened wire was accepted")
			}
		})
	}
	partial := record.Envelope
	partial.InputHash = ""
	partialBytes, err := CanonicalJSON(partial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeAuthority(partialBytes, DomainHash(AuthorityHashDomainV1, partialBytes)); err == nil {
		t.Fatal("partial final authority envelope was accepted")
	}
}

func TestCandidateRejectsRawSemanticLineageReviewAndIdentityDrift(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*Candidate){
		"definition semantic claim": func(candidate *Candidate) {
			candidate.Input.Definition.DefinitionHash = digest("1")
		},
		"node input projection": func(candidate *Candidate) {
			candidate.Input.Predecessors[0].ValueHash = digest("2")
		},
		"node input raw": func(candidate *Candidate) {
			candidate.Materials.NodeInput[1] = 'X'
		},
		"manifest semantic claim": func(candidate *Candidate) {
			for index := range candidate.Input.InputManifests {
				candidate.Input.InputManifests[index].ManifestHash = digest("3")
			}
			candidate.Input.Run.InputManifestHash = digest("3")
			candidate.Input.Predecessors[0].InputManifest.Hash = digest("3")
		},
		"manifest private raw": func(candidate *Candidate) {
			candidate.Document.InputManifests[0].RawBytesHex = hex.EncodeToString([]byte(`{"forged":true}`))
		},
		"revision bytes": func(candidate *Candidate) {
			candidate.Materials.Revisions[0].Bytes[1] = 'X'
		},
		"build manifest semantic claim": func(candidate *Candidate) {
			candidate.Input.Build.BuildManifest.ManifestHash = digest("4")
			candidate.Input.QualityResult.BuildManifestHash = digest("4")
			candidate.Document.QualityResult.BuildManifestHash = digest("4")
		},
		"build contract raw": func(candidate *Candidate) {
			candidate.Materials.BuildContract[1] = 'X'
		},
		"review exact set omitted": func(candidate *Candidate) {
			candidate.Document.ReviewRequirements = []ReviewRequirementCandidate{}
			candidate.Input.ReviewReceipts = []ReviewReceiptBinding{}
			candidate.Materials.ReviewReceipts = []ReviewReceiptMaterial{}
		},
		"receipt revision facts": func(candidate *Candidate) {
			candidate.Input.Revisions[0].ArtifactKind = "decision_record"
		},
		"receipt governance": func(candidate *Candidate) {
			candidate.Input.Project.GovernanceMode = GovernanceTeam
		},
		"receipt bytes": func(candidate *Candidate) {
			candidate.Materials.ReviewReceipts[0].Bytes[1] = 'X'
		},
		"global identity collision": func(candidate *Candidate) {
			candidate.Input.Gate.ActivationEventID = candidate.Request.OperationID
		},
		"quality run reused as workflow node run": func(candidate *Candidate) {
			candidate.Input.QualityResult.QualityRunID = candidate.Input.Predecessors[0].SourceNodeRunID
			candidate.Document.QualityResult.QualityRunID = candidate.Input.QualityResult.QualityRunID
		},
		"non-v4 UUID": func(candidate *Candidate) {
			candidate.Request.AuthorityID = "88888888-8888-1888-8888-888888888888"
		},
		"sub-millisecond timestamp": func(candidate *Candidate) {
			candidate.Input.Run.StartedAt = "2026-07-19T00:00:00.000001Z"
		},
		"null required predecessor array": func(candidate *Candidate) {
			candidate.Input.Predecessors[0].ProposalPins = nil
		},
		"null delivery slice array": func(candidate *Candidate) {
			candidate.Input.Predecessors[0].DeliverySliceRefs = nil
		},
		"absolute content path": func(candidate *Candidate) {
			candidate.Input.Revisions[0].ContentRef = "/home/runtime/source.json"
		},
		"build source required policy": func(candidate *Candidate) {
			candidate.Input.Revisions[0].SourceRequiredAtFreeze = false
		},
		"workspace producer proposal omitted": func(candidate *Candidate) {
			candidate.Input.Revisions[1].ImplementationProposalID = nil
		},
		"producer build manifest not consumed": func(candidate *Candidate) {
			candidate.Input.Build.BuildManifest.StatusAtFreeze = "frozen"
		},
	}
	for name, mutate := range tests {
		mutate := mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := goldenCandidate(t)
			mutate(&candidate)
			if _, err := Compile(candidate); err == nil {
				t.Fatal("semantic/raw/lineage drift was accepted")
			}
		})
	}
}

func TestPolicyDerivedRevisionCurrencyAndReviewSubset(t *testing.T) {
	t.Parallel()

	t.Run("exact approved source may be non-latest when review remains required", func(t *testing.T) {
		t.Parallel()
		candidate := goldenCandidate(t)
		candidate.Input.Revisions[0].CurrencyPolicy = CurrencyExactApproved
		candidate.Input.Revisions[0].IsLatestApprovedAtFreeze = false
		candidate.Input.Revisions[0].IsLatestCurrentAtFreeze = false
		candidate.Document.Revisions[0].CurrencyPolicy = CurrencyExactApproved
		if _, err := Compile(candidate); err != nil {
			t.Fatalf("policy-authorized exact-approved source was rejected: %v", err)
		}
	})

	t.Run("non-required system source does not invent a review receipt", func(t *testing.T) {
		t.Parallel()
		candidate := goldenCandidate(t)
		candidate.Input.Revisions[0].CanonicalReviewRequired = false
		candidate.Input.Revisions[0].ChangeSourceAtFreeze = "system"
		candidate.Input.ReviewReceipts = []ReviewReceiptBinding{}
		candidate.Document.Revisions[0].CanonicalReviewRequired = false
		candidate.Document.ReviewRequirements = []ReviewRequirementCandidate{}
		candidate.Materials.ReviewReceipts = []ReviewReceiptMaterial{}
		if _, err := Compile(candidate); err != nil {
			t.Fatalf("policy-derived no-review source was rejected: %v", err)
		}
	})
}

func TestPolicyDerivedRevisionReviewCannotBeBypassed(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*Candidate){
		"exact approved source still needs its required receipt": func(candidate *Candidate) {
			candidate.Input.Revisions[0].CurrencyPolicy = CurrencyExactApproved
			candidate.Input.Revisions[0].IsLatestApprovedAtFreeze = false
			candidate.Input.Revisions[0].IsLatestCurrentAtFreeze = false
			candidate.Document.Revisions[0].CurrencyPolicy = CurrencyExactApproved
			candidate.Input.ReviewReceipts = []ReviewReceiptBinding{}
			candidate.Materials.ReviewReceipts = []ReviewReceiptMaterial{}
		},
		"human source cannot disable canonical review": func(candidate *Candidate) {
			candidate.Input.Revisions[0].CanonicalReviewRequired = false
			candidate.Input.ReviewReceipts = []ReviewReceiptBinding{}
			candidate.Document.Revisions[0].CanonicalReviewRequired = false
			candidate.Document.ReviewRequirements = []ReviewRequirementCandidate{}
			candidate.Materials.ReviewReceipts = []ReviewReceiptMaterial{}
		},
		"workspace cannot require canonical review": func(candidate *Candidate) {
			candidate.Input.Revisions[1].CanonicalReviewRequired = true
			candidate.Document.Revisions[1].CanonicalReviewRequired = true
		},
		"workspace cannot be a required governed source": func(candidate *Candidate) {
			candidate.Input.Revisions[1].SourceRequiredAtFreeze = true
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := goldenCandidate(t)
			mutate(&candidate)
			if _, err := Compile(candidate); err == nil {
				t.Fatal("policy-derived review constraint was bypassed")
			}
		})
	}
}

func TestValidateRevisionsRejectsTargetRevisionUnderGovernedPurpose(t *testing.T) {
	t.Parallel()
	candidate := goldenCandidate(t)
	workspace := candidate.Input.Revisions[1]
	governedAlias := cloneRevision(workspace)
	governedAlias.Purpose = "governed-source"
	revisions := []RevisionBinding{governedAlias, workspace}
	if err := validateRevisions(revisions, candidate.Input.InputManifests, candidate.Input.Target); err == nil {
		t.Fatal("Workspace target revision id was accepted under a governed source purpose")
	}
}

func TestPredecessorClosureRejectsForgedMaterializedRevisionFacts(t *testing.T) {
	t.Parallel()
	candidate := goldenCandidate(t)
	forged := candidate.Input.Predecessors[0].ArtifactRevisions[0]
	forged.ArtifactID = "20202020-2020-4020-8020-202020202020"
	forged.ContentHash = digest("2")
	candidate.Input.Predecessors[0].MaterializedArtifactRevisions = []ArtifactRevisionReference{forged}
	if err := validatePredecessorRevisionClosure(candidate.Input.Predecessors, candidate.Input.Revisions); err == nil {
		t.Fatal("materialized revision with forged artifact/content facts was accepted")
	}
}

func TestManifestClosureRejectsConflictingHashesForOneIdentity(t *testing.T) {
	t.Parallel()
	candidate := goldenCandidate(t)
	conflicting := candidate.Input.Predecessors[0]
	conflicting.EdgeID = "second-quality-edge"
	conflicting.MappingOrdinal = 1
	conflicting.InputManifest = cloneManifestReference(conflicting.InputManifest)
	conflicting.InputManifest.Hash = digest("3")
	predecessors := []PredecessorBinding{candidate.Input.Predecessors[0], conflicting}
	if err := validateManifestClosure(predecessors, candidate.Input.InputManifests); err == nil {
		t.Fatal("one manifest identity carrying conflicting predecessor hashes was accepted")
	}
}

func TestPredecessorsRejectCrossBindingIdentityFactConflicts(t *testing.T) {
	t.Parallel()
	newPair := func(t *testing.T) []PredecessorBinding {
		t.Helper()
		candidate := goldenCandidate(t)
		firstInput := cloneInput(candidate.Input)
		secondInput := cloneInput(candidate.Input)
		first := firstInput.Predecessors[0]
		second := secondInput.Predecessors[0]
		first.EdgeID, first.MappingOrdinal = "a-edge", 0
		second.EdgeID, second.MappingOrdinal = "z-edge", 1
		second.SourceNodeRunID = "20202020-2020-4020-8020-202020202020"
		return []PredecessorBinding{first, second}
	}
	tests := map[string]func([]PredecessorBinding){
		"source node row": func(predecessors []PredecessorBinding) {
			predecessors[1].SourceNodeRunID = predecessors[0].SourceNodeRunID
			predecessors[1].SourceNodeKey = "conflicting-node-key"
		},
		"proposal lineage": func(predecessors []PredecessorBinding) {
			proposalID := "24242424-2424-4424-8424-242424242424"
			predecessors[0].OutputProposal = &ProposalReference{ID: proposalID, PayloadHash: digest("4")}
			predecessors[1].ProposalPins = []ProposalLineagePin{{
				Manifest:                 ManifestReference{Hash: digest("5"), ID: "25252525-2525-4525-8525-252525252525"},
				ProducerDefinitionNodeID: "producer", ProducerNodeKey: "producer",
				Proposal: ProposalReference{ID: proposalID, PayloadHash: digest("6")},
			}}
		},
		"delivery slice": func(predecessors []PredecessorBinding) {
			sliceID := "26262626-2626-4626-8626-262626262626"
			predecessors[0].DeliverySliceRefs = []DeliverySliceReference{{FanOutNodeID: "fan-out", ID: sliceID, Key: "alpha"}}
			predecessors[1].DeliverySliceRefs = []DeliverySliceReference{{FanOutNodeID: "fan-out", ID: sliceID, Key: "beta"}}
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			predecessors := newPair(t)
			mutate(predecessors)
			if err := validatePredecessors(predecessors); err == nil {
				t.Fatal("cross-binding identity conflict was accepted")
			}
		})
	}
}

func TestNodeInputRejectsOneSourceNodeWithDifferentOutputRevisions(t *testing.T) {
	t.Parallel()
	candidate := goldenCandidate(t)
	var wire nodeInputEnvelopeWire
	if err := json.Unmarshal(candidate.Materials.NodeInput, &wire); err != nil {
		t.Fatal(err)
	}
	second := wire.Bindings[0]
	second.EdgeID = "z-edge"
	second.Source.OutputRevisionID = candidate.Input.Revisions[0].RevisionID
	second.Source.ArtifactRevisions = []domain.ArtifactRef{{
		ArtifactID: candidate.Input.Revisions[0].ArtifactID, ContentHash: candidate.Input.Revisions[0].ContentHash,
		RevisionID: candidate.Input.Revisions[0].RevisionID,
	}}
	envelope, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{wire.Bindings[0], second})
	if err != nil {
		t.Fatal(err)
	}
	input := cloneInput(candidate.Input)
	input.NodeInput.SemanticHash = authorityHash(envelope.Hash())
	input.Predecessors = []PredecessorBinding{
		goldenPredecessorAt(t, envelope, 0, "20202020-2020-4020-8020-202020202020"),
		goldenPredecessorAt(t, envelope, 1, "20202020-2020-4020-8020-202020202020"),
	}
	for index := range input.Predecessors {
		input.Predecessors[index].SourceNodeType = string(domain.NodeHumanEdit)
	}
	if _, err := validateNodeInputMaterial(envelope.Canonical(), input, candidate.Materials.BuildManifest); err == nil {
		t.Fatal("one source node run carrying different raw output revision ids was accepted")
	}
}

func TestManifestsRejectUnprovenNodeAndQualificationRoles(t *testing.T) {
	t.Parallel()
	candidate := goldenCandidate(t)
	for index, role := range []string{ManifestRoleNode, ManifestRoleQualification} {
		extra := candidate.Input.InputManifests[0]
		extra.ID = []string{"20202020-2020-4020-8020-202020202020", "21212121-2121-4121-8121-212121212121"}[index]
		extra.Role = role
		manifests := append(cloneSlice(candidate.Input.InputManifests), extra)
		sort.Slice(manifests, func(i, j int) bool {
			return manifests[i].Role+"\x00"+manifests[i].ID < manifests[j].Role+"\x00"+manifests[j].ID
		})
		if err := validateManifests(manifests, candidate.Input.Project.ID, candidate.Input.Run); err == nil {
			t.Fatalf("unproven %s manifest role was accepted", role)
		}
	}
}

func TestQualityGateResultRejectsMixedUpstreamAuthority(t *testing.T) {
	t.Parallel()
	candidate := goldenCandidate(t)
	var nodeInput nodeInputEnvelopeWire
	if err := json.Unmarshal(candidate.Materials.NodeInput, &nodeInput); err != nil {
		t.Fatal(err)
	}
	var selectedBuild core.WorkbenchBundle
	if err := json.Unmarshal(candidate.Materials.BuildManifest, &selectedBuild); err != nil {
		t.Fatal(err)
	}
	if err := validateQualityGateResult(nodeInput.Bindings[0].Output, candidate.Input, selectedBuild); err != nil {
		t.Fatalf("golden typed quality result was rejected: %v", err)
	}
	mixed := cloneInput(candidate.Input)
	mixed.QualityResult.QualityRunID = "20202020-2020-4020-8020-202020202020"
	if err := validateQualityGateResult(nodeInput.Bindings[0].Output, mixed, selectedBuild); err == nil {
		t.Fatal("quality output A was combined with quality authority B")
	}
}

func TestQualityBuildManifestAcceptsDerivedLeafAndRejectsWrongRoot(t *testing.T) {
	t.Parallel()
	candidate := goldenCandidate(t)
	var nodeInput nodeInputEnvelopeWire
	if err := json.Unmarshal(candidate.Materials.NodeInput, &nodeInput); err != nil {
		t.Fatal(err)
	}
	var result qualityGateResultWire
	if err := json.Unmarshal(nodeInput.Bindings[0].Output, &result); err != nil {
		t.Fatal(err)
	}
	var selectedBuild core.WorkbenchBundle
	if err := json.Unmarshal(candidate.Materials.BuildManifest, &selectedBuild); err != nil {
		t.Fatal(err)
	}
	rootID := "20202020-2020-4020-8020-202020202020"
	selectedBuild.RootBuildManifestID = rootID
	selectedBuild.ManifestHash = ""
	selectedHash, err := domain.CanonicalHash(selectedBuild)
	if err != nil {
		t.Fatal(err)
	}
	selectedBuild.ManifestHash = selectedHash
	input := cloneInput(candidate.Input)
	input.Build.BuildManifest.ManifestHash = authorityHash(selectedHash)
	input.QualityResult.BuildManifestHash = authorityHash(selectedHash)
	result.BuildManifest.BundleIDs[len(result.BuildManifest.BundleIDs)-1] = rootID
	result.BuildManifest.Hash = ""
	manifestHash, err := domain.CanonicalHash(*result.BuildManifest)
	if err != nil {
		t.Fatal(err)
	}
	result.BuildManifest.Hash = manifestHash
	derivedRaw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateQualityGateResult(derivedRaw, input, selectedBuild); err != nil {
		t.Fatalf("derived producer leaf/root coordinate was rejected: %v", err)
	}
	result.BuildManifest.BundleIDs[len(result.BuildManifest.BundleIDs)-1] = "21212121-2121-4121-8121-212121212121"
	result.BuildManifest.Hash = ""
	wrongHash, err := domain.CanonicalHash(*result.BuildManifest)
	if err != nil {
		t.Fatal(err)
	}
	result.BuildManifest.Hash = wrongHash
	wrongRaw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateQualityGateResult(wrongRaw, input, selectedBuild); err == nil {
		t.Fatal("typed quality result selected a bundle outside the producer root coordinate")
	}
}

func TestQualityBuildManifestRejectsWorkbenchSourceSwap(t *testing.T) {
	t.Parallel()
	candidate := goldenCandidate(t)
	var nodeInput nodeInputEnvelopeWire
	if err := json.Unmarshal(candidate.Materials.NodeInput, &nodeInput); err != nil {
		t.Fatal(err)
	}
	var result qualityGateResultWire
	if err := json.Unmarshal(nodeInput.Bindings[0].Output, &result); err != nil {
		t.Fatal(err)
	}
	var selectedBuild core.WorkbenchBundle
	if err := json.Unmarshal(candidate.Materials.BuildManifest, &selectedBuild); err != nil {
		t.Fatal(err)
	}
	result.BuildManifest.Sources = []domain.ArtifactRef{{
		ArtifactID:  "20202020-2020-4020-8020-202020202020",
		RevisionID:  "21212121-2121-4121-8121-212121212121",
		ContentHash: digest("2"),
	}}
	result.BuildManifest.Hash = ""
	manifestHash, err := domain.CanonicalHash(*result.BuildManifest)
	if err != nil {
		t.Fatal(err)
	}
	result.BuildManifest.Hash = manifestHash
	forged, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateQualityGateResult(forged, candidate.Input, selectedBuild); err == nil {
		t.Fatal("re-signed typed BuildManifest with swapped Workbench sources was accepted")
	}
}

func TestBuildManifestAllowsInitialAndPriorWorkspaceBaselines(t *testing.T) {
	t.Parallel()
	candidate := goldenCandidate(t)
	if _, err := validateBuildManifestMaterial(candidate.Materials.BuildManifest, candidate.Input); err != nil {
		t.Fatalf("initial nil Workspace baseline was rejected: %v", err)
	}
	var bundle core.WorkbenchBundle
	if err := json.Unmarshal(candidate.Materials.BuildManifest, &bundle); err != nil {
		t.Fatal(err)
	}
	bundle.CurrentWorkspaceRevision = &core.VersionRef{
		ArtifactID: "20202020-2020-4020-8020-202020202020", RevisionID: "21212121-2121-4121-8121-212121212121",
		ContentHash: digest("2"),
	}
	bundle.ManifestHash = ""
	hash, err := domain.CanonicalHash(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundle.ManifestHash = hash
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	input := cloneInput(candidate.Input)
	input.Build.BuildManifest.ManifestHash = authorityHash(hash)
	if _, err := validateBuildManifestMaterial(raw, input); err != nil {
		t.Fatalf("prior Workspace baseline was rejected: %v", err)
	}
}

func TestDefinitionIncomingEdgeSetMustEqualNodeInput(t *testing.T) {
	t.Parallel()
	candidate := goldenCandidate(t)
	var definition domain.WorkflowDefinition
	if err := json.Unmarshal(candidate.Materials.Definition, &definition); err != nil {
		t.Fatal(err)
	}
	if err := validateDefinitionEdgeClosure(definition, candidate.Input); err != nil {
		t.Fatalf("golden definition edge closure was rejected: %v", err)
	}
	for index := range definition.Edges {
		if definition.Edges[index].To == ExternalQualificationGate {
			definition.Edges[index].ID = "forged-quality-edge"
		}
	}
	if err := validateDefinitionEdgeClosure(definition, candidate.Input); err == nil {
		t.Fatal("NodeInput edge absent from the retained definition was accepted")
	}
}

func assertJSONKeys(t *testing.T, value any, want []string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON keys = %v, want %v; wire=%s", got, want, encoded)
	}
}

func mustCompileGolden(t *testing.T) Record {
	t.Helper()
	record, err := Compile(goldenCandidate(t))
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func goldenCandidate(t *testing.T) Candidate {
	t.Helper()
	const (
		owner             = "11111111-1111-4111-8111-111111111111"
		project           = "22222222-2222-4222-8222-222222222222"
		definitionID      = "33333333-3333-4333-8333-333333333333"
		definitionVersion = "44444444-4444-4444-8444-444444444444"
		run               = "55555555-5555-4555-8555-555555555555"
		node              = "66666666-6666-4666-8666-666666666666"
		operation         = "77777777-7777-4777-8777-777777777777"
		authority         = "88888888-8888-4888-8888-888888888888"
		activation        = "99999999-9999-4999-8999-999999999999"
		qualityRun        = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		sourceNodeRun     = "17171717-1717-4717-8717-171717171717"
		manifestID        = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		buildManifestID   = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		buildContractID   = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		workspaceArtifact = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
		workspaceRevision = "ffffffff-ffff-4fff-8fff-ffffffffffff"
		sourceArtifact    = "12121212-1212-4212-8212-121212121212"
		sourceRevision    = "13131313-1313-4313-8313-131313131313"
		policyAuthority   = "16161616-1616-4616-8616-161616161616"
		manifestGroup     = "29292929-2929-4929-8929-292929292929"
		deliverySlice     = "30303030-3030-4030-8030-303030303030"
		implementation    = "31313131-3131-4131-8131-313131313131"
		reportArtifact    = "32323232-3232-4232-8232-323232323232"
		reportRevision    = "34343434-3434-4434-8434-343434343434"
	)
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	sourceRaw := []byte(`{"requirements":"approved"}`)
	workspaceRaw := []byte(`{"workspace":"ready"}`)
	sourceHash := RawSHA256(sourceRaw)
	workspaceHash := RawSHA256(workspaceRaw)

	definition := goldenDefinition(t, definitionID, owner, now)
	definitionRaw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	sourceRef := domain.ArtifactRef{ArtifactID: sourceArtifact, RevisionID: sourceRevision, ContentHash: sourceHash}
	manifest, err := domain.NewInputManifest(
		manifestID, project, "workflow_start", "", nil,
		[]domain.ManifestSource{{Ref: sourceRef, Purpose: "governed-source"}}, json.RawMessage(`{"schemaVersion":"workflow-input/v1"}`),
		"workflow-input/v1", owner, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	workspaceRef := domain.ArtifactRef{ArtifactID: workspaceArtifact, RevisionID: workspaceRevision, ContentHash: workspaceHash}
	workflowManifest := qualityWorkflowBuildManifest{
		SchemaVersion: 1, ProjectID: project, RunID: run, ManifestGroupKey: manifestGroup,
		SliceIDs: []string{deliverySlice}, BundleIDs: []string{buildManifestID}, Sources: []domain.ArtifactRef{sourceRef, workspaceRef},
		Constraints: json.RawMessage(`{}`), CreatedAt: now,
	}
	workflowManifestHash, err := domain.CanonicalHash(workflowManifest)
	if err != nil {
		t.Fatal(err)
	}
	workflowManifest.Hash = workflowManifestHash
	findings, err := json.Marshal(qualityFindingsWire{
		Checks: json.RawMessage(`[]`), Diagnostics: json.RawMessage(`[]`), QualityRunID: qualityRun,
		ReportArtifactID: reportArtifact, ReportRevisionID: reportRevision, Score: 100,
		WorkspaceRevision: core.VersionRef{ArtifactID: workspaceArtifact, RevisionID: workspaceRevision, ContentHash: workspaceHash},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := json.Marshal(qualityGateResultWire{
		Passed: true, Findings: findings, QualityRunID: qualityRun, WorkspaceRevision: &workspaceRef,
		BuildManifest: &workflowManifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "quality-external", FromPort: "default", ToPort: "default",
		Source: domain.NodeOutputReference{
			RunID: run, NodeKey: "release-quality", DefinitionNodeID: "release-quality",
			InputManifest: &domain.ManifestRef{ID: manifest.ID, Hash: manifest.Hash}, OutputRevisionID: workspaceRevision,
			ArtifactRevisions: []domain.ArtifactRef{workspaceRef}, MaterializedArtifactRevisions: []domain.ArtifactRef{},
			ProposalPins: []domain.ProposalLineagePin{}, DeliverySliceRefs: []domain.WorkflowSliceRef{},
		},
		Output: value, Value: value,
	}})
	if err != nil {
		t.Fatal(err)
	}
	nodeInputRaw := []byte(envelope.Canonical())
	predecessor := goldenPredecessor(t, envelope, sourceNodeRun)

	bundle := goldenBuildManifest(buildManifestID, project, run, workspaceRef, owner, now)
	manifestGroupID, deliverySliceID := manifestGroup, deliverySlice
	bundle.ManifestGroupKey = &manifestGroupID
	bundle.DeliverySliceID = &deliverySliceID
	bundle.ManifestHash = ""
	bundleHash, err := domain.CanonicalHash(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundle.ManifestHash = bundleHash
	buildManifestRaw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	contract := goldenBuildContract(project, buildManifestID, bundleHash, sourceRef)
	contractHash, err := domain.CanonicalHash(contract)
	if err != nil {
		t.Fatal(err)
	}
	buildContractRaw, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}

	sourceManifestID := manifestID
	implementationProposalID := implementation
	revisions := []RevisionBinding{
		{
			ArtifactID: sourceArtifact, ArtifactKind: "product_requirements", ByteSize: int64(len(sourceRaw)), ContentHash: sourceHash,
			CanonicalReviewRequired: true, ChangeSourceAtFreeze: "human",
			ContentRef: "objects/revisions/source", ContentStore: "repository", CurrencyPolicy: CurrencyLatestApprovedRequired,
			IsLatestApprovedAtFreeze: true, IsLatestCurrentAtFreeze: true, Purpose: "governed-source", RawBytesHash: sourceHash,
			RevisionID: sourceRevision, SchemaVersion: 1, SourceManifestID: &sourceManifestID, WorkflowStatusAtFreeze: ApprovedRevisionStatus,
			SourceRequiredAtFreeze: true,
		},
		{
			ArtifactID: workspaceArtifact, ArtifactKind: "workspace", ByteSize: int64(len(workspaceRaw)), ContentHash: workspaceHash,
			CanonicalReviewRequired: false, ChangeSourceAtFreeze: "system",
			ContentRef: "objects/revisions/workspace", ContentStore: "repository", CurrencyPolicy: CurrencyLatestApprovedRequired,
			IsLatestApprovedAtFreeze: true, IsLatestCurrentAtFreeze: true, Purpose: RevisionPurposeWorkspaceTarget,
			ImplementationProposalID: &implementationProposalID, RawBytesHash: workspaceHash, RevisionID: workspaceRevision,
			SchemaVersion: 1, SourceManifestID: &sourceManifestID, SourceRequiredAtFreeze: false,
			WorkflowStatusAtFreeze: ApprovedRevisionStatus,
		},
	}
	receipt := goldenReviewReceipt(t, owner, project, revisions[0], now)
	manifestBinding := InputManifestBinding{
		ContentHash: RawSHA256(manifestRaw), ContentRef: "objects/manifests/run", ContentStore: "repository", ID: manifestID,
		Kind: manifest.JobType, ManifestHash: authorityHash(manifest.Hash), ProjectID: project, RawBytesHash: RawSHA256(manifestRaw),
		RawBytesSize: int64(len(manifestRaw)), SchemaVersion: 1,
	}
	input := WorkflowInputDocument{
		Project: ProjectBinding{GovernanceMode: GovernanceSolo, ID: project},
		Definition: DefinitionBinding{
			DefinitionHash: authorityHash(definition.Hash), DefinitionID: definitionID, DefinitionVersion: 3,
			DefinitionVersionID: definitionVersion, ExecutionProfileHash: executionProfileHashV3,
			ExecutionProfileVersion: ExecutionProfileV3, RawBytesHash: RawSHA256(definitionRaw), RawBytesSize: int64(len(definitionRaw)),
		},
		Run: RunBinding{
			ID: run, InputManifestHash: authorityHash(manifest.Hash), InputManifestID: manifestID,
			ScopeRawBytesHash: RawSHA256([]byte(`{}`)), ScopeRawBytesSize: 2,
			StartedAt: "2026-07-19T00:00:00.000000Z", StartedBy: owner,
		},
		Gate: GateBinding{
			ActivationEventID: activation, ActivationEventSequence: 43, DefinitionNodeID: ExternalQualificationGate,
			GateName: ExternalQualificationGate, NodeKey: ExternalQualificationGate, NodeRunID: node,
			NodeType: ExternalQualificationNodeType, SliceIdentity: SliceIdentity{Kind: SliceKindRoot}, StageGate: ExternalQualificationGate,
		},
		NodeInput: NodeInputBinding{
			BindingCount: 1, RawBytesHash: RawSHA256(nodeInputRaw), RawBytesSize: int64(len(nodeInputRaw)),
			SemanticHash: authorityHash(envelope.Hash()),
		},
		Predecessors: []PredecessorBinding{predecessor},
		InputManifests: []InputManifestBinding{
			withManifestRole(manifestBinding, ManifestRolePredecessor), withManifestRole(manifestBinding, ManifestRoleRun),
		},
		Revisions: revisions,
		ReviewReceipts: []ReviewReceiptBinding{{
			ArtifactID: sourceArtifact, ProjectID: project, Purpose: "governed-source", ReceiptHash: receipt.Hash,
			ReceiptRawBytesHash: RawSHA256(receipt.Bytes), ReceiptRawBytesSize: int64(len(receipt.Bytes)),
			ReceiptSchemaVersion: CanonicalReviewReceiptV1, ReviewRequestID: receipt.Receipt.ReviewRequest.ID,
			RevisionContentHash: sourceHash, RevisionID: sourceRevision,
		}},
		Build: BuildBinding{
			BuildManifest: BuildManifestBinding{
				ContentHash: RawSHA256(buildManifestRaw), ID: buildManifestID, ManifestHash: authorityHash(bundleHash),
				RawBytesHash: RawSHA256(buildManifestRaw), RawBytesSize: int64(len(buildManifestRaw)), StatusAtFreeze: ConsumedBuildManifestStatus,
			},
			BuildContract: BuildContractBinding{
				ContentHash: RawSHA256(buildContractRaw), ContractHash: authorityHash(contractHash), ID: buildContractID,
				RawBytesHash: RawSHA256(buildContractRaw), RawBytesSize: int64(len(buildContractRaw)), StatusAtFreeze: ReadyBuildContractStatus,
			},
		},
		QualificationPolicy: QualificationPolicyBinding{
			AuthorityHash: digest("7"), AuthorityID: policyAuthority, ExternalGatePolicy: ExternalQualificationPolicyV1,
		},
		QualityResult: QualityResultBinding{
			BuildManifestHash: authorityHash(bundleHash), BuildManifestID: buildManifestID, Passed: true, QualityRunID: qualityRun,
			WorkspaceRevisionContentHash: workspaceHash, WorkspaceRevisionID: workspaceRevision,
		},
		Target: TargetDocument{
			ManifestSubject: "stable-subject", NodeKey: ExternalQualificationGate, ProjectID: project,
			StageGate: ExternalQualificationGate, TargetRevisionContentHash: workspaceHash,
			TargetRevisionID: workspaceRevision, WorkflowRunID: run,
		},
	}
	materials := RetainedMaterials{
		BuildContract: buildContractRaw, BuildManifest: buildManifestRaw, Definition: definitionRaw,
		InputManifests: []InputManifestMaterial{
			{Bytes: manifestRaw, ManifestID: manifestID, Role: ManifestRolePredecessor},
			{Bytes: manifestRaw, ManifestID: manifestID, Role: ManifestRoleRun},
		},
		NodeInput: nodeInputRaw, ReviewReceipts: []ReviewReceiptMaterial{{Bytes: receipt.Bytes, ReviewRequestID: receipt.Receipt.ReviewRequest.ID}},
		Revisions: []RevisionMaterial{
			{Bytes: sourceRaw, Purpose: "governed-source", RevisionID: sourceRevision},
			{Bytes: workspaceRaw, Purpose: RevisionPurposeWorkspaceTarget, RevisionID: workspaceRevision},
		},
		RunScope: []byte(`{}`),
	}
	document, err := candidateDocumentFromRecord(input, materials)
	if err != nil {
		t.Fatal(err)
	}
	return Candidate{
		Request: FreezeRequest{
			AuthorityID: authority, ExpectedRunCursor: 42, NodeKey: ExternalQualificationGate, NodeRunID: node,
			OperationID: operation, ProjectID: project, WorkflowRunID: run,
		},
		Document: document, Input: input, Materials: materials,
	}
}

func goldenDefinition(t *testing.T, id, owner string, now time.Time) domain.WorkflowDefinition {
	t.Helper()
	schema := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	external := domain.ExactExternalQualificationGateConfig()
	nodes := []domain.NodeDefinition{
		{
			ID: "entry", Name: "Entry", Type: domain.NodeArtifactInput, InputSchema: schema, OutputSchema: schema,
			ArtifactInput: &domain.ArtifactInputNodeConfig{
				AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, AllowedKinds: []string{"product_requirements"},
				RequireApproved: true, MinimumArtifacts: 1, MaximumArtifacts: 1,
			},
		},
		{
			ID: "release-quality", Name: "Release quality", Type: domain.NodeQualityGate, InputSchema: schema, OutputSchema: schema,
			QualityGate: &domain.QualityGateNodeConfig{GateName: "release", Blocking: true},
		},
		{
			ID: ExternalQualificationGate, Name: "External qualification", Type: domain.NodeExternalQualificationGate,
			InputSchema: schema, OutputSchema: schema, ExternalQualificationGate: &external,
		},
	}
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		id, 3, "Golden qualification", "workflow-definition/v3", nodes,
		[]domain.WorkflowEdge{{ID: "entry-quality", From: "entry", To: "release-quality"}, {ID: "quality-external", From: "release-quality", To: ExternalQualificationGate}},
		domain.WorkflowInputContract{
			Capability: "golden-input", ManifestJobTypes: []string{"workflow_start"}, ArtifactKinds: []string{"product_requirements"},
			MinimumArtifacts: 1, MaximumArtifacts: 1, RequireApproved: true, RequiredSourcePurposes: []string{"governed-source"},
			ManifestSchemaContracts: map[string]string{"workflow_start": "workflow-input/v1"},
		},
		domain.WorkflowOutputContract{
			Capability: domain.WorkflowOutputApplication, ProducedArtifactKinds: []string{"workspace"},
			TerminalOutcome: domain.WorkflowOutcomeDeployment, TerminalNodeType: domain.NodeExternalQualificationGate,
		},
		domain.WorkflowExecutionProfileRef{Version: ExecutionProfileV3, Hash: strings.TrimPrefix(executionProfileHashV3, "sha256:")}, owner, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func goldenPredecessor(t *testing.T, envelope domain.NodeInputEnvelope, sourceNodeRunID string) PredecessorBinding {
	return goldenPredecessorAt(t, envelope, 0, sourceNodeRunID)
}

func goldenPredecessorAt(t *testing.T, envelope domain.NodeInputEnvelope, bindingIndex int, sourceNodeRunID string) PredecessorBinding {
	t.Helper()
	bindings := envelope.Bindings()
	if bindingIndex < 0 || bindingIndex >= len(bindings) {
		t.Fatal("golden envelope binding index")
	}
	binding := bindings[bindingIndex]
	var document map[string]any
	if err := json.Unmarshal(envelope.Canonical(), &document); err != nil {
		t.Fatal(err)
	}
	items := document["bindings"].([]any)
	bindingBytes, err := CanonicalJSON(items[bindingIndex])
	if err != nil {
		t.Fatal(err)
	}
	artifacts := make([]ArtifactRevisionReference, len(binding.Source.ArtifactRevisions))
	for index, reference := range binding.Source.ArtifactRevisions {
		artifacts[index] = artifactReferenceFromDomain(reference)
	}
	return PredecessorBinding{
		ArtifactRevisions: artifacts, BindingRawBytesHash: RawSHA256(bindingBytes), DeliverySliceRefs: []DeliverySliceReference{},
		EdgeID: binding.EdgeID, InputManifest: &ManifestReference{Hash: authorityHash(binding.Source.InputManifest.Hash), ID: binding.Source.InputManifest.ID},
		MappingHash: RawSHA256([]byte(`{}`)), MappingKind: MappingKindIdentity, MappingOrdinal: int64(bindingIndex),
		MaterializedArtifactRevisions: []ArtifactRevisionReference{}, OutputHash: authorityHash(binding.OutputHash), OutputProposal: nil,
		OutputRevisionNumber: 1, ProposalPins: []ProposalLineagePin{}, SourceDefinitionNodeID: binding.Source.DefinitionNodeID,
		SourceNodeKey: binding.Source.NodeKey, SourceNodeRunID: sourceNodeRunID, SourceNodeType: string(domain.NodeQualityGate),
		SourcePort: binding.FromPort, SourceSliceIdentity: SliceIdentity{Kind: SliceKindRoot}, SourceStatus: CompletedSourceStatus,
		TargetPort: binding.ToPort, ValueHash: authorityHash(binding.ValueHash),
	}
}

func goldenBuildManifest(id, project, run string, workspace domain.ArtifactRef, owner string, now time.Time) core.WorkbenchBundle {
	runID := run
	workspaceVersion := core.VersionRef{ArtifactID: workspace.ArtifactID, RevisionID: workspace.RevisionID, ContentHash: workspace.ContentHash}
	asset := core.AssetRef{AssetID: "asset", ContentHash: digest("8"), MediaType: "application/json", ByteSize: 2}
	return core.WorkbenchBundle{
		ID: id, ProjectID: project, RootBuildManifestID: id, WorkflowRunID: &runID,
		PageSpecRevision: workspaceVersion, PrototypeRevision: workspaceVersion, RequirementRevisions: []core.VersionRef{workspaceVersion},
		BlueprintRevision: workspaceVersion, ContractRevisions: []core.VersionRef{}, DesignSystemRevisions: []core.VersionRef{},
		ContextRevisions: []core.WorkbenchContextRevision{}, CurrentWorkspaceRevision: nil,
		SceneGraph: asset, RenderedFrames: []core.RenderedFrameRef{}, InteractionManifest: asset, FixtureBundle: asset,
		TokenManifest: asset, ComponentMapping: asset, TraceMatrix: asset, AcceptanceManifest: asset,
		Assumptions: []string{}, Waivers: []string{}, CreatedBy: owner, CreatedAt: now,
	}
}

func goldenBuildContract(project, manifestID, manifestHash string, source domain.ArtifactRef) constructor.ContractContent {
	sourceRevision := constructor.ExactRevisionRef{
		Kind: "product_requirements", Purpose: "governed-source", Required: true, ArtifactID: source.ArtifactID,
		RevisionID: source.RevisionID, ContentHash: source.ContentHash, ApprovalStatus: ApprovedRevisionStatus,
	}
	return constructor.ContractContent{
		SchemaVersion: constructor.BuildContractSchemaVersion,
		Compiler:      constructor.CompilerIdentity{Version: constructor.CompilerVersion, Hash: strings.Repeat("9", 64)},
		ProjectID:     project, DeliverySliceID: "", BuildManifest: constructor.BuildManifestRef{ID: manifestID, ContentHash: manifestHash},
		SourceRevisions:     []constructor.ExactRevisionRef{sourceRevision},
		FullStackTemplate:   constructor.FullStackTemplateRef{ID: "template", ContentHash: strings.Repeat("a", 64), Certification: "certified", PolicyStatus: "active"},
		TemplateReleaseRefs: []constructor.TemplateReleaseRef{}, Routes: []constructor.RouteConstraint{}, States: []constructor.StateConstraint{},
		ContractBindings: []constructor.ContractBinding{}, AcceptanceCriteria: []constructor.AcceptanceCriterion{},
		Oracles: []constructor.Oracle{{ID: "oracle", Kind: "unit", Target: "source", SourceRevision: sourceRevision, AcceptanceCriterionIDs: []string{}}},
		Obligations: []constructor.Obligation{{
			ID: "obligation", Level: "must", Kind: "source", SourceRevision: sourceRevision, SourceAnchorID: "root",
			OracleIDs: []string{"oracle"}, DependsOn: []string{}, Status: "ready",
		}},
		Waivers: []constructor.Waiver{}, Gaps: []constructor.BuildGap{}, Conflicts: []constructor.BuildConflict{},
		ForbiddenClaims: []string{}, Status: constructor.StatusReady,
	}
}

func goldenReviewReceipt(t *testing.T, owner, project string, revision RevisionBinding, now time.Time) canonicalreviewreceipt.Compiled {
	t.Helper()
	request := "14141414-1414-4414-8414-141414141414"
	decision := "15151515-1515-4515-8515-151515151515"
	approvedAt := "2026-07-18T12:00:00.000000Z"
	receipt := canonicalreviewreceipt.Receipt{
		IssuedAt: approvedAt,
		ReviewRequest: canonicalreviewreceipt.ReviewRequestSnapshot{
			ArtifactID: revision.ArtifactID, ClosedAt: approvedAt, ClosedByDecisionID: decision, ContentHash: revision.ContentHash,
			ID: request, ProjectID: project, RequestedAt: "2026-07-18T10:00:00.000000Z", RequestedBy: owner,
			ReviewAuthorityVersion: 1, RevisionID: revision.RevisionID, Status: "approved",
		},
		Revision: canonicalreviewreceipt.RevisionSnapshot{
			ApprovedAt: approvedAt, ArtifactID: revision.ArtifactID, ArtifactSchemaVersion: int(revision.SchemaVersion), ByteSize: revision.ByteSize,
			ChangeSource: "human", ChangeSummary: "Reviewed source", ContentHash: revision.ContentHash,
			ContentRef: revision.ContentRef, ContentStore: revision.ContentStore, CreatedAt: "2026-07-18T09:00:00.000000Z",
			CreatedBy: owner, ID: revision.RevisionID, RevisionNumber: 1, SourceManifestID: cloneStringPointer(revision.SourceManifestID),
			ProposalID: cloneStringPointer(revision.ProposalID), ImplementationProposalID: cloneStringPointer(revision.ImplementationProposalID),
			WorkflowStatus: "approved",
		},
		Policy: canonicalreviewreceipt.PolicySnapshot{Value: canonicalreviewreceipt.PolicyValue{
			GovernanceMode: GovernanceSolo, MinimumApprovals: 1, ProhibitSelfReview: true,
			ReviewerIDs: []string{owner}, SoloSelfReviewOwnerID: &owner,
		}},
		Decisions: canonicalreviewreceipt.DecisionsSnapshot{Decisions: []canonicalreviewreceipt.DecisionSnapshot{{
			AuthorityFacts: canonicalreviewreceipt.DecisionAuthorityFacts{
				ExplicitConfirmation: true, GovernanceMode: GovernanceSolo, OwnerCount: 1,
				PreconditionETag: `"review:14141414-1414-4414-8414-141414141414:open:0:0"`,
				ReviewerRole:     "owner", SoleOwnerID: &owner, Version: 1,
			},
			CreatedAt: approvedAt, Decision: "approve", ID: decision, ReviewerID: owner,
			SoloSelfReview: true, Summary: "Reviewed scope and accepted risk.",
		}}},
		Governance: canonicalreviewreceipt.GovernanceSnapshot{Mode: GovernanceSolo, OwnerCount: 1, SoleOwnerID: &owner},
		Approval: canonicalreviewreceipt.ApprovalSnapshot{
			ApprovalCount: 1, ApprovalDecisionIDs: []string{decision}, ApprovedAt: approvedAt,
			ArtifactID: revision.ArtifactID, ArtifactKind: revision.ArtifactKind, ArtifactLatestApprovedID: revision.RevisionID,
			ArtifactLatestRevisionID: revision.RevisionID, ArtifactLifecycle: "active", ArtifactVersion: 2,
			ClosedByDecisionID: decision, MinimumApprovals: 1, ProjectID: project, RevisionContentHash: revision.ContentHash,
			RevisionID: revision.RevisionID, SoloSelfReview: true, SubjectAuthorID: owner,
		},
	}
	compiled, err := canonicalreviewreceipt.Compile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func withManifestRole(binding InputManifestBinding, role string) InputManifestBinding {
	binding.Role = role
	return binding
}

func authorityHash(value string) string {
	if strings.HasPrefix(value, "sha256:") {
		return value
	}
	return "sha256:" + value
}

func digest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
