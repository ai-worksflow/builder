package qualificationreceiptv3

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCompileDeterministicCanonicalClosure(t *testing.T) {
	receipt := validReceipt(t)
	first, err := Compile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compile(cloneReceipt(t, receipt))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Payload, second.Payload) || first.PayloadDigest != second.PayloadDigest {
		t.Fatal("the same Receipt did not compile to deterministic payload bytes")
	}
	if first.PayloadDigest != SHA256Digest(first.Payload) {
		t.Fatalf("PayloadDigest = %s, want exact decoded payload digest", first.PayloadDigest)
	}
	if first.PayloadDigest == receipt.PlanAuthority.ProjectionHash || first.PayloadDigest == receipt.PlanAuthority.EvidencePlanHash {
		t.Fatal("PayloadDigest was confused with a Plan projection or evidence Plan digest")
	}
	if first.SubjectName != receipt.Snapshot.SnapshotID || first.SubjectDigest != receipt.Snapshot.SnapshotDigest {
		t.Fatalf("subject = %s/%s", first.SubjectName, first.SubjectDigest)
	}
	decoded, err := DecodePayload(first.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, receipt) {
		t.Fatal("decoded canonical Receipt drifted from input")
	}
	if got, want := first.PayloadDigest, "sha256:3df9f7d9bced54731e0163c0b6d7cda9f1fae7ef79b2c4017e406d53e767b326"; got != want {
		t.Fatalf("payload vector = %s, want %s", got, want)
	}
	if got, want := receipt.PlanAuthority.AuthorityHash, "sha256:c1da69c0e70f31a25d609f5720fec5b4321d181384d045a6bc053fd759f75596"; got != want {
		t.Fatalf("authority vector = %s, want %s", got, want)
	}
}

func TestReceiptDomainTamperMatrixFailsClosed(t *testing.T) {
	base := validReceipt(t)
	tests := []struct {
		name   string
		mutate func(*Receipt)
	}{
		{name: "authority UUID", mutate: func(value *Receipt) { value.PlanAuthority.AuthorityID = testProjectID }},
		{name: "authority hash", mutate: func(value *Receipt) { value.PlanAuthority.AuthorityHash = testDigest("other-authority") }},
		{name: "authority artifact", mutate: func(value *Receipt) { value.PlanAuthority.ArtifactID = "qualification-plan-other" }},
		{name: "input authority", mutate: func(value *Receipt) { value.PlanAuthority.InputAuthorityID = testProjectID }},
		{name: "input hash", mutate: func(value *Receipt) { value.PlanAuthority.InputHash = testDigest("other-input") }},
		{name: "projection hash", mutate: func(value *Receipt) { value.PlanAuthority.ProjectionHash = testDigest("other-projection") }},
		{name: "PlanDigest", mutate: func(value *Receipt) { value.PlanAuthority.PlanDigest = testDigest("other-plan") }},
		{name: "evidence Plan hash", mutate: func(value *Receipt) { value.PlanAuthority.EvidencePlanHash = testDigest("other-evidence-plan") }},
		{name: "target hash", mutate: func(value *Receipt) { value.PlanAuthority.TargetHash = testDigest("other-target") }},
		{name: "trust hash", mutate: func(value *Receipt) { value.PlanAuthority.TrustHash = testDigest("other-trust") }},
		{name: "trust bindings digest", mutate: func(value *Receipt) { value.PlanAuthority.TrustBindingsDigest = testDigest("other-trust-bindings") }},
		{name: "embedded evidence Plan", mutate: func(value *Receipt) { value.EvidencePlan.RunID = testWorkflowRunID }},
		{name: "target project", mutate: func(value *Receipt) { value.Target.PromotionTarget.ProjectID = testRunID }},
		{name: "source tree", mutate: func(value *Receipt) { value.Source.TreeDigest = testDigest("other-tree") }},
		{name: "dirty source", mutate: func(value *Receipt) { value.Source.Dirty = true }},
		{name: "template release", mutate: func(value *Receipt) { value.TemplateRelease.ContentHash = testDigest("other-template") }},
		{name: "build manifest", mutate: func(value *Receipt) { value.Build.Manifest.ContentHash = value.Build.Contract.ContentHash }},
		{name: "qualification manifest artifact", mutate: func(value *Receipt) { value.QualificationManifest.ArtifactID = "" }},
		{name: "qualification manifest revision", mutate: func(value *Receipt) { value.QualificationManifest.RevisionID = "00000000-0000-0000-0000-000000000000" }},
		{name: "qualification manifest content", mutate: func(value *Receipt) { value.QualificationManifest.ContentHash = value.PlanAuthority.PlanDigest }},
		{name: "Golden authority", mutate: func(value *Receipt) {
			value.GoldenRuntime.AuthorityDocumentDigest = value.GoldenRuntime.FixtureDocumentDigest
		}},
		{name: "Golden authority absent from Plan", mutate: func(value *Receipt) {
			value.GoldenRuntime.AuthorityDocumentArtifactID = "unplanned-golden-authority"
		}},
		{name: "Golden fixture", mutate: func(value *Receipt) { value.GoldenRuntime.FixtureID = testRunID }},
		{name: "credential set", mutate: func(value *Receipt) { value.CredentialSet.SetHandleHash = testDigest("other-set") }},
		{name: "credential revocation", mutate: func(value *Receipt) {
			value.CredentialSet.Revocation.ContentDigest = value.CredentialSet.Issuance.ContentDigest
		}},
		{name: "credential time", mutate: func(value *Receipt) { value.CredentialSet.RevokedAt = value.CredentialSet.ExpiresAt }},
		{name: "evidence closure", mutate: func(value *Receipt) { value.Evidence.ClosureDigest = testDigest("other-closure") }},
		{name: "artifact set", mutate: func(value *Receipt) { value.Evidence.ArtifactSetDigest = testDigest("other-artifact-set") }},
		{name: "evidence artifact omission", mutate: func(value *Receipt) { value.Evidence.ArtifactIDs = value.Evidence.ArtifactIDs[1:] }},
		{name: "evidence artifact order", mutate: func(value *Receipt) {
			value.Evidence.ArtifactIDs[0], value.Evidence.ArtifactIDs[1] = value.Evidence.ArtifactIDs[1], value.Evidence.ArtifactIDs[0]
		}},
		{name: "restricted artifact duplication", mutate: func(value *Receipt) {
			value.Evidence.RestrictedArtifactIDs[1] = value.Evidence.RestrictedArtifactIDs[0]
		}},
		{name: "artifact index authority", mutate: func(value *Receipt) { value.ArtifactIndex.AuthorityID = value.Trust.TrustBindings.SealerAuthorityID }},
		{name: "artifact index digest", mutate: func(value *Receipt) { value.ArtifactIndex.ContentDigest = testDigest("other-index") }},
		{name: "artifact index count", mutate: func(value *Receipt) { value.ArtifactIndex.ArtifactCount++ }},
		{name: "artifact descriptor digest", mutate: func(value *Receipt) {
			for index := range value.ArtifactIndex.Artifacts {
				if value.ArtifactIndex.Artifacts[index].ID == value.GoldenRuntime.AuthorityDocumentArtifactID {
					value.ArtifactIndex.Artifacts[index].ContentDigest = testDigest("other-golden-content")
				}
			}
		}},
		{name: "artifact descriptor omission", mutate: func(value *Receipt) {
			value.ArtifactIndex.Artifacts = value.ArtifactIndex.Artifacts[1:]
		}},
		{name: "snapshot includes wrong index", mutate: func(value *Receipt) { value.Snapshot.ArtifactIndexDigest = testDigest("other-index") }},
		{name: "snapshot digest", mutate: func(value *Receipt) { value.Snapshot.SnapshotDigest = testDigest("other-snapshot") }},
		{name: "snapshot authority", mutate: func(value *Receipt) { value.Snapshot.AuthorityID = value.Trust.TrustBindings.VerifierAuthorityID }},
		{name: "verification authority", mutate: func(value *Receipt) { value.SnapshotVerification.AuthorityID = value.Snapshot.AuthorityID }},
		{name: "verification snapshot", mutate: func(value *Receipt) { value.SnapshotVerification.SnapshotDigest = testDigest("other-snapshot") }},
		{name: "verification result", mutate: func(value *Receipt) { value.SnapshotVerification.Result = "passed" }},
		{name: "runner role", mutate: func(value *Receipt) { value.Signers.Runner.Role = SignerRoleApprover }},
		{name: "signer identity alias", mutate: func(value *Receipt) { value.Signers.Approver.Identity = value.Signers.Runner.Identity }},
		{name: "signer operational identity alias", mutate: func(value *Receipt) {
			value.Signers.Runner.Identity = value.Trust.TrustBindings.SealerAuthorityID
		}},
		{name: "receipt operation", mutate: func(value *Receipt) { value.OperationID = value.EvidencePlan.Operations.SnapshotSeal }},
		{name: "receipt output", mutate: func(value *Receipt) { value.ReceiptID = value.EvidencePlan.Outputs.SnapshotID }},
		{name: "qualification start semantics", mutate: func(value *Receipt) { value.QualificationStartedAt = value.Snapshot.SealedAt }},
		{name: "credential issued at qualification start", mutate: func(value *Receipt) {
			value.CredentialSet.IssuedAt = value.QualificationStartedAt
		}},
		{name: "credential revoked at qualification start", mutate: func(value *Receipt) {
			value.CredentialSet.RevokedAt = value.QualificationStartedAt
		}},
		{name: "credential revoked at index commit", mutate: func(value *Receipt) {
			value.CredentialSet.RevokedAt = value.ArtifactIndex.CommittedAt
		}},
		{name: "index committed after seal", mutate: func(value *Receipt) {
			value.ArtifactIndex.CommittedAt = "2026-07-19T12:02:00.001Z"
		}},
		{name: "snapshot before credential revoke", mutate: func(value *Receipt) { value.Snapshot.SealedAt = "2026-07-19T12:00:30.000Z" }},
		{name: "verification before seal", mutate: func(value *Receipt) { value.SnapshotVerification.VerifiedAt = "2026-07-19T12:01:00.000Z" }},
		{name: "issue before complete", mutate: func(value *Receipt) { value.IssuedAt = "2026-07-19T12:03:30.000Z" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneReceipt(t, base)
			test.mutate(&candidate)
			if _, err := Compile(candidate); !errors.Is(err, ErrInvalid) {
				t.Fatalf("tampered Receipt error = %v", err)
			}
		})
	}
}

func TestStrictPayloadShapeAndCanonicalBytes(t *testing.T) {
	compiled, err := Compile(validReceipt(t))
	if err != nil {
		t.Fatal(err)
	}
	duplicate := bytes.Replace(compiled.Payload,
		[]byte(`"schemaVersion":"`+ReceiptSchemaV3+`"`),
		[]byte(`"schemaVersion":"`+ReceiptSchemaV3+`","schemaVersion":"`+ReceiptSchemaV3+`"`), 1)
	tests := []struct {
		name    string
		payload func() []byte
	}{
		{name: "leading whitespace", payload: func() []byte { return append([]byte(" "), compiled.Payload...) }},
		{name: "trailing whitespace", payload: func() []byte { return append(append([]byte(nil), compiled.Payload...), ' ') }},
		{name: "trailing document", payload: func() []byte { return append(append([]byte(nil), compiled.Payload...), []byte(`{}`)...) }},
		{name: "duplicate predicate name", payload: func() []byte { return duplicate }},
		{name: "BOM", payload: func() []byte { return append([]byte{0xef, 0xbb, 0xbf}, compiled.Payload...) }},
		{name: "invalid UTF-8", payload: func() []byte { return append(append([]byte(nil), compiled.Payload...), 0xff) }},
		{name: "unknown statement field", payload: func() []byte {
			value := payloadMap(t, compiled.Payload)
			value["metadata"] = map[string]any{"reviewed": true}
			return canonicalMap(t, value)
		}},
		{name: "unknown predicate field", payload: func() []byte {
			value := payloadMap(t, compiled.Payload)
			nestedMap(t, value, "predicate")["metadata"] = map[string]any{"reviewed": true}
			return canonicalMap(t, value)
		}},
		{name: "snapshot receipt self reference", payload: func() []byte {
			value := payloadMap(t, compiled.Payload)
			nestedMap(t, value, "predicate", "snapshot")["receiptDigest"] = testDigest("receipt")
			return canonicalMap(t, value)
		}},
		{name: "snapshot receipt ID self reference", payload: func() []byte {
			value := payloadMap(t, compiled.Payload)
			nestedMap(t, value, "predicate", "snapshot")["receiptId"] = "qualification-receipt"
			return canonicalMap(t, value)
		}},
		{name: "missing explicit false field", payload: func() []byte {
			value := payloadMap(t, compiled.Payload)
			delete(nestedMap(t, value, "predicate", "source"), "dirty")
			return canonicalMap(t, value)
		}},
		{name: "null artifact array", payload: func() []byte {
			value := payloadMap(t, compiled.Payload)
			nestedMap(t, value, "predicate", "evidence")["artifactIds"] = nil
			return canonicalMap(t, value)
		}},
		{name: "empty subject array", payload: func() []byte {
			value := payloadMap(t, compiled.Payload)
			value["subject"] = []any{}
			return canonicalMap(t, value)
		}},
		{name: "multiple subjects", payload: func() []byte {
			value := payloadMap(t, compiled.Payload)
			subjects := value["subject"].([]any)
			value["subject"] = append(subjects, subjects[0])
			return canonicalMap(t, value)
		}},
		{name: "unknown evidence Plan field", payload: func() []byte {
			value := payloadMap(t, compiled.Payload)
			nestedMap(t, value, "predicate", "evidencePlan")["retryReason"] = "operator supplied"
			return canonicalMap(t, value)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodePayload(test.payload()); !errors.Is(err, ErrInvalid) {
				t.Fatalf("DecodePayload() error = %v", err)
			}
		})
	}
}

func TestPreReceiptSnapshotTypeCannotRepresentReceiptReference(t *testing.T) {
	typeOfSnapshot := reflect.TypeOf(PreReceiptSnapshotBinding{})
	for index := 0; index < typeOfSnapshot.NumField(); index++ {
		field := typeOfSnapshot.Field(index)
		if strings.Contains(strings.ToLower(field.Name), "receipt") || strings.Contains(strings.ToLower(field.Tag.Get("json")), "receipt") {
			t.Fatalf("pre-Receipt snapshot acquired self-referential field %s", field.Name)
		}
	}
	encoded, err := json.Marshal(validReceipt(t).Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"receipt", "receiptId", "receiptDigest", "receiptPayloadDigest", "receiptEnvelopeDigest"} {
		if _, exists := object[forbidden]; exists {
			t.Fatalf("pre-Receipt snapshot JSON contains forbidden field %q: %s", forbidden, encoded)
		}
	}
}
