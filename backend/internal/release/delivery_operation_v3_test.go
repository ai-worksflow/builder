package release

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	deliveryOperationTestID = "11111111-1111-4111-8111-111111111111"
	deliveryProjectTestID   = "22222222-2222-4222-8222-222222222222"
	deliveryHashA           = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	deliveryHashB           = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func deliveryControllerIdentityFixture() DeliveryControllerIdentity {
	return DeliveryControllerIdentity{
		SchemaVersion:  DeliveryControllerIdentitySchemaVersion,
		ID:             "delivery-controller",
		Version:        "2026.07.18+build.42",
		Protocol:       DeliveryControllerProtocolV3,
		TrustKeyDigest: deliveryHashA,
	}
}

func deliveryOperationRequestFixture(t *testing.T, kind DeliveryOperationKind) DeliveryOperationRequest {
	t.Helper()
	payload := map[string]any{
		"z":    "last",
		"a":    map[string]any{"z": 2, "a": true},
		"list": []any{"one", 2},
	}
	if kind == DeliveryOperationProduction {
		payload["expectedHead"] = ExpectedProductionHead{
			Revision: &repository.ExactReference{
				ID: "33333333-3333-4333-8333-333333333333", ContentHash: deliveryHashA,
			},
			ProductionReceipt: &repository.ExactReference{
				ID: "44444444-4444-4444-8444-444444444444", ContentHash: deliveryHashB,
			},
		}
	}
	request, err := NewDeliveryOperationRequest(deliveryOperationTestID, kind, deliveryProjectTestID, payload)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func hashDeliveryOperationResult(t *testing.T, result DeliveryOperationResult) DeliveryOperationResult {
	t.Helper()
	result.ResultHash = ""
	hash, err := domain.CanonicalHash(deliveryOperationResultHashPayload(result))
	if err != nil {
		t.Fatal(err)
	}
	result.ResultHash = "sha256:" + hash
	return result
}

func completedDeliveryOperationResultFixture(
	t *testing.T,
	request DeliveryOperationRequest,
) DeliveryOperationResult {
	t.Helper()
	result := DeliveryOperationResult{
		SchemaVersion: DeliveryOperationResultSchemaVersion,
		Controller:    deliveryControllerIdentityFixture(),
		OperationID:   request.OperationID,
		RequestHash:   request.RequestHash,
		Kind:          request.Kind,
		ProjectID:     request.ProjectID,
		Status:        DeliveryRemoteCompleted,
		Provider:      "kubernetes",
		ProviderRef:   "deployment/application@42",
		Checks: []PreviewCheck{
			{ID: "health", Kind: "health", Status: "passed"},
		},
		CompletedAt: time.Date(2026, 7, 18, 10, 11, 12, 123456000, time.UTC),
	}
	if request.Kind == DeliveryOperationProduction {
		result.PublicURL = "https://application.example.test"
		result.PreviousHead = &repository.ExactReference{
			ID: "33333333-3333-4333-8333-333333333333", ContentHash: deliveryHashA,
		}
	}
	return hashDeliveryOperationResult(t, result)
}

func rejectedDeliveryOperationResultFixture(
	t *testing.T,
	request DeliveryOperationRequest,
) DeliveryOperationResult {
	t.Helper()
	return hashDeliveryOperationResult(t, DeliveryOperationResult{
		SchemaVersion:   DeliveryOperationResultSchemaVersion,
		Controller:      deliveryControllerIdentityFixture(),
		OperationID:     request.OperationID,
		RequestHash:     request.RequestHash,
		Kind:            request.Kind,
		ProjectID:       request.ProjectID,
		Status:          DeliveryRemoteRejected,
		Checks:          []PreviewCheck{},
		NoMutation:      true,
		RejectionCode:   "policy-denied",
		RejectionDetail: "the exact release request did not satisfy the controller policy",
		CompletedAt:     time.Date(2026, 7, 18, 10, 11, 12, 123456000, time.UTC),
	})
}

func TestDeliveryOperationRequestIsCanonicalStableAndSelfAuthenticating(t *testing.T) {
	first := deliveryOperationRequestFixture(t, DeliveryOperationPreview)
	second, err := NewDeliveryOperationRequest(deliveryOperationTestID, DeliveryOperationPreview, deliveryProjectTestID, map[string]any{
		"list": []any{"one", 2},
		"a":    map[string]any{"a": true, "z": 2},
		"z":    "last",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.RequestHash != second.RequestHash || string(first.RequestDocument) != string(second.RequestDocument) {
		t.Fatalf("equivalent payloads were not canonical: first=%+v second=%+v", first, second)
	}
	const wantDocument = `{"kind":"preview","operationId":"11111111-1111-4111-8111-111111111111","payload":{"a":{"a":true,"z":2},"list":["one",2],"z":"last"},"projectId":"22222222-2222-4222-8222-222222222222","schemaVersion":"release-delivery-operation-document/v3"}`
	if string(first.RequestDocument) != wantDocument {
		t.Fatalf("unexpected canonical request document:\n got %s\nwant %s", first.RequestDocument, wantDocument)
	}
	parsed, err := ParseDeliveryOperationRequest(first)
	if err != nil || parsed.RequestHash != first.RequestHash {
		t.Fatalf("parse canonical request: parsed=%+v err=%v", parsed, err)
	}

	tampered := first
	tampered.RequestHash = deliveryHashB
	if _, err := ParseDeliveryOperationRequest(tampered); err == nil {
		t.Fatal("tampered request hash was accepted")
	}
	tampered = first
	tampered.Kind = DeliveryOperationProduction
	if _, err := ParseDeliveryOperationRequest(tampered); err == nil {
		t.Fatal("outer lineage drift was accepted")
	}
	tampered = first
	tampered.RequestDocument = append(json.RawMessage(nil), first.RequestDocument...)
	tampered.RequestDocument = append(tampered.RequestDocument[:1], append([]byte(" "), tampered.RequestDocument[1:]...)...)
	if _, err := ParseDeliveryOperationRequest(tampered); err == nil {
		t.Fatal("noncanonical request document was accepted")
	}
}

func TestDeliveryOperationRequestRejectsDuplicateOrUnknownJSONNames(t *testing.T) {
	request := deliveryOperationRequestFixture(t, DeliveryOperationPreview)
	if _, err := NewDeliveryOperationRequest(
		deliveryOperationTestID,
		DeliveryOperationPreview,
		deliveryProjectTestID,
		json.RawMessage(`{"nested":{"value":1,"value":2}}`),
	); err == nil {
		t.Fatal("request constructor silently collapsed a duplicate JSON payload name")
	}
	for name, document := range map[string]string{
		"duplicate document name":       `{"kind":"preview","kind":"production","operationId":"` + deliveryOperationTestID + `","payload":{"a":true},"projectId":"` + deliveryProjectTestID + `","schemaVersion":"` + DeliveryOperationDocumentSchemaVersion + `"}`,
		"duplicate nested payload name": `{"kind":"preview","operationId":"` + deliveryOperationTestID + `","payload":{"a":true,"a":false},"projectId":"` + deliveryProjectTestID + `","schemaVersion":"` + DeliveryOperationDocumentSchemaVersion + `"}`,
		"unknown document name":         `{"kind":"preview","operationId":"` + deliveryOperationTestID + `","payload":{"a":true},"projectId":"` + deliveryProjectTestID + `","schemaVersion":"` + DeliveryOperationDocumentSchemaVersion + `","unexpected":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			tampered := request
			tampered.RequestDocument = json.RawMessage(document)
			if _, err := ParseDeliveryOperationRequest(tampered); err == nil {
				t.Fatalf("unsafe JSON was accepted: %s", document)
			}
		})
	}
	if err := decodeReleaseStrictJSON([]byte(`{"value":{"nested":1,"nested":2}}`), &map[string]any{}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("strict decoder did not identify a nested duplicate name: %v", err)
	}
}

func TestDeliveryControllerIdentityRequiresExactAuthorityTuple(t *testing.T) {
	valid := deliveryControllerIdentityFixture()
	if parsed, err := ParseDeliveryControllerIdentity(valid); err != nil || parsed != valid {
		t.Fatalf("parse valid controller identity: parsed=%+v err=%v", parsed, err)
	}
	cases := map[string]func(*DeliveryControllerIdentity){
		"schema": func(value *DeliveryControllerIdentity) {
			value.SchemaVersion = "release-delivery-controller-identity/v2"
		},
		"id":       func(value *DeliveryControllerIdentity) { value.ID = "" },
		"version":  func(value *DeliveryControllerIdentity) { value.Version = "" },
		"protocol": func(value *DeliveryControllerIdentity) { value.Protocol = "worksflow.release-delivery/v2" },
		"key":      func(value *DeliveryControllerIdentity) { value.TrustKeyDigest = deliveryHashB[:70] },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			value := valid
			mutate(&value)
			if _, err := ParseDeliveryControllerIdentity(value); err == nil {
				t.Fatalf("controller identity drift %q was accepted", name)
			}
		})
	}
}

func TestDeliveryOperationCompletedAndRejectedResultsRequireExactHashAndLineage(t *testing.T) {
	preview := deliveryOperationRequestFixture(t, DeliveryOperationPreview)
	completed := completedDeliveryOperationResultFixture(t, preview)
	if parsed, err := ParseDeliveryOperationResult(completed, deliveryControllerIdentityFixture(), preview); err != nil || parsed.ResultHash != completed.ResultHash {
		t.Fatalf("parse completed result: parsed=%+v err=%v", parsed, err)
	}
	rejected := rejectedDeliveryOperationResultFixture(t, preview)
	if parsed, err := ParseDeliveryOperationResult(rejected, deliveryControllerIdentityFixture(), preview); err != nil || parsed.ResultHash != rejected.ResultHash {
		t.Fatalf("parse rejected result: parsed=%+v err=%v", parsed, err)
	}

	for name, mutate := range map[string]func(*DeliveryOperationResult){
		"result hash":  func(value *DeliveryOperationResult) { value.ResultHash = deliveryHashA },
		"controller":   func(value *DeliveryOperationResult) { value.Controller.Version = "other" },
		"operation":    func(value *DeliveryOperationResult) { value.OperationID = "55555555-5555-4555-8555-555555555555" },
		"request hash": func(value *DeliveryOperationResult) { value.RequestHash = deliveryHashA },
		"kind":         func(value *DeliveryOperationResult) { value.Kind = DeliveryOperationProduction },
		"project":      func(value *DeliveryOperationResult) { value.ProjectID = "55555555-5555-4555-8555-555555555555" },
		"status":       func(value *DeliveryOperationResult) { value.Status = DeliveryRemoteRunning },
		"time precision": func(value *DeliveryOperationResult) {
			value.CompletedAt = value.CompletedAt.Add(time.Nanosecond)
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := completed
			mutate(&value)
			if _, err := ParseDeliveryOperationResult(value, deliveryControllerIdentityFixture(), preview); err == nil {
				t.Fatalf("tampered result lineage %q was accepted", name)
			}
		})
	}

	nilChecks := rejected
	nilChecks.Checks = nil
	nilChecks = hashDeliveryOperationResult(t, nilChecks)
	if _, err := ParseDeliveryOperationResult(nilChecks, deliveryControllerIdentityFixture(), preview); err == nil {
		t.Fatal("rejected result without an explicit empty checks array was accepted")
	}
	mutated := rejected
	mutated.NoMutation = false
	mutated = hashDeliveryOperationResult(t, mutated)
	if _, err := ParseDeliveryOperationResult(mutated, deliveryControllerIdentityFixture(), preview); err == nil {
		t.Fatal("rejected result that could have mutated production was accepted")
	}

	for name, fixture := range map[string]DeliveryOperationResult{
		"provider": func() DeliveryOperationResult {
			value := completed
			value.Provider = " kubernetes"
			return hashDeliveryOperationResult(t, value)
		}(),
		"provider ref": func() DeliveryOperationResult {
			value := completed
			value.ProviderRef += " "
			return hashDeliveryOperationResult(t, value)
		}(),
		"rejection code": func() DeliveryOperationResult {
			value := rejected
			value.RejectionCode = " policy-denied"
			return hashDeliveryOperationResult(t, value)
		}(),
		"rejection detail": func() DeliveryOperationResult {
			value := rejected
			value.RejectionDetail += " "
			return hashDeliveryOperationResult(t, value)
		}(),
	} {
		t.Run("non-canonical "+name, func(t *testing.T) {
			if _, err := ParseDeliveryOperationResult(fixture, deliveryControllerIdentityFixture(), preview); err == nil {
				t.Fatalf("result with whitespace-padded %s was accepted", name)
			}
		})
	}
}

func TestDeliveryOperationProductionResultRequiresExactHeads(t *testing.T) {
	request := deliveryOperationRequestFixture(t, DeliveryOperationProduction)
	result := completedDeliveryOperationResultFixture(t, request)
	parsed, err := ParseDeliveryOperationResult(result, deliveryControllerIdentityFixture(), request)
	if err != nil || parsed.PreviousHead == nil || *parsed.PreviousHead != *result.PreviousHead {
		t.Fatalf("parse production heads: parsed=%+v err=%v", parsed, err)
	}
	publicURL := result
	publicURL.PublicURL += " "
	publicURL = hashDeliveryOperationResult(t, publicURL)
	if _, err := ParseDeliveryOperationResult(publicURL, deliveryControllerIdentityFixture(), request); err == nil {
		t.Fatal("production result with a whitespace-padded public URL was accepted")
	}

	for name, mutate := range map[string]func(*DeliveryOperationResult){
		"missing previous": func(value *DeliveryOperationResult) { value.PreviousHead = nil },
		"invalid previous id": func(value *DeliveryOperationResult) {
			value.PreviousHead = &repository.ExactReference{ID: "not-a-uuid", ContentHash: deliveryHashA}
		},
		"stale previous hash": func(value *DeliveryOperationResult) {
			value.PreviousHead = &repository.ExactReference{
				ID: "33333333-3333-4333-8333-333333333333", ContentHash: deliveryHashB,
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := result
			mutate(&value)
			value = hashDeliveryOperationResult(t, value)
			if _, err := ParseDeliveryOperationResult(value, deliveryControllerIdentityFixture(), request); err == nil {
				t.Fatalf("invalid production head %q was accepted", name)
			}
		})
	}

	previewRequest := deliveryOperationRequestFixture(t, DeliveryOperationPreview)
	previewResult := completedDeliveryOperationResultFixture(t, previewRequest)
	previewResult.PreviousHead = result.PreviousHead
	previewResult = hashDeliveryOperationResult(t, previewResult)
	if _, err := ParseDeliveryOperationResult(previewResult, deliveryControllerIdentityFixture(), previewRequest); err == nil {
		t.Fatal("preview result was allowed to mutate a production head")
	}
}

func TestDeliveryOperationObservationRequiresExactTerminalResult(t *testing.T) {
	request := deliveryOperationRequestFixture(t, DeliveryOperationPreview)
	observedAt := time.Date(2026, 7, 18, 10, 12, 0, 0, time.UTC)
	accepted := DeliveryOperationObservation{
		SchemaVersion: DeliveryOperationObservationSchema,
		Controller:    deliveryControllerIdentityFixture(),
		OperationID:   request.OperationID,
		RequestHash:   request.RequestHash,
		State:         DeliveryRemoteAccepted,
		Sequence:      1,
		ObservedAt:    observedAt,
	}
	if _, err := ParseDeliveryOperationObservation(accepted, deliveryControllerIdentityFixture(), request); err != nil {
		t.Fatalf("parse accepted observation: %v", err)
	}
	result := completedDeliveryOperationResultFixture(t, request)
	terminal := accepted
	terminal.State = DeliveryRemoteCompleted
	terminal.Sequence = 2
	terminal.Result = &result
	if _, err := ParseDeliveryOperationObservation(terminal, deliveryControllerIdentityFixture(), request); err != nil {
		t.Fatalf("parse terminal observation: %v", err)
	}

	terminal.Result = nil
	if _, err := ParseDeliveryOperationObservation(terminal, deliveryControllerIdentityFixture(), request); err == nil {
		t.Fatal("terminal observation without an exact result was accepted")
	}
	accepted.Result = &result
	if _, err := ParseDeliveryOperationObservation(accepted, deliveryControllerIdentityFixture(), request); err == nil {
		t.Fatal("nonterminal observation with a result was accepted")
	}
}
