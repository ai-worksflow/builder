package templates

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	actorID       = "11111111-1111-4111-8111-111111111111"
	evaluatorID   = "22222222-2222-4222-8222-222222222222"
	attemptID     = "33333333-3333-4333-8333-333333333333"
	releaseID     = "44444444-4444-4444-8444-444444444444"
	fullStackID   = "55555555-5555-4555-8555-555555555555"
	secondAttempt = "66666666-6666-4666-8666-666666666666"
	secondRelease = "77777777-7777-4777-8777-777777777777"
)

var baseTime = time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)

func TestAdmissionApprovesOnlyExactCompleteEvidence(t *testing.T) {
	attempt, err := NewAdmissionAttempt(attemptID, actorID, validCandidate("api-template", "api"), baseTime)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = attempt.BeginValidation(baseTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	subject := attempt.Snapshot().SubjectHash
	evidence := validEvidence(subject, baseTime.Add(2*time.Minute))
	attempt, release, err := attempt.Complete(
		releaseID,
		evidence,
		validSignature(subject, baseTime.Add(2*time.Minute)),
		evaluatorID,
		baseTime.Add(3*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if release == nil {
		t.Fatal("complete required evidence did not create a release")
	}
	snapshot := attempt.Snapshot()
	if snapshot.Status != AttemptApproved || snapshot.ApprovedReleaseID != releaseID || len(snapshot.Findings) != 0 {
		t.Fatalf("unexpected approved attempt: %#v", snapshot)
	}
	if !digestPattern.MatchString(release.ContentHash()) || !digestPattern.MatchString(release.SubjectHash()) {
		t.Fatalf("release commitments are not canonical: %#v", release.Snapshot())
	}
	encoded, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseTemplateRelease(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ContentHash() != release.ContentHash() || parsed.ID() != release.ID() {
		t.Fatal("release did not survive strict immutable hydration")
	}
	var inPlace TemplateRelease
	if err := json.Unmarshal(encoded, &inPlace); !errors.Is(err, ErrImmutableRelease) {
		t.Fatalf("in-place release hydration was not rejected: %v", err)
	}

	copy := release.Snapshot()
	copy.Manifest.DisplayName = "mutated caller copy"
	if release.Snapshot().Manifest.DisplayName == copy.Manifest.DisplayName {
		t.Fatal("release snapshot exposed mutable internal state")
	}
}

func TestAuthorityAdmissionCreatesOnlyExactV2ReleaseAndPolicyLineage(t *testing.T) {
	candidate := validCandidate("authority-api-template", "api")
	attempt, err := NewAuthorityAdmissionAttempt(attemptID, actorID, candidate, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = attempt.BeginValidation(baseTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	input := writerAdmissionInput(attemptID, releaseID, candidate)
	authority := &fakeArtifactAuthority{}
	receipt, err := authority.Verify(context.Background(), ArtifactAuthorityVerifyRequest{
		Candidate: candidate, SubjectHash: attempt.Snapshot().SubjectHash,
		Bundle: input.Bundle, RecordedBy: evaluatorID,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, release, err := attempt.CompleteWithAuthority(releaseID, receipt, evaluatorID, baseTime.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if release == nil {
		t.Fatal("passed authority receipt did not create a release")
	}
	policy, err := NewReleasePolicy(*release, evaluatorID, baseTime.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	attemptView := completed.Snapshot()
	releaseView := release.Snapshot()
	exact := receipt.Ref()
	if attemptView.SchemaVersion != AdmissionAttemptSchemaVersionV2 || releaseView.SchemaVersion != TemplateReleaseSchemaVersionV2 ||
		policy.SchemaVersion != ReleasePolicySchemaVersionV2 {
		t.Fatalf("authority lineage downgraded schemas: %s/%s/%s", attemptView.SchemaVersion, releaseView.SchemaVersion, policy.SchemaVersion)
	}
	if attemptView.AuthorityReceipt == nil || releaseView.AuthorityReceipt == nil || policy.AuthorityReceipt == nil ||
		*attemptView.AuthorityReceipt != exact || *releaseView.AuthorityReceipt != exact || *policy.AuthorityReceipt != exact {
		t.Fatalf("authority receipt exact reference drifted: receipt=%#v attempt=%#v release=%#v policy=%#v",
			exact, attemptView.AuthorityReceipt, releaseView.AuthorityReceipt, policy.AuthorityReceipt)
	}

	canonical, err := receipt.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseArtifactAuthorityReceipt(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Ref() != exact {
		t.Fatalf("authority receipt changed during strict hydration: %#v != %#v", parsed.Ref(), exact)
	}
	var inPlace ArtifactAuthorityReceipt
	if err := json.Unmarshal(canonical, &inPlace); !errors.Is(err, ErrImmutableRelease) {
		t.Fatalf("in-place authority receipt hydration was not rejected: %v", err)
	}

	if _, _, err := attempt.Complete(
		releaseID, validEvidence(attemptView.SubjectHash, baseTime.Add(2*time.Minute)),
		validSignature(attemptView.SubjectHash, baseTime.Add(2*time.Minute)), evaluatorID, baseTime.Add(3*time.Minute),
	); !errors.Is(err, ErrAdmissionRejected) {
		t.Fatalf("v2 attempt accepted caller-shaped evidence instead of an authority receipt: %v", err)
	}
}

func TestArtifactAuthorityReceiptRejectsTypedDescriptorAndProofDrift(t *testing.T) {
	candidate := validCandidate("typed-receipt-template", "api")
	attempt, err := NewAuthorityAdmissionAttempt(attemptID, actorID, candidate, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	input := writerAdmissionInput(attemptID, releaseID, candidate)
	receipt, err := (&fakeArtifactAuthority{}).Verify(context.Background(), ArtifactAuthorityVerifyRequest{
		Candidate: candidate, SubjectHash: attempt.Snapshot().SubjectHash,
		Bundle: input.Bundle, RecordedBy: evaluatorID,
	})
	if err != nil {
		t.Fatal(err)
	}
	view := receipt.Snapshot()
	if view.ArtifactDescriptor.Digest != view.ArtifactDigest || view.ArtifactDescriptor.TotalBytes != 150 ||
		view.SBOMDescriptor.Digest != view.SBOMDigest || view.SBOMDescriptor.ServiceCount != 1 ||
		view.Proof.SignatureBundleDigest != view.SignatureBundleDigest ||
		view.Proof.TransparencyBundleDigest == "" || view.Proof.TreeSize <= uint64(view.Proof.LogIndex) || view.Proof.RootHash == "" {
		t.Fatalf("deterministic authority fixture is missing typed verified material: %#v", view)
	}
	canonical, err := receipt.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		code string
		edit func(map[string]any)
	}{
		{
			name: "artifact manifest identity",
			code: "invalid_artifact_descriptor",
			edit: func(document map[string]any) {
				document["artifactDescriptor"].(map[string]any)["digest"] = digest("d")
			},
		},
		{
			name: "SBOM aggregate identity",
			code: "invalid_sbom_descriptor",
			edit: func(document map[string]any) {
				document["sbomDescriptor"].(map[string]any)["digest"] = digest("d")
			},
		},
		{
			name: "transparency tree coordinates",
			code: "invalid_authority_proof",
			edit: func(document map[string]any) {
				document["proof"].(map[string]any)["treeSize"] = float64(0)
			},
		},
		{
			name: "transparency root commitment",
			code: "authority_receipt_content_mismatch",
			edit: func(document map[string]any) {
				document["proof"].(map[string]any)["rootHash"] = digest("d")
			},
		},
		{
			name: "transparency bundle commitment",
			code: "authority_receipt_content_mismatch",
			edit: func(document map[string]any) {
				document["proof"].(map[string]any)["transparencyBundleDigest"] = digest("d")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal(canonical, &document); err != nil {
				t.Fatal(err)
			}
			test.edit(document)
			drifted, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ParseArtifactAuthorityReceipt(drifted)
			var domainError *Error
			if !errors.As(err, &domainError) || domainError.Code != test.code {
				t.Fatalf("typed receipt drift did not fail with %s: %v", test.code, err)
			}
		})
	}
}

func TestAdmissionFailsClosedForMissingFailedAndMismatchedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]GateEvidence) []GateEvidence
		code   string
	}{
		{
			name: "missing",
			mutate: func(values []GateEvidence) []GateEvidence {
				return values[:len(values)-1]
			},
			code: "required_gate_missing",
		},
		{
			name: "failed",
			mutate: func(values []GateEvidence) []GateEvidence {
				values[0].Outcome = EvidenceFailed
				return values
			},
			code: "required_gate_failed",
		},
		{
			name: "wrong subject",
			mutate: func(values []GateEvidence) []GateEvidence {
				values[0].SubjectHash = digest("9")
				return values
			},
			code: "evidence_subject_mismatch",
		},
		{
			name: "duplicate",
			mutate: func(values []GateEvidence) []GateEvidence {
				return append(values, values[0])
			},
			code: "duplicate_gate_evidence",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attempt, err := NewAdmissionAttempt(attemptID, actorID, validCandidate("api-template", "api"), baseTime)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err = attempt.BeginValidation(baseTime.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			subject := attempt.Snapshot().SubjectHash
			result, release, err := attempt.Complete(
				releaseID,
				test.mutate(validEvidence(subject, baseTime.Add(2*time.Minute))),
				validSignature(subject, baseTime.Add(2*time.Minute)),
				evaluatorID,
				baseTime.Add(3*time.Minute),
			)
			if err != nil {
				t.Fatal(err)
			}
			if release != nil || result.Snapshot().Status != AttemptRejected {
				t.Fatalf("invalid evidence escaped fail-closed admission: release=%v status=%s", release != nil, result.Snapshot().Status)
			}
			if !hasFinding(result.Snapshot().Findings, test.code) {
				t.Fatalf("expected finding %q, got %#v", test.code, result.Snapshot().Findings)
			}
		})
	}
}

func TestAdmissionRejectsInvalidSignatureDespitePassedSignatureGate(t *testing.T) {
	attempt, err := NewAdmissionAttempt(attemptID, actorID, validCandidate("api-template", "api"), baseTime)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ = attempt.BeginValidation(baseTime.Add(time.Minute))
	subject := attempt.Snapshot().SubjectHash
	signature := validSignature(subject, baseTime.Add(2*time.Minute))
	signature.SubjectHash = digest("8")
	result, release, err := attempt.Complete(releaseID, validEvidence(subject, baseTime.Add(2*time.Minute)), signature, evaluatorID, baseTime.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if release != nil || !hasFinding(result.Snapshot().Findings, "signature_subject_mismatch") {
		t.Fatalf("mismatched signature was not rejected: %#v", result.Snapshot())
	}
}

func TestCandidateStaticValidationRejectsUnpinnedOrUnsafeInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AdmissionCandidate)
	}{
		{"branch tip as commit", func(value *AdmissionCandidate) { value.Source.Commit = "main" }},
		{"repository credentials", func(value *AdmissionCandidate) {
			value.Source.Repository = "https://user:secret@github.com/ai-worksflow/templates.git"
		}},
		{"literal repository IP", func(value *AdmissionCandidate) { value.Source.Repository = "https://43.216.1.58/templates.git" }},
		{"missing lock", func(value *AdmissionCandidate) { value.Manifest.Lockfiles = nil }},
		{"unpinned toolchain", func(value *AdmissionCandidate) { value.Manifest.Toolchains[0].Image = "node:22" }},
		{"shell command", func(value *AdmissionCandidate) {
			value.Manifest.Commands["build"] = Command{WorkingDirectory: ".", Argv: []string{"sh", "-c", "npm run build"}}
		}},
		{"secret default", func(value *AdmissionCandidate) {
			secret := "committed-secret"
			value.Manifest.EnvironmentSchema = []EnvironmentVariable{{Name: "API_KEY", Required: true, Secret: true, Description: "provider key", Default: &secret}}
		}},
		{"overlapping protected path", func(value *AdmissionCandidate) { value.Manifest.ProtectedPaths = []string{"src/generated"} }},
		{"no concrete license", func(value *AdmissionCandidate) { value.LicenseExpression = "NOASSERTION" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validCandidate("api-template", "api")
			test.mutate(&candidate)
			if _, err := NewAdmissionAttempt(attemptID, actorID, candidate, baseTime); !errors.Is(err, ErrInvalidTemplate) {
				t.Fatalf("expected invalid template error, got %v", err)
			}
		})
	}
}

func TestCanonicalReleaseHashIsIndependentOfSetOrdering(t *testing.T) {
	candidate := validCandidate("api-template", "api")
	candidate.Manifest.ExtensionPaths = []string{"tests", "src"}
	candidate.Manifest.ProtectedPaths = []string{"templates.lock.json", "deployment"}
	first := approvedRelease(t, attemptID, releaseID, candidate, false)
	candidate.Manifest.ExtensionPaths = slices.Clone(candidate.Manifest.ExtensionPaths)
	slices.Reverse(candidate.Manifest.ExtensionPaths)
	candidate.Manifest.ProtectedPaths = slices.Clone(candidate.Manifest.ProtectedPaths)
	slices.Reverse(candidate.Manifest.ProtectedPaths)
	second := approvedRelease(t, attemptID, releaseID, candidate, true)
	if first.ContentHash() != second.ContentHash() {
		t.Fatalf("set ordering changed release commitment: %s != %s", first.ContentHash(), second.ContentHash())
	}
}

func TestTemplateReleaseParserRejectsTamperingAndUnknownFields(t *testing.T) {
	release := approvedRelease(t, attemptID, releaseID, validCandidate("api-template", "api"), false)
	encoded, _ := json.Marshal(release)
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	document["unexpected"] = true
	unknown, _ := json.Marshal(document)
	if _, err := ParseTemplateRelease(unknown); err == nil {
		t.Fatal("unknown release field was accepted")
	}
	delete(document, "unexpected")
	manifest := document["manifest"].(map[string]any)
	manifest["displayName"] = "tampered"
	tampered, _ := json.Marshal(document)
	if _, err := ParseTemplateRelease(tampered); err == nil {
		t.Fatal("release payload changed without a matching commitment")
	}
}

func TestReleasePolicyIsIndependentAndFailClosed(t *testing.T) {
	release := approvedRelease(t, attemptID, releaseID, validCandidate("api-template", "api"), false)
	policy, err := NewReleasePolicy(release, evaluatorID, baseTime.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if policy.AllowsNewProjects() || policy.AllowsBuilds() {
		t.Fatal("legacy v1 policy became selectable without an Artifact Authority receipt")
	}
	deprecated, err := policy.Transition(1, ReleaseDeprecated, "superseded by a patched template", actorID, baseTime.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if deprecated.AllowsNewProjects() || deprecated.AllowsBuilds() {
		t.Fatal("legacy deprecated policy became buildable without an Artifact Authority receipt")
	}
	if _, err := deprecated.Transition(2, ReleaseApproved, "reactivate", actorID, baseTime.Add(6*time.Minute)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("deprecated release was silently reactivated: %v", err)
	}
	revoked, err := policy.Transition(1, ReleaseRevoked, "critical supply-chain incident", actorID, baseTime.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if revoked.AllowsNewProjects() || revoked.AllowsBuilds() {
		t.Fatal("revoked release still permits selection or builds")
	}
}

func TestFullStackTemplatePinsExactWebAndAPIReleases(t *testing.T) {
	apiRelease := approvedRelease(t, attemptID, releaseID, validCandidate("api-template", "api"), false)
	webRelease := approvedRelease(t, secondAttempt, secondRelease, validCandidate("web-template", "web"), false)
	stack, err := NewFullStackTemplate(
		fullStackID,
		"ai-conversation-stack",
		"1.0.0",
		[]FullStackComponentInput{
			{Role: "web", MountPath: "apps/web", Release: webRelease},
			{Role: "api", MountPath: "services/api", Release: apiRelease},
		},
		FullStackLayout{
			ContractTruthSource: "openapi",
			OpenAPIPath:         "contracts/openapi.yaml",
			GeneratedClientPath: "packages/api-client",
			DeploymentPath:      "deployment",
			TestPath:            "tests",
			DatabaseEngine:      "postgresql",
		},
		actorID,
		baseTime.Add(10*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	view := stack.Snapshot()
	if len(view.Components) != 2 || view.Components[0].Role != "api" || view.Components[1].Role != "web" {
		t.Fatalf("components were not canonicalized: %#v", view.Components)
	}
	if view.Components[0].Release.ContentHash != apiRelease.ContentHash() {
		t.Fatal("full-stack template did not pin exact release content")
	}
	encoded, _ := json.Marshal(stack)
	parsed, err := ParseFullStackTemplate(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ContentHash() != stack.ContentHash() {
		t.Fatal("full-stack template commitment changed after hydration")
	}

	_, err = NewFullStackTemplate(
		fullStackID, "invalid-stack", "1.0.0",
		[]FullStackComponentInput{
			{Role: "web", MountPath: "apps/web", Release: webRelease},
			{Role: "api", MountPath: "apps/web/api", Release: apiRelease},
		},
		view.Layout, actorID, baseTime.Add(11*time.Minute),
	)
	if err == nil {
		t.Fatal("overlapping component mounts were accepted")
	}
}

func approvedRelease(t *testing.T, attemptIDValue, releaseIDValue string, candidate AdmissionCandidate, reverseEvidence bool) TemplateRelease {
	t.Helper()
	attempt, err := NewAdmissionAttempt(attemptIDValue, actorID, candidate, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = attempt.BeginValidation(baseTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	subject := attempt.Snapshot().SubjectHash
	evidence := validEvidence(subject, baseTime.Add(2*time.Minute))
	if reverseEvidence {
		slices.Reverse(evidence)
	}
	_, release, err := attempt.Complete(releaseIDValue, evidence, validSignature(subject, baseTime.Add(2*time.Minute)), evaluatorID, baseTime.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if release == nil {
		t.Fatal("valid release helper was rejected")
	}
	return *release
}

func validCandidate(templateID, serviceKind string) AdmissionCandidate {
	serviceID := serviceKind
	portName := serviceKind + "-http"
	return AdmissionCandidate{
		Source: TemplateSource{
			Repository: "https://github.com/ai-worksflow/templates.git",
			Branch:     templateID,
			Commit:     strings.Repeat("a", 40),
			TreeHash:   digest("b"),
		},
		Manifest: TemplateManifest{
			SchemaVersion: TemplateManifestSchemaVersion,
			TemplateID:    templateID,
			DisplayName:   templateID,
			Version:       "1.0.0",
			Services:      []TemplateService{{ID: serviceID, Kind: serviceKind, RootPath: "."}},
			Toolchains:    []Toolchain{{Name: "runtime", Version: "22.0.0", Image: "ghcr.io/worksflow/runtime@" + digest("c")}},
			Commands: map[string]Command{
				"install":   {WorkingDirectory: ".", Argv: []string{"pnpm", "install", "--frozen-lockfile"}},
				"lint":      {WorkingDirectory: ".", Argv: []string{"pnpm", "lint"}},
				"typecheck": {WorkingDirectory: ".", Argv: []string{"pnpm", "typecheck"}},
				"test":      {WorkingDirectory: ".", Argv: []string{"pnpm", "test"}},
				"build":     {WorkingDirectory: ".", Argv: []string{"pnpm", "build"}},
				"start":     {WorkingDirectory: ".", Argv: []string{"pnpm", "start"}},
			},
			Ports:             []Port{{Name: portName, ServiceID: serviceID, Number: 3000, Protocol: "http", Exposure: "preview"}},
			HealthChecks:      []HealthCheck{{ID: serviceKind + "-health", ServiceID: serviceID, PortName: portName, Path: "/health"}},
			BuildOutputs:      []BuildOutput{{ServiceID: serviceID, Path: "dist"}},
			ExtensionPaths:    []string{"src"},
			ProtectedPaths:    []string{"templates.lock.json"},
			EnvironmentSchema: []EnvironmentVariable{{Name: "PORT", Required: true, Description: "service port"}},
			Lockfiles:         []Lockfile{{Path: "pnpm-lock.yaml", Digest: digest("d"), Registry: "https://registry.npmjs.org"}},
			ProfileDigest:     digest("e"),
		},
		SBOMDigest:        digest("f"),
		LicenseExpression: "Apache-2.0",
		LicenseDigest:     digest("1"),
	}
}

func validEvidence(subject string, observedAt time.Time) []GateEvidence {
	result := make([]GateEvidence, 0, len(requiredAdmissionGates))
	for _, gate := range requiredAdmissionGates {
		result = append(result, GateEvidence{
			Gate:         gate,
			Outcome:      EvidencePassed,
			SubjectHash:  subject,
			Digest:       digest("2"),
			Reference:    "urn:evidence:" + string(gate),
			Producer:     "template-admission/v1",
			InvocationID: "invocation-" + string(gate),
			ObservedAt:   observedAt,
		})
	}
	return result
}

func validSignature(subject string, signedAt time.Time) SignatureEnvelope {
	return SignatureEnvelope{
		Format:             "dsse",
		SubjectHash:        subject,
		BundleDigest:       digest("3"),
		Signer:             "https://github.com/ai-worksflow/templates/.github/workflows/admit.yml@refs/heads/main",
		TransparencyLogRef: "urn:rekor:entry:1234",
		SignedAt:           signedAt,
	}
}

func digest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func hasFinding(findings []AdmissionFinding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
