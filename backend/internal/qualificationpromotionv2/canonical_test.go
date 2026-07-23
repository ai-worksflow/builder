package qualificationpromotionv2

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCompileGoldenCanonicalGraphAndSevenDomains(t *testing.T) {
	record := compileTestRecord(t)
	if err := ValidateRecord(record); err != nil {
		t.Fatalf("ValidateRecord() error = %v", err)
	}
	wantRequest := `{"handoffId":"10000000-0000-4000-8000-000000000004","operationId":"10000000-0000-4000-8000-000000000001","outputRevisionId":"10000000-0000-4000-8000-000000000005","planAuthorityId":"10000000-0000-4000-8000-000000000003","schemaVersion":"worksflow-qualification-promotion-consume-request/v2","workflowInputAuthorityId":"10000000-0000-4000-8000-000000000002"}`
	if string(record.RequestBytes) != wantRequest {
		t.Fatalf("request bytes = %s", record.RequestBytes)
	}
	independentBytes := []byte(`{"schemaVersion":"worksflow-qualification-promotion-independent-authority-admission/v1"}`)
	vectors := map[string]string{
		"request":     record.RequestHash,
		"eventSet":    record.EvidenceEventSetHash,
		"closure":     record.ClosureHash,
		"intent":      record.RevisionIntentHash,
		"consumption": record.ConsumptionHash,
		"handoff":     record.HandoffHash,
		"independent": DomainHash(IndependentHashDomainV1, independentBytes),
	}
	want := map[string]string{
		"request":     "sha256:3ca334b7acad6fdf636b853c6013ba5556cef2f4ea77766ae75adb9225bbd761",
		"eventSet":    "sha256:f0809fef21708f2f2251741b43a8473754ad48a525082ee4959aba54630fe843",
		"closure":     "sha256:5bf9c316d501dc1fc0514e84de593796c3300e46b9dffa7a63f6416d010fbb30",
		"intent":      "sha256:17c35795ee074c9b8489f56ed98cfaab71a7e4664ab47fab6f75702bf65d5f14",
		"consumption": "sha256:ece9d86aa6a6333fcbcedd50dfcce967297780e6eaa2b0db9b4e6d4714c16f77",
		"handoff":     "sha256:a2c913b1f89c48462d17f61915dda93e8ab4691e23cf9e025a8ea9e94f45ebac",
		"independent": "sha256:ba9bd4293a256e49566c21a68a7ca66f9e979e753fdae44bb4f3e3dbc340c660",
	}
	for name, got := range vectors {
		if got != want[name] {
			t.Errorf("%s hash = %q, want %q", name, got, want[name])
		}
	}
	seen := map[string]struct{}{}
	for name, hash := range vectors {
		if _, duplicate := seen[hash]; duplicate {
			t.Errorf("domain hash %s aliases another domain", name)
		}
		seen[hash] = struct{}{}
	}
	seen = map[string]struct{}{}
	for _, domain := range []string{
		RequestHashDomainV2, ClosureHashDomainV2, ConsumptionHashDomainV2, HandoffHashDomainV2,
		RevisionIntentHashDomainV2, EvidenceEventSetHashDomainV2, IndependentHashDomainV1,
	} {
		hash := DomainHash(domain, []byte("{}"))
		if _, duplicate := seen[hash]; duplicate {
			t.Errorf("domain %q aliases another domain over identical bytes", domain)
		}
		seen[hash] = struct{}{}
	}
	if bytes.Contains(record.ConsumptionBytes, []byte("handoff")) {
		t.Fatal("consumption introduces a handoff hash cycle")
	}
	if bytes.Contains(record.RevisionIntentBytes, []byte("consumption")) || bytes.Contains(record.ClosureBytes, []byte("requestHash")) {
		t.Fatal("canonical DAG contains a back-edge")
	}
	if record.Consumption.ConsumedAt != record.Handoff.CreatedAt || !record.ConsumedAt.Equal(record.CreatedAt) {
		t.Fatal("consumption and handoff did not use one trusted timestamp")
	}
}

func TestClosedStoreBundleRoundTripAndExactInnerDocuments(t *testing.T) {
	record := compileTestRecord(t)
	bundle, err := StoreBundleFromRecord(record)
	if err != nil {
		t.Fatalf("StoreBundleFromRecord() error = %v", err)
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	decoded, err := DecodeStoreBundle(encoded)
	if err != nil || !SameImmutableRecord(record, decoded) {
		t.Fatalf("DecodeStoreBundle() error = %v, same = %v", err, SameImmutableRecord(record, decoded))
	}
	record.Idempotent = true
	consumeBundle, err := ConsumeStoreBundleFromRecord(record)
	if err != nil {
		t.Fatalf("ConsumeStoreBundleFromRecord() error = %v", err)
	}
	consumeBytes, _ := json.Marshal(consumeBundle)
	decoded, err = DecodeConsumeStoreBundle(consumeBytes)
	if err != nil || !decoded.Idempotent || !SameImmutableRecord(record, decoded) {
		t.Fatalf("DecodeConsumeStoreBundle() error = %v, idempotent = %v", err, decoded.Idempotent)
	}

	cases := map[string][]byte{
		"unknown":        bytes.Replace(encoded, []byte(`{"closure"`), []byte(`{"extra":true,"closure"`), 1),
		"duplicate":      bytes.Replace(encoded, []byte(`{"closure"`), []byte(`{"schemaVersion":"worksflow-qualification-promotion-store-bundle/v2","closure"`), 1),
		"upper hex":      bytes.Replace(encoded, []byte(bundle.Request.BytesHex[:2]), []byte(strings.ToUpper(bundle.Request.BytesHex[:2])), 1),
		"document drift": bytes.Replace(encoded, []byte(`"handoffId":"10000000-0000-4000-8000-000000000004"`), []byte(`"handoffId":"70000000-0000-4000-8000-000000000004"`), 1),
		"wrapper drift":  bytes.Replace(encoded, []byte(`"state":"pending"`), []byte(`"state":"complete"`), 1),
	}
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeStoreBundle(candidate); !errors.Is(err, ErrInvalid) {
				t.Fatalf("DecodeStoreBundle() error = %v", err)
			}
		})
	}
	if _, err := DecodeConsumeStoreBundle(encoded); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing explicit idempotent error = %v", err)
	}
}

func TestStoreBundleTransportUsesAggregateLimitNotSingleDocumentLimit(t *testing.T) {
	record := compileTestRecord(t)
	bundle, err := StoreBundleFromRecord(record)
	if err != nil {
		t.Fatalf("StoreBundleFromRecord() error = %v", err)
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if len(encoded) >= MaximumCanonicalBytes {
		t.Fatalf("fixture unexpectedly exceeds single-document limit: %d", len(encoded))
	}
	// Transport bundles contain multiple independently bounded canonical
	// documents. Whitespace is valid only at this outer JSON transport layer;
	// pad across 16 MiB to prove the reviewed 128 MiB aggregate bound is used.
	encoded = append(encoded, bytes.Repeat([]byte{' '}, MaximumCanonicalBytes-len(encoded)+1)...)
	decoded, err := DecodeStoreBundle(encoded)
	if err != nil || !SameImmutableRecord(record, decoded) {
		t.Fatalf("DecodeStoreBundle(%d bytes) error = %v, same = %v", len(encoded), err, SameImmutableRecord(record, decoded))
	}
}

func TestStrictDecodersRejectWidenedDuplicateNullAndNonCanonicalJSON(t *testing.T) {
	record := compileTestRecord(t)
	requestCases := map[string][]byte{
		"unknown":    bytes.Replace(record.RequestBytes, []byte(`{"handoffId"`), []byte(`{"extra":true,"handoffId"`), 1),
		"duplicate":  bytes.Replace(record.RequestBytes, []byte(`{"handoffId":"10000000-0000-4000-8000-000000000004"`), []byte(`{"handoffId":"10000000-0000-4000-8000-000000000004","handoffId":"10000000-0000-4000-8000-000000000004"`), 1),
		"whitespace": append([]byte(" "), record.RequestBytes...),
		"null":       []byte("null"),
		"trailing":   append(append([]byte(nil), record.RequestBytes...), []byte("{}")...),
	}
	for name, encoded := range requestCases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRequest(encoded, DomainHash(RequestHashDomainV2, encoded)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("DecodeRequest() error = %v", err)
			}
		})
	}
	if _, err := DecodeRequest(record.RequestBytes, testDigest("wrong")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("hash mismatch error = %v", err)
	}
	nullEvents := bytes.Replace(record.EvidenceEventSetBytes, []byte(`"events":[`), []byte(`"events":null,"unused":[`), 1)
	if _, err := DecodeEvidenceEventSet(nullEvents, DomainHash(EvidenceEventSetHashDomainV2, nullEvents)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("null/unknown event collection error = %v", err)
	}
	nullEvents = []byte(`{"events":null,"headVersion":3,"orchestrationId":"30000000-0000-4000-8000-000000000005","schemaVersion":"worksflow-qualification-promotion-evidence-event-set/v2"}`)
	if _, err := DecodeEvidenceEventSet(nullEvents, DomainHash(EvidenceEventSetHashDomainV2, nullEvents)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("null event collection error = %v", err)
	}
}

func TestCanonicalJSONRejectsInvalidUTF8AndUnsafeNumbers(t *testing.T) {
	if _, err := CanonicalJSON(map[string]string{"value": string([]byte{0xff})}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	for name, value := range map[string]any{
		"float":  1.25,
		"unsafe": MaximumJavaScriptSafeInt64 + 1,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalJSON(map[string]any{"value": value}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("CanonicalJSON() error = %v", err)
			}
		})
	}
}

func TestEvidenceEventSetRequiresExactContiguousBoundedLedger(t *testing.T) {
	valid := testPrepared().EvidenceEventSet
	cases := map[string]func(*EvidenceEventSet){
		"nil":       func(value *EvidenceEventSet) { value.Events = nil },
		"missing":   func(value *EvidenceEventSet) { value.Events = value.Events[:2] },
		"reordered": func(value *EvidenceEventSet) { value.Events[0], value.Events[1] = value.Events[1], value.Events[0] },
		"version":   func(value *EvidenceEventSet) { value.Events[1].Version = 3 },
		"duplicate": func(value *EvidenceEventSet) { value.Events[1].EventID = value.Events[0].EventID },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Events = cloneSlice(valid.Events)
			mutate(&candidate)
			if _, _, err := EncodeEvidenceEventSet(candidate); !errors.Is(err, ErrInvalid) {
				t.Fatalf("EncodeEvidenceEventSet() error = %v", err)
			}
		})
	}
	oversized := valid
	oversized.HeadVersion = MaximumEvidenceEvents + 1
	oversized.Events = make([]EvidenceEvent, MaximumEvidenceEvents+1)
	if _, _, err := EncodeEvidenceEventSet(oversized); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized event set error = %v", err)
	}
	maximum := EvidenceEventSet{
		Events: make([]EvidenceEvent, MaximumEvidenceEvents), HeadVersion: MaximumEvidenceEvents,
		OrchestrationID: valid.OrchestrationID, SchemaVersion: EvidenceEventSetSchemaV2,
	}
	for index := range maximum.Events {
		maximum.Events[index] = EvidenceEvent{
			EventHash: testDigest(fmt.Sprintf("maximum-event-%d", index+1)),
			EventID:   fmt.Sprintf("80000000-0000-4000-8000-%012x", index+1), Version: int64(index + 1),
		}
	}
	if _, _, err := EncodeEvidenceEventSet(maximum); err != nil {
		t.Fatalf("exact maximum EncodeEvidenceEventSet() error = %v", err)
	}
}

func TestClosureRequiresExplicitEmptyIndependentCollection(t *testing.T) {
	closure := compileTestRecord(t).Closure
	closure.IndependentAuthorities = nil
	if _, _, err := EncodeClosure(closure); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil independent collection error = %v", err)
	}
	closure.IndependentAuthorities = []IndependentAuthorityProjection{
		{Kind: IndependentProductionPostgreSQL, AuthorityID: "posture", AuthorityHash: testDigest("posture"), AdmissionRecordHash: testDigest("posture-admission"), SourceReceiptHash: testDigest("posture-source"), ReceiptSchemaVersion: "posture/v1"},
		{Kind: IndependentModelProfileActivation, AuthorityID: "model", AuthorityHash: testDigest("model"), AdmissionRecordHash: testDigest("model-admission"), SourceReceiptHash: testDigest("model-source"), ReceiptSchemaVersion: "model/v1"},
	}
	if _, _, err := EncodeClosure(closure); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-empty independent collection error = %v", err)
	}
}

func TestClosureRequiresTypedInputPrecommitMember(t *testing.T) {
	closure := compileTestRecord(t).Closure
	closure.InputPrecommit = InputPrecommitProjection{}
	if _, _, err := EncodeClosure(closure); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing input precommit error = %v", err)
	}
	closure = compileTestRecord(t).Closure
	closure.InputPrecommit.SourceAdmissionHash = closure.InputPrecommit.CredentialAdmissionHash
	if _, _, err := EncodeClosure(closure); !errors.Is(err, ErrInvalid) {
		t.Fatalf("aliased input precommit proof error = %v", err)
	}
}

func TestSecretScannerRejectsCredentialAndHostMaterial(t *testing.T) {
	record := compileTestRecord(t)
	values := []string{
		"postgres://builder:supersecret@db.example/app",
		"Authorization: Bearer abcdefghijklmnop",
		"Cookie: sessionid=abcdefghijklmnop",
		"X-API-Key: abcdefghijklmnop",
		"DATABASE_URL=postgres://db.internal/app",
		"-----BEGIN PRIVATE KEY-----",
		"/home/operator/.config/credentials",
		"sk-abcdefghijklmnopqrstuvwxyz012345",
		"eyJabcdefghijk.abcdefghijkl.abcdefghijkl",
	}
	for _, value := range values {
		t.Run(strings.ReplaceAll(value[:min(len(value), 12)], "/", "_"), func(t *testing.T) {
			handoff := record.Handoff
			handoff.Target.Subject = value
			if _, _, err := EncodeHandoff(handoff); !errors.Is(err, ErrInvalid) {
				t.Fatalf("EncodeHandoff(%q) error = %v", value, err)
			}
		})
	}
}
