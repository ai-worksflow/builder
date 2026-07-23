package qualificationhandoff

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/qualificationpromotionv2"
)

func testUUID(index byte) string {
	value := make([]byte, 16)
	for position := range value {
		value[position] = index
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return uuid.Must(uuid.FromBytes(value)).String()
}

func testDigest(character byte) string {
	return "sha256:" + strings.Repeat(string(character), 64)
}

func testRecord(t *testing.T) Record {
	t.Helper()
	handoffID := testUUID(1)
	operationID := testUUID(2)
	outputID := testUUID(3)
	projectID := testUUID(4)
	runID := testUUID(5)
	gateID := testUUID(6)
	publishID := testUUID(7)
	artifactID := testUUID(8)
	parentID := testUUID(9)
	gateEventID := testUUID(10)
	publishEventID := testUUID(11)
	qualityRunID := testUUID(12)
	contentHash := testDigest('a')
	completedAt := "2026-07-20T12:30:45.123Z"
	target := PromotionTarget{
		ArtifactID: artifactID, NodeKey: externalQualificationKey, NodeRunID: gateID,
		ProjectID: projectID, RevisionContentHash: contentHash, RevisionID: parentID,
		StageGate: externalQualificationKey, Subject: "workspace-target", WorkflowRunID: runID,
	}
	completion := CompletionDocument{
		SchemaVersion: CompletionSchemaV1, HandoffID: handoffID, OperationID: operationID,
		ConsumptionHash: testDigest('b'), OutputRevisionID: outputID,
		OutputRevisionContentHash: contentHash, ProjectID: projectID, WorkflowRunID: runID,
		NodeRunID: gateID, NodeKey: externalQualificationKey, PublishNodeRunID: publishID,
		WorkflowEvents: []WorkflowEvent{
			{Role: gateCompletedRole, EventID: gateEventID, EventSequence: 41, EventType: gateCompletedEventType, NodeRunID: gateID, NodeKey: externalQualificationKey},
			{Role: publishAuthorizationRole, EventID: publishEventID, EventSequence: 42, EventType: publishAuthorizationEventType, NodeRunID: publishID, NodeKey: "publish-production"},
		},
		OutboxEvents: []OutboxEvent{
			{Role: gateCompletedRole, OutboxEventID: gateEventID, WorkflowEventID: gateEventID, EventType: gateCompletedEventType},
			{Role: publishAuthorizationRole, OutboxEventID: publishEventID, WorkflowEventID: publishEventID, EventType: publishAuthorizationEventType},
		},
		CompletedAt: completedAt,
	}
	authority := RevisionAuthorityDocument{
		SchemaVersion: RevisionAuthoritySchemaV1, HandoffID: handoffID,
		OperationID: operationID, OutputRevisionID: outputID,
		WorkflowInput: AuthorityReference{AuthorityID: testUUID(13), AuthorityHash: testDigest('c')},
		Plan:          AuthorityReference{AuthorityID: testUUID(14), AuthorityHash: testDigest('d')},
		Receipt:       ReceiptReference{ReceiptID: "qualification-receipt-v3", EnvelopeHash: testDigest('e')},
		Promotion: PromotionHashes{
			RequestHash: testDigest('f'), ClosureHash: testDigest('1'),
			RevisionIntentHash: testDigest('2'), ConsumptionHash: completion.ConsumptionHash,
		},
		Target: target,
		RevisionStateAtHandoff: RevisionStateAtHandoff{
			WorkflowStatus: "approved", ApprovedAt: completedAt, SupersededAt: nil,
			ParentWorkflowStatus: "superseded", ParentApprovedAt: "2026-07-20T12:00:00.000000Z",
			ParentSupersededAt: completedAt,
		},
		CopiedLineage: CopiedLineageSummary{
			SchemaVersion: "worksflow-qualification-handoff-copied-lineage/v1",
			RootHash:      testDigest('3'), SourceCount: 1, DependencyCount: 2, TraceCount: 3,
		},
	}
	workspace := ArtifactReference{ArtifactID: artifactID, RevisionID: outputID, ContentHash: contentHash}
	parentWorkspace := ArtifactReference{ArtifactID: artifactID, RevisionID: parentID, ContentHash: contentHash}
	buildManifest := BuildManifest{
		SchemaVersion: 1, ProjectID: projectID, RunID: runID, ManifestGroupKey: testUUID(17),
		SliceIDs: []string{testUUID(18)}, BundleIDs: []string{testUUID(19)},
		Sources: []ArtifactReference{parentWorkspace}, Constraints: json.RawMessage(`{}`),
		CreatedAt: "2026-07-20T12:00:00Z",
	}
	buildManifestHash, err := domain.CanonicalHash(buildManifest)
	if err != nil {
		t.Fatal(err)
	}
	buildManifest.Hash = buildManifestHash
	quality := QualityResult{
		Passed: true, QualityRunID: qualityRunID, WorkspaceRevision: workspace,
		Findings: QualityFindings{
			Checks: []json.RawMessage{}, Diagnostics: []json.RawMessage{}, QualityRunID: qualityRunID,
			ReportArtifactID: testUUID(15), ReportRevisionID: testUUID(16), Score: 100,
			WorkspaceRevision: parentWorkspace,
		},
		BuildManifest: buildManifest,
	}
	record := Record{
		HandoffID: uuid.MustParse(handoffID),
		Bundle: CompletionBundle{
			SchemaVersion: BundleSchemaV1, HandoffID: handoffID,
			Completion:        retainedCompletion(t, completion),
			RevisionAuthority: retainedAuthority(t, authority),
			OutputRevision: OutputRevision{
				ID: outputID, ArtifactID: artifactID, ParentRevisionID: parentID,
				RevisionNumber: 2, SchemaVersion: 1, ContentStore: "mongo", ContentRef: "revision/object",
				ContentHash: contentHash, ByteSize: 128,
				StateAtHandoff:     OutputStateAtHandoff{WorkflowStatus: "approved", ApprovedAt: completedAt, SupersededAt: nil},
				PromotionHandoffID: handoffID, CreatedAt: completedAt,
			},
			Workflow: WorkflowProjection{
				ProjectID: projectID, WorkflowRunID: runID, GateNodeRunID: gateID,
				GateNodeKey: externalQualificationKey, PublishNodeRunID: publishID,
				PublishNodeKey: "publish-production", EventCursorBefore: 40, EventCursorAfter: 42,
				QualityResult: quality, GateStatusAtHandoff: "completed",
				PublishStatusAtHandoff: "waiting_input", RunStatusAtHandoff: "waiting_input",
			},
		},
	}
	if err := ValidateRecord(record); err != nil {
		t.Fatalf("test fixture is invalid: %v", err)
	}
	return record
}

func retainedCompletion(t *testing.T, document CompletionDocument) CompletionMaterial {
	t.Helper()
	encoded, err := qualificationpromotionv2.CanonicalJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	return CompletionMaterial{
		Hash:     HandoffDomainHash(CompletionHashDomainV1, encoded),
		BytesHex: hex.EncodeToString(encoded), Document: document,
	}
}

func retainedAuthority(t *testing.T, document RevisionAuthorityDocument) RevisionAuthorityMaterial {
	t.Helper()
	encoded, err := qualificationpromotionv2.CanonicalJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	return RevisionAuthorityMaterial{
		Hash:     HandoffDomainHash(RevisionAuthorityDomainV1, encoded),
		BytesHex: hex.EncodeToString(encoded), Document: document,
	}
}

func refreshBuildManifestHash(t *testing.T, record *Record) {
	t.Helper()
	manifest := &record.Bundle.Workflow.QualityResult.BuildManifest
	manifest.Hash = ""
	hash, err := domain.CanonicalHash(*manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Hash = hash
}

func encodeCompleteRecord(t *testing.T, record Record) []byte {
	t.Helper()
	wire := completeBundleWire{
		SchemaVersion: record.Bundle.SchemaVersion, HandoffID: record.Bundle.HandoffID,
		Completion: record.Bundle.Completion, RevisionAuthority: record.Bundle.RevisionAuthority,
		OutputRevision: record.Bundle.OutputRevision, Workflow: record.Bundle.Workflow,
		Idempotent: new(bool),
	}
	*wire.Idempotent = record.Idempotent
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func encodeInspectRecord(t *testing.T, record Record) []byte {
	t.Helper()
	encoded, err := json.Marshal(record.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
