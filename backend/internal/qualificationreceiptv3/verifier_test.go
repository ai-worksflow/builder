package qualificationreceiptv3

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

type typedNilContext struct{}

func (*typedNilContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*typedNilContext) Done() <-chan struct{}       { return nil }
func (*typedNilContext) Err() error                  { return nil }
func (*typedNilContext) Value(any) any               { return nil }

func TestVerifierAcceptsExactTwoRoleCanonicalEnvelope(t *testing.T) {
	receipt := validReceipt(t)
	compiled, err := Compile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	envelope := signCompiled(t, compiled)
	verifier, resolver := verifierForReceipt(t, receipt)
	verified, err := verifier.Verify(context.Background(), envelope, receipt.PlanAuthority.AuthorityID, receipt.ReceiptID)
	if err != nil {
		t.Fatal(err)
	}
	if verified.PayloadDigest != SHA256Digest(compiled.Payload) || verified.PayloadDigest != compiled.PayloadDigest {
		t.Fatalf("PayloadDigest = %s", verified.PayloadDigest)
	}
	if verified.PayloadDigest == receipt.PlanAuthority.ProjectionHash {
		t.Fatal("DSSE PayloadDigest was copied from ProjectionHash")
	}
	if verified.SubjectName != receipt.Snapshot.SnapshotID || verified.SubjectDigest != receipt.Snapshot.SnapshotDigest {
		t.Fatalf("verified subject = %s/%s", verified.SubjectName, verified.SubjectDigest)
	}
	if verified.Runner.Role != SignerRoleRunner || verified.Runner.KeyID != receipt.Signers.Runner.KeyID ||
		verified.Approver.Role != SignerRoleApprover || verified.Approver.KeyID != receipt.Signers.Approver.KeyID ||
		verified.Runner.Identity == verified.Approver.Identity || verified.Runner.KeyID == verified.Approver.KeyID {
		t.Fatalf("role-separated signers = runner:%+v approver:%+v", verified.Runner, verified.Approver)
	}
	if !bytes.Equal(verified.CanonicalEnvelope, envelope) || verified.EnvelopeDigest != SHA256Digest(envelope) {
		t.Fatal("verified envelope bytes or digest drifted")
	}
	if got, want := verified.EnvelopeDigest, "sha256:b1aa02f02d8d3107f4a0ff2f1d159d760376c0f5b6d80aeddb4aa7ac77fcf57d"; got != want {
		t.Fatalf("envelope vector = %s, want %s", got, want)
	}
	if got, want := verified.PayloadDigest, "sha256:3df9f7d9bced54731e0163c0b6d7cda9f1fae7ef79b2c4017e406d53e767b326"; got != want {
		t.Fatalf("payload vector = %s, want %s", got, want)
	}
	if resolver.callCount() != 1 {
		t.Fatalf("expected resolver calls = %d, want 1", resolver.callCount())
	}
}

func TestVerifierRequiresServerResolvedExactExpectedReceipt(t *testing.T) {
	expected := validReceipt(t)
	candidate := cloneReceipt(t, expected)
	candidate.Build.Manifest.ContentHash = testDigest("different-but-valid-build-manifest")
	candidate.QualificationManifest.ContentHash = testDigest("different-but-valid-qualification-manifest")
	candidate.PlanAuthority.InputHash = testDigest("different-server-resolved-input")
	refreshAuthority(t, &candidate)
	compiled, err := Compile(candidate)
	if err != nil {
		t.Fatalf("alternative internally closed Receipt is not valid: %v", err)
	}
	verifier, resolver := verifierForReceipt(t, expected)
	if _, err := verifier.Verify(context.Background(), signCompiled(t, compiled), expected.PlanAuthority.AuthorityID, expected.ReceiptID); !errors.Is(err, ErrVerification) {
		t.Fatalf("cryptographically valid arbitrary upstream bindings error = %v", err)
	}
	if resolver.callCount() != 1 {
		t.Fatalf("trusted resolver calls = %d, want 1", resolver.callCount())
	}
	candidateVerifier, _ := verifierForReceipt(t, candidate)
	if _, err := candidateVerifier.Verify(context.Background(), signCompiled(t, compiled), candidate.PlanAuthority.AuthorityID, candidate.ReceiptID); err != nil {
		t.Fatalf("same envelope with exact trusted expected payload failed: %v", err)
	}
}

func TestSignedBindingTamperMatrixFailsClosed(t *testing.T) {
	expected := validReceipt(t)
	compiled, err := Compile(expected)
	if err != nil {
		t.Fatal(err)
	}
	verifier, _ := verifierForReceipt(t, expected)
	runner, approver := testKeys()
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "authority ID", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "planAuthority")["authorityId"] = testProjectID
		}},
		{name: "authority hash", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "planAuthority")["authorityHash"] = testDigest("signed-tamper-authority")
		}},
		{name: "authority artifact", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "planAuthority")["artifactId"] = "qualification-plan-tampered"
		}},
		{name: "input hash", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "planAuthority")["inputHash"] = testDigest("signed-tamper-input")
		}},
		{name: "projection hash", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "planAuthority")["projectionHash"] = testDigest("signed-tamper-projection")
		}},
		{name: "PlanDigest", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "planAuthority")["planDigest"] = testDigest("signed-tamper-plan")
		}},
		{name: "evidence Plan hash", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "planAuthority")["evidencePlanHash"] = testDigest("signed-tamper-evidence-plan")
		}},
		{name: "target hash", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "planAuthority")["targetHash"] = testDigest("signed-tamper-target")
		}},
		{name: "trust bindings digest", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "planAuthority")["trustBindingsDigest"] = testDigest("signed-tamper-trust")
		}},
		{name: "source", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "source")["commit"] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}},
		{name: "template", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "templateRelease")["contentHash"] = testDigest("signed-tamper-template")
		}},
		{name: "build", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "build", "contract")["contentHash"] = testDigest("signed-tamper-build")
		}},
		{name: "qualification manifest artifact", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "qualificationManifest")["artifactId"] = "tampered-manifest"
		}},
		{name: "qualification manifest revision", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "qualificationManifest")["revisionId"] = testProjectID
		}},
		{name: "qualification manifest content", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "qualificationManifest")["contentHash"] = testDigest("signed-tamper-manifest")
		}},
		{name: "Golden runtime", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "goldenRuntime")["authorityDocumentDigest"] = testDigest("signed-tamper-golden")
		}},
		{name: "credential", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "credentialSet", "issuance")["contentDigest"] = testDigest("signed-tamper-credential")
		}},
		{name: "evidence closure", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "evidence")["resultDigest"] = testDigest("signed-tamper-result")
		}},
		{name: "artifact index", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "artifactIndex")["requestDigest"] = testDigest("signed-tamper-index-request")
		}},
		{name: "pre-Receipt snapshot", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "snapshot")["requestDigest"] = testDigest("signed-tamper-snapshot-request")
		}},
		{name: "independent verification", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "snapshotVerification")["verifiedAt"] = "2026-07-19T12:03:01.000Z"
		}},
		{name: "signer binding", mutate: func(root map[string]any) {
			nestedMap(t, root, "predicate", "signers", "runner")["identity"] = "spiffe://worksflow.dev/qualification/other-runner"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := payloadMap(t, compiled.Payload)
			test.mutate(value)
			tamperedPayload := canonicalMap(t, value)
			tamperedEnvelope := signPayload(t, tamperedPayload, runner, approver)
			if _, err := verifier.Verify(context.Background(), tamperedEnvelope, expected.PlanAuthority.AuthorityID, expected.ReceiptID); !errors.Is(err, ErrVerification) {
				t.Fatalf("signed tamper error = %v", err)
			}
		})
	}
}

func TestDSSEEnvelopeAndPayloadTamperingFailsClosed(t *testing.T) {
	receipt := validReceipt(t)
	compiled, err := Compile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	verifier, _ := verifierForReceipt(t, receipt)
	runner, approver := testKeys()
	validEnvelope := signCompiled(t, compiled)
	var decoded testEnvelope
	if err := json.Unmarshal(validEnvelope, &decoded); err != nil {
		t.Fatal(err)
	}

	badSignature := decoded
	badSignature.Signatures = append([]testSignature(nil), decoded.Signatures...)
	rawSignature, err := base64.StdEncoding.DecodeString(badSignature.Signatures[0].Sig)
	if err != nil {
		t.Fatal(err)
	}
	rawSignature[0] ^= 0xff
	badSignature.Signatures[0].Sig = base64.StdEncoding.EncodeToString(rawSignature)
	badSignatureJSON, _ := json.Marshal(badSignature)

	reversed := decoded
	reversed.Signatures = append([]testSignature(nil), decoded.Signatures...)
	reversed.Signatures[0], reversed.Signatures[1] = reversed.Signatures[1], reversed.Signatures[0]
	reversedJSON, _ := json.Marshal(reversed)

	nonCanonicalPayload := append(append([]byte(nil), compiled.Payload...), ' ')
	unknownPredicate := payloadMap(t, compiled.Payload)
	nestedMap(t, unknownPredicate, "predicate")["metadata"] = "not-allowed"

	unknownEnvelope := map[string]any{}
	if err := json.Unmarshal(validEnvelope, &unknownEnvelope); err != nil {
		t.Fatal(err)
	}
	unknownEnvelope["trusted"] = true
	unknownEnvelopeJSON, _ := json.Marshal(unknownEnvelope)

	nonCanonicalBase64 := decoded
	nonCanonicalBase64.Payload = nonCanonicalBase64.Payload[:len(nonCanonicalBase64.Payload)-1]
	nonCanonicalBase64JSON, _ := json.Marshal(nonCanonicalBase64)

	tests := []struct {
		name     string
		envelope []byte
	}{
		{name: "runner only", envelope: signPayload(t, compiled.Payload, runner)},
		{name: "approver only", envelope: signPayload(t, compiled.Payload, approver)},
		{name: "duplicate runner key", envelope: signPayload(t, compiled.Payload, runner, runner)},
		{name: "invalid signature", envelope: badSignatureJSON},
		{name: "non-canonical signature order", envelope: reversedJSON},
		{name: "envelope whitespace", envelope: append(append([]byte(nil), validEnvelope...), ' ')},
		{name: "unknown envelope field", envelope: unknownEnvelopeJSON},
		{name: "non-canonical payload base64", envelope: nonCanonicalBase64JSON},
		{name: "signed non-canonical payload", envelope: signPayload(t, nonCanonicalPayload, runner, approver)},
		{name: "signed unknown predicate field", envelope: signPayload(t, canonicalMap(t, unknownPredicate), runner, approver)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := verifier.Verify(context.Background(), test.envelope, receipt.PlanAuthority.AuthorityID, receipt.ReceiptID); !errors.Is(err, ErrVerification) {
				t.Fatalf("tampered DSSE error = %v", err)
			}
		})
	}
}

func TestVerifierPolicyRequiresIndependentRunnerAndApprover(t *testing.T) {
	runner, approver := testKeys()
	base := testPolicy()
	tests := []struct {
		name   string
		mutate func(*TrustPolicy)
	}{
		{name: "same key ID", mutate: func(value *TrustPolicy) { value.Approver.KeyID = value.Runner.KeyID }},
		{name: "same identity", mutate: func(value *TrustPolicy) { value.Approver.Identity = value.Runner.Identity }},
		{name: "same public key", mutate: func(value *TrustPolicy) {
			value.Approver.PublicKey = append(ed25519.PublicKey(nil), value.Runner.PublicKey...)
		}},
		{name: "missing runner key", mutate: func(value *TrustPolicy) { value.Runner.PublicKey = nil }},
		{name: "short approver key", mutate: func(value *TrustPolicy) { value.Approver.PublicKey = value.Approver.PublicKey[:8] }},
		{name: "invalid runner identity", mutate: func(value *TrustPolicy) { value.Runner.Identity = "runner identity" }},
		{name: "missing schema", mutate: func(value *TrustPolicy) { value.SchemaVersion = "" }},
		{name: "digest drift", mutate: func(value *TrustPolicy) { value.Digest = testDigest("other-keyful-policy") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := TrustPolicy{
				Digest: base.Digest, SchemaVersion: base.SchemaVersion,
				Runner:   SignerTrust{KeyID: base.Runner.KeyID, Identity: base.Runner.Identity, PublicKey: append(ed25519.PublicKey(nil), runner.public...)},
				Approver: SignerTrust{KeyID: base.Approver.KeyID, Identity: base.Approver.Identity, PublicKey: append(ed25519.PublicKey(nil), approver.public...)},
			}
			test.mutate(&policy)
			if _, err := NewVerifier(policy, &fakeExpectedResolver{}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("NewVerifier() error = %v", err)
			}
		})
	}
}

func TestExpectedResolverAndContextBoundaryFailClosed(t *testing.T) {
	receipt := validReceipt(t)
	compiled, err := Compile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	envelope := signCompiled(t, compiled)
	policy := testPolicy()
	if _, err := NewVerifier(policy, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil resolver error = %v", err)
	}
	var typedNilResolver *fakeExpectedResolver
	if _, err := NewVerifier(policy, typedNilResolver); !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed-nil resolver error = %v", err)
	}
	verifier, resolver := verifierForReceipt(t, receipt)
	if _, err := verifier.Verify(nil, envelope, receipt.PlanAuthority.AuthorityID, receipt.ReceiptID); !errors.Is(err, ErrVerification) {
		t.Fatalf("nil context error = %v", err)
	}
	var typedContext *typedNilContext
	if _, err := verifier.Verify(typedContext, envelope, receipt.PlanAuthority.AuthorityID, receipt.ReceiptID); !errors.Is(err, ErrVerification) {
		t.Fatalf("typed-nil context error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := verifier.Verify(cancelled, envelope, receipt.PlanAuthority.AuthorityID, receipt.ReceiptID); !errors.Is(err, ErrVerification) {
		t.Fatalf("cancelled context error = %v", err)
	}
	if _, err := verifier.Verify(context.Background(), envelope, "latest", receipt.ReceiptID); !errors.Is(err, ErrVerification) {
		t.Fatalf("invalid opaque authority error = %v", err)
	}
	if _, err := verifier.Verify(context.Background(), envelope, receipt.PlanAuthority.AuthorityID, ""); !errors.Is(err, ErrVerification) {
		t.Fatalf("invalid opaque Receipt error = %v", err)
	}
	if resolver.callCount() != 0 {
		t.Fatalf("resolver called for invalid preconditions: %d", resolver.callCount())
	}

	tests := []struct {
		name   string
		mutate func(*fakeExpectedResolver)
	}{
		{name: "resolve error", mutate: func(value *fakeExpectedResolver) { value.err = errors.New("authority store unavailable") }},
		{name: "authority ID drift", mutate: func(value *fakeExpectedResolver) { value.resolution.AuthorityID = testProjectID }},
		{name: "Receipt ID drift", mutate: func(value *fakeExpectedResolver) { value.resolution.ReceiptID = "other-receipt" }},
		{name: "payload digest drift", mutate: func(value *fakeExpectedResolver) { value.resolution.PayloadDigest = testDigest("other-payload") }},
		{name: "payload bytes drift", mutate: func(value *fakeExpectedResolver) { value.resolution.Payload = append(value.resolution.Payload, ' ') }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateResolver := resolverForReceipt(t, receipt)
			test.mutate(candidateResolver)
			candidateVerifier, err := NewVerifier(policy, candidateResolver)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := candidateVerifier.Verify(context.Background(), envelope, receipt.PlanAuthority.AuthorityID, receipt.ReceiptID); !errors.Is(err, ErrVerification) {
				t.Fatalf("resolver drift error = %v", err)
			}
			if candidateResolver.callCount() != 1 {
				t.Fatalf("resolver calls = %d, want 1", candidateResolver.callCount())
			}
		})
	}
}

func TestFrozenTrustPolicyDigestMustMatchActualKeyfulPolicy(t *testing.T) {
	receipt := validReceipt(t)
	receipt.Trust.TrustPolicyDigest = testDigest("different-keyful-policy")
	refreshAuthority(t, &receipt)
	compiled, err := Compile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	resolver := resolverForReceipt(t, receipt)
	verifier, err := NewVerifier(testPolicy(), resolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(context.Background(), signCompiled(t, compiled), receipt.PlanAuthority.AuthorityID, receipt.ReceiptID); !errors.Is(err, ErrVerification) {
		t.Fatalf("frozen/keyful trust policy drift error = %v", err)
	}
}

func TestVerifierCopiesPolicyAndIsConcurrent(t *testing.T) {
	receipt := validReceipt(t)
	compiled, err := Compile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	envelope := signCompiled(t, compiled)
	policy := testPolicy()
	resolver := resolverForReceipt(t, receipt)
	verifier, err := NewVerifier(policy, resolver)
	if err != nil {
		t.Fatal(err)
	}
	for index := range policy.Runner.PublicKey {
		policy.Runner.PublicKey[index] ^= 0xff
	}
	for index := range policy.Approver.PublicKey {
		policy.Approver.PublicKey[index] ^= 0xff
	}
	const workers = 64
	var wait sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			verified, err := verifier.Verify(context.Background(), envelope, receipt.PlanAuthority.AuthorityID, receipt.ReceiptID)
			if err == nil && verified.PayloadDigest != compiled.PayloadDigest {
				err = errors.New("payload digest drifted")
			}
			errorsChannel <- err
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	if resolver.callCount() != workers {
		t.Fatalf("resolver calls = %d, want %d", resolver.callCount(), workers)
	}
}
