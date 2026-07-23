package qualificationhandoff

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDecodeCompleteAndInspectBundles(t *testing.T) {
	record := testRecord(t)
	complete, err := DecodeCompleteBundle(encodeCompleteRecord(t, record))
	if err != nil || !SameImmutableRecord(complete, record) || complete.Idempotent != record.Idempotent {
		t.Fatalf("DecodeCompleteBundle() = %#v, %v", complete, err)
	}
	inspected, err := DecodeInspectBundle(encodeInspectRecord(t, record))
	if err != nil || !SameImmutableRecord(inspected, record) || !inspected.Idempotent {
		t.Fatalf("DecodeInspectBundle() = %#v, %v", inspected, err)
	}
	if _, err := DecodeInspectBundle(encodeCompleteRecord(t, record)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("inspect accepted idempotent extension: %v", err)
	}
}

func TestDecodeRejectsUnknownDuplicateMalformedAndMissingFields(t *testing.T) {
	record := testRecord(t)
	valid := encodeCompleteRecord(t, record)
	cases := map[string][]byte{
		"unknown":            bytes.Replace(valid, []byte(`{"schemaVersion"`), []byte(`{"unknown":true,"schemaVersion"`), 1),
		"duplicate":          bytes.Replace(valid, []byte(`{"schemaVersion"`), []byte(`{"schemaVersion":"wrong","schemaVersion"`), 1),
		"trailing":           append(append([]byte(nil), valid...), []byte(` {}`)...),
		"BOM":                append([]byte{0xef, 0xbb, 0xbf}, valid...),
		"UTF8":               append(append([]byte(nil), valid...), 0xff),
		"missing idempotent": encodeInspectRecord(t, record),
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCompleteBundle(encoded); !errors.Is(err, ErrInvalid) {
				t.Fatalf("DecodeCompleteBundle() error = %v", err)
			}
		})
	}
}

func TestDecodeRejectsOmittedRequiredNullableMembers(t *testing.T) {
	record := testRecord(t)
	for name, remove := range map[string]func(map[string]any){
		"authority Handoff state": func(root map[string]any) {
			authority := root["revisionAuthority"].(map[string]any)
			document := authority["document"].(map[string]any)
			state := document["revisionStateAtHandoff"].(map[string]any)
			delete(state, "supersededAt")
		},
		"output Handoff state": func(root map[string]any) {
			output := root["outputRevision"].(map[string]any)
			state := output["stateAtHandoff"].(map[string]any)
			delete(state, "supersededAt")
		},
	} {
		t.Run(name, func(t *testing.T) {
			var root map[string]any
			if err := json.Unmarshal(encodeCompleteRecord(t, record), &root); err != nil {
				t.Fatal(err)
			}
			remove(root)
			encoded, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeCompleteBundle(encoded); !errors.Is(err, ErrInvalid) {
				t.Fatalf("DecodeCompleteBundle() error = %v", err)
			}
		})
	}
}

func TestDecodeRejectsNonExactCopiedLineageSchema(t *testing.T) {
	record := testRecord(t)
	record.Bundle.RevisionAuthority.Document.CopiedLineage.SchemaVersion =
		"worksflow-qualification-handoff-copied-lineage/v2"
	record.Bundle.RevisionAuthority = retainedAuthority(t, record.Bundle.RevisionAuthority.Document)
	if _, err := DecodeCompleteBundle(encodeCompleteRecord(t, record)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("DecodeCompleteBundle() error = %v", err)
	}
}

func TestDecodeRejectsCrossProjectionScalarDriftWithValidRetainedHashes(t *testing.T) {
	for name, mutate := range map[string]func(*Record){
		"operation": func(record *Record) {
			record.Bundle.RevisionAuthority.Document.OperationID = testUUID(50)
		},
		"consumption": func(record *Record) {
			record.Bundle.RevisionAuthority.Document.Promotion.ConsumptionHash = testDigest('8')
		},
		"project": func(record *Record) {
			record.Bundle.Completion.Document.ProjectID = testUUID(51)
		},
		"run": func(record *Record) {
			record.Bundle.RevisionAuthority.Document.Target.WorkflowRunID = testUUID(52)
		},
		"gate node": func(record *Record) {
			record.Bundle.Completion.Document.NodeRunID = testUUID(53)
		},
		"publish node": func(record *Record) {
			record.Bundle.Completion.Document.PublishNodeRunID = testUUID(54)
		},
		"parent Revision": func(record *Record) {
			record.Bundle.OutputRevision.ParentRevisionID = testUUID(55)
		},
		"self parent Revision": func(record *Record) {
			record.Bundle.OutputRevision.ParentRevisionID = record.Bundle.OutputRevision.ID
			record.Bundle.RevisionAuthority.Document.Target.RevisionID = record.Bundle.OutputRevision.ID
			record.Bundle.Workflow.QualityResult.Findings.WorkspaceRevision.RevisionID = record.Bundle.OutputRevision.ID
		},
		"output content": func(record *Record) {
			record.Bundle.Completion.Document.OutputRevisionContentHash = testDigest('7')
		},
		"completion time": func(record *Record) {
			record.Bundle.Completion.Document.CompletedAt = "2026-07-20T12:30:46.123Z"
		},
		"quality output": func(record *Record) {
			record.Bundle.Workflow.QualityResult.WorkspaceRevision.ContentHash = testDigest('6')
		},
		"quality parent": func(record *Record) {
			record.Bundle.Workflow.QualityResult.Findings.WorkspaceRevision.RevisionID = testUUID(56)
		},
		"manifest project": func(record *Record) {
			record.Bundle.Workflow.QualityResult.BuildManifest.ProjectID = testUUID(57)
		},
	} {
		t.Run(name, func(t *testing.T) {
			record := testRecord(t)
			mutate(&record)
			refreshBuildManifestHash(t, &record)
			record.Bundle.Completion = retainedCompletion(t, record.Bundle.Completion.Document)
			record.Bundle.RevisionAuthority = retainedAuthority(t, record.Bundle.RevisionAuthority.Document)
			if _, err := DecodeCompleteBundle(encodeCompleteRecord(t, record)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("DecodeCompleteBundle() error = %v", err)
			}
		})
	}
}

func TestDecodeRejectsRetainedTamperAndCrossProjectionDrift(t *testing.T) {
	for name, mutate := range map[string]func(*Record){
		"completion bytes": func(record *Record) {
			record.Bundle.Completion.BytesHex = "7b7d"
		},
		"completion hash": func(record *Record) {
			record.Bundle.Completion.Hash = testDigest('9')
		},
		"completion document": func(record *Record) {
			record.Bundle.Completion.Document.ProjectID = testUUID(30)
		},
		"authority bytes": func(record *Record) {
			record.Bundle.RevisionAuthority.BytesHex = "7b7d"
		},
		"authority hash": func(record *Record) {
			record.Bundle.RevisionAuthority.Hash = testDigest('9')
		},
		"output identity": func(record *Record) {
			record.Bundle.OutputRevision.ID = testUUID(31)
		},
		"event cursor": func(record *Record) {
			record.Bundle.Workflow.EventCursorAfter++
		},
		"quality output": func(record *Record) {
			record.Bundle.Workflow.QualityResult.WorkspaceRevision.RevisionID = testUUID(32)
		},
		"Handoff live status": func(record *Record) {
			record.Bundle.OutputRevision.StateAtHandoff.WorkflowStatus = "superseded"
		},
		"BuildManifest hash": func(record *Record) {
			record.Bundle.Workflow.QualityResult.BuildManifest.Hash = strings.Repeat("9", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			record := testRecord(t)
			mutate(&record)
			if _, err := DecodeCompleteBundle(encodeCompleteRecord(t, record)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("DecodeCompleteBundle() error = %v", err)
			}
		})
	}
}

func TestDecodeRejectsUnknownNestedFieldsAndSecretMaterial(t *testing.T) {
	record := testRecord(t)
	encoded := encodeCompleteRecord(t, record)
	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatal(err)
	}
	workflow := generic["workflow"].(map[string]any)
	workflow["unexpected"] = true
	unknown, _ := json.Marshal(generic)
	if _, err := DecodeCompleteBundle(unknown); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown nested field error = %v", err)
	}

	record = testRecord(t)
	record.Bundle.Workflow.QualityResult.BuildManifest.Constraints = json.RawMessage(
		`{"apiKey":"sk-1234567890abcdefghijklmnop"}`,
	)
	if _, err := DecodeCompleteBundle(encodeCompleteRecord(t, record)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("secret material error = %v", err)
	}
}

func TestHandoffDomainHashFramingIsUnambiguous(t *testing.T) {
	left := HandoffDomainHash("ab", []byte("c"))
	right := HandoffDomainHash("a", []byte("bc"))
	if left == right || !strings.HasPrefix(left, "sha256:") || len(left) != 71 {
		t.Fatalf("unexpected framed hashes: %q %q", left, right)
	}
}
